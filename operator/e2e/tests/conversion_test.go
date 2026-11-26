// Copyright 2026 The Grove Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build e2e

package tests

import (
	"context"
	"slices"
	"testing"

	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	corev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"

	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// convertedCRDName is the only grove.io CRD whose versions differ in schema, so the only one converted by the
// operator's conversion webhook.
const convertedCRDName = "podcliquesets.grove.io"

// Test_CONV1_GroveCRDVersions verifies that every grove.io CRD serves the deprecated v1alpha1 and stores
// v1beta1, that no object is stored at v1alpha1 any more, and that only PodCliqueSets use the conversion
// webhook.
func Test_CONV1_GroveCRDVersions(t *testing.T) {
	ctx := context.Background()
	k8sClient := setup.SharedCluster(Logger).GetClient()

	for _, name := range groveCRDNames {
		if name == "podgangs.scheduler.grove.io" {
			continue
		}
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(customResourceDefinition)
		require.NoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: name}, u), "get CRD %s", name)
		crd := &apiextensionsv1.CustomResourceDefinition{}
		require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, crd))

		versions := map[string]apiextensionsv1.CustomResourceDefinitionVersion{}
		for _, v := range crd.Spec.Versions {
			versions[v.Name] = v
		}
		alpha, ok := versions[corev1alpha1.SchemeGroupVersion.Version]
		require.True(t, ok, "CRD %s must keep serving v1alpha1", name)
		require.True(t, alpha.Served && alpha.Deprecated && !alpha.Storage, "CRD %s v1alpha1 must be served, deprecated and not stored", name)
		beta, ok := versions[corev1beta1.SchemeGroupVersion.Version]
		require.True(t, ok, "CRD %s must serve v1beta1", name)
		require.True(t, beta.Served && beta.Storage, "CRD %s v1beta1 must be served and stored", name)
		require.Equal(t, []string{corev1beta1.SchemeGroupVersion.Version}, crd.Status.StoredVersions, "CRD %s storedVersions", name)

		webhook := crd.Spec.Conversion != nil && crd.Spec.Conversion.Strategy == apiextensionsv1.WebhookConverter
		require.Equal(t, name == convertedCRDName, webhook, "CRD %s conversion strategy", name)
	}
}

// Test_CONV2_GroveObjectsThroughBothVersions verifies that a workload created through grove.io/v1beta1 reads
// the same through grove.io/v1alpha1, and that a write through v1alpha1 is visible through v1beta1 without
// changing the spec.
func Test_CONV2_GroveObjectsThroughBothVersions(t *testing.T) {
	ctx := context.Background()
	workloadConfig := &testctx.WorkloadConfig{
		Name:         "workload1",
		YAMLPath:     "../yaml/workload1.yaml",
		Namespace:    "default",
		ExpectedPods: 10,
	}
	tc, cleanup := testctx.PrepareTest(ctx, t, workloadConfig.ExpectedPods, testctx.WithWorkload(workloadConfig))
	defer cleanup()

	Logger.Info("1. Deploy the workload through v1beta1")
	_, err := tc.DeployAndVerifyWorkload()
	require.NoError(t, err, "deploy workload")
	key := types.NamespacedName{Namespace: workloadConfig.Namespace, Name: workloadConfig.Name}

	Logger.Info("2. The PodCliqueSet reads the same through v1alpha1 and v1beta1")
	alpha := &corev1alpha1.PodCliqueSet{}
	require.NoError(t, tc.Client.Get(ctx, key, alpha), "get PodCliqueSet at v1alpha1")
	beta := &corev1beta1.PodCliqueSet{}
	require.NoError(t, tc.Client.Get(ctx, key, beta), "get PodCliqueSet at v1beta1")
	converted := &corev1beta1.PodCliqueSet{}
	require.NoError(t, corev1alpha1.Convert_v1alpha1_PodCliqueSet_To_v1beta1_PodCliqueSet(alpha, converted, nil))
	require.True(t, equality.Semantic.DeepEqual(converted.Spec, beta.Spec), "PodCliqueSet spec differs between versions")

	Logger.Info("3. A v1alpha1 write is visible through v1beta1 and keeps the spec")
	patch := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"annotations":{"e2e.grove.io/written-at":"v1alpha1"}}}`))
	require.NoError(t, tc.Client.Patch(ctx, alpha, patch), "patch PodCliqueSet at v1alpha1")
	afterWrite := &corev1beta1.PodCliqueSet{}
	require.NoError(t, tc.Client.Get(ctx, key, afterWrite), "get PodCliqueSet at v1beta1")
	require.Equal(t, "v1alpha1", afterWrite.Annotations["e2e.grove.io/written-at"])
	require.True(t, equality.Semantic.DeepEqual(beta.Spec, afterWrite.Spec), "a v1alpha1 write changed the spec")

	Logger.Info("4. PodCliques and PodCliqueScalingGroups list the same through both versions")
	inNamespace := client.InNamespace(workloadConfig.Namespace)
	alphaPCLQs, betaPCLQs := &corev1alpha1.PodCliqueList{}, &corev1beta1.PodCliqueList{}
	require.NoError(t, tc.Client.List(ctx, alphaPCLQs, inNamespace))
	require.NoError(t, tc.Client.List(ctx, betaPCLQs, inNamespace))
	require.NotEmpty(t, betaPCLQs.Items)
	require.ElementsMatch(t, names(alphaPCLQs.Items), names(betaPCLQs.Items))
	alphaPCSGs, betaPCSGs := &corev1alpha1.PodCliqueScalingGroupList{}, &corev1beta1.PodCliqueScalingGroupList{}
	require.NoError(t, tc.Client.List(ctx, alphaPCSGs, inNamespace))
	require.NoError(t, tc.Client.List(ctx, betaPCSGs, inNamespace))
	require.ElementsMatch(t, names(alphaPCSGs.Items), names(betaPCSGs.Items))
}

// names returns the names of the objects, for comparing lists read at different versions.
func names[T any, PT interface {
	*T
	client.Object
}](items []T) []string {
	result := make([]string, 0, len(items))
	for i := range items {
		result = append(result, PT(&items[i]).GetName())
	}
	slices.Sort(result)
	return result
}

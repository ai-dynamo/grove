//go:build e2e && e2eupgrade

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

package upgrade

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	corev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// groveCRDNames are the grove.io CRDs whose storage version changes between v0.1.x and this release.
var groveCRDNames = []string{
	"podcliquesets.grove.io",
	"podcliques.grove.io",
	"podcliquescalinggroups.grove.io",
	"clustertopologybindings.grove.io",
	"podgangmaps.grove.io",
}

var crdGVK = apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition")

// getCRD reads a CRD through an unstructured object, since the e2e scheme has no apiextensions types.
func getCRD(ctx context.Context, cl client.Client, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(crdGVK)
	if err := cl.Get(ctx, types.NamespacedName{Name: name}, u); err != nil {
		return nil, err
	}
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, crd); err != nil {
		return nil, err
	}
	return crd, nil
}

// deleteGroveCRDs deletes the grove.io CRDs left by an earlier test, so that installing a released chart
// creates the CRDs of that release. Helm never replaces CRDs that already exist.
func deleteGroveCRDs(t *testing.T, tc *testctx.TestContext) {
	t.Helper()
	for _, name := range groveCRDNames {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(crdGVK)
		u.SetName(name)
		if err := tc.Client.Delete(t.Context(), u); err != nil && !apierrors.IsNotFound(err) {
			require.NoError(t, err, "delete CRD %s", name)
		}
	}
	require.NoError(t, wait.PollUntilContextTimeout(t.Context(), defaultPollInterval, defaultPollTimeout, true, func(ctx context.Context) (bool, error) {
		for _, name := range groveCRDNames {
			if _, err := getCRD(ctx, tc.Client, name); !apierrors.IsNotFound(err) {
				return false, nil
			}
		}
		return true, nil
	}), "waiting for the grove.io CRDs to be deleted")
}

// waitForStoredVersions waits until every grove.io CRD lists only version in status.storedVersions, which
// happens once all its objects are stored at version.
func waitForStoredVersions(t *testing.T, tc *testctx.TestContext, version string) {
	t.Helper()
	testctx.Logger.Infof("waiting for the grove.io CRDs to store only %s", version)
	var last map[string][]string
	err := wait.PollUntilContextTimeout(t.Context(), defaultPollInterval, defaultPollTimeout, true, func(ctx context.Context) (bool, error) {
		last = map[string][]string{}
		done := true
		for _, name := range groveCRDNames {
			crd, err := getCRD(ctx, tc.Client, name)
			if err != nil {
				return false, err
			}
			last[name] = crd.Status.StoredVersions
			done = done && slices.Equal(crd.Status.StoredVersions, []string{version})
		}
		return done, nil
	})
	require.NoError(t, err, "storedVersions of the grove.io CRDs are %v, want [%s]", last, version)
}

// requireCRDsServeOnly checks that every grove.io CRD serves and stores version, and no other version.
func requireCRDsServeOnly(t *testing.T, tc *testctx.TestContext, version string) {
	t.Helper()
	for _, name := range groveCRDNames {
		crd, err := getCRD(t.Context(), tc.Client, name)
		require.NoError(t, err, "get CRD %s", name)
		require.Len(t, crd.Spec.Versions, 1, "CRD %s versions", name)
		require.Equal(t, version, crd.Spec.Versions[0].Name, "CRD %s version", name)
		require.True(t, crd.Spec.Versions[0].Served && crd.Spec.Versions[0].Storage, "CRD %s must serve and store %s", name, version)
		require.Equal(t, []string{version}, crd.Status.StoredVersions, "CRD %s storedVersions", name)
	}
}

// requirePCSReadableAtBothVersions reads the PodCliqueSet through grove.io/v1alpha1 and grove.io/v1beta1 and
// checks that both describe the same spec.
func requirePCSReadableAtBothVersions(t *testing.T, tc *testctx.TestContext, key types.NamespacedName) {
	t.Helper()
	alpha := &corev1alpha1.PodCliqueSet{}
	require.NoError(t, tc.Client.Get(t.Context(), key, alpha), "get PodCliqueSet %s at v1alpha1", key)
	beta := &corev1beta1.PodCliqueSet{}
	require.NoError(t, tc.Client.Get(t.Context(), key, beta), "get PodCliqueSet %s at v1beta1", key)

	converted := &corev1beta1.PodCliqueSet{}
	require.NoError(t, corev1alpha1.Convert_v1alpha1_PodCliqueSet_To_v1beta1_PodCliqueSet(alpha, converted, nil))
	require.True(t, equality.Semantic.DeepEqual(converted.Spec, beta.Spec),
		"PodCliqueSet %s differs between v1alpha1 and v1beta1", key)
}

// runPrepareDowngrade runs grove-install-crds prepare-downgrade from the current checkout against the test
// cluster, the way a user prepares a downgrade.
func runPrepareDowngrade(t *testing.T) {
	t.Helper()
	rootDir, err := setup.GetOperatorRootDir()
	require.NoError(t, err, "locate current root directory")

	// The test cluster is the one in the kubeconfig. Without an explicit --kubeconfig, a run inside a Pod, such as a
	// CI runner, would prefer the in-cluster config of that Pod.
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, err := os.UserHomeDir()
		require.NoError(t, err, "locate the home directory")
		kubeconfig = filepath.Join(home, ".kube", "config")
	}

	testctx.Logger.Info("running grove-install-crds prepare-downgrade")
	cmd := exec.CommandContext(t.Context(), "go", "run", "./cmd/install-crds", "--kubeconfig", kubeconfig, "prepare-downgrade")
	cmd.Dir = rootDir
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	testctx.Logger.Infof("prepare-downgrade output:\n%s", output)
	require.NoError(t, err, "prepare-downgrade failed")
}

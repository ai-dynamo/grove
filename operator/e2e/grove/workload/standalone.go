//go:build e2e

// /*
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
// */

package workload

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveclient "github.com/ai-dynamo/grove/operator/client/clientset/versioned"
	"github.com/ai-dynamo/grove/operator/e2e/k8s"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// PodCliqueSetGVR is the GroupVersionResource for PodCliqueSet.
var PodCliqueSetGVR = schema.GroupVersionResource{
	Group:    "grove.io",
	Version:  "v1alpha1",
	Resource: "podcliquesets",
}

// GetPodCliqueSet returns a typed PodCliqueSet by name from the given namespace.
func GetPodCliqueSet(ctx context.Context, dynamicClient dynamic.Interface, workloadName, namespace string) (*grovecorev1alpha1.PodCliqueSet, error) {
	unstructuredPCS, err := dynamicClient.Resource(PodCliqueSetGVR).Namespace(namespace).Get(ctx, workloadName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get PodCliqueSet: %w", err)
	}

	var pcs grovecorev1alpha1.PodCliqueSet
	if err := k8s.ConvertUnstructuredToTyped(unstructuredPCS.Object, &pcs); err != nil {
		return nil, fmt.Errorf("failed to convert to PodCliqueSet: %w", err)
	}

	return &pcs, nil
}

// IsOnDeleteUpdateComplete returns true if the update of the PodCliqueSet has ended.
// It should be used for PodCliqueSets with UpdateStrategy set to OnDelete.
func IsOnDeleteUpdateComplete(pcs *grovecorev1alpha1.PodCliqueSet) bool {
	return pcs.Status.UpdateProgress != nil && pcs.Status.UpdateProgress.UpdateEndedAt != nil
}

// GetPCSUpdateProgress returns the UpdateProgress of the PodCliqueSet.
func GetPCSUpdateProgress(ctx context.Context, t *testing.T, dynamicClient dynamic.Interface, workloadName, namespace string) (*grovecorev1alpha1.PodCliqueSetUpdateProgress, error) {
	t.Helper()

	pcs, err := GetPodCliqueSet(ctx, dynamicClient, workloadName, namespace)
	if err != nil {
		return nil, err
	}

	return pcs.Status.UpdateProgress, nil
}

// DeletePodCliqueSet deletes a PodCliqueSet by name.
// NotFound errors are ignored; other errors are returned.
func DeletePodCliqueSet(ctx context.Context, dynamicClient dynamic.Interface, namespace, name string) error {
	err := dynamicClient.Resource(PodCliqueSetGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete PodCliqueSet %s/%s: %w", namespace, name, err)
	}
	return nil
}

// ScalePodCliqueSetWithClient scales a PodCliqueSet using an existing dynamic client.
func ScalePodCliqueSetWithClient(ctx context.Context, dynamicClient dynamic.Interface, namespace, name string, replicas int) error {
	return scaleCRDStandalone(ctx, dynamicClient, PodCliqueSetGVR, namespace, name, replicas)
}

// WaitForPodCliqueScalingGroup polls until a PodCliqueScalingGroup exists and returns it.
func WaitForPodCliqueScalingGroup(ctx context.Context, groveClient groveclient.Interface, namespace, name string, timeout, interval time.Duration) (*grovecorev1alpha1.PodCliqueScalingGroup, error) {
	w := waiter.New[*grovecorev1alpha1.PodCliqueScalingGroup]().
		WithTimeout(timeout).
		WithInterval(interval)
	return waiter.WaitForResource(ctx, w, name, groveClient.GroveV1alpha1().PodCliqueScalingGroups(namespace).Get)
}

// WaitForPodCliqueStandalone polls until a PodClique exists and returns it.
func WaitForPodCliqueStandalone(ctx context.Context, groveClient groveclient.Interface, namespace, name string, timeout, interval time.Duration) (*grovecorev1alpha1.PodClique, error) {
	w := waiter.New[*grovecorev1alpha1.PodClique]().
		WithTimeout(timeout).
		WithInterval(interval)
	return waiter.WaitForResource(ctx, w, name, groveClient.GroveV1alpha1().PodCliques(namespace).Get)
}

// WaitForPodCliqueSetDeletion polls until a PodCliqueSet no longer exists.
func WaitForPodCliqueSetDeletion(ctx context.Context, groveClient groveclient.Interface, namespace, name string, timeout, interval time.Duration) error {
	w := waiter.New[*grovecorev1alpha1.PodCliqueSet]().
		WithTimeout(timeout).
		WithInterval(interval)
	return waiter.WaitForResourceDeletion(ctx, w, name, groveClient.GroveV1alpha1().PodCliqueSets(namespace).Get)
}

func scaleCRDStandalone(ctx context.Context, dynamicClient dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string, replicas int) error {
	scalePatch := map[string]interface{}{
		"spec": map[string]interface{}{
			"replicas": replicas,
		},
	}
	patchBytes, err := json.Marshal(scalePatch)
	if err != nil {
		return fmt.Errorf("failed to marshal scale patch: %w", err)
	}

	if _, err := dynamicClient.Resource(gvr).Namespace(namespace).Patch(ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("failed to scale %s %s: %w", gvr.Resource, name, err)
	}

	return nil
}

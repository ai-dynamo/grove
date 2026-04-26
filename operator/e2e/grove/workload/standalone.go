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
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/k8sclient"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// IsOnDeleteUpdateComplete returns true if the update of the PodCliqueSet has ended.
// It should be used for PodCliqueSets with UpdateStrategy set to OnDelete.
func IsOnDeleteUpdateComplete(pcs *grovecorev1alpha1.PodCliqueSet) bool {
	return pcs.Status.UpdateProgress != nil && pcs.Status.UpdateProgress.UpdateEndedAt != nil
}

// ScalePodCliqueSetWithClient scales a PodCliqueSet using client.Client.
func ScalePodCliqueSetWithClient(ctx context.Context, cl client.Client, namespace, name string, replicas int) error {
	scalePatch := map[string]interface{}{
		"spec": map[string]interface{}{
			"replicas": replicas,
		},
	}
	patchBytes, err := json.Marshal(scalePatch)
	if err != nil {
		return fmt.Errorf("failed to marshal scale patch: %w", err)
	}

	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
	return cl.Patch(ctx, pcs, client.RawPatch(types.MergePatchType, patchBytes))
}

// WaitForPodCliqueScalingGroup polls until a PodCliqueScalingGroup exists and returns it.
func WaitForPodCliqueScalingGroup(ctx context.Context, k8sClient *k8sclient.Client, namespace, name string, timeout, interval time.Duration) (*grovecorev1alpha1.PodCliqueScalingGroup, error) {
	w := waiter.New[*grovecorev1alpha1.PodCliqueScalingGroup]().WithTimeout(timeout).WithInterval(interval)
	return waiter.WaitForResource(ctx, w, name, k8sclient.Getter[*grovecorev1alpha1.PodCliqueScalingGroup](k8sClient, namespace))
}

// WaitForPodCliqueStandalone polls until a PodClique exists and returns it.
func WaitForPodCliqueStandalone(ctx context.Context, k8sClient *k8sclient.Client, namespace, name string, timeout, interval time.Duration) (*grovecorev1alpha1.PodClique, error) {
	w := waiter.New[*grovecorev1alpha1.PodClique]().WithTimeout(timeout).WithInterval(interval)
	return waiter.WaitForResource(ctx, w, name, k8sclient.Getter[*grovecorev1alpha1.PodClique](k8sClient, namespace))
}

// WaitForPodCliqueSetDeletion polls until a PodCliqueSet no longer exists.
func WaitForPodCliqueSetDeletion(ctx context.Context, k8sClient *k8sclient.Client, namespace, name string, timeout, interval time.Duration) error {
	w := waiter.New[*grovecorev1alpha1.PodCliqueSet]().
		WithTimeout(timeout).
		WithInterval(interval)
	return waiter.WaitForResourceDeletion(ctx, w, name, k8sclient.Getter[*grovecorev1alpha1.PodCliqueSet](k8sClient, namespace))
}

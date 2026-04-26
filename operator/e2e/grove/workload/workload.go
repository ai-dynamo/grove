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
	"fmt"
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/gvk"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/k8sclient"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/resources"
	"github.com/ai-dynamo/grove/operator/e2e/log"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// WorkloadManager provides Grove workload operations using a controller-runtime client.
type WorkloadManager struct {
	cl        client.Client
	k8s       *k8sclient.Client
	resources *resources.ResourceManager
	logger    *log.Logger
}

// NewWorkloadManager creates a WorkloadManager bound to the given K8s client.
func NewWorkloadManager(k8s *k8sclient.Client, logger *log.Logger) *WorkloadManager {
	return &WorkloadManager{
		cl:        k8s,
		k8s:       k8s,
		resources: resources.NewResourceManager(k8s, logger),
		logger:    logger,
	}
}

// ScalePCS scales a PodCliqueSet to the specified replica count.
func (wm *WorkloadManager) ScalePCS(ctx context.Context, namespace, name string, replicas int) error {
	return wm.resources.ScaleCRD(ctx, gvk.PodCliqueSet, namespace, name, replicas)
}

// ScalePCSG scales a PodCliqueScalingGroup to the specified replica count.
// It waits for the PCSG to exist before scaling.
func (wm *WorkloadManager) ScalePCSG(ctx context.Context, namespace, name string, replicas int, timeout, interval time.Duration) error {
	_, err := WaitForPodCliqueScalingGroup(ctx, wm.k8s, namespace, name, timeout, interval)
	if err != nil {
		return fmt.Errorf("failed to find PodCliqueScalingGroup %s: %w", name, err)
	}

	return wm.resources.ScaleCRD(ctx, gvk.PodCliqueScalingGroup, namespace, name, replicas)
}

// DeletePCS deletes a PodCliqueSet by name. NotFound errors are ignored.
func (wm *WorkloadManager) DeletePCS(ctx context.Context, namespace, name string) error {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	err := wm.cl.Delete(ctx, pcs)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete PodCliqueSet %s/%s: %w", namespace, name, err)
	}
	return nil
}

// DeletePCSAndWait deletes a PodCliqueSet and polls until it is fully removed.
func (wm *WorkloadManager) DeletePCSAndWait(ctx context.Context, namespace, name string, timeout, interval time.Duration) error {
	if err := wm.DeletePCS(ctx, namespace, name); err != nil {
		return err
	}
	return WaitForPodCliqueSetDeletion(ctx, wm.k8s, namespace, name, timeout, interval)
}

// WaitForPCSG polls until a PodCliqueScalingGroup exists and returns it.
func (wm *WorkloadManager) WaitForPCSG(ctx context.Context, namespace, name string, timeout, interval time.Duration) (*grovecorev1alpha1.PodCliqueScalingGroup, error) {
	return WaitForPodCliqueScalingGroup(ctx, wm.k8s, namespace, name, timeout, interval)
}

// WaitForPodClique polls until a PodClique exists and returns it.
func (wm *WorkloadManager) WaitForPodClique(ctx context.Context, namespace, name string, timeout, interval time.Duration) (*grovecorev1alpha1.PodClique, error) {
	return WaitForPodCliqueStandalone(ctx, wm.k8s, namespace, name, timeout, interval)
}

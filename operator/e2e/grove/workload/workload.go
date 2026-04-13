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
	groveclient "github.com/ai-dynamo/grove/operator/client/clientset/versioned"
	"github.com/ai-dynamo/grove/operator/e2e/k8s"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/clients"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/resources"
	"github.com/ai-dynamo/grove/operator/e2e/log"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	podCliqueSetGVR = schema.GroupVersionResource{
		Group:    "grove.io",
		Version:  "v1alpha1",
		Resource: "podcliquesets",
	}

	podCliqueScalingGroupGVR = schema.GroupVersionResource{
		Group:    "grove.io",
		Version:  "v1alpha1",
		Resource: "podcliquescalinggroups",
	}
)

// WorkloadManager provides Grove workload operations using pre-created Kubernetes clients.
type WorkloadManager struct {
	clients   *clients.Clients
	resources *resources.ResourceManager
	pods      *pods.PodManager
	logger    *log.Logger
}

// NewWorkloadManager creates a WorkloadManager bound to the given clients.
func NewWorkloadManager(clients *clients.Clients, logger *log.Logger) *WorkloadManager {
	return &WorkloadManager{
		clients:   clients,
		resources: resources.NewResourceManager(clients, logger),
		pods:      pods.NewPodManager(clients, logger),
		logger:    logger,
	}
}

// ScalePCS scales a PodCliqueSet to the specified replica count.
func (wm *WorkloadManager) ScalePCS(ctx context.Context, namespace, name string, replicas int) error {
	return wm.resources.ScaleCRD(ctx, podCliqueSetGVR, namespace, name, replicas)
}

// ScalePCSG scales a PodCliqueScalingGroup to the specified replica count.
// It waits for the PCSG to exist before scaling.
func (wm *WorkloadManager) ScalePCSG(ctx context.Context, namespace, name string, replicas int, timeout, interval time.Duration) error {
	// Wait for PCSG to exist
	err := k8s.PollForCondition(ctx, timeout, interval, func() (bool, error) {
		_, err := wm.clients.DynamicClient.Resource(podCliqueScalingGroupGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("failed to find PodCliqueScalingGroup %s: %w", name, err)
	}

	return wm.resources.ScaleCRD(ctx, podCliqueScalingGroupGVR, namespace, name, replicas)
}

// DeletePCS deletes a PodCliqueSet by name. NotFound errors are ignored.
func (wm *WorkloadManager) DeletePCS(ctx context.Context, namespace, name string) error {
	err := wm.clients.DynamicClient.Resource(podCliqueSetGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
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

	return k8s.PollForCondition(ctx, timeout, interval, func() (bool, error) {
		_, err := wm.clients.DynamicClient.Resource(podCliqueSetGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return false, nil
	})
}

// WaitForPCSG polls until a PodCliqueScalingGroup exists and returns it.
func (wm *WorkloadManager) WaitForPCSG(ctx context.Context, namespace, name string, timeout, interval time.Duration) (*grovecorev1alpha1.PodCliqueScalingGroup, error) {
	var pcsg *grovecorev1alpha1.PodCliqueScalingGroup
	err := k8s.PollForCondition(ctx, timeout, interval, func() (bool, error) {
		var getErr error
		pcsg, getErr = wm.clients.GroveClient.GroveV1alpha1().PodCliqueScalingGroups(namespace).Get(ctx, name, metav1.GetOptions{})
		if errors.IsNotFound(getErr) {
			return false, nil
		}
		if getErr != nil {
			return false, getErr
		}
		return true, nil
	})
	return pcsg, err
}

// WaitForPodClique polls until a PodClique exists and returns it.
func (wm *WorkloadManager) WaitForPodClique(ctx context.Context, groveClient groveclient.Interface, namespace, name string, timeout, interval time.Duration) (*grovecorev1alpha1.PodClique, error) {
	var pclq *grovecorev1alpha1.PodClique
	err := k8s.PollForCondition(ctx, timeout, interval, func() (bool, error) {
		var getErr error
		pclq, getErr = groveClient.GroveV1alpha1().PodCliques(namespace).Get(ctx, name, metav1.GetOptions{})
		if errors.IsNotFound(getErr) {
			return false, nil
		}
		if getErr != nil {
			return false, getErr
		}
		return true, nil
	})
	return pclq, err
}

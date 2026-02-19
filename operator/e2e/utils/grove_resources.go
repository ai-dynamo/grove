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

package utils

import (
	"context"
	"fmt"
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveclient "github.com/ai-dynamo/grove/operator/client/clientset/versioned"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var podCliqueSetGVR = schema.GroupVersionResource{
	Group:    "grove.io",
	Version:  "v1alpha1",
	Resource: "podcliquesets",
}

// DeletePodCliqueSet deletes a PodCliqueSet by name.
// NotFound errors are ignored; other errors are returned.
func DeletePodCliqueSet(ctx context.Context, dynamicClient dynamic.Interface, namespace, name string) error {
	err := dynamicClient.Resource(podCliqueSetGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete PodCliqueSet %s/%s: %w", namespace, name, err)
	}
	return nil
}

// DeletePodCliqueSetAndWait deletes a PodCliqueSet and polls until it is fully removed.
func DeletePodCliqueSetAndWait(ctx context.Context, dynamicClient dynamic.Interface, namespace, name string, timeout, interval time.Duration) error {
	if err := DeletePodCliqueSet(ctx, dynamicClient, namespace, name); err != nil {
		return err
	}

	return PollForCondition(ctx, timeout, interval, func() (bool, error) {
		_, err := dynamicClient.Resource(podCliqueSetGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		return errors.IsNotFound(err), nil
	})
}

// WaitForPodCliqueScalingGroup polls until a PodCliqueScalingGroup exists and returns it.
func WaitForPodCliqueScalingGroup(ctx context.Context, groveClient groveclient.Interface, namespace, name string, timeout, interval time.Duration) (*grovecorev1alpha1.PodCliqueScalingGroup, error) {
	var pcsg *grovecorev1alpha1.PodCliqueScalingGroup
	err := PollForCondition(ctx, timeout, interval, func() (bool, error) {
		var getErr error
		pcsg, getErr = groveClient.GroveV1alpha1().PodCliqueScalingGroups(namespace).Get(ctx, name, metav1.GetOptions{})
		return getErr == nil, nil
	})
	return pcsg, err
}

// WaitForPodClique polls until a PodClique exists and returns it.
func WaitForPodClique(ctx context.Context, groveClient groveclient.Interface, namespace, name string, timeout, interval time.Duration) (*grovecorev1alpha1.PodClique, error) {
	var pclq *grovecorev1alpha1.PodClique
	err := PollForCondition(ctx, timeout, interval, func() (bool, error) {
		var getErr error
		pclq, getErr = groveClient.GroveV1alpha1().PodCliques(namespace).Get(ctx, name, metav1.GetOptions{})
		return getErr == nil, nil
	})
	return pclq, err
}

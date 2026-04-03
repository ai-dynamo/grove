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

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// ResourceClaimGVR is the GVR for resource.k8s.io/v1 ResourceClaim.
var ResourceClaimGVR = schema.GroupVersionResource{
	Group:    "resource.k8s.io",
	Version:  "v1",
	Resource: "resourceclaims",
}

// ListResourceClaims lists ResourceClaims in a namespace filtered by label selector.
func ListResourceClaims(ctx context.Context, dynamicClient dynamic.Interface, namespace, labelSelector string) (*unstructured.UnstructuredList, error) {
	return dynamicClient.Resource(ResourceClaimGVR).Namespace(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
}

// GetResourceClaim gets a single ResourceClaim by name.
func GetResourceClaim(ctx context.Context, dynamicClient dynamic.Interface, namespace, name string) (*unstructured.Unstructured, error) {
	return dynamicClient.Resource(ResourceClaimGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

// WaitForResourceClaimCount polls until the number of ResourceClaims matching the label selector equals expectedCount.
func WaitForResourceClaimCount(ctx context.Context, dynamicClient dynamic.Interface, namespace, labelSelector string, expectedCount int, timeout, interval time.Duration) error {
	return PollForCondition(ctx, timeout, interval, func() (bool, error) {
		list, err := ListResourceClaims(ctx, dynamicClient, namespace, labelSelector)
		if err != nil {
			return false, err
		}
		return len(list.Items) == expectedCount, nil
	})
}

// WaitForResourceClaimsByName polls until all named ResourceClaims exist.
func WaitForResourceClaimsByName(ctx context.Context, dynamicClient dynamic.Interface, namespace string, names []string, timeout, interval time.Duration) error {
	return PollForCondition(ctx, timeout, interval, func() (bool, error) {
		for _, name := range names {
			_, err := GetResourceClaim(ctx, dynamicClient, namespace, name)
			if err != nil {
				if errors.IsNotFound(err) {
					return false, nil
				}
				return false, err
			}
		}
		return true, nil
	})
}

// WaitForResourceClaimDeletion polls until the named ResourceClaim no longer exists.
func WaitForResourceClaimDeletion(ctx context.Context, dynamicClient dynamic.Interface, namespace, name string, timeout, interval time.Duration) error {
	return PollForCondition(ctx, timeout, interval, func() (bool, error) {
		_, err := GetResourceClaim(ctx, dynamicClient, namespace, name)
		if err != nil {
			if errors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
		return false, nil
	})
}

// ResourceClaimNames extracts the names from a list of unstructured ResourceClaims.
func ResourceClaimNames(list *unstructured.UnstructuredList) []string {
	names := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		names = append(names, item.GetName())
	}
	return names
}

// DeleteResourceClaimTemplate deletes a ResourceClaimTemplate by name. NotFound errors are ignored.
func DeleteResourceClaimTemplate(ctx context.Context, dynamicClient dynamic.Interface, namespace, name string) error {
	rctGVR := schema.GroupVersionResource{
		Group:    "resource.k8s.io",
		Version:  "v1",
		Resource: "resourceclaimtemplates",
	}
	err := dynamicClient.Resource(rctGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete ResourceClaimTemplate %s/%s: %w", namespace, name, err)
	}
	return nil
}

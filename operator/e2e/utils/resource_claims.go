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

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ListResourceClaims lists ResourceClaims in a namespace filtered by label selector.
func ListResourceClaims(ctx context.Context, crClient client.Client, namespace, labelSelector string) (*resourcev1.ResourceClaimList, error) {
	var list resourcev1.ResourceClaimList
	opts := []client.ListOption{
		client.InNamespace(namespace),
	}
	if labelSelector != "" {
		selector, err := labels.Parse(labelSelector)
		if err != nil {
			return nil, fmt.Errorf("failed to parse label selector %q: %w", labelSelector, err)
		}
		opts = append(opts, client.MatchingLabelsSelector{Selector: selector})
	}
	if err := crClient.List(ctx, &list, opts...); err != nil {
		return nil, err
	}
	return &list, nil
}

// GetResourceClaim gets a single ResourceClaim by name.
func GetResourceClaim(ctx context.Context, crClient client.Client, namespace, name string) (*resourcev1.ResourceClaim, error) {
	var rc resourcev1.ResourceClaim
	if err := crClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &rc); err != nil {
		return nil, err
	}
	return &rc, nil
}

// WaitForResourceClaimCount polls until the number of ResourceClaims matching the label selector equals expectedCount.
func WaitForResourceClaimCount(ctx context.Context, crClient client.Client, namespace, labelSelector string, expectedCount int, timeout, interval time.Duration) error {
	return PollForCondition(ctx, timeout, interval, func() (bool, error) {
		list, err := ListResourceClaims(ctx, crClient, namespace, labelSelector)
		if err != nil {
			return false, err
		}
		return len(list.Items) == expectedCount, nil
	})
}

// WaitForResourceClaimsByName polls until all named ResourceClaims exist.
func WaitForResourceClaimsByName(ctx context.Context, crClient client.Client, namespace string, names []string, timeout, interval time.Duration) error {
	return PollForCondition(ctx, timeout, interval, func() (bool, error) {
		for _, name := range names {
			_, err := GetResourceClaim(ctx, crClient, namespace, name)
			if err != nil {
				if client.IgnoreNotFound(err) == nil {
					return false, nil
				}
				return false, err
			}
		}
		return true, nil
	})
}

// WaitForResourceClaimDeletion polls until the named ResourceClaim no longer exists.
func WaitForResourceClaimDeletion(ctx context.Context, crClient client.Client, namespace, name string, timeout, interval time.Duration) error {
	return PollForCondition(ctx, timeout, interval, func() (bool, error) {
		_, err := GetResourceClaim(ctx, crClient, namespace, name)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return true, nil
			}
			return false, err
		}
		return false, nil
	})
}

// ResourceClaimNames extracts the names from a list of ResourceClaims.
func ResourceClaimNames(list *resourcev1.ResourceClaimList) []string {
	names := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		names = append(names, item.Name)
	}
	return names
}

// DeleteResourceClaimTemplate deletes a ResourceClaimTemplate by name. NotFound errors are ignored.
func DeleteResourceClaimTemplate(ctx context.Context, crClient client.Client, namespace, name string) error {
	rct := &resourcev1.ResourceClaimTemplate{}
	rct.Name = name
	rct.Namespace = namespace
	if err := crClient.Delete(ctx, rct); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil
		}
		return fmt.Errorf("failed to delete ResourceClaimTemplate %s/%s: %w", namespace, name, err)
	}
	return nil
}

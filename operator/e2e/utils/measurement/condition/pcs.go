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

package condition

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
)

// PCSDeletedCondition checks if a PodCliqueSet no longer exists.
type PCSDeletedCondition struct {
	Client    client.Client
	Name      string
	Namespace string
}

// Met returns true once the PodCliqueSet is not found.
func (c *PCSDeletedCondition) Met(ctx context.Context) (bool, error) {
	var pcs corev1alpha1.PodCliqueSet
	err := c.Client.Get(ctx, types.NamespacedName{Name: c.Name, Namespace: c.Namespace}, &pcs)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("get PodCliqueSet: %w", err)
	}
	return false, nil
}

// Progress returns a human-readable progress string.
func (c *PCSDeletedCondition) Progress(_ context.Context) string {
	return "Not deleted"
}

// PCSAvailableCondition checks if a PodCliqueSet has at least ExpectedCount available replicas.
type PCSAvailableCondition struct {
	Client        client.Client
	Name          string
	Namespace     string
	ExpectedCount int32
	lastAvailable int32
}

// Met returns true once the PodCliqueSet has enough available replicas.
func (c *PCSAvailableCondition) Met(ctx context.Context) (bool, error) {
	if c.ExpectedCount < 0 {
		return false, errors.New("expected count cannot be negative")
	}

	var pcs corev1alpha1.PodCliqueSet
	if err := c.Client.Get(ctx, types.NamespacedName{Name: c.Name, Namespace: c.Namespace}, &pcs); err != nil {
		return false, fmt.Errorf("get PodCliqueSet: %w", err)
	}

	c.lastAvailable = pcs.Status.AvailableReplicas
	return c.lastAvailable >= c.ExpectedCount, nil
}

// Progress returns a human-readable progress string.
func (c *PCSAvailableCondition) Progress(_ context.Context) string {
	return fmt.Sprintf("%d/%d replicas available", c.lastAvailable, c.ExpectedCount)
}

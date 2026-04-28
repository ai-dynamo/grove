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

	corev1 "k8s.io/api/core/v1"
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

// PCSFullyDeletedCondition checks that the PodCliqueSet and every resource it
// owns transitively (PodCliqueScalingGroups, PodCliques, and Pods carrying the
// part-of label) are gone from the API. Use this in delete-phase milestones
// when you want the timeline to reflect actual cascade-cleanup latency, not
// just finalizer removal on the parent PCS.
type PCSFullyDeletedCondition struct {
	Client        client.Client
	Name          string
	Namespace     string
	LabelSelector string

	sel       parsedSelector
	lastPCS   int
	lastPCSG  int
	lastPCLQ  int
	lastPod   int
}

// Met returns true when the PCS, all owned PCSGs, all owned PodCliques, and
// all owned Pods (matched by LabelSelector) are absent from the API.
func (c *PCSFullyDeletedCondition) Met(ctx context.Context) (bool, error) {
	c.sel.init(c.LabelSelector)
	s, err := c.sel.get()
	if err != nil {
		return false, err
	}

	var pcs corev1alpha1.PodCliqueSet
	switch err := c.Client.Get(ctx, types.NamespacedName{Name: c.Name, Namespace: c.Namespace}, &pcs); {
	case apierrors.IsNotFound(err):
		c.lastPCS = 0
	case err != nil:
		return false, fmt.Errorf("get PodCliqueSet: %w", err)
	default:
		c.lastPCS = 1
	}

	listOpts := []client.ListOption{
		client.InNamespace(c.Namespace),
		client.MatchingLabelsSelector{Selector: s},
	}

	var pcsgList corev1alpha1.PodCliqueScalingGroupList
	if err := c.Client.List(ctx, &pcsgList, listOpts...); err != nil {
		return false, fmt.Errorf("list PodCliqueScalingGroups: %w", err)
	}
	c.lastPCSG = len(pcsgList.Items)

	var pclqList corev1alpha1.PodCliqueList
	if err := c.Client.List(ctx, &pclqList, listOpts...); err != nil {
		return false, fmt.Errorf("list PodCliques: %w", err)
	}
	c.lastPCLQ = len(pclqList.Items)

	var podList corev1.PodList
	if err := c.Client.List(ctx, &podList, listOpts...); err != nil {
		return false, fmt.Errorf("list Pods: %w", err)
	}
	c.lastPod = len(podList.Items)

	return c.lastPCS == 0 && c.lastPCSG == 0 && c.lastPCLQ == 0 && c.lastPod == 0, nil
}

// Progress returns a human-readable progress string.
func (c *PCSFullyDeletedCondition) Progress(_ context.Context) string {
	return fmt.Sprintf("PCS=%d PCSG=%d PCLQ=%d Pod=%d remaining", c.lastPCS, c.lastPCSG, c.lastPCLQ, c.lastPod)
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

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
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubeutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
)

func listPods(ctx context.Context, cl client.Client, ns, sel string) ([]corev1.Pod, error) {
	parsed, err := labels.Parse(sel)
	if err != nil {
		return nil, fmt.Errorf("parse label selector: %w", err)
	}

	var podList corev1.PodList
	if err := cl.List(ctx, &podList, client.InNamespace(ns), client.MatchingLabelsSelector{Selector: parsed}); err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	return podList.Items, nil
}

// PodsCreatedCondition checks if at least ExpectedCount matching pods exist.
type PodsCreatedCondition struct {
	Client        client.Client
	Namespace     string
	LabelSelector string
	ExpectedCount int
	lastCount     int
}

// Met returns true once the expected pod count exists.
func (c *PodsCreatedCondition) Met(ctx context.Context) (bool, error) {
	if c.ExpectedCount < 0 {
		return false, errors.New("expected count cannot be negative")
	}

	pods, err := listPods(ctx, c.Client, c.Namespace, c.LabelSelector)
	if err != nil {
		return false, err
	}

	c.lastCount = len(pods)
	return c.lastCount >= c.ExpectedCount, nil
}

// Progress returns a human-readable progress string.
func (c *PodsCreatedCondition) Progress(_ context.Context) string {
	return fmt.Sprintf("%d/%d pods created", c.lastCount, c.ExpectedCount)
}

// PodsReadyCondition checks if at least ExpectedCount matching pods are Ready.
type PodsReadyCondition struct {
	Client        client.Client
	Namespace     string
	LabelSelector string
	ExpectedCount int
	lastReady     int
}

// Met returns true once the expected number of pods are Ready.
func (c *PodsReadyCondition) Met(ctx context.Context) (bool, error) {
	if c.ExpectedCount < 0 {
		return false, errors.New("expected count cannot be negative")
	}

	pods, err := listPods(ctx, c.Client, c.Namespace, c.LabelSelector)
	if err != nil {
		return false, err
	}

	c.lastReady = 0
	for i := range pods {
		if kubeutils.IsPodReady(&pods[i]) {
			c.lastReady++
		}
	}

	return c.lastReady >= c.ExpectedCount, nil
}

// Progress returns a human-readable progress string.
func (c *PodsReadyCondition) Progress(_ context.Context) string {
	return fmt.Sprintf("%d/%d pods ready", c.lastReady, c.ExpectedCount)
}

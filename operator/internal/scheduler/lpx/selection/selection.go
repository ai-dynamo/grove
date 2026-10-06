// Copyright 2026 The Grove Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package selection

import (
	"context"
	"fmt"
	"slices"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Resources identifies the resources routed to LPX rather than its fallback backend.
var Resources = []corev1.ResourceName{"lpu.nvidia.com/lpu", "nvidia.com/lpu"}

// UsesLPX reports whether the Pod requests an LPX resource.
func UsesLPX(podSpec corev1.PodSpec) bool {
	return slices.ContainsFunc(podSpec.Containers, func(container corev1.Container) bool {
		return slices.ContainsFunc(Resources, func(r corev1.ResourceName) bool {
			_, requests := container.Resources.Requests[r]
			_, limits := container.Resources.Limits[r]
			return requests || limits
		})
	})
}

// PodCliqueUsesFallback applies LPX's group-selection policy to a materialized PodClique.
func PodCliqueUsesFallback(ctx context.Context, cl client.Client, key client.ObjectKey) (bool, error) {
	var pclq grovecorev1alpha1.PodClique
	if err := cl.Get(ctx, key, &pclq); err != nil {
		return false, fmt.Errorf("get PodClique %s for LPX fallback: %w", key, err)
	}
	return !UsesLPX(pclq.Spec.PodSpec), nil
}

// ProjectPodGang returns a metadata-preserving copy containing only LPX-selected fallback groups.
func ProjectPodGang(ctx context.Context, cl client.Client, podGang *groveschedulerv1alpha1.PodGang) (*groveschedulerv1alpha1.PodGang, error) {
	fallback := podGang.DeepCopy()
	fallback.Spec.PodGroups = make([]groveschedulerv1alpha1.PodGroup, 0, len(podGang.Spec.PodGroups))
	for _, group := range podGang.Spec.PodGroups {
		selected, err := PodCliqueUsesFallback(ctx, cl, client.ObjectKey{Namespace: podGang.Namespace, Name: group.Name})
		if err != nil {
			return nil, fmt.Errorf("project PodGang %s/%s: %w", podGang.Namespace, podGang.Name, err)
		}
		if selected {
			fallback.Spec.PodGroups = append(fallback.Spec.PodGroups, group)
		}
	}
	return fallback, nil
}

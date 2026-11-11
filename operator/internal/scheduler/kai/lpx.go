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

package kai

import (
	"context"
	"fmt"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/lpx/selection"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (b *schedulerBackend) projectLPXPodGangs(ctx context.Context, materialized []componentutils.MaterializedPodGang) ([]componentutils.MaterializedPodGang, error) {
	projected := make([]componentutils.MaterializedPodGang, 0, len(materialized))
	for _, item := range materialized {
		podGang, err := selection.ProjectPodGang(ctx, b.client, item.PodGang)
		if err != nil {
			return nil, err
		}
		if len(podGang.Spec.PodGroups) > 0 {
			projected = append(projected, componentutils.MaterializedPodGang{PodGang: podGang, Entry: item.Entry})
		}
	}
	return projected, nil
}

func (b *schedulerBackend) selectLPXFallbackPods(ctx context.Context, pods []corev1.Pod) ([]corev1.Pod, error) {
	selected := make([]corev1.Pod, 0, len(pods))
	byClique := make(map[client.ObjectKey]bool)
	for _, pod := range pods {
		key := client.ObjectKey{Namespace: pod.Namespace, Name: pod.Labels[apicommon.LabelPodClique]}
		if key.Name == "" {
			return nil, fmt.Errorf("pod %s/%s has no PodClique for LPX selection", pod.Namespace, pod.Name)
		}
		fallback, found := byClique[key]
		if !found {
			var err error
			fallback, err = selection.PodCliqueUsesFallback(ctx, b.client, key)
			if err != nil {
				return nil, err
			}
			byClique[key] = fallback
		}
		if fallback {
			selected = append(selected, pod)
		}
	}
	return selected, nil
}

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
	"strings"
	"testing"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestGenerateSchedulerScopedPodGangName(t *testing.T) {
	registry := &testutils.FakeSchedulerRegistry{
		Backends: map[string]scheduler.Backend{
			string(configv1alpha1.SchedulerNameKube): testutils.NewFakeSchedulerBackend(string(configv1alpha1.SchedulerNameKube)),
			string(configv1alpha1.SchedulerNameKai):  testutils.NewFakeSchedulerBackend(string(configv1alpha1.SchedulerNameKai)),
		},
		DefaultBackend: string(configv1alpha1.SchedulerNameKube),
	}

	t.Run("preserves legacy name for a single scheduler", func(t *testing.T) {
		pcs := podCliqueSetWithSchedulers("", string(configv1alpha1.SchedulerNameKube))
		assert.Equal(t, "workload-0", GenerateSchedulerScopedPodGangName("workload-0", pcs, "", registry))
	})

	t.Run("qualifies each scheduler in a disjoint PCS", func(t *testing.T) {
		pcs := podCliqueSetWithSchedulers("", string(configv1alpha1.SchedulerNameKai))
		assert.Equal(t, "workload-0-default-scheduler", GenerateSchedulerScopedPodGangName("workload-0", pcs, "", registry))
		assert.Equal(t, "workload-0-kai-scheduler", GenerateSchedulerScopedPodGangName("workload-0", pcs, string(configv1alpha1.SchedulerNameKai), registry))
	})

	t.Run("keeps label values within the Kubernetes limit", func(t *testing.T) {
		pcs := podCliqueSetWithSchedulers("", string(configv1alpha1.SchedulerNameKai))
		name := GenerateSchedulerScopedPodGangName(strings.Repeat("a", 60), pcs, string(configv1alpha1.SchedulerNameKai), registry)
		require.Len(t, name, maxLabelValueLength)
		assert.Regexp(t, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`, name)
	})

	t.Run("hashes a short base name when the scheduler suffix exceeds the limit", func(t *testing.T) {
		pcs := podCliqueSetWithSchedulers("", string(configv1alpha1.SchedulerNameKai))
		baseName := strings.Repeat("a", 44) + "-0"

		name := GenerateSchedulerScopedPodGangName(baseName, pcs, "", registry)

		require.Len(t, name, len(baseName)+9)
		assert.Regexp(t, `^`+baseName+`-[a-f0-9]{8}$`, name)
	})
}

func podCliqueSetWithSchedulers(schedulerNames ...string) *grovecorev1alpha1.PodCliqueSet {
	pcs := &grovecorev1alpha1.PodCliqueSet{}
	for _, schedulerName := range schedulerNames {
		pcs.Spec.Template.Cliques = append(pcs.Spec.Template.Cliques, &grovecorev1alpha1.PodCliqueTemplateSpec{
			Spec: grovecorev1alpha1.PodCliqueSpec{
				PodSpec: corev1.PodSpec{SchedulerName: schedulerName},
			},
		})
	}
	return pcs
}

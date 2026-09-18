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

package component

import (
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"
)

func TestCoherentMaxUnavailableByComponent(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("fe").WithReplicas(5).WithMinAvailable(2).WithMaxUnavailable(3).Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("other").WithReplicas(4).WithMinAvailable(1).WithMaxUnavailable(1).Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("pf-leader").WithReplicas(1).WithMinAvailable(1).Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("pfnil-leader").WithReplicas(1).WithMinAvailable(1).Build()).
		WithPodCliqueScalingGroupConfig(grovecorev1alpha1.PodCliqueScalingGroupConfig{
			Name:          "pf",
			CliqueNames:   []string{"pf-leader"},
			MinAvailable:  ptr.To[int32](1),
			RollingUpdate: &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](2)},
		}).
		WithPodCliqueScalingGroupConfig(grovecorev1alpha1.PodCliqueScalingGroupConfig{
			Name:         "pfnil",
			CliqueNames:  []string{"pfnil-leader"},
			MinAvailable: ptr.To[int32](4),
		}).
		Build()

	// "fe" carries a set MaxUnavailable (3), "pf" a set MaxUnavailable (2), "pfnil" has no RollingUpdate
	// so Coherent falls back to its MinAvailable (4), and "other" is not in scope so it is excluded.
	got := CoherentMaxUnavailableByComponent(pcs, []string{"fe", "pf", "pfnil"})
	assert.Equal(t, map[string]int32{"fe": 3, "pf": 2, "pfnil": 4}, got)
}

func TestCoherentMinAvailableByComponent(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("fe").WithReplicas(5).WithMinAvailable(2).Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("other").WithReplicas(4).WithMinAvailable(1).Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("pf-leader").WithReplicas(1).WithMinAvailable(1).Build()).
		WithPodCliqueScalingGroupConfig(grovecorev1alpha1.PodCliqueScalingGroupConfig{
			Name:         "pf",
			CliqueNames:  []string{"pf-leader"},
			MinAvailable: ptr.To[int32](3),
		}).
		Build()

	// "fe" is an in-scope standalone PodClique, "pf" an in-scope PCSG, "other" is out of scope, and
	// "pf-leader" is a PCSG member (not standalone) so it lands in neither map even though in scope.
	standalonePCLQMinAvailable, pcsgMinAvailable := CoherentMinAvailableByComponent(pcs, []string{"fe", "pf", "pf-leader"})
	assert.Equal(t, map[string]int32{"fe": 2}, standalonePCLQMinAvailable)
	assert.Equal(t, map[string]int32{"pf": 3}, pcsgMinAvailable)
}

func TestEffectiveMaxUnavailable(t *testing.T) {
	testCases := []struct {
		description    string
		rollingUpdate  *grovecorev1alpha1.RollingUpdateConfiguration
		updateStrategy grovecorev1alpha1.UpdateStrategyType
		minAvailable   int32
		want           int
	}{
		{
			description:    "RollingRecreate nil rollingUpdate returns the RollingRecreate default",
			rollingUpdate:  nil,
			updateStrategy: grovecorev1alpha1.RollingRecreateStrategy,
			want:           int(DefaultRollingRecreateMaxUnavailable),
		},
		{
			description:    "RollingRecreate nil MaxUnavailable returns the RollingRecreate default",
			rollingUpdate:  &grovecorev1alpha1.RollingUpdateConfiguration{},
			updateStrategy: grovecorev1alpha1.RollingRecreateStrategy,
			want:           int(DefaultRollingRecreateMaxUnavailable),
		},
		{
			description:    "Coherent nil rollingUpdate returns MinAvailable",
			rollingUpdate:  nil,
			updateStrategy: grovecorev1alpha1.CoherentStrategy,
			minAvailable:   3,
			want:           3,
		},
		{
			description:    "Coherent nil MaxUnavailable returns MinAvailable",
			rollingUpdate:  &grovecorev1alpha1.RollingUpdateConfiguration{},
			updateStrategy: grovecorev1alpha1.CoherentStrategy,
			minAvailable:   2,
			want:           2,
		},
		{
			description:    "configured MaxUnavailable is returned regardless of strategy",
			rollingUpdate:  &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To[int32](5)},
			updateStrategy: grovecorev1alpha1.CoherentStrategy,
			minAvailable:   2,
			want:           5,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			assert.Equal(t, tc.want, EffectiveMaxUnavailable(tc.rollingUpdate, tc.updateStrategy, tc.minAvailable))
		})
	}
}

func TestComputeAllowedBudget(t *testing.T) {
	tests := []struct {
		description             string
		desiredNumUnits         int
		numReadyUnits           int
		effectiveMaxUnavailable int
		want                    int
	}{
		{"default budget of 1 with all available", 3, 3, 1, 1},
		{"budget of 1 when all replicas are required and ready", 2, 2, 1, 1},
		{"budget exhausted by an in-flight disruption", 3, 2, 1, 0},
		{"maxUnavailable allows multiple disruptions", 5, 5, 2, 2},
		{"full disruption when maxUnavailable equals replicas", 4, 4, 4, 4},
		{"floored at zero when unavailable exceeds the budget", 3, 1, 1, 0},
		{"budget accounts for existing unavailable units", 6, 5, 3, 2},
	}
	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			assert.Equal(t, tt.want, ComputeAllowedBudget(tt.desiredNumUnits, tt.numReadyUnits, tt.effectiveMaxUnavailable))
		})
	}
}

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

package lpx

import (
	"context"
	"testing"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

func TestBackendPreparePod(t *testing.T) {
	backend := New(configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameLPX})
	pod := testutils.NewPodBuilder("test-pod", "default").
		WithSchedulerName("default-scheduler").
		Build()
	pod.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: "grove.io/podgang-pending-creation"}}
	pod.Spec.ResourceClaims = []corev1.PodResourceClaim{{
		Name:              "partition",
		ResourceClaimName: ptr.To("model-partition-000"),
	}}

	require.NoError(t, backend.PreparePod(pod, scheduler.PreparePodContext{}))

	assert.Equal(t, string(configv1alpha1.SchedulerNameLPX), pod.Spec.SchedulerName)
	require.Len(t, pod.Spec.SchedulingGates, 1)
	assert.Equal(t, "grove.io/podgang-pending-creation", pod.Spec.SchedulingGates[0].Name)
	require.Len(t, pod.Spec.ResourceClaims, 1)
	assert.Equal(t, "model-partition-000", *pod.Spec.ResourceClaims[0].ResourceClaimName)
}

func TestBackendValidatePodCliqueSet(t *testing.T) {
	tests := []struct {
		name      string
		mutatePCS func(*grovecorev1alpha1.PodCliqueSet)
		wantError bool
	}{
		{
			name:      "no Grove topology constraints",
			mutatePCS: func(_ *grovecorev1alpha1.PodCliqueSet) {},
		},
		{
			name: "PodCliqueSet topology constraint",
			mutatePCS: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Spec.Template.TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{}
			},
			wantError: true,
		},
		{
			name: "PodClique topology constraint",
			mutatePCS: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{}
			},
			wantError: true,
		},
		{
			name: "PodCliqueScalingGroup topology constraint",
			mutatePCS: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Spec.Template.PodCliqueScalingGroupConfigs = []grovecorev1alpha1.PodCliqueScalingGroupConfig{{
					Name:               "workers",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{},
				}}
			},
			wantError: true,
		},
	}

	backend := New(configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameLPX})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-uid")).
				WithPodCliqueTemplateSpec(
					testutils.NewPodCliqueTemplateSpecBuilder("worker").
						WithRoleName("worker").
						WithReplicas(1).
						Build(),
				).
				Build()
			tt.mutatePCS(pcs)

			err := backend.ValidatePodCliqueSet(context.Background(), pcs)

			if tt.wantError {
				require.ErrorIs(t, err, errTopologyConstraintsUnsupported)
				return
			}
			require.NoError(t, err)
		})
	}
}

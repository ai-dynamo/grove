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

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/kai"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
)

func TestBackendPreparePod(t *testing.T) {
	t.Run("without secondary backend", func(t *testing.T) {
		backend := New(nil, configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameLPX}, nil)

		pod := testutils.NewPodWithBuilderWithDefaultSpec("test-pod", "default").
			WithSchedulerName("lpx-scheduler").
			Build()

		require.NoError(t, backend.PreparePod(pod))
		assert.Equal(t, string(corev1.DefaultSchedulerName), pod.Spec.SchedulerName)
	})

	t.Run("with secondary backend", func(t *testing.T) {
		kaiBackend := kai.New(nil, nil, nil, configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai})
		backend := New(nil, configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameLPX}, kaiBackend)

		pod := testutils.NewPodWithBuilderWithDefaultSpec("test-pod", "default").
			WithSchedulerName("default-scheduler").
			Build()
		pod.Spec.Containers[0].Resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{resourcesLPX[0]: resource.MustParse("1")},
		}

		require.NoError(t, backend.PreparePod(pod))
		assert.Equal(t, string(configv1alpha1.SchedulerNameLPX), pod.Spec.SchedulerName)

		pod = testutils.NewPodWithBuilderWithDefaultSpec("test-pod", "default").
			WithSchedulerName("lpx-scheduler").
			WithLabels(map[string]string{
				apicommon.LabelPodGang:                  "podgang",
				apicommon.LabelPodClique:                "podclique",
				apicommon.LabelPartOfKey:                "workload",
				apicommon.LabelPodCliqueSetReplicaIndex: "0",
			}).
			Build()

		require.NoError(t, backend.PreparePod(pod))
		assert.Equal(t, string(configv1alpha1.SchedulerNameKai), pod.Spec.SchedulerName)
	})
}

func TestBackendValidatePodCliqueSet(t *testing.T) {
	tests := []struct {
		name      string
		mutatePCS func(*grovecorev1alpha1.PodCliqueSet)
		wantError bool
		errorType error
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
			errorType: errTopologyConstraintsUnsupported,
		},
		{
			name: "PodClique topology constraint",
			mutatePCS: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{}
			},
			wantError: true,
			errorType: errTopologyConstraintsUnsupported,
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
			errorType: errTopologyConstraintsUnsupported,
		},
		{
			name: "Fallback validation for non-LPX pods",
			mutatePCS: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Labels = map[string]string{"kai.scheduler/queue": "default"}
				pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0] = corev1.Container{}
			},
		},
		{
			name: "Fallback validation for non-LPX pods without a queue name",
			mutatePCS: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0] = corev1.Container{}
			},
			wantError: true,
		},
	}

	kaiBackend := kai.New(nil, nil, nil, configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai})
	backend := New(nil, configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameLPX}, kaiBackend)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-uid")).
				WithPodCliqueTemplateSpec(
					testutils.NewPodCliqueTemplateSpecBuilder("worker").
						WithRoleName("worker").
						WithReplicas(1).
						WithContainer(corev1.Container{
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									resourcesLPX[0]: resource.MustParse("1"),
								},
							},
						}).
						Build(),
				).
				Build()
			tt.mutatePCS(pcs)

			err := backend.ValidatePodCliqueSet(context.Background(), pcs)

			if tt.wantError {
				require.Error(t, err)
				if tt.errorType != nil {
					require.ErrorIs(t, err, tt.errorType)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestUsesLPX(t *testing.T) {
	podSpecWithResources := func(r corev1.ResourceRequirements) corev1.PodSpec {
		return corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:      "worker",
					Image:     "worker-image",
					Resources: r,
				},
			},
		}
	}

	for _, tt := range []struct {
		name    string
		podSpec corev1.PodSpec
		usesLPX bool
	}{
		{
			name: "no containers or resources",
		},
		{
			name:    "no LPX resources",
			podSpec: podSpecWithResources(corev1.ResourceRequirements{Requests: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")}}),
		},
		{
			name:    "deprecated LPX resource on requests",
			podSpec: podSpecWithResources(corev1.ResourceRequirements{Requests: corev1.ResourceList{"lpu.nvidia.com/lpu": resource.MustParse("1")}}),
			usesLPX: true,
		},
		{
			name:    "deprecated LPX resource on limits",
			podSpec: podSpecWithResources(corev1.ResourceRequirements{Limits: corev1.ResourceList{"lpu.nvidia.com/lpu": resource.MustParse("1")}}),
			usesLPX: true,
		},
		{
			name:    "LPX resource on requests",
			podSpec: podSpecWithResources(corev1.ResourceRequirements{Requests: corev1.ResourceList{"nvidia.com/lpu": resource.MustParse("1")}}),
			usesLPX: true,
		},
		{
			name:    "LPX resource on limits",
			podSpec: podSpecWithResources(corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/lpu": resource.MustParse("1")}}),
			usesLPX: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, usesLPX(tt.podSpec), tt.usesLPX, "Pod spec expected usesLPX = %t: %+v", tt.usesLPX, tt.podSpec)
		})
	}
}

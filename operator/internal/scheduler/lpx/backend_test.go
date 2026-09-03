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

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/kai"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"
	schedulertest "github.com/ai-dynamo/grove/operator/test/utils/scheduler"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	kaischedulingv2alpha2 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestBackendPreparePod(t *testing.T) {
	backend := New(nil,
		configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameLPX},
		testutils.NewFakeSchedulerBackend(string(configv1alpha1.SchedulerNameKai)),
	)
	pod := testutils.NewPodWithBuilderWithDefaultSpec("test-pod", "default").
		WithSchedulerName("default-scheduler").
		Build()
	pod.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: "grove.io/podgang-pending-creation"}}
	pod.Spec.Containers[0].Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{resourcesLPX[0]: resource.MustParse("1")},
	}

	require.NoError(t, backend.PreparePod(pod))

	assert.Equal(t, string(configv1alpha1.SchedulerNameLPX), pod.Spec.SchedulerName)
	require.Len(t, pod.Spec.SchedulingGates, 1)
	assert.Equal(t, "grove.io/podgang-pending-creation", pod.Spec.SchedulingGates[0].Name)
}

func TestBackendSyncPodGangLPXOnly(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("lpx-workload", "default", types.UID("pcs-uid")).
		Build()
	lpxPodClique := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, "lpx-worker", pcs.Namespace, 0).
		Build()
	lpxPodClique.Spec.PodSpec.Containers[0].Resources = corev1.ResourceRequirements{Requests: corev1.ResourceList{resourcesLPX[0]: resource.MustParse("1")}}
	podGang := testutils.NewPodGangBuilder("lpx-workload-0", pcs.Namespace).
		WithPodGroups([]groveschedulerv1alpha1.PodGroup{
			{Name: lpxPodClique.Name, MinReplicas: 1},
		}).
		WithOwnerReference(constants.KindPodCliqueSet, pcs.Name, pcs.UID).
		Build()

	scheme := schedulertest.NewKAIScheme(t)
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, groveschedulerv1alpha1.AddToScheme(scheme))

	cl := testutils.NewTestClientBuilder().WithScheme(scheme).WithObjects(pcs, lpxPodClique, podGang).Build()

	fallback := kai.New(cl, scheme, nil, configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai})
	backend := New(cl, configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameLPX}, fallback)
	require.NoError(t, backend.Init(cl))

	require.NoError(t, backend.SyncPodGang(t.Context(), podGang))

	storedPodGang := &groveschedulerv1alpha1.PodGang{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(podGang), storedPodGang))
	require.Len(t, storedPodGang.Spec.PodGroups, 1)
}

func TestBackendSyncPodGangMixedWorkload(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("mixed-workload", "default", types.UID("pcs-uid")).
		WithLabels(map[string]string{"kai.scheduler/queue": "default"}).
		Build()

	lpxPodClique := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, "lpx-worker", pcs.Namespace, 0).
		Build()
	lpxPodClique.Spec.PodSpec.Containers[0].Resources = corev1.ResourceRequirements{Requests: corev1.ResourceList{resourcesLPX[0]: resource.MustParse("1")}}

	kaiPodClique := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, "kai-worker", pcs.Namespace, 0).
		Build()

	podGang := testutils.NewPodGangBuilder("mixed-workload-0", pcs.Namespace).
		WithPodGroups([]groveschedulerv1alpha1.PodGroup{
			{Name: lpxPodClique.Name, MinReplicas: 1},
			{Name: kaiPodClique.Name, MinReplicas: 1},
		}).
		WithOwnerReference(constants.KindPodCliqueSet, pcs.Name, pcs.UID).
		Build()

	scheme := schedulertest.NewKAIScheme(t)
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, groveschedulerv1alpha1.AddToScheme(scheme))

	cl := testutils.NewTestClientBuilder().WithScheme(scheme).WithObjects(pcs, lpxPodClique, kaiPodClique, podGang).Build()

	fallback := kai.New(cl, scheme, nil, configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai})
	backend := New(cl, configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameLPX}, fallback)
	require.NoError(t, backend.Init(cl))

	require.NoError(t, backend.SyncPodGang(t.Context(), podGang))

	kaiPodGroup := &kaischedulingv2alpha2.PodGroup{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(podGang), kaiPodGroup))
	require.NotNil(t, kaiPodGroup.Spec.MinMember)
	assert.Equal(t, int32(1), ptr.Deref(kaiPodGroup.Spec.MinMember, 0))
	require.Len(t, kaiPodGroup.Spec.SubGroups, 1)
	assert.Equal(t, kaiPodClique.Name, kaiPodGroup.Spec.SubGroups[0].Name)

	storedPodGang := &groveschedulerv1alpha1.PodGang{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(podGang), storedPodGang))
	require.Len(t, storedPodGang.Spec.PodGroups, 2)
	assert.Equal(t, "true", storedPodGang.Annotations["kai.scheduler/skip-podgrouper"])
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

	fallback := kai.New(nil, nil, nil, configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai})
	backend := New(nil, configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameLPX}, fallback)

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

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

package kueue

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	kueuev1beta2 "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestBackend_PreparePod_Defaults(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
	b := New(cl, cl.Scheme(), recorder, profile)
	assert.NoError(t, b.Init(nil))

	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
			Labels:    map[string]string{queueNameLabel: "test-queue"},
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2}},
				},
			},
		},
	}
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pcs-0", Namespace: "default"},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{{Name: "test-pcs-0-worker"}},
		},
	}
	require.NoError(t, cl.Create(context.Background(), pcs))
	require.NoError(t, cl.Create(context.Background(), podGang))
	require.NoError(t, cl.Create(context.Background(), newStandalonePodClique("default", "test-pcs-0-worker", "test-pcs")))
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Labels: map[string]string{
				apicommon.LabelPartOfKey: "test-pcs",
				apicommon.LabelPodClique: "test-pcs-0-worker",
				apicommon.LabelPodGang:   "test-pcs-0",
			},
		},
		Spec: corev1.PodSpec{SchedulerName: "kueue"},
	}

	require.NoError(t, b.PreparePod(pod))

	assert.Equal(t, string(configv1alpha1.SchedulerNameKube), pod.Spec.SchedulerName)
	assert.Equal(t, "test-queue", pod.Labels[queueNameLabel])
	assert.Equal(t, "test-pcs-0", pod.Labels[podGroupNameLabel])
	assert.Equal(t, "test-pcs-0", pod.Labels[prebuiltWorkloadNameLabel])
	assert.Equal(t, "2", pod.Annotations[podGroupTotalCountAnnotation])
	assert.Equal(t, "test-pcs-0-worker", pod.Annotations[roleHashAnnotation])
	// Grove pods are not marked as a Kueue serving group, so Kueue can finalize them on teardown.
	assert.Empty(t, pod.Annotations[podGroupServingAnnotation])
	assert.Equal(t, "false", pod.Annotations[retriableInGroupAnnotation])
}

func TestBackend_PreparePod_ConfigAndExistingMetadata(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{
		Name: configv1alpha1.SchedulerNameKueue,
		Config: &runtime.RawExtension{
			Raw: []byte(`{"underlyingSchedulerName":"custom-scheduler"}`),
		},
	}
	b := New(cl, cl.Scheme(), recorder, profile)
	assert.NoError(t, b.Init(nil))

	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pcs",
			Namespace: "default",
			Labels:    map[string]string{queueNameLabel: "configured-queue"},
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 7}},
				},
			},
		},
	}
	rackKey := "topology.ai-dynamo.io/rack"
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pcs-0", Namespace: "default"},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{
					Name: "test-pcs-0-worker",
					TopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{
						PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: &rackKey},
					},
				},
			},
		},
	}
	require.NoError(t, cl.Create(context.Background(), pcs))
	require.NoError(t, cl.Create(context.Background(), podGang))
	require.NoError(t, cl.Create(context.Background(), newStandalonePodClique("default", "test-pcs-0-worker", "test-pcs")))
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default",
		Labels: map[string]string{
			apicommon.LabelPartOfKey: "test-pcs",
			apicommon.LabelPodClique: "test-pcs-0-worker",
			apicommon.LabelPodGang:   "test-pcs-0",
		},
	}}

	require.NoError(t, b.PreparePod(pod))

	assert.Equal(t, "custom-scheduler", pod.Spec.SchedulerName)
	assert.Equal(t, "configured-queue", pod.Labels[queueNameLabel])
	assert.Equal(t, "test-pcs-0", pod.Labels[podGroupNameLabel])
	assert.Equal(t, "7", pod.Annotations[podGroupTotalCountAnnotation])
	assert.Equal(t, "topology.ai-dynamo.io/rack", pod.Annotations[podSetRequiredTopologyAnnotation])
}

func TestBackend_PreparePod_PCSGAndTopology(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
	b := New(cl, cl.Scheme(), recorder, profile)
	require.NoError(t, b.Init(nil))

	pcsgReplicas := int32(2)
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
			Labels:    map[string]string{queueNameLabel: "grove-poc"},
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{},
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "leader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2}},
				},
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{Name: "decode", CliqueNames: []string{"leader", "worker"}, Replicas: &pcsgReplicas},
				},
			},
		},
	}
	pclq := newPCSGPodClique("default", "demo-0-decode-0-worker", "demo-0-decode")
	rackKey := "topology.ai-dynamo.io/rack"
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-0", Namespace: "default"},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{Name: "demo-0-decode-0-leader"},
				{
					Name: "demo-0-decode-0-worker",
					TopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{
						PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: &rackKey},
					},
				},
			},
		},
	}
	require.NoError(t, cl.Create(context.Background(), pcs))
	require.NoError(t, cl.Create(context.Background(), podGang))
	require.NoError(t, cl.Create(context.Background(), newPCSGPodClique("default", "demo-0-decode-0-leader", "demo-0-decode")))
	require.NoError(t, cl.Create(context.Background(), pclq))
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default",
		Labels: map[string]string{
			apicommon.LabelPartOfKey: "demo",
			apicommon.LabelPodClique: pclq.Name,
			apicommon.LabelPodGang:   "demo-0",
		},
	}}

	require.NoError(t, b.PreparePod(pod))

	assert.Equal(t, "3", pod.Annotations[podGroupTotalCountAnnotation])
	assert.Equal(t, "demo-0-decode-0-worker", pod.Annotations[roleHashAnnotation])
	assert.Equal(t, rackKey, pod.Annotations[podSetRequiredTopologyAnnotation])
}

func TestBackend_TopologyRequestForPodGroup_CombinesRequiredAndPreferred(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
	backend := New(cl, cl.Scheme(), recorder, profile)
	require.NoError(t, backend.Init(nil))
	b := backend.(*schedulerBackend)

	rackKey := "topology.ai-dynamo.io/rack"
	hostKey := "topology.ai-dynamo.io/host"
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-0", Namespace: "default"},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{
					Name: "demo-0-worker",
					TopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{
						PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: &rackKey, Preferred: &hostKey},
					},
				},
			},
		},
	}

	got := b.topologyRequestForPodGroup(podGang, "demo-0-worker")

	require.NotNil(t, got)
	require.NotNil(t, got.request.Required)
	require.NotNil(t, got.request.Preferred)
	assert.Equal(t, rackKey, *got.request.Required)
	assert.Equal(t, hostKey, *got.request.Preferred)
	assert.Equal(t, rackKey, got.annotations[podSetRequiredTopologyAnnotation])
	assert.Equal(t, hostKey, got.annotations[podSetPreferredTopologyAnnotation])
}

func TestBackend_PreparePod_RequiresPodGangForTopology(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
	b := New(cl, cl.Scheme(), recorder, profile)
	require.NoError(t, b.Init(nil))

	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
			Labels:    map[string]string{queueNameLabel: "grove-poc"},
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{},
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
				},
			},
		},
	}
	require.NoError(t, cl.Create(context.Background(), pcs))
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default",
		Labels: map[string]string{
			apicommon.LabelPartOfKey: "demo",
			apicommon.LabelPodClique: "demo-0-worker",
			apicommon.LabelPodGang:   "demo-0",
		},
	}}

	err := b.PreparePod(pod)

	require.ErrorContains(t, err, "failed to get PodGang default/demo-0 when preparing Pod")
}

func TestBackend_SyncPodGang_RequiresPodCliqueSetLabel(t *testing.T) {
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-0", Namespace: "default"},
	}
	cl := testutils.NewTestClientBuilder().WithObjects(podGang).Build()
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue})
	require.NoError(t, b.Init(nil))

	require.ErrorContains(t, b.SyncPodGang(context.Background(), podGang), `must set label "app.kubernetes.io/part-of"`)
}

func TestBackend_SyncPodGang_RequiresPodCliqueSetQueueLabel(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
				},
			},
		},
	}
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-0",
			Namespace: "default",
			Labels:    map[string]string{apicommon.LabelPartOfKey: "demo"},
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{{Name: "demo-0-worker", MinReplicas: 1}},
		},
	}
	cl := testutils.NewTestClientBuilder().WithObjects(pcs, podGang).Build()
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
	b := New(cl, cl.Scheme(), recorder, profile)
	require.NoError(t, b.Init(nil))

	require.ErrorContains(t, b.SyncPodGang(context.Background(), podGang), `must set label "kueue.x-k8s.io/queue-name"`)
}

func TestBackend_SyncPodGang_CreatesPrebuiltWorkloadForSimplePCS(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
			Labels:    map[string]string{queueNameLabel: "grove-poc"},
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4}},
				},
			},
		},
	}
	cl := testutils.NewTestClientBuilder().WithObjects(
		pcs,
		newStandalonePodClique("default", "demo-0-worker", "demo"),
	).Build()
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
	b := New(cl, cl.Scheme(), recorder, profile)
	require.NoError(t, b.Init(nil))

	rackKey := "topology.ai-dynamo.io/rack"
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-0",
			Namespace: "default",
			UID:       "demo-0-uid",
			Labels:    map[string]string{apicommon.LabelPartOfKey: "demo"},
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{
					Name:        "demo-0-worker",
					MinReplicas: 2,
					TopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{
						PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: &rackKey},
					},
				},
			},
		},
	}

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	got := &kueuev1beta2.Workload{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "demo-0"}, got))

	assert.Equal(t, kueuev1beta2.LocalQueueName("grove-poc"), got.Spec.QueueName)

	ownerRefs := got.GetOwnerReferences()
	require.Len(t, ownerRefs, 1)
	assert.Equal(t, "PodGang", ownerRefs[0].Kind)
	assert.Equal(t, "demo-0", ownerRefs[0].Name)

	require.Len(t, got.Spec.PodSets, 1)
	podSet := got.Spec.PodSets[0]
	assert.Equal(t, kueuev1beta2.NewPodSetReference("demo-0-worker"), podSet.Name)
	assert.Equal(t, int32(4), podSet.Count)
	require.NotNil(t, podSet.MinCount)
	assert.Equal(t, int32(2), *podSet.MinCount)
	require.NotNil(t, podSet.TopologyRequest)
	require.NotNil(t, podSet.TopologyRequest.Required)
	assert.Equal(t, rackKey, *podSet.TopologyRequest.Required)
}

func TestBackend_SyncPodGang_RejectsInvalidMinReplicas(t *testing.T) {
	testCases := []struct {
		name        string
		minReplicas int32
	}{
		{name: "zero", minReplicas: 0},
		{name: "greater than replicas", minReplicas: 5},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "demo",
					Namespace: "default",
					Labels:    map[string]string{queueNameLabel: "grove-poc"},
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4}},
						},
					},
				},
			}
			podGang := &groveschedulerv1alpha1.PodGang{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "demo-0",
					Namespace: "default",
					Labels:    map[string]string{apicommon.LabelPartOfKey: "demo"},
				},
				Spec: groveschedulerv1alpha1.PodGangSpec{
					PodGroups: []groveschedulerv1alpha1.PodGroup{
						{Name: "demo-0-worker", MinReplicas: tt.minReplicas},
					},
				},
			}
			cl := testutils.NewTestClientBuilder().WithObjects(
				pcs,
				newStandalonePodClique("default", "demo-0-worker", "demo"),
			).Build()
			b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue})
			require.NoError(t, b.Init(nil))

			require.ErrorContains(t, b.SyncPodGang(context.Background(), podGang), "outside the valid range [1, 4]")
		})
	}
}

func TestBackend_SyncPodGang_RejectsMultiplePartiallyAdmittedPodGroups(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
			Labels:    map[string]string{queueNameLabel: "grove-poc"},
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4}},
					{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3}},
				},
			},
		},
	}
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-0",
			Namespace: "default",
			Labels:    map[string]string{apicommon.LabelPartOfKey: "demo"},
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{Name: "demo-0-worker", MinReplicas: 2},
				{Name: "demo-0-frontend", MinReplicas: 1},
			},
		},
	}
	cl := testutils.NewTestClientBuilder().WithObjects(
		pcs,
		newStandalonePodClique("default", "demo-0-worker", "demo"),
		newStandalonePodClique("default", "demo-0-frontend", "demo"),
	).Build()
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue})
	require.NoError(t, b.Init(nil))

	require.ErrorContains(t, b.SyncPodGang(context.Background(), podGang), "has more than one partially admitted standalone PodGroup")
}

func TestBackend_SyncPodGang_CreatesPrebuiltWorkloadForPCSGWithMinCountEqualsCount(t *testing.T) {
	pcsgReplicas := int32(2)
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
			Labels:    map[string]string{queueNameLabel: "grove-poc"},
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "leader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
					{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2}},
				},
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{Name: "decode", CliqueNames: []string{"leader", "worker"}, Replicas: &pcsgReplicas},
				},
			},
		},
	}
	cl := testutils.NewTestClientBuilder().WithObjects(
		pcs,
		newPCSGPodClique("default", "demo-0-decode-0-worker", "demo-0-decode"),
	).Build()
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
	b := New(cl, cl.Scheme(), recorder, profile)
	require.NoError(t, b.Init(nil))

	preferredTopologyKey := "kubernetes.io/hostname"
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-0",
			Namespace: "default",
			Labels:    map[string]string{apicommon.LabelPartOfKey: "demo"},
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				// MinReplicas is deliberately lower than the clique replicas to prove the PCSG
				// all-or-nothing override forces minCount == count.
				{
					Name:        "demo-0-decode-0-worker",
					MinReplicas: 1,
					TopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{
						PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Preferred: &preferredTopologyKey},
					},
				},
			},
		},
	}

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	got := &kueuev1beta2.Workload{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "demo-0"}, got))

	require.Len(t, got.Spec.PodSets, 1)
	podSet := got.Spec.PodSets[0]
	assert.Equal(t, kueuev1beta2.NewPodSetReference("demo-0-decode-0-worker"), podSet.Name)
	assert.Equal(t, int32(2), podSet.Count)
	require.NotNil(t, podSet.TopologyRequest)
	require.NotNil(t, podSet.TopologyRequest.Preferred)
	assert.Equal(t, preferredTopologyKey, *podSet.TopologyRequest.Preferred)
	assert.Equal(t, preferredTopologyKey, podSet.Template.Annotations[podSetPreferredTopologyAnnotation])
	// PodCliqueScalingGroup cliques are all-or-nothing: minCount is omitted (Kueue defaults it to count).
	// Kueue also rejects Workloads where more than one podSet sets minCount.
	assert.Nil(t, podSet.MinCount)
}

func TestBackend_ValidatePodCliqueSet_MinCount(t *testing.T) {
	testCases := []struct {
		description string
		cliques     []*grovecorev1alpha1.PodCliqueTemplateSpec
		pcsgConfigs []grovecorev1alpha1.PodCliqueScalingGroupConfig
		wantErr     bool
		wantErrMsg  string
	}{
		{
			description: "no partial-gang standalone clique is valid",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4, MinAvailable: ptr.To[int32](4)}},
			},
		},
		{
			description: "single partial-gang standalone clique is valid",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4, MinAvailable: ptr.To[int32](2)}},
				{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2, MinAvailable: ptr.To[int32](2)}},
			},
		},
		{
			description: "two partial-gang standalone cliques are rejected",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4, MinAvailable: ptr.To[int32](2)}},
				{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To[int32](1)}},
			},
			wantErr:    true,
			wantErrMsg: "at most one standalone PodClique with minAvailable < replicas",
		},
		{
			description: "scaling-group cliques with minAvailable == replicas are valid",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "leader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To[int32](1)}},
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4, MinAvailable: ptr.To[int32](4)}},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "decode", CliqueNames: []string{"leader", "worker"}},
			},
		},
		{
			description: "scaling-group clique with minAvailable < replicas is rejected",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "leader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To[int32](1)}},
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4, MinAvailable: ptr.To[int32](3)}},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "decode", CliqueNames: []string{"leader", "worker"}},
			},
			wantErr:    true,
			wantErrMsg: "members of a PodCliqueScalingGroup to set minAvailable == replicas",
		},
		{
			description: "nil minAvailable is treated as full gang",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4}},
				{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To[int32](1)}},
			},
		},
		{
			description: "PCSG with minAvailable == replicas is valid",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "leader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4, MinAvailable: ptr.To[int32](4)}},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "decode", CliqueNames: []string{"leader"}, Replicas: ptr.To[int32](4), MinAvailable: ptr.To[int32](4)},
			},
		},
		{
			description: "PCSG with minAvailable < replicas is rejected",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "leader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4, MinAvailable: ptr.To[int32](4)}},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "decode", CliqueNames: []string{"leader"}, Replicas: ptr.To[int32](4), MinAvailable: ptr.To[int32](2)},
			},
			wantErr:    true,
			wantErrMsg: "PodCliqueScalingGroups to set minAvailable == replicas",
		},
		{
			description: "PCSG with nil replicas or minAvailable defers to CRD defaulting",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "leader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To[int32](1)}},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "decode", CliqueNames: []string{"leader"}},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			cl := testutils.CreateDefaultFakeClient(nil)
			recorder := record.NewFakeRecorder(10)
			profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
			b := New(cl, cl.Scheme(), recorder, profile)
			require.NoError(t, b.Init(nil))

			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques:                      tc.cliques,
						PodCliqueScalingGroupConfigs: tc.pcsgConfigs,
					},
				},
			}

			err := b.ValidatePodCliqueSet(context.Background(), pcs)
			if tc.wantErr {
				require.ErrorContains(t, err, tc.wantErrMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestBackend_TopologyGVR(t *testing.T) {
	b := newKueueBackend(testutils.CreateDefaultFakeClient(nil))

	assert.Equal(t, schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta2",
		Resource: "topologies",
	}, b.TopologyGVR())
}

func TestBackend_SyncTopologyCreatesKueueTopology(t *testing.T) {
	ctx := context.Background()
	cl := testutils.CreateDefaultFakeClient(nil)
	b := newKueueBackend(cl)
	ct := testClusterTopology()

	require.NoError(t, b.SyncTopology(ctx, cl, ct))

	topology := newKueueTopology(ct.Name)
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: ct.Name}, topology))
	assert.True(t, metav1.IsControlledBy(topology, ct))
	assert.Equal(t, []any{
		map[string]any{"nodeLabel": "topology.ai-dynamo.io/rack"},
		map[string]any{"nodeLabel": "kubernetes.io/hostname"},
	}, kueueTopologyLevels(topology))
}

func TestBackend_CheckTopologyDrift(t *testing.T) {
	ctx := context.Background()
	ct := testClusterTopology()
	topology, err := buildKueueTopology(ct.Name, ct, testutils.CreateDefaultFakeClient(nil).Scheme())
	require.NoError(t, err)
	cl := testutils.NewTestClientBuilder().WithObjects(ct, topology).Build()
	b := newKueueBackend(cl)

	inSync, message, _, err := b.CheckTopologyDrift(ctx, ct, grovecorev1alpha1.SchedulerTopologyBinding{
		SchedulerName:     string(configv1alpha1.SchedulerNameKueue),
		TopologyReference: ct.Name,
	})

	require.NoError(t, err)
	assert.True(t, inSync)
	assert.Empty(t, message)
}

func newStandalonePodClique(namespace, name, pcsName string) *grovecorev1alpha1.PodClique {
	return &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{
		Namespace: namespace,
		Name:      name,
		Labels: map[string]string{
			apicommon.LabelPartOfKey:                pcsName,
			apicommon.LabelPodCliqueSetReplicaIndex: "0",
		},
	}}
}

func newPCSGPodClique(namespace, name, pcsgName string) *grovecorev1alpha1.PodClique {
	return &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{
		Namespace: namespace,
		Name:      name,
		Labels: map[string]string{
			apicommon.LabelPodCliqueScalingGroup:             pcsgName,
			apicommon.LabelPodCliqueScalingGroupReplicaIndex: "0",
		},
	}}
}

func newKueueBackend(cl client.Client) scheduler.TopologyAwareBackend {
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
	b := New(cl, cl.Scheme(), recorder, profile)
	return b.(scheduler.TopologyAwareBackend)
}

func testClusterTopology() *grovecorev1alpha1.ClusterTopologyBinding {
	return &grovecorev1alpha1.ClusterTopologyBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "grove-kind-topology",
			UID:  uuid.NewUUID(),
		},
		Spec: grovecorev1alpha1.ClusterTopologyBindingSpec{
			Levels: []grovecorev1alpha1.TopologyLevel{
				{Domain: grovecorev1alpha1.TopologyDomainRack, Key: "topology.ai-dynamo.io/rack"},
				{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
			},
		},
	}
}

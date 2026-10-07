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
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kueuev1beta2 "sigs.k8s.io/kueue/apis/kueue/v1beta2"
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
			PodGroups: []groveschedulerv1alpha1.PodGroup{{Name: "test-pcs-0-worker", MinReplicas: 2}},
		},
	}
	require.NoError(t, cl.Create(context.Background(), pcs))
	require.NoError(t, cl.Create(context.Background(), podGang))
	require.NoError(t, cl.Create(context.Background(), newStandalonePodClique("test-pcs-0-worker", "test-pcs")))
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
	assert.Equal(t, "true", pod.Annotations[podGroupServingAnnotation])
	assert.NotContains(t, pod.Annotations, "kueue.x-k8s.io/retriable-in-group")
}

func TestBackend_RemovePodFinalizers(t *testing.T) {
	const otherFinalizer = "example.com/other"
	newPod := func(finalizers ...string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker-0", Namespace: "default", Finalizers: finalizers}}
	}
	tests := []struct {
		name           string
		pod            *corev1.Pod
		stored         bool
		wantFinalizers []string
	}{
		{name: "removes Kueue's finalizer and keeps others", pod: newPod(podFinalizer, otherFinalizer), stored: true, wantFinalizers: []string{otherFinalizer}},
		{name: "leaves a Pod without Kueue's finalizer alone", pod: newPod(otherFinalizer), stored: true, wantFinalizers: []string{otherFinalizer}},
		{name: "ignores a Pod that no longer exists", pod: newPod(podFinalizer)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var existing []client.Object
			if tc.stored {
				existing = append(existing, tc.pod.DeepCopy())
			}
			cl := testutils.CreateDefaultFakeClient(existing)
			b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue})
			require.NoError(t, b.Init(nil))

			pod := tc.pod
			if tc.stored {
				pod = &corev1.Pod{}
				require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(tc.pod), pod))
			}
			require.NoError(t, b.(scheduler.Finalizer).RemovePodFinalizers(context.Background(), pod))

			if tc.stored {
				got := &corev1.Pod{}
				require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(tc.pod), got))
				assert.Equal(t, tc.wantFinalizers, got.Finalizers)
			}
		})
	}
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
					Name:        "test-pcs-0-worker",
					MinReplicas: 7,
					TopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{
						PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: &rackKey},
					},
				},
			},
		},
	}
	require.NoError(t, cl.Create(context.Background(), pcs))
	require.NoError(t, cl.Create(context.Background(), podGang))
	require.NoError(t, cl.Create(context.Background(), newStandalonePodClique("test-pcs-0-worker", "test-pcs")))
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
	pclq := newPCSGPodClique("demo-0-decode-0-worker")
	rackKey := "topology.ai-dynamo.io/rack"
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-0", Namespace: "default"},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{Name: "demo-0-decode-0-leader", MinReplicas: 1},
				{
					Name:        "demo-0-decode-0-worker",
					MinReplicas: 2,
					TopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{
						PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: &rackKey},
					},
				},
			},
		},
	}
	require.NoError(t, cl.Create(context.Background(), pcs))
	require.NoError(t, cl.Create(context.Background(), podGang))
	require.NoError(t, cl.Create(context.Background(), newPCSGPodClique("demo-0-decode-0-leader")))
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
		newStandalonePodClique("demo-0-worker", "demo"),
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
	assert.Equal(t, "true", got.Annotations[isGroupWorkloadAnnotation])

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

// newTestPCSAndPodGang returns a single-clique PodCliqueSet and matching PodGang fixture for the
// ensureWorkload repair tests: a Workload named after the PodGang, and a PodCliqueSet whose
// queueNameLabel differs from the "stale-queue" value the repair tests seed onto a pre-existing
// Workload, so a rebuilt Workload is distinguishable from an untouched one by QueueName alone.
func newTestPCSAndPodGang() (*grovecorev1alpha1.PodCliqueSet, *groveschedulerv1alpha1.PodGang) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
			Labels:    map[string]string{queueNameLabel: "grove-poc"},
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
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-0",
			Namespace: "default",
			UID:       "demo-0-uid",
			Labels:    map[string]string{apicommon.LabelPartOfKey: "demo"},
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{{Name: "demo-0-worker", MinReplicas: 2}},
		},
	}
	return pcs, podGang
}

func TestBackend_SyncPodGang_LeavesHealthyWorkloadUntouched(t *testing.T) {
	pcs, podGang := newTestPCSAndPodGang()
	cl := testutils.NewTestClientBuilder().WithObjects(pcs, newStandalonePodClique("demo-0-worker", "demo")).Build()
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue})
	require.NoError(t, b.Init(nil))
	staleWorkload := &kueuev1beta2.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-0", Namespace: "default"},
		Spec:       kueuev1beta2.WorkloadSpec{QueueName: "stale-queue"},
	}
	require.NoError(t, cl.Create(context.Background(), staleWorkload))

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	got := &kueuev1beta2.Workload{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "demo-0"}, got))
	assert.Equal(t, kueuev1beta2.LocalQueueName("stale-queue"), got.Spec.QueueName, "a healthy existing Workload must not be rebuilt")
}

func TestBackend_SyncPodGang_RecreatesFinishedWorkload(t *testing.T) {
	pcs, podGang := newTestPCSAndPodGang()
	cl := testutils.NewTestClientBuilder().WithObjects(pcs, newStandalonePodClique("demo-0-worker", "demo")).Build()
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue})
	require.NoError(t, b.Init(nil))
	finishedWorkload := &kueuev1beta2.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-0", Namespace: "default"},
		Spec:       kueuev1beta2.WorkloadSpec{QueueName: "stale-queue"},
		Status: kueuev1beta2.WorkloadStatus{
			Conditions: []metav1.Condition{
				{Type: kueuev1beta2.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "Test", Message: "test"},
			},
		},
	}
	require.NoError(t, cl.Create(context.Background(), finishedWorkload))

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	got := &kueuev1beta2.Workload{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "demo-0"}, got))
	assert.Equal(t, kueuev1beta2.LocalQueueName("grove-poc"), got.Spec.QueueName, "a Finished Workload must be deleted and rebuilt")
	assert.Empty(t, got.Status.Conditions, "the rebuilt Workload must not carry over the old Finished condition")
}

func TestBackend_SyncPodGang_RecreatesDeactivatedWorkload(t *testing.T) {
	pcs, podGang := newTestPCSAndPodGang()
	cl := testutils.NewTestClientBuilder().WithObjects(pcs, newStandalonePodClique("demo-0-worker", "demo")).Build()
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue})
	require.NoError(t, b.Init(nil))
	deactivatedWorkload := &kueuev1beta2.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-0", Namespace: "default"},
		Spec:       kueuev1beta2.WorkloadSpec{QueueName: "stale-queue", Active: ptr.To(false)},
	}
	require.NoError(t, cl.Create(context.Background(), deactivatedWorkload))

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	got := &kueuev1beta2.Workload{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "demo-0"}, got))
	assert.Equal(t, kueuev1beta2.LocalQueueName("grove-poc"), got.Spec.QueueName, "a deactivated Workload must be deleted and rebuilt")
}

func TestBackend_PreparePod_RecreatesFinishedWorkload(t *testing.T) {
	pcs, podGang := newTestPCSAndPodGang()
	pclq := newStandalonePodClique("demo-0-worker", "demo")
	cl := testutils.NewTestClientBuilder().WithObjects(pcs, podGang, pclq).Build()
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue})
	require.NoError(t, b.Init(nil))
	finishedWorkload := &kueuev1beta2.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-0", Namespace: "default"},
		Spec:       kueuev1beta2.WorkloadSpec{QueueName: "stale-queue"},
		Status: kueuev1beta2.WorkloadStatus{
			Conditions: []metav1.Condition{
				{Type: kueuev1beta2.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "Test", Message: "test"},
			},
		},
	}
	require.NoError(t, cl.Create(context.Background(), finishedWorkload))

	// A replacement Pod being prepared after Kueue preempted and finished the group.
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default",
		Labels: map[string]string{
			apicommon.LabelPartOfKey: "demo",
			apicommon.LabelPodClique: "demo-0-worker",
			apicommon.LabelPodGang:   "demo-0",
		},
	}}

	require.NoError(t, b.PreparePod(pod))

	got := &kueuev1beta2.Workload{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "demo-0"}, got))
	assert.Equal(t, kueuev1beta2.LocalQueueName("grove-poc"), got.Spec.QueueName, "preparing a replacement Pod must repair a Finished Workload")
}

// TestBackend_PreparePod_UsesDirectReaderForFinishedCheck proves PreparePod repairs a Workload that
// only an uncached read can see as Finished: the cached client (passed to New) still holds a healthy
// copy, while the directClient (passed to Init) holds the Finished one.
func TestBackend_PreparePod_UsesDirectReaderForFinishedCheck(t *testing.T) {
	pcs, podGang := newTestPCSAndPodGang()
	pclq := newStandalonePodClique("demo-0-worker", "demo")
	cachedCl := testutils.NewTestClientBuilder().Build()
	directCl := testutils.NewTestClientBuilder().Build()
	b := New(cachedCl, cachedCl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue})
	require.NoError(t, b.Init(directCl))
	require.NoError(t, cachedCl.Create(context.Background(), pcs))
	require.NoError(t, cachedCl.Create(context.Background(), podGang))
	require.NoError(t, cachedCl.Create(context.Background(), pclq))

	healthyWorkload := &kueuev1beta2.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-0", Namespace: "default"},
		Spec:       kueuev1beta2.WorkloadSpec{QueueName: "stale-queue"},
	}
	require.NoError(t, cachedCl.Create(context.Background(), healthyWorkload))
	finishedWorkload := &kueuev1beta2.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-0", Namespace: "default"},
		Spec:       kueuev1beta2.WorkloadSpec{QueueName: "stale-queue"},
		Status: kueuev1beta2.WorkloadStatus{
			Conditions: []metav1.Condition{
				{Type: kueuev1beta2.WorkloadFinished, Status: metav1.ConditionTrue, Reason: "Test", Message: "test"},
			},
		},
	}
	require.NoError(t, directCl.Create(context.Background(), finishedWorkload))

	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default",
		Labels: map[string]string{
			apicommon.LabelPartOfKey: "demo",
			apicommon.LabelPodClique: "demo-0-worker",
			apicommon.LabelPodGang:   "demo-0",
		},
	}}

	require.NoError(t, b.PreparePod(pod))

	got := &kueuev1beta2.Workload{}
	require.NoError(t, cachedCl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "demo-0"}, got))
	assert.Equal(t, kueuev1beta2.LocalQueueName("grove-poc"), got.Spec.QueueName, "PreparePod must rebuild based on the direct reader's Finished status, even though the cached client still reports the Workload healthy")
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
				newStandalonePodClique("demo-0-worker", "demo"),
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
		newStandalonePodClique("demo-0-worker", "demo"),
		newStandalonePodClique("demo-0-frontend", "demo"),
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
		newPCSGPodClique("demo-0-decode-0-worker"),
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

// TestBackend_SyncPodGang_UsesTopologyConstraintGroupConfig proves buildPrebuiltWorkload reads
// PodGang.Spec.TopologyConstraintGroupConfigs: a PCSG that declares its own topology constraint
// (distinct from each member clique's own) must still reach the Kueue Workload's PodSets, even
// though neither member PodGroup carries a per-PodGroup TopologyConstraint of its own.
func TestBackend_SyncPodGang_UsesTopologyConstraintGroupConfig(t *testing.T) {
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
		newPCSGPodClique("demo-0-decode-0-leader"),
		newPCSGPodClique("demo-0-decode-0-worker"),
	).Build()
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
	b := New(cl, cl.Scheme(), recorder, profile)
	require.NoError(t, b.Init(nil))

	groupTopologyKey := "topology.kubernetes.io/rack"
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-0",
			Namespace: "default",
			Labels:    map[string]string{apicommon.LabelPartOfKey: "demo"},
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				// Neither PodGroup carries its own TopologyConstraint; the constraint lives only on
				// the PCSG's TopologyConstraintGroupConfig below.
				{Name: "demo-0-decode-0-leader", MinReplicas: 1},
				{Name: "demo-0-decode-0-worker", MinReplicas: 2},
			},
			TopologyConstraintGroupConfigs: []groveschedulerv1alpha1.TopologyConstraintGroupConfig{
				{
					Name:          "decode",
					PodGroupNames: []string{"demo-0-decode-0-leader", "demo-0-decode-0-worker"},
					TopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{
						PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: &groupTopologyKey},
					},
				},
			},
		},
	}

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	got := &kueuev1beta2.Workload{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "demo-0"}, got))

	require.Len(t, got.Spec.PodSets, 2)
	for _, podSet := range got.Spec.PodSets {
		require.NotNilf(t, podSet.TopologyRequest, "podSet %s", podSet.Name)
		require.NotNilf(t, podSet.TopologyRequest.Required, "podSet %s", podSet.Name)
		assert.Equalf(t, groupTopologyKey, *podSet.TopologyRequest.Required, "podSet %s", podSet.Name)
		assert.Equalf(t, groupTopologyKey, podSet.Template.Annotations[podSetRequiredTopologyAnnotation], "podSet %s", podSet.Name)
	}
}

// TestBackend_SyncPodGang_SortsPodSetsByName proves the Workload lists its PodSets in name order (the order
// Kueue checks them in), not in the PodGang's order.
func TestBackend_SyncPodGang_SortsPodSetsByName(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
			Labels:    map[string]string{queueNameLabel: "grove-poc"},
		},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "prefill", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2}},
					{Name: "decode", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
				},
			},
		},
	}
	cl := testutils.NewTestClientBuilder().WithObjects(
		pcs,
		newStandalonePodClique("demo-0-prefill", "demo"),
		newStandalonePodClique("demo-0-decode", "demo"),
	).Build()
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue})
	require.NoError(t, b.Init(nil))
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-0",
			Namespace: "default",
			Labels:    map[string]string{apicommon.LabelPartOfKey: "demo"},
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{Name: "demo-0-prefill", MinReplicas: 2},
				{Name: "demo-0-decode", MinReplicas: 1},
			},
		},
	}

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	got := &kueuev1beta2.Workload{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "demo-0"}, got))
	require.Len(t, got.Spec.PodSets, 2)
	assert.Equal(t, kueuev1beta2.NewPodSetReference("demo-0-decode"), got.Spec.PodSets[0].Name)
	assert.Equal(t, int32(1), got.Spec.PodSets[0].Count)
	assert.Equal(t, kueuev1beta2.NewPodSetReference("demo-0-prefill"), got.Spec.PodSets[1].Name)
	assert.Equal(t, int32(2), got.Spec.PodSets[1].Count)
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

func newStandalonePodClique(name, pcsName string) *grovecorev1alpha1.PodClique {
	return &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default",
		Name:      name,
		Labels: map[string]string{
			apicommon.LabelPartOfKey:                pcsName,
			apicommon.LabelPodCliqueSetReplicaIndex: "0",
		},
	}}
}

func newPCSGPodClique(name string) *grovecorev1alpha1.PodClique {
	return &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default",
		Name:      name,
		Labels: map[string]string{
			apicommon.LabelPodCliqueScalingGroup:             "demo-0-decode",
			apicommon.LabelPodCliqueScalingGroupReplicaIndex: "0",
		},
	}}
}

func TestBackend_ValidatePodCliqueScale(t *testing.T) {
	testCases := []struct {
		description string
		oldReplicas int32
		newReplicas int32
		wantErr     bool
	}{
		{description: "unchanged replica count is allowed", oldReplicas: 4, newReplicas: 4},
		{description: "scale out is rejected", oldReplicas: 4, newReplicas: 7, wantErr: true},
		{description: "scale in is rejected", oldReplicas: 4, newReplicas: 2, wantErr: true},
		{description: "scale to zero is rejected", oldReplicas: 4, newReplicas: 0, wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			cl := testutils.CreateDefaultFakeClient(nil)
			recorder := record.NewFakeRecorder(10)
			profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
			b := New(cl, cl.Scheme(), recorder, profile)
			require.NoError(t, b.Init(nil))

			scaleValidator, ok := b.(scheduler.PodCliqueScaleValidator)
			require.True(t, ok, "kueue backend must implement scheduler.PodCliqueScaleValidator")

			err := scaleValidator.ValidatePodCliqueScale(context.Background(), tc.oldReplicas, tc.newReplicas)
			if tc.wantErr {
				require.ErrorContains(t, err, "does not support scaling a PodClique")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestBackend_ValidatePodCliqueSet_AutoScalingConfig(t *testing.T) {
	testCases := []struct {
		description string
		cliques     []*grovecorev1alpha1.PodCliqueTemplateSpec
		pcsgConfigs []grovecorev1alpha1.PodCliqueScalingGroupConfig
		wantErr     bool
	}{
		{
			description: "cliques without autoScalingConfig are valid",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4, MinAvailable: ptr.To[int32](4)}},
			},
		},
		{
			description: "clique declaring autoScalingConfig is rejected",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{
					Replicas:     4,
					MinAvailable: ptr.To[int32](4),
					ScaleConfig:  &grovecorev1alpha1.AutoScalingConfig{MaxReplicas: 10},
				}},
			},
			wantErr: true,
		},
		{
			description: "autoScalingConfig on a PodCliqueScalingGroup is allowed",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "leader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To[int32](1)}},
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4, MinAvailable: ptr.To[int32](4)}},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:        "decode",
					CliqueNames: []string{"leader", "worker"},
					ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{MaxReplicas: 10},
				},
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
				require.ErrorContains(t, err, "does not support autoScalingConfig on a PodClique")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestBackend_ValidatePodCliqueSet_TopologyRequiredAndPreferred(t *testing.T) {
	testCases := []struct {
		description      string
		pcsConstraint    *grovecorev1alpha1.TopologyConstraint
		cliqueConstraint *grovecorev1alpha1.TopologyConstraint
		wantErr          bool
	}{
		{
			description: "no topology constraint is valid",
		},
		{
			description:      "clique with only a required domain is valid",
			cliqueConstraint: &grovecorev1alpha1.TopologyConstraint{Pack: &grovecorev1alpha1.TopologyPackConstraint{RequiredDomain: "rack"}},
		},
		{
			description:      "clique with only a preferred domain is valid",
			cliqueConstraint: &grovecorev1alpha1.TopologyConstraint{Pack: &grovecorev1alpha1.TopologyPackConstraint{PreferredDomain: "host"}},
		},
		{
			description:      "clique resolving both required and preferred from its own constraint is rejected",
			cliqueConstraint: &grovecorev1alpha1.TopologyConstraint{Pack: &grovecorev1alpha1.TopologyPackConstraint{RequiredDomain: "rack", PreferredDomain: "host"}},
			wantErr:          true,
		},
		{
			description:      "clique inheriting a required domain from the PodCliqueSet on top of its own preferred domain is rejected",
			pcsConstraint:    &grovecorev1alpha1.TopologyConstraint{Pack: &grovecorev1alpha1.TopologyPackConstraint{RequiredDomain: "rack"}},
			cliqueConstraint: &grovecorev1alpha1.TopologyConstraint{Pack: &grovecorev1alpha1.TopologyPackConstraint{PreferredDomain: "host"}},
			wantErr:          true,
		},
		{
			description:   "PodCliqueSet-level required and preferred domains inherited by a clique with no constraint of its own are rejected",
			pcsConstraint: &grovecorev1alpha1.TopologyConstraint{Pack: &grovecorev1alpha1.TopologyPackConstraint{RequiredDomain: "rack", PreferredDomain: "host"}},
			wantErr:       true,
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
						TopologyConstraint: tc.pcsConstraint,
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{
								Name:               "worker",
								Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 4, MinAvailable: ptr.To[int32](4)},
								TopologyConstraint: tc.cliqueConstraint,
							},
						},
					},
				},
			}

			err := b.ValidatePodCliqueSet(context.Background(), pcs)
			if tc.wantErr {
				require.ErrorContains(t, err, "resolving both a required and a preferred topology domain")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

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

package volcano

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"
	schedulertest "github.com/ai-dynamo/grove/operator/test/utils/scheduler"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	volcanov1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

func TestBackend_PreparePod(t *testing.T) {
	cl := schedulertest.NewVolcanoClient(t)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameVolcano}
	b := New(cl, cl.Scheme(), recorder, profile)

	pod := testutils.NewPodBuilder("test-pod", "default").Build()
	pod.Labels = map[string]string{apicommon.LabelPodGang: "pg-1"}

	require.NoError(t, b.PreparePod(pod))

	assert.Equal(t, string(configv1alpha1.SchedulerNameVolcano), pod.Spec.SchedulerName)
	assert.Equal(t, "pg-1", pod.Annotations[volcanov1beta1.VolcanoGroupNameAnnotationKey])
	assert.Equal(t, "pg-1", pod.Annotations[volcanov1beta1.KubeGroupNameAnnotationKey])
}

func TestBackend_PreparePodFailsWhenPodGangLabelMissing(t *testing.T) {
	cl := schedulertest.NewVolcanoClient(t)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameVolcano}
	b := New(cl, cl.Scheme(), recorder, profile)

	pod := testutils.NewPodBuilder("test-pod", "default").Build()

	require.ErrorContains(t, b.PreparePod(pod), "volcano scheduler requires pod label")
}

func TestBackend_Init(t *testing.T) {
	tests := []struct {
		name    string
		objects []client.Object
		wantErr string
	}{
		{
			name:    "PodGroup CRD exposes subGroupPolicy",
			objects: []client.Object{testutils.NewVolcanoPodGroupCRD(true)},
		},
		{
			name:    "PodGroup CRD missing",
			wantErr: "failed to get CRD",
		},
		{
			name:    "PodGroup CRD does not expose subGroupPolicy",
			objects: []client.Object{testutils.NewVolcanoPodGroupCRD(false)},
			wantErr: "requires Volcano 1.14 or newer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := schedulertest.NewVolcanoClient(t, tt.objects...)
			recorder := record.NewFakeRecorder(10)
			profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameVolcano}
			b := New(cl, cl.Scheme(), recorder, profile)

			err := b.Init(cl)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestBackend_SyncPodGang(t *testing.T) {
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pg-1",
			Namespace: "default",
			UID:       "uid-1",
			Labels: map[string]string{
				apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
			},
			Annotations: map[string]string{
				QueueAnnotationKey: "gpu-training",
			},
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PriorityClassName: "high-priority",
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{Name: "worker", MinReplicas: 2},
				{Name: "ps", MinReplicas: 3},
			},
		},
	}
	cl := schedulertest.NewVolcanoClient(t, podGang)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameVolcano}
	b := New(cl, cl.Scheme(), recorder, profile)

	err := b.SyncPodGang(context.Background(), podGang)
	require.NoError(t, err)

	podGroup := &volcanov1beta1.PodGroup{}
	err = cl.Get(context.Background(), client.ObjectKey{Name: "pg-1", Namespace: "default"}, podGroup)
	require.NoError(t, err)
	assert.Equal(t, int32(5), podGroup.Spec.MinMember)
	assert.Equal(t, "gpu-training", podGroup.Spec.Queue)
	assert.Equal(t, "high-priority", podGroup.Spec.PriorityClassName)
	require.Len(t, podGroup.Spec.SubGroupPolicy, 2)
	assert.Equal(t, "worker", podGroup.Spec.SubGroupPolicy[0].Name)
	assert.Equal(t, map[string]string{apicommon.LabelPodClique: "worker"}, podGroup.Spec.SubGroupPolicy[0].LabelSelector.MatchLabels)
	assert.Equal(t, []string{apicommon.LabelPodClique}, podGroup.Spec.SubGroupPolicy[0].MatchLabelKeys)
	require.NotNil(t, podGroup.Spec.SubGroupPolicy[0].SubGroupSize)
	assert.Equal(t, int32(2), *podGroup.Spec.SubGroupPolicy[0].SubGroupSize)
	require.NotNil(t, podGroup.Spec.SubGroupPolicy[0].MinSubGroups)
	assert.Equal(t, int32(1), *podGroup.Spec.SubGroupPolicy[0].MinSubGroups)
	assert.Equal(t, "ps", podGroup.Spec.SubGroupPolicy[1].Name)
	require.NotNil(t, podGroup.Spec.SubGroupPolicy[1].SubGroupSize)
	assert.Equal(t, int32(3), *podGroup.Spec.SubGroupPolicy[1].SubGroupSize)
	require.NotNil(t, podGroup.Spec.SubGroupPolicy[1].MinSubGroups)
	assert.Equal(t, int32(1), *podGroup.Spec.SubGroupPolicy[1].MinSubGroups)
}

func TestBackend_SyncPodGangPreservesSchedulingConstraintsAfterRelease(t *testing.T) {
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pg-1",
			Namespace: "default",
			UID:       "uid-1",
			Annotations: map[string]string{
				QueueAnnotationKey: "gpu-training",
			},
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PriorityClassName: "high-priority",
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{Name: "worker", MinReplicas: 2},
				{Name: "ps", MinReplicas: 3},
			},
		},
	}
	cl := schedulertest.NewVolcanoClient(t, podGang)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameVolcano}
	b := New(cl, cl.Scheme(), recorder, profile)

	err := b.SyncPodGang(context.Background(), podGang)
	require.NoError(t, err)

	podGang.Spec.PriorityClassName = "released-priority"
	podGang.Spec.PodGroups = []groveschedulerv1alpha1.PodGroup{
		{Name: "worker", MinReplicas: 0},
		{Name: "ps", MinReplicas: 0},
	}

	err = b.SyncPodGang(context.Background(), podGang)
	require.NoError(t, err)

	podGroup := &volcanov1beta1.PodGroup{}
	err = cl.Get(context.Background(), client.ObjectKey{Name: "pg-1", Namespace: "default"}, podGroup)
	require.NoError(t, err)
	assert.Equal(t, int32(5), podGroup.Spec.MinMember)
	assert.Equal(t, "released-priority", podGroup.Spec.PriorityClassName)
	require.Len(t, podGroup.Spec.SubGroupPolicy, 2)
	assert.Equal(t, "worker", podGroup.Spec.SubGroupPolicy[0].Name)
	require.NotNil(t, podGroup.Spec.SubGroupPolicy[0].SubGroupSize)
	assert.Equal(t, int32(2), *podGroup.Spec.SubGroupPolicy[0].SubGroupSize)
	assert.Equal(t, "ps", podGroup.Spec.SubGroupPolicy[1].Name)
	require.NotNil(t, podGroup.Spec.SubGroupPolicy[1].SubGroupSize)
	assert.Equal(t, int32(3), *podGroup.Spec.SubGroupPolicy[1].SubGroupSize)
}

func TestBackend_SyncPodGangDefaultQueue(t *testing.T) {
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pg-1",
			Namespace: "default",
			UID:       "uid-1",
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{Name: "worker", MinReplicas: 2},
			},
		},
	}
	cl := schedulertest.NewVolcanoClient(t, podGang)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameVolcano}
	b := New(cl, cl.Scheme(), recorder, profile)

	err := b.SyncPodGang(context.Background(), podGang)
	require.NoError(t, err)

	podGroup := &volcanov1beta1.PodGroup{}
	err = cl.Get(context.Background(), client.ObjectKey{Name: "pg-1", Namespace: "default"}, podGroup)
	require.NoError(t, err)
	assert.Equal(t, DefaultQueue, podGroup.Spec.Queue)
}

func TestBackend_ValidatePodCliqueSet(t *testing.T) {
	cl := schedulertest.NewVolcanoClient(t)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameVolcano}
	b := New(cl, cl.Scheme(), recorder, profile)

	pcs := &grovecorev1alpha1.PodCliqueSet{
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
					PackDomain: grovecorev1alpha1.TopologyDomainZone,
				},
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{
						Name: "worker",
						Spec: grovecorev1alpha1.PodCliqueSpec{
							PodSpec: corev1.PodSpec{},
						},
					},
				},
			},
		},
	}

	err := b.ValidatePodCliqueSet(context.Background(), pcs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support topologyConstraint")
}

func TestBackend_ValidatePodCliqueSetQueues(t *testing.T) {
	makePCS := func() *grovecorev1alpha1.PodCliqueSet {
		return testutils.NewPodCliqueSetBuilder("volcano", "default", "test-uid").
			WithReplicas(1).
			WithPodCliqueTemplateSpec(
				testutils.NewPodCliqueTemplateSpecBuilder("worker").
					WithReplicas(1).
					WithRoleName("worker-role").
					WithMinAvailable(1).
					WithPodSpec(corev1.PodSpec{
						SchedulerName: string(configv1alpha1.SchedulerNameVolcano),
						Containers:    []corev1.Container{{Name: "worker", Image: "test:latest"}},
					}).
					Build(),
			).
			WithPodCliqueTemplateSpec(
				testutils.NewPodCliqueTemplateSpecBuilder("ps").
					WithReplicas(1).
					WithRoleName("ps-role").
					WithMinAvailable(1).
					WithPodSpec(corev1.PodSpec{
						SchedulerName: string(configv1alpha1.SchedulerNameVolcano),
						Containers:    []corev1.Container{{Name: "ps", Image: "test:latest"}},
					}).
					Build(),
			).
			Build()
	}

	makeQueue := func(name string, state volcanov1beta1.QueueState) *volcanov1beta1.Queue {
		return &volcanov1beta1.Queue{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Status:     volcanov1beta1.QueueStatus{State: state},
		}
	}

	testCases := []struct {
		description      string
		mutate           func(*grovecorev1alpha1.PodCliqueSet)
		existingObjs     []client.Object
		errorContains    string
		errorNotContains string
		errorIs          error
	}{
		{
			description: "global queue is used when cliques do not override",
			mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Annotations = map[string]string{QueueAnnotationKey: "gpu-training"}
			},
			existingObjs: []client.Object{makeQueue("gpu-training", volcanov1beta1.QueueStateOpen)},
		},
		{
			description: "cliques can repeat the same queue as metadata",
			mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Annotations = map[string]string{QueueAnnotationKey: "gpu-training"}
				pcs.Spec.Template.Cliques[0].Annotations = map[string]string{QueueAnnotationKey: "gpu-training"}
			},
			existingObjs: []client.Object{makeQueue("gpu-training", volcanov1beta1.QueueStateOpen)},
		},
		{
			description: "conflicting metadata and clique queues are rejected",
			mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Annotations = map[string]string{QueueAnnotationKey: "gpu-training"}
				pcs.Spec.Template.Cliques[0].Annotations = map[string]string{QueueAnnotationKey: "high-priority"}
			},
			errorIs: errConflictingQueueAnnotations,
		},
		{
			description: "all cliques must resolve to the same queue",
			mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Spec.Template.Cliques[0].Annotations = map[string]string{QueueAnnotationKey: "gpu-training"}
				pcs.Spec.Template.Cliques[1].Annotations = map[string]string{QueueAnnotationKey: "high-priority"}
			},
			errorContains: "all PodCliques in a PodCliqueSet using volcano scheduler must resolve to the same queue",
		},
		{
			description: "missing queue defaults to default",
			mutate:      func(_ *grovecorev1alpha1.PodCliqueSet) {},
			existingObjs: []client.Object{
				makeQueue(DefaultQueue, volcanov1beta1.QueueStateOpen),
			},
		},
		{
			description: "queue name must be valid",
			mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Annotations = map[string]string{QueueAnnotationKey: "Invalid_Queue"}
			},
			errorContains: "metadata.annotations[scheduling.grove.io/volcano-queue]",
		},
		{
			description: "invalid global queue returns before checking queue object",
			mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Annotations = map[string]string{QueueAnnotationKey: "Invalid_Queue"}
			},
			errorContains:    "metadata.annotations[scheduling.grove.io/volcano-queue]",
			errorNotContains: "does not exist",
		},
		{
			description: "queue must exist",
			mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Annotations = map[string]string{QueueAnnotationKey: "missing"}
			},
			errorContains: `volcano queue "missing" does not exist`,
		},
		{
			description: "queue must be open",
			mutate: func(pcs *grovecorev1alpha1.PodCliqueSet) {
				pcs.Annotations = map[string]string{QueueAnnotationKey: "closed"}
			},
			existingObjs:  []client.Object{makeQueue("closed", "Closed")},
			errorContains: `volcano queue "closed" is not Open`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			pcs := makePCS()
			tc.mutate(pcs)
			cl := schedulertest.NewVolcanoClient(t, tc.existingObjs...)
			recorder := record.NewFakeRecorder(10)
			profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameVolcano}
			b := New(cl, cl.Scheme(), recorder, profile)

			err := b.ValidatePodCliqueSet(context.Background(), pcs)
			if tc.errorIs != nil {
				require.ErrorIs(t, err, tc.errorIs)
				return
			}
			if tc.errorContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errorContains)
				if tc.errorNotContains != "" {
					assert.NotContains(t, err.Error(), tc.errorNotContains)
				}
				return
			}
			require.NoError(t, err)
		})
	}
}

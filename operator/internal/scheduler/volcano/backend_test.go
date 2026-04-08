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
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	volcanov1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

func newVolcanoQueue(name string, state volcanov1beta1.QueueState) *volcanov1beta1.Queue {
	return &volcanov1beta1.Queue{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: volcanov1beta1.QueueStatus{
			State: state,
		},
	}
}

func TestBackend_Init(t *testing.T) {
	t.Run("success with open queue", func(t *testing.T) {
		cl := testutils.CreateDefaultFakeClient([]client.Object{newVolcanoQueue("default", volcanov1beta1.QueueStateOpen)})
		recorder := record.NewFakeRecorder(10)
		profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameVolcano}
		b := New(cl, cl.Scheme(), recorder, profile)

		require.NoError(t, b.Init())
	})

	t.Run("queue not found", func(t *testing.T) {
		cl := testutils.CreateDefaultFakeClient(nil)
		recorder := record.NewFakeRecorder(10)
		profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameVolcano}
		b := New(cl, cl.Scheme(), recorder, profile)

		err := b.Init()
		require.Error(t, err)
		assert.Contains(t, err.Error(), `failed to get Volcano queue "default"`)
	})

	t.Run("queue not open", func(t *testing.T) {
		cl := testutils.CreateDefaultFakeClient([]client.Object{newVolcanoQueue("default", volcanov1beta1.QueueStateClosed)})
		recorder := record.NewFakeRecorder(10)
		profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameVolcano}
		b := New(cl, cl.Scheme(), recorder, profile)

		err := b.Init()
		require.Error(t, err)
		assert.Contains(t, err.Error(), `volcano queue "default" is not open: Closed`)
	})
}

func TestBackend_PreparePod(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameVolcano}
	b := New(cl, cl.Scheme(), recorder, profile)

	pod := testutils.NewPodBuilder("test-pod", "default").Build()
	pod.Labels = map[string]string{apicommon.LabelPodGang: "pg-1"}

	b.PreparePod(pod)

	assert.Equal(t, string(configv1alpha1.SchedulerNameVolcano), pod.Spec.SchedulerName)
	assert.Equal(t, "pg-1", pod.Annotations[volcanov1beta1.VolcanoGroupNameAnnotationKey])
	assert.Equal(t, "pg-1", pod.Annotations[volcanov1beta1.KubeGroupNameAnnotationKey])
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
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PriorityClassName: "high-priority",
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{Name: "a", MinReplicas: 2},
				{Name: "b", MinReplicas: 3},
			},
		},
	}
	cl := testutils.CreateDefaultFakeClient([]client.Object{podGang})
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameVolcano}
	b := New(cl, cl.Scheme(), recorder, profile)

	err := b.SyncPodGang(context.Background(), podGang)
	require.NoError(t, err)

	podGroup := &volcanov1beta1.PodGroup{}
	err = cl.Get(context.Background(), client.ObjectKey{Name: "pg-1", Namespace: "default"}, podGroup)
	require.NoError(t, err)
	assert.Equal(t, int32(5), podGroup.Spec.MinMember)
	assert.Equal(t, "default", podGroup.Spec.Queue)
	assert.Equal(t, "high-priority", podGroup.Spec.PriorityClassName)
}

func TestBackend_ValidatePodCliqueSet(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
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

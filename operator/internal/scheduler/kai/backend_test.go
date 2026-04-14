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

package kai

import (
	"context"
	"testing"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	kaischedulingv2alpha2 "github.com/NVIDIA/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestBackend_PreparePod(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}
	b := New(cl, cl.Scheme(), recorder, profile)

	pod := testutils.NewPodBuilder("test-pod", "default").
		WithSchedulerName("default-scheduler").
		Build()

	b.PreparePod(pod)

	assert.Equal(t, "kai-scheduler", pod.Spec.SchedulerName)
}

func TestBackend_SyncPodGang_CreateAndUpdate(t *testing.T) {
	podGang := testutils.NewPodGangBuilder("test-podgang", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		Build()
	podGang.Labels["kai.scheduler/queue"] = "team-a"
	podGang.Annotations = map[string]string{"grove.io/topology-name": "cluster-topology"}
	podGang.Spec.PriorityClassName = "high-priority"
	podGang.Spec.TopologyConstraint = &groveschedulerv1alpha1.TopologyConstraint{
		PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{
			Required: ptr.To("zone"),
		},
	}
	podGang.Spec.TopologyConstraintGroupConfigs = []groveschedulerv1alpha1.TopologyConstraintGroupConfig{
		{
			Name:          "decoder-group",
			PodGroupNames: []string{"decoder"},
			TopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{
				PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{
					Preferred: ptr.To("rack"),
				},
			},
		},
	}
	podGang.Spec.PodGroups = []groveschedulerv1alpha1.PodGroup{
		{
			Name:        "encoder",
			MinReplicas: 2,
			TopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{
				PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{
					Required: ptr.To("host"),
				},
			},
		},
		{
			Name:        "decoder",
			MinReplicas: 3,
		},
	}

	cl := testutils.NewTestClientBuilder().
		WithObjects(podGang).
		Build()
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}
	b := New(cl, cl.Scheme(), recorder, profile)

	ctx := context.Background()
	require.NoError(t, b.SyncPodGang(ctx, podGang))

	syncedPodGang := &groveschedulerv1alpha1.PodGang{}
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(podGang), syncedPodGang))
	assert.Equal(t, "true", syncedPodGang.Annotations["grove.io/ignore"])

	gotPodGroup := &kaischedulingv2alpha2.PodGroup{}
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: podGang.Name, Namespace: podGang.Namespace}, gotPodGroup))

	assert.Equal(t, int32(5), gotPodGroup.Spec.MinMember)
	assert.Equal(t, "team-a", gotPodGroup.Spec.Queue)
	assert.Equal(t, "high-priority", gotPodGroup.Spec.PriorityClassName)
	assert.Equal(t, "zone", gotPodGroup.Spec.TopologyConstraint.RequiredTopologyLevel)

	require.Len(t, gotPodGroup.Spec.SubGroups, 3)
	assert.Equal(t, "decoder-group", gotPodGroup.Spec.SubGroups[0].Name)
	assert.Equal(t, int32(0), gotPodGroup.Spec.SubGroups[0].MinMember)

	assert.Equal(t, "encoder", gotPodGroup.Spec.SubGroups[1].Name)
	assert.Equal(t, int32(2), gotPodGroup.Spec.SubGroups[1].MinMember)
	assert.Nil(t, gotPodGroup.Spec.SubGroups[1].Parent)
	assert.Equal(t, "host", gotPodGroup.Spec.SubGroups[1].TopologyConstraint.RequiredTopologyLevel)

	assert.Equal(t, "decoder", gotPodGroup.Spec.SubGroups[2].Name)
	require.NotNil(t, gotPodGroup.Spec.SubGroups[2].Parent)
	assert.Equal(t, "decoder-group", *gotPodGroup.Spec.SubGroups[2].Parent)
	assert.Equal(t, int32(3), gotPodGroup.Spec.SubGroups[2].MinMember)

	// Update PodGang: remove queue label and change min replicas.
	updatedPodGang := syncedPodGang.DeepCopy()
	delete(updatedPodGang.Labels, "kai.scheduler/queue")
	updatedPodGang.Spec.PodGroups[0].MinReplicas = 4
	require.NoError(t, cl.Update(ctx, updatedPodGang))

	require.NoError(t, b.SyncPodGang(ctx, updatedPodGang))
	gotAfterUpdate := &kaischedulingv2alpha2.PodGroup{}
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: podGang.Name, Namespace: podGang.Namespace}, gotAfterUpdate))

	// Existing queue should be preserved even when source label is removed.
	assert.Equal(t, "team-a", gotAfterUpdate.Spec.Queue)
	assert.Equal(t, int32(7), gotAfterUpdate.Spec.MinMember)
	assert.Equal(t, int32(4), gotAfterUpdate.Spec.SubGroups[1].MinMember)
}

func TestBackend_OnPodGangDelete(t *testing.T) {
	podGang := testutils.NewPodGangBuilder("to-delete", "default").
		WithSchedulerName(string(configv1alpha1.SchedulerNameKai)).
		Build()
	podGroup := &kaischedulingv2alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "to-delete",
			Namespace: "default",
		},
	}

	cl := testutils.NewTestClientBuilder().WithObjects(podGang, podGroup).Build()
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}
	b := New(cl, cl.Scheme(), recorder, profile)

	ctx := context.Background()
	require.NoError(t, b.OnPodGangDelete(ctx, podGang))

	err := cl.Get(ctx, client.ObjectKey{Name: podGroup.Name, Namespace: podGroup.Namespace}, &kaischedulingv2alpha2.PodGroup{})
	assert.Error(t, err)
}

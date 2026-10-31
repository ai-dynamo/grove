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
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kueuev1beta2 "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

const rackKey = "topology.kubernetes.io/rack"

func pcsgPodClique(name, pcsgFQN string) *grovecorev1alpha1.PodClique {
	return &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default",
		Name:      name,
		Labels: map[string]string{
			apicommon.LabelPodCliqueScalingGroup:             pcsgFQN,
			apicommon.LabelPodCliqueScalingGroupReplicaIndex: "0",
		},
	}}
}

func newTopologyGroupBackend(t *testing.T, cl client.Client) *schedulerBackend {
	t.Helper()
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue})
	require.NoError(t, b.Init(nil))
	return b.(*schedulerBackend)
}

// syncWorkload builds the Workload for a PodGang of the PodCliqueSet "demo" whose cliques are all in the PCSG "w".
func syncWorkload(t *testing.T, cliques []*grovecorev1alpha1.PodCliqueTemplateSpec, podGang *groveschedulerv1alpha1.PodGang) *kueuev1beta2.Workload {
	t.Helper()
	names := make([]string, 0, len(cliques))
	objs := []client.Object{}
	for _, c := range cliques {
		names = append(names, c.Name)
		objs = append(objs, pcsgPodClique("demo-0-w-0-"+c.Name, "demo-0-w"))
	}
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default", Labels: map[string]string{queueNameLabel: "q"}},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
			Cliques:                      cliques,
			PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{{Name: "w", CliqueNames: names}},
		}},
	}
	cl := testutils.NewTestClientBuilder().WithObjects(append(objs, pcs)...).Build()
	b := newTopologyGroupBackend(t, cl)
	podGang.ObjectMeta = metav1.ObjectMeta{Name: "demo-0", Namespace: "default", Labels: map[string]string{apicommon.LabelPartOfKey: "demo"}}
	require.NoError(t, b.SyncPodGang(context.Background(), podGang))
	got := &kueuev1beta2.Workload{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "demo-0"}, got))
	return got
}

func rackPack() *groveschedulerv1alpha1.TopologyConstraint {
	return &groveschedulerv1alpha1.TopologyConstraint{PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: ptr.To(rackKey)}}
}

func podGroupsFor(names ...string) []groveschedulerv1alpha1.PodGroup {
	groups := make([]groveschedulerv1alpha1.PodGroup, 0, len(names))
	for _, n := range names {
		groups = append(groups, groveschedulerv1alpha1.PodGroup{Name: "demo-0-w-0-" + n, MinReplicas: 1})
	}
	return groups
}

func fqns(names ...string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, "demo-0-w-0-"+n)
	}
	return out
}

// A multinode replica's leader and worker must land in one NVLink rack, so they share a podSetGroupName.
func TestTopologyGroup_LeaderAndWorkerShareGroup(t *testing.T) {
	got := syncWorkload(t,
		[]*grovecorev1alpha1.PodCliqueTemplateSpec{
			{Name: "l", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
			{Name: "w", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
		},
		&groveschedulerv1alpha1.PodGang{Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: podGroupsFor("l", "w"),
			TopologyConstraintGroupConfigs: []groveschedulerv1alpha1.TopologyConstraintGroupConfig{
				{Name: "demo-0-w-0", PodGroupNames: fqns("l", "w"), TopologyConstraint: rackPack()},
			},
		}},
	)
	require.Len(t, got.Spec.PodSets, 2)
	for _, ps := range got.Spec.PodSets {
		require.NotNil(t, ps.TopologyRequest, ps.Name)
		assert.Equal(t, rackKey, ptr.Deref(ps.TopologyRequest.Required, ""), ps.Name)
		assert.Equal(t, "demo-0-w-0", ptr.Deref(ps.TopologyRequest.PodSetGroupName, ""), ps.Name)
	}
}

// Kueue TAS only places the first two PodSets of a group together, so a larger group is left ungrouped rather than
// half-applied.
func TestTopologyGroup_SkipsGroupsKueueCannotPlace(t *testing.T) {
	got := syncWorkload(t,
		[]*grovecorev1alpha1.PodCliqueTemplateSpec{
			{Name: "a", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
			{Name: "b", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
			{Name: "c", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
		},
		&groveschedulerv1alpha1.PodGang{Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: podGroupsFor("a", "b", "c"),
			TopologyConstraintGroupConfigs: []groveschedulerv1alpha1.TopologyConstraintGroupConfig{
				{Name: "demo-0-w-0", PodGroupNames: fqns("a", "b", "c"), TopologyConstraint: rackPack()},
			},
		}},
	)
	require.Len(t, got.Spec.PodSets, 3)
	for _, ps := range got.Spec.PodSets {
		require.NotNil(t, ps.TopologyRequest, ps.Name)
		assert.Nil(t, ps.TopologyRequest.PodSetGroupName, ps.Name)
	}
}

// A clique annotated unconstrained, with no topology constraint, gets the same request Kueue would derive from Pods.
func TestTopologyGroup_UnconstrainedClique(t *testing.T) {
	got := syncWorkload(t,
		[]*grovecorev1alpha1.PodCliqueTemplateSpec{{
			Name:        "w",
			Annotations: map[string]string{podSetUnconstrainedAnnotation: "true"},
			Spec:        grovecorev1alpha1.PodCliqueSpec{Replicas: 1},
		}},
		&groveschedulerv1alpha1.PodGang{Spec: groveschedulerv1alpha1.PodGangSpec{PodGroups: podGroupsFor("w")}},
	)
	require.Len(t, got.Spec.PodSets, 1)
	require.NotNil(t, got.Spec.PodSets[0].TopologyRequest)
	assert.True(t, ptr.Deref(got.Spec.PodSets[0].TopologyRequest.Unconstrained, false))
	assert.Nil(t, got.Spec.PodSets[0].TopologyRequest.PodSetGroupName)
	assert.Equal(t, "true", got.Spec.PodSets[0].Template.Annotations[podSetUnconstrainedAnnotation])
}

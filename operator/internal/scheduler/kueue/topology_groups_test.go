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
	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kueuev1beta2 "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

const (
	rackKey = "topology.kubernetes.io/rack"
	hostKey = "kubernetes.io/hostname"
)

func pcsgPodClique(name, pcsgFQN string) *grovecorev1beta1.PodClique {
	return &grovecorev1beta1.PodClique{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default",
		Name:      name,
		Labels: map[string]string{
			apicommon.LabelPodCliqueScalingGroup:             pcsgFQN,
			apicommon.LabelPodCliqueScalingGroupReplicaIndex: "0",
		},
	}}
}

// newTopologyGroupBackend returns a Kueue backend whose profile sets enablePodSetGroupTopology to enableGroups.
func newTopologyGroupBackend(t *testing.T, cl client.Client, enableGroups bool) *schedulerBackend {
	t.Helper()
	profile := configv1beta1.SchedulerProfile{Name: configv1beta1.SchedulerNameKueue}
	if enableGroups {
		profile.Config = &runtime.RawExtension{Raw: []byte(`{"enablePodSetGroupTopology":true}`)}
	}
	b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), profile)
	require.NoError(t, b.Init(nil))
	return b.(*schedulerBackend)
}

// syncWorkload builds the Workload for a PodGang of the PodCliqueSet "demo" whose cliques are all in the PCSG "w",
// with enablePodSetGroupTopology set to enableGroups.
func syncWorkload(t *testing.T, enableGroups bool, cliques []*grovecorev1beta1.PodCliqueTemplateSpec, podGang *groveschedulerv1alpha1.PodGang) *kueuev1beta2.Workload {
	t.Helper()
	names := make([]string, 0, len(cliques))
	objs := []client.Object{}
	for _, c := range cliques {
		names = append(names, c.Name)
		objs = append(objs, pcsgPodClique("demo-0-w-0-"+c.Name, "demo-0-w"))
	}
	pcs := &grovecorev1beta1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default", Labels: map[string]string{queueNameLabel: "q"}},
		Spec: grovecorev1beta1.PodCliqueSetSpec{Template: grovecorev1beta1.PodCliqueSetTemplateSpec{
			Cliques:                      cliques,
			PodCliqueScalingGroupConfigs: []grovecorev1beta1.PodCliqueScalingGroupConfig{{Name: "w", CliqueNames: names}},
		}},
	}
	cl := testutils.NewTestClientBuilder().WithObjects(append(objs, pcs)...).Build()
	b := newTopologyGroupBackend(t, cl, enableGroups)
	podGang.ObjectMeta = metav1.ObjectMeta{Name: "demo-0", Namespace: "default", Labels: map[string]string{apicommon.LabelPartOfKey: "demo"}}
	require.NoError(t, b.SyncPodGang(context.Background(), podGang))
	got := &kueuev1beta2.Workload{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "demo-0"}, got))
	return got
}

func rackPack() *groveschedulerv1alpha1.TopologyConstraint {
	return &groveschedulerv1alpha1.TopologyConstraint{PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: ptr.To(rackKey)}}
}

func hostPack() *groveschedulerv1alpha1.TopologyConstraint {
	return &groveschedulerv1alpha1.TopologyConstraint{PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: ptr.To(hostKey)}}
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

// leaderAndWorkers returns the cliques and PodGang of one PodCliqueScalingGroup replica packed into a rack: a leader
// clique of leaderPods Pods and a worker clique of workerPods Pods.
func leaderAndWorkers(leaderPods, workerPods int32) ([]*grovecorev1beta1.PodCliqueTemplateSpec, *groveschedulerv1alpha1.PodGang) {
	cliques := []*grovecorev1beta1.PodCliqueTemplateSpec{
		{Name: "l", Spec: grovecorev1beta1.PodCliqueSpec{Replicas: leaderPods}},
		{Name: "w", Spec: grovecorev1beta1.PodCliqueSpec{Replicas: workerPods}},
	}
	podGang := &groveschedulerv1alpha1.PodGang{Spec: groveschedulerv1alpha1.PodGangSpec{
		PodGroups: podGroupsFor("l", "w"),
		TopologyConstraintGroupConfigs: []groveschedulerv1alpha1.TopologyConstraintGroupConfig{
			{Name: "demo-0-w-0", PodGroupNames: fqns("l", "w"), TopologyConstraint: rackPack()},
		},
	}}
	return cliques, podGang
}

// A multinode replica's leader and workers must land in one NVLink rack, so with the option enabled they share a
// podSetGroupName.
func TestTopologyGroup_LeaderAndWorkersShareGroup(t *testing.T) {
	cliques, podGang := leaderAndWorkers(1, 2)
	got := syncWorkload(t, true, cliques, podGang)
	require.Len(t, got.Spec.PodSets, 2)
	for _, ps := range got.Spec.PodSets {
		require.NotNil(t, ps.TopologyRequest, ps.Name)
		assert.Equal(t, rackKey, ptr.Deref(ps.TopologyRequest.Required, ""), ps.Name)
		assert.Equal(t, "demo-0-w-0", ptr.Deref(ps.TopologyRequest.PodSetGroupName, ""), ps.Name)
	}
}

// A member constraint equal to the group's leaves the group's placement unchanged, so the group is still emitted.
func TestTopologyGroup_MemberConstraintEqualToGroup(t *testing.T) {
	cliques, podGang := leaderAndWorkers(1, 2)
	podGang.Spec.PodGroups[1].TopologyConstraint = rackPack()
	got := syncWorkload(t, true, cliques, podGang)
	require.Len(t, got.Spec.PodSets, 2)
	for _, ps := range got.Spec.PodSets {
		require.NotNil(t, ps.TopologyRequest, ps.Name)
		assert.Equal(t, "demo-0-w-0", ptr.Deref(ps.TopologyRequest.PodSetGroupName, ""), ps.Name)
	}
}

// With only a PodGang-wide constraint, the PodGang's PodGroups form one group named after the PodGang.
func TestTopologyGroup_PodGangWideConstraint(t *testing.T) {
	cliques, podGang := leaderAndWorkers(1, 2)
	podGang.Spec.TopologyConstraintGroupConfigs = nil
	podGang.Spec.TopologyConstraint = rackPack()
	got := syncWorkload(t, true, cliques, podGang)
	require.Len(t, got.Spec.PodSets, 2)
	for _, ps := range got.Spec.PodSets {
		require.NotNil(t, ps.TopologyRequest, ps.Name)
		assert.Equal(t, rackKey, ptr.Deref(ps.TopologyRequest.Required, ""), ps.Name)
		assert.Equal(t, "demo-0", ptr.Deref(ps.TopologyRequest.PodSetGroupName, ""), ps.Name)
	}
}

// Grouping is opt-in: by default each PodSet keeps its own topology request and is placed independently.
func TestTopologyGroup_DisabledByDefault(t *testing.T) {
	cliques, podGang := leaderAndWorkers(1, 2)
	got := syncWorkload(t, false, cliques, podGang)
	require.Len(t, got.Spec.PodSets, 2)
	for _, ps := range got.Spec.PodSets {
		require.NotNil(t, ps.TopologyRequest, ps.Name)
		assert.Equal(t, rackKey, ptr.Deref(ps.TopologyRequest.Required, ""), ps.Name)
		assert.Nil(t, ps.TopologyRequest.PodSetGroupName, ps.Name)
	}
}

// An unset option is defaulted to false, so that a later change of the default leaves an explicit false alone.
func TestTopologyGroup_OptionDefaulting(t *testing.T) {
	tests := []struct {
		name   string
		config *runtime.RawExtension
		want   bool
	}{
		{name: "no config", config: nil, want: false},
		{name: "unset", config: &runtime.RawExtension{Raw: []byte(`{}`)}, want: false},
		{name: "null", config: &runtime.RawExtension{Raw: []byte(`{"enablePodSetGroupTopology":null}`)}, want: false},
		{name: "explicit false", config: &runtime.RawExtension{Raw: []byte(`{"enablePodSetGroupTopology":false}`)}, want: false},
		{name: "explicit true", config: &runtime.RawExtension{Raw: []byte(`{"enablePodSetGroupTopology":true}`)}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := testutils.NewTestClientBuilder().Build()
			profile := configv1beta1.SchedulerProfile{Name: configv1beta1.SchedulerNameKueue, Config: tt.config}
			b := New(cl, cl.Scheme(), record.NewFakeRecorder(10), profile).(*schedulerBackend)
			require.NoError(t, b.Init(nil))
			require.NotNil(t, b.config.EnablePodSetGroupTopology)
			assert.Equal(t, tt.want, *b.config.EnablePodSetGroupTopology)
		})
	}
}

// Kueue TAS places a group together only as a single leader Pod and its workers (ai-dynamo/grove#867), so any other
// group is left ungrouped rather than placed differently from what the PodGang asks for.
func TestTopologyGroup_SkipsGroupsKueueCannotPlace(t *testing.T) {
	multiPodLeader, multiPodLeaderGang := leaderAndWorkers(2, 4)
	// Kueue would place the leader by the workers' hostname request, onto the workers' host, rather than anywhere in
	// the rack.
	narrowWorkers, narrowWorkersGang := leaderAndWorkers(1, 2)
	narrowWorkersGang.Spec.PodGroups[1].TopologyConstraint = hostPack()
	// The members' requests match each other but not the group's, so grouping would still narrow the rack to a host.
	narrowBoth, narrowBothGang := leaderAndWorkers(1, 2)
	narrowBothGang.Spec.PodGroups[0].TopologyConstraint = hostPack()
	narrowBothGang.Spec.PodGroups[1].TopologyConstraint = hostPack()
	testCases := []struct {
		description string
		cliques     []*grovecorev1beta1.PodCliqueTemplateSpec
		podGang     *groveschedulerv1alpha1.PodGang
	}{
		{
			description: "more than two PodSets, since Kueue reads only the first two",
			cliques: []*grovecorev1beta1.PodCliqueTemplateSpec{
				{Name: "a", Spec: grovecorev1beta1.PodCliqueSpec{Replicas: 1}},
				{Name: "b", Spec: grovecorev1beta1.PodCliqueSpec{Replicas: 1}},
				{Name: "c", Spec: grovecorev1beta1.PodCliqueSpec{Replicas: 1}},
			},
			podGang: &groveschedulerv1alpha1.PodGang{Spec: groveschedulerv1alpha1.PodGangSpec{
				PodGroups: podGroupsFor("a", "b", "c"),
				TopologyConstraintGroupConfigs: []groveschedulerv1alpha1.TopologyConstraintGroupConfig{
					{Name: "demo-0-w-0", PodGroupNames: fqns("a", "b", "c"), TopologyConstraint: rackPack()},
				},
			}},
		},
		{
			description: "two PodSets with no single-Pod leader, since Kueue would place only one Pod of the smaller",
			cliques:     multiPodLeader,
			podGang:     multiPodLeaderGang,
		},
		{
			description: "workers pack more narrowly than the group, since Kueue places the group by the workers' request",
			cliques:     narrowWorkers,
			podGang:     narrowWorkersGang,
		},
		{
			description: "leader and workers both pack more narrowly than the group",
			cliques:     narrowBoth,
			podGang:     narrowBothGang,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			got := syncWorkload(t, true, tc.cliques, tc.podGang)
			require.Len(t, got.Spec.PodSets, len(tc.cliques))
			for _, ps := range got.Spec.PodSets {
				require.NotNil(t, ps.TopologyRequest, ps.Name)
				assert.NotEmpty(t, ptr.Deref(ps.TopologyRequest.Required, ""), ps.Name)
				assert.Nil(t, ps.TopologyRequest.PodSetGroupName, ps.Name)
			}
		})
	}
}

// A clique annotated unconstrained, with no topology constraint, gets the same request Kueue would derive from Pods.
func TestTopologyGroup_UnconstrainedClique(t *testing.T) {
	got := syncWorkload(t, false,
		[]*grovecorev1beta1.PodCliqueTemplateSpec{{
			Name:        "w",
			Annotations: map[string]string{podSetUnconstrainedAnnotation: "true"},
			Spec:        grovecorev1beta1.PodCliqueSpec{Replicas: 1},
		}},
		&groveschedulerv1alpha1.PodGang{Spec: groveschedulerv1alpha1.PodGangSpec{PodGroups: podGroupsFor("w")}},
	)
	require.Len(t, got.Spec.PodSets, 1)
	require.NotNil(t, got.Spec.PodSets[0].TopologyRequest)
	assert.True(t, ptr.Deref(got.Spec.PodSets[0].TopologyRequest.Unconstrained, false))
	assert.Nil(t, got.Spec.PodSets[0].TopologyRequest.PodSetGroupName)
	assert.Equal(t, "true", got.Spec.PodSets[0].Template.Annotations[podSetUnconstrainedAnnotation])
}

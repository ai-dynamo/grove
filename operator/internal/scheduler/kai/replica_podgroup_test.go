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

package kai

import (
	"strings"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/record"
)

func TestStableKAINamesAreValidAndCollisionResistant(t *testing.T) {
	longName := strings.Repeat("Mixed_Name.", 10)
	namer := newSubGroupNamer("job", 42)
	names := []string{
		replicaPodGroupName(longName, 42),
		namer.nonAnchorCollection(),
		namer.podGangBranch(longName),
		namer.nonAnchorPodGang(longName),
		namer.topologyGroup(longName, longName),
		namer.podGroupLeaf(longName, longName),
	}
	for _, name := range names {
		assert.Empty(t, validation.IsDNS1123Label(name), name)
		assert.LessOrEqual(t, len(name), 63)
		assert.Equal(t, strings.ToLower(name), name)
	}
	assert.Len(t, sets.New(names...), len(names))
}

func TestStableKAINamesDoNotChange(t *testing.T) {
	namer := newSubGroupNamer("job", 0)
	tests := []struct {
		name     string
		actual   string
		expected string
	}{
		{name: "replica PodGroup", actual: replicaPodGroupName("job", 0), expected: "grove-job-0"},
		{name: "sanitized replica PodGroup", actual: replicaPodGroupName("My_Job.v2", 3), expected: "grove-my-job-v2-3-0a5efdb4c1"},
		{name: "over-length replica PodGroup", actual: replicaPodGroupName(strings.Repeat("a", 70), 1), expected: "grove-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-157ee39d2e"},
		{name: "non-anchor collection", actual: namer.nonAnchorCollection(), expected: "non-anchors-r0"},
		{name: "anchor branch", actual: namer.podGangBranch("job-0-1000"), expected: "podgang-r0-1000"},
		{name: "non-anchor branch", actual: namer.podGangBranch("job-0-2000-workers-1"), expected: "podgang-r0-2000-workers-1"},
		{name: "non-anchor wrapper", actual: namer.nonAnchorPodGang("job-0-2000-workers-1"), expected: "non-anchor-r0-2000-workers-1"},
		{name: "topology group", actual: namer.topologyGroup("job-0-2000-workers-1", "job-0-workers-1"), expected: "group-r0-workers-1-2000"},
		{name: "standalone PodGroup leaf", actual: namer.podGroupLeaf("job-0-1000", "job-0-server"), expected: "leaf-r0-server-1000"},
		{name: "scaling group PodGroup leaf", actual: namer.podGroupLeaf("job-0-2000-workers-1", "job-0-workers-1-worker"), expected: "leaf-r0-workers-1-worker-2000"},
		{name: "PodGang of another replica", actual: namer.podGangBranch("job-1-1000"), expected: "podgang-r0-job-1-1000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.actual)
		})
	}
}

func TestBuildReplicaPodGroupPlanRoleAwareHierarchy(t *testing.T) {
	pcs := newPodCliqueSet("job", "team-a")
	anchorA := materializedTestPodGang(pcs, "job-0-1000", grovecorev1beta1.PodGangEntryRoleAnchor,
		groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 2},
		groveschedulerv1alpha1.PodGroup{Name: "server", MinReplicas: 1},
	)
	anchorA.PodGang.Spec.TopologyConstraintGroupConfigs = []groveschedulerv1alpha1.TopologyConstraintGroupConfig{{
		Name:          "rack",
		PodGroupNames: []string{"worker"},
	}, {Name: "empty"}}
	anchorB := materializedTestPodGang(pcs, "job-0-2000", grovecorev1beta1.PodGangEntryRoleAnchor,
		groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 3},
	)
	tail := materializedTestPodGang(pcs, "job-0-3000-workers-1", grovecorev1beta1.PodGangEntryRoleTail,
		groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1},
	)
	scaleOut := materializedTestPodGang(pcs, "job-0-4000-workers-2", grovecorev1beta1.PodGangEntryRoleScaleOut,
		groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1},
	)

	backend := testPlanBackend(t)
	materialized := []componentutils.MaterializedPodGang{scaleOut, anchorB, tail, anchorA}
	plan, err := backend.buildReplicaPodGroupPlan(
		pcs,
		0,
		materialized,
		"",
	)
	require.NoError(t, err)
	podGroup := plan.podGroup

	assert.Equal(t, replicaPodGroupName(pcs.Name, 0), podGroup.Name)
	assert.Equal(t, "job", podGroup.Labels[apicommon.LabelPartOfKey])
	assert.Equal(t, "0", podGroup.Labels[apicommon.LabelPodCliqueSetReplicaIndex])
	assert.Equal(t, apicommon.LabelComponentNamePodCliqueSetReplicaPodGroup, podGroup.Labels[apicommon.LabelComponentKey])
	assert.Nil(t, podGroup.Spec.MinMember)
	require.NotNil(t, podGroup.Spec.MinSubGroup)
	assert.Equal(t, int32(3), *podGroup.Spec.MinSubGroup)

	collection := requireSubGroup(t, podGroup, "non-anchors-r0")
	assert.Nil(t, collection.Parent)
	require.NotNil(t, collection.MinSubGroup)
	assert.Equal(t, int32(2), *collection.MinSubGroup)

	for _, podGang := range []string{"3000-workers-1", "4000-workers-2"} {
		wrapper := requireSubGroup(t, podGroup, "non-anchor-r0-"+podGang)
		require.NotNil(t, wrapper.Parent)
		assert.Equal(t, collection.Name, *wrapper.Parent)
		require.NotNil(t, wrapper.MinSubGroup)
		assert.Zero(t, *wrapper.MinSubGroup)

		branch := requireSubGroup(t, podGroup, "podgang-r0-"+podGang)
		require.NotNil(t, branch.Parent)
		assert.Equal(t, wrapper.Name, *branch.Parent)
		require.NotNil(t, branch.MinSubGroup)
		assert.Equal(t, int32(1), *branch.MinSubGroup)
	}

	anchorABranch := requireSubGroup(t, podGroup, "podgang-r0-1000")
	assert.Nil(t, anchorABranch.Parent)
	assert.Less(t, collection.Name, anchorABranch.Name, "the collection must sort ahead of the Anchor branches")
	require.NotNil(t, anchorABranch.MinSubGroup)
	assert.Equal(t, int32(2), *anchorABranch.MinSubGroup)
	anchorAGroup := requireSubGroup(t, podGroup, "group-r0-rack-1000")
	require.NotNil(t, anchorAGroup.MinSubGroup)
	assert.Equal(t, int32(1), *anchorAGroup.MinSubGroup)
	assert.Nil(t, findSubGroup(podGroup, "group-r0-empty-1000"))

	leafNames := make(map[string]struct{})
	for _, item := range []componentutils.MaterializedPodGang{anchorA, anchorB, tail, scaleOut} {
		leaf := requireSubGroup(t, podGroup, newSubGroupNamer(pcs.Name, 0).podGroupLeaf(item.PodGang.Name, "worker"))
		require.NotNil(t, leaf.MinMember)
		podLeaf, err := plan.leafForPod(legacyPodGangPod(pcs, item.PodGang, "worker"))
		require.NoError(t, err)
		assert.Equal(t, leaf.Name, podLeaf)
		leafNames[leaf.Name] = struct{}{}
	}
	assert.Len(t, leafNames, 4, "same PodGroup name in different PodGangs must produce unique leaves")
	assert.True(t, metav1.IsControlledBy(podGroup, pcs))
	assert.Equal(t, scaleOut.PodGang.Name, materialized[0].PodGang.Name, "building must not reorder the caller's materializations")
	assert.Equal(t, "worker", anchorA.PodGang.Spec.PodGroups[0].Name, "building must not reorder PodGang PodGroups")
	orderedPlan, err := backend.buildReplicaPodGroupPlan(pcs, 0, []componentutils.MaterializedPodGang{anchorA, tail, anchorB, scaleOut}, "")
	require.NoError(t, err)
	assert.Equal(t, plan, orderedPlan, "materialization order must not affect the PodGroup or its leaf mapping")
}

func TestBuildReplicaPodGroupPlanAnchorOnly(t *testing.T) {
	pcs := newPodCliqueSet("job", "team-a")
	anchor := materializedTestPodGang(pcs, "job-0-1000", grovecorev1beta1.PodGangEntryRoleAnchor,
		groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 2},
	)

	plan, err := testPlanBackend(t).buildReplicaPodGroupPlan(
		pcs,
		0,
		[]componentutils.MaterializedPodGang{anchor},
		"",
	)
	require.NoError(t, err)
	podGroup := plan.podGroup
	require.NotNil(t, podGroup.Spec.MinSubGroup)
	assert.Equal(t, int32(1), *podGroup.Spec.MinSubGroup)
	assert.Nil(t, findSubGroup(podGroup, "non-anchors-r0"))
}

func TestBuildReplicaPodGroupPlanAppliesReplicaAndTopology(t *testing.T) {
	pcs := newPodCliqueSet("job", "team-a")
	anchor := materializedTestPodGang(pcs, "job-1-1000", grovecorev1beta1.PodGangEntryRoleAnchor,
		groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 2},
	)
	rack := "topology.kubernetes.io/rack"
	anchor.PodGang.Spec.TopologyConstraint = &groveschedulerv1alpha1.TopologyConstraint{
		PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: &rack},
	}

	plan, err := testPlanBackend(t).buildReplicaPodGroupPlan(
		pcs,
		1,
		[]componentutils.MaterializedPodGang{anchor},
		"kai-topology",
	)
	require.NoError(t, err)
	assert.Equal(t, replicaPodGroupName(pcs.Name, 1), plan.podGroup.Name)
	assert.Equal(t, "1", plan.podGroup.Labels[apicommon.LabelPodCliqueSetReplicaIndex])
	assert.Equal(t, "kai-topology", plan.podGroup.Spec.TopologyConstraint.Topology)
	assert.Equal(t, rack, plan.podGroup.Spec.TopologyConstraint.RequiredTopologyLevel)
}

func TestBuildReplicaPodGroupPlanTreatsEmptyRootTopologyConstraintAsUnset(t *testing.T) {
	pcs := newPodCliqueSet("job", "team-a")
	anchor := materializedTestPodGang(pcs, "job-0-1000", grovecorev1beta1.PodGangEntryRoleAnchor,
		groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1},
	)
	empty := materializedTestPodGang(pcs, "job-0-2000", grovecorev1beta1.PodGangEntryRoleAnchor,
		groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1},
	)
	empty.PodGang.Spec.TopologyConstraint = &groveschedulerv1alpha1.TopologyConstraint{}
	emptyPack := materializedTestPodGang(pcs, "job-0-3000", grovecorev1beta1.PodGangEntryRoleAnchor,
		groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1},
	)
	emptyPack.PodGang.Spec.TopologyConstraint = &groveschedulerv1alpha1.TopologyConstraint{
		PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{},
	}

	plan, err := testPlanBackend(t).buildReplicaPodGroupPlan(pcs, 0, []componentutils.MaterializedPodGang{anchor, empty, emptyPack}, "")
	require.NoError(t, err)
	assert.Empty(t, plan.podGroup.Spec.TopologyConstraint.Topology)
}

func TestBuildReplicaPodGroupPlanSkipsGroveMetadataFromPodCliqueSet(t *testing.T) {
	pcs := newPodCliqueSet("job", "team-a")
	pcs.Labels["team"] = "a"
	pcs.Labels["grove.io/internal"] = "label"
	pcs.Annotations = map[string]string{"owner": "a", "grove.io/internal": "annotation"}
	anchor := materializedTestPodGang(pcs, "job-0-1000", grovecorev1beta1.PodGangEntryRoleAnchor,
		groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1},
	)

	plan, err := testPlanBackend(t).buildReplicaPodGroupPlan(pcs, 0, []componentutils.MaterializedPodGang{anchor}, "")
	require.NoError(t, err)
	assert.Equal(t, "a", plan.podGroup.Labels["team"])
	assert.NotContains(t, plan.podGroup.Labels, "grove.io/internal")
	assert.Equal(t, "0", plan.podGroup.Labels[apicommon.LabelPodCliqueSetReplicaIndex])
	assert.Equal(t, map[string]string{"owner": "a"}, plan.podGroup.Annotations)
}

func TestBuildReplicaPodGroupPlanRejectsInconsistentInputs(t *testing.T) {
	pcs := newPodCliqueSet("job", "team-a")
	anchor := materializedTestPodGang(pcs, "job-0-1000", grovecorev1beta1.PodGangEntryRoleAnchor,
		groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1},
	)

	tests := []struct {
		name          string
		materialized  []componentutils.MaterializedPodGang
		wantErrSubstr string
	}{
		{
			name:          "no PodGangs",
			wantErrSubstr: "has no materialized PodGangs",
		},
		{
			name: "no Anchor",
			materialized: []componentutils.MaterializedPodGang{
				materializedTestPodGang(pcs, "job-0-2000-workers-1", grovecorev1beta1.PodGangEntryRoleTail,
					groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1}),
			},
			wantErrSubstr: "has no materialized Anchor",
		},
		{
			name: "priority conflict",
			materialized: func() []componentutils.MaterializedPodGang {
				other := materializedTestPodGang(pcs, "job-0-2000", grovecorev1beta1.PodGangEntryRoleAnchor,
					groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1})
				other.PodGang.Spec.PriorityClassName = "different"
				return []componentutils.MaterializedPodGang{anchor, other}
			}(),
			wantErrSubstr: "conflicting priority classes",
		},
		{
			name: "unknown topology group member",
			materialized: func() []componentutils.MaterializedPodGang {
				invalid := materializedTestPodGang(pcs, "job-0-3000", grovecorev1beta1.PodGangEntryRoleAnchor,
					groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1})
				invalid.PodGang.Spec.TopologyConstraintGroupConfigs = []groveschedulerv1alpha1.TopologyConstraintGroupConfig{{
					Name:          "rack",
					PodGroupNames: []string{"missing"},
				}}
				return []componentutils.MaterializedPodGang{invalid}
			}(),
			wantErrSubstr: "references unknown PodGroup",
		},
		{
			name: "PodGang without PodGroups",
			materialized: []componentutils.MaterializedPodGang{
				anchor,
				materializedTestPodGang(pcs, "job-0-2000-workers-1", grovecorev1beta1.PodGangEntryRoleTail),
			},
			wantErrSubstr: "has no PodGroups",
		},
		{
			name: "root topology constraint conflict",
			materialized: func() []componentutils.MaterializedPodGang {
				other := materializedTestPodGang(pcs, "job-0-2000", grovecorev1beta1.PodGangEntryRoleAnchor,
					groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1})
				rack := "topology.kubernetes.io/rack"
				other.PodGang.Spec.TopologyConstraint = &groveschedulerv1alpha1.TopologyConstraint{
					PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: &rack},
				}
				return []componentutils.MaterializedPodGang{anchor, other}
			}(),
			wantErrSubstr: "conflicting root topology constraints",
		},
		{
			name: "PodGroup in two topology groups",
			materialized: func() []componentutils.MaterializedPodGang {
				invalid := materializedTestPodGang(pcs, "job-0-3000", grovecorev1beta1.PodGangEntryRoleAnchor,
					groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1})
				invalid.PodGang.Spec.TopologyConstraintGroupConfigs = []groveschedulerv1alpha1.TopologyConstraintGroupConfig{
					{Name: "rack", PodGroupNames: []string{"worker"}},
					{Name: "zone", PodGroupNames: []string{"worker"}},
				}
				return []componentutils.MaterializedPodGang{invalid}
			}(),
			wantErrSubstr: "belongs to topology groups",
		},
		{
			name: "unsupported role",
			materialized: []componentutils.MaterializedPodGang{
				materializedTestPodGang(pcs, "job-0-4000", grovecorev1beta1.PodGangEntryRole("Unknown"),
					groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1}),
			},
			wantErrSubstr: "has unsupported role",
		},
		{
			name:          "nil PodGang",
			materialized:  []componentutils.MaterializedPodGang{{Entry: anchor.Entry}},
			wantErrSubstr: "has a materialization without a PodGang",
		},
		{
			name:          "nil PodGangMap entry",
			materialized:  []componentutils.MaterializedPodGang{{PodGang: anchor.PodGang}},
			wantErrSubstr: "has no PodGangMap entry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := testPlanBackend(t).buildReplicaPodGroupPlan(pcs, 0, tt.materialized, "")
			require.ErrorContains(t, err, tt.wantErrSubstr)
		})
	}
}

func TestBuildReplicaPodGroupPlanAllowsLPXWithoutAnchor(t *testing.T) {
	pcs := newPodCliqueSet("job", "team-a")
	tail := materializedTestPodGang(pcs, "job-0-2000-workers-1", grovecorev1beta1.PodGangEntryRoleTail,
		groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1},
	)
	tail.PodGang.Labels = map[string]string{apicommon.LabelSchedulerName: string(configv1beta1.SchedulerNameLPX)}

	plan, err := testPlanBackend(t).buildReplicaPodGroupPlan(pcs, 0, []componentutils.MaterializedPodGang{tail}, "")
	require.NoError(t, err)
	require.NotNil(t, plan.podGroup.Spec.MinSubGroup)
	assert.Equal(t, int32(1), *plan.podGroup.Spec.MinSubGroup)
	requireSubGroup(t, plan.podGroup, "non-anchors-r0")
}

func TestReplicaPodGroupPlanLeafForPodRejectsUnknownPods(t *testing.T) {
	pcs := newPodCliqueSet("job", "team-a")
	anchor := materializedTestPodGang(pcs, "job-0-1000", grovecorev1beta1.PodGangEntryRoleAnchor,
		groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1},
	)
	other := testutils.NewPodGangBuilder("job-1-1000", pcs.Namespace).Build()
	plan, err := testPlanBackend(t).buildReplicaPodGroupPlan(pcs, 0, []componentutils.MaterializedPodGang{anchor}, "")
	require.NoError(t, err)

	_, err = plan.leafForPod(legacyPodGangPod(pcs, other, "worker"))
	require.ErrorContains(t, err, "outside replica PodGroup")

	_, err = plan.leafForPod(legacyPodGangPod(pcs, anchor.PodGang, "server"))
	require.ErrorContains(t, err, "maps to missing KAI subgroup")
}

func materializedTestPodGang(
	pcs *grovecorev1beta1.PodCliqueSet,
	name string,
	role grovecorev1beta1.PodGangEntryRole,
	podGroups ...groveschedulerv1alpha1.PodGroup,
) componentutils.MaterializedPodGang {
	podGang := testutils.NewPodGangBuilder(name, pcs.Namespace).Build()
	setPodCliqueSetControllerOwner(podGang, pcs)
	podGang.Spec.PodGroups = podGroups
	entry := &grovecorev1beta1.PodGangEntry{Role: role}
	return componentutils.MaterializedPodGang{PodGang: podGang, Entry: entry}
}

func testPlanBackend(t *testing.T) *schedulerBackend {
	t.Helper()
	cl := testutils.NewTestClientBuilder().Build()
	backend := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1beta1.SchedulerProfile{Name: configv1beta1.SchedulerNameKai}).(*schedulerBackend)
	initTestBackend(t, backend, cl)
	return backend
}

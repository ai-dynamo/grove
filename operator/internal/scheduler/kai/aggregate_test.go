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
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/record"
)

func TestStableKAINamesAreValidAndCollisionResistant(t *testing.T) {
	longName := strings.Repeat("Mixed_Name.", 10)
	names := []string{
		aggregatePodGroupName(longName, 42),
		anchorBranchName(longName),
		podGangBranchName(longName),
		utilityParentName(longName),
		topologyGroupName(longName, longName),
		podGroupLeafName(longName, longName),
	}
	for _, name := range names {
		assert.Empty(t, validation.IsDNS1123Label(name), name)
		assert.LessOrEqual(t, len(name), 63)
		assert.Equal(t, strings.ToLower(name), name)
	}

	assert.NotEqual(t, structuralKAIName("a/b", "c"), structuralKAIName("a", "b/c"))
}

func TestBuildAggregatePodGroupRoleAwareHierarchy(t *testing.T) {
	pcs := newPodCliqueSet("job", "team-a")
	anchorA := aggregateTestPodGang(pcs, "job-0-1000", grovecorev1alpha1.PodGangEntryRoleAnchor,
		groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 2},
		groveschedulerv1alpha1.PodGroup{Name: "server", MinReplicas: 1},
	)
	anchorA.PodGang.Spec.TopologyConstraintGroupConfigs = []groveschedulerv1alpha1.TopologyConstraintGroupConfig{{
		Name:          "rack",
		PodGroupNames: []string{"worker"},
	}, {Name: "empty"}}
	anchorB := aggregateTestPodGang(pcs, "job-0-2000", grovecorev1alpha1.PodGangEntryRoleAnchor,
		groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 3},
	)
	tail := aggregateTestPodGang(pcs, "job-0-workers-1-3000", grovecorev1alpha1.PodGangEntryRoleTail,
		groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1},
	)
	scaleOut := aggregateTestPodGang(pcs, "job-0-workers-2-4000", grovecorev1alpha1.PodGangEntryRoleScaleOut,
		groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1},
	)

	backend := testAggregateBackend(t)
	aggregate, err := backend.buildAggregatePodGroup(
		pcs,
		0,
		[]componentutils.MaterializedPodGang{scaleOut, anchorB, tail, anchorA},
		"",
	)
	require.NoError(t, err)

	assert.Equal(t, aggregatePodGroupName(pcs.Name, 0), aggregate.Name)
	assert.Equal(t, "job", aggregate.Labels[apicommon.LabelPartOfKey])
	assert.Equal(t, "0", aggregate.Labels[apicommon.LabelPodCliqueSetReplicaIndex])
	assert.Equal(t, apicommon.LabelComponentNameAggregatePodGroup, aggregate.Labels[apicommon.LabelComponentKey])
	assert.Nil(t, aggregate.Spec.MinMember)
	require.NotNil(t, aggregate.Spec.MinSubGroup)
	assert.Equal(t, int32(3), *aggregate.Spec.MinSubGroup)
	assert.True(t, strings.HasPrefix(anchorBranchName(anchorA.PodGang.Name), "1-"))

	collection := requireSubGroup(t, aggregate, nonAnchorPodGangsSubGroupName)
	assert.Nil(t, collection.Parent)
	require.NotNil(t, collection.MinSubGroup)
	assert.Equal(t, int32(2), *collection.MinSubGroup)

	for _, item := range []componentutils.MaterializedPodGang{tail, scaleOut} {
		utility := requireSubGroup(t, aggregate, utilityParentName(item.PodGang.Name))
		require.NotNil(t, utility.Parent)
		assert.Equal(t, collection.Name, *utility.Parent)
		require.NotNil(t, utility.MinSubGroup)
		assert.Zero(t, *utility.MinSubGroup)

		branch := requireSubGroup(t, aggregate, podGangBranchName(item.PodGang.Name))
		require.NotNil(t, branch.Parent)
		assert.Equal(t, utility.Name, *branch.Parent)
		require.NotNil(t, branch.MinSubGroup)
		assert.Equal(t, int32(1), *branch.MinSubGroup)
	}

	anchorABranch := requireSubGroup(t, aggregate, anchorBranchName(anchorA.PodGang.Name))
	require.NotNil(t, anchorABranch.MinSubGroup)
	assert.Equal(t, int32(2), *anchorABranch.MinSubGroup)
	anchorAGroup := requireSubGroup(t, aggregate, topologyGroupName(anchorA.PodGang.Name, "rack"))
	require.NotNil(t, anchorAGroup.MinSubGroup)
	assert.Equal(t, int32(1), *anchorAGroup.MinSubGroup)
	assert.Nil(t, findSubGroup(aggregate, topologyGroupName(anchorA.PodGang.Name, "empty")))

	leafNames := make(map[string]struct{})
	for _, item := range []componentutils.MaterializedPodGang{anchorA, anchorB, tail, scaleOut} {
		leaf := requireSubGroup(t, aggregate, podGroupLeafName(item.PodGang.Name, "worker"))
		require.NotNil(t, leaf.MinMember)
		leafNames[leaf.Name] = struct{}{}
	}
	assert.Len(t, leafNames, 4, "same PodGroup name in different PodGangs must produce unique leaves")
	assert.True(t, metav1.IsControlledBy(aggregate, pcs))
}

func TestBuildAggregatePodGroupAnchorOnly(t *testing.T) {
	pcs := newPodCliqueSet("job", "team-a")
	anchor := aggregateTestPodGang(pcs, "job-0-1000", grovecorev1alpha1.PodGangEntryRoleAnchor,
		groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 2},
	)

	aggregate, err := testAggregateBackend(t).buildAggregatePodGroup(
		pcs,
		0,
		[]componentutils.MaterializedPodGang{anchor},
		"",
	)
	require.NoError(t, err)
	require.NotNil(t, aggregate.Spec.MinSubGroup)
	assert.Equal(t, int32(1), *aggregate.Spec.MinSubGroup)
	assert.Nil(t, findSubGroup(aggregate, nonAnchorPodGangsSubGroupName))
}

func TestBuildAggregatePodGroupRejectsInconsistentInputs(t *testing.T) {
	pcs := newPodCliqueSet("job", "team-a")
	anchor := aggregateTestPodGang(pcs, "job-0-1000", grovecorev1alpha1.PodGangEntryRoleAnchor,
		groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1},
	)

	tests := []struct {
		name          string
		materialized  []componentutils.MaterializedPodGang
		wantErrSubstr string
	}{
		{
			name:          "no PodGangs",
			wantErrSubstr: "materializes no PodGangs",
		},
		{
			name: "no Anchor",
			materialized: []componentutils.MaterializedPodGang{
				aggregateTestPodGang(pcs, "job-0-workers-1-2000", grovecorev1alpha1.PodGangEntryRoleTail,
					groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1}),
			},
			wantErrSubstr: "has no materialized Anchor",
		},
		{
			name: "priority conflict",
			materialized: func() []componentutils.MaterializedPodGang {
				other := aggregateTestPodGang(pcs, "job-0-2000", grovecorev1alpha1.PodGangEntryRoleAnchor,
					groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1})
				other.PodGang.Spec.PriorityClassName = "different"
				return []componentutils.MaterializedPodGang{anchor, other}
			}(),
			wantErrSubstr: "conflicting priority classes",
		},
		{
			name: "unknown topology group member",
			materialized: func() []componentutils.MaterializedPodGang {
				invalid := aggregateTestPodGang(pcs, "job-0-3000", grovecorev1alpha1.PodGangEntryRoleAnchor,
					groveschedulerv1alpha1.PodGroup{Name: "worker", MinReplicas: 1})
				invalid.PodGang.Spec.TopologyConstraintGroupConfigs = []groveschedulerv1alpha1.TopologyConstraintGroupConfig{{
					Name:          "rack",
					PodGroupNames: []string{"missing"},
				}}
				return []componentutils.MaterializedPodGang{invalid}
			}(),
			wantErrSubstr: "references unknown PodGroup",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := testAggregateBackend(t).buildAggregatePodGroup(pcs, 0, tt.materialized, "")
			require.ErrorContains(t, err, tt.wantErrSubstr)
		})
	}
}

func aggregateTestPodGang(
	pcs *grovecorev1alpha1.PodCliqueSet,
	name string,
	role grovecorev1alpha1.PodGangEntryRole,
	podGroups ...groveschedulerv1alpha1.PodGroup,
) componentutils.MaterializedPodGang {
	podGang := testutils.NewPodGangBuilder(name, pcs.Namespace).Build()
	setPodCliqueSetControllerOwner(podGang, pcs)
	podGang.Spec.PodGroups = podGroups
	entry := &grovecorev1alpha1.PodGangEntry{Role: role}
	return componentutils.MaterializedPodGang{PodGang: podGang, Entry: entry}
}

func testAggregateBackend(t *testing.T) *schedulerBackend {
	t.Helper()
	cl := testutils.NewTestClientBuilder().Build()
	backend := New(cl, cl.Scheme(), record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}).(*schedulerBackend)
	initTestBackend(t, backend, cl)
	return backend
}

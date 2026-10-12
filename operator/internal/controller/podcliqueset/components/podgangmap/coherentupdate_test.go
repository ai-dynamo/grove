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

package podgangmap

import (
	"fmt"
	"strconv"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	coherentTestPCSName    = "pcs"
	coherentTestNamespace  = "default"
	coherentTestCurrentGen = "v2"
	coherentTestOldGen     = "v1"
	// coherentTestPCSGObjName is the fully qualified name of the PodCliqueScalingGroup object the
	// gatherPCSGReplicaInfos test builds member PodCliques for.
	coherentTestPCSGObjName = "pcs-0-sga"
)

// TestInScopeStandalonePCLQsByComponent checks that the standalone PodCliques of a replica are indexed by
// component name and that components outside the update scope are dropped.
func TestInScopeStandalonePCLQsByComponent(t *testing.T) {
	pcsNameReplica := apicommon.ResourceNameReplica{Name: coherentTestPCSName, Replica: 0}
	snap := &syncSnapshot{
		pcs:         &grovecorev1alpha1.PodCliqueSet{ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSName}},
		mvuTemplate: &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}},
		existingStandalonePCLQsByReplica: map[int][]grovecorev1alpha1.PodClique{
			0: {
				{ObjectMeta: metav1.ObjectMeta{Name: apicommon.GeneratePodCliqueName(pcsNameReplica, "frontend")}, Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 5}},
				{ObjectMeta: metav1.ObjectMeta{Name: apicommon.GeneratePodCliqueName(pcsNameReplica, "router")}, Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4}},
			},
		},
	}

	pclqByComponent := snap.inScopeStandalonePCLQsByComponent(0)

	require.Len(t, pclqByComponent, 1)
	assert.Contains(t, pclqByComponent, "frontend")
	assert.NotContains(t, pclqByComponent, "router")
	assert.Equal(t, int32(5), pclqByComponent["frontend"].Spec.Replicas)
}

// TestInScopePCSGsByComponent checks that the PodCliqueScalingGroups of a replica are indexed by component
// name, that out-of-scope components are dropped, and that a malformed PCSG name is reported as an error.
func TestInScopePCSGsByComponent(t *testing.T) {
	pcsNameReplica := apicommon.ResourceNameReplica{Name: coherentTestPCSName, Replica: 0}

	t.Run("indexes in-scope PodCliqueScalingGroups only", func(t *testing.T) {
		snap := &syncSnapshot{
			pcs:         &grovecorev1alpha1.PodCliqueSet{ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSName}},
			mvuTemplate: &mvuTemplate{pcsgs: map[string]int32{"decode": 3}},
			existingPCSGsByReplica: map[int][]grovecorev1alpha1.PodCliqueScalingGroup{
				0: {
					{ObjectMeta: metav1.ObjectMeta{Name: apicommon.GeneratePodCliqueScalingGroupName(pcsNameReplica, "decode")}, Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{Replicas: 6}},
					{ObjectMeta: metav1.ObjectMeta{Name: apicommon.GeneratePodCliqueScalingGroupName(pcsNameReplica, "prefill")}, Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{Replicas: 2}},
				},
			},
		}

		pcsgByComponent, err := snap.inScopePCSGsByComponent(0)

		require.NoError(t, err)
		require.Len(t, pcsgByComponent, 1)
		assert.Contains(t, pcsgByComponent, "decode")
		assert.Equal(t, int32(6), pcsgByComponent["decode"].Spec.Replicas)
	})

	t.Run("errors on a malformed PodCliqueScalingGroup name", func(t *testing.T) {
		snap := &syncSnapshot{
			pcs:         &grovecorev1alpha1.PodCliqueSet{ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSName}},
			mvuTemplate: &mvuTemplate{pcsgs: map[string]int32{"decode": 3}},
			existingPCSGsByReplica: map[int][]grovecorev1alpha1.PodCliqueScalingGroup{
				0: {{ObjectMeta: metav1.ObjectMeta{Name: "malformed"}, Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{Replicas: 6}}},
			},
		}

		_, err := snap.inScopePCSGsByComponent(0)

		require.Error(t, err)
	})
}

// TestComputeDesiredReplicas checks that the desired replica count of each in-scope component comes from
// the live child object when it exists and from the PCS template when the object is absent.
func TestComputeDesiredReplicas(t *testing.T) {
	const (
		frontendTemplateReplicas int32 = 4
		decodeTemplateReplicas   int32 = 6
	)
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSName, Namespace: coherentTestNamespace},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: frontendTemplateReplicas}},
					{Name: "decode-worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
				},
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{Name: "decode", CliqueNames: []string{"decode-worker"}, Replicas: ptr.To(decodeTemplateReplicas)},
				},
			},
		},
	}
	snap := &syncSnapshot{
		pcs:         pcs,
		mvuTemplate: &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}, pcsgs: map[string]int32{"decode": 3}},
	}
	frontendObj := grovecorev1alpha1.PodClique{Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 5}}
	decodeObj := grovecorev1alpha1.PodCliqueScalingGroup{Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{Replicas: 7}}

	tests := []struct {
		name       string
		standalone map[string]grovecorev1alpha1.PodClique
		pcsg       map[string]grovecorev1alpha1.PodCliqueScalingGroup
		want       map[string]int32
	}{
		{
			name:       "both components present use the live spec replicas",
			standalone: map[string]grovecorev1alpha1.PodClique{"frontend": frontendObj},
			pcsg:       map[string]grovecorev1alpha1.PodCliqueScalingGroup{"decode": decodeObj},
			want:       map[string]int32{"frontend": 5, "decode": 7},
		},
		{
			name:       "absent standalone falls back to the template replicas",
			standalone: map[string]grovecorev1alpha1.PodClique{},
			pcsg:       map[string]grovecorev1alpha1.PodCliqueScalingGroup{"decode": decodeObj},
			want:       map[string]int32{"frontend": frontendTemplateReplicas, "decode": 7},
		},
		{
			name:       "absent scaling group falls back to the template replicas",
			standalone: map[string]grovecorev1alpha1.PodClique{"frontend": frontendObj},
			pcsg:       map[string]grovecorev1alpha1.PodCliqueScalingGroup{},
			want:       map[string]int32{"frontend": 5, "decode": decodeTemplateReplicas},
		},
		{
			name:       "both absent fall back to the template replicas",
			standalone: map[string]grovecorev1alpha1.PodClique{},
			pcsg:       map[string]grovecorev1alpha1.PodCliqueScalingGroup{},
			want:       map[string]int32{"frontend": frontendTemplateReplicas, "decode": decodeTemplateReplicas},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := snap.computeDesiredReplicas(tc.standalone, tc.pcsg)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestSubsumedPodsScheduled covers the standalone-Pod scheduling gate. Each case names the standalone
// components with their committed current-hash count and their UpdateProgress.UpdatedScheduledReplicas, and
// states whether the gate lets the next sub-step proceed.
func TestSubsumedPodsScheduled(t *testing.T) {
	testCases := []struct {
		description                 string
		standalonePCLQByComponent   map[string]grovecorev1alpha1.PodClique
		currentHashCountByComponent map[string]int32
		want                        bool
	}{
		{
			description:                 "every subsumed Pod is scheduled so the gate proceeds",
			standalonePCLQByComponent:   map[string]grovecorev1alpha1.PodClique{"frontend": pclqWithUpdatedScheduledReplicas(5)},
			currentHashCountByComponent: map[string]int32{"frontend": 5},
			want:                        true,
		},
		{
			description:                 "a component is still catching up so the gate holds",
			standalonePCLQByComponent:   map[string]grovecorev1alpha1.PodClique{"frontend": pclqWithUpdatedScheduledReplicas(3)},
			currentHashCountByComponent: map[string]int32{"frontend": 5},
			want:                        false,
		},
		{
			description:                 "nothing committed yet so there is nothing to wait on",
			standalonePCLQByComponent:   map[string]grovecorev1alpha1.PodClique{"frontend": pclqWithUpdatedScheduledReplicas(0)},
			currentHashCountByComponent: map[string]int32{"frontend": 0},
			want:                        true,
		},
		{
			description:                 "an entry is committed but UpdateProgress is not yet initialized so the gate holds",
			standalonePCLQByComponent:   map[string]grovecorev1alpha1.PodClique{"frontend": {}},
			currentHashCountByComponent: map[string]int32{"frontend": 2},
			want:                        false,
		},
		{
			description:                 "a previously scheduled Pod regressed so the gate re-holds",
			standalonePCLQByComponent:   map[string]grovecorev1alpha1.PodClique{"frontend": pclqWithUpdatedScheduledReplicas(7)},
			currentHashCountByComponent: map[string]int32{"frontend": 8},
			want:                        false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			assert.Equal(t, tc.want, subsumedPodsScheduled(tc.standalonePCLQByComponent, planPosition{currentHashCountByComponent: tc.currentHashCountByComponent}))
		})
	}
}

// TestMaxUnavailableBudgetSatisfied covers the disruption budget gate. Standalone PodCliques use the
// count-anchored budget (existing non-terminating minus the availability floor minus in-flight replacements,
// with missing old-version Pods a free reclaim), so an already-unavailable old Pod never blocks its own
// replacement. PodCliqueScalingGroups use the same count-anchored budget over their replica indices.
func TestMaxUnavailableBudgetSatisfied(t *testing.T) {
	testCases := []struct {
		description               string
		desiredReplicas           map[string]int32
		maxUnavailableByComponent map[string]int32
		nonTerminatingByPCLQ      map[string]int32
		newNotReadyByPCLQ         map[string]int32
		numMissingOldVersionPods  map[string]int32
		pcsgReplicaInfos          map[string][]pcsgReplicaInfo
		drainByComponent          map[string]int32
		want                      bool
	}{
		{
			description:               "a single-replica standalone PodClique whose only Pod is unavailable can start its own replacement",
			nonTerminatingByPCLQ:      map[string]int32{"frontend": 1},
			desiredReplicas:           map[string]int32{"frontend": 1},
			maxUnavailableByComponent: map[string]int32{"frontend": 1},
			drainByComponent:          map[string]int32{"frontend": 1},
			want:                      true,
		},
		{
			description:               "a fully available standalone PodClique absorbs a full-budget drain",
			nonTerminatingByPCLQ:      map[string]int32{"frontend": 10},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			drainByComponent:          map[string]int32{"frontend": 3},
			want:                      true,
		},
		{
			description:               "a standalone drain beyond the budget holds",
			nonTerminatingByPCLQ:      map[string]int32{"frontend": 10},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			drainByComponent:          map[string]int32{"frontend": 4},
			want:                      false,
		},
		{
			description:               "in-flight replacements consume the standalone budget so a further drain holds",
			nonTerminatingByPCLQ:      map[string]int32{"frontend": 10},
			newNotReadyByPCLQ:         map[string]int32{"frontend": 2},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			drainByComponent:          map[string]int32{"frontend": 2},
			want:                      false,
		},
		{
			description:               "more unavailable old replicas than MaxUnavailable still lets the budgeted drain proceed",
			nonTerminatingByPCLQ:      map[string]int32{"frontend": 5},
			desiredReplicas:           map[string]int32{"frontend": 5},
			maxUnavailableByComponent: map[string]int32{"frontend": 1},
			drainByComponent:          map[string]int32{"frontend": 1},
			want:                      true,
		},
		{
			description:               "a standalone PodClique reclaiming only missing old-version Pods proceeds even when over budget",
			nonTerminatingByPCLQ:      map[string]int32{"frontend": 6},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			drainByComponent:          map[string]int32{"frontend": 4},
			numMissingOldVersionPods:  map[string]int32{"frontend": 4},
			want:                      true,
		},
		{
			description:               "a standalone drain beyond its missing old-version Pods crossing the budget holds",
			nonTerminatingByPCLQ:      map[string]int32{"frontend": 10},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			drainByComponent:          map[string]int32{"frontend": 5},
			numMissingOldVersionPods:  map[string]int32{"frontend": 1},
			want:                      false,
		},
		{
			description:               "a standalone PodClique the sub-step does not drain is not gated",
			nonTerminatingByPCLQ:      map[string]int32{"frontend": 6},
			newNotReadyByPCLQ:         map[string]int32{"frontend": 4},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			drainByComponent:          map[string]int32{},
			want:                      true,
		},
		{
			description:               "a single-replica PodCliqueScalingGroup whose only replica is unavailable can start its replacement",
			pcsgReplicaInfos:          pcsgReplicaInfosWith([]componentutils.PCSGReplicaState{componentutils.PCSGReplicaStateUnavailable}, nil),
			desiredReplicas:           map[string]int32{"decode": 1},
			maxUnavailableByComponent: map[string]int32{"decode": 1},
			drainByComponent:          map[string]int32{"decode": 1},
			want:                      true,
		},
		{
			description:               "more unavailable PodCliqueScalingGroup replicas than MaxUnavailable still lets the budgeted drain proceed",
			pcsgReplicaInfos:          pcsgReplicaInfosWith([]componentutils.PCSGReplicaState{componentutils.PCSGReplicaStateReady, componentutils.PCSGReplicaStateUnavailable, componentutils.PCSGReplicaStateUnavailable, componentutils.PCSGReplicaStateUnavailable, componentutils.PCSGReplicaStateUnavailable}, nil),
			desiredReplicas:           map[string]int32{"decode": 5},
			maxUnavailableByComponent: map[string]int32{"decode": 1},
			drainByComponent:          map[string]int32{"decode": 1},
			want:                      true,
		},
		{
			description:               "a PodCliqueScalingGroup drain that exactly reaches the budget proceeds",
			pcsgReplicaInfos:          pcsgReplicaInfosWith(repeatPCSGState(componentutils.PCSGReplicaStateReady, 6), nil),
			desiredReplicas:           map[string]int32{"decode": 6},
			maxUnavailableByComponent: map[string]int32{"decode": 2},
			drainByComponent:          map[string]int32{"decode": 2},
			want:                      true,
		},
		{
			description:               "a PodCliqueScalingGroup drain beyond the budget holds",
			pcsgReplicaInfos:          pcsgReplicaInfosWith(repeatPCSGState(componentutils.PCSGReplicaStateReady, 6), nil),
			desiredReplicas:           map[string]int32{"decode": 6},
			maxUnavailableByComponent: map[string]int32{"decode": 2},
			drainByComponent:          map[string]int32{"decode": 3},
			want:                      false,
		},
		{
			description:               "in-flight PodCliqueScalingGroup replacements consume the budget so a further drain holds",
			pcsgReplicaInfos:          pcsgReplicaInfosWith(repeatPCSGState(componentutils.PCSGReplicaStateReady, 4), repeatPCSGState(componentutils.PCSGReplicaStateUnavailable, 2)),
			desiredReplicas:           map[string]int32{"decode": 6},
			maxUnavailableByComponent: map[string]int32{"decode": 2},
			drainByComponent:          map[string]int32{"decode": 1},
			want:                      false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			planner := &subStepPlanner{
				desiredReplicas:           tc.desiredReplicas,
				maxUnavailableByComponent: tc.maxUnavailableByComponent,
				pcsgReplicaInfos:          tc.pcsgReplicaInfos,
				pclqPodCounts: standalonePCLQPodCounts{
					nonTerminatingByPCLQ: tc.nonTerminatingByPCLQ,
					newNotReadyByPCLQ:    tc.newNotReadyByPCLQ,
				},
				numMissingOldVersionPodsByPCLQ: tc.numMissingOldVersionPods,
			}
			assert.Equal(t, tc.want, planner.maxUnavailableBudgetSatisfied(tc.drainByComponent))
		})
	}
}

// TestCurrentBatchScheduled covers the scheduling gate on the most recent current-hash batch. It reports
// true when no current-hash entry exists yet, and otherwise tracks whether the PodGangs at the latest
// current-hash epoch have been scheduled at least once.
func TestCurrentBatchScheduled(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSName, Namespace: coherentTestNamespace},
		Status:     grovecorev1alpha1.PodCliqueSetStatus{CurrentGenerationHash: ptr.To(coherentTestCurrentGen)},
	}
	rnr := apicommon.ResourceNameReplica{Name: coherentTestPCSName, Replica: 0}
	anchorEntry := grovecorev1alpha1.PodGangEntry{Epoch: "200", PodCliqueSetGenerationHash: coherentTestCurrentGen, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 2}}
	tailEntry := grovecorev1alpha1.PodGangEntry{Epoch: "200", PodCliqueSetGenerationHash: coherentTestCurrentGen, Role: grovecorev1alpha1.PodGangEntryRoleTail, PCSGReplicaIndices: map[string][]int32{"inference": {0, 1}}}
	anchorPodGang := apicommon.GenerateAnchorPodGangName(rnr, "200")
	tailPodGang0 := apicommon.GenerateNonAnchorPodGangName(rnr, "200", "inference", 0)
	tailPodGang1 := apicommon.GenerateNonAnchorPodGangName(rnr, "200", "inference", 1)

	t.Run("no current-hash entry yet so nothing to wait on", func(t *testing.T) {
		entries := []grovecorev1alpha1.PodGangEntry{{Epoch: "50", PodCliqueSetGenerationHash: coherentTestOldGen, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 2}}}
		r := _resource{client: testutils.NewTestClientBuilder().Build()}

		ready, err := r.currentBatchScheduled(t.Context(), pcs, 0, entries)

		require.NoError(t, err)
		assert.True(t, ready)
	})

	t.Run("anchor batch is scheduled", func(t *testing.T) {
		r := _resource{client: testutils.NewTestClientBuilder().WithObjects(podGangAtEpoch(anchorPodGang, true)).Build()}

		ready, err := r.currentBatchScheduled(t.Context(), pcs, 0, []grovecorev1alpha1.PodGangEntry{anchorEntry})

		require.NoError(t, err)
		assert.True(t, ready)
	})

	t.Run("anchor batch is not scheduled", func(t *testing.T) {
		r := _resource{client: testutils.NewTestClientBuilder().WithObjects(podGangAtEpoch(anchorPodGang, false)).Build()}

		ready, err := r.currentBatchScheduled(t.Context(), pcs, 0, []grovecorev1alpha1.PodGangEntry{anchorEntry})

		require.NoError(t, err)
		assert.False(t, ready)
	})

	t.Run("tail batch fully materialized and scheduled", func(t *testing.T) {
		r := _resource{client: testutils.NewTestClientBuilder().WithObjects(podGangAtEpoch(tailPodGang0, true), podGangAtEpoch(tailPodGang1, true)).Build()}

		ready, err := r.currentBatchScheduled(t.Context(), pcs, 0, []grovecorev1alpha1.PodGangEntry{tailEntry})

		require.NoError(t, err)
		assert.True(t, ready)
	})

	t.Run("tail batch only partially materialized holds", func(t *testing.T) {
		// The entry commits indices 0 and 1, but only index 0's PodGang exists. The gate must not pass on the
		// gang that happens to exist, since the executor has already taken down the old pods for index 1.
		r := _resource{client: testutils.NewTestClientBuilder().WithObjects(podGangAtEpoch(tailPodGang0, true)).Build()}

		ready, err := r.currentBatchScheduled(t.Context(), pcs, 0, []grovecorev1alpha1.PodGangEntry{tailEntry})

		require.NoError(t, err)
		assert.False(t, ready)
	})
}

// TestBuildCoherentUpdateEntries covers the sub-step authoring for a single standalone component frontend
// with liveReplicas 4, MinAvailable 2, and MaxUnavailable 2. The anchor commits 2 pods and one tail
// sub-step subsumes the remaining 2. Each case states the committed entries and asserts whether the entries
// are held unchanged or advanced.
func TestBuildCoherentUpdateEntries(t *testing.T) {
	pcsNameReplica := apicommon.ResourceNameReplica{Name: coherentTestPCSName, Replica: 0}
	anchorV1 := grovecorev1alpha1.PodGangEntry{Epoch: "50", PodCliqueSetGenerationHash: coherentTestOldGen, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 2}}
	anchorV2 := grovecorev1alpha1.PodGangEntry{Epoch: "200", PodCliqueSetGenerationHash: coherentTestCurrentGen, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 2}}

	t.Run("no sub-step remains so the entries are unchanged", func(t *testing.T) {
		fullyRolledAnchor := grovecorev1alpha1.PodGangEntry{Epoch: "200", PodCliqueSetGenerationHash: coherentTestCurrentGen, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 4}}
		pgm := &grovecorev1alpha1.PodGangMap{Spec: grovecorev1alpha1.PodGangMapSpec{Entries: []grovecorev1alpha1.PodGangEntry{fullyRolledAnchor}}}
		snap := newCoherentTestSnapshot(pcsNameReplica, 4, 2, nil)
		r := _resource{client: testutils.NewTestClientBuilder().WithPodControllerUIDIndex().Build(), clk: clocktesting.NewFakeClock(metav1.Now().Time)}

		entries, err := r.buildCoherentUpdateEntries(t.Context(), snap, 0, pgm)

		require.NoError(t, err)
		assert.Equal(t, pgm.Spec.Entries, entries)
	})

	t.Run("gate holds when the current batch is not ready so the entries are unchanged", func(t *testing.T) {
		pgm := &grovecorev1alpha1.PodGangMap{Spec: grovecorev1alpha1.PodGangMapSpec{Entries: []grovecorev1alpha1.PodGangEntry{anchorV1, anchorV2}}}
		snap := newCoherentTestSnapshot(pcsNameReplica, 4, 2, ptr.To[int32](2))
		// No PodGang exists at the latest current-hash epoch, so currentBatchScheduled is false.
		r := _resource{client: testutils.NewTestClientBuilder().WithPodControllerUIDIndex().Build(), clk: clocktesting.NewFakeClock(metav1.Now().Time)}

		entries, err := r.buildCoherentUpdateEntries(t.Context(), snap, 0, pgm)

		require.NoError(t, err)
		assert.Equal(t, pgm.Spec.Entries, entries)
	})

	t.Run("gate passes so the next sub-step is emitted", func(t *testing.T) {
		pgm := &grovecorev1alpha1.PodGangMap{Spec: grovecorev1alpha1.PodGangMapSpec{Entries: []grovecorev1alpha1.PodGangEntry{anchorV1, anchorV2}}}
		snap := newCoherentTestSnapshot(pcsNameReplica, 4, 2, ptr.To[int32](2))
		r := _resource{client: testutils.NewTestClientBuilder().WithPodControllerUIDIndex().WithObjects(podGangAtEpoch(apicommon.GenerateAnchorPodGangName(pcsNameReplica, "200"), true)).Build(), clk: clocktesting.NewFakeClock(metav1.Now().Time)}

		entries, err := r.buildCoherentUpdateEntries(t.Context(), snap, 0, pgm)

		require.NoError(t, err)
		// The tail sub-step subsumes the remaining 2 pods into the current-hash anchor and drains the old
		// anchor to empty, leaving a single current-hash anchor carrying all 4 pods.
		require.Len(t, entries, 1)
		assert.Equal(t, coherentTestCurrentGen, entries[0].PodCliqueSetGenerationHash)
		assert.Equal(t, int32(4), entries[0].PodCliques["frontend"])
	})
}

// TestEntryHoldsInScopeContent checks the predicate that gates reconvergence, reporting whether an entry
// still carries pods or replica indices for any component within the coherent update scope.
func TestEntryHoldsInScopeContent(t *testing.T) {
	mvu := &mvuTemplate{
		standalonePCLQs: map[string]int32{"frontend": 1},
		pcsgs:           map[string]int32{"inference": 1},
	}
	testCases := []struct {
		description string
		entry       grovecorev1alpha1.PodGangEntry
		want        bool
	}{
		{"holds an in-scope standalone clique", grovecorev1alpha1.PodGangEntry{PodCliques: map[string]int32{"frontend": 2}}, true},
		{"holds in-scope PCSG replica indices", grovecorev1alpha1.PodGangEntry{PCSGReplicaIndices: map[string][]int32{"inference": {0}}}, true},
		{"holds only out-of-scope standalone content", grovecorev1alpha1.PodGangEntry{PodCliques: map[string]int32{"router": 3}}, false},
		{"in-scope standalone at zero count holds nothing", grovecorev1alpha1.PodGangEntry{PodCliques: map[string]int32{"frontend": 0}}, false},
		{"in-scope PCSG with empty indices holds nothing", grovecorev1alpha1.PodGangEntry{PCSGReplicaIndices: map[string][]int32{"inference": {}}}, false},
		{"empty entry holds nothing", grovecorev1alpha1.PodGangEntry{}, false},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			assert.Equal(t, tc.want, entryHoldsInScopeContent(tc.entry, mvu))
		})
	}
}

// TestAdvanceFullyDrainedEntries covers the three reconvergence outcomes for an old-generation entry. An
// entry drained of its in-scope content but still holding out-of-scope content advances to the current
// generation, an entry still holding in-scope content keeps its generation, and an empty entry is left for
// removeEmptyEntries to drop. frontend and inference are in scope, router is out of scope.
func TestAdvanceFullyDrainedEntries(t *testing.T) {
	mvu := &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 1}, pcsgs: map[string]int32{"inference": 1}}
	entryAt := func(gen string, pclqs map[string]int32, pcsg map[string][]int32) grovecorev1alpha1.PodGangEntry {
		return grovecorev1alpha1.PodGangEntry{Epoch: "50", PodCliqueSetGenerationHash: gen, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: pclqs, PCSGReplicaIndices: pcsg}
	}
	testCases := []struct {
		description string
		entry       grovecorev1alpha1.PodGangEntry
		wantGen     string
	}{
		{"advances an old-gen entry drained of in-scope content but holding out-of-scope content", entryAt(coherentTestOldGen, map[string]int32{"router": 2}, nil), coherentTestCurrentGen},
		{"keeps an old-gen entry still holding in-scope content", entryAt(coherentTestOldGen, nil, map[string][]int32{"inference": {0}}), coherentTestOldGen},
		{"leaves an empty old-gen entry for removal", entryAt(coherentTestOldGen, nil, nil), coherentTestOldGen},
		{"leaves a current-gen entry unchanged", entryAt(coherentTestCurrentGen, map[string]int32{"router": 2}, nil), coherentTestCurrentGen},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			got := advanceFullyDrainedEntries([]grovecorev1alpha1.PodGangEntry{tc.entry}, coherentTestCurrentGen, mvu)
			assert.Equal(t, tc.wantGen, got[0].PodCliqueSetGenerationHash)
		})
	}
}

// TestHeadroomByComponent checks the per-component drain headroom. For a standalone PodClique it is the
// free missing old-version reclaims plus the count-anchored running-Pod takedown headroom. For a
// PodCliqueScalingGroup it is the count-anchored replica takedown headroom, since a PodCliqueScalingGroup
// has no missing old-version term.
func TestHeadroomByComponent(t *testing.T) {
	testCases := []struct {
		description               string
		pcsgReplicaInfos          map[string][]pcsgReplicaInfo
		desiredReplicas           map[string]int32
		maxUnavailableByComponent map[string]int32
		nonTerminatingByPCLQ      map[string]int32
		newNotReadyByPCLQ         map[string]int32
		numMissingOldVersionPods  map[string]int32
		want                      map[string]int32
	}{
		{
			description:               "standalone fully available takes the full budget",
			nonTerminatingByPCLQ:      map[string]int32{"frontend": 10},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			want:                      map[string]int32{"frontend": 3},
		},
		{
			description:               "standalone in-flight replacements reduce the headroom",
			nonTerminatingByPCLQ:      map[string]int32{"frontend": 10},
			newNotReadyByPCLQ:         map[string]int32{"frontend": 2},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			want:                      map[string]int32{"frontend": 1},
		},
		{
			description:               "standalone short of desired with no missing old-version Pod has zero headroom",
			nonTerminatingByPCLQ:      map[string]int32{"frontend": 6},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			want:                      map[string]int32{"frontend": 0},
		},
		{
			description:               "standalone short of desired with missing old-version Pods has headroom equal to that count",
			nonTerminatingByPCLQ:      map[string]int32{"frontend": 6},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			numMissingOldVersionPods:  map[string]int32{"frontend": 4},
			want:                      map[string]int32{"frontend": 4},
		},
		{
			description:               "standalone within budget plus missing old-version Pods adds both",
			nonTerminatingByPCLQ:      map[string]int32{"frontend": 9},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			numMissingOldVersionPods:  map[string]int32{"frontend": 1},
			want:                      map[string]int32{"frontend": 3},
		},
		{
			description:               "PodCliqueScalingGroup within budget leaves the remaining budget",
			pcsgReplicaInfos:          pcsgReplicaInfosWith(repeatPCSGState(componentutils.PCSGReplicaStateReady, 6), nil),
			desiredReplicas:           map[string]int32{"decode": 6},
			maxUnavailableByComponent: map[string]int32{"decode": 2},
			want:                      map[string]int32{"decode": 2},
		},
		{
			description:               "PodCliqueScalingGroup short of desired has zero headroom",
			pcsgReplicaInfos:          pcsgReplicaInfosWith(repeatPCSGState(componentutils.PCSGReplicaStateReady, 3), nil),
			desiredReplicas:           map[string]int32{"decode": 6},
			maxUnavailableByComponent: map[string]int32{"decode": 2},
			want:                      map[string]int32{"decode": 0},
		},
		{
			description:               "standalone and PodCliqueScalingGroup headroom computed together",
			nonTerminatingByPCLQ:      map[string]int32{"frontend": 10},
			newNotReadyByPCLQ:         map[string]int32{"frontend": 2},
			pcsgReplicaInfos:          pcsgReplicaInfosWith(repeatPCSGState(componentutils.PCSGReplicaStateReady, 6), nil),
			desiredReplicas:           map[string]int32{"frontend": 10, "decode": 6},
			maxUnavailableByComponent: map[string]int32{"frontend": 3, "decode": 2},
			numMissingOldVersionPods:  map[string]int32{"frontend": 1},
			want:                      map[string]int32{"frontend": 2, "decode": 2},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			planner := &subStepPlanner{
				desiredReplicas:           tc.desiredReplicas,
				maxUnavailableByComponent: tc.maxUnavailableByComponent,
				pcsgReplicaInfos:          tc.pcsgReplicaInfos,
				pclqPodCounts: standalonePCLQPodCounts{
					nonTerminatingByPCLQ: tc.nonTerminatingByPCLQ,
					newNotReadyByPCLQ:    tc.newNotReadyByPCLQ,
				},
				numMissingOldVersionPodsByPCLQ: tc.numMissingOldVersionPods,
			}
			assert.Equal(t, tc.want, planner.headroomByComponent())
		})
	}
}

// TestNumMissingOldVersionPodsByStandalonePCLQ covers the count of missing old-version Pods per standalone
// PodClique: old-version anchor deficits are counted, current-version anchors and out-of-scope cliques are
// ignored, running at or above committed yields none, and deficits sum across old anchors.
func TestNumMissingOldVersionPodsByStandalonePCLQ(t *testing.T) {
	anchor := func(gen, epoch string, pclqs map[string]int32) grovecorev1alpha1.PodGangEntry {
		return grovecorev1alpha1.PodGangEntry{Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliqueSetGenerationHash: gen, Epoch: epoch, PodCliques: pclqs}
	}
	testCases := []struct {
		description string
		entries     []grovecorev1alpha1.PodGangEntry
		running     map[string]map[string]int32
		want        map[string]int32
	}{
		{
			description: "an old-version anchor with fewer running Pods than committed yields the deficit",
			entries:     []grovecorev1alpha1.PodGangEntry{anchor(coherentTestOldGen, "50", map[string]int32{"frontend": 3})},
			running:     map[string]map[string]int32{"frontend": {"50": 2}},
			want:        map[string]int32{"frontend": 1},
		},
		{
			description: "a current-version anchor is never a missing old-version Pod",
			entries:     []grovecorev1alpha1.PodGangEntry{anchor(coherentTestCurrentGen, "200", map[string]int32{"frontend": 3})},
			running:     map[string]map[string]int32{"frontend": {"200": 0}},
			want:        map[string]int32{},
		},
		{
			description: "an out-of-scope clique on an old-version anchor is ignored",
			entries:     []grovecorev1alpha1.PodGangEntry{anchor(coherentTestOldGen, "50", map[string]int32{"frontend": 2, "sidecar": 2})},
			running:     map[string]map[string]int32{"frontend": {"50": 2}},
			want:        map[string]int32{},
		},
		{
			description: "running equal to committed yields no missing old-version Pod",
			entries:     []grovecorev1alpha1.PodGangEntry{anchor(coherentTestOldGen, "50", map[string]int32{"frontend": 2})},
			running:     map[string]map[string]int32{"frontend": {"50": 2}},
			want:        map[string]int32{},
		},
		{
			description: "more running than committed clamps to zero",
			entries:     []grovecorev1alpha1.PodGangEntry{anchor(coherentTestOldGen, "50", map[string]int32{"frontend": 2})},
			running:     map[string]map[string]int32{"frontend": {"50": 3}},
			want:        map[string]int32{},
		},
		{
			description: "deficits sum across multiple old-version anchors",
			entries: []grovecorev1alpha1.PodGangEntry{
				anchor(coherentTestOldGen, "50", map[string]int32{"frontend": 2}),
				anchor("v0", "40", map[string]int32{"frontend": 3}),
			},
			running: map[string]map[string]int32{"frontend": {"50": 1, "40": 1}},
			want:    map[string]int32{"frontend": 3},
		},
		{
			description: "a non-anchor entry is ignored",
			entries:     []grovecorev1alpha1.PodGangEntry{{Role: grovecorev1alpha1.PodGangEntryRoleTail, PodCliqueSetGenerationHash: coherentTestOldGen, Epoch: "60", PodCliques: map[string]int32{"frontend": 5}}},
			running:     map[string]map[string]int32{"frontend": {}},
			want:        map[string]int32{},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			counts := standalonePCLQPodCounts{
				livePodCountsByAnchor: map[cliqueAndEpochKey]anchorLivePods{},
				nonTerminatingByPCLQ:  map[string]int32{},
			}
			for clique, runningByEpoch := range tc.running {
				counts.nonTerminatingByPCLQ[clique] = 0 // presence marks the clique in scope
				for epoch, running := range runningByEpoch {
					counts.livePodCountsByAnchor[cliqueAndEpochKey{clique: clique, epoch: epoch}] = anchorLivePods{running: running}
				}
			}
			assert.Equal(t, tc.want, numMissingOldVersionPodsByStandalonePCLQ(tc.entries, coherentTestCurrentGen, counts))
		})
	}
}

// TestGatherStandalonePodCounts covers the single-pass Pod count read. livePodCountsByAnchor buckets
// non-terminating Pods by their grove.io/podgang label mapped to an anchor epoch, recording the running and
// not-Ready counts, and excludes terminating Pods and Pods on a PodGang that is not an anchor of this
// replica. nonTerminatingByPCLQ counts non-terminating Pods on a known anchor of this replica, so it
// excludes a Pod off any anchor. newNotReadyByPCLQ counts non-terminating,
// not-Ready Pods on a current-hash anchor, so it excludes a not-Ready Pod on an old anchor and a not-Ready
// Pod off any anchor.
func TestGatherStandalonePodCounts(t *testing.T) {
	rnr := apicommon.ResourceNameReplica{Name: coherentTestPCSName, Replica: 0}
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSName, Namespace: coherentTestNamespace},
		Status:     grovecorev1alpha1.PodCliqueSetStatus{CurrentGenerationHash: ptr.To("new")},
	}
	pclq := grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: "frontend-pclq", Namespace: coherentTestNamespace, UID: "frontend-uid"}}
	entries := []grovecorev1alpha1.PodGangEntry{
		{Role: grovecorev1alpha1.PodGangEntryRoleAnchor, Epoch: "50", PodCliqueSetGenerationHash: "old"},
		{Role: grovecorev1alpha1.PodGangEntryRoleAnchor, Epoch: "200", PodCliqueSetGenerationHash: "new"},
	}
	oldGang := apicommon.GenerateAnchorPodGangName(rnr, "50")
	currentGang := apicommon.GenerateAnchorPodGangName(rnr, "200")
	pod := func(name, gang string, terminating, ready bool) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       coherentTestNamespace,
			Labels:          map[string]string{apicommon.LabelPodGang: gang},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "grove.io/v1alpha1", Kind: "PodClique", Name: pclq.Name, UID: pclq.UID, Controller: ptr.To(true)}},
		}}
		if terminating {
			p.DeletionTimestamp = ptr.To(metav1.Now())
			p.Finalizers = []string{"grove.io/test"}
		}
		if ready {
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		}
		return p
	}
	objs := []client.Object{
		pod("p1", oldGang, false, true),          // old anchor, Ready
		pod("p2", oldGang, false, false),         // old anchor, not Ready (not counted as newNotReady)
		pod("p3", currentGang, false, true),      // current anchor, Ready
		pod("p6", currentGang, false, false),     // current anchor, not Ready (the only newNotReady)
		pod("p4", currentGang, true, true),       // terminating, excluded everywhere
		pod("p5", "unrelated-gang", false, true), // Ready, off any anchor of this replica
	}
	r := _resource{client: testutils.NewTestClientBuilder().WithPodControllerUIDIndex().WithObjects(objs...).Build()}

	got, err := r.gatherStandalonePodCounts(t.Context(), pcs, 0, entries, map[string]grovecorev1alpha1.PodClique{"frontend": pclq})

	require.NoError(t, err)
	assert.Equal(t, map[cliqueAndEpochKey]anchorLivePods{
		{clique: "frontend", epoch: "50"}:  {running: 2, notReady: 1},
		{clique: "frontend", epoch: "200"}: {running: 2, notReady: 1},
	}, got.livePodCountsByAnchor)
	assert.Equal(t, map[string]int32{"frontend": 4}, got.nonTerminatingByPCLQ)
	assert.Equal(t, map[string]int32{"frontend": 1}, got.newNotReadyByPCLQ)
}

// TestGatherPCSGReplicaInfos covers the per-replica-index read of a PodCliqueScalingGroup's member
// PodCliques: a replica is present unless all its members terminate, its health comes from
// ComputePCSGReplicaState over the members, and atCurrentHash is set when a current-hash entry commits the
// index.
func TestGatherPCSGReplicaInfos(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSName, Namespace: coherentTestNamespace},
		Status:     grovecorev1alpha1.PodCliqueSetStatus{CurrentGenerationHash: ptr.To("new")},
	}
	pcsg := grovecorev1alpha1.PodCliqueScalingGroup{ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSGObjName, Namespace: coherentTestNamespace}, Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{Replicas: 4, CliqueNames: []string{"m"}}}
	// A current-hash entry commits replica index 0 of sga, so it is at the current hash.
	entries := []grovecorev1alpha1.PodGangEntry{
		{Role: grovecorev1alpha1.PodGangEntryRoleAnchor, Epoch: "100", PodCliqueSetGenerationHash: "new", PCSGReplicaIndices: map[string][]int32{"sga": {0}}},
	}
	objs := []client.Object{
		pcsgMemberPCLQ("sga-0-m", 0, 1, 1, false), // committed to current hash, Ready
		pcsgMemberPCLQ("sga-1-m", 1, 1, 0, false), // old, Unavailable (ready below MinAvailable)
		pcsgMemberPCLQ("sga-2-m", 2, 0, 0, false), // old, Pending (scheduled below MinAvailable)
		pcsgMemberPCLQ("sga-3-m", 3, 1, 1, true),  // all members terminating during a restart, classified Pending
	}
	r := _resource{client: testutils.NewTestClientBuilder().WithObjects(objs...).Build()}

	got, err := r.gatherPCSGReplicaInfos(t.Context(), pcs, entries, map[string]grovecorev1alpha1.PodCliqueScalingGroup{"sga": pcsg})

	require.NoError(t, err)
	assert.ElementsMatch(t, []pcsgReplicaInfo{
		{index: 0, state: componentutils.PCSGReplicaStateReady, atCurrentHash: true},
		{index: 1, state: componentutils.PCSGReplicaStateUnavailable, atCurrentHash: false},
		{index: 2, state: componentutils.PCSGReplicaStatePending, atCurrentHash: false},
		{index: 3, state: componentutils.PCSGReplicaStatePending, atCurrentHash: false},
	}, got["sga"])
}

// TestGatherPCSGReplicaInfosSkipsStrayReplicaIndices verifies a PodCliqueScalingGroup child whose replica
// index is outside [0, Spec.Replicas), a stray left by a scale-down, is excluded from the gather. A healthy
// stray must be excluded too, so the drain can neither count it nor commit it into a current-hash entry the
// PCSG controller would never recreate.
func TestGatherPCSGReplicaInfosSkipsStrayReplicaIndices(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSName, Namespace: coherentTestNamespace},
		Status:     grovecorev1alpha1.PodCliqueSetStatus{CurrentGenerationHash: ptr.To("new")},
	}
	pcsg := grovecorev1alpha1.PodCliqueScalingGroup{ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSGObjName, Namespace: coherentTestNamespace}, Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{Replicas: 2}}
	objs := []client.Object{
		pcsgMemberPCLQ("sga-0-m", 0, 1, 1, false), // in range, Ready
		pcsgMemberPCLQ("sga-1-m", 1, 1, 1, false), // in range, Ready
		pcsgMemberPCLQ("sga-2-m", 2, 1, 1, false), // out of range (index >= Spec.Replicas), scale-down stray, skipped
	}
	r := _resource{client: testutils.NewTestClientBuilder().WithObjects(objs...).Build()}

	got, err := r.gatherPCSGReplicaInfos(t.Context(), pcs, nil, map[string]grovecorev1alpha1.PodCliqueScalingGroup{"sga": pcsg})

	require.NoError(t, err)
	assert.ElementsMatch(t, []pcsgReplicaInfo{
		{index: 0, state: componentutils.PCSGReplicaStateReady, atCurrentHash: false},
		{index: 1, state: componentutils.PCSGReplicaStateReady, atCurrentHash: false},
	}, got["sga"])
}

// TestGatherPCSGReplicaInfosInventoriesAbsentReplicasAsPending verifies that every desired replica index is
// inventoried: an index with no member PodCliques (absent) and an index whose members are all terminating (a
// gang restart) are both classified Pending, so the anchor is never opened short of its replicas.
func TestGatherPCSGReplicaInfosInventoriesAbsentReplicasAsPending(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSName, Namespace: coherentTestNamespace},
		Status:     grovecorev1alpha1.PodCliqueSetStatus{CurrentGenerationHash: ptr.To("new")},
	}
	pcsg := grovecorev1alpha1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      coherentTestPCSGObjName,
			Namespace: coherentTestNamespace,
		},
		Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
			Replicas:    3,
			CliqueNames: []string{"m"},
		},
	}
	objs := []client.Object{
		pcsgMemberPCLQ("sga-0-m", 0, 1, 1, false), // present, Ready
		// index 1 has no member PodCliques (absent)
		pcsgMemberPCLQ("sga-2-m", 2, 1, 1, true), // all members terminating (restart)
	}
	r := _resource{client: testutils.NewTestClientBuilder().WithObjects(objs...).Build()}

	got, err := r.gatherPCSGReplicaInfos(t.Context(), pcs, nil, map[string]grovecorev1alpha1.PodCliqueScalingGroup{"sga": pcsg})

	require.NoError(t, err)
	assert.ElementsMatch(t, []pcsgReplicaInfo{
		{index: 0, state: componentutils.PCSGReplicaStateReady, atCurrentHash: false},
		{index: 1, state: componentutils.PCSGReplicaStatePending, atCurrentHash: false},
		{index: 2, state: componentutils.PCSGReplicaStatePending, atCurrentHash: false},
	}, got["sga"])
}

// pcsgMemberPCLQ builds a PodCliqueScalingGroup member PodClique at the given replica index with the owner
// reference, labels, MinAvailable, and scheduled and ready status counts that gatherPCSGReplicaInfos reads.
func pcsgMemberPCLQ(name string, replicaIndex, scheduled, ready int32, terminating bool) *grovecorev1alpha1.PodClique {
	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: coherentTestNamespace,
			Labels: map[string]string{
				apicommon.LabelPodCliqueScalingGroup:             coherentTestPCSGObjName,
				apicommon.LabelPodCliqueScalingGroupReplicaIndex: strconv.Itoa(int(replicaIndex)),
			},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "grove.io/v1alpha1", Kind: constants.KindPodCliqueScalingGroup, Name: coherentTestPCSGObjName, Controller: ptr.To(true)}},
		},
		Spec:   grovecorev1alpha1.PodCliqueSpec{MinAvailable: ptr.To(int32(1))},
		Status: grovecorev1alpha1.PodCliqueStatus{ScheduledReplicas: scheduled, ReadyReplicas: ready},
	}
	if terminating {
		pclq.DeletionTimestamp = ptr.To(metav1.Now())
		pclq.Finalizers = []string{"grove.io/test"}
	}
	return pclq
}

// pclqWithUpdatedScheduledReplicas builds a standalone PodClique reporting the given new-hash scheduled
// Pod count on its in-progress UpdateProgress.
func pclqWithUpdatedScheduledReplicas(updatedReady int32) grovecorev1alpha1.PodClique {
	return grovecorev1alpha1.PodClique{
		Status: grovecorev1alpha1.PodCliqueStatus{
			UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{UpdatedScheduledReplicas: updatedReady},
		},
	}
}

// pcsgReplicaInfosWith builds a single-component pcsgReplicaInfos map for the "decode" PodCliqueScalingGroup.
// oldStates are the states of not-yet-rolled replica indices and currentHashStates the states of
// already-rolled (current-hash) indices, assigned ascending indices.
func pcsgReplicaInfosWith(oldStates, currentHashStates []componentutils.PCSGReplicaState) map[string][]pcsgReplicaInfo {
	infos := make([]pcsgReplicaInfo, 0, len(oldStates)+len(currentHashStates))
	replicaIndex := 0
	for _, state := range oldStates {
		infos = append(infos, pcsgReplicaInfo{index: replicaIndex, state: state, atCurrentHash: false})
		replicaIndex++
	}
	for _, state := range currentHashStates {
		infos = append(infos, pcsgReplicaInfo{index: replicaIndex, state: state, atCurrentHash: true})
		replicaIndex++
	}
	return map[string][]pcsgReplicaInfo{"decode": infos}
}

// repeatPCSGState returns a slice of count copies of state.
func repeatPCSGState(state componentutils.PCSGReplicaState, count int) []componentutils.PCSGReplicaState {
	states := make([]componentutils.PCSGReplicaState, count)
	for i := range states {
		states[i] = state
	}
	return states
}

// podGangAtEpoch builds a PodGang owned by the test PCS replica 0 and stamped with the given epoch, marked
// scheduled when scheduled is true.
func podGangAtEpoch(name string, scheduled bool) *groveschedulerv1alpha1.PodGang {
	builder := testutils.NewPodGangBuilder(name, coherentTestNamespace).
		WithLabels(map[string]string{
			apicommon.LabelPartOfKey:                coherentTestPCSName,
			apicommon.LabelPodCliqueSetReplicaIndex: "0",
			apicommon.LabelEpoch:                    "200",
		})
	if scheduled {
		builder = builder.WithLastScheduled()
	}
	return builder.Build()
}

// newCoherentTestSnapshot builds a syncSnapshot for a single standalone component frontend under a coherent
// update, with the given live replicas and MinAvailable. maxUnavailable sets the component's
// RollingUpdate.MaxUnavailable, or leaves it unset so the Coherent default of MinAvailable applies. It sets
// a Ready and UpdatedReady count equal to liveReplicas so the subsumed-Pods and budget gates pass unless a
// case overrides them.
func newCoherentTestSnapshot(pcsNameReplica apicommon.ResourceNameReplica, liveReplicas, minAvailable int32, maxUnavailable *int32) *syncSnapshot {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSName, Namespace: coherentTestNamespace},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Replicas: 1,
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{
						Name:          "frontend",
						RollingUpdate: &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: maxUnavailable},
						Spec: grovecorev1alpha1.PodCliqueSpec{
							Replicas:     liveReplicas,
							MinAvailable: ptr.To(minAvailable),
						},
					},
				},
			},
		},
		Status: grovecorev1alpha1.PodCliqueSetStatus{CurrentGenerationHash: ptr.To(coherentTestCurrentGen)},
	}
	frontendPCLQ := grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: apicommon.GeneratePodCliqueName(pcsNameReplica, "frontend")},
		Spec:       grovecorev1alpha1.PodCliqueSpec{Replicas: liveReplicas},
		Status: grovecorev1alpha1.PodCliqueStatus{
			ReadyReplicas:  liveReplicas,
			UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{UpdatedScheduledReplicas: liveReplicas},
		},
	}
	return &syncSnapshot{
		pcs:                              pcs,
		mvuTemplate:                      &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": minAvailable}},
		existingStandalonePCLQsByReplica: map[int][]grovecorev1alpha1.PodClique{0: {frontendPCLQ}},
	}
}

// pcsgReplicaCondition is the observable condition of a PodCliqueScalingGroup replica. newCoherentPCSGTest
// synthesizes the replica's member PodClique to realize it, and gatherPCSGReplicaInfos re-derives the state
// from it, so that derivation is part of what the test exercises.
type pcsgReplicaCondition int

const (
	replicaReady       pcsgReplicaCondition = iota // member at or above MinAvailable ready
	replicaUnavailable                             // member scheduled but below MinAvailable ready
	replicaPending                                 // member present but below MinAvailable scheduled
	replicaTerminating                             // member present but terminating
)

// memberPCLQ builds the member PodClique that realizes the replica condition at the given replica index.
func (c pcsgReplicaCondition) memberPCLQ(replicaIndex int32) *grovecorev1alpha1.PodClique {
	name := fmt.Sprintf("%s-%d-m", coherentTestPCSGObjName, replicaIndex)
	switch c {
	case replicaReady:
		return pcsgMemberPCLQ(name, replicaIndex, 1, 1, false)
	case replicaUnavailable:
		return pcsgMemberPCLQ(name, replicaIndex, 1, 0, false)
	case replicaPending:
		return pcsgMemberPCLQ(name, replicaIndex, 0, 0, false)
	default: // replicaTerminating
		return pcsgMemberPCLQ(name, replicaIndex, 0, 0, true)
	}
}

// newCoherentPCSGTest builds the _resource (fake client), syncSnapshot, and PodGangMap for a coherent update
// of a single in-scope PodCliqueScalingGroup "sga" over one member clique "m". conditionByReplicaIndex gives
// each present replica's condition; a replica index omitted from the map has no member PodClique at all
// (temporarily absent). scheduledAnchorEpochs lists anchor epochs whose PodGang is already scheduled, to
// drive the emit gate.
func newCoherentPCSGTest(replicas, minAvailable int32, maxUnavailable *int32, conditionByReplicaIndex map[int32]pcsgReplicaCondition, entries []grovecorev1alpha1.PodGangEntry, scheduledAnchorEpochs ...string) (_resource, *syncSnapshot, *grovecorev1alpha1.PodGangMap) {
	pcsNameReplica := apicommon.ResourceNameReplica{Name: coherentTestPCSName, Replica: 0}
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSName, Namespace: coherentTestNamespace},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Replicas: 1,
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "m", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, MinAvailable: ptr.To(int32(1))}},
				},
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{Name: "sga", CliqueNames: []string{"m"}, Replicas: ptr.To(replicas), MinAvailable: ptr.To(minAvailable), RollingUpdate: &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: maxUnavailable}},
				},
			},
		},
		Status: grovecorev1alpha1.PodCliqueSetStatus{CurrentGenerationHash: ptr.To(coherentTestCurrentGen)},
	}
	pcsg := grovecorev1alpha1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSGObjName, Namespace: coherentTestNamespace},
		Spec:       grovecorev1alpha1.PodCliqueScalingGroupSpec{Replicas: replicas, CliqueNames: []string{"m"}},
	}
	objs := make([]client.Object, 0, len(conditionByReplicaIndex)+len(scheduledAnchorEpochs))
	for replicaIndex, condition := range conditionByReplicaIndex {
		objs = append(objs, condition.memberPCLQ(replicaIndex))
	}
	for _, epoch := range scheduledAnchorEpochs {
		objs = append(objs, podGangAtEpoch(apicommon.GenerateAnchorPodGangName(pcsNameReplica, epoch), true))
	}
	r := _resource{
		client: testutils.NewTestClientBuilder().WithPodControllerUIDIndex().WithObjects(objs...).Build(),
		clk:    clocktesting.NewFakeClock(metav1.Now().Time),
	}
	snap := &syncSnapshot{
		logger:                 logr.Discard(),
		pcs:                    pcs,
		existingPCSGsByReplica: map[int][]grovecorev1alpha1.PodCliqueScalingGroup{0: {pcsg}},
		mvuTemplate:            &mvuTemplate{pcsgs: map[string]int32{"sga": minAvailable}},
	}
	pgm := &grovecorev1alpha1.PodGangMap{Spec: grovecorev1alpha1.PodGangMapSpec{Entries: entries}}
	return r, snap, pgm
}

// TestBuildCoherentUpdateEntriesClaimsAbsentPCSGReplicaIntoWholeAnchor drives the whole pipeline for a
// PodCliqueScalingGroup at Replicas=2, MinAvailable=2 whose old-generation anchor committed both replicas
// [0,1], with replica 1's member temporarily absent. The new anchor must carry the complete MinAvailable
// composition [0,1], claiming the absent replica as Pending and recreating it at the new generation, rather
// than opening a smaller anchor that later splits replica 1 into a separate Tail entry.
func TestBuildCoherentUpdateEntriesClaimsAbsentPCSGReplicaIntoWholeAnchor(t *testing.T) {
	oldAnchor := grovecorev1alpha1.PodGangEntry{
		Epoch: "50", PodCliqueSetGenerationHash: coherentTestOldGen,
		Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PCSGReplicaIndices: map[string][]int32{"sga": {0, 1}},
	}
	r, snap, pgm := newCoherentPCSGTest(2, 2, ptr.To[int32](2),
		map[int32]pcsgReplicaCondition{0: replicaReady}, // replica 1 omitted => absent
		[]grovecorev1alpha1.PodGangEntry{oldAnchor})

	entries, err := r.buildCoherentUpdateEntries(t.Context(), snap, 0, pgm)
	require.NoError(t, err)

	require.Len(t, entries, 1)
	assert.Equal(t, grovecorev1alpha1.PodGangEntryRoleAnchor, entries[0].Role)
	assert.ElementsMatch(t, []int32{0, 1}, entries[0].PCSGReplicaIndices["sga"])
}

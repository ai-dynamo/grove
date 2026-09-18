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
	"testing"
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/clock"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
)

func TestComputeNumAnchorBearingSteps(t *testing.T) {
	testCases := []struct {
		description  string
		liveReplicas map[string]int32
		mvu          *mvuTemplate
		want         int32
	}{
		{
			description:  "standalone-only MVU uses a single anchor regardless of replicas",
			liveReplicas: map[string]int32{"frontend": 100},
			mvu:          &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 1}},
			want:         1,
		},
		{
			description:  "single PCSG uses floor(replicas/MinAvailable)",
			liveReplicas: map[string]int32{"decode": 20},
			mvu:          &mvuTemplate{pcsgs: map[string]int32{"decode": 3}},
			want:         6, // floor(20/3)
		},
		{
			description:  "multiple components use the minimum floor across them",
			liveReplicas: map[string]int32{"frontend": 10, "decode": 9},
			mvu:          &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}, pcsgs: map[string]int32{"decode": 3}},
			want:         3, // min(floor(10/2), floor(9/3)) = min(5, 3)
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			assert.Equal(t, tc.want, computeNumAnchorBearingSteps(tc.liveReplicas, tc.mvu))
		})
	}
}

func TestComputeStepPlan(t *testing.T) {
	testCases := []struct {
		description        string
		liveReplicas       map[string]int32
		mvu                *mvuTemplate
		wantNumAnchorSteps int32
		wantStepTarget     map[string]int32
		wantLeftover       map[string]int32
	}{
		{
			description:        "single PCSG rolls MinAvailable per step",
			liveReplicas:       map[string]int32{"prefill": 3},
			mvu:                &mvuTemplate{pcsgs: map[string]int32{"prefill": 3}},
			wantNumAnchorSteps: 1, // floor(3/3)
			wantStepTarget:     map[string]int32{"prefill": 3},
			wantLeftover:       map[string]int32{"prefill": 0},
		},
		{
			description:        "replicas an exact multiple of MinAvailable leave nothing over",
			liveReplicas:       map[string]int32{"prefill": 10},
			mvu:                &mvuTemplate{pcsgs: map[string]int32{"prefill": 2}},
			wantNumAnchorSteps: 5,
			wantStepTarget:     map[string]int32{"prefill": 2},
			wantLeftover:       map[string]int32{"prefill": 0},
		},
		{
			description:        "the tail spreads over the steps and the indivisible remainder becomes leftover",
			liveReplicas:       map[string]int32{"prefill": 13},
			mvu:                &mvuTemplate{pcsgs: map[string]int32{"prefill": 5}},
			wantNumAnchorSteps: 2,
			wantStepTarget:     map[string]int32{"prefill": 6}, // 5 + floor((13-2*5)/2)
			wantLeftover:       map[string]int32{"prefill": 1}, // 13 - 2*6
		},
		{
			description:        "each component sizes its own tail and leftover, step count is the min across them",
			liveReplicas:       map[string]int32{"frontend": 10, "decode": 9},
			mvu:                &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}, pcsgs: map[string]int32{"decode": 3}},
			wantNumAnchorSteps: 3,                                            // min(floor(10/2), floor(9/3))
			wantStepTarget:     map[string]int32{"frontend": 3, "decode": 3}, // frontend 2+floor((10-6)/3)=3, decode 3+0
			wantLeftover:       map[string]int32{"frontend": 1, "decode": 0}, // frontend 10-3*3, decode 9-3*3
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			plan := computeStepPlan(tc.liveReplicas, tc.mvu)
			assert.Equal(t, tc.wantNumAnchorSteps, plan.numAnchorBearingSteps)
			assert.Equal(t, tc.wantStepTarget, plan.anchorBearingStepTarget)
			assert.Equal(t, tc.wantLeftover, plan.leftover)
		})
	}
}

func TestCountReplicasAtCurrentHash(t *testing.T) {
	p := &subStepPlanner{
		pcs: pcsWithCurrentHash("v2"),
		mvu: &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}, pcsgs: map[string]int32{"decode": 3}},
		entries: []grovecorev1alpha1.PodGangEntry{
			{Epoch: "50", PodCliqueSetGenerationHash: "v1", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 5}},
			{Epoch: "100", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 3}, PCSGReplicaIndices: map[string][]int32{"decode": {0, 1, 2}}},
			{Epoch: "200", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 2}},
		},
	}
	// The old-hash anchor is ignored. Standalone PodClique "frontend" sums its pod counts (3+2), PCSG "decode" counts
	// its replica indices (3).
	assert.Equal(t, map[string]int32{"frontend": 5, "decode": 3}, p.countReplicasAtCurrentHash())
}

func TestMostRecentAnchorEpoch(t *testing.T) {
	t.Run("returns the epoch of the highest-epoch current-hash anchor", func(t *testing.T) {
		p := &subStepPlanner{
			pcs: pcsWithCurrentHash("v2"),
			entries: []grovecorev1alpha1.PodGangEntry{
				{Epoch: "100", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleAnchor},
				{Epoch: "200", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleAnchor},
				{Epoch: "300", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleTail},
				{Epoch: "400", PodCliqueSetGenerationHash: "v1", Role: grovecorev1alpha1.PodGangEntryRoleAnchor},
			},
		}
		// The v2 tail is not an anchor and the v1 anchor is a different hash despite its higher epoch,
		// so both are ignored.
		epoch, err := p.mostRecentAnchorEpoch()
		require.NoError(t, err)
		assert.Equal(t, "200", epoch)
	})
	t.Run("returns empty when there is no current-hash anchor", func(t *testing.T) {
		p := &subStepPlanner{
			pcs: pcsWithCurrentHash("v2"),
			entries: []grovecorev1alpha1.PodGangEntry{
				{Epoch: "400", PodCliqueSetGenerationHash: "v1", Role: grovecorev1alpha1.PodGangEntryRoleAnchor},
			},
		}
		epoch, err := p.mostRecentAnchorEpoch()
		require.NoError(t, err)
		assert.Equal(t, "", epoch)
	})
}

func TestAscertainPlanPosition(t *testing.T) {
	p := &subStepPlanner{
		pcs:          pcsWithCurrentHash("v2"),
		mvu:          &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}, pcsgs: map[string]int32{"decode": 3}},
		liveReplicas: map[string]int32{"frontend": 10, "decode": 9},
		plan:         computeStepPlan(map[string]int32{"frontend": 10, "decode": 9}, &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}, pcsgs: map[string]int32{"decode": 3}}),
		entries: []grovecorev1alpha1.PodGangEntry{
			{Epoch: "100", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 3}, PCSGReplicaIndices: map[string][]int32{"decode": {0, 1, 2}}},
			{Epoch: "200", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 2}},
		},
	}
	// Step plan is numAnchorBearingSteps 3, target {frontend:3, decode:3}, leftover {frontend:1, decode:0}. The
	// committed entries carry frontend:5 and decode:3, so step 0 is fully committed and the open step 1 holds
	// frontend:2, decode:0, no leftover yet.
	pos, err := p.ascertainPlanPosition()
	require.NoError(t, err)
	assert.Equal(t, map[string]int32{"frontend": 5, "decode": 3}, pos.currentHashCountByComponent)
	assert.Equal(t, int32(1), pos.anchorBearingStepsDone)
	assert.Equal(t, map[string]int32{"frontend": 2, "decode": 0}, pos.currentAnchorStepCountByComponent)
	assert.Equal(t, map[string]int32{"frontend": 0, "decode": 0}, pos.leftoverCountByComponent)
	assert.Equal(t, "200", pos.mostRecentAnchorEpoch)
}

// -----------------------------------------------------------------------------
// Coherent step-plan and sub-step planner tests.
//
// computeStepPlan derives the plan for one PCS replica from each in-scope component's live child
// Spec.Replicas (liveReplicas), MinAvailable, and MaxUnavailable. All division is integer (floor):
//
//	numAnchorBearingSteps      = min over components c of floor(liveReplicas[c] / minAvailable[c])
//	tailPerStep[c]             = floor((liveReplicas[c] - numAnchorBearingSteps*minAvailable[c]) / numAnchorBearingSteps)
//	anchorBearingStepTarget[c] = minAvailable[c] + tailPerStep[c]
//	leftover[c]                = liveReplicas[c] - numAnchorBearingSteps*anchorBearingStepTarget[c]
//
// Each test states its per-component scenario and the values these formulas yield.
// -----------------------------------------------------------------------------

func TestOpenAnchorStepHasTailRemaining(t *testing.T) {
	// One standalone PodClique (frontend) and one PCSG (decode). liveReplicas is each component's live child
	// Spec.Replicas.
	//
	//	component  liveReplicas  minAvailable  maxUnavailable  kind
	//	frontend   12            2             3               standalone PodClique
	//	decode     4             2             2               PCSG
	//
	//	numAnchorBearingSteps   = min(floor(12/2), floor(4/2)) = min(6, 2) = 2
	//	anchorBearingStepTarget = {frontend: 2 + floor((12-2*2)/2) = 6, decode: 2 + floor((4-2*2)/2) = 2}
	//	leftover                = {frontend: 0, decode: 0}
	//
	// Within a step the anchor commits MinAvailable {frontend: 2, decode: 2}. decode's target equals its
	// MinAvailable, so it is then done. frontend's tail is 4, over MaxUnavailable 3, so it rolls 2 -> 5 -> 6.
	// So the reachable committed counts while a step is open are frontend {0, 2, 5} and decode {0, 2}.
	planner := newTestPlanner(nil, "v2", nil, map[string]testComponent{
		"frontend": {liveReplicas: 12, minAvailable: 2, maxUnavailable: 3, standalone: true},
		"decode":   {liveReplicas: 4, minAvailable: 2, maxUnavailable: 2},
	})
	testCases := []struct {
		description string
		current     map[string]int32
		want        bool
	}{
		{"no anchor-bearing step is open, nothing committed yet", map[string]int32{"frontend": 0, "decode": 0}, false},
		{"anchor just committed, decode at its target with frontend's tail remaining", map[string]int32{"frontend": 2, "decode": 2}, true},
		{"frontend partway through its tail, decode already at its target", map[string]int32{"frontend": 5, "decode": 2}, true},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			assert.Equal(t, tc.want, planner.openAnchorStepHasTailRemaining(planPosition{currentAnchorStepCountByComponent: tc.current}))
		})
	}
}

func TestAnyLeftoverRemaining(t *testing.T) {
	// component  liveReplicas  minAvailable  maxUnavailable  kind
	// frontend   9             2             2               standalone PodClique
	// decode     22            3             4               PCSG
	//
	//	numAnchorBearingSteps   = min(floor(9/2), floor(22/3)) = min(4, 7) = 4
	//	anchorBearingStepTarget = {frontend: 2+floor((9-4*2)/4)=2, decode: 3+floor((22-4*3)/4)=5}
	//	leftover                = {frontend: 9-4*2=1, decode: 22-4*5=2}
	// anyLeftoverRemaining runs once all anchor-bearing steps are committed, so each component is at least at its
	// anchor-phase total (frontend 8, decode 20) and the leftover step drains the rest up to liveReplicas.
	planner := newTestPlanner(nil, "v2", nil, map[string]testComponent{
		"frontend": {liveReplicas: 9, minAvailable: 2, maxUnavailable: 2, standalone: true},
		"decode":   {liveReplicas: 22, minAvailable: 3, maxUnavailable: 4},
	})
	testCases := []struct {
		description      string
		currentHashCount map[string]int32
		want             bool
	}{
		{"leftover not yet rolled for either component", map[string]int32{"frontend": 8, "decode": 20}, true},
		{"frontend leftover done, decode leftover still remaining", map[string]int32{"frontend": 9, "decode": 21}, true},
		{"every component at its full live replica count", map[string]int32{"frontend": 9, "decode": 22}, false},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			assert.Equal(t, tc.want, planner.anyLeftoverRemaining(tc.currentHashCount))
		})
	}
}

func TestBuildNonAnchorSubStep(t *testing.T) {
	// buildNonAnchorSubStep depends only on component kind, MaxUnavailable, and the DependsOn entries, not on the
	// step plan, so liveReplicas and MinAvailable are unused here.
	//
	// component  liveReplicas  minAvailable  maxUnavailable  kind
	// frontend   10            2             5               standalone PodClique
	// decode     20            3             3               PCSG
	entries := []grovecorev1alpha1.PodGangEntry{{Epoch: "100", PodCliqueSetGenerationHash: "v2"}}
	planner := newTestPlanner(testingclock.NewFakeClock(time.Unix(0, 12345)), "v2", entries, map[string]testComponent{
		"frontend": {liveReplicas: 10, minAvailable: 2, maxUnavailable: 5, standalone: true},
		"decode":   {liveReplicas: 20, minAvailable: 3, maxUnavailable: 3},
	})
	indexStartFn := func(_ string, _ int32) int32 { return 2 }

	t.Run("clamps a PCSG to MaxUnavailable and subsumes standalone pods", func(t *testing.T) {
		ss, err := planner.buildNonAnchorSubStep(newEpoch(planner.clk), "100", map[string]int32{"frontend": 2, "decode": 4}, indexStartFn)
		require.NoError(t, err)
		require.NotNil(t, ss)
		assert.Equal(t, "12345", ss.epoch)
		assert.Equal(t, []string{"100"}, ss.dependsOn)
		assert.Equal(t, "100", ss.subsumeAnchorEpoch)
		assert.Equal(t, map[string][]int32{"decode": {2, 3, 4}}, ss.tailPCSGReplicaIndices) // min(3,4)=3 from index 2
		assert.Equal(t, map[string][]int32{"decode": {2, 3, 4}}, ss.drainPCSGReplicaIndices)
		assert.Equal(t, map[string]int32{"frontend": 2}, ss.subsumeStandalonePCLQCounts) // min(5,2)=2
		assert.Equal(t, map[string]int32{"frontend": 2}, ss.drainStandalonePCLQCounts)
	})
	t.Run("returns nil when nothing remains", func(t *testing.T) {
		ss, err := planner.buildNonAnchorSubStep(newEpoch(planner.clk), "100", map[string]int32{}, indexStartFn)
		require.NoError(t, err)
		assert.Nil(t, ss)
	})
}

func TestBuildAnchorBearingSubStep(t *testing.T) {
	// component  liveReplicas  minAvailable  maxUnavailable  kind
	// frontend   10            2             2               standalone PodClique
	// prefill    10            3             3               PCSG
	// decode     20            3             4               PCSG
	//
	//	numAnchorBearingSteps   = min(floor(10/2), floor(10/3), floor(20/3)) = min(5, 3, 6) = 3
	//	anchorBearingStepTarget = {frontend: 3, prefill: 3, decode: 6}
	//
	// Opening step k=1 claims each PCSG's MinAvailable indices from its block [k*target, k*target+minAvailable):
	// prefill [3,6)->[3,4,5], decode [6,9)->[6,7,8]. frontend is standalone, drained by its MinAvailable count 2.
	entries := []grovecorev1alpha1.PodGangEntry{{Epoch: "100", PodCliqueSetGenerationHash: "v2"}}
	planner := newTestPlanner(testingclock.NewFakeClock(time.Unix(0, 999)), "v2", entries, map[string]testComponent{
		"frontend": {liveReplicas: 10, minAvailable: 2, maxUnavailable: 2, standalone: true},
		"prefill":  {liveReplicas: 10, minAvailable: 3, maxUnavailable: 3},
		"decode":   {liveReplicas: 20, minAvailable: 3, maxUnavailable: 4},
	})
	ss, err := planner.buildAnchorBearingSubStep(planPosition{anchorBearingStepsDone: 1})
	require.NoError(t, err)
	assert.Equal(t, "999", ss.epoch)
	assert.Equal(t, []string{"100"}, ss.dependsOn)
	assert.True(t, ss.opensAnchor)
	assert.Equal(t, map[string][]int32{"prefill": {3, 4, 5}, "decode": {6, 7, 8}}, ss.anchorPCSGReplicaIndices)
	assert.Equal(t, map[string][]int32{"prefill": {3, 4, 5}, "decode": {6, 7, 8}}, ss.drainPCSGReplicaIndices)
	assert.Equal(t, map[string]int32{"frontend": 2}, ss.drainStandalonePCLQCounts)
}

func TestBuildTailSubStep(t *testing.T) {
	// component  liveReplicas  minAvailable  maxUnavailable  kind
	// frontend   8             2             2               standalone PodClique
	// prefill    4             2             2               PCSG
	// decode     12            2             3               PCSG
	//
	//	numAnchorBearingSteps   = min(floor(8/2), floor(4/2), floor(12/2)) = min(4, 2, 6) = 2
	//	anchorBearingStepTarget = {frontend: 2+floor((8-4)/2)=4, prefill: 2, decode: 2+floor((12-4)/2)=6}
	//
	// In step k=0 the anchor already committed MinAvailable {frontend:2, prefill:2, decode:2}. This tail sub-step
	// rolls each component's remaining toward its target: frontend 2 (subsumed, within MaxUnavailable 2), prefill 0
	// (done), decode min(MaxUnavailable 3, 4)=3 starting at (0+1)*6-4=2 -> [2,3,4].
	entries := []grovecorev1alpha1.PodGangEntry{{Epoch: "100", PodCliqueSetGenerationHash: "v2"}}
	planner := newTestPlanner(testingclock.NewFakeClock(time.Unix(0, 7)), "v2", entries, map[string]testComponent{
		"frontend": {liveReplicas: 8, minAvailable: 2, maxUnavailable: 2, standalone: true},
		"prefill":  {liveReplicas: 4, minAvailable: 2, maxUnavailable: 2},
		"decode":   {liveReplicas: 12, minAvailable: 2, maxUnavailable: 3},
	})
	ss, err := planner.buildTailSubStep(planPosition{anchorBearingStepsDone: 0, currentAnchorStepCountByComponent: map[string]int32{"frontend": 2, "prefill": 2, "decode": 2}})
	require.NoError(t, err)
	assert.Equal(t, map[string][]int32{"decode": {2, 3, 4}}, ss.tailPCSGReplicaIndices)
	assert.Equal(t, map[string]int32{"frontend": 2}, ss.subsumeStandalonePCLQCounts)
}

func TestBuildLeftoverSubStep(t *testing.T) {
	// component  liveReplicas  minAvailable  maxUnavailable  kind
	// frontend   9             2             2               standalone PodClique
	// decode     22            3             4               PCSG
	//
	// This gives leftover {frontend:1, decode:2} (see TestAnyLeftoverRemaining for the derivation). The leftover
	// step rolls each remainder from the end of its range: frontend 1 subsumed (within MaxUnavailable 2), decode
	// min(MaxUnavailable 4, 2)=2 starting at liveReplicas-remaining=22-2=20 -> [20,21].
	entries := []grovecorev1alpha1.PodGangEntry{{Epoch: "100", PodCliqueSetGenerationHash: "v2"}}
	planner := newTestPlanner(testingclock.NewFakeClock(time.Unix(0, 7)), "v2", entries, map[string]testComponent{
		"frontend": {liveReplicas: 9, minAvailable: 2, maxUnavailable: 2, standalone: true},
		"decode":   {liveReplicas: 22, minAvailable: 3, maxUnavailable: 4},
	})
	ss, err := planner.buildLeftoverSubStep(planPosition{leftoverCountByComponent: map[string]int32{"frontend": 0, "decode": 0}})
	require.NoError(t, err)
	assert.Equal(t, map[string][]int32{"decode": {20, 21}}, ss.tailPCSGReplicaIndices)
	assert.Equal(t, map[string]int32{"frontend": 1}, ss.subsumeStandalonePCLQCounts)
}

func TestNext(t *testing.T) {
	// component  liveReplicas  minAvailable  maxUnavailable  kind
	// frontend   10            2             2               standalone PodClique
	// prefill    10            3             3               PCSG
	// decode     20            3             4               PCSG
	//
	//	numAnchorBearingSteps   = 3, anchorBearingStepTarget = {frontend:3, prefill:3, decode:6}
	//	leftover                = {frontend:1, prefill:1, decode:2}, anchor-phase totals = {frontend:9, prefill:9, decode:18}
	newPlanner := func() *subStepPlanner {
		return newTestPlanner(testingclock.NewFakeClock(time.Unix(0, 1)), "v2",
			[]grovecorev1alpha1.PodGangEntry{{Epoch: "100", PodCliqueSetGenerationHash: "v2"}},
			map[string]testComponent{
				"frontend": {liveReplicas: 10, minAvailable: 2, maxUnavailable: 2, standalone: true},
				"prefill":  {liveReplicas: 10, minAvailable: 3, maxUnavailable: 3},
				"decode":   {liveReplicas: 20, minAvailable: 3, maxUnavailable: 4},
			})
	}
	t.Run("an open step with tail returns a tail sub-step", func(t *testing.T) {
		// Anchor of step 0 just committed MinAvailable, so frontend and decode still have tail.
		ss, err := newPlanner().next(planPosition{anchorBearingStepsDone: 0, currentAnchorStepCountByComponent: map[string]int32{"frontend": 2, "prefill": 3, "decode": 3}})
		require.NoError(t, err)
		require.NotNil(t, ss)
		assert.False(t, ss.opensAnchor)
		assert.NotEmpty(t, ss.tailPCSGReplicaIndices)
	})
	t.Run("no open step opens the next anchor-bearing step", func(t *testing.T) {
		ss, err := newPlanner().next(planPosition{anchorBearingStepsDone: 0, currentAnchorStepCountByComponent: map[string]int32{"frontend": 0, "prefill": 0, "decode": 0}})
		require.NoError(t, err)
		require.NotNil(t, ss)
		assert.True(t, ss.opensAnchor)
	})
	t.Run("all anchor-bearing steps committed rolls leftover", func(t *testing.T) {
		ss, err := newPlanner().next(planPosition{anchorBearingStepsDone: 3, currentHashCountByComponent: map[string]int32{"frontend": 9, "prefill": 9, "decode": 18}, leftoverCountByComponent: map[string]int32{"frontend": 0, "prefill": 0, "decode": 0}})
		require.NoError(t, err)
		require.NotNil(t, ss)
		assert.NotEmpty(t, ss.tailPCSGReplicaIndices)
	})
	t.Run("fully committed returns nil", func(t *testing.T) {
		ss, err := newPlanner().next(planPosition{anchorBearingStepsDone: 3, currentHashCountByComponent: map[string]int32{"frontend": 10, "prefill": 10, "decode": 20}, leftoverCountByComponent: map[string]int32{"frontend": 1, "prefill": 1, "decode": 2}})
		require.NoError(t, err)
		assert.Nil(t, ss)
	})
}

func TestNextForStandaloneOnlyMVU(t *testing.T) {
	// A standalone-only MVU with two PodCliques.
	//
	// component  liveReplicas  minAvailable  maxUnavailable
	// frontend   10            2             3
	// router     6             1             2
	//
	// No PodCliqueScalingGroup is in scope, so numAnchorBearingSteps is 1. The single anchor holds MinAvailable
	// of each PodClique {frontend:2, router:1}, and the remaining pods (8 frontend, 5 router) subsume into that
	// same anchor, each at most its own MaxUnavailable per sub-step.
	newPlanner := func() *subStepPlanner {
		return newTestPlanner(testingclock.NewFakeClock(time.Unix(0, 1)), "v2",
			[]grovecorev1alpha1.PodGangEntry{{Epoch: "100", PodCliqueSetGenerationHash: "v2"}},
			map[string]testComponent{
				"frontend": {liveReplicas: 10, minAvailable: 2, maxUnavailable: 3, standalone: true},
				"router":   {liveReplicas: 6, minAvailable: 1, maxUnavailable: 2, standalone: true},
			})
	}
	require.Equal(t, int32(1), newPlanner().plan.numAnchorBearingSteps)

	testCases := []struct {
		description string
		position    planPosition
		want        *subStep
	}{
		{
			description: "nothing committed opens the single anchor with each PodClique's MinAvailable",
			position:    planPosition{currentAnchorStepCountByComponent: map[string]int32{"frontend": 0, "router": 0}},
			want: &subStep{
				epoch:                     "1",
				dependsOn:                 []string{"100"},
				opensAnchor:               true,
				anchorPCSGReplicaIndices:  map[string][]int32{},
				drainStandalonePCLQCounts: map[string]int32{"frontend": 2, "router": 1},
				drainPCSGReplicaIndices:   map[string][]int32{},
			},
		},
		{
			description: "with the anchor committed (2 frontend, 1 router), each subsumes its MaxUnavailable, 3 frontend and 2 router",
			position:    planPosition{currentAnchorStepCountByComponent: map[string]int32{"frontend": 2, "router": 1}, mostRecentAnchorEpoch: "100"},
			want: &subStep{
				epoch:                       "1",
				dependsOn:                   []string{"100"},
				subsumeAnchorEpoch:          "100",
				subsumeStandalonePCLQCounts: map[string]int32{"frontend": 3, "router": 2},
				drainStandalonePCLQCounts:   map[string]int32{"frontend": 3, "router": 2},
				tailPCSGReplicaIndices:      map[string][]int32{},
				drainPCSGReplicaIndices:     map[string][]int32{},
			},
		},
		{
			description: "near the end (8 frontend, 5 router committed), fewer than MaxUnavailable remain, so each subsumes what is left, 2 frontend and 1 router",
			position:    planPosition{currentAnchorStepCountByComponent: map[string]int32{"frontend": 8, "router": 5}, mostRecentAnchorEpoch: "100"},
			want: &subStep{
				epoch:                       "1",
				dependsOn:                   []string{"100"},
				subsumeAnchorEpoch:          "100",
				subsumeStandalonePCLQCounts: map[string]int32{"frontend": 2, "router": 1},
				drainStandalonePCLQCounts:   map[string]int32{"frontend": 2, "router": 1},
				tailPCSGReplicaIndices:      map[string][]int32{},
				drainPCSGReplicaIndices:     map[string][]int32{},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			ss, err := newPlanner().next(tc.position)
			require.NoError(t, err)
			assert.Equal(t, tc.want, ss)
		})
	}
}

func TestNextForPCSGOnlyMVU(t *testing.T) {
	// A PCSG-only MVU with two PodCliqueScalingGroups.
	//
	// component  liveReplicas  minAvailable  maxUnavailable
	// prefill    10            3             3
	// decode     20            3             4
	//
	// With PodCliqueScalingGroups in scope the usual computation applies: numAnchorBearingSteps =
	// min(floor(10/3), floor(20/3)) = 3, target {prefill:3, decode:6}, leftover {prefill:1, decode:2}. Each
	// anchor-bearing step creates an anchor carrying MinAvailable PCSG indices, everything above rolls as tail
	// PodGangs at most MaxUnavailable at a time, and nothing subsumes since there is no standalone PodClique.
	newPlanner := func() *subStepPlanner {
		return newTestPlanner(testingclock.NewFakeClock(time.Unix(0, 1)), "v2",
			[]grovecorev1alpha1.PodGangEntry{{Epoch: "100", PodCliqueSetGenerationHash: "v2"}},
			map[string]testComponent{
				"prefill": {liveReplicas: 10, minAvailable: 3, maxUnavailable: 3},
				"decode":  {liveReplicas: 20, minAvailable: 3, maxUnavailable: 4},
			})
	}
	require.Equal(t, int32(3), newPlanner().plan.numAnchorBearingSteps)

	testCases := []struct {
		description string
		position    planPosition
		want        *subStep
	}{
		{
			description: "opening step 0 creates an anchor with each PCSG's MinAvailable indices",
			position:    planPosition{anchorBearingStepsDone: 0, currentAnchorStepCountByComponent: map[string]int32{"prefill": 0, "decode": 0}},
			want: &subStep{
				epoch:                     "1",
				dependsOn:                 []string{"100"},
				opensAnchor:               true,
				anchorPCSGReplicaIndices:  map[string][]int32{"prefill": {0, 1, 2}, "decode": {0, 1, 2}},
				drainStandalonePCLQCounts: map[string]int32{},
				drainPCSGReplicaIndices:   map[string][]int32{"prefill": {0, 1, 2}, "decode": {0, 1, 2}},
			},
		},
		{
			description: "the open step's decode tail rolls as PCSG PodGangs from index 3, prefill already at target",
			position:    planPosition{anchorBearingStepsDone: 0, currentAnchorStepCountByComponent: map[string]int32{"prefill": 3, "decode": 3}, mostRecentAnchorEpoch: "100"},
			want: &subStep{
				epoch:                       "1",
				dependsOn:                   []string{"100"},
				subsumeAnchorEpoch:          "100",
				subsumeStandalonePCLQCounts: map[string]int32{},
				drainStandalonePCLQCounts:   map[string]int32{},
				tailPCSGReplicaIndices:      map[string][]int32{"decode": {3, 4, 5}},
				drainPCSGReplicaIndices:     map[string][]int32{"decode": {3, 4, 5}},
			},
		},
		{
			description: "after all anchor-bearing steps, leftover PCSG replicas roll as tail PodGangs",
			position:    planPosition{anchorBearingStepsDone: 3, currentHashCountByComponent: map[string]int32{"prefill": 9, "decode": 18}, leftoverCountByComponent: map[string]int32{"prefill": 0, "decode": 0}, mostRecentAnchorEpoch: "100"},
			want: &subStep{
				epoch:                       "1",
				dependsOn:                   []string{"100"},
				subsumeAnchorEpoch:          "100",
				subsumeStandalonePCLQCounts: map[string]int32{},
				drainStandalonePCLQCounts:   map[string]int32{},
				tailPCSGReplicaIndices:      map[string][]int32{"prefill": {9}, "decode": {18, 19}},
				drainPCSGReplicaIndices:     map[string][]int32{"prefill": {9}, "decode": {18, 19}},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			ss, err := newPlanner().next(tc.position)
			require.NoError(t, err)
			assert.Equal(t, tc.want, ss)
		})
	}
}

func TestDependsOnLatestEpoch(t *testing.T) {
	// dependsOnLatestEpoch depends only on the entries and the current generation hash, so no component scenario
	// is needed. It returns the single latest current-hash epoch, or nil when no current-hash entry exists yet.
	t.Run("returns the latest current-hash epoch", func(t *testing.T) {
		planner := newTestPlanner(nil, "v2", []grovecorev1alpha1.PodGangEntry{
			{Epoch: "100", PodCliqueSetGenerationHash: "v2"},
			{Epoch: "200", PodCliqueSetGenerationHash: "v2"},
			{Epoch: "300", PodCliqueSetGenerationHash: "v1"},
		}, nil)
		dependsOn, err := planner.dependsOnLatestEpoch()
		require.NoError(t, err)
		assert.Equal(t, []string{"200"}, dependsOn)
	})
	t.Run("returns nil when no current-hash entry exists", func(t *testing.T) {
		planner := newTestPlanner(nil, "v2", []grovecorev1alpha1.PodGangEntry{{Epoch: "300", PodCliqueSetGenerationHash: "v1"}}, nil)
		dependsOn, err := planner.dependsOnLatestEpoch()
		require.NoError(t, err)
		assert.Nil(t, dependsOn)
	})
}

func pcsWithCurrentHash(currentGenerationHash string) *grovecorev1alpha1.PodCliqueSet {
	pcs := &grovecorev1alpha1.PodCliqueSet{}
	pcs.Status.CurrentGenerationHash = ptr.To(currentGenerationHash)
	return pcs
}

type testComponent struct {
	liveReplicas   int32
	minAvailable   int32
	maxUnavailable int32
	standalone     bool // standalone PodClique when true, PodCliqueScalingGroup when false
}

// newTestPlanner builds a subStepPlanner from an explicit per-component scenario, deriving the step plan
// with computeStepPlan so the plan is always consistent with the stated replicas and MinAvailable.
//
//nolint:unparam // currentHash is a genuine generation dimension; current tests only exercise "v2".
func newTestPlanner(clk clock.Clock, currentHash string, entries []grovecorev1alpha1.PodGangEntry, components map[string]testComponent) *subStepPlanner {
	var (
		standalonePCLQs           = map[string]int32{}
		pcsgs                     = map[string]int32{}
		liveReplicas              = map[string]int32{}
		maxUnavailableByComponent = map[string]int32{}
	)
	for name, c := range components {
		if c.standalone {
			standalonePCLQs[name] = c.minAvailable
		} else {
			pcsgs[name] = c.minAvailable
		}
		liveReplicas[name] = c.liveReplicas
		maxUnavailableByComponent[name] = c.maxUnavailable
	}
	mvu := &mvuTemplate{standalonePCLQs: standalonePCLQs, pcsgs: pcsgs}
	return &subStepPlanner{
		clk:                       clk,
		pcs:                       pcsWithCurrentHash(currentHash),
		entries:                   entries,
		mvu:                       mvu,
		liveReplicas:              liveReplicas,
		maxUnavailableByComponent: maxUnavailableByComponent,
		plan:                      computeStepPlan(liveReplicas, mvu),
	}
}

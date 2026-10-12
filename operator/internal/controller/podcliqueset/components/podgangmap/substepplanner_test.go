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
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

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
			description:  "single PCSG uses the integer division replicas/MinAvailable",
			liveReplicas: map[string]int32{"decode": 20},
			mvu:          &mvuTemplate{pcsgs: map[string]int32{"decode": 3}},
			want:         6, // 20/3 rounded down
		},
		{
			description:  "multiple components use the minimum step count across them",
			liveReplicas: map[string]int32{"frontend": 10, "decode": 9},
			mvu:          &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}, pcsgs: map[string]int32{"decode": 3}},
			want:         3, // min(⌊10/2⌋, ⌊9/3⌋) = min(5, 3)
		},
		{
			description:  "a PodCliqueScalingGroup scaled to zero does not bound the step count and a single anchor carries the active standalone",
			liveReplicas: map[string]int32{"frontend": 10, "decode": 0},
			mvu:          &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}, pcsgs: map[string]int32{"decode": 3}},
			want:         1,
		},
		{
			description:  "a zero-replica PodCliqueScalingGroup alongside an active one uses the active one",
			liveReplicas: map[string]int32{"frontend": 10, "decodeA": 9, "decodeB": 0},
			mvu:          &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}, pcsgs: map[string]int32{"decodeA": 3, "decodeB": 3}},
			want:         3, // min(⌊9/3⌋, ⌊10/2⌋) with decodeB skipped
		},
		{
			description:  "all components scaled to zero roll nothing",
			liveReplicas: map[string]int32{"frontend": 0, "decode": 0},
			mvu:          &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}, pcsgs: map[string]int32{"decode": 3}},
			want:         0,
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
			wantNumAnchorSteps: 1, // ⌊3/3⌋
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
			wantStepTarget:     map[string]int32{"prefill": 6}, // 5 + ⌊(13-2*5)/2⌋
			wantLeftover:       map[string]int32{"prefill": 1}, // 13 - 2*6
		},
		{
			description:        "each component sizes its own tail and leftover, step count is the min across them",
			liveReplicas:       map[string]int32{"frontend": 10, "decode": 9},
			mvu:                &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}, pcsgs: map[string]int32{"decode": 3}},
			wantNumAnchorSteps: 3,                                            // min(⌊10/2⌋, ⌊9/3⌋)
			wantStepTarget:     map[string]int32{"frontend": 3, "decode": 3}, // frontend 2+⌊(10-6)/3⌋=3, decode 3+0
			wantLeftover:       map[string]int32{"frontend": 1, "decode": 0}, // frontend 10-3*3, decode 9-3*3
		},
		{
			description:        "a zero-replica PodCliqueScalingGroup takes no anchor or tail and a single anchor carries the active standalone",
			liveReplicas:       map[string]int32{"frontend": 2, "decode": 0},
			mvu:                &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 1}, pcsgs: map[string]int32{"decode": 3}},
			wantNumAnchorSteps: 1,
			wantStepTarget:     map[string]int32{"frontend": 2},
			wantLeftover:       map[string]int32{"frontend": 0},
		},
		{
			description:        "all components scaled to zero produce an empty plan",
			liveReplicas:       map[string]int32{"frontend": 0, "decode": 0},
			mvu:                &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 1}, pcsgs: map[string]int32{"decode": 3}},
			wantNumAnchorSteps: 0,
			wantStepTarget:     map[string]int32{},
			wantLeftover:       map[string]int32{},
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
		pcs:             pcsWithCurrentHash("v2"),
		mvu:             &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}, pcsgs: map[string]int32{"decode": 3}},
		desiredReplicas: map[string]int32{"frontend": 10, "decode": 9},
		plan:            computeStepPlan(map[string]int32{"frontend": 10, "decode": 9}, &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}, pcsgs: map[string]int32{"decode": 3}}),
		entries: []grovecorev1alpha1.PodGangEntry{
			{Epoch: "100", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 3}, PCSGReplicaIndices: map[string][]int32{"decode": {0, 1, 2}}},
			{Epoch: "200", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 2}},
		},
	}
	// Step plan is numAnchorBearingSteps 3, target {frontend:3, decode:3}, leftover {frontend:1, decode:0}. The
	// committed entries carry frontend:5 and decode:3, so step 0 is fully committed and the open step 1 holds
	// frontend:2, decode:0, no leftover yet.
	planPos, err := p.ascertainPlanPosition()
	require.NoError(t, err)
	assert.Equal(t, map[string]int32{"frontend": 5, "decode": 3}, planPos.currentHashCountByComponent)
	assert.Equal(t, int32(1), planPos.anchorBearingStepsDone)
	assert.Equal(t, map[string]int32{"frontend": 2, "decode": 0}, planPos.currentAnchorStepCountByComponent)
	assert.Equal(t, map[string]int32{"frontend": 0, "decode": 0}, planPos.leftoverCountByComponent)
	assert.Equal(t, "200", planPos.mostRecentAnchorEpoch)
}

func TestAscertainPlanPositionSkipsZeroTargetComponent(t *testing.T) {
	// decode is in scope but scaled to zero, so its anchor-bearing step target is zero. The position
	// computation must skip it rather than divide its committed count by a zero target.
	mvu := &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 1}, pcsgs: map[string]int32{"decode": 1}}
	desiredReplicas := map[string]int32{"frontend": 2, "decode": 0}
	p := &subStepPlanner{
		pcs:             pcsWithCurrentHash("v2"),
		mvu:             mvu,
		desiredReplicas: desiredReplicas,
		plan:            computeStepPlan(desiredReplicas, mvu),
		entries: []grovecorev1alpha1.PodGangEntry{
			{Epoch: "100", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 1}},
		},
	}
	require.Zero(t, p.plan.anchorBearingStepTarget["decode"])
	planPos, err := p.ascertainPlanPosition()
	require.NoError(t, err)
	assert.Equal(t, map[string]int32{"frontend": 1}, planPos.currentHashCountByComponent)
	assert.Equal(t, int32(0), planPos.anchorBearingStepsDone)
}

// -----------------------------------------------------------------------------
// Coherent step-plan and sub-step planner tests.
//
// computeStepPlan derives the plan for one PCS replica from each in-scope component's live child
// Spec.Replicas (liveReplicas), MinAvailable, and MaxUnavailable. All division is integer division:
//
//	numAnchorBearingSteps      = min over components c of ⌊liveReplicas[c] / minAvailable[c]⌋
//	tailPerStep[c]             = ⌊(liveReplicas[c] - numAnchorBearingSteps*minAvailable[c]) / numAnchorBearingSteps⌋
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
	//	numAnchorBearingSteps   = min(⌊12/2⌋, ⌊4/2⌋) = min(6, 2) = 2
	//	anchorBearingStepTarget = {frontend: 2 + ⌊(12-2*2)/2⌋ = 6, decode: 2 + ⌊(4-2*2)/2⌋ = 2}
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
	//	numAnchorBearingSteps   = min(⌊9/2⌋, ⌊22/3⌋) = min(4, 7) = 4
	//	anchorBearingStepTarget = {frontend: 2+⌊(9-4*2)/4⌋=2, decode: 3+⌊(22-4*3)/4⌋=5}
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
	// buildNonAnchorSubStep rolls each component's remaining toward its target, a PCSG by taking its worst-off
	// still-unrolled indices and a standalone PodClique by subsuming pods, each capped by MaxUnavailable.
	//
	// component  liveReplicas  minAvailable  maxUnavailable  kind
	// frontend   10            2             5               standalone PodClique
	// decode     20            3             3               PCSG
	//
	// decode indices 0 and 1 are already committed to the current hash, so its old indices start at 2.
	entries := []grovecorev1alpha1.PodGangEntry{{Epoch: "100", PodCliqueSetGenerationHash: "v2", PCSGReplicaIndices: map[string][]int32{"decode": {0, 1}}}}
	planner := newTestPlanner(testingclock.NewFakeClock(time.Unix(0, 12345)), "v2", entries, map[string]testComponent{
		"frontend": {liveReplicas: 10, minAvailable: 2, maxUnavailable: 5, standalone: true},
		"decode":   {liveReplicas: 20, minAvailable: 3, maxUnavailable: 3},
	})

	t.Run("clamps a PCSG to MaxUnavailable and subsumes standalone pods", func(t *testing.T) {
		ss, err := planner.buildNonAnchorSubStep(newEpoch(planner.clk), "100", map[string]int32{"frontend": 2, "decode": 4}, nil)
		require.NoError(t, err)
		require.NotNil(t, ss)
		assert.Equal(t, "12345", ss.epoch)
		assert.Equal(t, []string{"100"}, ss.dependsOn)
		assert.Equal(t, "100", ss.subsumeAnchorEpoch)
		assert.Equal(t, map[string][]int32{"decode": {2, 3, 4}}, ss.tailPCSGReplicaIndices) // min(3,4)=3 worst-off old indices, starting at 2
		assert.Equal(t, map[string][]int32{"decode": {2, 3, 4}}, ss.drainPCSGReplicaIndices)
		assert.Equal(t, map[string]int32{"frontend": 2}, ss.subsumeStandalonePCLQCounts) // min(5,2)=2
		assert.Equal(t, map[string]int32{"frontend": 2}, ss.drainStandalonePCLQCounts)
	})
	t.Run("returns nil when nothing remains", func(t *testing.T) {
		ss, err := planner.buildNonAnchorSubStep(newEpoch(planner.clk), "100", map[string]int32{}, nil)
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
	//	numAnchorBearingSteps   = min(⌊10/2⌋, ⌊10/3⌋, ⌊20/3⌋) = min(5, 3, 6) = 3
	//	anchorBearingStepTarget = {frontend: 3, prefill: 3, decode: 6}
	//
	// Opening step k=1: indices [0,3) of prefill and [0,6) of decode are already committed to the current
	// hash, so the worst-off (here all Ready, so ascending) MinAvailable old indices are prefill [3,4,5] and
	// decode [6,7,8]. frontend is standalone, drained by its MinAvailable count 2.
	entries := []grovecorev1alpha1.PodGangEntry{{Epoch: "100", PodCliqueSetGenerationHash: "v2", PCSGReplicaIndices: map[string][]int32{"prefill": {0, 1, 2}, "decode": {0, 1, 2, 3, 4, 5}}}}
	planner := newTestPlanner(testingclock.NewFakeClock(time.Unix(0, 999)), "v2", entries, map[string]testComponent{
		"frontend": {liveReplicas: 10, minAvailable: 2, maxUnavailable: 2, standalone: true},
		"prefill":  {liveReplicas: 10, minAvailable: 3, maxUnavailable: 3},
		"decode":   {liveReplicas: 20, minAvailable: 3, maxUnavailable: 4},
	})
	ss, err := planner.buildAnchorBearingSubStep()
	require.NoError(t, err)
	assert.Equal(t, "999", ss.epoch)
	assert.Equal(t, []string{"100"}, ss.dependsOn)
	assert.True(t, ss.opensAnchor)
	assert.Equal(t, map[string][]int32{"prefill": {3, 4, 5}, "decode": {6, 7, 8}}, ss.anchorPCSGReplicaIndices)
	assert.Equal(t, map[string][]int32{"prefill": {3, 4, 5}, "decode": {6, 7, 8}}, ss.drainPCSGReplicaIndices)
	assert.Equal(t, map[string]int32{"frontend": 2}, ss.drainStandalonePCLQCounts)
}

func TestBuildAnchorBearingSubStepPicksWorstOffFirst(t *testing.T) {
	// decode has 3 old replicas, index 1 unavailable and 0 and 2 Ready. The anchor rolls MinAvailable 1, so it
	// must pick the unavailable index 1 before either Ready one.
	planner := newTestPlanner(testingclock.NewFakeClock(time.Unix(0, 5)), "v2",
		[]grovecorev1alpha1.PodGangEntry{{Epoch: "100", PodCliqueSetGenerationHash: "v2"}},
		map[string]testComponent{"decode": {liveReplicas: 3, minAvailable: 1, maxUnavailable: 1}})
	planner.pcsgReplicaInfos["decode"] = []pcsgReplicaInfo{
		{index: 0, state: componentutils.PCSGReplicaStateReady},
		{index: 1, state: componentutils.PCSGReplicaStateUnavailable},
		{index: 2, state: componentutils.PCSGReplicaStateReady},
	}
	ss, err := planner.buildAnchorBearingSubStep()
	require.NoError(t, err)
	assert.Equal(t, map[string][]int32{"decode": {1}}, ss.anchorPCSGReplicaIndices)
	assert.Equal(t, map[string][]int32{"decode": {1}}, ss.drainPCSGReplicaIndices)
}

// TestBuildAnchorBearingSubStepRequeuesWhenPCSGShortOfMinAvailable verifies that when the step plan expects
// an anchor-bearing step but the PodCliqueScalingGroup can no longer supply MinAvailable old replica indices,
// buildAnchorBearingSubStep requeues to recompute the plan rather than opening a short anchor.
func TestBuildAnchorBearingSubStepRequeuesWhenPCSGShortOfMinAvailable(t *testing.T) {
	planner := newTestPlanner(testingclock.NewFakeClock(time.Unix(0, 5)), "v2",
		[]grovecorev1alpha1.PodGangEntry{{Epoch: "100", PodCliqueSetGenerationHash: "v2"}},
		map[string]testComponent{"decode": {liveReplicas: 3, minAvailable: 2, maxUnavailable: 1}})
	// Opening the anchor needs MinAvailable 2 old replicas, but only one remains old.
	planner.pcsgReplicaInfos["decode"] = []pcsgReplicaInfo{
		{index: 0, state: componentutils.PCSGReplicaStateReady, atCurrentHash: true},
		{index: 1, state: componentutils.PCSGReplicaStateReady, atCurrentHash: true},
		{index: 2, state: componentutils.PCSGReplicaStateReady, atCurrentHash: false},
	}
	_, err := planner.buildAnchorBearingSubStep()
	require.Error(t, err)
}

// TestNextOldPCSGIndicesToRoll covers the health-ordered selection of a PodCliqueScalingGroup's old replica
// indices: current-hash indices are excluded, old indices are ordered pending then unavailable then ready
// (ascending by index within each state), and the result is capped to the requested count.
func TestNextOldPCSGIndicesToRoll(t *testing.T) {
	const pcsgName = "decode"
	old := func(index int, state componentutils.PCSGReplicaState) pcsgReplicaInfo {
		return pcsgReplicaInfo{index: index, state: state, atCurrentHash: false}
	}
	current := func(index int, state componentutils.PCSGReplicaState) pcsgReplicaInfo {
		return pcsgReplicaInfo{index: index, state: state, atCurrentHash: true}
	}
	testCases := []struct {
		description string
		infos       []pcsgReplicaInfo
		count       int32
		want        []int32
	}{
		{
			description: "no replicas yields no indices",
			infos:       []pcsgReplicaInfo{},
			count:       3,
			want:        []int32{},
		},
		{
			description: "all replicas already at the current hash yields no old indices",
			infos:       []pcsgReplicaInfo{current(0, componentutils.PCSGReplicaStateReady), current(1, componentutils.PCSGReplicaStateUnavailable)},
			count:       3,
			want:        []int32{},
		},
		{
			description: "worst-off first across states, ascending index within a state",
			infos: []pcsgReplicaInfo{
				old(0, componentutils.PCSGReplicaStateReady), old(1, componentutils.PCSGReplicaStatePending), old(2, componentutils.PCSGReplicaStateUnavailable),
				old(3, componentutils.PCSGReplicaStatePending), old(4, componentutils.PCSGReplicaStateReady), old(5, componentutils.PCSGReplicaStateUnavailable),
			},
			count: 6,
			want:  []int32{1, 3, 2, 5, 0, 4},
		},
		{
			description: "a count smaller than the number of old indices returns only the worst-off ones",
			infos:       []pcsgReplicaInfo{old(0, componentutils.PCSGReplicaStateReady), old(1, componentutils.PCSGReplicaStateUnavailable), old(2, componentutils.PCSGReplicaStatePending)},
			count:       1,
			want:        []int32{2},
		},
		{
			description: "count beyond the available old indices returns them all",
			infos:       []pcsgReplicaInfo{old(0, componentutils.PCSGReplicaStateReady), old(2, componentutils.PCSGReplicaStateUnavailable)},
			count:       5,
			want:        []int32{2, 0},
		},
		{
			description: "current-hash replicas are excluded regardless of state",
			infos:       []pcsgReplicaInfo{current(0, componentutils.PCSGReplicaStatePending), old(1, componentutils.PCSGReplicaStateReady), current(2, componentutils.PCSGReplicaStateUnavailable)},
			count:       3,
			want:        []int32{1},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			p := &subStepPlanner{pcsgReplicaInfos: map[string][]pcsgReplicaInfo{pcsgName: tc.infos}}
			assert.Equal(t, tc.want, p.nextOldPCSGIndicesToRoll(pcsgName, tc.count))
		})
	}
}

func TestBuildTailSubStep(t *testing.T) {
	// component  liveReplicas  minAvailable  maxUnavailable  kind
	// frontend   8             2             2               standalone PodClique
	// prefill    4             2             2               PCSG
	// decode     12            2             3               PCSG
	//
	//	numAnchorBearingSteps   = min(⌊8/2⌋, ⌊4/2⌋, ⌊12/2⌋) = min(4, 2, 6) = 2
	//	anchorBearingStepTarget = {frontend: 2+⌊(8-4)/2⌋=4, prefill: 2, decode: 2+⌊(12-4)/2⌋=6}
	//
	// In step k=0 the anchor already committed MinAvailable {frontend:2, prefill:2, decode:2}, so decode's
	// indices [0,2) are current-hash. This tail sub-step rolls each component's remaining toward its target:
	// frontend 2 (subsumed, within MaxUnavailable 2), prefill 0 (done), decode min(MaxUnavailable 3, 4)=3
	// worst-off old indices (all Ready, so ascending from 2) -> [2,3,4].
	entries := []grovecorev1alpha1.PodGangEntry{currentHashEntryWithCommittedPCSGIndices(map[string]int32{"prefill": 2, "decode": 2})}
	planner := newTestPlanner(testingclock.NewFakeClock(time.Unix(0, 7)), "v2", entries, map[string]testComponent{
		"frontend": {liveReplicas: 8, minAvailable: 2, maxUnavailable: 2, standalone: true},
		"prefill":  {liveReplicas: 4, minAvailable: 2, maxUnavailable: 2},
		"decode":   {liveReplicas: 12, minAvailable: 2, maxUnavailable: 3},
	})
	ss, err := planner.buildTailSubStep(planPosition{anchorBearingStepsDone: 0, currentAnchorStepCountByComponent: map[string]int32{"frontend": 2, "prefill": 2, "decode": 2}}, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string][]int32{"decode": {2, 3, 4}}, ss.tailPCSGReplicaIndices)
	assert.Equal(t, map[string]int32{"frontend": 2}, ss.subsumeStandalonePCLQCounts)
}

func TestBuildLeftoverSubStep(t *testing.T) {
	// component  liveReplicas  minAvailable  maxUnavailable  kind
	// frontend   9             2             2               standalone PodClique
	// decode     22            3             4               PCSG
	//
	// This gives leftover {frontend:1, decode:2} (see TestAnyLeftoverRemaining for the derivation). All
	// anchor-phase steps have committed decode's indices [0,20), so the leftover step rolls decode
	// min(MaxUnavailable 4, 2)=2 worst-off old indices (all Ready, so [20,21]) and subsumes frontend 1.
	entries := []grovecorev1alpha1.PodGangEntry{currentHashEntryWithCommittedPCSGIndices(map[string]int32{"decode": 20})}
	planner := newTestPlanner(testingclock.NewFakeClock(time.Unix(0, 7)), "v2", entries, map[string]testComponent{
		"frontend": {liveReplicas: 9, minAvailable: 2, maxUnavailable: 2, standalone: true},
		"decode":   {liveReplicas: 22, minAvailable: 3, maxUnavailable: 4},
	})
	ss, err := planner.buildLeftoverSubStep(planPosition{leftoverCountByComponent: map[string]int32{"frontend": 0, "decode": 0}}, nil)
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
		ss, err := newPlanner().next(planPosition{anchorBearingStepsDone: 0, currentAnchorStepCountByComponent: map[string]int32{"frontend": 2, "prefill": 3, "decode": 3}}, nil)
		require.NoError(t, err)
		require.NotNil(t, ss)
		assert.False(t, ss.opensAnchor)
		assert.NotEmpty(t, ss.tailPCSGReplicaIndices)
	})
	t.Run("no open step opens the next anchor-bearing step", func(t *testing.T) {
		ss, err := newPlanner().next(planPosition{anchorBearingStepsDone: 0, currentAnchorStepCountByComponent: map[string]int32{"frontend": 0, "prefill": 0, "decode": 0}}, nil)
		require.NoError(t, err)
		require.NotNil(t, ss)
		assert.True(t, ss.opensAnchor)
	})
	t.Run("all anchor-bearing steps committed rolls leftover", func(t *testing.T) {
		ss, err := newPlanner().next(planPosition{anchorBearingStepsDone: 3, currentHashCountByComponent: map[string]int32{"frontend": 9, "prefill": 9, "decode": 18}, leftoverCountByComponent: map[string]int32{"frontend": 0, "prefill": 0, "decode": 0}}, nil)
		require.NoError(t, err)
		require.NotNil(t, ss)
		assert.NotEmpty(t, ss.tailPCSGReplicaIndices)
	})
	t.Run("fully committed returns nil", func(t *testing.T) {
		ss, err := newPlanner().next(planPosition{anchorBearingStepsDone: 3, currentHashCountByComponent: map[string]int32{"frontend": 10, "prefill": 10, "decode": 20}, leftoverCountByComponent: map[string]int32{"frontend": 1, "prefill": 1, "decode": 2}}, nil)
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
			ss, err := newPlanner().next(tc.position, nil)
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
	// min(⌊10/3⌋, ⌊20/3⌋) = 3, target {prefill:3, decode:6}, leftover {prefill:1, decode:2}. Each
	// anchor-bearing step creates an anchor carrying MinAvailable PCSG indices, everything above rolls as tail
	// PodGangs at most MaxUnavailable at a time, and nothing subsumes since there is no standalone PodClique.
	newPlanner := func(committed map[string]int32) *subStepPlanner {
		return newTestPlanner(testingclock.NewFakeClock(time.Unix(0, 1)), "v2",
			[]grovecorev1alpha1.PodGangEntry{currentHashEntryWithCommittedPCSGIndices(committed)},
			map[string]testComponent{
				"prefill": {liveReplicas: 10, minAvailable: 3, maxUnavailable: 3},
				"decode":  {liveReplicas: 20, minAvailable: 3, maxUnavailable: 4},
			})
	}
	require.Equal(t, int32(3), newPlanner(nil).plan.numAnchorBearingSteps)

	testCases := []struct {
		description string
		committed   map[string]int32
		position    planPosition
		want        *subStep
	}{
		{
			description: "opening step 0 creates an anchor with each PCSG's MinAvailable indices",
			committed:   nil,
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
			committed:   map[string]int32{"prefill": 3, "decode": 3},
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
			committed:   map[string]int32{"prefill": 9, "decode": 18},
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
			ss, err := newPlanner(tc.committed).next(tc.position, nil)
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

func TestBuildNonAnchorSubStepCapsByHeadroom(t *testing.T) {
	// frontend standalone MaxUnavailable 5, decode PCSG MaxUnavailable 3. Remaining 4/4 would roll 4/3, but
	// headroom 1/2 caps them to 1/2. decode indices [0,2) are already committed, so its old indices start at 2.
	entries := []grovecorev1alpha1.PodGangEntry{currentHashEntryWithCommittedPCSGIndices(map[string]int32{"decode": 2})}
	planner := newTestPlanner(testingclock.NewFakeClock(time.Unix(0, 12345)), "v2", entries, map[string]testComponent{
		"frontend": {liveReplicas: 10, minAvailable: 2, maxUnavailable: 5, standalone: true},
		"decode":   {liveReplicas: 20, minAvailable: 3, maxUnavailable: 3},
	})

	ss, err := planner.buildNonAnchorSubStep(newEpoch(planner.clk), "100", map[string]int32{"frontend": 4, "decode": 4}, map[string]int32{"frontend": 1, "decode": 2})

	require.NoError(t, err)
	require.NotNil(t, ss)
	assert.Equal(t, map[string]int32{"frontend": 1}, ss.subsumeStandalonePCLQCounts)
	assert.Equal(t, map[string]int32{"frontend": 1}, ss.drainStandalonePCLQCounts)
	assert.Equal(t, map[string][]int32{"decode": {2, 3}}, ss.tailPCSGReplicaIndices)
	assert.Equal(t, map[string][]int32{"decode": {2, 3}}, ss.drainPCSGReplicaIndices)
}

func TestBuildNonAnchorSubStepHeadroomZeroDrainsNothing(t *testing.T) {
	// With zero headroom on every component the sub-step rolls nothing this reconcile.
	entries := []grovecorev1alpha1.PodGangEntry{{Epoch: "100", PodCliqueSetGenerationHash: "v2"}}
	planner := newTestPlanner(testingclock.NewFakeClock(time.Unix(0, 12345)), "v2", entries, map[string]testComponent{
		"frontend": {liveReplicas: 10, minAvailable: 2, maxUnavailable: 5, standalone: true},
		"decode":   {liveReplicas: 20, minAvailable: 3, maxUnavailable: 3},
	})

	ss, err := planner.buildNonAnchorSubStep(newEpoch(planner.clk), "100", map[string]int32{"frontend": 4, "decode": 4}, map[string]int32{"frontend": 0, "decode": 0})

	require.NoError(t, err)
	require.NotNil(t, ss)
	assert.True(t, ss.drainsNothing())
}

func TestSubStepDrainCountAndDrainsNothing(t *testing.T) {
	ss := subStep{
		drainStandalonePCLQCounts: map[string]int32{"frontend": 2},
		drainPCSGReplicaIndices:   map[string][]int32{"decode": {0, 1, 2}},
	}
	assert.Equal(t, map[string]int32{"frontend": 2, "decode": 3}, ss.drainCountByComponent())
	assert.False(t, ss.drainsNothing())
	assert.True(t, subStep{}.drainsNothing())
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

// currentHashEntryWithCommittedPCSGIndices builds a current-hash entry committing indices [0, count) of each
// listed PodCliqueScalingGroup, the already-rolled set a scenario starts from. newTestPlanner derives
// pcsgReplicaInfos from it, marking those indices atCurrentHash and the rest old.
func currentHashEntryWithCommittedPCSGIndices(committedCountByPCSG map[string]int32) grovecorev1alpha1.PodGangEntry {
	indicesByPCSG := make(map[string][]int32, len(committedCountByPCSG))
	for name, count := range committedCountByPCSG {
		indices := make([]int32, count)
		for i := range indices {
			indices[i] = int32(i)
		}
		indicesByPCSG[name] = indices
	}
	return grovecorev1alpha1.PodGangEntry{Epoch: "100", PodCliqueSetGenerationHash: "v2", PCSGReplicaIndices: indicesByPCSG}
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
	// Derive pcsgReplicaInfos from the committed current-hash entries exactly as the engine does, so the
	// health-ordered index selection exercises the real atCurrentHash derivation. Each PCSG's
	// current-hash-committed indices are atCurrentHash and the remaining desired indices are old. Every
	// replica defaults to Ready, a test needing other states sets pcsgReplicaInfos on the returned planner.
	pcsgReplicaInfos := make(map[string][]pcsgReplicaInfo, len(pcsgs))
	for name := range pcsgs {
		committedIndices := currentHashCommittedPCSGReplicaIndices(entries, name, currentHash)
		infos := make([]pcsgReplicaInfo, 0, liveReplicas[name])
		for replicaIndex := 0; replicaIndex < int(liveReplicas[name]); replicaIndex++ {
			infos = append(infos, pcsgReplicaInfo{index: replicaIndex, state: componentutils.PCSGReplicaStateReady, atCurrentHash: committedIndices.Has(replicaIndex)})
		}
		pcsgReplicaInfos[name] = infos
	}
	return &subStepPlanner{
		clk:                       clk,
		pcs:                       pcsWithCurrentHash(currentHash),
		entries:                   entries,
		mvu:                       mvu,
		desiredReplicas:           liveReplicas,
		maxUnavailableByComponent: maxUnavailableByComponent,
		pcsgReplicaInfos:          pcsgReplicaInfos,
		plan:                      computeStepPlan(liveReplicas, mvu),
	}
}

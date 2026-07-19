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

package podgangmap

import (
	"context"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
)

func TestAscertainUpdateProgress(t *testing.T) {
	// One PCSG "prefill": replicas 8, minAvailable 2, so numAnchorBearingSteps=4, target=2, leftover=0.
	// A separate shape with leftover is used where noted.
	tests := []struct {
		name                      string
		replicas                  map[string]int32
		minAvailable              map[string]int32
		standalone                map[string]int32 // mvu standalone minAvailable (for standalone components)
		entries                   []grovecorev1alpha1.PodGangEntry
		wantRolled                map[string]int32
		wantStepsDone             int32
		wantCurrentStepRolled     map[string]int32
		wantLeftoverRolled        map[string]int32
		wantMostRecentAnchorEpoch string
		wantErr                   bool
	}{
		{
			name:                      "nothing rolled",
			replicas:                  map[string]int32{"prefill": 8},
			minAvailable:              map[string]int32{"prefill": 2},
			entries:                   nil,
			wantRolled:                map[string]int32{},
			wantStepsDone:             0,
			wantCurrentStepRolled:     map[string]int32{"prefill": 0},
			wantLeftoverRolled:        map[string]int32{},
			wantMostRecentAnchorEpoch: "",
		},
		{
			name:         "one anchor-bearing step fully rolled (target==minAvailable)",
			replicas:     map[string]int32{"prefill": 8},
			minAvailable: map[string]int32{"prefill": 2},
			entries: []grovecorev1alpha1.PodGangEntry{
				anchorEntryAt("100", map[string][]int32{"prefill": {0, 1}}),
			},
			wantRolled:                map[string]int32{"prefill": 2},
			wantStepsDone:             1,
			wantCurrentStepRolled:     map[string]int32{"prefill": 0},
			wantLeftoverRolled:        map[string]int32{},
			wantMostRecentAnchorEpoch: "100",
		},
		{
			name:         "anchor-bearing step partially rolled (target 5, floor 2)",
			replicas:     map[string]int32{"prefill": 10, "decode": 2}, // steps=min(5,2)=2; prefill target 5, decode target 1
			minAvailable: map[string]int32{"prefill": 2, "decode": 1},
			entries: []grovecorev1alpha1.PodGangEntry{
				anchorEntryAt("100", map[string][]int32{"prefill": {0, 1}, "decode": {0}}),
			},
			wantRolled:                map[string]int32{"prefill": 2, "decode": 1},
			wantStepsDone:             0, // prefill floor 2 < target 5, so step 0 not complete
			wantCurrentStepRolled:     map[string]int32{"prefill": 2, "decode": 1},
			wantLeftoverRolled:        map[string]int32{},
			wantMostRecentAnchorEpoch: "100",
		},
		{
			name:         "uneven: step done for one component but not the other => min governs",
			replicas:     map[string]int32{"prefill": 10, "decode": 2}, // prefill target 5, decode target 1, steps 2
			minAvailable: map[string]int32{"prefill": 2, "decode": 1},
			entries: []grovecorev1alpha1.PodGangEntry{
				anchorEntryAt("100", map[string][]int32{"prefill": {0, 1, 2, 3, 4}, "decode": {0}}), // prefill hit target 5, decode at 1
			},
			wantRolled:                map[string]int32{"prefill": 5, "decode": 1},
			wantStepsDone:             1, // decode 1/1=1, prefill 5/5=1 => min 1
			wantCurrentStepRolled:     map[string]int32{"prefill": 0, "decode": 0},
			wantLeftoverRolled:        map[string]int32{},
			wantMostRecentAnchorEpoch: "100",
		},
		{
			name:         "all anchor-bearing steps done, leftover partially rolled",
			replicas:     map[string]int32{"prefill": 10}, // minAvail 3 => steps 3, target 3, leftover 1
			minAvailable: map[string]int32{"prefill": 3},
			entries: []grovecorev1alpha1.PodGangEntry{
				anchorEntryAt("100", map[string][]int32{"prefill": {0, 1, 2}}),
				anchorEntryAt("200", map[string][]int32{"prefill": {3, 4, 5}}),
				anchorEntryAt("300", map[string][]int32{"prefill": {6, 7, 8}}),
			},
			wantRolled:                map[string]int32{"prefill": 9},
			wantStepsDone:             3,
			wantCurrentStepRolled:     map[string]int32{"prefill": 0},
			wantLeftoverRolled:        map[string]int32{}, // index 9 not yet rolled
			wantMostRecentAnchorEpoch: "300",
		},
		{
			name:         "old-hash entries are ignored, most recent current-hash anchor reported",
			replicas:     map[string]int32{"prefill": 8},
			minAvailable: map[string]int32{"prefill": 2},
			entries: []grovecorev1alpha1.PodGangEntry{
				oldHashAnchorEntryAt("old-hash", "50", map[string][]int32{"prefill": {0, 1, 2, 3}}),
				anchorEntryAt("100", map[string][]int32{"prefill": {0, 1}}),
			},
			wantRolled:                map[string]int32{"prefill": 2},
			wantStepsDone:             1,
			wantCurrentStepRolled:     map[string]int32{"prefill": 0},
			wantLeftoverRolled:        map[string]int32{},
			wantMostRecentAnchorEpoch: "100",
		},
		{
			name:         "malformed epoch on an anchor entry yields error",
			replicas:     map[string]int32{"prefill": 8},
			minAvailable: map[string]int32{"prefill": 2},
			entries: []grovecorev1alpha1.PodGangEntry{
				anchorEntryAt("not-a-number", map[string][]int32{"prefill": {0, 1}}),
			},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			planner := plannerForReplicasMinAvail(t, tc.replicas, tc.minAvailable, tc.replicas /* maxUnavail unused here */)
			if tc.standalone != nil {
				planner.mvu.standalonePCLQs = tc.standalone
			}
			planner.entries = tc.entries

			prog, err := planner.ascertainUpdateProgress()
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantRolled, prog.rolledCounts, "rolledCounts")
			assert.Equal(t, tc.wantStepsDone, prog.anchorBearingStepsDone, "anchorBearingStepsDone")
			assert.Equal(t, tc.wantCurrentStepRolled, prog.currentAnchorBearingStepRolled, "currentAnchorBearingStepRolled")
			assert.Equal(t, tc.wantLeftoverRolled, prog.leftoverRolled, "leftoverRolled")
			assert.Equal(t, tc.wantMostRecentAnchorEpoch, prog.mostRecentAnchorEpoch, "mostRecentAnchorEpoch")
		})
	}
}

func TestAllComponentsFullyRolled(t *testing.T) {
	replicas := map[string]int32{"frontend": 5, "prefill": 3}
	tests := []struct {
		name         string
		rolledCounts map[string]int32
		want         bool
	}{
		{name: "true when every component matches replicas", rolledCounts: map[string]int32{"frontend": 5, "prefill": 3}, want: true},
		{name: "false on any gap", rolledCounts: map[string]int32{"frontend": 5, "prefill": 2}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, allComponentsFullyRolled(tc.rolledCounts, replicas))
		})
	}
}

func TestCurrentAnchorBearingStepInProgress(t *testing.T) {
	// prefill: replicas 10, minAvailable 2 alone => steps 5. Pair with decode replicas 2 minAvail 1 =>
	// steps=min(5,2)=2; prefill target 5, decode target 1.
	planner := plannerForReplicasMinAvail(t,
		map[string]int32{"prefill": 10, "decode": 2},
		map[string]int32{"prefill": 2, "decode": 1},
		map[string]int32{"prefill": 4, "decode": 1})
	tests := []struct {
		name string
		prog updateProgress
		want bool
	}{
		{
			name: "partially rolled current step => in progress",
			prog: updateProgress{anchorBearingStepsDone: 0, currentAnchorBearingStepRolled: map[string]int32{"prefill": 2, "decode": 1}},
			want: true, // prefill 2 in (0,5)
		},
		{
			name: "current step at target => not in progress",
			prog: updateProgress{anchorBearingStepsDone: 0, currentAnchorBearingStepRolled: map[string]int32{"prefill": 5, "decode": 1}},
			want: false,
		},
		{
			name: "all anchor-bearing steps done => not in progress",
			prog: updateProgress{anchorBearingStepsDone: 2, currentAnchorBearingStepRolled: map[string]int32{}},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, planner.currentAnchorBearingStepInProgress(tc.prog))
		})
	}
}

func TestAnyLeftoverRemaining(t *testing.T) {
	planner := plannerForReplicasMinAvail(t,
		map[string]int32{"prefill": 8}, map[string]int32{"prefill": 3}, map[string]int32{"prefill": 3})
	tests := []struct {
		name         string
		rolledCounts map[string]int32
		want         bool
	}{
		{name: "true when a component is short of replicas", rolledCounts: map[string]int32{"prefill": 6}, want: true},
		{name: "false when all rolled", rolledCounts: map[string]int32{"prefill": 8}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, planner.anyLeftoverRemaining(tc.rolledCounts))
		})
	}
}

func TestNewSubStepPlanner(t *testing.T) {
	tests := []struct {
		name         string
		replicas     map[string]int32
		minAvailable map[string]int32
		wantSteps    int32
		wantTarget   map[string]int32
		wantLeftover map[string]int32
	}{
		{
			name:         "worked example single PCSG",
			replicas:     map[string]int32{"prefill": 80},
			minAvailable: map[string]int32{"prefill": 3},
			wantSteps:    26,
			wantTarget:   map[string]int32{"prefill": 3},
			wantLeftover: map[string]int32{"prefill": 2},
		},
		{
			name:         "even division yields zero leftover",
			replicas:     map[string]int32{"prefill": 8},
			minAvailable: map[string]int32{"prefill": 2},
			wantSteps:    4,
			wantTarget:   map[string]int32{"prefill": 2},
			wantLeftover: map[string]int32{"prefill": 0},
		},
		{
			name:         "numAnchorBearingSteps is min over components",
			replicas:     map[string]int32{"prefill": 9, "decode": 4},
			minAvailable: map[string]int32{"prefill": 3, "decode": 2},
			wantSteps:    2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			planner := plannerForReplicasMinAvail(t, tc.replicas, tc.minAvailable, tc.replicas)
			assert.Equal(t, tc.wantSteps, planner.numAnchorBearingSteps)
			for c, want := range tc.wantTarget {
				assert.Equal(t, want, planner.anchorBearingStepTarget[c], "target[%s]", c)
			}
			for c, want := range tc.wantLeftover {
				assert.Equal(t, want, planner.leftover[c], "leftover[%s]", c)
			}
		})
	}
}

func TestNext(t *testing.T) {
	t.Run("current step in progress yields a tail sub-step", func(t *testing.T) {
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 10, "decode": 2},
			map[string]int32{"prefill": 2, "decode": 1},
			map[string]int32{"prefill": 4, "decode": 1})
		prog := updateProgress{
			rolledCounts:                   map[string]int32{"prefill": 2, "decode": 1},
			anchorBearingStepsDone:         0,
			currentAnchorBearingStepRolled: map[string]int32{"prefill": 2, "decode": 1},
			mostRecentAnchorEpoch:          "100",
		}
		ss, err := planner.next(prog)
		require.NoError(t, err)
		require.NotNil(t, ss)
		assert.NotEmpty(t, ss.tailPCSGReplicaIndices["prefill"])
	})

	t.Run("current step complete, anchor steps remain yields anchor-bearing sub-step", func(t *testing.T) {
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 8}, map[string]int32{"prefill": 2}, map[string]int32{"prefill": 2})
		prog := updateProgress{
			rolledCounts:                   map[string]int32{"prefill": 2},
			anchorBearingStepsDone:         1,
			currentAnchorBearingStepRolled: map[string]int32{"prefill": 0},
		}
		ss, err := planner.next(prog)
		require.NoError(t, err)
		require.NotNil(t, ss)
		assert.NotEmpty(t, ss.anchorPCSGReplicaIndices["prefill"])
	})

	t.Run("all anchor steps done, leftover remains yields leftover sub-step", func(t *testing.T) {
		// prefill replicas 10, minAvail 3 => steps 3, target 3, leftover 1.
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 10}, map[string]int32{"prefill": 3}, map[string]int32{"prefill": 2})
		planner.entries = []grovecorev1alpha1.PodGangEntry{
			anchorEntryAt("300", map[string][]int32{"prefill": {6, 7, 8}}),
		}
		prog := updateProgress{
			rolledCounts:           map[string]int32{"prefill": 9},
			anchorBearingStepsDone: 3,
			leftoverRolled:         map[string]int32{},
			mostRecentAnchorEpoch:  "300",
		}
		ss, err := planner.next(prog)
		require.NoError(t, err)
		require.NotNil(t, ss)
		assert.Equal(t, []int32{9}, ss.tailPCSGReplicaIndices["prefill"])
	})

	t.Run("all anchor steps done, no leftover yields nil", func(t *testing.T) {
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 8}, map[string]int32{"prefill": 2}, map[string]int32{"prefill": 2})
		prog := updateProgress{
			rolledCounts:           map[string]int32{"prefill": 8},
			anchorBearingStepsDone: 4,
		}
		ss, err := planner.next(prog)
		require.NoError(t, err)
		assert.Nil(t, ss)
	})
}

func TestBuildAnchorBearingSubStep(t *testing.T) {
	t.Run("first anchor claims floor indices [0, minAvailable)", func(t *testing.T) {
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 8}, map[string]int32{"prefill": 2}, map[string]int32{"prefill": 2})
		planner.clk = clocktesting.NewFakeClock(time.Unix(0, 7000))
		ss, err := planner.buildAnchorBearingSubStep(updateProgress{anchorBearingStepsDone: 0})
		require.NoError(t, err)
		assert.Equal(t, map[string][]int32{"prefill": {0, 1}}, ss.anchorPCSGReplicaIndices)
		assert.Equal(t, map[string][]int32{"prefill": {0, 1}}, ss.drainPCSGReplicaIndices)
		assert.Equal(t, "7000", ss.epoch)
	})

	t.Run("second anchor claims the next contiguous block", func(t *testing.T) {
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 8}, map[string]int32{"prefill": 2}, map[string]int32{"prefill": 2})
		ss, err := planner.buildAnchorBearingSubStep(updateProgress{anchorBearingStepsDone: 1})
		require.NoError(t, err)
		assert.Equal(t, map[string][]int32{"prefill": {2, 3}}, ss.anchorPCSGReplicaIndices)
	})
}

func TestBuildTailSubStep(t *testing.T) {
	// prefill replicas 10 minAvail 2, decode replicas 2 minAvail 1 => steps 2, prefill target 5.
	planner := plannerForReplicasMinAvail(t,
		map[string]int32{"prefill": 10, "decode": 2},
		map[string]int32{"prefill": 2, "decode": 1},
		map[string]int32{"prefill": 4, "decode": 1})
	planner.clk = clocktesting.NewFakeClock(time.Unix(0, 9000))
	prog := updateProgress{
		anchorBearingStepsDone:         0,
		currentAnchorBearingStepRolled: map[string]int32{"prefill": 2, "decode": 1},
		mostRecentAnchorEpoch:          "100",
	}
	ss, err := planner.buildTailSubStep(prog)
	require.NoError(t, err)
	// prefill target 5, rolled 2, remaining 3, rollBudget min(4,3)=3, start (0+1)*5-3=2 => [2,5).
	assert.Equal(t, []int32{2, 3, 4}, ss.tailPCSGReplicaIndices["prefill"])
	assert.Equal(t, "100", ss.subsumeAnchorEpoch)
}

func TestBuildLeftoverSubStep(t *testing.T) {
	// prefill replicas 10, minAvail 3, maxUnavail 2 => steps 3, target 3, leftover 1.
	planner := plannerForReplicasMinAvail(t,
		map[string]int32{"prefill": 10}, map[string]int32{"prefill": 3}, map[string]int32{"prefill": 2})
	planner.clk = clocktesting.NewFakeClock(time.Unix(0, 12000))
	prog := updateProgress{
		leftoverRolled:        map[string]int32{},
		mostRecentAnchorEpoch: "300",
	}
	ss, err := planner.buildLeftoverSubStep(prog)
	require.NoError(t, err)
	// leftover index start = replicas - remaining = 10 - 1 = 9 => [9,10).
	assert.Equal(t, []int32{9}, ss.tailPCSGReplicaIndices["prefill"])
	assert.Equal(t, "300", ss.subsumeAnchorEpoch)
}

func TestEntriesAfterSubStep(t *testing.T) {
	t.Run("anchor-bearing sub-step appends anchor and drains old-hash floor", func(t *testing.T) {
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 8}, map[string]int32{"prefill": 2}, map[string]int32{"prefill": 2})
		planner.mvu.standalonePCLQs = map[string]int32{"frontend": 1}
		planner.entries = []grovecorev1alpha1.PodGangEntry{
			oldHashAnchorEntryWithPods("old-hash", "old-e", map[string]int32{"frontend": 1}, map[string][]int32{"prefill": {0, 1, 2, 3}}),
		}
		ss := subStep{
			epoch:                     "new-e",
			anchorPCSGReplicaIndices:  map[string][]int32{"prefill": {0, 1}},
			drainStandalonePCLQCounts: map[string]int32{"frontend": 1},
			drainPCSGReplicaIndices:   map[string][]int32{"prefill": {0, 1}},
		}
		result := planner.entriesAfterSubStep(ss)

		byName := entriesByName(result)
		old := byName["old-hash-old-e"]
		assert.Equal(t, []int32{2, 3}, old.PCSGReplicaIndices["prefill"], "old floor drained")
		var newAnchor grovecorev1alpha1.PodGangEntry
		for _, e := range result {
			if e.PodCliqueSetGenerationHash == testHash {
				newAnchor = e
			}
		}
		assert.True(t, newAnchor.IsEpochAnchor)
		assert.Equal(t, map[string][]int32{"prefill": {0, 1}}, newAnchor.PCSGReplicaIndices)
		assert.Equal(t, int32(1), newAnchor.PodCliques["frontend"])
	})

	t.Run("does not mutate planner.entries", func(t *testing.T) {
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 8}, map[string]int32{"prefill": 2}, map[string]int32{"prefill": 2})
		planner.entries = []grovecorev1alpha1.PodGangEntry{
			oldHashAnchorEntryWithPods("old-hash", "old-e", nil, map[string][]int32{"prefill": {0, 1, 2, 3}}),
		}
		_ = planner.entriesAfterSubStep(subStep{epoch: "new-e", drainPCSGReplicaIndices: map[string][]int32{"prefill": {0}}})
		assert.Equal(t, []int32{0, 1, 2, 3}, planner.entries[0].PCSGReplicaIndices["prefill"])
	})
}

func TestDrainStandalonePCLQs(t *testing.T) {
	t.Run("drains oldest generation first", func(t *testing.T) {
		entries := []grovecorev1alpha1.PodGangEntry{
			oldHashAnchorEntryWithPods("v1-hash", "100", map[string]int32{"frontend": 2}, nil),
			oldHashAnchorEntryWithPods("v2-hash", "200", map[string]int32{"frontend": 3}, nil),
		}
		drainStandalonePCLQs(entries, testHash, map[string]int32{"frontend": 3})
		byName := entriesByName(entries)
		assert.Equal(t, int32(0), byName["v1-hash-100"].PodCliques["frontend"])
		assert.Equal(t, int32(2), byName["v2-hash-200"].PodCliques["frontend"])
	})

	t.Run("new-hash anchors untouched", func(t *testing.T) {
		entries := []grovecorev1alpha1.PodGangEntry{
			anchorEntryWithPods("300", map[string]int32{"frontend": 4}, nil),
		}
		drainStandalonePCLQs(entries, testHash, map[string]int32{"frontend": 2})
		assert.Equal(t, int32(4), entries[0].PodCliques["frontend"])
	})
}

func TestDrainPCSGIndices(t *testing.T) {
	entries := []grovecorev1alpha1.PodGangEntry{
		oldHashAnchorEntryWithPods("old-hash", "100", nil, map[string][]int32{"prefill": {0, 1, 2}}),
		anchorEntryWithPods("200", nil, map[string][]int32{"prefill": {0, 1, 2}}),
	}
	drainPCSGIndices(entries, testHash, map[string][]int32{"prefill": {1}})
	byName := entriesByName(entries)
	assert.Equal(t, []int32{0, 2}, byName["old-hash-100"].PCSGReplicaIndices["prefill"])
	assert.Equal(t, []int32{0, 1, 2}, byName["200"].PCSGReplicaIndices["prefill"], "new-hash untouched")
}

func TestSubsumeIntoAnchor(t *testing.T) {
	t.Run("adds counts to anchor at epoch", func(t *testing.T) {
		entries := []grovecorev1alpha1.PodGangEntry{anchorEntryWithPods("e0", map[string]int32{"frontend": 1}, nil)}
		subsumeIntoAnchor(entries, "e0", map[string]int32{"frontend": 2})
		assert.Equal(t, int32(3), entries[0].PodCliques["frontend"])
	})

	t.Run("initialises nil PodCliques map", func(t *testing.T) {
		entries := []grovecorev1alpha1.PodGangEntry{anchorEntryAt("e0", nil)}
		subsumeIntoAnchor(entries, "e0", map[string]int32{"frontend": 2})
		assert.Equal(t, int32(2), entries[0].PodCliques["frontend"])
	})

	t.Run("no-op on empty counts", func(t *testing.T) {
		entries := []grovecorev1alpha1.PodGangEntry{anchorEntryAt("e0", nil)}
		subsumeIntoAnchor(entries, "e0", nil)
		assert.Empty(t, entries[0].PodCliques)
	})
}

func TestNewHashEntriesForSubStep(t *testing.T) {
	planner := plannerForReplicasMinAvail(t,
		map[string]int32{"prefill": 8}, map[string]int32{"prefill": 2}, map[string]int32{"prefill": 2})
	planner.mvu.standalonePCLQs = map[string]int32{"frontend": 2}

	t.Run("anchor plus one non-anchor entry per PCSG", func(t *testing.T) {
		ss := subStep{
			epoch:                    "e0",
			dependsOn:                []string{"prev"},
			anchorPCSGReplicaIndices: map[string][]int32{"prefill": {0, 1}},
			tailPCSGReplicaIndices:   map[string][]int32{"prefill": {2, 3, 4}},
		}
		entries := planner.newHashEntriesForSubStep(ss)
		require.Len(t, entries, 2)
		anchor := anchorEntry(t, entries)
		assert.Equal(t, map[string]int32{"frontend": 2}, anchor.PodCliques)
		assert.Equal(t, map[string][]int32{"prefill": {0, 1}}, anchor.PCSGReplicaIndices)
		assert.Equal(t, []string{"prev"}, anchor.DependsOn)
		tails := nonAnchorEntries(entries)
		require.Len(t, tails, 1)
		assert.Equal(t, map[string][]int32{"prefill": {2, 3, 4}}, tails[0].PCSGReplicaIndices)
		assert.NotEqual(t, anchor.Name, tails[0].Name, "salted names distinct")
	})

	t.Run("empty index slice skipped", func(t *testing.T) {
		ss := subStep{epoch: "e0", tailPCSGReplicaIndices: map[string][]int32{"prefill": {}}}
		assert.Empty(t, planner.newHashEntriesForSubStep(ss))
	})
}

func TestRemoveIndices(t *testing.T) {
	tests := []struct {
		name   string
		from   []int32
		remove []int32
		want   []int32
	}{
		{name: "removes listed indices preserving order", from: []int32{0, 1, 2, 3, 4}, remove: []int32{1, 3}, want: []int32{0, 2, 4}},
		{name: "empty from is a no-op", from: nil, remove: []int32{1}, want: nil},
		{name: "empty remove returns from unchanged", from: []int32{0, 1}, remove: nil, want: []int32{0, 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, removeIndices(tc.from, tc.remove))
		})
	}
}

func TestParseEpoch(t *testing.T) {
	t.Run("parses numeric label", func(t *testing.T) {
		v, err := parseEpoch(anchorEntryAt("1748266985000000000", nil))
		require.NoError(t, err)
		assert.Equal(t, int64(1748266985000000000), v)
	})
	t.Run("missing label yields error", func(t *testing.T) {
		_, err := parseEpoch(grovecorev1alpha1.PodGangEntry{Name: "x", Labels: map[string]string{}})
		assertErrorCode(t, err, errCodeMissingEpochLabel)
	})
	t.Run("non-numeric label yields error", func(t *testing.T) {
		_, err := parseEpoch(anchorEntryAt("nope", nil))
		assertErrorCode(t, err, errCodeInvalidEpochLabel)
	})
}

func TestBuildCoherentUpdateEntries(t *testing.T) {
	t.Run("complete: fully rolled returns entries unchanged", func(t *testing.T) {
		pcs := coherentPCS(testHash)
		// current-hash entries fully cover prefill replicas (8, minAvail 2 => 4 anchor steps).
		entries := fullyRolledPrefillEntries()
		snap := coherentSnapshot(pcs, entries)
		r := newResource(newFakeClient(pcs)) // no PodGangs needed; complete short-circuits before List

		got, err := buildCoherentUpdateEntries(context.Background(), r.client, 0, snap, r.clk)
		require.NoError(t, err)
		assert.Equal(t, entries, got)
	})

	t.Run("waiting: latest batch not ready returns entries unchanged", func(t *testing.T) {
		pcs := coherentPCS(testHash)
		entries := []grovecorev1alpha1.PodGangEntry{anchorEntryAt("100", map[string][]int32{"prefill": {0, 1}})}
		snap := coherentSnapshot(pcs, entries)
		// A PodGang at epoch 100 exists but is NOT ready (no LastReady).
		r := newResource(newFakeClient(pcs, unreadyPodGang("pg-100", "100")))

		got, err := buildCoherentUpdateEntries(context.Background(), r.client, 0, snap, r.clk)
		require.NoError(t, err)
		assert.Equal(t, entries, got)
	})

	t.Run("proceed not started: opens the first anchor-bearing sub-step", func(t *testing.T) {
		pcs := coherentPCS(testHash)
		// Only old-hash entries: nothing current-hash emitted yet.
		entries := []grovecorev1alpha1.PodGangEntry{
			oldHashAnchorEntryWithPods("old-hash", "50", nil, map[string][]int32{"prefill": {0, 1, 2, 3, 4, 5, 6, 7}}),
		}
		snap := coherentSnapshot(pcs, entries)
		r := newResource(newFakeClient(pcs))

		got, err := buildCoherentUpdateEntries(context.Background(), r.client, 0, snap, r.clk)
		require.NoError(t, err)
		assert.Greater(t, len(got), len(entries), "a new-hash anchor entry was appended")
		assert.True(t, hasNewHashAnchor(got), "new-hash anchor emitted")
	})

	t.Run("proceed ready: advances to the next sub-step", func(t *testing.T) {
		pcs := coherentPCS(testHash)
		entries := []grovecorev1alpha1.PodGangEntry{anchorEntryAt("100", map[string][]int32{"prefill": {0, 1}})}
		snap := coherentSnapshot(pcs, entries)
		// PodGang at epoch 100 is ready => proceed.
		r := newResource(newFakeClient(pcs, readyPodGang("pg-100", "100")))

		got, err := buildCoherentUpdateEntries(context.Background(), r.client, 0, snap, r.clk)
		require.NoError(t, err)
		assert.Greater(t, len(got), len(entries), "next anchor-bearing step emitted")
	})

	t.Run("multi-generation re-update: drains older generations, plans current only", func(t *testing.T) {
		pcs := coherentPCS(testHash) // current hash V3
		// V1 (oldest) and V2 old-hash anchors still live, plus one current-hash (V3) anchor at epoch 300.
		entries := []grovecorev1alpha1.PodGangEntry{
			oldHashAnchorEntryWithPods("v1", "100", nil, map[string][]int32{"prefill": {6, 7}}),
			oldHashAnchorEntryWithPods("v2", "200", nil, map[string][]int32{"prefill": {2, 3, 4, 5}}),
			anchorEntryAt("300", map[string][]int32{"prefill": {0, 1}}),
		}
		snap := coherentSnapshot(pcs, entries)
		r := newResource(newFakeClient(pcs, readyPodGang("pg-300", "300")))

		got, err := buildCoherentUpdateEntries(context.Background(), r.client, 0, snap, r.clk)
		require.NoError(t, err)
		// V3 planning proceeds (a new current-hash entry appears) and older generations remain present
		// as drain targets (not wiped by the current-generation reconstruction).
		assert.True(t, hasNewHashAnchor(got))
		byName := entriesByName(got)
		_, v1Present := byName["v1-100"]
		_, v2Present := byName["v2-200"]
		assert.True(t, v1Present || v2Present, "older-generation entries remain until drained")
	})

	t.Run("budget violated: soft-requeues without emitting the next sub-step", func(t *testing.T) {
		pcs := coherentPCS(testHash)
		// Latest current-hash batch is ready, so the readiness gate passes and we reach the budget gate.
		entries := []grovecorev1alpha1.PodGangEntry{anchorEntryAt("100", map[string][]int32{"prefill": {0, 1}})}
		snap := coherentSnapshot(pcs, entries)
		// Drop the in-scope prefill PCSG below its floor (replicas 8 - maxUnavailable 2 = 6).
		snap.existingPCSGsByReplica = map[int][]grovecorev1alpha1.PodCliqueScalingGroup{0: {
			*testutils.NewPodCliqueScalingGroupBuilder("my-pcs-0-prefill", testNamespace, testPCSName, 0).
				WithReplicas(8).WithMinAvailable(2).WithStatusAvailableReplicas(5).Build(),
		}}
		r := newResource(newFakeClient(pcs, readyPodGang("pg-100", "100")))

		_, err := buildCoherentUpdateEntries(context.Background(), r.client, 0, snap, r.clk)
		assertErrorCode(t, err, groveerr.ErrCodeContinueReconcileAndRequeue)
	})
}

func TestMaxUnavailabilityBudgetViolated(t *testing.T) {
	// prefill PCSG: replicas 8, maxUnavailable 2 => floor 6. frontend PCLQ: replicas 3,
	// maxUnavailable 1 => floor 2.
	pcsg := func(available int32) *grovecorev1alpha1.PodCliqueScalingGroup {
		return testutils.NewPodCliqueScalingGroupBuilder("my-pcs-0-prefill", testNamespace, testPCSName, 0).
			WithReplicas(8).WithMinAvailable(2).WithStatusAvailableReplicas(available).Build()
	}
	pclq := func(ready int32) *grovecorev1alpha1.PodClique {
		return testutils.NewPodCliqueBuilder(testPCSName, "uid", "frontend", testNamespace, 0).
			WithReplicas(3).WithStatusReadyReplicas(ready).Build()
	}
	tests := []struct {
		name       string
		standalone map[string]int32
		pcsgs      map[string]int32
		pclqs      []grovecorev1alpha1.PodClique
		pcsgObjs   []grovecorev1alpha1.PodCliqueScalingGroup
		wantErr    bool
	}{
		{
			name:     "PCSG at floor is not a violation",
			pcsgs:    map[string]int32{"prefill": 2},
			pcsgObjs: []grovecorev1alpha1.PodCliqueScalingGroup{*pcsg(6)},
			wantErr:  false,
		},
		{
			name:     "PCSG below floor is a violation",
			pcsgs:    map[string]int32{"prefill": 2},
			pcsgObjs: []grovecorev1alpha1.PodCliqueScalingGroup{*pcsg(5)},
			wantErr:  true,
		},
		{
			name:       "PCLQ at floor is not a violation",
			standalone: map[string]int32{"frontend": 2},
			pclqs:      []grovecorev1alpha1.PodClique{*pclq(2)},
			wantErr:    false,
		},
		{
			name:       "PCLQ below floor is a violation",
			standalone: map[string]int32{"frontend": 2},
			pclqs:      []grovecorev1alpha1.PodClique{*pclq(1)},
			wantErr:    true,
		},
		{
			name:     "missing in-scope PCSG child is a violation",
			pcsgs:    map[string]int32{"prefill": 2},
			pcsgObjs: nil,
			wantErr:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			replicas := map[string]int32{}
			minAvail := map[string]int32{}
			maxUnavail := map[string]int32{}
			for c, m := range tc.pcsgs {
				replicas[c] = 8
				minAvail[c] = m
				maxUnavail[c] = 2
			}
			for c, m := range tc.standalone {
				replicas[c] = 3
				minAvail[c] = m
				maxUnavail[c] = 1
			}
			planner := plannerForReplicasMinAvail(t, replicas, minAvail, maxUnavail)
			planner.mvu.standalonePCLQs = tc.standalone
			planner.mvu.pcsgs = tc.pcsgs
			snap := &syncSnapshot{
				pcs:                              planner.pcs,
				existingStandalonePCLQsByReplica: map[int][]grovecorev1alpha1.PodClique{0: tc.pclqs},
				existingPCSGsByReplica:           map[int][]grovecorev1alpha1.PodCliqueScalingGroup{0: tc.pcsgObjs},
			}

			err := planner.maxUnavailabilityBudgetViolated(snap, 0)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// --- helpers (after all Test* functions per the package test convention) ---

// anchorEntryAt builds a current-hash anchor entry at the given epoch with the given PCSG indices.
func anchorEntryAt(epoch string, pcsgIndices map[string][]int32) grovecorev1alpha1.PodGangEntry {
	return testutils.NewPodGangEntryBuilder("anchor-"+epoch, testHash, epoch).
		WithEpochAnchor(true).WithPCSGReplicaIndices(pcsgIndices).Build()
}

// anchorEntryWithPods builds a current-hash anchor entry at the given epoch with pod counts and indices.
func anchorEntryWithPods(epoch string, pods map[string]int32, pcsgIndices map[string][]int32) grovecorev1alpha1.PodGangEntry {
	return testutils.NewPodGangEntryBuilder(epoch, testHash, epoch).
		WithEpochAnchor(true).WithPodCliques(pods).WithPCSGReplicaIndices(pcsgIndices).Build()
}

// oldHashAnchorEntryAt builds an old-generation anchor entry.
func oldHashAnchorEntryAt(hash, epoch string, pcsgIndices map[string][]int32) grovecorev1alpha1.PodGangEntry {
	return testutils.NewPodGangEntryBuilder(hash+"-"+epoch, hash, epoch).
		WithEpochAnchor(true).WithPCSGReplicaIndices(pcsgIndices).Build()
}

// oldHashAnchorEntryWithPods builds an old-generation anchor entry with pod counts and indices.
func oldHashAnchorEntryWithPods(hash, epoch string, pods map[string]int32, pcsgIndices map[string][]int32) grovecorev1alpha1.PodGangEntry {
	return testutils.NewPodGangEntryBuilder(hash+"-"+epoch, hash, epoch).
		WithEpochAnchor(true).WithPodCliques(pods).WithPCSGReplicaIndices(pcsgIndices).Build()
}

// fullyRolledPrefillEntries returns current-hash entries covering prefill replicas 8 (minAvail 2 =>
// 4 anchor-bearing steps of 2 indices each), leaving nothing to roll.
func fullyRolledPrefillEntries() []grovecorev1alpha1.PodGangEntry {
	return []grovecorev1alpha1.PodGangEntry{
		anchorEntryAt("100", map[string][]int32{"prefill": {0, 1}}),
		anchorEntryAt("200", map[string][]int32{"prefill": {2, 3}}),
		anchorEntryAt("300", map[string][]int32{"prefill": {4, 5}}),
		anchorEntryAt("400", map[string][]int32{"prefill": {6, 7}}),
	}
}

// hasNewHashAnchor reports whether entries contains a current-hash anchor entry.
func hasNewHashAnchor(entries []grovecorev1alpha1.PodGangEntry) bool {
	for _, e := range entries {
		if e.PodCliqueSetGenerationHash == testHash && e.IsEpochAnchor {
			return true
		}
	}
	return false
}

// readyPodGang builds a PodGang at the given epoch (for testPCS replica 0) that has become ready.
func readyPodGang(name, epoch string) *groveschedulerv1alpha1.PodGang {
	return podGangAtEpoch(name, epoch).WithStatusLastReady(metav1.NewTime(time.Unix(0, 0))).Build()
}

// unreadyPodGang builds a PodGang at the given epoch that has not become ready.
func unreadyPodGang(name, epoch string) *groveschedulerv1alpha1.PodGang {
	return podGangAtEpoch(name, epoch).Build()
}

// podGangAtEpoch builds a PodGang carrying the labels AllPodGangsAtEpochEverReady selects on for
// testPCS replica 0 at the given epoch.
func podGangAtEpoch(name, epoch string) *testutils.PodGangBuilder {
	return testutils.NewPodGangBuilder(name, testNamespace).
		WithLabel(apicommon.LabelPartOfKey, testPCSName).
		WithLabel(apicommon.LabelPodCliqueSetReplicaIndex, "0").
		WithLabel(apicommon.LabelEpoch, epoch)
}

// coherentPCS builds a Coherent-strategy PCS (replica 0 under update, prefill in scope) at the hash.
func coherentPCS(hash string) *grovecorev1alpha1.PodCliqueSet {
	return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
		WithReplicas(1).
		WithScalingGroupConfig("prefill", []string{"pworker"}, 8, 2).
		WithScalingGroupMaxUnavailable("prefill", 2).
		WithStatusCurrentGenerationHash(ptr.To(hash)).
		WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy}).
		WithUpdateProgress(&grovecorev1alpha1.PodCliqueSetUpdateProgress{
			CurrentlyUpdating:             []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}},
			InScopePodCliqueScalingGroups: []string{"prefill"},
		}).Build()
}

// coherentSnapshot assembles a syncSnapshot for the coherent path with the given PGM entries and a
// computed mvuTemplate for the in-scope prefill PCSG. The PCSG is seeded fully available
// (AvailableReplicas == replicas) so the MaxUnavailable budget gate is a no-op; tests that exercise
// a budget violation build their own snapshot.
func coherentSnapshot(pcs *grovecorev1alpha1.PodCliqueSet, entries []grovecorev1alpha1.PodGangEntry) *syncSnapshot {
	pgm := testutils.NewPodGangMapBuilder(testPCSName, testNamespace, "uid", 0).WithEntries(entries...).Build()
	pcsg := testutils.NewPodCliqueScalingGroupBuilder("my-pcs-0-prefill", testNamespace, testPCSName, 0).
		WithReplicas(8).WithMinAvailable(2).WithStatusAvailableReplicas(8).Build()
	return &syncSnapshot{
		pcs:                    pcs,
		existingPGMByReplica:   map[int]grovecorev1alpha1.PodGangMap{0: *pgm},
		existingPCSGsByReplica: map[int][]grovecorev1alpha1.PodCliqueScalingGroup{0: {*pcsg}},
		mvuTemplate:            &mvuTemplate{standalonePCLQs: map[string]int32{}, pcsgs: map[string]int32{"prefill": 2}},
	}
}

// plannerForReplicasMinAvail builds a subStepPlanner directly with the given step-plan inputs,
// computing numAnchorBearingSteps/anchorBearingStepTarget/leftover the same way newSubStepPlanner
// does. It avoids constructing a syncSnapshot for the pure planner tests. The mvu starts PCSG-only;
// tests that need standalone components set planner.mvu.standalonePCLQs afterward.
func plannerForReplicasMinAvail(t *testing.T, replicas, minAvailable, maxUnavailable map[string]int32) *subStepPlanner {
	t.Helper()
	pcsgs := make(map[string]int32, len(minAvailable))
	for c, m := range minAvailable {
		pcsgs[c] = m
	}
	mvu := &mvuTemplate{standalonePCLQs: map[string]int32{}, pcsgs: pcsgs}

	var numSteps int32
	first := true
	for c := range replicas {
		steps := replicas[c] / minAvailable[c]
		if first || steps < numSteps {
			numSteps = steps
			first = false
		}
	}
	target := make(map[string]int32, len(replicas))
	leftover := make(map[string]int32, len(replicas))
	for c := range replicas {
		tailPerStep := (replicas[c] - numSteps*minAvailable[c]) / numSteps
		target[c] = minAvailable[c] + tailPerStep
		leftover[c] = replicas[c] - numSteps*target[c]
	}

	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
		WithStatusCurrentGenerationHash(ptr.To(testHash)).Build()

	return &subStepPlanner{
		pcs:                     pcs,
		mvu:                     mvu,
		pcsReplicaIndex:         0,
		clk:                     clocktesting.NewFakeClock(time.Unix(0, 1000)),
		replicas:                replicas,
		minAvailable:            minAvailable,
		maxUnavailable:          maxUnavailable,
		numAnchorBearingSteps:   numSteps,
		anchorBearingStepTarget: target,
		leftover:                leftover,
	}
}

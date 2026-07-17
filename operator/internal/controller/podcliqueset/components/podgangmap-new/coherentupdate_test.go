// /*
// Copyright 2025 The Grove Authors.
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

package podgangmap_new

import (
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
)

func TestRecordedEpochs(t *testing.T) {
	t.Run("collects epochs across all steps and sub-steps", func(t *testing.T) {
		progress := newProgress(
			anchorStep("e0", "e1"),
			leftoverStep("e2"),
		)
		recorded := recordedEpochs(progress)
		assert.Equal(t, map[string]bool{"e0": true, "e1": true, "e2": true}, recorded)
	})

	t.Run("empty progress yields empty set", func(t *testing.T) {
		assert.Empty(t, recordedEpochs(&grovecorev1alpha1.PodGangMapUpdateProgress{}))
	})
}

func TestDetermineReadinessByEpoch(t *testing.T) {
	readyTime := metav1.NewTime(time.Unix(0, 0))
	readyPodGang := func(name, epoch string) groveschedulerv1alpha1.PodGang {
		return *testutils.NewPodGangBuilder(name, testNamespace).
			WithLabel(apicommon.LabelEpoch, epoch).WithStatusLastReady(readyTime).Build()
	}
	notReadyPodGang := func(name, epoch string) groveschedulerv1alpha1.PodGang {
		return *testutils.NewPodGangBuilder(name, testNamespace).
			WithLabel(apicommon.LabelEpoch, epoch).Build()
	}
	tests := []struct {
		name        string
		podGangs    []groveschedulerv1alpha1.PodGang
		wantReady   map[string]bool
		wantErrCode grovecorev1alpha1.ErrorCode
	}{
		{
			name:      "epoch ready when all its PodGangs have LastReady",
			podGangs:  []groveschedulerv1alpha1.PodGang{readyPodGang("pg-0", "e0"), readyPodGang("pg-1", "e0")},
			wantReady: map[string]bool{"e0": true},
		},
		{
			name:      "epoch not ready when one PodGang lacks LastReady",
			podGangs:  []groveschedulerv1alpha1.PodGang{readyPodGang("pg-0", "e0"), notReadyPodGang("pg-1", "e0")},
			wantReady: map[string]bool{"e0": false},
		},
		{
			name:      "independent epochs reported separately",
			podGangs:  []groveschedulerv1alpha1.PodGang{readyPodGang("pg-0", "e0"), notReadyPodGang("pg-1", "e1")},
			wantReady: map[string]bool{"e0": true, "e1": false},
		},
		{
			name:        "PodGang missing epoch label yields error",
			podGangs:    []groveschedulerv1alpha1.PodGang{*testutils.NewPodGangBuilder("pg-0", testNamespace).Build()},
			wantErrCode: errCodeMissingEpochLabel,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			readiness, err := determineReadinessByEpoch(tc.podGangs)
			if tc.wantErrCode != "" {
				assertErrorCode(t, err, tc.wantErrCode)
				return
			}
			require.NoError(t, err)
			for epoch, want := range tc.wantReady {
				assert.Equal(t, want, readiness[epoch], "epoch %s", epoch)
			}
		})
	}
}

func TestAdvanceReadiness(t *testing.T) {
	t.Run("ready epoch flips sub-step and step to Ready", func(t *testing.T) {
		progress := newProgress(anchorStep("e0"))
		advanceReadiness(progress, map[string]bool{"e0": true})
		assert.Equal(t, grovecorev1alpha1.PodGangMapUpdateStateReady, progress.Steps[0].SubSteps[0].State)
		assert.Equal(t, grovecorev1alpha1.PodGangMapUpdateStateReady, progress.Steps[0].State)
	})

	t.Run("step stays InProgress when not all sub-steps ready", func(t *testing.T) {
		progress := newProgress(anchorStep("e0", "e1"))
		advanceReadiness(progress, map[string]bool{"e0": true, "e1": false})
		assert.Equal(t, grovecorev1alpha1.PodGangMapUpdateStateReady, progress.Steps[0].SubSteps[0].State)
		assert.Equal(t, grovecorev1alpha1.PodGangMapUpdateStateInProgress, progress.Steps[0].SubSteps[1].State)
		assert.Equal(t, grovecorev1alpha1.PodGangMapUpdateStateInProgress, progress.Steps[0].State)
	})

	t.Run("already-Ready sub-step is not un-set when epoch reports false", func(t *testing.T) {
		progress := newProgress(anchorStep("e0"))
		progress.Steps[0].SubSteps[0].State = grovecorev1alpha1.PodGangMapUpdateStateReady
		advanceReadiness(progress, map[string]bool{"e0": false})
		assert.Equal(t, grovecorev1alpha1.PodGangMapUpdateStateReady, progress.Steps[0].SubSteps[0].State)
	})

	t.Run("nil progress is a no-op", func(t *testing.T) {
		assert.NotPanics(t, func() { advanceReadiness(nil, map[string]bool{"e0": true}) })
	})
}

func TestLatestBatchReady(t *testing.T) {
	readyAnchor := func() *grovecorev1alpha1.PodGangMapUpdateProgress {
		p := newProgress(anchorStep("e0"))
		p.Steps[0].SubSteps[0].State = grovecorev1alpha1.PodGangMapUpdateStateReady
		return p
	}
	tests := []struct {
		name     string
		progress *grovecorev1alpha1.PodGangMapUpdateProgress
		want     bool
	}{
		{name: "no steps means ungated", progress: &grovecorev1alpha1.PodGangMapUpdateProgress{}, want: true},
		{name: "last sub-step Ready", progress: readyAnchor(), want: true},
		{name: "last sub-step InProgress", progress: newProgress(anchorStep("e0")), want: false},
		{
			name:     "last step with no sub-steps keeps gate closed",
			progress: &grovecorev1alpha1.PodGangMapUpdateProgress{Steps: []grovecorev1alpha1.PodGangMapUpdateStep{{IsAnchorBearing: true}}},
			want:     false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, latestBatchReady(tc.progress))
		})
	}
}

func TestSyncProgressFromSpec(t *testing.T) {
	t.Run("no unrecorded epoch returns false", func(t *testing.T) {
		entries := []grovecorev1alpha1.PodGangEntry{
			testutils.NewPodGangEntryBuilder("a-0", testHash, "e0").WithEpochAnchor(true).Build(),
		}
		progress := newProgress(anchorStep("e0"))
		changed := syncProgressFromSpec(entries, progress, testHash)
		assert.False(t, changed)
	})

	t.Run("unrecorded anchor epoch opens a new anchor-bearing step", func(t *testing.T) {
		entries := []grovecorev1alpha1.PodGangEntry{
			testutils.NewPodGangEntryBuilder("a-0", testHash, "e0").WithEpochAnchor(true).Build(),
			testutils.NewPodGangEntryBuilder("a-1", testHash, "e1").WithEpochAnchor(true).Build(),
		}
		progress := newProgress(anchorStep("e0"))
		changed := syncProgressFromSpec(entries, progress, testHash)
		assert.True(t, changed)
		require.Len(t, progress.Steps, 2)
		assert.True(t, progress.Steps[1].IsAnchorBearing)
		assert.Equal(t, "e1", progress.Steps[1].SubSteps[0].Epoch)
	})

	t.Run("unrecorded non-anchor epoch appends a tail sub-step to current step", func(t *testing.T) {
		entries := []grovecorev1alpha1.PodGangEntry{
			testutils.NewPodGangEntryBuilder("a-0", testHash, "e0").WithEpochAnchor(true).Build(),
			testutils.NewPodGangEntryBuilder("t-0", testHash, "e1").WithEpochAnchor(false).Build(),
		}
		progress := newProgress(anchorStep("e0"))
		changed := syncProgressFromSpec(entries, progress, testHash)
		assert.True(t, changed)
		require.Len(t, progress.Steps, 1)
		require.Len(t, progress.Steps[0].SubSteps, 2)
		assert.Equal(t, "e1", progress.Steps[0].SubSteps[1].Epoch)
	})

	t.Run("old-hash entries are skipped", func(t *testing.T) {
		entries := []grovecorev1alpha1.PodGangEntry{
			testutils.NewPodGangEntryBuilder("old-0", "old-hash", "e9").WithEpochAnchor(true).Build(),
		}
		progress := newProgress(anchorStep("e0"))
		changed := syncProgressFromSpec(entries, progress, testHash)
		assert.False(t, changed)
	})
}

func TestNewSubStepPlanner(t *testing.T) {
	t.Run("worked example single PCSG", func(t *testing.T) {
		// prefill: replicas 80, minAvailable 3, maxUnavailable 4.
		// numAnchorBearingSteps = floor(80/3) = 26, tailPerStep = (80-26*3)/26 = 0,
		// anchorBearingStepTarget = 3, leftover = 80 - 26*3 = 2.
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 80}, // replicas
			map[string]int32{"prefill": 3},  // minAvailable
			map[string]int32{"prefill": 4},  // maxUnavailable
		)
		assert.Equal(t, int32(26), planner.numAnchorBearingSteps)
		assert.Equal(t, int32(3), planner.anchorBearingStepTarget["prefill"])
		assert.Equal(t, int32(2), planner.leftover["prefill"])
	})

	t.Run("even division yields zero leftover", func(t *testing.T) {
		// replicas 8, minAvailable 2: steps=4, tailPerStep=0, target=2, leftover=0.
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 8},
			map[string]int32{"prefill": 2},
			map[string]int32{"prefill": 2},
		)
		assert.Equal(t, int32(4), planner.numAnchorBearingSteps)
		assert.Equal(t, int32(2), planner.anchorBearingStepTarget["prefill"])
		assert.Equal(t, int32(0), planner.leftover["prefill"])
	})

	t.Run("numAnchorBearingSteps is min over components", func(t *testing.T) {
		// prefill: floor(9/3)=3, decode: floor(4/2)=2 -> min = 2.
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 9, "decode": 4},
			map[string]int32{"prefill": 3, "decode": 2},
			map[string]int32{"prefill": 3, "decode": 2},
		)
		assert.Equal(t, int32(2), planner.numAnchorBearingSteps)
	})
}

func TestMaxEpoch(t *testing.T) {
	tests := []struct {
		name        string
		entries     []grovecorev1alpha1.PodGangEntry
		wantEpoch   *string
		wantErrCode grovecorev1alpha1.ErrorCode
	}{
		{
			name: "largest numeric epoch",
			entries: []grovecorev1alpha1.PodGangEntry{
				testutils.NewPodGangEntryBuilder("a", testHash, "100").WithEpochAnchor(true).Build(),
				testutils.NewPodGangEntryBuilder("b", testHash, "2000").WithEpochAnchor(false).Build(),
				testutils.NewPodGangEntryBuilder("c", testHash, "30").WithEpochAnchor(false).Build(),
			},
			wantEpoch: ptr.To("2000"),
		},
		{
			name:      "empty entries yields nil",
			entries:   nil,
			wantEpoch: nil,
		},
		{
			name:        "missing epoch label yields error",
			entries:     []grovecorev1alpha1.PodGangEntry{{Name: "a", Labels: map[string]string{}}},
			wantErrCode: errCodeMissingEpochLabel,
		},
		{
			name:        "non-numeric epoch label yields error",
			entries:     []grovecorev1alpha1.PodGangEntry{testutils.NewPodGangEntryBuilder("a", testHash, "not-a-number").WithEpochAnchor(true).Build()},
			wantErrCode: errCodeInvalidEpochLabel,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := maxEpoch(tc.entries)
			if tc.wantErrCode != "" {
				assertErrorCode(t, err, tc.wantErrCode)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantEpoch, got)
		})
	}
}

func TestEpochDependencyAsSlice(t *testing.T) {
	tests := []struct {
		name  string
		epoch *string
		want  []string
	}{
		{name: "nil epoch yields nil slice", epoch: nil, want: nil},
		{name: "non-nil epoch yields single-element slice", epoch: ptr.To("5"), want: []string{"5"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, epochDependencyAsSlice(tc.epoch))
		})
	}
}

func TestReadyCountsByComponent(t *testing.T) {
	mvu := &mvuTemplate{
		standalonePCLQs: map[string]int32{"frontend": 1},
		pcsgs:           map[string]int32{"prefill": 1},
	}
	plannerWith := func(entries []grovecorev1alpha1.PodGangEntry) *subStepPlanner {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithStatusCurrentGenerationHash(ptr.To(testHash)).Build()
		return &subStepPlanner{pcs: pcs, mvu: mvu, entries: entries}
	}

	t.Run("counts only Ready sub-steps", func(t *testing.T) {
		p := plannerWith([]grovecorev1alpha1.PodGangEntry{
			testutils.NewPodGangEntryBuilder("a", testHash, "e0").WithEpochAnchor(true).WithPodCliques(map[string]int32{"frontend": 2}).WithPCSGReplicaIndices(map[string][]int32{"prefill": {0}}).Build(),
			testutils.NewPodGangEntryBuilder("b", testHash, "e1").WithEpochAnchor(false).WithPodCliques(nil).WithPCSGReplicaIndices(map[string][]int32{"prefill": {1, 2}}).Build(),
		})
		progress := newProgress(anchorStep("e0")) // e0 Ready, e1 not recorded
		progress.Steps[0].SubSteps[0].State = grovecorev1alpha1.PodGangMapUpdateStateReady

		counts := p.readyCountsByComponent(progress, nil)
		assert.Equal(t, int32(2), counts["frontend"])
		assert.Equal(t, int32(1), counts["prefill"]) // only e0's index counted
	})

	t.Run("matchingEpochs filter restricts count", func(t *testing.T) {
		p := plannerWith([]grovecorev1alpha1.PodGangEntry{
			testutils.NewPodGangEntryBuilder("a", testHash, "e0").WithEpochAnchor(true).WithPodCliques(nil).WithPCSGReplicaIndices(map[string][]int32{"prefill": {0}}).Build(),
			testutils.NewPodGangEntryBuilder("b", testHash, "e1").WithEpochAnchor(false).WithPodCliques(nil).WithPCSGReplicaIndices(map[string][]int32{"prefill": {1}}).Build(),
		})
		progress := newProgress(anchorStep("e0", "e1"))
		progress.Steps[0].SubSteps[0].State = grovecorev1alpha1.PodGangMapUpdateStateReady
		progress.Steps[0].SubSteps[1].State = grovecorev1alpha1.PodGangMapUpdateStateReady

		counts := p.readyCountsByComponent(progress, sets.New("e0"))
		assert.Equal(t, int32(1), counts["prefill"])
	})

	t.Run("old-hash entries excluded", func(t *testing.T) {
		p := plannerWith([]grovecorev1alpha1.PodGangEntry{
			testutils.NewPodGangEntryBuilder("old", "old-hash", "e0").WithEpochAnchor(true).WithPodCliques(map[string]int32{"frontend": 5}).WithPCSGReplicaIndices(nil).Build(),
		})
		progress := newProgress(anchorStep("e0"))
		progress.Steps[0].SubSteps[0].State = grovecorev1alpha1.PodGangMapUpdateStateReady

		counts := p.readyCountsByComponent(progress, nil)
		assert.Zero(t, counts["frontend"])
	})
}

func TestAllComponentsFullyRolled(t *testing.T) {
	replicas := map[string]int32{"frontend": 5, "prefill": 3}
	tests := []struct {
		name        string
		readyCounts map[string]int32
		want        bool
	}{
		{name: "true when every component matches replicas", readyCounts: map[string]int32{"frontend": 5, "prefill": 3}, want: true},
		{name: "false on any gap", readyCounts: map[string]int32{"frontend": 5, "prefill": 2}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, allComponentsFullyRolled(tc.readyCounts, replicas))
		})
	}
}

func TestRecordedAnchorBearingSteps(t *testing.T) {
	progress := newProgress(anchorStep("e0"), anchorStep("e1"), leftoverStep("e2"))
	assert.Equal(t, int32(2), recordedAnchorBearingSteps(progress))
}

func TestRemainingInCurrentStep(t *testing.T) {
	t.Run("anchor-bearing step gap against anchorBearingStepTarget", func(t *testing.T) {
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 8}, map[string]int32{"prefill": 2}, map[string]int32{"prefill": 2})
		// anchorBearingStepTarget = 2. Anchor rolled index 0 (1 ready), so remaining = 1.
		planner.entries = []grovecorev1alpha1.PodGangEntry{
			testutils.NewPodGangEntryBuilder("a", testHash, "e0").WithEpochAnchor(true).WithPodCliques(nil).WithPCSGReplicaIndices(map[string][]int32{"prefill": {0}}).Build(),
		}
		progress := newProgress(anchorStep("e0"))
		progress.Steps[0].SubSteps[0].State = grovecorev1alpha1.PodGangMapUpdateStateReady

		remaining := planner.remainingInCurrentStep(progress)
		assert.Equal(t, int32(1), remaining["prefill"])
	})

	t.Run("fully met step yields empty map", func(t *testing.T) {
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 8}, map[string]int32{"prefill": 2}, map[string]int32{"prefill": 2})
		planner.entries = []grovecorev1alpha1.PodGangEntry{
			testutils.NewPodGangEntryBuilder("a", testHash, "e0").WithEpochAnchor(true).WithPodCliques(nil).WithPCSGReplicaIndices(map[string][]int32{"prefill": {0, 1}}).Build(),
		}
		progress := newProgress(anchorStep("e0"))
		progress.Steps[0].SubSteps[0].State = grovecorev1alpha1.PodGangMapUpdateStateReady

		assert.Empty(t, planner.remainingInCurrentStep(progress))
	})
}

func TestIsCurrentStepComplete(t *testing.T) {
	planner := plannerForReplicasMinAvail(t,
		map[string]int32{"prefill": 8}, map[string]int32{"prefill": 2}, map[string]int32{"prefill": 2})
	planner.entries = []grovecorev1alpha1.PodGangEntry{
		testutils.NewPodGangEntryBuilder("a", testHash, "e0").WithEpochAnchor(true).WithPodCliques(nil).WithPCSGReplicaIndices(map[string][]int32{"prefill": {0, 1}}).Build(),
	}
	progress := newProgress(anchorStep("e0"))
	progress.Steps[0].SubSteps[0].State = grovecorev1alpha1.PodGangMapUpdateStateReady
	assert.True(t, planner.isCurrentStepComplete(progress))
}

func TestAnyLeftoverRemaining(t *testing.T) {
	planner := plannerForReplicasMinAvail(t,
		map[string]int32{"prefill": 8}, map[string]int32{"prefill": 3}, map[string]int32{"prefill": 3})
	tests := []struct {
		name        string
		readyCounts map[string]int32
		want        bool
	}{
		{name: "true when a component is short of replicas", readyCounts: map[string]int32{"prefill": 6}, want: true},
		{name: "false when all rolled", readyCounts: map[string]int32{"prefill": 8}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, planner.anyLeftoverRemaining(tc.readyCounts))
		})
	}
}

func TestBuildAnchorBearingSubStep(t *testing.T) {
	t.Run("first anchor claims floor indices and drains old-hash floor", func(t *testing.T) {
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 8}, map[string]int32{"prefill": 2}, map[string]int32{"prefill": 2})
		planner.clk = clocktesting.NewFakeClock(time.Unix(0, 7000))
		progress := &grovecorev1alpha1.PodGangMapUpdateProgress{}

		ss, progress, err := planner.buildAnchorBearingSubStep(progress)
		require.NoError(t, err)
		// k=0: floor indices [0, minAvailable) = [0, 2).
		assert.Equal(t, map[string][]int32{"prefill": {0, 1}}, ss.anchorPCSGReplicaIndices)
		assert.Equal(t, map[string][]int32{"prefill": {0, 1}}, ss.drainPCSGReplicaIndices)
		assert.Equal(t, planner.mvu.standalonePCLQs, ss.drainStandalonePCLQCounts)
		require.Len(t, progress.Steps, 1)
		assert.True(t, progress.Steps[0].IsAnchorBearing)
		assert.Equal(t, "7000", progress.Steps[0].SubSteps[0].Epoch)
	})

	t.Run("second anchor claims the next contiguous block", func(t *testing.T) {
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 8}, map[string]int32{"prefill": 2}, map[string]int32{"prefill": 2})
		// anchorBearingStepTarget = 2. k=1 floor = [1*2, 1*2+2) = [2, 4).
		progress := newProgress(anchorStep("e0"))
		ss, _, err := planner.buildAnchorBearingSubStep(progress)
		require.NoError(t, err)
		assert.Equal(t, map[string][]int32{"prefill": {2, 3}}, ss.anchorPCSGReplicaIndices)
	})
}

func TestBuildTailSubStep(t *testing.T) {
	// Worked example: prefill replicas 80, minAvailable 3, maxUnavailable 4 -> per README not
	// used here; use a smaller shape. replicas 8, minAvailable 2, maxUnavailable 3.
	// anchorBearingStepTarget = 2 + (8-4*2)/4 = 2. This does not exercise a tail (target==minAvail).
	// Use replicas 16, minAvailable 2, maxUnavailable 3: steps=8, tailPerStep=0, target=2. Still no tail.
	// A tail requires target > minAvailable: replicas 12, minAvailable 2 -> steps=6, tailPerStep=0.
	// tailPerStep>0 needs replicas >= steps*(minAvail+1); with steps=min(floor) this is delicate.
	// Use two components so numAnchorBearingSteps is bounded low: prefill replicas 10 minAvail 2
	// alone gives steps 5, target 2. Pair with decode replicas 2 minAvail 1 -> steps=min(5,2)=2.
	// prefill: tailPerStep=(10-2*2)/2=3, target=2+3=5, leftover=10-2*5=0.
	planner := plannerForReplicasMinAvail(t,
		map[string]int32{"prefill": 10, "decode": 2},
		map[string]int32{"prefill": 2, "decode": 1},
		map[string]int32{"prefill": 4, "decode": 1},
	)
	planner.clk = clocktesting.NewFakeClock(time.Unix(0, 9000))
	// Open step k=0 with anchor at epoch 100 having rolled prefill floor [0,2) and decode [0,1).
	planner.entries = []grovecorev1alpha1.PodGangEntry{
		testutils.NewPodGangEntryBuilder("a", testHash, "100").WithEpochAnchor(true).WithPodCliques(nil).WithPCSGReplicaIndices(map[string][]int32{"prefill": {0, 1}, "decode": {0}}).Build(),
	}
	progress := newProgress(anchorStep("100"))
	progress.Steps[0].SubSteps[0].State = grovecorev1alpha1.PodGangMapUpdateStateReady

	ss, _, err := planner.buildTailSubStep(progress)
	require.NoError(t, err)
	// prefill target 5, ready 2, remaining 3, rollBudget min(4,3)=3.
	// index start = (0+1)*5 - 3 = 2 -> indices [2,5).
	assert.Equal(t, []int32{2, 3, 4}, ss.tailPCSGReplicaIndices["prefill"])
	assert.Equal(t, []int32{2, 3, 4}, ss.drainPCSGReplicaIndices["prefill"])
	// subsume target is the current step's anchor epoch 100.
	assert.Equal(t, "100", ss.subsumeAnchorEpoch)
	// the new tail sub-step was appended to the current step.
	assert.Len(t, progress.Steps[0].SubSteps, 2)
}

func TestBuildLeftoverSubStep(t *testing.T) {
	// prefill replicas 10, minAvailable 3, maxUnavailable 2: numAnchorBearingSteps=3, target=3,
	// leftover=1. The three anchor-bearing steps have rolled indices [0,9); index 9 is the leftover.
	planner := plannerForReplicasMinAvail(t,
		map[string]int32{"prefill": 10}, map[string]int32{"prefill": 3}, map[string]int32{"prefill": 2})
	planner.clk = clocktesting.NewFakeClock(time.Unix(0, 12000))
	require.Equal(t, int32(1), planner.leftover["prefill"])
	planner.entries = []grovecorev1alpha1.PodGangEntry{
		testutils.NewPodGangEntryBuilder("a0", testHash, "100").WithEpochAnchor(true).WithPodCliques(nil).WithPCSGReplicaIndices(map[string][]int32{"prefill": {0, 1, 2}}).Build(),
		testutils.NewPodGangEntryBuilder("a1", testHash, "200").WithEpochAnchor(true).WithPodCliques(nil).WithPCSGReplicaIndices(map[string][]int32{"prefill": {3, 4, 5}}).Build(),
		testutils.NewPodGangEntryBuilder("a2", testHash, "300").WithEpochAnchor(true).WithPodCliques(nil).WithPCSGReplicaIndices(map[string][]int32{"prefill": {6, 7, 8}}).Build(),
	}
	// last recorded step is anchor-bearing; the leftover opens a new step.
	progress := newProgress(anchorStep("100"), anchorStep("200"), anchorStep("300"))
	markAllReady(progress)

	ss, progress, err := planner.buildLeftoverSubStep(progress)
	require.NoError(t, err)
	// leftover index start = replicas - remaining = 10 - 1 = 9 -> [9, 10).
	assert.Equal(t, []int32{9}, ss.tailPCSGReplicaIndices["prefill"])
	// standalone pods subsume into the most recent anchor step's anchor (epoch 300).
	assert.Equal(t, "300", ss.subsumeAnchorEpoch)
	require.Len(t, progress.Steps, 4)
	assert.False(t, progress.Steps[3].IsAnchorBearing)
}

func TestMostRecentAnchorEpoch(t *testing.T) {
	progress := newProgress(anchorStep("e0"), anchorStep("e1"), leftoverStep("e2"))
	assert.Equal(t, "e1", mostRecentAnchorEpoch(progress))
}

func TestApplySubStep(t *testing.T) {
	t.Run("anchor-bearing sub-step appends anchor and drains old-hash floor", func(t *testing.T) {
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 8}, map[string]int32{"prefill": 2}, map[string]int32{"prefill": 2})
		// old-hash anchor carries the full old generation.
		planner.entries = []grovecorev1alpha1.PodGangEntry{
			testutils.NewPodGangEntryBuilder("old", "old-hash", "old-e").WithEpochAnchor(true).WithPodCliques(map[string]int32{"frontend": 1}).WithPCSGReplicaIndices(map[string][]int32{"prefill": {0, 1, 2, 3}}).Build(),
		}
		planner.mvu.standalonePCLQs = map[string]int32{"frontend": 1}

		ss := subStep{
			epoch:                     "new-e",
			anchorPCSGReplicaIndices:  map[string][]int32{"prefill": {0, 1}},
			drainStandalonePCLQCounts: map[string]int32{"frontend": 1},
			drainPCSGReplicaIndices:   map[string][]int32{"prefill": {0, 1}},
		}
		result := planner.applySubStep(ss)

		byName := entriesByName(result)
		// new anchor added at new-e.
		var newAnchor grovecorev1alpha1.PodGangEntry
		for _, e := range result {
			if e.PodCliqueSetGenerationHash == testHash {
				newAnchor = e
			}
		}
		assert.True(t, newAnchor.IsEpochAnchor)
		assert.Equal(t, map[string][]int32{"prefill": {0, 1}}, newAnchor.PCSGReplicaIndices)
		assert.Equal(t, int32(1), newAnchor.PodCliques["frontend"])
		// old anchor drained: floor [0,1] removed, [2,3] remain; frontend 1->0.
		old := byName["old"]
		assert.Equal(t, []int32{2, 3}, old.PCSGReplicaIndices["prefill"])
	})

	t.Run("does not mutate planner.entries", func(t *testing.T) {
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 8}, map[string]int32{"prefill": 2}, map[string]int32{"prefill": 2})
		planner.entries = []grovecorev1alpha1.PodGangEntry{
			testutils.NewPodGangEntryBuilder("old", "old-hash", "old-e").WithEpochAnchor(true).WithPodCliques(nil).WithPCSGReplicaIndices(map[string][]int32{"prefill": {0, 1, 2, 3}}).Build(),
		}
		ss := subStep{epoch: "new-e", drainPCSGReplicaIndices: map[string][]int32{"prefill": {0}}}
		_ = planner.applySubStep(ss)
		assert.Equal(t, []int32{0, 1, 2, 3}, planner.entries[0].PCSGReplicaIndices["prefill"])
	})
}

func TestDrainStandalonePCLQs(t *testing.T) {
	t.Run("drains oldest generation first", func(t *testing.T) {
		entries := []grovecorev1alpha1.PodGangEntry{
			testutils.NewPodGangEntryBuilder("v1", "v1-hash", "100").WithEpochAnchor(true).WithPodCliques(map[string]int32{"frontend": 2}).WithPCSGReplicaIndices(nil).Build(),
			testutils.NewPodGangEntryBuilder("v2", "v2-hash", "200").WithEpochAnchor(true).WithPodCliques(map[string]int32{"frontend": 3}).WithPCSGReplicaIndices(nil).Build(),
		}
		// drain 3: fully drains v1 (epoch 100, oldest) then 1 from v2.
		drainStandalonePCLQs(entries, testHash, map[string]int32{"frontend": 3})
		byName := entriesByName(entries)
		assert.Equal(t, int32(0), byName["v1"].PodCliques["frontend"])
		assert.Equal(t, int32(2), byName["v2"].PodCliques["frontend"])
	})

	t.Run("new-hash anchors untouched", func(t *testing.T) {
		entries := []grovecorev1alpha1.PodGangEntry{
			testutils.NewPodGangEntryBuilder("cur", testHash, "300").WithEpochAnchor(true).WithPodCliques(map[string]int32{"frontend": 4}).WithPCSGReplicaIndices(nil).Build(),
		}
		drainStandalonePCLQs(entries, testHash, map[string]int32{"frontend": 2})
		assert.Equal(t, int32(4), entries[0].PodCliques["frontend"])
	})
}

func TestDrainPCSGIndices(t *testing.T) {
	entries := []grovecorev1alpha1.PodGangEntry{
		testutils.NewPodGangEntryBuilder("old", "old-hash", "100").WithEpochAnchor(true).WithPodCliques(nil).WithPCSGReplicaIndices(map[string][]int32{"prefill": {0, 1, 2}}).Build(),
		testutils.NewPodGangEntryBuilder("cur", testHash, "200").WithEpochAnchor(true).WithPodCliques(nil).WithPCSGReplicaIndices(map[string][]int32{"prefill": {0, 1, 2}}).Build(),
	}
	drainPCSGIndices(entries, testHash, map[string][]int32{"prefill": {1}})
	byName := entriesByName(entries)
	assert.Equal(t, []int32{0, 2}, byName["old"].PCSGReplicaIndices["prefill"])
	assert.Equal(t, []int32{0, 1, 2}, byName["cur"].PCSGReplicaIndices["prefill"], "new-hash untouched")
}

func TestSubsumeIntoAnchor(t *testing.T) {
	t.Run("adds counts to anchor at epoch", func(t *testing.T) {
		entries := []grovecorev1alpha1.PodGangEntry{
			testutils.NewPodGangEntryBuilder("a", testHash, "e0").WithEpochAnchor(true).WithPodCliques(map[string]int32{"frontend": 1}).WithPCSGReplicaIndices(nil).Build(),
		}
		subsumeIntoAnchor(entries, "e0", map[string]int32{"frontend": 2})
		assert.Equal(t, int32(3), entries[0].PodCliques["frontend"])
	})

	t.Run("initialises nil PodCliques map", func(t *testing.T) {
		entries := []grovecorev1alpha1.PodGangEntry{testutils.NewPodGangEntryBuilder("a", testHash, "e0").WithEpochAnchor(true).Build()}
		subsumeIntoAnchor(entries, "e0", map[string]int32{"frontend": 2})
		assert.Equal(t, int32(2), entries[0].PodCliques["frontend"])
	})

	t.Run("no-op on empty counts", func(t *testing.T) {
		entries := []grovecorev1alpha1.PodGangEntry{testutils.NewPodGangEntryBuilder("a", testHash, "e0").WithEpochAnchor(true).Build()}
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

	t.Run("no anchor when anchorPCSGReplicaIndices empty", func(t *testing.T) {
		ss := subStep{epoch: "e0", tailPCSGReplicaIndices: map[string][]int32{"prefill": {5}}}
		entries := planner.newHashEntriesForSubStep(ss)
		require.Len(t, entries, 1)
		assert.False(t, entries[0].IsEpochAnchor)
	})

	t.Run("empty index slice skipped", func(t *testing.T) {
		ss := subStep{epoch: "e0", tailPCSGReplicaIndices: map[string][]int32{"prefill": {}}}
		entries := planner.newHashEntriesForSubStep(ss)
		assert.Empty(t, entries)
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

func TestNext(t *testing.T) {
	t.Run("fully rolled yields nil sub-step", func(t *testing.T) {
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 2}, map[string]int32{"prefill": 1}, map[string]int32{"prefill": 1})
		planner.entries = []grovecorev1alpha1.PodGangEntry{
			testutils.NewPodGangEntryBuilder("a", testHash, "e0").WithEpochAnchor(true).WithPodCliques(nil).WithPCSGReplicaIndices(map[string][]int32{"prefill": {0}}).Build(),
			testutils.NewPodGangEntryBuilder("b", testHash, "e1").WithEpochAnchor(false).WithPodCliques(nil).WithPCSGReplicaIndices(map[string][]int32{"prefill": {1}}).Build(),
		}
		progress := newProgress(anchorStep("e0"), leftoverStep("e1"))
		markAllReady(progress)

		ss, _, err := planner.next(progress)
		require.NoError(t, err)
		assert.Nil(t, ss)
	})

	t.Run("no step open yields anchor-bearing sub-step", func(t *testing.T) {
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 4}, map[string]int32{"prefill": 2}, map[string]int32{"prefill": 2})
		planner.clk = clocktesting.NewFakeClock(time.Unix(0, 1000))
		progress := &grovecorev1alpha1.PodGangMapUpdateProgress{}

		ss, progress, err := planner.next(progress)
		require.NoError(t, err)
		require.NotNil(t, ss)
		assert.NotEmpty(t, ss.anchorPCSGReplicaIndices)
		assert.True(t, progress.Steps[0].IsAnchorBearing)
	})

	t.Run("all anchor steps done, leftover remains yields leftover sub-step", func(t *testing.T) {
		// prefill replicas 10, minAvail 3, maxUnavail 2: steps=3, target=3, leftover=1.
		planner := plannerForReplicasMinAvail(t,
			map[string]int32{"prefill": 10}, map[string]int32{"prefill": 3}, map[string]int32{"prefill": 2})
		planner.clk = clocktesting.NewFakeClock(time.Unix(0, 1000))
		// Three anchor-bearing steps recorded and fully rolled (indices [0,9)).
		planner.entries = []grovecorev1alpha1.PodGangEntry{
			testutils.NewPodGangEntryBuilder("a0", testHash, "100").WithEpochAnchor(true).WithPodCliques(nil).WithPCSGReplicaIndices(map[string][]int32{"prefill": {0, 1, 2}}).Build(),
			testutils.NewPodGangEntryBuilder("a1", testHash, "200").WithEpochAnchor(true).WithPodCliques(nil).WithPCSGReplicaIndices(map[string][]int32{"prefill": {3, 4, 5}}).Build(),
			testutils.NewPodGangEntryBuilder("a2", testHash, "300").WithEpochAnchor(true).WithPodCliques(nil).WithPCSGReplicaIndices(map[string][]int32{"prefill": {6, 7, 8}}).Build(),
		}
		progress := newProgress(anchorStep("100"), anchorStep("200"), anchorStep("300"))
		markAllReady(progress)

		ss, progress, err := planner.next(progress)
		require.NoError(t, err)
		require.NotNil(t, ss)
		assert.Equal(t, []int32{9}, ss.tailPCSGReplicaIndices["prefill"])
		assert.False(t, progress.Steps[len(progress.Steps)-1].IsAnchorBearing)
	})
}

func TestBuildCoherentUpdateEntries(t *testing.T) {
	clk := clocktesting.NewFakeClock(time.Unix(0, 1000))

	t.Run("first reconcile pre-syncs from statuses then records the seeded epoch", func(t *testing.T) {
		pcs := pcsWithCoherentUpdateInProgress(testHash)
		pclq := standalonePCLQWithMapping("frontend", 0, map[string]int32{"pg-0": 2})
		pcsg := prefillPCSGWithMapping(0, map[string][]int32{"pg-0": {0}})
		// PGM has entries but no Status.UpdateProgress -> this is the first coherent reconcile. The
		// pre-sync rebuilds entries at the current hash, then syncProgressFromSpec records that epoch.
		pgm := testutils.NewPodGangMapBuilder(testPCSName, testNamespace, testPCSUID, 0).
			WithEntries(testutils.NewPodGangEntryBuilder("pg-0", "old-hash", "100").WithEpochAnchor(true).Build()).Build()
		snap := snapshotForCoherent(t, pcs, pclq, pcsg, pgm)

		entries, progress, err := buildCoherentUpdateEntries(0, snap, nil, clk)
		require.NoError(t, err)
		require.NotNil(t, progress, "progress is initialised")
		assert.Len(t, progress.Steps, 1, "the seeded new-hash epoch is recorded")
		assert.NotEmpty(t, entries, "entries rebuilt from owner statuses")
	})

	t.Run("gated when the latest batch is not yet Ready", func(t *testing.T) {
		pcs := pcsWithCoherentUpdateInProgress(testHash)
		pclq := standalonePCLQWithMapping("frontend", 0, map[string]int32{"pg-0": 2})
		pcsg := prefillPCSGWithMapping(0, map[string][]int32{"pg-0": {0}})
		// A new-hash anchor already emitted and recorded, but its batch is still InProgress.
		newAnchor := testutils.NewPodGangEntryBuilder("pg-new", testHash, "100").WithEpochAnchor(true).
			WithPodCliques(map[string]int32{"frontend": 2}).WithPCSGReplicaIndices(map[string][]int32{"prefill": {0}}).Build()
		pgm := testutils.NewPodGangMapBuilder(testPCSName, testNamespace, testPCSUID, 0).
			WithEntries(newAnchor).WithUpdateProgress(newProgress(anchorStep("100"))).Build()
		snap := snapshotForCoherent(t, pcs, pclq, pcsg, pgm)

		entries, progress, err := buildCoherentUpdateEntries(0, snap, nil, clk)
		require.NoError(t, err)
		require.Len(t, progress.Steps, 1, "no new step emitted while gated")
		assert.Equal(t, grovecorev1alpha1.PodGangMapUpdateStateInProgress, progress.Steps[0].SubSteps[0].State)
		assert.Len(t, entries, 1, "entries unchanged")
	})

	t.Run("advances by opening the first anchor-bearing step when ready", func(t *testing.T) {
		pcs := pcsWithCoherentUpdateInProgress(testHash)
		pclq := standalonePCLQWithMapping("frontend", 0, map[string]int32{"pg-0": 2})
		pcsg := prefillPCSGWithMapping(0, map[string][]int32{"pg-0": {0}})
		// All old-hash entries, empty progress -> latestBatchReady is vacuously true, so next opens
		// the first anchor-bearing step.
		oldAnchor := testutils.NewPodGangEntryBuilder("pg-0", "old-hash", "100").WithEpochAnchor(true).
			WithPodCliques(map[string]int32{"frontend": 2}).WithPCSGReplicaIndices(map[string][]int32{"prefill": {0}}).Build()
		pgm := testutils.NewPodGangMapBuilder(testPCSName, testNamespace, testPCSUID, 0).
			WithEntries(oldAnchor).WithUpdateProgress(&grovecorev1alpha1.PodGangMapUpdateProgress{}).Build()
		snap := snapshotForCoherent(t, pcs, pclq, pcsg, pgm)

		entries, progress, err := buildCoherentUpdateEntries(0, snap, nil, clk)
		require.NoError(t, err)
		require.Len(t, progress.Steps, 1)
		assert.True(t, progress.Steps[0].IsAnchorBearing, "a new anchor-bearing step is opened")
		// a new-hash anchor entry was appended.
		var newHashCount int
		for _, e := range entries {
			if e.PodCliqueSetGenerationHash == testHash {
				newHashCount++
			}
		}
		assert.Equal(t, 1, newHashCount, "one new-hash anchor emitted")
	})
}

// --- helpers (after all Test* functions per the package test convention) ---

// newProgress builds an UpdateProgress from the given steps.
func newProgress(steps ...grovecorev1alpha1.PodGangMapUpdateStep) *grovecorev1alpha1.PodGangMapUpdateProgress {
	return &grovecorev1alpha1.PodGangMapUpdateProgress{Steps: steps}
}

// anchorStep builds an anchor-bearing step whose sub-steps carry the given epochs, all InProgress.
func anchorStep(epochs ...string) grovecorev1alpha1.PodGangMapUpdateStep {
	return buildStep(true, epochs...)
}

// leftoverStep builds a non-anchor (leftover) step whose sub-steps carry the given epochs.
func leftoverStep(epochs ...string) grovecorev1alpha1.PodGangMapUpdateStep {
	return buildStep(false, epochs...)
}

func buildStep(anchorBearing bool, epochs ...string) grovecorev1alpha1.PodGangMapUpdateStep {
	subSteps := make([]grovecorev1alpha1.PodGangMapUpdateSubStep, 0, len(epochs))
	for _, e := range epochs {
		subSteps = append(subSteps, grovecorev1alpha1.PodGangMapUpdateSubStep{Epoch: e, State: grovecorev1alpha1.PodGangMapUpdateStateInProgress})
	}
	return grovecorev1alpha1.PodGangMapUpdateStep{
		IsAnchorBearing: anchorBearing,
		State:           grovecorev1alpha1.PodGangMapUpdateStateInProgress,
		SubSteps:        subSteps,
	}
}

// markAllReady flips every sub-step and step in progress to Ready.
func markAllReady(progress *grovecorev1alpha1.PodGangMapUpdateProgress) {
	for i := range progress.Steps {
		progress.Steps[i].State = grovecorev1alpha1.PodGangMapUpdateStateReady
		for j := range progress.Steps[i].SubSteps {
			progress.Steps[i].SubSteps[j].State = grovecorev1alpha1.PodGangMapUpdateStateReady
		}
	}
}

// plannerForReplicasMinAvail builds a subStepPlanner directly with the given step-plan inputs,
// computing numAnchorBearingSteps/anchorBearingStepTarget/leftover the same way newSubStepPlanner
// does. It avoids constructing a syncSnapshot for the pure planner tests.
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

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

package podcliquesetreplica

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestReplicaScalingIndicesFromProgress(t *testing.T) {
	progress := &grovecorev1beta1.PodCliqueSetScalingProgress{
		ScaleOutReplicaIndices: []int32{1, 3},
		ScaleInReplicaIndices:  []int32{2, 4},
	}

	scalingIndices := replicaScalingIndicesFromProgress(progress)

	assert.Equal(t, pcsReplicaScalingIndices{scaleOut: []int{1, 3}, scaleIn: []int{2, 4}}, scalingIndices)
	assert.Equal(t, pcsReplicaScalingIndices{}, replicaScalingIndicesFromProgress(nil))
}

func TestScalingProgressFromScalingIndices(t *testing.T) {
	scalingIndices := pcsReplicaScalingIndices{
		scaleOut: []int{1, 3},
		scaleIn:  []int{2, 4},
	}

	assert.Equal(t, &grovecorev1beta1.PodCliqueSetScalingProgress{
		ScaleOutReplicaIndices: []int32{1, 3},
		ScaleInReplicaIndices:  []int32{2, 4},
	}, scalingProgressFromScalingIndices(scalingIndices))
	assert.Nil(t, scalingProgressFromScalingIndices(pcsReplicaScalingIndices{}))
}

func TestRecordScalingSelectionEvents(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-uid")).Build()
	eventRecorder := record.NewFakeRecorder(10)
	r := &_resource{eventRecorder: eventRecorder}

	r.recordNewScalingSelectionEvents(
		logr.Discard(),
		pcs,
		pcsReplicaScalingIndices{scaleOut: []int{0}, scaleIn: []int{5}},
		pcsReplicaScalingIndices{
			scaleOut: []int{0, 2, 4},
			scaleIn:  []int{1, 3, 5},
		},
	)

	assert.Equal(t, []string{
		"Normal PodCliqueSetReplicaIndicesReservedForScaleOut Reserved PCS replica indices [2 4] for scale-out",
		"Normal PodCliqueSetReplicaIndicesSelectedForScaleIn Selected PCS replica indices [1 3] for scale-in",
	}, []string{<-eventRecorder.Events, <-eventRecorder.Events})
}

func TestScaleInMaySelectMaterializedCurrentGenerationReplica(t *testing.T) {
	pcs := replicaRecreationSelectionTestPCS()
	info := replicaRecreationSelectionInfo(pcs, 1, "current", true, true)
	assert.True(t, info.hasMaterializedPCSGeneration(pcs, "current"))
	selected := selectReplicaIndicesForScaleIn(pcs, pcsReplicaInfoByIndex{1: info}, sets.New(1), sets.New[int](), sets.New[int](), 1)
	assert.Equal(t, []int{1}, selected, "current template marker must not protect excess replicas from scale-in")
}

func TestSelectReplicaIndicesForScaleIn(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-pcs-uid")).
		WithStandaloneClique("worker").
		Build()
	insufficientAvailablePCLQ := func(replicaIndex int) grovecorev1beta1.PodClique {
		pclq := pclqWithAvailability(pcs, replicaIndex, false)
		pclq.Status.ScheduledReplicas = *pclq.Spec.MinAvailable
		return pclq
	}
	replicaInfos := pcsReplicaInfoByIndex{
		0: {index: 0, standalonePCLQs: []grovecorev1beta1.PodClique{pclqWithAvailability(pcs, 0, false)}},
		2: {index: 2, standalonePCLQs: []grovecorev1beta1.PodClique{pclqWithAvailability(pcs, 2, true)}},
		3: {index: 3, standalonePCLQs: []grovecorev1beta1.PodClique{insufficientAvailablePCLQ(3)}},
		5: {index: 5, standalonePCLQs: []grovecorev1beta1.PodClique{pclqWithAvailability(pcs, 5, false)}},
		6: {index: 6, standalonePCLQs: []grovecorev1beta1.PodClique{insufficientAvailablePCLQ(6)}},
		7: {index: 7, standalonePCLQs: []grovecorev1beta1.PodClique{pclqWithAvailability(pcs, 7, true)}},
	}
	projectedReplicaIndices := sets.New(0, 1, 2, 3, 4, 5, 6, 7)
	scaleOutReplicaIndices := sets.New(1, 4)

	tests := []struct {
		name  string
		count int
		want  []int
	}{
		{
			name:  "selects by scheduled capacity availability scale-out progress and descending index",
			count: 8,
			want:  []int{5, 0, 6, 3, 4, 1, 7, 2},
		},
		{
			name:  "selects only requested number of indices",
			count: 3,
			want:  []int{5, 0, 6},
		},
		{
			name: "selects no indices for zero count",
		},
		{
			name:  "caps selection at number of desired indices",
			count: 10,
			want:  []int{5, 0, 6, 3, 4, 1, 7, 2},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := selectReplicaIndicesForScaleIn(
				pcs,
				replicaInfos,
				projectedReplicaIndices,
				scaleOutReplicaIndices,
				sets.New[int](),
				test.count,
			)
			assert.Equal(t, test.want, got)
			assert.Equal(t, sets.New(0, 1, 2, 3, 4, 5, 6, 7), projectedReplicaIndices)
			assert.Equal(t, sets.New(1, 4), scaleOutReplicaIndices)
		})
	}

	t.Run("preserves assigned replica that is warming over unassigned replica", func(t *testing.T) {
		warmingPCLQ := pclqWithAvailability(pcs, 4, false)
		warmingPCLQ.Status.ScheduledReplicas = *warmingPCLQ.Spec.MinAvailable
		replicaInfos := pcsReplicaInfoByIndex{
			1: {index: 1, standalonePCLQs: []grovecorev1beta1.PodClique{pclqWithAvailability(pcs, 1, false)}},
			4: {index: 4, standalonePCLQs: []grovecorev1beta1.PodClique{warmingPCLQ}},
		}

		got := selectReplicaIndicesForScaleIn(pcs, replicaInfos, sets.New(1, 4), sets.New[int](), sets.New[int](), 1)

		assert.Equal(t, []int{1}, got)
	})
}

func TestSelectReplicaIndicesForScaleInRanksReplacementTargetsAndConvergedReplicasLast(t *testing.T) {
	pcs := rollingUpdateTestPCS()

	t.Run("ranks a replacement target in scale-out below a non-target in scale-out", func(t *testing.T) {
		replicaInfos := pcsReplicaInfoByIndex{
			2: rollingUpdateReplicaInfo(pcs, 2, false),
			3: rollingUpdateReplicaInfo(pcs, 3, false),
		}
		committedPCSReplicaIndices := sets.New(2, 3)
		scaleOut := sets.New(2, 3)
		targets := sets.New(3)

		got := selectReplicaIndicesForScaleIn(pcs, replicaInfos, committedPCSReplicaIndices, scaleOut, targets, 2)

		// Both are in scale-out, so the non-target (2) precedes the target (3).
		assert.Equal(t, []int{2, 3}, got)
	})

	t.Run("ranks an available converged replica below an available unconverged one", func(t *testing.T) {
		replicaInfos := pcsReplicaInfoByIndex{
			0: rollingUpdateReplicaInfo(pcs, 0, false), // available, not converged (old spec)
			1: rollingUpdateReplicaInfo(pcs, 1, true),  // available, converged (new spec)
		}
		committedPCSReplicaIndices := sets.New(0, 1)

		got := selectReplicaIndicesForScaleIn(pcs, replicaInfos, committedPCSReplicaIndices, sets.New[int](), sets.New[int](), 2)

		// Old-spec available (0) precedes new-spec available (1).
		assert.Equal(t, []int{0, 1}, got)
	})
}

func TestComputePCSReplicaScalingWorkBeforeFirstCompletedReconcile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		desiredReplicas int32
		observedIndices []int
		scalingIndices  pcsReplicaScalingIndices
		progress        []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress
		wantScaleIn     []int
		wantScaleOut    []int
		wantCanceled    []int
	}{
		{name: "fresh creation is not ordinary scale-out", desiredReplicas: 3},
		{name: "partial creation is not ordinary scale-out", desiredReplicas: 3, observedIndices: []int{0}},
		{name: "existing absent scale-out remains recorded", desiredReplicas: 1, scalingIndices: pcsReplicaScalingIndices{scaleOut: []int{2}}, wantScaleOut: []int{2}},
		{name: "pending live scale-in remains recorded", desiredReplicas: 1, observedIndices: []int{2}, scalingIndices: pcsReplicaScalingIndices{scaleIn: []int{2}}, wantScaleIn: []int{2}},
		{name: "absent scale-in waits for participant confirmation", scalingIndices: pcsReplicaScalingIndices{scaleIn: []int{2}}, wantScaleIn: []int{2}},
		{name: "zero desired replicas removes ordinary live capacity", observedIndices: []int{0, 2}, wantScaleIn: []int{0, 2}},
		{
			name:         "zero desired replicas cancels absent same-index target",
			progress:     []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0}}},
			wantCanceled: []int{0},
		},
		{
			name:         "zero desired replicas cancels absent different-index target",
			progress:     []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 2, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1}}},
			wantCanceled: []int{2},
		},
		{
			name: "zero desired replicas cancels live target and removes unrelated capacity", observedIndices: []int{0, 1},
			progress:     []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0}}},
			wantCanceled: []int{0}, wantScaleIn: []int{1},
		},
		{
			name: "positive scale-in preserves active target over unavailable old source", desiredReplicas: 1, observedIndices: []int{0, 1},
			progress:    []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 1, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1}}},
			wantScaleIn: []int{0},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
				WithReplicas(tc.desiredReplicas).
				WithStandaloneClique("worker").
				WithPodCliqueSetGenerationHash(new("current")).
				WithUpdateProgress((&grovecorev1beta1.PodCliqueSetUpdateProgress{RequiresReplicaRecreation: true, CurrentlyUpdating: tc.progress}).DeepCopy()).Build()
			groups := groupPCSReplicaReplacements(pcs.Status.UpdateProgress)
			activeTargets := activeReplacementTargetToSource(groups.active)
			replicaInfos := make(pcsReplicaInfoByIndex, len(tc.observedIndices))
			for _, pcsReplicaIndex := range tc.observedIndices {
				generationHash := "old"
				if _, isActiveTarget := activeTargets[pcsReplicaIndex]; isActiveTarget {
					generationHash = "current"
				}
				pclq := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, "worker", pcs.Namespace, int32(pcsReplicaIndex)).
					WithOptions(testutils.WithPCLQCurrentPCSGenerationHash(generationHash)).Build()
				replicaInfos[pcsReplicaIndex] = pcsReplicaInfo{index: pcsReplicaIndex, standalonePCLQs: []grovecorev1beta1.PodClique{*pclq}}
			}
			before := pcs.DeepCopy()

			work := computePCSReplicaScalingWork(pcs, replicaInfos, tc.scalingIndices, groups)

			assert.Equal(t, tc.wantScaleIn, work.scalingIndices.scaleIn)
			assert.Equal(t, tc.wantScaleOut, work.scalingIndices.scaleOut)
			assert.Equal(t, tc.wantCanceled, work.replacementSourcesToCancel)
			assert.Equal(t, before, pcs, "computing scaling must not acknowledge the spec or mutate replacement progress")
		})
	}
}

func TestComputePCSReplicaScalingWorkDuringRecreation(t *testing.T) {
	tests := []struct {
		name            string
		generationHash  string
		observedPCLQs   int
		available       bool
		scheduled       bool
		targetAvailable bool
		wantScaleIn     []int
		wantCanceled    []int
	}{
		{
			name:           "preserves replacement when unavailable old replica is unscheduled",
			generationHash: "old",
			observedPCLQs:  2,
			wantScaleIn:    []int{2},
		},
		{
			name:           "preserves replacement when unavailable old replica is scheduled",
			generationHash: "old",
			observedPCLQs:  2,
			scheduled:      true,
			wantScaleIn:    []int{2},
		},
		{
			name:           "missing child does not hide an existing old-generation child",
			generationHash: "old",
			observedPCLQs:  1,
			wantScaleIn:    []int{2},
		},
		{
			name:           "cancels replacement to preserve available old replica",
			generationHash: "old",
			observedPCLQs:  2,
			available:      true,
			wantCanceled:   []int{0},
		},
		{
			name:           "cancels replacement to preserve available current replica",
			generationHash: "current",
			observedPCLQs:  2,
			available:      true,
			wantCanceled:   []int{0},
		},
		{
			name:           "unavailable current replica does not get pending-recreation preference",
			generationHash: "current",
			observedPCLQs:  2,
			wantCanceled:   []int{0},
		},
		{
			name:           "scheduled unavailable current replica does not get pending-recreation preference",
			generationHash: "current",
			observedPCLQs:  2,
			scheduled:      true,
			wantCanceled:   []int{0},
		},
		{
			name:           "missing child with only current-generation children does not imply pending recreation",
			generationHash: "current",
			observedPCLQs:  1,
			wantCanceled:   []int{0},
		},
		{
			name:           "no observed children does not imply pending old-generation recreation",
			generationHash: "current",
			wantCanceled:   []int{0},
		},
		{
			name:            "preserves an available replacement target over an unavailable current replica",
			generationHash:  "current",
			observedPCLQs:   2,
			targetAvailable: true,
			wantScaleIn:     []int{2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-pcs-uid")).
				WithStandaloneClique("worker").
				WithStandaloneClique("metrics").
				WithPodCliqueSetGenerationHash(new("current")).
				WithReplicas(1).
				Build()
			pcs.Status.ObservedGeneration = new(pcs.Generation)
			pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
				RequiresReplicaRecreation: true,
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
					ReplicaIndex: 0,
					Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0},
				}},
			}
			replicaInfos := pcsReplicaInfoByIndex{0: {index: 0}, 2: {index: 2}}
			for replicaIndex, replicaInfo := range replicaInfos {
				numPCLQs := tt.observedPCLQs
				generationHash := tt.generationHash
				available := tt.available
				if replicaIndex == 0 {
					numPCLQs = 2
					generationHash = "current"
					available = tt.targetAvailable
				}
				for i := range numPCLQs {
					pclq := pclqWithAvailability(pcs, replicaIndex, available)
					pclq.Name = fmt.Sprintf("%s-%d-%s", pcs.Name, replicaIndex, pcs.Spec.Template.Cliques[i].Name)
					pclq.Status.CurrentPodCliqueSetGenerationHash = &generationHash
					if available || (replicaIndex == 2 && tt.scheduled) {
						pclq.Status.ScheduledReplicas = *pclq.Spec.MinAvailable
					}
					replicaInfo.standalonePCLQs = append(replicaInfo.standalonePCLQs, pclq)
				}
				replicaInfos[replicaIndex] = replicaInfo
			}

			// The active same-index replacement has no persisted scale-out reservation.
			work := computePCSReplicaScalingWork(pcs, replicaInfos, pcsReplicaScalingIndices{}, groupPCSReplicaReplacements(pcs.Status.UpdateProgress))

			assert.Equal(t, tt.wantScaleIn, work.scalingIndices.scaleIn)
			assert.Equal(t, tt.wantCanceled, work.replacementSourcesToCancel)
		})
	}
}

func TestComputePCSReplicaScalingWorkCancelsReplacementWhenScaleInSelectsTarget(t *testing.T) {
	pcs := rollingUpdateTestPCS()
	pcs.Status.ObservedGeneration = new(pcs.Generation)
	pcs.Spec.Replicas = 1
	pcs.Status.UpdateProgress.RequiresReplicaRecreation = true
	pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
		{ReplicaIndex: 0, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 2}},
	}
	// Source 0 and target 2 exist. The active replacement commits target 2 instead of source 0, so the
	// committed indices are {1, 2}. With desired 1, one unit must be reclaimed. Target 2 is in scale-out
	// and ranks after the non-target 1, but 1
	// is available (converged) while 2 is an unconverged scale-out target, so 2 is selected first and canceled.
	replicaInfos := pcsReplicaInfoByIndex{
		0: rollingUpdateReplicaInfo(pcs, 0, true),
		1: rollingUpdateReplicaInfo(pcs, 1, true),
		2: rollingUpdateReplicaInfo(pcs, 2, false),
	}
	replacementGroups := groupPCSReplicaReplacements(pcs.Status.UpdateProgress)

	work := computePCSReplicaScalingWork(pcs, replicaInfos, pcsReplicaScalingIndices{scaleOut: []int{2}}, replacementGroups)

	assert.Equal(t, []int{0}, work.replacementSourcesToCancel)
	assert.Empty(t, work.scalingIndices.scaleIn)
}

func TestComputePCSReplicaScalingWorkScalesInNonTarget(t *testing.T) {
	pcs := rollingUpdateTestPCS()
	pcs.Status.ObservedGeneration = new(pcs.Generation)
	pcs.Spec.Replicas = 1
	replicaInfos := pcsReplicaInfoByIndex{
		0: rollingUpdateReplicaInfo(pcs, 0, true),
		1: rollingUpdateReplicaInfo(pcs, 1, true),
	}

	work := computePCSReplicaScalingWork(pcs, replicaInfos, pcsReplicaScalingIndices{}, pcsReplicaReplacementGroups{})

	assert.Equal(t, []int{1}, work.scalingIndices.scaleIn)
	assert.Empty(t, work.replacementSourcesToCancel)
}

// TestComputeReservedReplicaIndicesReservesReplacementIndices covers allocation exclusion for observed
// resources, scaling reservations, and active or canceled replacements, including their retired sources.
func TestComputeReservedReplicaIndicesReservesReplacementIndices(t *testing.T) {
	replicaInfos := pcsReplicaInfoByIndex{0: {}}
	replacementGroups := pcsReplicaReplacementGroups{
		active: []pcsReplicaReplacement{{
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 2, retiredSourcePCSReplicaIndices: []int{7},
		}},
		canceled: []pcsReplicaReplacement{{
			sourcePCSReplicaIndex: 3, targetPCSReplicaIndex: 4, retiredSourcePCSReplicaIndices: []int{8},
		}},
	}

	reservedPCSReplicaIndices := computeReservedReplicaIndices(replicaInfos, sets.New(5), sets.New(6), replacementGroups)

	assert.Equal(t, sets.New(0, 1, 2, 3, 4, 5, 6, 7, 8), reservedPCSReplicaIndices)
}

// TestComputePCSReplicaScalingWorkKeepsRetiredCleanupSeparate verifies that retired sources do not become
// ordinary scale-in work or contribute capacity, and their pending cleanup prevents scale-out index reuse.
func TestComputePCSReplicaScalingWorkKeepsRetiredCleanupSeparate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                           string
		existingPCSReplicaIndices      sets.Set[int]
		desiredPCSReplicas             int32
		retiredSourcePCSReplicaIndices []int32
		isCanceled                     bool
		want                           pcsReplicaScalingWork
	}{
		{
			name:                           "observed retired resources do not trigger ordinary scale-in",
			existingPCSReplicaIndices:      sets.New(0, 1, 2),
			desiredPCSReplicas:             2,
			retiredSourcePCSReplicaIndices: []int32{2},
		},
		{
			name:                           "scale-out skips an absent active retired source",
			existingPCSReplicaIndices:      sets.New(0, 1),
			desiredPCSReplicas:             3,
			retiredSourcePCSReplicaIndices: []int32{2},
			want: pcsReplicaScalingWork{scalingIndices: pcsReplicaScalingIndices{
				scaleOut: []int{3},
			}},
		},
		{
			name:                           "scale-out skips absent canceled and retired indices",
			existingPCSReplicaIndices:      sets.New(0),
			desiredPCSReplicas:             2,
			retiredSourcePCSReplicaIndices: []int32{2},
			isCanceled:                     true,
			want: pcsReplicaScalingWork{scalingIndices: pcsReplicaScalingIndices{
				scaleOut: []int{3},
			}},
		},
		{
			name:                           "absent retired index zero remains unavailable for scale-out",
			existingPCSReplicaIndices:      sets.New(1),
			desiredPCSReplicas:             2,
			retiredSourcePCSReplicaIndices: []int32{0},
			want: pcsReplicaScalingWork{scalingIndices: pcsReplicaScalingIndices{
				scaleOut: []int{2},
			}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			replacementProgress := &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex:          1,
				RetiredSourceReplicaIndices: tc.retiredSourcePCSReplicaIndices,
			}
			if tc.isCanceled {
				canceledAt := metav1.NewTime(time.Unix(150, 0).UTC())
				replacementProgress.CanceledAt = &canceledAt
			}
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "uid").
				WithReplicas(tc.desiredPCSReplicas).
				WithStandaloneClique("worker").
				WithPodCliqueSetGenerationHash(new("current")).
				WithObservedGeneration(new(int64(0))).
				WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
					RequiresReplicaRecreation: true,
					CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
						ReplicaIndex: 1, Replacement: replacementProgress,
					}},
				}).Build()
			replicaInfos := make(pcsReplicaInfoByIndex, tc.existingPCSReplicaIndices.Len())
			for pcsReplicaIndex := range tc.existingPCSReplicaIndices {
				replicaInfos[pcsReplicaIndex] = pcsReplicaInfo{index: pcsReplicaIndex}
			}
			before := pcs.DeepCopy()

			got := computePCSReplicaScalingWork(pcs, replicaInfos, pcsReplicaScalingIndices{}, groupPCSReplicaReplacements(pcs.Status.UpdateProgress))

			assert.Equal(t, tc.want, got)
			assert.Equal(t, before, pcs, "scaling decisions must not mutate replacement cleanup obligations")
		})
	}
}

func TestFindScaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-pcs-uid")).
		WithStandaloneClique("router").
		WithStandaloneClique("metrics").
		WithScalingGroup("workers", []string{"worker"}).
		Build()

	pclq := func(replicaIndex int, cliqueName string) grovecorev1beta1.PodClique {
		return *testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, cliqueName, pcs.Namespace, int32(replicaIndex)).Build()
	}
	pcsg := func(replicaIndex int, groupName string) grovecorev1beta1.PodCliqueScalingGroup {
		name := apicommon.GeneratePodCliqueScalingGroupName(
			apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replicaIndex},
			groupName,
		)
		return *testutils.NewPodCliqueScalingGroupBuilder(name, pcs.Namespace, pcs.Name, replicaIndex).Build()
	}

	tests := []struct {
		name                   string
		scaleOutReplicaIndices []int
		pclqsByReplica         map[int][]grovecorev1beta1.PodClique
		pcsgsByReplica         map[int][]grovecorev1beta1.PodCliqueScalingGroup
		want                   []int
	}{
		{
			name: "no recorded scale-out indices",
		},
		{
			name:                   "recorded index has no resources",
			scaleOutReplicaIndices: []int{1},
		},
		{
			name:                   "recorded index is missing a standalone PodClique",
			scaleOutReplicaIndices: []int{1},
			pclqsByReplica: map[int][]grovecorev1beta1.PodClique{
				1: {pclq(1, "router")},
			},
			pcsgsByReplica: map[int][]grovecorev1beta1.PodCliqueScalingGroup{
				1: {pcsg(1, "workers")},
			},
		},
		{
			name:                   "recorded index is missing a PodCliqueScalingGroup",
			scaleOutReplicaIndices: []int{1},
			pclqsByReplica: map[int][]grovecorev1beta1.PodClique{
				1: {pclq(1, "router"), pclq(1, "metrics")},
			},
		},
		{
			name:                   "recorded index has every expected resource",
			scaleOutReplicaIndices: []int{1},
			pclqsByReplica: map[int][]grovecorev1beta1.PodClique{
				1: {pclq(1, "router"), pclq(1, "metrics")},
			},
			pcsgsByReplica: map[int][]grovecorev1beta1.PodCliqueScalingGroup{
				1: {pcsg(1, "workers")},
			},
			want: []int{1},
		},
		{
			name:                   "unexpected resources do not replace missing expected resources",
			scaleOutReplicaIndices: []int{1},
			pclqsByReplica: map[int][]grovecorev1beta1.PodClique{
				1: {pclq(1, "router"), pclq(1, "unexpected")},
			},
			pcsgsByReplica: map[int][]grovecorev1beta1.PodCliqueScalingGroup{
				1: {pcsg(1, "workers")},
			},
		},
		{
			name:                   "indices materialize independently and return in ascending order",
			scaleOutReplicaIndices: []int{3, 1, 2},
			pclqsByReplica: map[int][]grovecorev1beta1.PodClique{
				1: {pclq(1, "router"), pclq(1, "metrics")},
				2: {pclq(2, "router")},
				3: {pclq(3, "router"), pclq(3, "metrics")},
			},
			pcsgsByReplica: map[int][]grovecorev1beta1.PodCliqueScalingGroup{
				1: {pcsg(1, "workers")},
				2: {pcsg(2, "workers")},
				3: {pcsg(3, "workers")},
			},
			want: []int{1, 3},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := findScaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs(
				pcs,
				test.scaleOutReplicaIndices,
				replicaInfosFromResources(test.pclqsByReplica, test.pcsgsByReplica),
			)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestGatherPCSReplicaInfosByIndex(t *testing.T) {
	const (
		pcsName      = "test-pcs"
		otherPCSName = "other-pcs"
		namespace    = "default"
	)
	pcsUID := types.UID("test-pcs-uid")
	pcs := testutils.NewPodCliqueSetBuilder(pcsName, namespace, pcsUID).Build()

	standalonePCLQ0 := testutils.NewPodCliqueBuilder(pcsName, pcsUID, "standalone-a", namespace, 0).Build()
	standalonePCLQ2 := testutils.NewPodCliqueBuilder(pcsName, pcsUID, "standalone-b", namespace, 2).Build()
	pcsg0 := testutils.NewPodCliqueScalingGroupBuilder("test-pcs-0-group", namespace, pcsName, 0).
		WithOwnerReference(apiconstants.KindPodCliqueSet, pcsName, pcsUID).
		Build()
	pcsg2 := testutils.NewPodCliqueScalingGroupBuilder("test-pcs-2-group", namespace, pcsName, 2).
		WithOwnerReference(apiconstants.KindPodCliqueSet, pcsName, pcsUID).
		Build()

	pcsgOwnedPCLQ := testutils.NewPCSGPodCliqueBuilder("test-pcs-0-group-0-member", namespace, pcsName, pcsg0.Name, 0, 0).Build()
	otherPCSUID := types.UID("other-pcs-uid")
	otherPCSPCLQ := testutils.NewPodCliqueBuilder(pcsName, otherPCSUID, "stale", namespace, 4).Build()
	otherPCSG := testutils.NewPodCliqueScalingGroupBuilder("test-pcs-4-group", namespace, pcsName, 4).
		WithOwnerReference(apiconstants.KindPodCliqueSet, otherPCSName, otherPCSUID).
		Build()
	headlessService2 := headlessServiceForPCSReplica(t, pcs, 2)

	r := _resource{client: newScalingTestClient(t,
		standalonePCLQ0,
		standalonePCLQ2,
		pcsg0,
		pcsg2,
		pcsgOwnedPCLQ,
		otherPCSPCLQ,
		otherPCSG,
		headlessService2,
	)}

	replicaInfos, err := r.gatherPCSReplicaInfosByIndex(context.Background(), pcs)
	require.NoError(t, err)

	require.Equal(t, []int{0, 2}, sortedMapKeys(replicaInfos))
	assert.Equal(t, standalonePCLQ0.Name, replicaInfos[0].standalonePCLQs[0].Name)
	assert.Equal(t, standalonePCLQ2.Name, replicaInfos[2].standalonePCLQs[0].Name)
	assert.Equal(t, pcsg0.Name, replicaInfos[0].pcsgs[0].Name)
	assert.Equal(t, pcsg2.Name, replicaInfos[2].pcsgs[0].Name)
	assert.Equal(t, headlessService2.Name, replicaInfos[2].headlessSvcs[0].Name)
}

func TestGatherPCSReplicaInfosByIndexIncludesPodGangMapEntriesForCoherentUpdate(t *testing.T) {
	pcs := coherentTestPCS([]string{"frontend"}, nil)
	podGangMapEntries := []grovecorev1beta1.PodGangEntry{{
		Epoch:                      "200",
		PodCliqueSetGenerationHash: coherentTestCurrentGen,
	}}
	podGangMap := testutils.NewPodGangMapBuilder(pcs.Name, pcs.Namespace, pcs.UID, 1).
		WithEntries(podGangMapEntries...).
		Build()
	r := _resource{client: newScalingTestClient(t, podGangMap)}

	pcsReplicaInfosByIndex, err := r.gatherPCSReplicaInfosByIndex(context.Background(), pcs)

	require.NoError(t, err)
	require.Contains(t, pcsReplicaInfosByIndex, 1)
	assert.Equal(t, podGangMapEntries, pcsReplicaInfosByIndex[1].pgmEntries)
}

func TestGatherPCSReplicaInfosByIndexIgnoresPodGangMapsOutsideCoherentUpdate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-pcs-uid")).Build()
	podGangMap := testutils.NewPodGangMapBuilder(pcs.Name, pcs.Namespace, pcs.UID, 1).
		WithEntries(grovecorev1beta1.PodGangEntry{Epoch: "200"}).
		Build()
	r := _resource{client: newScalingTestClient(t, podGangMap)}

	pcsReplicaInfosByIndex, err := r.gatherPCSReplicaInfosByIndex(context.Background(), pcs)

	require.NoError(t, err)
	assert.Empty(t, pcsReplicaInfosByIndex)
}

func TestGatherPCSReplicaInfosByIndexReturnsEmptyMaps(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-pcs-uid")).Build()
	r := _resource{client: newScalingTestClient(t)}

	replicaInfos, err := r.gatherPCSReplicaInfosByIndex(context.Background(), pcs)
	require.NoError(t, err)
	assert.Empty(t, replicaInfos)
}

func TestGatherPCSReplicaInfosByIndexRejectsInvalidPCLQReplicaIndex(t *testing.T) {
	pcsUID := types.UID("test-pcs-uid")
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", pcsUID).Build()
	pclq := testutils.NewPodCliqueBuilder(pcs.Name, pcsUID, "standalone", pcs.Namespace, 0).
		WithLabels(map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "invalid"}).
		Build()
	r := _resource{client: newScalingTestClient(t, pclq)}

	_, err := r.gatherPCSReplicaInfosByIndex(context.Background(), pcs)
	assertGroveErrorCode(t, err, errCodeListPCLQs)
}

func TestGatherPCSReplicaInfosByIndexRejectsInvalidPCSGReplicaIndex(t *testing.T) {
	pcsUID := types.UID("test-pcs-uid")
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", pcsUID).Build()
	pcsg := testutils.NewPodCliqueScalingGroupBuilder("test-pcs-group", pcs.Namespace, pcs.Name, 0).
		WithLabels(map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "invalid"}).
		WithOwnerReference(apiconstants.KindPodCliqueSet, pcs.Name, pcsUID).
		Build()
	r := _resource{client: newScalingTestClient(t, pcsg)}

	_, err := r.gatherPCSReplicaInfosByIndex(context.Background(), pcs)
	assertGroveErrorCode(t, err, errCodeListPCSGs)
}

func TestGatherPCSReplicaInfosByIndexRejectsInvalidPodGangMapReplicaIndex(t *testing.T) {
	pcs := coherentTestPCS([]string{"frontend"}, nil)
	podGangMap := testutils.NewPodGangMapBuilder(pcs.Name, pcs.Namespace, pcs.UID, 0).Build()
	podGangMap.Labels[apicommon.LabelPodCliqueSetReplicaIndex] = "invalid"
	r := _resource{client: newScalingTestClient(t, podGangMap)}

	_, err := r.gatherPCSReplicaInfosByIndex(context.Background(), pcs)

	assertGroveErrorCode(t, err, errCodeGetPodGangMap)
}

func TestGatherPCSReplicaInfosByIndexWrapsPodGangMapListError(t *testing.T) {
	pcs := coherentTestPCS([]string{"frontend"}, nil)
	r := _resource{
		client: &failingListClient{
			Client:       newScalingTestClient(t),
			failedObject: &grovecorev1beta1.PodGangMapList{},
		},
	}

	_, err := r.gatherPCSReplicaInfosByIndex(context.Background(), pcs)

	assertGroveErrorCode(t, err, errCodeGetPodGangMap)
}

func TestGatherPCSReplicaInfosByIndexWrapsListErrors(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-pcs-uid")).Build()
	tests := []struct {
		name      string
		failedGVK runtime.Object
		wantCode  grovecorev1beta1.ErrorCode
	}{
		{
			name:      "PodClique list",
			failedGVK: &grovecorev1beta1.PodCliqueList{},
			wantCode:  errCodeListPCLQs,
		},
		{
			name:      "PodCliqueScalingGroup list",
			failedGVK: &grovecorev1beta1.PodCliqueScalingGroupList{},
			wantCode:  errCodeListPCSGs,
		},
		{
			name:      "Service list",
			failedGVK: &corev1.ServiceList{},
			wantCode:  errCodeListServices,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := _resource{
				client: &failingListClient{
					Client:       newScalingTestClient(t),
					failedObject: test.failedGVK,
				},
			}

			_, err := r.gatherPCSReplicaInfosByIndex(context.Background(), pcs)
			assertGroveErrorCode(t, err, test.wantCode)
		})
	}
}

func headlessServiceForPCSReplica(t *testing.T, pcs *grovecorev1beta1.PodCliqueSet, pcsReplicaIndex int) *corev1.Service {
	t.Helper()
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", pcs.Name, pcsReplicaIndex),
			Namespace: pcs.Namespace,
			Labels: map[string]string{
				apicommon.LabelManagedByKey:             apicommon.LabelManagedByValue,
				apicommon.LabelPartOfKey:                pcs.Name,
				apicommon.LabelComponentKey:             apicommon.LabelComponentNamePodCliqueSetReplicaHeadlessService,
				apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(pcsReplicaIndex),
			},
		},
	}
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, controllerutil.SetControllerReference(pcs, service, scheme))
	return service
}

func pclqWithAvailability(pcs *grovecorev1beta1.PodCliqueSet, replicaIndex int, isAvailable bool) grovecorev1beta1.PodClique {
	pclq := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, "worker", pcs.Namespace, int32(replicaIndex)).Build()
	if isAvailable {
		pclq.Status.ReadyReplicas = *pclq.Spec.MinAvailable
	}
	return *pclq
}

func replicaInfoByIndexForIndexTests() pcsReplicaInfoByIndex {
	return pcsReplicaInfoByIndex{
		0: {index: 0, standalonePCLQs: []grovecorev1beta1.PodClique{{}}},
		1: {index: 1, standalonePCLQs: []grovecorev1beta1.PodClique{{}}, pcsgs: []grovecorev1beta1.PodCliqueScalingGroup{{}}},
		4: {index: 4, pcsgs: []grovecorev1beta1.PodCliqueScalingGroup{{}}},
	}
}

func replicaInfosFromResources(
	standalonePCLQsByReplicaIndex map[int][]grovecorev1beta1.PodClique,
	pcsgsByReplicaIndex map[int][]grovecorev1beta1.PodCliqueScalingGroup,
) pcsReplicaInfoByIndex {
	replicaIndices := sets.KeySet(standalonePCLQsByReplicaIndex)
	replicaIndices.Insert(sets.List(sets.KeySet(pcsgsByReplicaIndex))...)
	replicaInfos := make(pcsReplicaInfoByIndex, replicaIndices.Len())
	for replicaIndex := range replicaIndices {
		replicaInfos[replicaIndex] = pcsReplicaInfo{
			index:           replicaIndex,
			standalonePCLQs: standalonePCLQsByReplicaIndex[replicaIndex],
			pcsgs:           pcsgsByReplicaIndex[replicaIndex],
		}
	}
	return replicaInfos
}

type failingListClient struct {
	client.Client
	failedObject runtime.Object
}

func (c *failingListClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if runtimeObjectTypesEqual(list, c.failedObject) {
		return errors.New("list failed")
	}
	return c.Client.List(ctx, list, opts...)
}

func newScalingTestClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func sortedMapKeys[V any](values map[int]V) []int {
	keys := make([]int, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func runtimeObjectTypesEqual(a, b runtime.Object) bool {
	return reflect.TypeOf(a) == reflect.TypeOf(b)
}

func assertGroveErrorCode(t *testing.T, err error, wantCode grovecorev1beta1.ErrorCode) {
	t.Helper()
	require.Error(t, err)
	var groveErr *groveerr.GroveError
	require.True(t, errors.As(err, &groveErr))
	assert.Equal(t, wantCode, groveErr.Code)
	assert.Equal(t, component.OperationSync, groveErr.Operation)
}

// TestPreparePCSReplicaScalingCancelsReadyReplacementBeforeCompletion verifies that cancellation takes precedence over
// completion when a replacement target is already ready and has no pending retired-source cleanup.
func TestPreparePCSReplicaScalingCancelsReadyReplacementBeforeCompletion(t *testing.T) {
	t.Parallel()
	cleanupCompletedAt := metav1.NewTime(time.Unix(100, 0).UTC())
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "test-uid").
		WithReplicas(0).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithMinAvailable(1).Build()).
		WithPodCliqueSetGenerationHash(new("generation")).
		WithAppliedSchedulingConfigurationHash(new("superseded")).
		WithScalingProgress(&grovecorev1beta1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{1}}).
		WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
			RequiresReplicaRecreation:         true,
			UpdateStartedAt:                   cleanupCompletedAt,
			TargetSchedulingConfigurationHash: "target-scheduling",
			CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex: 1,
				Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
					TargetReplicaIndex: 1, SourceCleanupCompletedAt: &cleanupCompletedAt,
				},
			}},
		}).Build()
	replicaInfos := pcsReplicaInfoByIndex{1: rollingUpdateReplicaInfo(pcs, 1, true)}
	hooks := newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, replicaScalingIndicesFromProgress(pcs.Status.ScalingProgress))
	require.True(t, hooks.isReplacementComplete(pcs.Status.UpdateProgress.CurrentlyUpdating[0]), "without cancellation this replacement would complete")

	reconcileTime := metav1.NewTime(time.Unix(200, 0).UTC())
	scalingIndices := preparePCSReplicaScaling(pcs, replicaInfos, reconcileTime)
	hooks = newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, scalingIndices)
	err := advancePCSUpdateProgress(pcs, hooks, reconcileTime)

	require.NoError(t, err)
	require.NotNil(t, pcs.Status.UpdateProgress)
	require.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1)
	progress := pcs.Status.UpdateProgress.CurrentlyUpdating[0]
	assert.Equal(t, int32(1), progress.ReplicaIndex)
	require.NotNil(t, progress.Replacement)
	assert.Equal(t, int32(1), progress.Replacement.TargetReplicaIndex)
	assert.NotNil(t, progress.Replacement.CanceledAt, "the ready replacement must be canceled rather than completed")
	assert.Nil(t, progress.UpdateEndedAt)
	assert.Nil(t, pcs.Status.UpdateProgress.UpdateEndedAt)
	require.NotNil(t, pcs.Status.AppliedSchedulingConfigurationHash)
	assert.Equal(t, "superseded", *pcs.Status.AppliedSchedulingConfigurationHash)
}

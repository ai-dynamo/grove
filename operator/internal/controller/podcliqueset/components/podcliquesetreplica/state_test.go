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
	"slices"
	"testing"
	"time"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/sync"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// TestComputePCSReplicaStateBeforeFirstCompletedReconcile covers initial provisioning and replacement
// overlays while the first completed reconciliation has not yet recorded ObservedGeneration.
func TestComputePCSReplicaStateBeforeFirstCompletedReconcile(t *testing.T) {
	t.Parallel()
	progressTimestamp := metav1.NewTime(time.Unix(150, 0).UTC())
	tests := []struct {
		name                      string
		desiredPCSReplicas        int32
		observedPCSReplicaIndices []int
		replacementProgress       *grovecorev1beta1.PodCliqueSetReplicaReplacementProgress
		sourcePCSReplicaIndex     int32
		replicaUpdateCompleted    bool
		pcsUpdateCompleted        bool
		want                      pcssync.State
	}{
		{name: "no resources still provisions all initial indices", desiredPCSReplicas: 3, want: pcssync.State{ExpectedReplicaIndices: []int{0, 1, 2}}},
		{name: "some resources still provisions all initial indices", desiredPCSReplicas: 3, observedPCSReplicaIndices: []int{0, 2}, want: pcssync.State{ExpectedReplicaIndices: []int{0, 1, 2}}},
		{name: "all resources preserves initial indices", desiredPCSReplicas: 3, observedPCSReplicaIndices: []int{0, 1, 2}, want: pcssync.State{ExpectedReplicaIndices: []int{0, 1, 2}}},
		{name: "zero desired replicas", want: pcssync.State{}},
		{
			name: "same index source cannot be recreated before cleanup", desiredPCSReplicas: 2, observedPCSReplicaIndices: []int{0, 1},
			replacementProgress: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0},
			want:                pcssync.State{ExpectedReplicaIndices: []int{1}, ScaleInReplicaIndices: []int{0}},
		},
		{
			name: "absent source still needs participant cleanup confirmation", desiredPCSReplicas: 2, observedPCSReplicaIndices: []int{1},
			replacementProgress: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0},
			want:                pcssync.State{ExpectedReplicaIndices: []int{1}, ScaleInReplicaIndices: []int{0}},
		},
		{
			name: "same index creation starts after source cleanup", desiredPCSReplicas: 2, observedPCSReplicaIndices: []int{1},
			replacementProgress: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0, SourceCleanupCompletedAt: &progressTimestamp},
			want:                pcssync.State{ExpectedReplicaIndices: []int{0, 1}, ScaleOutReplicaIndices: []int{0}},
		},
		{
			name: "different index source and target are disjoint", desiredPCSReplicas: 2, observedPCSReplicaIndices: []int{1}, sourcePCSReplicaIndex: 1,
			replacementProgress: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0},
			want:                pcssync.State{ExpectedReplicaIndices: []int{0}, ScaleOutReplicaIndices: []int{0}, ScaleInReplicaIndices: []int{1}},
		},
		{
			name: "canceled same index drains without recreation", desiredPCSReplicas: 2, observedPCSReplicaIndices: []int{0, 1},
			replacementProgress: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0, CanceledAt: &progressTimestamp},
			want:                pcssync.State{ExpectedReplicaIndices: []int{1}, ScaleInReplicaIndices: []int{0}},
		},
		{
			name: "canceled different index drains absent source and target", desiredPCSReplicas: 2, sourcePCSReplicaIndex: 1,
			replacementProgress: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0, CanceledAt: &progressTimestamp},
			want:                pcssync.State{ScaleInReplicaIndices: []int{0, 1}},
		},
		{
			name: "retired initial index stays cleanup only before barrier", desiredPCSReplicas: 3, observedPCSReplicaIndices: []int{0, 1, 2},
			replacementProgress: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0, RetiredSourceReplicaIndices: []int32{2}},
			want:                pcssync.State{ExpectedReplicaIndices: []int{1}, ScaleInReplicaIndices: []int{0, 2}},
		},
		{
			name: "retired initial index stays cleanup only after barrier", desiredPCSReplicas: 3, observedPCSReplicaIndices: []int{1},
			replacementProgress: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0, SourceCleanupCompletedAt: &progressTimestamp, RetiredSourceReplicaIndices: []int32{2}},
			want:                pcssync.State{ExpectedReplicaIndices: []int{0, 1}, ScaleOutReplicaIndices: []int{0}, ScaleInReplicaIndices: []int{2}},
		},
		{
			name: "canceled retirement outside initial range remains cleanup work", desiredPCSReplicas: 2, sourcePCSReplicaIndex: 2,
			replacementProgress: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1, CanceledAt: &progressTimestamp, RetiredSourceReplicaIndices: []int32{3}},
			want:                pcssync.State{ExpectedReplicaIndices: []int{0}, ScaleInReplicaIndices: []int{1, 2, 3}},
		},
		{
			name: "completed entry does not restart source cleanup", desiredPCSReplicas: 2, observedPCSReplicaIndices: []int{0, 1}, replicaUpdateCompleted: true,
			replacementProgress: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0, SourceCleanupCompletedAt: &progressTimestamp},
			want:                pcssync.State{ExpectedReplicaIndices: []int{0, 1}},
		},
		{
			name: "completed update before observed generation preserves initial provisioning", desiredPCSReplicas: 2, observedPCSReplicaIndices: []int{0}, pcsUpdateCompleted: true,
			replacementProgress: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0, SourceCleanupCompletedAt: &progressTimestamp},
			want:                pcssync.State{ExpectedReplicaIndices: []int{0, 1}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var updateProgress *grovecorev1beta1.PodCliqueSetUpdateProgress
			if tc.replacementProgress != nil {
				replicaUpdateProgress := grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{ReplicaIndex: tc.sourcePCSReplicaIndex, Replacement: tc.replacementProgress.DeepCopy()}
				if tc.replicaUpdateCompleted {
					replicaUpdateProgress.UpdateEndedAt = &progressTimestamp
				}
				updateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{RequiresReplicaRecreation: true, CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{replicaUpdateProgress}}
				if tc.pcsUpdateCompleted {
					updateProgress.UpdateEndedAt = &progressTimestamp
				}
			}
			pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
				WithReplicas(tc.desiredPCSReplicas).
				WithObservedGeneration(nil).
				WithPodCliqueSetGenerationHash(new("S1")).
				WithAppliedSchedulingConfigurationHash(new("S0-scheduling")).
				WithUpdateProgress(updateProgress).Build()
			before := pcs.DeepCopy()
			expected := expectedReplicaIndicesFromScaling(sets.New(tc.observedPCSReplicaIndices...), pcsReplicaScalingIndices{})
			expectedBefore := expected.Clone()
			replacementGroups := groupPCSReplicaReplacements(updateProgress)
			replacementGroupsBefore := groupPCSReplicaReplacements(updateProgress)

			got := computePCSReplicaState(pcs, pcsReplicaScalingIndices{}, expected, replacementGroups)

			assert.ElementsMatch(t, tc.want.ExpectedReplicaIndices, got.ExpectedReplicaIndices)
			assert.True(t, slices.IsSorted(got.ExpectedReplicaIndices), "expected indices must be ordered")
			assert.Equal(t, tc.want.ScaleOutReplicaIndices, got.ScaleOutReplicaIndices)
			assert.Equal(t, tc.want.ScaleInReplicaIndices, got.ScaleInReplicaIndices)
			assert.True(t, expectedBefore.Equal(expected), "state construction must not mutate caller's expected indices")
			assert.Equal(t, replacementGroupsBefore, replacementGroups, "state construction must not mutate replacement groups")
			assert.Equal(t, before, pcs, "state construction must not mutate persisted progress")
			assert.Nil(t, pcs.Status.ObservedGeneration, "state construction must not acknowledge reconciliation")
			assert.Empty(t, sets.New(got.ExpectedReplicaIndices...).Intersection(sets.New(got.ScaleInReplicaIndices...)), "expected and cleanup indices must remain disjoint")
			assert.Empty(t, sets.New(got.ScaleOutReplicaIndices...).Intersection(sets.New(got.ScaleInReplicaIndices...)), "creation and cleanup indices must remain disjoint")
		})
	}
}

func TestExpectedReplicaIndicesFromScaling(t *testing.T) {
	existing := sets.New(0, 2)
	scalingIndices := pcsReplicaScalingIndices{scaleOut: []int{1}, scaleIn: []int{2}}

	got := expectedReplicaIndicesFromScaling(existing, scalingIndices)

	assert.Equal(t, sets.New(0, 1), got)
	assert.Equal(t, sets.New(0, 2), existing, "input set must not be mutated")
}

func TestComputePCSReplicaStateReplacementOverlay(t *testing.T) {
	observedPCS := func() *grovecorev1beta1.PodCliqueSet {
		return &grovecorev1beta1.PodCliqueSet{
			Status: grovecorev1beta1.PodCliqueSetStatus{
				ObservedGeneration: new(int64(1)),
			},
		}
	}
	withReplacements := func(pcs *grovecorev1beta1.PodCliqueSet, entries ...grovecorev1beta1.PodCliqueSetReplicaUpdateProgress) *grovecorev1beta1.PodCliqueSet {
		pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
			RequiresReplicaRecreation: true,
			CurrentlyUpdating:         entries,
		}
		return pcs
	}

	t.Run("different-index active replacement moves source to scale-in and target to scale-out", func(t *testing.T) {
		pcs := withReplacements(observedPCS(), grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex: 2,
			Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
		})
		replicaInfos := pcsReplicaInfoByIndex{0: {index: 0}, 2: {index: 2}}
		expected := replicaInfos.existingReplicaIndices()

		got := computePCSReplicaState(pcs, pcsReplicaScalingIndices{}, expected, groupPCSReplicaReplacements(pcs.Status.UpdateProgress))

		assert.Equal(t, []int{0, 1}, got.ExpectedReplicaIndices)
		assert.Equal(t, []int{1}, got.ScaleOutReplicaIndices)
		assert.Equal(t, []int{2}, got.ScaleInReplicaIndices)
	})

	t.Run("same-index active replacement deletes before source cleanup completes", func(t *testing.T) {
		pcs := withReplacements(observedPCS(), grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex: 1,
			Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
		})
		replicaInfos := pcsReplicaInfoByIndex{0: {index: 0}, 1: {index: 1}}
		expected := replicaInfos.existingReplicaIndices()

		got := computePCSReplicaState(pcs, pcsReplicaScalingIndices{}, expected, groupPCSReplicaReplacements(pcs.Status.UpdateProgress))

		assert.Equal(t, []int{0}, got.ExpectedReplicaIndices)
		assert.Empty(t, got.ScaleOutReplicaIndices)
		assert.Equal(t, []int{1}, got.ScaleInReplicaIndices)
	})

	t.Run("same-index active replacement creates after source cleanup completes", func(t *testing.T) {
		cleanupTime := metav1.Now()
		pcs := withReplacements(observedPCS(), grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex: 1,
			Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex:       1,
				SourceCleanupCompletedAt: &cleanupTime,
			},
		})
		replicaInfos := pcsReplicaInfoByIndex{0: {index: 0}}
		expected := replicaInfos.existingReplicaIndices()

		got := computePCSReplicaState(pcs, pcsReplicaScalingIndices{}, expected, groupPCSReplicaReplacements(pcs.Status.UpdateProgress))

		assert.Equal(t, []int{0, 1}, got.ExpectedReplicaIndices)
		assert.Equal(t, []int{1}, got.ScaleOutReplicaIndices)
		assert.Empty(t, got.ScaleInReplicaIndices)
	})

	t.Run("canceled replacement drains present source and target through scale-in", func(t *testing.T) {
		canceledAt := metav1.Now()
		pcs := withReplacements(observedPCS(), grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex: 2,
			Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex: 1,
				CanceledAt:         &canceledAt,
			},
		})
		replicaInfos := pcsReplicaInfoByIndex{0: {index: 0}, 1: {index: 1}, 2: {index: 2}}
		expected := replicaInfos.existingReplicaIndices()

		got := computePCSReplicaState(pcs, pcsReplicaScalingIndices{}, expected, groupPCSReplicaReplacements(pcs.Status.UpdateProgress))

		assert.Equal(t, []int{0}, got.ExpectedReplicaIndices)
		assert.Empty(t, got.ScaleOutReplicaIndices)
		assert.Equal(t, []int{1, 2}, got.ScaleInReplicaIndices)
	})

	t.Run("canceled replacement keeps absent target in scale-in for cleanup confirmation", func(t *testing.T) {
		canceledAt := metav1.Now()
		pcs := withReplacements(observedPCS(), grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex: 2,
			Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex: 1,
				CanceledAt:         &canceledAt,
			},
		})
		replicaInfos := pcsReplicaInfoByIndex{0: {index: 0}, 2: {index: 2}}
		expected := replicaInfos.existingReplicaIndices()

		got := computePCSReplicaState(pcs, pcsReplicaScalingIndices{}, expected, groupPCSReplicaReplacements(pcs.Status.UpdateProgress))

		assert.Equal(t, []int{0}, got.ExpectedReplicaIndices)
		assert.Equal(t, []int{1, 2}, got.ScaleInReplicaIndices)
	})

	t.Run("canceled replacement keeps absent source and target in scale-in for cleanup confirmation", func(t *testing.T) {
		canceledAt := metav1.Now()
		pcs := withReplacements(observedPCS(), grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex: 2,
			Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex: 1,
				CanceledAt:         &canceledAt,
			},
		})
		replicaInfos := pcsReplicaInfoByIndex{0: {index: 0}}
		expected := replicaInfos.existingReplicaIndices()

		got := computePCSReplicaState(pcs, pcsReplicaScalingIndices{}, expected, groupPCSReplicaReplacements(pcs.Status.UpdateProgress))

		assert.Equal(t, []int{0}, got.ExpectedReplicaIndices)
		assert.Empty(t, got.ScaleOutReplicaIndices)
		assert.Equal(t, []int{1, 2}, got.ScaleInReplicaIndices)
	})

	t.Run("canceled same-index replacement keeps cleanup confirmation alongside an active replacement", func(t *testing.T) {
		canceledAt := metav1.Now()
		pcs := withReplacements(observedPCS(),
			grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				ReplicaIndex: 0,
				Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
					TargetReplicaIndex: 0,
					CanceledAt:         &canceledAt,
				},
			},
			grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				ReplicaIndex: 2,
				Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
			},
		)
		pcs.Spec.Replicas = 1
		replicaInfos := pcsReplicaInfoByIndex{1: {index: 1}}
		expected := replicaInfos.existingReplicaIndices()

		got := computePCSReplicaState(pcs, pcsReplicaScalingIndices{}, expected, groupPCSReplicaReplacements(pcs.Status.UpdateProgress))

		assert.Equal(t, []int{1}, got.ExpectedReplicaIndices)
		assert.Equal(t, []int{1}, got.ScaleOutReplicaIndices)
		assert.Equal(t, []int{0, 2}, got.ScaleInReplicaIndices)
	})

	t.Run("does not mutate the caller expected set", func(t *testing.T) {
		pcs := withReplacements(observedPCS(), grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex: 2,
			Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
		})
		replicaInfos := pcsReplicaInfoByIndex{0: {index: 0}, 2: {index: 2}}
		expected := replicaInfos.existingReplicaIndices()

		computePCSReplicaState(pcs, pcsReplicaScalingIndices{}, expected, groupPCSReplicaReplacements(pcs.Status.UpdateProgress))

		assert.Equal(t, sets.New(0, 2), expected)
	})
}

// TestComputePCSReplicaStateRetiredSourceCleanup covers cleanup-only projection before and after the source
// barrier, cancellation, absent resources, stale scale-out, and preservation of ordinary scaling selections.
func TestComputePCSReplicaStateRetiredSourceCleanup(t *testing.T) {
	t.Parallel()
	cleanupCompletedAt := metav1.NewTime(time.Unix(150, 0).UTC())
	canceledAt := metav1.NewTime(time.Unix(175, 0).UTC())
	tests := []struct {
		name                           string
		observedPCSReplicaIndices      sets.Set[int]
		scalingIndices                 pcsReplicaScalingIndices
		desiredPCSReplicas             int32
		sourcePCSReplicaIndex          int32
		targetPCSReplicaIndex          int32
		retiredSourcePCSReplicaIndices []int32
		sourceCleanupCompletedAt       *metav1.Time
		canceledAt                     *metav1.Time
		want                           pcssync.State
	}{
		{
			name:                      "nil retirement list preserves same-index cleanup",
			observedPCSReplicaIndices: sets.New(0, 1), desiredPCSReplicas: 2,
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1,
			want: pcssync.State{ExpectedReplicaIndices: []int{0}, ScaleInReplicaIndices: []int{1}},
		},
		{
			name:                      "empty retirement list preserves same-index cleanup",
			observedPCSReplicaIndices: sets.New(0, 1), desiredPCSReplicas: 2,
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1,
			retiredSourcePCSReplicaIndices: []int32{},
			want:                           pcssync.State{ExpectedReplicaIndices: []int{0}, ScaleInReplicaIndices: []int{1}},
		},
		{
			name:                      "same-index cleanup includes the retired source before the barrier",
			observedPCSReplicaIndices: sets.New(0, 1, 2), desiredPCSReplicas: 2,
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1,
			retiredSourcePCSReplicaIndices: []int32{2},
			want:                           pcssync.State{ExpectedReplicaIndices: []int{0}, ScaleInReplicaIndices: []int{1, 2}},
		},
		{
			name:                      "target creation proceeds alongside retired cleanup after the barrier",
			observedPCSReplicaIndices: sets.New(0, 1, 2), desiredPCSReplicas: 2,
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1,
			retiredSourcePCSReplicaIndices: []int32{2}, sourceCleanupCompletedAt: &cleanupCompletedAt,
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1}, ScaleOutReplicaIndices: []int{1}, ScaleInReplicaIndices: []int{2},
			},
		},
		{
			name:                      "different-index creation proceeds alongside current and retired source cleanup",
			observedPCSReplicaIndices: sets.New(0, 2, 3), desiredPCSReplicas: 2,
			sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1,
			retiredSourcePCSReplicaIndices: []int32{3},
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1}, ScaleOutReplicaIndices: []int{1}, ScaleInReplicaIndices: []int{2, 3},
			},
		},
		{
			name:                      "canceled same-index replacement drains its current and retired sources",
			observedPCSReplicaIndices: sets.New(0, 1, 2), desiredPCSReplicas: 1,
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1,
			retiredSourcePCSReplicaIndices: []int32{2}, canceledAt: &canceledAt,
			want: pcssync.State{ExpectedReplicaIndices: []int{0}, ScaleInReplicaIndices: []int{1, 2}},
		},
		{
			name:                      "canceled different-index replacement drains source target and retired sources",
			observedPCSReplicaIndices: sets.New(0, 1, 2, 3), desiredPCSReplicas: 1,
			sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1,
			retiredSourcePCSReplicaIndices: []int32{3}, canceledAt: &canceledAt,
			want: pcssync.State{ExpectedReplicaIndices: []int{0}, ScaleInReplicaIndices: []int{1, 2, 3}},
		},
		{
			name:                      "absent retired source still requires cleanup confirmation after the barrier",
			observedPCSReplicaIndices: sets.New(0), desiredPCSReplicas: 2,
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1,
			retiredSourcePCSReplicaIndices: []int32{2}, sourceCleanupCompletedAt: &cleanupCompletedAt,
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1}, ScaleOutReplicaIndices: []int{1}, ScaleInReplicaIndices: []int{2},
			},
		},
		{
			name:                      "absent canceled and retired indices all require cleanup confirmation",
			observedPCSReplicaIndices: sets.New(0), desiredPCSReplicas: 1,
			sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1,
			retiredSourcePCSReplicaIndices: []int32{3}, canceledAt: &canceledAt,
			want: pcssync.State{ExpectedReplicaIndices: []int{0}, ScaleInReplicaIndices: []int{1, 2, 3}},
		},
		{
			name:                      "retired cleanup overrides a stale scale-out selection",
			observedPCSReplicaIndices: sets.New(0, 1, 2), desiredPCSReplicas: 2,
			scalingIndices:        pcsReplicaScalingIndices{scaleOut: []int{2}},
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1,
			retiredSourcePCSReplicaIndices: []int32{2}, sourceCleanupCompletedAt: &cleanupCompletedAt,
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1}, ScaleOutReplicaIndices: []int{1}, ScaleInReplicaIndices: []int{2},
			},
		},
		{
			name:                      "retired cleanup preserves unrelated ordinary scale-out and scale-in",
			observedPCSReplicaIndices: sets.New(0, 1, 2, 4), desiredPCSReplicas: 3,
			scalingIndices:        pcsReplicaScalingIndices{scaleOut: []int{3}, scaleIn: []int{4}},
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1,
			retiredSourcePCSReplicaIndices: []int32{2}, sourceCleanupCompletedAt: &cleanupCompletedAt,
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1, 3}, ScaleOutReplicaIndices: []int{1, 3}, ScaleInReplicaIndices: []int{2, 4},
			},
		},
		{
			name:                      "multiple retired sources produce sorted cleanup indices",
			observedPCSReplicaIndices: sets.New(0, 1, 2, 4), desiredPCSReplicas: 2,
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1,
			retiredSourcePCSReplicaIndices: []int32{4, 2},
			want:                           pcssync.State{ExpectedReplicaIndices: []int{0}, ScaleInReplicaIndices: []int{1, 2, 4}},
		},
		{
			name:                      "retired index zero is cleanup rather than creation work",
			observedPCSReplicaIndices: sets.New(0, 1), desiredPCSReplicas: 1,
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1,
			retiredSourcePCSReplicaIndices: []int32{0}, sourceCleanupCompletedAt: &cleanupCompletedAt,
			want: pcssync.State{
				ExpectedReplicaIndices: []int{1}, ScaleOutReplicaIndices: []int{1}, ScaleInReplicaIndices: []int{0},
			},
		},
		{
			name:                      "ordinary scale-in and retired cleanup share one deletion index",
			observedPCSReplicaIndices: sets.New(0, 1, 2), desiredPCSReplicas: 2,
			scalingIndices:        pcsReplicaScalingIndices{scaleIn: []int{2}},
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1,
			retiredSourcePCSReplicaIndices: []int32{2}, sourceCleanupCompletedAt: &cleanupCompletedAt,
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1}, ScaleOutReplicaIndices: []int{1}, ScaleInReplicaIndices: []int{2},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			progress := &grovecorev1beta1.PodCliqueSetUpdateProgress{
				RequiresReplicaRecreation: true,
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
					ReplicaIndex: tc.sourcePCSReplicaIndex,
					Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
						TargetReplicaIndex:          tc.targetPCSReplicaIndex,
						RetiredSourceReplicaIndices: tc.retiredSourcePCSReplicaIndices,
						SourceCleanupCompletedAt:    tc.sourceCleanupCompletedAt,
						CanceledAt:                  tc.canceledAt,
					},
				}},
			}
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "uid").
				WithReplicas(tc.desiredPCSReplicas).
				WithObservedGeneration(new(int64(0))).
				WithScalingProgress(scalingProgressFromScalingIndices(tc.scalingIndices)).
				WithUpdateProgress(progress.DeepCopy()).Build()
			expected := expectedReplicaIndicesFromScaling(tc.observedPCSReplicaIndices, tc.scalingIndices)
			expectedBefore := expected.Clone()
			scaleInBefore := slices.Clone(tc.scalingIndices.scaleIn)
			scaleOutBefore := slices.Clone(tc.scalingIndices.scaleOut)
			groups := groupPCSReplicaReplacements(pcs.Status.UpdateProgress)
			groupsBefore := groupPCSReplicaReplacements(pcs.Status.UpdateProgress)
			before := pcs.DeepCopy()

			got := computePCSReplicaState(pcs, tc.scalingIndices, expected, groups)

			assert.Equal(t, tc.want, got)
			assert.Equal(t, expectedBefore, expected, "projection must not mutate the caller's expected set")
			assert.Equal(t, scaleInBefore, tc.scalingIndices.scaleIn, "projection must not mutate scaling selections")
			assert.Equal(t, scaleOutBefore, tc.scalingIndices.scaleOut, "projection must not mutate scaling selections")
			assert.Equal(t, groupsBefore, groups, "projection must not mutate grouped cleanup obligations")
			assert.Equal(t, before, pcs, "reconcile-local cleanup must not be persisted as ordinary scaling progress")
		})
	}
}

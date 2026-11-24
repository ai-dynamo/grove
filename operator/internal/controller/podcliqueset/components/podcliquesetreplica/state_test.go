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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
			want:                pcssync.State{ExpectedReplicaIndices: []int{0, 2}, ScaleOutReplicaIndices: []int{0}, ScaleInReplicaIndices: []int{1}},
		},
		{
			name: "canceled same index drains without recreation", desiredPCSReplicas: 2, observedPCSReplicaIndices: []int{0, 1},
			replacementProgress: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0, CanceledAt: &progressTimestamp},
			want:                pcssync.State{ExpectedReplicaIndices: []int{1, 2}, ScaleInReplicaIndices: []int{0}},
		},
		{
			name: "canceled different index drains absent source and target", desiredPCSReplicas: 2, sourcePCSReplicaIndex: 1,
			replacementProgress: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0, CanceledAt: &progressTimestamp},
			want:                pcssync.State{ExpectedReplicaIndices: []int{2, 3}, ScaleInReplicaIndices: []int{0, 1}},
		},
		{
			name: "retired initial index stays cleanup only before barrier", desiredPCSReplicas: 3, observedPCSReplicaIndices: []int{0, 1, 2},
			replacementProgress: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0, RetiredSourceReplicaIndices: []int32{2}},
			want:                pcssync.State{ExpectedReplicaIndices: []int{1, 3}, ScaleInReplicaIndices: []int{0, 2}},
		},
		{
			name: "retired initial index stays cleanup only after barrier", desiredPCSReplicas: 3, observedPCSReplicaIndices: []int{1},
			replacementProgress: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0, SourceCleanupCompletedAt: &progressTimestamp, RetiredSourceReplicaIndices: []int32{2}},
			want:                pcssync.State{ExpectedReplicaIndices: []int{0, 1, 3}, ScaleOutReplicaIndices: []int{0}, ScaleInReplicaIndices: []int{2}},
		},
		{
			name: "canceled retirement outside initial range remains cleanup work", desiredPCSReplicas: 2, sourcePCSReplicaIndex: 2,
			replacementProgress: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1, CanceledAt: &progressTimestamp, RetiredSourceReplicaIndices: []int32{3}},
			want:                pcssync.State{ExpectedReplicaIndices: []int{0, 4}, ScaleInReplicaIndices: []int{1, 2, 3}},
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
			expected := computePCSReplicaIndicesAfterScaling(sets.New(tc.observedPCSReplicaIndices...), pcsReplicaScalingIndices{})
			expectedBefore := expected.Clone()
			replacementGroups := groupPCSReplicaReplacements(updateProgress)
			replacementGroupsBefore := groupPCSReplicaReplacements(updateProgress)

			replicaInfos := make(pcsReplicaInfoByIndex, len(tc.observedPCSReplicaIndices))
			for _, pcsReplicaIndex := range tc.observedPCSReplicaIndices {
				replicaInfos[pcsReplicaIndex] = pcsReplicaInfo{index: pcsReplicaIndex}
			}
			initialPCSReplicaIndices := computeInitialPCSReplicaProvisioningIndices(pcs, replicaInfos, pcsReplicaScalingIndices{}, replacementGroups)
			expectedWithInitialProvisioning := expected.Clone()
			expectedWithInitialProvisioning.Insert(initialPCSReplicaIndices...)
			got := computePCSReplicaState(pcs, pcsReplicaScalingIndices{}, expectedWithInitialProvisioning, replacementGroups)

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

func TestComputeInitialPCSReplicaProvisioningIndices(t *testing.T) {
	t.Parallel()
	cleanupTime := metav1.NewTime(time.Unix(150, 0).UTC())
	activeSame := grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
		ReplicaIndex: 1,
		Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
	}
	activeDifferent := grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
		ReplicaIndex: 2,
		Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
	}
	canceled := grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
		ReplicaIndex: 0,
		Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0, CanceledAt: &cleanupTime},
	}
	retired := grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
		ReplicaIndex: 1,
		Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1, RetiredSourceReplicaIndices: []int32{0, 2}},
	}
	canceledRetired := *retired.DeepCopy()
	canceledRetired.Replacement.CanceledAt = &cleanupTime
	tests := []struct {
		name               string
		desiredReplicas    int32
		observedGeneration *int64
		observedIndices    []int
		scalingIndices     pcsReplicaScalingIndices
		progress           []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress
		want               []int
	}{
		{name: "fresh three-replica creation", desiredReplicas: 3, want: []int{0, 1, 2}},
		{name: "partial creation fills the missing index", desiredReplicas: 3, observedIndices: []int{0, 2}, want: []int{1}},
		{name: "all initial identities already exist", desiredReplicas: 3, observedIndices: []int{0, 1, 2}},
		{name: "zero replicas needs no initial creation"},
		{name: "excess observed capacity is not initial creation", desiredReplicas: 1, observedIndices: []int{0, 1}},
		{name: "completed spec reconciliation uses ordinary scale-out", desiredReplicas: 3, observedGeneration: new(int64(1))},
		{name: "stale non-nil observation does not restart initial creation", desiredReplicas: 3, observedGeneration: new(int64(0))},
		{name: "absent recorded scale-out already covers capacity", desiredReplicas: 2, observedIndices: []int{0}, scalingIndices: pcsReplicaScalingIndices{scaleOut: []int{4}}},
		{name: "pending scale-out plus one initial identity", desiredReplicas: 3, observedIndices: []int{0}, scalingIndices: pcsReplicaScalingIndices{scaleOut: []int{4}}, want: []int{1}},
		{name: "scale-in identity remains unavailable", desiredReplicas: 2, observedIndices: []int{0, 1}, scalingIndices: pcsReplicaScalingIndices{scaleIn: []int{0}}, want: []int{2}},
		{name: "absent same-index target already covers one replica", desiredReplicas: 1, progress: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{activeSame}},
		{name: "sparse active target does not invent index zero", desiredReplicas: 1, observedIndices: []int{1}, progress: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{activeSame}},
		{name: "different-index source and target count once", desiredReplicas: 2, observedIndices: []int{1, 2}, progress: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{activeDifferent}, want: []int{0}},
		{name: "absent replacement target plus missing initial replica", desiredReplicas: 2, progress: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{activeSame}, want: []int{0}},
		{name: "canceled live target is cleanup not capacity", desiredReplicas: 1, observedIndices: []int{0}, progress: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{canceled}, want: []int{1}},
		{name: "absent canceled target remains unavailable", desiredReplicas: 1, progress: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{canceled}, want: []int{1}},
		{name: "absent retirement remains unavailable", desiredReplicas: 2, progress: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{retired}, want: []int{3}},
		{name: "live retirement does not cover desired capacity", desiredReplicas: 2, observedIndices: []int{0, 1, 2}, progress: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{retired}, want: []int{3}},
		{name: "canceled and retired cleanup exclude all owned indices", desiredReplicas: 2, progress: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{canceledRetired}, want: []int{3, 4}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
				WithReplicas(tc.desiredReplicas).
				WithGeneration(1).
				WithObservedGeneration(tc.observedGeneration).
				WithUpdateProgress((&grovecorev1beta1.PodCliqueSetUpdateProgress{RequiresReplicaRecreation: true, CurrentlyUpdating: tc.progress}).DeepCopy()).Build()
			replicaInfos := make(pcsReplicaInfoByIndex, len(tc.observedIndices))
			for _, pcsReplicaIndex := range tc.observedIndices {
				replicaInfos[pcsReplicaIndex] = pcsReplicaInfo{index: pcsReplicaIndex}
			}
			groups := groupPCSReplicaReplacements(pcs.Status.UpdateProgress)
			before := pcs.DeepCopy()
			groupsBefore := groupPCSReplicaReplacements(pcs.Status.UpdateProgress)
			scaleInBefore := slices.Clone(tc.scalingIndices.scaleIn)
			scaleOutBefore := slices.Clone(tc.scalingIndices.scaleOut)

			got := computeInitialPCSReplicaProvisioningIndices(pcs, replicaInfos, tc.scalingIndices, groups)

			assert.Equal(t, tc.want, got)
			assert.Equal(t, before, pcs, "initial creation must not write status or acknowledge the spec")
			assert.Equal(t, groupsBefore, groups, "initial creation must not change replacement ownership")
			assert.Equal(t, scaleInBefore, tc.scalingIndices.scaleIn)
			assert.Equal(t, scaleOutBefore, tc.scalingIndices.scaleOut)
			if len(got) > 0 {
				got[0] = -1
				assert.Equal(t, tc.want, computeInitialPCSReplicaProvisioningIndices(pcs, replicaInfos, tc.scalingIndices, groups), "returned indices must be independently owned")
			}
		})
	}
}

func TestComputePCSReplicaIndicesAfterScaling(t *testing.T) {
	existing := sets.New(0, 2)
	scalingIndices := pcsReplicaScalingIndices{scaleOut: []int{1}, scaleIn: []int{2}}

	got := computePCSReplicaIndicesAfterScaling(existing, scalingIndices)

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
			expected := computePCSReplicaIndicesAfterScaling(tc.observedPCSReplicaIndices, tc.scalingIndices)
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

func TestComputePCSReplicaState(t *testing.T) {
	newPCS := func(replicas int32) *grovecorev1beta1.PodCliqueSet {
		pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-pcs-uid")).
			WithReplicas(replicas).
			WithStandaloneClique("worker").
			Build()
		observedGeneration := pcs.Generation
		pcs.Status.ObservedGeneration = &observedGeneration
		return pcs
	}
	newUnobservedPCS := func(replicas int32) *grovecorev1beta1.PodCliqueSet {
		pcs := newPCS(replicas)
		pcs.Status.ObservedGeneration = nil
		return pcs
	}
	newReplicaInfos := func(pcs *grovecorev1beta1.PodCliqueSet, availabilityByIndex map[int]bool) pcsReplicaInfoByIndex {
		replicaInfos := make(pcsReplicaInfoByIndex, len(availabilityByIndex))
		for replicaIndex, isAvailable := range availabilityByIndex {
			replicaInfos[replicaIndex] = pcsReplicaInfo{
				index:           replicaIndex,
				standalonePCLQs: []grovecorev1beta1.PodClique{pclqWithAvailability(pcs, replicaIndex, isAvailable)},
			}
		}
		return replicaInfos
	}

	tests := []struct {
		name                   string
		pcs                    *grovecorev1beta1.PodCliqueSet
		replicaInfos           pcsReplicaInfoByIndex
		scaleOutReplicaIndices []int
		scaleInReplicaIndices  []int
		want                   pcssync.State
	}{
		{
			name:         "initial provisioning uses contiguous indices without scale-out progress",
			pcs:          newUnobservedPCS(3),
			replicaInfos: pcsReplicaInfoByIndex{},
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1, 2},
			},
		},
		{
			name:         "scale-out from zero reserves indices",
			pcs:          newPCS(3),
			replicaInfos: pcsReplicaInfoByIndex{},
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1, 2},
				ScaleOutReplicaIndices: []int{0, 1, 2},
			},
		},
		{
			name: "headless Service preserves standalone-only replica identity during gang termination",
			pcs:  newPCS(3),
			replicaInfos: pcsReplicaInfoByIndex{
				0: {index: 0, standalonePCLQs: []grovecorev1beta1.PodClique{{}}, headlessSvcs: []corev1.Service{{}}},
				2: {index: 2, standalonePCLQs: []grovecorev1beta1.PodClique{{}}, headlessSvcs: []corev1.Service{{}}},
				4: {index: 4, headlessSvcs: []corev1.Service{{}}},
			},
			want: pcssync.State{ExpectedReplicaIndices: []int{0, 2, 4}},
		},
		{
			name:         "replica indices already match desired count",
			pcs:          newPCS(2),
			replicaInfos: newReplicaInfos(newPCS(2), map[int]bool{0: true, 1: true}),
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1},
			},
		},
		{
			name:                   "scale-out index with expected PCLQs and PCSGs no longer needs a reservation",
			pcs:                    newPCS(2),
			replicaInfos:           newReplicaInfos(newPCS(2), map[int]bool{0: true, 1: false}),
			scaleOutReplicaIndices: []int{1},
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1},
			},
		},
		{
			name:                   "scale-out index without resources remains reserved",
			pcs:                    newPCS(2),
			replicaInfos:           newReplicaInfos(newPCS(2), map[int]bool{0: true}),
			scaleOutReplicaIndices: []int{1},
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1},
				ScaleOutReplicaIndices: []int{1},
			},
		},
		{
			name:         "scale-in selects unavailable replica before available replica",
			pcs:          newPCS(1),
			replicaInfos: newReplicaInfos(newPCS(1), map[int]bool{0: false, 1: true}),
			want: pcssync.State{
				ExpectedReplicaIndices: []int{1},
				ScaleInReplicaIndices:  []int{0},
			},
		},
		{
			name:         "scale-out fills lowest available index",
			pcs:          newPCS(3),
			replicaInfos: newReplicaInfos(newPCS(3), map[int]bool{0: true, 2: true}),
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1, 2},
				ScaleOutReplicaIndices: []int{1},
			},
		},
		{
			name:                  "scale-out does not reuse an index reserved for scale-in",
			pcs:                   newPCS(4),
			replicaInfos:          newReplicaInfos(newPCS(4), map[int]bool{0: true, 1: false, 2: true, 4: true}),
			scaleInReplicaIndices: []int{1},
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 2, 3, 4},
				ScaleOutReplicaIndices: []int{3},
				ScaleInReplicaIndices:  []int{1},
			},
		},
		{
			name:         "scale-in removes every current replica when desired count is zero",
			pcs:          newPCS(0),
			replicaInfos: newReplicaInfos(newPCS(0), map[int]bool{0: true, 2: false}),
			want: pcssync.State{
				ScaleInReplicaIndices: []int{0, 2},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scaleOutBefore := slices.Clone(test.scaleOutReplicaIndices)
			scaleInBefore := slices.Clone(test.scaleInReplicaIndices)

			got := func() pcssync.State {
				scalingWork := computePCSReplicaScalingWork(
					test.pcs,
					test.replicaInfos,
					pcsReplicaScalingIndices{scaleOut: test.scaleOutReplicaIndices, scaleIn: test.scaleInReplicaIndices},
					pcsReplicaReplacementGroups{},
				)
				scalingIndices := scalingWork.scalingIndices
				expectedReplicaIndices := computePCSReplicaIndicesAfterScaling(test.replicaInfos.existingReplicaIndices(), scalingIndices)
				initialPCSReplicaIndices := computeInitialPCSReplicaProvisioningIndices(test.pcs, test.replicaInfos, scalingIndices, pcsReplicaReplacementGroups{})
				expectedReplicaIndices.Insert(initialPCSReplicaIndices...)
				return computePCSReplicaState(test.pcs, scalingIndices, expectedReplicaIndices, pcsReplicaReplacementGroups{})
			}()

			assert.Equal(t, test.want, got)
			assert.Equal(t, scaleOutBefore, test.scaleOutReplicaIndices)
			assert.Equal(t, scaleInBefore, test.scaleInReplicaIndices)
			assert.Len(t, got.ExpectedReplicaIndices, int(test.pcs.Spec.Replicas))
		})
	}
}

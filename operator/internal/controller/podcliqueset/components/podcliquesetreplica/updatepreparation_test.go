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
	"testing"
	"time"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
)

const (
	testPCSName   = "test-pcs"
	testNamespace = "default"
)

func TestPreparePCSUpdateProgress(t *testing.T) {
	tests := []struct {
		name                            string
		setupPCS                        func() *grovecorev1beta1.PodCliqueSet
		expectUpdateEndedAtSet          bool
		expectRequiresReplicaRecreation bool
		expectedCurrentlyUpdatingIndex  *int32
	}{
		{
			name: "rolling_recreate_strategy_does_not_set_update_ended_at",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(2).
					WithPodCliqueParameters("worker", 1, nil).
					WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1beta1.RollingRecreateStrategy,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("old-hash")).
					Build()
				pcs.Status.UpdatedReplicas = 3 // should be reset to 0
				return pcs
			},
			expectUpdateEndedAtSet: false,
		},
		{
			name: "nil_update_strategy_does_not_set_update_ended_at",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(2).
					WithPodCliqueParameters("worker", 1, nil).
					WithPodCliqueSetGenerationHash(ptr.To("old-hash")).
					Build()
				pcs.Status.UpdatedReplicas = 2 // should be reset to 0
				return pcs
			},
			expectUpdateEndedAtSet: false,
		},
		{
			name: "on_delete_strategy_sets_update_ended_at_immediately",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(2).
					WithPodCliqueParameters("worker", 1, nil).
					WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1beta1.OnDeleteStrategy,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("old-hash")).
					Build()
				pcs.Status.UpdatedReplicas = 5 // should be reset to 0
				return pcs
			},
			expectUpdateEndedAtSet: true,
		},
		{
			name: "rolling_recreate_scheduling_change_requires_replica_recreation",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(2).
					WithPodCliqueParameters("worker", 1, nil).
					WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.RollingRecreateStrategy}).
					WithPodCliqueSetGenerationHash(new("old-hash")).
					Build()
				pcs.Status.AppliedSchedulingConfigurationHash = new("applied-scheduling-hash")
				return pcs
			},
			expectRequiresReplicaRecreation: true,
		},
		{
			name: "coherent_scheduling_change_requires_replica_recreation_without_coherent_scope",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(2).
					WithPodCliqueParameters("worker", 1, nil).
					WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.CoherentStrategy}).
					WithPodCliqueSetGenerationHash(new("old-hash")).
					Build()
				pcs.Status.AppliedSchedulingConfigurationHash = new("applied-scheduling-hash")
				return pcs
			},
			expectRequiresReplicaRecreation: true,
		},
		{
			name: "on_delete_scheduling_change_requires_replica_recreation",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(2).
					WithPodCliqueParameters("worker", 1, nil).
					WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.OnDeleteStrategy}).
					WithPodCliqueSetGenerationHash(new("old-hash")).
					Build()
				pcs.Status.AppliedSchedulingConfigurationHash = new("applied-scheduling-hash")
				return pcs
			},
			expectRequiresReplicaRecreation: true,
		},
		{
			name: "existing_rollingrecreate_in_progress_is_reset_when_hash_changes",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				updateStartedAt := metav1.Now()
				replicaUpdateStartedAt := metav1.Time{Time: updateStartedAt.Add(time.Second)}

				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(3).
					WithPodCliqueParameters("worker", 1, nil).
					WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1beta1.RollingRecreateStrategy,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("old-hash")).
					WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
						UpdateStartedAt:                    updateStartedAt,
						UpdatedPodCliqueScalingGroupsCount: 2,
						TotalPodCliqueScalingGroupsCount:   2,
						UpdatedPodCliquesCount:             3,
						TotalPodCliquesCount:               3,
						CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
							{
								ReplicaIndex:    1,
								UpdateStartedAt: replicaUpdateStartedAt,
							},
						},
					}).
					Build()
				pcs.Status.UpdatedReplicas = 2
				return pcs
			},
			expectUpdateEndedAtSet:         false,
			expectedCurrentlyUpdatingIndex: ptr.To[int32](1),
		},
		{
			name: "existing_on_delete_update_is_reset_when_hash_changes",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				updateStartedAt := metav1.Now()

				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(3).
					WithPodCliqueParameters("worker", 1, nil).
					WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1beta1.OnDeleteStrategy,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("old-hash")).
					WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
						UpdateStartedAt:                    updateStartedAt,
						UpdateEndedAt:                      ptr.To(updateStartedAt),
						UpdatedPodCliqueScalingGroupsCount: 1,
						TotalPodCliqueScalingGroupsCount:   1,
						UpdatedPodCliquesCount:             2,
						TotalPodCliquesCount:               2,
					}).
					Build()
				pcs.Status.UpdatedReplicas = 1
				return pcs
			},
			expectUpdateEndedAtSet: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs := tt.setupPCS()

			before := pcs.DeepCopy()
			updateStartedAt := metav1.NewTime(time.Unix(200, 0).UTC())
			targetSchedulingConfigurationHash := "target-scheduling-hash"
			updatedPCS := pcs.DeepCopy()
			updatedPCS.Status.UpdateProgress = preparePCSUpdateProgress(pcs, targetSchedulingConfigurationHash, updateScope{}, updateStartedAt)
			assert.Equal(t, before, pcs, "preparation must not mutate its input")
			// Verify UpdateProgress is set
			require.NotNil(t, updatedPCS.Status.UpdateProgress, "UpdateProgress should be set")
			assert.NotEmpty(t, updatedPCS.Status.UpdateProgress.UpdateStartedAt, "UpdateStartedAt should be set")
			assert.Equal(t, tt.expectRequiresReplicaRecreation, updatedPCS.Status.UpdateProgress.RequiresReplicaRecreation)
			assert.Equal(t, targetSchedulingConfigurationHash, updatedPCS.Status.UpdateProgress.TargetSchedulingConfigurationHash)
			require.NotNil(t, updatedPCS.Status.UpdateProgress.PreviousPodCliqueSetGenerationHash)
			assert.Equal(t, "old-hash", *updatedPCS.Status.UpdateProgress.PreviousPodCliqueSetGenerationHash)

			// Verify UpdateEndedAt based on strategy
			if tt.expectUpdateEndedAtSet {
				require.NotNil(t, updatedPCS.Status.UpdateProgress.UpdateEndedAt, "UpdateEndedAt should be set for OnDelete strategy")
			} else {
				assert.Nil(t, updatedPCS.Status.UpdateProgress.UpdateEndedAt, "UpdateEndedAt should be nil for non-OnDelete strategy")
			}

			assert.Equal(t, int32(0), updatedPCS.Status.UpdateProgress.UpdatedPodCliqueScalingGroupsCount, "UpdatedPodCliqueScalingGroupsCount should be reset to 0")
			assert.Equal(t, int32(0), updatedPCS.Status.UpdateProgress.UpdatedPodCliquesCount, "UpdatedPodCliquesCount should be reset to 0")
			if tt.expectedCurrentlyUpdatingIndex == nil {
				assert.Nil(t, updatedPCS.Status.UpdateProgress.CurrentlyUpdating)
			} else {
				require.Len(t, updatedPCS.Status.UpdateProgress.CurrentlyUpdating, 1)
				assert.Equal(t, *tt.expectedCurrentlyUpdatingIndex, updatedPCS.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex)
				assert.Equal(t, updatedPCS.Status.UpdateProgress.UpdateStartedAt, updatedPCS.Status.UpdateProgress.CurrentlyUpdating[0].UpdateStartedAt)
				assert.Nil(t, updatedPCS.Status.UpdateProgress.CurrentlyUpdating[0].UpdateEndedAt)
			}
		})
	}
}

func TestRequiresPCSReplicaRecreation(t *testing.T) {
	tests := []struct {
		name       string
		pcs        *grovecorev1beta1.PodCliqueSet
		targetHash string
		want       bool
	}{
		{name: "no scheduling baseline", pcs: &grovecorev1beta1.PodCliqueSet{}, targetHash: "target"},
		{name: "scheduling configuration unchanged", pcs: &grovecorev1beta1.PodCliqueSet{Status: grovecorev1beta1.PodCliqueSetStatus{AppliedSchedulingConfigurationHash: new("same")}}, targetHash: "same"},
		{name: "scheduling configuration changed", pcs: &grovecorev1beta1.PodCliqueSet{Status: grovecorev1beta1.PodCliqueSetStatus{AppliedSchedulingConfigurationHash: new("applied")}}, targetHash: "target", want: true},
		{
			name: "scheduling rollback preserves active recreation",
			pcs: &grovecorev1beta1.PodCliqueSet{Status: grovecorev1beta1.PodCliqueSetStatus{
				AppliedSchedulingConfigurationHash: new("applied"),
				UpdateProgress:                     &grovecorev1beta1.PodCliqueSetUpdateProgress{RequiresReplicaRecreation: true},
			}},
			targetHash: "applied",
			want:       true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, requiresPCSReplicaRecreation(tc.pcs, tc.targetHash))
		})
	}
}

func TestIsUpdateOrchestratedByGrove(t *testing.T) {
	onDeletePCS := &grovecorev1beta1.PodCliqueSet{Spec: grovecorev1beta1.PodCliqueSetSpec{UpdateStrategy: &grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.OnDeleteStrategy}}}
	rollingPCS := &grovecorev1beta1.PodCliqueSet{Spec: grovecorev1beta1.PodCliqueSetSpec{UpdateStrategy: &grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.RollingRecreateStrategy}}}

	assert.False(t, isUpdateOrchestratedByGrove(onDeletePCS, false))
	assert.True(t, isUpdateOrchestratedByGrove(onDeletePCS, true))
	assert.True(t, isUpdateOrchestratedByGrove(rollingPCS, false))
}

// TestPrepareReplicaSelectionsForUpdateSelectionRetention covers missing or empty progress,
// completed selections, ordinary scale-in exclusions, and fresh progress for retained ordinary selections.
func TestPrepareReplicaSelectionsForUpdateSelectionRetention(t *testing.T) {
	t.Parallel()
	previousStartedAt := metav1.NewTime(time.Unix(100, 0).UTC())
	updateStartedAt := metav1.NewTime(time.Unix(200, 0).UTC())
	completedAt := metav1.NewTime(time.Unix(150, 0).UTC())
	ordinaryProgress := grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
		ReplicaIndex:    2,
		UpdateStartedAt: previousStartedAt,
		InFlightEpochs:  []string{"previous-epoch"},
		Message:         new("waiting for the previous generation"),
	}
	ordinaryZeroProgress := ordinaryProgress.DeepCopy()
	ordinaryZeroProgress.ReplicaIndex = 0
	completedProgress := ordinaryProgress.DeepCopy()
	completedProgress.UpdateEndedAt = &completedAt
	completedReplacement := completedProgress.DeepCopy()
	completedReplacement.Replacement = &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
		TargetReplicaIndex: 1,
	}
	wantOrdinaryProgress := []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
		ReplicaIndex:    2,
		UpdateStartedAt: updateStartedAt,
	}}

	tests := []struct {
		name            string
		updateProgress  *grovecorev1beta1.PodCliqueSetUpdateProgress
		scalingProgress *grovecorev1beta1.PodCliqueSetScalingProgress
		want            []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress
	}{
		{
			name: "no update progress",
		},
		{
			name: "no update progress with pending scale-in",
			scalingProgress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleInReplicaIndices: []int32{2},
			},
		},
		{
			name:           "nil current selections",
			updateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{},
		},
		{
			name: "empty current selections",
			updateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{},
			},
		},
		{
			name: "ordinary selection gets fresh progress without scaling progress",
			updateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{ordinaryProgress},
			},
			want: wantOrdinaryProgress,
		},
		{
			name: "ordinary selection survives empty scaling progress",
			updateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{ordinaryProgress},
			},
			scalingProgress: &grovecorev1beta1.PodCliqueSetScalingProgress{},
			want:            wantOrdinaryProgress,
		},
		{
			name: "selection is carried provisionally despite prior scale-in",
			want: wantOrdinaryProgress,
			updateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{ordinaryProgress},
			},
			scalingProgress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleInReplicaIndices: []int32{2},
			},
		},
		{
			name: "replica zero is carried provisionally despite prior scale-in",
			want: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0, UpdateStartedAt: updateStartedAt}},
			updateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{*ordinaryZeroProgress},
			},
			scalingProgress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleInReplicaIndices: []int32{0},
			},
		},
		{
			name: "ordinary selection is not excluded by unrelated scale-in",
			updateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{ordinaryProgress},
			},
			scalingProgress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleInReplicaIndices: []int32{7},
			},
			want: wantOrdinaryProgress,
		},
		{
			name: "scale-out does not exclude an ordinary selection",
			updateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{ordinaryProgress},
			},
			scalingProgress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleOutReplicaIndices: []int32{2},
			},
			want: wantOrdinaryProgress,
		},
		{
			name: "completed ordinary selection is excluded",
			updateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{*completedProgress},
			},
		},
		{
			name: "completed replacement is excluded",
			updateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{*completedReplacement},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithUpdateProgress(tc.updateProgress.DeepCopy()).
				WithScalingProgress(tc.scalingProgress.DeepCopy()).Build()
			before := pcs.DeepCopy()

			got := prepareReplicaSelectionsForUpdate(pcs.Status.UpdateProgress, updateStartedAt)

			require.Len(t, got, len(tc.want))
			for i, want := range tc.want {
				assert.Equal(t, want, got[i])
			}
			if tc.updateProgress == nil {
				assert.Nil(t, got)
			}
			assert.Equal(t, before, pcs, "preparation must not mutate the input PCS")
		})
	}
}

// TestPrepareReplicaSelectionsForUpdateReplacementHandoffAndCancellation covers
// active replacement handoff, retired-source cleanup obligations, and fresh cleanup barriers. Canceled
// replacements retain their mapping, cancellation details, and cleanup progress.
func TestPrepareReplicaSelectionsForUpdateReplacementHandoffAndCancellation(t *testing.T) {
	t.Parallel()
	previousStartedAt := metav1.NewTime(time.Unix(100, 0).UTC())
	cleanupCompletedAt := metav1.NewTime(time.Unix(150, 0).UTC())
	canceledAt := metav1.NewTime(time.Unix(175, 0).UTC())
	updateStartedAt := metav1.NewTime(time.Unix(200, 0).UTC())
	tests := []struct {
		name                            string
		sourcePCSReplicaIndex           int32
		targetPCSReplicaIndex           int32
		retiredSourcePCSReplicaIndices  []int32
		sourceCleanupCompletedAt        *metav1.Time
		canceledAt                      *metav1.Time
		scaleInPCSReplicaIndices        []int32
		wantPCSReplicaIndex             int32
		wantRetiredSourceReplicaIndices []int32
		wantCleanupCompletedAt          *metav1.Time
	}{
		{
			name:                  "active same-index replacement already deleting",
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, wantPCSReplicaIndex: 1,
		},
		{
			name:                  "active same-index replacement restarts completed cleanup",
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, wantPCSReplicaIndex: 1,
			sourceCleanupCompletedAt: &cleanupCompletedAt,
		},
		{
			name:                  "active same-index replacement preserves retired sources",
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, wantPCSReplicaIndex: 1,
			retiredSourcePCSReplicaIndices:  []int32{4, 2},
			sourceCleanupCompletedAt:        &cleanupCompletedAt,
			wantRetiredSourceReplicaIndices: []int32{4, 2},
		},
		{
			name:                  "different-index replacement retires its previous source",
			sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, wantPCSReplicaIndex: 1,
			wantRetiredSourceReplicaIndices: []int32{2},
		},
		{
			name:                  "replica index zero is a valid source",
			sourcePCSReplicaIndex: 0, targetPCSReplicaIndex: 2, wantPCSReplicaIndex: 2,
			wantRetiredSourceReplicaIndices: []int32{0},
		},
		{
			name:                  "replica index zero is a valid target",
			sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 0, wantPCSReplicaIndex: 0,
			wantRetiredSourceReplicaIndices: []int32{2},
		},
		{
			name:                  "target index may be greater than source index",
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 3, wantPCSReplicaIndex: 3,
			wantRetiredSourceReplicaIndices: []int32{1},
		},
		{
			name:                  "different-index replacement resets a previous cleanup marker",
			sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, wantPCSReplicaIndex: 1,
			sourceCleanupCompletedAt:        &cleanupCompletedAt,
			wantRetiredSourceReplicaIndices: []int32{2},
		},
		{
			name:                  "previous source is merged with existing cleanup obligations",
			sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, wantPCSReplicaIndex: 1,
			retiredSourcePCSReplicaIndices:  []int32{4, 3},
			wantRetiredSourceReplicaIndices: []int32{2, 3, 4},
		},
		{
			name:                  "a source already recorded as retired is not duplicated",
			sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, wantPCSReplicaIndex: 1,
			retiredSourcePCSReplicaIndices:  []int32{4, 2},
			wantRetiredSourceReplicaIndices: []int32{2, 4},
		},
		{
			name:                  "active replacement is retained when its source is queued for scale-in",
			sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, wantPCSReplicaIndex: 1,
			scaleInPCSReplicaIndices:        []int32{2},
			wantRetiredSourceReplicaIndices: []int32{2},
		},
		{
			name:                  "active same-index replacement retains cleanup despite scale-in",
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, wantPCSReplicaIndex: 1,
			retiredSourcePCSReplicaIndices:  []int32{2},
			scaleInPCSReplicaIndices:        []int32{1},
			wantRetiredSourceReplicaIndices: []int32{2},
		},
		{
			name:                  "target scale-in does not discard active replacement progress",
			sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, wantPCSReplicaIndex: 1,
			scaleInPCSReplicaIndices:        []int32{1},
			wantRetiredSourceReplicaIndices: []int32{2},
		},
		{
			name:                  "canceled same-index replacement preserves its cleanup barrier",
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, wantPCSReplicaIndex: 1,
			retiredSourcePCSReplicaIndices:  []int32{2},
			sourceCleanupCompletedAt:        &cleanupCompletedAt,
			canceledAt:                      &canceledAt,
			wantRetiredSourceReplicaIndices: []int32{2},
			wantCleanupCompletedAt:          &cleanupCompletedAt,
		},
		{
			name:                  "canceled different-index replacement preserves mapping and retired sources",
			sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, wantPCSReplicaIndex: 2,
			retiredSourcePCSReplicaIndices:  []int32{4, 3},
			sourceCleanupCompletedAt:        &cleanupCompletedAt,
			canceledAt:                      &canceledAt,
			wantRetiredSourceReplicaIndices: []int32{4, 3},
			wantCleanupCompletedAt:          &cleanupCompletedAt,
		},
		{
			name:                  "canceled replacement still deleting preserves a nil cleanup marker",
			sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, wantPCSReplicaIndex: 2,
			canceledAt: &canceledAt,
		},
		{
			name:                  "scale-in cannot discard canceled replacement cleanup obligations",
			sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, wantPCSReplicaIndex: 2,
			retiredSourcePCSReplicaIndices:  []int32{4},
			sourceCleanupCompletedAt:        &cleanupCompletedAt,
			canceledAt:                      &canceledAt,
			scaleInPCSReplicaIndices:        []int32{2, 1},
			wantRetiredSourceReplicaIndices: []int32{4},
			wantCleanupCompletedAt:          &cleanupCompletedAt,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var cancellationMessage *string
			if tc.canceledAt != nil {
				cancellationMessage = new("replica-count reduction selected the replacement target")
			}
			updateProgress := &grovecorev1beta1.PodCliqueSetUpdateProgress{
				UpdateStartedAt: previousStartedAt,
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
					ReplicaIndex:    tc.sourcePCSReplicaIndex,
					UpdateStartedAt: previousStartedAt,
					InFlightEpochs:  []string{"previous-epoch"},
					Message:         new("waiting for the previous generation"),
					Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
						TargetReplicaIndex:          tc.targetPCSReplicaIndex,
						RetiredSourceReplicaIndices: tc.retiredSourcePCSReplicaIndices,
						SourceCleanupCompletedAt:    tc.sourceCleanupCompletedAt,
						CanceledAt:                  tc.canceledAt,
						CancellationMessage:         cancellationMessage,
					},
				}},
			}
			scalingProgress := &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleInReplicaIndices: tc.scaleInPCSReplicaIndices,
			}
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithUpdateProgress(updateProgress.DeepCopy()).
				WithScalingProgress(scalingProgress.DeepCopy()).Build()
			before := pcs.DeepCopy()

			got := prepareReplicaSelectionsForUpdate(pcs.Status.UpdateProgress, updateStartedAt)

			require.Len(t, got, 1)
			want := grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				ReplicaIndex:    tc.wantPCSReplicaIndex,
				UpdateStartedAt: updateStartedAt,
				Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
					TargetReplicaIndex:          tc.targetPCSReplicaIndex,
					RetiredSourceReplicaIndices: tc.wantRetiredSourceReplicaIndices,
					SourceCleanupCompletedAt:    tc.wantCleanupCompletedAt,
					CanceledAt:                  tc.canceledAt,
					CancellationMessage:         cancellationMessage,
				},
			}
			assert.Equal(t, want, got[0])
			assert.Equal(t, before, pcs, "preparation must not mutate the input PCS")
		})
	}
}

// TestPrepareReplicaSelectionsForUpdateSortsByPreparedReplicaIndex verifies
// ascending order by prepared PCS replica indices, including when handoff changes an already-sorted source
// order. Completed selections are excluded, while other selections remain provisional until scaling is planned.
func TestPrepareReplicaSelectionsForUpdateSortsByPreparedReplicaIndex(t *testing.T) {
	t.Parallel()
	updateStartedAt := metav1.NewTime(time.Unix(200, 0).UTC())
	canceledAt := metav1.NewTime(time.Unix(175, 0).UTC())
	canceledProgress := grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
		ReplicaIndex: 0,
		Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
			TargetReplicaIndex: 6,
			CanceledAt:         &canceledAt,
		},
	}
	ordinaryProgress := grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{ReplicaIndex: 2}
	activeProgress := grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
		ReplicaIndex: 5,
		Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
			TargetReplicaIndex: 1,
		},
	}
	completedAt := metav1.NewTime(time.Unix(150, 0).UTC())
	completedProgress := grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
		ReplicaIndex: 9, UpdateEndedAt: &completedAt,
	}
	scaleInProgress := grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{ReplicaIndex: 8}
	tests := []struct {
		name                     string
		currentlyUpdating        []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress
		scaleInPCSReplicaIndices []int32
		wantAdditionalSelections []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress
	}{
		{
			name:              "unsorted input",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{activeProgress, canceledProgress, ordinaryProgress},
		},
		{
			name:              "sorted sources must be reordered after target handoff",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{canceledProgress, ordinaryProgress, activeProgress},
		},
		{
			name: "excluded entries do not discard subsequent selections or canceled cleanup",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				completedProgress, scaleInProgress, ordinaryProgress, activeProgress, canceledProgress,
			},
			scaleInPCSReplicaIndices: []int32{8, 0},
			wantAdditionalSelections: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 8, UpdateStartedAt: updateStartedAt}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			updateProgress := &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: tc.currentlyUpdating}
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithUpdateProgress(updateProgress.DeepCopy()).
				WithScalingProgress(&grovecorev1beta1.PodCliqueSetScalingProgress{
					ScaleInReplicaIndices: tc.scaleInPCSReplicaIndices,
				}).Build()
			before := pcs.DeepCopy()

			got := prepareReplicaSelectionsForUpdate(pcs.Status.UpdateProgress, updateStartedAt)

			want := []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				{ReplicaIndex: 0, UpdateStartedAt: updateStartedAt, Replacement: canceledProgress.Replacement},
				{
					ReplicaIndex:    1,
					UpdateStartedAt: updateStartedAt,
					Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
						TargetReplicaIndex:          1,
						RetiredSourceReplicaIndices: []int32{5},
					},
				},
				{ReplicaIndex: 2, UpdateStartedAt: updateStartedAt},
			}
			assert.Equal(t, append(want, tc.wantAdditionalSelections...), got)
			assert.Equal(t, before, pcs, "sorting must not reorder or modify the input progress")
		})
	}
}

// TestPrepareReplicaSelectionsForUpdateRepeatedSupersession covers successive generations
// retaining the original retired source and restarting target cleanup without retiring the target itself.
func TestPrepareReplicaSelectionsForUpdateRepeatedSupersession(t *testing.T) {
	t.Parallel()
	updateProgress := &grovecorev1beta1.PodCliqueSetUpdateProgress{
		CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
			ReplicaIndex: 2,
			Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex: 1,
			},
		}},
	}
	for _, updateStartedAt := range []metav1.Time{
		metav1.NewTime(time.Unix(200, 0).UTC()),
		metav1.NewTime(time.Unix(300, 0).UTC()),
		metav1.NewTime(time.Unix(400, 0).UTC()),
	} {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithUpdateProgress(updateProgress.DeepCopy()).Build()
		before := pcs.DeepCopy()

		got := prepareReplicaSelectionsForUpdate(pcs.Status.UpdateProgress, updateStartedAt)

		want := []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
			ReplicaIndex:    1,
			UpdateStartedAt: updateStartedAt,
			Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex:          1,
				RetiredSourceReplicaIndices: []int32{2},
			},
		}}
		require.Equal(t, want, got, "unexpected handoff for generation started at %s", updateStartedAt.String())
		assert.Equal(t, before, pcs, "supersession must not mutate the previous generation")

		// Cleanup at the target applies only to the current generation. A later update must reset its barrier
		// without adding the target to the retired sources.
		cleanupCompletedAt := metav1.NewTime(updateStartedAt.Add(time.Second))
		nextProgress := got[0].DeepCopy()
		nextProgress.Replacement.SourceCleanupCompletedAt = &cleanupCompletedAt
		updateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
			CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{*nextProgress},
		}
	}
}

// TestPrepareReplicaSelectionsForUpdateCopyIsolation verifies that returned progress,
// retired-source slices, and pointer fields do not alias the source or independently prepared results.
func TestPrepareReplicaSelectionsForUpdateCopyIsolation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                  string
		sourcePCSReplicaIndex int32
		isCanceled            bool
	}{
		{name: "active different-index replacement", sourcePCSReplicaIndex: 2},
		{name: "active same-index replacement", sourcePCSReplicaIndex: 1},
		{name: "canceled different-index replacement", sourcePCSReplicaIndex: 2, isCanceled: true},
		{name: "canceled same-index replacement", sourcePCSReplicaIndex: 1, isCanceled: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cleanupCompletedAt := metav1.NewTime(time.Unix(150, 0).UTC())
			updateStartedAt := metav1.NewTime(time.Unix(200, 0).UTC())
			replacement := &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex:          1,
				RetiredSourceReplicaIndices: []int32{4, 3},
				SourceCleanupCompletedAt:    &cleanupCompletedAt,
			}
			if tc.isCanceled {
				canceledAt := metav1.NewTime(time.Unix(175, 0).UTC())
				replacement.CanceledAt = &canceledAt
				replacement.CancellationMessage = new("replica-count reduction selected the replacement target")
			}
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
					CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
						ReplicaIndex: tc.sourcePCSReplicaIndex,
						Replacement:  replacement,
					}},
				}).Build()
			before := pcs.DeepCopy()

			got := prepareReplicaSelectionsForUpdate(pcs.Status.UpdateProgress, updateStartedAt)
			again := prepareReplicaSelectionsForUpdate(pcs.Status.UpdateProgress, updateStartedAt)

			require.Len(t, got, 1)
			require.Equal(t, got, again, "repeated preparation must produce the same progress")
			assert.Equal(t, before, pcs, "preparation must not mutate the source")
			beforeResult := got[0].DeepCopy()
			require.NotNil(t, got[0].Replacement)
			require.NotEmpty(t, got[0].Replacement.RetiredSourceReplicaIndices)
			got[0].ReplicaIndex = 9
			got[0].UpdateStartedAt.Time = time.Unix(999, 0).UTC()
			got[0].Replacement.TargetReplicaIndex = 8
			got[0].Replacement.RetiredSourceReplicaIndices[0] = 7
			if tc.isCanceled {
				require.NotNil(t, got[0].Replacement.SourceCleanupCompletedAt)
				require.NotNil(t, got[0].Replacement.CanceledAt)
				require.NotNil(t, got[0].Replacement.CancellationMessage)
				got[0].Replacement.SourceCleanupCompletedAt.Time = time.Unix(998, 0).UTC()
				got[0].Replacement.CanceledAt.Time = time.Unix(997, 0).UTC()
				*got[0].Replacement.CancellationMessage = "changed cancellation reason"
			}

			assert.Equal(t, before, pcs, "mutating returned progress must not mutate the source")
			assert.Equal(t, *beforeResult, again[0], "independent preparation results must not alias each other")
		})
	}
}

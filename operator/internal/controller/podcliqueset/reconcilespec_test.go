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

package podcliqueset

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pclqcomponent "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/podclique"
	pcsreplicacomponent "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/podcliquesetreplica"
	pcsexpectations "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/expectations"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/sync"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCleanedUpScaleInReplicaIndices(t *testing.T) {
	reg := component.NewOperatorRegistry[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result]()
	reg.Register(component.KindPodClique, nil, component.AsReplicaScoped())
	reg.Register(component.KindHeadlessService, nil, component.AsReplicaScoped())
	reg.Register(component.KindPodGang, nil, component.AsReplicaScoped())
	reg.Register(component.KindRole, nil)
	reconciler := &Reconciler{operatorRegistry: reg}

	// Index 5 is reported cleaned up by every replica-scoped participant, so it is cleaned up. Index 1 is reported
	// by only one participant and index 3 by two, so neither reaches full coverage. The non-participant Role is
	// ignored.
	syncResults := map[component.Kind]pcssync.Result{
		component.KindPodClique:       {CleanedUpReplicaIndices: []int{5, 1}},
		component.KindHeadlessService: {CleanedUpReplicaIndices: []int{5, 3}},
		component.KindPodGang:         {CleanedUpReplicaIndices: []int{5}},
		component.KindRole:            {CleanedUpReplicaIndices: []int{1}},
	}

	assert.Equal(t, []int32{5}, sets.List(reconciler.cleanedUpScaleInReplicaIndices(syncResults)))
}

func TestRemoveCompletedScaleInPCSReplicaIndices(t *testing.T) {
	tests := []struct {
		name                            string
		progress                        *grovecorev1beta1.PodCliqueSetScalingProgress
		cleanedUpReplicaIndices         sets.Set[int32]
		expectedProgress                *grovecorev1beta1.PodCliqueSetScalingProgress
		expectedCompletedReplicaIndices []int32
	}{
		{
			name: "no scaling progress",
		},
		{
			name: "no scale-in progress",
			progress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleOutReplicaIndices: []int32{4},
			},
			expectedProgress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleOutReplicaIndices: []int32{4},
			},
		},
		{
			name: "no replicas cleaned up",
			progress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleInReplicaIndices: []int32{1, 3},
			},
			cleanedUpReplicaIndices: sets.New[int32](),
			expectedProgress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleInReplicaIndices: []int32{1, 3},
			},
		},
		{
			name: "cleaned-up replicas are removed",
			progress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleOutReplicaIndices: []int32{4},
				ScaleInReplicaIndices:  []int32{1, 3, 5},
			},
			cleanedUpReplicaIndices: sets.New[int32](1, 5),
			expectedProgress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleOutReplicaIndices: []int32{4},
				ScaleInReplicaIndices:  []int32{3},
			},
			expectedCompletedReplicaIndices: []int32{1, 5},
		},
		{
			name: "all scale-in replicas cleaned up clears scaling progress",
			progress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleInReplicaIndices: []int32{1, 3},
			},
			cleanedUpReplicaIndices:         sets.New[int32](1, 3),
			expectedCompletedReplicaIndices: []int32{1, 3},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
			pcs.Status.ScalingProgress = test.progress

			completed := removeCompletedScaleInPCSReplicaIndices(logr.Discard(), pcs, test.cleanedUpReplicaIndices)

			assert.Equal(t, test.expectedCompletedReplicaIndices, completed)
			assert.Equal(t, test.expectedProgress, pcs.Status.ScalingProgress)
		})
	}
}

func TestRecordReplacementCleanupWork(t *testing.T) {
	canceledAt := metav1.Now()
	activeSameIndex := func() grovecorev1beta1.PodCliqueSetReplicaUpdateProgress {
		return grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex: 1,
			Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
		}
	}
	canceled := func() grovecorev1beta1.PodCliqueSetReplicaUpdateProgress {
		return grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex: 3,
			Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 2, CanceledAt: &canceledAt},
		}
	}

	t.Run("records same-index cleanup completion once shared source resources are gone", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
		pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{activeSameIndex()}}

		recordReplacementCleanupWork(logr.Discard(), pcs, sets.New[int32](1))

		require.NotNil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.SourceCleanupCompletedAt)
	})

	t.Run("leaves same-index cleanup pending while shared source resources remain", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
		pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{activeSameIndex()}}

		recordReplacementCleanupWork(logr.Discard(), pcs, sets.New[int32]())

		assert.Nil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.SourceCleanupCompletedAt)
	})

	t.Run("does not re-record an already completed same-index cleanup", func(t *testing.T) {
		entry := activeSameIndex()
		entry.Replacement.SourceCleanupCompletedAt = &canceledAt
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
		pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{entry}}

		before := pcs.DeepCopy()
		recordReplacementCleanupWork(logr.Discard(), pcs, sets.New[int32](1))

		assert.Equal(t, before, pcs, "an existing source-cleanup barrier must not be overwritten")
	})

	t.Run("records confirmed different-index source cleanup without completing the replacement", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
		pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			{ReplicaIndex: 3, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 2}},
		}}

		recordReplacementCleanupWork(logr.Discard(), pcs, sets.New[int32](2, 3))

		require.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1)
		require.NotNil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.SourceCleanupCompletedAt)
		assert.Nil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].UpdateEndedAt)
	})

	t.Run("removes a canceled replacement once source and target resources are gone", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
		pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{canceled()}}

		recordReplacementCleanupWork(logr.Discard(), pcs, sets.New[int32](2, 3))

		assert.Empty(t, pcs.Status.UpdateProgress.CurrentlyUpdating)
	})

	t.Run("retains a canceled replacement while its target resources remain", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
		pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{canceled()}}

		recordReplacementCleanupWork(logr.Discard(), pcs, sets.New[int32](3))

		assert.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1)
	})

	t.Run("records a same-index completion and removes a canceled entry together", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
		pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{activeSameIndex(), canceled()}}

		recordReplacementCleanupWork(logr.Discard(), pcs, sets.New[int32](1, 2, 3))

		require.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1)
		assert.Equal(t, int32(1), pcs.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex)
	})
}

// TestRecordPCSReplicaReplacementCleanup covers retired-source confirmation, recording a new source barrier,
// and canceled cleanup completion. A recorded source barrier and completed cancellation are mutually exclusive.
func TestRecordPCSReplicaReplacementCleanup(t *testing.T) {
	t.Parallel()
	previousCleanupAt := metav1.NewTime(time.Unix(100, 0).UTC())
	canceledAt := metav1.NewTime(time.Unix(150, 0).UTC())
	tests := []struct {
		name                        string
		sourcePCSReplicaIndex       int32
		targetPCSReplicaIndex       int32
		retiredSourceReplicaIndices []int32
		sourceCleanupCompletedAt    *metav1.Time
		canceledAt                  *metav1.Time
		confirmedPCSReplicaIndices  sets.Set[int32]
		wantRetiredSourceIndices    []int32
		wantRecordedSourceBarrier   bool
		wantCanceledCleanupComplete bool
	}{
		{name: "unconfirmed same-index cleanup stays pending", sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1},
		{name: "empty retired list imposes no additional obligation", sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, retiredSourceReplicaIndices: []int32{}},
		{name: "confirmed same-index source records a new barrier", sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, confirmedPCSReplicaIndices: sets.New[int32](1), wantRecordedSourceBarrier: true},
		{name: "source index zero can record a same-index barrier", confirmedPCSReplicaIndices: sets.New[int32](0), wantRecordedSourceBarrier: true},
		{name: "an existing barrier is not recorded again", sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, sourceCleanupCompletedAt: &previousCleanupAt, confirmedPCSReplicaIndices: sets.New[int32](1)},
		{name: "an existing barrier is preserved without new confirmation", sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, sourceCleanupCompletedAt: &previousCleanupAt},
		{name: "unconfirmed different-index cleanup stays pending", sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1},
		{name: "target confirmation does not confirm different-index source cleanup", sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, confirmedPCSReplicaIndices: sets.New[int32](1)},
		{name: "confirmed different-index source records cleanup completion", sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, confirmedPCSReplicaIndices: sets.New[int32](1, 2), wantRecordedSourceBarrier: true},
		{name: "source index zero can record different-index cleanup completion", targetPCSReplicaIndex: 1, confirmedPCSReplicaIndices: sets.New[int32](0), wantRecordedSourceBarrier: true},
		{name: "existing different-index confirmation is not recorded again", sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, sourceCleanupCompletedAt: &previousCleanupAt, confirmedPCSReplicaIndices: sets.New[int32](2)},
		{name: "existing different-index confirmation is preserved without new confirmation", sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, sourceCleanupCompletedAt: &previousCleanupAt},
		{
			name:                  "partial retirement confirmation preserves unconfirmed sources",
			sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1,
			retiredSourceReplicaIndices: []int32{4, 3, 5}, confirmedPCSReplicaIndices: sets.New[int32](3, 99),
			wantRetiredSourceIndices: []int32{4, 5},
		},
		{
			name:                  "retirement can complete without recording a source barrier",
			sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1,
			retiredSourceReplicaIndices: []int32{3, 4}, confirmedPCSReplicaIndices: sets.New[int32](3, 4),
		},
		{
			name:                  "retirement progresses while same-index source cleanup remains pending",
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1,
			retiredSourceReplicaIndices: []int32{2}, confirmedPCSReplicaIndices: sets.New[int32](2),
		},
		{
			name:                  "source barrier can advance while retired cleanup remains pending",
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1,
			retiredSourceReplicaIndices: []int32{2}, confirmedPCSReplicaIndices: sets.New[int32](1),
			wantRetiredSourceIndices: []int32{2}, wantRecordedSourceBarrier: true,
		},
		{
			name:                  "source barrier and retired cleanup can advance together",
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1,
			retiredSourceReplicaIndices: []int32{2}, confirmedPCSReplicaIndices: sets.New[int32](1, 2),
			wantRecordedSourceBarrier: true,
		},
		{
			name:                  "unrelated cleanup confirmation cannot release a retired source",
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1,
			retiredSourceReplicaIndices: []int32{2}, confirmedPCSReplicaIndices: sets.New[int32](99),
			wantRetiredSourceIndices: []int32{2},
		},
		{name: "canceled cleanup stays pending without source or target confirmation", sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, canceledAt: &canceledAt},
		{name: "source confirmation alone cannot complete different-index cancellation", sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, canceledAt: &canceledAt, confirmedPCSReplicaIndices: sets.New[int32](2)},
		{name: "target confirmation alone cannot complete different-index cancellation", sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, canceledAt: &canceledAt, confirmedPCSReplicaIndices: sets.New[int32](1)},
		{name: "confirmed canceled source and target allow removal", sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, canceledAt: &canceledAt, confirmedPCSReplicaIndices: sets.New[int32](1, 2), wantCanceledCleanupComplete: true},
		{
			name:                  "cancellation waits for every retired source despite confirmed source and target",
			sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, canceledAt: &canceledAt,
			retiredSourceReplicaIndices: []int32{3, 4}, confirmedPCSReplicaIndices: sets.New[int32](1, 2, 3),
			wantRetiredSourceIndices: []int32{4},
		},
		{
			name:                  "canceled retirement can finish before source and target cleanup",
			sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, canceledAt: &canceledAt,
			retiredSourceReplicaIndices: []int32{3, 4}, confirmedPCSReplicaIndices: sets.New[int32](3, 4),
		},
		{
			name:                  "all confirmed cancellation obligations allow removal",
			sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, canceledAt: &canceledAt,
			retiredSourceReplicaIndices: []int32{3, 4}, confirmedPCSReplicaIndices: sets.New[int32](1, 2, 3, 4),
			wantCanceledCleanupComplete: true,
		},
		{
			name:                  "cancellation after target creation starts preserves the barrier and waits for target deletion",
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, canceledAt: &canceledAt,
			sourceCleanupCompletedAt: &previousCleanupAt, retiredSourceReplicaIndices: []int32{2},
			confirmedPCSReplicaIndices: sets.New[int32](2),
		},
		{
			name:                  "completed cancellation preserves the previously recorded source barrier",
			sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, canceledAt: &canceledAt,
			sourceCleanupCompletedAt: &previousCleanupAt, retiredSourceReplicaIndices: []int32{2},
			confirmedPCSReplicaIndices: sets.New[int32](1, 2), wantCanceledCleanupComplete: true,
		},
		{name: "canceled source zero is a valid cleanup obligation", targetPCSReplicaIndex: 1, canceledAt: &canceledAt, confirmedPCSReplicaIndices: sets.New[int32](0, 1), wantCanceledCleanupComplete: true},
		{name: "canceled target zero is a valid cleanup obligation", sourcePCSReplicaIndex: 1, canceledAt: &canceledAt, confirmedPCSReplicaIndices: sets.New[int32](0, 1), wantCanceledCleanupComplete: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			progress := &grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				ReplicaIndex:    tc.sourcePCSReplicaIndex,
				UpdateStartedAt: previousCleanupAt,
				InFlightEpochs:  []string{"existing-epoch"},
				Message:         new("existing wait reason"),
				Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
					TargetReplicaIndex:          tc.targetPCSReplicaIndex,
					RetiredSourceReplicaIndices: slices.Clone(tc.retiredSourceReplicaIndices),
					SourceCleanupCompletedAt:    tc.sourceCleanupCompletedAt,
					CanceledAt:                  tc.canceledAt,
				},
			}
			progress = progress.DeepCopy()
			before := progress.DeepCopy()
			confirmedBefore := tc.confirmedPCSReplicaIndices.Clone()

			canceledCleanupComplete := recordPCSReplicaReplacementCleanup(logr.Discard(), progress, tc.confirmedPCSReplicaIndices)

			assert.Equal(t, tc.wantCanceledCleanupComplete, canceledCleanupComplete)
			want := before.DeepCopy()
			if len(tc.wantRetiredSourceIndices) == 0 {
				assert.Empty(t, progress.Replacement.RetiredSourceReplicaIndices)
				want.Replacement.RetiredSourceReplicaIndices = progress.Replacement.RetiredSourceReplicaIndices
			} else {
				want.Replacement.RetiredSourceReplicaIndices = tc.wantRetiredSourceIndices
			}
			if tc.wantRecordedSourceBarrier {
				require.NotNil(t, progress.Replacement.SourceCleanupCompletedAt)
				assert.False(t, progress.Replacement.SourceCleanupCompletedAt.IsZero())
				want.Replacement.SourceCleanupCompletedAt = progress.Replacement.SourceCleanupCompletedAt
			}
			assert.Equal(t, want, progress, "cleanup must preserve all other update progress")
			assert.True(t, confirmedBefore.Equal(tc.confirmedPCSReplicaIndices), "cleanup must not mutate component confirmation")
		})
	}
}

// TestDifferentIndexSourceCleanupRequiresParticipantConfirmation verifies that source snapshot absence is
// insufficient when ResourceClaims, ComputeDomains, or PodGangMaps still require cleanup. Missing participant
// results prevent confirmation, while reported non-applicable participants do not block it.
func TestDifferentIndexSourceCleanupRequiresParticipantConfirmation(t *testing.T) {
	t.Parallel()
	participants := []component.Kind{
		component.KindPodClique, component.KindPodCliqueScalingGroup, component.KindHeadlessService,
		component.KindPodGang, component.KindHorizontalPodAutoscaler, component.KindPodGangMap,
		component.KindResourceClaim, component.KindComputeDomain,
	}
	tests := []struct {
		name                     string
		pendingParticipant       component.Kind
		missingParticipant       component.Kind
		notApplicableParticipant component.Kind
		wantConfirmed            bool
	}{
		{name: "every applicable participant confirms source cleanup", wantConfirmed: true},
		{name: "terminating ResourceClaims keep the source pending", pendingParticipant: component.KindResourceClaim},
		{name: "terminating ComputeDomains keep the source pending", pendingParticipant: component.KindComputeDomain},
		{name: "terminating PodGangMaps keep the source pending", pendingParticipant: component.KindPodGangMap},
		{name: "missing ResourceClaim result cannot release the source", missingParticipant: component.KindResourceClaim},
		{name: "missing ComputeDomain result cannot release the source", missingParticipant: component.KindComputeDomain},
		{name: "missing PodGangMap result cannot release the source", missingParticipant: component.KindPodGangMap},
		{name: "non-applicable ResourceClaims do not block source cleanup", notApplicableParticipant: component.KindResourceClaim, wantConfirmed: true},
		{name: "non-applicable ComputeDomains do not block source cleanup", notApplicableParticipant: component.KindComputeDomain, wantConfirmed: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			registry := component.NewOperatorRegistry[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result]()
			syncResults := make(map[component.Kind]pcssync.Result, len(participants))
			for _, kind := range participants {
				registry.Register(kind, nil, component.AsReplicaScoped())
				switch kind {
				case tc.missingParticipant:
					continue
				case tc.pendingParticipant:
					syncResults[kind] = pcssync.Result{CleanedUpReplicaIndices: []int{1}}
				case tc.notApplicableParticipant:
					syncResults[kind] = pcssync.Result{NotApplicable: true}
				default:
					syncResults[kind] = pcssync.Result{CleanedUpReplicaIndices: []int{1, 2}}
				}
			}
			reconciler := &Reconciler{operatorRegistry: registry}
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
					RequiresReplicaRecreation: true,
					CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
						ReplicaIndex: 2,
						Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
					}},
				}).Build()
			before := pcs.DeepCopy()

			confirmed := reconciler.cleanedUpScaleInReplicaIndices(syncResults)
			recordReplacementCleanupWork(logr.Discard(), pcs, confirmed)

			assert.Equal(t, tc.wantConfirmed, confirmed.Has(2), "every applicable participant must confirm source 2")
			require.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1)
			progress := &pcs.Status.UpdateProgress.CurrentlyUpdating[0]
			if tc.wantConfirmed {
				require.NotNil(t, progress.Replacement.SourceCleanupCompletedAt)
				assert.False(t, progress.Replacement.SourceCleanupCompletedAt.IsZero())
				before.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.SourceCleanupCompletedAt = progress.Replacement.SourceCleanupCompletedAt.DeepCopy()
			} else {
				assert.Nil(t, progress.Replacement.SourceCleanupCompletedAt)
			}
			assert.Nil(t, progress.UpdateEndedAt, "cleanup confirmation must not stamp replacement completion")
			assert.Equal(t, before, pcs, "source cleanup must preserve the mapping and all other progress")
		})
	}
}

// TestRetiredSourceCleanupRequiresParticipantConfirmation verifies that every applicable replica-scoped
// component must confirm cleanup. Missing results and partial confirmation preserve the cleanup obligation.
func TestRetiredSourceCleanupRequiresParticipantConfirmation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		results     map[component.Kind]pcssync.Result
		wantPending []int32
	}{
		{
			name: "every applicable participant confirms the retired source",
			results: map[component.Kind]pcssync.Result{
				component.KindPodClique:       {CleanedUpReplicaIndices: []int{2}},
				component.KindHeadlessService: {CleanedUpReplicaIndices: []int{2}},
				component.KindPodGang:         {CleanedUpReplicaIndices: []int{2}},
			},
		},
		{
			name: "missing participant result preserves the retired source",
			results: map[component.Kind]pcssync.Result{
				component.KindPodClique:       {CleanedUpReplicaIndices: []int{2}},
				component.KindHeadlessService: {CleanedUpReplicaIndices: []int{2}},
			},
			wantPending: []int32{2},
		},
		{
			name: "one participant still owning resources preserves the retired source",
			results: map[component.Kind]pcssync.Result{
				component.KindPodClique:       {CleanedUpReplicaIndices: []int{2}},
				component.KindHeadlessService: {CleanedUpReplicaIndices: []int{2}},
				component.KindPodGang:         {},
			},
			wantPending: []int32{2},
		},
		{
			name: "reported non-applicable participants do not block cleanup",
			results: map[component.Kind]pcssync.Result{
				component.KindPodClique:       {CleanedUpReplicaIndices: []int{2}},
				component.KindHeadlessService: {CleanedUpReplicaIndices: []int{2}},
				component.KindPodGang:         {NotApplicable: true},
			},
		},
		{
			name: "non-participants cannot provide the missing confirmation",
			results: map[component.Kind]pcssync.Result{
				component.KindPodClique:       {CleanedUpReplicaIndices: []int{2}},
				component.KindHeadlessService: {},
				component.KindPodGang:         {CleanedUpReplicaIndices: []int{2}},
				component.KindRole:            {CleanedUpReplicaIndices: []int{2}},
			},
			wantPending: []int32{2},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			registry := component.NewOperatorRegistry[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result]()
			registry.Register(component.KindPodClique, nil, component.AsReplicaScoped())
			registry.Register(component.KindHeadlessService, nil, component.AsReplicaScoped())
			registry.Register(component.KindPodGang, nil, component.AsReplicaScoped())
			registry.Register(component.KindRole, nil)
			reconciler := &Reconciler{operatorRegistry: registry}
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
					CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
						ReplicaIndex: 1,
						Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
							TargetReplicaIndex: 1, RetiredSourceReplicaIndices: []int32{2},
						},
					}},
				}).Build()

			confirmed := reconciler.cleanedUpScaleInReplicaIndices(tc.results)
			recordReplacementCleanupWork(logr.Discard(), pcs, confirmed)

			assert.Nil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.SourceCleanupCompletedAt, "retired-source confirmation does not confirm the current source")
			pending := pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.RetiredSourceReplicaIndices
			if len(tc.wantPending) == 0 {
				assert.Empty(t, pending)
			} else {
				assert.Equal(t, tc.wantPending, pending)
			}
		})
	}
}

// TestRecordReplacementCleanupWorkRetiredSources covers retirement-only updates, cancellation retention,
// removal after complete confirmation, and entries excluded from cleanup processing.
func TestRecordReplacementCleanupWorkRetiredSources(t *testing.T) {
	t.Parallel()
	canceledAt := metav1.NewTime(time.Unix(150, 0).UTC())
	t.Run("partial cancellation cleanup retains the entry and remaining obligation", func(t *testing.T) {
		t.Parallel()
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").WithUpdateProgress(
			&grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex: 3,
				Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
					TargetReplicaIndex: 2, CanceledAt: &canceledAt, RetiredSourceReplicaIndices: []int32{4, 5},
				},
			}}},
		).Build()

		recordReplacementCleanupWork(logr.Discard(), pcs, sets.New[int32](2, 3, 4))

		require.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1)
		assert.Equal(t, []int32{5}, pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.RetiredSourceReplicaIndices)
	})
	t.Run("complete cancellation cleanup removes the entry in the same sweep", func(t *testing.T) {
		t.Parallel()
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").WithUpdateProgress(
			&grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex: 3,
				Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
					TargetReplicaIndex: 2, CanceledAt: &canceledAt, RetiredSourceReplicaIndices: []int32{4, 5},
				},
			}}},
		).Build()

		recordReplacementCleanupWork(logr.Discard(), pcs, sets.New[int32](2, 3, 4, 5))

		assert.Empty(t, pcs.Status.UpdateProgress.CurrentlyUpdating)
	})
	t.Run("different-index retirement changes progress without source or cancellation flags", func(t *testing.T) {
		t.Parallel()
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").WithUpdateProgress(
			&grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex: 3,
				Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
					TargetReplicaIndex: 2, RetiredSourceReplicaIndices: []int32{4},
				},
			}}},
		).Build()

		recordReplacementCleanupWork(logr.Discard(), pcs, sets.New[int32](4))

		require.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1)
		assert.Empty(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.RetiredSourceReplicaIndices)
		assert.Nil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.SourceCleanupCompletedAt)
	})
	for _, tc := range []struct {
		name     string
		progress *grovecorev1beta1.PodCliqueSetUpdateProgress
	}{
		{name: "no progress remains untouched"},
		{name: "empty progress remains untouched", progress: &grovecorev1beta1.PodCliqueSetUpdateProgress{}},
		{
			name:     "ordinary entry remains untouched",
			progress: &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 1}}},
		},
		{
			name: "completed entry remains untouched",
			progress: &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex: 1, UpdateEndedAt: &canceledAt,
				Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
			}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").WithUpdateProgress(tc.progress.DeepCopy()).Build()
			before := pcs.DeepCopy()

			recordReplacementCleanupWork(logr.Discard(), pcs, sets.New[int32](1))

			assert.Equal(t, before, pcs)
		})
	}
}

// TestCommitPCSCleanupStatusRetiredSources covers retirement-only status patches, skipped no-change patches,
// and optimistic-lock failures. Retirement must not emit ordinary scale-in events or disappear on failure.
func TestCommitPCSCleanupStatusRetiredSources(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                       string
		confirmedPCSReplicaIndices sets.Set[int32]
		rejectPatch                bool
		wantPending                []int32
		wantChanged                bool
		wantError                  bool
	}{
		{name: "unchanged retirement skips the patch", rejectPatch: true, wantPending: []int32{3, 4}},
		{name: "unrelated confirmation skips the patch", confirmedPCSReplicaIndices: sets.New[int32](99), rejectPatch: true, wantPending: []int32{3, 4}},
		{name: "partial retirement persists without other cleanup flags", confirmedPCSReplicaIndices: sets.New[int32](3), wantPending: []int32{4}, wantChanged: true},
		{name: "complete retirement persists without other cleanup flags", confirmedPCSReplicaIndices: sets.New[int32](3, 4), wantChanged: true},
		{name: "failed partial retirement patch preserves server obligations", confirmedPCSReplicaIndices: sets.New[int32](3), rejectPatch: true, wantPending: []int32{3, 4}, wantError: true},
		{name: "failed complete retirement patch preserves server obligations", confirmedPCSReplicaIndices: sets.New[int32](3, 4), rejectPatch: true, wantPending: []int32{3, 4}, wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").WithUpdateProgress(
				&grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
					ReplicaIndex: 2,
					Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
						TargetReplicaIndex: 1, RetiredSourceReplicaIndices: []int32{3, 4},
					},
				}}},
			).Build()
			conflict := apierrors.NewConflict(schema.GroupResource{Group: "grove.io", Resource: "podcliquesets"}, pcs.Name, errors.New("stale resource version"))
			clientBuilder := testutils.NewTestClientBuilder().WithObjects(pcs).WithStatusSubresource(pcs)
			if tc.rejectPatch {
				clientBuilder = clientBuilder.RecordErrorForObjects(testutils.ClientMethodStatusPatch, conflict, client.ObjectKeyFromObject(pcs))
			}
			fakeClient := clientBuilder.Build()
			events := record.NewFakeRecorder(10)
			currentlyUpdatingExpectations := &pcsexpectations.CurrentlyUpdating{}
			reconciler := &Reconciler{
				client:                        fakeClient,
				eventRecorder:                 events,
				scalingProgressExpectations:   &pcsexpectations.ScalingProgress{},
				currentlyUpdatingExpectations: currentlyUpdatingExpectations,
			}
			originalPCS := pcs.DeepCopy()
			recordReplacementCleanupWork(logr.Discard(), pcs, tc.confirmedPCSReplicaIndices)
			require.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1)
			assert.Nil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.SourceCleanupCompletedAt)
			assert.Nil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.CanceledAt)

			changed, err := reconciler.commitPCSCleanupStatus(t.Context(), logr.Discard(), pcs, originalPCS, nil)

			assert.Equal(t, tc.wantChanged, changed)
			if tc.wantError {
				assert.ErrorIs(t, err, conflict)
			} else {
				require.NoError(t, err)
			}
			storedPCS := &grovecorev1beta1.PodCliqueSet{}
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), storedPCS))
			require.Len(t, storedPCS.Status.UpdateProgress.CurrentlyUpdating, 1)
			pending := storedPCS.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.RetiredSourceReplicaIndices
			if len(tc.wantPending) == 0 {
				assert.Empty(t, pending)
			} else {
				assert.Equal(t, tc.wantPending, pending)
			}
			assert.Nil(t, storedPCS.Status.ScalingProgress, "retirement must not create ordinary scaling progress")
			assert.Empty(t, events.Events, "retirement must not emit an ordinary scale-in completion event")
			wrongSelection := []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 99}}
			if tc.wantChanged {
				assert.False(t, currentlyUpdatingExpectations.ClearIfSatisfied(pcs.ObjectMeta, wrongSelection), "successful patches must record an expectation")
				assert.True(t, currentlyUpdatingExpectations.ClearIfSatisfied(pcs.ObjectMeta, storedPCS.Status.UpdateProgress.CurrentlyUpdating))
			} else {
				assert.True(t, currentlyUpdatingExpectations.ClearIfSatisfied(pcs.ObjectMeta, wrongSelection), "failed or skipped patches must leave no expectation")
			}
		})
	}
}

// TestCommitPCSCleanupStatus covers skipped no-change patches and persistence of actual scaling and update
// mutations. Stored status and completion events must match the cleanup that was performed.
func TestCommitPCSCleanupStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                       string
		confirmedPCSReplicaIndices sets.Set[int32]
		wantChanged                bool
	}{
		{name: "persists nothing when no cleanup occurred"},
		{name: "persists scaling and update progress and emits the real scale-in event", confirmedPCSReplicaIndices: sets.New[int32](1, 3), wantChanged: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithScalingProgress(&grovecorev1beta1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{3}}).
				WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
					CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
						ReplicaIndex: 1,
						Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
					}},
				}).Build()
			fakeClient := testutils.NewTestClientBuilder().WithObjects(pcs).WithStatusSubresource(pcs).Build()
			events := record.NewFakeRecorder(10)
			reconciler := &Reconciler{
				client:                        fakeClient,
				eventRecorder:                 events,
				scalingProgressExpectations:   &pcsexpectations.ScalingProgress{},
				currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{},
			}
			originalPCS := pcs.DeepCopy()
			completedScaleInReplicaIndices := removeCompletedScaleInPCSReplicaIndices(logr.Discard(), pcs, tc.confirmedPCSReplicaIndices)
			recordReplacementCleanupWork(logr.Discard(), pcs, tc.confirmedPCSReplicaIndices)

			changed, err := reconciler.commitPCSCleanupStatus(t.Context(), logr.Discard(), pcs, originalPCS, completedScaleInReplicaIndices)

			require.NoError(t, err)
			assert.Equal(t, tc.wantChanged, changed)
			storedPCS := &grovecorev1beta1.PodCliqueSet{}
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), storedPCS))
			if tc.wantChanged {
				assert.Nil(t, storedPCS.Status.ScalingProgress)
				require.Len(t, storedPCS.Status.UpdateProgress.CurrentlyUpdating, 1)
				assert.NotNil(t, storedPCS.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.SourceCleanupCompletedAt)
				assert.Len(t, events.Events, 1)
				assert.Equal(t, "Normal PodCliqueSetReplicaScaleInCompleted PCS replica index 3 completed scale-in", <-events.Events)
			} else {
				assert.Equal(t, originalPCS.Status, storedPCS.Status)
				assert.Empty(t, events.Events)
			}
		})
	}
}

// TestCommitPCSCleanupStatusSnapshotDifferences verifies that equal list lengths do not hide changed indices.
// Completion-event metadata alone must not trigger a patch when both status snapshots are unchanged.
func TestCommitPCSCleanupStatusSnapshotDifferences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                           string
		beforeScaling                  *grovecorev1beta1.PodCliqueSetScalingProgress
		afterScaling                   *grovecorev1beta1.PodCliqueSetScalingProgress
		beforeUpdate                   *grovecorev1beta1.PodCliqueSetUpdateProgress
		afterUpdate                    *grovecorev1beta1.PodCliqueSetUpdateProgress
		completedScaleInReplicaIndices []int32
		wantChanged                    bool
	}{
		{
			name:          "different scaling indices with equal lengths require a patch",
			beforeScaling: &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{3}},
			afterScaling:  &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{4}},
			wantChanged:   true,
		},
		{
			name:         "different update selections with equal lengths require a patch",
			beforeUpdate: &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 1}}},
			afterUpdate:  &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 2}}},
			wantChanged:  true,
		},
		{
			name: "different retired indices with equal lengths require a patch",
			beforeUpdate: &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex: 2, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1, RetiredSourceReplicaIndices: []int32{3}},
			}}},
			afterUpdate: &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex: 2, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1, RetiredSourceReplicaIndices: []int32{4}},
			}}},
			wantChanged: true,
		},
		{
			name:                           "completion metadata cannot force a patch without status differences",
			beforeScaling:                  &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{3}},
			afterScaling:                   &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{3}},
			completedScaleInReplicaIndices: []int32{3},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithScalingProgress(tc.beforeScaling.DeepCopy()).
				WithUpdateProgress(tc.beforeUpdate.DeepCopy()).Build()
			clientBuilder := testutils.NewTestClientBuilder().WithObjects(pcs).WithStatusSubresource(pcs)
			if !tc.wantChanged {
				clientBuilder = clientBuilder.RecordErrorForObjects(testutils.ClientMethodStatusPatch, apierrors.NewInternalError(errors.New("unexpected status patch")), client.ObjectKeyFromObject(pcs))
			}
			fakeClient := clientBuilder.Build()
			events := record.NewFakeRecorder(10)
			reconciler := &Reconciler{
				client: fakeClient, eventRecorder: events,
				scalingProgressExpectations:   &pcsexpectations.ScalingProgress{},
				currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{},
			}
			originalPCS := pcs.DeepCopy()
			pcs.Status.ScalingProgress = tc.afterScaling.DeepCopy()
			pcs.Status.UpdateProgress = tc.afterUpdate.DeepCopy()

			changed, err := reconciler.commitPCSCleanupStatus(t.Context(), logr.Discard(), pcs, originalPCS, tc.completedScaleInReplicaIndices)

			require.NoError(t, err)
			assert.Equal(t, tc.wantChanged, changed)
			storedPCS := &grovecorev1beta1.PodCliqueSet{}
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), storedPCS))
			assert.Equal(t, tc.afterScaling, storedPCS.Status.ScalingProgress)
			assert.Equal(t, tc.afterUpdate, storedPCS.Status.UpdateProgress)
			assert.Empty(t, events.Events, "no actual scale-in completion occurred")
		})
	}
}

func TestCommitPCSCleanupStatusPatchFailure(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
	pcs.Status.ScalingProgress = &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{1}}
	conflict := apierrors.NewConflict(
		schema.GroupResource{Group: "grove.io", Resource: "podcliquesets"},
		pcs.Name,
		errors.New("stale resource version"),
	)
	fakeClient := testutils.NewTestClientBuilder().
		WithObjects(pcs).
		WithStatusSubresource(pcs).
		RecordErrorForObjects(testutils.ClientMethodStatusPatch, conflict, client.ObjectKeyFromObject(pcs)).
		Build()
	scalingProgressExpectations := &pcsexpectations.ScalingProgress{}
	reconciler := &Reconciler{
		client:                        fakeClient,
		eventRecorder:                 record.NewFakeRecorder(10),
		scalingProgressExpectations:   scalingProgressExpectations,
		currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{},
	}
	originalPCS := pcs.DeepCopy()

	completedScaleInReplicaIndices := removeCompletedScaleInPCSReplicaIndices(logr.Discard(), pcs, sets.New[int32](1))
	changed, err := reconciler.commitPCSCleanupStatus(t.Context(), logr.Discard(), pcs, originalPCS, completedScaleInReplicaIndices)

	assert.False(t, changed)
	assert.ErrorIs(t, err, conflict)
	// A failed patch forgets the expectation, so there is nothing left to satisfy.
	assert.True(t, scalingProgressExpectations.ClearIfSatisfied(pcs.ObjectMeta, pcs.Status.ScalingProgress))
}

func TestProcessKindGroupRunResultErrors(t *testing.T) {
	const (
		podTaskName     = "SyncKind-Pod"
		serviceTaskName = "SyncKind-HeadlessService"
	)
	kindByTaskName := map[string]component.Kind{
		podTaskName:     component.KindPod,
		serviceTaskName: component.KindHeadlessService,
	}
	reconciliationErr := errors.New("sync failed")

	tests := []struct {
		name                     string
		runResult                utils.RunResult
		wantResult               bool
		wantErrors               bool
		wantRequeueAfter         time.Duration
		wantErrorIs              error
		wantContinueRequeueKinds []component.Kind
	}{
		{
			name: "continue and requeue failure",
			runResult: utils.RunResult{FailedTasks: []utils.FailedTask{{
				Name: podTaskName,
				Err:  groveerr.NewContinueReconcileAndRequeue(component.OperationSync, "continue"),
			}}},
			wantContinueRequeueKinds: []component.Kind{component.KindPod},
		},
		{
			name: "shortest timed requeue wins",
			runResult: utils.RunResult{FailedTasks: []utils.FailedTask{
				{Name: podTaskName, Err: groveerr.NewRequeueAfter(component.OperationSync, "later", 5*time.Second)},
				{Name: serviceTaskName, Err: groveerr.NewRequeueAfter(component.OperationSync, "sooner", 2*time.Second)},
			}},
			wantResult:       true,
			wantRequeueAfter: 2 * time.Second,
		},
		{
			name: "reconciliation error takes precedence over timed requeue",
			runResult: utils.RunResult{FailedTasks: []utils.FailedTask{
				{Name: podTaskName, Err: groveerr.NewRequeueAfter(component.OperationSync, "retry", time.Second)},
				{Name: serviceTaskName, Err: reconciliationErr},
			}},
			wantResult:  true,
			wantErrors:  true,
			wantErrorIs: reconciliationErr,
		},
		{
			name: "unknown task is a reconciliation error",
			runResult: utils.RunResult{FailedTasks: []utils.FailedTask{{
				Name: "unknown",
				Err:  reconciliationErr,
			}}},
			wantResult:  true,
			wantErrors:  true,
			wantErrorIs: reconciliationErr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, continueRequeueKinds := processKindGroupRunResultErrors(logr.Discard(), tt.runResult, kindByTaskName)

			assert.ElementsMatch(t, tt.wantContinueRequeueKinds, continueRequeueKinds)
			if !tt.wantResult {
				assert.Nil(t, result)
				return
			}
			require.NotNil(t, result)
			assert.Equal(t, tt.wantErrors, result.HasErrors())
			ctrlResult, err := result.Result()
			assert.Equal(t, tt.wantRequeueAfter, ctrlResult.RequeueAfter)
			if tt.wantErrorIs != nil {
				assert.ErrorIs(t, err, tt.wantErrorIs)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestUpdateObservedGeneration tests the updateObservedGeneration function for PodCliqueSet
func TestUpdateObservedGeneration(t *testing.T) {
	tests := []struct {
		name               string
		setupPCS           func() *grovecorev1beta1.PodCliqueSet
		expectPatchSkipped bool
		expectedGeneration int64
	}{
		{
			name: "patch_skipped_when_observed_equals_generation",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(1).
					Build()
				pcs.Generation = 5
				pcs.Status.ObservedGeneration = ptr.To(int64(5))
				return pcs
			},
			expectPatchSkipped: true,
			expectedGeneration: 5,
		},
		{
			name: "patch_made_when_observed_is_nil",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(1).
					Build()
				pcs.Generation = 3
				pcs.Status.ObservedGeneration = nil
				return pcs
			},
			expectPatchSkipped: false,
			expectedGeneration: 3,
		},
		{
			name: "patch_made_when_observed_differs_from_generation",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(1).
					Build()
				pcs.Generation = 7
				pcs.Status.ObservedGeneration = ptr.To(int64(4))
				return pcs
			},
			expectPatchSkipped: false,
			expectedGeneration: 7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs := tt.setupPCS()
			originalObservedGen := pcs.Status.ObservedGeneration

			fakeClient := testutils.SetupFakeClient(pcs)
			reconciler := &Reconciler{client: fakeClient, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}

			result := reconciler.updateObservedGeneration(context.Background(), logr.Discard(), pcs)

			require.False(t, result.HasErrors(), "updateObservedGeneration should not return errors")

			// Verify the result continues reconciliation
			_, err := result.Result()
			assert.NoError(t, err)

			// Fetch the updated object from the fake client
			updatedPCS := &grovecorev1beta1.PodCliqueSet{}
			err = fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), updatedPCS)
			require.NoError(t, err)

			// Verify ObservedGeneration is set correctly
			require.NotNil(t, updatedPCS.Status.ObservedGeneration, "ObservedGeneration should not be nil after update")
			assert.Equal(t, tt.expectedGeneration, *updatedPCS.Status.ObservedGeneration)

			if tt.expectPatchSkipped {
				// If patch was skipped, the ObservedGeneration should remain unchanged
				assert.Equal(t, originalObservedGen, updatedPCS.Status.ObservedGeneration)
			}
		})
	}
}

// TestGetKindSyncGroups tests that every expected component kind appears in exactly one
// sync group and that the number of groups is at least 1.
func TestGetKindSyncGroups(t *testing.T) {
	groups := getKindSyncGroups()
	assert.NotEmpty(t, groups)

	expectedKinds := []component.Kind{
		component.KindServiceAccount,
		component.KindRole,
		component.KindRoleBinding,
		component.KindServiceAccountTokenSecret,
		component.KindHeadlessService,
		component.KindHorizontalPodAutoscaler,
		component.KindPodCliqueSetReplica,
		component.KindComputeDomain,
		component.KindResourceClaim,
		component.KindPodClique,
		component.KindPodCliqueScalingGroup,
		component.KindPodGang,
		component.KindPodGangMap,
		component.KindPodGangMigrator,
	}

	actualKinds := make([]component.Kind, 0)
	seen := make(map[component.Kind]bool)
	for _, group := range groups {
		for _, k := range group {
			assert.False(t, seen[k], "kind %s appeared in more than one group", k)
			seen[k] = true
			actualKinds = append(actualKinds, k)
		}
	}
	// Exact set equality: a kind added to or removed from the sync groups without updating
	// expectedKinds must fail this test.
	assert.ElementsMatch(t, expectedKinds, actualKinds)
}

// TestInitOrResetUpdate tests the initOrResetUpdate function for both OnDelete and RollingRecreate strategies
func TestInitOrResetUpdate(t *testing.T) {
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

			fakeClient := testutils.SetupFakeClient(pcs)
			reconciler := &Reconciler{
				client:                        fakeClient,
				currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{},
			}

			newGenerationHash := "new-hash"

			targetSchedulingConfigurationHash := "target-scheduling-hash"
			err := reconciler.initOrResetUpdate(context.Background(), pcs, newGenerationHash, targetSchedulingConfigurationHash)
			require.NoError(t, err, "initOrResetUpdate should not return errors")

			// Fetch the updated PCS from the fake client
			updatedPCS := &grovecorev1beta1.PodCliqueSet{}
			err = fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), updatedPCS)
			require.NoError(t, err)

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
			assert.Equal(t, int32(0), updatedPCS.Status.UpdatedReplicas, "UpdatedReplicas should be reset to 0")
			require.NotNil(t, updatedPCS.Status.CurrentGenerationHash)
			assert.Equal(t, newGenerationHash, *updatedPCS.Status.CurrentGenerationHash)
		})
	}
}

// TestInitOrResetUpdateFailureIsolationAndRetry verifies that a failed supersession status update stops
// reconciliation without persisting prepared replacement progress. Error recording preserves server cleanup
// obligations, and a retry from freshly fetched status performs the handoff exactly once.
func TestInitOrResetUpdateFailureIsolationAndRetry(t *testing.T) {
	t.Parallel()
	cleanupCompletedAt := metav1.NewTime(time.Unix(100, 0).UTC())
	tests := []struct {
		name                               string
		progress                           grovecorev1beta1.PodCliqueSetReplicaUpdateProgress
		wantRetiredSourcePCSReplicaIndices []int32
	}{
		{
			name: "different-index handoff is not persisted by error recording",
			progress: grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				ReplicaIndex: 2,
				Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
			},
			wantRetiredSourcePCSReplicaIndices: []int32{2},
		},
		{
			name: "same-index barrier and existing retired obligations survive failure",
			progress: grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				ReplicaIndex: 1,
				Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
					TargetReplicaIndex:          1,
					RetiredSourceReplicaIndices: []int32{2, 4},
					SourceCleanupCompletedAt:    &cleanupCompletedAt,
				},
			},
			wantRetiredSourcePCSReplicaIndices: []int32{2, 4},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "test-uid").
				WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithMinAvailable(1).Build()).
				WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.CoherentStrategy}).
				WithPodCliqueSetGenerationHash(new("old-generation")).
				WithAppliedSchedulingConfigurationHash(new("applied-scheduling")).
				WithLastErrors(grovecorev1beta1.LastError{
					Code: "PREVIOUS_ERROR", Description: "previous reconciliation failed", ObservedAt: cleanupCompletedAt,
				}).
				WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
					RequiresReplicaRecreation:         true,
					TargetSchedulingConfigurationHash: "superseded-scheduling",
					UpdateStartedAt:                   cleanupCompletedAt,
					CurrentlyUpdating:                 []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{*tc.progress.DeepCopy()},
				}).Build()
			conflict := apierrors.NewConflict(schema.GroupResource{Group: "grove.io", Resource: "podcliquesets"}, pcs.Name, errors.New("stale resource version"))
			fakeClient := testutils.NewTestClientBuilder().WithObjects(pcs).WithStatusSubresource(pcs).
				RecordErrorForObjectsNTimes(testutils.ClientMethodStatusUpdate, conflict, 1, client.ObjectKeyFromObject(pcs)).Build()
			currentlyUpdatingExpectations := &pcsexpectations.CurrentlyUpdating{}
			reconciler := &Reconciler{
				client:                        fakeClient,
				currentlyUpdatingExpectations: currentlyUpdatingExpectations,
				reconcileStatusRecorder:       ctrlcommon.NewReconcileErrorRecorder(fakeClient),
			}
			before := pcs.DeepCopy()
			_, targetSchedulingHash, targetGenerationHash := computePCSHashes(pcs)

			failedResult := reconciler.processGenerationHashChange(t.Context(), logr.Discard(), pcs)

			require.True(t, failedResult.HasErrors())
			assert.True(t, ctrlcommon.ShortCircuitReconcileFlow(failedResult), "failed initialization must stop the spec reconciliation before child sync")
			assert.ErrorIs(t, errors.Join(failedResult.GetErrors()...), conflict)
			assert.Equal(t, targetGenerationHash, *pcs.Status.CurrentGenerationHash, "initialization prepares the new generation in memory before the write fails")
			assert.False(t, apiequality.Semantic.DeepEqual(before.Status.UpdateProgress, pcs.Status.UpdateProgress))
			assert.True(t, reconciler.pcsGenerationStatusExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil, nil), "a failed status write must not record a generation expectation")
			assert.True(t, currentlyUpdatingExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil), "a failed status write must not record an update-selection expectation")

			require.NoError(t, reconciler.reconcileStatusRecorder.RecordErrors(t.Context(), pcs, &failedResult))

			storedPCS := &grovecorev1beta1.PodCliqueSet{}
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), storedPCS))
			assert.Equal(t, before.Status.CurrentGenerationHash, storedPCS.Status.CurrentGenerationHash)
			assert.Equal(t, before.Status.AppliedSchedulingConfigurationHash, storedPCS.Status.AppliedSchedulingConfigurationHash)
			assert.True(t, apiequality.Semantic.DeepEqual(before.Status.UpdateProgress, storedPCS.Status.UpdateProgress), "the LastErrors patch must preserve the original replacement mapping, barrier, and retired sources")
			// The recorder maps only GroveError values. This path returns a plain wrapped API conflict,
			// so it clears the previous error rather than recording the current one.
			assert.Empty(t, storedPCS.Status.LastErrors)
			require.Len(t, before.Status.LastErrors, 1)
			assert.Equal(t, before.Status.ScalingProgress, storedPCS.Status.ScalingProgress)

			retryResult := reconciler.processGenerationHashChange(t.Context(), logr.Discard(), storedPCS)

			require.False(t, retryResult.HasErrors())
			updatedPCS := &grovecorev1beta1.PodCliqueSet{}
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), updatedPCS))
			assert.Equal(t, targetGenerationHash, *updatedPCS.Status.CurrentGenerationHash)
			assert.Equal(t, before.Status.AppliedSchedulingConfigurationHash, updatedPCS.Status.AppliedSchedulingConfigurationHash)
			require.NotNil(t, updatedPCS.Status.UpdateProgress)
			assert.True(t, updatedPCS.Status.UpdateProgress.RequiresReplicaRecreation)
			assert.Equal(t, targetSchedulingHash, updatedPCS.Status.UpdateProgress.TargetSchedulingConfigurationHash)
			assert.Equal(t, before.Status.CurrentGenerationHash, updatedPCS.Status.UpdateProgress.PreviousPodCliqueSetGenerationHash)
			require.Len(t, updatedPCS.Status.UpdateProgress.CurrentlyUpdating, 1)
			updatedProgress := updatedPCS.Status.UpdateProgress.CurrentlyUpdating[0]
			assert.Equal(t, int32(1), updatedProgress.ReplicaIndex)
			require.NotNil(t, updatedProgress.Replacement)
			assert.Equal(t, int32(1), updatedProgress.Replacement.TargetReplicaIndex)
			assert.Equal(t, tc.wantRetiredSourcePCSReplicaIndices, updatedProgress.Replacement.RetiredSourceReplicaIndices)
			assert.Nil(t, updatedProgress.Replacement.SourceCleanupCompletedAt)
			assert.Nil(t, updatedProgress.UpdateEndedAt)
			assert.False(t, reconciler.pcsGenerationStatusExpectations.ClearIfSatisfied(pcs.ObjectMeta, before.Status.CurrentGenerationHash, before.Status.AppliedSchedulingConfigurationHash))
			assert.True(t, reconciler.pcsGenerationStatusExpectations.ClearIfSatisfied(updatedPCS.ObjectMeta, updatedPCS.Status.CurrentGenerationHash, updatedPCS.Status.AppliedSchedulingConfigurationHash))
			assert.False(t, currentlyUpdatingExpectations.ClearIfSatisfied(updatedPCS.ObjectMeta, before.Status.UpdateProgress.CurrentlyUpdating))
			assert.True(t, currentlyUpdatingExpectations.ClearIfSatisfied(updatedPCS.ObjectMeta, updatedPCS.Status.UpdateProgress.CurrentlyUpdating))
		})
	}
}

func TestInitOrResetUpdatePreservesReplicaRecreationAcrossSchedulingRollback(t *testing.T) {
	sourceCleanupCompletedAt := metav1.Now()
	replacement := &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
		TargetReplicaIndex:       1,
		SourceCleanupCompletedAt: &sourceCleanupCompletedAt,
	}
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithReplicas(2).
		WithPodCliqueParameters("worker", 1, nil).
		WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.CoherentStrategy}).
		WithPodCliqueSetGenerationHash(new("old-hash")).
		WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
			RequiresReplicaRecreation:         true,
			TargetSchedulingConfigurationHash: "superseded-scheduling-hash",
			CurrentlyUpdating:                 []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 1, Replacement: replacement}},
		}).
		Build()
	pcs.Status.AppliedSchedulingConfigurationHash = new("applied-scheduling-hash")
	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{client: fakeClient, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}

	err := r.initOrResetUpdate(context.Background(), pcs, "new-hash", "applied-scheduling-hash")

	require.NoError(t, err)
	updated := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), updated))
	require.NotNil(t, updated.Status.UpdateProgress)
	assert.True(t, updated.Status.UpdateProgress.RequiresReplicaRecreation)
	assert.Empty(t, updated.Status.UpdateProgress.InScopeStandalonePodCliques)
	require.Len(t, updated.Status.UpdateProgress.CurrentlyUpdating, 1)
	assert.Equal(t, int32(1), updated.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex)
	updatedReplacement := updated.Status.UpdateProgress.CurrentlyUpdating[0].Replacement
	require.NotNil(t, updatedReplacement)
	assert.Equal(t, int32(1), updatedReplacement.TargetReplicaIndex)
	assert.Nil(t, updatedReplacement.SourceCleanupCompletedAt)
	assert.Equal(t, &sourceCleanupCompletedAt, replacement.SourceCleanupCompletedAt)
	assert.True(t, r.currentlyUpdatingExpectations.ClearIfSatisfied(updated.ObjectMeta, updated.Status.UpdateProgress.CurrentlyUpdating))
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
			name: "ordinary selection committed to scale-in is excluded",
			updateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{ordinaryProgress},
			},
			scalingProgress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleInReplicaIndices: []int32{2},
			},
		},
		{
			name: "ordinary replica zero committed to scale-in is excluded",
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

			got := prepareReplicaSelectionsForUpdate(pcs, updateStartedAt)

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

			got := prepareReplicaSelectionsForUpdate(pcs, updateStartedAt)

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
// order. It also covers sorting after completed selections and ordinary scale-in selections are excluded.
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

			got := prepareReplicaSelectionsForUpdate(pcs, updateStartedAt)

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
			assert.Equal(t, want, got)
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

		got := prepareReplicaSelectionsForUpdate(pcs, updateStartedAt)

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

			got := prepareReplicaSelectionsForUpdate(pcs, updateStartedAt)
			again := prepareReplicaSelectionsForUpdate(pcs, updateStartedAt)

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

// TestProcessGenerationHashChangeNotBlockedByStaleSameNameExpectation verifies that an expectation
// left by a previously deployed PodCliqueSet does not block a newly created
// PodCliqueSet with a different UID but with the same name as the previously deployed PodCliqueSet.
func TestProcessGenerationHashChangeNotBlockedByStaleSameNameExpectation(t *testing.T) {
	newPCS := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid-new").
		WithPodCliqueParameters("worker", 1, nil).
		Build()

	fakeClient := testutils.SetupFakeClient(newPCS)
	r := &Reconciler{client: fakeClient, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
	// A previous incarnation with a different UID left a stale expectation in the map.
	stalePCS := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid-old").Build()
	r.pcsGenerationStatusExpectations.Record(stalePCS.ObjectMeta, "stale-hash", nil)

	result := r.processGenerationHashChange(context.Background(), logr.Discard(), newPCS)

	require.False(t, result.HasErrors())
	require.True(t, result.NeedsRequeue())
	assert.True(t, ctrlcommon.ShortCircuitReconcileFlow(result))
	requeueResult, err := result.Result()
	require.NoError(t, err)
	assert.Equal(t, constants.InformerCacheSyncRetryInterval, requeueResult.RequeueAfter)
	updated := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(newPCS), updated))
	require.NotNil(t, updated.Status.CurrentGenerationHash, "generation hash should be set on the new PCS")
	assert.Equal(t, computeGenerationHash(newPCS), *updated.Status.CurrentGenerationHash)
	require.NotNil(t, updated.Status.AppliedSchedulingConfigurationHash)
	assert.Equal(t, computeSchedulingConfigurationHash(buildPCSSchedulingConfig(newPCS)), *updated.Status.AppliedSchedulingConfigurationHash)
	assert.Nil(t, updated.Status.UpdateProgress)
}

// TestSchedulingChangeAtZeroReplicasConsumesInitializationBeforeCompletion exercises the real replica
// orchestrator and PodClique component. Initialization must stop before zero-replica completion can advance
// the applied hash. Stale cache reads preserve the expectation, and scale-out is allowed whether its event
// arrives before initialization is acknowledged or after the zero-replica update completes.
func TestSchedulingChangeAtZeroReplicasConsumesInitializationBeforeCompletion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                         string
		strategy                     grovecorev1beta1.UpdateStrategyType
		scaleOutBeforeAcknowledgment bool
	}{
		{name: "RollingRecreate scales after zero-replica completion", strategy: grovecorev1beta1.RollingRecreateStrategy},
		{name: "RollingRecreate scales before initialization acknowledgment", strategy: grovecorev1beta1.RollingRecreateStrategy, scaleOutBeforeAcknowledgment: true},
		{name: "Coherent scales after zero-replica completion", strategy: grovecorev1beta1.CoherentStrategy},
		{name: "Coherent scales before initialization acknowledgment", strategy: grovecorev1beta1.CoherentStrategy, scaleOutBeforeAcknowledgment: true},
		{name: "OnDelete scales after zero-replica completion", strategy: grovecorev1beta1.OnDeleteStrategy},
		{name: "OnDelete scales before initialization acknowledgment", strategy: grovecorev1beta1.OnDeleteStrategy, scaleOutBeforeAcknowledgment: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			baseline := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "test-uid").
				WithReplicas(0).
				WithTerminationDelay(time.Minute).
				WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(2).WithMinAvailable(1).Build()).
				WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: tc.strategy}).Build()
			_, appliedSchedulingHash, previousGenerationHash := computePCSHashes(baseline)
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "test-uid").
				WithReplicas(0).
				WithTerminationDelay(time.Minute).
				WithObservedGeneration(new(int64(0))).
				WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(2).WithMinAvailable(2).Build()).
				WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: tc.strategy}).
				WithPodCliqueSetGenerationHash(new(previousGenerationHash)).
				WithAppliedSchedulingConfigurationHash(new(appliedSchedulingHash)).Build()
			_, targetSchedulingHash, targetGenerationHash := computePCSHashes(pcs)
			require.NotEqual(t, appliedSchedulingHash, targetSchedulingHash)
			fakeClient := testutils.NewTestClientBuilder().WithObjects(pcs).WithStatusSubresource(pcs).Build()
			scalingExpectations := &pcsexpectations.ScalingProgress{}
			selectionExpectations := &pcsexpectations.CurrentlyUpdating{}
			eventRecorder := record.NewFakeRecorder(50)
			registry := component.NewOperatorRegistry[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result]()
			registry.Register(component.KindPodCliqueSetReplica, pcsreplicacomponent.New(fakeClient, eventRecorder, scalingExpectations, selectionExpectations))
			registry.Register(component.KindPodClique, pclqcomponent.New(fakeClient, fakeClient.Scheme(), eventRecorder), component.AsReplicaScoped())
			reconciler := &Reconciler{
				client:                        fakeClient,
				operatorRegistry:              registry,
				currentlyUpdatingExpectations: selectionExpectations,
				scalingProgressExpectations:   scalingExpectations,
				reconcileStatusRecorder:       ctrlcommon.NewReconcileErrorRecorder(fakeClient),
			}
			require.Empty(t, reconciler.ensureFinalizer(t.Context(), logr.Discard(), pcs).GetErrors())
			stalePCS := pcs.DeepCopy()

			initializationResult := reconciler.reconcileSpec(t.Context(), logr.Discard(), pcs, &pcssync.State{})

			require.Empty(t, initializationResult.GetErrors())
			require.True(t, ctrlcommon.ShortCircuitReconcileFlow(initializationResult))
			initializationRequeue, err := initializationResult.Result()
			require.NoError(t, err)
			require.Equal(t, constants.InformerCacheSyncRetryInterval, initializationRequeue.RequeueAfter)
			initializedPCS := &grovecorev1beta1.PodCliqueSet{}
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), initializedPCS))
			require.Equal(t, targetGenerationHash, *initializedPCS.Status.CurrentGenerationHash)
			require.Equal(t, appliedSchedulingHash, *initializedPCS.Status.AppliedSchedulingConfigurationHash)
			require.NotNil(t, initializedPCS.Status.UpdateProgress)
			require.True(t, initializedPCS.Status.UpdateProgress.RequiresReplicaRecreation)
			require.Equal(t, targetSchedulingHash, initializedPCS.Status.UpdateProgress.TargetSchedulingConfigurationHash)
			require.Nil(t, initializedPCS.Status.UpdateProgress.UpdateEndedAt, "initialization must not reach the zero-replica completion path")
			require.False(t, reconciler.pcsGenerationStatusExpectations.ClearIfSatisfied(pcs.ObjectMeta, new(targetGenerationHash), new(targetSchedulingHash)), "initialization must still expect the applied scheduling baseline")
			require.False(t, reconciler.pcsGenerationStatusExpectations.ClearIfSatisfied(stalePCS.ObjectMeta, stalePCS.Status.CurrentGenerationHash, stalePCS.Status.AppliedSchedulingConfigurationHash), "initialization must expect the new generation hash")

			if tc.scaleOutBeforeAcknowledgment {
				base := initializedPCS.DeepCopy()
				initializedPCS.Spec.Replicas = 1
				require.NoError(t, fakeClient.Patch(t.Context(), initializedPCS, client.MergeFrom(base)))
				stalePCS.Spec.Replicas = 1
			}
			staleResult := reconciler.reconcileSpec(t.Context(), logr.Discard(), stalePCS, &pcssync.State{})
			require.Empty(t, staleResult.GetErrors())
			require.True(t, staleResult.NeedsRequeue())
			require.Contains(t, staleResult.GetDescription(), "generation status is not up-to-date")
			// Error recording refreshes stalePCS from the API response, so probe with the original cached hashes.
			require.False(t, reconciler.pcsGenerationStatusExpectations.ClearIfSatisfied(pcs.ObjectMeta, new(previousGenerationHash), new(appliedSchedulingHash)), "a stale cache read must not consume the initialization expectation")
			var beforeAcknowledgment grovecorev1beta1.PodCliqueList
			require.NoError(t, fakeClient.List(t.Context(), &beforeAcknowledgment))
			require.Empty(t, beforeAcknowledgment.Items, "no child synchronization may run before initialization is acknowledged")

			acknowledgedPCS := &grovecorev1beta1.PodCliqueSet{}
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), acknowledgedPCS))
			completionResult := reconciler.reconcileSpec(t.Context(), logr.Discard(), acknowledgedPCS, &pcssync.State{})
			require.Empty(t, completionResult.GetErrors())
			require.True(t, reconciler.pcsGenerationStatusExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil, nil), "the initialization expectation must be consumed before completion")
			completedPCS := &grovecorev1beta1.PodCliqueSet{}
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), completedPCS))
			require.Equal(t, targetSchedulingHash, *completedPCS.Status.AppliedSchedulingConfigurationHash)
			require.NotNil(t, completedPCS.Status.UpdateProgress.UpdateEndedAt)
			require.Empty(t, completedPCS.Status.UpdateProgress.CurrentlyUpdating)

			if !tc.scaleOutBeforeAcknowledgment {
				base := completedPCS.DeepCopy()
				completedPCS.Spec.Replicas = 1
				require.NoError(t, fakeClient.Patch(t.Context(), completedPCS, client.MergeFrom(base)))
				scaleResult := reconciler.reconcileSpec(t.Context(), logr.Discard(), completedPCS, &pcssync.State{})
				require.Empty(t, scaleResult.GetErrors(), "completed scheduling update must not block subsequent scale-out")
			}
			var pclq grovecorev1beta1.PodClique
			pclqKey := client.ObjectKey{Namespace: testNamespace, Name: apicommon.GeneratePodCliqueName(
				apicommon.ResourceNameReplica{Name: testPCSName, Replica: 0}, "worker",
			)}
			require.NoError(t, fakeClient.Get(t.Context(), pclqKey, &pclq), "scale-out must materialize replica 0 without restarting the reconciler")
			require.Equal(t, int32(2), *pclq.Spec.MinAvailable)
			require.Equal(t, targetGenerationHash, pclq.Labels[apicommon.LabelPodCliqueSetGenerationHash])
			require.True(t, reconciler.pcsGenerationStatusExpectations.ClearIfSatisfied(completedPCS.ObjectMeta, completedPCS.Status.CurrentGenerationHash, completedPCS.Status.AppliedSchedulingConfigurationHash))
		})
	}
}

func TestScaleInBeforeFirstCompletedReconcilePreservesRetainedTarget(t *testing.T) {
	t.Parallel()
	cleanupTime := metav1.NewTime(time.Unix(150, 0).UTC())
	baseline := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "test-uid").
		WithReplicas(1).WithTerminationDelay(time.Minute).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(1).WithMinAvailable(1).Build()).Build()
	_, currentSchedulingHash, currentGenerationHash := computePCSHashes(baseline)
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "test-uid").
		WithReplicas(1).
		WithTerminationDelay(time.Minute).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(1).WithMinAvailable(1).Build()).
		WithPodCliqueSetGenerationHash(new(currentGenerationHash)).
		WithAppliedSchedulingConfigurationHash(new("previous-scheduling")).
		WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
			RequiresReplicaRecreation: true, TargetSchedulingConfigurationHash: currentSchedulingHash,
			CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex: 1,
				Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1, SourceCleanupCompletedAt: &cleanupTime},
			}},
		}).Build()
	oldPCLQ := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, "worker", pcs.Namespace, 0).
		WithLabels(map[string]string{apicommon.LabelPodCliqueSetGenerationHash: "previous"}).
		WithOptions(testutils.WithPCLQCurrentPCSGenerationHash("previous")).Build()
	targetPCLQ := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, "worker", pcs.Namespace, 1).
		WithLabels(map[string]string{apicommon.LabelPodCliqueSetGenerationHash: currentGenerationHash}).
		WithOptions(testutils.WithPCLQCurrentPCSGenerationHash(currentGenerationHash)).Build()
	fakeClient := testutils.NewTestClientBuilder().WithObjects(pcs, oldPCLQ, targetPCLQ).WithStatusSubresource(pcs).Build()
	scalingExpectations := &pcsexpectations.ScalingProgress{}
	selectionExpectations := &pcsexpectations.CurrentlyUpdating{}
	eventRecorder := record.NewFakeRecorder(50)
	registry := component.NewOperatorRegistry[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result]()
	registry.Register(component.KindPodCliqueSetReplica, pcsreplicacomponent.New(fakeClient, eventRecorder, scalingExpectations, selectionExpectations))
	registry.Register(component.KindPodClique, pclqcomponent.New(fakeClient, fakeClient.Scheme(), eventRecorder), component.AsReplicaScoped())
	reconciler := &Reconciler{
		client: fakeClient, operatorRegistry: registry, eventRecorder: eventRecorder,
		currentlyUpdatingExpectations: selectionExpectations, scalingProgressExpectations: scalingExpectations,
		reconcileStatusRecorder: ctrlcommon.NewReconcileErrorRecorder(fakeClient),
	}
	for pass := range 3 {
		freshPCS := &grovecorev1beta1.PodCliqueSet{}
		require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), freshPCS))
		state := &pcssync.State{}

		result := reconciler.reconcileSpec(t.Context(), logr.Discard(), freshPCS, state)

		require.Empty(t, result.GetErrors(), "reconciliation pass %d", pass)
		require.True(t, result.NeedsRequeue(), "the unavailable target must keep the update active")
		require.Equal(t, []int{1}, state.ExpectedReplicaIndices, "scale-in must not recreate bootstrap index 0 on pass %d", pass)
		storedPCS := &grovecorev1beta1.PodCliqueSet{}
		require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), storedPCS))
		require.Nil(t, storedPCS.Status.ObservedGeneration)
		require.Nil(t, storedPCS.Status.UpdateProgress.UpdateEndedAt)
		require.Len(t, storedPCS.Status.UpdateProgress.CurrentlyUpdating, 1)
		require.Nil(t, storedPCS.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.CanceledAt, "scale-in must retain active target 1")
		if pass == 0 {
			require.Equal(t, []int{0}, state.ScaleInReplicaIndices)
			require.NotNil(t, storedPCS.Status.ScalingProgress)
			require.Equal(t, []int32{0}, storedPCS.Status.ScalingProgress.ScaleInReplicaIndices)
		}
		if pass > 0 {
			require.Nil(t, storedPCS.Status.ScalingProgress, "participant-confirmed cleanup must remove the ordinary selection")
		}
		var missing grovecorev1beta1.PodClique
		require.True(t, apierrors.IsNotFound(fakeClient.Get(t.Context(), client.ObjectKeyFromObject(oldPCLQ), &missing)), "replica 0 must remain deleted")
		var retained grovecorev1beta1.PodClique
		require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(targetPCLQ), &retained))
		require.True(t, metav1.IsControlledBy(&retained, pcs))
	}
}

func TestProcessGenerationHashChangeBaselinesActiveLegacyUpdate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(3).WithMinAvailable(2).Build()).
		Build()
	legacyGenerationHash := computeLegacyGenerationHash(pcs)
	pcs.Status.CurrentGenerationHash = ptr.To(legacyGenerationHash)
	updateStartedAt := metav1.Now()
	pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{UpdateStartedAt: updateStartedAt}

	fakeClient := testutils.SetupFakeClient(pcs)
	reconciler := &Reconciler{client: fakeClient, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
	result := reconciler.processGenerationHashChange(context.Background(), logr.Discard(), pcs)

	require.False(t, result.HasErrors())
	require.True(t, result.NeedsRequeue())
	assert.True(t, ctrlcommon.ShortCircuitReconcileFlow(result))
	requeueResult, err := result.Result()
	require.NoError(t, err)
	assert.Equal(t, constants.InformerCacheSyncRetryInterval, requeueResult.RequeueAfter)
	updated := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), updated))
	schedulingHash := computeSchedulingConfigurationHash(buildPCSSchedulingConfig(pcs))
	require.NotNil(t, updated.Status.AppliedSchedulingConfigurationHash)
	assert.Equal(t, schedulingHash, *updated.Status.AppliedSchedulingConfigurationHash)
	require.NotNil(t, updated.Status.CurrentGenerationHash)
	assert.Equal(t, legacyGenerationHash, *updated.Status.CurrentGenerationHash)
	require.NotNil(t, updated.Status.UpdateProgress)
	assert.Equal(t, updateStartedAt.Unix(), updated.Status.UpdateProgress.UpdateStartedAt.Unix())
	assert.Equal(t, schedulingHash, updated.Status.UpdateProgress.TargetSchedulingConfigurationHash)
}

func TestProcessGenerationHashChangeKeepsActiveMigratedLegacyUpdate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(3).WithMinAvailable(2).Build()).
		Build()
	legacyGenerationHash := computeLegacyGenerationHash(pcs)
	schedulingHash := computeSchedulingConfigurationHash(buildPCSSchedulingConfig(pcs))
	pcs.Status.CurrentGenerationHash = ptr.To(legacyGenerationHash)
	pcs.Status.AppliedSchedulingConfigurationHash = ptr.To(schedulingHash)
	updateStartedAt := metav1.Now()
	pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{TargetSchedulingConfigurationHash: schedulingHash, UpdateStartedAt: updateStartedAt}

	fakeClient := testutils.SetupFakeClient(pcs)
	reconciler := &Reconciler{client: fakeClient, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
	result := reconciler.processGenerationHashChange(context.Background(), logr.Discard(), pcs)

	require.False(t, result.HasErrors())
	updated := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), updated))
	assert.Equal(t, legacyGenerationHash, *updated.Status.CurrentGenerationHash)
	require.NotNil(t, updated.Status.UpdateProgress)
	assert.Equal(t, updateStartedAt.Unix(), updated.Status.UpdateProgress.UpdateStartedAt.Unix())
	assert.Equal(t, schedulingHash, updated.Status.UpdateProgress.TargetSchedulingConfigurationHash)
}

func TestProcessGenerationHashChangeMigratesCompletedLegacyUpdate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(3).WithMinAvailable(2).Build()).
		Build()
	legacyGenerationHash := computeLegacyGenerationHash(pcs)
	schedulingHash := computeSchedulingConfigurationHash(buildPCSSchedulingConfig(pcs))
	pcs.Status.CurrentGenerationHash = ptr.To(legacyGenerationHash)
	pcs.Status.AppliedSchedulingConfigurationHash = ptr.To(schedulingHash)
	updateStartedAt := metav1.Now()
	updateEndedAt := metav1.NewTime(updateStartedAt.Add(time.Minute))
	pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
		TargetSchedulingConfigurationHash: schedulingHash,
		UpdateStartedAt:                   updateStartedAt,
		UpdateEndedAt:                     &updateEndedAt,
	}

	fakeClient := testutils.SetupFakeClient(pcs)
	reconciler := &Reconciler{client: fakeClient, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
	result := reconciler.processGenerationHashChange(context.Background(), logr.Discard(), pcs)

	require.False(t, result.HasErrors())
	updated := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), updated))
	assert.Equal(t, computeGenerationHash(pcs), *updated.Status.CurrentGenerationHash)
	require.NotNil(t, updated.Status.UpdateProgress)
	assert.Equal(t, updateStartedAt.Unix(), updated.Status.UpdateProgress.UpdateStartedAt.Unix())
	require.NotNil(t, updated.Status.UpdateProgress.UpdateEndedAt)
	assert.Equal(t, updateEndedAt.Unix(), updated.Status.UpdateProgress.UpdateEndedAt.Unix())
}

func TestProcessGenerationHashChangeSkipsCurrentCombinedHash(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(3).WithMinAvailable(2).Build()).
		Build()
	schedulingHash := computeSchedulingConfigurationHash(buildPCSSchedulingConfig(pcs))
	pcs.Status.CurrentGenerationHash = ptr.To(computeGenerationHash(pcs))
	pcs.Status.AppliedSchedulingConfigurationHash = ptr.To(schedulingHash)

	unexpectedStatusUpdate := apierrors.NewInternalError(errors.New("unexpected status update"))
	fakeClient := testutils.NewTestClientBuilder().
		WithObjects(pcs).
		WithStatusSubresource(pcs).
		RecordErrorForObjects(testutils.ClientMethodStatusUpdate, unexpectedStatusUpdate, client.ObjectKeyFromObject(pcs)).
		Build()
	reconciler := &Reconciler{client: fakeClient, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
	result := reconciler.processGenerationHashChange(context.Background(), logr.Discard(), pcs)

	require.False(t, result.HasErrors())
	assert.Nil(t, pcs.Status.UpdateProgress)
}

func TestProcessGenerationHashChangeMigratesIdleLegacyHashWithoutUpdate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(3).WithMinAvailable(2).Build()).
		Build()
	legacyGenerationHash := computeLegacyGenerationHash(pcs)
	pcs.Status.CurrentGenerationHash = ptr.To(legacyGenerationHash)
	schedulingHash := computeSchedulingConfigurationHash(buildPCSSchedulingConfig(pcs))
	pcs.Status.AppliedSchedulingConfigurationHash = ptr.To(schedulingHash)

	fakeClient := testutils.SetupFakeClient(pcs)
	reconciler := &Reconciler{client: fakeClient, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
	result := reconciler.processGenerationHashChange(context.Background(), logr.Discard(), pcs)

	require.False(t, result.HasErrors())
	updated := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), updated))
	require.NotNil(t, updated.Status.CurrentGenerationHash)
	assert.Equal(t, computeGenerationHash(pcs), *updated.Status.CurrentGenerationHash)
	assert.Nil(t, updated.Status.UpdateProgress)
}

func TestProcessGenerationHashChangeStartsRecreationForLegacySchedulingChange(t *testing.T) {
	t.Parallel()

	testCases := []grovecorev1beta1.UpdateStrategyType{
		grovecorev1beta1.OnDeleteStrategy,
		grovecorev1beta1.RollingRecreateStrategy,
		grovecorev1beta1.CoherentStrategy,
	}
	for _, tc := range testCases {
		t.Run(string(tc), func(t *testing.T) {
			t.Parallel()

			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
				WithReplicas(2).
				WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(3).WithMinAvailable(2).Build()).
				WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: tc}).
				Build()
			legacyGenerationHash, appliedSchedulingHash, _ := computePCSHashes(pcs)
			pcs.Status.CurrentGenerationHash = ptr.To(legacyGenerationHash)
			pcs.Status.AppliedSchedulingConfigurationHash = ptr.To(appliedSchedulingHash)
			pcs.Status.UpdatedReplicas = pcs.Spec.Replicas

			pcs.Spec.Template.Cliques[0].Spec.MinAvailable = ptr.To[int32](1)
			targetLegacyGenerationHash, targetSchedulingHash, targetGenerationHash := computePCSHashes(pcs)
			require.Equal(t, legacyGenerationHash, targetLegacyGenerationHash)
			require.NotEqual(t, appliedSchedulingHash, targetSchedulingHash)

			fakeClient := testutils.SetupFakeClient(pcs)
			reconciler := &Reconciler{
				client:                        fakeClient,
				currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{},
			}
			result := reconciler.processGenerationHashChange(t.Context(), logr.Discard(), pcs)

			require.False(t, result.HasErrors())
			updated := &grovecorev1beta1.PodCliqueSet{}
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), updated))
			require.NotNil(t, updated.Status.CurrentGenerationHash)
			assert.Equal(t, targetGenerationHash, *updated.Status.CurrentGenerationHash)
			require.NotNil(t, updated.Status.AppliedSchedulingConfigurationHash)
			assert.Equal(t, appliedSchedulingHash, *updated.Status.AppliedSchedulingConfigurationHash)
			require.NotNil(t, updated.Status.UpdateProgress)
			assert.True(t, updated.Status.UpdateProgress.RequiresReplicaRecreation)
			assert.Equal(t, targetSchedulingHash, updated.Status.UpdateProgress.TargetSchedulingConfigurationHash)
			require.NotNil(t, updated.Status.UpdateProgress.PreviousPodCliqueSetGenerationHash)
			assert.Equal(t, legacyGenerationHash, *updated.Status.UpdateProgress.PreviousPodCliqueSetGenerationHash)
			assert.False(t, updated.Status.UpdateProgress.UpdateStartedAt.IsZero())
			assert.Nil(t, updated.Status.UpdateProgress.UpdateEndedAt)
			assert.Zero(t, updated.Status.UpdatedReplicas)
		})
	}
}

func TestProcessGenerationHashChangePreservesLegacyContentUpdate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(3).WithMinAvailable(2).Build()).
		Build()
	pcs.Status.CurrentGenerationHash = ptr.To(computeLegacyGenerationHash(pcs))
	pcs.Spec.Template.Cliques[0].Annotations = map[string]string{"revision": "new"}

	fakeClient := testutils.SetupFakeClient(pcs)
	reconciler := &Reconciler{client: fakeClient, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
	stalePCS := pcs.DeepCopy()

	baselineResult := reconciler.processGenerationHashChange(context.Background(), logr.Discard(), pcs)
	require.False(t, baselineResult.HasErrors())
	require.True(t, baselineResult.NeedsRequeue())
	assert.True(t, ctrlcommon.ShortCircuitReconcileFlow(baselineResult))
	baselineRequeueResult, err := baselineResult.Result()
	require.NoError(t, err)
	assert.Equal(t, constants.InformerCacheSyncRetryInterval, baselineRequeueResult.RequeueAfter)

	staleResult := reconciler.processGenerationHashChange(context.Background(), logr.Discard(), stalePCS)
	require.False(t, staleResult.HasErrors())
	require.True(t, staleResult.NeedsRequeue())
	assert.True(t, ctrlcommon.ShortCircuitReconcileFlow(staleResult))
	assert.Contains(t, staleResult.GetDescription(), "generation status is not up-to-date")

	baselined := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), baselined))
	updateResult := reconciler.processGenerationHashChange(context.Background(), logr.Discard(), baselined)
	require.False(t, updateResult.HasErrors())

	updated := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), updated))
	schedulingHash := computeSchedulingConfigurationHash(buildPCSSchedulingConfig(pcs))
	require.NotNil(t, updated.Status.UpdateProgress)
	assert.Equal(t, schedulingHash, updated.Status.UpdateProgress.TargetSchedulingConfigurationHash)
	require.NotNil(t, updated.Status.CurrentGenerationHash)
	assert.Equal(t, computeGenerationHash(pcs), *updated.Status.CurrentGenerationHash)
}

func TestProcessGenerationHashChangeSnapshotsSchedulingTarget(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(3).WithMinAvailable(2).Build()).
		Build()
	appliedSchedulingHash := computeSchedulingConfigurationHash(buildPCSSchedulingConfig(pcs))
	pcs.Status.AppliedSchedulingConfigurationHash = ptr.To(appliedSchedulingHash)
	pcs.Status.CurrentGenerationHash = ptr.To(computeGenerationHash(pcs))
	pcs.Spec.Template.Cliques[0].Spec.MinAvailable = ptr.To[int32](1)

	fakeClient := testutils.SetupFakeClient(pcs)
	reconciler := &Reconciler{client: fakeClient, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
	result := reconciler.processGenerationHashChange(context.Background(), logr.Discard(), pcs)

	require.False(t, result.HasErrors())
	updated := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), updated))
	assert.Equal(t, appliedSchedulingHash, *updated.Status.AppliedSchedulingConfigurationHash)
	require.NotNil(t, updated.Status.UpdateProgress)
	assert.True(t, updated.Status.UpdateProgress.RequiresReplicaRecreation)
	assert.Equal(t, computeSchedulingConfigurationHash(buildPCSSchedulingConfig(pcs)), updated.Status.UpdateProgress.TargetSchedulingConfigurationHash)
}

// TestTriggerDeletionFlowClearsGenerationStatusExpectation verifies the deletion flow removes the PCS
// generation-status expectation from the tracker.
func TestTriggerDeletionFlowClearsGenerationStatusExpectation(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid-del").Build()
	pcs.Finalizers = []string{apiconstants.FinalizerPodCliqueSet}

	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{
		client:                        fakeClient,
		operatorRegistry:              component.NewOperatorRegistry[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result](),
		reconcileStatusRecorder:       ctrlcommon.NewReconcileErrorRecorder(fakeClient),
		scalingProgressExpectations:   &pcsexpectations.ScalingProgress{},
		currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{},
	}
	r.pcsGenerationStatusExpectations.Record(pcs.ObjectMeta, "some-hash", nil)

	r.triggerDeletionFlow(context.Background(), logr.Discard(), pcs)

	assert.True(t, r.pcsGenerationStatusExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil, nil), "generation-hash expectation should be cleared when the PCS is deleted")
}

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
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pclqcomponent "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/podclique"
	pcsreplicacomponent "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/podcliquesetreplica"
	podgangcomponent "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/podgang"
	pcsexpectations "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/expectations"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/sync"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/record"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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

func TestSyncKindGroupResults(t *testing.T) {
	failure := errors.New("sync failed")
	for _, tc := range []struct {
		name      string
		result    pcssync.Result
		err       error
		wantError bool
	}{
		{name: "successful observations", result: pcssync.Result{CleanedUpReplicaIndices: []int{0}}},
		{name: "continuing retry retains observations", result: pcssync.Result{CleanedUpReplicaIndices: []int{0}, RequeueAfter: new(time.Second)}},
		{name: "stopping retry retains observations", result: pcssync.Result{CleanedUpReplicaIndices: []int{0}, RequeueAfter: new(time.Second), StopAndRequeue: true}},
		{name: "error discards accompanying observations and control", result: pcssync.Result{CleanedUpReplicaIndices: []int{0}, RequeueAfter: new(time.Second), StopAndRequeue: true}, err: failure, wantError: true},
		{name: "stop without retry is invalid", result: pcssync.Result{StopAndRequeue: true}, wantError: true},
		{name: "zero retry is invalid", result: pcssync.Result{RequeueAfter: new(time.Duration(0))}, wantError: true},
		{name: "negative retry is invalid", result: pcssync.Result{RequeueAfter: new(-time.Second)}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg := component.NewOperatorRegistry[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result]()
			reg.Register(component.KindPodGang, componentSyncTestOperator{sync: func(context.Context, logr.Logger, *grovecorev1beta1.PodCliqueSet, *pcssync.State) (pcssync.Result, error) {
				return tc.result, tc.err
			}})
			reg.Register(component.KindPodCliqueScalingGroup, componentSyncTestOperator{sync: func(context.Context, logr.Logger, *grovecorev1beta1.PodCliqueSet, *pcssync.State) (pcssync.Result, error) {
				return pcssync.Result{NotApplicable: true}, nil
			}})
			r := &Reconciler{operatorRegistry: reg}

			results, err := r.syncKindGroup(t.Context(), logr.Discard(), &grovecorev1beta1.PodCliqueSet{}, &pcssync.State{}, []component.Kind{component.KindPodGang, component.KindPodCliqueScalingGroup}, 4)

			if tc.wantError {
				require.Error(t, err)
				if tc.err != nil {
					assert.ErrorIs(t, err, tc.err)
				}
				assert.Nil(t, results)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, []componentSyncResult{
				{kind: component.KindPodGang, result: tc.result},
				{kind: component.KindPodCliqueScalingGroup, result: pcssync.Result{NotApplicable: true}},
			}, results)
		})
	}
}

func TestSyncPodCliqueSetResourcesRetryControl(t *testing.T) {
	failure := errors.New("API request failed")
	for _, tc := range []struct {
		name        string
		results     [3]pcssync.Result
		middleError error
		wantCalls   int32
		wantDelay   time.Duration
	}{
		{name: "zero result continues without retry", wantCalls: 3},
		{name: "shortest retry across groups", results: [3]pcssync.Result{{RequeueAfter: new(2 * time.Second)}, {RequeueAfter: new(5 * time.Second)}, {RequeueAfter: new(3 * time.Second)}}, wantCalls: 3, wantDelay: 2 * time.Second},
		{name: "progress barrier prevents dependent dispatch", results: [3]pcssync.Result{{RequeueAfter: new(constants.InformerCacheSyncRetryInterval), StopAndRequeue: true}}, wantCalls: 1, wantDelay: constants.InformerCacheSyncRetryInterval},
		{name: "later stop overrides earlier shorter continuing delay", results: [3]pcssync.Result{{RequeueAfter: new(time.Second)}, {RequeueAfter: new(5 * time.Second), StopAndRequeue: true}}, wantCalls: 2, wantDelay: 5 * time.Second},
		{name: "failure takes precedence over a retry", results: [3]pcssync.Result{{RequeueAfter: new(time.Second)}, {RequeueAfter: new(time.Second), StopAndRequeue: true}}, middleError: failure, wantCalls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
			reg := component.NewOperatorRegistry[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result]()
			var calls atomic.Int32
			for i, kind := range []component.Kind{component.KindPodCliqueSetReplica, component.KindHeadlessService, component.KindPodGang} {
				reg.Register(kind, componentSyncTestOperator{sync: func(context.Context, logr.Logger, *grovecorev1beta1.PodCliqueSet, *pcssync.State) (pcssync.Result, error) {
					calls.Add(1)
					if i == 1 {
						return tc.results[i], tc.middleError
					}
					return tc.results[i], nil
				}})
			}
			r := &Reconciler{operatorRegistry: reg}

			step := r.syncPodCliqueSetResources(t.Context(), logr.Discard(), pcs, &pcssync.State{})

			result, err := step.Result()
			if tc.middleError != nil {
				assert.ErrorIs(t, err, tc.middleError)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantDelay, result.RequeueAfter)
			assert.Equal(t, tc.wantCalls, calls.Load())
		})
	}
}

func TestSyncPodCliqueSetResourcesFirstStopUsesGroupOrder(t *testing.T) {
	for _, failSecond := range []bool{false, true} {
		t.Run(fmt.Sprintf("second task fails %t", failSecond), func(t *testing.T) {
			t.Parallel()
			secondFinished := make(chan struct{})
			failure := errors.New("second component failed")
			reg := component.NewOperatorRegistry[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result]()
			reg.Register(component.KindHeadlessService, componentSyncTestOperator{sync: func(ctx context.Context, _ logr.Logger, _ *grovecorev1beta1.PodCliqueSet, _ *pcssync.State) (pcssync.Result, error) {
				select {
				case <-secondFinished:
					return pcssync.Result{RequeueAfter: new(5 * time.Second), StopAndRequeue: true}, nil
				case <-ctx.Done():
					return pcssync.Result{}, ctx.Err()
				}
			}})
			reg.Register(component.KindHorizontalPodAutoscaler, componentSyncTestOperator{sync: func(context.Context, logr.Logger, *grovecorev1beta1.PodCliqueSet, *pcssync.State) (pcssync.Result, error) {
				defer close(secondFinished)
				if failSecond {
					return pcssync.Result{}, failure
				}
				return pcssync.Result{RequeueAfter: new(time.Second), StopAndRequeue: true}, nil
			}})
			var laterCalls atomic.Int32
			reg.Register(component.KindPodGang, componentSyncTestOperator{sync: func(context.Context, logr.Logger, *grovecorev1beta1.PodCliqueSet, *pcssync.State) (pcssync.Result, error) {
				laterCalls.Add(1)
				return pcssync.Result{}, nil
			}})
			r := &Reconciler{operatorRegistry: reg}

			step := r.syncPodCliqueSetResources(t.Context(), logr.Discard(), &grovecorev1beta1.PodCliqueSet{}, &pcssync.State{})

			result, err := step.Result()
			assert.Zero(t, laterCalls.Load())
			if failSecond {
				assert.ErrorIs(t, err, failure)
				assert.Zero(t, result.RequeueAfter)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 5*time.Second, result.RequeueAfter, "the first stopping kind wins, not the shortest delay or first task to finish")
		})
	}
}

func TestSyncPodCliqueSetResourcesCleanupDuringPodGangWait(t *testing.T) {
	for _, tc := range []struct {
		name                                                                                                      string
		sourcePresent, holdDeletion, missingParticipant, failList, failDelete, failPodGangPatch, failCleanupPatch bool
	}{
		{name: "absent source is acknowledged while replica 1 has no pods"},
		{name: "accepted deletion requires a later absence observation", sourcePresent: true},
		{name: "finalizer keeps deletion pending", sourcePresent: true, holdDeletion: true},
		{name: "missing participant prevents acknowledgment", missingParticipant: true},
		{name: "failed discovery produces no cleanup result", failList: true},
		{name: "failed deletion produces no cleanup result", sourcePresent: true, failDelete: true},
		{name: "real PodGang failure is not a pending result", failPodGangPatch: true},
		{name: "cleanup write conflict takes precedence over pending pods", failCleanupPatch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").WithReplicas(2).WithStandaloneClique("worker").Build()
			pcs.Spec.Template.Cliques[0].Spec.MinAvailable = new(int32(1))
			pcs.Spec.Template.Cliques[0].Spec.PodSpec.ServiceAccountName = "repaired"
			pcs.Status.CurrentGenerationHash = new("target")
			pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
				RequiresReplicaRecreation: true,
				CurrentlyUpdating:         []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0}}},
			}
			pclq := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, "worker", pcs.Namespace, 1).WithMinAvailable(1).Build()
			pclq.Spec.PodSpec.ServiceAccountName = "missing"
			pgm := testutils.NewPodGangMapBuilder(pcs.Name, pcs.Namespace, pcs.UID, 1).WithEntries(grovecorev1beta1.PodGangEntry{
				Epoch: "100", Role: grovecorev1beta1.PodGangEntryRoleAnchor, PodCliqueSetGenerationHash: "old", PodCliques: map[string]int32{"worker": 1},
			}).Build()
			objects := []client.Object{pcs, pclq, pgm}
			if tc.sourcePresent {
				objects = append(objects, &groveschedulerv1alpha1.PodGang{ObjectMeta: metav1.ObjectMeta{
					Name: "old-source", Namespace: pcs.Namespace,
					Labels:          map[string]string{apicommon.LabelManagedByKey: apicommon.LabelManagedByValue, apicommon.LabelPartOfKey: pcs.Name, apicommon.LabelComponentKey: apicommon.LabelComponentNamePodGang, apicommon.LabelPodCliqueSetReplicaIndex: "0", apicommon.LabelEpoch: "50"},
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(pcs, grovecorev1beta1.SchemeGroupVersion.WithKind("PodCliqueSet"))},
				}})
			}
			if tc.holdDeletion {
				objects[len(objects)-1].SetFinalizers([]string{"test/hold-deletion"})
			}
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1beta1.AddToScheme(scheme))
			require.NoError(t, groveschedulerv1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))
			baseClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(pcs, &groveschedulerv1alpha1.PodGang{}).Build()
			failure := errors.New("API unavailable")
			cl := interceptor.NewClient(baseClient, interceptor.Funcs{
				List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if _, isPodGangList := list.(*groveschedulerv1alpha1.PodGangList); isPodGangList && tc.failList {
						return failure
					}
					return cl.List(ctx, list, opts...)
				},
				Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					if tc.failDelete {
						return failure
					}
					return cl.Delete(ctx, obj, opts...)
				},
				SubResourcePatch: func(ctx context.Context, cl client.Client, name string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					if _, isPCS := obj.(*grovecorev1beta1.PodCliqueSet); isPCS && tc.failCleanupPatch {
						return apierrors.NewConflict(schema.GroupResource{Resource: "podcliquesets"}, pcs.Name, failure)
					}
					if _, isPodGang := obj.(*groveschedulerv1alpha1.PodGang); isPodGang && tc.failPodGangPatch {
						return failure
					}
					return cl.SubResource(name).Patch(ctx, obj, patch, opts...)
				},
			})
			schedRegistry := &testutils.FakeSchedulerRegistry{Backends: map[string]scheduler.Backend{"default-scheduler": testutils.NewFakeSchedulerBackend("default-scheduler")}, DefaultBackend: "default-scheduler"}
			reg := component.NewOperatorRegistry[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result]()
			reg.Register(component.KindPodGang, podgangcomponent.New(cl, scheme, record.NewFakeRecorder(30), configv1beta1.TopologyAwareSchedulingConfiguration{}, schedRegistry), component.AsReplicaScoped())
			reg.Register(component.KindPodClique, componentSyncTestOperator{sync: func(context.Context, logr.Logger, *grovecorev1beta1.PodCliqueSet, *pcssync.State) (pcssync.Result, error) {
				return pcssync.Result{CleanedUpReplicaIndices: []int{0}}, nil
			}}, component.AsReplicaScoped())
			if tc.missingParticipant {
				reg.Register(component.Kind("not-dispatched"), nil, component.AsReplicaScoped())
			}
			r := &Reconciler{client: cl, operatorRegistry: reg, eventRecorder: record.NewFakeRecorder(10), scalingProgressExpectations: &pcsexpectations.ScalingProgress{}, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
			state := &pcssync.State{ExpectedReplicaIndices: []int{1}, ScaleInReplicaIndices: []int{0}}

			step := r.syncPodCliqueSetResources(t.Context(), logr.Discard(), pcs, state)

			result, err := step.Result()
			stored := &grovecorev1beta1.PodCliqueSet{}
			require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pcs), stored))
			cleanupTime := stored.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.SourceCleanupCompletedAt
			if tc.failList || tc.failDelete || tc.failPodGangPatch || tc.failCleanupPatch {
				require.Error(t, err)
				assert.Nil(t, cleanupTime)
				return
			}
			require.NoError(t, err)
			if tc.sourcePresent || tc.missingParticipant {
				assert.Nil(t, cleanupTime)
				assert.Equal(t, constants.ComponentSyncRetryInterval, result.RequeueAfter)
			} else {
				require.NotNil(t, cleanupTime)
				assert.Equal(t, constants.InformerCacheSyncRetryInterval, result.RequeueAfter)
			}
			pendingPG := &groveschedulerv1alpha1.PodGang{}
			pendingName := apicommon.GenerateAnchorPodGangName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 1}, "100")
			require.NoError(t, cl.Get(t.Context(), client.ObjectKey{Namespace: pcs.Namespace, Name: pendingName}, pendingPG))
			assert.False(t, meta.IsStatusConditionTrue(pendingPG.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeInitialized)))
			if tc.sourcePresent {
				step = r.syncPodCliqueSetResources(t.Context(), logr.Discard(), stored, state)
				_, err = step.Result()
				require.NoError(t, err)
				require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pcs), stored))
				if tc.holdDeletion {
					assert.Nil(t, stored.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.SourceCleanupCompletedAt)
					source := &groveschedulerv1alpha1.PodGang{}
					require.NoError(t, cl.Get(t.Context(), client.ObjectKey{Namespace: pcs.Namespace, Name: "old-source"}, source))
					require.NotNil(t, source.DeletionTimestamp)
					source.Finalizers = nil
					require.NoError(t, cl.Update(t.Context(), source))
					step = r.syncPodCliqueSetResources(t.Context(), logr.Discard(), stored, state)
					_, err = step.Result()
					require.NoError(t, err)
					require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pcs), stored))
				}
				assert.NotNil(t, stored.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.SourceCleanupCompletedAt)
			}
		})
	}
}

type componentSyncTestOperator struct {
	component.Operator[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result]
	sync func(context.Context, logr.Logger, *grovecorev1beta1.PodCliqueSet, *pcssync.State) (pcssync.Result, error)
}

func (o componentSyncTestOperator) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, state *pcssync.State) (pcssync.Result, error) {
	return o.sync(ctx, logger, pcs, state)
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

// TestSchedulingChangeAtZeroReplicasAcknowledgesCombinedProgressBeforeScaleOut exercises the real replica
// and PodClique components. Generation and immediate completion are persisted together, so stale cache reads
// must not consume an expectation for an intermediate applied hash or block later scale-out.
func TestSchedulingChangeAtZeroReplicasAcknowledgesCombinedProgressBeforeScaleOut(t *testing.T) {
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
			appliedSchedulingHash, previousGenerationHash := initializeGenerationHashesForTest(t, baseline)
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "test-uid").
				WithReplicas(0).
				WithTerminationDelay(time.Minute).
				WithObservedGeneration(new(int64(0))).
				WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(2).WithMinAvailable(2).Build()).
				WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: tc.strategy}).
				WithPodCliqueSetGenerationHash(new(previousGenerationHash)).
				WithAppliedSchedulingConfigurationHash(new(appliedSchedulingHash)).Build()
			targetSchedulingHash, targetGenerationHash := initializeGenerationHashesForTest(t, pcs)
			require.NotEqual(t, appliedSchedulingHash, targetSchedulingHash)
			fakeClient := testutils.NewTestClientBuilder().WithObjects(pcs).WithStatusSubresource(pcs).Build()
			scalingExpectations := &pcsexpectations.ScalingProgress{}
			selectionExpectations := &pcsexpectations.CurrentlyUpdating{}
			eventRecorder := record.NewFakeRecorder(50)
			registry := component.NewOperatorRegistry[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result]()
			registry.Register(component.KindPodClique, pclqcomponent.New(fakeClient, fakeClient.Scheme(), eventRecorder), component.AsReplicaScoped())
			reconciler := &Reconciler{
				client:                        fakeClient,
				operatorRegistry:              registry,
				currentlyUpdatingExpectations: selectionExpectations,
				scalingProgressExpectations:   scalingExpectations,
				reconcileStatusRecorder:       ctrlcommon.NewReconcileErrorRecorder(fakeClient),
			}
			registry.Register(component.KindPodCliqueSetReplica, pcsreplicacomponent.New(fakeClient, eventRecorder, scalingExpectations, selectionExpectations, &reconciler.pcsGenerationStatusExpectations, clocktesting.NewFakeClock(time.Unix(1000, 0).UTC())))
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
			require.Equal(t, targetSchedulingHash, *initializedPCS.Status.AppliedSchedulingConfigurationHash)
			require.NotNil(t, initializedPCS.Status.UpdateProgress)
			require.True(t, initializedPCS.Status.UpdateProgress.RequiresReplicaRecreation)
			require.Equal(t, targetSchedulingHash, initializedPCS.Status.UpdateProgress.TargetSchedulingConfigurationHash)
			require.NotNil(t, initializedPCS.Status.UpdateProgress.UpdateEndedAt, "generation and zero-replica completion must share one transaction")
			require.False(t, reconciler.pcsGenerationStatusExpectations.ClearIfSatisfied(pcs.ObjectMeta, new(targetGenerationHash), new(appliedSchedulingHash)), "partial progress must not satisfy the combined expectation")
			require.False(t, reconciler.pcsGenerationStatusExpectations.ClearIfSatisfied(stalePCS.ObjectMeta, stalePCS.Status.CurrentGenerationHash, stalePCS.Status.AppliedSchedulingConfigurationHash), "initialization must expect the new generation hash")

			if tc.scaleOutBeforeAcknowledgment {
				base := initializedPCS.DeepCopy()
				initializedPCS.Spec.Replicas = 1
				require.NoError(t, fakeClient.Patch(t.Context(), initializedPCS, client.MergeFrom(base)))
				stalePCS.Spec.Replicas = 1
			}
			staleResult := reconciler.ensureGenerationStatusExpectationSatisfied(stalePCS)
			require.Empty(t, staleResult.GetErrors())
			require.True(t, staleResult.NeedsRequeue())
			require.Contains(t, staleResult.GetDescription(), "generation status is not up-to-date")
			require.False(t, reconciler.pcsGenerationStatusExpectations.ClearIfSatisfied(pcs.ObjectMeta, new(previousGenerationHash), new(appliedSchedulingHash)), "a stale cache read must not consume the initialization expectation")
			var beforeAcknowledgment grovecorev1beta1.PodCliqueList
			require.NoError(t, fakeClient.List(t.Context(), &beforeAcknowledgment))
			require.Empty(t, beforeAcknowledgment.Items, "no child synchronization may run before initialization is acknowledged")

			acknowledgedPCS := &grovecorev1beta1.PodCliqueSet{}
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), acknowledgedPCS))
			require.False(t, ctrlcommon.ShortCircuitReconcileFlow(reconciler.ensureGenerationStatusExpectationSatisfied(acknowledgedPCS)))
			completionResult := reconciler.reconcileSpec(t.Context(), logr.Discard(), acknowledgedPCS, &pcssync.State{})
			require.Empty(t, completionResult.GetErrors())
			require.True(t, reconciler.pcsGenerationStatusExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil, nil), "the combined progress expectation must be consumed before child execution")
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
			// The scale-out selection also crosses its own progress/cache barrier before child creation.
			freshPCS := &grovecorev1beta1.PodCliqueSet{}
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), freshPCS))
			require.False(t, ctrlcommon.ShortCircuitReconcileFlow(reconciler.ensureGenerationStatusExpectationSatisfied(freshPCS)))
			scaleResult := reconciler.reconcileSpec(t.Context(), logr.Discard(), freshPCS, &pcssync.State{})
			require.Empty(t, scaleResult.GetErrors())
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
	currentSchedulingHash, currentGenerationHash := initializeGenerationHashesForTest(t, baseline)
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
	registry.Register(component.KindPodClique, pclqcomponent.New(fakeClient, fakeClient.Scheme(), eventRecorder), component.AsReplicaScoped())
	reconciler := &Reconciler{
		client: fakeClient, operatorRegistry: registry, eventRecorder: eventRecorder,
		currentlyUpdatingExpectations: selectionExpectations, scalingProgressExpectations: scalingExpectations,
		reconcileStatusRecorder: ctrlcommon.NewReconcileErrorRecorder(fakeClient),
	}
	registry.Register(component.KindPodCliqueSetReplica, pcsreplicacomponent.New(fakeClient, eventRecorder, scalingExpectations, selectionExpectations, &reconciler.pcsGenerationStatusExpectations, clocktesting.NewFakeClock(time.Unix(1000, 0).UTC())))
	for pass := range 4 {
		freshPCS := &grovecorev1beta1.PodCliqueSet{}
		require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), freshPCS))
		state := &pcssync.State{}

		result := reconciler.reconcileSpec(t.Context(), logr.Discard(), freshPCS, state)

		require.Empty(t, result.GetErrors(), "reconciliation pass %d", pass)
		require.True(t, result.NeedsRequeue(), "the unavailable target must keep the update active")
		if pass == 0 {
			require.Equal(t, &pcssync.State{}, state, "new scaling progress must stop before child execution")
		} else {
			require.Equal(t, []int{1}, state.ExpectedReplicaIndices, "scale-in must not recreate bootstrap index 0 on pass %d", pass)
		}
		storedPCS := &grovecorev1beta1.PodCliqueSet{}
		require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), storedPCS))
		require.Nil(t, storedPCS.Status.ObservedGeneration)
		require.Nil(t, storedPCS.Status.UpdateProgress.UpdateEndedAt)
		require.Len(t, storedPCS.Status.UpdateProgress.CurrentlyUpdating, 1)
		require.Nil(t, storedPCS.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.CanceledAt, "scale-in must retain active target 1")
		if pass == 1 {
			require.Equal(t, []int{0}, state.ScaleInReplicaIndices)
		}
		if pass <= 1 {
			require.NotNil(t, storedPCS.Status.ScalingProgress)
			require.Equal(t, []int32{0}, storedPCS.Status.ScalingProgress.ScaleInReplicaIndices)
		}
		if pass > 1 {
			require.Nil(t, storedPCS.Status.ScalingProgress, "participant-confirmed cleanup must remove the scaling selection")
		}
		var missing grovecorev1beta1.PodClique
		lookupErr := fakeClient.Get(t.Context(), client.ObjectKeyFromObject(oldPCLQ), &missing)
		if pass == 0 {
			require.NoError(t, lookupErr, "replica 0 must survive the progress barrier")
		} else {
			require.True(t, apierrors.IsNotFound(lookupErr), "replica 0 must remain deleted")
		}
		var retained grovecorev1beta1.PodClique
		require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(targetPCLQ), &retained))
		require.True(t, metav1.IsControlledBy(&retained, pcs))
	}
}

// initializeGenerationHashesForTest obtains desired hashes through the real replica component on a fresh PCS.
// This keeps controller tests independent of the replica component's private hash implementation.
func initializeGenerationHashesForTest(t *testing.T, pcs *grovecorev1beta1.PodCliqueSet) (string, string) {
	t.Helper()
	freshPCS := pcs.DeepCopy()
	freshPCS.Status = grovecorev1beta1.PodCliqueSetStatus{}
	cl := testutils.SetupFakeClient(freshPCS)
	operator := pcsreplicacomponent.New(cl, record.NewFakeRecorder(10), &pcsexpectations.ScalingProgress{},
		&pcsexpectations.CurrentlyUpdating{}, &pcsexpectations.GenerationHashes{}, clocktesting.NewFakeClock(time.Unix(1000, 0).UTC()))
	result, err := operator.Sync(t.Context(), logr.Discard(), freshPCS, &pcssync.State{})
	require.NoError(t, err)
	assert.Equal(t, pcssync.Result{RequeueAfter: new(constants.InformerCacheSyncRetryInterval), StopAndRequeue: true}, result)
	require.NotNil(t, freshPCS.Status.AppliedSchedulingConfigurationHash)
	require.NotNil(t, freshPCS.Status.CurrentGenerationHash)
	return *freshPCS.Status.AppliedSchedulingConfigurationHash, *freshPCS.Status.CurrentGenerationHash
}

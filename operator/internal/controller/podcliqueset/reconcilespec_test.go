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
	"sync"
	"testing"
	"time"

	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pcsexpectations "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/expectations"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/sync"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

		sourceCleanupCompleted, removedCanceled := recordReplacementCleanupWork(logr.Discard(), pcs, sets.New[int32](1))

		assert.Equal(t, []int32{1}, sourceCleanupCompleted)
		assert.Empty(t, removedCanceled)
		require.NotNil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.SourceCleanupCompletedAt)
	})

	t.Run("leaves same-index cleanup pending while shared source resources remain", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
		pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{activeSameIndex()}}

		sourceCleanupCompleted, _ := recordReplacementCleanupWork(logr.Discard(), pcs, sets.New[int32]())

		assert.Empty(t, sourceCleanupCompleted)
		assert.Nil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.SourceCleanupCompletedAt)
	})

	t.Run("does not re-record an already completed same-index cleanup", func(t *testing.T) {
		entry := activeSameIndex()
		entry.Replacement.SourceCleanupCompletedAt = &canceledAt
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
		pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{entry}}

		sourceCleanupCompleted, _ := recordReplacementCleanupWork(logr.Discard(), pcs, sets.New[int32](1))

		assert.Empty(t, sourceCleanupCompleted)
	})

	t.Run("leaves a different-index active replacement untouched", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
		pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			{ReplicaIndex: 3, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 2}},
		}}

		sourceCleanupCompleted, removedCanceled := recordReplacementCleanupWork(logr.Discard(), pcs, sets.New[int32](2, 3))

		assert.Empty(t, sourceCleanupCompleted)
		assert.Empty(t, removedCanceled)
		assert.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1)
		assert.Nil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.SourceCleanupCompletedAt)
	})

	t.Run("removes a canceled replacement once source and target resources are gone", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
		pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{canceled()}}

		sourceCleanupCompleted, removedCanceled := recordReplacementCleanupWork(logr.Discard(), pcs, sets.New[int32](2, 3))

		assert.Empty(t, sourceCleanupCompleted)
		assert.Equal(t, []int32{3}, removedCanceled)
		assert.Empty(t, pcs.Status.UpdateProgress.CurrentlyUpdating)
	})

	t.Run("retains a canceled replacement while its target resources remain", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
		pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{canceled()}}

		_, removedCanceled := recordReplacementCleanupWork(logr.Discard(), pcs, sets.New[int32](3))

		assert.Empty(t, removedCanceled)
		assert.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1)
	})

	t.Run("records a same-index completion and removes a canceled entry together", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
		pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{activeSameIndex(), canceled()}}

		sourceCleanupCompleted, removedCanceled := recordReplacementCleanupWork(logr.Discard(), pcs, sets.New[int32](1, 2, 3))

		assert.Equal(t, []int32{1}, sourceCleanupCompleted)
		assert.Equal(t, []int32{3}, removedCanceled)
		require.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1)
		assert.Equal(t, int32(1), pcs.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex)
	})
}

func TestCommitPCSCleanupStatus(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
	pcs.Status.ScalingProgress = &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{3}}
	pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 1}}}
	fakeClient := testutils.NewTestClientBuilder().WithObjects(pcs).WithStatusSubresource(pcs).Build()
	reconciler := &Reconciler{
		client:                        fakeClient,
		eventRecorder:                 record.NewFakeRecorder(10),
		scalingProgressExpectations:   &pcsexpectations.ScalingProgress{},
		currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{},
	}
	originalPCS := pcs.DeepCopy()

	t.Run("persists nothing when no cleanup occurred", func(t *testing.T) {
		changed, err := reconciler.commitPCSCleanupStatus(context.Background(), logr.Discard(), pcs, originalPCS, nil, nil, nil)

		require.NoError(t, err)
		assert.False(t, changed)
	})

	t.Run("persists both scaling and update progress and emits a scale-in event", func(t *testing.T) {
		changed, err := reconciler.commitPCSCleanupStatus(context.Background(), logr.Discard(), pcs, originalPCS, []int32{2}, []int32{1}, nil)

		require.NoError(t, err)
		assert.True(t, changed)
		eventRecorder := reconciler.eventRecorder.(*record.FakeRecorder)
		assert.Equal(t, "Normal PodCliqueSetReplicaScaleInCompleted PCS replica index 2 completed scale-in", <-eventRecorder.Events)
	})
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

	changed, err := reconciler.commitPCSCleanupStatus(context.Background(), logr.Discard(), pcs, originalPCS, []int32{1}, nil, nil)

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
				client:                          fakeClient,
				pcsGenerationStatusExpectations: sync.Map{},
				currentlyUpdatingExpectations:   &pcsexpectations.CurrentlyUpdating{},
			}

			newGenerationHash := "new-hash"
			pcsObjectName := pcs.Namespace + "/" + pcs.Name

			targetSchedulingConfigurationHash := "target-scheduling-hash"
			err := reconciler.initOrResetUpdate(context.Background(), pcs, pcsObjectName, newGenerationHash, targetSchedulingConfigurationHash)
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
	r := &Reconciler{client: fakeClient, pcsGenerationStatusExpectations: sync.Map{}, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}

	err := r.initOrResetUpdate(context.Background(), pcs, pcsGenHashesExpKey(pcs), "new-hash", "applied-scheduling-hash")

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

func TestPreserveActiveReplicaSelectionsForSupersedingUpdate(t *testing.T) {
	updateStartedAt := metav1.Now()
	completedAt := metav1.Now()
	sourceCleanupCompletedAt := metav1.Now()
	canceledAt := metav1.Now()
	cancellationMessage := "replica-count reduction selected the replacement target"
	replacement := &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
		TargetReplicaIndex:       4,
		SourceCleanupCompletedAt: &sourceCleanupCompletedAt,
		CanceledAt:               &canceledAt,
		CancellationMessage:      &cancellationMessage,
	}
	pcs := &grovecorev1beta1.PodCliqueSet{
		Status: grovecorev1beta1.PodCliqueSetStatus{
			UpdateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
					{ReplicaIndex: 3, Replacement: replacement},
					{ReplicaIndex: 1},
					{ReplicaIndex: 0},
					{ReplicaIndex: 2, UpdateEndedAt: &completedAt},
				},
			},
			ScalingProgress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleInReplicaIndices: []int32{1},
			},
		},
	}

	got := preserveActiveReplicaSelectionsForSupersedingUpdate(pcs, updateStartedAt)

	require.Len(t, got, 2)
	assert.Equal(t, int32(0), got[0].ReplicaIndex)
	assert.Nil(t, got[0].Replacement)
	assert.Equal(t, updateStartedAt, got[0].UpdateStartedAt)
	assert.Nil(t, got[0].UpdateEndedAt)

	assert.Equal(t, int32(3), got[1].ReplicaIndex)
	require.NotNil(t, got[1].Replacement)
	assert.Equal(t, replacement, got[1].Replacement)
	assert.NotSame(t, replacement, got[1].Replacement)
	assert.NotSame(t, replacement.SourceCleanupCompletedAt, got[1].Replacement.SourceCleanupCompletedAt)
	assert.NotSame(t, replacement.CanceledAt, got[1].Replacement.CanceledAt)
	assert.NotSame(t, replacement.CancellationMessage, got[1].Replacement.CancellationMessage)
	assert.Equal(t, updateStartedAt, got[1].UpdateStartedAt)
	assert.Nil(t, got[1].UpdateEndedAt)
	assert.Nil(t, preserveActiveReplicaSelectionsForSupersedingUpdate(&grovecorev1beta1.PodCliqueSet{}, updateStartedAt))
}

func TestPreserveActiveReplicaSelectionsResetsReplacementCleanup(t *testing.T) {
	sourceCleanupCompletedAt := metav1.Now()
	canceledAt := metav1.Now()
	tests := []struct {
		name                     string
		targetPCSReplicaIndex    int32
		sourceCleanupCompletedAt *metav1.Time
		canceledAt               *metav1.Time
		wantCleanupCompletedAt   *metav1.Time
	}{
		{
			name:                     "active same-index replacement restarts cleanup",
			sourceCleanupCompletedAt: &sourceCleanupCompletedAt,
		},
		{
			name: "active same-index replacement already deleting",
		},
		{
			name:                     "canceled replacement preserves cleanup progress",
			sourceCleanupCompletedAt: &sourceCleanupCompletedAt,
			canceledAt:               &canceledAt,
			wantCleanupCompletedAt:   &sourceCleanupCompletedAt,
		},
		{
			name:                  "different-index replacement has no cleanup marker",
			targetPCSReplicaIndex: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
				WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
					CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
						ReplicaIndex: 0,
						Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
							TargetReplicaIndex:       tc.targetPCSReplicaIndex,
							SourceCleanupCompletedAt: tc.sourceCleanupCompletedAt,
							CanceledAt:               tc.canceledAt,
						},
					}},
				}).Build()
			before := pcs.DeepCopy()
			updateStartedAt := metav1.Now()

			got := preserveActiveReplicaSelectionsForSupersedingUpdate(pcs, updateStartedAt)

			require.Len(t, got, 1)
			assert.Equal(t, int32(0), got[0].ReplicaIndex)
			assert.Equal(t, updateStartedAt, got[0].UpdateStartedAt)
			assert.Nil(t, got[0].UpdateEndedAt)
			require.NotNil(t, got[0].Replacement)
			assert.Equal(t, tc.targetPCSReplicaIndex, got[0].Replacement.TargetReplicaIndex)
			assert.Equal(t, tc.wantCleanupCompletedAt, got[0].Replacement.SourceCleanupCompletedAt)
			assert.Equal(t, tc.canceledAt, got[0].Replacement.CanceledAt)
			assert.NotSame(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement, got[0].Replacement)
			assert.Equal(t, before, pcs, "supersession must not mutate the input progress")
		})
	}
}

// TestPCSGenerationStatusExpectationKey verifies the generation-status expectation key includes the PCS UID so two
// PodCliqueSets with the same namespace and name but different UIDs do not share a key.
func TestPCSGenerationStatusExpectationKey(t *testing.T) {
	pcs1 := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid-1").Build()
	pcs2 := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid-2").Build()

	key1 := pcsGenHashesExpKey(pcs1)

	assert.Equal(t, testNamespace+"/"+testPCSName+"/uid-1", key1)
	assert.NotEqual(t, key1, pcsGenHashesExpKey(pcs2))
}

func TestIsGenerationStatusExpectationSatisfied(t *testing.T) {
	schedulingHash := "scheduling"
	tests := []struct {
		name        string
		status      grovecorev1beta1.PodCliqueSetStatus
		expectation generationHashesExpectation
		want        bool
	}{
		{
			name: "both hashes match",
			status: grovecorev1beta1.PodCliqueSetStatus{
				CurrentGenerationHash:              new("generation"),
				AppliedSchedulingConfigurationHash: new(schedulingHash),
			},
			expectation: generationHashesExpectation{
				currentGenerationHash:              "generation",
				appliedSchedulingConfigurationHash: new(schedulingHash),
			},
			want: true,
		},
		{
			name: "scheduling hash is stale",
			status: grovecorev1beta1.PodCliqueSetStatus{
				CurrentGenerationHash:              new("generation"),
				AppliedSchedulingConfigurationHash: new("stale"),
			},
			expectation: generationHashesExpectation{
				currentGenerationHash:              "generation",
				appliedSchedulingConfigurationHash: new(schedulingHash),
			},
		},
		{
			name: "generation hash is stale",
			status: grovecorev1beta1.PodCliqueSetStatus{
				CurrentGenerationHash:              new("stale"),
				AppliedSchedulingConfigurationHash: new(schedulingHash),
			},
			expectation: generationHashesExpectation{
				currentGenerationHash:              "generation",
				appliedSchedulingConfigurationHash: new(schedulingHash),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Reconciler{pcsGenerationStatusExpectations: sync.Map{}, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
			r.pcsGenerationStatusExpectations.Store("key", tt.expectation)
			assert.Equal(t, tt.want, r.isGenerationStatusExpectationSatisfied("key", tt.status))
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
	r := &Reconciler{client: fakeClient, pcsGenerationStatusExpectations: sync.Map{}, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
	// A previous incarnation with a different UID left a stale expectation in the map.
	stalePCS := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid-old").Build()
	r.pcsGenerationStatusExpectations.Store(pcsGenHashesExpKey(stalePCS), generationHashesExpectation{currentGenerationHash: "stale-hash"})

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

func TestProcessGenerationHashChangeBaselinesActiveLegacyUpdate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(3).WithMinAvailable(2).Build()).
		Build()
	legacyGenerationHash := computeLegacyGenerationHash(pcs)
	pcs.Status.CurrentGenerationHash = ptr.To(legacyGenerationHash)
	updateStartedAt := metav1.Now()
	pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{UpdateStartedAt: updateStartedAt}

	fakeClient := testutils.SetupFakeClient(pcs)
	reconciler := &Reconciler{client: fakeClient, pcsGenerationStatusExpectations: sync.Map{}, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
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
	reconciler := &Reconciler{client: fakeClient, pcsGenerationStatusExpectations: sync.Map{}, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
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
	reconciler := &Reconciler{client: fakeClient, pcsGenerationStatusExpectations: sync.Map{}, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
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
	reconciler := &Reconciler{client: fakeClient, pcsGenerationStatusExpectations: sync.Map{}, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
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
	reconciler := &Reconciler{client: fakeClient, pcsGenerationStatusExpectations: sync.Map{}, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
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
	reconciler := &Reconciler{client: fakeClient, pcsGenerationStatusExpectations: sync.Map{}, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
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
	reconciler := &Reconciler{client: fakeClient, pcsGenerationStatusExpectations: sync.Map{}, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
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
// generation-status expectation from the in-memory map.
func TestTriggerDeletionFlowClearsGenerationStatusExpectation(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid-del").Build()
	pcs.Finalizers = []string{apiconstants.FinalizerPodCliqueSet}

	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{
		client:                          fakeClient,
		operatorRegistry:                component.NewOperatorRegistry[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result](),
		reconcileStatusRecorder:         ctrlcommon.NewReconcileErrorRecorder(fakeClient),
		pcsGenerationStatusExpectations: sync.Map{},
		scalingProgressExpectations:     &pcsexpectations.ScalingProgress{},
		currentlyUpdatingExpectations:   &pcsexpectations.CurrentlyUpdating{},
	}
	key := pcsGenHashesExpKey(pcs)
	r.pcsGenerationStatusExpectations.Store(key, generationHashesExpectation{currentGenerationHash: "some-hash"})

	r.triggerDeletionFlow(context.Background(), logr.Discard(), pcs)

	_, present := r.pcsGenerationStatusExpectations.Load(key)
	assert.False(t, present, "generation-hash expectation should be cleared when the PCS is deleted")
}

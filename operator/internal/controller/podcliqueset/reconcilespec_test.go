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
	"sync"
	"testing"
	"time"

	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/sync"
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

func TestScaleInPCSReplicaIndicesAwaitingResourceCleanup(t *testing.T) {
	syncResults := map[component.Kind]pcssync.Result{
		component.KindPodClique: {
			ScaleInReplicaIndicesAwaitingResourceCleanup: []int{5, 1},
		},
		component.KindHeadlessService: {
			ScaleInReplicaIndicesAwaitingResourceCleanup: []int{3, 5},
		},
		component.KindPodGang: {},
	}

	assert.Equal(t, []int32{1, 3, 5}, sets.List(scaleInPCSReplicaIndicesAwaitingResourceCleanup(syncResults)))
}

func TestRemoveCompletedScaleInPCSReplicaIndices(t *testing.T) {
	tests := []struct {
		name                            string
		progress                        *grovecorev1beta1.PodCliqueSetScalingProgress
		syncResults                     map[component.Kind]pcssync.Result
		expectedChanged                 bool
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
			name: "all replicas await cleanup",
			progress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleInReplicaIndices: []int32{1, 3},
			},
			syncResults: map[component.Kind]pcssync.Result{
				component.KindPodClique: {
					ScaleInReplicaIndicesAwaitingResourceCleanup: []int{1, 3},
				},
			},
			expectedProgress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleInReplicaIndices: []int32{1, 3},
			},
		},
		{
			name: "completed replicas are removed",
			progress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleOutReplicaIndices: []int32{4},
				ScaleInReplicaIndices:  []int32{1, 3, 5},
			},
			syncResults: map[component.Kind]pcssync.Result{
				component.KindPodClique: {
					ScaleInReplicaIndicesAwaitingResourceCleanup: []int{3},
				},
			},
			expectedChanged: true,
			expectedProgress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleOutReplicaIndices: []int32{4},
				ScaleInReplicaIndices:  []int32{3},
			},
			expectedCompletedReplicaIndices: []int32{1, 5},
		},
		{
			name: "completed scale-in clears scaling progress",
			progress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleInReplicaIndices: []int32{1, 3},
			},
			expectedChanged:                 true,
			expectedCompletedReplicaIndices: []int32{1, 3},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).Build()
			pcs.Status.ScalingProgress = test.progress
			fakeClient := testutils.NewTestClientBuilder().WithObjects(pcs).WithStatusSubresource(pcs).Build()
			reconciler := &Reconciler{
				client:                      fakeClient,
				eventRecorder:               record.NewFakeRecorder(10),
				scalingProgressExpectations: &pcssync.ScalingProgressExpectations{},
			}

			changed, err := reconciler.removeCompletedScaleInPCSReplicaIndices(context.Background(), logr.Discard(), pcs, test.syncResults)

			require.NoError(t, err)
			assert.Equal(t, test.expectedChanged, changed)
			assert.Equal(t, test.expectedProgress, pcs.Status.ScalingProgress)
			persistedPCS := &grovecorev1beta1.PodCliqueSet{}
			require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), persistedPCS))
			assert.Equal(t, test.expectedProgress, persistedPCS.Status.ScalingProgress)
			eventRecorder := reconciler.eventRecorder.(*record.FakeRecorder)
			for _, pcsReplicaIndex := range test.expectedCompletedReplicaIndices {
				assert.Equal(t, fmt.Sprintf("Normal PodCliqueSetReplicaScaleInCompleted PCS replica index %d completed scale-in", pcsReplicaIndex), <-eventRecorder.Events)
			}
		})
	}
}

func TestRemoveCompletedScaleInPCSReplicaIndicesPatchFailure(t *testing.T) {
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
	reconciler := &Reconciler{
		client:                      fakeClient,
		eventRecorder:               record.NewFakeRecorder(10),
		scalingProgressExpectations: &pcssync.ScalingProgressExpectations{},
	}

	changed, err := reconciler.removeCompletedScaleInPCSReplicaIndices(context.Background(), logr.Discard(), pcs, nil)

	assert.False(t, changed)
	assert.ErrorIs(t, err, conflict)
	assert.Equal(t, []int32{1}, pcs.Status.ScalingProgress.ScaleInReplicaIndices)
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
			reconciler := &Reconciler{client: fakeClient}

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
				pcsGenerationHashExpectations: sync.Map{},
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
			assert.Equal(t, targetSchedulingConfigurationHash, updatedPCS.Status.UpdateProgress.TargetSchedulingConfigurationHash)
			assert.Equal(t, tt.expectRequiresReplicaRecreation, updatedPCS.Status.UpdateProgress.RequiresReplicaRecreation)
			require.NotNil(t, updatedPCS.Status.UpdateProgress.SupersededGenerationHash)
			assert.Equal(t, "old-hash", *updatedPCS.Status.UpdateProgress.SupersededGenerationHash)

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

func TestInitOrResetUpdatePreservesReplicaRecreationAcrossSupersession(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithReplicas(2).
		WithPodCliqueParameters("worker", 1, nil).
		WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.CoherentStrategy}).
		WithPodCliqueSetGenerationHash(new("old-hash")).
		WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
			RequiresReplicaRecreation: true,
			CurrentlyUpdating:         []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 1}},
		}).
		Build()
	pcs.Status.AppliedSchedulingConfigurationHash = new("applied-scheduling-hash")
	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{client: fakeClient, pcsGenerationHashExpectations: sync.Map{}}

	err := r.initOrResetUpdate(context.Background(), pcs, pcsGenerationHashKey(pcs), "new-hash", "applied-scheduling-hash")

	require.NoError(t, err)
	updated := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), updated))
	require.NotNil(t, updated.Status.UpdateProgress)
	assert.True(t, updated.Status.UpdateProgress.RequiresReplicaRecreation)
	assert.Empty(t, updated.Status.UpdateProgress.InScopeStandalonePodCliques)
	require.Len(t, updated.Status.UpdateProgress.CurrentlyUpdating, 1)
	assert.Equal(t, int32(1), updated.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex)
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

func TestCarryCurrentlyUpdatingReplicaProgress(t *testing.T) {
	updateStartedAt := metav1.Now()
	completedAt := metav1.Now()
	pcs := &grovecorev1beta1.PodCliqueSet{
		Status: grovecorev1beta1.PodCliqueSetStatus{
			UpdateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
					{ReplicaIndex: 3},
					{ReplicaIndex: 1},
					{ReplicaIndex: 2, UpdateEndedAt: &completedAt},
				},
			},
			ScalingProgress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleInReplicaIndices: []int32{1},
			},
		},
	}

	got := carryCurrentlyUpdatingReplicaProgress(pcs, updateStartedAt)

	require.Len(t, got, 1)
	assert.Equal(t, int32(3), got[0].ReplicaIndex)
	assert.Equal(t, updateStartedAt, got[0].UpdateStartedAt)
	assert.Nil(t, got[0].UpdateEndedAt)
	assert.Nil(t, carryCurrentlyUpdatingReplicaProgress(&grovecorev1beta1.PodCliqueSet{}, updateStartedAt))
}

// TestPCSGenerationHashKey verifies the generation-hash expectation key includes the PCS UID so two
// PodCliqueSets with the same namespace and name but different UIDs do not share a key.
func TestPCSGenerationHashKey(t *testing.T) {
	pcs1 := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid-1").Build()
	pcs2 := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid-2").Build()

	key1 := pcsGenerationHashKey(pcs1)

	assert.Equal(t, testNamespace+"/"+testPCSName+"/uid-1", key1)
	assert.NotEqual(t, key1, pcsGenerationHashKey(pcs2))
}

// TestProcessGenerationHashChangeNotBlockedByStaleSameNameExpectation verifies that an expectation
// left by a previously deployed PodCliqueSet does not block a newly created
// PodCliqueSet with a different UID but with the same name as the previously deployed PodCliqueSet.
func TestProcessGenerationHashChangeNotBlockedByStaleSameNameExpectation(t *testing.T) {
	newPCS := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid-new").
		WithPodCliqueParameters("worker", 1, nil).
		Build()

	fakeClient := testutils.SetupFakeClient(newPCS)
	r := &Reconciler{client: fakeClient, pcsGenerationHashExpectations: sync.Map{}}
	// A previous incarnation with a different UID left a stale expectation in the map.
	stalePCS := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid-old").Build()
	r.pcsGenerationHashExpectations.Store(pcsGenerationHashKey(stalePCS), "stale-hash")

	result := r.processGenerationHashChange(context.Background(), logr.Discard(), newPCS)

	require.False(t, result.NeedsRequeue(), "a same-name recreate must not be blocked by a stale expectation")
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
	reconciler := &Reconciler{client: fakeClient, pcsGenerationHashExpectations: sync.Map{}}
	result := reconciler.processGenerationHashChange(context.Background(), logr.Discard(), pcs)

	require.False(t, result.HasErrors())
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
	pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
		TargetSchedulingConfigurationHash: schedulingHash,
		UpdateStartedAt:                   updateStartedAt,
	}

	fakeClient := testutils.SetupFakeClient(pcs)
	reconciler := &Reconciler{client: fakeClient, pcsGenerationHashExpectations: sync.Map{}}
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
	reconciler := &Reconciler{client: fakeClient, pcsGenerationHashExpectations: sync.Map{}}
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
	reconciler := &Reconciler{client: fakeClient, pcsGenerationHashExpectations: sync.Map{}}
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
	reconciler := &Reconciler{client: fakeClient, pcsGenerationHashExpectations: sync.Map{}}
	result := reconciler.processGenerationHashChange(context.Background(), logr.Discard(), pcs)

	require.False(t, result.HasErrors())
	updated := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), updated))
	require.NotNil(t, updated.Status.CurrentGenerationHash)
	assert.Equal(t, computeGenerationHash(pcs), *updated.Status.CurrentGenerationHash)
	assert.Nil(t, updated.Status.UpdateProgress)
}

func TestProcessGenerationHashChangePreservesLegacyContentUpdate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(3).WithMinAvailable(2).Build()).
		Build()
	pcs.Status.CurrentGenerationHash = ptr.To(computeLegacyGenerationHash(pcs))
	pcs.Spec.Template.Cliques[0].Annotations = map[string]string{"revision": "new"}

	fakeClient := testutils.SetupFakeClient(pcs)
	reconciler := &Reconciler{client: fakeClient, pcsGenerationHashExpectations: sync.Map{}}
	baselineResult := reconciler.processGenerationHashChange(context.Background(), logr.Discard(), pcs)
	require.False(t, baselineResult.HasErrors())

	baselined := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), baselined))
	reconciler.pcsGenerationHashExpectations.Delete(pcsGenerationHashKey(baselined))
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
	reconciler := &Reconciler{client: fakeClient, pcsGenerationHashExpectations: sync.Map{}}
	result := reconciler.processGenerationHashChange(context.Background(), logr.Discard(), pcs)

	require.False(t, result.HasErrors())
	updated := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), updated))
	assert.Equal(t, appliedSchedulingHash, *updated.Status.AppliedSchedulingConfigurationHash)
	require.NotNil(t, updated.Status.UpdateProgress)
	assert.Equal(t, computeSchedulingConfigurationHash(buildPCSSchedulingConfig(pcs)), updated.Status.UpdateProgress.TargetSchedulingConfigurationHash)
}

// TestTriggerDeletionFlowClearsGenerationHashExpectation verifies the deletion flow removes the PCS
// generation-hash expectation from the in-memory map.
func TestTriggerDeletionFlowClearsGenerationHashExpectation(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid-del").Build()
	pcs.Finalizers = []string{apiconstants.FinalizerPodCliqueSet}

	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{
		client:                        fakeClient,
		operatorRegistry:              component.NewOperatorRegistry[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result](),
		reconcileStatusRecorder:       ctrlcommon.NewReconcileErrorRecorder(fakeClient),
		pcsGenerationHashExpectations: sync.Map{},
		scalingProgressExpectations:   &pcssync.ScalingProgressExpectations{},
	}
	key := pcsGenerationHashKey(pcs)
	r.pcsGenerationHashExpectations.Store(key, "some-hash")

	r.triggerDeletionFlow(context.Background(), logr.Discard(), pcs)

	_, present := r.pcsGenerationHashExpectations.Load(key)
	assert.False(t, present, "generation-hash expectation should be cleared when the PCS is deleted")
}

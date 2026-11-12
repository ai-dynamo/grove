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

// TestUpdateStartsOnPodTemplateChange verifies that a pod template change starts an update for every strategy. An
// update already recorded is replaced, its counts are reset, and the generation hash is recomputed from the new
// template. OnDelete ends the update right away.
func TestUpdateStartsOnPodTemplateChange(t *testing.T) {
	tests := []struct {
		name                   string
		strategy               *grovecorev1beta1.PodCliqueSetUpdateStrategy
		existingProgress       *grovecorev1beta1.PodCliqueSetUpdateProgress
		expectUpdateEndedAtSet bool
	}{
		{
			name:     "rolling_recreate_strategy_does_not_set_update_ended_at",
			strategy: &grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.RollingRecreateStrategy},
		},
		{
			name: "nil_update_strategy_does_not_set_update_ended_at",
		},
		{
			name:                   "on_delete_strategy_sets_update_ended_at_immediately",
			strategy:               &grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.OnDeleteStrategy},
			expectUpdateEndedAtSet: true,
		},
		{
			name:     "existing_rollingrecreate_in_progress_is_reset_when_pod_template_changes",
			strategy: &grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.RollingRecreateStrategy},
			existingProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				UpdateStartedAt:                    metav1.Now(),
				UpdatedPodCliqueScalingGroupsCount: 2,
				TotalPodCliqueScalingGroupsCount:   2,
				UpdatedPodCliquesCount:             3,
				TotalPodCliquesCount:               3,
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
					{ReplicaIndex: 1, UpdateStartedAt: metav1.Now()},
				},
			},
		},
		{
			name:     "existing_on_delete_update_is_reset_when_pod_template_changes",
			strategy: &grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.OnDeleteStrategy},
			existingProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				UpdateStartedAt:                    metav1.Now(),
				UpdateEndedAt:                      ptr.To(metav1.Now()),
				UpdatedPodCliqueScalingGroupsCount: 1,
				TotalPodCliqueScalingGroupsCount:   1,
				UpdatedPodCliquesCount:             2,
				TotalPodCliquesCount:               2,
			},
			expectUpdateEndedAtSet: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
				WithReplicas(3).
				WithPodCliqueParameters("worker", 1, nil).
				WithUpdateStrategy(tt.strategy).
				WithPodCliqueSetGenerationHash(ptr.To("old-hash")).
				WithUpdateProgress(tt.existingProgress).
				Build()
			pcs.Status.UpdatedReplicas = 2
			previous := newTestRevision(t, pcs, &pcs.Spec.Template, 1)
			pcs.Status.UpdateRevision = previous.Name
			pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "new-image"

			fakeClient := testutils.SetupFakeClient(pcs, previous)
			reconciler := &Reconciler{client: fakeClient}
			require.False(t, reconciler.ensureUpdateRevision(context.Background(), logr.Discard(), pcs).HasErrors())

			updatedPCS := &grovecorev1beta1.PodCliqueSet{}
			require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), updatedPCS))
			require.NotNil(t, updatedPCS.Status.UpdateProgress, "UpdateProgress should be set")
			assert.NotEmpty(t, updatedPCS.Status.UpdateProgress.UpdateStartedAt, "UpdateStartedAt should be set")
			if tt.expectUpdateEndedAtSet {
				require.NotNil(t, updatedPCS.Status.UpdateProgress.UpdateEndedAt, "UpdateEndedAt should be set for OnDelete strategy")
			} else {
				assert.Nil(t, updatedPCS.Status.UpdateProgress.UpdateEndedAt, "UpdateEndedAt should be nil for non-OnDelete strategy")
			}
			assert.Equal(t, int32(0), updatedPCS.Status.UpdateProgress.UpdatedPodCliqueScalingGroupsCount, "UpdatedPodCliqueScalingGroupsCount should be reset to 0")
			assert.Equal(t, int32(0), updatedPCS.Status.UpdateProgress.UpdatedPodCliquesCount, "UpdatedPodCliquesCount should be reset to 0")
			assert.Nil(t, updatedPCS.Status.UpdateProgress.CurrentlyUpdating, "CurrentlyUpdating should be nil")
			assert.Equal(t, int32(0), updatedPCS.Status.UpdatedReplicas, "UpdatedReplicas should be reset to 0")
			assert.NotEqual(t, previous.Name, updatedPCS.Status.UpdateRevision)
			require.NotNil(t, updatedPCS.Status.CurrentGenerationHash)
			assert.Equal(t, computeGenerationHash(&pcs.Spec.Template), *updatedPCS.Status.CurrentGenerationHash)
		})
	}
}

// TestUpdateRevisionExpectationKey verifies the update-revision expectation key includes the PCS UID so two
// PodCliqueSets with the same namespace and name but different UIDs do not share a key.
func TestUpdateRevisionExpectationKey(t *testing.T) {
	pcs1 := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid-1").Build()
	pcs2 := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid-2").Build()

	key1 := updateRevisionExpectationKey(pcs1)

	assert.Equal(t, testNamespace+"/"+testPCSName+"/uid-1", key1)
	assert.NotEqual(t, key1, updateRevisionExpectationKey(pcs2))
}

// TestEnsureUpdateRevisionNotBlockedByStaleSameNameExpectation verifies that an expectation
// left by a previously deployed PodCliqueSet does not block a newly created
// PodCliqueSet with a different UID but with the same name as the previously deployed PodCliqueSet.
func TestEnsureUpdateRevisionNotBlockedByStaleSameNameExpectation(t *testing.T) {
	newPCS := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid-new").
		WithPodCliqueParameters("worker", 1, nil).
		Build()

	fakeClient := testutils.SetupFakeClient(newPCS)
	r := &Reconciler{client: fakeClient}
	// A previous incarnation with a different UID left a stale expectation in the map.
	stalePCS := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid-old").Build()
	r.updateRevisionExpectations.Store(updateRevisionExpectationKey(stalePCS), "stale-revision")

	result := r.ensureUpdateRevision(context.Background(), logr.Discard(), newPCS)

	require.False(t, result.NeedsRequeue(), "a same-name recreate must not be blocked by a stale expectation")
	updated := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(newPCS), updated))
	assert.NotEmpty(t, updated.Status.UpdateRevision, "update revision should be set on the new PCS")
	assert.NotNil(t, updated.Status.CurrentGenerationHash, "generation hash should be set on the new PCS")
}

// TestTriggerDeletionFlowClearsUpdateRevisionExpectation verifies the deletion flow removes the PCS
// update-revision expectation from the in-memory map.
func TestTriggerDeletionFlowClearsUpdateRevisionExpectation(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid-del").Build()
	pcs.Finalizers = []string{apiconstants.FinalizerPodCliqueSet}

	fakeClient := testutils.SetupFakeClient(pcs)
	r := &Reconciler{
		client:                      fakeClient,
		operatorRegistry:            component.NewOperatorRegistry[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result](),
		reconcileStatusRecorder:     ctrlcommon.NewReconcileErrorRecorder(fakeClient),
		scalingProgressExpectations: &pcssync.ScalingProgressExpectations{},
	}
	key := updateRevisionExpectationKey(pcs)
	r.updateRevisionExpectations.Store(key, "some-revision")

	r.triggerDeletionFlow(context.Background(), logr.Discard(), pcs)

	_, present := r.updateRevisionExpectations.Load(key)
	assert.False(t, present, "update-revision expectation should be cleared when the PCS is deleted")
}

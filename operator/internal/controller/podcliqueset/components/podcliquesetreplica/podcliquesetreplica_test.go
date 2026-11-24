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

package podcliquesetreplica

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	pcsexpectations "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/expectations"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/sync"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestNew tests creating a new PodCliqueSetReplica operator
func TestNew(t *testing.T) {
	// Tests creating a new operator instance
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	eventRecorder := &record.FakeRecorder{}

	scalingExpectations := &pcsexpectations.ScalingProgress{}
	currentlyUpdatingExpectations := &pcsexpectations.CurrentlyUpdating{}
	generationExpectations := &pcsexpectations.GenerationHashes{}
	operator := New(client, eventRecorder, scalingExpectations, currentlyUpdatingExpectations, generationExpectations)

	assert.NotNil(t, operator)
	resource, ok := operator.(*_resource)
	assert.True(t, ok)
	assert.Equal(t, client, resource.client)
	assert.Equal(t, eventRecorder, resource.eventRecorder)
	assert.Same(t, generationExpectations, resource.generationHashExpectations)
	assert.Same(t, scalingExpectations, resource.scalingProgressExpectations)
	assert.Same(t, currentlyUpdatingExpectations, resource.currentlyUpdatingExpectations)
}

// TestGetExistingResourceNames tests the GetExistingResourceNames function
func TestGetExistingResourceNames(t *testing.T) {
	// Tests that GetExistingResourceNames always returns empty slice
	scheme := runtime.NewScheme()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &_resource{
		client: client,
	}

	names, err := r.GetExistingResourceNames(context.Background(), logr.Discard(), metav1.ObjectMeta{})

	assert.NoError(t, err)
	assert.Empty(t, names)
}

func TestSyncPersistsScaleOutFromZeroAndPopulatesState(t *testing.T) {
	observedGeneration := int64(1)
	pcs := &grovecorev1beta1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test-pcs",
			UID:       "test-uid",
		},
		Spec: grovecorev1beta1.PodCliqueSetSpec{
			Replicas: 2,
			Template: grovecorev1beta1.PodCliqueSetTemplateSpec{
				TerminationDelay: &metav1.Duration{},
			},
		},
		Status: grovecorev1beta1.PodCliqueSetStatus{
			ObservedGeneration: &observedGeneration,
		},
	}
	_, schedulingHash, generationHash := computePCSHashes(pcs)
	initializePCSGenerationBaseline(pcs, schedulingHash, generationHash)
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pcs).
		WithStatusSubresource(pcs).
		Build()
	r := &_resource{
		client:                        fakeClient,
		eventRecorder:                 &record.FakeRecorder{},
		scalingProgressExpectations:   &pcsexpectations.ScalingProgress{},
		currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{},
	}
	state := &pcssync.State{}

	_, err := r.Sync(context.Background(), logr.Discard(), pcs, state)

	assert.True(t, groveerr.IsContinueReconcileAndRequeue(err))
	assert.Equal(t, pcssync.State{
		ExpectedReplicaIndices: []int{0, 1},
		ScaleOutReplicaIndices: []int{0, 1},
	}, *state)
	assert.Equal(t, &grovecorev1beta1.PodCliqueSetScalingProgress{
		ScaleOutReplicaIndices: []int32{0, 1},
	}, pcs.Status.ScalingProgress)
}

func TestSyncGathersEachReplicaResourceTypeOnceForCoherentUpdate(t *testing.T) {
	pcs := coherentTestPCS([]string{"frontend"}, nil)
	pcs.Spec.Replicas = 0
	pcs.Spec.Template.TerminationDelay = &metav1.Duration{}
	_, schedulingHash, generationHash := computePCSHashes(pcs)
	pcs.Status.CurrentGenerationHash = new(generationHash)
	pcs.Status.AppliedSchedulingConfigurationHash = new(schedulingHash)
	pcs.Status.UpdateProgress.TargetSchedulingConfigurationHash = schedulingHash
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pcs).
		WithStatusSubresource(pcs).
		Build()
	countingClient := &listCountingClient{
		Client:     fakeClient,
		listCounts: make(map[string]int),
	}
	r := &_resource{
		client:                        countingClient,
		eventRecorder:                 &record.FakeRecorder{},
		scalingProgressExpectations:   &pcsexpectations.ScalingProgress{},
		currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{},
	}

	_, err := r.Sync(context.Background(), logr.Discard(), pcs, &pcssync.State{})

	assert.NoError(t, err)
	assert.Equal(t, map[string]int{
		"PodCliqueList":             1,
		"PodCliqueScalingGroupList": 1,
		"ServiceList":               1,
		"PodGangMapList":            1,
	}, countingClient.listCounts)
}

// TestSync tests the Sync function
func TestSync(t *testing.T) {
	tests := []struct {
		name string
		// pcs is the PodCliqueSet to sync
		pcs *grovecorev1beta1.PodCliqueSet
		// existingObjs are the existing objects in the cluster
		existingObjs []runtime.Object
		// expectError indicates if an error is expected
		expectError bool
	}{
		{
			// Tests sync with no replicas
			name: "no_replicas",
			pcs: &grovecorev1beta1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
				},
				Spec: grovecorev1beta1.PodCliqueSetSpec{
					Replicas: 0,
					Template: grovecorev1beta1.PodCliqueSetTemplateSpec{
						TerminationDelay: &metav1.Duration{},
					},
				},
			},
			existingObjs: []runtime.Object{},
			expectError:  false,
		},
		{
			// Tests sync with replicas
			name: "with_replicas",
			pcs: &grovecorev1beta1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
				},
				Spec: grovecorev1beta1.PodCliqueSetSpec{
					Replicas: 2,
					Template: grovecorev1beta1.PodCliqueSetTemplateSpec{
						TerminationDelay: &metav1.Duration{},
					},
				},
			},
			existingObjs: []runtime.Object{},
			expectError:  false,
		},
	}

	ctx := context.Background()
	logger := logr.Discard()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, grovecorev1beta1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			existingObjs := append(slices.Clone(tc.existingObjs), tc.pcs)
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(existingObjs...).
				WithStatusSubresource(tc.pcs).
				Build()

			r := &_resource{
				generationHashExpectations:    &pcsexpectations.GenerationHashes{},
				client:                        fakeClient,
				eventRecorder:                 &record.FakeRecorder{},
				scalingProgressExpectations:   &pcsexpectations.ScalingProgress{},
				currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{},
			}

			_, err := r.Sync(ctx, logger, tc.pcs, &pcssync.State{})

			if tc.expectError {
				assert.Error(t, err)
			} else {
				// May return a requeue error which is expected
				if err != nil {
					if groveerr.IsContinueReconcileAndRequeue(err) || groveerr.IsRequeueAfter(err) {
						// This is expected
					} else {
						t.Errorf("Unexpected error: %v", err)
					}
				}
			}
		})
	}
}

func TestSyncGenerationBaselineStopsBeforeResourceDiscovery(t *testing.T) {
	t.Parallel()
	pcs := generationTestPCS()
	pcs.Status.CurrentGenerationHash = new("legacy-target")
	fakeClient := testutils.SetupFakeClient(pcs)
	countingClient := &listCountingClient{Client: fakeClient, listCounts: make(map[string]int)}
	generationExpectations := &pcsexpectations.GenerationHashes{}
	r := &_resource{client: countingClient, generationHashExpectations: generationExpectations,
		currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}, scalingProgressExpectations: &pcsexpectations.ScalingProgress{}}
	state := &pcssync.State{}

	_, err := r.Sync(t.Context(), logr.Discard(), pcs, state)

	require.True(t, groveerr.IsRequeueAfter(err), "baseline must hard-requeue: %v", err)
	assert.Empty(t, countingClient.listCounts)
	assert.Equal(t, &pcssync.State{}, state)
	storedPCS := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), storedPCS))
	assert.Equal(t, new("legacy-target"), storedPCS.Status.CurrentGenerationHash)
	assert.NotNil(t, storedPCS.Status.AppliedSchedulingConfigurationHash)
	assert.Nil(t, storedPCS.Status.UpdateProgress)
	assert.False(t, generationExpectations.ClearIfSatisfied(pcs.ObjectMeta, new("legacy-target"), nil))
	assert.True(t, generationExpectations.ClearIfSatisfied(storedPCS.ObjectMeta, storedPCS.Status.CurrentGenerationHash, storedPCS.Status.AppliedSchedulingConfigurationHash))
}

// TestSyncGenerationFailureIsolationAndRetry verifies that a failed supersession status update stops
// reconciliation without persisting prepared replacement progress. Error recording preserves server cleanup
// obligations, and a retry from freshly fetched status performs the handoff exactly once.
func TestSyncGenerationFailureIsolationAndRetry(t *testing.T) {
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
				RecordErrorForObjectsNTimes(testutils.ClientMethodStatusPatch, conflict, 1, client.ObjectKeyFromObject(pcs)).Build()
			currentlyUpdatingExpectations := &pcsexpectations.CurrentlyUpdating{}
			reconciler := &_resource{
				client:                        fakeClient,
				currentlyUpdatingExpectations: currentlyUpdatingExpectations,
				generationHashExpectations:    &pcsexpectations.GenerationHashes{},
				scalingProgressExpectations:   &pcsexpectations.ScalingProgress{},
			}
			errorRecorder := ctrlcommon.NewReconcileErrorRecorder(fakeClient)
			before := pcs.DeepCopy()
			_, targetSchedulingHash, targetGenerationHash := computePCSHashes(pcs)

			_, failedErr := reconciler.Sync(t.Context(), logr.Discard(), pcs, &pcssync.State{})
			require.Error(t, failedErr)
			failedResult := ctrlcommon.ReconcileWithErrors("generation preparation failed", failedErr)

			require.True(t, failedResult.HasErrors())
			assert.True(t, ctrlcommon.ShortCircuitReconcileFlow(failedResult), "failed initialization must stop the spec reconciliation before child sync")
			var groveError *groveerr.GroveError
			require.ErrorAs(t, failedErr, &groveError)
			assert.ErrorIs(t, groveError.Cause, conflict)
			assert.Equal(t, before, pcs, "a failed generation patch must leave the original PCS unchanged")
			assert.True(t, reconciler.generationHashExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil, nil), "a failed status write must not record a generation expectation")
			assert.True(t, currentlyUpdatingExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil), "a failed status write must not record an update-selection expectation")

			require.NoError(t, errorRecorder.RecordErrors(t.Context(), pcs, &failedResult))

			storedPCS := &grovecorev1beta1.PodCliqueSet{}
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), storedPCS))
			assert.Equal(t, before.Status.CurrentGenerationHash, storedPCS.Status.CurrentGenerationHash)
			assert.Equal(t, before.Status.AppliedSchedulingConfigurationHash, storedPCS.Status.AppliedSchedulingConfigurationHash)
			assert.True(t, apiequality.Semantic.DeepEqual(before.Status.UpdateProgress, storedPCS.Status.UpdateProgress), "the LastErrors patch must preserve the original replacement mapping, barrier, and retired sources")
			require.Len(t, storedPCS.Status.LastErrors, 1)
			assert.Equal(t, errCodeUpdatePCSStatus, storedPCS.Status.LastErrors[0].Code)
			require.Len(t, before.Status.LastErrors, 1)
			assert.Equal(t, before.Status.ScalingProgress, storedPCS.Status.ScalingProgress)

			_, retryErr := reconciler.Sync(t.Context(), logr.Discard(), storedPCS, &pcssync.State{})
			require.True(t, groveerr.IsRequeueAfter(retryErr), "successful initialization must stop for cache acknowledgment: %v", retryErr)
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
			assert.False(t, reconciler.generationHashExpectations.ClearIfSatisfied(pcs.ObjectMeta, before.Status.CurrentGenerationHash, before.Status.AppliedSchedulingConfigurationHash))
			assert.True(t, reconciler.generationHashExpectations.ClearIfSatisfied(updatedPCS.ObjectMeta, updatedPCS.Status.CurrentGenerationHash, updatedPCS.Status.AppliedSchedulingConfigurationHash))
			assert.False(t, currentlyUpdatingExpectations.ClearIfSatisfied(updatedPCS.ObjectMeta, before.Status.UpdateProgress.CurrentlyUpdating))
			assert.True(t, currentlyUpdatingExpectations.ClearIfSatisfied(updatedPCS.ObjectMeta, updatedPCS.Status.UpdateProgress.CurrentlyUpdating))
		})
	}
}

type listCountingClient struct {
	client.Client
	listCounts map[string]int
}

func (c *listCountingClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	c.listCounts[reflect.TypeOf(list).Elem().Name()]++
	return c.Client.List(ctx, list, opts...)
}

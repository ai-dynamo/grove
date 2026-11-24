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
	"fmt"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
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
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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
	clk := clocktesting.NewFakeClock(time.Unix(1000, 0).UTC())
	operator := New(client, eventRecorder, scalingExpectations, currentlyUpdatingExpectations, generationExpectations, clk)

	assert.NotNil(t, operator)
	resource, ok := operator.(*_resource)
	assert.True(t, ok)
	assert.Equal(t, client, resource.client)
	assert.Equal(t, eventRecorder, resource.eventRecorder)
	assert.Same(t, clk, resource.clk)
	assert.Same(t, generationExpectations, resource.generationHashExpectations)
	assert.Same(t, scalingExpectations, resource.scalingProgressExpectations)
	assert.Same(t, currentlyUpdatingExpectations, resource.currentlyUpdatingExpectations)
}

// TestGetExistingResourceNames tests the GetExistingResourceNames function
func TestGetExistingResourceNames(t *testing.T) {
	// Tests that GetExistingResourceNames always returns empty slice
	scheme := runtime.NewScheme()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &_resource{clk: clocktesting.NewFakeClock(time.Unix(1000, 0).UTC()),
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
	r := &_resource{clk: clocktesting.NewFakeClock(time.Unix(1000, 0).UTC()),
		client:                        fakeClient,
		eventRecorder:                 &record.FakeRecorder{},
		scalingProgressExpectations:   &pcsexpectations.ScalingProgress{},
		currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{},
	}
	state := &pcssync.State{}

	_, err := r.Sync(context.Background(), logr.Discard(), pcs, state)

	require.True(t, groveerr.IsRequeueAfter(err), "new scaling progress must stop at the cache barrier: %v", err)
	assert.Equal(t, pcssync.State{}, *state)
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), pcs))
	require.True(t, r.scalingProgressExpectations.ClearIfSatisfied(pcs.ObjectMeta, pcs.Status.ScalingProgress))
	_, err = r.Sync(t.Context(), logr.Discard(), pcs, state)
	require.NoError(t, err)
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
	r := &_resource{clk: clocktesting.NewFakeClock(time.Unix(1000, 0).UTC()),
		client:                        countingClient,
		eventRecorder:                 &record.FakeRecorder{},
		scalingProgressExpectations:   &pcsexpectations.ScalingProgress{},
		currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{},
	}

	_, err := r.Sync(context.Background(), logr.Discard(), pcs, &pcssync.State{})

	assert.True(t, groveerr.IsRequeueAfter(err), "completion must stop at the cache barrier: %v", err)
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

			r := &_resource{clk: clocktesting.NewFakeClock(time.Unix(1000, 0).UTC()),
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
	r := &_resource{clk: clocktesting.NewFakeClock(time.Unix(1000, 0).UTC()), client: countingClient, generationHashExpectations: generationExpectations,
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
				WithTerminationDelay(time.Minute).
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
			reconciler := &_resource{clk: clocktesting.NewFakeClock(time.Unix(1000, 0).UTC()),
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

func TestSyncCombinesGenerationScalingAndUpdateSelection(t *testing.T) {
	for _, strategy := range []grovecorev1beta1.UpdateStrategyType{grovecorev1beta1.RollingRecreateStrategy, grovecorev1beta1.CoherentStrategy, grovecorev1beta1.OnDeleteStrategy} {
		for _, resume := range []bool{false, true} {
			for _, replicas := range []int32{0, 1} {
				t.Run(fmt.Sprintf("%s/resume=%t/replicas=%d", strategy, resume, replicas), func(t *testing.T) {
					t.Parallel()
					pcs := lifecycleTestPCS()
					pcs.Spec.UpdateStrategy = &grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: strategy}
					_, oldScheduling, oldGeneration := computePCSHashes(pcs)
					pcs.Status.CurrentGenerationHash = new(oldGeneration)
					pcs.Status.AppliedSchedulingConfigurationHash = new(oldScheduling)
					children := []client.Object{pcs}
					for index := range 2 {
						info := rollingUpdateReplicaInfo(pcs, index, true)
						children = append(children, &info.standalonePCLQs[0])
					}
					pcs.Spec.Replicas = replicas
					pcs.Spec.Template.Cliques[0].Spec.MinAvailable = new(int32(2))
					_, schedulingHash, generationHash := computePCSHashes(pcs)
					pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
						UpdateStartedAt:                   metav1.NewTime(time.Unix(100, 0).UTC()),
						TargetSchedulingConfigurationHash: oldScheduling,
						CurrentlyUpdating:                 []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 1}},
					}
					if resume {
						pcs.Status.CurrentGenerationHash = new(generationHash)
						pcs.Status.UpdateProgress.TargetSchedulingConfigurationHash = schedulingHash
						pcs.Status.UpdateProgress.RequiresReplicaRecreation = true
					}
					cl := testutils.SetupFakeClient(children...)
					r := lifecycleTestResource(cl)
					before := pcs.DeepCopy()
					patches := 0
					r.client = interceptor.NewClient(cl, interceptor.Funcs{
						SubResourcePatch: func(ctx context.Context, cl client.Client, name string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							patches++
							assert.Equal(t, before, pcs, "planning must not mutate the original")
							assert.True(t, r.generationHashExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil, nil), "expectations must follow persistence")
							assert.True(t, r.scalingProgressExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil))
							assert.True(t, r.currentlyUpdatingExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil))
							return cl.SubResource(name).Patch(ctx, obj, patch, opts...)
						},
					})
					state := &pcssync.State{ExpectedReplicaIndices: []int{99}}
					_, err := r.Sync(t.Context(), logr.Discard(), pcs, state)
					require.True(t, groveerr.IsRequeueAfter(err), "%v", err)
					assert.Equal(t, 1, patches, "generation, scaling, selection and completion share one patch")
					assert.Equal(t, &pcssync.State{ExpectedReplicaIndices: []int{99}}, state)
					assert.Equal(t, new(generationHash), pcs.Status.CurrentGenerationHash)
					require.NotNil(t, pcs.Status.ScalingProgress)
					assert.True(t, pcs.Status.UpdateProgress.RequiresReplicaRecreation)
					if replicas == 0 {
						assert.Equal(t, []int32{0, 1}, pcs.Status.ScalingProgress.ScaleInReplicaIndices)
						assert.Empty(t, pcs.Status.UpdateProgress.CurrentlyUpdating)
						assert.NotNil(t, pcs.Status.UpdateProgress.UpdateEndedAt)
						assert.Equal(t, new(schedulingHash), pcs.Status.AppliedSchedulingConfigurationHash)
					} else {
						assert.Equal(t, []int32{1}, pcs.Status.ScalingProgress.ScaleInReplicaIndices)
						require.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1)
						selection := pcs.Status.UpdateProgress.CurrentlyUpdating[0]
						assert.Equal(t, int32(0), selection.ReplicaIndex)
						require.NotNil(t, selection.Replacement)
						assert.Equal(t, int32(0), selection.Replacement.TargetReplicaIndex)
						assert.Nil(t, selection.Replacement.SourceCleanupCompletedAt)
						assert.Equal(t, new(oldScheduling), pcs.Status.AppliedSchedulingConfigurationHash)
						if !resume {
							assert.True(t, pcs.Status.UpdateProgress.UpdateStartedAt.Equal(&selection.UpdateStartedAt), "one reconcile time governs preparation and selection")
						}
					}
					stored := &grovecorev1beta1.PodCliqueSet{}
					require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pcs), stored))
					assert.Equal(t, stored.Status, pcs.Status)
					assert.False(t, r.scalingProgressExpectations.ClearIfSatisfied(pcs.ObjectMeta, before.Status.ScalingProgress))
					assert.True(t, r.scalingProgressExpectations.ClearIfSatisfied(pcs.ObjectMeta, stored.Status.ScalingProgress))
					assert.True(t, r.generationHashExpectations.ClearIfSatisfied(pcs.ObjectMeta, stored.Status.CurrentGenerationHash, stored.Status.AppliedSchedulingConfigurationHash))
					assert.True(t, r.currentlyUpdatingExpectations.ClearIfSatisfied(pcs.ObjectMeta, stored.Status.UpdateProgress.CurrentlyUpdating))
					for _, child := range children[1:] {
						require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(child), &grovecorev1beta1.PodClique{}), "planning must not delete a child")
					}
				})
			}
		}
	}
}

func TestSyncGangTerminationFollowsProgressBarrier(t *testing.T) {
	for _, failure := range []string{"none", "conflict", "execution"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			pcs := lifecycleTestPCS()
			children := []client.Object{pcs}
			for index := range 3 {
				info := rollingUpdateReplicaInfo(pcs, index, true)
				if index == 0 || (failure == "execution" && index == 1) {
					info.standalonePCLQs[0].Status.LastScheduled = new(metav1.NewTime(time.Unix(50, 0).UTC()))
					info.standalonePCLQs[0].Status.Conditions = []metav1.Condition{{Type: apiconstants.ConditionTypeMinAvailableBreached, Status: metav1.ConditionTrue, LastTransitionTime: metav1.NewTime(time.Unix(100, 0).UTC())}}
				}
				children = append(children, &info.standalonePCLQs[0])
			}
			// Scale-in and a workload update are planned while replica 0 awaits gang termination.
			pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "new-image"
			cl := testutils.SetupFakeClient(children...)
			r := lifecycleTestResource(cl)
			before := pcs.DeepCopy()
			var deletes atomic.Int32
			patches := 0
			injected := errors.New("injected failure")
			r.client = interceptor.NewClient(cl, interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, cl client.Client, name string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					patches++
					assert.Zero(t, deletes.Load(), "gang deletion must not precede persistence")
					if failure == "conflict" {
						return apierrors.NewConflict(schema.GroupResource{Resource: "podcliquesets"}, pcs.Name, injected)
					}
					return cl.SubResource(name).Patch(ctx, obj, patch, opts...)
				},
				DeleteAllOf: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteAllOfOption) error {
					deletes.Add(1)
					options := &client.DeleteAllOfOptions{}
					for _, option := range opts {
						option.ApplyToDeleteAllOf(options)
					}
					replicaIndex, _ := options.LabelSelector.RequiresExactMatch(apicommon.LabelPodCliqueSetReplicaIndex)
					if failure == "execution" && replicaIndex == "1" {
						return injected
					}
					return cl.DeleteAllOf(ctx, obj, opts...)
				},
			})
			state := &pcssync.State{ExpectedReplicaIndices: []int{99}}
			_, err := r.Sync(t.Context(), logr.Discard(), pcs, state)
			assert.Equal(t, 1, patches)
			assert.Zero(t, deletes.Load())
			assert.Equal(t, &pcssync.State{ExpectedReplicaIndices: []int{99}}, state)
			if failure == "conflict" {
				assertGroveErrorCode(t, err, errCodeUpdatePCSStatus)
				assert.Equal(t, before, pcs)
				assert.Empty(t, r.eventRecorder.(*record.FakeRecorder).Events)
				assert.True(t, r.generationHashExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil, nil))
				assert.True(t, r.scalingProgressExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil))
				assert.True(t, r.currentlyUpdatingExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil))
				return
			}
			require.True(t, groveerr.IsRequeueAfter(err), "%v", err)
			require.NotNil(t, pcs.Status.ScalingProgress)
			require.Len(t, pcs.Status.ScalingProgress.ScaleInReplicaIndices, 1)
			require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pcs), pcs))
			require.True(t, r.generationHashExpectations.ClearIfSatisfied(pcs.ObjectMeta, pcs.Status.CurrentGenerationHash, pcs.Status.AppliedSchedulingConfigurationHash))
			require.True(t, r.scalingProgressExpectations.ClearIfSatisfied(pcs.ObjectMeta, pcs.Status.ScalingProgress))
			require.True(t, r.currentlyUpdatingExpectations.ClearIfSatisfied(pcs.ObjectMeta, pcs.Status.UpdateProgress.CurrentlyUpdating))
			persisted := pcs.DeepCopy()
			_, err = r.Sync(t.Context(), logr.Discard(), pcs, state)
			assert.Equal(t, 1, patches, "stable progress must not be written again")
			assert.Equal(t, persisted, pcs)
			if failure == "execution" {
				assert.Equal(t, int32(2), deletes.Load())
				assert.True(t, apierrors.IsNotFound(cl.Get(t.Context(), client.ObjectKeyFromObject(children[1]), &grovecorev1beta1.PodClique{})), "one task can succeed before another fails")
				require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(children[2]), &grovecorev1beta1.PodClique{}))
				assertGroveErrorCode(t, err, errCodeDeletePCSReplica)
				assert.Equal(t, &pcssync.State{ExpectedReplicaIndices: []int{99}}, state, "failed execution must not publish state")
				return
			}
			assert.True(t, groveerr.IsContinueReconcileAndRequeue(err), "%v", err)
			assert.Equal(t, int32(1), deletes.Load())
			assert.NotContains(t, state.ExpectedReplicaIndices, 99)
			assert.Len(t, state.ExpectedReplicaIndices, 2)
			terminated := &grovecorev1beta1.PodClique{}
			assert.True(t, apierrors.IsNotFound(cl.Get(t.Context(), client.ObjectKeyFromObject(children[1]), terminated)))
		})
	}
}

func TestSyncPlanningFailureDiscardsPreparedProgress(t *testing.T) {
	for _, failure := range []string{"discovery", "gang assessment"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			pcs := lifecycleTestPCS()
			info := rollingUpdateReplicaInfo(pcs, 0, true)
			pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "new-image"
			pcs.Spec.Replicas = 3
			cl := testutils.SetupFakeClient(pcs, &info.standalonePCLQs[0])
			r := lifecycleTestResource(cl)
			injected := errors.New("read failed")
			writes := 0
			r.client = interceptor.NewClient(cl, interceptor.Funcs{
				List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if _, found := list.(*corev1.ServiceList); found && failure == "discovery" {
						return injected
					}
					return cl.List(ctx, list, opts...)
				},
				Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, found := obj.(*grovecorev1beta1.PodClique); found && failure == "gang assessment" {
						return injected
					}
					return cl.Get(ctx, key, obj, opts...)
				},
				SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
					writes++
					return nil
				},
			})
			before := pcs.DeepCopy()
			state := &pcssync.State{ExpectedReplicaIndices: []int{99}}
			_, err := r.Sync(t.Context(), logr.Discard(), pcs, state)
			require.Error(t, err)
			assert.Equal(t, before, pcs)
			assert.Zero(t, writes)
			assert.Equal(t, &pcssync.State{ExpectedReplicaIndices: []int{99}}, state)
			assert.Empty(t, r.eventRecorder.(*record.FakeRecorder).Events)
			assert.True(t, r.generationHashExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil, nil))
			assert.True(t, r.scalingProgressExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil))
			assert.True(t, r.currentlyUpdatingExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil))
		})
	}
}

func TestSyncBootstrapCancellationUsesCurrentReplacementSource(t *testing.T) {
	for _, supersede := range []bool{false, true} {
		t.Run(fmt.Sprintf("supersede=%t", supersede), func(t *testing.T) {
			t.Parallel()
			pcs := lifecycleTestPCS()
			pcs.Status.ObservedGeneration = nil
			pcs.Spec.Replicas = 0
			cleanupTime := metav1.NewTime(time.Unix(100, 0).UTC())
			pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
				RequiresReplicaRecreation: true, TargetSchedulingConfigurationHash: *pcs.Status.AppliedSchedulingConfigurationHash,
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 2,
					Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1, SourceCleanupCompletedAt: &cleanupTime}}},
			}
			pcs.Status.AppliedSchedulingConfigurationHash = new("previous-scheduling")
			oldSource := rollingUpdateReplicaInfo(pcs, 2, false)
			readyTarget := rollingUpdateReplicaInfo(pcs, 1, true)
			if supersede {
				pcs.Spec.Template.Cliques[0].Spec.MinAvailable = new(int32(2))
			}
			cl := testutils.SetupFakeClient(pcs, &oldSource.standalonePCLQs[0], &readyTarget.standalonePCLQs[0])
			r := lifecycleTestResource(cl)
			state := &pcssync.State{}
			_, err := r.Sync(t.Context(), logr.Discard(), pcs, state)
			require.True(t, groveerr.IsRequeueAfter(err), "%v", err)
			assert.Equal(t, &pcssync.State{}, state)
			require.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1)
			progress := pcs.Status.UpdateProgress.CurrentlyUpdating[0]
			require.NotNil(t, progress.Replacement)
			require.NotNil(t, progress.Replacement.CanceledAt, "scale-to-zero must cancel even a ready target during bootstrap")
			assert.Nil(t, progress.UpdateEndedAt)
			assert.Nil(t, pcs.Status.UpdateProgress.UpdateEndedAt)
			if supersede {
				assert.Equal(t, int32(1), progress.ReplicaIndex)
				assert.Equal(t, []int32{2}, progress.Replacement.RetiredSourceReplicaIndices)
				assert.Nil(t, progress.Replacement.SourceCleanupCompletedAt)
				assert.True(t, pcs.Status.UpdateProgress.UpdateStartedAt.Equal(progress.Replacement.CanceledAt))
			} else {
				assert.Equal(t, int32(2), progress.ReplicaIndex)
				assert.True(t, cleanupTime.Equal(progress.Replacement.SourceCleanupCompletedAt))
			}
			persisted := pcs.DeepCopy()
			require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pcs), pcs))
			_, err = r.Sync(t.Context(), logr.Discard(), pcs, state)
			assert.True(t, groveerr.IsContinueReconcileAndRequeue(err), "%v", err)
			assert.Equal(t, persisted.Status, pcs.Status, "same configuration must preserve cancellation and cleanup markers")
			assert.Empty(t, state.ExpectedReplicaIndices)
			assert.Empty(t, state.ScaleOutReplicaIndices)
			assert.Equal(t, []int{1, 2}, state.ScaleInReplicaIndices)
		})
	}
}

func TestSyncBootstrapAllocationFollowsReplacementReservations(t *testing.T) {
	for _, source := range []int{0, 2} {
		t.Run(fmt.Sprintf("source=%d", source), func(t *testing.T) {
			t.Parallel()
			pcs := lifecycleTestPCS()
			pcs.Status.ObservedGeneration = nil
			if source == 0 {
				pcs.Spec.Replicas = 1
			}
			info := rollingUpdateReplicaInfo(pcs, source, true)
			pcs.Spec.Template.Cliques[0].Spec.MinAvailable = new(int32(2))
			cl := testutils.SetupFakeClient(pcs, &info.standalonePCLQs[0])
			r := lifecycleTestResource(cl)
			state := &pcssync.State{}
			_, err := r.Sync(t.Context(), logr.Discard(), pcs, state)
			require.True(t, groveerr.IsRequeueAfter(err), "%v", err)
			assert.Equal(t, &pcssync.State{}, state)
			require.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1)
			progress := pcs.Status.UpdateProgress.CurrentlyUpdating[0]
			assert.Equal(t, int32(source), progress.ReplicaIndex)
			require.NotNil(t, progress.Replacement)
			assert.Zero(t, progress.Replacement.TargetReplicaIndex, "replacement allocation precedes unreserved initial provisioning")
			require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pcs), pcs))
			_, err = r.Sync(t.Context(), logr.Discard(), pcs, state)
			require.True(t, groveerr.IsContinueReconcileAndRequeue(err), "%v", err)
			assert.Equal(t, []int{source}, state.ScaleInReplicaIndices)
			if source == 2 {
				assert.Equal(t, []int{0, 1}, state.ExpectedReplicaIndices, "initial provisioning must allocate 1, not collide with replacement target 0")
				assert.Equal(t, []int{0}, state.ScaleOutReplicaIndices)
				return
			}
			assert.Empty(t, state.ExpectedReplicaIndices)
			assert.Empty(t, state.ScaleOutReplicaIndices, "same-index creation must wait for persisted cleanup acknowledgment")
			// Model observed source absence and the separate participant-confirmed cleanup transaction.
			require.NoError(t, cl.Delete(t.Context(), &info.standalonePCLQs[0]))
			before := pcs.DeepCopy()
			pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.SourceCleanupCompletedAt = new(metav1.Now())
			require.NoError(t, cl.Status().Patch(t.Context(), pcs, client.MergeFrom(before)))
			require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pcs), pcs))
			_, err = r.Sync(t.Context(), logr.Discard(), pcs, state)
			require.True(t, groveerr.IsContinueReconcileAndRequeue(err), "%v", err)
			assert.Equal(t, []int{0}, state.ExpectedReplicaIndices)
			assert.Equal(t, []int{0}, state.ScaleOutReplicaIndices)
			assert.Empty(t, state.ScaleInReplicaIndices)
		})
	}
}

func TestSyncUpdateAdvanceFailureDiscardsScaling(t *testing.T) {
	t.Parallel()
	pcs := lifecycleTestPCS()
	pcs.Spec.UpdateStrategy = &grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.CoherentStrategy}
	pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
		TargetSchedulingConfigurationHash: *pcs.Status.AppliedSchedulingConfigurationHash,
		InScopeStandalonePodCliques:       []string{"worker"},
		CurrentlyUpdating:                 []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}},
	}
	info := rollingUpdateReplicaInfo(pcs, 0, false)
	pgm := testutils.NewPodGangMapBuilder(pcs.Name, pcs.Namespace, pcs.UID, 0).Build()
	pgm.Spec.Entries = []grovecorev1beta1.PodGangEntry{{Epoch: "invalid", PodCliqueSetGenerationHash: *pcs.Status.CurrentGenerationHash}}
	cl := testutils.SetupFakeClient(pcs, &info.standalonePCLQs[0], pgm)
	r := lifecycleTestResource(cl)
	before := pcs.DeepCopy()
	state := &pcssync.State{ExpectedReplicaIndices: []int{99}}
	_, err := r.Sync(t.Context(), logr.Discard(), pcs, state)
	assertGroveErrorCode(t, err, errCodeInvalidEpoch)
	assert.Equal(t, before, pcs)
	assert.Equal(t, &pcssync.State{ExpectedReplicaIndices: []int{99}}, state)
	assert.Empty(t, r.eventRecorder.(*record.FakeRecorder).Events)
	stored := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pcs), stored))
	assert.Equal(t, before.Status, stored.Status, "prepared scale-out must not survive a later update calculation error")
	assert.True(t, r.scalingProgressExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil))
	assert.True(t, r.currentlyUpdatingExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil))
}

func TestSyncUsesClockForGenerationAndCompletion(t *testing.T) {
	t.Parallel()
	clk := clocktesting.NewFakeClock(time.Unix(1000, 0).UTC())
	pcs := lifecycleTestPCS()
	pcs.Spec.Replicas = 0
	pcs.Spec.Template.Cliques[0].Spec.MinAvailable = new(int32(2))
	cl := testutils.SetupFakeClient(pcs)
	wantTime := metav1.NewTime(clk.Now())
	listCalls := 0
	clockAdvancingClient := interceptor.NewClient(cl, interceptor.Funcs{
		List: func(ctx context.Context, apiClient client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if listCalls == 0 {
				clk.Step(time.Minute)
			}
			listCalls++
			return apiClient.List(ctx, list, opts...)
		},
	})
	operator := New(clockAdvancingClient, record.NewFakeRecorder(10), &pcsexpectations.ScalingProgress{}, &pcsexpectations.CurrentlyUpdating{}, &pcsexpectations.GenerationHashes{}, clk)

	_, err := operator.Sync(t.Context(), logr.Discard(), pcs, &pcssync.State{})

	require.True(t, groveerr.IsRequeueAfter(err), "%v", err)
	require.NotNil(t, pcs.Status.UpdateProgress)
	require.Positive(t, listCalls, "resource discovery must advance the clock between planning stages")
	assert.True(t, wantTime.Equal(&pcs.Status.UpdateProgress.UpdateStartedAt), "generation start must use the injected clock")
	require.NotNil(t, pcs.Status.UpdateProgress.UpdateEndedAt)
	assert.True(t, wantTime.Equal(pcs.Status.UpdateProgress.UpdateEndedAt), "completion must use the same captured time")
	before := pcs.DeepCopy()
	clk.Step(time.Hour)
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pcs), pcs))
	_, err = operator.Sync(t.Context(), logr.Discard(), pcs, &pcssync.State{})
	require.NoError(t, err)
	assert.Equal(t, before.Status, pcs.Status, "later clock advances must not reopen or restamp a completed update")
	assert.Equal(t, before.ResourceVersion, pcs.ResourceVersion, "completed progress needs no additional write")
}

func TestSyncUsesClockForGangTerminationDelay(t *testing.T) {
	t.Parallel()
	clk := clocktesting.NewFakeClock(time.Unix(1000, 0).UTC())
	pcs := lifecycleTestPCS()
	pcs.Spec.Replicas = 1
	info := rollingUpdateReplicaInfo(pcs, 0, true)
	pclq := &info.standalonePCLQs[0]
	pclq.Status.LastScheduled = new(metav1.NewTime(clk.Now().Add(-time.Minute)))
	pclq.Status.Conditions = []metav1.Condition{{Type: apiconstants.ConditionTypeMinAvailableBreached, Status: metav1.ConditionTrue, LastTransitionTime: metav1.NewTime(clk.Now())}}
	cl := testutils.SetupFakeClient(pcs, pclq)
	operator := New(cl, record.NewFakeRecorder(10), &pcsexpectations.ScalingProgress{}, &pcsexpectations.CurrentlyUpdating{}, &pcsexpectations.GenerationHashes{}, clk)
	for _, elapsed := range []time.Duration{0, time.Minute - time.Second} {
		clk.Step(elapsed)
		_, err := operator.Sync(t.Context(), logr.Discard(), pcs, &pcssync.State{})
		require.True(t, groveerr.IsContinueReconcileAndRequeue(err), "%v", err)
		require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pclq), &grovecorev1beta1.PodClique{}), "the replica must survive until its termination delay expires")
	}
	clk.Step(time.Second)
	_, err := operator.Sync(t.Context(), logr.Discard(), pcs, &pcssync.State{})
	require.NoError(t, err)
	assert.True(t, apierrors.IsNotFound(cl.Get(t.Context(), client.ObjectKeyFromObject(pclq), &grovecorev1beta1.PodClique{})))
}

func lifecycleTestPCS() *grovecorev1beta1.PodCliqueSet {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "uid").
		WithReplicas(2).WithObservedGeneration(new(int64(1))).WithTerminationDelay(time.Minute).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(3).WithMinAvailable(1).Build()).Build()
	_, schedulingHash, generationHash := computePCSHashes(pcs)
	initializePCSGenerationBaseline(pcs, schedulingHash, generationHash)
	return pcs
}

func lifecycleTestResource(cl client.Client) *_resource {
	return &_resource{clk: clocktesting.NewFakeClock(time.Unix(1000, 0).UTC()), client: cl, eventRecorder: record.NewFakeRecorder(30),
		generationHashExpectations:    &pcsexpectations.GenerationHashes{},
		scalingProgressExpectations:   &pcsexpectations.ScalingProgress{},
		currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
}

type listCountingClient struct {
	client.Client
	listCounts map[string]int
}

func (c *listCountingClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	c.listCounts[reflect.TypeOf(list).Elem().Name()]++
	return c.Client.List(ctx, list, opts...)
}

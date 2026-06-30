// /*
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
// */

package podcliquesetreplica

import (
	"context"
	"fmt"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestNew tests creating a new PodCliqueSetReplica operator
func TestNew(t *testing.T) {
	// Tests creating a new operator instance
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	eventRecorder := &record.FakeRecorder{}

	operator := New(client, eventRecorder)

	assert.NotNil(t, operator)
	resource, ok := operator.(*_resource)
	assert.True(t, ok)
	assert.Equal(t, client, resource.client)
	assert.Equal(t, eventRecorder, resource.eventRecorder)
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

// TestDelete tests the Delete function
func TestDelete(t *testing.T) {
	// Tests that Delete is a no-op
	scheme := runtime.NewScheme()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &_resource{
		client: client,
	}

	err := r.Delete(context.Background(), logr.Discard(), metav1.ObjectMeta{})

	assert.NoError(t, err)
}

// TestSync tests the Sync function
func TestSync(t *testing.T) {
	tests := []struct {
		name string
		// pcs is the PodCliqueSet to sync
		pcs *grovecorev1alpha1.PodCliqueSet
		// existingObjs are the existing objects in the cluster
		existingObjs []runtime.Object
		// expectError indicates if an error is expected
		expectError bool
	}{
		{
			// Tests sync with no replicas
			name: "no_replicas",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 0,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
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
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 2,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
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
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tc.existingObjs...).
				WithStatusSubresource(tc.pcs).
				Build()

			r := &_resource{
				client:        fakeClient,
				eventRecorder: &record.FakeRecorder{},
			}

			err := r.Sync(ctx, logger, tc.pcs)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				// May return a requeue error which is expected
				if err != nil {
					groveErr, ok := err.(*groveerr.GroveError)
					if ok && groveErr.Code == groveerr.ErrCodeContinueReconcileAndRequeue {
						// This is expected
					} else {
						t.Errorf("Unexpected error: %v", err)
					}
				}
			}
		})
	}
}

// TestIsRollingRecreateUpdateInProgress tests the IsRollingRecreateUpdateInProgress function
func TestIsRollingRecreateUpdateInProgress(t *testing.T) {
	tests := []struct {
		name string
		// pcs is the PodCliqueSet to check
		pcs *grovecorev1alpha1.PodCliqueSet
		// expected is the expected result
		expected bool
	}{
		{
			// Tests when rolling update is not in progress
			name: "no_rolling_update",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					UpdateProgress: nil,
				},
			},
			expected: false,
		},
		{
			// Tests when rolling update is in progress
			name: "rolling_update_in_progress",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
						UpdateEndedAt: nil,
					},
				},
			},
			expected: true,
		},
		{
			// Tests when rolling update has ended
			name: "rolling_update_ended",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
						UpdateEndedAt: &metav1.Time{},
					},
				},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := componentutils.IsRollingRecreateUpdateInProgress(tc.pcs)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestComputeCoherentPendingWork(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

	makePCS := func(replicas int32) *grovecorev1alpha1.PodCliqueSet {
		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pcs", Namespace: "default"},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				Replicas: replicas,
				Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
					Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
						{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2, MinAvailable: ptrInt32(1)}},
					},
				},
			},
		}
		generationHash := componentutils.ComputePCSGenerationHash(pcs)
		pcs.Status.CurrentGenerationHash = &generationHash
		return pcs
	}

	// pclqHashesFromPCS returns the canonical (matching) PCS-generation and pod-template hashes
	// for the worker clique so a PCLQ can be marked as fully converged on the desired spec.
	pclqHashesFromPCS := func(pcs *grovecorev1alpha1.PodCliqueSet) (pcsGenerationHash, podTemplateHash string) {
		return componentutils.ComputePCSGenerationHash(pcs),
			componentutils.ComputePCLQPodTemplateHash(pcs.Spec.Template.Cliques[0], pcs.Spec.Template.PriorityClassName)
	}

	makePCLQ := func(name, namespace, pcsName string, replicaIndex int, pcsGenerationHash, podTemplateHash string, updatedReplicas, readyReplicas int32) grovecorev1alpha1.PodClique {
		return grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by":        "grove-operator",
					"app.kubernetes.io/part-of":           pcsName,
					"grove.io/podcliqueset-replica-index": fmt.Sprintf("%d", replicaIndex),
					apicommon.LabelPodTemplateHash:        podTemplateHash,
				},
				OwnerReferences: []metav1.OwnerReference{
					{Kind: "PodCliqueSet", Name: pcsName},
				},
			},
			Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2, MinAvailable: ptrInt32(1)},
			Status: grovecorev1alpha1.PodCliqueStatus{
				CurrentPodCliqueSetGenerationHash: &pcsGenerationHash,
				CurrentPodTemplateHash:            &podTemplateHash,
				UpdatedReplicas:                   updatedReplicas,
				ReadyReplicas:                     readyReplicas,
			},
		}
	}

	// Build PCS instances and look up their canonical hashes once so test cases can use the
	// matching values to mark a PCLQ "done" or stale values to mark it "pending".
	pcs2 := makePCS(2)
	pcs3 := makePCS(3)
	doneGenHash2, doneTemplateHash2 := pclqHashesFromPCS(pcs2)
	doneGenHash3, doneTemplateHash3 := pclqHashesFromPCS(pcs3)

	tests := []struct {
		name                    string
		pcs                     *grovecorev1alpha1.PodCliqueSet
		pclqs                   []grovecorev1alpha1.PodClique
		pcsIndicesToTerminate   []int
		expectedPendingReplicas []int
		expectedDoneReplicas    []int
	}{
		{
			name: "all_replicas_done",
			pcs:  pcs2,
			pclqs: []grovecorev1alpha1.PodClique{
				makePCLQ("test-pcs-0-worker", "default", "test-pcs", 0, doneGenHash2, doneTemplateHash2, 2, 2),
				makePCLQ("test-pcs-1-worker", "default", "test-pcs", 1, doneGenHash2, doneTemplateHash2, 2, 2),
			},
			expectedPendingReplicas: nil,
			expectedDoneReplicas:    []int{0, 1},
		},
		{
			name: "all_replicas_pending",
			pcs:  pcs2,
			pclqs: []grovecorev1alpha1.PodClique{
				makePCLQ("test-pcs-0-worker", "default", "test-pcs", 0, "stale-gen-hash", "stale-template-hash", 2, 2),
				makePCLQ("test-pcs-1-worker", "default", "test-pcs", 1, "stale-gen-hash", "stale-template-hash", 2, 2),
			},
			expectedPendingReplicas: []int{0, 1},
			expectedDoneReplicas:    nil,
		},
		{
			name: "mixed_done_and_pending",
			pcs:  pcs3,
			pclqs: []grovecorev1alpha1.PodClique{
				makePCLQ("test-pcs-0-worker", "default", "test-pcs", 0, doneGenHash3, doneTemplateHash3, 2, 2),
				makePCLQ("test-pcs-1-worker", "default", "test-pcs", 1, "stale-gen-hash", "stale-template-hash", 2, 2),
				makePCLQ("test-pcs-2-worker", "default", "test-pcs", 2, doneGenHash3, doneTemplateHash3, 2, 2),
			},
			expectedPendingReplicas: []int{1},
			expectedDoneReplicas:    []int{0, 2},
		},
		{
			name: "terminating_replica_excluded",
			pcs:  pcs2,
			pclqs: []grovecorev1alpha1.PodClique{
				makePCLQ("test-pcs-0-worker", "default", "test-pcs", 0, "stale-gen-hash", "stale-template-hash", 2, 2),
				makePCLQ("test-pcs-1-worker", "default", "test-pcs", 1, "stale-gen-hash", "stale-template-hash", 2, 2),
			},
			pcsIndicesToTerminate:   []int{1},
			expectedPendingReplicas: []int{0},
			expectedDoneReplicas:    nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			objs := []client.Object{tc.pcs}
			for i := range tc.pclqs {
				objs = append(objs, &tc.pclqs[i])
			}
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
			r := _resource{client: fakeClient}

			work, err := r.computeCoherentPendingWork(context.Background(), tc.pcs, tc.pcsIndicesToTerminate)
			require.NoError(t, err)
			assert.ElementsMatch(t, tc.expectedPendingReplicas, work.pendingReplicaIndices)
			assert.ElementsMatch(t, tc.expectedDoneReplicas, work.doneReplicaIndices)
		})
	}
}

func ptrInt32(v int32) *int32 {
	return &v
}

func ptrString(v string) *string {
	return &v
}

func TestCheckAndAdvanceCoherentUpdate(t *testing.T) {
	makePodGang := func(name, namespace string, available bool) *groveschedulerv1alpha1.PodGang {
		pg := &groveschedulerv1alpha1.PodGang{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		}
		if available {
			pg.Status.Conditions = []metav1.Condition{
				{
					Type:   string(groveschedulerv1alpha1.PodGangConditionTypeReady),
					Status: metav1.ConditionTrue,
				},
			}
		}
		return pg
	}

	tests := []struct {
		name                          string
		inFlightPodGangs              []string
		podGangs                      []*groveschedulerv1alpha1.PodGang
		podGangMap                    *grovecorev1alpha1.PodGangMap
		currentGenerationHash         *string
		replicaDone                   bool
		expectRequeue                 bool
		expectReplicaDone             bool
		expectInFlightPodGangsCleared bool
		expectInFlightPodGangsSet     []string
	}{
		{
			name:                  "empty InFlightPodGangs populates from PodGangMap",
			inFlightPodGangs:      nil,
			currentGenerationHash: ptrString("new-hash"),
			podGangMap: &grovecorev1alpha1.PodGangMap{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pcs-0", Namespace: "default"},
				Spec: grovecorev1alpha1.PodGangMapSpec{
					PodCliqueSetReplicaIndex: 0,
					Entries: []grovecorev1alpha1.PodGangEntry{
						{Name: "pg-old-0", PodCliqueSetGenerationHash: "old-hash", PodCliques: map[string]int32{"worker": 2}},
						{Name: "pg-new-0", PodCliqueSetGenerationHash: "new-hash", PodCliques: map[string]int32{"worker": 2}},
					},
				},
			},
			expectInFlightPodGangsSet: []string{"pg-new-0"},
		},
		{
			name:                  "empty InFlightPodGangs with no PodGangMap requeues",
			inFlightPodGangs:      nil,
			currentGenerationHash: ptrString("new-hash"),
			podGangMap:            nil,
			expectRequeue:         true,
		},
		{
			name:                  "empty InFlightPodGangs with all new-hash entries Available requeues",
			inFlightPodGangs:      nil,
			currentGenerationHash: ptrString("new-hash"),
			podGangMap: &grovecorev1alpha1.PodGangMap{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pcs-0", Namespace: "default"},
				Spec: grovecorev1alpha1.PodGangMapSpec{
					PodCliqueSetReplicaIndex: 0,
					Entries: []grovecorev1alpha1.PodGangEntry{
						{Name: "pg-new-0", PodCliqueSetGenerationHash: "new-hash", PodCliques: map[string]int32{"worker": 2}},
					},
				},
			},
			podGangs: []*groveschedulerv1alpha1.PodGang{
				makePodGang("pg-new-0", "default", true),
			},
			expectRequeue: true,
		},
		{
			name:             "not all PodGangs Available requeues",
			inFlightPodGangs: []string{"pg-0", "pg-1"},
			podGangs: []*groveschedulerv1alpha1.PodGang{
				makePodGang("pg-0", "default", true),
				makePodGang("pg-1", "default", false),
			},
			expectRequeue: true,
		},
		{
			name:             "all Available and replica done marks done",
			inFlightPodGangs: []string{"pg-0"},
			podGangs: []*groveschedulerv1alpha1.PodGang{
				makePodGang("pg-0", "default", true),
			},
			replicaDone:       true,
			expectReplicaDone: true,
		},
		{
			// Regression: prior to the early-exit reorder, an empty InFlightPodGangs reconcile
			// after the replica was already done would call populateInFlightPodGangs and loop
			// on "no new in-flight PodGangs found, requeueing" forever. The early-exit at the
			// top of checkAndAdvanceCoherentUpdate now marks done immediately.
			name:                  "empty InFlightPodGangs but replica already done marks done without populate",
			inFlightPodGangs:      nil,
			currentGenerationHash: ptrString("new-hash"),
			replicaDone:           true,
			expectReplicaDone:     true,
		},
		{
			name:             "all Available but replica not done clears InFlightPodGangs and requeues",
			inFlightPodGangs: []string{"pg-0"},
			podGangs: []*groveschedulerv1alpha1.PodGang{
				makePodGang("pg-0", "default", true),
			},
			replicaDone:                   false,
			expectRequeue:                 true,
			expectInFlightPodGangsCleared: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pcs", Namespace: "default"},
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					CurrentGenerationHash: tc.currentGenerationHash,
					UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
						UpdateStartedAt: metav1.Now(),
						CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{
							{
								ReplicaIndex:     0,
								UpdateStartedAt:  metav1.Now(),
								InFlightPodGangs: tc.inFlightPodGangs,
							},
						},
					},
				},
			}

			objs := []client.Object{pcs}
			for _, pg := range tc.podGangs {
				objs = append(objs, pg)
			}
			if tc.podGangMap != nil {
				objs = append(objs, tc.podGangMap)
			}
			cl := testutils.NewTestClientBuilder().
				WithObjects(objs...).
				WithStatusSubresource(pcs).
				Build()
			r := _resource{client: cl}

			updateWork := &coherentPendingWork{}
			if tc.replicaDone {
				updateWork.doneReplicaIndices = []int{0}
			} else {
				updateWork.pendingReplicaIndices = []int{0}
			}

			replicaDone, err := r.checkAndAdvanceCoherentUpdate(context.Background(), logr.Discard(), pcs, updateWork)

			if tc.expectRequeue {
				require.Error(t, err)
				testutils.CheckGroveError(t, &groveerr.GroveError{Code: groveerr.ErrCodeContinueReconcileAndRequeue, Operation: component.OperationSync}, err)
				assert.False(t, replicaDone)
				if tc.expectInFlightPodGangsCleared {
					// "iteration complete, seeking next" path: side-effect plus requeue.
					assert.Nil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].InFlightPodGangs)
					assert.Nil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].UpdateEndedAt)
				}
			} else if tc.expectReplicaDone {
				require.NoError(t, err)
				assert.True(t, replicaDone)
				assert.NotNil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].UpdateEndedAt)
				assert.Nil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].InFlightPodGangs)
			} else if tc.expectInFlightPodGangsSet != nil {
				require.NoError(t, err)
				assert.False(t, replicaDone)
				assert.ElementsMatch(t, tc.expectInFlightPodGangsSet, pcs.Status.UpdateProgress.CurrentlyUpdating[0].InFlightPodGangs)
			}
		})
	}
}

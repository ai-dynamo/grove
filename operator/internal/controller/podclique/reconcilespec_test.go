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

package podclique

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestShouldCheckPendingUpdatesForPCLQ verifies that standalone PCLQs can
// evaluate their own update state when the owning PCS has a current generation,
// even before a PCS replica is actively selected for rolling update.
func TestShouldCheckPendingUpdatesForPCLQ(t *testing.T) {
	replicas := int32(1)
	minAvailable := int32(1)
	tests := []struct {
		// Test case description
		name                string
		pcsUpdateProgress   *grovecorev1beta1.PodCliqueSetUpdateProgress
		pclqPCSReplicaIndex int32
		pcsgOwned           bool
		// expected is the expected result
		expected bool
	}{
		{
			name:                "standalone_without_pcs_update_progress",
			pclqPCSReplicaIndex: 0,
			expected:            true,
		},
		{
			name: "standalone_matching_active_pcs_replica",
			pcsUpdateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}},
			},
			pclqPCSReplicaIndex: 0,
			expected:            true,
		},
		{
			name:                "standalone_active_pcs_update_without_selected_replica",
			pcsUpdateProgress:   &grovecorev1beta1.PodCliqueSetUpdateProgress{},
			pclqPCSReplicaIndex: 0,
			expected:            false,
		},
		{
			name: "standalone_nonmatching_active_pcs_replica",
			pcsUpdateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 1}},
			},
			pclqPCSReplicaIndex: 0,
			expected:            false,
		},
		{
			name:                "pcsg_owned_pclq",
			pclqPCSReplicaIndex: 0,
			pcsgOwned:           true,
			expected:            false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcsUID := uuid.NewUUID()
			pcsBuilder := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
				WithReplicas(2).
				WithPodCliqueParameters("worker", 1, nil).
				WithPodCliqueSetGenerationHash(ptr.To("current-hash"))
			if tc.pcsUpdateProgress != nil {
				pcsBuilder.WithUpdateProgress(tc.pcsUpdateProgress)
			}
			if tc.pcsgOwned {
				pcsBuilder.WithPodCliqueScalingGroupConfig(grovecorev1beta1.PodCliqueScalingGroupConfig{
					Name:         "group",
					CliqueNames:  []string{"worker"},
					Replicas:     &replicas,
					MinAvailable: &minAvailable,
				})
			}
			pcs := pcsBuilder.Build()
			pclq := testutils.NewPodCliqueBuilder(testPCSName, pcsUID, "worker", testNamespace, tc.pclqPCSReplicaIndex).Build()
			if tc.pcsgOwned {
				pclq = testutils.NewPCSGPodCliqueBuilder(testPCSName+"-0-worker-0", testNamespace, testPCSName, "group", 0, 0).Build()
			}

			result, err := shouldCheckPendingUpdatesForPCLQ(logr.Discard(), pcs, pclq)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestGetOrderedKindsForSync tests the getOrderedKindsForSync function
func TestGetOrderedKindsForSync(t *testing.T) {
	kinds := getOrderedKindsForSync()
	assert.Equal(t, 2, len(kinds))
	assert.Equal(t, component.KindResourceClaim, kinds[0], "ResourceClaim must be synced before Pod")
	assert.Equal(t, component.KindPod, kinds[1])
}

// TestShouldResetOrTriggerUpdate tests the shouldResetOrTriggerUpdate function for PodClique. The PodClique's own
// revision and pod template hash labels are the update target.
func TestShouldResetOrTriggerUpdate(t *testing.T) {
	newPCLQ := func(labels map[string]string, status grovecorev1beta1.PodCliqueStatus) *grovecorev1beta1.PodClique {
		return &grovecorev1beta1.PodClique{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Status: status}
	}
	target := map[string]string{apicommon.LabelPodCliqueSetRevision: "revision-2", apicommon.LabelPodTemplateHash: "hash-2"}
	tests := []struct {
		name     string
		pclq     *grovecorev1beta1.PodClique
		expected bool
	}{
		{
			name: "waits_for_the_revision_label",
			pclq: newPCLQ(map[string]string{apicommon.LabelPodTemplateHash: "hash-2"},
				grovecorev1beta1.PodCliqueStatus{CurrentPodTemplateHash: ptr.To("hash-1")}),
			expected: false,
		},
		{
			name:     "first_ever_update_required",
			pclq:     newPCLQ(target, grovecorev1beta1.PodCliqueStatus{CurrentPodTemplateHash: ptr.To("hash-1")}),
			expected: true,
		},
		{
			name:     "bootstrap_is_not_an_update",
			pclq:     newPCLQ(target, grovecorev1beta1.PodCliqueStatus{}),
			expected: false,
		},
		{
			name: "new_revision_with_the_same_pod_template_is_not_an_update",
			pclq: newPCLQ(target, grovecorev1beta1.PodCliqueStatus{
				CurrentPodTemplateHash: ptr.To("hash-2"), CurrentPodCliqueSetRevision: "revision-1",
			}),
			expected: false,
		},
		{
			name: "in_progress_update_not_stale",
			pclq: newPCLQ(target, grovecorev1beta1.PodCliqueStatus{UpdateProgress: &grovecorev1beta1.PodCliqueUpdateProgress{
				PodCliqueSetRevision: "revision-2", PodTemplateHash: "hash-2", UpdateStartedAt: metav1.Now(),
			}}),
			expected: false,
		},
		{
			name: "in_progress_update_stale_revision",
			pclq: newPCLQ(target, grovecorev1beta1.PodCliqueStatus{UpdateProgress: &grovecorev1beta1.PodCliqueUpdateProgress{
				PodCliqueSetRevision: "revision-1", PodTemplateHash: "hash-2", UpdateStartedAt: metav1.Now(),
			}}),
			expected: true,
		},
		{
			name: "in_progress_update_stale_pod_template_hash",
			pclq: newPCLQ(target, grovecorev1beta1.PodCliqueStatus{UpdateProgress: &grovecorev1beta1.PodCliqueUpdateProgress{
				PodCliqueSetRevision: "revision-2", PodTemplateHash: "hash-1", UpdateStartedAt: metav1.Now(),
			}}),
			expected: true,
		},
		{
			name: "in_progress_update_from_an_older_operator_is_reset",
			pclq: newPCLQ(target, grovecorev1beta1.PodCliqueStatus{UpdateProgress: &grovecorev1beta1.PodCliqueUpdateProgress{
				PodCliqueSetGenerationHash: "generation-2", PodTemplateHash: "hash-2", UpdateStartedAt: metav1.Now(),
			}}),
			expected: true,
		},
		{
			name: "last_completed_update_with_the_same_pod_template_hash",
			pclq: newPCLQ(target, grovecorev1beta1.PodCliqueStatus{
				CurrentPodTemplateHash: ptr.To("hash-2"),
				UpdateProgress: &grovecorev1beta1.PodCliqueUpdateProgress{
					PodCliqueSetRevision: "revision-1", PodTemplateHash: "hash-2", UpdateStartedAt: metav1.Now(), UpdateEndedAt: ptr.To(metav1.Now()),
				},
			}),
			expected: false,
		},
		{
			// An OnDelete update ends before its Pods are replaced. A new revision must restart it, so the
			// Pods roll if the strategy changed to RollingRecreate.
			name: "new_revision_restarts_an_ended_update_whose_pods_did_not_converge",
			pclq: newPCLQ(target, grovecorev1beta1.PodCliqueStatus{
				CurrentPodTemplateHash: ptr.To("hash-1"),
				UpdateProgress: &grovecorev1beta1.PodCliqueUpdateProgress{
					PodCliqueSetRevision: "revision-1", PodTemplateHash: "hash-2", UpdateStartedAt: metav1.Now(), UpdateEndedAt: ptr.To(metav1.Now()),
				},
			}),
			expected: true,
		},
		{
			name: "ended_update_of_the_same_revision_whose_pods_did_not_converge",
			pclq: newPCLQ(target, grovecorev1beta1.PodCliqueStatus{
				CurrentPodTemplateHash: ptr.To("hash-1"),
				UpdateProgress: &grovecorev1beta1.PodCliqueUpdateProgress{
					PodCliqueSetRevision: "revision-2", PodTemplateHash: "hash-2", UpdateStartedAt: metav1.Now(), UpdateEndedAt: ptr.To(metav1.Now()),
				},
			}),
			expected: false,
		},
		{
			name: "last_completed_update_with_another_pod_template_hash",
			pclq: newPCLQ(target, grovecorev1beta1.PodCliqueStatus{UpdateProgress: &grovecorev1beta1.PodCliqueUpdateProgress{
				PodCliqueSetRevision: "revision-1", PodTemplateHash: "hash-1", UpdateStartedAt: metav1.Now(), UpdateEndedAt: ptr.To(metav1.Now()),
			}}),
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := shouldResetOrTriggerUpdate(tc.pclq)
			assert.Equal(t, tc.expected, result)
		})
	}
}

const (
	testNamespace = "test-namespace"
	testPCSName   = "test-pcs"
)

func TestProcessUpdateInitializesProgressWithoutActivePCSUpdate(t *testing.T) {
	pcsUID := uuid.NewUUID()
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
		WithReplicas(1).
		WithPodCliqueParameters("worker", 1, nil).
		WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{
			Type: grovecorev1beta1.RollingRecreateStrategy,
		}).
		WithPodCliqueSetGenerationHash(ptr.To("new-generation-hash")).
		WithUpdateRevision("revision-2").
		Build()
	pclq := testutils.NewPodCliqueBuilder(testPCSName, pcsUID, "worker", testNamespace, 0).
		WithLabels(map[string]string{apicommon.LabelPodCliqueSetRevision: "revision-2", apicommon.LabelPodTemplateHash: "new-template-hash"}).
		Build()
	pclq.Status = grovecorev1beta1.PodCliqueStatus{
		CurrentPodCliqueSetGenerationHash: ptr.To("old-generation-hash"),
		CurrentPodCliqueSetRevision:       "revision-1",
		CurrentPodTemplateHash:            ptr.To("old-template-hash"),
		UpdatedReplicas:                   1,
	}

	fakeClient := testutils.SetupFakeClient(pcs, pclq)
	reconciler := &Reconciler{client: fakeClient}

	result := reconciler.processUpdate(context.Background(), logr.Discard(), pclq)

	ctrlResult, err := result.Result()
	require.NoError(t, err)
	assert.Equal(t, updateProgressCacheSyncRetryInterval, ctrlResult.RequeueAfter)
	updatedPCLQ := &grovecorev1beta1.PodClique{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pclq), updatedPCLQ))
	require.NotNil(t, updatedPCLQ.Status.UpdateProgress)
	assert.Nil(t, updatedPCLQ.Status.UpdateProgress.UpdateEndedAt)
	assert.Equal(t, "new-generation-hash", updatedPCLQ.Status.UpdateProgress.PodCliqueSetGenerationHash)
	assert.Equal(t, "revision-2", updatedPCLQ.Status.UpdateProgress.PodCliqueSetRevision)
	assert.Equal(t, "new-template-hash", updatedPCLQ.Status.UpdateProgress.PodTemplateHash)
	assert.Equal(t, int32(0), updatedPCLQ.Status.UpdatedReplicas)
}

// TestProcessUpdateSkipsRevisionWithoutPodTemplateChange verifies that a new revision that keeps the PodClique's
// pod template hash starts no update, so no Pod is replaced.
func TestProcessUpdateSkipsRevisionWithoutPodTemplateChange(t *testing.T) {
	pcsUID := uuid.NewUUID()
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
		WithReplicas(1).
		WithPodCliqueParameters("worker", 1, nil).
		WithUpdateRevision("revision-2").
		Build()
	pclq := testutils.NewPodCliqueBuilder(testPCSName, pcsUID, "worker", testNamespace, 0).
		WithLabels(map[string]string{apicommon.LabelPodCliqueSetRevision: "revision-2", apicommon.LabelPodTemplateHash: "hash-from-older-build"}).
		Build()
	pclq.Status = grovecorev1beta1.PodCliqueStatus{
		CurrentPodCliqueSetRevision: "revision-1",
		CurrentPodTemplateHash:      ptr.To("hash-from-older-build"),
		UpdatedReplicas:             1,
		Replicas:                    1,
	}
	fakeClient := testutils.SetupFakeClient(pcs, pclq)
	reconciler := &Reconciler{client: fakeClient}

	result := reconciler.processUpdate(context.Background(), logr.Discard(), pclq)

	require.False(t, result.HasErrors())
	assert.False(t, result.NeedsRequeue())
	updatedPCLQ := &grovecorev1beta1.PodClique{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pclq), updatedPCLQ))
	assert.Nil(t, updatedPCLQ.Status.UpdateProgress)
	assert.Equal(t, int32(1), updatedPCLQ.Status.UpdatedReplicas)
}

// TestProcessUpdateResetsPCLQProgressToSelectedPCSGeneration verifies that processUpdate replaces
// progress for an older PCS revision after the PCS selects this standalone PCLQ's replica.
//
// The PCLQ previously completed revision generation-1, then started generation-2. Before generation-2
// completed, the PCS changed back to generation-1 and selected this replica. processUpdate must
// replace the active generation-2 progress with a new generation-1 update.
func TestProcessUpdateResetsPCLQProgressToSelectedPCSGeneration(t *testing.T) {
	const (
		currentGeneration    = "generation-1"
		supersededGeneration = "generation-2"
	)

	pcsUID := uuid.NewUUID()
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
		WithReplicas(1).
		WithPodCliqueParameters("worker", 1, nil).
		WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.RollingRecreateStrategy}).
		WithPodCliqueSetGenerationHash(ptr.To(currentGeneration)).
		WithUpdateRevision(currentGeneration).
		WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
			UpdateStartedAt: metav1.Now(),
			CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				{
					ReplicaIndex:    0,
					UpdateStartedAt: metav1.Now(),
				},
			},
		}).
		Build()

	pclq := testutils.NewPodCliqueBuilder(testPCSName, pcsUID, "worker", testNamespace, 0).
		WithLabels(map[string]string{apicommon.LabelPodCliqueSetRevision: currentGeneration, apicommon.LabelPodTemplateHash: "generation-1-template"}).
		Build()
	pclq.Status.CurrentPodCliqueSetRevision = currentGeneration
	pclq.Status.CurrentPodTemplateHash = ptr.To("generation-1-template")
	pclq.Status.UpdateProgress = &grovecorev1beta1.PodCliqueUpdateProgress{
		UpdateStartedAt:      metav1.Now(),
		PodCliqueSetRevision: supersededGeneration,
		PodTemplateHash:      "generation-2-template",
	}
	pclq.Status.UpdatedReplicas = 1

	fakeClient := testutils.SetupFakeClient(pcs, pclq)
	reconciler := &Reconciler{client: fakeClient}

	result := reconciler.processUpdate(t.Context(), logr.Discard(), pclq)

	require.False(t, result.HasErrors())
	ctrlResult, err := result.Result()
	require.NoError(t, err)
	assert.Equal(t, updateProgressCacheSyncRetryInterval, ctrlResult.RequeueAfter)
	updatedPCLQ := &grovecorev1beta1.PodClique{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pclq), updatedPCLQ))
	require.NotNil(t, updatedPCLQ.Status.UpdateProgress)
	assert.Equal(t, currentGeneration, updatedPCLQ.Status.UpdateProgress.PodCliqueSetGenerationHash)
	assert.Equal(t, currentGeneration, updatedPCLQ.Status.UpdateProgress.PodCliqueSetRevision)
	assert.Equal(t, "generation-1-template", updatedPCLQ.Status.UpdateProgress.PodTemplateHash)
	assert.Nil(t, updatedPCLQ.Status.UpdateProgress.UpdateEndedAt)
	assert.Zero(t, updatedPCLQ.Status.UpdatedReplicas)
}

// TestInitOrResetUpdate tests the initOrResetUpdate function for both OnDelete and RollingRecreate strategies
func TestInitOrResetUpdate(t *testing.T) {
	targetLabels := map[string]string{apicommon.LabelPodCliqueSetRevision: "target-revision", apicommon.LabelPodTemplateHash: "target-hash"}
	tests := []struct {
		name                   string
		setupPCS               func(pcsUID types.UID) *grovecorev1beta1.PodCliqueSet
		setupPCLQ              func(pcsUID types.UID) *grovecorev1beta1.PodClique
		expectUpdateEndedAtSet bool
	}{
		{
			name: "rolling_recreate_strategy_does_not_set_update_ended_at",
			setupPCS: func(pcsUID types.UID) *grovecorev1beta1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(2).
					WithPodCliqueParameters("worker", 1, nil).
					WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1beta1.RollingRecreateStrategy,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("current-hash")).
					Build()
			},
			setupPCLQ: func(pcsUID types.UID) *grovecorev1beta1.PodClique {
				pclq := testutils.NewPodCliqueBuilder(testPCSName, pcsUID, "worker", testNamespace, 0).WithLabels(targetLabels).Build()
				pclq.Status.UpdatedReplicas = 3 // should be reset to 0
				return pclq
			},
			expectUpdateEndedAtSet: false,
		},
		{
			name: "nil_update_strategy_does_not_set_update_ended_at",
			setupPCS: func(pcsUID types.UID) *grovecorev1beta1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(2).
					WithPodCliqueParameters("worker", 1, nil).
					WithPodCliqueSetGenerationHash(ptr.To("current-hash")).
					Build()
			},
			setupPCLQ: func(pcsUID types.UID) *grovecorev1beta1.PodClique {
				pclq := testutils.NewPodCliqueBuilder(testPCSName, pcsUID, "worker", testNamespace, 0).WithLabels(targetLabels).Build()
				pclq.Status.UpdatedReplicas = 2 // should be reset to 0
				return pclq
			},
			expectUpdateEndedAtSet: false,
		},
		{
			name: "on_delete_strategy_sets_update_ended_at_immediately",
			setupPCS: func(pcsUID types.UID) *grovecorev1beta1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(2).
					WithPodCliqueParameters("worker", 1, nil).
					WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1beta1.OnDeleteStrategy,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("current-hash")).
					Build()
			},
			setupPCLQ: func(pcsUID types.UID) *grovecorev1beta1.PodClique {
				pclq := testutils.NewPodCliqueBuilder(testPCSName, pcsUID, "worker", testNamespace, 0).WithLabels(targetLabels).Build()
				pclq.Status.UpdatedReplicas = 5 // should be reset to 0
				return pclq
			},
			expectUpdateEndedAtSet: true,
		},
		{
			name: "existing_rollingrecreate_in_progress_is_reset_when_hash_changes",
			setupPCS: func(pcsUID types.UID) *grovecorev1beta1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(3).
					WithPodCliqueParameters("worker", 1, nil).
					WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1beta1.RollingRecreateStrategy,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("new-hash")).
					Build()
			},
			setupPCLQ: func(pcsUID types.UID) *grovecorev1beta1.PodClique {
				updateStartedAt := metav1.Now()

				pclq := testutils.NewPodCliqueBuilder(testPCSName, pcsUID, "worker", testNamespace, 0).WithLabels(targetLabels).Build()
				// Simulate an existing update in progress
				pclq.Status.UpdateProgress = &grovecorev1beta1.PodCliqueUpdateProgress{
					UpdateStartedAt:            updateStartedAt,
					PodCliqueSetGenerationHash: "old-hash",
					PodTemplateHash:            "old-pod-template-hash",
				}
				pclq.Status.UpdatedReplicas = 2
				return pclq
			},
			expectUpdateEndedAtSet: false,
		},
		{
			name: "existing_on_delete_update_is_reset_when_hash_changes",
			setupPCS: func(pcsUID types.UID) *grovecorev1beta1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, pcsUID).
					WithReplicas(3).
					WithPodCliqueParameters("worker", 1, nil).
					WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1beta1.OnDeleteStrategy,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("new-hash")).
					Build()
			},
			setupPCLQ: func(pcsUID types.UID) *grovecorev1beta1.PodClique {
				updateStartedAt := metav1.Now()
				updateEndedAt := metav1.Now()

				pclq := testutils.NewPodCliqueBuilder(testPCSName, pcsUID, "worker", testNamespace, 0).WithLabels(targetLabels).Build()
				// Simulate an existing update that was completed
				pclq.Status.UpdateProgress = &grovecorev1beta1.PodCliqueUpdateProgress{
					UpdateStartedAt:            updateStartedAt,
					UpdateEndedAt:              ptr.To(updateEndedAt),
					PodCliqueSetGenerationHash: "old-hash",
					PodTemplateHash:            "old-pod-template-hash",
				}
				pclq.Status.UpdatedReplicas = 1
				return pclq
			},
			expectUpdateEndedAtSet: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcsUID := uuid.NewUUID()
			pcs := tt.setupPCS(pcsUID)
			pclq := tt.setupPCLQ(pcsUID)

			fakeClient := testutils.SetupFakeClient(pcs, pclq)
			reconciler := &Reconciler{client: fakeClient}

			err := reconciler.initOrResetUpdate(context.Background(), pcs, pclq)
			require.NoError(t, err, "initOrResetUpdate should not return errors")

			// Fetch the updated PCLQ from the fake client
			updatedPCLQ := &grovecorev1beta1.PodClique{}
			err = fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pclq), updatedPCLQ)
			require.NoError(t, err)

			// Verify UpdateProgress is set
			require.NotNil(t, updatedPCLQ.Status.UpdateProgress, "UpdateProgress should be set")
			assert.NotEmpty(t, updatedPCLQ.Status.UpdateProgress.UpdateStartedAt, "UpdateStartedAt should be set")

			// Verify UpdateEndedAt based on strategy
			if tt.expectUpdateEndedAtSet {
				require.NotNil(t, updatedPCLQ.Status.UpdateProgress.UpdateEndedAt, "UpdateEndedAt should be set for OnDelete strategy")
			} else {
				assert.Nil(t, updatedPCLQ.Status.UpdateProgress.UpdateEndedAt, "UpdateEndedAt should be nil for non-OnDelete strategy")
			}

			assert.Equal(t, *pcs.Status.CurrentGenerationHash, updatedPCLQ.Status.UpdateProgress.PodCliqueSetGenerationHash, "PodCliqueSetGenerationHash should match PCS CurrentGenerationHash")
			assert.Equal(t, "target-revision", updatedPCLQ.Status.UpdateProgress.PodCliqueSetRevision, "PodCliqueSetRevision should match the revision label")
			assert.Equal(t, "target-hash", updatedPCLQ.Status.UpdateProgress.PodTemplateHash, "PodTemplateHash should match the pod template hash label")
			assert.Equal(t, int32(0), updatedPCLQ.Status.UpdatedReplicas, "UpdatedReplicas should be reset to 0")
		})
	}
}

// TestUpdateObservedGeneration tests the updateObservedGeneration function for PodClique
func TestUpdateObservedGeneration(t *testing.T) {
	tests := []struct {
		name               string
		setupPCLQ          func() *grovecorev1beta1.PodClique
		expectPatchSkipped bool
		expectedGeneration int64
	}{
		{
			name: "patch_skipped_when_observed_equals_generation",
			setupPCLQ: func() *grovecorev1beta1.PodClique {
				pclq := testutils.NewPodCliqueBuilder(testPCSName, uuid.NewUUID(), "worker", testNamespace, 0).Build()
				pclq.Generation = 5
				pclq.Status.ObservedGeneration = ptr.To(int64(5))
				return pclq
			},
			expectPatchSkipped: true,
			expectedGeneration: 5,
		},
		{
			name: "patch_made_when_observed_is_nil",
			setupPCLQ: func() *grovecorev1beta1.PodClique {
				pclq := testutils.NewPodCliqueBuilder(testPCSName, uuid.NewUUID(), "worker", testNamespace, 0).Build()
				pclq.Generation = 3
				pclq.Status.ObservedGeneration = nil
				return pclq
			},
			expectPatchSkipped: false,
			expectedGeneration: 3,
		},
		{
			name: "patch_made_when_observed_differs_from_generation",
			setupPCLQ: func() *grovecorev1beta1.PodClique {
				pclq := testutils.NewPodCliqueBuilder(testPCSName, uuid.NewUUID(), "worker", testNamespace, 0).Build()
				pclq.Generation = 7
				pclq.Status.ObservedGeneration = ptr.To(int64(4))
				return pclq
			},
			expectPatchSkipped: false,
			expectedGeneration: 7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pclq := tt.setupPCLQ()
			originalObservedGen := pclq.Status.ObservedGeneration

			fakeClient := testutils.SetupFakeClient(pclq)
			reconciler := &Reconciler{client: fakeClient}

			result := reconciler.updateObservedGeneration(context.Background(), logr.Discard(), pclq)

			require.False(t, result.HasErrors(), "updateObservedGeneration should not return errors")

			// Verify the result continues reconciliation
			_, err := result.Result()
			assert.NoError(t, err)

			// Fetch the updated object from the fake client
			updatedPCLQ := &grovecorev1beta1.PodClique{}
			err = fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pclq), updatedPCLQ)
			require.NoError(t, err)

			// Verify ObservedGeneration is set correctly
			require.NotNil(t, updatedPCLQ.Status.ObservedGeneration, "ObservedGeneration should not be nil after update")
			assert.Equal(t, tt.expectedGeneration, *updatedPCLQ.Status.ObservedGeneration)

			if tt.expectPatchSkipped {
				// If patch was skipped, the ObservedGeneration should remain unchanged
				assert.Equal(t, originalObservedGen, updatedPCLQ.Status.ObservedGeneration)
			}
		})
	}
}

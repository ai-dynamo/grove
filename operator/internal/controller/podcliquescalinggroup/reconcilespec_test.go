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

package podcliquescalinggroup

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

// Test constants
const (
	testNamespacePCSG = "test-namespace"
	testPCSGName      = "test-pcsg"
	testPCSNamePCSG   = "test-pcs"
)

// TestUpdateObservedGeneration tests the updateObservedGeneration function for PodCliqueScalingGroup
func TestUpdateObservedGeneration(t *testing.T) {
	tests := []struct {
		name               string
		setupPCSG          func() *grovecorev1beta1.PodCliqueScalingGroup
		expectPatchSkipped bool
		expectedGeneration int64
	}{
		{
			name: "patch_skipped_when_observed_equals_generation",
			setupPCSG: func() *grovecorev1beta1.PodCliqueScalingGroup {
				pcsg := testutils.NewPodCliqueScalingGroupBuilder(testPCSGName, testNamespacePCSG, testPCSNamePCSG, 0).
					WithReplicas(1).
					Build()
				pcsg.Generation = 5
				pcsg.Status.ObservedGeneration = ptr.To(int64(5))
				return pcsg
			},
			expectPatchSkipped: true,
			expectedGeneration: 5,
		},
		{
			name: "patch_made_when_observed_is_nil",
			setupPCSG: func() *grovecorev1beta1.PodCliqueScalingGroup {
				pcsg := testutils.NewPodCliqueScalingGroupBuilder(testPCSGName, testNamespacePCSG, testPCSNamePCSG, 0).
					WithReplicas(1).
					Build()
				pcsg.Generation = 3
				pcsg.Status.ObservedGeneration = nil
				return pcsg
			},
			expectPatchSkipped: false,
			expectedGeneration: 3,
		},
		{
			name: "patch_made_when_observed_differs_from_generation",
			setupPCSG: func() *grovecorev1beta1.PodCliqueScalingGroup {
				pcsg := testutils.NewPodCliqueScalingGroupBuilder(testPCSGName, testNamespacePCSG, testPCSNamePCSG, 0).
					WithReplicas(1).
					Build()
				pcsg.Generation = 7
				pcsg.Status.ObservedGeneration = ptr.To(int64(4))
				return pcsg
			},
			expectPatchSkipped: false,
			expectedGeneration: 7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcsg := tt.setupPCSG()
			originalObservedGen := pcsg.Status.ObservedGeneration

			fakeClient := testutils.SetupFakeClient(pcsg)
			reconciler := &Reconciler{client: fakeClient}

			result := reconciler.updateObservedGeneration(context.Background(), logr.Discard(), pcsg)

			require.False(t, result.HasErrors(), "updateObservedGeneration should not return errors")

			// Verify the result continues reconciliation
			_, err := result.Result()
			assert.NoError(t, err)

			// Fetch the updated object from the fake client
			updatedPCSG := &grovecorev1beta1.PodCliqueScalingGroup{}
			err = fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcsg), updatedPCSG)
			require.NoError(t, err)

			// Verify ObservedGeneration is set correctly
			require.NotNil(t, updatedPCSG.Status.ObservedGeneration, "ObservedGeneration should not be nil after update")
			assert.Equal(t, tt.expectedGeneration, *updatedPCSG.Status.ObservedGeneration)

			if tt.expectPatchSkipped {
				// If patch was skipped, the ObservedGeneration should remain unchanged
				assert.Equal(t, originalObservedGen, updatedPCSG.Status.ObservedGeneration)
			}
		})
	}
}

// TestGetOrderedKindsForSyncPCSG tests the getOrderedKindsForSync function for PodCliqueScalingGroup
func TestGetOrderedKindsForSyncPCSG(t *testing.T) {
	kinds := getOrderedKindsForSync()

	expectedKinds := []component.Kind{
		component.KindPodClique,
	}

	assert.Equal(t, len(expectedKinds), len(kinds))
	for i, expected := range expectedKinds {
		assert.Equal(t, expected, kinds[i])
	}
}

func TestProcessUpdateStartsForSelectedPCSReplica(t *testing.T) {
	pcsUID := uuid.NewUUID()
	pcs := testutils.NewPodCliqueSetBuilder(testPCSNamePCSG, testNamespacePCSG, pcsUID).
		WithReplicas(1).
		WithScalingGroup(testPCSGName, []string{"worker"}).
		WithPodCliqueSetGenerationHash(ptr.To("new-generation-hash")).
		Build()
	pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
		CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}},
	}
	pcsg := testutils.NewPodCliqueScalingGroupBuilder(testPCSGName, testNamespacePCSG, testPCSNamePCSG, 0).
		WithLabels(map[string]string{apicommon.LabelPodCliqueSetRevision: "revision-2"}).Build()

	reconciler := &Reconciler{client: testutils.SetupFakeClient(pcs, pcsg)}
	result := reconciler.processUpdate(context.Background(), logr.Discard(), pcsg)
	_, err := result.Result()
	require.NoError(t, err)
	require.NotNil(t, pcsg.Status.UpdateProgress)
	assert.Equal(t, "new-generation-hash", pcsg.Status.UpdateProgress.PodCliqueSetGenerationHash)
	assert.Equal(t, "revision-2", pcsg.Status.UpdateProgress.PodCliqueSetRevision)
}

// TestProcessUpdateWaitsForRevisionLabel verifies that a PodCliqueScalingGroup the PodCliqueSet has not yet written
// a revision to starts no update.
func TestProcessUpdateWaitsForRevisionLabel(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSNamePCSG, testNamespacePCSG, uuid.NewUUID()).
		WithReplicas(1).
		WithScalingGroup(testPCSGName, []string{"worker"}).
		WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.OnDeleteStrategy}).
		Build()
	pcsg := testutils.NewPodCliqueScalingGroupBuilder(testPCSGName, testNamespacePCSG, testPCSNamePCSG, 0).Build()

	reconciler := &Reconciler{client: testutils.SetupFakeClient(pcs, pcsg)}
	result := reconciler.processUpdate(context.Background(), logr.Discard(), pcsg)

	require.False(t, result.HasErrors())
	assert.Nil(t, pcsg.Status.UpdateProgress)
}

// TestShouldResetOrTriggerUpdatePCSG tests the shouldResetOrTriggerUpdate function for PodCliqueScalingGroup. The
// revision label the PodCliqueSet writes on the PodCliqueScalingGroup is the update target.
func TestShouldResetOrTriggerUpdatePCSG(t *testing.T) {
	withTarget := func(progress *grovecorev1beta1.PodCliqueScalingGroupUpdateProgress) *grovecorev1beta1.PodCliqueScalingGroup {
		return &grovecorev1beta1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{apicommon.LabelPodCliqueSetRevision: "current-revision"}},
			Status:     grovecorev1beta1.PodCliqueScalingGroupStatus{UpdateProgress: progress},
		}
	}
	tests := []struct {
		name     string
		pcsg     *grovecorev1beta1.PodCliqueScalingGroup
		expected bool
	}{
		{
			name:     "should_not_trigger_update_until_the_revision_label_exists",
			pcsg:     &grovecorev1beta1.PodCliqueScalingGroup{},
			expected: false,
		},
		{
			name:     "should_trigger_update_when_no_update_progress",
			pcsg:     withTarget(nil),
			expected: true,
		},
		{
			name:     "should_not_trigger_update_when_revision_matches",
			pcsg:     withTarget(&grovecorev1beta1.PodCliqueScalingGroupUpdateProgress{PodCliqueSetRevision: "current-revision"}),
			expected: false,
		},
		{
			name:     "should_trigger_update_when_revision_differs",
			pcsg:     withTarget(&grovecorev1beta1.PodCliqueScalingGroupUpdateProgress{PodCliqueSetRevision: "old-revision"}),
			expected: true,
		},
		{
			name:     "should_trigger_update_for_progress_from_an_older_operator",
			pcsg:     withTarget(&grovecorev1beta1.PodCliqueScalingGroupUpdateProgress{PodCliqueSetGenerationHash: "current-hash"}),
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := shouldResetOrTriggerUpdate(tc.pcsg)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestProcessUpdateResetsStaleProgressForSelectedPCSReplica verifies the rollback path where a PCSG's
// last fully realized revision already matches its target, but its active update still targets a
// superseded revision. Once the PCS selects this replica again, processUpdate must reset the PCSG target
// to the current revision so the child update can finish or revert partially updated PodCliques.
func TestProcessUpdateResetsStaleProgressForSelectedPCSReplica(t *testing.T) {
	const (
		currentGeneration    = "generation-a"
		supersededGeneration = "generation-b"
	)

	pcsUID := uuid.NewUUID()
	pcs := testutils.NewPodCliqueSetBuilder(testPCSNamePCSG, testNamespacePCSG, pcsUID).
		WithReplicas(1).
		WithScalingGroup(testPCSGName, []string{"worker"}).
		WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.RollingRecreateStrategy}).
		WithPodCliqueSetGenerationHash(ptr.To(currentGeneration)).
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

	pcsg := testutils.NewPodCliqueScalingGroupBuilder(testPCSGName, testNamespacePCSG, testPCSNamePCSG, 0).
		WithReplicas(1).
		WithOwnerReference("PodCliqueSet", testPCSNamePCSG, pcsUID).
		WithLabels(map[string]string{apicommon.LabelPodCliqueSetRevision: currentGeneration}).
		Build()
	pcsg.Status.CurrentPodCliqueSetRevision = currentGeneration
	pcsg.Status.UpdateProgress = &grovecorev1beta1.PodCliqueScalingGroupUpdateProgress{
		UpdateStartedAt:      metav1.Now(),
		PodCliqueSetRevision: supersededGeneration,
	}
	pcsg.Status.UpdatedReplicas = 1

	fakeClient := testutils.SetupFakeClient(pcs, pcsg)
	reconciler := &Reconciler{client: fakeClient}

	result := reconciler.processUpdate(t.Context(), logr.Discard(), pcsg)

	require.False(t, result.HasErrors())
	updatedPCSG := &grovecorev1beta1.PodCliqueScalingGroup{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcsg), updatedPCSG))
	require.NotNil(t, updatedPCSG.Status.UpdateProgress)
	assert.Equal(t, currentGeneration, updatedPCSG.Status.UpdateProgress.PodCliqueSetGenerationHash)
	assert.Equal(t, currentGeneration, updatedPCSG.Status.UpdateProgress.PodCliqueSetRevision)
	assert.Nil(t, updatedPCSG.Status.UpdateProgress.UpdateEndedAt)
	assert.Zero(t, updatedPCSG.Status.UpdatedReplicas)
}

// TestInitOrResetUpdate tests the initOrResetUpdate function for both OnDelete and RollingRecreate strategies
func TestInitOrResetUpdate(t *testing.T) {
	tests := []struct {
		name                   string
		setupPCS               func(pcsUID types.UID) *grovecorev1beta1.PodCliqueSet
		setupPCSG              func(pcsUID types.UID) *grovecorev1beta1.PodCliqueScalingGroup
		expectUpdateEndedAtSet bool
	}{
		{
			name: "rolling_recreate_strategy_does_not_set_update_ended_at",
			setupPCS: func(pcsUID types.UID) *grovecorev1beta1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSNamePCSG, testNamespacePCSG, pcsUID).
					WithReplicas(2).
					WithScalingGroup(testPCSGName, []string{"worker", "router"}).
					WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1beta1.RollingRecreateStrategy,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("current-hash")).
					Build()
			},
			setupPCSG: func(pcsUID types.UID) *grovecorev1beta1.PodCliqueScalingGroup {
				pcsg := testutils.NewPodCliqueScalingGroupBuilder(testPCSGName, testNamespacePCSG, testPCSNamePCSG, 0).
					WithReplicas(2).
					WithOwnerReference("PodCliqueSet", testPCSNamePCSG, pcsUID).
					Build()
				pcsg.Status.UpdatedReplicas = 3 // should be reset to 0
				return pcsg
			},
			expectUpdateEndedAtSet: false,
		},
		{
			name: "nil_update_strategy_does_not_set_update_ended_at",
			setupPCS: func(pcsUID types.UID) *grovecorev1beta1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSNamePCSG, testNamespacePCSG, pcsUID).
					WithReplicas(2).
					WithScalingGroup(testPCSGName, []string{"worker", "router"}).
					WithPodCliqueSetGenerationHash(ptr.To("current-hash")).
					Build()
			},
			setupPCSG: func(pcsUID types.UID) *grovecorev1beta1.PodCliqueScalingGroup {
				pcsg := testutils.NewPodCliqueScalingGroupBuilder(testPCSGName, testNamespacePCSG, testPCSNamePCSG, 0).
					WithReplicas(2).
					WithOwnerReference("PodCliqueSet", testPCSNamePCSG, pcsUID).
					Build()
				pcsg.Status.UpdatedReplicas = 2 // should be reset to 0
				return pcsg
			},
			expectUpdateEndedAtSet: false,
		},
		{
			name: "on_delete_strategy_sets_update_ended_at_immediately",
			setupPCS: func(pcsUID types.UID) *grovecorev1beta1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSNamePCSG, testNamespacePCSG, pcsUID).
					WithReplicas(2).
					WithScalingGroup(testPCSGName, []string{"worker", "router"}).
					WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1beta1.OnDeleteStrategy,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("current-hash")).
					Build()
			},
			setupPCSG: func(pcsUID types.UID) *grovecorev1beta1.PodCliqueScalingGroup {
				pcsg := testutils.NewPodCliqueScalingGroupBuilder(testPCSGName, testNamespacePCSG, testPCSNamePCSG, 0).
					WithReplicas(2).
					WithOwnerReference("PodCliqueSet", testPCSNamePCSG, pcsUID).
					Build()
				pcsg.Status.UpdatedReplicas = 5 // should be reset to 0
				return pcsg
			},
			expectUpdateEndedAtSet: true,
		},
		{
			name: "existing_rollingrecreate_in_progress_is_reset_when_hash_changes",
			setupPCS: func(pcsUID types.UID) *grovecorev1beta1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSNamePCSG, testNamespacePCSG, pcsUID).
					WithReplicas(3).
					WithScalingGroup(testPCSGName, []string{"worker", "router"}).
					WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1beta1.RollingRecreateStrategy,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("new-hash")).
					Build()
			},
			setupPCSG: func(pcsUID types.UID) *grovecorev1beta1.PodCliqueScalingGroup {
				updateStartedAt := metav1.Now()

				pcsg := testutils.NewPodCliqueScalingGroupBuilder(testPCSGName, testNamespacePCSG, testPCSNamePCSG, 0).
					WithReplicas(2).
					WithOwnerReference("PodCliqueSet", testPCSNamePCSG, pcsUID).
					Build()
				// Simulate an existing update in progress
				pcsg.Status.UpdateProgress = &grovecorev1beta1.PodCliqueScalingGroupUpdateProgress{
					UpdateStartedAt:            updateStartedAt,
					PodCliqueSetGenerationHash: "old-hash",
				}
				pcsg.Status.UpdatedReplicas = 2
				return pcsg
			},
			expectUpdateEndedAtSet: false,
		},
		{
			name: "existing_on_delete_update_is_reset_when_hash_changes",
			setupPCS: func(pcsUID types.UID) *grovecorev1beta1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSNamePCSG, testNamespacePCSG, pcsUID).
					WithReplicas(3).
					WithScalingGroup(testPCSGName, []string{"worker", "router"}).
					WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1beta1.OnDeleteStrategy,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("new-hash")).
					Build()
			},
			setupPCSG: func(pcsUID types.UID) *grovecorev1beta1.PodCliqueScalingGroup {
				updateStartedAt := metav1.Now()
				updateEndedAt := metav1.Now()

				pcsg := testutils.NewPodCliqueScalingGroupBuilder(testPCSGName, testNamespacePCSG, testPCSNamePCSG, 0).
					WithReplicas(2).
					WithOwnerReference("PodCliqueSet", testPCSNamePCSG, pcsUID).
					Build()
				// Simulate an existing update that was completed
				pcsg.Status.UpdateProgress = &grovecorev1beta1.PodCliqueScalingGroupUpdateProgress{
					UpdateStartedAt:            updateStartedAt,
					UpdateEndedAt:              ptr.To(updateEndedAt),
					PodCliqueSetGenerationHash: "old-hash",
				}
				pcsg.Status.UpdatedReplicas = 1
				return pcsg
			},
			expectUpdateEndedAtSet: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcsUID := uuid.NewUUID()
			pcs := tt.setupPCS(pcsUID)
			pcsg := tt.setupPCSG(pcsUID)
			pcsg.Labels[apicommon.LabelPodCliqueSetRevision] = "target-revision"

			fakeClient := testutils.SetupFakeClient(pcs, pcsg)
			reconciler := &Reconciler{client: fakeClient}

			err := reconciler.initOrResetUpdate(context.Background(), pcs, pcsg)
			require.NoError(t, err, "initOrResetUpdate should not return errors")

			// Fetch the updated PCSG from the fake client
			updatedPCSG := &grovecorev1beta1.PodCliqueScalingGroup{}
			err = fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcsg), updatedPCSG)
			require.NoError(t, err)

			// Verify UpdateProgress is set
			require.NotNil(t, updatedPCSG.Status.UpdateProgress, "UpdateProgress should be set")
			assert.NotEmpty(t, updatedPCSG.Status.UpdateProgress.UpdateStartedAt, "UpdateStartedAt should be set")

			// Verify UpdateEndedAt based on strategy
			if tt.expectUpdateEndedAtSet {
				require.NotNil(t, updatedPCSG.Status.UpdateProgress.UpdateEndedAt, "UpdateEndedAt should be set for OnDelete strategy")
			} else {
				assert.Nil(t, updatedPCSG.Status.UpdateProgress.UpdateEndedAt, "UpdateEndedAt should be nil for non-OnDelete strategy")
			}

			assert.Equal(t, *pcs.Status.CurrentGenerationHash, updatedPCSG.Status.UpdateProgress.PodCliqueSetGenerationHash, "PodCliqueSetGenerationHash should match PCS CurrentGenerationHash")
			assert.Equal(t, "target-revision", updatedPCSG.Status.UpdateProgress.PodCliqueSetRevision, "PodCliqueSetRevision should match the revision label")
			assert.Equal(t, int32(0), updatedPCSG.Status.UpdatedReplicas, "UpdatedReplicas should be reset to 0")
		})
	}
}

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

package podcliquescalinggroup

import (
	"context"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
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
		setupPCSG          func() *grovecorev1alpha1.PodCliqueScalingGroup
		expectPatchSkipped bool
		expectedGeneration int64
	}{
		{
			name: "patch_skipped_when_observed_equals_generation",
			setupPCSG: func() *grovecorev1alpha1.PodCliqueScalingGroup {
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
			setupPCSG: func() *grovecorev1alpha1.PodCliqueScalingGroup {
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
			setupPCSG: func() *grovecorev1alpha1.PodCliqueScalingGroup {
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
			updatedPCSG := &grovecorev1alpha1.PodCliqueScalingGroup{}
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

// TestShouldResetOrTriggerUpdatePCSG tests the shouldResetOrTriggerUpdate function for PodCliqueScalingGroup
func TestShouldResetOrTriggerUpdatePCSG(t *testing.T) {
	tests := []struct {
		name     string
		pcs      *grovecorev1alpha1.PodCliqueSet
		pcsg     *grovecorev1alpha1.PodCliqueScalingGroup
		expected bool
	}{
		{
			name: "should_trigger_update_when_no_update_progress",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					CurrentGenerationHash: ptr.To("new-hash"),
				},
			},
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					UpdateProgress: nil,
				},
			},
			expected: true,
		},
		{
			name: "should_not_trigger_update_when_hash_matches",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					CurrentGenerationHash: ptr.To("current-hash"),
				},
			},
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{
						PodCliqueSetGenerationHash: "current-hash",
					},
				},
			},
			expected: false,
		},
		{
			name: "should_trigger_update_when_hash_differs",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					CurrentGenerationHash: ptr.To("new-hash"),
				},
			},
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{
						PodCliqueSetGenerationHash: "old-hash",
					},
				},
			},
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := shouldResetOrTriggerUpdate(tc.pcs, tc.pcsg)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestInitOrResetUpdate tests the initOrResetUpdate function for both OnDelete and RollingRecreate strategies
func TestInitOrResetUpdate(t *testing.T) {
	tests := []struct {
		name                   string
		setupPCS               func(pcsUID types.UID) *grovecorev1alpha1.PodCliqueSet
		setupPCSG              func(pcsUID types.UID) *grovecorev1alpha1.PodCliqueScalingGroup
		expectUpdateEndedAtSet bool
	}{
		{
			name: "rolling_recreate_strategy_does_not_set_update_ended_at",
			setupPCS: func(pcsUID types.UID) *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSNamePCSG, testNamespacePCSG, pcsUID).
					WithReplicas(2).
					WithScalingGroup(testPCSGName, []string{"worker", "router"}).
					WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1alpha1.RollingRecreateStrategyType,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("current-hash")).
					Build()
			},
			setupPCSG: func(pcsUID types.UID) *grovecorev1alpha1.PodCliqueScalingGroup {
				pcsg := testutils.NewPodCliqueScalingGroupBuilder(testPCSGName, testNamespacePCSG, testPCSNamePCSG, 0).
					WithReplicas(2).
					WithOwnerReference("PodCliqueSet", testPCSNamePCSG, string(pcsUID)).
					Build()
				pcsg.Status.UpdatedReplicas = 3 // should be reset to 0
				return pcsg
			},
			expectUpdateEndedAtSet: false,
		},
		{
			name: "nil_update_strategy_does_not_set_update_ended_at",
			setupPCS: func(pcsUID types.UID) *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSNamePCSG, testNamespacePCSG, pcsUID).
					WithReplicas(2).
					WithScalingGroup(testPCSGName, []string{"worker", "router"}).
					WithPodCliqueSetGenerationHash(ptr.To("current-hash")).
					Build()
			},
			setupPCSG: func(pcsUID types.UID) *grovecorev1alpha1.PodCliqueScalingGroup {
				pcsg := testutils.NewPodCliqueScalingGroupBuilder(testPCSGName, testNamespacePCSG, testPCSNamePCSG, 0).
					WithReplicas(2).
					WithOwnerReference("PodCliqueSet", testPCSNamePCSG, string(pcsUID)).
					Build()
				pcsg.Status.UpdatedReplicas = 2 // should be reset to 0
				return pcsg
			},
			expectUpdateEndedAtSet: false,
		},
		{
			name: "on_delete_strategy_sets_update_ended_at_immediately",
			setupPCS: func(pcsUID types.UID) *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSNamePCSG, testNamespacePCSG, pcsUID).
					WithReplicas(2).
					WithScalingGroup(testPCSGName, []string{"worker", "router"}).
					WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1alpha1.OnDeleteStrategyType,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("current-hash")).
					Build()
			},
			setupPCSG: func(pcsUID types.UID) *grovecorev1alpha1.PodCliqueScalingGroup {
				pcsg := testutils.NewPodCliqueScalingGroupBuilder(testPCSGName, testNamespacePCSG, testPCSNamePCSG, 0).
					WithReplicas(2).
					WithOwnerReference("PodCliqueSet", testPCSNamePCSG, string(pcsUID)).
					Build()
				pcsg.Status.UpdatedReplicas = 5 // should be reset to 0
				return pcsg
			},
			expectUpdateEndedAtSet: true,
		},
		{
			name: "existing_rollingrecreate_in_progress_is_reset_when_hash_changes",
			setupPCS: func(pcsUID types.UID) *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSNamePCSG, testNamespacePCSG, pcsUID).
					WithReplicas(3).
					WithScalingGroup(testPCSGName, []string{"worker", "router"}).
					WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1alpha1.RollingRecreateStrategyType,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("new-hash")).
					Build()
			},
			setupPCSG: func(pcsUID types.UID) *grovecorev1alpha1.PodCliqueScalingGroup {
				updateStartedAt := metav1.Now()

				pcsg := testutils.NewPodCliqueScalingGroupBuilder(testPCSGName, testNamespacePCSG, testPCSNamePCSG, 0).
					WithReplicas(2).
					WithOwnerReference("PodCliqueSet", testPCSNamePCSG, string(pcsUID)).
					Build()
				// Simulate an existing update in progress
				pcsg.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{
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
			setupPCS: func(pcsUID types.UID) *grovecorev1alpha1.PodCliqueSet {
				return testutils.NewPodCliqueSetBuilder(testPCSNamePCSG, testNamespacePCSG, pcsUID).
					WithReplicas(3).
					WithScalingGroup(testPCSGName, []string{"worker", "router"}).
					WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1alpha1.OnDeleteStrategyType,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("new-hash")).
					Build()
			},
			setupPCSG: func(pcsUID types.UID) *grovecorev1alpha1.PodCliqueScalingGroup {
				updateStartedAt := metav1.Now()
				updateEndedAt := metav1.Now()

				pcsg := testutils.NewPodCliqueScalingGroupBuilder(testPCSGName, testNamespacePCSG, testPCSNamePCSG, 0).
					WithReplicas(2).
					WithOwnerReference("PodCliqueSet", testPCSNamePCSG, string(pcsUID)).
					Build()
				// Simulate an existing update that was completed
				pcsg.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{
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

			fakeClient := testutils.SetupFakeClient(pcs, pcsg)
			reconciler := &Reconciler{client: fakeClient}

			err := reconciler.initOrResetUpdate(context.Background(), pcs, pcsg)
			require.NoError(t, err, "initOrResetUpdate should not return errors")

			// Fetch the updated PCSG from the fake client
			updatedPCSG := &grovecorev1alpha1.PodCliqueScalingGroup{}
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
			assert.Equal(t, int32(0), updatedPCSG.Status.UpdatedReplicas, "UpdatedReplicas should be reset to 0")
		})
	}
}

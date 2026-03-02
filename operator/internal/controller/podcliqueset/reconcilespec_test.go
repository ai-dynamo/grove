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

package podcliqueset

import (
	"context"
	"sync"
	"testing"
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestUpdateObservedGeneration tests the updateObservedGeneration function for PodCliqueSet
func TestUpdateObservedGeneration(t *testing.T) {
	tests := []struct {
		name               string
		setupPCS           func() *grovecorev1alpha1.PodCliqueSet
		expectPatchSkipped bool
		expectedGeneration int64
	}{
		{
			name: "patch_skipped_when_observed_equals_generation",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
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
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
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
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
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
			updatedPCS := &grovecorev1alpha1.PodCliqueSet{}
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

// TestGetOrderedKindsForSync tests the getOrderedKindsForSync function
func TestGetOrderedKindsForSync(t *testing.T) {
	kinds := getOrderedKindsForSync()

	expectedKinds := []component.Kind{
		component.KindServiceAccount,
		component.KindRole,
		component.KindRoleBinding,
		component.KindServiceAccountTokenSecret,
		component.KindHeadlessService,
		component.KindHorizontalPodAutoscaler,
		component.KindPodCliqueSetReplica,
		component.KindComputeDomain,
		component.KindPodClique,
		component.KindPodCliqueScalingGroup,
		component.KindPodGang,
	}

	assert.Equal(t, len(expectedKinds), len(kinds))
	for i, expected := range expectedKinds {
		assert.Equal(t, expected, kinds[i])
	}
}

// TestInitUpdateProgress tests the initUpdateProgress function for both OnDelete and RollingRecreate strategies
func TestInitUpdateProgress(t *testing.T) {
	tests := []struct {
		name                   string
		setupPCS               func() *grovecorev1alpha1.PodCliqueSet
		expectUpdateEndedAtSet bool
	}{
		{
			name: "rolling_recreate_strategy_does_not_set_update_ended_at",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(2).
					WithPodCliqueParameters("worker", 1, nil).
					WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1alpha1.RollingRecreateStrategyType,
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
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
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
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(2).
					WithPodCliqueParameters("worker", 1, nil).
					WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1alpha1.OnDeleteStrategyType,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("old-hash")).
					Build()
				pcs.Status.UpdatedReplicas = 5 // should be reset to 0
				return pcs
			},
			expectUpdateEndedAtSet: true,
		},
		{
			name: "existing_rollingrecreate_in_progress_is_reset_when_hash_changes",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				updateStartedAt := metav1.Now()
				replicaUpdateStartedAt := metav1.Time{Time: updateStartedAt.Add(time.Second)}

				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(3).
					WithPodCliqueParameters("worker", 1, nil).
					WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1alpha1.RollingRecreateStrategyType,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("old-hash")).
					WithUpdateProgress(&grovecorev1alpha1.PodCliqueSetUpdateProgress{
						UpdateStartedAt:               updateStartedAt,
						UpdatedPodCliqueScalingGroups: []string{"pcsg-1", "pcsg-2"},
						UpdatedPodCliques:             []string{"pclq-1", "pclq-2", "pclq-3"},
						CurrentlyUpdating: &grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{
							ReplicaIndex:    1,
							UpdateStartedAt: replicaUpdateStartedAt,
						},
					}).
					Build()
				pcs.Status.UpdatedReplicas = 2
				return pcs
			},
			expectUpdateEndedAtSet: false,
		},
		{
			name: "existing_on_delete_update_is_reset_when_hash_changes",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				updateStartedAt := metav1.Now()

				pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, uuid.NewUUID()).
					WithReplicas(3).
					WithPodCliqueParameters("worker", 1, nil).
					WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{
						Type: grovecorev1alpha1.OnDeleteStrategyType,
					}).
					WithPodCliqueSetGenerationHash(ptr.To("old-hash")).
					WithUpdateProgress(&grovecorev1alpha1.PodCliqueSetUpdateProgress{
						UpdateStartedAt:               updateStartedAt,
						UpdateEndedAt:                 ptr.To(updateStartedAt),
						UpdatedPodCliqueScalingGroups: []string{"pcsg-1"},
						UpdatedPodCliques:             []string{"pclq-1", "pclq-2"},
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

			err := reconciler.initUpdateProgress(context.Background(), pcs, pcsObjectName, newGenerationHash)
			require.NoError(t, err, "initUpdateProgress should not return errors")

			// Fetch the updated PCS from the fake client
			updatedPCS := &grovecorev1alpha1.PodCliqueSet{}
			err = fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), updatedPCS)
			require.NoError(t, err)

			// Verify UpdateProgress is set
			require.NotNil(t, updatedPCS.Status.UpdateProgress, "UpdateProgress should be set")
			assert.NotEmpty(t, updatedPCS.Status.UpdateProgress.UpdateStartedAt, "UpdateStartedAt should be set")

			// Verify UpdateEndedAt based on strategy
			if tt.expectUpdateEndedAtSet {
				require.NotNil(t, updatedPCS.Status.UpdateProgress.UpdateEndedAt, "UpdateEndedAt should be set for OnDelete strategy")
			} else {
				assert.Nil(t, updatedPCS.Status.UpdateProgress.UpdateEndedAt, "UpdateEndedAt should be nil for non-OnDelete strategy")
			}

			assert.Nil(t, updatedPCS.Status.UpdateProgress.UpdatedPodCliqueScalingGroups, "UpdatedPodCliqueScalingGroups should be nil")
			assert.Nil(t, updatedPCS.Status.UpdateProgress.UpdatedPodCliques, "UpdatedPodCliques should be nil")
			// Currently updating is not set by the initUpdateProgress function
			assert.Nil(t, updatedPCS.Status.UpdateProgress.CurrentlyUpdating, "CurrentlyUpdating should be nil")
			assert.Equal(t, int32(0), updatedPCS.Status.UpdatedReplicas, "UpdatedReplicas should be reset to 0")
			require.NotNil(t, updatedPCS.Status.CurrentGenerationHash)
			assert.Equal(t, newGenerationHash, *updatedPCS.Status.CurrentGenerationHash)
		})
	}
}

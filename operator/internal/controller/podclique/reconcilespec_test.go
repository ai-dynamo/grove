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

package podclique

import (
	"context"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestPcsHasNoActiveRollingUpdate tests the pcsHasNoActiveRollingUpdate function
func TestPcsHasNoActiveRollingUpdate(t *testing.T) {
	tests := []struct {
		// Test case description
		name string
		// pcs is the PodCliqueSet to check
		pcs *grovecorev1alpha1.PodCliqueSet
		// expected is the expected result
		expected bool
	}{
		{
			// Tests when CurrentGenerationHash is nil
			name: "current_generation_hash_nil",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					CurrentGenerationHash: nil,
				},
			},
			expected: true,
		},
		{
			// Tests when RollingUpdateProgress is nil
			name: "rolling_update_progress_nil",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					CurrentGenerationHash: ptr.To("hash123"),
					UpdateProgress:        nil,
				},
			},
			expected: true,
		},
		{
			// Tests when CurrentlyUpdating is nil
			name: "currently_updating_nil",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					CurrentGenerationHash: ptr.To("hash123"),
					UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
						CurrentlyUpdating: nil,
					},
				},
			},
			expected: true,
		},
		{
			// Tests when all required fields are present (active rolling update)
			name: "active_rolling_update",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					CurrentGenerationHash: ptr.To("hash123"),
					UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
						CurrentlyUpdating: &grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{
							ReplicaIndex: 0,
						},
					},
				},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := pcsHasNoActiveRollingUpdate(tc.pcs)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestGetOrderedKindsForSync tests the getOrderedKindsForSync function
func TestGetOrderedKindsForSync(t *testing.T) {
	// Verifies that the function returns the expected ordered list
	kinds := getOrderedKindsForSync()
	assert.Equal(t, 1, len(kinds))
	assert.Equal(t, component.KindPod, kinds[0])
}

// TestUpdateObservedGeneration tests the updateObservedGeneration function for PodClique
func TestUpdateObservedGeneration(t *testing.T) {
	const (
		testNamespace = "test-namespace"
		testPCSName   = "test-pcs"
	)

	tests := []struct {
		name               string
		setupPCLQ          func() *grovecorev1alpha1.PodClique
		expectPatchSkipped bool
		expectedGeneration int64
	}{
		{
			name: "patch_skipped_when_observed_equals_generation",
			setupPCLQ: func() *grovecorev1alpha1.PodClique {
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
			setupPCLQ: func() *grovecorev1alpha1.PodClique {
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
			setupPCLQ: func() *grovecorev1alpha1.PodClique {
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
			updatedPCLQ := &grovecorev1alpha1.PodClique{}
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

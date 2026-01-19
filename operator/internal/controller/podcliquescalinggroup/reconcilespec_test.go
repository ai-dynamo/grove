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

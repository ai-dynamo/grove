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
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"
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
					RollingUpdateProgress: nil,
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
					RollingUpdateProgress: &grovecorev1alpha1.PodCliqueSetRollingUpdateProgress{
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
					RollingUpdateProgress: &grovecorev1alpha1.PodCliqueSetRollingUpdateProgress{
						CurrentlyUpdating: &grovecorev1alpha1.PodCliqueSetReplicaRollingUpdateProgress{
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
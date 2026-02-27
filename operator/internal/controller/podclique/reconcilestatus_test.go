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
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// TestMutateUpdatedReplica tests the mutateUpdatedReplica function across different PodClique states
func TestMutateUpdatedReplica(t *testing.T) {
	tests := []struct {
		name                    string
		pclq                    *grovecorev1alpha1.PodClique
		existingPods            []*corev1.Pod
		expectedUpdatedReplicas int32
	}{
		{
			name: "rolling update in progress - count pods with new hash",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{
						PodTemplateHash: "new-hash-v2",
						UpdateStartedAt: metav1.Time{Time: time.Now()},
						UpdateEndedAt:   nil, // Still in progress
					},
					CurrentPodTemplateHash: ptr.To("old-hash-v1"),
				},
			},
			existingPods: []*corev1.Pod{
				// 3 pods updated to new version
				createPodWithHash("pod-1", "new-hash-v2"),
				createPodWithHash("pod-2", "new-hash-v2"),
				createPodWithHash("pod-3", "new-hash-v2"),
				// 7 pods still on old version
				createPodWithHash("pod-4", "old-hash-v1"),
				createPodWithHash("pod-5", "old-hash-v1"),
				createPodWithHash("pod-6", "old-hash-v1"),
				createPodWithHash("pod-7", "old-hash-v1"),
				createPodWithHash("pod-8", "old-hash-v1"),
				createPodWithHash("pod-9", "old-hash-v1"),
				createPodWithHash("pod-10", "old-hash-v1"),
			},
			expectedUpdatedReplicas: 3, // Only the 3 pods with new hash
		},
		{
			name: "update just completed - use RollingUpdateProgress hash (edge case)",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{
						PodTemplateHash: "new-hash-v2",
						UpdateStartedAt: metav1.Time{Time: time.Now().Add(-5 * time.Minute)},
						UpdateEndedAt:   &metav1.Time{Time: time.Now()}, // Just completed
					},
					// CurrentPodTemplateHash not updated yet - still has old hash
					CurrentPodTemplateHash: ptr.To("old-hash-v1"),
				},
			},
			existingPods: []*corev1.Pod{
				// All pods updated to new version
				createPodWithHash("pod-1", "new-hash-v2"),
				createPodWithHash("pod-2", "new-hash-v2"),
				createPodWithHash("pod-3", "new-hash-v2"),
				createPodWithHash("pod-4", "new-hash-v2"),
				createPodWithHash("pod-5", "new-hash-v2"),
				createPodWithHash("pod-6", "new-hash-v2"),
				createPodWithHash("pod-7", "new-hash-v2"),
				createPodWithHash("pod-8", "new-hash-v2"),
				createPodWithHash("pod-9", "new-hash-v2"),
				createPodWithHash("pod-10", "new-hash-v2"),
			},
			expectedUpdatedReplicas: 10, // All 10 pods should be counted as updated
		},
		{
			name: "steady state - no rolling update",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress:         nil, // No rolling update
					CurrentPodTemplateHash: ptr.To("stable-hash"),
				},
			},
			existingPods: []*corev1.Pod{
				createPodWithHash("pod-1", "stable-hash"),
				createPodWithHash("pod-2", "stable-hash"),
				createPodWithHash("pod-3", "stable-hash"),
				createPodWithHash("pod-4", "stable-hash"),
				createPodWithHash("pod-5", "stable-hash"),
			},
			expectedUpdatedReplicas: 5, // All pods match current hash
		},
		{
			name: "never reconciled - empty hash",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress:         nil,
					CurrentPodTemplateHash: nil, // Never set
				},
			},
			existingPods: []*corev1.Pod{
				createPodWithHash("pod-1", "some-hash"),
				createPodWithHash("pod-2", "some-hash"),
			},
			expectedUpdatedReplicas: 0, // Should not count any pods when hash is unknown - no rolling update
		},
		{
			name: "mixed state - some pods match, some don't",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress:         nil,
					CurrentPodTemplateHash: ptr.To("current-hash"),
				},
			},
			existingPods: []*corev1.Pod{
				createPodWithHash("pod-1", "current-hash"),
				createPodWithHash("pod-2", "current-hash"),
				createPodWithHash("pod-3", "old-hash"),
				createPodWithHash("pod-4", "old-hash"),
				createPodWithHash("pod-5", "old-hash"),
			},
			expectedUpdatedReplicas: 2, // Only pods with current hash
		},
		{
			name: "no pods exist",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					CurrentPodTemplateHash: ptr.To("some-hash"),
				},
			},
			existingPods:            []*corev1.Pod{},
			expectedUpdatedReplicas: 0,
		},
		{
			name: "rolling update progress exists but pods have different hash",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{
						PodTemplateHash: "target-hash",
						UpdateStartedAt: metav1.Time{Time: time.Now()},
					},
					CurrentPodTemplateHash: ptr.To("old-hash"),
				},
			},
			existingPods: []*corev1.Pod{
				createPodWithHash("pod-1", "completely-different-hash"),
				createPodWithHash("pod-2", "another-hash"),
			},
			expectedUpdatedReplicas: 0, // None match the target hash
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Call the function
			mutateUpdatedReplica(tt.pclq, tt.existingPods)

			// Assert the result
			assert.Equal(t, tt.expectedUpdatedReplicas, tt.pclq.Status.UpdatedReplicas,
				"UpdatedReplicas should match expected value")
		})
	}
}

// createPodWithHash creates a test pod with the specified template hash label
func createPodWithHash(name string, templateHash string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				apicommon.LabelPodTemplateHash: templateHash,
			},
		},
	}
}

// TestMirrorUpdateProgressToRollingUpdateProgressPCLQ tests the mirrorUpdateProgressToRollingUpdateProgress function for PodClique
func TestMirrorUpdateProgressToRollingUpdateProgressPCLQ(t *testing.T) {
	updateStartedAt := metav1.Now()
	updateEndedAt := metav1.NewTime(updateStartedAt.Add(1))
	tests := []struct {
		name                          string
		pclq                          *grovecorev1alpha1.PodClique
		expectedRollingUpdateProgress *grovecorev1alpha1.PodCliqueRollingUpdateProgress
	}{
		{
			name: "nil UpdateProgress results in nil RollingUpdateProgress",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: nil,
				},
			},
			expectedRollingUpdateProgress: nil,
		},
		{
			name: "UpdateProgress with no ReadyPodsSelectedToUpdate",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{
						UpdateStartedAt:            updateStartedAt,
						PodCliqueSetGenerationHash: "gen-hash-123",
						PodTemplateHash:            "pod-hash-456",
					},
				},
			},
			expectedRollingUpdateProgress: &grovecorev1alpha1.PodCliqueRollingUpdateProgress{
				UpdateStartedAt:            updateStartedAt,
				PodCliqueSetGenerationHash: "gen-hash-123",
				PodTemplateHash:            "pod-hash-456",
			},
		},
		{
			name: "UpdateProgress with ReadyPodsSelectedToUpdate",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{
						UpdateStartedAt:            updateStartedAt,
						UpdateEndedAt:              ptr.To(updateEndedAt),
						PodCliqueSetGenerationHash: "gen-hash-789",
						PodTemplateHash:            "pod-hash-012",
						ReadyPodsSelectedToUpdate: &grovecorev1alpha1.PodsSelectedToUpdate{
							Current:   "pod-1",
							Completed: []string{"pod-0"},
						},
					},
				},
			},
			expectedRollingUpdateProgress: &grovecorev1alpha1.PodCliqueRollingUpdateProgress{
				UpdateStartedAt:            updateStartedAt,
				UpdateEndedAt:              ptr.To(updateEndedAt),
				PodCliqueSetGenerationHash: "gen-hash-789",
				PodTemplateHash:            "pod-hash-012",
				ReadyPodsSelectedToUpdate: &grovecorev1alpha1.PodsSelectedToUpdate{
					Current:   "pod-1",
					Completed: []string{"pod-0"},
				},
			},
		},
		{
			name: "clears existing RollingUpdateProgress when UpdateProgress is nil",
			pclq: &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: nil,
					RollingUpdateProgress: &grovecorev1alpha1.PodCliqueRollingUpdateProgress{
						UpdateStartedAt:            updateStartedAt,
						UpdateEndedAt:              ptr.To(updateEndedAt),
						PodCliqueSetGenerationHash: "old-gen-hash",
						PodTemplateHash:            "old-pod-hash",
						ReadyPodsSelectedToUpdate: &grovecorev1alpha1.PodsSelectedToUpdate{
							Current:   "pod-1",
							Completed: []string{"pod-0"},
						},
					},
				},
			},
			expectedRollingUpdateProgress: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Call the function
			mirrorUpdateProgressToRollingUpdateProgress(tt.pclq)

			// Assert the result
			if tt.expectedRollingUpdateProgress == nil {
				assert.Nil(t, tt.pclq.Status.RollingUpdateProgress,
					"RollingUpdateProgress should be nil")
			} else {
				assert.NotNil(t, tt.pclq.Status.RollingUpdateProgress,
					"RollingUpdateProgress should not be nil")
				assert.Equal(t, tt.expectedRollingUpdateProgress.UpdateStartedAt,
					tt.pclq.Status.RollingUpdateProgress.UpdateStartedAt,
					"UpdateStartedAt should match")
				assert.Equal(t, tt.expectedRollingUpdateProgress.UpdateEndedAt,
					tt.pclq.Status.RollingUpdateProgress.UpdateEndedAt,
					"UpdateEndedAt should match")
				assert.Equal(t, tt.expectedRollingUpdateProgress.PodCliqueSetGenerationHash,
					tt.pclq.Status.RollingUpdateProgress.PodCliqueSetGenerationHash,
					"PodCliqueSetGenerationHash should match")
				assert.Equal(t, tt.expectedRollingUpdateProgress.PodTemplateHash,
					tt.pclq.Status.RollingUpdateProgress.PodTemplateHash,
					"PodTemplateHash should match")

				// Check ReadyPodsSelectedToUpdate
				if tt.expectedRollingUpdateProgress.ReadyPodsSelectedToUpdate == nil {
					assert.Nil(t, tt.pclq.Status.RollingUpdateProgress.ReadyPodsSelectedToUpdate,
						"ReadyPodsSelectedToUpdate should be nil")
				} else {
					assert.NotNil(t, tt.pclq.Status.RollingUpdateProgress.ReadyPodsSelectedToUpdate,
						"ReadyPodsSelectedToUpdate should not be nil")
					assert.Equal(t, tt.expectedRollingUpdateProgress.ReadyPodsSelectedToUpdate.Current,
						tt.pclq.Status.RollingUpdateProgress.ReadyPodsSelectedToUpdate.Current,
						"Current pod should match")
					assert.Equal(t, tt.expectedRollingUpdateProgress.ReadyPodsSelectedToUpdate.Completed,
						tt.pclq.Status.RollingUpdateProgress.ReadyPodsSelectedToUpdate.Completed,
						"Completed pods should match")
				}
			}
		})
	}
}

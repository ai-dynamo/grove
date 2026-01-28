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
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
					RollingUpdateProgress: &grovecorev1alpha1.PodCliqueRollingUpdateProgress{
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
					RollingUpdateProgress: &grovecorev1alpha1.PodCliqueRollingUpdateProgress{
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
					RollingUpdateProgress:  nil, // No rolling update
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
					RollingUpdateProgress:  nil,
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
					RollingUpdateProgress:  nil,
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
					RollingUpdateProgress: &grovecorev1alpha1.PodCliqueRollingUpdateProgress{
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

func TestComputeMinAvailableBreachedCondition_FLIPDetection(t *testing.T) {
	tests := []struct {
		name           string
		ctx            minAvailableBreachedContext
		expectedStatus metav1.ConditionStatus
		expectedReason string
		description    string
	}{
		// FLIP Detection Tests
		{
			name: "FLIP detected: was available, now unavailable",
			ctx: minAvailableBreachedContext{
				oldReadyReplicas:   3,
				newReadyReplicas:   2,
				minAvailable:       3,
				existingCondition:  nil,
				isUpdateInProgress: false,
			},
			expectedStatus: metav1.ConditionTrue,
			expectedReason: constants.ConditionReasonInsufficientReadyPods,
			description:    "When ready pods drop from >= min to < min, breach should be detected",
		},
		{
			name: "No FLIP: was never available",
			ctx: minAvailableBreachedContext{
				oldReadyReplicas:   2,
				newReadyReplicas:   1,
				minAvailable:       3,
				existingCondition:  nil,
				isUpdateInProgress: false,
			},
			expectedStatus: metav1.ConditionFalse,
			expectedReason: constants.ConditionReasonNeverAvailable,
			description:    "When was never available (old < min), no breach should be set",
		},
		{
			name: "No FLIP: still starting up",
			ctx: minAvailableBreachedContext{
				oldReadyReplicas:   0,
				newReadyReplicas:   2,
				minAvailable:       3,
				existingCondition:  nil,
				isUpdateInProgress: false,
			},
			expectedStatus: metav1.ConditionFalse,
			expectedReason: constants.ConditionReasonNeverAvailable,
			description:    "During startup when pods haven't reached min, no breach should be set",
		},

		// Persisted State Tests
		{
			name: "Respect persisted breach: already True",
			ctx: minAvailableBreachedContext{
				oldReadyReplicas: 2,
				newReadyReplicas: 2,
				minAvailable:     3,
				existingCondition: &metav1.Condition{
					Type:   constants.ConditionTypeMinAvailableBreached,
					Status: metav1.ConditionTrue,
					Reason: constants.ConditionReasonInsufficientReadyPods,
				},
				isUpdateInProgress: false,
			},
			expectedStatus: metav1.ConditionTrue,
			expectedReason: constants.ConditionReasonInsufficientReadyPods,
			description:    "When breach already persisted, should keep True (operator restart scenario)",
		},
		{
			name: "Recovery: persisted breach clears when available",
			ctx: minAvailableBreachedContext{
				oldReadyReplicas: 2,
				newReadyReplicas: 3,
				minAvailable:     3,
				existingCondition: &metav1.Condition{
					Type:   constants.ConditionTypeMinAvailableBreached,
					Status: metav1.ConditionTrue,
					Reason: constants.ConditionReasonInsufficientReadyPods,
				},
				isUpdateInProgress: false,
			},
			expectedStatus: metav1.ConditionFalse,
			expectedReason: constants.ConditionReasonSufficientReadyPods,
			description:    "When pods recover to >= min, breach should clear",
		},
		{
			name: "Persisted False, FLIP detected → True",
			ctx: minAvailableBreachedContext{
				oldReadyReplicas: 3,
				newReadyReplicas: 2,
				minAvailable:     3,
				existingCondition: &metav1.Condition{
					Type:   constants.ConditionTypeMinAvailableBreached,
					Status: metav1.ConditionFalse,
					Reason: constants.ConditionReasonSufficientReadyPods,
				},
				isUpdateInProgress: false,
			},
			expectedStatus: metav1.ConditionTrue,
			expectedReason: constants.ConditionReasonInsufficientReadyPods,
			description:    "When persisted is False but FLIP detected, should set True",
		},

		// Rolling Update Tests
		{
			name: "Update in progress: Unknown",
			ctx: minAvailableBreachedContext{
				oldReadyReplicas:   3,
				newReadyReplicas:   2,
				minAvailable:       3,
				existingCondition:  nil,
				isUpdateInProgress: true,
			},
			expectedStatus: metav1.ConditionUnknown,
			expectedReason: constants.ConditionReasonUpdateInProgress,
			description:    "During rolling update, condition should be Unknown",
		},
		{
			name: "Update complete, below threshold, no prior breach",
			ctx: minAvailableBreachedContext{
				oldReadyReplicas: 2,
				newReadyReplicas: 2,
				minAvailable:     3,
				existingCondition: &metav1.Condition{
					Type:   constants.ConditionTypeMinAvailableBreached,
					Status: metav1.ConditionUnknown,
					Reason: constants.ConditionReasonUpdateInProgress,
				},
				isUpdateInProgress: false,
			},
			expectedStatus: metav1.ConditionFalse,
			expectedReason: constants.ConditionReasonNeverAvailable,
			description:    "After update completes with pods below threshold, no FLIP means no breach",
		},

		// Currently Available Tests
		{
			name: "Currently available: sufficient pods",
			ctx: minAvailableBreachedContext{
				oldReadyReplicas:   3,
				newReadyReplicas:   3,
				minAvailable:       3,
				existingCondition:  nil,
				isUpdateInProgress: false,
			},
			expectedStatus: metav1.ConditionFalse,
			expectedReason: constants.ConditionReasonSufficientReadyPods,
			description:    "When ready >= min, condition should be False",
		},
		{
			name: "Currently available: more than minimum",
			ctx: minAvailableBreachedContext{
				oldReadyReplicas:   3,
				newReadyReplicas:   5,
				minAvailable:       3,
				existingCondition:  nil,
				isUpdateInProgress: false,
			},
			expectedStatus: metav1.ConditionFalse,
			expectedReason: constants.ConditionReasonSufficientReadyPods,
			description:    "When ready > min, condition should be False",
		},

		// Edge Cases
		{
			name: "First reconciliation: no pods yet",
			ctx: minAvailableBreachedContext{
				oldReadyReplicas:   0,
				newReadyReplicas:   0,
				minAvailable:       3,
				existingCondition:  nil,
				isUpdateInProgress: false,
			},
			expectedStatus: metav1.ConditionFalse,
			expectedReason: constants.ConditionReasonNeverAvailable,
			description:    "On first reconciliation with no pods, should be False/NeverAvailable",
		},
		{
			name: "Partial scheduling: some pods ready but below min",
			ctx: minAvailableBreachedContext{
				oldReadyReplicas:   1,
				newReadyReplicas:   2,
				minAvailable:       3,
				existingCondition:  nil,
				isUpdateInProgress: false,
			},
			expectedStatus: metav1.ConditionFalse,
			expectedReason: constants.ConditionReasonNeverAvailable,
			description:    "When partial pods are ready but never reached min, no breach",
		},
		{
			name: "FLIP at exact boundary",
			ctx: minAvailableBreachedContext{
				oldReadyReplicas:   3,
				newReadyReplicas:   2,
				minAvailable:       3,
				existingCondition:  nil,
				isUpdateInProgress: false,
			},
			expectedStatus: metav1.ConditionTrue,
			expectedReason: constants.ConditionReasonInsufficientReadyPods,
			description:    "FLIP from exactly min to below min should trigger breach",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := computeMinAvailableBreachedCondition(tt.ctx)

			assert.Equal(t, constants.ConditionTypeMinAvailableBreached, result.Type,
				"Condition type should always be MinAvailableBreached")
			assert.Equal(t, tt.expectedStatus, result.Status,
				"Status mismatch for: %s", tt.description)
			assert.Equal(t, tt.expectedReason, result.Reason,
				"Reason mismatch for: %s", tt.description)
		})
	}
}

func TestComputeMinAvailableBreachedCondition_Messages(t *testing.T) {
	tests := []struct {
		name            string
		ctx             minAvailableBreachedContext
		messageContains string
	}{
		{
			name: "FLIP message shows transition",
			ctx: minAvailableBreachedContext{
				oldReadyReplicas:   3,
				newReadyReplicas:   2,
				minAvailable:       3,
				existingCondition:  nil,
				isUpdateInProgress: false,
			},
			messageContains: "dropped below minimum",
		},
		{
			name: "NeverAvailable message mentions threshold",
			ctx: minAvailableBreachedContext{
				oldReadyReplicas:   0,
				newReadyReplicas:   2,
				minAvailable:       3,
				existingCondition:  nil,
				isUpdateInProgress: false,
			},
			messageContains: "not yet reached MinAvailable threshold",
		},
		{
			name: "Sufficient message shows counts",
			ctx: minAvailableBreachedContext{
				oldReadyReplicas:   3,
				newReadyReplicas:   3,
				minAvailable:       3,
				existingCondition:  nil,
				isUpdateInProgress: false,
			},
			messageContains: "Sufficient ready pods",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := computeMinAvailableBreachedCondition(tt.ctx)
			assert.Contains(t, result.Message, tt.messageContains,
				"Message should contain expected text")
		})
	}
}

// TestReconcileStatus_ObservedGenerationGuard tests that the MinAvailableBreached condition
// is NOT mutated when ObservedGeneration is nil (first reconciliation before updateObservedGeneration runs).
// This ensures we don't prematurely set breach conditions during initial PodClique creation.
func TestReconcileStatus_ObservedGenerationGuard(t *testing.T) {
	const (
		testNamespace = "test-namespace"
		testPCSName   = "test-pcs"
		testPCSUID    = types.UID("test-pcs-uid")
	)

	tests := []struct {
		name                         string
		observedGeneration           *int64
		expectConditionMutated       bool
		expectedConditionAfterMutate metav1.ConditionStatus // only checked if expectConditionMutated is true
		description                  string
	}{
		{
			name:                   "ObservedGeneration_nil_skips_condition_mutation",
			observedGeneration:     nil,
			expectConditionMutated: false,
			description:            "When ObservedGeneration is nil, conditions should NOT be mutated",
		},
		{
			name:                         "ObservedGeneration_set_allows_condition_mutation",
			observedGeneration:           ptr.To(int64(1)),
			expectConditionMutated:       true,
			expectedConditionAfterMutate: metav1.ConditionFalse, // NeverAvailable since no pods are ready
			description:                  "When ObservedGeneration is set, conditions should be mutated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a PodCliqueSet
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, testPCSUID).
				WithStandaloneCliqueReplicas("worker", 3).
				Build()

			// Create a PodClique with the specified ObservedGeneration
			pclq := testutils.NewPodCliqueBuilder(testPCSName, testPCSUID, "worker", testNamespace, 0).
				WithReplicas(3).
				Build()
			pclq.Status.ObservedGeneration = tt.observedGeneration

			// Verify no conditions exist initially
			require.Empty(t, pclq.Status.Conditions, "PodClique should have no conditions initially")

			// Create fake client with PCS and PCLQ
			fakeClient := testutils.SetupFakeClient(pcs, pclq)

			// Create reconciler
			reconciler := &Reconciler{client: fakeClient}

			// Execute reconcileStatus
			ctx := context.Background()
			logger := logr.Discard()
			result := reconciler.reconcileStatus(ctx, logger, pclq)

			// Verify no errors
			require.False(t, result.HasErrors(), "reconcileStatus should not return errors")

			// Fetch the updated PCLQ from the fake client
			updatedPCLQ := &grovecorev1alpha1.PodClique{}
			err := fakeClient.Get(ctx, client.ObjectKeyFromObject(pclq), updatedPCLQ)
			require.NoError(t, err)

			// Check if MinAvailableBreached condition was set
			breachCondition := meta.FindStatusCondition(updatedPCLQ.Status.Conditions, constants.ConditionTypeMinAvailableBreached)

			if tt.expectConditionMutated {
				assert.NotNil(t, breachCondition,
					"%s: MinAvailableBreached condition should be set when ObservedGeneration is not nil", tt.description)
				if breachCondition != nil {
					assert.Equal(t, tt.expectedConditionAfterMutate, breachCondition.Status,
						"%s: Condition status mismatch", tt.description)
				}
			} else {
				assert.Nil(t, breachCondition,
					"%s: MinAvailableBreached condition should NOT be set when ObservedGeneration is nil", tt.description)
			}
		})
	}
}

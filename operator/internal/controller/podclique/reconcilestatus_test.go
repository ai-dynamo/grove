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
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	internalconstants "github.com/ai-dynamo/grove/operator/internal/constants"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
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
			name: "update just completed - use UpdateProgress hash (edge case)",
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

// TestEmitAllScheduledReplicasLostIfNeeded covers the only explicit signal users have when a
// previously-running PodClique loses every scheduled pod. Gang termination is suppressed in
// that state, so this event must fire on the non-zero → zero transition (and only on that
// transition) for the regression to remain observable.
func TestEmitAllScheduledReplicasLostIfNeeded(t *testing.T) {
	tests := []struct {
		name              string
		originalScheduled int32
		nowScheduled      int32
		wantEvent         bool
	}{
		{name: "non-zero to zero emits event", originalScheduled: 3, nowScheduled: 0, wantEvent: true},
		{name: "zero to zero stays silent (initial startup)", originalScheduled: 0, nowScheduled: 0, wantEvent: false},
		{name: "non-zero to non-zero stays silent (partial regression handled by breach)", originalScheduled: 3, nowScheduled: 2, wantEvent: false},
		{name: "zero to non-zero stays silent (recovery)", originalScheduled: 0, nowScheduled: 3, wantEvent: false},
		{name: "stable non-zero stays silent (steady state)", originalScheduled: 3, nowScheduled: 3, wantEvent: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := record.NewFakeRecorder(2)
			r := &Reconciler{eventRecorder: recorder}
			pclq := &grovecorev1alpha1.PodClique{
				Status: grovecorev1alpha1.PodCliqueStatus{ScheduledReplicas: tt.nowScheduled},
			}

			r.emitAllScheduledReplicasLostIfNeeded(pclq, tt.originalScheduled)

			select {
			case ev := <-recorder.Events:
				if !tt.wantEvent {
					t.Fatalf("unexpected event: %s", ev)
				}
				assert.Contains(t, ev, "Warning", "event type should be Warning")
				assert.Contains(t, ev, internalconstants.ReasonAllScheduledReplicasLost, "event reason mismatch")
			default:
				if tt.wantEvent {
					t.Fatal("expected an event, got none")
				}
			}
		})
	}
}

// TestComputeMinAvailableBreachedConditionPartialScheduleRegression covers the
// behaviour where MinAvailableBreached must flip True when scheduled replicas
// drop below MinAvailable but stay above zero. With a gang scheduler this can
// only happen by regression after a healthy state. scheduledReplicas == 0 stays
// suppressed regardless of history: recreating the PodGang would just produce
// the same Pending pods against the same cluster state (churn loop).
func TestComputeMinAvailableBreachedConditionPartialScheduleRegression(t *testing.T) {
	pastTransition := metav1.NewTime(time.Now().Add(-10 * time.Minute))

	tests := []struct {
		name                                                string
		pclq                                                *grovecorev1alpha1.PodClique
		numPodsHavingAtleastOneContainerWithNonZeroExitCode int
		numPodsStartedButNotReady                           int
		wantStatus                                          metav1.ConditionStatus
		wantReason                                          string
	}{
		{
			name: "0 < scheduled < MinAvailable breaches",
			pclq: &grovecorev1alpha1.PodClique{
				Spec: grovecorev1alpha1.PodCliqueSpec{
					Replicas:     3,
					MinAvailable: ptr.To(int32(3)),
				},
				Status: grovecorev1alpha1.PodCliqueStatus{
					ObservedGeneration: ptr.To(int64(1)),
					Replicas:           3,
					ScheduledReplicas:  1,
					ReadyReplicas:      1,
					Conditions: []metav1.Condition{
						{
							Type:               constants.ConditionTypePodCliqueScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             constants.ConditionReasonSufficientScheduledPods,
							LastTransitionTime: pastTransition,
						},
						{
							Type:               constants.ConditionTypeMinAvailableBreached,
							Status:             metav1.ConditionFalse,
							Reason:             constants.ConditionReasonSufficientReadyPods,
							LastTransitionTime: pastTransition,
						},
					},
				},
			},
			wantStatus: metav1.ConditionTrue,
			wantReason: constants.ConditionReasonScheduledReplicasBelowMinAvailable,
		},
		{
			// Sanity-pin: scheduled == 0 must NOT breach even when the PCLQ was
			// previously healthy. Gang termination has no useful action here
			// (would re-create the same Pending pods) and the suppression has
			// to win to avoid a churn loop.
			name: "previously-healthy PCLQ loses all scheduled pods — must NOT breach",
			pclq: &grovecorev1alpha1.PodClique{
				Spec: grovecorev1alpha1.PodCliqueSpec{
					Replicas:     2,
					MinAvailable: ptr.To(int32(2)),
				},
				Status: grovecorev1alpha1.PodCliqueStatus{
					ObservedGeneration: ptr.To(int64(1)),
					Replicas:           2,
					ScheduledReplicas:  0,
					ReadyReplicas:      0,
					Conditions: []metav1.Condition{
						{
							Type:               constants.ConditionTypePodCliqueScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             constants.ConditionReasonSufficientScheduledPods,
							LastTransitionTime: pastTransition,
						},
					},
				},
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: constants.ConditionReasonInsufficientScheduledPods,
		},
		{
			// Sanity case that the fix must preserve: a freshly-created PCLQ
			// that has not yet scheduled any pods MUST NOT be considered breached.
			name: "fresh PCLQ never scheduled — must not breach",
			pclq: &grovecorev1alpha1.PodClique{
				Spec: grovecorev1alpha1.PodCliqueSpec{
					Replicas:     3,
					MinAvailable: ptr.To(int32(3)),
				},
				Status: grovecorev1alpha1.PodCliqueStatus{
					ObservedGeneration: ptr.To(int64(1)),
					Replicas:           3,
					ScheduledReplicas:  0,
					ReadyReplicas:      0,
				},
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: constants.ConditionReasonInsufficientScheduledPods,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condition := computeMinAvailableBreachedCondition(tt.pclq,
				tt.numPodsHavingAtleastOneContainerWithNonZeroExitCode,
				tt.numPodsStartedButNotReady)
			assert.Equal(t, constants.ConditionTypeMinAvailableBreached, condition.Type)
			assert.Equal(t, tt.wantStatus, condition.Status, "MinAvailableBreached status mismatch")
			assert.Equal(t, tt.wantReason, condition.Reason, "MinAvailableBreached reason mismatch")
		})
	}
}

// TestMutateSelector verifies the /scale selector is published for standalone PodCliques (with or
// without ScaleConfig) and suppressed for PodCliques that belong to a PodCliqueScalingGroup,
// regardless of whether the PodClique itself has ScaleConfig set.
func TestMutateSelector(t *testing.T) {
	const (
		pcsName       = "test-pcs"
		standaloneFQN = "test-pcs-0-frontend"
		pcsgMemberFQN = "test-pcs-0-prefill-0-worker"
		pcsgName      = "test-pcs-0-prefill"
	)
	withScale := &grovecorev1alpha1.AutoScalingConfig{MaxReplicas: 5}

	tests := []struct {
		name             string
		pclqName         string
		pcsgMemberLabel  string // empty == standalone
		scaleConfig      *grovecorev1alpha1.AutoScalingConfig
		expectSelectorOK bool
	}{
		{name: "standalone, no ScaleConfig", pclqName: standaloneFQN, expectSelectorOK: true},
		{name: "standalone, with ScaleConfig", pclqName: standaloneFQN, scaleConfig: withScale, expectSelectorOK: true},
		{name: "PCSG member, no ScaleConfig", pclqName: pcsgMemberFQN, pcsgMemberLabel: pcsgName, expectSelectorOK: false},
		// PCSG members are scaled through the PCSG; their own ScaleConfig (if any) must not
		// resurrect the selector and re-expose them as an HPA target.
		{name: "PCSG member, with ScaleConfig", pclqName: pcsgMemberFQN, pcsgMemberLabel: pcsgName, scaleConfig: withScale, expectSelectorOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels := map[string]string{}
			if tt.pcsgMemberLabel != "" {
				labels[apicommon.LabelPodCliqueScalingGroup] = tt.pcsgMemberLabel
			}
			pclq := &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{Name: tt.pclqName, Labels: labels},
				Spec:       grovecorev1alpha1.PodCliqueSpec{ScaleConfig: tt.scaleConfig},
			}

			err := mutateSelector(pcsName, pclq)
			assert.NoError(t, err)
			if tt.expectSelectorOK {
				require.NotNil(t, pclq.Status.Selector, "selector should be populated")
				assert.Contains(t, *pclq.Status.Selector, apicommon.LabelPodClique+"="+pclq.Name)
			} else {
				assert.Nil(t, pclq.Status.Selector, "selector should not be populated for PCSG-member PCLQ")
			}
		})
	}
}

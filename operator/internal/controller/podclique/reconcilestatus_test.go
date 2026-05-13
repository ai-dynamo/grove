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
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			err := mutateUpdatedReplica(nil, tt.pclq, tt.existingPods)
			assert.NoError(t, err)

			// Assert the result
			assert.Equal(t, tt.expectedUpdatedReplicas, tt.pclq.Status.UpdatedReplicas,
				"UpdatedReplicas should match expected value")
		})
	}
}

// TestMutateUpdatedReplicaCountsCanonicalAndLegacyCurrentPodLabels verifies that
// mutateUpdatedReplica treats both the canonical and legacy pod-template-hash
// values as "current" when counting UpdatedReplicas. This protects in-flight
// rollouts during a hash-format migration: pods labeled with either hash variant
// of the current template are counted as updated, while pods with an unrelated
// (stale) hash are not.
func TestMutateUpdatedReplicaCountsCanonicalAndLegacyCurrentPodLabels(t *testing.T) {
	template := &grovecorev1alpha1.PodCliqueTemplateSpec{
		Name: "worker",
		Spec: grovecorev1alpha1.PodCliqueSpec{
			PodSpec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "sidecar", Image: "sidecar:v1"},
					{Name: "main", Image: "main:v1"},
				},
			},
		},
	}
	hashes := componentutils.ComputePCLQPodTemplateHashCandidates(template, "")
	require.NotEqual(t, hashes.Canonical, hashes.Legacy, "test must exercise canonical/legacy divergence")

	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "pcs", Namespace: "default"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{template},
			},
		},
	}
	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pcs-0-worker",
			Namespace: "default",
			Labels: map[string]string{
				apicommon.LabelPartOfKey:                "pcs",
				apicommon.LabelPodCliqueSetReplicaIndex: "0",
			},
		},
		Status: grovecorev1alpha1.PodCliqueStatus{
			CurrentPodTemplateHash: ptr.To(hashes.Canonical),
		},
	}

	err := mutateUpdatedReplica(pcs, pclq, []*corev1.Pod{
		createPodWithHash("pod-canonical", hashes.Canonical),
		createPodWithHash("pod-legacy", hashes.Legacy),
		createPodWithHash("pod-stale", "stale-hash"),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), pclq.Status.UpdatedReplicas)
}

// TestMutateCurrentHashesAdvancesGenerationWhenTemplateHashIsCurrent verifies that
// once a PodClique has fully converged on the current template (Replicas ==
// UpdatedReplicas) and its CurrentPodTemplateHash matches the template — even
// via the legacy hash — mutateCurrentHashes advances both the
// CurrentPodTemplateHash and the CurrentPodCliqueSetGenerationHash to their
// canonical values, completing the migration off of legacy hashes.
func TestMutateCurrentHashesAdvancesGenerationWhenTemplateHashIsCurrent(t *testing.T) {
	pcs, pclq, templateHashes, generationHashes := newPodCliqueHashConvergenceFixture()
	oldGenerationHash := "old-generation-hash"
	pclq.Status.CurrentPodTemplateHash = ptr.To(templateHashes.Legacy)
	pclq.Status.CurrentPodCliqueSetGenerationHash = ptr.To(oldGenerationHash)
	pclq.Status.Replicas = 2
	pclq.Status.UpdatedReplicas = 2

	err := mutateCurrentHashes(logr.Discard(), pcs, pclq)

	require.NoError(t, err)
	assert.Equal(t, templateHashes.Canonical, *pclq.Status.CurrentPodTemplateHash)
	assert.Equal(t, generationHashes.Canonical, *pclq.Status.CurrentPodCliqueSetGenerationHash)
}

// TestMutateCurrentHashesDoesNotAdvanceWhenTemplateHashIsStale verifies that
// mutateCurrentHashes refuses to advance CurrentPodTemplateHash or
// CurrentPodCliqueSetGenerationHash when the PodClique has not actually
// converged on the current PodCliqueSet template, even though the replica
// counts (Replicas == UpdatedReplicas) would superficially suggest it has.
//
// "Stale" here means a hash value that matches neither the canonical nor the
// legacy form of the expected pod-template hash for the current PCS template
// (i.e. it is left over from some prior, no-longer-current template). Because
// convergence is established by checking both the pod-template-hash label on
// the PodClique and Status.CurrentPodTemplateHash against those expected
// canonical/legacy hashes, a stale value in either field must block the
// advance. The two table cases exercise exactly those inputs:
//
//   - "stale label": the PodClique's pod-template-hash label is a stale value
//     while Status.CurrentPodTemplateHash is unset. Convergence must fail on
//     the label check, so neither the template hash nor the generation hash
//     may move to canonical.
//   - "stale current template hash": the label is the current canonical hash
//     (so the label check passes), but Status.CurrentPodTemplateHash already
//     holds a stale value from a prior template. Convergence must fail on the
//     status check, the stale Status.CurrentPodTemplateHash must be preserved
//     as-is (not overwritten with the canonical hash), and the generation
//     hash must not advance.
func TestMutateCurrentHashesDoesNotAdvanceWhenTemplateHashIsStale(t *testing.T) {
	tests := []struct {
		name                    string
		labelPodTemplateHash    string
		currentPodTemplateHash  string
		wantPCSGenerationHash   string
		wantCurrentTemplateHash string
	}{
		{
			name:                    "stale label",
			labelPodTemplateHash:    "stale-template-hash",
			currentPodTemplateHash:  "",
			wantPCSGenerationHash:   "old-generation-hash",
			wantCurrentTemplateHash: "",
		},
		{
			name:                    "stale current template hash",
			labelPodTemplateHash:    "",
			currentPodTemplateHash:  "stale-template-hash",
			wantPCSGenerationHash:   "old-generation-hash",
			wantCurrentTemplateHash: "stale-template-hash",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs, pclq, templateHashes, _ := newPodCliqueHashConvergenceFixture()
			if tt.labelPodTemplateHash == "" {
				tt.labelPodTemplateHash = templateHashes.Canonical
			}
			pclq.Labels[apicommon.LabelPodTemplateHash] = tt.labelPodTemplateHash
			pclq.Status.CurrentPodTemplateHash = ptr.To(tt.currentPodTemplateHash)
			pclq.Status.CurrentPodCliqueSetGenerationHash = ptr.To(tt.wantPCSGenerationHash)
			pclq.Status.Replicas = 2
			pclq.Status.UpdatedReplicas = 2

			err := mutateCurrentHashes(logr.Discard(), pcs, pclq)

			require.NoError(t, err)
			assert.Equal(t, tt.wantCurrentTemplateHash, *pclq.Status.CurrentPodTemplateHash)
			assert.Equal(t, tt.wantPCSGenerationHash, *pclq.Status.CurrentPodCliqueSetGenerationHash)
		})
	}
}

func newPodCliqueHashConvergenceFixture() (*grovecorev1alpha1.PodCliqueSet, *grovecorev1alpha1.PodClique, componentutils.HashCandidates, componentutils.HashCandidates) {
	template := &grovecorev1alpha1.PodCliqueTemplateSpec{
		Name: "worker",
		Spec: grovecorev1alpha1.PodCliqueSpec{
			PodSpec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "main", Image: "main:v1"}},
			},
		},
	}
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "pcs", Namespace: "default"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{template},
			},
		},
	}
	templateHashes := componentutils.ComputePCLQPodTemplateHashCandidates(template, "")
	generationHashes := componentutils.ComputePCSGenerationHashCandidates(pcs)
	pcs.Status.CurrentGenerationHash = ptr.To(generationHashes.Canonical)
	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pcs-0-worker",
			Namespace: "default",
			Labels: map[string]string{
				apicommon.LabelPartOfKey:                "pcs",
				apicommon.LabelPodCliqueSetReplicaIndex: "0",
				apicommon.LabelPodTemplateHash:          templateHashes.Canonical,
			},
		},
	}
	return pcs, pclq, templateHashes, generationHashes
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

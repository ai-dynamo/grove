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

package podclique

import (
	"context"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	internalconstants "github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/utils/podtemplatehash"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
			name: "desired metadata label overrides stale current status",
			pclq: &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						apicommon.LabelPodTemplateHash: "desired-hash",
					},
				},
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress:         nil,
					CurrentPodTemplateHash: ptr.To("stale-hash"),
				},
			},
			existingPods: []*corev1.Pod{
				createPodWithHash("pod-1", "desired-hash"),
				createPodWithHash("pod-2", "stale-hash"),
			},
			expectedUpdatedReplicas: 1,
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

// TestReconcileStatusConvergesWhenReadyPodMatchesDesiredHash covers the live
// latch where PCLQ metadata and the Ready pod already carry the desired
// pod-template hash, but Status.CurrentPodTemplateHash is stale and
// UpdateProgress is absent. UpdatedReplicas must be derived from the desired
// hash first so CurrentPodTemplateHash can advance in the same status pass.
func TestReconcileStatusConvergesWhenReadyPodMatchesDesiredHash(t *testing.T) {
	pcs, pclq, templateHash := newPodCliqueHashConvergenceFixture(t)
	pclq.UID = types.UID("pclq-uid")
	pclq.Generation = 2
	pclq.Spec = grovecorev1alpha1.PodCliqueSpec{
		Replicas:     1,
		MinAvailable: ptr.To[int32](1),
	}
	pclq.Status = grovecorev1alpha1.PodCliqueStatus{
		Replicas:                          1,
		ReadyReplicas:                     1,
		ScheduledReplicas:                 1,
		UpdatedReplicas:                   0,
		ObservedGeneration:                ptr.To[int64](2),
		CurrentPodTemplateHash:            ptr.To("stale-template-hash"),
		CurrentPodCliqueSetGenerationHash: ptr.To("old-generation-hash"),
	}
	pod := createReadyOwnedPodWithHash("ready-current-pod", pclq, templateHash)

	revision := testutils.NewPodCliqueSetControllerRevision(pcs, map[string]string{"worker": templateHash})
	cl := testutils.SetupFakeClient(pcs, pclq, pod, revision)
	r := &Reconciler{
		client:        cl,
		eventRecorder: record.NewFakeRecorder(1),
	}

	result := r.reconcileStatus(context.Background(), logr.Discard(), pclq)

	_, err := result.Result()
	require.NoError(t, err)
	updatedPCLQ := &grovecorev1alpha1.PodClique{}
	require.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: pclq.Name, Namespace: pclq.Namespace}, updatedPCLQ))
	assert.Equal(t, int32(1), updatedPCLQ.Status.UpdatedReplicas)
	assert.Equal(t, templateHash, *updatedPCLQ.Status.CurrentPodTemplateHash)
	assert.Equal(t, *pcs.Status.CurrentGenerationHash, *updatedPCLQ.Status.CurrentPodCliqueSetGenerationHash)
}

// TestMutateCurrentHashesDoesNotAdvanceWhenTemplateHashIsStale verifies that
// mutateCurrentHashes refuses to advance CurrentPodTemplateHash or
// CurrentPodCliqueSetGenerationHash when the PodClique metadata label has not
// actually converged on the current PodCliqueSet template, even though the
// replica counts (Replicas == UpdatedReplicas) would superficially suggest it has.
func TestMutateCurrentHashesDoesNotAdvanceWhenTemplateHashIsStale(t *testing.T) {
	pcs, pclq, templateHash := newPodCliqueHashConvergenceFixture(t)
	pclq.Labels[apicommon.LabelPodTemplateHash] = "stale-template-hash"
	pclq.Status.CurrentPodTemplateHash = ptr.To("")
	pclq.Status.CurrentPodCliqueSetGenerationHash = ptr.To("old-generation-hash")
	pclq.Status.Replicas = 2
	pclq.Status.UpdatedReplicas = 2

	selectedRevision := testutils.NewSelectedRevision(*pcs.Status.CurrentGenerationHash, map[string]string{"worker": templateHash})
	err := mutateCurrentHashes(logr.Discard(), selectedRevision, pclq)

	require.NoError(t, err)
	assert.Equal(t, "", *pclq.Status.CurrentPodTemplateHash)
	assert.Equal(t, "old-generation-hash", *pclq.Status.CurrentPodCliqueSetGenerationHash)
}

func newPodCliqueHashConvergenceFixture(t *testing.T) (*grovecorev1alpha1.PodCliqueSet, *grovecorev1alpha1.PodClique, string) {
	t.Helper()
	template := &grovecorev1alpha1.PodCliqueTemplateSpec{
		Name: "worker",
		Spec: grovecorev1alpha1.PodCliqueSpec{
			PodSpec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "main", Image: "main:v1"}},
			},
		},
	}
	generationHash := "current-generation-hash"
	templateHash := podtemplatehash.ComputePodClique(template, "")
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "pcs", Namespace: "default"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{template},
			},
		},
		Status: grovecorev1alpha1.PodCliqueSetStatus{
			CurrentGenerationHash: ptr.To(generationHash),
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
	}
	pclq.Labels[apicommon.LabelPodTemplateHash] = templateHash
	return pcs, pclq, templateHash
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

func createReadyOwnedPodWithHash(name string, owner *grovecorev1alpha1.PodClique, templateHash string) *corev1.Pod {
	pod := createPodWithHash(name, templateHash)
	pod.Namespace = owner.Namespace
	pod.Labels[apicommon.LabelPodClique] = owner.Name
	pod.OwnerReferences = []metav1.OwnerReference{
		*metav1.NewControllerRef(owner, grovecorev1alpha1.SchemeGroupVersion.WithKind("PodClique")),
	}
	pod.Status.Conditions = []corev1.PodCondition{
		{
			Type:   corev1.PodScheduled,
			Status: corev1.ConditionTrue,
		},
		{
			Type:   corev1.PodReady,
			Status: corev1.ConditionTrue,
		},
	}
	return pod
}

// TestEmitAllScheduledReplicasLostIfNeeded covers the only explicit signal users have when a
// previously-running PodClique loses every scheduled pod. Gang termination is suppressed in
// that state, so this event must fire on the non-zero to zero transition (and only on that
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
// behaviour where MinAvailableBreached must flip True whenever scheduledReplicas
// drops below MinAvailable. Both partial regression (0 < scheduled < min) and
// full regression to zero produce a breach; TerminationDelay is the natural
// debounce against transient startup flicker.
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
			// scheduled == 0 also breaches now. Gang termination is armed; the
			// downstream TerminationDelay (default 4h) gives the cluster a
			// window to schedule before the workload is recycled.
			name: "previously-healthy PCLQ loses all scheduled pods — breaches",
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
			wantStatus: metav1.ConditionTrue,
			wantReason: constants.ConditionReasonScheduledReplicasBelowMinAvailable,
		},
		{
			// A fresh PCLQ that has not yet scheduled any pods still breaches
			// under the always-breach rule. TerminationDelay (4h) is the grace
			// window: if pods schedule in time the breach resolves before any
			// termination action.
			name: "fresh PCLQ never scheduled — also breaches (TerminationDelay is the grace)",
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
			wantStatus: metav1.ConditionTrue,
			wantReason: constants.ConditionReasonScheduledReplicasBelowMinAvailable,
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

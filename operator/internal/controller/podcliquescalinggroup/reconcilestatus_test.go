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
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	internalconstants "github.com/ai-dynamo/grove/operator/internal/constants"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestComputeReplicaStatus(t *testing.T) {
	tests := []struct {
		name              string
		expectedSize      int
		cliques           []grovecorev1alpha1.PodClique
		minAvailable      int32
		pcsGenerationHash *string
		wantScheduled     bool
		wantAvailable     bool
		wantUpdated       bool
	}{
		{
			name:          "healthy vs failed states",
			expectedSize:  2,
			cliques:       []grovecorev1alpha1.PodClique{buildHealthyClique("frontend"), buildFailedClique("backend")},
			minAvailable:  1,
			wantScheduled: false,
			wantAvailable: false,
			wantUpdated:   false,
		},
		{
			name:          "incomplete replica counting",
			expectedSize:  3,
			cliques:       []grovecorev1alpha1.PodClique{buildHealthyClique("frontend")},
			minAvailable:  1,
			wantScheduled: false,
			wantAvailable: false,
			wantUpdated:   false,
		},
		{
			name:          "scheduled but unavailable",
			expectedSize:  2,
			cliques:       []grovecorev1alpha1.PodClique{buildHealthyClique("frontend"), buildScheduledClique("backend")},
			minAvailable:  1,
			wantScheduled: true,
			wantAvailable: false,
			wantUpdated:   false,
		},
		{
			name:          "terminating clique filtering",
			expectedSize:  2,
			cliques:       []grovecorev1alpha1.PodClique{buildHealthyClique("frontend"), buildHealthyClique("backend"), buildTerminatingClique("old"), buildTerminatingClique("terminated")},
			minAvailable:  1,
			wantScheduled: true,
			wantAvailable: true,
		},
		{
			name:          "available",
			expectedSize:  2,
			cliques:       []grovecorev1alpha1.PodClique{buildHealthyClique("frontend"), buildHealthyClique("backend")},
			minAvailable:  2,
			wantScheduled: true,
			wantAvailable: true,
			wantUpdated:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheduled, available, updated := computeReplicaStatus(logr.Discard(), componentutils.HashCandidates{}, tt.pcsGenerationHash, "0", tt.expectedSize, tt.cliques)

			assert.Equal(t, tt.wantScheduled, scheduled, "scheduled mismatch")
			assert.Equal(t, tt.wantAvailable, available, "available mismatch")
			assert.Equal(t, tt.wantUpdated, updated, "updated mismatch")
		})
	}
}

func TestComputeMinAvailableBreachedCondition(t *testing.T) {
	tests := []struct {
		name         string
		replicas     int32
		minAvailable *int32
		scheduled    int32
		available    int32
		pclqsMap     map[string][]grovecorev1alpha1.PodClique
		wantStatus   metav1.ConditionStatus
		wantReason   string
	}{
		{
			name:       "sufficient replicas",
			replicas:   3,
			scheduled:  3,
			available:  3,
			pclqsMap:   make(map[string][]grovecorev1alpha1.PodClique),
			wantStatus: metav1.ConditionFalse,
			wantReason: "SufficientAvailablePodCliqueScalingGroupReplicas",
		},
		{
			name:         "custom minAvailable met",
			replicas:     5,
			minAvailable: ptr.To(int32(2)),
			scheduled:    3,
			available:    3,
			pclqsMap:     make(map[string][]grovecorev1alpha1.PodClique),
			wantStatus:   metav1.ConditionFalse,
			wantReason:   "SufficientAvailablePodCliqueScalingGroupReplicas",
		},
		{
			// 0 < scheduled < MinAvailable is structurally unreachable from initial
			// startup under gang scheduling, so it is treated as a regression and
			// breaches MinAvailable.
			name:         "partially scheduled regression breaches",
			replicas:     3,
			minAvailable: ptr.To(int32(2)),
			scheduled:    1,
			available:    1,
			pclqsMap:     make(map[string][]grovecorev1alpha1.PodClique),
			wantStatus:   metav1.ConditionTrue,
			wantReason:   "ScheduledReplicasBelowMinAvailable",
		},
		{
			// scheduled == 0 stays suppressed: gang termination would only recreate
			// the same Pending replicas against the same cluster state.
			name:         "zero scheduled does not breach",
			replicas:     3,
			minAvailable: ptr.To(int32(2)),
			scheduled:    0,
			available:    0,
			pclqsMap:     make(map[string][]grovecorev1alpha1.PodClique),
			wantStatus:   metav1.ConditionFalse,
			wantReason:   "InsufficientScheduledPodCliqueScalingGroupReplicas",
		},
		{
			name:         "insufficient available",
			replicas:     3,
			minAvailable: ptr.To(int32(2)),
			scheduled:    2,
			available:    1,
			pclqsMap: map[string][]grovecorev1alpha1.PodClique{
				"0": {
					{
						Status: grovecorev1alpha1.PodCliqueStatus{
							Conditions: []metav1.Condition{
								{
									Type:   constants.ConditionTypeMinAvailableBreached,
									Status: metav1.ConditionTrue,
								},
							},
						},
					},
				},
			},
			wantStatus: metav1.ConditionTrue,
			wantReason: "InsufficientAvailablePodCliqueScalingGroupReplicas",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			minAvailable := tt.minAvailable
			if minAvailable == nil {
				minAvailable = &tt.replicas
			}
			pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					Replicas:     tt.replicas,
					MinAvailable: minAvailable,
				},
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					ScheduledReplicas: tt.scheduled,
					AvailableReplicas: tt.available,
				},
			}

			condition := computeMinAvailableBreachedCondition(logr.Discard(), pcsg, tt.pclqsMap)

			assert.Equal(t, "MinAvailableBreached", condition.Type)
			assert.Equal(t, tt.wantStatus, condition.Status)
			assert.Equal(t, tt.wantReason, condition.Reason)
		})
	}
}

// TestEmitAllScheduledReplicasLostIfNeeded covers the only explicit signal users have when a
// previously-running PodCliqueScalingGroup loses every scheduled replica. Gang termination is
// suppressed in that state, so this event must fire on the non-zero → zero transition (and
// only on that transition) for the regression to remain observable.
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
			pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{ScheduledReplicas: tt.nowScheduled},
			}

			r.emitAllScheduledReplicasLostIfNeeded(pcsg, tt.originalScheduled)

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
// regression-after-healthy-state behaviour at the PodCliqueScalingGroup level.
// See the PodClique-level test for the full rationale.
//
// Several cases populate pclqsMap with breached PCLQs. That input is unread by
// the under-scheduled branches today, but we keep it populated so that any
// future refactor that drops the short-circuit and starts consulting pclqsMap
// must continue to satisfy the assertions.
func TestComputeMinAvailableBreachedConditionPartialScheduleRegression(t *testing.T) {
	pastTransition := metav1.NewTime(time.Now().Add(-10 * time.Minute))

	// breachedPCLQReplica is a single PCSG replica whose constituent PodCliques
	// already report MinAvailableBreached=True. If a refactor accidentally
	// consulted pclqsMap on the under-scheduled paths, these would inflate the
	// breached count and could flip the answer — the assertions below must
	// remain correct in that hypothetical world.
	breachedPCLQReplica := map[string][]grovecorev1alpha1.PodClique{
		"0": {{Status: grovecorev1alpha1.PodCliqueStatus{
			Conditions: []metav1.Condition{{Type: constants.ConditionTypeMinAvailableBreached, Status: metav1.ConditionTrue}},
		}}},
	}

	tests := []struct {
		name           string
		replicas       int32
		minAvailable   *int32
		scheduled      int32
		available      int32
		updateProgress *grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress
		conditions     []metav1.Condition
		pclqsMap       map[string][]grovecorev1alpha1.PodClique
		wantStatus     metav1.ConditionStatus
		wantReason     string
	}{
		{
			name:         "0 < scheduled < MinAvailable breaches",
			replicas:     3,
			minAvailable: ptr.To(int32(3)),
			scheduled:    1,
			available:    1,
			conditions: []metav1.Condition{{
				Type:               constants.ConditionTypeMinAvailableBreached,
				Status:             metav1.ConditionFalse,
				Reason:             constants.ConditionReasonSufficientAvailablePCSGReplicas,
				LastTransitionTime: pastTransition,
			}},
			pclqsMap:   breachedPCLQReplica,
			wantStatus: metav1.ConditionTrue,
			wantReason: constants.ConditionReasonScheduledReplicasBelowMinAvailable,
		},
		{
			// Sanity-pin: scheduled == 0 must NOT breach, even with PCLQs in the
			// map already breached. The short-circuit on scheduled==0 has to
			// take precedence over PCLQ-level breach signal — otherwise a stale
			// PCLQ condition would push us into a churn loop.
			name:         "scheduled == 0 with breached PCLQs in map — must NOT breach",
			replicas:     2,
			minAvailable: ptr.To(int32(2)),
			scheduled:    0,
			available:    0,
			conditions: []metav1.Condition{{
				Type:               constants.ConditionTypeMinAvailableBreached,
				Status:             metav1.ConditionFalse,
				Reason:             constants.ConditionReasonSufficientAvailablePCSGReplicas,
				LastTransitionTime: pastTransition,
			}},
			pclqsMap:   breachedPCLQReplica,
			wantStatus: metav1.ConditionFalse,
			wantReason: constants.ConditionReasonInsufficientScheduledPCSGReplicas,
		},
		{
			// A fresh PCSG that has never scheduled any replica must NOT be
			// reported as breached.
			name:         "fresh PCSG never scheduled — must not breach",
			replicas:     3,
			minAvailable: ptr.To(int32(3)),
			scheduled:    0,
			available:    0,
			pclqsMap:     map[string][]grovecorev1alpha1.PodClique{},
			wantStatus:   metav1.ConditionFalse,
			wantReason:   constants.ConditionReasonInsufficientScheduledPCSGReplicas,
		},
		{
			// Pin branch ordering: when an update is in progress, the
			// IsPCSGUpdateInProgress short-circuit must win over the regression
			// breach branch. Otherwise a rolling update across a node cordon
			// would falsely flip the breach to True and risk silent churn.
			name:         "update in progress overrides partial-schedule breach",
			replicas:     3,
			minAvailable: ptr.To(int32(3)),
			scheduled:    1,
			available:    1,
			updateProgress: &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{
				UpdateStartedAt:            pastTransition,
				UpdateEndedAt:              nil, // active update
				PodCliqueSetGenerationHash: "gen-hash-1",
			},
			pclqsMap:   breachedPCLQReplica,
			wantStatus: metav1.ConditionUnknown,
			wantReason: constants.ConditionReasonUpdateInProgress,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					Replicas:     tt.replicas,
					MinAvailable: tt.minAvailable,
				},
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					ScheduledReplicas: tt.scheduled,
					AvailableReplicas: tt.available,
					Conditions:        tt.conditions,
					UpdateProgress:    tt.updateProgress,
				},
			}

			condition := computeMinAvailableBreachedCondition(logr.Discard(), pcsg, tt.pclqsMap)

			assert.Equal(t, constants.ConditionTypeMinAvailableBreached, condition.Type)
			assert.Equal(t, tt.wantStatus, condition.Status, "MinAvailableBreached status mismatch")
			assert.Equal(t, tt.wantReason, condition.Reason, "MinAvailableBreached reason mismatch")
		})
	}
}

func TestGetPodCliquesPerPCSGReplica(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name         string
		objects      []client.Object
		wantReplicas int
	}{
		{
			name: "find expected cliques",
			objects: []client.Object{
				testutils.NewPCSGPodCliqueBuilder("test-pcs-0-frontend-0", "test-ns", "test-pcs", "test-pcsg", 0, 0).
					WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").Build(),
				testutils.NewPCSGPodCliqueBuilder("test-pcs-0-backend-0", "test-ns", "test-pcs", "test-pcsg", 0, 0).
					WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").Build(),
				testutils.NewPCSGPodCliqueBuilder("test-pcs-0-frontend-1", "test-ns", "test-pcs", "test-pcsg", 0, 1).
					WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").Build(),
			},
			wantReplicas: 2,
		},
		{
			name:         "no cliques found",
			objects:      []client.Object{},
			wantReplicas: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := testutils.SetupFakeClient(tt.objects...)
			reconciler := &Reconciler{client: fakeClient}
			objKey := client.ObjectKey{Name: "test-pcsg", Namespace: "test-ns"}

			result, err := reconciler.getPodCliquesPerPCSGReplica(ctx, "test-pcs", objKey)

			require.NoError(t, err)
			assert.Len(t, result, tt.wantReplicas)
		})
	}
}

func TestReconcileStatus(t *testing.T) {
	ctx := context.Background()
	pcsGenerationHash := string(uuid.NewUUID())

	tests := []struct {
		name          string
		setup         func() (*grovecorev1alpha1.PodCliqueScalingGroup, *grovecorev1alpha1.PodCliqueSet, []client.Object)
		wantScheduled int32
		wantAvailable int32
		wantUpdated   int32
		wantBreached  bool
	}{
		{
			name: "happy path",
			setup: func() (*grovecorev1alpha1.PodCliqueScalingGroup, *grovecorev1alpha1.PodCliqueSet, []client.Object) {
				pcsg := testutils.NewPodCliqueScalingGroupBuilder("test-pcsg", "test-ns", "test-pcs", 0).
					WithReplicas(2).
					WithCliqueNames([]string{"frontend", "backend"}).
					WithOptions(testutils.WithPCSGObservedGeneration(1)).Build()
				pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).WithPodCliqueSetGenerationHash(&pcsGenerationHash).Build()
				cliques := []client.Object{
					testutils.NewPCSGPodCliqueBuilder("test-pcsg-0-frontend", "test-ns", "test-pcs", "test-pcsg", 0, 0).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPCSGPodCliqueBuilder("test-pcsg-0-backend", "test-ns", "test-pcs", "test-pcsg", 0, 0).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPCSGPodCliqueBuilder("test-pcsg-1-frontend", "test-ns", "test-pcs", "test-pcsg", 0, 1).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPCSGPodCliqueBuilder("test-pcsg-1-backend", "test-ns", "test-pcs", "test-pcsg", 0, 1).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
				}
				return pcsg, pcs, cliques
			},
			wantScheduled: 2,
			wantAvailable: 2,
			wantUpdated:   2,
			wantBreached:  false,
		},
		{
			name: "mixed replica states",
			setup: func() (*grovecorev1alpha1.PodCliqueScalingGroup, *grovecorev1alpha1.PodCliqueSet, []client.Object) {
				pcsg := testutils.NewPodCliqueScalingGroupBuilder("test-pcsg", "test-ns", "test-pcs", 0).
					WithReplicas(3).
					WithCliqueNames([]string{"worker"}).
					WithMinAvailable(2).
					WithOptions(testutils.WithPCSGObservedGeneration(1)).Build()
				pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).WithPodCliqueSetGenerationHash(&pcsGenerationHash).Build()
				cliques := []client.Object{
					testutils.NewPCSGPodCliqueBuilder("test-pcsg-0-worker", "test-ns", "test-pcs", "test-pcsg", 0, 0).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPCSGPodCliqueBuilder("test-pcsg-1-worker", "test-ns", "test-pcs", "test-pcsg", 0, 1).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledButBreached(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPCSGPodCliqueBuilder("test-pcsg-2-worker", "test-ns", "test-pcs", "test-pcsg", 0, 2).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQNotScheduled(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
				}
				return pcsg, pcs, cliques
			},
			wantScheduled: 2,
			wantAvailable: 1,
			wantUpdated:   1,
			wantBreached:  true,
		},
		{
			name: "with terminating cliques",
			setup: func() (*grovecorev1alpha1.PodCliqueScalingGroup, *grovecorev1alpha1.PodCliqueSet, []client.Object) {
				pcsg := testutils.NewPodCliqueScalingGroupBuilder("test-pcsg", "test-ns", "test-pcs", 0).
					WithReplicas(2).
					WithCliqueNames([]string{"frontend", "backend"}).
					WithOptions(testutils.WithPCSGObservedGeneration(1)).Build()
				pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).WithPodCliqueSetGenerationHash(&pcsGenerationHash).Build()
				cliques := []client.Object{
					// Replica 0: healthy
					testutils.NewPCSGPodCliqueBuilder("test-pcsg-0-frontend", "test-ns", "test-pcs", "test-pcsg", 0, 0).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPCSGPodCliqueBuilder("test-pcsg-0-backend", "test-ns", "test-pcs", "test-pcsg", 0, 0).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					// Replica 1: has one terminating clique
					testutils.NewPCSGPodCliqueBuilder("test-pcsg-1-frontend", "test-ns", "test-pcs", "test-pcsg", 0, 1).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPCSGPodCliqueBuilder("test-pcsg-1-backend", "test-ns", "test-pcs", "test-pcsg", 0, 1).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQTerminating(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
				}
				return pcsg, pcs, cliques
			},
			wantAvailable: 1,     // only replica 0 has all non-terminated cliques
			wantScheduled: 1,     // only replica 0 has sufficient non-terminated cliques
			wantUpdated:   1,     // only nonterminating cliques will count against updated
			wantBreached:  false, // 1 >= 1 (default minAvailable)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcsg, pcs, cliques := tt.setup()
			allObjects := append([]client.Object{pcsg, pcs}, cliques...)
			fakeClient := testutils.SetupFakeClient(allObjects...)
			reconciler := &Reconciler{client: fakeClient}

			result := reconciler.reconcileStatus(ctx, logr.Discard(), client.ObjectKeyFromObject(pcsg))

			require.False(t, result.HasErrors())
			assert.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcsg), pcsg), "fake client object fetch failed")
			assert.Equal(t, tt.wantScheduled, pcsg.Status.ScheduledReplicas)
			assert.Equal(t, tt.wantAvailable, pcsg.Status.AvailableReplicas)
			assert.Equal(t, tt.wantUpdated, pcsg.Status.UpdatedReplicas)

			if pcsg.Status.ObservedGeneration != nil {
				assertCondition(t, pcsg, tt.wantBreached)
			}
		})
	}
}

func TestReconcileStatus_EdgeCases(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		pcsg *grovecorev1alpha1.PodCliqueScalingGroup
	}{
		{
			name: "zero replicas",
			pcsg: testutils.NewPodCliqueScalingGroupBuilder("test-pcsg", "test-ns", "test-pcs", 0).
				WithReplicas(0).
				WithOptions(testutils.WithPCSGObservedGeneration(1)).Build(),
		},
		{
			name: "empty clique names",
			pcsg: testutils.NewPodCliqueScalingGroupBuilder("test-pcsg", "test-ns", "test-pcs", 0).
				WithReplicas(1).
				WithCliqueNames([]string{}).
				WithOptions(testutils.WithPCSGObservedGeneration(1)).Build(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).Build()
			fakeClient := testutils.SetupFakeClient(tt.pcsg, pcs)
			reconciler := &Reconciler{client: fakeClient}

			result := reconciler.reconcileStatus(ctx, logr.Discard(), client.ObjectKeyFromObject(tt.pcsg))

			assert.False(t, result.HasErrors())
		})
	}
}

// TestMutateCurrentPodCliqueSetGenerationHashWaitsForPodCliqueGenerationConvergence verifies that
// the PCSG's CurrentPodCliqueSetGenerationHash is only advanced to the canonical PCS hash once all
// of its PodCliques report having converged to that hash. While any child PodClique still reports
// the old generation hash, the PCSG must continue to surface the old hash to avoid prematurely
// signaling that a rollout has completed.
func TestMutateCurrentPodCliqueSetGenerationHashWaitsForPodCliqueGenerationConvergence(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
		WithScalingGroupConfig("compute", []string{"worker"}, 2, 1).
		Build()
	pcsGenerationHashes := componentutils.ComputePCSGenerationHashCandidates(pcs)
	pcs.Status.CurrentGenerationHash = ptr.To(pcsGenerationHashes.Canonical)

	pcsg := testutils.NewPodCliqueScalingGroupBuilder("test-pcs-0-compute", "test-ns", "test-pcs", 0).
		WithReplicas(2).
		WithCliqueNames([]string{"worker"}).
		WithOptions(testutils.WithPCSGCurrentPCSGenerationHash("old-generation-hash")).
		Build()
	expectedTemplateHashes := componentutils.GetPCLQTemplateHashCandidates(pcs, pcsg)
	pclqs := []grovecorev1alpha1.PodClique{
		buildConvergedPCSGPodClique(t, pcsg, "worker", 0, expectedTemplateHashes, pcsGenerationHashes.Canonical),
		buildConvergedPCSGPodClique(t, pcsg, "worker", 1, expectedTemplateHashes, "old-generation-hash"),
	}

	mutateCurrentPodCliqueSetGenerationHash(logr.Discard(), pcs, pcsg, pclqs)
	require.NotNil(t, pcsg.Status.CurrentPodCliqueSetGenerationHash)
	assert.Equal(t, "old-generation-hash", *pcsg.Status.CurrentPodCliqueSetGenerationHash)

	pclqs[1].Status.CurrentPodCliqueSetGenerationHash = ptr.To(pcsGenerationHashes.Canonical)
	mutateCurrentPodCliqueSetGenerationHash(logr.Discard(), pcs, pcsg, pclqs)
	require.NotNil(t, pcsg.Status.CurrentPodCliqueSetGenerationHash)
	assert.Equal(t, pcsGenerationHashes.Canonical, *pcsg.Status.CurrentPodCliqueSetGenerationHash)
}

// buildConvergedPCSGPodClique constructs a PodClique that is fully converged on the given
// template hash, with its CurrentPodCliqueSetGenerationHash set to pcsGenerationHash so callers
// can simulate cliques at a specific PCS generation.
func buildConvergedPCSGPodClique(
	t *testing.T,
	pcsg *grovecorev1alpha1.PodCliqueScalingGroup,
	cliqueName string,
	pcsgReplicaIndex int,
	expectedTemplateHashes map[string]componentutils.HashCandidates,
	pcsGenerationHash string,
) grovecorev1alpha1.PodClique {
	t.Helper()
	pclqName := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsg.Name, Replica: pcsgReplicaIndex}, cliqueName)
	templateHashes, ok := expectedTemplateHashes[pclqName]
	require.True(t, ok, "expected template hash for %s", pclqName)
	pclq := testutils.NewPCSGPodCliqueBuilder(pclqName, pcsg.Namespace, "test-pcs", pcsg.Name, 0, pcsgReplicaIndex).
		WithLabels(map[string]string{apicommon.LabelPodTemplateHash: templateHashes.Canonical}).
		Build()
	pclq.Status.CurrentPodTemplateHash = ptr.To(templateHashes.Canonical)
	pclq.Status.CurrentPodCliqueSetGenerationHash = ptr.To(pcsGenerationHash)
	return *pclq
}

// Test helpers
func buildHealthyClique(name string) grovecorev1alpha1.PodClique {
	return *testutils.NewPodCliqueBuilder("test-pcs", uuid.NewUUID(), name, "test-ns", 0).
		WithOptions(testutils.WithPCLQScheduledAndAvailable()).Build()
}

func buildScheduledClique(name string) grovecorev1alpha1.PodClique {
	return *testutils.NewPodCliqueBuilder("test-pcs", uuid.NewUUID(), name, "test-ns", 0).
		WithOptions(testutils.WithPCLQScheduledButBreached()).Build()
}

func buildFailedClique(name string) grovecorev1alpha1.PodClique {
	return *testutils.NewPodCliqueBuilder("test-pcs", uuid.NewUUID(), name, "test-ns", 0).
		WithOptions(testutils.WithPCLQNotScheduled()).Build()
}

func buildTerminatingClique(name string) grovecorev1alpha1.PodClique {
	return *testutils.NewPodCliqueBuilder("test-pcs", uuid.NewUUID(), name, "test-ns", 0).
		WithOptions(testutils.WithPCLQTerminating()).Build()
}

// TestPCSGMutateReplicasWritesUpdateProgressCounts asserts that mutateReplicas only writes the
// new bounded count fields when UpdateProgress is non-nil. The counts are derived from the
// child PCLQs already loaded into pclqsPerPCSGReplica (no extra API calls).
func TestPCSGMutateReplicasWritesUpdateProgressCounts(t *testing.T) {
	pcsHash := "gen-hash-current"
	otherHash := "gen-hash-old"

	matchingPCLQ := func(name string) grovecorev1alpha1.PodClique {
		return *testutils.NewPodCliqueBuilder("test-pcs", uuid.NewUUID(), name, "test-ns", 0).
			WithOptions(testutils.WithPCLQCurrentPCSGenerationHash(pcsHash)).Build()
	}
	staleHashPCLQ := func(name string) grovecorev1alpha1.PodClique {
		return *testutils.NewPodCliqueBuilder("test-pcs", uuid.NewUUID(), name, "test-ns", 0).
			WithOptions(testutils.WithPCLQCurrentPCSGenerationHash(otherHash)).Build()
	}
	terminatingMatchingPCLQ := func(name string) grovecorev1alpha1.PodClique {
		return *testutils.NewPodCliqueBuilder("test-pcs", uuid.NewUUID(), name, "test-ns", 0).
			WithOptions(testutils.WithPCLQCurrentPCSGenerationHash(pcsHash), testutils.WithPCLQTerminating()).Build()
	}

	build := func(replicas int32, withProgress bool) *grovecorev1alpha1.PodCliqueScalingGroup {
		pcsg := testutils.NewPodCliqueScalingGroupBuilder("test-pcsg", "test-ns", "test-pcs", 0).
			WithReplicas(replicas).
			WithCliqueNames([]string{"frontend", "backend"}).Build()
		if withProgress {
			pcsg.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{
				UpdateStartedAt:            metav1.Now(),
				PodCliqueSetGenerationHash: pcsHash,
			}
		}
		return pcsg
	}

	tests := []struct {
		name             string
		pcsg             *grovecorev1alpha1.PodCliqueScalingGroup
		pclqsPerReplica  map[string][]grovecorev1alpha1.PodClique
		wantWritten      bool
		wantUpdatedCount int32
		wantTotalCount   int32
	}{
		{
			name:            "UpdateProgress nil — counts not written, no panic",
			pcsg:            build(2, false),
			pclqsPerReplica: map[string][]grovecorev1alpha1.PodClique{"0": {matchingPCLQ("frontend"), matchingPCLQ("backend")}},
			wantWritten:     false,
		},
		{
			name: "UpdateProgress set, all PCLQs at current hash → updated == total",
			pcsg: build(2, true),
			pclqsPerReplica: map[string][]grovecorev1alpha1.PodClique{
				"0": {matchingPCLQ("frontend"), matchingPCLQ("backend")},
				"1": {matchingPCLQ("frontend"), matchingPCLQ("backend")},
			},
			wantWritten:      true,
			wantUpdatedCount: 4,
			wantTotalCount:   4, // replicas (2) * cliqueNames (2)
		},
		{
			name: "UpdateProgress set, mixed hashes → partial updated count",
			pcsg: build(2, true),
			pclqsPerReplica: map[string][]grovecorev1alpha1.PodClique{
				"0": {matchingPCLQ("frontend"), staleHashPCLQ("backend")},
				"1": {matchingPCLQ("frontend"), matchingPCLQ("backend")},
			},
			wantWritten:      true,
			wantUpdatedCount: 3,
			wantTotalCount:   4,
		},
		{
			name: "UpdateProgress set, terminating matching PCLQ excluded from updated count",
			pcsg: build(2, true),
			pclqsPerReplica: map[string][]grovecorev1alpha1.PodClique{
				"0": {matchingPCLQ("frontend"), terminatingMatchingPCLQ("backend")},
				"1": {matchingPCLQ("frontend"), matchingPCLQ("backend")},
			},
			wantWritten:      true,
			wantUpdatedCount: 3, // terminating excluded even though hash matches
			wantTotalCount:   4, // total derives from spec, unaffected by terminating children
		},
		{
			name:             "UpdateProgress set, zero replicas → counts are 0/0",
			pcsg:             build(0, true),
			pclqsPerReplica:  map[string][]grovecorev1alpha1.PodClique{},
			wantWritten:      true,
			wantUpdatedCount: 0,
			wantTotalCount:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs := &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					CurrentGenerationHash: &pcsHash,
				},
			}
			mutateReplicas(logr.Discard(), pcs, tt.pcsg, tt.pclqsPerReplica)

			if !tt.wantWritten {
				require.Nil(t, tt.pcsg.Status.UpdateProgress, "UpdateProgress must remain nil")
				return
			}
			require.NotNil(t, tt.pcsg.Status.UpdateProgress)
			assert.Equal(t, tt.wantUpdatedCount, tt.pcsg.Status.UpdateProgress.UpdatedPodCliquesCount)
			assert.Equal(t, tt.wantTotalCount, tt.pcsg.Status.UpdateProgress.TotalPodCliquesCount)
		})
	}
}

func TestCountPCSGReplicaUpdatedPCLQs(t *testing.T) {
	hash := "h"
	otherHash := "old"
	mk := func(currHash *string, terminating bool) grovecorev1alpha1.PodClique {
		var p grovecorev1alpha1.PodClique
		p.Status.CurrentPodCliqueSetGenerationHash = currHash
		if terminating {
			now := metav1.NewTime(time.Now())
			p.DeletionTimestamp = &now
			p.Finalizers = []string{"f"}
		}
		return p
	}

	tests := []struct {
		name string
		hash *string
		in   []grovecorev1alpha1.PodClique
		want int32
	}{
		{"nil parent hash → 0", nil, []grovecorev1alpha1.PodClique{mk(&hash, false)}, 0},
		{"empty input → 0", &hash, nil, 0},
		{"all matching", &hash, []grovecorev1alpha1.PodClique{mk(&hash, false), mk(&hash, false)}, 2},
		{"mixed", &hash, []grovecorev1alpha1.PodClique{mk(&hash, false), mk(&otherHash, false), mk(nil, false)}, 1},
		{"terminating matching excluded", &hash, []grovecorev1alpha1.PodClique{mk(&hash, false), mk(&hash, true)}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, countPCSGReplicaUpdatedPCLQs(componentutils.HashCandidates{}, tt.hash, tt.in))
		})
	}
}

// TestPruneStrayPCSGPCLQs covers the two stray-child cases that would otherwise inflate
// downstream counters: replica indexes outside [0, Spec.Replicas) (scale-down leftovers) and
// PCLQ FQNs that are not produced by Spec.CliqueNames at the kept indexes (post clique-name change).
func TestPruneStrayPCSGPCLQs(t *testing.T) {
	pcsg := testutils.NewPodCliqueScalingGroupBuilder("test-pcsg", "test-ns", "test-pcs", 0).
		WithReplicas(2).
		WithCliqueNames([]string{"frontend", "backend"}).Build()

	mkPCLQ := func(name string) grovecorev1alpha1.PodClique {
		return grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}

	in := map[string][]grovecorev1alpha1.PodClique{
		// expected children for replicas 0 and 1
		"0": {mkPCLQ("test-pcsg-0-frontend"), mkPCLQ("test-pcsg-0-backend")},
		"1": {mkPCLQ("test-pcsg-1-frontend"), mkPCLQ("test-pcsg-1-backend")},
		// stale-index leftover from a prior Spec.Replicas=3 (scale-down case)
		"2": {mkPCLQ("test-pcsg-2-frontend"), mkPCLQ("test-pcsg-2-backend")},
	}
	// stray name within an expected index (post clique-name change)
	in["1"] = append(in["1"], mkPCLQ("test-pcsg-1-removed-clique"))

	out := pruneStrayPCSGPCLQs(pcsg, in)

	// Stale-index bucket must be dropped entirely.
	_, hasStaleIdx := out["2"]
	assert.False(t, hasStaleIdx, "replica index 2 is outside Spec.Replicas and must be dropped")

	// Expected indexes retain only spec-derived FQNs.
	require.Len(t, out["0"], 2)
	require.Len(t, out["1"], 2, "stray-named PCLQ at expected index must be filtered out")

	for _, pclq := range append(out["0"], out["1"]...) {
		assert.Contains(t, []string{
			"test-pcsg-0-frontend", "test-pcsg-0-backend",
			"test-pcsg-1-frontend", "test-pcsg-1-backend",
		}, pclq.Name)
	}
}

// TestReconcileStatusBoundedDuringScaleDown is the integration-level guard for the same fix:
// stale-index children that still live in the cache after a scale-down must not push
// UpdatedPodCliquesCount past the spec-derived TotalPodCliquesCount, nor inflate replica counters.
func TestReconcileStatusBoundedDuringScaleDown(t *testing.T) {
	ctx := context.Background()
	pcsHash := string(uuid.NewUUID())

	// PCSG was scaled 3 → 2; replica index 2 children have not yet been GC'd by the cascade.
	pcsg := testutils.NewPodCliqueScalingGroupBuilder("test-pcsg", "test-ns", "test-pcs", 0).
		WithReplicas(2).
		WithCliqueNames([]string{"frontend", "backend"}).
		WithOptions(testutils.WithPCSGObservedGeneration(1)).Build()
	pcsg.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{
		UpdateStartedAt:            metav1.Now(),
		PodCliqueSetGenerationHash: pcsHash,
	}
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
		WithPodCliqueSetGenerationHash(&pcsHash).Build()

	mkChild := func(name string, replicaIdx int) client.Object {
		return testutils.NewPCSGPodCliqueBuilder(name, "test-ns", "test-pcs", "test-pcsg", 0, replicaIdx).
			WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
			WithReplicas(2).
			WithOptions(testutils.WithPCLQScheduledAndAvailable(), testutils.WithPCLQCurrentPCSGenerationHash(pcsHash)).Build()
	}

	objs := []client.Object{
		pcsg, pcs,
		mkChild("test-pcsg-0-frontend", 0),
		mkChild("test-pcsg-0-backend", 0),
		mkChild("test-pcsg-1-frontend", 1),
		mkChild("test-pcsg-1-backend", 1),
		// stale-index children from the pre-scale-down generation
		mkChild("test-pcsg-2-frontend", 2),
		mkChild("test-pcsg-2-backend", 2),
	}

	fakeClient := testutils.SetupFakeClient(objs...)
	reconciler := &Reconciler{client: fakeClient}
	result := reconciler.reconcileStatus(ctx, logr.Discard(), client.ObjectKeyFromObject(pcsg))
	require.False(t, result.HasErrors())

	require.NoError(t, fakeClient.Get(ctx, client.ObjectKeyFromObject(pcsg), pcsg))
	assert.Equal(t, int32(2), pcsg.Status.Replicas)
	assert.Equal(t, int32(2), pcsg.Status.ScheduledReplicas, "must not count stale-index replicas")
	assert.Equal(t, int32(2), pcsg.Status.AvailableReplicas, "must not count stale-index replicas")
	assert.Equal(t, int32(2), pcsg.Status.UpdatedReplicas, "must not count stale-index replicas")

	require.NotNil(t, pcsg.Status.UpdateProgress)
	assert.Equal(t, int32(4), pcsg.Status.UpdateProgress.TotalPodCliquesCount)
	assert.Equal(t, int32(4), pcsg.Status.UpdateProgress.UpdatedPodCliquesCount,
		"updated count must be bounded by total — stale-index PCLQs must not contribute")
	assert.LessOrEqual(t, pcsg.Status.UpdateProgress.UpdatedPodCliquesCount,
		pcsg.Status.UpdateProgress.TotalPodCliquesCount,
		"UpdatedPodCliquesCount must never exceed TotalPodCliquesCount")
}

func assertCondition(t *testing.T, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, expectBreached bool) {
	var condition *metav1.Condition
	for i := range pcsg.Status.Conditions {
		if pcsg.Status.Conditions[i].Type == "MinAvailableBreached" {
			condition = &pcsg.Status.Conditions[i]
			break
		}
	}

	require.NotNil(t, condition, "MinAvailableBreached condition should exist")
	isBreached := condition.Status == metav1.ConditionTrue
	assert.Equal(t, expectBreached, isBreached, "condition breach status mismatch")
}

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

package podcliquescalinggroup

import (
	"context"
	"fmt"
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
		name          string
		expectedSize  int
		cliques       []grovecorev1alpha1.PodClique
		minAvailable  int32
		wantScheduled bool
		wantAvailable bool
		wantUpdated   bool
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
			scheduled, available, updated := computeReplicaStatus(logr.Discard(), "generation", nil, "0", tt.expectedSize, tt.cliques)

			assert.Equal(t, tt.wantScheduled, scheduled, "scheduled mismatch")
			assert.Equal(t, tt.wantAvailable, available, "available mismatch")
			assert.Equal(t, tt.wantUpdated, updated, "updated mismatch")
		})
	}
}

// healthyPCSGReplica returns a complete PCSG replica entry (one non-terminating, non-breached
// PCLQ). The tests using this set Spec.CliqueNames to a single name so the replica counts as
// complete; computeNotInBreachReplicas only counts a replica as not-in-breach when all expected
// PodCliques exist and none has MinAvailableBreached=True.
func healthyPCSGReplica() []grovecorev1alpha1.PodClique {
	return []grovecorev1alpha1.PodClique{{}}
}

// breachedPCSGReplica returns a complete PCSG replica entry with one breached PCLQ.
func breachedPCSGReplica() []grovecorev1alpha1.PodClique {
	return []grovecorev1alpha1.PodClique{{
		Status: grovecorev1alpha1.PodCliqueStatus{
			Conditions: []metav1.Condition{{
				Type:   constants.ConditionTypeMinAvailableBreached,
				Status: metav1.ConditionTrue,
			}},
		},
	}}
}

// incompletePCSGReplica returns a replica entry that is missing PodCliques relative to
// Spec.CliqueNames (an empty PCLQ list). It has no breached PCLQ but must NOT be counted as
// not-in-breach, because a partially-created replica is not a valid healthy replica.
func incompletePCSGReplica() []grovecorev1alpha1.PodClique {
	return []grovecorev1alpha1.PodClique{}
}

// TestComputeMinAvailableBreachedCondition exercises the PCSG-level breach formula:
// the PCSG is in breach when (not-in-breach replicas) < MinAvailable. A replica counts as
// not-in-breach only when it is complete (all Spec.CliqueNames PodCliques exist and are
// non-terminating) AND none of them is breached; incomplete replicas are never counted as
// not-in-breach. Each fixture replica carries one PodClique, so the PCSG sets CliqueNames to a
// single name.
func TestComputeMinAvailableBreachedCondition(t *testing.T) {
	tests := []struct {
		name         string
		replicas     int32
		minAvailable *int32
		pclqsMap     map[string][]grovecorev1alpha1.PodClique
		wantStatus   metav1.ConditionStatus
		wantReason   string
	}{
		{
			name:     "all replicas healthy, none breached",
			replicas: 3,
			pclqsMap: map[string][]grovecorev1alpha1.PodClique{
				"0": healthyPCSGReplica(),
				"1": healthyPCSGReplica(),
				"2": healthyPCSGReplica(),
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: "SufficientAvailablePodCliqueScalingGroupReplicas",
		},
		{
			name:         "1 of 5 replicas breached, MinAvailable=2 — not in breach",
			replicas:     5,
			minAvailable: ptr.To(int32(2)),
			pclqsMap: map[string][]grovecorev1alpha1.PodClique{
				"0": breachedPCSGReplica(),
				"1": healthyPCSGReplica(),
				"2": healthyPCSGReplica(),
				"3": healthyPCSGReplica(),
				"4": healthyPCSGReplica(),
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: "SufficientAvailablePodCliqueScalingGroupReplicas",
		},
		{
			// GT-4 step 4 scenario: WL2 sg-x has 2 replicas, MinAvailable=1.
			// Killing all pc-c pods of replica 0 breaches that replica; replica 1
			// stays healthy. notInBreach=1 == MinAvailable=1 → PCSG NOT in breach.
			// PCS-level handler must NOT delete the whole PCS replica here; only
			// the PCSG-level scoped restart should fire.
			name:         "1 of 2 replicas breached, MinAvailable=1 — not in breach (PCSG-scoped restart only)",
			replicas:     2,
			minAvailable: ptr.To(int32(1)),
			pclqsMap: map[string][]grovecorev1alpha1.PodClique{
				"0": breachedPCSGReplica(),
				"1": healthyPCSGReplica(),
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: "SufficientAvailablePodCliqueScalingGroupReplicas",
		},
		{
			// GT-4 step 6: both PCSG replicas breached. notInBreach=0 < 1 → PCSG
			// in breach. PCS-level handler should restart the whole PCS replica.
			name:         "both replicas breached, MinAvailable=1 — in breach (escalates to PCS-level)",
			replicas:     2,
			minAvailable: ptr.To(int32(1)),
			pclqsMap: map[string][]grovecorev1alpha1.PodClique{
				"0": breachedPCSGReplica(),
				"1": breachedPCSGReplica(),
			},
			wantStatus: metav1.ConditionTrue,
			wantReason: "InsufficientAvailablePodCliqueScalingGroupReplicas",
		},
		{
			name:         "2 of 3 replicas breached, MinAvailable=2 — in breach",
			replicas:     3,
			minAvailable: ptr.To(int32(2)),
			pclqsMap: map[string][]grovecorev1alpha1.PodClique{
				"0": breachedPCSGReplica(),
				"1": breachedPCSGReplica(),
				"2": healthyPCSGReplica(),
			},
			wantStatus: metav1.ConditionTrue,
			wantReason: "InsufficientAvailablePodCliqueScalingGroupReplicas",
		},
		{
			// No replicas exist yet (mid-creation / scale-up). notInBreach=0 < min → breach.
			// TerminationDelay (default 4h) absorbs the startup window before any action fires.
			name:         "no replicas exist yet — in breach (startup)",
			replicas:     3,
			minAvailable: ptr.To(int32(2)),
			pclqsMap:     map[string][]grovecorev1alpha1.PodClique{},
			wantStatus:   metav1.ConditionTrue,
			wantReason:   "InsufficientAvailablePodCliqueScalingGroupReplicas",
		},
		{
			// Replica 0 is incomplete (its pc-c was deleted and not yet recreated) so it has no
			// breached PodClique but is not a valid healthy replica; replica 1 is breached.
			// notInBreach=0 < MinAvailable=1 → in breach. An incomplete replica must not be
			// mistaken for a healthy one (which would also poison the was-healthy gate).
			name:         "one replica incomplete, one breached, MinAvailable=1 — in breach",
			replicas:     2,
			minAvailable: ptr.To(int32(1)),
			pclqsMap: map[string][]grovecorev1alpha1.PodClique{
				"0": incompletePCSGReplica(),
				"1": breachedPCSGReplica(),
			},
			wantStatus: metav1.ConditionTrue,
			wantReason: "InsufficientAvailablePodCliqueScalingGroupReplicas",
		},
		{
			// Replica 0 is incomplete, replica 1 is complete and healthy. notInBreach=1 ==
			// MinAvailable=1 → not in breach (the one complete replica satisfies MinAvailable).
			name:         "one replica incomplete, one healthy, MinAvailable=1 — not in breach",
			replicas:     2,
			minAvailable: ptr.To(int32(1)),
			pclqsMap: map[string][]grovecorev1alpha1.PodClique{
				"0": incompletePCSGReplica(),
				"1": healthyPCSGReplica(),
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: "SufficientAvailablePodCliqueScalingGroupReplicas",
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
					// Each fixture replica carries exactly one PodClique, so a single clique name
					// makes healthy/breached replicas "complete" and empty ones "incomplete".
					CliqueNames: []string{"pc"},
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

// TestComputeMinAvailableBreachedConditionUpdateInProgress pins the precedence of the
// IsPCSGUpdateInProgress short-circuit over the breach formula: while a rolling update is
// running, the condition must be Unknown so gang termination does not fire mid-update even
// if the breach formula would otherwise flip True.
func TestComputeMinAvailableBreachedConditionUpdateInProgress(t *testing.T) {
	pastTransition := metav1.NewTime(time.Now().Add(-10 * time.Minute))

	tests := []struct {
		name           string
		replicas       int32
		minAvailable   *int32
		updateProgress *grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress
		pclqsMap       map[string][]grovecorev1alpha1.PodClique
		wantStatus     metav1.ConditionStatus
		wantReason     string
	}{
		{
			name:         "update in progress overrides every-replica-breached",
			replicas:     2,
			minAvailable: ptr.To(int32(1)),
			updateProgress: &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{
				UpdateStartedAt:            pastTransition,
				UpdateEndedAt:              nil, // active update
				PodCliqueSetGenerationHash: "gen-hash-1",
			},
			pclqsMap: map[string][]grovecorev1alpha1.PodClique{
				"0": breachedPCSGReplica(),
				"1": breachedPCSGReplica(),
			},
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
					UpdateProgress: tt.updateProgress,
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
				pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					WithPodCliqueSetGenerationHash(&pcsGenerationHash).
					WithScalingGroup("compute", []string{"frontend", "backend"}).
					Build()
				cliques := []client.Object{
					markPCSGPCLQConverged(t, pcs, pcsg, testutils.NewPCSGPodCliqueBuilder("test-pcsg-0-frontend", "test-ns", "test-pcs", "test-pcsg", 0, 0).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable()).Build(), pcsGenerationHash),
					markPCSGPCLQConverged(t, pcs, pcsg, testutils.NewPCSGPodCliqueBuilder("test-pcsg-0-backend", "test-ns", "test-pcs", "test-pcsg", 0, 0).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable()).Build(), pcsGenerationHash),
					markPCSGPCLQConverged(t, pcs, pcsg, testutils.NewPCSGPodCliqueBuilder("test-pcsg-1-frontend", "test-ns", "test-pcs", "test-pcsg", 0, 1).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable()).Build(), pcsGenerationHash),
					markPCSGPCLQConverged(t, pcs, pcsg, testutils.NewPCSGPodCliqueBuilder("test-pcsg-1-backend", "test-ns", "test-pcs", "test-pcsg", 0, 1).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable()).Build(), pcsGenerationHash),
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
				pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					WithPodCliqueSetGenerationHash(&pcsGenerationHash).
					WithScalingGroupConfig("compute", []string{"worker"}, 3, 2).
					Build()
				cliques := []client.Object{
					markPCSGPCLQConverged(t, pcs, pcsg, testutils.NewPCSGPodCliqueBuilder("test-pcsg-0-worker", "test-ns", "test-pcs", "test-pcsg", 0, 0).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable()).Build(), pcsGenerationHash),
					testutils.NewPCSGPodCliqueBuilder("test-pcsg-1-worker", "test-ns", "test-pcs", "test-pcsg", 0, 1).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledButBreached()).Build(),
					testutils.NewPCSGPodCliqueBuilder("test-pcsg-2-worker", "test-ns", "test-pcs", "test-pcsg", 0, 2).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQNotScheduled()).Build(),
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
				pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
					WithPodCliqueSetGenerationHash(&pcsGenerationHash).
					WithScalingGroup("compute", []string{"frontend", "backend"}).
					Build()
				cliques := []client.Object{
					// Replica 0: healthy
					markPCSGPCLQConverged(t, pcs, pcsg, testutils.NewPCSGPodCliqueBuilder("test-pcsg-0-frontend", "test-ns", "test-pcs", "test-pcsg", 0, 0).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable()).Build(), pcsGenerationHash),
					markPCSGPCLQConverged(t, pcs, pcsg, testutils.NewPCSGPodCliqueBuilder("test-pcsg-0-backend", "test-ns", "test-pcs", "test-pcsg", 0, 0).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable()).Build(), pcsGenerationHash),
					// Replica 1: has one terminating clique
					markPCSGPCLQConverged(t, pcs, pcsg, testutils.NewPCSGPodCliqueBuilder("test-pcsg-1-frontend", "test-ns", "test-pcs", "test-pcsg", 0, 1).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable()).Build(), pcsGenerationHash),
					testutils.NewPCSGPodCliqueBuilder("test-pcsg-1-backend", "test-ns", "test-pcs", "test-pcsg", 0, 1).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQTerminating()).Build(),
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
			revision := testutils.NewPodCliqueSetControllerRevision(pcs, testutils.ComputePodCliqueTemplateHashes(pcs))
			allObjects := append([]client.Object{pcsg, pcs, revision}, cliques...)
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
			revision := testutils.NewPodCliqueSetControllerRevision(pcs, testutils.ComputePodCliqueTemplateHashes(pcs))
			fakeClient := testutils.SetupFakeClient(tt.pcsg, pcs, revision)
			reconciler := &Reconciler{client: fakeClient}

			result := reconciler.reconcileStatus(ctx, logr.Discard(), client.ObjectKeyFromObject(tt.pcsg))

			assert.False(t, result.HasErrors())
		})
	}
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
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
		WithPodCliqueSetGenerationHash(&pcsHash).
		WithScalingGroup("compute", []string{"frontend", "backend"}).
		Build()

	matchingPCLQ := func(t *testing.T, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, replicaIndex int, cliqueName string) grovecorev1alpha1.PodClique {
		name := pcsgChildName(pcsg.Name, replicaIndex, cliqueName)
		pclq := testutils.NewPCSGPodCliqueBuilder(name, "test-ns", "test-pcs", pcsg.Name, 0, replicaIndex).Build()
		return *markPCSGPCLQConverged(t, pcs, pcsg, pclq, pcsHash)
	}
	staleHashPCLQ := func(t *testing.T, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, replicaIndex int, cliqueName string) grovecorev1alpha1.PodClique {
		name := pcsgChildName(pcsg.Name, replicaIndex, cliqueName)
		pclq := testutils.NewPCSGPodCliqueBuilder(name, "test-ns", "test-pcs", pcsg.Name, 0, replicaIndex).Build()
		return *markPCSGPCLQConverged(t, pcs, pcsg, pclq, otherHash)
	}
	terminatingMatchingPCLQ := func(t *testing.T, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, replicaIndex int, cliqueName string) grovecorev1alpha1.PodClique {
		name := pcsgChildName(pcsg.Name, replicaIndex, cliqueName)
		pclq := testutils.NewPCSGPodCliqueBuilder(name, "test-ns", "test-pcs", pcsg.Name, 0, replicaIndex).
			WithOptions(testutils.WithPCLQTerminating()).Build()
		return *markPCSGPCLQConverged(t, pcs, pcsg, pclq, pcsHash)
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
		pclqsPerReplica  func(*testing.T, *grovecorev1alpha1.PodCliqueScalingGroup) map[string][]grovecorev1alpha1.PodClique
		wantWritten      bool
		wantUpdatedCount int32
		wantTotalCount   int32
	}{
		{
			name: "UpdateProgress nil - counts not written, no panic",
			pcsg: build(2, false),
			pclqsPerReplica: func(t *testing.T, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) map[string][]grovecorev1alpha1.PodClique {
				return map[string][]grovecorev1alpha1.PodClique{"0": {matchingPCLQ(t, pcsg, 0, "frontend"), matchingPCLQ(t, pcsg, 0, "backend")}}
			},
			wantWritten: false,
		},
		{
			name: "UpdateProgress set, all PCLQs at current hash -> updated == total",
			pcsg: build(2, true),
			pclqsPerReplica: func(t *testing.T, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) map[string][]grovecorev1alpha1.PodClique {
				return map[string][]grovecorev1alpha1.PodClique{
					"0": {matchingPCLQ(t, pcsg, 0, "frontend"), matchingPCLQ(t, pcsg, 0, "backend")},
					"1": {matchingPCLQ(t, pcsg, 1, "frontend"), matchingPCLQ(t, pcsg, 1, "backend")},
				}
			},
			wantWritten:      true,
			wantUpdatedCount: 4,
			wantTotalCount:   4, // replicas (2) * cliqueNames (2)
		},
		{
			name: "UpdateProgress set, mixed hashes -> partial updated count",
			pcsg: build(2, true),
			pclqsPerReplica: func(t *testing.T, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) map[string][]grovecorev1alpha1.PodClique {
				return map[string][]grovecorev1alpha1.PodClique{
					"0": {matchingPCLQ(t, pcsg, 0, "frontend"), staleHashPCLQ(t, pcsg, 0, "backend")},
					"1": {matchingPCLQ(t, pcsg, 1, "frontend"), matchingPCLQ(t, pcsg, 1, "backend")},
				}
			},
			wantWritten:      true,
			wantUpdatedCount: 3,
			wantTotalCount:   4,
		},
		{
			name: "UpdateProgress set, terminating matching PCLQ excluded from updated count",
			pcsg: build(2, true),
			pclqsPerReplica: func(t *testing.T, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) map[string][]grovecorev1alpha1.PodClique {
				return map[string][]grovecorev1alpha1.PodClique{
					"0": {matchingPCLQ(t, pcsg, 0, "frontend"), terminatingMatchingPCLQ(t, pcsg, 0, "backend")},
					"1": {matchingPCLQ(t, pcsg, 1, "frontend"), matchingPCLQ(t, pcsg, 1, "backend")},
				}
			},
			wantWritten:      true,
			wantUpdatedCount: 3, // terminating excluded even though hash matches
			wantTotalCount:   4, // total derives from spec, unaffected by terminating children
		},
		{
			name: "UpdateProgress set, zero replicas -> counts are 0/0",
			pcsg: build(0, true),
			pclqsPerReplica: func(_ *testing.T, _ *grovecorev1alpha1.PodCliqueScalingGroup) map[string][]grovecorev1alpha1.PodClique {
				return map[string][]grovecorev1alpha1.PodClique{}
			},
			wantWritten:      true,
			wantUpdatedCount: 0,
			wantTotalCount:   0,
		},
	}

	selectedRevision := testutils.NewSelectedRevision(*pcs.Status.CurrentGenerationHash, testutils.ComputePodCliqueTemplateHashes(pcs))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutateReplicas(logr.Discard(), selectedRevision, pcs, tt.pcsg, tt.pclqsPerReplica(t, tt.pcsg))

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
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
		WithPodCliqueSetGenerationHash(&hash).
		WithScalingGroup("compute", []string{"frontend"}).
		Build()
	pcsg := testutils.NewPodCliqueScalingGroupBuilder("test-pcsg", "test-ns", "test-pcs", 0).
		WithReplicas(1).
		WithCliqueNames([]string{"frontend"}).Build()
	selectedRevision := testutils.NewSelectedRevision(hash, testutils.ComputePodCliqueTemplateHashes(pcs))
	expectedHashes := componentutils.GetPCLQTemplateHashes(selectedRevision, pcs, pcsg)
	mk := func(currHash *string, terminating bool) grovecorev1alpha1.PodClique {
		p := testutils.NewPCSGPodCliqueBuilder("test-pcsg-0-frontend", "test-ns", "test-pcs", "test-pcsg", 0, 0).Build()
		if currHash != nil {
			p = markPCSGPCLQConverged(t, pcs, pcsg, p, *currHash)
		}
		if terminating {
			now := metav1.NewTime(time.Now())
			p.DeletionTimestamp = &now
			p.Finalizers = []string{"f"}
		}
		return *p
	}

	tests := []struct {
		name string
		hash string
		in   []grovecorev1alpha1.PodClique
		want int32
	}{
		{"empty input → 0", hash, nil, 0},
		{"all matching", hash, []grovecorev1alpha1.PodClique{mk(&hash, false), mk(&hash, false)}, 2},
		{"mixed", hash, []grovecorev1alpha1.PodClique{mk(&hash, false), mk(&otherHash, false), mk(nil, false)}, 1},
		{"terminating matching excluded", hash, []grovecorev1alpha1.PodClique{mk(&hash, false), mk(&hash, true)}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, countPCSGReplicaUpdatedPCLQs(tt.hash, expectedHashes, tt.in))
		})
	}
}

func TestHavePCSGPodCliquesConverged(t *testing.T) {
	hash := "generation-hash"
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
		WithPodCliqueSetGenerationHash(&hash).
		WithScalingGroup("compute", []string{"frontend"}).
		Build()
	pcsg := testutils.NewPodCliqueScalingGroupBuilder("test-pcsg", "test-ns", "test-pcs", 0).
		WithReplicas(1).
		WithCliqueNames([]string{"frontend"}).Build()
	selectedRevision := testutils.NewSelectedRevision(hash, testutils.ComputePodCliqueTemplateHashes(pcs))
	base := markPCSGPCLQConverged(t, pcs, pcsg,
		testutils.NewPCSGPodCliqueBuilder("test-pcsg-0-frontend", "test-ns", "test-pcs", "test-pcsg", 0, 0).Build(),
		hash)

	tests := []struct {
		name   string
		pclqs  []grovecorev1alpha1.PodClique
		mutate func(*grovecorev1alpha1.PodClique)
		want   bool
	}{
		{
			name:  "all expected hashes converged",
			pclqs: []grovecorev1alpha1.PodClique{*base.DeepCopy()},
			want:  true,
		},
		{
			name:  "missing expected child",
			pclqs: nil,
			want:  false,
		},
		{
			name:  "label hash stale",
			pclqs: []grovecorev1alpha1.PodClique{*base.DeepCopy()},
			mutate: func(pclq *grovecorev1alpha1.PodClique) {
				pclq.Labels[apicommon.LabelPodTemplateHash] = "old-template"
			},
			want: false,
		},
		{
			name:  "status template hash stale",
			pclqs: []grovecorev1alpha1.PodClique{*base.DeepCopy()},
			mutate: func(pclq *grovecorev1alpha1.PodClique) {
				pclq.Status.CurrentPodTemplateHash = ptr.To("old-template")
			},
			want: false,
		},
		{
			name:  "generation hash stale",
			pclqs: []grovecorev1alpha1.PodClique{*base.DeepCopy()},
			mutate: func(pclq *grovecorev1alpha1.PodClique) {
				pclq.Status.CurrentPodCliqueSetGenerationHash = ptr.To("old-generation")
			},
			want: false,
		},
		{
			name:  "terminating child",
			pclqs: []grovecorev1alpha1.PodClique{*base.DeepCopy()},
			mutate: func(pclq *grovecorev1alpha1.PodClique) {
				now := metav1.NewTime(time.Now())
				pclq.DeletionTimestamp = &now
				pclq.Finalizers = []string{"f"}
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pclqs := tt.pclqs
			if tt.mutate != nil && len(pclqs) > 0 {
				tt.mutate(&pclqs[0])
			}
			assert.Equal(t, tt.want, havePCSGPodCliquesConverged(selectedRevision, pcs, pcsg, pclqs))
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
		WithPodCliqueSetGenerationHash(&pcsHash).
		WithScalingGroup("compute", []string{"frontend", "backend"}).
		Build()

	mkChild := func(name string, replicaIdx int) client.Object {
		pclq := testutils.NewPCSGPodCliqueBuilder(name, "test-ns", "test-pcs", "test-pcsg", 0, replicaIdx).
			WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
			WithReplicas(2).
			WithOptions(testutils.WithPCLQScheduledAndAvailable()).Build()
		if replicaIdx >= int(pcsg.Spec.Replicas) {
			pclq.Status.CurrentPodCliqueSetGenerationHash = ptr.To(pcsHash)
			return pclq
		}
		return markPCSGPCLQConverged(t, pcs, pcsg, pclq, pcsHash)
	}

	objs := []client.Object{
		pcsg, pcs, testutils.NewPodCliqueSetControllerRevision(pcs, testutils.ComputePodCliqueTemplateHashes(pcs)),
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

func pcsgChildName(pcsgName string, replicaIndex int, cliqueName string) string {
	return fmt.Sprintf("%s-%d-%s", pcsgName, replicaIndex, cliqueName)
}

func markPCSGPCLQConverged(t testing.TB, pcs *grovecorev1alpha1.PodCliqueSet, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, pclq *grovecorev1alpha1.PodClique, generationHash string) *grovecorev1alpha1.PodClique {
	t.Helper()
	selectedRevision := testutils.NewSelectedRevision(generationHash, testutils.ComputePodCliqueTemplateHashes(pcs))
	expectedHashes := componentutils.GetPCLQTemplateHashes(selectedRevision, pcs, pcsg)
	expectedTemplateHash, ok := expectedHashes[pclq.Name]
	require.True(t, ok, "expected template hash for %s", pclq.Name)
	if pclq.Labels == nil {
		pclq.Labels = map[string]string{}
	}
	pclq.Labels[apicommon.LabelPodTemplateHash] = expectedTemplateHash
	pclq.Status.CurrentPodTemplateHash = ptr.To(expectedTemplateHash)
	pclq.Status.CurrentPodCliqueSetGenerationHash = ptr.To(generationHash)
	pclq.Status.ReadyReplicas = *pclq.Spec.MinAvailable
	pclq.Status.UpdatedReplicas = *pclq.Spec.MinAvailable
	return pclq
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

// TestMutateMinAvailableBreachedConditionClearsGangTerminationInProgress pins the second half
// of the in-progress-flag loop-break design: when MinAvailableBreached transitions from True
// (or unset) to False, the PCSG status reconciler must also remove the
// GangTerminationInProgress condition so the next regression can be recycled.
func TestMutateMinAvailableBreachedConditionClearsGangTerminationInProgress(t *testing.T) {
	pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
		Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
			Replicas:     2,
			MinAvailable: ptr.To(int32(1)),
			// Each fixture replica carries one PodClique, so a single clique name makes the
			// recovered replicas count as complete and not-in-breach.
			CliqueNames: []string{"pc"},
		},
		Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
			Conditions: []metav1.Condition{
				{
					Type:   constants.ConditionTypeMinAvailableBreached,
					Status: metav1.ConditionTrue,
					Reason: constants.ConditionReasonScheduledReplicasBelowMinAvailable,
				},
				{
					Type:   constants.ConditionTypeGangTerminationInProgress,
					Status: metav1.ConditionTrue,
					Reason: constants.ConditionReasonGangTerminationActive,
				},
			},
		},
	}
	pclqsHealthy := map[string][]grovecorev1alpha1.PodClique{
		"0": healthyPCSGReplica(),
		"1": healthyPCSGReplica(),
	}

	mutateMinAvailableBreachedCondition(logr.Discard(), pcsg, pclqsHealthy)

	// MinAvailableBreached must now be False (recovery).
	breach := pcsg.Status.Conditions
	var breachStatus metav1.ConditionStatus
	for _, c := range breach {
		if c.Type == constants.ConditionTypeMinAvailableBreached {
			breachStatus = c.Status
		}
	}
	assert.Equal(t, metav1.ConditionFalse, breachStatus, "MinAvailableBreached should be False after recovery")

	// GangTerminationInProgress must have been cleared.
	for _, c := range pcsg.Status.Conditions {
		if c.Type == constants.ConditionTypeGangTerminationInProgress {
			t.Fatalf("GangTerminationInProgress condition should have been removed on recovery, still present with status %s", c.Status)
		}
	}
}

// TestMutateSelector verifies the /scale selector is published for PCSGs regardless of whether
// ScaleConfig is set in the parent PodCliqueSet template, and is suppressed for PCSGs whose name
// does not match any config in the PodCliqueSet template.
func TestMutateSelector(t *testing.T) {
	const (
		pcsName        = "test-pcs"
		pcsgConfigName = "prefill"
	)
	pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcsName, Replica: 0}, pcsgConfigName)
	withScale := &grovecorev1alpha1.AutoScalingConfig{MaxReplicas: 5}

	tests := []struct {
		name                    string
		pcsgName                string // FQN to put on the PCSG; defaults to pcsgFQN
		pcsgConfigScaleConfig   *grovecorev1alpha1.AutoScalingConfig
		expectSelectorPopulated bool
	}{
		{name: "no ScaleConfig still publishes selector", expectSelectorPopulated: true},
		{name: "ScaleConfig present publishes selector", pcsgConfigScaleConfig: withScale, expectSelectorPopulated: true},
		// PCSG whose name does not match any config in the PCS template (e.g. stale object
		// during a rename) must not publish a selector.
		{name: "unknown PCSG name does not publish selector", pcsgName: "test-pcs-0-stale", expectSelectorPopulated: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: pcsName},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{Name: pcsgConfigName, ScaleConfig: tt.pcsgConfigScaleConfig},
						},
					},
				},
			}
			name := tt.pcsgName
			if name == "" {
				name = pcsgFQN
			}
			pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:   name,
					Labels: map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "0"},
				},
			}

			err := mutateSelector(pcs, pcsg)
			require.NoError(t, err)
			if tt.expectSelectorPopulated {
				require.NotNil(t, pcsg.Status.Selector)
				assert.Contains(t, *pcsg.Status.Selector, apicommon.LabelPodCliqueScalingGroup+"="+pcsg.Name)
			} else {
				assert.Nil(t, pcsg.Status.Selector)
			}
		})
	}
}

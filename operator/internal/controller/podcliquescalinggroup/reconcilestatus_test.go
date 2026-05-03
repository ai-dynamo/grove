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

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
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
			scheduled, available, updated := computeReplicaStatus(logr.Discard(), nil, "0", tt.expectedSize, tt.cliques)

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
			name:         "insufficient scheduled",
			replicas:     3,
			minAvailable: ptr.To(int32(2)),
			scheduled:    1,
			available:    1,
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
					testutils.NewPCSGPodCliqueBuilder("test-pcs-0-frontend-0", "test-ns", "test-pcs", "test-pcsg", 0, 0).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPCSGPodCliqueBuilder("test-pcs-0-backend-0", "test-ns", "test-pcs", "test-pcsg", 0, 0).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPCSGPodCliqueBuilder("test-pcs-0-frontend-1", "test-ns", "test-pcs", "test-pcsg", 0, 1).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPCSGPodCliqueBuilder("test-pcs-0-backend-1", "test-ns", "test-pcs", "test-pcsg", 0, 1).
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
					testutils.NewPCSGPodCliqueBuilder("test-pcs-0-worker-0", "test-ns", "test-pcs", "test-pcsg", 0, 0).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPCSGPodCliqueBuilder("test-pcs-0-worker-1", "test-ns", "test-pcs", "test-pcsg", 0, 1).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledButBreached(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPCSGPodCliqueBuilder("test-pcs-0-worker-2", "test-ns", "test-pcs", "test-pcsg", 0, 2).
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
					testutils.NewPCSGPodCliqueBuilder("test-pcs-0-frontend-0", "test-ns", "test-pcs", "test-pcsg", 0, 0).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPCSGPodCliqueBuilder("test-pcs-0-backend-0", "test-ns", "test-pcs", "test-pcsg", 0, 0).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					// Replica 1: has one terminating clique
					testutils.NewPCSGPodCliqueBuilder("test-pcs-0-frontend-1", "test-ns", "test-pcs", "test-pcsg", 0, 1).
						WithOwnerReference("PodCliqueScalingGroup", "test-pcsg", "").
						WithReplicas(2).
						WithOptions(testutils.WithPCLQScheduledAndAvailable(), testutils.WithPCLQCurrentPCSGenerationHash(pcsGenerationHash)).Build(),
					testutils.NewPCSGPodCliqueBuilder("test-pcs-0-backend-1", "test-ns", "test-pcs", "test-pcsg", 0, 1).
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
			mutateReplicas(logr.Discard(), &pcsHash, tt.pcsg, tt.pclqsPerReplica)

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
			assert.Equal(t, tt.want, countPCSGReplicaUpdatedPCLQs(tt.hash, tt.in))
		})
	}
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


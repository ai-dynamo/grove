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
	"errors"
	"fmt"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/expect"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	testRollingUpdateNamespace = "test-ns"
	testRollingUpdatePCSName   = "test-pcs"
	testRollingUpdatePCSGName  = "test-pcsg"
	testRollingUpdateNewHash   = "new-hash-abc"
	testRollingUpdateOldHash   = "old-hash-xyz"
	testRollingUpdateGenHash   = "pcs-gen-1"
)

// testReplica describes a PCSG replica to synthesize for a rolling-update test.
type testReplica struct {
	index       int
	hash        string
	scheduled   int32
	ready       int32
	updated     int32
	terminating bool
}

func TestMarkUpdateEndReturnsRequeueAfterPatch(t *testing.T) {
	pcsg := testutils.NewPodCliqueScalingGroupBuilder("test-pcsg", "test-ns", "test-pcs", 0).Build()
	pcsg.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{
		UpdateStartedAt:            metav1.Now(),
		PodCliqueSetGenerationHash: testRollingUpdateGenHash,
	}
	cl := testutils.NewTestClientBuilder().
		WithObjects(pcsg).
		WithStatusSubresource(&grovecorev1alpha1.PodCliqueScalingGroup{}).
		Build()
	r := _resource{client: cl}

	err := r.markUpdateEnd(t.Context(), logr.Discard(), pcsg)

	require.Error(t, err)
	var groveError *groveerr.GroveError
	require.True(t, errors.As(err, &groveError))
	assert.Equal(t, groveerr.ErrCodeContinueReconcileAndRequeue, groveError.Code)
	assert.Equal(t, component.OperationSync, groveError.Operation)

	var updated grovecorev1alpha1.PodCliqueScalingGroup
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pcsg), &updated))
	require.NotNil(t, updated.Status.UpdateProgress)
	assert.NotNil(t, updated.Status.UpdateProgress.UpdateEndedAt)
	require.NotNil(t, updated.Status.CurrentPodCliqueSetGenerationHash)
	assert.Equal(t, testRollingUpdateGenHash, *updated.Status.CurrentPodCliqueSetGenerationHash)
}

// TestReconcileInProgressUpdateDoesNotCompleteStaleTarget verifies the safety precondition for
// markUpdateEnd: a PCSG update cannot end, or publish its target as current, when its target differs
// from the current PCS generation. Removing the common stale-target guard must make this test fail.
func TestReconcileInProgressUpdateDoesNotCompleteStaleTarget(t *testing.T) {
	sc := buildRollingUpdateSnapshot(1, 1, 1, []testReplica{updatedReadyReplica(0)})
	sc.pcsg.Status.CurrentPodCliqueSetGenerationHash = ptr.To(testRollingUpdateGenHash)
	sc.pcsg.Status.UpdateProgress.PodCliqueSetGenerationHash = "superseded-generation"
	cl := testutils.NewTestClientBuilder().
		WithObjects(sc.pcsg).
		WithStatusSubresource(&grovecorev1alpha1.PodCliqueScalingGroup{}).
		Build()
	r := _resource{client: cl, expectationsStore: expect.NewExpectationsStore()}

	err := r.reconcileInProgressUpdate(t.Context(), logr.Discard(), sc)

	require.Error(t, err)
	var groveError *groveerr.GroveError
	require.True(t, errors.As(err, &groveError))
	assert.Equal(t, groveerr.ErrCodeContinueReconcileAndRequeue, groveError.Code)
	assert.Nil(t, sc.pcsg.Status.UpdateProgress.UpdateEndedAt)
	require.NotNil(t, sc.pcsg.Status.CurrentPodCliqueSetGenerationHash)
	assert.Equal(t, testRollingUpdateGenHash, *sc.pcsg.Status.CurrentPodCliqueSetGenerationHash)
}

func TestComputePendingUpdateWork(t *testing.T) {
	tests := []struct {
		description         string
		replicas            int32
		reps                []testReplica
		wantOldInfos        []componentutils.PCSGReplicaDisruptionInfo
		wantExisting        int
		wantNewNotReady     int
		wantNumUpdatedReady int
	}{
		{"all replicas old and ready", 3, []testReplica{oldReadyReplica(0), oldReadyReplica(1), oldReadyReplica(2)}, []componentutils.PCSGReplicaDisruptionInfo{{Index: 0, State: componentutils.PCSGReplicaStateReady}, {Index: 1, State: componentutils.PCSGReplicaStateReady}, {Index: 2, State: componentutils.PCSGReplicaStateReady}}, 3, 0, 0},
		{"mixed updated, old ready and old pending", 3, []testReplica{updatedReadyReplica(0), oldReadyReplica(1), oldPendingReplica(2)}, []componentutils.PCSGReplicaDisruptionInfo{{Index: 1, State: componentutils.PCSGReplicaStateReady}, {Index: 2, State: componentutils.PCSGReplicaStatePending}}, 3, 0, 1},
		{"terminating replica is skipped", 2, []testReplica{updatedReadyReplica(0), terminatingReplica(1)}, nil, 1, 0, 1},
		{"old unavailable replica", 1, []testReplica{oldUnavailableReplica(0)}, []componentutils.PCSGReplicaDisruptionInfo{{Index: 0, State: componentutils.PCSGReplicaStateUnavailable}}, 1, 0, 0},
		{"all replicas updated and ready", 2, []testReplica{updatedReadyReplica(0), updatedReadyReplica(1)}, nil, 2, 0, 2},
		{"new configuration replica not yet ready", 1, []testReplica{updatedNotReadyReplica(0)}, nil, 1, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			sc := buildRollingUpdateSnapshot(tt.replicas, 1, 1, tt.reps)
			r := _resource{expectationsStore: expect.NewExpectationsStore()}
			uw, err := r.computePendingUpdateWork(sc)
			require.NoError(t, err)
			assert.Equal(t, tt.wantOldInfos, uw.oldReplicaDisruptionInfos, "oldReplicaDisruptionInfos")
			assert.Equal(t, tt.wantExisting, uw.existingReplicas, "existingReplicas")
			assert.Equal(t, tt.wantNewNotReady, uw.newNotReadyReplicas, "newNotReadyReplicas")
			assert.Equal(t, tt.wantNumUpdatedReady, uw.numUpdatedReadyReplicas, "numUpdatedReadyReplicas")
		})
	}
}

func TestComputePendingUpdateWorkSkipsReplicaWithDeleteExpectation(t *testing.T) {
	sc := buildRollingUpdateSnapshot(3, 1, 2, []testReplica{oldReadyReplica(0), oldReadyReplica(1), oldReadyReplica(2)})
	// Simulate a disruption we already triggered for replica 0 whose deletion the informer cache has not
	// yet observed, by recording a delete expectation for its member PodCliques.
	store := expect.NewExpectationsStore()
	membersByReplicaIndex, err := componentutils.GroupPCLQsByPCSGReplicaIndex(sc.existingPCLQs)
	require.NoError(t, err)
	replica0UIDs := lo.Map(membersByReplicaIndex[0], func(pclq grovecorev1alpha1.PodClique, _ int) types.UID {
		return pclq.GetUID()
	})
	require.NoError(t, store.ExpectDeletions(logr.Discard(), sc.expectationsStoreKey, replica0UIDs...))
	r := _resource{expectationsStore: store}

	uw, err := r.computePendingUpdateWork(sc)
	require.NoError(t, err)
	assert.Equal(t, []componentutils.PCSGReplicaDisruptionInfo{{Index: 1, State: componentutils.PCSGReplicaStateReady}, {Index: 2, State: componentutils.PCSGReplicaStateReady}}, uw.oldReplicaDisruptionInfos, "replica 0 with a pending delete expectation must not be a disruption candidate")
	assert.Equal(t, 2, uw.existingReplicas, "replica 0 with a pending delete expectation must not be counted as an existing replica")
}

func TestIsReplicaUpdatedRequiresNonEmptyExpectedHash(t *testing.T) {
	// A member whose expected pod-template hash is unknown (empty) must never read as updated, even when
	// its status hash is also empty, guarding against a "" == "" false positive. updatedReadyReplica is
	// otherwise fully updated, so only the empty expected hash can flip the result.
	sc := buildRollingUpdateSnapshot(1, 1, 1, []testReplica{updatedReadyReplica(0)})
	m := &sc.existingPCLQs[0]
	sc.expectedPCLQPodTemplateHashMap[m.Name] = ""
	m.Labels[apicommon.LabelPodTemplateHash] = ""
	m.Status.CurrentPodTemplateHash = ptr.To("")

	assert.False(t, isReplicaUpdated(sc, 0, sc.existingPCLQs),
		"a replica whose expected pod-template hash is empty must not be considered updated")
}

func TestProcessPendingUpdates(t *testing.T) {
	newResource := func(sc *syncSnapshot) (_resource, client.Client) {
		objs := []client.Object{sc.pcsg}
		for i := range sc.existingPCLQs {
			objs = append(objs, &sc.existingPCLQs[i])
		}
		cl := testutils.NewTestClientBuilder().
			WithObjects(objs...).
			WithStatusSubresource(&grovecorev1alpha1.PodCliqueScalingGroup{}).
			Build()
		return _resource{client: cl, eventRecorder: record.NewFakeRecorder(64), expectationsStore: expect.NewExpectationsStore()}, cl
	}
	remainingReplicaIndices := func(t *testing.T, cl client.Client) []string {
		var list grovecorev1alpha1.PodCliqueList
		require.NoError(t, cl.List(t.Context(), &list, client.InNamespace(testRollingUpdateNamespace)))
		indices := make([]string, 0, len(list.Items))
		for _, pclq := range list.Items {
			indices = append(indices, pclq.Labels[apicommon.LabelPodCliqueScalingGroupReplicaIndex])
		}
		return indices
	}
	requeueErr := &groveerr.GroveError{Code: groveerr.ErrCodeContinueReconcileAndRequeue, Operation: component.OperationSync}

	t.Run("completes when all replicas are updated and ready", func(t *testing.T) {
		sc := buildRollingUpdateSnapshot(2, 1, 1, []testReplica{updatedReadyReplica(0), updatedReadyReplica(1)})
		r, _ := newResource(sc)

		err := r.processPendingUpdates(t.Context(), logr.Discard(), sc)
		testutils.AssertGroveError(t, requeueErr, err)
		assert.NotNil(t, sc.pcsg.Status.UpdateProgress.UpdateEndedAt, "expected UpdateEndedAt to be set")
	})

	t.Run("disrupts the lowest-index ready old replicas up to the budget", func(t *testing.T) {
		sc := buildRollingUpdateSnapshot(3, 1, 2, []testReplica{oldReadyReplica(0), oldReadyReplica(1), oldReadyReplica(2)})
		r, cl := newResource(sc)

		err := r.processPendingUpdates(t.Context(), logr.Discard(), sc)
		testutils.AssertGroveError(t, requeueErr, err)
		// Budget of 2 and MinAvailable headroom of 2, so replicas 0 and 1 are deleted and 2 remains.
		assert.ElementsMatch(t, []string{"2"}, remainingReplicaIndices(t, cl))
	})

	t.Run("rolls one replica at a time when MinAvailable equals replicas", func(t *testing.T) {
		sc := buildRollingUpdateSnapshot(3, 3, 1, []testReplica{oldReadyReplica(0), oldReadyReplica(1), oldReadyReplica(2)})
		r, cl := newResource(sc)

		err := r.processPendingUpdates(t.Context(), logr.Discard(), sc)
		testutils.AssertGroveError(t, requeueErr, err)
		assert.Len(t, remainingReplicaIndices(t, cl), 2, "MinAvailable equal to replicas must not block the roll; MaxUnavailable=1 disrupts one replica")
	})

	t.Run("disrupts a worst-off unavailable replica instead of deadlocking when unavailable replicas exhaust MaxUnavailable", func(t *testing.T) {
		// Two of three replicas are unavailable with MaxUnavailable=1. The old readiness-delta budget was 0
		// here and blocked forever. The count-anchored budget is existing(3) - reserve(3-1=2) -
		// newNotReady(0) = 1, so one worst-off (unavailable) replica is disrupted instead of stalling.
		sc := buildRollingUpdateSnapshot(3, 1, 1, []testReplica{oldReadyReplica(0), oldUnavailableReplica(1), oldUnavailableReplica(2)})
		r, cl := newResource(sc)

		err := r.processPendingUpdates(t.Context(), logr.Discard(), sc)
		testutils.AssertGroveError(t, requeueErr, err)
		assert.ElementsMatch(t, []string{"0", "2"}, remainingReplicaIndices(t, cl), "the lowest-index unavailable replica must be disrupted, not blocked")
	})

	t.Run("disrupts worst-off replicas first up to the budget", func(t *testing.T) {
		// Budget is existing(3) - reserve(3-2=1) - newNotReady(0) = 2. The unavailable replica 2 is worst-off
		// and is selected before Ready replicas, and one Ready replica (0) fills the remaining budget.
		sc := buildRollingUpdateSnapshot(3, 1, 2, []testReplica{oldReadyReplica(0), oldReadyReplica(1), oldUnavailableReplica(2)})
		r, cl := newResource(sc)

		err := r.processPendingUpdates(t.Context(), logr.Discard(), sc)
		testutils.AssertGroveError(t, requeueErr, err)
		assert.ElementsMatch(t, []string{"1"}, remainingReplicaIndices(t, cl), "worst-off unavailable replica and one Ready replica disrupted, leaving one Ready")
	})

	t.Run("updates a pending replica whose own unavailability would have exhausted the budget", func(t *testing.T) {
		// The single replica is pending. The reserve is 1-1=0, so the budget is existing(1) - 0 - 0 = 1 and the
		// pending replica is updated rather than deadlocking.
		sc := buildRollingUpdateSnapshot(1, 1, 1, []testReplica{oldPendingReplica(0)})
		r, cl := newResource(sc)

		err := r.processPendingUpdates(t.Context(), logr.Discard(), sc)
		testutils.AssertGroveError(t, requeueErr, err)
		assert.Empty(t, remainingReplicaIndices(t, cl), "the pending replica must be disrupted for replacement")
	})

	t.Run("does not disrupt when existing replicas are below the availability reserve", func(t *testing.T) {
		// desired=5 but only 2 replicas are present (the rest deleted externally, awaiting recreation).
		// The reserve is 5-2=3, so the budget is existing(2) - 3 - 0 = -1 <= 0: no disruption this reconcile
		// until the missing replicas are recreated and availability recovers.
		sc := buildRollingUpdateSnapshot(5, 1, 2, []testReplica{oldReadyReplica(0), oldReadyReplica(1)})
		r, cl := newResource(sc)

		err := r.processPendingUpdates(t.Context(), logr.Discard(), sc)
		testutils.AssertGroveError(t, requeueErr, err)
		assert.ElementsMatch(t, []string{"0", "1"}, remainingReplicaIndices(t, cl), "no replica may be disrupted while below the availability reserve")
	})
}

func oldReadyReplica(index int) testReplica {
	return testReplica{index: index, hash: testRollingUpdateOldHash, scheduled: 1, ready: 1}
}

func oldPendingReplica(index int) testReplica {
	return testReplica{index: index, hash: testRollingUpdateOldHash, scheduled: 0, ready: 0}
}

func oldUnavailableReplica(index int) testReplica {
	return testReplica{index: index, hash: testRollingUpdateOldHash, scheduled: 1, ready: 0}
}

func updatedReadyReplica(index int) testReplica {
	return testReplica{index: index, hash: testRollingUpdateNewHash, scheduled: 1, ready: 1, updated: 1}
}

func updatedNotReadyReplica(index int) testReplica {
	return testReplica{index: index, hash: testRollingUpdateNewHash, scheduled: 1, ready: 0, updated: 0}
}

func terminatingReplica(index int) testReplica {
	return testReplica{index: index, hash: testRollingUpdateOldHash, terminating: true}
}

// buildRollingUpdateSnapshot builds a syncSnapshot with one member PodClique per replica, wiring the
// expected hash and FQN maps so the rolling-update logic can classify each replica.
func buildRollingUpdateSnapshot(replicas, minAvailable, maxUnavailable int32, reps []testReplica) *syncSnapshot {
	pcs := testutils.NewPodCliqueSetBuilder(testRollingUpdatePCSName, testRollingUpdateNamespace, "uid").
		WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy}).
		Build()
	pcs.Status.CurrentGenerationHash = ptr.To(testRollingUpdateGenHash)
	pcsg := testutils.NewPodCliqueScalingGroupBuilder(testRollingUpdatePCSGName, testRollingUpdateNamespace, testRollingUpdatePCSName, 0).
		WithReplicas(replicas).
		WithMinAvailable(minAvailable).
		Build()
	pcsg.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{
		PodCliqueSetGenerationHash: testRollingUpdateGenHash,
	}
	members := make([]grovecorev1alpha1.PodClique, 0, len(reps))
	expectedHashByName := map[string]string{}
	expectedFQNsByReplica := map[int][]string{}
	for _, rep := range reps {
		name := fmt.Sprintf("%s-%d-c", testRollingUpdatePCSGName, rep.index)
		members = append(members, newRollingUpdateMemberPCLQ(name, rep))
		expectedHashByName[name] = testRollingUpdateNewHash
		expectedFQNsByReplica[rep.index] = append(expectedFQNsByReplica[rep.index], name)
	}
	return &syncSnapshot{
		pcs:                            pcs,
		pcsg:                           pcsg,
		expectationsStoreKey:           pcsg.Namespace + "/" + pcsg.Name,
		pcsgConfig:                     &grovecorev1alpha1.PodCliqueScalingGroupConfig{RollingUpdate: &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: ptr.To(maxUnavailable)}},
		existingPCLQs:                  members,
		expectedPCLQPodTemplateHashMap: expectedHashByName,
		expectedPCLQFQNsPerPCSGReplica: expectedFQNsByReplica,
	}
}

func newRollingUpdateMemberPCLQ(name string, rep testReplica) grovecorev1alpha1.PodClique {
	return *testutils.NewPCSGPodCliqueBuilder(name, testRollingUpdateNamespace, testRollingUpdatePCSName, testRollingUpdatePCSGName, 0, rep.index).
		WithLabels(map[string]string{apicommon.LabelPodTemplateHash: rep.hash}).
		WithOptions(func(p *grovecorev1alpha1.PodClique) {
			p.UID = types.UID(name)
			p.Status.ScheduledReplicas = rep.scheduled
			p.Status.ReadyReplicas = rep.ready
			p.Status.UpdatedReplicas = rep.updated
			p.Status.CurrentPodTemplateHash = ptr.To(rep.hash)
			p.Status.CurrentPodCliqueSetGenerationHash = ptr.To(testRollingUpdateGenHash)
			if rep.terminating {
				now := metav1.Now()
				p.DeletionTimestamp = &now
				p.Finalizers = []string{"fake.finalizer/rollingupdate-test"}
			}
		}).
		Build()
}

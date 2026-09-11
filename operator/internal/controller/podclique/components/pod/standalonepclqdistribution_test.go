// Copyright 2026 The Grove Authors.
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

package pod

import (
	"context"
	"errors"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/podclique/expectations"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/expect"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestBuildDesiredCountByPodGang verifies the desired per-anchor pod counts for a standalone
// PodClique are read from the PodGangMap anchor entries, skipping entries without this clique or with
// a zero count.
func TestBuildDesiredCountByPodGang(t *testing.T) {
	anchor0 := podGangNameForEpoch(testAnchor0Epoch)
	anchor1 := podGangNameForEpoch(testAnchor1Epoch)
	tests := []struct {
		name     string
		entries  []grovecorev1alpha1.PodGangEntry
		expected map[string]int32
	}{
		{
			name:     "single anchor carrying the clique",
			entries:  []grovecorev1alpha1.PodGangEntry{anchorEntryWithCliques(testAnchor0Epoch, 0, map[string]int32{testCliqueName: 3})},
			expected: map[string]int32{anchor0: 3},
		},
		{
			name: "multiple anchors carrying the clique",
			entries: []grovecorev1alpha1.PodGangEntry{
				anchorEntryWithCliques(testAnchor0Epoch, 0, map[string]int32{testCliqueName: 3}),
				anchorEntryWithCliques(testAnchor1Epoch, 1, map[string]int32{testCliqueName: 2}),
			},
			expected: map[string]int32{anchor0: 3, anchor1: 2},
		},
		{
			name:     "entry without the clique is skipped",
			entries:  []grovecorev1alpha1.PodGangEntry{anchorEntryWithCliques(testAnchor0Epoch, 0, map[string]int32{"other": 3})},
			expected: map[string]int32{},
		},
		{
			name:     "entry with a zero count is skipped",
			entries:  []grovecorev1alpha1.PodGangEntry{anchorEntryWithCliques(testAnchor0Epoch, 0, map[string]int32{testCliqueName: 0})},
			expected: map[string]int32{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ss := &syncSnapshot{pcs: pcs(), pcsReplicaIndex: testPCSReplicaIndex, cliqueName: testCliqueName, pgm: pgmWithEntries(tc.entries...)}
			actual := buildDesiredCountByPodGang(ss)
			assert.Equal(t, tc.expected, actual)
		})
	}

	t.Run("nil PodGangMap yields an empty map", func(t *testing.T) {
		ss := &syncSnapshot{pcs: pcs(), pcsReplicaIndex: testPCSReplicaIndex, cliqueName: testCliqueName, pgm: nil}
		assert.Empty(t, buildDesiredCountByPodGang(ss))
	})
}

// TestComputeCountDeltaByPodGang verifies the per-PodGang delta is desired minus the effective pod
// count (live plus outstanding create expectations minus outstanding delete expectations), positive
// for creation and negative for deletion, omitting matches and covering PodGangs present only in the
// live set. The expectation accounting keeps an in-flight create or delete from a prior reconcile,
// not yet reflected in the informer cache, from being repeated.
func TestComputeCountDeltaByPodGang(t *testing.T) {
	pclq := testutils.NewPodCliqueBuilder(testPCSName, "uid", testCliqueName, testNamespace, testPCSReplicaIndex).Build()
	keyFor := func(podGangName string) string {
		key, err := componentutils.PodGangScopedExpectationsStoreKey(pclq.ObjectMeta, podGangName)
		require.NoError(t, err)
		return key
	}

	tests := []struct {
		name            string
		desired         map[string]int32
		livePods        map[string][]*corev1.Pod
		terminatingPods map[string][]*corev1.Pod
		createExpUID    map[string][]types.UID // pending creates per PodGang
		deleteExpUID    map[string][]types.UID // pending deletes per PodGang
		expected        map[string]int32
	}{
		{
			name:     "deficit needs creation",
			desired:  map[string]int32{"pg-a": 3},
			livePods: map[string][]*corev1.Pod{"pg-a": {nonTerminatingPodInPodGang("pod-a", "pg-a", "")}},
			expected: map[string]int32{"pg-a": 2},
		},
		{
			name:     "excess needs deletion",
			desired:  map[string]int32{"pg-a": 1},
			livePods: map[string][]*corev1.Pod{"pg-a": {nonTerminatingPodInPodGang("pod-a", "pg-a", ""), nonTerminatingPodInPodGang("pod-b", "pg-a", ""), nonTerminatingPodInPodGang("pod-c", "pg-a", "")}},
			expected: map[string]int32{"pg-a": -2},
		},
		{
			name:     "matching count is omitted",
			desired:  map[string]int32{"pg-a": 2},
			livePods: map[string][]*corev1.Pod{"pg-a": {nonTerminatingPodInPodGang("pod-a", "pg-a", ""), nonTerminatingPodInPodGang("pod-b", "pg-a", "")}},
			expected: map[string]int32{},
		},
		{
			name:     "live PodGang absent from desired is fully deleted",
			desired:  map[string]int32{},
			livePods: map[string][]*corev1.Pod{"pg-a": {nonTerminatingPodInPodGang("pod-a", "pg-a", ""), nonTerminatingPodInPodGang("pod-b", "pg-a", "")}},
			expected: map[string]int32{"pg-a": -2},
		},
		{
			// A pod was created in a prior reconcile but the informer cache has not shown it yet. The
			// outstanding create expectation covers the deficit, so no duplicate is created.
			name:         "pending create covers the deficit so no duplicate is created",
			desired:      map[string]int32{"pg-a": 3},
			livePods:     map[string][]*corev1.Pod{"pg-a": {nonTerminatingPodInPodGang("pod-a", "pg-a", ""), nonTerminatingPodInPodGang("pod-b", "pg-a", "")}},
			createExpUID: map[string][]types.UID{"pg-a": {"pending-create"}},
			expected:     map[string]int32{},
		},
		{
			// A pod was deleted in a prior reconcile but the cache still shows it live. The outstanding
			// delete expectation offsets it, so another pod is not deleted.
			name:         "pending delete offsets the live count so no extra pod is deleted",
			desired:      map[string]int32{"pg-a": 2},
			livePods:     map[string][]*corev1.Pod{"pg-a": {nonTerminatingPodInPodGang("pod-a", "pg-a", ""), nonTerminatingPodInPodGang("pod-b", "pg-a", ""), podWithUID("pod-c", "pg-a", "pending-delete")}},
			deleteExpUID: map[string][]types.UID{"pg-a": {"pending-delete"}},
			expected:     map[string]int32{},
		},
		{
			// A terminating pod is counted as present and is also carried in the delete expectations by
			// SyncExpectations. The two cancel, so a terminating pod neither adds to nor subtracts from
			// the effective count. With the desired count already met by non-terminating pods, no pod is
			// created or deleted.
			name:            "a terminating pod alongside the desired non-terminating pods triggers no change",
			desired:         map[string]int32{"pg-a": 2},
			livePods:        map[string][]*corev1.Pod{"pg-a": {nonTerminatingPodInPodGang("pod-a", "pg-a", ""), nonTerminatingPodInPodGang("pod-b", "pg-a", "")}},
			terminatingPods: map[string][]*corev1.Pod{"pg-a": {terminatingPodInPodGang("pod-c", "pg-a", "terminating")}},
			deleteExpUID:    map[string][]types.UID{"pg-a": {"terminating"}},
			expected:        map[string]int32{},
		},
		{
			// A terminating pod is counted as present and is also carried in the delete expectations, so
			// it does not offset the deficit. The deficit is measured against the non-terminating pods
			// alone, so a replacement is created for every missing pod.
			name:            "a terminating pod does not offset the deficit from non-terminating pods",
			desired:         map[string]int32{"pg-a": 3},
			livePods:        map[string][]*corev1.Pod{"pg-a": {nonTerminatingPodInPodGang("pod-a", "pg-a", "")}},
			terminatingPods: map[string][]*corev1.Pod{"pg-a": {terminatingPodInPodGang("pod-b", "pg-a", "terminating")}},
			deleteExpUID:    map[string][]types.UID{"pg-a": {"terminating"}},
			expected:        map[string]int32{"pg-a": 2},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := expect.NewExpectationsStore()
			podsByPodGang := make(map[string]podGangPods, len(tc.livePods))
			for podGangName, pods := range tc.livePods {
				podsByPodGang[podGangName] = podGangPods{nonTerminating: pods, terminating: tc.terminatingPods[podGangName]}
				if uids := tc.createExpUID[podGangName]; len(uids) > 0 {
					require.NoError(t, store.ExpectCreations(logr.Discard(), keyFor(podGangName), uids...))
				}
				if uids := tc.deleteExpUID[podGangName]; len(uids) > 0 {
					require.NoError(t, store.ExpectDeletions(logr.Discard(), keyFor(podGangName), uids...))
				}
			}
			r := _resource{expectationsStore: store}
			ss := &syncSnapshot{pclq: pclq}

			actual, err := r.computeCountDeltaByPodGang(ss, tc.desired, podsByPodGang)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

// TestReconcileUnassignedPods verifies unassigned standalone pods are relabeled to the single anchor
// PodGang, or deleted when multiple anchors make the target ambiguous, and that a clean set is a
// no-op.
func TestReconcileUnassignedPods(t *testing.T) {
	anchor0 := podGangNameForEpoch(testAnchor0Epoch)
	anchor1 := podGangNameForEpoch(testAnchor1Epoch)

	t.Run("single anchor relabels the labelless pod in place", func(t *testing.T) {
		pod := testutils.NewPodBuilder("pod-a", testNamespace).Build()
		cl := testutils.NewTestClientBuilder().WithObjects(pod).Build()
		r := &_resource{client: cl}

		repaired, err := r.reconcileUnassignedPods(context.Background(), logr.Discard(), map[string]int32{anchor0: 3}, []*corev1.Pod{pod})
		require.NoError(t, err)
		assert.True(t, repaired)

		updated := &corev1.Pod{}
		require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pod), updated))
		assert.Equal(t, anchor0, updated.Labels[apicommon.LabelPodGang])
	})

	t.Run("multiple anchors delete the labelless pod for recreation", func(t *testing.T) {
		pod := testutils.NewPodBuilder("pod-a", testNamespace).Build()
		cl := testutils.NewTestClientBuilder().WithObjects(pod).Build()
		r := &_resource{client: cl}

		repaired, err := r.reconcileUnassignedPods(context.Background(), logr.Discard(), map[string]int32{anchor0: 3, anchor1: 2}, []*corev1.Pod{pod})
		require.NoError(t, err)
		assert.True(t, repaired)

		err = cl.Get(context.Background(), client.ObjectKeyFromObject(pod), &corev1.Pod{})
		assert.True(t, apierrors.IsNotFound(err), "labelless pod should be deleted")
	})

	t.Run("no unassigned pods is a no-op", func(t *testing.T) {
		cl := testutils.NewTestClientBuilder().Build()
		r := &_resource{client: cl}

		repaired, err := r.reconcileUnassignedPods(context.Background(), logr.Discard(), map[string]int32{anchor0: 3}, nil)
		require.NoError(t, err)
		assert.False(t, repaired)
	})

	t.Run("single anchor propagates a relabel failure", func(t *testing.T) {
		pod := testutils.NewPodBuilder("pod-a", testNamespace).Build()
		cl := testutils.NewTestClientBuilder().WithObjects(pod).
			RecordErrorForObjects(testutils.ClientMethodPatch, apierrors.NewInternalError(errors.New("boom")), client.ObjectKeyFromObject(pod)).
			Build()
		r := &_resource{client: cl}

		repaired, err := r.reconcileUnassignedPods(context.Background(), logr.Discard(), map[string]int32{anchor0: 3}, []*corev1.Pod{pod})
		require.Error(t, err)
		assert.False(t, repaired)
	})

	t.Run("multiple anchors propagate a delete failure", func(t *testing.T) {
		pod := testutils.NewPodBuilder("pod-a", testNamespace).Build()
		cl := testutils.NewTestClientBuilder().WithObjects(pod).
			RecordErrorForObjects(testutils.ClientMethodDelete, apierrors.NewInternalError(errors.New("boom")), client.ObjectKeyFromObject(pod)).
			Build()
		r := &_resource{client: cl}

		repaired, err := r.reconcileUnassignedPods(context.Background(), logr.Discard(), map[string]int32{anchor0: 3, anchor1: 2}, []*corev1.Pod{pod})
		require.Error(t, err)
		assert.False(t, repaired)
	})
}

// TestReconcileStandalonePCLQDistributionEarlyReturn verifies the distribution requeues before
// reconciling pods when the PodGangMap is not ready, a labelless pod was repaired, or the PodGangMap
// has not yet absorbed a scale.
func TestReconcileStandalonePCLQDistributionEarlyReturn(t *testing.T) {
	labellessPod := testutils.NewPodBuilder("pod-a", testNamespace).Build()

	tests := []struct {
		name    string
		ss      *syncSnapshot
		objects []client.Object
	}{
		{
			name: "requeues when the PodGangMap has no anchor entry for the clique",
			ss:   &syncSnapshot{pcs: pcs(), pcsReplicaIndex: testPCSReplicaIndex, pclq: pclqWithReplicas(1), cliqueName: testCliqueName, pgm: pgmWithEntries()},
		},
		{
			name: "requeues when the PodGangMap carries no count for the clique but the clique wants replicas",
			ss: &syncSnapshot{
				pcs: pcs(), pcsReplicaIndex: testPCSReplicaIndex, pclq: pclqWithReplicas(1), cliqueName: testCliqueName,
				pgm: pgmWithEntries(anchorEntryWithCliques(testAnchor0Epoch, 0, map[string]int32{"other-clique": 1})),
			},
		},
		{
			name: "requeues after repairing a labelless pod",
			ss: &syncSnapshot{
				pcs: pcs(), pcsReplicaIndex: testPCSReplicaIndex, pclq: pclqWithReplicas(1), cliqueName: testCliqueName,
				pgm:              pgmWithEntries(anchorEntryWithCliques(testAnchor0Epoch, 0, map[string]int32{testCliqueName: 1})),
				existingPCLQPods: []*corev1.Pod{labellessPod},
			},
			objects: []client.Object{labellessPod},
		},
		{
			name: "requeues while waiting for the PodGangMap to absorb a scale",
			ss: &syncSnapshot{
				pcs: pcs(), pcsReplicaIndex: testPCSReplicaIndex, pclq: pclqWithReplicas(10), cliqueName: testCliqueName,
				pgm: pgmWithEntries(
					anchorEntryWithCliques(testAnchor0Epoch, 0, map[string]int32{testCliqueName: 3}),
					anchorEntryWithCliques(testAnchor1Epoch, 1, map[string]int32{testCliqueName: 3}),
				),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			builder := testutils.NewTestClientBuilder()
			for _, obj := range tc.objects {
				builder = builder.WithObjects(obj)
			}
			r := &_resource{client: builder.Build()}

			err := r.reconcileStandalonePCLQDistribution(context.Background(), logr.Discard(), tc.ss)
			testutils.AssertGroveError(t, &groveerr.GroveError{Code: groveerr.ErrCodeRequeueAfter, Operation: component.OperationSync}, err)
		})
	}
}

// TestReconcileStandalonePCLQDistributionCreateAndDelete verifies the full non-early-return path
// creates the deficit pods for the anchor PodGang and deletes the excess pods, driving the real
// pod-creation build path through the default scheduler backend.
func TestReconcileStandalonePCLQDistributionCreateAndDelete(t *testing.T) {
	anchor0 := podGangNameForEpoch(testAnchor0Epoch)
	registry := testutils.NewDefaultFakeRegistry()
	podSpec := corev1.PodSpec{
		SchedulerName: string(configv1alpha1.SchedulerNameKube),
		Containers:    []corev1.Container{{Name: "worker", Image: "worker"}},
	}
	testPCS := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder(testCliqueName).WithPodSpec(podSpec).Build()).
		Build()
	// The PodClique name must be a real FQN so buildResource can resolve the PCS replica index from it.
	newPCLQ := func(replicas int32) *grovecorev1alpha1.PodClique {
		pclq := testutils.NewPodCliqueBuilder(testPCSName, "uid", testCliqueName, testNamespace, testPCSReplicaIndex).WithReplicas(replicas).Build()
		pclq.Spec.PodSpec = *podSpec.DeepCopy()
		return pclq
	}

	t.Run("creates the deficit pods for the anchor PodGang", func(t *testing.T) {
		pclq := newPCLQ(3)
		cl := testutils.NewTestClientBuilder().WithObjects(pclq).Build()
		r := _resource{client: cl, scheme: cl.Scheme(), schedRegistry: registry, eventRecorder: record.NewFakeRecorder(64), expectationsStore: expect.NewExpectationsStore()}
		ss := &syncSnapshot{
			pcs: testPCS, pclq: pclq, pcsReplicaIndex: testPCSReplicaIndex, cliqueName: testCliqueName,
			pgm: pgmWithEntries(anchorEntryWithCliques(testAnchor0Epoch, 0, map[string]int32{testCliqueName: 3})),
		}

		err := r.reconcileStandalonePCLQDistribution(context.Background(), logr.Discard(), ss)
		require.NoError(t, err)

		pods := listPodsForPodGang(t, cl, anchor0)
		assert.Len(t, pods, 3)
	})

	t.Run("deletes the excess pods for the anchor PodGang", func(t *testing.T) {
		pclq := newPCLQ(1)
		excess := []*corev1.Pod{nonTerminatingPodInPodGang("pod-a", anchor0, ""), nonTerminatingPodInPodGang("pod-b", anchor0, ""), nonTerminatingPodInPodGang("pod-c", anchor0, "")}
		cl := testutils.NewTestClientBuilder().WithObjects(pclq, excess[0], excess[1], excess[2]).Build()
		r := _resource{client: cl, scheme: cl.Scheme(), schedRegistry: registry, eventRecorder: record.NewFakeRecorder(64), expectationsStore: expect.NewExpectationsStore()}
		ss := &syncSnapshot{
			pcs: testPCS, pclq: pclq, pcsReplicaIndex: testPCSReplicaIndex, cliqueName: testCliqueName,
			pgm:              pgmWithEntries(anchorEntryWithCliques(testAnchor0Epoch, 0, map[string]int32{testCliqueName: 1})),
			existingPCLQPods: excess,
		}

		err := r.reconcileStandalonePCLQDistribution(context.Background(), logr.Discard(), ss)
		require.NoError(t, err)

		pods := listPodsForPodGang(t, cl, anchor0)
		assert.Len(t, pods, 1)
	})

	t.Run("scaled to zero deletes all pods when the PodGangMap has no anchor entry", func(t *testing.T) {
		pclq := newPCLQ(0)
		live := []*corev1.Pod{nonTerminatingPodInPodGang("pod-a", anchor0, ""), nonTerminatingPodInPodGang("pod-b", anchor0, "")}
		cl := testutils.NewTestClientBuilder().WithObjects(pclq, live[0], live[1]).Build()
		r := _resource{client: cl, scheme: cl.Scheme(), schedRegistry: registry, eventRecorder: record.NewFakeRecorder(64), expectationsStore: expect.NewExpectationsStore()}
		ss := &syncSnapshot{
			pcs: testPCS, pclq: pclq, pcsReplicaIndex: testPCSReplicaIndex, cliqueName: testCliqueName,
			pgm:              pgmWithEntries(),
			existingPCLQPods: live,
		}

		err := r.reconcileStandalonePCLQDistribution(context.Background(), logr.Discard(), ss)
		require.NoError(t, err)

		pods := listPodsForPodGang(t, cl, anchor0)
		assert.Empty(t, pods)
	})

	t.Run("propagates a pod deletion failure", func(t *testing.T) {
		pclq := newPCLQ(1)
		excess := []*corev1.Pod{nonTerminatingPodInPodGang("pod-a", anchor0, ""), nonTerminatingPodInPodGang("pod-b", anchor0, "")}
		cl := testutils.NewTestClientBuilder().WithObjects(pclq, excess[0], excess[1]).
			RecordErrorForObjects(testutils.ClientMethodDelete, apierrors.NewInternalError(errors.New("boom")), client.ObjectKeyFromObject(excess[0]), client.ObjectKeyFromObject(excess[1])).
			Build()
		r := _resource{client: cl, scheme: cl.Scheme(), schedRegistry: registry, eventRecorder: record.NewFakeRecorder(64), expectationsStore: expect.NewExpectationsStore()}
		ss := &syncSnapshot{
			pcs: testPCS, pclq: pclq, pcsReplicaIndex: testPCSReplicaIndex, cliqueName: testCliqueName,
			pgm:              pgmWithEntries(anchorEntryWithCliques(testAnchor0Epoch, 0, map[string]int32{testCliqueName: 1})),
			existingPCLQPods: excess,
		}

		err := r.reconcileStandalonePCLQDistribution(context.Background(), logr.Discard(), ss)
		require.Error(t, err)
	})

	t.Run("redistribution deletes from the shrinking anchor and creates on the growing anchor", func(t *testing.T) {
		anchor1 := podGangNameForEpoch(testAnchor1Epoch)
		pclq := newPCLQ(3)
		// anchor0 currently holds all 3 pods; the redistribution moves to anchor0:1, anchor1:2. The live
		// pods carry hostname indices so creating the replacements can pick free indices.
		live := []*corev1.Pod{
			nonTerminatingPodInPodGang("pod-a", anchor0, "worker-0"),
			nonTerminatingPodInPodGang("pod-b", anchor0, "worker-1"),
			nonTerminatingPodInPodGang("pod-c", anchor0, "worker-2"),
		}
		cl := testutils.NewTestClientBuilder().WithObjects(pclq, live[0], live[1], live[2]).Build()
		r := _resource{client: cl, scheme: cl.Scheme(), schedRegistry: registry, eventRecorder: record.NewFakeRecorder(64), expectationsStore: expect.NewExpectationsStore()}
		ss := &syncSnapshot{
			pcs: testPCS, pclq: pclq, pcsReplicaIndex: testPCSReplicaIndex, cliqueName: testCliqueName,
			pgm: pgmWithEntries(
				anchorEntryWithCliques(testAnchor0Epoch, 0, map[string]int32{testCliqueName: 1}),
				anchorEntryWithCliques(testAnchor1Epoch, 1, map[string]int32{testCliqueName: 2}),
			),
			existingPCLQPods: live,
		}

		err := r.reconcileStandalonePCLQDistribution(context.Background(), logr.Discard(), ss)
		require.NoError(t, err)

		assert.Len(t, listPodsForPodGang(t, cl, anchor0), 1)
		assert.Len(t, listPodsForPodGang(t, cl, anchor1), 2)
	})

	t.Run("redistribution creates no replacements when the delete on the shrinking anchor fails", func(t *testing.T) {
		anchor1 := podGangNameForEpoch(testAnchor1Epoch)
		pclq := newPCLQ(3)
		live := []*corev1.Pod{nonTerminatingPodInPodGang("pod-a", anchor0, ""), nonTerminatingPodInPodGang("pod-b", anchor0, ""), nonTerminatingPodInPodGang("pod-c", anchor0, "")}
		cl := testutils.NewTestClientBuilder().WithObjects(pclq, live[0], live[1], live[2]).
			RecordErrorForObjects(testutils.ClientMethodDelete, apierrors.NewInternalError(errors.New("boom")), client.ObjectKeyFromObject(live[0]), client.ObjectKeyFromObject(live[1]), client.ObjectKeyFromObject(live[2])).
			Build()
		r := _resource{client: cl, scheme: cl.Scheme(), schedRegistry: registry, eventRecorder: record.NewFakeRecorder(64), expectationsStore: expect.NewExpectationsStore()}
		ss := &syncSnapshot{
			pcs: testPCS, pclq: pclq, pcsReplicaIndex: testPCSReplicaIndex, cliqueName: testCliqueName,
			pgm: pgmWithEntries(
				anchorEntryWithCliques(testAnchor0Epoch, 0, map[string]int32{testCliqueName: 1}),
				anchorEntryWithCliques(testAnchor1Epoch, 1, map[string]int32{testCliqueName: 2}),
			),
			existingPCLQPods: live,
		}

		err := r.reconcileStandalonePCLQDistribution(context.Background(), logr.Discard(), ss)
		require.Error(t, err)
		// Deletes run before creates, so a delete failure short-circuits before any replacement is created.
		assert.Empty(t, listPodsForPodGang(t, cl, anchor1))
	})
}

// TestGroupPodsByPodGang verifies pods are grouped by their grove.io/podgang label into their
// non-terminating and terminating pods, non-terminating pods without the label are returned as
// unassigned, and terminating pods without the label are ignored.
func TestGroupPodsByPodGang(t *testing.T) {
	t.Run("groups labeled pods by PodGang and termination state", func(t *testing.T) {
		aliveA1 := nonTerminatingPodInPodGang("pod-a1", "pg-a", "")
		aliveA2 := nonTerminatingPodInPodGang("pod-a2", "pg-a", "")
		dyingA := nonTerminatingPodInPodGang("pod-a3", "pg-a", "")
		dyingA.DeletionTimestamp = &metav1.Time{Time: time.Now()}
		aliveB := nonTerminatingPodInPodGang("pod-b1", "pg-b", "")

		podsByPodGang, unassignedPods := groupPodsByPodGang([]*corev1.Pod{aliveA1, aliveA2, dyingA, aliveB})

		assert.ElementsMatch(t, []string{"pod-a1", "pod-a2"}, podNamesOf(podsByPodGang["pg-a"].nonTerminating))
		assert.ElementsMatch(t, []string{"pod-a3"}, podNamesOf(podsByPodGang["pg-a"].terminating))
		assert.ElementsMatch(t, []string{"pod-b1"}, podNamesOf(podsByPodGang["pg-b"].nonTerminating))
		assert.Empty(t, unassignedPods)
	})

	t.Run("returns a non-terminating pod without the label as unassigned", func(t *testing.T) {
		labelless := testutils.NewPodBuilder("pod-a", testNamespace).Build()
		podsByPodGang, unassignedPods := groupPodsByPodGang([]*corev1.Pod{labelless})
		assert.Empty(t, podsByPodGang)
		assert.ElementsMatch(t, []string{"pod-a"}, podNamesOf(unassignedPods))
	})

	t.Run("ignores a terminating pod without the label", func(t *testing.T) {
		labelless := testutils.NewPodBuilder("pod-a", testNamespace).Build()
		labelless.DeletionTimestamp = &metav1.Time{Time: time.Now()}
		podsByPodGang, unassignedPods := groupPodsByPodGang([]*corev1.Pod{labelless})
		assert.Empty(t, podsByPodGang)
		assert.Empty(t, unassignedPods)
	})

	t.Run("returns an empty map and no unassigned pods for no pods", func(t *testing.T) {
		podsByPodGang, unassignedPods := groupPodsByPodGang(nil)
		assert.Empty(t, podsByPodGang)
		assert.Empty(t, unassignedPods)
	})
}

// TestSumCounts verifies the total across all PodGang counts.
func TestSumCounts(t *testing.T) {
	tests := []struct {
		name           string
		countByPodGang map[string]int32
		expected       int32
	}{
		{"sums multiple counts", map[string]int32{"pg-a": 3, "pg-b": 2}, 5},
		{"single count returns that count", map[string]int32{"pg-a": 4}, 4},
		{"no counts sum to zero", nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, sumCounts(tc.countByPodGang))
		})
	}
}

// TestBuildPerPodGangDeletionTasks verifies one deletion task is built per excess pod, that the
// DeletionSorter prefers the Pending pod, and that non-negative deltas and empty groups build no
// tasks.
func TestBuildPerPodGangDeletionTasks(t *testing.T) {
	r := &_resource{expectationsStore: expect.NewExpectationsStore()}
	ss := &syncSnapshot{pcs: pcs(), pclq: pclqWithReplicas(1), cliqueName: testCliqueName}

	t.Run("builds one task per excess pod picking the Pending pod first", func(t *testing.T) {
		pending := testutils.NewPodBuilder("pod-pending", testNamespace).WithPhase(corev1.PodPending).Build()
		running := testutils.NewPodBuilder("pod-running", testNamespace).WithPhase(corev1.PodRunning).Build()
		podsByPodGang := map[string]podGangPods{"pg-a": {nonTerminating: []*corev1.Pod{running, pending}}}

		tasks := r.buildPerPodGangDeletionTasks(logr.Discard(), ss, map[string]int32{"pg-a": -1}, []string{"pg-a"}, podsByPodGang)

		require.Len(t, tasks, 1)
		assert.Contains(t, tasks[0].Name, "pod-pending")
	})

	t.Run("skips a PodGang whose delta is non-negative", func(t *testing.T) {
		podsByPodGang := map[string]podGangPods{"pg-a": {nonTerminating: []*corev1.Pod{nonTerminatingPodInPodGang("pod-a", "pg-a", "")}}}
		tasks := r.buildPerPodGangDeletionTasks(logr.Discard(), ss, map[string]int32{"pg-a": 0}, []string{"pg-a"}, podsByPodGang)
		assert.Empty(t, tasks)
	})

	t.Run("skips a PodGang with no live pods", func(t *testing.T) {
		tasks := r.buildPerPodGangDeletionTasks(logr.Discard(), ss, map[string]int32{"pg-a": -2}, []string{"pg-a"}, map[string]podGangPods{})
		assert.Empty(t, tasks)
	})
}

// TestBuildPerPodGangCreationTasks verifies one creation task is built per deficit pod across
// PodGangs, and no tasks when nothing is to be created.
func TestBuildPerPodGangCreationTasks(t *testing.T) {
	r := &_resource{expectationsStore: expect.NewExpectationsStore()}
	ss := &syncSnapshot{pcs: pcs(), pclq: pclqWithReplicas(1), cliqueName: testCliqueName}

	t.Run("builds one task per deficit pod across PodGangs", func(t *testing.T) {
		tasks, err := r.buildPerPodGangCreationTasks(logr.Discard(), ss, map[string]int32{"pg-a": 2, "pg-b": 1}, []string{"pg-a", "pg-b"})
		require.NoError(t, err)
		assert.Len(t, tasks, 3)
	})

	t.Run("builds no tasks when there is no deficit", func(t *testing.T) {
		tasks, err := r.buildPerPodGangCreationTasks(logr.Discard(), ss, map[string]int32{"pg-a": -1}, []string{"pg-a"})
		require.NoError(t, err)
		assert.Nil(t, tasks)
	})
}

// anchorEntryWithCliques builds an Anchor PodGangEntry carrying the given standalone PodClique counts.
func anchorEntryWithCliques(epoch string, anchorIndex int32, cliques map[string]int32) grovecorev1alpha1.PodGangEntry {
	return testutils.NewPodGangEntryBuilder("hash", epoch).
		WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
		WithAnchorIndex(anchorIndex).
		WithPodCliques(cliques).
		Build()
}

// pclqWithReplicas returns a standalone PodClique named after testCliqueName with the given replicas.
func pclqWithReplicas(replicas int32) *grovecorev1alpha1.PodClique {
	return &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: testCliqueName, Namespace: testNamespace},
		Spec:       grovecorev1alpha1.PodCliqueSpec{Replicas: replicas},
	}
}

// listPodsForPodGang lists pods in the test namespace carrying the given PodGang label.
func listPodsForPodGang(t *testing.T, cl client.Client, podGangName string) []corev1.Pod {
	t.Helper()
	podList := &corev1.PodList{}
	require.NoError(t, cl.List(context.Background(), podList, client.InNamespace(testNamespace)))
	matching := make([]corev1.Pod, 0, len(podList.Items))
	for i := range podList.Items {
		if podList.Items[i].Labels[apicommon.LabelPodGang] == podGangName {
			matching = append(matching, podList.Items[i])
		}
	}
	return matching
}

// podNamesOf returns the names of the given pods.
func podNamesOf(pods []*corev1.Pod) []string {
	return lo.Map(pods, func(pod *corev1.Pod, _ int) string {
		return pod.GetName()
	})
}

// nonTerminatingPodInPodGang builds a non-terminating pod assigned to podGangName via the
// grove.io/podgang label, with the given hostname so an available host-name index can be derived
// from it when replacements are created. Pass an empty hostname when the test does not create pods.
func nonTerminatingPodInPodGang(name, podGangName, hostname string) *corev1.Pod {
	pod := testutils.NewPodBuilder(name, testNamespace).
		WithLabels(map[string]string{apicommon.LabelPodGang: podGangName}).
		Build()
	pod.Spec.Hostname = hostname
	return pod
}

// podWithUID builds a non-terminating pod assigned to podGangName and carrying the given UID.
func podWithUID(name, podGangName string, uid types.UID) *corev1.Pod {
	pod := nonTerminatingPodInPodGang(name, podGangName, "")
	pod.UID = uid
	return pod
}

// terminatingPodInPodGang builds a terminating pod assigned to podGangName and carrying the given
// UID. A terminating pod has a deletion timestamp but still occupies its slot until its grace period
// elapses.
func terminatingPodInPodGang(name, podGangName string, uid types.UID) *corev1.Pod {
	pod := podWithUID(name, podGangName, uid)
	pod.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	return pod
}

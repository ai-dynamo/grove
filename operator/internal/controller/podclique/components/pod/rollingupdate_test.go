/*
Copyright 2025 The Grove Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pod

import (
	"context"
	"fmt"
	"testing"

	"github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/expect"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testNewHash = "new-hash-abc"
	testOldHash = "old-hash-xyz"
	testNS      = "test-ns"
)

func TestComputeUpdateWork(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bucket
	}{
		{"old pending", newTestPod("old-pending", testOldHash, withPhase(corev1.PodPending)), bucketOldPending},
		{"old unhealthy (started, not ready)", newTestPod("old-unhealthy-started", testOldHash, withPhase(corev1.PodRunning), withContainerStatus(ptr.To(true), false)), bucketOldUnhealthy},
		{"old unhealthy (erroneous exit)", newTestPod("old-unhealthy-exit", testOldHash, withPhase(corev1.PodRunning), withErroneousExit()), bucketOldUnhealthy},
		{"old ready", newTestPod("old-ready", testOldHash, withPhase(corev1.PodRunning), withReadyCondition(), withContainerStatus(ptr.To(true), true)), bucketOldReady},
		{"old starting (Started=false)", newTestPod("old-starting-false", testOldHash, withPhase(corev1.PodRunning), withContainerStatus(ptr.To(false), false)), bucketOldStarting},
		{"old starting (Started=nil)", newTestPod("old-starting-nil", testOldHash, withPhase(corev1.PodRunning), withContainerStatus(nil, false)), bucketOldStarting},
		{"old uncategorized (no containers)", newTestPod("old-uncategorized", testOldHash, withPhase(corev1.PodRunning)), bucketOldUncategorized},
		{"old terminating is skipped", newTestPod("old-terminating", testOldHash, withDeletionTimestamp()), bucketSkipped},
		{"new ready", newTestPod("new-ready", testNewHash, withPhase(corev1.PodRunning), withReadyCondition(), withContainerStatus(ptr.To(true), true)), bucketNewReady},
		{"new not-ready is not tracked", newTestPod("new-not-ready", testNewHash, withPhase(corev1.PodRunning), withContainerStatus(ptr.To(false), false)), bucketSkipped},
	}

	r := _resource{expectationsStore: expect.NewExpectationsStore()}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := &syncContext{
				existingPCLQPods:         []*corev1.Pod{tt.pod},
				expectedPodTemplateHash:  testNewHash,
				pclqExpectationsStoreKey: "test-key",
			}
			work := r.computeUpdateWork(logr.Discard(), sc)

			bucketPods := map[bucket][]*corev1.Pod{
				bucketOldPending:       work.oldTemplateHashPendingPods,
				bucketOldUnhealthy:     work.oldTemplateHashUnhealthyPods,
				bucketOldStarting:      work.oldTemplateHashStartingPods,
				bucketOldUncategorized: work.oldTemplateHashUncategorizedPods,
				bucketOldReady:         work.oldTemplateHashReadyPods,
				bucketNewReady:         work.newTemplateHashReadyPods,
			}

			bucketNames := map[bucket]string{
				bucketOldPending:       "oldPending",
				bucketOldUnhealthy:     "oldUnhealthy",
				bucketOldStarting:      "oldStarting",
				bucketOldUncategorized: "oldUncategorized",
				bucketOldReady:         "oldReady",
				bucketNewReady:         "newReady",
			}
			for b, pods := range bucketPods {
				name := bucketNames[b]
				if b == tt.expected {
					assert.Len(t, pods, 1, fmt.Sprintf("expected pod in bucket %s", name))
				} else {
					assert.Empty(t, pods, fmt.Sprintf("expected no pods in bucket %s", name))
				}
			}
		})
	}
}

// newTestPod creates a pod with the given name, template hash label, and options applied.
func newTestPod(name, templateHash string, opts ...func(*corev1.Pod)) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNS,
			Labels: map[string]string{
				common.LabelPodTemplateHash: templateHash,
			},
		},
	}
	for _, opt := range opts {
		opt(pod)
	}
	return pod
}

func withPhase(phase corev1.PodPhase) func(*corev1.Pod) {
	return func(pod *corev1.Pod) { pod.Status.Phase = phase }
}

func withReadyCondition() func(*corev1.Pod) {
	return func(pod *corev1.Pod) {
		pod.Status.Conditions = append(pod.Status.Conditions, corev1.PodCondition{
			Type: corev1.PodReady, Status: corev1.ConditionTrue,
		})
	}
}

func withContainerStatus(started *bool, ready bool) func(*corev1.Pod) {
	return func(pod *corev1.Pod) {
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{
			Name: "main", Started: started, Ready: ready,
		})
	}
}

func withErroneousExit() func(*corev1.Pod) {
	return func(pod *corev1.Pod) {
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{
			Name: "main",
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{ExitCode: 1},
			},
		})
	}
}

func withDeletionTimestamp() func(*corev1.Pod) {
	return func(pod *corev1.Pod) {
		now := metav1.Now()
		pod.DeletionTimestamp = &now
		pod.Finalizers = []string{"fake.finalizer/test"}
	}
}

// TestComputeUpdateWorkPCSHashFlipDoesNotProduceOldHashWork pins the rolling-update
// invariant that "PCS-level generation hash flipped, but per-PCLQ pod-template hash
// did NOT" produces no deletion work.
//
// Concretely: when the upstream operator reorders the cliques map-list (a
// +listType=map +listMapKey=name field) but does not modify any individual
// clique's content, the per-PCLQ ComputePCLQPodTemplateHash output is unchanged
// from what is already stamped on existing pod labels. The PCLQ controller's
// initOrResetUpdate then records that same (unchanged) hash in
// pclq.Status.UpdateProgress.PodTemplateHash. When pod-level processPendingUpdates
// runs, prepareSyncFlow recomputes sc.expectedPodTemplateHash via the same
// function, so it matches every pod's LabelPodTemplateHash. computeUpdateWork
// must therefore put every pod in newTemplateHashReadyPods and leave every
// "old" bucket empty.
//
// This is the precondition that lets processPendingUpdates fall through to
// markRollingUpdateEnd without deleting any pods. If this test fails, the
// "slice reorder alone causes pod deletion" hypothesis is correct. If it
// passes, that hypothesis is refuted: a separate per-PCLQ template change is
// required to drive the actual gang roll.
func TestComputeUpdateWorkPCSHashFlipDoesNotProduceOldHashWork(t *testing.T) {
	const sharedHash = "per-pclq-hash-stable"

	// All four pods carry the same per-PCLQ pod-template hash on the
	// LabelPodTemplateHash label — the same hash the PCS template would
	// produce for this clique even after a sibling clique was reordered in
	// the cliques slice.
	existingPods := []*corev1.Pod{
		newTestPod("frontend-r0-pod-0", sharedHash, withPhase(corev1.PodRunning), withReadyCondition(), withContainerStatus(ptr.To(true), true)),
		newTestPod("planner-r0-pod-0", sharedHash, withPhase(corev1.PodRunning), withReadyCondition(), withContainerStatus(ptr.To(true), true)),
		newTestPod("decode-r0-pod-0", sharedHash, withPhase(corev1.PodRunning), withReadyCondition(), withContainerStatus(ptr.To(true), true)),
		newTestPod("prefill-r0-pod-0", sharedHash, withPhase(corev1.PodRunning), withReadyCondition(), withContainerStatus(ptr.To(true), true)),
	}

	r := _resource{expectationsStore: expect.NewExpectationsStore()}
	sc := &syncContext{
		existingPCLQPods: existingPods,
		// expectedPodTemplateHash matches every pod's LabelPodTemplateHash
		// because ComputePCLQPodTemplateHash for an unchanged per-clique
		// template returns an unchanged value — irrespective of whether the
		// PCS-level computeGenerationHash flipped due to clique reorder.
		expectedPodTemplateHash:  sharedHash,
		pclqExpectationsStoreKey: "test-key",
	}

	work := r.computeUpdateWork(logr.Discard(), sc)

	assert.Empty(t, work.oldTemplateHashPendingPods, "no pod should be classified as old-pending when per-PCLQ hash is unchanged")
	assert.Empty(t, work.oldTemplateHashUnhealthyPods, "no pod should be classified as old-unhealthy when per-PCLQ hash is unchanged")
	assert.Empty(t, work.oldTemplateHashStartingPods, "no pod should be classified as old-starting when per-PCLQ hash is unchanged")
	assert.Empty(t, work.oldTemplateHashUncategorizedPods, "no pod should be classified as old-uncategorized when per-PCLQ hash is unchanged")
	assert.Empty(t, work.oldTemplateHashReadyPods, "no pod should be classified as old-ready when per-PCLQ hash is unchanged")
	assert.Len(t, work.newTemplateHashReadyPods, len(existingPods),
		"every existing ready pod must land in the new-template-hash bucket when per-PCLQ hash is unchanged; otherwise processPendingUpdates would delete them")

	// processPendingUpdates uses these same buckets to decide what to delete.
	// With every "old" bucket empty:
	//   - deleteOldNonReadyPods is a no-op (lo.Union of empty slices)
	//   - getPodNamesPendingUpdate returns no pods
	//   - nextPodToUpdate is nil
	//   - control falls through to markRollingUpdateEnd
	// i.e. NO pods are deleted from a pure clique-slice reorder.
	allOldBuckets := append([]*corev1.Pod{}, work.oldTemplateHashPendingPods...)
	allOldBuckets = append(allOldBuckets, work.oldTemplateHashUnhealthyPods...)
	allOldBuckets = append(allOldBuckets, work.oldTemplateHashStartingPods...)
	allOldBuckets = append(allOldBuckets, work.oldTemplateHashUncategorizedPods...)
	allOldBuckets = append(allOldBuckets, work.oldTemplateHashReadyPods...)
	assert.Empty(t, allOldBuckets,
		"sanity: union of all old-hash buckets must be empty — this is the precondition that prevents pod deletion in the rolling-update path")
}

// TestProcessPendingUpdatesPCSHashFlipDoesNotDeletePods runs the full
// processPendingUpdates flow against a fake client to settle the question:
// "If the PCS-level generation hash flipped purely from clique slice reorder,
// will the PCLQ pod-level rolling update delete pods?"
//
// Setup mirrors the failing scenario from the latency-mode bug:
//   - a PodClique with an in-progress rolling update whose target
//     PodTemplateHash equals what is already on every existing pod (the
//     per-PCLQ template content has not changed)
//   - 4 ready pods, all healthy, all carrying that hash
//
// Expected outcome: processPendingUpdates calls markRollingUpdateEnd
// (UpdateEndedAt set, ReadyPodsSelectedToUpdate cleared) and deletes nothing.
//
// If pods get deleted here, the "slice reorder alone causes the gang roll"
// hypothesis from the alternate RCA is correct. If they survive, that
// hypothesis is refuted and the actual gang roll requires a separate
// per-PCLQ template change (see
// TestComputePCLQPodTemplateHashSensitivityToSliceOrder).
func TestProcessPendingUpdatesPCSHashFlipDoesNotDeletePods(t *testing.T) {
	const sharedHash = "per-pclq-hash-stable"
	const pcsHashAfterReorder = "pcs-generation-hash-after-clique-reorder"
	const namespace = testNS
	const pclqName = "test-pclq"

	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pclqName,
			Namespace: namespace,
			Labels: map[string]string{
				common.LabelPodTemplateHash: sharedHash,
			},
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			Replicas:     4,
			MinAvailable: ptr.To(int32(1)),
		},
		Status: grovecorev1alpha1.PodCliqueStatus{
			Replicas:        4,
			ReadyReplicas:   4,
			UpdatedReplicas: 4,
			// Update was just reset by the PCLQ reconciler in response to a
			// PCS generation hash flip. The per-PCLQ template hash recorded
			// here is identical to what is already on the pod labels because
			// only the cliques map-list was reordered.
			UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{
				UpdateStartedAt:            metav1.Now(),
				PodCliqueSetGenerationHash: pcsHashAfterReorder,
				PodTemplateHash:            sharedHash,
			},
		},
	}

	makePod := func(name string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels: map[string]string{
					common.LabelPodTemplateHash: sharedHash,
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: corev1.ConditionTrue},
				},
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "main", Started: ptr.To(true), Ready: true},
				},
			},
		}
	}

	pods := []*corev1.Pod{
		makePod("pclq-pod-0"),
		makePod("pclq-pod-1"),
		makePod("pclq-pod-2"),
		makePod("pclq-pod-3"),
	}
	originalUIDs := make(map[string]string, len(pods))
	for _, p := range pods {
		originalUIDs[p.Name] = string(p.UID)
	}

	objs := []client.Object{pclq}
	for i := range pods {
		objs = append(objs, pods[i])
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&grovecorev1alpha1.PodClique{}).
		Build()

	r := _resource{
		client:            fakeClient,
		scheme:            scheme,
		eventRecorder:     record.NewFakeRecorder(32),
		expectationsStore: expect.NewExpectationsStore(),
	}

	sc := &syncContext{
		ctx:                      context.Background(),
		pclq:                     pclq,
		existingPCLQPods:         pods,
		expectedPodTemplateHash:  sharedHash,
		pclqExpectationsStoreKey: namespace + "/" + pclqName,
	}

	require.NoError(t, r.processPendingUpdates(logr.Discard(), sc),
		"processPendingUpdates must not error when there is no work to do")

	// 1. No pods deleted.
	remainingPods := &corev1.PodList{}
	require.NoError(t, fakeClient.List(context.Background(), remainingPods, client.InNamespace(namespace)))
	assert.Len(t, remainingPods.Items, len(pods),
		"no pods should be deleted when per-PCLQ pod-template hash is unchanged across the PCS hash flip")
	for _, p := range remainingPods.Items {
		assert.Nil(t, p.DeletionTimestamp,
			"pod %q must not be marked for deletion", p.Name)
	}

	// 2. Rolling update was marked complete (markRollingUpdateEnd ran).
	updatedPCLQ := &grovecorev1alpha1.PodClique{}
	require.NoError(t, fakeClient.Get(context.Background(),
		client.ObjectKey{Namespace: namespace, Name: pclqName}, updatedPCLQ))
	require.NotNil(t, updatedPCLQ.Status.UpdateProgress, "UpdateProgress must remain set")
	require.NotNil(t, updatedPCLQ.Status.UpdateProgress.UpdateEndedAt,
		"UpdateEndedAt must be set — markRollingUpdateEnd should have run because there was no real per-PCLQ work")
	assert.Nil(t, updatedPCLQ.Status.UpdateProgress.ReadyPodsSelectedToUpdate,
		"ReadyPodsSelectedToUpdate must be cleared by markRollingUpdateEnd")
}

// bucket identifies which updateWork bucket a pod should land in.
type bucket int

const (
	bucketOldPending bucket = iota
	bucketOldUnhealthy
	bucketOldStarting
	bucketOldUncategorized
	bucketOldReady
	bucketNewReady
	bucketSkipped // terminating pods — not in any bucket
)

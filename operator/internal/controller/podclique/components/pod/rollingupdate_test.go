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
	"k8s.io/apimachinery/pkg/types"
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
		// A currently-Ready, serving pod that merely carries a stale non-zero LastTerminationState
		// from an earlier restart must be treated as Ready (protected, one-at-a-time rolling path),
		// NOT as unhealthy. Otherwise deleteOldNonReadyPods hard-deletes a serving pod. Regression
		// for the harbinger-24b outage where both serving replicas were deleted in one reconcile.
		{"old ready with stale erroneous exit (currently serving)", newTestPod("old-ready-stale-exit", testOldHash, withPhase(corev1.PodRunning), withReadyCondition(), withReadyContainerPreviouslyErrored()), bucketOldReady},
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

// withUID assigns a distinct UID. Deletion expectations are keyed by UID; pods sharing an
// empty UID collapse in getPodNamesPendingUpdate, so full-flow tests must give each pod a
// unique UID to exercise multi-pod deletion behavior.
func withUID(uid string) func(*corev1.Pod) {
	return func(pod *corev1.Pod) { pod.UID = types.UID(uid) }
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

// withReadyContainerPreviouslyErrored sets a single container status that is currently
// started and ready but whose LastTerminationState records a prior non-zero exit — i.e.
// a serving pod that restarted erroneously at some earlier point and recovered.
func withReadyContainerPreviouslyErrored() func(*corev1.Pod) {
	return func(pod *corev1.Pod) {
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{
			Name:    "main",
			Started: ptr.To(true),
			Ready:   true,
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{ExitCode: 137},
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

// TestProcessPendingUpdatesPreservesServingPodWithStaleErroneousExit reproduces the
// harbinger-24b outage: replicas=2, minAvailable=1, where BOTH pods are currently Ready and
// serving, but one carries a stale non-zero LastTerminationState from an earlier restart.
// The rolling update must replace pods one-at-a-time (delete exactly one), never delete both
// serving replicas in a single reconcile.
func TestProcessPendingUpdatesPreservesServingPodWithStaleErroneousExit(t *testing.T) {
	const namespace = testNS
	const pclqName = "harbinger-24b-0-vllmdecodeworker"

	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pclqName,
			Namespace: namespace,
			Labels:    map[string]string{common.LabelPodTemplateHash: testOldHash},
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			Replicas:     2,
			MinAvailable: ptr.To(int32(1)),
		},
		Status: grovecorev1alpha1.PodCliqueStatus{
			Replicas:      2,
			ReadyReplicas: 2, // both pods are Ready per the status categorization (IsPodReady first)
			UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{
				UpdateStartedAt: metav1.Now(),
				PodTemplateHash: testNewHash,
			},
		},
	}

	// kf92w: plain Ready serving pod. phckw: Ready + serving but with a stale erroneous exit.
	kf92w := newTestPod("kf92w", testOldHash, withUID("uid-kf92w"), withPhase(corev1.PodRunning), withReadyCondition(), withContainerStatus(ptr.To(true), true))
	phckw := newTestPod("phckw", testOldHash, withUID("uid-phckw"), withPhase(corev1.PodRunning), withReadyCondition(), withReadyContainerPreviouslyErrored())
	pods := []*corev1.Pod{kf92w, phckw}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pclq, kf92w, phckw).
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
		expectedPodTemplateHash:  testNewHash,
		pclqExpectationsStoreKey: namespace + "/" + pclqName,
	}

	// processPendingUpdates requeues after selecting one pod; we only care how many it deleted.
	_ = r.processPendingUpdates(logr.Discard(), sc)

	remaining := &corev1.PodList{}
	require.NoError(t, fakeClient.List(context.Background(), remaining, client.InNamespace(namespace)))
	assert.Len(t, remaining.Items, 1,
		"exactly one pod may be deleted per reconcile; deleting both serving replicas caused the harbinger-24b outage")
}

// TestProcessPendingUpdatesDoesNotDeleteLastReadyPod verifies the minAvailable guard: with
// replicas=2, minAvailable=1, one Ready pod and one genuinely not-ready old pod, the rolling
// update may delete the not-ready pod but must NOT delete the last Ready pod (which would breach
// minAvailable). It must requeue and wait for a replacement to become Ready first.
func TestProcessPendingUpdatesDoesNotDeleteLastReadyPod(t *testing.T) {
	const namespace = testNS
	const pclqName = "test-pclq-minavailable"

	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pclqName,
			Namespace: namespace,
			Labels:    map[string]string{common.LabelPodTemplateHash: testOldHash},
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			Replicas:     2,
			MinAvailable: ptr.To(int32(1)),
		},
		Status: grovecorev1alpha1.PodCliqueStatus{
			Replicas:      2,
			ReadyReplicas: 1, // only the Ready pod counts
			UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{
				UpdateStartedAt: metav1.Now(),
				PodTemplateHash: testNewHash,
			},
		},
	}

	readyPod := newTestPod("ready-pod", testOldHash, withUID("uid-ready"), withPhase(corev1.PodRunning), withReadyCondition(), withContainerStatus(ptr.To(true), true))
	notReadyPod := newTestPod("not-ready-pod", testOldHash, withUID("uid-notready"), withPhase(corev1.PodPending))
	pods := []*corev1.Pod{readyPod, notReadyPod}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pclq, readyPod, notReadyPod).
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
		expectedPodTemplateHash:  testNewHash,
		pclqExpectationsStoreKey: namespace + "/" + pclqName,
	}

	_ = r.processPendingUpdates(logr.Discard(), sc)

	// The last Ready pod must survive; only the not-ready pod may be deleted.
	err := fakeClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "ready-pod"}, &corev1.Pod{})
	assert.NoError(t, err, "the last Ready pod must not be deleted — doing so breaches minAvailable and caused a 0-ready outage window")
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

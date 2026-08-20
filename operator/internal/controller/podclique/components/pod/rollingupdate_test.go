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
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
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

func TestProcessPendingUpdatesWaitsForAllUpdatedPodsReady(t *testing.T) {
	tests := []struct {
		name            string
		pods            []*corev1.Pod
		wantRequeue     bool
		wantReadyDetail string
	}{
		{
			name: "one of two updated pods ready with minAvailable one",
			pods: []*corev1.Pod{
				newTestPod("new-ready", testNewHash, withPhase(corev1.PodRunning), withReadyCondition(), withContainerStatus(ptr.To(true), true)),
				newTestPod("new-crash-looping", testNewHash, withPhase(corev1.PodRunning), withErroneousExit()),
			},
			wantRequeue:     true,
			wantReadyDetail: "1 ready, need 2",
		},
		{
			name: "all updated pods ready",
			pods: []*corev1.Pod{
				newTestPod("new-ready-0", testNewHash, withPhase(corev1.PodRunning), withReadyCondition(), withContainerStatus(ptr.To(true), true)),
				newTestPod("new-ready-1", testNewHash, withPhase(corev1.PodRunning), withReadyCondition(), withContainerStatus(ptr.To(true), true)),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pclq := &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pclq",
					Namespace: testNS,
				},
				Spec: grovecorev1alpha1.PodCliqueSpec{
					Replicas:     2,
					MinAvailable: ptr.To[int32](1),
				},
				Status: grovecorev1alpha1.PodCliqueStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{
						UpdateStartedAt:            metav1.Now(),
						PodCliqueSetGenerationHash: "new-generation-hash",
						PodTemplateHash:            testNewHash,
					},
				},
			}
			cl := testutils.SetupFakeClient(pclq)
			r := _resource{
				client:            cl,
				expectationsStore: expect.NewExpectationsStore(),
			}
			sc := &syncContext{
				ctx:                      context.Background(),
				pclq:                     pclq,
				existingPCLQPods:         tt.pods,
				expectedPodTemplateHash:  testNewHash,
				pclqExpectationsStoreKey: "test-key",
			}

			err := r.processPendingUpdates(logr.Discard(), sc)
			updatedPCLQ := &grovecorev1alpha1.PodClique{}
			require.NoError(t, cl.Get(context.Background(), types.NamespacedName{
				Name: pclq.Name, Namespace: pclq.Namespace,
			}, updatedPCLQ))

			if tt.wantRequeue {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantReadyDetail)
				assert.Nil(t, updatedPCLQ.Status.UpdateProgress.UpdateEndedAt)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, updatedPCLQ.Status.UpdateProgress.UpdateEndedAt)
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

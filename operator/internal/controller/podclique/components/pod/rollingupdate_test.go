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
	"fmt"
	"testing"

	"github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/internal/expect"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const (
	testNewHash = "new-hash-abc"
	testOldHash = "old-hash-xyz"
	testNS      = "test-ns"
)

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

func TestComputeUpdateWork(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bucket
	}{
		{"old pending", newTestPod("p", testOldHash, withPhase(corev1.PodPending)), bucketOldPending},
		{"old unhealthy (started, not ready)", newTestPod("p", testOldHash, withPhase(corev1.PodRunning), withContainerStatus(ptr.To(true), false)), bucketOldUnhealthy},
		{"old unhealthy (erroneous exit)", newTestPod("p", testOldHash, withPhase(corev1.PodRunning), withErroneousExit()), bucketOldUnhealthy},
		{"old ready", newTestPod("p", testOldHash, withPhase(corev1.PodRunning), withReadyCondition(), withContainerStatus(ptr.To(true), true)), bucketOldReady},
		{"old starting (Started=false)", newTestPod("p", testOldHash, withPhase(corev1.PodRunning), withContainerStatus(ptr.To(false), false)), bucketOldStarting},
		{"old starting (Started=nil)", newTestPod("p", testOldHash, withPhase(corev1.PodRunning), withContainerStatus(nil, false)), bucketOldStarting},
		{"old uncategorized (no containers)", newTestPod("p", testOldHash, withPhase(corev1.PodRunning)), bucketOldUncategorized},
		{"old terminating is skipped", newTestPod("p", testOldHash, withDeletionTimestamp()), bucketSkipped},
		{"new ready", newTestPod("p", testNewHash, withPhase(corev1.PodRunning), withReadyCondition(), withContainerStatus(ptr.To(true), true)), bucketNewReady},
		{"new not-ready is not tracked", newTestPod("p", testNewHash, withPhase(corev1.PodRunning), withContainerStatus(ptr.To(false), false)), bucketSkipped},
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

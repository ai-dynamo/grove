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
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/expect"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	testNewHash = "new-hash-abc"
	testOldHash = "old-hash-xyz"
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
			sc := &syncSnapshot{
				pclq:                    &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: "pclq-1", Namespace: testNamespace}},
				existingPCLQPods:        []*corev1.Pod{tt.pod},
				expectedPodTemplateHash: testNewHash,
			}
			work := r.computeUpdateWork(logr.Discard(), sc)

			bucketPods := map[bucket][]*corev1.Pod{
				bucketOldPending:       work.oldTemplateHashPendingPods,
				bucketOldUnhealthy:     work.oldTemplateHashUnhealthyPods,
				bucketOldStarting:      work.oldTemplateHashStartingPods,
				bucketOldUncategorized: work.oldTemplateHashUncategorizedPods,
				bucketOldReady:         work.oldTemplateHashReadyPods,
			}

			bucketNames := map[bucket]string{
				bucketOldPending:       "oldPending",
				bucketOldUnhealthy:     "oldUnhealthy",
				bucketOldStarting:      "oldStarting",
				bucketOldUncategorized: "oldUncategorized",
				bucketOldReady:         "oldReady",
			}
			for b, pods := range bucketPods {
				name := bucketNames[b]
				if b == tt.expected {
					assert.Len(t, pods, 1, fmt.Sprintf("expected pod in bucket %s", name))
				} else {
					assert.Empty(t, pods, fmt.Sprintf("expected no pods in bucket %s", name))
				}
			}

			wantNewReadyCount := 0
			if tt.expected == bucketNewReady {
				wantNewReadyCount = 1
			}
			assert.Equal(t, wantNewReadyCount, work.newReadyPodCount, "unexpected newReadyPodCount")

			wantAwaitingCount := 0
			if tt.pod.Labels[apicommon.LabelPodTemplateHash] == testOldHash && tt.expected != bucketSkipped {
				wantAwaitingCount = 1
			}
			assert.Equal(t, wantAwaitingCount, work.oldHashPodsAwaitingReplacement, "unexpected oldHashPodsAwaitingReplacement")
		})
	}
}

func TestSelectOldestPods(t *testing.T) {
	newest := newTestPod("newest", testOldHash, withAgeMinutes(1))
	middle := newTestPod("middle", testOldHash, withAgeMinutes(5))
	oldest := newTestPod("oldest", testOldHash, withAgeMinutes(10))

	t.Run("returns nil for non-positive n", func(t *testing.T) {
		assert.Nil(t, selectOldestPods([]*corev1.Pod{oldest, newest}, 0))
		assert.Nil(t, selectOldestPods([]*corev1.Pod{oldest, newest}, -1))
	})
	t.Run("returns nil for empty input", func(t *testing.T) {
		assert.Nil(t, selectOldestPods(nil, 2))
	})
	t.Run("selects the n oldest pods in order", func(t *testing.T) {
		got := selectOldestPods([]*corev1.Pod{newest, oldest, middle}, 2)
		assert.Equal(t, []*corev1.Pod{oldest, middle}, got)
	})
	t.Run("caps at the number of available pods", func(t *testing.T) {
		got := selectOldestPods([]*corev1.Pod{newest, oldest}, 5)
		assert.Len(t, got, 2)
	})
}

func TestProcessPendingUpdates(t *testing.T) {
	pcsWithMaxUnavailable := func(maxUnavailable int32) *grovecorev1alpha1.PodCliqueSet {
		return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy}).
			WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder(testCliqueName).WithMaxUnavailable(maxUnavailable).Build()).
			Build()
	}
	pclqUpdating := func(replicas, minAvailable int32) *grovecorev1alpha1.PodClique {
		return testutils.NewPodCliqueBuilder(testPCSName, "uid", testCliqueName, testNamespace, 0).
			WithReplicas(replicas).
			WithMinAvailable(minAvailable).
			WithOptions(func(p *grovecorev1alpha1.PodClique) {
				p.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueUpdateProgress{}
			}).
			Build()
	}
	readyPod := func(name, hash string, ageMinutes int) *corev1.Pod {
		return newTestPod(name, hash, withPhase(corev1.PodRunning), withReadyCondition(), withAgeMinutes(ageMinutes))
	}
	newResource := func(pclq *grovecorev1alpha1.PodClique, pods []*corev1.Pod) (_resource, client.Client) {
		objs := []client.Object{pclq}
		for _, p := range pods {
			objs = append(objs, p)
		}
		cl := testutils.NewTestClientBuilder().WithObjects(objs...).Build()
		return _resource{
			client:            cl,
			scheme:            cl.Scheme(),
			eventRecorder:     record.NewFakeRecorder(64),
			expectationsStore: expect.NewExpectationsStore(),
		}, cl
	}
	remainingPodNames := func(t *testing.T, cl client.Client) []string {
		var list corev1.PodList
		require.NoError(t, cl.List(context.Background(), &list, client.InNamespace(testNamespace)))
		names := make([]string, 0, len(list.Items))
		for _, p := range list.Items {
			names = append(names, p.Name)
		}
		return names
	}
	requeueErr := &groveerr.GroveError{Code: groveerr.ErrCodeContinueReconcileAndRequeue, Operation: component.OperationSync}

	t.Run("completes when no old pods remain and desired new pods are ready", func(t *testing.T) {
		pclq := pclqUpdating(2, 1)
		pods := []*corev1.Pod{readyPod("new-0", testNewHash, 2), readyPod("new-1", testNewHash, 1)}
		r, _ := newResource(pclq, pods)
		ss := &syncSnapshot{pcs: pcsWithMaxUnavailable(1), pclq: pclq, cliqueName: testCliqueName, expectedPodTemplateHash: testNewHash, existingPCLQPods: pods}

		require.NoError(t, r.processPendingUpdates(context.Background(), logr.Discard(), ss))
		assert.NotNil(t, pclq.Status.UpdateProgress.UpdateEndedAt, "expected UpdateEndedAt to be set")
	})

	t.Run("completes when replacements are ready even if an old pod is stuck terminating", func(t *testing.T) {
		pclq := pclqUpdating(2, 1)
		newPods := []*corev1.Pod{readyPod("new-0", testNewHash, 2), readyPod("new-1", testNewHash, 1)}
		r, _ := newResource(pclq, newPods)
		// A Ready old-hash Pod that is stuck terminating. It is kept out of the fake client (its deletion is
		// already in flight) but present in the snapshot the update logic reads.
		stuckOld := newTestPod("old-terminating", testOldHash, withPhase(corev1.PodRunning), withReadyCondition(), withDeletionTimestamp())
		ss := &syncSnapshot{pcs: pcsWithMaxUnavailable(1), pclq: pclq, cliqueName: testCliqueName, expectedPodTemplateHash: testNewHash, existingPCLQPods: append(newPods, stuckOld)}

		require.NoError(t, r.processPendingUpdates(context.Background(), logr.Discard(), ss))
		assert.NotNil(t, pclq.Status.UpdateProgress.UpdateEndedAt, "a stuck-terminating old Pod must not block completion once replacements are Ready")
	})

	t.Run("waits when there are no ready old pods but replacements are not yet ready", func(t *testing.T) {
		pclq := pclqUpdating(2, 1)
		pods := []*corev1.Pod{readyPod("new-0", testNewHash, 1)}
		r, cl := newResource(pclq, pods)
		ss := &syncSnapshot{pcs: pcsWithMaxUnavailable(1), pclq: pclq, cliqueName: testCliqueName, expectedPodTemplateHash: testNewHash, existingPCLQPods: pods}

		err := r.processPendingUpdates(context.Background(), logr.Discard(), ss)
		testutils.AssertGroveError(t, requeueErr, err)
		assert.Nil(t, pclq.Status.UpdateProgress.UpdateEndedAt)
		assert.Len(t, remainingPodNames(t, cl), 1)
	})

	t.Run("disrupts the oldest ready old pods up to the budget", func(t *testing.T) {
		pclq := pclqUpdating(3, 1)
		pods := []*corev1.Pod{
			readyPod("old-0", testOldHash, 3),
			readyPod("old-1", testOldHash, 2),
			readyPod("old-2", testOldHash, 1),
		}
		r, cl := newResource(pclq, pods)
		ss := &syncSnapshot{pcs: pcsWithMaxUnavailable(2), pclq: pclq, cliqueName: testCliqueName, expectedPodTemplateHash: testNewHash, existingPCLQPods: pods}

		err := r.processPendingUpdates(context.Background(), logr.Discard(), ss)
		testutils.AssertGroveError(t, requeueErr, err)
		// Budget of 2 and MinAvailable headroom of 2, so the two oldest are deleted and the newest remains.
		assert.ElementsMatch(t, []string{"old-2"}, remainingPodNames(t, cl))
	})

	t.Run("rolls one pod at a time when MinAvailable equals replicas", func(t *testing.T) {
		pclq := pclqUpdating(3, 3)
		pods := []*corev1.Pod{
			readyPod("old-0", testOldHash, 3),
			readyPod("old-1", testOldHash, 2),
			readyPod("old-2", testOldHash, 1),
		}
		r, cl := newResource(pclq, pods)
		ss := &syncSnapshot{pcs: pcsWithMaxUnavailable(1), pclq: pclq, cliqueName: testCliqueName, expectedPodTemplateHash: testNewHash, existingPCLQPods: pods}

		err := r.processPendingUpdates(context.Background(), logr.Discard(), ss)
		testutils.AssertGroveError(t, requeueErr, err)
		assert.Len(t, remainingPodNames(t, cl), 2, "MinAvailable equal to replicas must not block the roll; MaxUnavailable=1 disrupts one pod")
	})
}

// newTestPod creates a pod with the given name, template hash label, and options applied.
func newTestPod(name, templateHash string, opts ...func(*corev1.Pod)) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels: map[string]string{
				apicommon.LabelPodTemplateHash: templateHash,
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

func withAgeMinutes(ageMinutes int) func(*corev1.Pod) {
	return func(pod *corev1.Pod) {
		pod.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Duration(ageMinutes) * time.Minute))
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
	bucketSkipped // terminating pods, not in any bucket
)

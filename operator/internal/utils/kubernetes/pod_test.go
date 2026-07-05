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

package kubernetes

import (
	"testing"

	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const (
	testPodName      = "test-pod"
	testPodNamespace = "test-ns"
)

func TestIsPodActive(t *testing.T) {
	testCases := []struct {
		description   string
		phase         corev1.PodPhase
		isTerminating bool
		want          bool
	}{
		{
			description: "should return true for pod that is running",
			phase:       corev1.PodRunning,
			want:        true,
		},
		{
			description:   "should return false for termination pod",
			phase:         corev1.PodRunning,
			isTerminating: true,
			want:          false,
		},
		{
			description: "should return false when pod has failed",
			phase:       corev1.PodFailed,
			want:        false,
		},
		{
			description: "should return false for pod that has finished execution with success",
			phase:       corev1.PodSucceeded,
			want:        false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			podBuilder := testutils.NewPodWithBuilderWithDefaultSpec(testPodName, testPodNamespace).WithPhase(tc.phase)
			if tc.isTerminating {
				podBuilder.MarkForTermination()
			}
			pod := podBuilder.Build()
			assert.Equal(t, tc.want, IsPodActive(pod))
		})
	}
}

func TestIsPodScheduled(t *testing.T) {
	testCases := []struct {
		description        string
		scheduledCondition *corev1.PodCondition
		expectedResult     bool
	}{
		{
			description:    "Pod does not have a PodScheduled condition",
			expectedResult: false,
		},
		{
			description: "Pod has a PodScheduled condition with status True",
			scheduledCondition: &corev1.PodCondition{
				Type:   corev1.PodScheduled,
				Status: corev1.ConditionTrue,
			},
			expectedResult: true,
		},
		{
			description: "Pod has a PodScheduled condition with status False",
			scheduledCondition: &corev1.PodCondition{
				Type:   corev1.PodScheduled,
				Status: corev1.ConditionFalse,
			},
			expectedResult: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			podBuilder := testutils.NewPodWithBuilderWithDefaultSpec(testPodName, testPodNamespace)
			if tc.scheduledCondition != nil {
				podBuilder.WithCondition(*tc.scheduledCondition)
			}
			pod := podBuilder.Build()
			result := IsPodScheduled(pod)
			if result != tc.expectedResult {
				t.Errorf("expected %v, got %v", tc.expectedResult, result)
			}
		})
	}
}

// TestIsPodScheduleGated tests checking if a pod has scheduling gates.
func TestIsPodScheduleGated(t *testing.T) {
	testCases := []struct {
		// name identifies the test case
		name string
		// scheduledCondition is the PodScheduled condition to add to the pod
		scheduledCondition *corev1.PodCondition
		// expectedResult is whether the pod should be considered schedule gated
		expectedResult bool
	}{
		{
			// Pod without PodScheduled condition is not schedule gated
			name:           "no-scheduled-condition",
			expectedResult: false,
		},
		{
			// Pod with PodScheduled=False and SchedulingGated reason is schedule gated
			name: "schedule-gated-condition",
			scheduledCondition: &corev1.PodCondition{
				Type:   corev1.PodScheduled,
				Status: corev1.ConditionFalse,
				Reason: corev1.PodReasonSchedulingGated,
			},
			expectedResult: true,
		},
		{
			// Pod with PodScheduled=False but different reason is not schedule gated
			name: "scheduled-false-different-reason",
			scheduledCondition: &corev1.PodCondition{
				Type:   corev1.PodScheduled,
				Status: corev1.ConditionFalse,
				Reason: "Unschedulable",
			},
			expectedResult: false,
		},
		{
			// Pod with PodScheduled=True is not schedule gated
			name: "scheduled-true",
			scheduledCondition: &corev1.PodCondition{
				Type:   corev1.PodScheduled,
				Status: corev1.ConditionTrue,
			},
			expectedResult: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			podBuilder := testutils.NewPodWithBuilderWithDefaultSpec(testPodName, testPodNamespace)
			if tc.scheduledCondition != nil {
				podBuilder.WithCondition(*tc.scheduledCondition)
			}
			pod := podBuilder.Build()
			assert.Equal(t, tc.expectedResult, IsPodScheduleGated(pod))
		})
	}
}

// TestIsPodReady tests checking if a pod is ready.
func TestIsPodReady(t *testing.T) {
	testCases := []struct {
		// name identifies the test case
		name string
		// readyCondition is the PodReady condition to add to the pod
		readyCondition *corev1.PodCondition
		// expectedResult is whether the pod should be considered ready
		expectedResult bool
	}{
		{
			// Pod without PodReady condition is not ready
			name:           "no-ready-condition",
			expectedResult: false,
		},
		{
			// Pod with PodReady=True is ready
			name: "ready-true",
			readyCondition: &corev1.PodCondition{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			},
			expectedResult: true,
		},
		{
			// Pod with PodReady=False is not ready
			name: "ready-false",
			readyCondition: &corev1.PodCondition{
				Type:   corev1.PodReady,
				Status: corev1.ConditionFalse,
			},
			expectedResult: false,
		},
		{
			// Pod with PodReady=Unknown is not ready
			name: "ready-unknown",
			readyCondition: &corev1.PodCondition{
				Type:   corev1.PodReady,
				Status: corev1.ConditionUnknown,
			},
			expectedResult: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			podBuilder := testutils.NewPodWithBuilderWithDefaultSpec(testPodName, testPodNamespace)
			if tc.readyCondition != nil {
				podBuilder.WithCondition(*tc.readyCondition)
			}
			pod := podBuilder.Build()
			assert.Equal(t, tc.expectedResult, IsPodReady(pod))
		})
	}
}

// TestIsPodPending tests checking if a pod is in pending phase.
func TestIsPodPending(t *testing.T) {
	// Pod in pending phase
	pod := testutils.NewPodWithBuilderWithDefaultSpec(testPodName, testPodNamespace).
		WithPhase(corev1.PodPending).
		Build()
	assert.True(t, IsPodPending(pod))

	// Pod in running phase
	pod = testutils.NewPodWithBuilderWithDefaultSpec(testPodName, testPodNamespace).
		WithPhase(corev1.PodRunning).
		Build()
	assert.False(t, IsPodPending(pod))
}

// TestHasAnyStartedButNotReadyContainer tests checking for containers that have started but are not ready.
func TestHasAnyStartedButNotReadyContainer(t *testing.T) {
	testCases := []struct {
		// name identifies the test case
		name string
		// containerStatuses is the list of container statuses to set on the pod
		containerStatuses []corev1.ContainerStatus
		// expectedResult is whether the pod should have a started but not ready container
		expectedResult bool
	}{
		{
			// Container that has started but is not ready
			name: "started-not-ready",
			containerStatuses: []corev1.ContainerStatus{
				{
					Name:    "container-1",
					Started: ptr.To(true),
					Ready:   false,
				},
			},
			expectedResult: true,
		},
		{
			// Container that has started and is ready
			name: "started-and-ready",
			containerStatuses: []corev1.ContainerStatus{
				{
					Name:    "container-1",
					Started: ptr.To(true),
					Ready:   true,
				},
			},
			expectedResult: false,
		},
		{
			// Container that has not started
			name: "not-started",
			containerStatuses: []corev1.ContainerStatus{
				{
					Name:    "container-1",
					Started: ptr.To(false),
					Ready:   false,
				},
			},
			expectedResult: false,
		},
		{
			// Container with nil Started field
			name: "started-nil",
			containerStatuses: []corev1.ContainerStatus{
				{
					Name:    "container-1",
					Started: nil,
					Ready:   false,
				},
			},
			expectedResult: false,
		},
		{
			// Multiple containers with one started but not ready
			name: "multiple-containers-one-started-not-ready",
			containerStatuses: []corev1.ContainerStatus{
				{
					Name:    "container-1",
					Started: ptr.To(true),
					Ready:   true,
				},
				{
					Name:    "container-2",
					Started: ptr.To(true),
					Ready:   false,
				},
			},
			expectedResult: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testPodName,
					Namespace: testPodNamespace,
				},
				Status: corev1.PodStatus{
					ContainerStatuses: tc.containerStatuses,
				},
			}
			assert.Equal(t, tc.expectedResult, HasAnyStartedButNotReadyContainer(pod))
		})
	}
}

// TestHasAnyContainerNotStarted tests checking for containers that have not yet passed their startup probe.
func TestHasAnyContainerNotStarted(t *testing.T) {
	testCases := []struct {
		// name identifies the test case
		name string
		// containerStatuses is the list of container statuses to set on the pod
		containerStatuses []corev1.ContainerStatus
		// expectedResult is whether the pod should have a not-yet-started container
		expectedResult bool
	}{
		{
			// Container with Started==false (startup probe not yet passed)
			name: "started-false",
			containerStatuses: []corev1.ContainerStatus{
				{
					Name:    "container-1",
					Started: ptr.To(false),
					Ready:   false,
				},
			},
			expectedResult: true,
		},
		{
			// Container with Started==nil (startup probe not yet evaluated)
			name: "started-nil",
			containerStatuses: []corev1.ContainerStatus{
				{
					Name:    "container-1",
					Started: nil,
					Ready:   false,
				},
			},
			expectedResult: true,
		},
		{
			// Container that has started (startup probe passed)
			name: "started-true",
			containerStatuses: []corev1.ContainerStatus{
				{
					Name:    "container-1",
					Started: ptr.To(true),
					Ready:   false,
				},
			},
			expectedResult: false,
		},
		{
			// Container that has started and is ready
			name: "started-and-ready",
			containerStatuses: []corev1.ContainerStatus{
				{
					Name:    "container-1",
					Started: ptr.To(true),
					Ready:   true,
				},
			},
			expectedResult: false,
		},
		{
			// No container statuses
			name:              "no-containers",
			containerStatuses: []corev1.ContainerStatus{},
			expectedResult:    false,
		},
		{
			// Multiple containers — one started, one not started
			name: "multiple-containers-one-not-started",
			containerStatuses: []corev1.ContainerStatus{
				{
					Name:    "container-1",
					Started: ptr.To(true),
					Ready:   true,
				},
				{
					Name:    "container-2",
					Started: ptr.To(false),
					Ready:   false,
				},
			},
			expectedResult: true,
		},
		{
			// Multiple containers — all started
			name: "multiple-containers-all-started",
			containerStatuses: []corev1.ContainerStatus{
				{
					Name:    "container-1",
					Started: ptr.To(true),
					Ready:   true,
				},
				{
					Name:    "container-2",
					Started: ptr.To(true),
					Ready:   false,
				},
			},
			expectedResult: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testPodName,
					Namespace: testPodNamespace,
				},
				Status: corev1.PodStatus{
					ContainerStatuses: tc.containerStatuses,
				},
			}
			assert.Equal(t, tc.expectedResult, HasAnyContainerNotStarted(pod))
		})
	}
}

// TestGetContainerStatusIfTerminatedErroneously tests finding containers that terminated with non-zero exit code.
func TestGetContainerStatusIfTerminatedErroneously(t *testing.T) {
	testCases := []struct {
		// name identifies the test case
		name string
		// containerStatuses is the list of container statuses to check
		containerStatuses []corev1.ContainerStatus
		// expectNil indicates if nil should be returned
		expectNil bool
		// expectedContainerName is the expected name of the erroneous container
		expectedContainerName string
	}{
		{
			// Container terminated with non-zero exit code
			name: "terminated-non-zero-exit",
			containerStatuses: []corev1.ContainerStatus{
				{
					Name: "container-1",
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 1,
						},
					},
				},
			},
			expectNil:             false,
			expectedContainerName: "container-1",
		},
		{
			// Container terminated with zero exit code
			name: "terminated-zero-exit",
			containerStatuses: []corev1.ContainerStatus{
				{
					Name: "container-1",
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 0,
						},
					},
				},
			},
			expectNil: true,
		},
		{
			// Container not terminated
			name: "not-terminated",
			containerStatuses: []corev1.ContainerStatus{
				{
					Name:                 "container-1",
					LastTerminationState: corev1.ContainerState{},
				},
			},
			expectNil: true,
		},
		{
			// Multiple containers with one terminated erroneously
			name: "multiple-containers-one-errored",
			containerStatuses: []corev1.ContainerStatus{
				{
					Name: "container-1",
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 0,
						},
					},
				},
				{
					Name: "container-2",
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 137,
						},
					},
				},
			},
			expectNil:             false,
			expectedContainerName: "container-2",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := GetContainerStatusIfTerminatedErroneously(tc.containerStatuses)
			if tc.expectNil {
				assert.Nil(t, result)
			} else {
				assert.NotNil(t, result)
				assert.Equal(t, tc.expectedContainerName, result.Name)
			}
		})
	}
}

// TestHasAnyContainerExitedErroneously tests checking if any container has exited with non-zero exit code.
func TestHasAnyContainerExitedErroneously(t *testing.T) {
	testCases := []struct {
		// name identifies the test case
		name string
		// initContainerStatuses is the list of init container statuses
		initContainerStatuses []corev1.ContainerStatus
		// containerStatuses is the list of regular container statuses
		containerStatuses []corev1.ContainerStatus
		// expectedResult is whether any container exited erroneously
		expectedResult bool
	}{
		{
			// Init container terminated with non-zero exit code
			name: "init-container-errored",
			initContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "init-1",
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 1,
						},
					},
				},
			},
			expectedResult: true,
		},
		{
			// Regular container terminated with non-zero exit code
			name: "container-errored",
			containerStatuses: []corev1.ContainerStatus{
				{
					Name: "container-1",
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 255,
						},
					},
				},
			},
			expectedResult: true,
		},
		{
			// No containers terminated erroneously
			name: "no-errors",
			containerStatuses: []corev1.ContainerStatus{
				{
					Name: "container-1",
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 0,
						},
					},
				},
			},
			expectedResult: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testPodName,
					Namespace: testPodNamespace,
				},
				Status: corev1.PodStatus{
					InitContainerStatuses: tc.initContainerStatuses,
					ContainerStatuses:     tc.containerStatuses,
				},
			}
			assert.Equal(t, tc.expectedResult, HasAnyContainerExitedErroneously(logr.Discard(), pod))
		})
	}
}

// TestComputeHash tests computing hash from pod template specs.
func TestComputeHash(t *testing.T) {
	podSpec1 := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{"app": "test"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "container-1",
					Image: "image:v1",
				},
			},
		},
	}

	podSpec2 := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{"app": "test"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "container-1",
					Image: "image:v2",
				},
			},
		},
	}

	// Same spec should produce same hash
	hash1 := ComputeHash(podSpec1)
	hash2 := ComputeHash(podSpec1)
	assert.Equal(t, hash1, hash2)

	// Different specs should produce different hashes
	hash3 := ComputeHash(podSpec2)
	assert.NotEqual(t, hash1, hash3)

	// Multiple specs should produce a consistent hash
	hash4 := ComputeHash(podSpec1, podSpec2)
	hash5 := ComputeHash(podSpec1, podSpec2)
	assert.Equal(t, hash4, hash5)
}

// TestCategorizePodsByConditionType tests categorizing pods by their conditions.
func TestCategorizePodsByConditionType(t *testing.T) {
	// Ready pod
	readyPod := testutils.NewPodWithBuilderWithDefaultSpec("ready-pod", testPodNamespace).
		WithCondition(corev1.PodCondition{
			Type:   corev1.PodScheduled,
			Status: corev1.ConditionTrue,
		}).
		WithCondition(corev1.PodCondition{
			Type:   corev1.PodReady,
			Status: corev1.ConditionTrue,
		}).
		Build()

	// Schedule gated pod
	gatedPod := testutils.NewPodWithBuilderWithDefaultSpec("gated-pod", testPodNamespace).
		WithCondition(corev1.PodCondition{
			Type:   corev1.PodScheduled,
			Status: corev1.ConditionFalse,
			Reason: corev1.PodReasonSchedulingGated,
		}).
		Build()

	// Terminating pod
	terminatingPod := testutils.NewPodWithBuilderWithDefaultSpec("terminating-pod", testPodNamespace).
		WithCondition(corev1.PodCondition{
			Type:   corev1.PodScheduled,
			Status: corev1.ConditionTrue,
		}).
		MarkForTermination().
		Build()

	// Pod with container that exited erroneously
	erroneousPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "erroneous-pod",
			Namespace: testPodNamespace,
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodScheduled,
					Status: corev1.ConditionTrue,
				},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "container-1",
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 1,
						},
					},
				},
			},
		},
	}

	// Pod with started but not ready container
	startedNotReadyPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "started-not-ready-pod",
			Namespace: testPodNamespace,
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodScheduled,
					Status: corev1.ConditionTrue,
				},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:    "container-1",
					Started: ptr.To(true),
					Ready:   false,
				},
			},
		},
	}

	pods := []*corev1.Pod{readyPod, gatedPod, terminatingPod, erroneousPod, startedNotReadyPod}
	categories := CategorizePodsByConditionType(logr.Discard(), pods)

	// Check ready pods
	assert.Len(t, categories[corev1.PodReady], 1)
	assert.Equal(t, "ready-pod", categories[corev1.PodReady][0].Name)

	// Check scheduled pods
	assert.Len(t, categories[corev1.PodScheduled], 4)

	// Check schedule gated pods
	assert.Len(t, categories[ScheduleGatedPod], 1)
	assert.Equal(t, "gated-pod", categories[ScheduleGatedPod][0].Name)

	// Check terminating pods
	assert.Len(t, categories[TerminatingPod], 1)
	assert.Equal(t, "terminating-pod", categories[TerminatingPod][0].Name)

	// Check pods with erroneous containers
	assert.Len(t, categories[PodHasAtleastOneContainerWithNonZeroExitCode], 1)
	assert.Equal(t, "erroneous-pod", categories[PodHasAtleastOneContainerWithNonZeroExitCode][0].Name)

	// Check started but not ready pods
	assert.Len(t, categories[PodStartedButNotReady], 1)
	assert.Equal(t, "started-not-ready-pod", categories[PodStartedButNotReady][0].Name)
}

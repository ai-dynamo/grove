//go:build e2e

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

package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	jobSupportCliqueName = "worker"
	jobSupportNamespace  = "default"
	jobSupportStableFor  = 10 * time.Second
)

func Test_JobSupport_PCLQCompletesWhenAllPodsSucceed(t *testing.T) {
	ctx := context.Background()
	workloadName := "js-pclq-complete"
	pclqName := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: workloadName, Replica: 0}, jobSupportCliqueName)

	Logger.Info("1. Initialize a 2-node Grove cluster for finite PCLQ success")
	tc, cleanup := testctx.PrepareTest(ctx, t, 2,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         workloadName,
			Namespace:    jobSupportNamespace,
			ExpectedPods: 2,
		}),
		testctx.WithTimeout(2*time.Minute),
		testctx.WithInterval(2*time.Second),
	)
	defer cleanup()

	Logger.Info("2. Create a finite PodCliqueSet")
	pcs := newFinitePCLQPodCliqueSet(workloadName, 2)
	if err := tc.Client.Create(ctx, pcs); err != nil {
		t.Fatalf("Failed to create PodCliqueSet: %v", err)
	}

	Logger.Info("3. Wait for pods to run and mark all of them Succeeded")
	if err := tc.WaitForRunningPods(2); err != nil {
		t.Fatalf("Failed to wait for running pods: %v", err)
	}
	podList, err := tc.WaitForPodCount(2)
	if err != nil {
		t.Fatalf("Failed to list finite PodClique pods: %v", err)
	}
	markPodsPhase(t, tc, podList.Items, corev1.PodSucceeded)

	Logger.Info("4. Wait for the PodClique to reach phase=Completed")
	pclq := waitForPCLQPhase(t, tc, pclqName, grovev1alpha1.JobPhaseCompleted)

	Logger.Info("5. Verify succeeded pods are retained")
	podList = waitForRetainedPodsInPhase(t, tc, int(pclq.Spec.Replicas), corev1.PodSucceeded)
	assertPodsInPhase(t, podList.Items, corev1.PodSucceeded)
}

func Test_JobSupport_PCLQFailureCleansActivePods(t *testing.T) {
	ctx := context.Background()
	workloadName := "js-pclq-fail"
	pclqName := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: workloadName, Replica: 0}, jobSupportCliqueName)

	Logger.Info("1. Initialize a 4-node Grove cluster for finite PCLQ failure")
	tc, cleanup := testctx.PrepareTest(ctx, t, 4,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         workloadName,
			Namespace:    jobSupportNamespace,
			ExpectedPods: 4,
		}),
		testctx.WithTimeout(2*time.Minute),
		testctx.WithInterval(2*time.Second),
	)
	defer cleanup()

	Logger.Info("2. Create a finite PodCliqueSet")
	pcs := newFinitePCLQPodCliqueSet(workloadName, 4)
	if err := tc.Client.Create(ctx, pcs); err != nil {
		t.Fatalf("Failed to create PodCliqueSet: %v", err)
	}

	Logger.Info("3. Wait for pods to run and mark pod phases: 0=Succeeded, 1=Failed, 2=Running, 3=Pending")
	if err := tc.WaitForRunningPods(4); err != nil {
		t.Fatalf("Failed to wait for running pods: %v", err)
	}
	podList, err := tc.WaitForPodCount(4)
	if err != nil {
		t.Fatalf("Failed to list finite PodClique pods: %v", err)
	}
	succeededPod := findPodByCliqueIndex(t, podList.Items, "0")
	failedPod := findPodByCliqueIndex(t, podList.Items, "1")
	pendingPod := findPodByCliqueIndex(t, podList.Items, "3")
	markPodPhase(t, tc, succeededPod, corev1.PodSucceeded)
	markPodPhase(t, tc, pendingPod, corev1.PodPending)
	waitForPodsByPhase(t, tc, map[corev1.PodPhase]int{
		corev1.PodSucceeded: 1,
		corev1.PodRunning:   2,
		corev1.PodPending:   1,
	})
	markPodPhase(t, tc, failedPod, corev1.PodFailed)

	Logger.Info("4. Wait for the PodClique to reach phase=Failed")
	waitForPCLQPhase(t, tc, pclqName, grovev1alpha1.JobPhaseFailed)

	Logger.Info("5. Verify terminal pods are retained and non-terminal pods are deleted without recreation")
	podList = waitForRetainedPodsByPhase(t, tc, map[corev1.PodPhase]int{
		corev1.PodSucceeded: 1,
		corev1.PodFailed:    1,
	})
	assertOnlyPodsRemainFor(t, tc, podList.Items, jobSupportStableFor)
}

func newFinitePCLQPodCliqueSet(name string, replicas int32) *grovev1alpha1.PodCliqueSet {
	minAvailable := replicas
	return testutils.NewPodCliqueSetBuilder(name, jobSupportNamespace, uuid.NewUUID()).
		WithReplicas(1).
		WithTerminationDelay(4 * time.Hour).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder(jobSupportCliqueName).
				WithLabels(map[string]string{
					"kai.scheduler/queue": "test",
				}).
				WithReplicas(replicas).
				WithRoleName("worker-role").
				WithMinAvailable(minAvailable).
				WithPodSpec(newFinitePCLQPodSpec()).
				Build(),
		).
		Build()
}

func newFinitePCLQPodSpec() corev1.PodSpec {
	return corev1.PodSpec{
		RestartPolicy:                 corev1.RestartPolicyNever,
		SchedulerName:                 string(configv1alpha1.SchedulerNameKai),
		TerminationGracePeriodSeconds: ptr.To[int64](5),
		Affinity: &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      setup.WorkerNodeLabelKey,
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{setup.WorkerNodeLabelValue},
								},
							},
						},
					},
				},
			},
		},
		Tolerations: []corev1.Toleration{
			{
				Key:      setup.WorkerNodeLabelKey,
				Operator: corev1.TolerationOpEqual,
				Value:    setup.WorkerNodeLabelValue,
				Effect:   corev1.TaintEffectNoSchedule,
			},
		},
		Containers: []corev1.Container{
			{
				Name:    jobSupportCliqueName,
				Image:   "registry:5001/busybox:latest",
				Command: []string{"sleep", "3600"},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("80Mi"),
					},
				},
			},
		},
	}
}

func markPodsPhase(t *testing.T, tc *testctx.TestContext, pods []corev1.Pod, phase corev1.PodPhase) {
	t.Helper()
	for i := range pods {
		markPodPhase(t, tc, &pods[i], phase)
	}
}

// markPodPhase simulates kubelet-reported pod state for the default e2e
// cluster, where KWOK nodes schedule pods but do not execute containers.
// This keeps the test focused on Grove's finite-PCLQ reconciliation behavior:
// observing pod phases, setting PCLQ phase, and cleaning active pods.
func markPodPhase(t *testing.T, tc *testctx.TestContext, pod *corev1.Pod, phase corev1.PodPhase) {
	t.Helper()

	current := &corev1.Pod{}
	if err := tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(pod), current); err != nil {
		t.Fatalf("Failed to get pod %s before status patch: %v", pod.Name, err)
	}

	patch := client.MergeFrom(current.DeepCopy())
	current.Status.Phase = phase
	switch phase {
	case corev1.PodSucceeded, corev1.PodFailed:
		current.Status.ContainerStatuses = []corev1.ContainerStatus{terminalContainerStatus(phase)}
		setPodCondition(current, corev1.PodReady, corev1.ConditionFalse, string(phase))
	case corev1.PodPending:
		current.Status.ContainerStatuses = nil
		setPodCondition(current, corev1.PodReady, corev1.ConditionFalse, "Pending")
		setPodCondition(current, corev1.PodScheduled, corev1.ConditionFalse, corev1.PodReasonUnschedulable)
	}

	if err := tc.Client.Status().Patch(tc.Ctx, current, patch); err != nil {
		t.Fatalf("Failed to patch pod %s phase to %s: %v", pod.Name, phase, err)
	}
}

func setPodCondition(pod *corev1.Pod, conditionType corev1.PodConditionType, status corev1.ConditionStatus, reason string) {
	now := metav1.Now()
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == conditionType {
			pod.Status.Conditions[i].Status = status
			pod.Status.Conditions[i].Reason = reason
			pod.Status.Conditions[i].LastTransitionTime = now
			return
		}
	}
	pod.Status.Conditions = append(pod.Status.Conditions, corev1.PodCondition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		LastTransitionTime: now,
	})
}

func terminalContainerStatus(phase corev1.PodPhase) corev1.ContainerStatus {
	finishedAt := metav1.Now()
	exitCode := int32(0)
	reason := "Completed"
	if phase == corev1.PodFailed {
		exitCode = 1
		reason = "Error"
	}

	return corev1.ContainerStatus{
		Name:    jobSupportCliqueName,
		Ready:   false,
		Started: ptr.To(false),
		State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				ExitCode:   exitCode,
				Reason:     reason,
				FinishedAt: finishedAt,
			},
		},
	}
}

func findPodByCliqueIndex(t *testing.T, pods []corev1.Pod, index string) *corev1.Pod {
	t.Helper()
	for i := range pods {
		if pods[i].Labels[apicommon.LabelPodCliquePodIndex] == index {
			return &pods[i]
		}
	}
	t.Fatalf("Failed to find pod with %s=%s", apicommon.LabelPodCliquePodIndex, index)
	return nil
}

func waitForPCLQPhase(t *testing.T, tc *testctx.TestContext, name string, phase grovev1alpha1.JobPhase) *grovev1alpha1.PodClique {
	t.Helper()

	fetchPCLQ := waiter.FetchFunc[*grovev1alpha1.PodClique](func(ctx context.Context) (*grovev1alpha1.PodClique, error) {
		pclq := &grovev1alpha1.PodClique{}
		err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: name}, pclq)
		return pclq, client.IgnoreNotFound(err)
	})
	predicate := waiter.Predicate[*grovev1alpha1.PodClique](func(pclq *grovev1alpha1.PodClique) bool {
		return pclq.Name != "" && pclq.Status.Phase == phase
	})

	pclq, err := waiter.New[*grovev1alpha1.PodClique]().
		WithTimeout(tc.Timeout).
		WithInterval(tc.Interval).
		WaitFor(tc.Ctx, fetchPCLQ, predicate)
	if err != nil {
		t.Fatalf("Failed to wait for PodClique %s phase %s: %v", name, phase, err)
	}
	return pclq
}

func waitForRetainedPodsInPhase(t *testing.T, tc *testctx.TestContext, expectedCount int, phase corev1.PodPhase) *corev1.PodList {
	t.Helper()

	fetchPods := waiter.FetchFunc[*corev1.PodList](func(ctx context.Context) (*corev1.PodList, error) {
		return tc.ListPods()
	})
	predicate := waiter.Predicate[*corev1.PodList](func(podList *corev1.PodList) bool {
		if len(podList.Items) != expectedCount {
			return false
		}
		for i := range podList.Items {
			if podList.Items[i].Status.Phase != phase || !podList.Items[i].DeletionTimestamp.IsZero() {
				return false
			}
		}
		return true
	})

	podList, err := waiter.New[*corev1.PodList]().
		WithTimeout(tc.Timeout).
		WithInterval(tc.Interval).
		WaitFor(tc.Ctx, fetchPods, predicate)
	if err != nil {
		t.Fatalf("Failed to wait for retained pods in phase %s: %v", phase, err)
	}
	return podList
}

func waitForRetainedPodsByPhase(t *testing.T, tc *testctx.TestContext, expectedByPhase map[corev1.PodPhase]int) *corev1.PodList {
	t.Helper()
	podList := waitForPodsByPhase(t, tc, expectedByPhase)
	for i := range podList.Items {
		if !podList.Items[i].DeletionTimestamp.IsZero() {
			t.Fatalf("Expected retained pod %s to stay present, but it is deleting", podList.Items[i].Name)
		}
	}
	return podList
}

func waitForPodsByPhase(t *testing.T, tc *testctx.TestContext, expectedByPhase map[corev1.PodPhase]int) *corev1.PodList {
	t.Helper()
	expectedCount := 0
	for _, count := range expectedByPhase {
		expectedCount += count
	}

	fetchPods := waiter.FetchFunc[*corev1.PodList](func(ctx context.Context) (*corev1.PodList, error) {
		return tc.ListPods()
	})
	predicate := waiter.Predicate[*corev1.PodList](func(podList *corev1.PodList) bool {
		if len(podList.Items) != expectedCount {
			return false
		}
		actualByPhase := map[corev1.PodPhase]int{}
		for i := range podList.Items {
			actualByPhase[podList.Items[i].Status.Phase]++
		}
		for phase, expected := range expectedByPhase {
			if actualByPhase[phase] != expected {
				return false
			}
		}
		return true
	})

	podList, err := waiter.New[*corev1.PodList]().
		WithTimeout(tc.Timeout).
		WithInterval(tc.Interval).
		WaitFor(tc.Ctx, fetchPods, predicate)
	if err != nil {
		podList, listErr := tc.ListPods()
		if listErr != nil {
			t.Fatalf("Failed to wait for pods by phase %v: %v; also failed to list pods: %v", expectedByPhase, err, listErr)
		}
		t.Fatalf("Failed to wait for pods by phase %v: %v; current pods: %v", expectedByPhase, err, describePods(podList.Items))
	}
	return podList
}

func assertOnlyPodsRemainFor(t *testing.T, tc *testctx.TestContext, expectedPods []corev1.Pod, duration time.Duration) {
	t.Helper()
	expectedByUID := make(map[string]corev1.PodPhase, len(expectedPods))
	for _, pod := range expectedPods {
		expectedByUID[string(pod.UID)] = pod.Status.Phase
	}

	deadline := time.Now().Add(duration)
	for {
		podList, err := tc.ListPods()
		if err != nil {
			t.Fatalf("Failed to list pods while checking no recreation: %v", err)
		}
		assertPodUIDsAndPhases(t, podList.Items, expectedByUID)
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(tc.Interval)
	}
}

func assertPodUIDsAndPhases(t *testing.T, pods []corev1.Pod, expectedByUID map[string]corev1.PodPhase) {
	t.Helper()
	if len(pods) != len(expectedByUID) {
		t.Fatalf("Expected only retained terminal pods to remain, got %s", describePods(pods))
	}
	for _, pod := range pods {
		expectedPhase, ok := expectedByUID[string(pod.UID)]
		if !ok {
			t.Fatalf("Unexpected pod present after terminal cleanup, got %s", describePods(pods))
		}
		if pod.Status.Phase != expectedPhase {
			t.Fatalf("Expected retained pod %s to stay in phase %s, got %s", pod.Name, expectedPhase, pod.Status.Phase)
		}
		if !pod.DeletionTimestamp.IsZero() {
			t.Fatalf("Expected retained pod %s to stay present, but it is deleting", pod.Name)
		}
	}
}

func describePods(pods []corev1.Pod) []string {
	descriptions := make([]string, 0, len(pods))
	for _, pod := range pods {
		descriptions = append(descriptions, fmt.Sprintf("%s/%s phase=%s uid=%s", pod.Namespace, pod.Name, pod.Status.Phase, pod.UID))
	}
	return descriptions
}

func assertPodsInPhase(t *testing.T, pods []corev1.Pod, phase corev1.PodPhase) {
	t.Helper()
	for _, pod := range pods {
		if pod.Status.Phase != phase {
			t.Fatalf("Expected pod %s to be in phase %s, got %s", pod.Name, phase, pod.Status.Phase)
		}
		if !pod.DeletionTimestamp.IsZero() {
			t.Fatalf("Expected pod %s to be retained, but it is deleting", pod.Name)
		}
	}
}

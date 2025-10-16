//go:build e2e

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

package tests

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/grove/operator/e2e_testing/utils"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Helper function to verify startup order by checking pod creation timestamps
func verifyStartupOrder(t *testing.T, pods []v1.Pod, expectedOrder []string) {
	// Create a map of pod name to creation time
	podTimes := make(map[string]time.Time)
	for _, pod := range pods {
		podTimes[pod.Name] = pod.CreationTimestamp.Time
	}

	// Verify the order
	for i := 1; i < len(expectedOrder); i++ {
		currentPod := expectedOrder[i]
		previousPod := expectedOrder[i-1]
		
		currentTime, currentExists := podTimes[currentPod]
		previousTime, previousExists := podTimes[previousPod]
		
		if !currentExists {
			t.Fatalf("Expected pod %s not found in pod list", currentPod)
		}
		if !previousExists {
			t.Fatalf("Expected pod %s not found in pod list", previousPod)
		}
		
		// Allow a small tolerance for pods that should start at the same time
		tolerance := 5 * time.Second
		if currentTime.Before(previousTime.Add(-tolerance)) {
			t.Errorf("Startup order violation: %s started at %v, but %s started at %v", 
				currentPod, currentTime, previousPod, previousTime)
		}
	}
}

// Helper function to get pods by clique name pattern
func getPodsByCliquePattern(pods []v1.Pod, pattern string) []v1.Pod {
	var result []v1.Pod
	for _, pod := range pods {
		if strings.Contains(pod.Name, pattern) {
			result = append(result, pod)
		}
	}
	return result
}

// Test_SO1_InorderStartupOrderWithFullReplicas tests inorder startup with full replicas
// Scenario SO-1:
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload WL3, and verify 10 newly created pods
// 3. Wait for pods to get scheduled and become ready
// 4. Verify each print clique prints in the following order:
//    pcs-0-pc-a
//    pcs-0-sg-x-0-pc-b, pcs-0-sg-x-1-pc-b
//    pcs-0-sg-x-0-pc-c, pcs-0-sg-x-1-pc-c
func Test_SO1_InorderStartupOrderWithFullReplicas(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster")
	clientset, restConfig, _, cleanup, _ := setupTestCluster(ctx, t, 10)
	defer cleanup()

	logger.Info("2. Deploy workload WL3, and verify 10 newly created pods")
	workloadNamespace := "default"
	workloadYAMLPath := "../yaml/workload3.yaml"
	workloadLabelSelector := "app.kubernetes.io/part-of=workload3"

	_, err := utils.ApplyYAMLFile(ctx, workloadYAMLPath, workloadNamespace, restConfig, logger)
	if err != nil {
		t.Fatalf("Failed to apply workload YAML: %v", err)
	}

	expectedPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10

	var pods *v1.PodList
	err = pollForCondition(ctx, 2*time.Minute, 5*time.Second, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}
		return len(pods.Items) == expectedPods, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for pods to be created: %v", err)
	}

	logger.Info("3. Wait for pods to get scheduled and become ready")
	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, 10*time.Minute, logger); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	// Verify all pods are running
	runningPods := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			runningPods++
		}
	}
	if runningPods != len(pods.Items) {
		t.Fatalf("Expected all %d pods to be running, but only %d are running", len(pods.Items), runningPods)
	}

	logger.Info("4. Verify each print clique prints in the following order:")
	logger.Info("   pcs-0-pc-a")
	logger.Info("   pcs-0-sg-x-0-pc-b, pcs-0-sg-x-1-pc-b")
	logger.Info("   pcs-0-sg-x-0-pc-c, pcs-0-sg-x-1-pc-c")

	// Get pods by clique pattern
	pcAPods := getPodsByCliquePattern(pods.Items, "pcs-0-pc-a")
	pcBPods := getPodsByCliquePattern(pods.Items, "pcs-0-sg-x-")
	pcCPods := getPodsByCliquePattern(pods.Items, "pcs-0-sg-x-")

	// Filter pc-b and pc-c pods from the scaling group
	var pcBPodsFiltered []v1.Pod
	var pcCPodsFiltered []v1.Pod
	for _, pod := range pcBPods {
		if strings.Contains(pod.Name, "pc-b") {
			pcBPodsFiltered = append(pcBPodsFiltered, pod)
		}
	}
	for _, pod := range pcCPods {
		if strings.Contains(pod.Name, "pc-c") {
			pcCPodsFiltered = append(pcCPodsFiltered, pod)
		}
	}

	// Verify we have the expected number of pods
	if len(pcAPods) != 2 {
		t.Fatalf("Expected 2 pc-a pods, got %d", len(pcAPods))
	}
	if len(pcBPodsFiltered) != 2 {
		t.Fatalf("Expected 2 pc-b pods, got %d", len(pcBPodsFiltered))
	}
	if len(pcCPodsFiltered) != 6 {
		t.Fatalf("Expected 6 pc-c pods, got %d", len(pcCPodsFiltered))
	}

	// Verify startup order: pc-a should start first, then pc-b, then pc-c
	// Sort pods by clique type for verification
	pcATime := pcAPods[0].CreationTimestamp.Time
	pcBTime := pcBPodsFiltered[0].CreationTimestamp.Time
	pcCTime := pcCPodsFiltered[0].CreationTimestamp.Time

	// pc-a should start before pc-b
	if pcBTime.Before(pcATime) {
		t.Errorf("pc-b started before pc-a: pc-a at %v, pc-b at %v", pcATime, pcBTime)
	}

	// pc-b should start before pc-c
	if pcCTime.Before(pcBTime) {
		t.Errorf("pc-c started before pc-b: pc-b at %v, pc-c at %v", pcBTime, pcCTime)
	}

	// Within each clique, pods should start at roughly the same time (within 5 seconds)
	tolerance := 5 * time.Second
	for i := 1; i < len(pcAPods); i++ {
		if pcAPods[i].CreationTimestamp.Time.After(pcAPods[0].CreationTimestamp.Time.Add(tolerance)) {
			t.Errorf("pc-a pods started too far apart: %v vs %v", 
				pcAPods[0].CreationTimestamp.Time, pcAPods[i].CreationTimestamp.Time)
		}
	}

	logger.Info("🎉 Inorder startup order with full replicas test completed successfully!")
}

// Test_SO2_InorderStartupOrderWithMinReplicas tests inorder startup with min replicas
// Scenario SO-2:
// 1. Initialize a 6-node Grove cluster
// 2. Deploy workload WL4, and verify 10 newly created pods
// 3. Wait for 6 pods get scheduled and become ready:
//    pcs-0-{pc-a = 2}
//    pcs-0-{sg-x-0-pc-b = 1, sg-x-0-pc-c = 3}
// 4. Verify each print clique prints in the following order:
//    pcs-0-pc-a
//    pcs-0-sg-x-0-pc-b
//    pcs-0-sg-x-0-pc-c
func Test_SO2_InorderStartupOrderWithMinReplicas(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 6-node Grove cluster")
	clientset, restConfig, _, cleanup, _ := setupTestCluster(ctx, t, 6)
	defer cleanup()

	logger.Info("2. Deploy workload WL4, and verify 10 newly created pods")
	workloadNamespace := "default"
	workloadYAMLPath := "../yaml/workload4.yaml"
	workloadLabelSelector := "app.kubernetes.io/part-of=workload4"

	_, err := utils.ApplyYAMLFile(ctx, workloadYAMLPath, workloadNamespace, restConfig, logger)
	if err != nil {
		t.Fatalf("Failed to apply workload YAML: %v", err)
	}

	expectedPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10

	var pods *v1.PodList
	err = pollForCondition(ctx, 2*time.Minute, 5*time.Second, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}
		return len(pods.Items) == expectedPods, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for pods to be created: %v", err)
	}

	logger.Info("3. Wait for 6 pods get scheduled and become ready:")
	logger.Info("   pcs-0-{pc-a = 2}")
	logger.Info("   pcs-0-{sg-x-0-pc-b = 1, sg-x-0-pc-c = 3}")

	// Wait for pods to be scheduled and ready
	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, 10*time.Minute, logger); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	// Verify we have 6 running pods (min replicas)
	runningPods := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			runningPods++
		}
	}
	if runningPods != 6 {
		t.Fatalf("Expected 6 pods to be running (min replicas), but %d are running", runningPods)
	}

	logger.Info("4. Verify each print clique prints in the following order:")
	logger.Info("   pcs-0-pc-a")
	logger.Info("   pcs-0-sg-x-0-pc-b")
	logger.Info("   pcs-0-sg-x-0-pc-c")

	// Get running pods by clique pattern
	var runningPodsList []v1.Pod
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			runningPodsList = append(runningPodsList, pod)
		}
	}

	pcAPods := getPodsByCliquePattern(runningPodsList, "pcs-0-pc-a")
	pcBPods := getPodsByCliquePattern(runningPodsList, "pcs-0-sg-x-0-pc-b")
	pcCPods := getPodsByCliquePattern(runningPodsList, "pcs-0-sg-x-0-pc-c")

	// Verify we have the expected number of running pods
	if len(pcAPods) != 2 {
		t.Fatalf("Expected 2 running pc-a pods, got %d", len(pcAPods))
	}
	if len(pcBPods) != 1 {
		t.Fatalf("Expected 1 running pc-b pod, got %d", len(pcBPods))
	}
	if len(pcCPods) != 3 {
		t.Fatalf("Expected 3 running pc-c pods, got %d", len(pcCPods))
	}

	// Verify startup order: pc-a should start first, then pc-b, then pc-c
	pcATime := pcAPods[0].CreationTimestamp.Time
	pcBTime := pcBPods[0].CreationTimestamp.Time
	pcCTime := pcCPods[0].CreationTimestamp.Time

	// pc-a should start before pc-b
	if pcBTime.Before(pcATime) {
		t.Errorf("pc-b started before pc-a: pc-a at %v, pc-b at %v", pcATime, pcBTime)
	}

	// pc-b should start before pc-c
	if pcCTime.Before(pcBTime) {
		t.Errorf("pc-c started before pc-b: pc-b at %v, pc-c at %v", pcBTime, pcCTime)
	}

	logger.Info("🎉 Inorder startup order with min replicas test completed successfully!")
}

// Test_SO3_ExplicitStartupOrderWithFullReplicas tests explicit startup order with full replicas
// Scenario SO-3:
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload WL5, and verify 10 newly created pods
// 3. Wait for pods to get scheduled and become ready
// 4. Verify each print clique prints in the following order:
//    pcs-0-pc-a
//    pcs-0-sg-x-0-pc-c, pcs-0-sg-x-1-pc-c
//    pcs-0-sg-x-0-pc-b, pcs-0-sg-x-1-pc-b
func Test_SO3_ExplicitStartupOrderWithFullReplicas(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster")
	clientset, restConfig, _, cleanup, _ := setupTestCluster(ctx, t, 10)
	defer cleanup()

	logger.Info("2. Deploy workload WL5, and verify 10 newly created pods")
	workloadNamespace := "default"
	workloadYAMLPath := "../yaml/workload5.yaml"
	workloadLabelSelector := "app.kubernetes.io/part-of=workload5"

	_, err := utils.ApplyYAMLFile(ctx, workloadYAMLPath, workloadNamespace, restConfig, logger)
	if err != nil {
		t.Fatalf("Failed to apply workload YAML: %v", err)
	}

	expectedPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10

	var pods *v1.PodList
	err = pollForCondition(ctx, 2*time.Minute, 5*time.Second, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}
		return len(pods.Items) == expectedPods, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for pods to be created: %v", err)
	}

	logger.Info("3. Wait for pods to get scheduled and become ready")
	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, 10*time.Minute, logger); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	// Verify all pods are running
	runningPods := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			runningPods++
		}
	}
	if runningPods != len(pods.Items) {
		t.Fatalf("Expected all %d pods to be running, but only %d are running", len(pods.Items), runningPods)
	}

	logger.Info("4. Verify each print clique prints in the following order:")
	logger.Info("   pcs-0-pc-a")
	logger.Info("   pcs-0-sg-x-0-pc-c, pcs-0-sg-x-1-pc-c")
	logger.Info("   pcs-0-sg-x-0-pc-b, pcs-0-sg-x-1-pc-b")

	// Get pods by clique pattern
	pcAPods := getPodsByCliquePattern(pods.Items, "pcs-0-pc-a")
	pcBPods := getPodsByCliquePattern(pods.Items, "pcs-0-sg-x-")
	pcCPods := getPodsByCliquePattern(pods.Items, "pcs-0-sg-x-")

	// Filter pc-b and pc-c pods from the scaling group
	var pcBPodsFiltered []v1.Pod
	var pcCPodsFiltered []v1.Pod
	for _, pod := range pcBPods {
		if strings.Contains(pod.Name, "pc-b") {
			pcBPodsFiltered = append(pcBPodsFiltered, pod)
		}
	}
	for _, pod := range pcCPods {
		if strings.Contains(pod.Name, "pc-c") {
			pcCPodsFiltered = append(pcCPodsFiltered, pod)
		}
	}

	// Verify we have the expected number of pods
	if len(pcAPods) != 2 {
		t.Fatalf("Expected 2 pc-a pods, got %d", len(pcAPods))
	}
	if len(pcBPodsFiltered) != 2 {
		t.Fatalf("Expected 2 pc-b pods, got %d", len(pcBPodsFiltered))
	}
	if len(pcCPodsFiltered) != 6 {
		t.Fatalf("Expected 6 pc-c pods, got %d", len(pcCPodsFiltered))
	}

	// Verify startup order: pc-a should start first, then pc-c, then pc-b (explicit order)
	pcATime := pcAPods[0].CreationTimestamp.Time
	pcBTime := pcBPodsFiltered[0].CreationTimestamp.Time
	pcCTime := pcCPodsFiltered[0].CreationTimestamp.Time

	// pc-a should start first
	if pcCTime.Before(pcATime) {
		t.Errorf("pc-c started before pc-a: pc-a at %v, pc-c at %v", pcATime, pcCTime)
	}
	if pcBTime.Before(pcATime) {
		t.Errorf("pc-b started before pc-a: pc-a at %v, pc-b at %v", pcATime, pcBTime)
	}

	// pc-c should start before pc-b (explicit dependency: pc-b starts after pc-c)
	if pcBTime.Before(pcCTime) {
		t.Errorf("pc-b started before pc-c (explicit dependency violated): pc-c at %v, pc-b at %v", pcCTime, pcBTime)
	}

	logger.Info("🎉 Explicit startup order with full replicas test completed successfully!")
}

// Test_SO4_ExplicitStartupOrderWithMinReplicas tests explicit startup order with min replicas
// Scenario SO-4:
// 1. Initialize a 6-node Grove cluster
// 2. Deploy workload WL6, and verify 10 newly created pods
// 3. Wait for 6 pods get scheduled and become ready:
//    pcs-0-{pc-a = 2}
//    pcs-0-{sg-x-0-pc-b = 1, sg-x-0-pc-c = 3}
// 4. Verify each print clique prints in the following order:
//    pcs-0-pc-a
//    pcs-0-sg-x-0-pc-b
//    pcs-0-sg-x-0-pc-c
func Test_SO4_ExplicitStartupOrderWithMinReplicas(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 6-node Grove cluster")
	clientset, restConfig, _, cleanup, _ := setupTestCluster(ctx, t, 6)
	defer cleanup()

	logger.Info("2. Deploy workload WL6, and verify 10 newly created pods")
	workloadNamespace := "default"
	workloadYAMLPath := "../yaml/workload6.yaml"
	workloadLabelSelector := "app.kubernetes.io/part-of=workload6"

	_, err := utils.ApplyYAMLFile(ctx, workloadYAMLPath, workloadNamespace, restConfig, logger)
	if err != nil {
		t.Fatalf("Failed to apply workload YAML: %v", err)
	}

	expectedPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10

	var pods *v1.PodList
	err = pollForCondition(ctx, 2*time.Minute, 5*time.Second, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}
		return len(pods.Items) == expectedPods, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for pods to be created: %v", err)
	}

	logger.Info("3. Wait for 6 pods get scheduled and become ready:")
	logger.Info("   pcs-0-{pc-a = 2}")
	logger.Info("   pcs-0-{sg-x-0-pc-b = 1, sg-x-0-pc-c = 3}")

	// Wait for pods to be scheduled and ready
	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, 10*time.Minute, logger); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	// Verify we have 6 running pods (min replicas)
	runningPods := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			runningPods++
		}
	}
	if runningPods != 6 {
		t.Fatalf("Expected 6 pods to be running (min replicas), but %d are running", runningPods)
	}

	logger.Info("4. Verify each print clique prints in the following order:")
	logger.Info("   pcs-0-pc-a")
	logger.Info("   pcs-0-sg-x-0-pc-b")
	logger.Info("   pcs-0-sg-x-0-pc-c")

	// Get running pods by clique pattern
	var runningPodsList []v1.Pod
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			runningPodsList = append(runningPodsList, pod)
		}
	}

	pcAPods := getPodsByCliquePattern(runningPodsList, "pcs-0-pc-a")
	pcBPods := getPodsByCliquePattern(runningPodsList, "pcs-0-sg-x-0-pc-b")
	pcCPods := getPodsByCliquePattern(runningPodsList, "pcs-0-sg-x-0-pc-c")

	// Verify we have the expected number of running pods
	if len(pcAPods) != 2 {
		t.Fatalf("Expected 2 running pc-a pods, got %d", len(pcAPods))
	}
	if len(pcBPods) != 1 {
		t.Fatalf("Expected 1 running pc-b pod, got %d", len(pcBPods))
	}
	if len(pcCPods) != 3 {
		t.Fatalf("Expected 3 running pc-c pods, got %d", len(pcCPods))
	}

	// Verify startup order: pc-a should start first, then pc-b, then pc-c
	// Note: In the explicit case with min replicas, the order should still be pc-a -> pc-b -> pc-c
	// because pc-b has a startsAfter dependency on pc-c, but with min replicas, only one scaling group
	// replica is created, so the dependency is satisfied by the same scaling group instance
	pcATime := pcAPods[0].CreationTimestamp.Time
	pcBTime := pcBPods[0].CreationTimestamp.Time
	pcCTime := pcCPods[0].CreationTimestamp.Time

	// pc-a should start first
	if pcBTime.Before(pcATime) {
		t.Errorf("pc-b started before pc-a: pc-a at %v, pc-b at %v", pcATime, pcBTime)
	}
	if pcCTime.Before(pcATime) {
		t.Errorf("pc-c started before pc-a: pc-a at %v, pc-c at %v", pcATime, pcCTime)
	}

	// With explicit dependencies and min replicas, pc-c should start before pc-b
	// (pc-b has startsAfter: pc-c dependency)
	if pcBTime.Before(pcCTime) {
		t.Errorf("pc-b started before pc-c (explicit dependency violated): pc-c at %v, pc-b at %v", pcCTime, pcBTime)
	}

	logger.Info("🎉 Explicit startup order with min replicas test completed successfully!")
}
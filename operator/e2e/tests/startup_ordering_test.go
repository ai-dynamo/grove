//go:build e2e

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

// The file contains E2E tests for startup ordering functionality.
//
// Startup Ordering Mechanism:
// The Grove operator enforces startup ordering using init containers (grove-initc).
// The init container watches for parent PodCliques to reach their minAvailable count
// in the Ready state, blocking the pod from becoming ready until dependencies are satisfied.
//
// Test Verification Approach:
// These tests verify startup ordering by checking the LastTransitionTime of each pod's
// Ready condition (not CreationTimestamp). This is the correct approach because:
//   - CreationTimestamp: When the pod object was created (doesn't reflect dependencies)
//   - Ready LastTransitionTime: When the pod actually became ready (after init containers complete)
//
// The init container enforces ordering by blocking the Ready state, so we must check
// when pods became ready, not when they were created.

package tests

import (
	"context"
	"strings"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Test_SO1_InorderStartupOrderWithFullReplicas tests inorder startup with full replicas
// Scenario SO-1:
//  1. Initialize a 10-node Grove cluster
//  2. Deploy workload WL3, and verify 10 newly created pods
//  3. Wait for pods to get scheduled and become ready
//  4. Verify each print clique prints in the following order:
//     pcs-0-pc-a
//     pcs-0-sg-x-0-pc-b, pcs-0-sg-x-1-pc-b
//     pcs-0-sg-x-0-pc-c, pcs-0-sg-x-1-pc-c
func Test_SO1_InorderStartupOrderWithFullReplicas(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 10)
	defer cleanup()

	logger.Info("2. Deploy workload WL3, and verify 10 newly created pods")
	expectedPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10

	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		RestConfig:    restConfig,
		DynamicClient: dynamicClient,
		Namespace:     "default",
		Timeout:       5 * time.Minute,
		Interval:      defaultPollTimeout,
		Workload: &WorkloadConfig{
			Name:         "workload3",
			YAMLPath:     "../yaml/workload3.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	pods, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Wait for pods to get scheduled and become ready")
	if err := waitForPods(tc, expectedPods); err != nil {
		debugPodState(tc)
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	// Re-fetch pods to ensure we have the latest state with Ready conditions
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to re-fetch pods after waiting: %v", err)
	}

	logger.Info("4. Verify each print clique prints in the following order:")
	logger.Info("   pcs-0-pc-a")
	logger.Info("   pcs-0-sg-x-0-pc-b, pcs-0-sg-x-1-pc-b")
	logger.Info("   pcs-0-sg-x-0-pc-c, pcs-0-sg-x-1-pc-c")

	// Get pods by clique pattern
	pcAPods := getPodsByCliquePattern(pods.Items, "-pc-a-")
	pcBPodsFiltered := getPodsByCliquePattern(pods.Items, "-pc-b-")
	pcCPodsFiltered := getPodsByCliquePattern(pods.Items, "-pc-c-")

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

	// Verify startup order: all pc-a pods should start before any pc-b pod,
	// and all pc-b pods should start before any pc-c pod
	verifyGroupStartupOrder(t, pcAPods, pcBPodsFiltered, "pc-a", "pc-b")
	verifyGroupStartupOrder(t, pcBPodsFiltered, pcCPodsFiltered, "pc-b", "pc-c")

	logger.Info("🎉 Inorder startup order with full replicas test completed successfully!")
}

// Test_SO2_InorderStartupOrderWithMinReplicas tests inorder startup with min replicas
// Scenario SO-2:
//  1. Initialize a 10-node Grove cluster
//  2. Deploy workload WL4, and verify 10 newly created pods
//  3. Wait for 10 pods get scheduled and become ready:
//     pcs-0-{pc-a = 2}
//     pcs-0-{sg-x-0-pc-b = 1, sg-x-0-pc-c = 3} (base PodGang)
//     pcs-0-{sg-x-1-pc-b = 1, sg-x-1-pc-c = 3} (scaled PodGang - independent)
//  4. Verify startup order within each gang:
//     - pc-a starts before scaling groups
//     - Within sg-x-0: pc-a → pc-b → pc-c
//     - Within sg-x-1: pc-b → pc-c (independent from sg-x-0)
func Test_SO2_InorderStartupOrderWithMinReplicas(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 10)
	defer cleanup()

	logger.Info("2. Deploy workload WL4, and verify 10 newly created pods")
	expectedPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10

	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		RestConfig:    restConfig,
		DynamicClient: dynamicClient,
		Namespace:     "default",
		Timeout:       5 * time.Minute,
		Interval:      defaultPollTimeout,
		Workload: &WorkloadConfig{
			Name:         "workload4",
			YAMLPath:     "../yaml/workload4.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	pods, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Wait for 10 pods get scheduled and become ready:")
	logger.Info("   pcs-0-{pc-a = 2}")
	logger.Info("   pcs-0-{sg-x-0-pc-b = 1, sg-x-0-pc-c = 3} (there are 2 replicas)")
	logger.Info("   pcs-0-{sg-x-1-pc-b = 1, sg-x-1-pc-c = 3}")

	// Wait for all 10 pods to become running
	// Note: The original test waited for running pods manually. 
	// deployAndVerifyWorkload already waits for pods to be created.
	// But we want to wait for them to be running/ready.
	// We can use waitForRunningPods or waitForPods (which waits for Ready state).
	// The original code uses a custom poll to check PodRunning phase for all 10 pods.
	// setup.go has waitForRunningPods(tc, count).
	
	if err := waitForRunningPods(tc, 10); err != nil {
		debugPodState(tc)
		t.Fatalf("Failed to wait for 10 pods to be running: %v", err)
	}

	// The original test logic was:
	// "Wait for all 10 pods to become running" -> check phase == Running.
	// Then later verify startup order using Ready conditions.
	// Wait, if pods are just Running, they might not be Ready yet.
	// Startup ordering is checked based on Ready condition LastTransitionTime.
	// So we should probably wait for them to be Ready.
	// In the original code:
	// "3. Wait for 10 pods get scheduled and become ready:"
	// But the code block actually checks `pod.Status.Phase == v1.PodRunning`.
	// And then `verifyGroupStartupOrder` checks `getReadyConditionTransitionTime`.
	// If a pod is Running but not Ready, `getReadyConditionTransitionTime` returns zero time, and `verifyGroupStartupOrder` fails if no pods have ready timestamps.
	// So we MUST wait for Ready state.
	// The original test `Test_SO2` waited for `Phase == Running` in step 3.
	// But then in step 4 it calls `verifyGroupStartupOrder`.
	// If the pods are running but the init container hasn't finished (startup ordering), they won't be Ready.
	// Wait, if the test passes, it means they become Ready fast enough or `WaitForPods` was called earlier?
	// In `Test_SO2` original:
	// err = pollForCondition(..., func() { ... return runningPods == 10 })
	// Then verify.
	// If they are running, are they ready?
	// The init container blocks Ready.
	// So if we only wait for Running, they might be blocked by init container.
	// But if they are blocked, they are Running (init container running).
	// The test verifies startup order.
	// It seems `verifyGroupStartupOrder` checks Ready times.
	// So if we verify startup order, we need the pods to be Ready.
	// The original code might have relied on the fact that by the time we check, they are ready? Or maybe I misread.
	// In `Test_SO1`, it calls `utils.WaitForPods` (which usually implies Ready).
	// In `Test_SO2`, it calls manual poll for `PodRunning`.
	// BUT `verifyGroupStartupOrder` fails if "Group %s has no pods with valid Ready condition timestamps".
	// So the pods MUST be Ready for the verification to pass.
	// If `Test_SO2` only waited for Running, it might be flaky if they aren't Ready yet.
	// However, `Test_SO2` description says "Wait for 10 pods get scheduled and become ready".
	// So I will use `waitForPods(tc, 10)` which waits for Ready.
	
	if err := waitForPods(tc, 10); err != nil {
		debugPodState(tc)
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}
	
	// Re-fetch to get latest conditions
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to re-fetch pods: %v", err)
	}

	logger.Info("4. Verify startup order within each gang:")
	logger.Info("   pc-a starts before scaling groups")
	logger.Info("   Within sg-x-0 (base): pc-a → pc-b → pc-c")
	logger.Info("   Within sg-x-1 (scaled): pc-b → pc-c (independent)")

	// Get running pods by clique pattern
	// Note: original code filtered for Running phase again. listPods returns all pods.
	// If we waited for Ready, they are Running.
	
	var runningPodsList []v1.Pod
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			runningPodsList = append(runningPodsList, pod)
		}
	}

	pcAPods := getPodsByCliquePattern(runningPodsList, "-pc-a-")
	pcBPods := getPodsByCliquePattern(runningPodsList, "-pc-b-")
	pcCPods := getPodsByCliquePattern(runningPodsList, "-pc-c-")

	// Verify we have the expected number of running pods
	if len(pcAPods) != 2 {
		t.Fatalf("Expected 2 running pc-a pods, got %d", len(pcAPods))
	}
	if len(pcBPods) != 2 {
		t.Fatalf("Expected 2 running pc-b pods (1 per scaling group replica), got %d", len(pcBPods))
	}
	if len(pcCPods) != 6 {
		t.Fatalf("Expected 6 running pc-c pods (3 per scaling group replica), got %d", len(pcCPods))
	}

	// With minAvailable=1 for the scaling group:
	// - sg-x-0 (replica 0) is the base PodGang
	// - sg-x-1 (replica 1) is a scaled PodGang (independent)
	// Startup ordering is enforced WITHIN each gang, not globally across all gangs.
	// We need to verify ordering separately for each scaling group replica.

	// InOrder startup: pc-a → pc-b → pc-c (within each gang)

	// First verify pc-a starts before any scaling group pods
	allScalingGroupPods := append([]v1.Pod{}, pcBPods...)
	allScalingGroupPods = append(allScalingGroupPods, pcCPods...)
	verifyGroupStartupOrder(t, pcAPods, allScalingGroupPods, "pc-a", "scaling-groups")

	// Verify ordering within sg-x-0 (base gang): pc-b → pc-c
	sgX0PCBPods := getPodsByCliquePattern(pcBPods, "-sg-x-0-")
	sgX0PCCPods := getPodsByCliquePattern(pcCPods, "-sg-x-0-")
	if len(sgX0PCBPods) > 0 && len(sgX0PCCPods) > 0 {
		verifyGroupStartupOrder(t, sgX0PCBPods, sgX0PCCPods, "sg-x-0-pc-b", "sg-x-0-pc-c")
	}

	// Verify ordering within sg-x-1 (scaled gang): pc-b → pc-c
	sgX1PCBPods := getPodsByCliquePattern(pcBPods, "-sg-x-1-")
	sgX1PCCPods := getPodsByCliquePattern(pcCPods, "-sg-x-1-")
	if len(sgX1PCBPods) > 0 && len(sgX1PCCPods) > 0 {
		verifyGroupStartupOrder(t, sgX1PCBPods, sgX1PCCPods, "sg-x-1-pc-b", "sg-x-1-pc-c")
	}

	logger.Info("🎉 Inorder startup order with min replicas test completed successfully!")
}

// Test_SO3_ExplicitStartupOrderWithFullReplicas tests explicit startup order with full replicas
// Scenario SO-3:
//  1. Initialize a 10-node Grove cluster
//  2. Deploy workload WL5, and verify 10 newly created pods
//  3. Wait for pods to get scheduled and become ready
//  4. Verify each print clique prints in the following order:
//     pcs-0-pc-a
//     pcs-0-sg-x-0-pc-c, pcs-0-sg-x-1-pc-c
//     pcs-0-sg-x-0-pc-b, pcs-0-sg-x-1-pc-b
func Test_SO3_ExplicitStartupOrderWithFullReplicas(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 10)
	defer cleanup()

	logger.Info("2. Deploy workload WL5, and verify 10 newly created pods")
	expectedPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10

	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		RestConfig:    restConfig,
		DynamicClient: dynamicClient,
		Namespace:     "default",
		Timeout:       5 * time.Minute,
		Interval:      defaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         "workload5",
			YAMLPath:     "../yaml/workload5.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	pods, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Wait for pods to get scheduled and become ready")
	if err := waitForPods(tc, expectedPods); err != nil {
		debugPodState(tc)
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	// Re-fetch pods
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to re-fetch pods: %v", err)
	}

	logger.Info("4. Verify each print clique prints in the following order:")
	logger.Info("   pcs-0-pc-a")
	logger.Info("   pcs-0-sg-x-0-pc-c, pcs-0-sg-x-1-pc-c")
	logger.Info("   pcs-0-sg-x-0-pc-b, pcs-0-sg-x-1-pc-b")

	// Get pods by clique pattern
	pcAPods := getPodsByCliquePattern(pods.Items, "-pc-a-")
	pcBPodsFiltered := getPodsByCliquePattern(pods.Items, "-pc-b-")
	pcCPodsFiltered := getPodsByCliquePattern(pods.Items, "-pc-c-")

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

	// Verify startup order: all pc-a pods should start first, then all pc-c pods,
	// then all pc-b pods (explicit dependency: pc-b starts after pc-c)
	verifyGroupStartupOrder(t, pcAPods, pcCPodsFiltered, "pc-a", "pc-c")
	verifyGroupStartupOrder(t, pcAPods, pcBPodsFiltered, "pc-a", "pc-b")
	verifyGroupStartupOrder(t, pcCPodsFiltered, pcBPodsFiltered, "pc-c", "pc-b")

	logger.Info("🎉 Explicit startup order with full replicas test completed successfully!")
}

// Test_SO4_ExplicitStartupOrderWithMinReplicas tests explicit startup order with min replicas
// Scenario SO-4:
//  1. Initialize a 10-node Grove cluster
//  2. Deploy workload WL6, and verify 10 newly created pods
//  3. Wait for 10 pods get scheduled and become ready:
//     pcs-0-{pc-a = 2}
//     pcs-0-{sg-x-0-pc-b = 1, sg-x-0-pc-c = 3} (base PodGang)
//     pcs-0-{sg-x-1-pc-b = 1, sg-x-1-pc-c = 3} (scaled PodGang - independent)
//  4. Verify startup order within each gang (explicit dependency: pc-c startsAfter pc-b):
//     - pc-a starts before scaling groups
//     - Within sg-x-0: pc-a → pc-b → pc-c
//     - Within sg-x-1: pc-b → pc-c (independent from sg-x-0)
func Test_SO4_ExplicitStartupOrderWithMinReplicas(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 10)
	defer cleanup()

	logger.Info("2. Deploy workload WL6, and verify 10 newly created pods")
	expectedPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10

	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		RestConfig:    restConfig,
		DynamicClient: dynamicClient,
		Namespace:     "default",
		Timeout:       5 * time.Minute,
		Interval:      defaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         "workload6",
			YAMLPath:     "../yaml/workload6.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	pods, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Wait for 10 pods get scheduled and become ready:")
	logger.Info("   pcs-0-{pc-a = 2}")
	logger.Info("   pcs-0-{sg-x-0-pc-b = 1, sg-x-0-pc-c = 3} (there are 2 replicas)")
	logger.Info("   pcs-0-{sg-x-1-pc-b = 1, sg-x-1-pc-c = 3}")

	// Wait for all 10 pods to become ready
	if err := waitForPods(tc, 10); err != nil {
		debugPodState(tc)
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}
	
	// Re-fetch pods
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to re-fetch pods: %v", err)
	}

	logger.Info("4. Verify startup order within each gang:")
	logger.Info("   pc-a starts before scaling groups")
	logger.Info("   Within sg-x-0 (base): pc-a → pc-b → pc-c (explicit dependency)")
	logger.Info("   Within sg-x-1 (scaled): pc-b → pc-c (independent)")

	// Get running pods by clique pattern
	var runningPodsList []v1.Pod
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			runningPodsList = append(runningPodsList, pod)
		}
	}

	pcAPods := getPodsByCliquePattern(runningPodsList, "-pc-a-")
	pcBPods := getPodsByCliquePattern(runningPodsList, "-pc-b-")
	pcCPods := getPodsByCliquePattern(runningPodsList, "-pc-c-")

	// Verify we have the expected number of running pods
	if len(pcAPods) != 2 {
		t.Fatalf("Expected 2 running pc-a pods, got %d", len(pcAPods))
	}
	if len(pcBPods) != 2 {
		t.Fatalf("Expected 2 running pc-b pods (1 per scaling group replica), got %d", len(pcBPods))
	}
	if len(pcCPods) != 6 {
		t.Fatalf("Expected 6 running pc-c pods (3 per scaling group replica), got %d", len(pcCPods))
	}

	// With minAvailable=1 for the scaling group:
	// - sg-x-0 (replica 0) is the base PodGang
	// - sg-x-1 (replica 1) is a scaled PodGang (independent)
	// Startup ordering is enforced WITHIN each gang, not globally across all gangs.
	// We need to verify ordering separately for each scaling group replica.

	// Explicit startup: pc-a → pc-b → pc-c (pc-c has startsAfter: [pc-b])

	// First verify pc-a starts before any scaling group pods
	allScalingGroupPods := append([]v1.Pod{}, pcBPods...)
	allScalingGroupPods = append(allScalingGroupPods, pcCPods...)
	verifyGroupStartupOrder(t, pcAPods, allScalingGroupPods, "pc-a", "scaling-groups")

	// Verify ordering within sg-x-0 (base gang): pc-b → pc-c
	sgX0PCBPods := getPodsByCliquePattern(pcBPods, "-sg-x-0-")
	sgX0PCCPods := getPodsByCliquePattern(pcCPods, "-sg-x-0-")
	if len(sgX0PCBPods) > 0 && len(sgX0PCCPods) > 0 {
		verifyGroupStartupOrder(t, sgX0PCBPods, sgX0PCCPods, "sg-x-0-pc-b", "sg-x-0-pc-c")
	}

	// Verify ordering within sg-x-1 (scaled gang): pc-b → pc-c
	sgX1PCBPods := getPodsByCliquePattern(pcBPods, "-sg-x-1-")
	sgX1PCCPods := getPodsByCliquePattern(pcCPods, "-sg-x-1-")
	if len(sgX1PCBPods) > 0 && len(sgX1PCCPods) > 0 {
		verifyGroupStartupOrder(t, sgX1PCBPods, sgX1PCCPods, "sg-x-1-pc-b", "sg-x-1-pc-c")
	}

	logger.Info("🎉 Explicit startup order with min replicas test completed successfully!")
}

// Helper function to get the Ready condition's LastTransitionTime from a pod
// According to the sample files, this is the correct timestamp to check for startup ordering
func getReadyConditionTransitionTime(pod v1.Pod) time.Time {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
			return condition.LastTransitionTime.Time
		}
	}
	// Debug: log why we couldn't find a Ready timestamp
	logger.Debugf("Pod %s has no Ready=True condition. Phase: %s, Conditions: %+v",
		pod.Name, pod.Status.Phase, pod.Status.Conditions)
	return time.Time{}
}

// Helper function to get the earliest Ready transition time from a list of pods
func getEarliestPodTime(pods []v1.Pod) time.Time {
	if len(pods) == 0 {
		return time.Time{}
	}

	var earliest time.Time
	for _, pod := range pods {
		readyTime := getReadyConditionTransitionTime(pod)
		if readyTime.IsZero() {
			continue // Skip pods without a valid Ready timestamp
		}
		if earliest.IsZero() || readyTime.Before(earliest) {
			earliest = readyTime
		}
	}
	return earliest
}

// Helper function to get the latest Ready transition time from a list of pods
func getLatestPodTime(pods []v1.Pod) time.Time {
	if len(pods) == 0 {
		return time.Time{}
	}

	var latest time.Time
	for _, pod := range pods {
		readyTime := getReadyConditionTransitionTime(pod)
		if readyTime.IsZero() {
			continue // Skip pods without a valid Ready timestamp
		}
		if readyTime.After(latest) {
			latest = readyTime
		}
	}
	return latest
}

// Helper function to verify that all pods in groupBefore started before all pods in groupAfter
func verifyGroupStartupOrder(t *testing.T, groupBefore, groupAfter []v1.Pod, beforeName, afterName string) {
	t.Helper() // Mark as helper for better error reporting

	if len(groupBefore) == 0 {
		t.Fatalf("Group %s has no pods", beforeName)
	}
	if len(groupAfter) == 0 {
		t.Fatalf("Group %s has no pods", afterName)
	}

	// Get the latest time from the "before" group
	latestBefore := getLatestPodTime(groupBefore)
	// Get the earliest time from the "after" group
	earliestAfter := getEarliestPodTime(groupAfter)

	// Check for pods without Ready timestamps
	if latestBefore.IsZero() {
		// Debug: Show which pods don't have Ready timestamps
		logger.Errorf("Group %s has no pods with valid Ready timestamps. Debugging pod states:", beforeName)
		for i, pod := range groupBefore {
			readyTime := getReadyConditionTransitionTime(pod)
			logger.Errorf("  Pod[%d] %s: Phase=%s, ReadyTime=%v", i, pod.Name, pod.Status.Phase, readyTime)
		}
		t.Fatalf("Group %s has no pods with valid Ready condition timestamps (pods may not be ready yet)", beforeName)
	}
	if earliestAfter.IsZero() {
		// Debug: Show which pods don't have Ready timestamps
		logger.Errorf("Group %s has no pods with valid Ready timestamps. Debugging pod states:", afterName)
		for i, pod := range groupAfter {
			readyTime := getReadyConditionTransitionTime(pod)
			logger.Errorf("  Pod[%d] %s: Phase=%s, ReadyTime=%v", i, pod.Name, pod.Status.Phase, readyTime)
		}
		t.Fatalf("Group %s has no pods with valid Ready condition timestamps (pods may not be ready yet)", afterName)
	}

	// Verify the ordering: all pods in groupBefore should start before any pod in groupAfter
	if earliestAfter.Before(latestBefore) {
		t.Fatalf("Startup order violation: group %s (earliest at %v) started before group %s (latest at %v)",
			afterName, earliestAfter, beforeName, latestBefore)
	}

	logger.Debugf("✓ Verified startup order: %s (latest: %v) → %s (earliest: %v)",
		beforeName, latestBefore, afterName, earliestAfter)
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

// debugPodState logs detailed state information for all pods in the namespace.
// This helps diagnose why pods might not be becoming Ready.
func debugPodState(tc TestContext) {
	pods, err := listPods(tc)
	if err != nil {
		logger.Errorf("Failed to list pods for debugging: %v", err)
		return
	}
	logger.Infof("Debug: Found %d pods in namespace %s", len(pods.Items), tc.Namespace)
	for _, pod := range pods.Items {
		logger.Infof("Pod %s: Phase=%s, Reason=%s, Message=%s", pod.Name, pod.Status.Phase, pod.Status.Reason, pod.Status.Message)
		for _, cond := range pod.Status.Conditions {
			if cond.Status != v1.ConditionTrue {
				logger.Infof("  Condition %s=%s: %s", cond.Type, cond.Status, cond.Message)
			}
		}
		// Log init container statuses
		for _, status := range pod.Status.InitContainerStatuses {
			if !status.Ready {
				logger.Infof("  InitContainer %s: Ready=%v, State=%+v", status.Name, status.Ready, status.State)
			}
		}
		// Log container statuses
		for _, status := range pod.Status.ContainerStatuses {
			if !status.Ready {
				logger.Infof("  Container %s: Ready=%v, State=%+v", status.Name, status.Ready, status.State)
			}
		}
		// Events
		events, err := tc.Clientset.CoreV1().Events(tc.Namespace).List(tc.Ctx, metav1.ListOptions{
			FieldSelector: "involvedObject.name=" + pod.Name,
		})
		if err == nil {
			for _, e := range events.Items {
				if e.Type == "Warning" {
					logger.Infof("  Event Warning: %s: %s", e.Reason, e.Message)
				}
			}
		}
	}
}

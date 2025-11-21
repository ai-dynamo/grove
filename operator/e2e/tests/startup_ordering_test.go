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
//  4. Verify each pod clique starts in the following order:
//     pcs-0-pc-a
//     pcs-0-sg-x-0-pc-b, pcs-0-sg-x-1-pc-b
//     pcs-0-sg-x-0-pc-c, pcs-0-sg-x-1-pc-c
func Test_SO1_InorderStartupOrderWithFullReplicas(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster")
	totalPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, totalPods)
	defer cleanup()

	logger.Info("2. Deploy workload WL3, and verify 10 newly created pods")
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
			ExpectedPods: totalPods,
		},
	}

	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Wait for pods to get scheduled and become ready")
	if err := waitForReadyPods(tc, totalPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	logger.Info("4. Verify each pod clique starts in the following order:")
	logger.Info("   pcs-0-pc-a")
	logger.Info("   pcs-0-sg-x-0-pc-b, pcs-0-sg-x-1-pc-b")
	logger.Info("   pcs-0-sg-x-0-pc-c, pcs-0-sg-x-1-pc-c")
	verifyCliqueGroupStartupOrder(t, tc, "pc-a", 2, "pc-b", 2)
	verifyCliqueGroupStartupOrder(t, tc, "pc-b", 2, "pc-c", 6)

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
	totalPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, totalPods)
	defer cleanup()

	logger.Info("2. Deploy workload WL4, and verify 10 newly created pods")
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
			ExpectedPods: totalPods,
		},
	}

	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Wait for 10 pods get scheduled and become ready:")
	logger.Info("   pcs-0-{pc-a = 2}")
	logger.Info("   pcs-0-{sg-x-0-pc-b = 1, sg-x-0-pc-c = 3} (there are 2 replicas)")
	logger.Info("   pcs-0-{sg-x-1-pc-b = 1, sg-x-1-pc-c = 3}")
	if err := waitForReadyPods(tc, totalPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	logger.Info("4. Verify startup order within each gang:")
	logger.Info("   pc-a starts before scaling groups")
	logger.Info("   Within sg-x-0 (base): pc-a → pc-b → pc-c")
	logger.Info("   Within sg-x-1 (scaled): pc-b → pc-c (independent)")

	// Verify complex startup ordering for scaling groups
	verifyScalingGroupStartupOrder(t, tc, ScalingGroupOrderSpec{
		PrefixGroups: []CliqueGroupSpec{
			{Name: "pc-a", ExpectedCount: 2},
		},
		ScalingGroupReplicas: []string{"-sg-x-0-", "-sg-x-1-"},
		WithinReplicaOrder: []CliqueGroupSpec{
			{Name: "pc-b", ExpectedCount: 2},
			{Name: "pc-c", ExpectedCount: 6},
		},
	})

	logger.Info("🎉 Inorder startup order with min replicas test completed successfully!")
}

// Test_SO3_ExplicitStartupOrderWithFullReplicas tests explicit startup order with full replicas
// Scenario SO-3:
//  1. Initialize a 10-node Grove cluster
//  2. Deploy workload WL5, and verify 10 newly created pods
//  3. Wait for pods to get scheduled and become ready
//  4. Verify each pod clique starts in the following order:
//     pcs-0-pc-a
//     pcs-0-sg-x-0-pc-c, pcs-0-sg-x-1-pc-c
//     pcs-0-sg-x-0-pc-b, pcs-0-sg-x-1-pc-b
func Test_SO3_ExplicitStartupOrderWithFullReplicas(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster")
	totalPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, totalPods)
	defer cleanup()

	logger.Info("2. Deploy workload WL5, and verify 10 newly created pods")
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
			ExpectedPods: totalPods,
		},
	}

	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Wait for pods to get scheduled and become ready")
	if err := waitForReadyPods(tc, tc.Workload.ExpectedPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	logger.Info("4. Verify each pod clique starts in the following order:")
	logger.Info("   pcs-0-pc-a")
	logger.Info("   pcs-0-sg-x-0-pc-c, pcs-0-sg-x-1-pc-c")
	logger.Info("   pcs-0-sg-x-0-pc-b, pcs-0-sg-x-1-pc-b")
	verifyCliqueGroupStartupOrder(t, tc, "pc-a", 2, "pc-c", 6)
	verifyCliqueGroupStartupOrder(t, tc, "pc-a", 2, "pc-b", 2)
	verifyCliqueGroupStartupOrder(t, tc, "pc-c", 6, "pc-b", 2)

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
	totalPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, totalPods)
	defer cleanup()

	logger.Info("2. Deploy workload WL6, and verify 10 newly created pods")
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
			ExpectedPods: totalPods,
		},
	}

	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Wait for 10 pods get scheduled and become ready:")
	logger.Info("   pcs-0-{pc-a = 2}")
	logger.Info("   pcs-0-{sg-x-0-pc-b = 1, sg-x-0-pc-c = 3} (there are 2 replicas)")
	logger.Info("   pcs-0-{sg-x-1-pc-b = 1, sg-x-1-pc-c = 3}")
	// Wait for all 10 pods to become ready
	if err := waitForReadyPods(tc, totalPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	logger.Info("4. Verify startup order within each gang:")
	logger.Info("   pc-a starts before scaling groups")
	logger.Info("   Within sg-x-0 (base): pc-a → pc-b → pc-c (explicit dependency)")
	logger.Info("   Within sg-x-1 (scaled): pc-b → pc-c (independent)")

	// With minAvailable=1 for the scaling group:
	// - sg-x-0 (replica 0) is the base PodGang
	// - sg-x-1 (replica 1) is a scaled PodGang (independent)
	// Startup ordering is enforced WITHIN each gang, not globally across all gangs.
	// Explicit startup: pc-a → pc-b → pc-c (pc-c has startsAfter: [pc-b])
	verifyScalingGroupStartupOrder(t, tc, ScalingGroupOrderSpec{
		PrefixGroups: []CliqueGroupSpec{
			{Name: "pc-a", ExpectedCount: 2},
		},
		ScalingGroupReplicas: []string{"-sg-x-0-", "-sg-x-1-"},
		WithinReplicaOrder: []CliqueGroupSpec{
			{Name: "pc-b", ExpectedCount: 2},
			{Name: "pc-c", ExpectedCount: 6},
		},
	})

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

// CliqueGroupSpec defines a group of pods by clique name and expected count
type CliqueGroupSpec struct {
	Name          string // The clique name pattern (e.g., "pc-a", "pc-b")
	ExpectedCount int    // Expected number of pods in this group
}

// ScalingGroupOrderSpec defines the startup ordering verification for scaling groups
type ScalingGroupOrderSpec struct {
	// PrefixGroups are clique groups that should start before all scaling group replicas
	PrefixGroups []CliqueGroupSpec

	// ScalingGroupReplicas are the scaling group replica patterns to verify (e.g., "-sg-x-0-", "-sg-x-1-")
	ScalingGroupReplicas []string

	// WithinReplicaOrder defines the startup order within each scaling group replica
	// Each element is a list of clique groups that should start in order
	WithinReplicaOrder []CliqueGroupSpec
}

// verifyScalingGroupStartupOrder verifies startup ordering for tests with scaling groups and minAvailable.
// It uses the ScalingGroupOrderSpec to define:
// 1. Which clique groups start before all scaling group pods
// 2. Which scaling group replicas to check
// 3. What startup order to verify within each scaling group replica
func verifyScalingGroupStartupOrder(t *testing.T, tc TestContext, spec ScalingGroupOrderSpec) {
	t.Helper()

	// Fetch the latest pod state
	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to fetch pods: %v", err)
	}

	// Get running pods
	var runningPodsList []v1.Pod
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			runningPodsList = append(runningPodsList, pod)
		}
	}

	// Build a map of clique name -> pods for all groups
	cliquePods := make(map[string][]v1.Pod)

	// Collect prefix group pods
	for _, group := range spec.PrefixGroups {
		cliquePods[group.Name] = getPodsByCliquePattern(runningPodsList, group.Name)
	}

	// Collect scaling group pods for each clique in the within-replica order
	for _, group := range spec.WithinReplicaOrder {
		if _, exists := cliquePods[group.Name]; !exists {
			cliquePods[group.Name] = getPodsByCliquePattern(runningPodsList, group.Name)
		}
	}

	// Verify counts for prefix groups
	for _, group := range spec.PrefixGroups {
		pods := cliquePods[group.Name]
		if len(pods) != group.ExpectedCount {
			t.Fatalf("Expected %d running %s pods, got %d", group.ExpectedCount, group.Name, len(pods))
		}
	}

	// Verify counts for within-replica order groups
	for _, group := range spec.WithinReplicaOrder {
		pods := cliquePods[group.Name]
		if len(pods) != group.ExpectedCount {
			t.Fatalf("Expected %d running %s pods, got %d", group.ExpectedCount, group.Name, len(pods))
		}
	}

	// Verify prefix groups start before all scaling group pods
	if len(spec.PrefixGroups) > 0 {
		// Collect all scaling group pods
		var allScalingGroupPods []v1.Pod
		for _, group := range spec.WithinReplicaOrder {
			allScalingGroupPods = append(allScalingGroupPods, cliquePods[group.Name]...)
		}

		// Verify each prefix group starts before scaling groups
		for _, group := range spec.PrefixGroups {
			prefixPods := cliquePods[group.Name]
			if len(prefixPods) > 0 && len(allScalingGroupPods) > 0 {
				verifyGroupStartupOrder(t, prefixPods, allScalingGroupPods, group.Name, "scaling-groups")
			}
		}
	}

	// Verify ordering within each scaling group replica
	for _, replicaPattern := range spec.ScalingGroupReplicas {
		// For each replica, verify the ordering of cliques within it
		for i := 0; i < len(spec.WithinReplicaOrder)-1; i++ {
			beforeGroup := spec.WithinReplicaOrder[i]
			afterGroup := spec.WithinReplicaOrder[i+1]

			// Filter pods by replica pattern
			beforePods := getPodsByCliquePattern(cliquePods[beforeGroup.Name], replicaPattern)
			afterPods := getPodsByCliquePattern(cliquePods[afterGroup.Name], replicaPattern)

			if len(beforePods) > 0 && len(afterPods) > 0 {
				beforeName := replicaPattern + beforeGroup.Name
				afterName := replicaPattern + afterGroup.Name
				verifyGroupStartupOrder(t, beforePods, afterPods, beforeName, afterName)
			}
		}
	}
}

// verifyCliqueGroupStartupOrder combines pod fetching, filtering, count verification, and startup order verification.
// It fetches the latest pod state, gets pods matching the clique names, verifies their counts,
// and then verifies that all pods in the "before" group started before all pods in the "after" group.
func verifyCliqueGroupStartupOrder(t *testing.T, tc TestContext,
	beforeClique string, beforeCount int,
	afterClique string, afterCount int) {
	t.Helper()

	// Always fetch the latest pod state to ensure we have current Ready conditions
	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to fetch pods: %v", err)
	}

	// Get pods by clique name (getPodsByCliquePattern automatically adds dashes)
	groupBefore := getPodsByCliquePattern(pods.Items, beforeClique)
	groupAfter := getPodsByCliquePattern(pods.Items, afterClique)

	// Verify counts
	if len(groupBefore) != beforeCount {
		t.Fatalf("Expected %d %s pods, got %d", beforeCount, beforeClique, len(groupBefore))
	}
	if len(groupAfter) != afterCount {
		t.Fatalf("Expected %d %s pods, got %d", afterCount, afterClique, len(groupAfter))
	}

	// Verify startup order
	verifyGroupStartupOrder(t, groupBefore, groupAfter, beforeClique, afterClique)
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

// Helper function to get pods by clique name pattern.
// If the pattern doesn't start with "-", it's treated as a clique name and wrapped with dashes.
// Otherwise, it's used as-is (for sub-patterns like "-sg-x-0-").
func getPodsByCliquePattern(pods []v1.Pod, pattern string) []v1.Pod {
	// If pattern doesn't start with "-", treat it as a clique name and wrap with dashes
	searchPattern := pattern
	if !strings.HasPrefix(pattern, "-") {
		searchPattern = "-" + pattern + "-"
	}

	var result []v1.Pod
	for _, pod := range pods {
		if strings.Contains(pod.Name, searchPattern) {
			result = append(result, pod)
		}
	}
	return result
}

// debugPodState logs detailed state information for all pods in the namespace.
// Use this to help diagnose why pods might not be becoming Ready.
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

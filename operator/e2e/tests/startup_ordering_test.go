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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/testctx"
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

	Logger.Info("1. Initialize a 10-node Grove cluster")
	totalPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10
	tc, cleanup := testctx.PrepareTest(ctx, t, totalPods,
		testctx.WithTimeout(5*time.Minute),
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload3",
			YAMLPath:     "../yaml/workload3.yaml",
			Namespace:    "default",
			ExpectedPods: totalPods,
		}),
	)
	defer cleanup()

	Logger.Info("2. Deploy workload WL3, and verify 10 newly created pods")
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Wait for pods to get scheduled and become ready")
	if err := tc.WaitForReadyPods(totalPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	Logger.Info("4. Verify each pod clique starts in the following order:")
	Logger.Info("   pcs-0-pc-a")
	Logger.Info("   pcs-0-sg-x-0-pc-b, pcs-0-sg-x-1-pc-b")
	Logger.Info("   pcs-0-sg-x-0-pc-c, pcs-0-sg-x-1-pc-c")
	verifyPodCliqueStartupOrder(t, tc, "pc-a", 2, "pc-b", 2)
	verifyPodCliqueStartupOrder(t, tc, "pc-b", 2, "pc-c", 6)

	Logger.Info("Inorder startup order with full replicas test completed successfully!")
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

	Logger.Info("1. Initialize a 10-node Grove cluster")
	totalPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10
	tc, cleanup := testctx.PrepareTest(ctx, t, totalPods,
		testctx.WithTimeout(5*time.Minute),
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload4",
			YAMLPath:     "../yaml/workload4.yaml",
			Namespace:    "default",
			ExpectedPods: totalPods,
		}),
	)
	defer cleanup()

	Logger.Info("2. Deploy workload WL4, and verify 10 newly created pods")
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Wait for 10 pods get scheduled and become ready:")
	Logger.Info("   pcs-0-{pc-a = 2}")
	Logger.Info("   pcs-0-{sg-x-0-pc-b = 1, sg-x-0-pc-c = 3} (there are 2 replicas)")
	Logger.Info("   pcs-0-{sg-x-1-pc-b = 1, sg-x-1-pc-c = 3}")
	if err := tc.WaitForReadyPods(totalPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	Logger.Info("4. Verify startup order within each gang:")
	Logger.Info("   pc-a starts before scaling groups")
	Logger.Info("   Within sg-x-0 (base): pc-a → pc-b → pc-c")
	Logger.Info("   Within sg-x-1 (scaled): pc-b → pc-c (independent)")

	// Verify complex startup ordering for scaling groups
	// minAvailable values from workload4.yaml: pc-a=1, pc-b=1, pc-c=1
	verifyScalingGroupStartupOrder(t, tc, ScalingGroupOrderSpec{
		PrefixGroups: []PodCliqueSpec{
			{Name: "pc-a", ExpectedCount: 2, MinAvailable: 1},
		},
		ScalingGroupReplicas: []string{"-sg-x-0-", "-sg-x-1-"},
		WithinReplicaOrder: []PodCliqueSpec{
			{Name: "pc-b", ExpectedCount: 2, MinAvailable: 1},
			{Name: "pc-c", ExpectedCount: 6, MinAvailable: 1},
		},
	})

	Logger.Info("Inorder startup order with min replicas test completed successfully!")
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

	Logger.Info("1. Initialize a 10-node Grove cluster")
	totalPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10
	tc, cleanup := testctx.PrepareTest(ctx, t, totalPods,
		testctx.WithTimeout(5*time.Minute),
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload5",
			YAMLPath:     "../yaml/workload5.yaml",
			Namespace:    "default",
			ExpectedPods: totalPods,
		}),
	)
	defer cleanup()

	Logger.Info("2. Deploy workload WL5, and verify 10 newly created pods")
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Wait for pods to get scheduled and become ready")
	if err := tc.WaitForReadyPods(tc.Workload.ExpectedPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	Logger.Info("4. Verify each pod clique starts in the following order:")
	Logger.Info("   pcs-0-pc-a")
	Logger.Info("   pcs-0-sg-x-0-pc-c, pcs-0-sg-x-1-pc-c")
	Logger.Info("   pcs-0-sg-x-0-pc-b, pcs-0-sg-x-1-pc-b")
	verifyPodCliqueStartupOrder(t, tc, "pc-a", 2, "pc-c", 6)
	verifyPodCliqueStartupOrder(t, tc, "pc-a", 2, "pc-b", 2)
	verifyPodCliqueStartupOrder(t, tc, "pc-c", 6, "pc-b", 2)

	Logger.Info("Explicit startup order with full replicas test completed successfully!")
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

	Logger.Info("1. Initialize a 10-node Grove cluster")
	totalPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10
	tc, cleanup := testctx.PrepareTest(ctx, t, totalPods,
		testctx.WithTimeout(5*time.Minute),
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload6",
			YAMLPath:     "../yaml/workload6.yaml",
			Namespace:    "default",
			ExpectedPods: totalPods,
		}),
	)
	defer cleanup()

	Logger.Info("2. Deploy workload WL6, and verify 10 newly created pods")
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Wait for 10 pods get scheduled and become ready:")
	Logger.Info("   pcs-0-{pc-a = 2}")
	Logger.Info("   pcs-0-{sg-x-0-pc-b = 1, sg-x-0-pc-c = 3} (there are 2 replicas)")
	Logger.Info("   pcs-0-{sg-x-1-pc-b = 1, sg-x-1-pc-c = 3}")
	// Wait for all 10 pods to become ready
	if err := tc.WaitForReadyPods(totalPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	Logger.Info("4. Verify startup order within each gang:")
	Logger.Info("   pc-a starts before scaling groups")
	Logger.Info("   Within sg-x-0 (base): pc-a → pc-b → pc-c (explicit dependency)")
	Logger.Info("   Within sg-x-1 (scaled): pc-b → pc-c (independent)")

	// With minAvailable=1 for the scaling group:
	// - sg-x-0 (replica 0) is the base PodGang
	// - sg-x-1 (replica 1) is a scaled PodGang (independent)
	// Startup ordering is enforced WITHIN each gang, not globally across all gangs.
	// Explicit startup: pc-a → pc-b → pc-c (pc-c has startsAfter: [pc-b])
	// minAvailable values from workload6.yaml: pc-a=1, pc-b=1, pc-c=1
	verifyScalingGroupStartupOrder(t, tc, ScalingGroupOrderSpec{
		PrefixGroups: []PodCliqueSpec{
			{Name: "pc-a", ExpectedCount: 2, MinAvailable: 1},
		},
		ScalingGroupReplicas: []string{"-sg-x-0-", "-sg-x-1-"},
		WithinReplicaOrder: []PodCliqueSpec{
			{Name: "pc-b", ExpectedCount: 2, MinAvailable: 1},
			{Name: "pc-c", ExpectedCount: 6, MinAvailable: 1},
		},
	})

	Logger.Info("Explicit startup order with min replicas test completed successfully!")
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
	Logger.Debugf("Pod %s has no Ready=True condition. Phase: %s, Conditions: %+v",
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

// Helper function to get the Nth earliest Ready transition time from a list of pods.
// n is 1-based (n=1 returns the earliest, n=2 returns the second earliest, etc.)
// This is used to verify minAvailable-based startup ordering where only n pods need to be ready.
func getNthEarliestPodTime(pods []v1.Pod, n int) time.Time {
	if len(pods) == 0 || n <= 0 {
		return time.Time{}
	}

	// Collect all valid ready times
	var readyTimes []time.Time
	for _, pod := range pods {
		readyTime := getReadyConditionTransitionTime(pod)
		if !readyTime.IsZero() {
			readyTimes = append(readyTimes, readyTime)
		}
	}

	// If we don't have enough pods with ready times, return zero
	if len(readyTimes) < n {
		return time.Time{}
	}

	// Sort times in ascending order (earliest first)
	slices.SortFunc(readyTimes, func(a, b time.Time) int {
		return a.Compare(b)
	})

	// Return the Nth earliest (1-indexed, so n-1 in 0-indexed array)
	return readyTimes[n-1]
}

// PodCliqueSpec defines a group of pods by clique name and expected count
type PodCliqueSpec struct {
	Name          string // The clique name pattern (e.g., "pc-a", "pc-b")
	ExpectedCount int    // Expected number of pods in this group
	MinAvailable  int    // Minimum pods that must be ready before dependents can start (must be > 0)
}

// ScalingGroupOrderSpec defines the startup ordering verification for scaling groups
type ScalingGroupOrderSpec struct {
	// PrefixGroups are clique groups that should start before all scaling group replicas
	PrefixGroups []PodCliqueSpec

	// ScalingGroupReplicas are the scaling group replica patterns to verify (e.g., "-sg-x-0-", "-sg-x-1-")
	ScalingGroupReplicas []string

	// WithinReplicaOrder defines the startup order within each scaling group replica
	// Each element is a list of clique groups that should start in order
	WithinReplicaOrder []PodCliqueSpec
}

// verifyScalingGroupStartupOrder verifies startup ordering for tests with scaling groups and minAvailable.
func verifyScalingGroupStartupOrder(t *testing.T, tc *testctx.TestContext, spec ScalingGroupOrderSpec) {
	t.Helper()

	// Fetch the latest pod state
	pods, err := tc.ListPods()
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

	// Build a map of clique name pattern -> matching pods for all PodCliqueSpec test groups
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

	// Verify counts for all groups (prefix and within-replica order)
	allGroups := append([]PodCliqueSpec{}, spec.PrefixGroups...)
	allGroups = append(allGroups, spec.WithinReplicaOrder...)
	for _, group := range allGroups {
		pods := cliquePods[group.Name]
		if len(pods) != group.ExpectedCount {
			t.Fatalf("Expected %d running %s pods, got %d", group.ExpectedCount, group.Name, len(pods))
		}
	}

	// Verify prefix groups start before all scaling group pods
	if len(spec.PrefixGroups) > 0 {
		var allScalingGroupPods []v1.Pod
		for _, group := range spec.WithinReplicaOrder {
			allScalingGroupPods = append(allScalingGroupPods, cliquePods[group.Name]...)
		}

		for _, group := range spec.PrefixGroups {
			prefixPods := cliquePods[group.Name]
			if len(prefixPods) > 0 && len(allScalingGroupPods) > 0 {
				verifyGroupStartupOrderWithMinAvailable(t, prefixPods, allScalingGroupPods, group.Name, "scaling-groups", group.MinAvailable)
			}
		}
	}

	// Verify ordering within each scaling group replica
	for _, replicaPattern := range spec.ScalingGroupReplicas {
		for i := 0; i < len(spec.WithinReplicaOrder)-1; i++ {
			beforeGroup := spec.WithinReplicaOrder[i]
			afterGroup := spec.WithinReplicaOrder[i+1]

			beforePods := getPodsByCliquePattern(cliquePods[beforeGroup.Name], replicaPattern)
			afterPods := getPodsByCliquePattern(cliquePods[afterGroup.Name], replicaPattern)

			if len(beforePods) > 0 && len(afterPods) > 0 {
				beforeName := replicaPattern + beforeGroup.Name
				afterName := replicaPattern + afterGroup.Name
				verifyGroupStartupOrderWithMinAvailable(t, beforePods, afterPods, beforeName, afterName, beforeGroup.MinAvailable)
			}
		}
	}
}

// verifyPodCliqueStartupOrder combines pod fetching, filtering, count verification, and startup order verification.
func verifyPodCliqueStartupOrder(t *testing.T, tc *testctx.TestContext,
	beforeClique string, beforeCount int,
	afterClique string, afterCount int) {
	t.Helper()

	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to fetch pods: %v", err)
	}

	groupBefore := getPodsByCliquePattern(pods.Items, beforeClique)
	groupAfter := getPodsByCliquePattern(pods.Items, afterClique)

	if len(groupBefore) != beforeCount {
		t.Fatalf("Expected %d %s pods, got %d", beforeCount, beforeClique, len(groupBefore))
	}
	if len(groupAfter) != afterCount {
		t.Fatalf("Expected %d %s pods, got %d", afterCount, afterClique, len(groupAfter))
	}

	verifyGroupStartupOrder(t, groupBefore, groupAfter, beforeClique, afterClique)
}

// verifyGroupStartupOrderWithMinAvailable verifies startup ordering with minAvailable semantics.
func verifyGroupStartupOrderWithMinAvailable(t *testing.T, groupBefore, groupAfter []v1.Pod, beforeName, afterName string, minAvailable int) {
	t.Helper()

	if len(groupBefore) == 0 {
		t.Fatalf("Group %s has no pods", beforeName)
	}
	if len(groupAfter) == 0 {
		t.Fatalf("Group %s has no pods", afterName)
	}

	nthEarliestBefore := getNthEarliestPodTime(groupBefore, minAvailable)
	earliestAfter := getEarliestPodTime(groupAfter)

	if nthEarliestBefore.IsZero() {
		Logger.Errorf("Group %s doesn't have %d pods with valid Ready timestamps. Debugging pod states:", beforeName, minAvailable)
		for i, pod := range groupBefore {
			readyTime := getReadyConditionTransitionTime(pod)
			Logger.Errorf("  Pod[%d] %s: Phase=%s, ReadyTime=%v", i, pod.Name, pod.Status.Phase, readyTime)
		}
		t.Fatalf("Group %s doesn't have %d pods with valid Ready condition timestamps (pods may not be ready yet)", beforeName, minAvailable)
	}
	if earliestAfter.IsZero() {
		Logger.Errorf("Group %s has no pods with valid Ready timestamps. Debugging pod states:", afterName)
		for i, pod := range groupAfter {
			readyTime := getReadyConditionTransitionTime(pod)
			Logger.Errorf("  Pod[%d] %s: Phase=%s, ReadyTime=%v", i, pod.Name, pod.Status.Phase, readyTime)
		}
		t.Fatalf("Group %s has no pods with valid Ready condition timestamps (pods may not be ready yet)", afterName)
	}

	if earliestAfter.Before(nthEarliestBefore) {
		t.Fatalf("Startup order violation: group %s (earliest at %v) started before group %s had %d pods ready (pod #%d ready at %v)",
			afterName, earliestAfter, beforeName, minAvailable, minAvailable, nthEarliestBefore)
	}

	Logger.Debugf("Verified startup order: %s (%d pods ready by %v) -> %s (earliest: %v)",
		beforeName, minAvailable, nthEarliestBefore, afterName, earliestAfter)
}

// Helper function to verify that all pods in groupBefore started before all pods in groupAfter.
func verifyGroupStartupOrder(t *testing.T, groupBefore, groupAfter []v1.Pod, beforeName, afterName string) {
	verifyGroupStartupOrderWithMinAvailable(t, groupBefore, groupAfter, beforeName, afterName, len(groupBefore))
}

// Helper function to get pods by clique name pattern.
func getPodsByCliquePattern(pods []v1.Pod, pattern string) []v1.Pod {
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
func debugPodState(tc *testctx.TestContext) {
	pods, err := tc.ListPods()
	if err != nil {
		Logger.Errorf("Failed to list pods for debugging: %v", err)
		return
	}

	Logger.Infof("Debug: Found %d pods in namespace %s", len(pods.Items), tc.Namespace)
	for _, pod := range pods.Items {
		Logger.Infof("Pod %s: Phase=%s, Reason=%s, Message=%s", pod.Name, pod.Status.Phase, pod.Status.Reason, pod.Status.Message)
		for _, cond := range pod.Status.Conditions {
			if cond.Status != v1.ConditionTrue {
				Logger.Infof("  Condition %s=%s: %s", cond.Type, cond.Status, cond.Message)
			}
		}
		for _, status := range pod.Status.InitContainerStatuses {
			if !status.Ready {
				Logger.Infof("  InitContainer %s: Ready=%v, State=%+v", status.Name, status.Ready, status.State)
			}
		}
		for _, status := range pod.Status.ContainerStatuses {
			if !status.Ready {
				Logger.Infof("  Container %s: Ready=%v, State=%+v", status.Name, status.Ready, status.State)
			}
		}
		events, err := tc.Clients.Clientset.CoreV1().Events(tc.Namespace).List(tc.Ctx, metav1.ListOptions{
			FieldSelector: "involvedObject.name=" + pod.Name,
		})
		if err == nil {
			for _, e := range events.Items {
				if e.Type == "Warning" {
					Logger.Infof("  Event Warning: %s: %s", e.Reason, e.Message)
				}
			}
		}
	}
}

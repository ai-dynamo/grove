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
	"testing"

	"github.com/ai-dynamo/grove/operator/e2e/utils"
)

// Test_GS1_GangSchedulingWithFullReplicas tests gang-scheduling behavior with insufficient resources
// Scenario GS-1:
// 1. Initialize a 10-node Grove cluster, then cordon 1 node
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon the node and verify all pods get scheduled
func Test_GS1_GangSchedulingWithFullReplicas(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster, then cordon 1 node")
	// Setup test cluster with 10 worker nodes
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 10)
	defer cleanup()

	// Create test context with workload configuration
	expectedPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10
	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		RestConfig:    restConfig,
		DynamicClient: dynamicClient,
		Namespace:     "default",
		Timeout:       defaultPollTimeout,
		Interval:      defaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	// Setup and cordon nodes
	nodesToCordon := setupAndCordonNodes(tc, 1)
	workerNodeToCordon := nodesToCordon[0]
	logger.Debugf("🚫 Cordoned worker node: %s", workerNodeToCordon)

	logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	if err := verifyPodsArePendingWithUnschedulableEvents(tc, true, expectedPods); err != nil {
		t.Fatalf("Failed to verify all pods have Unschedulable events: %v", err)
	}

	logger.Info("4. Uncordon the node and verify all pods get scheduled")
	uncordonNodesAndWaitForPods(tc, []string{workerNodeToCordon}, expectedPods)

	// Verify that each pod is scheduled on a unique node, worker nodes have 150m memory
	// and workload pods requests 80m memory, so only 1 should fit per node
	listPodsAndAssertDistinctNodes(tc)

	logger.Info("🎉 Gang-scheduling With Full Replicas test completed successfully!")
}

// Test_GS2_GangSchedulingWithScalingFullReplicas verifies gang-scheduling behavior when scaling a PodCliqueScalingGroup
// Scenario GS-2:
// 1. Initialize a 14-node Grove cluster, then cordon 5 nodes
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon 1 node to allow scheduling and verify pods get scheduled
// 5. Wait for pods to become ready
// 6. Scale PCSG replicas to 3 and verify 4 new pending pods
// 7. Uncordon remaining nodes and verify all pods get scheduled
func Test_GS2_GangSchedulingWithScalingFullReplicas(t *testing.T) {
	ctx := context.Background()

	// Setup cluster (shared or individual based on test run mode)
	logger.Info("1. Initialize a 14-node Grove cluster, then cordon 5 nodes")

	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 14)
	defer cleanup()

	// Create test context
	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		RestConfig:    restConfig,
		DynamicClient: dynamicClient,
		Namespace:     "default",
		Timeout:       defaultPollTimeout,
		Interval:      defaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		},
	}

	// Setup and cordon nodes
	nodesToCordon := setupAndCordonNodes(tc, 5)

	logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	expectedPods := 10
	pods, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	if err := verifyPodsArePendingWithUnschedulableEvents(tc, true, expectedPods); err != nil {
		t.Fatalf("Failed to verify all pods have Unschedulable events: %v", err)
	}

	logger.Info("4. Uncordon 1 node to allow scheduling and verify pods get scheduled")
	logger.Info("5. Wait for pods to become ready")
	uncordonNodesAndWaitForPods(tc, nodesToCordon[:1], expectedPods)

	logger.Info("6. Scale PCSG replicas to 3 and verify 4 new pending pods")
	pcsgName := "workload1-0-sg-x"
	if err := scalePodCliqueScalingGroup(tc, pcsgName, 3); err != nil {
		t.Fatalf("Failed to scale PodCliqueScalingGroup %s: %v", pcsgName, err)
	}

	expectedScaledPods := 14
	pods, err = waitForPodCount(tc, expectedScaledPods)
	if err != nil {
		t.Fatalf("Failed to wait for scaled pods to be created: %v", err)
	}

	if err := utils.VerifyPodPhases(pods, expectedPods, 4); err != nil {
		t.Fatalf("Pod phase verification failed: %v", err)
	}

	logger.Info("7. Uncordon remaining nodes and verify all pods get scheduled")
	uncordonNodesAndWaitForPods(tc, nodesToCordon[1:], expectedScaledPods)

	// Verify that each pod is scheduled on a unique node
	listPodsAndAssertDistinctNodes(tc)

	logger.Info("🎉 Gang-scheduling PCSG scaling test completed successfully!")
}

// TestGangSchedulingWithPCSScalingFullReplicas verifies gang-scheduling behavior when scaling a PodCliqueSet
// Scenario GS-3:
// 1. Initialize a 20-node Grove cluster, then cordon 11 nodes
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon 1 node to allow scheduling and verify pods get scheduled
// 5. Wait for pods to become ready
// 6. Scale PCS replicas to 2 and verify 10 new pending pods
// 7. Uncordon remaining nodes and verify all pods get scheduled
func Test_GS3_GangSchedulingWithPCSScalingFullReplicas(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 20-node Grove cluster, then cordon 11 nodes")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 20)
	defer cleanup()

	// Create test context
	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		RestConfig:    restConfig,
		Namespace:     "default",
		Timeout:       defaultPollTimeout,
		Interval:      defaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		},
	}

	// Setup and cordon nodes
	nodesToCordon := setupAndCordonNodes(tc, 11)

	logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	// workloadNamespace set via tc.Namespace
	expectedPods := 10
	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	if err := verifyPodsArePendingWithUnschedulableEvents(tc, true, expectedPods); err != nil {
		t.Fatalf("Failed to verify all pods have Unschedulable events: %v", err)
	}

	logger.Info("4. Uncordon 1 node to allow scheduling and verify pods get scheduled")
	logger.Info("5. Wait for pods to become ready")
	uncordonNodesAndWaitForPods(tc, nodesToCordon[:1], expectedPods)

	logger.Info("6. Scale PCS replicas to 2 and verify 10 new pending pods")
	pcsName := "workload1"
	replicas := int32(2)
	expectedScaledPods := int(replicas) * expectedPods
	scalePCSAndWait(tc, pcsName, replicas, expectedScaledPods, expectedPods)

	expectedNewPending := expectedScaledPods - expectedPods
	if err := waitForPodCountAndPhases(tc, expectedScaledPods, expectedPods, expectedNewPending); err != nil {
		t.Fatalf("Failed to wait for scaled pods with expected phases: %v", err)
	}

	logger.Info("7. Uncordon remaining nodes and verify all pods get scheduled")
	uncordonNodesAndWaitForPods(tc, nodesToCordon[1:], expectedScaledPods)

	listPodsAndAssertDistinctNodes(tc)

	logger.Info("🎉 Gang-scheduling PCS scaling test completed successfully!")
}

// TestGangSchedulingWithPCSAndPCSGScalingFullReplicas verifies gang scheduling while scaling both PodCliqueSet and PodCliqueScalingGroup replicas
// Scenario GS-4:
// 1. Initialize a 28-node Grove cluster, then cordon 19 nodes
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon 1 node to allow scheduling and verify pods get scheduled
// 5. Wait for pods to become ready
// 6. Scale PCSG replicas to 3 and verify 4 new pending pods
// 7. Uncordon 4 nodes and verify scaled pods get scheduled
// 8. Scale PCS replicas to 2 and verify 10 new pending pods
// 9. Scale PCSG replicas to 3 and verify 4 new pending pods
// 10. Uncordon remaining nodes and verify all pods get scheduled
func Test_GS4_GangSchedulingWithPCSAndPCSGScalingFullReplicas(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster, then cordon 19 nodes")
	// Setup cluster (shared or individual based on test run mode)
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	// Create test context
	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		RestConfig:    restConfig,
		Namespace:     "default",
		Timeout:       defaultPollTimeout,
		Interval:      defaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		},
	}

	// Setup and cordon nodes
	nodesToCordon := setupAndCordonNodes(tc, 19)

	logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	// workloadNamespace set via tc.Namespace
	expectedPods := 10
	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	if err := verifyPodsArePendingWithUnschedulableEvents(tc, true, expectedPods); err != nil {
		t.Fatalf("Failed to verify all pods have Unschedulable events: %v", err)
	}

	logger.Info("4. Uncordon 1 node to allow scheduling and verify pods get scheduled")
	logger.Info("5. Wait for pods to become ready")
	uncordonNodesAndWaitForPods(tc, nodesToCordon[:1], expectedPods)

	logger.Info("6. Scale PCSG replicas to 3 and verify 4 new pending pods")
	pcsgName := "workload1-0-sg-x"
	scalePCSGInstanceAndWait(tc, pcsgName, 3, 14, 4)

	logger.Info("7. Uncordon 4 nodes and verify scaled pods get scheduled")
	uncordonNodesAndWaitForPods(tc, nodesToCordon[1:5], 14)

	logger.Info("8. Scale PCS replicas to 2 and verify 10 new pending pods")
	scalePCSAndWait(tc, "workload1", 2, 24, 10)
	uncordonNodesAndWaitForPods(tc, nodesToCordon[5:15], 24)

	logger.Info("9. Scale PCSG replicas to 3 and verify 4 new pending pods")
	secondReplicaPCSGName := "workload1-1-sg-x"
	scalePCSGInstanceAndWait(tc, secondReplicaPCSGName, 3, 28, 4)

	logger.Info("10. Uncordon remaining nodes and verify all pods get scheduled")
	uncordonNodesAndWaitForPods(tc, nodesToCordon[15:19], 28)

	listPodsAndAssertDistinctNodes(tc)

	logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
}

// Test_GS5_GangSchedulingWithMinReplicas tests gang-scheduling behavior with min-replicas
// Scenario GS-5:
// 1. Initialize a 10-node Grove cluster, then cordon 8 nodes
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})
// 5. Wait for scheduled pods to become ready
// 6. Uncordon 7 nodes and verify all remaining workload pods get scheduled
func Test_GS5_GangSchedulingWithMinReplicas(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster, then cordon 8 nodes")
	// Setup cluster (shared or individual based on test run mode)
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 10)
	defer cleanup()

	// Create test context
	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		RestConfig:    restConfig,
		DynamicClient: dynamicClient,
		Namespace:     "default",
		Timeout:       defaultPollTimeout,
		Interval:      defaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		},
	}

	// Setup and cordon nodes
	nodesToCordon := setupAndCordonNodes(tc, 8)

	logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	// workloadNamespace set via tc.Namespace
	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	verifyAllPodsArePendingWithSleep(tc)

	uncordonNodes(tc, nodesToCordon[:1])

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	if err := waitForPodPhases(tc, 3, 7); err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	logger.Info("5. Wait for scheduled pods to become ready")
	if err := waitForReadyPods(tc, 3); err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	logger.Info("6. Uncordon 7 nodes and verify all remaining workload pods get scheduled")
	uncordonNodes(tc, nodesToCordon[1:])

	// Wait for all remaining pods to be scheduled and ready
	if err := waitForPods(tc, 10); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all pods should be running and distributed across distinct nodes
	listPodsAndAssertDistinctNodes(tc)

	logger.Info("🎉 Gang-scheduling min-replicas test (GS-5) completed successfully!")
}

// Test_GS6_GangSchedulingWithPCSGScalingMinReplicas tests gang-scheduling behavior with PCSG scaling and min-replicas
// Scenario GS-6:
// 1. Initialize a 14-node Grove cluster, then cordon 12 nodes
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})
// 5. Wait for scheduled pods to become ready
// 6. Uncordon 7 nodes and verify the remaining workload pods get scheduled
// 7. Wait for scheduled pods to become ready
// 8. Set pcs-0-sg-x resource replicas equal to 3, then verify 4 newly created pods
// 9. Verify all newly created pods are pending due to insufficient resources
// 10. Uncordon 2 nodes and verify 2 more pods get scheduled (pcs-0-{sg-x-2-pc-b=1, sg-x-2-pc-c=1})
// 11. Wait for scheduled pods to become ready
// 12. Uncordon 2 nodes and verify remaining workload pods get scheduled
func Test_GS6_GangSchedulingWithPCSGScalingMinReplicas(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 14-node Grove cluster, then cordon 12 nodes")
	// Setup cluster (shared or individual based on test run mode)
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 14)
	defer cleanup()

	// Create test context
	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		RestConfig:    restConfig,
		Namespace:     "default",
		Timeout:       defaultPollTimeout,
		Interval:      defaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		},
	}

	// Setup and cordon nodes
	nodesToCordon := setupAndCordonNodes(tc, 12)

	logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	tc.Workload = &WorkloadConfig{
		Name:         "workload2",
		YAMLPath:     "../yaml/workload2.yaml",
		Namespace:    "default",
		ExpectedPods: 10,
	}
	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	verifyAllPodsArePendingWithSleep(tc)

	logger.Info("4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	// Based on workload2 min-replicas: pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1}
	uncordonNodes(tc, nodesToCordon[:1])

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	if err := waitForPodPhases(tc, 3, 7); err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	logger.Info("5. Wait for scheduled pods to become ready")
	if err := waitForReadyPods(tc, 3); err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	logger.Info("6. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	sevenNodesToUncordon := nodesToCordon[1:8]
	uncordonNodes(tc, sevenNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := waitForPods(tc, 10); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	logger.Info("8. Set pcs-0-sg-x resource replicas equal to 3, then verify 4 newly created pods")
	// Scale PCSG sg-x to 3 replicas and verify 4 newly created pods
	pcsgName := "workload2-0-sg-x"
	// Expected total pods after scaling: 10 (initial) + 4 (new from scaling sg-x from 2 to 3) = 14
	expectedPodsAfterScaling := 14
	expectedNewPendingPods := 4

	scalePCSGInstanceAndWait(tc, pcsgName, 3, expectedPodsAfterScaling, expectedNewPendingPods)

	logger.Info("9. Verify all newly created pods are pending due to insufficient resources")
	if err := verifyPodsArePendingWithUnschedulableEvents(tc, false, 4); err != nil {
		t.Fatalf("Failed to verify all pending pods have Unschedulable events: %v", err)
	}

	logger.Info("10. Uncordon 2 nodes and verify 2 more pods get scheduled (pcs-0-{sg-x-2-pc-b=1, sg-x-2-pc-c=1})")
	// Uncordon 2 nodes and verify exactly 2 more pods get scheduled
	// pcs-0-{sg-x-2-pc-b = 1, sg-x-2-pc-c = 1} (min-replicas for the new PCSG replica)
	twoNodesToUncordon := nodesToCordon[8:10]
	uncordonNodes(tc, twoNodesToUncordon)

	// Wait for exactly 2 more pods to be scheduled (min-replicas for new PCSG replica)
	if err := waitForPodPhases(tc, 12, 2); err != nil {
		t.Fatalf("Failed to wait for exactly 2 more pods to be scheduled after PCSG scaling: %v", err)
	}

	logger.Info("11. Wait for scheduled pods to become ready")
	if err := waitForReadyPods(tc, 12); err != nil {
		t.Fatalf("Failed to wait for 12 pods to become ready: %v", err)
	}

	logger.Info("12. Uncordon 2 nodes and verify remaining workload pods get scheduled")
	// Uncordon remaining 2 nodes and verify all remaining workload pods get scheduled
	remainingNodesToUncordon := nodesToCordon[10:12]
	uncordonNodes(tc, remainingNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := waitForPods(tc, 14); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 14 pods should be running and distributed across distinct nodes
	listPodsAndAssertDistinctNodes(tc)

	logger.Info("🎉 Gang-scheduling PCSG scaling min-replicas test (GS-6) completed successfully!")
}

// Test_GS7_GangSchedulingWithPCSGScalingMinReplicasAdvanced1 tests advanced gang-scheduling behavior with PCSG scaling and min-replicas
// Scenario GS-7:
// 1. Initialize a 14-node Grove cluster, then cordon 12 nodes
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})
// 5. Wait for scheduled pods to become ready
// 6. Uncordon 2 nodes and verify 2 more pods get scheduled (pcs-0-{sg-x-1-pc-b=1, sg-x-1-pc-c=1})
// 7. Wait for scheduled pods to become ready
// 8. Uncordon 5 nodes and verify the remaining workload pods get scheduled
// 9. Wait for scheduled pods to become ready
// 10. Set pcs-0-sg-x resource replicas equal to 3, then verify 4 newly created pods
// 11. Verify all newly created pods are pending due to insufficient resources
// 12. Uncordon 2 nodes and verify 2 more pods get scheduled (pcs-0-{sg-x-2-pc-b=1, sg-x-2-pc-c=1})
// 13. Wait for scheduled pods to become ready
// 14. Uncordon 2 nodes and verify remaining workload pods get scheduled
func Test_GS7_GangSchedulingWithPCSGScalingMinReplicasAdvanced1(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 14-node Grove cluster, then cordon 12 nodes")
	// Setup cluster (shared or individual based on test run mode)
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 14)
	defer cleanup()

	// Create test context
	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		RestConfig:    restConfig,
		Namespace:     "default",
		Timeout:       defaultPollTimeout,
		Interval:      defaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		},
	}

	// Setup and cordon nodes
	nodesToCordon := setupAndCordonNodes(tc, 12)

	logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	tc.Workload = &WorkloadConfig{
		Name:         "workload2",
		YAMLPath:     "../yaml/workload2.yaml",
		Namespace:    "default",
		ExpectedPods: 10,
	}
	pods, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	verifyAllPodsArePendingWithSleep(tc)

	logger.Info("4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	firstNodeToUncordon := nodesToCordon[0]
	if err := uncordonNode(tc, firstNodeToUncordon); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	if err := waitForPodPhases(tc, 3, len(pods.Items)-3); err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	logger.Info("5. Wait for scheduled pods to become ready")
	if err := waitForReadyPods(tc, 3); err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	logger.Info("6. Uncordon 2 nodes and verify 2 more pods get scheduled (pcs-0-{sg-x-1-pc-b=1, sg-x-1-pc-c=1})")
	twoNodesToUncordon := nodesToCordon[1:3]
	uncordonNodes(tc, twoNodesToUncordon)

	// Wait for exactly 2 more pods to be scheduled (sg-x-1 min-replicas)
	if err := waitForPodPhases(tc, 5, len(pods.Items)-5); err != nil {
		t.Fatalf("Failed to wait for exactly 2 more pods to be scheduled: %v", err)
	}

	logger.Info("7. Wait for scheduled pods to become ready")
	if err := waitForReadyPods(tc, 5); err != nil {
		t.Fatalf("Failed to wait for 5 scheduled pods to become ready: %v", err)
	}

	logger.Info("8. Uncordon 5 nodes and verify the remaining workload pods get scheduled")
	fiveNodesToUncordon := nodesToCordon[3:8]
	uncordonNodes(tc, fiveNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := waitForPods(tc, 10); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Verify all 10 initial pods are running
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	logger.Info("9. Wait for scheduled pods to become ready (already verified above)")
	logger.Info("11. Verify all newly created pods are pending due to insufficient resources (verified in scalePCSGInstanceAndWait)")
	logger.Info("10. Set pcs-0-sg-x resource replicas equal to 3, then verify 4 newly created pods")
	pcsgName := "workload2-0-sg-x"
	expectedPodsAfterScaling := 14
	expectedNewPendingPods := 4
	scalePCSGInstanceAndWait(tc, pcsgName, 3, expectedPodsAfterScaling, expectedNewPendingPods)

	logger.Info("12. Uncordon 2 nodes and verify 2 more pods get scheduled (pcs-0-{sg-x-2-pc-b=1, sg-x-2-pc-c=1})")
	twoMoreNodesToUncordon := nodesToCordon[8:10]
	uncordonNodes(tc, twoMoreNodesToUncordon)

	// Wait for exactly 2 more pods to be scheduled (min-replicas for new PCSG replica)
	if err := waitForPodPhases(tc, 12, 2); err != nil {
		t.Fatalf("Failed to wait for exactly 2 more pods to be scheduled after PCSG scaling: %v", err)
	}

	logger.Info("13. Wait for scheduled pods to become ready")
	if err := waitForReadyPods(tc, 12); err != nil {
		t.Fatalf("Failed to wait for 12 pods to become ready: %v", err)
	}

	logger.Info("14. Uncordon 2 nodes and verify remaining workload pods get scheduled")
	remainingNodesToUncordon := nodesToCordon[10:12]
	uncordonNodes(tc, remainingNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := waitForPods(tc, 14); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 14 pods should be running and distributed across distinct nodes
	listPodsAndAssertDistinctNodes(tc)

	logger.Info("🎉 Gang-scheduling PCSG scaling min-replicas advanced1 test (GS-7) completed successfully! All workload pods transitioned correctly through advanced PCSG scaling with min-replicas.")
}

// TestGangSchedulingWithPCSGScalingMinReplicasAdvanced2 tests advanced gang-scheduling behavior with early PCSG scaling and min-replicas
// Scenario GS-8:
// 1. Initialize a 14-node Grove cluster, then cordon 12 nodes
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Set pcs-0-sg-x resource replicas equal to 3, verify 4 more newly created pods
// 5. Verify all 14 newly created pods are pending due to insufficient resources
// 6. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})
// 7. Wait for scheduled pods to become ready
// 8. Uncordon 4 nodes and verify 4 more pods get scheduled (pcs-0-{sg-x-1-pc-b=1, sg-x-1-pc-c=1}, pcs-0-{sg-x-2-pc-b=1, sg-x-2-pc-c=1})
// 9. Wait for scheduled pods to become ready
// 10. Uncordon 7 nodes and verify the remaining workload pods get scheduled
func Test_GS8_GangSchedulingWithPCSGScalingMinReplicasAdvanced2(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 14-node Grove cluster, then cordon 12 nodes")
	// Setup cluster (shared or individual based on test run mode)
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 14)
	defer cleanup()

	// Create test context
	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		RestConfig:    restConfig,
		Namespace:     "default",
		Timeout:       defaultPollTimeout,
		Interval:      defaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		},
	}

	// Setup and cordon nodes
	nodesToCordon := setupAndCordonNodes(tc, 12)

	logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	tc.Workload = &WorkloadConfig{
		Name:         "workload2",
		YAMLPath:     "../yaml/workload2.yaml",
		Namespace:    "default",
		ExpectedPods: 10,
	}
	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	verifyAllPodsArePendingWithSleep(tc)

	logger.Info("4. Set pcs-0-sg-x resource replicas equal to 3, verify 4 more newly created pods")
	pcsgName := "workload2-0-sg-x"
	expectedPodsAfterScaling := 14
	scalePCSGInstanceAndWait(tc, pcsgName, 3, expectedPodsAfterScaling, expectedPodsAfterScaling)

	logger.Info("5. Verify all 14 newly created pods are pending due to insufficient resources")
	verifyAllPodsArePendingWithSleep(tc)

	logger.Info("6. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	firstNodeToUncordon := nodesToCordon[0]
	if err := uncordonNode(tc, firstNodeToUncordon); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	// expectedPodsAfterScaling is 14, so 14-3 = 11 pending
	if err := waitForPodPhases(tc, 3, 11); err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	logger.Info("7. Wait for scheduled pods to become ready")
	if err := waitForReadyPods(tc, 3); err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	logger.Info("8. Uncordon 4 nodes and verify 4 more pods get scheduled")
	fourNodesToUncordon := nodesToCordon[1:5]
	uncordonNodes(tc, fourNodesToUncordon)

	// Wait for exactly 4 more pods to be scheduled (sg-x-1 and sg-x-2 min-replicas)
	// Total is 14, so 14-7 = 7 pending
	if err := waitForPodPhases(tc, 7, 7); err != nil {
		t.Fatalf("Failed to wait for exactly 4 more pods to be scheduled: %v", err)
	}

	logger.Info("9. Wait for scheduled pods to become ready")
	if err := waitForReadyPods(tc, 7); err != nil {
		t.Fatalf("Failed to wait for 7 scheduled pods to become ready: %v", err)
	}

	logger.Info("10. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	remainingNodesToUncordon := nodesToCordon[5:]
	uncordonNodes(tc, remainingNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := waitForPods(tc, 14); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 14 pods should be running and distributed across distinct nodes
	listPodsAndAssertDistinctNodes(tc)

	logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
}

// TestGangSchedulingWithPCSScalingMinReplicas tests gang-scheduling behavior with PodCliqueSet scaling and min-replicas
// Scenario GS-9:
// 1. Initialize a 20-node Grove cluster, then cordon 18 nodes
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})
// 5. Wait for scheduled pods to become ready
// 6. Uncordon 7 nodes and verify the remaining workload pods get scheduled
// 7. Wait for scheduled pods to become ready
// 8. Set PCS resource replicas equal to 2, then verify 10 more newly created pods
// 9. Uncordon 3 nodes and verify another 3 pods get scheduled (pcs-1-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})
// 10. Wait for scheduled pods to become ready
// 11. Uncordon 7 nodes and verify the remaining workload pods get scheduled
func Test_GS9_GangSchedulingWithPCSScalingMinReplicas(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 20-node Grove cluster, then cordon 18 nodes")
	// Setup cluster (shared or individual based on test run mode)
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 20)
	defer cleanup()

	// Create test context
	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		RestConfig:    restConfig,
		Namespace:     "default",
		Timeout:       defaultPollTimeout,
		Interval:      defaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		},
	}

	// Setup and cordon nodes
	nodesToCordon := setupAndCordonNodes(tc, 18)

	logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	tc.Workload = &WorkloadConfig{
		Name:         "workload2",
		YAMLPath:     "../yaml/workload2.yaml",
		Namespace:    "default",
		ExpectedPods: 10,
	}
	pods, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	verifyAllPodsArePendingWithSleep(tc)

	logger.Info("4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	firstNodeToUncordon := nodesToCordon[0]
	if err := uncordonNode(tc, firstNodeToUncordon); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	if err := waitForPodPhases(tc, 3, len(pods.Items)-3); err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	logger.Info("5. Wait for scheduled pods to become ready")
	if err := waitForReadyPods(tc, 3); err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	logger.Info("6. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	logger.Info("7. Wait for scheduled pods to become ready")
	sevenNodesToUncordon := nodesToCordon[1:8]
	uncordonNodes(tc, sevenNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := waitForPods(tc, 10); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	logger.Info("8. Set PCS resource replicas equal to 2, then verify 10 more newly created pods")
	// Scale PodCliqueSet to 2 replicas and verify 10 more newly created pods
	pcsName := "workload2"

	// Expected total pods after scaling: 10 (initial) + 10 (new from scaling PCS from 1 to 2) = 20
	expectedPodsAfterScaling := 20
	expectedNewPendingPods := 10
	scalePCSAndWait(tc, pcsName, 2, expectedPodsAfterScaling, expectedNewPendingPods)

	logger.Info("9. Uncordon 3 nodes and verify another 3 pods get scheduled (pcs-1-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	threeNodesToUncordon := nodesToCordon[8:11]
	uncordonNodes(tc, threeNodesToUncordon)

	// Wait for exactly 3 more pods to be scheduled (min-replicas for new PCS replica)
	if err := waitForPodPhases(tc, 13, 7); err != nil {
		t.Fatalf("Failed to wait for exactly 3 more pods to be scheduled after PCS scaling: %v", err)
	}

	logger.Info("10. Wait for scheduled pods to become ready")
	if err := waitForReadyPods(tc, 13); err != nil {
		t.Fatalf("Failed to wait for 13 pods to become ready: %v", err)
	}

	logger.Info("11. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	remainingNodesToUncordon := nodesToCordon[11:18]
	uncordonNodes(tc, remainingNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := waitForPods(tc, 20); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 20 pods should be running and distributed across distinct nodes
	listPodsAndAssertDistinctNodes(tc)

	logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
}

// Test_GS10_GangSchedulingWithPCSScalingMinReplicasAdvanced tests advanced gang-scheduling behavior with early PCS scaling and min-replicas
// Scenario GS-10:
// 1. Initialize a 20-node Grove cluster, then cordon 18 nodes
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Set PCS resource replicas equal to 2, then verify 10 more newly created pods
// 5. Verify all 20 newly created pods are pending due to insufficient resources
// 6. Uncordon 4 nodes and verify a total of 6 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1}, pcs-1-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})
// 7. Wait for scheduled pods to become ready
// 8. Uncordon 4 nodes and verify 4 more pods get scheduled (pcs-0-{sg-x-1-pc-b=1, sg-x-1-pc-c=1}, pcs-1-{sg-x-1-pc-b=1, sg-x-1-pc-c=1})
// 9. Wait for scheduled pods to become ready
// 10. Uncordon 10 nodes and verify the remaining workload pods get scheduled
func Test_GS10_GangSchedulingWithPCSScalingMinReplicasAdvanced(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 20-node Grove cluster, then cordon 18 nodes")
	// Setup cluster (shared or individual based on test run mode)
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 20)
	defer cleanup()

	// Create test context
	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		RestConfig:    restConfig,
		Namespace:     "default",
		Timeout:       defaultPollTimeout,
		Interval:      defaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		},
	}

	// Setup and cordon nodes
	nodesToCordon := setupAndCordonNodes(tc, 18)

	logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	tc.Workload = &WorkloadConfig{
		Name:         "workload2",
		YAMLPath:     "../yaml/workload2.yaml",
		Namespace:    "default",
		ExpectedPods: 10,
	}
	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}
	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	// Need to use a sleep here unfortunately, see: https://github.com/NVIDIA/grove/issues/226
	verifyAllPodsArePendingWithSleep(tc)

	logger.Info("4. Set PCS resource replicas equal to 2, then verify 10 more newly created pods")
	pcsName := "workload2"

	// Expected total pods after scaling: 10 (initial) + 10 (new from scaling PCS from 1 to 2) = 20
	expectedPodsAfterScaling := 20
	scalePCSAndWait(tc, pcsName, 2, expectedPodsAfterScaling, expectedPodsAfterScaling)

	logger.Info("5. Verify all 20 newly created pods are pending due to insufficient resources")
	verifyAllPodsArePendingWithSleep(tc)

	logger.Info("6. Uncordon 4 nodes and verify a total of 6 pods get scheduled")
	fourNodesToUncordon := nodesToCordon[0:4]
	uncordonNodes(tc, fourNodesToUncordon)

	// Wait for exactly 6 pods to be scheduled (min-replicas for both PCS replicas)
	// expectedPodsAfterScaling is 20, so 20-6 = 14 pending
	if err := waitForPodPhases(tc, 6, 14); err != nil {
		t.Fatalf("Failed to wait for exactly 6 pods to be scheduled: %v", err)
	}

	logger.Info("7. Wait for scheduled pods to become ready")
	if err := waitForReadyPods(tc, 6); err != nil {
		t.Fatalf("Failed to wait for 6 scheduled pods to become ready: %v", err)
	}

	logger.Info("8. Uncordon 4 nodes and verify 4 more pods get scheduled")
	fourMoreNodesToUncordon := nodesToCordon[4:8]
	uncordonNodes(tc, fourMoreNodesToUncordon)

	// Wait for exactly 4 more pods to be scheduled (sg-x-1 for both PCS replicas)
	// Total is 20, so 20-10 = 10 pending
	if err := waitForPodPhases(tc, 10, 10); err != nil {
		t.Fatalf("Failed to wait for exactly 4 more pods to be scheduled: %v", err)
	}

	logger.Info("9. Wait for scheduled pods to become ready")
	if err := waitForReadyPods(tc, 10); err != nil {
		t.Fatalf("Failed to wait for 10 scheduled pods to become ready: %v", err)
	}

	logger.Info("10. Uncordon 10 nodes and verify the remaining workload pods get scheduled")
	remainingNodesToUncordon := nodesToCordon[8:18]
	uncordonNodes(tc, remainingNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := waitForPods(tc, 20); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 20 pods should be running and distributed across distinct nodes
	listPodsAndAssertDistinctNodes(tc)

	logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
}

// Test_GS11_GangSchedulingWithPCSAndPCSGScalingMinReplicas tests gang-scheduling behavior with both PCS and PCSG scaling using min-replicas
// Scenario GS-11:
// 1. Initialize a 28-node Grove cluster, then cordon 26 nodes
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon 1 node
// 5. Wait for min-replicas pods to be scheduled and ready (should be 3 pods for min-available)
// 6. Uncordon 7 nodes and verify the remaining workload pods get scheduled
// 7. Set pcs-0-sg-x resource replicas equal to 3, then verify 4 newly created pods
// 8. Verify all newly created pods are pending due to insufficient resources
// 9. Uncordon 2 nodes
// 10. Wait for 2 more pods to be scheduled and ready (min-available for sg-x-2)
// 11. Uncordon 2 nodes and verify remaining workload pods get scheduled
// 12. Set pcs resource replicas equal to 2, then verify 10 more newly created pods
// 13. Uncordon 3 nodes
// 14. Wait for 3 more pods to be scheduled (min-available for pcs-1)
// 15. Uncordon 7 nodes and verify the remaining workload pods get scheduled
// 16. Set pcs-1-sg-x resource replicas equal to 3, then verify 4 newly created pods
// 17. Verify all newly created pods are pending due to insufficient resources
// 18. Uncordon 2 nodes
// 19. Wait for 2 more pods to be scheduled (min-available for pcs-1-sg-x-2)
// 20. Uncordon 2 nodes and verify remaining workload pods get scheduled
func Test_GS11_GangSchedulingWithPCSAndPCSGScalingMinReplicas(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster, then cordon 26 nodes")
	// Setup cluster (shared or individual based on test run mode)
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	// Create test context
	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		RestConfig:    restConfig,
		Namespace:     "default",
		Timeout:       defaultPollTimeout,
		Interval:      defaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		},
	}

	// Setup and cordon nodes
	nodesToCordon := setupAndCordonNodes(tc, 26)

	logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	tc.Workload = &WorkloadConfig{
		Name:         "workload2",
		YAMLPath:     "../yaml/workload2.yaml",
		Namespace:    "default",
		ExpectedPods: 10,
	}
	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	verifyAllPodsArePendingWithSleep(tc)

	logger.Info("4. Uncordon 1 node")
	firstNodeToUncordon := nodesToCordon[0]
	if err := uncordonNode(tc, firstNodeToUncordon); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	logger.Info("5. Wait for min-replicas pods to be scheduled and ready (should be 3 pods for min-available)")
	if err := waitForRunningPods(tc, 3); err != nil {
		t.Fatalf("Failed to wait for min-replicas pods to be scheduled: %v", err)
	}

	logger.Info("6. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	remainingNodesFirstWave := nodesToCordon[1:8]
	uncordonNodes(tc, remainingNodesFirstWave)

	if err := waitForPods(tc, 10); err != nil {
		t.Fatalf("Failed to wait for first wave pods to be ready: %v", err)
	}

	logger.Info("7. Set pcs-0-sg-x resource replicas equal to 3, then verify 4 newly created pods")
	pcsgName := "workload2-0-sg-x"
	scalePCSGInstanceAndWait(tc, pcsgName, 3, 14, 4)

	logger.Info("8. Verify all newly created pods are pending due to insufficient resources")
	expectedRunning := 10 // Initial 10 pods from first wave
	expectedPending := 4  // 4 new pods from PCSG scaling
	if err := waitForPodCountAndPhases(tc, 14, expectedRunning, expectedPending); err != nil {
		t.Fatalf("Failed to verify newly created pods are pending: %v", err)
	}

	logger.Info("9. Uncordon 2 nodes")
	remainingNodesSecondWave := nodesToCordon[8:10]
	uncordonNodes(tc, remainingNodesSecondWave)

	logger.Info("10. Wait for 2 more pods to be scheduled and ready (min-available for sg-x-2)")
	if err := waitForRunningPods(tc, 12); err != nil {
		t.Fatalf("Failed to wait for PCSG partial scheduling: %v", err)
	}

	logger.Info("11. Uncordon 2 nodes and verify remaining workload pods get scheduled")
	remainingNodesThirdWave := nodesToCordon[10:12]
	uncordonNodes(tc, remainingNodesThirdWave)

	if err := waitForPods(tc, 14); err != nil {
		t.Fatalf("Failed to wait for PCSG completion pods to be ready: %v", err)
	}

	logger.Info("12. Set pcs resource replicas equal to 2, then verify 10 more newly created pods")
	scalePCSAndWait(tc, "workload2", 2, 24, 10)

	logger.Info("13. Uncordon 3 nodes")
	remainingNodesFourthWave := nodesToCordon[12:15]
	uncordonNodes(tc, remainingNodesFourthWave)

	logger.Info("14. Wait for 3 more pods to be scheduled (min-available for pcs-1)")
	if err := waitForRunningPods(tc, 17); err != nil {
		t.Fatalf("Failed to wait for PCS partial scheduling: %v", err)
	}

	logger.Info("15. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	remainingNodesFifthWave := nodesToCordon[15:22]
	uncordonNodes(tc, remainingNodesFifthWave)

	if err := waitForPods(tc, 24); err != nil {
		t.Fatalf("Failed to wait for PCS completion pods to be ready: %v", err)
	}

	logger.Info("16. Set pcs-1-sg-x resource replicas equal to 3, then verify 4 newly created pods")
	secondReplicaPCSGName := "workload2-1-sg-x"
	scalePCSGInstanceAndWait(tc, secondReplicaPCSGName, 3, 28, 4)

	logger.Info("17. Verify all newly created pods are pending due to insufficient resources")
	expectedRunning = 24 // All previous pods should be running
	expectedPending = 4  // 4 new pods from second PCSG scaling
	if err := waitForPodCountAndPhases(tc, 28, expectedRunning, expectedPending); err != nil {
		t.Fatalf("Failed to verify newly created pods are pending after second PCSG scaling: %v", err)
	}

	logger.Info("18. Uncordon 2 nodes")
	remainingNodesSixthWave := nodesToCordon[22:24]
	uncordonNodes(tc, remainingNodesSixthWave)

	logger.Info("19. Wait for 2 more pods to be scheduled (min-available for pcs-1-sg-x-2)")
	if err := waitForRunningPods(tc, 26); err != nil {
		t.Fatalf("Failed to wait for final PCSG partial scheduling: %v", err)
	}

	logger.Info("20. Uncordon 2 nodes and verify remaining workload pods get scheduled")
	finalNodes := nodesToCordon[24:26]
	uncordonNodes(tc, finalNodes)

	if err := waitForPods(tc, 28); err != nil {
		t.Fatalf("Failed to wait for all final pods to be ready: %v", err)
	}

	listPodsAndAssertDistinctNodes(tc)

	logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")

}

// Test_GS12_GangSchedulingWithComplexPCSGScaling tests gang-scheduling behavior with complex PCSG scaling operations
// Scenario GS-12:
// 1. Initialize a 28-node Grove cluster, then cordon 26 nodes
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Set pcs resource replicas equal to 2, then verify 10 more newly created pods
// 5. Verify all 20 newly created pods are pending due to insufficient resources
// 6. Set both pcs-0-sg-x and pcs-1-sg-x resource replicas equal to 3, verify 8 newly created pods
// 7. Verify all 28 created pods are pending due to insufficient resources
// 8. Uncordon 4 nodes and verify a total of 6 pods get scheduled (pcs-0 and pcs-1 min-available)
// 9. Wait for scheduled pods to become ready
// 10. Uncordon 8 nodes and verify 8 more pods get scheduled (remaining PCSG pods)
// 11. Wait for scheduled pods to become ready
// 12. Uncordon 14 nodes and verify the remaining workload pods get scheduled
func Test_GS12_GangSchedulingWithComplexPCSGScaling(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster, then cordon 26 nodes")
	// Setup cluster (shared or individual based on test run mode)
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	// Create test context
	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		RestConfig:    restConfig,
		Namespace:     "default",
		Timeout:       defaultPollTimeout,
		Interval:      defaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		},
	}

	// Setup and cordon nodes
	nodesToCordon := setupAndCordonNodes(tc, 26)

	logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	tc.Workload = &WorkloadConfig{
		Name:         "workload2",
		YAMLPath:     "../yaml/workload2.yaml",
		Namespace:    "default",
		ExpectedPods: 10,
	}
	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	verifyAllPodsArePendingWithSleep(tc)

	logger.Info("4. Set pcs resource replicas equal to 2, then verify 10 more newly created pods")
	scalePCSAndWait(tc, "workload2", 2, 20, 20)

	logger.Info("5. Verify all 20 newly created pods are pending due to insufficient resources")
	if err := waitForPodCountAndPhases(tc, 20, 0, 20); err != nil {
		t.Fatalf("Failed to verify all 20 pods are pending: %v", err)
	}

	logger.Info("6. Set both pcs-0-sg-x and pcs-1-sg-x resource replicas equal to 3, verify 8 newly created pods")

	pcsg1Name := "workload2-0-sg-x"
	scalePCSGInstanceAndWait(tc, pcsg1Name, 3, 24, 24)

	pcsg2Name := "workload2-1-sg-x"
	scalePCSGInstanceAndWait(tc, pcsg2Name, 3, 28, 28)

	logger.Info("7. Verify all 28 created pods are pending due to insufficient resources")
	if err := waitForPodCountAndPhases(tc, 28, 0, 28); err != nil {
		t.Fatalf("Failed to verify all 28 pods are pending: %v", err)
	}

	logger.Info("8. Uncordon 4 nodes and verify a total of 6 pods get scheduled (pcs-0 and pcs-1 min-available)")
	firstWaveNodes := nodesToCordon[:4]
	uncordonNodes(tc, firstWaveNodes)

	if err := waitForRunningPods(tc, 6); err != nil {
		t.Fatalf("Failed to wait for 6 pods to be scheduled: %v", err)
	}

	logger.Info("9. Wait for scheduled pods to become ready (only the 6 that are scheduled)")
	if err := waitForReadyPods(tc, 6); err != nil {
		t.Fatalf("Failed to wait for 6 pods to be ready: %v", err)
	}

	logger.Info("10. Uncordon 8 nodes and verify 8 more pods get scheduled (remaining PCSG pods)")
	secondWaveNodes := nodesToCordon[4:12]
	uncordonNodes(tc, secondWaveNodes)

	if err := waitForRunningPods(tc, 14); err != nil {
		t.Fatalf("Failed to wait for 8 more pods to be scheduled: %v", err)
	}

	logger.Info("11. Wait for scheduled pods to become ready (only the 14 that are scheduled)")
	if err := waitForReadyPods(tc, 14); err != nil {
		t.Fatalf("Failed to wait for 14 pods to be ready: %v", err)
	}

	logger.Info("12. Uncordon 14 nodes and verify the remaining workload pods get scheduled")
	finalWaveNodes := nodesToCordon[12:26]
	uncordonNodes(tc, finalWaveNodes)

	if err := waitForPods(tc, 28); err != nil {
		t.Fatalf("Failed to wait for all final pods to be ready: %v", err)
	}

	listPodsAndAssertDistinctNodes(tc)

	logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
}

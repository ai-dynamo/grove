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

	"github.com/ai-dynamo/grove/operator/e2e/testctx"
)

// Test_GS1_GangSchedulingWithFullReplicas tests gang-scheduling behavior with insufficient resources
// Scenario GS-1:
// 1. Initialize a 10-node Grove cluster, then cordon 1 node
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon the node and verify all pods get scheduled
func Test_GS1_GangSchedulingWithFullReplicas(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 10-node Grove cluster, then cordon 1 node")
	// Setup test cluster with 10 worker nodes
	expectedPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10
	tc, cleanup := testctx.PrepareTest(ctx, t, 10,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(1)
	workerNodeToCordon := nodesToCordon[0]
	Logger.Debugf("🚫 Cordoned worker node: %s", workerNodeToCordon)

	Logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	if err := tc.VerifyPodsArePendingWithUnschedulableEvents(true, expectedPods); err != nil {
		t.Fatalf("Failed to verify all pods have Unschedulable events: %v", err)
	}

	Logger.Info("4. Uncordon the node and verify all pods get scheduled")
	tc.UncordonNodesAndWaitForPods([]string{workerNodeToCordon}, expectedPods)

	// Verify that each pod is scheduled on a unique node, worker nodes have 150m memory
	// and workload pods requests 80m memory, so only 1 should fit per node
	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling With Full Replicas test completed successfully!")
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
	Logger.Info("1. Initialize a 14-node Grove cluster, then cordon 5 nodes")

	tc, cleanup := testctx.PrepareTest(ctx, t, 14,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(5)

	Logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	expectedPods := 10
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	if err := tc.VerifyPodsArePendingWithUnschedulableEvents(true, expectedPods); err != nil {
		t.Fatalf("Failed to verify all pods have Unschedulable events: %v", err)
	}

	Logger.Info("4. Uncordon 1 node to allow scheduling and verify pods get scheduled")
	Logger.Info("5. Wait for pods to become ready")
	tc.UncordonNodesAndWaitForPods(nodesToCordon[:1], expectedPods)

	Logger.Info("6. Scale PCSG replicas to 3 and verify 4 new pending pods")
	pcsgName := "workload1-0-sg-x"
	if err := tc.ScalePCSG(pcsgName, 3); err != nil {
		t.Fatalf("Failed to scale PodCliqueScalingGroup %s: %v", pcsgName, err)
	}

	expectedScaledPods := 14
	_, err = tc.WaitForPodCount(expectedScaledPods)
	if err != nil {
		t.Fatalf("Failed to wait for scaled pods to be created: %v", err)
	}

	if err := tc.WaitForPodCountAndPhases(expectedScaledPods, expectedPods, 4); err != nil {
		t.Fatalf("Pod phase verification failed: %v", err)
	}

	Logger.Info("7. Uncordon remaining nodes and verify all pods get scheduled")
	tc.UncordonNodesAndWaitForPods(nodesToCordon[1:], expectedScaledPods)

	// Verify that each pod is scheduled on a unique node
	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCSG scaling test completed successfully!")
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

	Logger.Info("1. Initialize a 20-node Grove cluster, then cordon 11 nodes")
	tc, cleanup := testctx.PrepareTest(ctx, t, 20,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(11)

	Logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	// workloadNamespace set via tc.Namespace
	expectedPods := 10
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	if err := tc.VerifyPodsArePendingWithUnschedulableEvents(true, expectedPods); err != nil {
		t.Fatalf("Failed to verify all pods have Unschedulable events: %v", err)
	}

	Logger.Info("4. Uncordon 1 node to allow scheduling and verify pods get scheduled")
	Logger.Info("5. Wait for pods to become ready")
	tc.UncordonNodesAndWaitForPods(nodesToCordon[:1], expectedPods)

	Logger.Info("6. Scale PCS replicas to 2 and verify 10 new pending pods")
	pcsName := "workload1"
	replicas := int32(2)
	expectedScaledPods := int(replicas) * expectedPods
	tc.ScalePCSAndWait(pcsName, replicas, expectedScaledPods, expectedPods)

	expectedNewPending := expectedScaledPods - expectedPods
	if err := tc.WaitForPodCountAndPhases(expectedScaledPods, expectedPods, expectedNewPending); err != nil {
		t.Fatalf("Failed to wait for scaled pods with expected phases: %v", err)
	}

	Logger.Info("7. Uncordon remaining nodes and verify all pods get scheduled")
	tc.UncordonNodesAndWaitForPods(nodesToCordon[1:], expectedScaledPods)

	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCS scaling test completed successfully!")
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

	Logger.Info("1. Initialize a 28-node Grove cluster, then cordon 19 nodes")
	// Setup cluster (shared or individual based on test run mode)
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(19)

	Logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	// workloadNamespace set via tc.Namespace
	expectedPods := 10
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	if err := tc.VerifyPodsArePendingWithUnschedulableEvents(true, expectedPods); err != nil {
		t.Fatalf("Failed to verify all pods have Unschedulable events: %v", err)
	}

	Logger.Info("4. Uncordon 1 node to allow scheduling and verify pods get scheduled")
	Logger.Info("5. Wait for pods to become ready")
	tc.UncordonNodesAndWaitForPods(nodesToCordon[:1], expectedPods)

	Logger.Info("6. Scale PCSG replicas to 3 and verify 4 new pending pods")
	pcsgName := "workload1-0-sg-x"
	tc.ScalePCSGInstanceAndWait(pcsgName, 3, 14, 4)

	Logger.Info("7. Uncordon 4 nodes and verify scaled pods get scheduled")
	tc.UncordonNodesAndWaitForPods(nodesToCordon[1:5], 14)

	Logger.Info("8. Scale PCS replicas to 2 and verify 10 new pending pods")
	tc.ScalePCSAndWait("workload1", 2, 24, 10)
	tc.UncordonNodesAndWaitForPods(nodesToCordon[5:15], 24)

	Logger.Info("9. Scale PCSG replicas to 3 and verify 4 new pending pods")
	secondReplicaPCSGName := "workload1-1-sg-x"
	tc.ScalePCSGInstanceAndWait(secondReplicaPCSGName, 3, 28, 4)

	Logger.Info("10. Uncordon remaining nodes and verify all pods get scheduled")
	tc.UncordonNodesAndWaitForPods(nodesToCordon[15:19], 28)

	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
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

	Logger.Info("1. Initialize a 10-node Grove cluster, then cordon 8 nodes")
	// Setup cluster (shared or individual based on test run mode)
	tc, cleanup := testctx.PrepareTest(ctx, t, 10,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(8)

	Logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	// workloadNamespace set via tc.Namespace
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	tc.VerifyAllPodsArePendingWithSleep()

	tc.UncordonNodes(nodesToCordon[:1])

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	if err := tc.WaitForPodPhases(3, 7); err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	Logger.Info("5. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(3); err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	Logger.Info("6. Uncordon 7 nodes and verify all remaining workload pods get scheduled")
	tc.UncordonNodes(nodesToCordon[1:])

	// Wait for all remaining pods to be scheduled and ready
	if err := tc.WaitForPods(10); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all pods should be running and distributed across distinct nodes
	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling min-replicas test (GS-5) completed successfully!")
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

	Logger.Info("1. Initialize a 14-node Grove cluster, then cordon 12 nodes")
	// Setup cluster (shared or individual based on test run mode)
	tc, cleanup := testctx.PrepareTest(ctx, t, 14,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(12)

	Logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	tc.VerifyAllPodsArePendingWithSleep()

	Logger.Info("4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	// Based on workload2 min-replicas: pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1}
	tc.UncordonNodes(nodesToCordon[:1])

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	if err := tc.WaitForPodPhases(3, 7); err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	Logger.Info("5. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(3); err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	Logger.Info("6. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	sevenNodesToUncordon := nodesToCordon[1:8]
	tc.UncordonNodes(sevenNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := tc.WaitForPods(10); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	Logger.Info("8. Set pcs-0-sg-x resource replicas equal to 3, then verify 4 newly created pods")
	// Scale PCSG sg-x to 3 replicas and verify 4 newly created pods
	pcsgName := "workload2-0-sg-x"
	// Expected total pods after scaling: 10 (initial) + 4 (new from scaling sg-x from 2 to 3) = 14
	expectedPodsAfterScaling := 14
	expectedNewPendingPods := 4

	tc.ScalePCSGInstanceAndWait(pcsgName, 3, expectedPodsAfterScaling, expectedNewPendingPods)

	Logger.Info("9. Verify all newly created pods are pending due to insufficient resources")
	if err := tc.VerifyPodsArePendingWithUnschedulableEvents(false, 4); err != nil {
		t.Fatalf("Failed to verify all pending pods have Unschedulable events: %v", err)
	}

	Logger.Info("10. Uncordon 2 nodes and verify 2 more pods get scheduled (pcs-0-{sg-x-2-pc-b=1, sg-x-2-pc-c=1})")
	// Uncordon 2 nodes and verify exactly 2 more pods get scheduled
	// pcs-0-{sg-x-2-pc-b = 1, sg-x-2-pc-c = 1} (min-replicas for the new PCSG replica)
	twoNodesToUncordon := nodesToCordon[8:10]
	tc.UncordonNodes(twoNodesToUncordon)

	// Wait for exactly 2 more pods to be scheduled (min-replicas for new PCSG replica)
	if err := tc.WaitForPodPhases(12, 2); err != nil {
		t.Fatalf("Failed to wait for exactly 2 more pods to be scheduled after PCSG scaling: %v", err)
	}

	Logger.Info("11. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(12); err != nil {
		t.Fatalf("Failed to wait for 12 pods to become ready: %v", err)
	}

	Logger.Info("12. Uncordon 2 nodes and verify remaining workload pods get scheduled")
	// Uncordon remaining 2 nodes and verify all remaining workload pods get scheduled
	remainingNodesToUncordon := nodesToCordon[10:12]
	tc.UncordonNodes(remainingNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := tc.WaitForPods(14); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 14 pods should be running and distributed across distinct nodes
	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCSG scaling min-replicas test (GS-6) completed successfully!")
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

	Logger.Info("1. Initialize a 14-node Grove cluster, then cordon 12 nodes")
	// Setup cluster (shared or individual based on test run mode)
	tc, cleanup := testctx.PrepareTest(ctx, t, 14,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(12)

	Logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	pods, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	tc.VerifyAllPodsArePendingWithSleep()

	Logger.Info("4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	firstNodeToUncordon := nodesToCordon[0]
	if err := tc.UncordonNode(firstNodeToUncordon); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	if err := tc.WaitForPodPhases(3, len(pods.Items)-3); err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	Logger.Info("5. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(3); err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	Logger.Info("6. Uncordon 2 nodes and verify 2 more pods get scheduled (pcs-0-{sg-x-1-pc-b=1, sg-x-1-pc-c=1})")
	twoNodesToUncordon := nodesToCordon[1:3]
	tc.UncordonNodes(twoNodesToUncordon)

	// Wait for exactly 2 more pods to be scheduled (sg-x-1 min-replicas)
	if err := tc.WaitForPodPhases(5, len(pods.Items)-5); err != nil {
		t.Fatalf("Failed to wait for exactly 2 more pods to be scheduled: %v", err)
	}

	Logger.Info("7. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(5); err != nil {
		t.Fatalf("Failed to wait for 5 scheduled pods to become ready: %v", err)
	}

	Logger.Info("8. Uncordon 5 nodes and verify the remaining workload pods get scheduled")
	fiveNodesToUncordon := nodesToCordon[3:8]
	tc.UncordonNodes(fiveNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := tc.WaitForPods(10); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Verify all 10 initial pods are running
	pods, err = tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	Logger.Info("9. Wait for scheduled pods to become ready (already verified above)")
	Logger.Info("11. Verify all newly created pods are pending due to insufficient resources (verified in scalePCSGInstanceAndWait)")
	Logger.Info("10. Set pcs-0-sg-x resource replicas equal to 3, then verify 4 newly created pods")
	pcsgName := "workload2-0-sg-x"
	expectedPodsAfterScaling := 14
	expectedNewPendingPods := 4
	tc.ScalePCSGInstanceAndWait(pcsgName, 3, expectedPodsAfterScaling, expectedNewPendingPods)

	Logger.Info("12. Uncordon 2 nodes and verify 2 more pods get scheduled (pcs-0-{sg-x-2-pc-b=1, sg-x-2-pc-c=1})")
	twoMoreNodesToUncordon := nodesToCordon[8:10]
	tc.UncordonNodes(twoMoreNodesToUncordon)

	// Wait for exactly 2 more pods to be scheduled (min-replicas for new PCSG replica)
	if err := tc.WaitForPodPhases(12, 2); err != nil {
		t.Fatalf("Failed to wait for exactly 2 more pods to be scheduled after PCSG scaling: %v", err)
	}

	Logger.Info("13. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(12); err != nil {
		t.Fatalf("Failed to wait for 12 pods to become ready: %v", err)
	}

	Logger.Info("14. Uncordon 2 nodes and verify remaining workload pods get scheduled")
	remainingNodesToUncordon := nodesToCordon[10:12]
	tc.UncordonNodes(remainingNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := tc.WaitForPods(14); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 14 pods should be running and distributed across distinct nodes
	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCSG scaling min-replicas advanced1 test (GS-7) completed successfully! All workload pods transitioned correctly through advanced PCSG scaling with min-replicas.")
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

	Logger.Info("1. Initialize a 14-node Grove cluster, then cordon 12 nodes")
	// Setup cluster (shared or individual based on test run mode)
	tc, cleanup := testctx.PrepareTest(ctx, t, 14,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(12)

	Logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	tc.VerifyAllPodsArePendingWithSleep()

	Logger.Info("4. Set pcs-0-sg-x resource replicas equal to 3, verify 4 more newly created pods")
	pcsgName := "workload2-0-sg-x"
	expectedPodsAfterScaling := 14
	tc.ScalePCSGInstanceAndWait(pcsgName, 3, expectedPodsAfterScaling, expectedPodsAfterScaling)

	Logger.Info("5. Verify all 14 newly created pods are pending due to insufficient resources")
	tc.VerifyAllPodsArePendingWithSleep()

	Logger.Info("6. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	firstNodeToUncordon := nodesToCordon[0]
	if err := tc.UncordonNode(firstNodeToUncordon); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	// expectedPodsAfterScaling is 14, so 14-3 = 11 pending
	if err := tc.WaitForPodPhases(3, 11); err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	Logger.Info("7. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(3); err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	Logger.Info("8. Uncordon 4 nodes and verify 4 more pods get scheduled")
	fourNodesToUncordon := nodesToCordon[1:5]
	tc.UncordonNodes(fourNodesToUncordon)

	// Wait for exactly 4 more pods to be scheduled (sg-x-1 and sg-x-2 min-replicas)
	// Total is 14, so 14-7 = 7 pending
	if err := tc.WaitForPodPhases(7, 7); err != nil {
		t.Fatalf("Failed to wait for exactly 4 more pods to be scheduled: %v", err)
	}

	Logger.Info("9. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(7); err != nil {
		t.Fatalf("Failed to wait for 7 scheduled pods to become ready: %v", err)
	}

	Logger.Info("10. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	remainingNodesToUncordon := nodesToCordon[5:]
	tc.UncordonNodes(remainingNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := tc.WaitForPods(14); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 14 pods should be running and distributed across distinct nodes
	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
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

	Logger.Info("1. Initialize a 20-node Grove cluster, then cordon 18 nodes")
	// Setup cluster (shared or individual based on test run mode)
	tc, cleanup := testctx.PrepareTest(ctx, t, 20,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(18)

	Logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	pods, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	tc.VerifyAllPodsArePendingWithSleep()

	Logger.Info("4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	firstNodeToUncordon := nodesToCordon[0]
	if err := tc.UncordonNode(firstNodeToUncordon); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	if err := tc.WaitForPodPhases(3, len(pods.Items)-3); err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	Logger.Info("5. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(3); err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	Logger.Info("6. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	Logger.Info("7. Wait for scheduled pods to become ready")
	sevenNodesToUncordon := nodesToCordon[1:8]
	tc.UncordonNodes(sevenNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := tc.WaitForPods(10); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	Logger.Info("8. Set PCS resource replicas equal to 2, then verify 10 more newly created pods")
	// Scale PodCliqueSet to 2 replicas and verify 10 more newly created pods
	pcsName := "workload2"

	// Expected total pods after scaling: 10 (initial) + 10 (new from scaling PCS from 1 to 2) = 20
	expectedPodsAfterScaling := 20
	expectedNewPendingPods := 10
	tc.ScalePCSAndWait(pcsName, 2, expectedPodsAfterScaling, expectedNewPendingPods)

	Logger.Info("9. Uncordon 3 nodes and verify another 3 pods get scheduled (pcs-1-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	threeNodesToUncordon := nodesToCordon[8:11]
	tc.UncordonNodes(threeNodesToUncordon)

	// Wait for exactly 3 more pods to be scheduled (min-replicas for new PCS replica)
	if err := tc.WaitForPodPhases(13, 7); err != nil {
		t.Fatalf("Failed to wait for exactly 3 more pods to be scheduled after PCS scaling: %v", err)
	}

	Logger.Info("10. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(13); err != nil {
		t.Fatalf("Failed to wait for 13 pods to become ready: %v", err)
	}

	Logger.Info("11. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	remainingNodesToUncordon := nodesToCordon[11:18]
	tc.UncordonNodes(remainingNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := tc.WaitForPods(20); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 20 pods should be running and distributed across distinct nodes
	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
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

	Logger.Info("1. Initialize a 20-node Grove cluster, then cordon 18 nodes")
	// Setup cluster (shared or individual based on test run mode)
	tc, cleanup := testctx.PrepareTest(ctx, t, 20,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(18)

	Logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}
	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	// Need to use a sleep here unfortunately, see: https://github.com/NVIDIA/grove/issues/226
	tc.VerifyAllPodsArePendingWithSleep()

	Logger.Info("4. Set PCS resource replicas equal to 2, then verify 10 more newly created pods")
	pcsName := "workload2"

	// Expected total pods after scaling: 10 (initial) + 10 (new from scaling PCS from 1 to 2) = 20
	expectedPodsAfterScaling := 20
	tc.ScalePCSAndWait(pcsName, 2, expectedPodsAfterScaling, expectedPodsAfterScaling)

	Logger.Info("5. Verify all 20 newly created pods are pending due to insufficient resources")
	tc.VerifyAllPodsArePendingWithSleep()

	Logger.Info("6. Uncordon 4 nodes and verify a total of 6 pods get scheduled")
	fourNodesToUncordon := nodesToCordon[0:4]
	tc.UncordonNodes(fourNodesToUncordon)

	// Wait for exactly 6 pods to be scheduled (min-replicas for both PCS replicas)
	// expectedPodsAfterScaling is 20, so 20-6 = 14 pending
	if err := tc.WaitForPodPhases(6, 14); err != nil {
		t.Fatalf("Failed to wait for exactly 6 pods to be scheduled: %v", err)
	}

	Logger.Info("7. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(6); err != nil {
		t.Fatalf("Failed to wait for 6 scheduled pods to become ready: %v", err)
	}

	Logger.Info("8. Uncordon 4 nodes and verify 4 more pods get scheduled")
	fourMoreNodesToUncordon := nodesToCordon[4:8]
	tc.UncordonNodes(fourMoreNodesToUncordon)

	// Wait for exactly 4 more pods to be scheduled (sg-x-1 for both PCS replicas)
	// Total is 20, so 20-10 = 10 pending
	if err := tc.WaitForPodPhases(10, 10); err != nil {
		t.Fatalf("Failed to wait for exactly 4 more pods to be scheduled: %v", err)
	}

	Logger.Info("9. Wait for scheduled pods to become ready")
	if err := tc.WaitForReadyPods(10); err != nil {
		t.Fatalf("Failed to wait for 10 scheduled pods to become ready: %v", err)
	}

	Logger.Info("10. Uncordon 10 nodes and verify the remaining workload pods get scheduled")
	remainingNodesToUncordon := nodesToCordon[8:18]
	tc.UncordonNodes(remainingNodesToUncordon)

	// Wait for all remaining pods to be scheduled and ready
	if err := tc.WaitForPods(20); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 20 pods should be running and distributed across distinct nodes
	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
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

	Logger.Info("1. Initialize a 28-node Grove cluster, then cordon 26 nodes")
	// Setup cluster (shared or individual based on test run mode)
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(26)

	Logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	tc.VerifyAllPodsArePendingWithSleep()

	Logger.Info("4. Uncordon 1 node")
	firstNodeToUncordon := nodesToCordon[0]
	if err := tc.UncordonNode(firstNodeToUncordon); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	Logger.Info("5. Wait for min-replicas pods to be scheduled and ready (should be 3 pods for min-available)")
	if err := tc.WaitForRunningPods(3); err != nil {
		t.Fatalf("Failed to wait for min-replicas pods to be scheduled: %v", err)
	}

	Logger.Info("6. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	remainingNodesFirstWave := nodesToCordon[1:8]
	tc.UncordonNodes(remainingNodesFirstWave)

	if err := tc.WaitForPods(10); err != nil {
		t.Fatalf("Failed to wait for first wave pods to be ready: %v", err)
	}

	Logger.Info("7. Set pcs-0-sg-x resource replicas equal to 3, then verify 4 newly created pods")
	pcsgName := "workload2-0-sg-x"
	tc.ScalePCSGInstanceAndWait(pcsgName, 3, 14, 4)

	Logger.Info("8. Verify all newly created pods are pending due to insufficient resources")
	expectedRunning := 10 // Initial 10 pods from first wave
	expectedPending := 4  // 4 new pods from PCSG scaling
	if err := tc.WaitForPodCountAndPhases(14, expectedRunning, expectedPending); err != nil {
		t.Fatalf("Failed to verify newly created pods are pending: %v", err)
	}

	Logger.Info("9. Uncordon 2 nodes")
	remainingNodesSecondWave := nodesToCordon[8:10]
	tc.UncordonNodes(remainingNodesSecondWave)

	Logger.Info("10. Wait for 2 more pods to be scheduled and ready (min-available for sg-x-2)")
	if err := tc.WaitForRunningPods(12); err != nil {
		t.Fatalf("Failed to wait for PCSG partial scheduling: %v", err)
	}

	Logger.Info("11. Uncordon 2 nodes and verify remaining workload pods get scheduled")
	remainingNodesThirdWave := nodesToCordon[10:12]
	tc.UncordonNodes(remainingNodesThirdWave)

	if err := tc.WaitForPods(14); err != nil {
		t.Fatalf("Failed to wait for PCSG completion pods to be ready: %v", err)
	}

	Logger.Info("12. Set pcs resource replicas equal to 2, then verify 10 more newly created pods")
	tc.ScalePCSAndWait("workload2", 2, 24, 10)

	Logger.Info("13. Uncordon 3 nodes")
	remainingNodesFourthWave := nodesToCordon[12:15]
	tc.UncordonNodes(remainingNodesFourthWave)

	Logger.Info("14. Wait for 3 more pods to be scheduled (min-available for pcs-1)")
	if err := tc.WaitForRunningPods(17); err != nil {
		t.Fatalf("Failed to wait for PCS partial scheduling: %v", err)
	}

	Logger.Info("15. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	remainingNodesFifthWave := nodesToCordon[15:22]
	tc.UncordonNodes(remainingNodesFifthWave)

	if err := tc.WaitForPods(24); err != nil {
		t.Fatalf("Failed to wait for PCS completion pods to be ready: %v", err)
	}

	Logger.Info("16. Set pcs-1-sg-x resource replicas equal to 3, then verify 4 newly created pods")
	secondReplicaPCSGName := "workload2-1-sg-x"
	tc.ScalePCSGInstanceAndWait(secondReplicaPCSGName, 3, 28, 4)

	Logger.Info("17. Verify all newly created pods are pending due to insufficient resources")
	expectedRunning = 24 // All previous pods should be running
	expectedPending = 4  // 4 new pods from second PCSG scaling
	if err := tc.WaitForPodCountAndPhases(28, expectedRunning, expectedPending); err != nil {
		t.Fatalf("Failed to verify newly created pods are pending after second PCSG scaling: %v", err)
	}

	Logger.Info("18. Uncordon 2 nodes")
	remainingNodesSixthWave := nodesToCordon[22:24]
	tc.UncordonNodes(remainingNodesSixthWave)

	Logger.Info("19. Wait for 2 more pods to be scheduled (min-available for pcs-1-sg-x-2)")
	if err := tc.WaitForRunningPods(26); err != nil {
		t.Fatalf("Failed to wait for final PCSG partial scheduling: %v", err)
	}

	Logger.Info("20. Uncordon 2 nodes and verify remaining workload pods get scheduled")
	finalNodes := nodesToCordon[24:26]
	tc.UncordonNodes(finalNodes)

	if err := tc.WaitForPods(28); err != nil {
		t.Fatalf("Failed to wait for all final pods to be ready: %v", err)
	}

	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")

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

	Logger.Info("1. Initialize a 28-node Grove cluster, then cordon 26 nodes")
	// Setup cluster (shared or individual based on test run mode)
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload2",
			YAMLPath:     "../yaml/workload2.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	// Setup and cordon nodes
	nodesToCordon := tc.SetupAndCordonNodes(26)

	Logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	tc.VerifyAllPodsArePendingWithSleep()

	Logger.Info("4. Set pcs resource replicas equal to 2, then verify 10 more newly created pods")
	tc.ScalePCSAndWait("workload2", 2, 20, 20)

	Logger.Info("5. Verify all 20 newly created pods are pending due to insufficient resources")
	if err := tc.WaitForPodCountAndPhases(20, 0, 20); err != nil {
		t.Fatalf("Failed to verify all 20 pods are pending: %v", err)
	}

	Logger.Info("6. Set both pcs-0-sg-x and pcs-1-sg-x resource replicas equal to 3, verify 8 newly created pods")

	pcsg1Name := "workload2-0-sg-x"
	tc.ScalePCSGInstanceAndWait(pcsg1Name, 3, 24, 24)

	pcsg2Name := "workload2-1-sg-x"
	tc.ScalePCSGInstanceAndWait(pcsg2Name, 3, 28, 28)

	Logger.Info("7. Verify all 28 created pods are pending due to insufficient resources")
	if err := tc.WaitForPodCountAndPhases(28, 0, 28); err != nil {
		t.Fatalf("Failed to verify all 28 pods are pending: %v", err)
	}

	Logger.Info("8. Uncordon 4 nodes and verify a total of 6 pods get scheduled (pcs-0 and pcs-1 min-available)")
	firstWaveNodes := nodesToCordon[:4]
	tc.UncordonNodes(firstWaveNodes)

	if err := tc.WaitForRunningPods(6); err != nil {
		t.Fatalf("Failed to wait for 6 pods to be scheduled: %v", err)
	}

	Logger.Info("9. Wait for scheduled pods to become ready (only the 6 that are scheduled)")
	if err := tc.WaitForReadyPods(6); err != nil {
		t.Fatalf("Failed to wait for 6 pods to be ready: %v", err)
	}

	Logger.Info("10. Uncordon 8 nodes and verify 8 more pods get scheduled (remaining PCSG pods)")
	secondWaveNodes := nodesToCordon[4:12]
	tc.UncordonNodes(secondWaveNodes)

	if err := tc.WaitForRunningPods(14); err != nil {
		t.Fatalf("Failed to wait for 8 more pods to be scheduled: %v", err)
	}

	Logger.Info("11. Wait for scheduled pods to become ready (only the 14 that are scheduled)")
	if err := tc.WaitForReadyPods(14); err != nil {
		t.Fatalf("Failed to wait for 14 pods to be ready: %v", err)
	}

	Logger.Info("12. Uncordon 14 nodes and verify the remaining workload pods get scheduled")
	finalWaveNodes := nodesToCordon[12:26]
	tc.UncordonNodes(finalWaveNodes)

	if err := tc.WaitForPods(28); err != nil {
		t.Fatalf("Failed to wait for all final pods to be ready: %v", err)
	}

	tc.ListPodsAndAssertDistinctNodes()

	Logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
}

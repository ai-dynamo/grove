//go:build e2e

package tests

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

import (
	"context"
	"fmt"
	"testing"

	kaischedulingv2alpha2 "github.com/NVIDIA/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	nameutils "github.com/ai-dynamo/grove/operator/api/common"
	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/utils"
	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// DeployWorkloadAndGetPods deploys workload, waits for pods to be ready, and returns the pod list
func DeployWorkloadAndGetPods(tc TestContext, expectedPods int) ([]v1.Pod, error) {
	if _, err := deployAndVerifyWorkload(tc); err != nil {
		return nil, fmt.Errorf("failed to deploy workload: %w", err)
	}

	logger.Info("Wait for all pods to be scheduled and running")
	if err := utils.WaitForPods(tc.Ctx, tc.RestConfig, []string{tc.Namespace}, tc.getLabelSelector(), expectedPods, tc.Timeout, tc.Interval, logger); err != nil {
		return nil, fmt.Errorf("failed to wait for pods ready: %w", err)
	}

	logger.Info("Get all pods once for verification")
	podList, err := listPods(tc)
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	return podList.Items, nil
}

// createTopologyTestContext creates a TestContext for topology tests with standard configuration
func createTopologyTestContext(
	t *testing.T,
	ctx context.Context,
	clientset *kubernetes.Clientset,
	restConfig *rest.Config,
	dynamicClient dynamic.Interface,
	workloadName, yamlPath string,
	expectedPods int,
) TestContext {
	return TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		RestConfig:    restConfig,
		DynamicClient: dynamicClient,
		Namespace:     "default",
		Timeout:       defaultPollTimeout,
		Interval:      defaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         workloadName,
			YAMLPath:     yamlPath,
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}
}

// GetPodGroupOrFail retrieves a PodGroup for the specified PCS replica or fails the test.
func GetPodGroupOrFail(t *testing.T, tc TestContext, pcsReplica int) *kaischedulingv2alpha2.PodGroup {
	podGroup, err := utils.GetPodGroupForBasePodGangReplica(
		tc.Ctx, tc.DynamicClient, tc.Namespace, tc.Workload.Name,
		pcsReplica, tc.Timeout, tc.Interval, logger,
	)
	if err != nil {
		t.Fatalf("Failed to get PodGroup for replica %d: %v", pcsReplica, err)
	}
	return podGroup
}

// Test_TAS1_TopologyInfrastructure verifies that the operator creates ClusterTopology and KAI Topology CRs at startup
// 1. Verify ClusterTopology CR exists with the correct 4-level hierarchy (zone, block, rack, host)
// 2. Verify KAI Topology CR exists with matching levels
// 3. Verify KAI Topology has owner reference to ClusterTopology
// 4. Verify worker nodes have topology labels
func Test_TAS1_TopologyInfrastructure(t *testing.T) {
	ctx := context.Background()

	clientset, _, dynamicClient, cleanup := prepareTestCluster(ctx, t, 0)
	defer cleanup()

	logger.Info("1. Verify ClusterTopology CR exists with correct 4-level hierarchy")

	expectedLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: setup.TopologyLabelZone},
		{Domain: corev1alpha1.TopologyDomainBlock, Key: setup.TopologyLabelBlock},
		{Domain: corev1alpha1.TopologyDomainRack, Key: setup.TopologyLabelRack},
		{Domain: corev1alpha1.TopologyDomainHost, Key: setup.TopologyLabelHostname},
	}

	if err := utils.VerifyClusterTopologyLevels(ctx, dynamicClient, corev1alpha1.DefaultClusterTopologyName, expectedLevels, logger); err != nil {
		t.Fatalf("Failed to verify ClusterTopology levels: %v", err)
	}

	logger.Info("2. Verify KAI Topology CR exists with matching levels and owner reference")

	expectedKeys := []string{
		setup.TopologyLabelZone,
		setup.TopologyLabelBlock,
		setup.TopologyLabelRack,
		setup.TopologyLabelHostname,
	}

	if err := utils.VerifyKAITopologyLevels(ctx, dynamicClient, corev1alpha1.DefaultClusterTopologyName, expectedKeys, logger); err != nil {
		t.Fatalf("Failed to verify KAI Topology levels: %v", err)
	}

	logger.Info("3. Verify worker nodes have topology labels")

	// Use label selector to get only worker nodes by role label
	workerLabelSelector := setup.GetWorkerNodeLabelSelector()
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: workerLabelSelector,
	})
	if err != nil {
		t.Fatalf("Failed to list nodes: %v", err)
	}

	// Reuse expectedKeys from step 2 (same topology label keys)
	workerCount := len(nodes.Items)
	for _, node := range nodes.Items {
		for _, key := range expectedKeys {
			if value, ok := node.Labels[key]; !ok || value == "" {
				t.Errorf("Node %s missing %s label", node.Name, key)
			}
		}
	}

	if workerCount == 0 {
		t.Fatal("No worker nodes found in cluster")
	}

	logger.Infof("Successfully verified topology labels on %d worker nodes", workerCount)
	logger.Info("🎉 Topology Infrastructure test completed successfully!")
}

// Test_TAS2_MultipleCliquesWithDifferentConstraints tests PCS with multiple cliques having different topology constraints
// 1. Deploy workload with PCS (no constraint) containing 2 cliques:
//   - worker-rack: packDomain=rack (3 pods)
//   - worker-block: packDomain=block (4 pods)
//
// 2. Verify all 7 pods are scheduled successfully
// 3. Verify worker-rack pods (3) are in the same rack
// 4. Verify worker-block pods (4) are in the same block
// 5. Verify different cliques can have independent topology constraints
func Test_TAS2_MultipleCliquesWithDifferentConstraints(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 7 // worker-rack: 3 pods, worker-block: 4 pods
	tc := createTopologyTestContext(t, ctx, clientset, restConfig, dynamicClient,
		"tas-indep-clq", "../yaml/tas-indep-clq.yaml", expectedPods)

	logger.Info("2. Deploy workload (TAS2: multiple cliques with different constraints)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify worker-rack pods (3) are in the same rack")
	if err := utils.VerifyLabeledPodsInTopologyDomain(tc.Ctx, tc.Clientset, allPods, LabelPodClique, "tas-indep-clq-0-worker-rack", 3, setup.TopologyLabelRack, logger); err != nil {
		t.Fatalf("Failed to verify worker-rack pods in same rack: %v", err)
	}

	logger.Info("4. Verify worker-block pods (4) are in the same block")
	if err := utils.VerifyLabeledPodsInTopologyDomain(tc.Ctx, tc.Clientset, allPods, LabelPodClique, "tas-indep-clq-0-worker-block", 4, setup.TopologyLabelBlock, logger); err != nil {
		t.Fatalf("Failed to verify worker-block pods in same block: %v", err)
	}

	logger.Info("5. Verify KAI PodGroup has correct SubGroups with topology constraints")
	podGroup := GetPodGroupOrFail(t, tc, 0)

	// Verify top-level TopologyConstraint is empty (no PCS constraint in this test)
	// Verify SubGroups (2 standalone PCLQs - no PCSG)
	expectedSubGroups := []utils.ExpectedSubGroup{
		utils.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "worker-rack", 3, setup.TopologyLabelRack),
		utils.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "worker-block", 4, setup.TopologyLabelBlock),
	}
	if err := utils.VerifyPodGroupTopology(podGroup, "", "", expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	logger.Info("🎉 TAS2: Multiple Cliques with Different Constraints test completed successfully!")
}

// Test_TAS3_PCSOnlyConstraint tests constraint only at PCS level with no PCSG/PCLQ constraints
// 1. Deploy workload with PCS-only constraint (packDomain: rack)
//   - PCSG: NO explicit constraint (nil)
//   - PCLQs: NO explicit constraints
//
// 2. Verify all 4 pods are in same rack (inherited from PCS)
// 3. Verify PCSG worker pods (2 total, 1 per replica)
// 4. Verify router pods (2 standalone)
// 5. Verify KAI PodGroup SubGroups: NO PCSG parent groups (because PCSG constraint is nil, per PR #357)
func Test_TAS3_PCSOnlyConstraint(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 4 // 2 PCSG workers + 2 router standalone
	tc := createTopologyTestContext(t, ctx, clientset, restConfig, dynamicClient,
		"tas-sl-pcs-only", "../yaml/tas-sl-pcs-only.yaml", expectedPods)

	logger.Info("2. Deploy workload (TAS3: PCS-only constraint)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify all 4 pods in same rack (inherited from PCS)")
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, allPods, setup.TopologyLabelRack, logger); err != nil {
		t.Fatalf("Failed to verify all pods in same rack: %v", err)
	}

	logger.Info("4. Verify KAI PodGroup has correct SubGroups (PCS-only constraint)")
	podGroup := GetPodGroupOrFail(t, tc, 0)

	// Verify top-level TopologyConstraint (PCS level: rack)
	// Verify SubGroups (2 PCLQ children + 1 router standalone = 3 total)
	// Note: PCSG parent groups are NOT created when PCSG has nil TopologyConstraint (PR #357)
	expectedSubGroups := []utils.ExpectedSubGroup{
		// Worker PCLQs (directly under PCS constraint, no PCSG parents)
		utils.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "workers-0-worker", 1, ""),
		utils.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "workers-1-worker", 1, ""),
		// Router (standalone)
		utils.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "router", 2, ""),
	}
	if err := utils.VerifyPodGroupTopology(podGroup, setup.TopologyLabelRack, "", expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	logger.Info("🎉 TAS3: PCS-Only Constraint test completed successfully!")
}

// Test_TAS4_PCSGOnlyConstraint tests constraint only at PCSG level with no PCS/PCLQ constraints
// 1. Deploy workload with constraint only at PCSG level (packDomain: rack)
// 2. PCS and PCLQs have NO explicit constraints
// 3. Verify PCSG worker pods (2 total) respect rack constraint
// 4. Router pods (2 standalone) are unconstrained
func Test_TAS4_PCSGOnlyConstraint(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 4 // 2 PCSG workers + 2 router standalone
	tc := createTopologyTestContext(t, ctx, clientset, restConfig, dynamicClient,
		"tas-sl-pcsg-only", "../yaml/tas-sl-pcsg-only.yaml", expectedPods)

	logger.Info("2. Deploy workload (TAS4: PCSG-only constraint)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify PCSG worker pods (2 total, 1 per replica) in same rack")
	if err := utils.VerifyLabeledPodsInTopologyDomain(tc.Ctx, tc.Clientset, allPods, LabelPodCliqueScalingGroup, "tas-sl-pcsg-only-0-workers", 2, setup.TopologyLabelRack, logger); err != nil {
		t.Fatalf("Failed to verify worker pods in same rack: %v", err)
	}

	logger.Info("4. Verify KAI PodGroup has correct SubGroups (PCSG-only constraint)")
	podGroup := GetPodGroupOrFail(t, tc, 0)

	// Verify top-level TopologyConstraint (no PCS constraint)
	// Verify SubGroups (2 PCSG parents + 2 PCLQ children + 1 router standalone = 5 total)
	expectedSubGroups := []utils.ExpectedSubGroup{
		// PCSG replicas (parent groups, rack constraint)
		utils.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, "workers", 0, setup.TopologyLabelRack),
		utils.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, "workers", 1, setup.TopologyLabelRack),
		// Worker PCLQs (children of PCSG replicas)
		utils.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "workers", 0, "worker", 1, ""),
		utils.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "workers", 1, "worker", 1, ""),
		// Router (standalone, no constraint)
		utils.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "router", 2, ""),
	}
	if err := utils.VerifyPodGroupTopology(podGroup, "", "", expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	logger.Info("🎉 TAS4: PCSG-Only Constraint test completed successfully!")
}

// Test_TAS5_HostLevelConstraint tests PCLQ-only constraint with host-level packing
// 1. Deploy workload with constraint only at PCLQ level (packDomain: host)
// 2. PCS has NO explicit constraint
// 3. Verify all 2 pods on same host (strictest constraint)
func Test_TAS5_HostLevelConstraint(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 2
	tc := createTopologyTestContext(t, ctx, clientset, restConfig, dynamicClient,
		"tas-host-level", "../yaml/tas-host-level.yaml", expectedPods)

	logger.Info("2. Deploy workload (TAS5: PCLQ-only host constraint)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify all pods on same host")
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, allPods, setup.TopologyLabelHostname, logger); err != nil {
		t.Fatalf("Failed to verify pods on same host: %v", err)
	}

	// Additional check: verify both pods have same node name
	if len(allPods) != 2 {
		t.Fatalf("Expected 2 pods, got %d", len(allPods))
	}
	if allPods[0].Spec.NodeName != allPods[1].Spec.NodeName {
		t.Fatalf("Pods not on same node: %s vs %s", allPods[0].Spec.NodeName, allPods[1].Spec.NodeName)
	}

	logger.Info("4. Verify KAI PodGroup has correct SubGroups (PCLQ-only host constraint)")
	podGroup := GetPodGroupOrFail(t, tc, 0)

	// Verify top-level TopologyConstraint (no PCS constraint)
	// Verify SubGroups (1 standalone PCLQ with host constraint)
	expectedSubGroups := []utils.ExpectedSubGroup{
		utils.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "worker", 2, setup.TopologyLabelHostname),
	}
	if err := utils.VerifyPodGroupTopology(podGroup, "", "", expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	logger.Info("🎉 TAS5: Host-Level Constraint test completed successfully!")
}

// Test_TAS6_StandalonePCLQOnlyPCSZoneConstraint tests standalone PCLQ with only PCS zone constraint (no PCSG layer)
// This test differs from TAS3 in two ways:
// 1. Uses zone constraint (wider domain) instead of rack at PCS level
// 2. Has NO PCSG layer - only standalone PCLQ directly under PCS (simpler structure)
// 3. PCLQ itself has NO explicit constraint (inherits from PCS)
//
// 1. Deploy workload with PCS zone constraint and single standalone PCLQ (4 replicas)
// 2. Verify all 4 pods in same zone (PCS constraint inherited)
// 3. Verify KAI PodGroup has zone constraint at top level
// 4. Verify 1 SubGroup (standalone PCLQ) with NO additional constraint
func Test_TAS6_StandalonePCLQOnlyPCSZoneConstraint(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 4
	tc := createTopologyTestContext(t, ctx, clientset, restConfig, dynamicClient,
		"tas-standalone-pclq", "../yaml/tas-standalone-pclq-only-pcs-zone.yaml", expectedPods)

	logger.Info("2. Deploy workload (TAS6: Standalone PCLQ with only PCS zone constraint)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify all 4 pods in same zone (PCS zone constraint)")
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, allPods, setup.TopologyLabelZone, logger); err != nil {
		t.Fatalf("Failed to verify pods in same zone: %v", err)
	}

	logger.Info("4. Verify KAI PodGroup has correct SubGroups (Standalone PCLQ with PCS zone constraint)")
	podGroup := GetPodGroupOrFail(t, tc, 0)

	// Verify top-level TopologyConstraint (PCS level: zone)
	// Verify SubGroups (1 standalone PCLQ with NO constraint - zone is at PCS level)
	expectedSubGroups := []utils.ExpectedSubGroup{
		utils.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "worker", 4, ""),
	}
	if err := utils.VerifyPodGroupTopology(podGroup, setup.TopologyLabelZone, "", expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	logger.Info("🎉 TAS6: Standalone PCLQ with Only PCS Zone Constraint test completed successfully!")
}

// Test_TAS7_NoTopologyConstraint tests gang scheduling without any topology constraints
// 1. Deploy workload with no constraints at PCS, PCSG, or PCLQ levels
// 2. Verify all 4 pods scheduled (gang scheduling works)
// 3. Verify KAI PodGroup has 4 SubGroups with NO topology constraints
func Test_TAS7_NoTopologyConstraint(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 4 // 2 PCSG replicas × 2 pods each
	tc := createTopologyTestContext(t, ctx, clientset, restConfig, dynamicClient,
		"tas-no-constraint", "../yaml/tas-no-constraint.yaml", expectedPods)

	logger.Info("2. Deploy workload (TAS7: No topology constraints)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify all 4 pods scheduled (gang scheduling works without constraints)")
	if len(allPods) != 4 {
		t.Fatalf("Expected 4 pods, got %d", len(allPods))
	}
	logger.Info("4. Verify KAI PodGroup has correct SubGroups (no constraints)")
	podGroup := GetPodGroupOrFail(t, tc, 0)

	// Verify top-level TopologyConstraint (no PCS constraint)
	// Verify SubGroups (2 PCLQ children, NO constraints)
	// Note: PCSG parent groups are NOT created when PCSG has nil TopologyConstraint (PR #357)
	expectedSubGroups := []utils.ExpectedSubGroup{
		// Worker PCLQs (directly under PCS, no PCSG parents, no constraints)
		utils.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "workers-0-worker", 2, ""),
		utils.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "workers-1-worker", 2, ""),
	}
	if err = utils.VerifyPodGroupTopology(podGroup, "", "", expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	logger.Info("🎉 TAS7: No Topology Constraint test completed successfully!")
}

// Test_TAS8_FullHierarchyWithCascadingConstraints tests 3-level topology hierarchy with cascading constraints
// 1. Deploy workload with PCS (block) → PCSG (rack) → PCLQ (host) constraints
// 2. PCSG: 2 replicas with prefill (2 pods) + decode (2 pods) cliques
// 3. Verify each PCLQ's pods on same host (4 verifications: prefill0, decode0, prefill1, decode1)
// 4. Verify each PCSG replica in same rack (2 verifications: replica0, replica1)
// 5. Verify all pods in same block (PCS constraint)
// 6. Verify KAI PodGroup hierarchy with correct topology constraints
func Test_TAS8_FullHierarchyWithCascadingConstraints(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize an 8-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 8)
	defer cleanup()

	expectedPods := 8 // 2 PCSG replicas × (prefill: 2 pods + decode: 2 pods)
	tc := createTopologyTestContext(t, ctx, clientset, restConfig, dynamicClient,
		"tas-hierarchy", "../yaml/tas-hierarchy.yaml", expectedPods)

	logger.Info("2. Deploy workload (TAS8: full 3-level hierarchy with cascading constraints)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify PCLQ constraints (2 replicas × 2 clique types) - all on same host")
	cliqueTypes := []string{"prefill", "decode"}
	for pcsgReplica := 0; pcsgReplica < 2; pcsgReplica++ {
		for _, cliqueType := range cliqueTypes {
			cliquePods := utils.FilterPodsByLabel(allPods, LabelPodClique,
				fmt.Sprintf("tas-hierarchy-0-inference-group-%d-%s", pcsgReplica, cliqueType))
			if len(cliquePods) != 2 {
				t.Fatalf("Expected 2 %s pods for PCSG replica %d, got %d", cliqueType, pcsgReplica, len(cliquePods))
			}
			if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, cliquePods, setup.TopologyLabelHostname, logger); err != nil {
				t.Fatalf("Failed to verify %s pods on same host for PCSG replica %d: %v", cliqueType, pcsgReplica, err)
			}
		}
	}

	logger.Info("4. Verify PCSG constraints (2 replicas) - all in same rack")
	if err := utils.VerifyPCSGReplicasInTopologyDomain(tc.Ctx, tc.Clientset, allPods,
		"tas-hierarchy-0-inference-group", 2, 4, setup.TopologyLabelRack, logger); err != nil {
		t.Fatalf("Failed to verify PCSG replicas: %v", err)
	}

	logger.Info("5. Verify all pods are in same block (PCS constraint)")
	if len(allPods) != expectedPods {
		t.Fatalf("Expected %d pods, got %d", expectedPods, len(allPods))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, allPods, setup.TopologyLabelBlock, logger); err != nil {
		t.Fatalf("Failed to verify all pods in same block: %v", err)
	}

	logger.Info("6. Verify KAI PodGroup has correct hierarchy with topology constraints")
	podGroup := GetPodGroupOrFail(t, tc, 0)

	// Verify top-level TopologyConstraint (PCS level: block) + SubGroups hierarchy (2 PCSG parents + 4 PCLQ children)
	expectedSubGroups := []utils.ExpectedSubGroup{
		utils.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, "inference-group", 0, setup.TopologyLabelRack),
		utils.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, "inference-group", 1, setup.TopologyLabelRack),
		utils.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "inference-group", 0, "prefill", 2, setup.TopologyLabelHostname),
		utils.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "inference-group", 0, "decode", 2, setup.TopologyLabelHostname),
		utils.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "inference-group", 1, "prefill", 2, setup.TopologyLabelHostname),
		utils.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "inference-group", 1, "decode", 2, setup.TopologyLabelHostname),
	}
	if err := utils.VerifyPodGroupTopology(podGroup, setup.TopologyLabelBlock, "", expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	logger.Info("🎉 TAS8: Full Hierarchy with Cascading Constraints test completed successfully!")
}

// Test_TAS9_PCSPlusPCLQConstraint tests PCS block constraint combined with PCLQ host constraint
// 1. Deploy workload with PCS: block constraint, PCLQ: host constraint
// 2. 2 pods total
// 3. Verify pods on same host (PCLQ constraint - strictest)
// 4. Verify KAI PodGroup has block constraint at top level, host constraint at PCLQ level
func Test_TAS9_PCSPlusPCLQConstraint(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 2
	tc := createTopologyTestContext(t, ctx, clientset, restConfig, dynamicClient,
		"tas-pcs-pclq", "../yaml/tas-pcs-pclq.yaml", expectedPods)

	logger.Info("2. Deploy workload (TAS9: PCS block + PCLQ host constraint)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify 2 pods on same host (PCLQ host constraint)")
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, allPods, setup.TopologyLabelHostname, logger); err != nil {
		t.Fatalf("Failed to verify pods on same host: %v", err)
	}

	logger.Info("4. Verify KAI PodGroup has correct SubGroups (PCS block + PCLQ host)")
	podGroup := GetPodGroupOrFail(t, tc, 0)

	// Verify top-level TopologyConstraint (PCS level: block) + SubGroups (1 standalone PCLQ with host constraint)
	expectedSubGroups := []utils.ExpectedSubGroup{
		utils.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "worker", 2, setup.TopologyLabelHostname),
	}
	if err := utils.VerifyPodGroupTopology(podGroup, setup.TopologyLabelBlock, "", expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	logger.Info("🎉 TAS9: PCS+PCLQ Constraint test completed successfully!")
}

// Test_TAS10_PCSGScalingWithTopologyConstraints tests PCSG scaling with rack constraints
// 1. Deploy workload with 3 PCSG replicas, each with rack constraint
// 2. 6 pods total (2 per PCSG replica)
// 3. Verify each PCSG replica's pods in same rack
// 4. Verify all pods respect PCS-level rack constraint (all in same rack)
// 5. Verify base PodGang KAI PodGroup topology constraints
// 6. Verify scaled PodGangs' KAI PodGroups (replicas 1-2)
func Test_TAS10_PCSGScalingWithTopologyConstraints(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 6 // 3 PCSG replicas × 2 pods each
	tc := createTopologyTestContext(t, ctx, clientset, restConfig, dynamicClient,
		"tas-pcsg-scale", "../yaml/tas-pcsg-scale.yaml", expectedPods)

	logger.Info("2. Deploy workload (TAS10: PCSG scaling with topology constraints)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify each PCSG replica's worker pods (2) are in same rack")
	if err := utils.VerifyPCSGReplicasInTopologyDomain(tc.Ctx, tc.Clientset, allPods,
		"tas-pcsg-scale-0-inference-group", 3, 2, setup.TopologyLabelRack, logger); err != nil {
		t.Fatalf("Failed to verify PCSG replicas: %v", err)
	}

	logger.Info("4. Verify all pods respect PCS-level block constraint")
	if len(allPods) != expectedPods {
		t.Fatalf("Expected %d pods, got %d", expectedPods, len(allPods))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, allPods, setup.TopologyLabelBlock, logger); err != nil {
		t.Fatalf("Failed to verify all pods in same block: %v", err)
	}

	logger.Info("5. Verify KAI PodGroup has correct SubGroups with topology constraints")
	podGroup := GetPodGroupOrFail(t, tc, 0)

	// Verify top-level TopologyConstraint (PCS level: block)
	// Base PodGang contains only minAvailable=1 PCSG replica
	// PCSG has replicas=3 and minAvailable=1, so base PodGang contains ONLY replica 0
	// Replicas 1 and 2 are in separate scaled PodGangs
	expectedSubGroups := []utils.ExpectedSubGroup{
		utils.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, "inference-group", 0, setup.TopologyLabelRack),
		utils.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "inference-group", 0, "worker", 2, ""),
	}
	if err := utils.VerifyPodGroupTopology(podGroup, setup.TopologyLabelBlock, "", expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	logger.Info("6. Verify scaled PodGangs' KAI PodGroups (replicas 1-2)")

	// Verify PCSG replicas 1-2 (minAvailable=1, totalReplicas=3)
	lo.ForEach([]int{1, 2}, func(pcsgReplica int, _ int) {
		utils.VerifyScaledPCSGReplicaTopology(tc.Ctx, t, tc.DynamicClient, tc.Namespace, tc.Workload.Name, 0,
			utils.ScaledPCSGConfig{
				Name:         "inference-group",
				PCSGName:     "inference-group",
				PCSGReplica:  pcsgReplica,
				MinAvailable: 1,
				CliqueConfigs: []utils.PCSGCliqueConfig{
					{Name: "worker", PodCount: 2, Constraint: ""},
				},
				Constraint: setup.TopologyLabelRack,
			}, setup.TopologyLabelBlock, logger)
	})

	logger.Info("🎉 TAS10: PCSG Scaling with Topology Constraints test completed successfully!")
}

// Test_TAS11_PCSGPlusPCLQNoParentConstraint tests PCSG rack + PCLQ host constraints without PCS constraint
// 1. Deploy workload with PCSG: rack constraint, PCLQ: host constraint, NO PCS constraint
// 2. 4 pods (2 PCSG replicas × 2 pods)
// 3. Verify each PCSG replica's pods on same host
// 4. Verify KAI PodGroup has PCSG rack + PCLQ host constraints, NO top-level PCS constraint
func Test_TAS11_PCSGPlusPCLQNoParentConstraint(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 4 // 2 PCSG replicas × 2 pods each
	tc := createTopologyTestContext(t, ctx, clientset, restConfig, dynamicClient,
		"tas-pcsg-pclq", "../yaml/tas-pcsg-pclq.yaml", expectedPods)

	logger.Info("2. Deploy workload (TAS11: PCSG rack + PCLQ host, no PCS constraint)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify each PCSG replica's pods on same host")
	workersPCSG := nameutils.GeneratePodCliqueScalingGroupName(
		nameutils.ResourceNameReplica{Name: tc.Workload.Name, Replica: 0},
		"workers",
	)
	if err := utils.VerifyPCSGReplicasInTopologyDomain(tc.Ctx, tc.Clientset, allPods,
		workersPCSG, 2, 2, setup.TopologyLabelHostname, logger); err != nil {
		t.Fatalf("Failed to verify PCSG replicas: %v", err)
	}

	logger.Info("4. Verify KAI PodGroup has correct SubGroups (PCSG rack + PCLQ host)")
	podGroup := GetPodGroupOrFail(t, tc, 0)

	// Verify top-level TopologyConstraint (no PCS constraint)
	// SubGroups (2 PCSG parents with rack + 2 PCLQ children with host)
	expectedSubGroups := []utils.ExpectedSubGroup{
		utils.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, "workers", 0, setup.TopologyLabelRack),
		utils.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, "workers", 1, setup.TopologyLabelRack),
		utils.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "workers", 0, "worker", 2, setup.TopologyLabelHostname),
		utils.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "workers", 1, "worker", 2, setup.TopologyLabelHostname),
	}
	if err := utils.VerifyPodGroupTopology(podGroup, "", "", expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	logger.Info("🎉 TAS11: PCSG+PCLQ Constraint test completed successfully!")
}

// Test_TAS12_LargeScalingRatio tests large PCSG scaling ratio with minAvailable
// 1. Deploy workload with replicas=10, minAvailable=3, PCSG host constraint, PCS block constraint
// 2. 20 pods expected (only minAvailable=3 replicas × 2 pods from base PodGang + 7 scaled PodGangs × 2 pods)
// 3. Verify each PCSG replica's pods on same host
// 4. Verify all pods in same block (PCS constraint)
// 5. Verify base PodGang KAI PodGroup contains minAvailable=3 replicas
// 6. Verify 7 scaled PodGangs' KAI PodGroups (replicas 3-9)
func Test_TAS12_LargeScalingRatio(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 20 // Base PodGang: 3 PCSG replicas × 2 pods (6) + Scaled: 7 PCSG replicas × 2 pods (14)
	tc := createTopologyTestContext(t, ctx, clientset, restConfig, dynamicClient,
		"tas-large-scale", "../yaml/tas-large-scale.yaml", expectedPods)

	logger.Info("2. Deploy workload (TAS12: Large scaling ratio, replicas=10/minAvailable=3)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify each PCSG replica's pods on same host")
	if err := utils.VerifyPCSGReplicasInTopologyDomain(tc.Ctx, tc.Clientset, allPods,
		"tas-large-scale-0-workers", 10, 2, setup.TopologyLabelHostname, logger); err != nil {
		t.Fatalf("Failed to verify PCSG replicas: %v", err)
	}

	logger.Info("4. Verify all 20 pods in same block (PCS block constraint)")
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, allPods, setup.TopologyLabelBlock, logger); err != nil {
		t.Fatalf("Failed to verify all pods in same block: %v", err)
	}

	logger.Info("5. Verify base PodGang's KAI PodGroup (replicas 0-2)")
	podGroup := GetPodGroupOrFail(t, tc, 0)

	// Verify top-level TopologyConstraint (PCS level: block)
	// SubGroups (3 worker PCLQs with host constraint, no PCSG parent since no rack constraint)
	pcsgFQN := nameutils.GeneratePodCliqueScalingGroupName(
		nameutils.ResourceNameReplica{Name: tc.Workload.Name, Replica: 0},
		"workers",
	)
	expectedSubGroups := []utils.ExpectedSubGroup{
		utils.CreateExpectedPCLQInPCSGSubGroupNoParent(tc.Workload.Name, 0, "workers", 0, "worker", 2, setup.TopologyLabelHostname),
		utils.CreateExpectedPCLQInPCSGSubGroupNoParent(tc.Workload.Name, 0, "workers", 1, "worker", 2, setup.TopologyLabelHostname),
		utils.CreateExpectedPCLQInPCSGSubGroupNoParent(tc.Workload.Name, 0, "workers", 2, "worker", 2, setup.TopologyLabelHostname),
	}
	if err := utils.VerifyPodGroupTopology(podGroup, setup.TopologyLabelBlock, "", expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	logger.Info("6. Verify scaled PodGangs' KAI PodGroups (replicas 3-9)")
	podGroups, err := utils.GetKAIPodGroupsForPCS(tc.Ctx, tc.DynamicClient, tc.Namespace, tc.Workload.Name)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroups: %v", err)
	}

	// PCSG config: replicas=10, minAvailable=3
	// Base PodGang contains replicas 0-2, scaled PodGangs contain replicas 3-9 (reuse pcsgFQN from above)
	pcsgMinAvailable := 3
	pcsgTotalReplicas := 10
	scaledPodGangCount := pcsgTotalReplicas - pcsgMinAvailable

	for scaledIndex := 0; scaledIndex < scaledPodGangCount; scaledIndex++ {
		pcsgReplicaIndex := pcsgMinAvailable + scaledIndex
		scaledPodGangName := nameutils.CreatePodGangNameFromPCSGFQN(pcsgFQN, scaledIndex)

		scaledPodGroup, err := utils.FilterPodGroupByOwner(podGroups, scaledPodGangName)
		if err != nil {
			t.Fatalf("Failed to find scaled PodGroup for %s: %v", scaledPodGangName, err)
		}

		// Each scaled PodGang contains 1 PCSG replica with 1 PCLQ SubGroup (host constraint)
		expectedSubGroups := []utils.ExpectedSubGroup{
			utils.CreateExpectedPCLQInPCSGSubGroupNoParent(tc.Workload.Name, 0, "workers", pcsgReplicaIndex, "worker", 2, setup.TopologyLabelHostname),
		}

		if err := utils.VerifyPodGroupTopology(scaledPodGroup, setup.TopologyLabelBlock, "", expectedSubGroups, logger); err != nil {
			t.Fatalf("Failed to verify scaled PodGroup %s (PCSG replica %d) topology: %v",
				scaledPodGangName, pcsgReplicaIndex, err)
		}
	}

	logger.Info("🎉 TAS12: Large Scaling Ratio test completed successfully!")
}

// Test_TAS13_InsufficientNodesForConstraint tests gang scheduling failure with unsatisfiable topology constraint
// 1. Deploy workload with rack constraint requesting 10 pods (exceeds rack capacity)
// 2. Verify all 10 pods remain in Pending state (no partial scheduling)
// 3. Verify NO pods are scheduled (all-or-nothing gang behavior)
// 4. Verify pod events show Unschedulable reason
// 5. Verify KAI PodGroup exists with correct constraints even though pods are pending
func Test_TAS13_InsufficientNodesForConstraint(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 10
	tc := createTopologyTestContext(t, ctx, clientset, restConfig, dynamicClient,
		"tas-insuffic", "../yaml/tas-insuffic.yaml", expectedPods)

	logger.Info("2. Deploy workload (TAS13: insufficient nodes for rack constraint)")
	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Verify all 10 pods remain in Pending state (no partial scheduling)")
	if err := verifyPodsArePendingWithUnschedulableEvents(tc, true, expectedPods); err != nil {
		t.Fatalf("Failed to verify pods are pending with unschedulable events: %v", err)
	}

	logger.Info("4. Verify NO pods are scheduled (all-or-nothing gang behavior)")
	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}

	lo.ForEach(pods.Items, func(pod v1.Pod, _ int) {
		if pod.Spec.NodeName != "" {
			t.Fatalf("Expected pod %s to have no node assignment, but assigned to %s", pod.Name, pod.Spec.NodeName)
		}
	})
	logger.Info("5. Verify KAI PodGroup exists with correct topology constraints (even though pods are pending)")
	podGroup := GetPodGroupOrFail(t, tc, 0)

	// Verify top-level TopologyConstraint (PCS level: rack)
	// SubGroups (1 standalone PCLQ - no PCSG)
	expectedSubGroups := []utils.ExpectedSubGroup{
		utils.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "worker", 10, ""),
	}
	if err := utils.VerifyPodGroupTopology(podGroup, setup.TopologyLabelRack, "", expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	logger.Info("🎉 TAS13: Insufficient Nodes for Constraint test completed successfully!")
}

// Test_TAS14_MultiReplicaWithRackConstraint tests multiple PCS replicas with rack constraints
// 1. Deploy workload with 2 PCS replicas, each with rack constraint
// 2. 4 pods (2 per PCS replica)
// 3. Verify each PCS replica's pods in same rack
// 4. Verify KAI PodGroups for both PCS replicas have correct topology constraints
func Test_TAS14_MultiReplicaWithRackConstraint(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 4
	tc := createTopologyTestContext(t, ctx, clientset, restConfig, dynamicClient,
		"tas-multirep", "../yaml/tas-multirep.yaml", expectedPods)

	logger.Info("2. Deploy workload (TAS14: multi-replica with rack constraint)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify each PCS replica's pods (2) are in same rack")
	for pcsReplica := 0; pcsReplica < 2; pcsReplica++ {
		replicaPods := utils.FilterPodsByLabel(allPods, "grove.io/podcliqueset-replica-index", fmt.Sprintf("%d", pcsReplica))
		if len(replicaPods) != 2 {
			t.Fatalf("Expected 2 replica-%d pods, got %d", pcsReplica, len(replicaPods))
		}
		if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, replicaPods, setup.TopologyLabelRack, logger); err != nil {
			t.Fatalf("Failed to verify replica-%d pods in same rack: %v", pcsReplica, err)
		}
	}

	logger.Info("4. Verify KAI PodGroups for both replicas have correct topology constraints")
	for pcsReplica := 0; pcsReplica < 2; pcsReplica++ {
		podGroup := GetPodGroupOrFail(t, tc, pcsReplica)

		expectedSubGroups := []utils.ExpectedSubGroup{
			utils.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, pcsReplica, "worker", 2, ""),
		}
		if err := utils.VerifyPodGroupTopology(podGroup, setup.TopologyLabelRack, "", expectedSubGroups, logger); err != nil {
			t.Fatalf("Failed to verify PodGroup-%d topology: %v", pcsReplica, err)
		}
	}

	logger.Info("🎉 TAS14: Multi-Replica with Rack Constraint test completed successfully!")
}

// Test_TAS15_DisaggregatedInferenceMultiplePCSGs tests disaggregated inference with multiple PCSGs
// 1. Deploy workload with 2 PCSGs (decoder, prefill) + standalone router
// 2. decoder PCSG (2 replicas, rack constraint) + prefill PCSG (2 replicas, rack constraint) + router standalone
// 3. PCS: block constraint
// 4. 10 pods total: decoder (2×2) + prefill (2×2) + router (2)
// 5. Verify all in same block, each PCSG replica in same rack
// 6. Verify base PodGang KAI PodGroup topology for complex multi-PCSG workload
// 7. Verify scaled PodGangs' KAI PodGroups (decoder replica 1, prefill replica 1)
func Test_TAS15_DisaggregatedInferenceMultiplePCSGs(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 10 // decoder (2×2) + prefill (2×2) + router (2)
	tc := createTopologyTestContext(t, ctx, clientset, restConfig, dynamicClient,
		"tas-pcs-multi-pcsg", "../yaml/tas-pcs-multi-pcsg.yaml", expectedPods)

	logger.Info("2. Deploy workload (TAS15: disaggregated inference with multiple PCSGs)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify block-level constraint (all 10 pods in same block)")
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, allPods, setup.TopologyLabelBlock, logger); err != nil {
		t.Fatalf("Failed to verify all pods in same block: %v", err)
	}

	// Generate PCSG and PCLQ names
	pcsReplica := nameutils.ResourceNameReplica{Name: tc.Workload.Name, Replica: 0}
	decoderPCSG := nameutils.GeneratePodCliqueScalingGroupName(pcsReplica, "decoder")
	prefillPCSG := nameutils.GeneratePodCliqueScalingGroupName(pcsReplica, "prefill")
	routerPCLQ := nameutils.GeneratePodCliqueName(pcsReplica, "router")

	logger.Info("4. Verify PCSG replicas (2 types × 2 replicas) are in same rack")
	pcsgTypes := []utils.PCSGTypeConfig{
		{Name: "decoder", FQN: decoderPCSG},
		{Name: "prefill", FQN: prefillPCSG},
	}
	if err := utils.VerifyMultiTypePCSGReplicas(tc.Ctx, tc.Clientset, allPods, pcsgTypes, 2, 2,
		setup.TopologyLabelRack, logger); err != nil {
		t.Fatalf("Failed to verify PCSG replicas: %v", err)
	}

	logger.Info("5. Verify router pods (2 standalone, no PCSG label)")
	routerPods := utils.FilterPodsByLabel(allPods, LabelPodClique, routerPCLQ)
	if len(routerPods) != 2 {
		t.Fatalf("Expected 2 router pods, got %d", len(routerPods))
	}

	logger.Info("6. Verify KAI PodGroup has correct SubGroups for disaggregated inference")
	podGroup := GetPodGroupOrFail(t, tc, 0)

	// Verify top-level TopologyConstraint (PCS level: block)
	// SubGroups (Base PodGang contains only minAvailable=1 PCSG replicas)
	expectedSubGroups := []utils.ExpectedSubGroup{
		utils.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, "decoder", 0, setup.TopologyLabelRack),
		utils.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, "prefill", 0, setup.TopologyLabelRack),
		utils.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "decoder", 0, "dworker", 1, ""),
		utils.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "decoder", 0, "dleader", 1, ""),
		utils.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "prefill", 0, "pworker", 1, ""),
		utils.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "prefill", 0, "pleader", 1, ""),
		utils.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "router", 2, ""),
	}
	if err := utils.VerifyPodGroupTopology(podGroup, setup.TopologyLabelBlock, "", expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	logger.Info("7. Verify scaled PodGangs' KAI PodGroups (decoder replica 1, prefill replica 1)")

	// Define PCSG configurations (minAvailable=1, totalReplicas=2 for each)
	pcsgConfigs := []utils.ScaledPCSGConfig{
		{
			Name:         "decoder",
			PCSGName:     "decoder",
			PCSGReplica:  1,
			MinAvailable: 1,
			CliqueConfigs: []utils.PCSGCliqueConfig{
				{Name: "dworker", PodCount: 1, Constraint: ""},
				{Name: "dleader", PodCount: 1, Constraint: ""},
			},
			Constraint: setup.TopologyLabelRack,
		},
		{
			Name:         "prefill",
			PCSGName:     "prefill",
			PCSGReplica:  1,
			MinAvailable: 1,
			CliqueConfigs: []utils.PCSGCliqueConfig{
				{Name: "pworker", PodCount: 1, Constraint: ""},
				{Name: "pleader", PodCount: 1, Constraint: ""},
			},
			Constraint: setup.TopologyLabelRack,
		},
	}

	// Verify each PCSG's scaled replica
	lo.ForEach(pcsgConfigs, func(pcsgConfig utils.ScaledPCSGConfig, _ int) {
		utils.VerifyScaledPCSGReplicaTopology(tc.Ctx, t, tc.DynamicClient, tc.Namespace, tc.Workload.Name, 0,
			pcsgConfig, setup.TopologyLabelBlock, logger)
	})

	logger.Info("🎉 TAS15: Disaggregated Inference with Multiple PCSGs test completed successfully!")
}

// Test_TAS16_MultiReplicaPCSWithThreeLevelHierarchy tests multi-replica PCS with full 3-level topology hierarchy
// 1. Deploy workload with 2 PCS replicas, each with full 3-level hierarchy
// 2. 20 pods (10 per PCS replica): decoder (2×2) + prefill (2×2) + router (2)
// 3. PCS: block constraint, PCSG: rack constraint, PCLQ (pworker): host constraint
// 4. Verify block constraint at PCS level, rack at PCSG, for both PCS replicas
// 5. Similar to TAS15 but scaled across 2 PCS replicas
func Test_TAS16_MultiReplicaPCSWithThreeLevelHierarchy(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for multi-replica PCS testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 20 // PCS replica 0: 10 pods + PCS replica 1: 10 pods
	tc := createTopologyTestContext(t, ctx, clientset, restConfig, dynamicClient,
		"tas-pcs-multi-pcsg", "../yaml/tas-pcs-multi-pcsg-multi-replica.yaml", expectedPods)

	logger.Info("2. Deploy workload (TAS16: 2 PCS replicas with 3-level topology hierarchy)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	// Verify for each PCS replica
	for pcsReplica := 0; pcsReplica < 2; pcsReplica++ {
		replicaLabel := fmt.Sprintf("%d", pcsReplica)
		replicaPods := utils.FilterPodsByLabel(allPods, "grove.io/podcliqueset-replica-index", replicaLabel)
		if len(replicaPods) != 10 {
			t.Fatalf("Expected 10 pods for PCS replica %d, got %d", pcsReplica, len(replicaPods))
		}

		logger.Infof("3.%d. Verify PCS replica %d pods in same block (PCS block constraint)", pcsReplica+1, pcsReplica)
		if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, replicaPods, setup.TopologyLabelBlock, logger); err != nil {
			t.Fatalf("Failed to verify PCS replica %d pods in same block: %v", pcsReplica, err)
		}

		logger.Infof("4.%d. Verify PCS replica %d pods topology constraints", pcsReplica+1, pcsReplica)

		// Generate PCSG and PCLQ names for this PCS replica
		decoderPCSG := nameutils.GeneratePodCliqueScalingGroupName(
			nameutils.ResourceNameReplica{Name: tc.Workload.Name, Replica: pcsReplica},
			"decoder",
		)
		prefillPCSG := nameutils.GeneratePodCliqueScalingGroupName(
			nameutils.ResourceNameReplica{Name: tc.Workload.Name, Replica: pcsReplica},
			"prefill",
		)
		routerPCLQ := nameutils.GeneratePodCliqueName(
			nameutils.ResourceNameReplica{Name: tc.Workload.Name, Replica: pcsReplica},
			"router",
		)

		// Verify PCSG replicas (2 types × 2 replicas) are in same rack
		pcsgTypes := []utils.PCSGTypeConfig{
			{Name: "decoder", FQN: decoderPCSG},
			{Name: "prefill", FQN: prefillPCSG},
		}
		if err := utils.VerifyMultiTypePCSGReplicas(tc.Ctx, tc.Clientset, replicaPods, pcsgTypes, 2, 2,
			setup.TopologyLabelRack, logger); err != nil {
			t.Fatalf("Failed to verify PCSG replicas for PCS replica %d: %v", pcsReplica, err)
		}

		// Verify router pods (2 standalone)
		logger.Infof("4.%d. Verify router pods (2 standalone)", pcsReplica+1)
		routerPods := utils.FilterPodsByLabel(replicaPods, LabelPodClique, routerPCLQ)
		if len(routerPods) != 2 {
			t.Fatalf("Expected 2 router pods for PCS replica %d, got %d", pcsReplica, len(routerPods))
		}
	}

	logger.Info("5. Verify KAI PodGroups for both PCS replicas have correct topology constraints")
	for pcsReplica := 0; pcsReplica < 2; pcsReplica++ {
		podGroup := GetPodGroupOrFail(t, tc, pcsReplica)

		// Verify SubGroups for this PCS replica (hierarchy: PCS→PCSG→PCLQ)
		expectedSubGroups := []utils.ExpectedSubGroup{
			utils.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, pcsReplica, "decoder", 0, setup.TopologyLabelRack),
			utils.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, pcsReplica, "prefill", 0, setup.TopologyLabelRack),
			utils.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, pcsReplica, "decoder", 0, "dworker", 1, ""),
			utils.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, pcsReplica, "decoder", 0, "dleader", 1, ""),
			utils.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, pcsReplica, "prefill", 0, "pworker", 1, setup.TopologyLabelHostname),
			utils.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, pcsReplica, "prefill", 0, "pleader", 1, ""),
			utils.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, pcsReplica, "router", 2, ""),
		}
		if err := utils.VerifyPodGroupTopology(podGroup, setup.TopologyLabelBlock, "", expectedSubGroups, logger); err != nil {
			t.Fatalf("Failed to verify KAI PodGroup-%d topology: %v", pcsReplica, err)
		}
	}

	logger.Info("🎉 TAS16: Multi-replica PCS with 3-level topology hierarchy test completed successfully!")
}

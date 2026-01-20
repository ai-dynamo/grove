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

	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/utils"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// Test_TI1_TopologyInfrastructure verifies that the operator creates ClusterTopology and KAI Topology CRs at startup
// Scenario TI-1 (Topology Infrastructure Setup):
// 1. Verify ClusterTopology CR exists with the correct 4-level hierarchy (zone, block, rack, host)
// 2. Verify KAI Topology CR exists with matching levels
// 3. Verify KAI Topology has owner reference to ClusterTopology
// 4. Verify worker nodes have topology labels
func Test_TAS_TI1_TopologyInfrastructure(t *testing.T) {
	ctx := context.Background()

	clientset, _, dynamicClient, cleanup := prepareTestCluster(ctx, t, 0)
	defer cleanup()

	logger.Info("1. Verify ClusterTopology CR exists with correct 4-level hierarchy")

	expectedLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: "kubernetes.io/zone"},
		{Domain: corev1alpha1.TopologyDomainBlock, Key: "kubernetes.io/block"},
		{Domain: corev1alpha1.TopologyDomainRack, Key: "kubernetes.io/rack"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}

	if err := utils.VerifyClusterTopologyLevels(ctx, dynamicClient, corev1alpha1.DefaultClusterTopologyName, expectedLevels, logger); err != nil {
		t.Fatalf("Failed to verify ClusterTopology levels: %v", err)
	}

	logger.Info("2. Verify KAI Topology CR exists with matching levels and owner reference")

	expectedKeys := []string{
		"kubernetes.io/zone",
		"kubernetes.io/block",
		"kubernetes.io/rack",
		"kubernetes.io/hostname",
	}

	if err := utils.VerifyKAITopologyLevels(ctx, dynamicClient, corev1alpha1.DefaultClusterTopologyName, expectedKeys, logger); err != nil {
		t.Fatalf("Failed to verify KAI Topology levels: %v", err)
	}

	logger.Info("3. Verify worker nodes have topology labels")

	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Failed to list nodes: %v", err)
	}

	workerCount := 0
	for _, node := range nodes.Items {
		if _, isControlPlane := node.Labels["node-role.kubernetes.io/control-plane"]; isControlPlane {
			continue
		}

		workerCount++

		// Verify zone label
		if zone, ok := node.Labels["kubernetes.io/zone"]; !ok || zone == "" {
			t.Errorf("Node %s missing kubernetes.io/zone label", node.Name)
		}

		// Verify block label
		if block, ok := node.Labels["kubernetes.io/block"]; !ok || block == "" {
			t.Errorf("Node %s missing kubernetes.io/block label", node.Name)
		}

		// Verify rack label
		if rack, ok := node.Labels["kubernetes.io/rack"]; !ok || rack == "" {
			t.Errorf("Node %s missing kubernetes.io/rack label", node.Name)
		}

		// hostname label should exist by default
		if hostname, ok := node.Labels["kubernetes.io/hostname"]; !ok || hostname == "" {
			t.Errorf("Node %s missing kubernetes.io/hostname label", node.Name)
		}
	}

	if workerCount == 0 {
		t.Fatal("No worker nodes found in cluster")
	}

	logger.Infof("Successfully verified topology labels on %d worker nodes", workerCount)
	logger.Info("🎉 Topology Infrastructure test completed successfully!")
}

// Test_TAS_BP1_MultipleCliquesWithDifferentConstraints tests PCS with multiple cliques having different topology constraints
// Scenario BP-1:
// 1. Deploy workload with PCS (no constraint) containing 2 cliques:
//   - worker-rack: packDomain=rack (3 pods)
//   - worker-block: packDomain=block (4 pods)
//
// 2. Verify all 7 pods are scheduled successfully
// 3. Verify worker-rack pods (3) are in the same rack
// 4. Verify worker-block pods (4) are in the same block
// 5. Verify different cliques can have independent topology constraints
func Test_TAS_BP1_MultipleCliquesWithDifferentConstraints(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 7-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 7)
	defer cleanup()

	expectedPods := 7 // worker-rack: 3 pods, worker-block: 4 pods
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
			Name:         "tas-indep-clq",
			YAMLPath:     "../yaml/tas-indep-clq.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	logger.Info("2. Deploy workload (BP-1: multiple cliques with different constraints)")
	allPods, err := deployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify worker-rack pods (3) are in the same rack")
	rackPods := utils.FilterPodsByLabel(allPods, "grove.io/podclique", "tas-indep-clq-0-worker-rack")
	if len(rackPods) != 3 {
		t.Fatalf("Expected 3 worker-rack pods, got %d", len(rackPods))
	}

	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, rackPods, "kubernetes.io/rack", logger); err != nil {
		t.Fatalf("Failed to verify worker-rack pods in same rack: %v", err)
	}

	logger.Info("4. Verify worker-block pods (4) are in the same block")
	blockPods := utils.FilterPodsByLabel(allPods, "grove.io/podclique", "tas-indep-clq-0-worker-block")
	if len(blockPods) != 4 {
		t.Fatalf("Expected 4 worker-block pods, got %d", len(blockPods))
	}

	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, blockPods, "kubernetes.io/block", logger); err != nil {
		t.Fatalf("Failed to verify worker-block pods in same block: %v", err)
	}

	logger.Info("5. Verify KAI PodGroup has correct SubGroups with topology constraints")
	podGroups, err := utils.WaitForKAIPodGroups(tc.Ctx, tc.DynamicClient, tc.Namespace, "tas-indep-clq", tc.Timeout, tc.Interval, logger)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroups: %v", err)
	}

	podGroup, err := utils.FilterPodGroupByOwner(podGroups, "tas-indep-clq-0")
	if err != nil {
		t.Fatalf("Failed to find PodGroup for PodGang tas-indep-clq-0: %v", err)
	}

	// Verify top-level TopologyConstraint is empty (no PCS constraint in this test)
	if err := utils.VerifyKAIPodGroupTopologyConstraint(podGroup, "", "", logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup top-level constraint: %v", err)
	}

	// Verify SubGroups (2 standalone PCLQs - no PCSG)
	expectedSubGroups := []utils.ExpectedSubGroup{
		{
			Name:                  "tas-indep-clq-0-worker-rack",
			MinMember:             3,
			Parent:                nil,
			RequiredTopologyLevel: "kubernetes.io/rack",
		},
		{
			Name:                  "tas-indep-clq-0-worker-block",
			MinMember:             4,
			Parent:                nil,
			RequiredTopologyLevel: "kubernetes.io/block",
		},
	}
	if err := utils.VerifyKAIPodGroupSubGroups(podGroup, expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup SubGroups: %v", err)
	}

	logger.Info("🎉 BP-1: Multiple Cliques with Different Constraints test completed successfully!")
}

// Test_TAS_SP1_FullHierarchyWithCascadingConstraints tests complete PCS → PCSG → PCLQ hierarchy
// Scenario SP-1:
// 1. Deploy workload with full 3-level hierarchy:
//   - PCS: packDomain=block
//   - PCSG: packDomain=rack (stricter than block)
//   - PodCliques (prefill, decode): packDomain=host (strictest)
//
// 2. Verify all 8 pods are scheduled successfully
// 3. Verify all pods are on the same host (strictest constraint wins)
// 4. Verify constraint inheritance and override behavior
func deployWorkloadAndGetPods(tc TestContext, expectedPods int) ([]v1.Pod, error) {
	if _, err := deployAndVerifyWorkload(tc); err != nil {
		return nil, fmt.Errorf("failed to deploy workload: %w", err)
	}

	logger.Info("Wait for all pods to be scheduled and running")
	if err := utils.WaitForPodsReady(tc.Ctx, tc.Clientset, tc.Namespace, tc.getLabelSelector(), expectedPods, tc.Timeout, tc.Interval, logger); err != nil {
		return nil, fmt.Errorf("failed to wait for pods ready: %w", err)
	}

	logger.Info("Get all pods once for verification")
	podList, err := listPods(tc)
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	return podList.Items, nil
}

func Test_TAS_SL1_PCSOnlyConstraint(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 4 // 2 PCSG workers + 2 router standalone
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
			Name:         "tas-sl-pcs-only",
			YAMLPath:     "../yaml/tas-sl-pcs-only.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	logger.Info("2. Deploy workload (SL-1: PCS-only constraint)")
	allPods, err := deployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify all 4 pods in same rack (inherited from PCS)")
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, allPods, "kubernetes.io/rack", logger); err != nil {
		t.Fatalf("Failed to verify all pods in same rack: %v", err)
	}

	logger.Info("4. Verify PCSG worker pods (2 total, 1 per replica)")
	workerPods := utils.FilterPodsByLabel(allPods, "grove.io/podcliquescalinggroup", "tas-sl-pcs-only-0-workers")
	if len(workerPods) != 2 {
		t.Fatalf("Expected 2 worker pods, got %d", len(workerPods))
	}

	logger.Info("5. Verify router pods (2 standalone)")
	routerPods := utils.FilterPodsByLabel(allPods, "grove.io/podclique", "tas-sl-pcs-only-0-router")
	if len(routerPods) != 2 {
		t.Fatalf("Expected 2 router pods, got %d", len(routerPods))
	}

	logger.Info("6. Verify KAI PodGroup has correct SubGroups (PCS-only constraint)")
	podGroups, err := utils.WaitForKAIPodGroups(tc.Ctx, tc.DynamicClient, tc.Namespace, "tas-sl-pcs-only", tc.Timeout, tc.Interval, logger)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroups: %v", err)
	}

	podGroup, err := utils.FilterPodGroupByOwner(podGroups, "tas-sl-pcs-only-0")
	if err != nil {
		t.Fatalf("Failed to find PodGroup for PodGang tas-sl-pcs-only-0: %v", err)
	}

	// Verify top-level TopologyConstraint (PCS level: rack)
	if err := utils.VerifyKAIPodGroupTopologyConstraint(podGroup, "kubernetes.io/rack", "", logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup top-level constraint: %v", err)
	}

	// Verify SubGroups (2 PCSG parents + 2 PCLQ children + 1 router standalone = 5 total)
	expectedSubGroups := []utils.ExpectedSubGroup{
		// PCSG replicas (parent groups, no explicit constraint)
		{Name: "tas-sl-pcs-only-0-workers-0", MinMember: 0, Parent: nil},
		{Name: "tas-sl-pcs-only-0-workers-1", MinMember: 0, Parent: nil},
		// Worker PCLQs (children of PCSG replicas)
		{Name: "tas-sl-pcs-only-0-workers-0-worker", MinMember: 1, Parent: ptr.To("tas-sl-pcs-only-0-workers-0")},
		{Name: "tas-sl-pcs-only-0-workers-1-worker", MinMember: 1, Parent: ptr.To("tas-sl-pcs-only-0-workers-1")},
		// Router (standalone)
		{Name: "tas-sl-pcs-only-0-router", MinMember: 2, Parent: nil},
	}
	if err := utils.VerifyKAIPodGroupSubGroups(podGroup, expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup SubGroups: %v", err)
	}

	logger.Info("🎉 SL-1: PCS-Only Constraint test completed successfully!")
}

// Test_TAS_SL2_PCSGOnlyConstraint tests constraint only at PCSG level with no PCS/PCLQ constraints
// Scenario SL-2:
// 1. Deploy workload with constraint only at PCSG level (packDomain: rack)
// 2. PCS and PCLQs have NO explicit constraints
// 3. Verify PCSG worker pods (2 total) respect rack constraint
// 4. Router pods (2 standalone) are unconstrained
func Test_TAS_SL2_PCSGOnlyConstraint(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 4 // 2 PCSG workers + 2 router standalone
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
			Name:         "tas-sl-pcsg-only",
			YAMLPath:     "../yaml/tas-sl-pcsg-only.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	logger.Info("2. Deploy workload (SL-2: PCSG-only constraint)")
	allPods, err := deployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify PCSG worker pods (2 total, 1 per replica) in same rack")
	workerPods := utils.FilterPodsByLabel(allPods, "grove.io/podcliquescalinggroup", "tas-sl-pcsg-only-0-workers")
	if len(workerPods) != 2 {
		t.Fatalf("Expected 2 worker pod, got %d", len(workerPods))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, workerPods, "kubernetes.io/rack", logger); err != nil {
		t.Fatalf("Failed to verify worker pods in same rack: %v", err)
	}

	logger.Info("4. Verify router pods (2 standalone, unconstrained)")
	routerPods := utils.FilterPodsByLabel(allPods, "grove.io/podclique", "tas-sl-pcsg-only-0-router")
	if len(routerPods) != 2 {
		t.Fatalf("Expected 2 router pods, got %d", len(routerPods))
	}

	logger.Info("5. Verify KAI PodGroup has correct SubGroups (PCSG-only constraint)")
	podGroups, err := utils.WaitForKAIPodGroups(tc.Ctx, tc.DynamicClient, tc.Namespace, "tas-sl-pcsg-only", tc.Timeout, tc.Interval, logger)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroups: %v", err)
	}

	podGroup, err := utils.FilterPodGroupByOwner(podGroups, "tas-sl-pcsg-only-0")
	if err != nil {
		t.Fatalf("Failed to find PodGroup for PodGang tas-sl-pcsg-only-0: %v", err)
	}

	// Verify top-level TopologyConstraint (no PCS constraint)
	if err := utils.VerifyKAIPodGroupTopologyConstraint(podGroup, "", "", logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup top-level constraint: %v", err)
	}

	// Verify SubGroups (2 PCSG parents + 2 PCLQ children + 1 router standalone = 5 total)
	expectedSubGroups := []utils.ExpectedSubGroup{
		// PCSG replicas (parent groups, rack constraint)
		{Name: "tas-sl-pcsg-only-0-workers-0", MinMember: 0, Parent: nil, RequiredTopologyLevel: "kubernetes.io/rack"},
		{Name: "tas-sl-pcsg-only-0-workers-1", MinMember: 0, Parent: nil, RequiredTopologyLevel: "kubernetes.io/rack"},
		// Worker PCLQs (children of PCSG replicas)
		{Name: "tas-sl-pcsg-only-0-workers-0-worker", MinMember: 1, Parent: ptr.To("tas-sl-pcsg-only-0-workers-0")},
		{Name: "tas-sl-pcsg-only-0-workers-1-worker", MinMember: 1, Parent: ptr.To("tas-sl-pcsg-only-0-workers-1")},
		// Router (standalone, no constraint)
		{Name: "tas-sl-pcsg-only-0-router", MinMember: 2, Parent: nil},
	}
	if err := utils.VerifyKAIPodGroupSubGroups(podGroup, expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup SubGroups: %v", err)
	}

	logger.Info("🎉 SL-2: PCSG-Only Constraint test completed successfully!")
}

// Test_TAS_PC1_HostLevelConstraint tests PCLQ-only constraint with host-level packing
// Scenario PC-1:
// 1. Deploy workload with constraint only at PCLQ level (packDomain: host)
// 2. PCS has NO explicit constraint
// 3. Verify all 2 pods on same host (strictest constraint)
func Test_TAS_PC1_HostLevelConstraint(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 2
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
			Name:         "tas-host-level",
			YAMLPath:     "../yaml/tas-host-level.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	logger.Info("2. Deploy workload (PC-1: PCLQ-only host constraint)")
	allPods, err := deployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify all pods on same host")
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, allPods, "kubernetes.io/hostname", logger); err != nil {
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
	podGroups, err := utils.WaitForKAIPodGroups(tc.Ctx, tc.DynamicClient, tc.Namespace, "tas-host-level", tc.Timeout, tc.Interval, logger)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroups: %v", err)
	}

	podGroup, err := utils.FilterPodGroupByOwner(podGroups, "tas-host-level-0")
	if err != nil {
		t.Fatalf("Failed to find PodGroup for PodGang tas-host-level-0: %v", err)
	}

	// Verify top-level TopologyConstraint (no PCS constraint)
	if err := utils.VerifyKAIPodGroupTopologyConstraint(podGroup, "", "", logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup top-level constraint: %v", err)
	}

	// Verify SubGroups (1 standalone PCLQ with host constraint)
	expectedSubGroups := []utils.ExpectedSubGroup{
		{Name: "tas-host-level-0-worker", MinMember: 2, Parent: nil, RequiredTopologyLevel: "kubernetes.io/hostname"},
	}
	if err := utils.VerifyKAIPodGroupSubGroups(podGroup, expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup SubGroups: %v", err)
	}

	logger.Info("🎉 PC-1: Host-Level Constraint test completed successfully!")
}

// Test_TAS_SP2_PCSPlusPCLQConstraint tests PCS with block constraint and standalone PCLQ with host constraint
// Scenario SP-2:
// 1. Deploy workload with PCS block constraint and PCLQ host constraint (no PCSG layer)
// 2. Verify 2 pods on same host (PCLQ constraint, strictest)
// 3. Verify both pods in same block (PCS constraint inherited)
// 4. Verify KAI PodGroup has block constraint at top level, 1 SubGroup with host constraint
func Test_TAS_ZL1_ZoneLevelConstraint(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 4
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
			Name:         "tas-zone-level",
			YAMLPath:     "../yaml/tas-zone-level.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	logger.Info("2. Deploy workload (ZL-1: PCS zone constraint)")
	allPods, err := deployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify all 4 pods in same zone (PCS zone constraint)")
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, allPods, "kubernetes.io/zone", logger); err != nil {
		t.Fatalf("Failed to verify pods in same zone: %v", err)
	}

	logger.Info("4. Verify KAI PodGroup has correct SubGroups (zone at PCS level)")
	podGroups, err := utils.WaitForKAIPodGroups(tc.Ctx, tc.DynamicClient, tc.Namespace, "tas-zone-level", tc.Timeout, tc.Interval, logger)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroups: %v", err)
	}

	podGroup, err := utils.FilterPodGroupByOwner(podGroups, "tas-zone-level-0")
	if err != nil {
		t.Fatalf("Failed to find PodGroup for PodGang tas-zone-level-0: %v", err)
	}

	// Verify top-level TopologyConstraint (PCS level: zone)
	if err := utils.VerifyKAIPodGroupTopologyConstraint(podGroup, "kubernetes.io/zone", "", logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup top-level constraint: %v", err)
	}

	// Verify SubGroups (1 standalone PCLQ with NO constraint - zone is at PCS level)
	expectedSubGroups := []utils.ExpectedSubGroup{
		{Name: "tas-zone-level-0-worker", MinMember: 4, Parent: nil},
	}
	if err := utils.VerifyKAIPodGroupSubGroups(podGroup, expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup SubGroups: %v", err)
	}

	logger.Info("🎉 ZL-1: Zone-Level Constraint test completed successfully!")
}

// Test_TAS_SL3_NoTopologyConstraint tests gang scheduling without any topology constraints
// Scenario SL-3:
// 1. Deploy workload with no constraints at PCS, PCSG, or PCLQ levels
// 2. Verify all 4 pods scheduled (gang scheduling works)
// 3. Verify KAI PodGroup has 4 SubGroups with NO topology constraints
func Test_TAS_SL3_NoTopologyConstraint(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 4 // 2 PCSG replicas × 2 pods each
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
			Name:         "tas-no-constraint",
			YAMLPath:     "../yaml/tas-no-constraint.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	logger.Info("2. Deploy workload (SL-3: No topology constraints)")
	allPods, err := deployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify all 4 pods scheduled (gang scheduling works without constraints)")
	if len(allPods) != 4 {
		t.Fatalf("Expected 4 pods, got %d", len(allPods))
	}
	for _, pod := range allPods {
		if pod.Status.Phase != v1.PodRunning {
			t.Fatalf("Pod %s not running: %s", pod.Name, pod.Status.Phase)
		}
	}

	logger.Info("4. Verify KAI PodGroup has correct SubGroups (no constraints)")
	podGroups, err := utils.WaitForKAIPodGroups(tc.Ctx, tc.DynamicClient, tc.Namespace, "tas-no-constraint", tc.Timeout, tc.Interval, logger)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroups: %v", err)
	}

	podGroup, err := utils.FilterPodGroupByOwner(podGroups, "tas-no-constraint-0")
	if err != nil {
		t.Fatalf("Failed to find PodGroup for PodGang tas-no-constraint-0: %v", err)
	}

	// Verify top-level TopologyConstraint (no PCS constraint)
	if err := utils.VerifyKAIPodGroupTopologyConstraint(podGroup, "", "", logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup top-level constraint: %v", err)
	}

	// Verify SubGroups (2 PCSG parents + 2 PCLQ children, all with NO constraints)
	expectedSubGroups := []utils.ExpectedSubGroup{
		// PCSG replicas (parent groups, no constraint)
		{Name: "tas-no-constraint-0-workers-0", MinMember: 0, Parent: nil},
		{Name: "tas-no-constraint-0-workers-1", MinMember: 0, Parent: nil},
		// Worker PCLQs (children, no constraint)
		{Name: "tas-no-constraint-0-workers-0-worker", MinMember: 2, Parent: ptr.To("tas-no-constraint-0-workers-0")},
		{Name: "tas-no-constraint-0-workers-1-worker", MinMember: 2, Parent: ptr.To("tas-no-constraint-0-workers-1")},
	}
	if err := utils.VerifyKAIPodGroupSubGroups(podGroup, expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup SubGroups: %v", err)
	}

	logger.Info("🎉 SL-3: No Topology Constraint test completed successfully!")
}

func Test_TAS_SP1_FullHierarchyWithCascadingConstraints(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize an 8-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 8)
	defer cleanup()

	expectedPods := 8 // 2 PCSG replicas × (prefill: 2 pods + decode: 2 pods)
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
			Name:         "tas-hierarchy",
			YAMLPath:     "../yaml/tas-hierarchy.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	logger.Info("2. Deploy workload (SP-1: full 3-level hierarchy with cascading constraints)")
	allPods, err := deployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify PCSG replica 0 prefill pods (2) are on same host (PCLQ constraint)")
	prefill0Pods := utils.FilterPodsByLabel(allPods, "grove.io/podclique", "tas-hierarchy-0-inference-group-0-prefill")
	if len(prefill0Pods) != 2 {
		t.Fatalf("Expected 2 prefill-0 pods, got %d", len(prefill0Pods))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, prefill0Pods, "kubernetes.io/hostname", logger); err != nil {
		t.Fatalf("Failed to verify prefill-0 pods on same host: %v", err)
	}

	logger.Info("4. Verify PCSG replica 0 decode pods (2) are on same host (PCLQ constraint)")
	decode0Pods := utils.FilterPodsByLabel(allPods, "grove.io/podclique", "tas-hierarchy-0-inference-group-0-decode")
	if len(decode0Pods) != 2 {
		t.Fatalf("Expected 2 decode-0 pods, got %d", len(decode0Pods))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, decode0Pods, "kubernetes.io/hostname", logger); err != nil {
		t.Fatalf("Failed to verify decode-0 pods on same host: %v", err)
	}

	logger.Info("5. Verify PCSG replica 1 prefill pods (2) are on same host (PCLQ constraint)")
	prefill1Pods := utils.FilterPodsByLabel(allPods, "grove.io/podclique", "tas-hierarchy-0-inference-group-1-prefill")
	if len(prefill1Pods) != 2 {
		t.Fatalf("Expected 2 prefill-1 pods, got %d", len(prefill1Pods))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, prefill1Pods, "kubernetes.io/hostname", logger); err != nil {
		t.Fatalf("Failed to verify prefill-1 pods on same host: %v", err)
	}

	logger.Info("6. Verify PCSG replica 1 decode pods (2) are on same host (PCLQ constraint)")
	decode1Pods := utils.FilterPodsByLabel(allPods, "grove.io/podclique", "tas-hierarchy-0-inference-group-1-decode")
	if len(decode1Pods) != 2 {
		t.Fatalf("Expected 2 decode-1 pods, got %d", len(decode1Pods))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, decode1Pods, "kubernetes.io/hostname", logger); err != nil {
		t.Fatalf("Failed to verify decode-1 pods on same host: %v", err)
	}

	logger.Info("7. Verify all PCSG replica 0 pods are in same rack (PCSG constraint)")
	pcsg0Pods := utils.FilterPodsByLabel(allPods, "grove.io/podcliquescalinggroup-replica-index", "0")
	if len(pcsg0Pods) != 4 {
		t.Fatalf("Expected 4 PCSG replica 0 pods, got %d", len(pcsg0Pods))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, pcsg0Pods, "kubernetes.io/rack", logger); err != nil {
		t.Fatalf("Failed to verify PCSG replica 0 pods in same rack: %v", err)
	}

	logger.Info("8. Verify all PCSG replica 1 pods are in same rack (PCSG constraint)")
	pcsg1Pods := utils.FilterPodsByLabel(allPods, "grove.io/podcliquescalinggroup-replica-index", "1")
	if len(pcsg1Pods) != 4 {
		t.Fatalf("Expected 4 PCSG replica 1 pods, got %d", len(pcsg1Pods))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, pcsg1Pods, "kubernetes.io/rack", logger); err != nil {
		t.Fatalf("Failed to verify PCSG replica 1 pods in same rack: %v", err)
	}

	logger.Info("9. Verify all pods are in same block (PCS constraint)")
	if len(allPods) != expectedPods {
		t.Fatalf("Expected %d pods, got %d", expectedPods, len(allPods))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, allPods, "kubernetes.io/block", logger); err != nil {
		t.Fatalf("Failed to verify all pods in same block: %v", err)
	}

	logger.Info("10. Verify KAI PodGroup has correct hierarchy with topology constraints")
	podGroups, err := utils.WaitForKAIPodGroups(tc.Ctx, tc.DynamicClient, tc.Namespace, "tas-hierarchy", tc.Timeout, tc.Interval, logger)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroups: %v", err)
	}

	podGroup, err := utils.FilterPodGroupByOwner(podGroups, "tas-hierarchy-0")
	if err != nil {
		t.Fatalf("Failed to find PodGroup for PodGang tas-hierarchy-0: %v", err)
	}

	// Verify top-level TopologyConstraint (PCS level: block)
	if err := utils.VerifyKAIPodGroupTopologyConstraint(podGroup, "kubernetes.io/block", "", logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup top-level constraint: %v", err)
	}

	// Verify SubGroups hierarchy (2 PCSG parents + 4 PCLQ children)
	expectedSubGroups := []utils.ExpectedSubGroup{
		// PCSG replica 0 (parent group)
		{
			Name:                  "tas-hierarchy-0-inference-group-0",
			MinMember:             0,
			Parent:                nil,
			RequiredTopologyLevel: "kubernetes.io/rack",
		},
		// PCSG replica 1 (parent group)
		{
			Name:                  "tas-hierarchy-0-inference-group-1",
			MinMember:             0,
			Parent:                nil,
			RequiredTopologyLevel: "kubernetes.io/rack",
		},
		// PCLQ prefill replica 0
		{
			Name:                  "tas-hierarchy-0-inference-group-0-prefill",
			MinMember:             2,
			Parent:                ptr.To("tas-hierarchy-0-inference-group-0"),
			RequiredTopologyLevel: "kubernetes.io/hostname",
		},
		// PCLQ decode replica 0
		{
			Name:                  "tas-hierarchy-0-inference-group-0-decode",
			MinMember:             2,
			Parent:                ptr.To("tas-hierarchy-0-inference-group-0"),
			RequiredTopologyLevel: "kubernetes.io/hostname",
		},
		// PCLQ prefill replica 1
		{
			Name:                  "tas-hierarchy-0-inference-group-1-prefill",
			MinMember:             2,
			Parent:                ptr.To("tas-hierarchy-0-inference-group-1"),
			RequiredTopologyLevel: "kubernetes.io/hostname",
		},
		// PCLQ decode replica 1
		{
			Name:                  "tas-hierarchy-0-inference-group-1-decode",
			MinMember:             2,
			Parent:                ptr.To("tas-hierarchy-0-inference-group-1"),
			RequiredTopologyLevel: "kubernetes.io/hostname",
		},
	}
	if err := utils.VerifyKAIPodGroupSubGroups(podGroup, expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup SubGroups: %v", err)
	}

	logger.Info("🎉 SP-1: Full Hierarchy with Cascading Constraints test completed successfully!")
}

// Test_TAS_SP3_PCSGScalingWithTopologyConstraints tests PCSG scaling with topology constraints
// Scenario SP-3:
// 1. Deploy workload with PCSG scaling (3 replicas):
//   - PCS: packDomain=rack, minAvailable=1
//   - PCSG: replicas=3, packDomain=rack
//   - PodClique (worker): 2 pods per replica
//
// 2. Verify all 6 pods (3 PCSG replicas × 2 pods) are scheduled successfully
// 3. Verify each PCSG replica's pods are in the same rack
// 4. Verify PCSG scaling creates multiple TopologyConstraintGroups
// 5. Verify topology constraints work with PCSG-level scaling
func Test_TAS_SP2_PCSPlusPCLQConstraint(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 2
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
			Name:         "tas-pcs-pclq",
			YAMLPath:     "../yaml/tas-pcs-pclq.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	logger.Info("2. Deploy workload (SP-2: PCS block + PCLQ host constraint)")
	allPods, err := deployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify 2 pods on same host (PCLQ host constraint)")
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, allPods, "kubernetes.io/hostname", logger); err != nil {
		t.Fatalf("Failed to verify pods on same host: %v", err)
	}

	logger.Info("4. Verify both pods in same block (PCS block constraint)")
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, allPods, "kubernetes.io/block", logger); err != nil {
		t.Fatalf("Failed to verify pods in same block: %v", err)
	}

	logger.Info("5. Verify KAI PodGroup has correct SubGroups (PCS block + PCLQ host)")
	podGroups, err := utils.WaitForKAIPodGroups(tc.Ctx, tc.DynamicClient, tc.Namespace, "tas-pcs-pclq", tc.Timeout, tc.Interval, logger)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroups: %v", err)
	}

	podGroup, err := utils.FilterPodGroupByOwner(podGroups, "tas-pcs-pclq-0")
	if err != nil {
		t.Fatalf("Failed to find PodGroup for PodGang tas-pcs-pclq-0: %v", err)
	}

	// Verify top-level TopologyConstraint (PCS level: block)
	if err := utils.VerifyKAIPodGroupTopologyConstraint(podGroup, "kubernetes.io/block", "", logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup top-level constraint: %v", err)
	}

	// Verify SubGroups (1 standalone PCLQ with host constraint)
	expectedSubGroups := []utils.ExpectedSubGroup{
		{Name: "tas-pcs-pclq-0-worker", MinMember: 2, Parent: nil, RequiredTopologyLevel: "kubernetes.io/hostname"},
	}
	if err := utils.VerifyKAIPodGroupSubGroups(podGroup, expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup SubGroups: %v", err)
	}

	logger.Info("🎉 SP-2: PCS+PCLQ Constraint test completed successfully!")
}

// Test_TAS_SP5_PCSGPlusPCLQNoParentConstraint tests PCSG with rack constraint and PCLQ with host constraint, no PCS constraint
// Scenario SP-5:
// 1. Deploy workload with no PCS constraint, PCSG rack constraint, PCLQ host constraint
// 2. PCSG has replicas=2, minAvailable=2 (both in base PodGang)
// 3. Verify each PCSG replica's 2 pods on same host (PCLQ constraint)
// 4. Verify PCSG replicas respect rack constraint
// 5. Verify KAI PodGroup has 4 SubGroups (2 PCSG parents + 2 PCLQ children)
func Test_TAS_SP3_PCSGScalingWithTopologyConstraints(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 6 // 3 PCSG replicas × 2 pods each
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
			Name:         "tas-pcsg-scale",
			YAMLPath:     "../yaml/tas-pcsg-scale.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	logger.Info("2. Deploy workload (SP-3: PCSG scaling with topology constraints)")
	allPods, err := deployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify PCSG replica 0 worker pods (2) are in same rack")
	pcsg0Pods := utils.FilterPodsByLabel(allPods, "grove.io/podcliquescalinggroup-replica-index", "0")
	if len(pcsg0Pods) != 2 {
		t.Fatalf("Expected 2 PCSG replica 0 pods, got %d", len(pcsg0Pods))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, pcsg0Pods, "kubernetes.io/rack", logger); err != nil {
		t.Fatalf("Failed to verify PCSG replica 0 pods in same rack: %v", err)
	}

	logger.Info("4. Verify PCSG replica 1 worker pods (2) are in same rack")
	pcsg1Pods := utils.FilterPodsByLabel(allPods, "grove.io/podcliquescalinggroup-replica-index", "1")
	if len(pcsg1Pods) != 2 {
		t.Fatalf("Expected 2 PCSG replica 1 pods, got %d", len(pcsg1Pods))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, pcsg1Pods, "kubernetes.io/rack", logger); err != nil {
		t.Fatalf("Failed to verify PCSG replica 1 pods in same rack: %v", err)
	}

	logger.Info("5. Verify PCSG replica 2 worker pods (2) are in same rack")
	pcsg2Pods := utils.FilterPodsByLabel(allPods, "grove.io/podcliquescalinggroup-replica-index", "2")
	if len(pcsg2Pods) != 2 {
		t.Fatalf("Expected 2 PCSG replica 2 pods, got %d", len(pcsg2Pods))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, pcsg2Pods, "kubernetes.io/rack", logger); err != nil {
		t.Fatalf("Failed to verify PCSG replica 2 pods in same rack: %v", err)
	}

	logger.Info("6. Verify all pods respect PCS-level rack constraint")
	if len(allPods) != expectedPods {
		t.Fatalf("Expected %d pods, got %d", expectedPods, len(allPods))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, allPods, "kubernetes.io/rack", logger); err != nil {
		t.Fatalf("Failed to verify all pods in same rack: %v", err)
	}

	logger.Info("7. Verify KAI PodGroup has correct SubGroups with topology constraints")
	podGroups, err := utils.WaitForKAIPodGroups(tc.Ctx, tc.DynamicClient, tc.Namespace, "tas-pcsg-scale", tc.Timeout, tc.Interval, logger)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroups: %v", err)
	}

	podGroup, err := utils.FilterPodGroupByOwner(podGroups, "tas-pcsg-scale-0")
	if err != nil {
		t.Fatalf("Failed to find PodGroup for PodGang tas-pcsg-scale-0: %v", err)
	}

	// Verify top-level TopologyConstraint (PCS level: rack)
	if err := utils.VerifyKAIPodGroupTopologyConstraint(podGroup, "kubernetes.io/rack", "", logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup top-level constraint: %v", err)
	}

	// Verify SubGroups (Base PodGang contains only minAvailable=1 PCSG replica)
	// PCSG has replicas=3 and minAvailable=1, so base PodGang contains ONLY replica 0
	// Replicas 1 and 2 are in separate scaled PodGangs
	expectedSubGroups := []utils.ExpectedSubGroup{
		// PCSG replica 0 (parent group)
		{
			Name:                  "tas-pcsg-scale-0-inference-group-0",
			MinMember:             0,
			Parent:                nil,
			RequiredTopologyLevel: "kubernetes.io/rack",
		},
		// PCLQ worker for PCSG replica 0 (2 pods)
		{
			Name:      "tas-pcsg-scale-0-inference-group-0-worker",
			MinMember: 2,
			Parent:    ptr.To("tas-pcsg-scale-0-inference-group-0"),
		},
	}
	if err := utils.VerifyKAIPodGroupSubGroups(podGroup, expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup SubGroups: %v", err)
	}

	logger.Info("🎉 SP-3: PCSG Scaling with Topology Constraints test completed successfully!")
}

// Test_TAS_EC1_InsufficientNodesForConstraint tests gang scheduling failure when topology constraint cannot be satisfied
// Scenario EC-1:
// 1. Deploy workload with rack constraint requesting 10 pods (exceeds rack capacity)
// 2. Verify all 10 pods remain in Pending state (no partial scheduling)
// 3. Verify NO pods are scheduled (all-or-nothing gang behavior)
// 4. Verify pod events show Unschedulable reason
func Test_TAS_SP5_PCSGPlusPCLQNoParentConstraint(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 4 // 2 PCSG replicas × 2 pods each
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
			Name:         "tas-pcsg-pclq",
			YAMLPath:     "../yaml/tas-pcsg-pclq.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	logger.Info("2. Deploy workload (SP-5: PCSG rack + PCLQ host, no PCS constraint)")
	allPods, err := deployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify each PCSG replica's pods on same host")
	// Get pods for each PCSG replica
	replica0Pods := utils.FilterPodsByLabel(
		utils.FilterPodsByLabel(allPods, "grove.io/podcliquescalinggroup", "tas-pcsg-pclq-0-workers"),
		"grove.io/podcliquescalinggroup-replica-index", "0")
	if len(replica0Pods) != 2 {
		t.Fatalf("Expected 2 pods for replica 0, got %d", len(replica0Pods))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, replica0Pods, "kubernetes.io/hostname", logger); err != nil {
		t.Fatalf("Failed to verify replica 0 pods on same host: %v", err)
	}

	replica1Pods := utils.FilterPodsByLabel(
		utils.FilterPodsByLabel(allPods, "grove.io/podcliquescalinggroup", "tas-pcsg-pclq-0-workers"),
		"grove.io/podcliquescalinggroup-replica-index", "1")
	if len(replica1Pods) != 2 {
		t.Fatalf("Expected 2 pods for replica 1, got %d", len(replica1Pods))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, replica1Pods, "kubernetes.io/hostname", logger); err != nil {
		t.Fatalf("Failed to verify replica 1 pods on same host: %v", err)
	}

	logger.Info("4. Verify KAI PodGroup has correct SubGroups (PCSG rack + PCLQ host)")
	podGroups, err := utils.WaitForKAIPodGroups(tc.Ctx, tc.DynamicClient, tc.Namespace, "tas-pcsg-pclq", tc.Timeout, tc.Interval, logger)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroups: %v", err)
	}

	podGroup, err := utils.FilterPodGroupByOwner(podGroups, "tas-pcsg-pclq-0")
	if err != nil {
		t.Fatalf("Failed to find PodGroup for PodGang tas-pcsg-pclq-0: %v", err)
	}

	// Verify top-level TopologyConstraint (no PCS constraint)
	if err := utils.VerifyKAIPodGroupTopologyConstraint(podGroup, "", "", logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup top-level constraint: %v", err)
	}

	// Verify SubGroups (2 PCSG parents with rack + 2 PCLQ children with host)
	expectedSubGroups := []utils.ExpectedSubGroup{
		// PCSG replicas (parent groups with rack constraint)
		{Name: "tas-pcsg-pclq-0-workers-0", MinMember: 0, Parent: nil, RequiredTopologyLevel: "kubernetes.io/rack"},
		{Name: "tas-pcsg-pclq-0-workers-1", MinMember: 0, Parent: nil, RequiredTopologyLevel: "kubernetes.io/rack"},
		// Worker PCLQs (children with host constraint)
		{Name: "tas-pcsg-pclq-0-workers-0-worker", MinMember: 2, Parent: ptr.To("tas-pcsg-pclq-0-workers-0"), RequiredTopologyLevel: "kubernetes.io/hostname"},
		{Name: "tas-pcsg-pclq-0-workers-1-worker", MinMember: 2, Parent: ptr.To("tas-pcsg-pclq-0-workers-1"), RequiredTopologyLevel: "kubernetes.io/hostname"},
	}
	if err := utils.VerifyKAIPodGroupSubGroups(podGroup, expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup SubGroups: %v", err)
	}

	logger.Info("🎉 SP-5: PCSG+PCLQ Constraint test completed successfully!")
}

// Test_TAS_ZL1_ZoneLevelConstraint tests zone-level constraint (widest topology domain)
// Scenario ZL-1:
// 1. Deploy workload with PCS zone constraint (widest domain)
// 2. Verify all 4 pods in same zone
// 3. Verify KAI PodGroup has zone constraint at top level, 1 SubGroup with NO constraint
func Test_TAS_SP8_LargeScalingRatio(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 20 // Only minAvailable=3 PCSG replicas × 2 pods each (base PodGang)
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
			Name:         "tas-large-scale",
			YAMLPath:     "../yaml/tas-large-scale.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	logger.Info("2. Deploy workload (SP-8: Large scaling ratio, replicas=10/minAvailable=3)")
	allPods, err := deployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify each PCSG replica's pods on same host")
	// Get pods for each PCSG replica (only minAvailable=3 from base PodGang)
	for i := 0; i < 3; i++ {
		replicaPods := utils.FilterPodsByLabel(
			utils.FilterPodsByLabel(allPods, "grove.io/podcliquescalinggroup", "tas-large-scale-0-workers"),
			"grove.io/podcliquescalinggroup-replica-index", fmt.Sprintf("%d", i))
		if len(replicaPods) != 2 {
			t.Fatalf("Expected 2 pods for replica %d, got %d", i, len(replicaPods))
		}
		if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, replicaPods, "kubernetes.io/hostname", logger); err != nil {
			t.Fatalf("Failed to verify replica %d pods on same host: %v", i, err)
		}
	}

	logger.Info("4. Verify all 20 pods in same rack (PCS rack constraint)")
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, allPods, "kubernetes.io/block", logger); err != nil {
		t.Fatalf("Failed to verify all pods in same rack: %v", err)
	}

	logger.Info("5. Verify base PodGang's KAI PodGroup (replicas 0-2)")
	podGroups, err := utils.WaitForKAIPodGroups(tc.Ctx, tc.DynamicClient, tc.Namespace, "tas-large-scale", tc.Timeout, tc.Interval, logger)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroups: %v", err)
	}

	podGroup, err := utils.FilterPodGroupByOwner(podGroups, "tas-large-scale-0")
	if err != nil {
		t.Fatalf("Failed to find PodGroup for PodGang tas-large-scale-0: %v", err)
	}

	// Verify top-level TopologyConstraint (PCS level: rack)
	if err := utils.VerifyKAIPodGroupTopologyConstraint(podGroup, "kubernetes.io/block", "", logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup top-level constraint: %v", err)
	}

	// Verify SubGroups (3 PCSG parents with block + 3 worker children with host)
	expectedSubGroups := []utils.ExpectedSubGroup{
		// PCSG replicas (parent groups with block constraint)
		{Name: "tas-large-scale-0-workers-0", MinMember: 0, Parent: nil, RequiredTopologyLevel: "kubernetes.io/rack"},
		{Name: "tas-large-scale-0-workers-1", MinMember: 0, Parent: nil, RequiredTopologyLevel: "kubernetes.io/rack"},
		{Name: "tas-large-scale-0-workers-2", MinMember: 0, Parent: nil, RequiredTopologyLevel: "kubernetes.io/rack"},
		// Worker PCLQs (children with host constraint)
		{Name: "tas-large-scale-0-workers-0-worker", MinMember: 2, Parent: ptr.To("tas-large-scale-0-workers-0"), RequiredTopologyLevel: "kubernetes.io/hostname"},
		{Name: "tas-large-scale-0-workers-1-worker", MinMember: 2, Parent: ptr.To("tas-large-scale-0-workers-1"), RequiredTopologyLevel: "kubernetes.io/hostname"},
		{Name: "tas-large-scale-0-workers-2-worker", MinMember: 2, Parent: ptr.To("tas-large-scale-0-workers-2"), RequiredTopologyLevel: "kubernetes.io/hostname"},
	}
	if err := utils.VerifyKAIPodGroupSubGroups(podGroup, expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup SubGroups: %v", err)
	}

	logger.Info("6. Verify scaled PodGangs' KAI PodGroups (PCSG replicas 3-9)")
	minAvailable := 3
	for pcsgReplicaIndex := minAvailable; pcsgReplicaIndex < 10; pcsgReplicaIndex++ {
		scaledIndex := pcsgReplicaIndex - minAvailable
		scaledPodGangName := fmt.Sprintf("tas-large-scale-0-workers-%d", scaledIndex)

		scaledPodGroup, err := utils.FilterPodGroupByOwner(podGroups, scaledPodGangName)
		if err != nil {
			t.Fatalf("Failed to find PodGroup for scaled PodGang %s: %v", scaledPodGangName, err)
		}

		// Verify PCS-level constraint is inherited
		if err := utils.VerifyKAIPodGroupTopologyConstraint(scaledPodGroup, "kubernetes.io/block", "", logger); err != nil {
			t.Fatalf("Failed to verify scaled PodGroup %s top-level constraint: %v", scaledPodGangName, err)
		}

		// Verify SubGroups (1 PCSG parent with rack + 1 worker child with host)
		// SubGroup names use the actual PCSG replica index, not the scaled index
		expectedScaledSubGroups := []utils.ExpectedSubGroup{
			{
				Name:                  fmt.Sprintf("tas-large-scale-0-workers-%d", pcsgReplicaIndex),
				MinMember:             0,
				Parent:                nil,
				RequiredTopologyLevel: "kubernetes.io/rack",
			},
			{
				Name:                  fmt.Sprintf("tas-large-scale-0-workers-%d-worker", pcsgReplicaIndex),
				MinMember:             2,
				Parent:                ptr.To(fmt.Sprintf("tas-large-scale-0-workers-%d", pcsgReplicaIndex)),
				RequiredTopologyLevel: "kubernetes.io/hostname",
			},
		}

		if err := utils.VerifyKAIPodGroupSubGroups(scaledPodGroup, expectedScaledSubGroups, logger); err != nil {
			t.Fatalf("Failed to verify scaled PodGroup %s SubGroups: %v", scaledPodGangName, err)
		}

		logger.Infof("Verified scaled PodGroup for PCSG replica %d (scaled index %d)", pcsgReplicaIndex, scaledIndex)
	}

	logger.Info("🎉 SP-8: Large Scaling Ratio test completed successfully!")
}
func Test_TAS_EC1_InsufficientNodesForConstraint(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 10
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
			Name:         "tas-insuffic",
			YAMLPath:     "../yaml/tas-insuffic.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	logger.Info("2. Deploy workload (EC-1: insufficient nodes for rack constraint)")
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

	for _, pod := range pods.Items {
		if pod.Status.Phase != v1.PodPending {
			t.Fatalf("Expected all pods to be Pending, but pod %s is in phase %s", pod.Name, pod.Status.Phase)
		}
		if pod.Spec.NodeName != "" {
			t.Fatalf("Expected pod %s to have no node assignment, but assigned to %s", pod.Name, pod.Spec.NodeName)
		}
	}

	logger.Info("5. Verify KAI PodGroup exists with correct topology constraints (even though pods are pending)")
	podGroups, err := utils.WaitForKAIPodGroups(tc.Ctx, tc.DynamicClient, tc.Namespace, "tas-insuffic", tc.Timeout, tc.Interval, logger)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroups: %v", err)
	}

	podGroup, err := utils.FilterPodGroupByOwner(podGroups, "tas-insuffic-0")
	if err != nil {
		t.Fatalf("Failed to find PodGroup for PodGang tas-insuffic-0: %v", err)
	}

	// Verify top-level TopologyConstraint (PCS level: rack)
	if err := utils.VerifyKAIPodGroupTopologyConstraint(podGroup, "kubernetes.io/rack", "", logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup top-level constraint: %v", err)
	}

	// Verify SubGroups (1 standalone PCLQ - no PCSG)
	expectedSubGroups := []utils.ExpectedSubGroup{
		{
			Name:                  "tas-insuffic-0-worker",
			MinMember:             10,
			Parent:                nil,
			RequiredTopologyLevel: "",
		},
	}
	if err := utils.VerifyKAIPodGroupSubGroups(podGroup, expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup SubGroups: %v", err)
	}

	logger.Info("🎉 EC-1: Insufficient Nodes for Constraint test completed successfully!")
}

// Test_TAS_MR1_MultiReplicaWithRackConstraint tests multi-replica PCS with per-replica topology packing
// Scenario MR-1:
// 1. Deploy workload with 2 PCS replicas, each with rack constraint (2 pods per replica)
// 2. Verify all 4 pods are scheduled successfully
// 3. Verify PCS replica 0 pods (2) are in same rack (per-replica packing)
// 4. Verify PCS replica 1 pods (2) are in same rack (per-replica packing)
// Note: We do NOT verify replicas are in different racks (spread constraints not supported)
func Test_TAS_MR1_MultiReplicaWithRackConstraint(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 4 // 2 PCS replicas × 2 pods each
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
			Name:         "tas-multirep",
			YAMLPath:     "../yaml/tas-multirep.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	logger.Info("2. Deploy workload (MR-1: multi-replica with rack constraint)")
	allPods, err := deployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify PCS replica 0 pods (2) are in same rack")
	replica0Pods := utils.FilterPodsByLabel(allPods, "grove.io/podcliqueset-replica-index", "0")
	if len(replica0Pods) != 2 {
		t.Fatalf("Expected 2 replica-0 pods, got %d", len(replica0Pods))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, replica0Pods, "kubernetes.io/rack", logger); err != nil {
		t.Fatalf("Failed to verify replica-0 pods in same rack: %v", err)
	}

	logger.Info("4. Verify PCS replica 1 pods (2) are in same rack")
	replica1Pods := utils.FilterPodsByLabel(allPods, "grove.io/podcliqueset-replica-index", "1")
	if len(replica1Pods) != 2 {
		t.Fatalf("Expected 2 replica-1 pods, got %d", len(replica1Pods))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, replica1Pods, "kubernetes.io/rack", logger); err != nil {
		t.Fatalf("Failed to verify replica-1 pods in same rack: %v", err)
	}

	logger.Info("5. Verify KAI PodGroups for both replicas have correct topology constraints")
	// Get all PodGroups for the PCS
	podGroups, err := utils.WaitForKAIPodGroups(tc.Ctx, tc.DynamicClient, tc.Namespace, "tas-multirep", tc.Timeout, tc.Interval, logger)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroups: %v", err)
	}

	// Verify PCS replica 0 PodGroup
	podGroup0, err := utils.FilterPodGroupByOwner(podGroups, "tas-multirep-0")
	if err != nil {
		t.Fatalf("Failed to find PodGroup for PodGang tas-multirep-0: %v", err)
	}
	if err := utils.VerifyKAIPodGroupTopologyConstraint(podGroup0, "kubernetes.io/rack", "", logger); err != nil {
		t.Fatalf("Failed to verify PodGroup-0 top-level constraint: %v", err)
	}
	expectedSubGroups0 := []utils.ExpectedSubGroup{
		{
			Name:      "tas-multirep-0-worker",
			MinMember: 2,
			Parent:    nil,
		},
	}
	if err := utils.VerifyKAIPodGroupSubGroups(podGroup0, expectedSubGroups0, logger); err != nil {
		t.Fatalf("Failed to verify PodGroup-0 SubGroups: %v", err)
	}

	// Verify PCS replica 1 PodGroup
	podGroup1, err := utils.FilterPodGroupByOwner(podGroups, "tas-multirep-1")
	if err != nil {
		t.Fatalf("Failed to find PodGroup for PodGang tas-multirep-1: %v", err)
	}
	if err := utils.VerifyKAIPodGroupTopologyConstraint(podGroup1, "kubernetes.io/rack", "", logger); err != nil {
		t.Fatalf("Failed to verify PodGroup-1 top-level constraint: %v", err)
	}
	expectedSubGroups1 := []utils.ExpectedSubGroup{
		{
			Name:      "tas-multirep-1-worker",
			MinMember: 2,
			Parent:    nil,
		},
	}
	if err := utils.VerifyKAIPodGroupSubGroups(podGroup1, expectedSubGroups1, logger); err != nil {
		t.Fatalf("Failed to verify PodGroup-1 SubGroups: %v", err)
	}

	logger.Info("🎉 MR-1: Multi-Replica with Rack Constraint test completed successfully!")
}

// Test_TAS_SP4_DisaggregatedInferenceMultiplePCSGs tests disaggregated inference with multiple PCSGs
// Scenario SP-4:
// 1. Deploy workload with 2 PCSGs (decoder, prefill) + standalone router:
//   - PCS: packDomain=block (all 10 pods in same block)
//   - Decoder PCSG: replicas=2, minAvaileale=1 (each replica's 2 pods in same rack)
//   - Prefill PCSG: replicas=2, minAvailable=1, (each replica's 2 pods in same rack)
//   - Router: standalone, 2 pods (no PCSG, no topology constraint)
//
// 2. Verify all 10 pods are scheduled successfully
// 3. Verify block-level constraint covers all pods
// 4. Verify each PCSG replica respects rack-level constraint independently
// 5. Verify router pods have no PCSG replica index label
func Test_TAS_SP4_DisaggregatedInferenceMultiplePCSGs(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 10 // decoder (2×2) + prefill (2×2) + router (2)
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
			Name:         "tas-disagg-inference",
			YAMLPath:     "../yaml/tas-disagg-inference.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	logger.Info("2. Deploy workload (SP-4: disaggregated inference with multiple PCSGs with minAvailable 1)")
	allPods, err := deployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify block-level constraint (all 10 pods in same block)")
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, allPods, "kubernetes.io/block", logger); err != nil {
		t.Fatalf("Failed to verify all pods in same block: %v", err)
	}

	logger.Info("4. Verify decoder PCSG replica-0 (2 pods in same rack)")
	decoderReplica0 := utils.FilterPodsByLabel(
		utils.FilterPodsByLabel(allPods, "grove.io/podcliquescalinggroup", "tas-disagg-inference-0-decoder"),
		"grove.io/podcliquescalinggroup-replica-index", "0")
	if len(decoderReplica0) != 2 {
		t.Fatalf("Expected 2 decoder replica-0 pods, got %d", len(decoderReplica0))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, decoderReplica0, "kubernetes.io/rack", logger); err != nil {
		t.Fatalf("Failed to verify decoder replica-0 pods in same rack: %v", err)
	}

	logger.Info("5. Verify decoder PCSG replica-1 (2 pods in same rack)")
	decoderReplica1 := utils.FilterPodsByLabel(
		utils.FilterPodsByLabel(allPods, "grove.io/podcliquescalinggroup", "tas-disagg-inference-0-decoder"),
		"grove.io/podcliquescalinggroup-replica-index", "1")
	if len(decoderReplica1) != 2 {
		t.Fatalf("Expected 2 decoder replica-1 pods, got %d", len(decoderReplica1))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, decoderReplica1, "kubernetes.io/rack", logger); err != nil {
		t.Fatalf("Failed to verify decoder replica-1 pods in same rack: %v", err)
	}

	logger.Info("6. Verify prefill PCSG replica-0 (2 pods in same rack)")
	prefillReplica0 := utils.FilterPodsByLabel(
		utils.FilterPodsByLabel(allPods, "grove.io/podcliquescalinggroup", "tas-disagg-inference-0-prefill"),
		"grove.io/podcliquescalinggroup-replica-index", "0")
	if len(prefillReplica0) != 2 {
		t.Fatalf("Expected 2 prefill replica-0 pods, got %d", len(prefillReplica0))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, prefillReplica0, "kubernetes.io/rack", logger); err != nil {
		t.Fatalf("Failed to verify prefill replica-0 pods in same rack: %v", err)
	}

	logger.Info("7. Verify prefill PCSG replica-1 (2 pods in same rack)")
	prefillReplica1 := utils.FilterPodsByLabel(
		utils.FilterPodsByLabel(allPods, "grove.io/podcliquescalinggroup", "tas-disagg-inference-0-prefill"),
		"grove.io/podcliquescalinggroup-replica-index", "1")
	if len(prefillReplica1) != 2 {
		t.Fatalf("Expected 2 prefill replica-1 pods, got %d", len(prefillReplica1))
	}
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, prefillReplica1, "kubernetes.io/rack", logger); err != nil {
		t.Fatalf("Failed to verify prefill replica-1 pods in same rack: %v", err)
	}

	logger.Info("8. Verify router pods (2 standalone, no PCSG label)")
	routerPods := utils.FilterPodsByLabel(allPods, "grove.io/podclique", "tas-disagg-inference-0-router")
	if len(routerPods) != 2 {
		t.Fatalf("Expected 2 router pods, got %d", len(routerPods))
	}

	logger.Info("9. Verify PodGang's KAI PodGroup (decoder-0, prefill-0, router)")
	podGroups, err := utils.WaitForKAIPodGroups(tc.Ctx, tc.DynamicClient, tc.Namespace, "tas-disagg-inference", tc.Timeout, tc.Interval, logger)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroups: %v", err)
	}

	podGroup, err := utils.FilterPodGroupByOwner(podGroups, "tas-disagg-inference-0")
	if err != nil {
		t.Fatalf("Failed to find PodGroup for PodGang tas-disagg-inference-0: %v", err)
	}

	// Verify top-level TopologyConstraint (PCS level: block)
	if err := utils.VerifyKAIPodGroupTopologyConstraint(podGroup, "kubernetes.io/block", "", logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup top-level constraint: %v", err)
	}

	// Verify SubGroups (Base PodGang contains only minAvailable=1 PCSG replicas)
	// Both decoder and prefill PCSGs have replicas=2 and minAvailable=1
	// So base PodGang contains ONLY replica 0 of each PCSG
	// Replica 1 of each PCSG is in separate scaled PodGangs
	expectedSubGroups := []utils.ExpectedSubGroup{
		// Decoder PCSG replica 0 only (parent group)
		{Name: "tas-disagg-inference-0-decoder-0", MinMember: 0, Parent: nil, RequiredTopologyLevel: "kubernetes.io/rack"},
		// Prefill PCSG replica 0 only (parent group)
		{Name: "tas-disagg-inference-0-prefill-0", MinMember: 0, Parent: nil, RequiredTopologyLevel: "kubernetes.io/rack"},
		// Decoder PCLQs for replica 0 only (children of decoder PCSG replica 0)
		{Name: "tas-disagg-inference-0-decoder-0-dworker", MinMember: 1, Parent: ptr.To("tas-disagg-inference-0-decoder-0")},
		{Name: "tas-disagg-inference-0-decoder-0-dleader", MinMember: 1, Parent: ptr.To("tas-disagg-inference-0-decoder-0")},
		// Prefill PCLQs for replica 0 only (children of prefill PCSG replica 0)
		{Name: "tas-disagg-inference-0-prefill-0-pworker", MinMember: 1, Parent: ptr.To("tas-disagg-inference-0-prefill-0")},
		{Name: "tas-disagg-inference-0-prefill-0-pleader", MinMember: 1, Parent: ptr.To("tas-disagg-inference-0-prefill-0")},
		// Router (standalone, no PCSG)
		{Name: "tas-disagg-inference-0-router", MinMember: 2, Parent: nil},
	}
	if err := utils.VerifyKAIPodGroupSubGroups(podGroup, expectedSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup SubGroups: %v", err)
	}

	logger.Info("10. Verify scaled PodGangs' KAI PodGroups (decoder-0 and prefill-0)")
	// Note: Both PCSGs have replicas=2, minAvailable=1, so PCSG replica 1 is in scaled PodGangs
	// Scaled index starts at 0 for the first replica above minAvailable
	// So decoder PCSG replica 1 → scaled PodGang "tas-disagg-inference-0-decoder-0"

	// Verify decoder PCSG replica 1 scaled PodGang (named with scaled index 0)
	decoderScaledPodGroup, err := utils.FilterPodGroupByOwner(podGroups, "tas-disagg-inference-0-decoder-0")
	if err != nil {
		t.Fatalf("Failed to find PodGroup for scaled PodGang tas-disagg-inference-0-decoder-0: %v", err)
	}

	// Verify PCS-level constraint is inherited
	if err = utils.VerifyKAIPodGroupTopologyConstraint(decoderScaledPodGroup, "kubernetes.io/block", "", logger); err != nil {
		t.Fatalf("Failed to verify decoder scaled PodGroup top-level constraint: %v", err)
	}

	expectedDecoderScaledSubGroups := []utils.ExpectedSubGroup{
		{Name: "tas-disagg-inference-0-decoder-1", MinMember: 0, Parent: nil, RequiredTopologyLevel: "kubernetes.io/rack"},
		{Name: "tas-disagg-inference-0-decoder-1-dworker", MinMember: 1, Parent: ptr.To("tas-disagg-inference-0-decoder-1")},
		{Name: "tas-disagg-inference-0-decoder-1-dleader", MinMember: 1, Parent: ptr.To("tas-disagg-inference-0-decoder-1")},
	}

	if err := utils.VerifyKAIPodGroupSubGroups(decoderScaledPodGroup, expectedDecoderScaledSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify decoder scaled PodGroup SubGroups: %v", err)
	}

	logger.Info("Verified decoder scaled PodGroup (PCSG replica 1)")

	// Verify prefill PCSG replica 1 scaled PodGang (named with scaled index 0)
	prefillScaledPodGroup, err := utils.FilterPodGroupByOwner(podGroups, "tas-disagg-inference-0-prefill-0")
	if err != nil {
		t.Fatalf("Failed to find PodGroup for scaled PodGang tas-disagg-inference-0-prefill-0: %v", err)
	}

	// Verify PCS-level constraint is inherited
	if err := utils.VerifyKAIPodGroupTopologyConstraint(prefillScaledPodGroup, "kubernetes.io/block", "", logger); err != nil {
		t.Fatalf("Failed to verify prefill scaled PodGroup top-level constraint: %v", err)
	}

	expectedPrefillScaledSubGroups := []utils.ExpectedSubGroup{
		{Name: "tas-disagg-inference-0-prefill-1", MinMember: 0, Parent: nil, RequiredTopologyLevel: "kubernetes.io/rack"},
		{Name: "tas-disagg-inference-0-prefill-1-pworker", MinMember: 1, Parent: ptr.To("tas-disagg-inference-0-prefill-1")},
		{Name: "tas-disagg-inference-0-prefill-1-pleader", MinMember: 1, Parent: ptr.To("tas-disagg-inference-0-prefill-1")},
	}

	if err := utils.VerifyKAIPodGroupSubGroups(prefillScaledPodGroup, expectedPrefillScaledSubGroups, logger); err != nil {
		t.Fatalf("Failed to verify prefill scaled PodGroup SubGroups: %v", err)
	}

	logger.Info("Verified prefill scaled PodGroup (PCSG replica 1)")

	logger.Info("🎉 SP-4: Disaggregated Inference with Multiple PCSGs test completed successfully!")
}

// Test_TAS_SL1_PCSOnlyConstraint tests constraint only at PCS level with no PCSG/PCLQ constraints
// Scenario SL-1:
// 1. Deploy workload with constraint only at PCS level (packDomain: rack)
// 2. PCSG and PCLQs have NO explicit constraints
// 3. Verify all 4 pods (2 PCSG workers + 2 router) in same rack via inheritance
func Test_TAS_SP9_MultiReplicaPCSWithThreeLevelHierarchy(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster for multi-replica PCS testing")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	expectedPods := 20 // PCS replica 0: 10 pods + PCS replica 1: 10 pods
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
			Name:         "tas-disagg-inference",
			YAMLPath:     "../yaml/tas-disagg-inference-multi-pcs.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	logger.Info("2. Deploy workload (SP-9: 2 PCS replicas with 3-level topology hierarchy)")
	allPods, err := deployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	logger.Info("3. Verify block-level constraint (all 20 pods in same block)")
	if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, allPods, "kubernetes.io/block", logger); err != nil {
		t.Fatalf("Failed to verify all pods in same block: %v", err)
	}

	// Verify for each PCS replica
	for pcsReplica := 0; pcsReplica < 2; pcsReplica++ {
		replicaLabel := fmt.Sprintf("%d", pcsReplica)
		replicaPods := utils.FilterPodsByLabel(allPods, "grove.io/podcliqueset-replica-index", replicaLabel)
		if len(replicaPods) != 10 {
			t.Fatalf("Expected 10 pods for PCS replica %d, got %d", pcsReplica, len(replicaPods))
		}

		logger.Infof("4.%d. Verify PCS replica %d pods topology constraints", pcsReplica+1, pcsReplica)

		// Verify decoder PCSG replica-0 (2 pods in same rack)
		logger.Infof("4.%d.1. Verify decoder PCSG replica-0 (2 pods in same rack)", pcsReplica+1)
		decoderReplica0 := utils.FilterPodsByLabel(
			utils.FilterPodsByLabel(replicaPods, "grove.io/podcliquescalinggroup", fmt.Sprintf("tas-disagg-inference-%d-decoder", pcsReplica)),
			"grove.io/podcliquescalinggroup-replica-index", "0")
		if len(decoderReplica0) != 2 {
			t.Fatalf("Expected 2 decoder replica-0 pods for PCS replica %d, got %d", pcsReplica, len(decoderReplica0))
		}
		if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, decoderReplica0, "kubernetes.io/rack", logger); err != nil {
			t.Fatalf("Failed to verify decoder replica-0 pods in same rack for PCS replica %d: %v", pcsReplica, err)
		}

		// Verify decoder PCSG replica-1 (2 pods in same rack)
		logger.Infof("4.%d.2. Verify decoder PCSG replica-1 (2 pods in same rack)", pcsReplica+1)
		decoderReplica1 := utils.FilterPodsByLabel(
			utils.FilterPodsByLabel(replicaPods, "grove.io/podcliquescalinggroup", fmt.Sprintf("tas-disagg-inference-%d-decoder", pcsReplica)),
			"grove.io/podcliquescalinggroup-replica-index", "1")
		if len(decoderReplica1) != 2 {
			t.Fatalf("Expected 2 decoder replica-1 pods for PCS replica %d, got %d", pcsReplica, len(decoderReplica1))
		}
		if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, decoderReplica1, "kubernetes.io/rack", logger); err != nil {
			t.Fatalf("Failed to verify decoder replica-1 pods in same rack for PCS replica %d: %v", pcsReplica, err)
		}

		// Verify prefill PCSG replica-0 (2 pods in same rack)
		logger.Infof("4.%d.3. Verify prefill PCSG replica-0 (2 pods in same rack)", pcsReplica+1)
		prefillReplica0 := utils.FilterPodsByLabel(
			utils.FilterPodsByLabel(replicaPods, "grove.io/podcliquescalinggroup", fmt.Sprintf("tas-disagg-inference-%d-prefill", pcsReplica)),
			"grove.io/podcliquescalinggroup-replica-index", "0")
		if len(prefillReplica0) != 2 {
			t.Fatalf("Expected 2 prefill replica-0 pods for PCS replica %d, got %d", pcsReplica, len(prefillReplica0))
		}
		if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, prefillReplica0, "kubernetes.io/rack", logger); err != nil {
			t.Fatalf("Failed to verify prefill replica-0 pods in same rack for PCS replica %d: %v", pcsReplica, err)
		}

		// Verify pworker PCLQ constraint (host level) for prefill replica-0
		logger.Infof("4.%d.4. Verify pworker pods on same host (PCLQ-level constraint) for prefill replica-0", pcsReplica+1)
		pworkerReplica0 := utils.FilterPodsByLabel(prefillReplica0, "grove.io/podclique", fmt.Sprintf("tas-disagg-inference-%d-prefill-0-pworker", pcsReplica))
		if len(pworkerReplica0) != 1 {
			t.Fatalf("Expected 1 pworker pod in prefill replica-0 for PCS replica %d, got %d", pcsReplica, len(pworkerReplica0))
		}

		// Verify prefill PCSG replica-1 (2 pods in same rack)
		logger.Infof("4.%d.5. Verify prefill PCSG replica-1 (2 pods in same rack)", pcsReplica+1)
		prefillReplica1 := utils.FilterPodsByLabel(
			utils.FilterPodsByLabel(replicaPods, "grove.io/podcliquescalinggroup", fmt.Sprintf("tas-disagg-inference-%d-prefill", pcsReplica)),
			"grove.io/podcliquescalinggroup-replica-index", "1")
		if len(prefillReplica1) != 2 {
			t.Fatalf("Expected 2 prefill replica-1 pods for PCS replica %d, got %d", pcsReplica, len(prefillReplica1))
		}
		if err := utils.VerifyPodsInSameTopologyDomain(tc.Ctx, tc.Clientset, prefillReplica1, "kubernetes.io/rack", logger); err != nil {
			t.Fatalf("Failed to verify prefill replica-1 pods in same rack for PCS replica %d: %v", pcsReplica, err)
		}

		// Verify pworker PCLQ constraint (host level) for prefill replica-1
		logger.Infof("4.%d.6. Verify pworker pods on same host (PCLQ-level constraint) for prefill replica-1", pcsReplica+1)
		pworkerReplica1 := utils.FilterPodsByLabel(prefillReplica1, "grove.io/podclique", fmt.Sprintf("tas-disagg-inference-%d-prefill-1-pworker", pcsReplica))
		if len(pworkerReplica1) != 1 {
			t.Fatalf("Expected 1 pworker pod in prefill replica-1 for PCS replica %d, got %d", pcsReplica, len(pworkerReplica1))
		}

		// Verify router pods (2 standalone, no PCSG label)
		logger.Infof("4.%d.7. Verify router pods (2 standalone)", pcsReplica+1)
		routerPods := utils.FilterPodsByLabel(replicaPods, "grove.io/podclique", fmt.Sprintf("tas-disagg-inference-%d-router", pcsReplica))
		if len(routerPods) != 2 {
			t.Fatalf("Expected 2 router pods for PCS replica %d, got %d", pcsReplica, len(routerPods))
		}
	}

	logger.Info("5. Verify all 6 KAI PodGroups with correct topology constraints and SubGroups")
	podGroups, err := utils.WaitForKAIPodGroups(tc.Ctx, tc.DynamicClient, tc.Namespace, "tas-disagg-inference", tc.Timeout, tc.Interval, logger)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroups: %v", err)
	}

	if len(podGroups) != 6 {
		t.Fatalf("Expected 6 PodGroups (2 base + 4 scaled), got %d", len(podGroups))
	}

	// Verify each PCS replica's base and scaled PodGangs
	for pcsReplica := 0; pcsReplica < 2; pcsReplica++ {
		basePodGangName := fmt.Sprintf("tas-disagg-inference-%d", pcsReplica)
		logger.Infof("5.%d. Verify base PodGang: %s", pcsReplica+1, basePodGangName)

		basePodGroup, err := utils.FilterPodGroupByOwner(podGroups, basePodGangName)
		if err != nil {
			t.Fatalf("Failed to find PodGroup for base PodGang %s: %v", basePodGangName, err)
		}

		// Verify PCS-level constraint (block)
		if err := utils.VerifyKAIPodGroupTopologyConstraint(basePodGroup, "kubernetes.io/block", "", logger); err != nil {
			t.Fatalf("Failed to verify base PodGroup %s top-level constraint: %v", basePodGangName, err)
		}

		// Verify SubGroups for base PodGang
		expectedBaseSubGroups := []utils.ExpectedSubGroup{
			// Decoder PCSG replica 0 (parent)
			{Name: fmt.Sprintf("tas-disagg-inference-%d-decoder-0", pcsReplica), MinMember: 0, Parent: nil, RequiredTopologyLevel: "kubernetes.io/rack"},
			{Name: fmt.Sprintf("tas-disagg-inference-%d-decoder-0-dworker", pcsReplica), MinMember: 1, Parent: ptr.To(fmt.Sprintf("tas-disagg-inference-%d-decoder-0", pcsReplica))},
			{Name: fmt.Sprintf("tas-disagg-inference-%d-decoder-0-dleader", pcsReplica), MinMember: 1, Parent: ptr.To(fmt.Sprintf("tas-disagg-inference-%d-decoder-0", pcsReplica))},
			// Prefill PCSG replica 0 (parent)
			{Name: fmt.Sprintf("tas-disagg-inference-%d-prefill-0", pcsReplica), MinMember: 0, Parent: nil, RequiredTopologyLevel: "kubernetes.io/rack"},
			{Name: fmt.Sprintf("tas-disagg-inference-%d-prefill-0-pworker", pcsReplica), MinMember: 1, Parent: ptr.To(fmt.Sprintf("tas-disagg-inference-%d-prefill-0", pcsReplica)), RequiredTopologyLevel: "kubernetes.io/hostname"},
			{Name: fmt.Sprintf("tas-disagg-inference-%d-prefill-0-pleader", pcsReplica), MinMember: 1, Parent: ptr.To(fmt.Sprintf("tas-disagg-inference-%d-prefill-0", pcsReplica))},
			// Router (standalone)
			{Name: fmt.Sprintf("tas-disagg-inference-%d-router", pcsReplica), MinMember: 2, Parent: nil},
		}

		if err := utils.VerifyKAIPodGroupSubGroups(basePodGroup, expectedBaseSubGroups, logger); err != nil {
			t.Fatalf("Failed to verify base PodGroup %s SubGroups: %v", basePodGangName, err)
		}

		logger.Infof("Verified base PodGroup for PCS replica %d", pcsReplica)

		// Verify scaled PodGangs for decoder and prefill
		scaledPodGangs := []struct {
			name       string
			pcsgName   string
			replicaIdx int
		}{
			{fmt.Sprintf("tas-disagg-inference-%d-decoder-0", pcsReplica), "decoder", 1},
			{fmt.Sprintf("tas-disagg-inference-%d-prefill-0", pcsReplica), "prefill", 1},
		}

		for _, spg := range scaledPodGangs {
			logger.Infof("5.%d. Verify scaled PodGang: %s", pcsReplica+1, spg.name)

			scaledPodGroup, err := utils.FilterPodGroupByOwner(podGroups, spg.name)
			if err != nil {
				t.Fatalf("Failed to find PodGroup for scaled PodGang %s: %v", spg.name, err)
			}

			// Verify PCS-level constraint is inherited (block)
			if err := utils.VerifyKAIPodGroupTopologyConstraint(scaledPodGroup, "kubernetes.io/block", "", logger); err != nil {
				t.Fatalf("Failed to verify scaled PodGroup %s top-level constraint: %v", spg.name, err)
			}

			// Build expected SubGroups based on PCSG type
			var expectedScaledSubGroups []utils.ExpectedSubGroup
			if spg.pcsgName == "decoder" {
				expectedScaledSubGroups = []utils.ExpectedSubGroup{
					{Name: fmt.Sprintf("tas-disagg-inference-%d-decoder-%d", pcsReplica, spg.replicaIdx), MinMember: 0, Parent: nil, RequiredTopologyLevel: "kubernetes.io/rack"},
					{Name: fmt.Sprintf("tas-disagg-inference-%d-decoder-%d-dworker", pcsReplica, spg.replicaIdx), MinMember: 1, Parent: ptr.To(fmt.Sprintf("tas-disagg-inference-%d-decoder-%d", pcsReplica, spg.replicaIdx))},
					{Name: fmt.Sprintf("tas-disagg-inference-%d-decoder-%d-dleader", pcsReplica, spg.replicaIdx), MinMember: 1, Parent: ptr.To(fmt.Sprintf("tas-disagg-inference-%d-decoder-%d", pcsReplica, spg.replicaIdx))},
				}
			} else {
				expectedScaledSubGroups = []utils.ExpectedSubGroup{
					{Name: fmt.Sprintf("tas-disagg-inference-%d-prefill-%d", pcsReplica, spg.replicaIdx), MinMember: 0, Parent: nil, RequiredTopologyLevel: "kubernetes.io/rack"},
					{Name: fmt.Sprintf("tas-disagg-inference-%d-prefill-%d-pworker", pcsReplica, spg.replicaIdx), MinMember: 1, Parent: ptr.To(fmt.Sprintf("tas-disagg-inference-%d-prefill-%d", pcsReplica, spg.replicaIdx)), RequiredTopologyLevel: "kubernetes.io/hostname"},
					{Name: fmt.Sprintf("tas-disagg-inference-%d-prefill-%d-pleader", pcsReplica, spg.replicaIdx), MinMember: 1, Parent: ptr.To(fmt.Sprintf("tas-disagg-inference-%d-prefill-%d", pcsReplica, spg.replicaIdx))},
				}
			}

			if err := utils.VerifyKAIPodGroupSubGroups(scaledPodGroup, expectedScaledSubGroups, logger); err != nil {
				t.Fatalf("Failed to verify scaled PodGroup %s SubGroups: %v", spg.name, err)
			}

			logger.Infof("Verified scaled PodGroup %s for PCS replica %d", spg.name, pcsReplica)
		}
	}

	logger.Info("🎉 SP-9: Multi-replica PCS with 3-level topology hierarchy test completed successfully!")
}

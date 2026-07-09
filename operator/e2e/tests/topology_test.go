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
	"slices"
	"strings"
	"testing"

	nameutils "github.com/ai-dynamo/grove/operator/api/common"
	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/podgang"
	"github.com/ai-dynamo/grove/operator/e2e/grove/podgroup"
	"github.com/ai-dynamo/grove/operator/e2e/grove/topology"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"
	kaischedulingv2alpha2 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/scheduling/v2alpha2"
	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// groveTopologyLevels is the standard 4-level topology used by TAS1-TAS16.
// These levels match the node labels applied by the e2e cluster setup.
var groveTopologyLevels = []corev1alpha1.TopologyLevel{
	{Domain: corev1alpha1.TopologyDomainZone, Key: setup.TopologyLabelZone},
	{Domain: corev1alpha1.TopologyDomainBlock, Key: setup.TopologyLabelBlock},
	{Domain: corev1alpha1.TopologyDomainRack, Key: setup.TopologyLabelRack},
	{Domain: corev1alpha1.TopologyDomainHost, Key: setup.TopologyLabelHostname},
}

// Test_TAS1_TopologyInfrastructure verifies the ClusterTopologyBinding → KAI Topology sync loop.
// 1. Create "grove-topology" ClusterTopologyBinding with the standard 4-level hierarchy (zone, block, rack, host)
// 2. Verify the operator auto-creates a matching KAI Topology CR
// 3. Verify worker nodes have the expected topology labels
//
// Note: grove-topology is NOT cleaned up after this test — it is shared cluster infrastructure
// used by TAS2-TAS16. ensureGroveTopology() in each subsequent test is idempotent.
func Test_TAS1_TopologyInfrastructure(t *testing.T) {
	ctx := context.Background()

	tc, cleanup := testctx.PrepareTest(ctx, t, 0)
	defer cleanup()
	topologyVerifier := topology.NewVerifier(tc.Client, Logger)

	Logger.Info("1. Create grove-topology ClusterTopologyBinding with standard 4-level hierarchy")
	ensureGroveTopology(ctx, t, topologyVerifier)

	Logger.Info("2. Verify KAI Topology CR is auto-created with matching levels")

	expectedKeys := []string{
		setup.TopologyLabelZone,
		setup.TopologyLabelBlock,
		setup.TopologyLabelRack,
		setup.TopologyLabelHostname,
	}

	if err := topologyVerifier.WaitForKAITopology(ctx, "grove-topology", expectedKeys, tc.Timeout, tc.Interval); err != nil {
		t.Fatalf("Failed to wait for KAI Topology: %v", err)
	}

	if err := topologyVerifier.VerifyKAITopologyLevels(ctx, "grove-topology", expectedKeys); err != nil {
		t.Fatalf("Failed to verify KAI Topology levels: %v", err)
	}

	Logger.Info("3. Verify worker nodes have topology labels")

	// Use label selector to get only worker nodes by role label
	workerLabelSelector := setup.GetWorkerNodeLabelSelector()
	var nodeList v1.NodeList
	if err := tc.Client.List(ctx, &nodeList, &client.ListOptions{
		Raw: &metav1.ListOptions{LabelSelector: workerLabelSelector},
	}); err != nil {
		t.Fatalf("Failed to list nodes: %v", err)
	}

	// Reuse expectedKeys from step 2 (same topology label keys)
	workerCount := len(nodeList.Items)
	for _, node := range nodeList.Items {
		for _, key := range expectedKeys {
			if value, ok := node.Labels[key]; !ok || value == "" {
				t.Errorf("Node %s missing %s label", node.Name, key)
			}
		}
	}

	if workerCount == 0 {
		t.Fatal("No worker nodes found in cluster")
	}

	Logger.Infof("Successfully verified topology labels on %d worker nodes", workerCount)
	Logger.Info("Topology Infrastructure test completed successfully!")
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

	Logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	expectedPods := 7 // worker-rack: 3 pods, worker-block: 4 pods
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "tas-indep-clq",
			YAMLPath:     "../yaml/tas-indep-clq.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	topologyVerifier := topology.NewVerifier(tc.Client, Logger)
	podGroupVerifier := podgroup.NewVerifier(tc.Client, Logger)

	ensureGroveTopology(ctx, t, topologyVerifier)
	Logger.Info("2. Deploy workload (TAS2: multiple cliques with different constraints)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	Logger.Info("3. Verify worker-rack pods (3) are in the same rack")
	if err := topologyVerifier.VerifyLabeledPodsInTopologyDomain(tc.Ctx, allPods, nameutils.LabelPodClique, "tas-indep-clq-0-worker-rack", 3, setup.TopologyLabelRack); err != nil {
		t.Fatalf("Failed to verify worker-rack pods in same rack: %v", err)
	}

	Logger.Info("4. Verify worker-block pods (4) are in the same block")
	if err := topologyVerifier.VerifyLabeledPodsInTopologyDomain(tc.Ctx, allPods, nameutils.LabelPodClique, "tas-indep-clq-0-worker-block", 4, setup.TopologyLabelBlock); err != nil {
		t.Fatalf("Failed to verify worker-block pods in same block: %v", err)
	}

	Logger.Info("5. Verify KAI PodGroup has correct SubGroups with topology constraints")
	podGroup := requireSinglePodGroup(t, tc, podGroupVerifier, 0)

	// Verify top-level TopologyConstraint is empty (no PCS constraint in this test)
	// Verify SubGroups (2 standalone PCLQs - no PCSG)
	expectedSubGroups := []podgroup.ExpectedSubGroup{
		podgroup.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "worker-rack", 3, setup.TopologyLabelRack),
		podgroup.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "worker-block", 4, setup.TopologyLabelBlock),
	}
	if err := podGroupVerifier.VerifyPodGroupTopology(podGroup, "", "", expectedSubGroups); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	Logger.Info("TAS2: Multiple Cliques with Different Constraints test completed successfully!")
}

// Test_TAS3_PCSOnlyConstraint tests constraint only at PCS level with no PCSG/PCLQ constraints
// 1. Deploy workload with PCS-only constraint (pack.required: rack)
//   - PCSG: NO explicit constraint (nil)
//   - PCLQs: NO explicit constraints
//
// 2. Verify all 4 pods are in same rack (explicit PCS constraint)
// 3. Verify PCSG worker pods (2 total, 1 per replica)
// 4. Verify router pods (2 standalone)
// 5. Verify KAI PodGroup SubGroups: NO PCSG parent groups (because PCSG constraint is nil, per PR #357)
func Test_TAS3_PCSOnlyConstraint(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	expectedPods := 4 // 2 PCSG workers + 2 router standalone
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "tas-sl-pcs-only",
			YAMLPath:     "../yaml/tas-sl-pcs-only.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	topologyVerifier := topology.NewVerifier(tc.Client, Logger)
	podGroupVerifier := podgroup.NewVerifier(tc.Client, Logger)

	ensureGroveTopology(ctx, t, topologyVerifier)
	Logger.Info("2. Deploy workload (TAS3: PCS-only constraint)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	Logger.Info("3. Verify all 4 pods in same rack (explicit PCS constraint)")
	if err := topologyVerifier.VerifyPodsInSameTopologyDomain(tc.Ctx, allPods, setup.TopologyLabelRack); err != nil {
		t.Fatalf("Failed to verify all pods in same rack: %v", err)
	}

	Logger.Info("4. Verify KAI PodGroup has correct SubGroups (PCS-only constraint)")
	podGroup := requireSinglePodGroup(t, tc, podGroupVerifier, 0)

	// Verify top-level TopologyConstraint (PCS level: rack)
	// Verify SubGroups (2 PCLQ children + 1 router standalone = 3 total)
	// Note: PCSG parent groups are NOT created when PCSG has nil TopologyConstraint (PR #357)
	expectedSubGroups := []podgroup.ExpectedSubGroup{
		// Worker PCLQs (directly under PCS constraint, no PCSG parents)
		podgroup.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "workers-0-worker", 1, ""),
		podgroup.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "workers-1-worker", 1, ""),
		// Router (standalone)
		podgroup.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "router", 2, ""),
	}
	if err := podGroupVerifier.VerifyPodGroupTopology(podGroup, setup.TopologyLabelRack, "", expectedSubGroups); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	Logger.Info("TAS3: PCS-Only Constraint test completed successfully!")
}

// Test_TAS4_PCSGOnlyConstraint tests constraint only at PCSG level with no PCS/PCLQ constraints
// 1. Deploy workload with constraint only at PCSG level (pack.required: rack)
// 2. PCS and PCLQs have NO explicit constraints
// 3. Verify PCSG worker pods (2 total) respect rack constraint
// 4. Router pods (2 standalone) are unconstrained
func Test_TAS4_PCSGOnlyConstraint(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	expectedPods := 4 // 2 PCSG workers + 2 router standalone
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "tas-sl-pcsg-only",
			YAMLPath:     "../yaml/tas-sl-pcsg-only.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	topologyVerifier := topology.NewVerifier(tc.Client, Logger)
	podGroupVerifier := podgroup.NewVerifier(tc.Client, Logger)

	ensureGroveTopology(ctx, t, topologyVerifier)
	Logger.Info("2. Deploy workload (TAS4: PCSG-only constraint)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	Logger.Info("3. Verify PCSG worker pods (2 total, 1 per replica) in same rack")
	if err := topologyVerifier.VerifyLabeledPodsInTopologyDomain(tc.Ctx, allPods, nameutils.LabelPodCliqueScalingGroup, "tas-sl-pcsg-only-0-workers", 2, setup.TopologyLabelRack); err != nil {
		t.Fatalf("Failed to verify worker pods in same rack: %v", err)
	}

	Logger.Info("4. Verify KAI PodGroup has correct SubGroups (PCSG-only constraint)")
	podGroup := requireSinglePodGroup(t, tc, podGroupVerifier, 0)

	// Verify top-level TopologyConstraint (no PCS constraint)
	// Verify SubGroups (2 PCSG parents + 2 PCLQ children + 1 router standalone = 5 total)
	expectedSubGroups := []podgroup.ExpectedSubGroup{
		// PCSG replicas (parent groups, rack constraint)
		podgroup.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, "workers", 0, setup.TopologyLabelRack),
		podgroup.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, "workers", 1, setup.TopologyLabelRack),
		// Worker PCLQs (children of PCSG replicas)
		podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "workers", 0, "worker", 1, ""),
		podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "workers", 1, "worker", 1, ""),
		// Router (standalone, no constraint)
		podgroup.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "router", 2, ""),
	}
	if err := podGroupVerifier.VerifyPodGroupTopology(podGroup, "", "", expectedSubGroups); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	Logger.Info("5. Verify TopologyLevelsUnavailable = False (PCSG-only explicit topology constraint)")
	tv := topology.NewVerifier(tc.Client, Logger)
	if err := tv.WaitForPCSCondition(ctx, "default", tc.Workload.Name,
		apicommonconstants.ConditionTopologyLevelsUnavailable,
		string(metav1.ConditionFalse),
		apicommonconstants.ConditionReasonAllTopologyLevelsAvailable,
		tc.Timeout, tc.Interval); err != nil {
		t.Fatalf("Failed to verify TopologyLevelsUnavailable is False: %v", err)
	}

	Logger.Info("TAS4: PCSG-Only Constraint test completed successfully!")
}

// Test_TAS5_HostLevelConstraint tests PCLQ-only constraint with host-level packing
// 1. Deploy workload with constraint only at PCLQ level (pack.required: host)
// 2. PCS has NO explicit constraint
// 3. Verify all 2 pods on same host (strictest constraint)
func Test_TAS5_HostLevelConstraint(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	expectedPods := 2
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "tas-host-level",
			YAMLPath:     "../yaml/tas-host-level.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	topologyVerifier := topology.NewVerifier(tc.Client, Logger)
	podGroupVerifier := podgroup.NewVerifier(tc.Client, Logger)

	ensureGroveTopology(ctx, t, topologyVerifier)
	Logger.Info("2. Deploy workload (TAS5: PCLQ-only host constraint)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	Logger.Info("3. Verify all pods on same host")
	if err := topologyVerifier.VerifyPodsInSameTopologyDomain(tc.Ctx, allPods, setup.TopologyLabelHostname); err != nil {
		t.Fatalf("Failed to verify pods on same host: %v", err)
	}

	// Additional check: verify both pods have same node name
	if len(allPods) != 2 {
		t.Fatalf("Expected 2 pods, got %d", len(allPods))
	}
	if allPods[0].Spec.NodeName != allPods[1].Spec.NodeName {
		t.Fatalf("Pods not on same node: %s vs %s", allPods[0].Spec.NodeName, allPods[1].Spec.NodeName)
	}

	Logger.Info("4. Verify KAI PodGroup has correct SubGroups (PCLQ-only host constraint)")
	podGroup := requireSinglePodGroup(t, tc, podGroupVerifier, 0)

	// Verify top-level TopologyConstraint (no PCS constraint)
	// Verify SubGroups (1 standalone PCLQ with host constraint)
	expectedSubGroups := []podgroup.ExpectedSubGroup{
		podgroup.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "worker", 2, setup.TopologyLabelHostname),
	}
	if err := podGroupVerifier.VerifyPodGroupTopology(podGroup, "", "", expectedSubGroups); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	Logger.Info("TAS5: Host-Level Constraint test completed successfully!")
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

	Logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	expectedPods := 4
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "tas-standalone-pclq",
			YAMLPath:     "../yaml/tas-standalone-pclq-only-pcs-zone.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	topologyVerifier := topology.NewVerifier(tc.Client, Logger)
	podGroupVerifier := podgroup.NewVerifier(tc.Client, Logger)

	ensureGroveTopology(ctx, t, topologyVerifier)
	Logger.Info("2. Deploy workload (TAS6: Standalone PCLQ with only PCS zone constraint)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	Logger.Info("3. Verify all 4 pods in same zone (PCS zone constraint)")
	if err := topologyVerifier.VerifyPodsInSameTopologyDomain(tc.Ctx, allPods, setup.TopologyLabelZone); err != nil {
		t.Fatalf("Failed to verify pods in same zone: %v", err)
	}

	Logger.Info("4. Verify KAI PodGroup has correct SubGroups (Standalone PCLQ with PCS zone constraint)")
	podGroup := requireSinglePodGroup(t, tc, podGroupVerifier, 0)

	// Verify top-level TopologyConstraint (PCS level: zone)
	// Verify SubGroups (1 standalone PCLQ with NO constraint - zone is at PCS level)
	expectedSubGroups := []podgroup.ExpectedSubGroup{
		podgroup.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "worker", 4, ""),
	}
	if err := podGroupVerifier.VerifyPodGroupTopology(podGroup, setup.TopologyLabelZone, "", expectedSubGroups); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	Logger.Info("TAS6: Standalone PCLQ with Only PCS Zone Constraint test completed successfully!")
}

// Test_TAS7_NoTopologyConstraint tests gang scheduling without any topology constraints
// 1. Deploy workload with no constraints at PCS, PCSG, or PCLQ levels
// 2. Verify all 4 pods scheduled (gang scheduling works)
// 3. Verify KAI PodGroup has 4 SubGroups with NO topology constraints
func Test_TAS7_NoTopologyConstraint(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	expectedPods := 4 // 2 PCSG replicas × 2 pods each
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "tas-no-constraint",
			YAMLPath:     "../yaml/tas-no-constraint.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	// TAS7 YAML has no topology constraints at all, so grove-topology is not needed.
	podGroupVerifier := podgroup.NewVerifier(tc.Client, Logger)

	Logger.Info("2. Deploy workload (TAS7: No topology constraints)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	Logger.Info("3. Verify all 4 pods scheduled (gang scheduling works without constraints)")
	if len(allPods) != 4 {
		t.Fatalf("Expected 4 pods, got %d", len(allPods))
	}
	Logger.Info("4. Verify KAI PodGroup has correct SubGroups (no constraints)")
	podGroup := requireSinglePodGroup(t, tc, podGroupVerifier, 0)

	// Verify top-level TopologyConstraint (no PCS constraint)
	// Verify SubGroups (2 PCLQ children, NO constraints)
	// Note: PCSG parent groups are NOT created when PCSG has nil TopologyConstraint (PR #357)
	expectedSubGroups := []podgroup.ExpectedSubGroup{
		// Worker PCLQs (directly under PCS, no PCSG parents, no constraints)
		podgroup.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "workers-0-worker", 2, ""),
		podgroup.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "workers-1-worker", 2, ""),
	}
	if err = podGroupVerifier.VerifyPodGroupTopology(podGroup, "", "", expectedSubGroups); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	Logger.Info("TAS7: No Topology Constraint test completed successfully!")
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

	Logger.Info("1. Initialize an 8-node Grove cluster for topology testing")
	expectedPods := 8 // 2 PCSG replicas × (prefill: 2 pods + decode: 2 pods)
	tc, cleanup := testctx.PrepareTest(ctx, t, 8,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "tas-hierarchy",
			YAMLPath:     "../yaml/tas-hierarchy.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	topologyVerifier := topology.NewVerifier(tc.Client, Logger)
	podGroupVerifier := podgroup.NewVerifier(tc.Client, Logger)

	ensureGroveTopology(ctx, t, topologyVerifier)
	Logger.Info("2. Deploy workload (TAS8: full 3-level hierarchy with cascading constraints)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	Logger.Info("3. Verify PCLQ constraints (2 replicas × 2 clique types) - all on same host")
	cliqueTypes := []string{"prefill", "decode"}
	for pcsgReplica := 0; pcsgReplica < 2; pcsgReplica++ {
		for _, cliqueType := range cliqueTypes {
			cliquePods := topology.FilterPodsByLabel(allPods, nameutils.LabelPodClique,
				fmt.Sprintf("tas-hierarchy-0-inference-group-%d-%s", pcsgReplica, cliqueType))
			if len(cliquePods) != 2 {
				t.Fatalf("Expected 2 %s pods for PCSG replica %d, got %d", cliqueType, pcsgReplica, len(cliquePods))
			}
			if err := topologyVerifier.VerifyPodsInSameTopologyDomain(tc.Ctx, cliquePods, setup.TopologyLabelHostname); err != nil {
				t.Fatalf("Failed to verify %s pods on same host for PCSG replica %d: %v", cliqueType, pcsgReplica, err)
			}
		}
	}

	Logger.Info("4. Verify PCSG constraints (2 replicas) - all in same rack")
	if err := topologyVerifier.VerifyPCSGReplicasInTopologyDomain(tc.Ctx, allPods,
		"tas-hierarchy-0-inference-group", 2, 4, setup.TopologyLabelRack); err != nil {
		t.Fatalf("Failed to verify PCSG replicas: %v", err)
	}

	Logger.Info("5. Verify all pods are in same block (PCS constraint)")
	if len(allPods) != expectedPods {
		t.Fatalf("Expected %d pods, got %d", expectedPods, len(allPods))
	}
	if err := topologyVerifier.VerifyPodsInSameTopologyDomain(tc.Ctx, allPods, setup.TopologyLabelBlock); err != nil {
		t.Fatalf("Failed to verify all pods in same block: %v", err)
	}

	Logger.Info("6. Verify KAI PodGroup has correct hierarchy with topology constraints")
	podGroup := requireSinglePodGroup(t, tc, podGroupVerifier, 0)

	// Verify top-level TopologyConstraint (PCS level: block) + SubGroups hierarchy (2 PCSG parents + 4 PCLQ children)
	expectedSubGroups := []podgroup.ExpectedSubGroup{
		podgroup.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, "inference-group", 0, setup.TopologyLabelRack),
		podgroup.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, "inference-group", 1, setup.TopologyLabelRack),
		podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "inference-group", 0, "prefill", 2, setup.TopologyLabelHostname),
		podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "inference-group", 0, "decode", 2, setup.TopologyLabelHostname),
		podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "inference-group", 1, "prefill", 2, setup.TopologyLabelHostname),
		podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "inference-group", 1, "decode", 2, setup.TopologyLabelHostname),
	}
	if err := podGroupVerifier.VerifyPodGroupTopology(podGroup, setup.TopologyLabelBlock, "", expectedSubGroups); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	Logger.Info("TAS8: Full Hierarchy with Cascading Constraints test completed successfully!")
}

// Test_TAS9_PCSPlusPCLQConstraint tests PCS block constraint combined with PCLQ host constraint
// 1. Deploy workload with PCS: block constraint, PCLQ: host constraint
// 2. 2 pods total
// 3. Verify pods on same host (PCLQ constraint - strictest)
// 4. Verify KAI PodGroup has block constraint at top level, host constraint at PCLQ level
func Test_TAS9_PCSPlusPCLQConstraint(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	expectedPods := 2
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "tas-pcs-pclq",
			YAMLPath:     "../yaml/tas-pcs-pclq.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	topologyVerifier := topology.NewVerifier(tc.Client, Logger)
	podGroupVerifier := podgroup.NewVerifier(tc.Client, Logger)

	ensureGroveTopology(ctx, t, topologyVerifier)
	Logger.Info("2. Deploy workload (TAS9: PCS block + PCLQ host constraint)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	Logger.Info("3. Verify 2 pods on same host (PCLQ host constraint)")
	if err := topologyVerifier.VerifyPodsInSameTopologyDomain(tc.Ctx, allPods, setup.TopologyLabelHostname); err != nil {
		t.Fatalf("Failed to verify pods on same host: %v", err)
	}

	Logger.Info("4. Verify KAI PodGroup has correct SubGroups (PCS block + PCLQ host)")
	podGroup := requireSinglePodGroup(t, tc, podGroupVerifier, 0)

	// Verify top-level TopologyConstraint (PCS level: block) + SubGroups (1 standalone PCLQ with host constraint)
	expectedSubGroups := []podgroup.ExpectedSubGroup{
		podgroup.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "worker", 2, setup.TopologyLabelHostname),
	}
	if err := podGroupVerifier.VerifyPodGroupTopology(podGroup, setup.TopologyLabelBlock, "", expectedSubGroups); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	Logger.Info("TAS9: PCS+PCLQ Constraint test completed successfully!")
}

// Test_TAS10_PCSGScalingWithTopologyConstraints tests PCSG scaling with rack constraints
//  1. Deploy workload with 3 PCSG replicas, each with rack constraint
//  2. 6 pods total (2 per PCSG replica)
//  3. Verify each PCSG replica's pods in same rack
//  4. Verify all pods respect PCS-level block constraint (all in same block)
//  5. Verify each PCSG replica's PodGang has the correct KAI PodGroup topology
//     (PCSG r=3 minAvail=1 with no standalone PCLQ → 3 MVU PodGangs, one per PCSG replica).
func Test_TAS10_PCSGScalingWithTopologyConstraints(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	expectedPods := 6 // 3 PCSG replicas × 2 pods each
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "tas-pcsg-scale",
			YAMLPath:     "../yaml/tas-pcsg-scale.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	topologyVerifier := topology.NewVerifier(tc.Client, Logger)
	podGroupVerifier := podgroup.NewVerifier(tc.Client, Logger)

	ensureGroveTopology(ctx, t, topologyVerifier)
	Logger.Info("2. Deploy workload (TAS10: PCSG scaling with topology constraints)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	Logger.Info("3. Verify each PCSG replica's worker pods (2) are in same rack")
	if err := topologyVerifier.VerifyPCSGReplicasInTopologyDomain(tc.Ctx, allPods,
		"tas-pcsg-scale-0-inference-group", 3, 2, setup.TopologyLabelRack); err != nil {
		t.Fatalf("Failed to verify PCSG replicas: %v", err)
	}

	Logger.Info("4. Verify all pods respect PCS-level block constraint")
	if len(allPods) != expectedPods {
		t.Fatalf("Expected %d pods, got %d", expectedPods, len(allPods))
	}
	if err := topologyVerifier.VerifyPodsInSameTopologyDomain(tc.Ctx, allPods, setup.TopologyLabelBlock); err != nil {
		t.Fatalf("Failed to verify all pods in same block: %v", err)
	}

	Logger.Info("5. Verify each PCSG replica's PodGang KAI PodGroup")

	// PCSG has replicas=3, minAvailable=1, no standalone PCLQ → 3 MVU PodGangs, one per PCSG replica.
	// Every PodGang has the same shape: top-level=PCS block, PCSG parent=rack, PCLQ-in-PCSG.
	for pcsgReplica := int32(0); pcsgReplica < 3; pcsgReplica++ {
		pg, err := podgang.ForPCSGReplica(tc.Ctx, tc.Client, tc.Namespace, tc.Workload.Name, 0, "inference-group", pcsgReplica)
		if err != nil {
			t.Fatalf("Failed to find PodGang for PCSG replica %d: %v", pcsgReplica, err)
		}
		podGroup, err := podGroupVerifier.GetKAIPodGroupForPodGang(tc.Ctx, tc.Namespace, tc.Workload.Name, pg.Name)
		if err != nil {
			t.Fatalf("Failed to get KAI PodGroup for PodGang %s: %v", pg.Name, err)
		}
		expectedSubGroups := []podgroup.ExpectedSubGroup{
			podgroup.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, "inference-group", int(pcsgReplica), setup.TopologyLabelRack),
			podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "inference-group", int(pcsgReplica), "worker", 2, ""),
		}
		if err := podGroupVerifier.VerifyPodGroupTopology(podGroup, setup.TopologyLabelBlock, "", expectedSubGroups); err != nil {
			t.Fatalf("Failed to verify KAI PodGroup topology for PCSG replica %d: %v", pcsgReplica, err)
		}
	}

	Logger.Info("TAS10: PCSG Scaling with Topology Constraints test completed successfully!")
}

// Test_TAS11_PCSGPlusPCLQNoParentConstraint tests PCSG rack + PCLQ host constraints without PCS constraint
// 1. Deploy workload with PCSG: rack constraint, PCLQ: host constraint, NO PCS constraint
// 2. 4 pods (2 PCSG replicas × 2 pods)
// 3. Verify each PCSG replica's pods on same host
// 4. Verify KAI PodGroup has PCSG rack + PCLQ host constraints, NO top-level PCS constraint
func Test_TAS11_PCSGPlusPCLQNoParentConstraint(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	expectedPods := 4 // 2 PCSG replicas × 2 pods each
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "tas-pcsg-pclq",
			YAMLPath:     "../yaml/tas-pcsg-pclq.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	topologyVerifier := topology.NewVerifier(tc.Client, Logger)
	podGroupVerifier := podgroup.NewVerifier(tc.Client, Logger)

	ensureGroveTopology(ctx, t, topologyVerifier)
	Logger.Info("2. Deploy workload (TAS11: PCSG rack + PCLQ host, no PCS constraint)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	Logger.Info("3. Verify each PCSG replica's pods on same host")
	workersPCSG := nameutils.GeneratePodCliqueScalingGroupName(
		nameutils.ResourceNameReplica{Name: tc.Workload.Name, Replica: 0},
		"workers",
	)
	if err := topologyVerifier.VerifyPCSGReplicasInTopologyDomain(tc.Ctx, allPods,
		workersPCSG, 2, 2, setup.TopologyLabelHostname); err != nil {
		t.Fatalf("Failed to verify PCSG replicas: %v", err)
	}

	Logger.Info("4. Verify KAI PodGroup has correct SubGroups (PCSG rack + PCLQ host)")
	podGroup := requireSinglePodGroup(t, tc, podGroupVerifier, 0)

	// Verify top-level TopologyConstraint (no PCS constraint)
	// SubGroups (2 PCSG parents with rack + 2 PCLQ children with host)
	expectedSubGroups := []podgroup.ExpectedSubGroup{
		podgroup.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, "workers", 0, setup.TopologyLabelRack),
		podgroup.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, "workers", 1, setup.TopologyLabelRack),
		podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "workers", 0, "worker", 2, setup.TopologyLabelHostname),
		podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "workers", 1, "worker", 2, setup.TopologyLabelHostname),
	}
	if err := podGroupVerifier.VerifyPodGroupTopology(podGroup, "", "", expectedSubGroups); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	Logger.Info("TAS11: PCSG+PCLQ Constraint test completed successfully!")
}

// Test_TAS12_LargeScalingRatio tests large PCSG scaling ratio with minAvailable
//  1. Deploy workload with replicas=10, minAvailable=3, PCLQ host constraint, PCS block constraint (no PCSG constraint)
//  2. 20 pods expected (10 PCSG replicas × 2 pods)
//  3. Verify each PCSG replica's pods on same host
//  4. Verify all pods in same block (PCS constraint)
//  5. Verify each PodGang's KAI PodGroup carries the expected PCSG replicas
//     (3 MVUs of 3 indices each + 1 Tail-PG of 1 index = 4 PodGangs).
func Test_TAS12_LargeScalingRatio(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	expectedPods := 20 // Base PodGang: 3 PCSG replicas × 2 pods (6) + Scaled: 7 PCSG replicas × 2 pods (14)
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "tas-large-scale",
			YAMLPath:     "../yaml/tas-large-scale.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	topologyVerifier := topology.NewVerifier(tc.Client, Logger)
	podGroupVerifier := podgroup.NewVerifier(tc.Client, Logger)

	ensureGroveTopology(ctx, t, topologyVerifier)
	Logger.Info("2. Deploy workload (TAS12: Large scaling ratio, replicas=10/minAvailable=3)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	Logger.Info("3. Verify each PCSG replica's pods on same host")
	if err := topologyVerifier.VerifyPCSGReplicasInTopologyDomain(tc.Ctx, allPods,
		"tas-large-scale-0-workers", 10, 2, setup.TopologyLabelHostname); err != nil {
		t.Fatalf("Failed to verify PCSG replicas: %v", err)
	}

	// TODO: This assertion for the cluster + constraint shape used by this test will always fail for coherent update
	// see https://github.com/ai-dynamo/grove/issues/648 - once the issue has been resolved properly we will either
	// uncomment this or revise the assertion.

	//Logger.Info("4. Verify all 20 pods in same block (PCS block constraint)")
	//if err := topologyVerifier.VerifyPodsInSameTopologyDomain(tc.Ctx, allPods, setup.TopologyLabelBlock); err != nil {
	//	t.Fatalf("Failed to verify all pods in same block: %v", err)
	//}

	Logger.Info("5. Verify each PodGang's KAI PodGroup carries the expected PCSG replicas")

	// PCSG `workers` (r=10, minAvailable=3) with no PCSG topology constraint.
	// MVU formation: 3 MVUs of 3 indices each (0-2, 3-5, 6-8) + 1 Tail-PG (9). Total 4 PodGangs.
	// Each PodGang's KAI PodGroup has one PCLQ-NoParent SubGroup per PCSG replica index it carries
	// (no PCSG parent SubGroup because PCSG has no topology constraint).
	podGangs, err := podgang.ListForPCSReplica(tc.Ctx, tc.Client, tc.Namespace, tc.Workload.Name, 0)
	if err != nil {
		t.Fatalf("Failed to list PodGangs for PCS replica 0: %v", err)
	}
	pgmName := nameutils.GeneratePodGangMapName(nameutils.ResourceNameReplica{Name: tc.Workload.Name, Replica: 0})
	pgm := &corev1alpha1.PodGangMap{}
	if err := tc.Client.Get(tc.Ctx, client.ObjectKey{Namespace: tc.Namespace, Name: pgmName}, pgm); err != nil {
		t.Fatalf("Failed to get PodGangMap %s: %v", pgmName, err)
	}
	indicesByPodGang := make(map[string][]int32, len(pgm.Spec.Entries))
	for _, entry := range pgm.Spec.Entries {
		indicesByPodGang[entry.Name] = entry.PCSGReplicaIndices["workers"]
	}
	for _, pg := range podGangs {
		indices := indicesByPodGang[pg.Name]
		podGroup, err := podGroupVerifier.GetKAIPodGroupForPodGang(tc.Ctx, tc.Namespace, tc.Workload.Name, pg.Name)
		if err != nil {
			t.Fatalf("Failed to get KAI PodGroup for PodGang %s: %v", pg.Name, err)
		}
		expectedSubGroups := make([]podgroup.ExpectedSubGroup, 0, len(indices))
		for _, idx := range indices {
			expectedSubGroups = append(expectedSubGroups,
				podgroup.CreateExpectedPCLQInPCSGSubGroupNoParent(tc.Workload.Name, 0, "workers", int(idx), "worker", 2, setup.TopologyLabelHostname))
		}
		if err := podGroupVerifier.VerifyPodGroupTopology(podGroup, setup.TopologyLabelBlock, "", expectedSubGroups); err != nil {
			t.Fatalf("Failed to verify KAI PodGroup topology for PodGang %s (PCSG replicas %v): %v", pg.Name, indices, err)
		}
	}

	Logger.Info("TAS12: Large Scaling Ratio test completed successfully!")
}

// Test_TAS13_InsufficientNodesForConstraint tests gang scheduling failure with unsatisfiable topology constraint
// 1. Deploy workload with rack constraint requesting 10 pods (exceeds rack capacity)
// 2. Verify all 10 pods remain in Pending state (no partial scheduling)
// 3. Verify NO pods are scheduled (all-or-nothing gang behavior)
// 4. Verify pod events show Unschedulable reason
// 5. Verify KAI PodGroup exists with correct constraints even though pods are pending
func Test_TAS13_InsufficientNodesForConstraint(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	expectedPods := 10
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "tas-insuffic",
			YAMLPath:     "../yaml/tas-insuffic.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	podGroupVerifier := podgroup.NewVerifier(tc.Client, Logger)
	ensureGroveTopology(ctx, t, topology.NewVerifier(tc.Client, Logger))

	Logger.Info("2. Deploy workload (TAS13: insufficient nodes for rack constraint)")
	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	Logger.Info("3. Verify all 10 pods remain in Pending state (no partial scheduling)")
	if err := tc.VerifyPodsArePendingWithUnschedulableEvents(true, expectedPods); err != nil {
		t.Fatalf("Failed to verify pods are pending with unschedulable events: %v", err)
	}

	Logger.Info("4. Verify NO pods are scheduled (all-or-nothing gang behavior)")
	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}

	lo.ForEach(pods.Items, func(pod v1.Pod, _ int) {
		if pod.Spec.NodeName != "" {
			t.Fatalf("Expected pod %s to have no node assignment, but assigned to %s", pod.Name, pod.Spec.NodeName)
		}
	})
	Logger.Info("5. Verify KAI PodGroup exists with correct topology constraints (even though pods are pending)")
	podGroup := requireSinglePodGroup(t, tc, podGroupVerifier, 0)

	// Verify top-level TopologyConstraint (PCS level: rack)
	// SubGroups (1 standalone PCLQ - no PCSG)
	expectedSubGroups := []podgroup.ExpectedSubGroup{
		podgroup.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "worker", 10, ""),
	}
	if err := podGroupVerifier.VerifyPodGroupTopology(podGroup, setup.TopologyLabelRack, "", expectedSubGroups); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology: %v", err)
	}

	Logger.Info("TAS13: Insufficient Nodes for Constraint test completed successfully!")
}

// Test_TAS14_MultiReplicaWithRackConstraint tests multiple PCS replicas with rack constraints
// 1. Deploy workload with 2 PCS replicas, each with rack constraint
// 2. 4 pods (2 per PCS replica)
// 3. Verify each PCS replica's pods in same rack
// 4. Verify KAI PodGroups for both PCS replicas have correct topology constraints
func Test_TAS14_MultiReplicaWithRackConstraint(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	expectedPods := 4
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "tas-multirep",
			YAMLPath:     "../yaml/tas-multirep.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	topologyVerifier := topology.NewVerifier(tc.Client, Logger)
	podGroupVerifier := podgroup.NewVerifier(tc.Client, Logger)

	ensureGroveTopology(ctx, t, topologyVerifier)
	Logger.Info("2. Deploy workload (TAS14: multi-replica with rack constraint)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	Logger.Info("3. Verify each PCS replica's pods (2) are in same rack")
	for pcsReplica := 0; pcsReplica < 2; pcsReplica++ {
		replicaPods := topology.FilterPodsByLabel(allPods, nameutils.LabelPodCliqueSetReplicaIndex, fmt.Sprintf("%d", pcsReplica))
		if len(replicaPods) != 2 {
			t.Fatalf("Expected 2 replica-%d pods, got %d", pcsReplica, len(replicaPods))
		}
		if err := topologyVerifier.VerifyPodsInSameTopologyDomain(tc.Ctx, replicaPods, setup.TopologyLabelRack); err != nil {
			t.Fatalf("Failed to verify replica-%d pods in same rack: %v", pcsReplica, err)
		}
	}

	Logger.Info("4. Verify KAI PodGroups for both replicas have correct topology constraints")
	for pcsReplica := 0; pcsReplica < 2; pcsReplica++ {
		podGroup := requireSinglePodGroup(t, tc, podGroupVerifier, pcsReplica)

		expectedSubGroups := []podgroup.ExpectedSubGroup{
			podgroup.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, pcsReplica, "worker", 2, ""),
		}
		if err := podGroupVerifier.VerifyPodGroupTopology(podGroup, setup.TopologyLabelRack, "", expectedSubGroups); err != nil {
			t.Fatalf("Failed to verify PodGroup-%d topology: %v", pcsReplica, err)
		}
	}

	Logger.Info("TAS14: Multi-Replica with Rack Constraint test completed successfully!")
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

	Logger.Info("1. Initialize a 28-node Grove cluster for topology testing")
	expectedPods := 10 // decoder (2×2) + prefill (2×2) + router (2)
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "tas-pcs-multi-pcsg",
			YAMLPath:     "../yaml/tas-pcs-multi-pcsg.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	topologyVerifier := topology.NewVerifier(tc.Client, Logger)
	podGroupVerifier := podgroup.NewVerifier(tc.Client, Logger)

	ensureGroveTopology(ctx, t, topologyVerifier)
	Logger.Info("2. Deploy workload (TAS15: disaggregated inference with multiple PCSGs)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	Logger.Info("3. Verify block-level constraint (all 10 pods in same block)")
	if err := topologyVerifier.VerifyPodsInSameTopologyDomain(tc.Ctx, allPods, setup.TopologyLabelBlock); err != nil {
		t.Fatalf("Failed to verify all pods in same block: %v", err)
	}

	// Generate PCSG and PCLQ names
	pcsReplica := nameutils.ResourceNameReplica{Name: tc.Workload.Name, Replica: 0}
	decoderPCSG := nameutils.GeneratePodCliqueScalingGroupName(pcsReplica, "decoder")
	prefillPCSG := nameutils.GeneratePodCliqueScalingGroupName(pcsReplica, "prefill")
	routerPCLQ := nameutils.GeneratePodCliqueName(pcsReplica, "router")

	Logger.Info("4. Verify PCSG replicas (2 types × 2 replicas) are in same rack")
	pcsgTypes := []topology.PCSGTypeConfig{
		{Name: "decoder", FQN: decoderPCSG},
		{Name: "prefill", FQN: prefillPCSG},
	}
	if err := topologyVerifier.VerifyMultiTypePCSGReplicas(tc.Ctx, allPods, pcsgTypes, 2, 2,
		setup.TopologyLabelRack); err != nil {
		t.Fatalf("Failed to verify PCSG replicas: %v", err)
	}

	Logger.Info("5. Verify router pods (2 standalone, no PCSG label)")
	routerPods := topology.FilterPodsByLabel(allPods, nameutils.LabelPodClique, routerPCLQ)
	if len(routerPods) != 2 {
		t.Fatalf("Expected 2 router pods, got %d", len(routerPods))
	}

	Logger.Info("6. Verify MVU PodGang's KAI PodGroup (PCSG replicas 0, standalone router)")

	// MVU formation: standalone router (r=2, minAvail=2) exhausts the PCLQ pool in iter 1, so
	// only 1 MVU forms carrying decoder@0, prefill@0, router. Tail-PGs hold decoder@1 and prefill@1.
	// Total: 3 PodGangs per PCS replica.
	mvuMatches, err := podgang.ListForStandalonePCLQ(tc.Ctx, tc.Client, tc.Namespace, tc.Workload.Name, 0, "router")
	if err != nil {
		t.Fatalf("Failed to find PodGangs carrying standalone router: %v", err)
	}
	if len(mvuMatches) != 1 {
		t.Fatalf("Expected exactly one PodGang carrying standalone router (router has replicas=minAvailable=2 → single MVU), got %d", len(mvuMatches))
	}
	mvu := &mvuMatches[0]
	mvuPodGroup, err := podGroupVerifier.GetKAIPodGroupForPodGang(tc.Ctx, tc.Namespace, tc.Workload.Name, mvu.Name)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroup for MVU PodGang %s: %v", mvu.Name, err)
	}
	mvuExpectedSubGroups := []podgroup.ExpectedSubGroup{
		podgroup.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, "decoder", 0, setup.TopologyLabelRack),
		podgroup.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, "prefill", 0, setup.TopologyLabelRack),
		podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "decoder", 0, "dworker", 1, ""),
		podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "decoder", 0, "dleader", 1, ""),
		podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "prefill", 0, "pworker", 1, ""),
		podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "prefill", 0, "pleader", 1, ""),
		podgroup.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "router", 2, ""),
	}
	if err := podGroupVerifier.VerifyPodGroupTopology(mvuPodGroup, setup.TopologyLabelBlock, "", mvuExpectedSubGroups); err != nil {
		t.Fatalf("Failed to verify KAI PodGroup topology for MVU PodGang %s: %v", mvu.Name, err)
	}

	Logger.Info("7. Verify Tail-PG PodGangs' KAI PodGroups (decoder replica 1, prefill replica 1)")

	// Each Tail-PG holds 1 PCSG replica with the same uniform mapping (top=PCS block, PCSG parent + PCLQ children).
	tailPCSGs := []struct {
		name    string
		cliques []string
	}{
		{name: "decoder", cliques: []string{"dworker", "dleader"}},
		{name: "prefill", cliques: []string{"pworker", "pleader"}},
	}
	for _, tail := range tailPCSGs {
		tailPG, err := podgang.ForPCSGReplica(tc.Ctx, tc.Client, tc.Namespace, tc.Workload.Name, 0, tail.name, 1)
		if err != nil {
			t.Fatalf("Failed to find Tail-PG for PCSG %s replica 1: %v", tail.name, err)
		}
		tailPodGroup, err := podGroupVerifier.GetKAIPodGroupForPodGang(tc.Ctx, tc.Namespace, tc.Workload.Name, tailPG.Name)
		if err != nil {
			t.Fatalf("Failed to get KAI PodGroup for Tail-PG %s: %v", tailPG.Name, err)
		}
		tailExpectedSubGroups := []podgroup.ExpectedSubGroup{
			podgroup.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, tail.name, 1, setup.TopologyLabelRack),
		}
		for _, clique := range tail.cliques {
			tailExpectedSubGroups = append(tailExpectedSubGroups,
				podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, tail.name, 1, clique, 1, ""))
		}
		if err := podGroupVerifier.VerifyPodGroupTopology(tailPodGroup, setup.TopologyLabelBlock, "", tailExpectedSubGroups); err != nil {
			t.Fatalf("Failed to verify KAI PodGroup topology for Tail-PG %s (PCSG %s replica 1): %v", tailPG.Name, tail.name, err)
		}
	}

	Logger.Info("TAS15: Disaggregated Inference with Multiple PCSGs test completed successfully!")
}

// Test_TAS16_MultiReplicaPCSWithThreeLevelHierarchy tests multi-replica PCS with full 3-level topology hierarchy
// 1. Deploy workload with 2 PCS replicas, each with full 3-level hierarchy
// 2. 20 pods (10 per PCS replica): decoder (2×2) + prefill (2×2) + router (2)
// 3. PCS: block constraint, PCSG: rack constraint, PCLQ (pworker): host constraint
// 4. Verify block constraint at PCS level, rack at PCSG, for both PCS replicas
// 5. Similar to TAS15 but scaled across 2 PCS replicas
func Test_TAS16_MultiReplicaPCSWithThreeLevelHierarchy(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 28-node Grove cluster for multi-replica PCS testing")
	expectedPods := 20 // PCS replica 0: 10 pods + PCS replica 1: 10 pods
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "tas-pcs-multi-pcsg",
			YAMLPath:     "../yaml/tas-pcs-multi-pcsg-multi-replica.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	topologyVerifier := topology.NewVerifier(tc.Client, Logger)
	podGroupVerifier := podgroup.NewVerifier(tc.Client, Logger)

	ensureGroveTopology(ctx, t, topologyVerifier)
	Logger.Info("2. Deploy workload (TAS16: 2 PCS replicas with 3-level topology hierarchy)")
	allPods, err := DeployWorkloadAndGetPods(tc, expectedPods)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	// Verify for each PCS replica
	for pcsReplica := 0; pcsReplica < 2; pcsReplica++ {
		replicaLabel := fmt.Sprintf("%d", pcsReplica)
		replicaPods := topology.FilterPodsByLabel(allPods, nameutils.LabelPodCliqueSetReplicaIndex, replicaLabel)
		if len(replicaPods) != 10 {
			t.Fatalf("Expected 10 pods for PCS replica %d, got %d", pcsReplica, len(replicaPods))
		}

		Logger.Infof("3.%d. Verify PCS replica %d pods in same block (PCS block constraint)", pcsReplica+1, pcsReplica)
		if err := topologyVerifier.VerifyPodsInSameTopologyDomain(tc.Ctx, replicaPods, setup.TopologyLabelBlock); err != nil {
			t.Fatalf("Failed to verify PCS replica %d pods in same block: %v", pcsReplica, err)
		}

		Logger.Infof("4.%d. Verify PCS replica %d pods topology constraints", pcsReplica+1, pcsReplica)

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
		pcsgTypes := []topology.PCSGTypeConfig{
			{Name: "decoder", FQN: decoderPCSG},
			{Name: "prefill", FQN: prefillPCSG},
		}
		if err := topologyVerifier.VerifyMultiTypePCSGReplicas(tc.Ctx, replicaPods, pcsgTypes, 2, 2,
			setup.TopologyLabelRack); err != nil {
			t.Fatalf("Failed to verify PCSG replicas for PCS replica %d: %v", pcsReplica, err)
		}

		// Verify router pods (2 standalone)
		Logger.Infof("4.%d. Verify router pods (2 standalone)", pcsReplica+1)
		routerPods := topology.FilterPodsByLabel(replicaPods, nameutils.LabelPodClique, routerPCLQ)
		if len(routerPods) != 2 {
			t.Fatalf("Expected 2 router pods for PCS replica %d, got %d", pcsReplica, len(routerPods))
		}
	}

	Logger.Info("5. Verify KAI PodGroups for both PCS replicas (1 MVU + 2 Tail-PGs each)")

	// Same shape as TAS15 per PCS replica: standalone router exhausts the PCLQ pool in iter 1,
	// so 1 MVU forms with decoder@0 + prefill@0 + router; 2 Tail-PGs hold decoder@1 and prefill@1.
	for pcsReplica := 0; pcsReplica < 2; pcsReplica++ {
		mvuMatches, err := podgang.ListForStandalonePCLQ(tc.Ctx, tc.Client, tc.Namespace, tc.Workload.Name, pcsReplica, "router")
		if err != nil {
			t.Fatalf("Failed to find PodGangs carrying standalone router for PCS replica %d: %v", pcsReplica, err)
		}
		if len(mvuMatches) != 1 {
			t.Fatalf("Expected exactly one PodGang carrying standalone router for PCS replica %d, got %d", pcsReplica, len(mvuMatches))
		}
		mvu := &mvuMatches[0]
		mvuPodGroup, err := podGroupVerifier.GetKAIPodGroupForPodGang(tc.Ctx, tc.Namespace, tc.Workload.Name, mvu.Name)
		if err != nil {
			t.Fatalf("Failed to get KAI PodGroup for MVU PodGang %s (PCS replica %d): %v", mvu.Name, pcsReplica, err)
		}
		mvuExpectedSubGroups := []podgroup.ExpectedSubGroup{
			podgroup.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, pcsReplica, "decoder", 0, setup.TopologyLabelRack),
			podgroup.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, pcsReplica, "prefill", 0, setup.TopologyLabelRack),
			podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, pcsReplica, "decoder", 0, "dworker", 1, ""),
			podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, pcsReplica, "decoder", 0, "dleader", 1, ""),
			podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, pcsReplica, "prefill", 0, "pworker", 1, setup.TopologyLabelHostname),
			podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, pcsReplica, "prefill", 0, "pleader", 1, ""),
			podgroup.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, pcsReplica, "router", 2, ""),
		}
		if err := podGroupVerifier.VerifyPodGroupTopology(mvuPodGroup, setup.TopologyLabelBlock, "", mvuExpectedSubGroups); err != nil {
			t.Fatalf("Failed to verify KAI PodGroup topology for MVU %s (PCS replica %d): %v", mvu.Name, pcsReplica, err)
		}

		tailSpecs := []struct {
			name    string
			cliques []struct {
				name       string
				constraint string
			}
		}{
			{name: "decoder", cliques: []struct {
				name       string
				constraint string
			}{{"dworker", ""}, {"dleader", ""}}},
			{name: "prefill", cliques: []struct {
				name       string
				constraint string
			}{{"pworker", setup.TopologyLabelHostname}, {"pleader", ""}}},
		}
		for _, tail := range tailSpecs {
			tailPG, err := podgang.ForPCSGReplica(tc.Ctx, tc.Client, tc.Namespace, tc.Workload.Name, pcsReplica, tail.name, 1)
			if err != nil {
				t.Fatalf("Failed to find Tail-PG for PCS replica %d, PCSG %s replica 1: %v", pcsReplica, tail.name, err)
			}
			tailPodGroup, err := podGroupVerifier.GetKAIPodGroupForPodGang(tc.Ctx, tc.Namespace, tc.Workload.Name, tailPG.Name)
			if err != nil {
				t.Fatalf("Failed to get KAI PodGroup for Tail-PG %s (PCS replica %d): %v", tailPG.Name, pcsReplica, err)
			}
			tailExpectedSubGroups := []podgroup.ExpectedSubGroup{
				podgroup.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, pcsReplica, tail.name, 1, setup.TopologyLabelRack),
			}
			for _, c := range tail.cliques {
				tailExpectedSubGroups = append(tailExpectedSubGroups,
					podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, pcsReplica, tail.name, 1, c.name, 1, c.constraint))
			}
			if err := podGroupVerifier.VerifyPodGroupTopology(tailPodGroup, setup.TopologyLabelBlock, "", tailExpectedSubGroups); err != nil {
				t.Fatalf("Failed to verify KAI PodGroup topology for Tail-PG %s (PCS replica %d, PCSG %s replica 1): %v", tailPG.Name, pcsReplica, tail.name, err)
			}
		}
	}

	Logger.Info("TAS16: Multi-replica PCS with 3-level topology hierarchy test completed successfully!")
}

// Test_TAS17_HeterogeneousGPUCluster tests multi-topology scheduling across H100 and GB200 hardware segments.
// Same domain name "block" maps to different node label keys in each topology.
// 1. Prepare 28-node cluster, split into H100 (first 14) and GB200 (last 14) segments
// 2. Relabel GB200 nodes: remove kubernetes.io/rack, add example.com/nvl-block and example.com/nvlink-domain
// 3. Create h100-topology (block→kubernetes.io/rack, host→kubernetes.io/hostname)
// 4. Create gb200-topology (block→example.com/nvl-block, rack→example.com/nvlink-domain, host→kubernetes.io/hostname)
// 5. Verify KAI Topology CRs auto-created with correct keys
// 6. Deploy H100 and GB200 workloads, verify pods packed at block level on correct node segments
func Test_TAS17_HeterogeneousGPUCluster(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 28-node Grove cluster for heterogeneous GPU testing")
	tc, cleanup := testctx.PrepareTest(ctx, t, 28)
	defer cleanup()
	tv := topology.NewVerifier(tc.Client, Logger)

	Logger.Info("2. Get worker node names and split into H100/GB200 segments")
	workerNodes, err := tv.GetWorkerNodeNames(ctx)
	if err != nil {
		t.Fatalf("Failed to get worker node names: %v", err)
	}
	if len(workerNodes) < 28 {
		t.Fatalf("Expected at least 28 worker nodes, got %d", len(workerNodes))
	}
	const gb200NodesPerBlock = 7  // 14 GB200 nodes split into 2 blocks of 7
	h100Nodes := workerNodes[:14] // H100 segment (first 14 nodes keep default labels)
	gb200Nodes := workerNodes[14:28]

	Logger.Info("3. Relabel GB200 nodes: remove kubernetes.io/rack, add NVLink labels")
	var labelChanges []topology.NodeLabelChange
	for idx, nodeName := range gb200Nodes {
		labelChanges = append(labelChanges, topology.NodeLabelChange{
			NodeName:     nodeName,
			RemoveLabels: []string{setup.TopologyLabelRack},
			AddLabels: map[string]string{
				"example.com/nvl-block":     fmt.Sprintf("block-%d", idx/gb200NodesPerBlock),
				"example.com/nvlink-domain": fmt.Sprintf("nvl-domain-%d", idx),
			},
		})
	}
	labelCleanup, err := tv.MutateNodeLabels(ctx, t, labelChanges)
	if err != nil {
		t.Fatalf("Failed to mutate GB200 node labels: %v", err)
	}
	defer labelCleanup()

	Logger.Info("4. Create h100-topology (block→kubernetes.io/rack, host→kubernetes.io/hostname)")
	h100Levels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainBlock, Key: setup.TopologyLabelRack},
		{Domain: corev1alpha1.TopologyDomainHost, Key: setup.TopologyLabelHostname},
	}
	if err := tv.CreateClusterTopology(ctx, "h100-topology", h100Levels); err != nil {
		t.Fatalf("Failed to create h100-topology: %v", err)
	}
	defer func() {
		if err := tv.DeleteClusterTopology(ctx, "h100-topology"); err != nil {
			Logger.Errorf("Failed to delete h100-topology: %v", err)
		}
	}()

	Logger.Info("5. Create gb200-topology (block→example.com/nvl-block, rack→example.com/nvlink-domain, host→kubernetes.io/hostname)")
	gb200Levels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainBlock, Key: "example.com/nvl-block"},
		{Domain: corev1alpha1.TopologyDomainRack, Key: "example.com/nvlink-domain"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: setup.TopologyLabelHostname},
	}
	if err := tv.CreateClusterTopology(ctx, "gb200-topology", gb200Levels); err != nil {
		t.Fatalf("Failed to create gb200-topology: %v", err)
	}
	defer func() {
		if err := tv.DeleteClusterTopology(ctx, "gb200-topology"); err != nil {
			Logger.Errorf("Failed to delete gb200-topology: %v", err)
		}
	}()

	Logger.Info("6. Wait for KAI Topology auto-created for h100-topology (2 keys)")
	if err := tv.WaitForKAITopology(ctx, "h100-topology",
		[]string{setup.TopologyLabelRack, setup.TopologyLabelHostname},
		tc.Timeout, tc.Interval); err != nil {
		t.Fatalf("Failed to wait for h100-topology KAI Topology: %v", err)
	}

	Logger.Info("7. Wait for KAI Topology auto-created for gb200-topology (3 keys)")
	if err := tv.WaitForKAITopology(ctx, "gb200-topology",
		[]string{"example.com/nvl-block", "example.com/nvlink-domain", setup.TopologyLabelHostname},
		tc.Timeout, tc.Interval); err != nil {
		t.Fatalf("Failed to wait for gb200-topology KAI Topology: %v", err)
	}

	Logger.Info("8. Deploy H100 workload")
	if _, err := tc.ApplyYAMLFile("../yaml/tas-multi-topology-h100.yaml"); err != nil {
		t.Fatalf("Failed to apply H100 workload YAML: %v", err)
	}

	Logger.Info("9. Deploy GB200 workload")
	if _, err := tc.ApplyYAMLFile("../yaml/tas-multi-topology-gb200.yaml"); err != nil {
		t.Fatalf("Failed to apply GB200 workload YAML: %v", err)
	}

	Logger.Info("10. Wait for H100 pods to be ready")
	h100LabelSelector := "app.kubernetes.io/part-of=tas-h100-workload"
	allRunning := func(podList *v1.PodList) bool {
		if len(podList.Items) < 2 {
			return false
		}
		for _, pod := range podList.Items {
			if pod.Status.Phase != v1.PodRunning {
				return false
			}
		}
		return true
	}
	h100PodList, err := waiter.New[*v1.PodList]().
		WithTimeout(tc.Timeout).
		WithInterval(tc.Interval).
		WithRetryOnError().
		WaitFor(ctx, func(ctx context.Context) (*v1.PodList, error) {
			var podList v1.PodList
			if err := tc.Client.List(ctx, &podList, &client.ListOptions{
				Namespace: "default",
				Raw:       &metav1.ListOptions{LabelSelector: h100LabelSelector},
			}); err != nil {
				return nil, err
			}
			return &podList, nil
		}, allRunning)
	if err != nil {
		t.Fatalf("Failed to wait for H100 pods: %v", err)
	}
	h100Pods := h100PodList.Items

	Logger.Info("11. Wait for GB200 pods to be ready")
	gb200LabelSelector := "app.kubernetes.io/part-of=tas-gb200-workload"
	gb200PodList, err := waiter.New[*v1.PodList]().
		WithTimeout(tc.Timeout).
		WithInterval(tc.Interval).
		WithRetryOnError().
		WaitFor(ctx, func(ctx context.Context) (*v1.PodList, error) {
			var podList v1.PodList
			if err := tc.Client.List(ctx, &podList, &client.ListOptions{
				Namespace: "default",
				Raw:       &metav1.ListOptions{LabelSelector: gb200LabelSelector},
			}); err != nil {
				return nil, err
			}
			return &podList, nil
		}, allRunning)
	if err != nil {
		t.Fatalf("Failed to wait for GB200 pods: %v", err)
	}
	gb200Pods := gb200PodList.Items

	Logger.Info("12. Verify H100 pods packed at block level (kubernetes.io/rack)")
	if err := tv.VerifyPodsInSameTopologyDomain(ctx, h100Pods, setup.TopologyLabelRack); err != nil {
		t.Fatalf("Failed to verify H100 pods in same rack (block): %v", err)
	}

	Logger.Info("13. Verify GB200 pods packed at block level (example.com/nvl-block)")
	if err := tv.VerifyPodsInSameTopologyDomain(ctx, gb200Pods, "example.com/nvl-block"); err != nil {
		t.Fatalf("Failed to verify GB200 pods in same nvl-block (block): %v", err)
	}

	Logger.Info("14. Verify H100 pods are on H100 nodes (not on GB200 nodes)")
	for _, pod := range h100Pods {
		if !slices.Contains(h100Nodes, pod.Spec.NodeName) {
			t.Fatalf("H100 pod %s scheduled on node %s which is not in the H100 segment %v", pod.Name, pod.Spec.NodeName, h100Nodes)
		}
	}

	Logger.Info("15. Verify GB200 pods are on GB200 nodes (not on H100 nodes)")
	for _, pod := range gb200Pods {
		if !slices.Contains(gb200Nodes, pod.Spec.NodeName) {
			t.Fatalf("GB200 pod %s scheduled on node %s which is not in the GB200 segment %v", pod.Name, pod.Spec.NodeName, gb200Nodes)
		}
	}

	Logger.Info("TAS17: Heterogeneous GPU Cluster test completed successfully!")
}

// Test_TAS18_ClusterTopologyDriftDetection tests drift detection when a ClusterTopologyBinding references
// a non-existent KAI Topology via schedulerTopologyReferences.
// 1. Create CT with schedulerTopologyReferences pointing to a non-existent KAI Topology
// 2. Verify SchedulerTopologyDrift condition becomes True/Drift
// 3. Verify SchedulerTopologyStatuses shows InSync=false
func Test_TAS18_ClusterTopologyDriftDetection(t *testing.T) {
	const ctName = "drift-detect-topo"
	const kaiTopoRef = "non-existent-kai-topo"
	ctx := context.Background()

	Logger.Info("1. Initialize test environment (0 nodes needed)")
	tc, cleanup := testctx.PrepareTest(ctx, t, 0)
	defer cleanup()
	tv := topology.NewVerifier(tc.Client, Logger)

	Logger.Info("2. Create CT with schedulerTopologyReferences pointing to non-existent KAI Topology")
	levels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: setup.TopologyLabelZone},
		{Domain: corev1alpha1.TopologyDomainRack, Key: setup.TopologyLabelRack},
		{Domain: corev1alpha1.TopologyDomainHost, Key: setup.TopologyLabelHostname},
	}
	refs := []corev1alpha1.SchedulerTopologyBinding{
		{SchedulerName: "kai-scheduler", TopologyReference: kaiTopoRef},
	}
	if err := tv.CreateClusterTopologyWithSchedulerReferences(ctx, ctName, levels, refs); err != nil {
		t.Fatalf("Failed to create ClusterTopologyBinding with scheduler references: %v", err)
	}
	defer func() {
		if err := tv.DeleteClusterTopology(ctx, ctName); err != nil {
			Logger.Errorf("Failed to delete %s: %v", ctName, err)
		}
	}()

	Logger.Info("3. Wait for SchedulerTopologyDrift condition to become True/Drift")
	if err := tv.WaitForClusterTopologyCondition(ctx, ctName,
		apicommonconstants.ConditionSchedulerTopologyDrift,
		string(metav1.ConditionTrue),
		apicommonconstants.ConditionReasonDrift,
		tc.Timeout, tc.Interval); err != nil {
		t.Fatalf("Failed to wait for SchedulerTopologyDrift=True/Drift: %v", err)
	}

	Logger.Info("4. Verify SchedulerTopologyStatuses count and InSync=false")
	statuses, err := tv.VerifyClusterTopologySchedulerStatuses(ctx, ctName, 1)
	if err != nil {
		t.Fatalf("Failed to verify scheduler topology statuses: %v", err)
	}
	if statuses[0].InSync {
		t.Fatalf("Expected SchedulerTopologyStatus InSync=false, got true")
	}
	if statuses[0].TopologyReference != kaiTopoRef {
		t.Fatalf("Expected SchedulerTopologyStatus TopologyReference='%s', got '%s'", kaiTopoRef, statuses[0].TopologyReference)
	}

	Logger.Info("TAS18: ClusterTopologyBinding Drift Detection test completed successfully!")
}

// Test_TAS19_AutoManagedCTLifecycle tests that auto-managed ClusterTopologies (no schedulerTopologyReferences)
// have their KAI Topology automatically created and updated when levels change.
// 1. Create CT with 2 levels (zone, host) — no schedulerTopologyReferences
// 2. Verify KAI Topology auto-created with matching keys
// 3. Verify SchedulerTopologyDrift = False/InSync
// 4. Update CT to 3 levels (add rack)
// 5. Verify KAI Topology recreated with 3 keys
// 6. Verify SchedulerTopologyDrift remains False/InSync
func Test_TAS19_AutoManagedCTLifecycle(t *testing.T) {
	const ctName = "lifecycle-topo"
	ctx := context.Background()

	Logger.Info("1. Initialize test environment (0 nodes needed)")
	tc, cleanup := testctx.PrepareTest(ctx, t, 0)
	defer cleanup()
	tv := topology.NewVerifier(tc.Client, Logger)

	Logger.Info("2. Create CT with 2 levels (zone, host) — no schedulerTopologyReferences")
	levels2 := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: setup.TopologyLabelZone},
		{Domain: corev1alpha1.TopologyDomainHost, Key: setup.TopologyLabelHostname},
	}
	if err := tv.CreateClusterTopology(ctx, ctName, levels2); err != nil {
		t.Fatalf("Failed to create %s: %v", ctName, err)
	}
	defer func() {
		if err := tv.DeleteClusterTopology(ctx, ctName); err != nil {
			Logger.Errorf("Failed to delete %s: %v", ctName, err)
		}
	}()

	Logger.Info("3. Wait for KAI Topology auto-created with 2 keys")
	if err := tv.WaitForKAITopology(ctx, ctName,
		[]string{setup.TopologyLabelZone, setup.TopologyLabelHostname},
		tc.Timeout, tc.Interval); err != nil {
		t.Fatalf("Failed to wait for %s KAI Topology with 2 keys: %v", ctName, err)
	}

	Logger.Info("4. Verify SchedulerTopologyDrift = False/InSync")
	if err := tv.WaitForClusterTopologyCondition(ctx, ctName,
		apicommonconstants.ConditionSchedulerTopologyDrift,
		string(metav1.ConditionFalse),
		apicommonconstants.ConditionReasonInSync,
		tc.Timeout, tc.Interval); err != nil {
		t.Fatalf("Failed to wait for SchedulerTopologyDrift=False/InSync: %v", err)
	}

	Logger.Info("5. Update CT to 3 levels (add rack)")
	levels3 := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: setup.TopologyLabelZone},
		{Domain: corev1alpha1.TopologyDomainRack, Key: setup.TopologyLabelRack},
		{Domain: corev1alpha1.TopologyDomainHost, Key: setup.TopologyLabelHostname},
	}
	if err := tv.UpdateClusterTopologyLevels(ctx, ctName, levels3); err != nil {
		t.Fatalf("Failed to update %s to 3 levels: %v", ctName, err)
	}

	Logger.Info("6. Wait for KAI Topology recreated with 3 keys")
	if err := tv.WaitForKAITopology(ctx, ctName,
		[]string{setup.TopologyLabelZone, setup.TopologyLabelRack, setup.TopologyLabelHostname},
		tc.Timeout, tc.Interval); err != nil {
		t.Fatalf("Failed to wait for %s KAI Topology with 3 keys: %v", ctName, err)
	}

	Logger.Info("7. Verify SchedulerTopologyDrift remains False/InSync")
	if err := tv.WaitForClusterTopologyCondition(ctx, ctName,
		apicommonconstants.ConditionSchedulerTopologyDrift,
		string(metav1.ConditionFalse),
		apicommonconstants.ConditionReasonInSync,
		tc.Timeout, tc.Interval); err != nil {
		t.Fatalf("Failed to wait for SchedulerTopologyDrift=False/InSync after update: %v", err)
	}

	Logger.Info("TAS19: Auto-Managed CT Lifecycle test completed successfully!")
}

// Test_TAS20_PCSTopologyLevelsUnavailableCondition tests the PCS TopologyLevelsUnavailable condition lifecycle.
// The webhook rejects a PCS referencing a non-existent CT, so we simulate the deletion scenario:
// 1. Create the ClusterTopologyBinding (tas20-topology)
// 2. Deploy the PCS (admitted since CT exists)
// 3. Use a zero-replica PCS so it is otherwise quiescent
// 4. Delete the ClusterTopologyBinding (simulates CT removed after job was created)
// 5. Verify TopologyLevelsUnavailable = Unknown/ClusterTopologyNotFound on PCS
// 6. Scale the PCS up while the topology is unavailable
// 7. Verify new pods are created and their PodGroup has no topology constraints
// 8. Scale the PCS back down and wait for pods to be removed
// 9. Re-create the ClusterTopologyBinding
// 10. Verify TopologyLevelsUnavailable = False/AllClusterTopologyLevelsAvailable
func Test_TAS20_PCSTopologyLevelsUnavailableCondition(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 2-node Grove cluster for PCS condition testing")
	tc, cleanup := testctx.PrepareTest(ctx, t, 2,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "tas-topology-condition",
			YAMLPath:     "../yaml/tas-topology-condition.yaml",
			Namespace:    "default",
			ExpectedPods: 0,
		}),
	)
	defer cleanup()
	tv := topology.NewVerifier(tc.Client, Logger)
	podGroupVerifier := podgroup.NewVerifier(tc.Client, Logger)

	levels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: setup.TopologyLabelZone},
		{Domain: corev1alpha1.TopologyDomainRack, Key: setup.TopologyLabelRack},
		{Domain: corev1alpha1.TopologyDomainHost, Key: setup.TopologyLabelHostname},
	}

	Logger.Info("2. Create tas20-topology ClusterTopologyBinding")
	if err := tv.CreateClusterTopology(ctx, "tas20-topology", levels); err != nil {
		t.Fatalf("Failed to create tas20-topology: %v", err)
	}
	defer func() {
		if err := tv.DeleteClusterTopology(ctx, "tas20-topology"); err != nil {
			Logger.Errorf("Failed to delete tas20-topology: %v", err)
		}
	}()

	Logger.Info("3. Apply PCS workload YAML (tas20-topology now exists, webhook admits it)")
	if _, err := tc.ApplyYAMLFile(tc.Workload.YAMLPath); err != nil {
		t.Fatalf("Failed to apply topology condition workload YAML: %v", err)
	}

	Logger.Info("3a. Wait for PCS to be reconciled with CT present")
	if err := tv.WaitForPCSCondition(ctx, "default", "tas-topology-condition",
		apicommonconstants.ConditionTopologyLevelsUnavailable,
		string(metav1.ConditionFalse),
		apicommonconstants.ConditionReasonAllTopologyLevelsAvailable,
		tc.Timeout, tc.Interval); err != nil {
		t.Fatalf("Failed to wait for PCS to be reconciled before deleting CT: %v", err)
	}

	Logger.Info("4. Delete tas20-topology to simulate CT removal after PCS was created")
	if err := tv.DeleteClusterTopology(ctx, "tas20-topology"); err != nil {
		t.Fatalf("Failed to delete tas20-topology: %v", err)
	}

	Logger.Info("5. Wait for TopologyLevelsUnavailable = Unknown/ClusterTopologyNotFound on PCS")
	if err := tv.WaitForPCSCondition(ctx, "default", "tas-topology-condition",
		apicommonconstants.ConditionTopologyLevelsUnavailable,
		string(metav1.ConditionUnknown),
		apicommonconstants.ConditionReasonClusterTopologyNotFound,
		tc.Timeout, tc.Interval); err != nil {
		t.Fatalf("Failed to wait for TopologyLevelsUnavailable=Unknown/ClusterTopologyNotFound: %v", err)
	}

	Logger.Info("6. Scale PCS to 1 while topology is unavailable")
	if err := tc.ScalePCS("tas-topology-condition", 1); err != nil {
		t.Fatalf("Failed to scale tas-topology-condition PCS: %v", err)
	}

	Logger.Info("7. Wait for new pods and verify PodGroup topology constraints were removed")
	if err := tc.WaitForPods(2); err != nil {
		t.Fatalf("Failed to wait for new pods after scaling PCS without topology: %v", err)
	}

	podGangs, err := podgang.ListForPCSReplica(tc.Ctx, tc.Client, tc.Namespace, tc.Workload.Name, 0)
	if err != nil {
		t.Fatalf("Failed to list PodGangs for PCS replica 0: %v", err)
	}
	if len(podGangs) != 1 {
		t.Fatalf("Expected exactly one PodGang for PCS replica 0, got %d", len(podGangs))
	}
	mvuPodGang := &podGangs[0]
	_, hasPodGangTopologyAnnotation := mvuPodGang.Annotations[apicommonconstants.AnnotationTopologyName]
	if hasPodGangTopologyAnnotation {
		t.Fatalf("Expected PodGang %s to have no %q annotation when topology is unavailable", mvuPodGang.Name, apicommonconstants.AnnotationTopologyName)
	}

	workerPCLQName := nameutils.GeneratePodCliqueName(nameutils.ResourceNameReplica{Name: tc.Workload.Name, Replica: 0}, "worker")
	workerPCLQ := &corev1alpha1.PodClique{}
	if err := tc.Client.Get(ctx, client.ObjectKey{Namespace: tc.Namespace, Name: workerPCLQName}, workerPCLQ); err != nil {
		t.Fatalf("Failed to get PodClique after scaling PCS without topology: %v", err)
	}
	_, hasPCLQTopologyAnnotation := workerPCLQ.Annotations[apicommonconstants.AnnotationTopologyName]
	if hasPCLQTopologyAnnotation {
		t.Fatalf("Expected PodClique %s to have no %q annotation when topology is unavailable", workerPCLQName, apicommonconstants.AnnotationTopologyName)
	}

	podGroup := requireSinglePodGroup(t, tc, podGroupVerifier, 0)
	expectedSubGroups := []podgroup.ExpectedSubGroup{
		podgroup.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "worker", 2, ""),
	}
	if err := podGroupVerifier.VerifyPodGroupTopology(podGroup, "", "", expectedSubGroups); err != nil {
		t.Fatalf("Failed to verify topology constraints were removed from new PodGroup: %v", err)
	}

	Logger.Info("8. Scale PCS back to 0 and wait for pods to be removed")
	if err := tc.ScalePCS("tas-topology-condition", 0); err != nil {
		t.Fatalf("Failed to scale tas-topology-condition PCS back to 0: %v", err)
	}
	if _, err := tc.WaitForPodCount(0); err != nil {
		t.Fatalf("Failed to wait for all pods to be removed after scaling PCS back to 0: %v", err)
	}

	Logger.Info("9. Re-create tas20-topology ClusterTopologyBinding")
	if err := tv.CreateClusterTopology(ctx, "tas20-topology", levels); err != nil {
		t.Fatalf("Failed to re-create tas20-topology: %v", err)
	}

	Logger.Info("10. Wait for TopologyLevelsUnavailable = False/AllClusterTopologyLevelsAvailable")
	if err := tv.WaitForPCSCondition(ctx, "default", "tas-topology-condition",
		apicommonconstants.ConditionTopologyLevelsUnavailable,
		string(metav1.ConditionFalse),
		apicommonconstants.ConditionReasonAllTopologyLevelsAvailable,
		tc.Timeout, tc.Interval); err != nil {
		t.Fatalf("Failed to wait for TopologyLevelsUnavailable=False/AllClusterTopologyLevelsAvailable: %v", err)
	}

	Logger.Info("TAS20: PCS TopologyLevelsUnavailable Condition test completed successfully!")
}

// Test_TAS21_TopologyValidationWebhooks verifies validation behavior for topology-related resources:
//  1. The ClusterTopologyBinding validating webhook rejects invalid topology definitions and scheduler references.
//  2. The PodCliqueSet validating webhook allows a child topologyConstraint without topologyName when it
//     can inherit from the PCS topologyConstraint and the referenced ClusterTopologyBinding exists.
func Test_TAS21_TopologyValidationWebhooks(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a Grove cluster for topology webhook validation testing")
	tc, cleanup := testctx.PrepareTest(ctx, t, 0)
	defer cleanup()

	tests := []struct {
		name        string
		ctName      string
		levels      []corev1alpha1.TopologyLevel
		refs        []corev1alpha1.SchedulerTopologyBinding
		errContains []string
	}{
		{
			name:   "duplicate topology domains",
			ctName: "tas21-invalid-topology",
			levels: []corev1alpha1.TopologyLevel{
				{Domain: corev1alpha1.TopologyDomainZone, Key: setup.TopologyLabelZone},
				{Domain: corev1alpha1.TopologyDomainZone, Key: setup.TopologyLabelRack},
			},
			errContains: []string{"spec.levels[1].domain", "Duplicate value"},
		},
		{
			name:   "duplicate scheduler backend references",
			ctName: "tas22-duplicate-backend",
			levels: []corev1alpha1.TopologyLevel{
				{Domain: corev1alpha1.TopologyDomainZone, Key: setup.TopologyLabelZone},
				{Domain: corev1alpha1.TopologyDomainRack, Key: setup.TopologyLabelRack},
			},
			refs: []corev1alpha1.SchedulerTopologyBinding{
				{SchedulerName: string(configv1alpha1.SchedulerNameKai), TopologyReference: "kai-topology-a"},
				{SchedulerName: string(configv1alpha1.SchedulerNameKai), TopologyReference: "kai-topology-b"},
			},
			errContains: []string{"spec.schedulerTopologyReferences[1].schedulerName", "Duplicate value"},
		},
		{
			name:   "unknown scheduler backend reference",
			ctName: "tas22-unknown-backend",
			levels: []corev1alpha1.TopologyLevel{
				{Domain: corev1alpha1.TopologyDomainZone, Key: setup.TopologyLabelZone},
				{Domain: corev1alpha1.TopologyDomainRack, Key: setup.TopologyLabelRack},
			},
			refs: []corev1alpha1.SchedulerTopologyBinding{
				{SchedulerName: "unknown-scheduler", TopologyReference: "topology"},
			},
			errContains: []string{"spec.schedulerTopologyReferences[0].schedulerName", "scheduler backend is not enabled in Grove"},
		},
		{
			name:   "non topology aware scheduler backend reference",
			ctName: "tas22-non-tas-backend",
			levels: []corev1alpha1.TopologyLevel{
				{Domain: corev1alpha1.TopologyDomainZone, Key: setup.TopologyLabelZone},
				{Domain: corev1alpha1.TopologyDomainRack, Key: setup.TopologyLabelRack},
			},
			refs: []corev1alpha1.SchedulerTopologyBinding{
				{SchedulerName: string(configv1alpha1.SchedulerNameKube), TopologyReference: "default-topology"},
			},
			errContains: []string{"spec.schedulerTopologyReferences[0].schedulerName", "scheduler backend does not implement topology-aware scheduling"},
		},
	}

	for _, tcData := range tests {
		t.Run(tcData.name, func(t *testing.T) {
			invalidCT := &corev1alpha1.ClusterTopologyBinding{
				ObjectMeta: metav1.ObjectMeta{Name: tcData.ctName},
				Spec: corev1alpha1.ClusterTopologyBindingSpec{
					Levels:                    tcData.levels,
					SchedulerTopologyBindings: tcData.refs,
				},
			}

			err := tc.Client.Create(ctx, invalidCT)
			if err == nil {
				t.Fatalf("Expected ClusterTopologyBinding validating webhook rejection for %s, but create succeeded", tcData.name)
			}
			for _, want := range tcData.errContains {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Expected validation error containing %q, got: %v", want, err)
				}
			}
		})
	}

	topologyVerifier := topology.NewVerifier(tc.Client, Logger)

	Logger.Info("2. Ensure grove-topology ClusterTopologyBinding exists for PodCliqueSet validation")
	ensureGroveTopology(ctx, t, topologyVerifier)

	Logger.Info("3. Create PodCliqueSet with PCS topologyName and child inherited topology constraint")
	pcs := testutils.NewPodCliqueSetBuilder("tas21-pcs-optional-topology-name", "default", uuid.NewUUID()).
		WithReplicas(1).
		WithTopologyConstraint(&corev1alpha1.TopologyConstraint{
			TopologyName: "grove-topology",
			Pack: &corev1alpha1.TopologyPackConstraint{
				RequiredDomain: corev1alpha1.TopologyDomainZone,
			},
		}).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder("worker").
				WithReplicas(1).
				WithRoleName("worker-role").
				WithMinAvailable(1).
				WithTopologyConstraint(&corev1alpha1.TopologyConstraint{
					Pack: &corev1alpha1.TopologyPackConstraint{
						RequiredDomain: corev1alpha1.TopologyDomainHost,
					},
				}).
				Build(),
		).
		Build()

	if err := tc.Client.Create(ctx, pcs); err != nil {
		t.Fatalf("Expected PodCliqueSet create to succeed with inherited optional topologyName, got: %v", err)
	}

	Logger.Info("TAS21: Topology validation webhook test completed successfully!")
}

// Test_TAS22_PodCliqueSetTopologyCELValidation verifies schema-level CEL validation for
// static PodCliqueSet topologyConstraint shapes.
func Test_TAS22_PodCliqueSetTopologyCELValidation(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a Grove cluster for topology CEL validation testing")
	tc, cleanup := testctx.PrepareTest(ctx, t, 0)
	defer cleanup()

	topologyVerifier := topology.NewVerifier(tc.Client, Logger)

	Logger.Info("2. Ensure grove-topology ClusterTopologyBinding exists for PodCliqueSet CEL validation")
	ensureGroveTopology(ctx, t, topologyVerifier)

	newPCS := func(name string, topologyConstraint *corev1alpha1.TopologyConstraint) *corev1alpha1.PodCliqueSet {
		return testutils.NewPodCliqueSetBuilder(name, "default", uuid.NewUUID()).
			WithReplicas(0).
			WithTopologyConstraint(topologyConstraint).
			WithPodCliqueTemplateSpec(
				testutils.NewPodCliqueTemplateSpecBuilder("worker").
					WithRoleName("worker-role").
					WithMinAvailable(1).
					Build(),
			).
			Build()
	}

	tests := []struct {
		name        string
		pcs         *corev1alpha1.PodCliqueSet
		errContains []string
	}{
		{
			name: "deprecated packDomain on PCS create",
			pcs: newPCS("tas22-legacy-pcs", &corev1alpha1.TopologyConstraint{
				TopologyName: "grove-topology",
				PackDomain:   corev1alpha1.TopologyDomainHost,
			}),
			errContains: []string{"packDomain is deprecated and cannot be used on new workloads; use pack.required"},
		},
		{
			name: "topologyConstraint without pack",
			pcs: newPCS("tas22-missing-pack", &corev1alpha1.TopologyConstraint{
				TopologyName: "grove-topology",
			}),
			errContains: []string{"topologyConstraint must specify pack or deprecated packDomain"},
		},
		{
			name: "empty pack",
			pcs: newPCS("tas22-empty-pack", &corev1alpha1.TopologyConstraint{
				TopologyName: "grove-topology",
				Pack:         &corev1alpha1.TopologyPackConstraint{},
			}),
			errContains: []string{"pack must specify at least one of required or preferred"},
		},
		{
			name: "deprecated packDomain with pack.required",
			pcs: newPCS("tas22-ambiguous-pack", &corev1alpha1.TopologyConstraint{
				TopologyName: "grove-topology",
				PackDomain:   corev1alpha1.TopologyDomainHost,
				Pack: &corev1alpha1.TopologyPackConstraint{
					RequiredDomain: corev1alpha1.TopologyDomainHost,
				},
			}),
			errContains: []string{"must not set both pack.required and deprecated packDomain"},
		},
		{
			name: "deprecated packDomain on PodClique create",
			pcs: func() *corev1alpha1.PodCliqueSet {
				pcs := newPCS("tas22-legacy-pclq", &corev1alpha1.TopologyConstraint{
					TopologyName: "grove-topology",
					Pack: &corev1alpha1.TopologyPackConstraint{
						RequiredDomain: corev1alpha1.TopologyDomainRack,
					},
				})
				pcs.Spec.Template.Cliques[0].TopologyConstraint = &corev1alpha1.TopologyConstraint{
					PackDomain: corev1alpha1.TopologyDomainHost,
				}
				return pcs
			}(),
			errContains: []string{"packDomain is deprecated and cannot be used on new workloads; use pack.required"},
		},
		{
			name: "deprecated packDomain on PodCliqueScalingGroup create",
			pcs: func() *corev1alpha1.PodCliqueSet {
				pcs := newPCS("tas22-legacy-pcsg", &corev1alpha1.TopologyConstraint{
					TopologyName: "grove-topology",
					Pack: &corev1alpha1.TopologyPackConstraint{
						RequiredDomain: corev1alpha1.TopologyDomainRack,
					},
				})
				replicas := int32(1)
				minAvailable := int32(1)
				pcs.Spec.Template.PodCliqueScalingGroupConfigs = []corev1alpha1.PodCliqueScalingGroupConfig{
					{
						Name:         "workers",
						CliqueNames:  []string{"worker"},
						Replicas:     &replicas,
						MinAvailable: &minAvailable,
						TopologyConstraint: &corev1alpha1.TopologyConstraint{
							PackDomain: corev1alpha1.TopologyDomainHost,
						},
					},
				}
				return pcs
			}(),
			errContains: []string{"packDomain is deprecated and cannot be used on new workloads; use pack.required"},
		},
	}

	Logger.Info("3. Verify invalid PodCliqueSet topologyConstraint shapes are rejected by CEL")
	for _, tcData := range tests {
		t.Run(tcData.name, func(t *testing.T) {
			err := tc.Client.Create(ctx, tcData.pcs)
			if err == nil {
				t.Fatalf("Expected PodCliqueSet CEL validation rejection for %s, but create succeeded", tcData.name)
			}
			for _, want := range tcData.errContains {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Expected validation error containing %q, got: %v", want, err)
				}
			}
		})
	}

	Logger.Info("4. Verify preferred-only pack passes CEL validation")
	validPreferredOnlyPCS := newPCS("tas22-valid-preferred-only", &corev1alpha1.TopologyConstraint{
		TopologyName: "grove-topology",
		Pack: &corev1alpha1.TopologyPackConstraint{
			PreferredDomain: corev1alpha1.TopologyDomainHost,
		},
	})
	if err := tc.Client.Create(ctx, validPreferredOnlyPCS); err != nil {
		t.Fatalf("Expected preferred-only pack PodCliqueSet create to pass CEL validation, got: %v", err)
	}

	Logger.Info("TAS22: PodCliqueSet topology CEL validation test completed successfully!")
}

// Test_TAS23_PreferredPackConstraintPropagation verifies that Grove preferred pack domains
// are propagated to KAI PodGroup topology constraints. It does not assert placement because
// preferred constraints are soft scheduling hints.
func Test_TAS23_PreferredPackConstraintPropagation(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Initialize a 28-node Grove cluster for preferred topology propagation testing")
	expectedPods := 3 // 2 PCSG worker replicas + 1 standalone router
	tc, cleanup := testctx.PrepareTest(ctx, t, 28,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "tas-preferred-pack",
			YAMLPath:     "../yaml/tas-preferred-pack.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		}),
	)
	defer cleanup()
	topologyVerifier := topology.NewVerifier(tc.Client, Logger)
	podGroupVerifier := podgroup.NewVerifier(tc.Client, Logger)

	ensureGroveTopology(ctx, t, topologyVerifier)
	Logger.Info("2. Deploy workload (TAS23: preferred topology pack constraints)")
	if _, err := DeployWorkloadAndGetPods(tc, expectedPods); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	Logger.Info("3. Verify base KAI PodGroup required and preferred topology constraints")
	basePodGroup := requireSinglePodGroup(t, tc, podGroupVerifier, 0)

	workersParent := podgroup.CreateExpectedPCSGParentSubGroup(tc.Workload.Name, 0, "workers", 0, "")
	workersParent.PreferredTopologyLevel = setup.TopologyLabelHostname
	router := podgroup.CreateExpectedStandalonePCLQSubGroup(tc.Workload.Name, 0, "router", 1, "")
	router.PreferredTopologyLevel = setup.TopologyLabelHostname
	expectedBaseSubGroups := []podgroup.ExpectedSubGroup{
		workersParent,
		podgroup.CreateExpectedPCLQInPCSGSubGroup(tc.Workload.Name, 0, "workers", 0, "worker", 1, ""),
		router,
	}
	if err := podGroupVerifier.VerifyPodGroupTopology(basePodGroup, setup.TopologyLabelBlock, setup.TopologyLabelRack, expectedBaseSubGroups); err != nil {
		t.Fatalf("Failed to verify base KAI PodGroup topology: %v", err)
	}

	Logger.Info("4. Verify scaled PCSG KAI PodGroup preferred topology constraint")
	podGroups, err := podGroupVerifier.GetKAIPodGroupsForPCS(tc.Ctx, tc.Namespace, tc.Workload.Name)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroups: %v", err)
	}
	workersFQN := nameutils.GeneratePodCliqueScalingGroupName(
		nameutils.ResourceNameReplica{Name: tc.Workload.Name, Replica: 0},
		"workers",
	)
	scaledPodGangName := nameutils.CreatePodGangNameFromPCSGFQN(workersFQN, 0)
	scaledPodGroup, err := podgroup.FilterPodGroupByOwner(podGroups, scaledPodGangName)
	if err != nil {
		t.Fatalf("Failed to find scaled PodGroup %s: %v", scaledPodGangName, err)
	}
	expectedScaledSubGroups := []podgroup.ExpectedSubGroup{
		podgroup.CreateExpectedPCLQInPCSGSubGroupNoParent(tc.Workload.Name, 0, "workers", 1, "worker", 1, ""),
	}
	if err := podGroupVerifier.VerifyPodGroupTopology(scaledPodGroup, "", setup.TopologyLabelHostname, expectedScaledSubGroups); err != nil {
		t.Fatalf("Failed to verify scaled KAI PodGroup topology: %v", err)
	}

	Logger.Info("5. Verify TopologyLevelsUnavailable = False")
	if err := topologyVerifier.WaitForPCSCondition(ctx, "default", tc.Workload.Name,
		apicommonconstants.ConditionTopologyLevelsUnavailable,
		string(metav1.ConditionFalse),
		apicommonconstants.ConditionReasonAllTopologyLevelsAvailable,
		tc.Timeout, tc.Interval); err != nil {
		t.Fatalf("Failed to verify TopologyLevelsUnavailable is False: %v", err)
	}

	Logger.Info("TAS23: Preferred Pack Constraint Propagation test completed successfully!")
}

// ensureGroveTopology creates the shared "grove-topology" ClusterTopologyBinding if it does not already
// exist. TAS1-TAS16 all reference this topology; TAS1 is expected to run first and create it, but
// each test calls this so that tests can also run in isolation.
func ensureGroveTopology(ctx context.Context, t *testing.T, tv *topology.Verifier) {
	t.Helper()
	if err := tv.EnsureClusterTopology(ctx, "grove-topology", groveTopologyLevels); err != nil {
		t.Fatalf("Failed to ensure grove-topology ClusterTopologyBinding: %v", err)
	}
}

// DeployWorkloadAndGetPods deploys workload, waits for pods to be ready, and returns the pod list
func DeployWorkloadAndGetPods(tc *testctx.TestContext, expectedPods int) ([]v1.Pod, error) {
	if _, err := tc.DeployAndVerifyWorkload(); err != nil {
		return nil, fmt.Errorf("failed to deploy workload: %w", err)
	}

	Logger.Info("Wait for all pods to be scheduled and running")
	if err := tc.WaitForPods(expectedPods); err != nil {
		return nil, fmt.Errorf("failed to wait for pods ready: %w", err)
	}

	Logger.Info("Get all pods once for verification")
	podList, err := tc.ListPods()
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	return podList.Items, nil
}

// requireSinglePodGroup looks up the KAI PodGroup for a PCS replica that has exactly
// one PodGang and fails the test if the cardinality differs or any lookup fails.
func requireSinglePodGroup(t *testing.T, tc *testctx.TestContext, podGroupVerifier *podgroup.Verifier, pcsReplica int) *kaischedulingv2alpha2.PodGroup {
	t.Helper()
	podGangs, err := podgang.ListForPCSReplica(tc.Ctx, tc.Client, tc.Namespace, tc.Workload.Name, pcsReplica)
	if err != nil {
		t.Fatalf("Failed to list PodGangs for PCS replica %d: %v", pcsReplica, err)
	}
	if len(podGangs) != 1 {
		t.Fatalf("Expected exactly one PodGang for PCS replica %d, got %d", pcsReplica, len(podGangs))
	}
	podGroup, err := podGroupVerifier.GetKAIPodGroupForPodGang(tc.Ctx, tc.Namespace, tc.Workload.Name, podGangs[0].Name)
	if err != nil {
		t.Fatalf("Failed to get KAI PodGroup for PodGang %s: %v", podGangs[0].Name, err)
	}
	return podGroup
}

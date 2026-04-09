//go:build e2e

// /*
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
// */

package tests

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"testing"

	"github.com/ai-dynamo/grove/operator/e2e/utils"
	v1 "k8s.io/api/core/v1"
)

const (
	rsWorkloadName = "rs-test"
	rsYAMLPath     = "../yaml/workload-resource-sharing.yaml"
	rsNamespace    = "default"
)

// --- RC name inventories ---
//
// Naming convention:
//   AllReplicas:  <owner>-all-<tpl>
//   PerReplica:   <owner>-<idx>-<tpl>
//
// Hierarchy (from YAML):
//   PCS "rs-test":
//     int-tpl5/AllReplicas (broadcast)
//     ext-tpl/PerReplica   (broadcast)
//     int-tpl/PerReplica   (filter: childCliqueNames: [worker-a])
//     int-tpl7/AllReplicas (filter: childScalingGroupNames: [sga])
//   PCLQ "worker-a" (standalone):
//     int-tpl4/PerReplica
//   PCSG "sga" (replicas=2):
//     int-tpl2/AllReplicas (broadcast)
//     int-tpl3/PerReplica  (broadcast)
//     int-tpl8/AllReplicas (filter: childCliqueNames: [worker-b])
//   PCLQ "worker-b" in sga:
//     int-tpl6/AllReplicas
//
// Filter coverage:
//   PCS  → childCliqueNames        (int-tpl  targets worker-a only)
//   PCS  → childScalingGroupNames  (int-tpl7 targets sga only)
//   PCSG → childCliqueNames        (int-tpl8 targets worker-b only)

// initialRCNames: PCS=1, sga=2, worker-a=1 pod. Total: 11 RCs.
func initialRCNames() []string {
	return []string{
		// PCS level
		"rs-test-all-int-tpl5",
		"rs-test-0-ext-tpl",
		"rs-test-0-int-tpl",
		"rs-test-all-int-tpl7",
		// Standalone PCLQ worker-a
		"rs-test-0-worker-a-0-int-tpl4",
		// PCSG sga
		"rs-test-0-sga-all-int-tpl2",
		"rs-test-0-sga-0-int-tpl3",
		"rs-test-0-sga-1-int-tpl3",
		"rs-test-0-sga-all-int-tpl8",
		// PCLQ worker-b in sga
		"rs-test-0-sga-0-worker-b-all-int-tpl6",
		"rs-test-0-sga-1-worker-b-all-int-tpl6",
	}
}

// pcsgScaleInRCNames: sga 2→1. Total: 9 RCs.
func pcsgScaleInRCNames() []string {
	return []string{
		"rs-test-all-int-tpl5",
		"rs-test-0-ext-tpl",
		"rs-test-0-int-tpl",
		"rs-test-all-int-tpl7",
		"rs-test-0-worker-a-0-int-tpl4",
		"rs-test-0-sga-all-int-tpl2",
		"rs-test-0-sga-0-int-tpl3",
		"rs-test-0-sga-all-int-tpl8",
		"rs-test-0-sga-0-worker-b-all-int-tpl6",
	}
}

// pcsgScaleOutRCNames: sga 1→3. Total: 13 RCs.
func pcsgScaleOutRCNames() []string {
	return []string{
		"rs-test-all-int-tpl5",
		"rs-test-0-ext-tpl",
		"rs-test-0-int-tpl",
		"rs-test-all-int-tpl7",
		"rs-test-0-worker-a-0-int-tpl4",
		"rs-test-0-sga-all-int-tpl2",
		"rs-test-0-sga-0-int-tpl3",
		"rs-test-0-sga-1-int-tpl3",
		"rs-test-0-sga-2-int-tpl3",
		"rs-test-0-sga-all-int-tpl8",
		"rs-test-0-sga-0-worker-b-all-int-tpl6",
		"rs-test-0-sga-1-worker-b-all-int-tpl6",
		"rs-test-0-sga-2-worker-b-all-int-tpl6",
	}
}

// pclqScaleOutRCNames: worker-a 1→2 (sga still at 3). Total: 14 RCs.
func pclqScaleOutRCNames() []string {
	return append(pcsgScaleOutRCNames(), "rs-test-0-worker-a-1-int-tpl4")
}

// pcsScaleOutRCNames: PCS 1→2.
// Rep 0 keeps sga=3, worker-a=1 (13 RCs from pcsgScaleOutRCNames).
// Rep 1 created from template: sga=2, worker-a=1 (9 new RCs).
// Total: 22 RCs.
func pcsScaleOutRCNames() []string {
	names := pcsgScaleOutRCNames()
	return append(names,
		// PCS PerReplica for rep 1
		"rs-test-1-ext-tpl",
		"rs-test-1-int-tpl",
		// Standalone PCLQ worker-a rep 1
		"rs-test-1-worker-a-0-int-tpl4",
		// PCSG sga rep 1 (template replicas=2)
		"rs-test-1-sga-all-int-tpl2",
		"rs-test-1-sga-0-int-tpl3",
		"rs-test-1-sga-1-int-tpl3",
		"rs-test-1-sga-all-int-tpl8",
		// PCLQ worker-b in sga rep 1
		"rs-test-1-sga-0-worker-b-all-int-tpl6",
		"rs-test-1-sga-1-worker-b-all-int-tpl6",
	)
}

// --- Pod RC ref maps ---
//
// Filter behaviour on pod injection (matchNames = [pclqTemplateName, pcsgConfigName]):
//   worker-a pod: matchNames=["worker-a"]
//     int-tpl5 (broadcast)          → injected
//     ext-tpl  (broadcast)          → injected
//     int-tpl  (filter: worker-a)   → injected (matches "worker-a")
//     int-tpl7 (filter: sga)        → NOT injected ("worker-a" ∉ [sga])
//   worker-b pod in sga: matchNames=["worker-b","sga"]
//     int-tpl5 (broadcast)          → injected
//     ext-tpl  (broadcast)          → injected
//     int-tpl  (filter: worker-a)   → NOT injected ("worker-b","sga" ∉ [worker-a])
//     int-tpl7 (filter: sga)        → injected (matches "sga")
//     int-tpl8 (PCSG filter: worker-b) → injected (matches "worker-b")

// initialPodRefs: 3 pods (PCS=1, sga=2, worker-a=1).
func initialPodRefs() map[string][]string {
	return map[string][]string{
		"rs-test-0-worker-a": {
			"rs-test-all-int-tpl5",
			"rs-test-0-ext-tpl",
			"rs-test-0-int-tpl",
			"rs-test-0-worker-a-0-int-tpl4",
		},
		"rs-test-0-sga-0-worker-b": {
			"rs-test-all-int-tpl5",
			"rs-test-0-ext-tpl",
			"rs-test-all-int-tpl7",
			"rs-test-0-sga-all-int-tpl2",
			"rs-test-0-sga-0-int-tpl3",
			"rs-test-0-sga-all-int-tpl8",
			"rs-test-0-sga-0-worker-b-all-int-tpl6",
		},
		"rs-test-0-sga-1-worker-b": {
			"rs-test-all-int-tpl5",
			"rs-test-0-ext-tpl",
			"rs-test-all-int-tpl7",
			"rs-test-0-sga-all-int-tpl2",
			"rs-test-0-sga-1-int-tpl3",
			"rs-test-0-sga-all-int-tpl8",
			"rs-test-0-sga-1-worker-b-all-int-tpl6",
		},
	}
}

// pcsgScaleInPodRefs: 2 pods (sga=1).
func pcsgScaleInPodRefs() map[string][]string {
	return map[string][]string{
		"rs-test-0-worker-a": {
			"rs-test-all-int-tpl5",
			"rs-test-0-ext-tpl",
			"rs-test-0-int-tpl",
			"rs-test-0-worker-a-0-int-tpl4",
		},
		"rs-test-0-sga-0-worker-b": {
			"rs-test-all-int-tpl5",
			"rs-test-0-ext-tpl",
			"rs-test-all-int-tpl7",
			"rs-test-0-sga-all-int-tpl2",
			"rs-test-0-sga-0-int-tpl3",
			"rs-test-0-sga-all-int-tpl8",
			"rs-test-0-sga-0-worker-b-all-int-tpl6",
		},
	}
}

// pcsgScaleOutPodRefs: 4 pods (sga=3).
func pcsgScaleOutPodRefs() map[string][]string {
	return map[string][]string{
		"rs-test-0-worker-a": {
			"rs-test-all-int-tpl5",
			"rs-test-0-ext-tpl",
			"rs-test-0-int-tpl",
			"rs-test-0-worker-a-0-int-tpl4",
		},
		"rs-test-0-sga-0-worker-b": {
			"rs-test-all-int-tpl5",
			"rs-test-0-ext-tpl",
			"rs-test-all-int-tpl7",
			"rs-test-0-sga-all-int-tpl2",
			"rs-test-0-sga-0-int-tpl3",
			"rs-test-0-sga-all-int-tpl8",
			"rs-test-0-sga-0-worker-b-all-int-tpl6",
		},
		"rs-test-0-sga-1-worker-b": {
			"rs-test-all-int-tpl5",
			"rs-test-0-ext-tpl",
			"rs-test-all-int-tpl7",
			"rs-test-0-sga-all-int-tpl2",
			"rs-test-0-sga-1-int-tpl3",
			"rs-test-0-sga-all-int-tpl8",
			"rs-test-0-sga-1-worker-b-all-int-tpl6",
		},
		"rs-test-0-sga-2-worker-b": {
			"rs-test-all-int-tpl5",
			"rs-test-0-ext-tpl",
			"rs-test-all-int-tpl7",
			"rs-test-0-sga-all-int-tpl2",
			"rs-test-0-sga-2-int-tpl3",
			"rs-test-0-sga-all-int-tpl8",
			"rs-test-0-sga-2-worker-b-all-int-tpl6",
		},
	}
}

// pcsScaleOutPodRefs: 7 pods (PCS=2, rep 0 sga=3, rep 1 sga=2 from template).
func pcsScaleOutPodRefs() map[string][]string {
	refs := pcsgScaleOutPodRefs()
	refs["rs-test-1-worker-a"] = []string{
		"rs-test-all-int-tpl5",
		"rs-test-1-ext-tpl",
		"rs-test-1-int-tpl",
		"rs-test-1-worker-a-0-int-tpl4",
	}
	refs["rs-test-1-sga-0-worker-b"] = []string{
		"rs-test-all-int-tpl5",
		"rs-test-1-ext-tpl",
		"rs-test-all-int-tpl7",
		"rs-test-1-sga-all-int-tpl2",
		"rs-test-1-sga-0-int-tpl3",
		"rs-test-1-sga-all-int-tpl8",
		"rs-test-1-sga-0-worker-b-all-int-tpl6",
	}
	refs["rs-test-1-sga-1-worker-b"] = []string{
		"rs-test-all-int-tpl5",
		"rs-test-1-ext-tpl",
		"rs-test-all-int-tpl7",
		"rs-test-1-sga-all-int-tpl2",
		"rs-test-1-sga-1-int-tpl3",
		"rs-test-1-sga-all-int-tpl8",
		"rs-test-1-sga-1-worker-b-all-int-tpl6",
	}
	return refs
}

// Test_RS1_HierarchicalResourceSharing verifies ResourceClaim lifecycle at all
// hierarchy levels. It starts with PCS=1 to test PCSG and PCLQ scaling in
// isolation, then tests PCS scale-out/in at the end.
//
// Key filter verifications:
//   - int-tpl5 (PCS AllReplicas, broadcast) appears on ALL pods
//   - int-tpl  (PCS PerReplica, filter: childCliqueNames: [worker-a]) appears ONLY on worker-a pods
//   - int-tpl7 (PCS AllReplicas, filter: childScalingGroupNames: [sga]) appears ONLY on worker-b-in-sga pods
//   - int-tpl8 (PCSG AllReplicas, filter: childCliqueNames: [worker-b]) appears on worker-b-in-sga pods
func Test_RS1_HierarchicalResourceSharing(t *testing.T) {
	ctx := context.Background()

	Logger.Info("1. Prepare cluster")
	clients, cleanup := PrepareTestCluster(ctx, t, 7)
	defer cleanup()

	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clients.Clientset,
		RestConfig:    clients.RestConfig,
		DynamicClient: clients.DynamicClient,
		Namespace:     rsNamespace,
		Timeout:       DefaultPollTimeout,
		Interval:      DefaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         rsWorkloadName,
			YAMLPath:     rsYAMLPath,
			Namespace:    rsNamespace,
			ExpectedPods: 3,
		},
	}

	rcLabelSelector := fmt.Sprintf("app.kubernetes.io/managed-by=grove-operator,app.kubernetes.io/part-of=%s,app.kubernetes.io/component=resource-claim", rsWorkloadName)
	podSelector := fmt.Sprintf("app.kubernetes.io/part-of=%s", rsWorkloadName)

	Logger.Info("2. Deploy workload (PCS=1, sga=2, worker-a=1)")
	_, err := applyYAMLFile(tc, rsYAMLPath)
	if err != nil {
		t.Fatalf("Failed to apply resource sharing workload: %v", err)
	}

	// --- Verify initial state (11 RCs, 3 pods) ---

	Logger.Info("3. Verify initial ResourceClaim creation (11 RCs)")
	verifyRCState(t, tc, rcLabelSelector, 11, initialRCNames())

	Logger.Info("4. Verify ResourceClaim labels")
	rcList, err := utils.ListResourceClaims(ctx, clients.DynamicClient, rsNamespace, rcLabelSelector)
	if err != nil {
		t.Fatalf("Failed to list ResourceClaims for label check: %v", err)
	}
	for _, rc := range rcList.Items {
		labels := rc.GetLabels()
		if labels["app.kubernetes.io/managed-by"] != "grove-operator" {
			t.Errorf("RC %s missing managed-by label", rc.GetName())
		}
		if labels["app.kubernetes.io/part-of"] != rsWorkloadName {
			t.Errorf("RC %s missing part-of label", rc.GetName())
		}
		if labels["app.kubernetes.io/component"] != "resource-claim" {
			t.Errorf("RC %s missing component label", rc.GetName())
		}
	}

	Logger.Info("5. Verify pod ResourceClaim references (3 pods)")
	verifyPodState(t, tc, podSelector, 3, initialPodRefs())

	// --- PCSG scale-in/out (single PCS replica) ---

	Logger.Info("6. Scale PCSG sga from 2 to 1")
	if err := scalePodCliqueScalingGroup(tc, "rs-test-0-sga", 1); err != nil {
		t.Fatalf("Failed to scale PCSG: %v", err)
	}
	verifyRCState(t, tc, rcLabelSelector, 9, pcsgScaleInRCNames())
	verifyPodState(t, tc, podSelector, 2, pcsgScaleInPodRefs())
	Logger.Info("   Verified 9 RCs and 2 pods after PCSG scale-in")

	Logger.Info("7. Scale PCSG sga from 1 to 3")
	if err := scalePodCliqueScalingGroup(tc, "rs-test-0-sga", 3); err != nil {
		t.Fatalf("Failed to scale PCSG: %v", err)
	}
	verifyRCState(t, tc, rcLabelSelector, 13, pcsgScaleOutRCNames())
	verifyPodState(t, tc, podSelector, 4, pcsgScaleOutPodRefs())
	Logger.Info("   Verified 13 RCs and 4 pods after PCSG scale-out")

	// --- PCLQ scale-out/in (single PCS replica) ---

	Logger.Info("8. Scale standalone PCLQ worker-a from 1 to 2")
	if err := scalePodClique(tc, "rs-test-0-worker-a", 2); err != nil {
		t.Fatalf("Failed to scale PCLQ: %v", err)
	}
	verifyRCState(t, tc, rcLabelSelector, 14, pclqScaleOutRCNames())
	_, err = utils.WaitForPodCount(ctx, clients.Clientset, rsNamespace, podSelector, 5, tc.Timeout, tc.Interval)
	if err != nil {
		t.Fatalf("Expected 5 pods after PCLQ scale-out but timed out: %v", err)
	}
	Logger.Info("   Verified 14 RCs and 5 pods after PCLQ scale-out")

	Logger.Info("9. Scale standalone PCLQ worker-a from 2 to 1")
	if err := scalePodClique(tc, "rs-test-0-worker-a", 1); err != nil {
		t.Fatalf("Failed to scale PCLQ: %v", err)
	}
	verifyRCState(t, tc, rcLabelSelector, 13, pcsgScaleOutRCNames())
	verifyPodState(t, tc, podSelector, 4, pcsgScaleOutPodRefs())
	Logger.Info("   Verified 13 RCs and 4 pods after PCLQ scale-in")

	// --- PCS scale-out/in ---
	// Rep 0 retains sga=3 from step 7. Rep 1 is created from template (sga=2).

	Logger.Info("10. Scale PCS from 1 to 2")
	if err := scalePodCliqueSet(tc, rsWorkloadName, 2); err != nil {
		t.Fatalf("Failed to scale PCS to 2: %v", err)
	}
	verifyRCState(t, tc, rcLabelSelector, 22, pcsScaleOutRCNames())
	verifyPodState(t, tc, podSelector, 7, pcsScaleOutPodRefs())
	Logger.Info("   Verified 22 RCs and 7 pods after PCS scale-out")

	Logger.Info("11. Scale PCS from 2 to 1")
	if err := scalePodCliqueSet(tc, rsWorkloadName, 1); err != nil {
		t.Fatalf("Failed to scale PCS to 1: %v", err)
	}
	verifyRCState(t, tc, rcLabelSelector, 13, pcsgScaleOutRCNames())
	verifyPodState(t, tc, podSelector, 4, pcsgScaleOutPodRefs())
	Logger.Info("   Verified 13 RCs and 4 pods after PCS scale-in")

	Logger.Info("Hierarchical resource sharing e2e test completed successfully!")
}

// verifyRCState waits for the expected RC count and verifies exact RC names.
func verifyRCState(t *testing.T, tc TestContext, labelSelector string, expectedCount int, expectedNames []string) {
	t.Helper()
	err := utils.WaitForResourceClaimCount(tc.Ctx, tc.DynamicClient, tc.Namespace, labelSelector, expectedCount, tc.Timeout, tc.Interval)
	if err != nil {
		t.Fatalf("Expected %d ResourceClaims but timed out: %v", expectedCount, err)
	}

	rcList, err := utils.ListResourceClaims(tc.Ctx, tc.DynamicClient, tc.Namespace, labelSelector)
	if err != nil {
		t.Fatalf("Failed to list ResourceClaims: %v", err)
	}

	actualNames := utils.ResourceClaimNames(rcList)
	sort.Strings(actualNames)
	sortedExpected := make([]string, len(expectedNames))
	copy(sortedExpected, expectedNames)
	sort.Strings(sortedExpected)
	if !slices.Equal(actualNames, sortedExpected) {
		t.Fatalf("RC name mismatch\nexpected (%d): %v\nactual   (%d): %v", len(sortedExpected), sortedExpected, len(actualNames), actualNames)
	}
}

// verifyPodState waits for the expected pod count and verifies RC references in pod specs.
func verifyPodState(t *testing.T, tc TestContext, podSelector string, expectedCount int, expectedRefs map[string][]string) {
	t.Helper()
	pods, err := utils.WaitForPodCount(tc.Ctx, tc.Clientset, tc.Namespace, podSelector, expectedCount, tc.Timeout, tc.Interval)
	if err != nil {
		t.Fatalf("Expected %d pods but timed out: %v", expectedCount, err)
	}
	verifyPodResourceClaimRefs(t, pods.Items, expectedRefs)
}

// verifyPodResourceClaimRefs checks that each pod's spec.resourceClaims and
// container resources.claims reference the correct ResourceClaim names based
// on the pod's PodClique label.
func verifyPodResourceClaimRefs(t *testing.T, pods []v1.Pod, expectedRefsByPCLQ map[string][]string) {
	t.Helper()

	matchedPCLQs := make(map[string]bool)

	for _, pod := range pods {
		pclqName := pod.Labels[LabelPodClique]
		if pclqName == "" {
			t.Errorf("Pod %s missing %s label", pod.Name, LabelPodClique)
			continue
		}

		expectedRCNames, ok := expectedRefsByPCLQ[pclqName]
		if !ok {
			t.Errorf("Unexpected PCLQ %s for pod %s", pclqName, pod.Name)
			continue
		}
		matchedPCLQs[pclqName] = true

		podClaimNames := extractPodResourceClaimNames(pod.Spec)
		sort.Strings(podClaimNames)
		sortedExpected := make([]string, len(expectedRCNames))
		copy(sortedExpected, expectedRCNames)
		sort.Strings(sortedExpected)

		if !slices.Equal(podClaimNames, sortedExpected) {
			t.Errorf("Pod %s (pclq=%s) spec.resourceClaims mismatch\n  expected: %v\n  actual:   %v",
				pod.Name, pclqName, sortedExpected, podClaimNames)
		}

		for _, container := range pod.Spec.Containers {
			containerClaimNames := extractContainerClaimNames(container)
			sort.Strings(containerClaimNames)
			if !slices.Equal(containerClaimNames, sortedExpected) {
				t.Errorf("Pod %s container %s resources.claims mismatch\n  expected: %v\n  actual:   %v",
					pod.Name, container.Name, sortedExpected, containerClaimNames)
			}
		}
	}

	for pclq := range expectedRefsByPCLQ {
		if !matchedPCLQs[pclq] {
			t.Errorf("No pod found for expected PodClique %s", pclq)
		}
	}
}

func extractPodResourceClaimNames(spec v1.PodSpec) []string {
	names := make([]string, 0, len(spec.ResourceClaims))
	for _, rc := range spec.ResourceClaims {
		names = append(names, rc.Name)
	}
	return names
}

func extractContainerClaimNames(container v1.Container) []string {
	names := make([]string, 0, len(container.Resources.Claims))
	for _, claim := range container.Resources.Claims {
		names = append(names, claim.Name)
	}
	return names
}

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
	"strings"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/gvk"
	"github.com/ai-dynamo/grove/operator/e2e/grove/workload"
	k8sutils "github.com/ai-dynamo/grove/operator/e2e/k8s"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/resources"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	v1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
//     ext-ns-tpl/AllReplicas (cross-namespace: rs-shared)
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

// initialRCNames: PCS=1, sga=2, worker-a=1 pod. Total: 12 RCs.
func initialRCNames() []string {
	return []string{
		// PCS level
		"rs-test-all-int-tpl5",
		"rs-test-0-ext-tpl",
		"rs-test-0-int-tpl",
		"rs-test-all-int-tpl7",
		// Standalone PCLQ worker-a
		"rs-test-0-worker-a-0-int-tpl4",
		"rs-test-0-worker-a-all-ext-ns-tpl",
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

// pcsgScaleInRCNames: sga 2→1. Total: 10 RCs.
func pcsgScaleInRCNames() []string {
	return []string{
		"rs-test-all-int-tpl5",
		"rs-test-0-ext-tpl",
		"rs-test-0-int-tpl",
		"rs-test-all-int-tpl7",
		"rs-test-0-worker-a-0-int-tpl4",
		"rs-test-0-worker-a-all-ext-ns-tpl",
		"rs-test-0-sga-all-int-tpl2",
		"rs-test-0-sga-0-int-tpl3",
		"rs-test-0-sga-all-int-tpl8",
		"rs-test-0-sga-0-worker-b-all-int-tpl6",
	}
}

// pcsgScaleOutRCNames: sga 1→3. Total: 14 RCs.
func pcsgScaleOutRCNames() []string {
	return []string{
		"rs-test-all-int-tpl5",
		"rs-test-0-ext-tpl",
		"rs-test-0-int-tpl",
		"rs-test-all-int-tpl7",
		"rs-test-0-worker-a-0-int-tpl4",
		"rs-test-0-worker-a-all-ext-ns-tpl",
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

// pclqScaleOutRCNames: worker-a 1→2 (sga still at 3). Total: 15 RCs.
func pclqScaleOutRCNames() []string {
	return append(pcsgScaleOutRCNames(), "rs-test-0-worker-a-1-int-tpl4")
}

// pcsScaleOutRCNames: PCS 1→2.
// Rep 0 keeps sga=3, worker-a=1 (14 RCs from pcsgScaleOutRCNames).
// Rep 1 created from template: sga=2, worker-a=1 (10 new RCs).
// Total: 24 RCs.
func pcsScaleOutRCNames() []string {
	names := pcsgScaleOutRCNames()
	return append(names,
		// PCS PerReplica for rep 1
		"rs-test-1-ext-tpl",
		"rs-test-1-int-tpl",
		// Standalone PCLQ worker-a rep 1
		"rs-test-1-worker-a-0-int-tpl4",
		"rs-test-1-worker-a-all-ext-ns-tpl",
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
			"rs-test-0-worker-a-all-ext-ns-tpl",
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
			"rs-test-0-worker-a-all-ext-ns-tpl",
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
			"rs-test-0-worker-a-all-ext-ns-tpl",
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
		"rs-test-1-worker-a-all-ext-ns-tpl",
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
	tc, cleanup := testctx.PrepareTest(ctx, t, 7,
		testctx.WithNamespace(rsNamespace),
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         rsWorkloadName,
			YAMLPath:     rsYAMLPath,
			Namespace:    rsNamespace,
			ExpectedPods: 3,
		}),
	)
	defer cleanup()

	crClient := tc.Client
	rcLabelSelector := fmt.Sprintf("%s=%s,%s=%s,%s=resource-claim", apicommon.LabelManagedByKey, apicommon.LabelManagedByValue, apicommon.LabelPartOfKey, rsWorkloadName, apicommon.LabelComponentKey)
	podSelector := fmt.Sprintf("%s=%s", apicommon.LabelPartOfKey, rsWorkloadName)

	Logger.Info("2. Create cross-namespace RCT (rs-shared/ext-ns-tpl)")
	createCrossNamespaceRCT(t, tc)

	Logger.Info("3. Deploy workload (PCS=1, sga=2, worker-a=1)")
	_, err := tc.ApplyYAMLFile(rsYAMLPath)
	if err != nil {
		t.Fatalf("Failed to apply resource sharing workload: %v", err)
	}

	// --- Verify initial state (12 RCs, 3 pods) ---

	Logger.Info("4. Verify initial ResourceClaim creation (12 RCs)")
	verifyRCState(t, tc, rcLabelSelector, 12, initialRCNames())

	Logger.Info("5. Verify ResourceClaim labels")
	rcList, err := k8sutils.ListResourceClaims(ctx, crClient, rsNamespace, rcLabelSelector)
	if err != nil {
		t.Fatalf("Failed to list ResourceClaims for label check: %v", err)
	}
	for _, rc := range rcList.Items {
		if rc.Labels[apicommon.LabelManagedByKey] != apicommon.LabelManagedByValue {
			t.Errorf("RC %s missing managed-by label", rc.Name)
		}
		if rc.Labels[apicommon.LabelPartOfKey] != rsWorkloadName {
			t.Errorf("RC %s missing part-of label", rc.Name)
		}
		if rc.Labels[apicommon.LabelComponentKey] != "resource-claim" {
			t.Errorf("RC %s missing component label", rc.Name)
		}
	}

	Logger.Info("6. Verify pod ResourceClaim references (3 pods)")
	verifyPodState(t, tc, podSelector, 3, initialPodRefs())

	Logger.Info("7. Verify ownerReferences on initial ResourceClaims")
	verifyOwnerReferences(t, tc, rcLabelSelector, initialOwnerRefs())

	// --- PCSG scale-in/out (single PCS replica) ---

	Logger.Info("8. Scale PCSG sga from 2 to 1")
	if err := tc.ScalePCSG("rs-test-0-sga", 1); err != nil {
		t.Fatalf("Failed to scale PCSG: %v", err)
	}
	verifyRCState(t, tc, rcLabelSelector, 10, pcsgScaleInRCNames())
	verifyPodState(t, tc, podSelector, 2, pcsgScaleInPodRefs())
	Logger.Info("   Verified 10 RCs and 2 pods after PCSG scale-in")

	Logger.Info("9. Scale PCSG sga from 1 to 3")
	if err := tc.ScalePCSG("rs-test-0-sga", 3); err != nil {
		t.Fatalf("Failed to scale PCSG: %v", err)
	}
	verifyRCState(t, tc, rcLabelSelector, 14, pcsgScaleOutRCNames())
	verifyPodState(t, tc, podSelector, 4, pcsgScaleOutPodRefs())
	Logger.Info("   Verified 14 RCs and 4 pods after PCSG scale-out")

	// --- PCLQ scale-out/in (single PCS replica) ---

	Logger.Info("10. Scale standalone PCLQ worker-a from 1 to 2")
	if err := scalePodClique(tc, "rs-test-0-worker-a", 2); err != nil {
		t.Fatalf("Failed to scale PCLQ: %v", err)
	}
	verifyRCState(t, tc, rcLabelSelector, 15, pclqScaleOutRCNames())
	pods, err := pods.NewPodManager(tc.Client, Logger).WaitForCount(ctx, rsNamespace, podSelector, 5, tc.Timeout, tc.Interval)
	if err != nil {
		t.Fatalf("Expected 5 pods after PCLQ scale-out but timed out: %v", err)
	}
	verifyMultiPodPerReplicaRefs(t, pods.Items, "rs-test-0-worker-a",
		[]string{
			"rs-test-all-int-tpl5",
			"rs-test-0-ext-tpl",
			"rs-test-0-int-tpl",
			"rs-test-0-worker-a-all-ext-ns-tpl",
		},
		[]string{
			"rs-test-0-worker-a-0-int-tpl4",
			"rs-test-0-worker-a-1-int-tpl4",
		},
	)
	Logger.Info("   Verified 15 RCs and 5 pods (per-pod refs) after PCLQ scale-out")

	Logger.Info("11. Scale standalone PCLQ worker-a from 2 to 1")
	if err := scalePodClique(tc, "rs-test-0-worker-a", 1); err != nil {
		t.Fatalf("Failed to scale PCLQ: %v", err)
	}
	verifyRCState(t, tc, rcLabelSelector, 14, pcsgScaleOutRCNames())
	verifyPodState(t, tc, podSelector, 4, pcsgScaleOutPodRefs())
	Logger.Info("   Verified 14 RCs and 4 pods after PCLQ scale-in")

	// --- PCS scale-out/in ---
	// Rep 0 retains sga=3 from step 9. Rep 1 is created from template (sga=2).

	Logger.Info("12. Scale PCS from 1 to 2")
	if err := tc.ScalePCS(rsWorkloadName, 2); err != nil {
		t.Fatalf("Failed to scale PCS to 2: %v", err)
	}
	verifyRCState(t, tc, rcLabelSelector, 24, pcsScaleOutRCNames())
	verifyPodState(t, tc, podSelector, 7, pcsScaleOutPodRefs())
	Logger.Info("   Verified 24 RCs and 7 pods after PCS scale-out")

	Logger.Info("13. Re-verify ownerReferences after PCS scale-out (rep-1 code path)")
	verifyOwnerReferences(t, tc, rcLabelSelector, pcsScaleOutOwnerRefs())

	Logger.Info("14. Scale PCS from 2 to 1")
	if err := tc.ScalePCS(rsWorkloadName, 1); err != nil {
		t.Fatalf("Failed to scale PCS to 1: %v", err)
	}
	verifyRCState(t, tc, rcLabelSelector, 14, pcsgScaleOutRCNames())
	verifyPodState(t, tc, podSelector, 4, pcsgScaleOutPodRefs())
	Logger.Info("   Verified 14 RCs and 4 pods after PCS scale-in")

	// --- Immutability rejection ---

	Logger.Info("15. Verify webhook rejects immutable resourceSharing change")
	verifyImmutabilityRejection(t, tc)

	Logger.Info("16. Delete PCS and verify all ResourceClaims are garbage-collected")
	wm := workload.NewWorkloadManager(tc.Client, Logger)
	if err := wm.DeletePCSAndWait(ctx, tc.Namespace, rsWorkloadName, tc.Timeout, tc.Interval); err != nil {
		t.Fatalf("Failed to delete PCS: %v", err)
	}
	if err := k8sutils.WaitForResourceClaimCount(ctx, crClient, tc.Namespace, rcLabelSelector, 0, tc.Timeout, tc.Interval); err != nil {
		t.Fatalf("Expected 0 ResourceClaims after PCS deletion but timed out: %v", err)
	}
	Logger.Info("   Verified all ResourceClaims garbage-collected after PCS deletion")

	Logger.Info("Hierarchical resource sharing e2e test completed successfully!")
}

// scalePodClique scales a PodClique using the resource manager.
func scalePodClique(tc *testctx.TestContext, name string, replicas int) error {
	rm := resources.NewResourceManager(tc.Client, Logger)
	return rm.ScaleCRD(tc.Ctx, gvk.PodClique, tc.Namespace, name, replicas)
}

// verifyRCState waits for the expected RC count and verifies exact RC names.
func verifyRCState(t *testing.T, tc *testctx.TestContext, labelSelector string, expectedCount int, expectedNames []string) {
	t.Helper()
	err := k8sutils.WaitForResourceClaimCount(tc.Ctx, tc.Client, tc.Namespace, labelSelector, expectedCount, tc.Timeout, tc.Interval)
	if err != nil {
		t.Fatalf("Expected %d ResourceClaims but timed out: %v", expectedCount, err)
	}

	rcList, err := k8sutils.ListResourceClaims(tc.Ctx, tc.Client, tc.Namespace, labelSelector)
	if err != nil {
		t.Fatalf("Failed to list ResourceClaims: %v", err)
	}

	actualNames := k8sutils.ResourceClaimNames(rcList)
	sort.Strings(actualNames)
	sortedExpected := make([]string, len(expectedNames))
	copy(sortedExpected, expectedNames)
	sort.Strings(sortedExpected)
	if !slices.Equal(actualNames, sortedExpected) {
		t.Fatalf("RC name mismatch\nexpected (%d): %v\nactual   (%d): %v", len(sortedExpected), sortedExpected, len(actualNames), actualNames)
	}
}

// verifyPodState waits for the expected pod count and verifies RC references in pod specs.
func verifyPodState(t *testing.T, tc *testctx.TestContext, podSelector string, expectedCount int, expectedRefs map[string][]string) {
	t.Helper()
	pods, err := pods.NewPodManager(tc.Client, Logger).WaitForCount(tc.Ctx, tc.Namespace, podSelector, expectedCount, tc.Timeout, tc.Interval)
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
		pclqName := pod.Labels[apicommon.LabelPodClique]
		if pclqName == "" {
			t.Errorf("Pod %s missing %s label", pod.Name, apicommon.LabelPodClique)
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

// --- Cross-namespace RCT setup ---

func createCrossNamespaceRCT(t *testing.T, tc *testctx.TestContext) {
	t.Helper()
	crClient := tc.Client

	ns := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "rs-shared"}}
	if err := crClient.Create(tc.Ctx, ns); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create namespace rs-shared: %v", err)
	}

	rct := &resourcev1.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ext-ns-tpl",
			Namespace: "rs-shared",
		},
		Spec: resourcev1.ResourceClaimTemplateSpec{
			Spec: resourcev1.ResourceClaimSpec{
				Devices: resourcev1.DeviceClaim{
					Requests: []resourcev1.DeviceRequest{
						{
							Name: "ext-ns-dev",
							Exactly: &resourcev1.ExactDeviceRequest{
								DeviceClassName: "ext-ns-class",
							},
						},
					},
				},
			},
		},
	}
	if err := crClient.Create(tc.Ctx, rct); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create ResourceClaimTemplate rs-shared/ext-ns-tpl: %v", err)
	}
}

// --- Owner reference verification ---

type expectedOwner struct {
	Kind string
	Name string
}

func initialOwnerRefs() map[string]expectedOwner {
	return map[string]expectedOwner{
		// PCS-level RCs → owned by PodCliqueSet
		"rs-test-all-int-tpl5": {Kind: "PodCliqueSet", Name: "rs-test"},
		"rs-test-0-ext-tpl":    {Kind: "PodCliqueSet", Name: "rs-test"},
		"rs-test-0-int-tpl":    {Kind: "PodCliqueSet", Name: "rs-test"},
		"rs-test-all-int-tpl7": {Kind: "PodCliqueSet", Name: "rs-test"},
		// PCLQ worker-a RCs → owned by PodClique
		"rs-test-0-worker-a-0-int-tpl4":     {Kind: "PodClique", Name: "rs-test-0-worker-a"},
		"rs-test-0-worker-a-all-ext-ns-tpl": {Kind: "PodClique", Name: "rs-test-0-worker-a"},
		// PCSG sga RCs → owned by PodCliqueScalingGroup
		"rs-test-0-sga-all-int-tpl2": {Kind: "PodCliqueScalingGroup", Name: "rs-test-0-sga"},
		"rs-test-0-sga-0-int-tpl3":   {Kind: "PodCliqueScalingGroup", Name: "rs-test-0-sga"},
		"rs-test-0-sga-1-int-tpl3":   {Kind: "PodCliqueScalingGroup", Name: "rs-test-0-sga"},
		"rs-test-0-sga-all-int-tpl8": {Kind: "PodCliqueScalingGroup", Name: "rs-test-0-sga"},
		// PCLQ worker-b in sga RCs → owned by PodClique
		"rs-test-0-sga-0-worker-b-all-int-tpl6": {Kind: "PodClique", Name: "rs-test-0-sga-0-worker-b"},
		"rs-test-0-sga-1-worker-b-all-int-tpl6": {Kind: "PodClique", Name: "rs-test-0-sga-1-worker-b"},
	}
}

// pcsScaleOutOwnerRefs: all 24 RCs after PCS scale-out (rep 0 sga=3, rep 1 sga=2).
func pcsScaleOutOwnerRefs() map[string]expectedOwner {
	refs := map[string]expectedOwner{
		// PCS-level RCs → owned by PodCliqueSet
		"rs-test-all-int-tpl5": {Kind: "PodCliqueSet", Name: "rs-test"},
		"rs-test-0-ext-tpl":    {Kind: "PodCliqueSet", Name: "rs-test"},
		"rs-test-0-int-tpl":    {Kind: "PodCliqueSet", Name: "rs-test"},
		"rs-test-all-int-tpl7": {Kind: "PodCliqueSet", Name: "rs-test"},
		"rs-test-1-ext-tpl":    {Kind: "PodCliqueSet", Name: "rs-test"},
		"rs-test-1-int-tpl":    {Kind: "PodCliqueSet", Name: "rs-test"},
		// Rep 0: standalone PCLQ worker-a
		"rs-test-0-worker-a-0-int-tpl4":     {Kind: "PodClique", Name: "rs-test-0-worker-a"},
		"rs-test-0-worker-a-all-ext-ns-tpl": {Kind: "PodClique", Name: "rs-test-0-worker-a"},
		// Rep 0: PCSG sga (3 replicas after scale-out in step 9)
		"rs-test-0-sga-all-int-tpl2": {Kind: "PodCliqueScalingGroup", Name: "rs-test-0-sga"},
		"rs-test-0-sga-0-int-tpl3":   {Kind: "PodCliqueScalingGroup", Name: "rs-test-0-sga"},
		"rs-test-0-sga-1-int-tpl3":   {Kind: "PodCliqueScalingGroup", Name: "rs-test-0-sga"},
		"rs-test-0-sga-2-int-tpl3":   {Kind: "PodCliqueScalingGroup", Name: "rs-test-0-sga"},
		"rs-test-0-sga-all-int-tpl8": {Kind: "PodCliqueScalingGroup", Name: "rs-test-0-sga"},
		// Rep 0: PCLQ worker-b in sga (3 replicas)
		"rs-test-0-sga-0-worker-b-all-int-tpl6": {Kind: "PodClique", Name: "rs-test-0-sga-0-worker-b"},
		"rs-test-0-sga-1-worker-b-all-int-tpl6": {Kind: "PodClique", Name: "rs-test-0-sga-1-worker-b"},
		"rs-test-0-sga-2-worker-b-all-int-tpl6": {Kind: "PodClique", Name: "rs-test-0-sga-2-worker-b"},
		// Rep 1: standalone PCLQ worker-a
		"rs-test-1-worker-a-0-int-tpl4":     {Kind: "PodClique", Name: "rs-test-1-worker-a"},
		"rs-test-1-worker-a-all-ext-ns-tpl": {Kind: "PodClique", Name: "rs-test-1-worker-a"},
		// Rep 1: PCSG sga (2 replicas from template)
		"rs-test-1-sga-all-int-tpl2": {Kind: "PodCliqueScalingGroup", Name: "rs-test-1-sga"},
		"rs-test-1-sga-0-int-tpl3":   {Kind: "PodCliqueScalingGroup", Name: "rs-test-1-sga"},
		"rs-test-1-sga-1-int-tpl3":   {Kind: "PodCliqueScalingGroup", Name: "rs-test-1-sga"},
		"rs-test-1-sga-all-int-tpl8": {Kind: "PodCliqueScalingGroup", Name: "rs-test-1-sga"},
		// Rep 1: PCLQ worker-b in sga (2 replicas)
		"rs-test-1-sga-0-worker-b-all-int-tpl6": {Kind: "PodClique", Name: "rs-test-1-sga-0-worker-b"},
		"rs-test-1-sga-1-worker-b-all-int-tpl6": {Kind: "PodClique", Name: "rs-test-1-sga-1-worker-b"},
	}
	return refs
}

func verifyOwnerReferences(t *testing.T, tc *testctx.TestContext, labelSelector string, expected map[string]expectedOwner) {
	t.Helper()
	rcList, err := k8sutils.ListResourceClaims(tc.Ctx, tc.Client, tc.Namespace, labelSelector)
	if err != nil {
		t.Fatalf("Failed to list ResourceClaims for ownerRef check: %v", err)
	}
	for _, rc := range rcList.Items {
		exp, ok := expected[rc.Name]
		if !ok {
			continue
		}
		owners := rc.OwnerReferences
		if len(owners) == 0 {
			t.Errorf("RC %s has no ownerReferences", rc.Name)
			continue
		}
		found := false
		for _, ref := range owners {
			if ref.Kind == exp.Kind && ref.Name == exp.Name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("RC %s: expected owner %s/%s, got %v", rc.Name, exp.Kind, exp.Name, owners)
		}
	}
}

// --- Multi-pod PerReplica ref verification ---

func verifyMultiPodPerReplicaRefs(t *testing.T, allPods []v1.Pod, pclqLabel string, commonRefs, uniqueRefs []string) {
	t.Helper()
	var matchedPods []v1.Pod
	for _, pod := range allPods {
		if pod.Labels[apicommon.LabelPodClique] == pclqLabel {
			matchedPods = append(matchedPods, pod)
		}
	}
	if len(matchedPods) != len(uniqueRefs) {
		t.Fatalf("Expected %d pods for PCLQ %s, got %d", len(uniqueRefs), pclqLabel, len(matchedPods))
	}

	seenUnique := make(map[string]bool)
	for _, pod := range matchedPods {
		podClaimNames := extractPodResourceClaimNames(pod.Spec)
		expectedTotal := len(commonRefs) + 1
		if len(podClaimNames) != expectedTotal {
			t.Errorf("Pod %s (pclq=%s) expected %d refs, got %d: %v", pod.Name, pclqLabel, expectedTotal, len(podClaimNames), podClaimNames)
			continue
		}
		for _, common := range commonRefs {
			if !slices.Contains(podClaimNames, common) {
				t.Errorf("Pod %s missing common ref %s", pod.Name, common)
			}
		}
		for _, name := range podClaimNames {
			if slices.Contains(uniqueRefs, name) {
				seenUnique[name] = true
			}
		}
	}
	for _, u := range uniqueRefs {
		if !seenUnique[u] {
			t.Errorf("PerReplica ref %s not found on any pod of PCLQ %s", u, pclqLabel)
		}
	}
}

// --- Immutability rejection ---

func verifyImmutabilityRejection(t *testing.T, tc *testctx.TestContext) {
	t.Helper()
	var pcs grovecorev1alpha1.PodCliqueSet
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: rsWorkloadName}, &pcs); err != nil {
		t.Fatalf("Failed to get PCS for immutability test: %v", err)
	}

	if len(pcs.Spec.Template.ResourceSharing) == 0 {
		t.Fatal("PCS has no resourceSharing entries to test immutability")
	}
	if pcs.Spec.Template.ResourceSharing[0].Scope == "PerReplica" {
		pcs.Spec.Template.ResourceSharing[0].Scope = "AllReplicas"
	} else {
		pcs.Spec.Template.ResourceSharing[0].Scope = "PerReplica"
	}

	err := tc.Client.Update(tc.Ctx, &pcs)
	if err == nil {
		t.Fatal("Expected webhook rejection for immutable resourceSharing change, but update succeeded")
	}
	if !strings.Contains(err.Error(), "field is immutable") {
		t.Fatalf("Expected 'field is immutable' error, got: %v", err)
	}
}

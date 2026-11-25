//go:build e2e

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

package update

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/grove/workload"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	updateutils "github.com/ai-dynamo/grove/operator/e2e/tests/update/utils"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Test_OD1_NoAutomaticDeletionOnSpecChange tests that OnDelete strategy does not automatically delete pods when spec changes.
// Scenario OD-1 (from proposal Testcase 1):
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload with OnDelete strategy, and verify 10 newly created pods
// 3. Change the specification of pc-a
// 4. Verify that NO pods are automatically deleted
// 5. Verify that the update is marked complete (UpdateEndedAt is set)
func Test_OD1_NoAutomaticDeletionOnSpecChange(t *testing.T) {
	tests.Logger.Info("1. Initialize a 10-node Grove cluster")
	tests.Logger.Info("2. Deploy workload with OnDelete strategy, and verify 10 newly created pods")
	tc, cleanup, tracker := setupTest(t, testConfig{
		workloadName: "workload-ondelete",
		workloadYAML: "../../yaml/workload-ondelete.yaml",
		workerNodes:  10,
		expectedPods: 10,
	})
	defer cleanup()

	existingPodNames, err := captureExistingPodNames(tc)
	if err != nil {
		t.Fatalf("Failed to capture existing Pods: %v", err)
	}
	// Capture the PodGangMap entries and generation hash before the update.
	oldHash := updateutils.PCSGenerationHash(t, tc)
	entriesBefore := updateutils.PodGangMapEntries(t, tc, 0)

	tests.Logger.Info("3. Change the specification of pc-a")
	if err := updateutils.TriggerPodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	tests.Logger.Info("4. Verify that NO pods are automatically deleted")
	tests.Logger.Info("5. Verify that the update is marked complete (UpdateEndedAt is set)")
	verifyNoAutomaticDeletionAfterUpdate(tc, tracker, existingPodNames, 10, true)

	tests.Logger.Info("6. Verify the PodGangMap entries advanced to the new generation hash, otherwise unchanged")
	newHash := updateutils.PCSGenerationHash(t, tc)
	if newHash == oldHash {
		t.Fatalf("PodCliqueSet generation hash did not change after the update: %s", newHash)
	}
	updateutils.AssertEntriesUnchangedExceptHashForNonCoherentUpdate(t, entriesBefore, updateutils.PodGangMapEntries(t, tc, 0), newHash)

	tests.Logger.Info("OnDelete Update - No Automatic Deletion test (OD-1) completed successfully!")
}

// Test_OD2_ManualDeletionCreatesUpdatedPod tests that manually deleted pods are recreated with the new spec.
// Scenario OD-2 (from proposal Testcase 1):
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload with OnDelete strategy, and verify 10 newly created pods
// 3. Change the specification of pc-a
// 4. Delete a pod manually and verify the replacement uses the new template
// 5. Verify status fields accurately reflect the update state
func Test_OD2_ManualDeletionCreatesUpdatedPod(t *testing.T) {
	tests.Logger.Info("1. Initialize a 10-node Grove cluster")
	tests.Logger.Info("2. Deploy workload with OnDelete strategy, and verify 10 newly created pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload-ondelete",
		workloadYAML: "../../yaml/workload-ondelete.yaml",
		workerNodes:  10,
		expectedPods: 10,
	})
	defer cleanup()

	existingPodNames, err := captureExistingPodNames(tc)
	if err != nil {
		t.Fatalf("Failed to capture existing Pods: %v", err)
	}

	podToDelete, err := firstPodForClique(tc, "pc-a")
	if err != nil {
		t.Fatalf("Failed to get pod for pc-a: %v", err)
	}
	tests.Logger.Debugf("Will delete pod %s after spec change", podToDelete)

	tests.Logger.Info("3. Change the specification of pc-a")
	if err = updateutils.TriggerPodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	// Wait for update to be marked complete
	if err = waitForOnDeleteUpdateCompleteWithTimeout(tc, 1*time.Minute); err != nil {
		t.Fatalf("Failed to verify OnDelete update completion: %v", err)
	}

	tests.Logger.Info("4. Delete a pod manually and verify the replacement uses the new template")
	// Delete the pod
	if err = updateutils.DeletePodAndWaitForTermination(tc, podToDelete); err != nil {
		t.Fatalf("Failed to delete pod and wait for replacement: %v", err)
	}

	if err = tc.WaitForRunningPods(10); err != nil {
		t.Fatalf("Failed to wait for pod with new specification to be created")
	}

	newPodName, err := findFirstNewPodName(tc, existingPodNames)
	if err != nil {
		t.Fatalf("Failed to find replacement pod: %v", err)
	}

	tests.Logger.Debugf("New replacement pod: %s", newPodName)

	// Verify the new pod has the updated spec
	if err = updateutils.VerifyPodHasUpdatedSpec(tc, newPodName); err != nil {
		t.Fatalf("Replacement pod does not have updated spec: %v", err)
	}

	tests.Logger.Info("5. Verify status fields accurately reflect the update state")
	// Get PCLQ and verify UpdatedReplicas has increased
	pclqName := fmt.Sprintf("%s-%d-%s", tc.Workload.Name, 0, "pc-a")
	var pclq grovev1beta1.PodClique
	if err = tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: pclqName}, &pclq); err != nil {
		t.Fatalf("Failed to get PodClique: %v", err)
	}

	if pclq.Status.UpdatedReplicas != int32(1) {
		t.Fatalf("Updated replicas has not increased by 1")
	}

	tests.Logger.Debugf("PCLQ Status - UpdatedReplicas: %d, Replicas: %d", pclq.Status.UpdatedReplicas, pclq.Status.Replicas)

	tests.Logger.Info("OnDelete Update - Manual Deletion Creates Updated Pod test (OD-2) completed successfully!")
}

// Test_OD3_ScaleInPrefersOutdatedPods tests that during scale-in, pods with outdated templates are deleted first.
// Scenario OD-3 (from proposal Testcase 1):
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload with OnDelete strategy, and verify 10 newly created pods (pc-a has 2 replicas)
// 3. Change the specification of pc-a (triggering OnDelete update)
// 4. Manually delete ONE pc-a pod to update it (1 updated, 1 outdated)
// 5. Scale in pc-a from 2 to 1 replica
// 6. Verify pods with outdated templates are deleted first (not the updated one)
func Test_OD3_ScaleInPrefersOutdatedPods(t *testing.T) {
	tests.Logger.Info("1. Initialize a 10-node Grove cluster")
	tests.Logger.Info("2. Deploy workload with OnDelete strategy, and verify 10 newly created pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload-ondelete",
		workloadYAML: "../../yaml/workload-ondelete.yaml",
		workerNodes:  10,
		expectedPods: 10,
	})
	defer cleanup()

	// Get the current pods for pc-a BEFORE the update
	oldPcaPods, err := podsForClique(tc, "pc-a")
	if err != nil {
		t.Fatalf("Failed to get pods for pc-a: %v", err)
	}
	tests.Logger.Debugf("pc-a pods before update: %v", oldPcaPods)

	if len(oldPcaPods) != 2 {
		t.Fatalf("Expected 2 pc-a pods, got %d", len(oldPcaPods))
	}

	tests.Logger.Info("3. Change the specification of pc-a (triggering OnDelete update)")
	if err = updateutils.TriggerPodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	// Wait for update to be marked complete
	if err = waitForOnDeleteUpdateCompleteWithTimeout(tc, 1*time.Minute); err != nil {
		t.Fatalf("Failed to verify OnDelete update completion: %v", err)
	}

	tests.Logger.Info("4. Manually delete ONE pc-a pod to update it")
	// Delete the first pod from pc-a to create one with updated spec
	podToUpdate := oldPcaPods[0]
	if err = updateutils.DeletePodAndWaitForTermination(tc, podToUpdate); err != nil {
		t.Fatalf("Failed to delete pod and wait for replacement: %v", err)
	}

	// wait for the new pc-a pod to be created
	if err = tc.WaitForReadyPods(10); err != nil {
		t.Fatalf("Failed to wait for pod with new specification to be created")
	}

	// Get the new pc-a pods
	updatedPcaPods, err := podsForClique(tc, "pc-a")
	if err != nil {
		t.Fatalf("Failed to get pods for pc-a after update: %v", err)
	}

	// Find the updated pod (the one not in oldPcaPods)
	oldPodSet := make(map[string]bool)
	for _, p := range oldPcaPods {
		oldPodSet[p] = true
	}

	var updatedPod string
	for _, p := range updatedPcaPods {
		if !oldPodSet[p] {
			updatedPod = p
			break
		}
	}

	if updatedPod == "" {
		t.Fatalf("Could not identify the updated pod")
	}

	tests.Logger.Debugf("Updated pod: %s, remaining outdated pod: %s", updatedPod, oldPcaPods[1])

	tests.Logger.Info("5. Scale in pc-a from 2 to 1 replica")
	if err = updateutils.ScalePodCliqueInPCS(tc, "pc-a", 1); err != nil {
		t.Fatalf("Failed to scale in PodClique pc-a: %v", err)
	}

	// Wait for scale-in to complete (9 total Pods: 1 pc-a + 8 others)
	if err = tc.WaitForPods(9); err != nil {
		t.Fatalf("Failed to wait for pods after pc-a scale-in: %v", err)
	}

	tests.Logger.Info("6. Verify pods with outdated templates are deleted first (not the updated one)")
	// Get the remaining pc-a pods
	remainingPcaPods, err := podsForClique(tc, "pc-a")
	if err != nil {
		t.Fatalf("Failed to get remaining pods for pc-a: %v", err)
	}

	if len(remainingPcaPods) != 1 {
		t.Fatalf("Expected 1 pc-a pod after scale-in, got %d", len(remainingPcaPods))
	}

	// Verify the updated pod is still present
	foundUpdatedPod := slices.Contains(remainingPcaPods, updatedPod)

	if !foundUpdatedPod {
		t.Fatalf("The updated pod %s was deleted during scale-in instead of the outdated pod", updatedPod)
	}

	tests.Logger.Info("OnDelete Update - Scale-In Prefers Outdated Pods test (OD-3) completed successfully!")
}

// Test_OD4_PCSGNoAutomaticDeletionOnSpecChange tests OnDelete strategy with PodCliqueScalingGroups.
// Scenario OD-4 (from proposal Testcase 3):
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload with OnDelete strategy, and verify 10 newly created pods
// 3. Change the specification of pc-b (part of PCSG sg-x)
// 4. Verify that NO pods/replicas are automatically deleted
// 5. Verify that the update is marked complete
func Test_OD4_PCSGNoAutomaticDeletionOnSpecChange(t *testing.T) {
	tests.Logger.Info("1. Initialize a 10-node Grove cluster")
	tests.Logger.Info("2. Deploy workload with OnDelete strategy, and verify 10 newly created pods")
	tc, cleanup, tracker := setupTest(t, testConfig{
		workloadName: "workload-ondelete",
		workloadYAML: "../../yaml/workload-ondelete.yaml",
		workerNodes:  10,
		expectedPods: 10,
	})
	defer cleanup()

	existingPodNames, err := captureExistingPodNames(tc)
	if err != nil {
		t.Fatalf("Failed to capture existing Pods: %v", err)
	}

	tests.Logger.Info("3. Change the specification of pc-b (part of PCSG sg-x)")
	if err = updateutils.TriggerPodCliqueUpdate(tc, "pc-b"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	tests.Logger.Info("4. Verify that NO pods/replicas are automatically deleted")
	tests.Logger.Info("5. Verify that the update is marked complete")
	verifyNoAutomaticDeletionAfterUpdate(tc, tracker, existingPodNames, 10, true)

	tests.Logger.Info("OnDelete Update - PCSG No Automatic Deletion test (OD-4) completed successfully!")
}

// Test_OD5_PCSGManualDeletionCreatesUpdatedReplica tests manual deletion in PCSG with OnDelete strategy.
// Scenario OD-5 (from proposal Testcase 3):
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload with OnDelete strategy, and verify 10 newly created pods
// 3. Change the specification of pc-c (part of PCSG sg-x)
// 4. Delete a pc-c pod manually and verify the replacement uses the new template
func Test_OD5_PCSGManualDeletionCreatesUpdatedReplica(t *testing.T) {
	tests.Logger.Info("1. Initialize a 10-node Grove cluster")
	tests.Logger.Info("2. Deploy workload with OnDelete strategy, and verify 10 newly created pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload-ondelete",
		workloadYAML: "../../yaml/workload-ondelete.yaml",
		workerNodes:  10,
		expectedPods: 10,
	})
	defer cleanup()

	existingPodNames, err := captureExistingPodNames(tc)
	if err != nil {
		t.Fatalf("Failed to capture existing Pods: %v", err)
	}

	podToDelete, err := firstPodForClique(tc, "pc-c")
	if err != nil {
		t.Fatalf("Failed to get pod for pc-c: %v", err)
	}
	tests.Logger.Debugf("Will delete pod %s after spec change", podToDelete)

	tests.Logger.Info("3. Change the specification of pc-c (part of PCSG sg-x)")
	if err = updateutils.TriggerPodCliqueUpdate(tc, "pc-c"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	// Wait for update to be marked complete
	if err = waitForOnDeleteUpdateCompleteWithTimeout(tc, 1*time.Minute); err != nil {
		t.Fatalf("Failed to verify OnDelete update completion: %v", err)
	}

	tests.Logger.Info("4. Delete a pc-c pod manually and verify the replacement uses the new template")
	// Delete the pod
	if err = updateutils.DeletePodAndWaitForTermination(tc, podToDelete); err != nil {
		t.Fatalf("Failed to delete pod and wait for replacement: %v", err)
	}

	if err = tc.WaitForRunningPods(10); err != nil {
		t.Fatalf("Failed to wait for pod with new specification to be created")
	}

	newPodName, err := findFirstNewPodName(tc, existingPodNames)
	if err != nil {
		t.Fatalf("Failed to find replacement pod: %v", err)
	}

	tests.Logger.Debugf("New replacement pod: %s", newPodName)

	// Verify the new pod has the updated spec
	if err = updateutils.VerifyPodHasUpdatedSpec(tc, newPodName); err != nil {
		t.Fatalf("Replacement pod does not have updated spec: %v", err)
	}

	tests.Logger.Info("OnDelete Update - PCSG Manual Deletion Creates Updated Replica test (OD-5) completed successfully!")
}

// Test_OD6_MixedPCLQsAndPCSG tests OnDelete strategy with both standalone PodCliques and PodCliqueScalingGroups.
// Scenario OD-6 (from proposal Testcase 4):
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload with OnDelete strategy, and verify 10 newly created pods
// 3. Change the specification of ALL PodCliques (pc-a, pc-b, pc-c)
// 4. Verify that NO pods are automatically deleted
// 5. Manually delete one pod from each type (standalone and PCSG)
// 6. Verify replacements use the new template
func Test_OD6_MixedPCLQsAndPCSG(t *testing.T) {
	tests.Logger.Info("1. Initialize a 10-node Grove cluster")
	tests.Logger.Info("2. Deploy workload with OnDelete strategy, and verify 10 newly created pods")
	tc, cleanup, tracker := setupTest(t, testConfig{
		workloadName: "workload-ondelete",
		workloadYAML: "../../yaml/workload-ondelete.yaml",
		workerNodes:  10,
		expectedPods: 10,
	})
	defer cleanup()

	existingPodNames, err := captureExistingPodNames(tc)
	if err != nil {
		t.Fatalf("Failed to capture existing Pods: %v", err)
	}

	tests.Logger.Info("3. Change the specification of ALL PodCliques (pc-a, pc-b, pc-c)")
	for _, cliqueName := range []string{"pc-a", "pc-b", "pc-c"} {
		if err = updateutils.TriggerPodCliqueUpdate(tc, cliqueName); err != nil {
			t.Fatalf("Failed to update PodClique %s spec: %v", cliqueName, err)
		}
	}

	tests.Logger.Info("4. Verify that NO pods are automatically deleted")
	verifyNoAutomaticDeletionAfterUpdate(tc, tracker, existingPodNames, 10, true)

	tests.Logger.Info("5. Manually delete one pod from each type (standalone and PCSG)")
	standalonePodToDelete, err := firstPodForClique(tc, "pc-a")
	if err != nil {
		t.Fatalf("Failed to get pod for pc-a: %v", err)
	}

	pcsgPodToDelete, err := firstPodForClique(tc, "pc-c")
	if err != nil {
		t.Fatalf("Failed to get pod for pc-c: %v", err)
	}

	// Delete standalone pod
	tests.Logger.Debugf("Deleting standalone pod: %s", standalonePodToDelete)
	if err = updateutils.DeletePodAndWaitForTermination(tc, standalonePodToDelete); err != nil {
		t.Fatalf("Failed to delete standalone pod and wait for replacement: %v", err)
	}

	if err = tc.WaitForRunningPods(10); err != nil {
		t.Fatalf("Failed to wait for pod with new specification to be created")
	}

	// Delete PCSG pod
	tests.Logger.Debugf("Deleting PCSG pod: %s", pcsgPodToDelete)
	if err = updateutils.DeletePodAndWaitForTermination(tc, pcsgPodToDelete); err != nil {
		t.Fatalf("Failed to delete PCSG pod and wait for replacement: %v", err)
	}

	if err = tc.WaitForRunningPods(10); err != nil {
		t.Fatalf("Failed to wait for pod with new specification to be created")
	}

	tests.Logger.Info("6. Verify replacements use the new template")
	// Get final pod list
	finalPods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list pods after deletions: %v", err)
	}

	// Find new pods and verify they have updated spec
	newPodsFound := 0
	for _, pod := range finalPods.Items {
		if !existingPodNames[pod.Name] {
			newPodsFound++
			if err := updateutils.VerifyPodHasUpdatedSpec(tc, pod.Name); err != nil {
				t.Fatalf("New pod %s does not have updated spec: %v", pod.Name, err)
			}
		}
	}

	if newPodsFound < 2 {
		t.Fatalf("Expected at least 2 new pods to be created, found %d", newPodsFound)
	}

	tests.Logger.Info("OnDelete Update - Mixed PodCliques and PCSG test (OD-6) completed successfully!")
}

// Test_OD7_MultipleReplicasPCS tests OnDelete strategy with multiple PCS replicas.
// Scenario OD-7 (from proposal Testcase 4):
// 1. Initialize a 20-node Grove cluster
// 2. Deploy workload with OnDelete strategy and 2 PCS replicas
// 3. Change the specification of pc-a
// 4. Verify that NO pods are automatically deleted
// 5. Verify uniform strategy application across both replicas
func Test_OD7_MultipleReplicasPCS(t *testing.T) {
	tests.Logger.Info("1. Initialize a 20-node Grove cluster")
	tests.Logger.Info("2. Deploy workload with OnDelete strategy and 2 PCS replicas, verify 20 pods")
	tc, cleanup, tracker := setupTest(t, testConfig{
		workloadName:       "workload-ondelete",
		workloadYAML:       "../../yaml/workload-ondelete.yaml",
		workerNodes:        20,
		expectedPods:       10,
		initialPCSReplicas: 2,
		postScalePods:      20,
	})
	defer cleanup()

	existingPodNames, err := captureExistingPodNames(tc)
	if err != nil {
		t.Fatalf("Failed to capture existing Pods: %v", err)
	}
	// Capture each replica's PodGangMap entries and the generation hash before the update.
	oldHash := updateutils.PCSGenerationHash(t, tc)
	entriesBefore := map[int][]grovev1beta1.PodGangEntry{}
	for _, pcsReplicaIndex := range []int{0, 1} {
		entriesBefore[pcsReplicaIndex] = updateutils.PodGangMapEntries(t, tc, pcsReplicaIndex)
	}

	tests.Logger.Info("3. Change the specification of pc-a")
	if err = updateutils.TriggerPodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	tests.Logger.Info("4. Verify uniform strategy application across both replicas")
	verifyNoAutomaticDeletionAfterUpdate(tc, tracker, existingPodNames, 20, false)

	tests.Logger.Info("5. Verify every replica's PodGangMap entries advanced to the new generation hash, otherwise unchanged")
	newHash := updateutils.PCSGenerationHash(t, tc)
	if newHash == oldHash {
		t.Fatalf("PodCliqueSet generation hash did not change after the update: %s", newHash)
	}
	for _, pcsReplicaIndex := range []int{0, 1} {
		updateutils.AssertEntriesUnchangedExceptHashForNonCoherentUpdate(t, entriesBefore[pcsReplicaIndex], updateutils.PodGangMapEntries(t, tc, pcsReplicaIndex), newHash)
	}

	tests.Logger.Info("OnDelete Update - Multiple PCS Replicas test (OD-7) completed successfully!")
}

// Test_OD8_NodeFailureRecoveryWorkflow simulates the node failure recovery workflow from the GREP-291 proposal.
// Scenario OD-8 (from proposal Testcase 2 / Example Usage):
// 1. Initialize an 11-node Grove cluster (10 pods + 1 spare node)
// 2. Deploy workload with OnDelete strategy, and verify 10 newly created pods
// 3. Identify a node running a pc-a pod (simulating a "failed node")
// 4. Update pc-a's nodeAffinity to exclude the failed node's hostname (NotIn matchExpression)
// 5. Verify NO pods are automatically deleted (OnDelete behavior)
// 6. Manually delete the pod on the "failed node"
// 7. Verify the replacement pod has the updated nodeAffinity and is NOT on the failed node
// 8. Verify pods on healthy nodes were never disrupted
func Test_OD8_NodeFailureRecoveryWorkflow(t *testing.T) {
	tests.Logger.Info("1. Initialize an 11-node Grove cluster (10 pods + 1 spare node)")
	tests.Logger.Info("2. Deploy workload with OnDelete strategy, and verify 10 newly created pods")
	tc, cleanup, tracker := setupTest(t, testConfig{
		workloadName: "workload-ondelete",
		workloadYAML: "../../yaml/workload-ondelete.yaml",
		workerNodes:  11,
		expectedPods: 10,
	})
	defer cleanup()

	existingPodNames, err := captureExistingPodNames(tc)
	if err != nil {
		t.Fatalf("Failed to capture existing Pods: %v", err)
	}

	tests.Logger.Info("3. Identify a node running a pc-a pod (simulating a 'failed node')")
	podToEvict, err := firstPodForClique(tc, "pc-a")
	if err != nil {
		t.Fatalf("Failed to get pod for pc-a: %v", err)
	}

	failedNodeName, err := nodeForPod(tc, podToEvict)
	if err != nil {
		t.Fatalf("Failed to get node for pod %s: %v", podToEvict, err)
	}
	tests.Logger.Debugf("Simulated failed node: %s (running pod %s)", failedNodeName, podToEvict)

	podsOnFailedNode, err := podNamesOnNode(tc, failedNodeName)
	if err != nil {
		t.Fatalf("Failed to get pods on failed node: %v", err)
	}
	tests.Logger.Debugf("Pods on failed node %s: %v", failedNodeName, podsOnFailedNode)

	healthyNodePods := identifyHealthyNodePods(existingPodNames, podsOnFailedNode)
	tests.Logger.Debugf("Pods on healthy nodes (should not be disrupted): %d pods", len(healthyNodePods))

	tests.Logger.Info("4. Update pc-a's nodeAffinity to exclude the failed node's hostname")
	if err = excludeNodeFromPodCliqueAffinity(tc, "pc-a", failedNodeName); err != nil {
		t.Fatalf("Failed to update pc-a nodeAffinity to exclude node %s: %v", failedNodeName, err)
	}

	tests.Logger.Info("5. Verify NO pods are automatically deleted (OnDelete behavior)")
	verifyNoAutomaticDeletionAfterUpdate(tc, tracker, existingPodNames, 10, true)

	tests.Logger.Info("6. Manually delete the pod on the 'failed node' (simulating eviction)")
	if err = updateutils.DeletePodAndWaitForTermination(tc, podToEvict); err != nil {
		t.Fatalf("Failed to delete pod (simulating eviction): %v", err)
	}

	if err = tc.WaitForRunningPods(10); err != nil {
		t.Fatalf("Failed to wait for replacement pod to be created")
	}

	tests.Logger.Info("7. Verify the replacement pod has the updated nodeAffinity and is NOT on the failed node")
	newPodName, err := findFirstNewPodName(tc, existingPodNames)
	if err != nil {
		t.Fatalf("Failed to find replacement pod: %v", err)
	}
	tests.Logger.Debugf("Replacement pod: %s (replaced evicted pod %s)", newPodName, podToEvict)

	if err = verifyPodHasNodeAffinityExclusion(tc, newPodName, failedNodeName); err != nil {
		t.Fatalf("Replacement pod does not have expected nodeAffinity exclusion: %v", err)
	}
	verifyPodNotOnNode(tc, newPodName, failedNodeName)

	tests.Logger.Info("8. Verify pods on healthy nodes were never disrupted")
	verifyPodsStillPresent(tc, healthyNodePods)

	tests.Logger.Info("OnDelete Update - Node Failure Recovery Workflow test (OD-8) completed successfully!")
}

// Test_OD9_StrategyTransition tests transitioning between RollingRecreate and OnDelete strategies.
// Scenario OD-9 (from proposal Testcase 2):
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload with OnDelete strategy, and verify 10 newly created pods
// 3. Update pc-a's spec and verify no auto-deletion (OnDelete behavior confirmed)
// 4. Switch strategy from OnDelete to RollingRecreate
// 5. Update pc-b's spec and verify pods ARE automatically recreated (RollingRecreate behavior)
// 6. Switch strategy back from RollingRecreate to OnDelete
// 7. Update pc-c's spec and verify no auto-deletion again (OnDelete behavior restored)
func Test_OD9_StrategyTransition(t *testing.T) {
	tests.Logger.Info("1. Initialize a 10-node Grove cluster")
	tests.Logger.Info("2. Deploy workload with OnDelete strategy, and verify 10 newly created pods")
	tc, cleanup, tracker := setupTest(t, testConfig{
		workloadName: "workload-ondelete",
		workloadYAML: "../../yaml/workload-ondelete.yaml",
		workerNodes:  10,
		expectedPods: 10,
	})
	defer cleanup()

	existingPodNames, err := captureExistingPodNames(tc)
	if err != nil {
		t.Fatalf("Failed to capture existing Pods: %v", err)
	}

	tests.Logger.Info("3. Update pc-a's spec and verify no auto-deletion (OnDelete behavior confirmed)")
	if err = updateutils.TriggerPodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("Failed to update PodClique pc-a spec: %v", err)
	}

	verifyNoAutomaticDeletionAfterUpdate(tc, tracker, existingPodNames, 10, true)

	tests.Logger.Info("4. Switch strategy from OnDelete to RollingRecreate")
	if err = updateutils.UpdatePCSUpdateStrategy(tc, grovev1beta1.RollingRecreateStrategy); err != nil {
		t.Fatalf("Failed to switch update strategy to RollingRecreate: %v", err)
	}

	tests.Logger.Info("5. Update pc-b's spec and verify pods ARE automatically recreated (RollingRecreate behavior)")
	// Capture pod names before rolling update
	preRollingUpdatePods, err := captureExistingPodNames(tc)
	if err != nil {
		t.Fatalf("Failed to capture pods before rolling update: %v", err)
	}

	// Trigger update on pc-b — with RollingRecreate, this should automatically recreate pods
	if err = updateutils.TriggerPodCliqueUpdate(tc, "pc-b"); err != nil {
		t.Fatalf("Failed to update PodClique pc-b spec: %v", err)
	}

	// Wait for rolling update to complete
	// pc-b has 1 replica per PCSG replica (2 PCSG replicas) = 2 pods in PCSG
	// RollingRecreate should handle the update automatically
	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 2 * time.Minute
	if err = updateutils.WaitForRollingUpdateComplete(&tcLongTimeout, 1); err != nil {
		t.Fatalf("Rolling update did not complete after strategy switch to RollingRecreate: %v", err)
	}

	// Wait for all pods to be running
	if err = tc.WaitForRunningPods(10); err != nil {
		t.Fatalf("Failed to wait for pods after rolling update: %v", err)
	}

	// Verify at least some pods were recreated (pc-b pods should have changed)
	podsAfterRolling, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list pods after rolling update: %v", err)
	}

	newPodsCreated := 0
	for _, pod := range podsAfterRolling.Items {
		if !preRollingUpdatePods[pod.Name] {
			newPodsCreated++
			tests.Logger.Debugf("New pod after RollingRecreate: %s", pod.Name)
		}
	}
	if newPodsCreated == 0 {
		t.Fatalf("No pods were recreated after switching to RollingRecreate and triggering update — strategy transition may have failed")
	}
	tests.Logger.Debugf("RollingRecreate automatically recreated %d pods", newPodsCreated)

	tests.Logger.Info("6. Switch strategy back from RollingRecreate to OnDelete")
	if err = updateutils.UpdatePCSUpdateStrategy(tc, grovev1beta1.OnDeleteStrategy); err != nil {
		t.Fatalf("Failed to switch update strategy back to OnDelete: %v", err)
	}

	tests.Logger.Info("7. Update pc-c's spec and verify no auto-deletion again (OnDelete behavior restored)")
	// Re-capture current pod state after the rolling update
	postSwitchPodNames, err := captureExistingPodNames(tc)
	if err != nil {
		t.Fatalf("Failed to capture pods after strategy switch: %v", err)
	}

	// Start a new tracker for the OnDelete verification
	tracker2 := updateutils.NewUpdateTracker()
	if err = tracker2.Start(tc); err != nil {
		t.Fatalf("Failed to start new tracker: %v", err)
	}
	defer tracker2.Stop()

	if err = updateutils.TriggerPodCliqueUpdate(tc, "pc-c"); err != nil {
		t.Fatalf("Failed to update PodClique pc-c spec: %v", err)
	}

	// Verify no auto-deletion after switching back to OnDelete
	verifyNoAutomaticDeletionAfterUpdate(tc, tracker2, postSwitchPodNames, 10, true)

	tests.Logger.Info("OnDelete Update - Strategy Transition test (OD-9) completed successfully!")
}

// Test_OD10_ScaleOutAfterOnDeleteUpdateNoTail verifies the OnDelete update then PCSG scale-out flow for
// a workload whose PodCliqueScalingGroup has replicas == minAvailable, so the PodGangMap has no tail
// entry (anchor + scale-out only). An OnDelete update advances the PodCliqueSet generation hash but does
// not churn PodGangs. The PodGangMap entries must be advanced to the new hash so the scale-out entry's
// DependsOn resolves to the anchor epoch. Without that, DependsOn holds an empty epoch (""), which
// resolves to a dependency PodGang name that never exists, so the scaled-out pods stay scheduling-gated.
// Scenario OD-10:
// 1. Initialize a 14-node Grove cluster
// 2. Deploy the OnDelete workload (sg-x replicas 2, minAvailable 2), verify 10 pods
// 3. Assert the initial PodGangMap has an anchor and a scale-out entry, and no tail entry
// 4. Trigger an OnDelete update on pc-c, then wait for it to complete
// 5. Assert the entries are unchanged except that all now carry the new generation hash
// 6. Scale out sg-x from 2 to 3 replicas, and assert the new index lands in the scale-out entry
// 7. Verify all pods including the scaled-out replica become running, none stranded gated
func Test_OD10_ScaleOutAfterOnDeleteUpdateNoTail(t *testing.T) {
	tests.Logger.Info("1. Initialize a 14-node Grove cluster")
	tests.Logger.Info("2. Deploy the OnDelete workload (sg-x replicas 2, minAvailable 2), verify 10 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload-ondelete",
		workloadYAML: "../../yaml/workload-ondelete.yaml",
		workerNodes:  14,
		expectedPods: 10,
	})
	defer cleanup()

	tests.Logger.Info("3. Assert the initial PodGangMap has an anchor and a scale-out entry, and no tail entry")
	oldHash := updateutils.PCSGenerationHash(t, tc)
	before := updateutils.PodGangMapEntries(t, tc, 0)
	updateutils.AssertEntryRoles(t, before, grovev1beta1.PodGangEntryRoleAnchor, grovev1beta1.PodGangEntryRoleScaleOut)

	anchorEntry := updateutils.RequireSingleEntryByRole(t, before, grovev1beta1.PodGangEntryRoleAnchor)
	updateutils.AssertStandalonePCLQPodCounts(t, anchorEntry, map[string]int32{"pc-a": 2})
	updateutils.AssertPodGangEntryPCSGIndices(t, anchorEntry, "sg-x", []int32{0, 1})
	updateutils.AssertPodGangEntryDependsOn(t, anchorEntry, nil)

	scaleOutEntry := updateutils.RequireSingleEntryByRole(t, before, grovev1beta1.PodGangEntryRoleScaleOut)
	updateutils.AssertPodGangEntryPCSGIndices(t, scaleOutEntry, "sg-x", nil)
	updateutils.AssertPodGangEntryDependsOn(t, scaleOutEntry, []string{anchorEntry.Epoch})

	tests.Logger.Info("4. Trigger an OnDelete update on pc-c, then wait for it to complete")
	if err := updateutils.TriggerPodCliqueUpdate(tc, "pc-c"); err != nil {
		t.Fatalf("Failed to update PodClique pc-c spec: %v", err)
	}
	if err := waitForOnDeleteUpdateCompleteWithTimeout(tc, 1*time.Minute); err != nil {
		t.Fatalf("Failed to verify OnDelete update completion: %v", err)
	}

	tests.Logger.Info("5. Assert the entries are unchanged except that all now carry the new generation hash")
	newHash := updateutils.PCSGenerationHash(t, tc)
	if newHash == oldHash {
		t.Fatalf("PodCliqueSet generation hash did not change after the update: %s", newHash)
	}
	after := updateutils.PodGangMapEntries(t, tc, 0)
	updateutils.AssertEntriesUnchangedExceptHashForNonCoherentUpdate(t, before, after, newHash)

	tests.Logger.Info("6. Scale out sg-x from 2 to 3 replicas, and assert the new index lands in the scale-out entry")
	// sg-x holds pc-b (1) + pc-c (3) = 4 pods per PCSG replica. With pc-a (2 standalone) the totals are
	// 2 + 4*2 = 10 at 2 replicas and 2 + 4*3 = 14 at 3 replicas.
	tc.ScalePCSGAcrossAllReplicasAndWait(tc.Workload.Name, "sg-x", 1, 3, 14, 0)

	afterScaleOut := updateutils.PodGangMapEntries(t, tc, 0)
	scaleOutEntry = updateutils.RequireSingleEntryByRole(t, afterScaleOut, grovev1beta1.PodGangEntryRoleScaleOut)
	updateutils.AssertPodGangEntryPCSGIndices(t, scaleOutEntry, "sg-x", []int32{2})
	updateutils.AssertPodGangEntryDependsOn(t, scaleOutEntry, []string{anchorEntry.Epoch})

	tests.Logger.Info("7. Verify all pods including the scaled-out replica become running, none stranded gated")
	if err := tc.WaitForRunningPods(14); err != nil {
		t.Fatalf("Scaled-out pods did not all become running after an OnDelete update; they may be stranded scheduling-gated: %v", err)
	}

	tests.Logger.Info("OnDelete Update - Scale-Out After OnDelete Update (no tail) test (OD-10) completed successfully!")
}

// Test_OD11_ScaleOutAfterOnDeleteUpdateWithTail is the OD-10 flow for a workload whose
// PodCliqueScalingGroup has replicas > minAvailable, so the PodGangMap materializes a tail entry
// (anchor + tail + scale-out). It asserts the same invariants across the update, so the tail entry is
// exercised alongside the anchor and scale-out.
// Scenario OD-11:
// 1. Initialize a 14-node Grove cluster
// 2. Deploy the with-tail OnDelete workload (sg-x replicas 2, minAvailable 1), verify 10 pods
// 3. Assert the initial PodGangMap has an anchor, a tail, and a scale-out entry
// 4. Trigger an OnDelete update on pc-c, then wait for it to complete
// 5. Assert the entries are unchanged except that all now carry the new generation hash
// 6. Scale out sg-x from 2 to 3 replicas, and assert the new index lands in the scale-out entry
// 7. Verify all pods including the scaled-out replica become running, none stranded gated
func Test_OD11_ScaleOutAfterOnDeleteUpdateWithTail(t *testing.T) {
	tests.Logger.Info("1. Initialize a 14-node Grove cluster")
	tests.Logger.Info("2. Deploy the with-tail OnDelete workload (sg-x replicas 2, minAvailable 1), verify 10 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload-ondelete-with-tail",
		workloadYAML: "../../yaml/workload-ondelete-with-tail.yaml",
		workerNodes:  14,
		expectedPods: 10,
	})
	defer cleanup()

	tests.Logger.Info("3. Assert the initial PodGangMap has an anchor, a tail, and a scale-out entry")
	oldHash := updateutils.PCSGenerationHash(t, tc)
	before := updateutils.PodGangMapEntries(t, tc, 0)
	updateutils.AssertEntryRoles(t, before,
		grovev1beta1.PodGangEntryRoleAnchor,
		grovev1beta1.PodGangEntryRoleTail,
		grovev1beta1.PodGangEntryRoleScaleOut)

	anchorEntry := updateutils.RequireSingleEntryByRole(t, before, grovev1beta1.PodGangEntryRoleAnchor)
	updateutils.AssertStandalonePCLQPodCounts(t, anchorEntry, map[string]int32{"pc-a": 2})
	updateutils.AssertPodGangEntryPCSGIndices(t, anchorEntry, "sg-x", []int32{0})
	updateutils.AssertPodGangEntryDependsOn(t, anchorEntry, nil)

	tailEntry := updateutils.RequireSingleEntryByRole(t, before, grovev1beta1.PodGangEntryRoleTail)
	updateutils.AssertPodGangEntryPCSGIndices(t, tailEntry, "sg-x", []int32{1})
	updateutils.AssertPodGangEntryDependsOn(t, tailEntry, []string{anchorEntry.Epoch})

	scaleOutEntry := updateutils.RequireSingleEntryByRole(t, before, grovev1beta1.PodGangEntryRoleScaleOut)
	updateutils.AssertPodGangEntryPCSGIndices(t, scaleOutEntry, "sg-x", nil)
	updateutils.AssertPodGangEntryDependsOn(t, scaleOutEntry, []string{anchorEntry.Epoch})

	tests.Logger.Info("4. Trigger an OnDelete update on pc-c, then wait for it to complete")
	if err := updateutils.TriggerPodCliqueUpdate(tc, "pc-c"); err != nil {
		t.Fatalf("Failed to update PodClique pc-c spec: %v", err)
	}
	if err := waitForOnDeleteUpdateCompleteWithTimeout(tc, 1*time.Minute); err != nil {
		t.Fatalf("Failed to verify OnDelete update completion: %v", err)
	}

	tests.Logger.Info("5. Assert the entries are unchanged except that all now carry the new generation hash")
	newHash := updateutils.PCSGenerationHash(t, tc)
	if newHash == oldHash {
		t.Fatalf("PodCliqueSet generation hash did not change after the update: %s", newHash)
	}
	after := updateutils.PodGangMapEntries(t, tc, 0)
	updateutils.AssertEntriesUnchangedExceptHashForNonCoherentUpdate(t, before, after, newHash)

	tests.Logger.Info("6. Scale out sg-x from 2 to 3 replicas, and assert the new index lands in the scale-out entry")
	tc.ScalePCSGAcrossAllReplicasAndWait(tc.Workload.Name, "sg-x", 1, 3, 14, 0)

	afterScaleOut := updateutils.PodGangMapEntries(t, tc, 0)
	scaleOutEntry = updateutils.RequireSingleEntryByRole(t, afterScaleOut, grovev1beta1.PodGangEntryRoleScaleOut)
	updateutils.AssertPodGangEntryPCSGIndices(t, scaleOutEntry, "sg-x", []int32{2})
	updateutils.AssertPodGangEntryDependsOn(t, scaleOutEntry, []string{anchorEntry.Epoch})

	tests.Logger.Info("7. Verify all pods including the scaled-out replica become running, none stranded gated")
	if err := tc.WaitForRunningPods(14); err != nil {
		t.Fatalf("Scaled-out pods did not all become running after an OnDelete update; they may be stranded scheduling-gated: %v", err)
	}

	tests.Logger.Info("OnDelete Update - Scale-Out After OnDelete Update (with tail) test (OD-11) completed successfully!")
}

// captureExistingPodNames returns a map of all current pod names for the workload.
func captureExistingPodNames(tc *testctx.TestContext) (map[string]bool, error) {
	pods, err := tc.ListPods()
	if err != nil {
		return nil, err
	}

	existingPodNames := make(map[string]bool, len(pods.Items))
	for _, pod := range pods.Items {
		existingPodNames[pod.Name] = true
	}

	tests.Logger.Debugf("Captured %d existing pods before spec change", len(existingPodNames))
	return existingPodNames, nil
}

// verifyNoAutomaticDeletionAfterUpdate checks that an OnDelete template edit preserves existing pods until manual deletion.
func verifyNoAutomaticDeletionAfterUpdate(
	tc *testctx.TestContext,
	tracker *updateutils.UpdateTracker,
	existingPodNames map[string]bool,
	expectedPods int,
	verifyProgressFields bool,
) {
	tc.T.Helper()

	time.Sleep(10 * time.Second)

	if err := waitForOnDeleteUpdateCompleteWithTimeout(tc, 1*time.Minute); err != nil {
		tc.T.Fatalf("Failed to verify OnDelete update completion: %v", err)
	}

	if verifyProgressFields {
		verifyUpdateProgressFields(tc)
	}

	tracker.Stop()
	events := tracker.Events()
	verifyNoPodsDeleted(tc, events, existingPodNames)

	pods, err := tc.ListPods()
	if err != nil {
		tc.T.Fatalf("Failed to list Pods: %v", err)
	}

	if len(pods.Items) != expectedPods {
		tc.T.Fatalf("Expected %d pods to still be running, but got %d", expectedPods, len(pods.Items))
	}
}

// waitForOnDeleteUpdateCompleteWithTimeout waits for OnDelete update completion with the supplied timeout.
//
//nolint:unparam // Keep the caller's observation deadline explicit.
func waitForOnDeleteUpdateCompleteWithTimeout(tc *testctx.TestContext, timeout time.Duration) error {
	tcWithTimeout := *tc
	tcWithTimeout.Timeout = timeout
	return waitForOnDeleteUpdateComplete(&tcWithTimeout)
}

func waitForOnDeleteUpdateComplete(tc *testctx.TestContext) error {
	pollCount := 0
	fetchPCS := waiter.FetchFunc[*grovev1beta1.PodCliqueSet](func(_ context.Context) (*grovev1beta1.PodCliqueSet, error) {
		return updateutils.GetPCS(tc, tc.Workload.Name)
	})
	predicate := waiter.Predicate[*grovev1beta1.PodCliqueSet](func(pcs *grovev1beta1.PodCliqueSet) bool {
		pollCount++
		if pcs == nil {
			// A transient cache miss returns a nil object. Keep polling.
			return false
		}
		if pcs.Status.ObservedGeneration == nil || *pcs.Status.ObservedGeneration != pcs.Generation {
			return false
		}
		if workload.IsOnDeleteUpdateComplete(pcs) {
			tests.Logger.Debugf("[waitForOnDeleteUpdateComplete] OnDelete update marked complete after %d polls (UpdatedReplicas=%d)",
				pollCount, pcs.Status.UpdatedReplicas)
			return true
		}
		return false
	})
	w := waiter.New[*grovev1beta1.PodCliqueSet]().WithTimeout(tc.Timeout).WithInterval(tc.Interval)
	_, err := w.WaitFor(tc.Ctx, fetchPCS, predicate)
	return err
}

func verifyUpdateProgressFields(tc *testctx.TestContext) {
	tc.T.Helper()

	pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
	if err != nil {
		tc.T.Fatalf("Failed to get PCS for UpdateProgress: %v", err)
	}
	updateProgress := pcs.Status.UpdateProgress

	if updateProgress == nil {
		tc.T.Fatalf("UpdateProgress should not be nil after OnDelete update")
	}

	if updateProgress.UpdateStartedAt.IsZero() {
		tc.T.Fatalf("UpdateProgress.UpdateStartedAt should be set")
	}

	if updateProgress.UpdateEndedAt == nil {
		tc.T.Fatalf("UpdateProgress.UpdateEndedAt should be set for OnDelete strategy")
	}

	if updateProgress.CurrentlyUpdating != nil {
		tc.T.Fatalf("UpdateProgress.CurrentlyUpdating should be nil for OnDelete strategy, got %v", updateProgress.CurrentlyUpdating)
	}

	// For OnDelete, UpdateEndedAt is set immediately when the update is initiated — it does not
	// imply that PCLQs have converged to the new generation hash. Pods are replaced only after
	// the user manually deletes them, so UpdatedPodCliquesCount may legitimately be 0 (no manual
	// deletes) or partial (some manual deletes). The assertions below check sanity (counts
	// derived, non-negative, updated <= total), not convergence.
	if updateProgress.TotalPodCliquesCount == 0 && updateProgress.TotalPodCliqueScalingGroupsCount == 0 {
		tc.T.Fatalf("UpdateProgress totals are 0/0 — count derivation did not run on a non-empty PCS")
	}
	if updateProgress.UpdatedPodCliquesCount > updateProgress.TotalPodCliquesCount {
		tc.T.Fatalf("UpdateProgress.UpdatedPodCliquesCount = %d > TotalPodCliquesCount = %d (counts cannot exceed totals)",
			updateProgress.UpdatedPodCliquesCount, updateProgress.TotalPodCliquesCount)
	}
	if updateProgress.UpdatedPodCliqueScalingGroupsCount > updateProgress.TotalPodCliqueScalingGroupsCount {
		tc.T.Fatalf("UpdateProgress.UpdatedPodCliqueScalingGroupsCount = %d > TotalPodCliqueScalingGroupsCount = %d (counts cannot exceed totals)",
			updateProgress.UpdatedPodCliqueScalingGroupsCount, updateProgress.TotalPodCliqueScalingGroupsCount)
	}
}

func verifyNoPodsDeleted(tc *testctx.TestContext, events []updateutils.PodEvent, existingPodNames map[string]bool) {
	tc.T.Helper()

	deletedPods := []string{}
	for _, event := range events {
		if event.EventType == watch.Deleted && existingPodNames[event.Pod.Name] {
			deletedPods = append(deletedPods, event.Pod.Name)
		}
	}

	if len(deletedPods) > 0 {
		tc.T.Fatalf("OnDelete strategy violated: pods were automatically deleted when spec changed: %v", deletedPods)
	}
}

// firstPodForClique returns the name of the first pod belonging to the specified clique.
func firstPodForClique(tc *testctx.TestContext, cliqueName string) (string, error) {
	cliquePods, err := podsForClique(tc, cliqueName)
	if err != nil {
		return "", err
	}
	if len(cliquePods) == 0 {
		return "", fmt.Errorf("no pods found for %s", cliqueName)
	}
	return cliquePods[0], nil
}

// podsForClique returns the names of all pods belonging to the specified clique.
func podsForClique(tc *testctx.TestContext, cliqueName string) ([]string, error) {
	tc.T.Helper()

	pods, err := tc.ListPods()
	if err != nil {
		return nil, err
	}

	var cliquePods []string
	for _, pod := range pods.Items {
		if pod.Labels == nil {
			continue
		}
		if pclq, ok := pod.Labels[apicommon.LabelPodClique]; ok && strings.HasSuffix(pclq, "-"+cliqueName) {
			cliquePods = append(cliquePods, pod.Name)
		}
	}

	return cliquePods, nil
}

// findFirstNewPodName returns the name of the first pod not present in the given set.
func findFirstNewPodName(tc *testctx.TestContext, existingPodNames map[string]bool) (string, error) {
	pods, err := tc.ListPods()
	if err != nil {
		return "", err
	}

	for _, pod := range pods.Items {
		if !existingPodNames[pod.Name] {
			return pod.Name, nil
		}
	}

	return "", fmt.Errorf("could not find newly created replacement pod")
}

// nodeForPod returns the node name where the specified pod is scheduled.
func nodeForPod(tc *testctx.TestContext, podName string) (string, error) {
	tc.T.Helper()

	var pod corev1.Pod
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: podName}, &pod); err != nil {
		return "", fmt.Errorf("failed to get pod %s: %w", podName, err)
	}
	if pod.Spec.NodeName == "" {
		return "", fmt.Errorf("pod %s has no node assignment", podName)
	}
	return pod.Spec.NodeName, nil
}

// podNamesOnNode returns the names of pods scheduled on the specified node.
func podNamesOnNode(tc *testctx.TestContext, nodeName string) ([]string, error) {
	tc.T.Helper()

	pods, err := tc.ListPods()
	if err != nil {
		return nil, err
	}

	var podsOnNode []string
	for _, pod := range pods.Items {
		if pod.Spec.NodeName == nodeName {
			podsOnNode = append(podsOnNode, pod.Name)
		}
	}

	return podsOnNode, nil
}

// identifyHealthyNodePods returns the set of pod names that are NOT on the specified node.
func identifyHealthyNodePods(allPods map[string]bool, podsOnExcludedNode []string) map[string]bool {
	healthyNodePods := make(map[string]bool)
	excludedSet := make(map[string]bool, len(podsOnExcludedNode))
	for _, name := range podsOnExcludedNode {
		excludedSet[name] = true
	}
	for name := range allPods {
		if !excludedSet[name] {
			healthyNodePods[name] = true
		}
	}
	return healthyNodePods
}

// excludeNodeFromPodCliqueAffinity updates the PCS spec to add a nodeAffinity exclusion
// (kubernetes.io/hostname NotIn [nodeName]) to the specified PodClique's nodeAffinity.
// This simulates the node failure recovery workflow from GREP-291 Example Usage,
// where a failed node is excluded by adding a NotIn matchExpression for its hostname.
func excludeNodeFromPodCliqueAffinity(tc *testctx.TestContext, cliqueName string, nodeName string) error {
	tc.T.Helper()

	pcsName := tc.Workload.Name
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var pcs grovev1beta1.PodCliqueSet
		if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Name: pcsName, Namespace: tc.Namespace}, &pcs); err != nil {
			return fmt.Errorf("failed to get PodCliqueSet: %w", err)
		}
		pcs.SetGroupVersionKind(grovev1beta1.SchemeGroupVersion.WithKind("PodCliqueSet"))
		pcs.SetManagedFields(nil)
		pcs.SetResourceVersion("")

		found := false
		for i, clique := range pcs.Spec.Template.Cliques {
			if clique.Name == cliqueName {
				affinity := pcs.Spec.Template.Cliques[i].Spec.PodSpec.Affinity
				if affinity == nil || affinity.NodeAffinity == nil ||
					affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil ||
					len(affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms) == 0 {
					return fmt.Errorf("clique %s has no existing nodeAffinity nodeSelectorTerms to append to", cliqueName)
				}

				// Append a NotIn matchExpression to the first nodeSelectorTerm, matching the GREP-291 Example Usage:
				//   - key: kubernetes.io/hostname
				//     operator: NotIn
				//     values: [<failed-node-name>]
				terms := &affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0]
				terms.MatchExpressions = append(terms.MatchExpressions, corev1.NodeSelectorRequirement{
					Key:      "kubernetes.io/hostname",
					Operator: corev1.NodeSelectorOpNotIn,
					Values:   []string{nodeName},
				})

				found = true
				break
			}
		}

		if !found {
			return fmt.Errorf("clique %s not found in PodCliqueSet %s", cliqueName, pcsName)
		}

		return tc.Client.Patch(tc.Ctx, &pcs, client.Apply, client.FieldOwner("e2e-rolling-update-test"), client.ForceOwnership)
	})
}

// verifyPodHasNodeAffinityExclusion checks that the given pod's spec contains a
// kubernetes.io/hostname NotIn matchExpression that excludes the specified node name.
func verifyPodHasNodeAffinityExclusion(tc *testctx.TestContext, podName string, excludedNode string) error {
	tc.T.Helper()

	var pod corev1.Pod
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: podName}, &pod); err != nil {
		return fmt.Errorf("failed to get pod %s: %w", podName, err)
	}

	if pod.Spec.Affinity == nil || pod.Spec.Affinity.NodeAffinity == nil ||
		pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return fmt.Errorf("pod %s has no requiredDuringSchedulingIgnoredDuringExecution nodeAffinity", podName)
	}

	for _, term := range pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		for _, expr := range term.MatchExpressions {
			if expr.Key == "kubernetes.io/hostname" && expr.Operator == corev1.NodeSelectorOpNotIn {
				if slices.Contains(expr.Values, excludedNode) {
					return nil
				}
			}
		}
	}

	return fmt.Errorf("pod %s does not have kubernetes.io/hostname NotIn [%s] in its nodeAffinity", podName, excludedNode)
}

// verifyPodNotOnNode asserts that the specified pod is not scheduled on the given node.
func verifyPodNotOnNode(tc *testctx.TestContext, podName string, excludedNodeName string) {
	tc.T.Helper()

	nodeName, err := nodeForPod(tc, podName)
	if err != nil {
		tc.T.Fatalf("Failed to get node for pod %s: %v", podName, err)
	}
	if nodeName == excludedNodeName {
		tc.T.Fatalf("Pod %s is scheduled on excluded node %s — nodeAffinity exclusion not enforced", podName, excludedNodeName)
	}
	tests.Logger.Debugf("Pod %s is on node %s (not on excluded node %s)", podName, nodeName, excludedNodeName)
}

// verifyPodsStillPresent asserts that all pods in the given set are still present in the cluster.
func verifyPodsStillPresent(tc *testctx.TestContext, expectedPods map[string]bool) {
	tc.T.Helper()

	pods, err := tc.ListPods()
	if err != nil {
		tc.T.Fatalf("Failed to list Pods: %v", err)
	}

	currentPods := make(map[string]bool, len(pods.Items))
	for _, pod := range pods.Items {
		currentPods[pod.Name] = true
	}

	for podName := range expectedPods {
		if !currentPods[podName] {
			tc.T.Fatalf("Pod %s was disrupted (deleted/recreated) — update strategy violated", podName)
		}
	}
}

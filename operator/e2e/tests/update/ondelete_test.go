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

package update

import (
	"fmt"
	"slices"
	"testing"
	"time"

	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	"github.com/ai-dynamo/grove/operator/e2e/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
		t.Fatalf("Failed to capture existing pods: %v", err)
	}

	tests.Logger.Info("3. Change the specification of pc-a")
	if err := triggerPodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	tests.Logger.Info("4. Verify that NO pods are automatically deleted")
	tests.Logger.Info("5. Verify that the update is marked complete (UpdateEndedAt is set)")
	verifyNoAutomaticDeletionAfterUpdate(tc, tracker, existingPodNames, 10, true)

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
		t.Fatalf("Failed to capture existing pods: %v", err)
	}

	podToDelete, err := getFirstPodForClique(tc, "pc-a")
	if err != nil {
		t.Fatalf("Failed to get pod for pc-a: %v", err)
	}
	tests.Logger.Debugf("Will delete pod %s after spec change", podToDelete)

	tests.Logger.Info("3. Change the specification of pc-a")
	if err = triggerPodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	// Wait for update to be marked complete
	if err = waitForOnDeleteUpdateCompleteWithTimeout(tc, 1*time.Minute); err != nil {
		t.Fatalf("Failed to verify OnDelete update completion: %v", err)
	}

	tests.Logger.Info("4. Delete a pod manually and verify the replacement uses the new template")
	// Delete the pod
	if err = deletePodAndWaitForTermination(tc, podToDelete); err != nil {
		t.Fatalf("Failed to delete pod and wait for replacement: %v", err)
	}

	if err = tests.WaitForRunningPods(tc, 10); err != nil {
		t.Fatalf("Failed to wait for pod with new specification to be created")
	}

	newPodName, err := findFirstNewPodName(tc, existingPodNames)
	if err != nil {
		t.Fatalf("Failed to find replacement pod: %v", err)
	}

	tests.Logger.Debugf("New replacement pod: %s", newPodName)

	// Verify the new pod has the updated spec
	if err = verifyPodHasUpdatedSpec(tc, newPodName); err != nil {
		t.Fatalf("Replacement pod does not have updated spec: %v", err)
	}

	tests.Logger.Info("5. Verify status fields accurately reflect the update state")
	// Get PCLQ and verify UpdatedReplicas has increased
	pclqName := fmt.Sprintf("%s-%d-%s", tc.Workload.Name, 0, "pc-a")
	pclqGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliques"}
	unstructuredPCLQ, err := tc.DynamicClient.Resource(pclqGVR).Namespace(tc.Namespace).Get(tc.Ctx, pclqName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get PodClique: %v", err)
	}

	var pclq grovev1alpha1.PodClique
	if err = utils.ConvertUnstructuredToTyped(unstructuredPCLQ.Object, &pclq); err != nil {
		t.Fatalf("Failed to convert to PodClique: %v", err)
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
	oldPcaPods, err := getPodsForClique(tc, "pc-a")
	if err != nil {
		t.Fatalf("Failed to get pods for pc-a: %v", err)
	}
	tests.Logger.Debugf("pc-a pods before update: %v", oldPcaPods)

	if len(oldPcaPods) != 2 {
		t.Fatalf("Expected 2 pc-a pods, got %d", len(oldPcaPods))
	}

	tests.Logger.Info("3. Change the specification of pc-a (triggering OnDelete update)")
	if err = triggerPodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	// Wait for update to be marked complete
	if err = waitForOnDeleteUpdateCompleteWithTimeout(tc, 1*time.Minute); err != nil {
		t.Fatalf("Failed to verify OnDelete update completion: %v", err)
	}

	tests.Logger.Info("4. Manually delete ONE pc-a pod to update it")
	// Delete the first pod from pc-a to create one with updated spec
	podToUpdate := oldPcaPods[0]
	if err = deletePodAndWaitForTermination(tc, podToUpdate); err != nil {
		t.Fatalf("Failed to delete pod and wait for replacement: %v", err)
	}

	// wait for the new pc-a pod to be created
	if err = tests.WaitForReadyPods(tc, 10); err != nil {
		t.Fatalf("Failed to wait for pod with new specification to be created")
	}

	// Get the new pc-a pods
	updatedPcaPods, err := getPodsForClique(tc, "pc-a")
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
	if err = scalePodCliqueInPCS(tc, "pc-a", 1); err != nil {
		t.Fatalf("Failed to scale in PodClique pc-a: %v", err)
	}

	// Wait for scale-in to complete (9 total pods: 1 pc-a + 8 others)
	if err = tests.WaitForPods(tc, 9); err != nil {
		t.Fatalf("Failed to wait for pods after pc-a scale-in: %v", err)
	}

	tests.Logger.Info("6. Verify pods with outdated templates are deleted first (not the updated one)")
	// Get the remaining pc-a pods
	remainingPcaPods, err := getPodsForClique(tc, "pc-a")
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
		t.Fatalf("Failed to capture existing pods: %v", err)
	}

	tests.Logger.Info("3. Change the specification of pc-b (part of PCSG sg-x)")
	if err = triggerPodCliqueUpdate(tc, "pc-b"); err != nil {
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
		t.Fatalf("Failed to capture existing pods: %v", err)
	}

	podToDelete, err := getFirstPodForClique(tc, "pc-c")
	if err != nil {
		t.Fatalf("Failed to get pod for pc-c: %v", err)
	}
	tests.Logger.Debugf("Will delete pod %s after spec change", podToDelete)

	tests.Logger.Info("3. Change the specification of pc-c (part of PCSG sg-x)")
	if err = triggerPodCliqueUpdate(tc, "pc-c"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	// Wait for update to be marked complete
	if err = waitForOnDeleteUpdateCompleteWithTimeout(tc, 1*time.Minute); err != nil {
		t.Fatalf("Failed to verify OnDelete update completion: %v", err)
	}

	tests.Logger.Info("4. Delete a pc-c pod manually and verify the replacement uses the new template")
	// Delete the pod
	if err = deletePodAndWaitForTermination(tc, podToDelete); err != nil {
		t.Fatalf("Failed to delete pod and wait for replacement: %v", err)
	}

	if err = tests.WaitForRunningPods(tc, 10); err != nil {
		t.Fatalf("Failed to wait for pod with new specification to be created")
	}

	newPodName, err := findFirstNewPodName(tc, existingPodNames)
	if err != nil {
		t.Fatalf("Failed to find replacement pod: %v", err)
	}

	tests.Logger.Debugf("New replacement pod: %s", newPodName)

	// Verify the new pod has the updated spec
	if err = verifyPodHasUpdatedSpec(tc, newPodName); err != nil {
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
		t.Fatalf("Failed to capture existing pods: %v", err)
	}

	tests.Logger.Info("3. Change the specification of ALL PodCliques (pc-a, pc-b, pc-c)")
	for _, cliqueName := range []string{"pc-a", "pc-b", "pc-c"} {
		if err = triggerPodCliqueUpdate(tc, cliqueName); err != nil {
			t.Fatalf("Failed to update PodClique %s spec: %v", cliqueName, err)
		}
	}

	tests.Logger.Info("4. Verify that NO pods are automatically deleted")
	verifyNoAutomaticDeletionAfterUpdate(tc, tracker, existingPodNames, 10, true)

	tests.Logger.Info("5. Manually delete one pod from each type (standalone and PCSG)")
	standalonePodToDelete, err := getFirstPodForClique(tc, "pc-a")
	if err != nil {
		t.Fatalf("Failed to get pod for pc-a: %v", err)
	}

	pcsgPodToDelete, err := getFirstPodForClique(tc, "pc-c")
	if err != nil {
		t.Fatalf("Failed to get pod for pc-c: %v", err)
	}

	// Delete standalone pod
	tests.Logger.Debugf("Deleting standalone pod: %s", standalonePodToDelete)
	if err = deletePodAndWaitForTermination(tc, standalonePodToDelete); err != nil {
		t.Fatalf("Failed to delete standalone pod and wait for replacement: %v", err)
	}

	if err = tests.WaitForRunningPods(tc, 10); err != nil {
		t.Fatalf("Failed to wait for pod with new specification to be created")
	}

	// Delete PCSG pod
	tests.Logger.Debugf("Deleting PCSG pod: %s", pcsgPodToDelete)
	if err = deletePodAndWaitForTermination(tc, pcsgPodToDelete); err != nil {
		t.Fatalf("Failed to delete PCSG pod and wait for replacement: %v", err)
	}

	if err = tests.WaitForRunningPods(tc, 10); err != nil {
		t.Fatalf("Failed to wait for pod with new specification to be created")
	}

	tests.Logger.Info("6. Verify replacements use the new template")
	// Get final pod list
	finalPods, err := tests.ListPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods after deletions: %v", err)
	}

	// Find new pods and verify they have updated spec
	newPodsFound := 0
	for _, pod := range finalPods.Items {
		if !existingPodNames[pod.Name] {
			newPodsFound++
			if err := verifyPodHasUpdatedSpec(tc, pod.Name); err != nil {
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
		t.Fatalf("Failed to capture existing pods: %v", err)
	}

	tests.Logger.Info("3. Change the specification of pc-a")
	if err = triggerPodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	tests.Logger.Info("4. Verify uniform strategy application across both replicas")
	verifyNoAutomaticDeletionAfterUpdate(tc, tracker, existingPodNames, 20, false)

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
		t.Fatalf("Failed to capture existing pods: %v", err)
	}

	tests.Logger.Info("3. Identify a node running a pc-a pod (simulating a 'failed node')")
	podToEvict, err := getFirstPodForClique(tc, "pc-a")
	if err != nil {
		t.Fatalf("Failed to get pod for pc-a: %v", err)
	}

	failedNodeName, err := getNodeForPod(tc, podToEvict)
	if err != nil {
		t.Fatalf("Failed to get node for pod %s: %v", podToEvict, err)
	}
	tests.Logger.Debugf("Simulated failed node: %s (running pod %s)", failedNodeName, podToEvict)

	podsOnFailedNode, err := getPodsOnNode(tc, failedNodeName)
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
	if err = deletePodAndWaitForTermination(tc, podToEvict); err != nil {
		t.Fatalf("Failed to delete pod (simulating eviction): %v", err)
	}

	if err = tests.WaitForRunningPods(tc, 10); err != nil {
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
		t.Fatalf("Failed to capture existing pods: %v", err)
	}

	tests.Logger.Info("3. Update pc-a's spec and verify no auto-deletion (OnDelete behavior confirmed)")
	if err = triggerPodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("Failed to update PodClique pc-a spec: %v", err)
	}

	verifyNoAutomaticDeletionAfterUpdate(tc, tracker, existingPodNames, 10, true)

	tests.Logger.Info("4. Switch strategy from OnDelete to RollingRecreate")
	if err = updatePCSUpdateStrategy(tc, grovev1alpha1.RollingRecreateStrategy); err != nil {
		t.Fatalf("Failed to switch update strategy to RollingRecreate: %v", err)
	}

	tests.Logger.Info("5. Update pc-b's spec and verify pods ARE automatically recreated (RollingRecreate behavior)")
	// Capture pod names before rolling update
	preRollingUpdatePods, err := captureExistingPodNames(tc)
	if err != nil {
		t.Fatalf("Failed to capture pods before rolling update: %v", err)
	}

	// Trigger update on pc-b — with RollingRecreate, this should automatically recreate pods
	if err = triggerPodCliqueUpdate(tc, "pc-b"); err != nil {
		t.Fatalf("Failed to update PodClique pc-b spec: %v", err)
	}

	// Wait for rolling update to complete
	// pc-b has 1 replica per PCSG replica (2 PCSG replicas) = 2 pods in PCSG
	// RollingRecreate should handle the update automatically
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 2 * time.Minute
	if err = waitForRollingUpdateComplete(tcLongTimeout, 1); err != nil {
		t.Fatalf("Rolling update did not complete after strategy switch to RollingRecreate: %v", err)
	}

	// Wait for all pods to be running
	if err = tests.WaitForRunningPods(tc, 10); err != nil {
		t.Fatalf("Failed to wait for pods after rolling update: %v", err)
	}

	// Verify at least some pods were recreated (pc-b pods should have changed)
	podsAfterRolling, err := tests.ListPods(tc)
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
	if err = updatePCSUpdateStrategy(tc, grovev1alpha1.OnDeleteStrategy); err != nil {
		t.Fatalf("Failed to switch update strategy back to OnDelete: %v", err)
	}

	tests.Logger.Info("7. Update pc-c's spec and verify no auto-deletion again (OnDelete behavior restored)")
	// Re-capture current pod state after the rolling update
	postSwitchPodNames, err := captureExistingPodNames(tc)
	if err != nil {
		t.Fatalf("Failed to capture pods after strategy switch: %v", err)
	}

	// Start a new tracker for the OnDelete verification
	tracker2 := newUpdateTracker()
	if err = tracker2.start(tc); err != nil {
		t.Fatalf("Failed to start new tracker: %v", err)
	}
	defer tracker2.stop()

	if err = triggerPodCliqueUpdate(tc, "pc-c"); err != nil {
		t.Fatalf("Failed to update PodClique pc-c spec: %v", err)
	}

	// Verify no auto-deletion after switching back to OnDelete
	verifyNoAutomaticDeletionAfterUpdate(tc, tracker2, postSwitchPodNames, 10, true)

	tests.Logger.Info("OnDelete Update - Strategy Transition test (OD-9) completed successfully!")
}

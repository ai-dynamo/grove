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
	"fmt"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/utils"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// TerminationDelay is a mirror of the value in the workload YAMLs.
	TerminationDelay = 10 * time.Second
)

// Test_GT1_GangTerminationFullReplicasPCSOwned tests gang-termination behavior when a PCS-owned PodClique is breached
// Scenario GT-1:
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Wait for pods to get scheduled and become ready
// 4. Cordon node and then delete 1 ready pod from PCS-owned podclique pcs-0-pc-a
// 5. Wait for TerminationDelay seconds
// 6. Verify that all pods in the workload get terminated
func Test_GT1_GangTerminationFullReplicasPCSOwned(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster")
	totalPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, totalPods)
	defer cleanup()

	logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
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
			ExpectedPods: totalPods,
		},
	}

	pods, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Wait for pods to get scheduled and become ready")
	if err := waitForReadyPods(tc, totalPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	// Verify pods are distributed across distinct nodes
	listPodsAndAssertDistinctNodes(tc)

	logger.Info("4. Cordon node and then delete 1 ready pod from PCS-owned podclique pcs-0-pc-a")
	// Refresh pods list to get current ready status
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to refresh pod list: %v", err)
	}

	// Find a pod from workload1-0-pc-a podclique (PCS-owned)
	targetPod := findReadyPodFromPodClique(pods, "workload1-0-pc-a")
	if targetPod == nil {
		// Debug: List all pods and their cliques
		logger.Errorf("Failed to find ready pod from workload1-0-pc-a. Available pods:")
		for _, pod := range pods.Items {
			clique := pod.Labels["grove.io/podclique"]
			logger.Errorf("  Pod %s: clique=%s, phase=%s, ready=%v", pod.Name, clique, pod.Status.Phase, utils.IsPodReady(&pod))
		}
		t.Fatalf("Failed to find a ready pod from PCS-owned podclique workload1-0-pc-a")
	}

	// Cordon the node where the target pod is running
	if err := cordonNode(tc, targetPod.Spec.NodeName); err != nil {
		t.Fatalf("Failed to cordon node %s: %v", targetPod.Spec.NodeName, err)
	}

	// Capture pod UIDs before gang termination
	originalPodUIDs := capturePodUIDs(pods)

	// Delete the target pod
	logger.Debugf("Deleting pod %s from node %s", targetPod.Name, targetPod.Spec.NodeName)
	if err := tc.Clientset.CoreV1().Pods(tc.Namespace).Delete(tc.Ctx, targetPod.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("Failed to delete pod %s: %v", targetPod.Name, err)
	}

	logger.Infof("5. Wait for TerminationDelay (%v) seconds", TerminationDelay)
	time.Sleep(TerminationDelay)

	logger.Info("6. Verify that all pods in the workload get gang-terminated and recreated")
	verifyGangTermination(tc, totalPods, originalPodUIDs)

	logger.Info("🎉 Gang-termination with full-replicas PCS-owned test (GT-1) completed successfully!")
}

// Test_GT2_GangTerminationFullReplicasPCSGOwned tests gang-termination behavior when a PCSG-owned PodClique is breached
// Scenario GT-2:
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Wait for pods to get scheduled and become ready
// 4. Cordon node and then delete 1 ready pod from PCSG-owned podclique pcs-0-sg-x-0-pc-c
// 5. Wait for TerminationDelay seconds
// 6. Verify that all pods in the workload get terminated
func Test_GT2_GangTerminationFullReplicasPCSGOwned(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster")
	totalPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, totalPods)
	defer cleanup()

	logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
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
			ExpectedPods: totalPods,
		},
	}

	pods, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Wait for pods to get scheduled and become ready")
	if err := waitForReadyPods(tc, totalPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	// Verify pods are distributed across distinct nodes
	listPodsAndAssertDistinctNodes(tc)

	logger.Info("4. Cordon node and then delete 1 ready pod from PCSG-owned podclique pcs-0-sg-x-0-pc-c")
	// Refresh pods list to get current ready status
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to refresh pod list: %v", err)
	}

	// Find a pod from workload1-0-sg-x-0-pc-c podclique (PCSG-owned)
	targetPod := findReadyPodFromPodClique(pods, "workload1-0-sg-x-0-pc-c")
	if targetPod == nil {
		// Debug: List all pods and their cliques
		logger.Errorf("Failed to find ready pod from workload1-0-sg-x-0-pc-c. Available pods:")
		for _, pod := range pods.Items {
			clique := pod.Labels["grove.io/podclique"]
			logger.Errorf("  Pod %s: clique=%s, phase=%s, ready=%v", pod.Name, clique, pod.Status.Phase, utils.IsPodReady(&pod))
		}
		t.Fatalf("Failed to find a ready pod from PCSG-owned podclique workload1-0-sg-x-0-pc-c")
	}

	// Cordon the node where the target pod is running
	if err := cordonNode(tc, targetPod.Spec.NodeName); err != nil {
		t.Fatalf("Failed to cordon node %s: %v", targetPod.Spec.NodeName, err)
	}

	// Capture pod UIDs before gang termination
	originalPodUIDs := capturePodUIDs(pods)

	// Delete the target pod
	logger.Debugf("Deleting pod %s from node %s", targetPod.Name, targetPod.Spec.NodeName)
	if err := tc.Clientset.CoreV1().Pods(tc.Namespace).Delete(tc.Ctx, targetPod.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("Failed to delete pod %s: %v", targetPod.Name, err)
	}

	logger.Infof("5. Wait for TerminationDelay (%v) seconds", TerminationDelay)
	time.Sleep(TerminationDelay)

	logger.Info("6. Verify that all pods in the workload get gang-terminated and recreated")
	verifyGangTermination(tc, totalPods, originalPodUIDs)

	logger.Info("🎉 Gang-termination with full-replicas PCSG-owned test (GT-2) completed successfully!")
}

// Test_GT3_GangTerminationMinReplicasPCSOwned tests gang-termination behavior with min-replicas when a PCS-owned PodClique is breached
// Scenario GT-3:
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Wait for pods to get scheduled and become ready
// 4. Cordon node and then delete 1 pod from PCS-owned podclique pcs-0-pc-a
// 5. Wait for TerminationDelay seconds
// 6. Verify that workload pods do not get gang-terminated
// 7. Cordon node and then delete 1 ready pod from PCS-owned podclique pcs-0-pc-a
// 8. Wait for TerminationDelay seconds
// 9. Verify that all pods in the workload get terminated
func Test_GT3_GangTerminationMinReplicasPCSOwned(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster")
	totalPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, totalPods)
	defer cleanup()

	logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
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
			ExpectedPods: totalPods,
		},
	}

	pods, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Wait for pods to get scheduled and become ready")
	if err := waitForReadyPods(tc, totalPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	// Verify pods are distributed across distinct nodes
	listPodsAndAssertDistinctNodes(tc)

	logger.Info("4. Cordon node and then delete 1 pod from PCS-owned podclique pcs-0-pc-a")
	// Find the first pod from workload2-0-pc-a podclique (PCS-owned)
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	firstTargetPod := findReadyPodFromPodClique(pods, "workload2-0-pc-a")
	if firstTargetPod == nil {
		t.Fatalf("Failed to find a ready pod from PCS-owned podclique workload2-0-pc-a")
	}

	// Cordon the node where the first target pod is running
	if err := cordonNode(tc, firstTargetPod.Spec.NodeName); err != nil {
		t.Fatalf("Failed to cordon node %s: %v", firstTargetPod.Spec.NodeName, err)
	}

	// Capture pod UIDs before deletion to verify they remain after (no gang termination)
	originalPodUIDs := capturePodUIDs(pods)

	// Delete the first target pod
	logger.Debugf("Deleting first pod %s from node %s", firstTargetPod.Name, firstTargetPod.Spec.NodeName)
	if err := tc.Clientset.CoreV1().Pods(tc.Namespace).Delete(tc.Ctx, firstTargetPod.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("Failed to delete pod %s: %v", firstTargetPod.Name, err)
	}

	logger.Infof("5. Wait for 2x TerminationDelay (%v) to ensure no gang-termination occurs", 2*TerminationDelay)
	time.Sleep(2 * TerminationDelay)

	logger.Info("6. Verify that workload pods do not get gang-terminated")
	// Verify at least 3 original pods remain (gang termination would replace all pod UIDs)
	verifyNoGangTermination(tc, 3, originalPodUIDs)

	logger.Info("7. Cordon node and then delete 1 ready pod from PCS-owned podclique pcs-0-pc-a")
	// Find another ready pod from workload2-0-pc-a
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	secondTargetPod := findReadyPodFromPodCliqueExcluding(pods, "workload2-0-pc-a", []string{firstTargetPod.Name})
	if secondTargetPod == nil {
		t.Fatalf("Failed to find a second ready pod from PCS-owned podclique workload2-0-pc-a")
	}

	// Cordon the node where the second target pod is running
	if err := cordonNode(tc, secondTargetPod.Spec.NodeName); err != nil {
		t.Fatalf("Failed to cordon node %s: %v", secondTargetPod.Spec.NodeName, err)
	}

	// Capture pod UIDs before gang termination (after first deletion)
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}
	podUIDsBeforeSecondDeletion := capturePodUIDs(pods)

	// Delete the second target pod
	logger.Debugf("Deleting second pod %s from node %s", secondTargetPod.Name, secondTargetPod.Spec.NodeName)
	if err := tc.Clientset.CoreV1().Pods(tc.Namespace).Delete(tc.Ctx, secondTargetPod.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("Failed to delete pod %s: %v", secondTargetPod.Name, err)
	}

	logger.Infof("8. Wait for TerminationDelay (%v) seconds", TerminationDelay)
	time.Sleep(TerminationDelay)

	logger.Info("9. Verify that all pods in the workload get gang-terminated and recreated")
	verifyGangTermination(tc, totalPods, podUIDsBeforeSecondDeletion)

	logger.Info("🎉 Gang-termination with min-replicas PCS-owned test (GT-3) completed successfully!")
}

// Test_GT4_GangTerminationMinReplicasPCSGOwned tests gang-termination behavior with min-replicas when a PCSG-owned PodClique is breached
// Scenario GT-4:
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Wait for pods to get scheduled and become ready
// 4. Cordon node and then delete 1 ready pod from PCSG-owned podclique pcs-0-sg-x-0-pc-c
// 5. Wait for TerminationDelay seconds
// 6. Verify that workload pods do not get gang-terminated
// 7. Cordon nodes and then delete 2 remaining ready pods from PCSG-owned podclique pcs-0-sg-x-0-pc-c
// 8. Wait for TerminationDelay seconds
// 9. Verify that both podcliques on PCSG pcs-0-sg-x-0 are recreated but workload is not gang-terminated
// 10. Cordon node and then delete 1 ready pod from PCSG-owned podclique pcs-0-sg-x-1-pc-c
// 11. Wait for TerminationDelay seconds
// 12. Verify that workload pods do not get gang-terminated
// 13. Cordon nodes and then delete 2 remaining ready pods from PCSG-owned podclique pcs-0-sg-x-1-pc-c
// 14. Wait for TerminationDelay seconds
// 15. Verify that all pods in the workload get terminated
func Test_GT4_GangTerminationMinReplicasPCSGOwned(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster")
	totalPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, totalPods)
	defer cleanup()

	logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
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
			ExpectedPods: totalPods,
		},
	}

	pods, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	logger.Info("3. Wait for pods to get scheduled and become ready")
	if err := waitForReadyPods(tc, totalPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	logger.Info("4. Cordon node and then delete 1 ready pod from PCSG-owned podclique pcs-0-sg-x-0-pc-c")
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	// Capture pod UIDs before deletion to verify they remain after (no gang termination)
	originalPodUIDs := capturePodUIDs(pods)

	// Find and delete first pod from workload2-0-sg-x-0-pc-c
	// (we intentionally delete Ready pods to emulate healthy replica loss)
	firstPodToDelete := findAndDeleteReadyPodFromPodClique(tc, pods, "workload2-0-sg-x-0-pc-c", "first")

	logger.Infof("5. Wait for 2x TerminationDelay (%v) to ensure no gang-termination occurs", 2*TerminationDelay)
	time.Sleep(2 * TerminationDelay)

	logger.Info("6. Verify that workload pods do not get gang-terminated")
	// Verify at least 3 original pods remain (gang termination would replace all pod UIDs)
	verifyNoGangTermination(tc, 3, originalPodUIDs)

	logger.Info("7. Cordon nodes and then delete 2 remaining ready pods from PCSG-owned podclique pcs-0-sg-x-0-pc-c")
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	// Find and delete remaining pods from workload2-0-sg-x-0-pc-c
	deletedPods := []string{firstPodToDelete}
	for i := 0; i < 2; i++ {
		// Continue removing Ready pods so the test covers gang termination-triggering scenarios
		podName := findAndDeleteReadyPodFromPodCliqueExcluding(tc, pods, "workload2-0-sg-x-0-pc-c", deletedPods, fmt.Sprintf("pod %d", i+2))
		deletedPods = append(deletedPods, podName)
		// Refresh pod list
		pods, err = listPods(tc)
		if err != nil {
			t.Fatalf("Failed to list workload pods: %v", err)
		}
	}

	logger.Infof("8. Wait for 2x TerminationDelay (%v) seconds", 2*TerminationDelay)
	time.Sleep(2 * TerminationDelay)

	logger.Info("9. Verify that both podcliques on PCSG pcs-0-sg-x-0 (pcs-0-sg-x-0-pc-b and pcs-0-sg-x-0-pc-c) are recreated but workload is not gang-terminated")
	// Verify at least 3 original pods remain (gang termination would replace all pod UIDs)
	verifyNoGangTermination(tc, 3, originalPodUIDs)

	// Wait for pods to be recreated
	time.Sleep(10 * time.Second)

	logger.Info("10. Cordon node and then delete 1 ready pod from PCSG-owned podclique pcs-0-sg-x-1-pc-c")
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	// Capture pod UIDs before deletion to verify they remain after (no gang termination)
	originalPodUIDsSgx1 := capturePodUIDs(pods)

	// Find and delete first pod from workload2-0-sg-x-1-pc-c (must be Ready to simulate a real healthy replica eviction)
	firstPodSgx1 := findAndDeleteReadyPodFromPodClique(tc, pods, "workload2-0-sg-x-1-pc-c", "first from sg-x-1")

	logger.Infof("11. Wait for 2x TerminationDelay (%v) to ensure no gang-termination occurs", 2*TerminationDelay)
	time.Sleep(2 * TerminationDelay)

	logger.Info("12. Verify that workload pods do not get gang-terminated")
	// Verify at least 3 original pods remain (gang termination would replace all pod UIDs)
	verifyNoGangTermination(tc, 3, originalPodUIDsSgx1)

	logger.Info("13. Cordon nodes and then delete 2 remaining ready pods from PCSG-owned podclique pcs-0-sg-x-1-pc-c")
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	// Find and delete remaining pods from workload2-0-sg-x-1-pc-c
	deletedPodsSgx1 := []string{firstPodSgx1}
	for i := 0; i < 2; i++ {
		// Same reasoning: delete Ready pods only, so we validate behavior after healthy losses
		podName := findAndDeleteReadyPodFromPodCliqueExcluding(tc, pods, "workload2-0-sg-x-1-pc-c", deletedPodsSgx1, fmt.Sprintf("pod %d from sg-x-1", i+2))
		deletedPodsSgx1 = append(deletedPodsSgx1, podName)
		// Refresh pod list
		pods, err = listPods(tc)
		if err != nil {
			t.Fatalf("Failed to list workload pods: %v", err)
		}
	}

	// Capture pod UIDs before final gang termination
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}
	podUIDsBeforeFinalDeletion := capturePodUIDs(pods)

	logger.Infof("14. Wait for TerminationDelay (%v) seconds", TerminationDelay)
	time.Sleep(TerminationDelay)

	logger.Info("15. Verify that all pods in the workload get gang-terminated and recreated")
	verifyGangTermination(tc, totalPods, podUIDsBeforeFinalDeletion)

	logger.Info("🎉 Gang-termination with min-replicas PCSG-owned test (GT-4) completed successfully!")
}

// Helper functions

// capturePodUIDs captures the UIDs of all pods in the list
func capturePodUIDs(pods *v1.PodList) map[string]string {
	uidMap := make(map[string]string)
	for _, pod := range pods.Items {
		uidMap[pod.Name] = string(pod.UID)
	}
	return uidMap
}

// findReadyPodFromPodClique finds a ready pod from the specified podclique.
// These helpers focus on Ready pods because the gang-termination tests must emulate
// the operator reacting to healthy replica loss, not pods that were already failing.
func findReadyPodFromPodClique(pods *v1.PodList, podCliqueName string) *v1.Pod {
	for i := range pods.Items {
		pod := &pods.Items[i]
		if podCliqueLabel, exists := pod.Labels["grove.io/podclique"]; exists && podCliqueLabel == podCliqueName {
			if utils.IsPodReady(pod) {
				return pod
			}
		}
	}
	return nil
}

// findReadyPodFromPodCliqueExcluding finds first ready pod from the specified podclique (excluding certain pods).
// Maintaining the Ready requirement keeps the test aligned with healthy-replica eviction scenarios.
func findReadyPodFromPodCliqueExcluding(pods *v1.PodList, podCliqueName string, excludePods []string) *v1.Pod {
	for i := range pods.Items {
		pod := &pods.Items[i]
		if podCliqueLabel, exists := pod.Labels["grove.io/podclique"]; exists && podCliqueLabel == podCliqueName {
			if pod.Status.Phase == v1.PodRunning && utils.IsPodReady(pod) {
				// Check if this pod should be excluded
				shouldExclude := false
				for _, excludeName := range excludePods {
					if pod.Name == excludeName {
						shouldExclude = true
						break
					}
				}
				if !shouldExclude {
					return pod
				}
			}
		}
	}
	return nil
}

// findAndDeleteReadyPodFromPodClique finds a ready pod from the specified podclique and deletes it.
// Ready-only deletions mimic losing healthy replicas, which is what should trigger gang termination logic.
func findAndDeleteReadyPodFromPodClique(tc TestContext, pods *v1.PodList, podCliqueName, description string) string {
	targetPod := findReadyPodFromPodClique(pods, podCliqueName)
	if targetPod == nil {
		tc.T.Fatalf("Failed to find %s ready pod from podclique %s", description, podCliqueName)
		return ""
	}

	// Cordon the node
	if err := cordonNode(tc, targetPod.Spec.NodeName); err != nil {
		tc.T.Fatalf("Failed to cordon node %s: %v", targetPod.Spec.NodeName, err)
	}

	// Delete the pod
	logger.Debugf("Deleting %s pod %s from node %s (podclique: %s)", description, targetPod.Name, targetPod.Spec.NodeName, podCliqueName)
	if err := tc.Clientset.CoreV1().Pods(tc.Namespace).Delete(tc.Ctx, targetPod.Name, metav1.DeleteOptions{}); err != nil {
		tc.T.Fatalf("Failed to delete pod %s: %v", targetPod.Name, err)
	}

	return targetPod.Name
}

// findAndDeleteReadyPodFromPodCliqueExcluding finds a ready pod from the specified podclique (excluding certain pods) and deletes it.
// This helper keeps the Ready constraint for the same reason—testing healthy replica loss semantics.
func findAndDeleteReadyPodFromPodCliqueExcluding(tc TestContext, pods *v1.PodList, podCliqueName string, excludePods []string, description string) string {
	targetPod := findReadyPodFromPodCliqueExcluding(pods, podCliqueName, excludePods)
	if targetPod == nil {
		tc.T.Fatalf("Failed to find %s ready pod from podclique %s", description, podCliqueName)
		return ""
	}

	// Cordon the node
	if err := cordonNode(tc, targetPod.Spec.NodeName); err != nil {
		tc.T.Fatalf("Failed to cordon node %s: %v", targetPod.Spec.NodeName, err)
	}

	// Delete the pod
	logger.Debugf("Deleting %s pod %s from node %s (podclique: %s)", description, targetPod.Name, targetPod.Spec.NodeName, podCliqueName)
	if err := tc.Clientset.CoreV1().Pods(tc.Namespace).Delete(tc.Ctx, targetPod.Name, metav1.DeleteOptions{}); err != nil {
		tc.T.Fatalf("Failed to delete pod %s: %v", targetPod.Name, err)
	}

	return targetPod.Name
}

// verifyNoGangTermination verifies that gang-termination has not occurred by checking that original pod UIDs still exist
func verifyNoGangTermination(tc TestContext, minExpectedRunning int, originalPodUIDs map[string]string) {
	tc.T.Helper()

	pollCount := 0
	err := pollForCondition(tc, func() (bool, error) {
		pollCount++
		pods, err := tc.Clientset.CoreV1().Pods(tc.Namespace).List(tc.Ctx, metav1.ListOptions{
			LabelSelector: tc.getLabelSelector(),
		})
		if err != nil {
			return false, err
		}

		runningCount := 0
		pendingCount := 0
		terminatingCount := 0
		currentUIDs := make(map[string]bool)
		oldPodsRemaining := 0

		for _, pod := range pods.Items {
			currentUIDs[string(pod.UID)] = true

			switch pod.Status.Phase {
			case v1.PodRunning:
				runningCount++
			case v1.PodPending:
				pendingCount++
			}
			if pod.DeletionTimestamp != nil {
				terminatingCount++
			}
		}

		// Check that original UIDs still exist (gang termination would replace all UIDs)
		for _, originalUID := range originalPodUIDs {
			if currentUIDs[originalUID] {
				oldPodsRemaining++
			}
		}

		runningOrPendingCount := runningCount + pendingCount
		// Success criteria:
		// 1. At least minExpectedRunning pods are running/pending
		// 2. Most original pods still exist (allowing for the few we intentionally deleted)
		success := runningOrPendingCount >= minExpectedRunning && oldPodsRemaining >= minExpectedRunning
		status := "✅"
		if !success {
			status = "❌"
		}
		logger.Debugf("%s [Poll %d] running=%d, pending=%d, terminating=%d, total=%d, original_remain=%d/%d (min_expected=%d)",
			status, pollCount, runningCount, pendingCount, terminatingCount, len(pods.Items), oldPodsRemaining, len(originalPodUIDs), minExpectedRunning)

		return success, nil
	})

	if err != nil {
		tc.T.Fatalf("Failed to verify no gang-termination: %v", err)
	}
}

// verifyGangTermination verifies that gang-termination has occurred and all pods were recreated
func verifyGangTermination(tc TestContext, expectedPods int, originalPodUIDs map[string]string) {
	tc.T.Helper()

	// After gang-termination, pods should be recreated with new UIDs
	// They don't need to all be Pending - they may have already started scheduling
	pollCount := 0
	err := pollForCondition(tc, func() (bool, error) {
		pollCount++
		pods, err := tc.Clientset.CoreV1().Pods(tc.Namespace).List(tc.Ctx, metav1.ListOptions{
			LabelSelector: tc.getLabelSelector(),
		})
		if err != nil {
			return false, err
		}

		// Verify none of the original pod UIDs exist (all were deleted and recreated)
		currentUIDs := make(map[string]bool)
		pendingCount := 0
		runningCount := 0
		terminatingCount := 0
		oldPodsRemaining := 0
		nonTerminatingCount := 0

		for _, pod := range pods.Items {
			currentUIDs[string(pod.UID)] = true

			// Don't count terminating pods
			if pod.DeletionTimestamp == nil {
				nonTerminatingCount++
			}

			switch pod.Status.Phase {
			case v1.PodPending:
				pendingCount++
			case v1.PodRunning:
				runningCount++
			}
			if pod.DeletionTimestamp != nil {
				terminatingCount++
			}
		}

		// Check that no original UIDs exist in current pods (all recreated)
		for _, originalUID := range originalPodUIDs {
			if currentUIDs[originalUID] {
				oldPodsRemaining++
			}
		}

		// Success criteria:
		// 1. We have the expected number of non-terminating pods
		// 2. None of the old pod UIDs remain (all were recreated)
		success := nonTerminatingCount == expectedPods && oldPodsRemaining == 0
		status := "✅"
		if !success {
			status = "❌"
		}
		logger.Debugf("%s [Poll %d] total=%d, non-terminating=%d/%d, pending=%d, running=%d, terminating=%d, old=%d",
			status, pollCount, len(pods.Items), nonTerminatingCount, expectedPods, pendingCount, runningCount, terminatingCount, oldPodsRemaining)

		return success, nil
	})

	if err != nil {
		// Add detailed diagnostics on failure
		pods, listErr := utils.ListPods(tc.Ctx, tc.Clientset, tc.Namespace, tc.getLabelSelector())
		if listErr == nil {
			logger.Errorf("Gang-termination verification failed. Current state: total_pods=%d, expected=%d", len(pods.Items), expectedPods)

			// Count old UIDs still present
			oldUIDs := 0
			for _, pod := range pods.Items {
				if _, exists := originalPodUIDs[pod.Name]; exists && originalPodUIDs[pod.Name] == string(pod.UID) {
					oldUIDs++
					logger.Debugf("  OLD Pod %s: phase=%s, uid=%s (terminating=%v)", pod.Name, pod.Status.Phase, pod.UID, pod.DeletionTimestamp != nil)
				} else {
					logger.Debugf("  NEW Pod %s: phase=%s, uid=%s (terminating=%v)", pod.Name, pod.Status.Phase, pod.UID, pod.DeletionTimestamp != nil)
				}
			}
			logger.Errorf("Old pods remaining: %d/%d", oldUIDs, len(originalPodUIDs))
		}
		tc.T.Fatalf("Failed to verify gang-termination and recreation: %v", err)
	}
}

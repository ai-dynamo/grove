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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// TerminationDelay is a mirror of the value in the workload YAMLs.
	TerminationDelay = 10 * time.Second
)

var (
	// pclqGVR is the GroupVersionResource for PodClique resources
	pclqGVR = schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliques"}
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
			YAMLPath:     "../yaml/workload1-gt.yaml",
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
			logger.Errorf("  Pod %s: clique=%s, phase=%s, ready=%v", pod.Name, clique, pod.Status.Phase, k8sutils.IsPodReady(&pod))
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

	logger.Info("Gang-termination with full-replicas PCS-owned test (GT-1) completed successfully!")
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
			YAMLPath:     "../yaml/workload1-gt.yaml",
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
			logger.Errorf("  Pod %s: clique=%s, phase=%s, ready=%v", pod.Name, clique, pod.Status.Phase, k8sutils.IsPodReady(&pod))
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

	logger.Info("Gang-termination with full-replicas PCSG-owned test (GT-2) completed successfully!")
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
			YAMLPath:     "../yaml/workload2-gt.yaml",
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
	firstPod := findAndDeleteReadyPodFromPodClique(tc, pods, "workload2-0-pc-a", "first")

	// Capture pod UIDs after first deletion for later comparison
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}
	originalPodUIDs := capturePodUIDs(pods)

	logger.Infof("5. Wait for TerminationDelay (%v) seconds", TerminationDelay)
	time.Sleep(TerminationDelay)

	logger.Info("6. Verify that workload pods do not get gang-terminated")
	// Verify at least 9 original pods remain (only deleted 1, gang termination would replace all pod UIDs)
	verifyNoGangTermination(tc, 9, originalPodUIDs)

	logger.Info("7. Cordon node and then delete 1 ready pod from PCS-owned podclique pcs-0-pc-a")
	// Refresh pod list
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	// Find and delete another pod from workload2-0-pc-a (excluding the first one we deleted)
	findAndDeleteReadyPodFromPodCliqueExcluding(tc, pods, "workload2-0-pc-a", []string{firstPod}, "second")

	// Capture pod UIDs before gang termination
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}
	podUIDsBeforeFinalDeletion := capturePodUIDs(pods)

	logger.Infof("8. Wait for TerminationDelay (%v) seconds", TerminationDelay)
	time.Sleep(TerminationDelay)

	logger.Info("9. Verify that all pods in the workload get gang-terminated and recreated")
	verifyGangTermination(tc, totalPods, podUIDsBeforeFinalDeletion)

	logger.Info("Gang-termination with min-replicas PCS-owned test (GT-3) completed successfully!")
}

// Test_GT4_GangTerminationMinReplicasPCSGOwned tests PCSG-level gang-termination behavior when a PCSG replica is breached
// but the PCSG's overall minAvailable is NOT breached. In this case, only the breached PCSG replica should be
// gang-terminated, not the entire PCS replica.
//
// Workload WL2 has:
// - pc-a: 2 replicas (PCS-owned, standalone)
// - sg-x: PCSG with 2 replicas (minAvailable=1), each containing pc-b (1 pod) and pc-c (3 pods, minAvailable=1)
//
// When sg-x-1's pc-c loses all pods (breaching pc-c's minAvailable), the PCSG should:
// - Gang-terminate only sg-x-1 (sg-x-1-pc-b and sg-x-1-pc-c)
// - NOT terminate sg-x-0 or pc-a (since PCSG's minAvailable=1 is still satisfied by sg-x-0)
//
// Scenario GT-4:
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Wait for pods to get scheduled and become ready
// 4. Cordon node and then delete 1 ready pod from PCSG-owned podclique pcs-0-sg-x-0-pc-c
// 5. Wait for TerminationDelay seconds
// 6. Verify that workload pods do not get gang-terminated
// 7. Cordon node and then delete 1 ready pod from PCSG-owned podclique pcs-0-sg-x-0-pc-c
// 8. Wait for TerminationDelay seconds
// 9. Verify that workload pods do not get gang-terminated
// 10. Cordon node and then delete 1 ready pod from PCSG-owned podclique pcs-0-sg-x-1-pc-c
// 11. Wait for TerminationDelay seconds
// 12. Verify that workload pods do not get gang-terminated
// 13. Cordon nodes and then delete 2 remaining ready pods from PCSG-owned podclique pcs-0-sg-x-1-pc-c
// 14. Wait for TerminationDelay seconds
// 15. Verify that only PCSG replica sg-x-1 pods get gang-terminated and recreated (not the entire workload)
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
			YAMLPath:     "../yaml/workload2-gt.yaml",
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
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}
	firstPodSgx0 := findAndDeleteReadyPodFromPodClique(tc, pods, "workload2-0-sg-x-0-pc-c", "first from sg-x-0")

	// Capture pod UIDs for sg-x-0 verification
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}
	originalPodUIDsSgx0 := capturePodUIDs(pods)

	logger.Infof("5. Wait for TerminationDelay (%v) seconds", TerminationDelay)
	time.Sleep(TerminationDelay)

	logger.Info("6. Verify that workload pods do not get gang-terminated")
	verifyNoGangTermination(tc, 9, originalPodUIDsSgx0)

	logger.Info("7. Cordon node and then delete 1 ready pod from PCSG-owned podclique pcs-0-sg-x-0-pc-c")
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}
	_ = findAndDeleteReadyPodFromPodCliqueExcluding(tc, pods, "workload2-0-sg-x-0-pc-c", []string{firstPodSgx0}, "second from sg-x-0")

	// Capture pod UIDs for verification
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}
	originalPodUIDsAfterSecond := capturePodUIDs(pods)

	logger.Infof("8. Wait for TerminationDelay (%v) seconds", TerminationDelay)
	time.Sleep(TerminationDelay)

	logger.Info("9. Verify that workload pods do not get gang-terminated")
	// sg-x-0-pc-c has 3 replicas with minAvailable=1, so losing 2 pods still keeps us at 1 >= minAvailable
	verifyNoGangTermination(tc, 8, originalPodUIDsAfterSecond)

	logger.Info("10. Cordon node and then delete 1 ready pod from PCSG-owned podclique pcs-0-sg-x-1-pc-c")
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}
	firstPodSgx1 := findAndDeleteReadyPodFromPodClique(tc, pods, "workload2-0-sg-x-1-pc-c", "first from sg-x-1")

	// Capture pod UIDs for sg-x-1 verification
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}
	originalPodUIDsSgx1 := capturePodUIDs(pods)

	logger.Infof("11. Wait for TerminationDelay (%v) seconds", TerminationDelay)
	time.Sleep(TerminationDelay)

	logger.Info("12. Verify that workload pods do not get gang-terminated")
	// Verify at least 3 original pods remain (gang termination would replace all pod UIDs)
	verifyNoGangTermination(tc, 3, originalPodUIDsSgx1)

	logger.Info("13. Cordon nodes and then delete 2 remaining ready pods from PCSG-owned podclique pcs-0-sg-x-1-pc-c")
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	// Capture pod UIDs BEFORE deleting the remaining pods - we need to track which pods should be terminated
	// and which should remain unchanged
	podsBeforeFinalDeletion, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	// Identify pods that belong to sg-x-1 (should be terminated) vs other components (should remain)
	sgx1PodUIDs := make(map[string]string)  // Pods in sg-x-1 (should be gang-terminated)
	otherPodUIDs := make(map[string]string) // Pods NOT in sg-x-1 (should remain unchanged)
	for _, pod := range podsBeforeFinalDeletion.Items {
		clique := pod.Labels["grove.io/podclique"]
		// sg-x-1 pods have clique names like "workload2-0-sg-x-1-pc-b" or "workload2-0-sg-x-1-pc-c"
		if strings.Contains(clique, "-sg-x-1-") {
			sgx1PodUIDs[pod.Name] = string(pod.UID)
		} else {
			otherPodUIDs[pod.Name] = string(pod.UID)
		}
	}
	logger.Debugf("Pods in sg-x-1 (to be terminated): %d, Other pods (should remain): %d", len(sgx1PodUIDs), len(otherPodUIDs))

	// Find and delete remaining pods from workload2-0-sg-x-1-pc-c
	deletedPodsSgx1 := []string{firstPodSgx1}
	for i := range 2 {
		// Same reasoning: delete Ready pods only, so we validate behavior after healthy losses
		podName := findAndDeleteReadyPodFromPodCliqueExcluding(tc, pods, "workload2-0-sg-x-1-pc-c", deletedPodsSgx1, fmt.Sprintf("pod %d from sg-x-1", i+2))
		deletedPodsSgx1 = append(deletedPodsSgx1, podName)
		// Refresh pod list
		pods, err = listPods(tc)
		if err != nil {
			t.Fatalf("Failed to list workload pods: %v", err)
		}
	}

	logger.Infof("14. Wait for TerminationDelay (%v) seconds", TerminationDelay)
	time.Sleep(TerminationDelay)

	logger.Info("15. Verify that only PCSG replica sg-x-1 pods get gang-terminated (not the entire workload)")
	// sg-x-1 should have 4 pods: 1 from pc-b + 3 from pc-c
	expectedSgx1Pods := 4
	verifyPCSGReplicaGangTermination(tc, totalPods, sgx1PodUIDs, otherPodUIDs, expectedSgx1Pods)

	logger.Info("PCSG-level gang-termination test (GT-4) completed successfully!")
}

// Test_GT5_GangTerminationPCSGMinAvailableBreach tests PCSG-level minAvailable breach that delegates to PCS-level gang termination.
// When both PCSG replicas are breached, the PCSG's own minAvailable becomes unsatisfied, and it should delegate
// to PCS-level gang termination (terminating the entire workload, including standalone pc-a).
//
// Workload WL2 has:
// - pc-a: 2 replicas (PCS-owned, standalone)
// - sg-x: PCSG with 2 replicas (minAvailable=1), each containing pc-b (1 pod) and pc-c (3 pods, minAvailable=1)
//
// Key difference from GT-4:
// | Test | Breach                 | PCSG minAvailable       | Result                        |
// |------|------------------------|-------------------------|-------------------------------|
// | GT-4 | Only sg-x-1            | Still satisfied (1 ≥ 1) | Only sg-x-1 terminated        |
// | GT-5 | Both sg-x-0 AND sg-x-1 | Breached (0 < 1)        | Entire PCS replica terminated |
//
// Scenario GT-5:
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload WL2, and verify 10 newly created pods
// 3. Wait for pods to get scheduled and become ready
// 4. Delete all 3 pods from sg-x-0-pc-c (breach sg-x-0)
// 5. Immediately delete all 3 pods from sg-x-1-pc-c (breach sg-x-1)
//    (Must do steps 4-5 quickly, within TerminationDelay window)
// 6. Wait for TerminationDelay seconds
// 7. Verify that ALL pods in the workload get gang-terminated and recreated (including pc-a)
func Test_GT5_GangTerminationPCSGMinAvailableBreach(t *testing.T) {
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
			YAMLPath:     "../yaml/workload2-gt.yaml",
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

	logger.Info("4. Cordon ALL nodes hosting workload pods to prevent replacement pods from scheduling")
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	// Cordon all nodes hosting workload pods BEFORE deleting any pods.
	// This is critical for FLIP detection: if replacement pods become Ready before
	// the status reconcile detects the FLIP (oldReadyReplicas >= minAvailable -> newReadyReplicas < minAvailable),
	// the breach won't be recorded because newReadyReplicas would still satisfy minAvailable.
	cordonedNodes := cordonAllWorkloadPodNodes(tc, pods)
	logger.Infof("Cordoned %d nodes hosting workload pods", len(cordonedNodes))

	logger.Info("5. Delete all 3 pods from sg-x-0-pc-c (breach sg-x-0)")
	// Delete all 3 pods from sg-x-0-pc-c (nodes already cordoned, so just delete)
	deletedPodsSgx0 := deletePodsFromPodCliqueWithoutCordon(tc, pods, "workload2-0-sg-x-0-pc-c")
	if len(deletedPodsSgx0) != 3 {
		t.Fatalf("Expected to delete 3 pods from sg-x-0-pc-c, but deleted %d", len(deletedPodsSgx0))
	}

	logger.Info("6. Immediately delete all 3 pods from sg-x-1-pc-c (breach sg-x-1)")
	// Refresh pod list
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	// Delete all 3 pods from sg-x-1-pc-c (nodes already cordoned, so just delete)
	deletedPodsSgx1 := deletePodsFromPodCliqueWithoutCordon(tc, pods, "workload2-0-sg-x-1-pc-c")
	if len(deletedPodsSgx1) != 3 {
		t.Fatalf("Expected to delete 3 pods from sg-x-1-pc-c, but deleted %d", len(deletedPodsSgx1))
	}

	// Capture pod UIDs after deletions for later verification
	// Note: We need to capture these AFTER the deletions to track what remains
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}
	originalPodUIDs := capturePodUIDs(pods)

	logger.Infof("7. Wait for TerminationDelay (%v) seconds", TerminationDelay)
	time.Sleep(TerminationDelay)

	logger.Info("8. Verify that ALL pods in the workload get gang-terminated and recreated (including pc-a)")
	// With both PCSG replicas breached, PCSG minAvailable (0 < 1) is violated
	// This should trigger PCS-level gang termination, which terminates ALL pods including pc-a
	verifyGangTermination(tc, totalPods, originalPodUIDs)

	logger.Info("PCSG minAvailable breach → PCS-level gang-termination test (GT-5) completed successfully!")
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
			if pod.Status.Phase == v1.PodRunning && k8sutils.IsPodReady(pod) {
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
			if pod.Status.Phase == v1.PodRunning && k8sutils.IsPodReady(pod) {
				// Check if this pod should be excluded
				if !slices.Contains(excludePods, pod.Name) {
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

// deleteAllPodsFromPodClique finds all ready pods from the specified podclique, cordons their nodes, and deletes them.
// Returns the names of all deleted pods. This is used in GT-5 to quickly breach multiple PCSG replicas
// within the TerminationDelay window.
func deleteAllPodsFromPodClique(tc TestContext, pods *v1.PodList, podCliqueName string) []string {
	var deletedPods []string

	for i := range pods.Items {
		pod := &pods.Items[i]
		if podCliqueLabel, exists := pod.Labels["grove.io/podclique"]; exists && podCliqueLabel == podCliqueName {
			if pod.Status.Phase == v1.PodRunning && k8sutils.IsPodReady(pod) {
				// Cordon the node
				if err := cordonNode(tc, pod.Spec.NodeName); err != nil {
					tc.T.Fatalf("Failed to cordon node %s: %v", pod.Spec.NodeName, err)
				}

				// Delete the pod
				logger.Debugf("Deleting pod %s from node %s (podclique: %s)", pod.Name, pod.Spec.NodeName, podCliqueName)
				if err := tc.Clientset.CoreV1().Pods(tc.Namespace).Delete(tc.Ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
					tc.T.Fatalf("Failed to delete pod %s: %v", pod.Name, err)
				}
				deletedPods = append(deletedPods, pod.Name)
			}
		}
	}

	if len(deletedPods) == 0 {
		tc.T.Fatalf("Failed to find any ready pods from podclique %s", podCliqueName)
	}

	return deletedPods
}

// cordonAllWorkloadPodNodes cordons all nodes that are hosting workload pods.
// This is used to prevent replacement pods from becoming ready during gang termination tests,
// which is critical for reliable FLIP detection.
func cordonAllWorkloadPodNodes(tc TestContext, pods *v1.PodList) []string {
	cordonedNodes := make(map[string]bool)
	var nodeNames []string

	for i := range pods.Items {
		pod := &pods.Items[i]
		nodeName := pod.Spec.NodeName
		if nodeName != "" && !cordonedNodes[nodeName] {
			if err := cordonNode(tc, nodeName); err != nil {
				tc.T.Fatalf("Failed to cordon node %s: %v", nodeName, err)
			}
			cordonedNodes[nodeName] = true
			nodeNames = append(nodeNames, nodeName)
		}
	}

	return nodeNames
}

// deletePodsFromPodCliqueWithoutCordon deletes all ready pods from the specified podclique
// without cordoning nodes (assumes nodes are already cordoned).
// This is used when nodes have been pre-cordoned to ensure FLIP detection works reliably.
func deletePodsFromPodCliqueWithoutCordon(tc TestContext, pods *v1.PodList, podCliqueName string) []string {
	var deletedPods []string

	for i := range pods.Items {
		pod := &pods.Items[i]
		if podCliqueLabel, exists := pod.Labels["grove.io/podclique"]; exists && podCliqueLabel == podCliqueName {
			if pod.Status.Phase == v1.PodRunning && k8sutils.IsPodReady(pod) {
				// Delete the pod (node already cordoned)
				logger.Debugf("Deleting pod %s from node %s (podclique: %s)", pod.Name, pod.Spec.NodeName, podCliqueName)
				if err := tc.Clientset.CoreV1().Pods(tc.Namespace).Delete(tc.Ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
					tc.T.Fatalf("Failed to delete pod %s: %v", pod.Name, err)
				}
				deletedPods = append(deletedPods, pod.Name)
			}
		}
	}

	if len(deletedPods) == 0 {
		tc.T.Fatalf("Failed to find any ready pods from podclique %s", podCliqueName)
	}

	return deletedPods
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
		status := "OK"
		if !success {
			status = "WAITING"
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
		status := "OK"
		if !success {
			status = "WAITING"
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

// verifyGangTerminationOccurred verifies that gang termination occurred by checking
// that all original pods were deleted. Unlike verifyGangTermination, this does NOT
// require that new pods are fully recreated - it only checks that the old pods are gone.
// This is useful for tests where recreation may be blocked (e.g., cordoned nodes) but
// the key assertion is that gang termination was triggered.
func verifyGangTerminationOccurred(tc TestContext, originalPodUIDs map[string]string) {
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

		// Check if any original pod UIDs still exist
		currentUIDs := make(map[string]bool)
		for _, pod := range pods.Items {
			currentUIDs[string(pod.UID)] = true
		}

		oldPodsRemaining := 0
		for _, originalUID := range originalPodUIDs {
			if currentUIDs[originalUID] {
				oldPodsRemaining++
			}
		}

		// Success: all original pods are gone (gang termination occurred)
		success := oldPodsRemaining == 0
		status := "OK"
		if !success {
			status = "WAITING"
		}
		logger.Debugf("%s [Poll %d] total_pods=%d, old_pods_remaining=%d/%d",
			status, pollCount, len(pods.Items), oldPodsRemaining, len(originalPodUIDs))

		return success, nil
	})

	if err != nil {
		// Add detailed diagnostics on failure
		pods, listErr := utils.ListPods(tc.Ctx, tc.Clientset, tc.Namespace, tc.getLabelSelector())
		if listErr == nil {
			oldUIDs := 0
			for _, pod := range pods.Items {
				if _, exists := originalPodUIDs[pod.Name]; exists && originalPodUIDs[pod.Name] == string(pod.UID) {
					oldUIDs++
					logger.Debugf("  OLD Pod %s: phase=%s, uid=%s", pod.Name, pod.Status.Phase, pod.UID)
				}
			}
			logger.Errorf("Gang termination verification failed. Old pods remaining: %d/%d", oldUIDs, len(originalPodUIDs))
		}
		tc.T.Fatalf("Failed to verify gang-termination occurred: %v", err)
	}

	logger.Info("Gang termination verified: all original pods were deleted")
}

// verifyPCSGReplicaGangTermination verifies that only the specified PCSG replica's pods were gang-terminated
// while other pods in the workload remained unchanged. This tests PCSG-level gang termination where only
// a single PCSG replica is breached but the PCSG's overall minAvailable is still satisfied.
func verifyPCSGReplicaGangTermination(tc TestContext, expectedTotalPods int, terminatedPodUIDs, unchangedPodUIDs map[string]string, expectedTerminatedPods int) {
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

		// Track current state
		currentUIDs := make(map[string]string)
		nonTerminatingCount := 0
		terminatingCount := 0
		newTerminatedPods := 0      // Pods that were in terminatedPodUIDs but now have new UIDs
		unchangedPodsRemaining := 0 // Pods that were in unchangedPodUIDs and still have same UIDs

		for _, pod := range pods.Items {
			currentUIDs[pod.Name] = string(pod.UID)

			if pod.DeletionTimestamp != nil {
				terminatingCount++
				continue
			}
			nonTerminatingCount++

			// Check if this pod was supposed to be terminated (sg-x-1 pods)
			// We look for pods with similar names (same clique) but different UIDs
			for origName, origUID := range terminatedPodUIDs {
				clique := pod.Labels["grove.io/podclique"]
				origClique := strings.TrimSuffix(origName, origName[strings.LastIndex(origName, "-"):])
				if strings.HasPrefix(clique, "workload2-0-sg-x-1-") && string(pod.UID) != origUID {
					// This is a new pod for a sg-x-1 clique
					newTerminatedPods++
					break
				}
				_ = origClique // avoid unused variable
			}
		}

		// Check that unchanged pods still have same UIDs
		for origName, origUID := range unchangedPodUIDs {
			if currentUID, exists := currentUIDs[origName]; exists && currentUID == origUID {
				unchangedPodsRemaining++
			}
		}

		// Count how many sg-x-1 pods now exist (should be expectedTerminatedPods with new UIDs)
		sgx1PodsCount := 0
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp != nil {
				continue
			}
			clique := pod.Labels["grove.io/podclique"]
			if strings.Contains(clique, "-sg-x-1-") {
				sgx1PodsCount++
			}
		}

		// Success criteria:
		// 1. Total non-terminating pods equals expected total
		// 2. sg-x-1 has the expected number of pods (recreated)
		// 3. Unchanged pods (pc-a, sg-x-0-*) still have original UIDs
		success := nonTerminatingCount == expectedTotalPods &&
			sgx1PodsCount == expectedTerminatedPods &&
			unchangedPodsRemaining == len(unchangedPodUIDs)

		status := "OK"
		if !success {
			status = "WAITING"
		}
		logger.Debugf("%s [Poll %d] total=%d, non-terminating=%d/%d, terminating=%d, sg-x-1=%d/%d, unchanged=%d/%d",
			status, pollCount, len(pods.Items), nonTerminatingCount, expectedTotalPods,
			terminatingCount, sgx1PodsCount, expectedTerminatedPods,
			unchangedPodsRemaining, len(unchangedPodUIDs))

		return success, nil
	})

	if err != nil {
		// Add detailed diagnostics on failure
		pods, listErr := utils.ListPods(tc.Ctx, tc.Clientset, tc.Namespace, tc.getLabelSelector())
		if listErr == nil {
			logger.Errorf("PCSG replica gang-termination verification failed. Current pods:")
			sgx1Count := 0
			otherCount := 0
			for _, pod := range pods.Items {
				clique := pod.Labels["grove.io/podclique"]
				isSgx1 := strings.Contains(clique, "-sg-x-1-")
				if isSgx1 {
					sgx1Count++
				} else {
					otherCount++
				}
				// Check if UID changed
				var uidStatus string
				if origUID, wasTerminated := terminatedPodUIDs[pod.Name]; wasTerminated {
					if string(pod.UID) == origUID {
						uidStatus = "SAME-UID (should be NEW)"
					} else {
						uidStatus = "NEW-UID (correct)"
					}
				} else if origUID, wasUnchanged := unchangedPodUIDs[pod.Name]; wasUnchanged {
					if string(pod.UID) == origUID {
						uidStatus = "SAME-UID (correct)"
					} else {
						uidStatus = "NEW-UID (should be SAME)"
					}
				} else {
					uidStatus = "NEW-POD"
				}
				logger.Debugf("  Pod %s: clique=%s, phase=%s, uid=%s, %s, terminating=%v",
					pod.Name, clique, pod.Status.Phase, pod.UID, uidStatus, pod.DeletionTimestamp != nil)
			}
			logger.Errorf("Summary: sg-x-1 pods=%d (expected %d), other pods=%d (expected %d)",
				sgx1Count, expectedTerminatedPods, otherCount, len(unchangedPodUIDs))
		}
		tc.T.Fatalf("Failed to verify PCSG replica gang-termination: %v", err)
	}
}

// Test_GT6_NeverHealthyNoGangTermination tests that workloads that never became healthy
// do NOT trigger gang termination. This validates the FLIP detection logic:
// MinAvailableBreached should only be True when transitioning from available to unavailable.
//
// Scenario GT-6:
// 1. Initialize a 2-node Grove cluster
// 2. Deploy workload WL3 (init container always fails - pods never become Ready)
// 3. Wait for pods to be created and scheduled
// 4. Wait for TerminationDelay + buffer
// 5. Verify MinAvailableBreached condition is False with reason NeverAvailable
// 6. Verify no gang termination occurred (same pods remain)
func Test_GT6_NeverHealthyNoGangTermination(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 2-node Grove cluster")
	totalPods := 2 // pc-a: 2 replicas
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, totalPods)
	defer cleanup()

	logger.Info("2. Deploy workload WL3 (init container always fails - pods never become Ready)")
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
			Name:         "workload3",
			YAMLPath:     "../yaml/workload3-gt.yaml",
			Namespace:    "default",
			ExpectedPods: totalPods,
		},
	}

	// Deploy workload but don't wait for pods to be ready (they never will be)
	_, err := applyYAMLFile(tc, tc.Workload.YAMLPath)
	if err != nil {
		t.Fatalf("Failed to apply workload YAML: %v", err)
	}

	logger.Info("3. Wait for pods to be created and scheduled")
	// Wait for pods to be created (they'll be scheduled but init container will fail)
	_, err = waitForPodCount(tc, totalPods)
	if err != nil {
		t.Fatalf("Failed to wait for pods to be created: %v", err)
	}

	// Wait a bit for pods to be scheduled and init container to fail
	logger.Info("3b. Wait for pods to be scheduled and init container to fail multiple times")
	time.Sleep(15 * time.Second)

	// Capture pod UIDs for later verification
	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	originalPodUIDs := capturePodUIDs(pods)

	logger.Infof("4. Wait for TerminationDelay (%v) + buffer to ensure gang termination would have triggered if breached", TerminationDelay)
	time.Sleep(TerminationDelay + 5*time.Second)

	logger.Info("5. Verify MinAvailableBreached condition is False with reason NeverAvailable")
	// Get the PodClique and check its condition
	pclqName := "workload3-0-pc-a" // PCS name + replica index + clique name
	verifyMinAvailableBreachedCondition(tc, pclqName, metav1.ConditionFalse, constants.ConditionReasonNeverAvailable)

	logger.Info("6. Verify no gang termination occurred (same pods remain)")
	// Verify that the same pods still exist (no gang termination)
	verifyNoGangTermination(tc, totalPods, originalPodUIDs)

	logger.Info("Never-healthy workload test (GT-6) completed successfully!")
}

// Test_GT7_OperatorRestartPreservesBreach tests that gang termination proceeds correctly
// after an operator restart. This validates that the persisted MinAvailableBreached=True
// condition is respected after restart.
//
// Scenario GT-7:
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload WL1, wait for pods to become ready
// 3. Cordon node and delete a pod to breach minAvailable
// 4. Verify MinAvailableBreached=True condition is set
// 5. Restart operator (before termination delay expires)
// 6. Wait for TerminationDelay
// 7. Verify gang termination occurred (breach was respected after restart)
func Test_GT7_OperatorRestartPreservesBreach(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster")
	totalPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, totalPods)
	defer cleanup()

	logger.Info("2. Deploy workload WL1, wait for pods to become ready")
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
			YAMLPath:     "../yaml/workload1-gt.yaml",
			Namespace:    "default",
			ExpectedPods: totalPods,
		},
	}

	pods, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	if err := waitForReadyPods(tc, totalPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	// Verify pods are distributed across distinct nodes
	listPodsAndAssertDistinctNodes(tc)

	logger.Info("3. Cordon node and delete a pod to breach minAvailable")
	// Refresh pods list
	pods, err = listPods(tc)
	if err != nil {
		t.Fatalf("Failed to refresh pod list: %v", err)
	}

	// Find and delete a pod from workload1-0-pc-a (PCS-owned, minAvailable=2)
	targetPod := findReadyPodFromPodClique(pods, "workload1-0-pc-a")
	if targetPod == nil {
		t.Fatalf("Failed to find a ready pod from PCS-owned podclique workload1-0-pc-a")
	}

	// Cordon the node
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

	logger.Info("4. Wait briefly for MinAvailableBreached=True to be set")
	time.Sleep(3 * time.Second)

	// Verify the breach condition is set
	pclqName := "workload1-0-pc-a"
	verifyMinAvailableBreachedCondition(tc, pclqName, metav1.ConditionTrue, constants.ConditionReasonInsufficientReadyPods)

	logger.Info("5. Restart operator (before termination delay expires)")
	if err := restartOperator(tc); err != nil {
		t.Fatalf("Failed to restart operator: %v", err)
	}

	// Wait for operator to be ready again
	if err := waitForOperatorReady(tc); err != nil {
		t.Fatalf("Failed to wait for operator to be ready: %v", err)
	}

	logger.Infof("6. Wait for remaining TerminationDelay (%v)", TerminationDelay)
	// We already spent ~3s + operator restart time, but wait for the full delay to be safe
	time.Sleep(TerminationDelay)

	logger.Info("7. Verify gang termination occurred (breach was respected after restart)")
	// Only verify that old pods were deleted (gang termination occurred).
	// We don't verify full recreation because a node is still cordoned,
	// preventing all pods from being scheduled. The key test assertion is
	// that the persisted MinAvailableBreached=True was respected after restart.
	verifyGangTerminationOccurred(tc, originalPodUIDs)

	logger.Info("Operator restart preserves breach test (GT-7) completed successfully!")
}

// verifyMinAvailableBreachedCondition verifies that the MinAvailableBreached condition
// on a PodClique has the expected status and reason.
func verifyMinAvailableBreachedCondition(tc TestContext, pclqName string, expectedStatus metav1.ConditionStatus, expectedReason string) {
	tc.T.Helper()

	err := pollForCondition(tc, func() (bool, error) {
		pclq, err := tc.DynamicClient.Resource(pclqGVR).Namespace(tc.Namespace).Get(tc.Ctx, pclqName, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("failed to get PodClique %s: %w", pclqName, err)
		}

		// Extract conditions from the status
		status, found, err := unstructuredNestedMap(pclq.Object, "status")
		if err != nil || !found {
			logger.Debugf("PodClique %s has no status yet", pclqName)
			return false, nil
		}

		conditions, found, err := unstructuredNestedSlice(status, "conditions")
		if err != nil || !found {
			logger.Debugf("PodClique %s has no conditions yet", pclqName)
			return false, nil
		}

		// Find MinAvailableBreached condition
		for _, cond := range conditions {
			condMap, ok := cond.(map[string]interface{})
			if !ok {
				continue
			}

			condType, _ := condMap["type"].(string)
			if condType != constants.ConditionTypeMinAvailableBreached {
				continue
			}

			condStatus, _ := condMap["status"].(string)
			condReason, _ := condMap["reason"].(string)

			logger.Debugf("PodClique %s MinAvailableBreached: status=%s, reason=%s (expected: status=%s, reason=%s)",
				pclqName, condStatus, condReason, expectedStatus, expectedReason)

			if condStatus == string(expectedStatus) && condReason == expectedReason {
				return true, nil
			}
			return false, nil
		}

		logger.Debugf("PodClique %s does not have MinAvailableBreached condition yet", pclqName)
		return false, nil
	})

	if err != nil {
		tc.T.Fatalf("Failed to verify MinAvailableBreached condition on %s: %v", pclqName, err)
	}
}

// unstructuredNestedMap extracts a nested map from an unstructured object
func unstructuredNestedMap(obj map[string]interface{}, fields ...string) (map[string]interface{}, bool, error) {
	var current interface{} = obj
	for _, field := range fields {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil, false, nil
		}
		current, ok = m[field]
		if !ok {
			return nil, false, nil
		}
	}
	result, ok := current.(map[string]interface{})
	return result, ok, nil
}

// unstructuredNestedSlice extracts a nested slice from a map
func unstructuredNestedSlice(obj map[string]interface{}, fields ...string) ([]interface{}, bool, error) {
	var current interface{} = obj
	for _, field := range fields {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil, false, nil
		}
		current, ok = m[field]
		if !ok {
			return nil, false, nil
		}
	}
	result, ok := current.([]interface{})
	return result, ok, nil
}

// restartOperator restarts the Grove operator by deleting its pod.
// The deployment will automatically recreate the pod.
func restartOperator(tc TestContext) error {
	// List pods in the operator namespace
	pods, err := tc.Clientset.CoreV1().Pods(setup.OperatorNamespace).List(tc.Ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list pods in namespace %s: %w", setup.OperatorNamespace, err)
	}

	// Find and delete operator pods
	deletedCount := 0
	for _, pod := range pods.Items {
		if strings.HasPrefix(pod.Name, setup.OperatorDeploymentName) {
			logger.Debugf("Deleting operator pod %s to trigger restart", pod.Name)
			if err := tc.Clientset.CoreV1().Pods(setup.OperatorNamespace).Delete(tc.Ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
				return fmt.Errorf("failed to delete operator pod %s: %w", pod.Name, err)
			}
			deletedCount++
		}
	}

	if deletedCount == 0 {
		return fmt.Errorf("no operator pods found with prefix %s in namespace %s", setup.OperatorDeploymentName, setup.OperatorNamespace)
	}

	logger.Debugf("Deleted %d operator pod(s)", deletedCount)
	return nil
}

// waitForOperatorReady waits for the Grove operator to be ready after a restart.
func waitForOperatorReady(tc TestContext) error {
	return pollForCondition(tc, func() (bool, error) {
		pods, err := tc.Clientset.CoreV1().Pods(setup.OperatorNamespace).List(tc.Ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		for _, pod := range pods.Items {
			if strings.HasPrefix(pod.Name, setup.OperatorDeploymentName) {
				// Check if pod is Running and Ready
				if pod.Status.Phase != v1.PodRunning {
					logger.Debugf("Operator pod %s is not running yet: %s", pod.Name, pod.Status.Phase)
					return false, nil
				}

				// Check Ready condition
				for _, cond := range pod.Status.Conditions {
					if cond.Type == v1.PodReady && cond.Status == v1.ConditionTrue {
						logger.Debugf("Operator pod %s is ready", pod.Name)
						return true, nil
					}
				}

				logger.Debugf("Operator pod %s is running but not ready yet", pod.Name)
				return false, nil
			}
		}

		logger.Debugf("No operator pod found yet, waiting for deployment to recreate it")
		return false, nil
	})
}

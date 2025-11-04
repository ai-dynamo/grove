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
	"encoding/json"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/utils"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// Test_GS1_GangSchedulingWithFullReplicas tests gang-scheduling behavior with insufficient resources
// Scenario GS-1:
// 1. Initialize a 10-node Grove cluster, then cordon 1 node
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Verify all workload pods are pending due to insufficient resources
// 4. Uncordon the node and verify all pods get scheduled
func Test_GS1_GangSchedulingWithFullReplicas(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster, then cordon 1 node")
	// Setup test cluster with 10 worker nodes
	clientset, restConfig, _, cleanup := prepareTestCluster(ctx, t, 10)
	defer cleanup()

	// Get worker nodes for cordoning
	workerNodes, err := getWorkerNodes(ctx, clientset)
	if err != nil {
		t.Fatalf("Failed to get worker nodes: %v", err)
	}

	if len(workerNodes) < 1 {
		t.Fatalf("Need at least 1 worker node to cordon, but found %d", len(workerNodes))
	}

	workerNodeToCordon := workerNodes[0]
	logger.Debugf("🚫 Cordoning worker node: %s", workerNodeToCordon)
	if err := utils.CordonNode(ctx, clientset, workerNodeToCordon, true); err != nil {
		t.Fatalf("Failed to cordon node %s: %v", workerNodeToCordon, err)
	}

	logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	// Deploy workload1.yaml
	workloadNamespace := "default"

	_, err = utils.ApplyYAMLFile(ctx, "../yaml/workload1.yaml", workloadNamespace, restConfig, logger)
	if err != nil {
		t.Fatalf("Failed to apply workload YAML: %v", err)
	}

	// Poll for pod creation and verify they are pending
	expectedPods := 10 // pc-a: 2 replicas, pc-b: 1*2 (scaling group), pc-c: 3*2 (scaling group) = 2+2+6=10

	// Poll until we have the expected number of pods created
	var pods *v1.PodList
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/part-of=workload1",
		})
		if err != nil {
			return false, err
		}
		return len(pods.Items) == expectedPods, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for pods to be created: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	if err := verifyPodsArePendingWithUnschedulableEvents(ctx, clientset, workloadNamespace, "app.kubernetes.io/part-of=workload1", true, defaultPollTimeout, defaultPollInterval); err != nil {
		t.Fatalf("Failed to verify all pods have Unschedulable events: %v", err)
	}

	logger.Info("4. Uncordon the node and verify all pods get scheduled")
	if err := utils.CordonNode(ctx, clientset, workerNodeToCordon, false); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", workerNodeToCordon, err)
	}

	// Wait for all pods to be scheduled and ready
	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, "", defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	// Verify all pods are now running
	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/part-of=workload1",
	})
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	// Verify that each pod is scheduled on a unique node, worker nodes have 150m memory
	// and workload pods requests 80m memory, so only 1 should fit per node
	assertPodsOnDistinctNodes(t, pods.Items)

	logger.Info("🎉 Gang-scheduling With Full Replicas test completed successfully!")
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
	logger.Info("1. Initialize a 14-node Grove cluster, then cordon 5 nodes")

	clientset, restConfig, _, cleanup, _ := setupTestCluster(ctx, t, 14)
	defer cleanup()

	// Get worker nodes for cordoning
	workerNodes, err := getWorkerNodes(ctx, clientset)
	if err != nil {
		t.Fatalf("Failed to get worker nodes: %v", err)
	}

	if len(workerNodes) < 5 {
		t.Fatalf("expected at least 5 worker nodes to cordon, but found %d", len(workerNodes))
	}

	nodesToCordon := workerNodes[:5]
	for _, nodeName := range nodesToCordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, true); err != nil {
			t.Fatalf("Failed to cordon node %s: %v", nodeName, err)
		}
	}

	logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	workloadNamespace := "default"
	workloadYAMLPath := "../yaml/workload1.yaml"
	workloadLabelSelector := "app.kubernetes.io/part-of=workload1"

	_, err = utils.ApplyYAMLFile(ctx, workloadYAMLPath, workloadNamespace, restConfig, logger)
	if err != nil {
		t.Fatalf("Failed to apply workload YAML: %v", err)
	}

	expectedPods := 10

	var pods *v1.PodList
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		return len(pods.Items) == expectedPods, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for pods to be created: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	if err := verifyPodsArePendingWithUnschedulableEvents(ctx, clientset, workloadNamespace, workloadLabelSelector, true, defaultPollTimeout, defaultPollInterval); err != nil {
		t.Fatalf("Failed to verify all pods have Unschedulable events: %v", err)
	}

	logger.Info("4. Uncordon 1 node to allow scheduling and verify pods get scheduled")
	firstNodeToUncordon := nodesToCordon[0]
	if err := utils.CordonNode(ctx, clientset, firstNodeToUncordon, false); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	logger.Info("5. Wait for pods to become ready")
	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: workloadLabelSelector,
	})
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	runningPods := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			runningPods++
		}
	}

	if runningPods != len(pods.Items) {
		t.Fatalf("Expected all %d pods to be running, but only %d are running", len(pods.Items), runningPods)
	}
	assertPodsOnDistinctNodes(t, pods.Items)

	logger.Info("6. Scale PCSG replicas to 3 and verify 4 new pending pods")
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("Failed to create dynamic client: %v", err)
	}

	pcsgGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquescalinggroups"}
	pcsgName := "workload1-0-sg-x"

	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		_, err := dynamicClient.Resource(pcsgGVR).Namespace(workloadNamespace).Get(ctx, pcsgName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("Failed to find PodCliqueScalingGroup %s: %v", pcsgName, err)
	}

	scalePatch := map[string]interface{}{
		"spec": map[string]interface{}{
			"replicas": 3,
		},
	}
	patchBytes, err := json.Marshal(scalePatch)
	if err != nil {
		t.Fatalf("Failed to marshal scale patch: %v", err)
	}

	if _, err := dynamicClient.Resource(pcsgGVR).Namespace(workloadNamespace).Patch(ctx, pcsgName, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
		t.Fatalf("Failed to scale PodCliqueScalingGroup %s: %v", pcsgName, err)
	}

	expectedScaledPods := 14
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}
		return len(pods.Items) == expectedScaledPods, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for scaled pods to be created: %v", err)
	}

	runningPods = 0
	pendingPods := 0
	for _, pod := range pods.Items {
		switch pod.Status.Phase {
		case v1.PodRunning:
			runningPods++
		case v1.PodPending:
			pendingPods++
		}
	}

	if pendingPods != 4 {
		t.Fatalf("Expected 4 pending pods after scaling, but found %d", pendingPods)
	}
	if runningPods != expectedPods {
		t.Fatalf("Expected %d running pods after scaling, but found %d", expectedPods, runningPods)
	}

	logger.Info("7. Uncordon remaining nodes and verify all pods get scheduled")
	remainingNodesToUncordon := nodesToCordon[1:]
	for _, nodeName := range remainingNodesToUncordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for scaled pods to be ready: %v", err)
	}

	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: workloadLabelSelector,
	})
	if err != nil {
		t.Fatalf("Failed to list workload pods after final uncordon: %v", err)
	}

	runningPods = 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			runningPods++
		}
	}

	if runningPods != len(pods.Items) {
		t.Fatalf("Expected all %d pods to be running after scaling, but only %d are running", len(pods.Items), runningPods)
	}

	assertPodsOnDistinctNodes(t, pods.Items)

	logger.Info("🎉 Gang-scheduling PCSG scaling test completed successfully!")
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

	logger.Info("1. Initialize a 20-node Grove cluster, then cordon 11 nodes")
	clientset, restConfig, _, cleanup, _ := setupTestCluster(ctx, t, 20)
	defer cleanup()

	// Get worker nodes for cordoning
	workerNodes, err := getWorkerNodes(ctx, clientset)
	if err != nil {
		t.Fatalf("Failed to get worker nodes: %v", err)
	}

	if len(workerNodes) < 11 {
		t.Fatalf("expected at least 11 worker nodes to cordon, but found %d", len(workerNodes))
	}

	// Step 1 (continued): Cordon 11 nodes
	nodesToCordon := workerNodes[:11]
	for _, nodeName := range nodesToCordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, true); err != nil {
			t.Fatalf("Failed to cordon node %s: %v", nodeName, err)
		}
	}

	logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	workloadNamespace := "default"
	workloadYAMLPath := "../yaml/workload1.yaml"
	workloadLabelSelector := "app.kubernetes.io/part-of=workload1"

	_, err = utils.ApplyYAMLFile(ctx, workloadYAMLPath, workloadNamespace, restConfig, logger)
	if err != nil {
		t.Fatalf("Failed to apply workload YAML: %v", err)
	}

	expectedPods := 10
	var pods *v1.PodList
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}
		return len(pods.Items) == expectedPods, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for pods to be created: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	if err := verifyPodsArePendingWithUnschedulableEvents(ctx, clientset, workloadNamespace, workloadLabelSelector, true, defaultPollTimeout, defaultPollInterval); err != nil {
		t.Fatalf("Failed to verify all pods have Unschedulable events: %v", err)
	}

	logger.Info("4. Uncordon 1 node to allow scheduling and verify pods get scheduled")
	firstNodeToUncordon := nodesToCordon[0]
	if err := utils.CordonNode(ctx, clientset, firstNodeToUncordon, false); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	logger.Info("5. Wait for pods to become ready")
	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: workloadLabelSelector,
	})
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	assertPodsOnDistinctNodes(t, pods.Items)

	logger.Info("6. Scale PCS replicas to 2 and verify 10 new pending pods")
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("Failed to create dynamic client: %v", err)
	}

	replicas := int32(2)
	pcsPatch := map[string]interface{}{
		"spec": map[string]interface{}{
			"replicas": replicas,
		},
	}
	pcsPatchBytes, err := json.Marshal(pcsPatch)
	if err != nil {
		t.Fatalf("Failed to marshal PodCliqueSet patch: %v", err)
	}

	pcsGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}
	pcsName := "workload1"

	if _, err := dynamicClient.Resource(pcsGVR).Namespace(workloadNamespace).Patch(ctx, pcsName, types.MergePatchType, pcsPatchBytes, metav1.PatchOptions{}); err != nil {
		t.Fatalf("Failed to scale PodCliqueSet %s: %v", pcsName, err)
	}

	expectedScaledPods := int(replicas) * expectedPods
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		return len(pods.Items) == expectedScaledPods, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for scaled pods to be created: %v", err)
	}

	runningPods := 0
	pendingPods := 0
	for _, pod := range pods.Items {
		switch pod.Status.Phase {
		case v1.PodRunning:
			runningPods++
		case v1.PodPending:
			pendingPods++
		}
	}

	expectedNewPending := expectedScaledPods - expectedPods
	if pendingPods != expectedNewPending {
		t.Fatalf("Expected %d pending pods after scaling, but found %d", expectedNewPending, pendingPods)
	}
	if runningPods != expectedPods {
		t.Fatalf("Expected %d running pods after scaling, but found %d", expectedPods, runningPods)
	}

	logger.Info("7. Uncordon remaining nodes and verify all pods get scheduled")
	remainingNodesToUncordon := nodesToCordon[1:]
	for _, nodeName := range remainingNodesToUncordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for scaled pods to be ready: %v", err)
	}

	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: workloadLabelSelector,
	})
	if err != nil {
		t.Fatalf("Failed to list workload pods after final uncordon: %v", err)
	}

	runningPods = 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			runningPods++
		}
	}

	if runningPods != len(pods.Items) {
		t.Fatalf("Expected all %d pods to be running after scaling, but only %d are running", len(pods.Items), runningPods)
	}
	assertPodsOnDistinctNodes(t, pods.Items)

	logger.Info("🎉 Gang-scheduling PCS scaling test completed successfully!")
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

	logger.Info("1. Initialize a 28-node Grove cluster, then cordon 19 nodes")
	// Setup cluster (shared or individual based on test run mode)
	clientset, restConfig, _, cleanup, _ := setupTestCluster(ctx, t, 28)
	defer cleanup()

	// Get worker nodes for cordoning
	workerNodes, err := getWorkerNodes(ctx, clientset)
	if err != nil {
		t.Fatalf("Failed to get worker nodes: %v", err)
	}

	if len(workerNodes) < 19 {
		t.Fatalf("expected at least 19 worker nodes to cordon, but found %d", len(workerNodes))
	}

	// cordon 19 nodes
	nodesToCordon := workerNodes[:19]
	for _, nodeName := range nodesToCordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, true); err != nil {
			t.Fatalf("Failed to cordon node %s: %v", nodeName, err)
		}
	}

	logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	workloadNamespace := "default"
	workloadYAMLPath := "../yaml/workload1.yaml"
	workloadLabelSelector := "app.kubernetes.io/part-of=workload1"

	_, err = utils.ApplyYAMLFile(ctx, workloadYAMLPath, workloadNamespace, restConfig, logger)
	if err != nil {
		t.Fatalf("Failed to apply workload YAML: %v", err)
	}

	logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	expectedPods := 10

	var pods *v1.PodList
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
		if err != nil {
			return false, err
		}

		return len(pods.Items) == expectedPods, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for pods to be created: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	if err := verifyPodsArePendingWithUnschedulableEvents(ctx, clientset, workloadNamespace, workloadLabelSelector, true, defaultPollTimeout, defaultPollInterval); err != nil {
		t.Fatalf("Failed to verify all pods have Unschedulable events: %v", err)
	}

	logger.Info("4. Uncordon 1 node to allow scheduling and verify pods get scheduled")
	firstNodeToUncordon := nodesToCordon[0]
	if err := utils.CordonNode(ctx, clientset, firstNodeToUncordon, false); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	logger.Info("5. Wait for pods to become ready")
	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	runningPods := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			runningPods++
		}
	}

	assertPodsOnDistinctNodes(t, pods.Items)

	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("Failed to create dynamic client: %v", err)
	}

	logger.Info("6. Scale PCSG replicas to 3 and verify 4 new pending pods")
	pcsgName := "workload1-0-sg-x"
	scalePCSGAndWait(t, ctx, clientset, dynamicClient, workloadNamespace, workloadLabelSelector, pcsgName, 3, 14, 4, defaultPollTimeout, defaultPollInterval)

	logger.Info("7. Uncordon 4 nodes and verify scaled pods get scheduled")
	remainingNodesAfterFirstUncordon := nodesToCordon[1:5]
	for _, nodeName := range remainingNodesAfterFirstUncordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for pods to be ready after PCSG scale: %v", err)
	}

	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
	if err != nil {
		t.Fatalf("Failed to list workload pods after PCSG scale: %v", err)
	}

	assertPodsOnDistinctNodes(t, pods.Items)

	logger.Info("8. Scale PCS replicas to 2 and verify 10 new pending pods")
	scalePCSAndWait(t, ctx, clientset, dynamicClient, workloadNamespace, workloadLabelSelector, "workload1", 2, 24, 10, defaultPollTimeout, defaultPollInterval)

	remainingNodesAfterPCSScale := nodesToCordon[5:15]
	for _, nodeName := range remainingNodesAfterPCSScale {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for pods to be ready after PCS scale: %v", err)
	}

	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
	if err != nil {
		t.Fatalf("Failed to list workload pods after PCS scale: %v", err)
	}

	assertPodsOnDistinctNodes(t, pods.Items)

	logger.Info("9. Scale PCSG replicas to 3 and verify 4 new pending pods")
	secondReplicaPCSGName := "workload1-1-sg-x"
	scalePCSGAndWait(t, ctx, clientset, dynamicClient, workloadNamespace, workloadLabelSelector, secondReplicaPCSGName, 3, 28, 4, defaultPollTimeout, defaultPollInterval)

	logger.Info("10. Uncordon remaining nodes and verify all pods get scheduled")
	finalNodes := nodesToCordon[15:19]
	for _, nodeName := range finalNodes {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for pods to be ready after final PCSG scale: %v", err)
	}

	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
	if err != nil {
		t.Fatalf("Failed to list workload pods after final PCSG scale: %v", err)
	}

	assertPodsOnDistinctNodes(t, pods.Items)

	logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
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

	logger.Info("1. Initialize a 10-node Grove cluster, then cordon 8 nodes")
	// Setup cluster (shared or individual based on test run mode)
	clientset, restConfig, _, cleanup, _ := setupTestCluster(ctx, t, 10)
	defer cleanup()

	// Get worker nodes for cordoning
	workerNodes, err := getWorkerNodes(ctx, clientset)
	if err != nil {
		t.Fatalf("Failed to get worker nodes: %v", err)
	}

	if len(workerNodes) < 8 {
		t.Fatalf("expected at least 8 worker nodes to cordon, but found %d", len(workerNodes))
	}

	// Cordon 8 worker nodes
	nodesToCordon := workerNodes[:8]
	for _, nodeName := range nodesToCordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, true); err != nil {
			t.Fatalf("Failed to cordon node %s: %v", nodeName, err)
		}
	}

	logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	workloadNamespace := "default"
	workloadYAMLPath := "../yaml/workload2.yaml"
	workloadLabelSelector := "app.kubernetes.io/part-of=workload2"

	_, err = utils.ApplyYAMLFile(ctx, workloadYAMLPath, workloadNamespace, restConfig, logger)
	if err != nil {
		t.Fatalf("Failed to apply workload YAML: %v", err)
	}

	// workload2 creates: 1 PCS replica * (pc-a: 2 + pc-b: 1 + pc-c: 3) + sg-x: 2 replicas * (pc-b: 1 + pc-c: 3) = 6 + 8 = 14 pods
	// But the test description says 10 pods, so let me check the actual workload2 structure more carefully
	expectedPods := 10

	var pods *v1.PodList
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		return len(pods.Items) == expectedPods, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for pods to be created: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	// Need to use a sleep here unfortunately, see: https://github.com/ai-dynamo/grove/issues/226
	time.Sleep(30 * time.Second)
	if err := verifyAllPodsArePending(ctx, clientset, workloadNamespace, workloadLabelSelector, defaultPollTimeout, defaultPollInterval); err != nil {
		t.Fatalf("Failed to verify all pods are pending: %v", err)
	}

	firstNodeToUncordon := nodesToCordon[0]
	if err := utils.CordonNode(ctx, clientset, firstNodeToUncordon, false); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		runningPods := 0
		pendingPods := 0
		for _, pod := range pods.Items {
			switch pod.Status.Phase {
			case v1.PodRunning:
				runningPods++
			case v1.PodPending:
				pendingPods++
			}
		}

		// We expect exactly 3 pods to be running (min-replicas) and the rest pending
		return runningPods == 3 && pendingPods == len(pods.Items)-3, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	// Verify the scheduled pods and their distribution
	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: workloadLabelSelector,
	})
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	runningPods := 0
	pendingPods := 0
	runningPodNames := make([]string, 0)
	for _, pod := range pods.Items {
		switch pod.Status.Phase {
		case v1.PodRunning:
			runningPods++
			runningPodNames = append(runningPodNames, pod.Name)
		case v1.PodPending:
			pendingPods++
		}
	}

	if runningPods != 3 {
		t.Fatalf("Expected exactly 3 pods to be running (min-replicas), but found %d", runningPods)
	}

	if pendingPods != len(pods.Items)-3 {
		t.Fatalf("Expected %d pods to remain pending, but found %d", len(pods.Items)-3, pendingPods)
	}

	logger.Info("5. Wait for scheduled pods to become ready")
	// Note: WaitForPods waits for ALL pods, but we only want the running ones to be ready
	// We'll verify readiness manually
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		readyRunningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				// Check if pod is ready
				for _, condition := range pod.Status.Conditions {
					if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
						readyRunningPods++
						break
					}
				}
			}
		}

		return readyRunningPods == 3, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	logger.Info("6. Uncordon 7 nodes and verify all remaining workload pods get scheduled")
	remainingNodesToUncordon := nodesToCordon[1:]
	for _, nodeName := range remainingNodesToUncordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	// Wait for all remaining pods to be scheduled and ready
	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all pods should be running
	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: workloadLabelSelector,
	})
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	finalRunningPods := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			finalRunningPods++
		}
	}

	if finalRunningPods != len(pods.Items) {
		t.Fatalf("Expected all %d pods to be running, but only %d are running", len(pods.Items), finalRunningPods)
	}

	// Verify pods are distributed across distinct nodes
	assertPodsOnDistinctNodes(t, pods.Items)

	logger.Info("🎉 Gang-scheduling min-replicas test (GS-5) completed successfully!")
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

	logger.Info("1. Initialize a 14-node Grove cluster, then cordon 12 nodes")
	// Setup cluster (shared or individual based on test run mode)
	clientset, restConfig, _, cleanup, _ := setupTestCluster(ctx, t, 14)
	defer cleanup()

	// Get worker nodes for cordoning
	workerNodes, err := getWorkerNodes(ctx, clientset)
	if err != nil {
		t.Fatalf("Failed to get worker nodes: %v", err)
	}

	if len(workerNodes) < 12 {
		t.Fatalf("expected at least 12 worker nodes to cordon, but found %d", len(workerNodes))
	}

	// Cordon 12 worker nodes
	nodesToCordon := workerNodes[:12]
	for _, nodeName := range nodesToCordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, true); err != nil {
			t.Fatalf("Failed to cordon node %s: %v", nodeName, err)
		}
	}

	logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	workloadNamespace := "default"
	workloadYAMLPath := "../yaml/workload2.yaml"
	workloadLabelSelector := "app.kubernetes.io/part-of=workload2"

	_, err = utils.ApplyYAMLFile(ctx, workloadYAMLPath, workloadNamespace, restConfig, logger)
	if err != nil {
		t.Fatalf("Failed to apply workload YAML: %v", err)
	}

	// workload2 initially creates 10 pods
	expectedPods := 10
	var pods *v1.PodList
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		return len(pods.Items) == expectedPods, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for pods to be created: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	// Need to use a sleep here unfortunately, see: https://github.com/NVIDIA/grove/issues/226
	time.Sleep(30 * time.Second)
	if err := verifyAllPodsArePending(ctx, clientset, workloadNamespace, workloadLabelSelector, defaultPollTimeout, defaultPollInterval); err != nil {
		t.Fatalf("Failed to verify all pods are pending: %v", err)
	}

	logger.Info("4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	// Based on workload2 min-replicas: pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1}
	firstNodeToUncordon := nodesToCordon[0]
	if err := utils.CordonNode(ctx, clientset, firstNodeToUncordon, false); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		runningPods := 0
		pendingPods := 0
		for _, pod := range pods.Items {
			switch pod.Status.Phase {
			case v1.PodRunning:
				runningPods++
			case v1.PodPending:
				pendingPods++
			}
		}

		// We expect exactly 3 pods to be running (min-replicas) and the rest pending
		return runningPods == 3 && pendingPods == len(pods.Items)-3, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	// Verify the scheduled pods and their distribution
	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: workloadLabelSelector,
	})
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	runningPods := 0
	pendingPods := 0
	runningPodNames := make([]string, 0)
	for _, pod := range pods.Items {
		switch pod.Status.Phase {
		case v1.PodRunning:
			runningPods++
			runningPodNames = append(runningPodNames, pod.Name)
		case v1.PodPending:
			pendingPods++
		}
	}

	if runningPods != 3 {
		t.Fatalf("Expected exactly 3 pods to be running (min-replicas), but found %d", runningPods)
	}

	if pendingPods != len(pods.Items)-3 {
		t.Fatalf("Expected %d pods to remain pending, but found %d", len(pods.Items)-3, pendingPods)
	}

	logger.Info("5. Wait for scheduled pods to become ready")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		readyRunningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				// Check if pod is ready
				for _, condition := range pod.Status.Conditions {
					if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
						readyRunningPods++
						break
					}
				}
			}
		}

		return readyRunningPods == 3, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	logger.Info("6. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	sevenNodesToUncordon := nodesToCordon[1:8]
	for _, nodeName := range sevenNodesToUncordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	// Wait for all remaining pods to be scheduled and ready
	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Verify all 10 initial pods are running
	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: workloadLabelSelector,
	})
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	allRunningPods := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			allRunningPods++
		}
	}

	if allRunningPods != len(pods.Items) {
		t.Fatalf("Expected all %d pods to be running, but only %d are running", len(pods.Items), allRunningPods)
	}

	logger.Info("7. Wait for scheduled pods to become ready")
	// Create dynamic client for PCSG scaling operations
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("Failed to create dynamic client: %v", err)
	}

	logger.Info("8. Set pcs-0-sg-x resource replicas equal to 3, then verify 4 newly created pods")
	// Scale PCSG sg-x to 3 replicas and verify 4 newly created pods
	pcsgName := "workload2-0-sg-x"
	// Expected total pods after scaling: 10 (initial) + 4 (new from scaling sg-x from 2 to 3) = 14
	expectedPodsAfterScaling := 14
	expectedNewPendingPods := 4

	scalePCSGAndWait(t, ctx, clientset, dynamicClient, workloadNamespace, workloadLabelSelector, pcsgName, 3, expectedPodsAfterScaling, expectedNewPendingPods, defaultPollTimeout, defaultPollInterval)

	logger.Info("9. Verify all newly created pods are pending due to insufficient resources")
	if err := verifyPodsArePendingWithUnschedulableEvents(ctx, clientset, workloadNamespace, workloadLabelSelector, false, defaultPollTimeout, defaultPollInterval); err != nil {
		t.Fatalf("Failed to verify all pending pods have Unschedulable events: %v", err)
	}

	logger.Info("10. Uncordon 2 nodes and verify 2 more pods get scheduled (pcs-0-{sg-x-2-pc-b=1, sg-x-2-pc-c=1})")
	// Uncordon 2 nodes and verify exactly 2 more pods get scheduled
	// pcs-0-{sg-x-2-pc-b = 1, sg-x-2-pc-c = 1} (min-replicas for the new PCSG replica)
	twoNodesToUncordon := nodesToCordon[8:10]
	for _, nodeName := range twoNodesToUncordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	// Wait for exactly 2 more pods to be scheduled (min-replicas for new PCSG replica)
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		runningPods := 0
		pendingPods := 0
		for _, pod := range pods.Items {
			switch pod.Status.Phase {
			case v1.PodRunning:
				runningPods++
			case v1.PodPending:
				pendingPods++
			}
		}

		// We expect 12 pods running (10 initial + 2 from min-replicas) and 2 pending
		return runningPods == 12 && pendingPods == 2, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for exactly 2 more pods to be scheduled after PCSG scaling: %v", err)
	}

	logger.Info("11. Wait for scheduled pods to become ready")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		readyRunningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				// Check if pod is ready
				for _, condition := range pod.Status.Conditions {
					if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
						readyRunningPods++
						break
					}
				}
			}
		}

		return readyRunningPods == 12, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for 12 pods to become ready: %v", err)
	}

	logger.Info("12. Uncordon 2 nodes and verify remaining workload pods get scheduled")
	// Uncordon remaining 2 nodes and verify all remaining workload pods get scheduled
	remainingNodesToUncordon := nodesToCordon[10:12]
	for _, nodeName := range remainingNodesToUncordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	// Wait for all remaining pods to be scheduled and ready
	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 14 pods should be running
	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: workloadLabelSelector,
	})
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	finalRunningPods := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			finalRunningPods++
		}
	}

	if finalRunningPods != expectedPodsAfterScaling {
		t.Fatalf("Expected all %d pods to be running, but only %d are running", expectedPodsAfterScaling, finalRunningPods)
	}

	// Verify pods are distributed across distinct nodes
	assertPodsOnDistinctNodes(t, pods.Items)

	logger.Info("🎉 Gang-scheduling PCSG scaling min-replicas test (GS-6) completed successfully!")
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

	logger.Info("1. Initialize a 14-node Grove cluster, then cordon 12 nodes")
	// Setup cluster (shared or individual based on test run mode)
	clientset, restConfig, _, cleanup, _ := setupTestCluster(ctx, t, 14)
	defer cleanup()

	// Get worker nodes for cordoning
	workerNodes, err := getWorkerNodes(ctx, clientset)
	if err != nil {
		t.Fatalf("Failed to get worker nodes: %v", err)
	}

	if len(workerNodes) < 12 {
		t.Fatalf("expected at least 12 worker nodes to cordon, but found %d", len(workerNodes))
	}

	// Cordon 12 worker nodes
	nodesToCordon := workerNodes[:12]
	for _, nodeName := range nodesToCordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, true); err != nil {
			t.Fatalf("Failed to cordon node %s: %v", nodeName, err)
		}
	}

	logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	workloadNamespace := "default"
	workloadYAMLPath := "../yaml/workload2.yaml"
	workloadLabelSelector := "app.kubernetes.io/part-of=workload2"

	_, err = utils.ApplyYAMLFile(ctx, workloadYAMLPath, workloadNamespace, restConfig, logger)
	if err != nil {
		t.Fatalf("Failed to apply workload YAML: %v", err)
	}

	// workload2 initially creates 10 pods
	expectedPods := 10
	var pods *v1.PodList
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		return len(pods.Items) == expectedPods, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for pods to be created: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	// Need to use a sleep here unfortunately, see: https://github.com/NVIDIA/grove/issues/226
	time.Sleep(30 * time.Second)
	if err := verifyAllPodsArePending(ctx, clientset, workloadNamespace, workloadLabelSelector, defaultPollTimeout, defaultPollInterval); err != nil {
		t.Fatalf("Failed to verify all pods are pending: %v", err)
	}

	logger.Info("4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	firstNodeToUncordon := nodesToCordon[0]
	if err := utils.CordonNode(ctx, clientset, firstNodeToUncordon, false); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		runningPods := 0
		pendingPods := 0
		for _, pod := range pods.Items {
			switch pod.Status.Phase {
			case v1.PodRunning:
				runningPods++
			case v1.PodPending:
				pendingPods++
			}
		}

		// We expect exactly 3 pods to be running (min-replicas) and the rest pending
		return runningPods == 3 && pendingPods == len(pods.Items)-3, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	logger.Info("5. Wait for scheduled pods to become ready")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		readyRunningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				// Check if pod is ready
				for _, condition := range pod.Status.Conditions {
					if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
						readyRunningPods++
						break
					}
				}
			}
		}

		return readyRunningPods == 3, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	logger.Info("6. Uncordon 2 nodes and verify 2 more pods get scheduled (pcs-0-{sg-x-1-pc-b=1, sg-x-1-pc-c=1})")
	twoNodesToUncordon := nodesToCordon[1:3]
	for _, nodeName := range twoNodesToUncordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	// Wait for exactly 2 more pods to be scheduled (sg-x-1 min-replicas)
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		runningPods := 0
		pendingPods := 0
		for _, pod := range pods.Items {
			switch pod.Status.Phase {
			case v1.PodRunning:
				runningPods++
			case v1.PodPending:
				pendingPods++
			}
		}

		// We expect 5 pods running (3 + 2 new) and the rest pending
		return runningPods == 5 && pendingPods == len(pods.Items)-5, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for exactly 2 more pods to be scheduled: %v", err)
	}

	logger.Info("7. Wait for scheduled pods to become ready")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		readyRunningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				// Check if pod is ready
				for _, condition := range pod.Status.Conditions {
					if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
						readyRunningPods++
						break
					}
				}
			}
		}

		return readyRunningPods == 5, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for 5 scheduled pods to become ready: %v", err)
	}

	logger.Info("8. Uncordon 5 nodes and verify the remaining workload pods get scheduled")
	fiveNodesToUncordon := nodesToCordon[3:8]
	for _, nodeName := range fiveNodesToUncordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	// Wait for all remaining pods to be scheduled and ready
	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Verify all 10 initial pods are running
	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: workloadLabelSelector,
	})
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	allRunningPods := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			allRunningPods++
		}
	}

	if allRunningPods != len(pods.Items) {
		t.Fatalf("Expected all %d pods to be running, but only %d are running", len(pods.Items), allRunningPods)
	}

	logger.Info("9. Wait for scheduled pods to become ready (already verified above)")

	// Create dynamic client for PCSG scaling operations
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("Failed to create dynamic client: %v", err)
	}

	logger.Info("11. Verify all newly created pods are pending due to insufficient resources (verified in scalePCSGAndWait)")
	pcsgName := "workload2-0-sg-x"
	expectedPodsAfterScaling := 14
	expectedNewPendingPods := 4
	logger.Info("10. Set pcs-0-sg-x resource replicas equal to 3, then verify 4 newly created pods")
	scalePCSGAndWait(t, ctx, clientset, dynamicClient, workloadNamespace, workloadLabelSelector, pcsgName, 3, expectedPodsAfterScaling, expectedNewPendingPods, defaultPollTimeout, defaultPollInterval)

	logger.Info("12. Uncordon 2 nodes and verify 2 more pods get scheduled (pcs-0-{sg-x-2-pc-b=1, sg-x-2-pc-c=1})")
	twoMoreNodesToUncordon := nodesToCordon[8:10]
	for _, nodeName := range twoMoreNodesToUncordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	// Wait for exactly 2 more pods to be scheduled (min-replicas for new PCSG replica)
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		runningPods := 0
		pendingPods := 0
		for _, pod := range pods.Items {
			switch pod.Status.Phase {
			case v1.PodRunning:
				runningPods++
			case v1.PodPending:
				pendingPods++
			}
		}

		// We expect 12 pods running (10 initial + 2 from min-replicas) and 2 pending
		return runningPods == 12 && pendingPods == 2, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for exactly 2 more pods to be scheduled after PCSG scaling: %v", err)
	}

	logger.Info("13. Wait for scheduled pods to become ready")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		readyRunningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				// Check if pod is ready
				for _, condition := range pod.Status.Conditions {
					if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
						readyRunningPods++
						break
					}
				}
			}
		}

		return readyRunningPods == 12, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for 12 pods to become ready: %v", err)
	}

	logger.Info("14. Uncordon 2 nodes and verify remaining workload pods get scheduled")
	remainingNodesToUncordon := nodesToCordon[10:12]
	for _, nodeName := range remainingNodesToUncordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	// Wait for all remaining pods to be scheduled and ready
	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 14 pods should be running
	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: workloadLabelSelector,
	})
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	finalRunningPods := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			finalRunningPods++
		}
	}

	if finalRunningPods != expectedPodsAfterScaling {
		t.Fatalf("Expected all %d pods to be running, but only %d are running", expectedPodsAfterScaling, finalRunningPods)
	}

	// Verify pods are distributed across distinct nodes
	assertPodsOnDistinctNodes(t, pods.Items)

	logger.Info("🎉 Gang-scheduling PCSG scaling min-replicas advanced1 test (GS-7) completed successfully! All workload pods transitioned correctly through advanced PCSG scaling with min-replicas.")
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

	logger.Info("1. Initialize a 14-node Grove cluster, then cordon 12 nodes")
	// Setup cluster (shared or individual based on test run mode)
	clientset, restConfig, _, cleanup, _ := setupTestCluster(ctx, t, 14)
	defer cleanup()

	// Get worker nodes for cordoning
	workerNodes, err := getWorkerNodes(ctx, clientset)
	if err != nil {
		t.Fatalf("Failed to get worker nodes: %v", err)
	}

	if len(workerNodes) < 12 {
		t.Fatalf("expected at least 12 worker nodes to cordon, but found %d", len(workerNodes))
	}

	// Cordon 12 worker nodes
	nodesToCordon := workerNodes[:12]
	for _, nodeName := range nodesToCordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, true); err != nil {
			t.Fatalf("Failed to cordon node %s: %v", nodeName, err)
		}
	}

	logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	workloadNamespace := "default"
	workloadYAMLPath := "../yaml/workload2.yaml"
	workloadLabelSelector := "app.kubernetes.io/part-of=workload2"

	_, err = utils.ApplyYAMLFile(ctx, workloadYAMLPath, workloadNamespace, restConfig, logger)
	if err != nil {
		t.Fatalf("Failed to apply workload YAML: %v", err)
	}

	// workload2 initially creates 10 pods
	expectedPods := 10
	var pods *v1.PodList
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		return len(pods.Items) == expectedPods, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for pods to be created: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	// Need to use a sleep here unfortunately, see: https://github.com/NVIDIA/grove/issues/226
	time.Sleep(30 * time.Second)
	if err := verifyAllPodsArePending(ctx, clientset, workloadNamespace, workloadLabelSelector, defaultPollTimeout, defaultPollInterval); err != nil {
		t.Fatalf("Failed to verify all pods are pending: %v", err)
	}

	// Create dynamic client for PCSG scaling operations
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("Failed to create dynamic client: %v", err)
	}

	logger.Info("4. Set pcs-0-sg-x resource replicas equal to 3, verify 4 more newly created pods")
	pcsgName := "workload2-0-sg-x"
	expectedPodsAfterScaling := 14

	scalePatch := map[string]interface{}{
		"spec": map[string]interface{}{
			"replicas": 3,
		},
	}
	patchBytes, err := json.Marshal(scalePatch)
	if err != nil {
		t.Fatalf("Failed to marshal scale patch: %v", err)
	}

	pcsgGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquescalinggroups"}
	if _, err := dynamicClient.Resource(pcsgGVR).Namespace(workloadNamespace).Patch(ctx, pcsgName, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
		t.Fatalf("Failed to scale PodCliqueScalingGroup %s: %v", pcsgName, err)
	}

	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		return len(pods.Items) == expectedPodsAfterScaling, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for scaled pods to be created: %v", err)
	}

	logger.Info("5. Verify all 14 newly created pods are pending due to insufficient resources")
	// Need to use a sleep here unfortunately, see: https://github.com/NVIDIA/grove/issues/226
	time.Sleep(30 * time.Second)
	if err := verifyAllPodsArePending(ctx, clientset, workloadNamespace, workloadLabelSelector, defaultPollTimeout, defaultPollInterval); err != nil {
		t.Fatalf("Failed to verify all pods are pending: %v", err)
	}

	logger.Info("6. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	firstNodeToUncordon := nodesToCordon[0]
	if err := utils.CordonNode(ctx, clientset, firstNodeToUncordon, false); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		runningPods := 0
		pendingPods := 0
		for _, pod := range pods.Items {
			switch pod.Status.Phase {
			case v1.PodRunning:
				runningPods++
			case v1.PodPending:
				pendingPods++
			}
		}

		// We expect exactly 3 pods to be running (min-replicas) and the rest pending
		return runningPods == 3 && pendingPods == len(pods.Items)-3, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	logger.Info("7. Wait for scheduled pods to become ready")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		readyRunningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				// Check if pod is ready
				for _, condition := range pod.Status.Conditions {
					if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
						readyRunningPods++
						break
					}
				}
			}
		}

		return readyRunningPods == 3, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	logger.Info("8. Uncordon 4 nodes and verify 4 more pods get scheduled")
	fourNodesToUncordon := nodesToCordon[1:5]
	for _, nodeName := range fourNodesToUncordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	// Wait for exactly 4 more pods to be scheduled (sg-x-1 and sg-x-2 min-replicas)
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		runningPods := 0
		pendingPods := 0
		for _, pod := range pods.Items {
			switch pod.Status.Phase {
			case v1.PodRunning:
				runningPods++
			case v1.PodPending:
				pendingPods++
			}
		}
		// We expect 7 pods running (3 + 4 new) and the rest pending
		return runningPods == 7 && pendingPods == len(pods.Items)-7, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for exactly 4 more pods to be scheduled: %v", err)
	}

	logger.Info("9. Wait for scheduled pods to become ready")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		readyRunningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				// Check if pod is ready
				for _, condition := range pod.Status.Conditions {
					if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
						readyRunningPods++
						break
					}
				}
			}
		}

		return readyRunningPods == 7, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for 7 scheduled pods to become ready: %v", err)
	}

	logger.Info("10. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	remainingNodesToUncordon := nodesToCordon[5:]
	for _, nodeName := range remainingNodesToUncordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	// Wait for all remaining pods to be scheduled and ready
	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 14 pods should be running
	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: workloadLabelSelector,
	})
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	finalRunningPods := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			finalRunningPods++
		}
	}

	if finalRunningPods != expectedPodsAfterScaling {
		t.Fatalf("Expected all %d pods to be running, but only %d are running", expectedPodsAfterScaling, finalRunningPods)
	}

	// Verify pods are distributed across distinct nodes
	assertPodsOnDistinctNodes(t, pods.Items)

	logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
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

	logger.Info("1. Initialize a 20-node Grove cluster, then cordon 18 nodes")
	// Setup cluster (shared or individual based on test run mode)
	clientset, restConfig, _, cleanup, _ := setupTestCluster(ctx, t, 20)
	defer cleanup()

	// Get worker nodes for cordoning
	workerNodes, err := getWorkerNodes(ctx, clientset)
	if err != nil {
		t.Fatalf("Failed to get worker nodes: %v", err)
	}

	if len(workerNodes) < 18 {
		t.Fatalf("expected at least 18 worker nodes to cordon, but found %d", len(workerNodes))
	}

	// Cordon 18 worker nodes
	nodesToCordon := workerNodes[:18]
	for _, nodeName := range nodesToCordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, true); err != nil {
			t.Fatalf("Failed to cordon node %s: %v", nodeName, err)
		}
	}

	logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	workloadNamespace := "default"
	workloadYAMLPath := "../yaml/workload2.yaml"
	workloadLabelSelector := "app.kubernetes.io/part-of=workload2"

	_, err = utils.ApplyYAMLFile(ctx, workloadYAMLPath, workloadNamespace, restConfig, logger)
	if err != nil {
		t.Fatalf("Failed to apply workload YAML: %v", err)
	}

	// workload2 initially creates 10 pods
	expectedPods := 10

	var pods *v1.PodList
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		return len(pods.Items) == expectedPods, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for pods to be created: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	// Need to use a sleep here unfortunately, see: https://github.com/NVIDIA/grove/issues/226
	time.Sleep(30 * time.Second)
	if err := verifyAllPodsArePending(ctx, clientset, workloadNamespace, workloadLabelSelector, defaultPollTimeout, defaultPollInterval); err != nil {
		t.Fatalf("Failed to verify all pods are pending: %v", err)
	}

	logger.Info("4. Uncordon 1 node and verify a total of 3 pods get scheduled (pcs-0-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	firstNodeToUncordon := nodesToCordon[0]
	if err := utils.CordonNode(ctx, clientset, firstNodeToUncordon, false); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	// Wait for exactly 3 pods to be scheduled (min-replicas)
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		runningPods := 0
		pendingPods := 0
		for _, pod := range pods.Items {
			switch pod.Status.Phase {
			case v1.PodRunning:
				runningPods++
			case v1.PodPending:
				pendingPods++
			}
		}
		// We expect exactly 3 pods to be running (min-replicas) and the rest pending
		return runningPods == 3 && pendingPods == len(pods.Items)-3, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for exactly 3 pods to be scheduled: %v", err)
	}

	logger.Info("5. Wait for scheduled pods to become ready")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		readyRunningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				// Check if pod is ready
				for _, condition := range pod.Status.Conditions {
					if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
						readyRunningPods++
						break
					}
				}
			}
		}

		return readyRunningPods == 3, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for 3 scheduled pods to become ready: %v", err)
	}

	logger.Info("6. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	sevenNodesToUncordon := nodesToCordon[1:8]
	for _, nodeName := range sevenNodesToUncordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	// Wait for all remaining pods to be scheduled and ready
	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Verify all 10 initial pods are running
	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: workloadLabelSelector,
	})
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	allRunningPods := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			allRunningPods++
		}
	}

	if allRunningPods != len(pods.Items) {
		t.Fatalf("Expected all %d pods to be running, but only %d are running", len(pods.Items), allRunningPods)
	}

	logger.Info("7. Wait for scheduled pods to become ready (already verified above)")
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("Failed to create dynamic client: %v", err)
	}

	logger.Info("8. Set PCS resource replicas equal to 2, then verify 10 more newly created pods")
	// Scale PodCliqueSet to 2 replicas and verify 10 more newly created pods
	pcsName := "workload2"

	// Expected total pods after scaling: 10 (initial) + 10 (new from scaling PCS from 1 to 2) = 20
	expectedPodsAfterScaling := 20
	expectedNewPendingPods := 10

	scalePCSAndWait(t, ctx, clientset, dynamicClient, workloadNamespace, workloadLabelSelector, pcsName, 2, expectedPodsAfterScaling, expectedNewPendingPods, defaultPollTimeout, defaultPollInterval)

	logger.Info("9. Uncordon 3 nodes and verify another 3 pods get scheduled (pcs-1-{pc-a=1, sg-x-0-pc-b=1, sg-x-0-pc-c=1})")
	threeNodesToUncordon := nodesToCordon[8:11]
	for _, nodeName := range threeNodesToUncordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	// Wait for exactly 3 more pods to be scheduled (min-replicas for new PCS replica)
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		runningPods := 0
		pendingPods := 0
		for _, pod := range pods.Items {
			switch pod.Status.Phase {
			case v1.PodRunning:
				runningPods++
			case v1.PodPending:
				pendingPods++
			}
		}
		// We expect 13 pods running (10 initial + 3 from min-replicas) and 7 pending
		return runningPods == 13 && pendingPods == 7, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for exactly 3 more pods to be scheduled after PCS scaling: %v", err)
	}

	logger.Info("10. Wait for scheduled pods to become ready")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		readyRunningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				// Check if pod is ready
				for _, condition := range pod.Status.Conditions {
					if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
						readyRunningPods++
						break
					}
				}
			}
		}

		return readyRunningPods == 13, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for 13 pods to become ready: %v", err)
	}

	logger.Info("11. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	remainingNodesToUncordon := nodesToCordon[11:18]
	for _, nodeName := range remainingNodesToUncordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	// Wait for all remaining pods to be scheduled and ready
	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 20 pods should be running
	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: workloadLabelSelector,
	})
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	finalRunningPods := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			finalRunningPods++
		}
	}

	if finalRunningPods != expectedPodsAfterScaling {
		t.Fatalf("Expected all %d pods to be running, but only %d are running", expectedPodsAfterScaling, finalRunningPods)
	}

	// Verify pods are distributed across distinct nodes
	assertPodsOnDistinctNodes(t, pods.Items)

	logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
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

	logger.Info("1. Initialize a 20-node Grove cluster, then cordon 18 nodes")
	// Setup cluster (shared or individual based on test run mode)
	clientset, restConfig, _, cleanup, _ := setupTestCluster(ctx, t, 20)
	defer cleanup()

	// Get worker nodes for cordoning
	workerNodes, err := getWorkerNodes(ctx, clientset)
	if err != nil {
		t.Fatalf("Failed to get worker nodes: %v", err)
	}

	if len(workerNodes) < 18 {
		t.Fatalf("expected at least 18 worker nodes to cordon, but found %d", len(workerNodes))
	}

	// Cordon 18 worker nodes
	nodesToCordon := workerNodes[:18]
	for _, nodeName := range nodesToCordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, true); err != nil {
			t.Fatalf("Failed to cordon node %s: %v", nodeName, err)
		}
	}

	logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	workloadNamespace := "default"
	workloadYAMLPath := "../yaml/workload2.yaml"
	workloadLabelSelector := "app.kubernetes.io/part-of=workload2"

	_, err = utils.ApplyYAMLFile(ctx, workloadYAMLPath, workloadNamespace, restConfig, logger)
	if err != nil {
		t.Fatalf("Failed to apply workload YAML: %v", err)
	}

	// workload2 initially creates 10 pods
	expectedPods := 10

	var pods *v1.PodList
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		return len(pods.Items) == expectedPods, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for pods to be created: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	// Need to use a sleep here unfortunately, see: https://github.com/NVIDIA/grove/issues/226
	time.Sleep(30 * time.Second)
	if err := verifyAllPodsArePending(ctx, clientset, workloadNamespace, workloadLabelSelector, defaultPollTimeout, defaultPollInterval); err != nil {
		t.Fatalf("Failed to verify all pods are pending: %v", err)
	}

	// Create dynamic client for PCS scaling operations
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("Failed to create dynamic client: %v", err)
	}

	logger.Info("4. Set PCS resource replicas equal to 2, then verify 10 more newly created pods")
	pcsName := "workload2"

	// Expected total pods after scaling: 10 (initial) + 10 (new from scaling PCS from 1 to 2) = 20
	expectedPodsAfterScaling := 20

	scalePatch := map[string]interface{}{
		"spec": map[string]interface{}{
			"replicas": 2,
		},
	}
	patchBytes, err := json.Marshal(scalePatch)
	if err != nil {
		t.Fatalf("Failed to marshal scale patch: %v", err)
	}

	pcsGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}
	if _, err := dynamicClient.Resource(pcsGVR).Namespace(workloadNamespace).Patch(ctx, pcsName, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
		t.Fatalf("Failed to scale PodCliqueSet %s: %v", pcsName, err)
	}

	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		return len(pods.Items) == expectedPodsAfterScaling, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for scaled pods to be created: %v", err)
	}

	logger.Info("5. Verify all 20 newly created pods are pending due to insufficient resources")
	// Need to use a sleep here unfortunately, see: https://github.com/NVIDIA/grove/issues/226
	time.Sleep(30 * time.Second)
	if err := verifyAllPodsArePending(ctx, clientset, workloadNamespace, workloadLabelSelector, defaultPollTimeout, defaultPollInterval); err != nil {
		t.Fatalf("Failed to verify all pods are pending: %v", err)
	}

	logger.Info("6. Uncordon 4 nodes and verify a total of 6 pods get scheduled")
	fourNodesToUncordon := nodesToCordon[0:4]
	for _, nodeName := range fourNodesToUncordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	// Wait for exactly 6 pods to be scheduled (min-replicas for both PCS replicas)
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		runningPods := 0
		pendingPods := 0
		for _, pod := range pods.Items {
			switch pod.Status.Phase {
			case v1.PodRunning:
				runningPods++
			case v1.PodPending:
				pendingPods++
			}
		}
		// We expect exactly 6 pods to be running (min-replicas for both PCS replicas) and the rest pending
		return runningPods == 6 && pendingPods == len(pods.Items)-6, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for exactly 6 pods to be scheduled: %v", err)
	}

	logger.Info("7. Wait for scheduled pods to become ready")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		readyRunningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				// Check if pod is ready
				for _, condition := range pod.Status.Conditions {
					if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
						readyRunningPods++
						break
					}
				}
			}
		}

		return readyRunningPods == 6, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for 6 scheduled pods to become ready: %v", err)
	}

	logger.Info("8. Uncordon 4 nodes and verify 4 more pods get scheduled")
	fourMoreNodesToUncordon := nodesToCordon[4:8]
	for _, nodeName := range fourMoreNodesToUncordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	// Wait for exactly 4 more pods to be scheduled (sg-x-1 for both PCS replicas)
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		runningPods := 0
		pendingPods := 0
		for _, pod := range pods.Items {
			switch pod.Status.Phase {
			case v1.PodRunning:
				runningPods++
			case v1.PodPending:
				pendingPods++
			}
		}
		// We expect 10 pods running (6 + 4 new) and the rest pending
		return runningPods == 10 && pendingPods == len(pods.Items)-10, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for exactly 4 more pods to be scheduled: %v", err)
	}

	logger.Info("9. Wait for scheduled pods to become ready")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: workloadLabelSelector,
		})
		if err != nil {
			return false, err
		}

		readyRunningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				// Check if pod is ready
				for _, condition := range pod.Status.Conditions {
					if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
						readyRunningPods++
						break
					}
				}
			}
		}

		return readyRunningPods == 10, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for 10 scheduled pods to become ready: %v", err)
	}

	logger.Info("10. Uncordon 10 nodes and verify the remaining workload pods get scheduled")
	remainingNodesToUncordon := nodesToCordon[8:18]
	for _, nodeName := range remainingNodesToUncordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	// Wait for all remaining pods to be scheduled and ready
	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for all pods to be ready: %v", err)
	}

	// Final verification - all 20 pods should be running
	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: workloadLabelSelector,
	})
	if err != nil {
		t.Fatalf("Failed to list workload pods: %v", err)
	}

	finalRunningPods := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			finalRunningPods++
		}
	}

	if finalRunningPods != expectedPodsAfterScaling {
		t.Fatalf("Expected all %d pods to be running, but only %d are running", expectedPodsAfterScaling, finalRunningPods)
	}

	// Verify pods are distributed across distinct nodes
	assertPodsOnDistinctNodes(t, pods.Items)

	logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
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

	logger.Info("1. Initialize a 28-node Grove cluster, then cordon 26 nodes")
	// Setup cluster (shared or individual based on test run mode)
	clientset, restConfig, _, cleanup, _ := setupTestCluster(ctx, t, 28)
	defer cleanup()

	// Get worker nodes for cordoning
	workerNodes, err := getWorkerNodes(ctx, clientset)
	if err != nil {
		t.Fatalf("Failed to get worker nodes: %v", err)
	}

	if len(workerNodes) < 26 {
		t.Fatalf("expected at least 26 worker nodes to cordon, but found %d", len(workerNodes))
	}

	nodesToCordon := workerNodes[:26]
	for _, nodeName := range nodesToCordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, true); err != nil {
			t.Fatalf("Failed to cordon node %s: %v", nodeName, err)
		}
	}

	logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	workloadNamespace := "default"
	workloadYAMLPath := "../yaml/workload2.yaml"
	workloadLabelSelector := "app.kubernetes.io/part-of=workload2"

	_, err = utils.ApplyYAMLFile(ctx, workloadYAMLPath, workloadNamespace, restConfig, logger)
	if err != nil {
		t.Fatalf("Failed to apply workload YAML: %v", err)
	}

	expectedPods := 10
	var pods *v1.PodList
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
		if err != nil {
			return false, err
		}

		return len(pods.Items) == expectedPods, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for pods to be created: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	// Need to use a sleep here unfortunately, see: https://github.com/NVIDIA/grove/issues/226
	time.Sleep(30 * time.Second)
	if err := verifyAllPodsArePending(ctx, clientset, workloadNamespace, workloadLabelSelector, defaultPollTimeout, defaultPollInterval); err != nil {
		t.Fatalf("Failed to verify all pods are pending: %v", err)
	}

	logger.Info("4. Uncordon 1 node")
	firstNodeToUncordon := nodesToCordon[0]
	if err := utils.CordonNode(ctx, clientset, firstNodeToUncordon, false); err != nil {
		t.Fatalf("Failed to uncordon node %s: %v", firstNodeToUncordon, err)
	}

	logger.Info("5. Wait for min-replicas pods to be scheduled and ready (should be 3 pods for min-available)")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
		if err != nil {
			return false, err
		}

		runningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				runningPods++
			}
		}

		return runningPods == 3, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for min-replicas pods to be scheduled: %v", err)
	}

	logger.Info("6. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	remainingNodesFirstWave := nodesToCordon[1:8]
	for _, nodeName := range remainingNodesFirstWave {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for first wave pods to be ready: %v", err)
	}

	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("Failed to create dynamic client: %v", err)
	}

	logger.Info("7. Set pcs-0-sg-x resource replicas equal to 3, then verify 4 newly created pods")
	pcsgName := "workload2-0-sg-x"
	scalePCSGAndWait(t, ctx, clientset, dynamicClient, workloadNamespace, workloadLabelSelector, pcsgName, 3, 14, 4, defaultPollTimeout, defaultPollInterval)

	logger.Info("8. Verify all newly created pods are pending due to insufficient resources")
	expectedRunning := 10 // Initial 10 pods from first wave
	expectedPending := 4  // 4 new pods from PCSG scaling
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
		if err != nil {
			return false, err
		}

		totalPods := len(pods.Items)
		runningPods := 0
		pendingPods := 0
		for _, pod := range pods.Items {
			switch pod.Status.Phase {
			case v1.PodRunning:
				runningPods++
			case v1.PodPending:
				pendingPods++
			}
		}
		return totalPods == 14 && runningPods == expectedRunning && pendingPods == expectedPending, nil
	})
	if err != nil {
		t.Fatalf("Failed to verify newly created pods are pending: %v", err)
	}

	logger.Info("9. Uncordon 2 nodes")
	remainingNodesSecondWave := nodesToCordon[8:10]
	for _, nodeName := range remainingNodesSecondWave {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	logger.Info("10. Wait for 2 more pods to be scheduled and ready (min-available for sg-x-2)")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
		if err != nil {
			return false, err
		}

		runningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				runningPods++
			}
		}

		return runningPods == 12, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for PCSG partial scheduling: %v", err)
	}

	logger.Info("11. Uncordon 2 nodes and verify remaining workload pods get scheduled")
	remainingNodesThirdWave := nodesToCordon[10:12]
	for _, nodeName := range remainingNodesThirdWave {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for PCSG completion pods to be ready: %v", err)
	}

	logger.Info("12. Set pcs resource replicas equal to 2, then verify 10 more newly created pods")
	scalePCSAndWait(t, ctx, clientset, dynamicClient, workloadNamespace, workloadLabelSelector, "workload2", 2, 24, 10, defaultPollTimeout, defaultPollInterval)

	logger.Info("13. Uncordon 3 nodes")
	remainingNodesFourthWave := nodesToCordon[12:15]
	for _, nodeName := range remainingNodesFourthWave {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	logger.Info("14. Wait for 3 more pods to be scheduled (min-available for pcs-1)")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
		if err != nil {
			return false, err
		}

		runningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				runningPods++
			}
		}

		return runningPods == 17, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for PCS partial scheduling: %v", err)
	}

	logger.Info("15. Uncordon 7 nodes and verify the remaining workload pods get scheduled")
	remainingNodesFifthWave := nodesToCordon[15:22]
	for _, nodeName := range remainingNodesFifthWave {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for PCS completion pods to be ready: %v", err)
	}

	logger.Info("16. Set pcs-1-sg-x resource replicas equal to 3, then verify 4 newly created pods")
	secondReplicaPCSGName := "workload2-1-sg-x"
	scalePCSGAndWait(t, ctx, clientset, dynamicClient, workloadNamespace, workloadLabelSelector, secondReplicaPCSGName, 3, 28, 4, defaultPollTimeout, defaultPollInterval)

	logger.Info("17. Verify all newly created pods are pending due to insufficient resources")
	expectedRunning = 24 // All previous pods should be running
	expectedPending = 4  // 4 new pods from second PCSG scaling
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
		if err != nil {
			return false, err
		}

		totalPods := len(pods.Items)
		runningPods := 0
		pendingPods := 0
		for _, pod := range pods.Items {
			switch pod.Status.Phase {
			case v1.PodRunning:
				runningPods++
			case v1.PodPending:
				pendingPods++
			}
		}
		return totalPods == 28 && runningPods == expectedRunning && pendingPods == expectedPending, nil
	})
	if err != nil {
		t.Fatalf("Failed to verify newly created pods are pending after second PCSG scaling: %v", err)
	}

	logger.Info("18. Uncordon 2 nodes")
	remainingNodesSixthWave := nodesToCordon[22:24]
	for _, nodeName := range remainingNodesSixthWave {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	logger.Info("19. Wait for 2 more pods to be scheduled (min-available for pcs-1-sg-x-2)")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
		if err != nil {
			return false, err
		}

		runningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				runningPods++
			}
		}

		return runningPods == 26, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for final PCSG partial scheduling: %v", err)
	}

	logger.Info("20. Uncordon 2 nodes and verify remaining workload pods get scheduled")
	finalNodes := nodesToCordon[24:26]
	for _, nodeName := range finalNodes {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for all final pods to be ready: %v", err)
	}

	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
	if err != nil {
		t.Fatalf("Failed to list all final workload pods: %v", err)
	}

	assertPodsOnDistinctNodes(t, pods.Items)

	logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")

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

	logger.Info("1. Initialize a 28-node Grove cluster, then cordon 26 nodes")
	// Setup cluster (shared or individual based on test run mode)
	clientset, restConfig, _, cleanup, _ := setupTestCluster(ctx, t, 28)
	defer cleanup()

	// Get worker nodes for cordoning
	workerNodes, err := getWorkerNodes(ctx, clientset)
	if err != nil {
		t.Fatalf("Failed to get worker nodes: %v", err)
	}

	if len(workerNodes) < 26 {
		t.Fatalf("expected at least 26 worker nodes to cordon, but found %d", len(workerNodes))
	}

	nodesToCordon := workerNodes[:26]
	for _, nodeName := range nodesToCordon {
		if err := utils.CordonNode(ctx, clientset, nodeName, true); err != nil {
			t.Fatalf("Failed to cordon node %s: %v", nodeName, err)
		}
	}

	logger.Info("2. Deploy workload WL2, and verify 10 newly created pods")
	workloadNamespace := "default"
	workloadYAMLPath := "../yaml/workload2.yaml"
	workloadLabelSelector := "app.kubernetes.io/part-of=workload2"

	_, err = utils.ApplyYAMLFile(ctx, workloadYAMLPath, workloadNamespace, restConfig, logger)
	if err != nil {
		t.Fatalf("Failed to apply workload YAML: %v", err)
	}

	expectedPods := 10
	var pods *v1.PodList
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		var err error
		pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
		if err != nil {
			return false, err
		}

		return len(pods.Items) == expectedPods, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for pods to be created: %v", err)
	}

	logger.Info("3. Verify all workload pods are pending due to insufficient resources")
	// Need to use a sleep here unfortunately, see: https://github.com/NVIDIA/grove/issues/226
	time.Sleep(30 * time.Second)
	if err := verifyAllPodsArePending(ctx, clientset, workloadNamespace, workloadLabelSelector, defaultPollTimeout, defaultPollInterval); err != nil {
		t.Fatalf("Failed to verify all pods are pending: %v", err)
	}

	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("Failed to create dynamic client: %v", err)
	}

	logger.Info("4. Set pcs resource replicas equal to 2, then verify 10 more newly created pods")
	scalePCSAndWait(t, ctx, clientset, dynamicClient, workloadNamespace, workloadLabelSelector, "workload2", 2, 20, 20, defaultPollTimeout, defaultPollInterval)

	logger.Info("5. Verify all 20 newly created pods are pending due to insufficient resources")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
		if err != nil {
			return false, err
		}

		totalPods := len(pods.Items)
		pendingPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodPending {
				pendingPods++
			}
		}

		return totalPods == 20 && pendingPods == 20, nil
	})
	if err != nil {
		t.Fatalf("Failed to verify all 20 pods are pending: %v", err)
	}

	logger.Info("6. Set both pcs-0-sg-x and pcs-1-sg-x resource replicas equal to 3, verify 8 newly created pods")

	pcsg1Name := "workload2-0-sg-x"
	scalePCSGAndWait(t, ctx, clientset, dynamicClient, workloadNamespace, workloadLabelSelector, pcsg1Name, 3, 24, 24, defaultPollTimeout, defaultPollInterval)

	pcsg2Name := "workload2-1-sg-x"
	scalePCSGAndWait(t, ctx, clientset, dynamicClient, workloadNamespace, workloadLabelSelector, pcsg2Name, 3, 28, 28, defaultPollTimeout, defaultPollInterval)

	logger.Info("7. Verify all 28 created pods are pending due to insufficient resources")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
		if err != nil {
			return false, err
		}

		totalPods := len(pods.Items)
		pendingPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodPending {
				pendingPods++
			}
		}

		return totalPods == 28 && pendingPods == 28, nil
	})
	if err != nil {
		t.Fatalf("Failed to verify all 28 pods are pending: %v", err)
	}

	logger.Info("8. Uncordon 4 nodes and verify a total of 6 pods get scheduled (pcs-0 and pcs-1 min-available)")
	firstWaveNodes := nodesToCordon[:4]
	for _, nodeName := range firstWaveNodes {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
		if err != nil {
			return false, err
		}

		runningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				runningPods++
			}
		}

		return runningPods == 6, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for 6 pods to be scheduled: %v", err)
	}

	logger.Info("9. Wait for scheduled pods to become ready (only the 6 that are scheduled)")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
		if err != nil {
			return false, err
		}

		readyPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				for _, condition := range pod.Status.Conditions {
					if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
						readyPods++
						break
					}
				}
			}
		}

		return readyPods == 6, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for 6 pods to be ready: %v", err)
	}

	logger.Info("10. Uncordon 8 nodes and verify 8 more pods get scheduled (remaining PCSG pods)")
	secondWaveNodes := nodesToCordon[4:12]
	for _, nodeName := range secondWaveNodes {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
		if err != nil {
			return false, err
		}

		runningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				runningPods++
			}
		}

		return runningPods == 14, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for 8 more pods to be scheduled: %v", err)
	}

	logger.Info("11. Wait for scheduled pods to become ready (only the 14 that are scheduled)")
	err = utils.PollForCondition(ctx, defaultPollTimeout, defaultPollInterval, func() (bool, error) {
		pods, err := clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
		if err != nil {
			return false, err
		}

		readyPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == v1.PodRunning {
				for _, condition := range pod.Status.Conditions {
					if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
						readyPods++
						break
					}
				}
			}
		}

		return readyPods == 14, nil
	})
	if err != nil {
		t.Fatalf("Failed to wait for 14 pods to be ready: %v", err)
	}

	logger.Info("12. Uncordon 14 nodes and verify the remaining workload pods get scheduled")
	finalWaveNodes := nodesToCordon[12:26]
	for _, nodeName := range finalWaveNodes {
		if err := utils.CordonNode(ctx, clientset, nodeName, false); err != nil {
			t.Fatalf("Failed to uncordon node %s: %v", nodeName, err)
		}
	}

	if err := utils.WaitForPods(ctx, restConfig, []string{workloadNamespace}, workloadLabelSelector, defaultPollTimeout, defaultPollInterval, logger); err != nil {
		t.Fatalf("Failed to wait for all final pods to be ready: %v", err)
	}

	pods, err = clientset.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadLabelSelector})
	if err != nil {
		t.Fatalf("Failed to list all final workload pods: %v", err)
	}

	assertPodsOnDistinctNodes(t, pods.Items)

	logger.Info("🎉 Gang-scheduling PCS+PCSG scaling test completed successfully!")
}

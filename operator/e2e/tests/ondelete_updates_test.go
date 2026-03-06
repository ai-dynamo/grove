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
	"testing"
	"time"

	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
)

// OnDeleteTestConfig holds configuration for OnDelete update strategy test setup
type OnDeleteTestConfig struct {
	// Required
	WorkerNodes  int // Number of worker nodes required for the test
	ExpectedPods int // Expected pods after initial deployment

	// Optional - PCS scaling before tracker starts
	InitialPCSReplicas int32 // If > 0, scale PCS to this many replicas before starting tracker
	PostScalePods      int   // Expected pods after initial PCS scaling (required if InitialPCSReplicas > 0)

	// Optional - PCSG scaling
	InitialPCSGReplicas int32  // If > 0, scale PCSGs to this many replicas
	PCSGName            string // Name of the PCSG scaling group (e.g., "sg-x")
	PostPCSGScalePods   int    // Expected pods after PCSG scaling

	// Optional - defaults can be overridden
	WorkloadName string // Defaults to "workload-ondelete"
	WorkloadYAML string // Defaults to "../yaml/workload-ondelete.yaml"
	Namespace    string // Defaults to "default"
}

// setupOnDeleteTest initializes an OnDelete update strategy test with the given configuration.
// It handles:
// 1. Cluster preparation with required worker nodes
// 2. TestContext creation with standard parameters
// 3. Workload deployment and pod verification (OnDelete strategy)
// 4. Optional PCS scaling to initial replicas
// 5. Optional PCSG scaling
// 6. Tracker creation and startup
//
// Returns:
//   - tc: TestContext for the test
//   - cleanup: Function that should be deferred by the caller (stops tracker and cleans up cluster)
//   - tracker: Started rolling update tracker - caller can use tracker.getEvents() after stopping
func setupOnDeleteTest(t *testing.T, cfg OnDeleteTestConfig) (TestContext, func(), *rollingUpdateTracker) {
	t.Helper()
	ctx := context.Background()

	// Apply defaults
	if cfg.WorkloadName == "" {
		cfg.WorkloadName = "workload-ondelete"
	}
	if cfg.WorkloadYAML == "" {
		cfg.WorkloadYAML = "../yaml/workload-ondelete.yaml"
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}

	// Step 1: Prepare test cluster
	clientset, restConfig, dynamicClient, clusterCleanup := prepareTestCluster(ctx, t, cfg.WorkerNodes)

	// Step 2: Create TestContext
	tc := TestContext{
		T:             t,
		Ctx:           ctx,
		Clientset:     clientset,
		RestConfig:    restConfig,
		DynamicClient: dynamicClient,
		Namespace:     cfg.Namespace,
		Timeout:       defaultPollTimeout,
		Interval:      defaultPollInterval,
		Workload: &WorkloadConfig{
			Name:         cfg.WorkloadName,
			YAMLPath:     cfg.WorkloadYAML,
			Namespace:    cfg.Namespace,
			ExpectedPods: cfg.ExpectedPods,
		},
	}

	// Step 3: Deploy workload and verify initial pods
	pods, err := deployAndVerifyWorkload(tc)
	if err != nil {
		clusterCleanup()
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	if err := waitForPods(tc, cfg.ExpectedPods); err != nil {
		clusterCleanup()
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	if len(pods.Items) != cfg.ExpectedPods {
		clusterCleanup()
		t.Fatalf("Expected %d pods, but found %d", cfg.ExpectedPods, len(pods.Items))
	}

	// Step 4: Optional PCS scaling
	if cfg.InitialPCSReplicas > 0 {
		scalePCSAndWait(tc, cfg.WorkloadName, cfg.InitialPCSReplicas, cfg.PostScalePods, 0)

		if err := waitForPods(tc, cfg.PostScalePods); err != nil {
			clusterCleanup()
			t.Fatalf("Failed to wait for pods to be ready after PCS scaling: %v", err)
		}
	}

	// Step 5: Optional PCSG scaling
	if cfg.InitialPCSGReplicas > 0 && cfg.PCSGName != "" {
		// Scale across all PCS replicas
		pcsReplicas := int32(1)
		if cfg.InitialPCSReplicas > 0 {
			pcsReplicas = cfg.InitialPCSReplicas
		}
		scalePCSGAcrossAllReplicasAndWait(tc, cfg.WorkloadName, cfg.PCSGName, pcsReplicas, cfg.InitialPCSGReplicas, cfg.PostPCSGScalePods, 0)
	}

	// Step 6: Create and start tracker
	tracker := newRollingUpdateTracker()
	if err := tracker.Start(tc); err != nil {
		clusterCleanup()
		t.Fatalf("Failed to start tracker: %v", err)
	}

	// Create combined cleanup function
	// Note: clusterCleanup already handles diagnostics collection on failure
	cleanup := func() {
		tracker.Stop()
		clusterCleanup()
	}

	return tc, cleanup, tracker
}

// triggerOnDeletePodCliqueUpdate triggers an update on a PodClique by modifying an environment variable.
// Unlike RollingRecreate, this should NOT cause automatic pod deletion.
func triggerOnDeletePodCliqueUpdate(tc TestContext, cliqueName string) error {
	return triggerPodCliqueUpdate(tc, cliqueName)
}

// waitForOnDeleteUpdateComplete waits for OnDelete update to be marked complete.
// For OnDelete, the update is marked complete immediately after the spec is synced,
// even though pods are not yet recreated.
func waitForOnDeleteUpdateComplete(tc TestContext, expectedReplicas int32) error {
	pcsGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}
	pcsName := tc.Workload.Name

	pollCount := 0
	return pollForCondition(tc, func() (bool, error) {
		pollCount++
		unstructuredPCS, err := tc.DynamicClient.Resource(pcsGVR).Namespace(tc.Namespace).Get(tc.Ctx, pcsName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		var pcs grovev1alpha1.PodCliqueSet
		err = utils.ConvertUnstructuredToTyped(unstructuredPCS.Object, &pcs)
		if err != nil {
			return false, err
		}

		// Log status every few polls for debugging
		if pollCount%3 == 1 {
			logger.Debugf("[waitForOnDeleteUpdateComplete] Poll #%d: UpdatedReplicas=%d, expectedReplicas=%d, UpdateProgress=%v",
				pollCount, pcs.Status.UpdatedReplicas, expectedReplicas, pcs.Status.UpdateProgress != nil)
			if pcs.Status.UpdateProgress != nil {
				logger.Debugf("  UpdateStartedAt=%v, UpdateEndedAt=%v",
					pcs.Status.UpdateProgress.UpdateStartedAt,
					pcs.Status.UpdateProgress.UpdateEndedAt)
			}
		}

		// For OnDelete strategy, the update is complete when:
		// - UpdateProgress exists with UpdateEndedAt set (both timestamps are set simultaneously for OnDelete)
		// Note: UpdatedReplicas will NOT match expected replicas immediately since pods aren't recreated
		if pcs.Status.UpdateProgress != nil && pcs.Status.UpdateProgress.UpdateEndedAt != nil {
			logger.Debugf("[waitForOnDeleteUpdateComplete] OnDelete update marked complete after %d polls (UpdatedReplicas=%d)",
				pollCount, pcs.Status.UpdatedReplicas)
			return true, nil
		}

		return false, nil
	})
}

// verifyNoPodsDeleted verifies that no pods were deleted during the observation period.
// This is crucial for OnDelete strategy - pods should NOT be automatically deleted when spec changes.
func verifyNoPodsDeleted(tc TestContext, events []podEvent, existingPodNames map[string]bool) {
	tc.T.Helper()

	deletedPods := []string{}
	for _, event := range events {
		if event.Type == watch.Deleted && existingPodNames[event.Pod.Name] {
			deletedPods = append(deletedPods, event.Pod.Name)
		}
	}

	if len(deletedPods) > 0 {
		tc.T.Fatalf("OnDelete strategy violated: pods were automatically deleted when spec changed: %v", deletedPods)
	}
}

// verifyPodHasUpdatedSpec verifies that a pod has the updated environment variable.
func verifyPodHasUpdatedSpec(tc TestContext, podName string) error {
	tc.T.Helper()

	pod, err := tc.Clientset.CoreV1().Pods(tc.Namespace).Get(tc.Ctx, podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get pod %s: %w", podName, err)
	}

	// Check if the pod has the UPDATE_TRIGGER env var
	for _, container := range pod.Spec.Containers {
		for _, env := range container.Env {
			if env.Name == "UPDATE_TRIGGER" {
				return nil // Pod has the updated spec
			}
		}
	}

	return fmt.Errorf("pod %s does not have the UPDATE_TRIGGER environment variable", podName)
}

// deletePodAndWaitForTermination deletes a pod and waits for its replacement to be ready.
func deletePodAndWaitForTermination(tc TestContext, podName string) error {
	tc.T.Helper()

	// Delete the pod
	err := tc.Clientset.CoreV1().Pods(tc.Namespace).Delete(tc.Ctx, podName, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete pod %s: %w", podName, err)
	}

	err = pollForCondition(tc, func() (bool, error) {
		pods, err := listPods(tc)
		if err != nil {
			return false, err
		}
		for _, pod := range pods.Items {
			if pod.Name == podName {
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("failed to wait for the pod %s to be terminated: %w", podName, err)
	}

	logger.Debugf("Deleted pod %s, waiting for replacement...", podName)

	return nil
}

// getPodsForClique returns pods belonging to a specific PodClique.
func getPodsForClique(tc TestContext, cliqueName string) ([]string, error) {
	tc.T.Helper()

	pods, err := listPods(tc)
	if err != nil {
		return nil, err
	}

	var cliquePods []string
	for _, pod := range pods.Items {
		if pod.Labels != nil {
			if pclq, ok := pod.Labels["grove.io/podclique"]; ok {
				// PodClique name format: {pcsName}-{replicaIndex}-{cliqueName}
				// We need to check if the clique name suffix matches
				if len(pclq) > len(cliqueName) && pclq[len(pclq)-len(cliqueName):] == cliqueName {
					cliquePods = append(cliquePods, pod.Name)
				}
			}
		}
	}

	return cliquePods, nil
}

// Test_OD1_NoAutomaticDeletionOnSpecChange tests that OnDelete strategy does not automatically delete pods when spec changes.
// Scenario OD-1 (from proposal Testcase 1):
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload with OnDelete strategy, and verify 10 newly created pods
// 3. Change the specification of pc-a
// 4. Verify that NO pods are automatically deleted
// 5. Verify that the update is marked complete (UpdateEndedAt is set)
func Test_OD1_NoAutomaticDeletionOnSpecChange(t *testing.T) {
	logger.Info("1. Initialize a 10-node Grove cluster")
	logger.Info("2. Deploy workload with OnDelete strategy, and verify 10 newly created pods")
	tc, cleanup, tracker := setupOnDeleteTest(t, OnDeleteTestConfig{
		WorkerNodes:  10,
		ExpectedPods: 10,
	})
	defer cleanup()

	// Capture existing pods before triggering update
	existingPods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list existing pods: %v", err)
	}

	existingPodNames := make(map[string]bool)
	for _, pod := range existingPods.Items {
		existingPodNames[pod.Name] = true
	}
	logger.Debugf("Captured %d existing pods before spec change", len(existingPodNames))

	logger.Info("3. Change the specification of pc-a")
	if err := triggerOnDeletePodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	logger.Info("4. Verify that NO pods are automatically deleted")
	// Wait a reasonable time for any automatic deletion to occur (it shouldn't)
	time.Sleep(10 * time.Second)

	// Verify no pods were deleted
	tracker.Stop()
	events := tracker.getEvents()
	verifyNoPodsDeleted(tc, events, existingPodNames)

	logger.Info("5. Verify that the update is marked complete (UpdateEndedAt is set)")
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 1 * time.Minute
	if err := waitForOnDeleteUpdateComplete(tcLongTimeout, 1); err != nil {
		t.Fatalf("Failed to verify OnDelete update completion: %v", err)
	}

	// Verify all original pods are still running
	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}

	if len(pods.Items) != 10 {
		t.Fatalf("Expected 10 pods to still be running, but got %d", len(pods.Items))
	}

	// Verify original pods are unchanged
	for _, pod := range pods.Items {
		if !existingPodNames[pod.Name] {
			t.Fatalf("Unexpected new pod %s found - pods should not have been recreated", pod.Name)
		}
	}

	logger.Info("OnDelete Update - No Automatic Deletion test (OD-1) completed successfully!")
}

// Test_OD2_ManualDeletionCreatesUpdatedPod tests that manually deleted pods are recreated with the new spec.
// Scenario OD-2 (from proposal Testcase 1):
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload with OnDelete strategy, and verify 10 newly created pods
// 3. Change the specification of pc-a
// 4. Delete a pod manually and verify the replacement uses the new template
// 5. Verify status fields accurately reflect the update state
func Test_OD2_ManualDeletionCreatesUpdatedPod(t *testing.T) {
	logger.Info("1. Initialize a 10-node Grove cluster")
	logger.Info("2. Deploy workload with OnDelete strategy, and verify 10 newly created pods")
	tc, cleanup, _ := setupOnDeleteTest(t, OnDeleteTestConfig{
		WorkerNodes:  10,
		ExpectedPods: 10,
	})
	defer cleanup()

	// Capture existing pods before triggering update
	existingPods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list existing pods: %v", err)
	}

	// Get pods belonging to pc-a
	pcaPods, err := getPodsForClique(tc, "pc-a")
	if err != nil {
		t.Fatalf("Failed to get pods for pc-a: %v", err)
	}

	if len(pcaPods) == 0 {
		t.Fatalf("No pods found for pc-a")
	}

	podToDelete := pcaPods[0]
	logger.Debugf("Will delete pod %s after spec change", podToDelete)

	logger.Info("3. Change the specification of pc-a")
	if err := triggerOnDeletePodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	// Wait for update to be marked complete
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 1 * time.Minute
	if err := waitForOnDeleteUpdateComplete(tcLongTimeout, 1); err != nil {
		t.Fatalf("Failed to verify OnDelete update completion: %v", err)
	}

	logger.Info("4. Delete a pod manually and verify the replacement uses the new template")
	// Capture existing pod names before deletion
	existingPodNames := make(map[string]bool)
	for _, pod := range existingPods.Items {
		existingPodNames[pod.Name] = true
	}

	// Delete the pod
	if err := deletePodAndWaitForTermination(tc, podToDelete); err != nil {
		t.Fatalf("Failed to delete pod and wait for replacement: %v", err)
	}

	if err := waitForRunningPods(tc, 10); err != nil {
		t.Fatalf("Failed to wait for pod with new specification to be created")
	}

	// Get the new pod list
	newPods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods after deletion: %v", err)
	}

	// Find the new pod (one that wasn't in the original list)
	var newPodName string
	for _, pod := range newPods.Items {
		if !existingPodNames[pod.Name] {
			newPodName = pod.Name
			break
		}
	}

	if newPodName == "" {
		t.Fatalf("Could not find newly created replacement pod")
	}

	logger.Debugf("New replacement pod: %s", newPodName)

	// Verify the new pod has the updated spec
	if err := verifyPodHasUpdatedSpec(tc, newPodName); err != nil {
		t.Fatalf("Replacement pod does not have updated spec: %v", err)
	}

	logger.Info("5. Verify status fields accurately reflect the update state")
	// Get PCLQ and verify UpdatedReplicas has increased
	pclqName := fmt.Sprintf("%s-%d-%s", tc.Workload.Name, 0, "pc-a")
	pclqGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliques"}
	unstructuredPCLQ, err := tc.DynamicClient.Resource(pclqGVR).Namespace(tc.Namespace).Get(tc.Ctx, pclqName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get PodCliqueSet: %v", err)
	}

	var pclq grovev1alpha1.PodClique
	if err := utils.ConvertUnstructuredToTyped(unstructuredPCLQ.Object, &pclq); err != nil {
		t.Fatalf("Failed to convert to PodClique: %v", err)
	}

	if pclq.Status.UpdatedReplicas != int32(1) {
		t.Fatalf("Updated replicas has not increased by 1")
	}

	logger.Debugf("PCLQ Status - UpdatedReplicas: %d, Replicas: %d", pclq.Status.UpdatedReplicas, pclq.Status.Replicas)

	logger.Info("OnDelete Update - Manual Deletion Creates Updated Pod test (OD-2) completed successfully!")
}

// Test_OD3_ScaleInPrefersOutdatedPods tests that during scale-in, pods with outdated templates are deleted first.
// Scenario OD-3 (from proposal Testcase 1):
// 1. Initialize a 12-node Grove cluster
// 2. Deploy workload with OnDelete strategy, and verify 10 newly created pods
// 3. Scale out pc-a to 3 replicas (11 total pods)
// 4. Change the specification of pc-a (triggering OnDelete update)
// 5. Manually delete ONE pc-a pod to update it
// 6. Scale in pc-a from 3 to 2 replicas
// 7. Verify pods with outdated templates are deleted first (not the updated one)
func Test_OD3_ScaleInPrefersOutdatedPods(t *testing.T) {
	logger.Info("1. Initialize a 12-node Grove cluster")
	logger.Info("2. Deploy workload with OnDelete strategy, and verify 10 newly created pods")
	tc, cleanup, _ := setupOnDeleteTest(t, OnDeleteTestConfig{
		WorkerNodes:  12,
		ExpectedPods: 10,
	})
	defer cleanup()

	logger.Info("3. Scale out pc-a to 3 replicas (11 total pods)")
	// Scale pc-a from 2 to 3 replicas
	// After scaling pc-a to 3: 3 + 8 = 11 pods
	if err := scalePodCliqueInPCS(tc, "pc-a", 3); err != nil {
		t.Fatalf("Failed to scale out PodClique pc-a: %v", err)
	}

	if err := waitForPods(tc, 11); err != nil {
		t.Fatalf("Failed to wait for pods after pc-a scale-out: %v", err)
	}

	// Get the current pods for pc-a BEFORE the update
	oldPcaPods, err := getPodsForClique(tc, "pc-a")
	if err != nil {
		t.Fatalf("Failed to get pods for pc-a: %v", err)
	}
	logger.Debugf("pc-a pods before update: %v", oldPcaPods)

	if len(oldPcaPods) != 3 {
		t.Fatalf("Expected 3 pc-a pods, got %d", len(oldPcaPods))
	}

	logger.Info("4. Change the specification of pc-a (triggering OnDelete update)")
	if err := triggerOnDeletePodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	// Wait for update to be marked complete
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 1 * time.Minute
	if err := waitForOnDeleteUpdateComplete(tcLongTimeout, 1); err != nil {
		t.Fatalf("Failed to verify OnDelete update completion: %v", err)
	}

	logger.Info("5. Manually delete ONE pc-a pod to update it")
	// Delete the first pod from pc-a to create one with updated spec
	podToUpdate := oldPcaPods[0]
	if err := deletePodAndWaitForTermination(tc, podToUpdate); err != nil {
		t.Fatalf("Failed to delete pod and wait for replacement: %v", err)
	}

	// wait for the new pc-a pod to be created
	if err := waitForReadyPods(tc, 11); err != nil {
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

	logger.Debugf("Updated pod: %s, remaining old pods: %v", updatedPod, oldPcaPods[1:])

	logger.Info("6. Scale in pc-a from 3 to 2 replicas")
	if err := scalePodCliqueInPCS(tc, "pc-a", 2); err != nil {
		t.Fatalf("Failed to scale in PodClique pc-a: %v", err)
	}

	// Wait for scale-in to complete (back to 10 pods)
	if err := waitForPods(tc, 10); err != nil {
		t.Fatalf("Failed to wait for pods after pc-a scale-in: %v", err)
	}

	logger.Info("7. Verify pods with outdated templates are deleted first (not the updated one)")
	// Get the remaining pc-a pods
	remainingPcaPods, err := getPodsForClique(tc, "pc-a")
	if err != nil {
		t.Fatalf("Failed to get remaining pods for pc-a: %v", err)
	}

	if len(remainingPcaPods) != 2 {
		t.Fatalf("Expected 2 pc-a pods after scale-in, got %d", len(remainingPcaPods))
	}

	// Verify the updated pod is still present
	foundUpdatedPod := slices.Contains(remainingPcaPods, updatedPod)

	if !foundUpdatedPod {
		t.Fatalf("The updated pod %s was deleted during scale-in instead of an outdated pod", updatedPod)
	}

	logger.Info("OnDelete Update - Scale-In Prefers Outdated Pods test (OD-3) completed successfully!")
}

// Test_OD4_PCSGNoAutomaticDeletionOnSpecChange tests OnDelete strategy with PodCliqueScalingGroups.
// Scenario OD-4 (from proposal Testcase 3):
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload with OnDelete strategy, and verify 10 newly created pods
// 3. Change the specification of pc-b (part of PCSG sg-x)
// 4. Verify that NO pods/replicas are automatically deleted
// 5. Verify that the update is marked complete
func Test_OD4_PCSGNoAutomaticDeletionOnSpecChange(t *testing.T) {
	logger.Info("1. Initialize a 10-node Grove cluster")
	logger.Info("2. Deploy workload with OnDelete strategy, and verify 10 newly created pods")
	tc, cleanup, tracker := setupOnDeleteTest(t, OnDeleteTestConfig{
		WorkerNodes:  10,
		ExpectedPods: 10,
	})
	defer cleanup()

	// Capture existing pods before triggering update
	existingPods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list existing pods: %v", err)
	}

	existingPodNames := make(map[string]bool)
	for _, pod := range existingPods.Items {
		existingPodNames[pod.Name] = true
	}
	logger.Debugf("Captured %d existing pods before spec change", len(existingPodNames))

	logger.Info("3. Change the specification of pc-b (part of PCSG sg-x)")
	if err := triggerOnDeletePodCliqueUpdate(tc, "pc-b"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	logger.Info("4. Verify that NO pods/replicas are automatically deleted")
	// Wait a reasonable time for any automatic deletion to occur (it shouldn't)
	time.Sleep(10 * time.Second)

	// Verify no pods were deleted
	tracker.Stop()
	events := tracker.getEvents()
	verifyNoPodsDeleted(tc, events, existingPodNames)

	logger.Info("5. Verify that the update is marked complete")
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 1 * time.Minute
	if err := waitForOnDeleteUpdateComplete(tcLongTimeout, 1); err != nil {
		t.Fatalf("Failed to verify OnDelete update completion: %v", err)
	}

	// Verify all original pods are still running
	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}

	if len(pods.Items) != 10 {
		t.Fatalf("Expected 10 pods to still be running, but got %d", len(pods.Items))
	}

	logger.Info("OnDelete Update - PCSG No Automatic Deletion test (OD-4) completed successfully!")
}

// Test_OD5_PCSGManualDeletionCreatesUpdatedReplica tests manual deletion in PCSG with OnDelete strategy.
// Scenario OD-5 (from proposal Testcase 3):
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload with OnDelete strategy, and verify 10 newly created pods
// 3. Change the specification of pc-c (part of PCSG sg-x)
// 4. Delete a pc-c pod manually and verify the replacement uses the new template
func Test_OD5_PCSGManualDeletionCreatesUpdatedReplica(t *testing.T) {
	logger.Info("1. Initialize a 10-node Grove cluster")
	logger.Info("2. Deploy workload with OnDelete strategy, and verify 10 newly created pods")
	tc, cleanup, _ := setupOnDeleteTest(t, OnDeleteTestConfig{
		WorkerNodes:  10,
		ExpectedPods: 10,
	})
	defer cleanup()

	// Capture existing pods before triggering update
	existingPods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list existing pods: %v", err)
	}

	// Get pods belonging to pc-c (part of PCSG)
	pccPods, err := getPodsForClique(tc, "pc-c")
	if err != nil {
		t.Fatalf("Failed to get pods for pc-c: %v", err)
	}

	if len(pccPods) == 0 {
		t.Fatalf("No pods found for pc-c")
	}

	podToDelete := pccPods[0]
	logger.Debugf("Will delete pod %s after spec change", podToDelete)

	logger.Info("3. Change the specification of pc-c (part of PCSG sg-x)")
	if err := triggerOnDeletePodCliqueUpdate(tc, "pc-c"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	// Wait for update to be marked complete
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 1 * time.Minute
	if err := waitForOnDeleteUpdateComplete(tcLongTimeout, 1); err != nil {
		t.Fatalf("Failed to verify OnDelete update completion: %v", err)
	}

	logger.Info("4. Delete a pc-c pod manually and verify the replacement uses the new template")
	// Capture existing pod names before deletion
	existingPodNames := make(map[string]bool)
	for _, pod := range existingPods.Items {
		existingPodNames[pod.Name] = true
	}

	// Delete the pod
	if err := deletePodAndWaitForTermination(tc, podToDelete); err != nil {
		t.Fatalf("Failed to delete pod and wait for replacement: %v", err)
	}

	if err := waitForRunningPods(tc, 10); err != nil {
		t.Fatalf("Failed to wait for pod with new specification to be created")
	}

	// Get the new pod list
	newPods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods after deletion: %v", err)
	}

	// Find the new pod (one that wasn't in the original list)
	var newPodName string
	for _, pod := range newPods.Items {
		if !existingPodNames[pod.Name] {
			newPodName = pod.Name
			break
		}
	}

	if newPodName == "" {
		t.Fatalf("Could not find newly created replacement pod")
	}

	logger.Debugf("New replacement pod: %s", newPodName)

	// Verify the new pod has the updated spec
	if err := verifyPodHasUpdatedSpec(tc, newPodName); err != nil {
		t.Fatalf("Replacement pod does not have updated spec: %v", err)
	}

	logger.Info("OnDelete Update - PCSG Manual Deletion Creates Updated Replica test (OD-5) completed successfully!")
}

// Test_OD6_MixedPodCliquesAndPCSG tests OnDelete strategy with both standalone PodCliques and PodCliqueScalingGroups.
// Scenario OD-6 (from proposal Testcase 4):
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload with OnDelete strategy, and verify 10 newly created pods
// 3. Change the specification of ALL PodCliques (pc-a, pc-b, pc-c)
// 4. Verify that NO pods are automatically deleted
// 5. Manually delete one pod from each type (standalone and PCSG)
// 6. Verify replacements use the new template
func Test_OD6_MixedPodCliquesAndPCSG(t *testing.T) {
	logger.Info("1. Initialize a 10-node Grove cluster")
	logger.Info("2. Deploy workload with OnDelete strategy, and verify 10 newly created pods")
	tc, cleanup, tracker := setupOnDeleteTest(t, OnDeleteTestConfig{
		WorkerNodes:  10,
		ExpectedPods: 10,
	})
	defer cleanup()

	// Capture existing pods before triggering update
	existingPods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list existing pods: %v", err)
	}

	existingPodNames := make(map[string]bool)
	for _, pod := range existingPods.Items {
		existingPodNames[pod.Name] = true
	}

	logger.Info("3. Change the specification of ALL PodCliques (pc-a, pc-b, pc-c)")
	for _, cliqueName := range []string{"pc-a", "pc-b", "pc-c"} {
		if err := triggerOnDeletePodCliqueUpdate(tc, cliqueName); err != nil {
			t.Fatalf("Failed to update PodClique %s spec: %v", cliqueName, err)
		}
	}

	logger.Info("4. Verify that NO pods are automatically deleted")
	// Wait a reasonable time for any automatic deletion to occur (it shouldn't)
	time.Sleep(10 * time.Second)

	// Verify no pods were deleted
	tracker.Stop()
	events := tracker.getEvents()
	verifyNoPodsDeleted(tc, events, existingPodNames)

	// Wait for update to be marked complete
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 1 * time.Minute
	if err := waitForOnDeleteUpdateComplete(tcLongTimeout, 1); err != nil {
		t.Fatalf("Failed to verify OnDelete update completion: %v", err)
	}

	logger.Info("5. Manually delete one pod from each type (standalone and PCSG)")
	// Get a standalone pod (pc-a)
	pcaPods, err := getPodsForClique(tc, "pc-a")
	if err != nil {
		t.Fatalf("Failed to get pods for pc-a: %v", err)
	}

	if len(pcaPods) == 0 {
		t.Fatalf("No pods found for pc-a")
	}

	// Get a PCSG pod (pc-c)
	pccPods, err := getPodsForClique(tc, "pc-c")
	if err != nil {
		t.Fatalf("Failed to get pods for pc-c: %v", err)
	}

	if len(pccPods) == 0 {
		t.Fatalf("No pods found for pc-c")
	}

	// Delete standalone pod
	standalonePodToDelete := pcaPods[0]
	logger.Debugf("Deleting standalone pod: %s", standalonePodToDelete)
	if err := deletePodAndWaitForTermination(tc, standalonePodToDelete); err != nil {
		t.Fatalf("Failed to delete standalone pod and wait for replacement: %v", err)
	}

	if err := waitForRunningPods(tc, 10); err != nil {
		t.Fatalf("Failed to wait for pod with new specification to be created")
	}

	// Refresh existing pod names
	existingPods2, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	existingPodNames2 := make(map[string]bool)
	for _, pod := range existingPods2.Items {
		existingPodNames2[pod.Name] = true
	}

	// Delete PCSG pod
	pcsgPodToDelete := pccPods[0]
	logger.Debugf("Deleting PCSG pod: %s", pcsgPodToDelete)
	if err := deletePodAndWaitForTermination(tc, pcsgPodToDelete); err != nil {
		t.Fatalf("Failed to delete PCSG pod and wait for replacement: %v", err)
	}

	if err := waitForRunningPods(tc, 10); err != nil {
		t.Fatalf("Failed to wait for pod with new specification to be created")
	}

	logger.Info("6. Verify replacements use the new template")
	// Get final pod list
	finalPods, err := listPods(tc)
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

	logger.Info("OnDelete Update - Mixed PodCliques and PCSG test (OD-6) completed successfully!")
}

// Test_OD7_MultipleReplicasPCS tests OnDelete strategy with multiple PCS replicas.
// Scenario OD-7 (from proposal Testcase 4):
// 1. Initialize a 20-node Grove cluster
// 2. Deploy workload with OnDelete strategy and 2 PCS replicas
// 3. Change the specification of pc-a
// 4. Verify that NO pods are automatically deleted
// 5. Verify uniform strategy application across both replicas
func Test_OD7_MultipleReplicasPCS(t *testing.T) {
	logger.Info("1. Initialize a 20-node Grove cluster")
	logger.Info("2. Deploy workload with OnDelete strategy and 2 PCS replicas, verify 20 pods")
	tc, cleanup, tracker := setupOnDeleteTest(t, OnDeleteTestConfig{
		WorkerNodes:        20,
		ExpectedPods:       10,
		InitialPCSReplicas: 2,
		PostScalePods:      20,
	})
	defer cleanup()

	// Capture existing pods before triggering update
	existingPods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list existing pods: %v", err)
	}

	existingPodNames := make(map[string]bool)
	for _, pod := range existingPods.Items {
		existingPodNames[pod.Name] = true
	}
	logger.Debugf("Captured %d existing pods before spec change", len(existingPodNames))

	logger.Info("3. Change the specification of pc-a")
	if err := triggerOnDeletePodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	logger.Info("4. Verify that NO pods are automatically deleted")
	// Wait a reasonable time for any automatic deletion to occur (it shouldn't)
	time.Sleep(10 * time.Second)

	// Verify no pods were deleted
	tracker.Stop()
	events := tracker.getEvents()
	verifyNoPodsDeleted(tc, events, existingPodNames)

	// Wait for update to be marked complete
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 1 * time.Minute
	if err := waitForOnDeleteUpdateComplete(tcLongTimeout, 2); err != nil {
		t.Fatalf("Failed to verify OnDelete update completion: %v", err)
	}

	logger.Info("5. Verify uniform strategy application across both replicas")
	// Verify all original pods are still running
	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}

	if len(pods.Items) != 20 {
		t.Fatalf("Expected 20 pods to still be running, but got %d", len(pods.Items))
	}

	// Verify pods from both replicas are present
	replica0Count := 0
	replica1Count := 0
	for _, pod := range pods.Items {
		if pod.Labels != nil {
			if idx, ok := pod.Labels["grove.io/podcliqueset-replica-index"]; ok {
				if idx == "0" {
					replica0Count++
				} else if idx == "1" {
					replica1Count++
				}
			}
		}
	}

	if replica0Count != 10 || replica1Count != 10 {
		t.Fatalf("Expected 10 pods per replica, got replica0=%d, replica1=%d", replica0Count, replica1Count)
	}

	logger.Info("OnDelete Update - Multiple PCS Replicas test (OD-7) completed successfully!")
}

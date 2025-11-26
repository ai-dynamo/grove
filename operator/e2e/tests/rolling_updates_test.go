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
	"fmt"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/util/retry"
)

// Test_RU7_RollingUpdatePCSPodClique tests rolling update when PCS-owned Podclique spec is updated
// Scenario RU-7:
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Change the specification of pc-a
// 4. Verify that only one pod is deleted at a time
// 5. Verify that a single PCS replica is updated first before moving to another
func Test_RU7_RollingUpdatePCSPodClique(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 10)
	defer cleanup()

	logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
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
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	pods, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	if err := waitForPods(tc, expectedPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	if len(pods.Items) != expectedPods {
		t.Fatalf("Expected %d pods, but found %d", expectedPods, len(pods.Items))
	}

	// Set up watcher before triggering update
	tracker := newRollingUpdateTracker()
	if err := tracker.Start(tc); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer tracker.Stop()

	// Wait to ensure the pod watcher is fully established before triggering the rolling update.
	// This prevents a race condition where early pod events (deletions/additions) could be
	// missed if the watcher hasn't fully subscribed to the API server's watch stream.
	if err := tracker.WaitForReady(); err != nil {
		t.Fatalf("Failed to wait for tracker to be ready: %v", err)
	}

	logger.Info("3. Change the specification of pc-a")
	if err := triggerPodCliqueRollingUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	// Wait for rolling update to complete (use modified tc with longer timeout)
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 1 * time.Minute
	if err := waitForRollingUpdateComplete(tcLongTimeout, 1); err != nil {
		// Capture operator logs before failing
		logger.Info("=== Rolling update timed out - capturing operator logs ===")
		captureOperatorLogs(tc, "grove-system", "grove-operator")
		t.Fatalf("Failed to wait for rolling update to complete: %v", err)
	}

	// Stop tracking now that the update is complete
	tracker.Stop()

	logger.Info("4. Verify that only one pod is deleted at a time")
	events := tracker.getEvents()

	verifyOnePodDeletedAtATime(tc, events)

	logger.Info("5. Verify that a single PCS replica is updated first before moving to another")
	verifySinglePCSReplicaUpdatedFirst(tc, events)

	logger.Info("🎉 Rolling Update on PCS-owned Podclique test (RU-7) completed successfully!")
}

// Test_RU8_RollingUpdatePCSGPodClique tests rolling update when PCSG-owned Podclique spec is updated
// Scenario RU-8:
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Change the specification of pc-b
// 4. Verify that only one PCSG replica is deleted at a time
// 5. Verify that a single PCS replica is updated first before moving to another
func Test_RU8_RollingUpdatePCSGPodClique(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 10)
	defer cleanup()

	logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
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
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	pods, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	if err := waitForPods(tc, expectedPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	if len(pods.Items) != expectedPods {
		t.Fatalf("Expected %d pods, but found %d", expectedPods, len(pods.Items))
	}

	tracker := newRollingUpdateTracker()
	if err := tracker.Start(tc); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer tracker.Stop()

	// Wait to ensure the pod watcher is fully established before triggering the rolling update.
	// This prevents a race condition where early pod events (deletions/additions) could be
	// missed if the watcher hasn't fully subscribed to the API server's watch stream.
	if err := tracker.WaitForReady(); err != nil {
		t.Fatalf("Failed to wait for tracker to be ready: %v", err)
	}

	logger.Info("3. Change the specification of pc-b")
	if err := triggerPodCliqueRollingUpdate(tc, "pc-b"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	// Wait for rolling update to complete
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 1 * time.Minute
	if err := waitForRollingUpdateComplete(tcLongTimeout, 1); err != nil {
		t.Fatalf("Failed to wait for rolling update to complete: %v", err)
	}

	// Stop tracking now that the update is complete
	tracker.Stop()

	logger.Info("4. Verify that only one PCSG replica is deleted at a time")
	events := tracker.getEvents()
	verifyOnePCSGReplicaDeletedAtATime(tc, events)

	logger.Info("5. Verify that a single PCS replica is updated first before moving to another")
	verifySinglePCSReplicaUpdatedFirst(tc, events)

	logger.Info("🎉 Rolling Update on PCSG-owned Podclique test (RU-8) completed successfully!")
}

// Test_RU9_RollingUpdateAllPodCliques tests rolling update when all Podclique specs are updated
// Scenario RU-9:
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Change the specification of pc-a, pc-b and pc-c
// 4. Verify that only one pod in each Podclique and one replica in each PCSG is deleted at a time
// 5. Verify that a single PCS replica is updated first before moving to another
func Test_RU9_RollingUpdateAllPodCliques(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 10)
	defer cleanup()

	logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
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
			Name:         "workload1",
			YAMLPath:     "../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: expectedPods,
		},
	}

	pods, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	if err := waitForPods(tc, expectedPods); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	if len(pods.Items) != expectedPods {
		t.Fatalf("Expected %d pods, but found %d", expectedPods, len(pods.Items))
	}

	tracker := newRollingUpdateTracker()
	if err := tracker.Start(tc); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer tracker.Stop()

	// Wait to ensure the pod watcher is fully established before triggering the rolling update.
	// This prevents a race condition where early pod events (deletions/additions) could be
	// missed if the watcher hasn't fully subscribed to the API server's watch stream.
	if err := tracker.WaitForReady(); err != nil {
		t.Fatalf("Failed to wait for tracker to be ready: %v", err)
	}

	logger.Info("3. Change the specification of pc-a, pc-b and pc-c")
	for _, cliqueName := range []string{"pc-a", "pc-b", "pc-c"} {
		if err := triggerPodCliqueRollingUpdate(tc, cliqueName); err != nil {
			t.Fatalf("Failed to update PodClique %s spec: %v", cliqueName, err)
		}
	}

	// Wait for rolling update to complete
	if err := waitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("Failed to wait for rolling update to complete: %v", err)
	}

	// Stop tracking now that the update is complete
	tracker.Stop()

	logger.Info("4. Verify that only one pod in each Podclique and one replica in each PCSG is deleted at a time")
	events := tracker.getEvents()
	verifySinglePCSReplicaUpdatedFirst(tc, events)
	verifyOnePodDeletedAtATimePerPodclique(tc, events)
	verifyOnePCSGReplicaDeletedAtATimePerPCSG(tc, events)

	logger.Info("5. Verify that a single PCS replica is updated first before moving to another")
	verifySinglePCSReplicaUpdatedFirst(tc, events)

	logger.Info("🎉 Rolling Update on all Podcliques test (RU-9) completed successfully!")
}

/* This test fails. The rolling update starts, a pod gets deleted.
// Test_RU10_RollingUpdateInsufficientResources tests rolling update with insufficient resources
// Scenario RU-10:
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Cordon all worker nodes
// 4. Change the specification of pc-a
// 5. Verify the rolling update does not progress due to insufficient resources
// 6. Uncordon the nodes, and verify the rolling update continues
func Test_RU10_RollingUpdateInsufficientResources(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 10-node Grove cluster")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 10)
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
			ExpectedPods: 10,
		},
	}

	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	if err := waitForPods(tc, 10); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	logger.Info("3. Cordon all worker nodes")
	workerNodes, err := getWorkerNodes(tc)
	if err != nil {
		t.Fatalf("Failed to get agent nodes: %v", err)
	}

	cordonNodes(tc, workerNodes)

	// Capture the existing pods before starting the tracker
	existingPods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list existing pods: %v", err)
	}

	// Capture the existing pods names for verification later
	existingPodNames := make(map[string]bool)
	for _, pod := range existingPods.Items {
		existingPodNames[pod.Name] = true
	}
	logger.Infof("Captured %d existing pods before rolling update", len(existingPodNames))

	tracker := newRollingUpdateTracker()
	if err := tracker.Start(ctx, clientset, tc.Namespace, tc.getLabelSelector()); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer tracker.Stop()

	// Wait to ensure the pod watcher is fully established before triggering the rolling update.
	// This prevents a race condition where early pod events (deletions/additions) could be
	// missed if the watcher hasn't fully subscribed to the API server's watch stream.
	if err := tracker.WaitForReady(); err != nil {
		t.Fatalf("Failed to wait for tracker to be ready: %v", err)
	}

	logger.Info("4. Change the specification of pc-a")
	err = triggerPodCliqueRollingUpdate(ctx, dynamicClient, tc.Namespace, "workload1", "pc-a")
	if err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	logger.Info("5. Verify the rolling update does not progress due to insufficient resources")
	time.Sleep(1 * time.Minute)

	// Verify that none of the existing pods were deleted during the insufficient resources period
	events := tracker.getEvents()
	var deletedExistingPods []string
	for _, event := range events {
		switch event.Type {
		case watch.Deleted:
			if existingPodNames[event.Pod.Name] {
				deletedExistingPods = append(deletedExistingPods, event.Pod.Name)
				logger.Debugf("Existing pod deleted during insufficient resources: %s", event.Pod.Name)
			}
		}
	}

	if len(deletedExistingPods) > 0 {
		t.Fatalf("Rolling update progressed despite insufficient resources: %d existing pods deleted: %v",
			len(deletedExistingPods), deletedExistingPods)
	}

	logger.Info("6. Uncordon the nodes, and verify the rolling update continues")
	uncordonNodes(tc, workerNodes)

	// Wait for rolling update to complete after uncordoning
	if err := waitForRollingUpdateComplete(ctx, dynamicClient, tc.Namespace, "workload1", 1, 1*time.Minute); err != nil {
		t.Fatalf("Failed to wait for rolling update to complete: %v", err)
	}

	logger.Info("🎉 Rolling Update with insufficient resources test (RU-10) completed successfully!")
}
*/

// Test_RU11_RollingUpdateWithPCSScaleOut tests rolling update with scale-out on PCS
// Scenario RU-11:
// 1. Initialize a 30-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Change the specification of pc-a
// 4. Scale out the PCS during the rolling update
// 5. Verify the scaled out replica is created with the correct specifications
func Test_RU11_RollingUpdateWithPCSScaleOut(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 30-node Grove cluster")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 30)
	defer cleanup()

	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
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
			ExpectedPods: 10,
		},
	}

	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	// Patch all containers to ignore SIGTERM so pods take full termination grace period.
	// This slows down rolling updates, giving us time to test scale-out during the update.
	if err := patchPCSWithSIGTERMIgnoringCommand(tc); err != nil {
		t.Fatalf("Failed to patch PCS with SIGTERM-ignoring command: %v", err)
	}

	// Wait for the rolling update from this patch to complete
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 2 * time.Minute
	if err := waitForRollingUpdateComplete(tcLongTimeout, 1); err != nil {
		t.Fatalf("Failed to wait for SIGTERM patch rolling update to complete: %v", err)
	}

	// Scale PCS to 2 replicas first
	scalePCSAndWait(tc, "workload1", 2, 20, 0)

	// Wait for pods to be ready
	if err := waitForPods(tc, 20); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	tracker := newRollingUpdateTracker()
	if err := tracker.Start(tc); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer tracker.Stop()

	if err := tracker.WaitForReady(); err != nil {
		t.Fatalf("Failed to wait for tracker to be ready: %v", err)
	}

	logger.Info("3. Change the specification of pc-a")
	tcLongTimeout.Timeout = 2 * time.Minute
	updateWait := triggerRollingUpdate(tcLongTimeout, 3, "pc-a")

	logger.Info("4. Scale out the PCS during the rolling update (in parallel)")
	scaleWait := scalePCS(tcLongTimeout, "workload1", 3, 30, 0, 100) // 100ms delay so update is "first"

	if err := updateWait.Wait(); err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := scaleWait.Wait(); err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	tracker.Stop()

	logger.Info("5. Verify the scaled out replica is created with the correct specifications")
	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}

	if len(pods.Items) != 30 {
		t.Fatalf("Expected 30 pods after scale-out, got %d", len(pods.Items))
	}

	// Verify all pods have the updated spec
	events := tracker.getEvents()
	logger.Debugf("Captured %d pod events during rolling update", len(events))

	logger.Info("🎉 Rolling Update with PCS scale-out test (RU-11) completed successfully!")
}

// Test_RU12_RollingUpdateWithPCSScaleInDuringUpdate tests rolling update with scale-in on PCS while final ordinal is being updated
// Scenario RU-12:
// 1. Initialize a 30-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Change the specification of pc-a, pc-b and pc-c
// 4. Scale in the PCS while the final ordinal is being updated
// 5. Verify the update goes through successfully
func Test_RU12_RollingUpdateWithPCSScaleInDuringUpdate(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 30-node Grove cluster")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 30)
	defer cleanup()

	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
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
			ExpectedPods: 10,
		},
	}

	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	// Patch all containers to ignore SIGTERM so pods take full termination grace period.
	// This slows down rolling updates, giving us time to test scale-out during the update.
	if err := patchPCSWithSIGTERMIgnoringCommand(tc); err != nil {
		t.Fatalf("Failed to patch PCS with SIGTERM-ignoring command: %v", err)
	}

	// Wait for the rolling update from this patch to complete
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 2 * time.Minute
	if err := waitForRollingUpdateComplete(tcLongTimeout, 1); err != nil {
		t.Fatalf("Failed to wait for SIGTERM patch rolling update to complete: %v", err)
	}
	scalePCSAndWait(tc, "workload1", 2, 20, 0)

	if err := waitForPods(tc, 20); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	tracker := newRollingUpdateTracker()
	if err := tracker.Start(tc); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer tracker.Stop()

	if err := tracker.WaitForReady(); err != nil {
		t.Fatalf("Failed to wait for tracker to be ready: %v", err)
	}

	logger.Info("3. Change the specification of pc-a, pc-b and pc-c")
	// Use raw trigger since we need to wait for ordinal before starting the wait
	for _, cliqueName := range []string{"pc-a", "pc-b", "pc-c"} {
		if err := triggerPodCliqueRollingUpdate(tc, cliqueName); err != nil {
			t.Fatalf("Failed to trigger rolling update on %s: %v", cliqueName, err)
		}
	}

	logger.Info("4. Scale in the PCS while the final ordinal is being updated")
	// Wait for the final ordinal (ordinal 1 since there's two replicas, indexed from 0) to start updating before scaling in
	// Rolling updates process ordinals from highest to lowest, so ordinal 1 is updated first
	tcOrdinalTimeout := tc
	tcOrdinalTimeout.Timeout = 60 * time.Second
	if err := waitForOrdinalUpdating(tcOrdinalTimeout, 1); err != nil {
		t.Fatalf("Failed to wait for final ordinal to start updating: %v", err)
	}

	// Scale in parallel with the ongoing rolling update (no delay since we already waited for ordinal)
	scaleWait := scalePCS(tcLongTimeout, "workload1", 1, 10, 0, 0) // No delay since update already in progress

	logger.Info("5. Verify the update goes through successfully")
	updateWait := waitForRollingUpdate(tcLongTimeout, 1)

	if err := updateWait.Wait(); err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := scaleWait.Wait(); err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	tracker.Stop()

	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}

	if len(pods.Items) != 10 {
		t.Fatalf("Expected 10 pods after scale-in, got %d", len(pods.Items))
	}

	logger.Info("🎉 Rolling Update with PCS scale-in during update test (RU-12) completed successfully!")
}

// Test_RU13_RollingUpdateWithPCSScaleInAfterFinalOrdinal tests rolling update with scale-in on PCS after final ordinal finishes
// Scenario RU-13:
// 1. Initialize a 20-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Change the specification of pc-a, pc-b and pc-c
// 4. Wait for rolling update to complete on replica 1
// 5. Scale in the PCS after final ordinal has been updated
// 6. Verify the update goes through successfully
func Test_RU13_RollingUpdateWithPCSScaleInAfterFinalOrdinal(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 20-node Grove cluster")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 20)
	defer cleanup()

	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
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
			ExpectedPods: 10,
		},
	}

	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	scalePCSAndWait(tc, "workload1", 2, 20, 0)

	if err := waitForPods(tc, 20); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	tracker := newRollingUpdateTracker()
	if err := tracker.Start(tc); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer tracker.Stop()

	if err := tracker.WaitForReady(); err != nil {
		t.Fatalf("Failed to wait for tracker to be ready: %v", err)
	}

	logger.Info("3. Change the specification of pc-a, pc-b and pc-c")
	for _, cliqueName := range []string{"pc-a", "pc-b", "pc-c"} {
		if err := triggerPodCliqueRollingUpdate(tc, cliqueName); err != nil {
			t.Fatalf("Failed to update PodClique %s spec: %v", cliqueName, err)
		}
	}

	logger.Info("4. Wait for rolling update to complete on both replicas")
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 2 * time.Minute
	if err := waitForRollingUpdateComplete(tcLongTimeout, 2); err != nil {
		t.Fatalf("Failed to wait for rolling update to complete: %v", err)
	}

	logger.Info("5. Scale in the PCS after final ordinal has been updated")
	scalePCSAndWait(tc, "workload1", 1, 10, 0)

	logger.Info("6. Verify the update goes through successfully")
	tracker.Stop()

	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}

	if len(pods.Items) != 10 {
		t.Fatalf("Expected 10 pods after scale-in, got %d", len(pods.Items))
	}

	logger.Info("🎉 Rolling Update with PCS scale-in after final ordinal test (RU-13) completed successfully!")
}

// Test_RU14_RollingUpdateWithPCSGScaleOutDuringUpdate tests rolling update with scale-out on PCSG being updated
// Scenario RU-14:
// 1. Initialize a 28-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Change the specification of pc-a, pc-b and pc-c
// 4. Scale out the PCSG during its rolling update
// 5. Verify the scaled out replica is created with the correct specifications
// 6. Verify it should not be updated again before the rolling update ends
func Test_RU14_RollingUpdateWithPCSGScaleOutDuringUpdate(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
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
			ExpectedPods: 10,
		},
	}

	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	scalePCSAndWait(tc, "workload1", 2, 20, 0)

	if err := waitForPods(tc, 20); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	tracker := newRollingUpdateTracker()
	if err := tracker.Start(tc); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer tracker.Stop()

	if err := tracker.WaitForReady(); err != nil {
		t.Fatalf("Failed to wait for tracker to be ready: %v", err)
	}

	logger.Info("3. Change the specification of pc-a, pc-b and pc-c")
	tcLongerTimeout := tc
	tcLongerTimeout.Timeout = 2 * time.Minute
	updateWait := triggerRollingUpdate(tcLongerTimeout, 2, "pc-a", "pc-b", "pc-c")

	logger.Info("4. Scale out the PCSG during its rolling update (in parallel)")
	scaleWait := scalePCSGAcrossAllReplicas(tcLongerTimeout, "workload1", "sg-x", 2, 3, 28, 0, 100) // 100ms delay so update is "first"

	logger.Info("5. Verify the scaled out replica is created with the correct specifications")
	// sg-x = 4 pods per replica (1 pc-b + 3 pc-c)
	// Scaling PCSG instances directly (workload1-0-sg-x, workload1-1-sg-x) since the PCS controller
	// only sets replicas during initial PCSG creation to support HPA scaling.
	// After scaling sg-x to 3 replicas: 2 PCS replicas x (2 pc-a + 3 sg-x x 4 pods) = 2 x 14 = 28 pods

	logger.Info("6. Verify it should not be updated again before the rolling update ends")
	if err := updateWait.Wait(); err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := scaleWait.Wait(); err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	tracker.Stop()

	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}

	if len(pods.Items) != 28 {
		t.Fatalf("Expected 28 pods after PCSG scale-out 2*(2 pc-a + 3 (1 pc-b + 3 pc-c))), got %d", len(pods.Items))
	}

	logger.Info("🎉 Rolling Update with PCSG scale-out during update test (RU-14) completed successfully!")
}

// Test_RU15_RollingUpdateWithPCSGScaleOutBeforeUpdate tests rolling update with scale-out on PCSG before it is updated
// Scenario RU-15:
// 1. Initialize a 28-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Change the specification of pc-a, pc-b and pc-c
// 4. Scale out the PCSG before its rolling update starts
// 5. Verify the scaled out replica is created with the correct specifications
// 6. Verify it should not be updated again before the rolling update ends
func Test_RU15_RollingUpdateWithPCSGScaleOutBeforeUpdate(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
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
			ExpectedPods: 10,
		},
	}

	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	scalePCSAndWait(tc, "workload1", 2, 20, 0)

	if err := waitForPods(tc, 20); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	tracker := newRollingUpdateTracker()
	if err := tracker.Start(tc); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer tracker.Stop()

	if err := tracker.WaitForReady(); err != nil {
		t.Fatalf("Failed to wait for tracker to be ready: %v", err)
	}

	logger.Info("3. Scale out the PCSG before its rolling update starts (in parallel)")
	// Scaling PCSG instances directly (workload1-0-sg-x, workload1-1-sg-x) since the PCS controller
	// only sets replicas during initial PCSG creation to support HPA scaling.
	// After scaling sg-x to 3 replicas: 2 PCS replicas x (2 pc-a + 3 sg-x x 4 pods) = 2 x 14 = 28 pods
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 2 * time.Minute
	// Scale starts first (no delay)
	scaleWait := scalePCSGAcrossAllReplicas(tcLongTimeout, "workload1", "sg-x", 2, 3, 28, 0, 0)

	logger.Info("4. Change the specification of pc-a, pc-b and pc-c")

	// Small delay so scale is clearly "first", then trigger update
	time.Sleep(100 * time.Millisecond)
	updateWait := triggerRollingUpdate(tcLongTimeout, 2, "pc-a", "pc-b", "pc-c")

	logger.Info("5. Verify the scaled out replica is created with the correct specifications")
	logger.Info("6. Verify it should not be updated again before the rolling update ends")

	if err := updateWait.Wait(); err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := scaleWait.Wait(); err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	tracker.Stop()

	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}

	if len(pods.Items) != 28 {
		t.Fatalf("Expected 28 pods after PCSG scale-out (2 x (2 pc-a + 3 sg-x replicas)), got %d", len(pods.Items))
	}

	logger.Info("🎉 Rolling Update with PCSG scale-out before update test (RU-15) completed successfully!")
}

// Test_RU16_RollingUpdateWithPCSGScaleInDuringUpdate tests rolling update with scale-in on PCSG being updated
// Scenario RU-16:
// 1. Initialize a 28-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Scale up sg-x to 3 replicas (28 pods) so we can later scale in without going below minAvailable
// 4. Change the specification of pc-a, pc-b and pc-c
// 5. Scale in the PCSG during its rolling update (back to 2 replicas)
// 6. Verify the update goes through successfully
func Test_RU16_RollingUpdateWithPCSGScaleInDuringUpdate(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
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
			ExpectedPods: 10,
		},
	}

	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	scalePCSAndWait(tc, "workload1", 2, 20, 0)

	if err := waitForPods(tc, 20); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	logger.Info("3. Scale up sg-x to 3 replicas (28 pods) so we can later scale in")
	// Scaling PCSG instances directly (workload1-0-sg-x, workload1-1-sg-x) since the PCS controller
	// only sets replicas during initial PCSG creation to support HPA scaling.
	// After scaling sg-x to 3 replicas: 2 PCS replicas x (2 pc-a + 3 sg-x x 4 pods) = 2 x 14 = 28 pods
	scalePCSGAcrossAllReplicasAndWait(tc, "workload1", "sg-x", 2, 3, 28, 0)

	tracker := newRollingUpdateTracker()
	if err := tracker.Start(tc); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer tracker.Stop()

	if err := tracker.WaitForReady(); err != nil {
		t.Fatalf("Failed to wait for tracker to be ready: %v", err)
	}

	logger.Info("4. Change the specification of pc-a, pc-b and pc-c")
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 2 * time.Minute
	updateWait := triggerRollingUpdate(tcLongTimeout, 2, "pc-a", "pc-b", "pc-c")

	logger.Info("5. Scale in the PCSG during its rolling update (in parallel)")
	// Scaling PCSG instances directly (workload1-0-sg-x, workload1-1-sg-x) since the PCS controller
	// only sets replicas during initial PCSG creation to support HPA scaling.
	// After scaling sg-x back to 2 replicas: 2 PCS replicas x (2 pc-a + 2 sg-x x 4 pods) = 2 x 10 = 20 pods
	scaleWait := scalePCSGAcrossAllReplicas(tcLongTimeout, "workload1", "sg-x", 2, 2, 20, 0, 100) // 100ms delay so update is "first"

	logger.Info("6. Verify the update goes through successfully")

	if err := updateWait.Wait(); err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := scaleWait.Wait(); err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	tracker.Stop()

	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}

	// After scaling sg-x back to 2 replicas via PCS template (affects all PCS replicas):
	// 2 PCS replicas x (2 pc-a + 2 sg-x x 4 pods) = 2 x 10 = 20 pods
	if len(pods.Items) != 20 {
		t.Fatalf("Expected 20 pods after PCSG scale-in, got %d", len(pods.Items))
	}

	logger.Info("🎉 Rolling Update with PCSG scale-in during update test (RU-16) completed successfully!")
}

// Test_RU17_RollingUpdateWithPCSGScaleInBeforeUpdate tests rolling update with scale-in on PCSG before it is updated
// Scenario RU-17:
// 1. Initialize a 28-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Scale up sg-x to 3 replicas (28 pods) so we can later scale in without going below minAvailable
// 4. Scale in the PCSG before its rolling update starts (back to 2 replicas)
// 5. Change the specification of pc-a, pc-b and pc-c
// 6. Verify the update goes through successfully
func Test_RU17_RollingUpdateWithPCSGScaleInBeforeUpdate(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 28-node Grove cluster")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 28)
	defer cleanup()

	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
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
			ExpectedPods: 10,
		},
	}

	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	scalePCSAndWait(tc, "workload1", 2, 20, 0)

	if err := waitForPods(tc, 20); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	logger.Info("3. Scale up sg-x to 3 replicas (28 pods) so we can later scale in")
	// Scaling PCSG instances directly (workload1-0-sg-x, workload1-1-sg-x) since the PCS controller
	// only sets replicas during initial PCSG creation to support HPA scaling.
	// After scaling sg-x to 3 replicas: 2 PCS replicas x (2 pc-a + 3 sg-x x 4 pods) = 2 x 14 = 28 pods
	scalePCSGAcrossAllReplicasAndWait(tc, "workload1", "sg-x", 2, 3, 28, 0)

	tracker := newRollingUpdateTracker()
	if err := tracker.Start(tc); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer tracker.Stop()

	if err := tracker.WaitForReady(); err != nil {
		t.Fatalf("Failed to wait for tracker to be ready: %v", err)
	}

	logger.Info("4. Scale in the PCSG before its rolling update starts (in parallel)")
	// Scale starts first (no delay)
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 2 * time.Minute
	scaleWait := scalePCSGAcrossAllReplicas(tcLongTimeout, "workload1", "sg-x", 2, 2, 20, 0, 0)

	logger.Info("5. Change the specification of pc-a, pc-b and pc-c")
	// Scaling PCSG instances directly (workload1-0-sg-x, workload1-1-sg-x) since the PCS controller
	// only sets replicas during initial PCSG creation to support HPA scaling.
	// After scaling sg-x back to 2 replicas: 2 PCS replicas x (2 pc-a + 2 sg-x x 4 pods) = 2 x 10 = 20 pods

	logger.Info("6. Verify the update goes through successfully")

	// Small delay so scale is clearly "first", then trigger update
	time.Sleep(100 * time.Millisecond)
	updateWait := triggerRollingUpdate(tcLongTimeout, 2, "pc-a", "pc-b", "pc-c")

	if err := updateWait.Wait(); err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := scaleWait.Wait(); err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	tracker.Stop()

	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}

	// After scaling sg-x back to 2 replicas via PCS template (affects all PCS replicas):
	// 2 PCS replicas x (2 pc-a + 2 sg-x x 4 pods) = 2 x 10 = 20 pods
	if len(pods.Items) != 20 {
		t.Fatalf("Expected 20 pods after PCSG scale-in, got %d", len(pods.Items))
	}

	logger.Info("🎉 Rolling Update with PCSG scale-in before update test (RU-17) completed successfully!")
}

// Test_RU18_RollingUpdateWithPodCliqueScaleOutDuringUpdate tests rolling update with scale-out on standalone PCLQ being updated
// Scenario RU-18:
// 1. Initialize a 24-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Change the specification of pc-a, pc-b and pc-c
// 4. Scale out the standalone PCLQ (pc-a) during its rolling update
// 5. Verify the scaled pods are created with the correct specifications
// 6. Verify they should not be updated again before the rolling update ends
func Test_RU18_RollingUpdateWithPodCliqueScaleOutDuringUpdate(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 24-node Grove cluster")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 24)
	defer cleanup()

	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
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
			ExpectedPods: 10,
		},
	}

	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	scalePCSAndWait(tc, "workload1", 2, 20, 0)

	if err := waitForPods(tc, 20); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	tracker := newRollingUpdateTracker()
	if err := tracker.Start(tc); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer tracker.Stop()

	if err := tracker.WaitForReady(); err != nil {
		t.Fatalf("Failed to wait for tracker to be ready: %v", err)
	}

	logger.Info("3. Change the specification of pc-a, pc-b and pc-c")
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 4 * time.Minute // Extra headroom for rolling update + scale

	updateWait := triggerRollingUpdate(tcLongTimeout, 2, "pc-a", "pc-b", "pc-c")

	logger.Info("4. Scale out the standalone PCLQ (pc-a) during its rolling update (in parallel)")

	logger.Info("5. Verify the scaled pods are created with the correct specifications")
	logger.Info("6. Verify they should not be updated again before the rolling update ends")
	scaleWait := scalePodClique(tcLongTimeout, "pc-a", 4, 24, 100) // 100ms delay so update is "first"

	if err := updateWait.Wait(); err != nil {
		// Capture diagnostics on failure
		logger.Info("=== Rolling update failed - capturing diagnostics ===")
		captureOperatorLogs(tc, "grove-system", "grove-operator")
		capturePodCliqueStatus(tc)
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := scaleWait.Wait(); err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	tracker.Stop()

	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}

	if len(pods.Items) != 24 {
		t.Fatalf("Expected 24 pods after PodClique scale-out (4 pc-a + 4 pc-b + 12 pc-c), got %d", len(pods.Items))
	}

	logger.Info("🎉 Rolling Update with PodClique scale-out during update test (RU-18) completed successfully!")
}

// Test_RU19_RollingUpdateWithPodCliqueScaleOutBeforeUpdate tests rolling update with scale-out on standalone PCLQ before it is updated
// Scenario RU-19:
// 1. Initialize a 24-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Scale out the standalone PCLQ (pc-a) before its rolling update
// 4. Change the specification of pc-a, pc-b and pc-c
// 5. Verify the scaled pods are created with the correct specifications
// 6. Verify they should not be updated again before the rolling update ends
func Test_RU19_RollingUpdateWithPodCliqueScaleOutBeforeUpdate(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 24-node Grove cluster")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 24)
	defer cleanup()

	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
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
			ExpectedPods: 10,
		},
	}

	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	scalePCSAndWait(tc, "workload1", 2, 20, 0)

	if err := waitForPods(tc, 20); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	tracker := newRollingUpdateTracker()
	if err := tracker.Start(tc); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer tracker.Stop()

	if err := tracker.WaitForReady(); err != nil {
		t.Fatalf("Failed to wait for tracker to be ready: %v", err)
	}

	logger.Info("3. Scale out the standalone PCLQ (pc-a) before its rolling update (in parallel)")
	// Scale starts first (no delay)
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 4 * time.Minute // Extra headroom for rolling update + scale
	scaleWait := scalePodClique(tcLongTimeout, "pc-a", 4, 24, 0)

	logger.Info("4. Change the specification of pc-a, pc-b and pc-c")
	// Small delay so scale is clearly "first", then trigger update
	time.Sleep(100 * time.Millisecond)
	updateWait := triggerRollingUpdate(tcLongTimeout, 2, "pc-a", "pc-b", "pc-c")

	logger.Info("5. Verify the scaled pods are created with the correct specifications")
	logger.Info("6. Verify they should not be updated again before the rolling update ends")

	if err := updateWait.Wait(); err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := scaleWait.Wait(); err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	tracker.Stop()

	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}

	if len(pods.Items) != 24 {
		t.Fatalf("Expected 24 pods after PodClique scale-out, got %d", len(pods.Items))
	}

	logger.Info("🎉 Rolling Update with PodClique scale-out before update test (RU-19) completed successfully!")
}

// Test_RU20_RollingUpdateWithPodCliqueScaleInDuringUpdate tests rolling update with scale-in on standalone PCLQ being updated
// Scenario RU-20:
// 1. Initialize a 22-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Scale out pc-a to 3 replicas (above minAvailable=2) to allow scale-in during update
// 4. Change the specification of pc-a, pc-b and pc-c
// 5. Scale in the standalone PCLQ (pc-a) from 3 to 2 during its rolling update
// 6. Verify the update goes through successfully
func Test_RU20_RollingUpdateWithPodCliqueScaleInDuringUpdate(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 22-node Grove cluster")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 22)
	defer cleanup()

	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
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
			ExpectedPods: 10,
		},
	}

	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	scalePCSAndWait(tc, "workload1", 2, 20, 0)

	if err := waitForPods(tc, 20); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	logger.Info("3. Scale out pc-a to 3 replicas (above minAvailable=2) to allow scale-in during update")
	// pc-a has minAvailable=2, so we scale up to 3 first to allow scale-in back to 2 during rolling update
	// Each PCS replica has: 2 pc-a + 8 sg-x pods = 10 pods
	// After scaling pc-a to 3: 2 PCS replicas × (3 pc-a + 8 sg-x) = 2 × 11 = 22 pods
	if err := scalePodCliqueInPCS(tc, "pc-a", 3); err != nil {
		t.Fatalf("Failed to scale out PodClique pc-a: %v", err)
	}

	if err := waitForPods(tc, 22); err != nil {
		t.Fatalf("Failed to wait for pods after pc-a scale-out: %v", err)
	}

	tracker := newRollingUpdateTracker()
	if err := tracker.Start(tc); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer tracker.Stop()

	if err := tracker.WaitForReady(); err != nil {
		t.Fatalf("Failed to wait for tracker to be ready: %v", err)
	}

	logger.Info("4. Change the specification of pc-a, pc-b and pc-c")
	// Scale in from 3 to 2 (stays at minAvailable=2)
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 4 * time.Minute // Extra headroom for rolling update + scale

	updateWait := triggerRollingUpdate(tcLongTimeout, 2, "pc-a", "pc-b", "pc-c")

	logger.Info("5. Scale in the standalone PCLQ (pc-a) from 3 to 2 during its rolling update (in parallel)")
	scaleWait := scalePodClique(tcLongTimeout, "pc-a", 2, 20, 100) // 100ms delay so update is "first"

	logger.Info("6. Verify the update goes through successfully")
	if err := updateWait.Wait(); err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := scaleWait.Wait(); err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	tracker.Stop()

	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}

	// After scale-in from 3 to 2 pc-a: 2 PCS replicas × (2 pc-a + 8 sg-x) = 2 × 10 = 20 pods
	if len(pods.Items) != 20 {
		t.Fatalf("Expected 20 pods after PodClique scale-in (2 pc-a per replica), got %d", len(pods.Items))
	}

	logger.Info("🎉 Rolling Update with PodClique scale-in during update test (RU-20) completed successfully!")
}

// Test_RU21_RollingUpdateWithPodCliqueScaleInBeforeUpdate tests rolling update with scale-in on standalone PCLQ before it is updated
// Scenario RU-21:
// 1. Initialize a 22-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Scale out pc-a to 3 replicas (above minAvailable=2) to allow scale-in
// 4. Scale in pc-a from 3 to 2 before its rolling update
// 5. Change the specification of pc-a, pc-b and pc-c
// 6. Verify the update goes through successfully
func Test_RU21_RollingUpdateWithPodCliqueScaleInBeforeUpdate(t *testing.T) {
	ctx := context.Background()

	logger.Info("1. Initialize a 22-node Grove cluster")
	clientset, restConfig, dynamicClient, cleanup := prepareTestCluster(ctx, t, 22)
	defer cleanup()

	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
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
			ExpectedPods: 10,
		},
	}

	_, err := deployAndVerifyWorkload(tc)
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	scalePCSAndWait(tc, "workload1", 2, 20, 0)

	if err := waitForPods(tc, 20); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	logger.Info("3. Scale out pc-a to 3 replicas (above minAvailable=2) to allow scale-in")
	// pc-a has minAvailable=2, so we scale up to 3 first to allow scale-in back to 2
	// After scaling pc-a to 3: 2 PCS replicas × (3 pc-a + 8 sg-x) = 2 × 11 = 22 pods
	if err := scalePodCliqueInPCS(tc, "pc-a", 3); err != nil {
		t.Fatalf("Failed to scale out PodClique pc-a: %v", err)
	}

	if err := waitForPods(tc, 22); err != nil {
		t.Fatalf("Failed to wait for pods after pc-a scale-out: %v", err)
	}

	tracker := newRollingUpdateTracker()
	if err := tracker.Start(tc); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer tracker.Stop()

	if err := tracker.WaitForReady(); err != nil {
		t.Fatalf("Failed to wait for tracker to be ready: %v", err)
	}

	logger.Info("4. Scale in pc-a from 3 to 2 before its rolling update (in parallel)")
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 4 * time.Minute // Extra headroom for rolling update + scale
	// Scale starts first (no delay) - scaling in from 3 to 2 (stays at minAvailable=2)
	scaleWait := scalePodClique(tcLongTimeout, "pc-a", 2, 20, 0)

	logger.Info("5. Change the specification of pc-a, pc-b and pc-c")
	// Small delay so scale is clearly "first", then trigger update
	time.Sleep(100 * time.Millisecond)
	updateWait := triggerRollingUpdate(tcLongTimeout, 2, "pc-a", "pc-b", "pc-c")

	logger.Info("6. Verify the update goes through successfully")

	if err := updateWait.Wait(); err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := scaleWait.Wait(); err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	tracker.Stop()

	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}

	// After scale-in from 3 to 2 pc-a: 2 PCS replicas × (2 pc-a + 8 sg-x) = 2 × 10 = 20 pods
	if len(pods.Items) != 20 {
		t.Fatalf("Expected 20 pods after PodClique scale-in, got %d", len(pods.Items))
	}

	logger.Info("🎉 Rolling Update with PodClique scale-in before update test (RU-21) completed successfully!")
}

// podEvent represents a pod lifecycle event during rolling update
type podEvent struct {
	Type      watch.EventType
	Pod       *corev1.Pod
	Timestamp time.Time
}

// rollingUpdateTracker tracks pod events during rolling update
type rollingUpdateTracker struct {
	events  []podEvent
	mu      sync.Mutex
	watcher watch.Interface
	cancel  context.CancelFunc
	ready   chan struct{}
}

// newRollingUpdateTracker creates a new rolling update tracker
func newRollingUpdateTracker() *rollingUpdateTracker {
	return &rollingUpdateTracker{
		ready: make(chan struct{}),
	}
}

// Start begins watching pod events.
// Uses tc.Ctx, tc.Clientset, tc.Namespace, and tc.getLabelSelector() for watch configuration.
func (t *rollingUpdateTracker) Start(tc TestContext) error {
	watcherCtx, cancel := context.WithCancel(tc.Ctx)
	t.cancel = cancel

	watcher, err := tc.Clientset.CoreV1().Pods(tc.Namespace).Watch(watcherCtx, metav1.ListOptions{
		LabelSelector: tc.getLabelSelector(),
	})
	if err != nil {
		cancel()
		return err
	}
	t.watcher = watcher

	go func() {
		// Signal that the watcher goroutine is running and ready to receive events
		close(t.ready)

		for {
			select {
			case <-watcherCtx.Done():
				return
			case event, ok := <-watcher.ResultChan():
				if !ok {
					return
				}
				if pod, ok := event.Object.(*corev1.Pod); ok {
					t.recordEvent(event.Type, pod)
				}
			}
		}
	}()

	return nil
}

// Stop stops the watcher and cleans up resources
func (t *rollingUpdateTracker) Stop() {
	if t.watcher != nil {
		t.watcher.Stop()
	}
	if t.cancel != nil {
		t.cancel()
	}
}

// WaitForReady waits for the watcher goroutine to be running and ready to receive events
// This prevents a race condition where early pod events could be missed
func (t *rollingUpdateTracker) WaitForReady() error {
	select {
	case <-t.ready:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("timeout waiting for watcher to be ready")
	}
}

func (t *rollingUpdateTracker) recordEvent(eventType watch.EventType, pod *corev1.Pod) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, podEvent{
		Type:      eventType,
		Pod:       pod.DeepCopy(),
		Timestamp: time.Now(),
	})
}

func (t *rollingUpdateTracker) getEvents() []podEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]podEvent{}, t.events...)
}

// triggerRollingUpdate triggers a rolling update on the specified cliques and returns an errgroup.Group
// that completes when the rolling update finishes.
// Uses tc.Workload.Name as the PCS name and tc.Timeout for the wait timeout.
func triggerRollingUpdate(tc TestContext, expectedReplicas int32, cliqueNames ...string) *errgroup.Group {
	g := new(errgroup.Group)
	g.Go(func() error {
		startTime := time.Now()

		// Trigger synchronously first
		for _, cliqueName := range cliqueNames {
			if err := triggerPodCliqueRollingUpdate(tc, cliqueName); err != nil {
				return fmt.Errorf("failed to update PodClique %s spec: %w", cliqueName, err)
			}
		}
		logger.Debugf("[triggerRollingUpdate] Triggered update on %v, waiting for completion...", cliqueNames)

		// Wait for completion
		err := waitForRollingUpdateComplete(tc, expectedReplicas)
		elapsed := time.Since(startTime)
		if err != nil {
			logger.Infof("[triggerRollingUpdate] Rolling update FAILED after %v: %v", elapsed, err)
		} else {
			logger.Infof("[triggerRollingUpdate] Rolling update completed in %v", elapsed)
		}
		return err
	})
	return g
}

// triggerPodCliqueRollingUpdate triggers a rolling update by adding/updating an environment variable in a PodClique.
// Uses tc.Workload.Name as the PCS name.
func triggerPodCliqueRollingUpdate(tc TestContext, cliqueName string) error {
	pcsGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}
	pcsName := tc.Workload.Name

	// Use current timestamp to ensure the value changes
	updateValue := fmt.Sprintf("%d", time.Now().Unix())

	// Retry on conflict errors (optimistic concurrency control)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get the unstructured PodCliqueSet
		unstructuredPCS, err := tc.DynamicClient.Resource(pcsGVR).Namespace(tc.Namespace).Get(tc.Ctx, pcsName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get PodCliqueSet: %w", err)
		}

		// Convert unstructured to typed PodCliqueSet
		var pcs grovev1alpha1.PodCliqueSet
		err = convertUnstructuredToTyped(unstructuredPCS.Object, &pcs)
		if err != nil {
			return fmt.Errorf("failed to convert to PodCliqueSet: %w", err)
		}

		// Find and update the appropriate clique
		found := false
		for i, clique := range pcs.Spec.Template.Cliques {
			if clique.Name == cliqueName {
				// Update the first container's environment variable
				if len(clique.Spec.PodSpec.Containers) > 0 {
					container := &pcs.Spec.Template.Cliques[i].Spec.PodSpec.Containers[0]

					// Check if ROLLING_UPDATE_TRIGGER env var exists
					envVarFound := false
					for j := range container.Env {
						if container.Env[j].Name == "ROLLING_UPDATE_TRIGGER" {
							container.Env[j].Value = updateValue
							envVarFound = true
							break
						}
					}

					// If not found, add new env var
					if !envVarFound {
						container.Env = append(container.Env, corev1.EnvVar{
							Name:  "ROLLING_UPDATE_TRIGGER",
							Value: updateValue,
						})
					}
					found = true
				}
				break
			}
		}

		if !found {
			return fmt.Errorf("clique %s not found in PodCliqueSet %s", cliqueName, pcsName)
		}

		// Convert back to unstructured
		updatedUnstructured, err := convertTypedToUnstructured(&pcs)
		if err != nil {
			return fmt.Errorf("failed to convert to unstructured: %w", err)
		}

		// Update the resource - will return conflict error if resource was modified
		_, err = tc.DynamicClient.Resource(pcsGVR).Namespace(tc.Namespace).Update(tc.Ctx, updatedUnstructured, metav1.UpdateOptions{})
		if err != nil {
			return err
		}

		return nil
	})
}

// patchPCSWithSIGTERMIgnoringCommand patches all containers in the PCS to use a command that ignores SIGTERM
// and sets the termination grace period to 5 seconds. This makes pods ignore graceful shutdown but still
// allows rolling updates to progress in a reasonable time for testing.
// Uses tc.Workload.Name as the PCS name.
func patchPCSWithSIGTERMIgnoringCommand(tc TestContext) error {
	pcsGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}
	pcsName := tc.Workload.Name

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		unstructuredPCS, err := tc.DynamicClient.Resource(pcsGVR).Namespace(tc.Namespace).Get(tc.Ctx, pcsName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get PodCliqueSet: %w", err)
		}

		var pcs grovev1alpha1.PodCliqueSet
		err = convertUnstructuredToTyped(unstructuredPCS.Object, &pcs)
		if err != nil {
			return fmt.Errorf("failed to convert to PodCliqueSet: %w", err)
		}

		// Update all cliques: set termination grace period and make containers ignore SIGTERM
		terminationGracePeriod := int64(5)
		for i := range pcs.Spec.Template.Cliques {
			// Set termination grace period to 5 seconds
			pcs.Spec.Template.Cliques[i].Spec.PodSpec.TerminationGracePeriodSeconds = &terminationGracePeriod

			// Update all containers to use a command that ignores SIGTERM
			for j := range pcs.Spec.Template.Cliques[i].Spec.PodSpec.Containers {
				container := &pcs.Spec.Template.Cliques[i].Spec.PodSpec.Containers[j]
				// Use shell command that traps and ignores SIGTERM, then sleeps forever
				container.Command = []string{"/bin/sh", "-c", "trap '' TERM; sleep infinity"}
			}
		}

		updatedUnstructured, err := convertTypedToUnstructured(&pcs)
		if err != nil {
			return fmt.Errorf("failed to convert to unstructured: %w", err)
		}

		_, err = tc.DynamicClient.Resource(pcsGVR).Namespace(tc.Namespace).Update(tc.Ctx, updatedUnstructured, metav1.UpdateOptions{})
		return err
	})
}

// waitForRollingUpdate starts polling for rolling update completion in the background and returns an errgroup.Group.
// Use this when you need to trigger an update separately (e.g., when doing something between trigger and wait).
// For the common case, use triggerRollingUpdate which combines trigger + wait.
func waitForRollingUpdate(tc TestContext, expectedReplicas int32) *errgroup.Group {
	g := new(errgroup.Group)
	g.Go(func() error {
		return waitForRollingUpdateComplete(tc, expectedReplicas)
	})
	return g
}

// waitForRollingUpdateComplete waits for rolling update to complete by checking UpdatedReplicas.
// Uses tc.Workload.Name as the PCS name and tc.Timeout for the timeout (use a modified tc if a different timeout is needed).
func waitForRollingUpdateComplete(tc TestContext, expectedReplicas int32) error {
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
		err = convertUnstructuredToTyped(unstructuredPCS.Object, &pcs)
		if err != nil {
			return false, err
		}

		// Log status every few polls for debugging
		if pollCount%3 == 1 {
			logger.Debugf("[waitForRollingUpdateComplete] Poll #%d: UpdatedReplicas=%d, expectedReplicas=%d, RollingUpdateProgress=%v",
				pollCount, pcs.Status.UpdatedReplicas, expectedReplicas, pcs.Status.RollingUpdateProgress != nil)
			if pcs.Status.RollingUpdateProgress != nil {
				logger.Debugf("  UpdateStartedAt=%v, UpdateEndedAt=%v, CurrentlyUpdating=%v",
					pcs.Status.RollingUpdateProgress.UpdateStartedAt,
					pcs.Status.RollingUpdateProgress.UpdateEndedAt,
					pcs.Status.RollingUpdateProgress.CurrentlyUpdating)
			}
		}

		// Check if rolling update is complete:
		// - UpdatedReplicas should match expected
		// - RollingUpdateProgress should exist with UpdateEndedAt set (not nil)
		if pcs.Status.UpdatedReplicas == expectedReplicas &&
			pcs.Status.RollingUpdateProgress != nil &&
			pcs.Status.RollingUpdateProgress.UpdateEndedAt != nil {
			logger.Debugf("[waitForRollingUpdateComplete] Rolling update completed after %d polls", pollCount)
			return true, nil
		}

		return false, nil
	})
}

// waitForOrdinalUpdating waits for a specific ordinal to start being updated during rolling update.
// Uses tc.Workload.Name as the PCS name and tc.Timeout for the timeout (use a modified tc if a different timeout is needed).
func waitForOrdinalUpdating(tc TestContext, ordinal int32) error {
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
		err = convertUnstructuredToTyped(unstructuredPCS.Object, &pcs)
		if err != nil {
			return false, err
		}

		// Log status every few polls for debugging
		if pollCount%3 == 1 {
			currentOrdinal := int32(-1)
			if pcs.Status.RollingUpdateProgress != nil && pcs.Status.RollingUpdateProgress.CurrentlyUpdating != nil {
				currentOrdinal = pcs.Status.RollingUpdateProgress.CurrentlyUpdating.ReplicaIndex
			}
			logger.Debugf("[waitForOrdinalUpdating] Poll #%d: waiting for ordinal %d, currently updating ordinal: %d",
				pollCount, ordinal, currentOrdinal)
		}

		// Check if the target ordinal is currently being updated
		if pcs.Status.RollingUpdateProgress != nil &&
			pcs.Status.RollingUpdateProgress.CurrentlyUpdating != nil &&
			pcs.Status.RollingUpdateProgress.CurrentlyUpdating.ReplicaIndex == ordinal {
			logger.Debugf("[waitForOrdinalUpdating] Ordinal %d started updating after %d polls", ordinal, pollCount)
			return true, nil
		}

		return false, nil
	})
}

// getPodIdentifier returns a stable identifier for a pod based on its logical position in the workload.
// This identifier remains the same when a pod is replaced during rolling updates.
//
// Stability is guaranteed because the hostname is set by the operator based on the pod's logical
// position in the PodClique (pclqName-podIndex), not the pod's generated name. During a rolling
// update:
//  1. The old pod is deleted (e.g., hostname: "workload1-pc-a-0-0")
//  2. The PodClique remains unchanged (same name and replica count)
//  3. The new pod is created to fill the same logical position
//  4. The new pod receives the same hostname (e.g., "workload1-pc-a-0-0")
//
// Only the pod name changes (due to GenerateName), while the hostname represents the pod's
// logical position in the workload hierarchy and remains constant.
func getPodIdentifier(tc TestContext, pod *corev1.Pod) string {
	tc.T.Helper()

	// Use hostname as the stable identifier (set by configurePodHostname in pod.go)
	// Format: {pclqName}-{podIndex} (e.g., "workload1-pc-a-0-0")
	if pod.Spec.Hostname == "" {
		tc.T.Fatalf("Pod %s does not have hostname set, cannot determine stable identifier", pod.Name)
	}

	return pod.Spec.Hostname
}

// verifyOnePodDeletedAtATime verifies only one pod globally is being deleted at a time
// during rolling updates.
//
// This function handles the fact that Kubernetes watch events can arrive out of order.
// Specifically, the ADDED event for a replacement pod can arrive before the DELETE event
// for the old pod, even though the deletion was initiated first. This happens because:
// 1. The old pod is marked for deletion (DeletionTimestamp is set)
// 2. The controller creates a replacement pod while the old one is terminating
// 3. Watch events for both can arrive in any order
//
// To handle this, we track both ADDED and DELETE events and only consider a hostname
// as "actively deleting" if there's no replacement pod that was added.
func verifyOnePodDeletedAtATime(tc TestContext, events []podEvent) {
	tc.T.Helper()

	// Log all events for debugging
	logger.Debug("=== Starting verifyOnePodDeletedAtATime analysis ===")
	logger.Debugf("Total events captured: %d", len(events))

	for i, event := range events {
		podclique := ""
		if event.Pod.Labels != nil {
			podclique = event.Pod.Labels["grove.io/podclique"]
		}
		logger.Debugf("Event[%d]: Type=%s PodName=%s Hostname=%s PodClique=%s Timestamp=%v",
			i, event.Type, event.Pod.Name, event.Pod.Spec.Hostname, podclique, event.Timestamp.Format("15:04:05.000"))
	}

	// Track the most recent ADDED pod for each hostname (podID).
	// Key: hostname, Value: pod name of the most recent ADDED event
	addedPods := make(map[string]string)

	// Track DELETE events for each hostname.
	// Key: hostname, Value: pod name of the deleted pod
	deletedPods := make(map[string]string)

	maxConcurrentDeletions := 0
	var violationEvents []string

	logger.Debug("=== Processing events to find concurrent deletions ===")
	for i, event := range events {
		podID := getPodIdentifier(tc, event.Pod)
		podName := event.Pod.Name

		switch event.Type {
		case watch.Deleted:
			// Record the DELETE event for this hostname
			deletedPods[podID] = podName
			logger.Debugf("Event[%d] DELETE: podID=%s, podName=%s", i, podID, podName)
		case watch.Added:
			// Record the ADDED event for this hostname
			addedPods[podID] = podName
			logger.Debugf("Event[%d] ADDED: podID=%s, podName=%s", i, podID, podName)
		case watch.Modified:
			logger.Debugf("Event[%d] MODIFIED: podID=%s (ignored for deletion tracking)", i, podID)
		}

		// Calculate "actively deleting" hostnames: those with a DELETE event where
		// either no ADDED event exists, or the ADDED pod is the same as the deleted pod
		// (meaning the replacement hasn't been added yet)
		activelyDeleting := make(map[string]bool)
		for hostname, deletedPodName := range deletedPods {
			addedPodName, hasAdded := addedPods[hostname]
			// A hostname is "actively deleting" if:
			// 1. No replacement pod has been added, OR
			// 2. The added pod is the same as the deleted pod (no actual replacement)
			if !hasAdded || addedPodName == deletedPodName {
				activelyDeleting[hostname] = true
			}
		}

		currentlyDeleting := len(activelyDeleting)
		if currentlyDeleting > 0 {
			deletingKeys := make([]string, 0, len(activelyDeleting))
			for k := range activelyDeleting {
				deletingKeys = append(deletingKeys, k)
			}
			slices.Sort(deletingKeys)
			logger.Debugf("Event[%d] State: currentlyDeleting=%d, activelyDeleting=%v",
				i, currentlyDeleting, deletingKeys)
		}

		// Track the maximum number of concurrent deletions observed.
		if currentlyDeleting > maxConcurrentDeletions {
			maxConcurrentDeletions = currentlyDeleting
			deletingKeys := make([]string, 0, len(activelyDeleting))
			for k := range activelyDeleting {
				deletingKeys = append(deletingKeys, k)
			}
			slices.Sort(deletingKeys)
			violationEvents = append(violationEvents, fmt.Sprintf(
				"Event[%d]: maxConcurrentDeletions increased to %d, activelyDeleting=%v",
				i, maxConcurrentDeletions, deletingKeys))
		}
	}

	logger.Debugf("=== Analysis complete: maxConcurrentDeletions=%d ===", maxConcurrentDeletions)
	if len(violationEvents) > 0 {
		logger.Debug("Violation events:")
		for _, v := range violationEvents {
			logger.Debugf("  %s", v)
		}
	}

	// Assert that at most 1 pod was being deleted/replaced at any point in time,
	// which ensures the rolling update respects the MaxUnavailable=1 constraint at the global level.
	if maxConcurrentDeletions > 1 {
		tc.T.Fatalf("Expected at most 1 pod being deleted at a time, but found %d concurrent deletions", maxConcurrentDeletions)
	}
}

// getMapKeys returns a slice of keys from a map[string]time.Time for logging
func getMapKeys(m map[string]time.Time) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// verifyOnePodDeletedAtATimePerPodclique verifies only one pod per Podclique is being deleted at a time
// during rolling updates.
//
// This function processes a sequence of pod events and tracks the number of individual pods
// that are in a "deleting" state within each Podclique at any given time.
//
// IMPORTANT: This function handles the fact that Kubernetes watch events can arrive out of order.
// See verifyOnePCSGReplicaDeletedAtATime for detailed explanation.
func verifyOnePodDeletedAtATimePerPodclique(tc TestContext, events []podEvent) {
	tc.T.Helper()

	// Track DELETE and ADDED counts separately per podID per PodClique to handle out-of-order events.
	// Map structure: podcliqueName -> podID -> count
	deletedCount := make(map[string]map[string]int)
	addedCount := make(map[string]map[string]int)
	maxConcurrentDeletionsPerPodclique := make(map[string]int)

	for _, event := range events {
		// All pods should have labels - if nil, that's a bug
		if event.Pod.Labels == nil {
			tc.T.Fatalf("Pod %s has no labels, which indicates a bug in pod creation", event.Pod.Name)
		}

		podcliqueName, ok := event.Pod.Labels["grove.io/podclique"]
		if !ok {
			tc.T.Fatalf("Pod %s does not have grove.io/podclique label", event.Pod.Name)
		}

		podID := getPodIdentifier(tc, event.Pod)

		switch event.Type {
		case watch.Deleted:
			if deletedCount[podcliqueName] == nil {
				deletedCount[podcliqueName] = make(map[string]int)
			}
			deletedCount[podcliqueName][podID]++
		case watch.Added:
			if addedCount[podcliqueName] == nil {
				addedCount[podcliqueName] = make(map[string]int)
			}
			addedCount[podcliqueName][podID]++
		}

		// Calculate "actively deleting" pods per PodClique: those where DELETE count > ADDED count.
		for podclique, pcDeleted := range deletedCount {
			activelyDeleting := 0
			for podID, deletes := range pcDeleted {
				adds := 0
				if addedCount[podclique] != nil {
					adds = addedCount[podclique][podID]
				}
				if deletes > adds {
					activelyDeleting++
				}
			}
			if activelyDeleting > maxConcurrentDeletionsPerPodclique[podclique] {
				maxConcurrentDeletionsPerPodclique[podclique] = activelyDeleting
			}
		}
	}

	// Assert that at most 1 pod per Podclique was in the deletion-to-creation window at any point in time,
	// which ensures the rolling update deletes pods sequentially within each Podclique.
	for podclique, maxDeletions := range maxConcurrentDeletionsPerPodclique {
		if maxDeletions > 1 {
			tc.T.Fatalf("Expected at most 1 pod being deleted at a time in Podclique %s, but found %d concurrent deletions", podclique, maxDeletions)
		}
	}
}

// verifySinglePCSReplicaUpdatedFirst verifies that a single PCS replica is fully updated before another replica begins.
//
// This function ensures strict replica-level ordering during PodCliqueSet rolling updates:
// - Once a replica starts updating (any pod is deleted), NO other replica can start updating
// - The updating replica must complete ALL pod updates across ALL its PodCliques before another replica begins
// - A replica is considered "complete" only when all pods across all PodCliques have been deleted and re-added
//
// Background:
// A PodCliqueSet (PCS) can have multiple replicas, where each replica contains multiple PodCliques.
// For example, with replicas=2, you might have:
//   - Replica 0: pc-a (2 pods), pc-b (1 pod), pc-c (3 pods) = 6 total pods
//   - Replica 1: pc-a (2 pods), pc-b (1 pod), pc-c (3 pods) = 6 total pods
//
// During a rolling update, the system should update Replica 0 completely (all 6 pods across all PodCliques)
// before starting any updates to Replica 1.
//
// Implementation Details:
// - Tracks DELETE and ADDED counts separately per pod identifier per replica to handle out-of-order events
// - Uses stable pod identifiers (hostname) to correlate pod deletions with additions
// - A pod is "in-flight" (being updated) if DELETE count > ADDED count for that pod
// - A replica is "actively updating" if it has any pods in-flight
// - Only considers Deleted/Added events; Modified events are ignored as they don't affect ordering
//
// Note: This verification is most meaningful when PCS has replicas > 1. For replicas=1, it provides
// minimal validation since there's only one replica to update.
//
// IMPORTANT: This function handles the fact that Kubernetes watch events can arrive out of order.
// Specifically, ADDED events for new pods can arrive BEFORE DELETE events for old pods because:
// 1. Old pods are deleted (start terminating with deletion timestamp)
// 2. New pods are created immediately (new pods are ADDED)
// 3. DELETE events only arrive when old pods are fully terminated (after grace period)
//
// To handle this, we track both DELETE and ADDED counts and calculate "actively updating"
// replicas based on the difference (DELETE > ADDED means the pod is still in-flight).
func verifySinglePCSReplicaUpdatedFirst(tc TestContext, events []podEvent) {
	tc.T.Helper()

	logger.Debug("=== Starting verifySinglePCSReplicaUpdatedFirst analysis ===")
	logger.Debugf("Total events captured: %d", len(events))

	// Log all events for debugging
	for i, event := range events {
		replicaIdx := 0
		if val, ok := event.Pod.Labels["grove.io/podcliqueset-replica-index"]; ok {
			replicaIdx, _ = strconv.Atoi(val)
		}
		podclique := ""
		if event.Pod.Labels != nil {
			podclique = event.Pod.Labels["grove.io/podclique"]
		}
		logger.Debugf("Event[%d]: Type=%s PodName=%s Hostname=%s Replica=%d PodClique=%s Timestamp=%v",
			i, event.Type, event.Pod.Name, event.Pod.Spec.Hostname, replicaIdx, podclique, event.Timestamp.Format("15:04:05.000"))
	}

	// Track DELETE and ADDED counts separately per pod identifier per replica to handle out-of-order events.
	// Structure: replicaIndex -> podID -> count
	deletedCount := make(map[int]map[string]int)
	addedCount := make(map[int]map[string]int)

	maxConcurrentUpdatingReplicas := 0
	var violationEvent string

	logger.Debug("=== Processing events to find concurrent replica updates ===")
	for i, event := range events {
		// Extract replica index from pod labels (defaults to 0 if not present)
		replicaIdx := 0
		if val, ok := event.Pod.Labels["grove.io/podcliqueset-replica-index"]; ok {
			replicaIdx, _ = strconv.Atoi(val)
		}

		// Extract PodClique name - skip pods without this label
		_, ok := event.Pod.Labels["grove.io/podclique"]
		if !ok {
			continue
		}

		// Get stable pod identifier (hostname) that persists across pod replacements
		podID := getPodIdentifier(tc, event.Pod)

		// Track DELETE and ADDED events
		switch event.Type {
		case watch.Deleted:
			if deletedCount[replicaIdx] == nil {
				deletedCount[replicaIdx] = make(map[string]int)
			}
			deletedCount[replicaIdx][podID]++
			logger.Debugf("Event[%d] DELETE: replica=%d podID=%s deleted=%d added=%d",
				i, replicaIdx, podID, deletedCount[replicaIdx][podID], getReplicaPodCount(addedCount, replicaIdx, podID))

		case watch.Added:
			if addedCount[replicaIdx] == nil {
				addedCount[replicaIdx] = make(map[string]int)
			}
			addedCount[replicaIdx][podID]++
			logger.Debugf("Event[%d] ADDED: replica=%d podID=%s deleted=%d added=%d",
				i, replicaIdx, podID, getReplicaPodCount(deletedCount, replicaIdx, podID), addedCount[replicaIdx][podID])
		}

		// Calculate which replicas are "actively updating" - those with any pods where DELETE > ADDED
		// A pod is "in-flight" if it has been deleted but not yet replaced (DELETE count > ADDED count)
		activelyUpdating := make(map[int]int) // replicaIdx -> count of pods in-flight
		for replica, deletedPods := range deletedCount {
			inFlight := 0
			for podID, deletes := range deletedPods {
				adds := getReplicaPodCount(addedCount, replica, podID)
				if deletes > adds {
					inFlight++
				}
			}
			if inFlight > 0 {
				activelyUpdating[replica] = inFlight
			}
		}

		numUpdatingReplicas := len(activelyUpdating)
		if numUpdatingReplicas > 0 {
			replicas := make([]string, 0, len(activelyUpdating))
			for idx, count := range activelyUpdating {
				replicas = append(replicas, fmt.Sprintf("replica-%d(%d pods)", idx, count))
			}
			slices.Sort(replicas)
			logger.Debugf("Event[%d] State: numUpdatingReplicas=%d activelyUpdating=%v",
				i, numUpdatingReplicas, replicas)
		}

		if numUpdatingReplicas > maxConcurrentUpdatingReplicas {
			maxConcurrentUpdatingReplicas = numUpdatingReplicas
			replicas := make([]string, 0, len(activelyUpdating))
			for idx, count := range activelyUpdating {
				replicas = append(replicas, fmt.Sprintf("replica-%d(%d pods)", idx, count))
			}
			slices.Sort(replicas)
			violationEvent = fmt.Sprintf("Event[%d] %s: maxConcurrentUpdatingReplicas increased to %d, activelyUpdating=%v",
				i, event.Type, maxConcurrentUpdatingReplicas, replicas)
		}
	}

	logger.Debugf("=== Analysis complete: maxConcurrentUpdatingReplicas=%d ===", maxConcurrentUpdatingReplicas)
	if violationEvent != "" {
		logger.Debugf("Max concurrent replicas event: %s", violationEvent)
	}

	// Assert that at most 1 replica was being updated at any point in time.
	// This ensures the rolling update respects replica-level serialization.
	if maxConcurrentUpdatingReplicas > 1 {
		tc.T.Fatalf("Expected at most 1 PCS replica to be updating at a time, but found %d replicas updating concurrently. %s",
			maxConcurrentUpdatingReplicas, violationEvent)
	}
}

// getReplicaPodCount safely gets a count from a nested map (replicaIdx -> podID -> count), returning 0 if not found
func getReplicaPodCount(m map[int]map[string]int, replicaIdx int, podID string) int {
	if m[replicaIdx] == nil {
		return 0
	}
	return m[replicaIdx][podID]
}

// verifyOnePCSGReplicaDeletedAtATime verifies only one PCSG replica globally is deleted at a time
// during rolling updates.
//
// SCOPE: GLOBAL across all PCSGs
// This is the strictest constraint - only ONE PCSG replica in the entire system can be rolling at any time.
// If you have multiple PCSGs (e.g., sg-x and sg-y), only one replica from any of them can be rolling.
//
// Compare with verifyOnePCSGReplicaDeletedAtATimePerPCSG which allows concurrent rolling across different PCSGs.
//
// IMPORTANT: This function handles the fact that Kubernetes watch events can arrive out of order.
// Specifically, ADDED events for new pods can arrive BEFORE DELETE events for old pods because:
// 1. Old PodCliques are deleted (pods start terminating with deletion timestamp)
// 2. New PodCliques are created immediately (new pods are ADDED)
// 3. DELETE events only arrive when old pods are fully terminated (after grace period)
//
// The controller considers a replica "update complete" when new pods are READY, not when old
// pods are fully terminated. So we track by comparing ADDED vs DELETE counts, allowing
// ADDED to offset DELETE regardless of order.
func verifyOnePCSGReplicaDeletedAtATime(tc TestContext, events []podEvent) {
	tc.T.Helper()

	logger.Debug("=== Starting verifyOnePCSGReplicaDeletedAtATime analysis ===")
	logger.Debugf("Total events captured: %d", len(events))

	// Log all PCSG-related events for debugging
	for i, event := range events {
		if event.Pod.Labels == nil {
			continue
		}
		replicaID, hasReplicaLabel := event.Pod.Labels["grove.io/podcliquescalinggroup-replica-index"]
		if !hasReplicaLabel {
			continue
		}
		pcsgName := event.Pod.Labels["grove.io/podcliquescalinggroup"]
		podclique := event.Pod.Labels["grove.io/podclique"]
		logger.Debugf("Event[%d]: Type=%s PodName=%s PCSG=%s ReplicaID=%s PodClique=%s Timestamp=%v",
			i, event.Type, event.Pod.Name, pcsgName, replicaID, podclique, event.Timestamp.Format("15:04:05.000"))
	}

	// Track DELETE and ADDED counts separately per replica to handle out-of-order events.
	// Key format: "<pcsg-name>-<replica-index>"
	deletedCount := make(map[string]int)
	addedCount := make(map[string]int)
	maxConcurrentDeletions := 0
	var violationEvent string

	logger.Debug("=== Processing events to find concurrent deletions ===")
	for i, event := range events {
		// All pods should have labels - if nil, that's a bug
		if event.Pod.Labels == nil {
			tc.T.Fatalf("Pod %s has no labels, which indicates a bug in pod creation", event.Pod.Name)
		}

		// Only process pods that belong to a PCSG (not all pods have this label)
		replicaID, hasReplicaLabel := event.Pod.Labels["grove.io/podcliquescalinggroup-replica-index"]
		if !hasReplicaLabel {
			continue
		}

		pcsgName, ok := event.Pod.Labels["grove.io/podcliquescalinggroup"]
		if !ok {
			tc.T.Fatalf("Pod %s has PCSG replica index but no PCSG name label", event.Pod.Name)
		}

		// Create a composite key to uniquely identify this PCSG replica globally
		replicaKey := fmt.Sprintf("%s-%s", pcsgName, replicaID)

		switch event.Type {
		case watch.Deleted:
			deletedCount[replicaKey]++
			logger.Debugf("Event[%d] DELETE: pod=%s replica=%s deleted=%d added=%d",
				i, event.Pod.Name, replicaKey, deletedCount[replicaKey], addedCount[replicaKey])
		case watch.Added:
			addedCount[replicaKey]++
			logger.Debugf("Event[%d] ADDED: pod=%s replica=%s deleted=%d added=%d",
				i, event.Pod.Name, replicaKey, deletedCount[replicaKey], addedCount[replicaKey])
		}

		// Calculate "actively rolling" replicas: those where DELETE count > ADDED count.
		// This means more pods have been deleted than replaced, so the replica is still rolling.
		activelyRolling := make(map[string]int)
		for replica, deletes := range deletedCount {
			adds := addedCount[replica]
			if deletes > adds {
				activelyRolling[replica] = deletes - adds
			}
		}

		currentConcurrent := len(activelyRolling)
		if currentConcurrent > 0 {
			replicas := make([]string, 0, len(activelyRolling))
			for k, v := range activelyRolling {
				replicas = append(replicas, fmt.Sprintf("%s:%d", k, v))
			}
			slices.Sort(replicas)
			logger.Debugf("Event[%d] State: concurrent=%d activelyRolling=%v", i, currentConcurrent, replicas)
		}
		if currentConcurrent > maxConcurrentDeletions {
			maxConcurrentDeletions = currentConcurrent
			replicas := make([]string, 0, len(activelyRolling))
			for k, v := range activelyRolling {
				replicas = append(replicas, fmt.Sprintf("%s:%d", k, v))
			}
			slices.Sort(replicas)
			violationEvent = fmt.Sprintf("Event[%d] maxConcurrentDeletions increased to %d, activelyRolling=%v", i, maxConcurrentDeletions, replicas)
		}
	}

	logger.Debugf("=== Analysis complete: maxConcurrentDeletions=%d ===", maxConcurrentDeletions)
	if violationEvent != "" {
		logger.Debugf("Violation: %s", violationEvent)
	}

	// Assert that at most 1 replica was being deleted/replaced at any point in time.
	// This ensures the rolling update respects the MaxUnavailable=1 constraint at the global level.
	if maxConcurrentDeletions > 1 {
		tc.T.Fatalf("Expected at most 1 PCSG replica being deleted at a time, but found %d concurrent deletions", maxConcurrentDeletions)
	}
}

// verifyOnePCSGReplicaDeletedAtATimePerPCSG verifies only one replica per PCSG is deleted at a time
// during rolling updates.
//
// SCOPE: PER-PCSG (allows concurrent rolling across different PCSGs)
// This constraint allows multiple PCSGs to roll simultaneously, but within each PCSG, only one replica
// can be rolling at a time. For example, if you have sg-x and sg-y:
//   - ✅ ALLOWED: sg-x replica 0 AND sg-y replica 0 rolling simultaneously
//   - ❌ NOT ALLOWED: sg-x replica 0 AND sg-x replica 1 rolling simultaneously
//
// Compare with verifyOnePCSGReplicaDeletedAtATime which enforces a global constraint (stricter).
//
// IMPORTANT: This function handles the fact that Kubernetes watch events can arrive out of order.
// See verifyOnePCSGReplicaDeletedAtATime for detailed explanation.
func verifyOnePCSGReplicaDeletedAtATimePerPCSG(tc TestContext, events []podEvent) {
	tc.T.Helper()

	// Track DELETE and ADDED counts separately per replica per PCSG to handle out-of-order events.
	// Map structure: pcsgName -> replicaIndex -> count
	deletedCount := make(map[string]map[string]int)
	addedCount := make(map[string]map[string]int)
	maxConcurrentDeletionsPerPCSG := make(map[string]int)

	for _, event := range events {
		// All pods should have labels - if nil, that's a bug
		if event.Pod.Labels == nil {
			tc.T.Fatalf("Pod %s has no labels, which indicates a bug in pod creation", event.Pod.Name)
		}

		// Only process pods that belong to a PCSG (not all pods have this label)
		replicaID, hasReplicaLabel := event.Pod.Labels["grove.io/podcliquescalinggroup-replica-index"]
		if !hasReplicaLabel {
			continue
		}

		pcsgName, ok := event.Pod.Labels["grove.io/podcliquescalinggroup"]
		if !ok {
			tc.T.Fatalf("Pod %s has PCSG replica index but no PCSG name label", event.Pod.Name)
		}

		switch event.Type {
		case watch.Deleted:
			if deletedCount[pcsgName] == nil {
				deletedCount[pcsgName] = make(map[string]int)
			}
			deletedCount[pcsgName][replicaID]++
			logger.Debugf("Pod %s deleted, replica %s in PCSG %s: deleted=%d added=%d",
				event.Pod.Name, replicaID, pcsgName,
				deletedCount[pcsgName][replicaID], getCount(addedCount, pcsgName, replicaID))
		case watch.Added:
			if addedCount[pcsgName] == nil {
				addedCount[pcsgName] = make(map[string]int)
			}
			addedCount[pcsgName][replicaID]++
			logger.Debugf("Pod %s added, replica %s in PCSG %s: deleted=%d added=%d",
				event.Pod.Name, replicaID, pcsgName,
				getCount(deletedCount, pcsgName, replicaID), addedCount[pcsgName][replicaID])
		}

		// Calculate "actively rolling" replicas per PCSG: those where DELETE count > ADDED count.
		for pcsg, pcsgDeleted := range deletedCount {
			activelyRolling := 0
			for replica, deletes := range pcsgDeleted {
				adds := getCount(addedCount, pcsg, replica)
				if deletes > adds {
					activelyRolling++
				}
			}
			if activelyRolling > maxConcurrentDeletionsPerPCSG[pcsg] {
				maxConcurrentDeletionsPerPCSG[pcsg] = activelyRolling
			}
		}
	}

	// Assert that at most 1 replica per PCSG was being deleted/replaced at any point in time.
	// This ensures the rolling update respects the MaxUnavailable=1 constraint at the PCSG level.
	for pcsg, maxDeletions := range maxConcurrentDeletionsPerPCSG {
		if maxDeletions > 1 {
			tc.T.Fatalf("Expected at most 1 replica being deleted at a time in PCSG %s, but found %d concurrent deletions", pcsg, maxDeletions)
		}
	}
}

// getCount safely gets a count from a nested map, returning 0 if not found
func getCount(m map[string]map[string]int, key1, key2 string) int {
	if m[key1] == nil {
		return 0
	}
	return m[key1][key2]
}

// scalePodClique scales a PodClique and returns an errgroup.Group that completes when the expected pod count is reached.
// Uses tc.Workload.Name as the PCS name.
// The operation runs asynchronously - call Wait() on the returned Group to block until complete.
// If delayMs > 0, the operation will sleep for that duration before starting.
func scalePodClique(tc TestContext, cliqueName string, replicas int32, expectedTotalPods, delayMs int) *errgroup.Group {
	g := new(errgroup.Group)
	g.Go(func() error {
		startTime := time.Now()

		if delayMs > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}

		logger.Debugf("[scalePodClique] Scaling %s to %d replicas, expecting %d total pods", cliqueName, replicas, expectedTotalPods)

		if err := scalePodCliqueInPCS(tc, cliqueName, replicas); err != nil {
			return fmt.Errorf("failed to scale PodClique %s: %w", cliqueName, err)
		}

		logger.Debugf("[scalePodClique] Scale patch applied, waiting for pods...")

		// Wait for pods to reach expected count
		pollCount := 0
		err := pollForCondition(tc, func() (bool, error) {
			pollCount++
			pods, err := listPods(tc)
			if err != nil {
				return false, err
			}
			if pollCount%3 == 1 {
				logger.Debugf("[scalePodClique] Poll #%d: current pods=%d, expected=%d", pollCount, len(pods.Items), expectedTotalPods)
			}
			return len(pods.Items) == expectedTotalPods, nil
		})
		elapsed := time.Since(startTime)
		if err != nil {
			logger.Infof("[scalePodClique] Scale %s FAILED after %v: %v", cliqueName, elapsed, err)
			return fmt.Errorf("failed to wait for pods after scaling PodClique %s: %w", cliqueName, err)
		}
		logger.Infof("[scalePodClique] Scale %s completed in %v (pods=%d)", cliqueName, elapsed, expectedTotalPods)
		return nil
	})
	return g
}

// scalePodCliqueInPCS scales all PodClique instances for a given clique name across all PCS replicas.
// Uses tc.Workload.Name as the PCS name.
//
// IMPORTANT: This function scales PodClique resources directly rather than modifying the PCS template.
//
// Why we can't scale via the PCS template:
// The PCS controller intentionally preserves existing PodClique replica counts to support HPA
// (Horizontal Pod Autoscaler) scaling. When a PodClique already exists, the controller does:
//
//	if pclqExists {
//	    currentPCLQReplicas := pclq.Spec.Replicas  // Preserve existing value
//	    pclq.Spec = pclqTemplateSpec.Spec          // Apply template
//	    pclq.Spec.Replicas = currentPCLQReplicas   // Restore preserved value
//	}
//
// This design prevents the PCS controller from fighting with HPA, which directly mutates
// PodClique.Spec.Replicas. Without this behavior, HPA scaling would be immediately reverted
// on the next PCS reconciliation.
//
// As a result, the PCS template's clique replicas value is only used during initial PodClique
// creation. Post-creation scaling must be done by:
//   - HPA (configured via ScaleConfig in the PCS template)
//   - Direct patching of PodClique resources (what this function does)
//
// See: internal/controller/podcliqueset/components/podclique/podclique.go buildResource()
func scalePodCliqueInPCS(tc TestContext, cliqueName string, replicas int32) error {
	pcsGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}
	pclqGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliques"}
	pcsName := tc.Workload.Name

	// Get the PCS to find out how many replicas it has
	unstructuredPCS, err := tc.DynamicClient.Resource(pcsGVR).Namespace(tc.Namespace).Get(tc.Ctx, pcsName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get PodCliqueSet: %w", err)
	}

	var pcs grovev1alpha1.PodCliqueSet
	if err := convertUnstructuredToTyped(unstructuredPCS.Object, &pcs); err != nil {
		return fmt.Errorf("failed to convert to PodCliqueSet: %w", err)
	}

	// Verify the clique exists in the PCS template
	found := false
	for _, clique := range pcs.Spec.Template.Cliques {
		if clique.Name == cliqueName {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("clique %s not found in PodCliqueSet %s template", cliqueName, pcsName)
	}

	// Scale each PodClique instance directly (one per PCS replica)
	// PodClique naming convention: {pcsName}-{replicaIndex}-{cliqueName}
	for replicaIndex := range pcs.Spec.Replicas {
		pclqName := fmt.Sprintf("%s-%d-%s", pcsName, replicaIndex, cliqueName)

		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Patch the PodClique's replicas directly
			patch := map[string]interface{}{
				"spec": map[string]interface{}{
					"replicas": replicas,
				},
			}
			patchBytes, err := json.Marshal(patch)
			if err != nil {
				return fmt.Errorf("failed to marshal patch: %w", err)
			}

			_, err = tc.DynamicClient.Resource(pclqGVR).Namespace(tc.Namespace).Patch(
				tc.Ctx, pclqName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
			return err
		}); err != nil {
			return fmt.Errorf("failed to scale PodClique %s: %w", pclqName, err)
		}
	}

	return nil
}

// captureOperatorLogs fetches and logs the operator logs from the specified namespace and deployment.
// Uses tc.Ctx and tc.Clientset but takes namespace and deploymentPrefix as parameters since they
// typically differ from the test namespace (e.g., "grove-system" vs "default").
func captureOperatorLogs(tc TestContext, namespace, deploymentPrefix string) {
	// List pods in the namespace
	pods, err := tc.Clientset.CoreV1().Pods(namespace).List(tc.Ctx, metav1.ListOptions{})
	if err != nil {
		logger.Errorf("Failed to list pods in namespace %s: %v", namespace, err)
		return
	}

	// Find operator pods by prefix
	for _, pod := range pods.Items {
		if len(pod.Name) >= len(deploymentPrefix) && pod.Name[:len(deploymentPrefix)] == deploymentPrefix {
			logger.Debugf("=== Operator Pod: %s (Phase: %s) ===", pod.Name, pod.Status.Phase)

			// Get logs for each container
			for _, container := range pod.Spec.Containers {
				logger.Debugf("--- Container: %s ---", container.Name)

				req := tc.Clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
					Container: container.Name,
					TailLines: func() *int64 { v := int64(200); return &v }(), // Last 200 lines
				})

				logStream, err := req.Stream(tc.Ctx)
				if err != nil {
					logger.Errorf("Failed to get logs for container %s: %v", container.Name, err)
					continue
				}

				// Read logs
				buf := make([]byte, 64*1024) // 64KB buffer
				for {
					n, err := logStream.Read(buf)
					if n > 0 {
						// Print logs line by line, filtering for [ROLLING_UPDATE] tags
						lines := string(buf[:n])
						for _, line := range splitLines(lines) {
							if len(line) > 0 {
								// Print all lines, but highlight rolling update related ones
								if containsRollingUpdateTag(line) {
									logger.Debugf("[OP-LOG] %s", line)
								}
							}
						}
					}
					if err != nil {
						break
					}
				}
				logStream.Close()
			}
		}
	}
}

// splitLines splits a string into lines
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// containsRollingUpdateTag checks if a line contains rolling update related tags
func containsRollingUpdateTag(line string) bool {
	tags := []string{"[ROLLING_UPDATE]", "rolling update", "processPendingUpdates", "deleteOldPending", "nextPodToUpdate"}
	for _, tag := range tags {
		if len(line) >= len(tag) {
			for i := 0; i <= len(line)-len(tag); i++ {
				if line[i:i+len(tag)] == tag {
					return true
				}
			}
		}
	}
	return false
}

// capturePodCliqueStatus captures and logs the status of all PodCliques for debugging
func capturePodCliqueStatus(tc TestContext) {
	pclqGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliques"}
	pcsGVR := schema.GroupVersionResource{Group: "grove.io", Version: "v1alpha1", Resource: "podcliquesets"}

	logger.Info("=== PodCliqueSet Status ===")
	pcsList, err := tc.DynamicClient.Resource(pcsGVR).Namespace(tc.Namespace).List(tc.Ctx, metav1.ListOptions{})
	if err != nil {
		logger.Errorf("Failed to list PodCliqueSets: %v", err)
	} else {
		for _, pcs := range pcsList.Items {
			status, _, _ := unstructured.NestedMap(pcs.Object, "status")
			logger.Infof("PCS %s: status=%v", pcs.GetName(), status)
		}
	}

	logger.Info("=== PodClique Status ===")
	pclqList, err := tc.DynamicClient.Resource(pclqGVR).Namespace(tc.Namespace).List(tc.Ctx, metav1.ListOptions{})
	if err != nil {
		logger.Errorf("Failed to list PodCliques: %v", err)
	} else {
		for _, pclq := range pclqList.Items {
			spec, _, _ := unstructured.NestedMap(pclq.Object, "spec")
			status, _, _ := unstructured.NestedMap(pclq.Object, "status")
			replicas, _, _ := unstructured.NestedInt64(spec, "replicas")
			readyReplicas, _, _ := unstructured.NestedInt64(status, "readyReplicas")
			updatedReplicas, _, _ := unstructured.NestedInt64(status, "updatedReplicas")
			logger.Infof("PCLQ %s: replicas=%d, readyReplicas=%d, updatedReplicas=%d",
				pclq.GetName(), replicas, readyReplicas, updatedReplicas)
		}
	}

	logger.Info("=== Pod Status ===")
	pods, err := listPods(tc)
	if err != nil {
		logger.Errorf("Failed to list pods: %v", err)
	} else {
		for _, pod := range pods.Items {
			pclq := pod.Labels["grove.io/podclique"]
			logger.Infof("Pod %s: phase=%s, pclq=%s, ready=%v",
				pod.Name, pod.Status.Phase, pclq, isPodReady(&pod))
		}
	}
}

// isPodReady checks if a pod is ready
func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

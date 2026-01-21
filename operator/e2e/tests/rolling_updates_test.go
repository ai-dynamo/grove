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
	"k8s.io/apimachinery/pkg/watch"
)

// Test_RU7_RollingUpdatePCSPodClique tests rolling update when PCS-owned Podclique spec is updated
// Scenario RU-7:
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Change the specification of pc-a
// 4. Verify that only one pod is deleted at a time
// 5. Verify that a single PCS replica is updated first before moving to another
func Test_RU7_RollingUpdatePCSPodClique(t *testing.T) {
	logger.Info("1. Initialize a 10-node Grove cluster")
	logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	tc, cleanup, tracker := setupRollingUpdateTest(t, RollingUpdateTestConfig{
		WorkerNodes:  10,
		ExpectedPods: 10,
	})
	defer cleanup()

	logger.Info("3. Change the specification of pc-a")
	if err := triggerPodCliqueRollingUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	// With MaxSurge rolling update strategy, each pod requires: create surge pod -> wait Ready -> delete old pod
	// This takes longer than delete-first approach, so we use a longer timeout
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 5 * time.Minute
	if err := waitForRollingUpdateComplete(tcLongTimeout, 1); err != nil {
		// Diagnostics will be collected automatically by cleanup on test failure
		t.Fatalf("Failed to wait for rolling update to complete: %v", err)
	}

	logger.Info("4. Verify that only one pod is deleted at a time")
	tracker.Stop()
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
	logger.Info("1. Initialize a 10-node Grove cluster")
	logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	tc, cleanup, tracker := setupRollingUpdateTest(t, RollingUpdateTestConfig{
		WorkerNodes:  10,
		ExpectedPods: 10,
	})
	defer cleanup()

	logger.Info("3. Change the specification of pc-b")
	if err := triggerPodCliqueRollingUpdate(tc, "pc-b"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	tcLongTimeout := tc
	tcLongTimeout.Timeout = 1 * time.Minute
	if err := waitForRollingUpdateComplete(tcLongTimeout, 1); err != nil {
		t.Fatalf("Failed to wait for rolling update to complete: %v", err)
	}

	logger.Info("4. Verify that only one PCSG replica is deleted at a time")
	tracker.Stop()
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
	logger.Info("1. Initialize a 10-node Grove cluster")
	logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	tc, cleanup, tracker := setupRollingUpdateTest(t, RollingUpdateTestConfig{
		WorkerNodes:  10,
		ExpectedPods: 10,
	})
	defer cleanup()

	logger.Info("3. Change the specification of pc-a, pc-b and pc-c")
	for _, cliqueName := range []string{"pc-a", "pc-b", "pc-c"} {
		if err := triggerPodCliqueRollingUpdate(tc, cliqueName); err != nil {
			t.Fatalf("Failed to update PodClique %s spec: %v", cliqueName, err)
		}
	}

	if err := waitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("Failed to wait for rolling update to complete: %v", err)
	}

	logger.Info("4. Verify that only one pod in each Podclique and one replica in each PCSG is deleted at a time")
	tracker.Stop()
	events := tracker.getEvents()
	verifySinglePCSReplicaUpdatedFirst(tc, events)
	verifyOnePodDeletedAtATimePerPodclique(tc, events)
	verifyOnePCSGReplicaDeletedAtATimePerPCSG(tc, events)

	logger.Info("5. Verify that a single PCS replica is updated first before moving to another")
	verifySinglePCSReplicaUpdatedFirst(tc, events)

	logger.Info("🎉 Rolling Update on all Podcliques test (RU-9) completed successfully!")
}

// Test_RU10_RollingUpdateInsufficientResources tests rolling update behavior with insufficient resources.
// This test verifies the "delete-first" strategy: when nodes are cordoned, the operator deletes
// one old pod and creates a new one (which remains Pending). No further progress is made until
// nodes are uncordoned, at which point the rolling update completes.
// Scenario RU-10:
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Cordon all worker nodes
// 4. Change the specification of pc-a
// 5. Verify exactly one pod is deleted and a new Pending pod is created (delete-first strategy)
// 6. Uncordon the nodes, and verify the rolling update completes
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
	logger.Debugf("Captured %d existing pods before rolling update", len(existingPodNames))

	tracker := newRollingUpdateTracker()
	if err := tracker.Start(tc); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer tracker.Stop()

	logger.Info("4. Change the specification of pc-a")
	if err := triggerPodCliqueRollingUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	logger.Info("5. Verify exactly one pod is deleted and a new Pending pod is created (delete-first strategy)")

	// Poll until we see exactly 1 pod deleted and 1 new pod created, verifying delete-first behavior
	pollErr := utils.PollForCondition(ctx, 2*time.Minute, 2*time.Second, func() (bool, error) {
		events := tracker.getEvents()
		var deletedExistingPods []string
		var addedPods []string

		for _, event := range events {
			switch event.Type {
			case watch.Deleted:
				if existingPodNames[event.Pod.Name] {
					deletedExistingPods = append(deletedExistingPods, event.Pod.Name)
					logger.Debugf("Existing pod deleted during rolling update: %s", event.Pod.Name)
				}
			case watch.Added:
				if !existingPodNames[event.Pod.Name] {
					addedPods = append(addedPods, event.Pod.Name)
					logger.Debugf("New pod created during rolling update: %s", event.Pod.Name)
				}
			}
		}

		// Check if we've reached the expected delete-first state
		deletedCount := len(deletedExistingPods)
		addedCount := len(addedPods)

		// If more than 1 pod was deleted, the test should fail (not delete-first)
		if deletedCount > 1 {
			return false, fmt.Errorf("rolling update progressed beyond first deletion - expected 1 pod deleted but got %d: %v (not delete-first strategy)", deletedCount, deletedExistingPods)
		}

		// Success: exactly 1 deleted and 1 new pod created
		if deletedCount == 1 && addedCount == 1 {
			logger.Infof("✅ Delete-first strategy verified: 1 pod deleted (%v), 1 new pod created (%v)", deletedExistingPods, addedPods)
			return true, nil
		}

		// Still waiting for the condition
		logger.Debugf("Waiting for delete-first state: deleted=%d (want 1), added=%d (want 1)", deletedCount, addedCount)
		return false, nil
	})

	if pollErr != nil {
		t.Fatalf("Failed to verify delete-first strategy: %v", pollErr)
	}

	logger.Info("6. Uncordon the nodes, and verify the rolling update completes")
	uncordonNodes(tc, workerNodes)

	// Wait for rolling update to complete after uncordoning
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 5 * time.Minute
	if err := waitForRollingUpdateComplete(tcLongTimeout, 1); err != nil {
		logger.Info("=== Rolling update timed out - capturing debug info ===")
		t.Fatalf("Failed to wait for rolling update to complete: %v", err)
	}

	logger.Info("🎉 Rolling Update with insufficient resources test (RU-10) completed successfully!")
}

// Test_RU11_RollingUpdateWithPCSScaleOut tests rolling update with scale-out on PCS
// Scenario RU-11:
// 1. Initialize a 30-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Change the specification of pc-a
// 4. Scale out the PCS during the rolling update
// 5. Verify the scaled out replica is created with the correct specifications
func Test_RU11_RollingUpdateWithPCSScaleOut(t *testing.T) {
	logger.Info("1. Initialize a 30-node Grove cluster")
	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
	tc, cleanup, tracker := setupRollingUpdateTest(t, RollingUpdateTestConfig{
		WorkerNodes:        30,
		ExpectedPods:       10,
		PatchSIGTERM:       true,
		InitialPCSReplicas: 2,
		PostScalePods:      20,
	})
	defer cleanup()

	logger.Info("3. Change the specification of pc-a")
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 2 * time.Minute
	updateWait := triggerRollingUpdate(tcLongTimeout, 3, "pc-a")

	logger.Info("4. Scale out the PCS during the rolling update (in parallel)")
	scaleWait := scalePCS(tcLongTimeout, "workload1", 3, 30, 0, 100) // 100ms delay so update is "first"

	if err := <-updateWait; err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := <-scaleWait; err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	logger.Info("5. Verify the scaled out replica is created with the correct specifications")
	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	if len(pods.Items) != 30 {
		t.Fatalf("Expected 30 pods, got %d", len(pods.Items))
	}

	tracker.Stop()
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
	logger.Info("1. Initialize a 30-node Grove cluster")
	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
	tc, cleanup, tracker := setupRollingUpdateTest(t, RollingUpdateTestConfig{
		WorkerNodes:        30,
		ExpectedPods:       10,
		PatchSIGTERM:       true,
		InitialPCSReplicas: 2,
		PostScalePods:      20,
	})
	defer cleanup()

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
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 2 * time.Minute
	scaleWait := scalePCS(tcLongTimeout, "workload1", 1, 10, 0, 0) // No delay since update already in progress

	logger.Info("5. Verify the update goes through successfully")
	updateWait := waitForRollingUpdate(tcLongTimeout, 1)

	if err := <-updateWait; err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := <-scaleWait; err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	if len(pods.Items) != 10 {
		t.Fatalf("Expected 10 pods, got %d", len(pods.Items))
	}
	tracker.Stop()

	logger.Info("🎉 Rolling Update with PCS scale-in during update test (RU-12) completed successfully!")
}

/* This test is flaky. It sometimes fails with "Failed to wait for rolling update to complete: condition not met..."
// Test_RU13_RollingUpdateWithPCSScaleInAfterFinalOrdinal tests rolling update with scale-in on PCS after final ordinal finishes
// Scenario RU-13:
// 1. Initialize a 20-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Change the specification of pc-a, pc-b and pc-c
// 4. Wait for rolling update to complete on replica 1
// 5. Scale in the PCS after final ordinal has been updated
// 6. Verify the update goes through successfully
func Test_RU13_RollingUpdateWithPCSScaleInAfterFinalOrdinal(t *testing.T) {
	logger.Info("1. Initialize a 20-node Grove cluster")
	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
	tc, cleanup, tracker := setupRollingUpdateTest(t, RollingUpdateTestConfig{
		WorkerNodes:        20,
		ExpectedPods:       10,
		InitialPCSReplicas: 2,
		PostScalePods:      20,
	})
	defer cleanup()

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
	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	if len(pods.Items) != 10 {
		t.Fatalf("Expected 10 pods, got %d", len(pods.Items))
	}
	tracker.Stop()

	logger.Info("🎉 Rolling Update with PCS scale-in after final ordinal test (RU-13) completed successfully!")
}
*/

/* This test is flaky. It sometimes fails with rolling_updates_test.go:454: Rolling update failed: condition not met within timeout
// Test_RU14_RollingUpdateWithPCSGScaleOutDuringUpdate tests rolling update with scale-out on PCSG being updated
// Scenario RU-14:
// 1. Initialize a 28-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Change the specification of pc-a, pc-b and pc-c
// 4. Scale out the PCSG during its rolling update
// 5. Verify the scaled out replica is created with the correct specifications
// 6. Verify it should not be updated again before the rolling update ends
func Test_RU14_RollingUpdateWithPCSGScaleOutDuringUpdate(t *testing.T) {
	logger.Info("1. Initialize a 28-node Grove cluster")
	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
	tc, cleanup, tracker := setupRollingUpdateTest(t, RollingUpdateTestConfig{
		WorkerNodes:        28,
		ExpectedPods:       10,
		InitialPCSReplicas: 2,
		PostScalePods:      20,
	})
	defer cleanup()

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
	if err := <-updateWait; err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := <-scaleWait; err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	if len(pods.Items) != 28 {
		t.Fatalf("Expected 28 pods, got %d", len(pods.Items))
	}
	tracker.Stop()

	logger.Info("🎉 Rolling Update with PCSG scale-out during update test (RU-14) completed successfully!")
}
*/

/* This test is flaky. It sometimes fails with "rolling_updates_test.go:516: Expected 28 pods, got 30"
// Test_RU15_RollingUpdateWithPCSGScaleOutBeforeUpdate tests rolling update with scale-out on PCSG before it is updated
// Scenario RU-15:
// 1. Initialize a 28-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Change the specification of pc-a, pc-b and pc-c
// 4. Scale out the PCSG before its rolling update starts
// 5. Verify the scaled out replica is created with the correct specifications
// 6. Verify it should not be updated again before the rolling update ends
func Test_RU15_RollingUpdateWithPCSGScaleOutBeforeUpdate(t *testing.T) {
	logger.Info("1. Initialize a 28-node Grove cluster")
	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
	tc, cleanup, tracker := setupRollingUpdateTest(t, RollingUpdateTestConfig{
		WorkerNodes:        28,
		ExpectedPods:       10,
		InitialPCSReplicas: 2,
		PostScalePods:      20,
	})
	defer cleanup()

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

	if err := <-updateWait; err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := <-scaleWait; err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	if len(pods.Items) != 28 {
		t.Fatalf("Expected 28 pods, got %d", len(pods.Items))
	}
	tracker.Stop()

	logger.Info("🎉 Rolling Update with PCSG scale-out before update test (RU-15) completed successfully!")
}
*/

// Test_RU16_RollingUpdateWithPCSGScaleInDuringUpdate tests rolling update with scale-in on PCSG being updated
// Scenario RU-16:
// 1. Initialize a 28-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Scale up sg-x to 3 replicas (28 pods) so we can later scale in without going below minAvailable
// 4. Change the specification of pc-a, pc-b and pc-c
// 5. Scale in the PCSG during its rolling update (back to 2 replicas)
// 6. Verify the update goes through successfully
func Test_RU16_RollingUpdateWithPCSGScaleInDuringUpdate(t *testing.T) {
	logger.Info("1. Initialize a 28-node Grove cluster")
	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
	logger.Info("3. Scale up sg-x to 3 replicas (28 pods) so we can later scale in")
	tc, cleanup, tracker := setupRollingUpdateTest(t, RollingUpdateTestConfig{
		WorkerNodes:         28,
		ExpectedPods:        10,
		InitialPCSReplicas:  2,
		PostScalePods:       20,
		InitialPCSGReplicas: 3,
		PCSGName:            "sg-x",
		PostPCSGScalePods:   28,
	})
	defer cleanup()

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

	if err := <-updateWait; err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := <-scaleWait; err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	// After scaling sg-x back to 2 replicas via PCS template (affects all PCS replicas):
	// 2 PCS replicas x (2 pc-a + 2 sg-x x 4 pods) = 2 x 10 = 20 pods
	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	if len(pods.Items) != 20 {
		t.Fatalf("Expected 20 pods, got %d", len(pods.Items))
	}
	tracker.Stop()

	logger.Info("🎉 Rolling Update with PCSG scale-in during update test (RU-16) completed successfully!")
}

/* This test is flaky. It sometimes fails with "Failed to wait for rolling update to complete: condition not met..."
// Test_RU17_RollingUpdateWithPCSGScaleInBeforeUpdate tests rolling update with scale-in on PCSG before it is updated
// Scenario RU-17:
// 1. Initialize a 28-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Scale up sg-x to 3 replicas (28 pods) so we can later scale in without going below minAvailable
// 4. Scale in the PCSG before its rolling update starts (back to 2 replicas)
// 5. Change the specification of pc-a, pc-b and pc-c
// 6. Verify the update goes through successfully
func Test_RU17_RollingUpdateWithPCSGScaleInBeforeUpdate(t *testing.T) {
	logger.Info("1. Initialize a 28-node Grove cluster")
	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
	logger.Info("3. Scale up sg-x to 3 replicas (28 pods) so we can later scale in")
	tc, cleanup, tracker := setupRollingUpdateTest(t, RollingUpdateTestConfig{
		WorkerNodes:         28,
		ExpectedPods:        10,
		InitialPCSReplicas:  2,
		PostScalePods:       20,
		InitialPCSGReplicas: 3,
		PCSGName:            "sg-x",
		PostPCSGScalePods:   28,
	})
	defer cleanup()

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

	if err := <-updateWait; err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := <-scaleWait; err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	// After scaling sg-x back to 2 replicas via PCS template (affects all PCS replicas):
	// 2 PCS replicas x (2 pc-a + 2 sg-x x 4 pods) = 2 x 10 = 20 pods
	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	if len(pods.Items) != 20 {
		t.Fatalf("Expected 20 pods, got %d", len(pods.Items))
	}
	tracker.Stop()

	logger.Info("🎉 Rolling Update with PCSG scale-in before update test (RU-17) completed successfully!")
}
*/

/* This test is failing intermittently. Need to investigate. Seems to be
   a race between the scale and the update.

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

	logger.Info("3. Change the specification of pc-a, pc-b and pc-c")
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 4 * time.Minute // Extra headroom for rolling update + scale

	updateWait := triggerRollingUpdate(tcLongTimeout, 2, "pc-a", "pc-b", "pc-c")

	logger.Info("4. Scale out the standalone PCLQ (pc-a) during its rolling update (in parallel)")

	logger.Info("5. Verify the scaled pods are created with the correct specifications")
	logger.Info("6. Verify they should not be updated again before the rolling update ends")
	scaleWait := scalePodClique(tcLongTimeout, "pc-a", 4, 24, 100) // 100ms delay so update is "first"

	if err := <-updateWait; err != nil {
		// Diagnostics will be collected automatically by cleanup on test failure
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := <-scaleWait; err != nil {
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
*/

// Test_RU19_RollingUpdateWithPodCliqueScaleOutBeforeUpdate tests rolling update with scale-out on standalone PCLQ before it is updated
// Scenario RU-19:
// 1. Initialize a 24-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Scale out the standalone PCLQ (pc-a) before its rolling update
// 4. Change the specification of pc-a, pc-b and pc-c
// 5. Verify the scaled pods are created with the correct specifications
// 6. Verify they should not be updated again before the rolling update ends
func Test_RU19_RollingUpdateWithPodCliqueScaleOutBeforeUpdate(t *testing.T) {
	logger.Info("1. Initialize a 24-node Grove cluster")
	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
	tc, cleanup, tracker := setupRollingUpdateTest(t, RollingUpdateTestConfig{
		WorkerNodes:        24,
		ExpectedPods:       10,
		InitialPCSReplicas: 2,
		PostScalePods:      20,
	})
	defer cleanup()

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

	if err := <-updateWait; err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := <-scaleWait; err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	if len(pods.Items) != 24 {
		t.Fatalf("Expected 24 pods, got %d", len(pods.Items))
	}
	tracker.Stop()

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
	logger.Info("1. Initialize a 22-node Grove cluster")
	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
	tc, cleanup, tracker := setupRollingUpdateTest(t, RollingUpdateTestConfig{
		WorkerNodes:        22,
		ExpectedPods:       10,
		InitialPCSReplicas: 2,
		PostScalePods:      20,
	})
	defer cleanup()

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

	logger.Info("4. Change the specification of pc-a, pc-b and pc-c")
	// Scale in from 3 to 2 (stays at minAvailable=2)
	tcLongTimeout := tc
	tcLongTimeout.Timeout = 4 * time.Minute // Extra headroom for rolling update + scale

	updateWait := triggerRollingUpdate(tcLongTimeout, 2, "pc-a", "pc-b", "pc-c")

	logger.Info("5. Scale in the standalone PCLQ (pc-a) from 3 to 2 during its rolling update (in parallel)")
	scaleWait := scalePodClique(tcLongTimeout, "pc-a", 2, 20, 100) // 100ms delay so update is "first"

	logger.Info("6. Verify the update goes through successfully")
	if err := <-updateWait; err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := <-scaleWait; err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	// After scale-in from 3 to 2 pc-a: 2 PCS replicas × (2 pc-a + 8 sg-x) = 2 × 10 = 20 pods
	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	if len(pods.Items) != 20 {
		t.Fatalf("Expected 20 pods, got %d", len(pods.Items))
	}
	tracker.Stop()

	logger.Info("🎉 Rolling Update with PodClique scale-in during update test (RU-20) completed successfully!")
}

/* This test is flaky. It sometimes fails with "Failed to wait for rolling update to complete: condition not met..."
// Test_RU21_RollingUpdateWithPodCliqueScaleInBeforeUpdate tests rolling update with scale-in on standalone PCLQ before it is updated
// Scenario RU-21:
// 1. Initialize a 22-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Scale out pc-a to 3 replicas (above minAvailable=2) to allow scale-in
// 4. Scale in pc-a from 3 to 2 before its rolling update
// 5. Change the specification of pc-a, pc-b and pc-c
// 6. Verify the update goes through successfully
func Test_RU21_RollingUpdateWithPodCliqueScaleInBeforeUpdate(t *testing.T) {
	logger.Info("1. Initialize a 22-node Grove cluster")
	logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
	tc, cleanup, tracker := setupRollingUpdateTest(t, RollingUpdateTestConfig{
		WorkerNodes:        22,
		ExpectedPods:       10,
		InitialPCSReplicas: 2,
		PostScalePods:      20,
	})
	defer cleanup()

	logger.Info("3. Scale out pc-a to 3 replicas (above minAvailable=2) to allow scale-in")
	// pc-a has minAvailable=2, so we scale up to 3 first to allow scale-in back to 2
	// After scaling pc-a to 3: 2 PCS replicas × (3 pc-a + 8 sg-x) = 2 × 11 = 22 pods
	if err := scalePodCliqueInPCS(tc, "pc-a", 3); err != nil {
		t.Fatalf("Failed to scale out PodClique pc-a: %v", err)
	}

	if err := waitForPods(tc, 22); err != nil {
		t.Fatalf("Failed to wait for pods after pc-a scale-out: %v", err)
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

	if err := <-updateWait; err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := <-scaleWait; err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	// After scale-in from 3 to 2 pc-a: 2 PCS replicas × (2 pc-a + 8 sg-x) = 2 × 10 = 20 pods
	pods, err := listPods(tc)
	if err != nil {
		t.Fatalf("Failed to list pods: %v", err)
	}
	if len(pods.Items) != 20 {
		t.Fatalf("Expected 20 pods, got %d", len(pods.Items))
	}
	tracker.Stop()

	logger.Info("🎉 Rolling Update with PodClique scale-in before update test (RU-21) completed successfully!")
}
*/

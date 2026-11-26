//go:build e2e

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

package update

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/kwok"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/pods"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	updateutils "github.com/ai-dynamo/grove/operator/e2e/tests/update/utils"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Test_RU7_RollingUpdatePCSPodClique tests rolling update when PCS-owned Podclique spec is updated
// Scenario RU-7:
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Change the specification of pc-a
// 4. Verify that only one pod is deleted at a time
// 5. Verify that a single PCS replica is updated first before moving to another
func Test_RU7_RollingUpdatePCSPodClique(t *testing.T) {
	tests.Logger.Info("1. Initialize a 10-node Grove cluster")
	tests.Logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	tc, cleanup, tracker := setupTest(t, testConfig{
		workloadName: "workload1",
		workloadYAML: "../../yaml/workload1.yaml",
		workerNodes:  10,
		expectedPods: 10,
	})
	defer cleanup()

	tests.Logger.Info("3. Change the specification of pc-a")
	if err := updateutils.TriggerPodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 1 * time.Minute
	if err := updateutils.WaitForRollingUpdateComplete(&tcLongTimeout, 1); err != nil {
		// Diagnostics will be collected automatically by cleanup on test failure
		t.Fatalf("Failed to wait for rolling update to complete: %v", err)
	}

	tests.Logger.Info("4. Verify that only one pod is deleted at a time")
	tracker.Stop()
	events := tracker.Events()
	verifyOnePodDeletedAtATime(tc, events)

	tests.Logger.Info("5. Verify that a single PCS replica is updated first before moving to another")
	verifySinglePCSReplicaUpdatedFirst(tc, events)

	tests.Logger.Info("6. Verify omitted MaxUnavailable remains unstored and UpdateInProgress is cleared")
	assertMaxUnavailableOmitted(tc, "pc-a")
	updateutils.AssertUpdateInProgressCleared(tc)

	tests.Logger.Info("Rolling Update on PCS-owned Podclique test (RU-7) completed successfully!")
}

// Test_RU8_RollingUpdatePCSGPodClique tests rolling update when PCSG-owned Podclique spec is updated
// Scenario RU-8:
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Change the specification of pc-b
// 4. Verify that only one PCSG replica is deleted at a time
// 5. Verify that a single PCS replica is updated first before moving to another
func Test_RU8_RollingUpdatePCSGPodClique(t *testing.T) {
	tests.Logger.Info("1. Initialize a 10-node Grove cluster")
	tests.Logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	tc, cleanup, tracker := setupTest(t, testConfig{
		workloadName: "workload1",
		workloadYAML: "../../yaml/workload1.yaml",
		workerNodes:  10,
		expectedPods: 10,
	})
	defer cleanup()

	tests.Logger.Info("3. Change the specification of pc-b")
	if err := updateutils.TriggerPodCliqueUpdate(tc, "pc-b"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 1 * time.Minute
	if err := updateutils.WaitForRollingUpdateComplete(&tcLongTimeout, 1); err != nil {
		t.Fatalf("Failed to wait for rolling update to complete: %v", err)
	}

	tests.Logger.Info("4. Verify that only one PCSG replica is deleted at a time")
	tracker.Stop()
	events := tracker.Events()
	verifyOnePCSGReplicaDeletedAtATime(tc, events)

	tests.Logger.Info("5. Verify that a single PCS replica is updated first before moving to another")
	verifySinglePCSReplicaUpdatedFirst(tc, events)

	updateutils.AssertUpdateInProgressCleared(tc)

	tests.Logger.Info("Rolling Update on PCSG-owned Podclique test (RU-8) completed successfully!")
}

// Test_RU9_RollingUpdateAllPodCliques tests rolling update when all Podclique specs are updated
// Scenario RU-9:
// 1. Initialize a 10-node Grove cluster
// 2. Deploy workload WL1, and verify 10 newly created pods
// 3. Change the specification of pc-a, pc-b and pc-c
// 4. Verify that only one pod in each Podclique and one replica in each PCSG is deleted at a time
// 5. Verify that a single PCS replica is updated first before moving to another
func Test_RU9_RollingUpdateAllPodCliques(t *testing.T) {
	tests.Logger.Info("1. Initialize a 10-node Grove cluster")
	tests.Logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	tc, cleanup, tracker := setupTest(t, testConfig{
		workloadName: "workload1",
		workloadYAML: "../../yaml/workload1.yaml",
		workerNodes:  10,
		expectedPods: 10,
	})
	defer cleanup()

	tests.Logger.Info("3. Change the specification of pc-a, pc-b and pc-c")
	for _, cliqueName := range []string{"pc-a", "pc-b", "pc-c"} {
		if err := updateutils.TriggerPodCliqueUpdate(tc, cliqueName); err != nil {
			t.Fatalf("Failed to update PodClique %s spec: %v", cliqueName, err)
		}
	}

	if err := updateutils.WaitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("Failed to wait for rolling update to complete: %v", err)
	}

	tests.Logger.Info("4. Verify that only one pod in each Podclique and one replica in each PCSG is deleted at a time")
	tracker.Stop()
	events := tracker.Events()
	verifySinglePCSReplicaUpdatedFirst(tc, events)
	verifyOnePodDeletedAtATimePerPodClique(tc, events)
	verifyOnePCSGReplicaDeletedAtATimePerPCSG(tc, events)

	tests.Logger.Info("5. Verify that a single PCS replica is updated first before moving to another")
	verifySinglePCSReplicaUpdatedFirst(tc, events)

	updateutils.AssertUpdateInProgressCleared(tc)

	tests.Logger.Info("Rolling Update on all Podcliques test (RU-9) completed successfully!")
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

	tests.Logger.Info("1. Initialize a 10-node Grove cluster")
	tests.Logger.Info("2. Deploy workload WL1, and verify 10 newly created pods")
	tc, cleanup := testctx.PrepareTest(ctx, t, 10,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	if err := tc.WaitForPods(10); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	tests.Logger.Info("3. Cordon all worker nodes")
	workerNodes, err := tc.GetWorkerNodes()
	if err != nil {
		t.Fatalf("Failed to get agent nodes: %v", err)
	}

	tc.CordonNodes(workerNodes)

	// Capture the existing pods before starting the tracker
	existingPods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list existing Pods: %v", err)
	}

	// Capture the existing pods names for verification later
	existingPodNames := make(map[string]bool)
	for _, pod := range existingPods.Items {
		existingPodNames[pod.Name] = true
	}
	tests.Logger.Debugf("Captured %d existing pods before rolling update", len(existingPodNames))

	tracker := updateutils.NewUpdateTracker()
	if err := tracker.Start(tc); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer tracker.Stop()

	tests.Logger.Info("4. Change the specification of pc-a")
	if err := updateutils.TriggerPodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("Failed to update PodClique spec: %v", err)
	}

	tests.Logger.Info("5. Verify exactly one pod is deleted and a new Pending pod is created (delete-first strategy)")

	// Poll until we see exactly 1 pod deleted and 1 new pod created, verifying delete-first behavior
	fetchEvents := waiter.FetchFunc[[]updateutils.PodEvent](func(_ context.Context) ([]updateutils.PodEvent, error) {
		events := tracker.Events()
		deletedCount := 0
		for _, event := range events {
			if event.EventType == watch.Deleted && existingPodNames[event.Pod.Name] {
				deletedCount++
			}
		}
		if deletedCount > 1 {
			return nil, fmt.Errorf("rolling update progressed beyond first deletion - expected 1 pod deleted but got %d (not delete-first strategy)", deletedCount)
		}
		return events, nil
	})
	isDeleteFirst := waiter.Predicate[[]updateutils.PodEvent](func(events []updateutils.PodEvent) bool {
		var deletedExistingPods []string
		var addedPods []string
		for _, event := range events {
			switch event.EventType {
			case watch.Deleted:
				if existingPodNames[event.Pod.Name] {
					deletedExistingPods = append(deletedExistingPods, event.Pod.Name)
					tests.Logger.Debugf("Existing pod deleted during rolling update: %s", event.Pod.Name)
				}
			case watch.Added:
				if !existingPodNames[event.Pod.Name] {
					addedPods = append(addedPods, event.Pod.Name)
					tests.Logger.Debugf("New pod created during rolling update: %s", event.Pod.Name)
				}
			}
		}
		deletedCount := len(deletedExistingPods)
		addedCount := len(addedPods)
		if deletedCount == 1 && addedCount == 1 {
			tests.Logger.Infof("Delete-first strategy verified: 1 pod deleted (%v), 1 new pod created (%v)", deletedExistingPods, addedPods)
			return true
		}
		tests.Logger.Debugf("Waiting for delete-first state: deleted=%d (want 1), added=%d (want 1)", deletedCount, addedCount)
		return false
	})
	w := waiter.New[[]updateutils.PodEvent]().
		WithTimeout(2 * time.Minute).
		WithInterval(2 * time.Second)
	pollErr := w.WaitUntil(ctx, fetchEvents, isDeleteFirst)

	if pollErr != nil {
		t.Fatalf("Failed to verify delete-first strategy: %v", pollErr)
	}

	tests.Logger.Info("6. Uncordon the nodes, and verify the rolling update completes")
	tc.UncordonNodes(workerNodes)

	// Wait for rolling update to complete after uncordoning
	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 5 * time.Minute
	if err := updateutils.WaitForRollingUpdateComplete(&tcLongTimeout, 1); err != nil {
		tests.Logger.Info("=== Rolling update timed out - capturing debug info ===")
		t.Fatalf("Failed to wait for rolling update to complete: %v", err)
	}

	tests.Logger.Info("Rolling Update with insufficient resources test (RU-10) completed successfully!")
}

// Test_RU11_RollingUpdateWithPCSScaleOut tests rolling update with scale-out on PCS
// Scenario RU-11:
// 1. Initialize a 30-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Change the specification of pc-a
// 4. Scale out the PCS during the rolling update
// 5. Verify the scaled out replica is created with the correct specifications
func Test_RU11_RollingUpdateWithPCSScaleOut(t *testing.T) {
	tests.Logger.Info("1. Initialize a 30-node Grove cluster")
	tests.Logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
	tc, cleanup, tracker := setupTest(t, testConfig{
		workloadName:       "workload1",
		workloadYAML:       "../../yaml/workload1.yaml",
		workerNodes:        30,
		expectedPods:       10,
		patchSIGTERM:       true,
		initialPCSReplicas: 2,
		postScalePods:      20,
	})
	defer cleanup()

	tests.Logger.Info("3. Change the specification of pc-a")
	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 2 * time.Minute
	updateWait := triggerRollingUpdate(&tcLongTimeout, 3, "pc-a")

	tests.Logger.Info("4. Scale out the PCS during the rolling update (in parallel)")
	scaleWait := tcLongTimeout.ScalePCSAsync("workload1", 3, 30, 0, 100) // 100ms delay so update is "first"

	if err := <-updateWait; err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := <-scaleWait; err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	tests.Logger.Info("5. Verify the scaled out replica is created with the correct specifications")
	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list Pods: %v", err)
	}
	if len(pods.Items) != 30 {
		t.Fatalf("Expected 30 pods, got %d", len(pods.Items))
	}

	tracker.Stop()
	events := tracker.Events()
	tests.Logger.Debugf("Captured %d pod events during rolling update", len(events))

	tests.Logger.Info("Rolling Update with PCS scale-out test (RU-11) completed successfully!")
}

// Test_RU12_RollingUpdateWithPCSScaleInDuringUpdate tests rolling update with scale-in on PCS while final ordinal is being updated
// Scenario RU-12:
// 1. Initialize a 30-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods
// 3. Change the specification of pc-a, pc-b and pc-c
// 4. Scale in the PCS while the final ordinal is being updated
// 5. Verify the update goes through successfully
func Test_RU12_RollingUpdateWithPCSScaleInDuringUpdate(t *testing.T) {
	tests.Logger.Info("1. Initialize a 30-node Grove cluster")
	tests.Logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
	tc, cleanup, tracker := setupTest(t, testConfig{
		workloadName:       "workload1",
		workloadYAML:       "../../yaml/workload1.yaml",
		workerNodes:        30,
		expectedPods:       10,
		patchSIGTERM:       true,
		initialPCSReplicas: 2,
		postScalePods:      20,
	})
	defer cleanup()

	// Widen the update window so updateutils.WaitForOrdinalUpdating reliably observes the final ordinal. On KWOK the
	// update completes within a poll interval, so the transient CurrentlyUpdating window can close before
	// the poll sees it. The readiness-delay stage keeps freshly created pods not-ready long enough for the
	// window to span more than the poll interval.
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyDelayedPath); err != nil {
		t.Fatalf("failed to apply readiness-delay KWOK stage: %v", err)
	}
	defer func() {
		if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
			t.Errorf("failed to delete readiness-delay KWOK stage: %v", err)
		}
	}()

	tests.Logger.Info("3. Change the specification of pc-a, pc-b and pc-c")
	// Use raw trigger since we need to wait for ordinal before starting the wait
	for _, cliqueName := range []string{"pc-a", "pc-b", "pc-c"} {
		if err := updateutils.TriggerPodCliqueUpdate(tc, cliqueName); err != nil {
			t.Fatalf("Failed to trigger rolling update on %s: %v", cliqueName, err)
		}
	}

	tests.Logger.Info("4. Scale in the PCS while the final ordinal is being updated")
	// Wait for the final ordinal (ordinal 1 since there's two replicas, indexed from 0) to start updating before scaling in
	// Rolling updates process ordinals from highest to lowest, so ordinal 1 is updated first
	tcOrdinalTimeout := *tc
	tcOrdinalTimeout.Timeout = 60 * time.Second
	if err := updateutils.WaitForOrdinalUpdating(&tcOrdinalTimeout, 1); err != nil {
		t.Fatalf("Failed to wait for final ordinal to start updating: %v", err)
	}

	// Scale in parallel with the ongoing rolling update (no delay since we already waited for ordinal)
	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 2 * time.Minute
	scaleWait := tcLongTimeout.ScalePCSAsync("workload1", 1, 10, 0, 0) // No delay since update already in progress

	tests.Logger.Info("5. Verify the update goes through successfully")
	updateWait := waitForRollingUpdate(&tcLongTimeout, 1)

	if err := <-updateWait; err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := <-scaleWait; err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list Pods: %v", err)
	}
	if len(pods.Items) != 10 {
		t.Fatalf("Expected 10 pods, got %d", len(pods.Items))
	}
	tracker.Stop()

	tests.Logger.Info("Rolling Update with PCS scale-in during update test (RU-12) completed successfully!")
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
	tests.Logger.Info("1. Initialize a 20-node Grove cluster")
	tests.Logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
	tc, cleanup, tracker := SetupTest(t, TestConfig{
		WorkloadName:       "workload1",
		WorkloadYAML:       "../../yaml/workload1.yaml",
		WorkerNodes:        20,
		ExpectedPods:       10,
		InitialPCSReplicas: 2,
		PostScalePods:      20,
	})
	defer cleanup()

	tests.Logger.Info("3. Change the specification of pc-a, pc-b and pc-c")
	for _, cliqueName := range []string{"pc-a", "pc-b", "pc-c"} {
		if err := triggerPodCliqueRollingUpdate(tc, cliqueName); err != nil {
			t.Fatalf("Failed to update PodClique %s spec: %v", cliqueName, err)
		}
	}

	tests.Logger.Info("4. Wait for rolling update to complete on both replicas")
	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 2 * time.Minute
	if err := updateutils.WaitForRollingUpdateComplete(&tcLongTimeout, 2); err != nil {
		t.Fatalf("Failed to wait for rolling update to complete: %v", err)
	}

	tests.Logger.Info("5. Scale in the PCS after final ordinal has been updated")
	tests.ScalePCSAndWait(tc, "workload1", 1, 10, 0)

	tests.Logger.Info("6. Verify the update goes through successfully")
	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list Pods: %v", err)
	}
	if len(pods.Items) != 10 {
		t.Fatalf("Expected 10 pods, got %d", len(pods.Items))
	}
	tracker.Stop()

	tests.Logger.Info("Rolling Update with PCS scale-in after final ordinal test (RU-13) completed successfully!")
}
*/

// Test_RU14_RollingUpdateWithPCSGScaleOutDuringUpdate tests a PodCliqueScalingGroup scale-out that runs
// concurrently with a RollingRecreate update on a multi-replica PodCliqueSet. The PodCliqueSet generation
// hash advances globally while replicas roll one at a time, so a replica not yet selected still carries
// old-generation PodGangMap entries when the scale-out reconciles. Every replica's scale-out entry must
// resolve its DependsOn to that replica's anchor epoch, never an empty epoch, so the scaled-out pods are
// not left scheduling-gated.
// Scenario RU-14:
// 1. Initialize a 28-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, verify 20 pods
// 3. Assert each replica's PodGangMap starts with an anchor and a scale-out entry
// 4. Roll an update, wait until replica 0 is updating, then scale out sg-x from 2 to 3 during the update
// 5. Verify all 28 pods run, none stranded scheduling-gated
// 6. Assert each replica's entries advanced to the new hash and the scale-out entry holds the new index
func Test_RU14_RollingUpdateWithPCSGScaleOutDuringUpdate(t *testing.T) {
	tests.Logger.Info("1. Initialize a 28-node Grove cluster")
	tests.Logger.Info("2. Deploy workload WL1 with 2 replicas, verify 20 pods")
	tc, cleanup, tracker := setupTest(t, testConfig{
		workloadName:       "workload1",
		workloadYAML:       "../../yaml/workload1.yaml",
		workerNodes:        28,
		expectedPods:       10,
		initialPCSReplicas: 2,
		postScalePods:      20,
	})
	defer cleanup()

	tests.Logger.Info("3. Assert each replica's PodGangMap starts with an anchor and a scale-out entry")
	oldHash := updateutils.PCSGenerationHash(t, tc)
	// Each replica has its own PodGangMap, but they share this expected shape since the workload template
	// is uniform across replicas. Only the epochs differ per replica, which updateutils.AssertReplicaPodGangMap reads
	// from each replica's own entries.
	wantPGM := updateutils.ExpectedReplicaPodGangMap{
		StandalonePodCounts: map[string]int32{"pc-a": 2},
		PCSGName:            "sg-x",
		AnchorIndices:       []int32{0, 1},
	}
	for _, pcsReplicaIndex := range []int{0, 1} {
		updateutils.AssertReplicaPodGangMap(t, updateutils.PodGangMapEntries(t, tc, pcsReplicaIndex), wantPGM)
	}

	// Widen replica 0's update window so updateutils.WaitForOrdinalUpdating reliably observes it. On KWOK the whole
	// update completes in seconds, so the transient CurrentlyUpdating=[0] window can close within a single
	// poll interval. The readiness-delay stage keeps freshly created pods not-ready long enough that the
	// window spans more than the poll interval, and the scale-out lands while replica 0 is still updating
	// and replica 1 still carries old-generation entries.
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyDelayedPath); err != nil {
		t.Fatalf("failed to apply readiness-delay KWOK stage: %v", err)
	}
	defer func() {
		if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
			t.Errorf("failed to delete readiness-delay KWOK stage: %v", err)
		}
	}()

	tests.Logger.Info("4. Roll an update, wait until replica 0 is updating, then scale out sg-x during the update")
	tcLongerTimeout := *tc
	tcLongerTimeout.Timeout = 2 * time.Minute
	updateErrCh := triggerRollingUpdate(&tcLongerTimeout, 2, "pc-a", "pc-b", "pc-c")
	// Wait until replica 0 is actually updating. Replica 1 then still carries old-generation entries,
	// which is the window where a scale-out could produce a DependsOn on an empty anchor epoch.
	if err := updateutils.WaitForOrdinalUpdating(&tcLongerTimeout, 0); err != nil {
		t.Fatalf("Update did not start on replica 0: %v", err)
	}
	scaleErrCh := tcLongerTimeout.ScalePCSGAcrossAllReplicasAsync("workload1", "sg-x", 2, 3, 28, 0, 0)

	if err := <-updateErrCh; err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := <-scaleErrCh; err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}
	tracker.Stop()

	tests.Logger.Info("5. Verify all 28 pods run, none stranded scheduling-gated")
	// sg-x holds 4 pods per replica (1 pc-b + 3 pc-c). At 3 PCSG replicas each PCS replica has
	// 2 pc-a + 3*4 = 14 pods, so 2 PCS replicas total 28.
	if err := tc.WaitForRunningPods(28); err != nil {
		t.Fatalf("Pods did not all become running after scale-out during a rolling update; they may be stranded scheduling-gated: %v", err)
	}

	tests.Logger.Info("6. Assert each replica's entries advanced to the new hash and the scale-out entry holds the new index")
	newHash := updateutils.PCSGenerationHash(t, tc)
	if newHash == oldHash {
		t.Fatalf("PodCliqueSet generation hash did not change after the update: %s", newHash)
	}
	wantPGM.ScaleOutIndices = []int32{2}
	for _, pcsReplicaIndex := range []int{0, 1} {
		entries := updateutils.PodGangMapEntries(t, tc, pcsReplicaIndex)
		updateutils.AssertReplicaPodGangMap(t, entries, wantPGM)
		for _, entry := range entries {
			assert.Equal(t, newHash, entry.PodCliqueSetGenerationHash, "entry %s must carry the new generation hash", entry.Epoch)
		}
	}

	tests.Logger.Info("Rolling Update with PCSG scale-out during update test (RU-14) completed successfully!")
}

// Test_RU15_RollingUpdateWithPCSGScaleOutBeforeUpdate tests a PodCliqueScalingGroup scale-out that
// completes before a RollingRecreate update on a multi-replica PodCliqueSet. The scale-out entry gains
// its new index while all replicas share the current generation hash, then the update advances the hash
// across replicas. Every replica's scale-out entry must keep its index and depend on its anchor epoch, so
// the scaled-out pods are not left scheduling-gated.
// Scenario RU-15:
// 1. Initialize a 28-node Grove cluster
// 2. Deploy workload WL1 with 2 replicas, verify 20 pods
// 3. Assert each replica's PodGangMap starts with an anchor and a scale-out entry
// 4. Scale out sg-x from 2 to 3 across all replicas and wait for it to complete
// 5. Roll an update of pc-a, pc-b and pc-c and wait for it to complete
// 6. Verify all 28 pods run, none stranded scheduling-gated
// 7. Assert each replica's entries advanced to the new hash and the scale-out entry holds the new index
func Test_RU15_RollingUpdateWithPCSGScaleOutBeforeUpdate(t *testing.T) {
	tests.Logger.Info("1. Initialize a 28-node Grove cluster")
	tests.Logger.Info("2. Deploy workload WL1 with 2 replicas, verify 20 pods")
	tc, cleanup, tracker := setupTest(t, testConfig{
		workloadName:       "workload1",
		workloadYAML:       "../../yaml/workload1.yaml",
		workerNodes:        28,
		expectedPods:       10,
		initialPCSReplicas: 2,
		postScalePods:      20,
	})
	defer cleanup()

	tests.Logger.Info("3. Assert each replica's PodGangMap starts with an anchor and a scale-out entry")
	oldHash := updateutils.PCSGenerationHash(t, tc)
	// Each replica has its own PodGangMap, but they share this expected shape since the workload template
	// is uniform across replicas. Only the epochs differ per replica, which updateutils.AssertReplicaPodGangMap reads
	// from each replica's own entries.
	wantPGM := updateutils.ExpectedReplicaPodGangMap{
		StandalonePodCounts: map[string]int32{"pc-a": 2},
		PCSGName:            "sg-x",
		AnchorIndices:       []int32{0, 1},
	}
	for _, pcsReplicaIndex := range []int{0, 1} {
		updateutils.AssertReplicaPodGangMap(t, updateutils.PodGangMapEntries(t, tc, pcsReplicaIndex), wantPGM)
	}

	tests.Logger.Info("4. Scale out sg-x from 2 to 3 across all replicas and wait for it to complete")
	// sg-x holds 4 pods per replica (1 pc-b + 3 pc-c). At 3 PCSG replicas each PCS replica has
	// 2 pc-a + 3*4 = 14 pods, so 2 PCS replicas total 28.
	tc.ScalePCSGAcrossAllReplicasAndWait("workload1", "sg-x", 2, 3, 28, 0)

	tests.Logger.Info("5. Roll an update of pc-a, pc-b and pc-c and wait for it to complete")
	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 2 * time.Minute
	if err := <-triggerRollingUpdate(&tcLongTimeout, 2, "pc-a", "pc-b", "pc-c"); err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	tracker.Stop()

	tests.Logger.Info("6. Verify all 28 pods run, none stranded scheduling-gated")
	if err := tc.WaitForRunningPods(28); err != nil {
		t.Fatalf("Pods did not all become running after a scale-out then rolling update; they may be stranded scheduling-gated: %v", err)
	}

	tests.Logger.Info("7. Assert each replica's entries advanced to the new hash and the scale-out entry holds the new index")
	newHash := updateutils.PCSGenerationHash(t, tc)
	if newHash == oldHash {
		t.Fatalf("PodCliqueSet generation hash did not change after the update: %s", newHash)
	}
	wantPGM.ScaleOutIndices = []int32{2}
	for _, pcsReplicaIndex := range []int{0, 1} {
		entries := updateutils.PodGangMapEntries(t, tc, pcsReplicaIndex)
		updateutils.AssertReplicaPodGangMap(t, entries, wantPGM)
		for _, entry := range entries {
			assert.Equal(t, newHash, entry.PodCliqueSetGenerationHash, "entry %s must carry the new generation hash", entry.Epoch)
		}
	}

	tests.Logger.Info("Rolling Update with PCSG scale-out before update test (RU-15) completed successfully!")
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
	tests.Logger.Info("1. Initialize a 28-node Grove cluster")
	tests.Logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
	tests.Logger.Info("3. Scale up sg-x to 3 replicas (28 pods) so we can later scale in")
	tc, cleanup, tracker := setupTest(t, testConfig{
		workloadName:        "workload1",
		workloadYAML:        "../../yaml/workload1.yaml",
		workerNodes:         28,
		expectedPods:        10,
		initialPCSReplicas:  2,
		postScalePods:       20,
		initialPCSGReplicas: 3,
		pcsgName:            "sg-x",
		postPCSGScalePods:   28,
	})
	defer cleanup()

	tests.Logger.Info("4. Change the specification of pc-a, pc-b and pc-c")
	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 2 * time.Minute
	updateWait := triggerRollingUpdate(&tcLongTimeout, 2, "pc-a", "pc-b", "pc-c")

	tests.Logger.Info("5. Scale in the PCSG during its rolling update (in parallel)")
	// Scaling PCSG instances directly (workload1-0-sg-x, workload1-1-sg-x) since the PCS controller
	// only sets replicas during initial PCSG creation to support HPA scaling.
	// After scaling sg-x back to 2 Replicas: 2 PCS replicas x (2 pc-a + 2 sg-x x 4 pods) = 2 x 10 = 20 pods
	scaleWait := tcLongTimeout.ScalePCSGAcrossAllReplicasAsync("workload1", "sg-x", 2, 2, 20, 0, 100) // 100ms delay so update is "first"

	tests.Logger.Info("6. Verify the update goes through successfully")

	if err := <-updateWait; err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := <-scaleWait; err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	// After scaling sg-x back to 2 replicas via PCS template (affects all PCS replicas):
	// 2 PCS replicas x (2 pc-a + 2 sg-x x 4 pods) = 2 x 10 = 20 pods
	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list Pods: %v", err)
	}
	if len(pods.Items) != 20 {
		t.Fatalf("Expected 20 pods, got %d", len(pods.Items))
	}
	tracker.Stop()

	tests.Logger.Info("Rolling Update with PCSG scale-in during update test (RU-16) completed successfully!")
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
	tests.Logger.Info("1. Initialize a 28-node Grove cluster")
	tests.Logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
	tests.Logger.Info("3. Scale up sg-x to 3 replicas (28 pods) so we can later scale in")
	tc, cleanup, tracker := SetupTest(t, TestConfig{
		WorkloadName:        "workload1",
		WorkloadYAML:        "../../yaml/workload1.yaml",
		WorkerNodes:         28,
		ExpectedPods:        10,
		InitialPCSReplicas:  2,
		PostScalePods:       20,
		InitialPCSGReplicas: 3,
		PCSGName:            "sg-x",
		PostPCSGScalePods:   28,
	})
	defer cleanup()

	tests.Logger.Info("4. Scale in the PCSG before its rolling update starts (in parallel)")
	// Scale starts first (no delay)
	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 2 * time.Minute
	scaleWait := tcLongTimeout.ScalePCSGAcrossAllReplicasAsync("workload1", "sg-x", 2, 2, 20, 0, 0)

	tests.Logger.Info("5. Change the specification of pc-a, pc-b and pc-c")
	// Scaling PCSG instances directly (workload1-0-sg-x, workload1-1-sg-x) since the PCS controller
	// only sets replicas during initial PCSG creation to support HPA scaling.
	// After scaling sg-x back to 2 Replicas: 2 PCS replicas x (2 pc-a + 2 sg-x x 4 pods) = 2 x 10 = 20 pods

	tests.Logger.Info("6. Verify the update goes through successfully")

	// Small delay so scale is clearly "first", then trigger update
	time.Sleep(100 * time.Millisecond)
	updateWait := triggerRollingUpdate(&tcLongTimeout, 2, "pc-a", "pc-b", "pc-c")

	if err := <-updateWait; err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := <-scaleWait; err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	// After scaling sg-x back to 2 replicas via PCS template (affects all PCS replicas):
	// 2 PCS replicas x (2 pc-a + 2 sg-x x 4 pods) = 2 x 10 = 20 pods
	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list Pods: %v", err)
	}
	if len(pods.Items) != 20 {
		t.Fatalf("Expected 20 pods, got %d", len(pods.Items))
	}
	tracker.Stop()

	tests.Logger.Info("Rolling Update with PCSG scale-in before update test (RU-17) completed successfully!")
}
*/

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

	tests.Logger.Info("1. Initialize a 24-node Grove cluster")
	tests.Logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
	tc, cleanup := testctx.PrepareTest(ctx, t, 24,
		testctx.WithWorkload(&testctx.WorkloadConfig{
			Name:         "workload1",
			YAMLPath:     "../../yaml/workload1.yaml",
			Namespace:    "default",
			ExpectedPods: 10,
		}),
	)
	defer cleanup()

	_, err := tc.DeployAndVerifyWorkload()
	if err != nil {
		t.Fatalf("Failed to deploy workload: %v", err)
	}

	tc.ScalePCSAndWait("workload1", 2, 20, 0)

	if err := tc.WaitForPods(20); err != nil {
		t.Fatalf("Failed to wait for pods to be ready: %v", err)
	}

	tracker := updateutils.NewUpdateTracker()
	if err := tracker.Start(tc); err != nil {
		t.Fatalf("Failed to start tracker: %v", err)
	}
	defer tracker.Stop()

	tests.Logger.Info("3. Change the specification of pc-a, pc-b and pc-c")
	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 4 * time.Minute // Extra headroom for rolling update + scale

	updateWait := triggerRollingUpdate(&tcLongTimeout, 2, "pc-a", "pc-b", "pc-c")

	tests.Logger.Info("4. Scale out the standalone PCLQ (pc-a) during its rolling update (in parallel)")

	tests.Logger.Info("5. Verify the scaled pods are created with the correct specifications")
	tests.Logger.Info("6. Verify they should not be updated again before the rolling update ends")
	scaleWait := scalePodClique(&tcLongTimeout, "pc-a", 4, 24, 100) // 100ms delay so update is "first"

	if err := <-updateWait; err != nil {
		// Diagnostics will be collected automatically by cleanup on test failure
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := <-scaleWait; err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	tracker.Stop()

	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list Pods: %v", err)
	}

	if len(pods.Items) != 24 {
		t.Fatalf("Expected 24 pods after PodClique scale-out (4 pc-a + 4 pc-b + 12 pc-c), got %d", len(pods.Items))
	}

	tests.Logger.Info("Rolling Update with PodClique scale-out during update test (RU-18) completed successfully!")
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
	tests.Logger.Info("1. Initialize a 24-node Grove cluster")
	tests.Logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
	tc, cleanup, tracker := setupTest(t, testConfig{
		workloadName:       "workload1",
		workloadYAML:       "../../yaml/workload1.yaml",
		workerNodes:        24,
		expectedPods:       10,
		initialPCSReplicas: 2,
		postScalePods:      20,
	})
	defer cleanup()

	tests.Logger.Info("3. Scale out the standalone PCLQ (pc-a) before its rolling update (in parallel)")
	// Scale starts first (no delay)
	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 4 * time.Minute // Extra headroom for rolling update + scale
	scaleWait := scalePodClique(&tcLongTimeout, "pc-a", 4, 24, 0)

	tests.Logger.Info("4. Change the specification of pc-a, pc-b and pc-c")
	// Small delay so scale is clearly "first", then trigger update
	time.Sleep(100 * time.Millisecond)
	updateWait := triggerRollingUpdate(&tcLongTimeout, 2, "pc-a", "pc-b", "pc-c")

	tests.Logger.Info("5. Verify the scaled pods are created with the correct specifications")
	tests.Logger.Info("6. Verify they should not be updated again before the rolling update ends")

	if err := <-updateWait; err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := <-scaleWait; err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list Pods: %v", err)
	}
	if len(pods.Items) != 24 {
		t.Fatalf("Expected 24 pods, got %d", len(pods.Items))
	}
	tracker.Stop()

	tests.Logger.Info("Rolling Update with PodClique scale-out before update test (RU-19) completed successfully!")
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
	tests.Logger.Info("1. Initialize a 22-node Grove cluster")
	tests.Logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
	tc, cleanup, tracker := setupTest(t, testConfig{
		workloadName:       "workload1",
		workloadYAML:       "../../yaml/workload1.yaml",
		workerNodes:        22,
		expectedPods:       10,
		initialPCSReplicas: 2,
		postScalePods:      20,
	})
	defer cleanup()

	tests.Logger.Info("3. Scale out pc-a to 3 replicas (above minAvailable=2) to allow scale-in during update")
	// pc-a has minAvailable=2, so we scale up to 3 first to allow scale-in back to 2 during rolling update
	// Each PCS replica has: 2 pc-a + 8 sg-x pods = 10 pods
	// After scaling pc-a to 3: 2 PCS replicas × (3 pc-a + 8 sg-x) = 2 × 11 = 22 pods
	if err := updateutils.ScalePodCliqueInPCS(tc, "pc-a", 3); err != nil {
		t.Fatalf("Failed to scale out PodClique pc-a: %v", err)
	}

	if err := tc.WaitForPods(22); err != nil {
		t.Fatalf("Failed to wait for pods after pc-a scale-out: %v", err)
	}

	tests.Logger.Info("4. Change the specification of pc-a, pc-b and pc-c")
	// Scale in from 3 to 2 (stays at minAvailable=2)
	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 4 * time.Minute // Extra headroom for rolling update + scale

	updateWait := triggerRollingUpdate(&tcLongTimeout, 2, "pc-a", "pc-b", "pc-c")

	tests.Logger.Info("5. Scale in the standalone PCLQ (pc-a) from 3 to 2 during its rolling update (in parallel)")
	scaleWait := scalePodClique(&tcLongTimeout, "pc-a", 2, 20, 100) // 100ms delay so update is "first"

	tests.Logger.Info("6. Verify the update goes through successfully")
	if err := <-updateWait; err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := <-scaleWait; err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	// After scale-in from 3 to 2 pc-a: 2 PCS replicas × (2 pc-a + 8 sg-x) = 2 × 10 = 20 pods
	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list Pods: %v", err)
	}
	if len(pods.Items) != 20 {
		t.Fatalf("Expected 20 pods, got %d", len(pods.Items))
	}
	tracker.Stop()

	tests.Logger.Info("Rolling Update with PodClique scale-in during update test (RU-20) completed successfully!")
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
	tests.Logger.Info("1. Initialize a 22-node Grove cluster")
	tests.Logger.Info("2. Deploy workload WL1 with 2 replicas, and verify 20 newly created pods")
	tc, cleanup, tracker := SetupTest(t, TestConfig{
		WorkloadName:       "workload1",
		WorkloadYAML:       "../../yaml/workload1.yaml",
		WorkerNodes:        22,
		ExpectedPods:       10,
		InitialPCSReplicas: 2,
		PostScalePods:      20,
	})
	defer cleanup()

	tests.Logger.Info("3. Scale out pc-a to 3 replicas (above minAvailable=2) to allow scale-in")
	// pc-a has minAvailable=2, so we scale up to 3 first to allow scale-in back to 2
	// After scaling pc-a to 3: 2 PCS replicas × (3 pc-a + 8 sg-x) = 2 × 11 = 22 pods
	if err := updateutils.ScalePodCliqueInPCS(tc, "pc-a", 3); err != nil {
		t.Fatalf("Failed to scale out PodClique pc-a: %v", err)
	}

	if err := tc.WaitForPods( 22); err != nil {
		t.Fatalf("Failed to wait for pods after pc-a scale-out: %v", err)
	}

	tests.Logger.Info("4. Scale in pc-a from 3 to 2 before its rolling update (in parallel)")
	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 4 * time.Minute // Extra headroom for rolling update + scale
	// Scale starts first (no delay) - scaling in from 3 to 2 (stays at minAvailable=2)
	scaleWait := scalePodClique(&tcLongTimeout, "pc-a", 2, 20, 0)

	tests.Logger.Info("5. Change the specification of pc-a, pc-b and pc-c")
	// Small delay so scale is clearly "first", then trigger update
	time.Sleep(100 * time.Millisecond)
	updateWait := triggerRollingUpdate(&tcLongTimeout, 2, "pc-a", "pc-b", "pc-c")

	tests.Logger.Info("6. Verify the update goes through successfully")

	if err := <-updateWait; err != nil {
		t.Fatalf("Rolling update failed: %v", err)
	}
	if err := <-scaleWait; err != nil {
		t.Fatalf("Scale operation failed: %v", err)
	}

	// After scale-in from 3 to 2 pc-a: 2 PCS replicas × (2 pc-a + 8 sg-x) = 2 × 10 = 20 pods
	pods, err := tc.ListPods()
	if err != nil {
		t.Fatalf("Failed to list Pods: %v", err)
	}
	if len(pods.Items) != 20 {
		t.Fatalf("Expected 20 pods, got %d", len(pods.Items))
	}
	tracker.Stop()

	tests.Logger.Info("Rolling Update with PodClique scale-in before update test (RU-21) completed successfully!")
}
*/

// Test_RU22_RollingUpdateStandaloneMaxUnavailable verifies that a standalone PodClique with
// MaxUnavailable=2 keeps at most 2 replicas unavailable at a time (never more) during a rolling update.
// Scenario RU-22:
// 1. Deploy workload-maxunavailable (pc-a replicas 4, minAvailable 1, maxUnavailable 2)
// 2. Apply the readiness-delay KWOK stage so new pods stay not-ready long enough to observe
// 3. Roll pc-a and sample the number of not-ready pc-a pods
// 4. Verify the peak is at most 2 (budget respected) and reaches 2 (budget exercised)
// 5. Verify the UpdateInProgress condition clears after completion
func Test_RU22_RollingUpdateStandaloneMaxUnavailable(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-maxunavailable")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload-maxunavailable",
		workloadYAML: "../../yaml/workload-maxunavailable.yaml",
		workerNodes:  10,
		expectedPods: 8,
	})
	defer cleanup()

	tests.Logger.Info("2. Delay pod readiness so the unavailability window is observable")
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyDelayedPath); err != nil {
		t.Fatalf("failed to apply readiness-delay KWOK stage: %v", err)
	}
	defer func() {
		if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
			t.Errorf("failed to delete readiness-delay KWOK stage: %v", err)
		}
	}()

	tests.Logger.Info("3. Roll pc-a while sampling not-ready pc-a pods")
	tcLong := *tc
	tcLong.Timeout = 2 * time.Minute
	maxUnavailable, err := updateutils.MaxUnavailablePods(&tcLong, updateutils.NotReadyPodForClique("pc-a"), func() error {
		if err := updateutils.TriggerPodCliqueUpdate(&tcLong, "pc-a"); err != nil {
			return err
		}
		return updateutils.WaitForRollingUpdateComplete(&tcLong, 1)
	})
	if err != nil {
		t.Fatalf("rolling update of pc-a did not complete: %v", err)
	}

	tests.Logger.Info("4. Verify the disruption budget was respected and exercised")
	assert.LessOrEqualf(t, maxUnavailable, 2, "expected at most MaxUnavailable=2 pc-a pods unavailable at once, observed %d", maxUnavailable)
	assert.Equalf(t, 2, maxUnavailable, "expected the rollout to keep 2 pc-a pods unavailable at once (MaxUnavailable=2), observed peak %d", maxUnavailable)

	tests.Logger.Info("5. Verify UpdateInProgress cleared after completion")
	updateutils.AssertUpdateInProgressCleared(tc)
}

// Test_RU23_RollingUpdatePCSGMaxUnavailable verifies that a PodCliqueScalingGroup with
// MaxUnavailable=2 keeps at most 2 replicas unavailable at a time (never more) during a rolling update.
// Scenario RU-23:
// 1. Deploy workload-maxunavailable (sg-x replicas 4, minAvailable 1, maxUnavailable 2)
// 2. Apply the readiness-delay KWOK stage so new pods stay not-ready long enough to observe
// 3. Roll the sg-x member pc-b and sample the number of not-ready sg-x pods
// 4. Verify the peak is at most 2 (budget respected) and reaches 2 (budget exercised)
func Test_RU23_RollingUpdatePCSGMaxUnavailable(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-maxunavailable")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload-maxunavailable",
		workloadYAML: "../../yaml/workload-maxunavailable.yaml",
		workerNodes:  10,
		expectedPods: 8,
	})
	defer cleanup()

	tests.Logger.Info("2. Delay pod readiness so the unavailability window is observable")
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyDelayedPath); err != nil {
		t.Fatalf("failed to apply readiness-delay KWOK stage: %v", err)
	}
	defer func() {
		if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
			t.Errorf("failed to delete readiness-delay KWOK stage: %v", err)
		}
	}()

	tests.Logger.Info("3. Roll sg-x member pc-b while sampling not-ready sg-x pods")
	tcLong := *tc
	tcLong.Timeout = 2 * time.Minute
	maxUnavailable, err := updateutils.MaxUnavailablePods(&tcLong, updateutils.NotReadyPodForPCSG(&tcLong, "sg-x"), func() error {
		if err := updateutils.TriggerPodCliqueUpdate(&tcLong, "pc-b"); err != nil {
			return err
		}
		return updateutils.WaitForRollingUpdateComplete(&tcLong, 1)
	})
	if err != nil {
		t.Fatalf("rolling update of sg-x did not complete: %v", err)
	}

	tests.Logger.Info("4. Verify the disruption budget was respected and exercised")
	assert.LessOrEqualf(t, maxUnavailable, 2, "expected at most MaxUnavailable=2 sg-x replica pods unavailable at once, observed %d", maxUnavailable)
	assert.Equalf(t, 2, maxUnavailable, "expected the rollout to keep 2 sg-x replicas unavailable at once (MaxUnavailable=2), observed peak %d", maxUnavailable)

	updateutils.AssertUpdateInProgressCleared(tc)
}

// Test_RU24_RollingUpdateProgressDeadlineExceeded verifies that a stalled rollout surfaces
// UpdateInProgress=Unknown, does not prematurely complete, and keeps gang termination suspended.
// Scenario RU-24:
// 1. Deploy workload-progressdeadline (pc-a progressDeadline 30s)
// 2. Apply the crashloop KWOK stage so freshly created pods stay not-ready
// 3. Trigger a rolling update of pc-a; the new pods will not become Ready
// 4. Verify pc-a UpdateInProgress goes Unknown/ProgressDeadlineExceeded
// 5. Verify the PCS aggregate UpdateInProgress goes Unknown/ProgressDeadlineExceeded
// 6. Verify the update is not marked complete (issue #786) and gang termination stays suspended
func Test_RU24_RollingUpdateProgressDeadlineExceeded(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-progressdeadline")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload-progressdeadline",
		workloadYAML: "../../yaml/workload-progressdeadline.yaml",
		workerNodes:  10,
		expectedPods: 4,
	})
	defer cleanup()

	tests.Logger.Info("2. Make freshly created pods stall in CrashLoopBackOff so the next roll cannot progress")
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageCrashloopPath); err != nil {
		t.Fatalf("failed to apply crashloop KWOK stage: %v", err)
	}
	defer func() {
		if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageCrashloopName); err != nil {
			t.Errorf("failed to delete crashloop KWOK stage: %v", err)
		}
	}()

	tests.Logger.Info("3. Trigger a rolling update of pc-a; the new pods will not become Ready")
	if err := updateutils.TriggerPodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("failed to trigger update of pc-a: %v", err)
	}

	tcLong := *tc
	tcLong.Timeout = 2 * time.Minute
	tests.Logger.Info("4. Verify pc-a UpdateInProgress goes Unknown/ProgressDeadlineExceeded")
	if err := waitForPodCliqueUpdateCondition(&tcLong, "pc-a", metav1.ConditionUnknown, apiconstants.ConditionReasonProgressDeadlineExceeded); err != nil {
		t.Fatalf("pc-a did not surface ProgressDeadlineExceeded: %v", err)
	}

	tests.Logger.Info("5. Verify the PCS aggregate UpdateInProgress goes Unknown/ProgressDeadlineExceeded")
	if err := updateutils.WaitForPCSUpdateCondition(&tcLong, metav1.ConditionUnknown, apiconstants.ConditionReasonProgressDeadlineExceeded); err != nil {
		t.Fatalf("PodCliqueSet did not aggregate ProgressDeadlineExceeded: %v", err)
	}

	tests.Logger.Info("6. Verify the stalled update is not marked complete and gang termination stays suspended")
	assertPodCliqueUpdateNotComplete(tc, "pc-a")
	assertGangTerminationSuspended(tc, "pc-a")
}

// Test_RU25_RollingUpdateStuckThenCorrective verifies that a stuck rollout recovers when a corrective
// spec is applied, and that all components converge to the latest generation hash.
// Scenario RU-25:
// 1. Deploy workload-progressdeadline
// 2. Apply the crashloop KWOK stage so freshly created pods stay not-ready
// 3. Trigger a rolling update of pc-a and wait until it is stuck (Unknown/ProgressDeadlineExceeded)
// 4. Remove the crashloop stage and trigger a corrective roll so fresh pods become Ready
// 5. Verify the update completes, UpdateInProgress clears, and pods become Ready
// 6. Verify every component converged to the latest PodCliqueSet generation hash
func Test_RU25_RollingUpdateStuckThenCorrective(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-progressdeadline")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload-progressdeadline",
		workloadYAML: "../../yaml/workload-progressdeadline.yaml",
		workerNodes:  10,
		expectedPods: 4,
	})
	defer cleanup()

	tests.Logger.Info("2. Make freshly created pods stall in CrashLoopBackOff")
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageCrashloopPath); err != nil {
		t.Fatalf("failed to apply crashloop KWOK stage: %v", err)
	}
	defer func() { _ = kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageCrashloopName) }()

	tests.Logger.Info("3. Trigger a rolling update of pc-a and wait until it is stuck")
	if err := updateutils.TriggerPodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("failed to trigger update of pc-a: %v", err)
	}
	tcLong := *tc
	tcLong.Timeout = 2 * time.Minute
	if err := waitForPodCliqueUpdateCondition(&tcLong, "pc-a", metav1.ConditionUnknown, apiconstants.ConditionReasonProgressDeadlineExceeded); err != nil {
		t.Fatalf("pc-a did not become stuck: %v", err)
	}

	tests.Logger.Info("4. Remove the crashloop stage and trigger a corrective roll so fresh pods become Ready")
	if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageCrashloopName); err != nil {
		t.Fatalf("failed to delete crashloop KWOK stage: %v", err)
	}
	if err := updateutils.TriggerPodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("failed to trigger corrective update of pc-a: %v", err)
	}

	tests.Logger.Info("5. Verify the update completes, the condition clears, and pods are Ready")
	if err := updateutils.WaitForRollingUpdateComplete(&tcLong, 1); err != nil {
		t.Fatalf("corrective rolling update did not complete: %v", err)
	}
	updateutils.AssertUpdateInProgressCleared(tc)
	if err := tc.WaitForPods(4); err != nil {
		t.Fatalf("pods did not become Ready after the corrective update: %v", err)
	}

	tests.Logger.Info("6. Verify all components converged to the latest generation hash")
	updateutils.AssertGenerationHashConverged(tc)
}

// Test_RU26_CorrectiveUpdateReplacesUnschedulablePods verifies that a RollingRecreate still makes
// progress when a component is already unavailable and its unavailability has exhausted MaxUnavailable:
// the corrective update must replace the unschedulable pods and complete rather than deadlock. It covers
// a single PodCliqueScalingGroup replica (the #840 count-anchored-budget case) and a standalone
// PodClique whose MaxUnavailable is below MinAvailable, where a per-pod budget can never bring
// MinAvailable pods up together to admit the PodClique's PodGang.
//
// Scenario RU-26 (per case):
//  1. Deploy a workload whose pods are all Pending because the PCS template pins an impossible node
//     selector, so the component is unavailable.
//  2. Wait for the initial generation to reconcile (so the correction is a rolling update, not initial
//     creation) and confirm the workload reports no available pods.
//  3. Remove the impossible node selector from the PCS template, changing the generation hash.
//  4. Verify Grove replaces every unschedulable pod, the rolling update completes without the selector,
//     and the generation hash converges.
func Test_RU26_CorrectiveUpdateReplacesUnschedulablePods(t *testing.T) {
	cases := []struct {
		name                       string
		workloadName               string
		workloadYAML               string
		expectedPods               int
		assertInitiallyUnavailable func(c *assert.CollectT, tc *testctx.TestContext)
	}{
		{
			name:         "PCSG replica exhausts MaxUnavailable",
			workloadName: "workload-unavailable-pcsg",
			workloadYAML: "../../yaml/workload-unavailable-pcsg.yaml",
			expectedPods: 1,
			assertInitiallyUnavailable: func(c *assert.CollectT, tc *testctx.TestContext) {
				var pcsg grovev1beta1.PodCliqueScalingGroup
				require.NoError(c, tc.Client.Get(tc.Ctx, types.NamespacedName{
					Namespace: tc.Namespace, Name: updateutils.PCSGFQN(tc, "sg-x"),
				}, &pcsg))
				assert.EqualValues(c, 1, pcsg.Status.Replicas)
				assert.Zero(c, pcsg.Status.AvailableReplicas)
			},
		},
		{
			name:         "standalone PodClique, MaxUnavailable below MinAvailable",
			workloadName: "workload-unavailable-standalone-gang",
			workloadYAML: "../../yaml/workload-unavailable-standalone-gang.yaml",
			expectedPods: 2,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tc, cleanup := testctx.PrepareTest(context.Background(), t, 1,
				testctx.WithWorkload(&testctx.WorkloadConfig{
					Name:         tt.workloadName,
					YAMLPath:     tt.workloadYAML,
					Namespace:    "default",
					ExpectedPods: tt.expectedPods,
				}),
				testctx.WithTimeout(time.Minute),
				testctx.WithInterval(time.Second),
			)
			defer cleanup()

			tests.Logger.Info("1. Create a workload whose pods cannot be scheduled")
			pods, err := tc.DeployAndVerifyWorkload()
			require.NoError(t, err)
			require.Len(t, pods.Items, tt.expectedPods)
			oldUIDs := make(map[types.UID]bool, len(pods.Items))
			for _, p := range pods.Items {
				oldUIDs[p.UID] = true
			}

			// Let the first generation fully reconcile before editing the template. If we patch too
			// early, removing the selector can be folded into the initial pod creation instead of
			// triggering a rolling update, which is the path under test. Also confirm the workload is
			// unavailable first.
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
				require.NoError(c, err)
				require.NotNil(c, pcs.Status.ObservedGeneration)
				assert.Equal(c, pcs.Generation, *pcs.Status.ObservedGeneration)
				assert.Zero(c, pcs.Status.AvailableReplicas)
				if tt.assertInitiallyUnavailable != nil {
					tt.assertInitiallyUnavailable(c, tc)
				}
			}, tc.Timeout, tc.Interval, "initial generation must reconcile with the workload unavailable")

			tests.Logger.Info("2. Remove the impossible node selector from the PCS template")
			pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
			require.NoError(t, err)
			original := pcs.DeepCopy()
			delete(pcs.Spec.Template.Cliques[0].Spec.PodSpec.NodeSelector, "e2e.grove.io/unschedulable")
			require.NoError(t, tc.Client.Patch(tc.Ctx, pcs, client.MergeFrom(original)))
			require.Greater(t, pcs.Generation, original.Generation)

			tests.Logger.Info("3. Verify Grove replaces the unschedulable pods and completes the corrective update")
			// The corrective update must proceed even though MaxUnavailable is already breached;
			// otherwise the unschedulable pods are never replaced and this wait times out.
			require.NoError(t, updateutils.WaitForRollingUpdateComplete(tc, 1),
				"corrective update must replace the unschedulable pods and complete")
			require.NoError(t, tc.WaitForPods(tt.expectedPods))

			pods, err = tc.ListPods()
			require.NoError(t, err)
			require.Len(t, pods.Items, tt.expectedPods)
			for _, p := range pods.Items {
				assert.False(t, oldUIDs[p.UID], "every unschedulable pod must be replaced")
				assert.NotContains(t, p.Spec.NodeSelector, "e2e.grove.io/unschedulable")
			}
			updateutils.AssertUpdateInProgressCleared(tc)
			updateutils.AssertGenerationHashConverged(tc)
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				current, err := updateutils.GetPCS(tc, tc.Workload.Name)
				require.NoError(c, err)
				require.NotNil(c, current.Status.ObservedGeneration)
				assert.Equal(c, pcs.Generation, *current.Status.ObservedGeneration)
			}, tc.Timeout, tc.Interval, "observedGeneration must catch up after the corrective update")
		})
	}
}

// Test_RU27_RollingRecreateWithComponentScaledToZero verifies that a RollingRecreate update completes when
// the updated PodClique is scaled to zero. It has no pods to roll, so the update advances its resource to
// the new revision and completes, and a later scale-out launches new-revision pods. This exercises the
// zero-replica completion path on the RollingRecreate strategy.
func Test_RU27_RollingRecreateWithComponentScaledToZero(t *testing.T) {
	tests.Logger.Info("1. Deploy workload1 (RollingRecreate) and verify 10 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload1",
		workloadYAML: "../../yaml/workload1.yaml",
		workerNodes:  12,
		expectedPods: 10,
	})
	defer cleanup()

	tests.Logger.Info("2. Scale the standalone pc-a to 0")
	tc.ScalePodCliqueAndWait("workload1-0-pc-a", 0, 8, 0)

	tests.Logger.Info("3. Trigger a RollingRecreate update of pc-a while it has no pods")
	if err := updateutils.TriggerPodCliqueUpdate(tc, "pc-a"); err != nil {
		t.Fatalf("failed to trigger update of pc-a: %v", err)
	}

	tests.Logger.Info("4. The update completes and converges even though pc-a has no pods to roll")
	if err := updateutils.WaitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("rolling update did not complete: %v", err)
	}
	updateutils.AssertUpdateInProgressCleared(tc)
	updateutils.AssertGenerationHashConverged(tc)

	tests.Logger.Info("5. Scale pc-a back to 2 and verify new-revision pods launch")
	tc.ScalePodCliqueAndWait("workload1-0-pc-a", 2, 10, 0)
	updateutils.AssertGenerationHashConverged(tc)
	if err := tc.WaitForPods(10); err != nil {
		t.Fatalf("pods did not become Ready after scaling pc-a back up: %v", err)
	}
}

// Test_RU28_MaxUnavailableAboveTemplateReplicas verifies that maxUnavailable is a ceiling on how many
// replicas a RollingRecreate rollout may disrupt at once, applied against the current number of live
// replicas rather than the replica count declared in the PodCliqueSet template. The template is only a
// starting value. The live PodCliqueScalingGroup is scaled separately through its /scale subresource,
// so maxUnavailable may legitimately be larger than the template replica count.
//
// sg-x starts with template replicas 1 and maxUnavailable 2. Deploying it confirms a maxUnavailable
// larger than the template replica count is accepted. The test then checks the ceiling in both scale
// directions:
//
//  1. Scale sg-x out to 4 live replicas and roll it. The rollout keeps at most 2 replicas unavailable at
//     once and reaches 2, so the budget is applied to the 4 live replicas, not the template value of 1.
//  2. Scale sg-x back in to 1 live replica and roll it again. maxUnavailable is still 2, but only one
//     replica remains, so the rollout disrupts at most that one replica. The scale-in is not rejected,
//     and the larger budget never disrupts more replicas than exist.
func Test_RU28_MaxUnavailableAboveTemplateReplicas(t *testing.T) {
	const workloadName = "wl-mu-rr"

	tests.Logger.Info("1. Deploy wl-mu-rr (sg-x template replicas 1, maxUnavailable 2)")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: workloadName,
		workloadYAML: "../../yaml/workload-mu-rr.yaml",
		workerNodes:  10,
		// PCS replicas 1: front(1) + sg-x(1) x pc-b(1) = 2 pods.
		expectedPods: 2,
	})
	defer cleanup()

	tests.Logger.Info("2. Delay pod readiness so the unavailability window is observable")
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyDelayedPath); err != nil {
		t.Fatalf("failed to apply readiness-delay KWOK stage: %v", err)
	}
	defer func() {
		if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
			t.Errorf("failed to delete readiness-delay KWOK stage: %v", err)
		}
	}()

	tests.Logger.Info("3. Scale sg-x out from 1 to 4 live replicas (front 1 + sg-x 4 = 5 pods)")
	tc.ScalePCSGAcrossAllReplicasAndWait(workloadName, "sg-x", 1, 4, 5, 0)

	tests.Logger.Info("4. Roll sg-x and verify the budget is honoured against the live count of 4")
	tcLong := *tc
	tcLong.Timeout = 2 * time.Minute
	peakOut, err := updateutils.MaxUnavailablePods(&tcLong, updateutils.NotReadyPodForPCSG(&tcLong, "sg-x"), func() error {
		prevHash := updateutils.PCSGenerationHash(t, &tcLong)
		if err := updateutils.TriggerPodCliqueUpdate(&tcLong, "pc-b"); err != nil {
			return err
		}
		if err := updateutils.WaitForGenerationHashChange(&tcLong, prevHash); err != nil {
			return err
		}
		return updateutils.WaitForRollingUpdateComplete(&tcLong, 1)
	})
	if err != nil {
		t.Fatalf("rolling update of sg-x at 4 replicas did not complete: %v", err)
	}
	assert.LessOrEqualf(t, peakOut, 2, "expected at most maxUnavailable=2 sg-x replicas unavailable at once with 4 live replicas, observed %d", peakOut)
	assert.Equalf(t, 2, peakOut, "expected the rollout to exercise the full budget of 2 with 4 live replicas (not clamp to the template value of 1), observed peak %d", peakOut)
	updateutils.AssertUpdateInProgressCleared(tc)

	tests.Logger.Info("5. Scale sg-x back in from 4 to 1 live replica (front 1 + sg-x 1 = 2 pods)")
	tc.ScalePCSGAcrossAllReplicasAndWait(workloadName, "sg-x", 1, 1, 2, 0)

	tests.Logger.Info("6. Roll sg-x again: the stored budget of 2 must not disrupt more than the single remaining replica")
	peakIn, err := updateutils.MaxUnavailablePods(&tcLong, updateutils.NotReadyPodForPCSG(&tcLong, "sg-x"), func() error {
		prevHash := updateutils.PCSGenerationHash(t, &tcLong)
		if err := updateutils.TriggerPodCliqueUpdate(&tcLong, "pc-b"); err != nil {
			return err
		}
		if err := updateutils.WaitForGenerationHashChange(&tcLong, prevHash); err != nil {
			return err
		}
		return updateutils.WaitForRollingUpdateComplete(&tcLong, 1)
	})
	if err != nil {
		t.Fatalf("rolling update of sg-x at 1 replica did not complete: %v", err)
	}
	assert.LessOrEqualf(t, peakIn, 1, "expected the rollout to disrupt at most the 1 remaining sg-x replica despite the stored budget of 2, observed %d", peakIn)
	assert.Equalf(t, 1, peakIn, "expected the single remaining sg-x replica to be rolled, observed peak %d", peakIn)
	updateutils.AssertUpdateInProgressCleared(tc)
}

// Test_RU29_RapidRevisionChangesConvergeToLatestTarget verifies that independently reconciling
// controllers do not execute or complete stale update progress when the PCS template rapidly moves
// revision-1 -> revision-2 -> revision-1 -> revision-2. It covers both update orchestration paths:
// a standalone PCLQ and a PCSG-owned PCLQ.
func Test_RU29_RapidRevisionChangesConvergeToLatestTarget(t *testing.T) {
	cases := []struct {
		name                 string
		cliqueName           string
		standaloneCliqueName string
		pcsgConfigName       string
	}{
		{name: "standalone PCLQ", cliqueName: "pc-a", standaloneCliqueName: "pc-a"},
		{name: "PCSG-owned PCLQ", cliqueName: "pc-b", pcsgConfigName: "sg-x"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tests.Logger.Info("1. Deploy workload1")
			tc, cleanup, _ := setupTest(t, testConfig{
				workloadName: "workload1",
				workloadYAML: "../../yaml/workload1.yaml",
				workerNodes:  10,
				expectedPods: 10,
			})
			defer cleanup()

			revision1 := tt.cliqueName + "-revision-1"
			revision2 := tt.cliqueName + "-revision-2"
			tcLong := *tc
			tcLong.Timeout = 2 * time.Minute

			tests.Logger.Info("2. Establish revision-1 as a fully completed baseline")
			require.NoError(t, updateutils.TriggerPodCliqueUpdateWithRevision(&tcLong, revision1, tt.cliqueName))
			require.NoError(t, updateutils.WaitForRollingUpdateComplete(&tcLong, 1))
			revision1GenerationHash := updateutils.PCSGenerationHash(t, &tcLong)

			tests.Logger.Info("3. Delay readiness and start revision-2")
			require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyDelayedPath))
			defer func() { _ = kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName) }()
			require.NoError(t, updateutils.TriggerPodCliqueUpdateWithRevision(&tcLong, revision2, tt.cliqueName))
			require.NoError(t, updateutils.WaitForGenerationHashChange(&tcLong, revision1GenerationHash))
			revision2GenerationHash := updateutils.PCSGenerationHash(t, &tcLong)
			require.NoError(t, waitForActiveChildUpdateProgressGeneration(&tcLong, tt.standaloneCliqueName, tt.pcsgConfigName, revision2GenerationHash))

			tests.Logger.Info("4. Revert to the exact revision-1 template while revision-2 is active")
			require.NoError(t, updateutils.TriggerPodCliqueUpdateWithRevision(&tcLong, revision1, tt.cliqueName))
			require.Eventually(t, func() bool {
				return updateutils.PCSGenerationHash(t, &tcLong) == revision1GenerationHash
			}, tcLong.Timeout, tcLong.Interval, "PCS did not return to the revision-1 generation hash")
			require.NoError(t, waitForActiveChildUpdateProgressGeneration(&tcLong, tt.standaloneCliqueName, tt.pcsgConfigName, revision1GenerationHash))

			tests.Logger.Info("5. Reapply the exact revision-2 template while the revert is active")
			require.NoError(t, updateutils.TriggerPodCliqueUpdateWithRevision(&tcLong, revision2, tt.cliqueName))
			require.Eventually(t, func() bool {
				return updateutils.PCSGenerationHash(t, &tcLong) == revision2GenerationHash
			}, tcLong.Timeout, tcLong.Interval, "PCS did not return to the revision-2 generation hash")
			require.NoError(t, waitForActiveChildUpdateProgressGeneration(&tcLong, tt.standaloneCliqueName, tt.pcsgConfigName, revision2GenerationHash))

			tests.Logger.Info("6. Let replacement Pods become Ready and verify final convergence")
			require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName))
			require.NoError(t, updateutils.WaitForRollingUpdateComplete(&tcLong, 1))
			require.NoError(t, tcLong.WaitForPods(10))
			updateutils.AssertUpdateInProgressCleared(&tcLong)
			updateutils.AssertGenerationHashConverged(&tcLong)
			updateutils.AssertAffectedPodsAtRevision(&tcLong, tt.cliqueName, revision2)
			updateutils.AssertNoActiveChildUpdates(&tcLong)
		})
	}
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
func verifyOnePodDeletedAtATime(tc *testctx.TestContext, events []updateutils.PodEvent) {
	tc.T.Helper()

	// Log all events for debugging
	tests.Logger.Debug("=== Starting verifyOnePodDeletedAtATime analysis ===")
	tests.Logger.Debugf("Total events captured: %d", len(events))

	for i, event := range events {
		podclique := ""
		if event.Pod.Labels != nil {
			podclique = event.Pod.Labels[apicommon.LabelPodClique]
		}
		tests.Logger.Debugf("Event[%d]: Type=%s PodName=%s Hostname=%s PodClique=%s Timestamp=%v",
			i, event.EventType, event.Pod.Name, event.Pod.Spec.Hostname, podclique, event.Timestamp.Format("15:04:05.000"))
	}

	// Track the most recent ADDED pod for each hostname (podID).
	// Key: hostname, Value: pod name of the most recent ADDED event
	addedPods := make(map[string]string)

	// Track DELETE events for each hostname.
	// Key: hostname, Value: pod name of the deleted pod
	deletedPods := make(map[string]string)

	maxConcurrentDeletions := 0
	var violationEvents []string

	tests.Logger.Debug("=== Processing events to find concurrent deletions ===")
	for i, event := range events {
		podID := getPodIdentifier(tc, event.Pod)
		podName := event.Pod.Name

		switch event.EventType {
		case watch.Deleted:
			// Record the DELETE event for this hostname
			deletedPods[podID] = podName
			tests.Logger.Debugf("Event[%d] DELETE: podID=%s, podName=%s", i, podID, podName)
		case watch.Added:
			// Record the ADDED event for this hostname
			addedPods[podID] = podName
			tests.Logger.Debugf("Event[%d] ADDED: podID=%s, podName=%s", i, podID, podName)
		case watch.Modified:
			tests.Logger.Debugf("Event[%d] MODIFIED: podID=%s (ignored for deletion tracking)", i, podID)
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
			tests.Logger.Debugf("Event[%d] State: currentlyDeleting=%d, activelyDeleting=%v",
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

	tests.Logger.Debugf("=== Analysis complete: maxConcurrentDeletions=%d ===", maxConcurrentDeletions)
	if len(violationEvents) > 0 {
		tests.Logger.Debug("Violation events:")
		for _, v := range violationEvents {
			tests.Logger.Debugf("  %s", v)
		}
	}

	// Assert that at most 1 pod was being deleted/replaced at any point in time,
	// which ensures the rolling update respects the MaxUnavailable=1 constraint at the global level.
	if maxConcurrentDeletions > 1 {
		tc.T.Fatalf("Expected at most 1 pod being deleted at a time, but found %d concurrent deletions", maxConcurrentDeletions)
	}
}

// getPodIdentifier returns a stable identifier for a pod based on its logical position in the workload.
// This identifier remains the same when a pod is replaced during rolling updates.
//
// Stability is guaranteed because the pod's internal hostname (pod.Spec.Hostname) is set by the
// operator based on the pod's logical position in the PodClique (pclqName-podIndex), not the pod's
// generated name. Note: this is the Kubernetes pod hostname field (what `hostname` returns inside
// the container), NOT the node/host where the pod is scheduled.
//
// See: internal/controller/podclique/components/pod/pod.go - configurePodHostname()
//
// During a rolling update:
//  1. The old pod is deleted (e.g., pod.Spec.Hostname: "workload1-pc-a-0-0")
//  2. The PodClique remains unchanged (same name and replica count)
//  3. The new pod is created to fill the same logical position
//  4. The new pod receives the same pod.Spec.Hostname (e.g., "workload1-pc-a-0-0")
//
// Only the pod name changes (due to GenerateName), while pod.Spec.Hostname represents the pod's
// logical position in the workload hierarchy and remains constant.
func getPodIdentifier(tc *testctx.TestContext, pod *corev1.Pod) string {
	tc.T.Helper()

	// Use hostname as the stable identifier (set by configurePodHostname in pod.go)
	// Format: {pclqName}-{podIndex} (e.g., "workload1-pc-a-0-0")
	if pod.Spec.Hostname == "" {
		tc.T.Fatalf("Pod %s does not have hostname set, cannot determine stable identifier", pod.Name)
	}

	return pod.Spec.Hostname
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
func verifySinglePCSReplicaUpdatedFirst(tc *testctx.TestContext, events []updateutils.PodEvent) {
	tc.T.Helper()

	tests.Logger.Debug("=== Starting verifySinglePCSReplicaUpdatedFirst analysis ===")
	tests.Logger.Debugf("Total events captured: %d", len(events))

	// Log all events for debugging
	for i, event := range events {
		replicaIdx := 0
		if val, ok := event.Pod.Labels[apicommon.LabelPodCliqueSetReplicaIndex]; ok {
			replicaIdx, _ = strconv.Atoi(val)
		}
		podclique := ""
		if event.Pod.Labels != nil {
			podclique = event.Pod.Labels[apicommon.LabelPodClique]
		}
		tests.Logger.Debugf("Event[%d]: Type=%s PodName=%s Hostname=%s Replica=%d PodClique=%s Timestamp=%v",
			i, event.EventType, event.Pod.Name, event.Pod.Spec.Hostname, replicaIdx, podclique, event.Timestamp.Format("15:04:05.000"))
	}

	// Track DELETE and ADDED counts separately per pod identifier per replica to handle out-of-order events.
	// Structure: replicaIndex -> podID -> count
	deletedCount := make(map[int]map[string]int)
	addedCount := make(map[int]map[string]int)

	maxConcurrentUpdatingReplicas := 0
	var violationEvent string

	tests.Logger.Debug("=== Processing events to find concurrent replica updates ===")
	for i, event := range events {
		// Extract replica index from pod labels (defaults to 0 if not present)
		replicaIdx := 0
		if val, ok := event.Pod.Labels[apicommon.LabelPodCliqueSetReplicaIndex]; ok {
			replicaIdx, _ = strconv.Atoi(val)
		}

		// Extract PodClique name - skip pods without this label
		_, ok := event.Pod.Labels[apicommon.LabelPodClique]
		if !ok {
			continue
		}

		// Get stable pod identifier (hostname) that persists across pod replacements
		podID := getPodIdentifier(tc, event.Pod)

		// Track DELETE and ADDED events
		switch event.EventType {
		case watch.Deleted:
			if deletedCount[replicaIdx] == nil {
				deletedCount[replicaIdx] = make(map[string]int)
			}
			deletedCount[replicaIdx][podID]++
			tests.Logger.Debugf("Event[%d] DELETE: replica=%d podID=%s deleted=%d added=%d",
				i, replicaIdx, podID, deletedCount[replicaIdx][podID], getReplicaPodCount(addedCount, replicaIdx, podID))

		case watch.Added:
			if addedCount[replicaIdx] == nil {
				addedCount[replicaIdx] = make(map[string]int)
			}
			addedCount[replicaIdx][podID]++
			tests.Logger.Debugf("Event[%d] ADDED: replica=%d podID=%s deleted=%d added=%d",
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
			tests.Logger.Debugf("Event[%d] State: numUpdatingReplicas=%d activelyUpdating=%v",
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
				i, event.EventType, maxConcurrentUpdatingReplicas, replicas)
		}
	}

	tests.Logger.Debugf("=== Analysis complete: maxConcurrentUpdatingReplicas=%d ===", maxConcurrentUpdatingReplicas)
	if violationEvent != "" {
		tests.Logger.Debugf("Max concurrent replicas event: %s", violationEvent)
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

// assertMaxUnavailableOmitted fails the test unless the named clique keeps an omitted MaxUnavailable unset.
func assertMaxUnavailableOmitted(tc *testctx.TestContext, cliqueName string) {
	tc.T.Helper()
	pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
	if err != nil {
		tc.T.Fatalf("failed to get PodCliqueSet: %v", err)
	}
	for _, clique := range pcs.Spec.Template.Cliques {
		if clique.Name != cliqueName {
			continue
		}
		if clique.RollingUpdate != nil && clique.RollingUpdate.MaxUnavailable != nil {
			tc.T.Fatalf("MaxUnavailable for clique %s = %d, want omitted", cliqueName, *clique.RollingUpdate.MaxUnavailable)
		}
		return
	}
	tc.T.Fatalf("clique %s not found in PodCliqueSet %s", cliqueName, tc.Workload.Name)
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
func verifyOnePCSGReplicaDeletedAtATime(tc *testctx.TestContext, events []updateutils.PodEvent) {
	tc.T.Helper()

	tests.Logger.Debug("=== Starting verifyOnePCSGReplicaDeletedAtATime analysis ===")
	tests.Logger.Debugf("Total events captured: %d", len(events))

	// Log all PCSG-related events for debugging
	for i, event := range events {
		if event.Pod.Labels == nil {
			continue
		}
		replicaID, hasReplicaLabel := event.Pod.Labels[apicommon.LabelPodCliqueScalingGroupReplicaIndex]
		if !hasReplicaLabel {
			continue
		}
		pcsgName := event.Pod.Labels[apicommon.LabelPodCliqueScalingGroup]
		podclique := event.Pod.Labels[apicommon.LabelPodClique]
		tests.Logger.Debugf("Event[%d]: Type=%s PodName=%s PCSG=%s ReplicaID=%s PodClique=%s Timestamp=%v",
			i, event.EventType, event.Pod.Name, pcsgName, replicaID, podclique, event.Timestamp.Format("15:04:05.000"))
	}

	// Track DELETE and ADDED counts separately per replica to handle out-of-order events.
	// Key format: "<pcsg-name>-<replica-index>"
	deletedCount := make(map[string]int)
	addedCount := make(map[string]int)
	maxConcurrentDeletions := 0
	var violationEvent string

	tests.Logger.Debug("=== Processing events to find concurrent deletions ===")
	for i, event := range events {
		// All pods should have labels - if nil, that's a bug
		if event.Pod.Labels == nil {
			tc.T.Fatalf("Pod %s has no labels, which indicates a bug in pod creation", event.Pod.Name)
		}

		// Only process pods that belong to a PCSG (not all pods have this label)
		replicaID, hasReplicaLabel := event.Pod.Labels[apicommon.LabelPodCliqueScalingGroupReplicaIndex]
		if !hasReplicaLabel {
			continue
		}

		pcsgName, ok := event.Pod.Labels[apicommon.LabelPodCliqueScalingGroup]
		if !ok {
			tc.T.Fatalf("Pod %s has PCSG replica index but no PCSG name label", event.Pod.Name)
		}

		// Create a composite key to uniquely identify this PCSG replica globally
		replicaKey := fmt.Sprintf("%s-%s", pcsgName, replicaID)

		switch event.EventType {
		case watch.Deleted:
			deletedCount[replicaKey]++
			tests.Logger.Debugf("Event[%d] DELETE: pod=%s replica=%s deleted=%d added=%d",
				i, event.Pod.Name, replicaKey, deletedCount[replicaKey], addedCount[replicaKey])
		case watch.Added:
			addedCount[replicaKey]++
			tests.Logger.Debugf("Event[%d] ADDED: pod=%s replica=%s deleted=%d added=%d",
				i, event.Pod.Name, replicaKey, deletedCount[replicaKey], addedCount[replicaKey])
		}

		// Calculate "actively rolling" Replicas: those where DELETE count > ADDED count.
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
			tests.Logger.Debugf("Event[%d] State: concurrent=%d activelyRolling=%v", i, currentConcurrent, replicas)
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

	tests.Logger.Debugf("=== Analysis complete: maxConcurrentDeletions=%d ===", maxConcurrentDeletions)
	if violationEvent != "" {
		tests.Logger.Debugf("Violation: %s", violationEvent)
	}

	// Assert that at most 1 replica was being deleted/replaced at any point in time.
	// This ensures the rolling update respects the MaxUnavailable=1 constraint at the global level.
	if maxConcurrentDeletions > 1 {
		tc.T.Fatalf("Expected at most 1 PCSG replica being deleted at a time, but found %d concurrent deletions", maxConcurrentDeletions)
	}
}

// verifyOnePodDeletedAtATimePerPodClique verifies only one pod per Podclique is being deleted at a time
// during rolling updates.
//
// This function processes a sequence of pod events and tracks the number of individual pods
// that are in a "deleting" state within each Podclique at any given time.
//
// IMPORTANT: This function handles the fact that Kubernetes watch events can arrive out of order.
// See verifyOnePCSGReplicaDeletedAtATime for detailed explanation.
func verifyOnePodDeletedAtATimePerPodClique(tc *testctx.TestContext, events []updateutils.PodEvent) {
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

		podcliqueName, ok := event.Pod.Labels[apicommon.LabelPodClique]
		if !ok {
			tc.T.Fatalf("Pod %s does not have grove.io/podclique label", event.Pod.Name)
		}

		podID := getPodIdentifier(tc, event.Pod)

		switch event.EventType {
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

// verifyOnePCSGReplicaDeletedAtATimePerPCSG verifies only one replica per PCSG is deleted at a time
// during rolling updates.
//
// SCOPE: PER-PCSG (allows concurrent rolling across different PCSGs)
// This constraint allows multiple PCSGs to roll simultaneously, but within each PCSG, only one replica
// can be rolling at a time. For example, if you have sg-x and sg-y:
//   - ALLOWED: sg-x replica 0 AND sg-y replica 0 rolling simultaneously
//   - NOT ALLOWED: sg-x replica 0 AND sg-x replica 1 rolling simultaneously
//
// Compare with verifyOnePCSGReplicaDeletedAtATime which enforces a global constraint (stricter).
//
// IMPORTANT: This function handles the fact that Kubernetes watch events can arrive out of order.
// See verifyOnePCSGReplicaDeletedAtATime for detailed explanation.
func verifyOnePCSGReplicaDeletedAtATimePerPCSG(tc *testctx.TestContext, events []updateutils.PodEvent) {
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
		replicaID, hasReplicaLabel := event.Pod.Labels[apicommon.LabelPodCliqueScalingGroupReplicaIndex]
		if !hasReplicaLabel {
			continue
		}

		pcsgName, ok := event.Pod.Labels[apicommon.LabelPodCliqueScalingGroup]
		if !ok {
			tc.T.Fatalf("Pod %s has PCSG replica index but no PCSG name label", event.Pod.Name)
		}

		switch event.EventType {
		case watch.Deleted:
			if deletedCount[pcsgName] == nil {
				deletedCount[pcsgName] = make(map[string]int)
			}
			deletedCount[pcsgName][replicaID]++
			tests.Logger.Debugf("Pod %s deleted, replica %s in PCSG %s: deleted=%d added=%d",
				event.Pod.Name, replicaID, pcsgName,
				deletedCount[pcsgName][replicaID], getCount(addedCount, pcsgName, replicaID))
		case watch.Added:
			if addedCount[pcsgName] == nil {
				addedCount[pcsgName] = make(map[string]int)
			}
			addedCount[pcsgName][replicaID]++
			tests.Logger.Debugf("Pod %s added, replica %s in PCSG %s: deleted=%d added=%d",
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

// triggerRollingUpdate triggers a rolling update on the specified cliques and returns a channel
// that receives an error (or nil) when the rolling update finishes.
// Uses tc.Workload.Name as the PCS name and tc.Timeout for the wait timeout.
func triggerRollingUpdate(tc *testctx.TestContext, expectedReplicas int32, cliqueNames ...string) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		startTime := time.Now()

		if err := updateutils.TriggerPodCliqueUpdate(tc, cliqueNames...); err != nil {
			errCh <- fmt.Errorf("failed to update PodCliques %v: %w", cliqueNames, err)
			return
		}
		tests.Logger.Debugf("[triggerRollingUpdate] Triggered update on %v, waiting for completion...", cliqueNames)

		// Wait for completion
		err := updateutils.WaitForRollingUpdateComplete(tc, expectedReplicas)
		elapsed := time.Since(startTime)
		if err != nil {
			tests.Logger.Debugf("[triggerRollingUpdate] Rolling update FAILED after %v: %v", elapsed, err)
		} else {
			tests.Logger.Debugf("[triggerRollingUpdate] Rolling update completed in %v", elapsed)
		}
		errCh <- err
	}()
	return errCh
}

// waitForRollingUpdate starts polling for rolling update completion in the background and returns a channel.
// Use this when you need to trigger an update separately (e.g., when doing something between trigger and wait).
// For the common case, use triggerRollingUpdate which combines trigger + wait.
func waitForRollingUpdate(tc *testctx.TestContext, expectedReplicas int32) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- updateutils.WaitForRollingUpdateComplete(tc, expectedReplicas)
	}()
	return errCh
}

// scalePodClique scales a PodClique and returns a channel that receives an error when the expected pod count is reached.
// Uses tc.Workload.Name as the PCS name.
// The operation runs asynchronously - receive from the returned channel to block until complete.
// If delayMs > 0, the operation will sleep for that duration before starting.
func scalePodClique(tc *testctx.TestContext, cliqueName string, replicas int32, expectedTotalPods, delayMs int) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		startTime := time.Now()

		if delayMs > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}

		tests.Logger.Debugf("[scalePodClique] Scaling %s to %d replicas, expecting %d total pods", cliqueName, replicas, expectedTotalPods)

		if err := updateutils.ScalePodCliqueInPCS(tc, cliqueName, replicas); err != nil {
			errCh <- fmt.Errorf("failed to scale PodClique %s: %w", cliqueName, err)
			return
		}

		tests.Logger.Debugf("[scalePodClique] Scale patch applied, waiting for pods...")

		// Wait for pods to reach expected count
		pm := pods.NewPodManager(tc.Client, tests.Logger)
		_, err := pm.WaitForCount(tc.Ctx, tc.Namespace, tc.GetLabelSelector(), expectedTotalPods, tc.Timeout, tc.Interval)
		elapsed := time.Since(startTime)
		if err != nil {
			tests.Logger.Debugf("[scalePodClique] Scale %s FAILED after %v: %v", cliqueName, elapsed, err)
			errCh <- fmt.Errorf("failed to wait for pods after scaling PodClique %s: %w", cliqueName, err)
			return
		}
		tests.Logger.Debugf("[scalePodClique] Scale %s completed in %v (pods=%d)", cliqueName, elapsed, expectedTotalPods)
		errCh <- nil
	}()
	return errCh
}

// waitForPodCliqueUpdateCondition waits for a standalone PodClique's UpdateInProgress condition to match.
func waitForPodCliqueUpdateCondition(tc *testctx.TestContext, cliqueName string, wantStatus metav1.ConditionStatus, wantReason string) error {
	name := updateutils.StandaloneCliqueFQN(tc, cliqueName)
	fetch := waiter.FetchFunc[*grovev1beta1.PodClique](func(ctx context.Context) (*grovev1beta1.PodClique, error) {
		var pclq grovev1beta1.PodClique
		if err := tc.Client.Get(ctx, types.NamespacedName{Name: name, Namespace: tc.Namespace}, &pclq); err != nil {
			return nil, err
		}
		return &pclq, nil
	})
	_, err := waiter.New[*grovev1beta1.PodClique]().WithTimeout(tc.Timeout).WithInterval(tc.Interval).WithRetryOnError().
		WaitFor(tc.Ctx, fetch, updateutils.UpdateInProgressConditionMet(func(p *grovev1beta1.PodClique) []metav1.Condition { return p.Status.Conditions }, wantStatus, wantReason))
	if err != nil {
		return fmt.Errorf("PodClique %s UpdateInProgress did not reach status=%s reason=%s: %w", name, wantStatus, wantReason, err)
	}
	return nil
}

// assertPodCliqueUpdateNotComplete fails the test unless the named standalone PodClique has a rolling
// update still in progress (UpdateProgress set with UpdateEndedAt nil). This guards against premature
// completion while replacements are not yet Ready (issue #786).
func assertPodCliqueUpdateNotComplete(tc *testctx.TestContext, cliqueName string) {
	tc.T.Helper()
	name := updateutils.StandaloneCliqueFQN(tc, cliqueName)
	var pclq grovev1beta1.PodClique
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Name: name, Namespace: tc.Namespace}, &pclq); err != nil {
		tc.T.Fatalf("failed to get PodClique %s: %v", name, err)
	}
	if pclq.Status.UpdateProgress == nil || pclq.Status.UpdateProgress.UpdateEndedAt != nil {
		tc.T.Fatalf("PodClique %s rolling update was marked complete while stalled, UpdateProgress=%+v", name, pclq.Status.UpdateProgress)
	}
}

// assertGangTerminationSuspended fails the test unless the named standalone PodClique's
// MinAvailableBreached condition is Unknown with reason UpdateInProgress, which is how gang
// termination stays suspended for a component that is (or is stuck) updating.
func assertGangTerminationSuspended(tc *testctx.TestContext, cliqueName string) {
	tc.T.Helper()
	name := updateutils.StandaloneCliqueFQN(tc, cliqueName)
	var pclq grovev1beta1.PodClique
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Name: name, Namespace: tc.Namespace}, &pclq); err != nil {
		tc.T.Fatalf("failed to get PodClique %s: %v", name, err)
	}
	cond := meta.FindStatusCondition(pclq.Status.Conditions, apiconstants.ConditionTypeMinAvailableBreached)
	if cond == nil || cond.Status != metav1.ConditionUnknown || cond.Reason != apiconstants.ConditionReasonUpdateInProgress {
		tc.T.Fatalf("expected MinAvailableBreached suspended (Unknown/UpdateInProgress) for %s, got %+v", name, cond)
	}
}

// waitForActiveChildUpdateProgressGeneration waits until the selected standalone PCLQ or PCSG has
// active update progress whose captured PCS generation hash equals generationHash. The readiness-delay
// KWOK stage keeps the update active long enough for this milestone to be observed.
func waitForActiveChildUpdateProgressGeneration(tc *testctx.TestContext, standaloneCliqueName, pcsgConfigName, generationHash string) error {
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		if standaloneCliqueName != "" {
			var pclq grovev1beta1.PodClique
			key := types.NamespacedName{Namespace: tc.Namespace, Name: updateutils.StandaloneCliqueFQN(tc, standaloneCliqueName)}
			if err := tc.Client.Get(ctx, key, &pclq); err != nil {
				return false, client.IgnoreNotFound(err)
			}
			progress := pclq.Status.UpdateProgress
			return progress != nil && progress.UpdateEndedAt == nil &&
				progress.PodCliqueSetGenerationHash == generationHash, nil
		}

		var pcsg grovev1beta1.PodCliqueScalingGroup
		key := types.NamespacedName{Namespace: tc.Namespace, Name: updateutils.PCSGFQN(tc, pcsgConfigName)}
		if err := tc.Client.Get(ctx, key, &pcsg); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		progress := pcsg.Status.UpdateProgress
		return progress != nil && progress.UpdateEndedAt == nil &&
			progress.PodCliqueSetGenerationHash == generationHash, nil
	})
}

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
	"testing"
	"time"

	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/kwok"
	"github.com/ai-dynamo/grove/operator/e2e/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test_RR1_PCSGMinAvailableIncreaseRecreatesReplica verifies that increasing a PodCliqueScalingGroup
// MinAvailable triggers structural PCS replica recreation (not an in-place pod rolling update), reshapes
// the PodGangMap anchor and tail entries to the new MinAvailable, advances the applied scheduling
// configuration hash, and converges with every replica available.
//
// Scenario:
//  1. Deploy workload2 (1 PCS replica; sg-x replicas=2, minAvailable=1).
//  2. Capture the pre-change PodGangMap entries and applied scheduling hash. The anchor carries sg-x
//     index [0] and the tail carries index [1].
//  3. Keep recreation observable with the readiness-delay KWOK stage.
//  4. Increase sg-x MinAvailable 1 -> 2.
//  5. Observe active structural recreation while it is in flight.
//  6. Remove the readiness-delay stage so recreation completes.
//  7. Assert the applied scheduling hash advanced, the recreated PodGangMap anchor carries sg-x indices
//     [0, 1] with no tail entry, every entry is at the new generation hash, and all pods are available.
func Test_RR1_PCSGMinAvailableIncreaseRecreatesReplica(t *testing.T) {
	tests.Logger.Info("1. Deploy workload2 with a scaling group sg-x at minAvailable 1")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload2",
		workloadYAML: "../../yaml/workload2.yaml",
		workerNodes:  10,
		expectedPods: 10,
	})
	defer cleanup()

	tests.Logger.Info("2. Capture the pre-change PodGangMap entries and applied scheduling hash")
	beforeEntries := getPodGangMapEntries(t, tc, 0)
	beforeAnchor := requireSingleEntryByRole(t, beforeEntries, grovev1beta1.PodGangEntryRoleAnchor)
	assertPodGangEntryPCSGIndices(t, beforeAnchor, "sg-x", []int32{0})
	beforeTail := requireSingleEntryByRole(t, beforeEntries, grovev1beta1.PodGangEntryRoleTail)
	assertPodGangEntryPCSGIndices(t, beforeTail, "sg-x", []int32{1})

	beforeHash, err := getPCSAppliedSchedulingHash(tc)
	require.NoError(t, err)
	require.NotEmpty(t, beforeHash)

	tests.Logger.Info("3. Apply the readiness-delay KWOK stage so recreation stays observable")
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyDelayedPath); err != nil {
		t.Fatalf("failed to apply readiness-delay KWOK stage: %v", err)
	}
	stageRemoved := false
	defer func() {
		if !stageRemoved {
			if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
				t.Errorf("failed to delete readiness-delay KWOK stage: %v", err)
			}
		}
	}()

	tests.Logger.Info("4. Increase sg-x MinAvailable 1 -> 2")
	require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 2))

	tests.Logger.Info("5. Observe active structural PCS replica recreation in flight")
	require.NoError(t, waitForActivePCSReplicaRecreation(tc))

	tests.Logger.Info("6. Remove the readiness-delay stage so recreation completes")
	if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
		t.Fatalf("failed to delete readiness-delay KWOK stage: %v", err)
	}
	stageRemoved = true

	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 2 * time.Minute
	require.NoError(t, waitForReplicaRecreationComplete(&tcLongTimeout, 1))

	tests.Logger.Info("7. Assert the applied scheduling hash advanced and the PodGangMap was reshaped")
	afterHash, err := getPCSAppliedSchedulingHash(tc)
	require.NoError(t, err)
	assert.NotEqual(t, beforeHash, afterHash, "applied scheduling hash should advance after recreation")

	currentHash := getPCSGenerationHash(t, tc)
	afterEntries := getPodGangMapEntries(t, tc, 0)
	afterAnchor := requireSingleEntryByRole(t, afterEntries, grovev1beta1.PodGangEntryRoleAnchor)
	assertPodGangEntryPCSGIndices(t, afterAnchor, "sg-x", []int32{0, 1})
	for _, entry := range afterEntries {
		assert.Equalf(t, currentHash, entry.PodCliqueSetGenerationHash,
			"PodGangMap entry (role %s, epoch %s) should be at the current generation hash", entry.Role, entry.Epoch)
		assert.NotEqualf(t, grovev1beta1.PodGangEntryRoleTail, entry.Role,
			"a tail entry should not remain once sg-x MinAvailable equals its replica count")
	}

	require.NoError(t, tc.WaitForPods(10))
}

// Test_RR2_StandalonePCLQMinAvailableIncreaseRecreatesReplica verifies that increasing a standalone PodClique
// MinAvailable triggers structural PCS replica recreation, re-materializes the PodGangMap at the new generation
// hash, advances the applied scheduling configuration hash, and records the new MinAvailable on the recreated
// PodClique. A standalone PodClique carries its full replica count on the PodGangMap anchor, so unlike a
// PodCliqueScalingGroup MinAvailable change the anchor pod count does not change.
//
// Scenario:
//  1. Deploy workload2 (1 PCS replica; standalone pc-a replicas=2, minAvailable=1).
//  2. Capture the pre-change applied scheduling hash and the anchor pc-a pod count.
//  3. Keep recreation observable with the readiness-delay KWOK stage.
//  4. Increase pc-a MinAvailable 1 -> 2.
//  5. Observe active structural recreation while it is in flight.
//  6. Remove the readiness-delay stage so recreation completes.
//  7. Assert the applied scheduling hash advanced, every PodGangMap entry is at the new generation hash, the
//     anchor pc-a pod count is unchanged, the recreated pc-a records MinAvailable 2, and all pods are available.
func Test_RR2_StandalonePCLQMinAvailableIncreaseRecreatesReplica(t *testing.T) {
	tests.Logger.Info("1. Deploy workload2 with a standalone PodClique pc-a at minAvailable 1")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload2",
		workloadYAML: "../../yaml/workload2.yaml",
		workerNodes:  10,
		expectedPods: 10,
	})
	defer cleanup()

	tests.Logger.Info("2. Capture the pre-change applied scheduling hash and anchor pc-a pod count")
	beforeHash, err := getPCSAppliedSchedulingHash(tc)
	require.NoError(t, err)
	require.NotEmpty(t, beforeHash)
	beforeAnchor := requireSingleEntryByRole(t, getPodGangMapEntries(t, tc, 0), grovev1beta1.PodGangEntryRoleAnchor)
	assertStandalonePCLQPodCounts(t, beforeAnchor, map[string]int32{"pc-a": 2})

	tests.Logger.Info("3. Apply the readiness-delay KWOK stage so recreation stays observable")
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyDelayedPath); err != nil {
		t.Fatalf("failed to apply readiness-delay KWOK stage: %v", err)
	}
	stageRemoved := false
	defer func() {
		if !stageRemoved {
			if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
				t.Errorf("failed to delete readiness-delay KWOK stage: %v", err)
			}
		}
	}()

	tests.Logger.Info("4. Increase pc-a MinAvailable 1 -> 2")
	require.NoError(t, triggerStandalonePCLQMinAvailableChange(tc, "pc-a", 2))

	tests.Logger.Info("5. Observe active structural PCS replica recreation in flight")
	require.NoError(t, waitForActivePCSReplicaRecreation(tc))

	tests.Logger.Info("6. Remove the readiness-delay stage so recreation completes")
	if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
		t.Fatalf("failed to delete readiness-delay KWOK stage: %v", err)
	}
	stageRemoved = true

	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 2 * time.Minute
	require.NoError(t, waitForReplicaRecreationComplete(&tcLongTimeout, 1))

	tests.Logger.Info("7. Assert the applied scheduling hash advanced and the recreated PodClique records the new MinAvailable")
	afterHash, err := getPCSAppliedSchedulingHash(tc)
	require.NoError(t, err)
	assert.NotEqual(t, beforeHash, afterHash, "applied scheduling hash should advance after recreation")

	currentHash := getPCSGenerationHash(t, tc)
	afterAnchor := requireSingleEntryByRole(t, getPodGangMapEntries(t, tc, 0), grovev1beta1.PodGangEntryRoleAnchor)
	assertStandalonePCLQPodCounts(t, afterAnchor, map[string]int32{"pc-a": 2})
	for _, entry := range getPodGangMapEntries(t, tc, 0) {
		assert.Equalf(t, currentHash, entry.PodCliqueSetGenerationHash,
			"PodGangMap entry (role %s, epoch %s) should be at the current generation hash", entry.Role, entry.Epoch)
	}

	minAvailable, err := getStandalonePCLQMinAvailable(tc, "pc-a")
	require.NoError(t, err)
	assert.Equal(t, int32(2), minAvailable, "recreated pc-a should record the new MinAvailable")

	require.NoError(t, tc.WaitForPods(10))
}

// Test_RR3_DifferentIndexReplacementFillsIndexGap verifies that when a scheduling-configuration change drives
// recreation on a PodCliqueSet whose claimed replica indices have a gap, the replacement is a different-index
// replacement that fills the lowest available index (the gap) rather than reusing the source index.
//
// Scenario:
//  1. Deploy workload-recreation-standalone and scale it to 3 PCS replicas, claiming indices 0, 1, 2.
//  2. Block replica 1 from becoming available, then scale to 2 replicas. Scale-in selects the unavailable
//     replica 1, leaving claimed indices 0 and 2 with a gap at index 1.
//  3. Release the block and increase the worker MinAvailable 1 -> 2 to trigger recreation.
//  4. Observe a different-index replacement whose target is the gap index 1.
//  5. Assert the replacement completes, the claimed indices become 0 and 1, the applied scheduling hash
//     advanced, and all pods are available.
func Test_RR3_DifferentIndexReplacementFillsIndexGap(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-recreation-standalone and scale to 3 PCS replicas")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName:       "workload-recreation-standalone",
		workloadYAML:       "../../yaml/workload-recreation-standalone.yaml",
		workerNodes:        10,
		expectedPods:       2,
		initialPCSReplicas: 3,
		postScalePods:      6,
	})
	defer cleanup()

	claimed, err := getClaimedPCSReplicaIndices(tc)
	require.NoError(t, err)
	require.Equal(t, []int{0, 1, 2}, claimed)

	beforeHash, err := getPCSAppliedSchedulingHash(tc)
	require.NoError(t, err)
	require.NotEmpty(t, beforeHash)

	tests.Logger.Info("2. Block replica 1 and scale to 2 replicas so scale-in selects replica 1")
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Path); err != nil {
		t.Fatalf("failed to apply replica 1 readiness-block KWOK stage: %v", err)
	}
	blockRemoved := false
	defer func() {
		if !blockRemoved {
			if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Name); err != nil {
				t.Errorf("failed to delete replica 1 readiness-block KWOK stage: %v", err)
			}
		}
	}()

	// Delete replica 1's pods so the operator recreates them. The readiness-block stage catches the fresh pods
	// and holds them not-ready, which makes replica 1 the unavailable replica that scale-in selects.
	require.NoError(t, deletePodsForReplica(tc, 1))
	require.NoError(t, waitForStandaloneReplicaUnavailable(tc, "worker", 1))

	if err := tc.ScalePCS(tc.Workload.Name, 2); err != nil {
		t.Fatalf("failed to scale PodCliqueSet to 2 replicas: %v", err)
	}
	require.NoError(t, waitForClaimedPCSReplicaIndices(tc, []int{0, 2}))

	tests.Logger.Info("3. Release the block and increase worker MinAvailable 1 -> 2")
	if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Name); err != nil {
		t.Fatalf("failed to delete replica 1 readiness-block KWOK stage: %v", err)
	}
	blockRemoved = true
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyDelayedPath); err != nil {
		t.Fatalf("failed to apply readiness-delay KWOK stage: %v", err)
	}
	delayRemoved := false
	defer func() {
		if !delayRemoved {
			if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
				t.Errorf("failed to delete readiness-delay KWOK stage: %v", err)
			}
		}
	}()
	require.NoError(t, triggerStandalonePCLQMinAvailableChange(tc, "worker", 2))

	tests.Logger.Info("4. Observe a different-index replacement whose target is the gap index 1")
	sourceReplicaIndex, err := waitForActiveReplacementTargetingIndex(tc, 1)
	require.NoError(t, err)
	assert.Equal(t, int32(2), sourceReplicaIndex, "the gap at index 1 should be filled by recreating the surviving replica 2")

	tests.Logger.Info("5. Release the delay and assert the recreation completed and filled the gap")
	if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
		t.Fatalf("failed to delete readiness-delay KWOK stage: %v", err)
	}
	delayRemoved = true

	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 2 * time.Minute
	require.NoError(t, waitForReplicaRecreationComplete(&tcLongTimeout, 2))

	afterHash, err := getPCSAppliedSchedulingHash(tc)
	require.NoError(t, err)
	assert.NotEqual(t, beforeHash, afterHash, "applied scheduling hash should advance after recreation")
	require.NoError(t, waitForClaimedPCSReplicaIndices(&tcLongTimeout, []int{0, 1}))
	require.NoError(t, tc.WaitForPods(4))
}

// Test_RR4_ScaleInCancelsReplacement verifies that scaling in a PodCliqueSet whose claimed indices are being
// recreated cancels the replacement whose target scale-in reclaims, rather than scaling the target in. The
// canceled replacement records CanceledAt and a cancellation message, and both its source and target drain so
// the PodCliqueSet converges to the desired replica count.
//
// Scenario:
//  1. Deploy workload-recreation-standalone and drive it to claimed indices {0, 2} with a gap at index 1,
//     keeping replica 1 blocked so a pod created at index 1 stays not-ready.
//  2. Increase worker MinAvailable 1 -> 2 to trigger recreation, and observe a different-index replacement
//     whose target is the gap index 1. The block keeps the target not-ready, so the replacement stays active.
//  3. Start a PCS watch before scaling to 1 replica. Scale-in selects the in-progress target index 1, which
//     cancels the replacement of source index 2 instead of scaling the target in.
//  4. Observe cancellation through the PCS watch, even if the drained entry is immediately removed.
//  5. Release the block and assert the PodCliqueSet converges to a single claimed index with no active
//     replacement, and the applied scheduling hash advanced.
func Test_RR4_ScaleInCancelsReplacement(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-recreation-standalone and create a gap at index 1")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName:       "workload-recreation-standalone",
		workloadYAML:       "../../yaml/workload-recreation-standalone.yaml",
		workerNodes:        10,
		expectedPods:       2,
		initialPCSReplicas: 3,
		postScalePods:      6,
	})
	defer cleanup()

	beforeHash, err := getPCSAppliedSchedulingHash(tc)
	require.NoError(t, err)
	require.NotEmpty(t, beforeHash)

	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Path); err != nil {
		t.Fatalf("failed to apply replica 1 readiness-block KWOK stage: %v", err)
	}
	blockRemoved := false
	defer func() {
		if !blockRemoved {
			if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Name); err != nil {
				t.Errorf("failed to delete replica 1 readiness-block KWOK stage: %v", err)
			}
		}
	}()
	require.NoError(t, deletePodsForReplica(tc, 1))
	require.NoError(t, waitForStandaloneReplicaUnavailable(tc, "worker", 1))
	if err := tc.ScalePCS(tc.Workload.Name, 2); err != nil {
		t.Fatalf("failed to scale PodCliqueSet to 2 replicas: %v", err)
	}
	require.NoError(t, waitForClaimedPCSReplicaIndices(tc, []int{0, 2}))

	tests.Logger.Info("2. Increase worker MinAvailable 1 -> 2 and observe an active replacement targeting index 1")
	require.NoError(t, triggerStandalonePCLQMinAvailableChange(tc, "worker", 2))
	sourceReplicaIndex, err := waitForActiveReplacementTargetingIndex(tc, 1)
	require.NoError(t, err)
	require.Equal(t, int32(2), sourceReplicaIndex)

	tests.Logger.Info("3. Watch PCS status before scaling to 1 replica")
	watchCtx, cancelWatch := context.WithTimeout(tc.Ctx, tc.Timeout)
	defer cancelWatch()
	pcsWatcher, err := watchPodCliqueSet(watchCtx, tc)
	require.NoError(t, err)
	defer pcsWatcher.Stop()

	if err := tc.ScalePCS(tc.Workload.Name, 1); err != nil {
		t.Fatalf("failed to scale PodCliqueSet to 1 replica: %v", err)
	}

	tests.Logger.Info("4. Observe the canceled replacement and its message through the PCS status watch")
	cancellationMessage, err := waitForCanceledReplacement(watchCtx, pcsWatcher, sourceReplicaIndex)
	require.NoError(t, err)
	assert.NotEmpty(t, cancellationMessage, "a canceled replacement should record a cancellation message")

	tests.Logger.Info("5. Release the block and assert convergence to a single replica with no active replacement")
	if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Name); err != nil {
		t.Fatalf("failed to delete replica 1 readiness-block KWOK stage: %v", err)
	}
	blockRemoved = true

	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 2 * time.Minute
	require.NoError(t, waitForReplicaRecreationComplete(&tcLongTimeout, 1))

	afterHash, err := getPCSAppliedSchedulingHash(tc)
	require.NoError(t, err)
	assert.NotEqual(t, beforeHash, afterHash, "applied scheduling hash should advance after recreation")
	claimed, err := getClaimedPCSReplicaIndices(tc)
	require.NoError(t, err)
	assert.Len(t, claimed, 1, "the PodCliqueSet should converge to a single claimed replica index")
	require.NoError(t, tc.WaitForPods(2))
}

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

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/kwok"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	updateutils "github.com/ai-dynamo/grove/operator/e2e/tests/update/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	beforeAnchor := updateutils.RequireSingleEntryByRole(t, beforeEntries, grovev1beta1.PodGangEntryRoleAnchor)
	assertPodGangEntryPCSGIndices(t, beforeAnchor, "sg-x", []int32{0})
	beforeTail := updateutils.RequireSingleEntryByRole(t, beforeEntries, grovev1beta1.PodGangEntryRoleTail)
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
	require.NoError(t, updateutils.WaitForReplicaRecreationComplete(&tcLongTimeout, 1))

	tests.Logger.Info("7. Assert the applied scheduling hash advanced and the PodGangMap was reshaped")
	afterHash, err := getPCSAppliedSchedulingHash(tc)
	require.NoError(t, err)
	assert.NotEqual(t, beforeHash, afterHash, "applied scheduling hash should advance after recreation")

	currentHash := getPCSGenerationHash(t, tc)
	afterEntries := getPodGangMapEntries(t, tc, 0)
	afterAnchor := updateutils.RequireSingleEntryByRole(t, afterEntries, grovev1beta1.PodGangEntryRoleAnchor)
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
	beforeAnchor := updateutils.RequireSingleEntryByRole(t, getPodGangMapEntries(t, tc, 0), grovev1beta1.PodGangEntryRoleAnchor)
	updateutils.AssertStandalonePCLQPodCounts(t, beforeAnchor, map[string]int32{"pc-a": 2})

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
	require.NoError(t, updateutils.WaitForReplicaRecreationComplete(&tcLongTimeout, 1))

	tests.Logger.Info("7. Assert the applied scheduling hash advanced and the recreated PodClique records the new MinAvailable")
	afterHash, err := getPCSAppliedSchedulingHash(tc)
	require.NoError(t, err)
	assert.NotEqual(t, beforeHash, afterHash, "applied scheduling hash should advance after recreation")

	currentHash := getPCSGenerationHash(t, tc)
	afterAnchor := updateutils.RequireSingleEntryByRole(t, getPodGangMapEntries(t, tc, 0), grovev1beta1.PodGangEntryRoleAnchor)
	updateutils.AssertStandalonePCLQPodCounts(t, afterAnchor, map[string]int32{"pc-a": 2})
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

	claimed, err := updateutils.GetClaimedPCSReplicaIndices(tc)
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
	require.NoError(t, updateutils.WaitForStandaloneReplicaUnavailable(tc, "worker", 1))

	if err := tc.ScalePCS(tc.Workload.Name, 2); err != nil {
		t.Fatalf("failed to scale PodCliqueSet to 2 Replicas: %v", err)
	}
	require.NoError(t, updateutils.WaitForClaimedPCSReplicaIndices(tc, []int{0, 2}))

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
	require.NoError(t, updateutils.WaitForReplicaRecreationComplete(&tcLongTimeout, 2))

	afterHash, err := getPCSAppliedSchedulingHash(tc)
	require.NoError(t, err)
	assert.NotEqual(t, beforeHash, afterHash, "applied scheduling hash should advance after recreation")
	require.NoError(t, updateutils.WaitForClaimedPCSReplicaIndices(&tcLongTimeout, []int{0, 1}))
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
	require.NoError(t, updateutils.WaitForStandaloneReplicaUnavailable(tc, "worker", 1))
	if err := tc.ScalePCS(tc.Workload.Name, 2); err != nil {
		t.Fatalf("failed to scale PodCliqueSet to 2 Replicas: %v", err)
	}
	require.NoError(t, updateutils.WaitForClaimedPCSReplicaIndices(tc, []int{0, 2}))

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
	require.NoError(t, updateutils.WaitForReplicaRecreationComplete(&tcLongTimeout, 1))

	afterHash, err := getPCSAppliedSchedulingHash(tc)
	require.NoError(t, err)
	assert.NotEqual(t, beforeHash, afterHash, "applied scheduling hash should advance after recreation")
	claimed, err := updateutils.GetClaimedPCSReplicaIndices(tc)
	require.NoError(t, err)
	assert.Len(t, claimed, 1, "the PodCliqueSet should converge to a single claimed replica index")
	require.NoError(t, tc.WaitForPods(2))
}

// Test_RR5_SchedulingChangeSupersedesActiveRecreation verifies that a second scheduling change during
// replica recreation preserves the active replacement and converges to the latest configuration.
//
// Scenario:
//  1. Deploy workload2 and verify the baseline PGM: pc-a has two anchor pods; sg-x replica 0 is on the
//     anchor and replica 1 is on the tail. An empty scale-out entry depends on the anchor.
//  2. Block new standalone pods on replica 0 and change pc-a MinAvailable 1 -> 2 (S1).
//  3. Wait for source cleanup and replacement children to exist. Verify the active 0->0 replacement and
//     the same PGM composition, now at S1's generation.
//  4. Change sg-x MinAvailable 1 -> 2 (S2) while readiness remains blocked. Watch PCS status to verify
//     supersession preserves the replacement mapping and clears the cleanup marker, with no cancellation
//     or premature completion.
//  5. Wait for S1 cleanup to complete again and verify S2 recreates the PCLQ, PCSG, and PGM resources.
//     The PGM anchor must hold pc-a's two pods and both sg-x replicas, with no tail and an empty scale-out entry.
//  6. Release readiness and verify convergence to S2.
func Test_RR5_SchedulingChangeSupersedesActiveRecreation(t *testing.T) {
	tests.Logger.Info("1. Deploy workload2 and verify the S0 anchor, tail, and scale-out entries")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload2",
		workloadYAML: "../../yaml/workload2.yaml",
		workerNodes:  10,
		expectedPods: 10,
	})
	defer cleanup()

	baseline := updateutils.RequireReplicaSnapshot(t, tc)
	wantS0 := updateutils.ExpectedReplicaConfiguration{
		StandaloneMinAvailable: 1,
		PCSGMinAvailable:       1,
		PCSGReplicas:           2,
		PodGangMap: updateutils.ExpectedReplicaPodGangMap{
			GenerationHash:      *baseline.PCS.Status.CurrentGenerationHash,
			StandalonePodCounts: map[string]int32{"pc-a": 2},
			PCSGName:            "sg-x",
			AnchorIndices:       []int32{0},
			TailIndices:         []int32{1},
		},
	}
	updateutils.AssertReplicaConfiguration(t, baseline, wantS0)

	tests.Logger.Info("2. Block replica 0 readiness and change pc-a MinAvailable 1 -> 2 (S1)")
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path))
	blockRemoved := false
	defer func() {
		if !blockRemoved {
			if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name); err != nil {
				t.Errorf("failed to delete replica 0 readiness-block KWOK stage: %v", err)
			}
		}
	}()
	require.NoError(t, triggerStandalonePCLQMinAvailableChange(tc, "pc-a", 2))

	tests.Logger.Info("3. Wait for the active 0->0 replacement's source cleanup and S1 child resources")
	firstCleanupPCS, err := updateutils.WaitForSameIndexReplacementCleanup(tc, 0)
	require.NoError(t, err, "S1 source cleanup must complete while the replacement remains active")
	wantS1 := wantS0
	wantS1.StandaloneMinAvailable = 2
	wantS1.PodGangMap.GenerationHash = *firstCleanupPCS.Status.CurrentGenerationHash
	first := updateutils.RequireRecreatedReplica(t, tc, baseline, wantS1)
	updateutils.RequireSchedulingTargetChanged(t, baseline.PCS, first.PCS)

	tests.Logger.Info("4. Watch PCS status while changing sg-x MinAvailable 1 -> 2 (S2)")
	watchCtx, cancelWatch := context.WithTimeout(tc.Ctx, tc.Timeout)
	defer cancelWatch()
	pcsWatcher, err := watchPodCliqueSet(watchCtx, tc)
	require.NoError(t, err)
	defer pcsWatcher.Stop()
	require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 2))
	secondPCS, err := waitForRecreationSupersession(watchCtx, pcsWatcher, first.PCS)
	require.NoError(t, err, "S2 must preserve the active replacement and restart cleanup without completing or applying it")
	secondTarget := updateutils.RequireRecreationSuperseded(t, first.PCS, secondPCS)
	require.NotEqual(t, *baseline.PCS.Status.AppliedSchedulingConfigurationHash, secondTarget.SchedulingHash,
		"S2 must differ from the applied S0 scheduling configuration")

	tests.Logger.Info("5. Wait for S1 cleanup and verify S2 recreates the children and PodGangMap")
	updateutils.RequireCleanupAfterSupersession(t, tc, secondPCS)
	wantS2 := wantS1
	wantS2.PCSGMinAvailable = 2
	wantS2.PodGangMap.GenerationHash = secondTarget.GenerationHash
	wantS2.PodGangMap.AnchorIndices = []int32{0, 1}
	wantS2.PodGangMap.TailIndices = nil
	updateutils.RequireRecreatedReplica(t, tc, first, wantS2)

	tests.Logger.Info("6. Release readiness and verify S2 convergence and the exact PGM composition")
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
	blockRemoved = true
	// KWOK may retain the old delayed action. Recreate only held pods, never the PCLQ or PCSG resources.
	deleteNotReadyPodsOnReplica(t, tc, 0)
	updateutils.RequireReplicaRecreationConverged(t, tc, updateutils.ExpectedReplicaRecreationCompletion{
		GenerationHash:    secondTarget.GenerationHash,
		SchedulingHash:    secondTarget.SchedulingHash,
		Replicas:          1,
		Pods:              10,
		PCSReplicaIndices: []int{0},
		Configuration:     wantS2,
	})
}

// Test_RR6_SchedulingRollbackDuringActiveRecreation verifies that rolling a PCSG MinAvailable change
// back to the applied scheduling configuration does not stop structural recreation. The obsolete target
// is deleted, the cleanup barrier is re-established, and the rollback recreates the original tail entry.
//
// Scenario:
//  1. Deploy workload2 with sg-x MinAvailable 1 (S0). The anchor carries sg-x index 0 and the tail index 1.
//  2. Block replica 0 standalone-pod readiness and increase sg-x MinAvailable to 2 (S1).
//  3. Wait for S0 cleanup and S1 target creation. The S1 anchor holds both sg-x indices and has no tail.
//  4. Roll sg-x MinAvailable back to 1 while S1 remains active. Watch the return to S0's generation and
//     scheduling hashes, with recreation still required and the same-index cleanup marker reset.
//  5. Wait for S1 cleanup and fresh S0 target resources. Verify the restored tail while readiness is blocked.
//  6. Release readiness and verify completion at S0 with no active replica or child updates.
func Test_RR6_SchedulingRollbackDuringActiveRecreation(t *testing.T) {
	tests.Logger.Info("1. Deploy workload2 and capture the S0 resources and scheduling configuration")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload2",
		workloadYAML: "../../yaml/workload2.yaml",
		workerNodes:  10,
		expectedPods: 10,
	})
	defer cleanup()

	baseline := updateutils.RequireReplicaSnapshot(t, tc)
	baselineTarget := updateutils.RecreationTarget{
		GenerationHash: *baseline.PCS.Status.CurrentGenerationHash,
		SchedulingHash: *baseline.PCS.Status.AppliedSchedulingConfigurationHash,
	}
	wantS0 := updateutils.ExpectedReplicaConfiguration{
		StandaloneMinAvailable: 1,
		PCSGMinAvailable:       1,
		PCSGReplicas:           2,
		PodGangMap: updateutils.ExpectedReplicaPodGangMap{
			GenerationHash:      baselineTarget.GenerationHash,
			StandalonePodCounts: map[string]int32{"pc-a": 2},
			PCSGName:            "sg-x",
			AnchorIndices:       []int32{0},
			TailIndices:         []int32{1},
		},
	}
	updateutils.AssertReplicaConfiguration(t, baseline, wantS0)

	tests.Logger.Info("2. Block replica 0 readiness and increase sg-x MinAvailable 1 -> 2 (S1)")
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path))
	blockRemoved := false
	defer func() {
		if !blockRemoved {
			if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name); err != nil {
				t.Errorf("failed to delete replica 0 readiness-block KWOK stage: %v", err)
			}
		}
	}()
	require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 2))

	tests.Logger.Info("3. Wait for S0 cleanup and verify the S1 target has no tail")
	firstCleanupPCS, err := updateutils.WaitForSameIndexReplacementCleanup(tc, 0)
	require.NoError(t, err)
	wantS1 := wantS0
	wantS1.PCSGMinAvailable = 2
	wantS1.PodGangMap.GenerationHash = *firstCleanupPCS.Status.CurrentGenerationHash
	wantS1.PodGangMap.AnchorIndices = []int32{0, 1}
	wantS1.PodGangMap.TailIndices = nil
	first := updateutils.RequireRecreatedReplica(t, tc, baseline, wantS1)
	updateutils.RequireSchedulingTargetChanged(t, baseline.PCS, first.PCS)

	tests.Logger.Info("4. Roll sg-x MinAvailable 2 -> 1 and watch recreation remain active at S0's hashes")
	watchCtx, cancelWatch := context.WithTimeout(tc.Ctx, tc.Timeout)
	defer cancelWatch()
	pcsWatcher, err := watchPodCliqueSet(watchCtx, tc)
	require.NoError(t, err)
	defer pcsWatcher.Stop()
	require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 1))
	rollbackPCS, err := waitForRecreationSupersession(watchCtx, pcsWatcher, first.PCS)
	require.NoError(t, err, "rollback to the applied scheduling hash must preserve recreation and restart cleanup")
	rollbackTarget := updateutils.RequireRecreationSuperseded(t, first.PCS, rollbackPCS)
	require.Equal(t, baselineTarget, rollbackTarget, "rollback must restore both S0 hashes")

	tests.Logger.Info("5. Wait for S1 cleanup and verify fresh rollback resources restore the S0 tail")
	updateutils.RequireCleanupAfterSupersession(t, tc, rollbackPCS)
	updateutils.RequireRecreatedReplica(t, tc, first, wantS0)

	tests.Logger.Info("6. Release readiness and verify the rollback completes at S0")
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
	blockRemoved = true
	// Recreate held pods to discard delayed KWOK actions without changing the recreated root resources.
	deleteNotReadyPodsOnReplica(t, tc, 0)
	updateutils.RequireReplicaRecreationConverged(t, tc, updateutils.ExpectedReplicaRecreationCompletion{
		GenerationHash:    baselineTarget.GenerationHash,
		SchedulingHash:    baselineTarget.SchedulingHash,
		Replicas:          1,
		Pods:              10,
		PCSReplicaIndices: []int{0},
		Configuration:     wantS0,
	})
}

// Test_RR7_MultiplePCSReplicasRecreateSerially verifies that a combined MinAvailable change recreates
// three PCS replicas with only one unfinished replacement at a time. Completed same-index commitments
// remain in CurrentlyUpdating while later replacements run, frozen PodGangMap entries (including empty
// ScaleOut entries) retain their S0 identity, and every replica realizes the new gang shape.
//
// Scenario:
//  1. Deploy workload2 with three ready PCS replicas and capture their child and PodGangMap UIDs.
//  2. Hold fresh standalone pods on replicas 0 and 1, then change pc-a and sg-x MinAvailable 1 -> 2 in one patch.
//  3. Keep a PCS watch open through the entire update, validating serialization, preserved commitments,
//     unchanged targets, and the unapplied scheduling baseline on every status event.
//  4. Verify replica 0 is recreated while replicas 1 and 2 retain their S0 resources and configuration. Release replica 0.
//  5. Verify replica 0's completed mapping and resources remain while replica 1 is recreated. Release replica 1.
//  6. Observe replica 2 and completion on the original watch, then verify all three recreated replicas converge.
func Test_RR7_MultiplePCSReplicasRecreateSerially(t *testing.T) {
	tests.Logger.Info("1. Deploy workload2 with three PCS replicas and capture the S0 resources")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload2", workloadYAML: "../../yaml/workload2.yaml",
		workerNodes: 30, expectedPods: 10, initialPCSReplicas: 3, postScalePods: 30,
	})
	defer cleanup()
	require.NoError(t, updateutils.WaitForClaimedPCSReplicaIndices(tc, []int{0, 1, 2}))
	updateutils.AssertGenerationHashConverged(tc)
	beforeByPCSReplicaIndex := make(map[int]updateutils.ReplicaRecreationSnapshot, 3)
	for pcsReplicaIndex := range 3 {
		before := updateutils.RequireReplicaSnapshotForIndex(t, tc, pcsReplicaIndex)
		beforeByPCSReplicaIndex[pcsReplicaIndex] = before
		updateutils.AssertReplicaConfiguration(t, before, updateutils.ExpectedReplicaConfiguration{
			StandaloneMinAvailable: 1, PCSGMinAvailable: 1, PCSGReplicas: 2,
			PodGangMap: updateutils.ExpectedReplicaPodGangMap{
				GenerationHash:      *before.PCS.Status.CurrentGenerationHash,
				StandalonePodCounts: map[string]int32{"pc-a": 2}, PCSGName: "sg-x", AnchorIndices: []int32{0}, TailIndices: []int32{1},
			},
		})
	}
	before := beforeByPCSReplicaIndex[0]
	observer := updateutils.NewSerialRecreationObserver(3, updateutils.RecreationTarget{
		GenerationHash: *before.PCS.Status.CurrentGenerationHash, SchedulingHash: *before.PCS.Status.AppliedSchedulingConfigurationHash,
	})
	want := updateutils.ExpectedReplicaConfiguration{
		StandaloneMinAvailable: 2, PCSGMinAvailable: 2, PCSGReplicas: 2,
		PodGangMap: updateutils.ExpectedReplicaPodGangMap{StandalonePodCounts: map[string]int32{"pc-a": 2}, PCSGName: "sg-x", AnchorIndices: []int32{0, 1}},
	}

	tests.Logger.Info("2. Hold fresh pods on replicas 0 and 1 and change both MinAvailable values in one PCS patch")
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path))
	defer func() { assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name)) }()
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Path))
	defer func() { assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Name)) }()
	watchCtx, cancelWatch := context.WithTimeout(tc.Ctx, 2*time.Minute)
	defer cancelWatch()
	pcsWatcher, err := watchPodCliqueSet(watchCtx, tc)
	require.NoError(t, err)
	defer pcsWatcher.Stop()
	updateutils.RequireCombinedMinAvailableChange(t, tc, want.StandaloneMinAvailable, want.PCSGMinAvailable)

	tests.Logger.Info("3. Verify replica 0 is recreated while the other replicas retain their resources")
	firstPCS, err := observer.WaitForActivePCSReplica(watchCtx, pcsWatcher, 0)
	require.NoError(t, err)
	target := updateutils.RequireSchedulingTargetChanged(t, before.PCS, firstPCS)
	assert.Equal(t, before.PCS.Generation+1, firstPCS.Generation, "both changes must be one PCS spec mutation")
	want.PodGangMap.GenerationHash = target.GenerationHash
	first := updateutils.RequireSerialReplicaRecreated(t, tc, before, 0, want)
	for _, pcsReplicaIndex := range []int{1, 2} {
		after := updateutils.RequireReplicaSnapshotForIndex(t, tc, pcsReplicaIndex)
		updateutils.AssertReplicaResourceUIDsUnchanged(t, beforeByPCSReplicaIndex[pcsReplicaIndex], after)
		pendingBefore := beforeByPCSReplicaIndex[pcsReplicaIndex]
		assert.Equal(t, pendingBefore.PGM.Spec.Entries, after.PGM.Spec.Entries, "pending PodGangMap must retain all S0 entries, including its empty ScaleOut")
		assert.Equal(t, pendingBefore.PCLQ.Spec, after.PCLQ.Spec, "pending pc-a must retain its complete S0 spec")
		assert.Equal(t, pendingBefore.PCSG.Spec, after.PCSG.Spec, "pending sg-x must retain its complete S0 spec")
		assert.Equal(t, *before.PCS.Status.CurrentGenerationHash, after.PCLQ.Labels[apicommon.LabelPodCliqueSetGenerationHash], "pending pc-a must retain S0 marker")
		assert.Equal(t, *before.PCS.Status.CurrentGenerationHash, after.PCSG.Labels[apicommon.LabelPodCliqueSetGenerationHash], "pending sg-x must retain S0 marker")
		assert.Equal(t, pendingBefore.PCLQ.Labels[apicommon.LabelPodTemplateHash], after.PCLQ.Labels[apicommon.LabelPodTemplateHash], "pending pc-a must retain its running template hash")
	}

	tests.Logger.Info("4. Release replica 0 and verify its completed mapping remains while replica 1 is recreated")
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
	deleteNotReadyPodsOnReplica(t, tc, 0)
	_, err = observer.WaitForActivePCSReplica(watchCtx, pcsWatcher, 1)
	require.NoError(t, err)
	updateutils.RequireSerialReplicaRecreated(t, tc, beforeByPCSReplicaIndex[1], 1, want)
	updateutils.AssertReplicaResourceUIDsUnchanged(t, first, updateutils.RequireReplicaSnapshotForIndex(t, tc, 0))
	pending := updateutils.RequireReplicaSnapshotForIndex(t, tc, 2)
	updateutils.AssertReplicaResourceUIDsUnchanged(t, beforeByPCSReplicaIndex[2], pending)
	pendingBefore := beforeByPCSReplicaIndex[2]
	assert.Equal(t, pendingBefore.PGM.Spec.Entries, pending.PGM.Spec.Entries, "pending PodGangMap must retain all S0 entries, including its empty ScaleOut")
	assert.Equal(t, pendingBefore.PCLQ.Spec, pending.PCLQ.Spec, "pending pc-a must retain its complete S0 spec")
	assert.Equal(t, pendingBefore.PCSG.Spec, pending.PCSG.Spec, "pending sg-x must retain its complete S0 spec")
	assert.Equal(t, *before.PCS.Status.CurrentGenerationHash, pending.PCLQ.Labels[apicommon.LabelPodCliqueSetGenerationHash], "pending pc-a must retain S0 marker")
	assert.Equal(t, *before.PCS.Status.CurrentGenerationHash, pending.PCSG.Labels[apicommon.LabelPodCliqueSetGenerationHash], "pending sg-x must retain S0 marker")
	assert.Equal(t, pendingBefore.PCLQ.Labels[apicommon.LabelPodTemplateHash], pending.PCLQ.Labels[apicommon.LabelPodTemplateHash], "pending pc-a must retain its running template hash")

	tests.Logger.Info("5. Release replica 1 and observe replica 2 and final completion on the original watch")
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Name))
	deleteNotReadyPodsOnReplica(t, tc, 1)
	_, err = observer.WaitForCompletion(watchCtx, pcsWatcher)
	require.NoError(t, err)
	assert.Equal(t, []int32{0, 1, 2}, observer.SelectionOrder(), "all PCS replicas must be selected serially")

	tests.Logger.Info("6. Verify every recreated replica's configuration, gang shape, and convergence")
	updateutils.RequireReplicaRecreationConverged(t, tc, updateutils.ExpectedReplicaRecreationCompletion{
		GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash, Replicas: 3,
		Pods: 30, PCSReplicaIndices: []int{0, 1, 2}, Configuration: want,
	})
	for pcsReplicaIndex := range 3 {
		after := updateutils.RequireReplicaSnapshotForIndex(t, tc, pcsReplicaIndex)
		updateutils.RequireReplicaResourceUIDsChanged(t, beforeByPCSReplicaIndex[pcsReplicaIndex], after)
		updateutils.AssertReplicaConfiguration(t, after, want)
	}
}

// Test_RR8_ReplicaRecreationResumesAfterOperatorRestart verifies recovery on both sides of the persisted
// same-index cleanup barrier. A held source prevents target creation before the first restart; a readiness
// hold keeps fresh targets unavailable across the second restart so their UIDs and cleanup marker must survive.
func Test_RR8_ReplicaRecreationResumesAfterOperatorRestart(t *testing.T) {
	tests.Logger.Info("1. Deploy workload2 and hold replica 0 source cleanup and target readiness")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload2", workloadYAML: "../../yaml/workload2.yaml", workerNodes: 10, expectedPods: 10,
	})
	defer cleanup()
	before := updateutils.RequireReplicaSnapshot(t, tc)
	releaseSource, err := updateutils.HoldReplicaSourceCleanup(tc, before.PCLQ)
	require.NoError(t, err)
	defer func() { assert.NoError(t, releaseSource()) }()
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path))
	defer func() { assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name)) }()

	tests.Logger.Info("2. Increase sg-x MinAvailable 1 -> 2 and wait for blocked source deletion")
	require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 2))
	require.NoError(t, updateutils.WaitForReplicaSourceDeletion(tc, before.PCLQ))
	blockedPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	target := updateutils.RequireSchedulingTargetChanged(t, before.PCS, blockedPCS)
	blockedReplacement := updateutils.RequireActiveReplacement(t, blockedPCS.Status.UpdateProgress, updateutils.ExpectedReplicaReplacement{
		SourcePCSReplicaIndex: 0, TargetPCSReplicaIndex: 0,
	})
	require.Nil(t, blockedReplacement.Replacement.SourceCleanupCompletedAt, "held source must prevent cleanup completion")

	tests.Logger.Info("3. Restart before cleanup completes and verify the original source remains terminating")
	require.NoError(t, restartOperator(tc))
	resumedPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	updateutils.RequireRecreationResumed(t, blockedPCS, resumedPCS)
	var heldSource grovev1beta1.PodClique
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(before.PCLQ), &heldSource))
	assert.Equal(t, before.PCLQ.UID, heldSource.UID, "restart must not create a target before deleting the source")
	require.NotNil(t, heldSource.DeletionTimestamp)

	tests.Logger.Info("4. Release source cleanup and wait for the persisted barrier and fresh target resources")
	require.NoError(t, releaseSource())
	_, err = updateutils.WaitForSameIndexReplacementCleanup(tc, 0)
	require.NoError(t, err)
	want := updateutils.ExpectedReplicaConfiguration{
		StandaloneMinAvailable: 1, PCSGMinAvailable: 2, PCSGReplicas: 2,
		PodGangMap: updateutils.ExpectedReplicaPodGangMap{
			GenerationHash: target.GenerationHash, StandalonePodCounts: map[string]int32{"pc-a": 2},
			PCSGName: "sg-x", AnchorIndices: []int32{0, 1},
		},
	}
	recreated := updateutils.RequireRecreatedReplica(t, tc, before, want)

	tests.Logger.Info("5. Restart after cleanup and verify the barrier and fresh targets are not reset or deleted")
	require.NoError(t, restartOperator(tc))
	resumed := updateutils.RequireReplicaSnapshot(t, tc)
	updateutils.RequireRecreationResumed(t, recreated.PCS, resumed.PCS)
	updateutils.AssertReplicaResourceUIDsUnchanged(t, recreated, resumed)
	updateutils.AssertReplicaConfiguration(t, resumed, want)
	require.NoError(t, updateutils.WaitForStandaloneReplicaUnavailable(tc, "pc-a", 0))

	tests.Logger.Info("6. Release readiness and verify completion without recreating the targets again")
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
	deleteNotReadyPodsOnReplica(t, tc, 0)
	updateutils.RequireReplicaRecreationConverged(t, tc, updateutils.ExpectedReplicaRecreationCompletion{
		GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash, Replicas: 1,
		Pods: 10, PCSReplicaIndices: []int{0}, Configuration: want,
	})
	updateutils.AssertReplicaResourceUIDsUnchanged(t, recreated, updateutils.RequireReplicaSnapshot(t, tc))
}

// Test_RR9_ScaleOutDuringActiveReplicaRecreation verifies that scale-out adds replicas directly at the
// active scheduling target without changing the held replacement or later recreating the added replicas.
func Test_RR9_ScaleOutDuringActiveReplicaRecreation(t *testing.T) {
	tests.Logger.Info("1. Deploy workload2 and hold replica 0 while changing sg-x MinAvailable 1 -> 2")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload2", workloadYAML: "../../yaml/workload2.yaml", workerNodes: 30, expectedPods: 10,
	})
	defer cleanup()
	before := updateutils.RequireReplicaSnapshot(t, tc)
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path))
	defer func() { assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name)) }()
	require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 2))
	firstPCS, err := updateutils.WaitForSameIndexReplacementCleanup(tc, 0)
	require.NoError(t, err)
	target := updateutils.RequireSchedulingTargetChanged(t, before.PCS, firstPCS)
	want := updateutils.ExpectedReplicaConfiguration{
		StandaloneMinAvailable: 1, PCSGMinAvailable: 2, PCSGReplicas: 2,
		PodGangMap: updateutils.ExpectedReplicaPodGangMap{
			GenerationHash: target.GenerationHash, StandalonePodCounts: map[string]int32{"pc-a": 2}, PCSGName: "sg-x", AnchorIndices: []int32{0, 1},
		},
	}
	first := updateutils.RequireRecreatedReplica(t, tc, before, want)

	tests.Logger.Info("2. Watch PCS status and scale from one to three replicas while replacement 0 remains unavailable")
	watchCtx, cancelWatch := context.WithTimeout(tc.Ctx, tc.Timeout)
	defer cancelWatch()
	pcsWatcher, err := watchPodCliqueSet(watchCtx, tc)
	require.NoError(t, err)
	defer pcsWatcher.Stop()
	require.NoError(t, tc.ScalePCS(tc.Workload.Name, 3))
	_, err = updateutils.WaitForRecreationReplicaCount(watchCtx, pcsWatcher, first.PCS, 3)
	require.NoError(t, err)
	require.NoError(t, updateutils.WaitForClaimedPCSReplicaIndices(tc, []int{0, 1, 2}))

	tests.Logger.Info("3. Verify added replicas are Ready at the current target while replica 0 remains active and unapplied")
	addedByPCSReplicaIndex := make(map[int]updateutils.ReplicaRecreationSnapshot, 2)
	for _, pcsReplicaIndex := range []int{1, 2} {
		require.NoError(t, updateutils.WaitForReplicaReadyPods(tc, pcsReplicaIndex, 10))
		added := updateutils.RequireReplicaSnapshotForIndex(t, tc, pcsReplicaIndex)
		updateutils.AssertReplicaConfiguration(t, added, want)
		addedByPCSReplicaIndex[pcsReplicaIndex] = added
	}
	held := updateutils.RequireReplicaSnapshot(t, tc)
	require.NoError(t, updateutils.ValidateRecreationWhileBlocked(first.PCS, held.PCS))
	assert.Equal(t, first.PCS.Status.CurrentGenerationHash, held.PCS.Status.CurrentGenerationHash)
	assert.Equal(t, target.SchedulingHash, held.PCS.Status.UpdateProgress.TargetSchedulingConfigurationHash)
	assert.Equal(t, first.PCS.Status.UpdateProgress.UpdateStartedAt, held.PCS.Status.UpdateProgress.UpdateStartedAt)
	updateutils.AssertReplicaResourceUIDsUnchanged(t, first, held)
	require.NoError(t, updateutils.WaitForStandaloneReplicaUnavailable(tc, "pc-a", 0))

	tests.Logger.Info("4. Release replica 0 and verify convergence without recreating the scale-out replicas")
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
	deleteNotReadyPodsOnReplica(t, tc, 0)
	updateutils.RequireReplicaRecreationConverged(t, tc, updateutils.ExpectedReplicaRecreationCompletion{
		GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash, Replicas: 3,
		Pods: 30, PCSReplicaIndices: []int{0, 1, 2}, Configuration: want,
	})
	updateutils.AssertReplicaResourceUIDsUnchanged(t, first, updateutils.RequireReplicaSnapshot(t, tc))
	for _, pcsReplicaIndex := range []int{1, 2} {
		after := updateutils.RequireReplicaSnapshotForIndex(t, tc, pcsReplicaIndex)
		updateutils.AssertReplicaResourceUIDsUnchanged(t, addedByPCSReplicaIndex[pcsReplicaIndex], after)
		updateutils.AssertReplicaConfiguration(t, after, want)
	}
}

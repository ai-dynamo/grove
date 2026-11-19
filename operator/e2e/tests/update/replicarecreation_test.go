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
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/k8sclient"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/kwok"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	updateutils "github.com/ai-dynamo/grove/operator/e2e/tests/update/utils"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	kubeutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	watchtools "k8s.io/client-go/tools/watch"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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
	beforeEntries := updateutils.PodGangMapEntries(t, tc, 0)
	beforeAnchor := updateutils.RequireSingleEntryByRole(t, beforeEntries, grovev1beta1.PodGangEntryRoleAnchor)
	updateutils.AssertPodGangEntryPCSGIndices(t, beforeAnchor, "sg-x", []int32{0})
	beforeTail := updateutils.RequireSingleEntryByRole(t, beforeEntries, grovev1beta1.PodGangEntryRoleTail)
	updateutils.AssertPodGangEntryPCSGIndices(t, beforeTail, "sg-x", []int32{1})

	beforeHash, err := pcsAppliedSchedulingHash(tc)
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
	afterHash, err := pcsAppliedSchedulingHash(tc)
	require.NoError(t, err)
	assert.NotEqual(t, beforeHash, afterHash, "applied scheduling hash should advance after recreation")

	currentHash := updateutils.PCSGenerationHash(t, tc)
	afterEntries := updateutils.PodGangMapEntries(t, tc, 0)
	afterAnchor := updateutils.RequireSingleEntryByRole(t, afterEntries, grovev1beta1.PodGangEntryRoleAnchor)
	updateutils.AssertPodGangEntryPCSGIndices(t, afterAnchor, "sg-x", []int32{0, 1})
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
	beforeHash, err := pcsAppliedSchedulingHash(tc)
	require.NoError(t, err)
	require.NotEmpty(t, beforeHash)
	beforeAnchor := updateutils.RequireSingleEntryByRole(t, updateutils.PodGangMapEntries(t, tc, 0), grovev1beta1.PodGangEntryRoleAnchor)
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
	require.NoError(t, waitForReplicaRecreationComplete(&tcLongTimeout, 1))

	tests.Logger.Info("7. Assert the applied scheduling hash advanced and the recreated PodClique records the new MinAvailable")
	afterHash, err := pcsAppliedSchedulingHash(tc)
	require.NoError(t, err)
	assert.NotEqual(t, beforeHash, afterHash, "applied scheduling hash should advance after recreation")

	currentHash := updateutils.PCSGenerationHash(t, tc)
	afterAnchor := updateutils.RequireSingleEntryByRole(t, updateutils.PodGangMapEntries(t, tc, 0), grovev1beta1.PodGangEntryRoleAnchor)
	updateutils.AssertStandalonePCLQPodCounts(t, afterAnchor, map[string]int32{"pc-a": 2})
	for _, entry := range updateutils.PodGangMapEntries(t, tc, 0) {
		assert.Equalf(t, currentHash, entry.PodCliqueSetGenerationHash,
			"PodGangMap entry (role %s, epoch %s) should be at the current generation hash", entry.Role, entry.Epoch)
	}

	minAvailable, err := standalonePCLQMinAvailable(tc, "pc-a")
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

	claimed, err := claimedPCSReplicaIndices(tc)
	require.NoError(t, err)
	require.Equal(t, []int{0, 1, 2}, claimed)

	beforeHash, err := pcsAppliedSchedulingHash(tc)
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
		t.Fatalf("failed to scale PodCliqueSet to 2 Replicas: %v", err)
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

	afterHash, err := pcsAppliedSchedulingHash(tc)
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

	beforeHash, err := pcsAppliedSchedulingHash(tc)
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
		t.Fatalf("failed to scale PodCliqueSet to 2 Replicas: %v", err)
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

	afterHash, err := pcsAppliedSchedulingHash(tc)
	require.NoError(t, err)
	assert.NotEqual(t, beforeHash, afterHash, "applied scheduling hash should advance after recreation")
	claimed, err := claimedPCSReplicaIndices(tc)
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

	baseline := requireReplicaSnapshot(t, tc)
	wantS0 := expectedReplicaConfiguration{
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
	assertReplicaConfiguration(t, baseline, wantS0)

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
	firstCleanupPCS, err := waitForSameIndexReplacementCleanup(tc, 0)
	require.NoError(t, err, "S1 source cleanup must complete while the replacement remains active")
	wantS1 := wantS0
	wantS1.StandaloneMinAvailable = 2
	wantS1.PodGangMap.GenerationHash = *firstCleanupPCS.Status.CurrentGenerationHash
	first := requireRecreatedReplica(t, tc, baseline, wantS1)
	requireSchedulingTargetChanged(t, baseline.PCS, first.PCS)

	tests.Logger.Info("4. Watch PCS status while changing sg-x MinAvailable 1 -> 2 (S2)")
	watchCtx, cancelWatch := context.WithTimeout(tc.Ctx, tc.Timeout)
	defer cancelWatch()
	pcsWatcher, err := watchPodCliqueSet(watchCtx, tc)
	require.NoError(t, err)
	defer pcsWatcher.Stop()
	require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 2))
	secondPCS, err := waitForRecreationSupersession(watchCtx, pcsWatcher, first.PCS)
	require.NoError(t, err, "S2 must preserve the active replacement and restart cleanup without completing or applying it")
	secondTarget := requireRecreationSuperseded(t, first.PCS, secondPCS)
	require.NotEqual(t, *baseline.PCS.Status.AppliedSchedulingConfigurationHash, secondTarget.SchedulingHash,
		"S2 must differ from the applied S0 scheduling configuration")

	tests.Logger.Info("5. Wait for S1 cleanup and verify S2 recreates the children and PodGangMap")
	requireCleanupAfterSupersession(t, tc, secondPCS)
	wantS2 := wantS1
	wantS2.PCSGMinAvailable = 2
	wantS2.PodGangMap.GenerationHash = secondTarget.GenerationHash
	wantS2.PodGangMap.AnchorIndices = []int32{0, 1}
	wantS2.PodGangMap.TailIndices = nil
	requireRecreatedReplica(t, tc, first, wantS2)

	tests.Logger.Info("6. Release readiness and verify S2 convergence and the exact PGM composition")
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
	blockRemoved = true
	// KWOK may retain the old delayed action. Recreate only held pods, never the PCLQ or PCSG resources.
	updateutils.DeleteNotReadyPodsOnReplica(t, tc, 0)
	requireReplicaRecreationConverged(t, tc, expectedReplicaRecreationCompletion{
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

	baseline := requireReplicaSnapshot(t, tc)
	baselineTarget := recreationTarget{
		GenerationHash: *baseline.PCS.Status.CurrentGenerationHash,
		SchedulingHash: *baseline.PCS.Status.AppliedSchedulingConfigurationHash,
	}
	wantS0 := expectedReplicaConfiguration{
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
	assertReplicaConfiguration(t, baseline, wantS0)

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
	firstCleanupPCS, err := waitForSameIndexReplacementCleanup(tc, 0)
	require.NoError(t, err)
	wantS1 := wantS0
	wantS1.PCSGMinAvailable = 2
	wantS1.PodGangMap.GenerationHash = *firstCleanupPCS.Status.CurrentGenerationHash
	wantS1.PodGangMap.AnchorIndices = []int32{0, 1}
	wantS1.PodGangMap.TailIndices = nil
	first := requireRecreatedReplica(t, tc, baseline, wantS1)
	requireSchedulingTargetChanged(t, baseline.PCS, first.PCS)

	tests.Logger.Info("4. Roll sg-x MinAvailable 2 -> 1 and watch recreation remain active at S0's hashes")
	watchCtx, cancelWatch := context.WithTimeout(tc.Ctx, tc.Timeout)
	defer cancelWatch()
	pcsWatcher, err := watchPodCliqueSet(watchCtx, tc)
	require.NoError(t, err)
	defer pcsWatcher.Stop()
	require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 1))
	rollbackPCS, err := waitForRecreationSupersession(watchCtx, pcsWatcher, first.PCS)
	require.NoError(t, err, "rollback to the applied scheduling hash must preserve recreation and restart cleanup")
	rollbackTarget := requireRecreationSuperseded(t, first.PCS, rollbackPCS)
	require.Equal(t, baselineTarget, rollbackTarget, "rollback must restore both S0 hashes")

	tests.Logger.Info("5. Wait for S1 cleanup and verify fresh rollback resources restore the S0 tail")
	requireCleanupAfterSupersession(t, tc, rollbackPCS)
	requireRecreatedReplica(t, tc, first, wantS0)

	tests.Logger.Info("6. Release readiness and verify the rollback completes at S0")
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
	blockRemoved = true
	// Recreate held pods to discard delayed KWOK actions without changing the recreated root resources.
	updateutils.DeleteNotReadyPodsOnReplica(t, tc, 0)
	requireReplicaRecreationConverged(t, tc, expectedReplicaRecreationCompletion{
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
	require.NoError(t, waitForClaimedPCSReplicaIndices(tc, []int{0, 1, 2}))
	updateutils.AssertGenerationHashConverged(tc)
	beforeByPCSReplicaIndex := make(map[int]replicaRecreationSnapshot, 3)
	for pcsReplicaIndex := range 3 {
		before := requireReplicaSnapshotForIndex(t, tc, pcsReplicaIndex)
		beforeByPCSReplicaIndex[pcsReplicaIndex] = before
		assertReplicaConfiguration(t, before, expectedReplicaConfiguration{
			StandaloneMinAvailable: 1, PCSGMinAvailable: 1, PCSGReplicas: 2,
			PodGangMap: updateutils.ExpectedReplicaPodGangMap{
				GenerationHash:      *before.PCS.Status.CurrentGenerationHash,
				StandalonePodCounts: map[string]int32{"pc-a": 2}, PCSGName: "sg-x", AnchorIndices: []int32{0}, TailIndices: []int32{1},
			},
		})
	}
	before := beforeByPCSReplicaIndex[0]
	observer := newSerialRecreationObserver(3, recreationTarget{
		GenerationHash: *before.PCS.Status.CurrentGenerationHash, SchedulingHash: *before.PCS.Status.AppliedSchedulingConfigurationHash,
	})
	want := expectedReplicaConfiguration{
		StandaloneMinAvailable: 2, PCSGMinAvailable: 2, PCSGReplicas: 2,
		PodGangMap: updateutils.ExpectedReplicaPodGangMap{StandalonePodCounts: map[string]int32{"pc-a": 2}, PCSGName: "sg-x", AnchorIndices: []int32{0, 1}},
	}

	tests.Logger.Info("2. Hold fresh pods on replicas 0 and 1 and change both MinAvailable values in one PCS patch")
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path))
	defer func() {
		assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
	}()
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Path))
	defer func() {
		assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Name))
	}()
	watchCtx, cancelWatch := context.WithTimeout(tc.Ctx, 2*time.Minute)
	defer cancelWatch()
	pcsWatcher, err := watchPodCliqueSet(watchCtx, tc)
	require.NoError(t, err)
	defer pcsWatcher.Stop()
	requireCombinedMinAvailableChange(t, tc, want.StandaloneMinAvailable, want.PCSGMinAvailable)

	tests.Logger.Info("3. Verify replica 0 is recreated while the other replicas retain their resources")
	firstPCS, err := observer.waitForActivePCSReplica(watchCtx, pcsWatcher, 0)
	require.NoError(t, err)
	target := requireSchedulingTargetChanged(t, before.PCS, firstPCS)
	assert.Equal(t, before.PCS.Generation+1, firstPCS.Generation, "both changes must be one PCS spec mutation")
	want.PodGangMap.GenerationHash = target.GenerationHash
	first := requireSerialReplicaRecreated(t, tc, before, 0, want)
	for _, pcsReplicaIndex := range []int{1, 2} {
		after := requireReplicaSnapshotForIndex(t, tc, pcsReplicaIndex)
		assertReplicaResourceUIDsUnchanged(t, beforeByPCSReplicaIndex[pcsReplicaIndex], after)
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
	updateutils.DeleteNotReadyPodsOnReplica(t, tc, 0)
	_, err = observer.waitForActivePCSReplica(watchCtx, pcsWatcher, 1)
	require.NoError(t, err)
	requireSerialReplicaRecreated(t, tc, beforeByPCSReplicaIndex[1], 1, want)
	assertReplicaResourceUIDsUnchanged(t, first, requireReplicaSnapshotForIndex(t, tc, 0))
	pending := requireReplicaSnapshotForIndex(t, tc, 2)
	assertReplicaResourceUIDsUnchanged(t, beforeByPCSReplicaIndex[2], pending)
	pendingBefore := beforeByPCSReplicaIndex[2]
	assert.Equal(t, pendingBefore.PGM.Spec.Entries, pending.PGM.Spec.Entries, "pending PodGangMap must retain all S0 entries, including its empty ScaleOut")
	assert.Equal(t, pendingBefore.PCLQ.Spec, pending.PCLQ.Spec, "pending pc-a must retain its complete S0 spec")
	assert.Equal(t, pendingBefore.PCSG.Spec, pending.PCSG.Spec, "pending sg-x must retain its complete S0 spec")
	assert.Equal(t, *before.PCS.Status.CurrentGenerationHash, pending.PCLQ.Labels[apicommon.LabelPodCliqueSetGenerationHash], "pending pc-a must retain S0 marker")
	assert.Equal(t, *before.PCS.Status.CurrentGenerationHash, pending.PCSG.Labels[apicommon.LabelPodCliqueSetGenerationHash], "pending sg-x must retain S0 marker")
	assert.Equal(t, pendingBefore.PCLQ.Labels[apicommon.LabelPodTemplateHash], pending.PCLQ.Labels[apicommon.LabelPodTemplateHash], "pending pc-a must retain its running template hash")

	tests.Logger.Info("5. Release replica 1 and observe replica 2 and final completion on the original watch")
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Name))
	updateutils.DeleteNotReadyPodsOnReplica(t, tc, 1)
	_, err = observer.waitForCompletion(watchCtx, pcsWatcher)
	require.NoError(t, err)
	assert.Equal(t, []int32{0, 1, 2}, observer.observedSelectionOrder(), "all PCS replicas must be selected serially")

	tests.Logger.Info("6. Verify every recreated replica's configuration, gang shape, and convergence")
	requireReplicaRecreationConverged(t, tc, expectedReplicaRecreationCompletion{
		GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash, Replicas: 3,
		Pods: 30, PCSReplicaIndices: []int{0, 1, 2}, Configuration: want,
	})
	for pcsReplicaIndex := range 3 {
		after := requireReplicaSnapshotForIndex(t, tc, pcsReplicaIndex)
		requireReplicaResourceUIDsChanged(t, beforeByPCSReplicaIndex[pcsReplicaIndex], after)
		assertReplicaConfiguration(t, after, want)
	}
}

// Test_RR8_ReplicaRecreationResumesAfterOperatorRestart verifies recovery before and after source cleanup
// during same-index replica recreation. A test finalizer prevents source cleanup from finishing before the
// first restart. A KWOK stage keeps replacement standalone pods NotReady across the second restart, so
// the replacement resources and persisted cleanup marker must survive without restarting recreation.
//
// Scenario:
//  1. Deploy workload2 and capture replica 0's S0 resources. Add a test finalizer to its source standalone
//     PodClique and apply a KWOK stage that blocks readiness of replacement standalone pods.
//  2. Increase sg-x MinAvailable 1 -> 2 and wait for the source PodClique to become terminating.
//     Verify the replacement targets index 0 and source cleanup is not yet marked complete.
//  3. Restart the operator before cleanup completes. Verify the source retains its UID and remains
//     terminating, and the replacement mapping, scheduling target, and update start time remain unchanged.
//  4. Remove the test finalizer and wait for the persisted cleanup marker and new replacement resources.
//     Keep replacement standalone pods NotReady so recreation cannot finish.
//  5. Restart the operator again. Verify the replacement resources retain their UIDs and configuration,
//     the cleanup marker remains unchanged, and the scheduling target is still unapplied.
//  6. Remove the KWOK stage and delete the NotReady pods so fresh pods can become Ready. Verify recreation
//     completes at the original target without recreating the replacement resources again.
func Test_RR8_ReplicaRecreationResumesAfterOperatorRestart(t *testing.T) {
	tests.Logger.Info("1. Deploy workload2 and hold replica 0 source cleanup and target readiness")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload2", workloadYAML: "../../yaml/workload2.yaml", workerNodes: 10, expectedPods: 10,
	})
	defer cleanup()
	before := requireReplicaSnapshot(t, tc)
	releaseSource, err := holdReplicaSourceCleanup(tc, before.PCLQ)
	require.NoError(t, err)
	defer func() { assert.NoError(t, releaseSource()) }()
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path))
	defer func() { assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name)) }()

	tests.Logger.Info("2. Increase sg-x MinAvailable 1 -> 2 and wait for blocked source deletion")
	require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 2))
	require.NoError(t, waitForReplicaSourceDeletion(tc, before.PCLQ))
	blockedPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	target := requireSchedulingTargetChanged(t, before.PCS, blockedPCS)
	blockedReplacement := requireActiveReplacement(t, blockedPCS.Status.UpdateProgress, expectedReplicaReplacement{
		SourcePCSReplicaIndex: 0, TargetPCSReplicaIndex: 0,
	})
	require.Nil(t, blockedReplacement.Replacement.SourceCleanupCompletedAt, "held source must prevent cleanup completion")

	tests.Logger.Info("3. Restart before cleanup completes and verify the original source remains terminating")
	require.NoError(t, updateutils.RestartOperator(tc))
	resumedPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	requireRecreationResumed(t, blockedPCS, resumedPCS)
	var heldSource grovev1beta1.PodClique
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(before.PCLQ), &heldSource))
	assert.Equal(t, before.PCLQ.UID, heldSource.UID, "restart must not create a target before deleting the source")
	require.NotNil(t, heldSource.DeletionTimestamp)

	tests.Logger.Info("4. Release source cleanup and wait for the persisted barrier and fresh target resources")
	require.NoError(t, releaseSource())
	_, err = waitForSameIndexReplacementCleanup(tc, 0)
	require.NoError(t, err)
	want := expectedReplicaConfiguration{
		StandaloneMinAvailable: 1, PCSGMinAvailable: 2, PCSGReplicas: 2,
		PodGangMap: updateutils.ExpectedReplicaPodGangMap{
			GenerationHash: target.GenerationHash, StandalonePodCounts: map[string]int32{"pc-a": 2},
			PCSGName: "sg-x", AnchorIndices: []int32{0, 1},
		},
	}
	recreated := requireRecreatedReplica(t, tc, before, want)

	tests.Logger.Info("5. Restart after cleanup and verify the barrier and fresh targets are not reset or deleted")
	require.NoError(t, updateutils.RestartOperator(tc))
	resumed := requireReplicaSnapshot(t, tc)
	requireRecreationResumed(t, recreated.PCS, resumed.PCS)
	assertReplicaResourceUIDsUnchanged(t, recreated, resumed)
	assertReplicaConfiguration(t, resumed, want)
	require.NoError(t, waitForStandaloneReplicaUnavailable(tc, "pc-a", 0))

	tests.Logger.Info("6. Release readiness and verify completion without recreating the targets again")
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
	updateutils.DeleteNotReadyPodsOnReplica(t, tc, 0)
	requireReplicaRecreationConverged(t, tc, expectedReplicaRecreationCompletion{
		GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash, Replicas: 1,
		Pods: 10, PCSReplicaIndices: []int{0}, Configuration: want,
	})
	assertReplicaResourceUIDsUnchanged(t, recreated, requireReplicaSnapshot(t, tc))
}

// Test_RR9_ScaleOutDuringActiveReplicaRecreation verifies that scale-out adds replicas directly at the
// active scheduling target while replica 0's replacement remains unavailable. Scale-out must preserve
// that replacement's resources and cleanup marker, and the added replicas must not be recreated later.
//
// Scenario:
//  1. Deploy workload2 and capture replica 0's S0 resources and scheduling baseline. Block readiness of
//     replacement standalone pods with a KWOK stage, increase sg-x MinAvailable 1 -> 2, and wait for
//     source cleanup and the new same-index replacement resources.
//  2. Start a PCS watch and scale 1 -> 3 while replacement 0 remains unavailable. Verify the scale change
//     preserves the replacement mapping, cleanup marker, update start time, and scheduling target.
//  3. Wait for replicas 1 and 2 to become Ready at the active target and capture their resource UIDs.
//     Verify replica 0 remains unavailable with its original replacement resources and the target is unapplied.
//  4. Remove the KWOK stage and delete replica 0's NotReady pods so fresh pods can become Ready.
//     Verify all three replicas converge at the original target without recreating their resources again.
func Test_RR9_ScaleOutDuringActiveReplicaRecreation(t *testing.T) {
	tests.Logger.Info("1. Deploy workload2 and hold replica 0 while changing sg-x MinAvailable 1 -> 2")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload2", workloadYAML: "../../yaml/workload2.yaml", workerNodes: 30, expectedPods: 10,
	})
	defer cleanup()
	before := requireReplicaSnapshot(t, tc)
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path))
	defer func() { assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name)) }()
	require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 2))
	firstPCS, err := waitForSameIndexReplacementCleanup(tc, 0)
	require.NoError(t, err)
	target := requireSchedulingTargetChanged(t, before.PCS, firstPCS)
	want := expectedReplicaConfiguration{
		StandaloneMinAvailable: 1, PCSGMinAvailable: 2, PCSGReplicas: 2,
		PodGangMap: updateutils.ExpectedReplicaPodGangMap{
			GenerationHash: target.GenerationHash, StandalonePodCounts: map[string]int32{"pc-a": 2}, PCSGName: "sg-x", AnchorIndices: []int32{0, 1},
		},
	}
	first := requireRecreatedReplica(t, tc, before, want)

	tests.Logger.Info("2. Watch PCS status and scale from one to three replicas while replacement 0 remains unavailable")
	watchCtx, cancelWatch := context.WithTimeout(tc.Ctx, tc.Timeout)
	defer cancelWatch()
	pcsWatcher, err := watchPodCliqueSet(watchCtx, tc)
	require.NoError(t, err)
	defer pcsWatcher.Stop()
	require.NoError(t, tc.ScalePCS(tc.Workload.Name, 3))
	_, err = waitForRecreationReplicaCount(watchCtx, pcsWatcher, first.PCS, 3)
	require.NoError(t, err)
	require.NoError(t, waitForClaimedPCSReplicaIndices(tc, []int{0, 1, 2}))

	tests.Logger.Info("3. Verify added replicas are Ready at the current target while replica 0 remains active and unapplied")
	addedByPCSReplicaIndex := make(map[int]replicaRecreationSnapshot, 2)
	for _, pcsReplicaIndex := range []int{1, 2} {
		require.NoError(t, waitForReplicaReadyPods(tc, pcsReplicaIndex, 10))
		added := requireReplicaSnapshotForIndex(t, tc, pcsReplicaIndex)
		assertReplicaConfiguration(t, added, want)
		addedByPCSReplicaIndex[pcsReplicaIndex] = added
	}
	held := requireReplicaSnapshot(t, tc)
	require.NoError(t, validateRecreationWhileBlocked(first.PCS, held.PCS))
	assert.Equal(t, first.PCS.Status.CurrentGenerationHash, held.PCS.Status.CurrentGenerationHash)
	assert.Equal(t, target.SchedulingHash, held.PCS.Status.UpdateProgress.TargetSchedulingConfigurationHash)
	assert.Equal(t, first.PCS.Status.UpdateProgress.UpdateStartedAt, held.PCS.Status.UpdateProgress.UpdateStartedAt)
	assertReplicaResourceUIDsUnchanged(t, first, held)
	require.NoError(t, waitForStandaloneReplicaUnavailable(tc, "pc-a", 0))

	tests.Logger.Info("4. Release replica 0 and verify convergence without recreating the scale-out replicas")
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
	updateutils.DeleteNotReadyPodsOnReplica(t, tc, 0)
	requireReplicaRecreationConverged(t, tc, expectedReplicaRecreationCompletion{
		GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash, Replicas: 3,
		Pods: 30, PCSReplicaIndices: []int{0, 1, 2}, Configuration: want,
	})
	assertReplicaResourceUIDsUnchanged(t, first, requireReplicaSnapshot(t, tc))
	for _, pcsReplicaIndex := range []int{1, 2} {
		after := requireReplicaSnapshotForIndex(t, tc, pcsReplicaIndex)
		assertReplicaResourceUIDsUnchanged(t, addedByPCSReplicaIndex[pcsReplicaIndex], after)
		assertReplicaConfiguration(t, after, want)
	}
}

// replicaRecreationSnapshot captures the PCS and one replica's pc-a, sg-x, and PodGangMap resources.
type replicaRecreationSnapshot struct {
	PCS  *grovev1beta1.PodCliqueSet
	PCLQ *grovev1beta1.PodClique
	PCSG *grovev1beta1.PodCliqueScalingGroup
	PGM  *grovev1beta1.PodGangMap
}

// expectedReplicaConfiguration describes the child configuration and gang shape realized by a PCS replica.
type expectedReplicaConfiguration struct {
	StandaloneMinAvailable int32
	PCSGMinAvailable       int32
	PCSGReplicas           int32
	PodGangMap             updateutils.ExpectedReplicaPodGangMap
}

// expectedReplicaReplacement describes the source-to-target mapping committed by a replacement.
type expectedReplicaReplacement struct {
	SourcePCSReplicaIndex int32
	TargetPCSReplicaIndex int32
}

// recreationTarget identifies the PCS generation and scheduling configuration of an update target.
type recreationTarget struct {
	GenerationHash string
	SchedulingHash string
}

// expectedReplicaRecreationCompletion describes the final scheduling state, identities, and replica configuration.
type expectedReplicaRecreationCompletion struct {
	GenerationHash    string
	SchedulingHash    string
	Replicas          int32
	Pods              int
	PCSReplicaIndices []int
	Configuration     expectedReplicaConfiguration
}

// serialRecreationObserver records a single scheduling change from a healthy, dense PCS identity set.
// It validates every watched status, not just polled milestones. All access is synchronous in the caller;
// neither clients nor watches are stored in this lifecycle state.
type serialRecreationObserver struct {
	baseline              recreationTarget
	target                recreationTarget
	pcsReplicas           int32
	selectionsBySource    map[int32]*grovev1beta1.PodCliqueSetReplicaUpdateProgress
	selectionOrder        []int32
	activePCSReplicaIndex *int32
	isComplete            bool
}

// pcsAppliedSchedulingHash returns the PodCliqueSet Status.AppliedSchedulingConfigurationHash, or an empty
// string when it is unset.
func pcsAppliedSchedulingHash(tc *testctx.TestContext) (string, error) {
	pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
	if err != nil {
		return "", err
	}
	if pcs.Status.AppliedSchedulingConfigurationHash == nil {
		return "", nil
	}
	return *pcs.Status.AppliedSchedulingConfigurationHash, nil
}

// triggerPCSGMinAvailableChange sets spec.template.podCliqueScalingGroups[pcsgConfigName].minAvailable on the
// PodCliqueSet. A MinAvailable change is a scheduling-configuration change that drives structural PCS replica
// recreation. Uses tc.Workload.Name as the PCS name. It issues a targeted single-field merge patch, which
// applies on top of concurrent status writes without clobbering them, so it needs no optimistic lock.
//
//nolint:unparam // Keep the scheduling component explicit at the mutation call site.
func triggerPCSGMinAvailableChange(tc *testctx.TestContext, pcsgConfigName string, minAvailable int32) error {
	var pcs grovev1beta1.PodCliqueSet
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Name: tc.Workload.Name, Namespace: tc.Namespace}, &pcs); err != nil {
		return fmt.Errorf("failed to get PodCliqueSet: %w", err)
	}
	base := pcs.DeepCopy()
	for i := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if pcs.Spec.Template.PodCliqueScalingGroupConfigs[i].Name == pcsgConfigName {
			pcs.Spec.Template.PodCliqueScalingGroupConfigs[i].MinAvailable = &minAvailable
			return tc.Client.Patch(tc.Ctx, &pcs, client.MergeFrom(base))
		}
	}
	return fmt.Errorf("PodCliqueScalingGroup config %s not found in PodCliqueSet %s", pcsgConfigName, tc.Workload.Name)
}

// waitForActivePCSReplicaRecreation waits until the PodCliqueSet reports an active structural replica
// recreation: UpdateProgress is set with RequiresReplicaRecreation true and UpdateEndedAt nil. The
// readiness-delay KWOK stage keeps the recreation active long enough for this milestone to be observed.
func waitForActivePCSReplicaRecreation(tc *testctx.TestContext) error {
	fetchPCS := waiter.FetchByName(tc.Workload.Name, k8sclient.Getter[*grovev1beta1.PodCliqueSet](tc.Client, tc.Namespace))
	predicate := waiter.Predicate[*grovev1beta1.PodCliqueSet](func(pcs *grovev1beta1.PodCliqueSet) bool {
		if pcs == nil || pcs.Status.UpdateProgress == nil {
			return false
		}
		progress := pcs.Status.UpdateProgress
		return progress.RequiresReplicaRecreation && progress.UpdateEndedAt == nil
	})
	w := waiter.New[*grovev1beta1.PodCliqueSet]().WithTimeout(tc.Timeout).WithInterval(tc.Interval)
	return w.WaitUntil(tc.Ctx, fetchPCS, predicate)
}

// waitForReplicaRecreationComplete waits until structural PCS replica recreation has finished: UpdateProgress
// has UpdateEndedAt set, AppliedSchedulingConfigurationHash equals the target scheduling hash the update
// converged to, and UpdatedReplicas matches expectedReplicas.
func waitForReplicaRecreationComplete(tc *testctx.TestContext, expectedReplicas int32) error {
	fetchPCS := waiter.FetchByName(tc.Workload.Name, k8sclient.Getter[*grovev1beta1.PodCliqueSet](tc.Client, tc.Namespace))
	predicate := waiter.Predicate[*grovev1beta1.PodCliqueSet](func(pcs *grovev1beta1.PodCliqueSet) bool {
		if pcs == nil || pcs.Status.UpdateProgress == nil {
			return false
		}
		progress := pcs.Status.UpdateProgress
		// The end marker is persisted before status reconciliation updates the replica counters.
		if progress.UpdateEndedAt == nil || pcs.Status.UpdatedReplicas != expectedReplicas || pcs.Status.AvailableReplicas != expectedReplicas {
			return false
		}
		return pcs.Status.AppliedSchedulingConfigurationHash != nil &&
			*pcs.Status.AppliedSchedulingConfigurationHash == progress.TargetSchedulingConfigurationHash
	})
	w := waiter.New[*grovev1beta1.PodCliqueSet]().WithTimeout(tc.Timeout).WithInterval(tc.Interval)
	return w.WaitUntil(tc.Ctx, fetchPCS, predicate)
}

// triggerStandalonePCLQMinAvailableChange sets spec.template.cliques[cliqueName].minAvailable on the PodCliqueSet.
// A MinAvailable change is a scheduling-configuration change that drives structural PCS replica recreation. Uses
// tc.Workload.Name as the PCS name. It issues a targeted single-field merge patch, which applies on top of
// concurrent status writes without clobbering them, so it needs no optimistic lock.
//
//nolint:unparam // Keep the scheduling value explicit at the mutation call site.
func triggerStandalonePCLQMinAvailableChange(tc *testctx.TestContext, cliqueName string, minAvailable int32) error {
	var pcs grovev1beta1.PodCliqueSet
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Name: tc.Workload.Name, Namespace: tc.Namespace}, &pcs); err != nil {
		return fmt.Errorf("failed to get PodCliqueSet: %w", err)
	}
	base := pcs.DeepCopy()
	for i := range pcs.Spec.Template.Cliques {
		if pcs.Spec.Template.Cliques[i].Name == cliqueName {
			pcs.Spec.Template.Cliques[i].Spec.MinAvailable = &minAvailable
			return tc.Client.Patch(tc.Ctx, &pcs, client.MergeFrom(base))
		}
	}
	return fmt.Errorf("clique %s not found in PodCliqueSet %s", cliqueName, tc.Workload.Name)
}

// standalonePCLQMinAvailable returns the MinAvailable recorded on the replica 0 standalone PodClique for the
// named clique.
func standalonePCLQMinAvailable(tc *testctx.TestContext, cliqueName string) (int32, error) {
	var pclq grovev1beta1.PodClique
	key := types.NamespacedName{Namespace: tc.Namespace, Name: updateutils.StandaloneCliqueFQN(tc, cliqueName)}
	if err := tc.Client.Get(tc.Ctx, key, &pclq); err != nil {
		return 0, err
	}
	if pclq.Spec.MinAvailable == nil {
		return 0, fmt.Errorf("PodClique %s has no MinAvailable set", key.Name)
	}
	return *pclq.Spec.MinAvailable, nil
}

// claimedPCSReplicaIndices returns the sorted PCS replica indices that currently have a PodGangMap, which
// identifies the replicas whose resources exist.
func claimedPCSReplicaIndices(tc *testctx.TestContext) ([]int, error) {
	pgmList := &grovev1beta1.PodGangMapList{}
	if err := tc.Client.List(tc.Ctx, pgmList, client.InNamespace(tc.Namespace), client.MatchingLabels{apicommon.LabelPartOfKey: tc.Workload.Name}); err != nil {
		return nil, err
	}
	indices := make([]int, 0, len(pgmList.Items))
	for i := range pgmList.Items {
		replicaIndex, err := kubeutils.GetPodCliqueSetReplicaIndex(pgmList.Items[i].ObjectMeta)
		if err != nil {
			return nil, err
		}
		indices = append(indices, replicaIndex)
	}
	slices.Sort(indices)
	return indices, nil
}

const (
	// kwokStageReadyBlockedR1Path is the KWOK stage fixture path.
	kwokStageReadyBlockedR1Path = "../../yaml/kwok/pod-ready-blocked-r1-standalone.yaml"
	// kwokStageReadyBlockedR1Name is the KWOK stage name.
	kwokStageReadyBlockedR1Name = "pod-ready-blocked-r1-standalone"
)

// deletePodsForReplica deletes all pods of the given PCS replica index. The operator recreates them, and a
// readiness-block KWOK stage applied beforehand catches the fresh pods and holds them not-ready, which makes the
// replica unavailable.
func deletePodsForReplica(tc *testctx.TestContext, pcsReplicaIndex int) error {
	return tc.Client.DeleteAllOf(tc.Ctx, &corev1.Pod{},
		client.InNamespace(tc.Namespace),
		client.MatchingLabels{
			apicommon.LabelPartOfKey:              tc.Workload.Name,
			"grove.io/podcliqueset-replica-index": strconv.Itoa(pcsReplicaIndex),
		},
	)
}

// waitForStandaloneReplicaUnavailable waits until the named standalone PodClique on the given PCS replica index
// reports fewer ready replicas than its MinAvailable, so the operator classifies that replica as unavailable.
func waitForStandaloneReplicaUnavailable(tc *testctx.TestContext, cliqueName string, pcsReplicaIndex int) error {
	fqn := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: tc.Workload.Name, Replica: pcsReplicaIndex}, cliqueName)
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		var pclq grovev1beta1.PodClique
		if err := tc.Client.Get(ctx, types.NamespacedName{Namespace: tc.Namespace, Name: fqn}, &pclq); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		minAvailable := int32(1)
		if pclq.Spec.MinAvailable != nil {
			minAvailable = *pclq.Spec.MinAvailable
		}
		return pclq.Status.ReadyReplicas < minAvailable, nil
	})
}

// waitForClaimedPCSReplicaIndices waits until the claimed PCS replica indices equal the expected set.
func waitForClaimedPCSReplicaIndices(tc *testctx.TestContext, expectedReplicaIndices []int) error {
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(context.Context) (bool, error) {
		claimed, err := claimedPCSReplicaIndices(tc)
		if err != nil {
			return false, client.IgnoreNotFound(err)
		}
		return slices.Equal(claimed, expectedReplicaIndices), nil
	})
}

// waitForActiveReplacementTargetingIndex waits until an active replacement whose target is targetReplicaIndex is
// present in UpdateProgress.CurrentlyUpdating, and returns the source replica index of that replacement. It is
// used to observe a different-index replacement that fills an index gap.
func waitForActiveReplacementTargetingIndex(tc *testctx.TestContext, targetReplicaIndex int32) (int32, error) {
	var sourceReplicaIndex int32
	fetchPCS := waiter.FetchByName(tc.Workload.Name, k8sclient.Getter[*grovev1beta1.PodCliqueSet](tc.Client, tc.Namespace))
	predicate := waiter.Predicate[*grovev1beta1.PodCliqueSet](func(pcs *grovev1beta1.PodCliqueSet) bool {
		if pcs == nil || pcs.Status.UpdateProgress == nil {
			return false
		}
		for _, replicaProgress := range pcs.Status.UpdateProgress.CurrentlyUpdating {
			replacement := replicaProgress.Replacement
			if replacement != nil && replacement.CanceledAt == nil && replacement.TargetReplicaIndex == targetReplicaIndex {
				sourceReplicaIndex = replicaProgress.ReplicaIndex
				return true
			}
		}
		return false
	})
	w := waiter.New[*grovev1beta1.PodCliqueSet]().WithTimeout(tc.Timeout).WithInterval(tc.Interval)
	if err := w.WaitUntil(tc.Ctx, fetchPCS, predicate); err != nil {
		return 0, err
	}
	return sourceReplicaIndex, nil
}

// watchPodCliqueSet starts watching changes after the workload's current resource version. Call it before
// mutating the PCS so transient status changes are not lost. The caller owns the watch and must stop it.
func watchPodCliqueSet(ctx context.Context, tc *testctx.TestContext) (watch.Interface, error) {
	var pcs grovev1beta1.PodCliqueSet
	key := types.NamespacedName{Namespace: tc.Namespace, Name: tc.Workload.Name}
	if err := tc.Client.Get(ctx, key, &pcs); err != nil {
		return nil, fmt.Errorf("get PodCliqueSet %s before watching: %w", key, err)
	}
	watchClient, err := client.NewWithWatch(tc.Client.RestConfig, client.Options{Scheme: tc.Client.Scheme()})
	if err != nil {
		return nil, fmt.Errorf("create PodCliqueSet watch client: %w", err)
	}
	watcher, err := watchClient.Watch(ctx, &grovev1beta1.PodCliqueSetList{},
		client.InNamespace(tc.Namespace),
		client.MatchingFields{"metadata.name": tc.Workload.Name},
		&client.ListOptions{Raw: &metav1.ListOptions{ResourceVersion: pcs.ResourceVersion}},
	)
	if err != nil {
		return nil, fmt.Errorf("watch PodCliqueSet %s: %w", key, err)
	}
	return watcher, nil
}

// waitForCanceledReplacement consumes a watch established before scale-in and returns the cancellation message
// for the expected source replica. It observes the cancellation event even if a later event already removed
// the drained replacement from CurrentlyUpdating. The caller owns the watch and its lifetime.
func waitForCanceledReplacement(ctx context.Context, watcher watch.Interface, sourceReplicaIndex int32) (string, error) {
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("waiting for replacement of replica %d to be canceled: %w", sourceReplicaIndex, ctx.Err())
		case event, open := <-watcher.ResultChan():
			if !open {
				return "", fmt.Errorf("PodCliqueSet watch closed before replacement of replica %d was canceled", sourceReplicaIndex)
			}
			switch event.Type {
			case watch.Error:
				return "", fmt.Errorf("PodCliqueSet watch failed: %w", apierrors.FromObject(event.Object))
			case watch.Deleted:
				return "", fmt.Errorf("PodCliqueSet deleted before replacement of replica %d was canceled", sourceReplicaIndex)
			case watch.Added, watch.Modified:
				pcs, ok := event.Object.(*grovev1beta1.PodCliqueSet)
				if !ok {
					return "", fmt.Errorf("unexpected PodCliqueSet watch object %T", event.Object)
				}
				if pcs.Status.UpdateProgress == nil {
					continue
				}
				for _, replicaProgress := range pcs.Status.UpdateProgress.CurrentlyUpdating {
					replacement := replicaProgress.Replacement
					if replicaProgress.ReplicaIndex != sourceReplicaIndex || replacement == nil || replacement.CanceledAt == nil {
						continue
					}
					if replacement.CancellationMessage != nil {
						return *replacement.CancellationMessage, nil
					}
					return "", nil
				}
			}
		}
	}
}

// requireReplicaSnapshot fetches replica 0's resources and verifies the PCS scheduling hashes exist.
func requireReplicaSnapshot(t *testing.T, tc *testctx.TestContext) replicaRecreationSnapshot {
	t.Helper()
	return requireReplicaSnapshotForIndex(t, tc, 0)
}

// requireReplicaSnapshotForIndex fetches one replica's resources and requires initialized PCS scheduling hashes.
func requireReplicaSnapshotForIndex(t *testing.T, tc *testctx.TestContext, pcsReplicaIndex int) replicaRecreationSnapshot {
	t.Helper()
	pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err, "fetch PCS")
	require.NotNil(t, pcs.Status.CurrentGenerationHash)
	require.NotNil(t, pcs.Status.AppliedSchedulingConfigurationHash)
	require.NotEmpty(t, *pcs.Status.CurrentGenerationHash)
	require.NotEmpty(t, *pcs.Status.AppliedSchedulingConfigurationHash)
	var pclq grovev1beta1.PodClique
	var pcsg grovev1beta1.PodCliqueScalingGroup
	pclqName := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: tc.Workload.Name, Replica: pcsReplicaIndex}, "pc-a")
	pclqKey := types.NamespacedName{Namespace: tc.Namespace, Name: pclqName}
	pcsgKey := types.NamespacedName{Namespace: tc.Namespace, Name: updateutils.PCSGFQNForReplica(tc, "sg-x", pcsReplicaIndex)}
	require.NoError(t, tc.Client.Get(tc.Ctx, pclqKey, &pclq), "fetch replica %d standalone pc-a", pcsReplicaIndex)
	require.NoError(t, tc.Client.Get(tc.Ctx, pcsgKey, &pcsg), "fetch replica %d sg-x", pcsReplicaIndex)
	pgm, err := updateutils.GetReplicaPodGangMap(tc, pcsReplicaIndex)
	require.NoError(t, err, "fetch replica %d PodGangMap", pcsReplicaIndex)
	return replicaRecreationSnapshot{PCS: pcs, PCLQ: &pclq, PCSG: &pcsg, PGM: pgm}
}

// assertReplicaConfiguration checks the applied generation markers, child configuration, and exact PGM composition.
func assertReplicaConfiguration(t *testing.T, snapshot replicaRecreationSnapshot, want expectedReplicaConfiguration) {
	t.Helper()
	require.NotNil(t, snapshot.PCLQ.Spec.MinAvailable)
	require.NotNil(t, snapshot.PCSG.Spec.MinAvailable)
	require.NotEmpty(t, want.PodGangMap.GenerationHash, "expected applied generation must be known")
	assert.Equal(t, want.PodGangMap.GenerationHash, snapshot.PCLQ.Labels[apicommon.LabelPodCliqueSetGenerationHash], "pc-a applied generation")
	assert.Equal(t, want.PodGangMap.GenerationHash, snapshot.PCSG.Labels[apicommon.LabelPodCliqueSetGenerationHash], "sg-x applied generation")
	assert.Equal(t, want.StandaloneMinAvailable, *snapshot.PCLQ.Spec.MinAvailable, "pc-a MinAvailable")
	assert.Equal(t, want.PCSGMinAvailable, *snapshot.PCSG.Spec.MinAvailable, "sg-x MinAvailable")
	assert.Equal(t, want.PCSGReplicas, snapshot.PCSG.Spec.Replicas, "sg-x replicas")
	updateutils.AssertReplicaPodGangMap(t, snapshot.PGM.Spec.Entries, want.PodGangMap)
}

// waitForSameIndexReplacementCleanup waits for an active replacement at replicaIndex to finish deleting
// its source resources. It checks PCS progress only. Replacement child resources may not exist yet.
func waitForSameIndexReplacementCleanup(tc *testctx.TestContext, replicaIndex int32) (*grovev1beta1.PodCliqueSet, error) {
	fetchPCS := waiter.FetchByName(tc.Workload.Name, k8sclient.Getter[*grovev1beta1.PodCliqueSet](tc.Client, tc.Namespace))
	predicate := waiter.Predicate[*grovev1beta1.PodCliqueSet](func(pcs *grovev1beta1.PodCliqueSet) bool {
		if pcs == nil || pcs.Status.CurrentGenerationHash == nil || pcs.Status.UpdateProgress == nil {
			return false
		}
		progress := pcs.Status.UpdateProgress
		if !progress.RequiresReplicaRecreation || progress.UpdateEndedAt != nil {
			return false
		}
		for _, replicaProgress := range progress.CurrentlyUpdating {
			replacement := replicaProgress.Replacement
			if replicaProgress.ReplicaIndex != replicaIndex || replicaProgress.UpdateEndedAt != nil || replacement == nil {
				continue
			}
			if replacement.TargetReplicaIndex == replicaIndex && replacement.CanceledAt == nil && replacement.SourceCleanupCompletedAt != nil {
				return true
			}
		}
		return false
	})
	return waiter.New[*grovev1beta1.PodCliqueSet]().WithTimeout(tc.Timeout).WithInterval(tc.Interval).
		WaitFor(tc.Ctx, fetchPCS, predicate)
}

// requireRecreatedReplica waits for fresh replica 0 resources with the expected configuration and an
// established cleanup barrier. Its standalone PodClique must remain unavailable while readiness is blocked.
func requireRecreatedReplica(
	t *testing.T,
	tc *testctx.TestContext,
	before replicaRecreationSnapshot,
	want expectedReplicaConfiguration,
) replicaRecreationSnapshot {
	t.Helper()
	pclq, err := waitForRecreatedPCLQ(tc, before.PCLQ)
	require.NoError(t, err, "standalone pc-a must be recreated")
	require.NoError(t, waitForStandaloneReplicaUnavailable(tc, "pc-a", 0))
	pcsg, err := waitForRecreatedPCSG(tc, before.PCSG)
	require.NoError(t, err, "sg-x must be recreated")
	expectedEntryCount := 2
	if want.PodGangMap.TailIndices != nil {
		expectedEntryCount++
	}
	pgm, err := waitForRecreatedPodGangMap(tc, before.PGM, want.PodGangMap.GenerationHash, expectedEntryCount)
	require.NoError(t, err, "PodGangMap must be recreated at the target generation")
	snapshot := requireReplicaSnapshot(t, tc)
	require.Equal(t, pclq.UID, snapshot.PCLQ.UID, "recreated pc-a must remain the observed target")
	require.Equal(t, pcsg.UID, snapshot.PCSG.UID, "recreated sg-x must remain the observed target")
	require.Equal(t, pgm.UID, snapshot.PGM.UID, "recreated PodGangMap must remain the observed target")
	requireReplicaResourceUIDsChanged(t, before, snapshot)
	require.Equal(t, want.PodGangMap.GenerationHash, *snapshot.PCS.Status.CurrentGenerationHash)
	require.Equal(t, before.PCS.Status.AppliedSchedulingConfigurationHash, snapshot.PCS.Status.AppliedSchedulingConfigurationHash,
		"an unavailable recreated target must not be marked applied")
	assertReplicaConfiguration(t, snapshot, want)
	progress := requireActiveReplicaRecreation(t, snapshot.PCS)
	replicaProgress := requireActiveReplacement(t, progress, expectedReplicaReplacement{
		SourcePCSReplicaIndex: 0,
		TargetPCSReplicaIndex: 0,
	})
	require.NotNil(t, replicaProgress.Replacement.SourceCleanupCompletedAt)
	return snapshot
}

// waitForRecreatedPCLQ waits for the PodClique to reappear with a new UID. Its spec is asserted by the caller.
func waitForRecreatedPCLQ(tc *testctx.TestContext, previous *grovev1beta1.PodClique) (*grovev1beta1.PodClique, error) {
	fetchPCLQ := waiter.FetchByName(previous.Name, k8sclient.Getter[*grovev1beta1.PodClique](tc.Client, previous.Namespace))
	predicate := waiter.Predicate[*grovev1beta1.PodClique](func(pclq *grovev1beta1.PodClique) bool {
		return pclq != nil && pclq.DeletionTimestamp == nil && pclq.UID != previous.UID
	})
	return waiter.New[*grovev1beta1.PodClique]().WithTimeout(tc.Timeout).WithInterval(tc.Interval).
		WaitFor(tc.Ctx, fetchPCLQ, predicate)
}

// waitForRecreatedPCSG waits for the scaling group to reappear with a new UID. Its spec is asserted by the caller.
func waitForRecreatedPCSG(tc *testctx.TestContext, previous *grovev1beta1.PodCliqueScalingGroup) (*grovev1beta1.PodCliqueScalingGroup, error) {
	fetchPCSG := waiter.FetchByName(previous.Name, k8sclient.Getter[*grovev1beta1.PodCliqueScalingGroup](tc.Client, previous.Namespace))
	predicate := waiter.Predicate[*grovev1beta1.PodCliqueScalingGroup](func(pcsg *grovev1beta1.PodCliqueScalingGroup) bool {
		return pcsg != nil && pcsg.DeletionTimestamp == nil && pcsg.UID != previous.UID
	})
	return waiter.New[*grovev1beta1.PodCliqueScalingGroup]().WithTimeout(tc.Timeout).WithInterval(tc.Interval).
		WaitFor(tc.Ctx, fetchPCSG, predicate)
}

// waitForRecreatedPodGangMap waits for a new map with the requested entry count and generation.
// The caller asserts each entry's composition after this milestone.
func waitForRecreatedPodGangMap(
	tc *testctx.TestContext,
	previous *grovev1beta1.PodGangMap,
	generationHash string,
	expectedEntryCount int,
) (*grovev1beta1.PodGangMap, error) {
	fetchPGM := waiter.FetchByName(previous.Name, k8sclient.Getter[*grovev1beta1.PodGangMap](tc.Client, previous.Namespace))
	predicate := waiter.Predicate[*grovev1beta1.PodGangMap](func(pgm *grovev1beta1.PodGangMap) bool {
		if pgm == nil || pgm.DeletionTimestamp != nil || pgm.UID == previous.UID || len(pgm.Spec.Entries) != expectedEntryCount {
			return false
		}
		for _, entry := range pgm.Spec.Entries {
			if entry.PodCliqueSetGenerationHash != generationHash {
				return false
			}
		}
		return true
	})
	return waiter.New[*grovev1beta1.PodGangMap]().WithTimeout(tc.Timeout).WithInterval(tc.Interval).
		WaitFor(tc.Ctx, fetchPGM, predicate)
}

// requireReplicaResourceUIDsChanged checks that structural recreation replaced every replica-scoped resource.
func requireReplicaResourceUIDsChanged(t *testing.T, before, after replicaRecreationSnapshot) {
	t.Helper()
	require.NotEqual(t, before.PCLQ.UID, after.PCLQ.UID, "%s UID must change", before.PCLQ.Name)
	require.NotEqual(t, before.PCSG.UID, after.PCSG.UID, "%s UID must change", before.PCSG.Name)
	require.NotEqual(t, before.PGM.UID, after.PGM.UID, "%s UID must change", before.PGM.Name)
}

// requireActiveReplicaRecreation returns the update progress of an unfinished structural recreation.
func requireActiveReplicaRecreation(t *testing.T, pcs *grovev1beta1.PodCliqueSet) *grovev1beta1.PodCliqueSetUpdateProgress {
	t.Helper()
	require.NotNil(t, pcs)
	progress := pcs.Status.UpdateProgress
	require.NotNil(t, progress, "PCS %s must have update progress", pcs.Name)
	require.True(t, progress.RequiresReplicaRecreation, "PCS %s must require structural recreation", pcs.Name)
	require.Nil(t, progress.UpdateEndedAt, "PCS %s recreation must remain unfinished", pcs.Name)
	return progress
}

// requireActiveReplacement returns the single unfinished replica's progress, requiring a non-canceled
// replacement with the expected source and target indices. Retained completed selections are ignored.
func requireActiveReplacement(
	t *testing.T,
	progress *grovev1beta1.PodCliqueSetUpdateProgress,
	want expectedReplicaReplacement,
) *grovev1beta1.PodCliqueSetReplicaUpdateProgress {
	t.Helper()
	require.NotNil(t, progress)
	require.Len(t, progress.CurrentlyUpdating, 1, "expected a single selected replacement")
	replicaProgress := &progress.CurrentlyUpdating[0]
	require.Equal(t, want.SourcePCSReplicaIndex, replicaProgress.ReplicaIndex, "replacement source index")
	require.Nil(t, replicaProgress.UpdateEndedAt, "replica %d replacement must remain unfinished", want.SourcePCSReplicaIndex)
	require.NotNil(t, replicaProgress.Replacement, "replica %d must have replacement progress", want.SourcePCSReplicaIndex)
	require.Equal(t, want.TargetPCSReplicaIndex, replicaProgress.Replacement.TargetReplicaIndex, "replacement target index")
	require.Nil(t, replicaProgress.Replacement.CanceledAt, "replica %d replacement must not be canceled", want.SourcePCSReplicaIndex)
	return replicaProgress
}

// requireSchedulingTargetChanged verifies a new structural target without advancing the applied baseline.
func requireSchedulingTargetChanged(t *testing.T, before, after *grovev1beta1.PodCliqueSet) recreationTarget {
	t.Helper()
	require.NotNil(t, before.Status.CurrentGenerationHash)
	require.NotNil(t, before.Status.AppliedSchedulingConfigurationHash)
	require.NotNil(t, after.Status.CurrentGenerationHash)
	require.NotEqual(t, *before.Status.CurrentGenerationHash, *after.Status.CurrentGenerationHash, "PCS generation must change")
	progress := requireActiveReplicaRecreation(t, after)
	require.NotEmpty(t, progress.TargetSchedulingConfigurationHash)
	previousSchedulingTarget := *before.Status.AppliedSchedulingConfigurationHash
	if before.Status.UpdateProgress != nil && before.Status.UpdateProgress.UpdateEndedAt == nil {
		previousSchedulingTarget = before.Status.UpdateProgress.TargetSchedulingConfigurationHash
	}
	require.NotEqual(t, previousSchedulingTarget, progress.TargetSchedulingConfigurationHash, "scheduling target must change")
	require.Equal(t, before.Status.AppliedSchedulingConfigurationHash, after.Status.AppliedSchedulingConfigurationHash,
		"an unavailable replacement must not advance the applied scheduling baseline")
	return recreationTarget{GenerationHash: *after.Status.CurrentGenerationHash, SchedulingHash: progress.TargetSchedulingConfigurationHash}
}

// waitForRecreationSupersession observes a new generation and scheduling target requiring replica recreation
// on a watch started before the scheduling change. Losing the watch fails rather than skipping intermediate
// status changes. UntilWithoutRetry stops the supplied watch on return.
func waitForRecreationSupersession(ctx context.Context, watcher watch.Interface, previous *grovev1beta1.PodCliqueSet) (*grovev1beta1.PodCliqueSet, error) {
	event, err := watchtools.UntilWithoutRetry(ctx, watcher, recreationSupersessionCondition(previous))
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, fmt.Errorf("waiting for recreation supersession: %w", err)
	}
	pcs, ok := event.Object.(*grovev1beta1.PodCliqueSet)
	if !ok {
		return nil, fmt.Errorf("unexpected PCS supersession watch object %T", event.Object)
	}
	return pcs.DeepCopy(), nil
}

// recreationSupersessionCondition validates each PCS status update while fresh pod readiness is blocked,
// then succeeds when a new generation and scheduling target require replica recreation. An existing replacement must retain
// its mapping and restart cleanup. A superseded template update must clear coherent scope and epochs.
func recreationSupersessionCondition(previous *grovev1beta1.PodCliqueSet) watchtools.ConditionFunc {
	return func(event watch.Event) (bool, error) {
		switch event.Type {
		case watch.Error:
			return false, apierrors.FromObject(event.Object)
		case watch.Deleted:
			return false, fmt.Errorf("PCS deleted before recreation was superseded")
		case watch.Added, watch.Modified:
		default:
			return false, nil
		}
		pcs, ok := event.Object.(*grovev1beta1.PodCliqueSet)
		if !ok {
			return false, fmt.Errorf("unexpected PCS watch object %T", event.Object)
		}
		if err := validateRecreationWhileBlocked(previous, pcs); err != nil {
			return false, err
		}
		return pcs.Status.CurrentGenerationHash != nil &&
			*pcs.Status.CurrentGenerationHash != *previous.Status.CurrentGenerationHash &&
			pcs.Status.UpdateProgress.TargetSchedulingConfigurationHash != previous.Status.UpdateProgress.TargetSchedulingConfigurationHash, nil
	}
}

// validateRecreationWhileBlocked checks that the update is still active and unapplied, and that the active
// replacement mappings and cancellation details have not changed. Supersession must clear active replacement
// cleanup markers before deleting the obsolete targets and drop completed selections. Before supersession,
// completed selections remain unchanged. Per-update start times may change.
func validateRecreationWhileBlocked(previous, current *grovev1beta1.PodCliqueSet) error {
	progress := current.Status.UpdateProgress
	if progress == nil || !progress.RequiresReplicaRecreation || progress.UpdateEndedAt != nil {
		return fmt.Errorf("recreation stopped while replacement readiness was blocked")
	}
	if current.Status.AppliedSchedulingConfigurationHash == nil ||
		!assert.ObjectsAreEqual(previous.Status.AppliedSchedulingConfigurationHash, current.Status.AppliedSchedulingConfigurationHash) {
		return fmt.Errorf("scheduling configuration was marked applied while replacement readiness was blocked")
	}
	previousSelections := previous.Status.UpdateProgress.CurrentlyUpdating
	if len(progress.CurrentlyUpdating) != len(previousSelections) {
		return fmt.Errorf("replica selections changed during supersession: before %d, after %d", len(previousSelections), len(progress.CurrentlyUpdating))
	}
	hasSupersededGeneration := current.Status.CurrentGenerationHash != nil &&
		*current.Status.CurrentGenerationHash != *previous.Status.CurrentGenerationHash
	for i, replicaProgress := range progress.CurrentlyUpdating {
		before := previousSelections[i]
		if replicaProgress.UpdateEndedAt != nil {
			return fmt.Errorf("replica %d update ended while replacement readiness was blocked", replicaProgress.ReplicaIndex)
		}
		expectedReplacement := before.Replacement.DeepCopy()
		if hasSupersededGeneration && expectedReplacement != nil && expectedReplacement.CanceledAt == nil {
			expectedReplacement.SourceCleanupCompletedAt = nil
		}
		if replicaProgress.ReplicaIndex != before.ReplicaIndex || !assert.ObjectsAreEqual(expectedReplacement, replicaProgress.Replacement) {
			return fmt.Errorf("unexpected replacement mapping, cleanup marker, or cancellation details during supersession for replica %d", before.ReplicaIndex)
		}
	}
	return nil
}

// requireRecreationSuperseded verifies scheduling-target supersession, replacement preservation,
// cleanup reset, and update timestamps, returning the new target even when it matches the applied configuration.
func requireRecreationSuperseded(t *testing.T, before, after *grovev1beta1.PodCliqueSet) recreationTarget {
	t.Helper()
	beforeProgress := requireActiveReplicaRecreation(t, before)
	require.Len(t, beforeProgress.CurrentlyUpdating, 1)
	beforeReplicaProgress := beforeProgress.CurrentlyUpdating[0]
	require.NotNil(t, beforeReplicaProgress.Replacement)
	wantReplacement := expectedReplicaReplacement{
		SourcePCSReplicaIndex: beforeReplicaProgress.ReplicaIndex,
		TargetPCSReplicaIndex: beforeReplicaProgress.Replacement.TargetReplicaIndex,
	}
	requireActiveReplacement(t, beforeProgress, wantReplacement)
	target := requireSchedulingTargetChanged(t, before, after)
	progress := requireActiveReplicaRecreation(t, after)
	replicaProgress := requireActiveReplacement(t, progress, wantReplacement)
	require.NoError(t, validateRecreationWhileBlocked(before, after), "supersession must preserve replacement commitments")
	require.Nil(t, replicaProgress.Replacement.SourceCleanupCompletedAt, "supersession must restart cleanup")
	require.NotNil(t, progress.PreviousPodCliqueSetGenerationHash)
	assert.Equal(t, *before.Status.CurrentGenerationHash, *progress.PreviousPodCliqueSetGenerationHash)
	assert.Equal(t, progress.UpdateStartedAt, replicaProgress.UpdateStartedAt, "replacement must use the new update's start time")
	return target
}

// requireCleanupAfterSupersession waits for a fresh same-index cleanup barrier at the superseding target.
func requireCleanupAfterSupersession(t *testing.T, tc *testctx.TestContext, updatePCS *grovev1beta1.PodCliqueSet) {
	t.Helper()
	updateProgress := requireActiveReplicaRecreation(t, updatePCS)
	require.NotNil(t, updatePCS.Status.CurrentGenerationHash)
	require.Len(t, updateProgress.CurrentlyUpdating, 1)
	updateReplicaProgress := updateProgress.CurrentlyUpdating[0]
	require.NotNil(t, updateReplicaProgress.Replacement)
	require.Nil(t, updateReplicaProgress.Replacement.SourceCleanupCompletedAt, "supersession must reset the cleanup barrier")
	wantReplacement := expectedReplicaReplacement{
		SourcePCSReplicaIndex: updateReplicaProgress.ReplicaIndex,
		TargetPCSReplicaIndex: updateReplicaProgress.Replacement.TargetReplicaIndex,
	}
	require.Equal(t, wantReplacement.SourcePCSReplicaIndex, wantReplacement.TargetPCSReplicaIndex, "cleanup barrier requires a same-index replacement")
	cleanupPCS, err := waitForSameIndexReplacementCleanup(tc, wantReplacement.SourcePCSReplicaIndex)
	require.NoError(t, err, "superseded target resources must be cleaned up")
	require.NotNil(t, cleanupPCS.Status.CurrentGenerationHash)
	require.Equal(t, *updatePCS.Status.CurrentGenerationHash, *cleanupPCS.Status.CurrentGenerationHash)
	cleanupProgress := requireActiveReplicaRecreation(t, cleanupPCS)
	cleanupReplicaProgress := requireActiveReplacement(t, cleanupProgress, wantReplacement)
	cleanupAt := cleanupReplicaProgress.Replacement.SourceCleanupCompletedAt
	require.NotNil(t, cleanupAt)
	assert.False(t, cleanupAt.Before(&updateProgress.UpdateStartedAt), "cleanup completion must not predate supersession")
}

// requireReplicaRecreationConverged waits for completion and verifies status, ready pods, replica identities,
// child configuration, gang composition, and the absence of active child updates.
func requireReplicaRecreationConverged(t *testing.T, tc *testctx.TestContext, want expectedReplicaRecreationCompletion) {
	t.Helper()
	tcLongTimeout := *tc
	tcLongTimeout.Timeout = 2 * time.Minute
	// Inspect the final state even on timeout so a failure also reports the target PGM composition.
	assert.NoError(t, waitForReplicaRecreationComplete(&tcLongTimeout, want.Replicas))
	require.NoError(t, tc.WaitForPods(want.Pods))
	after := requireReplicaSnapshot(t, tc)
	assertReplicaRecreationCompleted(t, after.PCS, want)
	require.NoError(t, waitForClaimedPCSReplicaIndices(tc, want.PCSReplicaIndices))
	assertReplicaConfiguration(t, after, want.Configuration)
	updateutils.AssertGenerationHashConverged(tc)
	updateutils.AssertNoActiveChildUpdates(tc)
}

// assertReplicaRecreationCompleted checks the completed update's hashes, progress, and replica counts.
func assertReplicaRecreationCompleted(t *testing.T, pcs *grovev1beta1.PodCliqueSet, want expectedReplicaRecreationCompletion) {
	t.Helper()
	require.NotNil(t, pcs)
	require.NotNil(t, pcs.Status.CurrentGenerationHash)
	require.NotNil(t, pcs.Status.AppliedSchedulingConfigurationHash)
	assert.Equal(t, want.GenerationHash, *pcs.Status.CurrentGenerationHash, "PCS generation hash")
	assert.Equal(t, want.SchedulingHash, *pcs.Status.AppliedSchedulingConfigurationHash, "applied scheduling hash")
	require.NotNil(t, pcs.Status.UpdateProgress)
	assert.Equal(t, want.SchedulingHash, pcs.Status.UpdateProgress.TargetSchedulingConfigurationHash, "target scheduling hash")
	assert.NotNil(t, pcs.Status.UpdateProgress.UpdateEndedAt, "recreation must have completed")
	assert.Empty(t, pcs.Status.UpdateProgress.CurrentlyUpdating, "completed recreation must release its selections")
	assert.Equal(t, want.Replicas, pcs.Status.AvailableReplicas, "available PCS replicas")
	assert.Equal(t, want.Replicas, pcs.Status.UpdatedReplicas, "updated PCS replicas")
}

// newSerialRecreationObserver tracks serial replacement of a healthy, dense PCS identity set.
func newSerialRecreationObserver(pcsReplicas int32, baseline recreationTarget) *serialRecreationObserver {
	return &serialRecreationObserver{pcsReplicas: pcsReplicas, baseline: baseline}
}

// requireCombinedMinAvailableChange changes both workload2 scheduling inputs in one merge patch.
func requireCombinedMinAvailableChange(t *testing.T, tc *testctx.TestContext, standaloneMinAvailable, pcsgMinAvailable int32) {
	t.Helper()
	pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	base := pcs.DeepCopy()
	pclqTemplate := componentutils.FindPodCliqueTemplateSpecByName(pcs, "pc-a")
	require.NotNil(t, pclqTemplate)
	pclqTemplate.Spec.MinAvailable = new(standaloneMinAvailable)
	var pcsgConfig *grovev1beta1.PodCliqueScalingGroupConfig
	for i := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if pcs.Spec.Template.PodCliqueScalingGroupConfigs[i].Name == "sg-x" {
			pcsgConfig = &pcs.Spec.Template.PodCliqueScalingGroupConfigs[i]
			break
		}
	}
	require.NotNil(t, pcsgConfig)
	pcsgConfig.MinAvailable = new(pcsgMinAvailable)
	require.NoError(t, tc.Client.Patch(tc.Ctx, pcs, client.MergeFrom(base)), "patch both MinAvailable fields atomically")
}

// waitForActivePCSReplica preserves the same watch across held-replica milestones. The caller owns Stop.
func (o *serialRecreationObserver) waitForActivePCSReplica(
	ctx context.Context,
	watcher watch.Interface,
	pcsReplicaIndex int32,
) (*grovev1beta1.PodCliqueSet, error) {
	pcs, err := o.observeUntil(ctx, watcher, func() bool {
		return o.activePCSReplicaIndex != nil && *o.activePCSReplicaIndex == pcsReplicaIndex
	})
	if err != nil {
		return nil, fmt.Errorf("waiting for active PCS replica %d: %w", pcsReplicaIndex, err)
	}
	return pcs, nil
}

func (o *serialRecreationObserver) observeUntil(ctx context.Context, watcher watch.Interface, reached func() bool) (*grovev1beta1.PodCliqueSet, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case event, open := <-watcher.ResultChan():
			if !open {
				return nil, fmt.Errorf("PodCliqueSet watch closed before the serial recreation milestone")
			}
			switch event.Type {
			case watch.Error:
				return nil, fmt.Errorf("PodCliqueSet watch failed: %w", apierrors.FromObject(event.Object))
			case watch.Deleted:
				return nil, fmt.Errorf("PodCliqueSet deleted during serial recreation")
			case watch.Bookmark:
				continue
			case watch.Added, watch.Modified:
			default:
				return nil, fmt.Errorf("unexpected PodCliqueSet watch event %q", event.Type)
			}
			pcs, ok := event.Object.(*grovev1beta1.PodCliqueSet)
			if !ok || pcs == nil {
				return nil, fmt.Errorf("unexpected PodCliqueSet watch object %T", event.Object)
			}
			if err := o.observe(pcs.Status); err != nil {
				return nil, err
			}
			if reached() {
				return pcs.DeepCopy(), nil
			}
		}
	}
}

// observe validates one status and retains an owned copy of the latest source-to-target commitments.
func (o *serialRecreationObserver) observe(status grovev1beta1.PodCliqueSetStatus) error {
	if status.CurrentGenerationHash == nil {
		return fmt.Errorf("PCS generation hash disappeared")
	}
	if o.target.GenerationHash == "" && *status.CurrentGenerationHash == o.baseline.GenerationHash {
		return nil // Status writes before the combined spec change are not part of this recreation.
	}
	progress := status.UpdateProgress
	if progress == nil {
		return fmt.Errorf("recreation progress disappeared")
	}
	target := recreationTarget{GenerationHash: *status.CurrentGenerationHash, SchedulingHash: progress.TargetSchedulingConfigurationHash}
	if target.SchedulingHash == "" || target.SchedulingHash == o.baseline.SchedulingHash {
		return fmt.Errorf("recreation has no new scheduling target")
	}
	if o.target.GenerationHash != "" && target != o.target {
		return fmt.Errorf("recreation target changed: before %+v, after %+v", o.target, target)
	}
	if progress.UpdateEndedAt != nil {
		if status.AppliedSchedulingConfigurationHash == nil || *status.AppliedSchedulingConfigurationHash != target.SchedulingHash {
			return fmt.Errorf("completed recreation did not apply its target")
		}
		if len(progress.CurrentlyUpdating) != 0 {
			return fmt.Errorf("completed recreation retained selections")
		}
		if len(o.selectionOrder) != int(o.pcsReplicas) {
			return fmt.Errorf("recreation completed after observing %d of %d selections", len(o.selectionOrder), o.pcsReplicas)
		}
		o.activePCSReplicaIndex = nil
		o.isComplete = true
		return nil
	}
	if !progress.RequiresReplicaRecreation {
		return fmt.Errorf("structural recreation mode disappeared before completion")
	}
	if status.AppliedSchedulingConfigurationHash == nil || *status.AppliedSchedulingConfigurationHash != o.baseline.SchedulingHash {
		return fmt.Errorf("scheduling hash advanced before overall completion")
	}
	selectionsBySource, activePCSReplicaIndex, err := indexSerialRecreationSelections(progress.CurrentlyUpdating, o.pcsReplicas)
	if err != nil {
		return err
	}
	for pcsReplicaIndex, before := range o.selectionsBySource {
		after, exists := selectionsBySource[pcsReplicaIndex]
		if !exists {
			return fmt.Errorf("source %d mapping disappeared before overall completion", pcsReplicaIndex)
		}
		if before.UpdateEndedAt != nil && after.UpdateEndedAt == nil {
			return fmt.Errorf("completed source %d became unfinished", pcsReplicaIndex)
		}
		if !before.UpdateStartedAt.Equal(&after.UpdateStartedAt) {
			return fmt.Errorf("source %d replacement start time changed", pcsReplicaIndex)
		}
	}
	for pcsReplicaIndex, selection := range selectionsBySource {
		if _, exists := o.selectionsBySource[pcsReplicaIndex]; exists {
			continue
		}
		if pcsReplicaIndex != int32(len(o.selectionOrder)) {
			return fmt.Errorf("selected source %d, expected next source %d", pcsReplicaIndex, len(o.selectionOrder))
		}
		if selection.UpdateEndedAt != nil {
			return fmt.Errorf("source %d was already complete when first observed", pcsReplicaIndex)
		}
		if pcsReplicaIndex > 0 {
			previous := selectionsBySource[pcsReplicaIndex-1]
			if previous == nil || previous.UpdateEndedAt == nil {
				return fmt.Errorf("source %d selected before its predecessor completed", pcsReplicaIndex)
			}
			if selection.UpdateStartedAt.Before(previous.UpdateEndedAt) {
				return fmt.Errorf("source %d started before its predecessor completed", pcsReplicaIndex)
			}
		}
		o.selectionOrder = append(o.selectionOrder, pcsReplicaIndex)
	}
	o.target = target
	o.selectionsBySource = selectionsBySource
	o.activePCSReplicaIndex = activePCSReplicaIndex
	return nil
}

// indexSerialRecreationSelections rejects malformed commitments and enforces a single active replacement.
func indexSerialRecreationSelections(
	selections []grovev1beta1.PodCliqueSetReplicaUpdateProgress,
	pcsReplicas int32,
) (map[int32]*grovev1beta1.PodCliqueSetReplicaUpdateProgress, *int32, error) {
	selectionsBySource := make(map[int32]*grovev1beta1.PodCliqueSetReplicaUpdateProgress, len(selections))
	var activePCSReplicaIndex *int32
	activeCount := 0
	for _, selection := range selections {
		pcsReplicaIndex := selection.ReplicaIndex
		if pcsReplicaIndex < 0 || pcsReplicaIndex >= pcsReplicas {
			return nil, nil, fmt.Errorf("unexpected source %d", pcsReplicaIndex)
		}
		if _, exists := selectionsBySource[pcsReplicaIndex]; exists {
			return nil, nil, fmt.Errorf("duplicate source %d", pcsReplicaIndex)
		}
		if selection.Replacement == nil {
			return nil, nil, fmt.Errorf("source %d has no replacement progress", pcsReplicaIndex)
		}
		if selection.Replacement.TargetReplicaIndex != pcsReplicaIndex {
			return nil, nil, fmt.Errorf("source %d targets %d, expected same-index replacement", pcsReplicaIndex, selection.Replacement.TargetReplicaIndex)
		}
		if selection.Replacement.CanceledAt != nil {
			return nil, nil, fmt.Errorf("source %d replacement was canceled", pcsReplicaIndex)
		}
		if selection.UpdateEndedAt == nil {
			activeCount++
			activePCSReplicaIndex = new(pcsReplicaIndex)
		}
		selectionsBySource[pcsReplicaIndex] = selection.DeepCopy()
	}
	if activeCount > 1 {
		return nil, nil, fmt.Errorf("serial recreation has %d unfinished non-canceled replacements", activeCount)
	}
	return selectionsBySource, activePCSReplicaIndex, nil
}

// requireSerialReplicaRecreated waits for a held replica's fresh resources and checks retained prior completions.
func requireSerialReplicaRecreated(
	t *testing.T,
	tc *testctx.TestContext,
	before replicaRecreationSnapshot,
	pcsReplicaIndex int,
	want expectedReplicaConfiguration,
) replicaRecreationSnapshot {
	t.Helper()
	_, err := waitForRecreatedPCLQ(tc, before.PCLQ)
	require.NoError(t, err, "replica %d standalone pc-a must be recreated", pcsReplicaIndex)
	require.NoError(t, waitForStandaloneReplicaUnavailable(tc, "pc-a", pcsReplicaIndex))
	_, err = waitForRecreatedPCSG(tc, before.PCSG)
	require.NoError(t, err, "replica %d sg-x must be recreated", pcsReplicaIndex)
	_, err = waitForRecreatedPodGangMap(tc, before.PGM, want.PodGangMap.GenerationHash, 2)
	require.NoError(t, err, "replica %d PodGangMap must be recreated", pcsReplicaIndex)
	after := requireReplicaSnapshotForIndex(t, tc, pcsReplicaIndex)
	requireReplicaResourceUIDsChanged(t, before, after)
	assertReplicaConfiguration(t, after, want)
	require.Equal(t, want.PodGangMap.GenerationHash, *after.PCS.Status.CurrentGenerationHash)
	require.Equal(t, before.PCS.Status.AppliedSchedulingConfigurationHash, after.PCS.Status.AppliedSchedulingConfigurationHash,
		"an unavailable replacement must not advance the applied hash")
	progress := requireActiveReplicaRecreation(t, after.PCS)
	require.Len(t, progress.CurrentlyUpdating, pcsReplicaIndex+1, "completed mappings must remain alongside the active replacement")
	seenPCSReplicaIndices := make(map[int32]struct{}, len(progress.CurrentlyUpdating))
	for _, selection := range progress.CurrentlyUpdating {
		require.GreaterOrEqual(t, selection.ReplicaIndex, int32(0))
		require.LessOrEqual(t, selection.ReplicaIndex, int32(pcsReplicaIndex))
		require.NotNil(t, selection.Replacement)
		require.Equal(t, selection.ReplicaIndex, selection.Replacement.TargetReplicaIndex)
		require.Nil(t, selection.Replacement.CanceledAt)
		seenPCSReplicaIndices[selection.ReplicaIndex] = struct{}{}
		if selection.ReplicaIndex == int32(pcsReplicaIndex) {
			require.Nil(t, selection.UpdateEndedAt, "held replica %d must remain unfinished", pcsReplicaIndex)
			require.NotNil(t, selection.Replacement.SourceCleanupCompletedAt)
			continue
		}
		require.NotNil(t, selection.UpdateEndedAt, "prior replica %d must remain completed", selection.ReplicaIndex)
	}
	require.Len(t, seenPCSReplicaIndices, pcsReplicaIndex+1, "source indices must be unique")
	return after
}

// assertReplicaResourceUIDsUnchanged checks pending sources and completed targets are not recreated early or again.
func assertReplicaResourceUIDsUnchanged(t *testing.T, before, after replicaRecreationSnapshot) {
	t.Helper()
	assert.Equal(t, before.PCLQ.UID, after.PCLQ.UID, "%s UID must remain unchanged", before.PCLQ.Name)
	assert.Equal(t, before.PCSG.UID, after.PCSG.UID, "%s UID must remain unchanged", before.PCSG.Name)
	assert.Equal(t, before.PGM.UID, after.PGM.UID, "%s UID must remain unchanged", before.PGM.Name)
}

// waitForCompletion consumes the remainder of the original watch, including the final active replacement.
func (o *serialRecreationObserver) waitForCompletion(ctx context.Context, watcher watch.Interface) (*grovev1beta1.PodCliqueSet, error) {
	pcs, err := o.observeUntil(ctx, watcher, func() bool { return o.isComplete })
	if err != nil {
		return nil, fmt.Errorf("waiting for serial recreation completion: %w", err)
	}
	return pcs, nil
}

// selectionOrder returns an owned copy of the observed source selection order.
func (o *serialRecreationObserver) observedSelectionOrder() []int32 {
	return slices.Clone(o.selectionOrder)
}

// holdReplicaSourceCleanup adds a test finalizer to keep a source PodClique terminating.
// The returned release is idempotent and removes only this finalizer from the original UID.
func holdReplicaSourceCleanup(tc *testctx.TestContext, source *grovev1beta1.PodClique) (func() error, error) {
	const finalizer = "e2e.grove.io/hold-recreation-source-cleanup"
	key := client.ObjectKeyFromObject(source)
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current grovev1beta1.PodClique
		if err := tc.Client.Get(tc.Ctx, key, &current); err != nil {
			return err
		}
		if current.UID != source.UID {
			return fmt.Errorf("source %s UID changed before cleanup hold", key)
		}
		base := current.DeepCopy()
		controllerutil.AddFinalizer(&current, finalizer)
		return tc.Client.Patch(tc.Ctx, &current, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	}); err != nil {
		return nil, fmt.Errorf("hold source cleanup: %w", err)
	}
	return func() error {
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var current grovev1beta1.PodClique
			if err := tc.Client.Get(tc.Ctx, key, &current); err != nil {
				return client.IgnoreNotFound(err)
			}
			if current.UID != source.UID {
				return nil
			}
			base := current.DeepCopy()
			if !controllerutil.RemoveFinalizer(&current, finalizer) {
				return nil
			}
			return tc.Client.Patch(tc.Ctx, &current, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		})
	}, nil
}

// waitForReplicaSourceDeletion waits until cleanup reaches the held source PodClique.
func waitForReplicaSourceDeletion(tc *testctx.TestContext, source *grovev1beta1.PodClique) error {
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		var current grovev1beta1.PodClique
		if err := tc.Client.Get(ctx, client.ObjectKeyFromObject(source), &current); err != nil {
			return false, err
		}
		if current.UID != source.UID {
			return false, fmt.Errorf("source %s was recreated before cleanup completed", source.Name)
		}
		return current.DeletionTimestamp != nil, nil
	})
}

// requireRecreationResumed checks that a restart preserved the active update and its cleanup commitment.
func requireRecreationResumed(t *testing.T, before, after *grovev1beta1.PodCliqueSet) {
	t.Helper()
	beforeProgress := requireActiveReplicaRecreation(t, before)
	afterProgress := requireActiveReplicaRecreation(t, after)
	require.NotNil(t, before.Status.CurrentGenerationHash)
	require.NotNil(t, after.Status.CurrentGenerationHash)
	assert.Equal(t, before.Status.CurrentGenerationHash, after.Status.CurrentGenerationHash, "restart must preserve the target generation")
	assert.Equal(t, beforeProgress.TargetSchedulingConfigurationHash, afterProgress.TargetSchedulingConfigurationHash, "restart must preserve the scheduling target")
	assert.Equal(t, beforeProgress.UpdateStartedAt, afterProgress.UpdateStartedAt, "restart must not initialize another update")
	assert.Equal(t, beforeProgress.PreviousPodCliqueSetGenerationHash, afterProgress.PreviousPodCliqueSetGenerationHash)
	require.NoError(t, validateRecreationWhileBlocked(before, after), "restart must preserve the mapping, cleanup barrier, and unapplied baseline")
}

// waitForRecreationReplicaCount observes a scale change without permitting active replacement commitments to change.
// The watch must be started before scaling. Watch loss is an error, not a reason to skip intermediate statuses.
func waitForRecreationReplicaCount(ctx context.Context, watcher watch.Interface, before *grovev1beta1.PodCliqueSet, pcsReplicas int32) (*grovev1beta1.PodCliqueSet, error) {
	event, err := watchtools.UntilWithoutRetry(ctx, watcher, func(event watch.Event) (bool, error) {
		switch event.Type {
		case watch.Error:
			return false, apierrors.FromObject(event.Object)
		case watch.Deleted:
			return false, fmt.Errorf("PCS deleted during recreation scaling")
		case watch.Added, watch.Modified:
		default:
			return false, nil
		}
		pcs, ok := event.Object.(*grovev1beta1.PodCliqueSet)
		if !ok || pcs == nil {
			return false, fmt.Errorf("unexpected recreation scaling object %T", event.Object)
		}
		if err := validateRecreationWhileBlocked(before, pcs); err != nil {
			return false, err
		}
		if !assert.ObjectsAreEqual(before.Status.CurrentGenerationHash, pcs.Status.CurrentGenerationHash) ||
			before.Status.UpdateProgress.TargetSchedulingConfigurationHash != pcs.Status.UpdateProgress.TargetSchedulingConfigurationHash {
			return false, fmt.Errorf("PCS scaling superseded the scheduling target")
		}
		return pcs.Spec.Replicas == pcsReplicas, nil
	})
	if err != nil {
		return nil, fmt.Errorf("waiting for recreation scale to %d replicas: %w", pcsReplicas, err)
	}
	return event.Object.(*grovev1beta1.PodCliqueSet).DeepCopy(), nil
}

// waitForReplicaReadyPods waits for the expected number of live Ready pods on one PCS replica.
func waitForReplicaReadyPods(tc *testctx.TestContext, pcsReplicaIndex, expectedPods int) error {
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		var list corev1.PodList
		if err := tc.Client.List(ctx, &list, client.InNamespace(tc.Namespace), client.MatchingLabels{
			apicommon.LabelPartOfKey: tc.Workload.Name, apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(pcsReplicaIndex),
		}); err != nil {
			return false, err
		}
		live, ready := 0, 0
		for i := range list.Items {
			pod := &list.Items[i]
			if pod.DeletionTimestamp != nil {
				continue
			}
			live++
			if kubeutils.IsPodReady(pod) {
				ready++
			}
		}
		return live == expectedPods && ready == expectedPods, nil
	})
}

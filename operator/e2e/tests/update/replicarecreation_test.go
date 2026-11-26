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
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/k8sclient"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/kwok"
	"github.com/ai-dynamo/grove/operator/e2e/setup"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	updateutils "github.com/ai-dynamo/grove/operator/e2e/tests/update/utils"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	kubeutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	watchtools "k8s.io/client-go/tools/watch"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/yaml"
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
	before := requireReplicaSnapshot(t, tc)
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
	after := requireReplicaSnapshot(t, tc)
	requireReplicaResourceUIDsChanged(t, before, after)
	updateutils.AssertGenerationHashConverged(tc)
	updateutils.AssertNoActiveChildUpdates(tc)
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
	before := requireReplicaSnapshot(t, tc)
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
	after := requireReplicaSnapshot(t, tc)
	requireReplicaResourceUIDsChanged(t, before, after)
	updateutils.AssertGenerationHashConverged(tc)
	updateutils.AssertNoActiveChildUpdates(tc)
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

// Test_RR3_DifferentIndexReplacementFillsIndexGap verifies that different-index replacement fills the
// lowest available gap, but does not release its source identity until every cleanup participant confirms
// deletion. A Ready target alone cannot complete a replacement while its source PodGangMap is terminating.
// After confirmed cleanup, the next replacement may reuse that source index without inheriting completion.
//
// Scenario:
//  1. Deploy workload-recreation-standalone with four Ready PCS replicas at indices 0, 1, 2, and 3.
//  2. Make replica 1 unavailable and scale in to three replicas, leaving original sources 0, 2, and 3.
//  3. Hold source 2's PodGangMap with a finalizer and increase worker MinAvailable 1 -> 2. Observe 2 -> 1
//     after 0 -> 0 completes. Target creation may overlap the held source cleanup.
//  4. Let target 1 become Ready and wait until every other source-2 resource is gone. Publish a PCS label
//     and wait for its propagation to target 1's PodGang, proving another full resource sync ran after
//     target readiness. Verify 2 -> 1 still has neither source-cleanup confirmation nor completion, and
//     source 3 remains untouched while the old PodGangMap still occupies index 2.
//  5. Hold fresh target-2 readiness, release the PodGangMap finalizer, and watch confirmed source cleanup
//     and completion of 2 -> 1 followed by selection of 3 -> 2.
//  6. Verify target 2 has fresh S1 PodClique and PodGangMap UIDs and exact gang membership. It remains
//     unfinished despite the historical completed source-2 entry, which must remain unchanged.
//  7. Release target-2 readiness and verify six Ready pods at indices 0, 1, and 2, generation convergence,
//     and completion at the recorded scheduling target.
func Test_RR3_DifferentIndexReplacementFillsIndexGap(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-recreation-standalone with four Ready PCS replicas")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload-recreation-standalone", workloadYAML: "../../yaml/workload-recreation-standalone.yaml",
		workerNodes: 10, expectedPods: 2, initialPCSReplicas: 4, postScalePods: 8,
	})
	t.Cleanup(cleanup)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(tc.Ctx), tc.Timeout)
		defer cancel()
		assert.NoError(t, kwok.DeleteStage(ctx, tc.Client, kwokStageReadyBlockedR1Name))
		assert.NoError(t, kwok.DeleteStage(ctx, tc.Client, kwokStageReadyDelayedName))
		assert.NoError(t, kwok.DeleteStage(ctx, tc.Client, kwokStageReadyBlockedR2Name))
	})
	require.NoError(t, waitForClaimedPCSReplicaIndices(tc, []int{0, 1, 2, 3}))

	tests.Logger.Info("2. Make replica 1 unavailable and scale in to original sources 0, 2, and 3")
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Path))
	require.NoError(t, deletePodsForReplica(tc, 1))
	require.NoError(t, waitForStandaloneReplicaUnavailable(tc, "worker", 1))
	require.NoError(t, tc.ScalePCS(tc.Workload.Name, 3))
	require.NoError(t, waitForClaimedPCSReplicaIndices(tc, []int{0, 2, 3}))
	require.NoError(t, waitForReplicaResourcesDeleted(tc, client.MatchingLabels{
		apicommon.LabelPartOfKey: tc.Workload.Name, apicommon.LabelPodCliqueSetReplicaIndex: "1",
	}))
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Name))
	baselinePCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	oldPGM, err := updateutils.GetReplicaPodGangMap(tc, 2)
	require.NoError(t, err)
	oldSourceKey := client.ObjectKey{Namespace: tc.Namespace, Name: apicommon.GeneratePodCliqueName(
		apicommon.ResourceNameReplica{Name: tc.Workload.Name, Replica: 2}, "worker",
	)}
	var oldSource grovev1beta1.PodClique
	require.NoError(t, tc.Client.Get(tc.Ctx, oldSourceKey, &oldSource))
	sourceThreeKey := client.ObjectKey{Namespace: tc.Namespace, Name: apicommon.GeneratePodCliqueName(
		apicommon.ResourceNameReplica{Name: tc.Workload.Name, Replica: 3}, "worker",
	)}
	var sourceThree grovev1beta1.PodClique
	require.NoError(t, tc.Client.Get(tc.Ctx, sourceThreeKey, &sourceThree))

	tests.Logger.Info("3. Hold source 2's PodGangMap and observe replacement 2 -> 1")
	releaseSource, err := holdReplicaSourceCleanup(tc, oldPGM)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, releaseSource()) })
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyDelayedPath))
	require.NoError(t, triggerStandalonePCLQMinAvailableChange(tc, "worker", 2))
	sourcePCSReplicaIndex, err := waitForActiveReplacementTargetingIndex(tc, 1)
	require.NoError(t, err)
	require.Equal(t, int32(2), sourcePCSReplicaIndex)
	blockedPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	target := requireSchedulingTargetChanged(t, baselinePCS, blockedPCS)
	watchCtx, cancelWatch := context.WithTimeout(tc.Ctx, tc.Timeout)
	defer cancelWatch()
	pcsWatcher, err := watchPodCliqueSet(watchCtx, tc)
	require.NoError(t, err)
	defer pcsWatcher.Stop()

	tests.Logger.Info("4. Make target 1 Ready and prove source-only PodGangMap cleanup still blocks completion")
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName))
	updateutils.DeleteNotReadyPodsOnReplica(t, tc, 1)
	targetOneKey := client.ObjectKey{Namespace: tc.Namespace, Name: apicommon.GeneratePodCliqueName(
		apicommon.ResourceNameReplica{Name: tc.Workload.Name, Replica: 1}, "worker",
	)}
	require.NoError(t, wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		var pclq grovev1beta1.PodClique
		if err := tc.Client.Get(ctx, targetOneKey, &pclq); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		var pcs grovev1beta1.PodCliqueSet
		if err := tc.Client.Get(ctx, client.ObjectKeyFromObject(blockedPCS), &pcs); err != nil {
			return false, err
		}
		progress := findPCSReplicaUpdateSelection(pcs.Status.UpdateProgress, 2)
		if progress == nil || progress.UpdateEndedAt != nil {
			return false, fmt.Errorf("source 2 completion was recorded while its PodGangMap was still held")
		}
		return componentutils.IsPCLQUpdateComplete(&pcs, &pclq) && pclq.Status.ReadyReplicas == 2 &&
			pcs.Status.AvailableReplicas == 3 && pcs.Status.UpdatedReplicas == 2, nil
	}), "target 1 must be Ready and observed by PCS status while source 3 is still at S0")
	require.NoError(t, waitForReplicaResourcesDeleted(tc, client.MatchingLabels{
		apicommon.LabelPartOfKey: tc.Workload.Name, apicommon.LabelPodCliqueSetReplicaIndex: "2",
	}, &grovev1beta1.PodCliqueList{}, &grovev1beta1.PodCliqueScalingGroupList{},
		&groveschedulerv1alpha1.PodGangList{}, &corev1.PodList{}, &corev1.ServiceList{}))
	// Label propagation occurs in G5, after G1 checks replacement completion. It is a causal sync
	// milestone after target readiness, rather than a sleep that could miss a premature completion.
	const cleanupCheckLabel = "e2e.grove.io/rr3-cleanup-check"
	checkedPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	probePatch := []byte(fmt.Sprintf(`{"metadata":{"labels":{%q:"ready"}}}`, cleanupCheckLabel))
	require.NoError(t, tc.Client.Patch(tc.Ctx, checkedPCS, client.RawPatch(types.MergePatchType, probePatch)))
	require.NoError(t, wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		var podGangs groveschedulerv1alpha1.PodGangList
		if err := tc.Client.List(ctx, &podGangs, client.InNamespace(tc.Namespace), client.MatchingLabels{
			apicommon.LabelPartOfKey: tc.Workload.Name, apicommon.LabelPodCliqueSetReplicaIndex: "1",
		}); err != nil {
			return false, err
		}
		if len(podGangs.Items) == 0 {
			return false, nil
		}
		for _, podGang := range podGangs.Items {
			if podGang.Labels[cleanupCheckLabel] != "ready" {
				return false, nil
			}
		}
		return true, nil
	}), "a full resource sync after target readiness must publish the PCS label")
	checkedPCS, err = updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	pending := requireActiveReplacement(t, requireActiveReplicaRecreation(t, checkedPCS), expectedReplicaReplacement{
		SourcePCSReplicaIndex: 2, TargetPCSReplicaIndex: 1,
	})
	require.Nil(t, pending.Replacement.SourceCleanupCompletedAt, "terminating PodGangMap must prevent source cleanup confirmation")
	require.Nil(t, findPCSReplicaUpdateSelection(checkedPCS.Status.UpdateProgress, 3), "source 3 cannot start before source 2 is released")
	require.Equal(t, baselinePCS.Status.AppliedSchedulingConfigurationHash, checkedPCS.Status.AppliedSchedulingConfigurationHash)
	var heldPGM grovev1beta1.PodGangMap
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(oldPGM), &heldPGM))
	require.Equal(t, oldPGM.UID, heldPGM.UID)
	require.NotNil(t, heldPGM.DeletionTimestamp)
	var untouchedSourceThree grovev1beta1.PodClique
	require.NoError(t, tc.Client.Get(tc.Ctx, sourceThreeKey, &untouchedSourceThree))
	require.Equal(t, sourceThree.UID, untouchedSourceThree.UID)
	require.Equal(t, *baselinePCS.Status.CurrentGenerationHash, untouchedSourceThree.Labels[apicommon.LabelPodCliqueSetGenerationHash])

	tests.Logger.Info("5. Release the source PodGangMap and watch 2 -> 1 complete before 3 -> 2 begins")
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR2Path))
	require.NoError(t, releaseSource())
	handoffPCS, err := waitForNextReplacementAfterSourceCleanup(watchCtx, pcsWatcher, blockedPCS, expectedReplicaReplacement{
		SourcePCSReplicaIndex: 3, TargetPCSReplicaIndex: 2,
	})
	require.NoError(t, err)
	completed := findPCSReplicaUpdateSelection(handoffPCS.Status.UpdateProgress, 2)
	require.NotNil(t, completed)
	require.NotNil(t, completed.Replacement.SourceCleanupCompletedAt)
	require.NotNil(t, completed.UpdateEndedAt)
	historicalCompletion := completed.DeepCopy()

	tests.Logger.Info("6. Verify fresh target-2 resources cannot inherit source 2's earlier completion")
	require.NoError(t, wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		var pclq grovev1beta1.PodClique
		if err := tc.Client.Get(ctx, oldSourceKey, &pclq); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		pgm, err := updateutils.GetReplicaPodGangMap(tc, 2)
		if err != nil {
			return false, client.IgnoreNotFound(err)
		}
		return pclq.UID != oldSource.UID && pgm.UID != oldPGM.UID &&
			pclq.Labels[apicommon.LabelPodCliqueSetGenerationHash] == target.GenerationHash &&
			pgm.DeletionTimestamp == nil && len(pgm.Spec.Entries) == 1 &&
			pgm.Spec.Entries[0].PodCliqueSetGenerationHash == target.GenerationHash, nil
	}), "target 2 requires fresh S1 PodClique and PodGangMap identities")
	require.NoError(t, waitForStandaloneReplicaUnavailable(tc, "worker", 2))
	freshPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	requireActiveReplacement(t, requireActiveReplicaRecreation(t, freshPCS), expectedReplicaReplacement{
		SourcePCSReplicaIndex: 3, TargetPCSReplicaIndex: 2,
	})
	require.Equal(t, historicalCompletion, findPCSReplicaUpdateSelection(freshPCS.Status.UpdateProgress, 2))
	freshPGM, err := updateutils.GetReplicaPodGangMap(tc, 2)
	require.NoError(t, err)
	updateutils.AssertEntryRoles(t, freshPGM.Spec.Entries, grovev1beta1.PodGangEntryRoleAnchor)
	anchor := updateutils.RequireSingleEntryByRole(t, freshPGM.Spec.Entries, grovev1beta1.PodGangEntryRoleAnchor)
	require.Equal(t, target.GenerationHash, anchor.PodCliqueSetGenerationHash)
	updateutils.AssertStandalonePCLQPodCounts(t, anchor, map[string]int32{"worker": 2})
	updateutils.AssertPodGangEntryDependsOn(t, anchor, nil)
	require.Empty(t, anchor.PCSGReplicaIndices)

	tests.Logger.Info("7. Release target-2 readiness and converge to six Ready pods at indices 0, 1, and 2")
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR2Name))
	updateutils.DeleteNotReadyPodsOnReplica(t, tc, 2)
	require.NoError(t, waitForReplicaRecreationComplete(tc, 3))
	require.NoError(t, waitForClaimedPCSReplicaIndices(tc, []int{0, 1, 2}))
	require.NoError(t, tc.WaitForPods(6))
	updateutils.AssertGenerationHashConverged(tc)
	finalPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	require.Equal(t, target.SchedulingHash, *finalPCS.Status.AppliedSchedulingConfigurationHash)
	require.Empty(t, finalPCS.Status.UpdateProgress.CurrentlyUpdating)
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
	tests.Logger.Info("1. Deploy workload2, block replica 0 source cleanup with a test finalizer, and block replacement pod readiness with KWOK")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload2", workloadYAML: "../../yaml/workload2.yaml", workerNodes: 10, expectedPods: 10,
	})
	defer cleanup()
	before := requireReplicaSnapshot(t, tc)
	releaseSource, err := holdReplicaSourceCleanup(tc, before.PCLQ)
	require.NoError(t, err)
	defer func() { assert.NoError(t, releaseSource()) }()
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path))
	defer func() {
		assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
	}()

	tests.Logger.Info("2. Increase sg-x MinAvailable 1 -> 2 and wait for the source PodClique to become terminating")
	require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 2))
	require.NoError(t, waitForReplicaSourceDeletion(tc, before.PCLQ))
	blockedPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	target := requireSchedulingTargetChanged(t, before.PCS, blockedPCS)
	blockedReplacement := requireActiveReplacement(t, blockedPCS.Status.UpdateProgress, expectedReplicaReplacement{
		SourcePCSReplicaIndex: 0, TargetPCSReplicaIndex: 0,
	})
	require.Nil(t, blockedReplacement.Replacement.SourceCleanupCompletedAt, "test finalizer must prevent source cleanup completion")

	tests.Logger.Info("3. Restart before cleanup completes and verify the original source remains terminating")
	require.NoError(t, updateutils.RestartOperator(tc))
	resumedPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	requireRecreationResumed(t, blockedPCS, resumedPCS)
	var heldSource grovev1beta1.PodClique
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(before.PCLQ), &heldSource))
	assert.Equal(t, before.PCLQ.UID, heldSource.UID, "restart must not create a target before deleting the source")
	require.NotNil(t, heldSource.DeletionTimestamp)

	tests.Logger.Info("4. Remove the test finalizer and wait for the cleanup marker and new replacement resources")
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

	tests.Logger.Info("6. Remove the KWOK readiness block and verify completion without recreating replacement resources")
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
	tests.Logger.Info("1. Deploy workload2 and block readiness of replica 0's replacement standalone pods while changing sg-x MinAvailable 1 -> 2")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload2", workloadYAML: "../../yaml/workload2.yaml", workerNodes: 30, expectedPods: 10,
	})
	defer cleanup()
	before := requireReplicaSnapshot(t, tc)
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path))
	defer func() {
		assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
	}()
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

	tests.Logger.Info("4. Remove replica 0's KWOK readiness block and verify convergence without recreating the scale-out replicas")
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

// Test_RR10_NonTargetScaleInDuringActiveReplicaRecreation verifies that scale-in removes unavailable
// S0 replicas before an unavailable active replacement. The replacement keeps its resources, cleanup
// commitment, and scheduling target while the other replicas drain, then converges after its replacement pods become Ready.
//
// Scenario:
//  1. Deploy workload2 with three Ready PCS replicas and capture their S0 resources and scheduling baseline.
//  2. Block readiness of fresh standalone pods on replica 0 with a KWOK stage, increase sg-x MinAvailable
//     1 -> 2, and wait for source cleanup and the new same-index replacement resources.
//  3. Block readiness of fresh standalone pods on replicas 1 and 2 with KWOK stages and delete their existing
//     pods. Verify both replicas become unavailable while retaining their S0 specs, generation hashes,
//     and PodGangMap entries.
//  4. Start a PCS watch and scale 3 -> 1. Check every event until scale-in selects and removes exactly
//     replicas 1 and 2 without canceling replacement 0, resetting its cleanup marker, or changing the target.
//  5. Verify replica 0 remains unavailable with the same replacement resources and unapplied scheduling target.
//  6. Remove replica 0's KWOK stage and delete its NotReady pods so fresh pods can become Ready.
//     Verify one Ready replica at the original target, without recreating its resources again.
func Test_RR10_NonTargetScaleInDuringActiveReplicaRecreation(t *testing.T) {
	tests.Logger.Info("1. Deploy workload2 with three Ready PCS replicas and capture their S0 resources")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload2", workloadYAML: "../../yaml/workload2.yaml",
		workerNodes: 30, expectedPods: 10, initialPCSReplicas: 3, postScalePods: 30,
	})
	defer cleanup()
	require.NoError(t, waitForClaimedPCSReplicaIndices(tc, []int{0, 1, 2}))
	updateutils.AssertGenerationHashConverged(tc)
	beforeByPCSReplicaIndex := make(map[int]replicaRecreationSnapshot, 3)
	for pcsReplicaIndex := range 3 {
		beforeByPCSReplicaIndex[pcsReplicaIndex] = requireReplicaSnapshotForIndex(t, tc, pcsReplicaIndex)
	}
	before := beforeByPCSReplicaIndex[0]

	tests.Logger.Info("2. Block readiness of replica 0's replacement standalone pods while changing sg-x MinAvailable from 1 to 2")
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path))
	defer func() {
		assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
	}()
	require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 2))
	firstPCS, err := waitForSameIndexReplacementCleanup(tc, 0)
	require.NoError(t, err)
	target := requireSchedulingTargetChanged(t, before.PCS, firstPCS)
	want := expectedReplicaConfiguration{
		StandaloneMinAvailable: 1, PCSGMinAvailable: 2, PCSGReplicas: 2,
		PodGangMap: updateutils.ExpectedReplicaPodGangMap{
			GenerationHash: target.GenerationHash, StandalonePodCounts: map[string]int32{"pc-a": 2},
			PCSGName: "sg-x", AnchorIndices: []int32{0, 1},
		},
	}
	first := requireRecreatedReplica(t, tc, before, want)

	tests.Logger.Info("3. Make replicas 1 and 2 unavailable while they retain their S0 resources")
	// Ready non-target replicas would make scale-in cancel replacement 0 instead. Block them only
	// after replica 0 is selected so recreation cannot choose an unavailable non-target first.
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Path))
	defer func() {
		assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Name))
	}()
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR2Path))
	defer func() {
		assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR2Name))
	}()
	for _, pcsReplicaIndex := range []int{1, 2} {
		require.NoError(t, deletePodsForReplica(tc, pcsReplicaIndex))
		require.NoError(t, waitForStandaloneReplicaUnavailable(tc, "pc-a", pcsReplicaIndex))
		frozenBefore := beforeByPCSReplicaIndex[pcsReplicaIndex]
		frozen := requireReplicaSnapshotForIndex(t, tc, pcsReplicaIndex)
		assertReplicaResourceUIDsUnchanged(t, frozenBefore, frozen)
		assert.Equal(t, frozenBefore.PCLQ.Spec, frozen.PCLQ.Spec, "non-target pc-a must retain its S0 spec")
		assert.Equal(t, frozenBefore.PCSG.Spec, frozen.PCSG.Spec, "non-target sg-x must retain its S0 spec")
		assert.Equal(t, frozenBefore.PGM.Spec.Entries, frozen.PGM.Spec.Entries, "non-target PGM must retain all S0 entries")
		require.Equal(t, before.PCS.Status.CurrentGenerationHash, frozen.PCLQ.Status.CurrentPodCliqueSetGenerationHash,
			"unavailable non-target pc-a must still report S0 for scale-in selection")
		require.Equal(t, before.PCS.Status.CurrentGenerationHash, frozen.PCSG.Status.CurrentPodCliqueSetGenerationHash,
			"non-target sg-x must still report S0 for scale-in selection")
	}

	tests.Logger.Info("4. Watch every PCS status event through scale-in from three replicas to one")
	watchCtx, cancelWatch := context.WithTimeout(tc.Ctx, tc.Timeout)
	defer cancelWatch()
	pcsWatcher, err := watchPodCliqueSet(watchCtx, tc)
	require.NoError(t, err)
	defer pcsWatcher.Stop()
	require.NoError(t, tc.ScalePCS(tc.Workload.Name, 1))
	_, err = waitForRecreationScaleIn(watchCtx, pcsWatcher, first.PCS, []int32{1, 2})
	require.NoError(t, err, "scale-in must remove replicas 1 and 2 without disturbing replacement 0")
	require.NoError(t, waitForClaimedPCSReplicaIndices(tc, []int{0}))

	tests.Logger.Info("5. Verify replica 0 remains unavailable with the same replacement resources, cleanup marker, and target")
	held := requireReplicaSnapshot(t, tc)
	require.NoError(t, validateRecreationWhileBlocked(first.PCS, held.PCS))
	assert.Equal(t, first.PCS.Status.CurrentGenerationHash, held.PCS.Status.CurrentGenerationHash)
	assert.Equal(t, target.SchedulingHash, held.PCS.Status.UpdateProgress.TargetSchedulingConfigurationHash)
	assert.Equal(t, first.PCS.Status.UpdateProgress.UpdateStartedAt, held.PCS.Status.UpdateProgress.UpdateStartedAt)
	assertReplicaResourceUIDsUnchanged(t, first, held)
	assertReplicaConfiguration(t, held, want)
	require.NoError(t, waitForStandaloneReplicaUnavailable(tc, "pc-a", 0))

	tests.Logger.Info("6. Remove replica 0's KWOK readiness block and verify one Ready replica at the unchanged scheduling target")
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
	updateutils.DeleteNotReadyPodsOnReplica(t, tc, 0)
	requireReplicaRecreationConverged(t, tc, expectedReplicaRecreationCompletion{
		GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash, Replicas: 1,
		Pods: 10, PCSReplicaIndices: []int{0}, Configuration: want,
	})
	assertReplicaResourceUIDsUnchanged(t, first, requireReplicaSnapshot(t, tc))
}

// Test_RR11_MinAvailableChangeDuringTemplateUpdate verifies that a MinAvailable change switches an active
// Coherent or RollingRecreate template update to full PCS replica recreation. The recreated resources
// and final pods must use both the template revision and the new scheduling configuration.
//
// Scenario:
//  1. Deploy workload2 with one Ready PCS replica, select the strategy, and capture the S0 resources.
//  2. Block fresh standalone pod readiness with a KWOK stage and change the pc-a template. Wait for an
//     S1 pod to exist and remain NotReady. Under Coherent, verify the full four-entry PodGangMap,
//     including the old and new anchors, the unchanged ScaleOut, and the advanced out-of-scope tail.
//     Under RollingRecreate, verify only the entries' generation hashes change.
//  3. Start a PCS watch and increase sg-x MinAvailable 1 -> 2. Verify the S2 update requires replica
//     recreation, clears coherent scope and in-flight epochs, and keeps the scheduling baseline unapplied.
//  4. Wait for source cleanup and verify pc-a, sg-x, and the PodGangMap are recreated at S2. Their
//     configuration must include the template revision and sg-x MinAvailable 2 while pc-a stays unavailable.
//  5. Remove the KWOK stage and delete NotReady pods. Verify convergence to S2 without recreating
//     the replacement resources again, and check every pc-a pod uses the template revision.
func Test_RR11_MinAvailableChangeDuringTemplateUpdate(t *testing.T) {
	for _, strategyType := range []grovev1beta1.UpdateStrategyType{grovev1beta1.CoherentStrategy, grovev1beta1.RollingRecreateStrategy} {
		t.Run(string(strategyType), func(t *testing.T) {
			tests.Logger.Infof("1. Deploy workload2 with one Ready PCS replica under %s", strategyType)
			tc, cleanup, _ := setupTest(t, testConfig{
				workloadName: "workload2", workloadYAML: "../../yaml/workload2.yaml",
				workerNodes: 10, expectedPods: 10,
			})
			defer cleanup()
			require.NoError(t, updateutils.UpdatePCSUpdateStrategy(tc, strategyType))
			updateutils.AssertGenerationHashConverged(tc)
			before := requireReplicaSnapshot(t, tc)
			require.Equal(t, strategyType, before.PCS.Spec.UpdateStrategy.Type)

			tests.Logger.Info("2. Block fresh standalone pod readiness with KWOK and verify the active pc-a update's PGM composition")
			require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path))
			defer func() {
				assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
			}()
			const templateRevision = "rr11-template"
			require.NoError(t, updateutils.TriggerPodCliqueUpdateWithRevision(tc, templateRevision, "pc-a"))
			require.NoError(t, updateutils.WaitForGenerationHashChange(tc, *before.PCS.Status.CurrentGenerationHash))
			require.NoError(t, updateutils.WaitForOrdinalUpdating(tc, 0))
			require.NoError(t, waitForNotReadyPodAtRevision(tc, templateRevision))
			if strategyType == grovev1beta1.CoherentStrategy {
				updateutils.WaitForReplicaMidCoherentRoll(t, tc, 0)
				require.NoError(t, waitForInFlightCoherentEpochs(tc))
			}
			first := requireReplicaSnapshot(t, tc)
			assertReplicaResourceUIDsUnchanged(t, before, first)
			firstProgress := first.PCS.Status.UpdateProgress
			require.NotNil(t, firstProgress)
			require.False(t, firstProgress.RequiresReplicaRecreation, "the first update must be an ordinary template update")
			require.Nil(t, firstProgress.UpdateEndedAt)
			require.Len(t, firstProgress.CurrentlyUpdating, 1)
			require.Equal(t, int32(0), firstProgress.CurrentlyUpdating[0].ReplicaIndex)
			require.Nil(t, firstProgress.CurrentlyUpdating[0].Replacement)
			switch strategyType {
			case grovev1beta1.CoherentStrategy:
				require.ElementsMatch(t, []string{"pc-a"}, firstProgress.InScopeStandalonePodCliques)
				assertWorkload2MidCoherentPodGangMap(t, before, first)
			case grovev1beta1.RollingRecreateStrategy:
				updateutils.AssertEntriesUnchangedExceptHashForNonCoherentUpdate(t, before.PGM.Spec.Entries, first.PGM.Spec.Entries,
					*first.PCS.Status.CurrentGenerationHash)
			}
			assert.Equal(t, before.PCS.Status.AppliedSchedulingConfigurationHash, first.PCS.Status.AppliedSchedulingConfigurationHash)

			tests.Logger.Info("3. Watch the MinAvailable change switch the active template update to replica recreation")
			watchCtx, cancelWatch := context.WithTimeout(tc.Ctx, tc.Timeout)
			defer cancelWatch()
			pcsWatcher, err := watchPodCliqueSet(watchCtx, tc)
			require.NoError(t, err)
			defer pcsWatcher.Stop()
			require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 2))
			secondPCS, err := waitForRecreationSupersession(watchCtx, pcsWatcher, first.PCS)
			require.NoError(t, err, "MinAvailable must supersede the unfinished template update with replica recreation")
			target := requireSchedulingTargetChanged(t, first.PCS, secondPCS)
			require.Equal(t, first.PCS.Status.CurrentGenerationHash, secondPCS.Status.UpdateProgress.PreviousPodCliqueSetGenerationHash)

			tests.Logger.Info("4. Verify source cleanup and fresh replacement resources include both changes")
			_, err = waitForSameIndexReplacementCleanup(tc, 0)
			require.NoError(t, err)
			want := expectedReplicaConfiguration{
				StandaloneMinAvailable: 1, PCSGMinAvailable: 2, PCSGReplicas: 2,
				PodGangMap: updateutils.ExpectedReplicaPodGangMap{
					GenerationHash: target.GenerationHash, StandalonePodCounts: map[string]int32{"pc-a": 2},
					PCSGName: "sg-x", AnchorIndices: []int32{0, 1},
				},
			}
			second := requireRecreatedReplica(t, tc, first, want)
			require.NoError(t, waitForNotReadyPodAtRevision(tc, templateRevision))

			tests.Logger.Info("5. Remove the KWOK readiness block and verify the latest template and scheduling target converge")
			require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
			updateutils.DeleteNotReadyPodsOnReplica(t, tc, 0)
			requireReplicaRecreationConverged(t, tc, expectedReplicaRecreationCompletion{
				GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash, Replicas: 1,
				Pods: 10, PCSReplicaIndices: []int{0}, Configuration: want,
			})
			assertReplicaResourceUIDsUnchanged(t, second, requireReplicaSnapshot(t, tc))
			updateutils.AssertAffectedPodsAtRevision(tc, "pc-a", templateRevision)
		})
	}
}

// Test_RR12_TemplateChangeRejectedDuringReplicaRecreation verifies admission rejects pod-template
// changes during a same-index recreation. Retrying after recreation finishes uses the configured
// Coherent or RollingRecreate strategy without recreating the PCS replica again.
//
// Scenario:
//  1. Deploy one Ready workload2 PCS replica under Coherent or RollingRecreate.
//  2. Keep replacement standalone pods NotReady using a KWOK stage and increase sg-x MinAvailable 1 -> 2.
//     Wait for source cleanup and fresh S1 replacement resources.
//  3. Attempt standalone and PCSG-member template changes. Verify rejection and unchanged PCS spec,
//     generation, replacement mapping, timestamps, resource identities and PGM entries.
//  4. Remove the KWOK stage and delete NotReady pods. Verify the original recreation completes at S1.
//  5. Retry the template change with fresh replica-0 standalone pods kept NotReady using KWOK. Verify an
//     ordinary update, remove the stage and delete NotReady pods, then check all latest pod templates.
func Test_RR12_TemplateChangeRejectedDuringReplicaRecreation(t *testing.T) {
	for _, strategyType := range []grovev1beta1.UpdateStrategyType{grovev1beta1.CoherentStrategy, grovev1beta1.RollingRecreateStrategy} {
		t.Run(string(strategyType), func(t *testing.T) {
			tests.Logger.Infof("1. Deploy workload2 with one Ready PCS replica under %s", strategyType)
			tc, cleanup, _ := setupTest(t, testConfig{
				workloadName: "workload2", workloadYAML: "../../yaml/workload2.yaml",
				workerNodes: 10, expectedPods: 10,
			})
			defer cleanup()
			require.NoError(t, updateutils.UpdatePCSUpdateStrategy(tc, strategyType))
			updateutils.AssertGenerationHashConverged(tc)
			before := requireReplicaSnapshot(t, tc)
			require.Equal(t, strategyType, before.PCS.Spec.UpdateStrategy.Type)

			tests.Logger.Info("2. Block replacement standalone pod readiness with KWOK and change sg-x MinAvailable from 1 to 2")
			require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path))
			defer func() {
				assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
			}()
			require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 2))
			firstPCS, err := waitForSameIndexReplacementCleanup(tc, 0)
			require.NoError(t, err)
			firstTarget := requireSchedulingTargetChanged(t, before.PCS, firstPCS)
			want := expectedReplicaConfiguration{
				StandaloneMinAvailable: 1, PCSGMinAvailable: 2, PCSGReplicas: 2,
				PodGangMap: updateutils.ExpectedReplicaPodGangMap{
					GenerationHash: firstTarget.GenerationHash, StandalonePodCounts: map[string]int32{"pc-a": 2},
					PCSGName: "sg-x", AnchorIndices: []int32{0, 1},
				},
			}
			first := requireRecreatedReplica(t, tc, before, want)

			tests.Logger.Info("3. Reject standalone and PCSG-member template changes while preserving the active replacement")
			const templateRevision = "rr12-template"
			requirePodTemplateChangesRejectedDuringRecreation(tc, first.PCS, templateRevision)
			unchanged := requireReplicaSnapshot(t, tc)
			assertReplicaResourceUIDsUnchanged(t, first, unchanged)
			require.Equal(t, first.PCLQ.Spec, unchanged.PCLQ.Spec)
			require.Equal(t, first.PCSG.Spec, unchanged.PCSG.Spec)
			require.Equal(t, first.PGM.Spec.Entries, unchanged.PGM.Spec.Entries)

			tests.Logger.Info("4. Remove the KWOK stage and verify recreation finishes at its original generation")
			require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
			updateutils.DeleteNotReadyPodsOnReplica(t, tc, 0)
			requireReplicaRecreationConverged(t, tc, expectedReplicaRecreationCompletion{
				GenerationHash: firstTarget.GenerationHash, SchedulingHash: firstTarget.SchedulingHash, Replicas: 1,
				Pods: 10, PCSReplicaIndices: []int{0}, Configuration: want,
			})
			completed := requireReplicaSnapshot(t, tc)
			assertReplicaResourceUIDsUnchanged(t, first, completed)

			tests.Logger.Info("5. Retry the template change after recreation and verify an ordinary configured-strategy update")
			requireWorkload2TemplateUpdateAfterRecreation(tc, []replicaRecreationSnapshot{completed}, templateRevision)
		})
	}
}

// Test_RR13_TemplateChangeRejectedDuringDifferentIndexReplacement verifies admission rejects
// template changes after a different-index replacement's source is gone. The completed replica and
// active target retain S1, then both final indices receive the template update after recreation completes.
//
// Scenario:
//  1. Deploy three Ready workload2 PCS replicas under Coherent or RollingRecreate.
//  2. Keep replica 1's fresh standalone pods NotReady using KWOK and scale 3 -> 2, leaving indices 0 and 2.
//  3. Increase sg-x MinAvailable 1 -> 2. Allow replica 0 to complete at S1, then wait for an active 2 -> 1
//     replacement at S1. Source 2 is fully deleted and target 1 remains unavailable.
//  4. Attempt standalone and PCSG-member template changes. Verify rejection, unchanged update progress,
//     and unchanged completed-replica and replacement-target resources at S1.
//  5. Remove the replica-1 KWOK stage and delete NotReady pods. Verify recreation finishes at S1 on 0 and 1.
//  6. Retry the template change after recreation. Verify an ordinary configured-strategy update without
//     another PCS replica recreation and latest full pod templates on every final index.
func Test_RR13_TemplateChangeRejectedDuringDifferentIndexReplacement(t *testing.T) {
	for _, strategyType := range []grovev1beta1.UpdateStrategyType{grovev1beta1.CoherentStrategy, grovev1beta1.RollingRecreateStrategy} {
		t.Run(string(strategyType), func(t *testing.T) {
			tests.Logger.Infof("1. Deploy workload2 with three Ready PCS replicas under %s", strategyType)
			tc, cleanup, _ := setupTest(t, testConfig{
				workloadName: "workload2", workloadYAML: "../../yaml/workload2.yaml",
				workerNodes: 30, expectedPods: 10, initialPCSReplicas: 3, postScalePods: 30,
			})
			defer cleanup()
			require.NoError(t, updateutils.UpdatePCSUpdateStrategy(tc, strategyType))
			updateutils.AssertGenerationHashConverged(tc)
			require.NoError(t, waitForClaimedPCSReplicaIndices(tc, []int{0, 1, 2}))

			tests.Logger.Info("2. Keep replica 1's standalone pods NotReady with KWOK and scale 3 -> 2 to create a gap")
			require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Path))
			defer func() {
				assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Name))
			}()
			require.NoError(t, deletePodsForReplica(tc, 1))
			require.NoError(t, waitForStandaloneReplicaUnavailable(tc, "pc-a", 1))
			require.NoError(t, tc.ScalePCS(tc.Workload.Name, 2))
			require.NoError(t, waitForClaimedPCSReplicaIndices(tc, []int{0, 2}))
			require.NoError(t, tc.WaitForPods(20))
			original := requireReplicaSnapshot(t, tc)
			source := requireReplicaSnapshotForIndex(t, tc, 2)

			tests.Logger.Info("3. Change sg-x MinAvailable 1 -> 2 and verify completed replica 0 alongside active replacement 2 -> 1")
			require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 2))
			sourcePCSReplicaIndex, err := waitForActiveReplacementTargetingIndex(tc, 1)
			require.NoError(t, err)
			require.Equal(t, int32(2), sourcePCSReplicaIndex)
			firstPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
			require.NoError(t, err)
			firstTarget := requireSchedulingTargetChanged(t, source.PCS, firstPCS)
			require.NoError(t, waitForWorkload2ReplicaAtGeneration(tc, 1, firstTarget.GenerationHash, 2))
			require.NoError(t, waitForReplicaResourcesDeleted(tc, client.MatchingLabels{
				apicommon.LabelPartOfKey: tc.Workload.Name, apicommon.LabelPodCliqueSetReplicaIndex: "2",
			}))
			first := requireReplicaSnapshotForIndex(t, tc, 1)
			want := expectedReplicaConfiguration{
				StandaloneMinAvailable: 1, PCSGMinAvailable: 2, PCSGReplicas: 2,
				PodGangMap: updateutils.ExpectedReplicaPodGangMap{
					GenerationHash: firstTarget.GenerationHash, StandalonePodCounts: map[string]int32{"pc-a": 2},
					PCSGName: "sg-x", AnchorIndices: []int32{0, 1},
				},
			}
			assertReplicaConfiguration(t, first, want)
			require.NoError(t, waitForStandaloneReplicaUnavailable(tc, "pc-a", 1))
			requireActiveReplacement(t, requireActiveReplicaRecreation(t, first.PCS), expectedReplicaReplacement{
				SourcePCSReplicaIndex: 2, TargetPCSReplicaIndex: 1,
			})
			firstProgress := first.PCS.Status.UpdateProgress
			require.Len(t, firstProgress.CurrentlyUpdating, 2, "the completed mapping must remain alongside replacement 2 -> 1")
			completedSelection := firstProgress.CurrentlyUpdating[0]
			require.Equal(t, int32(0), completedSelection.ReplicaIndex)
			require.NotNil(t, completedSelection.UpdateEndedAt)
			require.NotNil(t, completedSelection.Replacement)
			require.Equal(t, int32(0), completedSelection.Replacement.TargetReplicaIndex)
			completed := requireReplicaSnapshot(t, tc)
			requireReplicaResourceUIDsChanged(t, original, completed)
			assertReplicaConfiguration(t, completed, want)

			tests.Logger.Info("4. Reject template changes and preserve the completed replica and active replacement at S1")
			const templateRevision = "rr13-template"
			requirePodTemplateChangesRejectedDuringRecreation(tc, first.PCS, templateRevision)
			pending := requireReplicaSnapshot(t, tc)
			assertReplicaResourceUIDsUnchanged(t, completed, pending)
			require.Equal(t, completed.PCLQ.Spec, pending.PCLQ.Spec)
			require.Equal(t, completed.PCSG.Spec, pending.PCSG.Spec)
			require.Equal(t, completed.PGM.Spec.Entries, pending.PGM.Spec.Entries)
			unchangedTarget := requireReplicaSnapshotForIndex(t, tc, 1)
			assertReplicaResourceUIDsUnchanged(t, first, unchangedTarget)
			require.Equal(t, first.PCLQ.Spec, unchangedTarget.PCLQ.Spec)
			require.Equal(t, first.PCSG.Spec, unchangedTarget.PCSG.Spec)
			require.Equal(t, first.PGM.Spec.Entries, unchangedTarget.PGM.Spec.Entries)

			tests.Logger.Info("5. Remove the replica-1 KWOK stage and verify recreation completes at its original generation")
			require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Name))
			updateutils.DeleteNotReadyPodsOnReplica(t, tc, 1)
			requireReplicaRecreationConverged(t, tc, expectedReplicaRecreationCompletion{
				GenerationHash: firstTarget.GenerationHash, SchedulingHash: firstTarget.SchedulingHash, Replicas: 2,
				Pods: 20, PCSReplicaIndices: []int{0, 1}, Configuration: want,
			})
			after0 := requireReplicaSnapshot(t, tc)
			after1 := requireReplicaSnapshotForIndex(t, tc, 1)
			assertReplicaResourceUIDsUnchanged(t, completed, after0)
			assertReplicaResourceUIDsUnchanged(t, first, after1)
			assertReplicaConfiguration(t, after1, want)

			tests.Logger.Info("6. Retry the template change and verify every final replica receives the ordinary configured-strategy update")
			requireWorkload2TemplateUpdateAfterRecreation(tc, []replicaRecreationSnapshot{after0, after1}, templateRevision)
		})
	}
}

// Test_RR14_ScaleToZeroCancelsSameIndexReplacement verifies that scaling to zero cancels an active
// same-index replacement, cleans up every replica resource, and permits scale-out at the latest configuration.
//
// Scenario:
//  1. Deploy workload2 with one Ready PCS replica under Coherent or RollingRecreate.
//  2. Keep replacement 0's standalone pods NotReady using a KWOK stage, increase sg-x MinAvailable
//     1 -> 2, and wait for the same-index source cleanup and fresh S1 replacement resources.
//  3. Add a test finalizer to the replacement standalone PodClique and start a PCS watch before scaling
//     1 -> 0. Verify replacement 0 is canceled and remains unapplied while the finalizer blocks cleanup.
//  4. Remove the test finalizer. Verify all replica resources disappear, recreation completes with zero
//     replicas, and no replacement commitment or claimed replica index remains.
//  5. Remove the KWOK stage and scale 0 -> 1. Verify fresh resources use the latest MinAvailable,
//     full pod templates, and exact S1 PodGangMap composition without another recreation.
func Test_RR14_ScaleToZeroCancelsSameIndexReplacement(t *testing.T) {
	for _, strategyType := range []grovev1beta1.UpdateStrategyType{grovev1beta1.CoherentStrategy, grovev1beta1.RollingRecreateStrategy} {
		t.Run(string(strategyType), func(t *testing.T) {
			tests.Logger.Infof("1. Deploy workload2 with one Ready PCS replica under %s", strategyType)
			tc, cleanup, _ := setupTest(t, testConfig{
				workloadName: "workload2", workloadYAML: "../../yaml/workload2.yaml", workerNodes: 10, expectedPods: 10,
			})
			defer cleanup()
			require.NoError(t, updateutils.UpdatePCSUpdateStrategy(tc, strategyType))
			updateutils.AssertGenerationHashConverged(tc)
			before := requireReplicaSnapshot(t, tc)

			tests.Logger.Info("2. Keep replacement 0's standalone pods NotReady with KWOK and change sg-x MinAvailable 1 -> 2")
			require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path))
			defer func() {
				assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
			}()
			require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 2))
			firstPCS, err := waitForSameIndexReplacementCleanup(tc, 0)
			require.NoError(t, err)
			target := requireSchedulingTargetChanged(t, before.PCS, firstPCS)
			want := expectedReplicaConfiguration{
				StandaloneMinAvailable: 1, PCSGMinAvailable: 2, PCSGReplicas: 2,
				PodGangMap: updateutils.ExpectedReplicaPodGangMap{
					GenerationHash: target.GenerationHash, StandalonePodCounts: map[string]int32{"pc-a": 2},
					PCSGName: "sg-x", AnchorIndices: []int32{0, 1},
				},
			}
			first := requireRecreatedReplica(t, tc, before, want)

			tests.Logger.Info("3. Block replacement cleanup with a test finalizer and watch scale 1 -> 0 cancel replacement 0")
			removeFinalizer, err := holdReplicaSourceCleanup(tc, first.PCLQ)
			require.NoError(t, err)
			defer func() { assert.NoError(t, removeFinalizer()) }()
			watchCtx, cancelWatch := context.WithTimeout(tc.Ctx, tc.Timeout)
			defer cancelWatch()
			pcsWatcher, err := watchPodCliqueSet(watchCtx, tc)
			require.NoError(t, err)
			defer pcsWatcher.Stop()
			require.NoError(t, tc.ScalePCS(tc.Workload.Name, 0))
			cancellationMessage, err := waitForCanceledReplacement(watchCtx, pcsWatcher, 0)
			require.NoError(t, err)
			require.NotEmpty(t, cancellationMessage)
			require.NoError(t, waitForReplicaSourceDeletion(tc, first.PCLQ))
			zeroPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
			require.NoError(t, err)
			progress := requireActiveReplicaRecreation(t, zeroPCS)
			require.Len(t, progress.CurrentlyUpdating, 1)
			canceled := progress.CurrentlyUpdating[0]
			require.Equal(t, int32(0), canceled.ReplicaIndex)
			require.NotNil(t, canceled.Replacement)
			require.Equal(t, int32(0), canceled.Replacement.TargetReplicaIndex)
			require.NotNil(t, canceled.Replacement.CanceledAt)
			require.Nil(t, canceled.UpdateEndedAt)
			require.Equal(t, target.GenerationHash, *zeroPCS.Status.CurrentGenerationHash)
			require.Equal(t, target.SchedulingHash, progress.TargetSchedulingConfigurationHash)
			require.Equal(t, first.PCS.Status.AppliedSchedulingConfigurationHash, zeroPCS.Status.AppliedSchedulingConfigurationHash,
				"zero desired replicas must not complete recreation before canceled resources are cleaned up")

			tests.Logger.Info("4. Remove the test finalizer and verify zero replicas, no replica resources, and no replacement commitments")
			require.NoError(t, removeFinalizer())
			require.NoError(t, waitForReplicaResourcesDeleted(tc, client.MatchingLabels{apicommon.LabelPartOfKey: tc.Workload.Name}))
			require.NoError(t, waitForReplicaRecreationComplete(tc, 0))
			zeroPCS, err = updateutils.GetPCS(tc, tc.Workload.Name)
			require.NoError(t, err)
			assertReplicaRecreationCompleted(t, zeroPCS, expectedReplicaRecreationCompletion{
				GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash, Replicas: 0,
			})
			require.Equal(t, int32(0), zeroPCS.Spec.Replicas)
			require.Equal(t, int32(0), zeroPCS.Status.Replicas)
			require.Nil(t, zeroPCS.Status.ScalingProgress)
			require.NoError(t, waitForClaimedPCSReplicaIndices(tc, []int{}))
			updateutils.AssertUpdateInProgressCleared(tc)

			tests.Logger.Info("5. Remove the KWOK stage and scale 0 -> 1 using the latest configuration")
			require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
			require.NoError(t, tc.ScalePCS(tc.Workload.Name, 1))
			requireReplicaRecreationConverged(t, tc, expectedReplicaRecreationCompletion{
				GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash, Replicas: 1,
				Pods: 10, PCSReplicaIndices: []int{0}, Configuration: want,
			})
			requireReplicaResourceUIDsChanged(t, first, requireReplicaSnapshot(t, tc))
			assertLatestPodTemplatesOnPCSReplicas(tc, []int{0})
		})
	}
}

// Test_RR15_SchedulingSupersessionRecreatesDifferentIndexTarget verifies that superseding an active
// different-index replacement recreates its already-materialized target rather than patching obsolete gang
// membership in place. The configured strategy is Coherent, but both scheduling changes execute replica
// recreation. Holding the old source's deletion makes its replacement-owned cleanup observable across handoff.
//
// Scenario:
//  1. Deploy workload2 under Coherent with three Ready PCS replicas at indices 0, 1, and 2.
//  2. Block fresh standalone pods at index 1, delete its existing pods, and wait for unavailability before
//     scaling to two replicas. Scale-in removes index 1, leaving the gap between indices 0 and 2.
//  3. Hold source 2's pc-a deletion and increase pc-a MinAvailable 1 -> 2 (S1). Observe replacement 2 -> 1
//     and its target resources. The target's sg-x anchor contains index 0 and its tail contains index 1.
//  4. Increase sg-x MinAvailable 1 -> 2 (S2). Observe handoff to 1 -> 1, retirement of source 2, and a reset
//     cleanup barrier. Verify new target resource UIDs and an sg-x anchor containing both indices with no tail.
//  5. Release source 2 deletion while target readiness remains blocked. Verify all retired resources disappear
//     and retirement clears without completing the update or advancing the applied scheduling hash.
//  6. Release target readiness and verify two Ready replicas at indices 0 and 1, the exact S2 gang layout,
//     generation convergence, and completion at the S2 scheduling hash.
func Test_RR15_SchedulingSupersessionRecreatesDifferentIndexTarget(t *testing.T) {
	tests.Logger.Info("1. Deploy workload2 with three Ready PCS replicas under Coherent")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload2", workloadYAML: "../../yaml/workload2.yaml",
		workerNodes: 30, expectedPods: 10,
	})
	t.Cleanup(cleanup)
	require.NoError(t, updateutils.UpdatePCSUpdateStrategy(tc, grovev1beta1.CoherentStrategy))
	tc.ScalePCSAndWait(tc.Workload.Name, 3, 30, 0)
	updateutils.AssertGenerationHashConverged(tc)
	require.NoError(t, waitForClaimedPCSReplicaIndices(tc, []int{0, 1, 2}))

	tests.Logger.Info("2. Make replica 1 unavailable before scale-in and wait for complete removal")
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Path))
	blockRemoved := false
	t.Cleanup(func() {
		if !blockRemoved {
			assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Name))
		}
	})
	require.NoError(t, deletePodsForReplica(tc, 1))
	require.NoError(t, waitForStandaloneReplicaUnavailable(tc, "pc-a", 1))
	require.NoError(t, tc.ScalePCS(tc.Workload.Name, 2))
	require.NoError(t, waitForClaimedPCSReplicaIndices(tc, []int{0, 2}))
	replicaOneLabels := client.MatchingLabels{
		apicommon.LabelPartOfKey: tc.Workload.Name, apicommon.LabelPodCliqueSetReplicaIndex: "1",
	}
	require.NoError(t, waitForReplicaResourcesDeleted(tc, replicaOneLabels))
	baseline := requireReplicaSnapshotForIndex(t, tc, 2)
	require.Equal(t, grovev1beta1.CoherentStrategy, baseline.PCS.Spec.UpdateStrategy.Type)

	tests.Logger.Info("3. Hold source 2 deletion and materialize the S1 different-index target at index 1")
	releaseSource, err := holdReplicaSourceCleanup(tc, baseline.PCLQ)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, releaseSource()) })
	require.NoError(t, triggerStandalonePCLQMinAvailableChange(tc, "pc-a", 2))
	sourcePCSReplicaIndex, err := waitForActiveReplacementTargetingIndex(tc, 1)
	require.NoError(t, err)
	require.Equal(t, int32(2), sourcePCSReplicaIndex)
	require.NoError(t, waitForReplicaSourceDeletion(tc, baseline.PCLQ))
	firstPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	firstTarget := requireSchedulingTargetChanged(t, baseline.PCS, firstPCS)
	require.NoError(t, waitForWorkload2ReplicaAtGeneration(tc, 1, firstTarget.GenerationHash, 3))
	require.NoError(t, waitForStandaloneReplicaUnavailable(tc, "pc-a", 1))
	first := requireReplicaSnapshotForIndex(t, tc, 1)
	firstProgress := requireActiveReplicaRecreation(t, first.PCS)
	requireActiveReplacement(t, firstProgress, expectedReplicaReplacement{SourcePCSReplicaIndex: 2, TargetPCSReplicaIndex: 1})
	wantS1 := expectedReplicaConfiguration{
		StandaloneMinAvailable: 2, PCSGMinAvailable: 1, PCSGReplicas: 2,
		PodGangMap: updateutils.ExpectedReplicaPodGangMap{
			GenerationHash: firstTarget.GenerationHash, StandalonePodCounts: map[string]int32{"pc-a": 2},
			PCSGName: "sg-x", AnchorIndices: []int32{0}, TailIndices: []int32{1},
		},
	}
	assertReplicaConfiguration(t, first, wantS1)

	tests.Logger.Info("4. Supersede S1, observe the retired source handoff, and verify fresh S2 target resources")
	watchCtx, cancelWatch := context.WithTimeout(tc.Ctx, tc.Timeout)
	defer cancelWatch()
	pcsWatcher, err := watchPodCliqueSet(watchCtx, tc)
	require.NoError(t, err)
	defer pcsWatcher.Stop()
	require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 2))
	secondPCS, err := waitForDifferentIndexReplacementHandoff(watchCtx, pcsWatcher, first.PCS)
	require.NoError(t, err)
	secondTarget := requireSchedulingTargetChanged(t, first.PCS, secondPCS)
	handoffProgress := requireActiveReplacement(t, requireActiveReplicaRecreation(t, secondPCS), expectedReplicaReplacement{
		SourcePCSReplicaIndex: 1, TargetPCSReplicaIndex: 1,
	})
	require.Equal(t, []int32{2}, handoffProgress.Replacement.RetiredSourceReplicaIndices)
	require.Nil(t, handoffProgress.Replacement.SourceCleanupCompletedAt)
	require.Equal(t, secondPCS.Status.UpdateProgress.UpdateStartedAt, handoffProgress.UpdateStartedAt)
	requireCleanupAfterSupersession(t, tc, secondPCS)
	require.NoError(t, waitForWorkload2ReplicaAtGeneration(tc, 1, secondTarget.GenerationHash, 2))
	second := requireReplicaSnapshotForIndex(t, tc, 1)
	requireReplicaResourceUIDsChanged(t, first, second)
	wantS2 := wantS1
	wantS2.PCSGMinAvailable = 2
	wantS2.PodGangMap.GenerationHash = secondTarget.GenerationHash
	wantS2.PodGangMap.AnchorIndices = []int32{0, 1}
	wantS2.PodGangMap.TailIndices = nil
	assertReplicaConfiguration(t, second, wantS2)
	secondProgress := requireActiveReplacement(t, requireActiveReplicaRecreation(t, second.PCS), expectedReplicaReplacement{
		SourcePCSReplicaIndex: 1, TargetPCSReplicaIndex: 1,
	})
	require.Equal(t, []int32{2}, secondProgress.Replacement.RetiredSourceReplicaIndices)
	require.NotNil(t, secondProgress.Replacement.SourceCleanupCompletedAt)
	require.NoError(t, waitForStandaloneReplicaUnavailable(tc, "pc-a", 1))
	var heldSource grovev1beta1.PodClique
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(baseline.PCLQ), &heldSource))
	require.Equal(t, baseline.PCLQ.UID, heldSource.UID)
	require.NotNil(t, heldSource.DeletionTimestamp)
	if second.PCS.Status.ScalingProgress != nil {
		assert.NotContains(t, second.PCS.Status.ScalingProgress.ScaleInReplicaIndices, int32(2), "retirement must not become ordinary scale-in")
	}

	tests.Logger.Info("5. Finish retired-source cleanup while target 1 remains unavailable")
	require.NoError(t, releaseSource())
	require.NoError(t, waitForReplicaResourcesDeleted(tc, client.MatchingLabels{
		apicommon.LabelPartOfKey: tc.Workload.Name, apicommon.LabelPodCliqueSetReplicaIndex: "2",
	}))
	cleanupPCS, err := waitForReplacementRetirementCleared(tc, 1, secondTarget.GenerationHash)
	require.NoError(t, err)
	requireActiveReplicaRecreation(t, cleanupPCS)
	assert.Equal(t, baseline.PCS.Status.AppliedSchedulingConfigurationHash, cleanupPCS.Status.AppliedSchedulingConfigurationHash)
	require.NoError(t, waitForStandaloneReplicaUnavailable(tc, "pc-a", 1))

	tests.Logger.Info("6. Release readiness and verify S2 completion, stable target UIDs, and exact gang membership")
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Name))
	blockRemoved = true
	updateutils.DeleteNotReadyPodsOnReplica(t, tc, 1)
	requireReplicaRecreationConverged(t, tc, expectedReplicaRecreationCompletion{
		GenerationHash: secondTarget.GenerationHash, SchedulingHash: secondTarget.SchedulingHash,
		Replicas: 2, Pods: 20, PCSReplicaIndices: []int{0, 1}, Configuration: wantS2,
	})
	finalTarget := requireReplicaSnapshotForIndex(t, tc, 1)
	assertReplicaConfiguration(t, finalTarget, wantS2)
	assertReplicaResourceUIDsUnchanged(t, second, finalTarget)
}

// Test_RR17_ReplicaRecreationBeforeFirstCompletedReconcile verifies that persisted replacement work is
// honored before the PCS has completed its first spec reconciliation. The fixture models an interruption
// after hash initialization and child creation but before ObservedGeneration is written. It preserves the
// current and applied scheduling hashes so this test does not exercise legacy baseline initialization.
//
// The operator is stopped while the fixture status and scheduling edit are installed. Admission is
// temporarily exempted only for the test PCS because the stopped operator has no webhook endpoints.
// Admission is restored before restart, and the controller must select and execute the replacement itself.
//
// Scenario:
//  1. Deploy workload2 and capture replica 0's original resources and initialized scheduling baseline.
//  2. Block fresh standalone pod readiness, stop the operator, and clear only ObservedGeneration.
//     Verify the hashes, specification, resource UIDs, and ordinary scaling progress are unchanged.
//  3. Increase sg-x MinAvailable 1 -> 2 while stopped, restore admission, and resume the operator.
//  4. Require a persisted same-index source-cleanup barrier while ObservedGeneration remains nil.
//  5. Verify fresh PodClique, PCSG, and PodGangMap UIDs and the new sg-x anchor containing indices 0
//     and 1 with no tail. Blocked readiness must keep recreation active and the scheduling target unapplied.
//  6. Hold the fresh target's deletion and scale to zero while readiness is still blocked. Require the
//     controller to record cancellation while ObservedGeneration stays nil and preserve source cleanup.
//  7. Release deletion without releasing readiness. Require all replica resources to disappear, recreation
//     to complete, and the first successful spec reconciliation to record ObservedGeneration at zero replicas.
//  8. Release readiness and scale to one. Verify 10 Ready pods with the latest scheduling configuration,
//     new resource UIDs, no new update attempt, and observation of the final metadata generation.
func Test_RR17_ReplicaRecreationBeforeFirstCompletedReconcile(t *testing.T) {
	tests.Logger.Info("1. Deploy workload2 and capture replica 0's resources and scheduling baseline")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload2",
		workloadYAML: "../../yaml/workload2.yaml",
		workerNodes:  10,
		expectedPods: 10,
	})
	t.Cleanup(cleanup)
	updateutils.AssertGenerationHashConverged(tc)
	before := requireReplicaSnapshot(t, tc)
	require.NotNil(t, before.PCS.Status.ObservedGeneration)
	require.Equal(t, before.PCS.Generation, *before.PCS.Status.ObservedGeneration)
	require.Nil(t, before.PCS.Status.UpdateProgress, "the fixture must start without a recorded update")
	require.Nil(t, before.PCS.Status.ScalingProgress, "the fixture must not contain ordinary scaling work")
	want := expectedReplicaConfiguration{
		StandaloneMinAvailable: 1, PCSGMinAvailable: 1, PCSGReplicas: 2,
		PodGangMap: updateutils.ExpectedReplicaPodGangMap{
			GenerationHash:      *before.PCS.Status.CurrentGenerationHash,
			StandalonePodCounts: map[string]int32{"pc-a": 2},
			PCSGName:            "sg-x", AnchorIndices: []int32{0}, TailIndices: []int32{1},
		},
	}
	assertReplicaConfiguration(t, before, want)

	tests.Logger.Info("2. Stop the operator and simulate interrupted provisioning by clearing only ObservedGeneration")
	operatorKey := client.ObjectKey{Namespace: setup.OperatorNamespace, Name: setup.OperatorDeploymentName}
	var deployment appsv1.Deployment
	require.NoError(t, tc.Client.Get(tc.Ctx, operatorKey, &deployment))
	require.NotNil(t, deployment.Spec.Replicas)
	operatorReplicas := *deployment.Spec.Replicas
	require.Positive(t, operatorReplicas)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(tc.Ctx), tc.Timeout)
		defer cancel()
		assert.NoError(t, setCoherentTestOperatorReplicas(ctx, tc, operatorReplicas))
	})
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path))
	blockRemoved := false
	t.Cleanup(func() {
		if !blockRemoved {
			assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
		}
	})
	updateutils.ExemptPCSFromAdmissionForUpgradeFixture(t, tc)
	require.NoError(t, setCoherentTestOperatorReplicas(tc.Ctx, tc, 0))
	pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	originalPCS := pcs.DeepCopy()
	pcs.Status.ObservedGeneration = nil
	require.NoError(t, tc.Client.Status().Patch(tc.Ctx, pcs, client.MergeFrom(originalPCS)))
	unobserved := requireReplicaSnapshot(t, tc)
	require.Nil(t, unobserved.PCS.Status.ObservedGeneration)
	require.Equal(t, before.PCS.Status.CurrentGenerationHash, unobserved.PCS.Status.CurrentGenerationHash)
	require.Equal(t, before.PCS.Status.AppliedSchedulingConfigurationHash, unobserved.PCS.Status.AppliedSchedulingConfigurationHash)
	require.Equal(t, before.PCS.Spec, unobserved.PCS.Spec)
	require.Equal(t, before.PCS.Generation, unobserved.PCS.Generation)
	require.Nil(t, unobserved.PCS.Status.UpdateProgress)
	require.Nil(t, unobserved.PCS.Status.ScalingProgress)
	assertReplicaResourceUIDsUnchanged(t, before, unobserved)

	tests.Logger.Info("3. Edit sg-x MinAvailable while stopped, restore admission, and resume reconciliation")
	require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 2))
	editedPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	require.Nil(t, editedPCS.Status.ObservedGeneration)
	require.Equal(t, unobserved.PCS.Generation+1, editedPCS.Generation)
	require.Equal(t, unobserved.PCS.Spec.Replicas, editedPCS.Spec.Replicas, "the scheduling edit must not scale the PCS")
	require.Equal(t, unobserved.PCS.Status.CurrentGenerationHash, editedPCS.Status.CurrentGenerationHash)
	require.Equal(t, unobserved.PCS.Status.AppliedSchedulingConfigurationHash, editedPCS.Status.AppliedSchedulingConfigurationHash)
	require.Nil(t, editedPCS.Status.UpdateProgress, "replacement selection must be performed by the controller")
	updateutils.RestorePCSAdmissionAfterUpgradeFixture(t, tc)
	require.NoError(t, setCoherentTestOperatorReplicas(tc.Ctx, tc, operatorReplicas))

	tests.Logger.Info("4. Require source cleanup to complete without acknowledging the first reconciliation")
	cleanupPCS, err := waitForSameIndexReplacementCleanup(tc, 0)
	require.NoError(t, err, "an unobserved PCS must execute replacement source cleanup instead of retaining the source")
	require.Nil(t, cleanupPCS.Status.ObservedGeneration, "source cleanup must not acknowledge an unfinished recreation")
	require.Nil(t, cleanupPCS.Status.ScalingProgress, "replacement cleanup must not become ordinary scaling work")
	target := requireSchedulingTargetChanged(t, unobserved.PCS, cleanupPCS)
	cleanupProgress := requireActiveReplacement(t, cleanupPCS.Status.UpdateProgress, expectedReplicaReplacement{
		SourcePCSReplicaIndex: 0, TargetPCSReplicaIndex: 0,
	})

	tests.Logger.Info("5. Verify fresh replacement resources and the new gang layout while readiness stays blocked")
	want.PCSGMinAvailable = 2
	want.PodGangMap.GenerationHash = target.GenerationHash
	want.PodGangMap.AnchorIndices = []int32{0, 1}
	want.PodGangMap.TailIndices = nil
	recreated := requireRecreatedReplica(t, tc, unobserved, want)
	require.Nil(t, recreated.PCS.Status.ObservedGeneration, "an unavailable replacement must keep the first reconciliation incomplete")
	require.Nil(t, recreated.PCS.Status.ScalingProgress)
	recreatedProgress := requireActiveReplacement(t, recreated.PCS.Status.UpdateProgress, expectedReplicaReplacement{
		SourcePCSReplicaIndex: 0, TargetPCSReplicaIndex: 0,
	})
	require.Equal(t, cleanupProgress.Replacement.SourceCleanupCompletedAt, recreatedProgress.Replacement.SourceCleanupCompletedAt,
		"target provisioning must preserve the confirmed source-cleanup barrier")

	tests.Logger.Info("6. Hold target deletion and require controller cancellation while readiness remains blocked")
	releaseTarget, err := holdReplicaSourceCleanup(tc, recreated.PCLQ)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, releaseTarget()) })
	watchCtx, cancelWatch := context.WithTimeout(tc.Ctx, tc.Timeout)
	defer cancelWatch()
	watcher, err := watchPodCliqueSet(watchCtx, tc)
	require.NoError(t, err)
	defer watcher.Stop()
	require.NoError(t, tc.ScalePCS(tc.Workload.Name, 0))
	cancellationMessage, err := waitForCanceledReplacement(watchCtx, watcher, 0)
	require.NoError(t, err)
	require.NotEmpty(t, cancellationMessage)
	require.NoError(t, waitForReplicaSourceDeletion(tc, recreated.PCLQ))
	canceledPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	require.Nil(t, canceledPCS.Status.ObservedGeneration, "cancellation must not acknowledge an incomplete spec reconciliation")
	require.Nil(t, canceledPCS.Status.ScalingProgress, "replacement cancellation must not become ordinary scale-in")
	canceledProgress := requireActiveReplicaRecreation(t, canceledPCS)
	require.Len(t, canceledProgress.CurrentlyUpdating, 1)
	canceled := canceledProgress.CurrentlyUpdating[0]
	require.NotNil(t, canceled.Replacement.CanceledAt)
	require.Nil(t, canceled.UpdateEndedAt)
	require.Equal(t, recreatedProgress.Replacement.SourceCleanupCompletedAt, canceled.Replacement.SourceCleanupCompletedAt)
	require.Equal(t, unobserved.PCS.Status.AppliedSchedulingConfigurationHash, canceledPCS.Status.AppliedSchedulingConfigurationHash)

	tests.Logger.Info("7. Release deletion and complete the first spec reconciliation at zero without releasing readiness")
	require.NoError(t, releaseTarget())
	require.NoError(t, waitForReplicaResourcesDeleted(tc, client.MatchingLabels{apicommon.LabelPartOfKey: tc.Workload.Name}))
	require.NoError(t, waitForReplicaRecreationComplete(tc, 0))
	require.NoError(t, wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
		if err != nil {
			return false, err
		}
		return pcs.Status.ObservedGeneration != nil && *pcs.Status.ObservedGeneration == pcs.Generation, nil
	}), "zero replicas must allow the first completed reconciliation while target readiness remains blocked")
	zeroPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	assertReplicaRecreationCompleted(t, zeroPCS, expectedReplicaRecreationCompletion{
		GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash, Replicas: 0,
	})
	require.Equal(t, editedPCS.Generation+1, zeroPCS.Generation)
	require.Nil(t, zeroPCS.Status.ScalingProgress)
	require.NoError(t, waitForClaimedPCSReplicaIndices(tc, []int{}))

	tests.Logger.Info("8. Release readiness and scale to one using the completed scheduling configuration")
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
	blockRemoved = true
	require.NoError(t, tc.ScalePCS(tc.Workload.Name, 1))
	requireReplicaRecreationConverged(t, tc, expectedReplicaRecreationCompletion{
		GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash,
		Replicas: 1, Pods: 10, PCSReplicaIndices: []int{0}, Configuration: want,
	})
	require.NoError(t, wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
		if err != nil {
			return false, err
		}
		return pcs.Status.ObservedGeneration != nil && *pcs.Status.ObservedGeneration == pcs.Generation, nil
	}), "ordinary scale-out must complete the final spec reconciliation")
	final := requireReplicaSnapshot(t, tc)
	require.Equal(t, zeroPCS.Generation+1, final.PCS.Generation)
	require.Equal(t, final.PCS.Generation, *final.PCS.Status.ObservedGeneration)
	require.Equal(t, zeroPCS.Status.UpdateProgress.UpdateStartedAt, final.PCS.Status.UpdateProgress.UpdateStartedAt)
	require.Equal(t, zeroPCS.Status.UpdateProgress.UpdateEndedAt, final.PCS.Status.UpdateProgress.UpdateEndedAt)
	requireReplicaResourceUIDsChanged(t, recreated, final)
}

// Test_RR18_CompletedReplacementLossDoesNotDelayUpdate verifies that recorded replacement completion
// remains final when a target loses a child resource. A completed source index may subsequently identify a
// different replacement's target without making that new attempt complete. Ordinary S1 scale-out must remain
// separate from update progress even when only some of its workload resources have been created.
//
// The configured strategy is Coherent, but the scheduling change executes replica recreation. Workload2's
// memory requests are reduced to 40Mi so its peak of 40 pods fits on the existing 30-worker test cluster.
// A ResourceQuota holds PCSG creation, not merely pod readiness, to keep resource repair and scale-out
// deterministically incomplete while the final original replacement finishes.
//
// Scenario:
//  1. Deploy four Ready replicas at indices 0, 1, 2, and 3. Make replica 1 unavailable and scale in to three,
//     leaving original sources 0, 2, and 3.
//  2. Increase pc-a MinAvailable 1 -> 2. Allow 0 -> 0 and 2 -> 1 to complete, then hold fresh standalone
//     readiness for 3 -> 2. Verify target 2 has fresh S1 resources and remains unfinished despite source 2's
//     earlier completion record.
//  3. Limit PCSG count to two after all three replacement targets have materialized. Delete completed
//     target 1's PCSG and scale out to four replicas. Active source 3 and target 2 remain reserved, so ordinary
//     scale-out uses index 4. Its standalone exists at S1 while its PCSG cannot be created.
//  4. Release target 2 readiness without removing the quota. Validate every watched PCS status preserves
//     completed selections until overall completion. Require the scheduling update to finish while target 1
//     and scale-out 4 still lack PCSGs, with no update selection for the fresh scale-out.
//  5. Remove the quota. Verify ordinary synchronization repairs target 1 and completes scale-out 4, yielding
//     40 Ready pods at indices 0, 1, 2, and 4 with exact S1 gang membership and stable surviving resource UIDs.
func Test_RR18_CompletedReplacementLossDoesNotDelayUpdate(t *testing.T) {
	tests.Logger.Info("1. Deploy four replicas with smaller requests and scale in unavailable replica 1")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload2", workloadYAML: "../../yaml/workload2.yaml",
		workerNodes: 30, expectedPods: 10,
	})
	t.Cleanup(cleanup)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(tc.Ctx), tc.Timeout)
		defer cancel()
		assert.NoError(t, kwok.DeleteStage(ctx, tc.Client, kwokStageReadyBlockedR1Name))
		assert.NoError(t, kwok.DeleteStage(ctx, tc.Client, kwokStageReadyBlockedR2Name))
	})
	require.NoError(t, updateutils.UpdatePCSUpdateStrategy(tc, grovev1beta1.CoherentStrategy))
	pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	base := pcs.DeepCopy()
	for _, clique := range pcs.Spec.Template.Cliques {
		for i := range clique.Spec.PodSpec.Containers {
			clique.Spec.PodSpec.Containers[i].Resources.Requests[corev1.ResourceMemory] = resource.MustParse("40Mi")
		}
	}
	require.NoError(t, tc.Client.Patch(tc.Ctx, pcs, client.MergeFrom(base)))
	require.NoError(t, updateutils.WaitForRollingUpdateComplete(tc, 1))
	tc.ScalePCSAndWait(tc.Workload.Name, 4, 40, 0)
	updateutils.AssertGenerationHashConverged(tc)
	require.NoError(t, waitForClaimedPCSReplicaIndices(tc, []int{0, 1, 2, 3}))
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Path))
	require.NoError(t, deletePodsForReplica(tc, 1))
	require.NoError(t, waitForStandaloneReplicaUnavailable(tc, "pc-a", 1))
	require.NoError(t, tc.ScalePCS(tc.Workload.Name, 3))
	require.NoError(t, waitForClaimedPCSReplicaIndices(tc, []int{0, 2, 3}))
	require.NoError(t, waitForReplicaResourcesDeleted(tc, client.MatchingLabels{
		apicommon.LabelPartOfKey: tc.Workload.Name, apicommon.LabelPodCliqueSetReplicaIndex: "1",
	}))
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR1Name))
	baseline := requireReplicaSnapshotForIndex(t, tc, 2)

	tests.Logger.Info("2. Complete 2 -> 1 and hold the distinct replacement 3 -> 2 at fresh target readiness")
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR2Path))
	require.NoError(t, triggerStandalonePCLQMinAvailableChange(tc, "pc-a", 2))
	sourcePCSReplicaIndex, err := waitForActiveReplacementTargetingIndex(tc, 2)
	require.NoError(t, err)
	require.Equal(t, int32(3), sourcePCSReplicaIndex)
	blockedPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	target := requireSchedulingTargetChanged(t, baseline.PCS, blockedPCS)
	require.NoError(t, waitForWorkload2ReplicaAtGeneration(tc, 2, target.GenerationHash, 3))
	require.NoError(t, waitForStandaloneReplicaUnavailable(tc, "pc-a", 2))
	require.NoError(t, waitForReplicaResourcesDeleted(tc, client.MatchingLabels{
		apicommon.LabelPartOfKey: tc.Workload.Name, apicommon.LabelPodCliqueSetReplicaIndex: "3",
	}))
	lastTarget := requireReplicaSnapshotForIndex(t, tc, 2)
	completedTarget := requireReplicaSnapshotForIndex(t, tc, 1)
	blockedPCS = lastTarget.PCS
	progress := requireActiveReplicaRecreation(t, blockedPCS)
	require.Len(t, progress.CurrentlyUpdating, 3)
	requireActiveReplacement(t, progress, expectedReplicaReplacement{SourcePCSReplicaIndex: 3, TargetPCSReplicaIndex: 2})
	firstCompleted := findPCSReplicaUpdateSelection(progress, 0)
	require.NotNil(t, firstCompleted)
	require.NotNil(t, firstCompleted.UpdateEndedAt)
	require.NotNil(t, firstCompleted.Replacement)
	require.Equal(t, int32(0), firstCompleted.Replacement.TargetReplicaIndex)
	completed := findPCSReplicaUpdateSelection(progress, 2)
	require.NotNil(t, completed)
	require.NotNil(t, completed.UpdateEndedAt)
	require.NotNil(t, completed.Replacement)
	require.Equal(t, int32(1), completed.Replacement.TargetReplicaIndex)
	require.Nil(t, completed.Replacement.CanceledAt)
	requireReplicaResourceUIDsChanged(t, baseline, lastTarget)
	want := expectedReplicaConfiguration{
		StandaloneMinAvailable: 2, PCSGMinAvailable: 1, PCSGReplicas: 2,
		PodGangMap: updateutils.ExpectedReplicaPodGangMap{
			GenerationHash: target.GenerationHash, StandalonePodCounts: map[string]int32{"pc-a": 2},
			PCSGName: "sg-x", AnchorIndices: []int32{0}, TailIndices: []int32{1},
		},
	}
	assertReplicaConfiguration(t, completedTarget, want)
	assertReplicaConfiguration(t, lastTarget, want)

	tests.Logger.Info("3. Hold PCSG repair and ordinary scale-out creation while retaining completed history")
	const pcsgCountResource corev1.ResourceName = "count/podcliquescalinggroups.grove.io"
	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "workload2-rr18-pcsg-creation-hold", Namespace: tc.Namespace},
		Spec:       corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{pcsgCountResource: resource.MustParse("2")}},
	}
	require.NoError(t, tc.Client.Create(tc.Ctx, quota))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(tc.Ctx), tc.Timeout)
		defer cancel()
		assert.NoError(t, client.IgnoreNotFound(tc.Client.Delete(ctx, quota)))
	})
	require.NoError(t, wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		var current corev1.ResourceQuota
		if err := tc.Client.Get(ctx, client.ObjectKeyFromObject(quota), &current); err != nil {
			return false, err
		}
		used := current.Status.Used[pcsgCountResource]
		return used.Value() == 3, nil
	}), "quota must account for all existing PCSGs before child loss")
	watchCtx, cancelWatch := context.WithTimeout(tc.Ctx, tc.Timeout)
	defer cancelWatch()
	pcsWatcher, err := watchPodCliqueSet(watchCtx, tc)
	require.NoError(t, err)
	defer pcsWatcher.Stop()
	require.NoError(t, tc.Client.Delete(tc.Ctx, completedTarget.PCSG))
	require.NoError(t, tc.ScalePCS(tc.Workload.Name, 4))
	freshReplica := apicommon.ResourceNameReplica{Name: tc.Workload.Name, Replica: 4}
	freshPCLQKey := client.ObjectKey{Namespace: tc.Namespace, Name: apicommon.GeneratePodCliqueName(freshReplica, "pc-a")}
	missingPCSGKeys := []client.ObjectKey{
		client.ObjectKeyFromObject(completedTarget.PCSG),
		{Namespace: tc.Namespace, Name: updateutils.PCSGFQNForReplica(tc, "sg-x", 4)},
	}
	require.NoError(t, wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		var freshPCLQ grovev1beta1.PodClique
		if err := tc.Client.Get(ctx, freshPCLQKey, &freshPCLQ); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		if freshPCLQ.Labels[apicommon.LabelPodCliqueSetGenerationHash] != target.GenerationHash {
			return false, fmt.Errorf("fresh scale-out replica 4 has generation %q, want %q", freshPCLQ.Labels[apicommon.LabelPodCliqueSetGenerationHash], target.GenerationHash)
		}
		for _, key := range missingPCSGKeys {
			var pcsg grovev1beta1.PodCliqueScalingGroup
			if err := tc.Client.Get(ctx, key, &pcsg); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return false, err
			}
			return false, nil
		}
		return true, nil
	}), "completed target 1 and fresh scale-out 4 must both lack PCSGs")
	partialPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	requireActiveReplacement(t, requireActiveReplicaRecreation(t, partialPCS), expectedReplicaReplacement{SourcePCSReplicaIndex: 3, TargetPCSReplicaIndex: 2})
	require.NotNil(t, partialPCS.Status.ScalingProgress)
	require.Contains(t, partialPCS.Status.ScalingProgress.ScaleOutReplicaIndices, int32(4))
	require.Nil(t, findPCSReplicaUpdateSelection(partialPCS.Status.UpdateProgress, 4))

	tests.Logger.Info("4. Complete the remaining original replacement without releasing PCSG creation")
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR2Name))
	updateutils.DeleteNotReadyPodsOnReplica(t, tc, 2)
	finishedPCS, err := waitForReplicaRecreationCompletionPreservingCompletedSelections(watchCtx, pcsWatcher, blockedPCS, 4)
	require.NoError(t, err)
	require.Equal(t, target.SchedulingHash, *finishedPCS.Status.AppliedSchedulingConfigurationHash)
	require.Equal(t, int32(4), finishedPCS.Spec.Replicas)
	require.Empty(t, finishedPCS.Status.UpdateProgress.CurrentlyUpdating)
	for _, key := range missingPCSGKeys {
		var missing grovev1beta1.PodCliqueScalingGroup
		require.True(t, apierrors.IsNotFound(tc.Client.Get(tc.Ctx, key, &missing)), "PCSG %s must still be absent at update completion", key)
	}
	var freshPCLQ grovev1beta1.PodClique
	require.NoError(t, tc.Client.Get(tc.Ctx, freshPCLQKey, &freshPCLQ))
	require.Equal(t, target.GenerationHash, freshPCLQ.Labels[apicommon.LabelPodCliqueSetGenerationHash])

	tests.Logger.Info("5. Release creation, repair the completed target, and converge to four Ready replicas")
	require.NoError(t, tc.Client.Delete(tc.Ctx, quota))
	requireReplicaRecreationConverged(t, tc, expectedReplicaRecreationCompletion{
		GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash,
		Replicas: 4, Pods: 40, PCSReplicaIndices: []int{0, 1, 2, 4}, Configuration: want,
	})
	repaired := requireReplicaSnapshotForIndex(t, tc, 1)
	require.NotEqual(t, completedTarget.PCSG.UID, repaired.PCSG.UID)
	require.Equal(t, completedTarget.PCLQ.UID, repaired.PCLQ.UID)
	require.Equal(t, completedTarget.PGM.UID, repaired.PGM.UID)
	assertReplicaResourceUIDsUnchanged(t, lastTarget, requireReplicaSnapshotForIndex(t, tc, 2))
	require.Equal(t, freshPCLQ.UID, requireReplicaSnapshotForIndex(t, tc, 4).PCLQ.UID)
}

// Test_RR19_ServiceAccountRepairProgressesPastUninitializedReplica verifies that an unrelated
// replica's failed pod creation cannot block source-cleanup acknowledgment for the selected replacement.
// Both sources start with a nonexistent service account. One edit repairs that reference and increases
// MinAvailable. Target 0 must create repaired pods while its readiness is held and source 1 stays invalid.
//
// Scenario:
//  1. Deploy two PCS replicas referencing a nonexistent service account. Require API-server pod-creation
//     rejections on both PodCliques, no pods, and an initialized scheduling baseline.
//  2. Block target 0 readiness and repair the account reference while increasing worker MinAvailable
//     from 1 to 2 in one patch. Require source-cleanup acknowledgment and fresh target resources.
//  3. Verify repaired pods exist only on target 0 and remain NotReady. Source 1 must retain its original
//     UID and invalid template while the replacement remains active and the scheduling target unapplied.
//  4. Release readiness and verify both replicas converge to four Ready pods at the repaired configuration,
//     the scheduling target becomes applied, and target 0 retains its replacement resource UIDs.
func Test_RR19_ServiceAccountRepairProgressesPastUninitializedReplica(t *testing.T) {
	const missingServiceAccount = "rr19-missing"
	tc, cleanup := testctx.PrepareTest(context.Background(), t, 10, testctx.WithWorkload(&testctx.WorkloadConfig{
		Name: "workload-recreation-standalone", YAMLPath: "../../yaml/workload-recreation-standalone.yaml",
		Namespace: "default", ExpectedPods: 4,
	}))
	t.Cleanup(cleanup)
	manifest, err := os.ReadFile(tc.Workload.YAMLPath)
	require.NoError(t, err)
	var pcs grovev1beta1.PodCliqueSet
	require.NoError(t, yaml.Unmarshal(manifest, &pcs))
	pcs.Namespace = tc.Namespace
	pcs.Spec.Replicas = 2
	pcs.Spec.UpdateStrategy = &grovev1beta1.PodCliqueSetUpdateStrategy{Type: grovev1beta1.RollingRecreateStrategy}
	pcs.Spec.Template.Cliques[0].Spec.PodSpec.ServiceAccountName = missingServiceAccount
	var serviceAccount corev1.ServiceAccount
	require.True(t, apierrors.IsNotFound(tc.Client.Get(tc.Ctx, client.ObjectKey{Namespace: tc.Namespace, Name: missingServiceAccount}, &serviceAccount)))
	require.NoError(t, tc.Client.Create(tc.Ctx, &pcs))

	tests.Logger.Info("1. Observe real pod-creation rejection on both uninitialized replicas")
	before := make([]grovev1beta1.PodClique, 2)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		for pcsReplicaIndex := range 2 {
			key := client.ObjectKey{Namespace: tc.Namespace, Name: apicommon.GeneratePodCliqueName(
				apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}, "worker",
			)}
			require.NoError(c, tc.Client.Get(tc.Ctx, key, &before[pcsReplicaIndex]))
			pclq := before[pcsReplicaIndex]
			assert.Nil(c, pclq.Status.LastScheduled)
			assert.True(c, slices.ContainsFunc(pclq.Status.LastErrors, func(lastError grovev1beta1.LastError) bool {
				return strings.Contains(lastError.Description, missingServiceAccount) &&
					strings.Contains(lastError.Description, "forbidden") && strings.Contains(lastError.Description, "not found")
			}), "replica %d must report the API-server service-account rejection: %+v", pcsReplicaIndex, pclq.Status.LastErrors)
		}
	}, tc.Timeout, tc.Interval)
	beforePCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	require.NotNil(t, beforePCS.Status.CurrentGenerationHash)
	require.NotNil(t, beforePCS.Status.AppliedSchedulingConfigurationHash)
	require.Nil(t, beforePCS.Status.UpdateProgress)
	initialPods, err := tc.ListPods()
	require.NoError(t, err)
	require.Empty(t, initialPods.Items)
	beforePGM, err := updateutils.GetReplicaPodGangMap(tc, 0)
	require.NoError(t, err)

	tests.Logger.Info("2. Hold target readiness and atomically repair the account reference and minimum")
	require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path))
	t.Cleanup(func() { assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name)) })
	base := beforePCS.DeepCopy()
	beforePCS.Spec.Template.Cliques[0].Spec.PodSpec.ServiceAccountName = "default"
	beforePCS.Spec.Template.Cliques[0].Spec.MinAvailable = new(int32(2))
	require.NoError(t, tc.Client.Patch(tc.Ctx, beforePCS, client.MergeFrom(base)))
	cleanupPCS, err := waitForSameIndexReplacementCleanup(tc, 0)
	require.NoError(t, err, "uninitialized source 1 must not block source 0 cleanup acknowledgment")
	target := requireSchedulingTargetChanged(t, base, cleanupPCS)
	freshPCLQ, err := waitForRecreatedPCLQ(tc, &before[0])
	require.NoError(t, err)
	freshPGM, err := waitForRecreatedPodGangMap(tc, beforePGM, target.GenerationHash, 1)
	require.NoError(t, err)

	tests.Logger.Info("3. Require repaired target pods while the unselected source still cannot create any")
	heldPods, err := tc.WaitForPodCount(2)
	require.NoError(t, err)
	for _, pod := range heldPods.Items {
		require.Equal(t, "0", pod.Labels[apicommon.LabelPodCliqueSetReplicaIndex])
		require.Equal(t, "default", pod.Spec.ServiceAccountName)
		require.False(t, kubeutils.IsPodReady(&pod))
	}
	var untouchedSource grovev1beta1.PodClique
	require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(&before[1]), &untouchedSource))
	require.Equal(t, before[1].UID, untouchedSource.UID)
	require.Equal(t, before[1].Spec, untouchedSource.Spec)
	require.Nil(t, untouchedSource.Status.LastScheduled)
	heldPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	progress := requireActiveReplicaRecreation(t, heldPCS)
	require.Len(t, progress.CurrentlyUpdating, 1)
	selection := requireActiveReplacement(t, progress, expectedReplicaReplacement{SourcePCSReplicaIndex: 0, TargetPCSReplicaIndex: 0})
	require.NotNil(t, selection.Replacement.SourceCleanupCompletedAt)
	require.Equal(t, base.Status.AppliedSchedulingConfigurationHash, heldPCS.Status.AppliedSchedulingConfigurationHash)
	require.Equal(t, "default", freshPCLQ.Spec.PodSpec.ServiceAccountName)
	require.Equal(t, new(int32(2)), freshPCLQ.Spec.MinAvailable)

	tests.Logger.Info("4. Release readiness and require the entire repair to converge")
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
	updateutils.DeleteNotReadyPodsOnReplica(t, tc, 0)
	require.NoError(t, waitForReplicaRecreationComplete(tc, 2))
	require.NoError(t, tc.WaitForPods(4))
	finishedPCS, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	assertReplicaRecreationCompleted(t, finishedPCS, expectedReplicaRecreationCompletion{
		GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash, Replicas: 2,
	})
	for pcsReplicaIndex := range 2 {
		var pclq grovev1beta1.PodClique
		require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(&before[pcsReplicaIndex]), &pclq))
		require.NotEqual(t, before[pcsReplicaIndex].UID, pclq.UID)
		require.Equal(t, "default", pclq.Spec.PodSpec.ServiceAccountName)
		require.Equal(t, new(int32(2)), pclq.Spec.MinAvailable)
		if pcsReplicaIndex == 0 {
			require.Equal(t, freshPCLQ.UID, pclq.UID)
		}
	}
	finalPGM, err := updateutils.GetReplicaPodGangMap(tc, 0)
	require.NoError(t, err)
	require.Equal(t, freshPGM.UID, finalPGM.UID)
	updateutils.AssertGenerationHashConverged(tc)
	updateutils.AssertNoActiveChildUpdates(tc)
}

// Test_RR20_MinAvailableChangeAtZeroReplicasAllowsScaleOut verifies that a scheduling edit made
// after complete scale-down can finish immediately without stranding generation expectations.
// Subsequent scale-out must succeed in the same operator process without opening another update.
//
// Scenario:
//  1. Deploy one Ready workload2 PCS replica and configure RollingRecreate, Coherent, or OnDelete.
//     Capture the operator identity, scale to zero, and wait for all replica resources and scaling progress
//     to disappear and the PCS generation to be observed. No update may be recorded yet.
//  2. Increase sg-x MinAvailable from 1 to 2 while no replicas or cleanup work remain. Require completion
//     at zero replicas with the new scheduling hash applied and no retained selections or scaling progress.
//  3. Scale back to one and verify ten Ready pods, the new minimum and gang layout, and unchanged update
//     start and completion timestamps. The operator pod UID and container ID must remain unchanged.
func Test_RR20_MinAvailableChangeAtZeroReplicasAllowsScaleOut(t *testing.T) {
	for _, strategy := range []grovev1beta1.UpdateStrategyType{grovev1beta1.RollingRecreateStrategy, grovev1beta1.CoherentStrategy, grovev1beta1.OnDeleteStrategy} {
		t.Run(string(strategy), func(t *testing.T) {
			tc, cleanup, _ := setupTest(t, testConfig{
				workloadName: "workload2", workloadYAML: "../../yaml/workload2.yaml", workerNodes: 10, expectedPods: 10,
			})
			t.Cleanup(cleanup)
			require.NoError(t, updateutils.UpdatePCSUpdateStrategy(tc, strategy))
			var operatorPods corev1.PodList
			require.NoError(t, tc.Client.List(tc.Ctx, &operatorPods, client.InNamespace(setup.OperatorNamespace), setup.OperatorPodLabels))
			require.Len(t, operatorPods.Items, 1)
			operatorBefore := operatorPods.Items[0].DeepCopy()

			tests.Logger.Info("1. Fully scale down the initialized PCS before changing its scheduling configuration")
			require.NoError(t, tc.ScalePCS(tc.Workload.Name, 0))
			require.NoError(t, waitForReplicaResourcesDeleted(tc, client.MatchingLabels{apicommon.LabelPartOfKey: tc.Workload.Name}))
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
				require.NoError(c, err)
				assert.Equal(c, new(pcs.Generation), pcs.Status.ObservedGeneration)
				assert.Nil(c, pcs.Status.ScalingProgress)
				assert.Zero(c, pcs.Status.Replicas)
			}, tc.Timeout, tc.Interval)
			before, err := updateutils.GetPCS(tc, tc.Workload.Name)
			require.NoError(t, err)
			require.NotNil(t, before.Status.CurrentGenerationHash)
			require.NotNil(t, before.Status.AppliedSchedulingConfigurationHash)
			require.Nil(t, before.Status.UpdateProgress)

			tests.Logger.Info("2. Change MinAvailable with no replicas or cleanup work remaining")
			require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 2))
			require.NoError(t, updateutils.WaitForGenerationHashChange(tc, *before.Status.CurrentGenerationHash))
			require.NoError(t, waitForReplicaRecreationComplete(tc, 0))
			completed, err := updateutils.GetPCS(tc, tc.Workload.Name)
			require.NoError(t, err)
			require.NotNil(t, completed.Status.UpdateProgress)
			target := recreationTarget{GenerationHash: *completed.Status.CurrentGenerationHash, SchedulingHash: completed.Status.UpdateProgress.TargetSchedulingConfigurationHash}
			require.NotEqual(t, *before.Status.AppliedSchedulingConfigurationHash, target.SchedulingHash)
			assertReplicaRecreationCompleted(t, completed, expectedReplicaRecreationCompletion{
				GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash, Replicas: 0,
			})
			require.Nil(t, completed.Status.ScalingProgress)

			tests.Logger.Info("3. Scale out without restarting the operator or resetting its expectations")
			require.NoError(t, tc.ScalePCS(tc.Workload.Name, 1))
			want := expectedReplicaConfiguration{
				StandaloneMinAvailable: 1, PCSGMinAvailable: 2, PCSGReplicas: 2,
				PodGangMap: updateutils.ExpectedReplicaPodGangMap{
					GenerationHash: target.GenerationHash, StandalonePodCounts: map[string]int32{"pc-a": 2},
					PCSGName: "sg-x", AnchorIndices: []int32{0, 1},
				},
			}
			requireReplicaRecreationConverged(t, tc, expectedReplicaRecreationCompletion{
				GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash,
				Replicas: 1, Pods: 10, PCSReplicaIndices: []int{0}, Configuration: want,
			})
			final, err := updateutils.GetPCS(tc, tc.Workload.Name)
			require.NoError(t, err)
			require.Equal(t, completed.Status.UpdateProgress.UpdateStartedAt, final.Status.UpdateProgress.UpdateStartedAt)
			require.Equal(t, completed.Status.UpdateProgress.UpdateEndedAt, final.Status.UpdateProgress.UpdateEndedAt)
			require.Equal(t, new(final.Generation), final.Status.ObservedGeneration)
			require.NoError(t, tc.Client.List(tc.Ctx, &operatorPods, client.InNamespace(setup.OperatorNamespace), setup.OperatorPodLabels))
			require.Len(t, operatorPods.Items, 1)
			require.Equal(t, operatorBefore.UID, operatorPods.Items[0].UID, "operator restart would erase the expectation under test")
			require.Len(t, operatorBefore.Status.ContainerStatuses, 1)
			require.Len(t, operatorPods.Items[0].Status.ContainerStatuses, 1)
			require.Equal(t, operatorBefore.Status.ContainerStatuses[0].ContainerID, operatorPods.Items[0].Status.ContainerStatuses[0].ContainerID)
		})
	}
}

// Test_RR21_MinAvailableChangeAndScaleToZeroDuringTemplateUpdate verifies that a scheduling handoff
// drops a configured-update selection made ineligible by the same request's new scale-in decision.
// It must not retain a non-replacement selection after deleting that selection's children.
//
// Scenario:
//  1. Deploy one Ready workload2 PCS replica under RollingRecreate or Coherent. Block fresh pc-a readiness
//     and trigger a template update. Require an unfinished selection with Replacement nil and no recorded
//     scaling progress. Under Coherent, wait for the intermediate multi-generation PodGangMap.
//  2. Hold source deletion with a finalizer, then increase sg-x MinAvailable from 1 to 2 and scale to zero
//     in one patch. Verify the inherited update selection is removed and replica 0 is recorded for scale-in.
//  3. Release deletion without releasing readiness. Require all replica resources to disappear and the
//     update to complete at zero replicas with its scheduling target applied and no scaling progress.
//  4. Submit another template edit and require admission and completion at zero. Release readiness and
//     scale to one. Verify ten Ready pods use the latest template, sg-x retains the new minimum, and
//     replica-scoped resources have fresh UIDs with no unfinished child updates.
func Test_RR21_MinAvailableChangeAndScaleToZeroDuringTemplateUpdate(t *testing.T) {
	for _, strategy := range []grovev1beta1.UpdateStrategyType{grovev1beta1.RollingRecreateStrategy, grovev1beta1.CoherentStrategy} {
		t.Run(string(strategy), func(t *testing.T) {
			tc, cleanup, _ := setupTest(t, testConfig{
				workloadName: "workload2", workloadYAML: "../../yaml/workload2.yaml", workerNodes: 10, expectedPods: 10,
			})
			t.Cleanup(cleanup)
			require.NoError(t, updateutils.UpdatePCSUpdateStrategy(tc, strategy))
			require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path))
			t.Cleanup(func() { assert.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name)) })

			tests.Logger.Info("1. Hold a configured template update with a non-replacement selection")
			require.NoError(t, updateutils.TriggerPodCliqueUpdateWithRevision(tc, "rr21-first", "pc-a"))
			require.NoError(t, updateutils.WaitForOrdinalUpdating(tc, 0))
			require.NoError(t, waitForNotReadyPodAtRevision(tc, "rr21-first"))
			if strategy == grovev1beta1.CoherentStrategy {
				updateutils.WaitForReplicaMidCoherentRoll(t, tc, 0)
			}
			before := requireReplicaSnapshot(t, tc)
			require.Nil(t, before.PCS.Status.ScalingProgress, "scale-in must not already be recorded before handoff")
			require.NotNil(t, before.PCS.Status.UpdateProgress)
			require.False(t, before.PCS.Status.UpdateProgress.RequiresReplicaRecreation)
			require.Nil(t, before.PCS.Status.UpdateProgress.UpdateEndedAt)
			require.Len(t, before.PCS.Status.UpdateProgress.CurrentlyUpdating, 1)
			require.Nil(t, before.PCS.Status.UpdateProgress.CurrentlyUpdating[0].Replacement)
			releaseSource, err := holdReplicaSourceCleanup(tc, before.PCLQ)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, releaseSource()) })

			tests.Logger.Info("2. Atomically change the scheduling minimum and scale one replica to zero")
			pcs := before.PCS.DeepCopy()
			pcs.Spec.Replicas = 0
			pcs.Spec.Template.PodCliqueScalingGroupConfigs[0].MinAvailable = new(int32(2))
			require.NoError(t, tc.Client.Patch(tc.Ctx, pcs, client.MergeFrom(before.PCS)))
			require.NoError(t, updateutils.WaitForGenerationHashChange(tc, *before.PCS.Status.CurrentGenerationHash))
			require.NoError(t, waitForReplicaSourceDeletion(tc, before.PCLQ))
			held, err := updateutils.GetPCS(tc, tc.Workload.Name)
			require.NoError(t, err)
			require.NotNil(t, held.Status.UpdateProgress)
			require.Empty(t, held.Status.UpdateProgress.CurrentlyUpdating, "scale-in must remove the inherited non-replacement selection")
			require.NotNil(t, held.Status.ScalingProgress)
			require.Equal(t, []int32{0}, held.Status.ScalingProgress.ScaleInReplicaIndices)
			target := recreationTarget{GenerationHash: *held.Status.CurrentGenerationHash, SchedulingHash: held.Status.UpdateProgress.TargetSchedulingConfigurationHash}
			require.NotEqual(t, *before.PCS.Status.AppliedSchedulingConfigurationHash, target.SchedulingHash)

			tests.Logger.Info("3. Finish scale-in without making the obsolete template-update pods Ready")
			require.NoError(t, releaseSource())
			require.NoError(t, waitForReplicaResourcesDeleted(tc, client.MatchingLabels{apicommon.LabelPartOfKey: tc.Workload.Name}))
			require.NoError(t, waitForReplicaRecreationComplete(tc, 0))
			completed, err := updateutils.GetPCS(tc, tc.Workload.Name)
			require.NoError(t, err)
			assertReplicaRecreationCompleted(t, completed, expectedReplicaRecreationCompletion{
				GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash, Replicas: 0,
			})
			require.Nil(t, completed.Status.ScalingProgress)

			tests.Logger.Info("4. Admit another template edit and provision the latest configuration")
			require.NoError(t, updateutils.TriggerPodCliqueUpdateWithRevision(tc, "rr21-next", "pc-a"))
			require.NoError(t, updateutils.WaitForGenerationHashChange(tc, target.GenerationHash))
			require.NoError(t, updateutils.WaitForRollingUpdateComplete(tc, 0))
			require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
			require.NoError(t, tc.ScalePCS(tc.Workload.Name, 1))
			require.NoError(t, updateutils.WaitForRollingUpdateComplete(tc, 1))
			require.NoError(t, tc.WaitForPods(10))
			updateutils.AssertAffectedPodsAtRevision(tc, "pc-a", "rr21-next")
			updateutils.AssertGenerationHashConverged(tc)
			updateutils.AssertNoActiveChildUpdates(tc)
			final := requireReplicaSnapshot(t, tc)
			require.Equal(t, new(int32(2)), final.PCSG.Spec.MinAvailable)
			requireReplicaResourceUIDsChanged(t, before, final)
		})
	}
}

// Test_RR22_OnDeleteMinAvailableChangeRecreatesLiveReplica verifies that a scheduling change
// automatically replaces a live PCS replica even when its configured update strategy is OnDelete.
//
// Scenario:
//  1. Deploy workload2 with one Ready replica and ten pods, then configure OnDelete. Capture the
//     original child UIDs and scheduling baseline with sg-x MinAvailable set to one.
//  2. Cordon the schedulable test workers and increase sg-x MinAvailable to two. Without manually
//     deleting pods or children, require fresh standalone PodClique, PCSG, and PodGangMap UIDs.
//  3. Verify both sg-x indices are in the anchor with no tail, source cleanup is acknowledged,
//     and the unscheduled replacement keeps the new scheduling target unapplied.
//  4. Uncordon the workers and require ten Ready pods, the applied target, and unchanged replacement
//     UIDs. The PCS must retain OnDelete throughout recreation and completion.
func Test_RR22_OnDeleteMinAvailableChangeRecreatesLiveReplica(t *testing.T) {
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload2", workloadYAML: "../../yaml/workload2.yaml", workerNodes: 10, expectedPods: 10,
	})
	t.Cleanup(cleanup)
	require.NoError(t, updateutils.UpdatePCSUpdateStrategy(tc, grovev1beta1.OnDeleteStrategy))
	before := requireReplicaSnapshot(t, tc)
	require.Equal(t, grovev1beta1.OnDeleteStrategy, before.PCS.Spec.UpdateStrategy.Type)
	require.Nil(t, before.PCS.Status.UpdateProgress)
	require.Equal(t, new(int32(1)), before.PCSG.Spec.MinAvailable)

	workers := cordonSchedulableTestWorkers(t, tc)
	require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 2))
	cleanupPCS, err := waitForSameIndexReplacementCleanup(tc, 0)
	require.NoError(t, err, "OnDelete must recreate the source without manual deletion")
	target := requireSchedulingTargetChanged(t, before.PCS, cleanupPCS)
	want := expectedReplicaConfiguration{
		StandaloneMinAvailable: 1, PCSGMinAvailable: 2, PCSGReplicas: 2,
		PodGangMap: updateutils.ExpectedReplicaPodGangMap{
			GenerationHash: target.GenerationHash, StandalonePodCounts: map[string]int32{"pc-a": 2},
			PCSGName: "sg-x", AnchorIndices: []int32{0, 1},
		},
	}
	recreated := requireRecreatedReplica(t, tc, before, want)
	require.Equal(t, grovev1beta1.OnDeleteStrategy, recreated.PCS.Spec.UpdateStrategy.Type)

	tc.UncordonNodes(workers)
	requireReplicaRecreationConverged(t, tc, expectedReplicaRecreationCompletion{
		GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash,
		Replicas: 1, Pods: 10, PCSReplicaIndices: []int{0}, Configuration: want,
	})
	final := requireReplicaSnapshot(t, tc)
	assertReplicaResourceUIDsUnchanged(t, recreated, final)
	require.Equal(t, grovev1beta1.OnDeleteStrategy, final.PCS.Spec.UpdateStrategy.Type)
}

// Test_RR23_PCSGMemberMinAvailableChangeRecreatesReplica verifies that changing a member PodClique's
// minimum recreates the PCS replica and updates the member's actual PodGang scheduling requirement.
//
// Scenario:
//  1. Deploy workload2 under RollingRecreate or Coherent with ten Ready pods. Capture the two pc-c
//     member PodCliques, each with three replicas and MinAvailable one, and the actual PodGang minima.
//  2. Cordon the schedulable test workers and change only pc-c MinAvailable from one to two in the PCS
//     template. Leave the standalone and scaling-group minima unchanged.
//  3. Require a fresh standalone PodClique, PCSG, PodGangMap, and both pc-c members. Verify the member
//     specs and PodGang minReplicas are two while the anchor/tail membership remains unchanged.
//     The unscheduled replacement must remain active with its scheduling target unapplied.
//  4. Uncordon the workers and require ten Ready pods, generation convergence, the applied target,
//     stable replacement UIDs, and the same member specs and actual scheduling minima.
func Test_RR23_PCSGMemberMinAvailableChangeRecreatesReplica(t *testing.T) {
	for _, strategy := range []grovev1beta1.UpdateStrategyType{grovev1beta1.RollingRecreateStrategy, grovev1beta1.CoherentStrategy} {
		t.Run(string(strategy), func(t *testing.T) {
			tc, cleanup, _ := setupTest(t, testConfig{
				workloadName: "workload2", workloadYAML: "../../yaml/workload2.yaml", workerNodes: 10, expectedPods: 10,
			})
			t.Cleanup(cleanup)
			require.NoError(t, updateutils.UpdatePCSUpdateStrategy(tc, strategy))
			before := requireReplicaSnapshot(t, tc)
			require.Nil(t, before.PCS.Status.UpdateProgress)
			beforeMembers := make([]grovev1beta1.PodClique, 2)
			wantMinima := map[string]int32{before.PCLQ.Name: 1}
			for i := range beforeMembers {
				owner := apicommon.ResourceNameReplica{Name: before.PCSG.Name, Replica: i}
				name := apicommon.GeneratePodCliqueName(owner, "pc-c")
				require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKey{Namespace: tc.Namespace, Name: name}, &beforeMembers[i]))
				require.Equal(t, new(int32(1)), beforeMembers[i].Spec.MinAvailable)
				require.Equal(t, int32(3), beforeMembers[i].Spec.Replicas)
				wantMinima[name] = 1
				wantMinima[apicommon.GeneratePodCliqueName(owner, "pc-b")] = 1
			}
			requirePodGangMinReplicas(t, tc, wantMinima)

			workers := cordonSchedulableTestWorkers(t, tc)
			changed := before.PCS.DeepCopy()
			memberTemplate := componentutils.FindPodCliqueTemplateSpecByName(changed, "pc-c")
			require.NotNil(t, memberTemplate)
			memberTemplate.Spec.MinAvailable = new(int32(2))
			require.NoError(t, tc.Client.Patch(tc.Ctx, changed, client.MergeFrom(before.PCS)))
			cleanupPCS, err := waitForSameIndexReplacementCleanup(tc, 0)
			require.NoError(t, err)
			target := requireSchedulingTargetChanged(t, before.PCS, cleanupPCS)
			want := expectedReplicaConfiguration{
				StandaloneMinAvailable: 1, PCSGMinAvailable: 1, PCSGReplicas: 2,
				PodGangMap: updateutils.ExpectedReplicaPodGangMap{
					GenerationHash: target.GenerationHash, StandalonePodCounts: map[string]int32{"pc-a": 2},
					PCSGName: "sg-x", AnchorIndices: []int32{0}, TailIndices: []int32{1},
				},
			}
			recreated := requireRecreatedReplica(t, tc, before, want)
			recreatedMembers := make([]*grovev1beta1.PodClique, len(beforeMembers))
			for i := range beforeMembers {
				member, err := waitForRecreatedPCLQ(tc, &beforeMembers[i])
				require.NoError(t, err, "member %s must be recreated", beforeMembers[i].Name)
				require.Equal(t, new(int32(2)), member.Spec.MinAvailable)
				require.Equal(t, int32(3), member.Spec.Replicas)
				require.Equal(t, target.GenerationHash, member.Labels[apicommon.LabelPodCliqueSetGenerationHash])
				recreatedMembers[i] = member
				wantMinima[member.Name] = 2
			}
			requirePodGangMinReplicas(t, tc, wantMinima)

			tc.UncordonNodes(workers)
			requireReplicaRecreationConverged(t, tc, expectedReplicaRecreationCompletion{
				GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash,
				Replicas: 1, Pods: 10, PCSReplicaIndices: []int{0}, Configuration: want,
			})
			assertReplicaResourceUIDsUnchanged(t, recreated, requireReplicaSnapshot(t, tc))
			for _, member := range recreatedMembers {
				var final grovev1beta1.PodClique
				require.NoError(t, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(member), &final))
				require.Equal(t, member.UID, final.UID)
				require.Equal(t, new(int32(2)), final.Spec.MinAvailable)
				require.Equal(t, int32(3), final.Spec.Replicas)
			}
			requirePodGangMinReplicas(t, tc, wantMinima)
		})
	}
}

// Test_RR24_MinAvailableDecreaseAfterCompletedRecreation verifies that decreasing a fully applied
// scheduling minimum starts a new structural recreation rather than reusing a completed update.
//
// Scenario:
//  1. Deploy workload2, increase sg-x MinAvailable from one to two, and wait for full completion with
//     ten Ready pods. Verify the applied higher minimum, fresh child UIDs, and an anchor with both indices.
//  2. Capture that completed update and cordon the schedulable test workers. Decrease sg-x MinAvailable
//     to one without changing replica counts or pod templates.
//  3. Require a new active replacement with fresh child UIDs. Its scheduling target must differ from the
//     fully applied higher minimum, which remains applied while the replacement is unscheduled.
//     The anchor must contain index zero and the restored tail must contain index one.
//  4. Uncordon the workers and require ten Ready pods at the lower minimum, the applied target, and
//     stable replacement UIDs. The previously completed update must not suppress the new recreation.
func Test_RR24_MinAvailableDecreaseAfterCompletedRecreation(t *testing.T) {
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: "workload2", workloadYAML: "../../yaml/workload2.yaml", workerNodes: 10, expectedPods: 10,
	})
	t.Cleanup(cleanup)
	original := requireReplicaSnapshot(t, tc)
	require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 2))
	require.NoError(t, updateutils.WaitForGenerationHashChange(tc, *original.PCS.Status.CurrentGenerationHash))
	require.NoError(t, waitForReplicaRecreationComplete(tc, 1))
	require.NoError(t, tc.WaitForPods(10))
	before := requireReplicaSnapshot(t, tc)
	requireReplicaResourceUIDsChanged(t, original, before)
	require.NotNil(t, before.PCS.Status.UpdateProgress)
	require.NotNil(t, before.PCS.Status.UpdateProgress.UpdateEndedAt)
	require.Empty(t, before.PCS.Status.UpdateProgress.CurrentlyUpdating)
	require.Nil(t, before.PCS.Status.ScalingProgress)
	require.Equal(t, new(before.PCS.Status.UpdateProgress.TargetSchedulingConfigurationHash), before.PCS.Status.AppliedSchedulingConfigurationHash)
	require.NotEqual(t, original.PCS.Status.AppliedSchedulingConfigurationHash, before.PCS.Status.AppliedSchedulingConfigurationHash)
	assertReplicaConfiguration(t, before, expectedReplicaConfiguration{
		StandaloneMinAvailable: 1, PCSGMinAvailable: 2, PCSGReplicas: 2,
		PodGangMap: updateutils.ExpectedReplicaPodGangMap{
			GenerationHash: *before.PCS.Status.CurrentGenerationHash, StandalonePodCounts: map[string]int32{"pc-a": 2},
			PCSGName: "sg-x", AnchorIndices: []int32{0, 1},
		},
	})
	updateutils.AssertGenerationHashConverged(tc)
	updateutils.AssertNoActiveChildUpdates(tc)

	workers := cordonSchedulableTestWorkers(t, tc)
	require.NoError(t, triggerPCSGMinAvailableChange(tc, "sg-x", 1))
	cleanupPCS, err := waitForSameIndexReplacementCleanup(tc, 0)
	require.NoError(t, err, "the decrease must start a new recreation after the higher minimum was applied")
	target := requireSchedulingTargetChanged(t, before.PCS, cleanupPCS)
	require.False(t, cleanupPCS.Status.UpdateProgress.UpdateStartedAt.Before(before.PCS.Status.UpdateProgress.UpdateEndedAt),
		"the new update must not predate completion of the increase")
	require.Equal(t, *original.PCS.Status.CurrentGenerationHash, target.GenerationHash)
	require.Equal(t, *original.PCS.Status.AppliedSchedulingConfigurationHash, target.SchedulingHash)
	want := expectedReplicaConfiguration{
		StandaloneMinAvailable: 1, PCSGMinAvailable: 1, PCSGReplicas: 2,
		PodGangMap: updateutils.ExpectedReplicaPodGangMap{
			GenerationHash: target.GenerationHash, StandalonePodCounts: map[string]int32{"pc-a": 2},
			PCSGName: "sg-x", AnchorIndices: []int32{0}, TailIndices: []int32{1},
		},
	}
	recreated := requireRecreatedReplica(t, tc, before, want)

	tc.UncordonNodes(workers)
	requireReplicaRecreationConverged(t, tc, expectedReplicaRecreationCompletion{
		GenerationHash: target.GenerationHash, SchedulingHash: target.SchedulingHash,
		Replicas: 1, Pods: 10, PCSReplicaIndices: []int{0}, Configuration: want,
	})
	assertReplicaResourceUIDsUnchanged(t, recreated, requireReplicaSnapshot(t, tc))
}

// cordonSchedulableTestWorkers prevents fresh pods from scheduling and restores only the workers it changed.
func cordonSchedulableTestWorkers(t *testing.T, tc *testctx.TestContext) []string {
	t.Helper()
	var workers corev1.NodeList
	require.NoError(t, tc.Client.List(tc.Ctx, &workers, client.MatchingLabels{setup.WorkerNodeLabelKey: setup.WorkerNodeLabelValue}))
	var names []string
	for _, node := range workers.Items {
		if !node.Spec.Unschedulable {
			names = append(names, node.Name)
		}
	}
	require.NotEmpty(t, names, "at least one test worker must be schedulable before holding the replacement")
	t.Cleanup(func() {
		for _, name := range names {
			assert.NoError(t, tc.UncordonNode(name))
		}
	})
	tc.CordonNodes(names)
	return names
}

// requirePodGangMinReplicas waits for replica zero's actual PodGroups to carry exactly the requested minima.
// Each member must occur in one live PodGang so duplicate or stale groups cannot satisfy the check.
func requirePodGangMinReplicas(t *testing.T, tc *testctx.TestContext, want map[string]int32) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var podGangs groveschedulerv1alpha1.PodGangList
		require.NoError(c, tc.Client.List(tc.Ctx, &podGangs, client.InNamespace(tc.Namespace), client.MatchingLabels{
			apicommon.LabelPartOfKey: tc.Workload.Name, apicommon.LabelPodCliqueSetReplicaIndex: "0",
		}))
		got := make(map[string]int32)
		for _, podGang := range podGangs.Items {
			if podGang.DeletionTimestamp != nil {
				continue
			}
			for _, group := range podGang.Spec.PodGroups {
				assert.NotContains(c, got, group.Name, "PodGroup must occur in only one live PodGang")
				got[group.Name] = group.MinReplicas
			}
		}
		assert.Equal(c, want, got, "actual PodGang scheduling minima")
	}, tc.Timeout, tc.Interval)
}

// findPCSReplicaUpdateSelection returns the source-keyed selection without copying or mutating its progress.
func findPCSReplicaUpdateSelection(progress *grovev1beta1.PodCliqueSetUpdateProgress, sourcePCSReplicaIndex int32) *grovev1beta1.PodCliqueSetReplicaUpdateProgress {
	if progress == nil {
		return nil
	}
	for i := range progress.CurrentlyUpdating {
		if progress.CurrentlyUpdating[i].ReplicaIndex == sourcePCSReplicaIndex {
			return &progress.CurrentlyUpdating[i]
		}
	}
	return nil
}

// waitForReplicaRecreationCompletionPreservingCompletedSelections validates every watched status until
// completion. The initial PCS must contain every original source's selection. Completed selections must
// remain unchanged while the update is active, and no additional sources may enter its selection history.
// The caller owns the watch and its lifetime.
func waitForReplicaRecreationCompletionPreservingCompletedSelections(
	ctx context.Context,
	watcher watch.Interface,
	before *grovev1beta1.PodCliqueSet,
	freshScaleOutPCSReplicaIndex int32,
) (*grovev1beta1.PodCliqueSet, error) {
	event, err := watchtools.UntilWithoutRetry(ctx, watcher, func(event watch.Event) (bool, error) {
		switch event.Type {
		case watch.Error:
			return false, apierrors.FromObject(event.Object)
		case watch.Deleted:
			return false, fmt.Errorf("PCS deleted before recreation completed")
		case watch.Bookmark:
			return false, nil
		case watch.Added, watch.Modified:
		default:
			return false, fmt.Errorf("unexpected PCS watch event %q", event.Type)
		}
		pcs, ok := event.Object.(*grovev1beta1.PodCliqueSet)
		if !ok || pcs == nil {
			return false, fmt.Errorf("unexpected PCS watch object %T", event.Object)
		}
		progress := pcs.Status.UpdateProgress
		if progress == nil {
			return false, fmt.Errorf("recreation progress disappeared before completion")
		}
		hasSameRecreationTarget := apiequality.Semantic.DeepEqual(pcs.Status.CurrentGenerationHash, before.Status.CurrentGenerationHash) &&
			progress.TargetSchedulingConfigurationHash == before.Status.UpdateProgress.TargetSchedulingConfigurationHash
		if !hasSameRecreationTarget || !progress.UpdateStartedAt.Equal(&before.Status.UpdateProgress.UpdateStartedAt) {
			return false, fmt.Errorf("recreation target or update identity changed")
		}
		if progress.UpdateEndedAt != nil {
			if pcs.Status.AppliedSchedulingConfigurationHash == nil ||
				*pcs.Status.AppliedSchedulingConfigurationHash != progress.TargetSchedulingConfigurationHash ||
				len(progress.CurrentlyUpdating) != 0 {
				return false, fmt.Errorf("completed recreation did not apply its target and clear selections")
			}
			return true, nil
		}
		for _, completed := range before.Status.UpdateProgress.CurrentlyUpdating {
			if completed.UpdateEndedAt != nil {
				after := findPCSReplicaUpdateSelection(progress, completed.ReplicaIndex)
				if after == nil || !apiequality.Semantic.DeepEqual(completed, *after) {
					return false, fmt.Errorf("completed source %d selection changed after resource loss", completed.ReplicaIndex)
				}
			}
		}
		if findPCSReplicaUpdateSelection(progress, freshScaleOutPCSReplicaIndex) != nil {
			return false, fmt.Errorf("ordinary S1 scale-out replica %d entered update progress", freshScaleOutPCSReplicaIndex)
		}
		for _, selection := range progress.CurrentlyUpdating {
			if findPCSReplicaUpdateSelection(before.Status.UpdateProgress, selection.ReplicaIndex) == nil {
				return false, fmt.Errorf("unexpected source %d selected after original replacements were committed", selection.ReplicaIndex)
			}
		}
		return false, nil
	})
	if err != nil {
		return nil, fmt.Errorf("waiting for recreation completion with missing repair and scale-out resources: %w", err)
	}
	return event.Object.(*grovev1beta1.PodCliqueSet).DeepCopy(), nil
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
// converged to, and UpdatedReplicas matches expectedReplicas. The current PCS generation must be observed.
func waitForReplicaRecreationComplete(tc *testctx.TestContext, expectedReplicas int32) error {
	fetchPCS := waiter.FetchByName(tc.Workload.Name, k8sclient.Getter[*grovev1beta1.PodCliqueSet](tc.Client, tc.Namespace))
	predicate := waiter.Predicate[*grovev1beta1.PodCliqueSet](func(pcs *grovev1beta1.PodCliqueSet) bool {
		if pcs == nil || pcs.Status.UpdateProgress == nil {
			return false
		}
		if pcs.Status.ObservedGeneration == nil || *pcs.Status.ObservedGeneration != pcs.Generation {
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
	// kwokStageReadyBlockedR2Path is the KWOK stage fixture path.
	kwokStageReadyBlockedR2Path = "../../yaml/kwok/pod-ready-blocked-r2-standalone.yaml"
	// kwokStageReadyBlockedR2Name is the KWOK stage name.
	kwokStageReadyBlockedR2Name = "pod-ready-blocked-r2-standalone"
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
	selections := unfinishedPCSReplicaSelections(progress)
	require.Len(t, selections, 1, "expected a single unfinished replacement")
	replicaProgress := &selections[0]
	require.Equal(t, want.SourcePCSReplicaIndex, replicaProgress.ReplicaIndex, "replacement source index")
	require.Nil(t, replicaProgress.UpdateEndedAt, "replica %d replacement must remain unfinished", want.SourcePCSReplicaIndex)
	require.NotNil(t, replicaProgress.Replacement, "replica %d must have replacement progress", want.SourcePCSReplicaIndex)
	require.Equal(t, want.TargetPCSReplicaIndex, replicaProgress.Replacement.TargetReplicaIndex, "replacement target index")
	require.Nil(t, replicaProgress.Replacement.CanceledAt, "replica %d replacement must not be canceled", want.SourcePCSReplicaIndex)
	return replicaProgress
}

// unfinishedPCSReplicaSelections returns a shallow copy of unfinished selections in their original order.
func unfinishedPCSReplicaSelections(progress *grovev1beta1.PodCliqueSetUpdateProgress) []grovev1beta1.PodCliqueSetReplicaUpdateProgress {
	return slices.DeleteFunc(slices.Clone(progress.CurrentlyUpdating), func(selection grovev1beta1.PodCliqueSetReplicaUpdateProgress) bool {
		return selection.UpdateEndedAt != nil
	})
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

// waitForDifferentIndexReplacementHandoff observes the first superseding generation on a PCS watch.
// The old target must become the same-index source, and the old source must remain retired cleanup.
// The caller starts the watch before changing the specification. UntilWithoutRetry stops it on return.
func waitForDifferentIndexReplacementHandoff(
	ctx context.Context,
	watcher watch.Interface,
	previous *grovev1beta1.PodCliqueSet,
) (*grovev1beta1.PodCliqueSet, error) {
	beforeSelections := unfinishedPCSReplicaSelections(previous.Status.UpdateProgress)
	if len(beforeSelections) != 1 || beforeSelections[0].Replacement == nil {
		return nil, fmt.Errorf("handoff requires exactly one existing replacement")
	}
	before := beforeSelections[0]
	if before.ReplicaIndex == before.Replacement.TargetReplicaIndex {
		return nil, fmt.Errorf("handoff requires a different-index replacement")
	}
	event, err := watchtools.UntilWithoutRetry(ctx, watcher, func(event watch.Event) (bool, error) {
		switch event.Type {
		case watch.Error:
			return false, apierrors.FromObject(event.Object)
		case watch.Deleted:
			return false, fmt.Errorf("PCS deleted before replacement handoff")
		case watch.Added, watch.Modified:
		default:
			return false, nil
		}
		pcs, ok := event.Object.(*grovev1beta1.PodCliqueSet)
		if !ok || pcs == nil {
			return false, fmt.Errorf("unexpected handoff watch object %T", event.Object)
		}
		if pcs.Status.CurrentGenerationHash == nil || *pcs.Status.CurrentGenerationHash == *previous.Status.CurrentGenerationHash {
			return false, nil
		}
		progress := pcs.Status.UpdateProgress
		if progress == nil || !progress.RequiresReplicaRecreation || progress.UpdateEndedAt != nil || len(progress.CurrentlyUpdating) != 1 {
			return false, fmt.Errorf("supersession did not retain one unfinished replacement")
		}
		after := progress.CurrentlyUpdating[0]
		replacement := after.Replacement
		if after.ReplicaIndex != before.Replacement.TargetReplicaIndex || replacement == nil || replacement.TargetReplicaIndex != after.ReplicaIndex {
			return false, fmt.Errorf("supersession did not rebase replacement %d -> %d to its target", before.ReplicaIndex, before.Replacement.TargetReplicaIndex)
		}
		if after.UpdateEndedAt != nil || replacement.CanceledAt != nil || replacement.SourceCleanupCompletedAt != nil {
			return false, fmt.Errorf("handoff completed, canceled, or failed to reset source cleanup")
		}
		if !slices.Contains(replacement.RetiredSourceReplicaIndices, before.ReplicaIndex) {
			return false, fmt.Errorf("handoff lost retired source %d", before.ReplicaIndex)
		}
		if !assert.ObjectsAreEqual(previous.Status.AppliedSchedulingConfigurationHash, pcs.Status.AppliedSchedulingConfigurationHash) {
			return false, fmt.Errorf("handoff advanced the applied scheduling hash before completion")
		}
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("waiting for different-index replacement handoff: %w", err)
	}
	pcs, ok := event.Object.(*grovev1beta1.PodCliqueSet)
	if !ok || pcs == nil {
		return nil, fmt.Errorf("unexpected handoff result %T", event.Object)
	}
	return pcs.DeepCopy(), nil
}

// waitForReplacementRetirementCleared waits for participant-confirmed retirement at an active same-index
// replacement. Completing or changing the update while readiness is blocked is a failure rather than success.
func waitForReplacementRetirementCleared(tc *testctx.TestContext, pcsReplicaIndex int32, generationHash string) (*grovev1beta1.PodCliqueSet, error) {
	var observed *grovev1beta1.PodCliqueSet
	err := wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(context.Context) (bool, error) {
		pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
		if err != nil {
			return false, err
		}
		if pcs.Status.CurrentGenerationHash == nil || *pcs.Status.CurrentGenerationHash != generationHash {
			return false, fmt.Errorf("PCS generation changed during retired-source cleanup")
		}
		progress := pcs.Status.UpdateProgress
		if progress == nil || progress.UpdateEndedAt != nil {
			return false, fmt.Errorf("update completed before target readiness was released")
		}
		for _, replicaProgress := range progress.CurrentlyUpdating {
			if replicaProgress.ReplicaIndex != pcsReplicaIndex || replicaProgress.Replacement == nil {
				continue
			}
			if replicaProgress.UpdateEndedAt != nil || replicaProgress.Replacement.CanceledAt != nil {
				return false, fmt.Errorf("replacement completed or canceled while target readiness was blocked")
			}
			observed = pcs
			return len(replicaProgress.Replacement.RetiredSourceReplicaIndices) == 0, nil
		}
		return false, fmt.Errorf("replacement %d disappeared during retirement", pcsReplicaIndex)
	})
	return observed, err
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
		progress := pcs.Status.UpdateProgress
		if progress == nil || progress.UpdateEndedAt != nil {
			return false, fmt.Errorf("update stopped while fresh pod readiness was blocked")
		}
		if !assert.ObjectsAreEqual(previous.Status.AppliedSchedulingConfigurationHash, pcs.Status.AppliedSchedulingConfigurationHash) {
			return false, fmt.Errorf("scheduling baseline changed while fresh pod readiness was blocked")
		}
		if previous.Status.UpdateProgress.RequiresReplicaRecreation {
			if err := validateRecreationWhileBlocked(previous, pcs); err != nil {
				return false, err
			}
		}
		if pcs.Status.CurrentGenerationHash == nil || *pcs.Status.CurrentGenerationHash == *previous.Status.CurrentGenerationHash {
			return false, nil
		}
		if !progress.RequiresReplicaRecreation {
			return false, fmt.Errorf("superseding generation must require replica recreation")
		}
		if progress.TargetSchedulingConfigurationHash == previous.Status.UpdateProgress.TargetSchedulingConfigurationHash {
			return false, fmt.Errorf("superseding generation must change the scheduling target")
		}
		if len(progress.InScopeStandalonePodCliques) != 0 || len(progress.InScopePodCliqueScalingGroups) != 0 {
			return false, fmt.Errorf("replica recreation retained coherent update scope")
		}
		// Existing recreation selections were checked above, including completed selections being dropped.
		if !previous.Status.UpdateProgress.RequiresReplicaRecreation && len(progress.CurrentlyUpdating) != len(previous.Status.UpdateProgress.CurrentlyUpdating) {
			return false, fmt.Errorf("supersession changed the active replica selection count")
		}
		for i, replicaProgress := range progress.CurrentlyUpdating {
			if !previous.Status.UpdateProgress.RequiresReplicaRecreation && replicaProgress.ReplicaIndex != previous.Status.UpdateProgress.CurrentlyUpdating[i].ReplicaIndex {
				return false, fmt.Errorf("supersession changed the active replica selection")
			}
			if replicaProgress.UpdateEndedAt != nil {
				return false, fmt.Errorf("supersession completed the active replica selection")
			}
			if len(replicaProgress.InFlightEpochs) != 0 {
				return false, fmt.Errorf("replica recreation retained coherent in-flight epochs")
			}
		}
		return true, nil
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
	hasSupersededGeneration := current.Status.CurrentGenerationHash != nil &&
		*current.Status.CurrentGenerationHash != *previous.Status.CurrentGenerationHash
	previousSelections := previous.Status.UpdateProgress.CurrentlyUpdating
	if hasSupersededGeneration {
		previousSelections = unfinishedPCSReplicaSelections(previous.Status.UpdateProgress)
	}
	if len(progress.CurrentlyUpdating) != len(previousSelections) {
		return fmt.Errorf("replica selections changed during supersession: expected %d, got %d", len(previousSelections), len(progress.CurrentlyUpdating))
	}
	for i, replicaProgress := range progress.CurrentlyUpdating {
		before := previousSelections[i]
		if !assert.ObjectsAreEqual(before.UpdateEndedAt, replicaProgress.UpdateEndedAt) {
			return fmt.Errorf("replica %d completion changed while replacement readiness was blocked", replicaProgress.ReplicaIndex)
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
	requireSchedulingTargetChanged(t, before, after)
	beforeProgress := requireActiveReplicaRecreation(t, before)
	beforeSelections := unfinishedPCSReplicaSelections(beforeProgress)
	require.Len(t, beforeSelections, 1)
	beforeReplicaProgress := beforeSelections[0]
	require.NotNil(t, beforeReplicaProgress.Replacement)
	wantReplacement := expectedReplicaReplacement{
		SourcePCSReplicaIndex: beforeReplicaProgress.ReplicaIndex,
		TargetPCSReplicaIndex: beforeReplicaProgress.Replacement.TargetReplicaIndex,
	}
	requireActiveReplacement(t, beforeProgress, wantReplacement)
	require.NotNil(t, after.Status.CurrentGenerationHash)
	require.NotEqual(t, *before.Status.CurrentGenerationHash, *after.Status.CurrentGenerationHash, "supersession must change the PCS generation")
	progress := requireActiveReplicaRecreation(t, after)
	replicaProgress := requireActiveReplacement(t, progress, wantReplacement)
	require.NoError(t, validateRecreationWhileBlocked(before, after), "supersession must preserve replacement commitments")
	require.Nil(t, replicaProgress.Replacement.SourceCleanupCompletedAt, "supersession must restart cleanup")
	require.NotNil(t, progress.PreviousPodCliqueSetGenerationHash)
	assert.Equal(t, *before.Status.CurrentGenerationHash, *progress.PreviousPodCliqueSetGenerationHash)
	assert.Equal(t, progress.UpdateStartedAt, replicaProgress.UpdateStartedAt, "replacement must use the new update's start time")
	return recreationTarget{GenerationHash: *after.Status.CurrentGenerationHash, SchedulingHash: progress.TargetSchedulingConfigurationHash}
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

// holdReplicaSourceCleanup adds a test finalizer to keep a source resource terminating.
// The returned release is idempotent and removes only this finalizer from the original UID.
func holdReplicaSourceCleanup(tc *testctx.TestContext, source client.Object) (func() error, error) {
	const finalizer = "e2e.grove.io/hold-recreation-source-cleanup"
	key := client.ObjectKeyFromObject(source)
	sourceUID := source.GetUID()
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := source.DeepCopyObject().(client.Object)
		if err := tc.Client.Get(tc.Ctx, key, current); err != nil {
			return err
		}
		if current.GetUID() != sourceUID {
			return fmt.Errorf("source %s UID changed before cleanup hold", key)
		}
		base := current.DeepCopyObject().(client.Object)
		controllerutil.AddFinalizer(current, finalizer)
		return tc.Client.Patch(tc.Ctx, current, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	}); err != nil {
		return nil, fmt.Errorf("hold source cleanup: %w", err)
	}
	return func() error {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(tc.Ctx), tc.Timeout)
		defer cancel()
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current := source.DeepCopyObject().(client.Object)
			if err := tc.Client.Get(ctx, key, current); err != nil {
				return client.IgnoreNotFound(err)
			}
			if current.GetUID() != sourceUID {
				return nil
			}
			base := current.DeepCopyObject().(client.Object)
			if !controllerutil.RemoveFinalizer(current, finalizer) {
				return nil
			}
			return tc.Client.Patch(ctx, current, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
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

// waitForNextReplacementAfterSourceCleanup observes confirmed cleanup and completion of the previous
// replacement before the expected next mapping is selected. It preserves update identity and rejects watch
// loss. The caller establishes the watch before releasing source cleanup and owns its lifetime.
func waitForNextReplacementAfterSourceCleanup(
	ctx context.Context,
	watcher watch.Interface,
	before *grovev1beta1.PodCliqueSet,
	want expectedReplicaReplacement,
) (*grovev1beta1.PodCliqueSet, error) {
	previousSelections := unfinishedPCSReplicaSelections(before.Status.UpdateProgress)
	if len(previousSelections) != 1 {
		return nil, fmt.Errorf("expected one previous active replacement, got %d", len(previousSelections))
	}
	previous := previousSelections[0]
	event, err := watchtools.UntilWithoutRetry(ctx, watcher, func(event watch.Event) (bool, error) {
		switch event.Type {
		case watch.Error:
			return false, apierrors.FromObject(event.Object)
		case watch.Deleted:
			return false, fmt.Errorf("PCS deleted before the next replacement was selected")
		case watch.Bookmark:
			return false, nil
		case watch.Added, watch.Modified:
		default:
			return false, fmt.Errorf("unexpected PCS watch event %q", event.Type)
		}
		pcs, ok := event.Object.(*grovev1beta1.PodCliqueSet)
		if !ok || pcs == nil {
			return false, fmt.Errorf("unexpected PCS watch object %T", event.Object)
		}
		progress := pcs.Status.UpdateProgress
		if progress == nil || progress.UpdateEndedAt != nil || !progress.RequiresReplicaRecreation {
			return false, fmt.Errorf("recreation ended before source %d was selected", want.SourcePCSReplicaIndex)
		}
		hasSameTarget := apiequality.Semantic.DeepEqual(before.Status.CurrentGenerationHash, pcs.Status.CurrentGenerationHash) &&
			before.Status.UpdateProgress.TargetSchedulingConfigurationHash == progress.TargetSchedulingConfigurationHash
		if !hasSameTarget || !before.Status.UpdateProgress.UpdateStartedAt.Equal(&progress.UpdateStartedAt) {
			return false, fmt.Errorf("recreation target or update identity changed during source cleanup")
		}
		next := findPCSReplicaUpdateSelection(progress, want.SourcePCSReplicaIndex)
		if next == nil {
			return false, nil
		}
		completed := findPCSReplicaUpdateSelection(progress, previous.ReplicaIndex)
		if completed == nil || completed.Replacement == nil || completed.Replacement.SourceCleanupCompletedAt == nil || completed.UpdateEndedAt == nil {
			return false, fmt.Errorf("source %d selected before source %d cleanup and completion were recorded", want.SourcePCSReplicaIndex, previous.ReplicaIndex)
		}
		if completed.Replacement.TargetReplicaIndex != previous.Replacement.TargetReplicaIndex ||
			!completed.UpdateStartedAt.Equal(&previous.UpdateStartedAt) {
			return false, fmt.Errorf("previous source %d mapping changed during cleanup", previous.ReplicaIndex)
		}
		if next.Replacement == nil || next.Replacement.TargetReplicaIndex != want.TargetPCSReplicaIndex ||
			next.Replacement.CanceledAt != nil || next.UpdateEndedAt != nil {
			return false, fmt.Errorf("expected an active replacement %d -> %d", want.SourcePCSReplicaIndex, want.TargetPCSReplicaIndex)
		}
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("waiting for next replacement after confirmed source cleanup: %w", err)
	}
	return event.Object.(*grovev1beta1.PodCliqueSet).DeepCopy(), nil
}

// waitForRecreationReplicaCount observes a scale change without permitting active replacement commitments to change.
// The watch must be started before scaling. Watch loss is an error, not a reason to skip intermediate statuses.
func waitForRecreationReplicaCount(ctx context.Context, watcher watch.Interface, before *grovev1beta1.PodCliqueSet, pcsReplicas int32) (*grovev1beta1.PodCliqueSet, error) {
	pcs, err := waitForRecreationScaling(ctx, watcher, before, func(pcs *grovev1beta1.PodCliqueSet) (bool, error) {
		return pcs.Spec.Replicas == pcsReplicas, nil
	})
	if err != nil {
		return nil, fmt.Errorf("waiting for recreation scale to %d replicas: %w", pcsReplicas, err)
	}
	return pcs, nil
}

// waitForRecreationScaling validates the held commitment on every event, then checks the scaling milestone.
func waitForRecreationScaling(
	ctx context.Context,
	watcher watch.Interface,
	before *grovev1beta1.PodCliqueSet,
	reached func(*grovev1beta1.PodCliqueSet) (bool, error),
) (*grovev1beta1.PodCliqueSet, error) {
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
		if !before.Status.UpdateProgress.UpdateStartedAt.Equal(&pcs.Status.UpdateProgress.UpdateStartedAt) {
			return false, fmt.Errorf("PCS scaling restarted recreation")
		}
		for i, replicaProgress := range pcs.Status.UpdateProgress.CurrentlyUpdating {
			if !before.Status.UpdateProgress.CurrentlyUpdating[i].UpdateStartedAt.Equal(&replicaProgress.UpdateStartedAt) {
				return false, fmt.Errorf("PCS scaling restarted replacement of replica %d", replicaProgress.ReplicaIndex)
			}
		}
		return reached(pcs)
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
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

// waitForRecreationScaleIn checks each PCS event until the specified non-target replicas have been
// selected and fully removed. The held replacement and scheduling target must stay unchanged throughout.
// The watch must start before scale-in and is stopped on return.
func waitForRecreationScaleIn(
	ctx context.Context,
	watcher watch.Interface,
	before *grovev1beta1.PodCliqueSet,
	pcsReplicaIndices []int32,
) (*grovev1beta1.PodCliqueSet, error) {
	wantPCSReplicaIndices := sets.New(pcsReplicaIndices...)
	selectedPCSReplicaIndices := sets.New[int32]()
	wantPCSReplicas := before.Spec.Replicas - int32(wantPCSReplicaIndices.Len())
	pcs, err := waitForRecreationScaling(ctx, watcher, before, func(pcs *grovev1beta1.PodCliqueSet) (bool, error) {
		if scalingProgress := pcs.Status.ScalingProgress; scalingProgress != nil {
			if len(scalingProgress.ScaleOutReplicaIndices) != 0 {
				return false, fmt.Errorf("unexpected scale-out during recreation scale-in: %v", scalingProgress.ScaleOutReplicaIndices)
			}
			for _, pcsReplicaIndex := range scalingProgress.ScaleInReplicaIndices {
				if !wantPCSReplicaIndices.Has(pcsReplicaIndex) {
					return false, fmt.Errorf("scale-in selected PCS replica %d, expected only %v", pcsReplicaIndex, pcsReplicaIndices)
				}
				selectedPCSReplicaIndices.Insert(pcsReplicaIndex)
			}
		}
		return selectedPCSReplicaIndices.Equal(wantPCSReplicaIndices) &&
			pcs.Spec.Replicas == wantPCSReplicas && pcs.Status.Replicas == wantPCSReplicas &&
			pcs.Status.ScalingProgress == nil, nil
	})
	if err != nil {
		return nil, fmt.Errorf("waiting for recreation scale-in of PCS replicas %v: %w", pcsReplicaIndices, err)
	}
	return pcs, nil
}

// waitForNotReadyPodAtRevision waits for a live workload pod at the requested UPDATE_TRIGGER revision
// that remains NotReady. This makes the first template update observable before another change supersedes it.
func waitForNotReadyPodAtRevision(tc *testctx.TestContext, updateValue string) error {
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		var workloadPods corev1.PodList
		if err := tc.Client.List(ctx, &workloadPods, client.InNamespace(tc.Namespace),
			client.MatchingLabels{apicommon.LabelPartOfKey: tc.Workload.Name}); err != nil {
			return false, err
		}
		for _, pod := range workloadPods.Items {
			if pod.DeletionTimestamp != nil || kubeutils.IsPodReady(&pod) {
				continue
			}
			for _, env := range pod.Spec.Containers[0].Env {
				if env.Name == "UPDATE_TRIGGER" && env.Value == updateValue {
					return true, nil
				}
			}
		}
		return false, nil
	})
}

// waitForInFlightCoherentEpochs waits until the active replica has recorded coherent epochs, so
// supersession can check that existing epoch references are cleared rather than checking an empty field.
func waitForInFlightCoherentEpochs(tc *testctx.TestContext) error {
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		var pcs grovev1beta1.PodCliqueSet
		key := types.NamespacedName{Namespace: tc.Namespace, Name: tc.Workload.Name}
		if err := tc.Client.Get(ctx, key, &pcs); err != nil {
			return false, err
		}
		if pcs.Status.UpdateProgress == nil {
			return false, nil
		}
		for _, replicaProgress := range pcs.Status.UpdateProgress.CurrentlyUpdating {
			if replicaProgress.UpdateEndedAt == nil && len(replicaProgress.InFlightEpochs) != 0 {
				return true, nil
			}
		}
		return false, nil
	})
}

// assertWorkload2MidCoherentPodGangMap checks the full first pc-a-only coherent sub-step while its new
// pod remains NotReady. The old anchor and the new anchor each carry one pc-a pod. The sg-x indices
// remain in the original anchor and tail, and the empty ScaleOut retains its original identity.
func assertWorkload2MidCoherentPodGangMap(t *testing.T, before, after replicaRecreationSnapshot) {
	t.Helper()
	oldHash := *before.PCS.Status.CurrentGenerationHash
	newHash := *after.PCS.Status.CurrentGenerationHash
	require.NotEqual(t, oldHash, newHash)
	updateutils.AssertReplicaPodGangMap(t, before.PGM.Spec.Entries, updateutils.ExpectedReplicaPodGangMap{
		GenerationHash: oldHash, StandalonePodCounts: map[string]int32{"pc-a": 2},
		PCSGName: "sg-x", AnchorIndices: []int32{0}, TailIndices: []int32{1},
	})

	wantOldAnchor := updateutils.RequireSingleEntryByRole(t, before.PGM.Spec.Entries, grovev1beta1.PodGangEntryRoleAnchor)
	wantOldAnchor.PodCliques = map[string]int32{"pc-a": 1}
	wantTail := updateutils.RequireSingleEntryByRole(t, before.PGM.Spec.Entries, grovev1beta1.PodGangEntryRoleTail)
	// sg-x is outside the template update scope, so its tail advances without moving its replica index.
	wantTail.PodCliqueSetGenerationHash = newHash
	wantScaleOut := updateutils.RequireSingleEntryByRole(t, before.PGM.Spec.Entries, grovev1beta1.PodGangEntryRoleScaleOut)

	inFlightEpochs := after.PCS.Status.UpdateProgress.CurrentlyUpdating[0].InFlightEpochs
	require.Len(t, inFlightEpochs, 1, "the first coherent sub-step must track its new anchor epoch")
	for _, entry := range before.PGM.Spec.Entries {
		require.NotEqual(t, entry.Epoch, inFlightEpochs[0], "the coherent update must create a new anchor epoch")
	}
	wantNewAnchor := grovev1beta1.PodGangEntry{
		Epoch: inFlightEpochs[0], Role: grovev1beta1.PodGangEntryRoleAnchor,
		PodCliqueSetGenerationHash: newHash, PodCliques: map[string]int32{"pc-a": 1},
	}
	require.ElementsMatch(t, []grovev1beta1.PodGangEntry{wantOldAnchor, wantTail, wantScaleOut, wantNewAnchor},
		after.PGM.Spec.Entries, "the blocked coherent update must retain the exact old and new entry composition")
}

// requirePodTemplateChangesRejectedDuringRecreation watches the commitment throughout rejected
// standalone and PCSG-member edits. Existing member PodCliques must retain their specs and UIDs.
func requirePodTemplateChangesRejectedDuringRecreation(tc *testctx.TestContext, before *grovev1beta1.PodCliqueSet, revision string) {
	tc.T.Helper()
	var beforeCliques grovev1beta1.PodCliqueList
	require.NoError(tc.T, tc.Client.List(tc.Ctx, &beforeCliques, client.InNamespace(tc.Namespace),
		client.MatchingLabels{apicommon.LabelPartOfKey: tc.Workload.Name}))
	watchCtx, cancel := context.WithTimeout(tc.Ctx, 2*tc.Interval)
	defer cancel()
	watcher, err := watchPodCliqueSet(watchCtx, tc)
	require.NoError(tc.T, err)
	defer watcher.Stop()
	for _, names := range [][]string{{"pc-a"}, {"pc-b"}, {"pc-c"}, {"pc-a", "pc-b", "pc-c"}} {
		err := updateutils.TriggerPodCliqueUpdateWithRevision(tc, revision, names...)
		require.ErrorContains(tc.T, err, "admission webhook", "template edit for %v must fail admission", names)
		require.ErrorContains(tc.T, err, "pod templates cannot be changed while PCS replica recreation is in progress")
		require.ErrorContains(tc.T, err, "Retry after recreation completes")
	}
	validate := func(pcs *grovev1beta1.PodCliqueSet) error {
		if pcs.Generation != before.Generation || !assert.ObjectsAreEqual(before.Spec, pcs.Spec) ||
			!assert.ObjectsAreEqual(before.Status.CurrentGenerationHash, pcs.Status.CurrentGenerationHash) {
			return fmt.Errorf("rejected template edit changed the PCS spec or generation")
		}
		if err := validateRecreationWhileBlocked(before, pcs); err != nil {
			return err
		}
		if !before.Status.UpdateProgress.UpdateStartedAt.Equal(&pcs.Status.UpdateProgress.UpdateStartedAt) {
			return fmt.Errorf("rejected template edit restarted replica recreation")
		}
		for i, progress := range pcs.Status.UpdateProgress.CurrentlyUpdating {
			if !before.Status.UpdateProgress.CurrentlyUpdating[i].UpdateStartedAt.Equal(&progress.UpdateStartedAt) {
				return fmt.Errorf("rejected template edit restarted replacement for PCS replica %d", progress.ReplicaIndex)
			}
		}
		return nil
	}
	_, err = watchtools.UntilWithoutRetry(watchCtx, watcher, func(event watch.Event) (bool, error) {
		switch event.Type {
		case watch.Error:
			return false, apierrors.FromObject(event.Object)
		case watch.Deleted:
			return false, fmt.Errorf("PCS deleted after rejected template edit")
		case watch.Added, watch.Modified:
		default:
			return false, nil
		}
		pcs, ok := event.Object.(*grovev1beta1.PodCliqueSet)
		if !ok {
			return false, fmt.Errorf("unexpected PCS watch object %T", event.Object)
		}
		return false, validate(pcs)
	})
	require.True(tc.T, wait.Interrupted(err), "recreation changed or the watch ended before the observation window: %v", err)
	require.ErrorIs(tc.T, watchCtx.Err(), context.DeadlineExceeded)
	after, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(tc.T, err)
	require.NoError(tc.T, validate(after))
	for _, beforeClique := range beforeCliques.Items {
		var afterClique grovev1beta1.PodClique
		require.NoError(tc.T, tc.Client.Get(tc.Ctx, client.ObjectKeyFromObject(&beforeClique), &afterClique))
		require.Equal(tc.T, beforeClique.UID, afterClique.UID, "%s must not be recreated", beforeClique.Name)
		require.Equal(tc.T, beforeClique.Spec, afterClique.Spec, "%s spec must remain unchanged", beforeClique.Name)
	}
}

// requireWorkload2TemplateUpdateAfterRecreation checks that retrying the template edit starts an ordinary
// update. KWOK makes that update observable before it completes, and every final index must converge.
func requireWorkload2TemplateUpdateAfterRecreation(tc *testctx.TestContext, before []replicaRecreationSnapshot, revision string) {
	tc.T.Helper()
	pcsBefore := before[0].PCS
	require.NoError(tc.T, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path))
	defer func() {
		assert.NoError(tc.T, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
	}()
	require.NoError(tc.T, updateutils.TriggerPodCliqueUpdateWithRevision(tc, revision, "pc-a", "pc-b", "pc-c"))
	require.NoError(tc.T, updateutils.WaitForGenerationHashChange(tc, *pcsBefore.Status.CurrentGenerationHash))
	require.NoError(tc.T, waitForNotReadyPodAtRevision(tc, revision))
	if pcsBefore.Spec.UpdateStrategy.Type == grovev1beta1.CoherentStrategy {
		updateutils.WaitForReplicaMidCoherentRoll(tc.T, tc, 0)
		require.NoError(tc.T, waitForInFlightCoherentEpochs(tc))
	}
	active, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(tc.T, err)
	require.NotNil(tc.T, active.Status.UpdateProgress)
	require.Nil(tc.T, active.Status.UpdateProgress.UpdateEndedAt)
	require.False(tc.T, active.Status.UpdateProgress.RequiresReplicaRecreation, "retry must use the configured template-update strategy")
	require.Equal(tc.T, pcsBefore.Status.AppliedSchedulingConfigurationHash, active.Status.AppliedSchedulingConfigurationHash)
	require.Equal(tc.T, *pcsBefore.Status.AppliedSchedulingConfigurationHash, active.Status.UpdateProgress.TargetSchedulingConfigurationHash)
	for _, selection := range active.Status.UpdateProgress.CurrentlyUpdating {
		require.Nil(tc.T, selection.Replacement, "ordinary template update must not select a replacement")
	}
	if pcsBefore.Spec.UpdateStrategy.Type == grovev1beta1.CoherentStrategy {
		require.Equal(tc.T, []string{"pc-a"}, active.Status.UpdateProgress.InScopeStandalonePodCliques)
		require.Equal(tc.T, []string{"sg-x"}, active.Status.UpdateProgress.InScopePodCliqueScalingGroups)
	} else {
		pgm, err := updateutils.GetReplicaPodGangMap(tc, 0)
		require.NoError(tc.T, err)
		updateutils.AssertEntriesUnchangedExceptHashForNonCoherentUpdate(tc.T, before[0].PGM.Spec.Entries, pgm.Spec.Entries, *active.Status.CurrentGenerationHash)
	}
	for pcsReplicaIndex, snapshot := range before {
		assertReplicaResourceUIDsUnchanged(tc.T, snapshot, requireReplicaSnapshotForIndex(tc.T, tc, pcsReplicaIndex))
	}
	require.NoError(tc.T, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name))
	updateutils.DeleteNotReadyPodsOnReplica(tc.T, tc, 0)
	require.NoError(tc.T, updateutils.WaitForRollingUpdateComplete(tc, pcsBefore.Spec.Replicas))
	require.NoError(tc.T, tc.WaitForPods(10*len(before)))
	pcsReplicaIndices := make([]int, 0, len(before))
	for pcsReplicaIndex, snapshot := range before {
		pcsReplicaIndices = append(pcsReplicaIndices, pcsReplicaIndex)
		assertReplicaResourceUIDsUnchanged(tc.T, snapshot, requireReplicaSnapshotForIndex(tc.T, tc, pcsReplicaIndex))
	}
	updateutils.AssertGenerationHashConverged(tc)
	updateutils.AssertNoActiveChildUpdates(tc)
	assertLatestPodTemplatesOnPCSReplicas(tc, pcsReplicaIndices)
	for _, cliqueName := range []string{"pc-a", "pc-b", "pc-c"} {
		updateutils.AssertAffectedPodsAtRevision(tc, cliqueName, revision)
	}
}

// assertLatestPodTemplatesOnPCSReplicas checks every standalone and group-member PodClique against
// the current PCS PodSpec. It requires the exact child counts and verifies every pod uses its latest hash.
func assertLatestPodTemplatesOnPCSReplicas(tc *testctx.TestContext, pcsReplicaIndices []int) {
	tc.T.Helper()
	pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(tc.T, err)
	require.NotNil(tc.T, pcs.Status.CurrentGenerationHash)
	wantPCLQCountsByCliqueName := make(map[string]int, len(pcs.Spec.Template.Cliques))
	for _, clique := range pcs.Spec.Template.Cliques {
		wantPCLQCountsByCliqueName[clique.Name] = 1
	}
	for _, config := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		require.NotNil(tc.T, config.Replicas, "group %s must have a configured replica count", config.Name)
		for _, cliqueName := range config.CliqueNames {
			wantPCLQCountsByCliqueName[cliqueName] = int(*config.Replicas)
		}
	}
	wantPCSGEnvironmentByCliqueName := expectedPCSGEnvironment(tc.T, pcs)
	for _, pcsReplicaIndex := range pcsReplicaIndices {
		labels := client.MatchingLabels{
			apicommon.LabelPartOfKey: tc.Workload.Name, apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(pcsReplicaIndex),
		}
		var pclqs grovev1beta1.PodCliqueList
		require.NoError(tc.T, tc.Client.List(tc.Ctx, &pclqs, client.InNamespace(tc.Namespace), labels))
		pclqCountsByCliqueName := make(map[string]int, len(wantPCLQCountsByCliqueName))
		wantPodTemplateHashByPCLQName := make(map[string]string, len(pclqs.Items))
		wantPodCount := 0
		for _, pclq := range pclqs.Items {
			require.Nil(tc.T, pclq.DeletionTimestamp, "PodClique %s must not be terminating", pclq.Name)
			cliqueName, err := componentutils.GetPodCliqueNameFromPodCliqueFQN(pclq.ObjectMeta)
			require.NoError(tc.T, err)
			template := componentutils.FindPodCliqueTemplateSpecByName(pcs, cliqueName)
			require.NotNil(tc.T, template, "PodClique %s must have a PCS template", pclq.Name)
			pclqCountsByCliqueName[cliqueName]++
			wantPodSpec := template.Spec.PodSpec.DeepCopy()
			if envVars, isPCSGMember := wantPCSGEnvironmentByCliqueName[cliqueName]; isPCSGMember {
				componentutils.PrependEnvVarsToContainers(wantPodSpec.Containers, envVars)
				componentutils.PrependEnvVarsToContainers(wantPodSpec.InitContainers, envVars)
			}
			require.Equal(tc.T, *wantPodSpec, pclq.Spec.PodSpec, "replica %d PodClique %s must use the latest full PodSpec", pcsReplicaIndex, pclq.Name)
			wantHash := componentutils.ComputePCLQPodTemplateHash(template, pcs.Spec.Template.PriorityClassName)
			require.Equal(tc.T, wantHash, pclq.Labels[apicommon.LabelPodTemplateHash], "PodClique %s template hash", pclq.Name)
			require.Equal(tc.T, &wantHash, pclq.Status.CurrentPodTemplateHash, "PodClique %s completed template hash", pclq.Name)
			require.Equal(tc.T, *pcs.Status.CurrentGenerationHash, pclq.Labels[apicommon.LabelPodCliqueSetGenerationHash], "PodClique %s applied generation", pclq.Name)
			require.Equal(tc.T, pcs.Status.CurrentGenerationHash, pclq.Status.CurrentPodCliqueSetGenerationHash, "PodClique %s completed generation", pclq.Name)
			wantPodTemplateHashByPCLQName[pclq.Name] = wantHash
			wantPodCount += int(pclq.Spec.Replicas)
		}
		require.Equal(tc.T, wantPCLQCountsByCliqueName, pclqCountsByCliqueName, "replica %d must have every expected standalone and group-member PodClique", pcsReplicaIndex)
		var pods corev1.PodList
		require.NoError(tc.T, tc.Client.List(tc.Ctx, &pods, client.InNamespace(tc.Namespace), labels))
		require.Len(tc.T, pods.Items, wantPodCount, "replica %d must have every expected pod", pcsReplicaIndex)
		for _, pod := range pods.Items {
			require.Nil(tc.T, pod.DeletionTimestamp, "Pod %s must not be terminating", pod.Name)
			wantHash, found := wantPodTemplateHashByPCLQName[pod.Labels[apicommon.LabelPodClique]]
			require.True(tc.T, found, "Pod %s must belong to an expected PodClique", pod.Name)
			require.Equal(tc.T, wantHash, pod.Labels[apicommon.LabelPodTemplateHash], "replica %d Pod %s must use the latest template", pcsReplicaIndex, pod.Name)
		}
	}
}

// expectedPCSGEnvironment describes the Grove runtime variables added to each group-member template.
// Including these variables preserves an exact PodSpec comparison instead of ignoring generated fields.
func expectedPCSGEnvironment(t *testing.T, pcs *grovev1beta1.PodCliqueSet) map[string][]corev1.EnvVar {
	t.Helper()
	environmentByCliqueName := make(map[string][]corev1.EnvVar, len(pcs.Spec.Template.Cliques))
	for _, config := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		templatePodCount := 0
		for _, cliqueName := range config.CliqueNames {
			template := componentutils.FindPodCliqueTemplateSpecByName(pcs, cliqueName)
			require.NotNil(t, template, "group %s member %s must have a PCS template", config.Name, cliqueName)
			templatePodCount += int(template.Spec.Replicas)
		}
		envVars := []corev1.EnvVar{
			{
				Name: apiconstants.EnvVarPodCliqueScalingGroupName,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: fmt.Sprintf("metadata.labels['%s']", apicommon.LabelPodCliqueScalingGroup),
					},
				},
			},
			{Name: apiconstants.EnvVarPodCliqueScalingGroupTemplateNumPods, Value: strconv.Itoa(templatePodCount)},
			{
				Name: apiconstants.EnvVarPodCliqueScalingGroupIndex,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: fmt.Sprintf("metadata.labels['%s']", apicommon.LabelPodCliqueScalingGroupReplicaIndex),
					},
				},
			},
		}
		for _, cliqueName := range config.CliqueNames {
			environmentByCliqueName[cliqueName] = envVars
		}
	}
	return environmentByCliqueName
}

// waitForWorkload2ReplicaAtGeneration waits for pc-a, sg-x, and the full replacement PodGangMap
// to materialize at the requested generation. Standalone pod readiness is checked separately.
func waitForWorkload2ReplicaAtGeneration(tc *testctx.TestContext, pcsReplicaIndex int, pcsGenerationHash string, expectedEntryCount int) error {
	pcsReplica := apicommon.ResourceNameReplica{Name: tc.Workload.Name, Replica: pcsReplicaIndex}
	pclqKey := client.ObjectKey{Namespace: tc.Namespace, Name: apicommon.GeneratePodCliqueName(pcsReplica, "pc-a")}
	pcsgKey := client.ObjectKey{Namespace: tc.Namespace, Name: updateutils.PCSGFQNForReplica(tc, "sg-x", pcsReplicaIndex)}
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		var pclq grovev1beta1.PodClique
		if err := tc.Client.Get(ctx, pclqKey, &pclq); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		var pcsg grovev1beta1.PodCliqueScalingGroup
		if err := tc.Client.Get(ctx, pcsgKey, &pcsg); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		if pclq.DeletionTimestamp != nil || pcsg.DeletionTimestamp != nil ||
			pclq.Labels[apicommon.LabelPodCliqueSetGenerationHash] != pcsGenerationHash ||
			pcsg.Labels[apicommon.LabelPodCliqueSetGenerationHash] != pcsGenerationHash {
			return false, nil
		}
		pgm, err := updateutils.GetReplicaPodGangMap(tc, pcsReplicaIndex)
		if err != nil {
			return false, client.IgnoreNotFound(err)
		}
		if pgm.DeletionTimestamp != nil || len(pgm.Spec.Entries) != expectedEntryCount {
			return false, nil
		}
		for _, entry := range pgm.Spec.Entries {
			if entry.PodCliqueSetGenerationHash != pcsGenerationHash {
				return false, nil
			}
		}
		return true, nil
	})
}

// waitForReplicaResourcesDeleted waits until no resources in the requested lists match the replica labels.
// Without explicit lists it checks PodCliques, PCSGs, PodGangMaps, PodGangs, Pods, and Services. PCS-wide
// resources such as the ServiceAccount and Role remain when only replicas are scaled to zero.
func waitForReplicaResourcesDeleted(tc *testctx.TestContext, labels client.MatchingLabels, resourceLists ...client.ObjectList) error {
	if len(resourceLists) == 0 {
		resourceLists = []client.ObjectList{
			&grovev1beta1.PodCliqueList{}, &grovev1beta1.PodCliqueScalingGroupList{}, &grovev1beta1.PodGangMapList{},
			&groveschedulerv1alpha1.PodGangList{}, &corev1.PodList{}, &corev1.ServiceList{},
		}
	}
	return wait.PollUntilContextTimeout(tc.Ctx, tc.Interval, tc.Timeout, true, func(ctx context.Context) (bool, error) {
		for _, resourceList := range resourceLists {
			if err := tc.Client.List(ctx, resourceList, client.InNamespace(tc.Namespace), labels); err != nil {
				return false, fmt.Errorf("list %T during replica cleanup: %w", resourceList, err)
			}
			items, err := meta.ExtractList(resourceList)
			if err != nil {
				return false, fmt.Errorf("extract %T during replica cleanup: %w", resourceList, err)
			}
			if len(items) != 0 {
				return false, nil
			}
		}
		return true, nil
	})
}

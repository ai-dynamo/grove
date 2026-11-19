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
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/kwok"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/ai-dynamo/grove/operator/e2e/tests"
	updateutils "github.com/ai-dynamo/grove/operator/e2e/tests/update/utils"
	kubeutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	coherentWorkloadName = "workload-coherent"
	coherentWorkloadYAML = "../../yaml/workload-coherent.yaml"
	// frontend 2 + inference PCSG (2 replicas x [prefill 1 + decode 1]) = 6 pods per PCS replica.
	coherentExpectedPods = 6

	coherentGTWorkloadName = "workload-coherent-gt"
	coherentGTWorkloadYAML = "../../yaml/workload-coherent-gt.yaml"
	// Two PCS replicas of 6 pods each.
	coherentGTExpectedPods = 12
)

// Test_CU1_CoherentBootstrapLayout verifies that deploying a Coherent-strategy workload lays out the
// initial PodGangMap the same way any strategy would, since bootstrap is not an update. The anchor holds
// every frontend pod and the inference MinAvailable index, and the tail holds the remaining index.
func Test_CU1_CoherentBootstrapLayout(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-coherent and verify 6 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: coherentWorkloadName,
		workloadYAML: coherentWorkloadYAML,
		workerNodes:  10,
		expectedPods: coherentExpectedPods,
	})
	defer cleanup()

	tests.Logger.Info("2. Verify the initial PodGangMap anchor, tail, and scale-out entries")
	updateutils.AssertReplicaPodGangMap(t, updateutils.PodGangMapEntries(t, tc, 0), updateutils.ExpectedReplicaPodGangMap{
		StandalonePodCounts: map[string]int32{"frontend": 2},
		PCSGName:            "inference",
		AnchorIndices:       []int32{0},
		TailIndices:         []int32{1},
	})
}

// Test_CU2_CoherentUpdateStandaloneClique verifies that a coherent update of only the standalone frontend
// PodClique completes, clears UpdateInProgress, and advances the empty ScaleOut with its surviving
// out-of-scope anchor without changing its epoch or dependency. A later inference scale-out must reuse
// that entry, and all pods must become Ready.
func Test_CU2_CoherentUpdateStandaloneClique(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-coherent and verify 6 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: coherentWorkloadName,
		workloadYAML: coherentWorkloadYAML,
		workerNodes:  10,
		expectedPods: coherentExpectedPods,
	})
	defer cleanup()

	beforePGM, err := updateutils.GetReplicaPodGangMap(tc, 0)
	require.NoError(t, err)
	beforeScaleOut := updateutils.RequireSingleEntryByRole(t, beforePGM.Spec.Entries, grovev1beta1.PodGangEntryRoleScaleOut)
	beforeAnchor := updateutils.RequireSingleEntryByRole(t, beforePGM.Spec.Entries, grovev1beta1.PodGangEntryRoleAnchor)
	require.Empty(t, beforeScaleOut.PodCliques)
	require.Empty(t, beforeScaleOut.PCSGReplicaIndices)
	require.Equal(t, []string{beforeAnchor.Epoch}, beforeScaleOut.DependsOn)
	require.Equal(t, beforeAnchor.PodCliqueSetGenerationHash, beforeScaleOut.PodCliqueSetGenerationHash)

	tests.Logger.Info("2. Trigger a coherent update of the frontend PodClique")
	if err := updateutils.TriggerPodCliqueUpdate(tc, "frontend"); err != nil {
		t.Fatalf("failed to trigger update of frontend: %v", err)
	}

	tests.Logger.Info("3. Wait for the coherent update to complete")
	if err := updateutils.WaitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("coherent update did not complete: %v", err)
	}

	tests.Logger.Info("4. Verify completion, convergence, and readiness")
	updateutils.AssertUpdateInProgressCleared(tc)
	updateutils.AssertGenerationHashConverged(tc)
	updateutils.AssertPodGangMapSingleGeneration(t, tc)
	if err := tc.WaitForPods(coherentExpectedPods); err != nil {
		t.Fatalf("pods did not become Ready after the coherent update: %v", err)
	}
	afterPGM, err := updateutils.GetReplicaPodGangMap(tc, 0)
	require.NoError(t, err)
	wantScaleOut := beforeScaleOut
	wantScaleOut.PodCliqueSetGenerationHash = updateutils.PCSGenerationHash(t, tc)
	require.NotEqual(t, beforeScaleOut.PodCliqueSetGenerationHash, wantScaleOut.PodCliqueSetGenerationHash)
	require.Equal(t, wantScaleOut, updateutils.RequireSingleEntryByRole(t, afterPGM.Spec.Entries, grovev1beta1.PodGangEntryRoleScaleOut),
		"the empty ScaleOut must advance with its original anchor without changing identity")

	tests.Logger.Info("5. Scale inference to three replicas and verify the preserved ScaleOut is reused")
	tc.ScalePCSGAcrossAllReplicasAndWait(coherentWorkloadName, "inference", 1, 3, coherentExpectedPods+2, 0)
	wantScaleOut.PCSGReplicaIndices = map[string][]int32{"inference": {2}}
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		pgm, err := updateutils.GetReplicaPodGangMap(tc, 0)
		if !assert.NoError(collect, err) {
			return
		}
		scaleOuts := make([]grovev1beta1.PodGangEntry, 0, 1)
		for _, entry := range pgm.Spec.Entries {
			if entry.Role == grovev1beta1.PodGangEntryRoleScaleOut {
				scaleOuts = append(scaleOuts, entry)
			}
		}
		if assert.Len(collect, scaleOuts, 1) {
			assert.Equal(collect, wantScaleOut, scaleOuts[0], "scale-out must reuse the original epoch and dependency")
		}
	}, tc.Timeout, tc.Interval)
}

// Test_CU3_CoherentUpdatePCSGMemberClique verifies that a coherent update of only the inference
// PodCliqueScalingGroup, triggered by changing its prefill member, completes, clears UpdateInProgress, and
// converges every component to the latest generation hash.
func Test_CU3_CoherentUpdatePCSGMemberClique(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-coherent and verify 6 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: coherentWorkloadName,
		workloadYAML: coherentWorkloadYAML,
		workerNodes:  10,
		expectedPods: coherentExpectedPods,
	})
	defer cleanup()

	tests.Logger.Info("2. Trigger a coherent update of the inference PCSG via its prefill member")
	if err := updateutils.TriggerPodCliqueUpdate(tc, "prefill"); err != nil {
		t.Fatalf("failed to trigger update of prefill: %v", err)
	}

	tests.Logger.Info("3. Wait for the coherent update to complete")
	if err := updateutils.WaitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("coherent update did not complete: %v", err)
	}

	tests.Logger.Info("4. Verify completion, convergence, and readiness")
	updateutils.AssertUpdateInProgressCleared(tc)
	updateutils.AssertGenerationHashConverged(tc)
	if err := tc.WaitForPods(coherentExpectedPods); err != nil {
		t.Fatalf("pods did not become Ready after the coherent update: %v", err)
	}

	tests.Logger.Info("5. Verify the frontend anchor, out of the update scope, reconverged to a single generation")
	updateutils.AssertPodGangMapSingleGeneration(t, tc)

	tests.Logger.Info("6. Scale the frontend standalone clique from 2 to 3 and verify the new pod is created")
	tc.ScalePodCliqueAndWait(coherentWorkloadName+"-0-frontend", 3, coherentExpectedPods+1, 0)
}

// Test_CU4_CoherentUpdateStandaloneAndPCSGAnchorOnly verifies that a coherent update of both the standalone
// frontend and the inference PCSG, whose replica-to-MinAvailable ratios match, rolls only anchor-bearing
// steps and no leftover step. frontend has 2 replicas at MinAvailable 1 and inference has 2 replicas at
// MinAvailable 1, so the plan is 2 anchor-bearing steps that each roll one frontend pod and one inference
// index. The converged PodGangMap holds two new-hash anchors, one per Minimum Viable Unit, and no tail.
func Test_CU4_CoherentUpdateStandaloneAndPCSGAnchorOnly(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-coherent and verify 6 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: coherentWorkloadName,
		workloadYAML: coherentWorkloadYAML,
		workerNodes:  10,
		expectedPods: coherentExpectedPods,
	})
	defer cleanup()

	tests.Logger.Info("2. Trigger a coherent update of both frontend and the inference PCSG")
	for _, cliqueName := range []string{"frontend", "prefill"} {
		if err := updateutils.TriggerPodCliqueUpdate(tc, cliqueName); err != nil {
			t.Fatalf("failed to trigger update of %s: %v", cliqueName, err)
		}
	}

	tests.Logger.Info("3. Wait for the coherent update to complete")
	if err := updateutils.WaitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("coherent update did not complete: %v", err)
	}
	updateutils.AssertUpdateInProgressCleared(tc)
	updateutils.AssertGenerationHashConverged(tc)

	tests.Logger.Info("4. Verify the converged PodGangMap holds two Minimum Viable Unit anchors and no leftover tail")
	newHash := updateutils.PCSGenerationHash(t, tc)
	entries := updateutils.PodGangMapEntries(t, tc, 0)
	assertCoherentAnchorCompositions(t, entries, newHash, "inference", []coherentAnchor{
		{standalone: map[string]int32{"frontend": 1}, pcsgIndices: []int32{0}},
		{standalone: map[string]int32{"frontend": 1}, pcsgIndices: []int32{1}},
	})
	assert.Empty(t, newHashTailPCSGIndices(entries, newHash, "inference"), "an anchor-only plan must leave no leftover tail")
}

// Test_CU5_CoherentUpdateStandaloneAndPCSGWithLeftover verifies that when a component's replica count
// exceeds what the anchor-bearing steps roll, the coherent update finishes with a single leftover step.
// Scaling inference to 3 replicas caps the plan at 2 anchor-bearing steps, matching frontend, so inference
// index 2 is leftover. The converged PodGangMap holds two new-hash anchors and one new-hash tail carrying
// the leftover inference index.
func Test_CU5_CoherentUpdateStandaloneAndPCSGWithLeftover(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-coherent and verify 6 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: coherentWorkloadName,
		workloadYAML: coherentWorkloadYAML,
		workerNodes:  10,
		expectedPods: coherentExpectedPods,
	})
	defer cleanup()

	tests.Logger.Info("2. Scale the inference PCSG from 2 to 3 so a leftover step is required (8 pods)")
	tc.ScalePCSGAcrossAllReplicasAndWait(coherentWorkloadName, "inference", 1, 3, 8, 0)

	tests.Logger.Info("3. Trigger a coherent update of both frontend and the inference PCSG")
	for _, cliqueName := range []string{"frontend", "prefill"} {
		if err := updateutils.TriggerPodCliqueUpdate(tc, cliqueName); err != nil {
			t.Fatalf("failed to trigger update of %s: %v", cliqueName, err)
		}
	}

	tests.Logger.Info("4. Wait for the coherent update to complete")
	if err := updateutils.WaitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("coherent update did not complete: %v", err)
	}
	updateutils.AssertUpdateInProgressCleared(tc)
	updateutils.AssertGenerationHashConverged(tc)

	tests.Logger.Info("5. Verify two anchors plus a leftover tail carrying inference index 2")
	newHash := updateutils.PCSGenerationHash(t, tc)
	entries := updateutils.PodGangMapEntries(t, tc, 0)
	assertCoherentAnchorCompositions(t, entries, newHash, "inference", []coherentAnchor{
		{standalone: map[string]int32{"frontend": 1}, pcsgIndices: []int32{0}},
		{standalone: map[string]int32{"frontend": 1}, pcsgIndices: []int32{1}},
	})
	assert.Equal(t, []int32{2}, newHashTailPCSGIndices(entries, newHash, "inference"), "the leftover step must roll inference index 2 into a tail")
}

// Test_CU6_CoherentUpdateBlocksScaling verifies that the validating webhooks reject a replica change on a
// PodClique and on a PodCliqueScalingGroup, whether made directly or through the scale subresource, while a
// coherent update is in progress on the owning PodCliqueSet, and allow it once the update completes. Scaling
// is blocked on every replica for the update's duration, so with two replicas the test asserts both the
// updating replica 0 and the idle replica 1 are rejected. The readiness-delay stage keeps the update in
// progress long enough to attempt the scales.
func Test_CU6_CoherentUpdateBlocksScaling(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-coherent and verify 6 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: coherentWorkloadName,
		workloadYAML: coherentWorkloadYAML,
		// Two PCS replicas need 12 pods, and each 80Mi pod takes a whole 150Mi KWOK node, so the test needs
		// at least 12 schedulable nodes.
		workerNodes:  14,
		expectedPods: coherentExpectedPods,
	})
	defer cleanup()

	tests.Logger.Info("2. Scale the PodCliqueSet to two replicas and verify 12 pods")
	tc.ScalePCSAndWait(coherentWorkloadName, 2, coherentExpectedPods*2, 0)

	tests.Logger.Info("3. Delay pod readiness so the coherent update stays in progress")
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyDelayedPath); err != nil {
		t.Fatalf("failed to apply readiness-delay KWOK stage: %v", err)
	}
	defer func() {
		if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
			t.Errorf("failed to delete readiness-delay KWOK stage: %v", err)
		}
	}()

	tests.Logger.Info("4. Trigger a coherent update and wait until replica 0 is updating")
	if err := updateutils.TriggerPodCliqueUpdate(tc, "frontend"); err != nil {
		t.Fatalf("failed to trigger update of frontend: %v", err)
	}
	if err := updateutils.WaitForOrdinalUpdating(tc, 0); err != nil {
		t.Fatalf("replica 0 did not start updating: %v", err)
	}

	tests.Logger.Info("5. A replica change is rejected on both the updating replica 0 and the idle replica 1")
	for _, replicaIndex := range []int{0, 1} {
		pclqScaleErr := tc.ScalePodClique(fmt.Sprintf("%s-%d-frontend", coherentWorkloadName, replicaIndex), 3)
		if assert.Errorf(t, pclqScaleErr, "a direct PodClique replica change on replica %d must be rejected while a coherent update is in progress", replicaIndex) {
			assert.Contains(t, pclqScaleErr.Error(), "coherent update is in progress")
		}
		pcsgScaleErr := tc.ScalePCSG(updateutils.PCSGFQNForReplica(tc, "inference", replicaIndex), 3)
		if assert.Errorf(t, pcsgScaleErr, "a PodCliqueScalingGroup scale change on replica %d must be rejected while a coherent update is in progress", replicaIndex) {
			assert.Contains(t, pcsgScaleErr.Error(), "coherent update is in progress")
		}
	}

	tests.Logger.Info("6. Remove the readiness delay and let the update complete")
	if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
		t.Fatalf("failed to delete readiness-delay KWOK stage: %v", err)
	}
	if err := updateutils.WaitForRollingUpdateComplete(tc, 2); err != nil {
		t.Fatalf("coherent update did not complete: %v", err)
	}

	tests.Logger.Info("7. Scaling the PodClique and the PodCliqueScalingGroup is allowed once the update has completed")
	if err := tc.ScalePodClique(fmt.Sprintf("%s-0-frontend", coherentWorkloadName), 3); err != nil {
		t.Fatalf("scaling the PodClique must be allowed after the coherent update completes: %v", err)
	}
	if err := tc.ScalePCSG(updateutils.PCSGFQNForReplica(tc, "inference", 0), 3); err != nil {
		t.Fatalf("scaling the PodCliqueScalingGroup must be allowed after the coherent update completes: %v", err)
	}
}

// Test_CU7_CoherentBackToBackUpdates verifies that a second coherent update triggered while a first is
// still in flight completes and converges. The first update changes only the standalone frontend, which
// opens an intermediate-generation anchor carrying no PodCliqueScalingGroup indices. The second update adds
// the inference PodCliqueScalingGroup to the scope, so its drain must skip that intermediate anchor rather
// than fail on it. The readiness delay holds the first update in flight until the second is triggered.
func Test_CU7_CoherentBackToBackUpdates(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-coherent and verify 6 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: coherentWorkloadName,
		workloadYAML: coherentWorkloadYAML,
		workerNodes:  10,
		expectedPods: coherentExpectedPods,
	})
	defer cleanup()

	tests.Logger.Info("2. Delay pod readiness so the first update stays in flight")
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyDelayedPath); err != nil {
		t.Fatalf("failed to apply readiness-delay KWOK stage: %v", err)
	}
	defer func() {
		if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
			t.Errorf("failed to delete readiness-delay KWOK stage: %v", err)
		}
	}()

	tests.Logger.Info("3. Trigger the first coherent update of the frontend PodClique and wait until the replica is updating")
	if err := updateutils.TriggerPodCliqueUpdate(tc, "frontend"); err != nil {
		t.Fatalf("failed to trigger update of frontend: %v", err)
	}
	if err := updateutils.WaitForOrdinalUpdating(tc, 0); err != nil {
		t.Fatalf("replica 0 did not start updating: %v", err)
	}

	tests.Logger.Info("4. Trigger the second coherent update of the inference PCSG while the first is still in flight")
	if err := updateutils.TriggerPodCliqueUpdate(tc, "prefill"); err != nil {
		t.Fatalf("failed to trigger update of prefill: %v", err)
	}

	tests.Logger.Info("5. Remove the readiness delay so both updates can converge")
	if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
		t.Fatalf("failed to delete readiness-delay KWOK stage: %v", err)
	}

	tests.Logger.Info("6. Wait for the coherent update to complete and verify convergence and readiness")
	if err := updateutils.WaitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("coherent update did not complete: %v", err)
	}
	updateutils.AssertUpdateInProgressCleared(tc)
	updateutils.AssertGenerationHashConverged(tc)
	if err := tc.WaitForPods(coherentExpectedPods); err != nil {
		t.Fatalf("pods did not become Ready after the coherent updates: %v", err)
	}

	tests.Logger.Info("7. Verify the PodGangMap converged to a single generation with no stale intermediate entries")
	updateutils.AssertPodGangMapSingleGeneration(t, tc)
}

// Test_CU8_CoherentUpdateResumesAfterOperatorRestart verifies that when the Grove operator crashes mid
// coherent update it resumes from the persisted PodGangMap and status and drives the update to completion.
// The readiness-delay stage keeps the update in flight while the operator pod is deleted and rescheduled.
func Test_CU8_CoherentUpdateResumesAfterOperatorRestart(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-coherent and verify 6 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: coherentWorkloadName,
		workloadYAML: coherentWorkloadYAML,
		workerNodes:  10,
		expectedPods: coherentExpectedPods,
	})
	defer cleanup()

	tests.Logger.Info("2. Delay pod readiness so the update stays in flight")
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyDelayedPath); err != nil {
		t.Fatalf("failed to apply readiness-delay KWOK stage: %v", err)
	}
	defer func() {
		if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
			t.Errorf("failed to delete readiness-delay KWOK stage: %v", err)
		}
	}()

	tests.Logger.Info("3. Trigger a coherent update of the frontend PodClique and wait until the replica is updating")
	if err := updateutils.TriggerPodCliqueUpdate(tc, "frontend"); err != nil {
		t.Fatalf("failed to trigger update of frontend: %v", err)
	}
	if err := updateutils.WaitForOrdinalUpdating(tc, 0); err != nil {
		t.Fatalf("replica 0 did not start updating: %v", err)
	}

	tests.Logger.Info("4. Crash the operator mid-update by deleting its pod and wait for a fresh replica")
	if err := updateutils.RestartOperator(tc); err != nil {
		t.Fatalf("operator did not come back after restart: %v", err)
	}

	tests.Logger.Info("5. Remove the readiness delay so the resumed update can converge")
	if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
		t.Fatalf("failed to delete readiness-delay KWOK stage: %v", err)
	}

	tests.Logger.Info("6. Wait for the resumed update to complete and verify convergence and readiness")
	if err := updateutils.WaitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("coherent update did not complete after the operator restart: %v", err)
	}
	updateutils.AssertUpdateInProgressCleared(tc)
	updateutils.AssertGenerationHashConverged(tc)
	updateutils.AssertPodGangMapSingleGeneration(t, tc)
	if err := tc.WaitForPods(coherentExpectedPods); err != nil {
		t.Fatalf("pods did not become Ready after the resumed coherent update: %v", err)
	}
}

// coherentAnchor is the expected composition of one anchor entry a coherent update commits, its standalone
// PodClique pod counts and the inference PCSG replica indices it carries. It is matched against actual
// anchors as a multiset, so epoch and anchor index ordering do not matter.
const (
	coherentMaxUnavailWorkloadName = "workload-coherent-mu"
	coherentMaxUnavailWorkloadYAML = "../../yaml/workload-coherent-mu.yaml"
	// frontend 4 + inference PCSG (2 replicas x [prefill 1 + decode 1]) = 8 pods.
	coherentMaxUnavailExpectedPods = 8
)

// Test_CU9_CoherentUpdateNeverExceedsMaxUnavailable verifies a coherent update never leaves more than
// MaxUnavailable pods of a component unavailable at once, even while the batch it just created is not yet
// Ready. The readiness-delay stage holds new pods not-Ready, so a gate that ignored the incoming drain
// would take the next batch down on top and exceed MaxUnavailable. The drain-aware gate holds instead.
func Test_CU9_CoherentUpdateNeverExceedsMaxUnavailable(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-coherent and verify 6 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: coherentWorkloadName,
		workloadYAML: coherentWorkloadYAML,
		workerNodes:  10,
		expectedPods: coherentExpectedPods,
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

	tests.Logger.Info("3. Roll the frontend clique while sampling not-ready frontend pods")
	tcLong := *tc
	tcLong.Timeout = 2 * time.Minute
	maxUnavailable, err := updateutils.MaxUnavailablePods(&tcLong, updateutils.NotReadyPodForClique("frontend"), func() error {
		if err := updateutils.TriggerPodCliqueUpdate(&tcLong, "frontend"); err != nil {
			return err
		}
		return updateutils.WaitForRollingUpdateComplete(&tcLong, 1)
	})
	if err != nil {
		t.Fatalf("coherent update of frontend did not complete: %v", err)
	}

	tests.Logger.Info("4. Verify MaxUnavailable=1 was never exceeded")
	assert.LessOrEqualf(t, maxUnavailable, 1, "a coherent update must never leave more than MaxUnavailable=1 frontend pods unavailable at once, observed %d", maxUnavailable)
	updateutils.AssertUpdateInProgressCleared(tc)
}

// Test_CU10_CoherentUpdatePipelinesUnderDelayedReadiness verifies that when MaxUnavailable (2) exceeds
// MinAvailable (1), a coherent update keeps up to MaxUnavailable pods in flight rather than serializing on
// readiness. With readiness delayed it drives two frontend pods unavailable at once, and never more. At
// MaxUnavailable equal to MinAvailable the same measurement would be 1.
func Test_CU10_CoherentUpdatePipelinesUnderDelayedReadiness(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-coherent-mu and verify 8 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: coherentMaxUnavailWorkloadName,
		workloadYAML: coherentMaxUnavailWorkloadYAML,
		workerNodes:  12,
		expectedPods: coherentMaxUnavailExpectedPods,
	})
	defer cleanup()

	tests.Logger.Info("2. Delay pod readiness so the in-flight batches stay observable")
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyDelayedPath); err != nil {
		t.Fatalf("failed to apply readiness-delay KWOK stage: %v", err)
	}
	defer func() {
		if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
			t.Errorf("failed to delete readiness-delay KWOK stage: %v", err)
		}
	}()

	tests.Logger.Info("3. Roll the frontend clique while sampling not-ready frontend pods")
	tcLong := *tc
	tcLong.Timeout = 3 * time.Minute
	maxUnavailable, err := updateutils.MaxUnavailablePods(&tcLong, updateutils.NotReadyPodForClique("frontend"), func() error {
		if err := updateutils.TriggerPodCliqueUpdate(&tcLong, "frontend"); err != nil {
			return err
		}
		return updateutils.WaitForRollingUpdateComplete(&tcLong, 1)
	})
	if err != nil {
		t.Fatalf("coherent update of frontend did not complete: %v", err)
	}

	tests.Logger.Info("4. Verify the roll pipelined to MaxUnavailable=2 and never exceeded it")
	assert.LessOrEqualf(t, maxUnavailable, 2, "MaxUnavailable=2 must never be exceeded, observed %d", maxUnavailable)
	assert.Equalf(t, 2, maxUnavailable, "with MaxUnavailable=2 greater than MinAvailable=1 the roll should keep 2 frontend pods in flight, observed %d", maxUnavailable)
	updateutils.AssertUpdateInProgressCleared(tc)
}

// Test_CU11_GangTerminationDuringCoherentUpdateRebuildsPodGangMap verifies that when a PCS replica that is
// not the one under update is fully gang terminated during an in-flight coherent update, its PodGangMap is
// deleted and rebuilt fresh. The rebuilt replica returns to the initial single-anchor layout, while the
// updated replica keeps the multi-anchor layout the coherent update produced. This proves the map is reset
// to match a freshly redeployed replica rather than left stranded at a stale layout.
func Test_CU11_GangTerminationDuringCoherentUpdateRebuildsPodGangMap(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-coherent-gt with two PCS replicas and verify 12 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: coherentGTWorkloadName,
		workloadYAML: coherentGTWorkloadYAML,
		workerNodes:  16,
		expectedPods: coherentGTExpectedPods,
	})
	defer cleanup()
	tc.Timeout = 4 * time.Minute

	tests.Logger.Info("2. Hold replica 0's rolled frontend pod Pending so its coherent update stays in flight and the orchestrator stays parked on replica 0")
	// Gang termination is disabled for a replica while it is itself under coherent update (its
	// MinAvailableBreached goes Unknown). Keeping replica 0 parked lets us gang terminate replica 1, which
	// is not under update, reliably. Step 6 removes this stage to let replica 0's update finish.
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path); err != nil {
		t.Fatalf("failed to apply replica-0 readiness-block KWOK stage: %v", err)
	}
	defer func() {
		if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name); err != nil {
			tests.Logger.Warnf("cleanup: delete replica-0 readiness-block KWOK stage: %v", err)
		}
	}()

	tests.Logger.Info("3. Trigger a coherent update of the frontend PodClique, then wait until replica 0 is updating")
	if err := updateutils.TriggerPodCliqueUpdate(tc, "frontend"); err != nil {
		t.Fatalf("failed to trigger update of frontend: %v", err)
	}
	if err := updateutils.WaitForOrdinalUpdating(tc, 0); err != nil {
		t.Fatalf("replica 0 did not start updating: %v", err)
	}

	tests.Logger.Info("4. Crash-loop replica 1's frontend pods and gang terminate replica 1 while replica 0 is still updating, then wait for its PodGangMap to be rebuilt")
	// Crash-looping replica 1's standalone frontend keeps it below MinAvailable persistently, so the breach
	// holds past terminationDelay and the PCS-level gang termination fires for replica 1.
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageCrashloopR1Path); err != nil {
		t.Fatalf("failed to apply replica-1 crash-loop KWOK stage: %v", err)
	}
	defer func() {
		if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageCrashloopR1Name); err != nil {
			tests.Logger.Warnf("cleanup: delete replica-1 crash-loop KWOK stage: %v", err)
		}
	}()
	gangTerminateReplicaAndWaitForPodGangMapRebuild(t, tc, "frontend", 1)

	tests.Logger.Info("5. Confirm replica 0 was still under update when replica 1 was gang terminated")
	pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
	if err != nil {
		t.Fatalf("failed to read PodCliqueSet: %v", err)
	}
	if assert.NotNilf(t, pcs.Status.UpdateProgress, "expected an in-flight update") &&
		assert.Lenf(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1, "expected exactly one replica under update") {
		assert.Equalf(t, int32(0), pcs.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex, "replica 0 must still be the one under update")
		assert.Nilf(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].UpdateEndedAt, "replica 0's update must not have ended yet")
	}

	tests.Logger.Info("6. Remove the shaping stages, recover replica 1, and wait for the coherent update to complete")
	if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageCrashloopR1Name); err != nil {
		t.Fatalf("failed to delete replica-1 crash-loop KWOK stage: %v", err)
	}
	if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name); err != nil {
		t.Fatalf("failed to delete replica-0 readiness-block KWOK stage: %v", err)
	}
	// Removing the readiness-block stage does not release replica 0's already-held frontend pod (KWOK does
	// not re-evaluate a pod already sitting in the removed stage's delay), and replica 1's rebuilt frontend
	// pods crash-looped. Delete both replicas' pods so fresh ones come up Ready under the default stage and
	// the coherent update converges.
	updateutils.DeleteNotReadyPodsOnReplica(t, tc, 0)
	updateutils.DeleteNotReadyPodsOnReplica(t, tc, 1)
	if err := updateutils.WaitForRollingUpdateComplete(tc, 2); err != nil {
		t.Fatalf("coherent update did not complete: %v", err)
	}
	updateutils.AssertUpdateInProgressCleared(tc)

	tests.Logger.Info("7. Verify all 12 pods are Ready")
	if err := tc.WaitForPods(coherentGTExpectedPods); err != nil {
		t.Fatalf("pods did not become Ready after the coherent update and gang termination: %v", err)
	}
}

// Test_CU12_CoherentUpdateWithPodCliqueScalingGroupScaledToZero verifies that a coherent update completes
// when an in-scope PodCliqueScalingGroup is scaled to zero before the update. The group has no pods to
// roll, so the update rolls only the active standalone frontend, converges the group by hash, and a later
// scale-out of the group launches new-revision pods.
func Test_CU12_CoherentUpdateWithPodCliqueScalingGroupScaledToZero(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-coherent and verify 6 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: coherentWorkloadName,
		workloadYAML: coherentWorkloadYAML,
		workerNodes:  10,
		expectedPods: coherentExpectedPods,
	})
	defer cleanup()

	tests.Logger.Info("2. Scale the inference PodCliqueScalingGroup to 0 so only the frontend has pods")
	tc.ScalePCSGAcrossAllReplicasAndWait(coherentWorkloadName, "inference", 1, 0, 2, 0)

	tests.Logger.Info("3. Trigger a coherent update of frontend and the inference PCSG")
	for _, cliqueName := range []string{"frontend", "prefill"} {
		if err := updateutils.TriggerPodCliqueUpdate(tc, cliqueName); err != nil {
			t.Fatalf("failed to trigger update of %s: %v", cliqueName, err)
		}
	}

	tests.Logger.Info("4. Wait for the coherent update to complete and converge")
	if err := updateutils.WaitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("coherent update did not complete: %v", err)
	}
	updateutils.AssertUpdateInProgressCleared(tc)
	updateutils.AssertGenerationHashConverged(tc)
	updateutils.AssertPodGangMapSingleGeneration(t, tc)

	tests.Logger.Info("5. Scale the inference PCSG back to 2 and verify new-revision pods launch")
	tc.ScalePCSGAcrossAllReplicasAndWait(coherentWorkloadName, "inference", 1, 2, coherentExpectedPods, 0)
	updateutils.AssertGenerationHashConverged(tc)
	if err := tc.WaitForPods(coherentExpectedPods); err != nil {
		t.Fatalf("pods did not become Ready after scaling the inference PCSG back up: %v", err)
	}
}

// Test_CU13_CoherentUpdateWithStandaloneScaledToZero verifies that a coherent update completes when an
// in-scope standalone PodClique is scaled to zero before the update. The standalone PodClique has no pods
// to roll, yet its resource still advances to the new revision, so a later scale-out launches new-revision
// pods. No new anchor commits frontend pods while its live desired replicas are zero, even though the
// PCS template still has positive Replicas and MinAvailable. The active inference PCSG rolls normally.
func Test_CU13_CoherentUpdateWithStandaloneScaledToZero(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-coherent and verify 6 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: coherentWorkloadName,
		workloadYAML: coherentWorkloadYAML,
		workerNodes:  10,
		expectedPods: coherentExpectedPods,
	})
	defer cleanup()

	tests.Logger.Info("2. Scale the frontend PodClique to 0 so only the inference PCSG has pods")
	tc.ScalePodCliqueAndWait(coherentWorkloadName+"-0-frontend", 0, 4, 0)

	tests.Logger.Info("3. Trigger a coherent update of frontend and the inference PCSG")
	for _, cliqueName := range []string{"frontend", "prefill"} {
		if err := updateutils.TriggerPodCliqueUpdate(tc, cliqueName); err != nil {
			t.Fatalf("failed to trigger update of %s: %v", cliqueName, err)
		}
	}

	tests.Logger.Info("4. Wait for the coherent update to complete and converge")
	if err := updateutils.WaitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("coherent update did not complete: %v", err)
	}
	updateutils.AssertUpdateInProgressCleared(tc)
	updateutils.AssertGenerationHashConverged(tc)
	updateutils.AssertPodGangMapSingleGeneration(t, tc)

	pgm, err := updateutils.GetReplicaPodGangMap(tc, 0)
	require.NoError(t, err)
	for _, entry := range pgm.Spec.Entries {
		require.NotContains(t, entry.PodCliques, "frontend", "epoch %s must not commit zero-replica frontend pods", entry.Epoch)
	}
	var frontend grovev1beta1.PodClique
	require.NoError(t, tc.Client.Get(tc.Ctx, types.NamespacedName{
		Namespace: tc.Namespace, Name: coherentWorkloadName + "-0-frontend",
	}, &frontend))
	require.Equal(t, int32(0), frontend.Spec.Replicas, "frontend must remain scaled to zero until explicitly scaled back up")
	require.NoError(t, tc.WaitForPods(4))

	tests.Logger.Info("5. Scale the frontend back to 2 and verify new-revision pods launch")
	tc.ScalePodCliqueAndWait(coherentWorkloadName+"-0-frontend", 2, coherentExpectedPods, 0)
	updateutils.AssertGenerationHashConverged(tc)
	if err := tc.WaitForPods(coherentExpectedPods); err != nil {
		t.Fatalf("pods did not become Ready after scaling the frontend back up: %v", err)
	}
}

// Test_CU14_CoherentUpdateWithAllComponentsScaledToZero verifies that a coherent update completes when
// every in-scope component is scaled to zero before the update. There is nothing to roll, so the update
// advances each component's resource to the new revision, the PodGangMap ends with no entries, and a later
// scale-out launches new-revision pods.
func Test_CU14_CoherentUpdateWithAllComponentsScaledToZero(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-coherent and verify 6 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: coherentWorkloadName,
		workloadYAML: coherentWorkloadYAML,
		workerNodes:  10,
		expectedPods: coherentExpectedPods,
	})
	defer cleanup()

	tests.Logger.Info("2. Scale the inference PCSG and the frontend to 0 so no component has pods")
	tc.ScalePCSGAcrossAllReplicasAndWait(coherentWorkloadName, "inference", 1, 0, 2, 0)
	tc.ScalePodCliqueAndWait(coherentWorkloadName+"-0-frontend", 0, 0, 0)

	tests.Logger.Info("3. Trigger a coherent update of frontend and the inference PCSG")
	for _, cliqueName := range []string{"frontend", "prefill"} {
		if err := updateutils.TriggerPodCliqueUpdate(tc, cliqueName); err != nil {
			t.Fatalf("failed to trigger update of %s: %v", cliqueName, err)
		}
	}

	tests.Logger.Info("4. Wait for the coherent update to complete and verify the PodGangMap has no entries")
	if err := updateutils.WaitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("coherent update did not complete: %v", err)
	}
	updateutils.AssertUpdateInProgressCleared(tc)
	updateutils.AssertGenerationHashConverged(tc)
	assert.Empty(t, updateutils.PodGangMapEntries(t, tc, 0), "an all-zero replica must leave the PodGangMap with no entries")

	tests.Logger.Info("5. Scale the frontend and the inference PCSG back up and verify new-revision pods launch")
	tc.ScalePodCliqueAndWait(coherentWorkloadName+"-0-frontend", 2, 2, 0)
	tc.ScalePCSGAcrossAllReplicasAndWait(coherentWorkloadName, "inference", 1, 2, coherentExpectedPods, 0)
	updateutils.AssertGenerationHashConverged(tc)
	if err := tc.WaitForPods(coherentExpectedPods); err != nil {
		t.Fatalf("pods did not become Ready after scaling both components back up: %v", err)
	}
}

// Test_CU15_CoherentUpdateRecoversFromOldVersionPodLoss verifies that losing an old-version pod during a
// coherent update, at a point where the disruption budget is already fully consumed, does not stall the
// roll and does not break coherence. Replica 0 readiness is blocked so the roll parks mid-sub-step with the
// new-version pod not Ready, which is zero headroom. Deleting a ready old-version frontend pod then would
// deadlock a naive gate. The engine instead reclaims the lost slot onto the current-version anchor, so no
// PodGang runs two revisions, and once readiness is released the update completes.
func Test_CU15_CoherentUpdateRecoversFromOldVersionPodLoss(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-coherent and verify pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: coherentWorkloadName,
		workloadYAML: coherentWorkloadYAML,
		workerNodes:  10,
		expectedPods: coherentExpectedPods,
	})
	defer cleanup()
	tc.Timeout = 3 * time.Minute

	tests.Logger.Info("2. Block replica 0 readiness so the coherent roll parks mid-sub-step at zero headroom")
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path); err != nil {
		t.Fatalf("failed to apply replica-0 readiness-block KWOK stage: %v", err)
	}
	defer func() {
		if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name); err != nil {
			tests.Logger.Warnf("cleanup: delete replica-0 readiness-block KWOK stage: %v", err)
		}
	}()

	tests.Logger.Info("3. Trigger a coherent update of the frontend PodClique and wait until it is mid-roll")
	if err := updateutils.TriggerPodCliqueUpdate(tc, "frontend"); err != nil {
		t.Fatalf("failed to trigger update of frontend: %v", err)
	}
	if err := updateutils.WaitForOrdinalUpdating(tc, 0); err != nil {
		t.Fatalf("replica 0 did not start updating: %v", err)
	}
	updateutils.WaitForReplicaMidCoherentRoll(t, tc, 0)

	tests.Logger.Info("4. With the new-revision pod held not-Ready (headroom 0), delete a ready old-revision frontend pod")
	oldPod, err := firstReadyPodForCliqueOnReplica(tc, "frontend", 0)
	if err != nil {
		t.Fatalf("no ready old-revision frontend pod to delete: %v", err)
	}
	if err := updateutils.DeletePodAndWaitForTermination(tc, oldPod); err != nil {
		t.Fatalf("failed to delete old-revision frontend pod %s: %v", oldPod, err)
	}

	tests.Logger.Info("5. Coherence must hold: the lost slot is reclaimed onto the current-revision anchor, never refilled on the old one")
	assertEachPodGangSingleRevision(t, tc, "frontend", 0)

	tests.Logger.Info("6. Release readiness and confirm the update completes, proving the loss did not deadlock the roll")
	if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name); err != nil {
		t.Fatalf("failed to delete replica-0 readiness-block KWOK stage: %v", err)
	}
	updateutils.DeleteNotReadyPodsOnReplica(t, tc, 0)
	if err := updateutils.WaitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("coherent update did not complete after the old-revision pod loss: %v", err)
	}
	updateutils.AssertUpdateInProgressCleared(tc)
	updateutils.AssertPodGangMapSingleGeneration(t, tc)
	updateutils.AssertGenerationHashConverged(tc)
	assertEachPodGangSingleRevision(t, tc, "frontend", 0)
}

// Test_CU16_CoherentUpdateSelfHealsCurrentVersionPodLoss verifies that losing a new-version pod on the
// current-version anchor mid-roll self-heals through the normal create path and never breaks coherence. A
// current-anchor deficit is not a missing old-version Pod, so the pod controller simply recreates the pod
// on the current-version anchor. The update then completes once readiness is released.
func Test_CU16_CoherentUpdateSelfHealsCurrentVersionPodLoss(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-coherent and verify pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: coherentWorkloadName,
		workloadYAML: coherentWorkloadYAML,
		workerNodes:  10,
		expectedPods: coherentExpectedPods,
	})
	defer cleanup()
	tc.Timeout = 3 * time.Minute

	tests.Logger.Info("2. Block replica 0 readiness so the coherent roll parks mid-sub-step")
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Path); err != nil {
		t.Fatalf("failed to apply replica-0 readiness-block KWOK stage: %v", err)
	}
	defer func() {
		if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name); err != nil {
			tests.Logger.Warnf("cleanup: delete replica-0 readiness-block KWOK stage: %v", err)
		}
	}()

	tests.Logger.Info("3. Trigger a coherent update of the frontend PodClique and wait until it is mid-roll")
	if err := updateutils.TriggerPodCliqueUpdate(tc, "frontend"); err != nil {
		t.Fatalf("failed to trigger update of frontend: %v", err)
	}
	if err := updateutils.WaitForOrdinalUpdating(tc, 0); err != nil {
		t.Fatalf("replica 0 did not start updating: %v", err)
	}
	updateutils.WaitForReplicaMidCoherentRoll(t, tc, 0)

	tests.Logger.Info("4. Delete the not-Ready new-revision frontend pod on the current-version anchor")
	newPod, err := firstNotReadyPodForCliqueOnReplica(tc, "frontend", 0)
	if err != nil {
		t.Fatalf("no not-ready new-revision frontend pod to delete: %v", err)
	}
	if err := updateutils.DeletePodAndWaitForTermination(tc, newPod); err != nil {
		t.Fatalf("failed to delete new-revision frontend pod %s: %v", newPod, err)
	}

	tests.Logger.Info("5. Coherence must hold: the pod is recreated on the current-version anchor, so no PodGang runs two revisions")
	assertEachPodGangSingleRevision(t, tc, "frontend", 0)

	tests.Logger.Info("6. Release readiness and confirm the update completes")
	if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyBlockedR0Name); err != nil {
		t.Fatalf("failed to delete replica-0 readiness-block KWOK stage: %v", err)
	}
	updateutils.DeleteNotReadyPodsOnReplica(t, tc, 0)
	if err := updateutils.WaitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("coherent update did not complete after the current-revision pod loss: %v", err)
	}
	updateutils.AssertUpdateInProgressCleared(tc)
	updateutils.AssertPodGangMapSingleGeneration(t, tc)
	updateutils.AssertGenerationHashConverged(tc)
	assertEachPodGangSingleRevision(t, tc, "frontend", 0)
}

// Test_CU17_CoherentCorrectiveUpdateRecoversUnavailableReplicas verifies that a Coherent update makes
// progress when a component is already unavailable and its unavailability has exhausted MaxUnavailable: the
// corrective update must replace the unavailable replicas and complete rather than deadlock. This is the
// Coherent analog of Test_RU26 and the #873 regression.
//
// It exercises both ways a replica is unavailable: Pending because the PCS template pins an impossible node
// selector, and scheduled-but-never-Ready via the pod-crashloop KWOK stage (the issue's bad-image repro).
// Both exhaust the component's MaxUnavailable budget. Cases cover a single PodCliqueScalingGroup replica, a
// standalone PodClique whose MinAvailable equals its replica count, and a PodCliqueScalingGroup with more
// unavailable replicas than MaxUnavailable.
//
// Scenario CU-17 (per case):
//  1. Make the component unavailable, then deploy, so it consumes its whole MaxUnavailable budget.
//  2. Wait for the initial generation to reconcile (so the correction is an update, not initial creation)
//     and confirm the workload reports no available pods.
//  3. Apply the corrective change (remove the selector, or clear the crash-loop stage and bump the
//     template), triggering a coherent update.
//  4. Verify Grove replaces every unavailable pod, the coherent update completes, UpdateInProgress clears,
//     and the generation hash converges.
func Test_CU17_CoherentCorrectiveUpdateRecoversUnavailableReplicas(t *testing.T) {
	cases := []struct {
		name                       string
		workloadName               string
		workloadYAML               string
		expectedPods               int
		workerNodes                int
		crashloopStage             bool
		assertInitiallyUnavailable func(c *assert.CollectT, tc *testctx.TestContext)
		correct                    func(t *testing.T, tc *testctx.TestContext)
	}{
		{
			name:                       "single PodCliqueScalingGroup replica Pending exhausts MaxUnavailable",
			workloadName:               "wl-coh-unavail-pcsg",
			workloadYAML:               "../../yaml/wl-coh-unavail-pcsg.yaml",
			expectedPods:               1,
			workerNodes:                1,
			assertInitiallyUnavailable: assertPCSGUnavailable("sg-x", 1),
			correct:                    removeUnschedulableSelector,
		},
		{
			name:         "standalone PodClique Pending with MinAvailable equal to replicas",
			workloadName: "wl-coh-unavail-standalone",
			workloadYAML: "../../yaml/wl-coh-unavail-standalone.yaml",
			expectedPods: 2,
			workerNodes:  2,
			correct:      removeUnschedulableSelector,
		},
		{
			name:                       "PodCliqueScalingGroup Pending with more unavailable replicas than MaxUnavailable",
			workloadName:               "wl-coh-unavail-pcsg-multi",
			workloadYAML:               "../../yaml/wl-coh-unavail-pcsg-multi.yaml",
			expectedPods:               3,
			workerNodes:                3,
			assertInitiallyUnavailable: assertPCSGUnavailable("sg-x", 3),
			correct:                    removeUnschedulableSelector,
		},
		{
			name:                       "single PodCliqueScalingGroup replica scheduled but never Ready",
			workloadName:               "wl-coh-crashloop-pcsg",
			workloadYAML:               "../../yaml/wl-coh-crashloop-pcsg.yaml",
			expectedPods:               1,
			workerNodes:                1,
			crashloopStage:             true,
			assertInitiallyUnavailable: assertPCSGUnavailable("sg-x", 1),
			correct:                    recoverFromCrashloopAndTriggerUpdate,
		},
		{
			name:           "standalone PodClique scheduled but never Ready, MinAvailable equal to replicas",
			workloadName:   "wl-coh-crashloop-standalone",
			workloadYAML:   "../../yaml/wl-coh-crashloop-standalone.yaml",
			expectedPods:   2,
			workerNodes:    2,
			crashloopStage: true,
			correct:        recoverFromCrashloopAndTriggerUpdate,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tc, cleanup := testctx.PrepareTest(context.Background(), t, tt.workerNodes,
				testctx.WithWorkload(&testctx.WorkloadConfig{
					Name:         tt.workloadName,
					YAMLPath:     tt.workloadYAML,
					Namespace:    "default",
					ExpectedPods: tt.expectedPods,
				}),
				testctx.WithTimeout(2*time.Minute),
				testctx.WithInterval(time.Second),
			)
			defer cleanup()

			if tt.crashloopStage {
				tests.Logger.Info("0. Hold freshly created pods scheduled-but-not-Ready via the crash-loop KWOK stage")
				require.NoError(t, kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageCrashloopPath))
				defer func() {
					if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageCrashloopName); err != nil {
						tests.Logger.Warnf("cleanup: delete crash-loop KWOK stage: %v", err)
					}
				}()
			}

			tests.Logger.Info("1. Create a Coherent workload whose pods never become available")
			pods, err := tc.DeployAndVerifyWorkload()
			require.NoError(t, err)
			require.Len(t, pods.Items, tt.expectedPods)
			oldUIDs := make(map[types.UID]bool, len(pods.Items))
			for _, p := range pods.Items {
				oldUIDs[p.UID] = true
			}

			// Let the first generation fully reconcile before correcting, so the correction is a coherent
			// update rather than part of initial pod creation. Confirm the workload is unavailable.
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

			tests.Logger.Info("2. Apply the corrective change to trigger the coherent update")
			tt.correct(t, tc)

			tests.Logger.Info("3. Verify Grove replaces the unavailable pods and completes the corrective update")
			// The corrective update must proceed even though MaxUnavailable is already breached; otherwise the
			// unavailable pods are never replaced and this wait times out.
			require.NoError(t, updateutils.WaitForRollingUpdateComplete(tc, 1),
				"corrective coherent update must replace the unavailable pods and complete")
			require.NoError(t, tc.WaitForPods(tt.expectedPods))

			pods, err = tc.ListPods()
			require.NoError(t, err)
			require.Len(t, pods.Items, tt.expectedPods)
			for _, p := range pods.Items {
				assert.False(t, oldUIDs[p.UID], "every unavailable pod must be replaced")
			}
			updateutils.AssertUpdateInProgressCleared(tc)
			updateutils.AssertGenerationHashConverged(tc)
			updateutils.AssertPodGangMapSingleGeneration(t, tc)
		})
	}
}

// Test_CU18_MaxUnavailableAboveTemplateReplicas is the Coherent version of Test_RU28. It verifies that
// maxUnavailable caps how many replicas a Coherent rollout disrupts at once, measured against the
// current number of live replicas rather than the replica count in the PodCliqueSet template. maxUnavailable
// may be larger than the template replica count because the live PodCliqueScalingGroup is scaled
// separately through its /scale subresource.
//
// sg-x starts with template replicas 1 and maxUnavailable 2 (maxUnavailable stays >= minAvailable 1,
// which Coherent requires). Deploying it confirms the value is accepted. The test then scales sg-x out
// to 4 live replicas and rolls it, expecting at most 2 replicas unavailable at once, then scales back in
// to 1 and rolls again, expecting at most the single remaining replica to be disrupted even though
// maxUnavailable is still 2.
func Test_CU18_MaxUnavailableAboveTemplateReplicas(t *testing.T) {
	const workloadName = "wl-mu-coh"

	tests.Logger.Info("1. Deploy wl-mu-coh (sg-x template replicas 1, maxUnavailable 2)")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: workloadName,
		workloadYAML: "../../yaml/workload-mu-coh.yaml",
		workerNodes:  10,
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

	tcLong := *tc
	tcLong.Timeout = 3 * time.Minute

	tests.Logger.Info("3. Scale sg-x out from 1 to 4 live replicas (front 1 + sg-x 4 = 5 pods)")
	tc.ScalePCSGAcrossAllReplicasAndWait(workloadName, "sg-x", 1, 4, 5, 0)

	tests.Logger.Info("4. Trigger a Coherent rollout of sg-x and verify the budget is honoured against the live count of 4")
	prevHash := updateutils.PCSGenerationHash(t, &tcLong)
	peakOut, err := updateutils.MaxUnavailablePods(&tcLong, updateutils.NotReadyPodForPCSG(&tcLong, "sg-x"), func() error {
		if err := updateutils.TriggerPodCliqueUpdate(&tcLong, "pc-b"); err != nil {
			return err
		}
		if err := updateutils.WaitForGenerationHashChange(&tcLong, prevHash); err != nil {
			return err
		}
		return updateutils.WaitForRollingUpdateComplete(&tcLong, 1)
	})
	if err != nil {
		t.Fatalf("coherent update of sg-x at 4 replicas did not complete: %v", err)
	}
	assert.LessOrEqualf(t, peakOut, 2, "expected at most maxUnavailable=2 sg-x replicas unavailable at once with 4 live replicas under Coherent, observed %d", peakOut)
	updateutils.AssertUpdateInProgressCleared(tc)

	tests.Logger.Info("5. Scale sg-x back in from 4 to 1 live replica (front 1 + sg-x 1 = 2 pods)")
	tc.ScalePCSGAcrossAllReplicasAndWait(workloadName, "sg-x", 1, 1, 2, 0)

	tests.Logger.Info("6. Trigger a Coherent rollout again: the stored budget of 2 must not disrupt more than the single remaining replica")
	prevHash = updateutils.PCSGenerationHash(t, &tcLong)
	peakIn, err := updateutils.MaxUnavailablePods(&tcLong, updateutils.NotReadyPodForPCSG(&tcLong, "sg-x"), func() error {
		if err := updateutils.TriggerPodCliqueUpdate(&tcLong, "pc-b"); err != nil {
			return err
		}
		if err := updateutils.WaitForGenerationHashChange(&tcLong, prevHash); err != nil {
			return err
		}
		return updateutils.WaitForRollingUpdateComplete(&tcLong, 1)
	})
	if err != nil {
		t.Fatalf("coherent update of sg-x at 1 replica did not complete: %v", err)
	}
	assert.LessOrEqualf(t, peakIn, 1, "expected the Coherent rollout to disrupt at most the 1 remaining sg-x replica despite the stored budget of 2, observed %d", peakIn)
	updateutils.AssertUpdateInProgressCleared(tc)
}

type coherentAnchor struct {
	standalone  map[string]int32
	pcsgIndices []int32
}

// assertCoherentAnchorCompositions fails unless the new-hash anchor entries match want as a multiset, and
// every new-hash anchor carries newHash. It compares each anchor's standalone pod counts and its indices
// for pcsgName, ignoring other entries so steady-state scaffolding does not affect the match.
func assertCoherentAnchorCompositions(t *testing.T, entries []grovev1beta1.PodGangEntry, newHash, pcsgName string, want []coherentAnchor) {
	t.Helper()
	gotKeys := make([]string, 0, len(want))
	for _, entry := range entries {
		if entry.Role != grovev1beta1.PodGangEntryRoleAnchor || entry.PodCliqueSetGenerationHash != newHash {
			continue
		}
		gotKeys = append(gotKeys, anchorCompositionKey(entry.PodCliques, entry.PCSGReplicaIndices[pcsgName]))
	}
	wantKeys := make([]string, 0, len(want))
	for _, anchor := range want {
		wantKeys = append(wantKeys, anchorCompositionKey(anchor.standalone, anchor.pcsgIndices))
	}
	assert.ElementsMatch(t, wantKeys, gotKeys, "coherent anchor compositions did not match")
}

// anchorCompositionKey builds a stable multiset key from an entry's standalone pod counts and one PCSG's
// replica indices, so anchors are compared by composition rather than by epoch or anchor index.
func anchorCompositionKey(standalone map[string]int32, pcsgIndices []int32) string {
	standaloneNames := make([]string, 0, len(standalone))
	for name := range standalone {
		standaloneNames = append(standaloneNames, name)
	}
	sort.Strings(standaloneNames)
	var b strings.Builder
	for _, name := range standaloneNames {
		fmt.Fprintf(&b, "%s=%d;", name, standalone[name])
	}
	sorted := append([]int32(nil), pcsgIndices...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	fmt.Fprintf(&b, "pcsg=%v", sorted)
	return b.String()
}

// newHashTailPCSGIndices returns the indices for pcsgName carried by the single new-hash tail entry, or nil
// when no new-hash tail entry exists. It fails when more than one new-hash tail entry is present.
func newHashTailPCSGIndices(entries []grovev1beta1.PodGangEntry, newHash, pcsgName string) []int32 {
	var indices []int32
	for _, entry := range entries {
		if entry.Role == grovev1beta1.PodGangEntryRoleTail && entry.PodCliqueSetGenerationHash == newHash {
			indices = entry.PCSGReplicaIndices[pcsgName]
		}
	}
	return indices
}

// gangTerminateReplicaAndWaitForPodGangMapRebuild breaches MinAvailable on the given PodClique of the
// given PCS replica and waits until the replica's PodGangMap is deleted and rebuilt. It deletes every live
// pod of that PodClique on the replica, dropping it below MinAvailable. With a crash-loop stage active for
// the replica the recreated pods stay not-ready past terminationDelay, so the component stays below MinAvailable long enough for the PCS-level gang termination to fire once (its
// re-fire guard prevents churn) and rebuild the replica. It polls until the replica's PodGangMap carries a
// new UID, which is how gang termination resets a fully torn-down replica.
//
// Gang termination is suppressed while a replica is itself under coherent update (its MinAvailableBreached
// goes Unknown). The caller keeps another replica's update parked in flight so the orchestrator never
// advances to this replica, and the poll fails fast if it ever does, rather than waiting out the deadline.
func gangTerminateReplicaAndWaitForPodGangMapRebuild(t *testing.T, tc *testctx.TestContext, cliqueName string, pcsReplicaIndex int) {
	t.Helper()
	pgmBefore, err := updateutils.GetReplicaPodGangMap(tc, pcsReplicaIndex)
	if err != nil {
		t.Fatalf("failed to read PodGangMap for replica %d: %v", pcsReplicaIndex, err)
	}
	oldUID := pgmBefore.UID

	victims, err := livePodsForCliqueOnReplica(tc, cliqueName, pcsReplicaIndex)
	if err != nil {
		t.Fatalf("failed to list %s pods on replica %d: %v", cliqueName, pcsReplicaIndex, err)
	}
	if len(victims) == 0 {
		t.Fatalf("no %s pods found on replica %d to breach MinAvailable", cliqueName, pcsReplicaIndex)
	}
	for i := range victims {
		if err := tc.Client.Delete(tc.Ctx, &victims[i]); err != nil && !apierrors.IsNotFound(err) {
			t.Fatalf("failed to delete pod %s to breach MinAvailable: %v", victims[i].Name, err)
		}
	}

	pollErr := wait.PollUntilContextTimeout(tc.Ctx, 2*time.Second, 90*time.Second, true, func(context.Context) (bool, error) {
		// Gang termination is suppressed while a replica is itself under coherent update: its
		// MinAvailableBreached goes Unknown ("Update is in progress"). If the orchestrator has advanced to
		// the replica being gang terminated, the rebuild can never happen, so fail fast with a clear message
		// instead of polling to the deadline. The readiness-hold on the rolling replica keeps the
		// orchestrator parked there, so in a healthy run this guard never trips.
		pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
		if err != nil {
			return false, err
		}
		if up := pcs.Status.UpdateProgress; up != nil && len(up.CurrentlyUpdating) > 0 &&
			int(up.CurrentlyUpdating[0].ReplicaIndex) == pcsReplicaIndex {
			return false, fmt.Errorf("orchestrator advanced to replica %d under gang termination; its MinAvailableBreached is suppressed while it updates, so the PodGangMap cannot be rebuilt", pcsReplicaIndex)
		}
		pgm, err := updateutils.GetReplicaPodGangMap(tc, pcsReplicaIndex)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil // briefly absent between delete and recreate
			}
			return false, err
		}
		return pgm.UID != oldUID, nil
	})
	if pollErr != nil {
		t.Fatalf("PodGangMap for replica %d was not rebuilt after gang termination: %v", pcsReplicaIndex, pollErr)
	}
}

// livePodsForCliqueOnReplica returns the live pods of the given clique that belong to the given PCS
// replica index.
func livePodsForCliqueOnReplica(tc *testctx.TestContext, cliqueName string, pcsReplicaIndex int) ([]corev1.Pod, error) {
	podList, err := tc.ListPods()
	if err != nil {
		return nil, err
	}
	wantReplica := strconv.Itoa(pcsReplicaIndex)
	var matched []corev1.Pod
	for i := range podList.Items {
		pod := podList.Items[i]
		pclq, ok := pod.Labels[apicommon.LabelPodClique]
		if !ok || !strings.HasSuffix(pclq, "-"+cliqueName) {
			continue
		}
		if pod.Labels[apicommon.LabelPodCliqueSetReplicaIndex] != wantReplica {
			continue
		}
		if pod.DeletionTimestamp != nil {
			continue
		}
		matched = append(matched, pod)
	}
	return matched, nil
}

// firstReadyPodForCliqueOnReplica returns the name of a Ready pod of the clique on the replica. During a
// coherent roll parked by blocked readiness, the Ready pods are the old-revision pods on the old anchor.
func firstReadyPodForCliqueOnReplica(tc *testctx.TestContext, cliqueName string, pcsReplicaIndex int) (string, error) {
	livePods, err := livePodsForCliqueOnReplica(tc, cliqueName, pcsReplicaIndex)
	if err != nil {
		return "", err
	}
	for i := range livePods {
		if kubeutils.IsPodReady(&livePods[i]) {
			return livePods[i].Name, nil
		}
	}
	return "", fmt.Errorf("no Ready %s pod found on replica %d", cliqueName, pcsReplicaIndex)
}

const (
	// kwokStageCrashloopR1Path is the KWOK stage fixture path.
	kwokStageCrashloopR1Path = "../../yaml/kwok/pod-crashloop-r1-standalone.yaml"
	// kwokStageCrashloopR1Name is the KWOK stage name.
	kwokStageCrashloopR1Name = "pod-crashloop-r1-standalone"
)

// assertEachPodGangSingleRevision fails the test if any PodGang carries pods of more than one pod template
// hash for the given clique on the given replica. A coherent update must never place a new-revision pod on
// an old-version PodGang, so every PodGang holds a single revision.
//
//nolint:unparam // Keep the clique selection explicit at the assertion call site.
func assertEachPodGangSingleRevision(t *testing.T, tc *testctx.TestContext, cliqueName string, pcsReplicaIndex int) {
	t.Helper()
	livePods, err := livePodsForCliqueOnReplica(tc, cliqueName, pcsReplicaIndex)
	if err != nil {
		t.Fatalf("failed to list %s pods on replica %d: %v", cliqueName, pcsReplicaIndex, err)
	}
	revisionByPodGang := make(map[string]string)
	for i := range livePods {
		podGang := livePods[i].Labels[apicommon.LabelPodGang]
		if podGang == "" {
			continue
		}
		revision := livePods[i].Labels[apicommon.LabelPodTemplateHash]
		if existing, ok := revisionByPodGang[podGang]; ok {
			assert.Equalf(t, existing, revision, "PodGang %s carries pods of two revisions (%s and %s), coherence broken", podGang, existing, revision)
			continue
		}
		revisionByPodGang[podGang] = revision
	}
}

// firstNotReadyPodForCliqueOnReplica returns the name of a not-Ready pod of the clique on the replica.
// During a coherent roll parked by blocked readiness, the not-Ready pod is the new-revision pod the current
// sub-step created on the current-version anchor.
func firstNotReadyPodForCliqueOnReplica(tc *testctx.TestContext, cliqueName string, pcsReplicaIndex int) (string, error) {
	livePods, err := livePodsForCliqueOnReplica(tc, cliqueName, pcsReplicaIndex)
	if err != nil {
		return "", err
	}
	for i := range livePods {
		if !kubeutils.IsPodReady(&livePods[i]) {
			return livePods[i].Name, nil
		}
	}
	return "", fmt.Errorf("no not-Ready %s pod found on replica %d", cliqueName, pcsReplicaIndex)
}

// assertPCSGUnavailable returns an assertion that the named PodCliqueScalingGroup reports the expected
// replica count and zero available replicas.
func assertPCSGUnavailable(pcsgConfigName string, expectedReplicas int32) func(c *assert.CollectT, tc *testctx.TestContext) {
	return func(c *assert.CollectT, tc *testctx.TestContext) {
		var pcsg grovev1beta1.PodCliqueScalingGroup
		require.NoError(c, tc.Client.Get(tc.Ctx, types.NamespacedName{
			Namespace: tc.Namespace, Name: updateutils.PCSGFQN(tc, pcsgConfigName),
		}, &pcsg))
		assert.EqualValues(c, expectedReplicas, pcsg.Status.Replicas)
		assert.Zero(c, pcsg.Status.AvailableReplicas)
	}
}

// removeUnschedulableSelector removes the impossible node selector from the first clique of the workload's
// PCS template, letting its Pending pods schedule and bumping the generation hash to trigger the update.
func removeUnschedulableSelector(t *testing.T, tc *testctx.TestContext) {
	pcs, err := updateutils.GetPCS(tc, tc.Workload.Name)
	require.NoError(t, err)
	original := pcs.DeepCopy()
	delete(pcs.Spec.Template.Cliques[0].Spec.PodSpec.NodeSelector, "e2e.grove.io/unschedulable")
	require.NoError(t, tc.Client.Patch(tc.Ctx, pcs, client.MergeFrom(original)))
	require.Greater(t, pcs.Generation, original.Generation)
}

// recoverFromCrashloopAndTriggerUpdate clears the crash-loop KWOK stage so freshly created pods come up
// Ready, then bumps the worker clique's template to trigger the coherent update. The already-crashlooped
// pods stay not-Ready (KWOK does not re-evaluate a held pod), so only the replacements the update creates
// recover, which is exactly what the corrective drain must do.
func recoverFromCrashloopAndTriggerUpdate(t *testing.T, tc *testctx.TestContext) {
	require.NoError(t, kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageCrashloopName))
	require.NoError(t, updateutils.TriggerPodCliqueUpdate(tc, "worker"))
}

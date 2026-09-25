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
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/k8s/kwok"
	tests "github.com/ai-dynamo/grove/operator/e2e/tests"

	"github.com/stretchr/testify/assert"
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
	assertReplicaPodGangMap(t, getPodGangMapEntries(t, tc, 0), expectedReplicaPodGangMap{
		standalonePodCounts: map[string]int32{"frontend": 2},
		pcsgName:            "inference",
		anchorIndices:       []int32{0},
		tailIndices:         []int32{1},
	})
}

// Test_CU2_CoherentUpdateStandaloneClique verifies that a coherent update of only the standalone frontend
// PodClique completes, clears UpdateInProgress, converges every component to the latest generation hash,
// and leaves all pods Ready.
func Test_CU2_CoherentUpdateStandaloneClique(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-coherent and verify 6 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: coherentWorkloadName,
		workloadYAML: coherentWorkloadYAML,
		workerNodes:  10,
		expectedPods: coherentExpectedPods,
	})
	defer cleanup()

	tests.Logger.Info("2. Trigger a coherent update of the frontend PodClique")
	if err := triggerPodCliqueUpdate(tc, "frontend"); err != nil {
		t.Fatalf("failed to trigger update of frontend: %v", err)
	}

	tests.Logger.Info("3. Wait for the coherent update to complete")
	if err := waitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("coherent update did not complete: %v", err)
	}

	tests.Logger.Info("4. Verify completion, convergence, and readiness")
	assertUpdateInProgressCleared(tc)
	assertGenerationHashConverged(tc)
	assertPodGangMapSingleGeneration(t, tc)
	if err := tc.WaitForPods(coherentExpectedPods); err != nil {
		t.Fatalf("pods did not become Ready after the coherent update: %v", err)
	}
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
	if err := triggerPodCliqueUpdate(tc, "prefill"); err != nil {
		t.Fatalf("failed to trigger update of prefill: %v", err)
	}

	tests.Logger.Info("3. Wait for the coherent update to complete")
	if err := waitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("coherent update did not complete: %v", err)
	}

	tests.Logger.Info("4. Verify completion, convergence, and readiness")
	assertUpdateInProgressCleared(tc)
	assertGenerationHashConverged(tc)
	if err := tc.WaitForPods(coherentExpectedPods); err != nil {
		t.Fatalf("pods did not become Ready after the coherent update: %v", err)
	}

	tests.Logger.Info("5. Verify the frontend anchor, out of the update scope, reconverged to a single generation")
	assertPodGangMapSingleGeneration(t, tc)

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
		if err := triggerPodCliqueUpdate(tc, cliqueName); err != nil {
			t.Fatalf("failed to trigger update of %s: %v", cliqueName, err)
		}
	}

	tests.Logger.Info("3. Wait for the coherent update to complete")
	if err := waitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("coherent update did not complete: %v", err)
	}
	assertUpdateInProgressCleared(tc)
	assertGenerationHashConverged(tc)

	tests.Logger.Info("4. Verify the converged PodGangMap holds two Minimum Viable Unit anchors and no leftover tail")
	newHash := getPCSGenerationHash(t, tc)
	entries := getPodGangMapEntries(t, tc, 0)
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
		if err := triggerPodCliqueUpdate(tc, cliqueName); err != nil {
			t.Fatalf("failed to trigger update of %s: %v", cliqueName, err)
		}
	}

	tests.Logger.Info("4. Wait for the coherent update to complete")
	if err := waitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("coherent update did not complete: %v", err)
	}
	assertUpdateInProgressCleared(tc)
	assertGenerationHashConverged(tc)

	tests.Logger.Info("5. Verify two anchors plus a leftover tail carrying inference index 2")
	newHash := getPCSGenerationHash(t, tc)
	entries := getPodGangMapEntries(t, tc, 0)
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
	if err := triggerPodCliqueUpdate(tc, "frontend"); err != nil {
		t.Fatalf("failed to trigger update of frontend: %v", err)
	}
	if err := waitForOrdinalUpdating(tc, 0); err != nil {
		t.Fatalf("replica 0 did not start updating: %v", err)
	}

	tests.Logger.Info("5. A replica change is rejected on both the updating replica 0 and the idle replica 1")
	for _, replicaIndex := range []int{0, 1} {
		pclqScaleErr := tc.ScalePodClique(fmt.Sprintf("%s-%d-frontend", coherentWorkloadName, replicaIndex), 3)
		if assert.Errorf(t, pclqScaleErr, "a direct PodClique replica change on replica %d must be rejected while a coherent update is in progress", replicaIndex) {
			assert.Contains(t, pclqScaleErr.Error(), "coherent update is in progress")
		}
		pcsgScaleErr := tc.ScalePCSG(pcsgFQNForReplica(tc, "inference", replicaIndex), 3)
		if assert.Errorf(t, pcsgScaleErr, "a PodCliqueScalingGroup scale change on replica %d must be rejected while a coherent update is in progress", replicaIndex) {
			assert.Contains(t, pcsgScaleErr.Error(), "coherent update is in progress")
		}
	}

	tests.Logger.Info("6. Remove the readiness delay and let the update complete")
	if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
		t.Fatalf("failed to delete readiness-delay KWOK stage: %v", err)
	}
	if err := waitForRollingUpdateComplete(tc, 2); err != nil {
		t.Fatalf("coherent update did not complete: %v", err)
	}

	tests.Logger.Info("7. Scaling the PodClique and the PodCliqueScalingGroup is allowed once the update has completed")
	if err := tc.ScalePodClique(fmt.Sprintf("%s-0-frontend", coherentWorkloadName), 3); err != nil {
		t.Fatalf("scaling the PodClique must be allowed after the coherent update completes: %v", err)
	}
	if err := tc.ScalePCSG(pcsgFQNForReplica(tc, "inference", 0), 3); err != nil {
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
	if err := triggerPodCliqueUpdate(tc, "frontend"); err != nil {
		t.Fatalf("failed to trigger update of frontend: %v", err)
	}
	if err := waitForOrdinalUpdating(tc, 0); err != nil {
		t.Fatalf("replica 0 did not start updating: %v", err)
	}

	tests.Logger.Info("4. Trigger the second coherent update of the inference PCSG while the first is still in flight")
	if err := triggerPodCliqueUpdate(tc, "prefill"); err != nil {
		t.Fatalf("failed to trigger update of prefill: %v", err)
	}

	tests.Logger.Info("5. Remove the readiness delay so both updates can converge")
	if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
		t.Fatalf("failed to delete readiness-delay KWOK stage: %v", err)
	}

	tests.Logger.Info("6. Wait for the coherent update to complete and verify convergence and readiness")
	if err := waitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("coherent update did not complete: %v", err)
	}
	assertUpdateInProgressCleared(tc)
	assertGenerationHashConverged(tc)
	if err := tc.WaitForPods(coherentExpectedPods); err != nil {
		t.Fatalf("pods did not become Ready after the coherent updates: %v", err)
	}

	tests.Logger.Info("7. Verify the PodGangMap converged to a single generation with no stale intermediate entries")
	assertPodGangMapSingleGeneration(t, tc)
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
	if err := triggerPodCliqueUpdate(tc, "frontend"); err != nil {
		t.Fatalf("failed to trigger update of frontend: %v", err)
	}
	if err := waitForOrdinalUpdating(tc, 0); err != nil {
		t.Fatalf("replica 0 did not start updating: %v", err)
	}

	tests.Logger.Info("4. Crash the operator mid-update by deleting its pod and wait for a fresh replica")
	if err := restartOperator(tc); err != nil {
		t.Fatalf("operator did not come back after restart: %v", err)
	}

	tests.Logger.Info("5. Remove the readiness delay so the resumed update can converge")
	if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedName); err != nil {
		t.Fatalf("failed to delete readiness-delay KWOK stage: %v", err)
	}

	tests.Logger.Info("6. Wait for the resumed update to complete and verify convergence and readiness")
	if err := waitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("coherent update did not complete after the operator restart: %v", err)
	}
	assertUpdateInProgressCleared(tc)
	assertGenerationHashConverged(tc)
	assertPodGangMapSingleGeneration(t, tc)
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
	maxUnavailable, err := maxUnavailablePods(&tcLong, notReadyPodForClique("frontend"), func() error {
		if err := triggerPodCliqueUpdate(&tcLong, "frontend"); err != nil {
			return err
		}
		return waitForRollingUpdateComplete(&tcLong, 1)
	})
	if err != nil {
		t.Fatalf("coherent update of frontend did not complete: %v", err)
	}

	tests.Logger.Info("4. Verify MaxUnavailable=1 was never exceeded")
	assert.LessOrEqualf(t, maxUnavailable, 1, "a coherent update must never leave more than MaxUnavailable=1 frontend pods unavailable at once, observed %d", maxUnavailable)
	assertUpdateInProgressCleared(tc)
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
	maxUnavailable, err := maxUnavailablePods(&tcLong, notReadyPodForClique("frontend"), func() error {
		if err := triggerPodCliqueUpdate(&tcLong, "frontend"); err != nil {
			return err
		}
		return waitForRollingUpdateComplete(&tcLong, 1)
	})
	if err != nil {
		t.Fatalf("coherent update of frontend did not complete: %v", err)
	}

	tests.Logger.Info("4. Verify the roll pipelined to MaxUnavailable=2 and never exceeded it")
	assert.LessOrEqualf(t, maxUnavailable, 2, "MaxUnavailable=2 must never be exceeded, observed %d", maxUnavailable)
	assert.Equalf(t, 2, maxUnavailable, "with MaxUnavailable=2 greater than MinAvailable=1 the roll should keep 2 frontend pods in flight, observed %d", maxUnavailable)
	assertUpdateInProgressCleared(tc)
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

	tests.Logger.Info("2. Delay pod readiness so replica 0's coherent update stays in flight while replica 1 is gang terminated")
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageReadyDelayedLongPath); err != nil {
		t.Fatalf("failed to apply readiness-delay KWOK stage: %v", err)
	}
	defer func() {
		if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedLongName); err != nil {
			tests.Logger.Warnf("cleanup: delete readiness-delay KWOK stage: %v", err)
		}
	}()
	tests.Logger.Info("2b. Make replica 1's fresh pods crash so gang termination fires for it while replica 0 rolls")
	if err := kwok.ApplyStage(tc.Ctx, tc.Client, kwokStageExitedErrorR1Path); err != nil {
		t.Fatalf("failed to apply crash KWOK stage: %v", err)
	}
	defer func() {
		if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageExitedErrorR1Name); err != nil {
			tests.Logger.Warnf("cleanup: delete crash KWOK stage: %v", err)
		}
	}()

	tests.Logger.Info("3. Trigger a coherent update of frontend and the inference PCSG, then wait until replica 0 is updating")
	for _, cliqueName := range []string{"frontend", "prefill"} {
		if err := triggerPodCliqueUpdate(tc, cliqueName); err != nil {
			t.Fatalf("failed to trigger update of %s: %v", cliqueName, err)
		}
	}
	if err := waitForOrdinalUpdating(tc, 0); err != nil {
		t.Fatalf("replica 0 did not start updating: %v", err)
	}

	tests.Logger.Info("4. While replica 0 is updating, gang terminate replica 1 and wait for its PodGangMap to be rebuilt")
	gangTerminateReplicaAndWaitForPodGangMapRebuild(t, tc, "prefill", 1)

	tests.Logger.Info("5. Confirm replica 0 was still under update when replica 1 was gang terminated")
	pcs, err := getPCS(tc, tc.Workload.Name)
	if err != nil {
		t.Fatalf("failed to read PodCliqueSet: %v", err)
	}
	if assert.NotNilf(t, pcs.Status.UpdateProgress, "expected an in-flight update") &&
		assert.Lenf(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1, "expected exactly one replica under update") {
		assert.Equalf(t, int32(0), pcs.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex, "replica 0 must still be the one under update")
		assert.Nilf(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].UpdateEndedAt, "replica 0's update must not have ended yet")
	}

	tests.Logger.Info("6. Remove the shaping stages, recover replica 1, and wait for the coherent update to complete")
	if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageExitedErrorR1Name); err != nil {
		t.Fatalf("failed to delete crash KWOK stage: %v", err)
	}
	if err := kwok.DeleteStage(tc.Ctx, tc.Client, kwokStageReadyDelayedLongName); err != nil {
		t.Fatalf("failed to delete readiness-delay KWOK stage: %v", err)
	}
	// Replica 1's rebuilt pods crashed while the stage was active and will not recover on their own, so
	// delete them once more to let fresh pods come up Ready under the default stage.
	deleteAllLivePodsOnReplica(t, tc, 1)
	if err := waitForRollingUpdateComplete(tc, 2); err != nil {
		t.Fatalf("coherent update did not complete: %v", err)
	}
	assertUpdateInProgressCleared(tc)

	tests.Logger.Info("7. Replica 0 keeps the coherent multi-anchor layout; replica 1 is back to the initial single-anchor layout")
	newHash := getPCSGenerationHash(t, tc)
	entries0 := getPodGangMapEntries(t, tc, 0)
	assertCoherentAnchorCompositions(t, entries0, newHash, "inference", []coherentAnchor{
		{standalone: map[string]int32{"frontend": 1}, pcsgIndices: []int32{0}},
		{standalone: map[string]int32{"frontend": 1}, pcsgIndices: []int32{1}},
	})
	assert.Empty(t, newHashTailPCSGIndices(entries0, newHash, "inference"), "replica 0's anchor-only plan must leave no leftover tail")

	assertReplicaPodGangMap(t, getPodGangMapEntries(t, tc, 1), expectedReplicaPodGangMap{
		standalonePodCounts: map[string]int32{"frontend": 2},
		pcsgName:            "inference",
		anchorIndices:       []int32{0},
		tailIndices:         []int32{1},
	})

	tests.Logger.Info("8. Verify all 12 pods are Ready")
	if err := tc.WaitForPods(coherentGTExpectedPods); err != nil {
		t.Fatalf("pods did not become Ready after the coherent update and gang termination: %v", err)
	}
}

type coherentAnchor struct {
	standalone  map[string]int32
	pcsgIndices []int32
}

// assertCoherentAnchorCompositions fails unless the new-hash anchor entries match want as a multiset, and
// every new-hash anchor carries newHash. It compares each anchor's standalone pod counts and its indices
// for pcsgName, ignoring other entries so steady-state scaffolding does not affect the match.
func assertCoherentAnchorCompositions(t *testing.T, entries []grovev1alpha1.PodGangEntry, newHash, pcsgName string, want []coherentAnchor) {
	t.Helper()
	gotKeys := make([]string, 0, len(want))
	for _, entry := range entries {
		if entry.Role != grovev1alpha1.PodGangEntryRoleAnchor || entry.PodCliqueSetGenerationHash != newHash {
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

// newHashTailPCSGIndices returns the indices for pcsgName carried by the single new-hash tail entry, or nil
// when no new-hash tail entry exists. It fails when more than one new-hash tail entry is present.
func newHashTailPCSGIndices(entries []grovev1alpha1.PodGangEntry, newHash, pcsgName string) []int32 {
	var indices []int32
	for _, entry := range entries {
		if entry.Role == grovev1alpha1.PodGangEntryRoleTail && entry.PodCliqueSetGenerationHash == newHash {
			indices = entry.PCSGReplicaIndices[pcsgName]
		}
	}
	return indices
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

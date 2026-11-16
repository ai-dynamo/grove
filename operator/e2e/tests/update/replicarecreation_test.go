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

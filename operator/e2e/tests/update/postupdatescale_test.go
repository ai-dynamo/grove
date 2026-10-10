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
	"sort"
	"testing"

	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/tests"

	"github.com/stretchr/testify/assert"
)

// Test_PCUS1_ScaleInAfterCoherentUpdateKeepsPodGangMapConsistent verifies steady-state PodGangMap scale
// reconciliation over the interleaved layout a coherent update leaves behind. This is not a coherent
// update test. The coherent update is only the setup that spreads the inference PodCliqueScalingGroup's
// replica indices across several anchor and tail entries. On a bootstrap layout the PodGangMap and the
// PodCliqueScalingGroup reconciler always pick the same replicas to remove, so the divergence this guards
// against cannot arise there.
//
// The inference PodCliqueScalingGroup is scaled to 4 before the update. The frontend standalone has a
// replicas to minAvailable ratio of 2, which caps the number of anchor bearing steps at 2, so inference
// rolls 2 indices per step. The completed layout is anchors carrying inference indices {0} and {2} and
// tails carrying {1} and {3}. Index 2 therefore sits in an anchor, and index 1 in a tail. A later
// scale-out adds index 4 into a ScaleOut entry, so a tail and a ScaleOut entry are both present.
//
// Scaling inference in from 5 to 2 makes the PodCliqueScalingGroup reconciler delete replicas 2, 3 and 4,
// the highest-numbered ones. The PodGangMap must remove the same indices, wherever they sit, and keep
// indices 0 and 1. A role-ordered drain would instead remove the tail indices 1 and 3 and the ScaleOut
// index 4 first and keep anchor index 2, leaving the map recording {0,2} while replicas {0,1} actually
// exist. The test asserts the map ends at exactly {0,1} and that a further coherent update still converges.
func Test_PCUS1_ScaleInAfterCoherentUpdateKeepsPodGangMapConsistent(t *testing.T) {
	tests.Logger.Info("1. Deploy workload-coherent and verify 6 pods")
	tc, cleanup, _ := setupTest(t, testConfig{
		workloadName: coherentWorkloadName,
		workloadYAML: coherentWorkloadYAML,
		// Peak is 12 pods at inference 5 (frontend 2 + inference 5 x 2). Each 80Mi pod takes a whole KWOK
		// node, so the test needs at least 12 schedulable nodes.
		workerNodes:  14,
		expectedPods: coherentExpectedPods,
	})
	defer cleanup()

	tests.Logger.Info("2. Scale the inference PCSG from 2 to 4 so a coherent update rolls 2 indices per step (10 pods)")
	tc.ScalePCSGAcrossAllReplicasAndWait(coherentWorkloadName, "inference", 1, 4, 10, 0)

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

	tests.Logger.Info("5. Verify anchors carry inference indices {0} and {2}, so a high index sits in an anchor")
	newHash := getPCSGenerationHash(t, tc)
	entries := getPodGangMapEntries(t, tc, 0)
	assertCoherentAnchorCompositions(t, entries, newHash, "inference", []coherentAnchor{
		{standalone: map[string]int32{"frontend": 1}, pcsgIndices: []int32{0}},
		{standalone: map[string]int32{"frontend": 1}, pcsgIndices: []int32{2}},
	})
	assert.Equal(t, []int32{1, 3}, pcsgIndicesForRole(entries, grovev1alpha1.PodGangEntryRoleTail, "inference"), "tails must carry inference indices 1 and 3")

	tests.Logger.Info("6. Scale the inference PCSG out from 4 to 5 so a ScaleOut entry carries index 4 (12 pods)")
	tc.ScalePCSGAcrossAllReplicasAndWait(coherentWorkloadName, "inference", 1, 5, 12, 0)
	entries = getPodGangMapEntries(t, tc, 0)
	assert.Equal(t, []int32{4}, pcsgIndicesForRole(entries, grovev1alpha1.PodGangEntryRoleScaleOut, "inference"), "the ScaleOut entry must carry inference index 4")
	assert.NotEmpty(t, pcsgIndicesForRole(entries, grovev1alpha1.PodGangEntryRoleTail, "inference"), "a tail entry must still be present alongside the ScaleOut entry")

	tests.Logger.Info("7. Scale the inference PCSG in from 5 to 2, deleting replicas 2, 3 and 4 (6 pods)")
	tc.ScalePCSGAcrossAllReplicasAndWait(coherentWorkloadName, "inference", 1, 2, 6, 0)

	tests.Logger.Info("8. The PodGangMap must record exactly the surviving inference replicas {0,1}")
	entries = getPodGangMapEntries(t, tc, 0)
	assert.Equal(t, []int32{0, 1}, allPCSGIndices(entries, "inference"), "the PodGangMap must record the same replicas the PCSG reconciler kept")

	tests.Logger.Info("9. A further coherent update still converges over the reconciled layout")
	prevHash := getPCSGenerationHash(t, tc)
	if err := triggerPodCliqueUpdate(tc, "frontend"); err != nil {
		t.Fatalf("failed to trigger the second update of frontend: %v", err)
	}
	// Wait until the second update is observed before waiting for completion, otherwise the first
	// update's UpdateEndedAt is still set and the completion wait returns on the stale state (issue #863).
	if err := waitForGenerationHashChange(tc, prevHash); err != nil {
		t.Fatalf("the second coherent update did not start: %v", err)
	}
	if err := waitForRollingUpdateComplete(tc, 1); err != nil {
		t.Fatalf("the second coherent update did not complete: %v", err)
	}
	assertUpdateInProgressCleared(tc)
	assertGenerationHashConverged(tc)
	// The second coherent update changes only the standalone frontend, so it opens a fresh anchor holding
	// both frontend replicas and no PodCliqueScalingGroup indices, while the pre-existing inference-bearing
	// anchor keeps index 0 (shedding its frontend pods to the new anchor) and the tail keeps index 1. The
	// map must reconverge to a single generation with the inference index set still exactly {0,1}.
	newHash = getPCSGenerationHash(t, tc)
	assertPodGangMapSingleGeneration(t, tc)
	entries = getPodGangMapEntries(t, tc, 0)
	assertCoherentAnchorCompositions(t, entries, newHash, "inference", []coherentAnchor{
		{standalone: map[string]int32{"frontend": 0}, pcsgIndices: []int32{0}},
		{standalone: map[string]int32{"frontend": 2}, pcsgIndices: nil},
	})
	assert.Equal(t, []int32{1}, newHashTailPCSGIndices(entries, newHash, "inference"), "the tail must keep inference index 1")
	scaleOutEntry := requireSingleEntryByRole(t, entries, grovev1alpha1.PodGangEntryRoleScaleOut)
	assert.Empty(t, scaleOutEntry.PCSGReplicaIndices, "the scale-out entry must carry no inference indices after scaling back in")
	assert.Equal(t, []int32{0, 1}, allPCSGIndices(entries, "inference"), "the second update must preserve the reconciled inference replicas")
}

// pcsgIndicesForRole returns the sorted union of pcsgName replica indices carried by every entry of the
// given role.
func pcsgIndicesForRole(entries []grovev1alpha1.PodGangEntry, role grovev1alpha1.PodGangEntryRole, pcsgName string) []int32 {
	var indices []int32
	for _, entry := range entries {
		if entry.Role == role {
			indices = append(indices, entry.PCSGReplicaIndices[pcsgName]...)
		}
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	return indices
}

// allPCSGIndices returns the sorted union of pcsgName replica indices carried by every entry, regardless
// of role.
func allPCSGIndices(entries []grovev1alpha1.PodGangEntry, pcsgName string) []int32 {
	var indices []int32
	for _, entry := range entries {
		indices = append(indices, entry.PCSGReplicaIndices[pcsgName]...)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	return indices
}

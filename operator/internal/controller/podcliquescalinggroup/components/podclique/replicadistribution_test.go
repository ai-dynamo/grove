// /*
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
// */

package podclique

import (
	"sort"
	"strconv"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
)

// Common test fixtures.
//
// Naming conventions used in tests:
//
//   - Unified naming convention (used for MPGs, TailPGs, and steady-state Scaled-PGs created
//     under the new scheme): <pcsName>-<pcsReplicaIndex>-<unix-nano>. The suffix is derived
//     from clock.Clock at the time the name is generated; tests pin a FakeClock to make the
//     suffix deterministic.
//   - Legacy SPG (pre-upgrade scaled PodGang): <pcsgFQN>-<counter>.
//   - BPG (legacy base PodGang): <pcsName>-<pcsReplicaIndex>.
//
// MPG/TailPG fixture names below use small integer suffixes (0, 1, 2, 3) to keep table-test
// expectations readable; the format is the same as a unix-nano-suffixed name, just with a
// small placeholder value.
const (
	tcsPCSName         = "simple1"
	tcsPCSReplica      = 0
	tcsHash            = "abc123"
	tcsPCSGConfigName  = "sga"
	tcsPCSGFQN         = "simple1-0-sga"
	tcsBPGName         = "simple1-0"
	tcsLegacySPGPrefix = "simple1-0-sga-"
	// Unified-naming PodGangs (placeholder small integer suffixes).
	tcsMPG0    = "simple1-0-0"
	tcsMPG1    = "simple1-0-1"
	tcsTailPG2 = "simple1-0-2"
	tcsTailPG3 = "simple1-0-3"
	// Legacy SPGs.
	tcsLegacySPG0 = "simple1-0-sga-0"
	tcsLegacySPG1 = "simple1-0-sga-1"
	// PodGangs created during a steady-state PCSG scale-out under the unified scheme. The
	// suffixes are derived from tcsScaleOutBaseNanos; tests that exercise scale-out pin a
	// FakeClock at this base so the generated names match these constants.
	tcsScaleOutBaseNanos = 100000
	tcsScaleOutPG0       = "simple1-0-100000"
	tcsScaleOutPG1       = "simple1-0-100001"
)

// newSyncContextForMappingTests builds a syncState usable by computeDesiredPCSGReplicaMapping
// and buildMappingFromPodGangMap. The PCS spec drives IsCoherentUpdateInProgress: when
// coherentUpdateInProgress=true, UpdateProgress is set with no UpdateEndedAt (mirrors the live
// definition in IsCoherentUpdateInProgress).
func newSyncContextForMappingTests(
	pcsgSpecReplicas int32,
	pcsgStatusMapping []grovecorev1alpha1.PodGangReplicaAssignment,
	pgmEntries []grovecorev1alpha1.PodGangEntry,
	coherentUpdateInProgress bool,
) *syncState {
	pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: tcsPCSGFQN, Namespace: "default"},
		Spec:       grovecorev1alpha1.PodCliqueScalingGroupSpec{Replicas: pcsgSpecReplicas},
		Status:     grovecorev1alpha1.PodCliqueScalingGroupStatus{PodGangMapping: pcsgStatusMapping},
	}
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: tcsPCSName, Namespace: "default"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy},
		},
		Status: grovecorev1alpha1.PodCliqueSetStatus{
			CurrentGenerationHash: ptr.To(tcsHash),
		},
	}
	if coherentUpdateInProgress {
		pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{}
	}
	pgm := &grovecorev1alpha1.PodGangMap{
		Spec: grovecorev1alpha1.PodGangMapSpec{Entries: pgmEntries},
	}
	return &syncState{
		pcs:             pcs,
		pcsg:            pcsg,
		pcsgConfig:      &grovecorev1alpha1.PodCliqueScalingGroupConfig{Name: tcsPCSGConfigName},
		pcsReplicaIndex: tcsPCSReplica,
		podGangMap:      pgm,
	}
}

// anchor / tail / scaleOut build PodGangReplicaAssignment fixtures for the mapping tests.
func anchorAssignment(epoch string, idx int32, replicaIndices ...int32) grovecorev1alpha1.PodGangReplicaAssignment {
	return grovecorev1alpha1.PodGangReplicaAssignment{Epoch: epoch, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, AnchorIndex: idx, ReplicaIndices: replicaIndices}
}
func tailAssignment(epoch string, replicaIndices ...int32) grovecorev1alpha1.PodGangReplicaAssignment {
	return grovecorev1alpha1.PodGangReplicaAssignment{Epoch: epoch, Role: grovecorev1alpha1.PodGangEntryRoleTail, ReplicaIndices: replicaIndices}
}
func scaleOutAssignment(epoch string, replicaIndices ...int32) grovecorev1alpha1.PodGangReplicaAssignment {
	return grovecorev1alpha1.PodGangReplicaAssignment{Epoch: epoch, Role: grovecorev1alpha1.PodGangEntryRoleScaleOut, ReplicaIndices: replicaIndices}
}

func TestBuildMappingFromPodGangMap(t *testing.T) {
	r := _resource{clk: clock.RealClock{}}

	t.Run("entries referencing this PCSG are picked up; others ignored", func(t *testing.T) {
		sc := newSyncContextForMappingTests(0, nil, []grovecorev1alpha1.PodGangEntry{
			{Epoch: "100", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, AnchorIndex: 0, PCSGReplicaIndices: map[string][]int32{tcsPCSGConfigName: {0, 1}}},
			{Epoch: "150", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, AnchorIndex: 1, PCSGReplicaIndices: map[string][]int32{"other-pcsg": {0, 1, 2, 3, 4}}},
			{Epoch: "200", Role: grovecorev1alpha1.PodGangEntryRoleTail, PCSGReplicaIndices: map[string][]int32{tcsPCSGConfigName: {2, 3, 4}}},
		}, false)
		got := r.buildMappingFromPodGangMap(sc)
		assert.ElementsMatch(t, []grovecorev1alpha1.PodGangReplicaAssignment{
			anchorAssignment("100", 0, 0, 1),
			tailAssignment("200", 2, 3, 4),
		}, got)
	})

	t.Run("entries with empty index slices are skipped", func(t *testing.T) {
		sc := newSyncContextForMappingTests(0, nil, []grovecorev1alpha1.PodGangEntry{
			{Epoch: "100", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, AnchorIndex: 0, PCSGReplicaIndices: map[string][]int32{tcsPCSGConfigName: {0, 1}}},
			{Epoch: "200", Role: grovecorev1alpha1.PodGangEntryRoleTail, PCSGReplicaIndices: map[string][]int32{tcsPCSGConfigName: {}}},
		}, false)
		got := r.buildMappingFromPodGangMap(sc)
		assert.ElementsMatch(t, []grovecorev1alpha1.PodGangReplicaAssignment{anchorAssignment("100", 0, 0, 1)}, got)
	})

	t.Run("empty PGM yields empty mapping", func(t *testing.T) {
		sc := newSyncContextForMappingTests(0, nil, nil, false)
		assert.Empty(t, r.buildMappingFromPodGangMap(sc))
	})
}

func TestComputeDesiredPCSGReplicaMapping(t *testing.T) {
	r := _resource{clk: clocktesting.NewFakeClock(time.Unix(0, tcsScaleOutBaseNanos))}

	t.Run("coherent update in progress — overwrites from PGM regardless of status", func(t *testing.T) {
		// Status mapping says one thing; PGM says another. Coherent-update flow should pick PGM.
		sc := newSyncContextForMappingTests(
			4,
			[]grovecorev1alpha1.PodGangReplicaAssignment{anchorAssignment("999", 0, 99)}, // bogus status, should be ignored
			[]grovecorev1alpha1.PodGangEntry{
				{Epoch: "100", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, AnchorIndex: 0, PCSGReplicaIndices: map[string][]int32{tcsPCSGConfigName: {0, 1}}},
				{Epoch: "200", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, AnchorIndex: 1, PCSGReplicaIndices: map[string][]int32{tcsPCSGConfigName: {2, 3}}},
			},
			true, // coherent update in progress
		)
		got, err := r.computeDesiredPCSGReplicaMapping(sc)
		require.NoError(t, err)
		assert.ElementsMatch(t, []grovecorev1alpha1.PodGangReplicaAssignment{
			anchorAssignment("100", 0, 0, 1),
			anchorAssignment("200", 1, 2, 3),
		}, got)
	})

	t.Run("fresh PCSG (empty status) — seeds from PGM", func(t *testing.T) {
		sc := newSyncContextForMappingTests(
			4,
			nil,
			[]grovecorev1alpha1.PodGangEntry{
				{Epoch: "100", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, AnchorIndex: 0, PCSGReplicaIndices: map[string][]int32{tcsPCSGConfigName: {0, 1}}},
				{Epoch: "200", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, AnchorIndex: 1, PCSGReplicaIndices: map[string][]int32{tcsPCSGConfigName: {2, 3}}},
			},
			false,
		)
		got, err := r.computeDesiredPCSGReplicaMapping(sc)
		require.NoError(t, err)
		assert.ElementsMatch(t, []grovecorev1alpha1.PodGangReplicaAssignment{
			anchorAssignment("100", 0, 0, 1),
			anchorAssignment("200", 1, 2, 3),
		}, got)
	})

	t.Run("steady state, no drift — returns clone of status mapping", func(t *testing.T) {
		status := []grovecorev1alpha1.PodGangReplicaAssignment{anchorAssignment("100", 0, 0, 1), anchorAssignment("200", 1, 2, 3)}
		sc := newSyncContextForMappingTests(4, status, nil, false)
		got, err := r.computeDesiredPCSGReplicaMapping(sc)
		require.NoError(t, err)
		assert.ElementsMatch(t, status, got)
		// Mutating the result must not affect the input — verifies clone.
		got[0].ReplicaIndices = append(got[0].ReplicaIndices, 99)
		assert.Equal(t, []int32{0, 1}, status[0].ReplicaIndices)
	})

	t.Run("scale-out — appends new replica indices to a ScaleOut assignment", func(t *testing.T) {
		// Spec=6, status sums to 4 (anchor0:[0,1], anchor1:[2,3]) → diff=+2 → the next contiguous
		// indices [4, 5] attach to the pre-created ScaleOut entry, taking its epoch from the PodGangMap.
		status := []grovecorev1alpha1.PodGangReplicaAssignment{anchorAssignment("100", 0, 0, 1), anchorAssignment("200", 1, 2, 3)}
		pgmEntries := []grovecorev1alpha1.PodGangEntry{
			{Epoch: "300", Role: grovecorev1alpha1.PodGangEntryRoleScaleOut, PodCliqueSetGenerationHash: tcsHash},
		}
		sc := newSyncContextForMappingTests(6, status, pgmEntries, false)
		got, err := r.computeDesiredPCSGReplicaMapping(sc)
		require.NoError(t, err)
		assert.ElementsMatch(t, []grovecorev1alpha1.PodGangReplicaAssignment{
			anchorAssignment("100", 0, 0, 1),
			anchorAssignment("200", 1, 2, 3),
			scaleOutAssignment("300", 4, 5),
		}, got)
	})

	t.Run("scale-out appends to an existing ScaleOut assignment", func(t *testing.T) {
		// Status already has a ScaleOut assignment holding index 4; spec grows by 1 → index 5 is
		// appended to the same ScaleOut assignment (not a new one).
		status := []grovecorev1alpha1.PodGangReplicaAssignment{
			anchorAssignment("100", 0, 0, 1),
			anchorAssignment("200", 1, 2, 3),
			scaleOutAssignment("300", 4),
		}
		pgmEntries := []grovecorev1alpha1.PodGangEntry{
			{Epoch: "300", Role: grovecorev1alpha1.PodGangEntryRoleScaleOut, PodCliqueSetGenerationHash: tcsHash},
		}
		sc := newSyncContextForMappingTests(6, status, pgmEntries, false)
		got, err := r.computeDesiredPCSGReplicaMapping(sc)
		require.NoError(t, err)
		assert.ElementsMatch(t, []grovecorev1alpha1.PodGangReplicaAssignment{
			anchorAssignment("100", 0, 0, 1),
			anchorAssignment("200", 1, 2, 3),
			scaleOutAssignment("300", 4, 5),
		}, got)
	})

	t.Run("scale-in — drains ScaleOut, then Tail, then highest-AnchorIndex anchor; empties pruned", func(t *testing.T) {
		// Status sums to 6 (anchor0:[0,1], anchor1:[2,3], scaleOut:[5], tail:[4]); spec=4 → diff=-2.
		// Drain order: ScaleOut ([5]) first, then Tail ([4]). Both empty and are pruned; anchors kept.
		status := []grovecorev1alpha1.PodGangReplicaAssignment{
			anchorAssignment("100", 0, 0, 1),
			anchorAssignment("200", 1, 2, 3),
			tailAssignment("300", 4),
			scaleOutAssignment("400", 5),
		}
		sc := newSyncContextForMappingTests(4, status, nil, false)
		got, err := r.computeDesiredPCSGReplicaMapping(sc)
		require.NoError(t, err)
		assert.ElementsMatch(t, []grovecorev1alpha1.PodGangReplicaAssignment{
			anchorAssignment("100", 0, 0, 1),
			anchorAssignment("200", 1, 2, 3),
		}, got)
	})

	t.Run("scale-in drains the highest-AnchorIndex anchor last-resort when only anchors remain", func(t *testing.T) {
		// Only anchors present. Spec=3, status sums to 4 → diff=-1. The highest-AnchorIndex anchor
		// (idx 1) is drained; its highest replica index (3) is removed.
		status := []grovecorev1alpha1.PodGangReplicaAssignment{anchorAssignment("100", 0, 0, 1), anchorAssignment("200", 1, 2, 3)}
		sc := newSyncContextForMappingTests(3, status, nil, false)
		got, err := r.computeDesiredPCSGReplicaMapping(sc)
		require.NoError(t, err)
		assert.ElementsMatch(t, []grovecorev1alpha1.PodGangReplicaAssignment{
			anchorAssignment("100", 0, 0, 1),
			anchorAssignment("200", 1, 2),
		}, got)
	})

	t.Run("orphan empty-slice assignment is pruned", func(t *testing.T) {
		// A pre-existing empty assignment in status must be removed even when no scale runs.
		status := []grovecorev1alpha1.PodGangReplicaAssignment{anchorAssignment("100", 0, 0, 1), tailAssignment("200")}
		sc := newSyncContextForMappingTests(2, status, nil, false)
		got, err := r.computeDesiredPCSGReplicaMapping(sc)
		require.NoError(t, err)
		assert.ElementsMatch(t, []grovecorev1alpha1.PodGangReplicaAssignment{anchorAssignment("100", 0, 0, 1)}, got)
	})

	t.Run("scale-in does not mutate the input status mapping (clone)", func(t *testing.T) {
		status := []grovecorev1alpha1.PodGangReplicaAssignment{anchorAssignment("100", 0, 0, 1), anchorAssignment("200", 1, 2, 3)}
		sc := newSyncContextForMappingTests(3, status, nil, false)
		_, err := r.computeDesiredPCSGReplicaMapping(sc)
		require.NoError(t, err)
		// Original status mapping unchanged.
		assert.Equal(t, []int32{0, 1}, status[0].ReplicaIndices)
		assert.Equal(t, []int32{2, 3}, status[1].ReplicaIndices)
	})
}

func TestComputePCSGCountDeltas(t *testing.T) {
	// Test PCSG has two cliques. The "covered" check requires both cliques present at an index.
	cliqueNames := []string{"pcb", "pcc"}

	t.Run("no desired and no live PCLQs is a no-op", func(t *testing.T) {
		dels, creates, err := computePCSGReplicaCreationsAndDeletions(map[int]string{}, nil, cliqueNames)
		require.NoError(t, err)
		assert.Empty(t, dels)
		assert.Empty(t, creates)
	})

	t.Run("desired is empty: all live indices are obsolete and flagged for deletion", func(t *testing.T) {
		// 2 replicas live under tcsMPG0; desired is empty (e.g. PCSG was deleted in spec).
		live := []grovecorev1alpha1.PodClique{
			pcsgPCLQ("pcb", 0, tcsMPG0, false),
			pcsgPCLQ("pcc", 0, tcsMPG0, false),
			pcsgPCLQ("pcb", 1, tcsMPG0, false),
			pcsgPCLQ("pcc", 1, tcsMPG0, false),
		}
		dels, creates, err := computePCSGReplicaCreationsAndDeletions(map[int]string{}, live, cliqueNames)
		require.NoError(t, err)
		sort.Ints(dels)
		assert.Equal(t, []int{0, 1}, dels)
		assert.Empty(t, creates)
	})

	t.Run("fully populated steady state: no deltas", func(t *testing.T) {
		desired := map[int]string{0: tcsMPG0, 1: tcsMPG1}
		live := []grovecorev1alpha1.PodClique{
			pcsgPCLQ("pcb", 0, tcsMPG0, false),
			pcsgPCLQ("pcc", 0, tcsMPG0, false),
			pcsgPCLQ("pcb", 1, tcsMPG1, false),
			pcsgPCLQ("pcc", 1, tcsMPG1, false),
		}
		dels, creates, err := computePCSGReplicaCreationsAndDeletions(desired, live, cliqueNames)
		require.NoError(t, err)
		assert.Empty(t, dels)
		assert.Empty(t, creates)
	})

	t.Run("missing replica entirely: index stays in creations, no deletion", func(t *testing.T) {
		// Index 1 is desired but no live PCLQs at index 1.
		desired := map[int]string{0: tcsMPG0, 1: tcsMPG1}
		live := []grovecorev1alpha1.PodClique{
			pcsgPCLQ("pcb", 0, tcsMPG0, false),
			pcsgPCLQ("pcc", 0, tcsMPG0, false),
		}
		dels, creates, err := computePCSGReplicaCreationsAndDeletions(desired, live, cliqueNames)
		require.NoError(t, err)
		assert.Empty(t, dels)
		assert.Equal(t, map[int]string{1: tcsMPG1}, creates)
	})

	t.Run("wrong PodGang label: index flagged for deletion AND stays in creations", func(t *testing.T) {
		// Live label at index 0 says tcsMPG1, but desired says tcsMPG0. The whole replica
		// gets deleted; the next reconcile will recreate under the correct PodGang.
		desired := map[int]string{0: tcsMPG0}
		live := []grovecorev1alpha1.PodClique{
			pcsgPCLQ("pcb", 0, tcsMPG1, false),
			pcsgPCLQ("pcc", 0, tcsMPG1, false),
		}
		dels, creates, err := computePCSGReplicaCreationsAndDeletions(desired, live, cliqueNames)
		require.NoError(t, err)
		assert.Equal(t, []int{0}, dels)
		assert.Equal(t, map[int]string{0: tcsMPG0}, creates)
	})

	t.Run("obsolete index (live but not in desired): flagged for deletion, no creation", func(t *testing.T) {
		desired := map[int]string{0: tcsMPG0}
		live := []grovecorev1alpha1.PodClique{
			pcsgPCLQ("pcb", 0, tcsMPG0, false),
			pcsgPCLQ("pcc", 0, tcsMPG0, false),
			pcsgPCLQ("pcb", 5, tcsMPG1, false),
			pcsgPCLQ("pcc", 5, tcsMPG1, false),
		}
		dels, creates, err := computePCSGReplicaCreationsAndDeletions(desired, live, cliqueNames)
		require.NoError(t, err)
		assert.Equal(t, []int{5}, dels)
		assert.Empty(t, creates)
	})

	t.Run("half-populated replica (one clique missing): stays in creations, NOT deleted", func(t *testing.T) {
		// Index 0 has only pcc; pcb is missing entirely. The lone live PCLQ has the correct
		// PodGang label. Without the per-clique presence check, this would be wrongly flagged
		// as "covered" and the missing pcb would never be created.
		desired := map[int]string{0: tcsMPG0}
		live := []grovecorev1alpha1.PodClique{
			pcsgPCLQ("pcc", 0, tcsMPG0, false),
		}
		dels, creates, err := computePCSGReplicaCreationsAndDeletions(desired, live, cliqueNames)
		require.NoError(t, err)
		assert.Empty(t, dels, "the existing pcc must not be deleted")
		assert.Equal(t, map[int]string{0: tcsMPG0}, creates, "pcb must be re-emitted for creation")
	})

	t.Run("terminating PCLQs are ignored (whole replica terminating): index stays in creations", func(t *testing.T) {
		// Both PCLQs at index 0 are terminating with the OLD PodGang label. desiredIndexToPG
		// says the index should belong to tcsMPG0 now. Without the terminating filter, the
		// stale OLD label would mark the index as live-with-wrong-label, but no actual delete
		// would happen (already terminating) and creations would be retained — but the whole
		// replica is in flux. The right behavior: ignore the terminators entirely so the
		// covered check sees an empty index → emit creations, no spurious deletes.
		desired := map[int]string{0: tcsMPG0}
		live := []grovecorev1alpha1.PodClique{
			pcsgPCLQ("pcb", 0, tcsMPG1, true), // terminating, old label
			pcsgPCLQ("pcc", 0, tcsMPG1, true),
		}
		dels, creates, err := computePCSGReplicaCreationsAndDeletions(desired, live, cliqueNames)
		require.NoError(t, err)
		assert.Empty(t, dels, "terminating PCLQs must not be re-flagged for deletion")
		assert.Equal(t, map[int]string{0: tcsMPG0}, creates)
	})

	t.Run("one clique terminating, sibling live with correct label: still treated as half-populated", func(t *testing.T) {
		// pcb is terminating (old hash, finalizer slow); pcc is fresh with the correct new
		// PodGang label. The terminator is ignored, so liveCliquesByIndex[0] = {pcc}, which
		// fails the equality check against {pcb, pcc} → creations[0] retained. Once pcb
		// finishes terminating, the next reconcile creates a new pcb under tcsMPG0; pcc's
		// re-create attempt hits AlreadyExists and is swallowed.
		desired := map[int]string{0: tcsMPG0}
		live := []grovecorev1alpha1.PodClique{
			pcsgPCLQ("pcb", 0, tcsMPG1, true),  // terminating, old label
			pcsgPCLQ("pcc", 0, tcsMPG0, false), // fresh, new label
		}
		dels, creates, err := computePCSGReplicaCreationsAndDeletions(desired, live, cliqueNames)
		require.NoError(t, err)
		assert.Empty(t, dels, "the live pcc must not be deleted")
		assert.Equal(t, map[int]string{0: tcsMPG0}, creates, "pcb must be re-emitted for creation")
	})

	t.Run("all cliques present, one terminating with the correct label: still half-populated", func(t *testing.T) {
		// Edge case: pcb is terminating with the desired PodGang label (e.g. it was deleted
		// externally during a stable period). The terminating filter excludes it, so the
		// index looks half-populated and pcb gets re-emitted. doCreate will swallow
		// AlreadyExists if the terminator hasn't finalized yet, retrying next reconcile.
		desired := map[int]string{0: tcsMPG0}
		live := []grovecorev1alpha1.PodClique{
			pcsgPCLQ("pcb", 0, tcsMPG0, true), // terminating, correct label
			pcsgPCLQ("pcc", 0, tcsMPG0, false),
		}
		dels, creates, err := computePCSGReplicaCreationsAndDeletions(desired, live, cliqueNames)
		require.NoError(t, err)
		assert.Empty(t, dels)
		assert.Equal(t, map[int]string{0: tcsMPG0}, creates)
	})

	t.Run("multiple indices with mixed states", func(t *testing.T) {
		// idx 0: fully covered under tcsMPG0 → no delta.
		// idx 1: half-populated under tcsMPG0 → stays in creations.
		// idx 2: wrong label (live=tcsMPG1, desired=tcsMPG0) → delete + retain creations.
		// idx 3: not live at all → stays in creations.
		// idx 7: obsolete (live but not desired) → delete only.
		desired := map[int]string{
			0: tcsMPG0,
			1: tcsMPG0,
			2: tcsMPG0,
			3: tcsMPG1,
		}
		live := []grovecorev1alpha1.PodClique{
			pcsgPCLQ("pcb", 0, tcsMPG0, false),
			pcsgPCLQ("pcc", 0, tcsMPG0, false),
			pcsgPCLQ("pcc", 1, tcsMPG0, false), // half-populated
			pcsgPCLQ("pcb", 2, tcsMPG1, false), // wrong label
			pcsgPCLQ("pcc", 2, tcsMPG1, false),
			pcsgPCLQ("pcb", 7, tcsMPG0, false), // obsolete (idx 7 not desired)
			pcsgPCLQ("pcc", 7, tcsMPG0, false),
		}
		dels, creates, err := computePCSGReplicaCreationsAndDeletions(desired, live, cliqueNames)
		require.NoError(t, err)
		sort.Ints(dels)
		assert.Equal(t, []int{2, 7}, dels)
		assert.Equal(t, map[int]string{1: tcsMPG0, 2: tcsMPG0, 3: tcsMPG1}, creates)
	})

	// --- Error paths ---

	t.Run("error on PCLQ missing PodGang label", func(t *testing.T) {
		live := []grovecorev1alpha1.PodClique{
			pcsgPCLQ("pcb", 0, tcsMPG0, false),
			pcsgPCLQWithoutLabels("pcc", 0, apicommon.LabelPodGang), // strip podgang label
		}
		_, _, err := computePCSGReplicaCreationsAndDeletions(map[int]string{0: tcsMPG0}, live, cliqueNames)
		require.Error(t, err)
		assert.Contains(t, err.Error(), apicommon.LabelPodGang)
	})

	t.Run("error on PCLQ missing replica-index label", func(t *testing.T) {
		live := []grovecorev1alpha1.PodClique{
			pcsgPCLQWithoutLabels("pcb", 0, apicommon.LabelPodCliqueScalingGroupReplicaIndex),
		}
		_, _, err := computePCSGReplicaCreationsAndDeletions(map[int]string{0: tcsMPG0}, live, cliqueNames)
		require.Error(t, err)
		assert.Contains(t, err.Error(), apicommon.LabelPodCliqueScalingGroupReplicaIndex)
	})

	t.Run("error on divergent PodGang labels at the same index", func(t *testing.T) {
		// Contract violation: two cliques at one PCSG replica must share the same PodGang label.
		live := []grovecorev1alpha1.PodClique{
			pcsgPCLQ("pcb", 0, tcsMPG0, false),
			pcsgPCLQ("pcc", 0, tcsMPG1, false),
		}
		_, _, err := computePCSGReplicaCreationsAndDeletions(map[int]string{0: tcsMPG0}, live, cliqueNames)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "divergent")
	})
}

// pcsgPCLQ constructs a non-/terminating PCSG-owned PodClique fixture with the labels the
// production code requires. cliqueName is unqualified (e.g. "pcb"), pcsgReplicaIndex is the
// PCSG replica the PCLQ belongs to, and podGangName is the value of the LabelPodGang label.
// terminating=true sets a non-nil DeletionTimestamp so IsResourceTerminating returns true.
func pcsgPCLQ(cliqueName string, pcsgReplicaIndex int, podGangName string, terminating bool) grovecorev1alpha1.PodClique {
	pclq := grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tcsPCSGFQN + "-" + strconv.Itoa(pcsgReplicaIndex) + "-" + cliqueName,
			Namespace: "default",
			Labels: map[string]string{
				apicommon.LabelPodCliqueScalingGroup:             tcsPCSGFQN,
				apicommon.LabelPodCliqueScalingGroupReplicaIndex: strconv.Itoa(pcsgReplicaIndex),
				apicommon.LabelPodGang:                           podGangName,
			},
		},
	}
	if terminating {
		now := metav1.Now()
		pclq.DeletionTimestamp = &now
		pclq.Finalizers = []string{"grove.io/test"}
	}
	return pclq
}

// pcsgPCLQWithoutLabels constructs a PCLQ fixture where the named label is missing. Used by
// error-path tests that exercise the contract-violation branches of computePCSGReplicaCreationsAndDeletions.
func pcsgPCLQWithoutLabels(cliqueName string, pcsgReplicaIndex int, missingLabel string) grovecorev1alpha1.PodClique {
	pclq := pcsgPCLQ(cliqueName, pcsgReplicaIndex, tcsMPG0, false)
	delete(pclq.Labels, missingLabel)
	return pclq
}

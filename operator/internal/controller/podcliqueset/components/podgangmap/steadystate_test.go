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

package podgangmap

import (
	"errors"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
)

const (
	testPCSName   = "my-pcs"
	testNamespace = "default"
	testHash      = "gen-hash-1"
)

func TestBuildBootstrapEntries(t *testing.T) {
	clk := clocktesting.NewFakeClock(time.Unix(0, 1000))

	t.Run("standalone PCLQs and PCSGs above minAvailable", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithStandaloneCliqueReplicas("frontend", 5).
			WithScalingGroupConfig("prefill", []string{"pworker"}, 4, 1).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()

		entries := buildBootstrapEntries(pcs, clk)
		require.Len(t, entries, 2)

		mpg := anchorEntry(t, entries)
		assert.Equal(t, grovecorev1alpha1.PodGangEntryRoleAnchor, mpg.Role)
		assert.Nil(t, mpg.DependsOn)
		assert.Equal(t, testHash, mpg.PodCliqueSetGenerationHash)
		assert.Equal(t, map[string]int32{"frontend": 5}, mpg.PodCliques)
		assert.Equal(t, map[string][]int32{"prefill": {0}}, mpg.PCSGReplicaIndices)

		tails := tpgEntries(entries)
		require.Len(t, tails, 1)
		tail := tails[0]
		assert.Equal(t, map[string][]int32{"prefill": {1, 2, 3}}, tail.PCSGReplicaIndices)
		assert.Empty(t, tail.PodCliques)
		assert.Equal(t, []string{mpg.Epoch}, tail.DependsOn)

		// Epoch is the batch identity: mpg and tail carry distinct epochs, tail = mpg+1.
		assert.NotEqual(t, mpg.Epoch, tail.Epoch)
		assert.Equal(t, "1000", mpg.Epoch)
		assert.Equal(t, "1001", tail.Epoch)
	})

	t.Run("PCSG total equals minAvailable emits no non-mpg entry", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithScalingGroupConfig("prefill", []string{"pworker"}, 2, 2).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()

		entries := buildBootstrapEntries(pcs, clk)
		require.Len(t, entries, 1)
		mpg := anchorEntry(t, entries)
		assert.Equal(t, map[string][]int32{"prefill": {0, 1}}, mpg.PCSGReplicaIndices)
	})

	t.Run("standalone only, no PCSGs", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithStandaloneCliqueReplicas("frontend", 3).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()

		entries := buildBootstrapEntries(pcs, clk)
		require.Len(t, entries, 1)
		mpg := anchorEntry(t, entries)
		assert.Equal(t, map[string]int32{"frontend": 3}, mpg.PodCliques)
		assert.Empty(t, mpg.PCSGReplicaIndices)
	})

	t.Run("multiple PCSGs above minAvailable aggregate into one TPG entry", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithScalingGroupConfig("prefill", []string{"pworker"}, 3, 1).
			WithScalingGroupConfig("decode", []string{"dworker"}, 2, 1).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()

		entries := buildBootstrapEntries(pcs, clk)
		tails := tpgEntries(entries)
		require.Len(t, tails, 1)

		tail := tails[0]
		assert.Equal(t, map[string][]int32{"prefill": {1, 2}, "decode": {1}}, tail.PCSGReplicaIndices)
		assert.Equal(t, []string{anchorEntry(t, entries).Epoch}, tail.DependsOn)
	})
}

func TestBuildBootstrapMPG(t *testing.T) {
	t.Run("carries standalone full counts and PCSG floor indices", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithStandaloneCliqueReplicas("frontend", 4).
			WithScalingGroupConfig("prefill", []string{"pworker"}, 5, 2).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()

		mpg := buildBootstrapMPGEntry(pcs, "epoch-0")
		assert.Equal(t, grovecorev1alpha1.PodGangEntryRoleAnchor, mpg.Role)
		assert.Nil(t, mpg.DependsOn)
		assert.Equal(t, "epoch-0", mpg.Epoch)
		assert.Equal(t, map[string]int32{"frontend": 4}, mpg.PodCliques)
		assert.Equal(t, map[string][]int32{"prefill": {0, 1}}, mpg.PCSGReplicaIndices)
	})

	t.Run("PCSG-only PCS has empty PodCliques on mpg", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithScalingGroupConfig("prefill", []string{"pworker"}, 3, 1).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()

		mpg := buildBootstrapMPGEntry(pcs, "epoch-0")
		assert.Empty(t, mpg.PodCliques)
		assert.Equal(t, map[string][]int32{"prefill": {0}}, mpg.PCSGReplicaIndices)
	})
}

func TestBuildBootstrapTPGEntry(t *testing.T) {
	t.Run("one entry carrying a PCSG's indices above minAvailable", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithScalingGroupConfig("prefill", []string{"pworker"}, 4, 1).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()

		tpgEntry, ok := buildBootstrapTPGEntry(pcs, "tpg-epoch", "mpg-epoch")
		require.True(t, ok)
		assert.Equal(t, grovecorev1alpha1.PodGangEntryRoleTail, tpgEntry.Role)
		assert.Equal(t, map[string][]int32{"prefill": {1, 2, 3}}, tpgEntry.PCSGReplicaIndices)
		assert.Equal(t, "tpg-epoch", tpgEntry.Epoch)
		assert.Equal(t, []string{"mpg-epoch"}, tpgEntry.DependsOn)
	})

	t.Run("single entry aggregates indices across all PCSGs", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithScalingGroupConfig("prefill", []string{"pworker"}, 4, 1).
			WithScalingGroupConfig("decode", []string{"dworker"}, 3, 1).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()

		tpgEntry, ok := buildBootstrapTPGEntry(pcs, "tpg-epoch", "mpg-epoch")
		require.True(t, ok)
		assert.Equal(t, map[string][]int32{"prefill": {1, 2, 3}, "decode": {1, 2}}, tpgEntry.PCSGReplicaIndices)
	})

	t.Run("PCSG at minAvailable is skipped", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithScalingGroupConfig("prefill", []string{"pworker"}, 2, 2).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()

		_, ok := buildBootstrapTPGEntry(pcs, "tpg-epoch", "mpg-epoch")
		assert.False(t, ok)
	})

	t.Run("no PCSGs yields no entry", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithStandaloneCliqueReplicas("frontend", 3).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()

		_, ok := buildBootstrapTPGEntry(pcs, "tpg-epoch", "mpg-epoch")
		assert.False(t, ok)
	})
}

func TestBuildEntriesFromPCLQAndPCSGStatuses(t *testing.T) {
	clk := clocktesting.NewFakeClock(time.Unix(0, 5000))

	newPCS := func() *grovecorev1alpha1.PodCliqueSet {
		return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithStandaloneCliqueReplicas("frontend", 5).
			WithScalingGroupConfig("prefill", []string{"pworker"}, 3, 1).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()
	}
	// standalone PCLQ FQN is <pcs>-<replica>-<clique>.
	newFrontendPCLQ := func(mapping []grovecorev1alpha1.PodGangPodCountAssignment) grovecorev1alpha1.PodClique {
		return *testutils.NewPodCliqueBuilder(testPCSName, "uid", "frontend", testNamespace, 0).
			WithPodGangMapping(mapping).Build()
	}
	newPrefillPCSG := func(mapping []grovecorev1alpha1.PodGangReplicaAssignment) grovecorev1alpha1.PodCliqueScalingGroup {
		return *testutils.NewPodCliqueScalingGroupBuilder("my-pcs-0-prefill", testNamespace, testPCSName, 0).
			WithPodGangMapping(mapping).Build()
	}

	// Epochs used to seed existingPGM and address entries by identity.
	anchorEpoch, tailEpoch, scaleOutEpoch := "e-anchor", "e-tail", "e-scaleout"

	t.Run("refreshes membership for existing epochs, preserving identity", func(t *testing.T) {
		pcs := newPCS()
		pclqs := []grovecorev1alpha1.PodClique{newFrontendPCLQ([]grovecorev1alpha1.PodGangPodCountAssignment{
			{Epoch: anchorEpoch, PodCount: 2},
		})}
		pcsgs := []grovecorev1alpha1.PodCliqueScalingGroup{newPrefillPCSG([]grovecorev1alpha1.PodGangReplicaAssignment{
			{Epoch: anchorEpoch, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, ReplicaIndices: []int32{0}},
			{Epoch: tailEpoch, Role: grovecorev1alpha1.PodGangEntryRoleTail, ReplicaIndices: []int32{1, 2}},
		})}
		existingPGM := &grovecorev1alpha1.PodGangMap{Spec: grovecorev1alpha1.PodGangMapSpec{Entries: []grovecorev1alpha1.PodGangEntry{
			{Epoch: anchorEpoch, PodCliqueSetGenerationHash: testHash, Role: grovecorev1alpha1.PodGangEntryRoleAnchor},
			{Epoch: tailEpoch, PodCliqueSetGenerationHash: testHash, Role: grovecorev1alpha1.PodGangEntryRoleTail, DependsOn: []string{anchorEpoch}},
		}}}

		entries, err := buildEntriesFromPCLQAndPCSGStatuses(pcs, pclqs, pcsgs, existingPGM, 0, clk)
		require.NoError(t, err)
		byEpoch := componentutils.IndexPodGangEntriesByEpoch(entries)
		require.Len(t, byEpoch, 2)
		assert.Equal(t, int32(2), byEpoch[anchorEpoch].PodCliques["frontend"])
		assert.Equal(t, []int32{0}, byEpoch[anchorEpoch].PCSGReplicaIndices["prefill"])
		assert.Equal(t, []int32{1, 2}, byEpoch[tailEpoch].PCSGReplicaIndices["prefill"])
	})

	t.Run("preserves epoch, DependsOn and Role of existing entries", func(t *testing.T) {
		pcs := newPCS()
		pclqs := []grovecorev1alpha1.PodClique{newFrontendPCLQ([]grovecorev1alpha1.PodGangPodCountAssignment{{Epoch: anchorEpoch, PodCount: 2}})}
		pcsgs := []grovecorev1alpha1.PodCliqueScalingGroup{newPrefillPCSG([]grovecorev1alpha1.PodGangReplicaAssignment{
			{Epoch: anchorEpoch, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, ReplicaIndices: []int32{0}},
		})}
		existingPGM := &grovecorev1alpha1.PodGangMap{Spec: grovecorev1alpha1.PodGangMapSpec{Entries: []grovecorev1alpha1.PodGangEntry{
			{Epoch: anchorEpoch, PodCliqueSetGenerationHash: testHash, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, AnchorIndex: 0, DependsOn: []string{"dep-epoch"}},
		}}}

		entries, err := buildEntriesFromPCLQAndPCSGStatuses(pcs, pclqs, pcsgs, existingPGM, 0, clk)
		require.NoError(t, err)
		anchor := componentutils.IndexPodGangEntriesByEpoch(entries)[anchorEpoch]
		assert.Equal(t, grovecorev1alpha1.PodGangEntryRoleAnchor, anchor.Role)
		assert.Equal(t, anchorEpoch, anchor.Epoch)
		assert.Equal(t, []string{"dep-epoch"}, anchor.DependsOn)
	})

	t.Run("net-new scale-out gets fresh epoch, nil DependsOn, ScaleOut role", func(t *testing.T) {
		pcs := newPCS()
		pclqs := []grovecorev1alpha1.PodClique{newFrontendPCLQ([]grovecorev1alpha1.PodGangPodCountAssignment{{Epoch: anchorEpoch, PodCount: 2}})}
		pcsgs := []grovecorev1alpha1.PodCliqueScalingGroup{newPrefillPCSG([]grovecorev1alpha1.PodGangReplicaAssignment{
			{Epoch: anchorEpoch, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, ReplicaIndices: []int32{0}},
			{Role: grovecorev1alpha1.PodGangEntryRoleScaleOut, ReplicaIndices: []int32{5}}, // empty epoch
		})}
		existingPGM := &grovecorev1alpha1.PodGangMap{Spec: grovecorev1alpha1.PodGangMapSpec{Entries: []grovecorev1alpha1.PodGangEntry{
			{Epoch: anchorEpoch, PodCliqueSetGenerationHash: testHash, Role: grovecorev1alpha1.PodGangEntryRoleAnchor},
		}}}

		entries, err := buildEntriesFromPCLQAndPCSGStatuses(pcs, pclqs, pcsgs, existingPGM, 0, clk)
		require.NoError(t, err)
		// fresh epoch is the fake clock's unix-nano (5000).
		scaleOut := componentutils.IndexPodGangEntriesByEpoch(entries)["5000"]
		assert.Equal(t, grovecorev1alpha1.PodGangEntryRoleScaleOut, scaleOut.Role)
		assert.Nil(t, scaleOut.DependsOn)
		assert.Equal(t, []int32{5}, scaleOut.PCSGReplicaIndices["prefill"])
	})

	t.Run("scale-out appends to existing ScaleOut entry, keeping its epoch", func(t *testing.T) {
		pcs := newPCS()
		pclqs := []grovecorev1alpha1.PodClique{newFrontendPCLQ([]grovecorev1alpha1.PodGangPodCountAssignment{{Epoch: anchorEpoch, PodCount: 2}})}
		pcsgs := []grovecorev1alpha1.PodCliqueScalingGroup{newPrefillPCSG([]grovecorev1alpha1.PodGangReplicaAssignment{
			{Epoch: anchorEpoch, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, ReplicaIndices: []int32{0}},
			{Role: grovecorev1alpha1.PodGangEntryRoleScaleOut, ReplicaIndices: []int32{5, 6}}, // empty epoch
		})}
		existingPGM := &grovecorev1alpha1.PodGangMap{Spec: grovecorev1alpha1.PodGangMapSpec{Entries: []grovecorev1alpha1.PodGangEntry{
			{Epoch: anchorEpoch, PodCliqueSetGenerationHash: testHash, Role: grovecorev1alpha1.PodGangEntryRoleAnchor},
			{Epoch: scaleOutEpoch, PodCliqueSetGenerationHash: testHash, Role: grovecorev1alpha1.PodGangEntryRoleScaleOut},
		}}}

		entries, err := buildEntriesFromPCLQAndPCSGStatuses(pcs, pclqs, pcsgs, existingPGM, 0, clk)
		require.NoError(t, err)
		byEpoch := componentutils.IndexPodGangEntriesByEpoch(entries)
		// reused the existing ScaleOut epoch, not a fresh clock epoch.
		require.Contains(t, byEpoch, scaleOutEpoch)
		assert.NotContains(t, byEpoch, "5000")
		assert.Equal(t, []int32{5, 6}, byEpoch[scaleOutEpoch].PCSGReplicaIndices["prefill"])
	})

	t.Run("scale-in shrinks a PCSG's replica indices at an existing epoch", func(t *testing.T) {
		pcs := newPCS()
		pclqs := []grovecorev1alpha1.PodClique{newFrontendPCLQ([]grovecorev1alpha1.PodGangPodCountAssignment{{Epoch: anchorEpoch, PodCount: 2}})}
		pcsgs := []grovecorev1alpha1.PodCliqueScalingGroup{newPrefillPCSG([]grovecorev1alpha1.PodGangReplicaAssignment{
			{Epoch: anchorEpoch, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, ReplicaIndices: []int32{0}},
			{Epoch: tailEpoch, Role: grovecorev1alpha1.PodGangEntryRoleTail, ReplicaIndices: []int32{1, 2}}, // was 1,2,3
		})}
		existingPGM := &grovecorev1alpha1.PodGangMap{Spec: grovecorev1alpha1.PodGangMapSpec{Entries: []grovecorev1alpha1.PodGangEntry{
			{Epoch: anchorEpoch, PodCliqueSetGenerationHash: testHash, Role: grovecorev1alpha1.PodGangEntryRoleAnchor},
			{Epoch: tailEpoch, PodCliqueSetGenerationHash: testHash, Role: grovecorev1alpha1.PodGangEntryRoleTail, DependsOn: []string{anchorEpoch}, PCSGReplicaIndices: map[string][]int32{"prefill": {1, 2, 3}}},
		}}}

		entries, err := buildEntriesFromPCLQAndPCSGStatuses(pcs, pclqs, pcsgs, existingPGM, 0, clk)
		require.NoError(t, err)
		assert.Equal(t, []int32{1, 2}, componentutils.IndexPodGangEntriesByEpoch(entries)[tailEpoch].PCSGReplicaIndices["prefill"])
	})

	t.Run("PCSG status epoch absent from PodGangMap is a hard error", func(t *testing.T) {
		pcs := newPCS()
		pclqs := []grovecorev1alpha1.PodClique{newFrontendPCLQ([]grovecorev1alpha1.PodGangPodCountAssignment{{Epoch: anchorEpoch, PodCount: 2}})}
		pcsgs := []grovecorev1alpha1.PodCliqueScalingGroup{newPrefillPCSG([]grovecorev1alpha1.PodGangReplicaAssignment{
			{Epoch: "unknown-epoch", Role: grovecorev1alpha1.PodGangEntryRoleTail, ReplicaIndices: []int32{1}},
		})}
		existingPGM := &grovecorev1alpha1.PodGangMap{Spec: grovecorev1alpha1.PodGangMapSpec{Entries: []grovecorev1alpha1.PodGangEntry{
			{Epoch: anchorEpoch, PodCliqueSetGenerationHash: testHash, Role: grovecorev1alpha1.PodGangEntryRoleAnchor},
		}}}

		_, err := buildEntriesFromPCLQAndPCSGStatuses(pcs, pclqs, pcsgs, existingPGM, 0, clk)
		assertErrorCode(t, err, errCodeStatusEpochNotInPodGangMap)
	})

	t.Run("unpublished PCLQ mapping triggers requeue error", func(t *testing.T) {
		pcs := newPCS()
		pclqs := []grovecorev1alpha1.PodClique{newFrontendPCLQ(nil)} // empty mapping
		pcsgs := []grovecorev1alpha1.PodCliqueScalingGroup{newPrefillPCSG([]grovecorev1alpha1.PodGangReplicaAssignment{
			{Epoch: anchorEpoch, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, ReplicaIndices: []int32{0}},
		})}

		_, err := buildEntriesFromPCLQAndPCSGStatuses(pcs, pclqs, pcsgs, &grovecorev1alpha1.PodGangMap{}, 0, clk)
		assertErrorCode(t, err, groveerr.ErrCodeContinueReconcileAndRequeue)
	})

	t.Run("fewer observed PCSGs than spec triggers requeue error", func(t *testing.T) {
		pcs := newPCS()
		pclqs := []grovecorev1alpha1.PodClique{newFrontendPCLQ([]grovecorev1alpha1.PodGangPodCountAssignment{{Epoch: anchorEpoch, PodCount: 2}})}
		var pcsgs []grovecorev1alpha1.PodCliqueScalingGroup // spec declares 1 PCSG, none observed

		_, err := buildEntriesFromPCLQAndPCSGStatuses(pcs, pclqs, pcsgs, &grovecorev1alpha1.PodGangMap{}, 0, clk)
		assertErrorCode(t, err, groveerr.ErrCodeContinueReconcileAndRequeue)
	})

	t.Run("drops entries that end up empty", func(t *testing.T) {
		pcs := newPCS()
		pclqs := []grovecorev1alpha1.PodClique{newFrontendPCLQ([]grovecorev1alpha1.PodGangPodCountAssignment{{Epoch: anchorEpoch, PodCount: 2}})}
		pcsgs := []grovecorev1alpha1.PodCliqueScalingGroup{newPrefillPCSG([]grovecorev1alpha1.PodGangReplicaAssignment{
			{Epoch: anchorEpoch, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, ReplicaIndices: []int32{0}},
			{Epoch: tailEpoch, Role: grovecorev1alpha1.PodGangEntryRoleTail, ReplicaIndices: []int32{}}, // drained empty
		})}
		existingPGM := &grovecorev1alpha1.PodGangMap{Spec: grovecorev1alpha1.PodGangMapSpec{Entries: []grovecorev1alpha1.PodGangEntry{
			{Epoch: anchorEpoch, PodCliqueSetGenerationHash: testHash, Role: grovecorev1alpha1.PodGangEntryRoleAnchor},
			{Epoch: tailEpoch, PodCliqueSetGenerationHash: testHash, Role: grovecorev1alpha1.PodGangEntryRoleTail, DependsOn: []string{anchorEpoch}},
		}}}

		entries, err := buildEntriesFromPCLQAndPCSGStatuses(pcs, pclqs, pcsgs, existingPGM, 0, clk)
		require.NoError(t, err)
		_, hasTail := componentutils.IndexPodGangEntriesByEpoch(entries)[tailEpoch]
		assert.False(t, hasTail)
	})

	t.Run("unparseable PCLQ FQN yields extract-name error", func(t *testing.T) {
		pcs := newPCS()
		badPCLQ := grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{Name: "not-a-valid-fqn", Namespace: testNamespace},
			Status:     grovecorev1alpha1.PodCliqueStatus{PodGangMapping: []grovecorev1alpha1.PodGangPodCountAssignment{{Epoch: anchorEpoch, PodCount: 1}}},
		}
		pcsgs := []grovecorev1alpha1.PodCliqueScalingGroup{newPrefillPCSG([]grovecorev1alpha1.PodGangReplicaAssignment{
			{Epoch: anchorEpoch, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, ReplicaIndices: []int32{0}},
		})}
		existingPGM := &grovecorev1alpha1.PodGangMap{Spec: grovecorev1alpha1.PodGangMapSpec{Entries: []grovecorev1alpha1.PodGangEntry{
			{Epoch: anchorEpoch, PodCliqueSetGenerationHash: testHash, Role: grovecorev1alpha1.PodGangEntryRoleAnchor},
		}}}

		_, err := buildEntriesFromPCLQAndPCSGStatuses(pcs, []grovecorev1alpha1.PodClique{badPCLQ}, pcsgs, existingPGM, 0, clk)
		assertErrorCode(t, err, errCodeExtractPodCliqueName)
	})
}

func TestCanRebuildPGMFromStatuses(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
		WithStandaloneCliqueReplicas("frontend", 5).
		WithScalingGroupConfig("prefill", []string{"pworker"}, 3, 1).
		WithStatusCurrentGenerationHash(ptr.To(testHash)).
		Build()

	publishedPCLQ := *testutils.NewPodCliqueBuilder(testPCSName, "uid", "frontend", testNamespace, 0).
		WithPodGangMapping([]grovecorev1alpha1.PodGangPodCountAssignment{{Epoch: "e0", PodCount: 2}}).Build()
	publishedPCSG := *testutils.NewPodCliqueScalingGroupBuilder("my-pcs-0-prefill", testNamespace, testPCSName, 0).
		WithPodGangMapping([]grovecorev1alpha1.PodGangReplicaAssignment{{Epoch: "e0", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, ReplicaIndices: []int32{0}}}).Build()
	unpublishedPCLQ := *testutils.NewPodCliqueBuilder(testPCSName, "uid", "frontend", testNamespace, 0).Build()
	unpublishedPCSG := *testutils.NewPodCliqueScalingGroupBuilder("my-pcs-0-prefill", testNamespace, testPCSName, 0).Build()

	tests := []struct {
		name  string
		pclqs []grovecorev1alpha1.PodClique
		pcsgs []grovecorev1alpha1.PodCliqueScalingGroup
		want  bool
	}{
		{
			name:  "true when all owners observed and published",
			pclqs: []grovecorev1alpha1.PodClique{publishedPCLQ},
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{publishedPCSG},
			want:  true,
		},
		{
			name:  "false when fewer standalone PCLQs than spec",
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{publishedPCSG},
			want:  false,
		},
		{
			name:  "false when fewer PCSGs than configs",
			pclqs: []grovecorev1alpha1.PodClique{publishedPCLQ},
			want:  false,
		},
		{
			name:  "false when a PCLQ mapping is empty",
			pclqs: []grovecorev1alpha1.PodClique{unpublishedPCLQ},
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{publishedPCSG},
			want:  false,
		},
		{
			name:  "false when a PCSG mapping is empty",
			pclqs: []grovecorev1alpha1.PodClique{publishedPCLQ},
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{unpublishedPCSG},
			want:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, canRebuildPGMFromStatuses(pcs, tc.pclqs, tc.pcsgs))
		})
	}
}

func TestRemoveEmptyEntries(t *testing.T) {
	t.Run("drops all-empty entry, keeps others", func(t *testing.T) {
		entries := []grovecorev1alpha1.PodGangEntry{
			{Epoch: "empty", PodCliques: map[string]int32{"a": 0}, PCSGReplicaIndices: map[string][]int32{"p": {}}},
			{Epoch: "has-pods", PodCliques: map[string]int32{"a": 1}},
			{Epoch: "has-indices", PCSGReplicaIndices: map[string][]int32{"p": {0}}},
		}
		result := removeEmptyEntries(entries)
		byEpoch := componentutils.IndexPodGangEntriesByEpoch(result)
		require.Len(t, byEpoch, 2)
		_, hasEmpty := byEpoch["empty"]
		assert.False(t, hasEmpty)
	})

	t.Run("empty input yields empty output", func(t *testing.T) {
		assert.Empty(t, removeEmptyEntries(nil))
	})
}

func TestReconstructEntriesFromExistingPodGangs(t *testing.T) {
	clk := clocktesting.NewFakeClock(time.Unix(0, 2000))
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
		WithStandaloneCliqueReplicas("frontend", 2).
		WithScalingGroupConfig("prefill", []string{"pworker"}, 2, 1).
		WithStatusCurrentGenerationHash(ptr.To(testHash)).
		Build()

	// A base PodGang carries the standalone clique group; a scaled PodGang carries the
	// base-podgang label and a PCSG-owned clique group.
	bpg := testutils.NewPodGangBuilder("my-pcs-0", testNamespace).
		WithPodGroup("my-pcs-0-frontend", 2).Build()
	// give the standalone group two pod references so the reconstructed count is 2.
	bpg.Spec.PodGroups[0].PodReferences = []groveschedulerv1alpha1.NamespacedName{
		{Namespace: testNamespace, Name: "p0"}, {Namespace: testNamespace, Name: "p1"},
	}
	spg := testutils.NewPodGangBuilder("my-pcs-0-prefill-1", testNamespace).
		WithLabel(apicommon.LabelBasePodGang, "my-pcs-0").
		WithPodGroup("my-pcs-0-prefill-1-pworker", 1).Build()

	t.Run("BPG becomes mpg E0, SPG depends on E0 at E1", func(t *testing.T) {
		entries, err := reconstructEntriesFromExistingPodGangs(pcs, []groveschedulerv1alpha1.PodGang{*bpg, *spg}, 0, clk)
		require.NoError(t, err)

		base := anchorEntry(t, entries)
		assert.Nil(t, base.DependsOn)
		assert.Equal(t, int32(2), base.PodCliques["frontend"])

		tails := tpgEntries(entries)
		require.Len(t, tails, 1)
		scaled := tails[0]
		assert.Equal(t, grovecorev1alpha1.PodGangEntryRoleTail, scaled.Role)
		require.Len(t, scaled.DependsOn, 1)
		assert.Equal(t, base.Epoch, scaled.DependsOn[0])
		assert.Equal(t, []int32{1}, scaled.PCSGReplicaIndices["prefill"])
		// E1 > E0.
		assert.Greater(t, scaled.Epoch, base.Epoch)
	})

	t.Run("BPG only yields a single mpg", func(t *testing.T) {
		entries, err := reconstructEntriesFromExistingPodGangs(pcs, []groveschedulerv1alpha1.PodGang{*bpg}, 0, clk)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, grovecorev1alpha1.PodGangEntryRoleAnchor, entries[0].Role)
	})

	t.Run("unparseable PodGroup name yields reconstruction error", func(t *testing.T) {
		bad := testutils.NewPodGangBuilder("my-pcs-0", testNamespace).
			WithPodGroup("my-pcs-0-unknownclique", 1).Build()
		_, err := reconstructEntriesFromExistingPodGangs(pcs, []groveschedulerv1alpha1.PodGang{*bad}, 0, clk)
		assertErrorCode(t, err, errCodeReconstructPodGangMapEntry)
	})
}

func TestBuildEntryFromPodGang(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
		WithStandaloneCliqueReplicas("frontend", 2).
		WithScalingGroupConfig("prefill", []string{"pworker"}, 3, 1).
		WithStatusCurrentGenerationHash(ptr.To(testHash)).
		Build()

	t.Run("standalone group contributes pod count, PCSG group contributes replica index", func(t *testing.T) {
		pg := testutils.NewPodGangBuilder("my-pcs-0", testNamespace).
			WithPodGroup("my-pcs-0-frontend", 2).
			WithPodGroup("my-pcs-0-prefill-0-pworker", 1).Build()
		pg.Spec.PodGroups[0].PodReferences = []groveschedulerv1alpha1.NamespacedName{
			{Namespace: testNamespace, Name: "p0"}, {Namespace: testNamespace, Name: "p1"},
		}

		entry, err := buildEntryFromPodGang(pcs, 0, testHash, *pg)
		require.NoError(t, err)
		assert.Equal(t, int32(2), entry.PodCliques["frontend"])
		assert.Equal(t, []int32{0}, entry.PCSGReplicaIndices["prefill"])
	})

	t.Run("same PCSG replica across multiple cliques de-duplicates", func(t *testing.T) {
		pcsMulti := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithScalingGroupConfig("prefill", []string{"pworker", "leader"}, 3, 1).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()
		pg := testutils.NewPodGangBuilder("my-pcs-0-prefill-0", testNamespace).
			WithPodGroup("my-pcs-0-prefill-0-pworker", 1).
			WithPodGroup("my-pcs-0-prefill-0-leader", 1).Build()

		entry, err := buildEntryFromPodGang(pcsMulti, 0, testHash, *pg)
		require.NoError(t, err)
		assert.Equal(t, []int32{0}, entry.PCSGReplicaIndices["prefill"])
	})

	t.Run("unknown clique template yields error", func(t *testing.T) {
		pg := testutils.NewPodGangBuilder("my-pcs-0", testNamespace).
			WithPodGroup("my-pcs-0-nope", 1).Build()
		_, err := buildEntryFromPodGang(pcs, 0, testHash, *pg)
		require.Error(t, err)
	})
}

func TestExtractCliqueName(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
		WithStandaloneCliqueReplicas("frontend", 2).
		WithStandaloneCliqueReplicas("backend", 2).
		Build()
	tests := []struct {
		name    string
		fqn     string
		want    string
		wantErr bool
	}{
		{name: "matches trailing template segment", fqn: "my-pcs-0-frontend", want: "frontend"},
		{name: "no matching template yields error", fqn: "my-pcs-0-unknown", wantErr: true},
		// len(pclqFQN) must exceed len(suffix): "frontend" is not longer than "-frontend".
		{name: "FQN equal to bare suffix does not match", fqn: "frontend", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, err := extractCliqueName(pcs, tc.fqn)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, name)
		})
	}
}

func TestExtractPCSGReplicaIndexFromPCLQFQN(t *testing.T) {
	tests := []struct {
		name    string
		fqn     string
		want    int32
		wantErr bool
	}{
		{name: "well-formed FQN parses index", fqn: "my-pcs-0-prefill-2-pworker", want: 2},
		{name: "multi-digit index", fqn: "my-pcs-0-prefill-13-pworker", want: 13},
		{name: "wrong suffix yields error", fqn: "my-pcs-0-prefill-2-other", wantErr: true},
		{name: "non-integer middle yields error", fqn: "my-pcs-0-prefill-x-pworker", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idx, err := extractPCSGReplicaIndexFromPCLQFQN(tc.fqn, testPCSName, 0, "prefill", "pworker")
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, idx)
		})
	}
}

// assertErrorCode asserts that err unwraps to a GroveError carrying the expected code.
func assertErrorCode(t *testing.T, err error, code grovecorev1alpha1.ErrorCode) {
	t.Helper()
	require.Error(t, err)
	var groveErr *groveerr.GroveError
	require.True(t, errors.As(err, &groveErr), "error is not a GroveError: %v", err)
	assert.Equal(t, code, groveErr.Code)
}

// anchorEntry returns the single entry with the Anchor role, failing if not exactly one.
func anchorEntry(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) grovecorev1alpha1.PodGangEntry {
	t.Helper()
	var anchorPGs []grovecorev1alpha1.PodGangEntry
	for _, e := range entries {
		if e.Role == grovecorev1alpha1.PodGangEntryRoleAnchor {
			anchorPGs = append(anchorPGs, e)
		}
	}
	require.Len(t, anchorPGs, 1, "expected exactly one mpg entry")
	return anchorPGs[0]
}

// tpgEntries returns the entries with the Tail role.
func tpgEntries(entries []grovecorev1alpha1.PodGangEntry) []grovecorev1alpha1.PodGangEntry {
	var out []grovecorev1alpha1.PodGangEntry
	for _, e := range entries {
		if e.Role != grovecorev1alpha1.PodGangEntryRoleAnchor {
			out = append(out, e)
		}
	}
	return out
}

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

package podgangmap

import (
	"os"
	"strconv"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"
)

const (
	testNamespace = "default"
	testPCSName   = "pcs"
	testGenHash   = "hash1"
	testPCSGName  = "sg"
)

func TestBuildBootstrapEntries(t *testing.T) {
	tests := []struct {
		name             string
		pcs              *grovecorev1alpha1.PodCliqueSet
		existingPodGangs []groveschedulerv1alpha1.PodGang
		expectedRoles    []grovecorev1alpha1.PodGangEntryRole
		assertEntries    func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry)
	}{
		{
			name: "standalone clique only, no scaling group",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithStandaloneCliqueReplicas("clq-a", 3).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).
				Build(),
			expectedRoles: []grovecorev1alpha1.PodGangEntryRole{grovecorev1alpha1.PodGangEntryRoleAnchor},
			assertEntries: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				assert.Equal(t, int32(3), anchor.PodCliques["clq-a"])
				assert.Empty(t, anchor.PCSGReplicaIndices)
				assert.Nil(t, anchor.DependsOn)
			},
		},
		{
			name: "scaling group only, no standalone clique, replicas equal minAvailable",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithScalingGroupConfig(testPCSGName, []string{"c"}, 2, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).
				Build(),
			expectedRoles: []grovecorev1alpha1.PodGangEntryRole{
				grovecorev1alpha1.PodGangEntryRoleAnchor,
				grovecorev1alpha1.PodGangEntryRoleScaleOut,
			},
			assertEntries: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				assert.Empty(t, anchor.PodCliques)
				assert.Equal(t, []int32{0, 1}, anchor.PCSGReplicaIndices[testPCSGName])
				scaleOut := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleScaleOut)
				assert.Empty(t, scaleOut.PCSGReplicaIndices[testPCSGName])
				assert.Equal(t, []string{anchor.Epoch}, scaleOut.DependsOn)
			},
		},
		{
			name: "scaling group only, replicas above minAvailable yields tail",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithScalingGroupConfig(testPCSGName, []string{"c"}, 4, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).
				Build(),
			expectedRoles: []grovecorev1alpha1.PodGangEntryRole{
				grovecorev1alpha1.PodGangEntryRoleAnchor,
				grovecorev1alpha1.PodGangEntryRoleTail,
				grovecorev1alpha1.PodGangEntryRoleScaleOut,
			},
			assertEntries: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				assert.Empty(t, anchor.PodCliques)
				assert.Equal(t, []int32{0, 1}, anchor.PCSGReplicaIndices[testPCSGName])
				tail := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleTail)
				assert.Equal(t, []int32{2, 3}, tail.PCSGReplicaIndices[testPCSGName])
				assert.Equal(t, []string{anchor.Epoch}, tail.DependsOn)
			},
		},
		{
			name: "standalone clique and scaling group together",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithStandaloneCliqueReplicas("clq-a", 3).
				WithScalingGroupConfig(testPCSGName, []string{"c"}, 4, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).
				Build(),
			expectedRoles: []grovecorev1alpha1.PodGangEntryRole{
				grovecorev1alpha1.PodGangEntryRoleAnchor,
				grovecorev1alpha1.PodGangEntryRoleTail,
				grovecorev1alpha1.PodGangEntryRoleScaleOut,
			},
			assertEntries: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				assert.Equal(t, int32(3), anchor.PodCliques["clq-a"])
				assert.Equal(t, []int32{0, 1}, anchor.PCSGReplicaIndices[testPCSGName])
				tail := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleTail)
				assert.Equal(t, []int32{2, 3}, tail.PCSGReplicaIndices[testPCSGName])
			},
		},
		{
			name: "reuses anchor epoch from an existing anchor PodGang",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithStandaloneCliqueReplicas("clq-a", 3).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).
				Build(),
			existingPodGangs: []groveschedulerv1alpha1.PodGang{
				*podGangWithEpochRole("pg-anchor", "500", grovecorev1alpha1.PodGangEntryRoleAnchor),
			},
			expectedRoles: []grovecorev1alpha1.PodGangEntryRole{grovecorev1alpha1.PodGangEntryRoleAnchor},
			assertEntries: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				assert.Equal(t, "500", anchor.Epoch)
			},
		},
		{
			name: "reuses anchor and tail epochs from existing PodGangs",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithScalingGroupConfig(testPCSGName, []string{"c"}, 4, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).
				Build(),
			existingPodGangs: []groveschedulerv1alpha1.PodGang{
				*podGangWithEpochRole("pg-anchor", "500", grovecorev1alpha1.PodGangEntryRoleAnchor),
				*podGangWithEpochRole("pg-tail", "600", grovecorev1alpha1.PodGangEntryRoleTail),
			},
			expectedRoles: []grovecorev1alpha1.PodGangEntryRole{
				grovecorev1alpha1.PodGangEntryRoleAnchor,
				grovecorev1alpha1.PodGangEntryRoleTail,
				grovecorev1alpha1.PodGangEntryRoleScaleOut,
			},
			assertEntries: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				assert.Equal(t, "500", anchor.Epoch)
				tail := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleTail)
				assert.Equal(t, "600", tail.Epoch)
			},
		},
		{
			name: "reuses anchor epoch and assigns tail epoch when no tail PodGang exists",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithScalingGroupConfig(testPCSGName, []string{"c"}, 4, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).
				Build(),
			existingPodGangs: []groveschedulerv1alpha1.PodGang{
				*podGangWithEpochRole("pg-anchor", "500", grovecorev1alpha1.PodGangEntryRoleAnchor),
			},
			expectedRoles: []grovecorev1alpha1.PodGangEntryRole{
				grovecorev1alpha1.PodGangEntryRoleAnchor,
				grovecorev1alpha1.PodGangEntryRoleTail,
				grovecorev1alpha1.PodGangEntryRoleScaleOut,
			},
			assertEntries: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				assert.Equal(t, "500", anchor.Epoch)
				// The tail epoch is assigned after the anchor epoch so anchor < tail holds.
				tail := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleTail)
				assert.Equal(t, "501", tail.Epoch)
				assert.Equal(t, []string{"500"}, tail.DependsOn)
			},
		},
		{
			name: "assigns all epochs when no anchor PodGang exists even if a tail PodGang carries one",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithScalingGroupConfig(testPCSGName, []string{"c"}, 4, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).
				Build(),
			existingPodGangs: []groveschedulerv1alpha1.PodGang{
				*podGangWithEpochRole("pg-tail", "600", grovecorev1alpha1.PodGangEntryRoleTail),
			},
			expectedRoles: []grovecorev1alpha1.PodGangEntryRole{
				grovecorev1alpha1.PodGangEntryRoleAnchor,
				grovecorev1alpha1.PodGangEntryRoleTail,
				grovecorev1alpha1.PodGangEntryRoleScaleOut,
			},
			assertEntries: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				// No anchor PodGang to reuse, so all epochs are assigned from the clock and the orphan
				// tail epoch is ignored. anchor < tail < scaleOut holds.
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				tail := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleTail)
				assert.NotEqual(t, "600", tail.Epoch)
				assert.Less(t, anchor.Epoch, tail.Epoch)
			},
		},
		{
			name: "assigns epochs when existing PodGangs carry no epoch label (legacy)",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithStandaloneCliqueReplicas("clq-a", 3).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).
				Build(),
			existingPodGangs: []groveschedulerv1alpha1.PodGang{
				*testutils.NewPodGangBuilder("pg-legacy", testNamespace).Build(),
			},
			expectedRoles: []grovecorev1alpha1.PodGangEntryRole{grovecorev1alpha1.PodGangEntryRoleAnchor},
			assertEntries: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				// A clock at 1000ns produces epoch "1000". A legacy PodGang carries no epoch to reuse.
				assert.Equal(t, "1000", anchor.Epoch)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := clocktesting.NewFakeClock(time.Unix(0, 1000))
			actual := buildBootstrapEntries(tt.pcs, clk, tt.existingPodGangs)
			assert.Equal(t, tt.expectedRoles, testutils.RolesOf(actual))
			tt.assertEntries(t, actual)
		})
	}
}

func TestEpochByRoleFromPodGangs(t *testing.T) {
	tests := []struct {
		name     string
		podGangs []groveschedulerv1alpha1.PodGang
		expected map[grovecorev1alpha1.PodGangEntryRole]int64
	}{
		{
			name:     "no PodGangs yields an empty map",
			podGangs: nil,
			expected: map[grovecorev1alpha1.PodGangEntryRole]int64{},
		},
		{
			name: "reads epoch per role",
			podGangs: []groveschedulerv1alpha1.PodGang{
				*podGangWithEpochRole("a", "500", grovecorev1alpha1.PodGangEntryRoleAnchor),
				*podGangWithEpochRole("t", "600", grovecorev1alpha1.PodGangEntryRoleTail),
			},
			expected: map[grovecorev1alpha1.PodGangEntryRole]int64{
				grovecorev1alpha1.PodGangEntryRoleAnchor: 500,
				grovecorev1alpha1.PodGangEntryRoleTail:   600,
			},
		},
		{
			name: "first PodGang of a role wins",
			podGangs: []groveschedulerv1alpha1.PodGang{
				*podGangWithEpochRole("t1", "600", grovecorev1alpha1.PodGangEntryRoleTail),
				*podGangWithEpochRole("t2", "700", grovecorev1alpha1.PodGangEntryRoleTail),
			},
			expected: map[grovecorev1alpha1.PodGangEntryRole]int64{
				grovecorev1alpha1.PodGangEntryRoleTail: 600,
			},
		},
		{
			name: "skips PodGangs missing the epoch or role label",
			podGangs: []groveschedulerv1alpha1.PodGang{
				*testutils.NewPodGangBuilder("no-labels", testNamespace).Build(),
				*testutils.NewPodGangBuilder("epoch-only", testNamespace).
					WithLabels(map[string]string{apicommon.LabelEpoch: "500"}).Build(),
			},
			expected: map[grovecorev1alpha1.PodGangEntryRole]int64{},
		},
		{
			name: "skips a PodGang whose epoch label is not an integer",
			podGangs: []groveschedulerv1alpha1.PodGang{
				*podGangWithEpochRole("bad", "not-a-number", grovecorev1alpha1.PodGangEntryRoleAnchor),
			},
			expected: map[grovecorev1alpha1.PodGangEntryRole]int64{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := epochByRoleFromPodGangs(tt.podGangs)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestSyncEntries(t *testing.T) {
	epochSeed := time.Unix(0, 5000).UnixNano()

	tests := []struct {
		name            string
		pcs             *grovecorev1alpha1.PodCliqueSet
		existingEntries []grovecorev1alpha1.PodGangEntry
		standalonePCLQs []grovecorev1alpha1.PodClique
		pcsgs           []grovecorev1alpha1.PodCliqueScalingGroup
		assertResult    func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry)
	}{
		{
			name: "no change keeps entries as is",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithStandaloneCliqueReplicas("clq-a", 3).
				WithScalingGroupConfig(testPCSGName, []string{"c"}, 2, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).Build(),
			existingEntries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(map[string]int32{"clq-a": 3}, map[string][]int32{testPCSGName: {0, 1}}),
				scaleOutEntry(nil),
			},
			standalonePCLQs: []grovecorev1alpha1.PodClique{standalonePCLQ("clq-a", 3)},
			pcsgs:           []grovecorev1alpha1.PodCliqueScalingGroup{pcsg(2)},
			assertResult: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				assert.Equal(t, []int32{0, 1}, anchor.PCSGReplicaIndices[testPCSGName])
				assert.Equal(t, int32(3), anchor.PodCliques["clq-a"])
			},
		},
		{
			name: "scale out appends new index to scaleout entry",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithScalingGroupConfig(testPCSGName, []string{"c"}, 2, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).Build(),
			existingEntries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(nil, map[string][]int32{testPCSGName: {0, 1}}),
				scaleOutEntry(nil),
			},
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{pcsg(3)},
			assertResult: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				scaleOut := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleScaleOut)
				assert.Equal(t, []int32{2}, scaleOut.PCSGReplicaIndices[testPCSGName])
			},
		},
		{
			name: "scale in drains only the scaleout entry",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithScalingGroupConfig(testPCSGName, []string{"c"}, 4, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).Build(),
			existingEntries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(nil, map[string][]int32{testPCSGName: {0, 1}}),
				tailEntry(map[string][]int32{testPCSGName: {2, 3}}),
				scaleOutEntry(map[string][]int32{testPCSGName: {4, 5}}),
			},
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{pcsg(4)},
			assertResult: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				// live=4, current=6, drain 2: both come from the scaleout entry (highest indices first).
				assert.Empty(t, testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleScaleOut).PCSGReplicaIndices[testPCSGName])
				assert.Equal(t, []int32{2, 3}, testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleTail).PCSGReplicaIndices[testPCSGName])
				// The anchor is never drained; its MinAvailable indices are preserved.
				assert.Equal(t, []int32{0, 1}, testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor).PCSGReplicaIndices[testPCSGName])
			},
		},
		{
			name: "scale in drains tail when scaleout has no indices",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithScalingGroupConfig(testPCSGName, []string{"c"}, 4, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).Build(),
			existingEntries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(nil, map[string][]int32{testPCSGName: {0, 1}}),
				tailEntry(map[string][]int32{testPCSGName: {2, 3}}),
				scaleOutEntry(nil),
			},
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{pcsg(3)},
			assertResult: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				// live=3, current=4, drain 1: the scaleout entry has nothing, so the tail is drained.
				assert.Empty(t, testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleScaleOut).PCSGReplicaIndices[testPCSGName])
				assert.Equal(t, []int32{2}, testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleTail).PCSGReplicaIndices[testPCSGName])
				// The anchor is never drained; its MinAvailable indices are preserved.
				assert.Equal(t, []int32{0, 1}, testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor).PCSGReplicaIndices[testPCSGName])
			},
		},
		{
			name: "standalone pod count refreshed from live replicas",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithStandaloneCliqueReplicas("clq-a", 3).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).Build(),
			existingEntries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(map[string]int32{"clq-a": 3}, nil),
			},
			standalonePCLQs: []grovecorev1alpha1.PodClique{standalonePCLQ("clq-a", 5)},
			assertResult: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				assert.Equal(t, int32(5), anchor.PodCliques["clq-a"])
			},
		},
		{
			name: "standalone scale to zero removes member and retains base anchor",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithStandaloneCliqueReplicas("clq-a", 3).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).Build(),
			existingEntries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(map[string]int32{"clq-a": 3}, nil),
			},
			standalonePCLQs: []grovecorev1alpha1.PodClique{standalonePCLQ("clq-a", 0)},
			assertResult: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				require.Len(t, entries, 1)
				assert.Empty(t, entries[0].PodCliques)
				assert.Equal(t, int32(0), *entries[0].AnchorIndex)
			},
		},
		{
			name: "standalone wake repopulates empty base anchor with fresh epoch",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithStandaloneCliqueReplicas("clq-a", 3).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).Build(),
			existingEntries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(nil, nil), // retained empty base anchor, epoch "100"
			},
			standalonePCLQs: []grovecorev1alpha1.PodClique{standalonePCLQ("clq-a", 2)},
			assertResult: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				require.Equal(t, grovecorev1alpha1.PodGangEntryRoleAnchor, anchor.Role)
				assert.Equal(t, int32(2), anchor.PodCliques["clq-a"])
				assert.Equal(t, strconv.FormatInt(epochSeed, 10), anchor.Epoch, "woken base anchor must take a fresh epoch")
				assert.Equal(t, int32(0), *anchor.AnchorIndex, "base anchor keeps AnchorIndex 0")
			},
		},
		{
			name: "standalone wake does not expand a materialized base anchor",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithStandaloneCliqueReplicas("router", 1).
				WithStandaloneCliqueReplicas("decode", 0).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).Build(),
			existingEntries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(map[string]int32{"router": 1}, nil),
			},
			standalonePCLQs: []grovecorev1alpha1.PodClique{
				standalonePCLQ("router", 1),
				standalonePCLQ("decode", 2),
			},
			assertResult: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				base := anchorEntryByIndex(t, entries, 0)
				assert.Equal(t, map[string]int32{"router": 1}, base.PodCliques)
				assert.Equal(t, "100", base.Epoch)

				wakeAnchor := anchorEntryByIndex(t, entries, 1)
				assert.Equal(t, map[string]int32{"decode": 2}, wakeAnchor.PodCliques)
				assert.Equal(t, strconv.FormatInt(epochSeed, 10), wakeAnchor.Epoch)
				assert.Equal(t, []string{"100"}, wakeAnchor.DependsOn)
			},
		},
		{
			name: "standalone and PCSG wake share an empty base anchor",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithStandaloneCliqueReplicas("router", 0).
				WithScalingGroupConfig(testPCSGName, []string{"c"}, 0, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).Build(),
			existingEntries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(nil, nil),
				scaleOutEntry(nil),
			},
			standalonePCLQs: []grovecorev1alpha1.PodClique{standalonePCLQ("router", 1)},
			pcsgs:           []grovecorev1alpha1.PodCliqueScalingGroup{pcsg(4)},
			assertResult: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				base := anchorEntryByIndex(t, entries, 0)
				assert.Equal(t, int32(1), base.PodCliques["router"])
				assert.Equal(t, []int32{0, 1}, base.PCSGReplicaIndices[testPCSGName])
				assert.Equal(t, strconv.FormatInt(epochSeed, 10), base.Epoch)

				scaleOut := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleScaleOut)
				assert.Equal(t, []int32{2, 3}, scaleOut.PCSGReplicaIndices[testPCSGName])
				assert.Equal(t, []string{base.Epoch}, scaleOut.DependsOn, "base epoch refresh must rewrite dependencies")
				assertUniqueEpochs(t, entries)
			},
		},
		{
			name: "multiple PCSG wakes allocate unique anchor epochs",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithStandaloneCliqueReplicas("router", 1).
				WithScalingGroupConfig("sg-a", []string{"a"}, 0, 1).
				WithScalingGroupConfig("sg-b", []string{"b"}, 0, 1).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).Build(),
			existingEntries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(map[string]int32{"router": 1}, nil),
				scaleOutEntry(nil),
			},
			standalonePCLQs: []grovecorev1alpha1.PodClique{standalonePCLQ("router", 1)},
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{
				pcsgNamed("sg-a", 2, 1),
				pcsgNamed("sg-b", 2, 1),
			},
			assertResult: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				assert.Equal(t, "100", anchorEntryByIndex(t, entries, 0).Epoch)
				assert.Equal(t, strconv.FormatInt(epochSeed, 10), anchorEntryByIndex(t, entries, 1).Epoch)
				assert.Equal(t, strconv.FormatInt(epochSeed+1, 10), anchorEntryByIndex(t, entries, 2).Epoch)
				assertUniqueEpochs(t, entries)
			},
		},
		{
			name: "PCSG scale to zero drains scaleout tail and base anchor",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithScalingGroupConfig(testPCSGName, []string{"c"}, 4, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).Build(),
			existingEntries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(nil, map[string][]int32{testPCSGName: {0, 1}}),
				tailEntry(map[string][]int32{testPCSGName: {2, 3}}),
				scaleOutEntry(map[string][]int32{testPCSGName: {4, 5}}),
			},
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{pcsg(0)},
			assertResult: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				require.Len(t, entries, 2)
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				assert.Empty(t, anchor.PCSGReplicaIndices[testPCSGName])
				scaleOut := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleScaleOut)
				assert.Empty(t, scaleOut.PCSGReplicaIndices[testPCSGName])
			},
		},
		{
			// GREP-0677 PCSG wake: an idle PCSG becoming active places quorum indices in the
			// base anchor and remaining indices in scale-out.
			name: "pcsg wake distributes indices across anchor and scaleout",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithScalingGroupConfig(testPCSGName, []string{"c"}, 4, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).Build(),
			existingEntries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(nil, nil), // retained empty base anchor
				scaleOutEntry(nil),
			},
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{pcsg(4)},
			assertResult: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				require.Equal(t, grovecorev1alpha1.PodGangEntryRoleAnchor, anchor.Role)
				assert.Equal(t, []int32{0, 1}, anchor.PCSGReplicaIndices[testPCSGName], "minAvailable indices join the anchor")
				scaleOut := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleScaleOut)
				require.Equal(t, grovecorev1alpha1.PodGangEntryRoleScaleOut, scaleOut.Role)
				assert.Equal(t, []int32{2, 3}, scaleOut.PCSGReplicaIndices[testPCSGName], "above-minAvailable indices go to scale-out")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := reconcileEntries(tt.pcs, tt.existingEntries, tt.standalonePCLQs, tt.pcsgs, 0, epochSeed)
			require.NoError(t, err)
			tt.assertResult(t, actual)
		})
	}
}

func TestRewriteDependsOnEpoch(t *testing.T) {
	entries := []grovecorev1alpha1.PodGangEntry{
		testutils.NewPodGangEntryBuilder(testGenHash, "101").WithDependsOn("100", "90").Build(),
		testutils.NewPodGangEntryBuilder("old-hash", "50").WithDependsOn("100").Build(),
	}

	rewriteDependsOnEpoch(entries, testGenHash, "100", "500")

	assert.Equal(t, []string{"500", "90"}, entries[0].DependsOn)
	assert.Equal(t, []string{"100"}, entries[1].DependsOn, "old-generation dependency must remain unchanged")
}

func TestAppendScaleOutReplicaIndicesRequiresCurrentEntry(t *testing.T) {
	entries := []grovecorev1alpha1.PodGangEntry{scaleOutEntry(nil)}
	require.Error(t, appendScaleOutReplicaIndices(entries, "other-generation", testPCSGName, []int32{1}))
}

func TestReconcileEntriesRejectsInvalidEntryLayout(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
		WithScalingGroupConfig(testPCSGName, []string{"c"}, 0, 1).
		WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).
		Build()

	t.Run("duplicate current ScaleOut entries", func(t *testing.T) {
		entries := []grovecorev1alpha1.PodGangEntry{anchorEntry(nil, nil), scaleOutEntry(nil), scaleOutEntry(nil)}
		_, err := reconcileEntries(pcs, entries, nil, nil, 0, 5000)
		require.Error(t, err)
	})
}

func TestReconcileEntriesRejectsInvalidPCSGWake(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
		WithScalingGroupConfig(testPCSGName, []string{"c"}, 0, 1).
		WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).
		Build()
	entries := []grovecorev1alpha1.PodGangEntry{anchorEntry(nil, nil), scaleOutEntry(nil)}

	t.Run("nil live minAvailable", func(t *testing.T) {
		livePCSG := pcsgNamed(testPCSGName, 2, 1)
		livePCSG.Spec.MinAvailable = nil
		_, err := reconcileEntries(pcs, entries, nil, []grovecorev1alpha1.PodCliqueScalingGroup{livePCSG}, 0, 5000)
		require.Error(t, err)
	})

	t.Run("live minAvailable above replicas", func(t *testing.T) {
		livePCSG := pcsgNamed(testPCSGName, 1, 2)
		_, err := reconcileEntries(pcs, entries, nil, []grovecorev1alpha1.PodCliqueScalingGroup{livePCSG}, 0, 5000)
		require.Error(t, err)
	})
}

// podGangWithEpochRole builds a PodGang carrying the grove.io/epoch and grove.io/podgang-role labels.
func podGangWithEpochRole(name, epoch string, role grovecorev1alpha1.PodGangEntryRole) *groveschedulerv1alpha1.PodGang {
	return testutils.NewPodGangBuilder(name, testNamespace).
		WithLabels(map[string]string{
			apicommon.LabelEpoch:       epoch,
			apicommon.LabelPodGangRole: string(role),
		}).Build()
}

func anchorEntry(podCliques map[string]int32, pcsgIndices map[string][]int32) grovecorev1alpha1.PodGangEntry {
	return testutils.NewPodGangEntryBuilder(testGenHash, "100").
		WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
		WithAnchorIndex(0).
		WithPodCliques(podCliques).
		WithPCSGReplicaIndices(pcsgIndices).
		Build()
}

func tailEntry(pcsgIndices map[string][]int32) grovecorev1alpha1.PodGangEntry {
	return testutils.NewPodGangEntryBuilder(testGenHash, "101").
		WithRole(grovecorev1alpha1.PodGangEntryRoleTail).
		WithPCSGReplicaIndices(pcsgIndices).
		WithDependsOn("100").
		Build()
}

func scaleOutEntry(pcsgIndices map[string][]int32) grovecorev1alpha1.PodGangEntry {
	return testutils.NewPodGangEntryBuilder(testGenHash, "102").
		WithRole(grovecorev1alpha1.PodGangEntryRoleScaleOut).
		WithPCSGReplicaIndices(pcsgIndices).
		WithDependsOn("100").
		Build()
}

//nolint:unparam // cliqueName is kept parameterized for readability across scenarios.
func standalonePCLQ(cliqueName string, replicas int32) grovecorev1alpha1.PodClique {
	return grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name: apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: testPCSName, Replica: 0}, cliqueName),
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: replicas},
	}
}

func pcsg(replicas int32) grovecorev1alpha1.PodCliqueScalingGroup {
	return pcsgNamed(testPCSGName, replicas, 2)
}

func pcsgNamed(name string, replicas, minAvailable int32) grovecorev1alpha1.PodCliqueScalingGroup {
	return grovecorev1alpha1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: testPCSName, Replica: 0}, name),
		},
		Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{Replicas: replicas, MinAvailable: ptr.To(minAvailable)},
	}
}

func anchorEntryByIndex(t *testing.T, entries []grovecorev1alpha1.PodGangEntry, index int32) grovecorev1alpha1.PodGangEntry {
	t.Helper()
	for i := range entries {
		if entries[i].Role == grovecorev1alpha1.PodGangEntryRoleAnchor &&
			entries[i].AnchorIndex != nil && *entries[i].AnchorIndex == index {
			return entries[i]
		}
	}
	require.FailNow(t, "anchor entry not found", "index: %d", index)
	return grovecorev1alpha1.PodGangEntry{}
}

func assertUniqueEpochs(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
	t.Helper()
	epochs := make(map[string]struct{}, len(entries))
	for i := range entries {
		assert.NotContains(t, epochs, entries[i].Epoch, "duplicate epoch %s", entries[i].Epoch)
		epochs[entries[i].Epoch] = struct{}{}
	}
}

// TestBuildBootstrapEntriesFromZeroReplicaSample loads the GREP-0677 scale-to-zero sample
// (router active, decode idle, prefill PCSG idle) and verifies the bootstrap anchor omits the idle
// standalone PodClique and the idle PodCliqueScalingGroup, so neither materializes a PodGang.
func TestBuildBootstrapEntriesFromZeroReplicaSample(t *testing.T) {
	data, err := os.ReadFile("../../../../testdata/zero-replica-gang-membership.yaml")
	require.NoError(t, err)
	pcs := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, yaml.Unmarshal(data, pcs))
	// Defaulting webhook normally sets MinAvailable; the sample already sets it on every clique/PCSG.
	pcs.Status.CurrentGenerationHash = ptr.To(testGenHash)

	clk := clocktesting.NewFakeClock(time.Unix(0, 1000))
	entries := buildBootstrapEntries(pcs, clk, nil)

	anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
	require.Equal(t, grovecorev1alpha1.PodGangEntryRoleAnchor, anchor.Role)
	// Active standalone router is present; idle decode is omitted.
	assert.Contains(t, anchor.PodCliques, "router")
	assert.NotContains(t, anchor.PodCliques, "decode", "idle standalone PodClique must be omitted")
	// Idle prefill PCSG contributes no anchor indices.
	assert.NotContains(t, anchor.PCSGReplicaIndices, "prefill", "idle PCSG must contribute no anchor indices")
	// No Tail entry (prefill is the only PCSG and it is idle).
	tail := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleTail)
	assert.NotEqual(t, grovecorev1alpha1.PodGangEntryRoleTail, tail.Role, "idle PCSG must not produce a Tail entry")
}

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

//go:build e2e

package utils

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
)

// ExpectedReplicaPodGangMap describes the expected entries of one replica's PodGangMap. A nil tailIndices
// means the workload has no tail entry (PodCliqueScalingGroup replicas equal MinAvailable). A nil
// scaleOutIndices means the scale-out entry carries no indices (nothing has scaled out).
type ExpectedReplicaPodGangMap struct {
	GenerationHash      string // If set, every entry must carry this hash.
	StandalonePodCounts map[string]int32
	PCSGName            string
	AnchorIndices       []int32
	TailIndices         []int32
	ScaleOutIndices     []int32
}

// AssertReplicaPodGangMap asserts the exact roles, standalone pod counts, and PCSG replica indices of a
// replica's PodGangMap. Tail and scale-out entries must depend on the anchor and carry no standalone pods.
// If want.GenerationHash is set, every entry must carry it.
func AssertReplicaPodGangMap(t *testing.T, entries []grovev1beta1.PodGangEntry, want ExpectedReplicaPodGangMap) {
	t.Helper()
	wantRoles := []grovev1beta1.PodGangEntryRole{
		grovev1beta1.PodGangEntryRoleAnchor,
		grovev1beta1.PodGangEntryRoleScaleOut,
	}
	if want.TailIndices != nil {
		wantRoles = append(wantRoles, grovev1beta1.PodGangEntryRoleTail)
	}
	AssertEntryRoles(t, entries, wantRoles...)
	if want.GenerationHash != "" {
		for _, entry := range entries {
			assert.Equalf(t, want.GenerationHash, entry.PodCliqueSetGenerationHash,
				"entry %s (%s) must carry the expected generation hash", entry.Epoch, entry.Role)
		}
	}

	anchor := RequireSingleEntryByRole(t, entries, grovev1beta1.PodGangEntryRoleAnchor)
	AssertStandalonePCLQPodCounts(t, anchor, want.StandalonePodCounts)
	assert.Equal(t, map[string][]int32{want.PCSGName: want.AnchorIndices}, anchor.PCSGReplicaIndices)
	AssertPodGangEntryDependsOn(t, anchor, nil)

	if want.TailIndices != nil {
		tail := RequireSingleEntryByRole(t, entries, grovev1beta1.PodGangEntryRoleTail)
		AssertStandalonePCLQPodCounts(t, tail, nil)
		assert.Equal(t, map[string][]int32{want.PCSGName: want.TailIndices}, tail.PCSGReplicaIndices)
		AssertPodGangEntryDependsOn(t, tail, []string{anchor.Epoch})
	}

	scaleOut := RequireSingleEntryByRole(t, entries, grovev1beta1.PodGangEntryRoleScaleOut)
	AssertStandalonePCLQPodCounts(t, scaleOut, nil)
	if len(want.ScaleOutIndices) == 0 {
		assert.Empty(t, scaleOut.PCSGReplicaIndices)
	} else {
		assert.Equal(t, map[string][]int32{want.PCSGName: want.ScaleOutIndices}, scaleOut.PCSGReplicaIndices)
	}
	AssertPodGangEntryDependsOn(t, scaleOut, []string{anchor.Epoch})
}

// AssertEntryRoles fails unless entries carry exactly the given roles, one entry per role.
func AssertEntryRoles(t *testing.T, entries []grovev1beta1.PodGangEntry, roles ...grovev1beta1.PodGangEntryRole) {
	t.Helper()
	actual := make([]grovev1beta1.PodGangEntryRole, 0, len(entries))
	for i := range entries {
		actual = append(actual, entries[i].Role)
	}
	assert.ElementsMatch(t, roles, actual)
}

// RequireSingleEntryByRole returns the single entry with the given role, failing if there is not exactly one.
func RequireSingleEntryByRole(t *testing.T, entries []grovev1beta1.PodGangEntry, role grovev1beta1.PodGangEntryRole) grovev1beta1.PodGangEntry {
	t.Helper()
	var found []grovev1beta1.PodGangEntry
	for i := range entries {
		if entries[i].Role == role {
			found = append(found, entries[i])
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one %s entry, found %d", role, len(found))
	}
	return found[0]
}

// AssertStandalonePCLQPodCounts fails unless the entry's PodCliques equal expected. A nil expected means empty.
func AssertStandalonePCLQPodCounts(t *testing.T, entry grovev1beta1.PodGangEntry, expected map[string]int32) {
	t.Helper()
	if len(expected) == 0 {
		assert.Empty(t, entry.PodCliques)
		return
	}
	assert.Equal(t, expected, entry.PodCliques)
}

// AssertPodGangEntryDependsOn fails unless the entry's DependsOn equals expected. A nil expected means empty.
func AssertPodGangEntryDependsOn(t *testing.T, entry grovev1beta1.PodGangEntry, expected []string) {
	t.Helper()
	if len(expected) == 0 {
		assert.Empty(t, entry.DependsOn)
		return
	}
	assert.Equal(t, expected, entry.DependsOn)
}

// AssertPodGangMapSingleGeneration fails the test unless every entry of the PCS replica 0 PodGangMap is at
// the PodCliqueSet's current generation hash, proving the coherent update reconverged the map and left no
// stale intermediate entry behind.
func AssertPodGangMapSingleGeneration(t *testing.T, tc *testctx.TestContext) {
	t.Helper()
	currentHash := PCSGenerationHash(t, tc)
	for _, entry := range PodGangMapEntries(t, tc, 0) {
		assert.Equalf(t, currentHash, entry.PodCliqueSetGenerationHash,
			"PodGang entry (role %s, epoch %s) is at a stale generation hash", entry.Role, entry.Epoch)
	}
}

// PodGangMapEntries returns the entries of the given PCS replica's PodGangMap.
func PodGangMapEntries(t *testing.T, tc *testctx.TestContext, pcsReplicaIndex int) []grovev1beta1.PodGangEntry {
	t.Helper()
	pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: tc.Workload.Name, Replica: pcsReplicaIndex})
	var pgm grovev1beta1.PodGangMap
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: pgmName}, &pgm); err != nil {
		t.Fatalf("Failed to get PodGangMap %s: %v", pgmName, err)
	}
	return pgm.Spec.Entries
}

// AssertPodGangEntryPCSGIndices fails unless the entry's replica indices for pcsgName equal expected. A nil
// expected means the entry carries no indices for pcsgName.
func AssertPodGangEntryPCSGIndices(t *testing.T, entry grovev1beta1.PodGangEntry, pcsgName string, expected []int32) {
	t.Helper()
	if len(expected) == 0 {
		assert.Empty(t, entry.PCSGReplicaIndices[pcsgName])
		return
	}
	assert.Equal(t, expected, entry.PCSGReplicaIndices[pcsgName])
}

// AssertEntriesUnchangedExceptHashForNonCoherentUpdate asserts the PodGangMap invariant that holds for
// RollingRecreate and OnDelete updates, which preserve PodGangs and entries. It fails unless after has
// the same entries as before, matched by epoch, with the same role, anchor index, pod counts, PCSG
// indices, and DependsOn, and only the generation hash changed, to newHash on every entry. It does not
// hold for Coherent updates, which create new-generation entries and drain old-generation ones.
func AssertEntriesUnchangedExceptHashForNonCoherentUpdate(t *testing.T, before, after []grovev1beta1.PodGangEntry, newHash string) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("entry count changed across the update: before %d, after %d", len(before), len(after))
	}
	afterByEpoch := make(map[string]grovev1beta1.PodGangEntry, len(after))
	for i := range after {
		afterByEpoch[after[i].Epoch] = after[i]
	}
	for i := range before {
		b := before[i]
		a, ok := afterByEpoch[b.Epoch]
		if !ok {
			t.Fatalf("entry with epoch %s is missing after the update", b.Epoch)
		}
		if a.PodCliqueSetGenerationHash != newHash {
			t.Fatalf("entry with epoch %s carries generation hash %q after the update, expected %q", b.Epoch, a.PodCliqueSetGenerationHash, newHash)
		}
		// Set the expected new hash on the before-copy, then assert every other field matches across
		// before and after. The before entry is a value copy, so this does not mutate the caller's slice.
		b.PodCliqueSetGenerationHash = newHash
		assert.Equal(t, b, a, "entry with epoch %s changed across the update beyond its generation hash", b.Epoch)
	}
}

// GetReplicaPodGangMap fetches the PodGangMap object of the given PCS replica.
func GetReplicaPodGangMap(tc *testctx.TestContext, pcsReplicaIndex int) (*grovev1beta1.PodGangMap, error) {
	pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: tc.Workload.Name, Replica: pcsReplicaIndex})
	var pgm grovev1beta1.PodGangMap
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: pgmName}, &pgm); err != nil {
		return nil, err
	}
	return &pgm, nil
}

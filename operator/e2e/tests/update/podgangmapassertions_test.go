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

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
)

// getPCSGenerationHash returns the PodCliqueSet's current generation hash from its status.
func getPCSGenerationHash(t *testing.T, tc *testctx.TestContext) string {
	t.Helper()
	var pcs grovev1alpha1.PodCliqueSet
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: tc.Workload.Name}, &pcs); err != nil {
		t.Fatalf("Failed to get PodCliqueSet %s: %v", tc.Workload.Name, err)
	}
	if pcs.Status.CurrentGenerationHash == nil {
		t.Fatalf("PodCliqueSet %s has no CurrentGenerationHash", tc.Workload.Name)
	}
	return *pcs.Status.CurrentGenerationHash
}

// getPodGangMapEntries returns the entries of the given PCS replica's PodGangMap.
func getPodGangMapEntries(t *testing.T, tc *testctx.TestContext, pcsReplicaIndex int) []grovev1alpha1.PodGangEntry {
	t.Helper()
	pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: tc.Workload.Name, Replica: pcsReplicaIndex})
	var pgm grovev1alpha1.PodGangMap
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: pgmName}, &pgm); err != nil {
		t.Fatalf("Failed to get PodGangMap %s: %v", pgmName, err)
	}
	return pgm.Spec.Entries
}

// entryByRole returns the single entry with the given role, failing if there is not exactly one.
func entryByRole(t *testing.T, entries []grovev1alpha1.PodGangEntry, role grovev1alpha1.PodGangEntryRole) grovev1alpha1.PodGangEntry {
	t.Helper()
	var found []grovev1alpha1.PodGangEntry
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

// assertEntryRoles fails unless entries carry exactly the given roles, one entry per role.
func assertEntryRoles(t *testing.T, entries []grovev1alpha1.PodGangEntry, roles ...grovev1alpha1.PodGangEntryRole) {
	t.Helper()
	actual := make([]grovev1alpha1.PodGangEntryRole, 0, len(entries))
	for i := range entries {
		actual = append(actual, entries[i].Role)
	}
	assert.ElementsMatch(t, roles, actual)
}

// assertStandalonePCLQPodCounts fails unless the entry's PodCliques equal expected. A nil expected means empty.
func assertStandalonePCLQPodCounts(t *testing.T, entry grovev1alpha1.PodGangEntry, expected map[string]int32) {
	t.Helper()
	if len(expected) == 0 {
		assert.Empty(t, entry.PodCliques)
		return
	}
	assert.Equal(t, expected, entry.PodCliques)
}

// assertPodGangEntryPCSGIndices fails unless the entry's replica indices for pcsgName equal expected. A nil
// expected means the entry carries no indices for pcsgName.
func assertPodGangEntryPCSGIndices(t *testing.T, entry grovev1alpha1.PodGangEntry, pcsgName string, expected []int32) {
	t.Helper()
	if len(expected) == 0 {
		assert.Empty(t, entry.PCSGReplicaIndices[pcsgName])
		return
	}
	assert.Equal(t, expected, entry.PCSGReplicaIndices[pcsgName])
}

// assertPodGangEntryDependsOn fails unless the entry's DependsOn equals expected. A nil expected means empty.
func assertPodGangEntryDependsOn(t *testing.T, entry grovev1alpha1.PodGangEntry, expected []string) {
	t.Helper()
	if len(expected) == 0 {
		assert.Empty(t, entry.DependsOn)
		return
	}
	assert.Equal(t, expected, entry.DependsOn)
}

// assertEntriesUnchangedExceptHashForNonCoherentUpdate asserts the PodGangMap invariant that holds for
// RollingRecreate and OnDelete updates, which preserve PodGangs and entries. It fails unless after has
// the same entries as before, matched by epoch, with the same role, anchor index, pod counts, PCSG
// indices, and DependsOn, and only the generation hash changed, to newHash on every entry. It does not
// hold for Coherent updates, which create new-generation entries and drain old-generation ones.
func assertEntriesUnchangedExceptHashForNonCoherentUpdate(t *testing.T, before, after []grovev1alpha1.PodGangEntry, newHash string) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("entry count changed across the update: before %d, after %d", len(before), len(after))
	}
	afterByEpoch := make(map[string]grovev1alpha1.PodGangEntry, len(after))
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

// expectedReplicaPodGangMap describes the expected entries of one replica's PodGangMap. A nil tailIndices
// means the workload has no tail entry (PodCliqueScalingGroup replicas equal MinAvailable). A nil
// scaleOutIndices means the scale-out entry carries no indices (nothing has scaled out).
type expectedReplicaPodGangMap struct {
	standalonePodCounts map[string]int32
	pcsgName            string
	anchorIndices       []int32
	tailIndices         []int32
	scaleOutIndices     []int32
}

// assertReplicaPodGangMap asserts a replica's PodGangMap entries match want. It verifies the anchor entry,
// the tail entry when want.tailIndices is set, and the scale-out entry, checking each entry's PCSG indices
// and that the tail and scale-out entries depend on the anchor epoch.
func assertReplicaPodGangMap(t *testing.T, entries []grovev1alpha1.PodGangEntry, want expectedReplicaPodGangMap) {
	t.Helper()
	wantRoles := []grovev1alpha1.PodGangEntryRole{
		grovev1alpha1.PodGangEntryRoleAnchor,
		grovev1alpha1.PodGangEntryRoleScaleOut,
	}
	if want.tailIndices != nil {
		wantRoles = append(wantRoles, grovev1alpha1.PodGangEntryRoleTail)
	}
	assertEntryRoles(t, entries, wantRoles...)

	anchor := entryByRole(t, entries, grovev1alpha1.PodGangEntryRoleAnchor)
	assertStandalonePCLQPodCounts(t, anchor, want.standalonePodCounts)
	assertPodGangEntryPCSGIndices(t, anchor, want.pcsgName, want.anchorIndices)
	assertPodGangEntryDependsOn(t, anchor, nil)

	if want.tailIndices != nil {
		tail := entryByRole(t, entries, grovev1alpha1.PodGangEntryRoleTail)
		assertPodGangEntryPCSGIndices(t, tail, want.pcsgName, want.tailIndices)
		assertPodGangEntryDependsOn(t, tail, []string{anchor.Epoch})
	}

	scaleOut := entryByRole(t, entries, grovev1alpha1.PodGangEntryRoleScaleOut)
	assertPodGangEntryPCSGIndices(t, scaleOut, want.pcsgName, want.scaleOutIndices)
	assertPodGangEntryDependsOn(t, scaleOut, []string{anchor.Epoch})
}

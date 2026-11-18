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
	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/e2e/testctx"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
	"testing"
)

// getPCSGenerationHash returns the PodCliqueSet's current generation hash from its status.
func getPCSGenerationHash(t *testing.T, tc *testctx.TestContext) string {
	t.Helper()
	var pcs grovev1beta1.PodCliqueSet
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: tc.Workload.Name}, &pcs); err != nil {
		t.Fatalf("Failed to get PodCliqueSet %s: %v", tc.Workload.Name, err)
	}
	if pcs.Status.CurrentGenerationHash == nil {
		t.Fatalf("PodCliqueSet %s has no CurrentGenerationHash", tc.Workload.Name)
	}
	return *pcs.Status.CurrentGenerationHash
}

// getPodGangMapEntries returns the entries of the given PCS replica's PodGangMap.
func getPodGangMapEntries(t *testing.T, tc *testctx.TestContext, pcsReplicaIndex int) []grovev1beta1.PodGangEntry {
	t.Helper()
	pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: tc.Workload.Name, Replica: pcsReplicaIndex})
	var pgm grovev1beta1.PodGangMap
	if err := tc.Client.Get(tc.Ctx, types.NamespacedName{Namespace: tc.Namespace, Name: pgmName}, &pgm); err != nil {
		t.Fatalf("Failed to get PodGangMap %s: %v", pgmName, err)
	}
	return pgm.Spec.Entries
}

// assertPodGangMapSingleGeneration fails the test unless every entry of the PCS replica 0 PodGangMap is at
// the PodCliqueSet's current generation hash, proving the coherent update reconverged the map and left no
// stale intermediate entry behind.
func assertPodGangMapSingleGeneration(t *testing.T, tc *testctx.TestContext) {
	t.Helper()
	currentHash := getPCSGenerationHash(t, tc)
	for _, entry := range getPodGangMapEntries(t, tc, 0) {
		assert.Equalf(t, currentHash, entry.PodCliqueSetGenerationHash,
			"PodGang entry (role %s, epoch %s) is at a stale generation hash", entry.Role, entry.Epoch)
	}
}

// assertPodGangEntryPCSGIndices fails unless the entry's replica indices for pcsgName equal expected. A nil
// expected means the entry carries no indices for pcsgName.
func assertPodGangEntryPCSGIndices(t *testing.T, entry grovev1beta1.PodGangEntry, pcsgName string, expected []int32) {
	t.Helper()
	if len(expected) == 0 {
		assert.Empty(t, entry.PCSGReplicaIndices[pcsgName])
		return
	}
	assert.Equal(t, expected, entry.PCSGReplicaIndices[pcsgName])
}

// assertEntriesUnchangedExceptHashForNonCoherentUpdate asserts the PodGangMap invariant that holds for
// RollingRecreate and OnDelete updates, which preserve PodGangs and entries. It fails unless after has
// the same entries as before, matched by epoch, with the same role, anchor index, pod counts, PCSG
// indices, and DependsOn, and only the generation hash changed, to newHash on every entry. It does not
// hold for Coherent updates, which create new-generation entries and drain old-generation ones.
func assertEntriesUnchangedExceptHashForNonCoherentUpdate(t *testing.T, before, after []grovev1beta1.PodGangEntry, newHash string) {
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

// assertEachPodGangSingleRevision fails the test if any PodGang carries pods of more than one pod template
// hash for the given clique on the given replica. A coherent update must never place a new-revision pod on
// an old-version PodGang, so every PodGang holds a single revision.
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

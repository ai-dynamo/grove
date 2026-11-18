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
	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/stretchr/testify/assert"
	"testing"
)

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

// AssertEntryRoles fails unless entries carry exactly the given roles, one entry per role.
func AssertEntryRoles(t *testing.T, entries []grovev1beta1.PodGangEntry, roles ...grovev1beta1.PodGangEntryRole) {
	t.Helper()
	actual := make([]grovev1beta1.PodGangEntryRole, 0, len(entries))
	for i := range entries {
		actual = append(actual, entries[i].Role)
	}
	assert.ElementsMatch(t, roles, actual)
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

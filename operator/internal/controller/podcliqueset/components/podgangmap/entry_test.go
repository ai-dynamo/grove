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
	"strconv"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
)

func TestSortEntriesByEpoch(t *testing.T) {
	tests := []struct {
		name            string
		epochs          []string
		expectErr       bool
		expectedOrdered []string
	}{
		{
			name:            "already sorted",
			epochs:          []string{"100", "200", "300"},
			expectedOrdered: []string{"100", "200", "300"},
		},
		{
			name:            "unsorted is ordered ascending",
			epochs:          []string{"300", "100", "200"},
			expectedOrdered: []string{"100", "200", "300"},
		},
		{
			name:            "numeric not lexicographic ordering",
			epochs:          []string{"1000000000", "999999999"},
			expectedOrdered: []string{"999999999", "1000000000"},
		},
		{
			name:      "non-numeric epoch is an error",
			epochs:    []string{"100", "abc"},
			expectErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := entriesWithEpochs(tt.epochs)
			err := sortEntriesByEpoch(entries)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			actual := epochsOf(entries)
			assert.Equal(t, tt.expectedOrdered, actual)
		})
	}
}

func TestIsPodGangEntryEmpty(t *testing.T) {
	tests := []struct {
		name     string
		entry    grovecorev1alpha1.PodGangEntry
		expected bool
	}{
		{
			name:     "nil pod counts and nil replica indices",
			entry:    testutils.NewPodGangEntryBuilder("hash", "100").Build(),
			expected: true,
		},
		{
			name:     "present but zero pod counts and empty replica index sets",
			entry:    testutils.NewPodGangEntryBuilder("hash", "100").WithPodCliques(map[string]int32{"a": 0}).WithPCSGReplicaIndices(map[string][]int32{"sg": {}}).Build(),
			expected: true,
		},
		{
			name:     "entry with a non-zero pod count",
			entry:    testutils.NewPodGangEntryBuilder("hash", "100").WithPodCliques(map[string]int32{"a": 1}).Build(),
			expected: false,
		},
		{
			name:     "entry with a non-empty replica index set",
			entry:    testutils.NewPodGangEntryBuilder("hash", "100").WithPCSGReplicaIndices(map[string][]int32{"sg": {0}}).Build(),
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := isPodGangEntryEmpty(tt.entry)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestShouldAdvanceEntriesGenerationHash(t *testing.T) {
	tests := []struct {
		name         string
		strategyType *grovecorev1alpha1.UpdateStrategyType
		entryHashes  []string
		expected     bool
	}{
		{
			name:        "nil strategy defaults to RollingRecreate and advances on drift",
			entryHashes: []string{"old"},
			expected:    true,
		},
		{
			name:         "RollingRecreate advances when an entry lags the current hash",
			strategyType: ptr.To(grovecorev1alpha1.RollingRecreateStrategy),
			entryHashes:  []string{"new", "old"},
			expected:     true,
		},
		{
			name:         "RollingRecreate does not advance when all entries are current",
			strategyType: ptr.To(grovecorev1alpha1.RollingRecreateStrategy),
			entryHashes:  []string{"new", "new"},
			expected:     false,
		},
		{
			name:         "OnDelete advances when an entry lags the current hash",
			strategyType: ptr.To(grovecorev1alpha1.OnDeleteStrategy),
			entryHashes:  []string{"old"},
			expected:     true,
		},
		{
			name:         "OnDelete does not advance when all entries are current",
			strategyType: ptr.To(grovecorev1alpha1.OnDeleteStrategy),
			entryHashes:  []string{"new", "new"},
			expected:     false,
		},
		{
			name:         "Coherent never advances even when entries lag",
			strategyType: ptr.To(grovecorev1alpha1.CoherentStrategy),
			entryHashes:  []string{"old"},
			expected:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcsBuilder := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
				WithPodCliqueSetGenerationHash(ptr.To("new"))
			if tt.strategyType != nil {
				pcsBuilder.WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: *tt.strategyType})
			}
			pcs := pcsBuilder.Build()
			entries := entriesWithGenerationHashes(tt.entryHashes)

			actual := shouldAdvanceEntriesGenerationHash(pcs, entries)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestAdvanceEntriesGenerationHash(t *testing.T) {
	entries := []grovecorev1alpha1.PodGangEntry{
		testutils.NewPodGangEntryBuilder("old", "100").Build(),
		testutils.NewPodGangEntryBuilder("old", "200").Build(),
	}
	advanceEntriesGenerationHash(entries, "new")
	for i := range entries {
		assert.Equal(t, "new", entries[i].PodCliqueSetGenerationHash)
	}
}

func TestClonePodGangEntriesDoesNotAlias(t *testing.T) {
	original := []grovecorev1alpha1.PodGangEntry{
		testutils.NewPodGangEntryBuilder("hash", "100").
			WithPodCliques(map[string]int32{"a": 1}).
			WithPCSGReplicaIndices(map[string][]int32{"sg": {0, 1}}).
			Build(),
	}
	cloned := clonePodGangEntries(original)

	// Mutating the clone's maps must not affect the original.
	cloned[0].PodCliques["a"] = 99
	cloned[0].PCSGReplicaIndices["sg"] = append(cloned[0].PCSGReplicaIndices["sg"], 2)

	assert.Equal(t, int32(1), original[0].PodCliques["a"])
	assert.Equal(t, []int32{0, 1}, original[0].PCSGReplicaIndices["sg"])
}

func entriesWithEpochs(epochs []string) []grovecorev1alpha1.PodGangEntry {
	entries := make([]grovecorev1alpha1.PodGangEntry, 0, len(epochs))
	for _, e := range epochs {
		entries = append(entries, testutils.NewPodGangEntryBuilder("hash", e).Build())
	}
	return entries
}

func epochsOf(entries []grovecorev1alpha1.PodGangEntry) []string {
	epochs := make([]string, 0, len(entries))
	for i := range entries {
		epochs = append(epochs, entries[i].Epoch)
	}
	return epochs
}

func entriesWithGenerationHashes(hashes []string) []grovecorev1alpha1.PodGangEntry {
	entries := make([]grovecorev1alpha1.PodGangEntry, 0, len(hashes))
	for i, h := range hashes {
		entries = append(entries, testutils.NewPodGangEntryBuilder(h, strconv.Itoa(i)).Build())
	}
	return entries
}

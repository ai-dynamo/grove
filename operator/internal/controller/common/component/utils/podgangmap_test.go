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

package utils

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
)

func TestGetPodGangMapEntriesByGenerationHash(t *testing.T) {
	entries := []grovecorev1alpha1.PodGangEntry{
		{Name: "pg-0", PodCliqueSetGenerationHash: "hash-old", PodCliques: map[string]int32{"f": 3}},
		{Name: "pg-1", PodCliqueSetGenerationHash: "hash-new", PodCliques: map[string]int32{"f": 2}},
		{Name: "pg-2", PodCliqueSetGenerationHash: "hash-old", PCSGReplicaIndices: map[string][]int32{"pcsg-0": {0}}},
		{Name: "pg-3", PodCliqueSetGenerationHash: "hash-new", PCSGReplicaIndices: map[string][]int32{"pcsg-0": {0}}},
	}

	t.Run("returns entries matching old hash", func(t *testing.T) {
		result := FilterPodGangMapEntriesByGenerationHash(entries, "hash-old")
		assert.Len(t, result, 2)
		assert.Equal(t, "pg-0", result[0].Name)
		assert.Equal(t, "pg-2", result[1].Name)
	})

	t.Run("returns entries matching new hash", func(t *testing.T) {
		result := FilterPodGangMapEntriesByGenerationHash(entries, "hash-new")
		assert.Len(t, result, 2)
		assert.Equal(t, "pg-1", result[0].Name)
		assert.Equal(t, "pg-3", result[1].Name)
	})

	t.Run("returns empty for non-existent hash", func(t *testing.T) {
		result := FilterPodGangMapEntriesByGenerationHash(entries, "hash-unknown")
		assert.Empty(t, result)
	})

	t.Run("returns empty for empty entries", func(t *testing.T) {
		result := FilterPodGangMapEntriesByGenerationHash(nil, "hash-old")
		assert.Empty(t, result)
	})
}

func TestGetPodGangMapEntriesForPCLQ(t *testing.T) {
	entries := []grovecorev1alpha1.PodGangEntry{
		{Name: "pg-0", PodCliques: map[string]int32{"pcs-0-frontend": 3, "pcs-0-backend": 2}},
		{Name: "pg-1", PodCliques: map[string]int32{"pcs-0-frontend": 2}},
		{Name: "pg-2", PCSGReplicaIndices: map[string][]int32{"pcs-0-decode": {0}}},
		{Name: "pg-3", PodCliques: map[string]int32{"pcs-0-backend": 1}},
	}

	t.Run("returns entries referencing pcs-0-frontend", func(t *testing.T) {
		result := GetPodGangMapEntriesForPCLQ(entries, "pcs-0-frontend")
		assert.Len(t, result, 2)
		assert.Equal(t, "pg-0", result[0].Name)
		assert.Equal(t, "pg-1", result[1].Name)
	})

	t.Run("returns entries referencing pcs-0-backend", func(t *testing.T) {
		result := GetPodGangMapEntriesForPCLQ(entries, "pcs-0-backend")
		assert.Len(t, result, 2)
		assert.Equal(t, "pg-0", result[0].Name)
		assert.Equal(t, "pg-3", result[1].Name)
	})

	t.Run("returns empty for PCLQ not in any entry", func(t *testing.T) {
		result := GetPodGangMapEntriesForPCLQ(entries, "pcs-0-nonexistent")
		assert.Empty(t, result)
	})

	t.Run("does not match PCSG entries", func(t *testing.T) {
		result := GetPodGangMapEntriesForPCLQ(entries, "pcs-0-decode")
		assert.Empty(t, result)
	})

	t.Run("returns empty for empty entries", func(t *testing.T) {
		result := GetPodGangMapEntriesForPCLQ(nil, "pcs-0-frontend")
		assert.Empty(t, result)
	})
}

func TestGetPodGangMapEntriesForPCSG(t *testing.T) {
	entries := []grovecorev1alpha1.PodGangEntry{
		{Name: "pg-0", PodCliques: map[string]int32{"pcs-0-frontend": 3}, PCSGReplicaIndices: map[string][]int32{"pcs-0-prefill": {0, 1}, "pcs-0-decode": {0}}},
		{Name: "pg-1", PCSGReplicaIndices: map[string][]int32{"pcs-0-prefill": {2}}},
		{Name: "pg-2", PCSGReplicaIndices: map[string][]int32{"pcs-0-decode": {1}}},
		{Name: "pg-3", PodCliques: map[string]int32{"pcs-0-frontend": 2}},
	}

	t.Run("returns entries referencing pcs-0-prefill", func(t *testing.T) {
		result := GetPodGangMapEntriesForPCSG(entries, "pcs-0-prefill")
		assert.Len(t, result, 2)
		assert.Equal(t, "pg-0", result[0].Name)
		assert.Equal(t, "pg-1", result[1].Name)
	})

	t.Run("returns entries referencing pcs-0-decode", func(t *testing.T) {
		result := GetPodGangMapEntriesForPCSG(entries, "pcs-0-decode")
		assert.Len(t, result, 2)
		assert.Equal(t, "pg-0", result[0].Name)
		assert.Equal(t, "pg-2", result[1].Name)
	})

	t.Run("returns empty for PCSG not in any entry", func(t *testing.T) {
		result := GetPodGangMapEntriesForPCSG(entries, "pcs-0-nonexistent")
		assert.Empty(t, result)
	})

	t.Run("does not match PodCliques entries", func(t *testing.T) {
		result := GetPodGangMapEntriesForPCSG(entries, "pcs-0-frontend")
		assert.Empty(t, result)
	})

	t.Run("returns empty for empty entries", func(t *testing.T) {
		result := GetPodGangMapEntriesForPCSG(nil, "pcs-0-prefill")
		assert.Empty(t, result)
	})
}

func TestLatestEpochForGenerationHash(t *testing.T) {
	epochEntry := func(name, hash, epoch string) grovecorev1alpha1.PodGangEntry {
		return grovecorev1alpha1.PodGangEntry{
			Name:                       name,
			PodCliqueSetGenerationHash: hash,
			Labels:                     map[string]string{apicommon.LabelEpoch: epoch},
		}
	}
	tests := []struct {
		name      string
		entries   []grovecorev1alpha1.PodGangEntry
		hash      string
		wantEpoch *string
		wantErr   bool
	}{
		{
			name:      "nil when no entry matches the hash",
			entries:   []grovecorev1alpha1.PodGangEntry{epochEntry("pg-0", "old", "100")},
			hash:      "new",
			wantEpoch: nil,
		},
		{
			name:      "nil for empty entries",
			entries:   nil,
			hash:      "new",
			wantEpoch: nil,
		},
		{
			name:      "returns the sole matching epoch",
			entries:   []grovecorev1alpha1.PodGangEntry{epochEntry("pg-0", "new", "100")},
			hash:      "new",
			wantEpoch: ptr.To("100"),
		},
		{
			name: "returns the numerically largest epoch, ignoring digit width",
			entries: []grovecorev1alpha1.PodGangEntry{
				epochEntry("pg-0", "new", "9"),
				epochEntry("pg-1", "new", "100"),
				epochEntry("pg-2", "new", "20"),
			},
			hash:      "new",
			wantEpoch: ptr.To("100"),
		},
		{
			name: "considers only entries at the requested hash",
			entries: []grovecorev1alpha1.PodGangEntry{
				epochEntry("pg-old", "old", "999"),
				epochEntry("pg-new", "new", "100"),
			},
			hash:      "new",
			wantEpoch: ptr.To("100"),
		},
		{
			name:    "error when a matching entry is missing the epoch label",
			entries: []grovecorev1alpha1.PodGangEntry{{Name: "pg-0", PodCliqueSetGenerationHash: "new"}},
			hash:    "new",
			wantErr: true,
		},
		{
			name:    "error when a matching entry has a non-numeric epoch label",
			entries: []grovecorev1alpha1.PodGangEntry{epochEntry("pg-0", "new", "not-a-number")},
			hash:    "new",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LatestEpochForGenerationHash(tc.entries, tc.hash)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantEpoch, got)
		})
	}
}

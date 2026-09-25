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
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplySubStep(t *testing.T) {
	testCases := []struct {
		description string
		entries     []grovecorev1alpha1.PodGangEntry
		mvu         *mvuTemplate
		ss          subStep
		want        []grovecorev1alpha1.PodGangEntry
	}{
		{
			// Current state is one old-hash v1 anchor holding MinAvailable of each component, frontend 2 and
			// decode [0,1,2]. The sub-step opens the v2 anchor, draining that MinAvailable from v1 and creating a
			// v2 anchor with MinAvailable frontend from the mvuTemplate and the decode MinAvailable indices. v1
			// then drains to empty and is removed, leaving only the new v2 anchor at AnchorIndex 0.
			description: "an anchor sub-step creates the new anchor and removes the drained old anchor",
			entries: []grovecorev1alpha1.PodGangEntry{
				{Epoch: "50", PodCliqueSetGenerationHash: "v1", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 2}, PCSGReplicaIndices: map[string][]int32{"decode": {0, 1, 2}}},
			},
			mvu: &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}, pcsgs: map[string]int32{"decode": 3}},
			ss: subStep{
				epoch:                     "200",
				opensAnchor:               true,
				anchorPCSGReplicaIndices:  map[string][]int32{"decode": {0, 1, 2}},
				drainStandalonePCLQCounts: map[string]int32{"frontend": 2},
				drainPCSGReplicaIndices:   map[string][]int32{"decode": {0, 1, 2}},
			},
			want: []grovecorev1alpha1.PodGangEntry{
				{Epoch: "200", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 2}, PCSGReplicaIndices: map[string][]int32{"decode": {0, 1, 2}}},
			},
		},
		{
			// Current state is an old v1 anchor {frontend:3, decode:[3,4,5]} and the already-opened v2 anchor
			// {frontend:2, decode:[0,1,2]}. The sub-step rolls the decode tail and subsumes 3 frontend pods, so
			// v1 drains fully and is removed, the v2 anchor grows to frontend 5, and a new v2 tail entry holds
			// decode indices [3,4,5] depending on the anchor epoch 200.
			description: "a tail sub-step subsumes standalone pods into the anchor and appends a tail entry",
			entries: []grovecorev1alpha1.PodGangEntry{
				{Epoch: "50", PodCliqueSetGenerationHash: "v1", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 3}, PCSGReplicaIndices: map[string][]int32{"decode": {3, 4, 5}}},
				{Epoch: "200", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 2}, PCSGReplicaIndices: map[string][]int32{"decode": {0, 1, 2}}},
			},
			mvu: &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}, pcsgs: map[string]int32{"decode": 3}},
			ss: subStep{
				epoch:                       "300",
				dependsOn:                   []string{"200"},
				subsumeAnchorEpoch:          "200",
				subsumeStandalonePCLQCounts: map[string]int32{"frontend": 3},
				drainStandalonePCLQCounts:   map[string]int32{"frontend": 3},
				tailPCSGReplicaIndices:      map[string][]int32{"decode": {3, 4, 5}},
				drainPCSGReplicaIndices:     map[string][]int32{"decode": {3, 4, 5}},
			},
			want: []grovecorev1alpha1.PodGangEntry{
				{Epoch: "200", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 5}, PCSGReplicaIndices: map[string][]int32{"decode": {0, 1, 2}}},
				{Epoch: "300", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleTail, DependsOn: []string{"200"}, PCSGReplicaIndices: map[string][]int32{"decode": {3, 4, 5}}},
			},
		},
		{
			// Current state is standalone-only, an old v1 anchor {frontend:5} and the v2 anchor {frontend:2}. The
			// sub-step subsumes 3 frontend pods into the v2 anchor and drains 3 from v1, and adds no entry because
			// there is no PCSG. v1 keeps its 2 remaining pods as a partial drain, the v2 anchor grows to frontend
			// 5, and nothing is appended.
			description: "a subsume-only sub-step grows the anchor, adds no entry, and keeps the partially drained old anchor",
			entries: []grovecorev1alpha1.PodGangEntry{
				{Epoch: "50", PodCliqueSetGenerationHash: "v1", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 5}},
				{Epoch: "200", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 2}},
			},
			mvu: &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}},
			ss: subStep{
				epoch:                       "300",
				dependsOn:                   []string{"200"},
				subsumeAnchorEpoch:          "200",
				subsumeStandalonePCLQCounts: map[string]int32{"frontend": 3},
				drainStandalonePCLQCounts:   map[string]int32{"frontend": 3},
			},
			want: []grovecorev1alpha1.PodGangEntry{
				{Epoch: "50", PodCliqueSetGenerationHash: "v1", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 2}},
				{Epoch: "200", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 5}},
			},
		},
		{
			// Current state has a mid-update re-update leaving two old generations, v0 {frontend:2} and v1
			// {frontend:3}, plus the v2 anchor {frontend:2}. The sub-step subsumes 3 frontend pods and drains 3.
			// The drain retires the oldest generation first, so v0 empties and is removed and v1 gives up 1 (kept
			// at frontend 2), while the v2 anchor grows to frontend 5.
			description: "the drain retires the oldest generation first across two old-hash generations",
			entries: []grovecorev1alpha1.PodGangEntry{
				{Epoch: "40", PodCliqueSetGenerationHash: "v0", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 2}},
				{Epoch: "50", PodCliqueSetGenerationHash: "v1", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 3}},
				{Epoch: "200", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 2}},
			},
			mvu: &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}},
			ss: subStep{
				epoch:                       "300",
				dependsOn:                   []string{"200"},
				subsumeAnchorEpoch:          "200",
				subsumeStandalonePCLQCounts: map[string]int32{"frontend": 3},
				drainStandalonePCLQCounts:   map[string]int32{"frontend": 3},
			},
			want: []grovecorev1alpha1.PodGangEntry{
				{Epoch: "50", PodCliqueSetGenerationHash: "v1", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 2}},
				{Epoch: "200", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 5}},
			},
		},
		{
			// A re-update mid-update left a frontend-only intermediate anchor v1 {frontend:1} whose
			// PCSGReplicaIndices map is nil, alongside the original v0 anchor {frontend:1, inference:[0]}. The
			// v2 sub-step opens the anchor and drains inference [0], so the drain must skip the v1 anchor that
			// carries no inference rather than write into its nil map. v0 drains to empty and is removed, the
			// v1 anchor is left untouched, and the new v2 anchor holds frontend 1 and inference [0].
			description: "an anchor sub-step draining a PCSG skips an old anchor that carries no PCSG indices",
			entries: []grovecorev1alpha1.PodGangEntry{
				{Epoch: "40", PodCliqueSetGenerationHash: "v0", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 1}, PCSGReplicaIndices: map[string][]int32{"inference": {0}}},
				{Epoch: "50", PodCliqueSetGenerationHash: "v1", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 1}},
			},
			mvu: &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 1}, pcsgs: map[string]int32{"inference": 1}},
			ss: subStep{
				epoch:                     "200",
				opensAnchor:               true,
				anchorPCSGReplicaIndices:  map[string][]int32{"inference": {0}},
				drainStandalonePCLQCounts: map[string]int32{"frontend": 1},
				drainPCSGReplicaIndices:   map[string][]int32{"inference": {0}},
			},
			want: []grovecorev1alpha1.PodGangEntry{
				{Epoch: "50", PodCliqueSetGenerationHash: "v1", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 1}},
				{Epoch: "200", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 1}, PCSGReplicaIndices: map[string][]int32{"inference": {0}}},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			planner := &subStepPlanner{pcs: pcsWithCurrentHash("v2"), mvu: tc.mvu, entries: tc.entries}
			got, err := planner.applySubStep(tc.ss)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

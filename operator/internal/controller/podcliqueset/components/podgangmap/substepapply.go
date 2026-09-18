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
	"maps"
	"slices"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"k8s.io/apimachinery/pkg/util/sets"
)

// applySubStep applies the sub-step to a copy of the current entries and returns the resulting entry set,
// leaving the snapshot's PodGangMap untouched. It drains the sub-step's old-hash take-down, grows the target
// new-hash anchor with the subsumed standalone PodClique pods, appends the sub-step's new-hash entry, and
// drops any entry drained to empty.
func (p *subStepPlanner) applySubStep(ss subStep) ([]grovecorev1alpha1.PodGangEntry, error) {
	entries := clonePodGangEntries(p.entries)
	currentHash := *p.pcs.Status.CurrentGenerationHash

	// Sort oldest epoch first so the drains retire the oldest generation first, which matters when a
	// re-update mid-update has left more than one old-hash generation live.
	if err := sortEntriesByEpoch(entries); err != nil {
		return nil, err
	}

	drainStandalonePCLQs(entries, currentHash, ss.drainStandalonePCLQCounts)
	drainPCSGIndices(entries, currentHash, ss.drainPCSGReplicaIndices)
	subsumeIntoAnchor(entries, ss.subsumeAnchorEpoch, ss.subsumeStandalonePCLQCounts)

	if newEntry, ok := p.newHashEntryForSubStep(ss); ok {
		entries = append(entries, newEntry)
	}

	return removeEmptyEntries(entries, currentHash), nil
}

// drainStandalonePCLQs subtracts each PodClique's take-down count from the old-hash anchor entries, in the
// order the entries appear. Standalone PodClique pods live only on anchor entries, so only old-hash anchors
// are touched. A standalone PodClique tracks its pods as a count rather than as identified replica indices,
// so any old-hash anchor's count can absorb the take-down. The caller sorts entries oldest first so the
// oldest generation is retired before a newer one.
func drainStandalonePCLQs(entries []grovecorev1alpha1.PodGangEntry, currentHash string, drainCounts map[string]int32) {
	for pclqName, remaining := range drainCounts {
		for i := range entries {
			if remaining == 0 {
				break
			}
			// Standalone PodClique pods live only on old-hash anchor entries, so skip everything else.
			isOldHashAnchor := entries[i].PodCliqueSetGenerationHash != currentHash && entries[i].Role == grovecorev1alpha1.PodGangEntryRoleAnchor
			if !isOldHashAnchor {
				continue
			}
			take := min(entries[i].PodCliques[pclqName], remaining)
			entries[i].PodCliques[pclqName] -= take
			remaining -= take
		}
	}
}

// drainPCSGIndices removes each PodCliqueScalingGroup's take-down replica indices from whichever old-hash
// entry carries them. A PCSG replica index is an identity that lives in exactly one entry, so no ordering or
// budgeting is needed.
func drainPCSGIndices(entries []grovecorev1alpha1.PodGangEntry, currentHash string, drainIndices map[string][]int32) {
	for pcsgName, indices := range drainIndices {
		drainSet := sets.New(indices...)
		for i := range entries {
			if entries[i].PodCliqueSetGenerationHash == currentHash {
				continue
			}
			existing, ok := entries[i].PCSGReplicaIndices[pcsgName]
			if !ok {
				continue
			}
			entries[i].PCSGReplicaIndices[pcsgName] = slices.DeleteFunc(existing, drainSet.Has)
		}
	}
}

// subsumeIntoAnchor adds the subsumed standalone PodClique pod counts to the new-hash anchor entry at
// anchorEpoch. It is a no-op when there is nothing to subsume.
func subsumeIntoAnchor(entries []grovecorev1alpha1.PodGangEntry, anchorEpoch string, subsumeCounts map[string]int32) {
	if len(subsumeCounts) == 0 {
		return
	}
	for i := range entries {
		if entries[i].Epoch != anchorEpoch {
			continue
		}
		if entries[i].PodCliques == nil {
			entries[i].PodCliques = make(map[string]int32, len(subsumeCounts))
		}
		for pclqName, count := range subsumeCounts {
			entries[i].PodCliques[pclqName] += count
		}
		return
	}
}

// newHashEntryForSubStep builds the one new-hash entry a sub-step adds, if any. An anchor sub-step adds an
// anchor entry carrying MinAvailable of every standalone PodClique plus the sub-step's PCSG floor indices. A
// sub-step that rolls PCSG tail indices adds a single tail entry holding them. A sub-step that only subsumes
// standalone PodClique pods into an existing anchor adds no entry. So a sub-step yields at most one entry,
// returned with ok true, or ok false when it adds none.
func (p *subStepPlanner) newHashEntryForSubStep(ss subStep) (grovecorev1alpha1.PodGangEntry, bool) {
	currentHash := *p.pcs.Status.CurrentGenerationHash

	if ss.opensAnchor {
		anchorEntry := newPodGangEntry(ss.epoch, currentHash, ss.dependsOn)
		anchorEntry.Role = grovecorev1alpha1.PodGangEntryRoleAnchor
		anchorEntry.PodCliques = maps.Clone(p.mvu.standalonePCLQs)
		anchorEntry.PCSGReplicaIndices = ss.anchorPCSGReplicaIndices
		return anchorEntry, true
	}

	tailPCSGReplicaIndices := make(map[string][]int32, len(ss.tailPCSGReplicaIndices))
	for pcsgName, indices := range ss.tailPCSGReplicaIndices {
		if len(indices) > 0 {
			tailPCSGReplicaIndices[pcsgName] = indices
		}
	}
	if len(tailPCSGReplicaIndices) == 0 {
		return grovecorev1alpha1.PodGangEntry{}, false
	}
	tailEntry := newPodGangEntry(ss.epoch, currentHash, ss.dependsOn)
	tailEntry.Role = grovecorev1alpha1.PodGangEntryRoleTail
	tailEntry.PCSGReplicaIndices = tailPCSGReplicaIndices
	return tailEntry, true
}

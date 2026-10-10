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

	drainStandalonePCLQs(entries, currentHash, ss.drainStandalonePCLQCounts, p.runningPodsByCliqueAndAnchor)
	drainPCSGIndices(entries, currentHash, ss.drainPCSGReplicaIndices)
	subsumeIntoAnchor(entries, ss.subsumeAnchorEpoch, ss.subsumeStandalonePCLQCounts)

	if newEntry, ok := p.newHashEntryForSubStep(ss); ok {
		entries = append(entries, newEntry)
	}

	return removeEmptyEntries(entries, currentHash), nil
}

// drainStandalonePCLQs removes each standalone PodClique take-down count from the old-version anchor entries.
// It runs in two phases per PodClique, and the phase order keeps the MaxUnavailable accounting exact.
//
// Phase 1 reclaims missing old-version Pods. A missing old-version Pod is an anchor slot with no running
// Pod behind it. Reclaiming lowers only the entry count and removes no running Pod, so it costs no
// availability. All old anchors are reclaimed first, in the order entries appear, because a reclaimed slot
// moves to the current anchor regardless of which old anchor it came from.
//
// Phase 2 takes down running Pods, oldest anchor first, so the oldest generation retires before a newer one.
//
// Why reclaim before takedown. The MaxUnavailable gate credits the sub-step for reclaiming missing
// old-version Pods for free. If the drain instead took down running Pods first and left missing old-version
// Pods in place, the gate credit would not match what actually happened and the budget could be breached.
//
// Example. Two old anchors of one PodClique during back-to-back updates. Drain 2.
//
//	A  count 2  running 2
//	B  count 2  running 1   (1 missing old-version Pod)
//	Phase 1 reclaims B's missing old-version Pod. B count 2 to 1. remaining 1.
//	Phase 2 takes down oldest first. A count 2 to 1 (1 running Pod removed). remaining 0.
//
// Result. 1 dead on B plus 1 removed on A is 2 unavailable, within a budget of 2. If Phase 2 ran over
// everything oldest first it would remove 2 running Pods on A and leave B's missing old-version Pod in
// place, giving 3 unavailable.
//
// runningPodsByCliqueAndAnchor gives the running Pod count per anchor epoch, so the split knows which slots
// are missing old-version Pods. A nil map treats every slot as one, which drains the same total from the
// same anchors as a plain oldest-first drain. The caller sorts entries oldest first, which both phases
// rely on.
func drainStandalonePCLQs(entries []grovecorev1alpha1.PodGangEntry, currentHash string, drainCounts map[string]int32, runningPodsByCliqueAndAnchor map[string]map[string]int32) {
	for cliqueName, remaining := range drainCounts {
		runningPodsByAnchor := runningPodsByCliqueAndAnchor[cliqueName]
		// Phase 1. Reclaim missing old-version Pods. Each reclaim lowers only the entry count, removing no running Pod.
		for i := range entries {
			if remaining == 0 {
				break
			}
			if !isOldHashAnchor(entries[i], currentHash) {
				continue
			}
			anchorPodCount, ok := entries[i].PodCliques[cliqueName]
			if !ok {
				continue
			}
			missingOldVersionPods := anchorPodCount - runningPodsByAnchor[entries[i].Epoch]
			if missingOldVersionPods <= 0 {
				continue
			}
			take := min(missingOldVersionPods, remaining)
			entries[i].PodCliques[cliqueName] -= take
			remaining -= take
		}
		// Phase 2. Take down running Pods, oldest anchor first. Every take here removes a running Pod.
		for i := range entries {
			if remaining == 0 {
				break
			}
			if !isOldHashAnchor(entries[i], currentHash) {
				continue
			}
			if anchorPodCount, ok := entries[i].PodCliques[cliqueName]; ok {
				take := min(anchorPodCount, remaining)
				entries[i].PodCliques[cliqueName] -= take
				remaining -= take
			}
		}
	}
}

// isOldHashAnchor reports whether the entry is an anchor at a generation other than the current one.
func isOldHashAnchor(entry grovecorev1alpha1.PodGangEntry, currentHash string) bool {
	return entry.Role == grovecorev1alpha1.PodGangEntryRoleAnchor && entry.PodCliqueSetGenerationHash != currentHash
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
// anchor entry carrying MinAvailable of every standalone PodClique plus the sub-step's PCSG MinAvailable indices. A
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

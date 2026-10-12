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

	drainStandalonePCLQs(entries, currentHash, ss.drainStandalonePCLQCounts, p.pclqPodCounts.livePodCountsByAnchor)
	drainPCSGIndices(entries, currentHash, ss.drainPCSGReplicaIndices)
	subsumeIntoAnchor(entries, ss.subsumeAnchorEpoch, ss.subsumeStandalonePCLQCounts)

	if newEntry, ok := p.newHashEntryForSubStep(ss); ok {
		entries = append(entries, newEntry)
	}

	return removeEmptyEntries(entries, currentHash), nil
}

// drainStandalonePCLQs removes each standalone PodClique take-down count from the old-version anchor entries.
// It runs in phases per PodClique, and the phase order keeps the MaxUnavailable accounting exact.
//
// Phase 1 reclaims missing old-version Pods. A missing old-version Pod is an anchor slot with no running
// Pod behind it. Reclaiming lowers only the entry count and removes no running Pod, so it costs no
// availability. All old anchors are reclaimed first, in the order entries appear, because a reclaimed slot
// moves to the current anchor regardless of which old anchor it came from.
//
// Phase 2a takes down not-Ready Pods first, across all old anchors. A standalone PodClique maps to one
// anchor PodGang per live generation, and the PodClique reconciler's DeletionSorter picks which Pod to
// delete only within a single PodGang. The choice of which anchor PodGang to shrink is made here. Spending a
// slot on an anchor of only Ready Pods, while an unavailable Pod survives on another old anchor, would drop
// availability below the floor and breach MaxUnavailable. Taking the not-Ready Pods down first keeps every
// slot on an already-unavailable Pod.
//
// Phase 2b takes down the remaining running Pods, oldest anchor first, so the oldest generation retires
// before a newer one. Only Ready Pods remain to take down by this phase.
//
// Why reclaim before takedown. The MaxUnavailable gate does not count a missing old-version Pod against the
// budget, since reclaiming it removes no running Pod. The gate therefore assumes the drain reclaims those
// slots first. If the drain instead took running Pods down first and left the missing slots in place, it
// would remove more running Pods than the gate allowed for and could breach MaxUnavailable.
//
// Example, reclaim before takedown. Two old anchors of one PodClique during back-to-back updates. Drain 2.
//
//	A  count 2  running 2
//	B  count 2  running 1   (1 missing old-version Pod)
//	Phase 1 reclaims B's missing old-version Pod. B count 2 to 1. remaining 1.
//	Phase 2b takes down oldest first. A count 2 to 1 (1 running Pod removed). remaining 0.
//
// Result. 1 dead on B plus 1 removed on A is 2 unavailable, within a budget of 2.
//
// Example, not-Ready first. Two old anchors, desired 3, MaxUnavailable 1, so the budget grants 1. Drain 1.
//
//	A  count 2  running 2 Ready
//	B  count 1  running 1 not-Ready
//	Phase 2a takes the not-Ready Pod on B down. B count 1 to 0. remaining 0. A keeps its 2 Ready Pods.
//
// Result. Availability stays at the floor desired - MaxUnavailable, which is 2. Spending the slot on A would
// leave B's unavailable Pod and drop availability to 1, breaching MaxUnavailable.
//
// livePodCountsByAnchor gives the running and not-Ready Pod count per clique and anchor epoch. Phase 1 reads
// running to find missing old-version slots, and Phase 2a reads notReady to drain unavailable Pods first. A
// nil map leaves those phases with nothing to find, which drains the same total from the same anchors as a
// plain oldest-first drain. The caller sorts entries oldest first, which every phase relies on.
func drainStandalonePCLQs(entries []grovecorev1alpha1.PodGangEntry, currentHash string, drainCounts map[string]int32, livePodCountsByAnchor map[cliqueAndEpochKey]anchorLivePods) {
	for cliqueName, remaining := range drainCounts {
		// Phase 1. Reclaim missing old-version Pods (removes no running Pod, costs no availability).
		remaining = drainOldHashAnchors(entries, currentHash, cliqueName, livePodCountsByAnchor, remaining, missingOldVersionPods)
		// Phase 2a. Take down not-Ready Pods first, across old anchors, so a slot never shrinks an anchor of
		// only Ready Pods while an unavailable Pod survives on another old anchor.
		remaining = drainOldHashAnchors(entries, currentHash, cliqueName, livePodCountsByAnchor, remaining, notReadyPods)
		// Phase 2b. Take down the remaining running Pods, oldest anchor first. Only Ready Pods remain here, and
		// nothing reads remaining after this phase, so its return is not kept.
		drainOldHashAnchors(entries, currentHash, cliqueName, livePodCountsByAnchor, remaining, allCommittedPods)
	}
}

// perAnchorTakeable reports how many Pods a drain phase may take down from one old-hash anchor, given the
// clique's committed Pod count on that anchor and the live Pods observed there.
type perAnchorTakeable func(committedPodCount int32, livePods anchorLivePods) int32

// missingOldVersionPods is the perAnchorTakeable for Phase 1, the committed slots with no running Pod behind them.
func missingOldVersionPods(committedPodCount int32, livePods anchorLivePods) int32 {
	return committedPodCount - livePods.running
}

// notReadyPods is the perAnchorTakeable for Phase 2a, the not-Ready running Pods on an anchor.
func notReadyPods(_ int32, livePods anchorLivePods) int32 {
	return livePods.notReady
}

// allCommittedPods is the perAnchorTakeable for Phase 2b, every Pod the anchor still commits.
func allCommittedPods(committedPodCount int32, _ anchorLivePods) int32 {
	return committedPodCount
}

// drainOldHashAnchors walks the old-hash anchors in the order entries are given (the caller sorts them
// oldest first) and takes down up to remaining Pods of cliqueName. From each anchor it takes what takeable
// reports, clamped to the Pods the anchor still commits and to the budget left. It returns the still
// undrained remainder.
func drainOldHashAnchors(entries []grovecorev1alpha1.PodGangEntry, currentHash, cliqueName string, livePodCountsByAnchor map[cliqueAndEpochKey]anchorLivePods, remaining int32, takeable perAnchorTakeable) int32 {
	for i := range entries {
		if remaining == 0 {
			break
		}
		if !isOldHashAnchor(entries[i], currentHash) {
			continue
		}
		committedPodCount, ok := entries[i].PodCliques[cliqueName]
		if !ok {
			continue
		}
		livePods := livePodCountsByAnchor[cliqueAndEpochKey{clique: cliqueName, epoch: entries[i].Epoch}]
		take := min(takeable(committedPodCount, livePods), committedPodCount, remaining)
		if take <= 0 {
			continue
		}
		entries[i].PodCliques[cliqueName] -= take
		remaining -= take
	}
	return remaining
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

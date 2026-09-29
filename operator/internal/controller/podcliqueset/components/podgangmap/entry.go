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
	"cmp"
	"fmt"
	"slices"
	"strconv"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
)

// newPodGangEntry constructs a fresh PodGangEntry setting epoch, PodCliqueSet generation hash and
// dependsOn. The caller sets Role, and AnchorIndex on an anchor entry, after this returns. An entry
// carries no name or labels. The PodGang materializer derives the name and stamps the epoch and role
// labels.
func newPodGangEntry(epoch, pcsGenerationHash string, dependsOn []string) grovecorev1alpha1.PodGangEntry {
	return grovecorev1alpha1.PodGangEntry{
		Epoch:                      epoch,
		PodCliqueSetGenerationHash: pcsGenerationHash,
		DependsOn:                  dependsOn,
	}
}

// sortEntriesByEpoch sorts entries in place by epoch ascending. Epoch is a unix-nano string compared
// numerically, so ordering is correct regardless of digit width. It returns an error if any entry
// has a non-numeric epoch, a contract violation since Grove is the sole writer of epochs.
func sortEntriesByEpoch(entries []grovecorev1alpha1.PodGangEntry) error {
	type entryWithEpoch struct {
		entry grovecorev1alpha1.PodGangEntry
		epoch int64
	}
	paired := make([]entryWithEpoch, len(entries))
	for i := range entries {
		epoch, err := strconv.ParseInt(entries[i].Epoch, 10, 64)
		if err != nil {
			return fmt.Errorf("PodGangMap entry with epoch %q has a non-numeric epoch: %w", entries[i].Epoch, err)
		}
		paired[i] = entryWithEpoch{entry: entries[i], epoch: epoch}
	}
	slices.SortStableFunc(paired, func(a, b entryWithEpoch) int {
		return cmp.Compare(a.epoch, b.epoch)
	})
	for i := range paired {
		entries[i] = paired[i].entry
	}
	return nil
}

// isPodGangEntryEmpty reports whether an entry carries no standalone PodClique pods and no
// PodCliqueScalingGroup replica indices.
func isPodGangEntryEmpty(entry grovecorev1alpha1.PodGangEntry) bool {
	for _, count := range entry.PodCliques {
		if count > 0 {
			return false
		}
	}
	for _, indices := range entry.PCSGReplicaIndices {
		if len(indices) > 0 {
			return false
		}
	}
	return true
}

// advanceEntriesGenerationHash sets every entry's PodCliqueSetGenerationHash to
// pcsCurrentGenerationHash.
func advanceEntriesGenerationHash(entries []grovecorev1alpha1.PodGangEntry, pcsCurrentGenerationHash string) {
	for i := range entries {
		entries[i].PodCliqueSetGenerationHash = pcsCurrentGenerationHash
	}
}

// shouldAdvanceEntriesGenerationHash reports whether the PodGangMap entries should be advanced to the
// current PodCliqueSet generation hash. RollingRecreate and OnDelete both preserve the PodGangs and
// entries across an update, so every entry always belongs to the current generation. When any entry
// lags the current hash it is advanced so the anchor and scale-out entries stay matchable by
// CurrentGenerationHash.
// Coherent update is skipped. A coherent update creates new-generation entries and drains old-generation
// entries, so a PodGangMap deliberately holds entries for more than one generation hash at once.
// Advancing all entries to the current hash would erase that distinction.
func shouldAdvanceEntriesGenerationHash(pcs *grovecorev1alpha1.PodCliqueSet, entries []grovecorev1alpha1.PodGangEntry) bool {
	if pcs.Spec.UpdateStrategy != nil && pcs.Spec.UpdateStrategy.Type == grovecorev1alpha1.CoherentStrategy {
		return false
	}
	currentHash := *pcs.Status.CurrentGenerationHash
	for i := range entries {
		if entries[i].PodCliqueSetGenerationHash != currentHash {
			return true
		}
	}
	return false
}

// clonePodGangEntries returns a deep copy of the entries so the caller can mutate without aliasing
// the source (typically the snapshot's PodGangMap spec).
func clonePodGangEntries(entries []grovecorev1alpha1.PodGangEntry) []grovecorev1alpha1.PodGangEntry {
	cloned := make([]grovecorev1alpha1.PodGangEntry, len(entries))
	for i := range entries {
		entries[i].DeepCopyInto(&cloned[i])
	}
	return cloned
}

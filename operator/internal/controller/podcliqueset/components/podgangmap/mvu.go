// /*
// Copyright 2025 The Grove Authors.
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

package podgangmap

import (
	"fmt"
	"maps"
	"slices"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
)

// mvuTemplate describes the composition of one MVU PodGang.
// It is computed once at update start and remains fixed for the duration of the update.
type mvuTemplate struct {
	// standalonePCLQs maps updated standalone PCLQ name → minAvailable pod count.
	standalonePCLQs map[string]int32
	// pcsgs maps updated PCSG name → minAvailable replica count.
	pcsgs map[string]int32
}

// computeMVUTemplate builds the MVU template from the snapshot of out-of-date components captured
// on PCS.Status.UpdateProgress at update start. The snapshot is frozen for the lifetime of the
// update so the template stays stable as PCLQs roll over to the new hash. MinAvailable values are
// looked up live from PCS.Spec.Template — the webhook prevents MinAvailable changes mid-update,
// so the spec values are the same as they were at update start.
func computeMVUTemplate(pcs *grovecorev1alpha1.PodCliqueSet) (*mvuTemplate, error) {
	progress := pcs.Status.UpdateProgress
	if progress == nil {
		return nil, fmt.Errorf("UpdateProgress is unset on PodCliqueSet %s; cannot derive MVU template", pcs.Name)
	}

	standalone := make(map[string]int32, len(progress.UpdatedStandalonePodCliques))
	for _, name := range progress.UpdatedStandalonePodCliques {
		template := componentutils.FindPodCliqueTemplateSpecByName(pcs, name)
		if template == nil {
			return nil, fmt.Errorf("standalone PodClique %q in UpdateProgress.UpdatedStandalonePodCliques is no longer in PCS %s spec", name, pcs.Name)
		}
		standalone[name] = *template.Spec.MinAvailable
	}

	pcsgs := make(map[string]int32, len(progress.UpdatedPodCliqueScalingGroups))
	for _, name := range progress.UpdatedPodCliqueScalingGroups {
		pcsgConfig := componentutils.FindScalingGroupConfigByName(pcs, name)
		if pcsgConfig == nil {
			return nil, fmt.Errorf("PodCliqueScalingGroup %q in UpdateProgress.UpdatedPodCliqueScalingGroups is no longer in PCS %s spec", name, pcs.Name)
		}
		pcsgs[name] = *pcsgConfig.MinAvailable
	}

	return &mvuTemplate{standalonePCLQs: standalone, pcsgs: pcsgs}, nil
}

// podGangMapState captures the state of the PodGangMap after a computation step:
// the updated old entries (with decremented counts) and newly introduced entries.
type podGangMapState struct {
	// oldEntries are existing old-hash PodGang entries with decremented pod/replica counts.
	// Entries that have reached zero across all counts are removed.
	oldEntries []grovecorev1alpha1.PodGangEntry
	// newEntries are the newly computed MVU PodGang or Tail-PG entries for this iteration.
	newEntries []grovecorev1alpha1.PodGangEntry
	// done is true when there are no remaining old pods/replicas to process.
	done bool
}

// computeNextPodGangMapState determines the next state of the PodGangMap for the current
// coherent update. It checks if a full MVU PodGang can be formed from the pods and
// replicas available in the old-hash entries. If yes, it produces exactly one MVU PodGang entry
// (absorbing leftover standalone PCLQ pods if no further MVU can be formed after it). If a full
// MVU cannot be formed, it produces one Tail-PG entry per remaining PCSG replica.
// It deducts the allocated pods/replicas from old entries (lowest-indexed first) and removes
// old entries that reach zero.
//
// Parameters:
//   - template: the fixed MVU template for this update
//   - oldEntries: existing old-hash PodGang entries ordered by index (lowest first)
//   - mpgNames: names of MVU PodGangs already created in previous iterations of this update
//     (Tail-PGs created in the current iteration depend on them)
//   - entryBuilder: a function that creates a PodGangEntry given the composition
func computeNextPodGangMapState(
	template mvuTemplate,
	oldEntries []grovecorev1alpha1.PodGangEntry,
	mpgNames []string,
	entryBuilder PodGangEntryBuilder,
) podGangMapState {
	if canFormMVUPodGang(template, oldEntries) {
		updatedOldEntries, newEntries := computeNextMVUPodGang(template, oldEntries, entryBuilder)
		return podGangMapState{oldEntries: updatedOldEntries, newEntries: newEntries, done: false}
	}
	// Only Tail-PGs remain. Create PodGangEntries for Tail-PGs.
	updatedOldEntries, newEntries, done := computeTailPodGangs(template, oldEntries, mpgNames, entryBuilder)
	return podGangMapState{oldEntries: updatedOldEntries, newEntries: newEntries, done: done}
}

// canFormMVUPodGang checks whether the old entries collectively have enough pods and replicas
// to fill a complete MVU.
func canFormMVUPodGang(template mvuTemplate, oldEntries []grovecorev1alpha1.PodGangEntry) bool {
	for pclqName, minAvailable := range template.standalonePCLQs {
		if sumPCLQPodsInEntries(oldEntries, pclqName) < minAvailable {
			return false
		}
	}
	for pcsgName, minAvailable := range template.pcsgs {
		if sumPCSGReplicasInEntries(oldEntries, pcsgName) < minAvailable {
			return false
		}
	}
	return true
}

// computeNextMVUPodGang computes the next MVU PodGang entry and deducts the allocated
// pods/replicas from old entries (lowest-indexed first). If another MVU cannot be formed
// after this one, all remaining standalone PCLQ pods are absorbed into this MVU.
func computeNextMVUPodGang(
	template mvuTemplate,
	oldEntries []grovecorev1alpha1.PodGangEntry,
	entryBuilder PodGangEntryBuilder,
) (updatedOldEntries []grovecorev1alpha1.PodGangEntry, newEntries []grovecorev1alpha1.PodGangEntry) {
	// Clone standalone PCLQ counts from template — may grow if absorption happens.
	nextMVUStandalonePCLQs := maps.Clone(template.standalonePCLQs)

	// Deduct standalone PCLQ pods from old entries (lowest index first).
	for pclqName, needed := range template.standalonePCLQs {
		oldEntries = deductPCLQPodsFromOldEntries(oldEntries, pclqName, needed)
	}

	// Pop PCSG replica indices from old entries (lowest index first). The returned slice is
	// the set of indices absorbed into this MVU PodGang.
	nextMVUPCSGIndices := make(map[string][]int32, len(template.pcsgs))
	for pcsgName, needed := range template.pcsgs {
		var taken []int32
		oldEntries, taken = popPCSGIndicesFromOldEntries(oldEntries, pcsgName, needed)
		nextMVUPCSGIndices[pcsgName] = taken
	}

	// Check if another full MVU can be formed after this deduction.
	if !canFormMVUPodGang(template, oldEntries) {
		// Absorb ALL remaining standalone PCLQ pods into this MVU PodGang.
		// PCSG replicas are NOT absorbed — they remain for Tail-PGs.
		for pclqName := range template.standalonePCLQs {
			remaining := sumPCLQPodsInEntries(oldEntries, pclqName)
			if remaining > 0 {
				nextMVUStandalonePCLQs[pclqName] += remaining
				oldEntries = deductPCLQPodsFromOldEntries(oldEntries, pclqName, remaining)
			}
		}
	}

	// Remove old entries that have no pods and no replicas left.
	updatedOldEntries = removeEmptyEntries(oldEntries)
	newEntries = []grovecorev1alpha1.PodGangEntry{entryBuilder(nextMVUStandalonePCLQs, nextMVUPCSGIndices, nil)}
	return
}

// computeTailPodGangs creates one Tail-PG per remaining PCSG replica in the old entries.
// Pops indices from old entries (lowest-indexed first). Each Tail-PG depends on the
// supplied MVU PodGang names and carries exactly one PCSG replica index.
func computeTailPodGangs(
	template mvuTemplate,
	oldEntries []grovecorev1alpha1.PodGangEntry,
	mpgNames []string,
	entryBuilder PodGangEntryBuilder,
) (updatedOldEntries []grovecorev1alpha1.PodGangEntry, newEntries []grovecorev1alpha1.PodGangEntry, done bool) {
	for pcsgName := range template.pcsgs {
		remaining := sumPCSGReplicasInEntries(oldEntries, pcsgName)
		for range remaining {
			var taken []int32
			oldEntries, taken = popPCSGIndicesFromOldEntries(oldEntries, pcsgName, 1)
			newEntries = append(newEntries, entryBuilder(nil, map[string][]int32{pcsgName: taken}, mpgNames))
		}
	}

	updatedOldEntries = removeEmptyEntries(oldEntries)
	if len(newEntries) == 0 {
		return updatedOldEntries, nil, true
	}
	return updatedOldEntries, newEntries, false
}

// sumPCLQPodsInEntries sums the pod count for a given standalone PCLQ across all entries.
func sumPCLQPodsInEntries(entries []grovecorev1alpha1.PodGangEntry, pclqName string) int32 {
	var total int32
	for _, entry := range entries {
		total += entry.PodCliques[pclqName]
	}
	return total
}

// sumPCSGReplicasInEntries sums the replica count for a given PCSG across all entries.
// Computed as the total length of index slices since each index is exactly one replica.
func sumPCSGReplicasInEntries(entries []grovecorev1alpha1.PodGangEntry, pcsgName string) int32 {
	var total int32
	for _, entry := range entries {
		total += int32(len(entry.PCSGReplicaIndices[pcsgName]))
	}
	return total
}

// deductPCLQPodsFromOldEntries deducts the specified number of pods for a PCLQ from old
// entries, starting from the lowest-indexed entry.
func deductPCLQPodsFromOldEntries(entries []grovecorev1alpha1.PodGangEntry, pclqName string, toDeduct int32) []grovecorev1alpha1.PodGangEntry {
	for i := range entries {
		available := entries[i].PodCliques[pclqName]
		if available <= 0 {
			continue
		}
		take := min(available, toDeduct)
		entries[i].PodCliques[pclqName] -= take
		toDeduct -= take
		if toDeduct == 0 {
			break
		}
	}
	return entries
}

// popPCSGIndicesFromOldEntries pops the smallest `count` PCSG replica indices for the given
// PCSG across `entries`, walking entries in slice order. It mutates each entry's index slice
// in place, removing the smallest indices first within an entry. The two-level traversal —
// entries in order, smallest index within each — preserves the existing "lowest first"
// deterministic deduction order.
//
// Returns:
//   - mutatedEntries: the same `entries` slice with the popped indices removed from each
//     entry's PCSGReplicaIndices[pcsgName]. Returned for call-site clarity; the input slice
//     is mutated in place.
//   - takenIndices: the union of indices popped across all entries, sorted ascending. Length
//     is min(count, total available). The caller assigns this to a new entry's
//     PCSGReplicaIndices[pcsgName] so the popped replicas land in their new home.
func popPCSGIndicesFromOldEntries(entries []grovecorev1alpha1.PodGangEntry, pcsgName string, count int32) (mutatedEntries []grovecorev1alpha1.PodGangEntry, takenIndices []int32) {
	takenIndices = make([]int32, 0, count)
	for i := range entries {
		if count == 0 {
			break
		}
		available := entries[i].PCSGReplicaIndices[pcsgName]
		if len(available) == 0 {
			continue
		}
		sorted := append([]int32(nil), available...)
		slices.Sort(sorted)
		take := int32(min(int(count), len(sorted)))
		takenIndices = append(takenIndices, sorted[:take]...)
		entries[i].PCSGReplicaIndices[pcsgName] = sorted[take:]
		count -= take
	}
	slices.Sort(takenIndices)
	mutatedEntries = entries
	return
}

// removeEmptyEntries removes entries where all PodClique pod counts are zero and all
// PCSG index slices are empty.
func removeEmptyEntries(entries []grovecorev1alpha1.PodGangEntry) []grovecorev1alpha1.PodGangEntry {
	return slices.DeleteFunc(entries, func(entry grovecorev1alpha1.PodGangEntry) bool {
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
	})
}

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
	"slices"
	"sort"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/samber/lo"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
)

// buildBootstrapEntries builds the initial PodGangMap entries for a PCS replica from the PCS spec. It
// produces an anchor entry with every standalone PodClique and each PodCliqueScalingGroup's
// MinAvailable replicas, a Tail entry for PodCliqueScalingGroup replicas above MinAvailable, and, when
// the PodCliqueSet has a PodCliqueScalingGroup, an empty ScaleOut entry that later scale-outs attach
// to. Each entry reuses the epoch its existing PodGangs carry. It assigns a new epoch for a role that
// has no existing PodGang, or for every role when there is no anchor PodGang to reuse.
func buildBootstrapEntries(pcs *grovecorev1alpha1.PodCliqueSet, clk clock.Clock, existingPodGangs []groveschedulerv1alpha1.PodGang) []grovecorev1alpha1.PodGangEntry {
	epochByRole := epochByRoleFromPodGangs(existingPodGangs)
	now := clk.Now().UnixNano()

	var anchorEpoch, tailEpoch, scaleOutEpoch int64
	if adopted, ok := epochByRole[grovecorev1alpha1.PodGangEntryRoleAnchor]; ok {
		anchorEpoch = adopted
		tailEpoch = epochOrDefault(epochByRole, grovecorev1alpha1.PodGangEntryRoleTail, anchorEpoch+1)
		scaleOutEpoch = epochOrDefault(epochByRole, grovecorev1alpha1.PodGangEntryRoleScaleOut, anchorEpoch+2)
	} else {
		anchorEpoch = now
		tailEpoch = now + 1
		scaleOutEpoch = now + 2
	}

	entries := make([]grovecorev1alpha1.PodGangEntry, 0, 3)
	entries = append(entries, buildBootstrapAnchorEntry(pcs, strconv.FormatInt(anchorEpoch, 10)))
	if tailEntry, ok := buildBootstrapTailEntry(pcs, strconv.FormatInt(tailEpoch, 10), strconv.FormatInt(anchorEpoch, 10)); ok {
		entries = append(entries, tailEntry)
	}
	entries = ensureScaleOutEntry(entries, pcs, strconv.FormatInt(scaleOutEpoch, 10), nil)

	return entries
}

// epochByRoleFromPodGangs returns the epoch each role's PodGangs carry, keyed by role, from the
// grove.io/podgang-role and grove.io/epoch labels. PodGangs of one role share an epoch, so the first
// value seen for a role is used. A role whose PodGangs carry no epoch, and a role with no PodGang, are
// omitted from the returned map.
func epochByRoleFromPodGangs(existingPodGangs []groveschedulerv1alpha1.PodGang) map[grovecorev1alpha1.PodGangEntryRole]int64 {
	byRole := make(map[grovecorev1alpha1.PodGangEntryRole]int64)
	for i := range existingPodGangs {
		labels := existingPodGangs[i].Labels
		epochStr, hasEpoch := labels[apicommon.LabelEpoch]
		role, hasRole := labels[apicommon.LabelPodGangRole]
		if !hasEpoch || !hasRole {
			continue
		}
		epoch, err := strconv.ParseInt(epochStr, 10, 64)
		if err != nil {
			continue
		}
		if _, seen := byRole[grovecorev1alpha1.PodGangEntryRole(role)]; !seen {
			byRole[grovecorev1alpha1.PodGangEntryRole(role)] = epoch
		}
	}
	return byRole
}

// epochOrDefault returns the epoch for the role, or defaultEpoch when the role is absent.
func epochOrDefault(epochByRole map[grovecorev1alpha1.PodGangEntryRole]int64, role grovecorev1alpha1.PodGangEntryRole, defaultEpoch int64) int64 {
	if epoch, ok := epochByRole[role]; ok {
		return epoch
	}
	return defaultEpoch
}

// buildBootstrapAnchorEntry returns the anchor entry carrying every standalone PodClique's full
// Replicas count and every PodCliqueScalingGroup's MinAvailable replicas (PCSG indices
// [0, MinAvailable)). DependsOn is nil.
func buildBootstrapAnchorEntry(pcs *grovecorev1alpha1.PodCliqueSet, epoch string) grovecorev1alpha1.PodGangEntry {
	entry := newPodGangEntry(epoch, *pcs.Status.CurrentGenerationHash, nil)
	entry.Role = grovecorev1alpha1.PodGangEntryRoleAnchor
	// A bootstrap PodGangMap has a single anchor, whose index is 0.
	entry.AnchorIndex = ptr.To[int32](0)
	entry.PodCliques = componentutils.GetStandalonePCLQReplicasFromPCSTemplateSpec(pcs)

	pcsgMinAvailable := componentutils.GetPCSGMinAvailableFromPCSTemplateSpec(pcs)
	entry.PCSGReplicaIndices = make(map[string][]int32, len(pcsgMinAvailable))
	for name, minAvailable := range pcsgMinAvailable {
		entry.PCSGReplicaIndices[name] = lo.RangeFrom[int32](0, int(minAvailable))
	}
	return entry
}

// buildBootstrapTailEntry returns a single Tail entry for a fresh PCS replica. The entry aggregates,
// across all PodCliqueScalingGroups, each PodCliqueScalingGroup's replica indices above MinAvailable
// into a single entry. All PodCliqueScalingGroups and their indices share the same epoch value and
// depend on the anchor epoch. The PodGang materializer expands this entry into one PodGang per
// (PodCliqueScalingGroup, index). It returns false when no PodCliqueScalingGroup has replicas above
// MinAvailable.
func buildBootstrapTailEntry(pcs *grovecorev1alpha1.PodCliqueSet, epoch, anchorEpoch string) (grovecorev1alpha1.PodGangEntry, bool) {
	pcsgReplicaIndices := make(map[string][]int32)
	for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		replicas := *pcsgConfig.Replicas
		minAvailable := *pcsgConfig.MinAvailable
		if replicas == minAvailable {
			continue
		}
		pcsgReplicaIndices[pcsgConfig.Name] = lo.RangeFrom(minAvailable, int(replicas-minAvailable))
	}
	if len(pcsgReplicaIndices) == 0 {
		return grovecorev1alpha1.PodGangEntry{}, false
	}
	entry := newPodGangEntry(epoch, *pcs.Status.CurrentGenerationHash, []string{anchorEpoch})
	entry.Role = grovecorev1alpha1.PodGangEntryRoleTail
	entry.PCSGReplicaIndices = pcsgReplicaIndices
	return entry, true
}

// reconcileEntries re-authors the entries of a PCS replica whose PodGangMap already has entries. It runs
// in steady state (no update in progress) and while a RollingRecreate is in progress.
//
// Each entry keeps its identity (epoch, role, DependsOn, anchor index) and its already-placed replica
// indices. Placement is not recomputed from the template. A template Replicas change does not reach an
// existing PodClique or PodCliqueScalingGroup, whose Spec.Replicas is set only at creation and changed
// only by external scaling.
//
// For each PodCliqueScalingGroup the count of its placed indices is diffed against its live
// Spec.Replicas. A scale-out appends new indices to the ScaleOut entry. A scale-in drains indices in
// role order ScaleOut, Tail, Anchor. Each standalone PodClique pod count on the anchor is set from its
// live Spec.Replicas. A ScaleOut entry is ensured and empty entries are dropped.
func reconcileEntries(pcs *grovecorev1alpha1.PodCliqueSet,
	entries []grovecorev1alpha1.PodGangEntry,
	standalonePCLQs []grovecorev1alpha1.PodClique,
	pcsgs []grovecorev1alpha1.PodCliqueScalingGroup,
	pcsReplicaIndex int,
	scaleOutEpoch string) ([]grovecorev1alpha1.PodGangEntry, error) {
	refreshStandalonePodCliqueCounts(entries, pcs, standalonePCLQs, pcsReplicaIndex)
	if err := reconcilePCSGReplicaIndices(entries, pcs, pcsgs, pcsReplicaIndex); err != nil {
		return nil, err
	}
	entries = ensureScaleOutEntry(entries, pcs, scaleOutEpoch, nil)
	return removeEmptyEntries(entries, *pcs.Status.CurrentGenerationHash), nil
}

// refreshStandalonePodCliqueCounts sets each standalone PodClique's pod count on the anchor entry to
// its live Spec.Replicas. A standalone PodClique always belongs to the anchor entry, and clique
// composition is immutable on update, so the anchor already carries a key for every standalone
// PodClique in the PCS spec. The count is updated only when the key exists.
// NOTE: this refreshes the single AnchorIndex 0 anchor. When coherent updates land there can be more
// than one anchor post-update, and this will need to select the right anchor per PodClique.
func refreshStandalonePodCliqueCounts(entries []grovecorev1alpha1.PodGangEntry, pcs *grovecorev1alpha1.PodCliqueSet, standalonePCLQs []grovecorev1alpha1.PodClique, pcsReplicaIndex int) {
	anchor := currentGenerationAnchor(entries, *pcs.Status.CurrentGenerationHash)
	if anchor == nil {
		return
	}
	rnr := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}
	for i := range standalonePCLQs {
		cliqueName := apicommon.ExtractPodCliqueNameFromStandalonePCLQFQN(standalonePCLQs[i].Name, rnr)
		if _, ok := anchor.PodCliques[cliqueName]; ok {
			anchor.PodCliques[cliqueName] = standalonePCLQs[i].Spec.Replicas
		}
	}
}

// reconcilePCSGReplicaIndices diffs each PodCliqueScalingGroup's replica-index count across all
// entries against its live Spec.Replicas and appends (scale-out) or drains (scale-in) accordingly.
func reconcilePCSGReplicaIndices(entries []grovecorev1alpha1.PodGangEntry, pcs *grovecorev1alpha1.PodCliqueSet, pcsgs []grovecorev1alpha1.PodCliqueScalingGroup, pcsReplicaIndex int) error {
	rnr := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}
	for i := range pcsgs {
		pcsgConfigName, err := apicommon.ExtractScalingGroupNameFromPCSGFQN(pcsgs[i].Name, rnr)
		if err != nil {
			return err
		}
		currentCount := countPCSGReplicaIndices(entries, pcsgConfigName)
		diff := int(pcsgs[i].Spec.Replicas) - currentCount
		switch {
		case diff > 0:
			appendScaleOutReplicaIndices(entries, pcsgConfigName, lo.RangeFrom[int32](int32(currentCount), diff))
		case diff < 0:
			drainReplicaIndicesForScaleIn(entries, pcsgConfigName, -diff)
		}
	}
	return nil
}

// countPCSGReplicaIndices returns the total number of the given PodCliqueScalingGroup's replica
// indices held across all entries.
func countPCSGReplicaIndices(entries []grovecorev1alpha1.PodGangEntry, pcsgConfigName string) int {
	count := 0
	for i := range entries {
		count += len(entries[i].PCSGReplicaIndices[pcsgConfigName])
	}
	return count
}

// appendScaleOutReplicaIndices appends the given PodCliqueScalingGroup replica indices to the
// ScaleOut entry. The ScaleOut entry is pre-created by ensureScaleOutEntry, so it is expected to
// exist here.
func appendScaleOutReplicaIndices(entries []grovecorev1alpha1.PodGangEntry, pcsgConfigName string, indices []int32) {
	for i := range entries {
		if entries[i].Role == grovecorev1alpha1.PodGangEntryRoleScaleOut {
			if entries[i].PCSGReplicaIndices == nil {
				entries[i].PCSGReplicaIndices = make(map[string][]int32)
			}
			entries[i].PCSGReplicaIndices[pcsgConfigName] = append(entries[i].PCSGReplicaIndices[pcsgConfigName], indices...)
			slices.Sort(entries[i].PCSGReplicaIndices[pcsgConfigName])
			return
		}
	}
}

// drainReplicaIndicesForScaleIn removes count of the given PodCliqueScalingGroup's replica indices,
// draining in role order ScaleOut, then Tail, then Anchor (highest AnchorIndex first, AnchorIndex 0
// last), and the highest index first within a chosen entry. The webhook guarantees Spec.Replicas
// stays at or above MinAvailable, so the anchor's MinAvailable indices are never drained.
func drainReplicaIndicesForScaleIn(entries []grovecorev1alpha1.PodGangEntry, pcsgConfigName string, count int) {
	order := make([]int, 0, len(entries))
	for i := range entries {
		if len(entries[i].PCSGReplicaIndices[pcsgConfigName]) > 0 {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		ip, jp := drainPriority(entries[order[a]]), drainPriority(entries[order[b]])
		if ip != jp {
			return ip < jp
		}
		if entries[order[a]].Role == grovecorev1alpha1.PodGangEntryRoleAnchor {
			return *entries[order[a]].AnchorIndex > *entries[order[b]].AnchorIndex
		}
		return false
	})
	remaining := count
	for _, idx := range order {
		if remaining == 0 {
			break
		}
		s := entries[idx].PCSGReplicaIndices[pcsgConfigName]
		slices.Sort(s)
		take := min(remaining, len(s))
		entries[idx].PCSGReplicaIndices[pcsgConfigName] = s[:len(s)-take]
		remaining -= take
	}
}

// drainPriority orders entries for a scale-in drain: ScaleOut first, then Tail, then Anchor. Among
// anchors the caller's sort further orders the highest AnchorIndex first.
func drainPriority(entry grovecorev1alpha1.PodGangEntry) int {
	switch entry.Role {
	case grovecorev1alpha1.PodGangEntryRoleScaleOut:
		return 0
	case grovecorev1alpha1.PodGangEntryRoleTail:
		return 1
	default:
		return 2
	}
}

// currentGenerationAnchor returns a pointer to the AnchorIndex 0 anchor entry for the current
// generation, or nil when none is present.
func currentGenerationAnchor(entries []grovecorev1alpha1.PodGangEntry, currentHash string) *grovecorev1alpha1.PodGangEntry {
	for i := range entries {
		if entries[i].Role == grovecorev1alpha1.PodGangEntryRoleAnchor && entries[i].PodCliqueSetGenerationHash == currentHash && entries[i].AnchorIndex != nil && *entries[i].AnchorIndex == 0 {
			return &entries[i]
		}
	}
	return nil
}

// removeEmptyEntries drops entries that carry no pods and no replica indices. A ScaleOut entry for
// the CURRENT generation hash is exempt, it is pre-created empty and kept as the PodGangMap-owned
// scale-out epoch that steady-state scale-outs attach to, so it must persist even when it carries
// nothing. A ScaleOut entry for an OLD generation hash is not exempt, once drained it is a remnant of
// a superseded generation and is removed like any other empty entry.
func removeEmptyEntries(entries []grovecorev1alpha1.PodGangEntry, currentGenerationHash string) []grovecorev1alpha1.PodGangEntry {
	return slices.DeleteFunc(entries, func(entry grovecorev1alpha1.PodGangEntry) bool {
		if entry.Role == grovecorev1alpha1.PodGangEntryRoleScaleOut && entry.PodCliqueSetGenerationHash == currentGenerationHash {
			return false
		}
		return isPodGangEntryEmpty(entry)
	})
}

// ensureScaleOutEntry appends a ScaleOut entry to entries when the PodCliqueSet has any
// PodCliqueScalingGroup and no ScaleOut entry is present. The entry carries scaleOutIndices (empty
// when nothing has scaled out) and depends on the current-generation anchor, the AnchorIndex 0
// anchor. When a ScaleOut entry already exists, entries are returned unchanged. The AnchorIndex 0
// anchor of the current generation is always present in entries when this is called.
func ensureScaleOutEntry(entries []grovecorev1alpha1.PodGangEntry, pcs *grovecorev1alpha1.PodCliqueSet, scaleOutEpoch string, scaleOutIndices map[string][]int32) []grovecorev1alpha1.PodGangEntry {
	if len(pcs.Spec.Template.PodCliqueScalingGroupConfigs) == 0 {
		return entries
	}
	currentHash := *pcs.Status.CurrentGenerationHash
	var anchorEpoch string
	for i := range entries {
		if entries[i].Role == grovecorev1alpha1.PodGangEntryRoleScaleOut && entries[i].PodCliqueSetGenerationHash == currentHash {
			return entries
		}
		if entries[i].Role == grovecorev1alpha1.PodGangEntryRoleAnchor && entries[i].PodCliqueSetGenerationHash == currentHash && entries[i].AnchorIndex != nil && *entries[i].AnchorIndex == 0 {
			anchorEpoch = entries[i].Epoch
		}
	}
	scaleOut := newPodGangEntry(scaleOutEpoch, currentHash, []string{anchorEpoch})
	scaleOut.Role = grovecorev1alpha1.PodGangEntryRoleScaleOut
	if len(scaleOutIndices) > 0 {
		sortIndicesPerPCSG(scaleOutIndices)
		scaleOut.PCSGReplicaIndices = scaleOutIndices
	}
	return append(entries, scaleOut)
}

// sortIndicesPerPCSG sorts each PodCliqueScalingGroup's index slice ascending for deterministic output.
func sortIndicesPerPCSG(indicesByPCSG map[string][]int32) {
	for name := range indicesByPCSG {
		slices.Sort(indicesByPCSG[name])
	}
}

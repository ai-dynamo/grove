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
	"slices"
	"sort"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/samber/lo"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
)

// buildBootstrapEntries builds the initial PodGangMap entries for a PCS replica from the PCS spec. It
// produces an anchor entry with every standalone PodClique and each PodCliqueScalingGroup's
// MinAvailable replicas, and a Tail entry for PodCliqueScalingGroup replicas above MinAvailable. Each
// entry reuses the epoch its existing PodGangs carry. It assigns a new epoch for a role that has no
// existing PodGang, or for every role when there is no anchor PodGang to reuse. It returns the entries
// and the scale-out epoch, which the caller uses to author the ScaleOut entry.
func buildBootstrapEntries(clk clock.Clock, pcs *grovecorev1alpha1.PodCliqueSet, existingPodGangs []groveschedulerv1alpha1.PodGang) ([]grovecorev1alpha1.PodGangEntry, string) {
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

	return entries, strconv.FormatInt(scaleOutEpoch, 10)
}

// epochByRoleFromPodGangs returns the epoch each role's PodGangs carry, keyed by role, from the
// grove.io/podgang-role and grove.io/epoch labels. PodGangs of one role share an epoch, so the first
// value seen for a role is used. A role whose PodGangs carry no epoch, and a role with no PodGang, are
// omitted from the returned map.
func epochByRoleFromPodGangs(existingPodGangs []groveschedulerv1alpha1.PodGang) map[grovecorev1alpha1.PodGangEntryRole]int64 {
	byRole := make(map[grovecorev1alpha1.PodGangEntryRole]int64)
	for _, podGang := range existingPodGangs {
		labels := podGang.Labels
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

// reconcileEntries authors the desired PodGangMap entries for a PCS replica and returns them for create
// or patch. When no PodGangMap exists, or one exists with no entries, it starts from a fresh set of
// bootstrap entries, reusing the epoch the replica's existing PodGangs carry. An entry-less PodGangMap
// is bootstrapped the same way so a replica whose entries were all drained recovers instead of staying
// empty. When a PodGangMap with entries exists it starts from those entries, advancing them to the
// current generation hash unless a coherent update is in progress.
//
// Each entry keeps its identity (epoch, role, DependsOn, anchor index) and its already-placed replica
// indices. Placement is not recomputed from the template. A template Replicas change does not reach an
// existing PodClique or PodCliqueScalingGroup, whose Spec.Replicas is set only at creation and changed
// only by external scaling.
//
// For each PodCliqueScalingGroup the count of its placed indices is diffed against its live
// Spec.Replicas. A scale-out appends new indices to the ScaleOut entry. A scale-in drains indices in
// role order ScaleOut, Tail, Anchor. Each standalone PodClique pod count on the anchor is set from its
// live Spec.Replicas. A ScaleOut entry is ensured before the diff so scale-out indices have a place to
// land, and empty entries are dropped afterward.
//
// NOTE: this reconstructs a single-anchor PodGangMap. After a coherent update the steady-state
// PodGangMap has more than one anchor entry and more than one tail entry, with a single ScaleOut entry.
// buildBootstrapEntries produces a single anchor, and each entry's anchor index and DependsOn are held
// only on the PodGangMap and are not recoverable from the live PodGangs. Reconstructing a multi-anchor
// PodGangMap is deferred to the coherent update engine.
func reconcileEntries(clk clock.Clock,
	pcs *grovecorev1alpha1.PodCliqueSet,
	pcsReplicaIndex int,
	pgm *grovecorev1alpha1.PodGangMap,
	existingPodGangs []groveschedulerv1alpha1.PodGang,
	standalonePCLQs []grovecorev1alpha1.PodClique,
	pcsgs []grovecorev1alpha1.PodCliqueScalingGroup) ([]grovecorev1alpha1.PodGangEntry, error) {
	var (
		entries       []grovecorev1alpha1.PodGangEntry
		scaleOutEpoch string
	)
	if pgm == nil || len(pgm.Spec.Entries) == 0 {
		entries, scaleOutEpoch = buildBootstrapEntries(clk, pcs, existingPodGangs)
	} else {
		// Deep-copy the existing entries so mutations here do not alias the snapshot's PodGangMap.
		entries = clonePodGangEntries(pgm.Spec.Entries)
		if shouldAdvanceEntriesGenerationHash(pcs, entries) {
			advanceEntriesGenerationHash(entries, *pcs.Status.CurrentGenerationHash)
		}
	}

	refreshStandalonePodCliqueCounts(entries, pcs, standalonePCLQs, pcsReplicaIndex)
	entries = ensureScaleOutEntry(clk, entries, pcs, scaleOutEpoch)
	if err := reconcilePCSGReplicaIndices(entries, pcs, pcsgs, pcsReplicaIndex); err != nil {
		return nil, err
	}
	return removeEmptyEntries(entries, *pcs.Status.CurrentGenerationHash), nil
}

// refreshStandalonePodCliqueCounts updates the per-anchor pod counts of each standalone PodClique to
// match its live Spec.Replicas. Every anchor of the current generation carries a count for every
// standalone PodClique. This sums a PodClique's counts across those anchors and applies the
// difference from Spec.Replicas. A scale-out adds to the highest-AnchorIndex anchor. A scale-in
// drains the highest-AnchorIndex anchor first and moves to the next as each reaches zero. With one
// anchor this sets that anchor's count to Spec.Replicas.
func refreshStandalonePodCliqueCounts(entries []grovecorev1alpha1.PodGangEntry, pcs *grovecorev1alpha1.PodCliqueSet, standalonePCLQs []grovecorev1alpha1.PodClique, pcsReplicaIndex int) {
	anchorsHighestFirst := currentGenerationAnchorsByIndexDesc(entries, *pcs.Status.CurrentGenerationHash)
	if len(anchorsHighestFirst) == 0 {
		return
	}
	pcsRnr := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}
	for _, standalonePCLQ := range standalonePCLQs {
		cliqueName := apicommon.ExtractPodCliqueNameFromStandalonePCLQFQN(standalonePCLQ.Name, pcsRnr)
		reconcileStandaloneCliqueCountAcrossAnchors(anchorsHighestFirst, cliqueName, standalonePCLQ.Spec.Replicas)
	}
}

// reconcileStandaloneCliqueCountAcrossAnchors drives the clique's total pod count across the anchors
// (ordered highest AnchorIndex first) toward desiredTotal. A positive difference is added to the
// highest-AnchorIndex anchor. A negative difference drains the highest-AnchorIndex anchor first and
// moves to the next as each reaches zero.
func reconcileStandaloneCliqueCountAcrossAnchors(anchorsHighestFirst []*grovecorev1alpha1.PodGangEntry, cliqueName string, desiredTotal int32) {
	var currentTotal int32
	for _, anchor := range anchorsHighestFirst {
		currentTotal += anchor.PodCliques[cliqueName]
	}
	diff := desiredTotal - currentTotal
	switch {
	case diff > 0:
		anchorsHighestFirst[0].PodCliques[cliqueName] += diff
	case diff < 0:
		remaining := -diff
		for _, anchor := range anchorsHighestFirst {
			if remaining == 0 {
				return
			}
			take := min(remaining, anchor.PodCliques[cliqueName])
			anchor.PodCliques[cliqueName] -= take
			remaining -= take
		}
	}
}

// currentGenerationAnchorsByIndexDesc returns the current-generation anchor entries ordered by
// AnchorIndex descending (highest first).
func currentGenerationAnchorsByIndexDesc(entries []grovecorev1alpha1.PodGangEntry, currentHash string) []*grovecorev1alpha1.PodGangEntry {
	var anchors []*grovecorev1alpha1.PodGangEntry
	for i := range entries {
		e := &entries[i]
		if e.Role == grovecorev1alpha1.PodGangEntryRoleAnchor && e.PodCliqueSetGenerationHash == currentHash && e.AnchorIndex != nil {
			anchors = append(anchors, e)
		}
	}
	slices.SortFunc(anchors, func(a, b *grovecorev1alpha1.PodGangEntry) int {
		return cmp.Compare(*b.AnchorIndex, *a.AnchorIndex) // highest AnchorIndex first
	})
	return anchors
}

// reconcilePCSGReplicaIndices diffs each PodCliqueScalingGroup's replica-index count across all
// entries against its live Spec.Replicas and appends (scale-out) or drains (scale-in) accordingly.
func reconcilePCSGReplicaIndices(entries []grovecorev1alpha1.PodGangEntry, pcs *grovecorev1alpha1.PodCliqueSet, pcsgs []grovecorev1alpha1.PodCliqueScalingGroup, pcsReplicaIndex int) error {
	rnr := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}
	for _, pcsg := range pcsgs {
		pcsgConfigName, err := apicommon.ExtractScalingGroupNameFromPCSGFQN(pcsg.Name, rnr)
		if err != nil {
			return err
		}
		currentCount := countPCSGReplicaIndices(entries, pcsgConfigName)
		diff := int(pcsg.Spec.Replicas) - currentCount
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
	for _, entry := range entries {
		count += len(entry.PCSGReplicaIndices[pcsgConfigName])
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

// removeEmptyEntries drops entries that carry no pods and no replica indices. A ScaleOut entry of the
// current generation is kept even when empty, but only while a current-generation anchor entry
// survives. A ScaleOut entry depends on its generation's anchor, so once the last current-generation
// anchor drains to empty it is removed and its ScaleOut entry is removed with it. A ScaleOut entry of
// an older generation is never kept when empty.
func removeEmptyEntries(entries []grovecorev1alpha1.PodGangEntry, currentGenerationHash string) []grovecorev1alpha1.PodGangEntry {
	keepCurrentGenerationScaleOut := hasNonEmptyCurrentGenerationAnchor(entries, currentGenerationHash)
	return slices.DeleteFunc(entries, func(entry grovecorev1alpha1.PodGangEntry) bool {
		if keepCurrentGenerationScaleOut &&
			entry.Role == grovecorev1alpha1.PodGangEntryRoleScaleOut &&
			entry.PodCliqueSetGenerationHash == currentGenerationHash {
			return false
		}
		return isPodGangEntryEmpty(entry)
	})
}

// hasNonEmptyCurrentGenerationAnchor reports whether entries contains a current-generation anchor
// entry that carries at least one pod or replica index, meaning it will survive empty-entry removal.
func hasNonEmptyCurrentGenerationAnchor(entries []grovecorev1alpha1.PodGangEntry, currentGenerationHash string) bool {
	return slices.ContainsFunc(entries, func(entry grovecorev1alpha1.PodGangEntry) bool {
		return entry.Role == grovecorev1alpha1.PodGangEntryRoleAnchor &&
			entry.PodCliqueSetGenerationHash == currentGenerationHash &&
			!isPodGangEntryEmpty(entry)
	})
}

// ensureScaleOutEntry appends an empty ScaleOut entry to entries when the PodCliqueSet has any
// PodCliqueScalingGroup and no current-generation ScaleOut entry is present. The entry depends on the
// AnchorIndex 0 anchor of the current generation, which is always present in entries when this is
// called. It uses scaleOutEpoch when set, and a freshly minted epoch otherwise. When a current-generation
// ScaleOut entry already exists, entries are returned unchanged.
func ensureScaleOutEntry(clk clock.Clock, entries []grovecorev1alpha1.PodGangEntry, pcs *grovecorev1alpha1.PodCliqueSet, scaleOutEpoch string) []grovecorev1alpha1.PodGangEntry {
	if len(pcs.Spec.Template.PodCliqueScalingGroupConfigs) == 0 {
		return entries
	}
	currentHash := *pcs.Status.CurrentGenerationHash
	var anchorEpoch string
	for _, entry := range entries {
		if entry.Role == grovecorev1alpha1.PodGangEntryRoleScaleOut && entry.PodCliqueSetGenerationHash == currentHash {
			return entries
		}
		if entry.Role == grovecorev1alpha1.PodGangEntryRoleAnchor && entry.PodCliqueSetGenerationHash == currentHash && entry.AnchorIndex != nil && *entry.AnchorIndex == 0 {
			anchorEpoch = entry.Epoch
		}
	}
	if scaleOutEpoch == "" {
		scaleOutEpoch = strconv.FormatInt(clk.Now().UnixNano(), 10)
	}
	scaleOut := newPodGangEntry(scaleOutEpoch, currentHash, []string{anchorEpoch})
	scaleOut.Role = grovecorev1alpha1.PodGangEntryRoleScaleOut
	return append(entries, scaleOut)
}

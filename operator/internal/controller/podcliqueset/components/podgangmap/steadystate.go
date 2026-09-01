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
	"fmt"
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

// buildBootstrapAnchorEntry returns the anchor entry carrying every active standalone PodClique's
// full Replicas count and every active PodCliqueScalingGroup's MinAvailable replicas (PCSG indices
// [0, MinAvailable)). GREP-0677: idle components (template Replicas == 0) are omitted from the anchor
// so a bootstrap PodGangMap already reflects the intended idle state. DependsOn is nil.
func buildBootstrapAnchorEntry(pcs *grovecorev1alpha1.PodCliqueSet, epoch string) grovecorev1alpha1.PodGangEntry {
	entry := newPodGangEntry(epoch, *pcs.Status.CurrentGenerationHash, nil)
	entry.Role = grovecorev1alpha1.PodGangEntryRoleAnchor
	// A bootstrap PodGangMap has a single anchor, whose index is 0.
	entry.AnchorIndex = ptr.To[int32](0)
	entry.PodCliques = make(map[string]int32)
	for name, replicas := range componentutils.GetStandalonePCLQReplicasFromPCSTemplateSpec(pcs) {
		if replicas == 0 {
			continue
		}
		entry.PodCliques[name] = replicas
	}

	pcsgReplicas := componentutils.GetPCSGReplicasFromPCSTemplateSpec(pcs)
	pcsgMinAvailable := componentutils.GetPCSGMinAvailableFromPCSTemplateSpec(pcs)
	entry.PCSGReplicaIndices = make(map[string][]int32, len(pcsgMinAvailable))
	for name, minAvailable := range pcsgMinAvailable {
		if pcsgReplicas[name] == 0 {
			continue
		}
		entry.PCSGReplicaIndices[name] = lo.RangeFrom[int32](0, int(minAvailable))
	}
	return entry
}

// buildBootstrapTailEntry returns a single Tail entry for a fresh PCS replica. The entry aggregates,
// across all PodCliqueScalingGroups, each PodCliqueScalingGroup's replica indices above MinAvailable
// into a single entry. All PodCliqueScalingGroups and their indices share the same epoch value and
// depend on the anchor epoch. The PodGang materializer expands this entry into one PodGang per
// (PodCliqueScalingGroup, index). It returns false when no PodCliqueScalingGroup has replicas above
// MinAvailable. GREP-0677: idle PodCliqueScalingGroups (template Replicas == 0) contribute nothing.
func buildBootstrapTailEntry(pcs *grovecorev1alpha1.PodCliqueSet, epoch, anchorEpoch string) (grovecorev1alpha1.PodGangEntry, bool) {
	pcsgReplicaIndices := make(map[string][]int32)
	for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		replicas := *pcsgConfig.Replicas
		minAvailable := *pcsgConfig.MinAvailable
		if replicas == 0 || replicas <= minAvailable {
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
// live Spec.Replicas. A ScaleOut entry is ensured (even if empty) and empty entries are dropped.
//
// GREP-0677: idle members (Spec.Replicas == 0) are removed from gang membership, and a member that
// becomes active again wakes back in (see refreshStandalonePodCliqueCounts / reconcilePCSGReplicaIndices).
// The base (AnchorIndex 0) current-generation anchor is retained even when empty, so a fully-idle
// replica keeps a stable anchor for a later wake to repopulate.
func reconcileEntries(pcs *grovecorev1alpha1.PodCliqueSet,
	entries []grovecorev1alpha1.PodGangEntry,
	standalonePCLQs []grovecorev1alpha1.PodClique,
	pcsgs []grovecorev1alpha1.PodCliqueScalingGroup,
	pcsReplicaIndex int,
	epochSeed int64) ([]grovecorev1alpha1.PodGangEntry, error) {
	epochs, err := newEpochAllocator(entries, epochSeed)
	if err != nil {
		return nil, err
	}
	currentHash := *pcs.Status.CurrentGenerationHash
	scaleOutCount := countScaleOutEntries(entries, currentHash)
	if scaleOutCount > 1 {
		return nil, fmt.Errorf("current generation %q has %d ScaleOut entries", currentHash, scaleOutCount)
	}
	if len(pcs.Spec.Template.PodCliqueScalingGroupConfigs) > 0 && scaleOutCount == 0 {
		entries = ensureScaleOutEntry(entries, pcs, epochs.allocate(), nil)
	}
	wake := newWakePlacement(entries, currentHash, epochs)
	entries, err = refreshStandalonePodCliqueCounts(entries, pcs, standalonePCLQs, pcsReplicaIndex, wake)
	if err != nil {
		return nil, err
	}
	entries, err = reconcilePCSGReplicaIndices(entries, pcs, pcsgs, pcsReplicaIndex, wake)
	if err != nil {
		return nil, err
	}
	return removeEmptyEntries(entries, *pcs.Status.CurrentGenerationHash), nil
}

func countScaleOutEntries(entries []grovecorev1alpha1.PodGangEntry, currentHash string) int {
	return lo.CountBy(entries, func(entry grovecorev1alpha1.PodGangEntry) bool {
		return entry.Role == grovecorev1alpha1.PodGangEntryRoleScaleOut && entry.PodCliqueSetGenerationHash == currentHash
	})
}

// wakePlacement coordinates all wake operations in one reconcile. If the base anchor was empty at
// the start, every wake in this batch may join it. Its epoch is refreshed once and all current-
// generation dependencies are rewritten to the new epoch.
type wakePlacement struct {
	currentHash        string
	epochs             *epochAllocator
	reuseBaseAnchor    bool
	baseEpochRefreshed bool
}

func newWakePlacement(entries []grovecorev1alpha1.PodGangEntry, currentHash string, epochs *epochAllocator) *wakePlacement {
	baseAnchor := findAnchorEntryByIndex(entries, currentHash, 0)
	return &wakePlacement{
		currentHash:     currentHash,
		epochs:          epochs,
		reuseBaseAnchor: baseAnchor != nil && componentutils.IsPodGangEntryEmpty(*baseAnchor),
	}
}

func (w *wakePlacement) reusableBaseAnchor(entries []grovecorev1alpha1.PodGangEntry) (*grovecorev1alpha1.PodGangEntry, bool) {
	if !w.reuseBaseAnchor {
		return nil, false
	}
	baseAnchor := findAnchorEntryByIndex(entries, w.currentHash, 0)
	if baseAnchor == nil {
		return nil, false
	}
	if !w.baseEpochRefreshed {
		oldEpoch := baseAnchor.Epoch
		baseAnchor.Epoch = w.epochs.allocate()
		rewriteDependsOnEpoch(entries, w.currentHash, oldEpoch, baseAnchor.Epoch)
		w.baseEpochRefreshed = true
	}
	return baseAnchor, true
}

func rewriteDependsOnEpoch(entries []grovecorev1alpha1.PodGangEntry, currentHash, oldEpoch, newEpoch string) {
	for i := range entries {
		if entries[i].PodCliqueSetGenerationHash != currentHash {
			continue
		}
		for j := range entries[i].DependsOn {
			if entries[i].DependsOn[j] == oldEpoch {
				entries[i].DependsOn[j] = newEpoch
			}
		}
	}
}

// refreshStandalonePodCliqueCounts reconciles each standalone PodClique's membership in the anchor
// entries to its live Spec.Replicas.
//
//   - Active (replicas > 0): the pod count on the owning anchor is set to the live Spec.Replicas.
//   - Idle (replicas == 0, GREP-0677): the PodClique leaves the gang. Its key is removed from the
//     owning anchor so the materializer emits no PodGroup for it. If it is not currently a member of
//     any anchor it stays out.
//   - Wake (replicas > 0 but no owning anchor): the PodClique re-joins gang membership. It is added
//     to the base (AnchorIndex 0) anchor when that anchor is empty (no materialized PodGang to
//     expand); otherwise a new anchor entry is created so an already-scheduled PodGang is not
//     enlarged.
//
// Clique composition is immutable on update, so an active PodClique already present in an anchor
// keeps its slot.
func refreshStandalonePodCliqueCounts(entries []grovecorev1alpha1.PodGangEntry, pcs *grovecorev1alpha1.PodCliqueSet, standalonePCLQs []grovecorev1alpha1.PodClique, pcsReplicaIndex int, wake *wakePlacement) ([]grovecorev1alpha1.PodGangEntry, error) {
	currentHash := *pcs.Status.CurrentGenerationHash
	rnr := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}
	var wokenCliques map[string]int32
	for _, standalonePCLQ := range standalonePCLQs {
		cliqueName := apicommon.ExtractPodCliqueNameFromStandalonePCLQFQN(standalonePCLQ.Name, rnr)
		anchor, _, err := componentutils.StandalonePCLQMembership(entries, currentHash, cliqueName)
		if err != nil {
			return nil, err
		}
		switch {
		case standalonePCLQ.Spec.Replicas == 0:
			// Idle: drop the clique from its owning anchor (if any) so it leaves the gang.
			if anchor != nil {
				delete(anchor.PodCliques, cliqueName)
			}
		case anchor != nil:
			// Active and already a member: refresh the count.
			anchor.PodCliques[cliqueName] = standalonePCLQ.Spec.Replicas
		default:
			// Wake: not currently a member. Collect for placement after the loop.
			if wokenCliques == nil {
				wokenCliques = make(map[string]int32)
			}
			wokenCliques[cliqueName] = standalonePCLQ.Spec.Replicas
		}
	}
	if len(wokenCliques) > 0 {
		entries = placeWokenStandaloneCliques(entries, currentHash, wokenCliques, wake)
	}
	return entries, nil
}

// placeWokenStandaloneCliques adds newly-active standalone PodCliques back into gang membership. When
// the base (AnchorIndex 0) anchor of the current generation carries no members, the cliques populate
// it in place — reusing the retained-but-empty anchor without creating a new PodGang. To avoid
// reusing the name of a PodGang that may still be terminating from before the idle transition, the
// repopulated base anchor is stamped with a fresh epoch (GREP-0677 race safety). Otherwise (the base
// anchor is already materialized) a new anchor entry is created so the waking cliques form their own
// PodGang instead of expanding an already-scheduled one.
func placeWokenStandaloneCliques(entries []grovecorev1alpha1.PodGangEntry, currentHash string, wokenCliques map[string]int32, wake *wakePlacement) []grovecorev1alpha1.PodGangEntry {
	if baseAnchor, ok := wake.reusableBaseAnchor(entries); ok {
		if baseAnchor.PodCliques == nil {
			baseAnchor.PodCliques = make(map[string]int32, len(wokenCliques))
		}
		for name, replicas := range wokenCliques {
			baseAnchor.PodCliques[name] = replicas
		}
		return entries
	}
	var dependsOn []string
	baseAnchor := findAnchorEntryByIndex(entries, currentHash, 0)
	if baseAnchor != nil {
		dependsOn = []string{baseAnchor.Epoch}
	}
	newAnchor := newPodGangEntry(wake.epochs.allocate(), currentHash, dependsOn)
	newAnchor.Role = grovecorev1alpha1.PodGangEntryRoleAnchor
	newAnchor.AnchorIndex = ptr.To(nextAnchorIndex(entries, currentHash))
	newAnchor.PodCliques = make(map[string]int32, len(wokenCliques))
	for name, replicas := range wokenCliques {
		newAnchor.PodCliques[name] = replicas
	}
	return append(entries, newAnchor)
}

// reconcilePCSGReplicaIndices diffs each PodCliqueScalingGroup's replica-index count across all
// entries against its live Spec.Replicas and appends (scale-out), drains (scale-in), or wakes-from-
// idle accordingly.
//
// GREP-0677: when a PCSG has no placed indices (idle) and becomes active again, indices
// [0, MinAvailable) join the base anchor (or a new anchor when the base is already materialized);
// remaining indices join the ScaleOut entry as independent PodGangs. A scale-down to zero drains all
// indices; the retained base anchor is what a later wake repopulates.
func reconcilePCSGReplicaIndices(entries []grovecorev1alpha1.PodGangEntry, pcs *grovecorev1alpha1.PodCliqueSet, pcsgs []grovecorev1alpha1.PodCliqueScalingGroup, pcsReplicaIndex int, wake *wakePlacement) ([]grovecorev1alpha1.PodGangEntry, error) {
	rnr := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}
	currentHash := *pcs.Status.CurrentGenerationHash
	for _, pcsg := range pcsgs {
		pcsgConfigName, err := apicommon.ExtractScalingGroupNameFromPCSGFQN(pcsg.Name, rnr)
		if err != nil {
			return nil, err
		}
		currentCount := countPCSGReplicaIndices(entries, pcsgConfigName)
		desired := int(pcsg.Spec.Replicas)
		diff := desired - currentCount
		switch {
		case currentCount == 0 && desired > 0:
			if pcsg.Spec.MinAvailable == nil {
				return nil, fmt.Errorf("PodCliqueScalingGroup %s has nil spec.minAvailable", pcsg.Name)
			}
			minAvailable := int(*pcsg.Spec.MinAvailable)
			if minAvailable > desired {
				return nil, fmt.Errorf("PodCliqueScalingGroup %s has minAvailable %d greater than replicas %d", pcsg.Name, minAvailable, desired)
			}
			entries, err = wakePCSGReplicaIndices(entries, currentHash, pcsgConfigName, desired, minAvailable, wake)
			if err != nil {
				return nil, err
			}
		case diff > 0:
			if err := appendScaleOutReplicaIndices(entries, currentHash, pcsgConfigName, lo.RangeFrom[int32](int32(currentCount), diff)); err != nil {
				return nil, err
			}
		case diff < 0:
			drainReplicaIndicesForScaleIn(entries, pcsgConfigName, -diff)
		}
	}
	return entries, nil
}

// wakePCSGReplicaIndices places a waking PodCliqueScalingGroup's replica indices. Indices
// [0, minAvailable) join the base (AnchorIndex 0) anchor when it is empty, or a fresh anchor entry
// when the base is already materialized, so quorum members form a single anchor PodGang. Indices
// [minAvailable, replicas) are appended to the ScaleOut entry as independent scaled PodGangs.
func wakePCSGReplicaIndices(entries []grovecorev1alpha1.PodGangEntry, currentHash, pcsgConfigName string, replicas, minAvailable int, wake *wakePlacement) ([]grovecorev1alpha1.PodGangEntry, error) {
	anchorIndices := lo.RangeFrom[int32](0, minAvailable)
	if baseAnchor, ok := wake.reusableBaseAnchor(entries); ok {
		if baseAnchor.PCSGReplicaIndices == nil {
			baseAnchor.PCSGReplicaIndices = make(map[string][]int32)
		}
		baseAnchor.PCSGReplicaIndices[pcsgConfigName] = anchorIndices
	} else {
		var dependsOn []string
		baseAnchor := findAnchorEntryByIndex(entries, currentHash, 0)
		if baseAnchor != nil {
			dependsOn = []string{baseAnchor.Epoch}
		}
		newAnchor := newPodGangEntry(wake.epochs.allocate(), currentHash, dependsOn)
		newAnchor.Role = grovecorev1alpha1.PodGangEntryRoleAnchor
		newAnchor.AnchorIndex = ptr.To(nextAnchorIndex(entries, currentHash))
		newAnchor.PCSGReplicaIndices = map[string][]int32{pcsgConfigName: anchorIndices}
		entries = append(entries, newAnchor)
	}
	if replicas > minAvailable {
		if err := appendScaleOutReplicaIndices(entries, currentHash, pcsgConfigName, lo.RangeFrom[int32](int32(minAvailable), replicas-minAvailable)); err != nil {
			return nil, err
		}
	}
	return entries, nil
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
func appendScaleOutReplicaIndices(entries []grovecorev1alpha1.PodGangEntry, currentHash, pcsgConfigName string, indices []int32) error {
	for i := range entries {
		if entries[i].Role == grovecorev1alpha1.PodGangEntryRoleScaleOut &&
			entries[i].PodCliqueSetGenerationHash == currentHash {
			if entries[i].PCSGReplicaIndices == nil {
				entries[i].PCSGReplicaIndices = make(map[string][]int32)
			}
			entries[i].PCSGReplicaIndices[pcsgConfigName] = append(entries[i].PCSGReplicaIndices[pcsgConfigName], indices...)
			slices.Sort(entries[i].PCSGReplicaIndices[pcsgConfigName])
			return nil
		}
	}
	return fmt.Errorf("current generation %q has no ScaleOut entry", currentHash)
}

// drainReplicaIndicesForScaleIn removes count of the given PodCliqueScalingGroup's replica indices,
// draining in role order ScaleOut, then Tail, then Anchor (highest AnchorIndex first, AnchorIndex 0
// last), and the highest index first within a chosen entry. Scale-to-zero is allowed to drain the
// base anchor after every less critical entry has been emptied.
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

// removeEmptyEntries drops entries that carry no pods and no replica indices. The current
// generation's ScaleOut entry and base anchor are retained as stable slots for later scale-out and
// wake operations. Empty entries from older generations and non-base anchors are removed.
func removeEmptyEntries(entries []grovecorev1alpha1.PodGangEntry, currentGenerationHash string) []grovecorev1alpha1.PodGangEntry {
	return slices.DeleteFunc(entries, func(entry grovecorev1alpha1.PodGangEntry) bool {
		if entry.Role == grovecorev1alpha1.PodGangEntryRoleScaleOut && entry.PodCliqueSetGenerationHash == currentGenerationHash {
			return false
		}
		if entry.Role == grovecorev1alpha1.PodGangEntryRoleAnchor &&
			entry.PodCliqueSetGenerationHash == currentGenerationHash &&
			entry.AnchorIndex != nil && *entry.AnchorIndex == 0 {
			return false
		}
		return componentutils.IsPodGangEntryEmpty(entry)
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
	for _, entry := range entries {
		if entry.Role == grovecorev1alpha1.PodGangEntryRoleScaleOut && entry.PodCliqueSetGenerationHash == currentHash {
			return entries
		}
		if entry.Role == grovecorev1alpha1.PodGangEntryRoleAnchor && entry.PodCliqueSetGenerationHash == currentHash && entry.AnchorIndex != nil && *entry.AnchorIndex == 0 {
			anchorEpoch = entry.Epoch
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

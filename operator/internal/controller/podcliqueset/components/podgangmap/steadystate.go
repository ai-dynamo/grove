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

package podgangmap

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// buildBootstrapEntries emits the initial PodGangMap entries for a fresh PCS replica. It produces
// one anchor entry (epoch E0, DependsOn nil) carrying every standalone PodClique's full replica
// count and every PodCliqueScalingGroup's MinAvailable replicas, one Tail entry aggregating each
// PodCliqueScalingGroup's replicas above MinAvailable across all PodCliqueScalingGroups (epoch
// E1 > E0, DependsOn E0), and, when the PodCliqueSet has any PodCliqueScalingGroup, one empty
// ScaleOut entry (epoch E2 > E1, DependsOn E0) that future scale-outs attach to. Entries are
// returned in epoch order.
func buildBootstrapEntries(pcs *grovecorev1alpha1.PodCliqueSet, clk clock.Clock) []grovecorev1alpha1.PodGangEntry {
	anchorEpoch := strconv.FormatInt(clk.Now().UnixNano(), 10)
	tailEpoch := strconv.FormatInt(clk.Now().UnixNano()+1, 10)
	scaleOutEpoch := strconv.FormatInt(clk.Now().UnixNano()+2, 10)

	entries := make([]grovecorev1alpha1.PodGangEntry, 0, 3)
	entries = append(entries, buildBootstrapAnchorEntry(pcs, anchorEpoch))
	if tailEntry, ok := buildBootstrapTailEntry(pcs, tailEpoch, anchorEpoch); ok {
		entries = append(entries, tailEntry)
	}
	entries = ensureScaleOutEntry(entries, pcs, scaleOutEpoch, nil)

	return entries
}

// buildBootstrapAnchorEntry returns the anchor entry carrying every standalone PodClique's full
// Replicas count and every PodCliqueScalingGroup's MinAvailable replicas (PCSG indices
// [0, MinAvailable)). DependsOn is nil.
func buildBootstrapAnchorEntry(pcs *grovecorev1alpha1.PodCliqueSet, epoch string) grovecorev1alpha1.PodGangEntry {
	entry := newPodGangEntry(epoch, *pcs.Status.CurrentGenerationHash, nil)
	entry.Role = grovecorev1alpha1.PodGangEntryRoleAnchor
	// entry.AnchorIndex is defaulted to 0. This is the correct index for the anchor. During bootstrap
	// there is only going to be a single anchor. Skipping setting this explicitly.
	entry.PodCliques = componentutils.GetStandalonePCLQReplicasFromPCSTemplateSpec(pcs)

	pcsgMinAvailable := componentutils.GetPCSGMinAvailableFromPCSTemplateSpec(pcs)
	entry.PCSGReplicaIndices = make(map[string][]int32, len(pcsgMinAvailable))
	for name, minAvailable := range pcsgMinAvailable {
		indices := make([]int32, 0, minAvailable)
		for i := int32(0); i < minAvailable; i++ {
			indices = append(indices, i)
		}
		entry.PCSGReplicaIndices[name] = indices
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
		if replicas <= minAvailable {
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

// buildEntriesFromPCLQAndPCSGStatuses rebuilds the steady-state PodGangMap entries from the
// PodGangMapping status on the standalone PodCliques and PodCliqueScalingGroups. existingPGM holds
// each entry's identity (epoch, role, anchor index, DependsOn, generation hash); this rebuild
// preserves that identity per epoch and refreshes only the membership (standalone PodClique pod
// counts and PCSG replica indices), which is what drifts when a child scales in or out. It returns
// ErrCodeContinueReconcileAndRequeue when any standalone PodClique or PodCliqueScalingGroup has not
// yet published its PodGangMapping, so a partial rebuild is not persisted. The only net-new entry is
// a PCSG scale-out, which collapses into a single ScaleOut entry with a nil DependsOn. A status that
// references an epoch absent from the PodGangMap is a contract violation and returns an error.
// Entries left with no membership are dropped.
func buildEntriesFromPCLQAndPCSGStatuses(pcs *grovecorev1alpha1.PodCliqueSet,
	existingStandalonePCLQs []grovecorev1alpha1.PodClique,
	existingPCSGs []grovecorev1alpha1.PodCliqueScalingGroup,
	existingPGM *grovecorev1alpha1.PodGangMap,
	pcsReplicaIndex int,
	clk clock.Clock) ([]grovecorev1alpha1.PodGangEntry, error) {

	if !canRebuildPGMFromStatuses(pcs, existingStandalonePCLQs, existingPCSGs) {
		return nil, groveerr.New(groveerr.ErrCodeContinueReconcileAndRequeue,
			component.OperationSync,
			fmt.Sprintf("cannot rebuild PodGangMap for replica %d or PodCliqueSet %v: PodGangMapping for one or more PCLQ/PCSG are not yet published", pcsReplicaIndex, client.ObjectKeyFromObject(pcs)))
	}

	// Preserve each existing entry's identity, cleared of membership. Membership is re-added below
	// from the current statuses.
	entryByEpoch := make(map[string]*grovecorev1alpha1.PodGangEntry, len(existingPGM.Spec.Entries))
	for i := range existingPGM.Spec.Entries {
		existing := existingPGM.Spec.Entries[i]
		entry := newPodGangEntry(existing.Epoch, existing.PodCliqueSetGenerationHash, existing.DependsOn)
		entry.Role = existing.Role
		entry.AnchorIndex = existing.AnchorIndex
		entryByEpoch[existing.Epoch] = &entry
	}

	// Standalone PodClique membership. The referenced epoch always names an existing entry, because
	// PGM authored that entry before the PodClique status was seeded from it.
	for i := range existingStandalonePCLQs {
		cliqueName, err := utils.GetPodCliqueNameFromPodCliqueFQN(existingStandalonePCLQs[i].ObjectMeta)
		if err != nil {
			return nil, groveerr.WrapError(err, errCodeExtractPodCliqueName, component.OperationSync,
				fmt.Sprintf("Error extracting PodClique name from FQN %s", existingStandalonePCLQs[i].Name))
		}
		for _, assignment := range existingStandalonePCLQs[i].Status.PodGangMapping {
			entry := entryByEpoch[assignment.Epoch]
			if entry == nil {
				return nil, groveerr.New(errCodeStatusEpochNotInPodGangMap, component.OperationSync,
					fmt.Sprintf("standalone PodClique %s status references epoch %q absent from PodGangMap for replica %d", existingStandalonePCLQs[i].Name, assignment.Epoch, pcsReplicaIndex))
			}
			if entry.PodCliques == nil {
				entry.PodCliques = make(map[string]int32)
			}
			entry.PodCliques[cliqueName] = assignment.PodCount
		}
	}

	// PodCliqueScalingGroup membership. Every assignment (Anchor, Tail or ScaleOut) names an existing
	// entry by epoch. The ScaleOut entry is pre-created in the PodGangMap, so a scale-out assignment
	// carries its epoch just like the others.
	for i := range existingPCSGs {
		pcsgConfigName, err := apicommon.ExtractScalingGroupNameFromPCSGFQN(existingPCSGs[i].Name, apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex})
		if err != nil {
			return nil, groveerr.WrapError(err, errCodeExtractPodCliqueName, component.OperationSync,
				fmt.Sprintf("Error extracting scaling group name from FQN %s", existingPCSGs[i].Name))
		}
		for _, assignment := range existingPCSGs[i].Status.PodGangMapping {
			entry := entryByEpoch[assignment.Epoch]
			if entry == nil {
				return nil, groveerr.New(errCodeStatusEpochNotInPodGangMap, component.OperationSync,
					fmt.Sprintf("PodCliqueScalingGroup %s status references epoch %q absent from PodGangMap for replica %d", existingPCSGs[i].Name, assignment.Epoch, pcsReplicaIndex))
			}
			if entry.PCSGReplicaIndices == nil {
				entry.PCSGReplicaIndices = make(map[string][]int32)
			}
			entry.PCSGReplicaIndices[pcsgConfigName] = assignment.ReplicaIndices
		}
	}

	entries := make([]grovecorev1alpha1.PodGangEntry, 0, len(entryByEpoch))
	for _, entry := range entryByEpoch {
		entries = append(entries, *entry)
	}
	entries = ensureScaleOutEntry(entries, pcs, strconv.FormatInt(clk.Now().UnixNano(), 10), nil)
	return removeEmptyEntries(entries, *pcs.Status.CurrentGenerationHash), nil
}

// canRebuildPGMFromStatuses returns true when every standalone PCLQ and every PCSG that the PCS
// spec declares is observed and has a non-empty Status.PodGangMapping. Rebuilding the PodGangMap
// from a partial set of owner statuses would wipe entries seeded from spec, so the rebuild waits
// until every owner has published its mapping.
func canRebuildPGMFromStatuses(pcs *grovecorev1alpha1.PodCliqueSet, standalonePCLQs []grovecorev1alpha1.PodClique, pcsgs []grovecorev1alpha1.PodCliqueScalingGroup) bool {
	if len(standalonePCLQs) < componentutils.CountStandalonePCLQs(pcs) {
		return false
	}
	if len(pcsgs) < len(pcs.Spec.Template.PodCliqueScalingGroupConfigs) {
		return false
	}
	for _, pclq := range standalonePCLQs {
		if len(pclq.Status.PodGangMapping) == 0 {
			return false
		}
	}
	for _, pcsg := range pcsgs {
		if len(pcsg.Status.PodGangMapping) == 0 {
			return false
		}
	}
	return true
}

// removeEmptyEntries drops entries that hold no members. A ScaleOut entry for the CURRENT generation
// hash is exempt, it is pre-created empty and kept as the PodGangMap-owned scale-out epoch that
// steady-state scale-outs attach to, so it must persist even with no members. A ScaleOut entry for an
// OLD generation hash is not exempt, once drained it is a remnant of a superseded generation and is
// removed like any other empty entry.
func removeEmptyEntries(entries []grovecorev1alpha1.PodGangEntry, currentGenerationHash string) []grovecorev1alpha1.PodGangEntry {
	return slices.DeleteFunc(entries, func(entry grovecorev1alpha1.PodGangEntry) bool {
		if entry.Role == grovecorev1alpha1.PodGangEntryRoleScaleOut && entry.PodCliqueSetGenerationHash == currentGenerationHash {
			return false
		}
		return isPodGangEntryEmpty(entry)
	})
}

// isPodGangEntryEmpty reports whether an entry holds no members, that is every standalone PodClique
// pod count is zero and every PodCliqueScalingGroup replica index set is empty.
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

// reconstructEntriesFromExistingPodGangs rebuilds PodGangMap entries from live PodGangs on upgrade
// from a pre-coherent Grove version (see the reconstruction case in section 11.4 of the design).
// The one PodGang without the grove.io/base-podgang label is the anchor and becomes the Anchor entry
// at epoch E0 with no DependsOn. Its absence is an error, a pre-coherent PCS replica always has an
// anchor. Every other PodGang carries the base-podgang label and represents exactly one
// (PodCliqueScalingGroup, replica index). Each index is bucketed against its PodCliqueScalingGroup's
// template Replicas, an index below Replicas joins the Tail entry (epoch E1 > E0, DependsOn E0) and
// an index at or above Replicas joins the ScaleOut entry (epoch E2 > E1, DependsOn E0). Tail indices
// aggregate into one Tail entry and ScaleOut indices into one ScaleOut entry. When the PodCliqueSet
// has any PodCliqueScalingGroup a ScaleOut entry is always emitted, empty if nothing scaled beyond
// the template, so a future scale-out attaches to a PodGangMap-owned epoch. Returns an error if the
// anchor is absent or a PodGang's PodGroup names cannot be parsed.
func reconstructEntriesFromExistingPodGangs(pcs *grovecorev1alpha1.PodCliqueSet, existingPGs []groveschedulerv1alpha1.PodGang, pcsReplicaIndex int, clk clock.Clock) ([]grovecorev1alpha1.PodGangEntry, error) {
	var (
		pcsGenerationHash = *pcs.Status.CurrentGenerationHash
		anchorEpoch       = strconv.FormatInt(clk.Now().UnixNano(), 10)
		tailEpoch         = strconv.FormatInt(clk.Now().UnixNano()+1, 10)
		scaleOutEpoch     = strconv.FormatInt(clk.Now().UnixNano()+2, 10)
		pcsgReplicas      = componentutils.GetPCSGReplicasFromPCSTemplateSpec(pcs)
	)

	var anchor *grovecorev1alpha1.PodGangEntry
	tailIndices := make(map[string][]int32)
	scaleOutIndices := make(map[string][]int32)

	for i := range existingPGs {
		pgEntry, err := buildEntryFromPodGang(pcs, pcsReplicaIndex, pcsGenerationHash, existingPGs[i])
		if err != nil {
			return nil, groveerr.WrapError(err,
				errCodeReconstructPodGangMapEntry,
				component.OperationSync,
				fmt.Sprintf("Error reconstructing PodGangMap entry from PodGang %s for PodCliqueSet: %v", existingPGs[i].Name, client.ObjectKeyFromObject(pcs)),
			)
		}

		if _, isScaled := existingPGs[i].Labels[apicommon.LabelBasePodGang]; !isScaled {
			pgEntry.Role = grovecorev1alpha1.PodGangEntryRoleAnchor
			pgEntry.Epoch = anchorEpoch
			anchor = pgEntry
			continue
		}

		pcsgName, index, err := singleScaledPCSGIndex(existingPGs[i].Name, pgEntry)
		if err != nil {
			return nil, err
		}
		if index < pcsgReplicas[pcsgName] {
			tailIndices[pcsgName] = append(tailIndices[pcsgName], index)
		} else {
			scaleOutIndices[pcsgName] = append(scaleOutIndices[pcsgName], index)
		}
	}

	if anchor == nil {
		return nil, groveerr.New(errCodeReconstructPodGangMapEntry, component.OperationSync,
			fmt.Sprintf("no anchor PodGang (without the %s label) found while reconstructing PodGangMap for PodCliqueSet %v", apicommon.LabelBasePodGang, client.ObjectKeyFromObject(pcs)))
	}

	entries := []grovecorev1alpha1.PodGangEntry{*anchor}
	if len(tailIndices) > 0 {
		sortIndicesPerPCSG(tailIndices)
		tail := newPodGangEntry(tailEpoch, pcsGenerationHash, []string{anchorEpoch})
		tail.Role = grovecorev1alpha1.PodGangEntryRoleTail
		tail.PCSGReplicaIndices = tailIndices
		entries = append(entries, tail)
	}
	entries = ensureScaleOutEntry(entries, pcs, scaleOutEpoch, scaleOutIndices)

	return entries, nil
}

// singleScaledPCSGIndex returns the one (PodCliqueScalingGroup, replica index) a scaled PodGang
// represents. A scaled PodGang always maps to exactly one PodCliqueScalingGroup replica, so exactly
// one PodCliqueScalingGroup with one index is expected. It returns an error otherwise.
func singleScaledPCSGIndex(podGangName string, entry *grovecorev1alpha1.PodGangEntry) (string, int32, error) {
	if len(entry.PCSGReplicaIndices) != 1 {
		return "", 0, groveerr.New(errCodeReconstructPodGangMapEntry, component.OperationSync,
			fmt.Sprintf("scaled PodGang %s maps to %d PodCliqueScalingGroups, expected exactly one", podGangName, len(entry.PCSGReplicaIndices)))
	}
	for pcsgName, indices := range entry.PCSGReplicaIndices {
		if len(indices) != 1 {
			return "", 0, groveerr.New(errCodeReconstructPodGangMapEntry, component.OperationSync,
				fmt.Sprintf("scaled PodGang %s maps to %d replica indices for PodCliqueScalingGroup %q, expected exactly one", podGangName, len(indices), pcsgName))
		}
		return pcsgName, indices[0], nil
	}
	return "", 0, groveerr.New(errCodeReconstructPodGangMapEntry, component.OperationSync,
		fmt.Sprintf("scaled PodGang %s has no PodCliqueScalingGroup replica index", podGangName))
}

// sortIndicesPerPCSG sorts each PodCliqueScalingGroup's index slice ascending for deterministic output.
func sortIndicesPerPCSG(indicesByPCSG map[string][]int32) {
	for name := range indicesByPCSG {
		slices.Sort(indicesByPCSG[name])
	}
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
		if entries[i].Role == grovecorev1alpha1.PodGangEntryRoleAnchor && entries[i].PodCliqueSetGenerationHash == currentHash && entries[i].AnchorIndex == 0 {
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

// buildEntryFromPodGang reconstructs a PodGangEntry from a live PodGang. Each PodGroup name is a
// PodClique FQN: a standalone PodClique group contributes its pod count (number of pod references)
// to PodCliques, while a PCSG-owned group contributes the PCSG replica index parsed from the FQN
// to PCSGReplicaIndices. The same PCSG replica appears once per constituent clique, so indices are
// de-duplicated per PCSG. Epoch and DependsOn are left unset; the caller assigns them.
func buildEntryFromPodGang(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int, pcsGenerationHash string, pg groveschedulerv1alpha1.PodGang) (*grovecorev1alpha1.PodGangEntry, error) {
	pgmEntry := &grovecorev1alpha1.PodGangEntry{
		PodCliqueSetGenerationHash: pcsGenerationHash,
		PodCliques:                 make(map[string]int32),
		PCSGReplicaIndices:         make(map[string][]int32),
	}

	pcsgNameToIndexSet := make(map[string]sets.Set[int32])
	for _, podGroup := range pg.Spec.PodGroups {
		cliqueName, err := extractCliqueName(pcs, podGroup.Name)
		if err != nil {
			return nil, err
		}
		pcsgConfig := componentutils.FindScalingGroupConfigForClique(pcs.Spec.Template.PodCliqueScalingGroupConfigs, cliqueName)
		if pcsgConfig == nil {
			// Clique is a standalone PCLQ - pod count = number of pod references
			pgmEntry.PodCliques[cliqueName] = int32(len(podGroup.PodReferences))
			continue
		}
		// PCSG-owned PodGroup: extract the PCSG replica index from PCSG FQN
		pcsgIndex, err := extractPCSGReplicaIndexFromPCLQFQN(podGroup.Name, pcs.Name, pcsReplicaIndex, pcsgConfig.Name, cliqueName)
		if err != nil {
			return nil, err
		}
		if pcsgNameToIndexSet[pcsgConfig.Name] == nil {
			pcsgNameToIndexSet[pcsgConfig.Name] = sets.New[int32]()
		}
		pcsgNameToIndexSet[pcsgConfig.Name].Insert(pcsgIndex)
	}
	for pcsgName, indexSet := range pcsgNameToIndexSet {
		pgmEntry.PCSGReplicaIndices[pcsgName] = sets.List(indexSet)
	}

	return pgmEntry, nil
}

// extractCliqueName returns the unqualified clique template name for a PodClique FQN by matching
// the FQN's trailing segment against the clique templates declared in the PCS spec. Works from a
// bare FQN string (it does not need the PodClique object's labels). Returns an error if no
// template matches.
func extractCliqueName(pcs *grovecorev1alpha1.PodCliqueSet, pclqFQN string) (string, error) {
	for _, cliqueTemplate := range pcs.Spec.Template.Cliques {
		if cliqueTemplate == nil {
			continue
		}
		suffix := "-" + cliqueTemplate.Name
		if len(pclqFQN) > len(suffix) && pclqFQN[len(pclqFQN)-len(suffix):] == suffix {
			return cliqueTemplate.Name, nil
		}
	}
	return "", fmt.Errorf("PodGroup name %q does not match any known clique template in PCS %s", pclqFQN, pcs.Name)
}

// extractPCSGReplicaIndexFromPCLQFQN parses the PCSG replica index from a PCSG-owned PodClique FQN
// of the form <pcsgFQN>-<pcsgReplicaIndex>-<cliqueName>, where pcsgFQN is derived from pcsName,
// pcsReplicaIndex, and pcsgName. Returns an error if the FQN does not match that shape. Grove is
// the sole writer of these names, so a parse failure is a contract violation, not a soft skip.
func extractPCSGReplicaIndexFromPCLQFQN(pclqFQN string, pcsName string, pcsReplicaIndex int, pcsgName, cliqueName string) (int32, error) {
	pcsNameReplica := apicommon.ResourceNameReplica{Name: pcsName, Replica: pcsReplicaIndex}
	pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(pcsNameReplica, pcsgName)
	prefix := pcsgFQN + "-"
	suffix := "-" + cliqueName
	if !strings.HasPrefix(pclqFQN, prefix) || !strings.HasSuffix(pclqFQN, suffix) {
		return 0, fmt.Errorf("PCLQ FQN %q does not match expected shape %s<index>%s", pclqFQN, prefix, suffix)
	}
	mid := pclqFQN[len(prefix) : len(pclqFQN)-len(suffix)]
	pcsgIndex, err := strconv.Atoi(mid)
	if err != nil {
		return 0, fmt.Errorf("PCSG replica index in PCLQ FQN %q is not a valid integer: %w", pclqFQN, err)
	}
	return int32(pcsgIndex), nil
}

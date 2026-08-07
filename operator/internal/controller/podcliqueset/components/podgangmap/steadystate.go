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
// one MPG entry (epoch E0, DependsOn nil) carrying every standalone PCLQ's full replica count
// and every PCSG's MinAvailable replicas, plus one TPG entry aggregating, across all PCSGs, each
// PCSG's replicas above MinAvailable (epoch E1 > E0, DependsOn E0). Returns the MPG entry first,
// the TPG entry after.
func buildBootstrapEntries(pcs *grovecorev1alpha1.PodCliqueSet, clk clock.Clock) []grovecorev1alpha1.PodGangEntry {
	mpgEpoch := strconv.FormatInt(clk.Now().UnixNano(), 10)
	tpgEpoch := strconv.FormatInt(clk.Now().UnixNano()+1, 10)

	entries := make([]grovecorev1alpha1.PodGangEntry, 0, 2)
	entries = append(entries, buildBootstrapMPGEntry(pcs, mpgEpoch))
	if tpgEntry, ok := buildBootstrapTPGEntry(pcs, tpgEpoch, mpgEpoch); ok {
		entries = append(entries, tpgEntry)
	}

	return entries
}

// buildBootstrapMPGEntry returns the MPG entry carrying every standalone PCLQ's full Replicas count
// and every PCSG's MinAvailable replicas (PCSG indices [0, MinAvailable)). DependsOn is nil.
func buildBootstrapMPGEntry(pcs *grovecorev1alpha1.PodCliqueSet, epoch string) grovecorev1alpha1.PodGangEntry {
	entry := newPodGangEntry(epoch, *pcs.Status.CurrentGenerationHash, nil)
	entry.Role = grovecorev1alpha1.PodGangEntryRoleAnchor
	// entry.AnchorIndex is defaulted to 0. This is the correct index for MPG. During bootstrap there is only going
	// to a single MPG. Skipping setting this explicitly.
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

// buildBootstrapTPGEntry returns a single TPG entry for a fresh PCS replica.
// A TPG entry corresponds to either a legacy scaled PodGang (SPG) or the tail PodGang (TPG).
// The entry aggregates, across all PCSGs, each PCSG's replica indices above MinAvailable into a single
// entry. All PCSGs and their indices share the same epoch value and depend on the same MPG epoch.
// The PodGang materializer expands this entry into one PodGang (SPG/TPG) per (PCSG, index).
func buildBootstrapTPGEntry(pcs *grovecorev1alpha1.PodCliqueSet, epoch, mpgEpoch string) (grovecorev1alpha1.PodGangEntry, bool) {
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
	entry := newPodGangEntry(epoch, *pcs.Status.CurrentGenerationHash, []string{mpgEpoch})
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

	// PodCliqueScalingGroup membership. Anchor and Tail assignments name an existing entry by epoch.
	// A ScaleOut assignment carries no epoch (all scale-out collapses into one ScaleOut entry): it
	// reuses the existing ScaleOut entry, or opens one fresh entry with a nil DependsOn.
	for i := range existingPCSGs {
		pcsgConfigName, err := apicommon.ExtractScalingGroupNameFromPCSGFQN(existingPCSGs[i].Name, apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex})
		if err != nil {
			return nil, groveerr.WrapError(err, errCodeExtractPodCliqueName, component.OperationSync,
				fmt.Sprintf("Error extracting scaling group name from FQN %s", existingPCSGs[i].Name))
		}
		for _, assignment := range existingPCSGs[i].Status.PodGangMapping {
			var entry *grovecorev1alpha1.PodGangEntry
			if assignment.Role == grovecorev1alpha1.PodGangEntryRoleScaleOut {
				entry = getOrCreateScaleOutEntry(entryByEpoch, *pcs.Status.CurrentGenerationHash, clk)
			} else {
				entry = entryByEpoch[assignment.Epoch]
				if entry == nil {
					return nil, groveerr.New(errCodeStatusEpochNotInPodGangMap, component.OperationSync,
						fmt.Sprintf("PodCliqueScalingGroup %s status references epoch %q absent from PodGangMap for replica %d", existingPCSGs[i].Name, assignment.Epoch, pcsReplicaIndex))
				}
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
	return removeEmptyEntries(entries), nil
}

// getOrCreateScaleOutEntry returns the single ScaleOut entry, creating it with a nil DependsOn if
// none exists yet. All PCSG scale-out replicas collapse into one ScaleOut entry, so there is at most
// one. A scale-out is not part of any scheduling batch, hence the nil DependsOn.
func getOrCreateScaleOutEntry(entryByEpoch map[string]*grovecorev1alpha1.PodGangEntry, pcsGenerationHash string, clk clock.Clock) *grovecorev1alpha1.PodGangEntry {
	for _, entry := range entryByEpoch {
		if entry.Role == grovecorev1alpha1.PodGangEntryRoleScaleOut {
			return entry
		}
	}
	epoch := strconv.FormatInt(clk.Now().UnixNano(), 10)
	entry := newPodGangEntry(epoch, pcsGenerationHash, nil)
	entry.Role = grovecorev1alpha1.PodGangEntryRoleScaleOut
	entryByEpoch[epoch] = &entry
	return &entry
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

// removeEmptyEntries drops entries whose PodClique pod counts are all zero and whose PCSG index
// slices are all empty. These arise from a scale-in where a PodGang's membership drained to zero.
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

// reconstructEntriesFromExistingPodGangs rebuilds PodGangMap entries from live BPG/SPG PodGangs
// on upgrade from a pre-coherent Grove version (see the reconstruction case in section 11.4 of
// the design). It assigns epoch E0 to the BPG (the entry with no grove.io/base-podgang label) with
// DependsOn nil, and epoch E1 > E0 to each SPG (an entry carrying the grove.io/base-podgang label)
// with DependsOn = &E0, so a gang-termination recreate keeps the BPG-first-then-SPG scheduling
// order. The BPG is identified by the absence of the base-podgang label rather than by carrying
// standalone PodClique pods, so a PCS whose cliques are all PCSG-owned (empty PodCliques on the
// BPG) still gets exactly one MPG. Returns an error if a PodGang's PodGroup names cannot be
// parsed.
func reconstructEntriesFromExistingPodGangs(pcs *grovecorev1alpha1.PodCliqueSet, existingPGs []groveschedulerv1alpha1.PodGang, pcsReplicaIndex int, clk clock.Clock) ([]grovecorev1alpha1.PodGangEntry, error) {
	var (
		pcsGenerationHash = *pcs.Status.CurrentGenerationHash
		bpgEpoch          = strconv.FormatInt(clk.Now().UnixNano(), 10)
		spgEpoch          = strconv.FormatInt(clk.Now().UnixNano()+1, 10)
	)

	pgEntries := make([]grovecorev1alpha1.PodGangEntry, 0, len(existingPGs))
	for i := range existingPGs {
		pgEntry, err := buildEntryFromPodGang(pcs, pcsReplicaIndex, pcsGenerationHash, existingPGs[i])
		if err != nil {
			return nil, groveerr.WrapError(err,
				errCodeReconstructPodGangMapEntry,
				component.OperationSync,
				fmt.Sprintf("Error reconstructing PodGangMap entry from PodGang %s for PodCliqueSet: %v", existingPGs[i].Name, client.ObjectKeyFromObject(pcs)),
			)
		}
		if _, isSPG := existingPGs[i].Labels[apicommon.LabelBasePodGang]; isSPG {
			// A scaled PodGang carries the base-podgang label pointing at its BPG. It depends on
			// the BPG and is not the MPG.
			pgEntry.Role = grovecorev1alpha1.PodGangEntryRoleTail
			pgEntry.Epoch = spgEpoch
			pgEntry.DependsOn = []string{bpgEpoch}
		} else {
			// The base PodGang carries no base-podgang label. It is the MPG.
			pgEntry.Role = grovecorev1alpha1.PodGangEntryRoleAnchor
			pgEntry.Epoch = bpgEpoch
		}
		pgEntries = append(pgEntries, *pgEntry)
	}
	return pgEntries, nil
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

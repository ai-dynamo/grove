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

package podcliquesetreplica

import (
	"cmp"
	"slices"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/utils"

	"k8s.io/apimachinery/pkg/util/sets"
)

// pcsReplicaRecreationPlanner selects new replacement mappings from the current resource snapshot and persisted
// commitments.
type pcsReplicaRecreationPlanner struct {
	pcs                 *grovecorev1beta1.PodCliqueSet
	replicaInfosByIndex pcsReplicaInfoByIndex
	scalingIndices      pcsReplicaScalingIndices
	replacementGroups   pcsReplicaReplacementGroups
}

// newPCSReplicaRecreationPlanner creates a planner for one reconciliation.
func newPCSReplicaRecreationPlanner(pcs *grovecorev1beta1.PodCliqueSet, replicaInfosByIndex pcsReplicaInfoByIndex, scalingIndices pcsReplicaScalingIndices) *pcsReplicaRecreationPlanner {
	return &pcsReplicaRecreationPlanner{
		pcs:                 pcs,
		replicaInfosByIndex: replicaInfosByIndex,
		scalingIndices:      scalingIndices,
		replacementGroups:   groupPCSReplicaReplacements(pcs.Status.UpdateProgress),
	}
}

// next returns the next replacement mapping to persist, or nil when no new replacement can be selected during the
// current reconciliation. A nil return does not mean replica recreation is complete. Persisted replacement work may
// still require execution or cleanup. The caller determines completion from persisted progress and observed resources.
func (p *pcsReplicaRecreationPlanner) next() *pcsReplicaReplacement {
	remainingSlots := max(0, pcsReplicaRecreationBudget(p.pcs)-len(p.replacementGroups.active))
	if remainingSlots == 0 {
		return nil
	}

	sourceReplicaIndices := p.selectSourceReplicaIndices(remainingSlots)
	if len(sourceReplicaIndices) == 0 {
		return nil
	}

	replacements := p.allocateReplacementTargets(sourceReplicaIndices)
	return &replacements[0]
}

// selectSourceReplicaIndices selects PCS replica indices for structural recreation.
//
// Selection follows these rules:
//   - PCS replicas already at the current generation are excluded.
//   - PCS scale-in reservations are excluded.
//   - Active replacement sources and targets are excluded.
//   - PCS scale-out reservations without a PCLQ or PCSG continue scale-out and are excluded.
//   - Scale-out reservations with some but not all expected PCLQs and PCSGs are eligible.
//   - Scale-out reservations with all expected PCLQs and PCSGs are excluded until scaling clears the reservation.
func (p *pcsReplicaRecreationPlanner) selectSourceReplicaIndices(remainingSlots int) []int {
	if remainingSlots <= 0 {
		return nil
	}

	scaleInPCSReplicaIndices := sets.New[int](p.scalingIndices.scaleIn...)
	scaleOutPCSReplicaIndices := sets.New[int](p.scalingIndices.scaleOut...)
	replacementGroups := p.replacementGroups
	replacementReplicaIndices := sets.New[int]()
	for _, replacement := range replacementGroups.active {
		replacementReplicaIndices.Insert(replacement.sourcePCSReplicaIndex, replacement.targetPCSReplicaIndex)
	}
	for _, replacement := range replacementGroups.canceled {
		replacementReplicaIndices.Insert(replacement.sourcePCSReplicaIndex, replacement.targetPCSReplicaIndex)
	}

	scaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs := sets.New(
		findScaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs(p.pcs, p.scalingIndices.scaleOut, p.replicaInfosByIndex)...)
	candidatePCSReplicaInfos := make([]pcsReplicaInfo, 0, len(p.replicaInfosByIndex))
	for pcsReplicaIndex, sourcePCSReplicaInfo := range p.replicaInfosByIndex {
		// replica index cannot be considered for recreation since it has been committed to be scaled-in.
		if scaleInPCSReplicaIndices.Has(pcsReplicaIndex) {
			continue
		}
		// replica index cannot be considered for recreation since it is participating as a source or target replica
		// index in an active recreation.
		if replacementReplicaIndices.Has(pcsReplicaIndex) {
			continue
		}
		// replica index cannot be considered for recreation since it's committed to be scale-out and not even a single
		// PCLQ or PCSG resource exists. So chances are that these resources will already see the updated PCS spec so no
		// recreation for this replica is required yet (it might change in the next reconcile).
		isScaleOutWithNoPCLQOrPCSG := scaleOutPCSReplicaIndices.Has(pcsReplicaIndex) &&
			len(sourcePCSReplicaInfo.standalonePCLQs) == 0 &&
			len(sourcePCSReplicaInfo.pcsgs) == 0
		if isScaleOutWithNoPCLQOrPCSG {
			continue
		}
		// replica index cannot be considered for recreation since it is actively scaling-out and all of its standalone
		// PCLQs and PCSGs have been created. However, it is still recorded as pending scale-out in PCS status. We allow
		// it to finish the scale-out and its subsequent removal from the PCS status before we consider this replica
		// for recreation.
		if scaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs.Has(pcsReplicaIndex) {
			continue
		}
		// replica index does not require recreation if all of its expected PCLQs and PCSGs have converged
		// to the current PCS generation.
		hasConvergedToCurrentPCSGeneration := p.pcs.Status.CurrentGenerationHash != nil &&
			sourcePCSReplicaInfo.hasConvergedToPCSGeneration(p.pcs, *p.pcs.Status.CurrentGenerationHash)
		if hasConvergedToCurrentPCSGeneration {
			continue
		}
		candidatePCSReplicaInfos = append(candidatePCSReplicaInfos, sourcePCSReplicaInfo)
	}

	slices.SortFunc(candidatePCSReplicaInfos, orderPCSReplicaInfoBySchedulingConfigRolloutPriority(p.pcs))
	numSelected := min(remainingSlots, len(candidatePCSReplicaInfos))
	return extractReplicaIndices(candidatePCSReplicaInfos[:numSelected])
}

// allocateReplacementTargets allocates one target PCS replica index for each selected source.
//
// Allocation follows these rules:
//   - Sources are processed in ascending PCS replica index order.
//   - Each source receives the lowest available target PCS replica index.
//   - Existing replicas, scaling reservations, and active replacements reserve their PCS replica indices.
func (p *pcsReplicaRecreationPlanner) allocateReplacementTargets(sourceReplicaIndices []int) []pcsReplicaReplacement {
	if len(sourceReplicaIndices) == 0 {
		return nil
	}

	reservedPCSReplicaIndices := p.replicaInfosByIndex.existingReplicaIndices()
	reservedPCSReplicaIndices.Insert(p.scalingIndices.activeScalingIndices()...)
	replacementGroups := p.replacementGroups
	for _, replacement := range replacementGroups.active {
		reservedPCSReplicaIndices.Insert(replacement.sourcePCSReplicaIndex, replacement.targetPCSReplicaIndex)
	}
	for _, replacement := range replacementGroups.canceled {
		reservedPCSReplicaIndices.Insert(replacement.sourcePCSReplicaIndex, replacement.targetPCSReplicaIndex)
	}

	sourceReplicaIndices = slices.Clone(sourceReplicaIndices)
	slices.Sort(sourceReplicaIndices)
	replacements := make([]pcsReplicaReplacement, 0, len(sourceReplicaIndices))
	for _, sourcePCSReplicaIndex := range sourceReplicaIndices {
		// Release the selected source index so a dense PCS can recreate the replica at the same index.
		reservedPCSReplicaIndices.Delete(sourcePCSReplicaIndex)
		targetPCSReplicaIndex := utils.GetAvailableIndices(reservedPCSReplicaIndices, 1)[0]
		replacements = append(replacements, pcsReplicaReplacement{
			sourcePCSReplicaIndex: sourcePCSReplicaIndex,
			targetPCSReplicaIndex: targetPCSReplicaIndex,
		})
		reservedPCSReplicaIndices.Insert(sourcePCSReplicaIndex, targetPCSReplicaIndex)
	}
	return replacements
}

// pcsReplicaRecreationBudget returns the number of concurrent PCS replica replacements allowed for the current update.
func pcsReplicaRecreationBudget(_ *grovecorev1beta1.PodCliqueSet) int {
	return 1
}

// pcsReplicaReplacement identifies a structural replacement from one PCS replica index to another.
// Its source and target correspond to ReplicaIndex and Replacement.TargetReplicaIndex in update progress.
type pcsReplicaReplacement struct {
	sourcePCSReplicaIndex int
	targetPCSReplicaIndex int
}

// pcsReplicaReplacementGroups contains incomplete replacement commitments grouped by lifecycle state.
type pcsReplicaReplacementGroups struct {
	// active contains incomplete replacements whose targets still contribute to the projected PCS identity set.
	active []pcsReplicaReplacement
	// canceled contains incomplete replacements whose sources and targets no longer contribute to the projected PCS
	// identity set but remain reserved until cleanup completes.
	canceled []pcsReplicaReplacement
}

// groupPCSReplicaReplacements returns incomplete replacement commitments grouped by lifecycle state and ordered by
// source PCS replica index within each group.
func groupPCSReplicaReplacements(progress *grovecorev1beta1.PodCliqueSetUpdateProgress) pcsReplicaReplacementGroups {
	if progress == nil || !progress.RequiresReplicaRecreation || progress.UpdateEndedAt != nil {
		return pcsReplicaReplacementGroups{}
	}

	groups := pcsReplicaReplacementGroups{}
	for _, replicaProgress := range progress.CurrentlyUpdating {
		if replicaProgress.UpdateEndedAt != nil || replicaProgress.Replacement == nil {
			continue
		}
		replacement := pcsReplicaReplacement{
			sourcePCSReplicaIndex: int(replicaProgress.ReplicaIndex),
			targetPCSReplicaIndex: int(replicaProgress.Replacement.TargetReplicaIndex),
		}
		if replicaProgress.Replacement.CanceledAt != nil {
			groups.canceled = append(groups.canceled, replacement)
			continue
		}
		groups.active = append(groups.active, replacement)
	}
	sortPCSReplicaReplacements(groups.active)
	sortPCSReplicaReplacements(groups.canceled)
	return groups
}

// sortPCSReplicaReplacements sorts replacements by source PCS replica index.
func sortPCSReplicaReplacements(replacements []pcsReplicaReplacement) {
	slices.SortFunc(replacements, func(a, b pcsReplicaReplacement) int {
		return cmp.Compare(a.sourcePCSReplicaIndex, b.sourcePCSReplicaIndex)
	})
}

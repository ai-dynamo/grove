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

var _ updateStrategyHooks = (*pcsReplicaRecreationStrategyHooks)(nil)

// pcsReplicaRecreationStrategyHooks coordinates persistent source-to-target PCS replica recreation.
//
// # Lifecycle for one source-to-target commitment
//
//	Pending → Committed → Recreating → Completed
//	Committed or Recreating → Canceled → Cleanup complete → Removed
//
// These conceptual states are derived from persisted progress and observed resources. They are not additional status
// fields.
//
// Committed means the source-to-target mapping is persisted and visible through the informer cache before resource
// changes begin.
//
// Same-index recreation crosses the SourceCleanupCompletedAt barrier before target creation. Different-index recreation
// can overlap source cleanup and target creation.
//
// Completion requires source and retired-source cleanup, target generation convergence, and target availability.
// Retired indices are cleared only after every applicable replica-scoped participant confirms cleanup. Materialization
// alone is not completion. Completed mappings remain recorded until the whole PCS update ends.
//
// Only active replacements consume the one-replacement budget. Active targets commit replica capacity. Canceled
// replacements and retired sources do not. Retired indices remain reserved until cleanup is confirmed, even when no
// resources are observed there. Canceled replacements reserve their source and target until removal.
//
// Scheduling supersession rebases active different-index replacements to same-index recreation at their reserved
// target. Previous sources become replacement-owned retired cleanup, which can continue during target creation. Active
// same-index replacements repeat source cleanup for the new target. Canceled replacements preserve their mapping,
// source-cleanup barrier, and remaining retired-source obligations.
//
// An operator restart resumes from persisted progress and observed resources.
//
// The whole PCS update completes only when no active or canceled commitment remains and every expected replica has
// converged and is available. Completion advances the applied scheduling hash to the recorded target.
type pcsReplicaRecreationStrategyHooks struct {
	pcs                 *grovecorev1beta1.PodCliqueSet
	replicaInfosByIndex pcsReplicaInfoByIndex
	scalingIndices      pcsReplicaScalingIndices
}

func newPCSReplicaRecreationStrategyHooks(
	pcs *grovecorev1beta1.PodCliqueSet,
	replicaInfosByIndex pcsReplicaInfoByIndex,
	scalingIndices pcsReplicaScalingIndices) *pcsReplicaRecreationStrategyHooks {
	return &pcsReplicaRecreationStrategyHooks{
		pcs:                 pcs,
		replicaInfosByIndex: replicaInfosByIndex,
		scalingIndices:      scalingIndices,
	}
}

// reconcileCurrentlyUpdatingReplicas reports active replacements whose source and retired-source cleanup has
// completed and whose target PCS replica has converged to the current generation and is available. The shared orchestrator stamps
// UpdateEndedAt on the returned source replica indices. It records no removals: a canceled replacement is
// removed by the post-sync cleanup once its resources are gone, and a completed replacement is retained with its
// mapping until the whole update ends.
func (h *pcsReplicaRecreationStrategyHooks) reconcileCurrentlyUpdatingReplicas(currentlyUpdating []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress) (reconcileCurrentlyUpdatingResult, error) {
	var completedPCSReplicaIndices []int
	for _, replicaProgress := range currentlyUpdating {
		replacement := replicaProgress.Replacement
		if replacement == nil || replacement.CanceledAt != nil || replicaProgress.UpdateEndedAt != nil {
			continue
		}
		if h.isReplacementComplete(replicaProgress) {
			completedPCSReplicaIndices = append(completedPCSReplicaIndices, int(replicaProgress.ReplicaIndex))
		}
	}
	return reconcileCurrentlyUpdatingResult{completedPCSReplicaIndices: completedPCSReplicaIndices}, nil
}

// isReplacementComplete reports whether a replacement's source and retired sources are cleaned up and its
// target PCS replica has converged to the current generation and is available. A retired source remains pending
// until replica-scoped participants confirm cleanup and its index is removed from replacement progress.
// It evaluates convergence only and does not consider lifecycle state, which the caller filters.
func (h *pcsReplicaRecreationStrategyHooks) isReplacementComplete(replicaProgress grovecorev1beta1.PodCliqueSetReplicaUpdateProgress) bool {
	replacement := replicaProgress.Replacement
	if len(replacement.RetiredSourceReplicaIndices) > 0 {
		return false
	}

	sourceReplicaIndex := int(replicaProgress.ReplicaIndex)
	targetReplicaIndex := int(replacement.TargetReplicaIndex)
	if !h.isReplacementSourceCleanedUp(sourceReplicaIndex, targetReplicaIndex, replacement) {
		return false
	}

	targetInfo := h.replicaInfosByIndex[targetReplicaIndex]
	targetInfo.index = targetReplicaIndex
	return targetInfo.isUpdateComplete(h.pcs)
}

// isReplacementSourceCleanedUp reports whether the replacement source resources are gone. A same-index
// replacement relies on the SourceCleanupCompletedAt barrier because the source keeps its headless Service. A
// different-index replacement is cleaned up once the source index has no observed resources.
func (h *pcsReplicaRecreationStrategyHooks) isReplacementSourceCleanedUp(sourceReplicaIndex, targetReplicaIndex int, replacement *grovecorev1beta1.PodCliqueSetReplicaReplacementProgress) bool {
	if sourceReplicaIndex == targetReplicaIndex {
		return replacement.SourceCleanupCompletedAt != nil
	}
	_, sourceExists := h.replicaInfosByIndex[sourceReplicaIndex]
	return !sourceExists
}

// selectReplicasToUpdate selects the next source-to-target PCS replica replacement.
func (h *pcsReplicaRecreationStrategyHooks) selectReplicasToUpdate() []pcsReplicaUpdateSelection {
	replacement := newPCSReplicaRecreationPlanner(h.pcs, h.replicaInfosByIndex, h.scalingIndices).next()
	if replacement == nil {
		return nil
	}
	return []pcsReplicaUpdateSelection{{
		replicaIndex:       replacement.sourcePCSReplicaIndex,
		targetReplicaIndex: &replacement.targetPCSReplicaIndex,
	}}
}

// isUpdateComplete reports whether the whole PCS replica recreation has finished. Recorded entries must
// satisfy completion and no unselected source may still require recreation. Ordinary provisioning at the
// target generation is not update progress. Resource loss does not invalidate a recorded completion.
// reconcileCurrentlyUpdatingReplicas stamps UpdateEndedAt before this check, so replacement grouping
// reflects completions recorded in the current reconciliation.
func (h *pcsReplicaRecreationStrategyHooks) isUpdateComplete() bool {
	groups := groupPCSReplicaReplacements(h.pcs.Status.UpdateProgress)
	if len(groups.active) > 0 || len(groups.canceled) > 0 {
		return false
	}

	for _, replicaProgress := range h.pcs.Status.UpdateProgress.CurrentlyUpdating {
		targetPCSReplicaIndex := int(replicaProgress.ReplicaIndex)
		if replicaProgress.Replacement != nil {
			targetPCSReplicaIndex = int(replicaProgress.Replacement.TargetReplicaIndex)
		}

		replicaInfo := h.replicaInfosByIndex[targetPCSReplicaIndex]
		replicaInfo.index = targetPCSReplicaIndex
		if !replicaInfo.isUpdateComplete(h.pcs) {
			return false
		}
	}

	pendingSourcePCSReplicaIndices := newPCSReplicaRecreationPlanner(
		h.pcs,
		h.replicaInfosByIndex,
		h.scalingIndices,
	).selectSourceReplicaIndices(1)
	return len(pendingSourcePCSReplicaIndices) == 0
}

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
// Selection follows these rules
//   - Completed source keys and completed target identities are excluded.
//   - PCS replicas whose observed workload resources apply the current generation are excluded.
//   - PCS scale-in reservations are excluded.
//   - Active and canceled replacement sources and targets are excluded.
//   - PCS scale-out reservations without a PCLQ or PCSG continue scale-out and are excluded.
//   - Scale-out reservations with partial workload resources at an older generation are eligible.
//   - Scale-out reservations with all expected PCLQs and PCSGs are excluded until scaling clears the reservation.
func (p *pcsReplicaRecreationPlanner) selectSourceReplicaIndices(remainingSlots int) []int {
	if remainingSlots <= 0 {
		return nil
	}

	scaleInPCSReplicaIndices := sets.New[int](p.scalingIndices.scaleIn...)
	scaleOutPCSReplicaIndices := sets.New[int](p.scalingIndices.scaleOut...)
	replacementReplicaIndices := p.replacementGroups.reservedReplicaIndices()
	completedSourcePCSReplicaIndices, completedTargetPCSReplicaIndices := collectCompletedPCSReplicaUpdateIndices(p.pcs.Status.UpdateProgress)
	scaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs := sets.New(findScaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs(p.pcs, p.scalingIndices.scaleOut, p.replicaInfosByIndex)...)

	candidatePCSReplicaInfos := make([]pcsReplicaInfo, 0, len(p.replicaInfosByIndex))
	for pcsReplicaIndex, sourcePCSReplicaInfo := range p.replicaInfosByIndex {
		// replica index cannot be considered for recreation since it has been committed to be scaled-in.
		if scaleInPCSReplicaIndices.Has(pcsReplicaIndex) {
			continue
		}
		// replica index cannot be considered for recreation since it is participating as a source or target replica
		// index in an active recreation or remains reserved for canceled replacement cleanup.
		if replacementReplicaIndices.Has(pcsReplicaIndex) {
			continue
		}
		if completedSourcePCSReplicaIndices.Has(pcsReplicaIndex) || completedTargetPCSReplicaIndices.Has(pcsReplicaIndex) {
			continue
		}
		sourcePCSReplicaInfo.index = pcsReplicaIndex
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
		// Missing children of resources already applying the target specification are ordinary
		// provisioning work, not a reason to select another replacement.
		hasObservedWorkloadResources := len(sourcePCSReplicaInfo.standalonePCLQs) > 0 || len(sourcePCSReplicaInfo.pcsgs) > 0
		hasAppliedCurrentPCSGeneration := p.pcs.Status.CurrentGenerationHash != nil && hasObservedWorkloadResources &&
			sourcePCSReplicaInfo.haveObservedResourcesAppliedPCSGeneration(*p.pcs.Status.CurrentGenerationHash)
		if hasAppliedCurrentPCSGeneration {
			continue
		}
		candidatePCSReplicaInfos = append(candidatePCSReplicaInfos, sourcePCSReplicaInfo)
	}

	slices.SortFunc(candidatePCSReplicaInfos, orderPCSReplicaInfoBySchedulingConfigRolloutPriority(p.pcs))
	numSelected := min(remainingSlots, len(candidatePCSReplicaInfos))
	return extractReplicaIndices(candidatePCSReplicaInfos[:numSelected])
}

// collectCompletedPCSReplicaUpdateIndices returns owned sets of completed source keys and target identities.
// A completed child-resource update contributes its unchanged replica index to both sets. A completed
// replacement contributes its source key and replacement target separately. Unfinished entries are ignored.
func collectCompletedPCSReplicaUpdateIndices(
	updateProgress *grovecorev1beta1.PodCliqueSetUpdateProgress,
) (completedSourcePCSReplicaIndices, completedTargetPCSReplicaIndices sets.Set[int]) {
	completedSourcePCSReplicaIndices = sets.New[int]()
	completedTargetPCSReplicaIndices = sets.New[int]()
	if updateProgress == nil {
		return completedSourcePCSReplicaIndices, completedTargetPCSReplicaIndices
	}

	for _, replicaProgress := range updateProgress.CurrentlyUpdating {
		if replicaProgress.UpdateEndedAt != nil {
			completedSourcePCSReplicaIndices.Insert(int(replicaProgress.ReplicaIndex))
			targetPCSReplicaIndex := replicaProgress.ReplicaIndex
			if replicaProgress.Replacement != nil {
				targetPCSReplicaIndex = replicaProgress.Replacement.TargetReplicaIndex
			}
			completedTargetPCSReplicaIndices.Insert(int(targetPCSReplicaIndex))
		}
	}
	return completedSourcePCSReplicaIndices, completedTargetPCSReplicaIndices
}

// allocateReplacementTargets allocates one target PCS replica index for each selected source.
//
// Allocation follows these rules
//   - Sources are processed in ascending PCS replica index order.
//   - Each source receives the lowest available target PCS replica index.
//   - Existing replicas, scaling reservations, and active and canceled replacements reserve their PCS replica indices.
func (p *pcsReplicaRecreationPlanner) allocateReplacementTargets(sourceReplicaIndices []int) []pcsReplicaReplacement {
	if len(sourceReplicaIndices) == 0 {
		return nil
	}

	reservedPCSReplicaIndices := p.replicaInfosByIndex.existingReplicaIndices()
	reservedPCSReplicaIndices.Insert(p.scalingIndices.activeScalingIndices()...)
	reservedPCSReplicaIndices.Insert(p.replacementGroups.reservedReplicaIndices().UnsortedList()...)

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
	// sourcePCSReplicaIndex is the source index recorded in ReplicaIndex of the replacement's update progress.
	// It equals targetPCSReplicaIndex for same-index recreation.
	sourcePCSReplicaIndex int
	// targetPCSReplicaIndex is the intended replacement index recorded in Replacement.TargetReplicaIndex.
	// Active replacements commit one PCS replica at this index. Canceled replacements retain the index
	// only for cleanup.
	targetPCSReplicaIndex int
	// retiredSourcePCSReplicaIndices contains previous sources with pending replacement-owned cleanup.
	// They identify cleanup work rather than replica capacity committed to the workload.
	retiredSourcePCSReplicaIndices []int
}

// pcsReplicaReplacementGroups contains incomplete replacement commitments grouped by lifecycle state.
type pcsReplicaReplacementGroups struct {
	// active contains incomplete replacements whose targets contribute to the committed PCS replica count.
	active []pcsReplicaReplacement
	// canceled contains incomplete replacements whose sources and targets do not contribute to the committed
	// PCS replica count but remain reserved until cleanup completes.
	canceled []pcsReplicaReplacement
}

// groupPCSReplicaReplacements returns incomplete replacement commitments grouped by lifecycle state and ordered by
// source PCS replica index within each group. Returned retired-source slices do not alias the update progress.
func groupPCSReplicaReplacements(progress *grovecorev1beta1.PodCliqueSetUpdateProgress) pcsReplicaReplacementGroups {
	if progress == nil || !progress.RequiresReplicaRecreation || progress.UpdateEndedAt != nil {
		return pcsReplicaReplacementGroups{}
	}

	groups := pcsReplicaReplacementGroups{}
	for _, replicaProgress := range progress.CurrentlyUpdating {
		replacementProgress := replicaProgress.Replacement
		if replicaProgress.UpdateEndedAt != nil || replacementProgress == nil {
			continue
		}

		replacement := pcsReplicaReplacement{
			sourcePCSReplicaIndex: int(replicaProgress.ReplicaIndex),
			targetPCSReplicaIndex: int(replacementProgress.TargetReplicaIndex),
		}
		if len(replacementProgress.RetiredSourceReplicaIndices) > 0 {
			replacement.retiredSourcePCSReplicaIndices = make([]int, len(replacementProgress.RetiredSourceReplicaIndices))
			for i, pcsReplicaIndex := range replacementProgress.RetiredSourceReplicaIndices {
				replacement.retiredSourcePCSReplicaIndices[i] = int(pcsReplicaIndex)
			}
		}

		if replacementProgress.CanceledAt != nil {
			groups.canceled = append(groups.canceled, replacement)
			continue
		}
		groups.active = append(groups.active, replacement)
	}
	sortPCSReplicaReplacements(groups.active)
	sortPCSReplicaReplacements(groups.canceled)
	return groups
}

// reservedReplicaIndices returns a new set containing the source, target, and retired-source
// indices of every active and canceled replacement.
func (g pcsReplicaReplacementGroups) reservedReplicaIndices() sets.Set[int] {
	indices := sets.New[int]()
	for _, replacement := range g.active {
		indices.Insert(replacement.sourcePCSReplicaIndex, replacement.targetPCSReplicaIndex)
		indices.Insert(replacement.retiredSourcePCSReplicaIndices...)
	}
	for _, replacement := range g.canceled {
		indices.Insert(replacement.sourcePCSReplicaIndex, replacement.targetPCSReplicaIndex)
		indices.Insert(replacement.retiredSourcePCSReplicaIndices...)
	}
	return indices
}

// sortPCSReplicaReplacements sorts replacements by source PCS replica index.
func sortPCSReplicaReplacements(replacements []pcsReplicaReplacement) {
	slices.SortFunc(replacements, func(a, b pcsReplicaReplacement) int {
		return cmp.Compare(a.sourcePCSReplicaIndex, b.sourcePCSReplicaIndex)
	})
}

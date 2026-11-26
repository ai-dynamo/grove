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

package podcliquesetreplica

import (
	"cmp"
	"maps"
	"slices"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// reconcileCurrentlyUpdatingResult contains status changes identified while reconciling selected PCS replicas.
type reconcileCurrentlyUpdatingResult struct {
	// completedPCSReplicaIndices identifies PCS replica updates that completed successfully. The shared
	// advancement stamps UpdateEndedAt on the corresponding CurrentlyUpdating entries.
	completedPCSReplicaIndices []int
	// pcsReplicaUpdateIndicesToRemove identifies CurrentlyUpdating entries that no longer participate
	// in the rolling update and should be removed without stamping UpdateEndedAt. For configured strategies,
	// this includes replicas removed from update eligibility by PCS scale-in or gang termination.
	pcsReplicaUpdateIndicesToRemove []int
}

// updateStrategyHooks supplies behavior that varies between rolling update strategies.
type updateStrategyHooks interface {
	// reconcileCurrentlyUpdatingReplicas updates execution-specific fields on the supplied deep-copied
	// progress entries. It reports completed updates and entries whose work and cleanup have finished so
	// shared advancement can remove them. It does not persist status or stamp UpdateEndedAt.
	reconcileCurrentlyUpdatingReplicas(currentlyUpdating []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress) (reconcileCurrentlyUpdatingResult, error)
	// selectReplicasToUpdate returns PodCliqueSet replicas newly selected for update. A nil result indicates
	// that there is no further update possible now. It does not imply that the update is complete.
	selectReplicasToUpdate() []pcsReplicaUpdateSelection
	// isUpdateComplete reports whether every relevant PodCliqueSet replica converged to the update target.
	isUpdateComplete() bool
}

// pcsReplicaUpdateSelection identifies a PodCliqueSet replica selected for update.
type pcsReplicaUpdateSelection struct {
	// replicaIndex is the PodCliqueSet replica index selected for update. It identifies the source
	// replica during replica recreation.
	replicaIndex int
	// targetReplicaIndex is the target PodCliqueSet replica index allocated during replica recreation.
	// It is nil for RollingRecreate and Coherent updates.
	targetReplicaIndex *int
}

// newUpdateStrategyHooks returns the hooks for the active PCS update execution path.
// RequiresReplicaRecreation takes precedence over the configured update strategy.
func newUpdateStrategyHooks(
	pcs *grovecorev1beta1.PodCliqueSet,
	replicaInfosByIndex pcsReplicaInfoByIndex,
	pcsReplicaIndicesAfterScaling sets.Set[int],
	delWork *deletionWork) updateStrategyHooks {
	if pcs.Status.UpdateProgress.RequiresReplicaRecreation {
		return newPCSReplicaRecreationStrategyHooks(
			pcs,
			replicaInfosByIndex,
			replicaScalingIndicesFromProgress(pcs.Status.ScalingProgress),
		)
	}
	replicaInfos := pcsReplicaInfosForUpdate(replicaInfosByIndex, sortedReplicaIndices(pcsReplicaIndicesAfterScaling), delWork.pcsIndicesToTerminate)
	minAvailableBreachedPCSReplicaIndices := slices.Sorted(maps.Keys(delWork.minAvailableBreachedConstituents))
	if componentutils.IsCoherentUpdateInProgress(pcs) {
		return newCoherentStrategyHooks(pcs, replicaInfos, minAvailableBreachedPCSReplicaIndices)
	}
	return newRollingRecreateStrategyHooks(pcs, replicaInfos, minAvailableBreachedPCSReplicaIndices)
}

// advancePCSUpdateProgress advances the PCS update on the caller's working copy.
//   - The strategy hooks must reference the same working copy.
//   - Progress is not persisted and no requeue decision is made.
//   - The caller must discard the working copy on error.
func advancePCSUpdateProgress(pcs *grovecorev1beta1.PodCliqueSet, updateHooks updateStrategyHooks, reconcileTime metav1.Time) error {
	currentlyUpdating := pcs.Status.UpdateProgress.CurrentlyUpdating
	reconcileResult, err := updateHooks.reconcileCurrentlyUpdatingReplicas(currentlyUpdating)
	if err != nil {
		return err
	}
	currentlyUpdating = removeObsoletePCSReplicaUpdateEntries(currentlyUpdating, reconcileResult.pcsReplicaUpdateIndicesToRemove)
	markPCSReplicaUpdatesCompleted(currentlyUpdating, reconcileResult.completedPCSReplicaIndices, reconcileTime)
	pcs.Status.UpdateProgress.CurrentlyUpdating = currentlyUpdating

	if updateHooks.isUpdateComplete() {
		pcs.Status.UpdateProgress.UpdateEndedAt = &reconcileTime
		pcs.Status.UpdateProgress.CurrentlyUpdating = nil
		pcs.Status.AppliedSchedulingConfigurationHash = new(pcs.Status.UpdateProgress.TargetSchedulingConfigurationHash)
		return nil
	}
	selectedPCSReplicas := updateHooks.selectReplicasToUpdate()
	pcs.Status.UpdateProgress.CurrentlyUpdating = mergeReplicaUpdateSelections(currentlyUpdating, selectedPCSReplicas, reconcileTime)
	return nil
}

// removeObsoletePCSReplicaUpdateEntries removes entries that no longer participate without marking them successful.
func removeObsoletePCSReplicaUpdateEntries(currentlyUpdating []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress, pcsReplicaIndicesToRemove []int) []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress {
	if len(pcsReplicaIndicesToRemove) == 0 {
		return currentlyUpdating
	}

	indicesToRemove := sets.New(pcsReplicaIndicesToRemove...)
	return slices.DeleteFunc(currentlyUpdating, func(progress grovecorev1beta1.PodCliqueSetReplicaUpdateProgress) bool {
		return indicesToRemove.Has(int(progress.ReplicaIndex))
	})
}

// cancelReplacementMessage records why a PodCliqueSet replica replacement was canceled.
const cancelReplacementMessage = "replacement canceled because its target was selected for PodCliqueSet scale-in"

// cancelReplacements stamps CanceledAt and a cancellation message on the active replacements whose source
// replica index is in sourceReplicaIndicesToCancel. An entry without a replacement, an already-canceled entry,
// or an absent source index is left unchanged, so the operation is idempotent.
func cancelReplacements(currentlyUpdating []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress, sourceReplicaIndicesToCancel []int, reconcileTime metav1.Time) {
	if len(sourceReplicaIndicesToCancel) == 0 {
		return
	}
	sourcesToCancel := sets.New(sourceReplicaIndicesToCancel...)
	message := cancelReplacementMessage
	for i := range currentlyUpdating {
		replicaProgress := &currentlyUpdating[i]
		if replicaProgress.Replacement == nil || replicaProgress.Replacement.CanceledAt != nil {
			continue
		}
		if sourcesToCancel.Has(int(replicaProgress.ReplicaIndex)) {
			replicaProgress.Replacement.CanceledAt = &reconcileTime
			replicaProgress.Replacement.CancellationMessage = &message
		}
	}
}

// markPCSReplicaUpdatesCompleted stamps UpdateEndedAt on completed PCS replica updates.
func markPCSReplicaUpdatesCompleted(currentlyUpdating []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress, completedPCSReplicaIndices []int, reconcileTime metav1.Time) {
	if len(completedPCSReplicaIndices) == 0 {
		return
	}

	completedIndices := sets.New(completedPCSReplicaIndices...)
	for i := range currentlyUpdating {
		replicaProgress := &currentlyUpdating[i]
		if replicaProgress.UpdateEndedAt == nil && completedIndices.Has(int(replicaProgress.ReplicaIndex)) {
			replicaProgress.UpdateEndedAt = &reconcileTime
		}
	}
}

// mergeReplicaUpdateSelections merges newly selected PCS replicas into the keyed CurrentlyUpdating list.
func mergeReplicaUpdateSelections(currentlyUpdating []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress, selections []pcsReplicaUpdateSelection, reconcileTime metav1.Time) []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress {
	// A completed child-resource update (rolling-recreate or coherent) keeps the same PCS replica index, so its progress no longer
	// carries information needed by later selections. Completed replacement mappings are retained
	// because their source and target PCS replica indices may differ.
	currentlyUpdating = slices.DeleteFunc(currentlyUpdating, func(progress grovecorev1beta1.PodCliqueSetReplicaUpdateProgress) bool {
		return progress.UpdateEndedAt != nil && progress.Replacement == nil
	})

	for _, selection := range selections {
		// Preserve progress already recorded for this replica. When a scheduling-configuration
		// change supersedes an active update, the existing entry has no Replacement yet. Attach
		// the newly selected replacement target to that entry instead of creating a duplicate.
		position := slices.IndexFunc(currentlyUpdating, func(progress grovecorev1beta1.PodCliqueSetReplicaUpdateProgress) bool {
			return progress.ReplicaIndex == int32(selection.replicaIndex)
		})
		if position >= 0 {
			if selection.targetReplicaIndex != nil {
				currentlyUpdating[position].Replacement = &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
					TargetReplicaIndex: int32(*selection.targetReplicaIndex),
				}
			}
			continue
		}

		// A selection without existing progress starts a new per-replica update. Replacement progress is
		// present only for PCS replica recreation. Child-resource updates leave Replacement nil.
		progress := grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex:    int32(selection.replicaIndex),
			UpdateStartedAt: reconcileTime,
		}
		if selection.targetReplicaIndex != nil {
			progress.Replacement = &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex: int32(*selection.targetReplicaIndex),
			}
		}
		currentlyUpdating = append(currentlyUpdating, progress)
	}

	// Preserve deterministic Kubernetes keyed-list ordering in persisted status.
	slices.SortFunc(currentlyUpdating, func(a, b grovecorev1beta1.PodCliqueSetReplicaUpdateProgress) int {
		return cmp.Compare(a.ReplicaIndex, b.ReplicaIndex)
	})
	return currentlyUpdating
}

// activePCSReplicaUpdateIndices returns incomplete selected PCS replica indices in ascending order.
func activePCSReplicaUpdateIndices(updateProgress *grovecorev1beta1.PodCliqueSetUpdateProgress) []int {
	if updateProgress == nil {
		return nil
	}

	var pcsReplicaIndices []int
	for _, replicaProgress := range updateProgress.CurrentlyUpdating {
		if replicaProgress.UpdateEndedAt == nil {
			pcsReplicaIndices = append(pcsReplicaIndices, int(replicaProgress.ReplicaIndex))
		}
	}
	slices.Sort(pcsReplicaIndices)
	return pcsReplicaIndices
}

// extractReplicaIndices returns the PCS replica indices represented by replicaInfos in the same order.
func extractReplicaIndices(replicaInfos []pcsReplicaInfo) []int {
	indices := make([]int, len(replicaInfos))
	for i, replicaInfo := range replicaInfos {
		indices[i] = replicaInfo.index
	}
	return indices
}

// pcsReplicaUpdateSelectionBudget returns how many additional PCS replicas may be selected for update.
// Issue #889 updates one PCS replica at a time. Issue #888 will derive this budget from PCS-level
// MaxUnavailable using count-anchored semantics: unavailable old-generation replicas remain replaceable,
// while unavailable current-generation replacements consume the budget.
func pcsReplicaUpdateSelectionBudget(
	_ *grovecorev1beta1.PodCliqueSet,
	numPCSReplicasStillUpdating int,
) int {
	return max(0, 1-numPCSReplicasStillUpdating)
}

// orderPCSReplicaInfoBySchedulingConfigRolloutPriority orders PCS replicas for a scheduling configuration
// rollout using these priority groups:
//  1. replicas that had not converged to the superseded PCS generation and are currently unavailable
//  2. replicas that had not converged to the superseded PCS generation and are currently available
//  3. replicas that had converged to the superseded PCS generation.
//
// Replica indices are ordered ascending within each group. Replicas already selected for update are retained
// separately and are not passed through this ordering.
func orderPCSReplicaInfoBySchedulingConfigRolloutPriority(pcs *grovecorev1beta1.PodCliqueSet) func(a, b pcsReplicaInfo) int {
	supersededGenerationHash := pcs.Status.UpdateProgress.PreviousPodCliqueSetGenerationHash
	return func(a, b pcsReplicaInfo) int {
		aConverged := supersededGenerationHash != nil && a.hasConvergedToPCSGeneration(pcs, *supersededGenerationHash)
		bConverged := supersededGenerationHash != nil && b.hasConvergedToPCSGeneration(pcs, *supersededGenerationHash)
		if aConverged != bConverged {
			if aConverged {
				return 1
			}
			return -1
		}
		if !aConverged {
			aAvailable := a.isAvailable(pcs)
			bAvailable := b.isAvailable(pcs)
			if aAvailable != bAvailable {
				if aAvailable {
					return 1
				}
				return -1
			}
		}
		return cmp.Compare(a.index, b.index)
	}
}

// reconcileCurrentlyUpdatingPCSReplicasForConfiguredStrategy classifies PCS replicas selected for the
// configured update strategy. Configured strategy means spec.updateStrategy, which is RollingRecreate or
// Coherent. It does not include controller-selected PCS replica recreation.
func reconcileCurrentlyUpdatingPCSReplicasForConfiguredStrategy(
	pcs *grovecorev1beta1.PodCliqueSet,
	replicaInfos []pcsReplicaInfo,
	currentlyUpdating []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress,
) (reconcileCurrentlyUpdatingResult, sets.Set[int]) {
	replicaInfosByIndex := make(map[int]pcsReplicaInfo, len(replicaInfos))
	for _, replicaInfo := range replicaInfos {
		replicaInfosByIndex[replicaInfo.index] = replicaInfo
	}

	result := reconcileCurrentlyUpdatingResult{}
	pcsReplicaIndicesStillUpdating := sets.New[int]()
	for _, progress := range currentlyUpdating {
		if progress.UpdateEndedAt != nil {
			continue
		}

		pcsReplicaIndex := int(progress.ReplicaIndex)
		replicaInfo, isEligibleForUpdate := replicaInfosByIndex[pcsReplicaIndex]
		if !isEligibleForUpdate {
			result.pcsReplicaUpdateIndicesToRemove = append(result.pcsReplicaUpdateIndicesToRemove, pcsReplicaIndex)
			continue
		}
		if replicaInfo.isUpdateComplete(pcs) {
			result.completedPCSReplicaIndices = append(result.completedPCSReplicaIndices, pcsReplicaIndex)
			continue
		}
		pcsReplicaIndicesStillUpdating.Insert(pcsReplicaIndex)
	}
	return result, pcsReplicaIndicesStillUpdating
}

// selectPCSReplicasForConfiguredStrategy selects PCS replicas that should execute the configured update
// strategy next. Configured strategy is the rolling update strategy set via spec.updateStrategy,
// which is RollingRecreate or Coherent. It does not include controller-selected PCS replica recreation.
func selectPCSReplicasForConfiguredStrategy(
	pcs *grovecorev1beta1.PodCliqueSet,
	replicaInfos []pcsReplicaInfo,
	selectedPCSReplicaIndices sets.Set[int],
	minAvailableBreachedPCSReplicaIndices []int,
) []pcsReplicaUpdateSelection {
	remainingSlots := pcsReplicaUpdateSelectionBudget(pcs, selectedPCSReplicaIndices.Len())
	if remainingSlots <= 0 {
		return nil
	}

	var pendingReplicaInfos []pcsReplicaInfo
	for _, replicaInfo := range replicaInfos {
		if selectedPCSReplicaIndices.Has(replicaInfo.index) || replicaInfo.isUpdateComplete(pcs) {
			continue
		}
		pendingReplicaInfos = append(pendingReplicaInfos, replicaInfo)
	}

	slices.SortFunc(pendingReplicaInfos, orderPCSReplicaInfo(pcs, minAvailableBreachedPCSReplicaIndices))
	numReplicasToSelect := min(remainingSlots, len(pendingReplicaInfos))
	selections := make([]pcsReplicaUpdateSelection, numReplicasToSelect)
	for i, replicaInfo := range pendingReplicaInfos[:numReplicasToSelect] {
		selections[i] = pcsReplicaUpdateSelection{replicaIndex: replicaInfo.index}
	}
	return selections
}

// isConfiguredStrategyUpdateComplete reports whether every relevant PCS replica completed the configured
// update strategy. Configured strategy means spec.updateStrategy, which is RollingRecreate or Coherent. It
// does not include controller-selected PCS replica recreation.
func isConfiguredStrategyUpdateComplete(pcs *grovecorev1beta1.PodCliqueSet, replicaInfos []pcsReplicaInfo) bool {
	for i := range replicaInfos {
		if !replicaInfos[i].isUpdateComplete(pcs) {
			return false
		}
	}
	return true
}

// orderPCSReplicaInfo returns a comparison function for prioritizing updates that use the configured update strategy.
func orderPCSReplicaInfo(pcs *grovecorev1beta1.PodCliqueSet, minAvailableBreachedPCSReplicaIndices []int) func(a, b pcsReplicaInfo) int {
	return func(a, b pcsReplicaInfo) int {
		scheduledPodsInA, scheduledPodsInB := a.getNumScheduledPods(pcs), b.getNumScheduledPods(pcs)
		// 1. Pick the PCS Replica that has no scheduled pods.
		if scheduledPodsInA == 0 && scheduledPodsInB != 0 {
			return -1
		} else if scheduledPodsInA != 0 && scheduledPodsInB == 0 {
			return 1
		}

		// 2. Pick the replicas which have the minAvailableBreached condition set to true, but the terminationDelay has not expired yet.
		// The replicas with minAvailableBreached with terminationDelay expired are deleted before the rolling update is started.
		minAvailableBreachedForA := slices.Contains(minAvailableBreachedPCSReplicaIndices, a.index)
		minAvailableBreachedForB := slices.Contains(minAvailableBreachedPCSReplicaIndices, b.index)
		if minAvailableBreachedForA && !minAvailableBreachedForB {
			return -1
		} else if !minAvailableBreachedForA && minAvailableBreachedForB {
			return 1
		}

		// 3. If all replicas are healthy, then pick the replicas in ascending ordinal value.
		return cmp.Compare(a.index, b.index)
	}
}

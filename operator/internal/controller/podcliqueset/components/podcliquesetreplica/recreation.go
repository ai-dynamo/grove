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
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"k8s.io/apimachinery/pkg/util/sets"
)

// pcsReplicaReplacement identifies a structural replacement from one PCS replica index to another.
// Its source and target correspond to ReplicaIndex and Replacement.TargetReplicaIndex in update progress.
type pcsReplicaReplacement struct {
	sourcePCSReplicaIndex int
	targetPCSReplicaIndex int
}

// getActivePCSReplicaReplacements returns incomplete structural replacements ordered by source PCS replica index.
func getActivePCSReplicaReplacements(pcs *grovecorev1beta1.PodCliqueSet) []pcsReplicaReplacement {
	if !componentutils.IsPCSReplicaRecreationInProgress(pcs) {
		return nil
	}
	replacements := make([]pcsReplicaReplacement, 0, len(pcs.Status.UpdateProgress.CurrentlyUpdating))
	for _, updateProgress := range pcs.Status.UpdateProgress.CurrentlyUpdating {
		if updateProgress.UpdateEndedAt != nil || updateProgress.Replacement == nil {
			continue
		}
		replacements = append(replacements, pcsReplicaReplacement{
			sourcePCSReplicaIndex: int(updateProgress.ReplicaIndex),
			targetPCSReplicaIndex: int(updateProgress.Replacement.TargetReplicaIndex),
		})
	}
	slices.SortFunc(replacements, func(a, b pcsReplicaReplacement) int {
		return cmp.Compare(a.sourcePCSReplicaIndex, b.sourcePCSReplicaIndex)
	})
	return replacements
}

// selectPCSReplicaRecreationSourceIndices selects PCS replica indices for structural recreation.
//
// Selection follows these rules:
//   - PCS replicas already at the current generation are excluded.
//   - Ordinary scale-in reservations are excluded.
//   - Active replacement sources and targets are excluded.
//   - Scale-out reservations without a PCLQ or PCSG continue ordinary scale-out and are excluded.
//   - Scale-out reservations with some but not all expected PCLQs and PCSGs are eligible.
//   - Scale-out reservations with all expected PCLQs and PCSGs are excluded until scaling clears the reservation.
func selectPCSReplicaRecreationSourceIndices(
	pcs *grovecorev1beta1.PodCliqueSet,
	pcsReplicaInfosByIndex pcsReplicaInfoByIndex,
	pcsReplicaScalingIndices pcsReplicaScalingIndices,
	remainingSlots int) []int {

	if remainingSlots <= 0 {
		return nil
	}

	scaleInPCSReplicaIndices := sets.New[int](pcsReplicaScalingIndices.scaleIn...)
	scaleOutPCSReplicaIndices := sets.New[int](pcsReplicaScalingIndices.scaleOut...)
	activePCSReplacementReplicaIndices := sets.New[int]()
	for _, replacement := range getActivePCSReplicaReplacements(pcs) {
		activePCSReplacementReplicaIndices.Insert(replacement.sourcePCSReplicaIndex, replacement.targetPCSReplicaIndex)
	}

	scaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs := sets.New(
		findScaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs(pcs, pcsReplicaScalingIndices.scaleOut, pcsReplicaInfosByIndex)...)
	candidatePCSReplicaInfos := make([]pcsReplicaInfo, 0, len(pcsReplicaInfosByIndex))
	for pcsReplicaIndex, sourcePCSReplicaInfo := range pcsReplicaInfosByIndex {
		// replica index cannot be considered for recreation since it has been committed to be scaled-in.
		if scaleInPCSReplicaIndices.Has(pcsReplicaIndex) {
			continue
		}
		// replica index cannot be considered for recreation since it is participating as a source or target replica index in an active recreation.
		if activePCSReplacementReplicaIndices.Has(pcsReplicaIndex) {
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
		// replica index does not require recreation if all of its expected PCLQs and PCSGs have converged to the current PCS generation.
		hasConvergedToCurrentPCSGeneration := pcs.Status.CurrentGenerationHash != nil &&
			sourcePCSReplicaInfo.hasConvergedToPCSGeneration(pcs, *pcs.Status.CurrentGenerationHash)
		if hasConvergedToCurrentPCSGeneration {
			continue
		}
		candidatePCSReplicaInfos = append(candidatePCSReplicaInfos, sourcePCSReplicaInfo)
	}

	slices.SortFunc(candidatePCSReplicaInfos, orderPCSReplicaInfoBySchedulingConfigRolloutPriority(pcs))
	numSelected := min(remainingSlots, len(candidatePCSReplicaInfos))
	return extractReplicaIndices(candidatePCSReplicaInfos[:numSelected])
}

// allocatePCSReplicaReplacementTargets allocates one target PCS replica index for each selected source.
//
// Allocation follows these rules:
//   - Sources are processed in ascending PCS replica index order.
//   - Each source receives the lowest available target PCS replica index.
//   - Existing replicas, scaling reservations, and active replacements reserve their PCS replica indices.
func allocatePCSReplicaReplacementTargets(
	pcs *grovecorev1beta1.PodCliqueSet,
	existingPCSReplicaIndices sets.Set[int],
	selectedSourcePCSReplicaIndices []int) []pcsReplicaReplacement {
	if len(selectedSourcePCSReplicaIndices) == 0 {
		return nil
	}

	reservedPCSReplicaIndices := existingPCSReplicaIndices.Clone()
	activePCSReplicaScalingIndices := replicaScalingIndicesFromProgress(pcs.Status.ScalingProgress)
	reservedPCSReplicaIndices.Insert(activePCSReplicaScalingIndices.activeScalingIndices()...)
	for _, replacement := range getActivePCSReplicaReplacements(pcs) {
		reservedPCSReplicaIndices.Insert(replacement.sourcePCSReplicaIndex, replacement.targetPCSReplicaIndex)
	}

	selectedSourcePCSReplicaIndices = slices.Clone(selectedSourcePCSReplicaIndices)
	slices.Sort(selectedSourcePCSReplicaIndices)
	replacements := make([]pcsReplicaReplacement, 0, len(selectedSourcePCSReplicaIndices))
	for _, sourcePCSReplicaIndex := range selectedSourcePCSReplicaIndices {
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

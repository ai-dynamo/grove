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
	"context"
	"fmt"
	"slices"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"github.com/go-logr/logr"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// orchestrateRollingUpdate drives a RollingRecreate or Coherent update for the PodCliqueSet replicas.
func (r *_resource) orchestrateRollingUpdate(
	ctx context.Context,
	logger logr.Logger,
	pcs *grovecorev1beta1.PodCliqueSet,
	replicaInfos []pcsReplicaInfo,
	minAvailableBreachedPCSReplicaIndices []int,
) error {
	activelyUpdatedReplicaInfos := currentlyUpdatingReplicaInfos(pcs, replicaInfos)
	var (
		replicaInfosWithCompletedUpdate []pcsReplicaInfo
		replicaInfosStillUpdating       []pcsReplicaInfo
	)
	for _, updatingReplicaInfo := range activelyUpdatedReplicaInfos {
		if updatingReplicaInfo.isUpdateComplete(pcs) {
			replicaInfosWithCompletedUpdate = append(replicaInfosWithCompletedUpdate, updatingReplicaInfo)
			continue
		}
		replicaInfosStillUpdating = append(replicaInfosStillUpdating, updatingReplicaInfo)
		// A Coherent update records its in-flight epochs and the components it is still waiting on. The
		// PodGangMap component owns the advance, so this only writes observability status.
		if componentutils.IsCoherentUpdateInProgress(pcs) {
			if err := r.updateCoherentReplicaProgress(ctx, logger, pcs, updatingReplicaInfo); err != nil {
				return err
			}
		}
	}

	if err := r.markUpdateEndForReplicas(ctx, logger, pcs, replicaInfosWithCompletedUpdate); err != nil {
		return err
	}

	remainingSelectionBudget := pcsReplicaUpdateSelectionBudget(pcs, replicaInfosStillUpdating)
	selectedReplicaInfos := selectReplicasToUpdate(
		pcs,
		replicaInfos,
		replicaInfosStillUpdating,
		minAvailableBreachedPCSReplicaIndices,
		remainingSelectionBudget,
	)
	if err := r.updatePCSWithSelectedReplicas(ctx, logger, pcs, selectedReplicaInfos); err != nil {
		return err
	}
	if len(selectedReplicaInfos) == 0 {
		return nil
	}
	return groveerr.NewContinueReconcileAndRequeue(
		component.OperationSync,
		fmt.Sprintf("rolling update of PodCliqueSet replica indices %v is in progress", replicaIndices(selectedReplicaInfos)),
	)
}

// currentlyUpdatingReplicaInfos returns selected PCS replicas that remain expected after scaling and
// termination decisions. The result is sorted by ascending PCS replica index.
func currentlyUpdatingReplicaInfos(pcs *grovecorev1beta1.PodCliqueSet, replicaInfos []pcsReplicaInfo) []pcsReplicaInfo {
	replicasSelectedForUpdate := sets.New(componentutils.PCSReplicaIndicesSelectedForUpdate(pcs)...)
	if replicasSelectedForUpdate.Len() == 0 {
		return nil
	}
	activelyUpdatedReplicaInfos := make([]pcsReplicaInfo, 0, replicasSelectedForUpdate.Len())
	for _, replicaInfo := range replicaInfos {
		if replicasSelectedForUpdate.Has(int32(replicaInfo.replicaIndex)) {
			activelyUpdatedReplicaInfos = append(activelyUpdatedReplicaInfos, replicaInfo)
		}
	}
	slices.SortFunc(activelyUpdatedReplicaInfos, func(a, b pcsReplicaInfo) int {
		return cmp.Compare(a.replicaIndex, b.replicaIndex)
	})
	return activelyUpdatedReplicaInfos
}

// markUpdateEndForReplicas stamps UpdateEndedAt on selected PCS replicas that have fully converged.
// Aggregate update progress counts are derived during status reconciliation and are not maintained here.
func (r *_resource) markUpdateEndForReplicas(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, replicaInfosWithCompletedUpdate []pcsReplicaInfo) error {
	if len(replicaInfosWithCompletedUpdate) == 0 {
		return nil
	}
	completedReplicaIndices := sets.New(replicaIndices(replicaInfosWithCompletedUpdate)...)
	original := pcs.DeepCopy()
	updateEndedAt := metav1.Now()
	for i := range pcs.Status.UpdateProgress.CurrentlyUpdating {
		progress := &pcs.Status.UpdateProgress.CurrentlyUpdating[i]
		if completedReplicaIndices.Has(int(progress.ReplicaIndex)) {
			progress.UpdateEndedAt = &updateEndedAt
		}
	}
	if err := r.patchUpdateProgressStatus(ctx, logger, pcs, original); err != nil {
		logger.Error(err, "failed to mark PCS replica updates ended", "replicaIndices", sets.List(completedReplicaIndices))
		return err
	}
	return nil
}

// replicaIndices returns the PCS replica indices represented by replicaInfos in the same order.
func replicaIndices(replicaInfos []pcsReplicaInfo) []int {
	indices := make([]int, len(replicaInfos))
	for i, replicaInfo := range replicaInfos {
		indices[i] = replicaInfo.replicaIndex
	}
	return indices
}

// patchUpdateProgressStatus persists update progress to the PCS status using a merge patch.
func (r *_resource) patchUpdateProgressStatus(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, original *grovecorev1beta1.PodCliqueSet) error {
	if err := r.client.Status().Patch(ctx, pcs, client.MergeFrom(original)); err != nil {
		return groveerr.WrapError(
			err,
			errCodeUpdatePCSStatus,
			component.OperationSync,
			"could not patch update progress",
		)
	}
	logger.V(1).Info("Updated the PodCliqueSet status with update progress")
	return nil
}

// pcsReplicaUpdateSelectionBudget returns how many additional PCS replicas may be selected for update.
// Issue #889 updates one PCS replica at a time. Issue #888 will derive this budget from PCS-level
// MaxUnavailable using count-anchored semantics: unavailable old-generation replicas remain replaceable,
// while unavailable current-generation replacements consume the budget.
func pcsReplicaUpdateSelectionBudget(
	_ *grovecorev1beta1.PodCliqueSet,
	replicaInfosStillUpdating []pcsReplicaInfo,
) int {
	return max(0, 1-len(replicaInfosStillUpdating))
}

// selectReplicasToUpdate retains replicas still under update and fills the remaining slots from pending
// replicas in update-priority order.
func selectReplicasToUpdate(pcs *grovecorev1beta1.PodCliqueSet, replicaInfos, replicasStillUpdating []pcsReplicaInfo, minAvailableBreachedPCSReplicaIndices []int, remainingSlots int) []pcsReplicaInfo {
	selectedReplicaInfos := make([]pcsReplicaInfo, 0, len(replicasStillUpdating)+remainingSlots)
	selectedReplicaInfos = append(selectedReplicaInfos, replicasStillUpdating...)
	if remainingSlots == 0 {
		return selectedReplicaInfos
	}
	selectedReplicaIndices := sets.New(replicaIndices(replicasStillUpdating)...)
	var replicaInfosPendingUpdate []pcsReplicaInfo
	for _, replicaInfo := range replicaInfos {
		if selectedReplicaIndices.Has(replicaInfo.replicaIndex) || replicaInfo.isUpdateComplete(pcs) {
			continue
		}
		replicaInfosPendingUpdate = append(replicaInfosPendingUpdate, replicaInfo)
	}
	if componentutils.IsPCSReplicaRecreationInProgress(pcs) {
		slices.SortFunc(replicaInfosPendingUpdate, orderPCSReplicaInfoBySchedulingConfigRolloutPriority(pcs))
	} else {
		slices.SortFunc(replicaInfosPendingUpdate, orderPCSReplicaInfo(pcs, minAvailableBreachedPCSReplicaIndices))
	}
	numReplicasToSelect := min(remainingSlots, len(replicaInfosPendingUpdate))
	return append(selectedReplicaInfos, replicaInfosPendingUpdate[:numReplicasToSelect]...)
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
		return cmp.Compare(a.replicaIndex, b.replicaIndex)
	}
}

// orderPCSReplicaInfo returns a comparison function for prioritizing ordinary replica updates.
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
		minAvailableBreachedForA := slices.Contains(minAvailableBreachedPCSReplicaIndices, a.replicaIndex)
		minAvailableBreachedForB := slices.Contains(minAvailableBreachedPCSReplicaIndices, b.replicaIndex)
		if minAvailableBreachedForA && !minAvailableBreachedForB {
			return -1
		} else if !minAvailableBreachedForA && minAvailableBreachedForB {
			return 1
		}

		// 3. If all replicas are healthy, then pick the replicas in ascending ordinal value.
		return cmp.Compare(a.replicaIndex, b.replicaIndex)
	}
}

// updatePCSWithSelectedReplicas retains existing progress for replicas still updating, initializes
// progress for newly selected replicas, or marks the whole update complete when no work remains.
func (r *_resource) updatePCSWithSelectedReplicas(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, selectedReplicaInfos []pcsReplicaInfo) error {
	selectedReplicaInfos = slices.Clone(selectedReplicaInfos)
	slices.SortFunc(selectedReplicaInfos, func(a, b pcsReplicaInfo) int {
		return cmp.Compare(a.replicaIndex, b.replicaIndex)
	})
	original := pcs.DeepCopy()
	if len(selectedReplicaInfos) == 0 {
		logger.Info("Rolling update has completed")
		pcs.Status.UpdateProgress.UpdateEndedAt = new(metav1.Now())
		pcs.Status.UpdateProgress.CurrentlyUpdating = nil
		return r.patchUpdateProgressStatus(ctx, logger, pcs, original)
	}

	progressByReplicaIndex := make(map[int32]grovecorev1beta1.PodCliqueSetReplicaUpdateProgress, len(pcs.Status.UpdateProgress.CurrentlyUpdating))
	for _, progress := range pcs.Status.UpdateProgress.CurrentlyUpdating {
		if progress.UpdateEndedAt == nil {
			progressByReplicaIndex[progress.ReplicaIndex] = progress
		}
	}
	updateStartedAt := metav1.Now()
	selectedProgress := make([]grovecorev1beta1.PodCliqueSetReplicaUpdateProgress, 0, len(selectedReplicaInfos))
	for _, selectedReplicaInfo := range selectedReplicaInfos {
		replicaIndex := int32(selectedReplicaInfo.replicaIndex)
		if progress, ok := progressByReplicaIndex[replicaIndex]; ok {
			selectedProgress = append(selectedProgress, progress)
			continue
		}
		selectedProgress = append(selectedProgress, grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex:    replicaIndex,
			UpdateStartedAt: updateStartedAt,
		})
	}
	if apiequality.Semantic.DeepEqual(pcs.Status.UpdateProgress.CurrentlyUpdating, selectedProgress) {
		return nil
	}
	logger.Info("Updated PCS replicas selected for rolling update", "replicaIndices", replicaIndices(selectedReplicaInfos))
	pcs.Status.UpdateProgress.CurrentlyUpdating = selectedProgress
	return r.patchUpdateProgressStatus(ctx, logger, pcs, original)
}

type pcsReplicaInfo struct {
	replicaIndex int
	pclqs        []grovecorev1beta1.PodClique
	pcsgs        []grovecorev1beta1.PodCliqueScalingGroup
	// pgmEntries holds the replica's PodGangMap entries under a coherent update, used to gate completion
	// on the map having reconverged to a single generation. It is nil for a non-coherent update or when
	// the PodGangMap does not exist yet.
	pgmEntries []grovecorev1beta1.PodGangEntry
}

// isUpdateComplete reports whether every expected PodClique and PodCliqueScalingGroup of the replica has
// converged to the current generation hash. A missing PodClique keeps the replica incomplete because the
// count of converged PodCliques falls short of the expected count. Under a coherent update it also requires
// the replica's PodGangMap to have reconverged to a single generation, so a mid-flight intermediate anchor
// left by back-to-back updates is drained before the update is declared complete.
func (pri *pcsReplicaInfo) isUpdateComplete(pcs *grovecorev1beta1.PodCliqueSet) bool {
	completeStandalonePCLQs := 0
	for i := range pri.pclqs {
		if componentutils.IsPCLQUpdateComplete(pcs, &pri.pclqs[i]) {
			completeStandalonePCLQs++
		}
	}
	if completeStandalonePCLQs != len(componentutils.GetPodCliqueFQNsForPCSReplicaNotInPCSG(pcs, pri.replicaIndex)) {
		return false
	}
	currentGenerationHash := *pcs.Status.CurrentGenerationHash
	completePCSGs := 0
	for i := range pri.pcsgs {
		if componentutils.IsPCSGUpdateComplete(&pri.pcsgs[i], currentGenerationHash) {
			completePCSGs++
		}
	}
	if completePCSGs != len(pcs.Spec.Template.PodCliqueScalingGroupConfigs) {
		return false
	}
	if componentutils.IsCoherentUpdateInProgress(pcs) && !componentutils.IsPodGangMapAtSingleGeneration(pri.pgmEntries, currentGenerationHash) {
		return false
	}
	return true
}

// hasConvergedToPCSGeneration reports whether every expected standalone PodClique and
// PodCliqueScalingGroup exists and reports the given PCS generation as current.
func (pri *pcsReplicaInfo) hasConvergedToPCSGeneration(pcs *grovecorev1beta1.PodCliqueSet, generationHash string) bool {
	if len(pri.pclqs) != len(componentutils.GetPodCliqueFQNsForPCSReplicaNotInPCSG(pcs, pri.replicaIndex)) {
		return false
	}
	for i := range pri.pclqs {
		currentGenerationHash := pri.pclqs[i].Status.CurrentPodCliqueSetGenerationHash
		if currentGenerationHash == nil || *currentGenerationHash != generationHash {
			return false
		}
	}
	if len(pri.pcsgs) != len(pcs.Spec.Template.PodCliqueScalingGroupConfigs) {
		return false
	}
	for i := range pri.pcsgs {
		currentGenerationHash := pri.pcsgs[i].Status.CurrentPodCliqueSetGenerationHash
		if currentGenerationHash == nil || *currentGenerationHash != generationHash {
			return false
		}
	}
	return true
}

// isAvailable reports whether every expected child exists and satisfies its availability requirement.
func (pri *pcsReplicaInfo) isAvailable(pcs *grovecorev1beta1.PodCliqueSet) bool {
	return componentutils.IsPCSReplicaAvailable(
		len(componentutils.GetPodCliqueFQNsForPCSReplicaNotInPCSG(pcs, pri.replicaIndex)),
		len(pcs.Spec.Template.PodCliqueScalingGroupConfigs),
		pri.pclqs,
		pri.pcsgs,
	)
}

// getNumScheduledPods calculates total scheduled pods across PCLQs and PCSGs for a replica.
func (pri *pcsReplicaInfo) getNumScheduledPods(pcs *grovecorev1beta1.PodCliqueSet) int {
	noScheduled := 0
	for _, pclq := range pri.pclqs {
		noScheduled += int(pclq.Status.ScheduledReplicas)
	}

	for _, pcsg := range pri.pcsgs {
		for _, cliqueName := range pcsg.Spec.CliqueNames {
			pclqTemplateSpec := componentutils.FindPodCliqueTemplateSpecByName(pcs, cliqueName)
			noScheduled += int(pcsg.Status.ScheduledReplicas * *pclqTemplateSpec.Spec.MinAvailable)
		}
	}
	return noScheduled
}

// /*
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
// */

package podcliquesetreplica

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// orchestrateRollingUpdate manages the rolling update process for PodCliqueSet replicas.
func (r _resource) orchestrateRollingUpdate(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pcsIndicesToTerminate, minAvailableBreachedPCSReplicaIndices []int) error {
	updateWork, err := r.computePendingUpdateWork(ctx, pcs, pcsIndicesToTerminate)
	if err != nil {
		return err
	}

	if pcs.Status.RollingUpdateProgress.CurrentlyUpdating != nil && updateWork.currentlyUpdatingReplicaInfo != nil {
		if err = r.updatePCSWithReplicaUpdateProgress(ctx, logger, pcs, updateWork.currentlyUpdatingReplicaInfo.updateProgress); err != nil {
			return err
		}
		if !updateWork.currentlyUpdatingReplicaInfo.updateProgress.done {
			return groveerr.New(
				groveerr.ErrCodeContinueReconcileAndRequeue,
				component.OperationSync,
				fmt.Sprintf("rolling update of PodCliqueSet replica index %d is not completed", updateWork.currentlyUpdatingReplicaInfo.replicaIndex),
			)
		}
	}

	// pick the next replica index to update.
	nextReplicaToUpdate := updateWork.getNextReplicaToUpdate(pcs, minAvailableBreachedPCSReplicaIndices)
	if err = r.updatePCSWithNextSelectedReplica(ctx, logger, pcs, nextReplicaToUpdate); err != nil {
		return err
	}

	if nextReplicaToUpdate != nil {
		return groveerr.New(
			groveerr.ErrCodeContinueReconcileAndRequeue,
			component.OperationSync,
			fmt.Sprintf("commencing rolling update of PodCliqueSet replica index %d", nextReplicaToUpdate),
		)
	}
	return nil
}

// computePendingUpdateWork identifies replicas that need updating and tracks current update progress.
func (r _resource) computePendingUpdateWork(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsIndicesToTerminate []int) (*pendingUpdateWork, error) {
	replicaInfos, err := r.getPCSReplicaInfos(ctx, pcs, pcsIndicesToTerminate)
	if err != nil {
		return nil, err
	}
	// iterate through each replica
	pendingWork := &pendingUpdateWork{}
	for _, replicaInfo := range replicaInfos {
		replicaInfo.computeUpdateProgress(pcs)

		if pcs.Status.RollingUpdateProgress.CurrentlyUpdating != nil &&
			pcs.Status.RollingUpdateProgress.CurrentlyUpdating.ReplicaIndex == int32(replicaInfo.replicaIndex) {
			pendingWork.currentlyUpdatingReplicaInfo = &replicaInfo
			continue
		}

		if !replicaInfo.updateProgress.done {
			pendingWork.pendingUpdateReplicaInfos = append(pendingWork.pendingUpdateReplicaInfos, replicaInfo)
		}
	}
	return pendingWork, nil
}

// getPCSReplicaInfos fetches the PCLQs and PCSGs for each PCS replica.
func (r _resource) getPCSReplicaInfos(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsIndicesToTerminate []int) ([]pcsReplicaInfo, error) {
	pcsObjectKey := client.ObjectKeyFromObject(pcs)
	pclqsByPCSIndex, err := componentutils.GetPCLQsByOwnerReplicaIndex(ctx, r.client, constants.KindPodCliqueSet, client.ObjectKeyFromObject(pcs), apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name))
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPCLQs,
			component.OperationSync,
			fmt.Sprintf("could not list PCLQs for PCS: %v", pcsObjectKey),
		)
	}
	pcsgsByPCSIndex, err := componentutils.GetPCSGsByPCSReplicaIndex(ctx, r.client, client.ObjectKeyFromObject(pcs))
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPCSGs,
			component.OperationSync,
			fmt.Sprintf("could not list PCSGs for PCS: %v", pcsObjectKey),
		)
	}
	replicaInfos := make([]pcsReplicaInfo, 0, pcs.Spec.Replicas)
	for pcsReplicaIndex := range int(pcs.Spec.Replicas) {
		if slices.Contains(pcsIndicesToTerminate, pcsReplicaIndex) {
			continue
		}
		pcsReplicaIndexStr := strconv.Itoa(pcsReplicaIndex)
		replicaInfos = append(replicaInfos, pcsReplicaInfo{
			replicaIndex: pcsReplicaIndex,
			pclqs:        pclqsByPCSIndex[pcsReplicaIndexStr],
			pcsgs:        pcsgsByPCSIndex[pcsReplicaIndexStr],
		})
	}
	return replicaInfos, nil
}

// updatePCSWithReplicaUpdateProgress records the progress of the currently updating replica.
func (r _resource) updatePCSWithReplicaUpdateProgress(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, currentReplicaUpdateProgress replicaUpdateProgress) error {
	// Set the updatedCliques
	updatedCliqueFQNs := lo.Uniq(append(pcs.Status.RollingUpdateProgress.UpdatedPodCliques, currentReplicaUpdateProgress.updatedPCLQFQNs...))
	// There is a possibility that the replica that is currently getting updated has been deleted due to scale-in.
	// We need to clean up the already recorded pcsg.Status.RollingUpdateProgress.UpdatedPodCliques.
	expectedPCLQFQNs := componentutils.GetPodCliqueFQNsForPCSNotInPCSG(pcs)
	updatedCliqueFQNs = slices.DeleteFunc(updatedCliqueFQNs, func(pclqFQN string) bool {
		return !slices.Contains(expectedPCLQFQNs, pclqFQN)
	})
	slices.Sort(updatedCliqueFQNs)
	pcs.Status.RollingUpdateProgress.UpdatedPodCliques = updatedCliqueFQNs

	// Set the updatedPodCliqueScalingGroups
	updatedPCSGFQNs := lo.Uniq(append(pcs.Status.RollingUpdateProgress.UpdatedPodCliqueScalingGroups, currentReplicaUpdateProgress.updatedPCSGFQNs...))
	// There is a possibility that the replica that is currently getting updated has been deleted due to scale-in.
	// We need to clean up the already recorded pcsg.Status.RollingUpdateProgress.UpdatedPodCliques.
	expectedPCSGFQNs := componentutils.GetExpectedPCSGFQNsForPCS(pcs)
	updatedPCSGFQNs = slices.DeleteFunc(updatedPCSGFQNs, func(pcsgFQN string) bool {
		return !slices.Contains(expectedPCSGFQNs, pcsgFQN)
	})
	slices.Sort(updatedPCSGFQNs)
	pcs.Status.RollingUpdateProgress.UpdatedPodCliqueScalingGroups = updatedPCSGFQNs

	logger.Info("Updating PodCliqueSet status with newly updated PodCliques and PodClique")
	if err := r.updateRollingUpdateProgressStatus(ctx, logger, pcs); err != nil {
		logger.Error(err, "failed to update rolling update progress", "replicaIndex", pcs.Status.RollingUpdateProgress.CurrentlyUpdating.ReplicaIndex)
		return err
	}
	return nil
}

// updatePCSWithNextSelectedReplica initiates an update for the next replica or marks completion.
func (r _resource) updatePCSWithNextSelectedReplica(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, nextPCSReplicaToUpdate *int) error {
	if nextPCSReplicaToUpdate == nil {
		logger.Info("Rolling update has completed")
		pcs.Status.RollingUpdateProgress.UpdateEndedAt = ptr.To(metav1.Now())
		pcs.Status.RollingUpdateProgress.CurrentlyUpdating = nil
	} else {
		logger.Info("Initiating rolling update for next replica index", "nextReplicaIndex", *nextPCSReplicaToUpdate)
		pcs.Status.RollingUpdateProgress.CurrentlyUpdating = &grovecorev1alpha1.PodCliqueSetReplicaRollingUpdateProgress{
			ReplicaIndex:    int32(*nextPCSReplicaToUpdate),
			UpdateStartedAt: metav1.Now(),
		}
	}
	return r.updateRollingUpdateProgressStatus(ctx, logger, pcs)
}

// updateRollingUpdateProgressStatus persists rolling update progress to the PCS status.
func (r _resource) updateRollingUpdateProgressStatus(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	if err := r.client.Status().Update(ctx, pcs); err != nil {
		return groveerr.WrapError(
			err,
			errCodeUpdatePCSStatus,
			component.OperationSync,
			"could not update rolling update progress",
		)
	}
	logger.Info("Updated the PodCliqueSet status with rolling update progress")
	return nil
}

// orderPCSReplicaInfo returns a comparison function for prioritizing replica updates.
func orderPCSReplicaInfo(pcs *grovecorev1alpha1.PodCliqueSet, minAvailableBreachedPCSReplicaIndices []int) func(a, b pcsReplicaInfo) int {
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
		if a.replicaIndex < b.replicaIndex {
			return -1
		} else {
			return 1
		}
	}
}

type pendingUpdateWork struct {
	pendingUpdateReplicaInfos    []pcsReplicaInfo
	currentlyUpdatingReplicaInfo *pcsReplicaInfo
}

type pcsReplicaInfo struct {
	replicaIndex   int
	pclqs          []grovecorev1alpha1.PodClique
	pcsgs          []grovecorev1alpha1.PodCliqueScalingGroup
	updateProgress replicaUpdateProgress
}

type replicaUpdateProgress struct {
	done            bool
	updatedPCLQFQNs []string
	updatedPCSGFQNs []string
}

// getNextReplicaToUpdate selects the next replica to update based on priority.
func (w *pendingUpdateWork) getNextReplicaToUpdate(pcs *grovecorev1alpha1.PodCliqueSet, minAvailableBreachedPCSReplicaIndices []int) *int {
	slices.SortFunc(w.pendingUpdateReplicaInfos, orderPCSReplicaInfo(pcs, minAvailableBreachedPCSReplicaIndices))
	if len(w.pendingUpdateReplicaInfos) > 0 {
		return &w.pendingUpdateReplicaInfos[0].replicaIndex
	}
	return nil
}

// computeUpdateProgress calculates update completion for a PCS replica.
func (pri *pcsReplicaInfo) computeUpdateProgress(pcs *grovecorev1alpha1.PodCliqueSet) {
	progress := replicaUpdateProgress{}
	for _, pclq := range pri.pclqs {
		if isPCLQUpdateComplete(&pclq, *pcs.Status.CurrentGenerationHash) {
			progress.updatedPCLQFQNs = append(progress.updatedPCLQFQNs, pclq.Name)
		}
	}
	for _, pcsg := range pri.pcsgs {
		if componentutils.IsPCSGUpdateComplete(&pcsg, *pcs.Status.CurrentGenerationHash) {
			progress.updatedPCSGFQNs = append(progress.updatedPCSGFQNs, pcsg.Name)
		}
	}
	progress.done = len(progress.updatedPCLQFQNs) == len(componentutils.GetPodCliqueFQNsForPCSReplicaNotInPCSG(pcs, pri.replicaIndex)) &&
		len(progress.updatedPCSGFQNs) == len(pcs.Spec.Template.PodCliqueScalingGroupConfigs)
	pri.updateProgress = progress
}

// getNumScheduledPods calculates total scheduled pods across PCLQs and PCSGs for a replica.
func (pri *pcsReplicaInfo) getNumScheduledPods(pcs *grovecorev1alpha1.PodCliqueSet) int {
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

// isPCLQUpdateComplete checks if a PodClique has completed its update to the target generation.
func isPCLQUpdateComplete(pclq *grovecorev1alpha1.PodClique, currentPCSGenerationHash string) bool {
	if pclq.Status.CurrentPodCliqueSetGenerationHash != nil &&
		*pclq.Status.CurrentPodCliqueSetGenerationHash == currentPCSGenerationHash &&
		pclq.Status.UpdatedReplicas >= *pclq.Spec.MinAvailable &&
		pclq.Status.ReadyReplicas >= *pclq.Spec.MinAvailable {
		return true
	}
	return false
}

// isRollingUpdateInProgress checks if a rolling update is currently in progress.
func isRollingUpdateInProgress(pcs *grovecorev1alpha1.PodCliqueSet) bool {
	return pcs.Status.RollingUpdateProgress != nil && pcs.Status.RollingUpdateProgress.UpdateEndedAt == nil
}

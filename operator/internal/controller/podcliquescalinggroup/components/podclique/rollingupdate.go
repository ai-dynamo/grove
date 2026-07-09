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

package podclique

import (
	"context"
	"fmt"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// updateWork encapsulates the information needed to perform a rolling update of a PodCliqueScalingGroup.
type updateWork struct {
	oldPendingReplicaIndices     []int
	oldUnavailableReplicaIndices []int
	oldReadyReplicaIndices       []int
}

type replicaState int

const (
	replicaStatePending replicaState = iota
	replicaStateUnAvailable
	replicaStateReady
)

// processPendingUpdates processes pending updates for the PodCliqueScalingGroup.
// This is the main entry point for handling rolling updates of PodCliques in the PodCliqueScalingGroup.
func (r _resource) processPendingUpdates(logger logr.Logger, sc *syncContext) error {
	work, err := computePendingUpdateWork(sc)
	if err != nil {
		return groveerr.WrapError(err,
			errCodeComputePendingPodCliqueScalingGroupUpdateWork,
			component.OperationSync,
			fmt.Sprintf("failed to compute pending update work for PodCliqueScalingGroup %v", client.ObjectKeyFromObject(sc.pcsg)))
	}
	// always delete PCSG replicas that are either pending or unavailable
	if err = r.deleteOldPendingAndUnavailableReplicas(logger, sc, work); err != nil {
		return err
	}

	// Check if there is currently a replica that is selected for update and its update has not yet completed.
	if isAnyReadyReplicaSelectedForUpdate(sc.pcsg) {
		complete, err := isCurrentReplicaUpdateComplete(sc)
		if err != nil {
			return groveerr.WrapError(err,
				errCodeComputePendingPodCliqueScalingGroupUpdateWork,
				component.OperationSync,
				fmt.Sprintf("failed to check if current replica update is complete for PodCliqueScalingGroup %v", client.ObjectKeyFromObject(sc.pcsg)))
		}
		if !complete {
			return groveerr.New(
				groveerr.ErrCodeContinueReconcileAndRequeue,
				component.OperationSync,
				fmt.Sprintf("rolling update of currently selected PCSG replica index: %d is not complete, requeuing", sc.pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate.Current),
			)
		}
	}

	// Either the update has not started, or a previously selected replica has been successfully updated.
	// Either of the cases requires selecting the next replica index to update.
	var nextReplicaIndexToUpdate *int
	if len(work.oldReadyReplicaIndices) > 0 {
		if sc.pcsg.Status.AvailableReplicas < *sc.pcsg.Spec.MinAvailable {
			return groveerr.New(
				groveerr.ErrCodeContinueReconcileAndRequeue,
				component.OperationSync,
				fmt.Sprintf("available replicas %d lesser than minAvailable %d, requeuing", sc.pcsg.Status.AvailableReplicas, *sc.pcsg.Spec.MinAvailable),
			)
		}
		nextReplicaIndexToUpdate = ptr.To(work.oldReadyReplicaIndices[0])
	}

	// Trigger the update if there is an index still pending an update.
	if nextReplicaIndexToUpdate != nil {
		logger.Info("Selected the next replica to update", "nextReplicaIndexToUpdate", *nextReplicaIndexToUpdate)
		if err := r.updatePCSGStatusWithNextReplicaToUpdate(sc.ctx, logger, sc.pcsg, *nextReplicaIndexToUpdate); err != nil {
			return err
		}

		// Trigger deletion of the next replica index.
		deleteTask := r.createDeleteTasks(logger, sc.pcs, sc.pcsg.Name, []int{*nextReplicaIndexToUpdate}, "deleting replica for rolling update")
		if err := r.triggerDeletionOfPodCliques(sc.ctx, logger, client.ObjectKeyFromObject(sc.pcsg), deleteTask); err != nil {
			return err
		}

		// Requeue to re-create the deleted PodCliques of the replica.
		return groveerr.New(
			groveerr.ErrCodeContinueReconcileAndRequeue,
			component.OperationSync,
			fmt.Sprintf("rolling update of currently selected PCSG replica index: %d is not complete, requeuing", sc.pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate.Current),
		)
	}

	return r.markRollingUpdateEnd(sc.ctx, logger, sc.pcsg)
}

// updatePCSGStatusWithNextReplicaToUpdate marks the next replica index as selected for rolling update in the PCSG status
func (r _resource) updatePCSGStatusWithNextReplicaToUpdate(ctx context.Context, logger logr.Logger, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, nextReplicaIndexToUpdate int) error {
	patch := client.MergeFrom(pcsg.DeepCopy())

	if pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate == nil {
		pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate = &grovecorev1alpha1.PodCliqueScalingGroupReplicaUpdateProgress{}
	} else {
		pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate.Completed = append(pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate.Completed, pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate.Current)
	}
	pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate.Current = int32(nextReplicaIndexToUpdate)

	if err := r.client.Status().Patch(ctx, pcsg, patch); err != nil {
		return groveerr.WrapError(
			err,
			errCodeUpdateStatus,
			component.OperationSync,
			fmt.Sprintf("failed to update ready replica selected to update in status of PodCliqueScalingGroup: %v", client.ObjectKeyFromObject(pcsg)),
		)
	}
	logger.Info("Updated PodCliqueScalingGroup status with new ready replica index selected to update", "nextReplicaIndexToUpdate", nextReplicaIndexToUpdate)
	return nil
}

// markRollingUpdateEnd finalizes the rolling update by setting the end timestamp and clearing update progress
func (r _resource) markRollingUpdateEnd(ctx context.Context, logger logr.Logger, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) error {
	patch := client.MergeFrom(pcsg.DeepCopy())

	pcsg.Status.UpdateProgress.UpdateEndedAt = ptr.To(metav1.Now())
	pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate = nil

	if err := r.client.Status().Patch(ctx, pcsg, patch); err != nil {
		return groveerr.WrapError(
			err,
			errCodeUpdateStatus,
			component.OperationSync,
			fmt.Sprintf("failed to mark end of rolling update in status of PodCliqueScalingGroup: %v", client.ObjectKeyFromObject(pcsg)),
		)
	}
	logger.Info("Marked the end of rolling update of PodCliqueScalingGroup")
	return groveerr.New(
		groveerr.ErrCodeContinueReconcileAndRequeue,
		component.OperationSync,
		fmt.Sprintf("rolling update of PodCliqueScalingGroup %v has ended, requeuing for status convergence", client.ObjectKeyFromObject(pcsg)),
	)
}

// checkAndMarkPCSGCoherentUpdateEnded closes out the PCSG-level UpdateProgress when a coherent
// update has finished rolling all owned PCLQs to the new template.
//
// In a coherent update PCSG-owned PCLQs roll via reconcilePCSGReplicaDistribution's
// label-mismatch driven delete-and-recreate flow — there is no per-PCSG orchestrator akin to
// processPendingUpdates that would call markRollingUpdateEnd. Without an explicit completion
// step, mutateCurrentHashes refuses to advance pcsg.Status.CurrentPodCliqueSetGenerationHash
// because IsPCSGUpdateInProgress stays true, which strands the PCS-level orchestrator's
// "replica done" check.
//
// UpdatedPodCliquesCount is computed from constituent PCLQs' updated state by the PCSG status
// reconciler, so the equality with TotalPodCliquesCount is a cheap, race-free completion signal.
// The caller already gates this call on IsCoherentUpdateInProgress(pcs) and
// IsPCSGUpdateInProgress(pcsg).
func (r _resource) checkAndMarkPCSGCoherentUpdateEnded(logger logr.Logger, sc *syncContext) error {
	if sc.pcsg.Status.UpdateProgress.TotalPodCliquesCount == 0 {
		return nil
	}
	if sc.pcsg.Status.UpdateProgress.UpdatedPodCliquesCount != sc.pcsg.Status.UpdateProgress.TotalPodCliquesCount {
		return nil
	}
	return r.markRollingUpdateEnd(sc.ctx, logger, sc.pcsg)
}

// computePendingUpdateWork analyzes existing replicas and categorizes them by update status and availability state
func computePendingUpdateWork(sc *syncContext) (*updateWork, error) {
	work := &updateWork{}
	existingPCLQsByReplicaIndex, err := componentutils.GroupPCLQsByPCSGReplicaIndex(sc.existingPCLQs)
	if err != nil {
		return nil, err
	}
	for pcsgReplicaIndex := range int(sc.pcsg.Spec.Replicas) {
		existingPCSGReplicaPCLQs := existingPCLQsByReplicaIndex[pcsgReplicaIndex]
		if isReplicaDeletedOrMarkedForDeletion(sc.pcsg, existingPCSGReplicaPCLQs, pcsgReplicaIndex) {
			continue
		}
		// pcsgReplicaIndex is the currently updating replica
		if sc.pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate != nil &&
			sc.pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate.Current == int32(pcsgReplicaIndex) {
			continue
		}
		isUpdated, err := isReplicaUpdated(sc.expectedPCLQPodTemplateHashMap, existingPCSGReplicaPCLQs)
		if err != nil {
			return nil, err
		}
		if isUpdated {
			continue
		}
		state := getReplicaState(existingPCSGReplicaPCLQs)
		switch state {
		case replicaStatePending:
			work.oldPendingReplicaIndices = append(work.oldPendingReplicaIndices, pcsgReplicaIndex)
		case replicaStateUnAvailable:
			work.oldUnavailableReplicaIndices = append(work.oldUnavailableReplicaIndices, pcsgReplicaIndex)
		case replicaStateReady:
			work.oldReadyReplicaIndices = append(work.oldReadyReplicaIndices, pcsgReplicaIndex)
		}
	}
	return work, nil
}

// deleteOldPendingAndUnavailableReplicas removes PCSG replicas that are pending or unavailable with old configurations
func (r _resource) deleteOldPendingAndUnavailableReplicas(logger logr.Logger, sc *syncContext, work *updateWork) error {
	replicaIndicesToDelete := append(work.oldPendingReplicaIndices, work.oldUnavailableReplicaIndices...)
	deleteTasks := r.createDeleteTasks(logger, sc.pcs, sc.pcsg.Name, replicaIndicesToDelete,
		"delete pending and unavailable PodCliqueScalingGroup replicas for rolling update")
	return r.triggerDeletionOfPodCliques(sc.ctx, logger, client.ObjectKeyFromObject(sc.pcsg), deleteTasks)
}

// isAnyReadyReplicaSelectedForUpdate checks if there is currently a ready replica selected for rolling update
func isAnyReadyReplicaSelectedForUpdate(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) bool {
	return pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate != nil
}

// isCurrentReplicaUpdateComplete verifies if the currently updating replica has completed its rolling update
func isCurrentReplicaUpdateComplete(sc *syncContext) (bool, error) {
	currentlyUpdatingReplicaIndex := int(sc.pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate.Current)
	existingPCLQsByReplicaIndex, err := componentutils.GroupPCLQsByPCSGReplicaIndex(sc.existingPCLQs)
	if err != nil {
		return false, err
	}
	// Get the expected PCLQ PodTemplateHash and compare it against all existing PCLQs for the currently updating replica index.
	expectedPCLQFQNs := sc.expectedPCLQFQNsPerPCSGReplica[currentlyUpdatingReplicaIndex]
	existingPCSGReplicaPCLQs := existingPCLQsByReplicaIndex[currentlyUpdatingReplicaIndex]
	if len(expectedPCLQFQNs) != len(existingPCSGReplicaPCLQs) {
		return false, nil
	}
	return lo.EveryBy(existingPCSGReplicaPCLQs, func(pclq grovecorev1alpha1.PodClique) bool {
		expectedPodTemplateHash := sc.expectedPCLQPodTemplateHashMap[pclq.Name]
		return expectedPodTemplateHash != "" &&
			pclq.Labels[apicommon.LabelPodTemplateHash] == expectedPodTemplateHash &&
			pclq.Status.CurrentPodTemplateHash != nil && *pclq.Status.CurrentPodTemplateHash == expectedPodTemplateHash &&
			sc.pcs.Status.CurrentGenerationHash != nil &&
			pclq.Status.CurrentPodCliqueSetGenerationHash != nil && *pclq.Status.CurrentPodCliqueSetGenerationHash == *sc.pcs.Status.CurrentGenerationHash &&
			pclq.Status.UpdatedReplicas >= *pclq.Spec.MinAvailable &&
			pclq.Status.ReadyReplicas >= *pclq.Spec.MinAvailable
	}), nil
}

// isReplicaUpdated checks if all PodCliques in a PCSG replica have the expected pod template hash
func isReplicaUpdated(expectedPCLQPodTemplateHashes map[string]string, pcsgReplicaPCLQs []grovecorev1alpha1.PodClique) (bool, error) {
	for _, pclq := range pcsgReplicaPCLQs {
		podTemplateHash, ok := pclq.Labels[apicommon.LabelPodTemplateHash]
		if !ok {
			return false, groveerr.ErrMissingPodTemplateHashLabel
		}
		if podTemplateHash != expectedPCLQPodTemplateHashes[pclq.Name] {
			return false, nil
		}
	}
	return true, nil
}

// isReplicaDeletedOrMarkedForDeletion determines if a PCSG replica is deleted or all its PodCliques are terminating
func isReplicaDeletedOrMarkedForDeletion(pcsg *grovecorev1alpha1.PodCliqueScalingGroup, pcsgReplicaPCLQs []grovecorev1alpha1.PodClique, _ int) bool {
	if pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate == nil {
		return false
	}
	if len(pcsgReplicaPCLQs) == 0 {
		return true
	}
	return lo.EveryBy(pcsgReplicaPCLQs, func(pclq grovecorev1alpha1.PodClique) bool {
		return k8sutils.IsResourceTerminating(pclq.ObjectMeta)
	})
}

// getReplicaState determines the overall state of a PCSG replica based on its constituent PodCliques
func getReplicaState(pcsgReplicaPCLQs []grovecorev1alpha1.PodClique) replicaState {
	for _, pclq := range pcsgReplicaPCLQs {
		if pclq.Status.ScheduledReplicas < *pclq.Spec.MinAvailable {
			return replicaStatePending
		}
		if pclq.Status.ReadyReplicas < *pclq.Spec.MinAvailable {
			return replicaStateUnAvailable
		}
	}
	return replicaStateReady
}

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
	"context"
	"fmt"
	"slices"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// orchestrateRollingUpdate drives a rolling update (RollingRecreate or Coherent) for the PodCliqueSet
// replicas, one replica at a time.
func (r _resource) orchestrateRollingUpdate(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pcsIndicesToTerminate, minAvailableBreachedPCSReplicaIndices []int) error {
	replicaInfos, err := r.getPCSReplicaInfos(ctx, pcs, pcsIndicesToTerminate)
	if err != nil {
		return err
	}

	if currentlyUpdating := findCurrentlyUpdatingReplicaInfo(pcs, replicaInfos); currentlyUpdating != nil {
		if !currentlyUpdating.isUpdateComplete(pcs) {
			// A Coherent update records its in-flight epochs and the components it is still waiting on. The
			// PodGangMap component owns the advance, so this only writes observability status.
			if componentutils.IsCoherentStrategy(pcs) {
				if err = r.updateCoherentReplicaProgress(ctx, logger, pcs, *currentlyUpdating); err != nil {
					return err
				}
			}
			return groveerr.New(
				groveerr.ErrCodeContinueReconcileAndRequeue,
				component.OperationSync,
				fmt.Sprintf("rolling update of PodCliqueSet replica index %d is not completed", currentlyUpdating.replicaIndex),
			)
		}
		if err = r.markCurrentReplicaUpdateEnded(ctx, logger, pcs); err != nil {
			return err
		}
	}

	nextReplicaToUpdate := selectNextReplicaToUpdate(pcs, replicaInfos, minAvailableBreachedPCSReplicaIndices)
	if err = r.updatePCSWithNextSelectedReplica(ctx, logger, pcs, nextReplicaToUpdate); err != nil {
		return err
	}

	if nextReplicaToUpdate != nil {
		return groveerr.New(
			groveerr.ErrCodeContinueReconcileAndRequeue,
			component.OperationSync,
			fmt.Sprintf("commencing rolling update of PodCliqueSet replica index %d", *nextReplicaToUpdate),
		)
	}
	return nil
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

// findCurrentlyUpdatingReplicaInfo returns the gathered info for the replica the status marks as
// currently updating, or nil when none is marked.
func findCurrentlyUpdatingReplicaInfo(pcs *grovecorev1alpha1.PodCliqueSet, replicaInfos []pcsReplicaInfo) *pcsReplicaInfo {
	if len(pcs.Status.UpdateProgress.CurrentlyUpdating) == 0 {
		return nil
	}
	currentReplicaIndex := pcs.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex
	for i := range replicaInfos {
		if int32(replicaInfos[i].replicaIndex) == currentReplicaIndex {
			return &replicaInfos[i]
		}
	}
	return nil
}

// markCurrentReplicaUpdateEnded stamps UpdateEndedAt on the currently-updating replica once it has fully
// converged. Aggregate update progress counts are derived in reconcileStatus each reconcile, so they are
// not maintained here.
func (r _resource) markCurrentReplicaUpdateEnded(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	original := pcs.DeepCopy()
	pcs.Status.UpdateProgress.CurrentlyUpdating[0].UpdateEndedAt = ptr.To(metav1.Now())
	if err := r.patchUpdateProgressStatus(ctx, logger, pcs, original); err != nil {
		logger.Error(err, "failed to patch update progress", "replicaIndex", pcs.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex)
		return err
	}
	return nil
}

// patchUpdateProgressStatus persists update progress to the PCS status using a merge patch.
func (r _resource) patchUpdateProgressStatus(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, original *grovecorev1alpha1.PodCliqueSet) error {
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

// selectNextReplicaToUpdate returns the index of the highest-priority replica not yet converged to the
// current generation hash, or nil when every replica is updated.
func selectNextReplicaToUpdate(pcs *grovecorev1alpha1.PodCliqueSet, replicaInfos []pcsReplicaInfo, minAvailableBreachedPCSReplicaIndices []int) *int {
	pendingReplicaInfos := make([]pcsReplicaInfo, 0, len(replicaInfos))
	for i := range replicaInfos {
		if !replicaInfos[i].isUpdateComplete(pcs) {
			pendingReplicaInfos = append(pendingReplicaInfos, replicaInfos[i])
		}
	}
	slices.SortFunc(pendingReplicaInfos, orderPCSReplicaInfo(pcs, minAvailableBreachedPCSReplicaIndices))
	if len(pendingReplicaInfos) > 0 {
		return &pendingReplicaInfos[0].replicaIndex
	}
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

// updatePCSWithNextSelectedReplica initiates an update for the next replica or marks completion.
func (r _resource) updatePCSWithNextSelectedReplica(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, nextPCSReplicaToUpdate *int) error {
	original := pcs.DeepCopy()

	if nextPCSReplicaToUpdate == nil {
		logger.Info("Rolling update has completed")
		pcs.Status.UpdateProgress.UpdateEndedAt = ptr.To(metav1.Now())
		pcs.Status.UpdateProgress.CurrentlyUpdating = nil
	} else {
		logger.Info("Initiating rolling update for next replica index", "nextReplicaIndex", *nextPCSReplicaToUpdate)
		pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{
			{
				ReplicaIndex:    int32(*nextPCSReplicaToUpdate),
				UpdateStartedAt: metav1.Now(),
			},
		}
	}
	return r.patchUpdateProgressStatus(ctx, logger, pcs, original)
}

type pcsReplicaInfo struct {
	replicaIndex int
	pclqs        []grovecorev1alpha1.PodClique
	pcsgs        []grovecorev1alpha1.PodCliqueScalingGroup
}

// isUpdateComplete reports whether every expected PodClique and PodCliqueScalingGroup of the replica has
// converged to the current generation hash. A missing PodClique keeps the replica incomplete because the
// count of converged PodCliques falls short of the expected count.
func (pri *pcsReplicaInfo) isUpdateComplete(pcs *grovecorev1alpha1.PodCliqueSet) bool {
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
	return completePCSGs == len(pcs.Spec.Template.PodCliqueScalingGroupConfigs)
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

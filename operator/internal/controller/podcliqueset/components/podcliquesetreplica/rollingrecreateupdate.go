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

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// orchestrateRollingRecreateUpdate manages the rolling recreate update process for PodCliqueSet replicas.
func (r _resource) orchestrateRollingRecreateUpdate(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pcsIndicesToTerminate, minAvailableBreachedPCSReplicaIndices []int) error {
	updateWork, err := r.computeRollingRecreatePendingWork(ctx, pcs, pcsIndicesToTerminate)
	if err != nil {
		return err
	}

	if len(pcs.Status.UpdateProgress.CurrentlyUpdating) > 0 && updateWork.currentlyUpdatingReplicaInfo != nil {
		if updateWork.currentlyUpdatingReplicaInfo.updateDone {
			if err = r.markCurrentReplicaUpdateDone(ctx, logger, pcs); err != nil {
				return err
			}
		} else {
			return groveerr.New(
				groveerr.ErrCodeContinueReconcileAndRequeue,
				component.OperationSync,
				fmt.Sprintf("rolling update of PodCliqueSet replica index %d is not completed", updateWork.currentlyUpdatingReplicaInfo.replicaIndex),
			)
		}
	}

	nextReplicaToUpdate := updateWork.getNextReplicaToUpdate(pcs, minAvailableBreachedPCSReplicaIndices)
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

// computeRollingRecreatePendingWork identifies replicas that need updating and tracks current update progress.
func (r _resource) computeRollingRecreatePendingWork(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsIndicesToTerminate []int) (*rollingRecreatePendingWork, error) {
	replicaInfos, err := r.getPCSReplicaInfos(ctx, pcs, pcsIndicesToTerminate)
	if err != nil {
		return nil, err
	}
	pendingWork := &rollingRecreatePendingWork{}
	for _, replicaInfo := range replicaInfos {
		replicaInfo.computeUpdateProgress(pcs)

		if len(pcs.Status.UpdateProgress.CurrentlyUpdating) > 0 &&
			pcs.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex == int32(replicaInfo.replicaIndex) {
			pendingWork.currentlyUpdatingReplicaInfo = &replicaInfo
			continue
		}

		if !replicaInfo.updateDone {
			pendingWork.pendingUpdateReplicaInfos = append(pendingWork.pendingUpdateReplicaInfos, replicaInfo)
		}
	}
	return pendingWork, nil
}

// updatePCSWithNextSelectedReplica initiates an update for the next replica or marks completion.
func (r _resource) updatePCSWithNextSelectedReplica(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, nextPCSReplicaToUpdate *int) error {
	original := pcs.DeepCopy()

	if nextPCSReplicaToUpdate == nil {
		logger.Info("Rolling recreate update has completed")
		pcs.Status.UpdateProgress.UpdateEndedAt = ptr.To(metav1.Now())
		pcs.Status.UpdateProgress.CurrentlyUpdating = nil
	} else {
		logger.Info("Initiating rolling recreate update for next replica index", "nextReplicaIndex", *nextPCSReplicaToUpdate)
		pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{
			{
				ReplicaIndex:    int32(*nextPCSReplicaToUpdate),
				UpdateStartedAt: metav1.Now(),
			},
		}
	}
	return r.patchUpdateProgressStatus(ctx, logger, pcs, original)
}

// orderPCSReplicaInfo returns a comparison function for prioritizing replica updates.
func orderPCSReplicaInfo(pcs *grovecorev1alpha1.PodCliqueSet, minAvailableBreachedPCSReplicaIndices []int) func(a, b pcsReplicaInfo) int {
	return func(a, b pcsReplicaInfo) int {
		scheduledPodsInA, scheduledPodsInB := a.getNumScheduledPods(pcs), b.getNumScheduledPods(pcs)
		if scheduledPodsInA == 0 && scheduledPodsInB != 0 {
			return -1
		} else if scheduledPodsInA != 0 && scheduledPodsInB == 0 {
			return 1
		}

		minAvailableBreachedForA := slices.Contains(minAvailableBreachedPCSReplicaIndices, a.replicaIndex)
		minAvailableBreachedForB := slices.Contains(minAvailableBreachedPCSReplicaIndices, b.replicaIndex)
		if minAvailableBreachedForA && !minAvailableBreachedForB {
			return -1
		} else if !minAvailableBreachedForA && minAvailableBreachedForB {
			return 1
		}

		if a.replicaIndex < b.replicaIndex {
			return -1
		} else {
			return 1
		}
	}
}

type rollingRecreatePendingWork struct {
	pendingUpdateReplicaInfos    []pcsReplicaInfo
	currentlyUpdatingReplicaInfo *pcsReplicaInfo
}

// getNextReplicaToUpdate selects the next replica to update based on priority.
func (w *rollingRecreatePendingWork) getNextReplicaToUpdate(pcs *grovecorev1alpha1.PodCliqueSet, minAvailableBreachedPCSReplicaIndices []int) *int {
	slices.SortFunc(w.pendingUpdateReplicaInfos, orderPCSReplicaInfo(pcs, minAvailableBreachedPCSReplicaIndices))
	if len(w.pendingUpdateReplicaInfos) > 0 {
		return &w.pendingUpdateReplicaInfos[0].replicaIndex
	}
	return nil
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

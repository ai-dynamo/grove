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

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// orchestrateCoherentUpdate manages the coherent update process for PodCliqueSet replicas.
// The orchestrator's responsibilities are narrowed to state machine progression:
//   - Pick the next replica to update
//   - Wait for InFlightPodGangs to become Available (PodGangConditionTypeAvailable)
//   - Advance to the next iteration or mark replica/update as complete
//
// It does NOT compute PodGangMap entries (PodGangMap component does that),
// does NOT mutate PodGang resources (PodGang component does that),
// and does NOT delete pods (PCLQ reconciler handles that).
func (r _resource) orchestrateCoherentUpdate(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pcsIndicesToTerminate []int) error {
	updateWork, err := r.computeCoherentPendingWork(ctx, pcs, pcsIndicesToTerminate)
	if err != nil {
		return err
	}

	if len(pcs.Status.UpdateProgress.CurrentlyUpdating) > 0 && pcs.Status.UpdateProgress.CurrentlyUpdating[0].UpdateEndedAt == nil {
		replicaDone, err := r.checkAndAdvanceCoherentUpdate(ctx, logger, pcs, updateWork)
		if err != nil {
			return err
		}
		if !replicaDone {
			return nil
		}
		// Replica was just marked done in this reconcile; fall through so the next-replica or
		// outer-UpdateEndedAt logic below runs in the same reconcile. Without this, the For-watch's
		// GenerationChangedPredicate would ignore the status-only patch and the update would never
		// close out.
	}

	nextReplica := updateWork.getNextPendingReplicaByIndex()
	if nextReplica != nil {
		logger.Info("Initiating coherent update for next replica index", "nextReplicaIndex", *nextReplica)
		original := pcs.DeepCopy()
		pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{
			{
				ReplicaIndex:    int32(*nextReplica),
				UpdateStartedAt: metav1.Now(),
			},
		}
		return r.patchUpdateProgressStatus(ctx, logger, pcs, original)
	}

	logger.Info("Coherent update has completed")
	original := pcs.DeepCopy()
	pcs.Status.UpdateProgress.UpdateEndedAt = ptr.To(metav1.Now())
	pcs.Status.UpdateProgress.CurrentlyUpdating = nil
	return r.patchUpdateProgressStatus(ctx, logger, pcs, original)
}

// checkAndAdvanceCoherentUpdate checks if the current in-flight PodGangs are Available.
// If they are not yet Available, it re-queues. If they are Available and the replica is fully
// updated, it marks the replica as done and returns (true, nil) so the caller falls through to
// pick the next replica or close the update. Otherwise, it clears InFlightPodGangs and re-queues
// so that the PodGangMap component can compute the next iteration's entries.
//
// The boolean return distinguishes "replica done, caller should continue" from the other terminal
// outcomes (waiting/requeue, in-progress) that always pair (false, err). markCurrentReplicaUpdateDone
// only patches status; the PCS controller's For-watch uses GenerationChangedPredicate and skips
// status-only events, so without same-reconcile fall-through the outer UpdateEndedAt would never
// be minted.
func (r _resource) checkAndAdvanceCoherentUpdate(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, updateWork *coherentPendingWork) (replicaDone bool, err error) {
	// NOTE: While the API make a provision in the PCS status to potentially allow more than one PCS replica to be updated concurrently, none of the update strategies
	// currently supported allow more than one PCS replica to be updated. Therefore, we only check for index 0 of `CurrentlyUpdating`
	// If and when concurrent PCS replica update is supported then we should iterate over all currently updating replicas.
	currentProgress := &pcs.Status.UpdateProgress.CurrentlyUpdating[0]
	replicaIndex := currentProgress.ReplicaIndex

	// Early-exit when this replica is already fully updated. computeCoherentPendingWork derives
	// updateWork.doneReplicaIndices from PCLQ/PCSG state rather than InFlightPodGangs, so the
	// determination is independent of any in-flight bookkeeping. Skipping populateInFlightPodGangs
	// in this case avoids the post-completion "no new in-flight PodGangs found, requeueing" loop.
	if slices.Contains(updateWork.doneReplicaIndices, int(replicaIndex)) {
		logger.Info("Coherent update for replica completed", "replicaIndex", replicaIndex)
		if err = r.markCurrentReplicaUpdateDone(ctx, logger, pcs); err != nil {
			return false, err
		}
		return true, nil
	}

	if len(currentProgress.InFlightPodGangs) == 0 {
		return false, r.populateInFlightPodGangs(ctx, logger, pcs, currentProgress)
	}

	// Check if all in-flight PodGangs have become Available.
	available, err := componentutils.ArePodGangsAvailable(ctx, r.client, pcs.Namespace, currentProgress.InFlightPodGangs)
	if err != nil {
		return false, groveerr.WrapError(err,
			errCodeListPCLQs,
			component.OperationSync,
			"failed to check availability of in-flight PodGangs",
		)
	}
	if !available {
		logger.Info("Waiting for in-flight PodGangs to become Available",
			"replicaIndex", replicaIndex,
			"inFlightPodGangs", currentProgress.InFlightPodGangs)
		return false, groveerr.New(
			groveerr.ErrCodeContinueReconcileAndRequeue,
			component.OperationSync,
			fmt.Sprintf("coherent update of PodCliqueSet replica %d in progress, waiting for in-flight PodGangs to become Available", replicaIndex),
		)
	}

	// All in-flight PodGangs are Available but the replica is not yet fully updated. Clear
	// InFlightPodGangs and requeue so the PodGangMap component computes the next iteration's
	// entries on the next reconcile. The requeue is required because patchUpdateProgressStatus
	// only mutates status, and the PCS controller's For-watch uses GenerationChangedPredicate —
	// without an explicit requeue here no further reconcile would fire to advance the update.
	logger.Info("Current iteration complete, seeking next update target", "replicaIndex", replicaIndex)
	original := pcs.DeepCopy()
	pcs.Status.UpdateProgress.CurrentlyUpdating[0].InFlightPodGangs = nil
	if err := r.patchUpdateProgressStatus(ctx, logger, pcs, original); err != nil {
		return false, err
	}
	return false, groveerr.New(
		groveerr.ErrCodeContinueReconcileAndRequeue,
		component.OperationSync,
		fmt.Sprintf("coherent update of PodCliqueSet replica %d cleared InFlightPodGangs; requeuing for next iteration", replicaIndex),
	)
}

// computeCoherentPendingWork identifies which replicas still need updating vs. which are done.
func (r _resource) computeCoherentPendingWork(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsIndicesToTerminate []int) (*coherentPendingWork, error) {
	replicaInfos, err := r.getPCSReplicaInfos(ctx, pcs, pcsIndicesToTerminate)
	if err != nil {
		return nil, err
	}
	work := &coherentPendingWork{}
	for _, ri := range replicaInfos {
		ri.computeUpdateProgress(pcs)
		if ri.updateDone {
			work.doneReplicaIndices = append(work.doneReplicaIndices, ri.replicaIndex)
		} else {
			work.pendingReplicaIndices = append(work.pendingReplicaIndices, ri.replicaIndex)
		}
	}
	return work, nil
}

// coherentPendingWork tracks PCS replicas that have been updated and
// replicas that are pending updates.
type coherentPendingWork struct {
	pendingReplicaIndices []int
	doneReplicaIndices    []int
}

func (w *coherentPendingWork) getNextPendingReplicaByIndex() *int {
	if len(w.pendingReplicaIndices) == 0 {
		return nil
	}
	slices.Sort(w.pendingReplicaIndices)
	return &w.pendingReplicaIndices[0]
}

// populateInFlightPodGangs reads the PodGangMap for the currently-updating replica,
// identifies new-hash entries whose PodGangs are not yet Available, and sets them
// as InFlightPodGangs in the PCS status.
func (r _resource) populateInFlightPodGangs(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, currentProgress *grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress) error {
	replicaIndex := currentProgress.ReplicaIndex
	pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: int(replicaIndex)})

	pgm, err := componentutils.GetPodGangMap(ctx, r.client, pgmName, pcs.Namespace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return groveerr.New(
				groveerr.ErrCodeContinueReconcileAndRequeue,
				component.OperationSync,
				fmt.Sprintf("PodGangMap %s not found for replica %d, requeueing", pgmName, replicaIndex),
			)
		}
		return groveerr.WrapError(err,
			errCodeListPCLQs,
			component.OperationSync,
			fmt.Sprintf("failed to get PodGangMap %s for replica %d", pgmName, replicaIndex),
		)
	}

	newHashEntries := componentutils.FilterPodGangMapEntriesByGenerationHash(pgm.Spec.Entries, *pcs.Status.CurrentGenerationHash)

	var inFlightNames []string
	for _, entry := range newHashEntries {
		pg, err := componentutils.GetPodGang(ctx, r.client, entry.Name, pcs.Namespace)
		if err != nil {
			if apierrors.IsNotFound(err) {
				inFlightNames = append(inFlightNames, entry.Name)
				continue
			}
			return groveerr.WrapError(err,
				errCodeListPCLQs,
				component.OperationSync,
				fmt.Sprintf("failed to get PodGang %s to check availability", entry.Name),
			)
		}
		if !k8sutils.IsConditionTrue(pg.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeAvailable)) {
			inFlightNames = append(inFlightNames, entry.Name)
		}
	}

	if len(inFlightNames) == 0 {
		return groveerr.New(
			groveerr.ErrCodeContinueReconcileAndRequeue,
			component.OperationSync,
			fmt.Sprintf("no new in-flight PodGangs found for replica %d, requeueing", replicaIndex),
		)
	}

	logger.Info("Populating InFlightPodGangs from PodGangMap", "replicaIndex", replicaIndex, "inFlightPodGangs", inFlightNames)
	original := pcs.DeepCopy()
	currentProgress.InFlightPodGangs = inFlightNames
	return r.patchUpdateProgressStatus(ctx, logger, pcs, original)
}

// /*
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

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// orchestrateCoherentUpdate manages the coherent update process for PodCliqueSet replicas.
// The orchestrator's responsibilities are narrowed to state machine progression:
//   - Pick the next replica to update
//   - Wait for the latest current-hash epoch's PodGangs to become Ready
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

// checkAndAdvanceCoherentUpdate drives one reconcile of the in-flight replica's coherent update.
//
// Under reconstruct-from-Spec the orchestrator holds no persisted control state: each reconcile it
// recomputes the latest current-hash epoch from the replica's PodGangMap (via latestCurrentHashEpoch)
// and gates on that epoch's readiness. InFlightEpochs mirrors that latest epoch and Message mirrors
// the current wait reason (observability + wait gate); both are overwritten each reconcile only when
// they change, and InFlightEpochs is never nil'd until the replica completes.
//
// Outcomes:
//   - replica already fully rolled  -> mark done, return (true, nil), caller falls through.
//   - PGM absent / no current-hash epoch yet -> requeue (PGM component has not emitted the first
//     sub-step).
//   - latest epoch not yet ready    -> reflect epoch + reason, requeue (wait).
//   - latest epoch ready but replica incomplete -> reflect epoch, requeue so the PGM component emits
//     the next sub-step (advancing the latest epoch next reconcile).
//
// The boolean return distinguishes "replica done, caller should continue" from the other terminal
// outcomes (waiting/requeue). markCurrentReplicaUpdateDone only patches status; the PCS controller's
// For-watch uses GenerationChangedPredicate and skips status-only events, so without same-reconcile
// fall-through the outer UpdateEndedAt would never be minted.
func (r _resource) checkAndAdvanceCoherentUpdate(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, updateWork *coherentPendingWork) (replicaDone bool, err error) {
	// NOTE: While the API makes a provision in the PCS status to potentially allow more than one PCS replica to be updated concurrently, none of the update strategies
	// currently supported allow more than one PCS replica to be updated. Therefore, we only check for index 0 of `CurrentlyUpdating`.
	// If and when concurrent PCS replica update is supported then we should iterate over all currently updating replicas.
	currentProgress := &pcs.Status.UpdateProgress.CurrentlyUpdating[0]
	replicaIndex := currentProgress.ReplicaIndex

	// Early-exit when this replica is already fully updated. computeCoherentPendingWork derives
	// updateWork.doneReplicaIndices from PCLQ/PCSG state, so the determination is independent of any
	// in-flight bookkeeping.
	if slices.Contains(updateWork.doneReplicaIndices, int(replicaIndex)) {
		logger.Info("Coherent update for replica completed", "replicaIndex", replicaIndex)
		if err = r.markCurrentReplicaUpdateDone(ctx, logger, pcs); err != nil {
			return false, err
		}
		return true, nil
	}

	// Recompute the latest current-hash epoch from the PodGangMap each reconcile.
	latestEpoch, err := r.latestCurrentHashEpoch(ctx, pcs, replicaIndex)
	if err != nil {
		return false, err
	}
	if latestEpoch == nil {
		// The PodGangMap component has not emitted a current-hash sub-step yet. Requeue.
		return false, r.requeueCoherentUpdate(ctx, logger, pcs,
			fmt.Sprintf("no current-hash epoch present in PodGangMap for replica %d, requeueing", replicaIndex))
	}

	// Reflect the latest epoch in status (overwrite, never clear), patching only on change.
	if !slices.Equal(currentProgress.InFlightEpochs, []string{*latestEpoch}) {
		original := pcs.DeepCopy()
		currentProgress.InFlightEpochs = []string{*latestEpoch}
		if err = r.patchUpdateProgressStatus(ctx, logger, pcs, original); err != nil {
			return false, err
		}
	}

	// Gate advance on the latest epoch's readiness.
	ready, err := componentutils.AllPodGangsAtEpochEverReady(ctx, r.client, pcs.Namespace, pcs.Name, replicaIndex, *latestEpoch)
	if err != nil {
		return false, groveerr.WrapError(err, errCodeListPCLQs, component.OperationSync,
			fmt.Sprintf("failed to check readiness of epoch %s for replica %d", *latestEpoch, replicaIndex))
	}
	if !ready {
		return false, r.requeueCoherentUpdate(ctx, logger, pcs,
			fmt.Sprintf("waiting for epoch %s PodGangs of replica %d to become Ready", *latestEpoch, replicaIndex))
	}

	// Latest epoch ready but the replica is not fully updated: current sub-step complete. Requeue so
	// the PodGangMap component emits the next sub-step. InFlightEpochs is left reflecting the
	// (now-ready) latest epoch; it is overwritten with the new epoch next reconcile.
	return false, r.requeueCoherentUpdate(ctx, logger, pcs,
		fmt.Sprintf("epoch %s of replica %d complete; awaiting next sub-step", *latestEpoch, replicaIndex))
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

// latestCurrentHashEpoch reads the PodGangMap for the replica and returns the latest grove.io/epoch
// among its current-hash entries, or nil when none have been emitted yet. A missing PodGangMap is
// treated as "not emitted yet" (nil) so the caller requeues rather than erroring — the PodGangMap
// component creates it on its own reconcile.
func (r _resource) latestCurrentHashEpoch(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, replicaIndex int32) (*string, error) {
	pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: int(replicaIndex)})
	pgm, err := componentutils.GetPodGangMap(ctx, r.client, pgmName, pcs.Namespace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, groveerr.WrapError(err, errCodeListPCLQs, component.OperationSync,
			fmt.Sprintf("failed to get PodGangMap %s for replica %d", pgmName, replicaIndex))
	}
	latestEpoch, err := componentutils.LatestEpochForGenerationHash(pgm.Spec.Entries, *pcs.Status.CurrentGenerationHash)
	if err != nil {
		return nil, groveerr.WrapError(err, errCodeListPCLQs, component.OperationSync,
			fmt.Sprintf("failed to derive latest current-hash epoch from PodGangMap %s for replica %d", pgmName, replicaIndex))
	}
	return latestEpoch, nil
}

// requeueCoherentUpdate records reason on CurrentlyUpdating[0].Message for observability (patching
// only when the message actually changes, so a steady wait does not write every reconcile) and
// returns a soft-requeue error carrying the same reason. It re-indexes CurrentlyUpdating[0] from the
// live pcs rather than trusting a caller-held pointer, because an earlier status patch in this
// reconcile replaces the backing slice.
func (r _resource) requeueCoherentUpdate(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, reason string) error {
	currentProgress := &pcs.Status.UpdateProgress.CurrentlyUpdating[0]
	if currentProgress.Message == nil || *currentProgress.Message != reason {
		original := pcs.DeepCopy()
		currentProgress.Message = ptr.To(reason)
		if err := r.patchUpdateProgressStatus(ctx, logger, pcs, original); err != nil {
			return err
		}
	}
	return groveerr.New(groveerr.ErrCodeContinueReconcileAndRequeue, component.OperationSync, reason)
}

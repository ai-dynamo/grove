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
	"context"
	"fmt"
	"slices"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ updateStrategyHooks = (*coherentStrategyHooks)(nil)

// coherentStrategyHooks supplies Coherent behavior to the rolling-update orchestrator.
type coherentStrategyHooks struct {
	pcs                                   *grovecorev1beta1.PodCliqueSet
	replicaInfos                          []pcsReplicaInfo
	minAvailableBreachedPCSReplicaIndices []int
	selectedPCSReplicaIndices             sets.Set[int]
}

// newCoherentStrategyHooks creates hooks for a Coherent update.
func newCoherentStrategyHooks(
	pcs *grovecorev1beta1.PodCliqueSet,
	replicaInfos []pcsReplicaInfo,
	minAvailableBreachedPCSReplicaIndices []int,
) *coherentStrategyHooks {
	return &coherentStrategyHooks{
		pcs:                                   pcs,
		replicaInfos:                          replicaInfos,
		minAvailableBreachedPCSReplicaIndices: minAvailableBreachedPCSReplicaIndices,
		selectedPCSReplicaIndices:             sets.New[int](),
	}
}

// reconcileCurrentlyUpdatingReplicas refreshes Coherent progress and returns completed PCS replica indices.
func (h *coherentStrategyHooks) reconcileCurrentlyUpdatingReplicas(currentlyUpdating []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress) (reconcileCurrentlyUpdatingResult, error) {
	result, selectedPCSReplicaIndices := reconcileCurrentlyUpdatingPCSReplicasForConfiguredStrategy(
		h.pcs,
		h.replicaInfos,
		currentlyUpdating,
	)
	h.selectedPCSReplicaIndices = selectedPCSReplicaIndices

	for _, replicaInfo := range h.replicaInfos {
		if !selectedPCSReplicaIndices.Has(replicaInfo.index) {
			continue
		}
		position := slices.IndexFunc(currentlyUpdating, func(progress grovecorev1beta1.PodCliqueSetReplicaUpdateProgress) bool {
			return progress.ReplicaIndex == int32(replicaInfo.index)
		})
		if err := refreshCoherentReplicaProgress(h.pcs, &currentlyUpdating[position], replicaInfo); err != nil {
			return reconcileCurrentlyUpdatingResult{}, err
		}
	}
	return result, nil
}

// selectReplicasToUpdate selects pending PCS replicas for the available update slots.
func (h *coherentStrategyHooks) selectReplicasToUpdate() []pcsReplicaUpdateSelection {
	return selectPCSReplicasForConfiguredStrategy(
		h.pcs,
		h.replicaInfos,
		h.selectedPCSReplicaIndices,
		h.minAvailableBreachedPCSReplicaIndices,
	)
}

// isUpdateComplete reports whether every relevant PCS replica completed its update.
func (h *coherentStrategyHooks) isUpdateComplete() bool {
	return isConfiguredStrategyUpdateComplete(h.pcs, h.replicaInfos)
}

// refreshCoherentReplicaProgress refreshes observability fields from the gathered replica snapshot.
func refreshCoherentReplicaProgress(
	pcs *grovecorev1beta1.PodCliqueSet,
	progress *grovecorev1beta1.PodCliqueSetReplicaUpdateProgress,
	replicaInfo pcsReplicaInfo) error {
	latestEpoch, err := componentutils.LatestEpochForGenerationHash(replicaInfo.pgmEntries, *pcs.Status.CurrentGenerationHash)
	if err != nil {
		return groveerr.WrapError(
			err,
			errCodeInvalidEpoch,
			component.OperationSync,
			fmt.Sprintf("could not determine the latest current-hash epoch for PodCliqueSet %v replica %d", client.ObjectKeyFromObject(pcs), replicaInfo.index),
		)
	}
	if latestEpoch == nil {
		progress.InFlightEpochs = nil
	} else {
		progress.InFlightEpochs = []string{*latestEpoch}
	}
	progress.Message = coherentProgressMessage(pcs, replicaInfo)
	return nil
}

// updateCoherentReplicaProgress records the observability fields for the replica currently under a
// coherent update. InFlightEpochs mirrors the latest current-hash epoch on the replica's PodGangMap, and
// Message summarizes the in-scope components that have not yet reached the current revision.
func (r *_resource) updateCoherentReplicaProgress(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, replicaInfo pcsReplicaInfo) error {
	inFlightEpochs, err := r.inFlightEpochsForReplica(ctx, pcs, replicaInfo.index)
	if err != nil {
		return err
	}
	original := pcs.DeepCopy()
	current := updateProgressForReplica(pcs.Status.UpdateProgress, replicaInfo.index)
	if current == nil {
		return fmt.Errorf("PodCliqueSet replica index %d is not selected for update", replicaInfo.index)
	}
	current.InFlightEpochs = inFlightEpochs
	current.Message = coherentProgressMessage(pcs, replicaInfo)
	return r.patchUpdateProgressStatus(ctx, logger, pcs, original)
}

// inFlightEpochsForReplica returns the latest current-hash epoch on the replica's PodGangMap, the batch
// the coherent engine is currently rolling. It is a list to leave room for a future iteration that rolls
// more than one batch at a time. It is empty when no PodGangMap or no current-hash entry exists yet.
func (r *_resource) inFlightEpochsForReplica(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet, pcsReplicaIndex int) ([]string, error) {
	pgm, err := componentutils.GetPodGangMap(ctx, r.client, client.ObjectKeyFromObject(pcs), pcsReplicaIndex)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, groveerr.WrapError(err, errCodeGetPodGangMap, component.OperationSync,
			fmt.Sprintf("could not get PodGangMap for PodCliqueSet %v replica %d", client.ObjectKeyFromObject(pcs), pcsReplicaIndex))
	}
	latestEpoch, err := componentutils.LatestEpochForGenerationHash(pgm.Spec.Entries, *pcs.Status.CurrentGenerationHash)
	if err != nil {
		return nil, groveerr.WrapError(err, errCodeInvalidEpoch, component.OperationSync,
			fmt.Sprintf("could not determine the latest current-hash epoch for PodCliqueSet %v replica %d", client.ObjectKeyFromObject(pcs), pcsReplicaIndex))
	}
	if latestEpoch == nil {
		return nil, nil
	}
	return []string{*latestEpoch}, nil
}

// updateProgressForReplica returns the update progress for the selected PCS replica index.
func updateProgressForReplica(progress *grovecorev1beta1.PodCliqueSetUpdateProgress, replicaIndex int) *grovecorev1beta1.PodCliqueSetReplicaUpdateProgress {
	if progress == nil {
		return nil
	}
	for i := range progress.CurrentlyUpdating {
		if int(progress.CurrentlyUpdating[i].ReplicaIndex) == replicaIndex {
			return &progress.CurrentlyUpdating[i]
		}
	}
	return nil
}

// coherentProgressMessage summarizes how many in-scope components of the replica have converged to the
// current revision, or returns nil once all have. It reports counts rather than names so the message
// stays bounded regardless of how many components an update touches.
func coherentProgressMessage(pcs *grovecorev1beta1.PodCliqueSet, replicaInfo pcsReplicaInfo) *string {
	inScopeStandalone, updatedStandalone, inScopePCSG, updatedPCSG := inScopeUpdateCounts(pcs, replicaInfo)
	if updatedStandalone == inScopeStandalone && updatedPCSG == inScopePCSG {
		return nil
	}
	var parts []string
	if inScopeStandalone > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d standalone PodCliques", updatedStandalone, inScopeStandalone))
	}
	if inScopePCSG > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d PodCliqueScalingGroups", updatedPCSG, inScopePCSG))
	}
	return new(fmt.Sprintf("%s updated to the current revision", strings.Join(parts, " and ")))
}

// inScopeUpdateCounts returns, for the update's in-scope components, how many are in scope and how many
// have converged to the current generation hash for the given replica, split by standalone PodCliques
// and PodCliqueScalingGroups.
func inScopeUpdateCounts(pcs *grovecorev1beta1.PodCliqueSet, replicaInfo pcsReplicaInfo) (inScopeStandalone, updatedStandalone, inScopePCSG, updatedPCSG int) {
	inScopeStandaloneNames := sets.New(pcs.Status.UpdateProgress.InScopeStandalonePodCliques...)
	inScopePCSGNames := sets.New(pcs.Status.UpdateProgress.InScopePodCliqueScalingGroups...)
	inScopeStandalone = inScopeStandaloneNames.Len()
	inScopePCSG = inScopePCSGNames.Len()
	for i := range replicaInfo.standalonePCLQs {
		pclq := &replicaInfo.standalonePCLQs[i]
		cliqueName, err := componentutils.GetPodCliqueNameFromPodCliqueFQN(pclq.ObjectMeta)
		if err != nil {
			continue
		}
		if inScopeStandaloneNames.Has(cliqueName) && componentutils.IsPCLQUpdateComplete(pcs, pclq) {
			updatedStandalone++
		}
	}
	pcsNameReplica := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replicaInfo.index}
	for i := range replicaInfo.pcsgs {
		pcsg := &replicaInfo.pcsgs[i]
		pcsgName, err := apicommon.ExtractScalingGroupNameFromPCSGFQN(pcsg.Name, pcsNameReplica)
		if err != nil {
			continue
		}
		if inScopePCSGNames.Has(pcsgName) && componentutils.IsPCSGUpdateComplete(pcsg, *pcs.Status.CurrentGenerationHash) {
			updatedPCSG++
		}
	}
	return
}

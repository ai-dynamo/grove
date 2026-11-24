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

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pcsexpectations "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/expectations"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/sync"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	errCodeComputingPCSReplicaDeletionWork grovecorev1beta1.ErrorCode = "ERR_COMPUTE_PCS_REPLICA_DELETION_WORK"
	errCodeDeletePCSReplica                grovecorev1beta1.ErrorCode = "ERR_DELETE_PCS_REPLICA"
	errCodeListPCLQs                       grovecorev1beta1.ErrorCode = "ERR_LIST_PCLQs"
	errCodeListPCSGs                       grovecorev1beta1.ErrorCode = "ERR_LIST_PCSGs"
	errCodeListServices                    grovecorev1beta1.ErrorCode = "ERR_LIST_SERVICES"
	errCodeUpdatePCSStatus                 grovecorev1beta1.ErrorCode = "ERR_UPDATE_PCS_STATUS"
	errCodeGetPodGangMap                   grovecorev1beta1.ErrorCode = "ERR_GET_PODGANGMAP"
	errCodeInvalidEpoch                    grovecorev1beta1.ErrorCode = "ERR_INVALID_EPOCH"
	errCodeComputeCoherentUpdateScope      grovecorev1beta1.ErrorCode = "ERR_COMPUTE_COHERENT_UPDATE_SCOPE"
)

type _resource struct {
	client                        client.Client
	eventRecorder                 record.EventRecorder
	generationHashExpectations    *pcsexpectations.GenerationHashes
	scalingProgressExpectations   *pcsexpectations.ScalingProgress
	currentlyUpdatingExpectations *pcsexpectations.CurrentlyUpdating
}

// New creates a new instance of the PodCliqueSetReplica operator.
func New(
	client client.Client,
	eventRecorder record.EventRecorder,
	scalingProgressExpectations *pcsexpectations.ScalingProgress,
	currentlyUpdatingExpectations *pcsexpectations.CurrentlyUpdating,
	generationHashExpectations *pcsexpectations.GenerationHashes,
) component.Operator[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result] {
	return &_resource{
		client:                        client,
		eventRecorder:                 eventRecorder,
		generationHashExpectations:    generationHashExpectations,
		scalingProgressExpectations:   scalingProgressExpectations,
		currentlyUpdatingExpectations: currentlyUpdatingExpectations,
	}
}

// GetExistingResourceNames is a no-op.
func (r *_resource) GetExistingResourceNames(_ context.Context, _ logr.Logger, _ metav1.ObjectMeta) ([]string, error) {
	return []string{}, nil
}

// Sync plans and persists PCS replica lifecycle progress before executing resource changes.
func (r *_resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, state *pcssync.State) (pcssync.Result, error) {
	workingPCS := pcs.DeepCopy()
	reconcileTime := metav1.Now()
	baselineInitialized, err := r.preparePCSGenerationChange(ctx, workingPCS, reconcileTime)
	if err != nil {
		return pcssync.Result{}, err
	}
	if baselineInitialized {
		if _, err := r.patchPCSScalingAndUpdateProgress(ctx, pcs, workingPCS); err != nil {
			return pcssync.Result{}, err
		}
		return pcssync.Result{}, groveerr.NewRequeueAfter(component.OperationSync,
			"waiting for PCS generation initialization to become visible in the informer cache",
			constants.InformerCacheSyncRetryInterval)
	}

	pcsReplicaInfosByIndex, err := r.gatherPCSReplicaInfosByIndex(ctx, workingPCS)
	if err != nil {
		return pcssync.Result{}, err
	}
	// Configured-strategy selections carry no replacement ownership and must not block replica recreation.
	if componentutils.IsPCSReplicaRecreationInProgress(workingPCS) {
		workingPCS.Status.UpdateProgress.CurrentlyUpdating = slices.DeleteFunc(
			workingPCS.Status.UpdateProgress.CurrentlyUpdating,
			func(progress grovecorev1beta1.PodCliqueSetReplicaUpdateProgress) bool {
				return progress.Replacement == nil
			})
	}
	scalingIndices := preparePCSReplicaScaling(workingPCS, pcsReplicaInfosByIndex, reconcileTime)
	pcsReplicaIndicesAfterScaling := computePCSReplicaIndicesAfterScaling(pcsReplicaInfosByIndex.existingReplicaIndices(), scalingIndices)
	gangTerminationCandidates := computePCSReplicaGangTerminationCandidates(workingPCS, pcsReplicaIndicesAfterScaling)
	delWork, err := r.getPCSReplicaDeletionWork(ctx, logger, workingPCS, gangTerminationCandidates, reconcileTime.Time)
	if err != nil {
		return pcssync.Result{}, groveerr.WrapError(err,
			errCodeComputingPCSReplicaDeletionWork,
			component.OperationSync,
			fmt.Sprintf("Could not compute replica deletion work for PCS: %v", client.ObjectKeyFromObject(pcs)))
	}

	pcsUpdateInProgress := componentutils.IsRollingUpdateInProgress(workingPCS) || componentutils.IsPCSReplicaRecreationInProgress(workingPCS)
	pcsUpdateCompleted := false
	if pcsUpdateInProgress {
		updateHooks := newUpdateStrategyHooks(workingPCS, pcsReplicaInfosByIndex, pcsReplicaIndicesAfterScaling, delWork)
		if err := advancePCSUpdateProgress(workingPCS, updateHooks, reconcileTime); err != nil {
			return pcssync.Result{}, err
		}
		pcsUpdateCompleted = workingPCS.Status.UpdateProgress.UpdateEndedAt != nil
		pcsUpdateInProgress = !pcsUpdateCompleted
	}

	previousScalingIndices := replicaScalingIndicesFromProgress(pcs.Status.ScalingProgress)
	changed, err := r.patchPCSScalingAndUpdateProgress(ctx, pcs, workingPCS)
	if err != nil {
		return pcssync.Result{}, err
	}
	if changed {
		r.recordNewScalingSelectionEvents(logger, pcs, previousScalingIndices, replicaScalingIndicesFromProgress(pcs.Status.ScalingProgress))
		if pcsUpdateCompleted {
			logger.Info("Rolling update has completed")
		}
		return pcssync.Result{}, groveerr.NewRequeueAfter(component.OperationSync,
			"waiting for PCS scaling and update progress to become visible in the informer cache",
			constants.InformerCacheSyncRetryInterval)
	}

	// Initial provisioning records no reservations, so replacement targets must claim their indices first.
	replacementGroups := groupPCSReplicaReplacements(pcs.Status.UpdateProgress)
	initialPCSReplicaIndices := computeInitialPCSReplicaProvisioningIndices(pcs, pcsReplicaInfosByIndex, scalingIndices, replacementGroups)
	pcsReplicaIndicesAfterScaling.Insert(initialPCSReplicaIndices...)
	if err := r.executePCSReplicaGangTermination(ctx, logger, pcs, delWork.pcsIndicesToTerminate); err != nil {
		return pcssync.Result{}, err
	}
	*state = computePCSReplicaState(pcs, scalingIndices, pcsReplicaIndicesAfterScaling, replacementGroups)

	if delWork.shouldRequeue() {
		return pcssync.Result{}, groveerr.NewContinueReconcileAndRequeue(
			component.OperationSync,
			"Requeuing to re-process PCS replicas that have breached MinAvailable but not crossed TerminationDelay",
		)
	}
	if pcsUpdateInProgress {
		return pcssync.Result{}, groveerr.NewContinueReconcileAndRequeue(
			component.OperationSync,
			fmt.Sprintf("rolling update of PodCliqueSet replica indices %v is in progress", activePCSReplicaUpdateIndices(pcs.Status.UpdateProgress)),
		)
	}
	return pcssync.Result{}, nil
}

// Delete is a no-op.
func (r *_resource) Delete(_ context.Context, _ logr.Logger, _ metav1.ObjectMeta) error {
	return nil
}

// pcsReplicaInfosForUpdate returns information for PCS replicas eligible to participate in update orchestration.
// It follows expectedPCSReplicaIndices, so replicas selected for scale-in are excluded, and also excludes replicas
// selected for gang termination. The result preserves the order of expectedPCSReplicaIndices.
func pcsReplicaInfosForUpdate(
	pcsReplicaInfosByIndex pcsReplicaInfoByIndex,
	expectedPCSReplicaIndices []int,
	pcsReplicaIndicesToTerminate []int) []pcsReplicaInfo {
	pcsReplicaIndicesSelectedForTermination := sets.New(pcsReplicaIndicesToTerminate...)
	pcsReplicaInfos := make([]pcsReplicaInfo, 0, len(expectedPCSReplicaIndices))
	for _, pcsReplicaIndex := range expectedPCSReplicaIndices {
		if pcsReplicaIndicesSelectedForTermination.Has(pcsReplicaIndex) {
			continue
		}
		pcsReplicaInfoForUpdate := pcsReplicaInfosByIndex[pcsReplicaIndex]
		pcsReplicaInfoForUpdate.index = pcsReplicaIndex
		pcsReplicaInfos = append(pcsReplicaInfos, pcsReplicaInfoForUpdate)
	}
	return pcsReplicaInfos
}

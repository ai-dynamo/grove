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
	"maps"
	"slices"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pcsexpectations "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/expectations"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/sync"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"
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
)

type _resource struct {
	client                        client.Client
	eventRecorder                 record.EventRecorder
	scalingProgressExpectations   *pcsexpectations.ScalingProgress
	currentlyUpdatingExpectations *pcsexpectations.CurrentlyUpdating
}

// New creates a new instance of the PodCliqueSetReplica operator.
func New(client client.Client, eventRecorder record.EventRecorder, scalingProgressExpectations *pcsexpectations.ScalingProgress, currentlyUpdatingExpectations *pcsexpectations.CurrentlyUpdating) component.Operator[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result] {
	return &_resource{
		client:                        client,
		eventRecorder:                 eventRecorder,
		scalingProgressExpectations:   scalingProgressExpectations,
		currentlyUpdatingExpectations: currentlyUpdatingExpectations,
	}
}

// GetExistingResourceNames is a no-op.
func (r *_resource) GetExistingResourceNames(_ context.Context, _ logr.Logger, _ metav1.ObjectMeta) ([]string, error) {
	return []string{}, nil
}

// Sync orchestrates replica deletion and rolling updates for the PodCliqueSet.
func (r *_resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, state *pcssync.State) (pcssync.Result, error) {
	pcsReplicaInfosByIndex, err := r.gatherPCSReplicaInfosByIndex(ctx, pcs)
	if err != nil {
		return pcssync.Result{}, err
	}
	scalingWork, scalingProgressChanged, err := r.syncScalingProgress(ctx, logger, pcs, pcsReplicaInfosByIndex)
	if err != nil {
		return pcssync.Result{}, err
	}
	scalingIndices := scalingWork.scalingIndices

	// Expected replica indices implied by the scaling decision alone. Gang termination and update
	// eligibility use these before any replacement overlay is applied.
	expectedReplicaIndices := expectedReplicaIndicesFromScaling(pcsReplicaInfosByIndex.existingReplicaIndices(), scalingIndices)
	sortedExpectedPCSReplicaIndices := sortedReplicaIndices(expectedReplicaIndices)

	pcsObjectKey := client.ObjectKeyFromObject(pcs)
	delWork, err := r.getPCSReplicaDeletionWork(ctx, logger, pcs, sortedExpectedPCSReplicaIndices)
	if err != nil {
		return pcssync.Result{}, groveerr.WrapError(err,
			errCodeComputingPCSReplicaDeletionWork,
			component.OperationSync,
			fmt.Sprintf("Could not compute pending replica deletion delWork for PCS: %v", pcsObjectKey))
	}

	if delWork.hasPendingPCSReplicaDeletion() {
		if runResult := utils.RunConcurrentlyAndWait(ctx, logger, delWork.deletionTasks); runResult.HasErrors() {
			return pcssync.Result{}, groveerr.WrapError(runResult.GetAggregatedError(),
				errCodeDeletePCSReplica,
				component.OperationSync,
				fmt.Sprintf("Error deleting PodCliques for PodCliqueSet: %v", pcsObjectKey),
			)
		}
	}

	// Orchestrate the active rolling update. A non-empty scalingWork.replacementSourcesToCancel implies an
	// active replacement, so PCS replica recreation is in progress and this block runs. The orchestrator owns
	// UpdateProgress, including canceling the replacements that scaling reclaimed.
	if componentutils.IsRollingUpdateInProgress(pcs) || componentutils.IsPCSReplicaRecreationInProgress(pcs) {
		pcsReplicaInfos := pcsReplicaInfosForUpdate(pcsReplicaInfosByIndex, sortedExpectedPCSReplicaIndices, delWork.pcsIndicesToTerminate)
		minAvailableBreachedPCSReplicaIndices := slices.Collect(maps.Keys(delWork.minAvailableBreachedConstituents))
		workingPCS := pcs.DeepCopy()
		updateHooks := newUpdateStrategyHooks(
			workingPCS,
			pcsReplicaInfosByIndex,
			pcsReplicaInfos,
			minAvailableBreachedPCSReplicaIndices,
		)
		orchestrator := newRollingUpdateOrchestrator(r.client.Status(), r.currentlyUpdatingExpectations, workingPCS, updateHooks)
		orchestrationCtx := logr.NewContext(ctx, logger)
		if err := orchestrator.orchestrate(orchestrationCtx, pcs, scalingWork.replacementSourcesToCancel); err != nil {
			return pcssync.Result{}, err
		}
	}

	// Build the component-facing state once, after scaling and any replacement mapping have been persisted
	// back into the reconcile-local pcs. It overlays persisted replacement progress onto the scaling indices.
	*state = computePCSReplicaState(pcs, pcsReplicaInfosByIndex, scalingIndices, expectedReplicaIndices, groupPCSReplicaReplacements(pcs.Status.UpdateProgress))

	if delWork.shouldRequeue() {
		return pcssync.Result{}, groveerr.NewContinueReconcileAndRequeue(
			component.OperationSync,
			"Requeuing to re-process PCS replicas that have breached MinAvailable but not crossed TerminationDelay",
		)
	}
	if scalingProgressChanged {
		return pcssync.Result{}, groveerr.NewContinueReconcileAndRequeue(
			component.OperationSync,
			"Requeuing after updating PodCliqueSet scaling progress",
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

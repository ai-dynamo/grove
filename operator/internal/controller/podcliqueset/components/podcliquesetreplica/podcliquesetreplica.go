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
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/sync"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	client                      client.Client
	eventRecorder               record.EventRecorder
	scalingProgressExpectations *pcssync.ScalingProgressExpectations
}

// New creates a new instance of the PodCliqueSetReplica operator.
func New(client client.Client, eventRecorder record.EventRecorder, scalingProgressExpectations *pcssync.ScalingProgressExpectations) component.Operator[grovecorev1beta1.PodCliqueSet, pcssync.State, pcssync.Result] {
	return &_resource{
		client:                      client,
		eventRecorder:               eventRecorder,
		scalingProgressExpectations: scalingProgressExpectations,
	}
}

// GetExistingResourceNames is a no-op.
func (r *_resource) GetExistingResourceNames(_ context.Context, _ logr.Logger, _ metav1.ObjectMeta) ([]string, error) {
	return []string{}, nil
}

// Sync orchestrates replica deletion and rolling updates for the PodCliqueSet.
func (r *_resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, state *pcssync.State) (pcssync.Result, error) {
	scalingProgressChanged, err := r.syncScalingProgress(ctx, logger, pcs, state)
	if err != nil {
		return pcssync.Result{}, err
	}

	pcsObjectKey := client.ObjectKeyFromObject(pcs)

	delWork, err := r.getPCSReplicaDeletionWork(ctx, logger, pcs, state.ExpectedReplicaIndices)
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

	// Orchestrate the rolling update (RollingRecreate or Coherent) while one is in progress.
	if componentutils.IsRollingUpdateInProgress(pcs) || componentutils.IsPCSReplicaRecreationInProgress(pcs) {
		replicaInfos, err := r.getPCSReplicaInfos(ctx, pcs, state.ExpectedReplicaIndices, delWork.pcsIndicesToTerminate)
		if err != nil {
			return pcssync.Result{}, err
		}
		minAvailableBreachedPCSReplicaIndices := slices.Collect(maps.Keys(delWork.minAvailableBreachedConstituents))
		if err := r.orchestrateRollingUpdate(ctx, logger, pcs, replicaInfos, minAvailableBreachedPCSReplicaIndices); err != nil {
			return pcssync.Result{}, err
		}
	}

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

// getPCSReplicaInfos fetches the PCLQs and PCSGs for each PCS replica.
func (r *_resource) getPCSReplicaInfos(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet, expectedPCSReplicaIndices, pcsReplicaIndicesToTerminate []int) ([]pcsReplicaInfo, error) {
	pcsObjectKey := client.ObjectKeyFromObject(pcs)
	pclqsByPCSReplicaIndex, err := componentutils.GetStandalonePCLQsByPCSReplicaIndex(ctx, r.client, pcs.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPCLQs,
			component.OperationSync,
			fmt.Sprintf("could not list PCLQs for PCS: %v", pcsObjectKey),
		)
	}
	pcsgsByPCSReplicaIndex, err := componentutils.GetPCSGsByPCSReplicaIndex(ctx, r.client, pcs.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPCSGs,
			component.OperationSync,
			fmt.Sprintf("could not list PCSGs for PCS: %v", pcsObjectKey),
		)
	}
	// A coherent update gates completion on the replica's PodGangMap having reconverged to a single
	// generation, so its entries are gathered here. One list serves every replica.
	var pgmsByPCSReplicaIndex map[int]*grovecorev1beta1.PodGangMap
	if componentutils.IsCoherentUpdateInProgress(pcs) {
		pgms, listErr := componentutils.ListPodGangMapsForPCS(ctx, r.client, pcs.ObjectMeta)
		if listErr != nil {
			return nil, groveerr.WrapError(listErr,
				errCodeGetPodGangMap,
				component.OperationSync,
				fmt.Sprintf("could not list PodGangMaps for PCS: %v", pcsObjectKey),
			)
		}
		pgmsByPCSReplicaIndex, err = componentutils.PodGangMapByPCSReplicaIndex(pgms)
		if err != nil {
			return nil, groveerr.WrapError(err,
				errCodeGetPodGangMap,
				component.OperationSync,
				fmt.Sprintf("could not index PodGangMaps by PCS replica for PCS: %v", pcsObjectKey),
			)
		}
	}
	replicaInfos := make([]pcsReplicaInfo, 0, len(expectedPCSReplicaIndices))
	for _, pcsReplicaIndex := range expectedPCSReplicaIndices {
		if slices.Contains(pcsReplicaIndicesToTerminate, pcsReplicaIndex) {
			continue
		}
		var pgmEntries []grovecorev1beta1.PodGangEntry
		if pgm := pgmsByPCSReplicaIndex[pcsReplicaIndex]; pgm != nil {
			pgmEntries = pgm.Spec.Entries
		}
		replicaInfos = append(replicaInfos, pcsReplicaInfo{
			index:           pcsReplicaIndex,
			standalonePCLQs: pclqsByPCSReplicaIndex[pcsReplicaIndex],
			pcsgs:           pcsgsByPCSReplicaIndex[pcsReplicaIndex],
			pgmEntries:      pgmEntries,
		})
	}
	return replicaInfos, nil
}

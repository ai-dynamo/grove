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

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
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
	errCodeComputingPCSReplicaDeletionWork grovecorev1alpha1.ErrorCode = "ERR_COMPUTE_PCS_REPLICA_DELETION_WORK"
	errCodeDeletePCSReplica                grovecorev1alpha1.ErrorCode = "ERR_DELETE_PCS_REPLICA"
	errCodeListPCLQs                       grovecorev1alpha1.ErrorCode = "ERR_LIST_PCLQs"
	errCodeListPCSGs                       grovecorev1alpha1.ErrorCode = "ERR_LIST_PCSGs"
	errCodeListServices                    grovecorev1alpha1.ErrorCode = "ERR_LIST_SERVICES"
	errCodeUpdatePCSStatus                 grovecorev1alpha1.ErrorCode = "ERR_UPDATE_PCS_STATUS"
	errCodeGetPodGangMap                   grovecorev1alpha1.ErrorCode = "ERR_GET_PODGANGMAP"
	errCodeInvalidEpoch                    grovecorev1alpha1.ErrorCode = "ERR_INVALID_EPOCH"
)

type _resource struct {
	client                      client.Client
	eventRecorder               record.EventRecorder
	scalingProgressExpectations *pcssync.ScalingProgressExpectations
}

// New creates a new instance of the PodCliqueSetReplica operator.
func New(client client.Client, eventRecorder record.EventRecorder, scalingProgressExpectations *pcssync.ScalingProgressExpectations) component.Operator[grovecorev1alpha1.PodCliqueSet, pcssync.State, pcssync.Result] {
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
func (r *_resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, state *pcssync.State) (pcssync.Result, error) {
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
	if componentutils.IsRollingUpdateInProgress(pcs) {
		minAvailableBreachedPCSReplicaIndices := slices.Collect(maps.Keys(delWork.minAvailableBreachedConstituents))
		if err := r.orchestrateRollingUpdate(ctx, logger, pcs, state.ExpectedReplicaIndices, delWork.pcsIndicesToTerminate, minAvailableBreachedPCSReplicaIndices); err != nil {
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

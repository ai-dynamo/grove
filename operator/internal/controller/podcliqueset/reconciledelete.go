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

package podcliqueset

import (
	"context"
	"fmt"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	"github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// triggerDeletionFlow handles the deletion of a PodCliqueSet.
// Owned resources (PodCliqueScalingGroup, PodClique, Service, ServiceAccount, Role,
// RoleBinding, HPA, PodGang, ResourceClaim, etc.) carry a controller owner reference
// to the PodCliqueSet, so removing the finalizer hands cleanup to the Kubernetes
// garbage collector — which cascades the entire subtree without per-resource
// reconcile work in this controller.
func (r *Reconciler) triggerDeletionFlow(ctx context.Context, logger logr.Logger, pcs *v1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	if stepResult := r.removeFinalizer(ctx, logger, pcs); ctrlcommon.ShortCircuitReconcileFlow(stepResult) {
		return r.recordIncompleteDeletion(ctx, logger, pcs, &stepResult)
	}
	logger.Info("PodCliqueSet finalizer removed; Kubernetes garbage collector will cascade-delete owned resources")
	return ctrlcommon.DoNotRequeue()
}

// removeFinalizer removes the PodCliqueSet finalizer if present.
func (r *Reconciler) removeFinalizer(ctx context.Context, logger logr.Logger, pcs *v1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	if !controllerutil.ContainsFinalizer(pcs, constants.FinalizerPodCliqueSet) {
		logger.Info("Finalizer not found", "PodCliqueSet", pcs)
		return ctrlcommon.ContinueReconcile()
	}
	logger.Info("Removing finalizer", "PodCliqueSet", pcs, "finalizerName", constants.FinalizerPodCliqueSet)
	if err := ctrlutils.RemoveAndPatchFinalizer(ctx, r.client, pcs, constants.FinalizerPodCliqueSet); err != nil {
		return ctrlcommon.ReconcileWithErrors("error removing finalizer", fmt.Errorf("failed to remove finalizer: %s from PodCliqueSet: %v: %w", constants.FinalizerPodCliqueSet, client.ObjectKeyFromObject(pcs), err))
	}
	return ctrlcommon.ContinueReconcile()
}

// recordIncompleteDeletion records errors that occurred during deletion and returns the combined result.
func (r *Reconciler) recordIncompleteDeletion(ctx context.Context, logger logr.Logger, pcs *v1alpha1.PodCliqueSet, errResult *ctrlcommon.ReconcileStepResult) ctrlcommon.ReconcileStepResult {
	if err := r.reconcileStatusRecorder.RecordErrors(ctx, pcs, errResult); err != nil {
		logger.Error(err, "failed to record deletion completion operation", "PodCliqueSet", pcs)
		// combine all errors
		allErrs := append(errResult.GetErrors(), err)
		return ctrlcommon.ReconcileWithErrors("error recording incomplete reconciliation", allErrs...)
	}
	return *errResult
}

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

package podclique

import (
	"context"
	"fmt"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"
	"github.com/ai-dynamo/grove/operator/internal/expect"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// triggerDeletionFlow handles the deletion of a PodClique. Owned Pods (and any
// PCLQ-scoped ResourceClaims) carry a controller owner reference back to this
// PodClique, so removing the finalizer hands the cascade off to the Kubernetes
// garbage collector. Local in-memory state (the expectations store) is the only
// thing the controller still has to clean up itself.
func (r *Reconciler) triggerDeletionFlow(ctx context.Context, logger logr.Logger, pclq *grovecorev1alpha1.PodClique) ctrlcommon.ReconcileStepResult {
	dLog := logger.WithValues("operation", "delete")
	deleteStepFns := []ctrlcommon.ReconcileStepFn[grovecorev1alpha1.PodClique]{
		r.clearPodCliqueExpectations,
		r.removeFinalizer,
	}
	for _, fn := range deleteStepFns {
		if stepResult := fn(ctx, dLog, pclq); ctrlcommon.ShortCircuitReconcileFlow(stepResult) {
			return stepResult
		}
	}
	dLog.Info("PodClique finalizer removed; Kubernetes garbage collector will cascade-delete owned Pods")
	return ctrlcommon.DoNotRequeue()
}

// clearPodCliqueExpectations drops the in-memory expectations entry for this
// PodClique so the store does not retain stale UIDs after the object is gone.
func (r *Reconciler) clearPodCliqueExpectations(_ context.Context, logger logr.Logger, pclq *grovecorev1alpha1.PodClique) ctrlcommon.ReconcileStepResult {
	key, err := expect.ControlleeKeyFunc(pclq)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("error building expectations store key", fmt.Errorf("failed to build expectations store key for PodClique %v: %w", client.ObjectKeyFromObject(pclq), err))
	}
	if err := r.expectationsStore.DeleteExpectations(logger, key); err != nil {
		return ctrlcommon.ReconcileWithErrors("error clearing expectations", err)
	}
	return ctrlcommon.ContinueReconcile()
}

// removeFinalizer removes the PodClique finalizer to allow Kubernetes to complete the deletion
func (r *Reconciler) removeFinalizer(ctx context.Context, logger logr.Logger, pclq *grovecorev1alpha1.PodClique) ctrlcommon.ReconcileStepResult {
	if !controllerutil.ContainsFinalizer(pclq, constants.FinalizerPodClique) {
		logger.Info("Finalizer not found", "PodClique", pclq)
		return ctrlcommon.DoNotRequeue()
	}
	logger.Info("Removing finalizer", "PodClique", pclq, "finalizerName", constants.FinalizerPodClique)
	if err := ctrlutils.RemoveAndPatchFinalizer(ctx, r.client, pclq, constants.FinalizerPodClique); err != nil {
		return ctrlcommon.ReconcileWithErrors("error removing finalizer", fmt.Errorf("failed to remove finalizer: %s from PodClique: %v: %w", constants.FinalizerPodClique, client.ObjectKeyFromObject(pclq), err))
	}
	return ctrlcommon.ContinueReconcile()
}

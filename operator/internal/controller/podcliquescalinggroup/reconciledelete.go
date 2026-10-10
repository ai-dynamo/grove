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

package podcliquescalinggroup

import (
	"context"
	"fmt"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	pcsgexpectations "github.com/ai-dynamo/grove/operator/internal/controller/podcliquescalinggroup/expectations"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// triggerDeletionFlow handles the deletion of a PodCliqueScalingGroup.
// Child PodCliques carry a controller owner reference to this PCSG, so finalizer
// removal hands the cascade off to the Kubernetes garbage collector.
func (r *Reconciler) triggerDeletionFlow(ctx context.Context, logger logr.Logger, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) ctrlcommon.ReconcileStepResult {
	dLog := logger.WithValues("operation", "delete")
	deleteStepFns := []ctrlcommon.ReconcileStepFn[grovecorev1alpha1.PodCliqueScalingGroup]{
		r.clearExpectations,
		r.removeFinalizer,
	}
	for _, fn := range deleteStepFns {
		if stepResult := fn(ctx, dLog, pcsg); ctrlcommon.ShortCircuitReconcileFlow(stepResult) {
			return stepResult
		}
	}
	dLog.Info("PodCliqueScalingGroup finalizer removed; Kubernetes garbage collector will cascade-delete owned PodCliques")
	return ctrlcommon.DoNotRequeue()
}

// clearExpectations drops the in-memory delete-expectations entry for this PodCliqueScalingGroup so
// the store does not retain an entry after the object is gone. It runs before finalizer removal so a
// failure is retried while the object still exists.
func (r *Reconciler) clearExpectations(_ context.Context, logger logr.Logger, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) ctrlcommon.ReconcileStepResult {
	if err := pcsgexpectations.ClearPCSGExpectations(logger, r.expectationStore, pcsg.ObjectMeta); err != nil {
		return ctrlcommon.ReconcileWithErrors("error clearing expectations", err)
	}
	return ctrlcommon.ContinueReconcile()
}

// removeFinalizer removes the PodCliqueScalingGroup finalizer to allow Kubernetes to complete the deletion
func (r *Reconciler) removeFinalizer(ctx context.Context, logger logr.Logger, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) ctrlcommon.ReconcileStepResult {
	if !controllerutil.ContainsFinalizer(pcsg, constants.FinalizerPodCliqueScalingGroup) {
		logger.Info("Finalizer not found", "PodCliqueScalingGroup", pcsg)
		return ctrlcommon.DoNotRequeue()
	}
	logger.Info("Removing finalizer", "PodCliqueScalingGroup", pcsg, "finalizerName", constants.FinalizerPodCliqueScalingGroup)
	if err := ctrlutils.RemoveAndPatchFinalizer(ctx, r.client, pcsg, constants.FinalizerPodCliqueScalingGroup); err != nil {
		return ctrlcommon.ReconcileWithErrors("error removing finalizer", fmt.Errorf("failed to remove finalizer: %s from PodCliqueScalingGroup: %v: %w", constants.FinalizerPodCliqueScalingGroup, client.ObjectKeyFromObject(pcsg), err))
	}
	return ctrlcommon.ContinueReconcile()
}

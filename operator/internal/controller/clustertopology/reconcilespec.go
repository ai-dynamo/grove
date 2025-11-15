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

package clustertopology

import (
	"context"
	"fmt"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveconstants "github.com/ai-dynamo/grove/operator/internal/constants"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// reconcileSpec performs the main reconciliation logic for ClusterTopology spec.
func (r *Reconciler) reconcileSpec(ctx context.Context, logger logr.Logger, ct *grovecorev1alpha1.ClusterTopology) ctrlcommon.ReconcileStepResult {
	log := logger.WithValues("operation", "spec-reconcile")

	reconcileStepFns := []ctrlcommon.ReconcileStepFn[grovecorev1alpha1.ClusterTopology]{
		r.ensureFinalizer,
	}

	for _, fn := range reconcileStepFns {
		if stepResult := fn(ctx, log, ct); ctrlcommon.ShortCircuitReconcileFlow(stepResult) {
			return stepResult
		}
	}

	log.Info("Finished spec reconciliation flow", "ClusterTopology", ct.Name)
	return ctrlcommon.ContinueReconcile()
}

// ensureFinalizer adds the ClusterTopology finalizer if not already present.
func (r *Reconciler) ensureFinalizer(ctx context.Context, logger logr.Logger, ct *grovecorev1alpha1.ClusterTopology) ctrlcommon.ReconcileStepResult {
	if !controllerutil.ContainsFinalizer(ct, constants.FinalizerClusterTopology) {
		logger.Info("Adding finalizer", "finalizerName", constants.FinalizerClusterTopology)
		if err := ctrlutils.AddAndPatchFinalizer(ctx, r.client, ct, constants.FinalizerClusterTopology); err != nil {
			r.eventRecorder.Eventf(ct, corev1.EventTypeWarning, groveconstants.ReasonClusterTopologyCreateOrUpdateFailed,
				"Failed to add finalizer %s to ClusterTopology %s: %v", constants.FinalizerClusterTopology, ct.Name, err)
			return ctrlcommon.ReconcileWithErrors("error adding finalizer", fmt.Errorf("failed to add finalizer: %s to ClusterTopology: %s: %w", constants.FinalizerClusterTopology, ct.Name, err))
		}
		r.eventRecorder.Eventf(ct, corev1.EventTypeNormal, groveconstants.ReasonClusterTopologyCreateOrUpdateSuccessful,
			"Successfully added finalizer %s to ClusterTopology %s", constants.FinalizerClusterTopology, ct.Name)
	}
	return ctrlcommon.ContinueReconcile()
}

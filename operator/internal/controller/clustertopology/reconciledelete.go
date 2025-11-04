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
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// triggerDeletionFlow handles the deletion of a ClusterTopology with deletion prevention checks.
func (r *Reconciler) triggerDeletionFlow(ctx context.Context, logger logr.Logger, ct *grovecorev1alpha1.ClusterTopology) ctrlcommon.ReconcileStepResult {
	deleteStepFns := []ctrlcommon.ReconcileStepFn[grovecorev1alpha1.ClusterTopology]{
		r.checkDeletionConditions,
		r.removeFinalizer,
	}

	for _, fn := range deleteStepFns {
		if stepResult := fn(ctx, logger, ct); ctrlcommon.ShortCircuitReconcileFlow(stepResult) {
			return stepResult
		}
	}

	logger.Info("ClusterTopology deleted successfully")
	return ctrlcommon.DoNotRequeue()
}

// checkDeletionConditions verifies that ClusterTopology can be safely deleted.
// Deletion is blocked if:
// 1. Any PodCliqueSet references this topology (via grove.io/topology-name label)
// 2. Topology is enabled AND this specific topology is configured
func (r *Reconciler) checkDeletionConditions(ctx context.Context, logger logr.Logger, ct *grovecorev1alpha1.ClusterTopology) ctrlcommon.ReconcileStepResult {
	// Condition 1: Check for PodCliqueSet references
	pcsList := &grovecorev1alpha1.PodCliqueSetList{}
	labelSelector := client.MatchingLabels{
		apicommon.LabelTopologyName: ct.Name,
	}
	if err := r.client.List(ctx, pcsList, labelSelector); err != nil {
		return ctrlcommon.ReconcileWithErrors("failed to list PodCliqueSet resources", err)
	}
	if len(pcsList.Items) > 0 {
		logger.Info("Cannot delete ClusterTopology: referenced by PodCliqueSet resources",
			"topologyName", ct.Name,
			"podCliqueSetCount", len(pcsList.Items))
		return ctrlcommon.ReconcileAfter(30*time.Second, "topology referenced by PodCliqueSet resources")
	}

	// Condition 2: Check if topology is enabled and configured to use this ClusterTopology
	if r.config.Enabled {
		configuredName := "grove-topology" // default
		if r.config.Name != nil {
			configuredName = *r.config.Name
		}
		if configuredName == ct.Name {
			logger.Info("Cannot delete ClusterTopology: topology feature is enabled and configured to use this topology",
				"topologyName", ct.Name,
				"configuredTopologyName", configuredName)
			return ctrlcommon.ReconcileAfter(30*time.Second, "topology feature configured to use this ClusterTopology")
		}
	}

	logger.Info("ClusterTopology can be safely deleted", "topologyName", ct.Name)
	return ctrlcommon.ContinueReconcile()
}

// removeFinalizer removes the ClusterTopology finalizer if present.
func (r *Reconciler) removeFinalizer(ctx context.Context, logger logr.Logger, ct *grovecorev1alpha1.ClusterTopology) ctrlcommon.ReconcileStepResult {
	if !controllerutil.ContainsFinalizer(ct, constants.FinalizerClusterTopology) {
		logger.Info("Finalizer not found", "ClusterTopology", ct.Name)
		return ctrlcommon.ContinueReconcile()
	}
	logger.Info("Removing finalizer", "ClusterTopology", ct.Name, "finalizerName", constants.FinalizerClusterTopology)
	if err := ctrlutils.RemoveAndPatchFinalizer(ctx, r.client, ct, constants.FinalizerClusterTopology); err != nil {
		return ctrlcommon.ReconcileWithErrors("error removing finalizer", fmt.Errorf("failed to remove finalizer: %s from ClusterTopology: %s: %w", constants.FinalizerClusterTopology, ct.Name, err))
	}
	return ctrlcommon.ContinueReconcile()
}

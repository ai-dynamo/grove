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

package podcliquescalinggroup

import (
	"context"
	"fmt"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// reconcileSpec performs the main reconciliation logic for PodCliqueScalingGroup spec changes
func (r *Reconciler) reconcileSpec(ctx context.Context, logger logr.Logger, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) ctrlcommon.ReconcileStepResult {
	reconcileStepFns := []ctrlcommon.ReconcileStepFn[grovecorev1alpha1.PodCliqueScalingGroup]{
		r.ensureFinalizer,
		r.processRollingUpdate,
		r.syncPodCliqueScalingGroupResources,
		r.updateObservedGeneration,
	}

	for _, fn := range reconcileStepFns {
		if stepResult := fn(ctx, logger, pcsg); ctrlcommon.ShortCircuitReconcileFlow(stepResult) {
			return r.recordIncompleteReconcile(ctx, logger, pcsg, &stepResult)
		}
	}
	logger.Info("Finished spec reconciliation flow")
	return ctrlcommon.ContinueReconcile()
}

// ensureFinalizer adds the PodCliqueScalingGroup finalizer if it's not already present
func (r *Reconciler) ensureFinalizer(ctx context.Context, logger logr.Logger, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) ctrlcommon.ReconcileStepResult {
	if !controllerutil.ContainsFinalizer(pcsg, apiconstants.FinalizerPodCliqueScalingGroup) {
		logger.Info("Adding finalizer", "finalizerName", apiconstants.FinalizerPodCliqueScalingGroup)
		if err := ctrlutils.AddAndPatchFinalizer(ctx, r.client, pcsg, apiconstants.FinalizerPodCliqueScalingGroup); err != nil {
			logger.Error(err, "failed to add finalizer")
			return ctrlcommon.ReconcileWithErrors("error adding finalizer", fmt.Errorf("failed to add finalizer: %s to PodCliqueScalingGroup: %v: %w", apiconstants.FinalizerPodCliqueScalingGroup, client.ObjectKeyFromObject(pcsg), err))
		}
	}
	return ctrlcommon.ContinueReconcile()
}

// processRollingUpdate handles rolling update initialization for PodCliqueScalingGroups when the parent PodCliqueSet is updating
func (r *Reconciler) processRollingUpdate(ctx context.Context, logger logr.Logger, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) ctrlcommon.ReconcileStepResult {
	pcsgObjectKey := client.ObjectKeyFromObject(pcsg)
	pcs, err := componentutils.GetPodCliqueSet(ctx, r.client, pcsg.ObjectMeta)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors(fmt.Sprintf("could not get owner PodCliqueSet for PodCliqueScalingGroup: %v", pcsgObjectKey), err)
	}
	if pcs.Status.RollingUpdateProgress == nil || pcs.Status.RollingUpdateProgress.CurrentlyUpdating == nil {
		// No update has yet been triggered for the PodCliqueSet. Nothing to do here.
		return ctrlcommon.ContinueReconcile()
	}
	pcsReplicaInUpdating := pcs.Status.RollingUpdateProgress.CurrentlyUpdating.ReplicaIndex
	pcsReplicaIndexStr, ok := pcsg.Labels[apicommon.LabelPodCliqueSetReplicaIndex]
	if !ok {
		logger.Info("PodCliqueSet is currently under rolling update. Cannot process pending updates for this PodCliqueScalingGroup as no PodCliqueSet index label is found")
		return ctrlcommon.ContinueReconcile()
	}
	if pcsReplicaIndexStr != strconv.Itoa(int(pcsReplicaInUpdating)) {
		logger.Info("PodCliqueSet is currently under rolling update. Skipping processing pending updates for this PodCliqueScalingGroup as it does not belong to the PodCliqueSet Index in update", "currentlyUpdatingPCSIndex", pcsReplicaInUpdating, "pcsIndexForPCSG", pcsReplicaIndexStr)
		return ctrlcommon.ContinueReconcile()
	}

	// Trigger processing of pending updates for this PCSG. Check if all pending updates for this PCSG and for the PCS CurrentGenerationHash
	// has already been completed or are already in-progress. If that is true, then there is nothing more to do.
	// If the rolling update is in-progress for a different PCS CurrentGenerationHash, or it has not even been started, then
	// reset the rolling update progress so that it can be restarted.
	if shouldResetOrTriggerRollingUpdate(pcs, pcsg) {
		pcsg.Status.UpdatedReplicas = 0
		pcsg.Status.RollingUpdateProgress = &grovecorev1alpha1.PodCliqueScalingGroupRollingUpdateProgress{
			UpdateStartedAt:            metav1.Now(),
			PodCliqueSetGenerationHash: *pcs.Status.CurrentGenerationHash,
		}
		if err = r.client.Status().Update(ctx, pcsg); err != nil {
			logger.Error(err, "could not update PodCliqueScalingGroup.Status.RollingUpdateProgress")
			return ctrlcommon.ReconcileWithErrors(fmt.Sprintf("could not update PodCliqueScalingGroup.Status.RollingUpdateProgress:  %v", pcsgObjectKey), err)
		}
	}
	return ctrlcommon.ContinueReconcile()
}

// shouldResetOrTriggerRollingUpdate determines if a rolling update should be initiated based on generation hash changes
func shouldResetOrTriggerRollingUpdate(pcs *grovecorev1alpha1.PodCliqueSet, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) bool {
	// If processing of rolling update of PCSG for PCS CurrentGenerationHash is either completed or in-progress,
	// there is no need to reset or trigger another rolling update of this PCSG for the same PCS CurrentGenerationHash.
	if pcsg.Status.RollingUpdateProgress != nil && pcsg.Status.RollingUpdateProgress.PodCliqueSetGenerationHash == *pcs.Status.CurrentGenerationHash {
		return false
	}
	return true
}

// syncPodCliqueScalingGroupResources synchronizes all managed resources using registered operators with retry logic
func (r *Reconciler) syncPodCliqueScalingGroupResources(ctx context.Context, logger logr.Logger, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) ctrlcommon.ReconcileStepResult {
	continueReconcileAndRequeueKinds := make([]component.Kind, 0)
	for _, kind := range getOrderedKindsForSync() {
		operator, err := r.operatorRegistry.GetOperator(kind)
		if err != nil {
			return ctrlcommon.ReconcileWithErrors(fmt.Sprintf("error getting operator for kind: %s", kind), err)
		}
		logger.Info("Syncing PodCliqueScalingGroup resource", "kind", kind)
		if err = operator.Sync(ctx, logger, pcsg); err != nil {
			if ctrlutils.ShouldContinueReconcileAndRequeue(err) {
				logger.Info("components has registered a request to requeue post completion of all components syncs", "kind", kind, "message", err.Error())
				continueReconcileAndRequeueKinds = append(continueReconcileAndRequeueKinds, kind)
				continue
			}
			if shouldRequeue := ctrlutils.ShouldRequeueAfter(err); shouldRequeue {
				logger.Info("retrying sync due to components", "kind", kind, "syncRetryInterval", constants.ComponentSyncRetryInterval, "message", err.Error())
				return ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, err.Error())
			}
			logger.Error(err, "failed to sync PodCliqueScalingGroup resources", "kind", kind)
			return ctrlcommon.ReconcileWithErrors("error syncing managed resources", fmt.Errorf("failed to sync %s: %w", kind, err))
		}
	}
	if len(continueReconcileAndRequeueKinds) > 0 {
		return ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, fmt.Sprintf("requeueing sync due to components(s) %v after %s", continueReconcileAndRequeueKinds, constants.ComponentSyncRetryInterval))
	}
	return ctrlcommon.ContinueReconcile()
}

// recordIncompleteReconcile records errors from failed reconciliation steps in the PodCliqueScalingGroup status
func (r *Reconciler) recordIncompleteReconcile(ctx context.Context, logger logr.Logger, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, errStepResult *ctrlcommon.ReconcileStepResult) ctrlcommon.ReconcileStepResult {
	if err := r.reconcileStatusRecorder.RecordErrors(ctx, pcsg, errStepResult); err != nil {
		logger.Error(err, "failed to record incomplete reconcile operation")
		// combine all errors
		allErrs := append(errStepResult.GetErrors(), err)
		return ctrlcommon.ReconcileWithErrors("error recording incomplete reconciliation", allErrs...)
	}
	return *errStepResult
}

// updateObservedGeneration updates the PodCliqueScalingGroup status to reflect the current generation being processed
func (r *Reconciler) updateObservedGeneration(ctx context.Context, logger logr.Logger, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) ctrlcommon.ReconcileStepResult {
	if pcsg.Status.ObservedGeneration != nil && *pcsg.Status.ObservedGeneration == pcsg.Generation {
		return ctrlcommon.ContinueReconcile()
	}

	original := pcsg.DeepCopy()
	pcsg.Status.ObservedGeneration = &pcsg.Generation
	if err := r.client.Status().Patch(ctx, pcsg, client.MergeFrom(original)); err != nil {
		logger.Error(err, "failed to patch status.ObservedGeneration")
		return ctrlcommon.ReconcileWithErrors("error updating observed generation", err)
	}
	logger.Info("patched status.ObservedGeneration", "ObservedGeneration", pcsg.Generation)
	return ctrlcommon.ContinueReconcile()
}

// getOrderedKindsForSync returns the ordered list of resource kinds to synchronize for PodCliqueScalingGroup
func getOrderedKindsForSync() []component.Kind {
	return []component.Kind{
		component.KindPodClique,
	}
}

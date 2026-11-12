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

package podclique

import (
	"context"
	"fmt"
	"strconv"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// reconcileSpec performs the main reconciliation logic for PodClique spec changes
func (r *Reconciler) reconcileSpec(ctx context.Context, logger logr.Logger, pclq *grovecorev1beta1.PodClique) ctrlcommon.ReconcileStepResult {
	log := logger.WithValues("operation", "specReconcile")
	reconcileStepFns := []ctrlcommon.ReconcileStepFn[grovecorev1beta1.PodClique]{
		r.ensureFinalizer,
		r.processUpdate,
		r.syncPCLQResources,
		r.updateObservedGeneration,
	}

	for _, fn := range reconcileStepFns {
		if stepResult := fn(ctx, log, pclq); ctrlcommon.ShortCircuitReconcileFlow(stepResult) {
			return r.recordIncompleteReconcile(ctx, logger, pclq, &stepResult)
		}
	}
	log.V(1).Info("Finished spec reconciliation flow", "PodClique", client.ObjectKeyFromObject(pclq))
	return ctrlcommon.ContinueReconcile()
}

// ensureFinalizer adds the PodClique finalizer if it's not already present
func (r *Reconciler) ensureFinalizer(ctx context.Context, logger logr.Logger, pclq *grovecorev1beta1.PodClique) ctrlcommon.ReconcileStepResult {
	if !controllerutil.ContainsFinalizer(pclq, apiconstants.FinalizerPodClique) {
		logger.Info("Adding finalizer", "PodClique", client.ObjectKeyFromObject(pclq), "finalizerName", apiconstants.FinalizerPodClique)
		if err := ctrlutils.AddAndPatchFinalizer(ctx, r.client, pclq, apiconstants.FinalizerPodClique); err != nil {
			return ctrlcommon.ReconcileWithErrors("error adding finalizer", err)
		}
	}
	return ctrlcommon.ContinueReconcile()
}

const updateProgressCacheSyncRetryInterval = 500 * time.Millisecond

// processUpdate handles update logic for PodClique when the owner PodCliqueSet has changes
func (r *Reconciler) processUpdate(ctx context.Context, logger logr.Logger, pclq *grovecorev1beta1.PodClique) ctrlcommon.ReconcileStepResult {
	pclqObjectKey := client.ObjectKeyFromObject(pclq)
	pcs, err := componentutils.GetPodCliqueSet(ctx, r.client, pclq.ObjectMeta)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors(fmt.Sprintf("could not get owner PodCliqueSet for PodClique: %v", pclqObjectKey), err)
	}

	// Handle OnDelete strategy first
	if !componentutils.IsRollingUpdateStrategy(pcs) {
		if shouldResetOrTriggerUpdate(pclq) {
			if err = r.initOrResetUpdate(ctx, pcs, pclq); err != nil {
				return ctrlcommon.ReconcileWithErrors("could not initialize update for OnDelete", err)
			}
		}
		return ctrlcommon.ContinueReconcile()
	}

	// Wait for the first revision of the PodCliqueSet.
	if pcs.Status.UpdateRevision == "" {
		return ctrlcommon.ContinueReconcile()
	}
	shouldEvaluatePCLQForUpdates, err := shouldCheckPendingUpdatesForPCLQ(logger, pcs, pclq)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("error checking if PodClique should be evaluated for pending updates", err)
	}
	if !shouldEvaluatePCLQForUpdates {
		return ctrlcommon.ContinueReconcile()
	}

	if shouldResetOrTriggerUpdate(pclq) {
		logger.Info("PodClique has a new pod template. Initializing or resetting update for PodClique",
			"podCliqueSetRevision", pclq.Labels[apicommon.LabelPodCliqueSetRevision],
			"podTemplateHash", pclq.Labels[apicommon.LabelPodTemplateHash],
			"currentPodCliqueSetRevision", pclq.Status.CurrentPodCliqueSetRevision,
			"isPCLQRollingUpdateInProgress", componentutils.IsPCLQRollingUpdateInProgress(pclq),
			"isLastPCLQUpdateCompleted", componentutils.IsLastPCLQUpdateCompleted(pclq))
		if err = r.initOrResetUpdate(ctx, pcs, pclq); err != nil {
			return ctrlcommon.ReconcileWithErrors("could not initialize RollingRecreate update", err)
		}
		return ctrlcommon.ReconcileAfter(
			updateProgressCacheSyncRetryInterval,
			fmt.Sprintf("waiting for PodClique update progress to become visible in the informer cache: %v", pclqObjectKey),
		)
	}

	return ctrlcommon.ContinueReconcile()
}

// shouldCheckPendingUpdatesForPCLQ determines if this PodClique should be evaluated for updates based on its owner, and the currently updating PodCliqueSet replica index
func shouldCheckPendingUpdatesForPCLQ(logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, pclq *grovecorev1beta1.PodClique) (bool, error) {
	// PCSG-owned PodCliques are updated by the PCSG controller, which replaces
	// their resources instead of updating their Pods individually.
	if !componentutils.IsStandalonePCLQ(pclq) {
		return false, nil
	}

	// If the PCS is not actively rolling, evaluate standalone PCLQs against
	// their own persisted generation state. This lets status recover when PCLQ
	// UpdateProgress was missed or cleared.
	if pcs.Status.UpdateProgress == nil || pcs.Status.UpdateProgress.UpdateEndedAt != nil {
		return true, nil
	}
	if len(pcs.Status.UpdateProgress.CurrentlyUpdating) == 0 {
		logger.V(1).Info("PodCliqueSet update is active but no replica is currently selected for update. Skipping processing update for this PodClique")
		return false, nil
	}

	// check if this PCLQ belongs to PCS index that is currently getting updated.
	pcsReplicaInUpdating := pcs.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex
	pcsReplicaIndexStr, ok := pclq.Labels[apicommon.LabelPodCliqueSetReplicaIndex]
	if !ok {
		return false, fmt.Errorf("could not determine PodCliqueSet index for this PodClique %v. Required label %s is missing", client.ObjectKeyFromObject(pclq), apicommon.LabelPodCliqueSetReplicaIndex)
	}
	if pcsReplicaIndexStr != strconv.Itoa(int(pcsReplicaInUpdating)) {
		logger.V(1).Info("PodCliqueSet is currently under update. Skipping processing update for this PodClique as it does not belong to the PodCliqueSet Index currently being updated", "currentlyUpdatingPCSIndex", pcsReplicaInUpdating, "pcsIndexForPCLQ", pcsReplicaIndexStr)
		return false, nil
	}

	return true, nil
}

// shouldResetOrTriggerUpdate determines whether an update of the PodClique's Pods should start or restart. The
// owner writes the desired revision and pod template hash together as labels on the PodClique, and the hash
// changes only when the clique's pod template changes, so no hash is recomputed here.
//   - An update in progress restarts when it targets another revision or pod template hash.
//   - Otherwise an update starts when the pod template hash differs from the target of the last update or,
//     without update history, from the hash all Pods last converged to.
//   - A new revision with the same hash also restarts an ended update whose Pods did not converge, as under
//     OnDelete. Otherwise it replaces no Pod, so the status records it without an update.
func shouldResetOrTriggerUpdate(pclq *grovecorev1beta1.PodClique) bool {
	desiredRevision := pclq.Labels[apicommon.LabelPodCliqueSetRevision]
	desiredPodTemplateHash := pclq.Labels[apicommon.LabelPodTemplateHash]
	// Wait until the owner writes the revision. A PodClique written by an older operator has none yet.
	if desiredRevision == "" || desiredPodTemplateHash == "" {
		return false
	}
	progress := pclq.Status.UpdateProgress
	if componentutils.IsPCLQRollingUpdateInProgress(pclq) {
		return progress.PodCliqueSetRevision != desiredRevision || progress.PodTemplateHash != desiredPodTemplateHash
	}
	if progress != nil {
		if progress.PodTemplateHash != desiredPodTemplateHash {
			return true
		}
		// The last update may have ended with Pods still on an older hash, as under OnDelete. A new revision
		// restarts it, so Pods are replaced if the strategy changed since.
		return progress.PodCliqueSetRevision != desiredRevision && !hasConvergedTo(pclq, desiredPodTemplateHash)
	}
	// A PodClique with neither an update history nor a converged pod template hash is being bootstrapped, not
	// updated. Its pods are created directly at the current template, so no update is triggered. The status
	// path records the current revision once those pods exist.
	if pclq.Status.CurrentPodTemplateHash == nil {
		return false
	}
	return *pclq.Status.CurrentPodTemplateHash != desiredPodTemplateHash
}

// hasConvergedTo reports whether every Pod of the PodClique last converged to podTemplateHash.
func hasConvergedTo(pclq *grovecorev1beta1.PodClique, podTemplateHash string) bool {
	return pclq.Status.CurrentPodTemplateHash != nil && *pclq.Status.CurrentPodTemplateHash == podTemplateHash
}

// initOrResetUpdate initializes or resets the update progress status for the PodClique. The target is read from
// the PodClique's own labels.
func (r *Reconciler) initOrResetUpdate(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet, pclq *grovecorev1beta1.PodClique) error {
	// reset and start the update
	patch := client.MergeFrom(pclq.DeepCopy())
	pclq.Status.UpdateProgress = &grovecorev1beta1.PodCliqueUpdateProgress{
		UpdateStartedAt:            metav1.Now(),
		PodCliqueSetGenerationHash: ptr.Deref(pcs.Status.CurrentGenerationHash, ""),
		PodCliqueSetRevision:       pclq.Labels[apicommon.LabelPodCliqueSetRevision],
		PodTemplateHash:            pclq.Labels[apicommon.LabelPodTemplateHash],
	}
	// OnDelete strategy sets UpdateEndedAt too, since we do not know when all the pods will manually be deleted, and gang termination is disabled when an update is in progress
	if !componentutils.IsRollingUpdateStrategy(pcs) {
		pclq.Status.UpdateProgress.UpdateEndedAt = ptr.To(metav1.Now())
	}
	// reset the updated replicas count to 0 so that the update can start afresh.
	pclq.Status.UpdatedReplicas = 0
	if err := r.client.Status().Patch(ctx, pclq, patch); err != nil {
		return fmt.Errorf("failed to update PodClique %s status with update progress: %w", client.ObjectKeyFromObject(pclq), err)
	}
	return nil
}

// syncPCLQResources synchronizes all managed resources for the PodClique using registered operators
func (r *Reconciler) syncPCLQResources(ctx context.Context, logger logr.Logger, pclq *grovecorev1beta1.PodClique) ctrlcommon.ReconcileStepResult {
	state := &component.EmptyState{}
	for _, kind := range getOrderedKindsForSync() {
		operator, err := r.operatorRegistry.GetOperator(kind)
		if err != nil {
			return ctrlcommon.ReconcileWithErrors(fmt.Sprintf("error getting operator for kind: %s", kind), err)
		}
		logger.V(1).Info("Syncing PodClique resources", "kind", kind)
		if _, err = operator.Sync(ctx, logger, pclq, state); err != nil {
			if requeueAfter, shouldRequeue := groveerr.GetRequeueAfter(err); shouldRequeue {
				logger.V(1).Info("Retrying sync due to component", "kind", kind, "requeueAfter", requeueAfter, "message", err.Error())
				return ctrlcommon.ReconcileAfter(requeueAfter, err.Error())
			}
			if groveerr.IsContinueReconcileAndRequeue(err) {
				logger.V(1).Info("Retrying sync due to component", "kind", kind, "requeueAfter", constants.ComponentSyncRetryInterval, "message", err.Error())
				return ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, err.Error())
			}
			logger.Error(err, "failed to sync PodClique resources", "kind", kind)
			return ctrlcommon.ReconcileWithErrors("error syncing managed resources", fmt.Errorf("failed to sync %s: %w", kind, err))
		}
	}
	return ctrlcommon.ContinueReconcile()
}

// updateObservedGeneration updates the PodClique status to reflect the current generation being processed
func (r *Reconciler) updateObservedGeneration(ctx context.Context, logger logr.Logger, pclq *grovecorev1beta1.PodClique) ctrlcommon.ReconcileStepResult {
	if pclq.Status.ObservedGeneration != nil && *pclq.Status.ObservedGeneration == pclq.Generation {
		return ctrlcommon.ContinueReconcile()
	}

	original := pclq.DeepCopy()
	pclq.Status.ObservedGeneration = &pclq.Generation
	if err := r.client.Status().Patch(ctx, pclq, client.MergeFrom(original)); err != nil {
		logger.Error(err, "failed to patch status.ObservedGeneration")
		return ctrlcommon.ReconcileWithErrors("error updating observed generation", err)
	}
	logger.V(1).Info("patched status.ObservedGeneration", "ObservedGeneration", pclq.Generation)
	return ctrlcommon.ContinueReconcile()
}

// recordIncompleteReconcile records errors from failed reconciliation steps in the PodClique status
func (r *Reconciler) recordIncompleteReconcile(ctx context.Context, logger logr.Logger, pclq *grovecorev1beta1.PodClique, errResult *ctrlcommon.ReconcileStepResult) ctrlcommon.ReconcileStepResult {
	if err := r.reconcileStatusRecorder.RecordErrors(ctx, pclq, errResult); err != nil {
		logger.Error(err, "failed to record incomplete reconcile operation")
		// combine all errors
		allErrs := append(errResult.GetErrors(), err)
		return ctrlcommon.ReconcileWithErrors("error recording incomplete reconciliation", allErrs...)
	}
	return *errResult
}

// getOrderedKindsForSync returns the ordered list of resource kinds to synchronize for PodClique.
// ResourceClaims are synced before Pods so that DRA claims exist before pods reference them.
func getOrderedKindsForSync() []component.Kind {
	return []component.Kind{
		component.KindResourceClaim,
		component.KindPod,
	}
}

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

	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/hash"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"
	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// reconcileSpec performs the main reconciliation logic for PodCliqueSet spec changes
func (r *Reconciler) reconcileSpec(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	rLog := logger.WithValues("operation", "spec-reconcile")
	reconcileStepFns := []ctrlcommon.ReconcileStepFn[grovecorev1alpha1.PodCliqueSet]{
		r.ensureFinalizer,
		r.processGenerationHashChange,
		r.syncPodCliqueSetResources,
		r.updateObservedGeneration,
	}

	for _, fn := range reconcileStepFns {
		if stepResult := fn(ctx, rLog, pcs); ctrlcommon.ShortCircuitReconcileFlow(stepResult) {
			return r.recordIncompleteReconcile(ctx, logger, pcs, &stepResult)
		}
	}
	logger.Info("Finished spec reconciliation flow", "PodCliqueSet", client.ObjectKeyFromObject(pcs))
	return ctrlcommon.ContinueReconcile()
}

// ensureFinalizer adds the PodCliqueSet finalizer if not already present.
func (r *Reconciler) ensureFinalizer(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	if !controllerutil.ContainsFinalizer(pcs, apiconstants.FinalizerPodCliqueSet) {
		logger.Info("Adding finalizer", "finalizerName", apiconstants.FinalizerPodCliqueSet)
		if err := ctrlutils.AddAndPatchFinalizer(ctx, r.client, pcs, apiconstants.FinalizerPodCliqueSet); err != nil {
			return ctrlcommon.ReconcileWithErrors("error adding finalizer", fmt.Errorf("failed to add finalizer: %s to PodCliqueSet: %v: %w", apiconstants.FinalizerPodCliqueSet, client.ObjectKeyFromObject(pcs), err))
		}
	}
	return ctrlcommon.ContinueReconcile()
}

// processGenerationHashChange computes the generation hash given a PodCliqueSet resource and if the generation has
// changed from the previously persisted pcs.status.generationHash then it resets the pcs.status.rollingUpdateProgress
func (r *Reconciler) processGenerationHashChange(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	pcsObjectKey := client.ObjectKeyFromObject(pcs)
	pcsObjectName := cache.NamespacedNameAsObjectName(pcsObjectKey).String()

	// if the generationHash is not reflected correctly yet, requeue. Allow the informer cache to catch-up.
	if !r.isGenerationHashExpectationSatisfied(pcsObjectName, pcs.Status.CurrentGenerationHash) {
		return ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, fmt.Sprintf("CurrentGenerationHash is not up-to-date for PodCliqueSet: %v", pcsObjectKey))
	}
	r.pcsGenerationHashExpectations.Delete(pcsObjectName)

	newGenerationHash, err := computeGenerationHash(pcs)
	if err != nil {
		logger.Error(err, "failed to compute generation hash for PodCliqueSet", "PodCliqueSet", pcsObjectKey)
		return ctrlcommon.ReconcileWithErrors("error computing generation hash", err)
	}
	if pcs.Status.CurrentGenerationHash == nil {
		// update the generation hash and continue reconciliation. No rolling update is required.
		if err = r.setGenerationHash(ctx, pcs, pcsObjectName, newGenerationHash); err != nil {
			logger.Error(err, "failed to set generation hash on PCS", "newGenerationHash", newGenerationHash)
			return ctrlcommon.ReconcileWithErrors("error updating generation hash", err)
		}
		return ctrlcommon.ContinueReconcile()
	}

	if newGenerationHash != *pcs.Status.CurrentGenerationHash {
		logger.Info("Generation hash has changed, triggering rolling update", "PodCliqueSet", pcsObjectKey, "oldGenerationHash", *pcs.Status.CurrentGenerationHash, "newGenerationHash", newGenerationHash)
		// trigger rolling update by setting or overriding pcs.Status.RollingUpdateProgress.
		if err = r.initRollingUpdateProgress(ctx, pcs, pcsObjectName, newGenerationHash); err != nil {
			return ctrlcommon.ReconcileWithErrors(fmt.Sprintf("could not triggering rolling update for PCS: %v", pcsObjectKey), err)
		}
	}

	return ctrlcommon.ContinueReconcile()
}

// isGenerationHashExpectationSatisfied checks if the current generation hash matches expectations.
func (r *Reconciler) isGenerationHashExpectationSatisfied(pcsObjectName string, pcsGenerationHash *string) bool {
	expectedGenerationHash, ok := r.pcsGenerationHashExpectations.Load(pcsObjectName)
	return !ok || (pcsGenerationHash != nil && expectedGenerationHash.(string) == *pcsGenerationHash)
}

// computeGenerationHash calculates a hash of the PodCliqueSet pod template specifications.
func computeGenerationHash(pcs *grovecorev1alpha1.PodCliqueSet) (string, error) {
	podTemplateSpecs := lo.Map(pcs.Spec.Template.Cliques, func(pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, _ int) *corev1.PodTemplateSpec {
		podTemplateSpec := &corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels:      pclqTemplateSpec.Labels,
				Annotations: pclqTemplateSpec.Annotations,
			},
			Spec: pclqTemplateSpec.Spec.PodSpec,
		}
		podTemplateSpec.Spec.PriorityClassName = pcs.Spec.Template.PriorityClassName
		return podTemplateSpec
	})
	pcsHash, err := hash.Compute(podTemplateSpecs...)
	if err != nil {
		return "", fmt.Errorf("error computing generation hash for PodCliqueSet: %v: %w", client.ObjectKeyFromObject(pcs), err)
	}
	return pcsHash, nil
}

// setGenerationHash updates the PodCliqueSet status with the new generation hash and stores the expectation.
func (r *Reconciler) setGenerationHash(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsObjectName, generationHash string) error {
	pcs.Status.CurrentGenerationHash = &generationHash
	if err := r.client.Status().Update(ctx, pcs); err != nil {
		return fmt.Errorf("could not update CurrentGenerationHash for PodCliqueSet: %v: %w", client.ObjectKeyFromObject(pcs), err)
	}
	r.pcsGenerationHashExpectations.Store(pcsObjectName, generationHash)
	return nil
}

// initRollingUpdateProgress initializes a new rolling update.
// Sets the UpdateStartedAt to now, reset UpdatedReplicas to 0, and update the CurrentGenerationHash to the new hash to trigger the rolling update process.
func (r *Reconciler) initRollingUpdateProgress(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsObjectName, newGenerationHash string) error {
	pcs.Status.RollingUpdateProgress = &grovecorev1alpha1.PodCliqueSetRollingUpdateProgress{
		UpdateStartedAt: metav1.Now(),
	}
	pcs.Status.UpdatedReplicas = 0
	pcs.Status.CurrentGenerationHash = &newGenerationHash
	if err := r.client.Status().Update(ctx, pcs); err != nil {
		return fmt.Errorf("could not set RollingUpdateProgress for PodCliqueSet: %v: %w", client.ObjectKeyFromObject(pcs), err)
	}
	r.pcsGenerationHashExpectations.Store(pcsObjectName, newGenerationHash)
	return nil
}

// syncPodCliqueSetResources synchronizes all managed child resources in order.
func (r *Reconciler) syncPodCliqueSetResources(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	continueReconcileAndRequeueKinds := make([]component.Kind, 0)
	for _, kind := range getOrderedKindsForSync() {
		operator, err := r.operatorRegistry.GetOperator(kind)
		if err != nil {
			// Skip operators that aren't registered (e.g., ComputeDomain when MNNVL is disabled)
			logger.V(1).Info("Skipping unregistered operator", "kind", kind)
			continue
		}
		logger.Info("Syncing PodCliqueSet resource", "kind", kind)
		if err = operator.Sync(ctx, logger, pcs); err != nil {
			if ctrlutils.ShouldContinueReconcileAndRequeue(err) {
				logger.Info("components has registered a request to requeue post completion of all components syncs", "kind", kind, "message", err.Error())
				continueReconcileAndRequeueKinds = append(continueReconcileAndRequeueKinds, kind)
				continue
			}
			if shouldRequeue := ctrlutils.ShouldRequeueAfter(err); shouldRequeue {
				logger.Info("retrying sync due to components", "kind", kind, "syncRetryInterval", constants.ComponentSyncRetryInterval, "message", err.Error())
				return ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, err.Error())
			}
			logger.Error(err, "failed to sync PodCliqueSet resources", "kind", kind)
			return ctrlcommon.ReconcileWithErrors("error syncing managed resources", fmt.Errorf("failed to sync %s: %w", kind, err))
		}
	}
	if len(continueReconcileAndRequeueKinds) > 0 {
		return ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, fmt.Sprintf("requeueing sync due to components(s) %v after %s", continueReconcileAndRequeueKinds, constants.ComponentSyncRetryInterval))
	}
	return ctrlcommon.ContinueReconcile()
}

// updateObservedGeneration updates the status to reflect the current observed generation.
func (r *Reconciler) updateObservedGeneration(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	if pcs.Status.ObservedGeneration != nil && *pcs.Status.ObservedGeneration == pcs.Generation {
		return ctrlcommon.ContinueReconcile()
	}

	original := pcs.DeepCopy()
	pcs.Status.ObservedGeneration = &pcs.Generation
	if err := r.client.Status().Patch(ctx, pcs, client.MergeFrom(original)); err != nil {
		logger.Error(err, "failed to patch status.ObservedGeneration")
		return ctrlcommon.ReconcileWithErrors("error updating observed generation", err)
	}
	logger.Info("patched status.ObservedGeneration", "ObservedGeneration", pcs.Generation)
	return ctrlcommon.ContinueReconcile()
}

// recordIncompleteReconcile records errors that occurred during reconciliation.
func (r *Reconciler) recordIncompleteReconcile(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, errResult *ctrlcommon.ReconcileStepResult) ctrlcommon.ReconcileStepResult {
	if err := r.reconcileStatusRecorder.RecordErrors(ctx, pcs, errResult); err != nil {
		logger.Error(err, "failed to record incomplete reconcile operation")
		// combine all errors
		allErrs := append(errResult.GetErrors(), err)
		return ctrlcommon.ReconcileWithErrors("error recording incomplete reconciliation", allErrs...)
	}
	return *errResult
}

// getOrderedKindsForSync returns the ordered list of component kinds to synchronize.
func getOrderedKindsForSync() []component.Kind {
	return []component.Kind{
		component.KindServiceAccount,
		component.KindRole,
		component.KindRoleBinding,
		component.KindServiceAccountTokenSecret,
		component.KindHeadlessService,
		component.KindHorizontalPodAutoscaler,
		component.KindPodCliqueSetReplica,
		component.KindComputeDomain,
		component.KindPodClique,
		component.KindPodCliqueScalingGroup,
		component.KindPodGang,
	}
}

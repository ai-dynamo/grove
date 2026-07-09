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
	"sync"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
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
// changed from the previously persisted pcs.status.generationHash then it resets the pcs.status.updateProgress
func (r *Reconciler) processGenerationHashChange(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	pcsObjectKey := client.ObjectKeyFromObject(pcs)
	pcsObjectName := cache.NamespacedNameAsObjectName(pcsObjectKey).String()

	// if the generationHash is not reflected correctly yet, requeue. Allow the informer cache to catch-up.
	if !r.isGenerationHashExpectationSatisfied(pcsObjectName, pcs.Status.CurrentGenerationHash) {
		return ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, fmt.Sprintf("CurrentGenerationHash is not up-to-date for PodCliqueSet: %v", pcsObjectKey))
	}
	r.pcsGenerationHashExpectations.Delete(pcsObjectName)

	newGenerationHash := computeGenerationHash(pcs)
	if pcs.Status.CurrentGenerationHash == nil {
		// update the generation hash and continue reconciliation. No rolling update is required.
		if err := r.setGenerationHashAndUpdateStatus(ctx, pcs, pcsObjectName, newGenerationHash); err != nil {
			logger.Error(err, "failed to set generation hash on PCS", "newGenerationHash", newGenerationHash)
			return ctrlcommon.ReconcileWithErrors("error updating generation hash", err)
		}
		return ctrlcommon.ContinueReconcile()
	}

	if newGenerationHash != *pcs.Status.CurrentGenerationHash {
		// trigger rolling update by setting or overriding pcs.Status.UpdateProgress.
		if err := r.initUpdateProgress(ctx, pcs, pcsObjectName, newGenerationHash); err != nil {
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
func computeGenerationHash(pcs *grovecorev1alpha1.PodCliqueSet) string {
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
	return k8sutils.ComputeHash(podTemplateSpecs...)
}

// setGenerationHashAndUpdateStatus updates the PodCliqueSet status with the new generation hash, stores the expectation, and updates the status subresource.
func (r *Reconciler) setGenerationHashAndUpdateStatus(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsObjectName, generationHash string) error {
	pcs.Status.CurrentGenerationHash = &generationHash
	if err := r.client.Status().Update(ctx, pcs); err != nil {
		return fmt.Errorf("could not update CurrentGenerationHash for PodCliqueSet: %v: %w", client.ObjectKeyFromObject(pcs), err)
	}
	r.pcsGenerationHashExpectations.Store(pcsObjectName, generationHash)
	return nil
}

// initUpdateProgress initializes a new update by resetting progress tracking for the active strategy.
//
// For the Coherent strategy it also captures the set of components (standalone PodCliques and
// PodCliqueScalingGroups) that are out-of-date relative to the new PCS spec at this exact moment.
// This snapshot drives the MVU template for the lifetime of the update — recomputing the set
// from live PCLQ hashes on every reconcile would shrink it as pods roll over and break the
// MVU invariants.
func (r *Reconciler) initUpdateProgress(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsObjectName, newGenerationHash string) error {
	if componentutils.IsCoherentStrategy(pcs) {
		updatedStandalonePCLQs, updatedPCSGs, err := r.findUpdatedStandalonePCLQsAndPCSGs(ctx, pcs)
		if err != nil {
			return fmt.Errorf("could not detect out-of-date components for PodCliqueSet %v: %w", client.ObjectKeyFromObject(pcs), err)
		}
		pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
			UpdateStartedAt:               metav1.Now(),
			UpdatedStandalonePodCliques:   updatedStandalonePCLQs,
			UpdatedPodCliqueScalingGroups: updatedPCSGs,
		}
	} else {
		pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
			UpdateStartedAt: metav1.Now(),
		}
		// OnDelete strategy sets UpdateEndedAt too, since we do not know when all the pods will manually be deleted, and gang termination is disabled when an update is in progress
		if pcs.Spec.UpdateStrategy != nil && pcs.Spec.UpdateStrategy.Type == grovecorev1alpha1.OnDeleteStrategy {
			pcs.Status.UpdateProgress.UpdateEndedAt = ptr.To(metav1.Now())
		}
	}
	pcs.Status.UpdatedReplicas = 0
	if err := r.setGenerationHashAndUpdateStatus(ctx, pcs, pcsObjectName, newGenerationHash); err != nil {
		return fmt.Errorf("could not set UpdateProgress for PodCliqueSet: %v: %w", client.ObjectKeyFromObject(pcs), err)
	}
	return nil
}

// findUpdatedStandalonePCLQsAndPCSGs lists live PodCliques for this PCS and returns the names of
// components (standalone PodCliques and PodCliqueScalingGroup configs) that have at least one live
// PodClique whose CurrentPodTemplateHash does not match the per-clique hash of the new PCS spec.
// PCLQs at one clique can be in mixed hash states (e.g. partway through a coherent update), so the
// "any live PCLQ mismatches" rule must scan every PCLQ at the clique, not just sample one. Returned
// slices are sorted ascending for stable status persistence.
func (r *Reconciler) findUpdatedStandalonePCLQsAndPCSGs(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (standalone, pcsgs []string, err error) {
	pclqs, err := componentutils.GetPCLQsMatchingLabels(ctx, r.client, pcs.Namespace, apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name))
	if err != nil {
		return nil, nil, err
	}
	pclqsByCliqueName, err := indexPCLQsByCliqueName(pclqs)
	if err != nil {
		return nil, nil, err
	}

	standaloneSet := sets.New[string]()
	pcsgSet := sets.New[string]()

	for _, cliqueTemplate := range pcs.Spec.Template.Cliques {
		newHash := componentutils.ComputePCLQPodTemplateHash(cliqueTemplate, pcs.Spec.Template.PriorityClassName)
		anyOutOfDate := lo.ContainsBy(pclqsByCliqueName[cliqueTemplate.Name], func(pclq grovecorev1alpha1.PodClique) bool {
			return pclq.Status.CurrentPodTemplateHash == nil || *pclq.Status.CurrentPodTemplateHash != newHash
		})
		if !anyOutOfDate {
			continue
		}
		// Standalone cliques aren't in any PCSG config; PCSG-owned cliques attribute to the owning PCSG.
		if owningPCSG := componentutils.FindScalingGroupConfigForClique(pcs.Spec.Template.PodCliqueScalingGroupConfigs, cliqueTemplate.Name); owningPCSG != nil {
			pcsgSet.Insert(owningPCSG.Name)
		} else {
			standaloneSet.Insert(cliqueTemplate.Name)
		}
	}

	standalone = sets.List(standaloneSet)
	pcsgs = sets.List(pcsgSet)
	return standalone, pcsgs, nil
}

// indexPCLQsByCliqueName groups the supplied PodCliques by unqualified clique name.
func indexPCLQsByCliqueName(pclqs []grovecorev1alpha1.PodClique) (map[string][]grovecorev1alpha1.PodClique, error) {
	out := make(map[string][]grovecorev1alpha1.PodClique)
	for _, pclq := range pclqs {
		cliqueName, err := utils.GetPodCliqueNameFromPodCliqueFQN(pclq.ObjectMeta)
		if err != nil {
			return nil, err
		}
		out[cliqueName] = append(out[cliqueName], pclq)
	}
	return out, nil
}

// syncPodCliqueSetResources synchronizes all managed child resources. Components are
// sync'd in dependency-ordered groups; within each group sync runs concurrently.
func (r *Reconciler) syncPodCliqueSetResources(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	continueReconcileAndRequeueKinds := make([]component.Kind, 0)
	for groupIdx, group := range getKindSyncGroups() {
		result, requeuedKinds := r.syncKindGroup(ctx, logger, pcs, group, groupIdx)
		continueReconcileAndRequeueKinds = append(continueReconcileAndRequeueKinds, requeuedKinds...)
		if result != nil {
			return *result
		}
	}
	if len(continueReconcileAndRequeueKinds) > 0 {
		return ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, fmt.Sprintf("requeueing sync due to components(s) %v after %s", continueReconcileAndRequeueKinds, constants.ComponentSyncRetryInterval))
	}
	return ctrlcommon.ContinueReconcile()
}

// syncKindGroup runs the Sync operation for each kind in the group concurrently.
// Returns a non-nil ReconcileStepResult if reconciliation should stop for this group,
// plus the list of kinds that returned "continue and requeue" errors.
func (r *Reconciler) syncKindGroup(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, group []component.Kind, groupIdx int) (*ctrlcommon.ReconcileStepResult, []component.Kind) {
	var (
		mu              sync.Mutex
		requeuedKinds   []component.Kind
		requeueAfterMsg string
		hasRequeueAfter bool
		errsByKind      []error
	)
	tasks := make([]utils.Task, 0, len(group))
	for _, kind := range group {
		operator, err := r.operatorRegistry.GetOperator(kind)
		if err != nil {
			// MNNVL-only components (ComputeDomain) aren't registered when the feature is
			// off. Not an error; just nothing to sync.
			logger.V(1).Info("Skipping unregistered operator", "kind", kind)
			continue
		}
		tasks = append(tasks, utils.Task{
			Name: fmt.Sprintf("SyncKind-%s", kind),
			Fn: func(ctx context.Context) error {
				logger.Info("Syncing PodCliqueSet resource", "kind", kind, "group", groupIdx)
				err := operator.Sync(ctx, logger, pcs)

				// One lock + defer covers all branches below; requeuedKinds /
				// hasRequeueAfter / errsByKind are read by the aggregator after
				// RunConcurrently joins, so writes must be serialised here.
				mu.Lock()
				defer mu.Unlock()

				if err == nil {
					return nil
				}
				if ctrlutils.ShouldContinueReconcileAndRequeue(err) {
					// Component is asking the parent to come back later — that signal is
					// not a sync failure, so we record the kind and return nil. The
					// caller bubbles requeuedKinds up and schedules one follow-up
					// reconcile after the whole sync sweep completes.
					requeuedKinds = append(requeuedKinds, kind)
					logger.Info("component requested post-sync requeue", "kind", kind, "message", err.Error())
					return nil
				}
				if ctrlutils.ShouldRequeueAfter(err) {
					// Timed retry — caller will return ReconcileAfter for the whole
					// group. First setter wins; runs are deterministic enough that the
					// chosen message is fine.
					hasRequeueAfter = true
					requeueAfterMsg = err.Error()
					return err
				}
				// Real failure: surface it as a reconcile error.
				logger.Error(err, "failed to sync PodCliqueSet resource", "kind", kind)
				errsByKind = append(errsByKind, fmt.Errorf("failed to sync %s: %w", kind, err))
				return err
			},
		})
	}
	if len(tasks) == 0 {
		return nil, nil
	}
	_ = utils.RunConcurrently(ctx, logger, tasks)

	if hasRequeueAfter {
		result := ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, requeueAfterMsg)
		return &result, requeuedKinds
	}
	if len(errsByKind) > 0 {
		result := ctrlcommon.ReconcileWithErrors("error syncing managed resources", errsByKind...)
		return &result, requeuedKinds
	}
	return nil, requeuedKinds
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

// getKindSyncGroups returns the component kinds grouped by dependency. Kinds within the
// same group have no dependencies on one another and can be sync'd concurrently; groups
// are processed in order to respect cross-group dependencies.
func getKindSyncGroups() [][]component.Kind {
	return [][]component.Kind{
		// G0: RBAC + static per-PCS infra (Service, HPA targets by name so no ordering
		// vs PodClique/PCSG needed, ComputeDomain/ResourceClaim are independent add-ons).
		// PodGangMap is computed here — it has no dependency on any other component, and
		// must be ready before PodCliqueSetReplica (G1) and PodClique/PCSG/PodGang (G2) read it.
		{
			component.KindServiceAccount,
			component.KindRole,
			component.KindRoleBinding,
			component.KindServiceAccountTokenSecret,
			component.KindHeadlessService,
			component.KindHorizontalPodAutoscaler,
			component.KindComputeDomain,
			component.KindResourceClaim,
			component.KindPodGangMap,
		},
		// G1: PodCliqueSetReplica orchestrates replica deletion and rolling updates; it reads
		// PodGangMap (for Coherent updates) so must run after G0.
		{
			component.KindPodCliqueSetReplica,
		},
		// G2: PodClique, PCSG, and PodGang run concurrently. PodGang reads existing PodClique/Pod
		// state from the API server (persisted from prior reconciles) — no same-reconcile ordering
		// dependency between them. PCSG creates its own PodCliques via a separate reconciler.
		{
			component.KindPodClique,
			component.KindPodCliqueScalingGroup,
			component.KindPodGang,
		},
	}
}

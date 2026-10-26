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

package podcliqueset

import (
	"context"
	"errors"
	"fmt"

	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/sync"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// reconcileSpec performs the main reconciliation logic for PodCliqueSet spec changes
func (r *Reconciler) reconcileSpec(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, state *pcssync.State) ctrlcommon.ReconcileStepResult {
	rLog := logger.WithValues("operation", "spec-reconcile")
	syncResources := func(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
		return r.syncPodCliqueSetResources(ctx, logger, pcs, state)
	}
	reconcileStepFns := []ctrlcommon.ReconcileStepFn[grovecorev1alpha1.PodCliqueSet]{
		r.ensureFinalizer,
		r.ensureCurrentControllerRevision,
		r.processGenerationHashChange,
		syncResources,
		r.updateObservedGeneration,
	}

	for _, fn := range reconcileStepFns {
		if stepResult := fn(ctx, rLog, pcs); ctrlcommon.ShortCircuitReconcileFlow(stepResult) {
			return r.recordIncompleteReconcile(ctx, logger, pcs, &stepResult)
		}
	}
	logger.V(1).Info("Finished spec reconciliation flow", "PodCliqueSet", client.ObjectKeyFromObject(pcs))
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
	pcsGenHashKey := pcsGenerationHashKey(pcs)

	// if the generationHash is not reflected correctly yet, requeue. Allow the informer cache to catch-up.
	if !r.isGenerationHashExpectationSatisfied(pcsGenHashKey, pcs.Status.CurrentGenerationHash) {
		return ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, fmt.Sprintf("CurrentGenerationHash is not up-to-date for PodCliqueSet: %v", pcsObjectKey))
	}
	r.pcsGenerationHashExpectations.Delete(pcsGenHashKey)

	newGenerationHash := computeGenerationHash(pcs)
	if pcs.Status.CurrentGenerationHash == nil {
		// update the generation hash and continue reconciliation. No rolling update is required.
		if err := r.setGenerationHashAndUpdateStatus(ctx, pcs, pcsGenHashKey, newGenerationHash); err != nil {
			logger.Error(err, "failed to set generation hash on PCS", "newGenerationHash", newGenerationHash)
			return ctrlcommon.ReconcileWithErrors("error updating generation hash", err)
		}
		return ctrlcommon.ContinueReconcile()
	}

	if newGenerationHash != *pcs.Status.CurrentGenerationHash {
		// trigger rolling update by setting or overriding pcs.Status.UpdateProgress.
		if err := r.initUpdateProgress(ctx, pcs, pcsGenHashKey, newGenerationHash); err != nil {
			return ctrlcommon.ReconcileWithErrors(fmt.Sprintf("could not triggering rolling update for PCS: %v", pcsObjectKey), err)
		}
	}

	return ctrlcommon.ContinueReconcile()
}

// pcsGenerationHashKey returns the key for the in-memory generation-hash expectation of a PCS.
// The key scheme is <pcs-namespace>/<pcs-name>/<pcs-UID>. Suffixing a UID ensures that a stale
// remnant expectation entry for the same <pcs-namespace>/<pcs-name> does not block reconciliation of
// a new PCS (with a different UID).
// See https://github.com/ai-dynamo/grove/issues/782 for context.
func pcsGenerationHashKey(pcs *grovecorev1alpha1.PodCliqueSet) string {
	return fmt.Sprintf("%s/%s", client.ObjectKeyFromObject(pcs), pcs.UID)
}

// isGenerationHashExpectationSatisfied checks if the current generation hash matches expectations.
func (r *Reconciler) isGenerationHashExpectationSatisfied(pcsGenHashKey string, pcsGenerationHash *string) bool {
	expectedGenerationHash, ok := r.pcsGenerationHashExpectations.Load(pcsGenHashKey)
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
func (r *Reconciler) setGenerationHashAndUpdateStatus(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsGenHashKey, newGenerationHash string) error {
	pcs.Status.CurrentGenerationHash = &newGenerationHash
	if err := r.client.Status().Update(ctx, pcs); err != nil {
		return fmt.Errorf("could not update CurrentGenerationHash for PodCliqueSet: %v: %w", client.ObjectKeyFromObject(pcs), err)
	}
	r.pcsGenerationHashExpectations.Store(pcsGenHashKey, newGenerationHash)
	return nil
}

// initUpdateProgress initializes a new rolling update by resetting progress tracking.
func (r *Reconciler) initUpdateProgress(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsGenHashKey, newGenerationHash string) error {
	if pcs.Status.CurrentRevision == "" {
		return fmt.Errorf("PodCliqueSet %v has no current ControllerRevision", client.ObjectKeyFromObject(pcs))
	}
	updateProgress := &grovecorev1alpha1.PodCliqueSetUpdateProgress{
		UpdateStartedAt:  metav1.Now(),
		ReplicaRevisions: replicaRevisionBindingsForUpdate(pcs),
	}
	// OnDelete strategy sets UpdateEndedAt too, since we do not know when all the pods will manually be deleted, and gang termination is disabled when an update is in progress
	if pcs.Spec.UpdateStrategy != nil && pcs.Spec.UpdateStrategy.Type == grovecorev1alpha1.OnDeleteStrategy {
		updateProgress.UpdateEndedAt = ptr.To(metav1.Now())
	}
	// The Coherent strategy rolls only the components whose pod template changed. Capture that scope now,
	// while Status.CurrentGenerationHash still holds the previous hash, and preserve it for the update.
	if componentutils.IsCoherentStrategy(pcs) {
		scope, err := r.computeCoherentUpdateScope(ctx, pcs)
		if err != nil {
			return fmt.Errorf("could not compute coherent update scope for PodCliqueSet: %v: %w", client.ObjectKeyFromObject(pcs), err)
		}
		updateProgress.InScopeStandalonePodCliques = sets.List(scope.standalonePCLQs)
		updateProgress.InScopePodCliqueScalingGroups = sets.List(scope.podCliqueScalingGroups)
	}
	pcs.Status.UpdateProgress = updateProgress
	pcs.Status.UpdatedReplicas = 0
	pcs.Status.CurrentGenerationHash = &newGenerationHash
	if err := r.setGenerationHashAndUpdateStatus(ctx, pcs, pcsGenHashKey, newGenerationHash); err != nil {
		return fmt.Errorf("could not set UpdateProgress for PodCliqueSet: %v: %w", client.ObjectKeyFromObject(pcs), err)
	}
	return nil
}

// replicaRevisionBindingsForUpdate returns one durable revision binding per desired replica. Existing
// valid bindings are preserved across a superseding update; replicas without a binding start from the
// last fully converged CurrentRevision. Iterating replica indices keeps status output deterministic.
func replicaRevisionBindingsForUpdate(pcs *grovecorev1alpha1.PodCliqueSet) []grovecorev1alpha1.PodCliqueSetReplicaRevision {
	existing := make(map[int32]string)
	if pcs.Status.UpdateProgress != nil {
		for _, binding := range pcs.Status.UpdateProgress.ReplicaRevisions {
			if binding.ReplicaIndex >= 0 && binding.ReplicaIndex < pcs.Spec.Replicas && binding.Revision != "" {
				existing[binding.ReplicaIndex] = binding.Revision
			}
		}
	}

	bindings := make([]grovecorev1alpha1.PodCliqueSetReplicaRevision, 0, pcs.Spec.Replicas)
	for replicaIndex := range pcs.Spec.Replicas {
		revisionName := pcs.Status.CurrentRevision
		if existingRevision, ok := existing[replicaIndex]; ok {
			revisionName = existingRevision
		}
		bindings = append(bindings, grovecorev1alpha1.PodCliqueSetReplicaRevision{
			ReplicaIndex: replicaIndex,
			Revision:     revisionName,
		})
	}
	return bindings
}

// syncPodCliqueSetResources synchronizes all managed child resources. Components are
// sync'd in dependency-ordered groups; within each group sync runs concurrently.
func (r *Reconciler) syncPodCliqueSetResources(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, state *pcssync.State) ctrlcommon.ReconcileStepResult {
	syncResults := make(map[component.Kind]pcssync.Result)
	continueReconcileAndRequeueKinds := make([]component.Kind, 0)
	for groupIdx, group := range getKindSyncGroups() {
		result, groupResults, requeuedKinds := r.syncKindGroup(ctx, logger, pcs, state, group, groupIdx)
		for _, groupResult := range groupResults {
			syncResults[groupResult.kind] = groupResult.result
		}
		continueReconcileAndRequeueKinds = append(continueReconcileAndRequeueKinds, requeuedKinds...)
		if result != nil {
			return *result
		}
	}
	if len(continueReconcileAndRequeueKinds) > 0 {
		return ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, fmt.Sprintf("requeueing sync due to components(s) %v after %s", continueReconcileAndRequeueKinds, constants.ComponentSyncRetryInterval))
	}

	scalingProgressChanged, err := r.removeCompletedScaleInPCSReplicaIndices(ctx, logger, pcs, syncResults)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("error updating PodCliqueSet scale-in progress", err)
	}
	if scalingProgressChanged {
		return ctrlcommon.ReconcileAfter(scalingProgressCacheSyncRetryInterval, fmt.Sprintf("waiting for updated scale-in progress to become visible in the informer cache for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)))
	}
	return ctrlcommon.ContinueReconcile()
}

// removeCompletedScaleInPCSReplicaIndices removes replica indices whose resources have been cleaned up from scaling progress.
func (r *Reconciler) removeCompletedScaleInPCSReplicaIndices(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, syncResults map[component.Kind]pcssync.Result) (bool, error) {
	if pcs.Status.ScalingProgress == nil || len(pcs.Status.ScalingProgress.ScaleInReplicaIndices) == 0 {
		return false, nil
	}

	currentScaleInPCSReplicaIndices := sets.New(pcs.Status.ScalingProgress.ScaleInReplicaIndices...)
	awaitingResourceCleanup := scaleInPCSReplicaIndicesAwaitingResourceCleanup(syncResults)
	completedPCSReplicaIndices := currentScaleInPCSReplicaIndices.Difference(awaitingResourceCleanup)
	if completedPCSReplicaIndices.Len() == 0 {
		return false, nil
	}

	updatedProgress := pcs.Status.ScalingProgress.DeepCopy()
	updatedScaleInPCSReplicaIndices := currentScaleInPCSReplicaIndices.Difference(completedPCSReplicaIndices)
	updatedProgress.ScaleInReplicaIndices = sets.List(updatedScaleInPCSReplicaIndices)
	if len(updatedProgress.ScaleOutReplicaIndices) == 0 && len(updatedProgress.ScaleInReplicaIndices) == 0 {
		updatedProgress = nil
	}

	r.scalingProgressExpectations.Record(pcs.ObjectMeta, updatedProgress)
	updatedPCS := pcs.DeepCopy()
	updatedPCS.Status.ScalingProgress = updatedProgress
	patch := client.MergeFromWithOptions(pcs, client.MergeFromWithOptimisticLock{})
	if err := r.client.Status().Patch(ctx, updatedPCS, patch); err != nil {
		r.scalingProgressExpectations.Forget(pcs.ObjectMeta)
		return false, fmt.Errorf("failed to update scale-in progress for PodCliqueSet %v: %w", client.ObjectKeyFromObject(pcs), err)
	}

	pcs.ResourceVersion = updatedPCS.ResourceVersion
	pcs.Status.ScalingProgress = updatedProgress
	for _, pcsReplicaIndex := range sets.List(completedPCSReplicaIndices) {
		logger.Info("PCS replica completed scale-in", "pcsReplicaIndex", pcsReplicaIndex)
		r.eventRecorder.Eventf(
			pcs,
			corev1.EventTypeNormal,
			constants.ReasonPodCliqueSetReplicaScaleInCompleted,
			"PCS replica index %d completed scale-in",
			pcsReplicaIndex,
		)
	}
	return true, nil
}

// scaleInPCSReplicaIndicesAwaitingResourceCleanup returns all scale-in replica indices still reported by components.
func scaleInPCSReplicaIndicesAwaitingResourceCleanup(syncResults map[component.Kind]pcssync.Result) sets.Set[int32] {
	awaitingResourceCleanup := sets.New[int32]()
	for _, syncResult := range syncResults {
		for _, pcsReplicaIndex := range syncResult.ScaleInReplicaIndicesAwaitingResourceCleanup {
			awaitingResourceCleanup.Insert(int32(pcsReplicaIndex))
		}
	}
	return awaitingResourceCleanup
}

// componentSyncResult associates a component kind with the result returned
// by its Sync operation.
type componentSyncResult struct {
	kind   component.Kind
	result pcssync.Result
}

// syncKindGroup runs the Sync operation for each kind in the group concurrently.
// It returns a non-nil ReconcileStepResult when reconciliation must stop, the
// results returned by operators that ran, and the kinds that requested the
// component sweep to continue before reconciliation is requeued.
func (r *Reconciler) syncKindGroup(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, state *pcssync.State, group []component.Kind, groupIndex int) (*ctrlcommon.ReconcileStepResult, []componentSyncResult, []component.Kind) {
	tasks := make([]utils.Task, 0, len(group))
	// Each task sends at most one result. Buffering by the group size lets tasks
	// finish without waiting for collection, which starts after the group completes.
	resultCh := make(chan componentSyncResult, len(group))
	kindByTaskName := make(map[string]component.Kind, len(group))
	for _, kind := range group {
		operator, err := r.operatorRegistry.GetOperator(kind)
		if err != nil {
			logger.V(1).Info("Skipping unregistered operator", "kind", kind)
			continue
		}
		taskName := fmt.Sprintf("SyncKind-%s", kind)
		kindByTaskName[taskName] = kind
		tasks = append(tasks, utils.Task{
			Name: taskName,
			Fn: func(ctx context.Context) error {
				logger.V(1).Info("Syncing PodCliqueSet resource", "kind", kind, "group", groupIndex)
				result, err := operator.Sync(ctx, logger, pcs, state)
				if err != nil {
					return err
				}
				resultCh <- componentSyncResult{
					kind:   kind,
					result: result,
				}
				return nil
			},
		})
	}
	if len(tasks) == 0 {
		return nil, nil, nil
	}

	runResult := utils.RunConcurrentlyAndWait(ctx, logger, tasks)
	close(resultCh)
	successfulResults := make([]componentSyncResult, 0, len(tasks))
	for taskResult := range resultCh {
		successfulResults = append(successfulResults, taskResult)
	}
	if runResult.HasErrors() {
		result, requeuedKinds := processKindGroupRunResultErrors(logger, runResult, kindByTaskName)
		return result, successfulResults, requeuedKinds
	}
	return nil, successfulResults, nil
}

// processKindGroupRunResultErrors processes component task failures and determines whether
// reconciliation should stop, requeue after a delay, or continue with the next group.
func processKindGroupRunResultErrors(logger logr.Logger, runResult utils.RunResult, kindByTaskName map[string]component.Kind) (*ctrlcommon.ReconcileStepResult, []component.Kind) {
	continueReconcileAndRequeueKinds := make([]component.Kind, 0)
	reconciliationErrors := make([]error, 0)
	requeueErrors := make([]error, 0)
	for _, failedTask := range runResult.FailedTasks {
		kind, ok := kindByTaskName[failedTask.Name]
		if !ok {
			reconciliationErrors = append(reconciliationErrors,
				fmt.Errorf("failed task %q has no component kind: %w", failedTask.Name, failedTask.Err))
			continue
		}
		if groveerr.IsContinueReconcileAndRequeue(failedTask.Err) {
			continueReconcileAndRequeueKinds = append(continueReconcileAndRequeueKinds, kind)
			logger.V(1).Info("Component requested post-sync requeue", "kind", kind, "message", failedTask.Err.Error())
			continue
		}
		if groveerr.IsRequeueAfter(failedTask.Err) {
			requeueErrors = append(requeueErrors, failedTask.Err)
			continue
		}
		logger.Error(failedTask.Err, "Failed to sync PodCliqueSet resource", "kind", kind)
		reconciliationErrors = append(reconciliationErrors, fmt.Errorf("failed to sync %s: %w", kind, failedTask.Err))
	}

	if len(reconciliationErrors) > 0 {
		result := ctrlcommon.ReconcileWithErrors("error syncing managed resources", reconciliationErrors...)
		return &result, continueReconcileAndRequeueKinds
	}
	if requeueAfter, shouldRequeue := groveerr.GetLowestRequeueAfter(requeueErrors); shouldRequeue {
		result := ctrlcommon.ReconcileAfter(requeueAfter, errors.Join(requeueErrors...).Error())
		return &result, continueReconcileAndRequeueKinds
	}
	return nil, continueReconcileAndRequeueKinds
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
	logger.V(1).Info("patched status.ObservedGeneration", "ObservedGeneration", pcs.Generation)
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
		// G1: PodCliqueSetReplica runs alone and first. It is the only component that writes the
		// PodCliqueSet status back into the shared object mid-reconcile. Status().Patch decodes the
		// server response into the same PodCliqueSet the other components read, so running it alongside
		// any component that reads the PodCliqueSet races that decode. It also produces the update
		// progress that PodGangMap reads, so it must complete before PodGangMap.
		{
			component.KindPodCliqueSetReplica,
		},
		// G2: RBAC + static per-PCS infra (Service, HPA targets by name so no ordering
		// vs PodClique/PCSG needed, ComputeDomain/ResourceClaim are independent add-ons).
		// PodGangMap is computed here — it has no dependency on any other component in this group, and
		// must be ready before PodGang (G5) reads it.
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
		// G3: migrate a legacy PodCliqueSet to the epoch-based PodGang scheme, using the PodGangMap from G2.
		// This runs before PodClique (G4) and PodGang (G5) so those see a consistent new-scheme world.
		{
			component.KindPodGangMigrator,
		},
		// G4: PodClique must exist before PodGang can reference their pods.
		{
			component.KindPodClique,
		},
		// G5: PCSG and PodGang run concurrently — PCSG creates its own PodCliques via a
		// separate reconciler, and PodGang reads existing PodClique/Pod state.
		{
			component.KindPodCliqueScalingGroup,
			component.KindPodGang,
		},
	}
}

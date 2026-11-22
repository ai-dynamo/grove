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
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/sync"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// reconcileSpec performs the main reconciliation logic for PodCliqueSet spec changes
func (r *Reconciler) reconcileSpec(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, state *pcssync.State) ctrlcommon.ReconcileStepResult {
	rLog := logger.WithValues("operation", "spec-reconcile")
	syncResources := func(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
		return r.syncPodCliqueSetResources(ctx, logger, pcs, state)
	}
	reconcileStepFns := []ctrlcommon.ReconcileStepFn[grovecorev1beta1.PodCliqueSet]{
		r.ensureFinalizer,
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
func (r *Reconciler) ensureFinalizer(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	if !controllerutil.ContainsFinalizer(pcs, apiconstants.FinalizerPodCliqueSet) {
		logger.Info("Adding finalizer", "finalizerName", apiconstants.FinalizerPodCliqueSet)
		if err := ctrlutils.AddAndPatchFinalizer(ctx, r.client, pcs, apiconstants.FinalizerPodCliqueSet); err != nil {
			return ctrlcommon.ReconcileWithErrors("error adding finalizer", fmt.Errorf("failed to add finalizer: %s to PodCliqueSet: %v: %w", apiconstants.FinalizerPodCliqueSet, client.ObjectKeyFromObject(pcs), err))
		}
	}
	return ctrlcommon.ContinueReconcile()
}

// processGenerationHashChange initializes generation hashes, migrates legacy status, and starts an update for a real specification change.
func (r *Reconciler) processGenerationHashChange(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	pcsObjectKey := client.ObjectKeyFromObject(pcs)
	expectationKey := pcsGenHashesExpKey(pcs)

	// If the generation status is not reflected correctly yet, requeue and allow the informer cache to catch up.
	if !r.isGenerationStatusExpectationSatisfied(expectationKey, pcs.Status) {
		return ctrlcommon.ReconcileAfter(constants.InformerCacheSyncRetryInterval, fmt.Sprintf("generation status is not up-to-date for PodCliqueSet: %v", pcsObjectKey))
	}
	r.pcsGenerationStatusExpectations.Delete(expectationKey)

	legacyGenerationHash, schedulingConfigurationHash, generationHash := computePCSHashes(pcs)
	if pcs.Status.CurrentGenerationHash == nil || pcs.Status.AppliedSchedulingConfigurationHash == nil {
		return r.establishGenerationHashBaseline(ctx, logger, pcs, expectationKey, schedulingConfigurationHash, generationHash)
	}
	if generationHash == *pcs.Status.CurrentGenerationHash {
		return ctrlcommon.ContinueReconcile()
	}
	if legacyGenerationHash == *pcs.Status.CurrentGenerationHash &&
		schedulingConfigurationHash == *pcs.Status.AppliedSchedulingConfigurationHash {
		return r.migrateLegacyGenerationHash(ctx, pcs, expectationKey, generationHash)
	}
	if err := r.initOrResetUpdate(ctx, pcs, expectationKey, generationHash, schedulingConfigurationHash); err != nil {
		return ctrlcommon.ReconcileWithErrors(fmt.Sprintf("could not initialize or reset update for PodCliqueSet: %v", pcsObjectKey), err)
	}
	return ctrlcommon.ContinueReconcile()
}

// establishGenerationHashBaseline initializes generation identities without starting an update. For a PodCliqueSet
// created before scheduling hashing was introduced, it preserves the existing generation hash so generation-format
// migration and update detection can happen on a later reconciliation.
func (r *Reconciler) establishGenerationHashBaseline(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, expectationKey, schedulingConfigurationHash, generationHash string) ctrlcommon.ReconcileStepResult {
	pcs.Status.AppliedSchedulingConfigurationHash = new(schedulingConfigurationHash)
	if pcs.Status.CurrentGenerationHash != nil {
		generationHash = *pcs.Status.CurrentGenerationHash
		if pcs.Status.UpdateProgress != nil {
			pcs.Status.UpdateProgress.TargetSchedulingConfigurationHash = schedulingConfigurationHash
		}
	}
	if err := r.setGenerationHashAndUpdateStatus(ctx, pcs, expectationKey, generationHash); err != nil {
		logger.Error(err, "failed to establish generation hash baseline", "generationHash", generationHash)
		return ctrlcommon.ReconcileWithErrors("error establishing generation hash baseline", err)
	}
	return ctrlcommon.ReconcileAfter(
		constants.InformerCacheSyncRetryInterval,
		fmt.Sprintf("waiting for the generation status baseline to become visible in the informer cache for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
	)
}

// migrateLegacyGenerationHash replaces an equivalent legacy hash with the combined hash without starting another update.
func (r *Reconciler) migrateLegacyGenerationHash(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet, expectationKey, generationHash string) ctrlcommon.ReconcileStepResult {
	if pcs.Status.UpdateProgress != nil && pcs.Status.UpdateProgress.UpdateEndedAt == nil {
		// Preserve the legacy target while an update that started before scheduling hashing converges.
		return ctrlcommon.ContinueReconcile()
	}
	if err := r.setGenerationHashAndUpdateStatus(ctx, pcs, expectationKey, generationHash); err != nil {
		return ctrlcommon.ReconcileWithErrors("error migrating PodCliqueSet generation hash", err)
	}
	return ctrlcommon.ContinueReconcile()
}

// pcsGenHashesExpKey returns the key for the in-memory generation-hashes expectation of a PCS.
// The key scheme is <pcs-namespace>/<pcs-name>/<pcs-UID>. Suffixing a UID ensures that a stale
// remnant expectation entry for the same <pcs-namespace>/<pcs-name> does not block reconciliation of
// a new PCS (with a different UID).
// See https://github.com/ai-dynamo/grove/issues/782 for context.
func pcsGenHashesExpKey(pcs *grovecorev1beta1.PodCliqueSet) string {
	return fmt.Sprintf("%s/%s", client.ObjectKeyFromObject(pcs), pcs.UID)
}

// generationHashesExpectation captures the generation hashes that must be visible in the informer cache before reconciliation continues.
type generationHashesExpectation struct {
	currentGenerationHash              string
	appliedSchedulingConfigurationHash *string
}

// isGenerationStatusExpectationSatisfied checks if the generation status matches expectations.
func (r *Reconciler) isGenerationStatusExpectationSatisfied(expectationKey string, status grovecorev1beta1.PodCliqueSetStatus) bool {
	expected, ok := r.pcsGenerationStatusExpectations.Load(expectationKey)
	if !ok {
		return true
	}
	expectation := expected.(generationHashesExpectation)
	return status.CurrentGenerationHash != nil &&
		*status.CurrentGenerationHash == expectation.currentGenerationHash &&
		utils.EqualOptional(status.AppliedSchedulingConfigurationHash, expectation.appliedSchedulingConfigurationHash)
}

// setGenerationHashAndUpdateStatus updates the PodCliqueSet generation status, stores the expectation, and updates the status subresource.
func (r *Reconciler) setGenerationHashAndUpdateStatus(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet, expectationKey, newGenerationHash string) error {
	pcs.Status.CurrentGenerationHash = &newGenerationHash
	if err := r.client.Status().Update(ctx, pcs); err != nil {
		return fmt.Errorf("could not update generation status for PodCliqueSet: %v: %w", client.ObjectKeyFromObject(pcs), err)
	}
	expectation := generationHashesExpectation{currentGenerationHash: newGenerationHash}
	if pcs.Status.AppliedSchedulingConfigurationHash != nil {
		expectation.appliedSchedulingConfigurationHash = new(*pcs.Status.AppliedSchedulingConfigurationHash)
	}
	r.pcsGenerationStatusExpectations.Store(expectationKey, expectation)
	return nil
}

// initOrResetUpdate initializes or resets update progress for the PodCliqueSet.
func (r *Reconciler) initOrResetUpdate(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet, expectationKey, newGenerationHash, targetSchedulingConfigurationHash string) error {
	updateStartedAt := metav1.Now()
	requiresReplicaRecreation := requiresPCSReplicaRecreation(pcs, targetSchedulingConfigurationHash)
	updateProgress := &grovecorev1beta1.PodCliqueSetUpdateProgress{
		RequiresReplicaRecreation:          requiresReplicaRecreation,
		TargetSchedulingConfigurationHash:  targetSchedulingConfigurationHash,
		PreviousPodCliqueSetGenerationHash: new(*pcs.Status.CurrentGenerationHash),
		UpdateStartedAt:                    updateStartedAt,
	}
	if isUpdateOrchestratedByGrove(pcs, requiresReplicaRecreation) {
		updateProgress.CurrentlyUpdating = prepareReplicaSelectionsForUpdate(pcs, updateStartedAt)
	} else {
		// Grove does not orchestrate ordinary OnDelete updates because it cannot know when every Pod has
		// been manually deleted. Replica recreation is always orchestrated by Grove.
		updateProgress.UpdateEndedAt = new(updateStartedAt)
	}
	// The Coherent strategy rolls only the components whose pod template changed. Capture that scope now,
	// while Status.CurrentGenerationHash still holds the previous hash, and preserve it for the update.
	if !requiresReplicaRecreation && componentutils.IsCoherentStrategy(pcs) {
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
	if err := r.setGenerationHashAndUpdateStatus(ctx, pcs, expectationKey, newGenerationHash); err != nil {
		return fmt.Errorf("could not set UpdateProgress for PodCliqueSet: %v: %w", client.ObjectKeyFromObject(pcs), err)
	}
	r.currentlyUpdatingExpectations.Record(pcs.ObjectMeta, updateProgress.CurrentlyUpdating)
	return nil
}

// requiresPCSReplicaRecreation reports whether the new update must recreate PCS replicas. Once started,
// recreation remains required across superseding updates, including a scheduling rollback to the applied hash.
func requiresPCSReplicaRecreation(pcs *grovecorev1beta1.PodCliqueSet, targetSchedulingConfigurationHash string) bool {
	if pcs.Status.AppliedSchedulingConfigurationHash == nil {
		return false
	}
	return targetSchedulingConfigurationHash != *pcs.Status.AppliedSchedulingConfigurationHash ||
		componentutils.IsPCSReplicaRecreationInProgress(pcs)
}

// isUpdateOrchestratedByGrove reports whether Grove drives the update to completion. Replica recreation is
// always orchestrated; ordinary OnDelete updates are completed by manual Pod deletion.
func isUpdateOrchestratedByGrove(pcs *grovecorev1beta1.PodCliqueSet, requiresReplicaRecreation bool) bool {
	return requiresReplicaRecreation || componentutils.ResolveUpdateStrategyType(pcs) != grovecorev1beta1.OnDeleteStrategy
}

// prepareReplicaSelectionsForUpdate prepares existing PCS replica selections for a new update.
// With no existing update progress, it returns no selections.
// Ordinary selections already committed to scale-in are excluded. Active replacements restart at their target
// index with a fresh same-index cleanup barrier and retain cleanup obligations for their previous sources.
// Canceled replacements retain their existing mapping and cleanup progress. Returned progress does not alias
// the existing update progress.
func prepareReplicaSelectionsForUpdate(pcs *grovecorev1beta1.PodCliqueSet, updateStartedAt metav1.Time) []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress {
	if pcs.Status.UpdateProgress == nil {
		return nil
	}

	scaleInPCSReplicaIndices := sets.New[int32]()
	if pcs.Status.ScalingProgress != nil {
		scaleInPCSReplicaIndices.Insert(pcs.Status.ScalingProgress.ScaleInReplicaIndices...)
	}

	previouslyUpdatingPCSReplicas := pcs.Status.UpdateProgress.CurrentlyUpdating
	currentlyUpdating := make([]grovecorev1beta1.PodCliqueSetReplicaUpdateProgress, 0, len(previouslyUpdatingPCSReplicas))

	for _, progress := range previouslyUpdatingPCSReplicas {
		if progress.UpdateEndedAt != nil {
			continue
		}
		if progress.Replacement == nil &&
			scaleInPCSReplicaIndices.Has(progress.ReplicaIndex) {
			continue
		}

		pcsReplicaIndex := progress.ReplicaIndex
		replacement := progress.Replacement.DeepCopy()

		if replacement != nil && replacement.CanceledAt == nil {
			if pcsReplicaIndex != replacement.TargetReplicaIndex {
				retiredSourcePCSReplicaIndices := sets.New(replacement.RetiredSourceReplicaIndices...)
				retiredSourcePCSReplicaIndices.Insert(pcsReplicaIndex)
				replacement.RetiredSourceReplicaIndices = sets.List(retiredSourcePCSReplicaIndices)
				pcsReplicaIndex = replacement.TargetReplicaIndex
			}
			replacement.SourceCleanupCompletedAt = nil
		}

		currentlyUpdating = append(currentlyUpdating,
			grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				ReplicaIndex:    pcsReplicaIndex,
				Replacement:     replacement,
				UpdateStartedAt: updateStartedAt,
			},
		)
	}

	slices.SortFunc(currentlyUpdating, func(a, b grovecorev1beta1.PodCliqueSetReplicaUpdateProgress) int {
		return cmp.Compare(a.ReplicaIndex, b.ReplicaIndex)
	})
	return currentlyUpdating
}

// syncPodCliqueSetResources synchronizes all managed child resources. Components are
// sync'd in dependency-ordered groups; within each group sync runs concurrently.
func (r *Reconciler) syncPodCliqueSetResources(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, state *pcssync.State) ctrlcommon.ReconcileStepResult {
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

	// Post-sync cleanup runs before honoring a soft component requeue, because an in-progress PCS replica
	// recreation depends on it to confirm source cleanup and retire drained replacements.
	// Reaching here means no group hard-stopped, so every registered replica-scoped participant was dispatched
	// and its result (if any) is recorded. The PodCliqueSetReplica orchestrator is not a replica-scoped
	// participant, so its in-progress requeue does not gate this cleanup.
	cleanedUpReplicaIndices := r.cleanedUpScaleInReplicaIndices(syncResults)
	originalPCS := pcs.DeepCopy()
	completedScaleInReplicaIndices := removeCompletedScaleInPCSReplicaIndices(logger, pcs, cleanedUpReplicaIndices)
	recordReplacementCleanupWork(logger, pcs, cleanedUpReplicaIndices)

	cleanupStatusChanged, err := r.commitPCSCleanupStatus(ctx, logger, pcs, originalPCS, completedScaleInReplicaIndices)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("error persisting PodCliqueSet cleanup status", err)
	}
	if cleanupStatusChanged {
		return ctrlcommon.ReconcileAfter(constants.InformerCacheSyncRetryInterval, fmt.Sprintf("waiting for updated cleanup progress to become visible in the informer cache for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)))
	}
	if len(continueReconcileAndRequeueKinds) > 0 {
		return ctrlcommon.ReconcileAfter(constants.ComponentSyncRetryInterval, fmt.Sprintf("requeueing sync due to components(s) %v after %s", continueReconcileAndRequeueKinds, constants.ComponentSyncRetryInterval))
	}
	return ctrlcommon.ContinueReconcile()
}

// cleanedUpScaleInReplicaIndices returns the scale-in replica indices that every applicable replica-scoped
// participant confirmed cleaned up this reconcile. It returns an empty set when any registered replica-scoped
// participant produced no Result this reconcile, for example because it errored or requested a requeue, because
// coverage is then incomplete and no index may be released. A participant that reported NotApplicable manages no
// resources for this PodCliqueSet and is excluded from coverage, so it neither confirms nor blocks a release.
func (r *Reconciler) cleanedUpScaleInReplicaIndices(syncResults map[component.Kind]pcssync.Result) sets.Set[int32] {
	participants := r.operatorRegistry.ReplicaScopedKinds()
	// Coverage gate. Every registered replica-scoped participant must have produced a Result this reconcile.
	for kind := range participants {
		if _, reported := syncResults[kind]; !reported {
			return sets.New[int32]()
		}
	}

	applicableParticipantCount := 0
	doneCountByReplicaIndex := make(map[int32]int)
	for kind, result := range syncResults {
		if !participants.Has(kind) || result.NotApplicable {
			continue
		}
		applicableParticipantCount++
		for _, replicaIndex := range result.CleanedUpReplicaIndices {
			doneCountByReplicaIndex[int32(replicaIndex)]++
		}
	}

	cleanedUp := sets.New[int32]()
	for replicaIndex, doneCount := range doneCountByReplicaIndex {
		if doneCount == applicableParticipantCount {
			cleanedUp.Insert(replicaIndex)
		}
	}
	return cleanedUp
}

// removeCompletedScaleInPCSReplicaIndices removes replica indices whose resources have been cleaned up from the
// in-memory scaling progress and returns the completed indices in ascending order. It does not persist status or
// emit events. The caller persists the mutated PodCliqueSet once for all post-sync cleanup.
func removeCompletedScaleInPCSReplicaIndices(logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, cleanedUpReplicaIndices sets.Set[int32]) []int32 {
	if pcs.Status.ScalingProgress == nil || len(pcs.Status.ScalingProgress.ScaleInReplicaIndices) == 0 {
		return nil
	}

	currentScaleInPCSReplicaIndices := sets.New(pcs.Status.ScalingProgress.ScaleInReplicaIndices...)
	completedPCSReplicaIndices := currentScaleInPCSReplicaIndices.Intersection(cleanedUpReplicaIndices)
	if completedPCSReplicaIndices.Len() == 0 {
		return nil
	}

	updatedProgress := pcs.Status.ScalingProgress.DeepCopy()
	updatedProgress.ScaleInReplicaIndices = sets.List(currentScaleInPCSReplicaIndices.Difference(completedPCSReplicaIndices))
	if len(updatedProgress.ScaleOutReplicaIndices) == 0 && len(updatedProgress.ScaleInReplicaIndices) == 0 {
		updatedProgress = nil
	}
	pcs.Status.ScalingProgress = updatedProgress

	completed := sets.List(completedPCSReplicaIndices)
	logger.Info("Removed completed scale-in replica indices from scaling progress", "completedPCSReplicaIndices", completed)
	return completed
}

// recordReplacementCleanupWork advances in-memory replacement cleanup from participant-confirmed indices.
// It clears confirmed retired sources, records source cleanup, and removes canceled entries only
// after all their cleanup obligations are satisfied. It never stamps UpdateEndedAt or persists status.
// The caller persists all post-sync cleanup mutations together.
func recordReplacementCleanupWork(
	logger logr.Logger,
	pcs *grovecorev1beta1.PodCliqueSet,
	cleanedUpReplicaIndices sets.Set[int32]) {
	if pcs.Status.UpdateProgress == nil || len(pcs.Status.UpdateProgress.CurrentlyUpdating) == 0 {
		return
	}

	var removedCanceledReplicaIndices []int32
	currentlyUpdating := pcs.Status.UpdateProgress.CurrentlyUpdating
	for i := range currentlyUpdating {
		replicaProgress := &currentlyUpdating[i]
		if replicaProgress.Replacement == nil || replicaProgress.UpdateEndedAt != nil {
			continue
		}

		canceledCleanupComplete := recordPCSReplicaReplacementCleanup(logger, replicaProgress, cleanedUpReplicaIndices)
		if canceledCleanupComplete {
			removedCanceledReplicaIndices = append(removedCanceledReplicaIndices, replicaProgress.ReplicaIndex)
		}
	}

	if len(removedCanceledReplicaIndices) > 0 {
		removedCanceledReplicaIndexSet := sets.New(removedCanceledReplicaIndices...)
		pcs.Status.UpdateProgress.CurrentlyUpdating = slices.DeleteFunc(currentlyUpdating, func(replicaProgress grovecorev1beta1.PodCliqueSetReplicaUpdateProgress) bool {
			return replicaProgress.Replacement != nil && replicaProgress.Replacement.CanceledAt != nil && removedCanceledReplicaIndexSet.Has(replicaProgress.ReplicaIndex)
		})
	}

	if len(removedCanceledReplicaIndices) > 0 {
		logger.Info("Removed canceled replacements after resource cleanup", "sourcePCSReplicaIndices", removedCanceledReplicaIndices)
	}
}

// recordPCSReplicaReplacementCleanup updates one unfinished replacement from participant-confirmed cleanup.
// It returns whether the canceled entry can be removed. The caller excludes completed entries and entries
// without replacement progress. Recording a new source-cleanup confirmation is logged immediately.
func recordPCSReplicaReplacementCleanup(
	logger logr.Logger,
	replicaProgress *grovecorev1beta1.PodCliqueSetReplicaUpdateProgress,
	cleanedUpReplicaIndices sets.Set[int32]) (canceledCleanupComplete bool) {
	replacement := replicaProgress.Replacement
	replacement.RetiredSourceReplicaIndices = slices.DeleteFunc(
		replacement.RetiredSourceReplicaIndices,
		func(pcsReplicaIndex int32) bool {
			return cleanedUpReplicaIndices.Has(pcsReplicaIndex)
		},
	)

	sourcePCSReplicaIndex := replicaProgress.ReplicaIndex
	if replacement.CanceledAt != nil {
		// Cancellation may follow source cleanup and partial target creation. Preserve any existing
		// barrier, but do not record a new creation barrier while draining canceled resources.
		canceledCleanupComplete = len(replacement.RetiredSourceReplicaIndices) == 0 &&
			cleanedUpReplicaIndices.Has(sourcePCSReplicaIndex) &&
			cleanedUpReplicaIndices.Has(replacement.TargetReplicaIndex)
		return canceledCleanupComplete
	}

	// Record source cleanup only after participant confirmation, and preserve an existing timestamp.
	// Repeated confirmation while the target is still converging must not cause another status write.
	if replacement.SourceCleanupCompletedAt != nil ||
		!cleanedUpReplicaIndices.Has(sourcePCSReplicaIndex) {
		return false
	}

	cleanupCompletedAt := metav1.Now()
	replacement.SourceCleanupCompletedAt = &cleanupCompletedAt
	logger.Info("Recorded replacement source cleanup completion", "sourcePCSReplicaIndex", sourcePCSReplicaIndex)
	return false
}

// commitPCSCleanupStatus persists post-sync cleanup mutations in a single optimistic-lock status patch.
// Actual differences from originalPCS determine which expectations are recorded. completedScaleInReplicaIndices
// is used only for scale-in completion events. Expectations are forgotten if the patch fails.
// It returns whether anything was persisted.
func (r *Reconciler) commitPCSCleanupStatus(
	ctx context.Context,
	logger logr.Logger,
	pcs, originalPCS *grovecorev1beta1.PodCliqueSet,
	completedScaleInReplicaIndices []int32) (bool, error) {
	scalingProgressChanged := !apiequality.Semantic.DeepEqual(originalPCS.Status.ScalingProgress, pcs.Status.ScalingProgress)
	updateProgressChanged := !apiequality.Semantic.DeepEqual(originalPCS.Status.UpdateProgress, pcs.Status.UpdateProgress)
	if !scalingProgressChanged && !updateProgressChanged {
		return false, nil
	}

	if scalingProgressChanged {
		r.scalingProgressExpectations.Record(pcs.ObjectMeta, pcs.Status.ScalingProgress)
	}
	if updateProgressChanged {
		r.currentlyUpdatingExpectations.Record(pcs.ObjectMeta, pcs.Status.UpdateProgress.CurrentlyUpdating)
	}

	patch := client.MergeFromWithOptions(originalPCS, client.MergeFromWithOptimisticLock{})
	if err := r.client.Status().Patch(ctx, pcs, patch); err != nil {
		if scalingProgressChanged {
			r.scalingProgressExpectations.Forget(pcs.ObjectMeta)
		}
		if updateProgressChanged {
			r.currentlyUpdatingExpectations.Forget(pcs.ObjectMeta)
		}
		return false, fmt.Errorf("failed to persist cleanup status for PodCliqueSet %v: %w", client.ObjectKeyFromObject(pcs), err)
	}

	for _, pcsReplicaIndex := range completedScaleInReplicaIndices {
		r.eventRecorder.Eventf(
			pcs,
			corev1.EventTypeNormal,
			constants.ReasonPodCliqueSetReplicaScaleInCompleted,
			"PCS replica index %d completed scale-in",
			pcsReplicaIndex,
		)
	}
	logger.Info("Persisted PodCliqueSet post-sync cleanup status")
	return true, nil
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
func (r *Reconciler) syncKindGroup(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, state *pcssync.State, group []component.Kind, groupIndex int) (*ctrlcommon.ReconcileStepResult, []componentSyncResult, []component.Kind) {
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
func (r *Reconciler) updateObservedGeneration(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
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
func (r *Reconciler) recordIncompleteReconcile(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, errResult *ctrlcommon.ReconcileStepResult) ctrlcommon.ReconcileStepResult {
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

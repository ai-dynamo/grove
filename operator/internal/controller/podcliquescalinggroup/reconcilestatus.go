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
	"slices"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	internalconstants "github.com/ai-dynamo/grove/operator/internal/constants"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// reconcileStatus updates the PodCliqueScalingGroup status with current replica counts and conditions
func (r *Reconciler) reconcileStatus(ctx context.Context, logger logr.Logger, pcsgObjectKey client.ObjectKey) ctrlcommon.ReconcileStepResult {
	// It is important that we re-fetch the PodCliqueScalingGroup. In case rolling update has been started during the spec reconciliation,
	// then UpdateInProgress condition will be set. It is essential that this is checked when computing status.
	// It is a possibility that the informer cache does not reflect the changes that are made to status conditions are not immediately reflected.
	// However, it is currently assumed that eventually this condition will be visible eventually. We will think of alleviating this delay
	// in the future.
	pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{}
	if result := ctrlutils.GetPodCliqueScalingGroup(ctx, r.client, logger, pcsgObjectKey, pcsg); ctrlcommon.ShortCircuitReconcileFlow(result) {
		return result
	}

	originalStatus := pcsg.Status.DeepCopy()
	patchObj := client.MergeFrom(pcsg.DeepCopy())

	pcs, err := componentutils.GetPodCliqueSet(ctx, r.client, pcsg.ObjectMeta)
	if err != nil {
		logger.Error(err, "failed to get owner PodCliqueSet")
		return ctrlcommon.ReconcileWithErrors("failed to get owner PodCliqueSet", err)
	}

	pclqsPerPCSGReplica, err := r.getPodCliquesPerPCSGReplica(ctx, pcs.Name, client.ObjectKeyFromObject(pcsg))
	if err != nil {
		logger.Error(err, "failed to list PodCliques for PodCliqueScalingGroup")
		return ctrlcommon.ReconcileWithErrors(fmt.Sprintf("failed to list PodCliques for PodCliqueScalingGroup: %q", client.ObjectKeyFromObject(pcsg)), err)
	}
	// Prune children that no longer belong to the spec — primarily PCLQs whose name is not in
	// Spec.CliqueNames after a clique-name change. Without this, lingering old-named PCLQs at
	// valid replica indexes would inflate UpdatedPodCliquesCount past TotalPodCliquesCount
	// (which is derived purely from the new spec) while the cascade delete is in flight.
	// Replica-index strays (idx >= Spec.Replicas) are also dropped for hygiene, though
	// mutateReplicas already ignores them via its [0, Spec.Replicas) loop bounds.
	pclqsPerPCSGReplica = pruneStrayPCSGPCLQs(pcsg, pclqsPerPCSGReplica)
	mutateReplicas(logger, pcs, pcsg, pclqsPerPCSGReplica)
	mutateMinAvailableBreachedCondition(logger, pcsg, pclqsPerPCSGReplica)
	r.emitAllScheduledReplicasLostIfNeeded(pcsg, originalStatus.ScheduledReplicas)

	if err = mutateSelector(pcs, pcsg); err != nil {
		logger.Error(err, "failed to update selector for PodCliqueScalingGroup")
		return ctrlcommon.ReconcileWithErrors("failed to update selector for PodCliqueScalingGroup", err)
	}

	mutateCurrentPodCliqueSetGenerationHash(logger, pcs, pcsg, lo.Flatten(lo.Values(pclqsPerPCSGReplica)))

	// Skip the status patch when every mutate* above left status byte-identical to what the
	// previous reconcile already persisted. The mutators are the only code writing
	// pcsg.Status here, so equality means there is nothing for the apiserver to store.
	// Issuing the Patch anyway bumps resourceVersion and fires a watch event that wakes the
	// parent PCS reconciler and any other PCSG observers, cascading into spurious
	// reconciles. equality.Semantic is required because the status mixes counters,
	// pointers, conditions, and a label-selector map.
	if equality.Semantic.DeepEqual(*originalStatus, pcsg.Status) {
		return ctrlcommon.ContinueReconcile()
	}

	if err = r.client.Status().Patch(ctx, pcsg, patchObj); err != nil {
		logger.Error(err, "failed to update PodCliqueScalingGroup status")
		return ctrlcommon.ReconcileWithErrors("failed to update the status with label selector and replicas", err)
	}

	return ctrlcommon.ContinueReconcile()
}

// mutateReplicas updates the PodCliqueScalingGroup status with replica counts based on constituent PodClique states.
// It also derives child-PCLQ update progress counts when an update is in flight. The iteration is bounded to
// expected replica indexes [0, Spec.Replicas) — the caller has already pruned stray children — so counters stay
// consistent with the spec-derived totals during scale-down.
func mutateReplicas(logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, pclqsPerPCSGReplica map[string][]grovecorev1alpha1.PodClique) {
	pcsg.Status.Replicas = pcsg.Spec.Replicas
	var scheduledReplicas, availableReplicas, updatedReplicas, updatedPCLQs, totalPCLQs int32
	cliqueNamesPerReplica := int32(len(pcsg.Spec.CliqueNames))
	currentPCSGenerationHash := pcs.Status.CurrentGenerationHash
	expectedPCLQPodTemplateHashes := componentutils.GetPCLQTemplateHashes(pcs, pcsg)
	for replicaIndex := 0; replicaIndex < int(pcsg.Spec.Replicas); replicaIndex++ {
		pcsgReplicaIndex := strconv.Itoa(replicaIndex)
		pclqs := pclqsPerPCSGReplica[pcsgReplicaIndex]
		isScheduled, isAvailable, isUpdated := computeReplicaStatus(logger, currentPCSGenerationHash, expectedPCLQPodTemplateHashes, pcsgReplicaIndex, len(pcsg.Spec.CliqueNames), pclqs)
		if isScheduled {
			scheduledReplicas++
		}
		if isAvailable {
			availableReplicas++
		}
		if isUpdated {
			updatedReplicas++
		}
		updatedPCLQs += countPCSGReplicaUpdatedPCLQs(currentPCSGenerationHash, expectedPCLQPodTemplateHashes, pclqs)
	}
	totalPCLQs = pcsg.Spec.Replicas * cliqueNamesPerReplica
	logger.Info("Mutating PodCliqueScalingGroup replicas",
		"pcsg", client.ObjectKeyFromObject(pcsg),
		"scheduledReplicas", scheduledReplicas, "availableReplicas", availableReplicas, "updatedReplicas", updatedReplicas,
		"updatedPCLQs", updatedPCLQs, "totalPCLQs", totalPCLQs)
	pcsg.Status.ScheduledReplicas = scheduledReplicas
	pcsg.Status.AvailableReplicas = availableReplicas
	pcsg.Status.UpdatedReplicas = updatedReplicas
	if pcsg.Status.UpdateProgress != nil {
		pcsg.Status.UpdateProgress.UpdatedPodCliquesCount = updatedPCLQs
		pcsg.Status.UpdateProgress.TotalPodCliquesCount = totalPCLQs
	}
}

// countPCSGReplicaUpdatedPCLQs counts non-terminating PCLQs in a PCSG replica whose generation
// hash matches the parent PCS hash.
func countPCSGReplicaUpdatedPCLQs(pcsGenerationHash *string, expectedPCLQPodTemplateHashes map[string]string, pclqs []grovecorev1alpha1.PodClique) int32 {
	if pcsGenerationHash == nil {
		return 0
	}
	var n int32
	for i := range pclqs {
		pclq := &pclqs[i]
		if k8sutils.IsResourceTerminating(pclq.ObjectMeta) {
			continue
		}
		if isPCSGChildPCLQUpdated(pclq, expectedPCLQPodTemplateHashes, pcsGenerationHash) {
			n++
		}
	}
	return n
}

// computeReplicaStatus processes a single PodCliqueScalingGroup replica and returns whether it is scheduled and available.
func computeReplicaStatus(logger logr.Logger, currentPCSGenerationHash *string, expectedPCLQPodTemplateHashes map[string]string, pcsgReplicaIndex string, numPCSGCliqueNames int, pclqs []grovecorev1alpha1.PodClique) (isScheduled, isAvailable, isUpdated bool) {
	nonTerminatedPCSGPodCliques := lo.Filter(pclqs, func(pclq grovecorev1alpha1.PodClique, _ int) bool {
		return !k8sutils.IsResourceTerminating(pclq.ObjectMeta)
	})
	if len(nonTerminatedPCSGPodCliques) != numPCSGCliqueNames {
		logger.V(1).Info("PCSG replica does not have the expected number of PodCliques",
			"pcsgReplicaIndex", pcsgReplicaIndex,
			"expectedPCSGReplicaPCLQSize", numPCSGCliqueNames,
			"actualPCSGReplicaPCLQSize", len(nonTerminatedPCSGPodCliques))
		return
	}
	isScheduled = lo.EveryBy(nonTerminatedPCSGPodCliques, func(pclq grovecorev1alpha1.PodClique) bool {
		return k8sutils.IsConditionTrue(pclq.Status.Conditions, constants.ConditionTypePodCliqueScheduled)
	})
	// A PodClique is considered available if it schedules at least MinAvailable pods.
	if isScheduled {
		isAvailable = lo.EveryBy(nonTerminatedPCSGPodCliques, func(pclq grovecorev1alpha1.PodClique) bool {
			return pclq.Status.ReadyReplicas >= *pclq.Spec.MinAvailable
		})
		isAvailable = isAvailable && len(nonTerminatedPCSGPodCliques) == numPCSGCliqueNames
		isUpdated = isAvailable &&
			currentPCSGenerationHash != nil &&
			lo.EveryBy(nonTerminatedPCSGPodCliques, func(pclq grovecorev1alpha1.PodClique) bool {
				return isPCSGChildPCLQUpdated(&pclq, expectedPCLQPodTemplateHashes, currentPCSGenerationHash)
			})
	}
	return
}

func isPCSGChildPCLQUpdated(pclq *grovecorev1alpha1.PodClique, expectedPCLQPodTemplateHashes map[string]string, pcsGenerationHash *string) bool {
	if pcsGenerationHash == nil || pclq.Spec.MinAvailable == nil {
		return false
	}
	expectedPodTemplateHash, ok := expectedPCLQPodTemplateHashes[pclq.Name]
	if !ok || expectedPodTemplateHash == "" {
		return false
	}
	return pclq.Labels[apicommon.LabelPodTemplateHash] == expectedPodTemplateHash &&
		pclq.Status.CurrentPodTemplateHash != nil &&
		*pclq.Status.CurrentPodTemplateHash == expectedPodTemplateHash &&
		pclq.Status.CurrentPodCliqueSetGenerationHash != nil &&
		*pclq.Status.CurrentPodCliqueSetGenerationHash == *pcsGenerationHash &&
		pclq.Status.ReadyReplicas >= *pclq.Spec.MinAvailable &&
		pclq.Status.UpdatedReplicas >= *pclq.Spec.MinAvailable
}

// emitAllScheduledReplicasLostIfNeeded emits a Warning event when ScheduledReplicas drops from
// non-zero to zero. The MinAvailableBreached condition also flips on this transition, but the
// event gives operators a discrete, log-visible signal that a previously-running workload is
// fully down (and that gang termination is now armed and will fire after TerminationDelay).
func (r *Reconciler) emitAllScheduledReplicasLostIfNeeded(pcsg *grovecorev1alpha1.PodCliqueScalingGroup, originalScheduled int32) {
	if originalScheduled > 0 && pcsg.Status.ScheduledReplicas == 0 {
		r.eventRecorder.Eventf(pcsg, corev1.EventTypeWarning, internalconstants.ReasonAllScheduledReplicasLost,
			"All scheduled replicas lost (was %d). Gang termination will fire after TerminationDelay if the PCSG stays below MinAvailable; investigate node availability or capacity.",
			originalScheduled)
	}
}

// mutateMinAvailableBreachedCondition updates the MinAvailableBreached condition based on replica availability
func mutateMinAvailableBreachedCondition(logger logr.Logger, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, pclqsPerPCSGReplica map[string][]grovecorev1alpha1.PodClique) {
	newCondition := computeMinAvailableBreachedCondition(logger, pcsg, pclqsPerPCSGReplica)
	if k8sutils.HasConditionChanged(pcsg.Status.Conditions, newCondition) {
		logger.Info("Updating MinAvailableBreached condition for PodCliqueScalingGroup",
			"pcsg", client.ObjectKeyFromObject(pcsg),
			"type", newCondition.Type,
			"status", newCondition.Status,
			"reason", newCondition.Reason)
		meta.SetStatusCondition(&pcsg.Status.Conditions, newCondition)
	}
}

// computeMinAvailableBreachedCondition computes the MinAvailableBreached condition for the
// PodCliqueScalingGroup. Definition:
//
//   - A PCSG replica is in breach if at least one of its PodCliques is in breach
//     (MinAvailableBreached=True on the PCLQ).
//   - The PCSG itself is in breach when the number of replicas NOT in breach is less than
//     pcsg.Spec.MinAvailable. Per-replica restart is handled separately by the PCSG sync
//     flow; the condition computed here is the signal the PCS-level gang-termination
//     handler reads to decide whether to restart the whole PCS replica.
//
// Rolling update short-circuits this to Unknown so gang termination doesn't fire mid-update.
func computeMinAvailableBreachedCondition(logger logr.Logger, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, pclqsPerPCSGReplica map[string][]grovecorev1alpha1.PodClique) metav1.Condition {
	if componentutils.IsPCSGUpdateInProgress(pcsg) {
		return metav1.Condition{
			Type:    constants.ConditionTypeMinAvailableBreached,
			Status:  metav1.ConditionUnknown,
			Reason:  constants.ConditionReasonUpdateInProgress,
			Message: "Update is in progress",
		}
	}

	minAvailable := int(*pcsg.Spec.MinAvailable)
	breachedReplicas := computeMinAvailableBreachedReplicas(logger, pcsg, pclqsPerPCSGReplica)
	existingReplicas := len(pclqsPerPCSGReplica)
	notInBreachReplicas := existingReplicas - breachedReplicas
	if notInBreachReplicas < minAvailable {
		return metav1.Condition{
			Type:    constants.ConditionTypeMinAvailableBreached,
			Status:  metav1.ConditionTrue,
			Reason:  constants.ConditionReasonInsufficientAvailablePCSGReplicas,
			Message: fmt.Sprintf("PCSG replicas not in breach (%d) below MinAvailable (%d)", notInBreachReplicas, minAvailable),
		}
	}
	return metav1.Condition{
		Type:    constants.ConditionTypeMinAvailableBreached,
		Status:  metav1.ConditionFalse,
		Reason:  constants.ConditionReasonSufficientAvailablePCSGReplicas,
		Message: fmt.Sprintf("PCSG replicas not in breach (%d) meets MinAvailable (%d)", notInBreachReplicas, minAvailable),
	}
}

// computeMinAvailableBreachedReplicas counts PCSG replicas that have at least one PodClique with MinAvailable breached.
// Bounded to expected replica indexes [0, Spec.Replicas) so stale-index children left behind during scale-down do not
// inflate the breach count and drive availableReplicas below minAvailable spuriously.
func computeMinAvailableBreachedReplicas(logger logr.Logger, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, pclqsPerPCSGReplica map[string][]grovecorev1alpha1.PodClique) int {
	var breachedReplicas int
	for replicaIndex := 0; replicaIndex < int(pcsg.Spec.Replicas); replicaIndex++ {
		pcsgReplicaIndex := strconv.Itoa(replicaIndex)
		pclqs := pclqsPerPCSGReplica[pcsgReplicaIndex]
		isMinAvailableBreached := lo.Reduce(pclqs, func(agg bool, pclq grovecorev1alpha1.PodClique, _ int) bool {
			return agg || k8sutils.IsConditionTrue(pclq.Status.Conditions, constants.ConditionTypeMinAvailableBreached)
		}, false)
		if isMinAvailableBreached {
			breachedReplicas++
		}
		logger.Info("PodCliqueScalingGroup replica has MinAvailableBreached condition set to true", "pcsgReplicaIndex", pcsgReplicaIndex, "isMinAvailableBreached", isMinAvailableBreached)
	}
	return breachedReplicas
}

// getPodCliquesPerPCSGReplica retrieves and groups PodCliques by their PCSG replica index
func (r *Reconciler) getPodCliquesPerPCSGReplica(ctx context.Context, pcsName string, pcsgObjKey client.ObjectKey) (map[string][]grovecorev1alpha1.PodClique, error) {
	selectorLabels := lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName),
		map[string]string{
			apicommon.LabelPodCliqueScalingGroup: pcsgObjKey.Name,
			apicommon.LabelComponentKey:          apicommon.LabelComponentNamePodCliqueScalingGroupPodClique,
		},
	)
	pclqs, err := componentutils.GetPCLQsByOwner(ctx,
		r.client,
		constants.KindPodCliqueScalingGroup,
		pcsgObjKey,
		selectorLabels,
	)
	if err != nil {
		return nil, err
	}
	pclqsPerPCSGReplica := componentutils.GroupPCLQsByPCSGReplicaIndex(pclqs)
	return pclqsPerPCSGReplica, nil
}

// mutateSelector creates and sets the label selector for autoscaler use when scaling is configured
func mutateSelector(pcs *grovecorev1alpha1.PodCliqueSet, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) error {
	pcsReplicaIndex, err := k8sutils.GetPodCliqueSetReplicaIndex(pcsg.ObjectMeta)
	if err != nil {
		return err
	}
	matchingPCSGConfig, ok := lo.Find(pcs.Spec.Template.PodCliqueScalingGroupConfigs, func(pcsgConfig grovecorev1alpha1.PodCliqueScalingGroupConfig) bool {
		pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}, pcsgConfig.Name)
		return pcsgFQN == pcsg.Name
	})
	if !ok {
		// This should ideally never happen but if you find a PCSG that is not defined in PCS then just ignore it.
		return nil
	}
	// No ScaleConfig has been defined of this PCSG, therefore there is no need to add a selector in the status.
	if matchingPCSGConfig.ScaleConfig == nil {
		return nil
	}
	labels := lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
		map[string]string{
			apicommon.LabelPodCliqueScalingGroup: pcsg.Name,
		},
	)
	selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{MatchLabels: labels})
	if err != nil {
		return fmt.Errorf("%w: failed to create label selector for PodCliqueScalingGroup %v", err, client.ObjectKeyFromObject(pcsg))
	}
	pcsg.Status.Selector = ptr.To(selector.String())
	return nil
}

// mutateCurrentPodCliqueSetGenerationHash updates the current generation hash when all PodCliques are updated and no rolling update is in progress
func mutateCurrentPodCliqueSetGenerationHash(logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, existingPCLQs []grovecorev1alpha1.PodClique) {
	pclqFQNsPendingUpdate := componentutils.GetPCLQsInPCSGPendingUpdate(pcs, pcsg, existingPCLQs)
	if len(pclqFQNsPendingUpdate) > 0 {
		logger.Info("Found PodCliques associated to PodCliqueScalingGroup pending update", "pclqFQNsPendingUpdate", pclqFQNsPendingUpdate)
		return
	}
	if componentutils.IsPCSGUpdateInProgress(pcsg) {
		logger.Info("PodCliqueScalingGroup is currently updating, cannot set PodCliqueSet CurrentGenerationHash yet")
		return
	}
	if pcs.Status.CurrentGenerationHash == nil {
		return
	}
	if !havePCSGPodCliquesConverged(pcs, pcsg, existingPCLQs) {
		return
	}
	pcsg.Status.CurrentPodCliqueSetGenerationHash = pcs.Status.CurrentGenerationHash
}

// havePCSGPodCliquesConverged reports whether every expected PodClique in the
// PodCliqueScalingGroup has reconciled its template and generation hashes to the
// current PodCliqueSet spec.
func havePCSGPodCliquesConverged(pcs *grovecorev1alpha1.PodCliqueSet, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, existingPCLQs []grovecorev1alpha1.PodClique) bool {
	if pcs.Status.CurrentGenerationHash == nil {
		return false
	}
	expectedPCLQPodTemplateHashes := componentutils.GetPCLQTemplateHashes(pcs, pcsg)
	if len(expectedPCLQPodTemplateHashes) != int(pcsg.Spec.Replicas)*len(pcsg.Spec.CliqueNames) {
		return false
	}
	existingPCLQsByName := lo.SliceToMap(existingPCLQs, func(pclq grovecorev1alpha1.PodClique) (string, grovecorev1alpha1.PodClique) {
		return pclq.Name, pclq
	})
	for pclqName, expectedPodTemplateHash := range expectedPCLQPodTemplateHashes {
		pclq, ok := existingPCLQsByName[pclqName]
		if !ok || k8sutils.IsResourceTerminating(pclq.ObjectMeta) {
			return false
		}
		if pclq.Labels[apicommon.LabelPodTemplateHash] != expectedPodTemplateHash {
			return false
		}
		if pclq.Status.CurrentPodTemplateHash == nil || *pclq.Status.CurrentPodTemplateHash != expectedPodTemplateHash {
			return false
		}
		if pclq.Status.CurrentPodCliqueSetGenerationHash == nil || *pclq.Status.CurrentPodCliqueSetGenerationHash != *pcs.Status.CurrentGenerationHash {
			return false
		}
	}
	return true
}

// pruneStrayPCSGPCLQs drops children whose replica index is outside [0, Spec.Replicas) or whose FQN
// is not produced by Spec.CliqueNames at the kept indexes — strays left behind by scale-down or a
// clique-name change that would otherwise inflate replica/progress counters past the spec-derived
// totals. Mutates the input map in place (caller holds the only reference, fresh from grouping).
func pruneStrayPCSGPCLQs(pcsg *grovecorev1alpha1.PodCliqueScalingGroup, pclqsPerPCSGReplica map[string][]grovecorev1alpha1.PodClique) map[string][]grovecorev1alpha1.PodClique {
	expectedReplicas := int(pcsg.Spec.Replicas)
	expectedFQNs := make(map[string]struct{}, expectedReplicas*len(pcsg.Spec.CliqueNames))
	for replicaIndex := 0; replicaIndex < expectedReplicas; replicaIndex++ {
		for _, cliqueName := range pcsg.Spec.CliqueNames {
			expectedFQNs[apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsg.Name, Replica: replicaIndex}, cliqueName)] = struct{}{}
		}
	}
	for key, pclqs := range pclqsPerPCSGReplica {
		idx, err := strconv.Atoi(key)
		if err != nil || idx < 0 || idx >= expectedReplicas {
			delete(pclqsPerPCSGReplica, key)
			continue
		}
		kept := slices.DeleteFunc(pclqs, func(p grovecorev1alpha1.PodClique) bool {
			_, ok := expectedFQNs[p.Name]
			return !ok
		})
		if len(kept) == 0 {
			delete(pclqsPerPCSGReplica, key)
			continue
		}
		pclqsPerPCSGReplica[key] = kept
	}
	return pclqsPerPCSGReplica
}

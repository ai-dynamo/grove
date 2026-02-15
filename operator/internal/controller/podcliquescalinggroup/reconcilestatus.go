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

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	ctrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
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
	mutateReplicas(logger, pcs.Status.CurrentGenerationHash, pcsg, pclqsPerPCSGReplica)
	mutateMinAvailableBreachedCondition(logger, pcsg, pclqsPerPCSGReplica)

	if err = mutateSelector(pcs, pcsg); err != nil {
		logger.Error(err, "failed to update selector for PodCliqueScalingGroup")
		return ctrlcommon.ReconcileWithErrors("failed to update selector for PodCliqueScalingGroup", err)
	}

	if err = r.mutateCurrentPodCliqueSetGenerationHash(logger, pcs, pcsg, lo.Flatten(lo.Values(pclqsPerPCSGReplica))); err != nil {
		return ctrlcommon.ReconcileWithErrors("failed to update CurrentPodCliqueSetGenerationHash for PodCliqueScalingGroup", err)
	}

	if err = r.client.Status().Patch(ctx, pcsg, patchObj); err != nil {
		logger.Error(err, "failed to update PodCliqueScalingGroup status")
		return ctrlcommon.ReconcileWithErrors("failed to update the status with label selector and replicas", err)
	}

	return ctrlcommon.ContinueReconcile()
}

// mutateReplicas updates the PodCliqueScalingGroup status with replica counts based on constituent PodClique states
func mutateReplicas(logger logr.Logger, currentPCSGenerationHash *string, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, pclqsPerPCSGReplica map[string][]grovecorev1alpha1.PodClique) {
	pcsg.Status.Replicas = pcsg.Spec.Replicas
	var scheduledReplicas, availableReplicas, updatedReplicas int32
	for pcsgReplicaIndex, pclqs := range pclqsPerPCSGReplica {
		isScheduled, isAvailable, isUpdated := computeReplicaStatus(logger, currentPCSGenerationHash, pcsgReplicaIndex, len(pcsg.Spec.CliqueNames), pclqs)
		if isScheduled {
			scheduledReplicas++
		}
		if isAvailable {
			availableReplicas++
		}
		if isUpdated {
			updatedReplicas++
		}
	}
	logger.Info("Mutating PodCliqueScalingGroup replicas",
		"pcsg", client.ObjectKeyFromObject(pcsg),
		"scheduledReplicas", scheduledReplicas, "availableReplicas", availableReplicas, "updatedReplicas", updatedReplicas)
	pcsg.Status.ScheduledReplicas = scheduledReplicas
	pcsg.Status.AvailableReplicas = availableReplicas
	pcsg.Status.UpdatedReplicas = updatedReplicas
}

// computeReplicaStatus processes a single PodCliqueScalingGroup replica and returns whether it is scheduled and available.
func computeReplicaStatus(logger logr.Logger, currentPCSGenerationHash *string, pcsgReplicaIndex string, numPCSGCliqueNames int, pclqs []grovecorev1alpha1.PodClique) (isScheduled, isAvailable, isUpdated bool) {
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
				return pclq.Status.CurrentPodCliqueSetGenerationHash != nil && *pclq.Status.CurrentPodCliqueSetGenerationHash == *currentPCSGenerationHash
			})
	}
	return
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

// computeMinAvailableBreachedCondition computes the MinAvailableBreached condition for the PodCliqueScalingGroup.
// If rolling update is under progress, then gang termination for this PCSG is disabled. This is achieved by marking the status to `Unknown`. This PCSG will not influence
// the gang termination of PCS replica till its update has completed.
// If the number of scheduled replicas is less than the MinAvailable, then it is too pre-mature to set the MinAvailableBreached condition to true.
// If we set MinAvailableBreached condition to true, then it can result in pre-mature gang termination when the PodClique Pods are still starting.
// If there are sufficient scheduled replicas (i.e. scheduledReplicas >= minAvailable), then we can compute the MinAvailableBreached condition based on the number of ready replicas.
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
	scheduledReplicas := int(pcsg.Status.ScheduledReplicas)
	if scheduledReplicas < minAvailable {
		return metav1.Condition{
			Type:    constants.ConditionTypeMinAvailableBreached,
			Status:  metav1.ConditionFalse,
			Reason:  constants.ConditionReasonInsufficientScheduledPCSGReplicas,
			Message: fmt.Sprintf("Insufficient scheduled replicas. expected at least: %d, found: %d", minAvailable, scheduledReplicas),
		}
	}
	minAvailableBreachedReplicas := computeMinAvailableBreachedReplicas(logger, pclqsPerPCSGReplica)
	availableReplicas := scheduledReplicas - minAvailableBreachedReplicas
	if availableReplicas < minAvailable {
		return metav1.Condition{
			Type:    constants.ConditionTypeMinAvailableBreached,
			Status:  metav1.ConditionTrue,
			Reason:  constants.ConditionReasonInsufficientAvailablePCSGReplicas,
			Message: fmt.Sprintf("Insufficient PodCliqueScalingGroup ready replicas, expected at least: %d, found: %d", minAvailable, availableReplicas),
		}
	}
	return metav1.Condition{
		Type:    constants.ConditionTypeMinAvailableBreached,
		Status:  metav1.ConditionFalse,
		Reason:  constants.ConditionReasonSufficientAvailablePCSGReplicas,
		Message: fmt.Sprintf("Sufficient PodCliqueScalingGroup ready replicas, expected at least: %d, found: %d", minAvailable, availableReplicas),
	}
}

// computeMinAvailableBreachedReplicas counts PCSG replicas that have at least one PodClique with MinAvailable breached
func computeMinAvailableBreachedReplicas(logger logr.Logger, pclqsPerPCSGReplica map[string][]grovecorev1alpha1.PodClique) int {
	var breachedReplicas int
	for pcsgReplicaIndex, pclqs := range pclqsPerPCSGReplica {
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
func (r *Reconciler) mutateCurrentPodCliqueSetGenerationHash(logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, existingPCLQs []grovecorev1alpha1.PodClique) error {
	pclqFQNsPendingUpdate, err := r.getPCLQsInPCSGPendingUpdate(logger, pcs, pcsg, existingPCLQs)
	if err != nil {
		return fmt.Errorf("failed to mutate CurrentPodCliqueSetGenerationHash for PodCliqueScalingGroup %q: %w", client.ObjectKeyFromObject(pcsg), err)
	}

	if len(pclqFQNsPendingUpdate) > 0 {
		logger.Info("Found PodCliques associated to PodCliqueScalingGroup pending update", "pclqFQNsPendingUpdate", pclqFQNsPendingUpdate)
		return nil
	}
	if componentutils.IsPCSGUpdateInProgress(pcsg) {
		logger.Info("PodCliqueScalingGroup is currently updating, cannot set PodCliqueSet CurrentGenerationHash yet")
		return nil
	}

	pcsg.Status.CurrentPodCliqueSetGenerationHash = pcs.Status.CurrentGenerationHash
	return nil
}

// getPCLQsInPCSGPendingUpdate collects the PodClique FQNs that are pending updates.
// It identifies PCLQ pending update by comparing the current PodTemplateHash label on an existing PCLQ with that of
// a computed PodTemplateHash from the latest PodCliqueSet resource.
func (r *Reconciler) getPCLQsInPCSGPendingUpdate(logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, existingPCLQs []grovecorev1alpha1.PodClique) ([]string, error) {
	pclqFQNsPendingUpdate := make([]string, 0, len(existingPCLQs))
	expectedPCLQPodTemplateHashes, err := componentutils.GetPCLQTemplateHashesForPCSG(r.podTemplateSpecHashCache, logger, pcs, pcsg)
	if err != nil {
		return nil, err
	}
	for _, existingPCLQ := range existingPCLQs {
		existingPodTemplateHash := existingPCLQ.Labels[apicommon.LabelPodTemplateHash]
		expectedPodTemplateHash := expectedPCLQPodTemplateHashes[existingPCLQ.Name]
		if existingPodTemplateHash != expectedPodTemplateHash {
			pclqFQNsPendingUpdate = append(pclqFQNsPendingUpdate, expectedPodTemplateHash)
		}
	}
	return pclqFQNsPendingUpdate, nil
}

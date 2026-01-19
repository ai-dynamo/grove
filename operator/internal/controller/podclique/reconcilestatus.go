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

package podclique

import (
	"context"
	"fmt"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// reconcileStatus updates the PodClique status
func (r *Reconciler) reconcileStatus(ctx context.Context, logger logr.Logger, pclq *grovecorev1alpha1.PodClique) ctrlcommon.ReconcileStepResult {
	pcsName := componentutils.GetPodCliqueSetName(pclq.ObjectMeta)
	pclqObjectKey := client.ObjectKeyFromObject(pclq)
	patch := client.MergeFrom(pclq.DeepCopy())

	pcs, err := componentutils.GetPodCliqueSet(ctx, r.client, pclq.ObjectMeta)
	if err != nil {
		logger.Error(err, "could not get PodCliqueSet for PodClique", "pclqObjectKey", pclqObjectKey)
		return ctrlcommon.ReconcileWithErrors("could not get PodCliqueSet for PodClique", err)
	}

	existingPods, err := componentutils.GetPCLQPods(ctx, r.client, pcsName, pclq)
	if err != nil {
		logger.Error(err, "failed to list pods for PodClique")
		return ctrlcommon.ReconcileWithErrors(fmt.Sprintf("failed to list pods for PodClique: %q", pclqObjectKey), err)
	}

	podCategories := k8sutils.CategorizePodsByConditionType(logger, existingPods)

	// mutate PodClique.Status.CurrentPodTemplateHash and PodClique.Status.CurrentPodCliqueSetGenerationHash
	if err = mutateCurrentHashes(logger, pcs, pclq); err != nil {
		logger.Error(err, "failed to compute PodClique current hashes")
		return ctrlcommon.ReconcileWithErrors("failed to compute PodClique current hashes", err)
	}
	// mutate PodClique Status Replicas, ReadyReplicas, ScheduleGatedReplicas and UpdatedReplicas.
	mutateReplicas(pclq, podCategories, len(existingPods))
	mutateUpdatedReplica(pclq, existingPods)

	// mutate the conditions only if the PodClique has been successfully reconciled at least once.
	// This prevents prematurely setting incorrect conditions.
	if pclq.Status.ObservedGeneration != nil {
		mutatePodCliqueScheduledCondition(pclq)
		mutateMinAvailableBreachedCondition(pclq,
			len(podCategories[k8sutils.PodHasAtleastOneContainerWithNonZeroExitCode]),
			len(podCategories[k8sutils.PodStartedButNotReady]))
	}

	// mutate the selector that will be used by an autoscaler.
	if err = mutateSelector(pcsName, pclq); err != nil {
		logger.Error(err, "failed to update selector for PodClique")
		return ctrlcommon.ReconcileWithErrors("failed to set selector for PodClique", err)
	}

	// update the PodClique status.
	if err := r.client.Status().Patch(ctx, pclq, patch); err != nil {
		logger.Error(err, "failed to update PodClique status")
		return ctrlcommon.ReconcileWithErrors("failed to update PodClique status", err)
	}
	return ctrlcommon.ContinueReconcile()
}

// mutateCurrentHashes updates the PodClique's current template and generation hashes when updates are complete
func mutateCurrentHashes(logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pclq *grovecorev1alpha1.PodClique) error {
	if componentutils.IsPCLQUpdateInProgress(pclq) || pclq.Status.UpdatedReplicas != pclq.Status.Replicas {
		logger.Info("PodClique is currently updating, cannot set PodCliqueSet CurrentGenerationHash yet")
		return nil
	}
	if pclq.Status.RollingUpdateProgress == nil {
		expectedPodTemplateHash, err := componentutils.GetExpectedPCLQPodTemplateHash(pcs, pclq.ObjectMeta)
		if err != nil {
			return err
		}
		if pclq.Status.CurrentPodTemplateHash == nil || *pclq.Status.CurrentPodTemplateHash == expectedPodTemplateHash {
			pclq.Status.CurrentPodTemplateHash = ptr.To(expectedPodTemplateHash)
			pclq.Status.CurrentPodCliqueSetGenerationHash = pcs.Status.CurrentGenerationHash
		}
	} else if componentutils.IsLastPCLQUpdateCompleted(pclq) {
		logger.Info("PodClique update has completed, setting CurrentPodCliqueSetGenerationHash")
		pclq.Status.CurrentPodTemplateHash = ptr.To(pclq.Status.RollingUpdateProgress.PodTemplateHash)
		pclq.Status.CurrentPodCliqueSetGenerationHash = ptr.To(pclq.Status.RollingUpdateProgress.PodCliqueSetGenerationHash)
	}
	return nil
}

// mutateReplicas updates the PodClique status with current replica counts based on pod categorization
func mutateReplicas(pclq *grovecorev1alpha1.PodClique, podCategories map[corev1.PodConditionType][]*corev1.Pod, numExistingPods int) {
	// mutate the PCLQ status with current number of schedule gated, ready pods and updated pods.
	numNonTerminatingPods := int32(numExistingPods - len(podCategories[k8sutils.TerminatingPod]))
	pclq.Status.Replicas = numNonTerminatingPods
	pclq.Status.ReadyReplicas = int32(len(podCategories[corev1.PodReady]))
	pclq.Status.ScheduleGatedReplicas = int32(len(podCategories[k8sutils.ScheduleGatedPod]))
	pclq.Status.ScheduledReplicas = int32(len(podCategories[corev1.PodScheduled]))
}

// mutateUpdatedReplica calculates and sets the number of pods with the expected template hash
func mutateUpdatedReplica(pclq *grovecorev1alpha1.PodClique, existingPods []*corev1.Pod) {
	var expectedPodTemplateHash string
	// If the PCLQ update is in progress then take the expected PodTemplateHash from the PodClique.Status.RollingUpdateProgress.PodTemplateHash field
	// else take it from the PodClique.Status.CurrentPodTemplateHash field
	if componentutils.IsPCLQUpdateInProgress(pclq) {
		expectedPodTemplateHash = pclq.Status.RollingUpdateProgress.PodTemplateHash
	} else if componentutils.IsLastPCLQUpdateCompleted(pclq) {
		// Rolling update just completed (UpdateEndedAt is set) - use the new hash from RollingUpdateProgress.
		// This handles the window between markRollingUpdateEnd() and mutateCurrentHashes() where
		// CurrentPodTemplateHash hasn't been updated yet but the rolling update has finished.
		expectedPodTemplateHash = pclq.Status.RollingUpdateProgress.PodTemplateHash
	} else if pclq.Status.CurrentPodTemplateHash != nil {
		// CurrentPodTemplateHash will be set if RollingUpdateProgress is nil or if the last update has completed.
		expectedPodTemplateHash = *pclq.Status.CurrentPodTemplateHash
	}
	// If expectedPodTemplateHash is empty, it means that the PCLQ has never been successfully reconciled and therefore no pods should be considered as updated.
	// This prevents incorrectly marking all existing pods as updated when the PCLQ is first created.
	// Once the PCLQ is successfully reconciled, the expectedPodTemplateHash will be set and the updated replicas can be calculated correctly.
	if expectedPodTemplateHash != "" {
		updatedReplicas := lo.Reduce(existingPods, func(agg int, pod *corev1.Pod, _ int) int {
			if pod.Labels[apicommon.LabelPodTemplateHash] == expectedPodTemplateHash {
				return agg + 1
			}
			return agg
		}, 0)
		pclq.Status.UpdatedReplicas = int32(updatedReplicas)
	}
}

// mutateSelector creates and sets the label selector for autoscaler use when scaling is configured
func mutateSelector(pcsName string, pclq *grovecorev1alpha1.PodClique) error {
	if pclq.Spec.ScaleConfig == nil {
		return nil
	}
	labels := lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName),
		map[string]string{
			apicommon.LabelPodClique: pclq.Name,
		},
	)
	selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{MatchLabels: labels})
	if err != nil {
		return fmt.Errorf("%w: failed to create label selector for PodClique %v", err, client.ObjectKeyFromObject(pclq))
	}
	pclq.Status.Selector = ptr.To(selector.String())
	return nil
}

// mutateMinAvailableBreachedCondition updates the MinAvailableBreached condition based on pod availability
func mutateMinAvailableBreachedCondition(pclq *grovecorev1alpha1.PodClique, numNotReadyPodsWithContainersInError, numPodsStartedButNotReady int) {
	newCondition := computeMinAvailableBreachedCondition(pclq, numNotReadyPodsWithContainersInError, numPodsStartedButNotReady)
	if k8sutils.HasConditionChanged(pclq.Status.Conditions, newCondition) {
		meta.SetStatusCondition(&pclq.Status.Conditions, newCondition)
	}
}

// computeMinAvailableBreachedCondition calculates the MinAvailableBreached condition status based on pod availability
func computeMinAvailableBreachedCondition(pclq *grovecorev1alpha1.PodClique, numPodsHavingAtleastOneContainerWithNonZeroExitCode, numPodsStartedButNotReady int) metav1.Condition {
	if componentutils.IsPCLQUpdateInProgress(pclq) {
		return metav1.Condition{
			Type:    constants.ConditionTypeMinAvailableBreached,
			Status:  metav1.ConditionUnknown,
			Reason:  constants.ConditionReasonUpdateInProgress,
			Message: "Update is in progress",
		}
	}
	// dereferencing is considered safe as MinAvailable will always be set by the defaulting webhook. If this changes in the future,
	// make sure that you check for nil explicitly.
	minAvailable := int(*pclq.Spec.MinAvailable)
	scheduledReplicas := int(pclq.Status.ScheduledReplicas)
	now := metav1.Now()

	// If the number of scheduled pods is less than the minimum available, then minAvailable is not considered as breached.
	// Consider a case where none of the PodCliques have been scheduled yet, then it should not cause the PodGang to be recreated all the time.
	if scheduledReplicas < minAvailable {
		return metav1.Condition{
			Type:               constants.ConditionTypeMinAvailableBreached,
			Status:             metav1.ConditionFalse,
			Reason:             constants.ConditionReasonInsufficientScheduledPods,
			Message:            fmt.Sprintf("Insufficient scheduled pods. expected at least: %d, found: %d", minAvailable, scheduledReplicas),
			LastTransitionTime: now,
		}
	}

	readyOrStartingPods := scheduledReplicas - numPodsHavingAtleastOneContainerWithNonZeroExitCode - numPodsStartedButNotReady
	// pclq.Status.ReadyReplicas do not account for Pods which are not yet ready and are in the process of starting/initializing.
	// This allows sufficient time specially for pods that have long-running init containers or slow-to-start main containers.
	// Therefore, we take Pods that are NotReady and at least one of their containers have exited with a non-zero exit code. Kubelet
	// has attempted to start the containers within the Pod at least once and failed. These pods count towards unavailability.
	if readyOrStartingPods < minAvailable {
		return metav1.Condition{
			Type:               constants.ConditionTypeMinAvailableBreached,
			Status:             metav1.ConditionTrue,
			Reason:             constants.ConditionReasonInsufficientReadyPods,
			Message:            fmt.Sprintf("Insufficient ready or starting pods. expected at least: %d, found: %d", minAvailable, readyOrStartingPods),
			LastTransitionTime: now,
		}
	}
	return metav1.Condition{
		Type:               constants.ConditionTypeMinAvailableBreached,
		Status:             metav1.ConditionFalse,
		Reason:             constants.ConditionReasonSufficientReadyPods,
		Message:            fmt.Sprintf("Either sufficient ready or starting pods found. expected at least: %d, found: %d", minAvailable, readyOrStartingPods),
		LastTransitionTime: now,
	}
}

// mutatePodCliqueScheduledCondition updates the PodCliqueScheduled condition based on scheduled pod counts
func mutatePodCliqueScheduledCondition(pclq *grovecorev1alpha1.PodClique) {
	newCondition := computePodCliqueScheduledCondition(pclq)
	if k8sutils.HasConditionChanged(pclq.Status.Conditions, newCondition) {
		meta.SetStatusCondition(&pclq.Status.Conditions, newCondition)
	}
}

// computePodCliqueScheduledCondition calculates the PodCliqueScheduled condition based on minimum availability requirements
func computePodCliqueScheduledCondition(pclq *grovecorev1alpha1.PodClique) metav1.Condition {
	now := metav1.Now()
	if pclq.Status.ScheduledReplicas < *pclq.Spec.MinAvailable {
		return metav1.Condition{
			Type:               constants.ConditionTypePodCliqueScheduled,
			Status:             metav1.ConditionFalse,
			Reason:             constants.ConditionReasonInsufficientScheduledPods,
			Message:            fmt.Sprintf("Insufficient scheduled pods. expected at least: %d, found: %d", *pclq.Spec.MinAvailable, pclq.Status.ScheduledReplicas),
			LastTransitionTime: now,
		}
	}
	return metav1.Condition{
		Type:               constants.ConditionTypePodCliqueScheduled,
		Status:             metav1.ConditionTrue,
		Reason:             constants.ConditionReasonSufficientScheduledPods,
		Message:            fmt.Sprintf("Sufficient scheduled pods found. expected at least: %d, found: %d", *pclq.Spec.MinAvailable, pclq.Status.ScheduledReplicas),
		LastTransitionTime: now,
	}
}

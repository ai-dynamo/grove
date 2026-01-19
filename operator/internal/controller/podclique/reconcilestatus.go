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
		// mutate WasAvailable before MinAvailableBreached condition as it's used in the breach calculation
		mutateWasAvailable(pclq)
		mutateMinAvailableBreachedCondition(pclq)
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
	// If RollingUpdateProgress exists (update in progress or recently completed), use the target hash from it.
	// This covers both the active update phase and the window after completion before CurrentPodTemplateHash is synced.
	if pclq.Status.RollingUpdateProgress != nil {
		expectedPodTemplateHash = pclq.Status.RollingUpdateProgress.PodTemplateHash
	} else if pclq.Status.CurrentPodTemplateHash != nil {
		// Steady state: no rolling update tracking exists.
		// Use the stable current hash for pods that have been reconciled.
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

// mutateWasAvailable updates the WasAvailable status field based on current pod availability.
// Once set to true, it remains true for the lifetime of the PodClique (sticky bit).
// This field is used to determine if a PodClique can be considered in breach of MinAvailable -
// a PodClique must have been available at least once before it can be considered breached.
func mutateWasAvailable(pclq *grovecorev1alpha1.PodClique) {
	// Once WasAvailable is true, it stays true
	if pclq.Status.WasAvailable {
		return
	}
	// Don't set WasAvailable during rolling updates to avoid false positives
	if componentutils.IsPCLQUpdateInProgress(pclq) {
		return
	}
	// Check if current ready replicas meets or exceeds MinAvailable threshold
	minAvailable := int32(0)
	if pclq.Spec.MinAvailable != nil {
		minAvailable = *pclq.Spec.MinAvailable
	}
	if pclq.Status.ReadyReplicas >= minAvailable {
		pclq.Status.WasAvailable = true
	}
}

// mutateMinAvailableBreachedCondition updates the MinAvailableBreached condition based on pod availability
func mutateMinAvailableBreachedCondition(pclq *grovecorev1alpha1.PodClique) {
	newCondition := computeMinAvailableBreachedCondition(pclq)
	if k8sutils.HasConditionChanged(pclq.Status.Conditions, newCondition) {
		meta.SetStatusCondition(&pclq.Status.Conditions, newCondition)
	}
}

// computeMinAvailableBreachedCondition calculates the MinAvailableBreached condition status based on pod availability.
// The breach calculation considers:
// 1. Rolling update status - during updates, condition is Unknown to prevent false terminations
// 2. WasAvailable status - a PodClique must have been available once before it can be considered breached
// 3. Only ready pods count toward availability - starting pods do NOT count
func computeMinAvailableBreachedCondition(pclq *grovecorev1alpha1.PodClique) metav1.Condition {
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
	now := metav1.Now()

	// Only ready pods count toward availability for breach detection.
	// Starting pods (pods that have started but aren't ready yet) do NOT count.
	// This ensures that a PodClique is considered in breach as soon as ready pods
	// drop below MinAvailable, regardless of how many pods are still starting.
	readyReplicas := int(pclq.Status.ReadyReplicas)

	// If we have sufficient ready pods, there's no breach - no need to check WasAvailable
	if readyReplicas >= minAvailable {
		return metav1.Condition{
			Type:               constants.ConditionTypeMinAvailableBreached,
			Status:             metav1.ConditionFalse,
			Reason:             constants.ConditionReasonSufficientReadyPods,
			Message:            fmt.Sprintf("Sufficient ready pods found. Expected at least: %d, found: %d", minAvailable, readyReplicas),
			LastTransitionTime: now,
		}
	}

	// At this point, readyReplicas < minAvailable. Check if this is a real breach or just startup.
	// A PodClique can only be considered in breach of MinAvailable if it was previously available.
	// This prevents false breach detection during initial startup when pods are still being created/scheduled.
	if !pclq.Status.WasAvailable {
		return metav1.Condition{
			Type:               constants.ConditionTypeMinAvailableBreached,
			Status:             metav1.ConditionFalse,
			Reason:             constants.ConditionReasonNeverAvailable,
			Message:            fmt.Sprintf("PodClique has never reached MinAvailable threshold of %d, cannot be considered breached", minAvailable),
			LastTransitionTime: now,
		}
	}

	// WasAvailable is true and readyReplicas < minAvailable - this is a real breach
	return metav1.Condition{
		Type:               constants.ConditionTypeMinAvailableBreached,
		Status:             metav1.ConditionTrue,
		Reason:             constants.ConditionReasonInsufficientReadyPods,
		Message:            fmt.Sprintf("Insufficient ready pods. Expected at least: %d, found: %d", minAvailable, readyReplicas),
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

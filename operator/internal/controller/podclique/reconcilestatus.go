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

// minAvailableBreachedContext contains the state needed for computing the MinAvailableBreached
// condition. This ensures the breach condition is only set to True when transitioning from
// available to unavailable, preventing false breach detection during initial startup when
// pods haven't reached MinAvailable yet.
type minAvailableBreachedContext struct {
	// oldReadyReplicas is the ReadyReplicas count before the current reconciliation mutation
	oldReadyReplicas int32
	// newReadyReplicas is the ReadyReplicas count after the current reconciliation mutation
	newReadyReplicas int32
	// minAvailable is the minimum number of ready pods required for availability
	minAvailable int32
	// existingCondition is the persisted MinAvailableBreached condition (nil if not set)
	existingCondition *metav1.Condition
	// isUpdateInProgress indicates whether a rolling update is currently in progress
	isUpdateInProgress bool
}

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

	// Capture old state BEFORE mutation for MinAvailableBreached detection
	oldReadyReplicas := pclq.Status.ReadyReplicas
	existingBreachCondition := meta.FindStatusCondition(pclq.Status.Conditions, constants.ConditionTypeMinAvailableBreached)

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

		// Build context for MinAvailableBreached condition computation
		breachCtx := minAvailableBreachedContext{
			oldReadyReplicas:   oldReadyReplicas,
			newReadyReplicas:   pclq.Status.ReadyReplicas,
			minAvailable:       *pclq.Spec.MinAvailable,
			existingCondition:  existingBreachCondition,
			isUpdateInProgress: componentutils.IsPCLQUpdateInProgress(pclq),
		}
		mutateMinAvailableBreachedCondition(pclq, breachCtx)
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

// mutateMinAvailableBreachedCondition updates the MinAvailableBreached condition based on pod availability
// using transition detection to distinguish between "never available" and "was available then degraded" scenarios.
func mutateMinAvailableBreachedCondition(pclq *grovecorev1alpha1.PodClique, ctx minAvailableBreachedContext) {
	newCondition := computeMinAvailableBreachedCondition(ctx)
	if k8sutils.HasConditionChanged(pclq.Status.Conditions, newCondition) {
		meta.SetStatusCondition(&pclq.Status.Conditions, newCondition)
	}
}

// computeMinAvailableBreachedCondition calculates the MinAvailableBreached condition status using transition detection.
//
// MinAvailableBreached Detection Logic:
// The condition is only set to True when transitioning from available to unavailable.
// This prevents false breach detection during initial startup when pods haven't reached MinAvailable yet.
//
// The logic handles three categories:
//  1. Rolling update in progress → Unknown (no breach detection during updates)
//  2. Currently available (readyReplicas >= minAvailable) → False (clear any previous breach)
//  3. Currently unavailable (readyReplicas < minAvailable):
//     a. Already breached (persisted condition is True) → True (respect persisted state)
//     b. Breach detected (old >= min, new < min) → True (breach just occurred)
//     c. No transition, never available → False with NeverAvailable reason
//
// This approach ensures:
// - Workloads that were healthy then degraded will be terminated (gang termination)
// - Workloads that never achieved availability will NOT be terminated
// - Operator restarts preserve breach state (respects persisted MinAvailableBreached=True)
func computeMinAvailableBreachedCondition(ctx minAvailableBreachedContext) metav1.Condition {
	// 1. Rolling update → Unknown (no breach detection during updates)
	if ctx.isUpdateInProgress {
		return metav1.Condition{
			Type:    constants.ConditionTypeMinAvailableBreached,
			Status:  metav1.ConditionUnknown,
			Reason:  constants.ConditionReasonUpdateInProgress,
			Message: "Update is in progress",
		}
	}

	// 2. Currently available → False (clear any previous breach)
	if ctx.newReadyReplicas >= ctx.minAvailable {
		return metav1.Condition{
			Type:    constants.ConditionTypeMinAvailableBreached,
			Status:  metav1.ConditionFalse,
			Reason:  constants.ConditionReasonSufficientReadyPods,
			Message: fmt.Sprintf("Sufficient ready pods: %d >= %d", ctx.newReadyReplicas, ctx.minAvailable),
		}
	}

	// 3. Currently unavailable (newReadyReplicas < minAvailable)

	// 3a. Already breached (persisted condition is True) → respect persisted state (keep True)
	// This handles operator restarts: if we previously detected a breach, we continue respecting it.
	if ctx.existingCondition != nil && ctx.existingCondition.Status == metav1.ConditionTrue {
		return metav1.Condition{
			Type:    constants.ConditionTypeMinAvailableBreached,
			Status:  metav1.ConditionTrue,
			Reason:  constants.ConditionReasonInsufficientReadyPods,
			Message: fmt.Sprintf("Insufficient ready pods: %d < %d", ctx.newReadyReplicas, ctx.minAvailable),
		}
	}

	// 3b. Breach detected → set True (breach just occurred)
	// A transition is when we go from available to unavailable in this reconciliation cycle.
	if ctx.oldReadyReplicas >= ctx.minAvailable {
		return metav1.Condition{
			Type:    constants.ConditionTypeMinAvailableBreached,
			Status:  metav1.ConditionTrue,
			Reason:  constants.ConditionReasonInsufficientReadyPods,
			Message: fmt.Sprintf("Ready pods dropped below minimum: %d -> %d (min: %d)", ctx.oldReadyReplicas, ctx.newReadyReplicas, ctx.minAvailable),
		}
	}

	// 3c. No breach detected, never reached availability → False with NeverAvailable reason
	// This prevents false breach detection during startup when pods haven't reached MinAvailable yet.
	return metav1.Condition{
		Type:    constants.ConditionTypeMinAvailableBreached,
		Status:  metav1.ConditionFalse,
		Reason:  constants.ConditionReasonNeverAvailable,
		Message: fmt.Sprintf("PodClique has not yet reached MinAvailable threshold of %d (current: %d)", ctx.minAvailable, ctx.newReadyReplicas),
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

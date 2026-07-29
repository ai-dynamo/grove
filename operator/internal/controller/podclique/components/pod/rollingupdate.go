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

package pod

import (
	"context"
	"fmt"
	"slices"

	"github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// updateWork encapsulates the information needed to perform a rolling update of pods in a PodClique.
type updateWork struct {
	oldTemplateHashPendingPods       []*corev1.Pod // pods with old hash still in Pending phase
	oldTemplateHashUnhealthyPods     []*corev1.Pod // pods with old hash that started but are not ready or exited erroneously
	oldTemplateHashStartingPods      []*corev1.Pod // pods with old hash whose containers have not yet passed startup probe
	oldTemplateHashUncategorizedPods []*corev1.Pod // pods with old hash in an unrecognized state
	oldTemplateHashReadyPods         []*corev1.Pod // pods with old hash that are fully ready and serving traffic
	newTemplateHashReadyPods         []*corev1.Pod // pods with new hash that are fully ready
	newTemplateHashNonReadyPods      []*corev1.Pod // pods with new hash that are still being created or becoming ready
}

// getPodNamesPendingUpdate returns names of pods with old template hash that are not already being deleted
func (w *updateWork) getPodNamesPendingUpdate(deletionExpectedPodUIDs []types.UID) []string {
	allOldPods := lo.Union(w.oldTemplateHashPendingPods, w.oldTemplateHashUnhealthyPods, w.oldTemplateHashStartingPods, w.oldTemplateHashUncategorizedPods, w.oldTemplateHashReadyPods)
	deletionExpectedPodUIDSet := componentutils.NewSet(deletionExpectedPodUIDs)
	return lo.FilterMap(allOldPods, func(pod *corev1.Pod, _ int) (string, bool) {
		if deletionExpectedPodUIDSet.Has(pod.UID) {
			return "", false
		}
		return pod.Name, true
	})
}

// getNextPodToUpdate selects the next ready pod with old template hash to update, prioritizing oldest pods first
func (w *updateWork) getNextPodToUpdate() *corev1.Pod {
	if len(w.oldTemplateHashReadyPods) > 0 {
		slices.SortFunc(w.oldTemplateHashReadyPods, func(a, b *corev1.Pod) int {
			return a.CreationTimestamp.Compare(b.CreationTimestamp.Time)
		})
		return w.oldTemplateHashReadyPods[0]
	}
	return nil
}

// processPendingUpdates processes pending updates for the PodClique.
// This is the main entry point for handling rolling updates of pods in the PodClique.
func (r _resource) processPendingUpdates(logger logr.Logger, sc *syncContext) error {
	updateWork := r.computeUpdateWork(logger, sc)
	pclq := sc.pclq
	budget, err := r.getRollingUpdateBudget(sc)
	if err != nil {
		return groveerr.WrapError(err,
			errCodeDeletePod,
			component.OperationSync,
			fmt.Sprintf("failed to evaluate rolling-update budget for PodClique %v", client.ObjectKeyFromObject(pclq)),
		)
	}
	if budget.blocked() {
		// Replacing an old-hash Pod that is already unavailable does not make
		// another Pod unavailable. Allow that repair to proceed so a later
		// configuration change can recover a stuck Pod without opening a new
		// availability slot. Once its replacement is in flight, wait for it to
		// become Ready before repairing or updating another Pod.
		if !r.hasPodRepairInFlight(sc, updateWork) {
			repairedPods, repairErr := r.deleteOldNonReadyPods(logger, sc, updateWork, budget.limit)
			if repairErr != nil {
				return repairErr
			}
			if repairedPods > 0 {
				return groveerr.New(
					groveerr.ErrCodeContinueReconcileAndRequeue,
					component.OperationSync,
					fmt.Sprintf("recreated %d unavailable old-hash Pod(s) without consuming another rolling-update slot, requeuing", repairedPods),
				)
			}
		}
		return groveerr.New(
			groveerr.ErrCodeContinueReconcileAndRequeue,
			component.OperationSync,
			fmt.Sprintf("rolling-update deletion blocked for PodClique %v: reason=%s unavailable=%d limit=%d",
				client.ObjectKeyFromObject(pclq), budget.reason, budget.unavailable, budget.limit),
		)
	}

	// Prefer deleting old-hash pods that are not Ready (pending, unhealthy, starting, or uncategorized)
	// when the configured rolling-update budget permits.
	deletedNonReadyPods, err := r.deleteOldNonReadyPods(logger, sc, updateWork, budget.allowed)
	if err != nil {
		return err
	}
	if budget.enabled && deletedNonReadyPods > 0 {
		return groveerr.New(
			groveerr.ErrCodeContinueReconcileAndRequeue,
			component.OperationSync,
			fmt.Sprintf("deleted %d non-ready Pod(s) within the rolling-update budget, requeuing", deletedNonReadyPods),
		)
	}

	// Check if there is currently a pod that is selected for update and its update has not yet completed.
	if isAnyReadyPodSelectedForUpdate(pclq) && !isCurrentPodUpdateComplete(sc, updateWork) {
		if isCurrentPodUpdateSupersededByScaleIn(sc, updateWork) {
			supersededPodName := pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate.Current
			if err := r.resetReadyPodsSelectedToUpdate(sc.ctx, logger, pclq); err != nil {
				return err
			}
			return groveerr.New(
				groveerr.ErrCodeContinueReconcileAndRequeue,
				component.OperationSync,
				fmt.Sprintf("reset rolling-update selection for Pod %s because scale-in removed its replacement, requeuing", supersededPodName),
			)
		}
		return groveerr.New(
			groveerr.ErrCodeContinueReconcileAndRequeue,
			component.OperationSync,
			fmt.Sprintf("rolling update of currently selected Pod: %s is not complete, requeuing", pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate.Current),
		)
	}

	// If we are here, then it means that either no ready pod has been selected for update or the current ready pod update is complete.
	// In either of these cases we should pick up next pod to update if there are any pending pods to update.
	var nextPodToUpdate *corev1.Pod
	if podNamesPendingUpdate := updateWork.getPodNamesPendingUpdate(r.expectationsStore.GetDeleteExpectations(sc.pclqExpectationsStoreKey)); len(podNamesPendingUpdate) > 0 {
		if pclq.Status.ReadyReplicas < *pclq.Spec.MinAvailable {
			return groveerr.New(
				groveerr.ErrCodeContinueReconcileAndRequeue,
				component.OperationSync,
				fmt.Sprintf("ready replicas %d lesser than minAvailable %d, requeuing", pclq.Status.ReadyReplicas, *pclq.Spec.MinAvailable),
			)
		}
		nextPodToUpdate = updateWork.getNextPodToUpdate()
	}

	// If there is next pod to update then trigger the update of this pod by first triggering its deletion followed by a requeue.
	if nextPodToUpdate != nil {
		nextPodToUpdateObjectKey := client.ObjectKeyFromObject(nextPodToUpdate)
		logger.Info("Selected nextPodToUpdate", "pod", nextPodToUpdateObjectKey)
		// update the status
		if err := r.updatePCLQStatusWithNextPodToUpdate(sc.ctx, logger, sc.pclq, nextPodToUpdate.Name); err != nil {
			return err
		}

		// trigger deletion of nextPodToUpdate
		deletionTask := r.createPodDeletionTask(logger, pclq, nextPodToUpdate, sc.pclqExpectationsStoreKey)
		if err := deletionTask.Fn(sc.ctx); err != nil {
			return groveerr.WrapError(
				err,
				errCodeDeletePod,
				component.OperationSync,
				fmt.Sprintf("failed to delete pod %s selected for update", nextPodToUpdateObjectKey),
			)
		}
		// requeue
		return groveerr.New(
			groveerr.ErrCodeContinueReconcileAndRequeue,
			component.OperationSync,
			fmt.Sprintf("deleted pod %s selected for rolling update, requeuing", nextPodToUpdateObjectKey),
		)
	}

	// If the control comes here, then mark the end of update.
	return r.markRollingUpdateEnd(sc.ctx, logger, pclq)
}

// computeUpdateWork categorizes pods by template hash and state.
// Old-hash pods: Pending, Unhealthy, Starting, Uncategorized, or Ready.
// New-hash pods: Ready or non-Ready.
func (r _resource) computeUpdateWork(logger logr.Logger, sc *syncContext) *updateWork {
	work := &updateWork{}
	for _, pod := range sc.existingPCLQPods {
		if pod.Labels[common.LabelPodTemplateHash] != sc.expectedPodTemplateHash {
			// Old-hash pod — skip if deletion already in flight.
			if r.hasPodDeletionBeenTriggered(sc, pod) {
				logger.Info("skipping old Pod since its deletion has already been triggered", "pod", client.ObjectKeyFromObject(pod))
				continue
			}
			// Pending, unhealthy, starting, and uncategorized pods are deleted immediately;
			// ready pods are queued for ordered one-at-a-time replacement.
			switch {
			case k8sutils.IsPodPending(pod):
				work.oldTemplateHashPendingPods = append(work.oldTemplateHashPendingPods, pod)
			case k8sutils.HasAnyStartedButNotReadyContainer(pod) || k8sutils.HasAnyContainerExitedErroneously(logger, pod):
				work.oldTemplateHashUnhealthyPods = append(work.oldTemplateHashUnhealthyPods, pod)
			case k8sutils.IsPodReady(pod):
				work.oldTemplateHashReadyPods = append(work.oldTemplateHashReadyPods, pod)
			case k8sutils.HasAnyContainerNotStarted(pod):
				work.oldTemplateHashStartingPods = append(work.oldTemplateHashStartingPods, pod)
			default:
				work.oldTemplateHashUncategorizedPods = append(work.oldTemplateHashUncategorizedPods, pod)
			}
		} else {
			if k8sutils.IsPodReady(pod) {
				work.newTemplateHashReadyPods = append(work.newTemplateHashReadyPods, pod)
			} else {
				work.newTemplateHashNonReadyPods = append(work.newTemplateHashNonReadyPods, pod)
			}
		}
	}
	return work
}

// hasPodRepairInFlight reports whether an unavailable slot is already being
// replaced. In that case another old unavailable Pod must not be deleted,
// even though deleting it would not reduce the currently available count.
func (r _resource) hasPodRepairInFlight(sc *syncContext, work *updateWork) bool {
	if len(work.newTemplateHashNonReadyPods) > 0 ||
		len(r.expectationsStore.GetDeleteExpectations(sc.pclqExpectationsStoreKey)) > 0 ||
		len(r.expectationsStore.GetCreateExpectations(sc.pclqExpectationsStoreKey)) > 0 {
		return true
	}
	return lo.SomeBy(sc.existingPCLQPods, func(pod *corev1.Pod) bool {
		return k8sutils.IsResourceTerminating(pod.ObjectMeta)
	})
}

// hasPodDeletionBeenTriggered checks if a pod is already terminating or has a delete expectation recorded
func (r _resource) hasPodDeletionBeenTriggered(sc *syncContext, pod *corev1.Pod) bool {
	return k8sutils.IsResourceTerminating(pod.ObjectMeta) || r.expectationsStore.HasDeleteExpectation(sc.pclqExpectationsStoreKey, pod.GetUID())
}

// deleteOldNonReadyPods removes up to maxDeletions old-hash pods that are not Ready: pending, unhealthy,
// starting (startup probe), or uncategorized (unknown state). These pods are preferred over Ready pods to avoid
// disrupting healthy serving capacity.
func (r _resource) deleteOldNonReadyPods(
	logger logr.Logger,
	sc *syncContext,
	work *updateWork,
	maxDeletions int,
) (int, error) {
	if len(work.oldTemplateHashUncategorizedPods) > 0 {
		logger.Info("found old-hash pods in an unrecognized state, deleting them",
			"unexpected", true,
			"pods", componentutils.PodsToObjectNames(work.oldTemplateHashUncategorizedPods))
	}

	podsToDelete := lo.Union(work.oldTemplateHashPendingPods, work.oldTemplateHashUnhealthyPods, work.oldTemplateHashStartingPods, work.oldTemplateHashUncategorizedPods)
	podsToDelete = podsToDelete[:min(len(podsToDelete), maxDeletions)]
	deletionTasks := r.createPodDeletionTasks(logger, sc.pclq, podsToDelete, sc.pclqExpectationsStoreKey)

	if len(deletionTasks) == 0 {
		logger.Info("no non-ready pods having old PodTemplateHash found")
		return 0, nil
	}

	logger.Info("triggering deletion of non-ready pods with old pod template hash in order to update",
		"oldPendingPods", componentutils.PodsToObjectNames(work.oldTemplateHashPendingPods),
		"oldUnhealthyPods", componentutils.PodsToObjectNames(work.oldTemplateHashUnhealthyPods),
		"oldStartingPods", componentutils.PodsToObjectNames(work.oldTemplateHashStartingPods),
		"oldUncategorizedPods", componentutils.PodsToObjectNames(work.oldTemplateHashUncategorizedPods))
	runResult := utils.RunConcurrently(sc.ctx, logger, deletionTasks)
	if runResult.HasErrors() {
		err := runResult.GetAggregatedError()
		pclqObjectKey := client.ObjectKeyFromObject(sc.pclq)
		logger.Error(err, "failed to delete pods for PCLQ", "runSummary", runResult.GetSummary())
		return len(runResult.SuccessfulTasks), groveerr.WrapError(err,
			errCodeDeletePod,
			component.OperationSync,
			fmt.Sprintf("failed to delete Pods for PodClique %v", pclqObjectKey),
		)
	}
	logger.Info("successfully deleted non-ready pods having old PodTemplateHash")
	return len(runResult.SuccessfulTasks), nil
}

// isAnyReadyPodSelectedForUpdate checks if there is currently a ready pod selected for rolling update
func isAnyReadyPodSelectedForUpdate(pclq *grovecorev1alpha1.PodClique) bool {
	return pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate != nil && pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate.Current != ""
}

// isCurrentPodUpdateComplete checks if the currently updating pod has completed its update.
// The update of the currently updating pod is considered complete if either the pod does not exist anymore
// or if the number of ready pods with new PodTemplateHash is greater than or equal to the number of pods
// that have been selected for update (including the currently updating pod).
func isCurrentPodUpdateComplete(sc *syncContext, work *updateWork) bool {
	// Get the pod corresponding to the currently updating pod. If the pod exists and still does not have a deletion timestamp
	// then the current update is not complete
	currentlyUpdatingPodName := sc.pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate.Current
	pod, ok := lo.Find(sc.existingPCLQPods, func(pod *corev1.Pod) bool {
		return currentlyUpdatingPodName == pod.Name
	})
	if ok && !k8sutils.IsResourceTerminating(pod.ObjectMeta) {
		return false
	}

	// Also verify count as a sanity check
	podsSelectedToUpdate := len(sc.pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate.Completed) + 1
	return len(work.newTemplateHashReadyPods) >= podsSelectedToUpdate
}

// isCurrentPodUpdateSupersededByScaleIn detects a selected rolling-update step
// whose replacement is no longer part of the reduced desired replica set.
func isCurrentPodUpdateSupersededByScaleIn(sc *syncContext, work *updateWork) bool {
	selected := sc.pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate
	if selected == nil || selected.Current == "" {
		return false
	}

	if _, selectedPodStillExists := lo.Find(sc.existingPCLQPods, func(pod *corev1.Pod) bool {
		return pod.Name == selected.Current
	}); selectedPodStillExists {
		return false
	}

	podsSelectedToUpdate := len(selected.Completed) + 1
	if len(work.newTemplateHashReadyPods) >= podsSelectedToUpdate {
		return false
	}

	desired := int(sc.pclq.Spec.Replicas)
	if desired <= 0 || len(sc.existingPCLQPods) != desired {
		return false
	}
	return lo.EveryBy(sc.existingPCLQPods, func(pod *corev1.Pod) bool {
		return pod.Status.Phase == corev1.PodRunning &&
			k8sutils.IsPodReady(pod) &&
			!k8sutils.IsResourceTerminating(pod.ObjectMeta)
	})
}

func (r _resource) resetReadyPodsSelectedToUpdate(ctx context.Context, logger logr.Logger, pclq *grovecorev1alpha1.PodClique) error {
	patch := client.MergeFrom(pclq.DeepCopy())
	pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate = nil

	if err := client.IgnoreNotFound(r.client.Status().Patch(ctx, pclq, patch)); err != nil {
		return groveerr.WrapError(err,
			errCodeUpdatePodCliqueStatus,
			component.OperationSync,
			fmt.Sprintf("failed to reset ready Pod selection in status of PodClique: %v", client.ObjectKeyFromObject(pclq)),
		)
	}
	logger.Info("reset ready Pod selection after scale-in superseded the current rolling-update step")
	return nil
}

// updatePCLQStatusWithNextPodToUpdate updates the PodClique status to track the next pod selected for rolling update
func (r _resource) updatePCLQStatusWithNextPodToUpdate(ctx context.Context, logger logr.Logger, pclq *grovecorev1alpha1.PodClique, nextPodToUpdate string) error {
	patch := client.MergeFrom(pclq.DeepCopy())

	if pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate == nil {
		pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate = &grovecorev1alpha1.PodsSelectedToUpdate{}
	} else {
		pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate.Completed = append(pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate.Completed, pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate.Current)
	}
	pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate.Current = nextPodToUpdate

	if err := client.IgnoreNotFound(r.client.Status().Patch(ctx, pclq, patch)); err != nil {
		return groveerr.WrapError(err,
			errCodeUpdatePodCliqueStatus,
			component.OperationSync,
			fmt.Sprintf("failed to update new ready pod selected to update in status of PodClique: %v", client.ObjectKeyFromObject(pclq)),
		)
	}
	logger.Info("updated pclq status with new ready pod selected to update", "nextPodToUpdate", nextPodToUpdate)
	return nil
}

// markRollingUpdateEnd marks the completion of the rolling update by setting the end timestamp and clearing selected pods
func (r _resource) markRollingUpdateEnd(ctx context.Context, logger logr.Logger, pclq *grovecorev1alpha1.PodClique) error {
	patch := client.MergeFrom(pclq.DeepCopy())

	pclq.Status.UpdateProgress.UpdateEndedAt = ptr.To(metav1.Now())
	pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate = nil

	if err := client.IgnoreNotFound(r.client.Status().Patch(ctx, pclq, patch)); err != nil {
		return groveerr.WrapError(err,
			errCodeUpdatePodCliqueStatus,
			component.OperationSync,
			fmt.Sprintf("failed to mark the end of rolling update in status of PodClique: %v", client.ObjectKeyFromObject(pclq)),
		)
	}
	logger.Info("Marked the end of rolling update of PodClique")
	return nil
}

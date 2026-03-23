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
}

// getPodNamesPendingUpdate returns names of pods with old template hash that are not already being deleted
func (w *updateWork) getPodNamesPendingUpdate(deletionExpectedPodUIDs []types.UID) []string {
	allOldPods := lo.Union(w.oldTemplateHashPendingPods, w.oldTemplateHashUnhealthyPods, w.oldTemplateHashStartingPods, w.oldTemplateHashUncategorizedPods, w.oldTemplateHashReadyPods)
	return lo.FilterMap(allOldPods, func(pod *corev1.Pod, _ int) (string, bool) {
		if slices.Contains(deletionExpectedPodUIDs, pod.UID) {
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
	// Always delete old-hash pods that are not Ready (pending, unhealthy, starting, or uncategorized).
	if err := r.deleteOldNonReadyPods(logger, sc, updateWork); err != nil {
		return err
	}

	// Check if there is currently a pod that is selected for update and its update has not yet completed.
	if isAnyReadyPodSelectedForUpdate(pclq) && !isCurrentPodUpdateComplete(sc, updateWork) {
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
// New-hash pods: Ready only.
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
			// New-hash pod — only count as ready; non-ready pods are not tracked so
			// isCurrentPodUpdateComplete won't prematurely declare success.
			if k8sutils.IsPodReady(pod) {
				work.newTemplateHashReadyPods = append(work.newTemplateHashReadyPods, pod)
			}
		}
	}
	return work
}

// hasPodDeletionBeenTriggered checks if a pod is already terminating or has a delete expectation recorded
func (r _resource) hasPodDeletionBeenTriggered(sc *syncContext, pod *corev1.Pod) bool {
	return k8sutils.IsResourceTerminating(pod.ObjectMeta) || r.expectationsStore.HasDeleteExpectation(sc.pclqExpectationsStoreKey, pod.GetUID())
}

// deleteOldNonReadyPods removes old-hash pods that are not Ready: pending, unhealthy, starting (startup probe),
// or uncategorized (unknown state). All of these are safe to delete immediately since they are not serving traffic
// and will be replaced with pods having the correct template hash.
func (r _resource) deleteOldNonReadyPods(logger logr.Logger, sc *syncContext, work *updateWork) error {
	if len(work.oldTemplateHashUncategorizedPods) > 0 {
		logger.Info("found old-hash pods in an unrecognized state, deleting them",
			"unexpected", true,
			"pods", componentutils.PodsToObjectNames(work.oldTemplateHashUncategorizedPods))
	}

	podsToDelete := lo.Union(work.oldTemplateHashPendingPods, work.oldTemplateHashUnhealthyPods, work.oldTemplateHashStartingPods, work.oldTemplateHashUncategorizedPods)
	deletionTasks := r.createPodDeletionTasks(logger, sc.pclq, podsToDelete, sc.pclqExpectationsStoreKey)

	if len(deletionTasks) == 0 {
		logger.Info("no non-ready pods having old PodTemplateHash found")
		return nil
	}

	logger.Info("triggering deletion of non-ready pods with old pod template hash in order to update",
		"oldPendingPods", componentutils.PodsToObjectNames(work.oldTemplateHashPendingPods),
		"oldUnhealthyPods", componentutils.PodsToObjectNames(work.oldTemplateHashUnhealthyPods),
		"oldStartingPods", componentutils.PodsToObjectNames(work.oldTemplateHashStartingPods),
		"oldUncategorizedPods", componentutils.PodsToObjectNames(work.oldTemplateHashUncategorizedPods))
	if runResult := utils.RunConcurrently(sc.ctx, logger, deletionTasks); runResult.HasErrors() {
		err := runResult.GetAggregatedError()
		pclqObjectKey := client.ObjectKeyFromObject(sc.pclq)
		logger.Error(err, "failed to delete pods for PCLQ", "runSummary", runResult.GetSummary())
		return groveerr.WrapError(err,
			errCodeDeletePod,
			component.OperationSync,
			fmt.Sprintf("failed to delete Pods for PodClique %v", pclqObjectKey),
		)
	}
	logger.Info("successfully deleted non-ready pods having old PodTemplateHash")
	return nil
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

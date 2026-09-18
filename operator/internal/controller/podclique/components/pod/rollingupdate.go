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

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	"github.com/ai-dynamo/grove/operator/internal/controller/podclique/expectations"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// updateWork categorizes a PodClique's Pods for a rolling update and captures the counts that drive
// the disruption budget and the completion check.
type updateWork struct {
	// oldTemplateHashReadyPods are old-hash Pods that are Ready and not already being deleted. They are
	// the candidates for in-place replacement, disrupted oldest first within the budget.
	oldTemplateHashReadyPods []*corev1.Pod
	// oldTemplateHashPendingPods are old-hash Pods still in the Pending phase.
	oldTemplateHashPendingPods []*corev1.Pod
	// oldTemplateHashUnhealthyPods are old-hash Pods that started but are not Ready or exited erroneously.
	oldTemplateHashUnhealthyPods []*corev1.Pod
	// oldTemplateHashStartingPods are old-hash Pods whose containers have not yet passed the startup probe.
	oldTemplateHashStartingPods []*corev1.Pod
	// oldTemplateHashUncategorizedPods are old-hash Pods in an unrecognized state.
	oldTemplateHashUncategorizedPods []*corev1.Pod
	// newReadyPodCount is the number of new-hash Pods that are Ready and not already being deleted.
	newReadyPodCount int
	// oldHashPodsAwaitingReplacement is the number of old-hash Pods still awaiting replacement, i.e. whose
	// deletion has not yet been triggered. Terminating Pods (and Pods with a recorded delete expectation)
	// are not counted, so a Pod stuck terminating does not block completion once its replacement is Ready.
	oldHashPodsAwaitingReplacement int
}

// processPendingUpdates advances the rolling update of a PodClique by one reconcile step.
//
// It always deletes old-hash Pods that are not Ready, then, honoring both the MaxUnavailable budget
// and the MinAvailable floor, deletes a bounded number of Ready old-hash Pods so the normal create
// flow can recreate them with the expected template hash. The update completes only when no old-hash
// Pods remain and the desired number of new-hash Pods are Ready.
func (r _resource) processPendingUpdates(ctx context.Context, logger logr.Logger, ss *syncSnapshot) error {
	uw := r.computeUpdateWork(logger, ss)

	// Always delete old-hash Pods that are not Ready. They are already unavailable, so deleting them
	// does not consume the disruption budget, and they will be recreated with the expected hash.
	if err := r.deleteOldNonReadyPods(ctx, logger, ss, uw); err != nil {
		return err
	}

	desiredNumPods := int(ss.pclq.Spec.Replicas)

	// Completion is readiness-aware. End the update once no old-hash Pods are awaiting replacement and the
	// desired number of new-hash Pods are Ready. Old Pods already being deleted do not block completion, so
	// a Pod stuck terminating cannot stall the rollout, mirroring the PodCliqueScalingGroup path.
	if uw.oldHashPodsAwaitingReplacement == 0 && uw.newReadyPodCount == desiredNumPods {
		return r.markRollingUpdateEnd(ctx, logger, ss.pclq)
	}

	// No Ready old-hash Pods to disrupt this reconcile means replacements are still in flight. Requeue
	// and wait for them to become Ready.
	if len(uw.oldTemplateHashReadyPods) == 0 {
		return groveerr.New(
			groveerr.ErrCodeContinueReconcileAndRequeue,
			component.OperationSync,
			fmt.Sprintf("rolling update of PodClique %v waiting for in-flight replacement Pods to become Ready, requeuing", client.ObjectKeyFromObject(ss.pclq)),
		)
	}

	// Compute the disruption budget against the current desired count. allowedBudget is the MaxUnavailable
	// headroom for this reconcile.
	numReadyPods := len(uw.oldTemplateHashReadyPods) + uw.newReadyPodCount
	effectiveMaxUnavailable := componentutils.EffectiveMaxUnavailable(rollingUpdateConfigForPCLQ(ss), ss.pcs.Spec.UpdateStrategy.Type, *ss.pclq.Spec.MinAvailable)
	allowedBudget := componentutils.ComputeAllowedBudget(desiredNumPods, numReadyPods, effectiveMaxUnavailable)
	if allowedBudget == 0 {
		return groveerr.New(
			groveerr.ErrCodeContinueReconcileAndRequeue,
			component.OperationSync,
			fmt.Sprintf("rolling update of PodClique %v paused, disruption budget exhausted, requeuing", client.ObjectKeyFromObject(ss.pclq)),
		)
	}

	// oldTemplateHashReadyPods is non-empty and allowedBudget > 0, so this selects at least one Pod.
	podsToUpdate := selectOldestPods(uw.oldTemplateHashReadyPods, allowedBudget)
	deletionTasks := r.createPodDeletionTasks(logger, ss.pclq, podsToUpdate)
	logger.Info("triggering deletion of Ready Pods with old pod template hash for rolling update",
		"pods", componentutils.PodsToObjectNames(podsToUpdate))
	if runResult := utils.RunConcurrently(ctx, logger, deletionTasks); runResult.HasErrors() {
		err := runResult.GetAggregatedError()
		logger.Error(err, "failed to delete Ready Pods selected for rolling update", "runSummary", runResult.GetSummary())
		return groveerr.WrapError(err,
			errCodeDeletePod,
			component.OperationSync,
			fmt.Sprintf("failed to delete Ready Pods selected for rolling update of PodClique %v", client.ObjectKeyFromObject(ss.pclq)),
		)
	}
	return groveerr.New(
		groveerr.ErrCodeContinueReconcileAndRequeue,
		component.OperationSync,
		fmt.Sprintf("deleted %d Ready Pod(s) for rolling update of PodClique %v, requeuing", len(podsToUpdate), client.ObjectKeyFromObject(ss.pclq)),
	)
}

// selectOldestPods returns up to n Pods from the given slice, oldest first by creation timestamp.
func selectOldestPods(pods []*corev1.Pod, n int) []*corev1.Pod {
	if n <= 0 || len(pods) == 0 {
		return nil
	}
	slices.SortFunc(pods, func(a, b *corev1.Pod) int {
		return a.CreationTimestamp.Compare(b.CreationTimestamp.Time)
	})
	return pods[:min(n, len(pods))]
}

// rollingUpdateConfigForPCLQ returns the RollingUpdate configuration for the PodClique from its
// PodCliqueSet template, or nil when the template or the field is absent.
func rollingUpdateConfigForPCLQ(ss *syncSnapshot) *grovecorev1alpha1.RollingUpdateConfiguration {
	templateSpec := componentutils.FindPodCliqueTemplateSpecByName(ss.pcs, ss.cliqueName)
	if templateSpec == nil {
		return nil
	}
	return templateSpec.RollingUpdate
}

// computeUpdateWork categorizes Pods by template hash and state and records the counts that drive the
// disruption budget and the completion check.
// Old-hash Pods: Pending, Unhealthy, Starting, Uncategorized, or Ready. New-hash Pods: Ready only.
func (r _resource) computeUpdateWork(logger logr.Logger, ss *syncSnapshot) *updateWork {
	work := &updateWork{}
	for _, pod := range ss.existingPCLQPods {
		isNewHash := pod.Labels[apicommon.LabelPodTemplateHash] == ss.expectedPodTemplateHash
		deletionTriggered := r.hasPodDeletionBeenTriggered(ss, pod)

		if isNewHash {
			// New-hash Pods need no rolling-update action. Track only those that are Ready and not
			// being deleted, since they are what completion and the budget count as available.
			if !deletionTriggered && k8sutils.IsPodReady(pod) {
				work.newReadyPodCount++
			}
			continue
		}

		// An old-hash Pod whose deletion has already been triggered (terminating, or a delete expectation
		// is recorded) is on its way out and does not block completion, so it is not counted. The work is
		// recomputed each reconcile, so the count always reflects the current state.
		if deletionTriggered {
			logger.Info("skipping old Pod since its deletion has already been triggered", "pod", client.ObjectKeyFromObject(pod))
			continue
		}
		// This old-hash Pod is still awaiting replacement.
		work.oldHashPodsAwaitingReplacement++
		// Pending, unhealthy, starting, and uncategorized Pods are deleted immediately; Ready Pods are
		// queued for ordered budgeted replacement.
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
	}
	return work
}

// hasPodDeletionBeenTriggered checks if a pod is already terminating or has a delete expectation recorded
// under its PodGang-scoped key.
func (r _resource) hasPodDeletionBeenTriggered(ss *syncSnapshot, pod *corev1.Pod) bool {
	if k8sutils.IsResourceTerminating(pod.ObjectMeta) {
		return true
	}
	key, err := expectations.PodGangScopedExpectationsStoreKey(ss.pclq.ObjectMeta, pod.Labels[apicommon.LabelPodGang])
	if err != nil {
		return false
	}
	return r.expectationsStore.HasDeleteExpectation(key, pod.GetUID())
}

// deleteOldNonReadyPods removes old-hash pods that are not Ready: pending, unhealthy, starting (startup probe),
// or uncategorized (unknown state). All of these are safe to delete immediately since they are not serving traffic
// and will be replaced with pods having the correct template hash.
func (r _resource) deleteOldNonReadyPods(ctx context.Context, logger logr.Logger, ss *syncSnapshot, work *updateWork) error {
	if len(work.oldTemplateHashUncategorizedPods) > 0 {
		logger.Info("found old-hash pods in an unrecognized state, deleting them",
			"unexpected", true,
			"pods", componentutils.PodsToObjectNames(work.oldTemplateHashUncategorizedPods))
	}

	podsToDelete := lo.Union(work.oldTemplateHashPendingPods, work.oldTemplateHashUnhealthyPods, work.oldTemplateHashStartingPods, work.oldTemplateHashUncategorizedPods)
	deletionTasks := r.createPodDeletionTasks(logger, ss.pclq, podsToDelete)

	if len(deletionTasks) == 0 {
		logger.Info("no non-ready pods having old PodTemplateHash found")
		return nil
	}

	logger.Info("triggering deletion of non-ready pods with old pod template hash in order to update",
		"oldPendingPods", componentutils.PodsToObjectNames(work.oldTemplateHashPendingPods),
		"oldUnhealthyPods", componentutils.PodsToObjectNames(work.oldTemplateHashUnhealthyPods),
		"oldStartingPods", componentutils.PodsToObjectNames(work.oldTemplateHashStartingPods),
		"oldUncategorizedPods", componentutils.PodsToObjectNames(work.oldTemplateHashUncategorizedPods))
	if runResult := utils.RunConcurrently(ctx, logger, deletionTasks); runResult.HasErrors() {
		err := runResult.GetAggregatedError()
		pclqObjectKey := client.ObjectKeyFromObject(ss.pclq)
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

// markRollingUpdateEnd marks the completion of the rolling update by setting the end timestamp.
func (r _resource) markRollingUpdateEnd(ctx context.Context, logger logr.Logger, pclq *grovecorev1alpha1.PodClique) error {
	patch := client.MergeFrom(pclq.DeepCopy())

	pclq.Status.UpdateProgress.UpdateEndedAt = ptr.To(metav1.Now())

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

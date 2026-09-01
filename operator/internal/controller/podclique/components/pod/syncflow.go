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
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/index"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// prepareSyncFlow gathers information in preparation for the sync flow to run.
func (r _resource) prepareSyncFlow(ctx context.Context, logger logr.Logger, pclq *grovecorev1alpha1.PodClique) (*syncSnapshot, error) {
	var (
		ss            = &syncSnapshot{pclq: pclq}
		err           error
		pclqObjectKey = client.ObjectKeyFromObject(pclq)
	)

	// Get associated PodCliqueSet for this PodClique.
	pcs, err := componentutils.GetPodCliqueSet(ctx, r.client, pclq.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGetPodCliqueSet,
			component.OperationSync,
			fmt.Sprintf("failed to get owner PodCliqueSet of PodClique: %v", pclqObjectKey),
		)
	}
	ss.pcs = pcs
	pcsObjectKey := client.ObjectKeyFromObject(pcs)

	ss.expectedPodTemplateHash, err = componentutils.GetExpectedPCLQPodTemplateHash(ss.pcs, pclq.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGetPodCliqueTemplate,
			component.OperationSync,
			fmt.Sprintf("failed to compute pod clique template hash for PodClique: %v in PodCliqueSet", pclqObjectKey),
		)
	}

	ss.cliqueName, err = utils.GetPodCliqueNameFromPodCliqueFQN(pclq.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGetPodCliqueTemplate,
			component.OperationSync,
			fmt.Sprintf("failed to extract clique name from PodClique: %v", pclqObjectKey),
		)
	}
	ss.isStandalonePCLQ = componentutils.IsStandalonePCLQ(ss.pcs, ss.cliqueName)

	ss.pcsReplicaIndex, err = k8sutils.GetPodCliqueSetReplicaIndex(pclq.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGetPodGangMap,
			component.OperationSync,
			fmt.Sprintf("failed to determine PodCliqueSet replica index for PodClique: %v", pclqObjectKey),
		)
	}

	// A PodCliqueScalingGroup-owned PodClique belongs to a single PodGang, named on its
	// grove.io/podgang label at creation. A standalone PodClique is distributed across anchor
	// PodGangs and has no single associated PodGang name.
	if !ss.isStandalonePCLQ {
		ss.pcsgReplicaPodGangName, err = r.getAssociatedPodGangName(pclq.ObjectMeta)
		if err != nil {
			return nil, err
		}
	}

	// The PodGangMap for this PCS replica is the source of truth for PodGang composition and the
	// DependsOn epochs that gate-removal reads. It is created before any PodClique, so it is expected
	// to exist; a missing PodGangMap is requeued.
	ss.pgm, err = componentutils.GetPodGangMap(ctx, r.client, pcsObjectKey, ss.pcsReplicaIndex)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, groveerr.WrapError(err,
				errCodePodGangMapNotFound,
				component.OperationSync,
				fmt.Sprintf("PodGangMap not found for PodCliqueSet(Name: %v, ReplicaIndex: %d)", pcsObjectKey, ss.pcsReplicaIndex),
			)
		}
		return nil, groveerr.WrapError(err,
			errCodeGetPodGangMap,
			component.OperationSync,
			fmt.Sprintf("failed to get PodGangMap for PodCliqueSet: %v, PCS replica index: %d", pcsObjectKey, ss.pcsReplicaIndex),
		)
	}

	// Get all existing pods for this PCLQ.
	ss.existingPCLQPods, err = componentutils.GetPCLQPods(ctx, r.client, ss.pcs.Name, pclq)
	if err != nil {
		logger.Error(err, "Failed to list pods that belong to PodClique")
		return nil, groveerr.WrapError(err,
			errCodeListPod,
			component.OperationSync,
			fmt.Sprintf("failed to list pods that belong to the PodClique %v", pclqObjectKey),
		)
	}

	return ss, nil
}

// getAssociatedPodGangName gets the associated PodGang name from PodClique labels.
// Returns an error if the label is not found.
// NOTE: This is only applicable for PCLQs that are owned by a PCSG. All PCLQs of a PCSG replica share the same PodGang.
func (r _resource) getAssociatedPodGangName(pclqObjectMeta metav1.ObjectMeta) (string, error) {
	podGangName, ok := pclqObjectMeta.GetLabels()[apicommon.LabelPodGang]
	if !ok {
		return "", groveerr.New(errCodeMissingPodGangLabelOnPCLQ,
			component.OperationSync,
			fmt.Sprintf("PodClique: %v is missing required label: %s", k8sutils.GetObjectKeyFromObjectMeta(pclqObjectMeta), apicommon.LabelPodGang),
		)
	}
	return podGangName, nil
}

// relabelPodsToPodGang patches the grove.io/podgang label to podGangName on each pod.
func (r _resource) relabelPodsToPodGang(ctx context.Context, logger logr.Logger, pods []*corev1.Pod, podGangName string) error {
	for _, pod := range pods {
		podClone := pod.DeepCopy()
		if pod.Labels == nil {
			pod.Labels = map[string]string{}
		}
		pod.Labels[apicommon.LabelPodGang] = podGangName
		if err := client.IgnoreNotFound(r.client.Patch(ctx, pod, client.MergeFrom(podClone))); err != nil {
			return groveerr.WrapError(err, errCodeLabelPod, component.OperationSync,
				fmt.Sprintf("failed to set %s label on pod %v", apicommon.LabelPodGang, client.ObjectKeyFromObject(pod)))
		}
		logger.Info("Relabeled pod to PodGang", "podObjectKey", client.ObjectKeyFromObject(pod), "podGang", podGangName)
	}
	return nil
}

// reconcilePCSGLabellessPods repairs non-terminating pods of a PodCliqueScalingGroup-owned PodClique
// that lack the grove.io/podgang label. Such a PodClique maps to a single PodGang, so labelless pods
// are relabeled to that PodGang. It returns whether any pod was repaired.
func (r _resource) reconcilePCSGLabellessPods(ctx context.Context, logger logr.Logger, ss *syncSnapshot) (bool, error) {
	labellessPods := lo.Filter(ss.existingPCLQPods, func(pod *corev1.Pod, _ int) bool {
		if k8sutils.IsResourceTerminating(pod.ObjectMeta) {
			return false
		}
		_, hasPodGangLabel := pod.Labels[apicommon.LabelPodGang]
		return !hasPodGangLabel
	})
	if len(labellessPods) == 0 {
		return false, nil
	}
	if err := r.relabelPodsToPodGang(ctx, logger, labellessPods, ss.pcsgReplicaPodGangName); err != nil {
		return false, err
	}
	return true, nil
}

// runSyncFlow executes the main synchronization logic including pod creation, deletion, updates, and scheduling gate management
func (r _resource) runSyncFlow(ctx context.Context, logger logr.Logger, ss *syncSnapshot) syncFlowResult {
	result := syncFlowResult{}
	if ss.isStandalonePCLQ {
		// A standalone PodClique's pods are distributed across one or more anchor PodGangs, so its
		// pods are reconciled per PodGang against the PodGangMap counts.
		if err := r.reconcileStandalonePCLQDistribution(ctx, logger, ss); err != nil {
			result.recordError(err)
		}
	} else {
		// A PodCliqueScalingGroup-owned PodClique belongs to a single PodGang. Repair any labelless
		// pod by relabeling it to that PodGang, then requeue so the diff runs on a consistent state.
		repaired, err := r.reconcilePCSGLabellessPods(ctx, logger, ss)
		if err != nil {
			result.recordError(err)
			return result
		}
		if repaired {
			result.recordError(groveerr.New(groveerr.ErrCodeRequeueAfter, component.OperationSync,
				fmt.Sprintf("repaired pods missing the %s label for PodCliqueScalingGroup PodClique %v, re-queueing", apicommon.LabelPodGang, client.ObjectKeyFromObject(ss.pclq))))
			return result
		}

		// A PodCliqueScalingGroup-owned PodClique belongs to a single PodGang, so a scalar delta drives
		// creation and deletion. A positive delta creates pods, a negative delta deletes them.
		delta, err := r.computePodCountDelta(ss)
		if err != nil {
			result.recordError(err)
		} else if delta > 0 {
			numScheduleGatedPods, err := r.createPods(ctx, logger, ss, delta)
			if err != nil {
				logger.Error(err, "failed to create pods")
				result.recordError(err)
			}
			logger.Info("created unassigned and scheduled gated pods", "numberOfCreatedPods", numScheduleGatedPods)
		} else if delta < 0 {
			if err := r.deleteExcessPods(ctx, logger, ss, -delta); err != nil {
				result.recordError(err)
			}
		}
	}

	if componentutils.IsAutoUpdateStrategy(ss.pcs) && componentutils.IsPCLQAutoUpdateInProgress(ss.pclq) {
		if err := r.processPendingUpdates(ctx, logger, ss); err != nil {
			result.recordError(err)
		}
	}

	skippedScheduleGatedPods, err := r.checkAndRemovePodSchedulingGates(ctx, logger, ss)
	if err != nil {
		result.recordError(err)
	}
	result.recordPendingScheduleGatedPods(skippedScheduleGatedPods)
	return result
}

// syncPCSGPodIndexLabels backfills and reconciles the group-wide index label on existing PCSG pods.
func (r _resource) syncPCSGPodIndexLabels(ctx context.Context, ss *syncSnapshot) error {
	if ss.pclq.Labels[apicommon.LabelPodCliqueScalingGroup] == "" {
		return nil
	}

	for _, pod := range ss.existingPCLQPods {
		podIndexValue, ok := pod.Labels[apicommon.LabelPodCliquePodIndex]
		if !ok {
			return groveerr.New(
				errCodeUpdatePCSGPodIndexLabel,
				component.OperationSync,
				fmt.Sprintf("Pod %v is missing required label %q", client.ObjectKeyFromObject(pod), apicommon.LabelPodCliquePodIndex),
			)
		}
		podIndex, err := strconv.Atoi(podIndexValue)
		if err != nil {
			return groveerr.WrapError(
				err,
				errCodeUpdatePCSGPodIndexLabel,
				component.OperationSync,
				fmt.Sprintf("Pod %v has invalid %s value %q", client.ObjectKeyFromObject(pod), apicommon.LabelPodCliquePodIndex, podIndexValue),
			)
		}
		pcsgPodIndex, err := getPCSGPodIndex(ss.pcs, ss.pclq, ss.pcsReplicaIndex, podIndex)
		if err != nil {
			return groveerr.WrapError(
				err,
				errCodeUpdatePCSGPodIndexLabel,
				component.OperationSync,
				fmt.Sprintf("error computing PodCliqueScalingGroup pod index for Pod %v", client.ObjectKeyFromObject(pod)),
			)
		}
		expectedValue := strconv.Itoa(*pcsgPodIndex)
		if pod.Labels[apicommon.LabelPodCliqueScalingGroupPodIndex] == expectedValue {
			continue
		}

		podBeforePatch := pod.DeepCopy()
		pod.Labels[apicommon.LabelPodCliqueScalingGroupPodIndex] = expectedValue
		if err = r.client.Patch(ctx, pod, client.MergeFrom(podBeforePatch)); err != nil {
			return groveerr.WrapError(
				err,
				errCodeUpdatePCSGPodIndexLabel,
				component.OperationSync,
				fmt.Sprintf("failed to update PodCliqueScalingGroup pod index label on Pod %v", client.ObjectKeyFromObject(pod)),
			)
		}
	}
	return nil
}

// computePodCountDelta returns desired minus the live pod count reconciled with expectations for a
// PodCliqueScalingGroup-owned PodClique, which belongs to a single PodGang. Reconciling with
// outstanding create and delete expectations keeps an operation from a prior reconcile, not yet
// reflected in the informer cache, from being repeated. A positive value is pods to create, a
// negative value is pods to delete.
func (r _resource) computePodCountDelta(ss *syncSnapshot) (int, error) {
	podsByPodGang, _ := groupPodsByPodGang(ss.existingPCLQPods)
	reconciledCount, err := r.reconcileLivePodCountWithExpectations(ss.pclq.ObjectMeta, ss.pcsgReplicaPodGangName, podsByPodGang[ss.pcsgReplicaPodGangName])
	if err != nil {
		return 0, err
	}
	return int(ss.pclq.Spec.Replicas) - int(reconciledCount), nil
}

// deleteExcessPods deletes `diff` number of excess Pods from this PodClique concurrently.
// It selects the pods using `DeletionSorter`. For details please see `DeletionSorter.Less` method.
// The deletion of Pods are done in batches of increasing size. This is done to prevent burst of load
// on the kube-apiserver. It will fail fast in case there is an
func (r _resource) deleteExcessPods(ctx context.Context, logger logr.Logger, ss *syncSnapshot, diff int) error {
	candidatePodsToDelete := r.selectExcessPodsToDelete(ss, logger)
	numPodsToSelectForDeletion := min(diff, len(candidatePodsToDelete))
	selectedPodsToDelete := candidatePodsToDelete[:numPodsToSelectForDeletion]

	deleteTasks := make([]utils.Task, 0, len(selectedPodsToDelete))
	for _, podToDelete := range selectedPodsToDelete {
		deleteTasks = append(deleteTasks, r.createPodDeletionTask(logger, ss.pclq, podToDelete))
	}

	if runResult := utils.RunConcurrentlyWithSlowStart(ctx, logger, 1, deleteTasks); runResult.HasErrors() {
		err := runResult.GetAggregatedError()
		pclqObjectKey := client.ObjectKeyFromObject(ss.pclq)
		logger.Error(err, "failed to delete pods for PCLQ", "runSummary", runResult.GetSummary())
		return groveerr.WrapError(err,
			errCodeDeletePod,
			component.OperationSync,
			fmt.Sprintf("failed to delete Pods for PodClique %v", pclqObjectKey),
		)
	}
	logger.Info("Deleted excess pods", "diff", diff, "noOfPodsDeleted", numPodsToSelectForDeletion)
	return nil
}

// selectExcessPodsToDelete identifies excess pods for deletion using DeletionSorter for prioritization.
//
// Pods whose deletion has already been triggered are excluded from the candidate set. GetPCLQPods
// returns terminating Pods as well, and a Pod stays Running and Ready for the whole of its
// terminationGracePeriodSeconds, so counting it as excess spends the deletion budget on a Pod that is
// already on its way out, and, since DeletionSorter cannot tell it apart from a healthy Pod, can
// select a Pod that is still serving instead. The rolling update path applies the same rule via
// hasPodDeletionBeenTriggered (see computeUpdateWork in rollingupdate.go).
//
// Filtering into a fresh slice also keeps sort.Sort from reordering ss.existingPCLQPods in place,
// which later steps of the same sync flow still read.
func (r _resource) selectExcessPodsToDelete(ss *syncSnapshot, logger logr.Logger) []*corev1.Pod {
	livePods := make([]*corev1.Pod, 0, len(ss.existingPCLQPods))
	for _, pod := range ss.existingPCLQPods {
		if r.hasPodDeletionBeenTriggered(ss, pod) {
			continue
		}
		livePods = append(livePods, pod)
	}
	numExcessPods := len(livePods) - int(ss.pclq.Spec.Replicas)
	if numExcessPods <= 0 {
		return nil
	}
	logger.Info("found excess pods for PodClique", "numExcessPods", numExcessPods)
	sorter := DeletionSorter{
		Pods:                    livePods,
		ExpectedPodTemplateHash: ss.getExpectedPodTemplateHash(),
	}
	sort.Sort(sorter)
	return sorter.Pods[:numExcessPods]
}

func (ss *syncSnapshot) getExpectedPodTemplateHash() string {
	if ss.pclq.Status.UpdateProgress != nil &&
		ss.pcs.Status.CurrentGenerationHash != nil &&
		ss.pclq.Status.UpdateProgress.PodCliqueSetGenerationHash == *ss.pcs.Status.CurrentGenerationHash {
		return ss.pclq.Status.UpdateProgress.PodTemplateHash
	}
	return ss.pclq.Labels[apicommon.LabelPodTemplateHash]
}

// checkAndRemovePodSchedulingGates removes the Grove PodGang scheduling gate from gated pods whose
// dependency PodGangs are scheduled. A gated pod's grove.io/podgang label resolves to a PodGangMap
// entry whose DependsOn epochs must all be scheduled before the gate is lifted. This works whether
// the PodClique's pods belong to one PodGang or several.
func (r _resource) checkAndRemovePodSchedulingGates(ctx context.Context, logger logr.Logger, ss *syncSnapshot) ([]string, error) {
	skippedScheduleGatedPods := make([]string, 0, len(ss.existingPCLQPods))

	gatedPods := lo.Filter(ss.existingPCLQPods, func(pod *corev1.Pod, _ int) bool {
		return hasPodGangSchedulingGate(pod)
	})
	if len(gatedPods) == 0 {
		return skippedScheduleGatedPods, nil
	}

	podGangByName, err := r.fetchPodGangsForGatedPods(ctx, gatedPods, ss.pclq.Namespace)
	if err != nil {
		return nil, err
	}
	dependencySatisfiedByEpoch, err := r.resolveDependencySatisfiedByEpoch(ctx, ss)
	if err != nil {
		return nil, err
	}

	tasks := make([]utils.Task, 0, len(gatedPods))
	for i, pod := range gatedPods {
		if !canRemoveSchedulingGate(logger, pod, ss.pclq.Name, podGangByName, dependencySatisfiedByEpoch) {
			skippedScheduleGatedPods = append(skippedScheduleGatedPods, pod.Name)
			continue
		}
		tasks = append(tasks, utils.Task{
			Name: fmt.Sprintf("RemoveSchedulingGate-%s-%d", pod.Name, i),
			Fn: func(ctx context.Context) error {
				podClone := pod.DeepCopy()
				// Remove only the Grove PodGang gate. Other controllers may add their own scheduling
				// gates on the same Pod; clearing the whole list would wipe those and let the Pod
				// schedule before those owners have released it.
				if !removePodGangSchedulingGate(pod) {
					return nil
				}
				if err := client.IgnoreNotFound(r.client.Patch(ctx, pod, client.MergeFrom(podClone))); err != nil {
					return err
				}
				logger.Info("Removed Grove PodGang scheduling gate from pod", "podObjectKey", client.ObjectKeyFromObject(pod))
				return nil
			},
		})
	}

	if len(tasks) > 0 {
		if runResult := utils.RunConcurrentlyWithSlowStart(ctx, logger, 1, tasks); runResult.HasErrors() {
			err := runResult.GetAggregatedError()
			logger.Error(err, "failed to remove scheduling gates from pods for PCLQ", "runSummary", runResult.GetSummary())
			return skippedScheduleGatedPods, groveerr.WrapError(err,
				errCodeRemovePodSchedulingGate,
				component.OperationSync,
				fmt.Sprintf("failed to remove scheduling gates from Pods for PodClique %v", client.ObjectKeyFromObject(ss.pclq)),
			)
		}
	}

	return skippedScheduleGatedPods, nil
}

// fetchPodGangsForGatedPods fetches, once each, the distinct PodGangs the gated pods reference by
// their grove.io/podgang label. A PodGang that does not exist yet is recorded as nil so its pods are
// skipped. A pod without the label is skipped as well, since its label is repaired earlier in the
// sync flow and it is not yet placeable in any PodGang.
func (r _resource) fetchPodGangsForGatedPods(ctx context.Context, gatedPods []*corev1.Pod, namespace string) (map[string]*groveschedulerv1alpha1.PodGang, error) {
	podGangByName := make(map[string]*groveschedulerv1alpha1.PodGang)
	for _, pod := range gatedPods {
		podGangName, ok := pod.Labels[apicommon.LabelPodGang]
		if !ok {
			continue
		}
		if _, seen := podGangByName[podGangName]; seen {
			continue
		}
		podGang, err := componentutils.GetPodGang(ctx, r.client, podGangName, namespace)
		if err != nil {
			if apierrors.IsNotFound(err) {
				podGangByName[podGangName] = nil
				continue
			}
			return nil, groveerr.WrapError(err,
				errCodeGetPodGang,
				component.OperationSync,
				fmt.Sprintf("failed to get PodGang %v", client.ObjectKey{Namespace: namespace, Name: podGangName}),
			)
		}
		podGangByName[podGangName] = podGang
	}
	return podGangByName, nil
}

// resolveDependencySatisfiedByEpoch returns, keyed by each PodGangMap entry's epoch, whether every
// epoch that entry DependsOn has all its PodGangs scheduled. An entry with no DependsOn is trivially
// satisfied. Each distinct dependency epoch is resolved with a single List, memoized across entries.
func (r _resource) resolveDependencySatisfiedByEpoch(ctx context.Context, ss *syncSnapshot) (map[string]bool, error) {
	epochScheduled := make(map[string]bool)
	satisfiedByEpoch := make(map[string]bool, len(ss.pgm.Spec.Entries))
	for _, entry := range ss.pgm.Spec.Entries {
		satisfied := true
		for _, dependencyEpoch := range entry.DependsOn {
			scheduled, ok := epochScheduled[dependencyEpoch]
			if !ok {
				var err error
				scheduled, err = componentutils.AllPodGangsAtEpochEverScheduled(ctx, r.client, client.ObjectKeyFromObject(ss.pcs), int32(ss.pcsReplicaIndex), dependencyEpoch)
				if err != nil {
					return nil, err
				}
				epochScheduled[dependencyEpoch] = scheduled
			}
			if !scheduled {
				satisfied = false
				break
			}
		}
		satisfiedByEpoch[entry.Epoch] = satisfied
	}
	return satisfiedByEpoch, nil
}

// canRemoveSchedulingGate reports whether pod's Grove PodGang gate can be lifted. Its PodGang must
// exist, record the pod in its PodReferences, and the dependencies of its PodGang's epoch must be
// satisfied. It reads only prefetched maps and makes no API calls. A PodGang epoch absent from
// dependencySatisfiedByEpoch (a transient PodGangMap and PodGang divergence) resolves to false, so
// the pod is skipped and the reconcile requeues.
func canRemoveSchedulingGate(logger logr.Logger, pod *corev1.Pod, pclqName string, podGangByName map[string]*groveschedulerv1alpha1.PodGang, dependencySatisfiedByEpoch map[string]bool) bool {
	podObjectKey := client.ObjectKeyFromObject(pod)
	podGangName, ok := pod.Labels[apicommon.LabelPodGang]
	if !ok {
		logger.Info("Pod has no PodGang label yet, skipping gate removal", "podObjectKey", podObjectKey)
		return false
	}

	podGang := podGangByName[podGangName]
	if podGang == nil {
		logger.Info("PodGang not found yet, skipping gate removal", "podObjectKey", podObjectKey, "podGangName", podGangName)
		return false
	}
	if !isPodInPodReferences(podGang, pclqName, pod.Name) {
		logger.Info("Pod not yet recorded in PodGang PodReferences, skipping gate removal", "podObjectKey", podObjectKey, "podGangName", podGangName)
		return false
	}
	if !dependencySatisfiedByEpoch[podGang.Labels[apicommon.LabelEpoch]] {
		logger.Info("Pod's PodGang epoch dependencies not yet scheduled, skipping gate removal", "podObjectKey", podObjectKey, "podGangName", podGangName)
		return false
	}
	return true
}

// isPodInPodReferences reports whether podName appears in the PodGroup for pclqFQN in podGang.
func isPodInPodReferences(podGang *groveschedulerv1alpha1.PodGang, pclqFQN, podName string) bool {
	for _, podGroup := range podGang.Spec.PodGroups {
		if podGroup.Name != pclqFQN {
			continue
		}
		for _, ref := range podGroup.PodReferences {
			if ref.Name == podName {
				return true
			}
		}
	}
	return false
}

// hasPodGangSchedulingGate checks if a pod has the PodGang scheduling gate
func hasPodGangSchedulingGate(pod *corev1.Pod) bool {
	return slices.ContainsFunc(pod.Spec.SchedulingGates, func(schedulingGate corev1.PodSchedulingGate) bool {
		return podGangSchedulingGate == schedulingGate.Name
	})
}

// removePodGangSchedulingGate removes only the Grove PodGang scheduling gate from the Pod,
// leaving any other scheduling gates untouched. Returns true if the gate was present and removed.
func removePodGangSchedulingGate(pod *corev1.Pod) bool {
	idx := slices.IndexFunc(pod.Spec.SchedulingGates, func(schedulingGate corev1.PodSchedulingGate) bool {
		return podGangSchedulingGate == schedulingGate.Name
	})
	if idx < 0 {
		return false
	}
	pod.Spec.SchedulingGates = slices.Delete(pod.Spec.SchedulingGates, idx, idx+1)
	return true
}

// createPods creates the specified number of new pods for the PodClique with proper indexing and concurrency control
func (r _resource) createPods(ctx context.Context, logger logr.Logger, ss *syncSnapshot, numPods int) (int, error) {
	// Pre-calculate all needed indices to avoid race conditions
	availableIndices, err := index.GetAvailableIndices(logger, ss.existingPCLQPods, numPods)
	if err != nil {
		return 0, groveerr.WrapError(err,
			errCodeGetAvailablePodHostNameIndices,
			component.OperationSync,
			fmt.Sprintf("error getting available indices for Pods in PodClique %v", client.ObjectKeyFromObject(ss.pclq)),
		)
	}
	// A PodCliqueScalingGroup-owned PodClique belongs to a single PodGang, so every created pod
	// records its create expectation under that PodGang's scoped key.
	expectationsKey, err := componentutils.PodGangScopedExpectationsStoreKey(ss.pclq.ObjectMeta, ss.pcsgReplicaPodGangName)
	if err != nil {
		return 0, err
	}
	createTasks := make([]utils.Task, 0, numPods)
	for i := range numPods {
		// Get the available Pod host name index. This ensures that we fill the holes in the indices if there are any when creating
		// new pods.
		podHostNameIndex := availableIndices[i]
		createTasks = append(createTasks, r.createPodCreationTask(logger, ss.pcs, ss.pclq, ss.pcsgReplicaPodGangName, expectationsKey, i, podHostNameIndex))
	}
	runResult := utils.RunConcurrentlyWithSlowStart(ctx, logger, 1, createTasks)
	if runResult.HasErrors() {
		err = runResult.GetAggregatedError()
		logger.Error(err, "failed to create pods for PCLQ", "runSummary", runResult.GetSummary())
		return 0, err
	}
	return len(runResult.SuccessfulTasks), nil
}

// Convenience functions, types and methods on these types that are used during sync flow run.
// ------------------------------------------------------------------------------------------------

// syncSnapshot holds the relevant state required during the sync flow run.
type syncSnapshot struct {
	pcs                     *grovecorev1alpha1.PodCliqueSet
	pclq                    *grovecorev1alpha1.PodClique
	pcsReplicaIndex         int
	pgm                     *grovecorev1alpha1.PodGangMap
	isStandalonePCLQ        bool
	cliqueName              string
	pcsgReplicaPodGangName  string
	existingPCLQPods        []*corev1.Pod
	expectedPodTemplateHash string
}

// syncFlowResult captures the result of a sync flow run.
type syncFlowResult struct {
	// scheduleGatedPods are the pods that were created but are still schedule gated.
	scheduleGatedPods []string
	// errs are the list of errors during the sync flow run.
	errs []error
}

// getAggregatedError combines all errors from the sync flow into a single error
func (sfr *syncFlowResult) getAggregatedError() error {
	return errors.Join(sfr.errs...)
}

// hasPendingScheduleGatedPods returns true if there are pods still waiting for schedule gate removal
func (sfr *syncFlowResult) hasPendingScheduleGatedPods() bool {
	return len(sfr.scheduleGatedPods) > 0
}

// recordError adds an error to the sync flow result
func (sfr *syncFlowResult) recordError(err error) {
	sfr.errs = append(sfr.errs, err)
}

// recordPendingScheduleGatedPods adds pod names that are still schedule gated to the result
func (sfr *syncFlowResult) recordPendingScheduleGatedPods(podNames []string) {
	sfr.scheduleGatedPods = append(sfr.scheduleGatedPods, podNames...)
}

// hasErrors returns true if any errors occurred during the sync flow
func (sfr *syncFlowResult) hasErrors() bool {
	return len(sfr.errs) > 0
}

// Copyright 2026 The Grove Authors.
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
	"sort"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/podclique/expectations"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/index"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// reconcileStandalonePCLQDistribution reconciles a standalone PodClique's pods across the anchor
// PodGangs the PodGangMap assigns them to. It first repairs any non-terminating pod missing the
// grove.io/podgang label, then reconciles per PodGang against the PodGangMap counts.
func (r _resource) reconcileStandalonePCLQDistribution(ctx context.Context, logger logr.Logger, ss *syncSnapshot) error {
	desiredCountByPodGang := buildDesiredCountByPodGang(ss)
	// An empty desired count means the PodGangMap carries no anchor entry for this PodClique. When the
	// PodClique still wants replicas this is a transient state, the PodGangMap has not authored or
	// absorbed the anchor entry yet, so requeue and wait. When the PodClique is scaled to zero the empty
	// desired count is correct, so fall through and let the delta computation delete the excess pods.
	if len(desiredCountByPodGang) == 0 && ss.pclq.Spec.Replicas > 0 {
		return groveerr.New(groveerr.ErrCodeRequeueAfter, component.OperationSync,
			fmt.Sprintf("PodGangMap has no anchor entry for standalone PodClique %v yet, re-queueing", client.ObjectKeyFromObject(ss.pclq)))
	}

	// Group the PodClique's pods by their PodGang once. Each group holds its non-terminating and
	// terminating pods, and non-terminating pods without a PodGang label are returned as unassigned
	// for repair. Every downstream step reads from this grouping.
	podsByPodGang, unassignedPods := groupPodsByPodGang(ss.existingPCLQPods)

	// Repair unassigned pods before computing deltas so the reconcile below sees a consistent label
	// state. Any repair requeues, and the next reconcile computes deltas on the settled labels.
	repaired, err := r.reconcileUnassignedPods(ctx, logger, desiredCountByPodGang, unassignedPods)
	if err != nil {
		return err
	}
	if repaired {
		return groveerr.New(groveerr.ErrCodeRequeueAfter, component.OperationSync,
			fmt.Sprintf("repaired pods missing the %s label for standalone PodClique %v, re-queueing", apicommon.LabelPodGang, client.ObjectKeyFromObject(ss.pclq)))
	}

	// With more than one anchor the PodGangMap decides which anchor a Spec.Replicas change lands on.
	// Wait until it has absorbed the change. A single anchor has nothing to decide.
	if len(desiredCountByPodGang) > 1 && sumCounts(desiredCountByPodGang) != ss.pclq.Spec.Replicas {
		return groveerr.New(groveerr.ErrCodeRequeueAfter, component.OperationSync,
			fmt.Sprintf("PodGangMap has not yet absorbed the replica change for standalone PodClique %v, re-queueing", client.ObjectKeyFromObject(ss.pclq)))
	}

	countDeltaByPodGang, err := r.computeCountDeltaByPodGang(ss, desiredCountByPodGang, podsByPodGang)
	if err != nil {
		return err
	}
	return r.applyCountDeltaByPodGang(ctx, logger, ss, countDeltaByPodGang, podsByPodGang)
}

// buildDesiredCountByPodGang returns the desired pod count per anchor PodGang for this standalone
// PodClique, read from the PodGangMap entries. Only anchor entries carry standalone PodClique counts.
// Entries without this PodClique, or with a zero count, are skipped.
func buildDesiredCountByPodGang(ss *syncSnapshot) map[string]int32 {
	desiredCountByPodGang := make(map[string]int32)
	if ss.pgm == nil {
		return desiredCountByPodGang
	}
	rnr := apicommon.ResourceNameReplica{Name: ss.pcs.Name, Replica: ss.pcsReplicaIndex}
	for _, entry := range ss.pgm.Spec.Entries {
		count, ok := entry.PodCliques[ss.cliqueName]
		if !ok || count == 0 {
			continue
		}
		desiredCountByPodGang[apicommon.GenerateAnchorPodGangName(rnr, entry.Epoch)] = count
	}
	return desiredCountByPodGang
}

// podGangPods holds a PodGang's pods split by termination state. nonTerminating pods count towards
// the live replica count. terminating pods are re-added to delete expectations during the sync.
type podGangPods struct {
	nonTerminating []*corev1.Pod
	terminating    []*corev1.Pod
}

// groupPodsByPodGang groups pods by their grove.io/podgang label in a single pass. Each group holds
// the PodGang's non-terminating and terminating pods. A non-terminating pod without the label is
// returned separately as unassigned for repair. A terminating pod without the label is ignored,
// since it is leaving anyway.
func groupPodsByPodGang(pods []*corev1.Pod) (podsByPodGang map[string]podGangPods, unassignedPods []*corev1.Pod) {
	podsByPodGang = make(map[string]podGangPods)
	for _, pod := range pods {
		terminating := k8sutils.IsResourceTerminating(pod.ObjectMeta)
		podGangName, labeled := pod.Labels[apicommon.LabelPodGang]
		if !labeled {
			if !terminating {
				unassignedPods = append(unassignedPods, pod)
			}
			continue
		}
		group := podsByPodGang[podGangName]
		if terminating {
			group.terminating = append(group.terminating, pod)
		} else {
			group.nonTerminating = append(group.nonTerminating, pod)
		}
		podsByPodGang[podGangName] = group
	}
	return
}

// reconcileUnassignedPods repairs non-terminating pods that lack the grove.io/podgang label. Grove
// sets that label at pod creation, so an unassigned pod is an inconsistent state. With a single anchor
// the pod can only belong to that anchor, so it is relabeled in place. With more than one anchor its
// target is ambiguous, and it may have been scheduled for a different gang, so it is deleted. It gets
// recreated with the correct label and scheduling gates on a later reconcile. It returns whether any
// pod was repaired.
func (r _resource) reconcileUnassignedPods(ctx context.Context, logger logr.Logger, desiredCountByPodGang map[string]int32, unassignedPods []*corev1.Pod) (bool, error) {
	if len(unassignedPods) == 0 {
		return false, nil
	}

	// There is only one anchor PodGang, in this case its safe to just re-label the Pod (in-place)
	// with the anchor PodGang.
	if len(desiredCountByPodGang) == 1 {
		targetPodGangName := lo.Keys(desiredCountByPodGang)[0]
		if err := r.relabelPodsToPodGang(ctx, logger, unassignedPods, targetPodGangName); err != nil {
			return false, err
		}
		return true, nil
	}

	for _, pod := range unassignedPods {
		if err := client.IgnoreNotFound(r.client.Delete(ctx, pod)); err != nil {
			return false, groveerr.WrapError(err, errCodeDeletePod, component.OperationSync,
				fmt.Sprintf("failed to delete pod %v missing the %s label", client.ObjectKeyFromObject(pod), apicommon.LabelPodGang))
		}
		logger.Info("Deleted pod missing the PodGang label under multiple anchors for recreation", "podObjectKey", client.ObjectKeyFromObject(pod))
	}
	return true, nil
}

// sumCounts returns the total across all PodGang counts.
func sumCounts(countByPodGang map[string]int32) int32 {
	var total int32
	for _, count := range countByPodGang {
		total += count
	}
	return total
}

// computeCountDeltaByPodGang returns desired minus the live pod count reconciled with expectations
// for every PodGang in the desired or live set. A positive value is pods to create, a negative value
// is pods to delete. PodGangs whose desired and reconciled counts already match are omitted.
func (r _resource) computeCountDeltaByPodGang(ss *syncSnapshot, desiredCountByPodGang map[string]int32, podsByPodGang map[string]podGangPods) (map[string]int32, error) {
	countDeltaByPodGang := make(map[string]int32)
	// Iterate the union of PodGangs that are desired and PodGangs that have live pods. A PodGang with a
	// desired count but no live pods yet still needs creation, and a PodGang with live pods but no
	// desired count (its entry was removed) still needs deletion.
	podGangNames := sets.New(lo.Keys(desiredCountByPodGang)...).Insert(lo.Keys(podsByPodGang)...)
	for _, podGangName := range podGangNames.UnsortedList() {
		reconciledCount, err := r.reconcileLivePodCountWithExpectations(ss.pclq.ObjectMeta, podGangName, podsByPodGang[podGangName])
		if err != nil {
			return nil, err
		}
		if delta := desiredCountByPodGang[podGangName] - reconciledCount; delta != 0 {
			countDeltaByPodGang[podGangName] = delta
		}
	}
	return countDeltaByPodGang, nil
}

// reconcileLivePodCountWithExpectations syncs the PodGang's expectations against its live pods, then
// returns the live pod count reconciled with its outstanding expectations. The count is the existing
// pods plus outstanding create expectations minus outstanding delete expectations, so a create or
// delete issued in a prior reconcile, but not yet reflected in the informer cache, is not repeated.
// A terminating pod still exists in the cache, so it is included in the existing pods. SyncExpectations
// keeps it in the delete expectations, so it is also subtracted. The two contributions cancel, so a
// terminating pod has no net effect on the count and is not subtracted twice.
func (r _resource) reconcileLivePodCountWithExpectations(pclqObjMeta metav1.ObjectMeta, podGangName string, group podGangPods) (int32, error) {
	key, err := componentutils.PodGangScopedExpectationsStoreKey(pclqObjMeta, podGangName)
	if err != nil {
		return 0, err
	}
	nonTerminatingUIDs := lo.Map(group.nonTerminating, func(pod *corev1.Pod, _ int) types.UID { return pod.GetUID() })
	terminatingUIDs := lo.Map(group.terminating, func(pod *corev1.Pod, _ int) types.UID { return pod.GetUID() })
	r.expectationsStore.SyncExpectations(key, nonTerminatingUIDs, terminatingUIDs)
	existingPodCount := int32(len(nonTerminatingUIDs) + len(terminatingUIDs))
	return existingPodCount +
		int32(len(r.expectationsStore.GetCreateExpectations(key))) -
		int32(len(r.expectationsStore.GetDeleteExpectations(key))), nil
}

// applyCountDeltaByPodGang deletes the excess pods and creates the deficit for each PodGang.
func (r _resource) applyCountDeltaByPodGang(ctx context.Context, logger logr.Logger, ss *syncSnapshot, countDeltaByPodGang map[string]int32, podsByPodGang map[string]podGangPods) error {
	if len(countDeltaByPodGang) == 0 {
		return nil
	}
	orderedPodGangNames := lo.Keys(countDeltaByPodGang)
	sort.Strings(orderedPodGangNames)

	createTasks, err := r.buildPerPodGangCreationTasks(logger, ss, countDeltaByPodGang, orderedPodGangNames)
	if err != nil {
		return err
	}
	deleteTasks := r.buildPerPodGangDeletionTasks(logger, ss, countDeltaByPodGang, orderedPodGangNames, podsByPodGang)

	// delete the excess pods first so replacements are created only after the old pods are taken down.
	if len(deleteTasks) > 0 {
		if runResult := utils.RunConcurrentlyWithSlowStart(ctx, logger, 1, deleteTasks); runResult.HasErrors() {
			err = runResult.GetAggregatedError()
			logger.Error(err, "failed to delete pods for PCLQ", "runSummary", runResult.GetSummary())
			return groveerr.WrapError(err, errCodeDeletePod, component.OperationSync,
				fmt.Sprintf("failed to delete pods for PodClique %v", client.ObjectKeyFromObject(ss.pclq)))
		}
	}

	if len(createTasks) > 0 {
		if runResult := utils.RunConcurrentlyWithSlowStart(ctx, logger, 1, createTasks); runResult.HasErrors() {
			err = runResult.GetAggregatedError()
			logger.Error(err, "failed to create pods for PCLQ", "runSummary", runResult.GetSummary())
			return err
		}
	}
	return nil
}

// buildPerPodGangCreationTasks builds creation tasks for each PodGang whose desired count exceeds the
// effective count, assigning each new pod a host-name index. Each task records its create expectation
// under the PodGang-scoped expectations key.
func (r _resource) buildPerPodGangCreationTasks(logger logr.Logger, ss *syncSnapshot, countDeltaByPodGang map[string]int32, orderedPodGangNames []string) ([]utils.Task, error) {
	var totalToCreate int32
	for _, delta := range countDeltaByPodGang {
		if delta > 0 {
			totalToCreate += delta
		}
	}
	if totalToCreate == 0 {
		return nil, nil
	}
	availableIndices, err := index.GetAvailableIndices(logger, ss.existingPCLQPods, int(totalToCreate))
	if err != nil {
		return nil, groveerr.WrapError(err, errCodeGetAvailablePodHostNameIndices, component.OperationSync,
			fmt.Sprintf("error getting available indices for Pods in PodClique %v", client.ObjectKeyFromObject(ss.pclq)))
	}
	tasks := make([]utils.Task, 0, totalToCreate)
	taskIndex := 0
	for _, podGangName := range orderedPodGangNames {
		expectationsKey, err := componentutils.PodGangScopedExpectationsStoreKey(ss.pclq.ObjectMeta, podGangName)
		if err != nil {
			return nil, err
		}
		for created := int32(0); created < countDeltaByPodGang[podGangName]; created++ {
			tasks = append(tasks, r.createPodCreationTask(logger, ss.pcs, ss.pclq, podGangName, expectationsKey, taskIndex, availableIndices[taskIndex]))
			taskIndex++
		}
	}
	return tasks, nil
}

// buildPerPodGangDeletionTasks builds deletion tasks for each PodGang whose effective count exceeds
// the desired count. Within each PodGang the DeletionSorter picks which pods to remove.
func (r _resource) buildPerPodGangDeletionTasks(logger logr.Logger, ss *syncSnapshot, countDeltaByPodGang map[string]int32, orderedPodGangNames []string, podsByPodGang map[string]podGangPods) []utils.Task {
	var tasks []utils.Task
	for _, podGangName := range orderedPodGangNames {
		if countDeltaByPodGang[podGangName] >= 0 {
			continue
		}
		numToDelete := int(-countDeltaByPodGang[podGangName])
		pods := podsByPodGang[podGangName].nonTerminating
		if len(pods) == 0 {
			continue
		}
		sorter := DeletionSorter{Pods: slices.Clone(pods), ExpectedPodTemplateHash: ss.expectedPodTemplateHash}
		sort.Sort(sorter)
		numToDelete = min(numToDelete, len(sorter.Pods))
		for _, pod := range sorter.Pods[:numToDelete] {
			tasks = append(tasks, r.createPodDeletionTask(logger, ss.pclq, pod))
		}
	}
	return tasks
}

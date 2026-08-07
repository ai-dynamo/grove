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
	"github.com/ai-dynamo/grove/operator/internal/expect"
	"github.com/ai-dynamo/grove/operator/internal/index"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// prepareSyncFlow gathers information in preparation for the sync flow to run.
func (r _resource) prepareSyncFlow(ctx context.Context, logger logr.Logger, pclq *grovecorev1alpha1.PodClique) (*syncContext, error) {
	var (
		sc  = &syncContext{ctx: ctx, pclq: pclq}
		err error
	)

	// Get associated PodCliqueSet for this PodClique.
	sc.pcs, err = componentutils.GetPodCliqueSet(ctx, r.client, pclq.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGetPodCliqueSet,
			component.OperationSync,
			fmt.Sprintf("failed to get owner PodCliqueSet of PodClique: %v", client.ObjectKeyFromObject(pclq)),
		)
	}

	sc.expectedPodTemplateHash, err = componentutils.GetExpectedPCLQPodTemplateHash(sc.pcs, pclq.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGetPodCliqueTemplate,
			component.OperationSync,
			fmt.Sprintf("failed to compute pod clique template hash for PodClique: %v in PodCliqueSet", client.ObjectKeyFromObject(pclq)),
		)
	}

	sc.isStandalonePCLQ = componentutils.IsStandalonePCLQ(pclq)

	sc.pcsReplicaIndex, err = getPCSReplicaIndexFromPCLQ(pclq)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGetPodCliqueSetReplicaIndex,
			component.OperationSync,
			fmt.Sprintf("failed to get PCS replica index from PodClique %s", pclq.Name),
		)
	}

	sc.cliqueName, err = utils.GetPodCliqueNameFromPodCliqueFQN(pclq.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGetPodCliqueTemplate,
			component.OperationSync,
			fmt.Sprintf("failed to extract clique name from PodClique %s", pclq.Name),
		)
	}

	// get the PCLQ expectations key
	sc.pclqExpectationsStoreKey, err = getPodCliqueExpectationsStoreKey(logger, component.OperationSync, pclq.ObjectMeta)
	if err != nil {
		return nil, err
	}

	if !sc.isStandalonePCLQ {
		// get the associated PodGang name.
		sc.pcsgReplicaPodGangName, err = r.getAssociatedPodGangName(pclq.ObjectMeta)
		if err != nil {
			return nil, err
		}
	}

	// Get all existing pods for this PCLQ.
	sc.existingPCLQPods, err = componentutils.GetPCLQPods(ctx, r.client, sc.pcs.Name, pclq)
	if err != nil {
		logger.Error(err, "Failed to list pods that belong to PodClique")
		return nil, groveerr.WrapError(err,
			errCodeListPod,
			component.OperationSync,
			fmt.Sprintf("failed to list pods that belong to the PodClique %v", client.ObjectKeyFromObject(pclq)),
		)
	}

	// Fetch the PodGangMap for this PCS replica once per reconcile and cache it on the
	// sync context. NotFound is left as a soft signal (sc.pgm == nil) so downstream paths
	// (schedule-gate removal, status realignment) can short-circuit without erroring out
	// when the PGM has not yet been created by the PCS reconciler.
	pgm, err := componentutils.GetPodGangMapForPCSReplica(ctx, r.client, sc.pcs.Name, pclq.Namespace, sc.pcsReplicaIndex)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, groveerr.WrapError(err,
			errCodeGetPodGang,
			component.OperationSync,
			fmt.Sprintf("failed to get PodGangMap for PCS replica %d", sc.pcsReplicaIndex),
		)
	}
	if err == nil {
		sc.pgm = pgm
	}

	return sc, nil
}

// getPCSReplicaIndexFromPCLQ extracts the PCS replica index from the PCLQ's labels.
func getPCSReplicaIndexFromPCLQ(pclq *grovecorev1alpha1.PodClique) (int, error) {
	indexStr, ok := pclq.Labels[apicommon.LabelPodCliqueSetReplicaIndex]
	if !ok {
		return 0, fmt.Errorf("%s label missing on PodClique %s", apicommon.LabelPodCliqueSetReplicaIndex, pclq.Name)
	}
	idx, err := strconv.Atoi(indexStr)
	if err != nil {
		return 0, fmt.Errorf("%s label on PodClique %s is not a valid integer: %q", apicommon.LabelPodCliqueSetReplicaIndex, pclq.Name, indexStr)
	}
	return idx, nil
}

// getPodCliqueExpectationsStoreKey creates the PodClique key against which expectations will be stored in the ExpectationStore.
func getPodCliqueExpectationsStoreKey(logger logr.Logger, operation string, pclqObjMeta metav1.ObjectMeta) (string, error) {
	pclqObjKey := k8sutils.GetObjectKeyFromObjectMeta(pclqObjMeta)
	pclqExpStoreKey, err := expect.ControlleeKeyFunc(&grovecorev1alpha1.PodClique{ObjectMeta: pclqObjMeta})
	if err != nil {
		logger.Error(err, "failed to construct expectations store key", "pclq", pclqObjKey)
		return "", groveerr.WrapError(err,
			errCodeCreatePodCliqueExpectationsStoreKey,
			operation,
			fmt.Sprintf("failed to construct expectations store key for PodClique %v", pclqObjKey),
		)
	}
	return pclqExpStoreKey, nil
}

// getAssociatedPodGangName gets the associated PodGang name from PodClique labels. Returns an error if the label is not found.
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

// runSyncFlow executes the main synchronization logic for the PodClique:
//   - For standalone PCLQs, the status-driven distribution flow updates pclq.Status.PodGangMapping
//     and reconciles pods per-PodGang (subsumes the previous coherent-update branch).
//   - For PCSG-owned PCLQs, all pods belong to a single PodGang fixed at PCLQ creation, so a simple
//     spec-diff create/delete is sufficient.
//
// RollingRecreate handling and schedule-gate removal run after distribution reconciliation.
func (r _resource) runSyncFlow(logger logr.Logger, sc *syncContext) syncFlowResult {
	result := syncFlowResult{}

	if sc.isStandalonePCLQ {
		if err := r.reconcileStandalonePCLQDistribution(logger, sc); err != nil {
			logger.Error(err, "failed to reconcile standalone PodClique distribution")
			result.recordError(err)
		}
	} else {
		diff := r.syncExpectationsAndComputeDifference(logger, sc)
		if diff < 0 {
			logger.Info("found fewer pods than desired", "pclq.spec.replicas", sc.pclq.Spec.Replicas, "delta", diff)
			numCreated, err := r.createPods(sc.ctx, logger, sc, -diff)
			if err != nil {
				logger.Error(err, "failed to create pods")
				result.recordError(err)
			}
			logger.Info("Created pods for PodClique", "numCreated", numCreated)
		} else if diff > 0 {
			if err := r.deleteExcessPods(sc, logger, diff); err != nil {
				result.recordError(err)
			}
		}
	}

	if componentutils.IsPCLQAutoUpdateInProgress(sc.pclq) {
		if componentutils.IsRollingRecreateUpdateInProgress(sc.pcs) {
			if err := r.processPendingUpdates(logger, sc); err != nil {
				result.recordError(err)
			}
		} else if sc.isStandalonePCLQ && componentutils.IsCoherentUpdateInProgress(sc.pcs) {
			if err := r.checkAndMarkPCLQCoherentUpdateEnded(logger, sc); err != nil {
				result.recordError(err)
			}
		}
	}

	skippedScheduleGatedPods, err := r.checkAndRemovePodSchedulingGates(sc, logger)
	if err != nil {
		result.recordError(err)
	}
	result.recordPendingScheduleGatedPods(skippedScheduleGatedPods)
	return result
}

// syncExpectationsAndComputeDifference reconciles create/delete expectations with actual pod state and computes the replica difference
// It takes in the existing pods and adjusts the captured create/delete expectations in the ExpectationStore. Post synchronization
// it computes the difference of pods using => as-is-pods + pods-expecting-creation - desired-pods - pods-expecting-deletion
func (r _resource) syncExpectationsAndComputeDifference(logger logr.Logger, sc *syncContext) int {
	terminatingPodUIDs, nonTerminatingPodUIDs := getTerminatingAndNonTerminatingPodUIDs(sc.existingPCLQPods)
	r.expectationsStore.SyncExpectations(sc.pclqExpectationsStoreKey, nonTerminatingPodUIDs, terminatingPodUIDs)
	createExpectations := r.expectationsStore.GetCreateExpectations(sc.pclqExpectationsStoreKey)
	deleteExpectations := r.expectationsStore.GetDeleteExpectations(sc.pclqExpectationsStoreKey)
	diff := len(sc.existingPCLQPods) + len(createExpectations) - int(sc.pclq.Spec.Replicas) - len(deleteExpectations)

	logger.V(4).Info("synced expectations",
		"pclq.spec.replicas", sc.pclq.Spec.Replicas,
		"existingPCLPodNames", lo.Map(sc.existingPCLQPods, func(pod *corev1.Pod, _ int) string { return pod.Name }),
		"createExpectations", createExpectations,
		"deleteExpectations", deleteExpectations,
		"diff", diff,
	)
	return diff
}

// getTerminatingAndNonTerminatingPodUIDs categorizes pod UIDs based on termination status
func getTerminatingAndNonTerminatingPodUIDs(existingPCLQPods []*corev1.Pod) (terminatingUIDs, nonTerminatingUIDs []types.UID) {
	nonTerminatingUIDs = make([]types.UID, 0, len(existingPCLQPods))
	terminatingUIDs = make([]types.UID, 0, len(existingPCLQPods))
	for _, pod := range existingPCLQPods {
		if k8sutils.IsResourceTerminating(pod.ObjectMeta) {
			terminatingUIDs = append(terminatingUIDs, pod.GetUID())
		} else {
			nonTerminatingUIDs = append(nonTerminatingUIDs, pod.GetUID())
		}
	}
	return
}

// createPods creates the specified number of new pods for a PCSG-owned PodClique. All pods of
// a PCSG-owned PCLQ belong to a single PodGang (set on the PCLQ's LabelPodGang at creation by
// the PCSG reconciler), so every new pod inherits the same PodGang name. Standalone PCLQs use
// the status-driven flow (reconcileStandalonePCLQDistribution) and do not go through here.
func (r _resource) createPods(ctx context.Context, logger logr.Logger, sc *syncContext, numPods int) (int, error) {
	availableIndices, err := index.GetAvailableIndices(logger, sc.existingPCLQPods, numPods)
	if err != nil {
		return 0, groveerr.WrapError(err,
			errCodeGetAvailablePodHostNameIndices,
			component.OperationSync,
			fmt.Sprintf("error getting available indices for Pods in PodClique %v", client.ObjectKeyFromObject(sc.pclq)),
		)
	}

	createTasks := make([]utils.Task, 0, numPods)
	for i := range numPods {
		createTasks = append(createTasks, r.createPodCreationTask(logger, sc.pcs, sc.pclq, sc.pcsgReplicaPodGangName, sc.pclqExpectationsStoreKey, i, availableIndices[i]))
	}
	runResult := utils.RunConcurrentlyWithSlowStart(ctx, logger, 1, createTasks)
	if runResult.HasErrors() {
		err = runResult.GetAggregatedError()
		logger.Error(err, "failed to create pods for PCLQ", "runSummary", runResult.GetSummary())
		return 0, err
	}
	return len(runResult.SuccessfulTasks), nil
}

// deleteExcessPods deletes `diff` number of excess Pods from this PodClique concurrently.
// It selects the pods using `DeletionSorter`. For details please see `DeletionSorter.Less` method.
// The deletion of Pods are done in batches of increasing size. This is done to prevent burst of load
// on the kube-apiserver. It will fail fast in case there is an
func (r _resource) deleteExcessPods(sc *syncContext, logger logr.Logger, diff int) error {
	candidatePodsToDelete := selectExcessPodsToDelete(sc, logger)
	numPodsToSelectForDeletion := min(diff, len(candidatePodsToDelete))
	selectedPodsToDelete := candidatePodsToDelete[:numPodsToSelectForDeletion]

	deleteTasks := make([]utils.Task, 0, len(selectedPodsToDelete))
	for _, podToDelete := range selectedPodsToDelete {
		deleteTasks = append(deleteTasks, r.createPodDeletionTask(logger, sc.pclq, podToDelete, sc.pclqExpectationsStoreKey))
	}

	if runResult := utils.RunConcurrentlyWithSlowStart(sc.ctx, logger, 1, deleteTasks); runResult.HasErrors() {
		err := runResult.GetAggregatedError()
		pclqObjectKey := client.ObjectKeyFromObject(sc.pclq)
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

// selectExcessPodsToDelete identifies excess pods for deletion using DeletionSorter for prioritization
func selectExcessPodsToDelete(sc *syncContext, logger logr.Logger) []*corev1.Pod {
	var candidatePodsToDelete []*corev1.Pod
	if diff := len(sc.existingPCLQPods) - int(sc.pclq.Spec.Replicas); diff > 0 {
		logger.Info("found excess pods for PodClique", "numExcessPods", diff)
		sorter := DeletionSorter{
			Pods: sc.existingPCLQPods,
		}
		sorter.ExpectedPodTemplateHash = sc.getExpectedPodTemplateHash()
		sort.Sort(sorter)
		candidatePodsToDelete = append(candidatePodsToDelete, sorter.Pods[:diff]...)
	}
	return candidatePodsToDelete
}

func (sc *syncContext) getExpectedPodTemplateHash() string {
	if sc.pclq.Status.UpdateProgress != nil &&
		sc.pcs.Status.CurrentGenerationHash != nil &&
		sc.pclq.Status.UpdateProgress.PodCliqueSetGenerationHash == *sc.pcs.Status.CurrentGenerationHash {
		return sc.pclq.Status.UpdateProgress.PodTemplateHash
	}
	return sc.pclq.Labels[apicommon.LabelPodTemplateHash]
}

// checkAndRemovePodSchedulingGates removes the Grove PodGang scheduling gate from a pod once its
// PodGang membership is confirmed (the pod appears in the PodGang's PodReferences) and every epoch
// the pod's PodGangMap entry DependsOn has all of its PodGangs scheduled.
func (r _resource) checkAndRemovePodSchedulingGates(sc *syncContext, logger logr.Logger) ([]string, error) {
	skippedScheduleGatedPods := make([]string, 0, len(sc.existingPCLQPods))

	var gatedPods []*corev1.Pod
	for _, p := range sc.existingPCLQPods {
		if hasPodGangSchedulingGate(p) {
			gatedPods = append(gatedPods, p)
		}
	}
	if len(gatedPods) == 0 {
		return skippedScheduleGatedPods, nil
	}

	if sc.pgm == nil {
		// PodGangMap has not been created yet for this PCS replica; gate-removal cannot
		// proceed without it. Mark every gated pod as skipped and let the next reconcile
		// retry once the PCS reconciler creates the PGM.
		for _, p := range gatedPods {
			skippedScheduleGatedPods = append(skippedScheduleGatedPods, p.Name)
		}
		return skippedScheduleGatedPods, nil
	}

	entryByEpoch := componentutils.IndexPodGangEntriesByEpoch(sc.pgm.Spec.Entries)

	// Fetch each distinct PodGang the gated pods reference once. Pods can be many but share a small
	// set of PodGangs, so this keeps the GET count at the number of distinct PodGangs, not pods.
	pgByName, err := r.fetchGatedPodPodGangs(sc.ctx, gatedPods, sc.pclq.Namespace)
	if err != nil {
		return nil, err
	}

	// Resolve, once per distinct DependsOn epoch, whether all PodGangs at that epoch are scheduled.
	scheduledByEpoch, err := r.computeDependencyEpochScheduled(sc, pgByName)
	if err != nil {
		return nil, err
	}

	tasks := make([]utils.Task, 0, len(gatedPods))
	for i, p := range gatedPods {
		if !canRemoveSchedulingGate(logger, p, sc.pclq.Name, pgByName, entryByEpoch, scheduledByEpoch) {
			skippedScheduleGatedPods = append(skippedScheduleGatedPods, p.Name)
			continue
		}
		tasks = append(tasks, utils.Task{
			Name: fmt.Sprintf("RemoveSchedulingGate-%s-%d", p.Name, i),
			Fn: func(ctx context.Context) error {
				podClone := p.DeepCopy()
				// Remove only the Grove PodGang gate. Other controllers may add their own
				// scheduling gates on the same Pod; clearing the whole list would wipe those
				// and let the Pod schedule before those owners have released it.
				if !removePodGangSchedulingGate(p) {
					return nil
				}
				if err = client.IgnoreNotFound(r.client.Patch(ctx, p, client.MergeFrom(podClone))); err != nil {
					return err
				}
				logger.Info("Removed Grove PodGang scheduling gate from pod", "podObjectKey", client.ObjectKeyFromObject(p))
				return nil
			},
		})
	}

	if len(tasks) > 0 {
		if runResult := utils.RunConcurrentlyWithSlowStart(sc.ctx, logger, 1, tasks); runResult.HasErrors() {
			err := runResult.GetAggregatedError()
			logger.Error(err, "failed to remove scheduling gates from pods for PCLQ", "runSummary", runResult.GetSummary())
			return skippedScheduleGatedPods, groveerr.WrapError(err,
				errCodeRemovePodSchedulingGate,
				component.OperationSync,
				fmt.Sprintf("failed to remove scheduling gates from Pods for PodClique %v", client.ObjectKeyFromObject(sc.pclq)),
			)
		}
	}

	return skippedScheduleGatedPods, nil
}

// fetchGatedPodPodGangs fetches, once each, the distinct PodGangs named by the grove.io/podgang
// label on the gated pods. A pod missing that label is a controller bug and returns a hard error.
// A PodGang that does not exist yet is recorded as a nil value so callers can skip its pods.
func (r _resource) fetchGatedPodPodGangs(ctx context.Context, gatedPods []*corev1.Pod, namespace string) (map[string]*groveschedulerv1alpha1.PodGang, error) {
	pgByName := make(map[string]*groveschedulerv1alpha1.PodGang)
	for _, pod := range gatedPods {
		name, ok := pod.Labels[apicommon.LabelPodGang]
		if !ok {
			return nil, groveerr.New(errCodeMissingPodGangLabelOnPod, component.OperationSync,
				fmt.Sprintf("gated pod %v is missing required label %s", client.ObjectKeyFromObject(pod), apicommon.LabelPodGang))
		}
		if _, seen := pgByName[name]; seen {
			continue
		}
		pg, err := componentutils.GetPodGang(ctx, r.client, name, namespace)
		if err != nil {
			if apierrors.IsNotFound(err) {
				pgByName[name] = nil
				continue
			}
			return nil, groveerr.WrapError(err, errCodeGetPodGang, component.OperationSync,
				fmt.Sprintf("failed to get PodGang %v", client.ObjectKey{Namespace: namespace, Name: name}))
		}
		pgByName[name] = pg
	}
	return pgByName, nil
}

// computeDependencyEpochScheduled returns, for each epoch that the gated pods' PodGangs depend on,
// whether that epoch is fully scheduled. A gated pod's PodGang maps to a PodGangMap entry (by the
// PodGang's epoch label), and that entry's DependsOn lists the epochs whose PodGangs must be
// scheduled before this pod's gate may be lifted. An epoch counts as scheduled only when every
// PodGang created at that epoch reports LastScheduled non-nil. Each distinct dependency epoch is
// resolved with a single List. The result is keyed by dependency epoch.
func (r _resource) computeDependencyEpochScheduled(sc *syncContext, pgByName map[string]*groveschedulerv1alpha1.PodGang) (map[string]bool, error) {
	entryByEpoch := componentutils.IndexPodGangEntriesByEpoch(sc.pgm.Spec.Entries)
	depEpochs := sets.New[string]()
	for _, pg := range pgByName {
		if pg == nil {
			continue
		}
		if entry, ok := entryByEpoch[pg.Labels[apicommon.LabelEpoch]]; ok {
			depEpochs.Insert(entry.DependsOn...)
		}
	}
	scheduledByEpoch := make(map[string]bool, depEpochs.Len())
	for _, epoch := range sets.List(depEpochs) {
		scheduled, err := componentutils.AllPodGangsAtEpochEverScheduled(sc.ctx, r.client, sc.pcs.Namespace, sc.pcs.Name, int32(sc.pcsReplicaIndex), epoch)
		if err != nil {
			return nil, err
		}
		scheduledByEpoch[epoch] = scheduled
	}
	return scheduledByEpoch, nil
}

// canRemoveSchedulingGate returns true when pod's scheduling gate can be lifted: its PodGang exists and records the pod
// in PodReferences, the PodGang's epoch resolves to a PodGangMap entry, and every epoch that entry
// DependsOn is scheduled. It reads only prefetched maps, so it makes no API calls.
func canRemoveSchedulingGate(logger logr.Logger, pod *corev1.Pod, pclqName string, pgByName map[string]*groveschedulerv1alpha1.PodGang, entryByEpoch map[string]grovecorev1alpha1.PodGangEntry, scheduledByEpoch map[string]bool) bool {
	podObjectKey := client.ObjectKeyFromObject(pod)
	podGangName := pod.Labels[apicommon.LabelPodGang] // presence guaranteed by fetchGatedPodPodGangs

	pg := pgByName[podGangName]
	if pg == nil {
		logger.Info("PodGang resource not found, skipping pod", "podObjectKey", podObjectKey, "podGangName", podGangName)
		return false
	}
	if !isPodInPodReferences(pg, pclqName, pod.Name) {
		logger.Info("Pod not yet recorded in PodGang PodReferences, skipping", "podObjectKey", podObjectKey, "podGangName", podGangName)
		return false
	}
	entry, ok := entryByEpoch[pg.Labels[apicommon.LabelEpoch]]
	if !ok {
		logger.Info("PodGangMap entry not found for pod's PodGang epoch, skipping", "podObjectKey", podObjectKey, "podGangName", podGangName)
		return false
	}
	for _, depEpoch := range entry.DependsOn {
		if !scheduledByEpoch[depEpoch] {
			logger.Info("Dependency epoch not yet scheduled, skipping gate removal", "podObjectKey", podObjectKey, "dependsOn", entry.DependsOn, "pendingEpoch", depEpoch)
			return false
		}
	}
	return true
}

// isPodInPodReferences returns true if podName appears in the PodGroup for pclqFQN in pg.
func isPodInPodReferences(pg *groveschedulerv1alpha1.PodGang, pclqFQN, podName string) bool {
	for _, podGroup := range pg.Spec.PodGroups {
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

// Convenience functions, types and methods on these types that are used during sync flow run.
// ------------------------------------------------------------------------------------------------

// syncContext holds the relevant state required during the sync flow run.
type syncContext struct {
	ctx              context.Context
	pcs              *grovecorev1alpha1.PodCliqueSet
	pclq             *grovecorev1alpha1.PodClique
	pgm              *grovecorev1alpha1.PodGangMap
	isStandalonePCLQ bool
	pcsReplicaIndex  int
	cliqueName       string
	// pcsReplicaPodGangName is the name of the PodGang associated to a PCSG replica.
	// All constituent PCLQs will share a common PodGang, all Pods of all PCLQs in a PCSG will inherit it from the PCLQ.
	pcsgReplicaPodGangName   string
	existingPCLQPods         []*corev1.Pod
	pclqExpectationsStoreKey string
	expectedPodTemplateHash  string
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

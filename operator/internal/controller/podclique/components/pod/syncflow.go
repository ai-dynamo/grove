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

	sc.expectedPodTemplateHashes, err = componentutils.GetExpectedPCLQPodTemplateHashCandidates(sc.pcs, pclq.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGetPodCliqueTemplate,
			component.OperationSync,
			fmt.Sprintf("failed to compute pod clique template hash for PodClique: %v in PodCliqueSet", client.ObjectKeyFromObject(pclq)),
		)
	}
	sc.expectedPodTemplateHash = sc.expectedPodTemplateHashes.Canonical

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
	if err = r.migrateLegacyCurrentPodLabels(ctx, logger, sc); err != nil {
		return nil, err
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
			Pods:                      sc.existingPCLQPods,
			ExpectedPodTemplateHashes: sc.expectedPodTemplateHashes,
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

// Pods carry the PodClique pod-template hash in a label so the controller can
// tell which desired template created each live pod. Older controllers hashed
// the same template before canonicalizing Kubernetes map-list fields, so those
// pods may have a legacy hash that should converge without a rollout.
func (r _resource) migrateLegacyCurrentPodLabels(ctx context.Context, logger logr.Logger, sc *syncContext) error {
	for _, pod := range sc.existingPCLQPods {
		if !sc.expectedPodTemplateHashes.IsLegacy(pod.Labels[apicommon.LabelPodTemplateHash]) {
			continue
		}
		patch := client.MergeFrom(pod.DeepCopy())
		if pod.Labels == nil {
			pod.Labels = make(map[string]string, 1)
		}
		pod.Labels[apicommon.LabelPodTemplateHash] = sc.expectedPodTemplateHashes.Canonical
		if err := client.IgnoreNotFound(r.client.Patch(ctx, pod, patch)); err != nil {
			return groveerr.WrapError(err,
				errCodeBuildPodResource,
				component.OperationSync,
				fmt.Sprintf("failed to migrate legacy PodTemplateHash label for Pod %v", client.ObjectKeyFromObject(pod)),
			)
		}
		logger.Info("migrated legacy PodTemplateHash label on Pod", "pod", client.ObjectKeyFromObject(pod), "podTemplateHash", sc.expectedPodTemplateHashes.Canonical)
	}
	return nil
}

// checkAndRemovePodSchedulingGates removes scheduling gates from pods when their PodGang
// membership is confirmed and all DependsOn PodGangs in the PodGangMap entry are scheduled.
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

	entryByName := lo.KeyBy(sc.pgm.Spec.Entries, func(e grovecorev1alpha1.PodGangEntry) string { return e.Name })
	pgCache := make(map[string]*groveschedulerv1alpha1.PodGang)
	depScheduled := make(map[string]bool)

	tasks := make([]utils.Task, 0, len(gatedPods))
	for i, p := range gatedPods {
		remove, err := r.shouldRemoveSchedulingGate(sc, logger, p, entryByName, pgCache, depScheduled)
		if err != nil {
			return nil, err
		}
		if !remove {
			skippedScheduleGatedPods = append(skippedScheduleGatedPods, p.Name)
			continue
		}
		tasks = append(tasks, utils.Task{
			Name: fmt.Sprintf("RemoveSchedulingGate-%s-%d", p.Name, i),
			Fn: func(ctx context.Context) error {
				podClone := p.DeepCopy()
				p.Spec.SchedulingGates = nil
				if err := client.IgnoreNotFound(r.client.Patch(ctx, p, client.MergeFrom(podClone))); err != nil {
					return err
				}
				logger.Info("Removed scheduling gate from pod", "podObjectKey", client.ObjectKeyFromObject(p))
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

// shouldRemoveSchedulingGate returns true when all conditions for gate removal are met for p.
// Returns a hard error only for controller bugs (missing LabelPodGang) or unexpected API failures.
func (r _resource) shouldRemoveSchedulingGate(
	sc *syncContext,
	logger logr.Logger,
	p *corev1.Pod,
	entryByName map[string]grovecorev1alpha1.PodGangEntry,
	pgCache map[string]*groveschedulerv1alpha1.PodGang,
	depScheduled map[string]bool,
) (bool, error) {
	podObjectKey := client.ObjectKeyFromObject(p)

	podGangName, ok := p.Labels[apicommon.LabelPodGang]
	if !ok {
		return false, groveerr.New(errCodeMissingPodGangLabelOnPod,
			component.OperationSync,
			fmt.Sprintf("gated pod %v is missing required label %s", podObjectKey, apicommon.LabelPodGang),
		)
	}

	entry, ok := entryByName[podGangName]
	if !ok {
		logger.Info("PodGangMap entry not found for pod's PodGang, skipping", "podObjectKey", podObjectKey, "podGangName", podGangName)
		return false, nil
	}

	pg, err := r.getOrFetchPodGang(sc.ctx, pgCache, podGangName, sc.pclq.Namespace)
	if err != nil {
		return false, err
	}
	if pg == nil {
		logger.Info("PodGang resource not found, skipping pod", "podObjectKey", podObjectKey, "podGangName", podGangName)
		return false, nil
	}

	if !isPodInPodReferences(pg, sc.pclq.Name, p.Name) {
		logger.Info("Pod not yet recorded in PodGang PodReferences, skipping", "podObjectKey", podObjectKey, "podGangName", podGangName)
		return false, nil
	}

	depsScheduled, err := r.areDependenciesScheduled(sc.ctx, logger, entry.DependsOn, sc.pclq.Namespace, pgCache, depScheduled)
	if err != nil {
		return false, err
	}
	if !depsScheduled {
		logger.Info("Dependencies not yet scheduled, skipping gate removal", "podObjectKey", podObjectKey, "dependsOn", entry.DependsOn)
		return false, nil
	}

	return true, nil
}

// getOrFetchPodGang returns a PodGang from the cache, fetching it from the API if not present.
// Returns (nil, nil) when the PodGang does not exist.
func (r _resource) getOrFetchPodGang(ctx context.Context, cache map[string]*groveschedulerv1alpha1.PodGang, name, namespace string) (*groveschedulerv1alpha1.PodGang, error) {
	if pg, ok := cache[name]; ok {
		return pg, nil
	}
	pg, err := componentutils.GetPodGang(ctx, r.client, name, namespace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			cache[name] = nil
			return nil, nil
		}
		return nil, groveerr.WrapError(err,
			errCodeGetPodGang,
			component.OperationSync,
			fmt.Sprintf("failed to get PodGang %v", client.ObjectKey{Namespace: namespace, Name: name}),
		)
	}
	cache[name] = pg
	return pg, nil
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

// areDependenciesScheduled returns true when every PodGang in depNames has
// ScheduledReplicas >= MinReplicas for each of its PodGroups.
func (r _resource) areDependenciesScheduled(ctx context.Context, logger logr.Logger, depNames []string, namespace string, pgCache map[string]*groveschedulerv1alpha1.PodGang, depScheduled map[string]bool) (bool, error) {
	for _, depName := range depNames {
		if scheduled, seen := depScheduled[depName]; seen {
			if !scheduled {
				return false, nil
			}
			continue
		}
		pg, err := r.getOrFetchPodGang(ctx, pgCache, depName, namespace)
		if err != nil {
			return false, err
		}
		if pg == nil {
			depScheduled[depName] = false
			return false, nil
		}
		scheduled, err := r.isPodGangScheduled(ctx, logger, namespace, pg)
		if err != nil {
			return false, err
		}
		depScheduled[depName] = scheduled
		if !scheduled {
			return false, nil
		}
	}
	return true, nil
}

// isPodGangScheduled returns true when every PodGroup in the PodGang has
// ScheduledReplicas >= MinReplicas in its corresponding PodClique.
func (r _resource) isPodGangScheduled(ctx context.Context, logger logr.Logger, namespace string, pg *groveschedulerv1alpha1.PodGang) (bool, error) {
	for _, podGroup := range pg.Spec.PodGroups {
		pclq := &grovecorev1alpha1.PodClique{}
		if err := r.client.Get(ctx, client.ObjectKey{Name: podGroup.Name, Namespace: namespace}, pclq); err != nil {
			return false, groveerr.WrapError(err,
				errCodeGetPodClique,
				component.OperationSync,
				fmt.Sprintf("failed to get PodClique %s in namespace %s for PodGang scheduled check", podGroup.Name, namespace),
			)
		}
		if pclq.Status.ScheduledReplicas < podGroup.MinReplicas {
			logger.Info("PodGang not scheduled: PodClique has insufficient scheduled replicas",
				"podGangName", pg.Name,
				"pclqName", podGroup.Name,
				"scheduledReplicas", pclq.Status.ScheduledReplicas,
				"minReplicas", podGroup.MinReplicas)
			return false, nil
		}
	}
	return true, nil
}

// hasPodGangSchedulingGate checks if a pod has the PodGang scheduling gate
func hasPodGangSchedulingGate(pod *corev1.Pod) bool {
	return slices.ContainsFunc(pod.Spec.SchedulingGates, func(schedulingGate corev1.PodSchedulingGate) bool {
		return podGangSchedulingGate == schedulingGate.Name
	})
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
	pcsgReplicaPodGangName    string
	existingPCLQPods          []*corev1.Pod
	pclqExpectationsStoreKey  string
	expectedPodTemplateHash   string
	expectedPodTemplateHashes componentutils.HashCandidates
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

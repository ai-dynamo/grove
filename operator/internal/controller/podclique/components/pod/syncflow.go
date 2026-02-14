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

	"github.com/ai-dynamo/grove/operator/api/common"
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

	sc.expectedPodTemplateHash, err = componentutils.GetExpectedPCLQPodTemplateHash(sc.pcs, pclq.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGetPodCliqueTemplate,
			component.OperationSync,
			fmt.Sprintf("failed to compute pod clique template hash for PodClique: %v in PodCliqueSet", client.ObjectKeyFromObject(pclq)),
		)
	}

	// get the PCLQ expectations key
	sc.pclqExpectationsStoreKey, err = getPodCliqueExpectationsStoreKey(logger, component.OperationSync, pclq.ObjectMeta)
	if err != nil {
		return nil, err
	}

	// get the associated PodGang name.
	sc.associatedPodGangName, err = r.getAssociatedPodGangName(pclq.ObjectMeta)
	if err != nil {
		return nil, err
	}

	// Get the associated PodGang resource.
	existingPodGang, err := componentutils.GetPodGang(ctx, r.client, sc.associatedPodGangName, pclq.Namespace)
	if err = lo.Ternary(apierrors.IsNotFound(err), nil, err); err != nil {
		return nil, err
	}

	// initialize the Pod names that are updated in the PodGang resource for this PCLQ.
	sc.podNamesUpdatedInPCLQPodGangs = r.getPodNamesUpdatedInAssociatedPodGang(existingPodGang, pclq.Name)

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

	return sc, nil
}

// getAssociatedPodGangName gets the associated PodGang name from PodClique labels. Returns an error if the label is not found.
func (r _resource) getAssociatedPodGangName(pclqObjectMeta metav1.ObjectMeta) (string, error) {
	podGangName, ok := pclqObjectMeta.GetLabels()[common.LabelPodGang]
	if !ok {
		return "", groveerr.New(errCodeMissingPodGangLabelOnPCLQ,
			component.OperationSync,
			fmt.Sprintf("PodClique: %v is missing required label: %s", k8sutils.GetObjectKeyFromObjectMeta(pclqObjectMeta), common.LabelPodGang),
		)
	}
	return podGangName, nil
}

// getPodNamesUpdatedInAssociatedPodGang gathers all Pod names that are already updated in PodGroups defined in the PodGang resource.
func (r _resource) getPodNamesUpdatedInAssociatedPodGang(existingPodGang *groveschedulerv1alpha1.PodGang, pclqFQN string) []string {
	if existingPodGang == nil {
		return nil
	}
	podGroup, ok := lo.Find(existingPodGang.Spec.PodGroups, func(podGroup groveschedulerv1alpha1.PodGroup) bool {
		return podGroup.Name == pclqFQN
	})
	if !ok {
		return nil
	}
	return lo.Map(podGroup.PodReferences, func(nsName groveschedulerv1alpha1.NamespacedName, _ int) string {
		return nsName.Name
	})
}

// runSyncFlow executes the main synchronization logic including pod creation, deletion, updates, and scheduling gate management
func (r _resource) runSyncFlow(logger logr.Logger, sc *syncContext) syncFlowResult {
	result := syncFlowResult{}
	diff := r.syncExpectationsAndComputeDifference(logger, sc)
	if diff < 0 {
		logger.Info("found fewer pods than desired", "pclq.spec.replicas", sc.pclq.Spec.Replicas, "delta", diff)
		diff *= -1
		numScheduleGatedPods, err := r.createPods(sc.ctx, logger, sc, diff)
		if err != nil {
			logger.Error(err, "failed to create pods")
			result.recordError(err)
		}
		logger.Info("created unassigned and scheduled gated pods", "numberOfCreatedPods", numScheduleGatedPods)
	} else if diff > 0 {
		if err := r.deleteExcessPods(sc, logger, diff); err != nil {
			result.recordError(err)
		}
	}

	if componentutils.IsPCLQUpdateInProgress(sc.pclq) {
		if err := r.processPendingUpdates(logger, sc); err != nil {
			result.recordError(err)
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
		sort.Sort(DeletionSorter(sc.existingPCLQPods))
		candidatePodsToDelete = append(candidatePodsToDelete, sc.existingPCLQPods[:diff]...)
	}
	return candidatePodsToDelete
}

// checkAndRemovePodSchedulingGates removes scheduling gates from pods when their dependencies are satisfied
func (r _resource) checkAndRemovePodSchedulingGates(sc *syncContext, logger logr.Logger) ([]string, error) {
	tasks := make([]utils.Task, 0, len(sc.existingPCLQPods))
	skippedScheduleGatedPods := make([]string, 0, len(sc.existingPCLQPods))

	// Pre-compute if the base PodGang is scheduled once for all pods in this PodClique
	// All pods in the same PodClique have the same base PodGang
	basePodGangScheduled, basePodGangName, err := r.checkBasePodGangScheduledForPodClique(sc.ctx, logger, sc.pclq)
	if err != nil {
		logger.Error(err, "Error checking if base PodGang is scheduled for PodClique - will requeue")
		return nil, groveerr.WrapError(err,
			errCodeRemovePodSchedulingGate,
			component.OperationSync,
			"failed to check if base PodGang is scheduled for PodClique",
		)
	}

	for i, p := range sc.existingPCLQPods {
		if hasPodGangSchedulingGate(p) {
			podObjectKey := client.ObjectKeyFromObject(p)
			if !slices.Contains(sc.podNamesUpdatedInPCLQPodGangs, p.Name) {
				logger.Info("Pod has scheduling gate but it has not yet been updated in PodGang", "podObjectKey", podObjectKey)
				skippedScheduleGatedPods = append(skippedScheduleGatedPods, p.Name)
				continue
			}
			shouldSkip := r.shouldSkipPodSchedulingGateRemoval(logger, p, basePodGangScheduled, basePodGangName)
			if shouldSkip {
				skippedScheduleGatedPods = append(skippedScheduleGatedPods, p.Name)
				continue
			}
			task := utils.Task{
				Name: fmt.Sprintf("RemoveSchedulingGate-%s-%d", p.Name, i),
				Fn: func(ctx context.Context) error {
					podClone := p.DeepCopy()
					p.Spec.SchedulingGates = nil
					if err := client.IgnoreNotFound(r.client.Patch(ctx, p, client.MergeFrom(podClone))); err != nil {
						return err
					}
					logger.Info("Removed scheduling gate from pod", "podObjectKey", podObjectKey)
					return nil
				},
			}
			tasks = append(tasks, task)
		}
	}

	if len(tasks) > 0 {
		pclqObjectKey := client.ObjectKeyFromObject(sc.pclq)
		if runResult := utils.RunConcurrentlyWithSlowStart(sc.ctx, logger, 1, tasks); runResult.HasErrors() {
			err := runResult.GetAggregatedError()
			logger.Error(err, "failed to remove scheduling gates from pods for PCLQ", "runSummary", runResult.GetSummary())
			return skippedScheduleGatedPods, groveerr.WrapError(err,
				errCodeRemovePodSchedulingGate,
				component.OperationSync,
				fmt.Sprintf("failed to remove scheduling gates from Pods for PodClique %v", pclqObjectKey),
			)
		}
	}

	return skippedScheduleGatedPods, nil
}

// isBasePodGangScheduled checks if the base PodGang (identified by name) is scheduled, returning errors only for actual API failures.
// A base PodGang is considered "scheduled" when ALL of its constituent PodCliques have achieved
// their minimum required number of scheduled pods (PodClique.Status.ScheduledReplicas >= PodGroup.MinReplicas).
//
// If the base PodGang does not exist (NotFound), this returns (false, nil) rather than an error.
// This is intentional because NotFound is a legitimate transient state that occurs during:
//   - Initial PodCliqueSet creation (PodGang created after pods are assigned)
//   - Gang termination and recreation (old PodGang deleted, new one not yet created)
//
// Returning nil allows the caller to handle this gracefully with a normal requeue interval,
// rather than triggering exponential backoff which would slow down recreation after gang termination.
func (r _resource) isBasePodGangScheduled(ctx context.Context, logger logr.Logger, namespace, basePodGangName string) (bool, error) {
	basePodGang, err := componentutils.GetPodGang(ctx, r.client, basePodGangName, namespace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Base PodGang doesn't exist yet - treat same as "not scheduled".
			// This happens during initial creation or recreation after gang termination.
			// The PodCliqueSet controller will create it once all pods are assigned.
			logger.Info("Base PodGang not found, treating as not scheduled",
				"basePodGangName", basePodGangName)
			return false, nil
		}
		// Real API errors (network, RBAC, etc.) should still trigger error handling
		return false, groveerr.WrapError(err,
			errCodeGetPodGang,
			component.OperationSync,
			fmt.Sprintf("failed to get base PodGang %v", client.ObjectKey{Namespace: namespace, Name: basePodGangName}),
		)
	}

	// Check if all PodGroups in the base PodGang have sufficient ready replicas
	// Each PodGroup represents a PodClique within the base PodGang and must meet its MinReplicas requirement
	for _, podGroup := range basePodGang.Spec.PodGroups {
		pclqName := podGroup.Name
		pclq := &grovecorev1alpha1.PodClique{}
		pclqKey := client.ObjectKey{Name: pclqName, Namespace: namespace}
		if err = r.client.Get(ctx, pclqKey, pclq); err != nil {
			// All errors (including NotFound) should trigger requeue for reliable retry
			// This ensures PodClique exists before we evaluate base PodGang readiness
			return false, groveerr.WrapError(err,
				errCodeGetPodClique,
				component.OperationSync,
				fmt.Sprintf("failed to get PodClique %s in namespace %s for base PodGang readiness check", pclqName, namespace),
			)
		}

		if pclq.Status.ScheduledReplicas < podGroup.MinReplicas {
			logger.Info("Base PodGang not scheduled: PodClique has insufficient scheduled replicas",
				"basePodGangName", basePodGangName,
				"pclqName", pclqName,
				"scheduledReplicas", pclq.Status.ScheduledReplicas,
				"minReplicas", podGroup.MinReplicas)
			return false, nil // Not ready, but no error - legitimate state
		}
	}

	logger.Info("Base PodGang is ready - all PodCliques meet MinAvailable requirements", "basePodGangName", basePodGangName)
	return true, nil
}

// checkBasePodGangScheduledForPodClique determines if there's a base PodGang for the PodClique. If there is one,
// this function checks if it is scheduled.
func (r _resource) checkBasePodGangScheduledForPodClique(ctx context.Context, logger logr.Logger, pclq *grovecorev1alpha1.PodClique) (bool, string, error) {
	// Check if this PodClique has a base PodGang dependency
	basePodGangName, hasBasePodGangLabel := pclq.GetLabels()[common.LabelBasePodGang]
	if !hasBasePodGangLabel {
		// This PodClique is a base PodGang itself - no dependency
		return true, "", nil
	}

	scheduled, err := r.isBasePodGangScheduled(ctx, logger, pclq.Namespace, basePodGangName)
	if err != nil {
		return false, basePodGangName, err
	}

	return scheduled, basePodGangName, nil
}

// shouldSkipPodSchedulingGateRemoval implements the core PodGang scheduling gate logic.
// It returns true if the pod scheduling gate removal should be skipped, false otherwise.
func (r _resource) shouldSkipPodSchedulingGateRemoval(logger logr.Logger, pod *corev1.Pod, basePodGangReady bool, basePodGangName string) bool {
	if basePodGangName == "" {
		// BASE PODGANG POD: This PodClique has no base PodGang dependency
		// These pods form the core gang and get their gates removed immediately once assigned to PodGang
		// They represent the minimum viable cluster (first minAvailable replicas) that must start together
		logger.Info("Proceeding with gate removal for base PodGang pod",
			"podObjectKey", client.ObjectKeyFromObject(pod))
		return false
	}
	// SCALED PODGANG POD: This PodClique depends on a base PodGang
	if basePodGangReady {
		logger.Info("Base PodGang is ready, proceeding with gate removal for scaled PodGang pod",
			"podObjectKey", client.ObjectKeyFromObject(pod),
			"basePodGangName", basePodGangName)
		return false
	}
	logger.Info("Scaled PodGang pod has scheduling gate but base PodGang is not ready yet, skipping scheduling gate removal",
		"podObjectKey", client.ObjectKeyFromObject(pod),
		"basePodGangName", basePodGangName)
	return true
}

// hasPodGangSchedulingGate checks if a pod has the PodGang scheduling gate
func hasPodGangSchedulingGate(pod *corev1.Pod) bool {
	return slices.ContainsFunc(pod.Spec.SchedulingGates, func(schedulingGate corev1.PodSchedulingGate) bool {
		return podGangSchedulingGate == schedulingGate.Name
	})
}

// createPods creates the specified number of new pods for the PodClique with proper indexing and concurrency control
func (r _resource) createPods(ctx context.Context, logger logr.Logger, sc *syncContext, numPods int) (int, error) {
	// Pre-calculate all needed indices to avoid race conditions
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
		// Get the available Pod host name index. This ensures that we fill the holes in the indices if there are any when creating
		// new pods.
		podHostNameIndex := availableIndices[i]
		createTasks = append(createTasks, r.createPodCreationTask(logger, sc.pcs, sc.pclq, sc.associatedPodGangName, sc.pclqExpectationsStoreKey, i, podHostNameIndex))
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

// syncContext holds the relevant state required during the sync flow run.
type syncContext struct {
	ctx                           context.Context
	pcs                           *grovecorev1alpha1.PodCliqueSet
	pclq                          *grovecorev1alpha1.PodClique
	associatedPodGangName         string
	existingPCLQPods              []*corev1.Pod
	podNamesUpdatedInPCLQPodGangs []string
	pclqExpectationsStoreKey      string
	expectedPodTemplateHash       string
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

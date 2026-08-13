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
func (r _resource) prepareSyncFlow(ctx context.Context, logger logr.Logger, pclq *grovecorev1alpha1.PodClique) (*syncSnapshot, error) {
	var (
		ss  = &syncSnapshot{pclq: pclq}
		err error
	)

	// Get associated PodCliqueSet for this PodClique.
	ss.pcs, err = componentutils.GetPodCliqueSet(ctx, r.client, pclq.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGetPodCliqueSet,
			component.OperationSync,
			fmt.Sprintf("failed to get owner PodCliqueSet of PodClique: %v", client.ObjectKeyFromObject(pclq)),
		)
	}

	ss.expectedPodTemplateHash, err = componentutils.GetExpectedPCLQPodTemplateHash(ss.pcs, pclq.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGetPodCliqueTemplate,
			component.OperationSync,
			fmt.Sprintf("failed to compute pod clique template hash for PodClique: %v in PodCliqueSet", client.ObjectKeyFromObject(pclq)),
		)
	}

	// get the PCLQ expectations key
	ss.pclqExpectationsStoreKey, err = getPodCliqueExpectationsStoreKey(logger, component.OperationSync, pclq.ObjectMeta)
	if err != nil {
		return nil, err
	}

	// get the associated PodGang name.
	ss.associatedPodGangName, err = r.getAssociatedPodGangName(pclq.ObjectMeta)
	if err != nil {
		return nil, err
	}

	// Get the associated PodGang resource.
	existingPodGang, err := componentutils.GetPodGang(ctx, r.client, ss.associatedPodGangName, pclq.Namespace)
	if err = lo.Ternary(apierrors.IsNotFound(err), nil, err); err != nil {
		return nil, err
	}
	// Record the associated PodGang's epoch. It is empty when the PodGang is not materialized yet (a
	// Grove PodGang always carries the epoch label), which gate-removal treats as "dependency not yet
	// resolvable".
	if existingPodGang != nil {
		ss.associatedPodGangEpoch = existingPodGang.Labels[apicommon.LabelEpoch]
	}

	// The PodGangMap for this PCS replica carries the DependsOn epochs that gate-removal reads. It is
	// created before any PodClique, so it is expected to exist; a missing PodGangMap is requeued.
	ss.pcsReplicaIndex, err = k8sutils.GetPodCliqueSetReplicaIndex(pclq.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGetPodGangMap,
			component.OperationSync,
			fmt.Sprintf("failed to determine PodCliqueSet replica index for PodClique: %v", client.ObjectKeyFromObject(pclq)),
		)
	}
	ss.pgm, err = componentutils.GetPodGangMap(ctx, r.client, client.ObjectKeyFromObject(ss.pcs), ss.pcsReplicaIndex)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGetPodGangMap,
			component.OperationSync,
			fmt.Sprintf("failed to get PodGangMap for PodCliqueSet: %v, PCS replica index: %d", client.ObjectKeyFromObject(ss.pcs), ss.pcsReplicaIndex),
		)
	}

	// initialize the Pod names that are updated in the PodGang resource for this PCLQ.
	ss.podNamesUpdatedInPCLQPodGangs = r.getPodNamesUpdatedInAssociatedPodGang(existingPodGang, pclq.Name)
	ss.podNamesUpdatedInPCLQPodGangSet = componentutils.NewSet(ss.podNamesUpdatedInPCLQPodGangs)

	// Get all existing pods for this PCLQ.
	ss.existingPCLQPods, err = componentutils.GetPCLQPods(ctx, r.client, ss.pcs.Name, pclq)
	if err != nil {
		logger.Error(err, "Failed to list pods that belong to PodClique")
		return nil, groveerr.WrapError(err,
			errCodeListPod,
			component.OperationSync,
			fmt.Sprintf("failed to list pods that belong to the PodClique %v", client.ObjectKeyFromObject(pclq)),
		)
	}

	return ss, nil
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
func (r _resource) runSyncFlow(ctx context.Context, logger logr.Logger, ss *syncSnapshot) syncFlowResult {
	result := syncFlowResult{}
	diff := r.syncExpectationsAndComputeDifference(logger, ss)
	if diff < 0 {
		logger.Info("found fewer pods than desired", "pclq.spec.replicas", ss.pclq.Spec.Replicas, "delta", diff)
		diff *= -1
		numScheduleGatedPods, err := r.createPods(ctx, logger, ss, diff)
		if err != nil {
			logger.Error(err, "failed to create pods")
			result.recordError(err)
		}
		logger.Info("created unassigned and scheduled gated pods", "numberOfCreatedPods", numScheduleGatedPods)
	} else if diff > 0 {
		if err := r.deleteExcessPods(ctx, logger, ss, diff); err != nil {
			result.recordError(err)
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

// syncExpectationsAndComputeDifference reconciles create/delete expectations with actual pod state and computes the replica difference
// It takes in the existing pods and adjusts the captured create/delete expectations in the ExpectationStore. Post synchronization
// it computes the difference of pods using => as-is-pods + pods-expecting-creation - desired-pods - pods-expecting-deletion
func (r _resource) syncExpectationsAndComputeDifference(logger logr.Logger, ss *syncSnapshot) int {
	terminatingPodUIDs, nonTerminatingPodUIDs := getTerminatingAndNonTerminatingPodUIDs(ss.existingPCLQPods)
	r.expectationsStore.SyncExpectations(ss.pclqExpectationsStoreKey, nonTerminatingPodUIDs, terminatingPodUIDs)
	createExpectations := r.expectationsStore.GetCreateExpectations(ss.pclqExpectationsStoreKey)
	deleteExpectations := r.expectationsStore.GetDeleteExpectations(ss.pclqExpectationsStoreKey)
	diff := len(ss.existingPCLQPods) + len(createExpectations) - int(ss.pclq.Spec.Replicas) - len(deleteExpectations)

	logger.V(4).Info("synced expectations",
		"pclq.spec.replicas", ss.pclq.Spec.Replicas,
		"existingPCLPodNames", lo.Map(ss.existingPCLQPods, func(pod *corev1.Pod, _ int) string { return pod.Name }),
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
func (r _resource) deleteExcessPods(ctx context.Context, logger logr.Logger, ss *syncSnapshot, diff int) error {
	candidatePodsToDelete := selectExcessPodsToDelete(ss, logger)
	numPodsToSelectForDeletion := min(diff, len(candidatePodsToDelete))
	selectedPodsToDelete := candidatePodsToDelete[:numPodsToSelectForDeletion]

	deleteTasks := make([]utils.Task, 0, len(selectedPodsToDelete))
	for _, podToDelete := range selectedPodsToDelete {
		deleteTasks = append(deleteTasks, r.createPodDeletionTask(logger, ss.pclq, podToDelete, ss.pclqExpectationsStoreKey))
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

// selectExcessPodsToDelete identifies excess pods for deletion using DeletionSorter for prioritization
func selectExcessPodsToDelete(ss *syncSnapshot, logger logr.Logger) []*corev1.Pod {
	var candidatePodsToDelete []*corev1.Pod
	if diff := len(ss.existingPCLQPods) - int(ss.pclq.Spec.Replicas); diff > 0 {
		logger.Info("found excess pods for PodClique", "numExcessPods", diff)
		sorter := DeletionSorter{
			Pods: ss.existingPCLQPods,
		}
		sorter.ExpectedPodTemplateHash = ss.getExpectedPodTemplateHash()
		sort.Sort(sorter)
		candidatePodsToDelete = append(candidatePodsToDelete, sorter.Pods[:diff]...)
	}
	return candidatePodsToDelete
}

func (ss *syncSnapshot) getExpectedPodTemplateHash() string {
	if ss.pclq.Status.UpdateProgress != nil &&
		ss.pcs.Status.CurrentGenerationHash != nil &&
		ss.pclq.Status.UpdateProgress.PodCliqueSetGenerationHash == *ss.pcs.Status.CurrentGenerationHash {
		return ss.pclq.Status.UpdateProgress.PodTemplateHash
	}
	return ss.pclq.Labels[apicommon.LabelPodTemplateHash]
}

// checkAndRemovePodSchedulingGates removes scheduling gates from pods when their dependencies are satisfied
func (r _resource) checkAndRemovePodSchedulingGates(ctx context.Context, logger logr.Logger, ss *syncSnapshot) ([]string, error) {
	tasks := make([]utils.Task, 0, len(ss.existingPCLQPods))
	skippedScheduleGatedPods := make([]string, 0, len(ss.existingPCLQPods))

	// Pre-compute once for all pods in this PodClique whether the PodGang(s) it depends on are
	// scheduled. All pods in the same PodClique share the same dependency.
	dependencyScheduled, dependencyPodGangName, err := r.checkDependencyScheduled(ctx, logger, ss)
	if err != nil {
		logger.Error(err, "Error checking if the dependency PodGang is scheduled for PodClique - will requeue")
		return nil, groveerr.WrapError(err,
			errCodeRemovePodSchedulingGate,
			component.OperationSync,
			"failed to check if the dependency PodGang is scheduled for PodClique",
		)
	}

	if ss.podNamesUpdatedInPCLQPodGangSet == nil {
		ss.podNamesUpdatedInPCLQPodGangSet = componentutils.NewSet(ss.podNamesUpdatedInPCLQPodGangs)
	}
	for i, p := range ss.existingPCLQPods {
		if hasPodGangSchedulingGate(p) {
			podObjectKey := client.ObjectKeyFromObject(p)
			if !ss.podNamesUpdatedInPCLQPodGangSet.Has(p.Name) {
				logger.Info("Pod has scheduling gate but it has not yet been updated in PodGang", "podObjectKey", podObjectKey)
				skippedScheduleGatedPods = append(skippedScheduleGatedPods, p.Name)
				continue
			}
			shouldSkip := r.shouldSkipPodSchedulingGateRemoval(logger, p, dependencyScheduled, dependencyPodGangName)
			if shouldSkip {
				skippedScheduleGatedPods = append(skippedScheduleGatedPods, p.Name)
				continue
			}
			task := utils.Task{
				Name: fmt.Sprintf("RemoveSchedulingGate-%s-%d", p.Name, i),
				Fn: func(ctx context.Context) error {
					podClone := p.DeepCopy()
					// Remove only the Grove PodGang gate. Other controllers may add their own
					// scheduling gates on the same Pod; clearing the whole list would wipe those
					// and let the Pod schedule before those owners have released it.
					if !removePodGangSchedulingGate(p) {
						return nil
					}
					if err := client.IgnoreNotFound(r.client.Patch(ctx, p, client.MergeFrom(podClone))); err != nil {
						return err
					}
					logger.Info("Removed Grove PodGang scheduling gate from pod", "podObjectKey", podObjectKey)
					return nil
				},
			}
			tasks = append(tasks, task)
		}
	}

	if len(tasks) > 0 {
		pclqObjectKey := client.ObjectKeyFromObject(ss.pclq)
		if runResult := utils.RunConcurrentlyWithSlowStart(ctx, logger, 1, tasks); runResult.HasErrors() {
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

// isPodGangScheduled checks if the PodGang (identified by name) is scheduled, returning errors for API failures.
// A PodGang is considered "scheduled" when ALL of its constituent PodCliques have achieved
// their minimum required number of scheduled pods (PodClique.Status.ScheduledReplicas >= PodGroup.MinReplicas).
func (r _resource) isPodGangScheduled(ctx context.Context, logger logr.Logger, namespace, podGangName string) (bool, error) {
	// Get the PodGang - treat all errors (including NotFound) as requeue-able
	podGang, err := componentutils.GetPodGang(ctx, r.client, podGangName, namespace)
	if err != nil {
		return false, groveerr.WrapError(err,
			errCodeGetPodGang,
			component.OperationSync,
			fmt.Sprintf("failed to get PodGang %v", client.ObjectKey{Namespace: namespace, Name: podGangName}),
		)
	}

	// Check if all PodGroups in the PodGang have sufficient scheduled replicas.
	// Each PodGroup represents a PodClique within the PodGang and must meet its MinReplicas requirement.
	for _, podGroup := range podGang.Spec.PodGroups {
		pclqName := podGroup.Name
		pclq := &grovecorev1alpha1.PodClique{}
		pclqKey := client.ObjectKey{Name: pclqName, Namespace: namespace}
		if err = r.client.Get(ctx, pclqKey, pclq); err != nil {
			// All errors (including NotFound) should trigger requeue for reliable retry.
			// This ensures the PodClique exists before we evaluate PodGang readiness.
			return false, groveerr.WrapError(err,
				errCodeGetPodClique,
				component.OperationSync,
				fmt.Sprintf("failed to get PodClique %s in namespace %s for PodGang readiness check", pclqName, namespace),
			)
		}

		if pclq.Status.ScheduledReplicas < podGroup.MinReplicas {
			logger.Info("PodGang not scheduled: PodClique has insufficient scheduled replicas",
				"podGangName", podGangName,
				"pclqName", pclqName,
				"scheduledReplicas", pclq.Status.ScheduledReplicas,
				"minReplicas", podGroup.MinReplicas)
			return false, nil // Not ready, but no error - legitimate state
		}
	}

	logger.Info("PodGang is scheduled - all PodCliques meet MinAvailable requirements", "podGangName", podGangName)
	return true, nil
}

// checkDependencyScheduled determines whether the PodGang(s) this PodClique depends on are scheduled.
// The dependency comes from the PodGangMap: the PodClique's own PodGang epoch resolves to a
// PodGangMap entry whose DependsOn lists the epochs of the PodGangs that must be scheduled first. When
// all of those PodGangs are scheduled, this PodClique's pods may have their scheduling gates lifted.
// An empty DependsOn means no dependency (an anchor or standalone PodClique) and returns an empty
// name, the same signal the caller used for the legacy no-base-podgang case.
// NOTE: in this release a PodClique maps to a single PodGang. When coherent updates introduce a 1:N
// PodClique-to-PodGang relationship this resolution must be revisited.
func (r _resource) checkDependencyScheduled(ctx context.Context, logger logr.Logger, ss *syncSnapshot) (bool, string, error) {
	// An empty epoch means the PodClique's PodGang is not materialized yet (a Grove PodGang always
	// carries the epoch label), so its dependency cannot be resolved. Keep the pods gated; the
	// reconcile requeues once the PodGang exists.
	if ss.associatedPodGangEpoch == "" {
		return false, ss.associatedPodGangName, nil
	}
	dependsOnEpochs, err := componentutils.DependsOnForEpoch(ss.pgm, ss.associatedPodGangEpoch)
	if err != nil {
		return false, "", err
	}
	if len(dependsOnEpochs) == 0 {
		// No dependency: this PodClique belongs to an anchor PodGang.
		return true, "", nil
	}
	rnr := apicommon.ResourceNameReplica{Name: ss.pcs.Name, Replica: ss.pcsReplicaIndex}
	for _, epoch := range dependsOnEpochs {
		dependencyPodGangName := apicommon.GenerateAnchorPodGangName(rnr, epoch)
		scheduled, err := r.isPodGangScheduled(ctx, logger, ss.pclq.Namespace, dependencyPodGangName)
		if err != nil {
			return false, dependencyPodGangName, err
		}
		if !scheduled {
			return false, dependencyPodGangName, nil
		}
	}
	return true, "", nil
}

// shouldSkipPodSchedulingGateRemoval implements the core PodGang scheduling gate logic.
// It returns true if the pod scheduling gate removal should be skipped, false otherwise.
func (r _resource) shouldSkipPodSchedulingGateRemoval(logger logr.Logger, pod *corev1.Pod, dependencyScheduled bool, dependencyPodGangName string) bool {
	if dependencyPodGangName == "" {
		// NO DEPENDENCY: this PodClique belongs to an anchor PodGang. These pods form the core gang and
		// get their gates removed immediately once assigned to the PodGang. They represent the minimum
		// viable cluster (first minAvailable replicas) that must start together.
		logger.Info("Proceeding with gate removal for anchor PodGang pod",
			"podObjectKey", client.ObjectKeyFromObject(pod))
		return false
	}
	// HAS DEPENDENCY: this PodClique's PodGang depends on another PodGang.
	if dependencyScheduled {
		logger.Info("Dependency PodGang is scheduled, proceeding with gate removal",
			"podObjectKey", client.ObjectKeyFromObject(pod),
			"dependencyPodGangName", dependencyPodGangName)
		return false
	}
	logger.Info("Pod has scheduling gate but its dependency PodGang is not scheduled yet, skipping scheduling gate removal",
		"podObjectKey", client.ObjectKeyFromObject(pod),
		"dependencyPodGangName", dependencyPodGangName)
	return true
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
	createTasks := make([]utils.Task, 0, numPods)
	for i := range numPods {
		// Get the available Pod host name index. This ensures that we fill the holes in the indices if there are any when creating
		// new pods.
		podHostNameIndex := availableIndices[i]
		createTasks = append(createTasks, r.createPodCreationTask(logger, ss.pcs, ss.pclq, ss.associatedPodGangName, ss.pclqExpectationsStoreKey, i, podHostNameIndex))
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
	pcs                             *grovecorev1alpha1.PodCliqueSet
	pclq                            *grovecorev1alpha1.PodClique
	pcsReplicaIndex                 int
	pgm                             *grovecorev1alpha1.PodGangMap
	associatedPodGangName           string
	associatedPodGangEpoch          string
	existingPCLQPods                []*corev1.Pod
	podNamesUpdatedInPCLQPodGangs   []string
	podNamesUpdatedInPCLQPodGangSet componentutils.Set[string]
	pclqExpectationsStoreKey        string
	expectedPodTemplateHash         string
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

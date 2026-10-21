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

package podcliquesetreplica

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// gangShapeWork is the gang-shape recreate work of a PodCliqueSet. A replica is stale when one of its
// PodCliques or PodCliqueScalingGroups carries a gang-shape hash other than the current one, which happens
// after an init.minReplicas change.
type gangShapeWork struct {
	// currentHash is the gang-shape hash of the PodCliqueSet spec.
	currentHash string
	// staleReplicaIndices are the indices of the stale replicas, in the order in which they are recreated. The
	// replica that recreateTask recreates is not included.
	staleReplicaIndices []int
	// currentlyRecreating is the index of the replica that was deleted last, until it is available again.
	currentlyRecreating *int
	// rebuilding reports whether currentlyRecreating is still missing PodCliques or PodCliqueScalingGroups, or
	// has some that are terminating.
	rebuilding bool
	// recreateTask recreates the next stale replica. It is nil while a replica that is not stale is unavailable,
	// or when no replica is stale.
	recreateTask *utils.Task
}

// inProgress reports whether a replica is stale or still recovering from its recreate.
func (w gangShapeWork) inProgress() bool {
	return len(w.staleReplicaIndices) > 0 || w.currentlyRecreating != nil
}

// replicaGangShapeState is the gang-shape state of one PodCliqueSet replica.
type replicaGangShapeState struct {
	index     int
	stale     bool
	available bool
	// complete reports whether every expected standalone PodClique and PodCliqueScalingGroup exists and none is
	// terminating.
	complete bool
}

// getGangShapeWork finds the stale replicas and the task that recreates the next one. Replicas are recreated one
// at a time under every update strategy, and the next one only once every replica that is not stale is available,
// including the replica recreated last. The next replica is the first of:
//  1. the stale replica under a rolling update;
//  2. stale replicas that are not available, lowest index first;
//  3. the other stale replicas, lowest index first.
//
// pcsIndicesToTerminate are being gang-terminated in this reconcile, so they are rebuilt anyway: they count as
// unavailable and are never chosen.
func (r _resource) getGangShapeWork(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, pcsIndicesToTerminate []int) (*gangShapeWork, error) {
	work := &gangShapeWork{currentHash: componentutils.ComputeGangShapeHash(pcs)}
	states, err := r.getReplicaGangShapeStates(ctx, pcs, work.currentHash, pcsIndicesToTerminate)
	if err != nil {
		return nil, err
	}

	if previous := getCurrentlyRecreating(pcs); previous != nil {
		if state := states[*previous]; !state.stale && !state.available {
			work.currentlyRecreating = previous
			work.rebuilding = !state.complete
		}
	}

	staleStates := lo.Filter(states, func(state replicaGangShapeState, _ int) bool { return state.stale })
	slices.SortStableFunc(staleStates, func(a, b replicaGangShapeState) int {
		return recreatePriority(pcs, a) - recreatePriority(pcs, b)
	})
	work.staleReplicaIndices = lo.Map(staleStates, func(state replicaGangShapeState, _ int) int { return state.index })
	if len(work.staleReplicaIndices) == 0 {
		return work, nil
	}

	if lo.SomeBy(states, func(state replicaGangShapeState) bool { return !state.stale && !state.available }) {
		logger.Info("Waiting for PodCliqueSet replicas to be available before recreating the next replica with a stale gang shape", "staleReplicaIndices", work.staleReplicaIndices)
		return work, nil
	}

	recreateReplicaIndex := work.staleReplicaIndices[0]
	work.staleReplicaIndices = work.staleReplicaIndices[1:]
	work.currentlyRecreating = ptr.To(recreateReplicaIndex)
	work.rebuilding = true
	reason := fmt.Sprintf("Recreate PodCliqueSet %v replica %d because init.minReplicas changed", client.ObjectKeyFromObject(pcs), recreateReplicaIndex)
	task := r.createPCSReplicaDeleteTask(logger, pcs, recreateReplicaIndex, constants.ReasonGangShapeChanged, reason)
	work.recreateTask = &task
	return work, nil
}

// getReplicaGangShapeStates returns the gang-shape state of every replica, indexed by replica index.
func (r _resource) getReplicaGangShapeStates(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet, currentHash string, pcsIndicesToTerminate []int) ([]replicaGangShapeState, error) {
	pclqs, err := componentutils.ListPCLQsMatchingLabels(ctx, r.client, pcs.Namespace, apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name))
	if err != nil {
		return nil, groveerr.WrapError(err, errCodeListPCLQs, component.OperationSync,
			fmt.Sprintf("could not list PCLQs for PCS: %v", client.ObjectKeyFromObject(pcs)))
	}
	pcsgs, err := componentutils.GetPCSGsForPCS(ctx, r.client, pcs.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err, errCodeListPCSGs, component.OperationSync,
			fmt.Sprintf("could not list PCSGs for PCS: %v", client.ObjectKeyFromObject(pcs)))
	}
	pclqsByReplica := groupByPCSReplicaIndex(pclqs, func(pclq grovecorev1beta1.PodClique) map[string]string { return pclq.Labels })
	pcsgsByReplica := groupByPCSReplicaIndex(pcsgs, func(pcsg grovecorev1beta1.PodCliqueScalingGroup) map[string]string { return pcsg.Labels })
	expectedStandalonePCLQs := componentutils.GetExpectedStandAlonePCLQFQNsPerPCSReplica(pcs)

	states := make([]replicaGangShapeState, 0, pcs.Spec.Replicas)
	for replicaIndex := range int(pcs.Spec.Replicas) {
		state := replicaGangShapeState{index: replicaIndex}
		if slices.Contains(pcsIndicesToTerminate, replicaIndex) {
			states = append(states, state)
			continue
		}
		replicaPCLQs := lo.Filter(pclqsByReplica[replicaIndex], func(pclq grovecorev1beta1.PodClique, _ int) bool {
			return !k8sutils.IsResourceTerminating(pclq.ObjectMeta)
		})
		replicaPCSGs := lo.Filter(pcsgsByReplica[replicaIndex], func(pcsg grovecorev1beta1.PodCliqueScalingGroup, _ int) bool {
			return !k8sutils.IsResourceTerminating(pcsg.ObjectMeta)
		})
		state.stale = lo.SomeBy(replicaPCLQs, func(pclq grovecorev1beta1.PodClique) bool { return isGangShapeStale(pclq.Labels, currentHash) }) ||
			lo.SomeBy(replicaPCSGs, func(pcsg grovecorev1beta1.PodCliqueScalingGroup) bool {
				return isGangShapeStale(pcsg.Labels, currentHash)
			})
		state.complete, state.available = getReplicaAvailability(pcs, replicaPCLQs, replicaPCSGs, len(expectedStandalonePCLQs[replicaIndex]))
		states = append(states, state)
	}
	return states, nil
}

// recreatePriority ranks a stale replica for the recreate order; lower goes first. Ties keep index order.
func recreatePriority(pcs *grovecorev1beta1.PodCliqueSet, state replicaGangShapeState) int {
	switch {
	case isCurrentlyUpdating(pcs, state.index):
		return 0
	case !state.available:
		return 1
	default:
		return 2
	}
}

// isCurrentlyUpdating reports whether a rolling update has started on the replica and not finished it.
func isCurrentlyUpdating(pcs *grovecorev1beta1.PodCliqueSet, replicaIndex int) bool {
	return pcs.Status.UpdateProgress != nil && len(pcs.Status.UpdateProgress.CurrentlyUpdating) > 0 &&
		int(pcs.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex) == replicaIndex
}

// getCurrentlyRecreating returns the replica that the recreate deleted last, according to the PodCliqueSet
// status, or nil when there is none or the replica no longer exists.
func getCurrentlyRecreating(pcs *grovecorev1beta1.PodCliqueSet) *int {
	if pcs.Status.GangShapeRecreate == nil || pcs.Status.GangShapeRecreate.CurrentlyRecreating == nil {
		return nil
	}
	index := int(*pcs.Status.GangShapeRecreate.CurrentlyRecreating)
	if index < 0 || index >= int(pcs.Spec.Replicas) {
		return nil
	}
	return &index
}

// isGangShapeStale reports whether a child carries a gang-shape hash other than currentHash. A child without
// the label is not stale: its builder adopts the current hash on the next sync.
func isGangShapeStale(labels map[string]string, currentHash string) bool {
	hash, ok := labels[apicommon.LabelGangShapeHash]
	return ok && hash != "" && hash != currentHash
}

// getReplicaAvailability reports whether every expected standalone PodClique and PodCliqueScalingGroup of the
// replica exists, and whether each also has at least its ready threshold available.
func getReplicaAvailability(pcs *grovecorev1beta1.PodCliqueSet, pclqs []grovecorev1beta1.PodClique, pcsgs []grovecorev1beta1.PodCliqueScalingGroup, expectedStandalonePCLQs int) (complete, available bool) {
	standalonePCLQs := lo.Filter(pclqs, func(pclq grovecorev1beta1.PodClique, _ int) bool {
		_, isPCSGMember := pclq.Labels[apicommon.LabelPodCliqueScalingGroup]
		return !isPCSGMember
	})
	if len(standalonePCLQs) != expectedStandalonePCLQs || len(pcsgs) != len(pcs.Spec.Template.PodCliqueScalingGroupConfigs) {
		return false, false
	}
	pclqsAvailable := lo.EveryBy(standalonePCLQs, func(pclq grovecorev1beta1.PodClique) bool {
		return pclq.Spec.Replicas == 0 || pclq.Status.ReadyReplicas >= componentutils.PCLQReadyThreshold(&pclq.Spec)
	})
	pcsgsAvailable := lo.EveryBy(pcsgs, func(pcsg grovecorev1beta1.PodCliqueScalingGroup) bool {
		return pcsg.Spec.Replicas == 0 || pcsg.Status.AvailableReplicas >= componentutils.PCSGReadyThreshold(&pcsg.Spec)
	})
	return true, pclqsAvailable && pcsgsAvailable
}

func groupByPCSReplicaIndex[T any](items []T, labelsOf func(T) map[string]string) map[int][]T {
	grouped := make(map[int][]T)
	for _, item := range items {
		replicaIndex, err := strconv.Atoi(labelsOf(item)[apicommon.LabelPodCliqueSetReplicaIndex])
		if err != nil {
			continue
		}
		grouped[replicaIndex] = append(grouped[replicaIndex], item)
	}
	return grouped
}

// patchGangShapeRecreateStatus writes status.gangShapeRecreate and the GangShapeRecreateInProgress condition for
// work. It runs before the recreate task, so the replica being deleted is recorded first. The condition is only
// written once a recreate has started, so a PodCliqueSet whose gang shape never changed gets none.
func (r _resource) patchGangShapeRecreateStatus(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, work *gangShapeWork) error {
	original := pcs.DeepCopy()
	mutateGangShapeRecreateStatus(pcs, work, metav1.Now())
	if equality.Semantic.DeepEqual(original.Status, pcs.Status) {
		return nil
	}
	if err := r.client.Status().Patch(ctx, pcs, client.MergeFrom(original)); err != nil {
		return groveerr.WrapError(err, errCodeUpdatePCSStatus, component.OperationSync,
			fmt.Sprintf("could not patch the gang-shape recreate status of PCS: %v", client.ObjectKeyFromObject(pcs)))
	}
	logger.V(1).Info("Updated the PodCliqueSet status with gang-shape recreate progress", "gangShapeRecreate", pcs.Status.GangShapeRecreate)
	return nil
}

// mutateGangShapeRecreateStatus sets status.gangShapeRecreate and the GangShapeRecreateInProgress condition of
// the PodCliqueSet from work.
func mutateGangShapeRecreateStatus(pcs *grovecorev1beta1.PodCliqueSet, work *gangShapeWork, now metav1.Time) {
	if !work.inProgress() {
		if pcs.Status.GangShapeRecreate == nil && meta.FindStatusCondition(pcs.Status.Conditions, apiconstants.ConditionTypeGangShapeRecreateInProgress) == nil {
			return
		}
		pcs.Status.GangShapeRecreate = nil
		meta.SetStatusCondition(&pcs.Status.Conditions, metav1.Condition{
			Type:    apiconstants.ConditionTypeGangShapeRecreateInProgress,
			Status:  metav1.ConditionFalse,
			Reason:  apiconstants.ConditionReasonGangShapeCurrent,
			Message: "Every PodCliqueSet replica has the current gang shape",
		})
		return
	}

	progress := &grovecorev1beta1.GangShapeRecreateProgress{StartedAt: now}
	if pcs.Status.GangShapeRecreate != nil {
		progress.StartedAt = pcs.Status.GangShapeRecreate.StartedAt
	}
	progress.GangShapeHash = work.currentHash
	if work.currentlyRecreating != nil {
		progress.CurrentlyRecreating = ptr.To(int32(*work.currentlyRecreating))
	}
	pcs.Status.GangShapeRecreate = progress

	condition := metav1.Condition{
		Type:   apiconstants.ConditionTypeGangShapeRecreateInProgress,
		Status: metav1.ConditionTrue,
	}
	if work.rebuilding {
		condition.Reason = apiconstants.ConditionReasonRecreatingReplica
		condition.Message = fmt.Sprintf("Recreating PodCliqueSet replica %d with the current gang shape", *work.currentlyRecreating)
	} else {
		condition.Reason = apiconstants.ConditionReasonWaitingForAvailableReplicas
		condition.Message = fmt.Sprintf("Waiting for PodCliqueSet replicas to be available before recreating replicas %v", work.staleReplicaIndices)
		if len(work.staleReplicaIndices) == 0 {
			condition.Message = fmt.Sprintf("Waiting for recreated PodCliqueSet replica %d to be available", *work.currentlyRecreating)
		}
	}
	meta.SetStatusCondition(&pcs.Status.Conditions, condition)
}

// setGangShapeHash writes hash as the gang-shape hash label of the PCSG.
func (r _resource) setGangShapeHash(ctx context.Context, pcsg *grovecorev1beta1.PodCliqueScalingGroup, hash string) error {
	if pcsg.Labels[apicommon.LabelGangShapeHash] == hash {
		return nil
	}
	patch := client.MergeFrom(pcsg.DeepCopy())
	if pcsg.Labels == nil {
		pcsg.Labels = make(map[string]string)
	}
	pcsg.Labels[apicommon.LabelGangShapeHash] = hash
	if err := r.client.Patch(ctx, pcsg, patch); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

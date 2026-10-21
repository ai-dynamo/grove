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
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// gangShapeWork is the gang-shape recreate work of a PodCliqueSet. A replica is stale when one of its
// PodCliques or PodCliqueScalingGroups carries a gang-shape hash other than the current one, which happens
// after an init.minReplicas change.
type gangShapeWork struct {
	// staleReplicaIndices are the indices of the stale replicas, in ascending order.
	staleReplicaIndices []int
	// recreateTask recreates the next stale replica. It is nil while a replica is still recovering from an
	// earlier recreate, or when no replica is stale.
	recreateTask *utils.Task
	// recreateReplicaIndex is the index of the replica that recreateTask recreates.
	recreateReplicaIndex int
}

// hasStaleReplicas reports whether any replica has a stale gang shape.
func (w gangShapeWork) hasStaleReplicas() bool {
	return len(w.staleReplicaIndices) > 0
}

// getGangShapeWork finds the stale replicas and, when no replica is recovering, the task that recreates the
// next one. Replicas are recreated one at a time under every update strategy: the next one starts only when
// every replica that is not stale is available, so a recreated replica is available again before the next
// recreate. The replica under a rolling update is recreated first, and then the lowest index.
// pcsIndicesToTerminate are being gang-terminated in this reconcile, so they are rebuilt anyway.
func (r _resource) getGangShapeWork(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, pcsIndicesToTerminate []int) (*gangShapeWork, error) {
	work := &gangShapeWork{}
	currentHash := componentutils.ComputeGangShapeHash(pcs)
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

	allNonStaleAvailable := true
	for replicaIndex := range int(pcs.Spec.Replicas) {
		if slices.Contains(pcsIndicesToTerminate, replicaIndex) {
			allNonStaleAvailable = false
			continue
		}
		replicaPCLQs := lo.Filter(pclqsByReplica[replicaIndex], func(pclq grovecorev1beta1.PodClique, _ int) bool {
			return !k8sutils.IsResourceTerminating(pclq.ObjectMeta)
		})
		replicaPCSGs := lo.Filter(pcsgsByReplica[replicaIndex], func(pcsg grovecorev1beta1.PodCliqueScalingGroup, _ int) bool {
			return !k8sutils.IsResourceTerminating(pcsg.ObjectMeta)
		})
		stale := lo.SomeBy(replicaPCLQs, func(pclq grovecorev1beta1.PodClique) bool { return isGangShapeStale(pclq.Labels, currentHash) }) ||
			lo.SomeBy(replicaPCSGs, func(pcsg grovecorev1beta1.PodCliqueScalingGroup) bool {
				return isGangShapeStale(pcsg.Labels, currentHash)
			})
		if stale {
			work.staleReplicaIndices = append(work.staleReplicaIndices, replicaIndex)
			continue
		}
		if !isReplicaAvailable(pcs, replicaPCLQs, replicaPCSGs, len(expectedStandalonePCLQs[replicaIndex])) {
			allNonStaleAvailable = false
		}
	}
	if !work.hasStaleReplicas() {
		return work, nil
	}
	if !allNonStaleAvailable {
		logger.Info("Waiting for PodCliqueSet replicas to be available before recreating the next replica with a stale gang shape", "staleReplicaIndices", work.staleReplicaIndices)
		return work, nil
	}
	work.recreateReplicaIndex = work.staleReplicaIndices[0]
	if pcs.Status.UpdateProgress != nil && len(pcs.Status.UpdateProgress.CurrentlyUpdating) > 0 {
		if updating := int(pcs.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex); slices.Contains(work.staleReplicaIndices, updating) {
			work.recreateReplicaIndex = updating
		}
	}
	reason := fmt.Sprintf("Recreate PodCliqueSet %v replica %d because init.minReplicas changed", client.ObjectKeyFromObject(pcs), work.recreateReplicaIndex)
	task := r.createPCSReplicaDeleteTask(logger, pcs, work.recreateReplicaIndex, constants.ReasonGangShapeChanged, reason)
	work.recreateTask = &task
	return work, nil
}

// isGangShapeStale reports whether a child carries a gang-shape hash other than currentHash. A child without
// the label is not stale: its builder adopts the current hash on the next sync.
func isGangShapeStale(labels map[string]string, currentHash string) bool {
	hash, ok := labels[apicommon.LabelGangShapeHash]
	return ok && hash != "" && hash != currentHash
}

// isReplicaAvailable reports whether every expected standalone PodClique and PodCliqueScalingGroup of the
// replica exists and has at least its ready threshold available.
func isReplicaAvailable(pcs *grovecorev1beta1.PodCliqueSet, pclqs []grovecorev1beta1.PodClique, pcsgs []grovecorev1beta1.PodCliqueScalingGroup, expectedStandalonePCLQs int) bool {
	standalonePCLQs := lo.Filter(pclqs, func(pclq grovecorev1beta1.PodClique, _ int) bool {
		_, isPCSGMember := pclq.Labels[apicommon.LabelPodCliqueScalingGroup]
		return !isPCSGMember
	})
	if len(standalonePCLQs) != expectedStandalonePCLQs || len(pcsgs) != len(pcs.Spec.Template.PodCliqueScalingGroupConfigs) {
		return false
	}
	pclqsAvailable := lo.EveryBy(standalonePCLQs, func(pclq grovecorev1beta1.PodClique) bool {
		return pclq.Spec.Replicas == 0 || pclq.Status.ReadyReplicas >= componentutils.PCLQReadyThreshold(&pclq.Spec)
	})
	pcsgsAvailable := lo.EveryBy(pcsgs, func(pcsg grovecorev1beta1.PodCliqueScalingGroup) bool {
		return pcsg.Spec.Replicas == 0 || pcsg.Status.AvailableReplicas >= componentutils.PCSGReadyThreshold(&pcsg.Spec)
	})
	return pclqsAvailable && pcsgsAvailable
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

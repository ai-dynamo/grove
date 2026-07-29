// /*
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
// */

package podclique

import (
	"fmt"
	"math"
	"strconv"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
)

type rollingUpdateBudgetBlockReason string

const rollingUpdateBudgetMaxUnavailableReached rollingUpdateBudgetBlockReason = "MaxUnavailableReached"

// rollingUpdateBudget limits controller-initiated deletion of complete PCSG
// replicas. A replica is one set of member PodCliques sharing the same
// grove.io/podcliquescalinggroup-replica-index label.
type rollingUpdateBudget struct {
	enabled     bool
	allowed     int
	unavailable int
	limit       int
	reason      rollingUpdateBudgetBlockReason
}

func (b rollingUpdateBudget) blocked() bool {
	return b.enabled && b.allowed == 0
}

func evaluateRollingUpdateBudget(sc *syncContext) (rollingUpdateBudget, error) {
	if sc.pcsgConfig == nil {
		return rollingUpdateBudget{allowed: math.MaxInt}, nil
	}
	strategy := sc.pcsgConfig.RollingUpdate
	if strategy == nil || strategy.MaxUnavailable == nil {
		return rollingUpdateBudget{allowed: math.MaxInt}, nil
	}
	if sc.pcsg.Spec.Replicas <= 0 {
		return rollingUpdateBudget{}, fmt.Errorf("replicas for PodCliqueScalingGroup must be positive, got %d", sc.pcsg.Spec.Replicas)
	}

	available := countAvailableReplicas(sc)
	desired := int(sc.pcsg.Spec.Replicas)
	unavailable := max(0, desired-min(available, desired))
	budget := rollingUpdateBudget{
		enabled:     true,
		allowed:     math.MaxInt,
		unavailable: unavailable,
	}
	budget.limit = min(int(*strategy.MaxUnavailable), desired)

	if unavailable >= budget.limit {
		budget.allowed = 0
		budget.reason = rollingUpdateBudgetMaxUnavailableReached
		return budget, nil
	}
	budget.allowed = budget.limit - unavailable
	return budget, nil
}

func countAvailableReplicas(sc *syncContext) int {
	existingPCLQsByReplicaIndex := componentutils.GroupPCLQsByPCSGReplicaIndex(sc.existingPCLQs)
	currentlyUpdatingReplica := -1
	if isAnyReadyReplicaSelectedForUpdate(sc.pcsg) && !isCurrentReplicaUpdateComplete(sc) {
		currentlyUpdatingReplica = int(sc.pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate.Current)
	}
	available := 0
	for replicaIndex := range int(sc.pcsg.Spec.Replicas) {
		// The selected replica consumes availability budget as soon as the
		// deletion is requested, even if the informer cache still contains its
		// previously ready PodCliques.
		if replicaIndex == currentlyUpdatingReplica {
			continue
		}
		if isDesiredReplicaAvailable(sc, replicaIndex, existingPCLQsByReplicaIndex[strconv.Itoa(replicaIndex)]) {
			available++
		}
	}
	return available
}

func isDesiredReplicaAvailable(sc *syncContext, replicaIndex int, existingPCLQs []grovecorev1alpha1.PodClique) bool {
	expectedPCLQNames := sc.expectedPCLQFQNsPerPCSGReplica[replicaIndex]
	if len(existingPCLQs) != len(expectedPCLQNames) {
		return false
	}

	expectedPCLQNameSet := componentutils.NewSet(expectedPCLQNames)
	for _, pclq := range existingPCLQs {
		if !expectedPCLQNameSet.Has(pclq.Name) ||
			k8sutils.IsResourceTerminating(pclq.ObjectMeta) ||
			pclq.Spec.MinAvailable == nil ||
			pclq.Status.ReadyReplicas < *pclq.Spec.MinAvailable {
			return false
		}
	}
	return true
}

func isPCSGReplicaChangeConcurrencyControlEnabled(sc *syncContext) bool {
	return sc.pcsgConfig != nil &&
		sc.pcsgConfig.RollingUpdate != nil &&
		sc.pcsgConfig.RollingUpdate.MaxUnavailable != nil
}

func evaluateScaleInBudget(sc *syncContext) (rollingUpdateBudget, map[string]struct{}, error) {
	if !isPCSGReplicaChangeConcurrencyControlEnabled(sc) {
		return rollingUpdateBudget{allowed: math.MaxInt}, nil, nil
	}

	strategy := sc.pcsgConfig.RollingUpdate
	existingPCLQsByReplicaIndex := componentutils.GroupPCLQsByPCSGReplicaIndex(sc.existingPCLQs)
	activeReplicaIndices := make(map[string]struct{})

	for replicaIndex := range int(sc.pcsg.Spec.Replicas) {
		replicaIndexStr := strconv.Itoa(replicaIndex)
		if !isDesiredReplicaAvailable(sc, replicaIndex, existingPCLQsByReplicaIndex[replicaIndexStr]) {
			activeReplicaIndices[replicaIndexStr] = struct{}{}
		}
	}
	if isAnyReadyReplicaSelectedForUpdate(sc.pcsg) && !isCurrentReplicaUpdateComplete(sc) {
		activeReplicaIndices[strconv.Itoa(int(sc.pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate.Current))] = struct{}{}
	}

	desired := int(sc.pcsg.Spec.Replicas)
	for replicaIndex, pclqs := range existingPCLQsByReplicaIndex {
		index, err := strconv.Atoi(replicaIndex)
		if err != nil || index < desired {
			continue
		}
		for _, pclq := range pclqs {
			if k8sutils.IsResourceTerminating(pclq.ObjectMeta) ||
				pclq.Spec.MinAvailable == nil ||
				pclq.Status.ReadyReplicas < *pclq.Spec.MinAvailable {
				activeReplicaIndices[replicaIndex] = struct{}{}
				break
			}
		}
	}

	population := max(desired, len(existingPCLQsByReplicaIndex))
	if population <= 0 {
		return rollingUpdateBudget{}, nil, fmt.Errorf("replica population for PodCliqueScalingGroup must be positive, got %d", population)
	}
	limit := min(int(*strategy.MaxUnavailable), population)
	unavailable := len(activeReplicaIndices)
	budget := rollingUpdateBudget{
		enabled:     true,
		allowed:     max(0, limit-unavailable),
		unavailable: unavailable,
		limit:       limit,
	}
	if unavailable >= limit {
		budget.reason = rollingUpdateBudgetMaxUnavailableReached
	}
	return budget, activeReplicaIndices, nil
}

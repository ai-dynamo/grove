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
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	"k8s.io/apimachinery/pkg/util/sets"
)

var _ updateStrategyHooks = (*rollingRecreateStrategyHooks)(nil)

// rollingRecreateStrategyHooks supplies RollingRecreate behavior to PCS update advancement.
type rollingRecreateStrategyHooks struct {
	pcs                                   *grovecorev1beta1.PodCliqueSet
	replicaInfos                          []pcsReplicaInfo
	minAvailableBreachedPCSReplicaIndices []int
	selectedPCSReplicaIndices             sets.Set[int]
}

// newRollingRecreateStrategyHooks creates hooks for a RollingRecreate update.
func newRollingRecreateStrategyHooks(
	pcs *grovecorev1beta1.PodCliqueSet,
	replicaInfos []pcsReplicaInfo,
	minAvailableBreachedPCSReplicaIndices []int,
) *rollingRecreateStrategyHooks {
	return &rollingRecreateStrategyHooks{
		pcs:                                   pcs,
		replicaInfos:                          replicaInfos,
		minAvailableBreachedPCSReplicaIndices: minAvailableBreachedPCSReplicaIndices,
		selectedPCSReplicaIndices:             sets.New[int](),
	}
}

// reconcileCurrentlyUpdatingReplicas identifies completed and incomplete PCS replica updates.
func (h *rollingRecreateStrategyHooks) reconcileCurrentlyUpdatingReplicas(currentlyUpdating []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress) (reconcileCurrentlyUpdatingResult, error) {
	result, selectedPCSReplicaIndices := reconcileCurrentlyUpdatingPCSReplicasForConfiguredStrategy(
		h.pcs,
		h.replicaInfos,
		currentlyUpdating,
	)
	h.selectedPCSReplicaIndices = selectedPCSReplicaIndices
	return result, nil
}

// selectReplicasToUpdate selects pending PCS replicas for the available update slots.
func (h *rollingRecreateStrategyHooks) selectReplicasToUpdate() []pcsReplicaUpdateSelection {
	return selectPCSReplicasForConfiguredStrategy(
		h.pcs,
		h.replicaInfos,
		h.selectedPCSReplicaIndices,
		h.minAvailableBreachedPCSReplicaIndices,
	)
}

// isUpdateComplete reports whether every relevant PCS replica completed its update.
func (h *rollingRecreateStrategyHooks) isUpdateComplete() bool {
	return isConfiguredStrategyUpdateComplete(h.pcs, h.replicaInfos)
}

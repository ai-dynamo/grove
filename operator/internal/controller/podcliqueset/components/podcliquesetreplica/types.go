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
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// pcsReplicaScalingIndices contains sorted, unique replica indices committed to scale-out and scale-in.
// The scale-out and scale-in slices must remain disjoint.
type pcsReplicaScalingIndices struct {
	scaleOut []int
	scaleIn  []int
}

// activeScalingIndices returns a combined slice of unique scale-in and scale-out indices that are currently active.
func (si pcsReplicaScalingIndices) activeScalingIndices() []int {
	indices := sets.New[int](si.scaleIn...)
	indices.Insert(si.scaleOut...)
	return indices.UnsortedList()
}

// pcsReplicaInfoByIndex contains PCS replica information keyed by replica index.
type pcsReplicaInfoByIndex map[int]pcsReplicaInfo

// existingReplicaIndices returns the replica indices represented by observed replica-scoped resources.
func (infos pcsReplicaInfoByIndex) existingReplicaIndices() sets.Set[int] {
	existingIndices := sets.New[int]()
	for replicaIndex := range infos {
		existingIndices.Insert(replicaIndex)
	}
	return existingIndices
}

// computeClaimedReplicaIndices computes the PCS replica indices claimed by existing resources and persisted scaling
// and replacement operations.
func (infos pcsReplicaInfoByIndex) computeClaimedReplicaIndices(scalingIndices pcsReplicaScalingIndices, replacementGroups pcsReplicaReplacementGroups) sets.Set[int] {
	claimedReplicaIndices := infos.existingReplicaIndices()
	claimedReplicaIndices.Insert(scalingIndices.scaleOut...)
	claimedReplicaIndices.Delete(scalingIndices.scaleIn...)
	for _, replacement := range replacementGroups.active {
		claimedReplicaIndices.Delete(replacement.sourcePCSReplicaIndex)
		claimedReplicaIndices.Insert(replacement.targetPCSReplicaIndex)
	}
	for _, replacement := range replacementGroups.canceled {
		claimedReplicaIndices.Delete(replacement.sourcePCSReplicaIndex, replacement.targetPCSReplicaIndex)
	}
	return claimedReplicaIndices
}

// pcsReplicaInfo contains the replica-scoped resources observed for one PCS replica index.

type pcsReplicaInfo struct {
	index           int
	standalonePCLQs []grovecorev1beta1.PodClique
	pcsgs           []grovecorev1beta1.PodCliqueScalingGroup
	headlessSvcs    []corev1.Service
	// pgmEntries holds the replica's PodGangMap entries under a coherent update, used to gate completion
	// on the map having reconverged to a single generation. It is nil for a non-coherent update or when
	// the PodGangMap does not exist yet.
	pgmEntries []grovecorev1beta1.PodGangEntry
}

// standalonePCLQNames returns the names of observed standalone PodCliques.
func (pri *pcsReplicaInfo) standalonePCLQNames() sets.Set[string] {
	pclqNames := sets.New[string]()
	for _, pclq := range pri.standalonePCLQs {
		pclqNames.Insert(pclq.Name)
	}
	return pclqNames
}

// pcsgNames returns the names of observed PodCliqueScalingGroups.
func (pri *pcsReplicaInfo) pcsgNames() sets.Set[string] {
	pcsgNames := sets.New[string]()
	for _, pcsg := range pri.pcsgs {
		pcsgNames.Insert(pcsg.Name)
	}
	return pcsgNames
}

// isUpdateComplete reports whether every expected PodClique and PodCliqueScalingGroup of the replica has
// converged to the current generation hash. A missing PodClique keeps the replica incomplete because the
// count of converged PodCliques falls short of the expected count. Under a coherent update it also requires
// the replica's PodGangMap to have reconverged to a single generation, so a mid-flight intermediate anchor
// left by back-to-back updates is drained before the update is declared complete.
func (pri *pcsReplicaInfo) isUpdateComplete(pcs *grovecorev1beta1.PodCliqueSet) bool {
	completeStandalonePCLQs := 0
	for i := range pri.standalonePCLQs {
		if componentutils.IsPCLQUpdateComplete(pcs, &pri.standalonePCLQs[i]) {
			completeStandalonePCLQs++
		}
	}
	if completeStandalonePCLQs != len(componentutils.GetPodCliqueFQNsForPCSReplicaNotInPCSG(pcs, pri.index)) {
		return false
	}
	currentGenerationHash := *pcs.Status.CurrentGenerationHash
	completePCSGs := 0
	for i := range pri.pcsgs {
		if componentutils.IsPCSGUpdateComplete(&pri.pcsgs[i], currentGenerationHash) {
			completePCSGs++
		}
	}
	if completePCSGs != len(pcs.Spec.Template.PodCliqueScalingGroupConfigs) {
		return false
	}
	if componentutils.IsCoherentUpdateInProgress(pcs) && !componentutils.IsPodGangMapAtSingleGeneration(pri.pgmEntries, currentGenerationHash) {
		return false
	}
	return true
}

// hasConvergedToPCSGeneration reports whether every expected standalone PodClique and
// PodCliqueScalingGroup exists and reports the given PCS generation as current.
func (pri *pcsReplicaInfo) hasConvergedToPCSGeneration(pcs *grovecorev1beta1.PodCliqueSet, generationHash string) bool {
	if len(pri.standalonePCLQs) != len(componentutils.GetPodCliqueFQNsForPCSReplicaNotInPCSG(pcs, pri.index)) {
		return false
	}
	for i := range pri.standalonePCLQs {
		currentGenerationHash := pri.standalonePCLQs[i].Status.CurrentPodCliqueSetGenerationHash
		if currentGenerationHash == nil || *currentGenerationHash != generationHash {
			return false
		}
	}
	if len(pri.pcsgs) != len(pcs.Spec.Template.PodCliqueScalingGroupConfigs) {
		return false
	}
	for i := range pri.pcsgs {
		currentGenerationHash := pri.pcsgs[i].Status.CurrentPodCliqueSetGenerationHash
		if currentGenerationHash == nil || *currentGenerationHash != generationHash {
			return false
		}
	}
	return true
}

// isAvailable reports whether every expected child exists and satisfies its availability requirement.
func (pri *pcsReplicaInfo) isAvailable(pcs *grovecorev1beta1.PodCliqueSet) bool {
	return componentutils.IsPCSReplicaAvailable(
		len(componentutils.GetPodCliqueFQNsForPCSReplicaNotInPCSG(pcs, pri.index)),
		len(pcs.Spec.Template.PodCliqueScalingGroupConfigs),
		pri.standalonePCLQs,
		pri.pcsgs,
	)
}

// getNumScheduledPods calculates total scheduled pods across PCLQs and PCSGs for a replica.
func (pri *pcsReplicaInfo) getNumScheduledPods(pcs *grovecorev1beta1.PodCliqueSet) int {
	noScheduled := 0
	for _, pclq := range pri.standalonePCLQs {
		noScheduled += int(pclq.Status.ScheduledReplicas)
	}

	for _, pcsg := range pri.pcsgs {
		for _, cliqueName := range pcsg.Spec.CliqueNames {
			pclqTemplateSpec := componentutils.FindPodCliqueTemplateSpecByName(pcs, cliqueName)
			noScheduled += int(pcsg.Status.ScheduledReplicas * *pclqTemplateSpec.Spec.MinAvailable)
		}
	}
	return noScheduled
}

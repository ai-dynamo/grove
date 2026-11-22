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

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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

// computeCommittedReplicaIndices returns the PCS replica indices accounted for by observed resources and
// persisted scale-out or active replacement commitments. Their count determines whether additional scaling
// is needed. Membership does not imply that resources exist or that a replica is ready.
//
// Ordinary scale-in and replacement-owned retired sources do not contribute to this count. An active
// replacement contributes its target instead of a separate source replica, even before target resources
// exist. Canceled replacements contribute neither source nor target. Cleanup indices can remain reserved
// even though they do not count.
func (infos pcsReplicaInfoByIndex) computeCommittedReplicaIndices(scalingIndices pcsReplicaScalingIndices, replacementGroups pcsReplicaReplacementGroups) sets.Set[int] {
	committedPCSReplicaIndices := infos.existingReplicaIndices()
	committedPCSReplicaIndices.Insert(scalingIndices.scaleOut...)
	committedPCSReplicaIndices.Delete(scalingIndices.scaleIn...)
	for _, replacement := range replacementGroups.active {
		committedPCSReplicaIndices.Delete(replacement.sourcePCSReplicaIndex)
		committedPCSReplicaIndices.Insert(replacement.targetPCSReplicaIndex)
		committedPCSReplicaIndices.Delete(replacement.retiredSourcePCSReplicaIndices...)
	}
	for _, replacement := range replacementGroups.canceled {
		committedPCSReplicaIndices.Delete(replacement.sourcePCSReplicaIndex, replacement.targetPCSReplicaIndex)
		committedPCSReplicaIndices.Delete(replacement.retiredSourcePCSReplicaIndices...)
	}
	return committedPCSReplicaIndices
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

// isUpdateComplete reports whether the replica has recorded completion in the current PCS update or its
// resources satisfy completion. Recorded completion remains valid after resource loss. Otherwise, every
// expected PodClique and PodCliqueScalingGroup must converge to the current generation. A coherent update
// also requires the PodGangMap to reconverge to a single generation. An unfinished recreation requires
// replica availability before completion can be recorded.
func (pri *pcsReplicaInfo) isUpdateComplete(pcs *grovecorev1beta1.PodCliqueSet) bool {
	if pri.hasRecordedUpdateCompletion(pcs) {
		return true
	}

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
	if componentutils.IsPCSReplicaRecreationInProgress(pcs) {
		return pri.isAvailable(pcs)
	}
	return true
}

// hasRecordedUpdateCompletion reports whether the current PCS update records completion for this replica.
// A replacement records completion for its target index. An unfinished selection at a reused target index
// takes precedence over an earlier completed selection.
func (pri *pcsReplicaInfo) hasRecordedUpdateCompletion(pcs *grovecorev1beta1.PodCliqueSet) bool {
	updateProgress := pcs.Status.UpdateProgress
	if updateProgress == nil {
		return false
	}

	completionRecorded := false
	for _, replicaProgress := range updateProgress.CurrentlyUpdating {
		targetPCSReplicaIndex := replicaProgress.ReplicaIndex
		if replicaProgress.Replacement != nil {
			targetPCSReplicaIndex = replicaProgress.Replacement.TargetReplicaIndex
		}

		if int(targetPCSReplicaIndex) == pri.index {
			if replicaProgress.UpdateEndedAt == nil {
				return false
			}
			completionRecorded = true
		}
	}
	return completionRecorded
}

// hasConvergedToPCSGeneration reports whether every expected standalone PodClique and
// PodCliqueScalingGroup exists and reports the given PCS generation as current.
func (pri *pcsReplicaInfo) hasConvergedToPCSGeneration(pcs *grovecorev1beta1.PodCliqueSet, generationHash string) bool {
	if len(pri.standalonePCLQs) != len(componentutils.GetPodCliqueFQNsForPCSReplicaNotInPCSG(pcs, pri.index)) {
		return false
	}
	if len(pri.pcsgs) != len(pcs.Spec.Template.PodCliqueScalingGroupConfigs) {
		return false
	}
	return pri.existingResourcesMatchPCSGeneration(generationHash)
}

// hasMaterializedPCSGeneration reports whether every expected resource exists with the given
// template generation applied. It does not imply pod readiness or status convergence.
func (pri *pcsReplicaInfo) hasMaterializedPCSGeneration(pcs *grovecorev1beta1.PodCliqueSet, generationHash string) bool {
	if generationHash == "" {
		return false
	}
	if len(pri.standalonePCLQs) != len(componentutils.GetPodCliqueFQNsForPCSReplicaNotInPCSG(pcs, pri.index)) ||
		len(pri.pcsgs) != len(pcs.Spec.Template.PodCliqueScalingGroupConfigs) {
		return false
	}
	return pri.haveObservedResourcesAppliedPCSGeneration(generationHash)
}

// haveObservedResourcesAppliedPCSGeneration reports whether every observed PodClique and
// PodCliqueScalingGroup has applied the given generation. Missing children do not invalidate this check.
func (pri *pcsReplicaInfo) haveObservedResourcesAppliedPCSGeneration(generationHash string) bool {
	if generationHash == "" {
		return false
	}
	for _, pclq := range pri.standalonePCLQs {
		if pclq.Labels[apicommon.LabelPodCliqueSetGenerationHash] != generationHash {
			return false
		}
	}
	for _, pcsg := range pri.pcsgs {
		if pcsg.Labels[apicommon.LabelPodCliqueSetGenerationHash] != generationHash {
			return false
		}
	}
	return true
}

// existingResourcesMatchPCSGeneration reports whether every observed standalone PodClique and PodCliqueScalingGroup
// reports the given PCS generation. Missing children do not affect this check; an empty snapshot matches. Unlike
// hasConvergedToPCSGeneration, it does not require all expected children to exist.
func (pri *pcsReplicaInfo) existingResourcesMatchPCSGeneration(generationHash string) bool {
	for i := range pri.standalonePCLQs {
		currentGenerationHash := pri.standalonePCLQs[i].Status.CurrentPodCliqueSetGenerationHash
		if currentGenerationHash == nil || *currentGenerationHash != generationHash {
			return false
		}
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

// gatherPCSReplicaInfosByIndex gathers observed replica-scoped resources and groups their information by PCS replica index.
// PodGangMaps are gathered only during a Coherent update.
func (r *_resource) gatherPCSReplicaInfosByIndex(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet) (pcsReplicaInfoByIndex, error) {
	standalonePCLQsByPCSReplicaIndex, err := componentutils.GetStandalonePCLQsByPCSReplicaIndex(ctx, r.client, pcs.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPCLQs,
			component.OperationSync,
			fmt.Sprintf("failed to list standalone PodCliques by replica for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	pcsgsByPCSReplicaIndex, err := componentutils.GetPCSGsByPCSReplicaIndex(ctx, r.client, pcs.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPCSGs,
			component.OperationSync,
			fmt.Sprintf("failed to list PodCliqueScalingGroups by replica for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	// Gang termination deletes every PodClique for a PCS replica but keeps its headless Service. Include
	// the Service so a standalone-only replica keeps the same index while its PodCliques are recreated.
	// If gang termination later deletes every resource for the replica, this lookup can be removed and
	// reconciliation can allocate the lowest available index instead.
	headlessServicesByPCSReplicaIndex, err := componentutils.GetHeadlessServicesByPCSReplicaIndex(ctx, r.client, pcs.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListServices,
			component.OperationSync,
			fmt.Sprintf("failed to list headless Services by replica for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	var podGangMapsByPCSReplicaIndex map[int]*grovecorev1beta1.PodGangMap
	if componentutils.IsCoherentUpdateInProgress(pcs) {
		podGangMaps, listErr := componentutils.ListPodGangMapsForPCS(ctx, r.client, pcs.ObjectMeta)
		if listErr != nil {
			return nil, groveerr.WrapError(listErr,
				errCodeGetPodGangMap,
				component.OperationSync,
				fmt.Sprintf("could not list PodGangMaps for PCS: %v", client.ObjectKeyFromObject(pcs)),
			)
		}
		podGangMapsByPCSReplicaIndex, err = componentutils.PodGangMapByPCSReplicaIndex(podGangMaps)
		if err != nil {
			return nil, groveerr.WrapError(err,
				errCodeGetPodGangMap,
				component.OperationSync,
				fmt.Sprintf("could not index PodGangMaps by PCS replica for PCS: %v", client.ObjectKeyFromObject(pcs)),
			)
		}
	}

	existingPCSReplicaIndices := sets.New[int]()
	for pcsReplicaIndex := range standalonePCLQsByPCSReplicaIndex {
		existingPCSReplicaIndices.Insert(pcsReplicaIndex)
	}
	for pcsReplicaIndex := range pcsgsByPCSReplicaIndex {
		existingPCSReplicaIndices.Insert(pcsReplicaIndex)
	}
	for pcsReplicaIndex := range headlessServicesByPCSReplicaIndex {
		existingPCSReplicaIndices.Insert(pcsReplicaIndex)
	}
	for pcsReplicaIndex := range podGangMapsByPCSReplicaIndex {
		existingPCSReplicaIndices.Insert(pcsReplicaIndex)
	}

	pcsReplicaInfosByIndex := make(pcsReplicaInfoByIndex, existingPCSReplicaIndices.Len())
	for pcsReplicaIndex := range existingPCSReplicaIndices {
		var podGangMapEntries []grovecorev1beta1.PodGangEntry
		if podGangMap := podGangMapsByPCSReplicaIndex[pcsReplicaIndex]; podGangMap != nil {
			podGangMapEntries = podGangMap.Spec.Entries
		}
		pcsReplicaInfosByIndex[pcsReplicaIndex] = pcsReplicaInfo{
			index:           pcsReplicaIndex,
			standalonePCLQs: standalonePCLQsByPCSReplicaIndex[pcsReplicaIndex],
			pcsgs:           pcsgsByPCSReplicaIndex[pcsReplicaIndex],
			headlessSvcs:    headlessServicesByPCSReplicaIndex[pcsReplicaIndex],
			pgmEntries:      podGangMapEntries,
		}
	}
	return pcsReplicaInfosByIndex, nil
}

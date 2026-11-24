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
	"cmp"
	"slices"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// preparePCSUpdateProgress returns owned progress for an accepted generation change.
// It preserves unfinished replacement cleanup obligations without mutating prior progress.
func preparePCSUpdateProgress(pcs *grovecorev1beta1.PodCliqueSet, targetSchedulingConfigurationHash string, scope updateScope, updateStartedAt metav1.Time) *grovecorev1beta1.PodCliqueSetUpdateProgress {
	requiresReplicaRecreation := requiresPCSReplicaRecreation(pcs, targetSchedulingConfigurationHash)
	progress := &grovecorev1beta1.PodCliqueSetUpdateProgress{
		RequiresReplicaRecreation:          requiresReplicaRecreation,
		TargetSchedulingConfigurationHash:  targetSchedulingConfigurationHash,
		PreviousPodCliqueSetGenerationHash: new(*pcs.Status.CurrentGenerationHash),
		UpdateStartedAt:                    updateStartedAt,
		InScopeStandalonePodCliques:        sets.List(scope.standalonePCLQs),
		InScopePodCliqueScalingGroups:      sets.List(scope.podCliqueScalingGroups),
	}
	if isUpdateOrchestratedByGrove(pcs, requiresReplicaRecreation) {
		progress.CurrentlyUpdating = prepareReplicaSelectionsForUpdate(pcs.Status.UpdateProgress, updateStartedAt)
		if requiresReplicaRecreation {
			// Configured-strategy selections are not replacement cleanup obligations.
			progress.CurrentlyUpdating = slices.DeleteFunc(progress.CurrentlyUpdating, func(selection grovecorev1beta1.PodCliqueSetReplicaUpdateProgress) bool {
				return selection.Replacement == nil
			})
		}
	} else {
		progress.UpdateEndedAt = new(updateStartedAt)
	}
	return progress
}

// requiresPCSReplicaRecreation reports whether the new update must recreate PCS replicas. Once started,
// recreation remains required across superseding updates, including a scheduling rollback to the applied hash.
func requiresPCSReplicaRecreation(pcs *grovecorev1beta1.PodCliqueSet, targetSchedulingConfigurationHash string) bool {
	if pcs.Status.AppliedSchedulingConfigurationHash == nil {
		return false
	}
	return targetSchedulingConfigurationHash != *pcs.Status.AppliedSchedulingConfigurationHash ||
		componentutils.IsPCSReplicaRecreationInProgress(pcs)
}

// isUpdateOrchestratedByGrove reports whether Grove drives the update to completion. Replica recreation is
// always orchestrated. Configured OnDelete updates are completed by manual Pod deletion.
func isUpdateOrchestratedByGrove(pcs *grovecorev1beta1.PodCliqueSet, requiresReplicaRecreation bool) bool {
	return requiresReplicaRecreation || componentutils.ResolveUpdateStrategyType(pcs) != grovecorev1beta1.OnDeleteStrategy
}

// prepareReplicaSelectionsForUpdate prepares existing PCS replica selections for a new update.
// With no existing update progress, it returns no selections. Active replacements restart at their target index with a
// fresh same-index cleanup barrier and retain cleanup obligations for their previous sources. Canceled replacements
// retain their existing mapping and cleanup progress. Returned progress does not alias the existing update progress.
func prepareReplicaSelectionsForUpdate(previousUpdateProgress *grovecorev1beta1.PodCliqueSetUpdateProgress, updateStartedAt metav1.Time) []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress {
	if previousUpdateProgress == nil {
		return nil
	}

	previouslyUpdatingPCSReplicas := previousUpdateProgress.CurrentlyUpdating
	currentlyUpdating := make([]grovecorev1beta1.PodCliqueSetReplicaUpdateProgress, 0, len(previouslyUpdatingPCSReplicas))

	for _, progress := range previouslyUpdatingPCSReplicas {
		if progress.UpdateEndedAt != nil {
			continue
		}

		pcsReplicaIndex := progress.ReplicaIndex
		replacement := progress.Replacement.DeepCopy()

		if replacement != nil && replacement.CanceledAt == nil {
			if pcsReplicaIndex != replacement.TargetReplicaIndex {
				retiredSourcePCSReplicaIndices := sets.New(replacement.RetiredSourceReplicaIndices...)
				retiredSourcePCSReplicaIndices.Insert(pcsReplicaIndex)
				replacement.RetiredSourceReplicaIndices = sets.List(retiredSourcePCSReplicaIndices)
				pcsReplicaIndex = replacement.TargetReplicaIndex
			}
			replacement.SourceCleanupCompletedAt = nil
		}

		currentlyUpdating = append(currentlyUpdating,
			grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				ReplicaIndex:    pcsReplicaIndex,
				Replacement:     replacement,
				UpdateStartedAt: updateStartedAt,
			},
		)
	}

	slices.SortFunc(currentlyUpdating, func(a, b grovecorev1beta1.PodCliqueSetReplicaUpdateProgress) int {
		return cmp.Compare(a.ReplicaIndex, b.ReplicaIndex)
	})
	return currentlyUpdating
}

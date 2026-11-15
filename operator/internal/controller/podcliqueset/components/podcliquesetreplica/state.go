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
	"slices"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/sync"

	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/util/sets"
)

// computePCSReplicaState builds the component-facing state by overlaying the scaling indices and persisted
// replacement progress onto the observed replica identities. expectedReplicaIndices carries the expected set
// implied by scaling alone, computed once by the caller.
func computePCSReplicaState(
	pcs *grovecorev1beta1.PodCliqueSet,
	replicaInfos pcsReplicaInfoByIndex,
	scalingIndices pcsReplicaScalingIndices,
	expectedReplicaIndices sets.Set[int],
	replacementGroups pcsReplicaReplacementGroups) pcssync.State {

	// An unobserved PCS is still being provisioned for the first time. Its expected identities are the
	// contiguous range for the desired replica count and require no scaling reservations.
	if pcs.Status.ObservedGeneration == nil {
		return pcssync.State{ExpectedReplicaIndices: lo.Range(int(pcs.Spec.Replicas))}
	}

	// The overlay mutates the index sets, so clone the caller's expected set to avoid aliasing.
	expectedReplicaIndices = expectedReplicaIndices.Clone()
	scaleOutReplicaIndices := sets.New(scalingIndices.scaleOut...)
	scaleInReplicaIndices := sets.New(scalingIndices.scaleIn...)

	for _, replacement := range replacementGroups.active {
		overlayActiveReplacement(pcs, replacement, expectedReplicaIndices, scaleInReplicaIndices, scaleOutReplicaIndices)
	}
	for _, replacement := range replacementGroups.canceled {
		overlayCanceledReplacement(replacement, replicaInfos, expectedReplicaIndices, scaleInReplicaIndices, scaleOutReplicaIndices)
	}

	return pcssync.State{
		ExpectedReplicaIndices: sortedReplicaIndices(expectedReplicaIndices),
		ScaleOutReplicaIndices: sortedReplicaIndices(scaleOutReplicaIndices),
		ScaleInReplicaIndices:  sortedReplicaIndices(scaleInReplicaIndices),
	}
}

// expectedReplicaIndicesFromScaling returns the expected PCS replica indices implied by the scaling decision
// alone: observed indices plus scale-out, minus scale-in. Replacement overlay is applied later when the full
// component state is built.
func expectedReplicaIndicesFromScaling(existingReplicaIndices sets.Set[int], scalingIndices pcsReplicaScalingIndices) sets.Set[int] {
	expectedReplicaIndices := existingReplicaIndices.Clone()
	expectedReplicaIndices.Insert(scalingIndices.scaleOut...)
	expectedReplicaIndices.Delete(scalingIndices.scaleIn...)
	return expectedReplicaIndices
}

// overlayActiveReplacement projects an active PodCliqueSet replica replacement onto the reconcile-local index sets.
func overlayActiveReplacement(
	pcs *grovecorev1beta1.PodCliqueSet,
	replacement pcsReplicaReplacement,
	expectedReplicaIndices, scaleInReplicaIndices, scaleOutReplicaIndices sets.Set[int]) {

	source := replacement.sourcePCSReplicaIndex
	target := replacement.targetPCSReplicaIndex

	if source != target {
		scaleInReplicaIndices.Insert(source)
		scaleOutReplicaIndices.Insert(target)
		expectedReplicaIndices.Delete(source)
		expectedReplicaIndices.Insert(target)
		return
	}

	// Same index replacement is staged. Delete at the shared index first, then create once the source cleanup
	// has completed. The two reconcile-local sets never contain the shared index together.
	if isReplacementSourceCleanupCompleted(pcs, source) {
		scaleInReplicaIndices.Delete(source)
		scaleOutReplicaIndices.Insert(source)
		expectedReplicaIndices.Insert(source)
		return
	}
	scaleOutReplicaIndices.Delete(source)
	scaleInReplicaIndices.Insert(source)
	expectedReplicaIndices.Delete(source)
}

// overlayCanceledReplacement projects a canceled PodCliqueSet replica replacement awaiting cleanup onto the
// reconcile-local index sets. A replacement is canceled when PCS scale-in selects its active target. Neither
// the source nor the target is a desired identity anymore, so both leave the expected and scale-out sets.
// Resources that still exist at either index drain through scale-in.
func overlayCanceledReplacement(
	replacement pcsReplicaReplacement,
	replicaInfos pcsReplicaInfoByIndex,
	expectedReplicaIndices, scaleInReplicaIndices, scaleOutReplicaIndices sets.Set[int]) {
	source := replacement.sourcePCSReplicaIndex
	target := replacement.targetPCSReplicaIndex

	expectedReplicaIndices.Delete(source, target)
	scaleOutReplicaIndices.Delete(source, target)

	if _, exists := replicaInfos[source]; exists {
		scaleInReplicaIndices.Insert(source)
	}
	if _, exists := replicaInfos[target]; exists {
		scaleInReplicaIndices.Insert(target)
	}
}

// isReplacementSourceCleanupCompleted reports whether the same-index replacement keyed by the source replica index
// has recorded its source cleanup barrier.
func isReplacementSourceCleanupCompleted(pcs *grovecorev1beta1.PodCliqueSet, sourcePCSReplicaIndex int) bool {
	if pcs.Status.UpdateProgress == nil {
		return false
	}
	for _, replicaProgress := range pcs.Status.UpdateProgress.CurrentlyUpdating {
		if int(replicaProgress.ReplicaIndex) != sourcePCSReplicaIndex {
			continue
		}
		return replicaProgress.Replacement != nil && replicaProgress.Replacement.SourceCleanupCompletedAt != nil
	}
	return false
}

// sortedReplicaIndices returns replica indices in ascending order.
func sortedReplicaIndices(replicaIndices sets.Set[int]) []int {
	if replicaIndices.Len() == 0 {
		return nil
	}
	indices := replicaIndices.UnsortedList()
	slices.Sort(indices)
	return indices
}

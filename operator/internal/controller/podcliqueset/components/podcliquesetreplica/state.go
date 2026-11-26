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
	"github.com/ai-dynamo/grove/operator/internal/utils"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"k8s.io/apimachinery/pkg/util/sets"
)

// computePCSReplicaState builds the component-facing state by overlaying scaling
// and replacement work onto the identities retained by the caller.
func computePCSReplicaState(
	pcs *grovecorev1beta1.PodCliqueSet,
	scalingIndices pcsReplicaScalingIndices,
	expectedReplicaIndices sets.Set[int],
	replacementGroups pcsReplicaReplacementGroups,
) pcssync.State {
	expectedReplicaIndices = expectedReplicaIndices.Clone()
	scaleOutReplicaIndices := sets.New(scalingIndices.scaleOut...)
	scaleInReplicaIndices := componentutils.ComputePCSReplicaCleanupIndices(
		pcs,
		scalingIndices.scaleIn,
	)

	for _, replacement := range replacementGroups.active {
		overlayActiveReplacement(
			replacement,
			expectedReplicaIndices,
			scaleInReplicaIndices,
			scaleOutReplicaIndices,
		)
	}
	for _, replacement := range replacementGroups.canceled {
		overlayCanceledReplacement(
			replacement,
			expectedReplicaIndices,
			scaleOutReplicaIndices,
		)
	}

	// Retired sources must not be retained or recreated through another selection.
	for _, replacement := range replacementGroups.active {
		expectedReplicaIndices.Delete(replacement.retiredSourcePCSReplicaIndices...)
		scaleOutReplicaIndices.Delete(replacement.retiredSourcePCSReplicaIndices...)
	}
	for _, replacement := range replacementGroups.canceled {
		expectedReplicaIndices.Delete(replacement.retiredSourcePCSReplicaIndices...)
		scaleOutReplicaIndices.Delete(replacement.retiredSourcePCSReplicaIndices...)
	}

	return pcssync.State{
		ExpectedReplicaIndices: sortedReplicaIndices(expectedReplicaIndices),
		ScaleOutReplicaIndices: sortedReplicaIndices(scaleOutReplicaIndices),
		ScaleInReplicaIndices:  sortedReplicaIndices(scaleInReplicaIndices),
	}
}

// computeInitialPCSReplicaProvisioningIndices chooses missing initial replicas.
// It accounts for existing resources and replacement targets, and excludes
// indices already owned by scaling or replacement work. It records no reservations.
func computeInitialPCSReplicaProvisioningIndices(
	pcs *grovecorev1beta1.PodCliqueSet,
	replicaInfos pcsReplicaInfoByIndex,
	scalingIndices pcsReplicaScalingIndices,
	replacementGroups pcsReplicaReplacementGroups,
) []int {
	if pcs.Status.ObservedGeneration != nil {
		return nil
	}

	committedIndices := replicaInfos.computeCommittedReplicaIndices(scalingIndices, replacementGroups)
	numReplicasToCreate := int(pcs.Spec.Replicas) - committedIndices.Len()
	if numReplicasToCreate <= 0 {
		return nil
	}

	reservedIndices := computeReservedReplicaIndices(
		replicaInfos,
		sets.New(scalingIndices.scaleOut...),
		sets.New(scalingIndices.scaleIn...),
		replacementGroups,
	)
	return utils.GetAvailableIndices(reservedIndices, numReplicasToCreate)
}

// computePCSReplicaIndicesAfterScaling returns observed indices plus scale-out, minus scale-in.
// Replacement ownership is applied later when component state is built.
func computePCSReplicaIndicesAfterScaling(existingReplicaIndices sets.Set[int], scalingIndices pcsReplicaScalingIndices) sets.Set[int] {
	pcsReplicaIndicesAfterScaling := existingReplicaIndices.Clone()
	pcsReplicaIndicesAfterScaling.Insert(scalingIndices.scaleOut...)
	pcsReplicaIndicesAfterScaling.Delete(scalingIndices.scaleIn...)
	return pcsReplicaIndicesAfterScaling
}

// overlayActiveReplacement projects target creation using the shared cleanup set.
// It does not change the cleanup classification.
func overlayActiveReplacement(
	replacement pcsReplicaReplacement,
	expectedReplicaIndices, cleanupReplicaIndices, scaleOutReplicaIndices sets.Set[int],
) {
	source := replacement.sourcePCSReplicaIndex
	target := replacement.targetPCSReplicaIndex

	if source != target {
		scaleOutReplicaIndices.Insert(target)
		expectedReplicaIndices.Delete(source)
		expectedReplicaIndices.Insert(target)
		return
	}

	// A shared source/target index can be created only after it stops being
	// cleanup-only.
	if cleanupReplicaIndices.Has(source) {
		scaleOutReplicaIndices.Delete(source)
		expectedReplicaIndices.Delete(source)
		return
	}

	scaleOutReplicaIndices.Insert(source)
	expectedReplicaIndices.Insert(source)
}

// overlayCanceledReplacement removes canceled source and target identities from
// retained and creation work. The shared cleanup calculation handles their deletion.
func overlayCanceledReplacement(
	replacement pcsReplicaReplacement,
	expectedReplicaIndices, scaleOutReplicaIndices sets.Set[int],
) {
	source := replacement.sourcePCSReplicaIndex
	target := replacement.targetPCSReplicaIndex

	expectedReplicaIndices.Delete(source, target)
	scaleOutReplicaIndices.Delete(source, target)
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

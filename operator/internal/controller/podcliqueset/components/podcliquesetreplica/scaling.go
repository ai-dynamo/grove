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
	"context"
	"slices"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pcsexpectations "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/expectations"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// syncScalingProgress computes and persists replica-index selections.
// It returns the pcs replica scaling indices, a boolean indicating that the scaling progress has changed.
// An error returned if the patch of scaling progress failed.
func (r *_resource) syncScalingProgress(
	ctx context.Context,
	logger logr.Logger,
	pcs *grovecorev1beta1.PodCliqueSet,
	pcsReplicaInfosByIndex pcsReplicaInfoByIndex) (pcsReplicaScalingWork, bool, error) {
	scalingIndices := replicaScalingIndicesFromProgress(pcs.Status.ScalingProgress)
	replacementGroups := groupPCSReplicaReplacements(pcs.Status.UpdateProgress)
	scalingWork := computePCSReplicaScalingWork(pcs, pcsReplicaInfosByIndex, scalingIndices, replacementGroups)
	computedProgress := scalingProgressFromScalingIndices(scalingWork.scalingIndices)
	scalingProgressChanged := !pcsexpectations.ScalingProgressEqual(pcs.Status.ScalingProgress, computedProgress)
	if scalingProgressChanged {
		if err := r.patchScalingProgress(ctx, pcs, computedProgress); err != nil {
			return pcsReplicaScalingWork{}, false, err
		}
		r.recordNewScalingSelectionEvents(logger, pcs, scalingIndices, scalingWork.scalingIndices)
	}
	return scalingWork, scalingProgressChanged, nil
}

// recordNewScalingSelectionEvents records newly selected scale-in indices and newly reserved scale-out indices.
func (r *_resource) recordNewScalingSelectionEvents(logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, previousScalingIndices, computedScalingIndices pcsReplicaScalingIndices) {
	newScaleOutReplicaIndices := sets.New(computedScalingIndices.scaleOut...).Difference(sets.New(previousScalingIndices.scaleOut...))
	if newScaleOutReplicaIndices.Len() > 0 {
		pcsReplicaIndices := sets.List(newScaleOutReplicaIndices)
		logger.Info("Reserved PCS replica indices for scale-out", "pcsReplicaIndices", pcsReplicaIndices)
		r.eventRecorder.Eventf(
			pcs,
			corev1.EventTypeNormal,
			constants.ReasonPodCliqueSetReplicaIndicesReservedForScaleOut,
			"Reserved PCS replica indices %v for scale-out",
			pcsReplicaIndices,
		)
	}

	newScaleInReplicaIndices := sets.New(computedScalingIndices.scaleIn...).Difference(sets.New(previousScalingIndices.scaleIn...))
	if newScaleInReplicaIndices.Len() > 0 {
		pcsReplicaIndices := sets.List(newScaleInReplicaIndices)
		logger.Info("Selected PCS replica indices for scale-in", "pcsReplicaIndices", pcsReplicaIndices)
		r.eventRecorder.Eventf(
			pcs,
			corev1.EventTypeNormal,
			constants.ReasonPodCliqueSetReplicaIndicesSelectedForScaleIn,
			"Selected PCS replica indices %v for scale-in",
			pcsReplicaIndices,
		)
	}
}

// scalingProgressFromScalingIndices converts reconcile-local scaling indices to their API representation.
func scalingProgressFromScalingIndices(scalingIndices pcsReplicaScalingIndices) *grovecorev1beta1.PodCliqueSetScalingProgress {
	if len(scalingIndices.scaleOut) == 0 && len(scalingIndices.scaleIn) == 0 {
		return nil
	}

	progress := &grovecorev1beta1.PodCliqueSetScalingProgress{}
	if len(scalingIndices.scaleOut) > 0 {
		progress.ScaleOutReplicaIndices = make([]int32, len(scalingIndices.scaleOut))
		for i, replicaIndex := range scalingIndices.scaleOut {
			progress.ScaleOutReplicaIndices[i] = int32(replicaIndex)
		}
	}
	if len(scalingIndices.scaleIn) > 0 {
		progress.ScaleInReplicaIndices = make([]int32, len(scalingIndices.scaleIn))
		for i, replicaIndex := range scalingIndices.scaleIn {
			progress.ScaleInReplicaIndices[i] = int32(replicaIndex)
		}
	}
	return progress
}

// patchScalingProgress persists scaling progress and records the value that must be observed through the informer cache.
func (r *_resource) patchScalingProgress(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet, progress *grovecorev1beta1.PodCliqueSetScalingProgress) error {
	r.scalingProgressExpectations.Record(pcs.ObjectMeta, progress)

	patchedPCS := pcs.DeepCopy()
	patchedPCS.Status.ScalingProgress = progress
	patch := client.MergeFromWithOptions(pcs, client.MergeFromWithOptimisticLock{})
	if err := r.client.Status().Patch(ctx, patchedPCS, patch); err != nil {
		r.scalingProgressExpectations.Forget(pcs.ObjectMeta)
		return groveerr.WrapError(
			err,
			errCodeUpdatePCSStatus,
			component.OperationSync,
			"failed to patch PodCliqueSet scaling progress",
		)
	}

	pcs.ResourceVersion = patchedPCS.ResourceVersion
	if progress == nil {
		pcs.Status.ScalingProgress = nil
	} else {
		pcs.Status.ScalingProgress = progress.DeepCopy()
	}
	return nil
}

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

// replicaScalingIndicesFromProgress converts API scaling progress to reconcile-local indices.
func replicaScalingIndicesFromProgress(progress *grovecorev1beta1.PodCliqueSetScalingProgress) pcsReplicaScalingIndices {
	if progress == nil {
		return pcsReplicaScalingIndices{}
	}
	return pcsReplicaScalingIndices{
		scaleOut: lo.Map(progress.ScaleOutReplicaIndices, func(replicaIndex int32, _ int) int { return int(replicaIndex) }),
		scaleIn:  lo.Map(progress.ScaleInReplicaIndices, func(replicaIndex int32, _ int) int { return int(replicaIndex) }),
	}
}

// pcsReplicaScalingWork is the scaling decision for a reconcile. The scale-in/out indices are persisted to
// ScalingProgress. replacementSourcesToCancel names the source replica indices of active replacements whose
// targets scale-in selected for reclamation. Those targets are deliberately excluded from scaleIn and are
// reclaimed by the rolling-update orchestrator through cancellation, never by scaling.
type pcsReplicaScalingWork struct {
	scalingIndices             pcsReplicaScalingIndices
	replacementSourcesToCancel []int
}

// computePCSReplicaScalingWork determines which PCS replica indices must be scaled out or scaled in, and which
// active replacements must be canceled because scale-in selected their target. It uses the observed resources,
// the recorded scaling progress, the persisted replacement progress, and the desired replica count. The
// returned scale-in/out slices are sorted ascending and disjoint.
func computePCSReplicaScalingWork(
	pcs *grovecorev1beta1.PodCliqueSet,
	replicaInfos pcsReplicaInfoByIndex,
	scalingIndices pcsReplicaScalingIndices,
	replacementGroups pcsReplicaReplacementGroups) pcsReplicaScalingWork {
	// An unobserved PCS is still being provisioned for the first time. Its initial
	// identities are not scale-out work and therefore do not require reservations.
	if pcs.Status.ObservedGeneration == nil {
		return pcsReplicaScalingWork{}
	}

	scaleOutIndices := sets.New(scalingIndices.scaleOut...)
	scaleInIndices := sets.New(scalingIndices.scaleIn...)

	// Remove scale-out indices after all expected PCLQs and PCSGs exist.
	scaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs := findScaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs(pcs, scalingIndices.scaleOut, replicaInfos)
	scaleOutIndices.Delete(scaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs...)

	// Claimed identities account for persisted replacements: an active replacement contributes its target,
	// a canceled replacement contributes neither source nor target.
	workingScalingIndices := pcsReplicaScalingIndices{
		scaleOut: scaleOutIndices.UnsortedList(),
		scaleIn:  scaleInIndices.UnsortedList(),
	}
	claimedPCSReplicaIndices := replicaInfos.computeClaimedReplicaIndices(workingScalingIndices, replacementGroups)
	desiredReplicaCount := int(pcs.Spec.Replicas)

	var replacementSourcesToCancel []int
	if claimedPCSReplicaIndices.Len() > desiredReplicaCount {
		numReplicasToRemove := claimedPCSReplicaIndices.Len() - desiredReplicaCount
		activeTargetToSource := activeReplacementTargetToSource(replacementGroups.active)
		activeReplacementTargetIndices := activeReplacementTargetIndexSet(activeTargetToSource)
		selectedReplicaIndices := selectReplicaIndicesForScaleIn(pcs, replicaInfos, claimedPCSReplicaIndices, scaleOutIndices, activeReplacementTargetIndices, numReplicasToRemove)
		for _, replicaIndex := range selectedReplicaIndices {
			if sourceReplicaIndex, isActiveTarget := activeTargetToSource[replicaIndex]; isActiveTarget {
				// Reclaim this unit by canceling the replacement. Scaling persists nothing for it: the
				// orchestrator stamps CanceledAt and the canceled replacement drains through the state overlay.
				replacementSourcesToCancel = append(replacementSourcesToCancel, sourceReplicaIndex)
				continue
			}
			scaleOutIndices.Delete(replicaIndex)
			scaleInIndices.Insert(replicaIndex)
		}
	} else if claimedPCSReplicaIndices.Len() < desiredReplicaCount {
		numReplicasToAdd := desiredReplicaCount - claimedPCSReplicaIndices.Len()
		reservedReplicaIndices := claimedReservedIndices(replicaInfos, scaleOutIndices, scaleInIndices, replacementGroups)
		selectedScaleOutIndices := utils.GetAvailableIndices(reservedReplicaIndices, numReplicasToAdd)
		scaleOutIndices.Insert(selectedScaleOutIndices...)
	}

	slices.Sort(replacementSourcesToCancel)
	return pcsReplicaScalingWork{
		scalingIndices: pcsReplicaScalingIndices{
			scaleOut: sortedReplicaIndices(scaleOutIndices),
			scaleIn:  sortedReplicaIndices(scaleInIndices),
		},
		replacementSourcesToCancel: replacementSourcesToCancel,
	}
}

// activeReplacementTargetToSource maps each active replacement target index to its source index.
func activeReplacementTargetToSource(active []pcsReplicaReplacement) map[int]int {
	targetToSource := make(map[int]int, len(active))
	for _, replacement := range active {
		targetToSource[replacement.targetPCSReplicaIndex] = replacement.sourcePCSReplicaIndex
	}
	return targetToSource
}

// activeReplacementTargetIndexSet returns the set of active replacement target indices.
func activeReplacementTargetIndexSet(targetToSource map[int]int) sets.Set[int] {
	targetIndices := sets.New[int]()
	for targetReplicaIndex := range targetToSource {
		targetIndices.Insert(targetReplicaIndex)
	}
	return targetIndices
}

// claimedReservedIndices returns the replica indices reserved against new scale-out allocation: existing
// resources, committed scale-out and scale-in, and every active and canceled replacement source and target.
// Reserving replacement indices prevents scale-out from reusing an index already committed to a replacement.
func claimedReservedIndices(
	replicaInfos pcsReplicaInfoByIndex,
	scaleOutIndices, scaleInIndices sets.Set[int],
	replacementGroups pcsReplicaReplacementGroups) sets.Set[int] {
	reserved := replicaInfos.existingReplicaIndices()
	reserved.Insert(scaleOutIndices.UnsortedList()...)
	reserved.Insert(scaleInIndices.UnsortedList()...)
	for _, replacement := range replacementGroups.active {
		reserved.Insert(replacement.sourcePCSReplicaIndex, replacement.targetPCSReplicaIndex)
	}
	for _, replacement := range replacementGroups.canceled {
		reserved.Insert(replacement.sourcePCSReplicaIndex, replacement.targetPCSReplicaIndex)
	}
	return reserved
}

// findScaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs returns scale-out PCS replica indices for which every
// expected PodClique and PodCliqueScalingGroup exists. It does not consider readiness or update convergence.
func findScaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs(
	pcs *grovecorev1beta1.PodCliqueSet,
	scaleOutReplicaIndices []int,
	replicaInfos pcsReplicaInfoByIndex) []int {
	var scaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs []int
	for _, pcsReplicaIndex := range scaleOutReplicaIndices {
		replicaInfo := replicaInfos[pcsReplicaIndex]
		expectedPCLQNames := sets.New(componentutils.GetPodCliqueFQNsForPCSReplicaNotInPCSG(pcs, pcsReplicaIndex)...)
		if !replicaInfo.standalonePCLQNames().IsSuperset(expectedPCLQNames) {
			continue
		}

		expectedPCSGNames := sets.New(componentutils.GetPCSGFQNsForPCSReplica(pcs, pcsReplicaIndex)...)
		if replicaInfo.pcsgNames().IsSuperset(expectedPCSGNames) {
			scaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs = append(scaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs, pcsReplicaIndex)
		}
	}

	slices.Sort(scaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs)
	return scaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs
}

type pcsReplicaScaleInPriority int

const (
	scaleInUnavailablePendingRecreation pcsReplicaScaleInPriority = iota
	scaleInInProgress
	scaleInOtherUnavailable
	scaleInAvailable
)

type pcsReplicaScaleInCandidate struct {
	replicaIndex int
	priority     pcsReplicaScaleInPriority
	// tieBreaker prefers insufficient scheduled capacity for unavailable replicas, ordinary scale-out
	// over replacement targets for in-progress work, and old-generation resources for available replicas.
	tieBreaker int
}

// selectReplicaIndicesForScaleIn selects up to count claimed PCS replica indices for reclamation:
//  1. unavailable replicas with existing old-generation resources and no active replacement
//  2. in-progress work: ordinary scale-out, then unavailable active replacement targets
//  3. other unavailable replicas
//  4. available replicas, preferring those with existing old-generation resources
//
// Without active replacements, all established unavailable replicas have first priority, preserving ordinary
// scale-in ordering. Missing children alone do not establish pending old-generation recreation. Replacement
// targets are recognized from their mapping, independently of scale-out reservations. A selected target is
// reclaimed by canceling its replacement rather than by scale-in. Higher indices break remaining ties.
func selectReplicaIndicesForScaleIn(
	pcs *grovecorev1beta1.PodCliqueSet,
	replicaInfos pcsReplicaInfoByIndex,
	claimedReplicaIndices sets.Set[int],
	scaleOutReplicaIndices sets.Set[int],
	activeReplacementTargetIndices sets.Set[int],
	count int) []int {
	if count <= 0 {
		return nil
	}
	numExpectedStandalonePCLQs := componentutils.CountStandalonePCLQs(pcs)
	numExpectedPCSGs := len(pcs.Spec.Template.PodCliqueScalingGroupConfigs)
	hasActiveReplacementTargets := activeReplacementTargetIndices.Len() > 0
	candidates := make([]pcsReplicaScaleInCandidate, 0, claimedReplicaIndices.Len())
	for replicaIndex := range claimedReplicaIndices {
		replicaInfo := replicaInfos[replicaIndex]
		replicaInfo.index = replicaIndex
		isReplacementTarget := activeReplacementTargetIndices.Has(replicaIndex)
		isAvailable := replicaInfo.isAvailable(pcs)
		hasOldGenerationResources := pcs.Status.CurrentGenerationHash != nil &&
			!replicaInfo.existingResourcesMatchPCSGeneration(*pcs.Status.CurrentGenerationHash)
		candidate := pcsReplicaScaleInCandidate{replicaIndex: replicaIndex}
		switch {
		case scaleOutReplicaIndices.Has(replicaIndex) && !isReplacementTarget:
			candidate.priority = scaleInInProgress
		case !isAvailable && isReplacementTarget:
			candidate.priority = scaleInInProgress
			candidate.tieBreaker = 1
		case isAvailable:
			candidate.priority = scaleInAvailable
			if !hasOldGenerationResources {
				candidate.tieBreaker = 1
			}
		default:
			candidate.priority = scaleInUnavailablePendingRecreation
			if hasActiveReplacementTargets && !hasOldGenerationResources {
				candidate.priority = scaleInOtherUnavailable
			}
			if componentutils.HasSufficientScheduledReplicasForPCSReplica(numExpectedStandalonePCLQs, numExpectedPCSGs, replicaInfo.standalonePCLQs, replicaInfo.pcsgs) {
				candidate.tieBreaker = 1
			}
		}
		candidates = append(candidates, candidate)
	}

	slices.SortFunc(candidates, func(a, b pcsReplicaScaleInCandidate) int {
		return cmp.Or(
			cmp.Compare(a.priority, b.priority),
			cmp.Compare(a.tieBreaker, b.tieBreaker),
			cmp.Compare(b.replicaIndex, a.replicaIndex),
		)
	})
	selectedReplicaIndices := make([]int, min(count, len(candidates)))
	for i := range selectedReplicaIndices {
		selectedReplicaIndices[i] = candidates[i].replicaIndex
	}
	return selectedReplicaIndices
}

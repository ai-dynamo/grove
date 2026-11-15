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
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/sync"
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
	pcsReplicaInfosByIndex pcsReplicaInfoByIndex) (pcsReplicaScalingIndices, bool, error) {

	scalingIndices := replicaScalingIndicesFromProgress(pcs.Status.ScalingProgress)
	computedScalingIndices := computePCSReplicaScalingIndices(pcs, pcsReplicaInfosByIndex, scalingIndices)
	computedProgress := scalingProgressFromScalingIndices(computedScalingIndices)
	scalingProgressChanged := !pcssync.ScalingProgressEqual(pcs.Status.ScalingProgress, computedProgress)
	if scalingProgressChanged {
		if err := r.patchScalingProgress(ctx, pcs, computedProgress); err != nil {
			return pcsReplicaScalingIndices{}, false, err
		}
		r.recordNewScalingSelectionEvents(logger, pcs, scalingIndices, computedScalingIndices)
	}
	return computedScalingIndices, scalingProgressChanged, nil
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

// computePCSReplicaScalingIndices determines which PCS replica indices must be scaled out or scaled in.
// It uses the observed resources, the recorded scaling progress, and the desired replica count.
// Both returned index slices are sorted in ascending order and remain disjoint.
func computePCSReplicaScalingIndices(pcs *grovecorev1beta1.PodCliqueSet, replicaInfos pcsReplicaInfoByIndex, scalingIndices pcsReplicaScalingIndices) pcsReplicaScalingIndices {
	// An unobserved PCS is still being provisioned for the first time. Its initial
	// identities are not scale-out work and therefore do not require reservations.
	if pcs.Status.ObservedGeneration == nil {
		return pcsReplicaScalingIndices{}
	}

	scaleOutIndices := sets.New(scalingIndices.scaleOut...)
	scaleInIndices := sets.New(scalingIndices.scaleIn...)

	// Remove scale-out indices after all expected PCLQs and PCSGs exist.
	scaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs := findScaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs(pcs, scalingIndices.scaleOut, replicaInfos)
	scaleOutIndices.Delete(scaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs...)

	claimedPCSReplicaIndices := replicaInfos.existingReplicaIndices()
	claimedPCSReplicaIndices.Insert(scaleOutIndices.UnsortedList()...)
	claimedPCSReplicaIndices.Delete(scaleInIndices.UnsortedList()...)
	desiredReplicaCount := int(pcs.Spec.Replicas)

	if claimedPCSReplicaIndices.Len() > desiredReplicaCount {
		numReplicasToRemove := claimedPCSReplicaIndices.Len() - desiredReplicaCount
		selectedScaleInIndices := selectReplicaIndicesForScaleIn(pcs, replicaInfos, claimedPCSReplicaIndices, scaleOutIndices, numReplicasToRemove)
		scaleOutIndices.Delete(selectedScaleInIndices...)
		scaleInIndices.Insert(selectedScaleInIndices...)
		claimedPCSReplicaIndices.Delete(selectedScaleInIndices...)
	} else if claimedPCSReplicaIndices.Len() < desiredReplicaCount {
		numReplicasToAdd := desiredReplicaCount - claimedPCSReplicaIndices.Len()
		reservedReplicaIndices := replicaInfos.existingReplicaIndices()
		reservedReplicaIndices.Insert(scaleOutIndices.UnsortedList()...)
		reservedReplicaIndices.Insert(scaleInIndices.UnsortedList()...)
		selectedScaleOutIndices := utils.GetAvailableIndices(reservedReplicaIndices, numReplicasToAdd)
		scaleOutIndices.Insert(selectedScaleOutIndices...)
		claimedPCSReplicaIndices.Insert(selectedScaleOutIndices...)
	}

	return pcsReplicaScalingIndices{
		scaleOut: sortedReplicaIndices(scaleOutIndices),
		scaleIn:  sortedReplicaIndices(scaleInIndices),
	}
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

// selectReplicaIndicesForScaleIn selects the requested number of desired PCS replica indices for removal.
// It prefers established replicas with insufficient scheduled capacity, established replicas with sufficient
// scheduled but insufficient available capacity, replicas with scale-out in progress, and then established
// available replicas. Higher indices are selected first within each group.
func selectReplicaIndicesForScaleIn(
	pcs *grovecorev1beta1.PodCliqueSet,
	replicaInfos pcsReplicaInfoByIndex,
	claimedReplicaIndices sets.Set[int],
	scaleOutReplicaIndices sets.Set[int],
	count int) []int {
	if count <= 0 {
		return nil
	}
	numExpectedStandalonePCLQs := componentutils.CountStandalonePCLQs(pcs)
	numExpectedPCSGs := len(pcs.Spec.Template.PodCliqueScalingGroupConfigs)

	var (
		insufficientScheduledReplicaIndices []int
		insufficientAvailableReplicaIndices []int
		availableReplicaIndices             []int
	)
	for replicaIndex := range claimedReplicaIndices {
		if scaleOutReplicaIndices.Has(replicaIndex) {
			continue
		}
		replicaInfo := replicaInfos[replicaIndex]
		standalonePCLQs := replicaInfo.standalonePCLQs
		pcsgs := replicaInfo.pcsgs
		if componentutils.IsPCSReplicaAvailable(numExpectedStandalonePCLQs, numExpectedPCSGs, standalonePCLQs, pcsgs) {
			availableReplicaIndices = append(availableReplicaIndices, replicaIndex)
			continue
		}
		if !componentutils.HasSufficientScheduledReplicasForPCSReplica(numExpectedStandalonePCLQs, numExpectedPCSGs, standalonePCLQs, pcsgs) {
			insufficientScheduledReplicaIndices = append(insufficientScheduledReplicaIndices, replicaIndex)
			continue
		}
		insufficientAvailableReplicaIndices = append(insufficientAvailableReplicaIndices, replicaIndex)
	}

	orderedReplicaIndices := sortAndCoalesceReplicaIndices(
		insufficientScheduledReplicaIndices,
		insufficientAvailableReplicaIndices,
		scaleOutReplicaIndices.UnsortedList(),
		availableReplicaIndices,
	)
	return orderedReplicaIndices[:min(count, len(orderedReplicaIndices))]
}

// sortAndCoalesceReplicaIndices sorts each group from highest to lowest and returns them in scale-in priority order.
func sortAndCoalesceReplicaIndices(insufficientScheduledReplicaIndices, insufficientAvailableReplicaIndices, scaleOutReplicaIndices, availableReplicaIndices []int) []int {
	sortReplicaIndicesDescending(insufficientScheduledReplicaIndices)
	sortReplicaIndicesDescending(insufficientAvailableReplicaIndices)
	sortReplicaIndicesDescending(scaleOutReplicaIndices)
	sortReplicaIndicesDescending(availableReplicaIndices)

	capacity := len(insufficientScheduledReplicaIndices) + len(insufficientAvailableReplicaIndices) + len(scaleOutReplicaIndices) + len(availableReplicaIndices)
	orderedReplicaIndices := make([]int, 0, capacity)
	orderedReplicaIndices = append(orderedReplicaIndices, insufficientScheduledReplicaIndices...)
	orderedReplicaIndices = append(orderedReplicaIndices, insufficientAvailableReplicaIndices...)
	orderedReplicaIndices = append(orderedReplicaIndices, scaleOutReplicaIndices...)
	orderedReplicaIndices = append(orderedReplicaIndices, availableReplicaIndices...)

	return orderedReplicaIndices
}

// sortReplicaIndicesDescending sorts replica indices from highest to lowest.
func sortReplicaIndicesDescending(replicaIndices []int) {
	slices.SortFunc(replicaIndices, func(a, b int) int {
		return cmp.Compare(b, a)
	})
}

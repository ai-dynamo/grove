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
	"fmt"
	"slices"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
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

// syncScalingProgress computes and persists replica-index selections and populates the state shared with later components.
func (r *_resource) syncScalingProgress(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, state *pcssync.State) (bool, error) {
	snapshot, err := r.takeReplicaResourceSnapshot(ctx, pcs)
	if err != nil {
		return false, err
	}

	scaleOutReplicaIndices, scaleInReplicaIndices := replicaIndicesFromScalingProgress(pcs.Status.ScalingProgress)
	computedState := computeReplicaIndices(pcs, snapshot, scaleOutReplicaIndices, scaleInReplicaIndices)
	computedProgress := scalingProgressFromState(computedState)
	scalingProgressChanged := !pcssync.ScalingProgressEqual(pcs.Status.ScalingProgress, computedProgress)
	if scalingProgressChanged {
		if err := r.patchScalingProgress(ctx, pcs, computedProgress); err != nil {
			return false, err
		}
		r.recordNewScalingSelectionEvents(logger, pcs, scaleOutReplicaIndices, scaleInReplicaIndices, computedState)
	}

	*state = computedState
	return scalingProgressChanged, nil
}

// recordNewScalingSelectionEvents records newly selected scale-in indices and newly reserved scale-out indices.
func (r *_resource) recordNewScalingSelectionEvents(logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, previousScaleOutReplicaIndices, previousScaleInReplicaIndices []int, computedState pcssync.State) {
	newScaleOutReplicaIndices := sets.New(computedState.ScaleOutReplicaIndices...).Difference(sets.New(previousScaleOutReplicaIndices...))
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

	newScaleInReplicaIndices := sets.New(computedState.ScaleInReplicaIndices...).Difference(sets.New(previousScaleInReplicaIndices...))
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

// takeReplicaResourceSnapshot lists standalone PodCliques and PodCliqueScalingGroups owned by the PCS and groups them by PCS replica index.
func (r *_resource) takeReplicaResourceSnapshot(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (*replicaResourceSnapshot, error) {
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
	return &replicaResourceSnapshot{
		standalonePCLQsByReplicaIndex:     standalonePCLQsByPCSReplicaIndex,
		pcsgsByReplicaIndex:               pcsgsByPCSReplicaIndex,
		headlessServicesByPCSReplicaIndex: headlessServicesByPCSReplicaIndex,
	}, nil
}

// replicaResourceSnapshot contains the standalone PodCliques and PodCliqueScalingGroups observed for each PCS replica
// index during one reconciliation.
type replicaResourceSnapshot struct {
	standalonePCLQsByReplicaIndex     map[int][]grovecorev1alpha1.PodClique
	pcsgsByReplicaIndex               map[int][]grovecorev1alpha1.PodCliqueScalingGroup
	headlessServicesByPCSReplicaIndex map[int][]corev1.Service
}

// standalonePCLQNames returns the names of standalone PodCliques observed for the given PCS replica index.
func (r *replicaResourceSnapshot) standalonePCLQNames(pcsReplicaIndex int) sets.Set[string] {
	pclqNames := sets.New[string]()
	for _, pclq := range r.standalonePCLQsByReplicaIndex[pcsReplicaIndex] {
		pclqNames.Insert(pclq.Name)
	}
	return pclqNames
}

// pcsgNames returns the names of PodCliqueScalingGroups observed for the given PCS replica index.
func (r *replicaResourceSnapshot) pcsgNames(pcsReplicaIndex int) sets.Set[string] {
	pcsgNames := sets.New[string]()
	for _, pcsg := range r.pcsgsByReplicaIndex[pcsReplicaIndex] {
		pcsgNames.Insert(pcsg.Name)
	}
	return pcsgNames
}

// projectedReplicaIndices returns the observed replica indices plus indices being added, excluding indices being removed.
func (r *replicaResourceSnapshot) projectedReplicaIndices(scaleOutReplicaIndices []int, scaleInReplicaIndices []int) sets.Set[int] {
	replicaIndices := r.existingReplicaIndices()
	replicaIndices.Insert(scaleOutReplicaIndices...)
	replicaIndices.Delete(scaleInReplicaIndices...)
	return replicaIndices
}

// existingReplicaIndices returns PCS replica indices represented in the snapshot.
// Headless Services preserve the identity of standalone-only replicas while gang termination temporarily removes their PodCliques.
func (r *replicaResourceSnapshot) existingReplicaIndices() sets.Set[int] {
	indices := sets.New[int]()
	for replicaIndex := range r.standalonePCLQsByReplicaIndex {
		indices.Insert(replicaIndex)
	}
	for replicaIndex := range r.pcsgsByReplicaIndex {
		indices.Insert(replicaIndex)
	}
	for pcsReplicaIndex := range r.headlessServicesByPCSReplicaIndex {
		indices.Insert(pcsReplicaIndex)
	}
	return indices
}

// reservedReplicaIndices returns PCS replica indices that are unavailable for allocation.
// It includes indices represented by resources and indices reserved by scale-out or scale-in progress.
func (r *replicaResourceSnapshot) reservedReplicaIndices(scaleOutReplicaIndices []int, scaleInReplicaIndices []int) sets.Set[int] {
	replicaIndices := r.existingReplicaIndices()
	replicaIndices.Insert(scaleOutReplicaIndices...)
	replicaIndices.Insert(scaleInReplicaIndices...)
	return replicaIndices
}

// replicaIndicesFromScalingProgress converts API scaling-progress indices to reconcile-local indices.
func replicaIndicesFromScalingProgress(progress *grovecorev1alpha1.PodCliqueSetScalingProgress) ([]int, []int) {
	if progress == nil {
		return nil, nil
	}

	scaleOutReplicaIndices := lo.Map(progress.ScaleOutReplicaIndices, func(replicaIndex int32, _ int) int { return int(replicaIndex) })
	scaleInReplicaIndices := lo.Map(progress.ScaleInReplicaIndices, func(replicaIndex int32, _ int) int { return int(replicaIndex) })

	return scaleOutReplicaIndices, scaleInReplicaIndices
}

// computeReplicaIndices determines which replica indices must exist and which are being added or removed.
// It uses the observed resources, the recorded scaling progress, and the desired replica count. All returned slices are sorted.
func computeReplicaIndices(pcs *grovecorev1alpha1.PodCliqueSet, snapshot *replicaResourceSnapshot, scaleOutReplicaIndices []int, scaleInReplicaIndices []int) pcssync.State {
	// An unobserved PCS is still being provisioned for the first time. Its initial
	// identities are not scale-out work and therefore do not require reservations.
	if pcs.Status.ObservedGeneration == nil {
		return pcssync.State{ExpectedReplicaIndices: lo.Range(int(pcs.Spec.Replicas))}
	}

	scaleOutIndices := sets.New(scaleOutReplicaIndices...)
	scaleInIndices := sets.New(scaleInReplicaIndices...)

	// Remove scale-out indices that have now been materialized.
	materializedReplicaIndices := findMaterializedScaleOutReplicaIndices(pcs, scaleOutReplicaIndices, snapshot)
	scaleOutIndices.Delete(materializedReplicaIndices...)

	projectedReplicaIndices := snapshot.projectedReplicaIndices(scaleOutIndices.UnsortedList(), scaleInIndices.UnsortedList())
	desiredReplicaCount := int(pcs.Spec.Replicas)

	if projectedReplicaIndices.Len() > desiredReplicaCount {
		numReplicasToRemove := projectedReplicaIndices.Len() - desiredReplicaCount
		selectedScaleInIndices := selectReplicaIndicesForScaleIn(pcs, snapshot, projectedReplicaIndices, scaleOutIndices, numReplicasToRemove)
		scaleOutIndices.Delete(selectedScaleInIndices...)
		scaleInIndices.Insert(selectedScaleInIndices...)
		projectedReplicaIndices.Delete(selectedScaleInIndices...)
	} else if projectedReplicaIndices.Len() < desiredReplicaCount {
		numReplicasToAdd := desiredReplicaCount - projectedReplicaIndices.Len()
		reservedIndices := snapshot.reservedReplicaIndices(scaleOutIndices.UnsortedList(), scaleInIndices.UnsortedList())
		selectedScaleOutIndices := utils.GetAvailableIndices(reservedIndices, numReplicasToAdd)
		scaleOutIndices.Insert(selectedScaleOutIndices...)
		projectedReplicaIndices.Insert(selectedScaleOutIndices...)
	}

	return pcssync.State{
		ExpectedReplicaIndices: sortedReplicaIndices(projectedReplicaIndices),
		ScaleOutReplicaIndices: sortedReplicaIndices(scaleOutIndices),
		ScaleInReplicaIndices:  sortedReplicaIndices(scaleInIndices),
	}
}

// findMaterializedScaleOutReplicaIndices returns recorded scale-out indices for which every expected standalone
// PodClique and PodCliqueScalingGroup resource exists.
// NOTE: Only existence of these resources is checked and not their readiness as that is sufficient.
func findMaterializedScaleOutReplicaIndices(
	pcs *grovecorev1alpha1.PodCliqueSet,
	scaleOutReplicaIndices []int,
	snapshot *replicaResourceSnapshot,
) []int {
	var materializedReplicaIndices []int
	for _, replicaIndex := range scaleOutReplicaIndices {
		pcsReplicaIndex := replicaIndex
		expectedPCLQNames := sets.New(componentutils.GetPodCliqueFQNsForPCSReplicaNotInPCSG(pcs, pcsReplicaIndex)...)
		if !snapshot.standalonePCLQNames(pcsReplicaIndex).IsSuperset(expectedPCLQNames) {
			continue
		}

		expectedPCSGNames := sets.New(componentutils.GetPCSGFQNsForPCSReplica(pcs, pcsReplicaIndex)...)
		if snapshot.pcsgNames(pcsReplicaIndex).IsSuperset(expectedPCSGNames) {
			materializedReplicaIndices = append(materializedReplicaIndices, replicaIndex)
		}
	}

	slices.Sort(materializedReplicaIndices)
	return materializedReplicaIndices
}

// selectReplicaIndicesForScaleIn selects the requested number of desired PCS replica indices for removal.
// It prefers established unavailable replicas, replicas with scale-out in progress, and then established available replicas.
// Higher indices are selected first within each group.
func selectReplicaIndicesForScaleIn(
	pcs *grovecorev1alpha1.PodCliqueSet,
	snapshot *replicaResourceSnapshot,
	projectedReplicaIndices sets.Set[int],
	scaleOutReplicaIndices sets.Set[int],
	count int) []int {
	if count <= 0 {
		return nil
	}
	numExpectedStandalonePCLQs := componentutils.CountStandalonePCLQs(pcs)
	numExpectedPCSGs := len(pcs.Spec.Template.PodCliqueScalingGroupConfigs)

	var (
		availableReplicaIndices   []int
		unavailableReplicaIndices []int
	)
	for replicaIndex := range projectedReplicaIndices {
		if scaleOutReplicaIndices.Has(replicaIndex) {
			continue
		}
		isReplicaAvailable := componentutils.IsPCSReplicaAvailable(
			numExpectedStandalonePCLQs,
			numExpectedPCSGs,
			snapshot.standalonePCLQsByReplicaIndex[replicaIndex],
			snapshot.pcsgsByReplicaIndex[replicaIndex],
		)
		if isReplicaAvailable {
			availableReplicaIndices = append(availableReplicaIndices, replicaIndex)
			continue
		}
		unavailableReplicaIndices = append(unavailableReplicaIndices, replicaIndex)
	}

	orderedReplicaIndices := sortAndCoalesceReplicaIndices(
		unavailableReplicaIndices,
		scaleOutReplicaIndices.UnsortedList(),
		availableReplicaIndices,
	)
	return orderedReplicaIndices[:min(count, len(orderedReplicaIndices))]
}

// sortAndCoalesceReplicaIndices sorts each group from highest to lowest and returns them in scale-in priority order.
func sortAndCoalesceReplicaIndices(unavailableReplicaIndices, scaleOutReplicaIndices, availableReplicaIndices []int) []int {
	sortReplicaIndicesDescending(unavailableReplicaIndices)
	sortReplicaIndicesDescending(scaleOutReplicaIndices)
	sortReplicaIndicesDescending(availableReplicaIndices)

	orderedReplicaIndices := make([]int, 0, len(unavailableReplicaIndices)+len(scaleOutReplicaIndices)+len(availableReplicaIndices))
	orderedReplicaIndices = append(orderedReplicaIndices, unavailableReplicaIndices...)
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

// sortedReplicaIndices returns replica indices in ascending order.
func sortedReplicaIndices(replicaIndices sets.Set[int]) []int {
	if replicaIndices.Len() == 0 {
		return nil
	}
	indices := replicaIndices.UnsortedList()
	slices.Sort(indices)
	return indices
}

// scalingProgressFromState converts reconcile-local scaling progress to its API representation.
func scalingProgressFromState(state pcssync.State) *grovecorev1alpha1.PodCliqueSetScalingProgress {
	if len(state.ScaleOutReplicaIndices) == 0 && len(state.ScaleInReplicaIndices) == 0 {
		return nil
	}

	progress := &grovecorev1alpha1.PodCliqueSetScalingProgress{}
	if len(state.ScaleOutReplicaIndices) > 0 {
		progress.ScaleOutReplicaIndices = make([]int32, len(state.ScaleOutReplicaIndices))
		for i, replicaIndex := range state.ScaleOutReplicaIndices {
			progress.ScaleOutReplicaIndices[i] = int32(replicaIndex)
		}
	}
	if len(state.ScaleInReplicaIndices) > 0 {
		progress.ScaleInReplicaIndices = make([]int32, len(state.ScaleInReplicaIndices))
		for i, replicaIndex := range state.ScaleInReplicaIndices {
			progress.ScaleInReplicaIndices[i] = int32(replicaIndex)
		}
	}
	return progress
}

// patchScalingProgress persists scaling progress and records the value that must be observed through the informer cache.
func (r *_resource) patchScalingProgress(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, progress *grovecorev1alpha1.PodCliqueSetScalingProgress) error {
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

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
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/sync"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/scalein"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// syncScalingProgress computes and persists replica-index selections and populates the state shared with later components.
func (r *_resource) syncScalingProgress(ctx context.Context, logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, state *pcssync.State) (bool, error) {
	snapshot, err := r.takeReplicaResourceSnapshot(ctx, pcs)
	if err != nil {
		return false, err
	}

	scaleOutReplicaIndices, scaleInReplicaIndices := replicaIndicesFromScalingProgress(pcs.Status.ScalingProgress)
	computedState, scaleInCosts, err := computeReplicaIndices(pcs, snapshot, scaleOutReplicaIndices, scaleInReplicaIndices, func() (map[int]int32, error) {
		return r.replicaScaleInCosts(ctx, pcs)
	})
	if err != nil {
		return false, err
	}
	computedProgress := scalingProgressFromState(computedState)
	scalingProgressChanged := !pcssync.ScalingProgressEqual(pcs.Status.ScalingProgress, computedProgress)
	if scalingProgressChanged {
		if err := r.patchScalingProgress(ctx, pcs, computedProgress); err != nil {
			return false, err
		}
		r.recordNewScalingSelectionEvents(logger, pcs, scaleOutReplicaIndices, scaleInReplicaIndices, computedState, scaleInCosts)
	}

	*state = computedState
	return scalingProgressChanged, nil
}

// recordNewScalingSelectionEvents records newly selected scale-in indices and newly reserved scale-out indices.
// scaleInCosts holds the scale-in cost of every replica the selection considered. The scale-in log line always
// carries the selected replicas' costs, and the event carries them when any of them is non-zero, so a scale-in
// without a cost reads as it did before.
func (r *_resource) recordNewScalingSelectionEvents(logger logr.Logger, pcs *grovecorev1beta1.PodCliqueSet, previousScaleOutReplicaIndices, previousScaleInReplicaIndices []int, computedState pcssync.State, scaleInCosts map[int]int32) {
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
		selectedCosts, anyNonZero := formatScaleInCosts(pcsReplicaIndices, scaleInCosts)
		logger.Info("Selected PCS replica indices for scale-in", "pcsReplicaIndices", pcsReplicaIndices, "scaleInCost", selectedCosts)
		message := fmt.Sprintf("Selected PCS replica indices %v for scale-in", pcsReplicaIndices)
		if anyNonZero {
			message += " (scale-in cost: " + selectedCosts + ")"
		}
		r.eventRecorder.Event(pcs, corev1.EventTypeNormal, constants.ReasonPodCliqueSetReplicaIndicesSelectedForScaleIn, message)
	}
}

// formatScaleInCosts renders the selected replicas' scale-in costs as "index=cost" pairs in index order and
// reports whether any of them is non-zero.
func formatScaleInCosts(selectedReplicaIndices []int, scaleInCosts map[int]int32) (string, bool) {
	pairs := make([]string, 0, len(selectedReplicaIndices))
	anyNonZero := false
	for _, replicaIndex := range selectedReplicaIndices {
		cost := scaleInCosts[replicaIndex]
		anyNonZero = anyNonZero || cost != 0
		pairs = append(pairs, fmt.Sprintf("%d=%d", replicaIndex, cost))
	}
	return strings.Join(pairs, ","), anyNonZero
}

// replicaScaleInCosts lists the PodCliqueSet's pods from the informer cache and returns the scale-in cost of every
// replica index that has a non-terminating pod: the minimum over the replica's pods of what the cost source reports.
// It runs only when a scale-in is being computed, so a reconcile with no scale-in reads no pods. The PodGang
// component lists the same pods from the same cache on every sync, so this adds no watch and no apiserver call.
// A source that reads no cost lists nothing: every replica costs 0 and the scale-in does the work it did before
// the cost existed, with no list and no list error to fail on.
func (r *_resource) replicaScaleInCosts(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet) (map[int]int32, error) {
	if scalein.ReadsNoCost(r.costSource) {
		return nil, nil
	}
	podList := &corev1.PodList{}
	if err := r.client.List(ctx,
		podList,
		client.InNamespace(pcs.Namespace),
		client.MatchingLabels(apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name)),
	); err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPods,
			component.OperationSync,
			fmt.Sprintf("failed to list Pods by replica for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	podsByReplicaIndex := make(map[int][]*corev1.Pod)
	for i := range podList.Items {
		pod := &podList.Items[i]
		if k8sutils.IsResourceTerminating(pod.ObjectMeta) {
			continue
		}
		replicaIndex, err := k8sutils.GetPodCliqueSetReplicaIndex(pod.ObjectMeta)
		if err != nil {
			// A pod without a readable replica index belongs to no replica a scale-in can select.
			continue
		}
		podsByReplicaIndex[replicaIndex] = append(podsByReplicaIndex[replicaIndex], pod)
	}
	costs := make(map[int]int32, len(podsByReplicaIndex))
	for replicaIndex, pods := range podsByReplicaIndex {
		costs[replicaIndex] = scalein.MinCost(r.costSource, pods)
	}
	return costs, nil
}

// replicaScaleInCostLookup returns the scale-in cost per PCS replica index. It is called only when a scale-in is
// being computed, so a reconcile without one costs nothing. A replica absent from the map costs 0.
type replicaScaleInCostLookup func() (map[int]int32, error)

// takeReplicaResourceSnapshot lists standalone PodCliques and PodCliqueScalingGroups owned by the PCS and groups them by PCS replica index.
func (r *_resource) takeReplicaResourceSnapshot(ctx context.Context, pcs *grovecorev1beta1.PodCliqueSet) (*replicaResourceSnapshot, error) {
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
	standalonePCLQsByReplicaIndex     map[int][]grovecorev1beta1.PodClique
	pcsgsByReplicaIndex               map[int][]grovecorev1beta1.PodCliqueScalingGroup
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
func replicaIndicesFromScalingProgress(progress *grovecorev1beta1.PodCliqueSetScalingProgress) ([]int, []int) {
	if progress == nil {
		return nil, nil
	}

	scaleOutReplicaIndices := lo.Map(progress.ScaleOutReplicaIndices, func(replicaIndex int32, _ int) int { return int(replicaIndex) })
	scaleInReplicaIndices := lo.Map(progress.ScaleInReplicaIndices, func(replicaIndex int32, _ int) int { return int(replicaIndex) })

	return scaleOutReplicaIndices, scaleInReplicaIndices
}

// computeReplicaIndices determines which replica indices must exist and which are being added or removed.
// It uses the observed resources, the recorded scaling progress, and the desired replica count. All returned slices are sorted.
// The scale-in costs are read through lookupScaleInCosts only when replicas have to be removed, and the costs read are
// returned beside the state so the selection event can carry them.
func computeReplicaIndices(pcs *grovecorev1beta1.PodCliqueSet, snapshot *replicaResourceSnapshot, scaleOutReplicaIndices []int, scaleInReplicaIndices []int, lookupScaleInCosts replicaScaleInCostLookup) (pcssync.State, map[int]int32, error) {
	// An unobserved PCS is still being provisioned for the first time. Its initial
	// identities are not scale-out work and therefore do not require reservations.
	if pcs.Status.ObservedGeneration == nil {
		return pcssync.State{ExpectedReplicaIndices: lo.Range(int(pcs.Spec.Replicas))}, nil, nil
	}

	scaleOutIndices := sets.New(scaleOutReplicaIndices...)
	scaleInIndices := sets.New(scaleInReplicaIndices...)

	// Remove scale-out indices that have now been materialized.
	materializedReplicaIndices := findMaterializedScaleOutReplicaIndices(pcs, scaleOutReplicaIndices, snapshot)
	scaleOutIndices.Delete(materializedReplicaIndices...)

	projectedReplicaIndices := snapshot.projectedReplicaIndices(scaleOutIndices.UnsortedList(), scaleInIndices.UnsortedList())
	desiredReplicaCount := int(pcs.Spec.Replicas)

	var scaleInCosts map[int]int32
	if projectedReplicaIndices.Len() > desiredReplicaCount {
		numReplicasToRemove := projectedReplicaIndices.Len() - desiredReplicaCount
		var err error
		if scaleInCosts, err = lookupScaleInCosts(); err != nil {
			return pcssync.State{}, nil, err
		}
		selectedScaleInIndices := selectReplicaIndicesForScaleIn(pcs, snapshot, projectedReplicaIndices, scaleOutIndices, scaleInCosts, numReplicasToRemove)
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
	}, scaleInCosts, nil
}

// findMaterializedScaleOutReplicaIndices returns recorded scale-out indices for which every expected standalone
// PodClique and PodCliqueScalingGroup resource exists.
// NOTE: Only existence of these resources is checked and not their readiness as that is sufficient.
func findMaterializedScaleOutReplicaIndices(
	pcs *grovecorev1beta1.PodCliqueSet,
	scaleOutReplicaIndices []int,
	snapshot *replicaResourceSnapshot,
) []int {
	var materializedReplicaIndices []int
	for _, pcsReplicaIndex := range scaleOutReplicaIndices {
		expectedPCLQNames := sets.New(componentutils.GetPodCliqueFQNsForPCSReplicaNotInPCSG(pcs, pcsReplicaIndex)...)
		if !snapshot.standalonePCLQNames(pcsReplicaIndex).IsSuperset(expectedPCLQNames) {
			continue
		}

		expectedPCSGNames := sets.New(componentutils.GetPCSGFQNsForPCSReplica(pcs, pcsReplicaIndex)...)
		if snapshot.pcsgNames(pcsReplicaIndex).IsSuperset(expectedPCSGNames) {
			materializedReplicaIndices = append(materializedReplicaIndices, pcsReplicaIndex)
		}
	}

	slices.Sort(materializedReplicaIndices)
	return materializedReplicaIndices
}

// selectReplicaIndicesForScaleIn selects the requested number of desired PCS replica indices for removal.
// It prefers established replicas with insufficient scheduled capacity, established replicas with sufficient
// scheduled but insufficient available capacity, replicas with scale-out in progress, and then established
// available replicas. Within each group the lowest scale-in cost is selected first, and among equal costs the
// higher index. scaleInCosts holds a cost per replica index; a replica absent from it costs 0. A cost never
// moves a replica across groups, so an available replica is never selected ahead of an unavailable one.
func selectReplicaIndicesForScaleIn(
	pcs *grovecorev1beta1.PodCliqueSet,
	snapshot *replicaResourceSnapshot,
	projectedReplicaIndices sets.Set[int],
	scaleOutReplicaIndices sets.Set[int],
	scaleInCosts map[int]int32,
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
	for replicaIndex := range projectedReplicaIndices {
		if scaleOutReplicaIndices.Has(replicaIndex) {
			continue
		}
		standalonePCLQs := snapshot.standalonePCLQsByReplicaIndex[replicaIndex]
		pcsgs := snapshot.pcsgsByReplicaIndex[replicaIndex]
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
		scaleInCosts,
	)
	return orderedReplicaIndices[:min(count, len(orderedReplicaIndices))]
}

// sortAndCoalesceReplicaIndices sorts each group by ascending scale-in cost, then from highest to lowest index, and
// returns them in scale-in priority order. The groups are never merged: a cost orders replicas within a group only.
func sortAndCoalesceReplicaIndices(insufficientScheduledReplicaIndices, insufficientAvailableReplicaIndices, scaleOutReplicaIndices, availableReplicaIndices []int, scaleInCosts map[int]int32) []int {
	sortReplicaIndicesByCostThenDescending(insufficientScheduledReplicaIndices, scaleInCosts)
	sortReplicaIndicesByCostThenDescending(insufficientAvailableReplicaIndices, scaleInCosts)
	sortReplicaIndicesByCostThenDescending(scaleOutReplicaIndices, scaleInCosts)
	sortReplicaIndicesByCostThenDescending(availableReplicaIndices, scaleInCosts)

	capacity := len(insufficientScheduledReplicaIndices) + len(insufficientAvailableReplicaIndices) + len(scaleOutReplicaIndices) + len(availableReplicaIndices)
	orderedReplicaIndices := make([]int, 0, capacity)
	orderedReplicaIndices = append(orderedReplicaIndices, insufficientScheduledReplicaIndices...)
	orderedReplicaIndices = append(orderedReplicaIndices, insufficientAvailableReplicaIndices...)
	orderedReplicaIndices = append(orderedReplicaIndices, scaleOutReplicaIndices...)
	orderedReplicaIndices = append(orderedReplicaIndices, availableReplicaIndices...)

	return orderedReplicaIndices
}

// sortReplicaIndicesByCostThenDescending sorts replica indices by ascending scale-in cost, then from highest to
// lowest index. A replica absent from scaleInCosts costs 0, so with no costs at all the order is descending index.
func sortReplicaIndicesByCostThenDescending(replicaIndices []int, scaleInCosts map[int]int32) {
	slices.SortFunc(replicaIndices, func(a, b int) int {
		return cmp.Or(cmp.Compare(scaleInCosts[a], scaleInCosts[b]), cmp.Compare(b, a))
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
func scalingProgressFromState(state pcssync.State) *grovecorev1beta1.PodCliqueSetScalingProgress {
	if len(state.ScaleOutReplicaIndices) == 0 && len(state.ScaleInReplicaIndices) == 0 {
		return nil
	}

	progress := &grovecorev1beta1.PodCliqueSetScalingProgress{}
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

// Copyright 2025 The Grove Authors.
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

package podcliqueset

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/clustertopology"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// reconcileStatus updates the PodCliqueSet status with current replica counts and rolling update progress
func (r *Reconciler) reconcileStatus(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	// Snapshot status before mutations so we can skip the Update call when nothing changes.
	originalStatus := pcs.Status.DeepCopy()

	// Calculate available replicas and update-progress stats in a single pass over the children.
	standalonePCLQs, pcsgs, err := r.listExpectedPCSChildren(ctx, pcs)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("failed to list PodCliqueSet children", err)
	}
	stats, err := r.computeAvailableAndUpdatedReplicas(logger, pcs, standalonePCLQs, pcsgs)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("failed to compute PodCliqueSet replica status", err)
	}
	mutateReplicas(pcs, stats)
	mutateUpdateInProgressCondition(pcs, computeUpdateInProgressCounts(standalonePCLQs, pcsgs))

	// Update TopologyLevelsUnavailable condition based on TAS config and ClusterTopologyBinding
	if err = r.mutateTopologyLevelUnavailableConditions(ctx, logger, pcs); err != nil {
		return ctrlcommon.ReconcileWithErrors("failed to mutate TopologyLevelsUnavailable condition", err)
	}

	if err = mutateSelector(pcs); err != nil {
		return ctrlcommon.ReconcileWithErrors("failed to update selector for PodCliqueSet", err)
	}

	// Skip the status update when every mutate* above left status byte-identical to what
	// the previous reconcile already persisted. The mutators are the only code writing
	// pcs.Status here, so equality means there is nothing for the apiserver to store.
	// Issuing the Update anyway bumps resourceVersion and fires a watch event that wakes
	// every PCS observer and cascades into spurious reconciles. equality.Semantic is
	// required because the status mixes counters, pointers, and conditions.
	if equality.Semantic.DeepEqual(*originalStatus, pcs.Status) {
		return ctrlcommon.ContinueReconcile()
	}

	// Update the PodCliqueSet status
	if err = r.client.Status().Update(ctx, pcs); err != nil {
		return ctrlcommon.ReconcileWithErrors("failed to update PodCliqueSet status", err)
	}
	return ctrlcommon.ContinueReconcile()
}

// mutateReplicas updates the PodCliqueSet status replica counts and update-progress counts.
func mutateReplicas(pcs *grovecorev1alpha1.PodCliqueSet, stats pcsReplicaStats) {
	pcs.Status.Replicas = pcs.Spec.Replicas
	pcs.Status.AvailableReplicas = stats.availableReplicas
	pcs.Status.UpdatedReplicas = stats.updatedReplicas
	if pcs.Status.UpdateProgress != nil {
		pcs.Status.UpdateProgress.UpdatedPodCliquesCount = stats.updatedPCLQs
		pcs.Status.UpdateProgress.TotalPodCliquesCount = stats.totalPCLQs
		pcs.Status.UpdateProgress.UpdatedPodCliqueScalingGroupsCount = stats.updatedPCSGs
		pcs.Status.UpdateProgress.TotalPodCliqueScalingGroupsCount = stats.totalPCSGs
	}
}

// mutateSelector publishes the label selector on the PodCliqueSet /scale subresource so HPAs can
// target the whole PodCliqueSet. PCS is the top-level scope, so the default labels are already
// the complete selector; no per-resource narrowing label like PodClique/PodCliqueScalingGroup is
// needed.
func mutateSelector(pcs *grovecorev1alpha1.PodCliqueSet) error {
	selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels: apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
	})
	if err != nil {
		return fmt.Errorf("%w: failed to create label selector for PodCliqueSet %v", err, client.ObjectKeyFromObject(pcs))
	}
	pcs.Status.Selector = ptr.To(selector.String())
	return nil
}

// pcsReplicaStats aggregates replica- and child-level update progress derived from a single
// pass over the informer cache.
type pcsReplicaStats struct {
	availableReplicas int32
	updatedReplicas   int32
	// updatedPCLQs counts standalone PCLQs (not in a PCSG) whose CurrentPodCliqueSetGenerationHash
	// matches pcs.Status.CurrentGenerationHash. PCSG-owned PCLQs are tracked on their owning PCSG.
	updatedPCLQs int32
	totalPCLQs   int32
	updatedPCSGs int32
	totalPCSGs   int32
}

// updateInProgressCounts holds the number of rolling and stuck children of each kind, derived from
// their UpdateInProgress conditions. rolling counts children whose condition is True or Unknown, and
// stuck counts those whose condition is Unknown.
type updateInProgressCounts struct {
	rollingPCLQs int32
	stuckPCLQs   int32
	rollingPCSGs int32
	stuckPCSGs   int32
}

// listExpectedPCSChildren fetches the PodCliqueSet's standalone PodCliques and PodCliqueScalingGroups
// and drops any strays that are not part of the current spec.
func (r *Reconciler) listExpectedPCSChildren(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) ([]grovecorev1alpha1.PodClique, []grovecorev1alpha1.PodCliqueScalingGroup, error) {
	// Hoist the expected-name lookups out of the filter callbacks so each pass is O(M) with a single
	// map lookup per element.
	expectedPCSGNameSet := flattenNamesToSet(componentutils.GetExpectedPCSGFQNsPerPCSReplica(pcs))
	expectedStandalonePCLQNameSet := flattenNamesToSet(componentutils.GetExpectedStandAlonePCLQFQNsPerPCSReplica(pcs))

	// Fetch all PCSGs for this PCS, then drop any stray PCSGs (not part of the spec). slices.DeleteFunc
	// compacts in-place; safe here because the slice came from a fresh fetch and isn't aliased.
	pcsgs, err := componentutils.GetPCSGsForPCS(ctx, r.client, pcs.ObjectMeta)
	if err != nil {
		return nil, nil, err
	}
	pcsgs = slices.DeleteFunc(pcsgs, func(pcsg grovecorev1alpha1.PodCliqueScalingGroup) bool {
		_, expected := expectedPCSGNameSet[pcsg.Name]
		return !expected
	})

	// Fetch all standalone PodCliques for this PCS and drop strays the same way.
	standalonePCLQs, err := componentutils.GetPodCliquesWithParentPCS(ctx, r.client, pcs.ObjectMeta)
	if err != nil {
		return nil, nil, err
	}
	standalonePCLQs = slices.DeleteFunc(standalonePCLQs, func(pclq grovecorev1alpha1.PodClique) bool {
		_, expected := expectedStandalonePCLQNameSet[pclq.Name]
		return !expected
	})
	return standalonePCLQs, pcsgs, nil
}

// computeAvailableAndUpdatedReplicas groups the PodCliqueSet's children by replica index and returns
// aggregate availability and update counts.
func (r *Reconciler) computeAvailableAndUpdatedReplicas(logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, standalonePCLQs []grovecorev1alpha1.PodClique, pcsgs []grovecorev1alpha1.PodCliqueScalingGroup) (pcsReplicaStats, error) {
	var stats pcsReplicaStats
	expectedPCSGFQNsPerPCSReplica := componentutils.GetExpectedPCSGFQNsPerPCSReplica(pcs)
	expectedStandAlonePCLQFQNsPerPCSReplica := componentutils.GetExpectedStandAlonePCLQFQNsPerPCSReplica(pcs)

	standalonePCLQsByReplica, err := componentutils.GroupPCLQsByPCSReplicaIndex(standalonePCLQs)
	if err != nil {
		return stats, err
	}
	pcsgsByReplica, err := componentutils.GroupPCSGsByPCSReplicaIndex(pcsgs)
	if err != nil {
		return stats, err
	}

	for replicaIndex := 0; replicaIndex < int(pcs.Spec.Replicas); replicaIndex++ {
		replicaStandalonePCLQs := standalonePCLQsByReplica[replicaIndex]
		replicaPCSGs := pcsgsByReplica[replicaIndex]
		expectedPCSGCount := len(expectedPCSGFQNsPerPCSReplica[replicaIndex])
		expectedPCLQCount := len(expectedStandAlonePCLQFQNsPerPCSReplica[replicaIndex])

		stats.totalPCLQs += int32(expectedPCLQCount)
		stats.totalPCSGs += int32(expectedPCSGCount)
		stats.updatedPCLQs += countUpdatedPCLQs(pcs, replicaStandalonePCLQs)
		stats.updatedPCSGs += countUpdatedPCSGs(pcs.Status.CurrentGenerationHash, replicaPCSGs)

		isReplicaAvailable, isReplicaUpdated := r.computeReplicaStatus(pcs, replicaPCSGs,
			replicaStandalonePCLQs, expectedPCSGCount, expectedPCLQCount)
		if isReplicaAvailable {
			stats.availableReplicas++
		}
		if isReplicaUpdated {
			stats.updatedReplicas++
		}
	}

	logger.Info(fmt.Sprintf("Calculated PCS replica and update progress stats for %s: available=%d updated=%d PCLQs=%d/%d PCSGs=%d/%d",
		client.ObjectKeyFromObject(pcs), stats.availableReplicas, stats.updatedReplicas,
		stats.updatedPCLQs, stats.totalPCLQs,
		stats.updatedPCSGs, stats.totalPCSGs))
	return stats, nil
}

// computeUpdateInProgressCounts counts the rolling and stuck standalone PodCliques and
// PodCliqueScalingGroups from their UpdateInProgress conditions.
func computeUpdateInProgressCounts(standalonePCLQs []grovecorev1alpha1.PodClique, pcsgs []grovecorev1alpha1.PodCliqueScalingGroup) updateInProgressCounts {
	var counts updateInProgressCounts
	for i := range standalonePCLQs {
		if rolling, stuck := updateInProgressState(standalonePCLQs[i].Status.Conditions); rolling {
			counts.rollingPCLQs++
			if stuck {
				counts.stuckPCLQs++
			}
		}
	}
	for i := range pcsgs {
		if rolling, stuck := updateInProgressState(pcsgs[i].Status.Conditions); rolling {
			counts.rollingPCSGs++
			if stuck {
				counts.stuckPCSGs++
			}
		}
	}
	return counts
}

// countUpdatedPCLQs counts non-terminating standalone PCLQs that have fully converged to the PCS hash.
func countUpdatedPCLQs(pcs *grovecorev1alpha1.PodCliqueSet, pclqs []grovecorev1alpha1.PodClique) int32 {
	if pcs.Status.CurrentGenerationHash == nil {
		return 0
	}
	var n int32
	for i := range pclqs {
		pclq := &pclqs[i]
		if k8sutils.IsResourceTerminating(pclq.ObjectMeta) {
			continue
		}
		if isStandalonePCLQUpdated(pcs, pclq) {
			n++
		}
	}
	return n
}

// countUpdatedPCSGs counts non-terminating PCSGs whose update completed at the PCS hash.
func countUpdatedPCSGs(pcsGenerationHash *string, pcsgs []grovecorev1alpha1.PodCliqueScalingGroup) int32 {
	if pcsGenerationHash == nil {
		return 0
	}
	var n int32
	for i := range pcsgs {
		pcsg := &pcsgs[i]
		if k8sutils.IsResourceTerminating(pcsg.ObjectMeta) {
			continue
		}
		if componentutils.IsPCSGUpdateComplete(pcsg, *pcsGenerationHash) {
			n++
		}
	}
	return n
}

// computeReplicaStatus determines if a replica is available and updated based on its components.
func (r *Reconciler) computeReplicaStatus(pcs *grovecorev1alpha1.PodCliqueSet, replicaPCSGs []grovecorev1alpha1.PodCliqueScalingGroup, standalonePCLQs []grovecorev1alpha1.PodClique, expectedPCSGs int, expectedStandalonePCLQs int) (bool, bool) {
	pclqsAvailable, pclqsUpdated := r.computePCLQsStatus(pcs, expectedStandalonePCLQs, standalonePCLQs)
	pcsgsAvailable, pcsgsUpdated := r.computePCSGsStatus(pcs.Status.CurrentGenerationHash, expectedPCSGs, replicaPCSGs)
	return pclqsAvailable && pcsgsAvailable, pclqsUpdated && pcsgsUpdated
}

// computePCLQsStatus checks if standalone PodCliques are available and updated.
func (r *Reconciler) computePCLQsStatus(pcs *grovecorev1alpha1.PodCliqueSet, expectedStandalonePCLQs int, existingPCLQs []grovecorev1alpha1.PodClique) (isAvailable, isUpdated bool) {
	nonTerminatedPCLQs := lo.Filter(existingPCLQs, func(pclq grovecorev1alpha1.PodClique, _ int) bool {
		return !k8sutils.IsResourceTerminating(pclq.ObjectMeta)
	})

	isAvailable = len(nonTerminatedPCLQs) == expectedStandalonePCLQs &&
		lo.EveryBy(nonTerminatedPCLQs, func(pclq grovecorev1alpha1.PodClique) bool {
			return pclq.Status.ReadyReplicas >= *pclq.Spec.MinAvailable
		})

	isUpdated = isAvailable && lo.EveryBy(nonTerminatedPCLQs, func(pclq grovecorev1alpha1.PodClique) bool {
		return isStandalonePCLQUpdated(pcs, &pclq)
	})

	return
}

// isStandalonePCLQUpdated checks if a standalone PodClique is fully updated to the expected pod template and PodCliqueSet generation hashes.
func isStandalonePCLQUpdated(pcs *grovecorev1alpha1.PodCliqueSet, pclq *grovecorev1alpha1.PodClique) bool {
	if pcs.Status.CurrentGenerationHash == nil || pclq.Spec.MinAvailable == nil {
		return false
	}
	expectedPodTemplateHash, err := componentutils.GetExpectedPCLQPodTemplateHash(pcs, pclq.ObjectMeta)
	if err != nil || expectedPodTemplateHash == "" {
		return false
	}
	return pclq.Labels[apicommon.LabelPodTemplateHash] == expectedPodTemplateHash &&
		pclq.Status.CurrentPodTemplateHash != nil &&
		*pclq.Status.CurrentPodTemplateHash == expectedPodTemplateHash &&
		pclq.Status.CurrentPodCliqueSetGenerationHash != nil &&
		*pclq.Status.CurrentPodCliqueSetGenerationHash == *pcs.Status.CurrentGenerationHash &&
		pclq.Status.ReadyReplicas >= *pclq.Spec.MinAvailable &&
		pclq.Status.UpdatedReplicas >= *pclq.Spec.MinAvailable
}

// computePCSGsStatus checks if PodCliqueScalingGroups are available and updated.
func (r *Reconciler) computePCSGsStatus(pcsGenerationHash *string, expectedPCSGs int, pcsgs []grovecorev1alpha1.PodCliqueScalingGroup) (isAvailable, isUpdated bool) {
	nonTerminatedPCSGs := lo.Filter(pcsgs, func(pcsg grovecorev1alpha1.PodCliqueScalingGroup, _ int) bool {
		return !k8sutils.IsResourceTerminating(pcsg.ObjectMeta)
	})

	isAvailable = expectedPCSGs == len(nonTerminatedPCSGs) &&
		lo.EveryBy(nonTerminatedPCSGs, func(pcsg grovecorev1alpha1.PodCliqueScalingGroup) bool {
			return pcsg.Status.AvailableReplicas >= *pcsg.Spec.MinAvailable
		})

	isUpdated = isAvailable && lo.EveryBy(nonTerminatedPCSGs, func(pcsg grovecorev1alpha1.PodCliqueScalingGroup) bool {
		return pcsGenerationHash != nil && componentutils.IsPCSGUpdateComplete(&pcsg, *pcsGenerationHash)
	})

	return
}

func (r *Reconciler) mutateTopologyLevelUnavailableConditions(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	if !r.tasConfig.Enabled {
		// If TAS is disabled but PCS has topology constraints, surface a warning condition.
		if len(componentutils.GetUniqueTopologyDomainsInPodCliqueSet(pcs)) > 0 {
			cond := metav1.Condition{
				Type:               apicommonconstants.ConditionTopologyLevelsUnavailable,
				Status:             metav1.ConditionUnknown,
				Reason:             apicommonconstants.ConditionReasonTopologyAwareSchedulingDisabled,
				Message:            "Topology constraints are defined but Topology Aware Scheduling is disabled",
				ObservedGeneration: pcs.Generation,
				LastTransitionTime: metav1.Now(),
			}
			if k8sutils.HasConditionChanged(pcs.Status.Conditions, cond) {
				logger.Info("Updating TopologyLevelsUnavailable condition for PodCliqueSet",
					"pcs", client.ObjectKeyFromObject(pcs),
					"reason", cond.Reason)
				meta.SetStatusCondition(&pcs.Status.Conditions, cond)
			}
			return nil
		}
		// Clear any existing topology level unavailable conditions if TAS is disabled
		meta.RemoveStatusCondition(&pcs.Status.Conditions, apicommonconstants.ConditionTopologyLevelsUnavailable)
		return nil
	}
	// compute the new TopologyLevelsUnavailable condition based on ClusterTopologyBinding and PodCliqueSet TopologyConstraints.
	newCond, err := r.computeTopologyLevelsUnavailableCondition(ctx, pcs)
	if err != nil {
		return err
	}
	if k8sutils.HasConditionChanged(pcs.Status.Conditions, newCond) {
		logger.Info("Updating TopologyLevelsUnavailable condition for PodCliqueSet",
			"pcs", client.ObjectKeyFromObject(pcs),
			"type", newCond.Type,
			"status", newCond.Status,
			"reason", newCond.Reason)
		meta.SetStatusCondition(&pcs.Status.Conditions, newCond)
	}
	return nil
}

// computeTopologyLevelsUnavailableCondition computes the TopologyLevelsUnavailable condition for the PodCliqueSet.
// It checks the PodCliqueSet's topology constraints against the topology levels defined in the single
// ClusterTopologyBinding referenced by the explicit topology constraints in the PodCliqueSet.
// If any topology domains used by the PodCliqueSet are not available in that ClusterTopologyBinding, it sets the condition to True.
// If all referenced topology domains are available, it sets the condition to False.
// If the ClusterTopologyBinding resource is not found, or an explicit topology constraint is incomplete, it sets the condition to Unknown.
func (r *Reconciler) computeTopologyLevelsUnavailableCondition(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (metav1.Condition, error) {
	if !componentutils.HasAnyTopologyConstraint(pcs) {
		return metav1.Condition{
			Type:               apicommonconstants.ConditionTopologyLevelsUnavailable,
			Status:             metav1.ConditionFalse,
			Reason:             apicommonconstants.ConditionReasonAllTopologyLevelsAvailable,
			Message:            "No topology constraints defined",
			ObservedGeneration: pcs.Generation,
			LastTransitionTime: metav1.Now(),
		}, nil
	}

	topologyName, err := componentutils.FindExplicitTopologyNameForPodCliqueSet(pcs)
	if err != nil {
		if errors.Is(err, componentutils.ErrTopologyNameMissing) {
			return metav1.Condition{
				Type:               apicommonconstants.ConditionTopologyLevelsUnavailable,
				Status:             metav1.ConditionUnknown,
				Reason:             apicommonconstants.ConditionReasonTopologyNameMissing,
				Message:            "PodCliqueSet topology constraints must include topologyName",
				ObservedGeneration: pcs.Generation,
				LastTransitionTime: metav1.Now(),
			}, nil
		}
		return metav1.Condition{}, fmt.Errorf("failed to find explicit topologyName: %w", err)
	}

	topologyLevels, err := clustertopology.GetClusterTopologyLevels(ctx, r.client, topologyName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return metav1.Condition{
				Type:               apicommonconstants.ConditionTopologyLevelsUnavailable,
				Status:             metav1.ConditionUnknown,
				Reason:             apicommonconstants.ConditionReasonClusterTopologyNotFound,
				Message:            "ClusterTopologyBinding resource not found",
				ObservedGeneration: pcs.Generation,
				LastTransitionTime: metav1.Now(),
			}, nil
		}
		return metav1.Condition{}, fmt.Errorf("failed to get topology levels: %w", err)
	}
	availableTopologyDomains := lo.Map(topologyLevels, func(tl grovecorev1alpha1.TopologyLevel, _ int) grovecorev1alpha1.TopologyDomain { return tl.Domain })
	pcsTopologyDomains := componentutils.GetUniqueTopologyDomainsInPodCliqueSet(pcs)
	unavailableTopologyDomains, _ := lo.Difference(pcsTopologyDomains, availableTopologyDomains)
	if len(unavailableTopologyDomains) > 0 {
		return metav1.Condition{
			Type:               apicommonconstants.ConditionTopologyLevelsUnavailable,
			Status:             metav1.ConditionTrue,
			Reason:             apicommonconstants.ConditionReasonTopologyLevelsUnavailable,
			Message:            fmt.Sprintf("Unavailable topology domains: %v", unavailableTopologyDomains),
			ObservedGeneration: pcs.Generation,
			LastTransitionTime: metav1.Now(),
		}, nil
	}
	return metav1.Condition{
		Type:               apicommonconstants.ConditionTopologyLevelsUnavailable,
		Status:             metav1.ConditionFalse,
		Reason:             apicommonconstants.ConditionReasonAllTopologyLevelsAvailable,
		Message:            "All topology levels are available",
		ObservedGeneration: pcs.Generation,
		LastTransitionTime: metav1.Now(),
	}, nil
}

// flattenNamesToSet flattens a per-replica expected-name map into a set for O(1) membership tests.
// Used to prune stray children that aren't part of the spec without paying O(M*E) per filter pass.
func flattenNamesToSet(perReplica map[int][]string) map[string]struct{} {
	total := 0
	for _, names := range perReplica {
		total += len(names)
	}
	set := make(map[string]struct{}, total)
	for _, names := range perReplica {
		for _, n := range names {
			set[n] = struct{}{}
		}
	}
	return set
}

// updateInProgressState reports whether a child's UpdateInProgress condition marks it as rolling
// (True or Unknown) and, of those, stuck (Unknown).
func updateInProgressState(conditions []metav1.Condition) (rolling, stuck bool) {
	cond := meta.FindStatusCondition(conditions, apicommonconstants.ConditionTypeUpdateInProgress)
	if cond == nil {
		return false, false
	}
	switch cond.Status {
	case metav1.ConditionTrue:
		return true, false
	case metav1.ConditionUnknown:
		return true, true
	default:
		return false, false
	}
}

// mutateUpdateInProgressCondition sets the aggregate UpdateInProgress condition on the PodCliqueSet
// from the rolling and stuck child counts.
func mutateUpdateInProgressCondition(pcs *grovecorev1alpha1.PodCliqueSet, counts updateInProgressCounts) {
	newCondition := computeUpdateInProgressCondition(counts)
	if k8sutils.HasConditionChanged(pcs.Status.Conditions, newCondition) {
		meta.SetStatusCondition(&pcs.Status.Conditions, newCondition)
	}
}

// computeUpdateInProgressCondition aggregates the child UpdateInProgress conditions. Any stuck child
// makes the PodCliqueSet Unknown (ProgressDeadlineExceeded) with a per-kind stuck-over-rolling
// message, any rolling child makes it True (Progressing), and otherwise it is False (NoActiveUpdate).
func computeUpdateInProgressCondition(counts updateInProgressCounts) metav1.Condition {
	now := metav1.Now()
	if counts.stuckPCLQs > 0 || counts.stuckPCSGs > 0 {
		var parts []string
		if counts.rollingPCLQs > 0 {
			parts = append(parts, fmt.Sprintf("%d/%d PodCliques stuck", counts.stuckPCLQs, counts.rollingPCLQs))
		}
		if counts.rollingPCSGs > 0 {
			parts = append(parts, fmt.Sprintf("%d/%d PodCliqueScalingGroups stuck", counts.stuckPCSGs, counts.rollingPCSGs))
		}
		return metav1.Condition{
			Type:               apicommonconstants.ConditionTypeUpdateInProgress,
			Status:             metav1.ConditionUnknown,
			Reason:             apicommonconstants.ConditionReasonProgressDeadlineExceeded,
			Message:            strings.Join(parts, ", "),
			LastTransitionTime: now,
		}
	}
	if counts.rollingPCLQs > 0 || counts.rollingPCSGs > 0 {
		return metav1.Condition{
			Type:               apicommonconstants.ConditionTypeUpdateInProgress,
			Status:             metav1.ConditionTrue,
			Reason:             apicommonconstants.ConditionReasonProgressing,
			Message:            "Rolling update is in progress",
			LastTransitionTime: now,
		}
	}
	return metav1.Condition{
		Type:               apicommonconstants.ConditionTypeUpdateInProgress,
		Status:             metav1.ConditionFalse,
		Reason:             apicommonconstants.ConditionReasonNoActiveUpdate,
		Message:            "No rolling update is in progress",
		LastTransitionTime: now,
	}
}

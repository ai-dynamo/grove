// /*
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
// */

package podcliqueset

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/clustertopology"
	ctrlcommon "github.com/ai-dynamo/grove/operator/internal/controller/common"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// reconcileStatus updates the PodCliqueSet status with current replica counts and rolling update progress
func (r *Reconciler) reconcileStatus(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
	// Snapshot status before mutations so we can skip the Update call when nothing changes.
	originalStatus := pcs.Status.DeepCopy()

	// Calculate available replicas using PCSG-inspired approach
	err := r.mutateReplicas(ctx, logger, pcs)
	if err != nil {
		return ctrlcommon.ReconcileWithErrors("failed to mutate replicas status", err)
	}

	// Update TopologyLevelsUnavailable condition based on TAS config and ClusterTopology
	if err = r.mutateTopologyLevelUnavailableConditions(ctx, logger, pcs); err != nil {
		return ctrlcommon.ReconcileWithErrors("failed to mutate TopologyLevelsUnavailable condition", err)
	}

	// Mirror UpdateProgress to the deprecated RollingUpdateProgress field for backward compatibility.
	// The previous slice-shaped UpdatedPodCliques/UpdatedPodCliqueScalingGroups fields on the
	// deprecated mirror are gone; only bounded counts and timestamps are mirrored now.
	mirrorUpdateProgressToRollingUpdateProgress(pcs)

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
func (r *Reconciler) mutateReplicas(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	// Set basic replica count
	pcs.Status.Replicas = pcs.Spec.Replicas
	stats, err := r.computeAvailableAndUpdatedReplicas(ctx, logger, pcs)
	if err != nil {
		return fmt.Errorf("could not compute available replicas: %w", err)
	}
	pcs.Status.AvailableReplicas = stats.availableReplicas
	pcs.Status.UpdatedReplicas = stats.updatedReplicas
	if pcs.Status.UpdateProgress != nil {
		pcs.Status.UpdateProgress.UpdatedPodCliquesCount = stats.updatedPCLQs
		pcs.Status.UpdateProgress.TotalPodCliquesCount = stats.totalPCLQs
		pcs.Status.UpdateProgress.UpdatedPodCliqueScalingGroupsCount = stats.updatedPCSGs
		pcs.Status.UpdateProgress.TotalPodCliqueScalingGroupsCount = stats.totalPCSGs
	}
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

// computeAvailableAndUpdatedReplicas walks the PCS's standalone PCLQs and PCSGs once and
// returns aggregate availability and update counts. Replaces the prior O(N²) accumulator that
// stored fully-qualified child names in status.
func (r *Reconciler) computeAvailableAndUpdatedReplicas(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) (pcsReplicaStats, error) {
	var (
		stats        pcsReplicaStats
		pcsObjectKey = client.ObjectKeyFromObject(pcs)
	)

	expectedPCSGFQNsPerPCSReplica := componentutils.GetExpectedPCSGFQNsPerPCSReplica(pcs)
	expectedStandAlonePCLQFQNsPerPCSReplica := componentutils.GetExpectedStandAlonePCLQFQNsPerPCSReplica(pcs)

	// Hoist the expected-name lookups out of the filter callbacks. Built once each (O(E) work and
	// space); each filter pass is then O(M) with a single map lookup per element, where M is the
	// number of live children fetched and E is the number of expected names. Replaces an earlier
	// O(M*E) pattern that re-flattened the per-replica name map on every element.
	expectedPCSGNameSet := flattenNamesToSet(expectedPCSGFQNsPerPCSReplica)
	expectedStandalonePCLQNameSet := flattenNamesToSet(expectedStandAlonePCLQFQNsPerPCSReplica)

	// Fetch all PCSGs for this PCS, then drop any stray PCSGs (not part of the spec) using O(1)
	// set lookup. slices.DeleteFunc compacts in-place; safe here because the slice came from a
	// fresh fetch and isn't aliased.
	pcsgs, err := componentutils.GetPCSGsForPCS(ctx, r.client, pcsObjectKey)
	if err != nil {
		return stats, err
	}
	pcsgs = slices.DeleteFunc(pcsgs, func(pcsg grovecorev1alpha1.PodCliqueScalingGroup) bool {
		_, expected := expectedPCSGNameSet[pcsg.Name]
		return !expected
	})

	// Fetch all standalone PodCliques for this PCS and drop strays the same way.
	standalonePCLQs, err := componentutils.GetPodCliquesWithParentPCS(ctx, r.client, pcsObjectKey)
	if err != nil {
		return stats, err
	}
	standalonePCLQs = slices.DeleteFunc(standalonePCLQs, func(pclq grovecorev1alpha1.PodClique) bool {
		_, expected := expectedStandalonePCLQNameSet[pclq.Name]
		return !expected
	})

	// Group both resources by PCS replica index
	standalonePCLQsByReplica := componentutils.GroupPCLQsByPCSReplicaIndex(standalonePCLQs)
	pcsgsByReplica := componentutils.GroupPCSGsByPCSReplicaIndex(pcsgs)

	for replicaIndex := 0; replicaIndex < int(pcs.Spec.Replicas); replicaIndex++ {
		replicaIndexStr := strconv.Itoa(replicaIndex)
		replicaStandalonePCLQs := standalonePCLQsByReplica[replicaIndexStr]
		replicaPCSGs := pcsgsByReplica[replicaIndexStr]
		expectedPCSGCount := len(expectedPCSGFQNsPerPCSReplica[replicaIndex])
		expectedPCLQCount := len(expectedStandAlonePCLQFQNsPerPCSReplica[replicaIndex])

		stats.totalPCLQs += int32(expectedPCLQCount)
		stats.totalPCSGs += int32(expectedPCSGCount)
		stats.updatedPCLQs += countUpdatedPCLQs(pcs.Status.CurrentGenerationHash, replicaStandalonePCLQs)
		stats.updatedPCSGs += countUpdatedPCSGs(pcs.Status.CurrentGenerationHash, replicaPCSGs)

		isReplicaAvailable, isReplicaUpdated := r.computeReplicaStatus(pcs.Status.CurrentGenerationHash, replicaPCSGs,
			replicaStandalonePCLQs, expectedPCSGCount, expectedPCLQCount)
		if isReplicaAvailable {
			stats.availableReplicas++
		}
		if isReplicaUpdated {
			stats.updatedReplicas++
		}
	}

	logger.Info("Calculated PCS replica and update progress stats",
		"pcs", pcsObjectKey,
		"availableReplicas", stats.availableReplicas,
		"updatedReplicas", stats.updatedReplicas,
		"updatedPCLQs", stats.updatedPCLQs, "totalPCLQs", stats.totalPCLQs,
		"updatedPCSGs", stats.updatedPCSGs, "totalPCSGs", stats.totalPCSGs)
	return stats, nil
}

// countUpdatedPCLQs counts non-terminating PCLQs whose generation hash matches the PCS hash.
func countUpdatedPCLQs(pcsGenerationHash *string, pclqs []grovecorev1alpha1.PodClique) int32 {
	if pcsGenerationHash == nil {
		return 0
	}
	var n int32
	for i := range pclqs {
		pclq := &pclqs[i]
		if k8sutils.IsResourceTerminating(pclq.ObjectMeta) {
			continue
		}
		if pclq.Status.CurrentPodCliqueSetGenerationHash != nil &&
			*pclq.Status.CurrentPodCliqueSetGenerationHash == *pcsGenerationHash {
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
func (r *Reconciler) computeReplicaStatus(pcsGenerationHash *string, replicaPCSGs []grovecorev1alpha1.PodCliqueScalingGroup, standalonePCLQs []grovecorev1alpha1.PodClique, expectedPCSGs int, expectedStandalonePCLQs int) (bool, bool) {
	pclqsAvailable, pclqsUpdated := r.computePCLQsStatus(expectedStandalonePCLQs, standalonePCLQs)
	pcsgsAvailable, pcsgsUpdated := r.computePCSGsStatus(pcsGenerationHash, expectedPCSGs, replicaPCSGs)
	return pclqsAvailable && pcsgsAvailable, pclqsUpdated && pcsgsUpdated
}

// computePCLQsStatus checks if standalone PodCliques are available and updated.
func (r *Reconciler) computePCLQsStatus(expectedStandalonePCLQs int, existingPCLQs []grovecorev1alpha1.PodClique) (isAvailable, isUpdated bool) {
	nonTerminatedPCLQs := lo.Filter(existingPCLQs, func(pclq grovecorev1alpha1.PodClique, _ int) bool {
		return !k8sutils.IsResourceTerminating(pclq.ObjectMeta)
	})

	isAvailable = len(nonTerminatedPCLQs) == expectedStandalonePCLQs &&
		lo.EveryBy(nonTerminatedPCLQs, func(pclq grovecorev1alpha1.PodClique) bool {
			return pclq.Status.ReadyReplicas >= *pclq.Spec.MinAvailable
		})

	isUpdated = isAvailable && lo.EveryBy(nonTerminatedPCLQs, func(pclq grovecorev1alpha1.PodClique) bool {
		return pclq.Status.UpdatedReplicas >= *pclq.Spec.MinAvailable
	})

	return
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
	// compute the new TopologyLevelsUnavailable condition based on ClusterTopology and PodCliqueSet TopologyConstraints.
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
// ClusterTopology referenced by the explicit topology constraints in the PodCliqueSet.
// If any topology domains used by the PodCliqueSet are not available in that ClusterTopology, it sets the condition to True.
// If all referenced topology domains are available, it sets the condition to False.
// If the ClusterTopology resource is not found, or an explicit topology constraint is incomplete, it sets the condition to Unknown.
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

	topologyName, err := componentutils.ResolveTopologyNameForPodCliqueSet(pcs)
	if err != nil {
		switch err {
		case componentutils.ErrTopologyNameMissing:
			return metav1.Condition{
				Type:               apicommonconstants.ConditionTopologyLevelsUnavailable,
				Status:             metav1.ConditionUnknown,
				Reason:             apicommonconstants.ConditionReasonTopologyNameMissing,
				Message:            "topology constraints must specify both topologyName and packDomain",
				ObservedGeneration: pcs.Generation,
				LastTransitionTime: metav1.Now(),
			}, nil
		case componentutils.ErrMultipleTopologyNamesUnsupported:
			return metav1.Condition{
				Type:               apicommonconstants.ConditionTopologyLevelsUnavailable,
				Status:             metav1.ConditionUnknown,
				Reason:             apicommonconstants.ConditionReasonTopologyNameMissing,
				Message:            "all topologyConstraint.topologyName values within a PodCliqueSet must match in the current implementation",
				ObservedGeneration: pcs.Generation,
				LastTransitionTime: metav1.Now(),
			}, nil
		default:
			return metav1.Condition{}, fmt.Errorf("failed to resolve topologyName: %w", err)
		}
	}

	topologyLevels, err := clustertopology.GetClusterTopologyLevels(ctx, r.client, topologyName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return metav1.Condition{
				Type:               apicommonconstants.ConditionTopologyLevelsUnavailable,
				Status:             metav1.ConditionUnknown,
				Reason:             apicommonconstants.ConditionReasonClusterTopologyNotFound,
				Message:            "ClusterTopology resource not found",
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

// mirrorUpdateProgressToRollingUpdateProgress mirrors the canonical UpdateProgress to the deprecated
// RollingUpdateProgress field so consumers still reading the deprecated path keep working. Only
// bounded count fields (and timestamps + currently-updating index) are mirrored — the previous
// unbounded slice fields have been removed (see issue #567).
func mirrorUpdateProgressToRollingUpdateProgress(pcs *grovecorev1alpha1.PodCliqueSet) {
	if pcs.Status.UpdateProgress == nil {
		pcs.Status.RollingUpdateProgress = nil
		return
	}
	up := pcs.Status.UpdateProgress
	pcs.Status.RollingUpdateProgress = &grovecorev1alpha1.PodCliqueSetRollingUpdateProgress{
		UpdateStartedAt:                    up.UpdateStartedAt,
		UpdateEndedAt:                      up.UpdateEndedAt,
		UpdatedPodCliquesCount:             up.UpdatedPodCliquesCount,
		TotalPodCliquesCount:               up.TotalPodCliquesCount,
		UpdatedPodCliqueScalingGroupsCount: up.UpdatedPodCliqueScalingGroupsCount,
		TotalPodCliqueScalingGroupsCount:   up.TotalPodCliqueScalingGroupsCount,
	}
	if len(up.CurrentlyUpdating) > 0 {
		pcs.Status.RollingUpdateProgress.CurrentlyUpdating = &grovecorev1alpha1.PodCliqueSetReplicaRollingUpdateProgress{
			ReplicaIndex:    up.CurrentlyUpdating[0].ReplicaIndex,
			UpdateStartedAt: up.CurrentlyUpdating[0].UpdateStartedAt,
		}
	}
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

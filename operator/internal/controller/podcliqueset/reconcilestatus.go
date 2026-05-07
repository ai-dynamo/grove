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

	// mirror UpdateProgress to the deprecated RollingUpdateProgress field for backward compatibility.
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

// mutateReplicas updates the PodCliqueSet status replica counts.
func (r *Reconciler) mutateReplicas(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	// Set basic replica count
	pcs.Status.Replicas = pcs.Spec.Replicas
	availableReplicas, updatedReplicas, err := r.computeAvailableAndUpdatedReplicas(ctx, logger, pcs)
	if err != nil {
		return fmt.Errorf("could not compute available replicas: %w", err)
	}
	pcs.Status.AvailableReplicas = availableReplicas
	pcs.Status.UpdatedReplicas = updatedReplicas
	return nil
}

// computeAvailableAndUpdatedReplicas calculates the number of available replicas for a PodCliqueSet.
// It checks both standalone PodCliques and PodCliqueScalingGroups to determine availability.
// A replica is considered available if it has all its required components (PCSGs and standalone PCLQs) available.
func (r *Reconciler) computeAvailableAndUpdatedReplicas(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) (int32, int32, error) {
	var (
		availableReplicas int32
		updatedReplicas   int32
		pcsObjectKey      = client.ObjectKeyFromObject(pcs)
	)

	expectedPCSGFQNsPerPCSReplica := componentutils.GetExpectedPCSGFQNsPerPCSReplica(pcs)
	expectedStandAlonePCLQFQNsPerPCSReplica := componentutils.GetExpectedStandAlonePCLQFQNsPerPCSReplica(pcs)

	// Fetch all PCSGs for this PCS
	pcsgs, err := componentutils.GetPCSGsForPCS(ctx, r.client, pcsObjectKey)
	if err != nil {
		return availableReplicas, updatedReplicas, err
	}
	// Filter the PCSGs that belong to the expected set of PCSGs for PCS, this ensures that we do not
	// consider any stray PCSGs that might have been created externally.
	pcsgs = lo.Filter(pcsgs, func(pcsg grovecorev1alpha1.PodCliqueScalingGroup, _ int) bool {
		return lo.Contains(lo.Flatten(lo.Values(expectedPCSGFQNsPerPCSReplica)), pcsg.Name)
	})

	// Fetch all standalone PodCliques for this PCS
	standalonePCLQs, err := componentutils.GetPodCliquesWithParentPCS(ctx, r.client, pcsObjectKey)
	if err != nil {
		return availableReplicas, updatedReplicas, err
	}
	// Filter the PCLQs that belong to the expected set of standalone PCLQs for PCS, this ensures that we do not
	// consider any stray PCLQs that might have been created externally.
	standalonePCLQs = lo.Filter(standalonePCLQs, func(pclq grovecorev1alpha1.PodClique, _ int) bool {
		return lo.Contains(lo.Flatten(lo.Values(expectedStandAlonePCLQFQNsPerPCSReplica)), pclq.Name)
	})

	// Group both resources by PCS replica index
	standalonePCLQsByReplica := componentutils.GroupPCLQsByPCSReplicaIndex(standalonePCLQs)
	pcsgsByReplica := componentutils.GroupPCSGsByPCSReplicaIndex(pcsgs)

	for replicaIndex := 0; replicaIndex < int(pcs.Spec.Replicas); replicaIndex++ {
		replicaIndexStr := strconv.Itoa(replicaIndex)
		replicaStandalonePCLQs := standalonePCLQsByReplica[replicaIndexStr]
		replicaPCSGs := pcsgsByReplica[replicaIndexStr]
		// Check if this PCS replica is available based on all its components
		isReplicaAvailable, isReplicaUpdated := r.computeReplicaStatus(pcs.Status.CurrentGenerationHash, replicaPCSGs,
			replicaStandalonePCLQs, len(expectedPCSGFQNsPerPCSReplica[replicaIndex]), len(expectedStandAlonePCLQFQNsPerPCSReplica[replicaIndex]))
		if isReplicaAvailable {
			availableReplicas++
		}
		if isReplicaUpdated {
			updatedReplicas++
		}
	}

	logger.Info("Calculated available and updated replicas for PCS", "pcs", pcsObjectKey, "availableReplicas", availableReplicas, "updatedReplicas", updatedReplicas, "totalReplicas", pcs.Spec.Replicas)
	return availableReplicas, updatedReplicas, nil
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

// mirrorUpdateProgressToRollingUpdateProgress mirrors the UpdateProgress field to the deprecated RollingUpdateProgress field
// for backward compatibility with consumers that still use the old field name.
func mirrorUpdateProgressToRollingUpdateProgress(pcs *grovecorev1alpha1.PodCliqueSet) {
	if pcs.Status.UpdateProgress == nil {
		pcs.Status.RollingUpdateProgress = nil
		return
	}

	pcs.Status.RollingUpdateProgress = &grovecorev1alpha1.PodCliqueSetRollingUpdateProgress{
		UpdateStartedAt:               pcs.Status.UpdateProgress.UpdateStartedAt,
		UpdateEndedAt:                 pcs.Status.UpdateProgress.UpdateEndedAt,
		UpdatedPodCliqueScalingGroups: pcs.Status.UpdateProgress.UpdatedPodCliqueScalingGroups,
		UpdatedPodCliques:             pcs.Status.UpdateProgress.UpdatedPodCliques,
	}

	if len(pcs.Status.UpdateProgress.CurrentlyUpdating) > 0 {
		pcs.Status.RollingUpdateProgress.CurrentlyUpdating = &grovecorev1alpha1.PodCliqueSetReplicaRollingUpdateProgress{
			ReplicaIndex:    pcs.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex,
			UpdateStartedAt: pcs.Status.UpdateProgress.CurrentlyUpdating[0].UpdateStartedAt,
		}
	}
}

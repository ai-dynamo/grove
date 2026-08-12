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

package podgang

import (
	"context"
	"errors"
	"fmt"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/clustertopology"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// prepareSyncFlow computes the required state for synchronizing PodGang resources.
func (r _resource) prepareSyncFlow(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) (ss *syncState, err error) {
	pcsObjectKey := client.ObjectKeyFromObject(pcs)
	ss = &syncState{
		pcs:                  pcs,
		logger:               logger,
		existingPCLQPods:     make(map[string][]corev1.Pod),
		unassignedPodsByPCLQ: make(map[string][]corev1.Pod),
	}

	ss.existingPCLQs, err = r.getExistingPCLQsForPCS(ctx, pcs)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPodCliques,
			component.OperationSync,
			fmt.Sprintf("failed to list PodCliques for PodCliqueSet %v", pcsObjectKey),
		)
	}
	ss.existingPCLQByName = componentutils.PodCliqueByName(ss.existingPCLQs)

	ss.tasEnabled = r.tasConfig.Enabled
	if r.tasConfig.Enabled {
		ss.topologyLevels, err = r.resolveTopologyLevels(ctx, logger, pcs)
		if err != nil {
			return nil, err
		}
	}

	ss.expectedPodGangs, err = r.computeExpectedPodGangs(ctx, ss)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeComputeExistingPodGangs,
			component.OperationSync,
			fmt.Sprintf("failed to compute existing PodGangs for PodCliqueSet %v", pcsObjectKey),
		)
	}
	ss.expectedPodGangByName = podGangInfoByName(ss.expectedPodGangs)
	ss.expectedPodGangNameSet = podGangInfoNameSet(ss.expectedPodGangs)

	ss.existingPodGangs, err = componentutils.GetExistingPodGangs(ctx, r.client, pcs.ObjectMeta, pcs.Namespace)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPodGangs,
			component.OperationSync,
			fmt.Sprintf("Failed to get existing PodGangs for PodCliqueSet: %v", client.ObjectKeyFromObject(ss.pcs)),
		)
	}
	ss.existingPodGangByName = componentutils.PodGangByName(ss.existingPodGangs)

	ss.existingPCLQPods, err = r.getExistingPodsByPCLQForPCS(ctx, pcsObjectKey)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPods,
			component.OperationSync,
			fmt.Sprintf("failed to list Pods for PodCliqueSet %v", pcsObjectKey),
		)
	}
	ss.initializeAssignedAndUnassignedPodsForPCS()

	return ss, nil
}

// getExistingPCLQsForPCS fetches all existing PodCliques managed by the PodCliqueSet.
func (r _resource) getExistingPCLQsForPCS(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) ([]grovecorev1alpha1.PodClique, error) {
	pclqList := &grovecorev1alpha1.PodCliqueList{}
	if err := r.client.List(ctx, pclqList,
		client.InNamespace(pcs.Namespace),
		client.MatchingLabels(apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name))); err != nil {
		return nil, err
	}

	// Return all PodCliques with matching labels. PodCliques can be owned either:
	// 1. Directly by PCS (standalone pclqs)
	// 2. By PCSG (scaling group member pclqs) - PCSG itself is owned by PCS
	// Label matching ensures they belong to this PCS, no ownership filter needed.
	return pclqList.Items, nil
}

// resolveTopologyLevels returns the cluster topology levels used to translate topology constraints.
// It returns nil (no error) when the PodCliqueSet declares no topology constraint, when no explicit
// topologyName resolves, or when the referenced ClusterTopologyBinding does not exist. In the last
// case the PCS reconciler surfaces the missing binding via the TopologyNameMissing condition. The
// caller is responsible for gating this on topology-aware scheduling being enabled.
func (r _resource) resolveTopologyLevels(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ([]grovecorev1alpha1.TopologyLevel, error) {
	if !componentutils.HasAnyTopologyConstraint(pcs) {
		return nil, nil
	}
	topologyName, err := componentutils.FindExplicitTopologyNameForPodCliqueSet(pcs)
	if err != nil || topologyName == "" {
		return nil, nil
	}
	levels, err := clustertopology.GetClusterTopologyLevels(ctx, r.client, topologyName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info(
				"ClusterTopologyBinding not found while preparing PodGang sync; continuing without translated topology constraints",
				"pcs", client.ObjectKeyFromObject(pcs),
				"topologyName", topologyName,
			)
			return nil, nil
		}
		return nil, groveerr.WrapError(err,
			errCodeGetClusterTopologyLevels,
			component.OperationSync,
			fmt.Sprintf("failed to get cluster topology levels for %q", topologyName))
	}
	return levels, nil
}

// computeExpectedPodGangs computes the expected PodGangs by reading the PodGangMap for each PCS
// replica. The PodGangMap is the single source of truth for PodGang composition.
func (r _resource) computeExpectedPodGangs(ctx context.Context, ss *syncState) ([]*podGangInfo, error) {
	var expectedPodGangs []*podGangInfo
	for pcsReplicaIndex := range int(ss.pcs.Spec.Replicas) {
		pgm, err := componentutils.GetPodGangMap(ctx, r.client, client.ObjectKeyFromObject(ss.pcs), pcsReplicaIndex)
		if err != nil {
			return nil, err
		}
		for _, entry := range pgm.Spec.Entries {
			pgInfos, err := r.buildPodGangInfosFromEntry(ss, pcsReplicaIndex, entry)
			if err != nil {
				return nil, fmt.Errorf("failed to build PodGang info from entry with epoch %q in PodGangMap %s: %w", entry.Epoch, pgm.Name, err)
			}
			expectedPodGangs = append(expectedPodGangs, pgInfos...)
		}
	}
	return expectedPodGangs, nil
}

// buildPodGangInfosFromEntry translates a PodGangMap entry into the PodGangs it materializes into.
// An Anchor entry yields a single PodGang carrying the standalone PodCliques and the PodCliqueScalingGroup
// replica indices the entry holds. A non-anchor entry (Tail or ScaleOut) yields one PodGang per
// (PodCliqueScalingGroup, replica index) it carries.
func (r _resource) buildPodGangInfosFromEntry(ss *syncState, pcsReplicaIndex int, pgEntry grovecorev1alpha1.PodGangEntry) ([]*podGangInfo, error) {
	if pgEntry.Role == grovecorev1alpha1.PodGangEntryRoleAnchor {
		rnr := apicommon.ResourceNameReplica{Name: ss.pcs.Name, Replica: pcsReplicaIndex}
		pg := &podGangInfo{
			fqn:                apicommon.GenerateAnchorPodGangName(rnr, pgEntry.Epoch),
			pcsReplicaIndex:    pcsReplicaIndex,
			extraLabels:        buildAdditionalLabelsFromPodGangEntry(pgEntry),
			topologyConstraint: createTopologyPackConstraint(ss, client.ObjectKeyFromObject(ss.pcs), ss.pcs.Spec.Template.TopologyConstraint),
		}
		pg.pclqs = buildStandalonePCLQInfosForAnchorEntry(ss, pcsReplicaIndex, pgEntry)
		pcsgPCLQInfos, pcsgTopoConstraints, err := buildPCSGPCLQInfosAndTopoConstraintsFromAnchorEntry(ss, pcsReplicaIndex, pgEntry)
		if err != nil {
			return nil, err
		}
		pg.pclqs = append(pg.pclqs, pcsgPCLQInfos...)
		pg.pcsgTopologyConstraints = pcsgTopoConstraints
		return []*podGangInfo{pg}, nil
	}
	return r.buildNonAnchorPodGangInfos(ss, pcsReplicaIndex, pgEntry)
}

// buildAdditionalLabelsFromPodGangEntry returns the epoch and role labels stamped on a PodGang
// materialized from the given entry.
func buildAdditionalLabelsFromPodGangEntry(pgEntry grovecorev1alpha1.PodGangEntry) map[string]string {
	return map[string]string{
		apicommon.LabelEpoch:       pgEntry.Epoch,
		apicommon.LabelPodGangRole: string(pgEntry.Role),
	}
}

// buildStandalonePCLQInfosForAnchorEntry builds pclqInfo entries for the standalone PodCliques the
// anchor entry carries. The pod count comes from the entry, since the PodGangMap is the source of
// truth. Iterates template cliques in order for deterministic output.
func buildStandalonePCLQInfosForAnchorEntry(ss *syncState, pcsReplicaIndex int, pgEntry grovecorev1alpha1.PodGangEntry) []pclqInfo {
	pclqInfos := make([]pclqInfo, 0, len(ss.pcs.Spec.Template.Cliques))
	for _, cliqueTemplate := range ss.pcs.Spec.Template.Cliques {
		desiredPCLQReplicas, ok := pgEntry.PodCliques[cliqueTemplate.Name]
		if !ok {
			continue
		}
		pclqFQN := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: ss.pcs.Name, Replica: pcsReplicaIndex}, cliqueTemplate.Name)
		pi := pclqInfo{
			fqn:          pclqFQN,
			replicas:     desiredPCLQReplicas,
			minAvailable: *cliqueTemplate.Spec.MinAvailable,
		}
		pi.topologyConstraint = createTopologyPackConstraint(ss, types.NamespacedName{Namespace: ss.pcs.Namespace, Name: pclqFQN}, cliqueTemplate.TopologyConstraint)
		pclqInfos = append(pclqInfos, pi)
	}
	return pclqInfos
}

// buildPCSGPCLQInfosAndTopoConstraintsFromAnchorEntry builds the pclqInfo entries and group-level
// topology constraints for all PodCliqueScalingGroup replica indices the anchor entry carries. The
// anchor is a single PodGang, so results are flattened. Iterates template PCSG configs in order for
// deterministic output.
func buildPCSGPCLQInfosAndTopoConstraintsFromAnchorEntry(ss *syncState, pcsReplicaIndex int, pgEntry grovecorev1alpha1.PodGangEntry) ([]pclqInfo, []groveschedulerv1alpha1.TopologyConstraintGroupConfig, error) {
	var (
		pclqInfos       []pclqInfo
		topoConstraints []groveschedulerv1alpha1.TopologyConstraintGroupConfig
	)
	for _, pcsgConfig := range ss.pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		pcsgReplicaIndices, ok := pgEntry.PCSGReplicaIndices[pcsgConfig.Name]
		if !ok || len(pcsgReplicaIndices) == 0 {
			continue
		}
		for _, pcsgReplicaIndex := range pcsgReplicaIndices {
			replicaPCLQs, topoConstraint, err := buildPCLQInfosAndTopoConstraintsForPCSGReplica(ss, pcsReplicaIndex, pcsgConfig, pcsgReplicaIndex)
			if err != nil {
				return nil, nil, err
			}
			pclqInfos = append(pclqInfos, replicaPCLQs...)
			if topoConstraint != nil {
				topoConstraints = append(topoConstraints, *topoConstraint)
			}
		}
	}
	return pclqInfos, topoConstraints, nil
}

// buildNonAnchorPodGangInfos expands a non-anchor entry (Tail or ScaleOut) into one podGangInfo per
// (PodCliqueScalingGroup, replica index). Iterates template PCSG configs in order for deterministic output.
func (r _resource) buildNonAnchorPodGangInfos(ss *syncState, pcsReplicaIndex int, pgEntry grovecorev1alpha1.PodGangEntry) ([]*podGangInfo, error) {
	var pgInfos []*podGangInfo
	rnr := apicommon.ResourceNameReplica{Name: ss.pcs.Name, Replica: pcsReplicaIndex}
	for _, pcsgConfig := range ss.pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		pcsgReplicaIndices, ok := pgEntry.PCSGReplicaIndices[pcsgConfig.Name]
		if !ok || len(pcsgReplicaIndices) == 0 {
			continue
		}
		for _, pcsgReplicaIndex := range pcsgReplicaIndices {
			pclqs, constraint, err := buildPCLQInfosAndTopoConstraintsForPCSGReplica(ss, pcsReplicaIndex, pcsgConfig, pcsgReplicaIndex)
			if err != nil {
				return nil, err
			}
			pg := &podGangInfo{
				fqn:                apicommon.GenerateNonAnchorPodGangName(rnr, pgEntry.Epoch, pcsgConfig.Name, pcsgReplicaIndex),
				pcsReplicaIndex:    pcsReplicaIndex,
				extraLabels:        buildAdditionalLabelsFromPodGangEntry(pgEntry),
				pclqs:              pclqs,
				topologyConstraint: createTopologyPackConstraint(ss, client.ObjectKeyFromObject(ss.pcs), ss.pcs.Spec.Template.TopologyConstraint),
			}
			if constraint != nil {
				pg.pcsgTopologyConstraints = []groveschedulerv1alpha1.TopologyConstraintGroupConfig{*constraint}
			}
			pgInfos = append(pgInfos, pg)
		}
	}
	return pgInfos, nil
}

// buildPCLQInfosAndTopoConstraintsForPCSGReplica builds the pclqInfo entries and optional group-level
// topology constraint for a single PodCliqueScalingGroup replica index. It returns an error when the
// PodCliqueScalingGroup references a clique that does not exist in the PodCliqueSet.
func buildPCLQInfosAndTopoConstraintsForPCSGReplica(ss *syncState, pcsReplicaIndex int, pcsgConfig grovecorev1alpha1.PodCliqueScalingGroupConfig, pcsgReplicaIndex int32) ([]pclqInfo, *groveschedulerv1alpha1.TopologyConstraintGroupConfig, error) {
	pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: ss.pcs.Name, Replica: pcsReplicaIndex}, pcsgConfig.Name)
	pclqFQNs := make([]string, 0, len(pcsgConfig.CliqueNames))
	pclqs := make([]pclqInfo, 0, len(pcsgConfig.CliqueNames))
	for _, cliqueName := range pcsgConfig.CliqueNames {
		pclqTemplateSpec := componentutils.FindPodCliqueTemplateSpecByName(ss.pcs, cliqueName)
		if pclqTemplateSpec == nil {
			return nil, nil, fmt.Errorf("PodCliqueScalingGroup %q references a PodClique %q that does not exist in the PodCliqueSet: %v", pcsgFQN, cliqueName, client.ObjectKeyFromObject(ss.pcs))
		}
		pclqFQN := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsgFQN, Replica: int(pcsgReplicaIndex)}, cliqueName)
		pclqs = append(pclqs, buildPodCliqueInfo(ss, pclqTemplateSpec, pclqFQN, true))
		pclqFQNs = append(pclqFQNs, pclqFQN)
	}
	var topoConstraintGroupConfig *groveschedulerv1alpha1.TopologyConstraintGroupConfig
	pcsgTopologyConstraint := createTopologyPackConstraint(ss, types.NamespacedName{Namespace: ss.pcs.Namespace, Name: pcsgFQN}, pcsgConfig.TopologyConstraint)
	if pcsgTopologyConstraint != nil {
		topoConstraintGroupConfig = &groveschedulerv1alpha1.TopologyConstraintGroupConfig{
			Name:               fmt.Sprintf("%s-%d", pcsgFQN, pcsgReplicaIndex),
			PodGroupNames:      pclqFQNs,
			TopologyConstraint: pcsgTopologyConstraint,
		}
	}
	return pclqs, topoConstraintGroupConfig, nil
}

// buildPodCliqueInfo creates pclqInfo with appropriate replica counts.
func buildPodCliqueInfo(ss *syncState, pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, pclqFQN string, belongsToPCSG bool) pclqInfo {
	replicas := determinePodCliqueReplicas(ss, pclqTemplateSpec, pclqFQN, belongsToPCSG)
	expectedPCLQ := pclqInfo{
		fqn:          pclqFQN,
		replicas:     replicas,
		minAvailable: *pclqTemplateSpec.Spec.MinAvailable,
	}
	expectedPCLQ.topologyConstraint = createTopologyPackConstraint(ss, types.NamespacedName{Namespace: ss.pcs.Namespace, Name: pclqFQN}, pclqTemplateSpec.TopologyConstraint)
	return expectedPCLQ
}

// createTopologyPackConstraint creates a TopologyPackConstraint based on the sync context and provided parameters for a resource.
// PackConstraints are defined at multiple levels (PodCliqueSet, PodCliqueScalingGroup, PodClique). This function helps create a TopologyPackConstraint for any of these levels.
func createTopologyPackConstraint(ss *syncState, nsName types.NamespacedName, topologyConstraint *grovecorev1alpha1.TopologyConstraint) *groveschedulerv1alpha1.TopologyConstraint {
	// If Topology aware scheduling is disabled, return nil even if TopologyConstraint is specified.
	if !ss.tasEnabled || topologyConstraint == nil {
		return nil
	}

	pgPackConstraint := &groveschedulerv1alpha1.TopologyPackConstraint{}
	pgPackConstraint.Required = topologyLevelKeyForPackDomain(ss, nsName, topologyConstraint, topologyConstraint.RequiredDomain(), "required")
	pgPackConstraint.Preferred = topologyLevelKeyForPackDomain(ss, nsName, topologyConstraint, topologyConstraint.PreferredDomain(), "preferred")

	if pgPackConstraint.Required == nil && pgPackConstraint.Preferred == nil {
		return nil
	}
	return &groveschedulerv1alpha1.TopologyConstraint{PackConstraint: pgPackConstraint}
}

func topologyLevelKeyForPackDomain(ss *syncState, nsName types.NamespacedName, topologyConstraint *grovecorev1alpha1.TopologyConstraint, topologyDomain grovecorev1alpha1.TopologyDomain, packConstraintType string) *string {
	if topologyDomain == "" {
		return nil
	}
	topologyLevel, found := lo.Find(ss.topologyLevels, func(topologyLevel grovecorev1alpha1.TopologyLevel) bool {
		return topologyLevel.Domain == topologyDomain
	})
	if !found {
		// This can happen if the ClusterTopologyBinding CR has changed after the resource was admitted.
		ss.logger.Info(packConstraintType+" topology domain not found in cluster topology levels, skipping setting "+packConstraintType+" pack constraint", "namespacedName", nsName, "topologyDomain", topologyDomain, "topologyConstraint", *topologyConstraint)
		return nil
	}
	return ptr.To(topologyLevel.Key)
}

// determinePodCliqueReplicas determines replica count considering HPA mutations.
func determinePodCliqueReplicas(ss *syncState, pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, pclqFQN string, belongsToPCSG bool) int32 {
	if belongsToPCSG || pclqTemplateSpec.Spec.ScaleConfig == nil {
		return pclqTemplateSpec.Spec.Replicas
	}
	matchingPCLQ, found := ss.existingPCLQByName[pclqFQN]
	if !found {
		// PodClique resource not found - might be during initial creation
		// Fall back to template replicas but log warning for visibility
		ss.logger.Info("[WARN]: PodClique resource not found, using template replicas",
			"podCliqueFQN", pclqFQN,
			"templateReplicas", pclqTemplateSpec.Spec.Replicas)
		return pclqTemplateSpec.Spec.Replicas
	}
	return matchingPCLQ.Spec.Replicas
}

// getExistingPodsByPCLQForPCS fetches all non-terminating pods grouped by PodClique.
// It returns a map where the key is the PodClique FQN and the value is a slice of Pods belonging to that PodClique.
func (r _resource) getExistingPodsByPCLQForPCS(ctx context.Context, pcsObjectKey client.ObjectKey) (map[string][]corev1.Pod, error) {
	podList := &corev1.PodList{}
	if err := r.client.List(ctx,
		podList,
		client.InNamespace(pcsObjectKey.Namespace),
		client.MatchingLabels(apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsObjectKey.Name)),
	); err != nil {
		return nil, err
	}

	podsByPCLQ := make(map[string][]corev1.Pod)
	for _, pod := range podList.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		pclqFQN := k8sutils.GetFirstOwnerName(pod.ObjectMeta)
		podsByPCLQ[pclqFQN] = append(podsByPCLQ[pclqFQN], pod)
	}

	return podsByPCLQ, nil
}

// runSyncFlow executes the PodGang synchronization workflow.
func (r _resource) runSyncFlow(ctx context.Context, ss *syncState) syncFlowResult {
	result := syncFlowResult{}
	if err := r.deleteExcessPodGangs(ctx, ss); err != nil {
		result.errs = append(result.errs, err)
		return result
	}
	return r.createOrUpdatePodGangs(ctx, ss)
}

// deleteExcessPodGangs removes PodGangs that are no longer needed.
func (r _resource) deleteExcessPodGangs(ctx context.Context, ss *syncState) error {
	excessPodGangs := ss.getExcessPodGangNames()
	namespace := ss.pcs.Namespace
	for _, podGangToDelete := range excessPodGangs {
		pgObjectKey := client.ObjectKey{Namespace: namespace, Name: podGangToDelete}
		pg := emptyPodGang(pgObjectKey)
		ss.logger.Info("Delete excess PodGang", "objectKey", client.ObjectKeyFromObject(pg))
		if err := client.IgnoreNotFound(r.client.Delete(ctx, pg)); err != nil {
			r.eventRecorder.Eventf(ss.pcs, corev1.EventTypeWarning, constants.ReasonPodGangDeleteFailed, "Error deleting PodGang %v: %v", pgObjectKey, err)
			return groveerr.WrapError(err,
				errCodeDeleteExcessPodGang,
				component.OperationSync,
				fmt.Sprintf("failed to delete PodGang %v", pgObjectKey),
			)
		}
		r.eventRecorder.Eventf(ss.pcs, corev1.EventTypeNormal, constants.ReasonPodGangDeleteSuccessful, "Deleted PodGang %v", pgObjectKey)
		ss.deletedPodGangNames = append(ss.deletedPodGangNames, podGangToDelete)
		ss.logger.Info("Triggered delete of excess PodGang", "objectKey", client.ObjectKeyFromObject(pg))
	}
	return nil
}

// createOrUpdatePodGangs creates or updates all expected PodGangs.
// PodGangs are created with empty podReferences, Initialized=False.
// Once all pods are created, PodReferences are populated and the PodGang is marked as Initialized=True.
func (r _resource) createOrUpdatePodGangs(ctx context.Context, ss *syncState) syncFlowResult {
	result := syncFlowResult{}
	for _, expectedPG := range ss.expectedPodGangs {
		// create or update all expected PodGang.
		if err := r.createOrUpdatePodGang(ctx, ss, expectedPG); err != nil {
			ss.logger.Error(err, "failed to create PodGang", "PodGangName", expectedPG.fqn)
			result.recordError(err)
			return result
		}

		// If the PodGang does not exist and the creation succeeded then record the PodGang creation.
		if !ss.isExistingPodGang(expectedPG.fqn) {
			result.recordPodGangCreation(expectedPG.fqn)
		}

		// Verify all pods are created before proceeding
		if err := r.verifyAllPodsCreated(ss, expectedPG); err != nil {
			ss.logger.Info("Not all pods are created or associated to the PodGang yet", "PodGangName", expectedPG.fqn)
			result.recordError(err)
			continue
		}

		// Update status to set Initialized=True (idempotent - no need to check current state)
		if !ss.isPodGangInitialized(expectedPG.fqn) {
			if err := r.patchPodGangInitializedStatus(ctx, ss, expectedPG.fqn, metav1.ConditionTrue, groveschedulerv1alpha1.ConditionReasonPodGangPodsCreated, "PodGang is fully initialized"); err != nil {
				ss.logger.Error(err, "failed to update Initialized condition in PodGang status", "PodGangName", expectedPG.fqn)
				result.recordError(err)
				continue
			}
		}
	}

	return result
}

// patchPodGangInitializedStatus patches the Initialized condition with the given status.
func (r _resource) patchPodGangInitializedStatus(ctx context.Context, ss *syncState, podGangName string, status metav1.ConditionStatus, reason, message string) error {
	// Create a PodGang object with only the status we want to patch
	statusPatch := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podGangName,
			Namespace: ss.pcs.Namespace,
		},
	}
	setOrUpdateInitializedCondition(statusPatch, status, reason, message)
	// One could argue why not use Status.Phase to also denote Initialized condition. For now the argument is that
	// current set of phases (Pending, Starting, Running) is influenced by the status of constituent Pods w.r.t their
	// scheduling state, whereas initialized condition is denoting if a PodGang is ready to be scheduled
	// (so it is pre-scheduling phase state). We can always revisit this in future if this reasoning changes.
	statusPatch.Status.Phase = groveschedulerv1alpha1.PodGangPhasePending
	if err := r.client.Status().Patch(ctx, statusPatch, client.Merge); err != nil {
		return err
	}
	ss.logger.Info("Successfully patched PodGang Initialized condition",
		"podGang", podGangName, "status", status)
	return nil
}

// verifyAllPodsCreated checks if all required pods exist before updating PodGang
func (r _resource) verifyAllPodsCreated(ss *syncState, pgi *podGangInfo) error {
	pclqs := ss.getPodCliques(pgi)
	if len(pclqs) != len(pgi.pclqs) {
		// Not all constituent PCLQs exist yet
		ss.logger.Info("Not all constituent PCLQs exist yet", "podGang", pgi.fqn, "expected", len(pgi.pclqs), "actual", len(pclqs))
		return groveerr.New(groveerr.ErrCodeRequeueAfter,
			component.OperationSync,
			fmt.Sprintf("Waiting for all pods to be created for PodGang %s", pgi.fqn),
		)
	}
	// check the health of each podclique
	numPendingPods := r.getPodsPendingCreationOrAssociation(ss, pgi)
	if numPendingPods > 0 {
		ss.logger.Info("skipping creation of PodGang as all desired replicas have not yet been created or assigned", "podGang", pgi.fqn, "numPendingPodsToCreateOrAssociate", numPendingPods)
		return groveerr.New(groveerr.ErrCodeRequeueAfter,
			component.OperationSync,
			fmt.Sprintf("Waiting for all pods to be created or assigned for PodGang %s", pgi.fqn),
		)
	}
	return nil
}

// getPodsForPodCliquesPendingCreation counts expected pods from non-existent PodCliques.
func (r _resource) getPodsForPodCliquesPendingCreation(ss *syncState, podGang *podGangInfo) int {
	return lo.Reduce(podGang.pclqs, func(agg int, pclq pclqInfo, _ int) int {
		if _, ok := ss.existingPCLQByName[pclq.fqn]; !ok {
			return agg + int(pclq.replicas)
		}
		return agg
	}, 0)
}

// getPodsPendingCreationOrAssociation counts pods not yet created or labeled for the PodGang.
func (r _resource) getPodsPendingCreationOrAssociation(ss *syncState, podGang *podGangInfo) int {
	// Find the number of expected pods from PodCliques that are pending creation
	numPodsPendingPCLQCreate := r.getPodsForPodCliquesPendingCreation(ss, podGang)

	// Find the number of pods pending creation of existing PodCliques
	var numPodsPendingCreateOrAssociate int
	pclqs := ss.getPodCliques(podGang)
	for _, pclq := range pclqs {
		existingPCLQPods := ss.existingPCLQPods[pclq.Name]
		// If there is a difference between the expected replicas and the existing pods, we need to account for that.
		// If the difference is positive, it means there are pending pods to create.
		// If the difference is negative, it means there are more existing pods than expected. In this case, we do not need to create any new pods, therefore we can ignore the negative difference.
		numPodsPendingCreateOrAssociate += max(0, int(pclq.Spec.Replicas)-len(existingPCLQPods))

		// For all existing pods in the PCLQ, check if they have the PodGang label set. If that is not set then add them to numPodsPendingCreateOrAssociate.
		for _, existingPod := range existingPCLQPods {
			podGangLabelValue, ok := existingPod.GetLabels()[apicommon.LabelPodGang]
			if !ok {
				ss.logger.Info("Pod does not have a PodGang label yet", "podObjectKey", client.ObjectKeyFromObject(&existingPod), "expectedPodGangName", podGang.fqn)
				numPodsPendingCreateOrAssociate += 1
				continue
			}
			if podGangLabelValue != podGang.fqn {
				ss.logger.Error(nil, "PodGang label does not match expected PodGang name. This should ideally never happen and indicates a coding error", "podObjectKey", client.ObjectKeyFromObject(&existingPod), "expectedPodGangName", podGang.fqn, "podGangLabelValue", podGangLabelValue)
				numPodsPendingCreateOrAssociate += 1
			}
		}
	}
	return numPodsPendingPCLQCreate + numPodsPendingCreateOrAssociate
}

// createOrUpdatePodGang creates or updates a single PodGang resource.
func (r _resource) createOrUpdatePodGang(ctx context.Context, ss *syncState, pgInfo *podGangInfo) error {
	pgObjectKey := client.ObjectKey{
		Namespace: ss.pcs.Namespace,
		Name:      pgInfo.fqn,
	}
	pg := emptyPodGang(pgObjectKey)
	ss.logger.Info("CreateOrPatch PodGang", "objectKey", pgObjectKey)
	_, err := controllerutil.CreateOrPatch(ctx, r.client, pg, func() error {
		return r.buildResource(ss.pcs, pgInfo, pg)
	})
	if err != nil {
		r.eventRecorder.Eventf(ss.pcs, corev1.EventTypeWarning, constants.ReasonPodGangCreateOrUpdateFailed, "Error Creating/Updating PodGang %v: %v", pgObjectKey, err)
		return groveerr.WrapError(err,
			errCodeCreateOrPatchPodGang,
			component.OperationSync,
			fmt.Sprintf("Failed to CreateOrPatch PodGang %v", pgObjectKey),
		)
	}

	// Update status with Initialized=False condition and Phase if not already set.
	// This needs to be done separately since CreateOrPatch doesn't handle updates/patches to status subresource.
	if !k8sutils.HasCondition(pg.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeInitialized)) {
		if err = r.patchPodGangInitializedStatus(ctx, ss, pg.Name, metav1.ConditionFalse, groveschedulerv1alpha1.ConditionReasonPodGangPodsCreationPending, "Not all constituent pods have been created yet"); err != nil {
			return err
		}
	}

	r.eventRecorder.Eventf(ss.pcs, corev1.EventTypeNormal, constants.ReasonPodGangCreateOrUpdateSuccessful, "Created/Updated PodGang %v", pgObjectKey)
	ss.logger.Info("Triggered CreateOrPatch of PodGang", "objectKey", pgObjectKey)
	return nil
}

// Convenience types and methods on these types that are used during sync flow run.
// ------------------------------------------------------------------------------------------------

// syncState holds the relevant state required during the sync flow run. The *ByName / *NameSet
// fields are O(1) views over their corresponding slices and are populated eagerly in
// prepareSyncFlow. Callers must access them as fields, not via getters — there is no lazy
// fallback because lazy mutation of syncState would race the moment the struct is shared
// across goroutines.
type syncState struct {
	logger                 logr.Logger
	pcs                    *grovecorev1alpha1.PodCliqueSet
	expectedPodGangs       []*podGangInfo
	existingPodGangs       []groveschedulerv1alpha1.PodGang
	existingPodGangByName  map[string]groveschedulerv1alpha1.PodGang
	deletedPodGangNames    []string
	existingPCLQPods       map[string][]corev1.Pod
	existingPCLQs          []grovecorev1alpha1.PodClique
	existingPCLQByName     map[string]grovecorev1alpha1.PodClique
	expectedPodGangByName  map[string]*podGangInfo
	expectedPodGangNameSet componentutils.Set[string]
	unassignedPodsByPCLQ   map[string][]corev1.Pod
	tasEnabled             bool
	topologyLevels         []grovecorev1alpha1.TopologyLevel
}

// getPodGangNamesPendingCreation identifies PodGangs not yet created.
func (ss *syncState) getPodGangNamesPendingCreation() []string {
	return lo.FilterMap(ss.expectedPodGangs, func(podGang *podGangInfo, _ int) (string, bool) {
		return podGang.fqn, !ss.isExistingPodGang(podGang.fqn)
	})
}

func (ss *syncState) isExistingPodGang(podGangName string) bool {
	_, ok := ss.existingPodGangByName[podGangName]
	return ok
}

func (ss *syncState) getExcessPodGangNames() []string {
	var excessPodGangNames []string
	for _, existingPodGang := range ss.existingPodGangs {
		if !ss.expectedPodGangNameSet.Has(existingPodGang.Name) {
			excessPodGangNames = append(excessPodGangNames, existingPodGang.Name)
		}
	}
	return excessPodGangNames
}

func (ss *syncState) isPodGangInitialized(podGangName string) bool {
	foundPG, ok := ss.existingPodGangByName[podGangName]
	return ok && k8sutils.IsConditionTrue(foundPG.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeInitialized))
}

// initializeAssignedAndUnassignedPodsForPCS categorizes pods by PodGang assignment.
// The lookup yields a *podGangInfo that aliases an entry in ss.expectedPodGangs (which stores
// pointers). Mutations via refreshAssociatedPCLQPods therefore propagate back to the slice;
// changing expectedPodGangs to a value-typed slice would silently break this aliasing.
func (ss *syncState) initializeAssignedAndUnassignedPodsForPCS() {
	for pclqName, pods := range ss.existingPCLQPods {
		for _, pod := range pods {
			if metav1.HasLabel(pod.ObjectMeta, apicommon.LabelPodGang) {
				podGangName := pod.GetLabels()[apicommon.LabelPodGang]
				pgi, ok := ss.expectedPodGangByName[podGangName]
				if !ok {
					continue
				}
				pgi.refreshAssociatedPCLQPods(pclqName, pod.Name)
			} else {
				ss.unassignedPodsByPCLQ[pclqName] = append(ss.unassignedPodsByPCLQ[pclqName], pod)
			}
		}
	}
}

// getPodCliques retrieves PodClique resources for a PodGang.
func (ss *syncState) getPodCliques(podGang *podGangInfo) []grovecorev1alpha1.PodClique {
	constituentPCLQs := make([]grovecorev1alpha1.PodClique, 0, len(podGang.pclqs))
	for _, podGangConstituentPCLQInfo := range podGang.pclqs {
		if pclq, ok := ss.existingPCLQByName[podGangConstituentPCLQInfo.fqn]; ok {
			constituentPCLQs = append(constituentPCLQs, pclq)
		}
	}
	return constituentPCLQs
}

// podGangInfoByName builds a name-keyed map for O(1) podGangInfo lookups. Kept local because
// podGangInfo is package-private; the public PodCliqueByName/PCSGByName/PodGangByName helpers
// in componentutils cover the cross-package equivalents.
func podGangInfoByName(podGangs []*podGangInfo) map[string]*podGangInfo {
	return lo.SliceToMap(podGangs, func(podGang *podGangInfo) (string, *podGangInfo) {
		return podGang.fqn, podGang
	})
}

// podGangInfoNameSet builds a Set of podGangInfo FQNs. Kept local for the same reason as
// podGangInfoByName.
func podGangInfoNameSet(podGangs []*podGangInfo) componentutils.Set[string] {
	return componentutils.NewSetBy(podGangs, func(podGang *podGangInfo) string {
		return podGang.fqn
	})
}

// syncFlowResult captures the result of a sync flow run.
type syncFlowResult struct {
	// createdPodGangNames are the names of the PodGangs that got created during the sync flow run.
	createdPodGangNames []string
	// errs are the list of errors during the sync flow run.
	errs []error
}

// hasErrors returns true if any errors occurred during sync.
func (sfr *syncFlowResult) hasErrors() bool {
	return len(sfr.errs) > 0
}

// recordError adds an error to the sync flow result.
func (sfr *syncFlowResult) recordError(err error) {
	sfr.errs = append(sfr.errs, err)
}

// recordPodGangCreation adds a PodGang to the created list.
func (sfr *syncFlowResult) recordPodGangCreation(podGangName string) {
	sfr.createdPodGangNames = append(sfr.createdPodGangNames, podGangName)
}

// getAggregatedError combines all errors into a single error.
func (sfr *syncFlowResult) getAggregatedError() error {
	return errors.Join(sfr.errs...)
}

// podGangInfo is a convenience type that holds the information about
// its constituent PodClique names and expected replicas per PodClique for this PodGang.
// Each PodClique constituent is directly mapped to a groveschedulerv1alpha1.PodGroup.
// This struct will be used to check if all pods required by this PodGang are created and determine if this PodGang can be created.
type podGangInfo struct {
	// fqn is a fully qualified name of a PodGang.
	fqn string
	// pcsReplicaIndex is the PodCliqueSet replica index this PodGang belongs to.
	pcsReplicaIndex int
	// extraLabels are additional labels stamped on the PodGang, derived from the PodGangMap entry it
	// is materialized from (epoch and role).
	extraLabels map[string]string
	// pclqs holds the relevant information for all constituent PodCliques for this PodGang.
	pclqs []pclqInfo
	// topologyConstraint holds the topology pack constraint applicable at the PodGang level.
	// These will be cleared when TAS is disabled.
	topologyConstraint *groveschedulerv1alpha1.TopologyConstraint
	// pcsgPackConstraints holds the topology pack constraints applicable at the PodCliqueScalingGroup level.
	// These will be cleared when TAS is disabled.
	pcsgTopologyConstraints []groveschedulerv1alpha1.TopologyConstraintGroupConfig
}

// refreshAssociatedPCLQPods adds pod names to a PodClique's associated pod list.
func (pgi *podGangInfo) refreshAssociatedPCLQPods(pclqName string, newlyAssociatedPods ...string) {
	for i := range pgi.pclqs {
		if pgi.pclqs[i].fqn == pclqName {
			pgi.pclqs[i].associatedPodNames = append(pgi.pclqs[i].associatedPodNames, newlyAssociatedPods...)
		}
	}
}

// pclqInfo represents a groveschedulerv1alpha1.PodGroup and captures information relative to the PodGang of which
// this PodClique is a constituent.
type pclqInfo struct {
	// fqn is a fully qualified name for the PodClique
	fqn string
	// replicas is the number of Pods that are assigned to the PodGang for which this PodClique is a constituent.
	replicas int32
	// minAvailable is the minimum number of pods that are required for gang scheduling from this PodClique
	minAvailable int32
	// associatedPodNames are Pod names (having this PodClique as an owner) that have already been associated to this PodGang.
	// This will be updated as and when pods are either deleted or new pods are associated.
	associatedPodNames []string
	// topologyConstraint holds the topology pack constraint for the PodClique.
	// These will be cleared when TAS is disabled.
	topologyConstraint *groveschedulerv1alpha1.TopologyConstraint
}

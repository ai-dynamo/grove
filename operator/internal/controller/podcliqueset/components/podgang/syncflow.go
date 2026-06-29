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

package podgang

import (
	"context"
	"errors"
	"fmt"
	"slices"

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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// prepareSyncFlow computes the required state for synchronizing PodGang resources.
func (r _resource) prepareSyncFlow(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) (sc *syncContext, err error) {
	pcsObjectKey := client.ObjectKeyFromObject(pcs)
	sc = &syncContext{
		pcs:                  pcs,
		logger:               logger,
		existingPCLQPods:     make(map[string][]corev1.Pod),
		unassignedPodsByPCLQ: make(map[string][]corev1.Pod),
	}

	sc.existingPCLQs, err = r.getExistingPCLQsForPCS(ctx, pcs)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPodCliques,
			component.OperationSync,
			fmt.Sprintf("failed to list PodCliques for PodCliqueSet %v", pcsObjectKey),
		)
	}
	sc.existingPCLQByName = componentutils.PodCliqueByName(sc.existingPCLQs)

	sc.existingPCSGs, err = r.getExistingPCSGsForPCS(ctx, pcs)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPodCliqueScalingGroups,
			component.OperationSync,
			fmt.Sprintf("failed to list PodCliqueScalingGroups for PodCliqueSet %v", pcsObjectKey),
		)
	}
	sc.existingPCSGByName = componentutils.PCSGByName(sc.existingPCSGs)

	sc.tasEnabled = r.tasConfig.Enabled
	if r.tasConfig.Enabled && componentutils.HasAnyTopologyConstraint(pcs) {
		topologyName, resolveErr := componentutils.FindExplicitTopologyNameForPodCliqueSet(pcs)
		if resolveErr == nil && topologyName != "" {
			sc.topologyLevels, err = clustertopology.GetClusterTopologyLevels(ctx, r.client, topologyName)
			if err != nil {
				if !apierrors.IsNotFound(err) {
					return nil, groveerr.WrapError(err,
						errCodeGetClusterTopologyLevels,
						component.OperationSync,
						fmt.Sprintf("failed to get cluster topology levels for %q", topologyName))
				}
				sc.logger.Info(
					"ClusterTopologyBinding not found while preparing PodGang sync; continuing without translated topology constraints",
					"pcs", pcsObjectKey,
					"topologyName", topologyName,
				)
				sc.topologyLevels = nil
			}
		}
		// If explicit topologyName lookup fails, sc.topologyLevels stays nil — the PCS reconciler
		// handles this via the TopologyNameMissing condition.
	}

	sc.existingPodGangs, err = componentutils.GetExistingPodGangs(ctx, r.client, pcs.ObjectMeta, pcs.Namespace)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPodGangs,
			component.OperationSync,
			fmt.Sprintf("Failed to get existing PodGangs for PodCliqueSet: %v", client.ObjectKeyFromObject(sc.pcs)),
		)
	}
	sc.existingPodGangByName = componentutils.PodGangByName(sc.existingPodGangs)

	if err = r.computeExpectedPodGangs(ctx, sc); err != nil {
		return nil, groveerr.WrapError(err,
			errCodeComputeExistingPodGangs,
			component.OperationSync,
			fmt.Sprintf("failed to compute expected PodGangs for PodCliqueSet %v", pcsObjectKey),
		)
	}
	sc.expectedPodGangByName = podGangInfoByName(sc.expectedPodGangs)
	sc.expectedPodGangNameSet = podGangInfoNameSet(sc.expectedPodGangs)

	sc.existingPCLQPods, err = r.getExistingPodsByPCLQForPCS(ctx, pcsObjectKey)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPods,
			component.OperationSync,
			fmt.Sprintf("failed to list Pods for PodCliqueSet %v", pcsObjectKey),
		)
	}
	sc.initializeAssignedAndUnassignedPodsForPCS()

	return sc, nil
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

// getExistingPCSGsForPCS fetches all existing PCSGs for the PodCliqueSet.
func (r _resource) getExistingPCSGsForPCS(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) ([]grovecorev1alpha1.PodCliqueScalingGroup, error) {
	pcsgList := &grovecorev1alpha1.PodCliqueScalingGroupList{}
	if err := r.client.List(ctx,
		pcsgList,
		client.InNamespace(pcs.Namespace),
		client.MatchingLabels(
			lo.Assign(
				apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
			),
		),
	); err != nil {
		return nil, err
	}
	return lo.Filter(pcsgList.Items, func(pcsg grovecorev1alpha1.PodCliqueScalingGroup, _ int) bool {
		return metav1.IsControlledBy(&pcsg, pcs)
	}), nil
}

// computeExpectedPodGangs computes expected PodGangs by reading the PodGangMap for each PCS replica.
// PodGangMap is the single source of truth for PodGang composition in all cases.
func (r _resource) computeExpectedPodGangs(ctx context.Context, sc *syncContext) error {
	for replicaIndex := range int(sc.pcs.Spec.Replicas) {
		pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: sc.pcs.Name, Replica: replicaIndex})
		pgm, err := componentutils.GetPodGangMap(ctx, r.client, pgmName, sc.pcs.Namespace)
		if err != nil {
			return err
		}
		for _, entry := range pgm.Spec.Entries {
			pgi, err := r.buildPodGangInfoFromEntry(sc, replicaIndex, entry)
			if err != nil {
				return fmt.Errorf("failed to build PodGang info from entry %q in PodGangMap %s: %w", entry.Name, pgmName, err)
			}
			sc.expectedPodGangs = append(sc.expectedPodGangs, pgi)
		}
	}
	return nil
}

// buildPodGangInfoFromEntry translates a PodGangEntry into a podGangInfo.
// The entry's PCSGReplicaIndices give the PCSG replica indices owned by this PodGang
// directly; no positional accumulator across entries is needed.
func (r _resource) buildPodGangInfoFromEntry(sc *syncContext, pcsReplicaIndex int, pgEntry grovecorev1alpha1.PodGangEntry) (*podGangInfo, error) {
	pg := &podGangInfo{fqn: pgEntry.Name, pcsReplicaIndex: pcsReplicaIndex}

	pg.pclqs = buildStandalonePCLQInfos(sc, pcsReplicaIndex, pgEntry)
	pcsgPCLQs, pcsgConstraints, err := buildPCLQInfosAndTopologyConstraintsForPCSGs(sc, pcsReplicaIndex, pgEntry)
	if err != nil {
		return nil, err
	}
	pg.pclqs = append(pg.pclqs, pcsgPCLQs...)
	pg.pcsgTopologyConstraints = pcsgConstraints
	pg.topologyConstraint = createTopologyPackConstraint(sc, client.ObjectKeyFromObject(sc.pcs), sc.pcs.Spec.Template.TopologyConstraint)

	return pg, nil
}

// buildStandalonePCLQInfos builds pclqInfo entries for standalone PodCliques referenced in the entry.
// Iterates template cliques in order to keep the result deterministic.
func buildStandalonePCLQInfos(sc *syncContext, pcsReplicaIndex int, pgEntry grovecorev1alpha1.PodGangEntry) []pclqInfo {
	var pclqs []pclqInfo
	for _, cliqueTemplate := range sc.pcs.Spec.Template.Cliques {
		pclqFQN := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: sc.pcs.Name, Replica: pcsReplicaIndex}, cliqueTemplate.Name)
		desiredPCLQReplicas, ok := pgEntry.PodCliques[cliqueTemplate.Name]
		if !ok {
			continue
		}
		pi := pclqInfo{
			fqn:          pclqFQN,
			replicas:     desiredPCLQReplicas,
			minAvailable: *cliqueTemplate.Spec.MinAvailable,
			isStandalone: true,
		}
		pi.topologyConstraint = createTopologyPackConstraint(sc, types.NamespacedName{Namespace: sc.pcs.Namespace, Name: pclqFQN}, cliqueTemplate.TopologyConstraint)
		pclqs = append(pclqs, pi)
	}
	return pclqs
}

// buildPCLQInfosAndTopologyConstraintsForPCSGs builds pclqInfo entries and TopologyConstraintGroupConfigs for
// PCSG-owned PodCliques referenced in the entry. Iterates template PCSG configs in order to keep the result deterministic.
func buildPCLQInfosAndTopologyConstraintsForPCSGs(sc *syncContext, pcsReplicaIndex int, pgEntry grovecorev1alpha1.PodGangEntry) ([]pclqInfo, []groveschedulerv1alpha1.TopologyConstraintGroupConfig, error) {
	var (
		pclqs           []pclqInfo
		pcsgConstraints []groveschedulerv1alpha1.TopologyConstraintGroupConfig
	)
	for _, pcsgConfig := range sc.pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: sc.pcs.Name, Replica: pcsReplicaIndex}, pcsgConfig.Name)
		replicaIndices, ok := pgEntry.PCSGReplicaIndices[pcsgConfig.Name]
		if !ok || len(replicaIndices) == 0 {
			continue
		}
		for _, replicaIdx := range replicaIndices {
			pclqFQNs := make([]string, 0, len(pcsgConfig.CliqueNames))
			for _, cliqueName := range pcsgConfig.CliqueNames {
				pclqTemplateSpec := componentutils.FindPodCliqueTemplateSpecByName(sc.pcs, cliqueName)
				if pclqTemplateSpec == nil {
					return nil, nil, fmt.Errorf("PCSG %q references clique %q that does not exist in PodCliqueSet %v", pcsgFQN, cliqueName, client.ObjectKeyFromObject(sc.pcs))
				}
				pclqFQN := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsgFQN, Replica: int(replicaIdx)}, cliqueName)
				pi := pclqInfo{
					fqn:          pclqFQN,
					replicas:     pclqTemplateSpec.Spec.Replicas,
					minAvailable: *pclqTemplateSpec.Spec.MinAvailable,
					isStandalone: false,
				}
				pi.topologyConstraint = createTopologyPackConstraint(sc, types.NamespacedName{Namespace: sc.pcs.Namespace, Name: pclqFQN}, pclqTemplateSpec.TopologyConstraint)
				pclqs = append(pclqs, pi)
				pclqFQNs = append(pclqFQNs, pclqFQN)
			}
			pcsgTopologyConstraint := createTopologyPackConstraint(sc, types.NamespacedName{Namespace: sc.pcs.Namespace, Name: pcsgFQN}, pcsgConfig.TopologyConstraint)
			if pcsgTopologyConstraint != nil {
				pcsgConstraints = append(pcsgConstraints, groveschedulerv1alpha1.TopologyConstraintGroupConfig{
					Name:               fmt.Sprintf("%s-%d", pcsgFQN, replicaIdx),
					PodGroupNames:      pclqFQNs,
					TopologyConstraint: pcsgTopologyConstraint,
				})
			}
		}
	}
	return pclqs, pcsgConstraints, nil
}

// createTopologyPackConstraint creates a TopologyPackConstraint based on the sync context and provided parameters for a resource.
// PackConstraints are defined at multiple levels (PodCliqueSet, PodCliqueScalingGroup, PodClique). This function helps create a TopologyPackConstraint for any of these levels.
func createTopologyPackConstraint(sc *syncContext, nsName types.NamespacedName, topologyConstraint *grovecorev1alpha1.TopologyConstraint) *groveschedulerv1alpha1.TopologyConstraint {
	// If Topology aware scheduling is disabled, return nil even if TopologyConstraint is specified.
	if !sc.tasEnabled || topologyConstraint == nil {
		return nil
	}

	pgPackConstraint := &groveschedulerv1alpha1.TopologyPackConstraint{}
	pgPackConstraint.Required = topologyLevelKeyForPackDomain(sc, nsName, topologyConstraint, topologyConstraint.RequiredDomain(), "required")
	pgPackConstraint.Preferred = topologyLevelKeyForPackDomain(sc, nsName, topologyConstraint, topologyConstraint.PreferredDomain(), "preferred")

	if pgPackConstraint.Required == nil && pgPackConstraint.Preferred == nil {
		return nil
	}
	return &groveschedulerv1alpha1.TopologyConstraint{PackConstraint: pgPackConstraint}
}

func topologyLevelKeyForPackDomain(sc *syncContext, nsName types.NamespacedName, topologyConstraint *grovecorev1alpha1.TopologyConstraint, topologyDomain grovecorev1alpha1.TopologyDomain, packConstraintType string) *string {
	if topologyDomain == "" {
		return nil
	}
	topologyLevel, found := lo.Find(sc.topologyLevels, func(topologyLevel grovecorev1alpha1.TopologyLevel) bool {
		return topologyLevel.Domain == topologyDomain
	})
	if !found {
		// This can happen if the ClusterTopologyBinding CR has changed after the resource was admitted.
		sc.logger.Info(packConstraintType+" topology domain not found in cluster topology levels, skipping setting "+packConstraintType+" pack constraint", "namespacedName", nsName, "topologyDomain", topologyDomain, "topologyConstraint", *topologyConstraint)
		return nil
	}
	return ptr.To(topologyLevel.Key)
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
func (r _resource) runSyncFlow(ctx context.Context, sc *syncContext) syncFlowResult {
	result := syncFlowResult{}
	if err := r.deleteExcessPodGangs(ctx, sc); err != nil {
		result.errs = append(result.errs, err)
		return result
	}
	return r.createOrUpdatePodGangs(ctx, sc)
}

// deleteExcessPodGangs removes PodGangs that are no longer needed.
func (r _resource) deleteExcessPodGangs(ctx context.Context, sc *syncContext) error {
	excessPodGangs := sc.getExcessPodGangNames()
	namespace := sc.pcs.Namespace
	for _, podGangToDelete := range excessPodGangs {
		pgObjectKey := client.ObjectKey{Namespace: namespace, Name: podGangToDelete}
		pg := emptyPodGang(pgObjectKey)
		sc.logger.Info("Delete excess PodGang", "objectKey", client.ObjectKeyFromObject(pg))
		if err := client.IgnoreNotFound(r.client.Delete(ctx, pg)); err != nil {
			r.eventRecorder.Eventf(sc.pcs, corev1.EventTypeWarning, constants.ReasonPodGangDeleteFailed, "Error deleting PodGang %v: %v", pgObjectKey, err)
			return groveerr.WrapError(err,
				errCodeDeleteExcessPodGang,
				component.OperationSync,
				fmt.Sprintf("failed to delete PodGang %v", pgObjectKey),
			)
		}
		r.eventRecorder.Eventf(sc.pcs, corev1.EventTypeNormal, constants.ReasonPodGangDeleteSuccessful, "Deleted PodGang %v", pgObjectKey)
		sc.deletedPodGangNames = append(sc.deletedPodGangNames, podGangToDelete)
		sc.logger.Info("Triggered delete of excess PodGang", "objectKey", client.ObjectKeyFromObject(pg))
	}
	return nil
}

// createOrUpdatePodGangs reconciles every expected PodGang. For each PodGang it performs the
// create-or-patch, verifies pods are present, and advances the Initialized → Scheduled → Ready
// condition lifecycle. Each step is idempotent.
func (r _resource) createOrUpdatePodGangs(ctx context.Context, sc *syncContext) syncFlowResult {
	result := syncFlowResult{}
	for _, expectedPG := range sc.expectedPodGangs {
		if err := r.createOrUpdatePodGang(ctx, sc, expectedPG); err != nil {
			sc.logger.Error(err, "failed to create or update PodGang", "PodGangName", expectedPG.fqn)
			result.recordError(err)
			return result
		}
		if !sc.isExistingPodGang(expectedPG.fqn) {
			result.recordPodGangCreation(expectedPG.fqn)
		}
		if err := r.verifyAllPodsCreated(sc, expectedPG); err != nil {
			sc.logger.Info("Not all pods are created or associated to the PodGang yet", "PodGangName", expectedPG.fqn)
			result.recordError(err)
			continue
		}
		if err := r.reconcileInitializedCondition(ctx, sc, expectedPG); err != nil {
			result.recordError(err)
			continue
		}
		if err := r.reconcileScheduledCondition(ctx, sc, expectedPG); err != nil {
			result.recordError(err)
			continue
		}
		if err := r.reconcileReadyCondition(ctx, sc, expectedPG); err != nil {
			result.recordError(err)
			continue
		}
	}

	return result
}

// createOrUpdatePodGang creates or updates a single PodGang resource.
func (r _resource) createOrUpdatePodGang(ctx context.Context, sc *syncContext, pgInfo *podGangInfo) error {
	pgObjectKey := client.ObjectKey{
		Namespace: sc.pcs.Namespace,
		Name:      pgInfo.fqn,
	}
	pg := emptyPodGang(pgObjectKey)
	sc.logger.Info("CreateOrPatch PodGang", "objectKey", pgObjectKey)
	_, err := controllerutil.CreateOrPatch(ctx, r.client, pg, func() error {
		return r.buildResource(sc.pcs, pgInfo, pg)
	})
	if err != nil {
		r.eventRecorder.Eventf(sc.pcs, corev1.EventTypeWarning, constants.ReasonPodGangCreateOrUpdateFailed, "Error Creating/Updating PodGang %v: %v", pgObjectKey, err)
		return groveerr.WrapError(err,
			errCodeCreateOrPatchPodGang,
			component.OperationSync,
			fmt.Sprintf("Failed to CreateOrPatch PodGang %v", pgObjectKey),
		)
	}

	// Update status with Initialized=False condition if not already set.
	// This needs to be done separately since CreateOrPatch doesn't handle updates/patches to status subresource.
	if !k8sutils.HasCondition(pg.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeInitialized)) {
		if err = r.patchPodGangCondition(ctx, sc, pg.Name, groveschedulerv1alpha1.PodGangConditionTypeInitialized, metav1.ConditionFalse, groveschedulerv1alpha1.ConditionReasonPodGangPodsCreationPending, "Not all constituent pods have been created yet"); err != nil {
			return err
		}
	}

	r.eventRecorder.Eventf(sc.pcs, corev1.EventTypeNormal, constants.ReasonPodGangCreateOrUpdateSuccessful, "Created/Updated PodGang %v", pgObjectKey)
	sc.logger.Info("Triggered CreateOrPatch of PodGang", "objectKey", pgObjectKey)
	return nil
}

// verifyAllPodsCreated checks if all required pods exist before updating PodGang
func (r _resource) verifyAllPodsCreated(sc *syncContext, pgi *podGangInfo) error {
	pclqs := sc.getPodCliques(pgi)
	if len(pclqs) != len(pgi.pclqs) {
		// Not all constituent PCLQs exist yet
		sc.logger.Info("Not all constituent PCLQs exist yet", "podGang", pgi.fqn, "expected", len(pgi.pclqs), "actual", len(pclqs))
		return groveerr.New(groveerr.ErrCodeRequeueAfter,
			component.OperationSync,
			fmt.Sprintf("Waiting for all pods to be created for PodGang %s", pgi.fqn),
		)
	}
	// check the health of each podclique
	numPendingPods := r.getPodsPendingCreationOrAssociation(pgi)
	if numPendingPods > 0 {
		sc.logger.Info("skipping creation of PodGang as all desired replicas have not yet been created or assigned", "podGang", pgi.fqn, "numPendingPodsToCreateOrAssociate", numPendingPods)
		return groveerr.New(groveerr.ErrCodeRequeueAfter,
			component.OperationSync,
			fmt.Sprintf("Waiting for all pods to be created or assigned for PodGang %s", pgi.fqn),
		)
	}
	return nil
}

// getPodsPendingCreationOrAssociation counts how many of this PodGang's expected pods are not
// yet associated to it. For each constituent PodClique, the deficit is
// `pclq.replicas - len(pclq.associatedPodNames)`:
//   - If the PCLQ resource itself does not exist yet, `associatedPodNames` is empty, so the
//     full replica count is reported as pending.
//   - If the PCLQ exists but some pods haven't been created yet, only those uncreated pods are
//     reported as pending.
//   - Pods of the same PCLQ that are associated to a different PodGang (e.g. another MVU PodGang
//     in the same PCS replica) do not appear in this PodGang's `associatedPodNames` and are
//     correctly excluded — they belong to a sibling PodGang.
func (r _resource) getPodsPendingCreationOrAssociation(podGang *podGangInfo) int {
	var pending int
	for _, pclq := range podGang.pclqs {
		deficit := int(pclq.replicas) - len(pclq.associatedPodNames)
		if deficit > 0 {
			pending += deficit
		}
	}
	return pending
}

// reconcileInitializedCondition marks the PodGang as Initialized=True once all expected pods exist
// and have been associated. Idempotent — no-op when the condition is already True.
func (r _resource) reconcileInitializedCondition(ctx context.Context, sc *syncContext, pgi *podGangInfo) error {
	if sc.isPodGangInitialized(pgi.fqn) {
		return nil
	}
	if err := r.patchPodGangCondition(ctx, sc, pgi.fqn, groveschedulerv1alpha1.PodGangConditionTypeInitialized, metav1.ConditionTrue, groveschedulerv1alpha1.ConditionReasonPodGangPodsCreated, "PodGang is fully initialized"); err != nil {
		sc.logger.Error(err, "failed to update Initialized condition in PodGang status", "PodGangName", pgi.fqn)
		return err
	}
	return nil
}

// reconcileScheduledCondition advances the PodGang's Scheduled condition (sticky).
// When MinReplicas pods of every PodGroup have been placed on nodes, it first releases MinReplicas=0
// on standalone-PCLQ PodGroups, then patches Scheduled=True. Idempotent — no-op when the condition
// is already True or when placement has not yet completed.
func (r _resource) reconcileScheduledCondition(ctx context.Context, sc *syncContext, pgi *podGangInfo) error {
	if sc.isPodGangScheduled(pgi.fqn) {
		return nil
	}
	if !r.arePodGangMinReplicasScheduled(sc, pgi) {
		return nil
	}
	if err := r.releaseMinReplicasConstraint(ctx, sc, pgi); err != nil {
		sc.logger.Error(err, "failed to release MinReplicas constraint on PodGang", "PodGangName", pgi.fqn)
		return err
	}
	if err := r.patchPodGangCondition(ctx, sc, pgi.fqn, groveschedulerv1alpha1.PodGangConditionTypeScheduled, metav1.ConditionTrue, groveschedulerv1alpha1.ConditionReasonPodGangScheduled, "MinReplicas pods of every PodGroup are scheduled"); err != nil {
		sc.logger.Error(err, "failed to update Scheduled condition in PodGang status", "PodGangName", pgi.fqn)
		return err
	}
	sc.logger.Info("PodGang is now Scheduled", "podGang", pgi.fqn)
	return nil
}

// reconcileReadyCondition flips the PodGang's Ready condition based on live readiness.
// True ↔ False both directions: when MinAvailable pods of every PodGroup pass readiness probes the
// condition is set True; if readiness regresses it flips back to False. Idempotent — no-op when
// the current condition state already matches the live verdict.
func (r _resource) reconcileReadyCondition(ctx context.Context, sc *syncContext, pgi *podGangInfo) error {
	ready := r.arePodGangMinReplicasReady(sc, pgi)
	if ready == sc.isPodGangReady(pgi.fqn) {
		return nil
	}
	status := metav1.ConditionFalse
	reason := groveschedulerv1alpha1.ConditionReasonPodGangNotReady
	message := "one or more PodGroups have fewer Ready pods than MinAvailable"
	if ready {
		status = metav1.ConditionTrue
		reason = groveschedulerv1alpha1.ConditionReasonPodGangReady
		message = "MinAvailable pods of every PodGroup are ready"
	}
	if err := r.patchPodGangCondition(ctx, sc, pgi.fqn, groveschedulerv1alpha1.PodGangConditionTypeReady, status, reason, message); err != nil {
		sc.logger.Error(err, "failed to update Ready condition in PodGang status", "PodGangName", pgi.fqn)
		return err
	}
	sc.logger.Info("PodGang Ready condition updated", "podGang", pgi.fqn, "ready", ready)
	return nil
}

// arePodGangMinReplicasScheduled returns true if, for each PodGroup in the PodGang, at least
// MinReplicas pods that are associated to this PodGang have been scheduled onto a node.
// Only pods whose names appear in associatedPodNames are considered — this correctly handles
// the case where a standalone PCLQ's pods are spread across multiple PodGangs during a
// coherent update.
func (r _resource) arePodGangMinReplicasScheduled(sc *syncContext, pgi *podGangInfo) bool {
	for _, pclq := range pgi.pclqs {
		pods := sc.existingPCLQPods[pclq.fqn]
		var scheduledCount int32
		for i := range pods {
			if slices.Contains(pclq.associatedPodNames, pods[i].Name) && k8sutils.IsPodScheduled(&pods[i]) {
				scheduledCount++
			}
		}
		if scheduledCount < pclq.minAvailable {
			return false
		}
	}
	return true
}

// arePodGangMinReplicasReady returns true if, for each PodGroup in the PodGang, at least
// MinReplicas pods that are associated to this PodGang are in Ready state.
// Only pods whose names appear in associatedPodNames are considered — this correctly handles
// the case where a standalone PCLQ's pods are spread across multiple PodGangs during a
// coherent update.
func (r _resource) arePodGangMinReplicasReady(sc *syncContext, pgi *podGangInfo) bool {
	for _, pclq := range pgi.pclqs {
		pods := sc.existingPCLQPods[pclq.fqn]
		var readyCount int32
		for i := range pods {
			if slices.Contains(pclq.associatedPodNames, pods[i].Name) && k8sutils.IsPodReady(&pods[i]) {
				readyCount++
			}
		}
		if readyCount < pclq.minAvailable {
			return false
		}
	}
	return true
}

// releaseMinReplicasConstraint sets MinReplicas=0 on every standalone-PCLQ PodGroup of the given PodGang.
// PCSG-member PodGroups keep their original MinReplicas so the backend scheduler continues to
// protect them from preemption. See GREP-393 section - "Why standalone-PCLQ PodGroups release MinReplicas
// but PCSG-member PodGroups do not".
//
// This releases the gang scheduling constraint for standalone PodGroups, allowing individual pods
// to be removed without the scheduler evicting the entire PodGang for breaching the minimum
// availability contract enforced via `podGroup.minReplicas` in a PodGang. This must be done after
// the backend scheduler has scheduled minReplica pods across all PodGroups in a PodGang thus
// completing the gang-scheduling constraint. After the initial gang scheduling has been done, we
// should relax the minReplicas constraints to allow scale-ins. This is especially important in case
// of coherent updates where standalone PCLQ pods can be spread across one or more PodGangs. This
// means that while a scale-in does not breach the minAvailability guarantee as defined in the
// PodCliqueTemplateSpec but in the PodGang which has subset of the PCLQ replicas its `minReplicas`
// can be breached.
func (r _resource) releaseMinReplicasConstraint(ctx context.Context, sc *syncContext, pgi *podGangInfo) error {
	existingPG, ok := sc.existingPodGangByName[pgi.fqn]
	if !ok {
		return fmt.Errorf("PodGang %s not found in existing PodGangs", pgi.fqn)
	}
	standaloneFQNs := sets.New[string]()
	for _, pclq := range pgi.pclqs {
		if pclq.isStandalone {
			standaloneFQNs.Insert(pclq.fqn)
		}
	}
	if standaloneFQNs.Len() == 0 {
		return nil
	}
	patch := client.MergeFrom(&existingPG)
	pgToUpdate := existingPG.DeepCopy()
	for i := range pgToUpdate.Spec.PodGroups {
		if standaloneFQNs.Has(pgToUpdate.Spec.PodGroups[i].Name) {
			pgToUpdate.Spec.PodGroups[i].MinReplicas = 0
		}
	}
	if err := r.client.Patch(ctx, pgToUpdate, patch); err != nil {
		return fmt.Errorf("failed to set MinReplicas=0 on standalone PodGroups of PodGang %s: %w", pgi.fqn, err)
	}
	sc.logger.Info("Released MinReplicas constraint on standalone PodGroups of PodGang", "podGang", pgi.fqn, "standalonePodGroups", standaloneFQNs.UnsortedList())
	return nil
}

// patchPodGangCondition patches a condition on the PodGang status.
//
// Implementation note: it does a Get-modify-Patch using client.MergeFrom rather than building
// a fresh PodGang with only the new condition. JSON Merge Patch (RFC 7396) — what client.Merge
// produces for CRDs — replaces list fields wholesale, so a patch sourced from a struct that
// only has the one new condition would wipe every previously-set condition. Reading the live
// state first and feeding meta.SetStatusCondition the full slice avoids that.
func (r _resource) patchPodGangCondition(ctx context.Context, sc *syncContext, podGangName string, conditionType groveschedulerv1alpha1.PodGangConditionType, status metav1.ConditionStatus, reason, message string) error {
	pg, err := componentutils.GetPodGang(ctx, r.client, podGangName, sc.pcs.Namespace)
	if err != nil {
		return err
	}
	patchBase := pg.DeepCopy()
	condition := metav1.Condition{
		Type:               string(conditionType),
		Status:             status,
		ObservedGeneration: pg.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}
	meta.SetStatusCondition(&pg.Status.Conditions, condition)
	if err := r.client.Status().Patch(ctx, pg, client.MergeFrom(patchBase)); err != nil {
		return err
	}
	sc.logger.Info("Successfully patched PodGang condition",
		"podGang", podGangName, "conditionType", conditionType, "status", status)
	return nil
}

// Convenience types and methods on these types that are used during sync flow run.
// ------------------------------------------------------------------------------------------------

// syncContext holds the relevant state required during the sync flow run. The *ByName / *NameSet
// fields are O(1) views over their corresponding slices and are populated eagerly in
// prepareSyncFlow. Callers must access them as fields, not via getters — there is no lazy
// fallback because lazy mutation of syncContext would race the moment the struct is shared
// across goroutines.
type syncContext struct {
	//ctx                  context.Context
	pcs                    *grovecorev1alpha1.PodCliqueSet
	logger                 logr.Logger
	expectedPodGangs       []*podGangInfo
	existingPodGangs       []groveschedulerv1alpha1.PodGang
	existingPodGangByName  map[string]groveschedulerv1alpha1.PodGang
	deletedPodGangNames    []string
	existingPCLQPods       map[string][]corev1.Pod
	existingPCLQs          []grovecorev1alpha1.PodClique
	existingPCLQByName     map[string]grovecorev1alpha1.PodClique
	existingPCSGs          []grovecorev1alpha1.PodCliqueScalingGroup
	existingPCSGByName     map[string]grovecorev1alpha1.PodCliqueScalingGroup
	expectedPodGangByName  map[string]*podGangInfo
	expectedPodGangNameSet componentutils.Set[string]
	unassignedPodsByPCLQ   map[string][]corev1.Pod
	tasEnabled             bool
	topologyLevels         []grovecorev1alpha1.TopologyLevel
}

// getPodGangNamesPendingCreation identifies PodGangs not yet created.
func (sc *syncContext) getPodGangNamesPendingCreation() []string {
	return lo.FilterMap(sc.expectedPodGangs, func(podGang *podGangInfo, _ int) (string, bool) {
		return podGang.fqn, !sc.isExistingPodGang(podGang.fqn)
	})
}

func (sc *syncContext) isExistingPodGang(podGangName string) bool {
	_, ok := sc.existingPodGangByName[podGangName]
	return ok
}

func (sc *syncContext) getExcessPodGangNames() []string {
	var excessPodGangNames []string
	for _, existingPodGang := range sc.existingPodGangs {
		if !sc.expectedPodGangNameSet.Has(existingPodGang.Name) {
			excessPodGangNames = append(excessPodGangNames, existingPodGang.Name)
		}
	}
	return excessPodGangNames
}

func (sc *syncContext) isPodGangInitialized(podGangName string) bool {
	foundPG, ok := sc.existingPodGangByName[podGangName]
	return ok && k8sutils.IsConditionTrue(foundPG.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeInitialized))
}

func (sc *syncContext) isPodGangScheduled(podGangName string) bool {
	foundPG, ok := sc.existingPodGangByName[podGangName]
	return ok && k8sutils.IsConditionTrue(foundPG.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeScheduled))
}

func (sc *syncContext) isPodGangReady(podGangName string) bool {
	foundPG, ok := sc.existingPodGangByName[podGangName]
	return ok && k8sutils.IsConditionTrue(foundPG.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeReady))
}

// initializeAssignedAndUnassignedPodsForPCS categorizes pods by PodGang assignment.
// The lookup yields a *podGangInfo that aliases an entry in sc.expectedPodGangs (which stores
// pointers). Mutations via refreshAssociatedPCLQPods therefore propagate back to the slice;
// changing expectedPodGangs to a value-typed slice would silently break this aliasing.
func (sc *syncContext) initializeAssignedAndUnassignedPodsForPCS() {
	for pclqName, pods := range sc.existingPCLQPods {
		for _, pod := range pods {
			if metav1.HasLabel(pod.ObjectMeta, apicommon.LabelPodGang) {
				podGangName := pod.GetLabels()[apicommon.LabelPodGang]
				pgi, ok := sc.expectedPodGangByName[podGangName]
				if !ok {
					continue
				}
				pgi.refreshAssociatedPCLQPods(pclqName, pod.Name)
			} else {
				sc.unassignedPodsByPCLQ[pclqName] = append(sc.unassignedPodsByPCLQ[pclqName], pod)
			}
		}
	}
}

// getPodCliques retrieves PodClique resources for a PodGang.
func (sc *syncContext) getPodCliques(podGang *podGangInfo) []grovecorev1alpha1.PodClique {
	constituentPCLQs := make([]grovecorev1alpha1.PodClique, 0, len(podGang.pclqs))
	for _, podGangConstituentPCLQInfo := range podGang.pclqs {
		if pclq, ok := sc.existingPCLQByName[podGangConstituentPCLQInfo.fqn]; ok {
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
	// pcsReplicaIndex is the PCS replica index this PodGang belongs to.
	pcsReplicaIndex int
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
	// isStandalone is true when this PodClique is not owned by a PodCliqueScalingGroup.
	// Standalone PodGroups have their MinReplicas released to 0 once the PodGang is Scheduled,
	// while PCSG-member PodGroups keep their original MinReplicas to retain preemption protection.
	// See GREP-393 §"Why standalone-PCLQ PodGroups release MinReplicas but PCSG-member PodGroups do not".
	isStandalone bool
}

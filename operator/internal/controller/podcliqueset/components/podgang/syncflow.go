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
	"sort"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// prepareSyncFlow computes the required state for synchronizing PodGang resources.
func (r _resource) prepareSyncFlow(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) (sc *syncContext, err error) {
	pcsObjectKey := client.ObjectKeyFromObject(pcs)
	sc = &syncContext{
		ctx:                  ctx,
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

	sc.existingPCSGs, err = r.getExistingPCSGsForPCS(ctx, pcs)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPodCliqueScalingGroups,
			component.OperationSync,
			fmt.Sprintf("failed to list PodCliqueScalingGroups for PodCliqueSet %v", pcsObjectKey),
		)
	}

	// Implementation NOTE:
	// In the current version of the code ClusterTopology CR is created by Grove operator and its contents
	// are based on OperatorConfiguration.TopologyAwareSchedulingConfig. _resource struct already has access
	// to OperatorConfiguration.TopologyAwareSchedulingConfig so we could have used it directly instead of fetching
	// ClusterTopology CR again. This is true now, but in future this will change when we introduce support for
	// externally defined ClusterTopology CR. Hence, fetching ClusterTopology CR here to keep the code future-proof.
	sc.tasEnabled = r.tasConfig.Enabled
	if r.tasConfig.Enabled {
		sc.topologyLevels, err = componentutils.GetClusterTopologyLevels(ctx, r.client, grovecorev1alpha1.DefaultClusterTopologyName)
		if err != nil {
			return nil, groveerr.WrapError(err,
				errCodeGetClusterTopologyLevels,
				component.OperationSync,
				"failed to get cluster topology levels")
		}
	}

	if err = r.computeExpectedPodGangs(sc); err != nil {
		return nil, groveerr.WrapError(err,
			errCodeComputeExistingPodGangs,
			component.OperationSync,
			fmt.Sprintf("failed to compute existing PodGangs for PodCliqueSet %v", pcsObjectKey),
		)
	}

	sc.existingPodGangNames, err = r.GetExistingResourceNames(ctx, logger, pcs.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPodGangs,
			component.OperationSync,
			fmt.Sprintf("Failed to get existing PodGang names for PodCliqueSet: %v", client.ObjectKeyFromObject(sc.pcs)),
		)
	}

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
	// 1. Directly by PCS (standalone cliques)
	// 2. By PCSG (scaling group member cliques) - PCSG itself is owned by PCS
	// Label matching ensures they belong to this PCS, no ownership filter needed.
	return pclqList.Items, nil
}

// computeExpectedPodGangs computes expected PodGangs based on PCS replicas and scaling groups.
func (r _resource) computeExpectedPodGangs(sc *syncContext) error {
	expectedPodGangs := make([]*podGangInfo, 0, 50) // preallocate to avoid multiple allocations

	// For each PodCliqueSet replica, a base PodGang is expected to be created.
	// A base PodGang constitutes the minimum viable set of PodCliques that must be scheduled together.
	basePodGangs, err := getExpectedBasePodGangForPCSReplicas(sc)
	if err != nil {
		return err
	}
	expectedPodGangs = append(expectedPodGangs, basePodGangs...)

	// For each replica of PodCliqueSet, get the PodGangs associated to PodCliqueScalingGroup replicas above MinAvailable.
	// These are also commonly called "scaled PodGangs" which refer to replica indexes for PCSG above MinAvailable.
	// Each scaled replica of a PCSG is gang scheduled as is represented by its own PodGang resource.
	if len(sc.pcs.Spec.Template.PodCliqueScalingGroupConfigs) > 0 {
		for pcsReplica := range sc.pcs.Spec.Replicas {
			expectedPodGangsForPCSG, err := r.getExpectedScaledPodGangsForPCSG(sc, int(pcsReplica))
			if err != nil {
				return err
			}
			expectedPodGangs = append(expectedPodGangs, expectedPodGangsForPCSG...)
		}
	}
	sc.expectedPodGangs = expectedPodGangs
	return nil
}

// getExpectedBasePodGangForPCSReplicas creates the BASE PodGangs for each PodCliqueSet replica.
// These are the foundational PodGangs that contain:
// 1. Standalone PodCliques (not part of any scaling group)
// 2. PodCliques that are part of PodCliqueScalingGroup replicas [0, minAvailable-1]
func getExpectedBasePodGangForPCSReplicas(sc *syncContext) ([]*podGangInfo, error) {
	expectedPodGangs := make([]*podGangInfo, 0, int(sc.pcs.Spec.Replicas))
	for pcsReplica := range int(sc.pcs.Spec.Replicas) {
		basePodGang, err := buildExpectedBasePodGangForPCSReplica(sc, pcsReplica)
		if err != nil {
			return nil, err
		}
		expectedPodGangs = append(expectedPodGangs, basePodGang)
	}
	return expectedPodGangs, nil
}

// buildExpectedBasePodGangForPCSReplica constructs the base PodGang info for a given PodCliqueSet replica.
func buildExpectedBasePodGangForPCSReplica(sc *syncContext, pcsReplica int) (*podGangInfo, error) {
	podGangFQN := apicommon.GenerateBasePodGangName(apicommon.ResourceNameReplica{Name: sc.pcs.Name, Replica: pcsReplica})
	pg := &podGangInfo{
		fqn: podGangFQN,
		// TopologyConstraint for the base PodGang comes from the topology constraint defined at the PCS level.
		topologyConstraint: createTopologyPackConstraint(sc, apicommonconstants.KindPodCliqueSet, client.ObjectKeyFromObject(sc.pcs), sc.pcs.Spec.Template.TopologyConstraint),
	}
	pclqInfos := make([]pclqInfo, 0, len(sc.pcs.Spec.Template.Cliques))

	// Add all standalone PodCliques to the base PodGang PCLQs
	pclqInfos = append(pclqInfos, buildStandalonePCLQInfosForBasePodGang(sc, pcsReplica)...)
	// Compute PCSG PodCliques and TopologyConstraintGroupConfig's that are part of the base PodGang
	pcsgPackConstraints, pcsgPodCliques, err := buildPCSGGroupPackConstraintsAndPCLQsForBasePodGang(sc, pcsReplica)
	if err != nil {
		return nil, fmt.Errorf("failed to build PCSG TopologyConstraintGroupConfigs and PodClique infos for base PodGang %q: %w", podGangFQN, err)
	}
	pclqInfos = append(pclqInfos, pcsgPodCliques...)
	pg.pcsgTopologyConstraints = pcsgPackConstraints
	pg.pclqs = pclqInfos

	return pg, nil
}

func buildStandalonePCLQInfosForBasePodGang(sc *syncContext, pcsReplica int) []pclqInfo {
	pclqInfos := make([]pclqInfo, 0, len(sc.pcs.Spec.Template.Cliques))
	for _, pclqTemplateSpec := range sc.pcs.Spec.Template.Cliques {
		// Check if this PodClique belongs to a scaling group
		pcsgConfig := componentutils.FindScalingGroupConfigForClique(sc.pcs.Spec.Template.PodCliqueScalingGroupConfigs, pclqTemplateSpec.Name)
		if pcsgConfig == nil { // Standalone PodClique
			pclqFQN := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: sc.pcs.Name, Replica: pcsReplica}, pclqTemplateSpec.Name)
			pclqInfos = append(pclqInfos, buildPodCliqueInfo(sc, pclqTemplateSpec, pclqFQN, false))
		}
	}
	return pclqInfos
}

func buildPCSGGroupPackConstraintsAndPCLQsForBasePodGang(sc *syncContext, pcsReplica int) ([]groveschedulerv1alpha1.TopologyConstraintGroupConfig, []pclqInfo, error) {
	var (
		pclqInfos           []pclqInfo
		pcsgPackConstraints []groveschedulerv1alpha1.TopologyConstraintGroupConfig
	)
	for _, pcsgConfig := range sc.pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		// Iterate through replicas of the PCSG that belong to the base PodGang [0, minAvailable-1]
		minAvailable := int(*pcsgConfig.MinAvailable)
		pcsgTopologyConstraints, pcsgPodCliqueInfos, err := doBuildPCSGGroupPackConstraintsAndPCLQs(sc, pcsReplica, pcsgConfig, 0, minAvailable)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to build PCSG TopologyConstraintGroupConfigs and PodClique infos for base PodGang for PCSG %q: %w", pcsgConfig.Name, err)
		}
		pclqInfos = append(pclqInfos, pcsgPodCliqueInfos...)
		pcsgPackConstraints = append(pcsgPackConstraints, pcsgTopologyConstraints...)
	}
	return pcsgPackConstraints, pclqInfos, nil
}

func (r _resource) getExpectedScaledPodGangsForPCSG(sc *syncContext, pcsReplica int) ([]*podGangInfo, error) {
	var expectedPodGangs []*podGangInfo
	for _, pcsgConfig := range sc.pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: sc.pcs.Name, Replica: pcsReplica}, pcsgConfig.Name)
		replicas := sc.determinePCSGReplicas(pcsgFQN, pcsgConfig)
		minAvailable := int(*pcsgConfig.MinAvailable)
		scaledReplicas := replicas - minAvailable
		for podGangIndex, pcsgReplica := 0, minAvailable; podGangIndex < scaledReplicas; podGangIndex, pcsgReplica = podGangIndex+1, pcsgReplica+1 {
			podGangName := apicommon.CreatePodGangNameFromPCSGFQN(pcsgFQN, podGangIndex)
			pcsgTopologyConstraints, pcsgPodCliqueInfos, err := doBuildPCSGGroupPackConstraintsAndPCLQs(sc, pcsReplica, pcsgConfig, pcsgReplica, pcsgReplica+1)
			if err != nil {
				return nil, fmt.Errorf("failed to build PCSG TopologyConstraintGroupConfigs and PodClique infos for scaled PodGang %q for PCSG %q replica %d: %w", podGangName, pcsgConfig.Name, pcsgReplica, err)
			}
			expectedPodGangs = append(expectedPodGangs, &podGangInfo{
				fqn:                     podGangName,
				pclqs:                   pcsgPodCliqueInfos,
				pcsgTopologyConstraints: pcsgTopologyConstraints,
			})
		}
	}
	return expectedPodGangs, nil
}

func doBuildPCSGGroupPackConstraintsAndPCLQs(sc *syncContext, pcsReplica int, pcsgConfig grovecorev1alpha1.PodCliqueScalingGroupConfig, pcsgReplicaStartIndex, pcsgReplicaEndIndex int) ([]groveschedulerv1alpha1.TopologyConstraintGroupConfig, []pclqInfo, error) {
	var (
		pclqInfos           []pclqInfo
		pcsgPackConstraints []groveschedulerv1alpha1.TopologyConstraintGroupConfig
	)

	pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: sc.pcs.Name, Replica: pcsReplica}, pcsgConfig.Name)
	for replicaIndex := pcsgReplicaStartIndex; replicaIndex < pcsgReplicaEndIndex; replicaIndex++ {
		// Iterate through each PCLQ within the PCSG
		pclqFQNs := make([]string, 0, len(pcsgConfig.CliqueNames))
		for _, pclqName := range pcsgConfig.CliqueNames {
			pclqTemplateSpec := componentutils.FindPodCliqueTemplateSpecByName(sc.pcs, pclqName)
			if pclqTemplateSpec == nil {
				return nil, nil, fmt.Errorf("PodCliqueScalingGroup %q references a PodClique %q that does not exist in the PodCliqueSet: %v", pcsgConfig.Name, pclqName, client.ObjectKeyFromObject(sc.pcs))
			}
			pclqFQN := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsgFQN, Replica: replicaIndex}, pclqName)
			pclqInfos = append(pclqInfos, buildPodCliqueInfo(sc, pclqTemplateSpec, pclqFQN, true))
			pclqFQNs = append(pclqFQNs, pclqFQN)
		}
		if sc.tasEnabled {
			// For every PCSG a TopologyConstraintGroupConfig is created which has its own TopologyConstraint that is
			// defined for PCLQs within the PCSG. For each PCSG replica there is a separate TopologyConstraintGroupConfig.
			pcsgPackConstraints = append(pcsgPackConstraints, groveschedulerv1alpha1.TopologyConstraintGroupConfig{
				Name:               fmt.Sprintf("%s-%d", pcsgFQN, replicaIndex),
				PodGroupNames:      pclqFQNs,
				TopologyConstraint: createTopologyPackConstraint(sc, apicommonconstants.KindPodCliqueScalingGroup, types.NamespacedName{Namespace: sc.pcs.Namespace, Name: pcsgFQN}, pcsgConfig.TopologyConstraint),
			})
		}
	}

	return pcsgPackConstraints, pclqInfos, nil
}

// buildPodCliqueInfo creates pclqInfo with appropriate replica counts.
func buildPodCliqueInfo(sc *syncContext, pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, pclqFQN string, belongsToPCSG bool) pclqInfo {
	replicas := determinePodCliqueReplicas(sc, pclqTemplateSpec, pclqFQN, belongsToPCSG)
	expectedPCLQ := pclqInfo{
		fqn:          pclqFQN,
		replicas:     replicas,
		minAvailable: *pclqTemplateSpec.Spec.MinAvailable,
	}
	if sc.tasEnabled {
		expectedPCLQ.packConstraint = createTopologyPackConstraint(sc, apicommonconstants.KindPodClique, types.NamespacedName{Namespace: sc.pcs.Namespace, Name: pclqFQN}, pclqTemplateSpec.TopologyConstraint)
	}
	return expectedPCLQ
}

// createTopologyPackConstraint creates a TopologyPackConstraint based on the sync context and provided parameters for a resource.
// PackConstraints are defined at multiple levels (PodCliqueSet, PodCliqueScalingGroup, PodClique). This function helps create a TopologyPackConstraint for any of these levels.
func createTopologyPackConstraint(sc *syncContext, resourceKind string, nsName types.NamespacedName, requiredTopologyConstraint *grovecorev1alpha1.TopologyConstraint) *groveschedulerv1alpha1.TopologyConstraint {
	// If Topology aware scheduling is disabled, return nil even if TopologyConstraint is specified.
	if !sc.tasEnabled {
		return nil
	}
	// It is expected that the configuredTopologyLevels are already sorted from broadest to narrowest, preferred topology
	// level is the densest packing and the most optimal one as well. Hence, we pick the last element in the slice and set it as preferred.
	// NOTE: Currently there is no provision in the PodCliqueSet API to specify preferred topology constraint.
	// Operator assumes the densest level as preferred. If a provision is introduced in the API allowing users to specify
	// preferred topology constraints in future then this logic will need to be updated accordingly.
	preferredTopologyLevel := sc.topologyLevels[len(sc.topologyLevels)-1]
	pgPackConstraint := groveschedulerv1alpha1.TopologyPackConstraint{
		Preferred: ptr.To(preferredTopologyLevel.Key),
	}
	// If requiredTopologyConstraint is specified, set the required topology key accordingly.
	if requiredTopologyConstraint != nil {
		requiredTopologyLevel, found := lo.Find(sc.topologyLevels, func(topologyLevel grovecorev1alpha1.TopologyLevel) bool {
			return topologyLevel.Domain == requiredTopologyConstraint.PackDomain
		})
		if !found {
			// This can only happen if the ClusterTopology CR has been updated and no longer contains a topology level
			// that is being referenced by the resource's TopologyConstraint.
			// In the current version it's been decided to log this occurrence and skip setting the required constraint which is equivalent
			// to nullifying the required constraint.
			sc.logger.Info("required topology domain not found in cluster topology levels, skipping setting required pack constraint", "kind", resourceKind, "namespacedName", nsName, "requiredTopologyConstraint", *requiredTopologyConstraint)
		} else {
			pgPackConstraint.Required = ptr.To(requiredTopologyLevel.Key)
		}
	}
	return &groveschedulerv1alpha1.TopologyConstraint{PackConstraint: &pgPackConstraint}
}

// determinePodCliqueReplicas determines replica count considering HPA mutations.
func determinePodCliqueReplicas(sc *syncContext, pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, pclqFQN string, belongsToPCSG bool) int32 {
	if belongsToPCSG || pclqTemplateSpec.Spec.ScaleConfig == nil {
		return pclqTemplateSpec.Spec.Replicas
	}
	matchingPCLQ, found := lo.Find(sc.existingPCLQs, func(pclq grovecorev1alpha1.PodClique) bool {
		return pclqFQN == pclq.Name
	})
	if !found {
		// PodClique resource not found - might be during initial creation
		// Fall back to template replicas but log warning for visibility
		sc.logger.Info("[WARN]: PodClique resource not found, using template replicas",
			"podCliqueFQN", pclqFQN,
			"templateReplicas", pclqTemplateSpec.Spec.Replicas)
		return pclqTemplateSpec.Spec.Replicas
	}
	return matchingPCLQ.Spec.Replicas
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
func (r _resource) runSyncFlow(sc *syncContext) syncFlowResult {
	result := syncFlowResult{}
	if err := r.deleteExcessPodGangs(sc); err != nil {
		result.errs = append(result.errs, err)
		return result
	}
	return r.createOrUpdatePodGangs(sc)
}

// deleteExcessPodGangs removes PodGangs that are no longer needed.
func (r _resource) deleteExcessPodGangs(sc *syncContext) error {
	expectedPodGangNames := lo.Map(sc.expectedPodGangs, func(pg *podGangInfo, _ int) string {
		return pg.fqn
	})
	excessPodGangs, _ := lo.Difference(sc.existingPodGangNames, expectedPodGangNames)
	namespace := sc.pcs.Namespace
	for _, podGangToDelete := range excessPodGangs {
		pgObjectKey := client.ObjectKey{Namespace: namespace, Name: podGangToDelete}
		pg := emptyPodGang(pgObjectKey)
		sc.logger.Info("Delete excess PodGang", "objectKey", client.ObjectKeyFromObject(pg))
		if err := client.IgnoreNotFound(r.client.Delete(sc.ctx, pg)); err != nil {
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

// createOrUpdatePodGangs creates or updates all expected PodGangs when ready.
func (r _resource) createOrUpdatePodGangs(sc *syncContext) syncFlowResult {
	result := syncFlowResult{}
	pendingPodGangNames := sc.getPodGangNamesPendingCreation()
	for _, podGang := range sc.expectedPodGangs {
		sc.logger.Info("[createOrUpdatePodGangs] processing PodGang", "fqn", podGang.fqn)
		isPodGangPendingCreation := slices.Contains(pendingPodGangNames, podGang.fqn)
		// check the health of each podclique
		numPendingPods := r.getPodsPendingCreationOrAssociation(sc, podGang)
		if isPodGangPendingCreation && numPendingPods > 0 {
			sc.logger.Info("skipping creation of PodGang as all desired replicas have not yet been created or assigned", "fqn", podGang.fqn, "numPendingPodsToCreateOrAssociate", numPendingPods)
			result.recordPodGangPendingCreation(podGang.fqn)
			continue
		}
		if err := r.createOrUpdatePodGang(sc, podGang); err != nil {
			sc.logger.Error(err, "failed to create PodGang", "PodGangName", podGang.fqn)
			result.recordError(err)
			return result
		}
		result.recordPodGangCreation(podGang.fqn)
	}
	return result
}

// getPodsForPodCliquesPendingCreation counts expected pods from non-existent PodCliques.
func (r _resource) getPodsForPodCliquesPendingCreation(sc *syncContext, podGang *podGangInfo) int {
	existingPCLQNames := lo.Map(sc.existingPCLQs, func(pclq grovecorev1alpha1.PodClique, _ int) string {
		return pclq.Name
	})

	return lo.Reduce(podGang.pclqs, func(agg int, pclq pclqInfo, _ int) int {
		if !slices.Contains(existingPCLQNames, pclq.fqn) {
			return agg + int(pclq.replicas)
		}
		return agg
	}, 0)
}

// getPodsPendingCreationOrAssociation counts pods not yet created or labeled for the PodGang.
func (r _resource) getPodsPendingCreationOrAssociation(sc *syncContext, podGang *podGangInfo) int {
	// Find the number of expected pods from PodCliques that are pending creation
	numPodsPendingPCLQCreate := r.getPodsForPodCliquesPendingCreation(sc, podGang)

	// Find the number of pods pending creation of existing PodCliques
	var numPodsPendingCreateOrAssociate int
	pclqs := sc.getPodCliques(podGang)
	for _, pclq := range pclqs {
		existingPCLQPods := sc.existingPCLQPods[pclq.Name]
		// If there is a difference between the expected replicas and the existing pods, we need to account for that.
		// If the difference is positive, it means there are pending pods to create.
		// If the difference is negative, it means there are more existing pods than expected. In this case, we do not need to create any new pods, therefore we can ignore the negative difference.
		numPodsPendingCreateOrAssociate += max(0, int(pclq.Spec.Replicas)-len(existingPCLQPods))

		// For all existing pods in the PCLQ, check if they have the PodGang label set. If that is not set then add them to numPodsPendingCreateOrAssociate.
		for _, existingPod := range existingPCLQPods {
			podGangLabelValue, ok := existingPod.GetLabels()[apicommon.LabelPodGang]
			if !ok {
				sc.logger.Info("Pod does not have a PodGang label yet", "podObjectKey", client.ObjectKeyFromObject(&existingPod), "expectedPodGangName", podGang.fqn)
				numPodsPendingCreateOrAssociate += 1
				continue
			}
			if podGangLabelValue != podGang.fqn {
				sc.logger.Error(nil, "PodGang label does not match expected PodGang name. This should ideally never happen and indicates a coding error", "podObjectKey", client.ObjectKeyFromObject(&existingPod), "expectedPodGangName", podGang.fqn, "podGangLabelValue", podGangLabelValue)
				numPodsPendingCreateOrAssociate += 1
			}
		}
	}
	return numPodsPendingPCLQCreate + numPodsPendingCreateOrAssociate
}

// createOrUpdatePodGang creates or updates a single PodGang resource.
func (r _resource) createOrUpdatePodGang(sc *syncContext, pgInfo *podGangInfo) error {
	pgObjectKey := client.ObjectKey{
		Namespace: sc.pcs.Namespace,
		Name:      pgInfo.fqn,
	}
	pg := emptyPodGang(pgObjectKey)
	sc.logger.Info("CreateOrPatch PodGang", "objectKey", pgObjectKey)
	_, err := controllerutil.CreateOrPatch(sc.ctx, r.client, pg, func() error {
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
	r.eventRecorder.Eventf(sc.pcs, corev1.EventTypeNormal, constants.ReasonPodGangCreateOrUpdateSuccessful, "Created/Updated PodGang %v", pgObjectKey)
	sc.logger.Info("Triggered CreateOrPatch of PodGang", "objectKey", pgObjectKey)
	return nil
}

// createPodGroupsForPodGang constructs PodGroups from constituent PodCliques.
func createPodGroupsForPodGang(namespace string, pgInfo *podGangInfo) []groveschedulerv1alpha1.PodGroup {
	podGroups := lo.Map(pgInfo.pclqs, func(pi pclqInfo, _ int) groveschedulerv1alpha1.PodGroup {
		namespacedNames := lo.Map(pi.associatedPodNames, func(associatedPodName string, _ int) groveschedulerv1alpha1.NamespacedName {
			return groveschedulerv1alpha1.NamespacedName{
				Namespace: namespace,
				Name:      associatedPodName,
			}
		})
		// sorting the slice of NamespaceName. This prevents unnecessary updates to the PodGang resource if the only thing
		// that is difference is the order of NamespaceNames.
		sort.Slice(namespacedNames, func(i, j int) bool {
			return namespacedNames[i].Name < namespacedNames[j].Name
		})
		return groveschedulerv1alpha1.PodGroup{
			Name:               pi.fqn,
			PodReferences:      namespacedNames,
			MinReplicas:        pi.minAvailable,
			TopologyConstraint: pi.packConstraint,
		}
	})
	return podGroups
}

// Convenience types and methods on these types that are used during sync flow run.
// ------------------------------------------------------------------------------------------------

// syncContext holds the relevant state required during the sync flow run.
type syncContext struct {
	ctx                  context.Context
	pcs                  *grovecorev1alpha1.PodCliqueSet
	logger               logr.Logger
	expectedPodGangs     []*podGangInfo
	existingPodGangNames []string
	deletedPodGangNames  []string
	existingPCLQPods     map[string][]corev1.Pod
	existingPCLQs        []grovecorev1alpha1.PodClique
	existingPCSGs        []grovecorev1alpha1.PodCliqueScalingGroup
	unassignedPodsByPCLQ map[string][]corev1.Pod
	tasEnabled           bool
	topologyLevels       []grovecorev1alpha1.TopologyLevel
}

// getPodGangNamesPendingCreation identifies PodGangs not yet created.
func (sc *syncContext) getPodGangNamesPendingCreation() []string {
	return lo.FilterMap(sc.expectedPodGangs, func(podGang *podGangInfo, _ int) (string, bool) {
		return podGang.fqn, !slices.Contains(sc.existingPodGangNames, podGang.fqn)
	})
}

// initializeAssignedAndUnassignedPodsForPCS categorizes pods by PodGang assignment.
func (sc *syncContext) initializeAssignedAndUnassignedPodsForPCS() {
	for pclqName, pods := range sc.existingPCLQPods {
		for _, pod := range pods {
			if metav1.HasLabel(pod.ObjectMeta, apicommon.LabelPodGang) {
				podGangName := pod.GetLabels()[apicommon.LabelPodGang]
				// Find the index to work with the original slice element, not a copy
				pgiIndex := slices.IndexFunc(sc.expectedPodGangs, func(pgi *podGangInfo) bool {
					return podGangName == pgi.fqn
				})
				if pgiIndex == -1 {
					continue
				}
				// Work with the original element in the slice, not a copy
				sc.expectedPodGangs[pgiIndex].refreshAssociatedPCLQPods(pclqName, pod.Name)
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
		for _, pclq := range sc.existingPCLQs {
			if pclq.Name == podGangConstituentPCLQInfo.fqn {
				constituentPCLQs = append(constituentPCLQs, pclq)
			}
		}
	}
	return constituentPCLQs
}

// determinePCSGReplicas retrieves the number of replicas for a PCSG for a given PCS and PCS replica index.
// If the PCSG exists then it will return the pcsg.Spec.Replicas value, else it will return the template replicas
// as defined in grovecorev1alpha1.PodCliqueScalingGroupConfig.Replicas
func (sc *syncContext) determinePCSGReplicas(pcsgFQN string, pcsgConfig grovecorev1alpha1.PodCliqueScalingGroupConfig) int {
	foundExistingPCSG, ok := lo.Find(sc.existingPCSGs, func(pcsg grovecorev1alpha1.PodCliqueScalingGroup) bool {
		return pcsg.Name == pcsgFQN
	})
	if ok {
		return int(foundExistingPCSG.Spec.Replicas)
	}
	return int(*pcsgConfig.Replicas)
}

// syncFlowResult captures the result of a sync flow run.
type syncFlowResult struct {
	// podsGangsPendingCreation are the names of PodGangs that could not be created in this sync run.
	// It could be due to all PCLQs not present, or it could be due to presence of at least one PCLQ that is not ready.
	podsGangsPendingCreation []string
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

// hasPodGangsPendingCreation returns true if any PodGangs are waiting to be created.
func (sfr *syncFlowResult) hasPodGangsPendingCreation() bool {
	return len(sfr.podsGangsPendingCreation) > 0
}

// recordPodGangCreation adds a PodGang to the created list.
func (sfr *syncFlowResult) recordPodGangCreation(podGangName string) {
	sfr.createdPodGangNames = append(sfr.createdPodGangNames, podGangName)
}

// recordPodGangPendingCreation adds a PodGang to the pending creation list.
func (sfr *syncFlowResult) recordPodGangPendingCreation(podGangName string) {
	sfr.podsGangsPendingCreation = append(sfr.podsGangsPendingCreation, podGangName)
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
	// pclqs holds the relevant information for all constituent PodCliques for this PodGang.
	pclqs []pclqInfo
	// packConstraint holds the topology pack constraint applicable at the PodGang level.
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
	// packConstraint holds the topology pack constraint for the PodClique.
	// These will be cleared when TAS is disabled.
	packConstraint *groveschedulerv1alpha1.TopologyConstraint
}

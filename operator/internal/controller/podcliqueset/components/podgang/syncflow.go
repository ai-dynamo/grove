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
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// prepareSyncFlow computes the required state for synchronizing PodGang resources.
func (r _resource) prepareSyncFlow(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) (*syncContext, error) {
	pcsObjectKey := client.ObjectKeyFromObject(pcs)
	sc := &syncContext{
		ctx:                  ctx,
		pcs:                  pcs,
		logger:               logger,
		expectedPodGangs:     make([]podGangInfo, 0),
		existingPodGangNames: make([]string, 0),
		pclqs:                make([]grovecorev1alpha1.PodClique, 0),
		unassignedPodsByPCLQ: make(map[string][]corev1.Pod),
		pclqPods:             make(map[string][]corev1.Pod),
	}

	pclqs, err := r.getPCLQsForPCS(ctx, pcsObjectKey)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPodCliques,
			component.OperationSync,
			fmt.Sprintf("failed to list PodCliques for PodCliqueSet %v", pcsObjectKey),
		)
	}
	sc.pclqs = pclqs

	if err := r.computeExpectedPodGangs(sc); err != nil {
		return nil, groveerr.WrapError(err,
			errCodeComputeExistingPodGangs,
			component.OperationSync,
			fmt.Sprintf("failed to compute existing PodGangs for PodCliqueSet %v", pcsObjectKey),
		)
	}

	existingPodGangNames, err := r.GetExistingResourceNames(ctx, logger, pcs.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPodGangs,
			component.OperationSync,
			fmt.Sprintf("Failed to get existing PodGang names for PodCliqueSet: %v", client.ObjectKeyFromObject(sc.pcs)),
		)
	}
	sc.existingPodGangNames = existingPodGangNames

	podsByPCLQ, err := r.getPodsByPCLQForPCS(ctx, pcsObjectKey)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPods,
			component.OperationSync,
			fmt.Sprintf("failed to list Pods for PodCliqueSet %v", pcsObjectKey),
		)
	}

	sc.pclqPods = podsByPCLQ
	sc.initializeAssignedAndUnassignedPodsForPCS(podsByPCLQ)

	return sc, nil
}

// getPCLQsForPCS fetches all PodCliques managed by the PodCliqueSet.
func (r _resource) getPCLQsForPCS(ctx context.Context, pcsObjectKey client.ObjectKey) ([]grovecorev1alpha1.PodClique, error) {
	pclqList := &grovecorev1alpha1.PodCliqueList{}
	if err := r.client.List(ctx, pclqList,
		client.InNamespace(pcsObjectKey.Namespace),
		client.MatchingLabels(apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsObjectKey.Name))); err != nil {
		return nil, err
	}
	return pclqList.Items, nil
}

// computeExpectedPodGangs computes expected PodGangs based on PCS replicas and scaling groups.
func (r _resource) computeExpectedPodGangs(sc *syncContext) error {
	expectedPodGangs := make([]podGangInfo, 0, 50) // preallocate to avoid multiple allocations

	// For each PodCliqueSet replica, a base-podgang is expected to be created.
	// It contains the references to the initial set of pods that is to be gang-scheduled.
	expectedPodGangs = append(expectedPodGangs, getExpectedPodGangForPCSReplicas(sc)...)

	// For each replica of PodCliqueSet, get the PodGangs associated to PodCliqueScalingGroup replicas above MinAvailable.
	if len(sc.pcs.Spec.Template.PodCliqueScalingGroupConfigs) > 0 {
		for pcsReplica := range sc.pcs.Spec.Replicas {
			expectedPodGangsForPCSG, err := r.getExpectedPodGangsForPCSG(sc.ctx, sc.pcs, int(pcsReplica))
			if err != nil {
				return err
			}
			expectedPodGangs = append(expectedPodGangs, expectedPodGangsForPCSG...)
		}
	}
	sc.expectedPodGangs = expectedPodGangs
	return nil
}

// getExpectedPodGangForPCSReplicas creates the BASE PodGangs for each PodCliqueSet replica.
//
// These are the foundational PodGangs that contain:
// 1. Standalone PodCliques (not part of any scaling group)
// 2. Base scaling group PodCliques (replicas 0 through minAvailable-1 of each scaling group)
//
// Scaled PodGangs (for scaling group replicas >= minAvailable) are handled
// separately by getExpectedPodGangsForPCSG() and managed by the PodCliqueScalingGroup controller.
func getExpectedPodGangForPCSReplicas(sc *syncContext) []podGangInfo {
	expectedPodGangs := make([]podGangInfo, 0, int(sc.pcs.Spec.Replicas))
	for pcsReplica := range sc.pcs.Spec.Replicas {
		podGangName := apicommon.GenerateBasePodGangName(apicommon.ResourceNameReplica{Name: sc.pcs.Name, Replica: int(pcsReplica)})
		expectedPodGangs = append(expectedPodGangs, podGangInfo{
			fqn:   podGangName,
			pclqs: identifyConstituentPCLQsForPCSBasePodGang(sc, pcsReplica),
		})
	}
	return expectedPodGangs
}

// getExpectedPodGangsForPCSG computes scaled PodGangs for PCSG replicas above MinAvailable.
func (r _resource) getExpectedPodGangsForPCSG(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsReplica int) ([]podGangInfo, error) {
	existingPCSGs, err := r.getExistingPodCliqueScalingGroups(ctx, pcs, pcsReplica)
	if err != nil {
		return nil, err
	}

	expectedPodGangs := make([]podGangInfo, 0, 50) // preallocate to avoid multiple allocations

	// For each template PCSG config, compute the expected podGangInfo's
	for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		// Generate PCSG resource name from template name
		pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplica}, pcsgConfig.Name)

		// MinAvailable should always be non-nil due to kubebuilder default and defaulting webhook
		minAvailable := int(*pcsgConfig.MinAvailable)

		// Compute the replicas value from PCSG resource if it exists. If not, then use template replicas.
		replicas := int(*pcsgConfig.Replicas)
		pcsg, ok := lo.Find(existingPCSGs, func(sg grovecorev1alpha1.PodCliqueScalingGroup) bool {
			return sg.Name == pcsgFQN
		})
		if ok {
			replicas = int(pcsg.Spec.Replicas)
		}

		// Create scaled PodGangs for replicas starting from minAvailable
		// The first 0..(minAvailable-1) replicas are handled by the PCS replica PodGang
		// Scaled PodGangs use 0-based indexing regardless of minAvailable value
		scaledReplicas := replicas - minAvailable
		for podGangIndex, pcsgReplica := 0, minAvailable; podGangIndex < scaledReplicas; podGangIndex, pcsgReplica = podGangIndex+1, pcsgReplica+1 {
			podGangName := apicommon.CreatePodGangNameFromPCSGFQN(pcsgFQN, podGangIndex)
			pclqs, err := identifyConstituentPCLQsForPCSGPodGang(pcs, pcsgFQN, pcsgReplica, pcsgConfig.CliqueNames)
			if err != nil {
				return nil, err
			}
			expectedPodGangs = append(expectedPodGangs, podGangInfo{
				fqn:   podGangName,
				pclqs: pclqs,
			})
		}
	}
	return expectedPodGangs, nil
}

// identifyConstituentPCLQsForPCSBasePodGang identifies PCLQs that belong to a base PodGang.
func identifyConstituentPCLQsForPCSBasePodGang(sc *syncContext, pcsReplica int32) []pclqInfo {
	constituentPCLQs := make([]pclqInfo, 0, len(sc.pcs.Spec.Template.Cliques))
	for _, pclqTemplateSpec := range sc.pcs.Spec.Template.Cliques {
		// Check if this PodClique belongs to a scaling group
		pcsgConfig := componentutils.FindScalingGroupConfigForClique(sc.pcs.Spec.Template.PodCliqueScalingGroupConfigs, pclqTemplateSpec.Name)
		if pcsgConfig != nil {
			// Add scaling group PodClique instances for replicas 0 through (minAvailable-1)
			scalingGroupPclqs := buildPCSGPodCliqueInfosForBasePodGang(sc, pclqTemplateSpec, pcsgConfig, pcsReplica)
			constituentPCLQs = append(constituentPCLQs, scalingGroupPclqs...)
		} else {
			// Add standalone PodClique (not part of a scaling group)
			standalonePclq := buildNonPCSGPodCliqueInfosForBasePodGang(sc, pclqTemplateSpec, pcsReplica)
			constituentPCLQs = append(constituentPCLQs, standalonePclq)
		}
	}
	return constituentPCLQs
}

// buildPCSGPodCliqueInfosForBasePodGang generates PodClique info for the BASE PODGANG portion of a scaling group.
//
// IMPORTANT: This function only generates PodClique info for replicas 0 through (minAvailable-1).
// These PodCliques will be grouped into the BASE PodGang, which represents the minimum viable
// cluster that must be scheduled together as a gang.
//
// Scaled PodGangs (for replicas >= minAvailable) are generated separately by the
// PodCliqueScalingGroup controller and get their own scaled PodGang resources.
//
// EXAMPLE with minAvailable=3:
//   - This function creates PodCliques for replicas 0, 1, 2 → go into base PodGang "simple1-0"
//   - PCSG controller creates PodCliques for replicas 3, 4 → get scaled PodGangs "simple1-0-sga-0", etc.
func buildPCSGPodCliqueInfosForBasePodGang(sc *syncContext, pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, pcsgConfig *grovecorev1alpha1.PodCliqueScalingGroupConfig, pcsReplica int32) []pclqInfo {
	// MinAvailable should always be non-nil due to kubebuilder default and defaulting webhook
	minAvailable := int(*pcsgConfig.MinAvailable)

	pclqs := make([]pclqInfo, 0, minAvailable)
	for replicaIndex := range minAvailable {
		pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(
			apicommon.ResourceNameReplica{Name: sc.pcs.Name, Replica: int(pcsReplica)},
			pcsgConfig.Name,
		)
		pclqFQN := apicommon.GeneratePodCliqueName(
			apicommon.ResourceNameReplica{Name: pcsgFQN, Replica: replicaIndex},
			pclqTemplateSpec.Name,
		)

		pclqInfo := buildPodCliqueInfo(sc, pclqTemplateSpec, pclqFQN)
		pclqs = append(pclqs, pclqInfo)
	}
	return pclqs
}

// buildNonPCSGPodCliqueInfosForBasePodGang generates info for standalone PodCliques.
func buildNonPCSGPodCliqueInfosForBasePodGang(sc *syncContext, pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, pcsReplica int32) pclqInfo {
	pclqFQN := apicommon.GeneratePodCliqueName(
		apicommon.ResourceNameReplica{Name: sc.pcs.Name, Replica: int(pcsReplica)},
		pclqTemplateSpec.Name,
	)
	return buildPodCliqueInfo(sc, pclqTemplateSpec, pclqFQN)
}

// buildPodCliqueInfo creates pclqInfo with appropriate replica counts.
func buildPodCliqueInfo(sc *syncContext, pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, pclqFQN string) pclqInfo {
	replicas := determinePodCliqueReplicas(sc, pclqTemplateSpec, pclqFQN)
	return pclqInfo{
		fqn:          pclqFQN,
		replicas:     replicas,
		minAvailable: *pclqTemplateSpec.Spec.MinAvailable,
	}
}

// determinePodCliqueReplicas determines replica count considering HPA mutations.
func determinePodCliqueReplicas(sc *syncContext, pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, pclqFQN string) int32 {
	if pclqTemplateSpec.Spec.ScaleConfig == nil {
		return pclqTemplateSpec.Spec.Replicas
	}
	matchingPCLQ, found := lo.Find(sc.pclqs, func(pclq grovecorev1alpha1.PodClique) bool {
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

// identifyConstituentPCLQsForPCSGPodGang identifies PCLQs for a scaled PCSG PodGang.
func identifyConstituentPCLQsForPCSGPodGang(pcs *grovecorev1alpha1.PodCliqueSet, pcsgFQN string, pcsgReplica int, cliqueNames []string) ([]pclqInfo, error) {
	constituentPCLQs := make([]pclqInfo, 0, len(cliqueNames))
	for _, pclqName := range cliqueNames {
		pclqTemplate, ok := lo.Find(pcs.Spec.Template.Cliques, func(pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec) bool {
			return pclqName == pclqTemplateSpec.Name
		})
		if !ok {
			return nil, fmt.Errorf("PodCliqueScalingGroup references a PodClique that does not exist in the PodCliqueSet: %s", pclqName)
		}

		pclqFQN := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsgFQN, Replica: pcsgReplica}, pclqName)

		// Create pclqInfo using consistent logic (note: we use template replicas here since this is for scaling group instances)
		constituentPCLQs = append(constituentPCLQs, pclqInfo{
			fqn:          pclqFQN,
			replicas:     pclqTemplate.Spec.Replicas, // For scaling group instances, always use template replicas
			minAvailable: *pclqTemplate.Spec.MinAvailable,
		})
	}
	return constituentPCLQs, nil
}

// getExistingPodCliqueScalingGroups fetches PCSGs for a specific PCS replica.
func (r _resource) getExistingPodCliqueScalingGroups(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsReplica int) ([]grovecorev1alpha1.PodCliqueScalingGroup, error) {
	pcsgList := &grovecorev1alpha1.PodCliqueScalingGroupList{}
	if err := r.client.List(ctx,
		pcsgList,
		client.InNamespace(pcs.Namespace),
		client.MatchingLabels(
			lo.Assign(
				apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
				map[string]string{
					apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(pcsReplica),
				},
			),
		),
	); err != nil {
		return nil, err
	}
	return lo.Filter(pcsgList.Items, func(pcsg grovecorev1alpha1.PodCliqueScalingGroup, _ int) bool {
		return metav1.IsControlledBy(&pcsg, pcs)
	}), nil
}

// getPodsByPCLQForPCS fetches all non-terminating pods grouped by PodClique.
func (r _resource) getPodsByPCLQForPCS(ctx context.Context, pcsObjectKey client.ObjectKey) (map[string][]corev1.Pod, error) {
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
	result = r.createOrUpdatePodGangs(sc)
	if result.hasErrors() {
		return result
	}
	// After creating/updating PodGangs, update them with pod references if all pods are created
	if err := r.updatePodGangsWithPodReferences(sc); err != nil {
		result.recordError(err)
	}
	return result
}

// deleteExcessPodGangs removes PodGangs that are no longer needed.
func (r _resource) deleteExcessPodGangs(sc *syncContext) error {
	expectedPodGangNames := lo.Map(sc.expectedPodGangs, func(pg podGangInfo, _ int) string {
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

// createOrUpdatePodGangs creates or updates all expected PodGangs.
// PodGangs are created with empty podReferences, Initialized=False.
func (r _resource) createOrUpdatePodGangs(sc *syncContext) syncFlowResult {
	result := syncFlowResult{}
	for _, podGang := range sc.expectedPodGangs {
		sc.logger.Info("[createOrUpdatePodGangs] processing PodGang", "fqn", podGang.fqn)

		if err := r.createOrUpdatePodGang(sc, podGang); err != nil {
			sc.logger.Error(err, "failed to create PodGang", "PodGangName", podGang.fqn)
			result.recordError(err)
			return result
		}
		result.recordPodGangCreation(podGang.fqn)
	}
	return result
}

// updatePodGangsWithPodReferences updates PodGangs with pod references after Pods are created.
// Sets PodGangInitialized condition to True when all pods are present.
func (r _resource) updatePodGangsWithPodReferences(sc *syncContext) error {
	sc.logger.Info("Updating PodGang resources with pod references")

	// Get all PodGangs for this PodCliqueSet
	podGangList := &groveschedulerv1alpha1.PodGangList{}
	if err := r.client.List(sc.ctx, podGangList,
		client.InNamespace(sc.pcs.Namespace),
		client.MatchingLabels(getPodGangSelectorLabels(sc.pcs.ObjectMeta)),
	); err != nil {
		return groveerr.WrapError(err,
			errCodeListPodGangs,
			component.OperationSync,
			fmt.Sprintf("Failed to list PodGangs for PodCliqueSet: %v", client.ObjectKeyFromObject(sc.pcs)),
		)
	}

	// Get all Pods for this PodCliqueSet
	podList := &corev1.PodList{}
	if err := r.client.List(sc.ctx, podList,
		client.InNamespace(sc.pcs.Namespace),
		client.MatchingLabels(apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(sc.pcs.Name)),
	); err != nil {
		return groveerr.WrapError(err,
			errCodeListPods,
			component.OperationSync,
			fmt.Sprintf("Failed to list Pods for PodCliqueSet: %v", client.ObjectKeyFromObject(sc.pcs)),
		)
	}

	// Update each PodGang with its pod references
	for i := range podGangList.Items {
		podGang := &podGangList.Items[i]
		if err := r.updatePodGangWithPodReferences(sc, podGang, podList.Items); err != nil {
			// Don't return error immediately, continue updating other PodGangs
			// Errors like "not all pods created" are expected and will trigger requeue
			if errors.Is(err, groveerr.New(groveerr.ErrCodeRequeueAfter, component.OperationSync, "")) {
				return err
			}
			sc.logger.Error(err, "Failed to update PodGang with pod references", "podGang", podGang.Name)
		}
	}

	return nil
}

// updatePodGangWithPodReferences updates a PodGang with pod references and sets Initialized condition.
func (r _resource) updatePodGangWithPodReferences(sc *syncContext, podGang *groveschedulerv1alpha1.PodGang, allPods []corev1.Pod) error {
	// Check if this is the initial setup or a subsequent update
	isInitialSetup := !isInitialized(podGang)

	// Group pods by PodGroup (PodClique name)
	podsByGroup := make(map[string][]corev1.Pod)
	podGangName := podGang.Name // Use the PodGang name directly

	for _, pod := range allPods {
		if pod.Labels[apicommon.LabelPodGang] == podGangName {
			pclqName := pod.Labels[apicommon.LabelPodClique]
			podsByGroup[pclqName] = append(podsByGroup[pclqName], pod)
		}
	}

	// Check if all expected pods are created (only for initial setup)
	allPodsCreated := true
	if isInitialSetup {
		for i := range podGang.Spec.PodGroups {
			podGroup := &podGang.Spec.PodGroups[i]
			pods := podsByGroup[podGroup.Name]

			// Check if we have all expected pods
			if len(pods) < int(podGroup.MinReplicas) {
				allPodsCreated = false
				sc.logger.Info("Not all pods created yet for PodGroup",
					"podGang", podGang.Name,
					"podGroup", podGroup.Name,
					"expected", podGroup.MinReplicas,
					"actual", len(pods))
				break
			}
		}

		if !allPodsCreated {
			// Not ready yet for initial setup, will retry on next reconcile
			return groveerr.New(groveerr.ErrCodeRequeueAfter,
				component.OperationSync,
				fmt.Sprintf("Waiting for all pods to be created for PodGang %s", podGang.Name),
			)
		}
	}

	// Update podReferences for all PodGroups (both initial setup and subsequent updates)
	for i := range podGang.Spec.PodGroups {
		podGroup := &podGang.Spec.PodGroups[i]
		pods := podsByGroup[podGroup.Name]

		// Update podReferences
		podReferences := make([]groveschedulerv1alpha1.NamespacedName, 0, len(pods))
		for _, pod := range pods {
			podReferences = append(podReferences, groveschedulerv1alpha1.NamespacedName{
				Namespace: pod.Namespace,
				Name:      pod.Name,
			})
		}

		// Sort for consistency
		sort.Slice(podReferences, func(i, j int) bool {
			return podReferences[i].Name < podReferences[j].Name
		})

		podGroup.PodReferences = podReferences
	}

	// Update PodGang spec with the latest podReferences
	if err := r.client.Update(sc.ctx, podGang); err != nil {
		return groveerr.WrapError(err,
			errCodeUpdatePodGang,
			component.OperationSync,
			fmt.Sprintf("Failed to update PodGang %s with pod references", podGang.Name),
		)
	}

	// Only update status during initial setup (set Initialized=True once)
	// For subsequent updates (scale-out/scale-in/replacement), we don't touch the Initialized condition
	if isInitialSetup {
		// Re-fetch the PodGang to get the latest resourceVersion after spec update
		// This is necessary because updating spec changes the generation, and we need
		// the latest version before updating status
		updatedPodGang := &groveschedulerv1alpha1.PodGang{}
		if err := r.client.Get(sc.ctx, client.ObjectKeyFromObject(podGang), updatedPodGang); err != nil {
			return groveerr.WrapError(err,
				errCodeUpdatePodGang,
				component.OperationSync,
				fmt.Sprintf("Failed to get updated PodGang %s", podGang.Name),
			)
		}

		// Update status with Initialized condition and Phase (only during initial setup)
		original := updatedPodGang.DeepCopy()
		setInitializedCondition(updatedPodGang, metav1.ConditionTrue, "AllPodsCreated", "All pods have been created and references populated")
		updatedPodGang.Status.Phase = groveschedulerv1alpha1.PodGangPhasePending // Set phase to Pending when initialized

		if err := r.client.Status().Patch(sc.ctx, updatedPodGang, client.MergeFrom(original)); err != nil {
			return groveerr.WrapError(err,
				errCodeUpdatePodGang,
				component.OperationSync,
				fmt.Sprintf("Failed to update PodGang %s status", podGang.Name),
			)
		}

		sc.logger.Info("Successfully completed initial setup of PodGang with pod references and set Initialized=True",
			"podGang", podGang.Name)
	} else {
		sc.logger.Info("Successfully updated PodGang pod references (post-initialization update)",
			"podGang", podGang.Name)
	}
	return nil
}

// isInitialized checks if PodGang has Initialized condition set to True
// and all PodGroups have non-empty podReferences.
func isInitialized(podGang *groveschedulerv1alpha1.PodGang) bool {
	// First check if Initialized condition is True
	hasInitializedCondition := false
	for _, cond := range podGang.Status.Conditions {
		if cond.Type == string(groveschedulerv1alpha1.PodGangConditionTypeInitialized) &&
			cond.Status == metav1.ConditionTrue {
			hasInitializedCondition = true
			break
		}
	}

	if !hasInitializedCondition {
		return false
	}

	// Also verify that all PodGroups have enough podReferences to meet minReplicas
	// This ensures we don't skip initialization if spec was somehow reset or incomplete
	for _, pg := range podGang.Spec.PodGroups {
		if int32(len(pg.PodReferences)) < pg.MinReplicas {
			return false
		}
	}

	return true
}

// setInitializedCondition sets the PodGangInitialized condition.
func setInitializedCondition(pg *groveschedulerv1alpha1.PodGang, status metav1.ConditionStatus, reason, message string) {
	condition := metav1.Condition{
		Type:               string(groveschedulerv1alpha1.PodGangConditionTypeInitialized),
		Status:             status,
		ObservedGeneration: pg.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}

	// Update or append condition
	found := false
	for i, cond := range pg.Status.Conditions {
		if cond.Type == string(groveschedulerv1alpha1.PodGangConditionTypeInitialized) {
			pg.Status.Conditions[i] = condition
			found = true
			break
		}
	}
	if !found {
		pg.Status.Conditions = append(pg.Status.Conditions, condition)
	}
}

// getPodsForPodCliquesPendingCreation counts expected pods from non-existent PodCliques.
func (r _resource) getPodsForPodCliquesPendingCreation(sc *syncContext, podGang podGangInfo) int {
	existingPCLQNames := lo.Map(sc.pclqs, func(pclq grovecorev1alpha1.PodClique, _ int) string {
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
func (r _resource) getPodsPendingCreationOrAssociation(sc *syncContext, podGang podGangInfo) int {
	// Find the number of expected pods from PodCliques that are pending creation
	numPodsPendingPCLQCreate := r.getPodsForPodCliquesPendingCreation(sc, podGang)

	// Find the number of pods pending creation of existing PodCliques
	var numPodsPendingCreateOrAssociate int
	pclqs := sc.getPodCliques(podGang)
	for _, pclq := range pclqs {
		existingPCLQPods := sc.pclqPods[pclq.Name]
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
func (r _resource) createOrUpdatePodGang(sc *syncContext, pgInfo podGangInfo) error {
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

	// Update status with Initialized=False condition and Phase if not already set
	// This needs to be done separately since CreateOrPatch doesn't handle status subresource
	if !hasInitializedCondition(pg) {
		original := pg.DeepCopy()
		setInitializedCondition(pg, metav1.ConditionFalse, "PodsNotCreated", "Waiting for all pods to be created")
		pg.Status.Phase = groveschedulerv1alpha1.PodGangPhasePending // Set initial phase
		if err := r.client.Status().Patch(sc.ctx, pg, client.MergeFrom(original)); err != nil {
			sc.logger.Error(err, "Failed to set Initialized condition on PodGang", "podGang", pg.Name)
			// Don't fail the entire operation for status update errors
		}
	}

	r.eventRecorder.Eventf(sc.pcs, corev1.EventTypeNormal, constants.ReasonPodGangCreateOrUpdateSuccessful, "Created/Updated PodGang %v", pgObjectKey)
	sc.logger.Info("Triggered CreateOrPatch of PodGang", "objectKey", pgObjectKey)
	return nil
}

// hasInitializedCondition checks if PodGang has Initialized condition.
func hasInitializedCondition(pg *groveschedulerv1alpha1.PodGang) bool {
	for _, cond := range pg.Status.Conditions {
		if cond.Type == string(groveschedulerv1alpha1.PodGangConditionTypeInitialized) {
			return true
		}
	}
	return false
}

// createPodGroupsForPodGang constructs PodGroups from constituent PodCliques.
func createPodGroupsForPodGang(namespace string, pgInfo podGangInfo) []groveschedulerv1alpha1.PodGroup {
	podGroups := lo.Map(pgInfo.pclqs, func(pclq pclqInfo, _ int) groveschedulerv1alpha1.PodGroup {
		namespacedNames := lo.Map(pclq.associatedPodNames, func(associatedPodName string, _ int) groveschedulerv1alpha1.NamespacedName {
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
			Name:          pclq.fqn,
			PodReferences: namespacedNames,
			MinReplicas:   pclq.minAvailable,
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
	expectedPodGangs     []podGangInfo
	existingPodGangNames []string
	deletedPodGangNames  []string
	pclqs                []grovecorev1alpha1.PodClique
	unassignedPodsByPCLQ map[string][]corev1.Pod
	pclqPods             map[string][]corev1.Pod
}

// getPodGangNamesPendingCreation identifies PodGangs not yet created.
func (sc *syncContext) getPodGangNamesPendingCreation() []string {
	return lo.FilterMap(sc.expectedPodGangs, func(podGang podGangInfo, _ int) (string, bool) {
		return podGang.fqn, !slices.Contains(sc.existingPodGangNames, podGang.fqn)
	})
}

// initializeAssignedAndUnassignedPodsForPCS categorizes pods by PodGang assignment.
func (sc *syncContext) initializeAssignedAndUnassignedPodsForPCS(podsByPLCQ map[string][]corev1.Pod) {
	for pclqName, pods := range podsByPLCQ {
		for _, pod := range pods {
			if metav1.HasLabel(pod.ObjectMeta, apicommon.LabelPodGang) {
				podGangName := pod.GetLabels()[apicommon.LabelPodGang]
				// Find the index to work with the original slice element, not a copy
				pgiIndex := slices.IndexFunc(sc.expectedPodGangs, func(pgi podGangInfo) bool {
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
func (sc *syncContext) getPodCliques(podGang podGangInfo) []grovecorev1alpha1.PodClique {
	constituentPCLQs := make([]grovecorev1alpha1.PodClique, 0, len(podGang.pclqs))
	for _, podGangConstituentPCLQInfo := range podGang.pclqs {
		for _, pclq := range sc.pclqs {
			if pclq.Name == podGangConstituentPCLQInfo.fqn {
				constituentPCLQs = append(constituentPCLQs, pclq)
			}
		}
	}
	return constituentPCLQs
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
}

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

package workload

import (
	"context"
	"errors"
	"fmt"
	"slices"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha1 "k8s.io/api/scheduling/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// prepareSyncFlow computes the required state for synchronizing Workload resources.
func (r _resource) prepareSyncFlow(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) (*syncContext, error) {
	pcsObjectKey := client.ObjectKeyFromObject(pcs)
	sc := &syncContext{
		ctx:                   ctx,
		pcs:                   pcs,
		logger:                logger,
		expectedWorkloads:     make([]workloadInfo, 0),
		existingWorkloadNames: make([]string, 0),
		pclqs:                 make([]grovecorev1alpha1.PodClique, 0),
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

	if err := r.computeExpectedWorkloads(sc); err != nil {
		return nil, groveerr.WrapError(err,
			errCodeComputeExistingWorkload,
			component.OperationSync,
			fmt.Sprintf("failed to compute existing Workloads for PodCliqueSet %v", pcsObjectKey),
		)
	}

	existingWorkloadNames, err := r.GetExistingResourceNames(ctx, logger, pcs.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListWorkloads,
			component.OperationSync,
			fmt.Sprintf("Failed to get existing Workload names for PodCliqueSet: %v", client.ObjectKeyFromObject(sc.pcs)),
		)
	}
	sc.existingWorkloadNames = existingWorkloadNames

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

// computeExpectedWorkloads computes expected Workloads based on PCS replicas and scaling groups.
func (r _resource) computeExpectedWorkloads(sc *syncContext) error {
	expectedWorkloads := make([]workloadInfo, 0, int(sc.pcs.Spec.Replicas))

	// Create one workload per PodCliqueSet replica
	for pcsReplica := range sc.pcs.Spec.Replicas {
		workloadName := apicommon.GenerateBasePodGangName(apicommon.ResourceNameReplica{Name: sc.pcs.Name, Replica: int(pcsReplica)})
		expectedWorkloads = append(expectedWorkloads, workloadInfo{
			fqn:   workloadName,
			pclqs: identifyConstituentPCLQsForWorkload(sc, pcsReplica),
		})
	}

	sc.expectedWorkloads = expectedWorkloads
	return nil
}

// identifyConstituentPCLQsForWorkload identifies PCLQs that belong to a Workload.
func identifyConstituentPCLQsForWorkload(sc *syncContext, pcsReplica int32) []pclqInfo {
	constituentPCLQs := make([]pclqInfo, 0, len(sc.pcs.Spec.Template.Cliques))
	for _, pclqTemplateSpec := range sc.pcs.Spec.Template.Cliques {
		// Check if this PodClique belongs to a scaling group
		pcsgConfig := componentutils.FindScalingGroupConfigForClique(sc.pcs.Spec.Template.PodCliqueScalingGroupConfigs, pclqTemplateSpec.Name)
		if pcsgConfig != nil {
			// For scaling groups, include minAvailable replicas in base workload
			scalingGroupPclqs := buildPCSGPodCliqueInfosForBaseWorkload(sc, pclqTemplateSpec, pcsgConfig, pcsReplica)
			constituentPCLQs = append(constituentPCLQs, scalingGroupPclqs...)
		} else {
			// Add standalone PodClique (not part of a scaling group)
			standalonePclq := buildNonPCSGPodCliqueInfosForBaseWorkload(sc, pclqTemplateSpec, pcsReplica)
			constituentPCLQs = append(constituentPCLQs, standalonePclq)
		}
	}
	return constituentPCLQs
}

// buildPCSGPodCliqueInfosForBaseWorkload generates PodClique info for scaling group in a workload.
func buildPCSGPodCliqueInfosForBaseWorkload(sc *syncContext, pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec,
	pcsgConfig *grovecorev1alpha1.PodCliqueScalingGroupConfig, pcsReplica int32) []pclqInfo {

	pclqInfos := make([]pclqInfo, 0, int(*pcsgConfig.MinAvailable))
	minAvailable := int(*pcsgConfig.MinAvailable)

	for pcsgReplica := 0; pcsgReplica < minAvailable; pcsgReplica++ {
		// Generate PodClique name for scaling group member
		pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(
			apicommon.ResourceNameReplica{Name: sc.pcs.Name, Replica: int(pcsReplica)},
			pcsgConfig.Name,
		)
		pclqFQN := fmt.Sprintf("%s-%d-%s", pcsgFQN, pcsgReplica, pclqTemplateSpec.Name)

		pclqInfos = append(pclqInfos, pclqInfo{
			fqn:          pclqFQN,
			minAvailable: pclqTemplateSpec.Spec.Replicas,
			replicas:     pclqTemplateSpec.Spec.Replicas,
		})
	}
	return pclqInfos
}

// buildNonPCSGPodCliqueInfosForBaseWorkload generates PodClique info for standalone cliques.
func buildNonPCSGPodCliqueInfosForBaseWorkload(sc *syncContext, pclqTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, pcsReplica int32) pclqInfo {
	pclqFQN := apicommon.GeneratePodCliqueName(
		apicommon.ResourceNameReplica{Name: sc.pcs.Name, Replica: int(pcsReplica)},
		pclqTemplateSpec.Name,
	)

	minAvailable := pclqTemplateSpec.Spec.Replicas
	if pclqTemplateSpec.Spec.MinAvailable != nil {
		minAvailable = *pclqTemplateSpec.Spec.MinAvailable
	}

	return pclqInfo{
		fqn:          pclqFQN,
		minAvailable: minAvailable,
		replicas:     pclqTemplateSpec.Spec.Replicas,
	}
}

// runSyncFlow executes the sync flow for Workload resources.
func (r _resource) runSyncFlow(sc *syncContext) syncFlowResult {
	result := syncFlowResult{}

	// Delete excess workloads
	if err := r.deleteExcessWorkloads(sc); err != nil {
		result.recordError(err)
		return result
	}

	// Create or update workloads
	createUpdateResult := r.createOrUpdateWorkloads(sc)
	result.merge(createUpdateResult)

	return result
}

// deleteExcessWorkloads removes Workloads that are no longer expected.
func (r _resource) deleteExcessWorkloads(sc *syncContext) error {
	expectedWorkloadNames := lo.Map(sc.expectedWorkloads, func(wl workloadInfo, _ int) string {
		return wl.fqn
	})

	excessWorkloadNames := lo.Filter(sc.existingWorkloadNames, func(name string, _ int) bool {
		return !slices.Contains(expectedWorkloadNames, name)
	})

	for _, workloadToDelete := range excessWorkloadNames {
		wlObjectKey := client.ObjectKey{
			Namespace: sc.pcs.Namespace,
			Name:      workloadToDelete,
		}
		wl := emptyWorkload(wlObjectKey)
		if err := r.client.Delete(sc.ctx, wl); err != nil {
			r.eventRecorder.Eventf(sc.pcs, corev1.EventTypeWarning, constants.ReasonPodGangDeleteFailed, "Error Deleting Workload %v: %v", wlObjectKey, err)
			return groveerr.WrapError(err,
				errCodeDeleteExcessWorkload,
				component.OperationSync,
				fmt.Sprintf("failed to delete Workload %v", wlObjectKey),
			)
		}
		r.eventRecorder.Eventf(sc.pcs, corev1.EventTypeNormal, constants.ReasonPodGangDeleteSuccessful, "Deleted Workload %v", wlObjectKey)
		sc.logger.Info("Triggered delete of excess Workload", "objectKey", client.ObjectKeyFromObject(wl))
	}
	return nil
}

// createOrUpdateWorkloads creates or updates all expected Workloads.
func (r _resource) createOrUpdateWorkloads(sc *syncContext) syncFlowResult {
	result := syncFlowResult{}

	for _, workload := range sc.expectedWorkloads {
		sc.logger.Info("[createOrUpdateWorkloads] processing Workload", "fqn", workload.fqn)

		if err := r.createOrUpdateWorkload(sc, workload); err != nil {
			sc.logger.Error(err, "failed to create Workload", "WorkloadName", workload.fqn)
			result.recordError(err)
			return result
		}
		result.recordWorkloadCreation(workload.fqn)
	}
	return result
}

// createOrUpdateWorkload creates or updates a single Workload resource.
func (r _resource) createOrUpdateWorkload(sc *syncContext, wlInfo workloadInfo) error {
	wlObjectKey := client.ObjectKey{
		Namespace: sc.pcs.Namespace,
		Name:      wlInfo.fqn,
	}
	wl := emptyWorkload(wlObjectKey)
	sc.logger.Info("CreateOrPatch Workload", "objectKey", wlObjectKey)
	_, err := controllerutil.CreateOrPatch(sc.ctx, r.client, wl, func() error {
		return r.buildResource(sc.pcs, wlInfo, wl)
	})
	if err != nil {
		r.eventRecorder.Eventf(sc.pcs, corev1.EventTypeWarning, constants.ReasonPodGangCreateOrUpdateFailed, "Error Creating/Updating Workload %v: %v", wlObjectKey, err)
		return groveerr.WrapError(err,
			errCodeCreateOrPatchWorkload,
			component.OperationSync,
			fmt.Sprintf("Failed to CreateOrPatch Workload %v", wlObjectKey),
		)
	}
	r.eventRecorder.Eventf(sc.pcs, corev1.EventTypeNormal, constants.ReasonPodGangCreateOrUpdateSuccessful, "Created/Updated Workload %v", wlObjectKey)
	sc.logger.Info("Triggered CreateOrPatch of Workload", "objectKey", wlObjectKey)
	return nil
}

// createPodGroupsForWorkload constructs PodGroups from constituent PodCliques for K8s Workload API.
func createPodGroupsForWorkload(wlInfo workloadInfo) []schedulingv1alpha1.PodGroup {
	podGroups := lo.Map(wlInfo.pclqs, func(pclq pclqInfo, _ int) schedulingv1alpha1.PodGroup {
		return schedulingv1alpha1.PodGroup{
			Name: pclq.fqn,
			Policy: schedulingv1alpha1.PodGroupPolicy{
				Gang: &schedulingv1alpha1.GangSchedulingPolicy{
					MinCount: pclq.minAvailable,
				},
			},
		}
	})
	return podGroups
}

// Convenience types and methods used during sync flow run.

// syncContext holds the state required during the sync flow run.
type syncContext struct {
	ctx                   context.Context
	pcs                   *grovecorev1alpha1.PodCliqueSet
	logger                logr.Logger
	expectedWorkloads     []workloadInfo
	existingWorkloadNames []string
	pclqs                 []grovecorev1alpha1.PodClique
}

// workloadInfo holds information about a workload to be created/updated.
type workloadInfo struct {
	fqn   string
	pclqs []pclqInfo
}

// pclqInfo holds information about a PodClique constituent of a workload.
type pclqInfo struct {
	fqn          string
	minAvailable int32
	replicas     int32
}

// syncFlowResult accumulates the results of a sync flow run.
type syncFlowResult struct {
	errors                   []error
	workloadsPendingCreation []string
	createdWorkloads         []string
}

func (r *syncFlowResult) hasErrors() bool {
	return len(r.errors) > 0
}

func (r *syncFlowResult) hasWorkloadsPendingCreation() bool {
	return len(r.workloadsPendingCreation) > 0
}

func (r *syncFlowResult) getAggregatedError() error {
	return errors.Join(r.errors...)
}

func (r *syncFlowResult) recordError(err error) {
	r.errors = append(r.errors, err)
}

func (r *syncFlowResult) recordWorkloadPendingCreation(name string) {
	r.workloadsPendingCreation = append(r.workloadsPendingCreation, name)
}

func (r *syncFlowResult) recordWorkloadCreation(name string) {
	r.createdWorkloads = append(r.createdWorkloads, name)
}

func (r *syncFlowResult) merge(other syncFlowResult) {
	r.errors = append(r.errors, other.errors...)
	r.workloadsPendingCreation = append(r.workloadsPendingCreation, other.workloadsPendingCreation...)
	r.createdWorkloads = append(r.createdWorkloads, other.createdWorkloads...)
}

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

package hpa

import (
	"context"
	"fmt"
	"slices"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	errListHPA   grovecorev1alpha1.ErrorCode = "ERR_LIST_HPA"
	errSyncHPA   grovecorev1alpha1.ErrorCode = "ERR_SYNC_HPA"
	errDeleteHPA grovecorev1alpha1.ErrorCode = "ERR_DELETE_HPA"
)

type _resource struct {
	client client.Client
	scheme *runtime.Scheme
}

// New creates an instance of HPA components operator.
func New(client client.Client, scheme *runtime.Scheme) component.Operator[grovecorev1alpha1.PodCliqueSet] {
	return &_resource{
		client: client,
		scheme: scheme,
	}
}

// GetExistingResourceNames returns the names of all the existing resources that the HPA Operator manages.
func (r _resource) GetExistingResourceNames(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) ([]string, error) {
	logger.Info("Looking for existing HPA resources")
	objMetaList := &metav1.PartialObjectMetadataList{}
	objMetaList.SetGroupVersionKind(autoscalingv2.SchemeGroupVersion.WithKind("HorizontalPodAutoscaler"))
	if err := r.client.List(ctx,
		objMetaList,
		client.InNamespace(pcsObjMeta.Namespace),
		client.MatchingLabels(getPodCliqueHPASelectorLabels(pcsObjMeta)),
	); err != nil {
		return nil, groveerr.WrapError(err,
			errListHPA,
			component.OperationGetExistingResourceNames,
			fmt.Sprintf("Error listing HorizontalPodAutoscaler for PodCliques belonging to PodCliqueSet: %s", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	return k8sutils.FilterMapOwnedResourceNames(pcsObjMeta, objMetaList.Items), nil
}

// Sync synchronizes all resources that the HPA Operator manages.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	existingHPANames, err := r.GetExistingResourceNames(ctx, logger, pcs.ObjectMeta)
	if err != nil {
		return err
	}

	expectedHPAInfos := r.computeExpectedHPAs(pcs)
	tasks := make([]utils.Task, 0, (len(pcs.Spec.Template.Cliques)+len(pcs.Spec.Template.PodCliqueScalingGroupConfigs))*int(pcs.Spec.Replicas))
	tasks = append(tasks, r.deleteExcessHPATasks(logger, pcs, existingHPANames, expectedHPAInfos)...)
	tasks = append(tasks, r.createOrUpdateHPATasks(logger, pcs, expectedHPAInfos)...)

	if runResult := utils.RunConcurrentlyWithSlowStart(ctx, logger, 1, tasks); runResult.HasErrors() {
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errSyncHPA,
			component.OperationSync,
			fmt.Sprintf("Error CreateOrUpdate HorizontalPodAutoscalers for PodCliqueSet: %v, run summary: %s", client.ObjectKeyFromObject(pcs), runResult.GetSummary()),
		)
	}
	logger.Info("Successfully synced HorizontalPodAutoscalers for PodCliqueSet")
	return nil
}

// Delete deletes all resources that the HPA Operator manages.
func (r _resource) Delete(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) error {
	logger.Info("Triggering delete of HPA(s)")
	if err := r.client.DeleteAllOf(ctx,
		&autoscalingv2.HorizontalPodAutoscaler{},
		client.InNamespace(pcsObjMeta.Namespace),
		client.MatchingLabels(getPodCliqueHPASelectorLabels(pcsObjMeta)),
	); err != nil {
		return groveerr.WrapError(err,
			errDeleteHPA,
			component.OperationDelete,
			fmt.Sprintf("Error deleting HPA for PodCliqueSet %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	logger.Info("Deleted HPA(s)")
	return nil
}

// hpaInfo holds the state for a HPA resource. This will be used during sync run for HPA resources.
type hpaInfo struct {
	objectKey               client.ObjectKey
	targetScaleResourceKind string
	targetScaleResourceName string
	scaleConfig             grovecorev1alpha1.AutoScalingConfig
}

// computeExpectedHPAs calculates the set of HPAs that should exist for the PodCliqueSet.
func (r _resource) computeExpectedHPAs(pcs *grovecorev1alpha1.PodCliqueSet) []hpaInfo {
	expectedHPAInfos := make([]hpaInfo, 0, (len(pcs.Spec.Template.Cliques)+len(pcs.Spec.Template.PodCliqueScalingGroupConfigs))*int(pcs.Spec.Replicas))
	for replicaIndex := range pcs.Spec.Replicas {
		// compute expected HPA for PodCliques with individual HPAs attached to them
		for _, pclqTemplateSpec := range pcs.Spec.Template.Cliques {
			if pclqTemplateSpec.Spec.ScaleConfig == nil {
				continue
			}
			pclqFQN := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: int(replicaIndex)}, pclqTemplateSpec.Name)
			hpaObjectKey := client.ObjectKey{
				Namespace: pcs.Namespace,
				Name:      pclqFQN,
			}
			expectedHPAInfos = append(expectedHPAInfos, hpaInfo{
				objectKey:               hpaObjectKey,
				targetScaleResourceKind: constants.KindPodClique,
				targetScaleResourceName: pclqFQN,
				scaleConfig:             *pclqTemplateSpec.Spec.ScaleConfig,
			})
		}
		for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
			if pcsgConfig.ScaleConfig == nil {
				continue
			}
			pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: int(replicaIndex)}, pcsgConfig.Name)
			hpaObjectKey := client.ObjectKey{
				Namespace: pcs.Namespace,
				Name:      pcsgFQN,
			}
			expectedHPAInfos = append(expectedHPAInfos, hpaInfo{
				objectKey:               hpaObjectKey,
				targetScaleResourceKind: constants.KindPodCliqueScalingGroup,
				targetScaleResourceName: pcsgFQN,
				scaleConfig:             *pcsgConfig.ScaleConfig,
			})
		}
	}
	return expectedHPAInfos
}

// createOrUpdateHPATasks generates tasks to create or update HPAs.
func (r _resource) createOrUpdateHPATasks(logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, expectedHPAInfos []hpaInfo) []utils.Task {
	createOrUpdateTasks := make([]utils.Task, 0, len(expectedHPAInfos))
	for _, expectedHPAInfo := range expectedHPAInfos {
		task := utils.Task{
			Name: fmt.Sprintf("CreateOrUpdateHPA-%s", expectedHPAInfo.objectKey.Name),
			Fn: func(ctx context.Context) error {
				return r.doCreateOrUpdateHPA(ctx, logger, pcs, expectedHPAInfo)
			},
		}
		logger.V(4).Info("Adding task to create or update HPA", "taskName", task.Name, "hpaObjectKey", expectedHPAInfo.objectKey, "targetResourceKind", expectedHPAInfo.targetScaleResourceKind, "targetResourceName", expectedHPAInfo.targetScaleResourceName)
		createOrUpdateTasks = append(createOrUpdateTasks, task)
	}
	return createOrUpdateTasks
}

// deleteExcessHPATasks generates tasks to delete HPAs that are no longer needed.
func (r _resource) deleteExcessHPATasks(logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, existingHPANames []string, expectedHPAInfos []hpaInfo) []utils.Task {
	deleteTasks := make([]utils.Task, 0)
	expectedHPANames := lo.Map(expectedHPAInfos, func(h hpaInfo, _ int) string {
		return h.objectKey.Name
	})
	excessHPANames := lo.Filter(existingHPANames, func(existingHPAName string, _ int) bool {
		return !slices.Contains(expectedHPANames, existingHPAName)
	})
	for _, excessHPA := range excessHPANames {
		objectKey := client.ObjectKey{
			Namespace: pcs.Namespace,
			Name:      excessHPA,
		}
		task := utils.Task{
			Name: fmt.Sprintf("DeleteHPA-%s", excessHPA),
			Fn: func(ctx context.Context) error {
				return r.doDeleteHPA(ctx, logger, pcs.ObjectMeta, objectKey)
			},
		}
		logger.V(4).Info("Adding task to delete HPA", "taskName", task.Name, "hpaObjectKey", objectKey)
		deleteTasks = append(deleteTasks, task)
	}
	return deleteTasks
}

// doCreateOrUpdateHPA creates or updates a single HPA resource.
func (r _resource) doCreateOrUpdateHPA(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, expectedHPAInfo hpaInfo) error {
	logger.Info("Running CreateOrUpdate HPA", "targetScaleResourceKind", expectedHPAInfo.targetScaleResourceKind, "targetScaleResourceName", expectedHPAInfo.targetScaleResourceName, "hpaObjectKey", expectedHPAInfo.objectKey)
	hpa := emptyHPA(expectedHPAInfo.objectKey)
	opResult, err := k8sutils.CreateOrUpdate(ctx, r.client, hpa, func() error {
		return r.buildResource(pcs, hpa, expectedHPAInfo)
	})
	if err != nil {
		return groveerr.WrapError(err,
			errSyncHPA,
			component.OperationSync,
			fmt.Sprintf("Error creating or updating HPA: %v for [Kind: %s, Name: %s]", expectedHPAInfo.objectKey, expectedHPAInfo.targetScaleResourceKind, expectedHPAInfo.targetScaleResourceName),
		)
	}
	logger.Info("Triggered create or update of HPA", "hpaObjectKey", expectedHPAInfo.objectKey, "result", opResult)
	return nil
}

// doDeleteHPA deletes a single HPA resource.
func (r _resource) doDeleteHPA(ctx context.Context, logger logr.Logger, pcsObjectMeta metav1.ObjectMeta, objectKey client.ObjectKey) error {
	logger.Info("Running Delete HPA", "hpaObjectKey", objectKey)
	if err := r.client.Delete(ctx, emptyHPA(objectKey)); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("HPA not found, deletion is a no-op", "objectKey", objectKey)
			return nil
		}
		return groveerr.WrapError(err,
			errDeleteHPA,
			component.OperationSync,
			fmt.Sprintf("Error deleting excess HPA for PodCliqueSet %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjectMeta)),
		)
	}
	logger.Info("Triggered Delete of HPA", "hpaObjectKey", objectKey)
	return nil
}

// buildResource configures an HPA with the desired state.
func (r _resource) buildResource(pcs *grovecorev1alpha1.PodCliqueSet, hpa *autoscalingv2.HorizontalPodAutoscaler, expectedHPAInfo hpaInfo) error {
	// MinReplicas is always set by defaulting webhook
	hpa.Spec.MinReplicas = expectedHPAInfo.scaleConfig.MinReplicas
	hpa.Spec.MaxReplicas = expectedHPAInfo.scaleConfig.MaxReplicas
	hpa.Spec.ScaleTargetRef = autoscalingv2.CrossVersionObjectReference{
		Kind:       expectedHPAInfo.targetScaleResourceKind,
		Name:       expectedHPAInfo.targetScaleResourceName,
		APIVersion: grovecorev1alpha1.SchemeGroupVersion.String(),
	}
	hpa.Spec.Metrics = expectedHPAInfo.scaleConfig.Metrics
	hpa.Labels = getLabels(pcs.Name, hpa.Name)
	if err := controllerutil.SetControllerReference(pcs, hpa, r.scheme); err != nil {
		return groveerr.WrapError(err,
			errSyncHPA,
			component.OperationSync,
			fmt.Sprintf("Error setting owner reference of HPA %s to PodGang %s", hpa.Name, pcs.Name),
		)
	}
	return nil
}

// getLabels constructs labels for an HPA resource.
func getLabels(pcsName, hpaName string) map[string]string {
	hpaComponentLabels := map[string]string{
		apicommon.LabelAppNameKey:   hpaName,
		apicommon.LabelComponentKey: apicommon.LabelComponentNameHorizontalPodAutoscaler,
	}
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName),
		hpaComponentLabels,
	)
}

// getPodCliqueHPASelectorLabels returns labels for selecting all HPAs of a PodCliqueSet.
func getPodCliqueHPASelectorLabels(pcsObjectMeta metav1.ObjectMeta) map[string]string {
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsObjectMeta.Name),
		map[string]string{
			apicommon.LabelComponentKey: apicommon.LabelComponentNameHorizontalPodAutoscaler,
		},
	)
}

// emptyHPA creates an empty HPA with only metadata set.
func emptyHPA(objKey client.ObjectKey) *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objKey.Name,
			Namespace: objKey.Namespace,
		},
	}
}

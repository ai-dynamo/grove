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

package podcliquescalinggroup

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	errListPodCliqueScalingGroup       grovecorev1alpha1.ErrorCode = "ERR_LIST_PODCLIQUESCALINGGROUP"
	errSyncPodCliqueScalingGroup       grovecorev1alpha1.ErrorCode = "ERR_SYNC_PODCLIQUESCALINGGROUP"
	errDeletePodCliqueScalingGroup     grovecorev1alpha1.ErrorCode = "ERR_DELETE_PODCLIQUESCALINGGROUP"
	errCodeCreatePodCliqueScalingGroup grovecorev1alpha1.ErrorCode = "ERR_CREATE_PODCLIQUESCALINGGROUP"
)

type _resource struct {
	client        client.Client
	scheme        *runtime.Scheme
	eventRecorder record.EventRecorder
}

// New creates an instance of PodCliqueScalingGroup components operator.
func New(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder) component.Operator[grovecorev1alpha1.PodCliqueSet] {
	return &_resource{
		client:        client,
		scheme:        scheme,
		eventRecorder: eventRecorder,
	}
}

// GetExistingResourceNames returns the names of all the existing resources that the PodCliqueScalingGroup Operator manages.
func (r _resource) GetExistingResourceNames(ctx context.Context, _ logr.Logger, pcsObjMeta metav1.ObjectMeta) ([]string, error) {
	objMetaList := &metav1.PartialObjectMetadataList{}
	objMetaList.SetGroupVersionKind(grovecorev1alpha1.SchemeGroupVersion.WithKind("PodCliqueScalingGroup"))
	if err := r.client.List(ctx,
		objMetaList,
		client.InNamespace(pcsObjMeta.Namespace),
		client.MatchingLabels(getPodCliqueScalingGroupSelectorLabels(pcsObjMeta)),
	); err != nil {
		return nil, groveerr.WrapError(err,
			errListPodCliqueScalingGroup,
			component.OperationGetExistingResourceNames,
			fmt.Sprintf("Error listing PodCliqueScalingGroup for PodCliqueSet: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	return k8sutils.FilterMapOwnedResourceNames(pcsObjMeta, objMetaList.Items), nil
}

// Sync synchronizes all resources that the PodCliqueScalingGroup Operator manages.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	existingPCSGNames, err := r.GetExistingResourceNames(ctx, logger, pcs.ObjectMeta)
	if err != nil {
		return groveerr.WrapError(err,
			errListPodCliqueScalingGroup,
			component.OperationSync,
			fmt.Sprintf("Error listing PodCliqueScalingGroup for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}

	tasks := make([]utils.Task, 0, int(pcs.Spec.Replicas)*len(pcs.Spec.Template.PodCliqueScalingGroupConfigs))
	expectedPCSGNames := make([]string, 0, 20)
	for pcsReplica := range pcs.Spec.Replicas {
		for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
			pcsgName := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: int(pcsReplica)}, pcsgConfig.Name)
			expectedPCSGNames = append(expectedPCSGNames, pcsgName)
			pcsgObjectKey := client.ObjectKey{
				Name:      pcsgName,
				Namespace: pcs.Namespace,
			}
			pcsgExists := slices.Contains(existingPCSGNames, pcsgName)
			createOrUpdateTask := utils.Task{
				Name: fmt.Sprintf("CreateOrUpdatePodCliqueScalingGroup-%s", pcsgObjectKey),
				Fn: func(ctx context.Context) error {
					return r.doCreateOrUpdate(ctx, logger, pcs, int(pcsReplica), pcsgObjectKey, pcsgConfig, pcsgExists)
				},
			}
			tasks = append(tasks, createOrUpdateTask)
		}
	}

	excessPCSGNames := lo.Filter(existingPCSGNames, func(existingPCSGName string, _ int) bool {
		return !slices.Contains(expectedPCSGNames, existingPCSGName)
	})
	for _, excessPCSGName := range excessPCSGNames {
		pcsgObjectKey := client.ObjectKey{
			Namespace: pcs.Namespace,
			Name:      excessPCSGName,
		}
		deleteTask := utils.Task{
			Name: fmt.Sprintf("DeletePodCliqueScalingGroup-%s", pcsgObjectKey),
			Fn: func(ctx context.Context) error {
				return r.doDelete(ctx, logger, pcs, pcsgObjectKey)
			},
		}
		tasks = append(tasks, deleteTask)
	}

	if runResult := utils.RunConcurrently(ctx, logger, tasks); runResult.HasErrors() {
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errSyncPodCliqueScalingGroup,
			component.OperationSync,
			fmt.Sprintf("Error creating or updating PodCliqueScalingGroup for PodCliqueSet: %v, run summary: %s", client.ObjectKeyFromObject(pcs), runResult.GetSummary()),
		)
	}
	logger.Info("Successfully synced PodCliqueScalingGroup for PodCliqueSet")
	return nil
}

// Delete deletes all resources that the PodCliqueScalingGroup Operator manages.
func (r _resource) Delete(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) error {
	logger.Info("Triggering delete of PodCliqueScalingGroups")
	if err := r.client.DeleteAllOf(ctx,
		&grovecorev1alpha1.PodCliqueScalingGroup{},
		client.InNamespace(pcsObjMeta.Namespace),
		client.MatchingLabels(getPodCliqueScalingGroupSelectorLabels(pcsObjMeta))); err != nil {
		return groveerr.WrapError(err,
			errDeletePodCliqueScalingGroup,
			component.OperationDelete,
			fmt.Sprintf("Error deleting PodCliqueScalingGroup for PodCliqueSet: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	logger.Info("Deleted PodCliqueScalingGroups")
	return nil
}

// doCreateOrUpdate creates or updates a single PCSG resource.
func (r _resource) doCreateOrUpdate(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pcsReplica int, pcsgObjectKey client.ObjectKey, pcsgConfig grovecorev1alpha1.PodCliqueScalingGroupConfig, pcsgExists bool) error {
	logger.Info("CreateOrUpdate PodCliqueScalingGroup", "objectKey", pcsgObjectKey)
	pcsg := emptyPodCliqueScalingGroup(pcsgObjectKey)

	if _, err := controllerutil.CreateOrPatch(ctx, r.client, pcsg, func() error {
		if err := r.buildResource(pcsg, pcs, pcsReplica, pcsgConfig, pcsgExists); err != nil {
			return groveerr.WrapError(err,
				errCodeCreatePodCliqueScalingGroup,
				component.OperationSync,
				fmt.Sprintf("Error creating or updating PodCliqueScalingGroup: %v for PodCliqueSet: %v", pcsgObjectKey, client.ObjectKeyFromObject(pcs)),
			)
		}
		return nil
	}); err != nil {
		r.eventRecorder.Eventf(pcs, corev1.EventTypeWarning, constants.ReasonPodCliqueScalingGroupCreateOrUpdateFailed, "Error creating or updating PodCliqueScalingGroup %v: %v", pcsgObjectKey, err)
		return err
	}

	r.eventRecorder.Eventf(pcs, corev1.EventTypeNormal, constants.ReasonPodCliqueScalingGroupCreateSuccessful, "Created PodCliqueScalingGroup %v", pcsgObjectKey)
	logger.Info("Created or updated PodCliqueScalingGroup", "objectKey", pcsgObjectKey)
	return nil
}

// doDelete deletes a single PCSG resource.
func (r _resource) doDelete(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pcsgObjectKey client.ObjectKey) error {
	logger.Info("Delete PodCliqueScalingGroup", "objectKey", pcsgObjectKey)
	pcsg := emptyPodCliqueScalingGroup(pcsgObjectKey)
	if err := r.client.Delete(ctx, pcsg); err != nil {
		r.eventRecorder.Eventf(pcs, corev1.EventTypeWarning, constants.ReasonPodCliqueScalingGroupDeleteFailed, "Error deleting PodCliqueScalingGroup %v: %v", pcsgObjectKey, err)
		return groveerr.WrapError(err,
			errDeletePodCliqueScalingGroup,
			component.OperationSync,
			fmt.Sprintf("Error in delete of PodCliqueScalingGroup: %v", pcsg),
		)
	}
	r.eventRecorder.Eventf(pcs, corev1.EventTypeNormal, constants.ReasonPodCliqueScalingGroupDeleteSuccessful, "Deleted PodCliqueScalingGroup %v", pcsgObjectKey)
	logger.Info("Triggered delete of PodCliqueScalingGroup", "objectKey", pcsgObjectKey)
	return nil
}

// buildResource configures a PCSG with the desired state from the template.
func (r _resource) buildResource(pcsg *grovecorev1alpha1.PodCliqueScalingGroup, pcs *grovecorev1alpha1.PodCliqueSet, pcsReplica int, pcsgConfig grovecorev1alpha1.PodCliqueScalingGroupConfig, pcsgExists bool) error {
	if err := controllerutil.SetControllerReference(pcs, pcsg, r.scheme); err != nil {
		return groveerr.WrapError(err,
			errSyncPodCliqueScalingGroup,
			component.OperationSync,
			fmt.Sprintf("Error setting controller reference for PodCliqueScalingGroup: %v", client.ObjectKeyFromObject(pcsg)),
		)
	}
	// Only set replicas when creating the PCSG to allow external scaling (HPA, direct patching)
	// Post-creation scaling must be done directly on the PCSG resource, not via PCS template
	if !pcsgExists {
		pcsg.Spec.Replicas = *pcsgConfig.Replicas
	}
	pcsg.Spec.MinAvailable = pcsgConfig.MinAvailable
	pcsg.Spec.CliqueNames = pcsgConfig.CliqueNames
	pcsg.Labels = getLabels(pcs, pcsReplica, client.ObjectKeyFromObject(pcsg))

	// Propagate MNNVL annotation from PCS to PCSG
	if mnnvl.IsAutoMNNVLEnabled(pcs.Annotations) {
		if pcsg.Annotations == nil {
			pcsg.Annotations = make(map[string]string)
		}
		pcsg.Annotations[mnnvl.AnnotationAutoMNNVL] = mnnvl.AnnotationAutoMNNVLEnabled
	}

	return nil
}

// getLabels constructs labels for a PodCliqueScalingGroup resource.
func getLabels(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplica int, pclqScalingGroupObjKey client.ObjectKey) map[string]string {
	componentLabels := map[string]string{
		apicommon.LabelAppNameKey:               pclqScalingGroupObjKey.Name,
		apicommon.LabelComponentKey:             apicommon.LabelComponentNamePodCliqueScalingGroup,
		apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(pcsReplica),
	}
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
		componentLabels,
	)
}

// getPodCliqueScalingGroupSelectorLabels returns labels for selecting all PCSGs of a PodCliqueSet.
func getPodCliqueScalingGroupSelectorLabels(pcsObjMeta metav1.ObjectMeta) map[string]string {
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsObjMeta.Name),
		map[string]string{
			apicommon.LabelComponentKey: apicommon.LabelComponentNamePodCliqueScalingGroup,
		},
	)
}

// emptyPodCliqueScalingGroup creates an empty PCSG with only metadata set.
func emptyPodCliqueScalingGroup(objKey client.ObjectKey) *grovecorev1alpha1.PodCliqueScalingGroup {
	return &grovecorev1alpha1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objKey.Name,
			Namespace: objKey.Namespace,
		},
	}
}

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

package service

import (
	"context"
	"fmt"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	errSyncPodCliqueSetService   grovecorev1alpha1.ErrorCode = "ERR_SYNC_PODCLIQUESET_SERVICE"
	errDeletePodCliqueSetService grovecorev1alpha1.ErrorCode = "ERR_DELETE_PODCLIQUESET_SERVICE"
)

type _resource struct {
	client client.Client
	scheme *runtime.Scheme
}

// New creates a new Service operator for managing Service resources within PodCliqueSets
func New(client client.Client, scheme *runtime.Scheme) component.Operator[grovecorev1alpha1.PodCliqueSet] {
	return &_resource{
		client: client,
		scheme: scheme,
	}
}

// GetExistingResourceNames returns the names of all the existing resources that the Service Operator manages.
func (r _resource) GetExistingResourceNames(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) ([]string, error) {
	logger.Info("Looking for existing PodCliqueSet Headless Services", "objectKey", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta))
	objMetaList := &metav1.PartialObjectMetadataList{}
	objMetaList.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Service"))
	if err := r.client.List(ctx,
		objMetaList,
		client.InNamespace(pcsObjMeta.Namespace),
		client.MatchingLabels(getSelectorLabelsForAllHeadlessServices(pcsObjMeta.Name)),
	); err != nil {
		return nil, groveerr.WrapError(err,
			errSyncPodCliqueSetService,
			component.OperationGetExistingResourceNames,
			fmt.Sprintf("Error listing Headless Services for PodCliqueSet: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	return k8sutils.FilterMapOwnedResourceNames(pcsObjMeta, objMetaList.Items), nil
}

// Sync synchronizes all resources that the Service Operator manages.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	replicaIndexToObjectKeys := getObjectKeys(pcs)
	tasks := make([]utils.Task, 0, len(replicaIndexToObjectKeys))
	for replicaIndex, objectKey := range replicaIndexToObjectKeys {
		createOrUpdateTask := utils.Task{
			Name: fmt.Sprintf("CreateOrUpdatePodCliqueSetService-%s", objectKey),
			Fn: func(ctx context.Context) error {
				return r.doCreateOrUpdate(ctx, logger, pcs, replicaIndex, objectKey)
			},
		}
		tasks = append(tasks, createOrUpdateTask)
	}
	if runResult := utils.RunConcurrently(ctx, logger, tasks); runResult.HasErrors() {
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errSyncPodCliqueSetService,
			component.OperationSync,
			fmt.Sprintf("Error creating or updating PodCliqueSet Headless Services for PodCliqueSet: %v, run summary: %s", client.ObjectKeyFromObject(pcs), runResult.GetSummary()),
		)
	}
	logger.Info("Successfully synced Headless Services")
	return nil
}

// Delete removes all headless Service resources for the PodCliqueSet.
func (r _resource) Delete(ctx context.Context, logger logr.Logger, pgObjMeta metav1.ObjectMeta) error {
	logger.Info("Deleting Headless Services")
	if err := r.client.DeleteAllOf(ctx,
		&corev1.Service{},
		client.InNamespace(pgObjMeta.Namespace),
		client.MatchingLabels(getSelectorLabelsForAllHeadlessServices(pgObjMeta.Name))); err != nil {
		return groveerr.WrapError(err,
			errDeletePodCliqueSetService,
			component.OperationDelete,
			fmt.Sprintf("Failed to delete Headless Services for PodCliqueSet: %v", k8sutils.GetObjectKeyFromObjectMeta(pgObjMeta)),
		)
	}
	logger.Info("Deleted Headless Services")
	return nil
}

// doCreateOrUpdate creates or updates a single headless Service.
func (r _resource) doCreateOrUpdate(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int, svcObjectKey client.ObjectKey) error {
	logger.Info("Running CreateOrUpdate PodCliqueSet Headless Service", "pcsReplicaIndex", pcsReplicaIndex, "objectKey", svcObjectKey)
	svc := emptyService(svcObjectKey)
	opResult, err := k8sutils.CreateOrUpdate(ctx, r.client, svc, func() error {
		return r.buildResource(svc, pcs, pcsReplicaIndex)
	})
	if err != nil {
		return groveerr.WrapError(err,
			errSyncPodCliqueSetService,
			component.OperationSync,
			fmt.Sprintf("Error syncing Headless Service: %v for PodCliqueSet: %v", svcObjectKey, client.ObjectKeyFromObject(pcs)),
		)
	}
	logger.Info("Triggered create or update of PodGang Headless Service", "svcObjectKey", svcObjectKey, "result", opResult)
	return nil
}

// buildResource configures a headless Service for a PCS replica.
func (r _resource) buildResource(svc *corev1.Service, pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int) error {
	svc.Labels = getLabels(pcs.Name, client.ObjectKeyFromObject(svc), pcsReplicaIndex)
	var publishNotReadyAddresses bool
	if pcs.Spec.Template.HeadlessServiceConfig != nil {
		publishNotReadyAddresses = pcs.Spec.Template.HeadlessServiceConfig.PublishNotReadyAddresses
	}
	svc.Spec = corev1.ServiceSpec{
		Selector:                 getLabelSelectorForPodsInAPodCliqueSetReplica(pcs.Name, pcsReplicaIndex),
		ClusterIP:                "None",
		PublishNotReadyAddresses: publishNotReadyAddresses,
	}

	if err := controllerutil.SetControllerReference(pcs, svc, r.scheme); err != nil {
		return err
	}

	return nil
}

// getLabels constructs labels for a headless Service resource.
func getLabels(pcsName string, svcObjectKey client.ObjectKey, pcsReplicaIndex int) map[string]string {
	svcLabels := map[string]string{
		apicommon.LabelAppNameKey:               svcObjectKey.Name,
		apicommon.LabelComponentKey:             apicommon.LabelComponentNamePodCliqueSetReplicaHeadlessService,
		apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(pcsReplicaIndex),
	}
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName),
		svcLabels,
	)
}

// getLabelSelectorForPodsInAPodCliqueSetReplica returns pod selector labels for a PCS replica.
func getLabelSelectorForPodsInAPodCliqueSetReplica(pcsName string, pcsReplicaIndex int) map[string]string {
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName),
		map[string]string{
			apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(pcsReplicaIndex),
		},
	)
}

// getSelectorLabelsForAllHeadlessServices returns labels for selecting all Services of a PodCliqueSet.
func getSelectorLabelsForAllHeadlessServices(pcsName string) map[string]string {
	svcMatchingLabels := map[string]string{
		apicommon.LabelComponentKey: apicommon.LabelComponentNamePodCliqueSetReplicaHeadlessService,
	}
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName),
		svcMatchingLabels,
	)
}

// getObjectKeys constructs object keys for all headless Services across PCS replicas.
func getObjectKeys(pcs *grovecorev1alpha1.PodCliqueSet) []client.ObjectKey {
	objectKeys := make([]client.ObjectKey, 0, pcs.Spec.Replicas)
	for replicaIndex := range pcs.Spec.Replicas {
		serviceName := apicommon.GenerateHeadlessServiceName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: int(replicaIndex)})
		objectKeys = append(objectKeys, client.ObjectKey{
			Name:      serviceName,
			Namespace: pcs.Namespace,
		})
	}
	return objectKeys
}

// emptyService creates an empty Service with only metadata set.
func emptyService(objKey client.ObjectKey) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objKey.Name,
			Namespace: objKey.Namespace,
		},
	}
}

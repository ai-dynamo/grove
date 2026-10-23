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

package service

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/sync"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
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
func New(client client.Client, scheme *runtime.Scheme) component.Operator[grovecorev1alpha1.PodCliqueSet, pcssync.State, pcssync.Result] {
	return &_resource{
		client: client,
		scheme: scheme,
	}
}

// GetExistingResourceNames returns the names of all the existing resources that the Service Operator manages.
func (r _resource) GetExistingResourceNames(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) ([]string, error) {
	logger.V(1).Info("Looking for existing PodCliqueSet Headless Services", "objectKey", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta))
	serviceList := &corev1.ServiceList{}
	if err := r.client.List(ctx,
		serviceList,
		client.InNamespace(pcsObjMeta.Namespace),
		client.MatchingLabels(getSelectorLabelsForAllHeadlessServices(pcsObjMeta.Name)),
	); err != nil {
		return nil, groveerr.WrapError(err,
			errSyncPodCliqueSetService,
			component.OperationGetExistingResourceNames,
			fmt.Sprintf("Error listing Headless Services for PodCliqueSet: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	return k8sutils.FilterMapOwnedResourceNames(pcsObjMeta, serviceList.Items), nil
}

// Sync synchronizes all resources that the Service Operator manages.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, state *pcssync.State) (pcssync.Result, error) {
	existingServicesByPCSReplicaIndex, err := componentutils.GetHeadlessServicesByPCSReplicaIndex(ctx, r.client, pcs.ObjectMeta)
	if err != nil {
		return pcssync.Result{}, err
	}

	pcsReplicaIndicesAwaitingResourceCleanup, err := r.triggerDeletionOfExcessServices(ctx, logger, pcs, existingServicesByPCSReplicaIndex, state)
	if err != nil {
		return pcssync.Result{}, err
	}
	if err := r.createOrUpdateServices(ctx, logger, pcs, state.ExpectedReplicaIndices); err != nil {
		return pcssync.Result{}, err
	}

	return pcssync.Result{
		ScaleInReplicaIndicesAwaitingResourceCleanup: pcsReplicaIndicesAwaitingResourceCleanup,
	}, nil
}

// triggerDeletionOfExcessServices deletes Services outside the expected replica set and returns scale-in indices that still have Services.
func (r _resource) triggerDeletionOfExcessServices(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, servicesByPCSReplicaIndex map[int][]corev1.Service, state *pcssync.State) ([]int, error) {
	expectedPCSReplicaIndices := sets.New(state.ExpectedReplicaIndices...)
	scaleInPCSReplicaIndices := sets.New(state.ScaleInReplicaIndices...)
	var (
		deleteTasks                              []utils.Task
		pcsReplicaIndicesAwaitingResourceCleanup []int
	)
	for pcsReplicaIndex, services := range servicesByPCSReplicaIndex {
		if scaleInPCSReplicaIndices.Has(pcsReplicaIndex) && len(services) > 0 {
			pcsReplicaIndicesAwaitingResourceCleanup = append(pcsReplicaIndicesAwaitingResourceCleanup, pcsReplicaIndex)
		}
		if expectedPCSReplicaIndices.Has(pcsReplicaIndex) {
			continue
		}
		for _, service := range services {
			serviceObjectKey := client.ObjectKeyFromObject(&service)
			deleteTasks = append(deleteTasks, utils.Task{
				Name: fmt.Sprintf("DeletePodCliqueSetService-%s", serviceObjectKey),
				Fn: func(ctx context.Context) error {
					return client.IgnoreNotFound(r.client.Delete(ctx, emptyService(serviceObjectKey)))
				},
			})
		}
	}

	if runResult := utils.RunConcurrentlyAndWait(ctx, logger, deleteTasks); runResult.HasErrors() {
		return nil, groveerr.WrapError(runResult.GetAggregatedError(),
			errDeletePodCliqueSetService,
			component.OperationSync,
			fmt.Sprintf("Error deleting Headless Services for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	slices.Sort(pcsReplicaIndicesAwaitingResourceCleanup)
	return pcsReplicaIndicesAwaitingResourceCleanup, nil
}

// createOrUpdateServices creates or updates headless Services for the expected replica indices.
func (r _resource) createOrUpdateServices(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, expectedReplicaIndices []int) error {
	objectKeys := getObjectKeys(pcs, expectedReplicaIndices)
	tasks := make([]utils.Task, 0, len(objectKeys))
	for i, objectKey := range objectKeys {
		replicaIndex := expectedReplicaIndices[i]
		tasks = append(tasks, utils.Task{
			Name: fmt.Sprintf("CreateOrUpdatePodCliqueSetService-%s", objectKey),
			Fn: func(ctx context.Context) error {
				return r.doCreateOrUpdate(ctx, logger, pcs, replicaIndex, objectKey)
			},
		})
	}
	if runResult := utils.RunConcurrentlyAndWait(ctx, logger, tasks); runResult.HasErrors() {
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errSyncPodCliqueSetService,
			component.OperationSync,
			fmt.Sprintf("Error creating or updating PodCliqueSet Headless Services for PodCliqueSet: %v, run summary: %s", client.ObjectKeyFromObject(pcs), runResult.GetSummary()),
		)
	}
	logger.V(1).Info("Successfully synced Headless Services")
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
	logger.V(1).Info("Running CreateOrUpdate PodCliqueSet Headless Service", "pcsReplicaIndex", pcsReplicaIndex, "objectKey", svcObjectKey)
	svc := emptyService(svcObjectKey)
	opResult, err := k8sutils.CreateOrPatchSpec(ctx, r.client, svc, func() error {
		return r.buildResource(svc, pcs, pcsReplicaIndex)
	})
	if err != nil {
		return groveerr.WrapError(err,
			errSyncPodCliqueSetService,
			component.OperationSync,
			fmt.Sprintf("Error syncing Headless Service: %v for PodCliqueSet: %v", svcObjectKey, client.ObjectKeyFromObject(pcs)),
		)
	}
	logger.V(1).Info("Triggered create or update of PodGang Headless Service", "svcObjectKey", svcObjectKey, "result", opResult)
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

// getObjectKeys constructs object keys for headless Services for the given PCS replica indices.
func getObjectKeys(pcs *grovecorev1alpha1.PodCliqueSet, replicaIndices []int) []client.ObjectKey {
	objectKeys := make([]client.ObjectKey, 0, len(replicaIndices))
	for _, replicaIndex := range replicaIndices {
		serviceName := apicommon.GenerateHeadlessServiceName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replicaIndex})
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

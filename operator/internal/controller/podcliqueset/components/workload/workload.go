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
	"fmt"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	schedulingv1alpha1 "k8s.io/api/scheduling/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	errCodeListWorkloads                grovecorev1alpha1.ErrorCode = "ERR_LIST_WORKLOADS"
	errCodeDeleteWorkloads              grovecorev1alpha1.ErrorCode = "ERR_DELETE_WORKLOADS"
	errCodeDeleteExcessWorkload         grovecorev1alpha1.ErrorCode = "ERR_DELETE_EXCESS_WORKLOAD"
	errCodeListPods                     grovecorev1alpha1.ErrorCode = "ERR_LIST_PODS_FOR_PODCLIQUESET"
	errCodeListPodCliques               grovecorev1alpha1.ErrorCode = "ERR_LIST_PODCLIQUES_FOR_PODCLIQUESET"
	errCodeListPodCliqueScalingGroup    grovecorev1alpha1.ErrorCode = "ERR_LIST_PODCLIQUESCALINGGROUPS"
	errCodeComputeExistingWorkload      grovecorev1alpha1.ErrorCode = "ERR_COMPUTE_EXISTING_WORKLOAD"
	errCodeSetControllerReference       grovecorev1alpha1.ErrorCode = "ERR_SET_CONTROLLER_REFERENCE"
	errCodeCreateOrPatchWorkload        grovecorev1alpha1.ErrorCode = "ERR_CREATE_OR_PATCH_WORKLOAD"
)

type _resource struct {
	client        client.Client
	scheme        *runtime.Scheme
	eventRecorder record.EventRecorder
}

// New creates a new instance of Workload components operator.
func New(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder) component.Operator[grovecorev1alpha1.PodCliqueSet] {
	return &_resource{
		client:        client,
		scheme:        scheme,
		eventRecorder: eventRecorder,
	}
}

// GetExistingResourceNames returns the names of existing Workload resources for the PodCliqueSet.
func (r _resource) GetExistingResourceNames(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) ([]string, error) {
	logger.Info("Looking for existing Workload resources created per replica of PodCliqueSet")
	objMetaList := &metav1.PartialObjectMetadataList{}
	objMetaList.SetGroupVersionKind(workloadGVK())
	if err := r.client.List(ctx,
		objMetaList,
		client.InNamespace(pcsObjMeta.Namespace),
		client.MatchingLabels(componentutils.GetPodGangSelectorLabels(pcsObjMeta)),
	); err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListWorkloads,
			component.OperationGetExistingResourceNames,
			fmt.Sprintf("Error listing Workload for PodCliqueSet: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	return k8sutils.FilterMapOwnedResourceNames(pcsObjMeta, objMetaList.Items), nil
}

// workloadGVK returns the GroupVersionKind for Kubernetes Workload API (1.35+)
func workloadGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "scheduling.k8s.io",
		Version: "v1alpha1",
		Kind:    "Workload",
	}
}

// Sync creates, updates, or deletes Workload resources to match the desired state.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	logger.Info("Syncing Workload resources")
	sc, err := r.prepareSyncFlow(ctx, logger, pcs)
	if err != nil {
		return err
	}
	result := r.runSyncFlow(sc)
	if result.hasErrors() {
		return result.getAggregatedError()
	}
	if result.hasWorkloadsPendingCreation() {
		return groveerr.New(groveerr.ErrCodeRequeueAfter,
			component.OperationSync,
			fmt.Sprintf("Workloads pending creation: %v", result.workloadsPendingCreation),
		)
	}
	return nil
}

// Delete removes all Workload resources managed by the PodCliqueSet.
func (r _resource) Delete(ctx context.Context, logger logr.Logger, pcsObjectMeta metav1.ObjectMeta) error {
	logger.Info("Triggering deletion of Workloads")
	if err := r.client.DeleteAllOf(ctx,
		&schedulingv1alpha1.Workload{},
		client.InNamespace(pcsObjectMeta.Namespace),
		client.MatchingLabels(getWorkloadSelectorLabels(pcsObjectMeta))); err != nil {
		return groveerr.WrapError(err,
			errCodeDeleteWorkloads,
			component.OperationDelete,
			fmt.Sprintf("Failed to delete Workloads for PodCliqueSet: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjectMeta)),
		)
	}
	logger.Info("Deleted Workloads")
	return nil
}

// buildResource configures a Workload with pod groups.
func (r _resource) buildResource(pcs *grovecorev1alpha1.PodCliqueSet, wlInfo workloadInfo, wl *schedulingv1alpha1.Workload) error {
	wl.Labels = getLabels(pcs.Name)
	if err := controllerutil.SetControllerReference(pcs, wl, r.scheme); err != nil {
		return groveerr.WrapError(
			err,
			errCodeSetControllerReference,
			component.OperationSync,
			fmt.Sprintf("failed to set the controller reference on Workload %s to PodCliqueSet %v", wlInfo.fqn, client.ObjectKeyFromObject(pcs)),
		)
	}
	wl.Spec.PodGroups = createPodGroupsForWorkload(wlInfo)
	return nil
}

// getWorkloadSelectorLabels returns labels for selecting all Workloads of a PodCliqueSet.
func getWorkloadSelectorLabels(pcsObjMeta metav1.ObjectMeta) map[string]string {
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsObjMeta.Name),
		map[string]string{
			apicommon.LabelComponentKey: apicommon.LabelComponentNamePodGang,
		})
}

// emptyWorkload creates an empty Workload with only metadata set.
func emptyWorkload(objKey client.ObjectKey) *schedulingv1alpha1.Workload {
	wl := &schedulingv1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: objKey.Namespace,
			Name:      objKey.Name,
		},
	}
	return wl
}

// getLabels constructs labels for a Workload resource.
func getLabels(pcsName string) map[string]string {
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName),
		map[string]string{
			apicommon.LabelComponentKey: apicommon.LabelComponentNamePodGang,
		})
}


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
	"fmt"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	errCodeListPodGangs            grovecorev1alpha1.ErrorCode = "ERR_LIST_PODGANGS"
	errCodeDeletePodGangs          grovecorev1alpha1.ErrorCode = "ERR_DELETE_PODGANGS"
	errCodeDeleteExcessPodGang     grovecorev1alpha1.ErrorCode = "ERR_DELETE_EXCESS_PODGANG"
	errCodeListPods                grovecorev1alpha1.ErrorCode = "ERR_LIST_PODS_FOR_PODCLIQUESET"
	errCodeListPodCliques          grovecorev1alpha1.ErrorCode = "ERR_LIST_PODCLIQUES_FOR_PODCLIQUESET"
	errCodeComputeExistingPodGangs grovecorev1alpha1.ErrorCode = "ERR_COMPUTE_EXISTING_PODGANG"
	errCodeSetControllerReference  grovecorev1alpha1.ErrorCode = "ERR_SET_CONTROLLER_REFERENCE"
	errCodeCreateOrPatchPodGang    grovecorev1alpha1.ErrorCode = "ERR_CREATE_OR_PATCH_PODGANG"
)

type _resource struct {
	client        client.Client
	scheme        *runtime.Scheme
	eventRecorder record.EventRecorder
}

// New creates a new instance of PodGang components operator.
func New(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder) component.Operator[grovecorev1alpha1.PodCliqueSet] {
	return &_resource{
		client:        client,
		scheme:        scheme,
		eventRecorder: eventRecorder,
	}
}

// GetExistingResourceNames returns the names of existing PodGang resources for the PodCliqueSet.
func (r _resource) GetExistingResourceNames(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) ([]string, error) {
	logger.Info("Looking for existing PodGang resources created per replica of PodCliqueSet")
	objMetaList := &metav1.PartialObjectMetadataList{}
	objMetaList.SetGroupVersionKind(groveschedulerv1alpha1.SchemeGroupVersion.WithKind("PodGang"))
	if err := r.client.List(ctx,
		objMetaList,
		client.InNamespace(pcsObjMeta.Namespace),
		client.MatchingLabels(componentutils.GetPodGangSelectorLabels(pcsObjMeta)),
	); err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPodGangs,
			component.OperationGetExistingResourceNames,
			fmt.Sprintf("Error listing PodGang for PodCliqueSet: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	return k8sutils.FilterMapOwnedResourceNames(pcsObjMeta, objMetaList.Items), nil
}

// Sync creates, updates, or deletes PodGang resources to match the desired state.
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	logger.Info("Syncing PodGang resources")
	sc, err := r.prepareSyncFlow(ctx, logger, pcs)
	if err != nil {
		return err
	}
	result := r.runSyncFlow(sc)
	if result.hasErrors() {
		return result.getAggregatedError()
	}
	if result.hasPodGangsPendingCreation() {
		return groveerr.New(groveerr.ErrCodeRequeueAfter,
			component.OperationSync,
			fmt.Sprintf("PodGangs pending creation: %v", result.podsGangsPendingCreation),
		)
	}
	return nil
}

// Delete removes all PodGang resources managed by the PodCliqueSet.
func (r _resource) Delete(ctx context.Context, logger logr.Logger, pcsObjectMeta metav1.ObjectMeta) error {
	logger.Info("Triggering deletion of PodGangs")
	if err := r.client.DeleteAllOf(ctx,
		&groveschedulerv1alpha1.PodGang{},
		client.InNamespace(pcsObjectMeta.Namespace),
		client.MatchingLabels(getPodGangSelectorLabels(pcsObjectMeta))); err != nil {
		return groveerr.WrapError(err,
			errCodeDeletePodGangs,
			component.OperationDelete,
			fmt.Sprintf("Failed to delete PodGangs for PodCliqueSet: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjectMeta)),
		)
	}
	logger.Info("Deleted PodGangs")
	return nil
}

// buildResource configures a PodGang with pod groups and priority.
func (r _resource) buildResource(pcs *grovecorev1alpha1.PodCliqueSet, pgInfo podGangInfo, pg *groveschedulerv1alpha1.PodGang) error {
	pg.Labels = getLabels(pcs)
	if err := controllerutil.SetControllerReference(pcs, pg, r.scheme); err != nil {
		return groveerr.WrapError(
			err,
			errCodeSetControllerReference,
			component.OperationSync,
			fmt.Sprintf("failed to set the controller reference on PodGang %s to PodCliqueSet %v", pgInfo.fqn, client.ObjectKeyFromObject(pcs)),
		)
	}
	pg.Spec.PodGroups = createPodGroupsForPodGang(pg.Namespace, pgInfo)
	pg.Spec.PriorityClassName = pcs.Spec.Template.PriorityClassName
	return nil
}

// getPodGangSelectorLabels returns labels for selecting all PodGangs of a PodCliqueSet.
func getPodGangSelectorLabels(pcsObjMeta metav1.ObjectMeta) map[string]string {
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsObjMeta.Name),
		map[string]string{
			apicommon.LabelComponentKey: apicommon.LabelComponentNamePodGang,
		})
}

// emptyPodGang creates an empty PodGang with only metadata set.
func emptyPodGang(objKey client.ObjectKey) *groveschedulerv1alpha1.PodGang {
	return &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: objKey.Namespace,
			Name:      objKey.Name,
		},
	}
}

// getLabels constructs labels for a PodGang resource.
func getLabels(pcs *grovecorev1alpha1.PodCliqueSet) map[string]string {
	labels := lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
		map[string]string{
			apicommon.LabelComponentKey: apicommon.LabelComponentNamePodGang,
		})

	// Add scheduler-backend label so Backend Controllers can identify which PodGang to handle
	// Check scheduler name from the first clique (all cliques should have same scheduler)
	schedulerName := ""
	if len(pcs.Spec.Template.Cliques) > 0 {
		schedulerName = pcs.Spec.Template.Cliques[0].Spec.PodSpec.SchedulerName
	}

	// Determine backend based on schedulerName
	if schedulerName == "" || schedulerName == "default-scheduler" {
		// Use Workload backend for default kube-scheduler
		labels[apicommon.LabelSchedulerBackend] = "workload"
	} else {
		// Use KAI backend for custom schedulers
		labels[apicommon.LabelSchedulerBackend] = "kai"
	}

	return labels
}

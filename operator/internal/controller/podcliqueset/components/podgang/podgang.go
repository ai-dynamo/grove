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
	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend"
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
	errCodeListPodGangs               grovecorev1alpha1.ErrorCode = "ERR_LIST_PODGANGS"
	errCodeDeletePodGangs             grovecorev1alpha1.ErrorCode = "ERR_DELETE_PODGANGS"
	errCodeDeleteExcessPodGang        grovecorev1alpha1.ErrorCode = "ERR_DELETE_EXCESS_PODGANG"
	errCodeListPods                   grovecorev1alpha1.ErrorCode = "ERR_LIST_PODS_FOR_PODCLIQUESET"
	errCodeListPodCliques             grovecorev1alpha1.ErrorCode = "ERR_LIST_PODCLIQUES_FOR_PODCLIQUESET"
	errCodeListPodCliqueScalingGroups grovecorev1alpha1.ErrorCode = "ERR_LIST_PODCLIQUESCALINGGROUPS_FOR_PODCLIQUESET"
	errCodeComputeExistingPodGangs    grovecorev1alpha1.ErrorCode = "ERR_COMPUTE_EXISTING_PODGANG"
	errCodeSetControllerReference     grovecorev1alpha1.ErrorCode = "ERR_SET_CONTROLLER_REFERENCE"
	errCodeCreateOrPatchPodGang       grovecorev1alpha1.ErrorCode = "ERR_CREATE_OR_PATCH_PODGANG"
	errCodeGetClusterTopologyLevels   grovecorev1alpha1.ErrorCode = "ERR_GET_CLUSTER_TOPOLOGY_LEVELS"
	errCodeUpdatePodGang              grovecorev1alpha1.ErrorCode = "ERR_UPDATE_PODGANG_WITH_POD_REFS"
)

type _resource struct {
	client        client.Client
	scheme        *runtime.Scheme
	eventRecorder record.EventRecorder
	tasConfig     configv1alpha1.TopologyAwareSchedulingConfiguration
}

// New creates a new instance of PodGang components operator.
func New(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, tasConfig configv1alpha1.TopologyAwareSchedulingConfiguration) component.Operator[grovecorev1alpha1.PodCliqueSet] {
	return &_resource{
		client:        client,
		scheme:        scheme,
		eventRecorder: eventRecorder,
		tasConfig:     tasConfig,
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
// NEW FLOW: PodGangs are created with empty podReferences before Pods are created.
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
func (r _resource) buildResource(pcs *grovecorev1alpha1.PodCliqueSet, pgi *podGangInfo, pg *groveschedulerv1alpha1.PodGang) error {
	pg.Labels = getLabels(pcs.Name)
	// Set scheduler name so the podgang controller can resolve the correct backend
	if schedName := getSchedulerNameForPCS(pcs); schedName != "" {
		if pg.Labels == nil {
			pg.Labels = make(map[string]string)
		}
		pg.Labels[apicommon.LabelSchedulerName] = schedName
	}
	if r.tasConfig.Enabled {
		if pg.Annotations == nil {
			pg.Annotations = make(map[string]string)
		}
		pg.Annotations[apicommonconstants.AnnotationTopologyName] = grovecorev1alpha1.DefaultClusterTopologyName
	}
	if err := controllerutil.SetControllerReference(pcs, pg, r.scheme); err != nil {
		return groveerr.WrapError(
			err,
			errCodeSetControllerReference,
			component.OperationSync,
			fmt.Sprintf("failed to set the controller reference on PodGang %s to PodCliqueSet %v", pgi.fqn, client.ObjectKeyFromObject(pcs)),
		)
	}
	pg.Spec.PriorityClassName = pcs.Spec.Template.PriorityClassName
	pg.Spec.TopologyConstraint = pgi.topologyConstraint
	pg.Spec.TopologyConstraintGroupConfigs = pgi.pcsgTopologyConstraints

	// Only create PodGroups if they don't exist yet (initial creation)
	// Once populated, we preserve existing podReferences to avoid clearing them on subsequent reconciles
	if len(pg.Spec.PodGroups) == 0 {
		// Create PodGroups with EMPTY podReferences initially
		pg.Spec.PodGroups = createEmptyPodGroupsForPodGang(*pgi)
	} else {
		// PodGroups already exist - preserve them but update MinReplicas and TopologyConstraint if needed
		expectedPodGroups := make(map[string]struct {
			minAvailable       int32
			topologyConstraint *groveschedulerv1alpha1.TopologyConstraint
		})
		for _, pclq := range pgi.pclqs {
			expectedPodGroups[pclq.fqn] = struct {
				minAvailable       int32
				topologyConstraint *groveschedulerv1alpha1.TopologyConstraint
			}{
				minAvailable:       pclq.minAvailable,
				topologyConstraint: pclq.topologyConstraint,
			}
		}

		// Update MinReplicas and TopologyConstraint for existing PodGroups
		for i := range pg.Spec.PodGroups {
			podGroup := &pg.Spec.PodGroups[i]
			if expectedPG, ok := expectedPodGroups[podGroup.Name]; ok {
				podGroup.MinReplicas = expectedPG.minAvailable
				podGroup.TopologyConstraint = expectedPG.topologyConstraint
			}
		}
	}

	return nil
}

// createEmptyPodGroupsForPodGang creates PodGroups with empty podReferences.
// These will be populated later when pods are created.
func createEmptyPodGroupsForPodGang(pgInfo podGangInfo) []groveschedulerv1alpha1.PodGroup {
	podGroups := lo.Map(pgInfo.pclqs, func(pclq pclqInfo, _ int) groveschedulerv1alpha1.PodGroup {
		return groveschedulerv1alpha1.PodGroup{
			Name:               pclq.fqn,
			PodReferences:      []groveschedulerv1alpha1.NamespacedName{},
			MinReplicas:        pclq.minAvailable,
			TopologyConstraint: pclq.topologyConstraint,
		}
	})
	return podGroups
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
func getLabels(pcsName string) map[string]string {
	return lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsName),
		map[string]string{
			apicommon.LabelComponentKey: apicommon.LabelComponentNamePodGang,
		})
}

// getSchedulerNameForPCS returns the scheduler backend name for the PodCliqueSet:
// the template's schedulerName if set (same across all cliques per validation), else the default backend.
func getSchedulerNameForPCS(pcs *grovecorev1alpha1.PodCliqueSet) string {
	for _, c := range pcs.Spec.Template.Cliques {
		if c != nil && c.Spec.PodSpec.SchedulerName != "" {
			return c.Spec.PodSpec.SchedulerName
		}
	}
	if def := schedulerbackend.GetDefault(); def != nil {
		return def.Name()
	}
	return ""
}

// setInitializedCondition sets or updates the PodGangInitialized condition on the PodGang status.
func setInitializedCondition(pg *groveschedulerv1alpha1.PodGang, status metav1.ConditionStatus, reason, message string) {
	condition := metav1.Condition{
		Type:               string(groveschedulerv1alpha1.PodGangConditionTypeInitialized),
		Status:             status,
		ObservedGeneration: pg.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}

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

// hasInitializedCondition returns true if the PodGang has an Initialized condition.
func hasInitializedCondition(pg *groveschedulerv1alpha1.PodGang) bool {
	for _, cond := range pg.Status.Conditions {
		if cond.Type == string(groveschedulerv1alpha1.PodGangConditionTypeInitialized) {
			return true
		}
	}
	return false
}

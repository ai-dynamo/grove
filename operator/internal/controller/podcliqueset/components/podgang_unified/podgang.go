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

package podgang_unified

import (
	"context"
	"fmt"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	"github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	errCodeListPodGangs       grovecorev1alpha1.ErrorCode = "ERR_LIST_PODGANGS"
	errCodeDeletePodGangs     grovecorev1alpha1.ErrorCode = "ERR_DELETE_PODGANGS"
	errCodeCreateOrPatchPodGang grovecorev1alpha1.ErrorCode = "ERR_CREATE_OR_PATCH_PODGANG"
)

type _resource struct {
	client        client.Client
	scheme        *runtime.Scheme
	eventRecorder record.EventRecorder
}

// New creates a new unified PodGang component operator
// This component ALWAYS creates PodGang regardless of schedulerName
func New(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder) component.Operator[grovecorev1alpha1.PodCliqueSet] {
	return &_resource{
		client:        client,
		scheme:        scheme,
		eventRecorder: eventRecorder,
	}
}

// GetExistingResourceNames returns the names of existing PodGang resources
func (r _resource) GetExistingResourceNames(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) ([]string, error) {
	logger.Info("Looking for existing PodGang resources")
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

// Sync creates, updates, or deletes PodGang resources
// ALWAYS creates PodGang with appropriate backend label
func (r _resource) Sync(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) error {
	logger.Info("Syncing unified PodGang resources (always created)")
	
	// Build gang info for all replicas
	builder := backend.NewGangInfoBuilder(r.client)
	gangInfos, err := builder.BuildGangInfos(ctx, pcs)
	if err != nil {
		return fmt.Errorf("failed to build gang infos: %w", err)
	}
	
	// Determine which backend should handle this PCS
	backendLabel := r.determineBackendLabel(pcs)
	logger.Info("Determined backend for PodCliqueSet", "backend", backendLabel)
	
	// Create or update PodGangs
	for _, gangInfo := range gangInfos {
		if err := r.createOrUpdatePodGang(ctx, logger, pcs, gangInfo, backendLabel); err != nil {
			return fmt.Errorf("failed to create/update PodGang %s: %w", gangInfo.Name, err)
		}
	}
	
	// Delete excess PodGangs (if replicas decreased)
	if err := r.deleteExcessPodGangs(ctx, logger, pcs, gangInfos); err != nil {
		return fmt.Errorf("failed to delete excess PodGangs: %w", err)
	}
	
	logger.Info("Successfully synced PodGang resources", "count", len(gangInfos))
	return nil
}

// Delete removes all PodGang resources for the PodCliqueSet
func (r _resource) Delete(ctx context.Context, logger logr.Logger, pcsObjMeta metav1.ObjectMeta) error {
	logger.Info("Deleting PodGang resources")
	
	if err := r.client.DeleteAllOf(ctx, &groveschedulerv1alpha1.PodGang{},
		client.InNamespace(pcsObjMeta.Namespace),
		client.MatchingLabels(componentutils.GetPodGangSelectorLabels(pcsObjMeta)),
	); err != nil {
		return groveerr.WrapError(err,
			errCodeDeletePodGangs,
			component.OperationDelete,
			fmt.Sprintf("Failed to delete PodGangs for PodCliqueSet: %v", k8sutils.GetObjectKeyFromObjectMeta(pcsObjMeta)),
		)
	}
	
	logger.Info("Deleted PodGangs")
	return nil
}

// determineBackendLabel determines which backend label to use based on schedulerName
func (r _resource) determineBackendLabel(pcs *grovecorev1alpha1.PodCliqueSet) string {
	// Check schedulerName from first clique (assuming all cliques use same scheduler)
	if len(pcs.Spec.Template.Cliques) == 0 {
		return "default" // fallback
	}
	
	schedulerName := pcs.Spec.Template.Cliques[0].Spec.PodSpec.SchedulerName
	
	// Map schedulerName to backend label
	if schedulerName == "" || schedulerName == "default-scheduler" {
		return "default"
	}
	
	// KAI/Grove scheduler
	if schedulerName == "kai-scheduler" || schedulerName == "grove-scheduler" {
		return "kai"
	}
	
	// Koordinator scheduler
	if schedulerName == "koord-scheduler" {
		return "koordinator"
	}
	
	// Default to KAI for any other custom scheduler
	return "kai"
}

// createOrUpdatePodGang creates or updates a single PodGang
func (r _resource) createOrUpdatePodGang(
	ctx context.Context,
	logger logr.Logger,
	pcs *grovecorev1alpha1.PodCliqueSet,
	gangInfo *backend.PodGangInfo,
	backendLabel string,
) error {
	podGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gangInfo.Name,
			Namespace: gangInfo.Namespace,
		},
	}
	
	logger.Info("CreateOrPatch PodGang", "name", gangInfo.Name)
	
	_, err := controllerutil.CreateOrPatch(ctx, r.client, podGang, func() error {
		return r.buildPodGang(pcs, gangInfo, backendLabel, podGang)
	})
	
	if err != nil {
		r.eventRecorder.Eventf(pcs, corev1.EventTypeWarning, "PodGangCreateOrUpdateFailed",
			"Error creating/updating PodGang %s: %v", gangInfo.Name, err)
		return groveerr.WrapError(err,
			errCodeCreateOrPatchPodGang,
			component.OperationSync,
			fmt.Sprintf("Failed to CreateOrPatch PodGang %s", gangInfo.Name),
		)
	}
	
	r.eventRecorder.Eventf(pcs, corev1.EventTypeNormal, "PodGangCreateOrUpdateSuccessful",
		"Created/Updated PodGang %s", gangInfo.Name)
	logger.Info("Successfully created/updated PodGang", "name", gangInfo.Name)
	
	return nil
}

// buildPodGang builds the PodGang resource
func (r _resource) buildPodGang(
	pcs *grovecorev1alpha1.PodCliqueSet,
	gangInfo *backend.PodGangInfo,
	backendLabel string,
	podGang *groveschedulerv1alpha1.PodGang,
) error {
	// Set labels
	if podGang.Labels == nil {
		podGang.Labels = make(map[string]string)
	}
	
	// Add default labels
	for k, v := range apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name) {
		podGang.Labels[k] = v
	}
	podGang.Labels[apicommon.LabelComponentKey] = apicommon.LabelComponentNamePodGang
	
	// CRITICAL: Set backend label to direct traffic to correct backend
	podGang.Labels["grove.io/scheduler-backend"] = backendLabel
	
	// Set owner reference
	if err := controllerutil.SetControllerReference(pcs, podGang, r.scheme); err != nil {
		return fmt.Errorf("failed to set controller reference: %w", err)
	}
	
	// Build spec from gangInfo
	podGang.Spec = r.buildPodGangSpec(gangInfo)
	
	return nil
}

// buildPodGangSpec builds PodGangSpec from gangInfo
func (r _resource) buildPodGangSpec(gangInfo *backend.PodGangInfo) groveschedulerv1alpha1.PodGangSpec {
	spec := groveschedulerv1alpha1.PodGangSpec{
		PodGroups:         make([]groveschedulerv1alpha1.PodGroup, len(gangInfo.PodGroups)),
		PriorityClassName: gangInfo.PriorityClassName,
	}
	
	// Convert PodGroups
	for i, pg := range gangInfo.PodGroups {
		podRefs := make([]groveschedulerv1alpha1.NamespacedName, len(pg.PodReferences))
		for j, ref := range pg.PodReferences {
			podRefs[j] = groveschedulerv1alpha1.NamespacedName{
				Namespace: ref.Namespace,
				Name:      ref.Name,
			}
		}
		
		spec.PodGroups[i] = groveschedulerv1alpha1.PodGroup{
			Name:          pg.Name,
			PodReferences: podRefs,
			MinReplicas:   pg.MinReplicas,
			TopologyConstraint: r.convertTopologyConstraint(pg.TopologyConstraint),
		}
	}
	
	// Convert topology constraints
	spec.TopologyConstraint = r.convertTopologyConstraint(gangInfo.TopologyConstraint)
	
	// Convert topology group configs
	if len(gangInfo.TopologyConstraintGroupConfigs) > 0 {
		spec.TopologyConstraintGroupConfigs = make([]groveschedulerv1alpha1.TopologyConstraintGroupConfig, len(gangInfo.TopologyConstraintGroupConfigs))
		for i, tgc := range gangInfo.TopologyConstraintGroupConfigs {
			spec.TopologyConstraintGroupConfigs[i] = groveschedulerv1alpha1.TopologyConstraintGroupConfig{
				PodGroupNames:      tgc.PodGroupNames,
				TopologyConstraint: r.convertTopologyConstraint(tgc.TopologyConstraint),
			}
		}
	}
	
	// Set reuse reservation ref
	if gangInfo.ReuseReservationRef != nil {
		spec.ReuseReservationRef = &groveschedulerv1alpha1.NamespacedName{
			Namespace: gangInfo.ReuseReservationRef.Namespace,
			Name:      gangInfo.ReuseReservationRef.Name,
		}
	}
	
	return spec
}

// convertTopologyConstraint converts backend topology constraint to PodGang format
func (r _resource) convertTopologyConstraint(tc *backend.TopologyConstraint) *groveschedulerv1alpha1.TopologyConstraint {
	if tc == nil {
		return nil
	}
	
	result := &groveschedulerv1alpha1.TopologyConstraint{}
	
	if tc.PackConstraint != nil {
		result.PackConstraint = &groveschedulerv1alpha1.TopologyPackConstraint{
			Required:  tc.PackConstraint.Required,
			Preferred: tc.PackConstraint.Preferred,
		}
	}
	
	return result
}

// deleteExcessPodGangs deletes PodGangs that are no longer needed
func (r _resource) deleteExcessPodGangs(
	ctx context.Context,
	logger logr.Logger,
	pcs *grovecorev1alpha1.PodCliqueSet,
	gangInfos []*backend.PodGangInfo,
) error {
	// Get existing PodGangs
	podGangList := &groveschedulerv1alpha1.PodGangList{}
	selector := labels.SelectorFromSet(componentutils.GetPodGangSelectorLabels(pcs.ObjectMeta))
	
	err := r.client.List(ctx, podGangList,
		client.InNamespace(pcs.Namespace),
		client.MatchingLabelsSelector{Selector: selector},
	)
	if err != nil {
		return fmt.Errorf("failed to list existing PodGangs: %w", err)
	}
	
	// Build expected names
	expectedNames := make(map[string]bool)
	for _, gangInfo := range gangInfos {
		expectedNames[gangInfo.Name] = true
	}
	
	// Delete excess
	for _, existing := range podGangList.Items {
		if !expectedNames[existing.Name] {
			logger.Info("Deleting excess PodGang", "name", existing.Name)
			if err := r.client.Delete(ctx, &existing); err != nil {
				return fmt.Errorf("failed to delete excess PodGang %s: %w", existing.Name, err)
			}
		}
	}
	
	return nil
}


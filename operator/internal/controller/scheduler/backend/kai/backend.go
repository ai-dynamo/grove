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

package kai

import (
	"context"
	"fmt"

	"github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// BackendName is the name of the KAI backend
	BackendName = "kai"
	// SchedulingGateName is the name of the scheduling gate used by KAI
	SchedulingGateName = "grove.io/podgang"
	// BackendLabelValue is the label value to identify KAI backend
	BackendLabelValue = "kai"
)

// PodGroup API constants (run.ai format)
const (
	PodGroupAPIGroup   = "scheduling.run.ai"
	PodGroupAPIVersion = "v2alpha2"
	PodGroupKind       = "PodGroup"
)

// Backend implements the SchedulerBackend interface for KAI scheduler
// Converts PodGang → PodGroup (scheduling.run.ai/v2alpha2 format, similar to posgroups.yaml)
type Backend struct {
	client        client.Client
	scheme        *runtime.Scheme
	eventRecorder record.EventRecorder
}

// Factory creates KAI backend instances
// New creates a new KAI backend instance
// This is exported for direct use by components that need a backend with client access
func New(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder) *Backend {
	return &Backend{
		client:        cl,
		scheme:        scheme,
		eventRecorder: eventRecorder,
	}
}

// init registers the KAI backend factory
func init() {
	// Register factory for kai-scheduler and grove-scheduler
	factory := func(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder) backend.SchedulerBackend {
		return New(cl, scheme, eventRecorder)
	}
	backend.RegisterBackendFactory("kai-scheduler", factory)
	backend.RegisterBackendFactory("grove-scheduler", factory)
}

// Name returns the backend name
func (b *Backend) Name() string {
	return BackendName
}

// Matches returns true if this PodGang should be handled by KAI backend
// Checks for label: grove.io/scheduler-backend: kai
func (b *Backend) Matches(podGang *groveschedulerv1alpha1.PodGang) bool {
	if podGang.Labels == nil {
		return false
	}
	return podGang.Labels["grove.io/scheduler-backend"] == BackendLabelValue
}

// Sync converts PodGang to KAI PodGroup (similar to posgroups.yaml format)
func (b *Backend) Sync(ctx context.Context, logger logr.Logger, podGang *groveschedulerv1alpha1.PodGang) error {
	logger.Info("Syncing PodGang to KAI PodGroup", "podGang", podGang.Name)

	// Convert PodGang to PodGroup
	podGroup := b.convertPodGangToPodGroup(podGang)

	// Create or update PodGroup
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(podGroupGVK())

	err := b.client.Get(ctx, client.ObjectKey{
		Namespace: podGroup.GetNamespace(),
		Name:      podGroup.GetName(),
	}, existing)

	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to get existing PodGroup: %w", err)
		}

		// Create new PodGroup
		logger.Info("Creating KAI PodGroup", "name", podGroup.GetName())
		if err := b.client.Create(ctx, podGroup); err != nil {
			return fmt.Errorf("failed to create PodGroup: %w", err)
		}
		return nil
	}

	// Update existing PodGroup
	podGroup.SetResourceVersion(existing.GetResourceVersion())
	logger.Info("Updating KAI PodGroup", "name", podGroup.GetName())
	if err := b.client.Update(ctx, podGroup); err != nil {
		return fmt.Errorf("failed to update PodGroup: %w", err)
	}

	return nil
}

// Delete removes the PodGroup owned by this PodGang
func (b *Backend) Delete(ctx context.Context, logger logr.Logger, podGang *groveschedulerv1alpha1.PodGang) error {
	logger.Info("Deleting KAI PodGroup", "podGang", podGang.Name)

	podGroup := &unstructured.Unstructured{}
	podGroup.SetGroupVersionKind(podGroupGVK())
	podGroup.SetName(b.getPodGroupName(podGang))
	podGroup.SetNamespace(podGang.Namespace)

	if err := b.client.Delete(ctx, podGroup); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to delete PodGroup: %w", err)
	}

	return nil
}

// CheckReady checks if the PodGroup is ready for scheduling
func (b *Backend) CheckReady(ctx context.Context, logger logr.Logger, podGang *groveschedulerv1alpha1.PodGang) (bool, string, error) {
	podGroupName := b.getPodGroupName(podGang)

	podGroup := &unstructured.Unstructured{}
	podGroup.SetGroupVersionKind(podGroupGVK())

	err := b.client.Get(ctx, client.ObjectKey{
		Namespace: podGang.Namespace,
		Name:      podGroupName,
	}, podGroup)

	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			// PodGroup doesn't exist yet - not ready
			return false, podGroupName, nil
		}
		return false, podGroupName, fmt.Errorf("failed to get PodGroup: %w", err)
	}

	// Check PodGroup status
	// In run.ai PodGroup, check status.schedulingConditions
	conditions, found, _ := unstructured.NestedSlice(podGroup.Object, "status", "schedulingConditions")
	if !found || len(conditions) == 0 {
		return false, podGroupName, nil
	}

	// Check if any condition indicates readiness
	// For now, if PodGroup exists and has conditions, consider it being processed
	isReady := false // TODO: Implement proper readiness check based on conditions

	logger.V(1).Info("Checked KAI PodGroup readiness",
		"podGroupName", podGroupName,
		"isReady", isReady)

	return isReady, podGroupName, nil
}

// GetSchedulingGateName returns the scheduling gate name used by this backend
func (b *Backend) GetSchedulingGateName() string {
	return SchedulingGateName
}

// ShouldRemoveSchedulingGate checks if the PodGang is ready for this PodClique
// For KAI/Grove scheduler:
// - Base PodGang pods: remove gate immediately after pod is created
// - Scaled PodGang pods: remove gate when the base PodGang is scheduled
func (b *Backend) ShouldRemoveSchedulingGate(ctx context.Context, logger logr.Logger, pod *corev1.Pod, podCliqueObj client.Object) (bool, string, error) {
	podClique, ok := podCliqueObj.(*grovecorev1alpha1.PodClique)
	if !ok {
		return false, "", fmt.Errorf("expected PodClique object, got %T", podCliqueObj)
	}

	// Check if this PodClique has a base PodGang dependency
	basePodGangName, hasBasePodGangLabel := podClique.GetLabels()[common.LabelBasePodGang]
	if !hasBasePodGangLabel {
		// This is a base PodGang pod - remove gate immediately
		logger.Info("Proceeding with gate removal for base PodGang pod",
			"podObjectKey", client.ObjectKeyFromObject(pod))
		return true, "base podgang pod", nil
	}

	// This is a scaled PodGang pod - check if base PodGang is scheduled
	basePodGang := &groveschedulerv1alpha1.PodGang{}
	basePodGangKey := client.ObjectKey{Name: basePodGangName, Namespace: podClique.Namespace}
	if err := b.client.Get(ctx, basePodGangKey, basePodGang); err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("Base PodGang not found yet", "basePodGangName", basePodGangName)
			return false, "base podgang not found", nil
		}
		return false, "", fmt.Errorf("failed to get base PodGang %v: %w", basePodGangKey, err)
	}

	// Check if all PodGroups in the base PodGang have sufficient scheduled replicas
	for _, podGroup := range basePodGang.Spec.PodGroups {
		pclqName := podGroup.Name
		pclq := &grovecorev1alpha1.PodClique{}
		pclqKey := client.ObjectKey{Name: pclqName, Namespace: podClique.Namespace}
		if err := b.client.Get(ctx, pclqKey, pclq); err != nil {
			return false, "", fmt.Errorf("failed to get PodClique %s for base PodGang readiness check: %w", pclqName, err)
		}

		// Check if MinReplicas is satisfied (use ScheduledReplicas for base PodGang check)
		if pclq.Status.ScheduledReplicas < podGroup.MinReplicas {
			logger.Info("Scaled PodGang pod has scheduling gate but base PodGang is not ready yet",
				"podObjectKey", client.ObjectKeyFromObject(pod),
				"basePodGangName", basePodGangName,
				"pclqName", pclqName,
				"scheduledReplicas", pclq.Status.ScheduledReplicas,
				"minReplicas", podGroup.MinReplicas)
			return false, fmt.Sprintf("base podgang not ready: %s has %d/%d scheduled pods", pclqName, pclq.Status.ScheduledReplicas, podGroup.MinReplicas), nil
		}
	}

	logger.Info("Base PodGang is ready, proceeding with gate removal for scaled PodGang pod",
		"podObjectKey", client.ObjectKeyFromObject(pod),
		"basePodGangName", basePodGangName)
	return true, "base podgang ready", nil
}

// MutatePodSpec mutates the Pod spec to add KAI-specific requirements
// For KAI scheduler, we don't need WorkloadRef (KAI reads PodGang directly)
// But we can add helpful annotations for tracking
func (b *Backend) MutatePodSpec(pod *corev1.Pod, gangName string, podGroupName string) error {
	// KAI scheduler consumes PodGang directly, so we don't need WorkloadRef
	// We can add annotations for better observability
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}

	// Add annotations to help track the gang membership
	pod.Annotations["kai.scheduler/podgang"] = gangName
	pod.Annotations["kai.scheduler/podgroup"] = podGroupName

	return nil
}

// convertPodGangToPodGroup converts PodGang to KAI PodGroup (run.ai format)
// Format similar to posgroups.yaml
func (b *Backend) convertPodGangToPodGroup(podGang *groveschedulerv1alpha1.PodGang) *unstructured.Unstructured {
	podGroup := &unstructured.Unstructured{}
	podGroup.SetGroupVersionKind(podGroupGVK())
	podGroup.SetName(b.getPodGroupName(podGang))
	podGroup.SetNamespace(podGang.Namespace)

	// Set labels
	labels := make(map[string]string)
	for k, v := range podGang.Labels {
		labels[k] = v
	}
	podGroup.SetLabels(labels)

	// Set owner reference
	ownerRef := metav1.OwnerReference{
		APIVersion: groveschedulerv1alpha1.SchemeGroupVersion.String(),
		Kind:       "PodGang",
		Name:       podGang.Name,
		UID:        podGang.UID,
	}
	podGroup.SetOwnerReferences([]metav1.OwnerReference{ownerRef})

	// Set annotations with top owner metadata (as in posgroups.yaml)
	annotations := map[string]string{
		"kai.scheduler/top-owner-metadata": fmt.Sprintf(`name: %s
uid: %s
group: %s
version: %s
kind: %s`,
			podGang.Name,
			podGang.UID,
			groveschedulerv1alpha1.SchemeGroupVersion.Group,
			groveschedulerv1alpha1.SchemeGroupVersion.Version,
			"PodGang",
		),
	}
	podGroup.SetAnnotations(annotations)

	// Build spec
	spec := make(map[string]interface{})

	// Calculate total minMember
	var totalMinMember int32
	for _, pg := range podGang.Spec.PodGroups {
		totalMinMember += pg.MinReplicas
	}
	spec["minMember"] = totalMinMember

	// Add priority class name if present
	if podGang.Spec.PriorityClassName != "" {
		spec["priorityClassName"] = podGang.Spec.PriorityClassName
	}

	// Add queue (default to "default-queue" as in posgroups.yaml)
	spec["queue"] = "default-queue"

	// Build subGroups from PodGang.Spec.PodGroups
	subGroups := make([]interface{}, 0, len(podGang.Spec.PodGroups))
	for _, pg := range podGang.Spec.PodGroups {
		subGroup := map[string]interface{}{
			"name":      pg.Name,
			"minMember": pg.MinReplicas,
		}
		subGroups = append(subGroups, subGroup)
	}
	spec["subGroups"] = subGroups

	// Add topology constraint if present
	if podGang.Spec.TopologyConstraint != nil {
		spec["topologyConstraint"] = convertTopologyConstraint(podGang.Spec.TopologyConstraint)
	} else {
		spec["topologyConstraint"] = map[string]interface{}{}
	}

	unstructured.SetNestedMap(podGroup.Object, spec, "spec")

	return podGroup
}

// convertTopologyConstraint converts Grove topology constraint to KAI format
func convertTopologyConstraint(tc *groveschedulerv1alpha1.TopologyConstraint) map[string]interface{} {
	result := make(map[string]interface{})

	if tc.PackConstraint != nil {
		if tc.PackConstraint.Required != nil {
			result["required"] = *tc.PackConstraint.Required
		}
		if tc.PackConstraint.Preferred != nil {
			result["preferred"] = *tc.PackConstraint.Preferred
		}
	}

	return result
}

// getPodGroupName generates PodGroup name from PodGang
// Format: pg-{podGangName}-{uid} (as in posgroups.yaml)
func (b *Backend) getPodGroupName(podGang *groveschedulerv1alpha1.PodGang) string {
	return fmt.Sprintf("pg-%s-%s", podGang.Name, podGang.UID)
}

// podGroupGVK returns the GroupVersionKind for KAI PodGroup
func podGroupGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   PodGroupAPIGroup,
		Version: PodGroupAPIVersion,
		Kind:    PodGroupKind,
	}
}

// NOTE: Registration is done in controller/manager.go to avoid circular imports

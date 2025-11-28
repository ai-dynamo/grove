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
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
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
	schedulerName string // The scheduler name from configuration
}

// New creates a new KAI backend instance
func New(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, schedulerName string) *Backend {
	return &Backend{
		client:        cl,
		scheme:        scheme,
		eventRecorder: eventRecorder,
		schedulerName: schedulerName,
	}
}

// Name returns the backend name
func (b *Backend) Name() string {
	return BackendName
}

// Init initializes the KAI backend
// For KAI backend, no special initialization is needed currently
func (b *Backend) Init() error {
	return nil
}

// SyncPodGang converts PodGang to KAI PodGroup and synchronizes it
// TODO: Currently disabled - will be implemented in phase 2
func (b *Backend) SyncPodGang(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error {
	// Phase 1: Skip PodGroup creation/update
	// Phase 2: Will convert PodGang to PodGroup and synchronize
	return nil

	// Convert PodGang to PodGroup (disabled)
	// podGroup := b.convertPodGangToPodGroup(podGang)
	//
	// // Create or update PodGroup
	// existing := &unstructured.Unstructured{}
	// existing.SetGroupVersionKind(podGroupGVK())
	//
	// err := b.client.Get(ctx, client.ObjectKey{
	// 	Namespace: podGroup.GetNamespace(),
	// 	Name:      podGroup.GetName(),
	// }, existing)
	//
	// if err != nil {
	// 	if client.IgnoreNotFound(err) != nil {
	// 		return fmt.Errorf("failed to get existing PodGroup: %w", err)
	// 	}
	//
	// 	// Create new PodGroup
	// 	if err := b.client.Create(ctx, podGroup); err != nil {
	// 		return fmt.Errorf("failed to create PodGroup: %w", err)
	// 	}
	// 	return nil
	// }
	//
	// // Update existing PodGroup
	// podGroup.SetResourceVersion(existing.GetResourceVersion())
	// if err := b.client.Update(ctx, podGroup); err != nil {
	// 	return fmt.Errorf("failed to update PodGroup: %w", err)
	// }
	//
	// return nil
}

// OnPodGangDelete removes the PodGroup owned by this PodGang
// TODO: Currently disabled - will be implemented in phase 2
func (b *Backend) OnPodGangDelete(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error {
	// Phase 1: Skip PodGroup deletion
	// Phase 2: Will delete PodGroup when PodGang is deleted
	return nil

	// Delete PodGroup (disabled)
	// podGroup := &unstructured.Unstructured{}
	// podGroup.SetGroupVersionKind(podGroupGVK())
	// podGroup.SetName(b.getPodGroupName(podGang))
	// podGroup.SetNamespace(podGang.Namespace)
	//
	// if err := b.client.Delete(ctx, podGroup); err != nil {
	// 	if client.IgnoreNotFound(err) == nil {
	// 		return nil // Already deleted
	// 	}
	// 	return fmt.Errorf("failed to delete PodGroup: %w", err)
	// }
	//
	// return nil
}

// PreparePod adds KAI scheduler-specific configuration to the Pod
// This includes: schedulerName, scheduling gates, and annotations
func (b *Backend) PreparePod(pod *corev1.Pod) {
	// Set scheduler name from configuration
	pod.Spec.SchedulerName = b.schedulerName

	// NOTE: We don't set schedulingGates here because KAI scheduler's mutating webhook
	// will add them automatically when it intercepts the Pod creation.
	// Grove operator will remove the gates later when PodGang is initialized.
	//
	// pod.Spec.SchedulingGates = []corev1.PodSchedulingGate{
	// 	{Name: SchedulingGateName},
	// }

	// Add annotations for observability
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}

	// Get PodGang and PodGroup names from labels
	if podGangName, ok := pod.Labels[common.LabelPodGang]; ok {
		pod.Annotations["kai.scheduler/podgang"] = podGangName
	}
	if podCliqueName, ok := pod.Labels[common.LabelPodClique]; ok {
		pod.Annotations["kai.scheduler/podgroup"] = podCliqueName
	}
}

// convertPodGangToPodGroup converts PodGang to KAI PodGroup (run.ai format)
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
	spec["minMember"] = int64(totalMinMember) // Convert int32 to int64 for unstructured

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
			"minMember": int64(pg.MinReplicas), // Convert int32 to int64 for unstructured
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

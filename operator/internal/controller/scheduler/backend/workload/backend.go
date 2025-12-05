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

	"github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha1 "k8s.io/api/scheduling/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// BackendName is the name of the Workload backend
	BackendName = "workload"
	// SchedulingGateName is the name of the scheduling gate used by Workload API
	SchedulingGateName = "scheduling.k8s.io/workload"
	// BackendLabelValue is the label value to identify Workload backend
	// Must match the label set in podgang component: grove.io/scheduler-backend=workload
	BackendLabelValue = "workload"
)

// Backend implements the SchedulerBackend interface for default kube-scheduler using Workload API
// Converts PodGang → Workload (scheduling.k8s.io/v1alpha1)
type Backend struct {
	client        client.Client
	scheme        *runtime.Scheme
	eventRecorder record.EventRecorder
}

// Factory creates Workload backend instances
type Factory struct{}

// CreateBackend creates a new Workload backend
func (f *Factory) CreateBackend(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder) (backend.SchedulerBackend, error) {
	return New(cl, scheme, eventRecorder), nil
}

// New creates a new Workload backend instance
// This is exported for direct use by components that need a backend with client access
func New(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder) *Backend {
	return &Backend{
		client:        cl,
		scheme:        scheme,
		eventRecorder: eventRecorder,
	}
}

// Name returns the backend name
func (b *Backend) Name() string {
	return BackendName
}

// Matches returns true if this PodGang should be handled by Workload backend
// Checks for label: grove.io/scheduler-backend: default
func (b *Backend) Matches(podGang *groveschedulerv1alpha1.PodGang) bool {
	if podGang.Labels == nil {
		return false
	}
	return podGang.Labels["grove.io/scheduler-backend"] == BackendLabelValue
}

// Sync converts PodGang to Workload and synchronizes it
func (b *Backend) Sync(ctx context.Context, logger logr.Logger, podGang *groveschedulerv1alpha1.PodGang) error {
	logger.Info("Converting PodGang to Workload", "podGang", podGang.Name)

	// Convert PodGang to Workload
	workload := b.convertPodGangToWorkload(podGang)

	// Create or update Workload
	existing := &schedulingv1alpha1.Workload{}
	err := b.client.Get(ctx, client.ObjectKey{
		Namespace: workload.Namespace,
		Name:      workload.Name,
	}, existing)

	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to get existing Workload: %w", err)
		}

		// Create new Workload
		logger.Info("Creating Workload", "name", workload.Name)
		if err := b.client.Create(ctx, workload); err != nil {
			return fmt.Errorf("failed to create Workload: %w", err)
		}
		return nil
	}

	// Update existing Workload
	workload.ResourceVersion = existing.ResourceVersion
	logger.Info("Updating Workload", "name", workload.Name)
	if err := b.client.Update(ctx, workload); err != nil {
		return fmt.Errorf("failed to update Workload: %w", err)
	}

	return nil
}

// convertPodGangToWorkload converts PodGang to Workload
func (b *Backend) convertPodGangToWorkload(podGang *groveschedulerv1alpha1.PodGang) *schedulingv1alpha1.Workload {
	workload := &schedulingv1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podGang.Name,
			Namespace: podGang.Namespace,
		},
		Spec: schedulingv1alpha1.WorkloadSpec{
			// Convert PodGang's PodGroups to Workload's PodGroups
			PodGroups: b.convertPodGroups(podGang.Spec.PodGroups),
		},
	}

	// Copy labels
	if workload.Labels == nil {
		workload.Labels = make(map[string]string)
	}
	for k, v := range podGang.Labels {
		workload.Labels[k] = v
	}

	// Set owner reference
	ownerRef := metav1.OwnerReference{
		APIVersion: groveschedulerv1alpha1.SchemeGroupVersion.String(),
		Kind:       "PodGang",
		Name:       podGang.Name,
		UID:        podGang.UID,
	}
	workload.SetOwnerReferences([]metav1.OwnerReference{ownerRef})

	return workload
}

// convertPodGroups converts PodGang's PodGroups to Workload API PodGroups
func (b *Backend) convertPodGroups(podGangGroups []groveschedulerv1alpha1.PodGroup) []schedulingv1alpha1.PodGroup {
	podGroups := make([]schedulingv1alpha1.PodGroup, 0, len(podGangGroups))

	for _, pg := range podGangGroups {
		podGroups = append(podGroups, schedulingv1alpha1.PodGroup{
			Name: pg.Name,
			Policy: schedulingv1alpha1.PodGroupPolicy{
				Gang: &schedulingv1alpha1.GangSchedulingPolicy{
					MinCount: pg.MinReplicas,
				},
			},
		})
	}

	return podGroups
}

// Delete removes the Workload owned by this PodGang
func (b *Backend) Delete(ctx context.Context, logger logr.Logger, podGang *groveschedulerv1alpha1.PodGang) error {
	logger.Info("Deleting Workload", "podGang", podGang.Name)

	workload := &schedulingv1alpha1.Workload{}
	workload.Name = podGang.Name
	workload.Namespace = podGang.Namespace

	if err := b.client.Delete(ctx, workload); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to delete Workload: %w", err)
	}

	return nil
}

// CheckReady checks if the Workload is ready for scheduling
func (b *Backend) CheckReady(ctx context.Context, logger logr.Logger, podGang *groveschedulerv1alpha1.PodGang) (bool, string, error) {
	workloadName := podGang.Name

	workload := &schedulingv1alpha1.Workload{}
	err := b.client.Get(ctx, client.ObjectKey{
		Namespace: podGang.Namespace,
		Name:      workloadName,
	}, workload)

	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			// Workload doesn't exist yet - not ready
			return false, workloadName, nil
		}
		return false, workloadName, fmt.Errorf("failed to get Workload: %w", err)
	}

	// Check if Workload is admitted (ready for scheduling)
	// Note: Workload API in k8s.io/api/scheduling/v1alpha1 may have different status structure
	// For now, consider it ready if it exists
	isReady := false
	// TODO: Update this when the actual Workload API structure is confirmed

	logger.V(1).Info("Checked Workload readiness",
		"workloadName", workloadName,
		"isReady", isReady)

	return isReady, workloadName, nil
}

// GetSchedulingGateName returns the scheduling gate name used by this backend
func (b *Backend) GetSchedulingGateName() string {
	return SchedulingGateName
}

// ShouldRemoveSchedulingGate checks if the Workload is ready for this PodClique
// For Workload API: remove gate when ALL PodGroups in the Workload meet their MinCount requirements
func (b *Backend) ShouldRemoveSchedulingGate(ctx context.Context, logger logr.Logger, pod *corev1.Pod, podCliqueObj client.Object) (bool, string, error) {
	podClique, ok := podCliqueObj.(*grovecorev1alpha1.PodClique)
	if !ok {
		return false, "", fmt.Errorf("expected PodClique object, got %T", podCliqueObj)
	}

	// Get the Workload name from the PodClique labels
	workloadName, hasWorkloadLabel := podClique.GetLabels()[common.LabelPodGang]
	if !hasWorkloadLabel {
		logger.Info("PodClique has no Workload label", "podClique", client.ObjectKeyFromObject(podClique))
		return false, "no workload label", nil
	}

	// Get the Workload object
	workload := &schedulingv1alpha1.Workload{}
	workloadKey := client.ObjectKey{Name: workloadName, Namespace: podClique.Namespace}
	if err := b.client.Get(ctx, workloadKey, workload); err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("Workload not found yet", "workloadName", workloadName)
			return false, "workload not found", nil
		}
		return false, "", fmt.Errorf("failed to get Workload %v: %w", workloadKey, err)
	}

	// Check if all PodGroups in the Workload have sufficient created replicas
	for _, podGroup := range workload.Spec.PodGroups {
		pclqName := podGroup.Name
		pclq := &grovecorev1alpha1.PodClique{}
		pclqKey := client.ObjectKey{Name: pclqName, Namespace: podClique.Namespace}
		if err := b.client.Get(ctx, pclqKey, pclq); err != nil {
			return false, "", fmt.Errorf("failed to get PodClique %s for Workload readiness check: %w", pclqName, err)
		}

		// Check if MinCount is satisfied
		if podGroup.Policy.Gang != nil {
			minCount := podGroup.Policy.Gang.MinCount
			if pclq.Status.Replicas < minCount {
				logger.V(1).Info("Workload not ready: PodClique has insufficient created pods",
					"workloadName", workloadName,
					"pclqName", pclqName,
					"createdReplicas", pclq.Status.Replicas,
					"minCount", minCount)
				return false, fmt.Sprintf("workload not ready: %s has %d/%d pods", pclqName, pclq.Status.Replicas, minCount), nil
			}
		}
	}

	logger.Info("Workload is ready - proceeding with gate removal",
		"workloadName", workloadName,
		"podObjectKey", client.ObjectKeyFromObject(pod))
	return true, "workload ready", nil
}

// MutatePodSpec mutates the Pod spec to add Workload-specific requirements
// For Workload API (K8s 1.35+), we need to set the WorkloadRef
func (b *Backend) MutatePodSpec(pod *corev1.Pod, gangName string, podGroupName string) error {
	// Set WorkloadRef (required by Kubernetes Workload API)
	pod.Spec.WorkloadRef = &corev1.WorkloadReference{
		Name:     gangName,     // Workload name (e.g., "simple1-0")
		PodGroup: podGroupName, // PodGroup name within the Workload (e.g., "simple1-0-pca")
	}

	return nil
}

// workloadGVK returns the GroupVersionKind for Kubernetes Workload API
func workloadGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "scheduling.k8s.io",
		Version: "v1alpha1",
		Kind:    "Workload",
	}
}

// init registers the Workload backend for pod mutation
func init() {
	// Register this backend for pod mutation
	// Create a singleton instance for mutation (doesn't need client)
	mutatorBackend := &Backend{}
	if err := backend.RegisterBackendForMutation("default-scheduler", mutatorBackend); err != nil {
		panic(fmt.Sprintf("failed to register Workload backend for mutation: %v", err))
	}
	backend.SetDefaultBackendForMutation(mutatorBackend)
}

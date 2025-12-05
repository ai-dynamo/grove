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

package backend

import (
	"context"
	"fmt"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SchedulerBackend defines the interface that different scheduler backends must implement.
//
// Architecture V2: Two-Phase Approach
// Phase 1: Operator always creates PodGang (unified intermediate representation)
// Phase 2: Backend converts PodGang → scheduler-specific CR (PodGroup/Workload/etc)
//
// This design provides:
// - PodGang as single source of truth
// - Backend as pure converter/synchronizer
// - Better decoupling and observability
//
// Design inspired by: https://github.com/kubernetes/kubernetes/tree/master/pkg/scheduler/backend/queue
type SchedulerBackend interface {
	// Name returns the unique name of this scheduler backend
	Name() string

	// Matches returns true if this backend should handle the given PodGang
	// Based on labels/annotations on PodGang (e.g., grove.io/scheduler-backend)
	Matches(podGang *groveschedulerv1alpha1.PodGang) bool

	// Sync converts PodGang to scheduler-specific CR and synchronizes it
	// The PodGang serves as the source of truth
	Sync(ctx context.Context, logger logr.Logger, podGang *groveschedulerv1alpha1.PodGang) error

	// Delete removes scheduler-specific CRs owned by this PodGang
	Delete(ctx context.Context, logger logr.Logger, podGang *groveschedulerv1alpha1.PodGang) error

	// CheckReady checks if the scheduler-specific CR is ready
	// Returns: (isReady, resourceName, error)
	CheckReady(ctx context.Context, logger logr.Logger, podGang *groveschedulerv1alpha1.PodGang) (bool, string, error)

	// GetSchedulingGateName returns the name of the scheduling gate used by this backend
	GetSchedulingGateName() string

	// MutatePodSpec mutates the Pod spec to add scheduler-specific requirements
	// This method allows backends to inject fields like WorkloadRef, annotations, etc.
	// Parameters:
	//   - pod: the Pod to mutate
	//   - gangName: the PodGang name this pod belongs to
	//   - podGroupName: the PodGroup name within the gang (e.g., PodClique name)
	MutatePodSpec(pod *corev1.Pod, gangName string, podGroupName string) error

	// ShouldRemoveSchedulingGate checks if the scheduling gate should be removed from the given Pod
	// Each backend implements its own logic to determine when it's safe to remove the gate
	// Returns:
	//   - shouldRemove: true if the gate should be removed
	//   - reason: human-readable reason for the decision
	//   - error: any error encountered during the check
	ShouldRemoveSchedulingGate(ctx context.Context, logger logr.Logger, pod *corev1.Pod, podClique client.Object) (bool, string, error)
}

// MutatePod applies scheduler-specific mutations to a Pod
// This is the main entry point for the pod component
func MutatePod(pod *corev1.Pod, gangName string, podGroupName string) error {
	// Get backend from global manager
	manager, err := GetGlobalManager()
	if err != nil {
		return fmt.Errorf("backend manager not initialized: %w", err)
	}

	backend, err := manager.GetBackend(pod.Spec.SchedulerName)
	if err != nil {
		return fmt.Errorf("failed to get backend for scheduler %s: %w", pod.Spec.SchedulerName, err)
	}

	// Apply scheduling gate
	pod.Spec.SchedulingGates = []corev1.PodSchedulingGate{
		{Name: backend.GetSchedulingGateName()},
	}

	// Apply backend-specific mutations
	return backend.MutatePodSpec(pod, gangName, podGroupName)
}

// Note: All PodGang-related types (PodGangSpec, PodGroup, TopologyConstraint, etc.)
// are defined in scheduler/api/core/v1alpha1/podgang.go
// This backend package uses those API types directly to avoid duplication

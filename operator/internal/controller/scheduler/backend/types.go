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
	"sync"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
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

// BackendFactory creates scheduler backends
type BackendFactory interface {
	// CreateBackend creates a new backend instance
	CreateBackend(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder) (SchedulerBackend, error)
}

// Global registry for scheduler backends (by scheduler name)
// This allows pod component to find the right backend to mutate pods
var (
	backendMutatorRegistry = make(map[string]SchedulerBackend)
	backendMutatorMu       sync.RWMutex
	defaultBackend         SchedulerBackend
)

// RegisterBackendForMutation registers a backend for pod mutation by scheduler name
// This should be called by each backend's init() function
func RegisterBackendForMutation(schedulerName string, backend SchedulerBackend) error {
	backendMutatorMu.Lock()
	defer backendMutatorMu.Unlock()

	if schedulerName == "" {
		return fmt.Errorf("scheduler name cannot be empty")
	}

	if _, exists := backendMutatorRegistry[schedulerName]; exists {
		return fmt.Errorf("backend for scheduler %s already registered", schedulerName)
	}

	backendMutatorRegistry[schedulerName] = backend
	return nil
}

// SetDefaultBackendForMutation sets the default backend for empty scheduler names
func SetDefaultBackendForMutation(backend SchedulerBackend) {
	backendMutatorMu.Lock()
	defer backendMutatorMu.Unlock()
	defaultBackend = backend
}

// MutatePod applies scheduler-specific mutations to a Pod
// This is the main entry point for the pod component
func MutatePod(pod *corev1.Pod, gangName string, podGroupName string) error {
	backend, err := getBackendForScheduler(pod.Spec.SchedulerName)
	if err != nil {
		return err
	}

	// Apply scheduling gate
	pod.Spec.SchedulingGates = []corev1.PodSchedulingGate{
		{Name: backend.GetSchedulingGateName()},
	}

	// Apply backend-specific mutations
	return backend.MutatePodSpec(pod, gangName, podGroupName)
}

// getBackendForScheduler returns the appropriate backend for the given scheduler name
func getBackendForScheduler(schedulerName string) (SchedulerBackend, error) {
	backendMutatorMu.RLock()
	defer backendMutatorMu.RUnlock()

	// Normalize empty scheduler name to "default-scheduler"
	if schedulerName == "" {
		schedulerName = "default-scheduler"
	}

	// Try exact match first
	if backend, exists := backendMutatorRegistry[schedulerName]; exists {
		return backend, nil
	}

	// Use default backend if set
	if defaultBackend != nil {
		return defaultBackend, nil
	}

	return nil, fmt.Errorf("no backend registered for scheduler %s", schedulerName)
}

// GetBackendForScheduler returns the backend instance for the given scheduler name (exported)
// This allows other components (like pod component) to access backends for gate removal logic
func GetBackendForScheduler(schedulerName string) (SchedulerBackend, error) {
	return getBackendForScheduler(schedulerName)
}

// PodGangInfo contains the information needed to create a gang scheduling resource
// This is the internal representation that backends translate to their specific CRs
type PodGangInfo struct {
	// Name is the name of the gang
	Name string
	// Namespace is the namespace of the gang
	Namespace string
	// PodGroups contains groups of pods that should be scheduled together
	PodGroups []PodGroupInfo
	// TopologyConstraint defines topology constraints for the entire gang
	TopologyConstraint *TopologyConstraint
	// TopologyConstraintGroupConfigs defines topology constraints for groups of pod groups
	TopologyConstraintGroupConfigs []TopologyConstraintGroupConfig
	// PriorityClassName is the priority class for the gang
	PriorityClassName string
	// ReuseReservationRef references a previous gang for reservation reuse
	ReuseReservationRef *NamespacedName
}

// PodGroupInfo represents a group of pods within a gang
type PodGroupInfo struct {
	// Name is the name of this pod group
	Name string
	// PodReferences are the pods in this group
	PodReferences []NamespacedName
	// MinReplicas is the minimum number of replicas that must be scheduled
	MinReplicas int32
	// TopologyConstraint defines topology constraints for this pod group
	TopologyConstraint *TopologyConstraint
}

// TopologyConstraint defines topology placement constraints
type TopologyConstraint struct {
	// PackConstraint defines how pods should be packed across topology domains
	PackConstraint *TopologyPackConstraint
}

// TopologyPackConstraint defines required and preferred topology packing
type TopologyPackConstraint struct {
	// Required topology key that must be satisfied
	Required *string
	// Preferred topology key for best-effort placement
	Preferred *string
}

// TopologyConstraintGroupConfig defines topology constraints for a group of pod groups
type TopologyConstraintGroupConfig struct {
	// PodGroupNames are the names of pod groups in this topology group
	PodGroupNames []string
	// TopologyConstraint for this group
	TopologyConstraint *TopologyConstraint
}

// NamespacedName identifies a resource by namespace and name
type NamespacedName struct {
	Namespace string
	Name      string
}

// GangStatus represents the status of a gang scheduling resource
type GangStatus struct {
	// Phase is the current phase of the gang
	Phase GangPhase
	// IsReady indicates if the gang is ready for pod scheduling
	IsReady bool
	// PlacementScore is the network optimality score (0.0 to 1.0)
	PlacementScore *float64
}

// GangPhase represents the phase of a gang
type GangPhase string

const (
	// GangPhasePending indicates the gang is pending scheduling
	GangPhasePending GangPhase = "Pending"
	// GangPhaseStarting indicates scheduling has started
	GangPhaseStarting GangPhase = "Starting"
	// GangPhaseRunning indicates all pods are scheduled
	GangPhaseRunning GangPhase = "Running"
	// GangPhaseFailed indicates gang scheduling failed
	GangPhaseFailed GangPhase = "Failed"
)

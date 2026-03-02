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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName={pg}

// PodGang defines a specification of a group of pods that should be scheduled together.
type PodGang struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// Spec defines the specification of the PodGang.
	Spec PodGangSpec `json:"spec"`
	// Status defines the status of the PodGang.
	Status PodGangStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PodGangList contains a list of PodGang's.
type PodGangList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	// Items is a slice of PodGang's.
	Items []PodGang `json:"items"`
}

// PodGangSpec defines the specification of a PodGang.
type PodGangSpec struct {
	// PodGroups is a list of member pod groups in the PodGang.
	PodGroups []PodGroup `json:"podgroups"`
	// TopologyConstraint defines topology packing constraints for entire pod gang.
	// This is the top level topology constraint that applies to all PodGroups in the PodGang.
	// Updated by operator on each reconciliation when PodCliqueSet topology constraints change.
	// +optional
	TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`
	// TopologyConstraintGroupConfigs defines TopologyConstraints for a strict subset of PodGroups.
	// +optional
	TopologyConstraintGroupConfigs []TopologyConstraintGroupConfig `json:"topologyConstraintGroupConfigs,omitempty"`
	// PriorityClassName is the name of the PriorityClass for the PodGang.
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`
	// ReuseReservationRef holds the reference to another PodGang resource scheduled previously.
	// During updates, an operator can suggest to reuse the reservation of the previous PodGang for a newer version of the
	// PodGang resource. This is a suggestion for the scheduler and not a requirement that must be met. If the scheduler plugin
	// finds that the reservation done previously was network optimised and there are no better alternatives available, then it
	// will reuse the reservation. If there are better alternatives available, then the scheduler will ignore this suggestion.
	// +optional
	ReuseReservationRef *NamespacedName `json:"reuseReservationRef,omitempty"`
}

// PodGroup defines a set of pods in a PodGang that share the same PodTemplateSpec.
type PodGroup struct {
	// Name is the name of the PodGroup.
	Name string `json:"name"`
	// PodReferences is a list of references to the Pods that are part of this group.
	PodReferences []NamespacedName `json:"podReferences"`
	// MinReplicas is the number of replicas that needs to be gang scheduled.
	// If the MinReplicas is greater than len(PodReferences) then scheduler makes the best effort to schedule as many pods beyond
	// MinReplicas. However, guaranteed gang scheduling is only provided for MinReplicas.
	MinReplicas int32 `json:"minReplicas"`
	// TopologyConstraint defines topology packing constraints for this PodGroup.
	// Enables PodClique-level topology constraints.
	// Updated by operator when PodClique topology constraints change.
	// +optional
	TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`
}

// TopologyConstraint defines topology packing constraints with required and preferred levels.
type TopologyConstraint struct {
	// PackConstraint defines topology packing constraint with required and preferred levels.
	// Operator translates user's level name to corresponding topologyKeys.
	// +optional
	PackConstraint *TopologyPackConstraint `json:"packConstraint,omitempty"`
}

// TopologyPackConstraint defines a topology packing constraint.
// Each of Required and Preferred fields hold a topologyKey, e.g. "kubernetes.io/hostname" ( these are key of labels added on nodes).
type TopologyPackConstraint struct {
	// Required defines a topology constraint that must be satisfied as a hard requirement. The workload will not be
	// scheduled if this constraint cannot be satisfied. Generally, it is easier for the scheduler to satisfy constraints
	// on topology domains with larger compute capacity, (e.g. zone or datacenter), than smaller domains, (e.g. host or
	// numa). Holds topologyKey (not level name) translated from user's packLevel specification.
	// Example: "topology.kubernetes.io/rack"
	// +optional
	Required *string `json:"required,omitempty"`
	// Preferred defines best-effort topology constraint. Topology domains that provide the most optimized performance
	// with dense packing (e.g. host or numa) are typically used as preferred constraints for topology packing. It might be
	// harder to satisfy these constraints if the topology domains are limited in compute capacity. Since it is preferred
	// constraint, it is therefore not binding on the scheduler to mandatorily satisfy this packing constraint. Scheduler
	// can fall back to higher topology levels (upto Required constraint) if preferred cannot be satisfied.
	// Example: "kubernetes.io/hostname"
	// +optional
	Preferred *string `json:"preferred,omitempty"`
}

// TopologyConstraintGroupConfig defines topology constraints for a group of PodGroups.
type TopologyConstraintGroupConfig struct {
	// Name is the name of the topology constraint group.
	Name string `json:"name"`
	// PodGroupNames is the list of PodGroup names in the topology constraint group.
	PodGroupNames []string `json:"podGroupNames"`
	// TopologyConstraint defines topology packing constraints for this group.
	// +optional
	TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`
}

// NamespacedName is a struct that contains the namespace and name of an object.
// types.NamespacedName does not have json tags, so we define our own for the time being.
// If https://github.com/kubernetes/kubernetes/issues/131313 is resolved, we can switch to using the APIMachinery type instead.
type NamespacedName struct {
	// Namespace is the namespace of the object.
	Namespace string `json:"namespace"`
	// Name is the name of the object.
	Name string `json:"name"`
}

// PodGangPhase defines the current phase of a PodGang.
type PodGangPhase string

const (
	// PodGangPhasePending indicates that all the pods in a PodGang have been created and the PodGang is pending scheduling.
	PodGangPhasePending PodGangPhase = "Pending"
	// PodGangPhaseStarting indicates that the scheduler has started binding pods in the PodGang to nodes.
	PodGangPhaseStarting PodGangPhase = "Starting"
	// PodGangPhaseRunning indicates that all the pods in the PodGang have been scheduled and are running.
	PodGangPhaseRunning PodGangPhase = "Running"
)

// PodGangConditionType defines the type of condition for a PodGang.
type PodGangConditionType string

const (
	// PodGangConditionTypeScheduled indicates that the PodGang has been scheduled.
	PodGangConditionTypeScheduled PodGangConditionType = "Scheduled"
	// PodGangConditionTypeReady indicates that all the constituent PodGroups are Ready.
	PodGangConditionTypeReady PodGangConditionType = "Ready"
	// PodGangConditionTypeInitialized indicates that all Pods have been created and PodGang has been populated with pod references.
	// This condition is set to True after all pods are created, signaling that scheduling gates can be removed.
	PodGangConditionTypeInitialized PodGangConditionType = "Initialized"
	// PodGangConditionTypeUnhealthy indicates that the PodGang is unhealthy. It is now a candidate for gang termination.
	// If this condition is true for at least PodGangSpec.TerminationDelay duration, then the PodGang will be terminated.
	PodGangConditionTypeUnhealthy PodGangConditionType = "Unhealthy"
	// PodGangConditionTypeDisruptionTarget indicates that the PodGang is a target for disruption and is about to be terminated.
	// due to one of the following reasons:
	// 1. PodGang is preempted by a higher priority PodGang.
	// 2. PodGang is being terminated due to PodGangConditionTypeUnhealthy condition being true for at least PodGangSpec.TerminationDelay duration.
	PodGangConditionTypeDisruptionTarget PodGangConditionType = "DisruptionTarget"
)

// PodGangStatus defines the status of a PodGang.
type PodGangStatus struct {
	// Phase is the current phase of a PodGang.
	Phase PodGangPhase `json:"phase"`
	// Conditions is a list of conditions that describe the current state of the PodGang.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// PlacementScore is network optimality score for the PodGang. If the choice that the scheduler has made corresponds to the
	// best possible placement of the pods in the PodGang, then the score will be 1.0. Higher the score, better the placement.
	PlacementScore *float64 `json:"placementScore,omitempty"`
}

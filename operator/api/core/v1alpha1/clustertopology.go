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
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=ct
// +kubebuilder:subresource:status

// ClusterTopology defines the topology hierarchy for the cluster.
type ClusterTopology struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// Spec defines the topology hierarchy specification.
	Spec ClusterTopologySpec `json:"spec"`
	// Status defines the observed state of the ClusterTopology.
	Status ClusterTopologyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ClusterTopologyList is a list of ClusterTopology resources.
type ClusterTopologyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterTopology `json:"items"`
}

// ClusterTopologySpec defines the topology hierarchy specification.
type ClusterTopologySpec struct {
	// Levels is an ordered list of topology levels from broadest to narrowest scope.
	// The order in this list defines the hierarchy (index 0 = broadest level).
	// Uniqueness of domain and key is enforced by the ClusterTopology validating webhook.
	// +kubebuilder:validation:MinItems=1
	Levels []TopologyLevel `json:"levels"`

	// SchedulerTopologyReferences controls per-backend topology resource management.
	// For each enabled TopologyAwareSchedBackend, the operator checks whether an entry
	// for that backend exists in this list:
	// - If absent: the operator auto-creates and manages the backend's topology resource.
	// - If present: the named resource is assumed to be externally managed; the operator
	//   compares its levels and reports any mismatch via the SchedulerTopologyDrift condition.
	// +optional
	SchedulerTopologyReferences []SchedulerTopologyReference `json:"schedulerTopologyReferences,omitempty"`
}

// ClusterTopologyStatus defines the observed state of ClusterTopology.
type ClusterTopologyStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions represents the latest available observations of the ClusterTopology.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// SchedulerTopologyStatuses reports the sync state between this ClusterTopology
	// and each topology-aware scheduler backend's topology resource.
	// +optional
	SchedulerTopologyStatuses []SchedulerTopologyStatus `json:"schedulerTopologyStatuses,omitempty"`
}

// SchedulerTopologyReference maps a ClusterTopology to a scheduler backend's topology resource.
type SchedulerTopologyReference struct {
	// SchedulerName is the name of the scheduler backend (e.g., "kai-scheduler").
	// +kubebuilder:validation:Required
	SchedulerName string `json:"schedulerName"`
	// TopologyReference is the name of the scheduler backend's topology resource.
	// +kubebuilder:validation:Required
	TopologyReference string `json:"topologyReference"`
}

// SchedulerTopologyStatus reports the sync state of a scheduler backend's topology resource.
type SchedulerTopologyStatus struct {
	// SchedulerTopologyReference identifies the scheduler backend topology resource
	// this status entry describes.
	SchedulerTopologyReference `json:",inline"`
	// InSync is true when the scheduler backend topology levels match the ClusterTopology levels.
	InSync bool `json:"inSync"`
	// SchedulerBackendTopologyObservedGeneration is the generation of the backend topology
	// resource that was last compared. Zero if the resource was not found.
	// +optional
	SchedulerBackendTopologyObservedGeneration int64 `json:"schedulerBackendTopologyObservedGeneration,omitempty"`
	// Message provides detail when InSync is false.
	// +optional
	Message string `json:"message,omitempty"`
}

// TopologyLevel defines a single level in the topology hierarchy.
// Maps a platform-agnostic domain to a platform-specific node label key,
// allowing workload operators a consistent way to reference topology levels when defining TopologyConstraint's.
type TopologyLevel struct {
	// Domain is a platform provider-agnostic level identifier.
	// +kubebuilder:validation:Required
	Domain TopologyDomain `json:"domain"`

	// Key is the node label key that identifies this topology domain.
	// Must be a valid Kubernetes label key (qualified name).
	// Examples: "topology.kubernetes.io/zone", "kubernetes.io/hostname"
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]/)?([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]$`
	Key string `json:"key"`
}

// TopologyDomain represents a level in the cluster topology hierarchy.
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=63
// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]*$`
type TopologyDomain string

const (
	// TopologyDomainRegion represents the region level in the topology hierarchy.
	TopologyDomainRegion TopologyDomain = "region"
	// TopologyDomainZone represents the zone level in the topology hierarchy.
	TopologyDomainZone TopologyDomain = "zone"
	// TopologyDomainDataCenter represents the datacenter level in the topology hierarchy.
	TopologyDomainDataCenter TopologyDomain = "datacenter"
	// TopologyDomainBlock represents the block level in the topology hierarchy.
	TopologyDomainBlock TopologyDomain = "block"
	// TopologyDomainRack represents the rack level in the topology hierarchy.
	TopologyDomainRack TopologyDomain = "rack"
	// TopologyDomainHost represents the host level in the topology hierarchy.
	TopologyDomainHost TopologyDomain = "host"
	// TopologyDomainNuma represents the numa level in the topology hierarchy.
	TopologyDomainNuma TopologyDomain = "numa"
)

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
// +kubebuilder:printcolumn:name="Domains",type=string,JSONPath=`.spec.levels[*].domain`
// +kubebuilder:printcolumn:name="Schedulers",type=string,JSONPath=`.spec.schedulerTopologyBindings[*].schedulerName`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ClusterTopologyBinding defines Grove's source-of-truth topology hierarchy and how it
// binds to topology resources used by topology-aware scheduler backends.
type ClusterTopologyBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// Spec defines the source-of-truth topology hierarchy and backend binding configuration.
	Spec ClusterTopologyBindingSpec `json:"spec"`
	// Status reports the observed state of backend topology bindings derived from this resource.
	Status ClusterTopologyBindingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ClusterTopologyBindingList is a list of ClusterTopologyBinding resources.
type ClusterTopologyBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterTopologyBinding `json:"items"`
}

// ClusterTopologyBindingSpec defines the desired topology hierarchy and backend binding behavior.
type ClusterTopologyBindingSpec struct {
	// Levels is the source-of-truth ordered topology hierarchy, from broadest to
	// narrowest scope, that Grove exposes to workloads and uses when reconciling
	// backend-specific topology resources.
	// Uniqueness of domain and key is enforced by the ClusterTopologyBinding validating webhook.
	// +kubebuilder:validation:MinItems=1
	Levels []TopologyLevel `json:"levels"`

	// SchedulerTopologyBindings declares how this ClusterTopologyBinding maps to
	// each scheduler backend's topology resource.
	// For each enabled TopologyAwareBackend, the operator checks whether an
	// entry for that backend exists in this list:
	// - If absent: the operator creates and manages the backend topology resource from Levels.
	// - If present: the named backend topology resource is treated as externally
	//   managed, and the operator only checks it for drift against Levels.
	// +optional
	SchedulerTopologyBindings []SchedulerTopologyBinding `json:"schedulerTopologyBindings,omitempty"`
}

// ClusterTopologyBindingStatus defines the observed state of backend topology bindings
// for this ClusterTopologyBinding.
type ClusterTopologyBindingStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions represents the latest available observations of the ClusterTopologyBinding.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// SchedulerTopologyStatuses reports whether each scheduler backend's topology
	// resource is in sync with this ClusterTopologyBinding.
	// +optional
	SchedulerTopologyStatuses []SchedulerTopologyStatus `json:"schedulerTopologyStatuses,omitempty"`
}

// SchedulerTopologyBinding identifies the topology resource through which a
// scheduler backend is bound to this ClusterTopologyBinding.
type SchedulerTopologyBinding struct {
	// SchedulerName is the name of the scheduler backend (e.g., "kai-scheduler").
	// +kubebuilder:validation:Required
	SchedulerName string `json:"schedulerName"`
	// TopologyReference is the name of the backend-specific topology resource
	// bound to this ClusterTopologyBinding.
	// +kubebuilder:validation:Required
	TopologyReference string `json:"topologyReference"`
}

// SchedulerTopologyStatus reports whether a scheduler backend's bound topology
// resource matches this ClusterTopologyBinding.
type SchedulerTopologyStatus struct {
	// SchedulerTopologyBinding identifies the scheduler backend topology resource
	// this status entry describes.
	SchedulerTopologyBinding `json:",inline"`
	// InSync is true when the scheduler backend topology levels match the ClusterTopologyBinding levels.
	InSync bool `json:"inSync"`
	// SchedulerBackendTopologyObservedGeneration is the generation of the backend topology
	// resource that was last compared. Zero if the resource was not found.
	// +optional
	SchedulerBackendTopologyObservedGeneration int64 `json:"schedulerBackendTopologyObservedGeneration,omitempty"`
	// Message provides detail when InSync is false.
	// +optional
	Message string `json:"message,omitempty"`
}

// TopologyLevel defines one level in Grove's source-of-truth topology hierarchy.
// Each level maps a Grove topology domain to the node label key that a backend
// topology representation should use for that level.
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

// TopologyDomain is the Grove-facing identifier for a topology level in the
// source-of-truth hierarchy.
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

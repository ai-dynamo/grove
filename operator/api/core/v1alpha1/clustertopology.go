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

const (
	// DefaultClusterTopologyName is the name of the default ClusterTopology resource managed by the operator.
	// Deprecated: With multi-topology support, workloads reference topologies by name via PodCliqueSetTopologyConstraint.TopologyName.
	// This constant is kept temporarily for backward compatibility during migration.
	DefaultClusterTopologyName = "grove-topology"
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
	// +optional
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
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:XValidation:rule="self.all(x, self.filter(y, y.domain == x.domain).size() == 1)",message="domain must be unique across all levels"
	// +kubebuilder:validation:XValidation:rule="self.all(x, self.filter(y, y.key == x.key).size() == 1)",message="key must be unique across all levels"
	Levels []TopologyLevel `json:"levels"`
	// SchedulerReferences maps this topology to scheduler backend topology resources.
	// When empty, the operator automatically creates and manages the scheduler backend topology.
	// When provided, the referenced resources are assumed to be externally managed and
	// the operator compares the domain/key pairs and their order against the ClusterTopology
	// levels, reporting any mismatch via the SchedulerTopologyDrift condition.
	// +optional
	SchedulerReferences []SchedulerReference `json:"schedulerReferences,omitempty"`
}

// SchedulerReference maps a ClusterTopology to a scheduler backend's topology resource.
type SchedulerReference struct {
	// SchedulerName is the name of the scheduler backend (e.g., "kai-scheduler").
	// +required
	SchedulerName string `json:"schedulerName"`
	// Reference is the name of the scheduler backend's topology resource.
	// +required
	Reference string `json:"reference"`
}

// TopologyLevel defines a single level in the topology hierarchy.
// Maps a platform-agnostic domain to a platform-specific node label key,
// allowing workload operators a consistent way to reference topology levels when defining TopologyConstraint's.
type TopologyLevel struct {
	// Domain is a topology level identifier used in TopologyConstraint references.
	// Administrators can use any name that describes their infrastructure hierarchy.
	// Well-known conventions: region, zone, datacenter, block, rack, host, numa
	// +kubebuilder:validation:Required
	Domain TopologyDomain `json:"domain"`

	// Key is the node label key that identifies this topology domain.
	// Must be a valid Kubernetes qualified label key.
	// Examples: "topology.kubernetes.io/zone", "kubernetes.io/hostname"
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=316
	// +kubebuilder:validation:Pattern=`^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]/)?([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]$`
	Key string `json:"key"`
}

// TopologyDomain represents a topology level identifier.
// Domain names are free-form strings that administrators define to match their infrastructure.
// Well-known conventions include: region, zone, datacenter, block, rack, host, numa.
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=63
// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]*$`
type TopologyDomain string

// ClusterTopologyStatus defines the observed state of a ClusterTopology.
type ClusterTopologyStatus struct {
	// ObservedGeneration is the metadata.generation of the ClusterTopology that was last reconciled.
	// Consumers can compare this to metadata.generation to determine whether the status is up to date.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions represent the latest available observations of the ClusterTopology's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// SchedulerTopologyStatuses reports the per-backend sync state for each scheduler reference.
	// +optional
	SchedulerTopologyStatuses []SchedulerTopologyStatus `json:"schedulerTopologyStatuses,omitempty"`
}

// SchedulerTopologyStatus reports the sync state between this ClusterTopology and a single
// scheduler backend's topology resource.
type SchedulerTopologyStatus struct {
	// SchedulerName is the scheduler backend name (matches SchedulerReference.SchedulerName).
	SchedulerName string `json:"schedulerName"`
	// Reference is the scheduler backend topology resource name (matches SchedulerReference.Reference).
	Reference string `json:"reference"`
	// InSync is true when the scheduler backend topology levels match the ClusterTopology levels.
	InSync bool `json:"inSync"`
	// SchedulerBackendTopologyObservedGeneration is the metadata.generation of the scheduler backend
	// topology resource that was last compared. Allows consumers to verify the comparison is current.
	// Zero if the resource was not found.
	// +optional
	SchedulerBackendTopologyObservedGeneration int64 `json:"schedulerBackendTopologyObservedGeneration,omitempty"`
	// Message provides detail when InSync is false (e.g., describing the mismatch).
	// +optional
	Message string `json:"message,omitempty"`
}

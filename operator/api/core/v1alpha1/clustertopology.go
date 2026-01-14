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
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// DefaultClusterTopologyName is the name of the default ClusterTopology resource managed by the operator.
	// Currently, Grove operator generates and maintains a single ClusterTopology resource per cluster. There is no support
	// for externally created ClusterTopology resources. However, this may change in the future to allow more flexibility.
	DefaultClusterTopologyName = "grove-topology"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=ct

// ClusterTopology defines the topology hierarchy for the cluster.
// This resource is immutable after creation.
type ClusterTopology struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// Spec defines the topology hierarchy specification.
	Spec ClusterTopologySpec `json:"spec"`
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
	// This field is immutable after creation.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=7
	// +kubebuilder:validation:XValidation:rule="self.all(x, self.filter(y, y.domain == x.domain).size() == 1)",message="domain must be unique across all levels"
	// +kubebuilder:validation:XValidation:rule="self.all(x, self.filter(y, y.key == x.key).size() == 1)",message="key must be unique across all levels"
	Levels []TopologyLevel `json:"levels"`
}

// TopologyLevel defines a single level in the topology hierarchy.
// Maps a platform-agnostic domain to a platform-specific node label key,
// allowing workload operators a consistent way to reference topology levels when defining TopologyConstraint's.
type TopologyLevel struct {
	// Domain is a platform provider-agnostic level identifier.
	// Must be one of: region, zone, datacenter, block, rack, host, numa
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=region;zone;datacenter;block;rack;host;numa
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

// IsTopologyDomainNarrower returns true if the current TopologyDomain is narrower in scope than the other TopologyDomain.
func (d TopologyDomain) IsTopologyDomainNarrower(other TopologyDomain) bool {
	return topologyDomainOrder[d] > topologyDomainOrder[other]
}

// SupportedTopologyDomains returns all supported topology domain values.
func SupportedTopologyDomains() []TopologyDomain {
	topologyDomains := make([]TopologyDomain, 0, len(topologyDomainOrder))
	for domain := range topologyDomainOrder {
		topologyDomains = append(topologyDomains, domain)
	}
	return topologyDomains
}

// topologyDomainOrder defines the hierarchical order of topology domains from broadest to narrowest.
// Lower value = broader scope (e.g., Region is broader than Zone).
// Context: TopologyDomain is used when defining TopologyConstraint in workload specifications. TopologyConstraint at different
// levels (PodClique, PodCliqueScalingGroup and PodCliqueSet) captures required packing guarantees that a scheduler backend
// must provide.
var topologyDomainOrder = map[TopologyDomain]int{
	TopologyDomainRegion:     0,
	TopologyDomainZone:       1,
	TopologyDomainDataCenter: 2,
	TopologyDomainBlock:      3,
	TopologyDomainRack:       4,
	TopologyDomainHost:       5,
	TopologyDomainNuma:       6,
}

// SortTopologyLevels sorts a slice of TopologyLevel from broadest to narrowest scope, sorting by their Domain.
func SortTopologyLevels(levels []TopologyLevel) {
	slices.SortFunc(levels, func(a, b TopologyLevel) int {
		return topologyDomainOrder[a.Domain] - topologyDomainOrder[b.Domain]
	})
}

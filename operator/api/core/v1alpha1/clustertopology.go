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
	// ClusterTopologyName is the name of the default ClusterTopology resource managed by the operator.
	ClusterTopologyName = "grove-topology"
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

// TopologyDomain represents a predefined topology level in the hierarchy.
// Topology ordering (broadest to narrowest):
// Region > Zone > DataCenter > Block > Rack > Host > Numa
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

// topologyDomainOrder defines the hierarchical order of topology domains from broadest to narrowest.
// Lower value = broader scope (e.g., Region is broader than Zone).
var topologyDomainOrder = map[TopologyDomain]int{
	TopologyDomainRegion:     0,
	TopologyDomainZone:       1,
	TopologyDomainDataCenter: 2,
	TopologyDomainBlock:      3,
	TopologyDomainRack:       4,
	TopologyDomainHost:       5,
	TopologyDomainNuma:       6,
}

// Compare compares this domain with another domain.
// Returns:
//   - negative value if this domain is broader than other
//   - zero if domains are equal
//   - positive value if this domain is narrower than other
//
// This method assumes both domains are valid (enforced by kubebuilder validation).
func (d TopologyDomain) Compare(other TopologyDomain) int {
	return topologyDomainOrder[d] - topologyDomainOrder[other]
}

// BroaderThan returns true if this domain represents a broader scope than the other domain.
// For example, Region.BroaderThan(Zone) returns true.
func (d TopologyDomain) BroaderThan(other TopologyDomain) bool {
	return d.Compare(other) < 0
}

// NarrowerThan returns true if this domain represents a narrower scope than the other domain.
// For example, Zone.NarrowerThan(Region) returns true.
func (d TopologyDomain) NarrowerThan(other TopologyDomain) bool {
	return d.Compare(other) > 0
}

// TopologyLevel defines a single level in the topology hierarchy.
type TopologyLevel struct {
	// Domain is the predefined level identifier used in TopologyConstraint references.
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

// Compare compares this topology level with another level based on their domains.
// Returns:
//   - negative value if this level is broader than other
//   - zero if levels have equal domain scope
//   - positive value if this level is narrower than other
func (l TopologyLevel) Compare(other TopologyLevel) int {
	return l.Domain.Compare(other.Domain)
}

// BroaderThan returns true if this level represents a broader scope than the other level.
// For example, a Region level is broader than a Zone level.
func (l TopologyLevel) BroaderThan(other TopologyLevel) bool {
	return l.Domain.BroaderThan(other.Domain)
}

// NarrowerThan returns true if this level represents a narrower scope than the other level.
// For example, a Zone level is narrower than a Region level.
func (l TopologyLevel) NarrowerThan(other TopologyLevel) bool {
	return l.Domain.NarrowerThan(other.Domain)
}

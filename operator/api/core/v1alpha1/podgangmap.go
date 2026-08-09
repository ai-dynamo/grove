// Copyright 2026 The Grove Authors.
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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName={pgm}

// PodGangMap is the desired-state mapping between PodGangs and their constituent
// PodClique and PodCliqueScalingGroup pod counts for a single PodCliqueSet replica.
// One PodGangMap resource exists per PodCliqueSet replica, named <pcs-name>-<pcs-replica-index>.
type PodGangMap struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// Spec defines the desired PodGang-to-pod-count mapping for this PodCliqueSet replica.
	Spec PodGangMapSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PodGangMapList is a list of PodGangMap resources.
type PodGangMapList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	// Items is the list of PodGangMap resources.
	Items []PodGangMap `json:"items"`
}

// PodGangMapSpec defines the desired PodGang composition for a PodCliqueSet replica.
type PodGangMapSpec struct {
	// PodCliqueSetReplicaIndex is the index of the PodCliqueSet replica this map belongs to.
	PodCliqueSetReplicaIndex int32 `json:"podCliqueSetReplicaIndex"`
	// Entries is the ordered list of desired PodGangs for this PodCliqueSet replica.
	// Each entry corresponds to one PodGang and specifies its pod and replica counts.
	// +listType=map
	// +listMapKey=epoch
	Entries []PodGangEntry `json:"entries"`
}

// PodGangEntryRole classifies the role a PodGangMap entry plays in a PodCliqueSet replica.
// +kubebuilder:validation:Enum={Anchor,Tail,ScaleOut}
type PodGangEntryRole string

const (
	// PodGangEntryRoleAnchor marks the entry that carries the MinAvailable replicas.
	// It holds every standalone PodClique and each PodCliqueScalingGroup's MinAvailable replicas.
	// It materializes into a single PodGang.
	PodGangEntryRoleAnchor PodGangEntryRole = "Anchor"
	// PodGangEntryRoleTail marks a non-anchor entry that holds a PodCliqueScalingGroup's replica
	// indices above MinAvailable, as declared by the template. It materializes into one PodGang per
	// replica index.
	PodGangEntryRoleTail PodGangEntryRole = "Tail"
	// PodGangEntryRoleScaleOut marks the entry that holds PodCliqueScalingGroup replicas added by a
	// steady-state scale-out beyond the template. It materializes into one PodGang per replica index.
	// It is created on the first scale-out and removed once it holds no replicas.
	PodGangEntryRoleScaleOut PodGangEntryRole = "ScaleOut"
)

// PodGangEntry describes one scheduling batch, identified by its epoch, that materializes into one
// or more PodGangs. An Anchor entry materializes into a single anchor PodGang; a Tail or ScaleOut
// entry materializes into one PodGang per (PodCliqueScalingGroup, replica index) it carries.
type PodGangEntry struct {
	// Epoch is the identity of this entry and the group of PodGangs materialized from it. It serves
	// two purposes. First, identity, it is unique across entries within a PodGangMap and is the
	// listMapKey, and every PodGang materialized from this entry carries it as the grove.io/epoch
	// label so those PodGangs are grouped by it. Second, ordering, DependsOn references epochs, and
	// comparing epochs orders entries so scheduling dependencies can be expressed and the most recent
	// anchor found. The value is a monotonic unix-nano integer used only as a distinct, orderable key.
	// It is not interpreted as a wall-clock time.
	Epoch string `json:"epoch"`
	// PodCliqueSetGenerationHash is the PodCliqueSet generation hash that pods in this PodGang
	// must match. Used by PodClique and PodCliqueScalingGroup reconcilers to create pods at the
	// correct spec version and to distinguish old pods from new pods during a coherent update.
	PodCliqueSetGenerationHash string `json:"podCliqueSetGenerationHash"`
	// Role classifies this entry. See PodGangEntryRole for the meaning of each value. Role is the
	// durable marker that lets the entry structure be reconstructed from the entries alone, so no
	// separate record needs to be persisted.
	Role PodGangEntryRole `json:"role"`
	// AnchorIndex is the index of an anchor entry within its generation hash. It is set only on
	// entries whose Role is Anchor and is 0 for every other entry. The index starts at 0 for each
	// generation hash and increments for each additional anchor of the same hash. It forms the last
	// segment of the anchor PodGang name.
	// +optional
	AnchorIndex int32 `json:"anchorIndex,omitempty"`
	// PodCliques maps standalone PodClique name to the number of pods that belong to this PodGang.
	// Only standalone PodCliques (not owned by a PodCliqueScalingGroup) are listed here.
	// PodCliques owned by a PodCliqueScalingGroup derive their PodGang association via
	// PCSGReplicaIndices below.
	// +optional
	PodCliques map[string]int32 `json:"podCliques,omitempty"`
	// PCSGReplicaIndices maps a PodCliqueScalingGroup config name to the PCSG replica indices this
	// entry carries. For a non-anchor entry the PodGang materializer expands these into one PodGang
	// per index. Indices are stable identities that survive entry reshuffles, so a PodClique
	// reconciler for a PodCliqueScalingGroup-owned PodClique can find its target PodGang by looking
	// up its replica index here.
	// +optional
	PCSGReplicaIndices map[string][]int32 `json:"pcsgReplicaIndices,omitempty"`
	// DependsOn lists the epochs whose PodGangs must be scheduled before this entry's PodGang
	// becomes eligible for scheduling. An empty DependsOn means the entry has no scheduling
	// dependency and its PodGang is eligible for scheduling immediately.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`
}

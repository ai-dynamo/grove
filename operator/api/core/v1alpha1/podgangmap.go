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
	// +listMapKey=name
	Entries []PodGangEntry `json:"entries"`
}

// PodGangEntry describes the desired composition of a single PodGang.
type PodGangEntry struct {
	// Name is the name of the PodGang this entry corresponds to.
	Name string `json:"name"`
	// PodCliqueSetGenerationHash is the PodCliqueSet generation hash that pods in this PodGang
	// must match. Used by PodClique and PodCliqueScalingGroup reconcilers to create pods at the
	// correct spec version and to distinguish old pods from new pods during a coherent update.
	PodCliqueSetGenerationHash string `json:"podCliqueSetGenerationHash"`
	// PodCliques maps standalone PodClique name to the number of pods that belong to this PodGang.
	// Only standalone PodCliques (not owned by a PodCliqueScalingGroup) are listed here.
	// PodCliques owned by a PodCliqueScalingGroup derive their PodGang association via
	// PCSGReplicaIndices below.
	// +optional
	PodCliques map[string]int32 `json:"podCliques,omitempty"`
	// PCSGReplicaIndices maps PodCliqueScalingGroup config name to the PCSG replica indices
	// that belong to this PodGang. The number of replicas is len(slice). Indices are stable
	// identities that survive entry reshuffles, so a PodClique reconciler for a PodCliqueScalingGroup-
	// owned PodClique can find its target PodGang by looking up its replica index here.
	// +optional
	PCSGReplicaIndices map[string][]int32 `json:"pcsgReplicaIndices,omitempty"`
	// Labels carries additional labels to stamp on the materialized PodGang resource,
	// beyond the labels the PodGang materialization adds by default. Today this carries
	// the grove.io/epoch label used for scheduling-order semantics.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// DependsOn lists the PodGang names within the same PodCliqueSet replica whose pods must be
	// scheduled before pods in this PodGang have their scheduling gates removed.
	// Deprecated: use DependsOnEpoch. Will be removed once all consumers migrate.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`
	// DependsOnEpoch is the epoch of the PodGangs whose pods must be scheduled
	// before this entry's PodGang becomes eligible for scheduling. nil means
	// the entry has no scheduling dependency and is eligible immediately.
	// +optional
	DependsOnEpoch *string `json:"dependsOnEpoch,omitempty"`
}

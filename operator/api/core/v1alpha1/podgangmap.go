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
// +kubebuilder:subresource:status

// PodGangMap is the desired-state mapping between PodGangs and their constituent
// PodClique and PodCliqueScalingGroup pod counts for a single PodCliqueSet replica.
// One PodGangMap resource exists per PodCliqueSet replica, named <pcs-name>-<pcs-replica-index>.
type PodGangMap struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// Spec defines the desired PodGang-to-pod-count mapping for this PodCliqueSet replica.
	Spec PodGangMapSpec `json:"spec,omitempty"`
	// Status records the observed progress of an in-flight coherent update for this
	// PodCliqueSet replica. It is populated only while a coherent update is in flight
	// and is cleared once the update completes.
	// +optional
	Status PodGangMapStatus `json:"status,omitempty"`
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
	// IsEpochAnchor marks whether this entry is the epoch anchor for its epoch. When true the entry
	// carries the minimum-availability floor that entries depending on it are scheduled after, and
	// standalone PodClique tail pods are subsumed into it. When false the entry collapses the tail
	// PodGangs of a single PodCliqueScalingGroup within one epoch into one entry: PCSGReplicaIndices
	// holds that epoch's rolled indices for the PCSG, which the PodGang materializer expands into one
	// PodGang per index. It is the durable marker that lets the step structure recorded in Status be
	// recovered from the entries after a crash between the spec and status writes.
	// +optional
	IsEpochAnchor bool `json:"isEpochAnchor,omitempty"`
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
	// Labels carries additional labels to stamp on the materialized PodGang resource,
	// beyond the labels the PodGang materialization adds by default. Today this carries
	// the grove.io/epoch label used for scheduling-order semantics.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// DependsOn lists the epochs whose PodGangs must be scheduled before this entry's PodGang
	// becomes eligible for scheduling. An empty DependsOn means the entry has no scheduling
	// dependency and its PodGang is eligible for scheduling immediately.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`
}

// PodGangMapStatus records the observed progress of a coherent update for a
// PodCliqueSet replica.
type PodGangMapStatus struct {
	// UpdateProgress records the steps and sub-steps emitted so far for the
	// in-flight coherent update, along with their readiness. It is non-nil only
	// while a coherent update is in flight for this replica and is cleared once
	// the update completes.
	// +optional
	UpdateProgress *PodGangMapUpdateProgress `json:"updateProgress,omitempty"`
}

// PodGangMapUpdateProgress records the observed step structure of an in-flight
// coherent update.
type PodGangMapUpdateProgress struct {
	// Steps records the update's steps in emission order. It gives a view of
	// what is done and what is in progress, and lets the coherent update path
	// detect step boundaries and locate the anchor a step's standalone tail
	// pods subsume into.
	Steps []PodGangMapUpdateStep `json:"steps"`
}

// PodGangMapUpdateStep records one step of a coherent update.
type PodGangMapUpdateStep struct {
	// IsAnchorBearing is true when this step produces an anchor entry (in its
	// first sub-step). It is false for a leftover step that produces no anchor.
	// When IsAnchorBearing is true, the anchor's epoch is SubSteps[0].Epoch.
	IsAnchorBearing bool `json:"isAnchorBearing"`
	// State is the step's readiness. It is Ready when every sub-step is Ready,
	// otherwise InProgress. Readiness is derived from PodGang readiness, which
	// never un-sets, so State only ever advances from InProgress to Ready.
	State PodGangMapUpdateState `json:"state"`
	// SubSteps are this step's sub-steps in emission order. For an
	// anchor-bearing step, SubSteps[0] is the anchor-bearing sub-step and the
	// rest are tail sub-steps.
	SubSteps []PodGangMapUpdateSubStep `json:"subSteps"`
}

// PodGangMapUpdateSubStep records one sub-step of a coherent update. A sub-step
// corresponds to exactly one batch of PodGangs sharing a single epoch.
type PodGangMapUpdateSubStep struct {
	// Epoch is the grove.io/epoch value shared by every PodGang this sub-step
	// emitted.
	Epoch string `json:"epoch"`
	// State is the sub-step's readiness. It is Ready when every PodGang at Epoch
	// has become ready at least once, otherwise InProgress. Readiness never
	// un-sets, so State only ever advances from InProgress to Ready.
	State PodGangMapUpdateState `json:"state"`
}

// PodGangMapUpdateState is the readiness of a step or sub-step of a coherent
// update.
type PodGangMapUpdateState string

const (
	// PodGangMapUpdateStateInProgress indicates the step or sub-step has been
	// emitted but is not yet fully ready.
	PodGangMapUpdateStateInProgress PodGangMapUpdateState = "InProgress"
	// PodGangMapUpdateStateReady indicates every PodGang in the step or sub-step
	// has become ready at least once.
	PodGangMapUpdateStateReady PodGangMapUpdateState = "Ready"
)

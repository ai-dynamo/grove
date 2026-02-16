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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:resource:shortName={pcsg}

// PodCliqueScalingGroup is the schema to define scaling groups that is used to scale a group of PodClique's.
// An instance of this custom resource will be created for every pod clique scaling group defined as part of PodCliqueSet.
type PodCliqueScalingGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// Spec is the specification of the PodCliqueScalingGroup.
	Spec PodCliqueScalingGroupSpec `json:"spec"`
	// Status is the status of the PodCliqueScalingGroup.
	Status PodCliqueScalingGroupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PodCliqueScalingGroupList is a slice of PodCliqueScalingGroup's.
type PodCliqueScalingGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	// Items is a slice of PodCliqueScalingGroup.
	Items []PodCliqueScalingGroup `json:"items"`
}

// PodCliqueScalingGroupSpec is the specification of the PodCliqueScalingGroup.
type PodCliqueScalingGroupSpec struct {
	// Replicas is the desired number of replicas for the PodCliqueScalingGroup.
	// If not specified, it defaults to 1.
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas"`
	// MinAvailable specifies the minimum number of ready replicas required for a PodCliqueScalingGroup to be considered operational.
	// A PodCliqueScalingGroup replica is considered "ready" when its associated PodCliques have sufficient ready or starting pods.
	// If MinAvailable is breached, it will be used to signal that the PodCliqueScalingGroup is no longer operating with the desired availability.
	// MinAvailable cannot be greater than Replicas. If ScaleConfig is defined then its MinAvailable should not be less than ScaleConfig.MinReplicas.
	//
	// It serves two main purposes:
	// 1. Gang Scheduling: MinAvailable defines the minimum number of replicas that are guaranteed to be gang scheduled.
	// 2. Gang Termination: MinAvailable is used as a lower bound below which a PodGang becomes a candidate for Gang termination.
	// If not specified, it defaults to 1.
	// +optional
	// +kubebuilder:default=1
	MinAvailable *int32 `json:"minAvailable,omitempty"`
	// CliqueNames is the list of PodClique names that are configured in the
	// matching PodCliqueScalingGroup in PodCliqueSet.Spec.Template.PodCliqueScalingGroupConfigs.
	CliqueNames []string `json:"cliqueNames"`
}

// PodCliqueScalingGroupStatus is the status of the PodCliqueScalingGroup.
type PodCliqueScalingGroupStatus struct {
	// Replicas is the observed number of replicas for the PodCliqueScalingGroup.
	Replicas int32 `json:"replicas,omitempty"`
	// ScheduledReplicas is the number of replicas that are scheduled for the PodCliqueScalingGroup.
	// A replica of PodCliqueScalingGroup is considered "scheduled" when at least MinAvailable number
	// of pods in each constituent PodClique has been scheduled.
	// +kubebuilder:default=0
	ScheduledReplicas int32 `json:"scheduledReplicas"`
	// AvailableReplicas is the number of PodCliqueScalingGroup replicas that are available.
	// A PodCliqueScalingGroup replica is considered available when all constituent PodClique's have
	// PodClique.Status.ReadyReplicas greater than or equal to PodClique.Spec.MinAvailable
	// +kubebuilder:default=0
	AvailableReplicas int32 `json:"availableReplicas"`
	// UpdatedReplicas is the number of PodCliqueScalingGroup replicas that correspond with the latest PodCliqueSetGenerationHash.
	// +kubebuilder:default=0
	UpdatedReplicas int32 `json:"updatedReplicas"`
	// Selector is the selector used to identify the pods that belong to this scaling group.
	Selector *string `json:"selector,omitempty"`
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration *int64 `json:"observedGeneration,omitempty"`
	// LastErrors captures the last errors observed by the controller when reconciling the PodClique.
	LastErrors []LastError `json:"lastErrors,omitempty"`
	// Conditions represents the latest available observations of the PodCliqueScalingGroup by its controller.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// CurrentPodCliqueSetGenerationHash establishes a correlation to PodCliqueSet generation hash indicating
	// that the spec of the PodCliqueSet at this generation is fully realized in the PodCliqueScalingGroup.
	CurrentPodCliqueSetGenerationHash *string `json:"currentPodCliqueSetGenerationHash,omitempty"`
	// UpdateProgress provides details about the ongoing update of the PodCliqueScalingGroup.
	UpdateProgress *PodCliqueScalingGroupUpdateProgress `json:"updateProgress,omitempty"`
}

// PodCliqueScalingGroupUpdateProgress provides details about the ongoing update of the PodCliqueScalingGroup.
type PodCliqueScalingGroupUpdateProgress struct {
	//UpdateStartedAt is the time at which the update started.
	UpdateStartedAt metav1.Time `json:"updateStartedAt"`
	// UpdateEndedAt is the time at which the update ended.
	UpdateEndedAt *metav1.Time `json:"updateEndedAt,omitempty"`
	// PodCliqueSetGenerationHash is the PodCliqueSet generation hash corresponding to the PodCliqueSet spec that is being rolled out.
	// While the update is in progress PodCliqueScalingGroupStatus.CurrentPodCliqueSetGenerationHash will not match this hash. Once the update is complete the
	// value of this field will be copied to PodCliqueScalingGroupStatus.CurrentPodCliqueSetGenerationHash.
	PodCliqueSetGenerationHash string `json:"podCliqueSetGenerationHash"`
	// UpdatedPodCliques is the list of PodClique names that have been updated to the latest PodCliqueSet spec.
	UpdatedPodCliques []string `json:"updatedPodCliques,omitempty"`
	// ReadyReplicaIndicesSelectedToUpdate provides the update progress of ready replicas of PodCliqueScalingGroup that have been selected for update.
	// PodCliqueScalingGroup replicas that are either pending or unhealthy will be force updated and the update will not wait for these replicas to become ready.
	// For all ready replicas, one replica is chosen at a time to update, once it is updated and becomes ready, the next ready replica is chosen for update.
	ReadyReplicaIndicesSelectedToUpdate *PodCliqueScalingGroupReplicaUpdateProgress `json:"readyReplicaIndicesSelectedToUpdate,omitempty"`
}

// PodCliqueScalingGroupReplicaUpdateProgress provides details about the update progress of ready replicas of PodCliqueScalingGroup that have been selected for update in a rolling recreate. It is not set in an OnDelete update.
type PodCliqueScalingGroupReplicaUpdateProgress struct {
	// Current is the index of the PodCliqueScalingGroup replica that is currently being updated.
	Current int32 `json:"current"`
	// Completed is the list of indices of PodCliqueScalingGroup replicas that have been updated to the latest PodCliqueSet spec.
	Completed []int32 `json:"completed,omitempty"`
}

// SetLastErrors sets the last errors observed by the controller when reconciling the PodCliqueScalingGroup.
func (pcsg *PodCliqueScalingGroup) SetLastErrors(lastErrs ...LastError) {
	pcsg.Status.LastErrors = lastErrs
}

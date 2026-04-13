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
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.hpaPodSelector
// +kubebuilder:resource:shortName={pcs}

// PodCliqueSet is a set of PodGangs defining specification on how to spread and manage a gang of pods and monitoring their status.
type PodCliqueSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// Spec defines the specification of the PodCliqueSet.
	Spec PodCliqueSetSpec `json:"spec"`
	// Status defines the status of the PodCliqueSet.
	Status PodCliqueSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PodCliqueSetList is a list of PodCliqueSet's.
type PodCliqueSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	// Items is a slice of PodCliqueSets.
	Items []PodCliqueSet `json:"items"`
}

// PodCliqueSetSpec defines the specification of a PodCliqueSet.
type PodCliqueSetSpec struct {
	// Replicas is the number of desired replicas of the PodCliqueSet.
	// +kubebuilder:default=0
	Replicas int32 `json:"replicas,omitempty"`
	// UpdateStrategy defines the strategy for updating replicas when
	// templates change. This applies to both standalone PodCliques and
	// PodCliqueScalingGroups.
	// +optional
	UpdateStrategy *PodCliqueSetUpdateStrategy `json:"updateStrategy,omitempty"`
	// Template describes the template spec for PodGangs that will be created in the PodCliqueSet.
	Template PodCliqueSetTemplateSpec `json:"template"`
}

// PodCliqueSetStatus defines the status of a PodCliqueSet.
type PodCliqueSetStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration *int64 `json:"observedGeneration,omitempty"`
	// Conditions represents the latest available observations of the PodCliqueSet by its controller.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// LastErrors captures the last errors observed by the controller when reconciling the PodCliqueSet.
	LastErrors []LastError `json:"lastErrors,omitempty"`
	// Replicas is the total number of PodCliqueSet replicas created.
	Replicas int32 `json:"replicas,omitempty"`
	// UpdatedReplicas is the number of replicas that have been updated to the desired revision of the PodCliqueSet.
	// +kubebuilder:default=0
	UpdatedReplicas int32 `json:"updatedReplicas"`
	// AvailableReplicas is the number of PodCliqueSet replicas that are available.
	// A PodCliqueSet replica is considered available when all standalone PodCliques within that replica
	// have MinAvailableBreached condition = False AND all PodCliqueScalingGroups (PCSG) within that replica
	// have MinAvailableBreached condition = False.
	// +kubebuilder:default=0
	AvailableReplicas int32 `json:"availableReplicas"`
	// Selector is the label selector that determines which pods are part of the PodGang.
	// PodGang is a unit of scale and this selector is used by HPA to scale the PodGang based on metrics captured for
	// the pods that match this selector.
	Selector *string `json:"hpaPodSelector,omitempty"`
	// PodGangStatuses captures the status for all the PodGang's that are part of the PodCliqueSet.
	PodGangStatutes []PodGangStatus `json:"podGangStatuses,omitempty"`
	// CurrentGenerationHash is a hash value generated out of a collection of fields in a PodCliqueSet.
	// Since only a subset of fields is taken into account when generating the hash, not every change in the PodCliqueSetSpec will
	// be accounted for when generating this hash value. A field in PodCliqueSetSpec is included if a change to it triggers
	// a rolling recreate of PodCliques and/or PodCliqueScalingGroups.
	// Only if this value is not nil and the newly computed hash value is different from the persisted CurrentGenerationHash value
	// then an update needs to be triggered.
	CurrentGenerationHash *string `json:"currentGenerationHash,omitempty"`
	// RollingUpdateProgress represents the progress of a rolling update.
	// Deprecated: Use UpdateProgress instead. This field is maintained for backward compatibility and will be
	// removed in a future release.
	RollingUpdateProgress *PodCliqueSetRollingUpdateProgress `json:"rollingUpdateProgress,omitempty"`
	// UpdateProgress represents the progress of an update.
	UpdateProgress *PodCliqueSetUpdateProgress `json:"updateProgress,omitempty"`
}

// PodCliqueSetUpdateStrategy defines the update strategy for a PodCliqueSet.
type PodCliqueSetUpdateStrategy struct {
	// Type indicates the type of update strategy.
	// This strategy applies uniformly to both standalone PodCliques and
	// PodCliqueScalingGroups within the PodCliqueSet.
	// Default is RollingRecreate.
	// +kubebuilder:default=RollingRecreate
	Type UpdateStrategyType `json:"type,omitempty"`
}

// PodCliqueSetRollingUpdateProgress captures the progress of a rolling update of the PodCliqueSet.
// Deprecated: Use PodCliqueSetUpdateProgress instead. This struct is maintained for backward compatibility.
type PodCliqueSetRollingUpdateProgress struct {
	// UpdateStartedAt is the time at which the rolling update started for the PodCliqueSet.
	UpdateStartedAt metav1.Time `json:"updateStartedAt,omitempty"`
	// UpdateEndedAt is the time at which the rolling update ended for the PodCliqueSet.
	// +optional
	UpdateEndedAt *metav1.Time `json:"updateEndedAt,omitempty"`
	// UpdatedPodCliqueScalingGroups is a list of PodCliqueScalingGroup names that have been updated to the desired PodCliqueSet generation hash.
	UpdatedPodCliqueScalingGroups []string `json:"updatedPodCliqueScalingGroups,omitempty"`
	// UpdatedPodCliques is a list of PodClique names that have been updated to the desired PodCliqueSet generation hash.
	UpdatedPodCliques []string `json:"updatedPodCliques,omitempty"`
	// CurrentlyUpdating captures the progress of the PodCliqueSet replica that is currently being updated.
	// +optional
	CurrentlyUpdating *PodCliqueSetReplicaRollingUpdateProgress `json:"currentlyUpdating,omitempty"`
}

// PodCliqueSetReplicaRollingUpdateProgress captures the progress of a rolling update for a specific PodCliqueSet replica.
// Deprecated: Use PodCliqueSetReplicaUpdateProgress instead. This struct is maintained for backward compatibility.
type PodCliqueSetReplicaRollingUpdateProgress struct {
	// ReplicaIndex is the replica index of the PodCliqueSet that is being updated.
	ReplicaIndex int32 `json:"replicaIndex"`
	// UpdateStartedAt is the time at which the rolling update started for this PodCliqueSet replica index.
	UpdateStartedAt metav1.Time `json:"updateStartedAt,omitempty"`
}

// PodCliqueSetUpdateProgress captures the progress of an update of the PodCliqueSet.
type PodCliqueSetUpdateProgress struct {
	// UpdateStartedAt is the time at which the update started for the PodCliqueSet.
	UpdateStartedAt metav1.Time `json:"updateStartedAt,omitempty"`
	// UpdateEndedAt is the time at which Grove does not have any work pending to manifest the update according to the
	// configured update strategy.
	// For auto update strategies where Grove handles the orchestration, while the update is still in progress it will be
	// nil, and will be set once the update finishes where all child resources are updated by Grove with the latest
	// specification.
	// For the OnDelete strategy, it is set to the same time as UpdateStartedAt, which implies that there is no work
	// pending on Grove.
	// +optional
	UpdateEndedAt *metav1.Time `json:"updateEndedAt,omitempty"`
	// UpdatedPodCliqueScalingGroups is a list of PodCliqueScalingGroup names that have been updated to the desired
	// PodCliqueSet generation hash.
	UpdatedPodCliqueScalingGroups []string `json:"updatedPodCliqueScalingGroups,omitempty"`
	// UpdatedPodCliques is a list of PodClique names that have been updated to the desired PodCliqueSet generation hash.
	UpdatedPodCliques []string `json:"updatedPodCliques,omitempty"`
	// CurrentlyUpdating captures the progress of the PodCliqueSet replicas that are currently being updated.
	// This field is only set for auto update strategies where Grove handles the orchestration. It is not set for the
	// OnDelete update strategy.
	// +optional
	CurrentlyUpdating []PodCliqueSetReplicaUpdateProgress `json:"currentlyUpdating,omitempty"`
}

// PodCliqueSetReplicaUpdateProgress captures the progress of an update for a specific PodCliqueSet replica.
type PodCliqueSetReplicaUpdateProgress struct {
	// ReplicaIndex is the replica index of the PodCliqueSet that is being updated.
	ReplicaIndex int32 `json:"replicaIndex"`
	// UpdateStartedAt is the time at which the update started for this PodCliqueSet replica index.
	UpdateStartedAt metav1.Time `json:"updateStartedAt,omitempty"`
	// UpdateEndedAt is the time at which the update ended for this PodCliqueSet replica index.
	// The update ends when all child resources have been updated with the latest specification, when all Pods are
	// running the latest specification.
	// +optional
	UpdateEndedAt *metav1.Time `json:"updateEndedAt,omitempty"`
}

// PodCliqueSetTemplateSpec defines a template spec for a PodGang.
// A PodGang does not have a RestartPolicy field because the restart policy is predefined:
// If the number of pods in any of the cliques falls below the threshold, the entire PodGang will be restarted.
// The threshold is determined by either:
// - The value of "MinReplicas", if specified in the ScaleConfig of that clique, or
// - The "Replicas" value of that clique
type PodCliqueSetTemplateSpec struct {
	// Cliques is a slice of cliques that make up the PodGang. There should be at least one PodClique.
	// +listType=map
	// +listMapKey=name
	Cliques []*PodCliqueTemplateSpec `json:"cliques"`
	// StartupType defines the type of startup dependency amongst the cliques within a PodGang.
	// If it is not defined then default of CliqueStartupTypeAnyOrder is used.
	// +kubebuilder:default=CliqueStartupTypeAnyOrder
	// +optional
	StartupType *CliqueStartupType `json:"cliqueStartupType,omitempty"`
	// PriorityClassName is the name of the PriorityClass to be used for the PodCliqueSet.
	// If specified, indicates the priority of the PodCliqueSet. "system-node-critical" and
	// "system-cluster-critical" are two special keywords which indicate the
	// highest priorities with the former being the highest priority. Any other
	// name must be defined by creating a PriorityClass object with that name.
	// If not specified, the pod priority will be default or zero if there is no default.
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`
	// HeadlessServiceConfig defines the config options for the headless service.
	// If present, create headless service for each PodGang.
	// +optional
	HeadlessServiceConfig *HeadlessServiceConfig `json:"headlessServiceConfig,omitempty"`
	// TopologyConstraint defines topology placement requirements for PodCliqueSet.
	// +optional
	TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`
	// TerminationDelay is the delay after which the gang termination will be triggered.
	// A gang is a candidate for termination if number of running pods fall below a threshold for any PodClique.
	// If a PodGang remains a candidate past TerminationDelay then it will be terminated. This allows additional time
	// to the backend scheduler to re-schedule sufficient pods in the PodGang that will result in having the total number of
	// running pods go above the threshold.
	// Defaults to 4 hours.
	// +optional
	TerminationDelay *metav1.Duration `json:"terminationDelay,omitempty"`
	// ResourceClaimTemplates declares named ResourceClaimTemplateSpecs that can be
	// referenced by name from resourceSharing fields at any level in the hierarchy.
	// +optional
	ResourceClaimTemplates []ResourceClaimTemplateConfig `json:"resourceClaimTemplates,omitempty"`
	// ResourceSharing defines shared ResourceClaims at the PCS level.
	// Each entry references a template (internal or external) and specifies a Scope:
	//   - AllReplicas: one RC for the entire PCS, shared across ALL pods in ALL replicas
	//   - PerReplica: one RC per PCS replica, shared across ALL pods in that replica
	// The optional Filter field controls which children receive the claims.
	// +optional
	ResourceSharing []PCSResourceSharingSpec `json:"resourceSharing,omitempty"`
	// PodCliqueScalingGroupConfigs is a list of scaling groups for the PodCliqueSet.
	PodCliqueScalingGroupConfigs []PodCliqueScalingGroupConfig `json:"podCliqueScalingGroups,omitempty"`
}

// PodCliqueTemplateSpec defines a template spec for a PodClique.
type PodCliqueTemplateSpec struct {
	// Name must be unique within a PodCliqueSet and is used to denote a role.
	// Once set it cannot be updated.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/names#names
	Name string `json:"name"`
	// Labels is a map of string keys and values that can be used to organize and categorize
	// (scope and select) objects. May match selectors of replication controllers
	// and services.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/labels
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// Annotations is an unstructured key value map stored with a resource that may be
	// set by external tools to store and retrieve arbitrary metadata. They are not
	// queryable and should be preserved when modifying objects.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/annotations
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
	// TopologyConstraint defines topology placement requirements for PodClique.
	// Must be equal to or stricter than parent resource constraints.
	// +optional
	TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`
	// ResourceSharing defines shared ResourceClaims for this PodClique.
	// Each entry references a template (internal or external) and specifies a Scope:
	//   - AllReplicas: one RC per PCLQ, shared by all replica pods
	//   - PerReplica: one RC per PCLQ replica, shared by all pods within that replica
	// This is distinct from adding ResourceClaimTemplate inside
	// Spec.PodSpec.ResourceClaims[x].ResourceClaimTemplateName, which creates a unique
	// ResourceClaim for each pod.
	// PCLQs have no children to filter, so no Filter field is available.
	// +optional
	ResourceSharing []ResourceSharingSpecBase `json:"resourceSharing,omitempty"`
	// Specification of the desired behavior of a PodClique.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#spec-and-status
	Spec PodCliqueSpec `json:"spec"`
}

// TopologyConstraint defines topology placement requirements.
type TopologyConstraint struct {
	// PackDomain specifies the topology domain for grouping replicas.
	// Controls placement constraint for EACH individual replica instance.
	// Must be one of: region, zone, datacenter, block, rack, host, numa
	// Example: "rack" means each replica independently placed within one rack.
	// Note: Does NOT constrain all replicas to the same rack together.
	// Different replicas can be in different topology domains.
	// +kubebuilder:validation:Enum=region;zone;datacenter;block;rack;host;numa
	PackDomain TopologyDomain `json:"packDomain"`
}

// PodCliqueScalingGroupConfig is a group of PodClique's that are scaled together.
// Each member PodClique.Replicas will be computed as a product of PodCliqueScalingGroupConfig.Replicas and PodCliqueTemplateSpec.Spec.Replicas.
// NOTE: If a PodCliqueScalingGroupConfig is defined, then for the member PodClique's, individual AutoScalingConfig cannot be defined.
type PodCliqueScalingGroupConfig struct {
	// Name is the name of the PodCliqueScalingGroupConfig. This should be unique within the PodCliqueSet.
	// It allows consumers to give a semantic name to a group of PodCliques that needs to be scaled together.
	Name string `json:"name"`
	// CliqueNames is the list of names of the PodClique's that are part of the scaling group.
	CliqueNames []string `json:"cliqueNames"`
	// Replicas is the desired number of replicas for the scaling group at template level.
	// This allows one to control the replicas of the scaling group at startup.
	// If not specified, it defaults to 1.
	// +optional
	// +kubebuilder:default=1
	Replicas *int32 `json:"replicas,omitempty"`
	// MinAvailable serves two purposes:
	// Gang Scheduling:
	// It defines the minimum number of replicas that are guaranteed to be gang scheduled.
	// Gang Termination:
	// It defines the minimum requirement of available replicas for a PodCliqueScalingGroup.
	// Violation of this threshold for a duration beyond TerminationDelay will result in termination of the PodCliqueSet replica that it belongs to.
	// Default: If not specified, it defaults to 1.
	// Constraints:
	// MinAvailable cannot be greater than Replicas.
	// If ScaleConfig is defined then its MinAvailable should not be less than ScaleConfig.MinReplicas.
	// +optional
	// +kubebuilder:default=1
	MinAvailable *int32 `json:"minAvailable,omitempty"`
	// ScaleConfig is the horizontal pod autoscaler configuration for the pod clique scaling group.
	// +optional
	ScaleConfig *AutoScalingConfig `json:"scaleConfig,omitempty"`
	// ResourceSharing defines shared ResourceClaims at the PCSG level.
	// Each entry references a template (internal or external) and specifies a Scope:
	//   - AllReplicas: one RC for the entire PCSG, shared across all replicas
	//   - PerReplica: one RC per PCSG replica, shared across all PCLQs in that replica
	// The optional Filter field controls which PodCliques receive the claims.
	// At PCSG level, only childCliqueNames filtering is available.
	// +optional
	ResourceSharing []PCSGResourceSharingSpec `json:"resourceSharing,omitempty"`
	// TopologyConstraint defines topology placement requirements for PodCliqueScalingGroup.
	// Must be equal to or stricter than parent PodCliqueSet constraints.
	// +optional
	TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`
}

// ResourceSharingScope defines the sharing scope for resource claims.
// +kubebuilder:validation:Enum=AllReplicas;PerReplica
type ResourceSharingScope string

const (
	// ResourceSharingScopeAllReplicas creates one ResourceClaim per instance of the owning
	// resource (PCS, PCLQ, or PCSG), shared across all replicas and pods within that instance.
	ResourceSharingScopeAllReplicas ResourceSharingScope = "AllReplicas"
	// ResourceSharingScopePerReplica creates one ResourceClaim per replica, shared
	// across all pods within that replica.
	ResourceSharingScopePerReplica ResourceSharingScope = "PerReplica"
)

// ResourceClaimTemplateConfig defines a named ResourceClaimTemplateSpec that can be
// referenced by ResourceSharingSpec entries in resourceSharing fields.
type ResourceClaimTemplateConfig struct {
	// Name is a unique identifier for this template within the PodCliqueSet.
	Name string `json:"name"`
	// TemplateSpec is the ResourceClaimTemplate spec used to create ResourceClaim objects.
	TemplateSpec resourcev1.ResourceClaimTemplateSpec `json:"templateSpec"`
}

// ResourceSharingSpecBase contains the common fields shared by all levels of
// resource sharing (PCS, PCSG, PCLQ). It is used directly for PCLQ-level
// resource sharing where no filter is needed.
type ResourceSharingSpecBase struct {
	// Name of the referenced template. Resolved by first looking up
	// PodCliqueSetTemplateSpec.ResourceClaimTemplates; if no match is found,
	// the operator looks for a Kubernetes ResourceClaimTemplate object in the
	// target namespace. Internal templates shadow external ones with the same name.
	Name string `json:"name"`
	// Namespace of the external ResourceClaimTemplate. When set, the name is
	// resolved as an external Kubernetes ResourceClaimTemplate in the given
	// namespace. When empty, defaults to the PCS namespace during resolution.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// Scope determines the sharing granularity for the ResourceClaims created from
	// this template.
	Scope ResourceSharingScope `json:"scope"`
}

// PCSResourceSharingSpec defines resource sharing at the PCS level. The filter
// can target both child PodCliques and child PodCliqueScalingGroups.
type PCSResourceSharingSpec struct {
	ResourceSharingSpecBase `json:",inline"`
	// Filter narrows the scope by restricting which children receive the
	// ResourceClaims. If absent, all children receive them (broadcast).
	// +optional
	Filter *PCSResourceSharingFilter `json:"filter,omitempty"`
}

// PCSResourceSharingFilter controls which children of a PCS receive the ResourceClaims.
type PCSResourceSharingFilter struct {
	// ChildCliqueNames limits distribution to the named immediate child PodCliques.
	// +optional
	ChildCliqueNames []string `json:"childCliqueNames,omitempty"`
	// ChildScalingGroupNames limits distribution to the named immediate child PodCliqueScalingGroups.
	// +optional
	ChildScalingGroupNames []string `json:"childScalingGroupNames,omitempty"`
}

// PCSGResourceSharingSpec defines resource sharing at the PCSG level. The filter
// can only target child PodCliques within the scaling group.
type PCSGResourceSharingSpec struct {
	ResourceSharingSpecBase `json:",inline"`
	// Filter narrows the scope by restricting which child PodCliques receive
	// the ResourceClaims. If absent, all PodCliques in the group receive them.
	// +optional
	Filter *PCSGResourceSharingFilter `json:"filter,omitempty"`
}

// PCSGResourceSharingFilter controls which child PodCliques of a PCSG receive the ResourceClaims.
type PCSGResourceSharingFilter struct {
	// ChildCliqueNames limits distribution to the named child PodCliques within this scaling group.
	// +optional
	ChildCliqueNames []string `json:"childCliqueNames,omitempty"`
}

// HeadlessServiceConfig defines the config options for the headless service.
type HeadlessServiceConfig struct {
	// PublishNotReadyAddresses if set to true will publish the DNS records of pods even if the pods are not ready.
	//  if not set, it defaults to true.
	// +kubebuilder:default=true
	PublishNotReadyAddresses bool `json:"publishNotReadyAddresses"`
}

// UpdateStrategyType defines the type of update strategy for PodCliqueSet.
// +kubebuilder:validation:Enum={RollingRecreate,OnDelete}
type UpdateStrategyType string

const (
	// RollingRecreateStrategy indicates that replicas will be progressively
	// deleted and recreated one at a time, when templates change. This applies to
	// both pods (for standalone PodCliques) and replicas of PodCliqueScalingGroups.
	// RollingRecreateStrategy qualifies as an auto update strategy in Grove since
	// it handles the orchestration entirely by itself.
	// This is the default update strategy.
	RollingRecreateStrategy UpdateStrategyType = "RollingRecreate"
	// OnDeleteStrategy indicates that replicas will only be updated when
	// they are manually deleted. Changes to templates do not automatically
	// trigger replica deletions.
	OnDeleteStrategy UpdateStrategyType = "OnDelete"
)

// CliqueStartupType defines the order in which each PodClique is started.
// +kubebuilder:validation:Enum={CliqueStartupTypeAnyOrder,CliqueStartupTypeInOrder,CliqueStartupTypeExplicit}
type CliqueStartupType string

const (
	// CliqueStartupTypeAnyOrder defines that the cliques can be started in any order. This allows for concurrent starts of cliques.
	// This is the default CliqueStartupType.
	CliqueStartupTypeAnyOrder CliqueStartupType = "CliqueStartupTypeAnyOrder"
	// CliqueStartupTypeInOrder defines that the cliques should be started in the order they are defined in the PodGang Cliques slice.
	CliqueStartupTypeInOrder CliqueStartupType = "CliqueStartupTypeInOrder"
	// CliqueStartupTypeExplicit defines that the cliques should be started after the cliques defined in PodClique.StartsAfter have started.
	CliqueStartupTypeExplicit CliqueStartupType = "CliqueStartupTypeExplicit"
)

// PodGangStatus defines the status of a PodGang.
type PodGangStatus struct {
	// Name is the name of the PodGang.
	Name string `json:"name"`
	// Phase is the current phase of the PodGang.
	Phase PodGangPhase `json:"phase"`
	// Conditions represents the latest available observations of the PodGang by its controller.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// PodGangPhase represents the phase of a PodGang.
// +kubebuilder:validation:Enum={Pending,Starting,Running,Failed,Succeeded}
type PodGangPhase string

const (
	// PodGangPending indicates that the pods in a PodGang have not yet been taken up for scheduling.
	PodGangPending PodGangPhase = "Pending"
	// PodGangStarting indicates that the pods are bound to nodes by the scheduler and are starting.
	PodGangStarting PodGangPhase = "Starting"
	// PodGangRunning indicates that the all the pods in a PodGang are running.
	PodGangRunning PodGangPhase = "Running"
	// PodGangFailed indicates that one or more pods in a PodGang have failed.
	// This is a terminal state and is typically used for batch jobs.
	PodGangFailed PodGangPhase = "Failed"
	// PodGangSucceeded indicates that all the pods in a PodGang have succeeded.
	// This is a terminal state and is typically used for batch jobs.
	PodGangSucceeded PodGangPhase = "Succeeded"
)

// LastOperationType is a string alias for the type of the last operation.
type LastOperationType string

const (
	// LastOperationTypeReconcile indicates that the last operation was a reconcile operation.
	LastOperationTypeReconcile LastOperationType = "Reconcile"
	// LastOperationTypeDelete indicates that the last operation was a delete operation.
	LastOperationTypeDelete LastOperationType = "Delete"
)

// LastOperationState is a string alias for the state of the last operation.
type LastOperationState string

const (
	// LastOperationStateProcessing indicates that the last operation is in progress.
	LastOperationStateProcessing LastOperationState = "Processing"
	// LastOperationStateSucceeded indicates that the last operation succeeded.
	LastOperationStateSucceeded LastOperationState = "Succeeded"
	// LastOperationStateError indicates that the last operation completed with errors and will be retried.
	LastOperationStateError LastOperationState = "Error"
)

// LastOperation captures the last operation done by the respective reconciler on the PodCliqueSet.
type LastOperation struct {
	// Type is the type of the last operation.
	Type LastOperationType `json:"type"`
	// State is the state of the last operation.
	State LastOperationState `json:"state"`
	// Description is a human-readable description of the last operation.
	Description string `json:"description"`
	// LastUpdateTime is the time at which the last operation was updated.
	LastUpdateTime metav1.Time `json:"lastUpdateTime"`
}

// ErrorCode is a custom error code that uniquely identifies an error.
type ErrorCode string

// LastError captures the last error observed by the controller when reconciling an object.
type LastError struct {
	// Code is the error code that uniquely identifies the error.
	Code ErrorCode `json:"code"`
	// Description is a human-readable description of the error.
	Description string `json:"description"`
	// ObservedAt is the time at which the error was observed.
	ObservedAt metav1.Time `json:"observedAt"`
}

// SetLastErrors sets the last errors observed by the controller when reconciling the PodCliqueSet.
func (pcs *PodCliqueSet) SetLastErrors(lastErrs ...LastError) {
	pcs.Status.LastErrors = lastErrs
}

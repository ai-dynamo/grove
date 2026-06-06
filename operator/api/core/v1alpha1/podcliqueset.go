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
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="Available",type=integer,JSONPath=`.status.availableReplicas`
// +kubebuilder:printcolumn:name="Updated",type=integer,JSONPath=`.status.updatedReplicas`
// +kubebuilder:printcolumn:name="PCLQs-Updated",type=integer,JSONPath=`.status.updateProgress.updatedPodCliquesCount`
// +kubebuilder:printcolumn:name="PCLQs-Total",type=integer,JSONPath=`.status.updateProgress.totalPodCliquesCount`
// +kubebuilder:printcolumn:name="PCSGs-Updated",type=integer,JSONPath=`.status.updateProgress.updatedPodCliqueScalingGroupsCount`
// +kubebuilder:printcolumn:name="PCSGs-Total",type=integer,JSONPath=`.status.updateProgress.totalPodCliqueScalingGroupsCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="oldSelf.hasValue() || !has(self.spec.template.topologyConstraint) || !has(self.spec.template.topologyConstraint.packDomain)",message="packDomain is deprecated and cannot be used on new workloads; use pack.required",fieldPath=".spec.template.topologyConstraint.packDomain",optionalOldSelf=true,reason=FieldValueForbidden
// +kubebuilder:validation:XValidation:rule="oldSelf.hasValue() || !has(self.spec.template.cliques) || self.spec.template.cliques.all(c, !has(c.topologyConstraint) || !has(c.topologyConstraint.packDomain))",message="packDomain is deprecated and cannot be used on new workloads; use pack.required",fieldPath=".spec.template.cliques",optionalOldSelf=true,reason=FieldValueForbidden
// +kubebuilder:validation:XValidation:rule="oldSelf.hasValue() || !has(self.spec.template.podCliqueScalingGroups) || self.spec.template.podCliqueScalingGroups.all(g, !has(g.topologyConstraint) || !has(g.topologyConstraint.packDomain))",message="packDomain is deprecated and cannot be used on new workloads; use pack.required",fieldPath=".spec.template.podCliqueScalingGroups",optionalOldSelf=true,reason=FieldValueForbidden

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
	// Deprecated: Status of the PodGang should be captured as part of the PodGang resource.
	// This field is not been set today and will be removed in the future.
	PodGangStatutes []PodGangStatus `json:"podGangStatuses,omitempty"`
	// CurrentGenerationHash is a hash value generated out of a collection of fields in a PodCliqueSet.
	// Since only a subset of fields is taken into account when generating the hash, not every change in the PodCliqueSetSpec will
	// be accounted for when generating this hash value. A field in PodCliqueSetSpec is included if a change to it triggers
	// a rolling recreate of PodCliques and/or PodCliqueScalingGroups.
	// Only if this value is not nil and the newly computed hash value is different from the persisted CurrentGenerationHash value
	// then an update needs to be triggered.
	CurrentGenerationHash *string `json:"currentGenerationHash,omitempty"`
	// UpdateProgress represents the progress of an update.
	UpdateProgress *PodCliqueSetUpdateProgress `json:"updateProgress,omitempty"`
}

// UpdateStrategyType defines the type of update strategy for PodCliqueSet.
// +kubebuilder:validation:Enum={Coherent,RollingRecreate,OnDelete}
type UpdateStrategyType string

const (
	// CoherentStrategy indicates that replicas will be updated in Minimal Viable Units —
	// MinAvailable replicas of each updated standalone PodClique plus MinAvailable replicas of each
	// updated PodCliqueScalingGroup — scheduled atomically as a new PodGang. This guarantees
	// that pods forming a minimum-viable serving unit are always version-compatible.
	// This is the default update strategy.
	CoherentStrategy UpdateStrategyType = "Coherent"
	// RollingRecreateStrategy indicates that replicas will be progressively deleted and recreated
	// one at a time when templates change. This applies to both pods (for standalone PodCliques)
	// and replicas of PodCliqueScalingGroups.
	RollingRecreateStrategy UpdateStrategyType = "RollingRecreate"
	// OnDeleteStrategy indicates that replicas will only be updated when they are manually deleted.
	// Changes to templates do not automatically trigger replica deletions.
	OnDeleteStrategy UpdateStrategyType = "OnDelete"
)

// PodCliqueSetUpdateStrategy defines the update strategy for a PodCliqueSet.
type PodCliqueSetUpdateStrategy struct {
	// Type indicates the type of update strategy.
	// This strategy applies uniformly to both standalone PodCliques and
	// PodCliqueScalingGroups within the PodCliqueSet.
	// Default is Coherent.
	// +kubebuilder:default=Coherent
	Type UpdateStrategyType `json:"type,omitempty"`
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
	// UpdatedPodCliquesCount is the number of PodCliques that have been updated to the desired PodCliqueSet
	// generation hash. Recomputed each reconcile from child generation-hash labels.
	// +optional
	// +kubebuilder:default=0
	UpdatedPodCliquesCount int32 `json:"updatedPodCliquesCount,omitempty"`
	// TotalPodCliquesCount is the total number of PodCliques expected to exist for the PodCliqueSet at the
	// current spec.
	// +optional
	// +kubebuilder:default=0
	TotalPodCliquesCount int32 `json:"totalPodCliquesCount,omitempty"`
	// UpdatedPodCliqueScalingGroupsCount is the number of PodCliqueScalingGroups that have been updated to the
	// desired PodCliqueSet generation hash.
	// +optional
	// +kubebuilder:default=0
	UpdatedPodCliqueScalingGroupsCount int32 `json:"updatedPodCliqueScalingGroupsCount,omitempty"`
	// TotalPodCliqueScalingGroupsCount is the total number of PodCliqueScalingGroups expected to exist for the
	// PodCliqueSet at the current spec.
	// +optional
	// +kubebuilder:default=0
	TotalPodCliqueScalingGroupsCount int32 `json:"totalPodCliqueScalingGroupsCount,omitempty"`
	// CurrentlyUpdating captures the progress of the PodCliqueSet replicas that are currently being updated.
	// This field is only set for auto update strategies where Grove handles the orchestration. It is not set for the
	// OnDelete update strategy.
	// +optional
	CurrentlyUpdating []PodCliqueSetReplicaUpdateProgress `json:"currentlyUpdating,omitempty"`
	// UpdatedStandalonePodCliques captures the names of standalone PodCliques whose pod template was detected
	// as out-of-date when this coherent update started. The set is frozen for the lifetime of the update
	// (cleared together with UpdateEndedAt). The MVU machinery uses this set, in combination with the live PCS
	// spec, to compute the MVU template; a fresh recomputation from live PCLQ hashes would shrink the set as
	// pods roll over and break the MVU invariants. Only populated for the Coherent strategy.
	// +optional
	UpdatedStandalonePodCliques []string `json:"updatedStandalonePodCliques,omitempty"`
	// UpdatedPodCliqueScalingGroups captures the config names of PodCliqueScalingGroups that had at least one
	// constituent PodClique detected as out-of-date when this coherent update started. Same lifetime semantics
	// as UpdatedStandalonePodCliques. Only populated for the Coherent strategy.
	// +optional
	UpdatedPodCliqueScalingGroups []string `json:"updatedPodCliqueScalingGroups,omitempty"`
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
	// InFlightPodGangs are the names of PodGangs that are part of the current update
	// iteration for this replica. The orchestrator waits for all of them to become
	// available before advancing to the next iteration.
	// +optional
	InFlightPodGangs []string `json:"inFlightPodGangs,omitempty"`
	// ErrorMessage captures the reason the update of this replica is stalled or failing, if any.
	// +optional
	ErrorMessage *string `json:"errorMessage,omitempty"`
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
	ResourceSharing []ResourceSharingSpec `json:"resourceSharing,omitempty"`
	// Specification of the desired behavior of a PodClique.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#spec-and-status
	Spec PodCliqueSpec `json:"spec"`
}

// TopologyConstraint defines topology placement requirements.
// +kubebuilder:validation:XValidation:rule="has(self.pack) || has(self.packDomain)",message="topologyConstraint must specify pack or deprecated packDomain",fieldPath=".pack",reason=FieldValueRequired
// +kubebuilder:validation:XValidation:rule="!(has(self.packDomain) && has(self.pack) && has(self.pack.required))",message="must not set both pack.required and deprecated packDomain",fieldPath=".pack.required"
type TopologyConstraint struct {
	// TopologyName is the name of the ClusterTopologyBinding resource to use for topology-aware scheduling.
	// Setting TopologyName may be optional if the name can be inherited from a higher level scope.
	// When TopologyName is specified at a PCS/PCSG/PCLQ resource constraint, it will also be inherited
	// as the default ClusterTopologyBinding name on all sub-resources, unless overridden by another TopologyName
	// at a sub-resource.
	// For example, setting TopologyName at a PCS level makes it optional for child PCSG or PCLQ levels
	// when the sub-resources reuse the same ClusterTopologyBinding.
	// Immutable after creation.
	// +optional
	TopologyName string `json:"topologyName,omitempty"`

	// Pack specifies topology packing constraints for each replica of the resource.
	// +optional
	Pack *TopologyPackConstraint `json:"pack,omitempty"`

	// PackDomain specifies the required topology domain using the legacy field name.
	// Controls placement constraint for EACH individual replica instance.
	// Must reference a domain in the topology levels defined in the ClusterTopologyBinding named by TopologyName.
	// Example: "rack" means each replica independently placed within one rack.
	// Note: Does NOT constrain all replicas to the same rack together.
	// Different replicas can be in different topology domains.
	// Deprecated: use Pack.RequiredDomain.
	// +optional
	PackDomain TopologyDomain `json:"packDomain,omitempty"`
}

// TopologyPackConstraint defines topology pack placement requirements.
// +kubebuilder:validation:XValidation:rule="has(self.required) || has(self.preferred)",message="pack must specify at least one of required or preferred",reason=FieldValueRequired
type TopologyPackConstraint struct {
	// RequiredDomain specifies the required topology packing constraint of each replica of the resource.
	// The workload will not be scheduled if this constraint cannot be satisfied.
	// Must reference a domain in the topology levels defined in the selected ClusterTopologyBinding.
	// +optional
	RequiredDomain TopologyDomain `json:"required,omitempty"`

	// PreferredDomain specifies a preferred best-effort topology domain.
	// If the constraint cannot be satisfied, the workload is scheduled anyway.
	// +optional
	PreferredDomain TopologyDomain `json:"preferred,omitempty"`
}

// HasAnyPackDomain reports whether the constraint has any required or preferred pack domain.
func (tc *TopologyConstraint) HasAnyPackDomain() bool {
	return tc != nil && (tc.RequiredDomain() != "" || tc.PreferredDomain() != "")
}

// RequiredDomain returns the required pack domain, falling back to legacy packDomain.
func (tc *TopologyConstraint) RequiredDomain() TopologyDomain {
	if tc == nil {
		return ""
	}
	if tc.Pack != nil && tc.Pack.RequiredDomain != "" {
		return tc.Pack.RequiredDomain
	}
	return tc.PackDomain
}

// PreferredDomain returns the preferred pack domain.
func (tc *TopologyConstraint) PreferredDomain() TopologyDomain {
	if tc == nil || tc.Pack == nil {
		return ""
	}
	return tc.Pack.PreferredDomain
}

// ReferencedDomains returns the unique required and preferred domains referenced by the constraint.
func (tc *TopologyConstraint) ReferencedDomains() []TopologyDomain {
	if tc == nil {
		return nil
	}
	var domains []TopologyDomain
	required := tc.RequiredDomain()
	if required != "" {
		domains = append(domains, required)
	}
	if preferred := tc.PreferredDomain(); preferred != "" && preferred != required {
		domains = append(domains, preferred)
	}
	return domains
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
	// Annotations is an unstructured key value map stored with a resource that may be
	// set by external tools to store and retrieve arbitrary metadata. They are not
	// queryable and should be preserved when modifying objects.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/annotations
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
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

// ResourceSharingSpec contains the common fields shared by all levels of
// resource sharing (PCS, PCSG, PCLQ). It is used directly for PCLQ-level
// resource sharing where no filter is needed.
type ResourceSharingSpec struct {
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
	ResourceSharingSpec `json:",inline"`
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
	ResourceSharingSpec `json:",inline"`
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
// Deprecated
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
// Deprecated
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

# GREP-291: `OnDelete` Update Strategy for PodCliqueSets

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [<code>RollingRecreate</code>](#rollingrecreate)
  - [<code>OnDelete</code>](#ondelete)
  - [User Stories](#user-stories)
    - [Story 1: Sophisticated application updates](#story-1-sophisticated-application-updates)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
    - [UpdateStrategyType](#updatestrategytype)
    - [PodCliqueSetSpec Changes](#podcliquesetspec-changes)
    - [PodCliqueSetStatus Changes](#podcliquesetstatus-changes)
    - [PodCliqueStatus Changes](#podcliquestatus-changes)
    - [PodCliqueScalingGroupStatus Changes](#podcliquescalinggroupstatus-changes)
  - [Behavior Details](#behavior-details)
    - [RollingRecreate Strategy (Default)](#rollingrecreate-strategy-default)
    - [OnDelete Strategy](#ondelete-strategy)
  - [Example Usage](#example-usage)
  - [Monitoring](#monitoring)
  - [Implementation Phases](#implementation-phases)
    - [Phase 1: Standalone PodCliques, and PodCliqueScalingGroups implementation](#phase-1-standalone-podcliques-and-podcliquescalinggroups-implementation)
    - [Phase 2: Comprehensive tests](#phase-2-comprehensive-tests)
  - [Test Plan](#test-plan)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This GREP introduces a new `OnDelete` update strategy for `PodCliqueSet` that allows users to update `PodClique` specifications (`podSpec` fields such as `image`) without automatically triggering pod deletions/restarts. With `OnDelete` strategy, only pods that are manually deleted are recreated with the new specification, enabling non-disruptive updates.

Currently, Grove implements a `RollingRecreate` strategy that automatically deletes and recreates replicas one at a time (PodCliqueScalingGroup replica for PodCliqueScalingGroups, and Pod for PodCliques) when templates are updated. To support the new `OnDelete` strategy alongside the existing `RollingRecreate` behavior, this GREP introduces a new `updateStrategy` field to the `PodCliqueSet` spec, enabling users to explicitly choose their preferred update strategy by specifying `.spec.updateStrategy.type`.

## Motivation

Grove currently only supports updates through its default `RollingRecreate` strategy, and the orchestration of updates is fully handled by grove. However, there could be certain sophisticated use cases where users might want to orchestrate the updates themselves, instead of relying on grove.

Currently, when a user updates a standalone PodClique's `podTemplate`, grove triggers a rolling recreate that deletes and recreates **all** pods of that standalone PodClique one at a time, *including unaffected ones*. This behavior is disruptive and wasteful for workloads that only need to recreate and reschedule a subset of pods. Similar behavior is observed in PodCliqueScalingGroups where a rolling recreate deletes and recreates **all** replicas of that PodCliqueScalingGroup one at a time.

To solve the current limitation stated above, we propose to introduce a new update strategy `OnDelete` (inspired by Kubernetes StatefulSet), users gain fine-grained control over when pods are updated. The new specification is applied only when a pod is manually deleted, allowing unaffected pods to continue running undisturbed.

### Goals

- Introduce a new `spec.updateStrategy` field to `PodCliqueSet` that allows users to choose between `RollingRecreate` (default) and `OnDelete` update strategy types.
- The update strategy applies uniformly to both standalone PodCliques and PodCliqueScalingGroups within a PodCliqueSet.
- When `OnDelete` strategy is configured:
  - Changes to the template do not automatically trigger pod deletions.
  - Existing pods continue running with their original specification until manually deleted.
  - New pods (created due to scale-out or manual deletion) use the updated template.
  - Pod deletions prefer pods with the older template.
- Maintain backward compatibility by defaulting to the current `RollingRecreate` behavior.

### Non-Goals

- Defining update strategy at each hierarchy level (PodCliqueSet, PodCliqueScalingGroup, PodClique) individually (following the design used for [topology configuration](/docs/proposals/244-topology-aware-scheduling/README.md) in a PodCliqueSet). This can be addressed in a future GREP to allow different strategies for different components.

## Proposal

Introduce a new update strategy in grove called `OnDelete` that enables users to orchestrate updates themselves, configurable through the `.spec.updateStrategy` field of the PodCliqueSet.

Two update strategy types will be supported:

### `RollingRecreate`

Pods are automatically updated when the template changes. In this strategy, pods of a PodClique are deleted first, and then created with the new template. The operator achieves this by:
- Either deleting the Pods of a standalone PodClique (created with the new template by the PodClique controller)
- Or deleting the PodCliques that belong to a specific replica of the PodCliqueScalingGroup (created with the new template by the PodCliqueSet or PodCliqueScalingGroup controller)

This strategy is already supported and is the default strategy, and can be explicitly set by specifying `.spec.updateStrategy.type: RollingRecreate` on a PodCliqueSet.

### `OnDelete`

Pods are not automatically updated when the template changes. The new specification is applied only when pods are manually deleted by the user, and subsequently created by the controller.

The `OnDelete` update strategy provides manual control over when parts of a PodCliqueSet adopt the latest template changes. With this strategy:  

**Template Changes Do Not Trigger Automatic Updates:**  
- When the pod template is modified in `spec.cliques[*].spec`, the controller **will not** automatically recreate any existing pods.
- The updates to Pod templates in a PodCliqueSet are stored and become the "desired" specification, but existing pods continue running with their current (potentially outdated) specification.

**Manual Deletion Triggers Updates:**  
- Pods are only updated when they are *manually deleted* by a user or external process (e.g., through `kubectl delete pod`, node eviction, or manual replica deletion).  
- When a pod (or a PodClique) is deleted (for any reason), the controller creates a replacement using the **current (updated) template** from the PodCliqueSet specification.

**Scale-Out uses updated template:**
- Any new pods created due to scale-out operations will always use the current (updated) template specification.
- This ensures that scaling operations benefit from the latest configuration even if existing replicas have not been updated.

**Preferential Deletion During Scale-In:**
- *For Standalone PodCliques*: When scaling down (`spec.cliques[*].spec.replicas` is reduced), the controller preferentially selects pods running with outdated templates for deletion. This helps accelerate convergence to the desired specification during normal scaling operations.
- *For PodCliqueScalingGroups*: Scale-in continues to delete replicas with the highest indices to prevent gaps in the replica set, regardless of template version.

**Visibility and Tracking:**

To track the status of the update, users can check the `.status.updatedReplicas` fields of the PodCliqueSet, PodCliqueScalingGroup, and PodClique resources to check the number of replicas (in the case of PodCliqueSet and PodCliqueScalingGroup) or Pods (in the case of PodCliques) that match the template specified in the PodCliqueSet.

This strategy can be set by specifying `.spec.updateStrategy.type: OnDelete` on a PodCliqueSet.
> **Note:** The user must ensure that the `OnDelete` update strategy is compatible with their applications (which might have multiple PodCliques running in HA), and that such updates do not cause the application to behave unexpectedly due to incompatibility between different versions of components running simultaneously.

### User Stories

#### Story 1: Sophisticated application updates

Consumers of grove with fairly complicated setups and requirements might want to orchestrate the updates themselves. The `RollingRecreate` behavior might not fit into how their application needs to be updated. In such cases:

With `OnDelete` strategy:
1. The consumer updates the PodCliqueSet with the new specifications for their PodCliques in the PodCliqueSet.
2. They manually delete the pods that need to be recreated with the new specifications according to their requirements on when/which/how many pods are to be updated.
3. New pods are created with the updated specifications, as and when the user wants updated pods to be created.

### Limitations/Risks & Mitigations

**Risk 1: Configuration Drift**

With `OnDelete` strategy, replicas within the same PodCliqueSet may run with different specifications for an extended period if replicas are not manually deleted.

*Mitigation*:
- The PodClique's `status.updatedReplicas` field will accurately reflect how many pods are running with the current (desired) specification.
- The PodCliqueScalingGroup's `status.updatedReplicas` field will accurately reflect how many replicas are running with the current (desired) specification.
- Clear documentation will explain the behavior and best practices for using `OnDelete` strategy.

**Risk 2: Lost control over updates**

Pod deletions due to preemption, eviction due to Node failure or any other reason can cause loss of control over orchestrating updates.

There is no mitigation for such events, the user has to be aware of the state of progress of updates in their cluster.

**Risk 3: Version incompatibility**

Components which are typically run in HA might not be compatible with older versions which make up the remaining pods of the PodClique, and also might not be compatible with older versions of other components in the system.

There is no clear mitigation for this as this incompatibility stems from the application layer. Users have to thoroughly evaluate the `OnDelete` strategy before using it, and ensure that their application can handle such updates.

**Risk 4: User Confusion**

Users may not understand why their spec changes are not being applied automatically.

*Mitigation*:
- The API will clearly document the behavior of each update strategy.

## Design Details

### API Changes

#### UpdateStrategyType

```go
// UpdateStrategyType defines the type of update strategy for PodCliqueSet.
// +kubebuilder:validation:Enum={RollingRecreate,OnDelete}
type UpdateStrategyType string

const (
    // RollingRecreateStrategyType indicates that pods and/or PodCliques will be progressively
    // deleted and created one at a time, when the template changes.
    // For standalone PodCliques, pods are deleted and created one at a time. 
    // For PodCliques belonging to PodCliqueScalingGroups, replicas of the
    // PodCliqueScalingGroup (i.e. a set of PodCliques) are deleted and created one at a time.
    // This is the default update strategy.
    RollingRecreateStrategyType UpdateStrategyType = "RollingRecreate"
    
    // OnDeleteStrategyType indicates that pods will only be updated when
    // they are manually deleted. Changes to templates do not automatically
    // trigger pod deletions.
    OnDeleteStrategyType UpdateStrategyType = "OnDelete"
)
```

#### PodCliqueSetSpec Changes

The `PodCliqueSetSpec` will be extended to include the `UpdateStrategy` (`spec.updateStrategy`) field:

```go
// PodCliqueSetUpdateStrategy defines the update strategy for a PodCliqueSet.
type PodCliqueSetUpdateStrategy struct {
    // Type indicates the type of update strategy.
    // This strategy applies uniformly to both standalone PodCliques and 
    // PodCliqueScalingGroups within the PodCliqueSet.
    // Default is RollingRecreate.
    // +kubebuilder:default=RollingRecreate
    Type UpdateStrategyType `json:"type,omitempty"`
}

// PodCliqueSetSpec defines the specification of a PodCliqueSet.
type PodCliqueSetSpec struct {
    // Replicas is the number of desired replicas of the PodCliqueSet.
    // +kubebuilder:default=0
    Replicas int32 `json:"replicas,omitempty"`
    
    // UpdateStrategy defines the strategy for updating replicas when 
    // templates change. This applies to both standalone PodCliques (pods are recreated) and
    // PodCliqueScalingGroups (PodCliqueScalingGroup replicas are recreated).
    // +optional
    UpdateStrategy *PodCliqueSetUpdateStrategy `json:"updateStrategy,omitempty"`
    
    // Template describes the template spec for PodGangs that will be 
    // created in the PodCliqueSet.
    Template PodCliqueSetTemplateSpec `json:"template"`
}
```

#### PodCliqueSetStatus Changes

A new field `PodCliqueSetUpdateProgress` (`status.updateProgress`) will be introduced, which is identical to the existing `PodCliqueSetRollingUpdateProgress` (`status.rollingUpdateProgress`). This new field will be used to generically track information about updates to the PodCliqueSet with all supported strategies. The existing `RollingUpdateProgress` field will be marked as deprecated and will be removed in a future release. Both fields will contain identical data during the deprecation period, but all future enhancements will only be made to `UpdateProgress`:

```go
// PodCliqueSetSpec defines the specification of a PodCliqueSet.
type PodCliqueSetStatus struct {
...
    // PodGangStatuses captures the status for all the PodGang's that are part of the PodCliqueSet.
    PodGangStatuses []PodGangStatus `json:"podGangStatuses,omitempty"`
    // CurrentGenerationHash is a hash value generated out of a collection of fields in a PodCliqueSet.
    // Since only a subset of fields is taken into account when generating the hash, not every change in the PodCliqueSetSpec will
    // be accounted for when generating this hash value. A field in PodCliqueSetSpec is included if a change to it triggers
    // an  update of PodCliques and/or PodCliqueScalingGroups.
    // Only if this value is not nil and the newly computed hash value is different from the persisted CurrentGenerationHash value
    // then an update needs to be triggerred.
    CurrentGenerationHash *string `json:"currentGenerationHash,omitempty"`
  	// RollingUpdateProgress represents the progress of a rolling update.
  	// Deprecated: Use UpdateProgress instead. This field is maintained for backward compatibility and will be removed in a future release.
  	RollingUpdateProgress *PodCliqueSetRollingUpdateProgress `json:"rollingUpdateProgress,omitempty"`
  	// UpdateProgress represents the progress of an update.
  	UpdateProgress *PodCliqueSetUpdateProgress `json:"updateProgress,omitempty"`
}

// PodCliqueSetUpdateProgress captures the progress of an update of the PodCliqueSet.
type PodCliqueSetUpdateProgress struct {
    // UpdateStartedAt is the time at which the update started for the PodCliqueSet.
	  UpdateStartedAt metav1.Time `json:"updateStartedAt,omitempty"`
	  // UpdateEndedAt is the time at which the update ended for the PodCliqueSet.
	  // +optional
	  UpdateEndedAt *metav1.Time `json:"updateEndedAt,omitempty"`
	  // UpdatedPodCliqueScalingGroups is a list of PodCliqueScalingGroup names that have been updated to the desired PodCliqueSet generation hash.
	  UpdatedPodCliqueScalingGroups []string `json:"updatedPodCliqueScalingGroups,omitempty"`
	  // UpdatedPodCliques is a list of PodClique names that have been updated to the desired PodCliqueSet generation hash.
	  UpdatedPodCliques []string `json:"updatedPodCliques,omitempty"`
	  // CurrentlyUpdating captures the progress of the PodCliqueSet replica that is currently being updated.
	  // +optional
	  CurrentlyUpdating *PodCliqueSetReplicaUpdateProgress `json:"currentlyUpdating,omitempty"`
}

// PodCliqueSetReplicaUpdateProgress captures the progress of an update for a specific PodCliqueSet replica.
type PodCliqueSetReplicaUpdateProgress struct {
	  // ReplicaIndex is the replica index of the PodCliqueSet that is being updated.
	  ReplicaIndex int32 `json:"replicaIndex"`
	  // UpdateStartedAt is the time at which the update started for this PodCliqueSet replica index.
	  UpdateStartedAt metav1.Time `json:"updateStartedAt,omitempty"`
}
```

#### PodCliqueStatus Changes

A new field `PodCliqueUpdateProgress` (`status.updateProgress`) will be introduced, which is identical to the existing `PodCliqueRollingUpdateProgress` (`status.rollingUpdateProgress`). This new field will be used to generically track information about updates to the PodClique with all supported strategies. The existing `RollingUpdateProgress` field will be marked as deprecated and will be removed in a future release. Both fields will contain identical data during the deprecation period, but all future enhancements will only be made to `UpdateProgress`:

```go
// PodCliqueStatus defines the status of a PodClique.
type PodCliqueStatus struct {
...
  	// CurrentPodCliqueSetGenerationHash establishes a correlation to PodCliqueSet generation hash indicating
  	// that the spec of the PodCliqueSet at this generation is fully realized in the PodClique.
  	CurrentPodCliqueSetGenerationHash *string `json:"currentPodCliqueSetGenerationHash,omitempty"`
  	// CurrentPodTemplateHash establishes a correlation to PodClique template hash indicating
  	// that the spec of the PodClique at this template hash is fully realized in the PodClique.
  	CurrentPodTemplateHash *string `json:"currentPodTemplateHash,omitempty"`
  	// RollingUpdateProgress provides details about the ongoing rolling update of the PodClique.
  	// Deprecated: Use UpdateProgress instead. This field is maintained for backward compatibility and will be removed in a future release.
  	RollingUpdateProgress *PodCliqueRollingUpdateProgress `json:"rollingUpdateProgress,omitempty"`
  	// UpdateProgress provides details about the ongoing update of the PodClique.
  	UpdateProgress *PodCliqueUpdateProgress `json:"updateProgress,omitempty"`
}

// PodCliqueUpdateProgress provides details about the ongoing update of the PodClique.
type PodCliqueUpdateProgress struct {
  	// UpdateStartedAt is the time at which the update started.
  	UpdateStartedAt metav1.Time `json:"updateStartedAt,omitempty"`
  	// UpdateEndedAt is the time at which the update ended.
  	// It will be set to nil if the update is still in progress.
  	UpdateEndedAt *metav1.Time `json:"updateEndedAt,omitempty"`
  	// PodCliqueSetGenerationHash is the PodCliqueSet generation hash corresponding to the PodCliqueSet spec that is being rolled out.
  	// While the update is in progress PodCliqueStatus.CurrentPodCliqueSetGenerationHash will not match this hash. Once the update is complete the
  	// value of this field will be copied to PodCliqueStatus.CurrentPodCliqueSetGenerationHash.
  	PodCliqueSetGenerationHash string `json:"podCliqueSetGenerationHash"`
  	// PodTemplateHash is the PodClique template hash corresponding to the PodClique spec that is being rolled out.
  	// While the update is in progress PodCliqueStatus.CurrentPodTemplateHash will not match this hash. Once the update is complete the
  	// value of this field will be copied to PodCliqueStatus.CurrentPodTemplateHash.
  	PodTemplateHash string `json:"podTemplateHash"`
  	// ReadyPodsSelectedToUpdate captures the pod names of ready Pods that are either currently being updated or have been previously updated.
  	ReadyPodsSelectedToUpdate *PodsSelectedToUpdate `json:"readyPodsSelectedToUpdate,omitempty"`
}
```

#### PodCliqueScalingGroupStatus Changes

A new field `PodCliqueScalingGroupUpdateProgress` (`status.updateProgress`) will be introduced, which is identical to the existing `PodCliqueScalingGroupRollingUpdateProgress` (`status.rollingUpdateProgress`). This new field will be used to generically track information about updates to the PodCliqueScalingGroup with all supported strategies. The existing `RollingUpdateProgress` field will be marked as deprecated and will be removed in a future release. Both fields will contain identical data during the deprecation period, but all future enhancements will only be made to `UpdateProgress`:

```go
// PodCliqueScalingGroupStatus defines the status of a PodCliqueScalingGroup.
type PodCliqueScalingGroupStatus struct {
...
	// Conditions represents the latest available observations of the PodCliqueScalingGroup by its controller.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// CurrentPodCliqueSetGenerationHash establishes a correlation to PodCliqueSet generation hash indicating
	// that the spec of the PodCliqueSet at this generation is fully realized in the PodCliqueScalingGroup.
	CurrentPodCliqueSetGenerationHash *string `json:"currentPodCliqueSetGenerationHash,omitempty"`
	// RollingUpdateProgress provides details about the ongoing update of the PodCliqueScalingGroup.
	// Deprecated: Use UpdateProgress instead. This field is maintained for backward compatibility and will be removed in a future release.
	RollingUpdateProgress *PodCliqueScalingGroupRollingUpdateProgress `json:"rollingUpdateProgress,omitempty"`
	// UpdateProgress provides details about the ongoing update of the PodCliqueScalingGroup.
	UpdateProgress *PodCliqueScalingGroupUpdateProgress `json:"updateProgress,omitempty"`
}

// PodCliqueScalingGroupUpdateProgress provides details about the ongoing update of the PodCliqueScalingGroup.
type PodCliqueScalingGroupUpdateProgress struct {
	// UpdateStartedAt is the time at which the update started.
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
```

### Behavior Details

The update strategy defined at the PodCliqueSet level applies uniformly to both standalone PodCliques and PodCliqueScalingGroups.

#### RollingRecreate Strategy (Default)

When `updateStrategy.type` is set to `RollingRecreate` (or when `updateStrategy` is not specified):

**For Standalone PodCliques:**
1. Changes to a PodCliqueSet's  `spec.cliques[*]` that affect the pod specification trigger a rolling recreate.
2. The controller progressively deletes and recreates pods to match the new specification.
3. The rolling recreate proceeds one pod at a time to minimize disruption.
4. `status.updateProgress` fields track the progress of the update at each hierarchy.

**For PodCliqueScalingGroups:**
1. Changes to a PodCliqueScalingGroup template trigger a rolling recreate of PodCliqueScalingGroup replicas.
2. The controller progressively deletes and recreates replicas to match the new specification.
3. Each replica is deleted first, then recreated with the new template.

#### OnDelete Strategy

When `updateStrategy.type` is set to `OnDelete`:

**For all PodCliques:**
1. Changes to a PodCliqueSet's `spec.cliques[*]` are stored in its template but do not trigger automatic pod deletions.
2. The PodClique's `spec` (both standalone and PodCliqueScalingGroup PodClique) is patched with the changes brought in the template at `spec.cliques[*].spec` of the PodCliqueSet.
3. Existing pods continue running with their original specification.
4. When a pod needs to be deleted (e.g., during scale-in of a standalone PodClique), pods with the older template are preferred for deletion.
5. When a pod is deleted (manually, or due to node failure/eviction):
   - The controller creates a replacement pod using the current (updated) template.
   - The new pod reflects any specification changes made since the original pod was created.
6. The PodClique's `status.updateProgress.podTemplateHash` can be compared with individual pod labels to identify which pods are running outdated specifications.
7. PodClique's `status.updatedReplicas` reflects the count of pods running with the current template.
8. `readyPodsSelectedToUpdate` is not set since the operator does not choose which replica is to be updated.
9. `readyReplicaIndicesSelectedToUpdate` of PodCliqueScalingGroups is not set since all replicas of the PodCliqueScalingGroup are updated at once.

> **Note:** The key distinction between how PodCliques of a PodCliqueScalingGroup are updated between both the strategies is the following:
> - `RollingRecreate`: PodCliques of each replica of the PodCliqueScalingGroup are all deleted together at once and then created to reflect the update.
> - `OnDelete`: All PodCliques of the PodCliqueScalingGroup are updated in-place, with their updates being identical functionally to that of standalone PodCliques.

**Key Implementation Points:**

- Logic that initiates updates through hash comparison will be enhanced to support `RollingRecreate` and `OnDelete`, by skipping the traditional `RollingRecreate` codeflow when `OnDelete` is specified.
- `status.updateProgress.updateStartedAt` and `status.updateProgress.updateEndedAt` are both set simultaneously when the `OnDelete` update is initiated on the standalone PodClique resources and the PodCliqueScalingGroup resources.
  - From an `OnDelete` update perspective, the update ends as soon as the specification of the resource is synced with the template specified in the PodCliqueSet. Thus, the timestamp is set as the same for both.
  - Gang termination is paused when a PodCliqueSet is being updated through the `RollingRecreate`. Since the user controls the time at which replicas are deleted, it is unknown when the update will end, and therefore is incorrect to pause gang termination until all resources are updated. Thus, both timestamps are set simultaneously, ensuring gang termination continues to be in effect.
- Replica creation logic will always use the latest template specification, regardless of update strategy, as it already exists.
- The `updatedReplicas` status field will be maintained to show how many replicas match the current specification in the PodCliqueSet.
- During scale-in operations for standalone PodCliques, replicas running with outdated templates will be preferentially selected for deletion.
- During scale-in operations for PodCliqueScalingGroups, replicas with the largest index are continued to be deleted as before. This ensures that no holes form in the replicas of a PodCliqueScalingGroup.

### Example Usage

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: training-workload
spec:
  replicas: 1
  updateStrategy:
    type: OnDelete
  template:
    cliques:
      - name: worker
        spec:
          roleName: worker
          replicas: 8
          podSpec:
            affinity:
              nodeAffinity:
                requiredDuringSchedulingIgnoredDuringExecution:
                  nodeSelectorTerms:
                    - matchExpressions:
                        - key: node.kubernetes.io/instance-type
                          operator: In
                          values:
                            - gpu-node
                        - key: kubernetes.io/hostname
                          operator: NotIn
                          values:
                            - failed-node-1  # Added after node failure
            containers:
              - name: trainer
                image: training-image:v1
                resources:
                  limits:
                    nvidia.com/gpu: "1"
```

With this configuration:
- Initially, 8 worker pods are scheduled across available GPU nodes.
- When `failed-node-1` encounters issues, the user updates the `nodeAffinity` to exclude it.
- Pods on healthy nodes continue running undisturbed.
- Pods on `failed-node-1` (if evicted or manually deleted) are recreated on other nodes with the updated affinity.
- If a scale-in occurs, pods still running with the old template (without the `failed-node-1` exclusion) are preferentially deleted.

The same `OnDelete` strategy applies uniformly to any PodCliqueScalingGroups defined in the PodCliqueSet.

### Monitoring

**Status Fields:**

The existing status fields provide visibility into update progress:

- PodCliqueSet's `status.updateProgress`: Progress information on the update.
- PodCliqueSet's `status.updatedReplicas`: Number of replicas running with the current template specification.
- PodCliqueSet's `status.replicas`: Total number of replicas.
- PodCliqueSet's `status.currentGenerationHash`: Hash of the current desired specification.
- PodClique's `status.currentPodTemplateHash`: Hash identifying the current template version for each PodClique.
- PodClique's `status.updateProgress`: Progress information on the update.
  - `status.updateProgress.podTemplateHash`: Hash identifying the template version for the PodClique for the ongoing update.
  - `status.updateProgress.podCliqueSetGenerationHash`: Hash of the current desired specification of the PodCliqueSet.
- PodCliqueScalingGroup's `status.updateProgress`: Progress information on the update.
  - `status.updateProgress.podCliqueSetGenerationHash`: Hash of the current desired specification of the PodCliqueSet.

When `UpdatedReplicas < Replicas` with `OnDelete` strategy, it indicates that some replicas are running with an outdated specification.

### Implementation Phases

Implementation will be split into two phases:

#### Phase 1: Standalone PodCliques, and PodCliqueScalingGroups implementation

**Scope:**
- Implement `updateStrategy.type` field at PodCliqueSet level.
- Implement `OnDelete` and `RollingRecreate` strategies for standalone PodCliques and PodCliqueScalingGroups.
- Status tracking for standalone PodCliques and PodCliqueScalingGroups.
- Preferential deletion of outdated pods during scale-in for standalone PodCliques.

**Deliverables:**
- API changes with `updateStrategy.type` field.
- API changes with the `updateProgress` field.
- Controller logic for `OnDelete` strategy for standalone PodCliques and PodCliqueScalingGroups.
- Unit tests for standalone PodClique and PodCliqueScalingGroup scenarios.

#### Phase 2: Comprehensive tests

**Scope**:
- Implement the E2E testsuite with various scenarios listed below for PodCliques and PodCliqueScalingGroups.

**Deliverables:**
- Comprehensive E2E testsuite for standalone PodCliques and PodCliqueScalingGroups.
- Complete documentation for both phases.

### Test Plan

**Unit Tests:**

- Test that `OnDelete` strategy prevents automatic deletion on template changes for both standalone PodCliques and PodCliqueScalingGroups.
- Test that new replicas use the updated template specification.
- Test that `UpdatedReplicas` accurately reflects replicas matching the current template.
- Test that `RollingRecreate` strategy maintains current behavior.
- Test validation of `updateStrategy.type` values.
- Test that during scale-in, replicas with outdated templates are preferentially selected for deletion for standalone PodCliques.

**E2E Tests:**

- Testcase 1:
  - Create a PodCliqueSet with `OnDelete` strategy, update the template, and verify no pods are deleted.
  - Delete a pod manually and verify the replacement uses the new template.
  - Scale-in a PodClique and verify pods with outdated templates are deleted first.
  - Verify status fields accurately reflect the update state.
- Testcase 2:
  - End-to-end test simulating node failure recovery workflow with standalone PodCliques.
  - Test transitioning between update strategies.
  - Test scale-in behavior with mixed template versions.
- Testcase 3:
  - Create a PodCliqueSet with PodCliqueScalingGroups and `OnDelete` strategy, update the template, and verify no replicas are deleted.
  - Delete a PodCliqueScalingGroup replica manually and verify the replacement uses the new template.
- Testcase 4:
  - End-to-end test with mixed standalone PodCliques and PodCliqueScalingGroups.
  - Verify uniform strategy application across both types.

## Alternatives

**Alternative 1: Field-Level Update Control**

Instead of a global update strategy, allow users to specify which template fields trigger rolling recreates and which do not. For example, `image` could be a specified field which does not trigger a traditional rolling recreate.

*Rejected because*: This adds significant complexity to the API and implementation. The `OnDelete` strategy provides a simpler, well-understood pattern from StatefulSet. Also, specifying fields with their path in the `PodCliqueSetSpec` that are to be ignored is fragile, since the PodCliqueSet API might change any time which forces users to reconfigure all their workloads. A dedicated `updateStrategy` section will make it far easier for users to adapt to breaking changes, if any are made in the future.

**Alternative 2: Annotation-Based Control**

Use annotations on the PodCliqueSet to control update behavior. A specific annotation can be added on the PodCliqueSet which will switch behavior from a typical `RollingRecreate` to `OnDelete`. This helps avoid changing the PodCliqueSet API that the users will have to adapt to.

*Rejected because*: Update strategy is a core behavioral setting that belongs in the spec, not in annotations.

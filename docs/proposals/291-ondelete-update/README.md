# GREP-291: `OnDelete` Update Strategy for PodCliqueSets

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Training Workload Node Failure Recovery](#story-1-training-workload-node-failure-recovery)
    - [Story 2: Non-Disruptive Affinity Updates](#story-2-non-disruptive-affinity-updates)
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
  - [Graduation Criteria](#graduation-criteria)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This GREP proposes adding an `updateStrategy` field to the `PodCliqueSet` spec, similar to the Kubernetes `StatefulSet` update strategy. The primary goal is to introduce an `OnDelete` update strategy that allows users to update PodClique and PodCliqueScalingGroup specifications (such as `nodeAffinity` rules) without automatically triggering replica deletions/restarts. This enables non-disruptive updates where only manually deleted replicas are recreated with the new specification.

The update strategy is defined at the PodCliqueSet level and applies uniformly to both standalone PodCliques and PodCliqueScalingGroups. In the future, update strategy could be defined at each hierarchy level to specify different behavior for different PodCliques/PodCliqueScalingGroups, following the convention used for [topology configuration](/docs/proposals/244-topology-aware-scheduling/README.md) in PodCliqueSet.

Implementation will be split into phases, with Phase 1 covering standalone PodCliques and Phase 2 extending support to PodCliqueScalingGroups.

## Motivation

Training workloads and long-running inference workloads may encounter machine failures during execution. The typical mitigation strategy involves:

1. **Automated/Manual Action**: Adjust workload affinity to prevent scheduling new pods on failed nodes.
2. **Pod Recovery**: Recreate only the affected pods with strict `nodeAffinity` rules against faulty nodes.
3. **Preservation**: Unaffected pods continue running to minimize disruption.

Currently, when a user updates a standalone PodClique's `podTemplate` (e.g., changing `nodeAffinity` rules), Grove triggers a rolling recreate that deletes and recreates **all** pods one at a time, including unaffected ones. This behavior is disruptive and wasteful for workloads that only need to recreate and reschedule a subset of pods. Similar behavior is displayed in PodCliqueScalingGroups where a rolling recreate deletes and recreates **all** replicas one at a time.

By introducing an `OnDelete` update strategy (inspired by Kubernetes StatefulSet), users gain fine-grained control over when replicas are updated. The new specification is applied only when a replica is manually deleted, allowing unaffected replicas to continue running undisturbed.

### Goals

- Introduce a new `spec.updateStrategy.type` field to `PodCliqueSet` that allows users to choose between `RollingRecreate` (default) and `OnDelete` update strategies.
- The update strategy applies uniformly to both standalone PodCliques and PodCliqueScalingGroups within a PodCliqueSet.
- When `OnDelete` strategy is configured:
  - Changes to the template do not automatically trigger replica deletions.
  - New replicas (created due to scale-out or manual deletion) use the updated template.
  - Replica deletions prefer replicas with the older template.
  - Existing replicas continue running with their original specification until manually deleted.
- Maintain backward compatibility by defaulting to the current `RollingRecreate` behavior.

### Non-Goals

- Implementing partition-based rolling updates (as in StatefulSet's `RollingUpdate` with `partition` field).
- Providing a `maxUnavailable` or `maxSurge` configuration for rolling updates (can be addressed in a future GREP).
- Automatic detection of failed nodes and selective pod recreation. This should be the responsibility of the scheduler.
- Defining update strategy at each hierarchy level (PodCliqueSet, PodCliqueScalingGroup, PodClique) individually (following the design used for topology configuration in a PodCliqueSet). This can be addressed in a future GREP to allow different strategies for different components.

## Proposal

Introduce a new `spec.updateStrategy` field in the `PodCliqueSet` specification. The `UpdateStrategy` field describes how replicas should be updated when templates change.

Two update strategy types will be supported:

1. **RollingRecreate** (default): A strategy where a replica is deleted first, and then recreated with the new template. This applies to both pods (for standalone PodCliques) and replicas of PodCliqueScalingGroups.
2. **OnDelete**: Replicas are not automatically deleted when the template changes. The new specification is applied only when a replica is manually deleted and recreated by the controller.

As mentioned previously, support to override the update strategy at PodClique and PodCliqueScalingGroup hierarchies can be brought about in the future if needed.

### User Stories

#### Story 1: Training Workload Node Failure Recovery

As an AI engineer running a distributed training job across multiple nodes, when one node fails, and I need to update the `nodeAffinity` to exclude the failed node, I want only the affected pods (those on the failed node) to be rescheduled, while all other pods continue their training computation without interruption.

With `OnDelete` strategy:
1. I update the PodCliqueSet with new `nodeAffinity` rules excluding the failed node.
2. The pods on healthy nodes continue running.
3. I manually delete the pods that were on the failed node (or they are automatically evicted by kubelet, or another actor in the system).
4. New pods are created with the updated `nodeAffinity` and scheduled on healthy nodes.

#### Story 2: Non-Disruptive Affinity Updates

As an operator, I want to update scheduling preferences (affinity, tolerations) for a running inference workload without causing service disruption. This could be for node maintenance, OS upgrade, Kubernetes version upgrade, etc. The `OnDelete` strategy allows me to update the specification and gradually roll out changes by selectively deleting pods during maintenance windows.

### Limitations/Risks & Mitigations

**Risk 1: Configuration Drift**

With `OnDelete` strategy, replicas within the same PodCliqueSet may run with different specifications for an extended period if replicas are not manually deleted.

*Mitigation*:
- The PodClique's `status.updatedReplicas` field will accurately reflect how many pods are running with the current (desired) specification.
- The PodCliqueScalingGroup's `status.updatedReplicas` field will accurately reflect how many replicas are running with the current (desired) specification.
- Clear documentation will explain the behavior and best practices for using `OnDelete` strategy.

**Risk 2: User Confusion**

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
    // RollingRecreateStrategyType indicates that replicas will be progressively
    // deleted and recreated one at a time, when templates change. This applies to
    // both pods (for standalone PodCliques) and replicas of PodCliqueScalingGroups.
    // This is the default update strategy.
    RollingRecreateStrategyType UpdateStrategyType = "RollingRecreate"
    
    // OnDeleteStrategyType indicates that replicas will only be updated when
    // they are manually deleted. Changes to templates do not automatically
    // trigger replica deletions.
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
    // templates change. This applies to both standalone PodCliques and
    // PodCliqueScalingGroups.
    // +optional
    UpdateStrategy *PodCliqueSetUpdateStrategy `json:"updateStrategy,omitempty"`
    
    // Template describes the template spec for PodGangs that will be 
    // created in the PodCliqueSet.
    Template PodCliqueSetTemplateSpec `json:"template"`
}
```

#### PodCliqueSetStatus Changes

The `PodCliqueSetStatus` field `PodCliqueSetRollingUpdateProgress` will be renamed to `PodCliqueSetUpdateProgress` (`status.updateProgress`). This field will be used to generically track information about updates to the PodCliqueSet, with all supported strategies:

```go
// PodCliqueSetSpec defines the specification of a PodCliqueSet.
type PodCliqueSetStatus struct {
    // PodGangStatuses captures the status for all the PodGang's that are part of the PodCliqueSet.
    PodGangStatutes []PodGangStatus `json:"podGangStatuses,omitempty"`
    // CurrentGenerationHash is a hash value generated out of a collection of fields in a PodCliqueSet.
    // Since only a subset of fields is taken into account when generating the hash, not every change in the PodCliqueSetSpec will
    // be accounted for when generating this hash value. A field in PodCliqueSetSpec is included if a change to it triggers
    // a rolling update of PodCliques and/or PodCliqueScalingGroups.
    // Only if this value is not nil and the newly computed hash value is different from the persisted CurrentGenerationHash value
    // then a rolling update needs to be triggerred.
    CurrentGenerationHash *string `json:"currentGenerationHash,omitempty"`
    // UpdateProgress represents the progress of a rolling update.
    UpdateProgress *PodCliqueSetUpdateProgress `json:"updateProgress,omitempty"`
}
```

#### PodCliqueStatus Changes

The `PodCliqueStatus` field `PodCliqueRollingUpdateProgress` will be renamed to `PodCliqueUpdateProgress` (`status.updateProgress`). This field will be used to generically track information about updates to the PodClique, with all supported strategies:

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
  	// UpdateProgress provides details about the ongoing rolling update of the PodClique.
  	UpdateProgress *PodCliqueUpdateProgress `json:"updateProgress,omitempty"`
}
```

#### PodCliqueScalingGroupStatus Changes

The `PodCliqueScalingGroupStatus` field `PodCliqueScalingGroupRollingUpdateProgress` will be renamed to `PodCliqueScalingGroupUpdateProgress` (`status.updateProgress`). This field will be used to generically track information about updates to the PodClique, with all supported strategies:

```go
// PodCliqueScalingGroupStatus defines the status of a PodCliqueScalingGroup.
type PodCliqueScalingGroupStatus struct {
...
  	// Conditions represents the latest available observations of the PodCliqueScalingGroup by its controller.
  	Conditions []metav1.Condition `json:"conditions,omitempty"`
  	// CurrentPodCliqueSetGenerationHash establishes a correlation to PodCliqueSet generation hash indicating
  	// that the spec of the PodCliqueSet at this generation is fully realized in the PodCliqueScalingGroup.
  	CurrentPodCliqueSetGenerationHash *string `json:"currentPodCliqueSetGenerationHash,omitempty"`
  	// UpdateProgress provides details about the ongoing rolling update of the PodCliqueScalingGroup.
  	UpdateProgress *PodCliqueScalingGroupUpdateProgress `json:"updateProgress,omitempty"`
...
}
```

> **Note:** In the future, update strategy could be defined at each hierarchy level to specify different behavior for different PodCliques/PodCliqueScalingGroups, following the convention used for topology configuration in PodCliqueSet. This would involve adding optional `UpdateStrategy` fields to `PodCliqueTemplateSpec` and `PodCliqueScalingGroupConfig` that override the PodCliqueSet-level strategy.

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

**For Standalone PodCliques:**
1. Changes to a PodCliqueSet's `spec.cliques[*]` are recorded but do not trigger automatic pod deletions.
2. The PodCliqueSet's `status.currentGenerationHash` is updated to reflect the new desired state.
3. The PodClique's `spec` is patched with the changes brought in the template at `spec.cliques[*].spec` of the PodCliqueSet.
4. Existing pods continue running with their original specification.
5. When a pod needs to be deleted (e.g., during scale-in), pods with the older template are preferred for deletion.
6. When a pod is deleted (manually, during scale-in, or due to node failure/eviction):
   - The controller creates a replacement pod using the current (updated) template.
   - The new pod reflects any specification changes made since the original pod was created.
7. The PodClique's `status.updateProgress.podTemplateHash` can be compared with individual pod labels to identify which pods are running outdated specifications.
8. PodClique's `status.updatedReplicas` reflects the count of pods running with the current template.

**For PodCliqueScalingGroups:**
1. Changes to a PodCliqueSet's `spec.cliques[*]` that belong to a PodCliqueScalingGroup are recorded but do not trigger automatic replica deletions.
2. The PodCliqueSet's `status.currentGenerationHash` is updated to reflect the new desired state.
3. Existing replicas continue running with their original specification.
4. When a replica is deleted (manually or during scale-in):
   - The controller creates a replacement replica using the current (updated) template.
5. PodCliqueScalingGroup's `status.updatedReplicas` reflects the count of replicas running with the current template.

**For both resources**:
1. The PodClique's `status.updateProgress.updateStartedAt` and `status.updateProgress.updateEndedAt` are both set simultaneously when the `OnDelete` update is initiated. The following are the reasons:
  - From an `OnDelete` update perspective, the update ends as soon as the specification of the resource is synced with the template specified in the PodCliqueSet.Thus, the timestamp is set as the same for both.
  - Gang termination is paused when a PodCliqueSet is being updated through the `RollingRecreate`. Since the user controls the time at which replicas are deleted, it is unknown when the update will end, and therefore is incorrect to pause gang termination until all resources are updated. Thus, both timestamps are set simultaneously, ensuring gang termination continues to be in effect.
2. The `readyPodsSelectedToUpdate` or the `readyReplicaIndicesSelectedToUpdate` are not set since the operator does not choose which replica is to be updated.

**Key Implementation Points:**

- Logic that initiates updates through hash comparison will be enhanced to support `RollingRecreate` and `OnDelete`, by skipping the traditional `RollingRecreate` codeflow when `OnDelete` is specified.
- `status.updateProgress.updateStartedAt` and `status.updateProgress.updateEndedAt` are both set simultaneously when the `OnDelete` update is initiated on the standalone PodClique resources and the PodCliqueScalingGroup resources.
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
  - `status.podCliqueSetGenerationHash`: Hash of the current desired specification of the PodCliqueSet.
- PodCliqueScalingGroup's `status.updateProgress`: Progress information on the update.
  - `status.podCliqueSetGenerationHash`: Hash of the current desired specification of the PodCliqueSet.

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

### Graduation Criteria

**Alpha:**
- `OnDelete` strategy for standalone PodCliques and PodCliqueScalingGroups is implemented behind a feature flag (if applicable).
- API is implemented as described.

**Beta:**
- All E2E tests are implemented and passing.
- Documentation is complete for both phases.
- Feature has been used in real-world scenarios and feedback incorporated.

**GA:**
- Feature has been stable for at least two releases.
- No significant bugs or usability issues reported.

## Alternatives

**Alternative 1: Field-Level Update Control**

Instead of a global update strategy, allow users to specify which template fields trigger rolling recreates and which do not. For example, `nodeAffinity` could be a specified field which does not trigger a traditional rolling recreate.

*Rejected because*: This adds significant complexity to the API and implementation. The `OnDelete` strategy provides a simpler, well-understood pattern from StatefulSet. Also, specifying fields with their path in the `PodCliqueSetSpec` that are to be ignored is fragile, since the PodCliqueSet API might change any time which forces users to reconfigure all their workloads. A dedicated `updateStrategy` section will make it far easier for users to adapt to breaking changes, if any are made in the future.

**Alternative 2: Annotation-Based Control**

Use annotations on the PodCliqueSet to control update behavior. A specific annotation can be added on the PodCliqueSet which will switch behavior from a typical `RollingRecreate` to `OnDelete`. This helps avoid changing the PodCliqueSet API that the users will have to adapt to.

*Rejected because*: Update strategy is a core behavioral setting that belongs in the spec, not in annotations.

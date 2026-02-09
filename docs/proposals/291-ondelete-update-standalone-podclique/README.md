# GREP-291: `OnDelete` Update Strategy for Standalone PodCliques

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
    - [PodCliqueSetUpdateStrategy](#podcliquesetupdatestrategy)
    - [PodCliqueSetSpec Changes](#podcliquesetspec-changes)
  - [Behavior Details](#behavior-details)
    - [RollingUpdate Strategy (Default)](#rollingupdate-strategy-default)
    - [OnDelete Strategy](#ondelete-strategy)
  - [Example Usage](#example-usage)
  - [Monitoring](#monitoring)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This GREP proposes adding an `updateStrategy` field to the `PodCliqueSet` spec, similar to the Kubernetes `StatefulSet` update strategy. The primary goal is to introduce an `OnDelete` update strategy that allows users to update standalone PodClique specifications (such as `nodeAffinity` rules) without automatically triggering pod deletions/restarts. This enables non-disruptive updates where only manually deleted pods are recreated with the new specification.

## Motivation

Training workloads and long-running inference workloads may encounter machine failures during execution. The typical mitigation strategy involves:

1. **Automated/Manual Action**: Adjust workload affinity to prevent scheduling new pods on failed nodes.
2. **Pod Recovery**: Recreate only the affected pods with strict `nodeAffinity` rules against faulty nodes.
3. **Preservation**: Unaffected pods continue running to minimize disruption.

Currently, when a user updates a standalone PodClique's `podTemplate` (e.g., changing `nodeAffinity` rules), Grove triggers a rolling update that deletes and recreates **all** pods, including unaffected ones. This behavior is disruptive and wasteful for workloads that only need to recreate and reschedule a subset of pods.

By introducing an `OnDelete` update strategy (inspired by Kubernetes StatefulSet), users gain fine-grained control over when pods are updated. The new specification is applied only when a pod is manually deleted, allowing unaffected pods to continue running undisturbed.

### Goals

- Introduce a new `spec.updateStrategy.podCliqueStrategy` field to `PodCliqueSet` that allows users to choose between `RollingUpdate` (default) and `OnDelete` update strategies for standalone PodCliques.
- When `OnDelete` strategy is configured:
  - Changes to the PodClique template do not automatically trigger pod deletions.
  - New pods (created due to scale-out or manual pod deletion) use the updated template.
  - Pod deletions prefer Pods with the older template.
  - Existing pods continue running with their original specification until manually deleted.
- Maintain backward compatibility by defaulting to the current `RollingUpdate` behavior.

### Non-Goals

- Implementing `OnDelete` for replicas of a `PodCliqueScalingGroup` (can be addressed in a future GREP, by introducing a seperate field `spec.updateStrategy.scalingGroupStrategy`).
- Implementing partition-based rolling updates (as in StatefulSet's `RollingUpdate` with `partition` field).
- Providing a `maxUnavailable` or `maxSurge` configuration for rolling updates (can be addressed in a future GREP).
- Automatic detection of failed nodes and selective pod recreation. This should be the responsibility of the scheduler.

## Proposal

Introduce a new `spec.updateStrategy` field in the `PodCliqueSet` specification. This field will accept an `UpdateStrategy` object that defines how pods should be updated when the PodClique template changes.

Two update strategy types will be supported:

1. **RollingUpdate** (default): The current behavior where pods are progressively deleted one by one and recreated with the new specification.
2. **OnDelete**: Pods are not automatically deleted when the template changes. The new specification is applied only when a pod is manually deleted by the user and recreated by the PodClique controller.

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

With `OnDelete` strategy, pods within the same PodClique may run with different specifications for an extended period if pods are not manually deleted.

*Mitigation*:
- The `PodCliqueStatus.UpdatedReplicas` field will accurately reflect how many pods are running with the current (desired) specification.
- Clear documentation will explain the behavior and best practices for using `OnDelete` strategy.

**Risk 2: User Confusion**

Users may not understand why their spec changes are not being applied automatically.

*Mitigation*:
- The API will clearly document the behavior of each update strategy.
- Status conditions will indicate when there are pods running with an outdated specification.

## Design Details

### API Changes

#### UpdateStrategyType

```go
// UpdateStrategyType defines the type of update strategy for PodCliqueSet.
// +kubebuilder:validation:Enum={RollingUpdate,OnDelete}
type UpdateStrategyType string

const (
    // RollingUpdateStrategyType indicates that pods will be progressively
    // deleted and recreated when the PodClique template changes.
    // This is the default update strategy.
    RollingUpdateStrategyType UpdateStrategyType = "RollingUpdate"
    
    // OnDeleteStrategyType indicates that pods will only be updated when
    // they are manually deleted. Changes to the PodClique template do not
    // automatically trigger pod deletions.
    OnDeleteStrategyType UpdateStrategyType = "OnDelete"
)
```

#### PodCliqueSetUpdateStrategy

```go
// PodCliqueSetUpdateStrategy defines the update strategy for a PodCliqueSet.
type PodCliqueSetUpdateStrategy struct {
    // PodCliqueStrategy defines the update strategy for standalone PodCliques
    // (PodCliques that are not part of a PodCliqueScalingGroup).
    // Default is RollingUpdate.
    // +optional
    PodCliqueStrategy *PodCliqueUpdateStrategy `json:"podCliqueStrategy,omitempty"`
    
    // ScalingGroupStrategy defines the update strategy for PodCliqueScalingGroups.
    // This field is reserved for future use and is currently not implemented.
    // Default is RollingUpdate.
    // +optional
    // ScalingGroupStrategy *ScalingGroupUpdateStrategy `json:"scalingGroupStrategy,omitempty"`
}

// PodCliqueUpdateStrategy defines the update strategy for standalone PodCliques.
type PodCliqueUpdateStrategy struct {
    // Type indicates the type of update strategy for standalone PodCliques.
    // Default is RollingUpdate.
    // +kubebuilder:default=RollingUpdate
    Type UpdateStrategyType `json:"type,omitempty"`
}
```

#### PodCliqueSetSpec Changes

The `PodCliqueSetSpec` will be extended to include the `updateStrategy` field:

```go
// PodCliqueSetSpec defines the specification of a PodCliqueSet.
type PodCliqueSetSpec struct {
    // Replicas is the number of desired replicas of the PodGang.
    // +kubebuilder:default=0
    Replicas int32 `json:"replicas,omitempty"`
    
    // UpdateStrategy defines the strategy for updating pods when the 
    // PodClique template changes.
    // +optional
    UpdateStrategy *PodCliqueSetUpdateStrategy `json:"updateStrategy,omitempty"`
    
    // Template describes the template spec for PodGangs that will be 
    // created in the PodCliqueSet.
    Template PodCliqueSetTemplateSpec `json:"template"`
}
```

### Behavior Details

#### RollingUpdate Strategy (Default)

When `updateStrategy.podCliqueStrategy.type` is set to `RollingUpdate` (or when `updateStrategy` is not specified):

1. Changes to a standalone PodClique template that affect the pod specification trigger a rolling update.
2. The controller progressively deletes and recreates pods to match the new specification.
3. The rolling update proceeds one PodCliqueSet replica at a time to minimize disruption.
4. `Status.RollingUpdateProgress` tracks the progress of the update.
5. This is the current behavior and remains unchanged.

#### OnDelete Strategy

When `updateStrategy.podCliqueStrategy.type` is set to `OnDelete`:

1. Changes to a standalone PodClique template are recorded but do not trigger automatic pod deletions.
2. The `Status.CurrentGenerationHash` is updated to reflect the new desired state.
3. Existing pods continue running with their original specification.
4. When a pod needs to be deleted (e.g., during scale-in), pods with the older template are preferred for deletion.
5. When a pod is deleted (manually, during scale-in, or due to node failure/eviction):
   - The controller creates a replacement pod using the current (updated) template.
   - The new pod reflects any specification changes made since the original pod was created.
6. `Status.UpdatedReplicas` reflects the count of pods running with the current template.
7. The `PodCliqueStatus.CurrentPodTemplateHash` can be compared with individual pod labels to identify which pods are running outdated specifications.

**Key Implementation Points:**

- The generation hash comparison logic will be modified to skip initiating rolling updates for standalone PodCliques when `OnDelete` strategy is configured. The `CurrentGenerationHash` is immediately set to the new hash, which helps avoid the trigger of a rolling update.
- Pod creation logic will always use the latest template specification, regardless of update strategy, as it already exists.
- The `UpdatedReplicas` status field will be maintained to show how many pods match the current specification.
- During scale-in operations, pods running with outdated templates will be preferentially selected for deletion.

### Example Usage

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: training-workload
spec:
  replicas: 1
  updateStrategy:
    podCliqueStrategy:
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

### Monitoring

**Status Fields:**

The existing status fields provide visibility into update progress:

- `Status.UpdatedReplicas`: Number of replicas running with the current template specification.
- `Status.Replicas`: Total number of replicas.
- `Status.CurrentGenerationHash`: Hash of the current desired specification.
- `PodCliqueStatus.CurrentPodTemplateHash`: Hash identifying the template version for each PodClique.

When `UpdatedReplicas < Replicas` with `OnDelete` strategy, it indicates that some pods are running with an outdated specification.

**Proposed Condition:**

A new condition `UpdatePending` can be added to `PodCliqueSetStatus.Conditions`:

| Status | Reason | Description |
|--------|--------|-------------|
| `True` | `PodsOutdated` | Some pods are running with an outdated specification |
| `False` | `AllPodsUpdated` | All pods are running with the current specification |

This condition will also be set when the update strategy is `RollingUpdate`, along with the typical `RollingUpdateStatus` fields.
Since `RollingUpdateStatus` is not relevant during `OnDelete`, users can consume the `UpdatePending` condition on the `PodClique` to understand if all their Pods match with the new template.

### Test Plan

**Unit Tests:**

- Test that `OnDelete` strategy prevents automatic pod deletion on standalone PodClique template changes.
- Test that new pods use the updated template specification.
- Test that `UpdatedReplicas` accurately reflects pods matching the current template.
- Test that `RollingUpdate` strategy maintains current behavior for standalone PodCliques.
- Test validation of `updateStrategy.podCliqueStrategy.type` values.
- Test that during scale-in, pods with outdated templates are preferentially selected for deletion.

**E2E Tests:**

- Testcase 1: 
  - Create a PodCliqueSet with `OnDelete` strategy for standalone PodCliques, update the template, and verify no pods are deleted.
  - Delete a pod manually and verify the replacement uses the new template.
  - Scale-in a PodCliqueSet and verify pods with outdated templates are deleted first.
  - Verify status fields accurately reflect the update state.
- Testcase 2:
  - End-to-end test simulating node failure recovery workflow with standalone PodCliques.
  - Test transitioning between update strategies.
  - Test scale-in behavior with mixed template versions.

### Graduation Criteria

**Alpha:**
- Feature is implemented behind a feature flag (if applicable).
- API is implemented as described.

**Beta:**
- E2E tests are implemented and passing.
- Documentation is complete.
- Feature has been used in real-world scenarios and feedback incorporated.

**GA:**
- Feature has been stable for at least two releases.
- No significant bugs or usability issues reported.

## Alternatives

**Alternative 1: Field-Level Update Control**

Instead of a global update strategy, allow users to specify which template fields trigger rolling updates and which do not. For example, `nodeAffinity` could be a specified field which does not trigger a traditional rolling update.

*Rejected because*: This adds significant complexity to the API and implementation. The `OnDelete` strategy provides a simpler, well-understood pattern from StatefulSet. Also, specifying fields with their path in the `PodCliqueSetSpec` that are to be ignored is fragile, since the PodCliqueSet API might change any time which forces users to reconfigure all their workloads. A dedicated `rollingUpdate` section will make it far easier for users to adapt to breaking changes, if any are made in the future.

**Alternative 2: Annotation-Based Control**

Use annotations on the PodCliqueSet to control update behavior. A specific annotation can be added on the PodCliqueSet which will switch behavior from a typical `RollingUpdate` to `OnDelete`. This helps avoid changing the PodCliqueSet API that the users will have to adapt to.

*Rejected because*: Update strategy is a core behavioral setting that belongs in the spec, not in annotations.

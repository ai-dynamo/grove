# GREP-0285: Job Support

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Running a finite job in Grove](#story-1-running-a-finite-job-in-grove)
    - [Story 2: Synchronous distributed training (all-ranks completion)](#story-2-synchronous-distributed-training-all-ranks-completion)
    - [Story 3: Leader-driven completion](#story-3-leader-driven-completion)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [Completion Policy](#completion-policy)
  - [New API Fields](#new-api-fields)
  - [Examples](#examples)
  - [Completion and Failure Evaluation](#completion-and-failure-evaluation)
  - [Gang Restart Flow](#gang-restart-flow)
  - [Condition and Status Model](#condition-and-status-model)
  - [Cleanup Behavior](#cleanup-behavior)
  - [Monitoring](#monitoring)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Alternatives](#alternatives)
  - [<code>restartPolicy: Never</code> as the completion signal](#restartpolicy-never-as-the-completion-signal)
  - [Separate completion mode field](#separate-completion-mode-field)
  - [Status phase for job observability](#status-phase-for-job-observability)
<!-- /toc -->

## Summary

Grove currently supports long-running workloads such as inference services, where pods are expected to run indefinitely. This GREP introduces job support through completion-aware resources: workloads that exit normally when their task is done, with Grove tracking completion and failure bottom-up through the `PodClique` -> `PodCliqueScalingGroup` -> `PodCliqueSet` hierarchy. A new `spec.policy.completion` API gives users precise control over named-child completion and failure tolerance. Gang-restart semantics ensure that tightly coupled distributed workloads, such as multi-node training jobs, are retried as a unit when a failure occurs.

## Motivation

Grove is purpose-built for AI infrastructure workloads. While its current model — gang scheduling, `minAvailable`-based availability, automatic restart on pod loss — serves long-running inference services well, it does not fit distributed training or batch workloads that run to completion. These workloads have fundamentally different lifecycle semantics: a pod exiting with code 0 is success, not a failure to be restarted; a single worker failure in a tightly coupled AllReduce job stalls the entire gang and warrants a full restart, not a local pod replacement; and the job as a whole has a well-defined done state.

Kubernetes' own `restartPolicy: Never` provides the right pod-level primitive — pods reach a terminal state (`Succeeded` or `Failed`) on exit — but Grove currently has no way to act on those terminal states. Without job support, users running training workloads on Grove must layer their own completion tracking and restart logic on top, or use a separate job controller that is unaware of Grove's gang scheduling and topology placement.

This GREP closes that gap by extending Grove's existing hierarchy with completion-aware lifecycle semantics, while reusing the existing gang scheduling, topology, and restart machinery.

### Goals

- Enable finite, completion-oriented workloads in Grove alongside existing long-running workloads, with Grove owning all completion and restart decisions.
- Define bottom-up completion and failure semantics across the `PodClique` → `PodCliqueScalingGroup` → `PodCliqueSet` hierarchy.
- Support diverse job framework patterns — from all-ranks completion (every worker must succeed) to leader-driven completion (a single designated pod's exit determines the outcome).
- Introduce `spec.policy.completion` on `PodClique` to signal completion-aware behavior, and on `PodCliqueScalingGroup` / `PodCliqueSet` to configure parent completion criteria and restart budgets.
- Define `policy.completion.failure.maxRestarts` on `PodClique`, `PodCliqueScalingGroup`, and `PodCliqueSet`; `PodClique` supports only `0` in this release, while `PodCliqueScalingGroup` and `PodCliqueSet` use it as a per-replica gang restart budget.
- Define `policy.completion.success.targetNames` on `PodCliqueScalingGroup` and `PodCliqueSet` to support named-child completion criteria.
- Support gang restart for completion-aware parent scopes: when a required `PodClique` or `PodCliqueScalingGroup` fails, the parent deletes and recreates the affected scope as a unit, consuming from a per-replica restart budget.
- Persist terminal states to status before Grove-initiated pod cleanup, and retain terminal pods (`Succeeded`, `Failed`) on a best-effort basis when the owning resource or replica will not be retried.

### Non-Goals

- **No pod-level retry.** A failed pod within a `PodClique` is not replaced in isolation. `PodClique` `policy.completion.failure.maxRestarts` may only be `0` in this release; values greater than `0` are future work. This is acceptable for the first release because current distributed training frameworks generally do not tolerate replacing a single failed worker independently. A single worker failure usually requires restarting the whole group, so gang restart covers the common recovery path.
- **No scaling for completion-aware workloads.** Completion-aware workloads use fixed replica counts. Grove rejects autoscaling configuration and manual replica changes for resources with `policy.completion` and for parent scopes that contain completion-aware direct children. As a consequence, this release also does not support elastic training workloads that dynamically change membership or continue with a partial worker set, such as TorchElastic, Elastic Horovod, or Ray Train.
- **No rolling updates for completion-aware workloads.** Changes that would update running job pods or change the generated child-resource hash are not supported. The validation webhook rejects these edits for resources with `policy.completion` and for parent scopes that contain completion-aware direct children.
- `completions` and index-based completion — configurable completion counts and index-based filtering at the `PodCliqueScalingGroup` and `PodCliqueSet` levels. In this release, all replicas must complete successfully for a resource to be considered Completed.
- Runtime deadline support (`maxRuntime`). Completion is decided by pod exit codes, not by a clock, so `maxRuntime` can be added later without changing completion tracking. Deadline semantics still need a separate design for resource-level versus replica-level or attempt-level limits, and whether deadlines reset on gang restart. Because this interaction is limited and self-contained, `maxRuntime` is deferred to keep the first release focused.
- Pod cleanup policies other than the fixed default (delete active pods on terminal state; best-effort retention of terminal pods only when the owning resource or replica will not be retried).
- TTL-based automatic workload deletion after completion.

## Proposal

Job support extends Grove's existing workload hierarchy with completion-aware behavior. The root signal is at the `PodClique` level: a `PodClique` becomes completion-aware by setting at least one supported field under `spec.policy.completion`. Parent `PodCliqueScalingGroup` and `PodCliqueSet` resources become completion-aware bottom-up when they contain completion-aware children. A parent may set its own `spec.policy.completion` to configure parent-level completion and failure behavior, but that policy does not make child `PodClique`s completion-aware.

Completion and failure are evaluated bottom-up. At each level, the resource is **Completed** when all required pods or completion-aware direct children have succeeded, and **Failed** when a required failure or exhausted gang-attempt budget makes the current scope terminate unsuccessfully. If a named-child completion criterion is already satisfied in the same reconciliation snapshot, completion takes precedence over failures from completion-aware children outside that criterion. Once a resource reaches **Failed**, it cannot subsequently become **Completed**.

When a completion-aware `PodClique` fails, its completion-aware parent (`PodCliqueScalingGroup` or `PodCliqueSet`) deletes and recreates the affected gang as a unit. This consumes one restart from the parent's per-replica budget. A `PodCliqueScalingGroup` or `PodCliqueSet` fails when enough of its replicas have exhausted their budget such that the completion target is unreachable.

Two nested policy fields control completion-aware behavior:

- **`policy.completion.success.targetNames`** *(PodCliqueScalingGroup / PodCliqueSet only)*: Named direct children within a replica that must complete for that replica to count as Completed. If omitted, all completion-aware direct children must complete. At the `PodClique` level, all pods must complete for the `PodClique` to be considered Completed.
- **`policy.completion.failure.maxRestarts`** *(PodClique / PodCliqueScalingGroup / PodCliqueSet)*: Restart budget. On `PodCliqueScalingGroup` and `PodCliqueSet`, this is a per-replica gang restart budget. On `PodClique`, only `0` is supported in this release, meaning any pod failure makes the `PodClique` Failed.

Regular-mode `PodClique`s within a `PodCliqueScalingGroup` or `PodCliqueSet` are excluded from completion evaluation. A resource can be Completed even if some of its children remain running in regular mode.

**Gang termination for completion-aware resources.** Gang scheduling is unchanged: `minAvailable` continues to gate pod launch until the full gang can be placed simultaneously, on initial start and after each restart. `minAvailable` also continues to be passed to scheduler backends as the gang's minimum member count. Completion-aware failures do not use `MinAvailableBreached`; the `Failed` condition is the termination signal, and the parent reacts immediately without waiting for `terminationDelay`. Regular-mode direct children keep `MinAvailableBreached`. If a regular-mode child breaches `minAvailable` in a mixed parent, Grove handles it through availability-based recovery, and that action does not increment `replicaRestartCounts` or consume `policy.completion.failure.maxRestarts`. A failed pod in a completion-aware `PodClique` causes the owning `PodClique` to set the `Failed` condition. The completion-aware parent then evaluates the affected replica: if restart budget remains, Grove deletes and recreates the replica as a gang; if the budget is exhausted, Grove marks the replica failed and re-evaluates the parent terminal conditions.

### User Stories

#### Story 1: Running a finite job in Grove

As a platform engineer, I want to run finite, completion-oriented workloads in Grove alongside existing long-running inference services, so that I can use a single orchestration system for both training jobs and inference deployments, with Grove handling gang scheduling, topology placement, and restart budgets consistently across both workload types.

#### Story 2: Synchronous distributed training (all-ranks completion)

As a machine learning engineer running a multi-node AllReduce training job, I want all worker pods to complete successfully before the job is considered done, so that a single worker failure triggers a full gang restart rather than leaving the remaining workers stalled at a collective barrier indefinitely.

#### Story 3: Leader-driven completion

As a machine learning engineer running a leader-worker training job, I want the job to be considered complete when the leader pod exits successfully — regardless of whether worker pods have exited — so that I can use the leader's exit code as the authoritative signal of job success, while workers remain running until the leader finishes.

### Limitations/Risks & Mitigations

**Application-level hangs without pod failure.**
Completion-aware `PodClique`s do not use `MinAvailableBreached` for availability-based recovery; ordinary pod failures and Kubernetes disruption/deletion paths are handled through the `Failed` condition when the pod fails or can no longer be accounted as `Succeeded`. This includes `pod phase=Failed` and expected pods that are deleted or disappear before success due to eviction, preemption, force deletion, or node-loss cleanup. Regular-mode children still use `MinAvailableBreached`, so below-minAvailable cases are still handled in mixed workloads. The remaining risk is narrower: if the cluster does not surface pod failure or deletion and the application keeps running despite a lost peer or broken collective, Grove cannot infer the application-level deadlock from availability alone. Workloads should use framework-level failure detection, such as rendezvous timeouts, and exit non-zero when peer loss makes progress impossible.

**API overhead and topology placement loss at scale.**
Completion-aware `PodClique`s use `restartPolicy: Never`, which disables kubelet's in-place container restart. Grove is solely responsible for recreating pods on failure. At scale, this means every gang restart triggers a full pod deletion and recreation cycle — incurring Kubernetes API overhead and requiring the scheduler to re-place all pods from scratch. Re-scheduling at scale can take meaningful time and may not recover the same topology placement that the previous attempt had. This is a known limitation of the design.

**Log loss on retry and external cleanup.**
When a gang scope is deleted and recreated during a gang restart, terminal pods from the previous attempt are cascade-deleted with it. Logs from failed attempts are not durably retained across retries. Terminal pod retention when the owning resource or replica will not be retried is also best-effort: external eviction, force deletion, PodGC, or TTL cleanup may remove pod objects before Grove observes them, so they may not be available for debugging. Mitigation: users who need per-attempt logs should rely on a cluster-level logging stack (e.g. Fluentd, Loki) rather than `kubectl logs`.

## Design Details

### Completion Policy

Completion-aware behavior is rooted at the `PodClique` level. A `PodClique` becomes completion-aware by setting at least one concrete supported field under `spec.policy.completion`. Omitting `policy.completion` keeps the `PodClique` in regular mode.

`PodCliqueScalingGroup` and `PodCliqueSet` become completion-aware when they contain at least one completion-aware direct child. Their own `spec.policy.completion` is optional and configures how the parent evaluates those children. Setting `policy.completion` on a parent with no completion-aware direct children is invalid. When `targetNames` is set, each listed name must refer to a completion-aware direct child.

An empty `policy.completion` object, including one that only contains empty `success` or `failure` objects, is invalid on every resource.

For a completion-aware `PodClique`, the pod template `restartPolicy` may be omitted or set to `Never`. If omitted, the Grove mutating webhook sets it to `Never`. Explicit `Always` and `OnFailure` are validation errors for completion-aware `PodClique`s. For regular `PodClique`s, omitted or `Always` `restartPolicy` is valid; explicit `Never` is rejected unless `policy.completion` is set, and `OnFailure` remains unsupported.

Scaling and rolling updates are rejected for completion-aware workloads after creation. The initial spec defines the fixed job shape, but later manual changes to `replicas` or fields that would update running pods or change generated child-resource hashes are not supported for resources with `policy.completion` or for parent scopes that contain completion-aware direct children. Grove-managed autoscaling is also rejected: a completion-aware `PodClique` cannot set `autoScalingConfig`, and a `PodCliqueScalingGroup` containing completion-aware children cannot set `scaleConfig`.

### New API Fields

The new fields are nested under `spec.policy.completion`. The API keeps the `PodClique` run policy separate from the parent run policy so each level exposes only fields that are meaningful for that level.

**PodCliqueSpec** gains:

```go
// Policy describes optional run behavior controlled by Grove.
// +optional
Policy *PodCliqueRunPolicy `json:"policy,omitempty"`

type PodCliqueRunPolicy struct {
    // Completion configures completion-aware behavior for this PodClique. Setting
    // at least one supported field enables completion-aware behavior.
    // +optional
    Completion *PodCliqueCompletionPolicy `json:"completion,omitempty"`
}

type PodCliqueCompletionPolicy struct {
    // Failure defines PodClique failure behavior.
    // +optional
    Failure *PodCliqueCompletionFailurePolicy `json:"failure,omitempty"`
}

type PodCliqueCompletionFailurePolicy struct {
    // MaxRestarts is the maximum number of pod-level restart attempts allowed
    // after failure. Only 0 is supported in this release.
    // +optional
    MaxRestarts *int32 `json:"maxRestarts,omitempty"`
}
```

**PodCliqueScalingGroupSpec** and **PodCliqueSetSpec** gain:

```go
// Policy describes optional run behavior controlled by Grove.
// +optional
Policy *RunPolicy `json:"policy,omitempty"`

type RunPolicy struct {
    // Completion configures evaluation of completion-aware direct children.
    // +optional
    Completion *CompletionPolicy `json:"completion,omitempty"`
}

type CompletionPolicy struct {
    // Success defines the success criterion for this resource.
    // +optional
    Success *CompletionSuccessPolicy `json:"success,omitempty"`

    // Failure defines restart and failure behavior for this resource.
    // +optional
    Failure *CompletionFailurePolicy `json:"failure,omitempty"`
}

type CompletionSuccessPolicy struct {
    // TargetNames lists direct child names that must reach Completed for a
    // PodCliqueScalingGroup or PodCliqueSet replica to be considered Completed.
    // If omitted, all completion-aware direct children must complete.
    // +optional
    TargetNames []string `json:"targetNames,omitempty"`
}

type CompletionFailurePolicy struct {
    // MaxRestarts is the maximum number of restart attempts allowed after failure.
    // It is a per-replica gang restart budget.
    // +optional
    MaxRestarts *int32 `json:"maxRestarts,omitempty"`
}
```

`policy.completion.success.targetNames` exists only on `PodCliqueScalingGroup` and `PodCliqueSet`. When set, it must be non-empty. `PodClique` always uses all-pods success: all pods must reach `pod phase=Succeeded`.

`policy.completion.failure.maxRestarts` exists on all three resources. On `PodCliqueScalingGroup` and `PodCliqueSet`, values must be non-negative; when omitted by a completion-aware parent, Grove treats the budget as `0`. On `PodClique`, the only supported explicit value is `0`; values greater than `0` are future work because pod-level retry is not implemented.

When a parent resource is completion-aware and a policy value is omitted, Grove applies semantic defaults during evaluation: all completion-aware direct children are required for parent success, and `maxRestarts` is treated as `0`. `PodClique` success always requires all pods to reach `pod phase=Succeeded`. Defaults do not make an empty `policy.completion` object valid.

### Examples

**All-ranks completion** — all 8 workers must succeed; the gang retries up to 3 times:

```yaml
# PodCliqueSet spec (relevant fields only)
spec:
  replicas: 1
  policy:
    completion:
      failure:
        maxRestarts: 3
  template:
    cliques:
    - name: worker
      spec:
        replicas: 8
        minAvailable: 8
        policy:
          completion:
            failure:
              maxRestarts: 0
        podSpec:
          restartPolicy: Never
```

**Leader-driven completion** — the PCSG replica is complete when the leader exits 0, regardless of workers:

```yaml
# PodCliqueSet spec (relevant fields only)
spec:
  replicas: 1
  policy:
    completion:
      success:
        targetNames: [trainer]
  template:
    podCliqueScalingGroups:
    - name: trainer
      policy:
        completion:
          success:
            targetNames: [leader]
          failure:
            maxRestarts: 3
      cliqueNames: [leader, worker]
    cliques:
    - name: leader
      spec:
        replicas: 1
        minAvailable: 1
        policy:
          completion:
            failure:
              maxRestarts: 0
        podSpec:
          restartPolicy: Never
    - name: worker
      spec:
        replicas: 7
        minAvailable: 7
        policy:
          completion:
            failure:
              maxRestarts: 0
        podSpec:
          restartPolicy: Never
```

### Completion and Failure Evaluation

Completion and failure are evaluated independently at each level, using only the observed state of direct children.

**PodClique**

A completion-aware `PodClique` is **Completed** when all of its pods have exited with code 0 (`pod phase=Succeeded`). It is **Failed** when any expected pod is observed as failed (`pod phase=Failed`) or is deleted/disrupted before reaching `Succeeded`, since pod-level retry is not supported and a single failure makes the all-pods completion criterion unreachable. This includes pods observed as failed, terminating, disrupted, or missing before `Succeeded` due to API eviction, scheduler preemption, force deletion, taint-based eviction, or node-loss cleanup; pods deleted because an owning parent scope is already terminal do not cause the `PodClique` to set `Failed`.

Regular-mode `PodClique`s never set `Completed` or `Failed` and keep `MinAvailableBreached`.

**PodCliqueScalingGroup**

For a completion-aware `PodCliqueScalingGroup`, evaluation is two-level:

1. *Replica state*: a replica is **Completed** when all required completion-aware child `PodClique`s are `Completed` — either all completion-aware children, or the named children in `policy.completion.success.targetNames` when it is set. Evaluation checks this success criterion first in each reconciliation snapshot; if it is satisfied, failures from completion-aware children outside `targetNames` do not trigger a restart or consume budget. If the success criterion is not yet satisfied, a failure of a completion-aware `PodClique` not listed in `targetNames` triggers a gang restart and consumes budget while budget remains, and fails the replica when no restart budget remains. A required child failure also fails the replica when no restart budget remains (`maxRestarts` exhausted, or `maxRestarts: 0`). Regular-mode child `PodClique`s are not part of PCSG completion evaluation. If one reports `MinAvailableBreached=True`, Grove handles it through availability-based recovery; this does not increment the PCSG `replicaRestartCounts` entry or consume the PCSG `maxRestarts` budget.

2. *PCSG state*: the PCSG is **Completed** when all replicas are `Completed`. It is **Failed** when enough replicas have exhausted their budget such that the remaining replicas cannot satisfy the completion criterion.

**PodCliqueSet**

For a completion-aware `PodCliqueSet`, evaluation follows the same two-level pattern as PCSG:

1. *Replica state*: a replica is **Completed** when all required completion-aware direct children are `Completed` — either all completion-aware `PodClique`s and `PodCliqueScalingGroup`s, or the named children in `policy.completion.success.targetNames` when it is set. Evaluation checks this success criterion first in each reconciliation snapshot; if it is satisfied, failures from completion-aware children outside `targetNames` do not trigger a restart or consume budget. If the success criterion is not yet satisfied, a failure of a completion-aware direct child not listed in `targetNames` follows the same gang-restart behavior as PCSG: it consumes budget while budget remains, and fails the replica when no budget remains. A required direct child failure also fails the replica when no restart budget remains. Regular-mode direct children (`PodClique`s or `PodCliqueScalingGroup`s) are not part of PCS completion evaluation. If one reports `MinAvailableBreached=True`, Grove handles it through availability-based recovery; this does not increment the PCS `replicaRestartCounts` entry or consume the PCS `maxRestarts` budget.

2. *PCS state*: the PCS is **Completed** when all replicas are `Completed`. It is **Failed** when enough replicas have exhausted their budget such that the remaining replicas cannot satisfy the completion criterion.

**General invariants**

- `Failed` is irreversible: once a resource reaches `Failed`, it will not subsequently transition to `Completed`.
- Terminal states (`Completed`, `Failed`) are written to status before any Grove-initiated pod cleanup begins.
- Completion and failure are evaluated bottom-up, but cleanup after a parent reaches a terminal state is applied top-down to all non-terminal children it owns.
- A terminal parent scope is a stop condition for descendants: child controllers must not recreate pods or child resources when their owning `PodCliqueScalingGroup` replica, `PodCliqueSet` replica, or `PodCliqueSet` resource is already terminal.

### Gang Restart Flow

When a completion-aware `PodClique` reaches `Failed`, its completion-aware parent evaluates whether to restart or mark the replica as terminally failed.

Availability-based recovery caused by `MinAvailableBreached` on a regular-mode child is not part of this flow and does not affect restart counters or completion restart budgets.

**PCLQ failure handled by completion-aware PCSG:**

1. The PCSG increments `replicaRestartCounts[replicaIndex]`.
2. If the budget is not exhausted: the PCSG deletes all `PodClique`s belonging to that PCSG replica and recreates them from the template. The recreated replica is scheduled as one gang, preserving the same gang boundary and topology-placement behavior as the initial launch.
3. If the budget is exhausted: the PCSG marks that replica as failed and re-evaluates its own terminal conditions.

**PCLQ or PCSG failure handled by completion-aware PCS:**

When a constituent completion-aware `PodClique` or `PodCliqueScalingGroup` within a PCS replica fails, the PCS treats it as a gang-level failure for the whole replica:

1. The PCS increments `replicaRestartCounts[replicaIndex]`.
2. If the budget is not exhausted: the PCS deletes all constituents of that replica (all `PodClique`s and `PodCliqueScalingGroup`s) and recreates them together from the template.
3. If the budget is exhausted: the PCS marks that replica as failed and re-evaluates its own terminal conditions.

**Ordering guarantee.** In all cases, terminal conditions and updated `replicaRestartCounts` are persisted to status before any Grove-initiated deletion begins. If the controller restarts mid-cleanup, it can resume from the persisted state without double-counting restarts or re-creating resources that were already deleted. Cleanup of active (non-terminal) pods when a resource reaches a terminal state is described in [Cleanup Behavior](#cleanup-behavior).

**Gang scheduling on restart.** Recreated pods are placed by the scheduler as a complete gang, consistent with the initial launch behavior.

### Condition and Status Model

**New conditions.** Two terminal conditions are added across all three resource types:

- `Completed` — all required pods or completion-aware children have succeeded.
- `Failed` — the scope terminated unsuccessfully due to a required failure or exhausted restart budget. Once set, this condition is irreversible.

These conditions are represented in the standard `conditions` list on each resource's status. Regular-mode resources never set these conditions. Grove does not introduce `Pending` or `Running` states for this feature.

Terminal conditions are not propagated top-down to children that did not independently complete or fail. For example, if a `PodCliqueScalingGroup` replica completes because the `PodClique`s listed in `targetNames` completed successfully, any other child `PodClique`s may not have a `Completed` or `Failed` condition. Instead, the parent terminal state makes them no longer desired, and top-down cleanup removes their active pods and non-terminal child resources.

Controllers must treat a terminal owner scope as authoritative when deciding whether to create or recreate descendants. A child controller must not recreate pods or child resources when its owning `PodCliqueScalingGroup` replica, `PodCliqueSet` replica, or `PodCliqueSet` resource is already terminal.

```go
const (
    ConditionTypeCompleted = "Completed"
    ConditionTypeFailed    = "Failed"

    ConditionReasonCompletionCriteriaMet  = "CompletionCriteriaMet"
    ConditionReasonRestartBudgetExhausted = "RestartBudgetExhausted"
)

// Conditions represent the latest available observations of the completion-aware resource.
// +optional
Conditions []metav1.Condition `json:"conditions,omitempty"`
```

Condition messages should carry resource-specific detail such as pod name, child name, replica index, or exhausted restart count. This GREP does not prescribe exact message text.

**Replica restart counts.** `PodCliqueScalingGroup` and `PodCliqueSet` each gain a `replicaRestartCounts` field to track per-replica restart history:

```go
// ReplicaRestartCounts tracks the number of times each replica has been restarted,
// indexed by replica index. This field is the authoritative restart history — it is
// not recomputed from child resources, which may have been deleted. A PCSG replica
// has no corresponding CRD object, so this is the only place restart state persists.
ReplicaRestartCounts []int32 `json:"replicaRestartCounts,omitempty"`
```

This field accumulates across restarts and is never decremented.

### Cleanup Behavior

Pod cleanup uses a single fixed policy for completion-aware resources. Completion and failure are calculated bottom-up, but cleanup is applied top-down: when a resource or replica reaches a terminal state, Grove deletes still-active pods (`Pending`, `Running`) it owns. Already-finished pods (`Succeeded`, `Failed`) are retained for log access on a best-effort basis only when that resource or replica will not be retried; failed attempts that are retried are deleted and recreated as part of gang restart.

This cleanup rule is important for partial-completion policies. For example, a `PodCliqueScalingGroup` replica may be considered `Completed` because the `PodClique`s listed in `targetNames` completed successfully, while other child `PodClique`s are still running. Once the replica is terminal, the PCSG controller deletes the non-terminal child `PodClique`s or active pods in that replica so they stop consuming resources. Similarly, once a `PodCliqueSet` replica or the whole PCS reaches a terminal state, the PCS controller cleans up active child `PodClique`s and `PodCliqueScalingGroup`s in that completed or failed scope.

**On `PodClique` terminal state (`Completed` or `Failed`):**
- Delete all active pods (`Pending`, `Running`) in the `PodClique`.
- Retain terminal pods (`Succeeded`, `Failed`) for log access via `kubectl logs` on a best-effort basis, unless the `PodClique` is deleted as part of a gang restart or the pod was already removed by external cleanup.

**On `PodCliqueScalingGroup` replica terminal state:**
- Delete active pods and non-terminal child `PodClique`s belonging to that replica.
- This covers `targetNames`, where only selected children are required for completion and the remaining children may still be running.
- Pod deletion uses normal Kubernetes termination semantics; users should set `podSpec.terminationGracePeriodSeconds` when workers need time to finish checkpoint flushes, final collectives, or other shutdown work.

**On `PodCliqueSet` replica or resource terminal state:**
- Delete active pods and non-terminal child `PodClique`s / `PodCliqueScalingGroup`s belonging to that scope.
- This prevents completed or failed PCS scopes from continuing to consume resources after the parent decision has already been made.

**Recreation guard:**
- Controllers must not recreate pods, `PodClique`s, or `PodCliqueScalingGroup`s whose owning PCSG/PCS scope is already terminal.

**During a gang restart:**
- The previous gang scope is deleted entirely (not retained), which cascade-deletes its terminal pods as well. Logs from the failed attempt are not preserved across restarts. See [Log loss on retry](#limitationsrisks--mitigations).

**Terminal pod retention:**
- Grove does not delete terminal pods when the owning resource or replica will not be retried. When the pod object remains in the API, it stays available until the workload is deleted by the user or an external TTL policy removes it. This is a best-effort debugging aid, not a guarantee against external eviction, force deletion, PodGC, or other cleanup.

**Ordering:**
- Terminal conditions are always written to status before any Grove-initiated pod deletion begins.

### Monitoring

Job support surfaces observability through status conditions and Kubernetes events for key lifecycle transitions.

**Status conditions.** `Completed` and `Failed` conditions are the primary status signals for completion-aware resources:

| Condition | Status | Meaning |
|---|---|---|
| `Completed` | `True` | All required pods or completion-aware children succeeded. |
| `Failed` | `True` | The scope terminated unsuccessfully due to a required failure or exhausted restart budget. |

Regular-mode resources never set these conditions.

**Kubernetes events.** Events carry the detail behind condition transitions and operational actions. The `involvedObject` field identifies the resource the event is attached to; the `message` field carries per-event detail such as replica index and child name. The `involvedObject.kind` distinguishes PCSG-level from PCS-level events sharing the same reason string.

| Attached to | Type | Reason | Message carries |
|---|---|---|---|
| PCLQ / PCSG / PCS | `Normal` | `JobCompleted` | — |
| PCLQ / PCSG / PCS | `Warning` | `JobFailed` | Failure reason (e.g. pod failure, restart budget exhausted) |
| PCSG / PCS | `Warning` | `GangRestartTriggered` | Replica index, failed child name |
| PCSG / PCS | `Warning` | `RestartBudgetExhausted` | Replica index |

The `PodCliqueScalingGroupReplicaDeleteSuccessful` / `PodCliqueSetReplicaDeleteSuccessful` events continue to fire on gang restart. `GangRestartTriggered` is an additional completion-aware signal that explicitly names the cause.

### Test Plan

**Unit tests**

- Validation: empty `policy.completion` and empty nested `success` / `failure` objects are rejected; `PodCliqueRunPolicy` exposes only `completion.failure.maxRestarts`; `PodClique` `maxRestarts` values greater than `0` are rejected; parent `targetNames` is non-empty when set.
- Validation: completion-aware `PodClique` accepts omitted `restartPolicy` or `restartPolicy: Never`; omitted `restartPolicy` is defaulted to `Never`; explicit `Always` and `OnFailure` are rejected for completion-aware `PodClique`s; explicit `Never` is rejected for regular `PodClique`s.
- Validation: `policy.completion` on a parent with no completion-aware direct children is rejected, and `targetNames` entries refer only to completion-aware direct children.
- Validation: autoscaling configuration, manual replica changes, and edits that would trigger rolling updates on resources with `policy.completion` or parent scopes containing completion-aware direct children are rejected.
- Completion evaluation logic at each level: all-pods success → `Completed`; pod failure or disruption/absence before success → PCLQ `Failed`; named-child completion → PCSG/PCS replica `Completed`; named-child completion takes precedence over failures outside `targetNames` in the same snapshot; failures outside `targetNames` before completion trigger restart and can fail the replica when budget is exhausted.
- Failure evaluation: budget exhaustion → replica `Failed`; `Failed` is irreversible.
- `replicaRestartCounts` increments correctly on each completion-aware gang restart and is never decremented; availability-based recovery from regular-mode `MinAvailableBreached` does not increment it or consume `maxRestarts`.
- Terminal conditions are written before Grove-initiated pod deletion (ordering guarantee).

**E2e tests** (new file: `e2e/tests/job_support_test.go`)

- **All-ranks completion**: all workers complete successfully → PCS reaches `Completed`.
- **Pod failure → gang restart**: one pod fails → PCLQ fails → parent restarts the gang → restart budget decremented.
- **Budget exhaustion**: replica exhausts `maxRestarts` → PCSG/PCS fails.
- **Leader-driven completion**: leader exits 0, workers still running → PCSG replica `Completed`, active worker pods cleaned up.
- **Mixed completion-aware/regular**: completion-aware PCLQ completes alongside regular PCLQ → parent reaches `Completed`; regular-mode `MinAvailableBreached` remains handled by availability-based recovery and does not consume `maxRestarts`.
- **Gang scheduling on restart**: after a gang restart, verify that a new PCLQ / PCSG / PCS replica is created and the existing gang scheduling machinery places it as a complete gang.
- **Conditions and events**: verify `Completed=True` / `Failed=True` conditions and `GangRestartTriggered` / `JobCompleted` / `JobFailed` events are emitted at the right moments.

### Graduation Criteria

**Alpha**

- Full implementation of job support as described in this GREP, including all API fields, controller logic, condition and event emission, and cleanup behavior.
- Unit and e2e tests passing.

**Beta**

- Validated in at least one production workload.
- No breaking API changes since alpha.
- User-facing documentation available.

**GA**

- Stable API.
- No open critical issues related to the feature.

## Alternatives

### `restartPolicy: Never` as the completion signal

Using `restartPolicy: Never` as the signal for completion-aware behavior was considered. It would reuse an existing Kubernetes pod field and make the initial API smaller.

This approach couples workload lifecycle semantics to kubelet container restart behavior. A future release may want to use `restartPolicy: OnFailure` so kubelet can restart containers in-place while Grove still tracks and limits retries at the Grove resource level. Treating `restartPolicy` as the mode signal would make that combination difficult to express, so this GREP keeps the signal in Grove's own `spec.policy.completion` API and treats `restartPolicy` as pod runtime behavior.

### Separate completion mode field

A concrete mode field such as `runPolicy`, `completionPolicy`, or `completionMode` was considered. It would make the mode signal explicit, but it would also add a field whose only job is to classify the resource separately from the policy knobs that define its behavior.

This GREP uses concrete fields under `spec.policy.completion` as the opt-in signal instead. An empty `policy.completion` object is invalid, so the API does not rely on a bare section existing without behavior. At the same time, the nested policy shape keeps completion and failure controls together and preserves room for future extensions under the same API surface.

### Status phase for job observability

A dedicated `phase` field was considered for job completion and failure. It would provide a compact mutually-exclusive status value, but it also raises follow-up lifecycle questions: if `Completed` and `Failed` are phases, users may reasonably expect non-terminal phases such as `Pending` and `Running`, and those states would also need semantics for long-running Grove workloads.

This GREP uses conditions instead. `Completed` and `Failed` are only set when applicable, follow common Kubernetes status conventions, and avoid introducing a general lifecycle state machine for resources that are otherwise expected to run indefinitely.

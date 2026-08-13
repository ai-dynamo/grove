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
- Introduce `spec.policy.completion` at the `PodClique`, `PodCliqueScalingGroup`, and `PodCliqueSet` levels as the explicit API signal for completion-aware behavior, completion criteria, and restart budgets.
- Define `policy.completion.failure.maxRestarts` on `PodClique`, `PodCliqueScalingGroup`, and `PodCliqueSet`; `PodClique` supports only `0` in this release, while `PodCliqueScalingGroup` and `PodCliqueSet` use it as a per-replica gang restart budget.
- Define `policy.completion.success.completedNames` on `PodCliqueScalingGroup` and `PodCliqueSet` to support named-child completion criteria.
- Support gang restart for completion-aware parent scopes: when a required `PodClique` or `PodCliqueScalingGroup` fails, the parent deletes and recreates the affected scope as a unit, consuming from a per-replica restart budget.
- Guarantee that terminal states are persisted to status before any pod cleanup, and that terminal pods (`Succeeded`, `Failed`) are retained for log access until the workload is deleted.

### Non-Goals

- **No pod-level retry.** A failed pod within a `PodClique` is not replaced in isolation. `PodClique` `policy.completion.failure.maxRestarts` may only be `0` in this release; values greater than `0` are future work. This is acceptable for the first release because current distributed training frameworks generally do not tolerate replacing a single failed worker independently. A single worker failure usually requires restarting the whole group, so gang restart covers the common recovery path.
- **No scaling for completion-aware workloads.** Completion-aware workloads use fixed replica counts. Grove rejects autoscaling configuration and manual replica changes for resources with `policy.completion` and for parent scopes that contain completion-aware direct children.
- `completions` and `completedIndexes` — configurable completion counts and index-based filtering at the `PodCliqueScalingGroup` and `PodCliqueSet` levels. In this release, all replicas must complete successfully for a resource to be considered Completed.
- Runtime deadline support (`maxRuntime`). Deadline semantics require a separate design for resource-level versus replica-level or attempt-level limits, and whether deadlines reset on gang restart. Since runtime deadlines are orthogonal to completion tracking and gang restart, they are deferred to keep the first release focused.
- Pod cleanup policies other than the fixed default (retain terminal pods, delete active pods on terminal state).
- TTL-based automatic workload deletion after completion.

## Proposal

Job support extends Grove's existing workload hierarchy with completion-aware behavior. The root signal is at the `PodClique` level: a `PodClique` becomes completion-aware by setting at least one supported field under `spec.policy.completion`. Parent `PodCliqueScalingGroup` and `PodCliqueSet` resources become completion-aware bottom-up when they contain completion-aware children. A parent may set its own `spec.policy.completion` to configure parent-level completion and failure behavior, but that policy does not make child `PodClique`s completion-aware.

Completion and failure are evaluated bottom-up. At each level, the resource is **Completed** when all required pods or completion-aware direct children have succeeded, and **Failed** when enough pods or children have failed that the completion criterion is unreachable — regardless of how remaining children resolve. Once a resource reaches **Failed**, it cannot subsequently become **Completed**.

When a completion-aware `PodClique` fails, its completion-aware parent (`PodCliqueScalingGroup` or `PodCliqueSet`) deletes and recreates the affected gang as a unit. This consumes one restart from the parent's per-replica budget. A `PodCliqueScalingGroup` or `PodCliqueSet` fails when enough of its replicas have exhausted their budget that the completion target is unreachable.

Two nested policy fields control completion-aware behavior:

- **`policy.completion.success.completedNames`** *(PodCliqueScalingGroup / PodCliqueSet only)*: Named direct children within a replica that must complete for that replica to count as Completed. If omitted, all completion-aware direct children must complete. At the `PodClique` level, all pods must complete for the `PodClique` to be considered Completed.
- **`policy.completion.failure.maxRestarts`** *(PodClique / PodCliqueScalingGroup / PodCliqueSet)*: Restart budget. On `PodCliqueScalingGroup` and `PodCliqueSet`, this is a per-replica gang restart budget. On `PodClique`, only `0` is supported in this release, meaning any pod failure makes the `PodClique` Failed.

Regular-mode `PodClique`s within a `PodCliqueScalingGroup` or `PodCliqueSet` are excluded from completion evaluation. A resource can be Completed even if some of its children remain running in regular mode.

**Gang termination for completion-aware resources.** Gang scheduling is unchanged: `minAvailable` continues to gate pod launch until the full gang can be placed simultaneously, on initial start and after each restart. `minAvailable` also continues to be passed to scheduler backends as the gang's minimum member count. For gang termination, completion-aware resources use the `Failed` condition as the termination signal instead of `MinAvailableBreached`, and fire immediately without `terminationDelay`. A pod failure or eviction in a completion-aware `PodClique` moves the pod to `pod phase=Failed`, causing the owning `PodClique` to set the `Failed` condition and triggering this gang-termination path. Crucially, the termination scope is identical to the existing gang-termination implementation: the same gang boundary that would have been torn down on a `MinAvailableBreached` event is torn down on a `Failed` event. Completion-aware behavior changes the runtime trigger, not the scheduling contract or termination scope.

### User Stories

#### Story 1: Running a finite job in Grove

As a platform engineer, I want to run completion-aware workloads in Grove alongside existing long-running inference services, so that I can use a single orchestration system for both training jobs and inference deployments, with Grove handling gang scheduling, topology placement, and restart budgets consistently across both workload types.

#### Story 2: Synchronous distributed training (all-ranks completion)

As a machine learning engineer running a multi-node AllReduce training job, I want all worker pods to complete successfully before the job is considered done, so that a single worker failure triggers a full gang restart rather than leaving the remaining workers stalled at a collective barrier indefinitely.

#### Story 3: Leader-driven completion

As a machine learning engineer running a leader-worker training job, I want the job to be considered complete when the leader pod exits successfully — regardless of whether worker pods have exited — so that I can use the leader's exit code as the authoritative signal of job success, while workers remain running until the leader finishes.

### Limitations/Risks & Mitigations

**Application-level hangs without pod failure.**
For completion-aware resources, `MinAvailableBreached`-based gang termination is disabled. Ordinary pod failures and evictions are still handled: the pod reaches `pod phase=Failed`, the owning `PodClique` sets the `Failed` condition, and Grove terminates or restarts the gang through the failure path. The remaining risk is narrower: if the cluster does not surface a failed pod and the application keeps running despite a lost peer or broken collective, Grove cannot infer the application-level deadlock from availability alone. Workloads should use framework-level failure detection, such as rendezvous timeouts, and exit non-zero when peer loss makes progress impossible.

**API overhead and topology placement loss at scale.**
Completion-aware `PodClique`s use `restartPolicy: Never`, which disables kubelet's in-place container restart. Grove is solely responsible for recreating pods on failure. At scale, this means every gang restart triggers a full pod deletion and recreation cycle — incurring Kubernetes API overhead and requiring the scheduler to re-place all pods from scratch. Re-scheduling at scale can take meaningful time and may not recover the same topology placement that the previous attempt had. This is a known limitation of the design.

**Log loss on retry.**
When a failed `PodClique` is deleted and recreated during a gang restart, terminal pods from the previous attempt are cascade-deleted along with it. Logs from failed attempts are not durably retained across retries. Mitigation: users who need per-attempt logs should rely on a cluster-level logging stack (e.g. Fluentd, Loki) rather than `kubectl logs`.

## Design Details

### Completion Policy

Completion-aware behavior is rooted at the `PodClique` level. A `PodClique` becomes completion-aware by setting at least one concrete supported leaf under `spec.policy.completion`. Omitting `policy.completion` keeps the `PodClique` in regular mode.

`PodCliqueScalingGroup` and `PodCliqueSet` become completion-aware when they contain at least one completion-aware direct child. Their own `spec.policy.completion` is optional and configures how the parent evaluates those children. Setting `policy.completion` on a parent with no completion-aware direct children is invalid. When `completedNames` is set, each listed name must refer to a completion-aware direct child.

An empty `policy.completion` object, including one that only contains empty `success` or `failure` objects, is invalid on every resource.

For a completion-aware `PodClique`, the pod template `restartPolicy` may be omitted or set to `Never`. If omitted, the Grove mutating webhook sets it to `Never`. Explicit `Always` and `OnFailure` are validation errors for completion-aware `PodClique`s. For regular `PodClique`s, omitted or `Always` `restartPolicy` is valid; explicit `Never` is rejected unless `policy.completion` is set, and `OnFailure` remains unsupported.

Scaling is rejected for completion-aware workloads after creation. The initial `replicas` values define the fixed job shape, but later manual changes to `replicas` are not supported for resources with `policy.completion` or for parent scopes that contain completion-aware direct children. Grove-managed autoscaling is also rejected: a completion-aware `PodClique` cannot set `autoScalingConfig`, and a `PodCliqueScalingGroup` containing completion-aware children cannot set `scaleConfig`.

### New API Fields

The new fields are nested under `spec.policy.completion`. The API shape is:

**PodCliqueSpec**, **PodCliqueScalingGroupSpec**, and **PodCliqueSetSpec** each gain:

```go
// Policy describes optional workload behavior controlled by Grove.
// +optional
Policy *WorkloadPolicy `json:"policy,omitempty"`

type WorkloadPolicy struct {
    // Completion enables completion-aware behavior for this resource when at least
    // one supported field under it is explicitly set.
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
    // CompletedNames lists direct child names that must reach Completed for a
    // PodCliqueScalingGroup or PodCliqueSet replica to be considered Completed.
    // If omitted, all completion-aware direct children must complete.
    // +optional
    CompletedNames []string `json:"completedNames,omitempty"`
}

type CompletionFailurePolicy struct {
    // MaxRestarts is the maximum number of restart attempts allowed after failure.
    // On PodCliqueScalingGroup and PodCliqueSet it is a per-replica gang restart
    // budget. On PodClique, only 0 is supported in this release.
    // +optional
    // +kubebuilder:default=0
    MaxRestarts *int32 `json:"maxRestarts,omitempty"`
}
```

`policy.completion.success.completedNames` is valid only for `PodCliqueScalingGroup` and `PodCliqueSet`. When set, it must be non-empty. `PodClique` always uses all-pods success: all pods must reach `pod phase=Succeeded`.

`policy.completion.failure.maxRestarts` is valid for all three resources. On `PodCliqueScalingGroup` and `PodCliqueSet`, values must be non-negative and default to `0` when omitted by a completion-aware resource. On `PodClique`, the only supported explicit value is `0`; values greater than `0` are future work because pod-level retry is not implemented.

Defaults apply only after a resource is completion-aware, either because a `PodClique` explicitly sets a supported completion-policy leaf or because a parent contains completion-aware direct children. Defaults do not make an empty `policy.completion` object valid.

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
        completedNames: [trainer]
  template:
    podCliqueScalingGroups:
    - name: trainer
      policy:
        completion:
          success:
            completedNames: [leader]
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

A completion-aware `PodClique` is **Completed** when all of its pods have exited with code 0 (`pod phase=Succeeded`). It is **Failed** when any pod exits with a non-zero code (`pod phase=Failed`), since pod-level retry is not supported and a single failure makes the all-pods completion criterion unreachable.

Regular-mode `PodClique`s never set `Completed` or `Failed`.

**PodCliqueScalingGroup**

For a completion-aware `PodCliqueScalingGroup`, evaluation is two-level:

1. *Replica state*: a replica is **Completed** when all required completion-aware child `PodClique`s are `Completed` — either all completion-aware children, or the named children in `policy.completion.success.completedNames` when it is set. A replica is permanently **Failed** when a required child `PodClique` fails and no restart budget remains (`maxRestarts` exhausted, or `maxRestarts: 0`). A failure of a completion-aware `PodClique` not listed in `completedNames` triggers a gang restart and consumes budget while budget remains; it does not mark the replica as `Failed` until no restart budget remains.

2. *PCSG state*: the PCSG is **Completed** when all replicas are `Completed`. It is **Failed** when enough replicas have exhausted their budget that the remaining replicas cannot satisfy the completion criterion.

**PodCliqueSet**

For a completion-aware `PodCliqueSet`, evaluation follows the same two-level pattern as PCSG:

1. *Replica state*: a replica is **Completed** when all required completion-aware direct children are `Completed` — either all completion-aware `PodClique`s and `PodCliqueScalingGroup`s, or the named children in `policy.completion.success.completedNames` when it is set. A replica is permanently **Failed** when a required direct child fails and no restart budget remains. A failure of a completion-aware direct child not listed in `completedNames` follows the same gang-restart behavior as PCSG: it consumes budget while budget remains, and fails the replica when no budget remains.

2. *PCS state*: the PCS is **Completed** when all replicas are `Completed`. It is **Failed** when enough replicas have exhausted their budget that the remaining replicas cannot satisfy the completion criterion.

**General invariants**

- `Failed` is irreversible: a resource that reaches `Failed` will not subsequently transition to `Completed`, by construction of the failure definition.
- Terminal states (`Completed`, `Failed`) are written to status before any pod cleanup begins.
- Completion and failure are evaluated bottom-up, but cleanup after a parent reaches a terminal state is applied top-down to all non-terminal children in that terminal scope.
- A terminal parent scope is a stop condition for descendants: child controllers must not recreate pods or child resources when their owning `PodCliqueScalingGroup` replica, `PodCliqueSet` replica, or `PodCliqueSet` resource is already terminal.

### Gang Restart Flow

When a completion-aware `PodClique` reaches `Failed`, its completion-aware parent evaluates whether to restart or mark the replica as terminally failed.

**PCLQ failure handled by completion-aware PCSG:**

1. The PCSG increments `replicaRestartCounts[replicaIndex]`.
2. If the budget is not exhausted: the PCSG deletes the failed `PodClique` and recreates it from the template. The new `PodClique` is placed by the scheduler as a complete gang before any pods run.
3. If the budget is exhausted: the PCSG marks that replica as failed and re-evaluates its own terminal conditions.

**PCLQ or PCSG failure handled by completion-aware PCS:**

When a constituent completion-aware `PodClique` or `PodCliqueScalingGroup` within a PCS replica fails, the PCS treats it as a gang-level failure for the whole replica:

1. The PCS increments `replicaRestartCounts[replicaIndex]`.
2. If the budget is not exhausted: the PCS deletes all constituents of that replica (all `PodClique`s and `PodCliqueScalingGroup`s) and recreates them together from the template.
3. If the budget is exhausted: the PCS marks that replica as failed and re-evaluates its own terminal conditions.

**Ordering guarantee.** In all cases, terminal conditions and updated `replicaRestartCounts` are persisted to status before any deletion begins. If the controller restarts mid-cleanup, it can resume from the persisted state without double-counting restarts or re-creating resources that were already deleted. Cleanup of active (non-terminal) pods when a resource reaches a terminal state is described in [Cleanup Behavior](#cleanup-behavior).

**Gang scheduling on restart.** Recreated pods are placed by the scheduler as a complete gang, consistent with the initial launch behavior.

### Condition and Status Model

**New conditions.** Two terminal conditions are added across all three resource types:

- `Completed` — all required pods or completion-aware children have succeeded.
- `Failed` — enough pods or children have failed that the completion criterion is permanently unreachable. Once set, this condition is irreversible.

These conditions are represented in the standard `conditions` list on each resource's status. Regular-mode resources never set these conditions. Grove does not introduce `Pending` or `Running` states for this feature.

Terminal conditions are not propagated top-down to children that did not independently complete or fail. For example, if a `PodCliqueScalingGroup` replica completes because the `PodClique`s listed in `completedNames` completed successfully, any other child `PodClique`s may not have a `Completed` or `Failed` condition. Instead, the parent terminal state makes them no longer desired, and top-down cleanup removes their active pods and non-terminal child resources.

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

Grove applies a single fixed cleanup policy for completion-aware resources: terminal state is calculated bottom-up, but cleanup is applied top-down. Active pods are deleted when the owning completion-aware scope reaches a terminal state; terminal pods are retained.

This distinction is important for partial-completion policies. For example, a `PodCliqueScalingGroup` replica may be considered `Completed` because the `PodClique`s listed in `completedNames` completed successfully, while other child `PodClique`s are still running. Once the replica is terminal, the PCSG controller deletes the non-terminal child `PodClique`s or active pods in that replica so they stop consuming resources. Similarly, once a `PodCliqueSet` replica or the whole PCS reaches a terminal state, the PCS controller cleans up active child `PodClique`s and `PodCliqueScalingGroup`s in that completed or failed scope.

**On `PodClique` terminal state (`Completed` or `Failed`):**
- Delete all active pods (`Pending`, `Running`) in the `PodClique`.
- Retain all terminal pods (`Succeeded`, `Failed`) for log access via `kubectl logs`.

**On `PodCliqueScalingGroup` replica terminal state:**
- Delete active pods and non-terminal child `PodClique`s belonging to that replica.
- This covers `completedNames`, where only selected children are required for completion and the remaining children may still be running.

**On `PodCliqueSet` replica or resource terminal state:**
- Delete active pods and non-terminal child `PodClique`s / `PodCliqueScalingGroup`s belonging to the terminal scope.
- This prevents completed or failed PCS scopes from continuing to consume resources after the parent decision has already been made.

**Recreation guard:**
- Controllers must not recreate pods, `PodClique`s, or `PodCliqueScalingGroup`s whose owning PCSG/PCS scope is already terminal.

**During a gang restart:**
- The failed `PodClique` is deleted entirely (not retained), which cascade-deletes its terminal pods as well. Logs from the failed attempt are not preserved across restarts. See [Log loss on retry](#limitationsrisks--mitigations).

**Terminal pod retention:**
- Terminal pods remain available until the workload is deleted by the user or an external TTL policy (out of scope for this release).

**Ordering:**
- Terminal conditions are always written to status before any pod deletion begins.

### Monitoring

Job support surfaces observability through status conditions and Kubernetes events for key lifecycle transitions.

**Status conditions.** `Completed` and `Failed` conditions are the primary status signals for completion-aware resources:

| Condition | Status | Meaning |
|---|---|---|
| `Completed` | `True` | All required pods or completion-aware children succeeded. |
| `Failed` | `True` | Completion is permanently unreachable. |

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

- Validation: empty `policy.completion` and empty nested `success` / `failure` objects are rejected; `completedNames` on `PodClique` is rejected; `completedNames` is non-empty when set; `PodClique` `maxRestarts` values greater than `0` are rejected.
- Validation: completion-aware `PodClique` accepts omitted `restartPolicy` or `restartPolicy: Never`; omitted `restartPolicy` is defaulted to `Never`; explicit `Always` and `OnFailure` are rejected for completion-aware `PodClique`s; explicit `Never` is rejected for regular `PodClique`s.
- Validation: `policy.completion` on a parent with no completion-aware direct children is rejected, and `completedNames` entries refer only to completion-aware direct children.
- Validation: autoscaling configuration and manual replica changes on resources with `policy.completion` or parent scopes containing completion-aware direct children are rejected.
- Completion evaluation logic at each level: all-pods success → `Completed`; any pod failure → PCLQ `Failed`; named-children completion → PCSG/PCS replica `Completed`; non-`completedNames` child failure triggers restart but not immediate replica failure.
- Failure evaluation: budget exhaustion → replica `Failed`; `Failed` is irreversible.
- `replicaRestartCounts` increments correctly on each restart and is never decremented.
- Terminal conditions are written before pod deletion (ordering guarantee).

**E2e tests** (new file: `e2e/tests/job_support_test.go`)

- **All-ranks completion**: all workers complete successfully → PCS reaches `Completed`.
- **Pod failure → gang restart**: one pod fails → PCLQ fails → parent restarts the gang → restart budget decremented.
- **Budget exhaustion**: replica exhausts `maxRestarts` → PCSG/PCS fails.
- **Leader-driven completion**: leader exits 0, workers still running → PCSG replica `Completed`, active worker pods cleaned up.
- **Mixed completion-aware/regular**: completion-aware PCLQ completes alongside regular PCLQ → parent reaches `Completed`.
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

This GREP uses concrete leaves under `spec.policy.completion` as the opt-in signal instead. An empty `policy.completion` object is invalid, so the API does not rely on a bare section existing without behavior. At the same time, the nested policy shape keeps completion and failure controls together and leaves room for future extensions under the same API surface.

### Status phase for job observability

A dedicated `phase` field was considered for job completion and failure. It would provide a compact mutually-exclusive status value, but it also raises follow-up lifecycle questions: if `Completed` and `Failed` are phases, users may reasonably expect non-terminal phases such as `Pending` and `Running`, and those states would also need semantics for long-running Grove workloads.

This GREP uses conditions instead. `Completed` and `Failed` are only set when applicable, follow common Kubernetes status conventions, and avoid introducing a general lifecycle state machine for resources that are otherwise expected to run indefinitely.

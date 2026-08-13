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
  - [Job Mode Signal](#job-mode-signal)
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
  - [Status phase for job observability](#status-phase-for-job-observability)
<!-- /toc -->

## Summary

Grove currently supports long-running workloads such as inference services, where pods are expected to run indefinitely. This GREP introduces job support — the ability to run finite, completion-oriented workloads in Grove. Job-mode workloads exit normally when their task is done, and Grove tracks completion and failure bottom-up through the `PodClique` → `PodCliqueScalingGroup` → `PodCliqueSet` hierarchy. New API fields (`completedNames`, `maxRestarts`) give users precise control over named-child completion and failure tolerance. Gang-restart semantics ensure that tightly coupled distributed workloads — such as multi-node training jobs — are retried as a unit when a failure occurs.

## Motivation

Grove is purpose-built for AI infrastructure workloads. While its current model — gang scheduling, `minAvailable`-based availability, automatic restart on pod loss — serves long-running inference services well, it does not fit distributed training or batch workloads that run to completion. These workloads have fundamentally different lifecycle semantics: a pod exiting with code 0 is success, not a failure to be restarted; a single worker failure in a tightly coupled AllReduce job stalls the entire gang and warrants a full restart, not a local pod replacement; and the job as a whole has a well-defined done state.

Kubernetes' own `restartPolicy: Never` provides the right pod-level primitive — pods reach a terminal state (`Succeeded` or `Failed`) on exit — but Grove currently has no way to act on those terminal states. Without job support, users running training workloads on Grove must layer their own completion tracking and restart logic on top, or use a separate job controller that is unaware of Grove's gang scheduling and topology placement.

This GREP closes that gap by extending Grove's existing hierarchy with the completion and failure semantics needed for finite workloads, while reusing the existing gang scheduling, topology, and restart machinery.

### Goals

- Enable finite, completion-oriented workloads in Grove alongside existing long-running workloads, with Grove owning all completion and restart decisions.
- Define bottom-up completion and failure semantics across the `PodClique` → `PodCliqueScalingGroup` → `PodCliqueSet` hierarchy.
- Support diverse job framework patterns — from all-ranks completion (every worker must succeed) to leader-driven completion (a single designated pod's exit determines the outcome).
- Introduce `completedNames` and `maxRestarts` API fields at the `PodCliqueScalingGroup` and `PodCliqueSet` levels to express named-child completion and per-replica restart budgets.
- Support gang restart: when a `PodClique` or `PodCliqueScalingGroup` fails, the parent deletes and recreates the affected scope as a unit, consuming from a per-replica restart budget.
- Guarantee that terminal states are persisted to status before any pod cleanup, and that terminal pods (`Succeeded`, `Failed`) are retained for log access until the workload is deleted.

### Non-Goals

- **No pod-level retry.** A failed pod within a `PodClique` is not replaced in isolation. Any pod failure immediately fails the `PodClique`; the retry scope is the gang, owned by the parent `PodCliqueScalingGroup` or `PodCliqueSet`.
- **No scaling for job-mode workloads.** Job workloads use fixed replica counts. Grove rejects autoscaling configuration and manual replica changes for job-mode `PodClique`, `PodCliqueScalingGroup`, and `PodCliqueSet` resources.
- `completions` and `completedIndexes` — configurable completion counts and index-based filtering at the `PodCliqueScalingGroup` and `PodCliqueSet` levels. In this release, all replicas must complete successfully for a resource to be considered Completed.
- Runtime deadline support (`maxRuntime`). Deadline semantics require a separate design for resource-level versus replica-level or attempt-level limits, and whether deadlines reset on gang restart. Since runtime deadlines are orthogonal to completion tracking and gang restart, they are deferred to keep the first release focused.
- Pod cleanup policies other than the fixed default (retain terminal pods, delete active pods on terminal state).
- TTL-based automatic workload deletion after completion.

## Proposal

Job support extends Grove's existing workload hierarchy to handle finite workloads. A `PodClique` enters job mode when its pod template sets `restartPolicy: Never`. `restartPolicy` is not a new field — today `Always` is the default and only permitted value. With this GREP, `Never` becomes a valid option: it remains optional (defaulting to `Always` preserves existing behavior), and setting it signals to Grove that this `PodClique` is job-mode. In job mode, pods reach terminal Kubernetes states (`Succeeded` / `Failed`) on exit, and Grove owns all completion and restart decisions — kubelet does not restart containers in-place.

Completion and failure are evaluated bottom-up. At each level, the resource is **Completed** when all job-mode children have succeeded, and **Failed** when enough children have failed that the completion criterion is unreachable — regardless of how remaining children resolve. Once a resource reaches **Failed**, it cannot subsequently become **Completed**.

When a `PodClique` fails, its parent (`PodCliqueScalingGroup` or `PodCliqueSet`) deletes it and recreates the full gang as a unit. This consumes one restart from the parent's per-replica budget. A `PodCliqueScalingGroup` or `PodCliqueSet` fails when enough of its replicas have exhausted their budget that the completion target is unreachable.

Two new API fields control job-mode policy:

- **`completedNames`** *(PodCliqueScalingGroup / PodCliqueSet only)*: Named children within a replica that must complete for that replica to count as Completed. If omitted, all job-mode children must complete. At the `PodClique` level, all pods must complete for the `PodClique` to be considered Completed.
- **`maxRestarts`** *(PodCliqueScalingGroup / PodCliqueSet only)*: Per-replica restart budget. A replica that exhausts its budget is considered failed.

Non-job-mode `PodClique`s (`restartPolicy: Always`) within a `PodCliqueScalingGroup` or `PodCliqueSet` are excluded from completion evaluation. A resource can be Completed even if some of its children remain running in long-running mode.

**Gang termination in job mode.** Gang scheduling is unchanged: `minAvailable` continues to gate pod launch until the full gang can be placed simultaneously, on initial start and after each restart. `minAvailable` also continues to be passed to scheduler backends as the gang's minimum member count. For gang termination, job mode uses the `Failed` condition as the termination signal instead of `MinAvailableBreached`, and fires immediately without `terminationDelay`. A pod failure or eviction with `restartPolicy: Never` moves the pod to a terminal `Failed` state, causing the owning `PodClique` to set the `Failed` condition and triggering this gang-termination path. Crucially, the termination scope is identical to the existing gang-termination implementation: the same gang boundary that would have been torn down on a `MinAvailableBreached` event is torn down on a `Failed` event. Job mode changes the runtime trigger, not the scheduling contract or termination scope.

### User Stories

#### Story 1: Running a finite job in Grove

As a platform engineer, I want to run finite, completion-oriented workloads in Grove — alongside existing long-running inference services — so that I can use a single orchestration system for both training jobs and inference deployments, with Grove handling gang scheduling, topology placement, and restart budgets consistently across both workload types.

#### Story 2: Synchronous distributed training (all-ranks completion)

As a machine learning engineer running a multi-node AllReduce training job, I want all worker pods to complete successfully before the job is considered done, so that a single worker failure triggers a full gang restart rather than leaving the remaining workers stalled at a collective barrier indefinitely.

#### Story 3: Leader-driven completion

As a machine learning engineer running a leader-worker training job, I want the job to be considered complete when the leader pod exits successfully — regardless of whether worker pods have exited — so that I can use the leader's exit code as the authoritative signal of job success, while workers remain running until the leader finishes.

### Limitations/Risks & Mitigations

**Application-level hangs without pod failure.**
In job mode, `MinAvailableBreached`-based gang termination is disabled. Ordinary pod failures and evictions are still handled: with `restartPolicy: Never`, the pod reaches a terminal `Failed` state, the owning `PodClique` sets the `Failed` condition, and Grove terminates or restarts the gang through the failure path. The remaining risk is narrower: if the cluster does not surface a failed pod and the application keeps running despite a lost peer or broken collective, Grove cannot infer the application-level deadlock from availability alone. Workloads should use framework-level failure detection, such as rendezvous timeouts, and exit non-zero when peer loss makes progress impossible.

**API overhead and topology placement loss at scale.**
Job mode uses `restartPolicy: Never`, which disables kubelet's in-place container restart. Grove is solely responsible for recreating pods on failure. At scale, this means every gang restart triggers a full pod deletion and recreation cycle — incurring Kubernetes API overhead and requiring the scheduler to re-place all pods from scratch. Re-scheduling at scale can take meaningful time and may not recover the same topology placement that the previous attempt had. This is a known limitation of the design.

**Log loss on retry.**
When a failed `PodClique` is deleted and recreated during a gang restart, terminal pods from the previous attempt are cascade-deleted along with it. Logs from failed attempts are not durably retained across retries. Mitigation: users who need per-attempt logs should rely on a cluster-level logging stack (e.g. Fluentd, Loki) rather than `kubectl logs`.

## Design Details

### Job Mode Signal

Job mode is signaled by setting `restartPolicy: Never` on the pod template spec of a `PodClique`. No separate mode flag is needed. `restartPolicy: Always` (the current default) continues to work unchanged; `restartPolicy: OnFailure` is explicitly forbidden and rejected at admission.

A `PodCliqueScalingGroup` or `PodCliqueSet` is in job mode when at least one of its child `PodClique`s is in job mode. The job-mode fields (`completedNames`, `maxRestarts`) are valid on a `PodCliqueScalingGroup` or `PodCliqueSet` only when at least one child is in job mode; setting them on a fully long-running resource is a validation error.

Scaling is rejected for job-mode workloads after creation. The initial `replicas` values define the fixed job shape, but later manual changes to `replicas` are not supported for job-mode `PodClique`, `PodCliqueScalingGroup`, or `PodCliqueSet` resources. Grove-managed autoscaling is also rejected: a job-mode `PodClique` cannot set `autoScalingConfig`, and a `PodCliqueScalingGroup` containing any job-mode child cannot set `scaleConfig`.

### New API Fields

The new fields are added flat under `spec`, at the same level as `replicas` and `minAvailable`. No new nested struct is introduced.

**PodCliqueScalingGroupSpec** and **PodCliqueSetSpec** each gain:

```go
// CompletedNames lists the names of child PodCliques (for PCSG) or PodCliques /
// PodCliqueScalingGroups (for PCS) within a replica that must reach Completed
// for that replica to be considered Completed. If omitted, all job-mode children
// must complete.
// +optional
CompletedNames []string `json:"completedNames,omitempty"`

// MaxRestarts is the maximum number of times a replica may be restarted after
// failure. Each restart consumes one unit from this per-replica budget. A replica
// that exhausts its budget is considered failed. Defaults to 0 — the first failure
// immediately marks the replica failed with no restart attempt.
// +optional
// +kubebuilder:default=0
MaxRestarts *int32 `json:"maxRestarts,omitempty"`
```

### Examples

**All-ranks completion** — all 8 workers must succeed; the gang retries up to 3 times:

```yaml
# PodCliqueSet spec (relevant fields only)
spec:
  replicas: 1
  maxRestarts: 3
  template:
    cliques:
    - name: worker
      spec:
        replicas: 8
        minAvailable: 8
        podSpec:
          restartPolicy: Never
```

**Leader-driven completion** — the PCSG replica is complete when the leader exits 0, regardless of workers:

```yaml
# PodCliqueSet spec (relevant fields only)
spec:
  replicas: 1
  template:
    podCliqueScalingGroups:
    - name: trainer
      completedNames: [leader]
      maxRestarts: 3
      cliqueNames: [leader, worker]
    cliques:
    - name: leader
      spec:
        replicas: 1
        minAvailable: 1
        podSpec:
          restartPolicy: Never
    - name: worker
      spec:
        replicas: 7
        minAvailable: 7
        podSpec:
          restartPolicy: Never
```

### Completion and Failure Evaluation

Completion and failure are evaluated independently at each level, using only the observed state of direct children.

**PodClique**

A `PodClique` is **Completed** when all of its pods have exited with code 0 (`pod phase=Succeeded`). It is **Failed** when any pod exits with a non-zero code (`pod phase=Failed`), since pod-level retry is not supported and a single failure makes the all-pods completion criterion unreachable.

Non-job-mode `PodClique`s (`restartPolicy: Always`) never set `Completed` or `Failed`.

**PodCliqueScalingGroup**

Evaluation is two-level:

1. *Replica state*: a replica is **Completed** when all of its job-mode child `PodClique`s are `Completed` — or, if `completedNames` is set, when all named children are `Completed`. A replica is permanently **Failed** when a required child `PodClique` fails and no restart budget remains (`maxRestarts` exhausted, or `maxRestarts: 0`). A failure of a `PodClique` not listed in `completedNames` triggers a gang restart and consumes budget, but does not immediately mark the replica as `Failed` — the named children can still complete on the next attempt.

2. *PCSG state*: the PCSG is **Completed** when all replicas are `Completed`. It is **Failed** when enough replicas have exhausted their budget that the remaining replicas cannot satisfy the completion criterion.

**PodCliqueSet**

Follows the same two-level pattern as PCSG, with constituent `PodClique`s and `PodCliqueScalingGroup`s in place of `PodClique`s.

**General invariants**

- `Failed` is irreversible: a resource that reaches `Failed` will not subsequently transition to `Completed`, by construction of the failure definition.
- Terminal states (`Completed`, `Failed`) are written to status before any pod cleanup begins.
- Completion and failure are evaluated bottom-up, but cleanup after a parent reaches a terminal state is applied top-down to all non-terminal children in that terminal scope.
- A terminal parent scope is a stop condition for descendants: child controllers must not recreate pods or child resources when their owning `PodCliqueScalingGroup` replica, `PodCliqueSet` replica, or `PodCliqueSet` resource is already terminal.

### Gang Restart Flow

When a `PodClique` reaches `Failed`, its parent evaluates whether to restart or mark the replica as terminally failed.

**PCLQ failure handled by PCSG:**

1. The PCSG increments `replicaRestartCounts[replicaIndex]`.
2. If the budget is not exhausted: the PCSG deletes the failed `PodClique` and recreates it from the template. The new `PodClique` is placed by the scheduler as a complete gang before any pods run.
3. If the budget is exhausted: the PCSG marks that replica as failed and re-evaluates its own terminal conditions.

**PCLQ or PCSG failure handled by PCS:**

When a constituent `PodClique` or `PodCliqueScalingGroup` within a PCS replica fails, the PCS treats it as a gang-level failure for the whole replica:

1. The PCS increments `replicaRestartCounts[replicaIndex]`.
2. If the budget is not exhausted: the PCS deletes all constituents of that replica (all `PodClique`s and `PodCliqueScalingGroup`s) and recreates them together from the template.
3. If the budget is exhausted: the PCS marks that replica as failed and re-evaluates its own terminal conditions.

**Ordering guarantee.** In all cases, terminal conditions and updated `replicaRestartCounts` are persisted to status before any deletion begins. If the controller restarts mid-cleanup, it can resume from the persisted state without double-counting restarts or re-creating resources that were already deleted. Cleanup of active (non-terminal) pods when a resource reaches a terminal state is described in [Cleanup Behavior](#cleanup-behavior).

**Gang scheduling on restart.** Recreated pods are placed by the scheduler as a complete gang, consistent with the initial launch behavior.

### Condition and Status Model

**New conditions.** Two terminal conditions are added across all three resource types:

- `Completed` — all required job-mode children have succeeded.
- `Failed` — enough children have failed that the completion criterion is permanently unreachable. Once set, this condition is irreversible.

These conditions are represented in the standard `conditions` list on each resource's status. Non-job-mode resources never set these conditions. Grove does not introduce `Pending` or `Running` states for this feature.

Terminal conditions are not propagated top-down to children that did not independently complete or fail. For example, if a `PodCliqueScalingGroup` replica completes because the `PodClique`s listed in `completedNames` completed successfully, any other child `PodClique`s may not have a `Completed` or `Failed` condition. Instead, the parent terminal state makes them no longer desired, and top-down cleanup removes their active pods and non-terminal child resources.

Controllers must treat a terminal owner scope as authoritative when deciding whether to create or recreate descendants. A child controller must not recreate pods or child resources when its owning `PodCliqueScalingGroup` replica, `PodCliqueSet` replica, or `PodCliqueSet` resource is already terminal.

```go
const (
    ConditionTypeCompleted = "Completed"
    ConditionTypeFailed    = "Failed"

    ConditionReasonAllRequiredChildrenCompleted = "AllRequiredChildrenCompleted"
    ConditionReasonRestartBudgetExhausted       = "RestartBudgetExhausted"
)

// Conditions represent the latest available observations of the job-mode resource.
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

Grove applies a single fixed cleanup policy for job-mode resources: terminal state is calculated bottom-up, but cleanup is applied top-down. Active pods are deleted when the owning job-mode scope reaches a terminal state; terminal pods are retained.

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

**Status conditions.** `Completed` and `Failed` conditions are the primary status signals for job-mode resources:

| Condition | Status | Meaning |
|---|---|---|
| `Completed` | `True` | All required job-mode children succeeded. |
| `Failed` | `True` | Completion is permanently unreachable. |

Non-job-mode resources never set these conditions.

**Kubernetes events.** Events carry the detail behind condition transitions and operational actions. The `involvedObject` field identifies the resource the event is attached to; the `message` field carries per-event detail such as replica index and child name. The `involvedObject.kind` distinguishes PCSG-level from PCS-level events sharing the same reason string.

| Attached to | Type | Reason | Message carries |
|---|---|---|---|
| PCLQ / PCSG / PCS | `Normal` | `JobCompleted` | — |
| PCLQ / PCSG / PCS | `Warning` | `JobFailed` | Failure reason (e.g. pod failure, restart budget exhausted) |
| PCSG / PCS | `Warning` | `GangRestartTriggered` | Replica index, failed child name |
| PCSG / PCS | `Warning` | `RestartBudgetExhausted` | Replica index |

The existing `PodCliqueScalingGroupReplicaDeleteSuccessful` / `PodCliqueSetReplicaDeleteSuccessful` events continue to fire on gang restart as before — `GangRestartTriggered` is an additional, job-mode-specific signal that explicitly names the cause.

### Test Plan

**Unit tests**

- Validation: `restartPolicy: OnFailure` is rejected; `completedNames` and `maxRestarts` on a fully long-running resource are rejected.
- Validation: autoscaling configuration and manual replica changes on job-mode resources are rejected.
- Completion evaluation logic at each level: all-pods success → `Completed`; any pod failure → PCLQ `Failed`; named-children completion → PCSG/PCS replica `Completed`; non-`completedNames` child failure triggers restart but not immediate replica failure.
- Failure evaluation: budget exhaustion → replica `Failed`; `Failed` is irreversible.
- `replicaRestartCounts` increments correctly on each restart and is never decremented.
- Terminal conditions are written before pod deletion (ordering guarantee).

**E2e tests** (new file: `e2e/tests/job_support_test.go`)

- **All-ranks completion**: all workers complete successfully → PCS reaches `Completed`.
- **Pod failure → gang restart**: one pod fails → PCLQ fails → parent restarts the gang → restart budget decremented.
- **Budget exhaustion**: replica exhausts `maxRestarts` → PCSG/PCS fails.
- **Leader-driven completion**: leader exits 0, workers still running → PCSG replica `Completed`, active worker pods cleaned up.
- **Mixed job/long-running**: job-mode PCLQ completes alongside long-running PCLQ → parent reaches `Completed`.
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

### Status phase for job observability

A dedicated `phase` field was considered for job completion and failure. It would provide a compact mutually-exclusive status value, but it also raises follow-up lifecycle questions: if `Completed` and `Failed` are phases, users may reasonably expect non-terminal phases such as `Pending` and `Running`, and those states would also need semantics for long-running Grove workloads.

This GREP uses conditions instead. `Completed` and `Failed` are only set when applicable, follow common Kubernetes status conventions, and avoid introducing a general lifecycle state machine for resources that are otherwise expected to run indefinitely.

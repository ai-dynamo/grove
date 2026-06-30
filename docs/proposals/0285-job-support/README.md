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
  - [Monitoring](#monitoring)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Grove currently supports long-running workloads such as inference services, where pods are expected to run indefinitely. This GREP introduces job support — the ability to run finite, completion-oriented workloads in Grove. Job-mode workloads exit normally when their task is done, and Grove tracks completion and failure bottom-up through the `PodClique` → `PodCliqueScalingGroup` → `PodCliqueSet` hierarchy. New API fields (`completedNames`, `maxRestarts`, `maxRuntime`) give users precise control over named-child completion, failure tolerance, and time bounds. Gang-restart semantics ensure that tightly coupled distributed workloads — such as multi-node training jobs — are retried as a unit when a failure occurs.

## Motivation

Grove is purpose-built for AI infrastructure workloads. While its current model — gang scheduling, `minAvailable`-based availability, automatic restart on pod loss — serves long-running inference services well, it does not fit distributed training or batch workloads that run to completion. These workloads have fundamentally different lifecycle semantics: a pod exiting with code 0 is success, not a failure to be restarted; a single worker failure in a tightly coupled AllReduce job stalls the entire gang and warrants a full restart, not a local pod replacement; and the job as a whole has a well-defined done state.

Kubernetes' own `restartPolicy: Never` provides the right pod-level primitive — pods reach a terminal phase (`Succeeded` or `Failed`) on exit — but Grove currently has no way to act on those terminal phases. Without job support, users running training workloads on Grove must layer their own completion tracking and restart logic on top, or use a separate job controller that is unaware of Grove's gang scheduling and topology placement.

This GREP closes that gap by extending Grove's existing hierarchy with the completion and failure semantics needed for finite workloads, while reusing the existing gang scheduling, topology, and restart machinery.

### Goals

- Enable finite, completion-oriented workloads in Grove alongside existing long-running workloads, with Grove owning all completion and restart decisions.
- Define bottom-up completion and failure semantics across the `PodClique` → `PodCliqueScalingGroup` → `PodCliqueSet` hierarchy.
- Support diverse job framework patterns — from all-ranks completion (every worker must succeed) to leader-driven completion (a single designated pod's exit determines the outcome).
- Introduce `completedNames`, `maxRestarts`, and `maxRuntime` API fields at the `PodCliqueScalingGroup` and `PodCliqueSet` levels, and `maxRuntime` at the `PodClique` level, to express named-child completion, per-replica restart budgets, and wall-clock deadlines.
- Support gang restart: when a `PodClique` or `PodCliqueScalingGroup` fails, the parent deletes and recreates the affected scope as a unit, consuming from a per-replica restart budget.
- Guarantee that terminal states are persisted to status before any pod cleanup, and that terminal pods (`Succeeded`, `Failed`) are retained for log access until the workload is deleted.

### Non-Goals

- **No pod-level retry.** A failed pod within a `PodClique` is not replaced in isolation. Any pod failure immediately fails the `PodClique`; the retry scope is the gang, owned by the parent `PodCliqueScalingGroup` or `PodCliqueSet`.
- `completions` and `completedIndexes` — configurable completion counts and index-based filtering at the `PodCliqueScalingGroup` and `PodCliqueSet` levels. In MVP, all replicas must complete successfully for a resource to be considered Completed.
- Pod cleanup policies other than the fixed default (retain terminal pods, delete active pods on terminal state).
- TTL-based automatic workload deletion after completion.

## Proposal

Job support extends Grove's existing workload hierarchy to handle finite workloads. A `PodClique` enters job mode when its pod template sets `restartPolicy: Never`. `restartPolicy` is not a new field — today `Always` is the default and only permitted value. With this GREP, `Never` becomes a valid option: it remains optional (defaulting to `Always` preserves existing behavior), and setting it signals to Grove that this `PodClique` is job-mode. In job mode, pods reach terminal Kubernetes phases (`Succeeded` / `Failed`) on exit, and Grove owns all completion and restart decisions — kubelet does not restart containers in-place.

Completion and failure are evaluated bottom-up. At each level, the resource is **Completed** when all job-mode children have succeeded, and **Failed** when enough children have failed that the completion criterion is unreachable — regardless of how remaining children resolve. Once a resource reaches **Failed**, it cannot subsequently become **Completed**.

When a `PodClique` fails, its parent (`PodCliqueScalingGroup` or `PodCliqueSet`) deletes it and recreates the full gang as a unit. This consumes one restart from the parent's per-replica budget. A `PodCliqueScalingGroup` or `PodCliqueSet` fails when enough of its replicas have exhausted their budget that the completion target is unreachable.

Three new API fields control job-mode policy:

- **`completedNames`** *(PodCliqueScalingGroup / PodCliqueSet only)*: Named children within a replica that must complete for that replica to count as Completed. If omitted, all job-mode children must complete. At the `PodClique` level, all pods must complete for the `PodClique` to be considered Completed.
- **`maxRestarts`** *(PodCliqueScalingGroup / PodCliqueSet only)*: Per-replica restart budget. A replica that exhausts its budget is considered failed.
- **`maxRuntime`** *(all levels)*: Wall-clock deadline from first start. Not reset on restarts. Exceeding it fails the resource immediately and permanently, regardless of any subsequent child outcomes.

Non-job-mode `PodClique`s (`restartPolicy: Always`) within a `PodCliqueScalingGroup` or `PodCliqueSet` are excluded from completion evaluation. A resource can be Completed even if some of its children remain running in long-running mode.

**Gang termination in job mode.** Gang scheduling is unchanged: `minAvailable` continues to gate pod launch until the full gang can be placed simultaneously, on initial start and after each restart. For gang termination, job mode replaces the `MinAvailableBreached` trigger with the `Failed` phase as the termination signal — and fires immediately, without `terminationDelay`. Crucially, the termination scope is identical to the existing gang-termination implementation: the same gang boundary that would have been torn down on a `MinAvailableBreached` event is torn down on a `Failed` event. Job mode changes the trigger, not the scope.

### User Stories

#### Story 1: Running a finite job in Grove

As a platform engineer, I want to run finite, completion-oriented workloads in Grove — alongside existing long-running inference services — so that I can use a single orchestration system for both training jobs and inference deployments, with Grove handling gang scheduling, topology placement, restart budgets, and runtime deadlines consistently across both workload types.

#### Story 2: Synchronous distributed training (all-ranks completion)

As a machine learning engineer running a multi-node AllReduce training job, I want all worker pods to complete successfully before the job is considered done, so that a single worker failure triggers a full gang restart rather than leaving the remaining workers stalled at a collective barrier indefinitely.

#### Story 3: Leader-driven completion

As a machine learning engineer running a leader-worker training job, I want the job to be considered complete when the leader pod exits successfully — regardless of whether worker pods have exited — so that I can use the leader's exit code as the authoritative signal of job success, while workers remain running until the leader finishes.

### Limitations/Risks & Mitigations

**Gang termination depends on application-level failure propagation.**
In job mode, `MinAvailableBreached`-based gang termination is disabled. If pods are evicted (e.g. due to a node failure) but the remaining running pods do not exit — for example because the application hangs rather than fails — Grove will not detect the partial gang and will not trigger a restart. Gang termination relies on the application observing the failure (e.g. a rendezvous timeout or broken collective) and exiting non-zero. Workloads that do not fail fast on peer loss may stall indefinitely.

**API overhead and topology placement loss at scale.**
Job mode uses `restartPolicy: Never`, which disables kubelet's in-place container restart. Grove is solely responsible for recreating pods on failure. At scale, this means every gang restart triggers a full pod deletion and recreation cycle — incurring Kubernetes API overhead and requiring the scheduler to re-place all pods from scratch. Re-scheduling at scale can take meaningful time and may not recover the same topology placement that the previous attempt had. This is a known limitation of the design.

**`maxRuntime` is not reset on restarts.**
`maxRuntime` is measured from the first start of the resource and does not reset across gang restarts. Users who set a tight deadline may find the budget exhausted before all retry attempts have run. Mitigation: documentation should clearly state this behavior; users should size `maxRuntime` to account for the full expected duration across all attempts, not just one.

**Log loss on retry.**
When a failed `PodClique` is deleted and recreated during a gang restart, terminal pods from the previous attempt are cascade-deleted along with it. Logs from failed attempts are not durably retained across retries. Mitigation: users who need per-attempt logs should rely on a cluster-level logging stack (e.g. Fluentd, Loki) rather than `kubectl logs`.

## Design Details

<!-- TODO -->

### Monitoring

<!-- TODO -->

### Test Plan

<!-- TODO -->

### Graduation Criteria

<!-- TODO -->

## Alternatives

<!-- TODO -->

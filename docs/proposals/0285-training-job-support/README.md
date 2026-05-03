# GREP-0285: Training Job Support for PodCliqueSet

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [WorkloadType Field](#workloadtype-field)
  - [Phase](#phase)
  - [trainingSpec Fields](#trainingspec-fields)
  - [terminationDelay](#terminationdelay)
  - [Controller Changes](#controller-changes)
    - [Defaulting Webhook](#defaulting-webhook)
    - [Validating Webhook](#validating-webhook)
    - [PodClique Controller](#podclique-controller)
    - [PodCliqueSet Controller](#podcliqueset-controller)
    - [PodCliqueScalingGroup Controller](#podcliquescalinggroup-controller)
    - [New Status Fields](#new-status-fields)
  - [State Persistence](#state-persistence)
  - [Monitoring](#monitoring)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
<!-- /toc -->

## Summary

Grove currently supports inference workloads exclusively. This proposal extends the `PodCliqueSet` API to support training jobs — finite workloads that complete normally once work is done. It introduces a `workloadType` field to distinguish training from inference, adds lifecycle phase tracking, and provides controls for runtime limits, restart budgets, and gang-aware failure handling.

## Motivation

Interest in grove and its adoption is growing in the community. Grove's unique capabilities — topology-aware scheduling, gang scheduling, clique startup ordering, and MNNVL support — are valuable not just for inference but also for distributed training workloads. The current `PodClique` controller assumes pods run indefinitely and recreates them when they exit, which is incompatible with training jobs where pods exiting successfully is the expected terminal state.

### Goals

- Introduce a `workloadType` field to distinguish `Inference` (default, no behavior change) from `Training` workloads.
- Add a `phase` field to `PodCliqueSetStatus` representing the workload lifecycle: `Pending`, `Running`, `Succeeded`, `Failed`.
- Add `maxRuntime` and `maxRestarts` controls under a `trainingSpec` stanza.
- Default `terminationDelay` to `0` for training workloads for immediate failure response.
- Disable rolling updates and replica count changes for training workloads via webhook enforcement.

### Non-Goals

- Per-clique failure or success policies (e.g., tolerate worker failures but not launcher failures).
- Suspend/resume support.

## Proposal

A new `workloadType` field is added to `PodCliqueSetSpec`. When set to `Training`, the operator changes its behavior in the following ways:

- Pods that exit with code 0 are not recreated. When all pods in a replica complete successfully, the replica is marked `Succeeded`.
- A `trainingSpec` stanza provides `maxRuntime` (maximum allowed duration) and `maxRestarts` (total restart budget across all replicas). Exceeding either terminates all pods and marks the workload `Failed`.
- `terminationDelay` defaults to `0` instead of 4 hours, so a pod failure triggers immediate teardown rather than waiting for the scheduler to reschedule.
- Rolling updates and replica count changes are rejected by the validation webhook for the lifetime of the workload.

The `phase` field in `PodCliqueSetStatus` reflects the current lifecycle state. A `Failed` condition with a structured `Reason` is maintained for programmatic failure cause disambiguation.

### Limitations/Risks & Mitigations

- **Backward compatibility:** `workloadType` defaults to `Inference`, so all existing `PodCliqueSet` resources are unaffected.
- **`terminationDelay` defaulting:** The change to default `terminationDelay: 0` only applies when `workloadType: Training`. Inference workloads retain the existing 4-hour default.

## Design Details

### WorkloadType Field

```go
type WorkloadType string

const (
    // WorkloadTypeInference is the default. Pods run indefinitely; failed pods are recreated.
    WorkloadTypeInference WorkloadType = "Inference"

    // WorkloadTypeTraining marks a finite workload. Pods that exit successfully are not recreated.
    // Rolling updates and replica count changes are disabled.
    WorkloadTypeTraining WorkloadType = "Training"
)
```

```yaml
spec:
  workloadType: Training   # default: Inference
  replicas: 1
  template:
    ...
```

### Phase

```go
type PodCliqueSetPhase string

const (
    PodCliqueSetPhasePending   PodCliqueSetPhase = "Pending"
    PodCliqueSetPhaseRunning   PodCliqueSetPhase = "Running"
    PodCliqueSetPhaseSucceeded PodCliqueSetPhase = "Succeeded"
    PodCliqueSetPhaseFailed    PodCliqueSetPhase = "Failed"
)
```

Phase semantics:
- **Pending** — Pods not yet scheduled.
- **Running** — At least one replica's pods are running. Due to grove's gang-scheduling, if any pod is running then all pods in that replica are running.
- **Succeeded** — All replicas completed successfully. (Training only.)
- **Failed** — `maxRestarts` exhausted or `maxRuntime` exceeded. All pods are terminated before the phase is set. (Training only.)

`Pending` and `Running` apply to both workload types. `Succeeded` and `Failed` are only meaningful for `WorkloadTypeTraining`.

### trainingSpec Fields

```go
type TrainingSpec struct {
    // MaxRuntime is the maximum duration the workload is allowed to run.
    // If exceeded, all pods across all PodCliques are terminated and the workload is marked Failed.
    // +optional
    MaxRuntime *metav1.Duration `json:"maxRuntime,omitempty"`

    // MaxRestarts is the total number of times the operator will restart failed replicas
    // across the entire PodCliqueSet before marking it as Failed.
    // A restart recreates all pods across all PodCliques in the affected replica.
    // Default: 0 (no restarts — any failure immediately fails the workload).
    // +optional
    MaxRestarts *int32 `json:"maxRestarts,omitempty"`
}
```

```yaml
spec:
  workloadType: Training
  trainingSpec:
    maxRuntime: 24h
    maxRestarts: 2
```

A "restart" means deleting and recreating all pods across all PodCliques in the affected replica. The counter is a single total across all replicas. The PodCliqueSet is marked `Failed` when the total restart count exceeds `maxRestarts`.

### terminationDelay

`terminationDelay` (default: 4 hours) is the grace period grove gives after `MinAvailableBreached` before tearing down the gang. For inference workloads, this allows time for transient pod failures to recover. For training workloads, a missing worker means the job cannot make progress — a 4-hour wait wastes reserved GPU capacity and delays the retry.

When `workloadType: Training`, `terminationDelay` defaults to `0`. The operator immediately tears down the replica and begins a restart (or marks it `Failed` if `maxRestarts` is exhausted). Users can override this with an explicit value if their framework tolerates brief pod gaps.

### Controller Changes

#### Defaulting Webhook

- Default `terminationDelay` to `0` when `workloadType: Training` (instead of 4 hours).
- Default pod `restartPolicy` to `Never` when `workloadType: Training` (instead of `Always`). For training, the operator controls restarts at the job level; kubelet-level pod restarts would interfere with the operator's failure detection and restart counting.

#### Validating Webhook

- Reject pod template spec changes that would produce a new generation hash (i.e., would trigger a rolling update) for Training workloads.
- `spec.replicas` is immutable at all levels (PodCliqueSet, PodCliqueScalingGroup, PodClique) for Training workloads. The webhook reads `workloadType` from the parent PodCliqueSet via owner reference at validation time.
- Reject any `ScaleConfig` on PodClique or PodCliqueScalingGroup specs when `workloadType: Training`. HPA creation is driven entirely by the presence of `ScaleConfig` in `computeExpectedHPAs` — rejecting it at admission means no HPA will ever be created for training workloads, with no special-casing needed in the controller.

#### PodClique Controller

- Before recreating a pod, check its exit code. If `exitCode == 0`, do not recreate it.
- When all pods in the PodClique have exited with code 0, set a `Succeeded` condition on `PodClique.Status.Conditions`. This must be written **before** any pod cleanup, so the state is durable across operator restarts.

#### PodCliqueSet Controller

The PCS controller is responsible for lifecycle orchestration in training mode:

- **Phase computation** — on every reconcile, aggregate PodClique statuses to derive the phase: all pods pending → `Pending`; any replica running → `Running`; all PodCliques have `Succeeded` condition → `Succeeded`; restart budget exhausted or `maxRuntime` exceeded → `Failed`.
- **maxRuntime enforcement** — record `startTime` in PCS status when phase first transitions to `Running`. `startTime` is never reset on restarts — `maxRuntime` is a wall-clock deadline from first start, inclusive of any time spent failing and restarting. This matches the behavior of Kubernetes Job's `activeDeadlineSeconds` and Kubeflow's `runPolicy.activeDeadlineSeconds`, and bounds total GPU consumption regardless of retry count. On each reconcile, check elapsed time; if elapsed > `maxRuntime`, terminate all pods and set phase `Failed`. Use `requeueAfter(remainingDuration)` to avoid busy-polling.
- **Restart orchestration** — when a PodClique has `MinAvailableBreached: True`, check `status.restartCount` vs `maxRestarts`. If budget remains, delete all pods across all PodCliques in the replica, increment `restartCount`, and emit a `ReplicaRestarting` event. If exhausted, terminate all remaining pods, set phase `Failed`, and emit `MaxRestartsExceeded`.
- **No HPA** — HPA creation requires `ScaleConfig` to be present on a PodClique or PodCliqueScalingGroup. Since the validating webhook rejects `ScaleConfig` for Training workloads, `computeExpectedHPAs` will always return an empty list — no controller-level special-casing needed.
- **Disable rolling updates** — skip generation hash / rolling update progression logic when `workloadType: Training`.

#### PodCliqueScalingGroup Controller

- Skip rolling update processing when `workloadType: Training`.

#### New Status Fields

```go
type PodCliqueSetStatus struct {
    // existing fields...
    Phase        PodCliqueSetPhase  // Pending / Running / Succeeded / Failed
    RestartCount int32              // total restarts consumed across all replicas
    StartTime    *metav1.Time       // when phase first became Running; used for maxRuntime tracking
}
```

### State Persistence

Grove can be restarted at any time; no in-memory state can be relied upon. The following persistence chain ensures the workload lifecycle is always recoverable from the Kubernetes API:

- **Pod level** — `pod.Status.Phase` and `pod.Status.ContainerStatuses[].State.Terminated.ExitCode` are stored in etcd by Kubernetes. On any reconcile, the controller can re-derive which pods have succeeded by listing owned pods.
- **PodClique level** — the PodClique controller writes a `Succeeded` condition to `PodClique.Status.Conditions` (etcd-backed) when all its pods exit with code 0. This must happen before any pod deletion so the state is not lost if pods are cleaned up.
- **PodCliqueSet level** — the PCS controller aggregates PodClique conditions into `PodCliqueSet.Status.Phase` (etcd-backed). `RestartCount` and `StartTime` are also stored in PCS status, making the full restart budget and runtime calculation recoverable after a restart.

The full chain is: **pod status (etcd) → PodClique `Succeeded` condition (etcd) → PodCliqueSet phase + restartCount + startTime (etcd)**.

### Monitoring

**Condition:**

A single `Failed` condition is maintained on `PodCliqueSetStatus.Conditions`, adding detail the phase field cannot express:

| Type | Status | Reason | Set when |
|---|---|---|---|
| `Failed` | `True` | `MaxRestartsExceeded` | Restart budget exhausted |
| `Failed` | `True` | `MaxRuntimeExceeded` | Runtime limit elapsed |

The `Reason` field disambiguates the cause of failure, and `LastTransitionTime` records when the workload failed — neither of which the phase field carries.

**Events:**

| Reason | Type | When |
|---|---|---|
| `PodCliqueFailed` | `Warning` | A PodClique's `MinAvailable` is breached in a training workload |
| `ReplicaRestarting` | `Normal` | A replica restart is triggered; message includes current restart count |
| `MaxRestartsExceeded` | `Warning` | Restart budget exhausted; workload transitioning to `Failed` |
| `MaxRuntimeExceeded` | `Warning` | `maxRuntime` elapsed; all pods being terminated |
| `WorkloadSucceeded` | `Normal` | All replicas completed successfully |

### Test Plan

E2e tests should cover:
- Successful completion: all pods exit 0 → phase `Succeeded`.
- Single pod failure with `maxRestarts: 0` → immediate `Failed`.
- Restart and eventual success within `maxRestarts` budget.
- `maxRuntime` exceeded → all pods terminated, phase `Failed`, event `MaxRuntimeExceeded`.
- Webhook rejection of rolling updates and replica count changes for training workloads.

### Graduation Criteria

**Alpha:** MVP feature set implemented and covered by e2e tests — `workloadType`, phase, `trainingSpec`, `terminationDelay` defaulting, webhook enforcement.

**GA:** Stable after community feedback and at least one release of alpha usage.

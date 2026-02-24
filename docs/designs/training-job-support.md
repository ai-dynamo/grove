# Training Job Support for PodCliqueSet

**Issue:** [#285](https://github.com/ai-dynamo/grove/issues/285)
**Status:** Draft

---

## Background

Grove currently manages long-running inference workloads via `PodCliqueSet`. The `PodClique` controller assumes pods run indefinitely — pods that exit (even successfully) trigger recreation. This model is incompatible with training jobs, which are finite workloads that complete normally.

The community wants to use grove's unique scheduling and gang-scheduling capabilities (topology constraints, clique startup ordering, MNNVL) for training workloads.

---

## Goals

- Introduce a `WorkloadType` field to distinguish `Inference` (default) from `Training` workloads.
- Add a top-level `phase` to `PodCliqueSetStatus` representing lifecycle state.
- Add `maxRuntime` and `maxRestarts` controls for training workloads.
- Disable rolling updates and autoscaling for training workloads.

## Naming and Conventions

- **Phase names:** `Pending`, `Running`, `Succeeded`, `Failed`. `Succeeded` is preferred over `Completed` as it is the dominant convention in the Kubernetes ML ecosystem (Kubeflow, Kueue).
- **`maxRuntime`:** `metav1.Duration` field — grove already uses this type for `terminationDelay`, keeping the API consistent.
- **`maxRestarts`:** `int32`, counted as a **total across all replicas** — simpler to reason about and consistent with Kubeflow's `backoffLimit`.

---

## API Changes

### 1. WorkloadType

Add a `WorkloadType` field to `PodCliqueSetSpec`:

```go
type WorkloadType string

const (
    // WorkloadTypeInference is the default. Pods run indefinitely; failed pods are recreated.
    WorkloadTypeInference WorkloadType = "Inference"

    // WorkloadTypeTraining marks a finite workload. Pods that exit successfully are not recreated.
    // Rolling updates and autoscaling are disabled.
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

### 2. PodCliqueSetStatus — Phase

Add a `phase` field to `PodCliqueSetStatus`:

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
- **Running** — At least one replica's pods are running. Due to grove's gang-scheduling, if any pod is running then all pods in that replica are running (not pending).
- **Succeeded** — All replicas completed successfully. (Training only.)
- **Failed** — `maxRestarts` exhausted or `maxRuntime` exceeded. `Failed` is always a clean terminal state: all pods are terminated before the phase is set, ensuring no pods remain running and holding GPU capacity. (Training only.)

`Pending` and `Running` apply to both `WorkloadTypeInference` and `WorkloadTypeTraining`. `Succeeded` and `Failed` are only meaningful for `WorkloadTypeTraining`.

### 3. Training Controls in Spec

Add a `trainingSpec` stanza to `PodCliqueSetSpec`, only meaningful when `workloadType: Training`:

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

A "restart" means deleting and recreating all pods across all PodCliques in the affected replica. The counter is a single total across all replicas — a failure in any replica consumes from the shared budget. The PodCliqueSet is marked `Failed` when the total restart count exceeds `maxRestarts`.

### 4. terminationDelay for Training Workloads

`terminationDelay` (default: 4 hours) is the grace period grove gives a gang after `MinAvailableBreached` before tearing it down. This exists for inference workloads where a pod crash may be transient and the scheduler should wait before disrupting the whole gang.

For training workloads this default is harmful: a distributed training job cannot make progress with a missing worker, so a 4-hour wait before restarting wastes reserved GPU capacity and delays the retry.

When `workloadType: Training`, the `terminationDelay` defaults to `0` — the operator immediately tears down the replica and begins a restart (or marks it `Failed` if `maxRestarts` is exhausted). Users can still set an explicit non-zero value if their framework tolerates brief pod gaps.

### 5. Disabled Features for Training Workloads

The webhook rejects the following when `workloadType: Training`:

- **Rolling updates** — Spec changes that would trigger a rolling update are rejected.
- **Replica count changes** — `spec.replicas` is immutable at all levels (PodCliqueSet, PodCliqueScalingGroup, PodClique). The validation webhook rejects any update that modifies `replicas` after creation.

### 6. Conditions

The following `metav1.Condition` entries are maintained on `PodCliqueSetStatus.Conditions`:

A single `Failed` condition is maintained, adding detail the phase field cannot express:

| Type | Status | Reason | Set when |
|---|---|---|---|
| `Failed` | `True` | `MaxRestartsExceeded` | Restart budget exhausted (Training only) |
| `Failed` | `True` | `MaxRuntimeExceeded` | Runtime limit elapsed (Training only) |

The `Reason` field disambiguates the cause of failure, and `LastTransitionTime` records when the workload failed — neither of which the phase field carries.

### 7. Events

The operator emits the following Kubernetes Events on the `PodCliqueSet`:

| Reason | Type | When |
|---|---|---|
| `PodCliqueFailed` | `Warning` | A PodClique's `MinAvailable` is breached in a training workload |
| `ReplicaRestarting` | `Normal` | A replica restart is triggered; message includes current restart count |
| `MaxRestartsExceeded` | `Warning` | Restart budget exhausted; workload transitioning to `Failed` |
| `MaxRuntimeExceeded` | `Warning` | `maxRuntime` elapsed; all pods being terminated |
| `WorkloadSucceeded` | `Normal` | All replicas completed successfully |

---

## MVP Scope

1. **`workloadType` field** — `Inference` (default) | `Training`.
2. **`PodCliqueSetPhase`** in status — `Pending` / `Running` / `Succeeded` / `Failed`.
3. **PodClique controller** — when `workloadType: Training`, pods that exit with code 0 are not recreated; all pods succeeded → replica is `Succeeded`.
4. **`maxRestarts`** — operator restarts failed replicas up to `maxRestarts` times; on exhaustion → phase `Failed`.
5. **`maxRuntime`** — if elapsed time since first pod running exceeds `maxRuntime` → terminate pods, phase `Failed`.
6. **`terminationDelay` defaults to `0`** — training workloads cannot make progress with a missing worker; immediate teardown avoids wasting GPU capacity during the grace period.
7. **Disable rolling updates** — webhook rejects rolling-update-triggering spec changes for training workloads.
8. **`replicas` immutable** — webhook rejects updates to `spec.replicas` for training workloads.


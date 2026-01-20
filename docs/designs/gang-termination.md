# Gang Termination Design Doc

# Overview

Gang termination is a mechanism in Grove that ensures when a component becomes unhealthy (falls below minimum availability threshold), the entire group (gang) is terminated rather than leaving it in a degraded state. This is particularly important for distributed AI inference workloads where partial availability often means the workload cannot function properly.

## Abbreviations

| Abbreviation | Full Name | Description |
|--------------|-----------|-------------|
| PCS | PodCliqueSet | Grove CRD that manages a set of PodCliques and PodCliqueScalingGroups |
| PCLQ | PodClique | Grove CRD representing a group of related pods |
| PCSG | PodCliqueScalingGroup | Grove CRD that manages scaling of PodCliques |

## Motivation

Distributed AI workloads often require all components to be available for the workload to function correctly. When some pods in a distributed inference or training job fail, the remaining pods may:

- Continue consuming expensive GPU resources without producing useful work
- Block other workloads from being scheduled
- Leave the system in an undefined state

Gang termination addresses this by providing an automated mechanism to clean up degraded workloads, freeing resources for healthy workloads to be scheduled.

## Background

### Gang Scheduling in Grove

Grove implements gang scheduling through the `MinAvailable` field, which ensures that a minimum number of units can be scheduled together before any are created. At the PodClique level, this means a minimum number of pods; at the PodCliqueScalingGroup level, this means a minimum number of PCSG replicas. This prevents partial scheduling where some units run while others are stuck pending.

### The Problem with Partial Availability

After a workload is running, individual pods may fail due to:
- Node failures
- OOM kills
- Application crashes
- Network partitions

Without gang termination, a workload that loses critical pods remains in a degraded state indefinitely. The `MinAvailable` threshold used for gang scheduling is reused to define "acceptable availability" for gang termination.

# Goals

- **Automatic Cleanup:** Automatically terminate workloads that fall below minimum availability threshold
- **Configurable Grace Period:** Allow time for Kubernetes scheduler to recover before terminating
- **Startup Protection:** Avoid terminating workloads that haven't yet reached full availability
- **Rolling Update Safety:** Prevent false terminations during expected pod churn
- **Multi-Level Granularity:** Support gang termination at PCLQ, PCSG, and PCS levels
- **Opt-In Behavior:** Gang termination is disabled by default and must be explicitly enabled

## Scope and Limitations

**Limitations:**

- **Ready Pods Only (PCLQ level):** Only ready pods count toward availability—starting pods are not considered
- **Sticky WasAvailable:** Once a PodClique has reached MinAvailable, the WasAvailable flag never reverts, even if the workload is later degraded and recovers
- **No Partial Recovery:** When gang termination triggers, the affected replica (PCS or PCSG) is deleted in its entirety, healthy PodCliqueswithin that replica are not preserved.
- **Single TerminationDelay per PCS:** While PCSGs can override the delay, standalone PCLQs within a PCS all use the PCS-level delay

# Design Details

## Key Concepts

### MinAvailable

`MinAvailable` is a field that exists at multiple levels in the Grove hierarchy, serving two purposes at each level:

1. **Gang Scheduling**: Defines the minimum number of units that must be schedulable together
2. **Gang Termination**: Defines the threshold below which availability is considered breached

**At each level:**

| Level | Resource | MinAvailable Meaning |
|-------|----------|---------------------|
| PCLQ | PodClique | Minimum number of **pods** that must be available |
| PCSG | PodCliqueScalingGroup / PodCliqueScalingGroupConfig | Minimum number of **PCSG replicas** that must be available |

**PCSG replica availability:** A PCSG replica is considered unavailable (breached) if **any** of its constituent PCLQs has `MinAvailableBreached=True`. The PCSG aggregates the breach status from all PCLQs within each replica to determine overall PCSG replica availability.

### MinAvailableBreached Condition

A status condition (`ConditionTypeMinAvailableBreached`) set on PodClique and PodCliqueScalingGroup resources when available replicas fall below `MinAvailable`. The condition includes:

- `Status`: True (breached), False (not breached), or Unknown (update in progress)
- `LastTransitionTime`: When the condition last changed, used for tracking termination delay
- `Reason`: Why the condition is in this state
- `Message`: Human-readable description

### TerminationDelay

A configurable duration specified at `PodCliqueSet.Spec.Template.TerminationDelay`. After `MinAvailable` is breached, this delay must pass before gang termination is triggered. This gives the Kubernetes scheduler time to potentially recover by rescheduling pods.

**Important:** Gang termination is **disabled by default**. When `terminationDelay` is `nil` (not set), no gang termination will occur. To enable gang termination, you must explicitly set `terminationDelay` to a non-zero duration.

### PCSG TerminationDelay Override

Individual `PodCliqueScalingGroupConfig` entries can override the PCS-level `terminationDelay`:

- If PCS-level `terminationDelay` is nil, gang termination is disabled for the entire PCS
- If PCS-level `terminationDelay` is set, each PCSG can optionally override it with its own delay
- PCSG overrides can only be set when PCS-level `terminationDelay` is set

### WasAvailable Gate

The `MinAvailableBreached` condition is only set to `True` after a PodClique has **previously reached** its `MinAvailable` threshold. This is tracked via the `WasAvailable` status field:

- `WasAvailable` starts as `false` for new PodCliques
- It becomes `true` when `readyReplicas >= MinAvailable` (and not during rolling update)
- Once `true`, it never reverts to `false` (sticky bit)
- If `WasAvailable` is `false`, the breach condition will be `False` with reason `"NeverAvailable"`

**Motivation for WasAvailable:**

1. **Ignore Initial Startup:** New PodCliques should not be terminated simply because they haven't yet reached their MinAvailable threshold. The WasAvailable gate ensures gang termination only triggers for workloads that were previously healthy and then became degraded—not for workloads that are still starting up.

2. **Operator Crash Resilience:** The WasAvailable flag is persisted in the PodClique's status (not held in memory). This ensures that if the Grove operator crashes and restarts during the transition to availability, the flag is not lost. Because it's a sticky bit that never reverts to `false`, the operator can safely restart at any time without missing the transition or incorrectly treating a previously-available PodClique as never-available.

## Architecture

Gang termination operates at three levels, with each level potentially triggering termination or delegating to the level above:

```
┌─────────────────────────────────────────────────────────────┐
│                      PodCliqueSet                           │
│  - Monitors all PCSGs and standalone PCLQs per replica      │
│  - Terminates entire PCS replica if any constituent         │
│    breaches MinAvailable past TerminationDelay              │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                 PodCliqueScalingGroup                       │
│  - Monitors constituent PodCliques                          │
│  - Can gang-terminate individual PCSG replicas if:          │
│    * PCSG.Spec.MinAvailable is NOT breached overall         │
│    * Individual replica has PCLQ with breach > delay        │
│  - If PCSG.Spec.MinAvailable IS breached, delegates to PCS  │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                       PodClique                             │
│  - Monitors ready pods only                                 │
│  - Sets MinAvailableBreached condition when:                │
│    readyPods < MinAvailable                                 │
└─────────────────────────────────────────────────────────────┘
```

## Detailed Flow

### 1. PodClique Level Detection

The PodClique controller continuously monitors pod status and updates the `MinAvailableBreached` condition. The evaluation follows this order:

1. **Check availability:** If `readyReplicas >= minAvailable`, the condition is set to `False` with reason `SufficientReadyPods`. No breach is occurring.

2. **Check WasAvailable gate:** If `readyReplicas < minAvailable` but `WasAvailable` is `false`, the condition is set to `False` with reason `NeverAvailable`. The PodClique has never reached its minimum threshold, so we don't consider this a breach.

3. **Check rolling update:** If a rolling update is in progress, the condition is set to `Unknown` with reason `UpdateInProgress`. This prevents false terminations during expected pod churn.

4. **Breach detected:** If none of the above apply (ready pods are below minimum, WasAvailable is true, and no rolling update), the condition is set to `True` with reason `InsufficientReadyPods`.

**WasAvailable tracking:** The `WasAvailable` status field is updated separately. When `readyReplicas >= minAvailable` (and no rolling update is in progress), `WasAvailable` is set to `true`. Once set, it never reverts to `false`.

**Key points:**
- Only **ready** pods count toward availability—starting pods do not count
- Breach is detected immediately when ready pods drop below MinAvailable
- The WasAvailable gate prevents termination of workloads that haven't yet reached full availability
- Rolling updates temporarily suspend breach detection to avoid false positives

### 2. PodCliqueScalingGroup Level Processing

The PCSG controller aggregates breach information from its constituent PodCliques and decides whether to handle termination itself or delegate to the PCS level.

**Identifying breached PCSG replicas:**

The controller groups PodCliques by their PCSG replica index and checks each group for breaches. For each PCSG replica:
- If any PCLQ has `MinAvailableBreached=True` and the termination delay has expired, the replica is marked for termination
- If any PCLQ has `MinAvailableBreached=True` but the delay hasn't expired, the replica is marked for requeue (to check again later)

**Two-level protection at PCSG:**

Before terminating individual PCSG replicas, the controller checks whether the overall PCSG MinAvailable would be breached:

1. **If PCSG-level MinAvailable is breached:** When the number of healthy replicas would fall below the PCSG's MinAvailable threshold, the PCSG controller does not terminate replicas itself. Instead, it signals the breach up to the PCS level, which will handle termination of the entire PCS replica.

2. **If PCSG-level MinAvailable is NOT breached:** The PCSG controller can safely gang-terminate individual PCSG replicas that have breached PCLQs. It deletes all PodCliques belonging to those PCSG replica indices.

### 3. PodCliqueSet Level Termination

The PCS controller scans all constituents within each PCS replica and determines whether the entire replica should be terminated.

**For each PCS replica, the controller checks:**

1. **PCSGs:** Are any PodCliqueScalingGroups signaling that their MinAvailable has been breached (delegated from PCSG level)?
2. **Standalone PCLQs:** Are any PodCliques (not part of a PCSG) breached with their termination delay expired?

**Termination decision:**

If either condition is true with an expired termination delay, the entire PCS replica is scheduled for deletion. This means all PodCliques belonging to that PCS replica index will be deleted, regardless of their individual health status.

If breaches exist but delays haven't expired, the controller requeues reconciliation to check again after the shortest remaining delay.

### 4. Deletion Execution

When gang termination is triggered for a PCS replica, the controller deletes all PodCliques that match:
- The PodCliqueSet name
- The specific PCS replica index

This bulk deletion ensures that all components of the PCS replica (both PCSG-managed and standalone PCLQs) are removed together, maintaining the gang semantics.

## Timing and Wait Calculation

The wait duration is calculated as:

```go
waitFor := terminationDelay - time.Since(condition.LastTransitionTime)
```

- If `waitFor <= 0`: Delay has expired, termination can proceed
- If `waitFor > 0`: Requeue reconciliation to check again after delay

When multiple constituents are breached, the minimum wait time across all is used to determine requeue timing.

## Special Cases

### Gang Termination Disabled

When `PodCliqueSet.Spec.Template.TerminationDelay` is nil:
- Gang termination is completely disabled
- `MinAvailableBreached` conditions are still computed and set on resources
- No automatic deletion of PCS replicas will occur regardless of breach status
- PCSG-level termination delay overrides cannot be set

### Rolling Updates

During rolling updates, `MinAvailableBreached` is set to `Unknown`:
- Prevents false positive terminations during expected pod churn
- Both old and new pods may be transitioning, affecting availability temporarily
- Once rolling update completes, normal monitoring resumes
- `WasAvailable` is not updated during rolling updates

### Never Available (WasAvailable Gate)

If `WasAvailable` is `false`, the condition is set to `False` with reason `NeverAvailable`:
- This is the initial state for new PodCliques
- Prevents premature termination during startup
- Only after pods have successfully reached MinAvailable threshold and then become unavailable is the breach detected
- This replaces the previous "Insufficient Scheduled Pods" logic

### Standalone PodCliques vs PCSG-managed PodCliques

PCS replica termination checks both:
1. **PCSGs**: Any PCSG with `MinAvailableBreached=True` triggers PCS replica check, using the effective termination delay (PCSG override or PCS default)
2. **Standalone PCLQs**: PodCliques not part of any PCSG are checked independently, using PCS-level termination delay

## Configuration

### Enabling Gang Termination

Gang termination is **disabled by default**. To enable it, set `terminationDelay` at the PodCliqueSet template level:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
spec:
  template:
    terminationDelay: 4h  # Enables gang termination with 4 hour delay
    cliques:
      - name: worker
        spec:
          replicas: 4
          minAvailable: 3  # Breach if < 3 pods available
```

### Disabling Gang Termination

To disable gang termination, omit the `terminationDelay` field or set it to null:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
spec:
  template:
    # terminationDelay not set - gang termination disabled
    cliques:
      - name: worker
        spec:
          replicas: 4
          minAvailable: 3
```

### PCSG-Level TerminationDelay Override

When gang termination is enabled at the PCS level, individual PCSGs can override the delay:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
spec:
  template:
    terminationDelay: 4h  # Default for all PCSGs
    cliques:
      - name: leader
        spec:
          replicas: 1
          minAvailable: 1
      - name: worker
        spec:
          replicas: 4
          minAvailable: 3
    podCliqueScalingGroups:
      - name: inference-group
        replicas: 2
        minAvailable: 1
        terminationDelay: 2h  # Override: shorter delay for this PCSG
        cliqueNames: [leader, worker]
      - name: another-group
        replicas: 3
        minAvailable: 2
        # No override: uses PCS-level 4h delay
        cliqueNames: [other-clique]
```

**Note:** PCSG-level `terminationDelay` can only be set if PCS-level `terminationDelay` is set. If you try to set a PCSG override when PCS-level is nil, validation will fail.

### MinAvailable at Different Levels

**PodClique level:**
```yaml
spec:
  replicas: 4
  minAvailable: 3  # At least 3 of 4 pods must be available
```

**PodCliqueScalingGroupConfig level:**
```yaml
spec:
  template:
    podCliqueScalingGroups:
      - name: inference-group
        replicas: 2
        minAvailable: 1  # At least 1 of 2 PCSG replicas must be available
        cliqueNames: [leader, worker]
```

## Source Files

| Component | File |
|-----------|------|
| PodClique status/condition | `operator/internal/controller/podclique/reconcilestatus.go` |
| PCSG gang termination | `operator/internal/controller/podcliquescalinggroup/components/podclique/sync.go` |
| PCS replica termination | `operator/internal/controller/podcliqueset/components/podcliquesetreplica/gangterminate.go` |
| Shared utilities | `operator/internal/controller/common/component/utils/podclique.go` |
| Condition constants | `operator/api/common/constants/constants.go` |

## Debugging Gang Termination Issues

### Check MinAvailableBreached Conditions

```bash
# Check PodClique conditions
kubectl get pclq -o jsonpath='{range .items[*]}{.metadata.name}: {.status.conditions[?(@.type=="MinAvailableBreached")]}{"\n"}{end}'

# Check PCSG conditions  
kubectl get pcsg -o jsonpath='{range .items[*]}{.metadata.name}: {.status.conditions[?(@.type=="MinAvailableBreached")]}{"\n"}{end}'
```

### Key Status Fields

- `PodClique.Status.ReadyReplicas`: Number of ready pods
- `PodClique.Status.ScheduledReplicas`: Number of scheduled pods
- `PodClique.Status.WasAvailable`: Whether the PodClique has ever reached MinAvailable
- `PodCliqueScalingGroup.Status.AvailableReplicas`: Number of available PCSG replicas
- `PodCliqueSet.Status.AvailableReplicas`: Number of available PCS replicas


# GREP-401: Enhance PodClique gang termination

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Recover from pods stuck terminating on failed nodes](#story-1-recover-from-pods-stuck-terminating-on-failed-nodes)
    - [Story 2: Configurable policy for stuck-terminating pods](#story-2-configurable-policy-for-stuck-terminating-pods)
    - [Story 3: Per–PCSG or per–PCLQ overrides](#story-3-perpcsg-or-perpclq-overrides)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [Background: Current gang termination](#background-current-gang-termination)
  - [Problem: Pods stuck in termination](#problem-pods-stuck-in-termination)
  - [Solution options](#solution-options)
    - [Option 1: Force delete pods stuck in termination](#option-1-force-delete-pods-stuck-in-termination)
    - [Option 2: Orphan pods stuck in termination](#option-2-orphan-pods-stuck-in-termination)
  - [API design](#api-design)
  - [Behavior and control flow](#behavior-and-control-flow)
  - [Monitoring](#monitoring)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Grove’s gang termination today deletes and recreates PodCliques (and their pods) when MinAvailable is breached for longer than TerminationDelay. In environments where pods are constrained to a topology (e.g. same rack), node or kubelet failures can leave pods stuck in a terminating state: the API server has set `deletionTimestamp` but the kubelet never completes termination. Those pods are excluded from ready/scheduled counts, so MinAvailable is breached and gang termination runs; however, the stuck pods are still present and can block cleanup and rescheduling. This GREP proposes a configurable enhancement so that pods stuck in termination for longer than a user-configurable duration are either force-deleted (grace period zero) or **orphaned**—i.e. Grove no longer manages them; the pods remain in the cluster for the admin to handle. Grove treats them as gone for availability and reconciliation so the gang can recover.

## Motivation

In topology-constrained workloads (e.g. MNNVL with all pods on the same rack), the following situation occurs:

1. All pods of a PodClique (or PodCliqueSet replica) are required to be on the same rack (e.g. Rack A).
2. During training, a node on Rack A fails; some pods are deleted but the kubelet on the failed node does not respond, so those pods remain in the cluster with `deletionTimestamp` set (stuck terminating).
3. Grove creates a **replacement** pod for the one that is stuck terminating. Because of the gang topology constraint (all pods on the same rack), the replacement pod must be scheduled to **Rack A**—the same rack as the remaining running pods.
4. Rack A has insufficient allocatable resources (e.g. due to the faulty node), so the scheduler cannot place the replacement pod there. The replacement pod stays **Pending**.
5. MinAvailable remains breached (one pod stuck terminating, one Pending). Gang termination will only be triggered after **TerminationDelay** expires. Until then, the gang is stuck: the replacement pod stays Pending and the gang cannot become healthy.
6. Only **after** TerminationDelay does gang termination run (issue delete for all PodCliques and pods for that replica). **If** all pods actually disappear from the API (e.g. no finalizers on the pod, kubelet completes deletion), the PodClique finalizer is removed and the PCLQs are gone; the controller then creates new PCLQs and new pods, which can be placed on a different rack (e.g. Rack B). **But** if a pod is stuck in the API (e.g. the pod has finalizers or the kubelet never completes termination), the PodClique **cannot** be fully deleted (its finalizer is only removed when no managed pods remain), so the PCLQs stay in a “deleting” state and **new PCLQs are never created**—the gang never recovers. The issue is thus both the long wait until TerminationDelay and, when pods are stuck, the **blocking of PCLQ deletion** by the finalizer.

When **no** pod is stuck, gang termination can eventually resolve the situation (after TerminationDelay): PCLQs and pods are deleted, then new PCLQs and pods are created and can be placed on another rack (e.g. Rack B). But when **a pod is stuck** in the API (e.g. stuck terminating, or the pod has finalizers), the PodClique **cannot** be fully deleted (its finalizer is only removed when no managed pods remain), so gang termination **does not** resolve the situation—the PCLQs stay in a “deleting” state and new PCLQs are never created; the gang never recovers. There is also a **long wait** until TerminationDelay even when pods are not stuck. To recover, we must special-case pods stuck in termination: either **force delete** them (remove from the API so the PCLQ finalizer can be removed and the PCLQ deleted, and replacement pods can be created), or **orphan** them—e.g. treat them as no longer “existing” for cleanup verification so the PCLQ finalizer can be removed and the PCLQ deleted; Grove stops managing the orphaned pods and they remain in the cluster for the admin to handle.

### Goals

- **Handle pods stuck in termination**: Introduce a mechanism so that pods that have been in a terminating state (have `deletionTimestamp` set) for longer than a configurable duration are handled explicitly.
- **Configurable policy**: Support at least two policies: (1) **Force delete** — issue delete with grace period zero (and optionally clear blocking finalizers) so the pod is removed from the API server; (2) **Orphan** — Grove stops managing such pods (they no longer belong to Grove); they remain in the cluster for the admin to handle. Grove treats them as gone for MinAvailable, replica counts, and reconciliation so replacement pods can be created (e.g. manual cleanup, node drain, or external tooling for the orphaned pods).
- **User-configurable timeout**: The duration after which a terminating pod is considered “stuck” is configurable (at PCS, PCSG, or PCLQ level, or in operator config), so users can tune it for their environment (node failure detection time, grace period, etc.).
- **Multi-level configuration**: Allow defining (and overriding) stuck-termination config at **PodCliqueSet** (default), **PodCliqueScalingGroup** (per scaling group), and **PodClique** template (per clique role), with a clear resolution order so each PodClique has a single effective config.
- **Consistency with existing behavior**: When the feature is disabled or the timeout is not exceeded, behavior remains as today (no force delete, no orphan semantics).

### Non-Goals

- **Changing MinAvailable or TerminationDelay semantics**: The definition of MinAvailable and the existing TerminationDelay for gang termination are unchanged; we only add handling for pods that are stuck terminating beyond a separate, configurable threshold.

## Proposal

Enhance PodClique (and, where applicable, PodCliqueSet replica / PodCliqueScalingGroup) reconciliation so that:

1. **Stuck-terminating detection**: Pods that have `deletionTimestamp` set and have been in that state for longer than a configurable **stuck termination timeout** are classified as “stuck terminating.”
2. **Policy application**:
   - **Force delete**: For each stuck-terminating pod, the controller performs a delete with grace period zero (and optionally removes pod-level finalizers that block deletion), so the pod is removed from the API server and replacement pods can be created.
   - **Orphan**: Grove **stops managing** the stuck-terminating pods—they no longer belong to Grove. They are excluded from the set of “existing pods” used for computing status and reconciliation, so the controller creates replacement pods and the gang can recover. The orphaned pods remain in the cluster; Grove does not delete them. The **admin** is responsible for handling them (e.g. manual delete, node drain, or external cleanup).

Exactly one of the two policies can be selected per PodCliqueSet (or globally). The stuck termination timeout is configurable (e.g. default 10–15 minutes) so that normal graceful shutdown is not affected.

### User Stories

#### Story 1: Recover from pods stuck terminating on failed nodes

As a user running a topology-constrained workload (e.g. all pods on the same rack), when a node fails and some pods remain stuck in termination because the kubelet does not respond, I want Grove to either remove those pods (force delete) or orphan them (leave them for admin to handle) after a configurable time, so that replacement pods can be created and scheduled and the gang can recover instead of remaining stuck.

#### Story 2: Configurable policy for stuck-terminating pods

As a cluster admin, I want to configure a timeout (how long a pod can be terminating before it is considered stuck) and a policy (force delete vs orphan) so that I can align behavior with our node failure detection and safety requirements. With **orphan**, I accept that stuck pods remain in the cluster and I will handle them myself (e.g. via runbooks or automation).

#### Story 3: Per–PCSG or per–PCLQ overrides

As a user with a PodCliqueSet that has multiple scaling groups or clique roles, I want to set a default stuck-termination behavior at the PCS level and override it for specific PodCliqueScalingGroups or clique roles (e.g. ForceDelete for one PCSG, Orphan for another; or a shorter timeout for a particular clique), so that different parts of the workload can have different recovery behavior.

### Limitations/Risks & Mitigations

| Risk / Limitation | Mitigation |
|-------------------|------------|
| Force delete can abort in-flight work | Make the stuck-termination timeout long enough (e.g. ≥ typical graceful shutdown). Document that force delete is for stuck pods only; normal termination is unchanged. |
| Orphan: pods no longer managed by Grove, left for admin | Document that with **Orphan**, Grove stops managing those pods (they no longer belong to Grove); they remain in the cluster and the admin is responsible for cleaning them up. Emit events/conditions so operators can see which pods were orphaned. |
| Misconfiguration (too short timeout) causes premature force delete | Validation or defaults that discourage timeouts shorter than a minimum (e.g. 5 minutes). Clear documentation. |

## Design Details

### Background: Current gang termination

- **MinAvailableBreached**: For each PodClique, status is updated from the set of pods that belong to it. Terminating pods (with `deletionTimestamp` set) are categorized separately and are not counted as ready; they are also excluded from the “replicas” count. So when a pod is stuck terminating, MinAvailable can be breached because that pod is no longer counted as available.
- **Gang termination**: If MinAvailable remains breached for longer than **TerminationDelay** (on PodCliqueSet template), the PodCliqueSet controller triggers gang termination for the affected **replica**: it issues delete for **all PodCliques (PCLQs)** for that replica index. Each PodClique has a **finalizer** (`grove.io/podclique.grove.io`). The PodClique controller’s delete flow: (1) delete managed resources (pods), (2) **verify no resources await cleanup**—each operator (e.g. pod) reports existing resource names; if any pod belonging to that PCLQ still exists in the API, this step fails and the controller requeues without removing the finalizer, (3) remove the PCLQ finalizer only when no managed resources remain. So **if a pod is stuck in the API** (e.g. stuck terminating, or has finalizers so it never disappears), **the PCLQ cannot be fully deleted**: the PCLQ remains with `deletionTimestamp` set and the finalizer still present, because `VerifyNoResourceAwaitsCleanup` never passes.
- **After gang termination (when no pod is stuck)**: Only when all pods are gone from the API does the PCLQ finalizer get removed and the PCLQ disappear. Then the PodCliqueSet controller sees that the replica has no PCLQs and **creates new PCLQs** (and PodGangs, etc.) and new pods, which can be scheduled elsewhere (e.g. Rack B). **When a pod is stuck** (e.g. finalizer on the pod or kubelet not responding), the PCLQ never leaves the API, so the replica still “has” PCLQs (stuck in deletion) and **new PCLQs are not created**—the gang cannot recover. This is why this GREP proposes force delete (remove the pod from the API, e.g. clear pod finalizers) or orphan (e.g. treat stuck-terminating pods as no longer “existing” for the purpose of cleanup verification so the PCLQ finalizer can be removed and the PCLQ can be deleted; the stuck pod then remains as an orphan for the admin to handle).

### Problem: Pods stuck in termination

**Note on Kubernetes pod state**: The Kubernetes Pod API does not define a dedicated “Terminating” value for `status.phase` (the phases are Pending, Running, Succeeded, Failed, Unknown). A pod is conventionally said to be in a **terminating** state when it has been marked for deletion: `metadata.deletionTimestamp` is set. The pod remains in the API (often still with `phase: Running`) until the kubelet completes deletion. Tools like `kubectl get pods` may show “Terminating” in the status column by deriving it from `deletionTimestamp`. In this GREP, “terminating” and “stuck in termination” refer to pods that have `deletionTimestamp` set and have not yet been removed from the API server.

A pod is **stuck in termination** if:

- It has `deletionTimestamp` set, and
- The time since `deletionTimestamp` is greater than the configured **stuck termination timeout**.

Such pods are still present in the API server but are not making progress toward termination (e.g. because the kubelet on the node is down or not responding).

### Solution options

#### Option 1: Force delete pods stuck in termination

- **Behavior**: When reconciling a PodClique (or the pod set for a replica), the controller identifies pods that are stuck terminating. For each such pod, it performs a **delete with `GracePeriodSeconds=0`**. If the pod has finalizers that block deletion, the controller may optionally clear them.
- **Effect**: The API server removes the pod object. The controller can then create a replacement pod; the scheduler can place it without being blocked by the old pod’s presence on the node.
- **Use case**: Operators who want stuck pods removed from the cluster so that replacement pods can be created and scheduled cleanly.

#### Option 2: Orphan pods stuck in termination

- **Behavior**: Grove **stops managing** the stuck-terminating pods—they **no longer belong to Grove**. When computing status and desired pod count, the controller excludes these pods from the “existing pods” set and creates replacement pods so the gang can recover. Grove does not delete the orphaned pods; they remain in the cluster.
- **Effect**: The gang recovers (replacement pods are created and scheduled). The orphaned pods stay in the cluster with `deletionTimestamp` set; Grove will not manage them again. The **admin** is responsible for handling them (e.g. manual `kubectl delete`, node drain, or external cleanup scripts).
- **Use case**: Operators who prefer not to force-delete (e.g. due to finalizers or policy) and who accept that once orphaned, those pods are no longer under Grove’s management.

Only one policy (force delete or orphan) is applied; the configuration specifies which.

### API design

**Placement and hierarchy**: The stuck-termination timeout and policy can be defined at multiple levels so that admins can set a default for the whole workload and optionally override per scaling group or per clique role:

- **PodCliqueSet (PCS) level**: Add optional `StuckTermination` to the PodCliqueSet **template** (`PodCliqueSetTemplateSpec`, e.g. alongside `TerminationDelay`). This is the default for the entire workload.
- **PodCliqueScalingGroup (PCSG) level**: Add optional `StuckTermination` to **PodCliqueScalingGroupConfig** (each entry in `Template.PodCliqueScalingGroupConfigs`). Each scaling group can override the PCS default (e.g. one PCSG uses ForceDelete, another uses Orphan).
- **PodClique (PCLQ) level**: Add optional `StuckTermination` to **PodCliqueTemplateSpec** (each entry in `Template.Cliques`). Each clique role can override the PCS default (and, when the clique is part of a PCSG, the PCSG config can still override the PCS default for that group; the PCLQ template override applies per clique).
- **OperatorConfiguration (optional)**: Add optional defaults under operator config so that all PodCliqueSets get a default when no PCS/PCSG/PCLQ config is set.

**Proposed shape**:

```go
// StuckTerminationConfig configures how pods stuck in termination are handled.
// +optional
StuckTermination *StuckTerminationConfig `json:"stuckTermination,omitempty"`

// StuckTerminationConfig holds timeout and policy for pods stuck in termination.
type StuckTerminationConfig struct {
    // Timeout is the duration a pod may remain in terminating state (deletionTimestamp set)
    // before it is considered stuck. After this duration, the configured Policy is applied.
    // Defaults to a value that allows normal graceful shutdown (e.g. 10m).
    // +optional
    Timeout *metav1.Duration `json:"timeout,omitempty"`

    // Policy is the action to take for pods stuck in termination.
    // - ForceDelete: delete the pod with GracePeriodSeconds=0 so it is removed from the API server.
    // - Orphan: Grove stops managing the pod (it no longer belongs to Grove). The pod remains in
    //   the cluster for the admin to handle (e.g. manual cleanup, node drain).
    // +kubebuilder:validation:Enum=ForceDelete;Orphan
    // +optional
    Policy StuckTerminationPolicy `json:"policy,omitempty"`
}

type StuckTerminationPolicy string

const (
    StuckTerminationPolicyForceDelete StuckTerminationPolicy = "ForceDelete"
    StuckTerminationPolicyOrphan      StuckTerminationPolicy = "Orphan"
)
```

- If `StuckTermination` is nil or `Timeout` is zero/negative, the feature is effectively disabled (no force delete, no orphan).
- Validation: if `StuckTermination` is set, `Policy` must be one of the allowed values; `Timeout` should have a reasonable minimum (e.g. 1m) to avoid accidental force delete during normal shutdown.

**Where to add** (same `StuckTerminationConfig` type reused at each level):

| Level | API type / field | Scope |
|-------|------------------|--------|
| PCS   | `PodCliqueSetTemplateSpec.StuckTermination` | Default for the whole PodCliqueSet |
| PCSG  | `PodCliqueScalingGroupConfig.StuckTermination` | Override for all cliques in that scaling group |
| PCLQ  | `PodCliqueTemplateSpec.StuckTermination` | Override for that clique role (each item in `Template.Cliques`) |
| Operator | `OperatorConfiguration` (optional) | Default when no PCS/PCSG/PCLQ config is set |

**Resolution order** (effective config for a given PodClique when reconciling):

1. **PodCliqueTemplateSpec** (the clique role’s template) — if `StuckTermination` is set, use it.
2. Else, if the PodClique belongs to a **PodCliqueScalingGroup**, use **PodCliqueScalingGroupConfig.StuckTermination** for that PCSG if set.
3. Else use **PodCliqueSetTemplateSpec.StuckTermination** (PCS template).
4. Else use OperatorConfiguration default if present; otherwise the feature is disabled for that clique.

This allows, for example: PCS default Orphan 10m; one PCSG overrides to ForceDelete 15m; one clique role overrides to Orphan 5m — so each PCLQ gets a single effective config from the hierarchy.

### Behavior and control flow

1. **PodClique controller (pod reconciliation)**:
   - When listing or considering pods for a PodClique, classify each pod that has `deletionTimestamp` set and `time.Since(pod.DeletionTimestamp.Time) > stuckTerminationTimeout` as **stuck terminating**.
   - **ForceDelete**: For each stuck-terminating pod, call delete with `GracePeriodSeconds=0` (and optionally patch to remove blocking finalizers). Do not create a replacement until the pod is gone (or treat as gone after a brief backoff).
   - **Orphan**: When computing status and desired pod count, exclude stuck-terminating pods from the “existing pods” set. When syncing pods, create replacements as if those pods do not exist. Grove does not delete the stuck pods; they remain as orphans for the admin to handle.

2. **Status computation (PodClique)**:
   - For **Orphan** policy: in `reconcileStatus` (and any shared helpers that categorize pods), exclude stuck-terminating pods before computing Replicas, ReadyReplicas, ScheduledReplicas, and MinAvailableBreached. Then stuck pods do not contribute to counts and do not delay MinAvailableBreached.

3. **Gang termination (PodCliqueSet replica / PCSG)**:
   - No change to when gang termination is triggered (still based on MinAvailableBreached and TerminationDelay). With **Orphan**, MinAvailableBreached may clear earlier because stuck-terminating pods are not counted, so the replica may recover without gang termination. With **ForceDelete**, stuck pods are removed so that after gang termination, replacement pods can be created and scheduled.

4. **Defaults**: If the feature is enabled via OperatorConfiguration default only, PodCliqueSet template may override with its own `stuckTermination` or leave it unset to use the default.

### Monitoring

- **Events**: Emit a warning (or normal) event on the PodClique (or PodCliqueSet) when a pod is force-deleted or first orphaned due to stuck termination (e.g. “Pod xyz stuck terminating for > timeout; force deleted” or “Pod xyz stuck terminating; orphaned for admin to handle”).
- **Conditions (optional)**: A condition on PodClique (e.g. `StuckTerminatingPods`) with reason/message listing which pods are currently stuck or orphaned, so operators can see the state without scanning events.
- **Metrics (optional)**: Counter or gauge for “pods force-deleted due to stuck termination” and “pods orphaned due to stuck termination” per PodCliqueSet or namespace.

### Test Plan

- **Unit tests**:
  - Classification: pods with `deletionTimestamp` older than timeout are classified as stuck; within timeout they are not.
  - ForceDelete: when policy is ForceDelete, controller issues delete with grace period 0 for stuck pods; replacement pod is created after pod is removed.
  - Orphan: when policy is Orphan, stuck-terminating pods are excluded from status counts and from “existing pods” in sync; replacement pods are created; stuck pods remain in the cluster as orphans for admin to handle.
  - Default/disabled: when StuckTermination is nil or timeout is 0, no force delete and no exclude logic; behavior matches current code.
- **E2E (optional)**: Simulate a pod stuck terminating (e.g. mock or node cordon/drain scenario where kubelet stops responding) and assert that with ForceDelete the pod is removed and replacement runs, or with Orphan the gang recovers (replacement pod created and scheduled) and the stuck pod remains as an orphan.

### Graduation Criteria

- **Alpha**: API (StuckTerminationConfig) added and wired; ForceDelete and Orphan implemented in PodClique controller; unit tests; feature gated or behind a default-off flag if desired.
- **Beta**: Defaults and validation stable; documentation and events; E2E coverage where feasible; at least one real-environment validation (e.g. MNNVL rack scenario).
- **GA**: No breaking changes to default behavior; production use; clear migration path if defaults change.

## Implementation History

- **2026-03-02**: Initial GREP created, tracking issue: [ai-dynamo/grove#401](https://github.com/ai-dynamo/grove/issues/401).

## Alternatives

- **Only force delete**: Simpler API but does not satisfy operators who do not want to force-delete (e.g. due to finalizers or policy). Supporting both ForceDelete and Orphan gives flexibility: admins who prefer to handle stuck pods themselves can use Orphan.
- **Node-level eviction**: Relying on node failure detection and eviction is out of scope and may be slower; this GREP focuses on pod-level “stuck terminating” only.
- **Global only (OperatorConfiguration)**: Per-PodCliqueSet configuration allows different workloads to use different timeouts and policies (e.g. short timeout for batch, longer for stateful) and is more flexible than a single global default.

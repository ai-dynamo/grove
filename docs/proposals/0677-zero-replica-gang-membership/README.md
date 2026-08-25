# GREP-0677: Zero-Replica Gang Membership

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Zero Replicas Per Level](#zero-replicas-per-level)
  - [Below-Quorum Behavior](#below-quorum-behavior)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [Autoscaler Integration](#autoscaler-integration)
  - [Example](#example)
  - [Monitoring](#monitoring)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Open Questions](#open-questions)
- [Remaining Work](#remaining-work)
- [Alternatives](#alternatives)
- [Historical Discussion](#historical-discussion)
- [Appendix](#appendix)
  - [Upstream Follow-ups](#upstream-follow-ups)
<!-- /toc -->

## Summary

Grove should treat `PodClique` or `PodCliqueScalingGroup` components with `replicas: 0` as an intentional idle state. This GREP keeps `minAvailable` non-zero and immutable, makes gang logic tolerant of zero-replica members, and uses a fixed, direction-agnostic `Clamp` rule that persists any positive scaling update below quorum as `minAvailable`.

## Motivation

Scale-to-zero serving commonly keeps a router running while workers scale to zero. In that state, worker pods are intentionally absent, not unhealthy.

Using `minAvailable: 0` would overload a field that already affects gang membership, termination, rolling updates, and startup ordering. `replicas` is the mutable scale target, so `replicas: 0` is the cleaner signal.

External replica writers should target either `0` or a value at least `minAvailable`; Grove clamps other positive below-quorum updates to `minAvailable` before persisting them.

### Goals

- Treat `replicas: 0` as an intentional idle state without changing `minAvailable`.
- Keep `spec.replicas` valid: it is either `0` or greater than or equal to `minAvailable`.
- Clamp any positive scaling update below `minAvailable` to `minAvailable`, for both wake-up and active scale-in.
- Persist the clamped value in `spec.replicas` as the desired replica count.

### Non-Goals

- Change `minAvailable` semantics, including allowing `minAvailable: 0` or making it mutable.

## Proposal

When a `PodClique` or `PodCliqueScalingGroup` has `replicas: 0`, Grove should not require it as a gang member. When replicas become positive again, its existing `minAvailable` should apply normally.

An idle component should leave the `PodGang` rather than stay in it with a zero threshold: no `PodGroup` in `PodGang.spec.podGroups`, and no scaled `PodGang` for an idle `PodCliqueScalingGroup`. Shrinking a gang must not disturb the members still running.

### Zero Replicas Per Level

`replicas: 0` means something different at each level:

| Level | `replicas: 0` today | Proposed |
| --- | --- | --- |
| `PodCliqueSet` | Already allowed. Nothing is created; no `minAvailable` at this level. | Unchanged. |
| `PodCliqueScalingGroup` | Rejected in the `PodCliqueSet` template; accepted on the object, which has no webhook. | Idle. No `PodGroup` in the base `PodGang` and no scaled `PodGang`. |
| `PodClique` | `replicas: 0` is accepted on the object, which has no webhook, but is silently defaulted to `1` in the `PodCliqueSet` template. | Idle when standalone, contributing no `PodGroup`. Inside a scaling group, idle is expressed at the group level. |

### Below-Quorum Behavior

For positive scaling updates below `minAvailable`, Grove uses a fixed, direction-agnostic `Clamp` rule:

| Requested replicas | Behavior |
| --- | --- |
| `0` | Intentional idle state. The component contributes no required gang members and does not breach `minAvailable`. |
| `0 < replicas < minAvailable` | Accepted and persisted as `spec.replicas: minAvailable`, for both wake-up and active scale-in. |
| `replicas >= minAvailable` | Persisted as requested. Normal Grove gang behavior applies. |

The persisted invariant is:

```text
spec.replicas == 0 || spec.replicas >= minAvailable
```

`Clamp` is independent of the previous replica count. With `minAvailable: 3`, requests from `0` to `1` and from `4` to `2` both persist `spec.replicas: 3`, allowing the closest viable scale-in while keeping the service alive.

A newly submitted `PodClique`, `PodCliqueScalingGroup`, or corresponding `PodCliqueSet` template entry must already satisfy the invariant: create requests with `0 < replicas < minAvailable` are rejected. Updates through the resource or its `/scale` subresource are clamped.

### Limitations/Risks & Mitigations

The main risk is confusing "idle" with "unavailable." The proposed direction ties idle state to desired replicas, not observed status.

An autoscaler may request `1` and observe `spec.replicas: 3` when `minAvailable` is `3`; the risk of repeated below-quorum writes is tracked in [Open Questions](#open-questions).

Rolling updates already stall on a zero-replica standalone `PodClique`: completion requires `minAvailable` updated and ready pods, which zero pods never reach. A `PodCliqueScalingGroup` at `replicas: 0` does not stall, because its completion check compares only generation hashes. That asymmetry is why idle needs an explicit definition rather than a per-level accident.

## Design Details

`Clamp` converts a scaling request into a valid desired replica count. It must never clamp `replicas: 0`, which would defeat scale-to-zero.

```text
canonicalReplicas = 0 if requestedReplicas == 0
canonicalReplicas = max(requestedReplicas, minAvailable) otherwise
```

`spec.replicas` stores `canonicalReplicas`; the defaulting of `PodClique` `replicas: 0` to `1` is removed.

When Grove clamps an update, the admission response includes a warning with the requested value, `minAvailable`, and the persisted value. The warning gives interactive clients immediate feedback without adding persistent annotations or conditions.

For the `/scale` subresource, `spec.replicas` remains the desired replica count and `status.replicas` reports actual observed replicas. `PodClique`, `PodCliqueScalingGroup`, and `PodCliqueSet` should use that same semantic contract.

While a component is idle, it contributes no `PodGroup`, does not set `MinAvailableBreached` to `True`, and counts as updated for rolling-update completion.

### Autoscaler Integration

Grove expects replica writers to emit either `0` or at least `minAvailable`. KEDA can express this contract with `idleReplicaCount: 0` and `minReplicaCount: minAvailable`. The HPA POC in [Open Questions](#open-questions) tests whether repeated positive below-quorum requests create a write loop. Compatibility work remains for Knative KPA, Dynamo Planner, custom controllers, and Grove's `scaleConfig`; each must either emit valid values or tolerate persisted `Clamp` without a write loop.

### Example

Only the fields relevant to gang membership are shown.

Initial shape: a standalone router, a standalone decode clique, and a prefill scaling group of one leader plus three workers. Decode and prefill need two members for quorum, so `(0, 2)` is a real below-quorum range.

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: scale-to-zero-deepseek
spec:
  replicas: 1
  template:
    cliques:
    - name: router
      spec:
        roleName: router
        replicas: 1
        minAvailable: 1
        podSpec:
          containers: [{name: router, image: nginx:latest}]
    - name: pleader
      spec:
        roleName: pleader
        replicas: 1
        minAvailable: 1
        podSpec:
          containers: [{name: pleader, image: nginx:latest}]
    - name: pworker
      spec:
        roleName: pworker
        replicas: 3
        minAvailable: 3
        podSpec:
          containers: [{name: pworker, image: nginx:latest}]
    - name: decode
      spec:
        roleName: decode
        replicas: 2
        minAvailable: 2
        podSpec:
          containers: [{name: decode, image: nginx:latest}]
    podCliqueScalingGroups:
    - name: prefill
      cliqueNames: [pleader, pworker]
      replicas: 2
      minAvailable: 2
```

Subsequent scale-to-zero happens on the derived objects, not this template: Grove sets `replicas` only at creation, so the replica writer owns it afterwards.

```bash
kubectl scale podclique scale-to-zero-deepseek-0-decode --replicas=0
kubectl scale podcliquescalinggroup scale-to-zero-deepseek-0-prefill --replicas=0
```

The router keeps serving. Both idle components contribute no `PodGroup`, so they neither hold the base `PodGang` nor breach `minAvailable`.

A writer wakes prefill by requesting one replica:

```bash
kubectl scale podcliquescalinggroup scale-to-zero-deepseek-0-prefill --replicas=1
```

`Clamp` accepts the request and persists `spec.replicas: 2`. The controller then reconciles two replicas.

Each row is one request on that component:

| Request | Persisted `spec.replicas` |
| --- | --- |
| `0` to `1` | `2` |
| `2` to `1` | `2` |
| `2` to `3` | `3` |
| `3` to `2` | `2` |
| `2` to `0` | `0` |

No positive below-quorum value is stored. The same update semantics apply to the main resource and its `/scale` subresource. A create request with `replicas: 1` and `minAvailable: 2` is rejected rather than clamped.

Today neither scale-to-zero write above is rejected: no admission webhook covers `PodClique` or `PodCliqueScalingGroup`. Both are accepted, and the idle member then holds the whole `PodGang` below its member count, leaving the router `SchedulingGated` (#676). Only the `PodCliqueSet` template validates the same shape:

```text
spec.template.podCliqueScalingGroups[0].replicas: Invalid value: 0: must be greater than 0
spec.template.podCliqueScalingGroups[0].minAvailable: Invalid value: 2: minAvailable must not be greater than replicas
```

A `PodClique` produces no error there only because defaulting rewrites `replicas: 0` to `1` first.

### Monitoring

No new metrics, conditions, or status fields are introduced. Idle is indicated by `spec.replicas: 0`.

### Test Plan

Prototype coverage should show:

- zero-replica components do not block the base gang;
- an idle component does not stall a rolling update;
- create rejects `0 < replicas < minAvailable`;
- resource and `/scale` updates clamp any positive below-quorum request to `minAvailable`, regardless of direction;
- clamped updates return an admission warning, while valid updates do not;
- replica writers that repeatedly request a positive below-quorum value converge without a write loop, pending confirmation by the HPA POC in [Open Questions](#open-questions);
- all three `/scale.status.replicas` implementations report actual observed replicas;
- KEDA with `idleReplicaCount: 0` and `minReplicaCount: minAvailable` writes only valid values;
- scaling to `minAvailable` or above re-enters normal gang behavior.

### Graduation Criteria

- Alpha: direction accepted and initial implementation exists.
- Beta: behavior documented and tested.
- GA: semantics are stable and validated with real scale-to-zero workloads.

## Open Questions

- Does an autoscaler that repeatedly requests `0 < replicas < minAvailable` create a write loop after Grove persists `minAvailable`? A POC should test HPA with `minReplicas < minAvailable`; scale-to-zero is not required.

## Remaining Work

- The admission rules for rejecting invalid creates and clamping resource and `/scale` updates.
- Gang behavior in detail: how the `PodGang` reshapes on idle and wake, how idle state propagates through scheduled, available, and updated status at each resource level, how `startsAfter` handles an idle dependency, whether an idle component is absent from the [coherent update](../393-coherent-rolling-updates/README.md) `PodGangMap` or present and empty, whether it joins an MVU, what a scale to zero does to an update in flight, and what that means for gang termination and partial scale.
- Implementation requires webhooks for `PodClique` and `PodCliqueScalingGroup` resource and `/scale` updates; template validation that allows `replicas: 0` but rejects positive below-quorum creates; removal of the `PodClique` zero-to-one default; updates to gang membership, breach, and rolling-update paths for idle components; observed `status.replicas` at all three levels; and a migration rule that specifies whether pre-existing below-quorum objects are clamped during upgrade or retained until a later replica update brings them into the invariant.

## Alternatives

- Allow `minAvailable: 0`: explicit, but overloads an existing availability contract.
- Keep idle members in the `PodGang` with `minReplicas: 0`: keeps the gang spec stable across idle transitions, but that value already signals released constraints during a coherent update, and backends map it back to a positive threshold.
- Make `minAvailable` mutable: follows scale state, but changes the immutability contract.
- Only support `Block` and never add `Clamp`: keeps `spec.replicas` valid, but writers without an active floor cannot wake from zero or repeatedly receive rejections.
- Preserve the below-quorum request in `spec.replicas` and derive an internal effective replica count: avoids mutating the writer's request, but permanently separates stored desired state from the count the controller actually reconciles.
- Add `activeMinReplicas` now: useful if active warm capacity can differ from gang quorum, but premature if the active floor is simply `minAvailable`.

## Historical Discussion

This section records an earlier design stage and is not normative. The proposal originally carried two below-quorum policies while reviewers considered whether `Clamp` should be fixed behavior:

| Desired replicas | Default policy `Block` | Opt-in policy `Clamp` |
| --- | --- | --- |
| `replicas: 0` | Intentional idle state. The component contributes no required gang members and does not breach `minAvailable`. | Same as `Block`. |
| `0 < replicas < minAvailable` | Invalid, as in the `PodCliqueSet` template today, with the same rule extended to the object and its `scale` subresource. Grove does not create partial gang members. | Accepted only when waking from `0`. Grove preserves `spec.replicas` as desired scale and computes `effectiveReplicas = minAvailable`. |
| `replicas >= minAvailable` | Normal Grove behavior. The component participates in gang scheduling and gang termination using its configured `minAvailable`. | Same as `Block`; desired and effective replicas are equal. |

The opt-in design proposed `belowMinAvailablePolicy` next to `replicas` and `minAvailable` on `PodClique`, `PodCliqueScalingGroup`, and their `PodCliqueSet` template configs:

```go
// +kubebuilder:validation:Enum=Block;Clamp
// +kubebuilder:default=Block
BelowMinAvailablePolicy *BelowMinAvailablePolicy `json:"belowMinAvailablePolicy,omitempty"`
```

This preserved existing behavior by default because unconditional clamping is not purely additive: no admission webhook covers `PodClique` or `PodCliqueScalingGroup` today, so a component already at `replicas: 1` with `minAvailable: 2` runs one pod now and would run two under `Clamp`. Opt-in `Clamp` was the escape for generic autoscalers that wake below `minAvailable`, at the cost of an additional API field and behavior that varied by policy.

An intermediate draft left active scale-in into `(0, minAvailable)` open: should it be rejected or clamped? Latest review feedback favored direction-agnostic `Clamp`; with `minAvailable: 3`, a scale-in request from `4` to `2` persists `spec.replicas: 3`, allowing the closest viable scale-in while keeping the service alive. That feedback also favored storing the valid clamped desired count in `spec.replicas` and reporting observed replicas in `/scale.status.replicas`. The normative proposal therefore drops `Block`, `belowMinAvailablePolicy`, the wake-only rule, and the in-memory `effectiveReplicas` split.

## Appendix

- Tracking issue: [ai-dynamo/grove#677](https://github.com/ai-dynamo/grove/issues/677)
- Related bug: [ai-dynamo/grove#676](https://github.com/ai-dynamo/grove/issues/676)
- Feasibility prototype: [yankay/grove#2](https://github.com/yankay/grove/pull/2)
- Local YAML shape: [multinode-disaggregated-with-frontend.yaml](../../../operator/samples/user-guide/02_pod-and-resource-naming-conventions/multinode-disaggregated-with-frontend.yaml)
- Related Dynamo issue: [ai-dynamo/dynamo#10753](https://github.com/ai-dynamo/dynamo/issues/10753)
- Related Dynamo PR: [ai-dynamo/dynamo#10532](https://github.com/ai-dynamo/dynamo/pull/10532)
- Dynamo autoscaling guide: [Dynamo autoscaling](https://docs.nvidia.com/dynamo/v1.1.1/kubernetes-deployment/deployment-guide/autoscaling)

### Upstream Follow-ups

Outside Grove, none blocking this GREP.

- Kubernetes HPA: allow an active floor above `1` alongside `minReplicas: 0`; without `HPAScaleToZero`, `spec.replicas: 0` disables the HPA today.
- KEDA: nothing needed; `minReplicaCount` with `idleReplicaCount: 0` already expresses that floor.
- Dynamo: give the SLA Planner an idle floor separate from `min_endpoint`, so it reaches `0` without passing through `1 .. minAvailable - 1`.

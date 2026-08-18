# GREP-0677: Zero-Replica Gang Membership

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Zero Replicas Per Level](#zero-replicas-per-level)
  - [Below-Quorum Policy](#below-quorum-policy)
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

Grove should treat `PodClique` or `PodCliqueScalingGroup` components with `replicas: 0` as an intentional idle state. This GREP keeps `minAvailable` non-zero and immutable, makes gang logic tolerant of zero-replica members, and uses a fixed `Clamp` rule to wake a component at `minAvailable` when an autoscaler writes a positive value below quorum.

## Motivation

Scale-to-zero serving commonly keeps a router running while workers scale to zero. In that state, worker pods are intentionally absent, not unhealthy.

Using `minAvailable: 0` would overload a field that already affects gang membership, termination, rolling updates, and startup ordering. `replicas` is the mutable scale target, so `replicas: 0` is the cleaner signal.

External autoscalers should be treated as producers of desired replicas, not as components that understand Grove's per-component `minAvailable`. If Grove needs gang-safe membership above the autoscaler-written value, Grove should derive an internal effective value without writing back to `spec.replicas`.

### Goals

- Treat `replicas: 0` as an intentional idle state without changing `minAvailable`.
- Make `0` the only value below `minAvailable` that Grove runs as written.
- Clamp a wake-up request in `0 < replicas < minAvailable` to an effective count of `minAvailable`.
- Preserve autoscaler-written `spec.replicas` as desired scale, never writing an effective value back.

### Non-Goals

- Change `minAvailable` semantics, including allowing `minAvailable: 0` or making it mutable.
- Require generic autoscalers to discover Grove component quorum.
- Define the complete production implementation.

## Proposal

When a `PodClique` or `PodCliqueScalingGroup` has `replicas: 0`, Grove should not require it as a gang member. When replicas become positive again, its existing `minAvailable` should apply normally.

An idle component should leave the `PodGang` rather than stay in it with a zero threshold: no `PodGroup` in `PodGang.spec.podGroups`, and no scaled `PodGang` for a fully idle `PodCliqueScalingGroup` replica. Shrinking a gang must not disturb the members still running.

### Zero Replicas Per Level

`replicas: 0` means something different at each level:

| Level | `replicas: 0` today | Proposed |
| --- | --- | --- |
| `PodCliqueSet` | Already allowed. Nothing is created; no `minAvailable` at this level. | Unchanged. |
| `PodCliqueScalingGroup` | Rejected in the `PodCliqueSet` template; accepted on the object, which has no webhook. | Idle. No `PodGroup` in the base `PodGang` and no scaled `PodGang`. |
| `PodClique` | Silently defaulted to `1`. | Idle when standalone, contributing no `PodGroup`. Inside a scaling group, idle is expressed at the group level. |

### Below-Quorum Policy

For `0 < replicas < minAvailable`, Grove uses a fixed `Clamp` rule:

| Desired replicas | Behavior |
| --- | --- |
| `replicas: 0` | Intentional idle state. The component contributes no required gang members and does not breach `minAvailable`. |
| `0 < replicas < minAvailable` | Accepted only when waking from `0`. Grove preserves `spec.replicas` as desired scale and computes `effectiveReplicas = minAvailable`. |
| `replicas >= minAvailable` | Normal Grove behavior. The component participates in gang scheduling and gang termination using its configured `minAvailable`; desired and effective replicas are equal. |

Existing behavior changes in two cases: a `PodClique` with `replicas: 0` becomes idle instead of defaulting to one, and an object already in `(0, minAvailable)` is reconciled at `effectiveReplicas = minAvailable`.

`Clamp` applies only on the wake-up edge: a write from `0` into the range is accepted, one from `minAvailable` or above is rejected, so `0` is the only legal scale-in target below `minAvailable`. An explicit scale-in deserves an error, not a silent promotion. Comparing against the previous value puts that check in a validating webhook on the resource and its `scale` subresource, not in defaulting.

Grove should not implement `Clamp` by writing the clamped value back to `spec.replicas`. HPA or KEDA may repeatedly recompute a desired value such as `1`; writing `minAvailable` back into the scale target can create a control-plane write fight. The override is therefore internal only: the autoscaler-written value stays in `spec.replicas` as desired scale, Grove derives the effective value separately, and a writer that keeps recomputing `1` keeps rewriting the value it already wrote, which is a no-op rather than a fight.

### Limitations/Risks & Mitigations

The main risk is confusing "idle" with "unavailable." The proposed direction ties idle state to desired replicas, not observed status.

Generic HPA/KEDA users can still fail to wake from zero to one without an autoscaler floor, so Grove applies `Clamp` on that wake-up edge. Its cost is a desired/effective split: `spec.replicas: 1` may intentionally run `minAvailable` members. Validity also depends on the object's current value, so the same manifest can be accepted or rejected.

That split is transitional: most replica writers cannot express an active floor above `1` while still allowing `0`. KEDA can, pairing `minReplicaCount` with `idleReplicaCount: 0`, and if the rest gain it nothing lands in `(0, minAvailable)`. Grove must still define the case, since users and custom controllers also write `spec.replicas`.

Rolling updates already stall on a zero-replica standalone `PodClique`: completion requires `minAvailable` updated and ready pods, which zero pods never reach. A `PodCliqueScalingGroup` at `replicas: 0` does not stall, because its completion check compares only generation hashes. That asymmetry is why idle needs an explicit definition rather than a per-level accident.

## Design Details

`Clamp` is fixed behavior for translating a positive desired replica count below `minAvailable` into gang-safe effective replicas when waking from zero. It must never clamp `replicas: 0`, which would defeat scale-to-zero.

For `Clamp`, the effective count is:

```text
effectiveReplicas = 0 if desiredReplicas == 0
effectiveReplicas = max(desiredReplicas, minAvailable) otherwise
```

`effectiveReplicas` is derived per reconciliation, not stored; only admission consults the previous value. A `Clamp` component therefore cannot oscillate: it is idle or at least `minAvailable`. Grove treats `spec.replicas` as read-only, leaving the replica writer as its only writer, and the defaulting of `PodClique` `replicas: 0` to `1` is removed.

For the `/scale` subresource, `spec.replicas` remains the desired replica count and `status.replicas` reports actual observed replicas. `PodClique`, `PodCliqueScalingGroup`, and `PodCliqueSet` should use that same semantic contract.

Gang logic reads `minAvailable` directly today, so idle needs a derived floor: `effectiveMinAvailable` is `0` while idle and `minAvailable` otherwise. The contributed `PodGroup`, the breach condition, and the rolling-update completion check should all read it, so an idle component contributes no `PodGroup`, never breaches, and counts as updated.

### Autoscaler Integration

Grove only observes `spec.replicas`, so what matters per writer is when it sleeps and what it writes on wake-up.

| Replica writer | Sleep trigger | Wake-up value | Grove needs |
| --- | --- | --- | --- |
| Native HPA | Manual `0` is Kubernetes' maintenance mode and stops the HPA. It sleeps by itself only with the `HPAScaleToZero` gate. | Manual, or `1` if the HPA slept it. | Fixed `Clamp` on wake-up, and no defaulting back to `1`. |
| KEDA | Triggers idle for `cooldownPeriod`, 5 minutes by default. Sleeping is the default shape. | `minReplicaCount`, `1` by default. It is also the scale-down floor, so skipping the below-quorum range needs `minReplicaCount: minAvailable` with `idleReplicaCount: 0`. | Fixed `Clamp` if `minAvailable` is above `1`; the pairing avoids the desired/effective split when the `ScaledObject` is editable. |
| Knative KPA | An idle window with no requests, on by default. | `1`, when the activator takes a request. | Same as KEDA, but out of reach: its activator and metrics assume Knative Revisions. |
| Dynamo Planner, writing the DGD or a `DynamoGraphDeploymentScalingAdapter` | Its own policy; zero is a computed target. | Whatever it computes. | `Clamp` only on a wake from `0`; active scale-in must stay at or above `minAvailable` or jump to `0`. |
| A custom controller | Its own policy. | Whatever it computes. | Fixed `Clamp` when it wakes into `1 .. minAvailable - 1`; active scale-in into that range is rejected. |
| Grove's `scaleConfig` | Never; admission requires `minReplicas >= minAvailable`. | n/a | `minReplicas: 0` valid while `1 .. minAvailable - 1` stays invalid, no defaulting it from `replicas`, and the `HPAScaleToZero` gate on the generated HPA. |

Waking from zero also needs a signal independent of a running replica, since serving metrics deadlock: in Dynamo, a model with no workers leaves `/v1/models` and returns `404`.

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

Scale-to-zero happens on the derived objects, not this template: Grove sets `replicas` only at creation, so the replica writer owns it afterwards.

```bash
kubectl scale podclique scale-to-zero-deepseek-0-decode --replicas=0
kubectl scale podcliquescalinggroup scale-to-zero-deepseek-0-prefill --replicas=0
```

The router keeps serving. Both idle components contribute no `PodGroup`, so they neither hold the base `PodGang` nor breach `minAvailable`.

KEDA wakes them by writing its `minReplicaCount`, `1` by default:

```bash
kubectl scale podcliquescalinggroup scale-to-zero-deepseek-0-prefill --replicas=1
```

`Clamp` accepts that write, keeps `spec.replicas: 1` as desired scale, and runs `effectiveReplicas: 2`.

Each row is one write on that component:

| Write | Fixed `Clamp` behavior |
| --- | --- |
| `0` to `1` | Accepted, `effectiveReplicas: 2` |
| `1` to `2` | Accepted, still two members |
| `2` to `3` | Accepted |
| `3` to `2` | Accepted |
| `2` to `1` | Rejected |
| `2` to `0` | Accepted |

`(0, minAvailable)` is therefore one-way: reachable only from `0`, left by scaling up or by writing `0`.

Today neither write is rejected: no admission webhook covers `PodClique` or `PodCliqueScalingGroup`. Both are accepted, and the idle member then holds the whole `PodGang` below its member count, leaving the router `SchedulingGated` (#676). Only the `PodCliqueSet` template validates the same shape:

```text
spec.template.podCliqueScalingGroups[0].replicas: Invalid value: 0: must be greater than 0
spec.template.podCliqueScalingGroups[0].minAvailable: Invalid value: 2: minAvailable must not be greater than replicas
```

A `PodClique` produces no error there only because defaulting rewrites `replicas: 0` to `1` first.

### Monitoring

For `Clamp`, a `ReplicasBelowMinAvailable` condition should carry the desired and effective replica counts, so users can see why the running member count exceeds `spec.replicas`; `/scale.status.replicas` exposes the observed count but not why it differs.

### Test Plan

Prototype coverage should show:

- zero-replica components do not block the base gang;
- an idle component does not stall a rolling update;
- fixed `Clamp` computes an effective count of at least `minAvailable` when waking from `0`;
- `Clamp` rejects a scale-in into the below-quorum range on both the resource and its `scale` subresource;
- `Clamp` does not write the effective value back to `spec.replicas`;
- all three `/scale.status.replicas` implementations report actual observed replicas;
- scaling back to `minAvailable` or above can re-enter normal gang behavior.

### Graduation Criteria

- Alpha: direction accepted and initial implementation exists.
- Beta: behavior documented and tested.
- GA: semantics are stable and validated with real scale-to-zero workloads.

## Open Questions

- Should active scale-in into `(0, minAvailable)` be rejected, or accepted and internally clamped? The current direction limits `Clamp` to scale-out from `0`, returning a validation error for invalid scale-in so callers receive explicit feedback; rejection, however, makes HPA retry failed writes. Acceptance avoids retries by preserving the autoscaler-written desired value in `spec.replicas` while `status.replicas` remains observed, but HPA may report a successful scale-down without reducing pods.

## Remaining Work

Accepting the direction above does not finish the GREP. Still to come, in this order:

- Measured autoscaler behavior, not reasoning about it: HPA and KEDA against a `Clamp` component across the wake-up edge, repeated below-quorum writes, `cooldownPeriod` and stabilization windows, and observed `status.replicas`. It confirms or replaces the no-write-fight and no-oscillation claims above, and lands here with its setup and versions.
- The admission rule over previous and new value pairs, including create and re-apply, where there is no previous value.
- Gang behavior in detail: how the `PodGang` reshapes on idle and wake, whether an idle component is absent from the [coherent update](../393-coherent-rolling-updates/README.md) `PodGangMap` or present and empty, whether it joins an MVU, what a scale to zero does to an update in flight, and what that means for gang termination and partial scale.
- The implementation surface in one place. Neither `PodClique` nor `PodCliqueScalingGroup` has a webhook today, so this GREP implies new ones on both and their `scale` subresource, plus dropping the `PodClique` `replicas: 0` defaulting, the `effectiveMinAvailable` readers, looser `scaleConfig` rules, observed `status.replicas` at all three levels, and upgrade handling for pre-existing below-quorum objects. Listing them is in scope; designing them is not.

## Alternatives

- Allow `minAvailable: 0`: explicit, but overloads an existing availability contract.
- Keep idle members in the `PodGang` with `minReplicas: 0`: keeps the gang spec stable across idle transitions, but that value already signals released constraints during a coherent update, and backends map it back to a positive threshold.
- Make `minAvailable` mutable: follows scale state, but changes the immutability contract.
- Only support `Block` and never add `Clamp`: keeps `spec.replicas` truthful, but generic HPA/KEDA scale-from-zero can get stuck without an autoscaler floor.
- Make `Clamp` direction-agnostic: re-applying a stored object never fails, but an explicit scale-in is silently promoted with no signal to the caller.
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

Review resolved two questions: `Clamp` is fixed behavior without a policy field, and `/scale.status.replicas` reports observed replicas while `spec.replicas` holds desired state. The normative proposal therefore drops `Block`, `belowMinAvailablePolicy`, and policy-qualified behavior; they remain here only to preserve the design history.

An intermediate draft considered persisting the clamped value to `spec.replicas`. The current proposal keeps `Clamp` internal to preserve replica-writer ownership and avoid write contention.

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

- Kubernetes HPA: allow an active floor above `1` alongside `minReplicas: 0`, and resume from `spec.replicas: 0`, which disables the HPA today.
- KEDA: nothing needed; `minReplicaCount` with `idleReplicaCount: 0` already expresses that floor.
- Dynamo: give the SLA Planner an idle floor separate from `min_endpoint`, so it reaches `0` without passing through `1 .. minAvailable - 1`.

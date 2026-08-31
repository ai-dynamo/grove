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
  - [Historical HPA Clamp Experiment](#historical-hpa-clamp-experiment)
  - [Upstream Follow-ups](#upstream-follow-ups)
<!-- /toc -->

## Summary

Grove should treat `PodClique` or `PodCliqueScalingGroup` components with `replicas: 0` as an intentional idle state. This GREP leaves existing `minAvailable` semantics unchanged, makes gang logic tolerant of zero-replica members, and rejects any positive replica count below `minAvailable` instead of clamping it.

## Motivation

Scale-to-zero serving commonly keeps a router running while workers scale to zero. In that state, worker pods are intentionally absent, not unhealthy.

Using `minAvailable: 0` would overload a field that already affects gang membership, termination, rolling updates, and startup ordering. `replicas` is the mutable scale target, so `replicas: 0` is the cleaner signal.

External replica writers should target either `0` or a value at least `minAvailable`; Grove rejects any other positive below-quorum request.

### Goals

- Treat `replicas: 0` as an intentional idle state without changing `minAvailable`.
- Keep `spec.replicas` valid: it is either `0` or greater than or equal to `minAvailable`.
- Reject any positive replica count below `minAvailable`.

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

Grove rejects positive replica counts below `minAvailable`:

| Requested replicas | Behavior |
| --- | --- |
| `0` | Intentional idle state. The component contributes no required gang members and does not breach `minAvailable`. |
| `0 < replicas < minAvailable` | Rejected for create and update requests, including the `/scale` subresource. |
| `replicas >= minAvailable` | Persisted as requested. Normal Grove gang behavior applies. |

The persisted invariant is:

```text
spec.replicas == 0 || spec.replicas >= minAvailable
```

Reject is independent of the previous replica count. With `minAvailable: 3`, requests from `0` to `1` and from `4` to `2` are both rejected.

A newly submitted `PodClique`, `PodCliqueScalingGroup`, or corresponding `PodCliqueSet` template entry must already satisfy the invariant. Updates through the main resource or its `/scale` subresource that request `0 < replicas < minAvailable` are rejected.

### Limitations/Risks & Mitigations

The main risk is confusing "idle" with "unavailable." The proposed direction ties idle state to desired replicas, not observed status.

An HPA may repeatedly request an invalid below-quorum value. The [historical HPA Clamp experiment](#historical-hpa-clamp-experiment) shows that accepting and clamping such requests does not make the writer converge; the reject path requires verification once implemented.

Rolling updates already stall on a zero-replica standalone `PodClique`: completion requires `minAvailable` updated and ready pods, which zero pods never reach. A `PodCliqueScalingGroup` at `replicas: 0` does not stall, because its completion check compares only generation hashes. That asymmetry is why idle needs an explicit definition rather than a per-level accident.

## Design Details

Admission rejects any positive requested replica count below `minAvailable` without rewriting `spec.replicas`. The defaulting of `PodClique` `replicas: 0` to `1` is removed.

While a component is idle, it contributes no `PodGroup`, does not set `MinAvailableBreached` to `True`, and counts as updated for rolling-update completion.

### Autoscaler Integration

Grove-compatible replica writers must converge to `0` while idle and at least `minAvailable` while active without repeatedly writing a positive below-quorum value.

| Model | Behavior | Compatibility |
| --- | --- | --- |
| KEDA-like | Separate idle and active floors, such as KEDA `idleReplicaCount: 0` with `minReplicaCount: minAvailable`. | Compatible. |
| HPA-like | May repeatedly write a positive below-quorum value or prevent scale-to-zero. | Incompatible. |

[KEDA](https://keda.sh/docs/2.20/reference/scaledobject-spec/) emits only `0` or at least `minAvailable`. Native [HPA](https://kubernetes.io/docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/#scaling-to-and-from-zero) is HPA-like. Knative Serving v1.23.0 KPA is also HPA-like because [`autoscaling.knative.dev/activation-scale`](https://knative.dev/docs/serving/autoscaling/scale-bounds/#scale-up-minimum) is not a persistent active floor. Dynamo Planner has not established Grove-compatible scale-to-zero behavior. Grove `scaleConfig` is HPA-like because it generates an HPA. New custom controllers should use KEDA-like semantics to support scale-to-zero.

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

Grove rejects the request because it is below `minAvailable: 2`. The writer must request at least two replicas:

```bash
kubectl scale podcliquescalinggroup scale-to-zero-deepseek-0-prefill --replicas=2
```

Each row is one request on that component:

| Request | Behavior |
| --- | --- |
| `0` to `1` | Rejected |
| `2` to `1` | Rejected |
| `2` to `3` | Persist `3` |
| `3` to `2` | Persist `2` |
| `2` to `0` | Persist `0` |

No positive below-quorum value is stored. The same validation applies to the main resource, its `/scale` subresource, and create requests.

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
- create requests and updates through the main resource or `/scale` reject `0 < replicas < minAvailable`;
- rejected updates leave `spec.replicas` unchanged;
- a KEDA-like integration transitions from `0` directly to `minAvailable` or above without writing a positive below-quorum value;
- an HPA-like writer receives a validation error for a below-quorum recommendation and leaves `spec.replicas` unchanged, with repeated retry behavior documented;
- scaling to `minAvailable` or above re-enters normal gang behavior.

### Graduation Criteria

- Alpha: direction accepted and initial implementation exists.
- Beta: behavior documented and tested.
- GA: semantics are stable and validated with real scale-to-zero workloads.

## Open Questions

Should a `PodClique` owned by a `PodCliqueScalingGroup` be allowed to set `replicas: 0` independently? The general replica invariant permits it, while [Zero Replicas Per Level](#zero-replicas-per-level) says that idle for a scaling-group member is expressed at the group level. The template, derived object, and admission semantics must align once this is resolved.

## Remaining Work

- The admission rules for rejecting invalid creates and replica updates through the main resource or `/scale`.
- Gang behavior in detail: how the `PodGang` reshapes on idle and wake, how idle state propagates through scheduled, available, and updated status at each resource level, how `startsAfter` handles an idle dependency, whether an idle component is absent from the [coherent update](../393-coherent-rolling-updates/README.md) `PodGangMap` or present and empty, whether it joins an MVU, what a scale to zero does to an update in flight, and what that means for gang termination and partial scale.
- Implementation requires webhooks for `PodClique` and `PodCliqueScalingGroup` resource and `/scale` updates; template validation that allows `replicas: 0` but rejects positive below-quorum creates; removal of the `PodClique` zero-to-one default; updates to gang membership, breach, and rolling-update paths for idle components; and a migration rule for pre-existing below-quorum objects.

## Alternatives

- Allow `minAvailable: 0`: explicit, but overloads an existing availability contract.
- Keep idle members in the `PodGang` with `minReplicas: 0`: keeps the gang spec stable across idle transitions, but that value already signals released constraints during a coherent update, and backends map it back to a positive threshold.
- Make `minAvailable` mutable: follows scale state, but changes the immutability contract.
- Clamp positive below-quorum requests to `minAvailable`: lets writers without an active floor wake from zero, but silently rewrites caller intent and does not make HPA-like writers converge.
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

An intermediate draft favored direction-agnostic `Clamp`; with `minAvailable: 3`, a scale-in request from `4` to `2` would persist `spec.replicas: 3`. Later review favored cleaner API semantics that reject the invalid range. The normative proposal therefore uses reject-only behavior for every positive value below `minAvailable` and does not add `belowMinAvailablePolicy`, wake-only clamping, or the in-memory `effectiveReplicas` split.

## Appendix

- Tracking issue: [ai-dynamo/grove#677](https://github.com/ai-dynamo/grove/issues/677)
- Related bug: [ai-dynamo/grove#676](https://github.com/ai-dynamo/grove/issues/676)
- Feasibility prototype: [yankay/grove#2](https://github.com/yankay/grove/pull/2)
- Local YAML shape: [multinode-disaggregated-with-frontend.yaml](../../../operator/samples/user-guide/02_pod-and-resource-naming-conventions/multinode-disaggregated-with-frontend.yaml)
- Related Dynamo issue: [ai-dynamo/dynamo#10753](https://github.com/ai-dynamo/dynamo/issues/10753)
- Related Dynamo PR: [ai-dynamo/dynamo#10532](https://github.com/ai-dynamo/dynamo/pull/10532)
- Dynamo autoscaling guide: [Dynamo autoscaling](https://docs.nvidia.com/dynamo/v1.1.1/kubernetes-deployment/deployment-guide/autoscaling)

### Historical HPA Clamp Experiment

This historical, non-normative experiment evaluated persisted `Clamp`, not the current Reject proposal. It shows that Clamp did not eliminate repeated writes, but provides no evidence about retry behavior after rejection.

Can persisted `Clamp` make an HPA-like writer converge when its recommendation falls below `minAvailable`?

An HPA POC against [PR #788](https://github.com/ai-dynamo/grove/pull/788) used `minAvailable: 2` and observed each case for 35 seconds after stabilization. Both configurations were tested with `Value` and `AverageValue` metrics against `PodClique` and `PodCliqueScalingGroup`.

| HPA configuration | Result for both metrics and scale targets |
| --- | --- |
| `minReplicas: 2` | `0` repeated writes; `NO_LOOP` |
| `minReplicas: 1`, recommending `1` | `7` repeated writes per case; `WRITE_LOOP` |

The experiment demonstrates that persisted `Clamp` does not make an HPA-like writer converge: HPA keeps requesting `1` while Grove stores `2`. Reject preserves the stored replica count and surfaces the invalid recommendation instead of silently rewriting it, but the experiment did not test whether HPA retries after a rejected update. The reject implementation must verify and document that behavior. KEDA-like writers avoid the invalid range by emitting only `0` or at least `minAvailable`.

### Upstream Follow-ups

Outside Grove, none blocking this GREP.

- [Kubernetes HPA](https://kubernetes.io/docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/#scaling-to-and-from-zero): provide separate idle and active floors. In Kubernetes v1.37, `HPAScaleToZero` is beta and enabled by default for Object and External metrics, but HPA still exposes only one `minReplicas` floor.
- [KEDA](https://keda.sh/docs/2.20/reference/scaledobject-spec/): nothing needed; `minReplicaCount` with `idleReplicaCount: 0` already expresses that floor.
- Dynamo Planner: provide separate idle and active floors.

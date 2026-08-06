# GREP-0677: Zero-Replica Gang Membership

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [Example](#example)
  - [Monitoring](#monitoring)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Alternatives](#alternatives)
- [Appendix](#appendix)
<!-- /toc -->

## Summary

Grove should treat `PodClique` or `PodCliqueScalingGroup` components with `replicas: 0` as an intentional idle state. This GREP is only a direction-setting draft: it keeps `minAvailable` non-zero and immutable, makes future gang logic tolerant of zero-replica members, keeps the default behavior for positive replicas below `minAvailable`, and defines an opt-in clamp policy for generic autoscalers.

## Motivation

Scale-to-zero serving commonly keeps a router running while workers scale to zero. In that state, worker pods are intentionally absent, not unhealthy.

Using `minAvailable: 0` would overload a field that already affects gang membership, termination, rolling updates, and startup ordering. `replicas` is the mutable scale target, so `replicas: 0` is the cleaner signal.

External autoscalers should be treated as producers of desired replicas, not as components that understand Grove's per-component `minAvailable`. If Grove needs gang-safe membership above the autoscaler-written value, Grove should derive an internal effective value without writing back to `spec.replicas`.

### Goals

- Treat `replicas: 0` as an intentional idle state without changing `minAvailable`.
- Keep the default validation behavior for positive replicas below `minAvailable`.
- Define an opt-in `Clamp` policy for generic autoscalers that wake below `minAvailable`.
- Preserve autoscaler-written `spec.replicas` as desired scale.

### Non-Goals

- Change `minAvailable` semantics, including allowing `minAvailable: 0` or making it mutable.
- Require generic autoscalers to discover Grove component quorum.
- Define the complete production implementation in this draft.

## Proposal

When a `PodClique` or `PodCliqueScalingGroup` has `replicas: 0`, Grove should not require it as a gang member. When replicas become positive again, its existing `minAvailable` should apply normally.

For `0 < replicas < minAvailable`, Grove should define an explicit below-quorum policy:

| Desired replicas | Default policy `Block` | Opt-in policy `Clamp` |
| --- | --- | --- |
| `replicas: 0` | Intentional idle state. The component contributes no required gang members and does not breach `minAvailable`. | Same as `Block`. |
| `0 < replicas < minAvailable` | Invalid, preserving the existing positive-replica validation behavior. Grove does not create partial gang members. | Grove preserves `spec.replicas` as desired scale, computes `effectiveReplicas = minAvailable`, and exposes the desired/effective split in status or events. |
| `replicas >= minAvailable` | Normal Grove behavior. The component participates in gang scheduling and gang termination using its configured `minAvailable`. | Same as `Block`; desired and effective replicas are equal. |

Under the default `Block` policy, this GREP only changes the zero-replica case; positive replicas below `minAvailable` keep the existing validation behavior.

Grove should not implement `Clamp` by writing the clamped value back to `spec.replicas`. HPA or KEDA may repeatedly recompute a desired value such as `1`; writing `minAvailable` back into the scale target can create a control-plane write fight. If `Clamp` is used, the autoscaler-written value remains desired scale and Grove uses a separate effective value internally.

This draft does not define every controller or backend branch. Those details should be added only after reviewers agree on the direction.

### Limitations/Risks & Mitigations

The main risk is confusing "idle" with "unavailable." The proposed direction ties idle state to desired replicas, not observed status.

The default `Block` policy keeps the existing positive-replica validation behavior. Generic HPA/KEDA users can still fail to wake from zero to one if they do not configure an autoscaler floor. Users who prefer automatic wake-up can opt into `Clamp`.

The `Clamp` policy makes generic HPA/KEDA scale-from-zero friendlier, but `spec.replicas: 1` may intentionally run `minAvailable` members. This desired/effective split must be visible in status or events.

The prototype must be described only as a feasibility artifact, not as the final design.

## Design Details

Implementation should derive gang membership from desired replicas and the below-quorum policy. A zero-replica component contributes zero required members while idle.

This draft proposes `belowMinAvailablePolicy` as an optional enum field next to `replicas` and `minAvailable` on `PodClique`, `PodCliqueScalingGroup`, and their `PodCliqueSet` template configs:

```go
// +kubebuilder:validation:Enum=Block;Clamp
// +kubebuilder:default=Block
BelowMinAvailablePolicy *BelowMinAvailablePolicy `json:"belowMinAvailablePolicy,omitempty"`
```

The default value is `Block`. `Clamp` is opt-in for users who want Grove to translate a positive desired replica count below `minAvailable` into gang-safe effective replicas.

For `Clamp`, the effective count is:

```text
effectiveReplicas = 0 if desiredReplicas == 0
effectiveReplicas = max(desiredReplicas, minAvailable) otherwise
```

For `Block`, positive desired replicas below `minAvailable` remain invalid, preserving the existing behavior for positive replicas.

### Example

Only the fields relevant to gang membership are shown.

Initial serving shape: the router, standalone decode worker, and prefill worker group are running.

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
        replicas: 1
        minAvailable: 1
        podSpec:
          containers: [{name: pworker, image: nginx:latest}]
    - name: decode
      spec:
        roleName: decode
        replicas: 1
        minAvailable: 1
        podSpec:
          containers: [{name: decode, image: nginx:latest}]
    podCliqueScalingGroups:
    - name: prefill
      cliqueNames: [pleader, pworker]
      replicas: 1
      minAvailable: 1
```

After scale-to-zero: the components remain defined, and the autoscaler only changes the worker `replicas`.

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: scale-to-zero-deepseek
spec:
  template:
    cliques:
    - name: decode
      spec:
        replicas: 0
        minAvailable: 1
        belowMinAvailablePolicy: Clamp
    podCliqueScalingGroups:
    - name: prefill
      cliqueNames: [pleader, pworker]
      replicas: 0
      minAvailable: 1
      belowMinAvailablePolicy: Clamp
```

Under this GREP, the second shape means the standalone decode `PodClique` and prefill `PodCliqueScalingGroup` are idle and do not block the router. If they scale above zero, their existing `minAvailable` applies again.

A canonical below-quorum wake-up looks like:

```text
minAvailable = 2
replicas = 0
external autoscaler writes replicas = 1
```

With `Block`, this remains invalid because positive replicas are below `minAvailable`. With `Clamp`, Grove preserves `replicas: 1`, internally uses `effectiveReplicas: 2`, and exposes that split.

With current Grove validation, the full scale-to-zero shape is rejected because `PodCliqueScalingGroup.replicas: 0` is not yet allowed:

```text
spec.template.podCliqueScalingGroups[0].replicas: Invalid value: 0: must be greater than 0
spec.template.podCliqueScalingGroups[0].minAvailable: Invalid value: 1: minAvailable must not be greater than replicas
```

### Monitoring

For `Clamp`, status or events should show desired replicas and effective replicas so users can understand why the running member count is greater than `spec.replicas`.

### Test Plan

Prototype coverage should show:

- zero-replica components do not block the base gang;
- `minAvailable` remains non-zero;
- default `Block` rejects `0 < replicas < minAvailable`;
- opt-in `Clamp` computes an effective count of at least `minAvailable`;
- `Clamp` does not write the effective value back to `spec.replicas`;
- scaling back to `minAvailable` or above can re-enter normal gang behavior.

### Graduation Criteria

- Alpha: direction accepted and initial implementation exists.
- Beta: behavior documented and tested.
- GA: semantics are stable and validated with real scale-to-zero workloads.

## Alternatives

- Allow `minAvailable: 0`: explicit, but overloads an existing availability contract.
- Make `minAvailable` mutable: follows scale state, but changes the immutability contract.
- Only support `Block` and never add `Clamp`: keeps `spec.replicas` truthful, but generic HPA/KEDA scale-from-zero can get stuck without an autoscaler floor.
- Always clamp positive replicas below `minAvailable`: makes generic HPA/KEDA wake-up work, but imposes a default desired/effective split on every below-quorum state.
- Add `activeMinReplicas` now: useful if active warm capacity can differ from gang quorum, but premature if the active floor is simply `minAvailable`.
- Work around this outside Grove: avoids Grove changes, but leaves no consistent Grove semantics.

## Appendix

- Tracking issue: [ai-dynamo/grove#677](https://github.com/ai-dynamo/grove/issues/677)
- Related bug: [ai-dynamo/grove#676](https://github.com/ai-dynamo/grove/issues/676)
- Feasibility prototype: [yankay/grove#2](https://github.com/yankay/grove/pull/2)
- Local YAML shape: [multinode-disaggregated-with-frontend.yaml](../../../operator/samples/user-guide/02_pod-and-resource-naming-conventions/multinode-disaggregated-with-frontend.yaml)
- Related Dynamo issue: [ai-dynamo/dynamo#10753](https://github.com/ai-dynamo/dynamo/issues/10753)
- Related Dynamo PR: [ai-dynamo/dynamo#10532](https://github.com/ai-dynamo/dynamo/pull/10532)
- Dynamo autoscaling guide: [Dynamo autoscaling](https://docs.nvidia.com/dynamo/v1.1.1/kubernetes-deployment/deployment-guide/autoscaling)
- Research notes: [research-notes.md](research-notes.md)

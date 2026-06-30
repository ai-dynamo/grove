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

Grove should treat `PodClique` or `PodCliqueScalingGroup` components with `replicas: 0` as an intentional idle state. This GREP is only a direction-setting draft: it keeps `minAvailable` non-zero and immutable, makes future gang logic tolerant of zero-replica members, and keeps positive replicas below `minAvailable` invalid.

## Motivation

Scale-to-zero serving commonly keeps a router running while workers scale to zero. In that state, worker pods are intentionally absent, not unhealthy.

Using `minAvailable: 0` would overload a field that already affects gang membership, termination, rolling updates, and startup ordering. `replicas` is the mutable scale target, so `replicas: 0` is the cleaner signal.

### Goals

- Confirm `replicas: 0` as an intentional idle state for `PodClique` and `PodCliqueScalingGroup`.
- Confirm that positive replicas below `minAvailable` remain invalid.
- Keep `minAvailable` non-zero and immutable.
- Give follow-up work one agreed direction.
- Use a prototype PR only to show feasibility.

### Non-Goals

- Support `minAvailable: 0`.
- Make `minAvailable` mutable.
- Automatically inflate positive replicas below `minAvailable`.
- Define the complete implementation in this draft.
- Treat the prototype as production complete.
- Define complete behavior differences between `PodClique` and `PodCliqueScalingGroup`.

## Proposal

When a `PodClique` or `PodCliqueScalingGroup` has `replicas: 0`, Grove should not require it as a gang member. When replicas become positive again, its existing `minAvailable` should apply normally.

This GREP only special-cases `replicas: 0`; positive replicas below `minAvailable` remain invalid. The intended contract is:

| Desired replicas | Behavior |
| --- | --- |
| `replicas: 0` | Intentional idle state. The component contributes no required gang members and does not breach `minAvailable`. |
| `0 < replicas < minAvailable` | Invalid desired state. Grove should reject it rather than gang-scheduling a partial component. |
| `replicas >= minAvailable` | Normal Grove behavior. The component participates in gang scheduling and gang termination using its configured `minAvailable`. |

Grove should not derive `max(minAvailable, replicas)` or overwrite the autoscaler's desired value.

This draft does not define every controller or backend branch. Those details should be added only after reviewers agree on the direction.

### Limitations/Risks & Mitigations

The main risk is confusing "idle" with "unavailable." The proposed direction ties idle state to desired replicas, not observed status.

External autoscalers must follow the same contract: scale these components either to `0` or to `>= minAvailable`.

The prototype must be described only as a feasibility artifact, not as the final design.

## Design Details

No new API fields are proposed.

Implementation should derive effective gang membership from desired replicas. A zero-replica component contributes zero required members while idle.

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
    podCliqueScalingGroups:
    - name: prefill
      cliqueNames: [pleader, pworker]
      replicas: 0
      minAvailable: 1
```

Under this GREP, the second shape means the standalone decode `PodClique` and prefill `PodCliqueScalingGroup` are idle and do not block the router. If they scale above zero, their existing `minAvailable` applies again.

With current Grove validation, the full scale-to-zero shape is rejected because `PodCliqueScalingGroup.replicas: 0` is not yet allowed:

```text
spec.template.podCliqueScalingGroups[0].replicas: Invalid value: 0: must be greater than 0
spec.template.podCliqueScalingGroups[0].minAvailable: Invalid value: 1: minAvailable must not be greater than replicas
```

### Monitoring

No new metrics or status fields are required by this initial draft.

### Test Plan

Prototype coverage should show:

- zero-replica components do not block the base gang;
- `minAvailable` remains non-zero;
- positive replicas below `minAvailable` are rejected;
- scaling back to `minAvailable` or above can re-enter normal gang behavior.

### Graduation Criteria

- Alpha: direction accepted and initial implementation exists.
- Beta: behavior documented and tested.
- GA: semantics are stable and validated with real scale-to-zero workloads.

## Alternatives

- Allow `minAvailable: 0`: explicit, but overloads an existing availability contract.
- Make `minAvailable` mutable: follows scale state, but changes the immutability contract.
- Treat desired replicas as `max(minAvailable, replicas)`: avoids rejecting partial wake-ups, but hides the autoscaler's requested state.
- Work around this outside Grove: avoids Grove changes, but leaves no consistent Grove semantics.

## Appendix

- Tracking issue: [ai-dynamo/grove#677](https://github.com/ai-dynamo/grove/issues/677)
- Related bug: [ai-dynamo/grove#676](https://github.com/ai-dynamo/grove/issues/676)
- Feasibility prototype: [yankay/grove#2](https://github.com/yankay/grove/pull/2)
- Local YAML shape: [multinode-disaggregated-with-frontend.yaml](../../../operator/samples/user-guide/02_pod-and-resource-naming-conventions/multinode-disaggregated-with-frontend.yaml)
- Related Dynamo issue: [ai-dynamo/dynamo#10753](https://github.com/ai-dynamo/dynamo/issues/10753)
- Related Dynamo PR: [ai-dynamo/dynamo#10532](https://github.com/ai-dynamo/dynamo/pull/10532)
- Dynamo autoscaling guide: [Dynamo autoscaling](https://docs.nvidia.com/dynamo/v1.1.1/kubernetes-deployment/deployment-guide/autoscaling)

# GREP-950: Optional stabilization window for rolling updates

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [Monitoring](#monitoring)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Add an optional stabilization window before Grove advances a rolling update. Replacement capacity must remain Ready continuously for a configurable duration before it permits further replacement of old capacity. The default is zero, preserving existing behavior.

Tracking issue: [#950](https://github.com/ai-dynamo/grove/issues/950).

## Motivation

We encounter transient `404`/`503` responses during Dynamo rolling updates on Grove. A replacement worker can become Kubernetes Ready before the frontend finishes fetching its metadata and initializing its serving pipeline. If the rollout removes the last routable old worker during this interval, requests fail until frontend initialization completes. This gap is documented in [Dynamo #13622](https://github.com/ai-dynamo/dynamo/issues/13622).

Disaggregated prefill/decode deployments also need time for replacement P/D capacity to become routable. Coherent coordinates component replacement, but its progression checks do not establish application-level routability.

A small, generic stabilization window would let applications retain remaining old capacity longer without taking over rollout orchestration through OnDelete. Grove would continue to own scheduling and replacement; application discovery would proceed normally during the wait.

### Goals

- Provide an opt-in settling interval for RollingRecreate and Coherent.
- Preserve existing behavior when the duration is zero.
- Support standalone workers and multi-node logical replicas.

### Non-Goals

Frontend acknowledgements, inference probes, generic pause/resume, cross-version P/D routing, request draining, and surge capacity are outside this proposal.

## Proposal

For RollingRecreate, a replacement only replenishes the disruption budget after it has remained Ready for the configured duration.

For Coherent, hold advancement to the next sub-step until the replacement units committed by the current sub-step have remained Ready for the configured duration. When P/D are updated together, this includes both participating components. Existing scheduling and availability-budget checks still apply. The window applies before further disruptive advancement, rather than postponing creation of the initial replacement batch.

Pod readiness and discovery remain unchanged, allowing frontend initialization to proceed during the wait.

### Limitations/Risks & Mitigations

This is a time-based mitigation, not proof that every frontend can serve through the replacements. Initialization can exceed the chosen window, and larger windows slow rollouts. Applications remain responsible for version-compatible routing and graceful draining.

The policy requires sufficient remaining capacity. A 1P1D deployment without spare capacity can still experience downtime. The window does not restore capacity already removed within the current batch or change the configured disruption budget.

## Design Details

Add an optional non-negative stabilization duration to rolling-update configuration. The exact API placement is open for discussion. Unset or zero disables the additional gate; OnDelete is unaffected.

For a multi-node logical replica, readiness must cover its required member Pods, not only its leader. Reset the relevant interval when readiness is lost or a tracked Pod is replaced. Timing evidence must be tied to the replacement revision and Pod identities, and remain correct across controller restarts. Requeue when the remaining window expires so progress does not depend on another Pod event.

### Monitoring

Expose the stabilization wait and remaining duration through rollout status or events, distinguishing it from scheduling failures and exhausted availability budgets.

### Test Plan

Cover zero-duration compatibility, continuous-readiness timing, readiness loss, Pod replacement, controller restart, and multi-node replica boundaries for both automatic strategies. Verify that Coherent cannot advance while a participating replacement P or D unit is still stabilizing.

Validate with continuous Dynamo traffic during aggregated and 2P3D Coherent rollouts. Measure errors and rollout duration with the setting disabled and enabled. A 1P1D case should document the unavoidable capacity gap without surge.

### Graduation Criteria

Initially opt-in, with controller tests and end-to-end evidence that the window reduces the reported rollout gap. Broader adoption depends on operational feedback; no nonzero default is proposed.

## Alternatives

Delaying Pod readiness can also delay discovery and frontend initialization. Using OnDelete moves batch orchestration and recovery into the consumer. A full application acknowledgement protocol provides stronger evidence but requires substantially more coordination.

The externally coordinated lifecycle hold discussed in [#858](https://github.com/ai-dynamo/grove/issues/858) covers a broader set of lifecycle actions. This proposal only adds a bounded rollout policy and complements the application-level admission discussion in [Dynamo #17083](https://github.com/ai-dynamo/dynamo/issues/17083).

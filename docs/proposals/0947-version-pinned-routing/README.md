# GREP-0947: Version-Pinned Routing for Coherent Updates

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [API and Strategy Behavior](#api-and-strategy-behavior)
  - [Routing Label Contract](#routing-label-contract)
  - [llm-d Integration Contract](#llm-d-integration-contract)
    - [Current Routing and Handoff](#current-routing-and-handoff)
    - [Proposed Overlay Requirements](#proposed-overlay-requirements)
    - [Failure and Retry Behavior](#failure-and-retry-behavior)
  - [Snapshot Selection and Materialization](#snapshot-selection-and-materialization)
  - [Rollout Identity and Completion](#rollout-identity-and-completion)
  - [Enabling and Disabling](#enabling-and-disabling)
  - [Corrective Edits and Retained Revisions](#corrective-edits-and-retained-revisions)
  - [Monitoring](#monitoring)
  - [Dependencies and Review Decisions](#dependencies-and-review-decisions)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This increment to [GREP-393](../393-coherent-rolling-updates/README.md) proposes an opt-in mode that replaces every component during a Coherent update and gives Pods from the same materialized PodCliqueSet snapshot a shared, Grove-owned routing revision label. A router can then select prefill and decode Pods from one revision for each request, including during a decode-only update. Selective replacement remains the default. This document proposes a contract, not an implemented feature; the API names and integration with revision persistence require maintainer agreement.

## Motivation

GREP-393 leaves cross-version communication to the data plane. Today, `grove.io/pod-template-hash` identifies an individual clique's template, not a shared PodCliqueSet revision. A PodCliqueSet generation label alone would not solve this: a decode-only update leaves unchanged prefill Pods at the previous generation, so a router requiring label equality across roles would find no complete revision.

The [llm-d rollout screener](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/epp/framework/plugins/requesthandling/screener/disaggregatedsetrollout/README.md) supports configurable revision and role label keys. A Grove-owned label would remove the need for users to maintain matching revision labels manually. Its safety depends on the controller associating the label with the configuration actually used to create each Pod, rather than with the latest desired configuration.

### Goals

- Provide a shared routing identity across all cliques materialized from one PodCliqueSet snapshot.
- Make all-component replacement explicit and opt-in, including unchanged standalone cliques and scaling groups.
- Keep identity correct across partial updates, corrective edits, resource recreation, and controller restart.
- Preserve Coherent planning, disruption budgets, and the existing default selective behavior.
- Define enable/disable behavior and a testable router contract.

### Non-Goals

- Inferring binary, protocol, model, or KV-cache compatibility from a revision label.
- Guaranteeing uninterrupted service, adding surge capacity, or draining in-flight requests.
- Relabeling unchanged Pods into multiple compatible revisions or defining revision membership sets.
- Adding a rollback API, implementing the router, or changing `RollingRecreate` and `OnDelete` semantics.
- Implementing the revision-persistence work tracked by [#941](https://github.com/ai-dynamo/grove/issues/941) in this documentation PR.

## Proposal

An operator may opt a Coherent PodCliqueSet into all-component replacement. Every rollout in that mode includes all standalone PodCliques and all PodCliqueScalingGroups, even when only one clique's template changed. Existing MVU, anchor/tail, replica ordering, and disruption-budget rules still determine the replacement cadence; "all components" does not mean replacing every Pod simultaneously.

Grove assigns a routing revision when it creates a Pod in this mode. All roles created from the same snapshot share the value, and surviving Pods keep the value they received at creation. A router selects one revision per request and offers it only when every required role has Ready, non-terminating candidates at that revision. If no revision has all required roles, it must not fall back to cross-revision pairing.

### Limitations/Risks & Mitigations

- **More disruption and restart cost:** unchanged components are replaced too. Keep the mode opt-in and retain component budgets. Capacity headroom remains the user's responsibility.
- **No complete Ready revision:** small workloads, failed targets, or mode transitions may leave neither revision routable. Routers fail closed; neither scheduling success nor rollout completion alone is a routing-availability guarantee.
- **False identity from reconciliation races:** an active replica may still contain old child resources. Require configuration and identity from the same durable materialization selection, and wait rather than guess when they cannot be reconciled.
- **Labels are not compatibility or integrity proofs:** mutable image tags, application state, and external configuration can change independently. Use immutable workload inputs where required; ordinary Kubernetes ownership/RBAC controls still apply.

## Design Details

### API and Strategy Behavior

Proposed API spelling, subject to review:

```yaml
apiVersion: grove.io/v1beta1
kind: PodCliqueSet
metadata:
  name: inference
spec:
  updateStrategy:
    type: Coherent
    updateScope: AllComponents
  # replicas and template omitted
```

`spec.updateStrategy.updateScope` is optional. Omission preserves today's selective scope and does not enable the routing label. The first increment accepts the explicit value `AllComponents` only with `type: Coherent`; validation rejects it with `RollingRecreate`, `OnDelete`, or a defaulted `RollingRecreate` strategy. Unknown scope values are rejected. A larger scope enum can be considered separately.

The new API targets `grove.io/v1beta1`, introduced by the now-merged [#946](https://github.com/ai-dynamo/grove/pull/946). The still-served `v1alpha1` API must not silently erase the opt-in on a write. Implementation must settle and test a preservation or explicit-rejection policy at the conversion boundary before shipping; this proposal does not add the field to `v1alpha1`.

### Routing Label Contract

The proposed key is `grove.io/routing-revision`.

- It is present on newly created Pods whose selected materialization snapshot enables `AllComponents`, including initial deployment and later scale-out. It is absent for snapshots using the default scope.
- Its value identifies the selected persisted snapshot, not a clique hash, PCS generation, replica index, or the latest desired revision. It is shared across roles and PCS replicas that use that snapshot.
- Consumers treat it as an opaque equality token, scoped to the intended PodCliqueSet workload. They must not parse it, order it, or infer compatibility between unequal tokens.
- A candidate encoding is the UID of the ControllerRevision that represents the canonical materialization snapshot, provided it satisfies Kubernetes label-value constraints. If revision persistence creates a record for a change outside routing identity, that record must preserve the existing routing token. Reusing a retained materialization snapshot reuses its token. Recreating an equivalent snapshot after history has been collected may yield a new token.
- Grove sets the label at Pod creation and does not advance or remove it on a surviving Pod during rollout, bootstrap, disablement, or adoption. Changing PodGang membership is not evidence of a different materialization snapshot.
- The key is reserved: admission rejects user-supplied values in Pod label metadata propagated from a PodCliqueSet template. Unlabeled Pods are never treated as a shared "empty revision."

### llm-d Integration Contract

This contract targets REST inference and the two P/D deployments below. Grove owns the Pod label and its materialization identity. The llm-d overlay owns pool isolation, the request pin, and HTTP behavior.

#### Current Routing and Handoff

At llm-d-router commit `8837883`, the rollout screener counts Ready Pods by revision and role from scoped Pod notifications. It excludes unlabeled Pods from the count. `IsPodReady` excludes terminating Pods even while their Ready condition is true. The screener intersects revisions with nonzero shares with revisions present among request candidates. With `sum` or `max-role`, every required role must contribute at least one Ready Pod to a revision's share. The strict header restricts that eligible set by exact equality and bypasses a new draw, overflow and cross-replica coordination. `disabled` can enforce header equality alone and provides no all-role gate. Pod notifications and endpoint discovery are asynchronous. [Pod notifications](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/epp/framework/plugins/requesthandling/screener/disaggregatedsetrollout/plugin.go#L248-L283), [readiness](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/epp/util/pod/pod.go#L23-L35), [screening](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/epp/framework/plugins/requesthandling/screener/disaggregatedsetrollout/screener.go#L44-L112).

| Deployment | Where the choice lives and how it reaches decode |
| --- | --- |
| One EPP with P/D profiles and a decode sidecar | The EPP screens once before scheduling and passes the same screened candidates to both role profiles. The handler defaults to selecting decode first and also supports prefill-first scheduling. Both picks happen before prefill execution. The scheduling result carries the selected endpoints in process. `PreRequest` writes the selected prefill address as `x-llm-d-prefiller-host-port` for the decode sidecar, which runs the worker calls. There is no second EPP decision between execution stages. [Scheduler](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/epp/scheduling/scheduler.go#L78-L127), [profile handler](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/epp/framework/plugins/scheduling/profilehandler/disagg/disagg_profile_handler.go#L344-L375), [sidecar handoff](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/epp/framework/plugins/scheduling/profilehandler/disagg/disagg_profile_handler.go#L594-L616), [execution](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/docs/disaggregation.md#L105-L114). |
| Coordinator with separate prefill and decode scheduling calls | The prefill EPP stamps the chosen endpoint's label into the prefill response as `x-llm-d-disagg-revision: <token>`. After successful HTTP prefill, the coordinator captures it in the request's `RequestContext` and sends it as a request header on the decode POST, alongside `x-llm-d-epp-profile: decode`. The selected route and pool supply workload scope in the proposed overlay. Connector data, such as `kv_transfer_params`, is a separate handoff. The same wire contract applies when both phase calls target one EPP instance: these are still two scheduling calls. The default `pipeline.forward_response_headers` includes the default header. A custom `revisionHeaderName` must match on both EPPs and be in the coordinator's forwarding list. [Stamp](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/epp/framework/plugins/requesthandling/screener/disaggregatedsetrollout/plugin.go#L225-L234), [prefill](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/coordinator/steps/prefill.go#L102-L138), [forwarding](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/coordinator/pipeline/context.go#L83-L152), [decode POST](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/coordinator/steps/decode_proxy.go#L48-L79), [defaults](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/coordinator/config/config.go#L121-L129). |

#### Proposed Overlay Requirements

1. Scope both endpoint discovery and `scope.labelSelector` to the same PCS instance, using a pool and route bound to namespace and PCS UID or an equivalent immutable instance selector. A role-specific pool may advertise only that role's candidates. Each EPP's Pod observation must still cover **both** roles of the same instance. Configure the overlay's role label, `revisionGating.revisionLabelKey: grove.io/routing-revision`, `requiredRoles: [prefill, decode]`, and `sum` or `max-role`. Equal tokens from different workload instances cannot jointly satisfy readiness.
2. Reserve the revision header, phase header and revision decision ID for trusted internal traffic. Strip client copies at public ingress before route matching or screening and restrict stage routes to the coordinator or trusted gateway. Configure the gateway to fail closed if its EPP cannot make a decision. Strip the revision header on public egress as well. Today the coordinator skips original-request header names present in its forwarding list, and the screener reads the configured revision header directly. Generic response header capture can accept a missing value or overwrite an earlier value, and the response-strip list does not cover the rollout revision header. For the pinned route, accept exactly one nonempty token from the trusted successful prefill response. Bind it to the current request attempt and configured PCS instance, store it once, and fail closed on missing, ambiguous or conflicting data. Apply the same boundary to a custom revision header name. [Coordinator context](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/coordinator/pipeline/context.go#L40-L73), [header lookup](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/epp/framework/plugins/requesthandling/screener/disaggregatedsetrollout/screener.go#L163-L179), [response proxy](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/coordinator/steps/decode_proxy.go#L130-L140).
3. On a later decode pick, require the pin and revalidate the exact scoped revision against both role coverage and the decode picker's current Ready, non-terminating candidates. If the gate or role filter leaves no candidate, fail the attempt even if another revision is complete. The coordinator's separate decode pick may select another endpoint only within the pinned revision and only if the existing connector handoff can be used. For a single scheduling call, keep the selected pair in the scheduling result and check it before the gateway dispatches to the sidecar. In serial execution, after successful prefill and before any decode-side connector action or dispatch, the sidecar revalidates the selected decode Pod against a trusted observation. Require the original Pod UID and pinned revision, a Ready condition, and no termination. A Pod recreated under the same name with a different UID fails the check. If validation fails, end the attempt without repicking the pair. The source and freshness requirements for this observation remain open. If the worker disappears after a successful recheck and the connection fails, handle it as a transport failure.
4. Qualify the sequential coordinator route with the ordinary decode proxy and qualify serial sidecar execution. Keep conditional decode, force-stream fallback, sidecar prefill retries and gateway status-code retries off for initial qualification. Parallel dispatch needs its own qualification with the pair fixed before either leg starts. A conditional decode 412 can feed a revision into a later phase. The force-stream path can issue a second decode and drops one-shot KV handoffs when necessary. A configured serial sidecar can also retry a prefill 5xx against the same target. [Conditional decode](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/coordinator/steps/conditional_decode.go#L89-L109), [force-stream fallback](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/coordinator/steps/decode.go#L108-L167), [sidecar prefill retries](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/sidecar/proxy/connector_nixlv2.go#L345-L384).

#### Failure and Retry Behavior

| Failure before public response headers | Current REST path | Proposed contract |
| --- | --- | --- |
| No complete revision at initial selection, or the pinned revision has lost a required role | An empty screener result produces `errcommon.Error{Code: ServiceUnavailable}` and `x-llm-d-request-dropped-reason: rejected-no-endpoints`. Empty role-profile candidates also produce `ServiceUnavailable`. The EPP maps it to an Envoy ext_proc `ImmediateResponse` with HTTP 503 and an API error envelope. The ordinary decode reverse proxy relays the decode leg's 503. A coordinator prefill-leg 503 arrives as `pipeline.UpstreamError` and becomes HTTP 502. [Director](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/epp/requestcontrol/director.go#L362-L386), [role filter](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/epp/scheduling/scheduler_profile.go#L139-L148), [EPP mapping](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/common/error/error.go#L95-L149), [prefill status](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/coordinator/steps/utils.go#L62-L103), [coordinator mapping](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/coordinator/server/handlers.go#L290-L304). | The EPP emits `NoCompleteRevision` when initial admission finds no revision with both roles ready, or `PinnedRevisionUnavailable` when a later pinned decode pick finds that its revision has lost a required role. Carry the explicit reason through the trusted gateway and preserve it on the coordinator's prefill error path. Before public response headers are committed, map either confirmed EPP rejection to HTTP 503. A worker's ordinary 503 follows the existing upstream error path. Status alone cannot establish EPP provenance or authorize an automatic retry. |
| Missing or invalid trusted handoff after prefill | Generic header forwarding can leave decode unpinned if no prior step supplied the configured header. [Forwarding](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/coordinator/pipeline/context.go#L114-L151), [selection](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/epp/framework/plugins/requesthandling/screener/disaggregatedsetrollout/screener.go#L79-L110). | Stop before decode with an internal `InvalidRevisionHandoff` and return 502. Record the cause and request ID internally. |
| Pinned endpoint disappears after selection and the worker connection fails | The ordinary coordinator decode proxy writes 502 on a round-trip failure before upstream headers and returns `pipeline.UpstreamStreamedError` to prevent a second write. Sidecar failed peer calls can also use `errorBadGateway` (502). A gateway-generated error response already received by the decode proxy is relayed with its status. [Decode proxy](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/coordinator/steps/decode_proxy.go#L124-L170), [error ownership](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/coordinator/steps/decode_proxy.go#L201-L215), [sidecar](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/sidecar/proxy/errors.go#L57-L86). | Keep transport failure separate from a confirmed empty candidate set. Preserve the public gateway error and cancel or release what the connector can. Do not infer from 502 that the worker never ran. |

Use the existing JSON error format before commitment (OpenAI-compatible `error.type: server_error` with numeric HTTP code, or Messages `overloaded_error` for 503 and `api_error` for 502). After public headers are committed, even before the first SSE token, the status is fixed. The ordinary decode proxy aborts a failed body copy, so a streaming caller sees a truncated stream without a success terminator. An already committed ordinary JSON response may also truncate. A buffered non-streaming path can return a structured error while its public response is still uncommitted. Use an API-defined terminal stream error only if that path explicitly owns the framing. Never append a success terminator, restart a committed response or switch revisions. [Error envelopes](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/common/request/errors.go#L38-L89), [proxy abort](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/coordinator/steps/decode_proxy.go#L111-L123), [body copy](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/coordinator/steps/decode_proxy.go#L175-L198), [response already written](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/coordinator/server/handlers.go#L230-L246).

Allow a bounded **whole-request** automatic retry only before public commitment and with proof that no prior model stage was dispatched. A failed initial TCP connect to the coordinator before any request bytes could reach it is one example used by llm-d's E2E gateway config. A verified initial admission rejection can start a fresh attempt with a fresh decision and no inherited pin or KV metadata. Treat ambiguous resets, timeouts, any prefill execution, and any committed response as outside that retry budget. The client may submit a new request from prefill after failure with the usual duplicate-work risk. A pinned decode rejection, including a 503 after prefill, never authorizes replay of its decode body. [Gateway retry example](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/test/e2e/artifacts/environments/dev/coordinator-e2e-infra/envoy-3-epp.yaml#L170-L187), [one-shot handoffs](https://github.com/llm-d/llm-d-router/blob/883788387ac6d2b315621c9fc2acadacf076300a/pkg/coordinator/steps/decode.go#L190-L198).

### Snapshot Selection and Materialization

This feature depends on durable revision persistence, not merely on adding a label to the existing Pod builder. The required invariant is:

> The PodSpec, snapshot-derived metadata and injected configuration, and routing token must describe the same persisted materialization selection.

The snapshot payload must include the effective scope as well as the configuration that determines materialized workload identity. Canonicalization must distinguish scope-only activation from a no-op, while excluding scale-only changes and disruption-budget-only changes from routing identity. The implementation needs an explicit, tested field classification; hashing the entire mutable PCS object or every `spec.template` field is not sufficient. Live replica counts and operational planning controls must not replace the snapshot's versioned configuration.

During an active PCS replica's update, old and new cohorts can coexist. Selecting only the active replica's latest target is insufficient to identify every Pod being recreated. Each permitted materialization must resolve its intended snapshot durably, including across an R1/R2/R3 cascade and controller restart. A binding from replica and rollout cohort/epoch to snapshot is one possible representation, not a requirement on the current #912 API. Persist the binding before creating dependent resources, and retain referenced snapshots while any live cohort or pending recreation needs them.

If a controller observes an old PodClique alongside a new target, it must either build all snapshot-derived inputs from the selected snapshot or wait for the child configuration and its materialization identity to agree. Matching a clique template hash alone is insufficient for an unchanged clique forced through replacement. Missing, foreign-owned, or undecodable snapshots must cause a visible retry/error, never fallback to the latest desired snapshot with a plausible-looking label.

Inactive replicas recreate resources from their bound snapshot, not the active target. Within the active replica, existing suppression of standalone Pod creation on old-generation anchors remains intact: this proposal must not refill a draining anchor merely because an old snapshot is now available. Other recreation paths, including scaling-group members and ResourceClaims, must honor the same coherent selection rules.

### Rollout Identity and Completion

The all-component scope feeds the existing planner. It must also force replacement when a component's clique template hash is unchanged. Reaching the target requires Pods materialized at the target routing identity, not just the same PodSpec hash or updated child metadata.

Updated and updated-scheduled counts, disruption accounting, child convergence, and PCS completion must consistently distinguish the target materialization identity in this mode. Old Pods with an unchanged prefill template hash cannot count as updated. Retain the existing readiness checks at their respective orchestration layers; do not equate `Scheduled` with `Ready` or a complete router role set.

A scale-only edit does not initiate a new routing revision or force replacement. Direct scaling of PodClique and PodCliqueScalingGroup children remains blocked during a Coherent update, as specified by [GREP-393](../393-coherent-rolling-updates/README.md#handling-scale-outs-and-scale-ins-during-update). When existing rules allow scale-out, new Pods use the snapshot selected for their replica or cohort. Generation-based planning may remain internal, but it must not redefine the external routing token.

### Enabling and Disabling

An effective-scope change is accepted only at a converged boundary: the previously observed rollout has ended, all desired replicas have converged to their selected materialization state, and no earlier scope transition is pending. Admission and reconciliation must account for stale status and rapid successive writes rather than relying only on an `UpdateInProgress=False` observation. The precise persisted transition guard is a review item below.

- **Enable on an existing PCS:** changing from omitted scope to `AllComponents` creates/selects an enabled snapshot and triggers an all-component bootstrap rollout, even with no Pod template edit. Existing unlabeled Pods remain unlabeled until replaced.
- **Disable:** keep `type: Coherent` when removing `AllComponents`. The controller selects a default-scope snapshot and finishes one final all-component replacement. Old labeled Pods retain their tokens until deleted. New Pods are unlabeled. After convergence, subsequent updates resume selective scope and a strategy change can be requested.
- **During either transition:** keep the transition's replacement semantics until convergence; reject further scope/strategy switches. Corrective template edits within that scope remain possible. Do not enable the new scope and simultaneously switch to an unsupported strategy.

Before switching traffic to a route that requires the label, confirm that one labeled revision has Ready, non-terminating endpoints for every required role. Before disabling `AllComponents`, move traffic to a separate compatible pool or plan for a service interruption. During either transition, unlabeled Pods are ineligible for version-pinned routing.

### Corrective Edits and Retained Revisions

Continue accepting corrective template edits during an unsuccessful rollout without introducing a new rollback operation. A new target R3 must include every component, including still-pending R1 and partially materialized R2 cohorts. Their Pods keep their actual tokens until replacement; persisting R3 does not reclassify them.

Qualify this against the existing planner rather than assume it guarantees recovery: it gates progress on scheduling of the latest current-generation batch and charges in-flight replacements against disruption budgets. Tests must show how those gates behave when R2 is unscheduled or scheduled but unready, and when a further edit changes the target again. A corrective edit is not permission to bypass budgets or a guarantee that a capacity-starved rollout will make progress.

Restoring configuration equal to a retained R1 snapshot may reuse R1's token only when the canonical payload, including scope, matches. Newly created Pods actually use that snapshot; surviving R2/R3 Pods are not relabeled. If R1 was collected, a new snapshot/token is allowed. History collection must retain every revision still referenced by bindings, live cohorts, or unfinished work; exact retention mechanics belong with revision persistence.

### Monitoring

Use existing PCS `UpdateProgress`, component updated/scheduled/Ready counts, and update conditions to expose progress and stalls. Extend their accounting as described above rather than declaring completion from clique hash equality. Logs/events for unresolved materialization or blocked scope transitions should identify the PCS, replica/cohort, and selected revision, with a specific reason.

Users should be able to inspect Pod routing labels alongside readiness and role labels and compare them with the selected target snapshot. Exact additional status fields and event reason names require agreement with #941. Do not add a Prometheus time series per opaque revision token; router rejection/availability telemetry remains a data-plane responsibility.

### Dependencies and Review Decisions

Source review used Grove main [`dafab9c`](https://github.com/ai-dynamo/grove/tree/dafab9c1dcbc0313939251071634fc43b54e3f12) and [#912](https://github.com/ai-dynamo/grove/pull/912) at [`64fd661`](https://github.com/ai-dynamo/grove/tree/64fd66153854d310921012c303cdb097b52f92f9).

- Main's [`computeCoherentUpdateScope`](https://github.com/ai-dynamo/grove/blob/dafab9c1dcbc0313939251071634fc43b54e3f12/operator/internal/controller/podcliqueset/coherentupdatescope.go) selects changed clique hashes and carries forward pending scope. All-component inclusion is an extension.
- Main's [`mutateUpdatedReplica`](https://github.com/ai-dynamo/grove/blob/dafab9c1dcbc0313939251071634fc43b54e3f12/operator/internal/controller/podclique/reconcilestatus.go) counts updated Pods by clique template hash. Forced replacement of unchanged cliques needs additional identity accounting.
- #912 stores [PCS template snapshots](https://github.com/ai-dynamo/grove/blob/64fd66153854d310921012c303cdb097b52f92f9/operator/internal/utils/controllerrevision/revision.go). Its [standalone PodClique writer](https://github.com/ai-dynamo/grove/blob/64fd66153854d310921012c303cdb097b52f92f9/operator/internal/controller/podcliqueset/components/podclique/podclique.go) can advance the internal revision label when a clique's Pod template is unchanged. The [Pod builder](https://github.com/ai-dynamo/grove/blob/64fd66153854d310921012c303cdb097b52f92f9/operator/internal/controller/podclique/components/pod/pod.go) copies the child PodClique's PodSpec and explicitly removes that internal revision label. The proposed routing label therefore needs its own creation-time materialization identity, including for cliques whose template hash did not change.

Before implementation, maintainers need to agree on:

1. The field/key names, ControllerRevision UID token encoding, and reserved-label validation surface.
2. The durable materialization-selection interface with #941/#912, including active cohorts, history retention, and the canonical field classification that excludes scaling and budgets.
3. The persisted converged-boundary guard for scope transitions, including stale admission observations, and `v1alpha1` write/conversion behavior.
4. The overlay selector and trusted handoff mechanism, including how the gateway preserves authenticated EPP reason codes.
5. The trusted source for the serial sidecar's Pod observation and the maximum acceptable observation age or other freshness guarantee.

The [#947 discussion](https://github.com/ai-dynamo/grove/issues/947#issuecomment-6796887804) leaves llm-d router and Grove-overlay integration to a separate contribution after this contract is agreed. This GREP does not require changes to that router as part of the Grove controller implementation.

### Test Plan

All items below are acceptance criteria for future implementation, not tests added or executed by this proposal.

**Unit and controller tests:**

- Extend `operator/internal/controller/podcliqueset/coherentupdatescope_test.go`: omitted-scope regressions, decode-only edits including every standalone clique and scaling group, pending-scope carry-forward, and components at zero replicas.
- Test API validation/defaulting for each strategy, unknown values, reserved labels, scope-only activation, converged-boundary guards with stale status/rapid writes, and supported-version round trips that cannot silently drop the opt-in.
- Test snapshot canonicalization: template/materialization changes and scope toggles affect identity; scale-only edits, budget-only edits, and semantically equal defaults do not. Test retained-revision reuse and equivalent-payload recreation after GC.
- Extend Pod construction and `standalonepclqdistribution_test.go` coverage: shared tokens across roles, old child/new target races even with equal clique hashes, missing/foreign snapshots, active/inactive replica recreation, initial deployment, scale-out, and preserved suppression of old-anchor creates.
- Extend `reconcilestatus_test.go`, `coherentupdate_test.go`, and PodGangMap `substepplanner_test.go`: unchanged-template old Pods never count toward target completion, current-target scheduling counts are correct, and terminating/unready/in-flight replacements remain charged correctly.
- With revision persistence, exercise R1/R2/R3 cascades, restoration of retained R1, snapshot retention, restart between binding and Pod creation, and deletion/recreation of Pods, PodCliques, scaling groups, and ResourceClaims.

**Integration and scheduler E2E:**

Extend `operator/e2e/tests/update/coherentupdate_test.go` and the revision-persistence tests with a disaggregated workload containing both standalone and scaling-group roles. Verify Pod UIDs and actual configuration as well as labels: a decode-only edit replaces unchanged prefill, no old Pod is relabeled, and completion waits for target identity. Cover initial deployment, enable without a template change, disable through replacement, scale-out after convergence, rejection of direct child scaling during rollout, controller restart, and active/inactive-replica resource loss.

Exercise unschedulable and scheduled-but-unready targets, corrective edits through R1/R2/R3, and restoration of a retained revision. Verify budget invariants and visible stalls, without assuming corrective edits can always recover from insufficient capacity. Preserve omitted-scope, `RollingRecreate`, and `OnDelete` regression coverage.

The router integration should exercise both picker deployments with a single PCS workload and record the PCS identity, attempt ID, and actual prefill/decode Pod revisions. Every dispatched P/D pair must match. Inject the failures at the indicated boundaries and count worker calls.

| Scenario | Required result |
| --- | --- |
| R1 has Ready P and D. R2 has only Ready P. | Every new request uses R1 for both roles. |
| R1 has only Ready P. R2 has only Ready D. | Reject before worker dispatch with `NoCompleteRevision`. A second PCS with the same token cannot supply the missing role. A trusted EPP rejection on the coordinator's prefill error path retains its reason and maps to HTTP 503 before public headers. |
| Both revisions are complete and prefill uses R1. | The decode pick uses R1. A client-supplied pin or phase header cannot pick R2 or bypass the coordinator. |
| Remove R1's last Ready D after prefill while R2 is complete. | Reject the second EPP pick with `PinnedRevisionUnavailable` or fail the serial sidecar's final Pod recheck before any decode connector action. Assert zero decode dispatches and no automatic replay. A new request can use R2 starting at prefill. |
| A worker returns an ordinary HTTP 503. | Preserve the existing upstream error handling. The worker response carries no trusted EPP admission reason and does not qualify as evidence that prefill or decode was never dispatched. |
| A matched endpoint is Ready and terminating, or a Pod has no revision label during bootstrap. | Exclude it from coverage and dispatch. Begin pinned traffic when a complete labeled group is available. Include disablement cutover. |
| Drop, duplicate or change the trusted prefill pin, or recreate the PCS with the same name. | Reject an invalid or out-of-scope handoff before decode on the coordinator route. Reject a stale selected endpoint identity on the sidecar route. |
| Lose the selected worker after scheduling, before public headers, then repeat after headers and after the first SSE token. | Check structured 503 for confirmed admission rejection and 502 for the induced proxy transport failure. After commitment, check fixed status, incomplete response and no replay. Cover ordinary JSON and SSE. |
| Fail the initial connect before bytes are sent, then fail after prefill or with unknown worker outcome. | Only the proved pre-dispatch failure is eligible for a bounded fresh attempt. |

Track controller and router E2E work under #947 with dedicated follow-up test issues before implementation.

### Graduation Criteria

- **Alpha:** contract and revision interface agreed; opt-in API, controller accounting, transition guards, documentation, and unit/controller tests implemented; scheduler and router failure-path E2E demonstrate no cross-revision pairing.
- **Beta:** canonicalization and conversion behavior stable; restart/recreation, failed-target corrections, retention, and enable/disable verified in representative deployments; operational guidance covers incomplete role sets and availability costs.
- **GA:** at least two releases of beta feedback, stable label/API semantics, no unresolved identity or completion-accounting defects, and maintained upgrade and regression coverage.

## Alternatives

- **PCS generation label only:** it changes for non-version edits and does not replace unchanged roles, so it cannot provide a complete shared routing revision.
- **Relabel out-of-scope Pods:** a surviving prefill P1 may need to belong to both `R1={P1,D1}` and `R2={P1,D2}`. One equality label cannot express that contract safely; explicit membership is a separate design.
- **Always replace all components:** it removes the existing independent-update behavior and imposes restart costs on users who do not need version-pinned routing.
- **User-maintained revision labels:** usable today, but correctness depends on users consistently updating every incompatible role.

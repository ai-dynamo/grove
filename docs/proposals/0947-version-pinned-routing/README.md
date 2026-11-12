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

The [llm-d rollout screener](https://github.com/llm-d/llm-d-router/tree/647ca7cacd74de4fcc94b4717b72161de0f683b9/pkg/epp/framework/plugins/requesthandling/screener/disaggregatedsetrollout) supports configurable revision and role label keys. A Grove-owned label would remove the need for users to maintain matching revision labels manually. Its safety depends on the controller associating the label with the configuration actually used to create each Pod, rather than with the latest desired configuration.

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

For llm-d, the overlay enables revision gating (`sum` or `max-role`), sets `revisionGating.revisionLabelKey` to `grove.io/routing-revision`, sets `roleLabelKey` to the overlay's role label, and lists both `prefill` and `decode` in `requiredRoles`. Scope the endpoint pool and the screener's `scope.labelSelector` to the same PodCliqueSet workload. If prefill and decode use separate endpoint pickers (EPPs), forward the revision returned by prefill as the strict revision on the decode request. Reject the decode request if its pinned revision no longer has a Ready endpoint for each required role. For example, Ready prefill only on R1 and Ready decode only on R2 leave no eligible revision.

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

The separate router integration should drive requests across the rollout and record the selected prefill/decode revision pair. Every accepted pair must match. Verify rejection when neither revision has every required Ready role and when the forwarded revision loses a required role before decode. Include unlabeled Pods during bootstrap, terminating endpoints, and the documented disablement cutover. Track controller and router E2E work under #947 with dedicated follow-up test issues before implementation.

### Graduation Criteria

- **Alpha:** contract and revision interface agreed; opt-in API, controller accounting, transition guards, documentation, and unit/controller tests implemented; scheduler and router failure-path E2E demonstrate no cross-revision pairing.
- **Beta:** canonicalization and conversion behavior stable; restart/recreation, failed-target corrections, retention, and enable/disable verified in representative deployments; operational guidance covers incomplete role sets and availability costs.
- **GA:** at least two releases of beta feedback, stable label/API semantics, no unresolved identity or completion-accounting defects, and maintained upgrade and regression coverage.

## Alternatives

- **PCS generation label only:** it changes for non-version edits and does not replace unchanged roles, so it cannot provide a complete shared routing revision.
- **Relabel out-of-scope Pods:** a surviving prefill P1 may need to belong to both `R1={P1,D1}` and `R2={P1,D2}`. One equality label cannot express that contract safely; explicit membership is a separate design.
- **Always replace all components:** it removes the existing independent-update behavior and imposes restart costs on users who do not need version-pinned routing.
- **User-maintained revision labels:** usable today, but correctness depends on users consistently updating every incompatible role.

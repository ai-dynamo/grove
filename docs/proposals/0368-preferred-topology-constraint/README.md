# GREP-0368: Preferred Topology Constraint API

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Preferred host-level locality for a workload](#story-1-preferred-host-level-locality-for-a-workload)
    - [Story 2: Automated preferred topology for externally managed workloads](#story-2-automated-preferred-topology-for-externally-managed-workloads)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [API Change](#api-change)
  - [PodGang Propagation](#podgang-propagation)
  - [Webhook Validation](#webhook-validation)
  - [Monitoring](#monitoring)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
- [Alternatives](#alternatives)
  - [Always-on preferred topology at the lowest level](#always-on-preferred-topology-at-the-lowest-level)
  - [Boolean opt-in on <code>PodCliqueSet</code>](#boolean-opt-in-on-podcliqueset)
<!-- /toc -->

## Summary

Grove currently supports topology-aware scheduling through `packDomain`, a required constraint that leaves workloads pending if the requested topology cannot be satisfied. This GREP makes the API more explicit by deprecating `packDomain` in favor of `pack.required`, and by adding `pack.preferred`, a best-effort constraint that requests topology-aware placement without making it a hard requirement. If the preferred constraint cannot be met, the workload still schedules.

## Motivation

Grove is increasingly used as an orchestration layer by higher-level tools that manage workload submission on behalf of users. These tools often want to apply topology preferences as soft hints, without exposing scheduling decisions to end users, but cannot do so today because the only available topology constraint is required (`packDomain`).

Additionally, preferred topology scheduling is a compute-intensive operation for the scheduler. It therefore cannot be enabled by default for all workloads — it must be an explicit opt-in. Exposing a preferred constraint API gives tools and users a way to request best-effort topology placement, decoupled from the all-or-nothing semantics of the current required constraint. Grouping required and preferred packing under `pack` makes the API clearer when both modes are specified side by side.

### Goals

- Introduce a preferred (best-effort) topology constraint API on `PodClique`, `PodCliqueScalingGroup`, and `PodCliqueSet`
- Replace the required topology constraint field `packDomain` with `pack.required` while preserving existing workloads that already use `packDomain`
- Ensure workloads with a preferred constraint are never blocked — if topology cannot be satisfied, scheduling proceeds without it

### Non-Goals

- Automatically applying preferred topology to all workloads (it remains an explicit opt-in)
- Changing the semantics of the existing required constraint, which remains a hard scheduling requirement under `pack.required`
- Guaranteeing topology-optimal placement — preferred is best-effort only

## Proposal

This GREP proposes adding a nested `pack` field to `TopologyConstraint`, which is used by `PodClique`, `PodCliqueScalingGroup`, and `PodCliqueSet`. `pack.required` is the explicit replacement for the existing `packDomain` field and keeps the same hard scheduling semantics. `pack.preferred` signals to the scheduler that topology-aware placement is desired at the specified domain level on a best-effort basis — the workload is never blocked if the preferred constraint cannot be satisfied.

`pack.preferred` and `pack.required` can coexist on the same resource. For example, a user may set `pack.preferred=host` and `pack.required=rack` to express that host-level packing is desired but not required, while rack-level packing is mandatory. When both are set, the scheduler attempts preferred placement first and falls back toward the required level if the preferred constraint cannot be satisfied. It is recommended that `pack.preferred` be stricter than or equal to `pack.required`. Setting a coarser preferred level than the required one is semantically redundant, so admission allows it but returns a warning to give users feedback without blocking the workload.

The existing `packDomain` field is deprecated. It remains in the API only for compatibility with existing workloads that were created before this GREP is implemented. The operator continues to honor `packDomain` for those workloads as the required packing domain. New workload creation that uses `packDomain` is rejected; new workloads must use `pack.required` for hard placement requirements. Updates to existing workloads that still use `packDomain` are allowed when they do not otherwise violate immutability rules, but admission returns a warning directing users to `pack.required`. Users can suppress the warning by migrating from `packDomain` to `pack.required` in an update, but only when the topology domain value is unchanged.

### User Stories

#### Story 1: Preferred host-level locality for a workload

As an ML engineer submitting a workload with Grove, I want to set `pack.preferred: host` to signal that my pods benefit from being placed close together, so that Grove and the scheduler attempt host-level locality without blocking the workload when that placement is unavailable.

#### Story 2: Automated preferred topology for externally managed workloads

As a platform operator managing workloads over Grove, I want to automatically apply a preferred topology constraint to submitted workloads — without requiring end users to configure scheduling parameters — so that topology-aware placement is attempted as a best-effort optimization while ensuring no workload is ever blocked due to an unsatisfiable constraint.

### Limitations/Risks & Mitigations

- **Scheduler cost:** Preferred topology placement can be compute-intensive for scheduler backends. *Mitigation: preferred placement is explicit opt-in through `pack.preferred`; Grove does not apply it by default.*
- **API transition complexity:** Keeping deprecated `packDomain` for existing workloads while rejecting it for new workloads adds validation and controller complexity. *Mitigation: controller code resolves one effective required domain, and admission tests cover create, update, warning, same-value migration, and ambiguity cases.*
- **Admission warnings may be missed:** Some clients may not surface warnings for deprecated `packDomain` or redundant preferred domains. *Mitigation: warnings are advisory only; invalid or ambiguous configurations are still rejected.*

## Design Details

### API Change

The existing `TopologyConstraint` struct in the operator API (`operator/api/core/v1alpha1/podcliqueset.go`) is updated. The struct is already shared across `PodCliqueSet`, `PodCliqueTemplateSpec`, and `PodCliqueScalingGroupConfig`:

```go
// TopologyConstraint defines topology placement requirements.
type TopologyConstraint struct {
    // TopologyName is the name of the ClusterTopology resource to use for topology-aware scheduling.
    // Setting TopologyName is optional when the name can be inherited from a parent scope.
    // When TopologyName is specified at the PodCliqueSet or PodCliqueScalingGroup level,
    // it is inherited as the default ClusterTopology name by child constraints unless
    // the child specifies another TopologyName.
    // Immutable after creation.
    // +optional
    TopologyName string `json:"topologyName,omitempty"`

    // Pack specifies the topology packing constraints of each replica of the resource.
    // +optional
    Pack *TopologyPackConstraint `json:"pack,omitempty"`

    // PackDomain specifies the required topology domain using the legacy field name.
    // Deprecated: use Pack.RequiredDomain.
    // This field is honored for existing workloads, rejected on new workload
    // creation, and warned on update when still in use. Existing workloads may
    // migrate from PackDomain to Pack.RequiredDomain if the domain value is unchanged.
    // +optional
    PackDomain *TopologyDomain `json:"packDomain,omitempty"`
}

// TopologyPackConstraint defines topology pack placement requirements.
type TopologyPackConstraint struct {
    // RequiredDomain specifies the required topology packing constraint of each replica of the resource.
    // The workload will not be scheduled if this constraint cannot be satisfied.
    // Must reference a domain in the topology levels defined in the selected ClusterTopology.
    // Example: "rack" means each replica is independently placed within one rack.
    // Note: Does NOT constrain all replicas to the same rack together.
    // Different replicas can be in different topology domains.
    // +optional
    RequiredDomain  *TopologyDomain `json:"required,omitempty"`

    // PreferredDomain specifies a preferred (best-effort) topology domain.
    // If the constraint cannot be satisfied, the workload is scheduled anyway.
    // When set alongside RequiredDomain, it is recommended that PreferredDomain be stricter
    // than or equal to RequiredDomain.
    // +optional
    PreferredDomain *TopologyDomain `json:"preferred,omitempty"`
}
```

`TopologyName` remains part of `TopologyConstraint` and continues to select the `ClusterTopology` used for domain-to-key resolution, following the existing topology-name inheritance behavior. `Pack.RequiredDomain` preserves the required placement behavior of the legacy `PackDomain` field, but is optional so a workload can use `Pack.PreferredDomain` without a hard topology requirement. `Pack.PreferredDomain` is optional and can be used alone for preferred-only topology placement.

The legacy `PackDomain` field changes from a required value to an optional pointer and remains in the API only for compatibility with existing workloads. New workloads must use `Pack.RequiredDomain` for hard placement requirements; preferred-only workloads can set only `Pack.PreferredDomain`. `PackDomain` is honored for existing workloads, rejected on new workload creation, and warned on update when still in use. Existing workloads may move from `PackDomain` to `Pack.RequiredDomain` in an update if the domain value is identical; this migration is a field-name repair, not a semantic change. Controller code must resolve the effective required domain from `Pack.RequiredDomain` first, then fall back to deprecated `PackDomain` for existing workloads.

### PodGang Propagation

The scheduler API (`scheduler/api/core/v1alpha1/podgang.go`) already supports both constraints via `TopologyPackConstraint.Required` and `TopologyPackConstraint.Preferred`. No changes are needed on the scheduler side.

The required implementation change is in `createTopologyPackConstraint` (`operator/internal/controller/podcliqueset/components/podgang/syncflow.go`), which translates Grove's `TopologyConstraint` into the scheduler's `TopologyPackConstraint`. It currently populates `Required` from `packDomain`. It must instead resolve the effective required domain from `pack.required`, falling back to deprecated `packDomain` for existing workloads, and populate `Required` with the resolved topology key. It must also resolve `pack.preferred` to its topology key and populate `Preferred`.

The domain-to-key translation follows the same existing path: looking up the domain name in the `ClusterTopology` levels to obtain the corresponding node label key. Admission validation rejects invalid domains on create and update. If a previously valid required or preferred domain is no longer found during reconciliation because the referenced `ClusterTopology` changed after admission, the missing scheduler constraint is silently skipped without blocking workload creation (same behavior as the existing required constraint).

### Webhook Validation

The admission webhook (`operator/internal/webhook/admission/pcs/validation/topologyconstraints.go`) must be extended to validate the nested `pack` field and the deprecated `packDomain` compatibility path:

- **Pack completeness:** New topology constraints must set `pack` with at least one of `required` or `preferred`. An empty `pack` is rejected.
- **Domain existence:** Creation or update with a `pack.required` or `pack.preferred` value that does not exist in the resolved `ClusterTopology` CR is rejected at admission time, same as `packDomain` is today. Accepted legacy `packDomain` values on existing workloads continue to be validated against the same domain list on update.
- **Deprecated field on create:** New workload creation that sets `packDomain` is rejected. New workloads must use `pack.required` for hard placement requirements.
- **Deprecated field on update:** Existing workloads that already use `packDomain` remain valid and continue to use it as the required packing domain, but admission returns a warning on update while the deprecated field remains in use. The warning can be suppressed by removing `packDomain` and setting `pack.required` to the same domain value in the update.
- **Required domain migration:** Updating an existing workload from deprecated `packDomain` to `pack.required` is allowed only when the topology domain value is unchanged. Changing the required topology domain during this migration is rejected.
- **Required domain ambiguity:** A resulting `TopologyConstraint` must not set both `pack.required` and deprecated `packDomain`.
- **Hierarchy:** `pack.required` follows the same hierarchy rules as the existing required `packDomain`. `pack.preferred` on a child resource must be stricter than or equal to the parent's `pack.preferred`.
- **Same-resource preferred vs. required domain:** When `pack.preferred` is coarser than the effective required domain on the same resource, the request is allowed but admission returns a warning because the preferred domain is semantically redundant.
- **Update immutability:** `pack.required`, `pack.preferred`, and deprecated `packDomain` cannot be changed on update, matching the existing immutability behavior for `packDomain`, except for the same-value migration from deprecated `packDomain` to `pack.required`.

### Monitoring

There is no scheduler-side feedback on whether preferred placement was achieved. This is consistent with the existing behavior for required topology placement, which also has no placement-outcome indicator.

The existing `TopologyLevelUnavailable` status condition on `PodCliqueSet`, which checks whether referenced topology domains exist in the `ClusterTopology` CR, will be extended to cover `pack.required` and `pack.preferred` domains. For existing workloads that still use deprecated `packDomain`, the condition continues to treat `packDomain` as the effective required domain.

### Test Plan

**Unit — webhook validation:**
- `pack.required` and `pack.preferred` domain names not present in `ClusterTopology` are rejected
- New topology constraints with empty `pack` are rejected
- Creation with deprecated `packDomain` is rejected
- Updates to existing workloads that still use deprecated `packDomain` are allowed with an admission warning
- Updates that migrate from deprecated `packDomain` to `pack.required` with the same domain value are allowed without the deprecated-field warning
- Updates that migrate from deprecated `packDomain` to `pack.required` with a different domain value are rejected
- Updates that otherwise change `pack.required`, `pack.preferred`, or deprecated `packDomain` are rejected
- A resulting `TopologyConstraint` that sets both `pack.required` and deprecated `packDomain` is rejected
- `pack.preferred` stricter than parent is accepted; coarser than the parent's `pack.preferred` is rejected
- `pack.preferred` coarser than the effective required domain on the same resource is allowed with an admission warning
- `pack.preferred` can be set without `pack.required`, and vice versa

**Unit — syncflow (`TestComputeExpectedPodGangsWithTopologyConstraints`):**
- `pack.preferred` alone → `TopologyPackConstraint.Preferred` set, `Required` nil
- `pack.required` alone → `TopologyPackConstraint.Required` set, `Preferred` nil
- Deprecated `packDomain` on an existing workload → `TopologyPackConstraint.Required` set from `packDomain`
- `pack.required` and `pack.preferred` set together → both `Required` and `Preferred` populated correctly
- Previously valid `pack.required` or `pack.preferred` domain missing from `ClusterTopology` during reconciliation after a topology change → the missing scheduler constraint is silently skipped, workload still created

**E2E:**
- Preferred constraint that can be satisfied → workload schedules successfully and scheduler receives the preferred constraint (validates the full propagation path including the scheduler)
- Preferred constraint that cannot be satisfied → workload schedules successfully (key behavioral difference from required)
- Existing workload created with deprecated `packDomain` before upgrade continues to reconcile and propagate the required scheduler constraint after upgrade

### Graduation Criteria

#### Alpha
- `pack.required` and `pack.preferred` fields available on all three resources (`PodCliqueSet`, `PodCliqueScalingGroupConfig`, `PodCliqueTemplateSpec`)
- Deprecated `packDomain` remains honored for existing workloads and rejected for new workloads
- Unit and e2e tests passing

#### Beta
- Validated in at least one production workload
- No breaking API changes since alpha

#### GA
- Stable API
- No open issues related to the feature

## Alternatives

Two coarser-grained alternatives were considered and ruled out. Grove, as a generic infrastructure layer, should remain neutral — exposing fine-grained, explicit controls and leaving opinionated defaults to higher-level tools.

### Always-on preferred topology at the lowest level

The scheduler could automatically apply a preferred constraint at the finest available topology level for every workload, with no user opt-in.

- **Pro:** Zero configuration required.
- **Con:** Preferred topology scheduling is compute-intensive and unsuitable as a blanket default. Removes user control entirely, and is incompatible with workloads that have no topology preference.

### Boolean opt-in on `PodCliqueSet`

A single boolean field on `PodCliqueSet` (e.g. `preferredTopologyEnabled`) would enable preferred placement at the lowest topology level for all cliques, without allowing per-resource or per-level control.

- **Pro:** Simpler API surface.
- **Con:** Provides no control over which topology level is preferred, and cannot express different preferences across `PodCliqueSet`, `PodCliqueScalingGroup`, and `PodClique`. A literal boolean is also technically insufficient because the workload must still select a `ClusterTopology` through `topologyName`; this alternative would need a topology reference plus the boolean opt-in. Higher-level tools can trivially implement this coarser interface on top of `pack.preferred`, but the reverse is not true.

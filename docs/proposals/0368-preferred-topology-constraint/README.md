# GREP-0368: Preferred Topology Constraint API

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Automated preferred topology for externally managed workloads](#story-1-automated-preferred-topology-for-externally-managed-workloads)
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

Grove currently supports topology-aware scheduling through `packDomain`, a required constraint that leaves workloads pending if the requested topology cannot be satisfied. This GREP makes the API more explicit by renaming that field to `requiredPackDomain` and adding `preferredPackDomain`, a best-effort constraint that requests topology-aware placement without making it a hard requirement. If the preferred constraint cannot be met, the workload still schedules.

## Motivation

Grove is increasingly used as an orchestration layer by higher-level tools that manage workload submission on behalf of users. These tools often want to apply topology preferences automatically, without exposing scheduling decisions to end users, but cannot do so today because the only available topology constraint is required (`packDomain`).

Additionally, preferred topology scheduling is a compute-intensive operation for the scheduler. It therefore cannot be enabled by default for all workloads — it must be an explicit opt-in. Exposing a preferred constraint API gives tools and users a way to request best-effort topology placement, decoupled from the all-or-nothing semantics of the current required constraint. Renaming the required field from `packDomain` to `requiredPackDomain` makes the API clearer when required and preferred packing can be specified side by side.

### Goals

- Introduce a preferred (best-effort) topology constraint API on `PodClique`, `PodCliqueScalingGroup`, and `PodCliqueSet`
- Rename the required topology constraint field from `packDomain` to `requiredPackDomain` while preserving existing workloads that already use `packDomain`
- Ensure workloads with a preferred constraint are never blocked — if topology cannot be satisfied, scheduling proceeds without it

### Non-Goals

- Automatically applying preferred topology to all workloads (it remains an explicit opt-in)
- Changing the semantics of the existing required constraint, which remains a hard scheduling requirement under the new `requiredPackDomain` name
- Guaranteeing topology-optimal placement — preferred is best-effort only

## Proposal

This GREP proposes adding `requiredPackDomain` and `preferredPackDomain` fields to `TopologyConstraint`, which is used by `PodClique`, `PodCliqueScalingGroup`, and `PodCliqueSet`. `requiredPackDomain` is the explicit replacement for the existing `packDomain` field and keeps the same hard scheduling semantics. `preferredPackDomain` signals to the scheduler that topology-aware placement is desired at the specified domain level on a best-effort basis — the workload is never blocked if the preferred constraint cannot be satisfied.

`preferredPackDomain` and `requiredPackDomain` can coexist on the same resource. For example, a user may set `preferredPackDomain=host` and `requiredPackDomain=rack` to express that host-level packing is desired but not required, while rack-level packing is mandatory. When both are set, the scheduler attempts preferred placement first and falls back toward the required level if the preferred constraint cannot be satisfied. It is recommended that `preferredPackDomain` be stricter than or equal to `requiredPackDomain`. Setting a coarser preferred level than the required one is semantically redundant, so admission allows it but returns a warning to give users feedback without blocking the workload.

The existing `packDomain` field is deprecated. It remains in the API only for compatibility with existing workloads that were created before this GREP is implemented. The operator continues to honor `packDomain` for those workloads as the required packing domain. New workload creation that uses `packDomain` is rejected; new workloads must use `requiredPackDomain`. Updates to existing workloads that still use `packDomain` are allowed when they do not otherwise violate immutability rules, but admission returns a warning directing users to `requiredPackDomain` for future workloads.

### User Stories

#### Story 1: Automated preferred topology for externally managed workloads

As a platform operator managing workloads over Grove, I want to automatically apply a preferred topology constraint to submitted workloads — without requiring end users to configure scheduling parameters — so that topology-aware placement is attempted as a best-effort optimization while ensuring no workload is ever blocked due to an unsatisfiable constraint.

### Limitations/Risks & Mitigations

- **Scheduler cost:** Preferred topology placement can be compute-intensive for scheduler backends. *Mitigation: preferred placement is explicit opt-in through `preferredPackDomain`; Grove does not apply it by default.*
- **API transition complexity:** Keeping deprecated `packDomain` for existing workloads while rejecting it for new workloads adds validation and controller complexity. *Mitigation: controller code resolves one effective required domain, and admission tests cover create, update, warning, and ambiguity cases.*
- **Admission warnings may be missed:** Some clients may not surface warnings for deprecated `packDomain` or redundant preferred domains. *Mitigation: warnings are advisory only; invalid or ambiguous configurations are still rejected.*

## Design Details

### API Change

The existing `TopologyConstraint` struct in the operator API (`operator/api/core/v1alpha1/podcliqueset.go`) is updated. The struct is already shared across `PodCliqueSet`, `PodCliqueTemplateSpec`, and `PodCliqueScalingGroupConfig`:

```go
// TopologyConstraint defines topology placement requirements.
type TopologyConstraint struct {
    // RequiredPackDomain specifies the required topology domain for grouping replicas.
    // The workload will not be scheduled if this constraint cannot be satisfied.
    // +kubebuilder:validation:Enum=region;zone;datacenter;block;rack;host;numa
    // +optional
    RequiredPackDomain *TopologyDomain `json:"requiredPackDomain,omitempty"`

    // PreferredPackDomain specifies a preferred (best-effort) topology domain.
    // If the constraint cannot be satisfied, the workload is scheduled anyway.
    // When set alongside RequiredPackDomain, it is recommended that
    // PreferredPackDomain be stricter than or equal to RequiredPackDomain.
    // +kubebuilder:validation:Enum=region;zone;datacenter;block;rack;host;numa
    // +optional
    PreferredPackDomain *TopologyDomain `json:"preferredPackDomain,omitempty"`

    // PackDomain specifies the required topology domain using the legacy field name.
    // Deprecated: use RequiredPackDomain. This field is honored for existing
    // workloads, rejected on new workload creation, and warned on update when still in use.
    // +kubebuilder:validation:Enum=region;zone;datacenter;block;rack;host;numa
    // +optional
    PackDomain *TopologyDomain `json:"packDomain,omitempty"`
}
```

`RequiredPackDomain` preserves the required placement behavior of the legacy `PackDomain` field. `PreferredPackDomain` is optional and can be used alone for preferred-only topology placement. The legacy `PackDomain` field changes from a value type to an optional pointer so new workloads can omit it while existing workloads that already use it remain readable. Controller code must resolve the effective required domain from `RequiredPackDomain` first, then fall back to deprecated `PackDomain` for existing workloads.

### PodGang Propagation

The scheduler API (`scheduler/api/core/v1alpha1/podgang.go`) already supports both constraints via `TopologyPackConstraint.Required` and `TopologyPackConstraint.Preferred`. No changes are needed on the scheduler side.

The required implementation change is in `createTopologyPackConstraint` (`operator/internal/controller/podcliqueset/components/podgang/syncflow.go`), which translates Grove's `TopologyConstraint` into the scheduler's `TopologyPackConstraint`. It currently populates `Required` from `packDomain`. It must instead resolve the effective required domain from `requiredPackDomain`, falling back to deprecated `packDomain` for existing workloads, and populate `Required` with the resolved topology key. It must also resolve `preferredPackDomain` to its topology key and populate `Preferred`.

The domain-to-key translation follows the same existing path: looking up the domain name in the `ClusterTopology` levels to obtain the corresponding node label key. Admission validation rejects invalid domains on create and update. If a previously valid required or preferred domain is no longer found during reconciliation because the referenced `ClusterTopology` changed after admission, the missing scheduler constraint is silently skipped without blocking workload creation (same behavior as the existing required constraint).

### Webhook Validation

The admission webhook (`operator/internal/webhook/admission/pcs/validation/topologyconstraints.go`) must be extended to validate the new field names and the deprecated `packDomain` compatibility path:

- **Domain existence:** Creation or update with a `requiredPackDomain` or `preferredPackDomain` value that does not exist in the referenced `ClusterTopology` CR is rejected at admission time, same as `packDomain` is today. Accepted legacy `packDomain` values on existing workloads continue to be validated against the same domain list on update.
- **Deprecated field on create:** New workload creation that sets `packDomain` is rejected. New workloads must use `requiredPackDomain`.
- **Deprecated field on update:** Existing workloads that already use `packDomain` remain valid and continue to use it as the required packing domain, but admission returns a warning on update while the deprecated field remains in use.
- **Required domain ambiguity:** A single `TopologyConstraint` must not set both `requiredPackDomain` and deprecated `packDomain`.
- **Hierarchy:** `requiredPackDomain` follows the same hierarchy rules as the existing required `packDomain`. `preferredPackDomain` on a child resource must be stricter than or equal to the parent's `preferredPackDomain`.
- **Same-resource preferred vs. required domain:** When `preferredPackDomain` is coarser than the effective required domain on the same resource, the request is allowed but admission returns a warning because the preferred domain is semantically redundant.
- **Update immutability:** `requiredPackDomain`, `preferredPackDomain`, and deprecated `packDomain` cannot be changed on update, matching the existing immutability behavior for `packDomain`.

### Monitoring

There is no scheduler-side feedback on whether preferred placement was achieved. This is consistent with the existing behavior for required topology placement, which also has no placement-outcome indicator.

The existing `TopologyLevelUnavailable` status condition on `PodCliqueSet`, which checks whether referenced topology domains exist in the `ClusterTopology` CR, will be extended to cover `requiredPackDomain` and `preferredPackDomain` domains. For existing workloads that still use deprecated `packDomain`, the condition continues to treat `packDomain` as the effective required domain.

### Test Plan

**Unit — webhook validation:**
- `requiredPackDomain` and `preferredPackDomain` domain names not present in `ClusterTopology` are rejected
- Creation with deprecated `packDomain` is rejected
- Updates to existing workloads that still use deprecated `packDomain` are allowed with an admission warning
- Updates that change `requiredPackDomain`, `preferredPackDomain`, or deprecated `packDomain` are rejected
- A single `TopologyConstraint` that sets both `requiredPackDomain` and deprecated `packDomain` is rejected
- `preferredPackDomain` stricter than parent is accepted; coarser than the parent's `preferredPackDomain` is rejected
- `preferredPackDomain` coarser than the effective required domain on the same resource is allowed with an admission warning
- `preferredPackDomain` can be set without `requiredPackDomain`, and vice versa

**Unit — syncflow (`TestComputeExpectedPodGangsWithTopologyConstraints`):**
- `preferredPackDomain` alone → `TopologyPackConstraint.Preferred` set, `Required` nil
- `requiredPackDomain` alone → `TopologyPackConstraint.Required` set, `Preferred` nil
- Deprecated `packDomain` on an existing workload → `TopologyPackConstraint.Required` set from `packDomain`
- `requiredPackDomain` and `preferredPackDomain` set together → both `Required` and `Preferred` populated correctly
- Previously valid `requiredPackDomain` or `preferredPackDomain` domain missing from `ClusterTopology` during reconciliation after a topology change → the missing scheduler constraint is silently skipped, workload still created

**E2E:**
- Preferred constraint that can be satisfied → workload schedules successfully and scheduler receives the preferred constraint (validates the full propagation path including the scheduler)
- Preferred constraint that cannot be satisfied → workload schedules successfully (key behavioral difference from required)
- Existing workload created with deprecated `packDomain` before upgrade continues to reconcile and propagate the required scheduler constraint after upgrade

### Graduation Criteria

#### Alpha
- `requiredPackDomain` and `preferredPackDomain` fields available on all three resources (`PodCliqueSet`, `PodCliqueScalingGroupConfig`, `PodCliqueTemplateSpec`)
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
- **Con:** Provides no control over which topology level is preferred, and cannot express different preferences across `PodCliqueSet`, `PodCliqueScalingGroup`, and `PodClique`. Higher-level tools can trivially implement this coarser interface on top of `preferredPackDomain`, but the reverse is not true.

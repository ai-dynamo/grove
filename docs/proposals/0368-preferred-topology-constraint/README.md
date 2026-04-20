# GREP-0368: Preferred Topology Constraint API

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Automated preferred topology in Dynamo on Run:ai](#story-1-automated-preferred-topology-in-dynamo-on-runai)
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

Grove currently supports topology-aware scheduling through a required constraint (`packDomain`), which blocks the workload pending resources if the requested topology cannot be satisfied. This GREP introduces a **preferred** topology constraint — a best-effort mode that requests topology-aware placement without making it a hard requirement. If the constraint cannot be met, the workload is still scheduled rather than remaining blocked, making topology optimization accessible in scenarios where strict placement is undesirable or incompatible with the scheduler.

## Motivation

Grove is increasingly used as an orchestration layer by higher-level tools (e.g. Dynamo on Run:ai) that manage workload submission on behalf of users. These tools often want to apply topology preferences automatically — without exposing scheduling decisions to end users — but cannot do so today because the only available topology constraint is required (`packDomain`), which risks blocking workloads indefinitely when the topology cannot be satisfied.

Additionally, preferred topology scheduling is a compute-intensive operation for the scheduler. It therefore cannot be enabled by default for all workloads — it must be an explicit opt-in. Exposing a preferred constraint API gives tools and users a way to request best-effort topology placement, decoupled from the all-or-nothing semantics of the current required constraint.

### Goals

- Introduce a preferred (best-effort) topology constraint API on `PodClique`, `PodGangScalingGroup`, and `PodCliqueSet`
- Ensure workloads with a preferred constraint are never blocked — if topology cannot be satisfied, scheduling proceeds without it

### Non-Goals

- Automatically applying preferred topology to all workloads (it remains an explicit opt-in)
- Changing the semantics of the existing required constraint (`packDomain`)
- Guaranteeing topology-optimal placement — preferred is best-effort only

## Proposal

This GREP proposes adding a `packDomainPreferred` field alongside the existing `packDomain` field on `PodClique`, `PodGangScalingGroup`, and `PodCliqueSet`. When set, it signals to the scheduler that topology-aware placement is desired at the specified domain level on a best-effort basis — the workload is never blocked if the constraint cannot be satisfied.

`packDomainPreferred` and `packDomain` can coexist on the same resource. For example, a user may set `packDomainPreferred=host` and `packDomain=rack` to express that host-level packing is desired but not required, while rack-level packing is mandatory. When both are set, the scheduler attempts preferred placement first and falls back toward the required level if the preferred constraint cannot be satisfied. It is recommended (but not enforced) that `packDomainPreferred` be stricter than or equal to `packDomain` — setting a coarser preferred level than the required one is semantically redundant.

### User Stories

#### Story 1: Automated preferred topology in Dynamo on Run:ai

As a **Run:ai platform operator** deploying Dynamo workloads over Grove, I want to automatically apply a preferred topology constraint to all submitted workloads — without requiring end users to configure scheduling parameters — so that topology-aware placement is attempted as a best-effort optimization while ensuring no workload is ever blocked due to an unsatisfiable constraint.

### Limitations/Risks & Mitigations

- **Scheduler performance:** `packDomainPreferred` triggers a scheduling algorithm that might be compute-intensive. Misuse (e.g. setting it on all workloads at scale) could degrade scheduler performance. *Mitigation: it's an explicit opt-in, never a default.*
- **Interaction with `packDomain`:** The semantics when both fields are set need to be well-defined.

## Design Details

### API Change

A single field is added to the existing `TopologyConstraint` struct in the operator API (`operator/api/core/v1alpha1/podcliqueset.go`), which is already shared across `PodCliqueSet`, `PodCliqueTemplateSpec`, and `PodCliqueScalingGroupConfig`:

```go
// TopologyConstraint defines topology placement requirements.
type TopologyConstraint struct {
    // PackDomain specifies the required topology domain for grouping replicas.
    // The workload will not be scheduled if this constraint cannot be satisfied.
    // +kubebuilder:validation:Enum=region;zone;datacenter;block;rack;host;numa
    // +optional
    PackDomain *TopologyDomain `json:"packDomain,omitempty"`

    // PackDomainPreferred specifies a preferred (best-effort) topology domain.
    // If the constraint cannot be satisfied, the workload is scheduled anyway.
    // When set alongside PackDomain, it is recommended that PackDomainPreferred
    // be stricter than or equal to PackDomain.
    // +kubebuilder:validation:Enum=region;zone;datacenter;block;rack;host;numa
    // +optional
    PackDomainPreferred *TopologyDomain `json:"packDomainPreferred,omitempty"`
}
```

Note: `PackDomain` changes from a value type to a pointer (`*TopologyDomain`) to allow a `TopologyConstraint` with only `PackDomainPreferred` set. This is not a breaking API change — existing resources with `packDomain` set continue to work. Controller code accessing `PackDomain` will need nil checks.

### PodGang Propagation

The scheduler API (`scheduler/api/core/v1alpha1/podgang.go`) already supports both constraints via `TopologyPackConstraint.Required` and `TopologyPackConstraint.Preferred`. No changes are needed on the scheduler side.

The only required implementation change is in `createTopologyPackConstraint` (`operator/internal/controller/podcliqueset/components/podgang/syncflow.go`), which translates Grove's `TopologyConstraint` into the scheduler's `TopologyPackConstraint`. It currently only populates `Required`. It must be extended to also resolve `PackDomainPreferred` to its topology key and populate `Preferred`.

The domain-to-key translation follows the same existing path: looking up the domain name in the `ClusterTopology` levels to obtain the corresponding node label key. If the preferred domain is not found in the current `ClusterTopology`, it is silently skipped (same behavior as the existing required constraint).

### Webhook Validation

The admission webhook (`operator/internal/webhook/admission/pcs/validation/topologyconstraints.go`) must be extended to cover `PackDomainPreferred` with the same rules that apply to `PackDomain`:

- **Domain existence:** A `packDomainPreferred` value that does not exist in the `ClusterTopology` CR is rejected at admission time, same as `packDomain`.
- **Hierarchy:** `packDomainPreferred` on a child resource must be stricter than or equal to the parent's `packDomainPreferred`.

### Monitoring

There is no scheduler-side feedback on whether preferred placement was achieved. This is consistent with the existing behavior for `packDomain`, which also has no placement-outcome indicator.

The existing `TopologyLevelUnavailable` status condition on `PodCliqueSet`, which checks whether referenced topology domains exist in the `ClusterTopology` CR, will be extended to also cover `packDomainPreferred` domains.

### Test Plan

**Unit — webhook validation:**
- `packDomainPreferred` domain not present in `ClusterTopology` is rejected
- `packDomainPreferred` stricter than parent is accepted; coarser passes without enforcement
- `packDomainPreferred` can be set without `packDomain`, and vice versa

**Unit — syncflow (`TestComputeExpectedPodGangsWithTopologyConstraints`):**
- `packDomainPreferred` alone → `TopologyPackConstraint.Preferred` set, `Required` nil
- Both fields set → both `Preferred` and `Required` populated correctly
- `packDomainPreferred` domain missing from `ClusterTopology` → `Preferred` silently skipped, workload still created

**E2E:**
- Preferred constraint that can be satisfied → workload schedules successfully and scheduler receives the preferred constraint (validates the full propagation path including the scheduler)
- Preferred constraint that cannot be satisfied → workload schedules successfully (key behavioral difference from required)

### Graduation Criteria

#### Alpha
- `packDomainPreferred` field available on all three resources (`PodCliqueSet`, `PodCliqueScalingGroupConfig`, `PodCliqueTemplateSpec`)
- Unit and e2e tests passing

#### Beta
- Validated in at least one production workload (e.g. Dynamo on Run:ai)
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
- **Con:** Provides no control over which topology level is preferred, and cannot express different preferences across `PodCliqueSet`, `PodCliqueScalingGroup`, and `PodClique`. Higher-level tools can trivially implement this coarser interface on top of `packDomainPreferred`, but the reverse is not true.

# GREP-0368: Preferred Topology Constraint API

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Topology-aware placement with graceful fallback](#story-1-topology-aware-placement-with-graceful-fallback)
    - [Story 2: Mixed required and preferred constraints for disaggregated inference](#story-2-mixed-required-and-preferred-constraints-for-disaggregated-inference)
    - [Story 3: Admin-controlled preferred topology](#story-3-admin-controlled-preferred-topology)
    - [Story 4: External orchestration platform](#story-4-external-orchestration-platform)
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
  - [Workload-level preferred domain](#workload-level-preferred-domain)
  - [Boolean opt-in on <code>PodCliqueSet</code>](#boolean-opt-in-on-podcliqueset)
<!-- /toc -->

## Summary

Grove topology-aware scheduling currently supports required packing through `packDomain`, which can leave workloads pending when the requested topology domain cannot fit them. This GREP introduces `ClusterTopology.spec.preferredPackDomain`, an administrator-controlled preferred packing domain that Grove applies as a best-effort scheduler preference to workloads that reference that `ClusterTopology`. Workloads can opt into topology-aware placement by setting `topologyName` with or without a required `packDomain`; when the referenced topology defines `preferredPackDomain`, Grove propagates it to the scheduler as a preferred constraint so placement can be optimized without blocking scheduling if the preference cannot be satisfied.

## Motivation

Grove is increasingly used as an orchestration layer by higher-level tools (e.g. Dynamo on Run:ai) that submit workloads on behalf of users. These tools often want topology-aware placement to improve workload performance, but the existing workload-facing constraint, `packDomain`, is required: if the requested domain cannot fit the workload, scheduling may remain blocked.

Preferred topology placement addresses this gap by letting the scheduler try for a more optimized placement while still allowing the workload to run when the preference cannot be satisfied. This is useful for platforms that want to apply topology-aware optimization transparently, and for workloads that benefit from locality but should not fail or remain pending solely because the most optimized placement is unavailable.

At the same time, preferred topology scheduling is a compute-intensive scheduler operation. Grove should not simply enable preferred placement by default at the lowest topology level for every workload, because doing so could impose scheduler cost on workloads that did not opt into topology-aware scheduling. Instead, this GREP makes preferred placement an explicit topology policy: administrators configure the preferred domain on `ClusterTopology`, and workloads opt into that topology by setting `topologyName`.

### Goals

- Add `preferredPackDomain` to `ClusterTopology.spec` as the administrator-configured best-effort topology packing domain.
- Validate that `preferredPackDomain`, when set, refers to a domain defined in the same `ClusterTopology.spec.levels` list.
- Allow workloads to set `topologyName` without `packDomain`, enabling preferred-only topology-aware scheduling when the referenced `ClusterTopology` defines `preferredPackDomain`.
- Propagate the resolved preferred topology key to the scheduler API through `TopologyPackConstraint.Preferred`.
- Preserve the existing required `packDomain` behavior and allow required and preferred constraints to be used together.

### Non-Goals

- Automatically applying preferred topology to workloads that do not reference a `ClusterTopology`.
- Selecting the preferred topology domain independently on each workload resource.
- Changing the hard scheduling semantics of `packDomain`.
- Guaranteeing topology-optimal placement. Preferred topology placement is best-effort only.
- Changing the scheduler API. The scheduler already exposes `TopologyPackConstraint.Preferred`.

## Proposal

This GREP proposes adding `preferredPackDomain` to `ClusterTopology.spec`. The field identifies the topology domain that Grove should use as the scheduler's preferred packing domain for workloads that opt into that topology.

A workload opts into topology-aware scheduling by setting `topologyName` in its `TopologyConstraint`. The workload may also set `packDomain` when it needs a hard placement requirement. If `packDomain` is omitted, Grove may still emit a preferred scheduler constraint if the referenced `ClusterTopology` defines `preferredPackDomain`.

The resulting behavior is:

- Workload has no `topologyName`: Grove emits no topology constraint for that workload.
- Workload has `topologyName`, and the referenced `ClusterTopology` has no `preferredPackDomain`: existing required `packDomain` behavior is preserved.
- Workload has `topologyName`, and the referenced `ClusterTopology` defines `preferredPackDomain`: Grove resolves that domain to its topology key and emits it as `TopologyPackConstraint.Preferred`.
- Workload has both `topologyName` and `packDomain`: Grove emits `packDomain` as `TopologyPackConstraint.Required` and, if configured on the topology, emits `preferredPackDomain` as `TopologyPackConstraint.Preferred`.

For example, an administrator can configure host-level preferred packing for a topology:

```yaml
apiVersion: grove.io/v1alpha1
kind: ClusterTopology
metadata:
  name: h100-topology
spec:
  preferredPackDomain: host
  levels:
    - domain: zone
      key: topology.kubernetes.io/zone
    - domain: rack
      key: topology.kubernetes.io/rack
    - domain: host
      key: kubernetes.io/hostname
```

A workload can then opt into preferred-only topology-aware placement:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: prefill
spec:
  template:
    topologyConstraint:
      topologyName: h100-topology
    cliques:
      - name: worker
        spec:
          replicas: 8
          template:
            spec:
              containers:
                - name: worker
                  image: example.com/worker:latest
```

The same workload can add a required rack-level constraint while retaining the topology's host-level preferred constraint:

```yaml
spec:
  template:
    topologyConstraint:
      topologyName: h100-topology
      packDomain: rack
```

### User Stories

#### Story 1: Topology-aware placement with graceful fallback

As an ML engineer, I want to request topology-aware placement for my workload so that Grove attempts optimized placement without failing the job if the preferred topology domain cannot fit it.

#### Story 2: Mixed required and preferred constraints for disaggregated inference

As an ML engineer running disaggregated inference, I want to require that each PodCliqueScalingGroup is packed within a rack while preferring that scheduling uses a narrower topology domain when possible, so that critical locality is guaranteed while additional optimization remains best-effort.

#### Story 3: Admin-controlled preferred topology

As a cluster administrator, I want to configure the preferred packing domain on each `ClusterTopology` so that workloads using that topology receive consistent best-effort placement behavior without each workload choosing infrastructure-specific policy.

#### Story 4: External orchestration platform

As an orchestration platform, such as Dynamo on Run:ai, I want to set `topologyName` when submitting Grove workloads so that topology-aware placement is attempted transparently for users while preserving scheduling fallback behavior.

### Limitations/Risks & Mitigations

- **Scheduler performance:** Preferred topology placement can trigger compute-intensive scheduler work. *Mitigation: preferred placement is only emitted for workloads that set `topologyName` and reference a `ClusterTopology` that defines `preferredPackDomain`; it is not a blanket default for all workloads.*
- **Cluster-wide policy for a topology:** A `preferredPackDomain` applies to all workloads that reference the `ClusterTopology`, which may be too broad for clusters with different workload classes. *Mitigation: administrators can define separate `ClusterTopology` resources with different preferred packing policies and direct workloads to the appropriate topology via `topologyName`.*
- **Misconfigured preferred domain:** If `preferredPackDomain` does not match a configured topology level, Grove cannot resolve it to a scheduler topology key. *Mitigation: admission validation rejects `ClusterTopology` resources where `preferredPackDomain` is not present in `spec.levels[*].domain`.*
- **Preference is not a guarantee:** A workload may still run outside the preferred domain if the scheduler cannot satisfy the preferred placement. *Mitigation: users must continue to use `packDomain` for hard placement requirements.*

## Design Details

### API Change

This GREP builds on the `ClusterTopology` and `PodCliqueSet` topology APIs where workload `TopologyConstraint` selects a topology with `topologyName` and may specify a required domain with `packDomain`. A single field is added to `ClusterTopologySpec` in `operator/api/core/v1alpha1/clustertopology.go`:

```go
// ClusterTopologySpec defines the topology hierarchy specification.
type ClusterTopologySpec struct {
    // Levels is an ordered list of topology levels from broadest to narrowest scope.
    // The order in this list defines the hierarchy (index 0 = broadest level).
    // +kubebuilder:validation:MinItems=1
    Levels []TopologyLevel `json:"levels"`

    // PreferredPackDomain specifies the topology domain Grove should propagate
    // to the scheduler as a best-effort preferred packing constraint for workloads
    // that reference this ClusterTopology.
    // Must match one of the domains in Levels.
    // +optional
    PreferredPackDomain *TopologyDomain `json:"preferredPackDomain,omitempty"`

    // SchedulerReferences controls per-backend topology resource management.
    // +optional
    SchedulerReferences []SchedulerReference `json:"schedulerReferences,omitempty"`
}
```

The workload-facing `TopologyConstraint` continues to use `topologyName` to select the `ClusterTopology` and `packDomain` to express a hard packing requirement. `packDomain` remains optional, so a workload can set `topologyName` without requiring a hard packing domain.

### PodGang Propagation

The scheduler API (`scheduler/api/core/v1alpha1/podgang.go`) already supports both constraints via `TopologyPackConstraint.Required` and `TopologyPackConstraint.Preferred`. No scheduler API change is needed.

The PodCliqueSet PodGang sync flow will load the referenced `ClusterTopology` for the workload's `topologyName`. In addition to the topology levels, the sync context will retain the resolved topology key for `ClusterTopology.spec.preferredPackDomain` when it is configured.

`createTopologyPackConstraint` (`operator/internal/controller/podcliqueset/components/podgang/syncflow.go`) will populate scheduler constraints as follows:

- If the workload `TopologyConstraint.packDomain` is non-empty, resolve it through `ClusterTopology.spec.levels` and set `TopologyPackConstraint.Required`.
- If the selected `ClusterTopology.spec.preferredPackDomain` is set, resolve it through `ClusterTopology.spec.levels` and set `TopologyPackConstraint.Preferred`.
- If neither required nor preferred can be resolved, omit the scheduler topology constraint.

This logic applies wherever Grove emits scheduler topology constraints: the PodGang-level constraint, PodCliqueScalingGroup `TopologyConstraintGroupConfig` constraints, and PodGroup constraints for PodClique-level topology constraints.

### Webhook Validation

ClusterTopology admission validation must reject resources where `preferredPackDomain` is set but does not match any `domain` in `spec.levels`.

PodCliqueSet admission validation must continue to validate workload `packDomain` values against the referenced `ClusterTopology` and enforce the existing hierarchy rules across PodCliqueSet, PodCliqueScalingGroup, and PodClique constraints. Empty `packDomain` is valid when `topologyName` is set. When topology-aware scheduling is disabled, topology constraints remain disallowed, including constraints that only set `topologyName`.

### Monitoring

There is no dedicated status condition that reports whether preferred placement was achieved. Operators can verify propagation by inspecting the generated `PodGang` resources and checking `spec.topologyConstraint.packConstraint.preferred`, group config constraints, or PodGroup constraints as appropriate.

Existing topology health signals remain relevant:

- `ClusterTopology.status.schedulerTopologyStatuses` and the `SchedulerTopologyDrift` condition report whether scheduler backend topology resources are in sync with Grove topology definitions.
- `PodCliqueSet` topology availability conditions continue to report unavailable required topology levels for workload constraints.
- Scheduler-specific placement quality signals, such as `PodGang.status.placementScore` when populated by the scheduler, can help diagnose placement quality, but they are not a hard success/failure signal for preferred placement.

### Test Plan

**Unit - webhook validation:**
- `ClusterTopology.spec.preferredPackDomain` is accepted when it matches a domain in `spec.levels`
- `ClusterTopology.spec.preferredPackDomain` is rejected when it does not match a domain in `spec.levels`
- `PodCliqueSet` with `topologyName` and no `packDomain` is accepted when topology-aware scheduling is enabled
- `PodCliqueSet` with `topologyName` and no `packDomain` is rejected when topology-aware scheduling is disabled
- Existing `packDomain` domain-existence and hierarchy validation continues to pass and fail as expected

**Unit - syncflow (`TestComputeExpectedPodGangsWithTopologyConstraints`):**
- `topologyName` with `ClusterTopology.spec.preferredPackDomain` and no `packDomain` creates a preferred-only scheduler constraint
- `topologyName` with both `ClusterTopology.spec.preferredPackDomain` and workload `packDomain` creates both `Preferred` and `Required`
- `topologyName` with no `ClusterTopology.spec.preferredPackDomain` preserves required-only behavior
- `ClusterTopology.spec.preferredPackDomain` is propagated to PodGang-level, PodCliqueScalingGroup group, and PodClique PodGroup constraints where Grove emits scheduler topology constraints
- Missing or unresolved preferred domain at reconciliation time is skipped without blocking workload creation

**E2E:**
- Preferred-only workload schedules successfully and the scheduler receives the preferred constraint
- Workload with both required and preferred constraints schedules successfully when the required constraint is satisfiable
- Workload remains schedulable when the preferred domain cannot be satisfied, demonstrating the best-effort behavior
- Required `packDomain` behavior remains unchanged: unsatisfiable required constraints can still keep the workload pending

### Graduation Criteria

#### Alpha
- `ClusterTopology.spec.preferredPackDomain` is available in the v1alpha1 API
- Admission validation and PodGang propagation are implemented
- Unit and e2e tests pass

#### Beta
- Validated in at least one production workload or platform integration, such as Dynamo on Run:ai
- No breaking API changes since alpha
- No significant scheduler performance regressions observed from opted-in workloads

#### GA
- Stable API
- Documentation covers admin configuration and workload opt-in behavior
- No open issues related to preferred topology propagation or validation for at least two releases after beta

## Alternatives

The following alternatives were considered and ruled out.

### Always-on preferred topology at the lowest level

The scheduler could automatically apply a preferred constraint at the finest available topology level for every workload, with no user opt-in.

- **Pro:** Zero configuration required.
- **Con:** Preferred topology scheduling is compute-intensive and unsuitable as a blanket default. It would impose scheduling cost on workloads that did not request topology-aware placement.

### Workload-level preferred domain

A workload-facing field could let users select the preferred domain directly for each PodCliqueSet, PodCliqueScalingGroup, or PodClique.

- **Pro:** Gives workload authors fine-grained control.
- **Con:** Pushes infrastructure policy into workload manifests and makes it harder for administrators to tune preferred topology behavior consistently across a cluster.

### Boolean opt-in on `PodCliqueSet`

A boolean field on `PodCliqueSet` could enable preferred placement without requiring the workload to choose a domain.

- **Pro:** Simple workload-facing API.
- **Con:** Still requires another source of truth for the preferred domain and provides less reuse than attaching that policy to the referenced `ClusterTopology`.

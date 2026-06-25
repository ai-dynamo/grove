# GREP-0617: Scheduler Backend Topology Binding Policy

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Externally Managed Scheduler Topology](#story-1-externally-managed-scheduler-topology)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
    - [Explicit Binding Required Before Workload Admission](#explicit-binding-required-before-workload-admission)
- [Design Details](#design-details)
  - [OperatorConfiguration API](#operatorconfiguration-api)
  - [Scheduler Backend Interfaces](#scheduler-backend-interfaces)
  - [ClusterTopologyBinding Reconciliation](#clustertopologybinding-reconciliation)
  - [Runtime Validation of Backend Support](#runtime-validation-of-backend-support)
  - [PodCliqueSet Validation](#podcliqueset-validation)
  - [Monitoring](#monitoring)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Alternatives](#alternatives)
  - [Backend Default Only, Without Configuration](#backend-default-only-without-configuration)
  - [Per-ClusterTopologyBinding Policy](#per-clustertopologybinding-policy)
<!-- /toc -->

## Summary

Grove currently assumes that any topology-aware scheduler backend can have its backend-specific topology resource derived from a `ClusterTopologyBinding` and created automatically when no explicit binding is provided. This GREP proposes a per-backend topology binding policy so each scheduler backend can declare whether Grove should auto-create that resource or require an explicit reference instead. This makes topology management more accurate for backends with different capabilities and ownership models, and allows Grove's scheduler backend framework to support both Grove-managed and externally managed backend topologies cleanly.

## Motivation

Grove's current topology integration model assumes that when a scheduler backend supports topology-aware scheduling, Grove can also derive and create that backend's topology resource from a `ClusterTopologyBinding` whenever no explicit binding is provided. That assumption does not hold for all scheduler backends. Some backends may be able to consume topology information only through an externally managed resource, or may require backend-specific topology data that Grove cannot synthesize. As Grove expands its scheduler backend framework, it needs a way to distinguish between "this backend is topology-aware" and "Grove is allowed and able to create this backend's topology resource." Without that distinction, Grove treats one backend behavior as the default for all backends, which makes the model less accurate and less extensible.

### Goals

* Add a per-scheduler-backend policy that controls how Grove handles `ClusterTopologyBinding` resources when no explicit `spec.schedulerTopologyBindings` entry exists.
* Support backend-specific default behavior that allows a scheduler backend to declare whether Grove may auto-create and manage its topology resource, or must require an explicit binding reference instead.
* Ensure workload validation rejects topology-aware workloads that target a scheduler backend requiring an explicit topology binding when the referenced `ClusterTopologyBinding` does not provide one.

### Non-Goals

* Changing the topology model defined by `ClusterTopologyBinding`.
* Adding new backend-specific topology synthesis capabilities for scheduler backends that Grove cannot already create topology resources for.
* Supporting multiple topology binding policies within a single scheduler backend or profile.
* Redesigning the broader scheduler backend capability-discovery architecture.

## Proposal

Grove will introduce a topology binding policy for each scheduler backend profile to control how `ClusterTopologyBinding` resources are handled when they do not contain an explicit `spec.schedulerTopologyBindings` entry for that backend. This policy allows Grove to distinguish between scheduler backends for which topology resources may be derived and managed by Grove, and scheduler backends for which an explicit backend-specific topology binding is required.

Two policy behaviors are introduced. With `AutoCreate`, Grove may derive and manage the scheduler backend topology resource from the `ClusterTopologyBinding` when no explicit binding is present. With `ReferenceRequired`, Grove does not create a backend topology resource and instead requires the `ClusterTopologyBinding` to explicitly reference the backend's topology resource.

This policy affects both topology reconciliation and workload validation. When a backend requires an explicit reference, Grove must not silently treat a missing binding as permission to create one. Workloads that rely on topology-aware scheduling for such a backend must reference a `ClusterTopologyBinding` that explicitly binds that backend.

### User Stories

#### Story 1: Externally Managed Scheduler Topology

As a cluster administrator using a scheduler backend such as Volcano, I want Grove to require an explicit topology binding instead of trying to create the backend's topology resource automatically, so that I can use the scheduler backend's own topology model and tooling while still using Grove's topology-aware scheduling integration.

This keeps Grove responsible for validating and binding the `ClusterTopologyBinding`, while leaving backend-specific topology resource creation to the tools and operational workflow already defined for that scheduler backend.

### Limitations/Risks & Mitigations

#### Explicit Binding Required Before Workload Admission

When a scheduler backend is configured with `ReferenceRequired`, topology-aware workloads cannot rely on Grove to create the backend-specific topology resource automatically. If the required backend topology resource has not been created and explicitly bound through `spec.schedulerTopologyBindings`, workload validation will reject topology-aware workloads targeting that backend.

**Mitigation:** Cluster administrators must provision the backend-specific topology resource using the scheduler backend's own tooling and create the corresponding explicit binding in `ClusterTopologyBinding` before admitting topology-aware workloads for that backend. Grove documentation should make this operational requirement clear for backends that use `ReferenceRequired`.

## Design Details

### OperatorConfiguration API

The topology binding policy is configured per scheduler profile in `OperatorConfiguration.scheduler.profiles[]` through a new optional `topologyBindingPolicy` field on `SchedulerProfile`. The supported values are `AutoCreate` and `ReferenceRequired`. If the field is omitted, Grove uses a backend-specific default through `SchedulerProfile.EffectiveTopologyBindingPolicy()`. In the current implementation, `kai-scheduler` defaults to `AutoCreate`, while all other backends default to `ReferenceRequired`. This keeps the policy with scheduler backend configuration rather than on individual `ClusterTopologyBinding` resources.

### Scheduler Backend Interfaces

This GREP distinguishes between a scheduler backend being topology-aware and Grove being able to create and manage that backend's topology resource. The interface split is:

```go
type TopologyAwareBackend interface {
    TopologyGVR() schema.GroupVersionResource
    TopologyBindingPolicy() TopologyBindingPolicy
    CheckTopologyDrift(ctx context.Context, ct *ClusterTopologyBinding, ref SchedulerTopologyBinding) (bool, string, int64, error)
}

type ManagedTopologyBackend interface {
    TopologyAwareBackend
    TopologyResourceName(ct *ClusterTopologyBinding) string
    SyncTopology(ctx context.Context, k8sClient client.Client, ct *ClusterTopologyBinding) error
    OnTopologyDelete(ctx context.Context, k8sClient client.Client, ct *ClusterTopologyBinding) error
}
```

`TopologyAwareBackend` now represents the minimum contract for a backend that participates in topology integration. It covers discovery of the backend topology resource type, reporting the backend's effective topology binding policy, and checking drift for explicitly bound topology resources. A backend that only supports externally managed topology resources can stop at this interface.

`ManagedTopologyBackend` is for backends that Grove can actively manage. It adds the methods required to name, create/update, and delete backend-specific topology resources derived from a `ClusterTopologyBinding`. This allows Grove to support backends that can validate and consume topology bindings without requiring Grove to synthesize their backend-specific topology resources. It also gives the runtime validation path a concrete contract: `AutoCreate` is only valid for backends that implement `ManagedTopologyBackend`.

### ClusterTopologyBinding Reconciliation

The topology binding policy affects `ClusterTopologyBinding` handling only when there is no explicit `spec.schedulerTopologyBindings` entry for a backend. If an explicit binding entry exists, Grove treats that backend topology resource as externally managed and performs drift detection against the referenced resource. If no binding entry exists, Grove resolves behavior from the backend's effective topology binding policy. With `AutoCreate`, Grove may create or reconcile the backend topology resource, but only for backends that implement `ManagedTopologyBackend`. With `ReferenceRequired`, Grove does not create a backend topology resource and skips auto-management for that backend. The same policy resolution is used in both startup topology synchronization and steady-state `ClusterTopologyBinding` reconciliation.

### Runtime Validation of Backend Support

Compatibility between the configured topology binding policy and the selected scheduler backend is validated at runtime when scheduler backends are constructed. If a profile configures `topologyBindingPolicy` for a backend that does not implement `TopologyAwareBackend`, Grove rejects the configuration. If a profile configures `AutoCreate` for a backend that is topology-aware but does not implement `ManagedTopologyBackend`, Grove also rejects the configuration. This ensures the configured behavior matches the actual capabilities of the backend implementation.

### PodCliqueSet Validation

For workloads that use topology-aware scheduling, Grove continues to validate topology constraints against the referenced `ClusterTopologyBinding`. This GREP adds an additional rule for backends whose effective topology binding policy is `ReferenceRequired`: the referenced `ClusterTopologyBinding` must contain a matching `spec.schedulerTopologyBindings` entry for that backend. This prevents Grove from admitting topology-aware workloads that depend on a backend-specific topology resource which Grove is not allowed to create automatically.

### Monitoring

This GREP does not introduce any new metrics, status conditions, or status fields. Operational visibility continues to come from the existing `ClusterTopologyBinding` reconciliation behavior and from admission validation errors returned when topology-aware workloads reference a scheduler backend that requires an explicit topology binding but the referenced `ClusterTopologyBinding` does not provide one.

### Test Plan

The implementation should include unit and integration coverage for the new topology binding policy behavior across configuration, topology reconciliation, and workload validation.

* **Configuration and backend initialization tests:**
  * Verify that `topologyBindingPolicy` accepts only supported enum values.
  * Verify that backend-specific defaults are applied through `EffectiveTopologyBindingPolicy()`.
  * Verify that Grove rejects configurations where `topologyBindingPolicy` is set for a backend that is not topology-aware.
  * Verify that Grove rejects `AutoCreate` for a topology-aware backend that does not implement managed topology support.

* **ClusterTopologyBinding controller tests:**
  * Verify that when an explicit `spec.schedulerTopologyBindings` entry exists, Grove treats the backend topology as externally managed and performs drift detection only.
  * Verify that when no explicit binding exists and the effective policy is `AutoCreate`, Grove creates or reconciles the backend topology resource.
  * Verify that when no explicit binding exists and the effective policy is `ReferenceRequired`, Grove does not auto-create the backend topology resource.

* **PodCliqueSet validation tests:**
  * Verify that topology-aware workloads targeting a backend with effective policy `ReferenceRequired` are rejected when the referenced `ClusterTopologyBinding` does not contain a matching `spec.schedulerTopologyBindings` entry.
  * Verify that the same workload is admitted when the explicit scheduler topology binding is present.

* **End-to-end tests:**
  * Add an e2e scenario that configures the `kai-scheduler` backend with `topologyBindingPolicy: ReferenceRequired`.
  * Verify that a topology-aware workload referencing a `ClusterTopologyBinding` without a matching `schedulerTopologyBindings` entry is rejected.
  * Verify that the workload is admitted once the `ClusterTopologyBinding` is updated to explicitly bind the KAI topology resource.

### Graduation Criteria

* **Alpha:** topology binding policy support is implemented and covered by unit and integration tests.
* **Beta:** e2e coverage is added for a backend using `ReferenceRequired`.
* **GA:** the API and behavior remain stable with no further changes required.

## Alternatives

### Backend Default Only, Without Configuration

One alternative is to rely only on backend-defined default behavior and not expose a configurable topology binding policy in scheduler profile configuration. In that model, each backend would decide internally whether it uses auto-creation or requires an explicit reference, and operators would not be able to override that behavior. This was not chosen because Grove needs a way to make the topology ownership model explicit in configuration and to support clusters where the desired policy may differ from the backend's built-in default.

### Per-ClusterTopologyBinding Policy

Another alternative is to place the topology binding policy on each `ClusterTopologyBinding` instead of on the scheduler backend profile. This was not chosen because the decision being modeled is primarily a backend capability and ownership decision, not a property of an individual topology object. Keeping the policy in scheduler backend configuration makes the behavior uniform for a backend and avoids repeating backend-level policy across multiple `ClusterTopologyBinding` resources.

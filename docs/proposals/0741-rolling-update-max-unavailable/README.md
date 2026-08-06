# GREP-0741: Rolling Update MaxUnavailable

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Protect serving capacity during updates](#story-1-protect-serving-capacity-during-updates)
    - [Story 2: Treat a multi-node replica as one rollout unit](#story-2-treat-a-multi-node-replica-as-one-rollout-unit)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
    - [No surge capacity](#no-surge-capacity)
    - [Availability is not application health](#availability-is-not-application-health)
    - [Budget scope is per component](#budget-scope-is-per-component)
    - [External disruptions can stall a rollout](#external-disruptions-can-stall-a-rollout)
    - [Conservative expectation accounting](#conservative-expectation-accounting)
    - [API overlap with coherent rolling updates](#api-overlap-with-coherent-rolling-updates)
- [Design Details](#design-details)
  - [API](#api)
  - [Availability budget](#availability-budget)
  - [Rolling-update flow](#rolling-update-flow)
  - [Scale-in coordination](#scale-in-coordination)
  - [Backward compatibility](#backward-compatibility)
  - [Relationship to GREP-393](#relationship-to-grep-393)
  - [Monitoring](#monitoring)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [End-to-end tests](#end-to-end-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [General Availability](#general-availability)
<!-- /toc -->

## Summary

This proposal introduces an opt-in `maxUnavailable` rolling update setting for standalone PodCliques and PodCliqueScalingGroups. It limits how many Pods or logical scaling-group replicas Grove may make unavailable at the same time during configuration rollouts and scale-in, helping serving workloads preserve available capacity while retaining the existing behavior when the setting is not configured.

## Motivation

Grove currently does not provide a dedicated API for users to configure how a PodClique or PodCliqueScalingGroup should be upgraded. Rollout behavior is therefore determined entirely by built-in controller logic, leaving workload owners unable to express workload-specific availability requirements during an upgrade.

This proposal introduces a `rollingUpdate` strategy field as the extensible configuration entry point for controlling workload upgrades. Keeping rollout-specific settings under this field gives the API a clear boundary and allows additional rolling-update constraints to be introduced in the future without adding unrelated fields directly to the PodClique or PodCliqueScalingGroup specification.

The first constraint introduced under `rollingUpdate` is `maxUnavailable`. Grove's existing reconciliation can initiate multiple deletions when old Pods or replicas are already Pending or unavailable. Scale-in also follows a separate deletion path and can overlap with a configuration rollout. During rapid configuration changes or overlapping rollout and scale-in operations, multiple Pods or logical PodCliqueScalingGroup replicas may therefore be recreated concurrently.

For serving workloads, every unavailable replica shifts requests onto fewer Ready replicas, potentially destabilizing latency and throughput. The existing `minAvailable` setting defines gang scheduling and gang termination thresholds; it is not a shared rollout concurrency budget. `rollingUpdate.maxUnavailable` provides that explicit budget while remaining opt-in so workloads that do not configure a rolling-update strategy retain their existing behavior.

### Goals

* Provide an extensible `rollingUpdate` strategy entry point for both standalone PodCliques and PodCliqueScalingGroups.
* Allow workload owners to configure `maxUnavailable` as any positive integer to bound disruption during rolling updates.
* Enforce the budget at the correct workload granularity: individual Pods for a standalone PodClique and complete logical replicas for a PodCliqueScalingGroup.
* Coordinate rolling-update and scale-in deletions through the same availability budget, including deletions that have been requested but are not yet visible in informer caches.
* Ensure that `maxUnavailable: 1` prevents Grove from starting a second controller-initiated Pod or logical-replica deletion until the current replacement becomes available.
* Preserve existing rollout behavior when `rollingUpdate.maxUnavailable` is not configured.

### Non-Goals

* Defining an ordinal, Pod-index, or creation-time rollout order. The proposal limits concurrency but does not change the existing object-selection policy.
* Introducing a separate `OrderedReady` policy. Readiness-based progression is expressed through the shared `maxUnavailable` budget.
* Serializing scale-out creation. The budget controls controller-initiated rollout and scale-in deletions.
* Replacing or changing `minAvailable`, PodGang scheduling, or gang-termination semantics.
* Preventing node failures, evictions, manual Pod deletions, or other external disruptions from making additional replicas unavailable. Grove accounts for observed unavailability before initiating its own deletion, but cannot prevent external events.
* Supporting percentage values for `maxUnavailable` in the initial API; the proposed field accepts positive integers.
* Guaranteeing application-level health or traffic distribution beyond Kubernetes Pod availability.

## Proposal

Grove will add a `rollingUpdate` strategy to standalone PodClique and PodCliqueScalingGroup configuration. This strategy provides a dedicated place for workload owners to declare constraints that govern controller-managed upgrades. The initial strategy option is `maxUnavailable`.

Before Grove starts a rolling-update or scale-in deletion, it will determine how much of the configured unavailability budget is already in use. Replicas that are currently unavailable, terminating, or already selected for deletion consume the budget. Grove may start only the number of additional deletions allowed by the remaining budget.

For a standalone PodClique, one budget unit represents one Pod. For a PodCliqueScalingGroup, one budget unit represents one complete logical replica containing all member PodCliques at the same replica index. Grove may replace the members of that logical replica together, but it will not begin changing another logical replica while the budget is exhausted.

Rolling updates and scale-in share this budget so that independent reconciliation paths cannot each initiate disruptive work at the same time. With `maxUnavailable: 1`, Grove starts at most one controller-managed Pod or logical-replica change and waits until sufficient availability has been restored before proceeding. When the strategy or `maxUnavailable` is absent, Grove retains its current behavior.

### User Stories

#### Story 1: Protect serving capacity during updates

As an inference platform operator, I want to limit a standalone PodClique rollout to one unavailable Pod so that rapid configuration updates do not remove multiple serving replicas at the same time.

#### Story 2: Treat a multi-node replica as one rollout unit

As an operator of a multi-node inference workload, I want the leader and workers of one PodCliqueScalingGroup replica to be treated as a single rollout unit so that Grove completes one logical replica replacement before disrupting another.

### Limitations/Risks & Mitigations

#### No surge capacity

This proposal controls how many existing replicas Grove may disrupt, but it does not create replacement capacity before deletion. A rollout can therefore temporarily reduce serving capacity by up to `maxUnavailable`, and a replacement that cannot be scheduled can stall further progress.

**Mitigation:** Operators should provision sufficient steady-state replicas and cluster capacity for the configured disruption budget. A future strategy option may add surge behavior without changing the `rollingUpdate` API boundary.

#### Availability is not application health

The budget uses Kubernetes readiness and the existing Grove replica-availability rules. A Ready Pod may still be unable to serve application traffic correctly, and an application can become overloaded even when the configured availability floor is satisfied.

**Mitigation:** Workloads should use representative readiness probes and continue to rely on application-level monitoring, load shedding, and traffic management.

#### Budget scope is per component

Each standalone PodClique and each PodCliqueScalingGroup has its own budget. If several independent components in one PodCliqueSet are updated at the same time, each component may consume its own budget concurrently. This proposal does not add a PodCliqueSet-wide disruption budget.

**Mitigation:** Workload owners that require cross-component version coherence or a PodCliqueSet-wide update plan should use the coherent update model described by [GREP-393](../393-coherent-rolling-updates/README.md) when it becomes available.

#### External disruptions can stall a rollout

Node failures, preemption, manual deletion, and other external events can make more replicas unavailable than the configured limit. Grove cannot prevent those events. It will conservatively stop initiating additional deletions until availability recovers.

**Mitigation:** The controller reports the blocked budget in its logs and automatically resumes after the observed workload recovers.

#### Conservative expectation accounting

Create and delete expectations reserve budget before informer caches observe the corresponding API operation. Delayed or stale observations can therefore pause a rollout temporarily even when the cluster has already completed an operation.

**Mitigation:** Existing expectation reconciliation clears reservations as informer state converges. Conservatively stalling is safer than initiating an extra deletion during the observation window.

#### API overlap with coherent rolling updates

[GREP-393](../393-coherent-rolling-updates/README.md) also proposes a per-component `rollingUpdate.maxUnavailable` setting as part of a broader coherent-update design. GREP-393 coordinates compatible sets of components through PodGangs and rejects scale operations during an update. This proposal instead adds an opt-in availability safeguard to the existing RollingRecreate PodClique and PodCliqueScalingGroup controllers and explicitly coordinates scale-in with their rollout deletions.

**Mitigation:** The proposals should converge on one public `rollingUpdate` API shape before either API becomes stable. The lower-level budget accounting described here can remain strategy-specific while sharing the public configuration type and field placement with GREP-393.

## Design Details

### API

The PodClique spec gains an optional rolling-update strategy:

```go
type PodCliqueSpec struct {
    // ... existing fields ...

    // RollingUpdate configures how Pod deletions are paced while this
    // PodClique is being updated to a new Pod template.
    // If unset, the original Grove rolling-update behavior is preserved.
    // +optional
    RollingUpdate *PodCliqueRollingUpdateStrategy `json:"rollingUpdate,omitempty"`
}

type PodCliqueRollingUpdateStrategy struct {
    // MaxUnavailable is the maximum number of desired Pods that may be
    // unavailable during a rolling update. Values greater than Replicas are
    // capped at Replicas.
    // +optional
    // +kubebuilder:validation:Minimum=1
    MaxUnavailable *int32 `json:"maxUnavailable,omitempty"`
}
```

A standalone PodClique is configured through its template:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: inference
spec:
  template:
    cliques:
      - name: frontend
        spec:
          replicas: 3
          rollingUpdate:
            maxUnavailable: 1
          podSpec:
            # ...
```

The PodCliqueScalingGroup configuration gains a corresponding strategy whose budget is measured in complete logical replicas:

```go
type PodCliqueScalingGroupConfig struct {
    // ... existing fields ...

    // RollingUpdate configures how PodCliqueScalingGroup replica deletions
    // are paced while the group is being updated.
    // If unset, the original Grove rolling-update behavior is preserved.
    // +optional
    RollingUpdate *PodCliqueScalingGroupRollingUpdateStrategy `json:"rollingUpdate,omitempty"`
}

type PodCliqueScalingGroupRollingUpdateStrategy struct {
    // MaxUnavailable is the maximum number of complete
    // PodCliqueScalingGroup replicas that may be unavailable during a rolling
    // update. Values greater than Replicas are capped at Replicas.
    // +optional
    // +kubebuilder:validation:Minimum=1
    MaxUnavailable *int32 `json:"maxUnavailable,omitempty"`
}
```

A multi-node component is configured at the PodCliqueScalingGroup level:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: multi-node-inference
spec:
  template:
    cliques:
      - name: leader
        spec:
          replicas: 1
          podSpec:
            # ...
      - name: worker
        spec:
          replicas: 2
          podSpec:
            # ...
    podCliqueScalingGroups:
      - name: decode
        replicas: 3
        minAvailable: 1
        cliqueNames:
          - leader
          - worker
        rollingUpdate:
          maxUnavailable: 1
```

`maxUnavailable` must be a positive integer. A configured value greater than the current desired replica count is capped at that count. An absent `rollingUpdate`, or a `rollingUpdate` with no `maxUnavailable`, disables the new concurrency safeguard and preserves the existing controller behavior.

### Availability budget

For a PodClique, let:

* `D` be the desired Pod count.
* `A` be the number of observed Pods that are Running, Ready, not terminating, and not covered by a deletion expectation.
* `C` be the number of active logical Pod lifecycle changes. Non-Running, non-Ready, terminating, create-expected, and delete-expected objects contribute to this count. Changes are de-duplicated by Pod index where possible so deletion and replacement of the same logical slot do not consume two units.
* `M` be the configured `maxUnavailable`.

The controller computes:

```text
limit       = min(M, D)
unavailable = max(D - min(A, D), C)
allowed     = max(0, limit - unavailable)
```

The `max` between availability loss and active lifecycle changes prevents scale-in work outside the new desired count from escaping the budget. Expectations reserve a slot immediately, before informer caches observe a deletion or replacement.

For a PodCliqueScalingGroup, the same calculation is performed in logical-replica units. A desired replica is available only when:

* every expected member PodClique for that replica index exists;
* none of those PodCliques is terminating; and
* every member PodClique has at least its own `spec.minAvailable` Ready Pods.

The PCSG replica currently recorded as selected for update consumes one unit until its replacement is complete, even if the informer cache still shows the old member PodCliques as available.

### Rolling-update flow

The existing object-selection behavior is preserved. The availability budget is evaluated before any new deletion:

1. If the budget is exhausted, reconciliation requeues without initiating another deletion.
2. Old non-Ready objects are preferred over Ready objects. For PodCliques, the existing priority remains Pending, Unhealthy, Starting, then uncategorized. For PCSGs, old Pending replicas are selected before old unavailable replicas.
3. The selected non-Ready set is truncated to the remaining budget. When the safeguard is enabled, the controller requeues after starting those deletions so that a Ready object is not also selected in the same reconcile.
4. Ready objects continue through the existing rolling-update state machine. The controller records the current selection and waits for the replacement to become available before selecting another Ready object.

The proposal does not add ordinal rollout semantics. Existing creation-time and deletion-sort behavior remains responsible for choosing among otherwise eligible objects.

### Scale-in coordination

Scale-in and rolling-update deletions use the same budget when `maxUnavailable` is configured.

For a PodClique, scale-in is evaluated against the current Pod population rather than only the reduced desired count. This keeps a surplus Pod charged to the budget until its lifecycle change disappears from informer state. An excess Pod that is already non-Ready may still be selected because deleting it does not introduce a new unavailable logical slot. A pending scale-in prevents the same reconciliation flow from initiating a rolling-update deletion. If scale-in removes the replacement for a previously selected update, stale update-selection status is reset before the rollout continues.

For a PodCliqueScalingGroup, the scale-in budget includes unavailable desired replicas, the replica currently selected for update, and excess replicas that are already terminating or unavailable. Excess replica indices continue to be selected from highest to lowest, but only the number permitted by the remaining logical-replica budget is deleted. While any excess replica remains, the controller does not initiate another rolling-update deletion.

### Backward compatibility

The feature is opt-in. Workloads that omit `rollingUpdate.maxUnavailable` retain the existing batch deletion behavior for old unavailable objects and scale-in. Existing `minAvailable`, update-progress status, Pod selection, PodGang scheduling, and gang-termination semantics are unchanged.

The two strategy structs are separate even though they initially contain the same field. Their units differ, and keeping distinct types allows PodClique and PodCliqueScalingGroup strategies to evolve independently while retaining clear API documentation.

### Relationship to GREP-393

This proposal and [GREP-393](../393-coherent-rolling-updates/README.md) share the concept of a per-component rolling-update disruption budget, but they operate at different orchestration layers:

* GREP-393 defines a new Coherent update strategy that coordinates version-compatible component sets through MVU-based PodGang steps.
* This proposal safeguards the existing PodClique and PodCliqueScalingGroup RollingRecreate paths, including rapid target changes, already-unavailable objects, informer observation windows, and overlapping scale-in.

The budget-evaluation mechanisms in this proposal do not provide cross-component coherence and are not a replacement for GREP-393. Before API graduation, the community should reconcile the common `rollingUpdate` field placement, defaulting, and validation semantics so that both strategies consume one consistent public configuration.

### Monitoring

The initial implementation does not add new Prometheus metrics, Kubernetes Events, status conditions, or status fields. Operators can observe rollout progress through existing resources:

* `PodClique.status.readyReplicas`, `updatedReplicas`, and `updateProgress`.
* `PodCliqueScalingGroup.status.availableReplicas`, `updatedReplicas`, and `updateProgress`.
* Controller logs emitted when a rollout or scale-in deletion is blocked. These entries include the current unavailable count, the effective limit, and the `MaxUnavailableReached` reason.

An exhausted budget is a waiting state rather than a controller failure. A workload that remains blocked should be investigated by checking the unavailable Pod or logical PCSG replica, its scheduling state and readiness, and any outstanding create/delete operation.

If operational experience shows that log- and status-based monitoring is insufficient, follow-up work should add a counter for blocked deletion attempts and a gauge for current budget consumption without changing rollout semantics.

### Test Plan

#### Unit tests

API and admission tests verify:

* positive integer values for both PodClique and PCSG strategies;
* rejection of zero and negative values;
* acceptance of arbitrary positive values;
* propagation of the PodClique strategy from the PodCliqueSet template to generated PodClique resources; and
* generated CRD and API-reference content.

PodClique controller tests in:

* `operator/internal/controller/podclique/components/pod/rollingupdatebudget_test.go`; and
* `operator/internal/controller/podclique/components/pod/syncflow_budget_test.go`

cover:

* budget calculation for Ready, Pending, terminating, missing, create-expected, and delete-expected Pods;
* values greater than the replica count;
* blocking a second deletion before informer-cache convergence;
* rapid target changes while a replacement remains Pending;
* preference for old non-Ready Pods;
* scale-in constrained by the same budget;
* deletion of an already-unavailable excess Pod without consuming a new slot;
* prevention of overlapping scale-in and rolling-update deletion; and
* preservation of legacy behavior when the field is unset.

PodCliqueScalingGroup controller tests in `operator/internal/controller/podcliquescalinggroup/components/podclique/rollingupdatebudget_test.go` cover:

* logical-replica availability across all member PodCliques;
* missing, unavailable, terminating, and currently selected replicas;
* one complete PCSG replica deletion per available budget unit;
* rapid target changes;
* scale-in using the logical-replica budget;
* terminating excess replicas blocking additional disruption; and
* preservation of legacy behavior when the field is unset.

#### End-to-end tests

End-to-end coverage should include:

* a three-Pod standalone PodClique with `maxUnavailable: 1`, verifying that an S1 → S2 → S3 target change never has more than one controller-managed Pod lifecycle change in flight;
* the same rollout with a pre-existing Pending or non-Ready Pod;
* scale-in during an active configuration rollout and a configuration change during scale-in;
* a three-replica PCSG whose logical replica contains a leader and multiple workers, verifying that one complete replica may rebuild together but a second replica is not selected until the first is available;
* arbitrary values greater than one; and
* an unset strategy, verifying compatibility with existing behavior.

The implementation is validated locally with:

```text
cd operator/api && go test ./...
cd operator && go test ./...
```

### Graduation Criteria

#### Alpha

* The additive API, CRD generation, validation, PodClique budget, PCSG logical-replica budget, and scale-in coordination are implemented.
* Unit tests cover budget accounting and informer-expectation races.
* Basic standalone and PCSG end-to-end rollout tests pass.
* User-facing API documentation describes the budget unit and opt-in behavior.

#### Beta

* The feature has been exercised in at least one production serving environment for both standalone and multi-node workloads.
* Operational validation covers rapid configuration changes and scale-in overlapping with rollout.
* No unresolved critical issues exist in availability accounting, stalled-rollout recovery, or backward compatibility.
* The public API relationship with GREP-393 has been resolved, with one documented `rollingUpdate` configuration model.

#### General Availability

* The API and behavior have remained stable for at least two releases after Beta.
* End-to-end tests continuously cover standalone, PCSG, rapid-update, existing-unavailable, and scale-in scenarios.
* Monitoring and troubleshooting guidance is considered sufficient based on production feedback.
* No open high-severity correctness issues remain.

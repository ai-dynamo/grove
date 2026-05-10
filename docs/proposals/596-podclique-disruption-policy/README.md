# GREP-596: PodClique Disruption Policy

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Taint-driven PodClique replacement](#story-1-taint-driven-podclique-replacement)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
  - [Behavior Details](#behavior-details)
  - [Monitoring](#monitoring)
  - [Dependencies](#dependencies)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
<!-- /toc -->

## Summary

This GREP adds one opt-in `PodClique` policy for taint-manager-driven replacement. When Kubernetes starts deleting a managed `Pod` because it does not tolerate a `NoExecute` taint, Grove deletes the owning `PodClique` instead of repairing only that `Pod`.

The owning `PodCliqueSet` or `PodCliqueScalingGroup` recreates that `PodClique`, and normal `PodClique` pod sync creates the desired `Pods`.

The policy applies only when the configured `PodClique` contains all `Pods` that should be replaced together. Behavior Details defines the exact trigger.

## Motivation

Grove currently repairs missing `Pods` one-for-one. That is correct for ordinary replicated `Pods`, but not for workloads where disruption of one member means the whole `PodClique` should be recreated. If one member is disrupted, one-for-one repair leaves the other `Pods` from the old placement in place.

Grove already has `MinAvailableBreached`-based gang termination: it can terminate a `PodCliqueScalingGroup` replica or an entire `PodCliqueSet` replica after `TerminationDelay`. That path is availability-driven, delayed, and scoped by gang-termination rules.

This policy uses the taint-manager deletion signal instead of waiting for availability loss as a proxy for placement failure.

### Goals

* Preserve current behavior by default.
* Add a simple opt-in `PodClique` policy for taint-manager-driven replacement.
* Use Kubernetes `Pod` disruption status instead of watching `Nodes`.
* Rely on existing Kubernetes object state to resume replacement after controller restart.
* Keep placement decisions in the scheduler.

### Non-Goals

* Supporting every `DisruptionTarget` reason.
* Implementing behavior for disruption reasons or actions beyond `DeletionByTaintManager` and `Recreate`.
* Replacing `PodCliqueSet` or `PodCliqueScalingGroup` replicas as a larger unit.
* Enforcing `PodDisruptionBudget` semantics for `Pods` removed by `PodClique` deletion.
* Changing scheduler placement logic.

## Proposal

Add an extensible disruption policy to `PodCliqueSpec` at `PodClique.spec.disruption`. For managed workloads, users set it in `PodCliqueSet` templates; Grove syncs it into generated `PodCliques`. `PodClique` admission prevents unmanaged/direct use and protects generated `PodCliques` as template-owned objects.

If unset, behavior is unchanged. This GREP defines one supported rule: action `Recreate` for the taint-manager deletion matcher shown below.

```
spec:
  template:
    cliques:
    - name: worker-group-a
      spec:
        replicas: 4
        minAvailable: 4
        disruption:
          rules:
          - action: Recreate
            onPodConditions:
            - type: DisruptionTarget
              status: "True"
              reason: DeletionByTaintManager
```

### User Stories

#### Story 1: Taint-driven PodClique replacement

A workload has a `PodClique` whose `Pods` must be scheduled together. The replacement flow is:

1. An external system taints the bad node with a custom `NoExecute` taint.
2. Kubernetes starts deleting affected `Pods` that do not tolerate the taint indefinitely.
3. Grove observes the matching terminating `Pod`.
4. Grove deletes the `PodClique`.
5. The owner recreates the `PodClique`; normal pod sync creates the desired `Pods`.

### Limitations/Risks & Mitigations

* The configured `PodClique` must contain all `Pods` that should be replaced together. Other `PodCliques` are not affected.
* The policy is rejected when a `PodClique` template sets both `spec.disruption` and its own `spec.autoScalingConfig` because delete-then-recreate can reset that `PodClique`'s live replica count. `PCSG-level` `scaleConfig` is allowed.
* Grove must observe the live terminating `Pod`; absence alone keeps current one-for-one repair behavior.
* The Eviction API is not used.
* If owned `Pods` or `PodClique-scoped` `ResourceClaims` are stuck, the old `PodClique` remains terminating and replacement stays blocked until cleanup finishes or an operator intervenes.

## Design Details

### API Changes

Use a small rule-based policy object, but keep the implemented scope narrow. This GREP only accepts action `Recreate` and an `onPodConditions` matcher for `DisruptionTarget=True` with reason `DeletionByTaintManager`.

```go
type PodCliqueSpec struct {
    // existing fields omitted
    // +optional
    Disruption *PodCliqueDisruptionPolicy `json:"disruption,omitempty"`
}

type PodCliqueDisruptionPolicy struct {
    // Rules contains the single supported replacement rule.
    // +kubebuilder:validation:MinItems=1
    // +kubebuilder:validation:MaxItems=1
    // +listType=atomic
    Rules []PodCliqueDisruptionRule `json:"rules"`
}

type PodCliqueDisruptionRule struct {
    // This GREP only accepts Recreate.
    // +kubebuilder:validation:Enum=Recreate
    Action PodCliqueDisruptionAction `json:"action"`

    // OnPodConditions matches Kubernetes Pod conditions.
    // This GREP requires exactly one DisruptionTarget=True / DeletionByTaintManager pattern.
    // +kubebuilder:validation:MinItems=1
    // +kubebuilder:validation:MaxItems=1
    // +listType=atomic
    OnPodConditions []PodConditionPattern `json:"onPodConditions"`
}

// +kubebuilder:validation:Enum=Recreate
type PodCliqueDisruptionAction string

const (
    PodCliqueDisruptionActionRecreate PodCliqueDisruptionAction = "Recreate"
)

type PodConditionPattern struct {
    // This GREP only accepts DisruptionTarget.
    // +kubebuilder:validation:Enum=DisruptionTarget
    Type corev1.PodConditionType `json:"type"`

    // This GREP only accepts True.
    // +kubebuilder:validation:Enum=True
    // +kubebuilder:default=True
    // +optional
    Status corev1.ConditionStatus `json:"status,omitempty"`

    // This GREP only accepts DeletionByTaintManager.
    // +kubebuilder:validation:Enum=DeletionByTaintManager
    Reason string `json:"reason"`
}
```

Validation rules:

* `PodCliqueSet` validation rejects invalid templates: empty or multiple rules, missing triggers, unsupported matcher/action, and any `PodClique` template that sets both `spec.disruption` and its own `spec.autoScalingConfig`. `PCSG-level` `scaleConfig` is allowed.
* `PodClique` admission rejects `spec.disruption` on unmanaged direct `PodCliques`.
* `PodClique` admission rejects non-reconciler create/update that sets, changes, or removes generated `spec.disruption` when old or new object has the field. The reconciler username is `system:serviceaccount:<manager-namespace>:<manager-serviceaccount>`; users/groups are used only for explicit webhook-configured exemptions.
* Generated `PodClique` admission verifies source ownership: direct `PCS`, or `PodClique -> PCSG -> PCS` for `PCSG-owned` cliques. It checks controller `ownerReferences`, generated name, `PCS/PCSG` replica labels, and clique membership, not full spec equality.
* Finalizer-only updates on terminating `PodCliques` are exempt.
* Owner checks use `APIReader`/live API reads.
* The only accepted rule is `action=Recreate` plus one `onPodConditions` matcher; omitted status defaults to `True`.

No new status field is added. Replacement state is represented by the `PodClique` deletion itself: once the `PodClique` is gone, owner sync recreates it from the template.

Kubernetes defines the `Pod` condition type as `corev1.DisruptionTarget`. The taint eviction controller uses reason string `DeletionByTaintManager`, which is not exposed as a `corev1` constant in the current dependency, so Grove compares the string directly.

Changes to `spec.template.cliques[*].spec.disruption` on the `PodCliqueSet` must enqueue both `PodCliqueSet` and `PodCliqueScalingGroup` owner sync so generated `PodCliques` are updated. They may change `PodClique` `metadata.generation`, but not the `PCS` generation hash, `PCLQ` pod-template hash, `updateProgress`, or `Pods`.

### Behavior Details

Replacement starts only after Grove observes a current `Pod` that is managed by the `PodClique`, matches the configured `Recreate` rule, has `DisruptionTarget=True` with reason `DeletionByTaintManager`, and has `metadata.deletionTimestamp` set.

Delete events alone are wakeups; absence alone never proves why a `Pod` disappeared.

The replacement flow is:

* Grove deletes the `PodClique` with foreground propagation and explicitly self-requeues the deleting `PodClique` so its finalizer runs; it does not rely on a `deletionTimestamp` watch update.
* Existing `PodClique` deletion clears expectations, removes the finalizer, and lets Kubernetes garbage-collect owned `Pods` and `PodClique-scoped` `ResourceClaims`.
* The owning `PodCliqueSet` or `PodCliqueScalingGroup` skips patching the terminating `PodClique` and recreates the expected `PodClique` only after the old object is gone.
* Normal `PodClique` pod sync creates the desired `Pods`.

Integration rules:

* Deletion of the owning `PodCliqueSet` or `PodCliqueScalingGroup` wins; `finalizer-only` `PodClique` updates remain allowed during cleanup.
* Rolling update and scale-in deletes do not trigger replacement.
* Policy removal does not cancel an accepted `PodClique` deletion; it only prevents later triggers after owner sync removes the copied policy.
* A second matching trigger while the `PodClique` is already terminating is ignored.
* With `updateStrategy=OnDelete`, the recreated `PodClique` uses the owning template at recreation time; later template changes follow normal `OnDelete` behavior.
* `PCS` and `PCSG` ignore terminating `PodCliques` when aggregating `MinAvailableBreached` and selecting gang-termination candidates. Stale `MinAvailableBreached=True` on a foreground-deleted object cannot trigger unrelated gang termination.

No new recovery state is needed after `PodClique` deletion is accepted: restart recovery uses the existing deletion and owner sync paths.

If Grove observes the `Pod` signal but crashes before deleting the `PodClique`, absence alone does not start replacement.

### Monitoring

No new status fields or metrics are added. Grove emits Kubernetes Events when it accepts a disruption-triggered `PodClique` deletion and when foreground deletion remains blocked on owned `Pods` or `PodClique`-scoped `ResourceClaims`.

### Dependencies

This feature depends on Kubernetes setting the `Pod` `DisruptionTarget` condition for taint-manager deletions and on the workload not tolerating the triggering `NoExecute` taint indefinitely.

A finite `tolerationSeconds` is compatible if Kubernetes eventually deletes the `Pod`.

### Test Plan

Unit tests should cover:

* API/admission: rule shape, supported matcher/action, defaulting, unmanaged direct `PodCliques`, non-reconciler create/update/removal, owner-chain identity checks, `finalizer-only` bypass, reject `spec.disruption` plus `PodClique` `spec.autoScalingConfig`, and allow `PCSG-level` `scaleConfig`.
* Policy propagation: `PodCliqueSet-owned` and `PodCliqueScalingGroup-owned` `PodCliques` sync `spec.disruption` without pod-template hash changes, `updateProgress`, or `Pod` replacement.
* Trigger/watch: condition-only `DisruptionTarget=True` does not delete; later `deletionTimestamp` with the same condition does. Cover delete-event wakeups and retroactive enablement.
* Replacement flow: for both `PCS-owned` and `PCSG-owned` `PodCliques`, the `PodClique` controller foreground-deletes the `PodClique` and self-requeues for finalizer cleanup; foreground deletion keeps the old object until owned `Pods/ResourceClaims` are gone, then the owner recreates it and normal pod sync creates `Pods`.
* Interactions: restart convergence, owner deletion, rolling update and scale-in, `OnDelete`, stale `MinAvailableBreached=True`, policy removal, second trigger, and unset-policy one-for-one repair.

Integration/e2e should cover:

* A `NoExecute` taint causes foreground `PodClique` delete-then-recreate: old `Pod` UIDs are gone before recreated `PodClique` `Pods` appear, replacement `Pods` become scheduled and ready, and the `PodClique` validating webhook chart/cert wiring is installed and functional.

### Graduation Criteria

The change is complete when the API, controller behavior, `PodClique` validating webhook registration, chart/cert-management wiring, tests, and generated API docs are updated.

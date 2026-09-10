# GREP-0793: Capacity Identity and Authorized Release

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Capacity slots](#capacity-slots)
  - [Authorized release](#authorized-release)
  - [User Stories](#user-stories)
    - [Draining a degraded member](#draining-a-degraded-member)
    - [Surviving a crash mid-transaction](#surviving-a-crash-mid-transaction)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [Stable group-wide slot index](#stable-group-wide-slot-index)
  - [API](#api)
  - [Authorization transport](#authorization-transport)
    - [The requirement](#the-requirement)
    - [Proposed: a status-acknowledged spec field](#proposed-a-status-acknowledged-spec-field)
  - [Reconciliation](#reconciliation)
  - [Monitoring](#monitoring)
  - [Dependencies](#dependencies)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Alternatives](#alternatives)
- [Appendix](#appendix)
<!-- /toc -->

## Summary

Grove's scale surface carries *cardinality* but not *identity*. `PodClique.spec.replicas` says how many pods a component should have; it cannot say which durable slot a pod occupies, and the `/scale` subresource cannot say which pod a scale-in should remove. For ordinary stateless components that is the right level of abstraction. For a live distributed engine it is not: an external controller that has drained a specific rank must be able to release exactly that rank's capacity, and no other.

This GREP proposes two additive contracts that close that gap. **Capacity slots** give every pod a durable logical position that survives replacement, formalizing the pod-index labels Grove already assigns and fixing a case where those indices are silently rewritten. **Authorized release** lets an external controller nominate the exact pods a scale-in may delete, bound to immutable pod UIDs, with Grove refusing stale or incomplete authorizations rather than falling back to heuristic victim selection.

Both are opt-in and default to today's behavior. Together they answer the identity half of [#793](https://github.com/ai-dynamo/grove/issues/793) while leaving Grove engine-neutral: Grove never learns what a rank is, only that a particular pod may or may not be released.

## Motivation

Issue [#793](https://github.com/ai-dynamo/grove/issues/793) asks how an external workload controller can resize one `PodClique` inside a specific `PodCliqueScalingGroup` replica while a distributed engine stays live. The motivating consumer is Dynamo-managed Elastic Expert Parallelism ([DEP #13121](https://github.com/ai-dynamo/dynamo/issues/13121)), but nothing below is engine-specific.

The issue enumerates six required semantics. They are not equally urgent, and they are not equally coupled. Five of them are *policy* questions — how much capacity must be admitted atomically, when a gang may be terminated, how a fleet-wide reshape is staged. One is a *representation* question: can the external controller name a piece of physical capacity at all, and can it hand that name back to Grove as an instruction?

The representation question comes first, because the other five are unanswerable without it. A survivor-preserving failure policy needs to report *which* capacity failed. A staged fleet operation needs durable per-world progress keyed to *something*. Every one of those requirements bottoms out in a stable name and a way to bind it to a concrete pod instance.

Two concrete defects motivate the specifics.

**Heuristic victim selection contradicts engine-authorized deletion.** Scale-in currently picks victims with [`DeletionSorter`](https://github.com/ai-dynamo/grove/blob/main/operator/internal/controller/podclique/components/pod/deletionsort.go), which prefers unscheduled pods, then non-ready pods, then — rule 5 — *newer* pods. In an elastic engine the newest member is typically the one that most recently finished joining the topology, so the default preference is close to the worst possible choice. An engine that has drained rank 3 and committed a topology without it will instead have rank 7 deleted underneath it.

**The PCSG-wide pod index is not stable under the operation #793 asks for.** `getPCSGPodIndexOffset` computes a member PodClique's offset by summing the *live* `spec.replicas` of every preceding clique in `PodCliqueScalingGroup.spec.cliqueNames`. That offset is persisted as `grove.io/podcliquescalinggroup-pod-index-offset` and turned into the `grove.io/podcliquescalinggroup-pod-index` label on each pod. Resizing an earlier member clique therefore shifts the offset of every later member, and `syncPCSGPodIndexLabels` patches the group-wide index label on pods that never moved and were never restarted. Live inner resize — the exact scenario in #793 — is the operation that triggers it.

The consequence is worse than a renumbering, because the index is published twice through two mechanisms with different update semantics. The label is patched in place, but `GROVE_PCSG_POD_INDEX` is a downward-API `fieldRef` reading that same label, and environment-variable `fieldRef`s are resolved once at container start rather than tracked live. After a sibling resize, a running container therefore keeps reporting its original index while the label on its own pod reports a new one, and the two stay divergent until the container happens to restart. A workload that reads its identity from the environment and a controller that reads it from the label will disagree without either observing an error.

This is worth stating plainly because #793 nominates the PCSG-wide pod index as "a promising basis" for capacity identity. It is a promising basis, but not in its current form.

### Goals

- Define a **capacity slot**: a durable logical position within a PodClique or PCSG replica, stable across pod replacement, retry, and live inner resize of sibling cliques.
- Make the PCSG-wide pod index stable under live inner resize, so it can serve as that slot identity.
- Expose a **slot to pod UID mapping** in status, so an external controller can resolve a logical slot to the concrete pod instance currently occupying it, and back.
- Allow an external controller to **authorize the exact pods** a scale-in may delete, bound to immutable UIDs, an operation ID, and an observed generation.
- Make Grove **refuse stale authorization** and **wait rather than substitute** a different victim when no valid authorization covers a requested scale-in.
- Preserve today's behavior by default. Both contracts are opt-in per PodClique.

### Non-Goals

This GREP deliberately covers the identity and release half of #793. The remaining semantics are separable and each deserves its own proposal; listing them here bounds the discussion rather than dismissing them.

- **Separating initial admission from runtime minimum.** #793 asks for a distinction between the capacity required for atomic initial admission and the minimum supported live capacity. That is a change to `minAvailable` semantics affecting gang scheduling and gang termination for every Grove user, and it interacts with [GREP-0677](https://github.com/ai-dynamo/grove/pull/686). It should not ride along with an additive identity contract.
- **Survivor-preserving failure policy.** Suppressing whole-gang termination during a bounded external recovery attempt is a gang-lifecycle policy question, related to [#789](https://github.com/ai-dynamo/grove/issues/789).
- **Staged fleet operations.** Ordering a fleet-wide reshape across PCSG replicas with a disruption budget is a rollout concern, related to the Coherent Updates epic [#776](https://github.com/ai-dynamo/grove/issues/776).
- **Topology-local joining capacity.** Placing a scale-up delta relative to the already-running world overlaps [#648](https://github.com/ai-dynamo/grove/issues/648).
- Grove understanding ranks, engine membership, or engine topology. Engine-active membership stays out of Grove state, as #793 specifies.
- Multi-pod capacity allocations. A slot maps to exactly one pod in this proposal. See [Alternatives](#alternatives) for why, and what would change if that assumption is lifted.

## Proposal

### Capacity slots

A **slot** is a durable logical position. Grove already has the raw material: pods carry `grove.io/podclique-pod-index` within their clique and `grove.io/podcliquescalinggroup-pod-index` within their PCSG replica, and indices are allocated hole-filling from zero so a replacement reuses the index its predecessor freed.

This proposal changes three things about that arrangement and formalizes the rest.

**Slot indices become stable under sibling resize.** The group-wide index of a pod must depend only on its own clique's identity and its own within-clique index — never on how many replicas a sibling clique currently has. See [Design Details](#stable-group-wide-slot-index) for the mechanism.

**Slot occupancy becomes observable.** A slot is a name; the pod occupying it is an instance. The two must be separately visible, because the whole point of UID binding is that a slot can outlive its occupant. Grove publishes the current mapping in `PodClique.status`.

**Slot reuse becomes explicit.** Hole-filling is retained — it is what makes a replacement recover the same identity — but it is now a stated contract rather than an implementation detail of hostname parsing. A pod that reuses a freed index is a *new occupant of the same slot*, with a different UID. That distinction is what makes stale-authorization refusal possible.

### Authorized release

A PodClique may opt into `Authorized` release policy. Under that policy:

- A scale-in is a two-party operation. The external controller writes an authorization naming the exact pod UIDs it has drained; the desired replica count is then reduced through the ordinary `/scale` subresource.
- Grove deletes only authorized UIDs. `DeletionSorter` is not consulted.
- If the desired count is lower than the current count but no valid authorization covers the difference, Grove **waits**. It does not select a substitute victim, and it does not replace the shortfall.
- An authorization naming a UID that no longer exists is refused as stale, including the case where a replacement pod has reused the same slot index and name. This is the ABA case: same slot, same hostname, different instance.
- Authorization is fail-closed with respect to ordering. Writing an authorization does not by itself delete anything, and reducing the replica count without one does not either.

Under the default `Heuristic` policy nothing changes: `DeletionSorter` continues to choose, exactly as today.

### User Stories

#### Draining a degraded member

As an operator of a live MoE engine, I have telemetry showing GPU Xid errors on the pod in slot 3. I want to retire that specific member — not simply "one member" — so that the shrink actually sheds the failing hardware. I instruct the engine to drain rank 3, wait for it to commit a topology without it, then authorize release of that pod's UID and reduce the replica count. Grove deletes that pod and no other.

#### Surviving a crash mid-transaction

As the author of the external controller, my controller restarts after writing an authorization but before reducing the replica count. On restart I read the authorization back from the API, see it is still valid and which operation ID it belongs to, and resume. Had my controller instead crashed after a pod was replaced, the authorization would name a UID that no longer exists, Grove would refuse it, and I would recompute rather than delete a healthy member.

### Limitations/Risks & Mitigations

**A stalled external controller blocks scale-in indefinitely.** "Wait rather than substitute" is the correct default for a live engine, but a controller that dies between reducing the replica count and writing an authorization leaves the PodClique permanently above its desired size. Mitigated by surfacing an `AwaitingReleaseAuthorization` condition with the outstanding count and by an optional `authorizationTimeout` after which Grove emits an event and — only if the workload opts in — falls back to heuristic selection. The default is to wait forever, because silently deleting an unauthorized member of a live engine is worse than a stuck scale-in.

**The index-stability fix is observable to existing workloads.** Any workload today that reads `grove.io/podcliquescalinggroup-pod-index` and happens to resize member cliques would see different values after this change. In practice the current values are unstable precisely in that case, so anything depending on them is already broken; but the change should be called out in release notes rather than treated as a pure bug fix.

**Status size.** Publishing a slot-to-UID mapping grows `PodClique.status` linearly in replica count. For the fleet sizes Grove targets this is small, but it is a real cost and argues against also publishing derived per-slot detail that the external controller can compute itself.

**Two writers on one resource.** The external controller writes authorizations while an autoscaler may write `spec.replicas`. This GREP does not attempt to arbitrate that; it only guarantees that a replica reduction without matching authorization is inert. Workloads using `Authorized` release should not also point a naive autoscaler at the same PodClique.

## Design Details

### Stable group-wide slot index

The defect is that `getPCSGPodIndexOffset` sums live sibling replica counts. Two candidate fixes, in preference order:

**Freeze the offset at PCSG replica creation.** Compute the offset once, from the PodCliqueSet template, and persist it on the PodClique. Subsequent reconciles read the persisted value rather than recomputing. Simple, preserves the existing flattened-integer index, and requires no API change — but a member clique that grows past its template size would collide with the next clique's range unless ranges are allocated with headroom.

**Make the index a pair rather than a sum.** Stop flattening. A slot is identified by `(cliqueName, podIndex)` within a PCSG replica; the group-wide integer becomes a presentation detail computed from a persisted per-clique base. No collision is possible and no headroom must be guessed, at the cost of changing what the existing label means.

The choice belongs to Grove. This GREP requires only the property: **a running pod's slot identity must not change because a sibling clique was resized.** An e2e test asserting exactly that is listed in the [Test Plan](#test-plan).

### API

Additive fields on `PodCliqueSpec`:

```go
// ReleasePolicy determines how Grove selects pods for deletion during scale-in.
// Heuristic (default) uses Grove's built-in deletion preference order.
// Authorized deletes only pods named in a valid release authorization.
// +kubebuilder:validation:Enum=Heuristic;Authorized
// +kubebuilder:default=Heuristic
// +optional
ReleasePolicy *ReleasePolicy `json:"releasePolicy,omitempty"`

// AuthorizationTimeout bounds how long a scale-in waits for a valid release
// authorization before Grove emits an event and falls back to Heuristic selection.
// If unset, Grove waits indefinitely, which is the safe default for a live engine.
// +optional
AuthorizationTimeout *metav1.Duration `json:"authorizationTimeout,omitempty"`
```

Slot occupancy in `PodCliqueStatus`:

```go
// Slots reports the durable logical positions of this PodClique and their current
// occupants. A slot with no OccupantUID is allocated but not yet filled.
// +listType=map
// +listMapKey=index
// +optional
Slots []CapacitySlot `json:"slots,omitempty"`
```

```go
// CapacitySlot is a durable logical position within a PodClique.
type CapacitySlot struct {
	// Index is the within-clique slot index. It is stable for the life of the slot
	// and is reused by a replacement pod.
	Index int32 `json:"index"`
	// GroupIndex is the slot's index within its PodCliqueScalingGroup replica, if any.
	// +optional
	GroupIndex *int32 `json:"groupIndex,omitempty"`
	// OccupantName is the name of the pod currently occupying this slot.
	// +optional
	OccupantName *string `json:"occupantName,omitempty"`
	// OccupantUID is the immutable UID of the pod currently occupying this slot.
	// A slot whose occupant is replaced reports a different UID under the same Index.
	// +optional
	OccupantUID *types.UID `json:"occupantUID,omitempty"`
}
```

### Authorization transport

#### The requirement

#793 leaves the transport to Grove and specifies only the semantics: authorization is bound to immutable pod UIDs, an operation ID, and the committed topology generation; it must be restart-safe; and it must fail closed. Any transport meeting those is acceptable. This GREP proposes the least invasive one and notes the alternative.

#### Proposed: a status-acknowledged spec field

```yaml
apiVersion: grove.io/v1alpha1
kind: PodClique
metadata:
  name: engine-0-worker
spec:
  replicas: 7          # reduced from 8 by the external controller
  releasePolicy: Authorized
  releaseAuthorization:
    operationID: "shrink-ep32-to-ep28-7f3a"
    observedGeneration: 12
    podUIDs:
      - "b4c1f0a2-9d3e-4a17-8c55-0e1f2a3b4c5d"
```

Grove acknowledges in status, so a restarted controller can tell what Grove actually acted on:

```yaml
status:
  releaseAuthorization:
    operationID: "shrink-ep32-to-ep28-7f3a"
    state: Completed          # Pending | Completed | Refused
    refusedReason: ""
    releasedPodUIDs:
      - "b4c1f0a2-9d3e-4a17-8c55-0e1f2a3b4c5d"
```

`observedGeneration` is the external controller's own committed topology generation, opaque to Grove and echoed back for correlation. Grove validates only that every named UID currently exists in the clique.

The alternative — a separate namespaced `PodCliqueReleaseAuthorization` resource — gives independent RBAC and a natural audit trail, at the cost of a new CRD, a new reconciliation edge, and a garbage-collection story. It is the better shape if Grove expects authorization to be written by a principal that must not otherwise mutate `PodClique.spec`. Since `Authorized` release already requires the external controller to write `spec.replicas`, the extra separation buys less than it costs, which is why the field is proposed first.

### Reconciliation

On each PodClique sync where `releasePolicy: Authorized` and `status.replicas > spec.replicas`:

1. Compute `surplus = status.replicas - spec.replicas`.
2. If `spec.releaseAuthorization` is absent, set condition `AwaitingReleaseAuthorization=True` with `surplus` in the message and return. Do not delete.
3. Validate the authorization. Every named UID must belong to a live, non-terminating pod of this clique. Any UID that does not resolve makes the whole authorization stale: set `state: Refused` with a reason, set `AwaitingReleaseAuthorization=True`, and return. Do not delete a partial set.
4. If the authorization names fewer than `surplus` pods, delete exactly the named set and keep waiting for the rest. Deleting a strict subset is safe because every member of that subset was independently drained.
5. If it names more than `surplus`, refuse. Over-authorization signals that the controller's view of the clique disagrees with Grove's.
6. Delete the authorized pods, record them in `status.releaseAuthorization.releasedPodUIDs`, set `state: Completed`.

`DeletionSorter` is never invoked on this path. Scale-*out* is unchanged: Grove allocates the lowest free slot indices as it does today.

Rolling updates are the one interaction worth calling out. Grove's update path also deletes pods, and it uses `DeletionSorter` to prefer outdated ones. Under `Authorized` release an update must not delete a live engine member without authorization either. The conservative rule: a PodClique with `releasePolicy: Authorized` requires `OnDelete` update strategy, so pod replacement is always externally initiated. Relaxing that is future work.

### Monitoring

Conditions on `PodClique`:

| Condition | Meaning |
| --- | --- |
| `AwaitingReleaseAuthorization` | Desired replicas are below current and no valid authorization covers the difference. Message carries the outstanding count. |

Events:

| Reason | When |
| --- | --- |
| `ReleaseAuthorizationRefused` | An authorization named a UID that does not resolve, or named more pods than the surplus. |
| `ReleaseAuthorizationTimedOut` | `authorizationTimeout` elapsed and Grove fell back to heuristic selection. |
| `SlotIndexReassigned` | A slot index changed for a running pod. Under this proposal this should never fire; it exists to make a regression loud. |

Metrics:

- `grove_podclique_slots_occupied` / `grove_podclique_slots_allocated`, gauge, by clique.
- `grove_podclique_release_authorizations_total`, counter, by `state`.
- `grove_podclique_awaiting_release_authorization_seconds`, gauge, age of the oldest outstanding wait. This is the one to alert on.

### Dependencies

None blocking. Related in-flight work that this GREP intentionally does not depend on: [GREP-0677](https://github.com/ai-dynamo/grove/pull/686) scale-to-zero, [#776](https://github.com/ai-dynamo/grove/issues/776) Coherent Updates, [#789](https://github.com/ai-dynamo/grove/issues/789) gang-termination failure classification.

### Test Plan

Unit:

- `getPCSGPodIndexOffset` (or its replacement) returns an unchanged offset for a member clique after a sibling clique's `spec.replicas` changes.
- Authorization validation: unknown UID refused; terminating-pod UID refused; over-authorization refused; subset accepted; empty authorization inert.
- `DeletionSorter` is not reachable from the scale-in path when `releasePolicy: Authorized`.
- Slot status reports a different `OccupantUID` under the same `Index` after replacement.

E2E, with a scheduler backend:

- **Index stability under live inner resize.** A PCSG replica with two member cliques `[a, b]`. Record every pod's `grove.io/podcliquescalinggroup-pod-index`. Scale `a` up. Assert no pod of `b` was relabelled or restarted, and that each surviving container's `GROVE_PCSG_POD_INDEX` still equals the label on its own pod. Both assertions are expected to fail against `main`.
- **Exact victim.** An 8-replica clique under `Authorized`. Authorize the UID of the pod in slot 3, scale to 7, assert the pod in slot 3 is gone and slots 0-2 and 4-7 are untouched — in particular that the newest pod survives, which is the case `DeletionSorter` gets wrong.
- **Stale refusal (ABA).** Authorize a UID, delete that pod out of band so a replacement takes the same slot and hostname, then scale in. Assert the authorization is refused and the replacement survives.
- **Wait, do not substitute.** Scale in with no authorization. Assert no deletion, and `AwaitingReleaseAuthorization=True`, for the duration of the test.
- **Restart safety.** Write an authorization, restart the operator, then reduce replicas. Assert the operation completes once and `releasedPodUIDs` names exactly the authorized set.

A dedicated tracking issue for the e2e suite should be filed once the API shape is agreed.

### Graduation Criteria

This is a new API surface plus a change to an existing labelling behavior, so criteria are on the richer side.

- **Alpha.** Slot status and `Authorized` release implemented behind a feature gate. Index stability fixed and covered by the e2e test above. `Heuristic` remains the default and is unchanged.
- **Beta.** Feature gate on by default. Authorization transport stable — no further changes to field shape or refusal semantics. `authorizationTimeout` behavior validated. Documented in the user guide, including the `OnDelete` requirement. Exercised by at least one external controller against a live engine.
- **GA.** Two releases after beta with no API changes. Multi-pod allocations either supported or explicitly deferred with a documented workaround. Interaction with rolling updates resolved beyond the `OnDelete` restriction.

## Alternatives

**Reuse `controller.kubernetes.io/pod-deletion-cost`.** The upstream ReplicaSet mechanism for influencing scale-in victims. Rejected: it is explicitly advisory — the controller may ignore it and does not guarantee ordering — it expresses a preference rather than an authorization, and it is not bound to a UID, so it cannot express "this instance, not whatever now has this name." It solves a ranking problem; #793 has an authorization problem.

**Order-based conventions — always delete the highest index.** Requires no API at all: the external controller drains the highest-numbered member and Grove deletes from the top. Rejected because it forces the engine to retire by position rather than by health. The motivating case is retiring a *degraded* member, which is at an arbitrary index; a convention that only permits retiring the newest member reproduces the exact failure this proposal exists to prevent.

**Delete the pod directly and let Grove observe it.** The external controller deletes the drained pod itself and lowers the replica count afterward. Rejected: Grove races to replace the pod before the scale-in is observed, and the window is not closable from outside. It also gives Grove no way to distinguish an authorized release from an unexpected failure, which is precisely the distinction a survivor-preserving failure policy will later need.

**Support multi-pod capacity allocations now.** #793 notes that one logical replica allocation may span several pods and that a partial allocation is not usable capacity. Deferred rather than rejected: it is a genuine requirement for engines where one DP replica spans multiple pods, but it changes admission, availability accounting, and release atomicity all at once. The contracts here are forward-compatible — a slot becomes a set of UIDs rather than one, and all-or-nothing release is already the refusal rule for a partial set.

## Appendix

- Tracking issue: [ai-dynamo/grove#793](https://github.com/ai-dynamo/grove/issues/793)
- Dynamo Elastic EP DEP: [ai-dynamo/dynamo#13121](https://github.com/ai-dynamo/dynamo/issues/13121)
- PCSG-wide pod index: [ai-dynamo/grove#754](https://github.com/ai-dynamo/grove/issues/754)
- Hierarchical workload groups: [ai-dynamo/grove#756](https://github.com/ai-dynamo/grove/issues/756)
- Cross-PodGang topology enforcement: [ai-dynamo/grove#648](https://github.com/ai-dynamo/grove/issues/648)
- Current deletion preference order: [`deletionsort.go`](https://github.com/ai-dynamo/grove/blob/main/operator/internal/controller/podclique/components/pod/deletionsort.go)
- Current index allocation: [`operator/internal/index/tracker.go`](https://github.com/ai-dynamo/grove/blob/main/operator/internal/index/tracker.go)

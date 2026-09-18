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
  - [PerReplica ResourceClaims and sparse indices](#perreplica-resourceclaims-and-sparse-indices)
  - [API](#api)
  - [Authorization transport](#authorization-transport)
    - [The requirement](#the-requirement)
    - [Proposed: a status-acknowledged spec field](#proposed-a-status-acknowledged-spec-field)
    - [Ownership caveat: <code>PodClique.spec</code> is Grove-owned and template-derived](#ownership-caveat-podcliquespec-is-grove-owned-and-template-derived)
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

`Authorized` release is opt-in per PodClique and defaults to today's behavior. The slot contract is not opt-in: it changes how an existing label's value is derived for every PCSG member clique. Together they answer the identity half of [#793](https://github.com/ai-dynamo/grove/issues/793) while leaving Grove engine-neutral: Grove never learns what a rank is, only that a particular pod may or may not be released.

## Motivation

Issue [#793](https://github.com/ai-dynamo/grove/issues/793) asks how an external workload controller can resize one `PodClique` inside a specific `PodCliqueScalingGroup` replica while a distributed engine stays live. The motivating consumer is Dynamo-managed Elastic Expert Parallelism ([DEP #13121](https://github.com/ai-dynamo/dynamo/issues/13121)), but nothing below is engine-specific.

The issue enumerates seven required semantics. They are not equally urgent, and they are not equally coupled. Five of them are *policy* questions — how much capacity must be admitted atomically, when a gang may be terminated, how a fleet-wide reshape is staged. One is a *representation* question: can the external controller name a piece of physical capacity at all, and can it hand that name back to Grove as an instruction? The seventh, *Reconciliation and authorization*, is not a semantic but a direct request to maintainers: confirm whether direct scale updates to PCSG-owned PodCliques are a stable integration contract. This GREP assumes an affirmative answer throughout, and that assumption is load-bearing — see [Dependencies](#dependencies) for its collision with GREP-0677.

The representation question comes first, because the policy five are unanswerable without it. A survivor-preserving failure policy needs to report *which* capacity failed. A staged fleet operation needs durable per-world progress keyed to *something*. Every one of those requirements bottoms out in a stable name and a way to bind it to a concrete pod instance.

Two concrete defects motivate the specifics.

**Heuristic victim selection contradicts engine-authorized deletion.** Scale-in currently picks victims with [`DeletionSorter`](https://github.com/ai-dynamo/grove/blob/main/operator/internal/controller/podclique/components/pod/deletionsort.go#L47-L88), which prefers, in order: unscheduled pods; then `Pending` over `Running`; then non-ready pods; then pods carrying an outdated pod-template-hash; and finally — rule 5 — the *newest* pod by creation timestamp. (Its rule 0, already-terminating, cannot decide anything here: `selectExcessPodsToDelete` filters terminating pods out before sorting.)

In a healthy steady-state engine every earlier rule ties, so **rule 5 decides** — and the newest member is typically the one that most recently finished joining the topology, which makes the default preference close to the worst possible choice. An engine that has drained rank 3 and committed a topology without it will instead have rank 7 deleted underneath it.

*This is not only an argument from the code.* The same class of failure was measured on an 8×GB300 cluster running DeepSeek-V2-Lite. The selector under test was not `DeletionSorter` but a Kubernetes Deployment's own replica scale-in — a different implementation of the same idea, and one that shares the property that matters here: no notion of which pod holds a live rank.

- **Uncoordinated shrink deleted a rank-holder.** The engine was drained from 4 ranks to 2 and the replica count lowered. Kubernetes kept a pod that had already been drained and deleted one still holding a live rank. The engine lost a member it was still committed to, and the deployment served nothing for **~2.5 minutes** before recovering.
- **Naming the victims fixed it.** Annotating the drained pods with `controller.kubernetes.io/pod-deletion-cost` before lowering the count sent the correct pods every time: no restarts, serving resumed in ~2s, and the pod holding the original rank survived every shrink across a `2 → 3 → 4 → 3 → 2 → 4 → 2` cycle with a graded request at each step.

The second result is the one that bears on this proposal, and it cuts both ways. It confirms the shape of the fix — *the controller knows which pod is safe to delete, and conveying that is sufficient* — while demonstrating why a deletion-ordering hint is not the mechanism. `pod-deletion-cost` is advisory, is not bound to a pod UID, and in that experiment was applied **by the test harness, not by the operator**: the operator has no engine-control client, so it cannot know which pod was drained. #793 states the same conclusion normatively — *"deletion-ordering hints are not authorization"* — and this is the measurement behind it.

**The PCSG-wide pod index trades stability for contiguity, and live resize needs the opposite trade.** [`getPCSGPodIndexOffset`](https://github.com/ai-dynamo/grove/blob/main/operator/internal/controller/podcliquescalinggroup/components/podclique/sync.go#L220-L241) computes a member PodClique's offset by summing the *live* `spec.replicas` of every preceding clique in `PodCliqueScalingGroup.spec.cliqueNames`. That offset is persisted as `grove.io/podcliquescalinggroup-pod-index-offset` and turned into the `grove.io/podcliquescalinggroup-pod-index` label on each pod. Resizing an earlier member clique therefore shifts the offset of every later member, and `syncPCSGPodIndexLabels` patches the group-wide index label on pods that never moved and were never restarted.

This is deliberate and documented, not an oversight. The user guide states it as intended behavior and prescribes the remedy: *"Grove keeps the indices contiguous in `cliqueNames` order. Downward API environment variables are resolved when their containers start, so applications must restart affected containers after such a scale operation."* The remedy is what does not carry over. #793 is about resizing an engine **while it serves**, and restarting a container is precisely the operation that is unavailable. So the ask here is not a bug report; it is whether the contiguity-over-stability trade can be revisited for the live-resize case, where stability is the property that matters and gaps are acceptable.

Two specifics are worth putting in front of reviewers, because they bear on whether the current scheme can serve as identity at all.

*The label and the environment variable diverge.* The index is published twice with different update semantics: the label is patched in place, but `GROVE_PCSG_POD_INDEX` is a downward-API `fieldRef` reading that same label, and `fieldRef` environment variables are resolved once at container start. After a sibling resize a running container keeps reporting its original index while its own pod's label reports a new one. This is the documented consequence above rather than a hidden trap, but a controller reading the label and a workload reading the environment will disagree with no error surfaced on either side.

*Scale-in can produce duplicate indices, with no resize involved.* Because the offset is the sum of live `spec.replicas` while survivors keep their original within-clique index, removing a middle-index pod collides the ranges. Clique `a` has `replicas: 3` and pods at within-indices `{0,1,2}`; the pod at index 1 becomes NotReady; `a` is scaled to 2; `DeletionSorter` — which has no index criterion — deletes index 1; survivors keep `{0,2}` while `offset(b)` recomputes to 2. The survivor of `a` and the first pod of `b` then both carry `grove.io/podcliquescalinggroup-pod-index=2`. That is the same uniqueness violation the live-offset design was adopted to prevent, reachable on `main` today.

This matters because #793 nominates the PCSG-wide pod index as "a promising basis" for capacity identity. It is a promising basis, but not in its current form.

### Goals

- Define a **capacity slot**: a durable logical position within a PodClique or PCSG replica, stable across pod replacement, retry, and live inner resize of sibling cliques.
- Make the PCSG-wide pod index stable under live inner resize, so it can serve as that slot identity.
- Expose a **slot to pod UID mapping** in status, so an external controller can resolve a logical slot to the concrete pod instance currently occupying it, and back.
- Allow an external controller to **authorize the exact pods** a scale-in may delete, bound to immutable UIDs, an operation ID, and an observed generation.
- Make Grove **refuse stale authorization** and **wait rather than substitute** a different victim when no valid authorization covers a requested scale-in.
- Keep heuristic scale-in the default. `Authorized` release is opt-in per PodClique through `spec.releasePolicy`; a PodClique that does not set it behaves exactly as today.
- Accept that the index-stability fix is **not** opt-in. `grove.io/podcliquescalinggroup-pod-index` is set on every pod of a PCSG member clique unconditionally and re-patched in place on running pods, so changing how the offset is derived changes an existing label's value. This is a deliberate behavior change, called out under [Limitations](#limitationsrisks--mitigations) and intended for release notes.

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

**The index-stability change alters a published contract, but has no released consumers yet.** Contiguity in `cliqueNames` order is documented in the user guide as intended behavior, and the CRD field description for `cliqueNames` states that the order determines the group-wide indices. Changing the scheme invalidates both, and both must be updated alongside it.

The compatibility cost, however, is smaller than it first appears: the label and `GROVE_PCSG_POD_INDEX` were introduced by a single commit (`0be004c`, 2026-09-08) that landed *after* the most recent release, `v0.1.0-alpha.13` (2026-09-05), and no tag contains it. Nothing that reads this label has ever shipped. The change can therefore be made as a pre-release correction to unreleased behavior rather than as a breaking change with a migration story — which is the argument for settling the scheme now, before the current semantics reach a release and acquire consumers.

**An authorized release below `minAvailable` fails, and which way it fails is now changing.** This is the sharpest open problem in the proposal. `minAvailable` defaults to the clique template's `replicas` and is the gang-admission minimum published to the scheduler as `PodGang.MinReplicas`, so full-width atomic admission implies `minAvailable == replicas`. It is immutable across PodCliqueSet updates and is not lowered by a `/scale` write.

For the motivating case — an 8-replica engine clique authorized down to 7 — there are two distinct outcomes depending on whether GREP-0677's validation has landed:

| | `/scale` to 7 | Outcome |
|---|---|---|
| **Today** (no admission webhook covers `PodClique`) | accepted | `scheduledReplicas (7) < minAvailable (8)` sets `MinAvailableBreached=True`; after `terminationDelay` the PCSG controller gang-terminates the **entire PCSG replica** — every member clique of that engine world, not just the released pod. The authorized scale-in destroys the world it was protecting. |
| **Under GREP-0677** (rejects `0 < replicas < minAvailable`, including via `/scale`) | **rejected** | The engine has already drained the rank and issued the authorization; the API then refuses to release the pod. Nothing is destroyed, but the release is unreachable and the drained capacity is stranded. |

The second is the better failure — fast, loud, and non-destructive — but both leave the motivating case unserviceable.

Two mitigations, neither free. The workload can pre-set `minAvailable` to the lowest size it ever intends to run at, which gives up full-width atomic admission — precisely the distinction #793 raises and this GREP lists as a Non-Goal. Or `minAvailable` gains the initial-admission/runtime-minimum split that #793 asks for, which is a change to gang semantics for every Grove user.

To be precise about the coupling, since the Dependencies section states there is no merge dependency: **the identity and authorized-release semantics proposed here are implementable and mergeable on their own.** What they cannot deliver alone is *full-width atomic admission together with runtime shrink* — that combination needs the `minAvailable` split, and until it exists a workload must choose one or the other. That is a stronger coupling to the `minAvailable` Non-Goal than the Non-Goals section currently admits.

**Status size.** Publishing a slot-to-UID mapping grows `PodClique.status` linearly in replica count. For the fleet sizes Grove targets this is small, but it is a real cost and argues against also publishing derived per-slot detail that the external controller can compute itself.

**Two writers on one resource.** The external controller writes authorizations while an autoscaler may write `spec.replicas`. This GREP does not attempt to arbitrate that; it only guarantees that a replica reduction without matching authorization is inert. Workloads using `Authorized` release should not also point a naive autoscaler at the same PodClique.

## Design Details

### Stable group-wide slot index

The instability comes from [`getPCSGPodIndexOffset`](https://github.com/ai-dynamo/grove/blob/main/operator/internal/controller/podcliquescalinggroup/components/podclique/sync.go#L220-L241), which sums the *live* `spec.replicas` of the preceding member cliques.

**Preferred: make the index a pair rather than a sum.** Stop flattening. A slot is identified by `(cliqueName, podIndex)` within a PCSG replica; the group-wide integer becomes a presentation detail computed from a persisted per-clique base. Uniqueness and stability both hold unconditionally, no capacity ceiling has to be declared or guessed, and no new `PodCliqueSet`-level API is required. The cost is the one that matters to existing users: it changes what the existing flattened label means.

**Considered and not proposed: a reserved stride.** Derive each member clique's offset from a declared per-clique capacity rather than from live replica counts, so offsets never move. This is worth naming explicitly because it is the obvious repair and it does not hold up:

- There is nowhere to put the capacity. The PodCliqueSet validating webhook rejects any `AutoScalingConfig` on a clique belonging to a scaling group ([`podcliqueset.go:394-398`](https://github.com/ai-dynamo/grove/blob/main/operator/internal/webhook/admission/pcs/validation/podcliqueset.go#L394-L398)), and `PodCliqueScalingGroupConfig.ScaleConfig.MaxReplicas` bounds PCSG *replicas*, not a member's. A new field would be needed, and it would have to be immutable to make offsets stable — a stronger constraint than the adjacent `PodCliqueTemplateSpec.Spec.Replicas`, which is deliberately mutable.
- Nothing enforces the ceiling. Within-clique allocation is uncapped hole-filling from zero and the group index is a bare `offset + podIndex`, with no group-wide uniqueness check anywhere. Exceeding the declared capacity would produce two pods carrying the same index — silent corruption rather than a validation error.
- Migration is fleet-wide. The offset annotation is reconciled unconditionally on every sync, with no feature gate in either controller, so every existing multi-clique PCSG would be renumbered once at operator upgrade.

**Already tried and rejected: freezing the offset at the template value.** For the record, because it is the first thing a reader will suggest: the initial revision of [#755](https://github.com/ai-dynamo/grove/pull/755) computed the offset once from the PodCliqueSet template and persisted it on the PodClique. Review found that a member clique scaled above its template size gives its new pods indices duplicating the next clique's range, breaking uniqueness within a PCSG replica ([review comment](https://github.com/ai-dynamo/grove/pull/755#discussion_r3888999706)), and the design was replaced by the current live-sum computation before merge (`0be004c`). Grove's own upgrade e2e would catch it: `operator/e2e/yaml/upgrade.yaml` gives `bootstrap` a template `replicas: 1`, `operator/e2e/tests/upgrade/upgrade_test.go` scales it to 2 and asserts the group-index set `{0, 1, 2}` — a template-frozen offset yields `{0, 1, 1}`.

**Interaction with `GROVE_PCSG_TEMPLATE_NUM_PODS`.** Grove injects a second PCSG-wide variable alongside the index: `getPCSGTemplateNumPods` sums the member cliques' *template* `spec.replicas`, and the result is injected as a literal value rather than a `fieldRef`. It is documented as "the total number of pods in the PCSG template" and already carries the caveat that it does not follow live scaling. Any scheme that makes the index space larger than that sum — a reserved stride being the clear case — lets a pod carry `GROVE_PCSG_POD_INDEX >= GROVE_PCSG_TEMPLATE_NUM_PODS` even at initial template size with no scaling at all, which silently breaks the natural `rank < world_size` reading of the pair. The pair-based scheme avoids this by not widening the space. Whichever is chosen, the relationship between the two variables must be restated in the user guide.

The choice belongs to Grove. This GREP requires only the property: **a running pod's slot identity must not change because a sibling clique was resized.** An e2e test asserting exactly that is listed in the [Test Plan](#test-plan).

### PerReplica ResourceClaims and sparse indices

Holes in the within-clique index set are a designed outcome of `Authorized` release, not an anomaly: releasing the pod in slot 3 of 8 leaves occupied indices `{0,1,2,4,5,6,7}` with `spec.replicas` at 7, and survivors are never renumbered because index allocation derives the used set from live pod hostnames.

One shipped Grove feature reads that index as a *dense* replica ordinal and must be adjusted before `Authorized` release ships. A pod's PCLQ-level `PerReplica` ResourceClaim is named from its within-clique index and referenced by name in its own pod spec, but the ResourceClaim component is bounded by `spec.replicas` on both sides: it creates claims only for `0..spec.replicas-1`, and it issues a `DeleteCollection` for any `PerReplica` claim whose index label falls outside that range. With survivors at `{0,…,7}` minus `{3}` and `spec.replicas` at 7, Grove would delete the ResourceClaim that the live pod at index 7 references, and never recreate it.

This is already reachable on `main` — `DeletionSorter` has no index criterion, so an ordinary scale-in can leave the same hole — so it is a pre-existing gap rather than one this proposal introduces. But `Authorized` release makes it the normal case instead of the unlucky one, so the claim lifecycle must key on the *occupied* index set rather than on `[0, spec.replicas)`.

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
    topologyGeneration: 12
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

`topologyGeneration` is the external controller's own committed topology generation, opaque to Grove and echoed back for correlation. It is deliberately **not** named `observedGeneration`: that name is a reserved Kubernetes convention meaning "the `.metadata.generation` this controller last reconciled", it belongs in status, and it is written by the resource's own controller — none of which applies here. Grove validates only that every named UID currently exists in the clique.

#### Ownership caveat: `PodClique.spec` is Grove-owned and template-derived

This is the reason the spec-field transport above is presented as a proposal rather than a recommendation, and it may well sink it.

Both PodClique reconcilers rebuild the child spec from the `PodCliqueSet` template on every sync and preserve exactly one pre-existing field:

```go
if pclqExists {
    // If an HPA is mutating the number of replicas, then it should not be
    // overwritten by the template spec replicas.
    currentPCLQReplicas := pclq.Spec.Replicas
    pclq.Spec = *pclqTemplateSpec.Spec.DeepCopy()
    pclq.Spec.Replicas = currentPCLQReplicas
}
```

This is `podcliquescalinggroup/components/podclique/podclique.go` for PCSG member cliques — reached through `createOrUpdatePCLQs` whenever the update strategy is `OnDelete`, which is exactly the strategy this proposal mandates — and `podcliqueset/components/podclique/podclique.go` for cliques owned directly by the PodCliqueSet, reached unconditionally. The write is persisted: `buildResource` is the mutate function of a `controllerutil.CreateOrPatch`.

`releasePolicy` and `authorizationTimeout` are template fields, so they arrive through `pclqTemplateSpec` and survive the rebuild. **`spec.releaseAuthorization` would not.** It is written directly onto the generated child PodClique by a third party, so the next sync erases it — and under `OnDelete` that sync is guaranteed to run. An authorization could therefore be silently dropped between being written and being acted on, which is precisely the fail-closed property the design is supposed to guarantee.

Three ways out, and the choice is Grove's:

1. **Add `releaseAuthorization` to the preserved set**, alongside `spec.replicas`. Smallest change, but it grows a list that Grove has deliberately kept at one entry.
2. **Carry the authorization in an annotation** rather than spec, if annotations on the generated PodClique are preserved across the rebuild. Needs confirming; the same template-derived concern may apply.
3. **A separate namespaced `PodCliqueReleaseAuthorization` resource.** Not owned by the PodCliqueSet template, so nothing overwrites it; it also gives independent RBAC and a natural audit trail. Costs a new CRD, a new reconciliation edge, and a garbage-collection story.

The GREP originally proposed the spec field on the grounds that the external controller already writes `spec.replicas`, so separation bought little. Given the ownership rule above, option 3 now looks the most likely to be correct, and this proposal defers to Grove on which to take.

### Reconciliation

Grove already computes scale-in surplus in two shapes, and `Authorized` release gates the existing computation rather than introducing a parallel one. A PodCliqueScalingGroup-owned PodClique belongs to a single PodGang and takes a scalar delta from `computePodCountDelta`, which reconciles the live pod count against the create/delete expectations store rather than reading `status.replicas`. A standalone PodClique spans several PodGangs and takes a per-PodGang delta map from `computeCountDeltaByPodGang`, so authorization is evaluated **per PodGang** there, not once per clique. `status.replicas` is deliberately not the input on either path, because it lags in-flight deletions.

On each PodClique sync where `releasePolicy: Authorized` and the computed delta indicates a scale-in:

1. Take `surplus` from the existing per-path computation above, not from `status.replicas`.
2. If `spec.releaseAuthorization` is absent, set condition `AwaitingReleaseAuthorization=True` with `surplus` in the message and return. Do not delete.
3. Validate the authorization. Every named UID must resolve to a pod of this clique that either is live or is already terminating **under this same `operationID`** — the latter exception matters, because a deleted pod stays `Running` with a `deletionTimestamp` for its whole grace period, and without it step 3 would refuse on the next sync the very authorization step 6 just fulfilled. Any UID that resolves to neither makes the whole authorization stale: set `state: Refused` with a reason, set `AwaitingReleaseAuthorization=True`, and return. Do not delete a partial set.
4. If the authorization names fewer than `surplus` pods, delete exactly the named set and keep waiting for the rest. Deleting a strict subset is safe because every member of that subset was independently drained.
5. If it names more than `surplus`, refuse. Over-authorization signals that the controller's view of the clique disagrees with Grove's.
6. Delete the authorized pods, record them in `status.releaseAuthorization.releasedPodUIDs`, set `state: Completed`.

`DeletionSorter` is never invoked on this path. Scale-*out* is unchanged: Grove allocates the lowest free slot indices as it does today.

Rolling updates are the one interaction worth calling out, because they delete pods too — by a different mechanism than scale-in. For a standalone PodClique the update path replaces one ready old-hash pod at a time, chosen oldest-first by `getNextPodToUpdate`, and deletes every non-ready old-hash pod immediately. For a PodClique owned by a PodCliqueScalingGroup the unit of update is the whole PCSG replica: `processPendingUpdates` picks the lowest old ready replica index and deletes that replica's PodCliques outright. **Neither path constructs a `DeletionSorter`** — that sorter is reached only from the two scale-in callers, `selectExcessPodsToDelete` and `buildPerPodGangDeletionTasks`. Its "prefer outdated pods" rule fires only when a scale-in overlaps an in-flight update.

The consequence for this proposal is unchanged, and for PCSG members it is sharper: under `Authorized` release an update must not delete a live engine member without authorization either, and for a member clique the update deletes the entire PCSG replica rather than one pod. The conservative rule for the first iteration is that a PodClique with `releasePolicy: Authorized` should belong to a PodCliqueSet whose `spec.updateStrategy.type` is `OnDelete`, so pod replacement is always externally initiated.

Two consequences of that rule should be stated rather than discovered later. First, **`updateStrategy` is a PodCliqueSet-level field** whose own contract is that it applies uniformly to every standalone PodClique and every PodCliqueScalingGroup in the set; opting one engine clique into `Authorized` therefore puts every other clique in the set — frontend, router, everything — onto manual updates. A per-clique opt-out of Grove-initiated pod replacement is the better long-term shape, and is future work. Second, `OnDelete` is also the strategy under which the PodCliqueScalingGroup controller in-place patches member PodClique specs, which the authorization transport must survive — see [Ownership caveat](#ownership-caveat-podcliquespec-is-grove-owned-and-template-derived).

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

No merge or implementation dependency: nothing proposed here requires another proposal to land first.

One semantic overlap with [GREP-0677](https://github.com/ai-dynamo/grove/pull/686) was open when this proposal was filed. **It is now resolved.**

GREP-0677 previously stated under *Zero Replicas Per Level* that *"a `PodClique` owned by a `PodCliqueScalingGroup` must not be scaled independently, including to zero"*, which would have foreclosed the mechanism this GREP is built on. As of [`f7e5005086`](https://github.com/ai-dynamo/grove/pull/686/commits/f7e5005086) it reads:

> A `PodClique` owned by a `PodCliqueScalingGroup` must not be independently scaled **to zero**; idle it by setting the owning group's `replicas` to `0`. **Non-zero scaling remains allowed when `replicas >= minAvailable`.**

Resizing a member clique while its `PodCliqueScalingGroup` stays at a fixed positive `replicas` is therefore explicitly supported, and the two proposals agree. [#793](https://github.com/ai-dynamo/grove/issues/793) still asks maintainers to confirm that direct scale updates to PCSG-owned PodCliques are intended as a stable integration contract for an external controller; GREP-0677's revised wording is strong evidence that they are, but an explicit confirmation is still wanted.

**The surviving constraint is narrower, and it is the one that matters.** GREP-0677 rejects `0 < replicas < minAvailable` on create, on update, and through the `/scale` subresource. That does not block this proposal's mechanism, but it does bound the widths an elastic workload can reach, and it changes *when* the motivating case fails — see Limitations. Separating `minAvailable`'s admission floor from its runtime minimum is required semantic #1 of [#793](https://github.com/ai-dynamo/grove/issues/793) and a Non-Goal here; the same field's scale-in behaviour is under discussion in [#829](https://github.com/ai-dynamo/grove/issues/829).

Other related in-flight work this GREP does not depend on: [#776](https://github.com/ai-dynamo/grove/issues/776) Coherent Updates, [#789](https://github.com/ai-dynamo/grove/issues/789) gang-termination failure classification.

### Test Plan

Unit:

- `getPCSGPodIndexOffset` (or its replacement) returns an unchanged offset for a member clique after a sibling clique's `spec.replicas` changes. This **inverts** `TestSyncPCSGPodIndexOffsetsUsesCurrentReplicaCounts`, which today asserts the `worker` offset annotation is rewritten `"1"` → `"2"` when the preceding `leader` clique grows. That test encodes the current contract faithfully and is *replaced*, not fixed, by this proposal.
- Authorization validation: unknown UID refused; terminating-pod UID refused; over-authorization refused; subset accepted; empty authorization inert.
- `DeletionSorter` is not reachable from the scale-in path when `releasePolicy: Authorized`.
- Slot status reports a different `OccupantUID` under the same `Index` after replacement.

E2E, with a scheduler backend:

- **Index stability under live inner resize.** A PCSG replica with two member cliques `[a, b]`. Record every pod's `grove.io/podcliquescalinggroup-pod-index`. Scale `a` up. Assert no pod of `b` was relabelled or restarted, and that each surviving container's `GROVE_PCSG_POD_INDEX` still equals the label on its own pod. Both assertions are expected to fail against `main`.
- **Exact victim.** An 8-replica clique under `Authorized`. Authorize the UID of the pod in slot 3, scale to 7, assert the pod in slot 3 is gone and slots 0-2 and 4-7 are untouched — in particular that the newest pod survives, which is the case `DeletionSorter` gets wrong.
- **Stale refusal (ABA).** Authorize a UID, delete that pod out of band so a replacement takes the same slot and hostname, then scale in. Assert the authorization is refused and the replacement survives.
- **Wait, do not substitute.** Scale in with no authorization. Assert no deletion, and `AwaitingReleaseAuthorization=True`, for the duration of the test.
- **Restart safety.** Write an authorization, restart the operator, then reduce replicas. Assert the operation completes once and `releasedPodUIDs` names exactly the authorized set.

**Existing e2e this proposal changes.** `operator/e2e/tests/upgrade/upgrade_test.go` scales the `bootstrap` member clique and then asserts an exact, contiguous group-index set through `waitForPCSGPodIndices`. That assertion *is* the contiguity contract, so it must be rewritten rather than kept green — under any stable-offset scheme the surviving set after scaling `bootstrap` from 2 to 1 is `{0, 2}`, not `{0, 1}`. Reviewers should treat a red `upgrade_test` as the expected signal that the change landed, not as a regression.

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

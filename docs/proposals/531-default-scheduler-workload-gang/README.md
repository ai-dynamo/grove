# GREP-531: Workload API Gang Scheduling for default-scheduler Backend

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Limitations / Risks &amp; Mitigations](#limitations--risks--mitigations)
- [Design Details](#design-details)
  - [Architecture Overview](#architecture-overview)
  - [OperatorConfiguration Extension](#operatorconfiguration-extension)
  - [API Version Strategy](#api-version-strategy)
  - [PodGang Mapping and Updates](#podgang-mapping-and-updates)
  - [Key Control Flow](#key-control-flow)
  - [Validation and API Discovery](#validation-and-api-discovery)
  - [Dependencies](#dependencies)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
  - [Implementation Phases (by Grove dependency baseline)](#implementation-phases-by-grove-dependency-baseline)
    - [Phase 1: v1alpha2 (Grove on <code>k8s.io/api</code> v0.36.x)](#phase-1-v1alpha2-grove-on-k8sioapi-v036x)
    - [Phase 2: Hierarchical, topology, GA-track](#phase-2-hierarchical-topology-ga-track)
      - [Hierarchical Gang via CompositePodGroup](#hierarchical-gang-via-compositepodgroup)
- [Appendix](#appendix)
  - [Sibling integrations](#sibling-integrations)
  - [Upstream Kubernetes KEPs](#upstream-kubernetes-keps)
<!-- /toc -->

## Summary

Add gang scheduling to Grove's existing `default-scheduler` backend (introduced in [GREP-375][grep-375]) by translating each `PodGang` into upstream `scheduling.k8s.io` `Workload` / `PodGroup` resources, so `kube-scheduler` enforces all-or-nothing admission. Opt-in via the existing `KubeSchedulerConfig.GangScheduling` field; no new backend, no framework change, no new user-facing API. Satisfies the Beta criterion already recorded in [GREP-375][grep-375] and is forward-compatible with [KEP-6012 CompositePodGroup][kep-6012] for hierarchical gangs.

## Motivation

Today the `default-scheduler` backend only sets `Pod.Spec.SchedulerName` and provides no gang guarantees, leaving operators on vanilla clusters with no path to gang admission for Grove workloads. Upstream `scheduling.k8s.io` Workload APIs close that gap: [KEP-4671][kep-4671] ships v1alpha1 in Kubernetes 1.35 with the embedded-PodGroup model, then in Kubernetes 1.36 **completely abandons v1alpha1** in favor of v1alpha2 — a decoupled standalone `PodGroup` model that supersedes [KEP-5832][kep-5832] (which KEP-4671 explicitly `replaces`). The gating factor for Grove is when each surface is reachable from Grove's `k8s.io/api` baseline. Grove's `PCS → PCSG → PodClique` hierarchy is a natural consumer of the upcoming hierarchical pieces ([KEP-6012][kep-6012]).

### Goals

- Map each `PodGang` to upstream `Workload` / `PodGroup`.
- Implement `SyncPodGang`, `OnPodGangDelete`, `PreparePod` in the `kube` backend (today no-ops) behind `KubeSchedulerConfig.GangScheduling`.
- **Reject-at-submit** for shapes the chosen API version cannot represent ([GREP-375][grep-375]'s "fail-submit vs pass-through").
- Define **API discovery**, an **escape hatch** for external owners (Kueue, cross-PCS controllers), and a forward path to **hierarchical gang** via [KEP-6012][kep-6012].

### Non-Goals

- Adding a new backend, changing `SchedulerProfile.Name`, or redesigning the framework ([GREP-375][grep-375]).
- New user-facing scheduling API in `PodCliqueSet` / `PodGang`.
- Workload-aware extensions such as [KEP-5732 TAS][kep-5732], topology on `Workload` (future `kube/topology.go`), and CompositePodGroup wiring in **alpha** (forward-compatible only).
- Kueue integration logic; Kueue consumes the `Workload` directly or via the escape hatch described in [PodGang Mapping and Updates](#podgang-mapping-and-updates).
- Reacting to `PodGang` status-only updates (current contract preserved).

## Proposal

When `GangScheduling=true`, the backend:

1. Reconciles a `Workload` per `PodGang` (with one `PodGroupTemplate` per Grove `PodClique`) and a standalone `PodGroup` per `PodGroupTemplate`, with `MinCount = MinReplicas`.
2. In `PreparePod`, sets `Pod.Spec.SchedulingGroup.PodGroupName` to the runtime `PodGroup` name.
3. Runs PodCliqueSet validation &mdash; rejects un-mappable shapes, escape-hatch contract violations, and clusters missing the upstream API.

When `GangScheduling=false`, behavior is unchanged.

### User Stories

1. **No third-party scheduler.** Platform operator on vanilla `kube-scheduler` wants gang admission so partial Grove deployments do not waste GPUs.
2. **Migration target.** Grove user migrates from KAI / Volcano to upstream once [KEP-4671][kep-4671] graduates, with no `PodCliqueSet` change.
3. **External owner** (Kueue, cross-PCS gang controller): see the escape hatch contract in [PodGang Mapping and Updates](#podgang-mapping-and-updates).

### Limitations / Risks &amp; Mitigations

| Risk | Mitigation |
|---|---|
| Upstream KEPs at very different maturity (full table in [Appendix](#upstream-kubernetes-keps)); load-bearing unreleased ones are [KEP-6012][kep-6012] (hierarchical) and [KEP-5732][kep-5732] (TAS). [KEP-4671][kep-4671] is still alpha (`beta: v1.37`, `stable: v1.38`), and v1alpha2 — the only live API version — ships in Kubernetes 1.36 but is not yet in Grove's `k8s.io/api` baseline. | Target v1alpha2 directly once Grove's modules adopt the v0.36.x baseline (skipping the abandoned v1alpha1), and align with sibling integrations on the same upstream APIs (see [Appendix](#sibling-integrations)) so divergence is intentional. |
| [KEP-4671 §PodGroup Creation Ordering][kep-4671] requires `Workload` → `PodGroup` → `Pod` creation order; pods created before the `PodGroup` are marked `UnschedulableAndUnresolvable` and re-enqueued when the `PodGroup` appears. | Backend creates gang resources in `SyncPodGang` **before** `PodGang.Initialized=True`; pods stay scheduling-gated, so the race window is harmless. Owner is `PodGang`, not a Pod. |
| Upstream marks gang shape fields immutable: `Workload.Spec.PodGroupTemplates`, `PodGroupTemplate.Name`, `PodGroup.Spec.SchedulingPolicy`, and `Pod.Spec.SchedulingGroup` are all immutable in v1alpha2. | Grove rejects in-place updates to gang shape and requires recreating the Grove workload; future phases may relax this only if a later upstream version explicitly marks the fields mutable. |

## Design Details

### Architecture Overview

| Surface | Trigger | `default-scheduler` responsibility |
|---|---|---|
| Initialization | Operator startup | Construct backend; unmarshal `KubeSchedulerConfig`; cache RESTMapper. |
| PodCliqueSet validation | PCS create/update webhook | API discovery; reject un-mappable shapes; reject escape-hatch contract violations. |
| Pod preparation | PodClique controller builds Pod | Always set `SchedulerName="default-scheduler"`. If gated on, also set the membership field. |
| PodGang sync | PodGang create / generation change | If gated on, reconcile `Workload` (+ standalone `PodGroup`s in Phase 2). Else no-op. |
| PodGang deletion | Delete event | No-op; cascading delete via owner reference. |

### OperatorConfiguration Extension

`default-scheduler` already exists ([GREP-375][grep-375]); opt in by:

```yaml
scheduler:
  defaultProfileName: default-scheduler
  profiles:
    - name: default-scheduler
      config:
        gangScheduling: true
```

`KubeSchedulerConfig.GangScheduling` already exists in `operator/api/config/v1alpha1/types.go`. No changes to `SchedulerProfile.Name`, its `+kubebuilder:validation:Enum`, or `manager.newBackendForProfile`.

### API Version Strategy

Grove targets **v1alpha2** as the only supported upstream API version. v1alpha1 shipped briefly in Kubernetes 1.35 with an embedded-PodGroup model and `Pod.Spec.WorkloadRef`, but in 1.36 [KEP-4671][kep-4671] **completely abandons v1alpha1** in favor of v1alpha2's decoupled standalone `PodGroup` and `Pod.Spec.SchedulingGroup`; `Pod.Spec.WorkloadRef` is tombstoned in 1.36. Supporting v1alpha1 would mean a code path usable on a single upstream minor (1.35) that the upstream community has explicitly walked away from, so Grove skips it.

The implementation is gated on Grove's `k8s.io/api` bump to v0.36.x (tracked in [#602][issue-602]) so that `Workload`, `PodGroup`, and `Pod.Spec.SchedulingGroup` are available as typed Go fields; alternatively, the backend may use dynamic/unstructured patches. The Grove-facing surface (`gangScheduling: bool`) is unchanged.

### PodGang Mapping and Updates

Flat: one `Workload` per `PodGang`, one `PodGroupTemplate` (and one runtime `PodGroup`) per Grove `PodClique`. This expresses all-or-nothing admission across one `PodGang`'s `PodGroup`s and each `PodGroup`'s `MinReplicas`. Two tiers have no v1alpha2 primitive and are deferred to Phase 2 / [KEP-6012][kep-6012]: a PCS-wide gang (each `PodGang` is an independent `Workload`, matching Grove's existing controller) and the PCSG-replica boundary (flattened into the `Workload`'s `PodGroupTemplates` list).

| Grove source | v1alpha2 target |
|---|---|
| `PodGang.{Name,Namespace}` | `Workload.{Name,Namespace}`; runtime `PodGroup` shares the namespace |
| `PodGang.Spec.PodGroups[i].Name` | `Workload.Spec.PodGroupTemplates[i].Name`; input to runtime `PodGroup.Name` |
| `PodGang.Spec.PodGroups[i].MinReplicas` | `Workload.Spec.PodGroupTemplates[i].SchedulingPolicy.Gang.MinCount` and runtime `PodGroup.Spec.SchedulingPolicy.Gang.MinCount` (copied from template per [KEP-4671][kep-4671]) |
| `PodGang.Spec.PodGroups[i]` | `PodGroup.Spec.PodGroupTemplateRef.Workload = {workloadName: <PodGang>, podGroupTemplateName: <PodGroupName>}` plus inline `SchedulingPolicy` |
| `PodGang` controller owner ref | `Workload` and each runtime `PodGroup` |
| Pod label `grove.io/podgang` | input to runtime `PodGroup.Name` |
| Pod label `grove.io/podclique` | input to runtime `PodGroup.Name` |

Pod-side membership is populated by `PreparePod` from labels in `operator/api/common/labels.go`. `PreparePod` must be able to compute the runtime `PodGroup.Name` without a client lookup:

```text
runtimePodGroupName = "<podgang-name>-<podgroup-name>"
```

where `<podgang-name>` comes from `grove.io/podgang` and `<podgroup-name>` comes from `grove.io/podclique` (the Grove `PodGang.Spec.PodGroups[i].Name`). When the joined name would exceed the DNS-1123 subdomain limit (253 chars) or collide in the namespace, fall back to a truncated name plus a short hash suffix; exact budget and probe deferred to the implementation PR. Validation rejects any generated runtime name that is not a valid upstream `PodGroup` object name.

**Update semantics.** v1alpha2 marks the relevant gang shape fields immutable per [KEP-4671][kep-4671]: `Workload.Spec.PodGroupTemplates` (including add/remove and per-template `Name`), `PodGroup.Spec.SchedulingPolicy`, and `Pod.Spec.SchedulingGroup`. Grove rejects any `PodCliqueSet` update that would change these on the `PodCliqueSet` validation webhook, independent of whether the `Workload` already exists in the cluster.

| Concern | v1alpha2 behavior |
|---|---|
| `MinCount` change | Rejected on `PodCliqueSet` update (`SchedulingPolicy` is immutable); recreate the `PodCliqueSet` to change the gang admission threshold. |
| Add / remove `PodGroup` (Grove `PodClique`) | Rejected on `PodCliqueSet` update (`Workload.Spec.PodGroupTemplates` is immutable); recreate the `PodCliqueSet` to change the `PodClique` set. |
| Update safety | No automatic delete-recreate of gang resources after creation; Grove always defers to upstream immutability. |

`PCSG` replica scaling (changing `PodCliquesScalingGroup.Spec.Replicas`) is **not** a gang shape change: it creates or deletes whole `PodGang` instances, each of which keeps its own `PodClique` set fixed. The immutability rules above apply per `PodGang`, not across the PCS.

**Escape hatch.** A `PodCliqueSet` may opt into gang scheduling but delegate `Workload` / `PodGroup` lifecycle to an external owner (Kueue, cross-PCS controller, future hierarchical wrapper). This follows the pattern sibling integrations have converged on for the same upstream APIs (see [Appendix](#sibling-integrations)). Contract:

- Pre-set `Pod.Spec.SchedulingGroup` in the template &rarr; `PreparePod` preserves it; the PodGang builder marks the generated `PodGang` with `grove.io/external-workload-managed="true"`; `SyncPodGang` skips Workload reconciliation when that label is present; the external owner creates the matching resources.
- Pre-set membership **without** `GangScheduling=true` &rarr; rejected, keeping the flag as the single source of truth.

When `grove.io/external-workload-managed="true"`, validation keeps only `GangScheduling=true` and `schedulerName` routing checks; all Grove-internal capacity / naming / topology / hierarchy checks are skipped because the external owner expresses shapes Grove's built-in mapping cannot.

No new Grove API; doubles as the alpha workaround for use cases the built-in mapping cannot yet express.

### Key Control Flow

The backend is selected per-`PodGang` by the framework via the `grove.io/scheduler-name` label ([GREP-375][grep-375]). The four backend trigger points fire in this order for a typical PCS lifecycle:

1. **`PodCliqueSet` admission (validation webhook).** Resolves `scheduling.k8s.io` GVKs through the cached RESTMapper built at `Init()`. Rejects un-mappable shapes, escape-hatch contract violations, missing upstream API, and updates that would change immutable gang-shape fields. Full rules in [Validation and API Discovery](#validation-and-api-discovery).
2. **`PreparePod` (PodClique controller).** Always sets `Pod.Spec.SchedulerName = "default-scheduler"`. If `GangScheduling=true` and the Pod template has no pre-set membership, populate `Pod.Spec.SchedulingGroup.PodGroupName = <runtimePodGroupName>`, computed locally (no client lookup) from labels in `operator/api/common/labels.go`. Pre-set membership is preserved (escape hatch).
3. **`SyncPodGang` (create / generation change; status-only updates ignored).** `GangScheduling=false` &rarr; no-op (bit-for-bit identical to current `main`). `grove.io/external-workload-managed="true"` &rarr; no-op. Otherwise compute the desired `Workload` and standalone `PodGroup`s, create if absent (in the order `Workload` &rarr; `PodGroup`s, matching [KEP-4671 §PodGroup Creation Ordering][kep-4671]). Spec patches are not issued because v1alpha2 marks gang shape immutable; immutable gang-shape changes are blocked at step 1. Owner is the `PodGang`, anchoring the `PCS &rarr; PodGang &rarr; Workload / PodGroup` cascade.
4. **`OnPodGangDelete`.** No-op; cleanup via the owner-ref cascade.

The implementation mirrors the proven `kai/backend.go` + `kai/topology.go` split. `SyncPodGang` becomes a thin gate-check delegating to `workload.go`:

```text
operator/internal/scheduler/kube/
├── backend.go        # existing; thin Backend interface impl
├── backend_test.go   # existing
├── workload.go       # new; buildWorkload, syncWorkload, RESTMapper helpers
├── workload_test.go  # new
└── topology.go       # reserved for KEP-5732 Workload-aware TAS (Phase 3)
```

### Validation and API Discovery

`ValidatePodCliqueSet` rejects:

- Any `topologyConstraint` while v1alpha2 has no topology surface; Grove's current topology API is a hard `packDomain` requirement, so accepting it would silently degrade. Points at [KEP-5732][kep-5732].
- Pre-set membership without `GangScheduling=true`.
- Missing upstream API.
- More than 8 Grove `PodGroup`s in a single `PodGang`, matching upstream `WorkloadMaxPodGroups = 8` ([KEP-4671][kep-4671]). Base-`PodGang` count is `#standalone_cliques + Σ_pcsg (pcsg.MinAvailable × #cliques_in_pcsg)`, so a moderately-sized disaggregated PCS (e.g. 1 router + 1 PCSG with `MinAvailable=3` and 2 cliques = 7) can already approach the cap; treat it as a real design constraint.
- Names that cannot be represented: the generated runtime `PodGroup.Name` must be a valid DNS subdomain and non-colliding within the namespace.
- Updates that change gang shape fields v1alpha2 marks immutable (`Workload.Spec.PodGroupTemplates`, `PodGroupTemplate.Name`, `PodGroup.Spec.SchedulingPolicy`, `Pod.Spec.SchedulingGroup`).

`schedulerName` mismatch is already handled by the framework webhook ([GREP-375][grep-375]) and not duplicated here.

API discovery uses a cached RESTMapper built at `Init()`; the webhook resolves `scheduling.k8s.io` GVKs via that cache, `NoMatchError` invalidates the cache and retries once, and a second miss rejects validation with an error naming the missing GVK and pointing at [KEP-4671][kep-4671]. This mirrors the pattern sibling integrations have converged on (see [Appendix](#sibling-integrations)).

### Dependencies

This feature depends on Kubernetes 1.36+ serving `scheduling.k8s.io/v1alpha2` `Workload` and `PodGroup`, with the `GenericWorkload` and `GangScheduling` feature gates enabled on `kube-apiserver` and `kube-scheduler` per [KEP-4671][kep-4671]. Grove's Go dependencies must include `Pod.Spec.SchedulingGroup` (`k8s.io/api` v0.36.x, tracked in [#602][issue-602]), unless the implementation intentionally uses dynamic/unstructured patches.

Required RBAC:

| API group | Resource | Verbs | Purpose |
|---|---|---|---|
| `scheduling.k8s.io` | `workloads` | create, get, list, watch, delete | `PodGang` &rarr; `Workload` reconciliation. `update`/`patch` are not requested because gang shape is immutable in v1alpha2. |
| `scheduling.k8s.io` | `podgroups` | create, get, list, watch, delete | Standalone `PodGroup` reconciliation. `update`/`patch` are not requested because `PodGroup.Spec` is immutable in v1alpha2. |

No new metrics are required for alpha. Validation failures should be returned directly by the existing webhook path; reconciliation failures should surface through existing controller logs, events, and PodGang readiness behavior. A follow-up may add Workload-specific status conditions or metrics once the upstream API shape is stable enough to avoid churn.

### Test Plan

**Unit.** `PreparePod` sets `SchedulerName` unconditionally and `Pod.Spec.SchedulingGroup.PodGroupName` only when gated on; preserves pre-set membership. `SyncPodGang` produces correct mapping and owner ref; is bit-for-bit identical to current `main` when gated off; skips reconciliation in escape-hatch mode. `ValidatePodCliqueSet` rejects any topology constraint while v1alpha2 lacks topology support, pre-set membership without the flag, missing upstream API, more than 8 `PodGroup`s per `PodGang`, generated runtime `PodGroup` names that are not valid DNS subdomains or that collide, and updates to immutable gang shape. API discovery: missing GVK &rarr; clear error; later install of the API succeeds without restart.

**E2E.** Gated on: Pods carry `Pod.Spec.SchedulingGroup.PodGroupName`; `Workload` and `PodGroup`s are created in the order `Workload` &rarr; `PodGroup` &rarr; `Pod` per [KEP-4671][kep-4671]; Pods gated until admitted; PCS delete cascades. Gated off: no `Workload` created; pass-through matches today. Escape hatch: pre-set `SchedulingGroup` &rarr; backend creates no `Workload` or `PodGroup`.

### Graduation Criteria

**Alpha.** `SyncPodGang` / `PreparePod` / `ValidatePodCliqueSet` implemented behind `GangScheduling`; unit tests cover mapping, gate-off identity, escape hatch, API discovery; Workload-specific code in `kube/workload.go`.

**Beta.** Upstream `Workload` / `PodGroup` APIs reach at least beta, or Grove explicitly accepts the risk of continuing to depend on alpha upstream APIs; E2E on at least one upstream Kubernetes minor that enables the Workload feature by default; follow-up GREP for hierarchical gang via [KEP-6012][kep-6012] drafted; user-facing migration guide from KAI / Volcano.

**GA.** `scheduling.k8s.io` Workload reaches v1 (or the project explicitly commits to its alpha state); multiple production reports.

### Implementation Phases (by Grove dependency baseline)

Phases align Grove's rollout with the `k8s.io/api` baseline Grove's modules are pinned to. v1alpha2 ships in Kubernetes 1.36; the gating factor is when Grove's modules adopt that baseline. Phases are orthogonal to the [Graduation Criteria](#graduation-criteria) above. The user-facing `gangScheduling: bool` is unchanged across phases.

#### Phase 1: v1alpha2 (Grove on `k8s.io/api` v0.36.x)

- Prerequisite: Grove's Go modules must be bumped to `k8s.io/api` v0.36.x (tracked in [#602][issue-602]); Phase 1 implementation is conditional on that bump landing first.
- Baseline: `k8s.io/api` v0.36.x. `Workload`, standalone `PodGroup`, `Workload.Spec.PodGroupTemplates`, and `Pod.Spec.SchedulingGroup` are all available; v1alpha1's `Pod.Spec.WorkloadRef` is tombstoned upstream and is not used.
- Mapping: flat &mdash; one `Workload` per `PodGang`, one `PodGroupTemplate` (and runtime `PodGroup`) per Grove `PodClique`.
- Pod-side membership: `Pod.Spec.SchedulingGroup.PodGroupName`.
- Update semantics: `Workload.Spec.PodGroupTemplates`, `PodGroupTemplate.Name`, `PodGroup.Spec.SchedulingPolicy`, and `Pod.Spec.SchedulingGroup` are all immutable upstream; gang shape changes are rejected and require recreating the Grove workload.
- Scope: `SyncPodGang` / `PreparePod` / `ValidatePodCliqueSet`, escape hatch, API discovery, RBAC, `WorkloadMaxPodGroups = 8` validation.
- Explicitly deferred: topology, hierarchical gang, in-place gang shape mutation.

#### Phase 2: Hierarchical, topology, GA-track

Each item is independently gated on the corresponding upstream KEP and can land as a separate sub-GREP / sub-PR.

- [KEP-6012 CompositePodGroup][kep-6012]: map `PCS -> PCSG -> PCSG replica -> PodClique` onto `CompositePodGroup` layers.
- [KEP-5732 Workload-aware TAS][kep-5732]: introduce `operator/internal/scheduler/kube/topology.go`; convert today's topology-constraint rejection in [Validation and API Discovery](#validation-and-api-discovery) into actual constraint propagation onto the `Workload`.
- GA target: when `scheduling.k8s.io` graduates `Workload` / `PodGroup` to `v1`.

##### Hierarchical Gang via CompositePodGroup

Grove's `PodCliqueSet → PodCliquesScalingGroup → PCSG replicas → PodClique` is a near-literal match for [KEP-6012 CompositePodGroup][kep-6012]:

```text
CompositePodGroup <pcs-name>                        # PCS-wide gang
├─ CompositePodGroup <pcs-name>-<pcsg>              # MinCount = PCSG MinAvailable
│  ├─ CompositePodGroup <pcs-name>-<pcsg>-0         # PCSG replica 0
│  │   ├─ PodGroup <pcs-name>-<pcsg>-0-prefill      # leaf, MinCount = PodClique.MinReplicas
│  │   └─ PodGroup <pcs-name>-<pcsg>-0-decode
│  └─ CompositePodGroup <pcs-name>-<pcsg>-1
└─ PodGroup <pcs-name>-<standalone-clique>
```

Future Work, not Phase 1. The flat mapping is forward-compatible: a hierarchical controller can later wrap the per-`PodGang` `Workload`s in parent `CompositePodGroup`s without invalidating Pod-side state. Grove's two-to-three-tier hierarchy is the main reason it needs `CompositePodGroup` where flatter sibling integrations on the same upstream APIs do not (see [Appendix](#sibling-integrations)).

## Appendix

**Grove.** [GREP-375 Scheduler Backend Framework][grep-375] (this GREP plugs into it; its Beta criterion explicitly anticipates this work); tracking issue [#531][issue-531]; umbrella [#395][issue-395]; [PR #532][pr-532] (DRAFT, predates this GREP &mdash; decisions here supersede #532 where they differ); sibling in-flight backend GREPs: KAI ([PR #553][pr-553]), Volcano ([#571][issue-571]), Koordinator ([#537][issue-537]).

### Sibling integrations

Prior art on the same upstream `scheduling.k8s.io` Workload APIs informs the lifecycle invariants, escape hatch, API discovery, and per-`PodGroup` gang policy adopted here: [LWS PR #844 (KEP-666)][lws-844] (most directly comparable design), [LWS KEP-766 DisaggregatedSet][lws-disaggregatedset] (per-`PodGroup` minAvailability), and [JobSet PR #1068][jobset-1068] (sibling consumer; predates the v1alpha2 cutover but informs the integration shape).

### Upstream Kubernetes KEPs

| KEP | Title | Status |
|---|---|---|
| [KEP-4671][kep-4671] | Gang Scheduling | Alpha; v1alpha1 shipped in Kubernetes 1.35 and was abandoned in 1.36 in favor of v1alpha2; target beta 1.37, stable 1.38 |
| [KEP-5558][kep-5558] | Workload API | Alpha (alongside 4671) |
| [KEP-5832][kep-5832] | Decouple PodGroup API (v1alpha2) | Superseded; [KEP-4671 `kep.yaml`][kep-4671] lists it under `replaces:`. The decoupled v1alpha2 surface lives in KEP-4671 |
| [KEP-6012][kep-6012] | CompositePodGroup (hierarchical) | **Design / unreleased** |
| [KEP-5732][kep-5732] | Workload-aware Topology-Aware Scheduling | **Design / unreleased** |

[grep-375]: ../375-scheduler-backend-framework/README.md
[issue-531]: https://github.com/ai-dynamo/grove/issues/531
[issue-395]: https://github.com/ai-dynamo/grove/issues/395
[issue-602]: https://github.com/ai-dynamo/grove/issues/602
[pr-532]: https://github.com/ai-dynamo/grove/pull/532
[pr-553]: https://github.com/ai-dynamo/grove/pull/553
[issue-571]: https://github.com/ai-dynamo/grove/issues/571
[issue-537]: https://github.com/ai-dynamo/grove/issues/537
[kep-4671]: https://github.com/kubernetes/enhancements/tree/master/keps/sig-scheduling/4671-gang-scheduling
[kep-5558]: https://github.com/kubernetes/enhancements/pull/5558
[kep-5832]: https://github.com/kubernetes/enhancements/tree/master/keps/sig-scheduling/5832-decouple-podgroup-api
[kep-6012]: https://github.com/kubernetes/enhancements/issues/6012
[kep-5732]: https://github.com/kubernetes/enhancements/issues/5732
[lws-844]: https://github.com/kubernetes-sigs/lws/pull/844
[lws-disaggregatedset]: https://github.com/kubernetes-sigs/lws/tree/main/keps/766-DisaggregatedSet
[jobset-1068]: https://github.com/kubernetes-sigs/jobset/pull/1068

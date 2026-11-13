# GREP-537: Koordinator Scheduler Backend

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Scope and Limitations](#scope-and-limitations)
- [Proposal](#proposal)
- [Design Details](#design-details)
  - [Architecture](#architecture)
  - [PodGang → Koordinator PodGroup Translation](#podgang--koordinator-podgroup-translation)
    - [Naming convention](#naming-convention)
    - [GangGroup aggregation](#ganggroup-aggregation)
    - [PodGroup spec fields](#podgroup-spec-fields)
    - [Base vs Scaled PodGang mapping](#base-vs-scaled-podgang-mapping)
  - [Pod Preparation](#pod-preparation)
  - [Topology Constraint Translation](#topology-constraint-translation)
    - [Required vs Preferred policy](#required-vs-preferred-policy)
    - [Key-to-layer mapping](#key-to-layer-mapping)
  - [Admission Validation](#admission-validation)
  - [OperatorConfiguration Extension](#operatorconfiguration-extension)
  - [Lifecycle and Ownership](#lifecycle-and-ownership)
  - [RBAC](#rbac)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [E2E Tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
- [Alternatives](#alternatives)
  - [Users write Koordinator annotations directly](#users-write-koordinator-annotations-directly)
  - [Map PodGang to a single PodGroup](#map-podgang-to-a-single-podgroup)
  - [Wait for Koordinator to natively support PodGang](#wait-for-koordinator-to-natively-support-podgang)
- [Appendix](#appendix)
  - [Reference: Koordinator annotations used](#reference-koordinator-annotations-used)
  - [Reference: Example translation](#reference-example-translation)
<!-- /toc -->

## Summary

This GREP, tracked by [issue #537](https://github.com/ai-dynamo/grove/issues/537), proposes `koord-scheduler` as a first-class Grove scheduler backend, built on top of the Scheduler Backend Framework defined in [GREP-375](../375-scheduler-backend-framework/README.md). The backend lets Grove workloads run on clusters that use [Koordinator](https://koordinator.sh/) as the scheduler, obtaining gang scheduling, single-layer topology-aware placement, and optional QoS-class injection without requiring users to hand-author any Koordinator-specific YAML.

The backend translates Grove `PodGang` resources into a GangGroup of sig-scheduler-plugins `PodGroup` CRs (the format Koordinator's coscheduling plugin consumes), sets `Pod.Spec.SchedulerName = "koord-scheduler"` on each constituent Pod, injects the `pod-group.scheduling.sigs.k8s.io` association label, optionally translates a single topology constraint into Koordinator's `network-topology-spec` annotation, and rejects incompatible configurations (notably MNNVL and PCSG-level topology constraints) at admission.

## Motivation

Koordinator is a widely deployed enhanced scheduler with a mature gang-scheduling and network-topology-aware feature set. Many platform teams already run Koordinator for non-AI workloads and want to onboard AI workloads onto the same scheduler rather than deploying a second advanced scheduler in parallel. Grove today supports `kai-scheduler` as its advanced-scheduler backend; it does not support Koordinator.

Without a dedicated backend, the only way to run a Grove workload on a Koordinator cluster is to fall back to `default-scheduler`, which loses gang guarantees and topology-aware placement — both of which are essential correctness properties for distributed training and inference workloads.

With GREP-375 landed, Grove now has a clean extension surface for scheduler backends: `SyncPodGang`, `PreparePod`, `ValidatePodCliqueSet`, and `Init`. This GREP defines how Grove should use that surface for Koordinator.

### Goals

- **Gang scheduling on Koordinator**: Every Grove `PodGang` is realised as a Koordinator GangGroup, so all constituent pods are scheduled together or not at all, subject to Koordinator's `Strict`/`NonStrict` modes.
- **`SyncPodGang` translation**: For each `PodGroup` in a `PodGang`, create one Koordinator `PodGroup` CR (named `{podgang}-{podgroup}`), linked into a GangGroup via the `gang.scheduling.koordinator.sh/groups` annotation.
- **`PreparePod` injection**: Set `Pod.Spec.SchedulerName`, inject the `pod-group.scheduling.sigs.k8s.io` label, and optionally inject the `koordinator.sh/qosClass` label when configured.
- **Single-layer topology-aware placement**: Translate Grove topology intent into the `network-topology-spec` annotation on the generated PodGroup CRs. The user-facing `PodCliqueSet` API expresses this as `topologyConstraint.packDomain`; the PodCliqueSet controller resolves it through `ClusterTopology` into an internal `PodGang.Spec.TopologyConstraint.PackConstraint.Required` topology key.
- **Admission rejection for incompatible features**: Reject PodCliqueSets that request features this backend fundamentally cannot honour (MNNVL and PCSG-level topology constraints) via `ValidatePodCliqueSet`, producing a clear error at `kubectl apply` time rather than silently mis-scheduling the workload.
- **Configurable backend knobs**: Expose a `KoordinatorSchedulerConfiguration` type under `SchedulerProfile.Config` covering gang mode, match policy, schedule timeout, default QoS class, and user-supplied topology-key-to-layer mappings.
- **Cleanup via owner references**: Koordinator PodGroup CRs are owned by the PodGang they are generated from, so deleting the PodGang garbage-collects the PodGroups — `OnPodGangDelete` is a no-op.

### Scope and Limitations

- **MNNVL / ComputeDomain is not supported**: Multi-Node NVLink depends on NVIDIA DRA `ComputeDomain` and ResourceClaims, which are incompatible with Koordinator's DeviceShare model. `ValidatePodCliqueSet` rejects workloads annotated with `grove.io/auto-mnnvl: enabled`.
- **Topology support is intentionally narrow**: The user-facing API supports one `topologyConstraint.packDomain` per scope. The PodCliqueSet controller resolves that into an internal Required topology key, and this backend maps that key to one Koordinator gather rule. Multi-layer rules, `PodCountMultiple`, pod-index alignment, and user-authored Preferred topology need a follow-up Grove API change.
- **PCSG-level topology is not representable**: One Grove PodGang becomes one Koordinator GangGroup. Koordinator cannot apply separate topology constraints to disjoint subsets inside that GangGroup, so `PodCliqueScalingGroupConfig.TopologyConstraint` is rejected at admission and internal `TopologyConstraintGroupConfigs` fail closed at reconcile time.
- **Only three Koordinator topology layers are configured in this version**: topology keys can map to `hostLayer`, `rackLayer`, or `blockLayer`. Arbitrary Koordinator layers such as `acceleratorLayer`, `spineLayer`, or `datacenterLayer` require widening the backend configuration and validation.
- **Fine-grained GPU sharing is out of scope**: Whole-card `nvidia.com/gpu` requests are left unchanged because Koordinator already recognises them. The backend does not translate Grove workloads to `koordinator.sh/gpu-core` or `koordinator.sh/gpu-memory-ratio`.
- **ClusterNetworkTopology lifecycle is external**: Koordinator's network-topology plugin reads cluster-scoped topology CRDs managed by Koordinator/operator tooling. Grove does not create or update them and does not implement `TopologyAwareSchedBackend`.
- **Other Koordinator subsystems are out of scope**: Reservation, ElasticQuota, NUMA scheduling, and Pod migration are not part of the scheduler backend contract. `ReuseReservationRef` is logged and skipped in the first version.

## Proposal

The Koordinator backend will be implemented in `operator/internal/scheduler/koordinator/` as a `scheduler.Backend` from GREP-375. It will be registered in the scheduler manager next to `kai-scheduler` and `default-scheduler`, enabled per cluster via `OperatorConfiguration.scheduler.profiles`.

No changes to Grove's user-facing `PodCliqueSet` API are required: the user writes the same PodCliqueSet YAML as for any other backend, and selects the backend either by listing `koord-scheduler` as the default profile or by setting `schedulerName: koord-scheduler` on individual PodClique templates.

## Design Details

### Architecture

```
 PodCliqueSet (user-facing)
        │
        ▼
 PodCliqueSet Controller  ──▶  PodGang  (scheduler.grove.io)
                                   │
                                   ▼
                         Backend Controller
                                   │
                                   ▼
                   koord-scheduler backend (this GREP)
                                   │
                  ┌────────────────┼────────────────┐
                  ▼                ▼                ▼
             PodGroup CR      PodGroup CR      PodGroup CR
        (scheduling.sigs.k8s.io/v1alpha1, linked by GangGroup annotation)
                  │                │                │
                  └────────────────┴────────────────┘
                                   │
                                   ▼
                           koord-scheduler
```

The Grove PodCliqueSet → PodGang translation is unchanged from GREP-375. This GREP only owns the `PodGang → Koordinator PodGroup CR` step and the per-Pod preparation.

### PodGang → Koordinator PodGroup Translation

#### Naming convention

For a PodGang named `<podgang>` containing a `PodGroup` named `<podgroup>` (where the Grove PodGroup maps to a PodClique), the Koordinator `PodGroup` CR is created in the same namespace with name:

```
{podgang}-{podgroup}
```

This convention is symmetric: `PreparePod` reads the pod's `grove.io/podgang` and `grove.io/podclique` labels and computes the same name to inject into the `pod-group.scheduling.sigs.k8s.io` Pod label. No separate registry or index is needed.

#### GangGroup aggregation

Koordinator's coscheduling plugin treats a set of PodGroups as a single atomic gang when each of them carries the `gang.scheduling.koordinator.sh/groups` annotation with a JSON array listing the full membership.

For each PodGroup CR generated from a PodGang, the backend stamps:

```yaml
gang.scheduling.koordinator.sh/groups: '["<ns>/<podgang>-<pg1>","<ns>/<podgang>-<pg2>", ...]'
gang.scheduling.koordinator.sh/mode: Strict           # configurable
gang.scheduling.koordinator.sh/match-policy: once-satisfied  # configurable
gang.scheduling.koordinator.sh/total-number: "<N>"    # this PodGroup's total child pod count, when known
```

Koordinator defines `gang.scheduling.koordinator.sh/total-number` as the total children number for an individual gang, not the number of PodGroups in a GangGroup. Grove therefore derives it from `len(PodGroup.PodReferences)` when that value is known and is not less than `PodGroup.MinReplicas`; it is omitted rather than guessed from GangGroup size.

#### PodGroup spec fields

```yaml
apiVersion: scheduling.sigs.k8s.io/v1alpha1
kind: PodGroup
metadata:
  name: {podgang}-{podgroup}
  namespace: <same as PodGang>
  labels:
    grove.io/podgang: <podgang>        # used by prune to narrow the List scope
  annotations: <as above>
  ownerReferences:
    - controller: true                 # owned by the PodGang
spec:
  minMember: <PodGroup.MinReplicas>
  scheduleTimeoutSeconds: 30           # configurable
  priorityClassName: <optional>
```

Note: Koordinator's coscheduling plugin reads `MinMember` to decide how many pods must arrive before gang scheduling is attempted. It does **not** consume `MinResources`, so the backend does not populate it. The Grove PodGang itself has no schedule-timeout field, so the backend uses a configurable backend-wide default.

#### Base vs Scaled PodGang mapping

Grove's PodCliqueSet controller splits PodCliqueScalingGroup (PCSG) replicas into:

- one **Base PodGang** per PodCliqueSet replica, containing the standalone cliques plus each PCSG's indices `[0, MinAvailable-1]`;
- one **Scaled PodGang** per PCSG replica beyond `MinAvailable-1` (one PodGang per replica).

This split is entirely upstream of the backend. The backend simply sees one PodGang at a time and emits one GangGroup per PodGang. Every PodGang therefore becomes one Koordinator GangGroup; scaled replicas get their own GangGroup.

### Pod Preparation

`PreparePod` is called by the PodClique controller at Pod creation time. It performs three actions:

1. Set `Pod.Spec.SchedulerName = "koord-scheduler"` so the pod is picked up by koord-scheduler.
2. Inject `pod-group.scheduling.sigs.k8s.io: {podgang}-{podclique}` into Pod labels. This is the label the sig-scheduler-plugins coscheduling plugin (and Koordinator, which extends it) uses to associate a Pod with its PodGroup CR.
3. Optionally inject `koordinator.sh/qosClass: <value>` when `KoordinatorSchedulerConfiguration.DefaultQoSClass` is configured.

### Topology Constraint Translation

`buildTopologyAnnotation` is invoked twice per reconcile:

1. once for the PodGang-level `TopologyConstraint` (the global annotation, applied to every PodGroup unless overridden);
2. once per PodGroup for the per-PodGroup `TopologyConstraint` (takes precedence over the global one).

The result is a JSON value assigned to the `gang.scheduling.koordinator.sh/network-topology-spec` annotation on the PodGroup CR.

#### Required vs Preferred policy

- `PackConstraint.Required` with a key that has no Koordinator layer equivalent is a **hard error**: `SyncPodGang` returns an error and the reconciler retries. Silently dropping a mandatory constraint would schedule the workload in a topology the user explicitly prohibited.
- `PackConstraint.Preferred` with an unmappable key is **logged at Info level and skipped** — it is advisory, not binding.

#### Key-to-layer mapping

Built-in (exact-match) mappings:

| Grove topology key | Koordinator layer |
|---|---|
| `kubernetes.io/hostname` | `hostLayer` |
| `topology.kubernetes.io/rack` | `rackLayer` |
| `topology.kubernetes.io/block` | `blockLayer` |

Substring matching is intentionally **not** used — `Contains("host")` would silently mis-map unrelated keys such as `nfs-hostpath`.

Cluster operators can add key aliases via `KoordinatorSchedulerConfiguration.TopologyKeyMappings` (a `map[string]string` from Grove topology key to Koordinator layer name). User mappings override built-ins, but this implementation intentionally validates mapped values against `{hostLayer, rackLayer, blockLayer}`. It therefore does not expose arbitrary Koordinator topology layers such as `acceleratorLayer`, `spineLayer`, or `datacenterLayer`; supporting those layers would require widening the backend configuration and validation.

### Admission Validation

`ValidatePodCliqueSet` runs at the `PodCliqueSet` validating webhook:

- **MNNVL rejection**: If the PodCliqueSet carries `grove.io/auto-mnnvl: enabled`, the webhook returns an error instructing the user to either remove the annotation or switch to `kai-scheduler`.
- **PCSG topology rejection**: If any `PodCliqueScalingGroupConfig` sets `topologyConstraint`, the webhook returns an error because this backend cannot represent per-scaling-group topology constraints within one Koordinator GangGroup.
- **Unknown Required topology key**: This check is done in `SyncPodGang` (reconcile time) rather than admission, because the backend receives resolved PodGang topology keys and applies its own key-to-layer mapping after PodGang creation.

### OperatorConfiguration Extension

A new scheduler name is registered next to `kai-scheduler` and `default-scheduler`:

```go
const SchedulerNameKoordinator SchedulerName = "koord-scheduler"
```

A new typed config:

```go
// KoordinatorSchedulerConfiguration is the Config payload for scheduler profile "koord-scheduler".
type KoordinatorSchedulerConfiguration struct {
    // GangMode applied to all generated PodGroups.
    // +kubebuilder:validation:Enum=Strict;NonStrict
    GangMode string `json:"gangMode,omitempty"`

    // MatchPolicy for the GangGroup.
    // +kubebuilder:validation:Enum=once-satisfied;only-waiting;waiting-and-running
    MatchPolicy string `json:"matchPolicy,omitempty"`

    // ScheduleTimeoutSeconds applied to all generated PodGroups.
    // +kubebuilder:validation:Minimum=1
    ScheduleTimeoutSeconds *int32 `json:"scheduleTimeoutSeconds,omitempty"`

    // DefaultQoSClass injects koordinator.sh/qosClass on every Pod when non-empty.
    // +kubebuilder:validation:Enum=LSE;LSR;LS;BE
    DefaultQoSClass string `json:"defaultQoSClass,omitempty"`

    // TopologyKeyMappings extends the built-in topology key → Koordinator layer table.
    // Value must be one of: hostLayer, rackLayer, blockLayer.
    // Arbitrary Koordinator layer names are not supported in this version.
    TopologyKeyMappings map[string]string `json:"topologyKeyMappings,omitempty"`
}
```

Example OperatorConfiguration:

```yaml
scheduler:
  defaultProfileName: koord-scheduler
  profiles:
    - name: koord-scheduler
      config:
        gangMode: Strict
        matchPolicy: once-satisfied
        scheduleTimeoutSeconds: 60
        defaultQoSClass: LS
        topologyKeyMappings:
          topology.example.com/zone: blockLayer
```

Invalid field values (e.g. unknown `GangMode`) cause `Init()` to return an error and the operator fails to start, rather than silently running with a partial/default configuration.

### Lifecycle and Ownership

- **PodGang create**: The Backend Controller reconciles the new PodGang and calls `SyncPodGang`. The backend creates one Koordinator PodGroup CR per Grove PodGroup, each with an OwnerReference to the PodGang.
- **PodGang update** (PodGroups added or removed between reconciles): `SyncPodGang` first creates/updates every desired PodGroup, then runs `pruneOrphanedPodGroups` to delete any stale PodGroups owned by this PodGang. The create-then-prune ordering ensures a failure mid-loop never deletes a still-valid PodGroup.
- **PodGang delete**: OwnerReference drives Kubernetes garbage collection; `OnPodGangDelete` is a no-op.
- **Ownership safety**: `createOrUpdatePodGroup` refuses to overwrite a PodGroup whose controller OwnerReference UID differs from the desired one, preventing one PodGang from clobbering another workload's PodGroup.
- **Metadata preservation**: Updates preserve existing labels, annotations, and finalizers that are not owned by Grove, then overlay the desired Grove-managed labels and annotations.

### RBAC

The backend needs `get`, `list`, `watch`, `create`, `update`, `delete` on `podgroups.scheduling.sigs.k8s.io`. The Helm chart grants these verbs unconditionally in the operator `ClusterRole`; Kubernetes accepts RBAC rules for resources whose CRDs are not installed, so the rule is harmless on clusters that do not use Koordinator.

### Test Plan

#### Unit Tests

Unit tests live under `operator/internal/scheduler/koordinator/`:

- `backend_test.go`: interface wiring (Name, Init error surfacing).
- `config_test.go`: parse and defaulting behaviour for every field in `KoordinatorSchedulerConfiguration`, including invalid-value error paths.
- `pod_test.go`: `PreparePod` injects `SchedulerName`, `pod-group` label, and (when configured) the QoS label.
- `podgroup_test.go`: single-group, multi-group, per-PodGroup total child count annotation, topology (global vs per-PodGroup override, Required hard-fail, Preferred soft-skip), GangGroup membership JSON, metadata-preserving update, prune (shrink and orphan-with-different-UID), `TopologyConstraintGroupConfigs` rejection, `ReuseReservationRef` warning event.
- `validation_test.go`: MNNVL rejection and PCSG-level topology rejection.

#### E2E Tests

E2E tests live under `operator/e2e/tests/koordinator/`:

- `gang_scheduling_test.go`: end-to-end gang scheduling of a multi-clique workload on a cluster running Koordinator, asserting PodGroup creation, GangGroup annotations, per-PodGroup child-count annotations, gang blocking while no worker nodes are schedulable, and MNNVL admission rejection.
- These tests assume an externally prepared `koordinator-grove` cluster and Grove operator deployment with the `koord-scheduler` profile enabled. They connect to the existing cluster instead of creating the cluster, installing Koordinator, or deploying Grove from the test binary.

Reusable sample workloads live at `operator/e2e/yaml/workload-koord.yaml`.

### Graduation Criteria

#### Alpha

- `scheduler.Backend` implementation lands with gang scheduling, single-layer topology, QoS label injection, MNNVL and PCSG topology rejection, owner-reference cleanup, and unit tests covering all branches.
- Registered as a valid scheduler name in OperatorConfiguration.
- Sample workload and e2e smoke test running against a Koordinator cluster.

#### Beta

- E2E suite covers multi-clique, multi-PCSG, topology Required + Preferred, and backend-switching scenarios.
- Documented operator playbook for deploying Grove on an existing Koordinator cluster.
- Ownership and prune semantics battle-tested against at least one production-style workload.

#### GA

- Grove `TopologyConstraint` API is evolved (in a follow-up GREP) to a form that can express multi-layer rules and pod-index alignment, and the Koordinator backend is updated to use it.
- Stable for two consecutive Grove releases with no breaking backend-interface changes.

## Alternatives

### Users write Koordinator annotations directly

**Rejected.** Requiring users to hand-author `gang.scheduling.koordinator.sh/*` annotations on every PodClique template defeats the purpose of Grove's unified API. It also leaks scheduler-specific syntax into the workload manifest, making workloads non-portable across backends.

### Map PodGang to a single PodGroup

**Rejected.** A single PodGroup would flatten the per-PodClique `MinReplicas`, losing the ability for Koordinator's coscheduling plugin to compute per-role gang satisfaction. Producing N PodGroups and tying them into a GangGroup preserves per-role semantics while still gating the overall gang.

### Wait for Koordinator to natively support PodGang

**Rejected.** There is no upstream proposal for Koordinator to consume Grove's PodGang CRD, and even if one existed, users need a solution today. GREP-375's backend framework is designed precisely for translation layers like this.

## Appendix

### Reference: Koordinator annotations used

| Annotation | Purpose |
|---|---|
| `gang.scheduling.koordinator.sh/groups` | JSON array of `"ns/podgroup"` strings declaring GangGroup membership |
| `gang.scheduling.koordinator.sh/mode` | `Strict` or `NonStrict` gang failure handling |
| `gang.scheduling.koordinator.sh/match-policy` | When the GangGroup is considered satisfied |
| `gang.scheduling.koordinator.sh/total-number` | Total children number for the individual Koordinator gang, derived from that Grove PodGroup's pod references when known |
| `gang.scheduling.koordinator.sh/network-topology-spec` | JSON-serialised single-layer topology constraint |
| `pod-group.scheduling.sigs.k8s.io` (Pod label) | Associates a Pod with its PodGroup CR |
| `koordinator.sh/qosClass` (Pod label) | Koordinator QoS class |

### Reference: Example translation

Given the sample `multi-node-disaggregated` PodCliqueSet (4 cliques: `pleader`, `pworker`, `dleader`, `dworker`; 2 PCSGs: `prefill`, `decode`), the PodCliqueSet controller produces one Base PodGang named e.g. `demo-0` and (as PCSG replicas scale) additional Scaled PodGangs such as `demo-0-prefill-1`.

The Koordinator backend then produces, for the Base PodGang:

```
demo-0-pleader    (MinMember: 1)   ┐
demo-0-pworker    (MinMember: 4)   │  all four PodGroups carry the same
demo-0-dleader    (MinMember: 1)   │  gang.scheduling.koordinator.sh/groups
demo-0-dworker    (MinMember: 3)   ┘  listing all four members
```

Each Scaled PodGang produces its own disjoint GangGroup containing just that replica's PodGroups.

> NOTE: This GREP template has been inspired by [KEP Template](https://github.com/kubernetes/enhancements/blob/f90055d254c356b2c038a1bdf4610bf4acd8d7be/keps/NNNN-kep-template/README.md).

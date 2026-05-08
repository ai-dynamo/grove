# GREP-377: Volcano Topology-Aware Scheduling Backend

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
    - [HyperNode Requires Scheduler-Specific Topology Resources](#hypernode-requires-scheduler-specific-topology-resources)
    - [Topology Changes Can Rebuild HyperNodes](#topology-changes-can-rebuild-hypernodes)
    - [Tier Number Mapping](#tier-number-mapping)
- [Design Details](#design-details)
  - [Control Flow](#control-flow)
  - [Volcano Scheduler Backend Topology](#volcano-scheduler-backend-topology)
  - [HyperNode Generation](#hypernode-generation)
  - [Externally Managed HyperNodes](#externally-managed-hypernodes)
  - [PodGang to Volcano PodGroup Translation](#podgang-to-volcano-podgroup-translation)
  - [Operator Configuration and Dependencies](#operator-configuration-and-dependencies)
  - [Monitoring](#monitoring)
  - [Test Plan](#test-plan)
- [Appendix](#appendix)
<!-- /toc -->

## Summary

Grove already defines a portable topology-aware scheduling API through `ClusterTopology`, `TopologyConstraint`, and `PodGang`. This GREP proposes implementing Volcano support for that API by extending the Volcano scheduler backend to implement `TopologyAwareSchedBackend`. The backend will translate Grove `ClusterTopology` resources into Volcano 1.14+ `HyperNode` resources and translate Grove topology constraints into Volcano `PodGroup.spec.networkTopology` and `PodGroup.spec.subGroupPolicy[*].networkTopology` so Grove workloads can use Volcano for topology-aware gang scheduling.

## Motivation

Grove workloads should be portable across scheduler backends. GREP-244 defines the Grove topology model, and GREP-375 defines the scheduler backend framework that lets a scheduler implement topology management without changing the user-facing workload API. PR #560 / GREP-376 introduces the base Volcano backend for Grove gang scheduling with Volcano `PodGroup` and `subGroupPolicy`. The next step is to make the Volcano backend understand the same topology-aware scheduling intent that KAI already supports.

Volcano 1.14+ exposes topology-aware scheduling through two related primitives:

* `HyperNode` resources model the scheduler-visible network topology hierarchy.
* `networkTopology` fields on Volcano jobs, `PodGroup`, and `subGroupPolicy` express how far a workload or subgroup may cross that hierarchy.

Volcano expresses topology as a HyperNode tree. Leaf HyperNodes contain real Nodes, while non-leaf HyperNodes contain child HyperNodes. Grove `ClusterTopology` defines a portable hierarchy of domain names and Node label keys, so the Volcano backend must materialize a scheduler-specific HyperNode tree by grouping Nodes according to the administrator-declared `ClusterTopology` label keys, while preserving Grove's portable domain-based workload API.

### Goals

* Add Volcano as a topology-aware scheduler backend by implementing the `TopologyAwareSchedBackend` interface.
* Translate each Grove `ClusterTopology` into Volcano `HyperNode` resources when the topology is auto-managed by Grove.
* Support externally managed Volcano HyperNodes through `schedulerTopologyReferences` and drift detection.
* Translate Grove `TopologyConstraint.packDomain` to Volcano `networkTopology.highestTierAllowed` using the numeric tier derived from the referenced `ClusterTopology` level.
* Preserve the base Volcano gang scheduling behavior defined by GREP-376 while adding topology constraints to the generated Volcano `PodGroup`.
* Document the Volcano 1.14+ API requirements for users and cluster administrators.

### Non-Goals

* Defining new Grove user-facing topology APIs. This GREP uses the APIs from GREP-244.
* Implementing automatic topology level discovery. Grove will only instantiate the levels declared by `ClusterTopology`.
* Supporting Volcano versions earlier than 1.14.
* Supporting topology-aware scheduling with Kubernetes `default-scheduler`.
* Changing the `TopologyAwareSchedBackend` interface.
* Replacing Volcano's own network topology plugin behavior or scoring logic.

## Proposal

The Volcano backend will become a topology-aware scheduler backend. When topology-aware scheduling is enabled and Volcano is configured as an active scheduler profile, the ClusterTopology controller will invoke the Volcano backend through `TopologyAwareSchedBackend` in the same way it invokes the KAI backend.

For auto-managed topologies, Grove will create and maintain Volcano `HyperNode` resources from each `ClusterTopology`. The generated HyperNodes will form a tree: leaf HyperNodes select real Nodes, and non-leaf HyperNodes reference child HyperNodes. Workload constraints will target those tiers through `networkTopology.highestTierAllowed`, using the numeric tier that corresponds to the requested Grove `packDomain`.

For externally managed topologies, the administrator can add a Volcano entry to `ClusterTopology.spec.schedulerTopologyReferences`. Grove will not create or update the referenced HyperNode, but it will check whether it is compatible with the Grove `ClusterTopology` and report drift through the standard `SchedulerTopologyDrift` condition and `schedulerTopologyStatuses`.

In this section, `networkTopology` refers to Volcano's workload-side API field on Volcano resources. Volcano `networkTopology` is the constraint that tells Volcano which HyperNode tier a job or subgroup may cross. Therefore this GREP uses both Volcano primitives:

* `HyperNode` as the backend topology resource managed by `TopologyAwareSchedBackend`.
* `PodGroup.spec.networkTopology` and `PodGroup.spec.subGroupPolicy[*].networkTopology` as the translated workload constraints created by `SyncPodGang`.

### Limitations/Risks & Mitigations

#### HyperNode Requires Scheduler-Specific Topology Resources

Grove `ClusterTopology` stores the portable topology contract, while Volcano schedules against a HyperNode tree. If Nodes are not labeled with the keys referenced by `ClusterTopology`, Grove cannot place those Nodes into the generated tree.

**Mitigation:** The backend will generate HyperNodes from Nodes that contain all label keys declared in `ClusterTopology`. Drift and reconciliation errors will be surfaced through the existing `SchedulerTopologyDrift` status path. Documentation will tell administrators to label Nodes before enabling Volcano TAS.

#### Topology Changes Can Rebuild HyperNodes

Changing `ClusterTopology.spec.levels` or changing Node label keys can alter the generated HyperNodes.

**Mitigation:** Grove will treat auto-managed HyperNodes as derived resources and reconcile them to the latest desired set. Already running Pods are not evicted. Existing limitations from GREP-244 still apply: topology constraints are guaranteed only for initial scheduling decisions.

#### Tier Number Mapping

Volcano expresses the scheduling boundary with `highestTierAllowed`. In Volcano's HyperNode model, lower tier numbers represent narrower, higher-locality domains, while higher tier numbers represent broader domains.

**Mitigation:** The Volcano backend will derive tier numbers deterministically from the `ClusterTopology` order. Grove `ClusterTopology.spec.levels` are ordered broadest to narrowest, so for `N` Grove levels, level index `i` maps to Volcano tier `N - i`. For example, `[block, rack, host]` maps to `block=3`, `rack=2`, and `host=1`. Workload constraints will use `highestTierAllowed`.

## Design Details

### Control Flow

```mermaid
sequenceDiagram
    participant Admin as Cluster admin
    participant API as Kubernetes API
    participant CT as ClusterTopology controller
    participant VolcanoBackend as Volcano backend
    participant Volcano as Volcano scheduler
    participant PCS as PodCliqueSet controller
    participant Backend as Scheduler backend controller

    Admin->>API: Apply ClusterTopology
    Admin->>API: Label Nodes with topology domains
    API-->>CT: Watch ClusterTopology changes

    alt Auto-managed Volcano topology
        CT->>VolcanoBackend: SyncTopology(ClusterTopology)
        VolcanoBackend->>API: Create or update HyperNodes
    else Externally managed Volcano topology
        CT->>VolcanoBackend: CheckTopologyDrift(ClusterTopology, reference)
        VolcanoBackend->>API: Read referenced HyperNode
        VolcanoBackend-->>CT: Report in-sync or drift
    end

    Admin->>API: Apply PodCliqueSet with TopologyConstraint
    API-->>PCS: Reconcile PodCliqueSet
    PCS->>API: Create Grove PodGang with topology constraints
    API-->>Backend: Reconcile PodGang
    Backend->>VolcanoBackend: SyncPodGang(PodGang)
    VolcanoBackend->>API: Create or update Volcano PodGroup with networkTopology
    Volcano->>API: Read PodGroup and HyperNodes
    Volcano->>API: Bind Pods within requested topology tier
```

The control flow has two independent reconciliation paths. The ClusterTopology path creates or validates Volcano `HyperNode` topology resources. The workload path translates Grove `PodGang` scheduling intent into a Volcano `PodGroup`. Volcano only has enough information to enforce topology-aware scheduling when both paths are healthy: the HyperNodes describe the topology tiers, and the PodGroup `networkTopology` fields describe the workload's requested boundary.

### Volcano Scheduler Backend Topology

The Volcano backend will implement the existing optional topology interface:

```go
type TopologyAwareSchedBackend interface {
	TopologyGVR() schema.GroupVersionResource
	TopologyResourceName(ct *grovecorev1alpha1.ClusterTopology) string
	SyncTopology(ctx context.Context, k8sClient client.Client, ct *grovecorev1alpha1.ClusterTopology) error
	OnTopologyDelete(ctx context.Context, k8sClient client.Client, ct *grovecorev1alpha1.ClusterTopology) error
	CheckTopologyDrift(
		ctx context.Context,
		ct *grovecorev1alpha1.ClusterTopology,
		ref grovecorev1alpha1.SchedulerTopologyReference,
	) (bool, string, int64, error)
}
```

For Volcano:

* `TopologyGVR()` returns `topology.volcano.sh/v1alpha1, Resource=hypernodes`.
* `TopologyResourceName(ct)` returns the synthetic root HyperNode name for the `ClusterTopology`. The default name is the `ClusterTopology` name.
* `SyncTopology()` lists Nodes, derives the desired HyperNode tree from the `ClusterTopology` levels and Node label values, and creates or updates all auto-managed HyperNodes for the `ClusterTopology`.
* `OnTopologyDelete()` is a no-op because every auto-managed HyperNode will have the `ClusterTopology` as its controller owner, allowing Kubernetes garbage collection to cascade-delete them.
* `CheckTopologyDrift()` compares the externally managed HyperNode tree rooted at `ref.topologyReference` with the desired topology derived from `ClusterTopology` and Node labels.

The backend must add Volcano topology API types to the operator scheme and ensure the `hypernodes.topology.volcano.sh` CRD is present when Volcano TAS is enabled.

### HyperNode Generation

The generated HyperNodes are derived from `ClusterTopology.spec.levels` and the current Node label values. The order of `spec.levels` remains broadest to narrowest, matching GREP-244.

Given this `ClusterTopology`:

```yaml
apiVersion: grove.io/v1alpha1
kind: ClusterTopology
metadata:
  name: grove-topology
spec:
  levels:
    - domain: block
      key: topology.grove.io/block
    - domain: rack
      key: topology.grove.io/rack
    - domain: host
      key: kubernetes.io/hostname
```

The Volcano backend will create value-level HyperNodes for each topology domain. Leaf HyperNodes select real Nodes. Non-leaf HyperNodes reference child HyperNodes by exact name. A synthetic root HyperNode named after the `ClusterTopology` ties the generated tree together and is the resource returned by `TopologyResourceName()`.

For example, assume the cluster has four Nodes with the following labels:

| Node   | `topology.grove.io/block` | `topology.grove.io/rack` | `kubernetes.io/hostname` |
| ------ | ------------------------- | ------------------------ | ------------------------ |
| node-1 | block-a                   | rack-1                   | node-1                   |
| node-2 | block-a                   | rack-1                   | node-2                   |
| node-3 | block-a                   | rack-2                   | node-3                   |
| node-4 | block-b                   | rack-3                   | node-4                   |

> NOTE: Each generated HyperNode will include Grove management labels and a `ClusterTopology` controller owner reference. The example below omits repeated metadata fields for readability.

```yaml
apiVersion: topology.volcano.sh/v1alpha1
kind: HyperNode
metadata:
  name: grove-topology
spec:
  tier: 4
  members:
    - type: HyperNode
      selector:
        exactMatch:
          name: grove-topology-block-block-a
    - type: HyperNode
      selector:
        exactMatch:
          name: grove-topology-block-block-b
---
apiVersion: topology.volcano.sh/v1alpha1
kind: HyperNode
metadata:
  name: grove-topology-block-block-a
spec:
  tier: 3
  members:
    - type: HyperNode
      selector:
        exactMatch:
          name: grove-topology-block-block-a-rack-rack-1
    - type: HyperNode
      selector:
        exactMatch:
          name: grove-topology-block-block-a-rack-rack-2
---
apiVersion: topology.volcano.sh/v1alpha1
kind: HyperNode
metadata:
  name: grove-topology-block-block-b
spec:
  tier: 3
  members:
    - type: HyperNode
      selector:
        exactMatch:
          name: grove-topology-block-block-b-rack-rack-3
---
apiVersion: topology.volcano.sh/v1alpha1
kind: HyperNode
metadata:
  name: grove-topology-block-block-a-rack-rack-1
spec:
  tier: 2
  members:
    - type: HyperNode
      selector:
        exactMatch:
          name: grove-topology-block-block-a-rack-rack-1-host-node-1
    - type: HyperNode
      selector:
        exactMatch:
          name: grove-topology-block-block-a-rack-rack-1-host-node-2
---
apiVersion: topology.volcano.sh/v1alpha1
kind: HyperNode
metadata:
  name: grove-topology-block-block-a-rack-rack-2
spec:
  tier: 2
  members:
    - type: HyperNode
      selector:
        exactMatch:
          name: grove-topology-block-block-a-rack-rack-2-host-node-3
---
apiVersion: topology.volcano.sh/v1alpha1
kind: HyperNode
metadata:
  name: grove-topology-block-block-b-rack-rack-3
spec:
  tier: 2
  members:
    - type: HyperNode
      selector:
        exactMatch:
          name: grove-topology-block-block-b-rack-rack-3-host-node-4
---
apiVersion: topology.volcano.sh/v1alpha1
kind: HyperNode
metadata:
  name: grove-topology-block-block-a-rack-rack-1-host-node-1
spec:
  tier: 1
  members:
    - type: Node
      selector:
        labelMatch:
          matchLabels:
            kubernetes.io/hostname: node-1
---
apiVersion: topology.volcano.sh/v1alpha1
kind: HyperNode
metadata:
  name: grove-topology-block-block-a-rack-rack-1-host-node-2
spec:
  tier: 1
  members:
    - type: Node
      selector:
        labelMatch:
          matchLabels:
            kubernetes.io/hostname: node-2
---
apiVersion: topology.volcano.sh/v1alpha1
kind: HyperNode
metadata:
  name: grove-topology-block-block-a-rack-rack-2-host-node-3
spec:
  tier: 1
  members:
    - type: Node
      selector:
        labelMatch:
          matchLabels:
            kubernetes.io/hostname: node-3
---
apiVersion: topology.volcano.sh/v1alpha1
kind: HyperNode
metadata:
  name: grove-topology-block-block-b-rack-rack-3-host-node-4
spec:
  tier: 1
  members:
    - type: Node
      selector:
        labelMatch:
          matchLabels:
            kubernetes.io/hostname: node-4
```

The resulting HyperNode tree is:

```mermaid
flowchart TB
    root["grove-topology<br/>synthetic root<br/>tier 4"]

    blockA["block-a<br/>tier 3"]
    blockB["block-b<br/>tier 3"]

    rack1["rack-1<br/>tier 2"]
    rack2["rack-2<br/>tier 2"]
    rack3["rack-3<br/>tier 2"]

    host1["host node-1<br/>tier 1"]
    host2["host node-2<br/>tier 1"]
    host3["host node-3<br/>tier 1"]
    host4["host node-4<br/>tier 1"]

    node1["node-1"]
    node2["node-2"]
    node3["node-3"]
    node4["node-4"]

    root --> blockA
    root --> blockB
    blockA --> rack1
    blockA --> rack2
    blockB --> rack3
    rack1 --> host1
    rack1 --> host2
    rack2 --> host3
    rack3 --> host4
    host1 --> node1
    host2 --> node2
    host3 --> node3
    host4 --> node4

    classDef hyperNode fill:#d8ebff,stroke:#333,stroke-width:1px,color:#111;
    classDef node fill:#eef6ff,stroke:#333,stroke-width:1px,color:#111;
    class root,blockA,blockB,rack1,rack2,rack3,host1,host2,host3,host4 hyperNode;
    class node1,node2,node3,node4 node;
```

Generated names must be deterministic and valid Kubernetes object names. Value-level HyperNode names include the topology path so equal rack names under different blocks do not collide. If raw label values do not fit object-name constraints, the implementation will sanitize them and add a stable hash suffix. Generated HyperNodes must set the `ClusterTopology` as their controller owner so Kubernetes garbage collection deletes them with the owning topology. They should also carry Grove labels such as the owning `ClusterTopology` name and topology domain so they can be listed, updated, and repaired as a set.

For Volcano 1.14+, only leaf HyperNodes point directly to Nodes. Non-leaf HyperNodes point to child HyperNodes using `exactMatch`. `regexMatch` must not be used for members whose type is `HyperNode`.

If a `ClusterTopology` includes a `host` domain backed by `kubernetes.io/hostname`, each host value is unique, so a faithful Volcano topology needs one leaf HyperNode per Node. This can create many HyperNodes in large clusters. Administrators who do not need Grove `packDomain: host` semantics for Volcano can reduce the object count by omitting the `host` level from the Volcano-targeted `ClusterTopology`; in that case the narrowest generated HyperNodes, such as rack HyperNodes, can select real Nodes directly with `labelMatch` or `regexMatch`. Grove must not collapse all hosts into a single host HyperNode, because that would make `packDomain: host` behave like a broader domain and weaken the user's requested constraint.

### Externally Managed HyperNodes

Administrators may choose to manage Volcano HyperNodes outside Grove:

```yaml
apiVersion: grove.io/v1alpha1
kind: ClusterTopology
metadata:
  name: grove-topology
spec:
  levels:
    - domain: block
      key: topology.grove.io/block
    - domain: rack
      key: topology.grove.io/rack
    - domain: host
      key: kubernetes.io/hostname
  schedulerTopologyReferences:
    - schedulerName: volcano
      topologyReference: grove-volcano-root
```

When this reference is present, Grove will not create or mutate the referenced HyperNode. The Volcano backend will verify that the referenced HyperNode exists and that its tier and Node selector match the expected domain from `ClusterTopology.spec.levels`. It will report mismatches through `schedulerTopologyStatuses[*].inSync=false` and the aggregate `SchedulerTopologyDrift` condition.

### PodGang to Volcano PodGroup Translation

GREP-376 defines the base translation from Grove `PodGang` to Volcano `PodGroup`, including `minMember` and `subGroupPolicy`. This GREP adds topology fields to the same generated `PodGroup` and uses Volcano `subGroupPolicy` directly for PodGroup subsets.

A Grove `PodGang` may carry both a gang-level topology constraint and a subgroup topology constraint:

```yaml
apiVersion: scheduler.grove.io/v1alpha1
kind: PodGang
metadata:
  name: disaggregated-inference
  namespace: default
spec:
  topologyConstraint:
    packConstraint:
      required: topology.grove.io/block
  topologyConstraintGroupConfigs:
    - name: prefill
      podGroupNames:
        - prefill
      topologyConstraint:
        packConstraint:
          required: topology.grove.io/rack
  podgroups:
    - name: prefill
      minReplicas: 4
      podReferences:
        - namespace: default
          name: prefill-0
        - namespace: default
          name: prefill-1
        - namespace: default
          name: prefill-2
        - namespace: default
          name: prefill-3
    - name: decode
      minReplicas: 8
      podReferences:
        - namespace: default
          name: decode-0
        - namespace: default
          name: decode-1
        # ... decode-2 through decode-7 omitted for brevity
```

The Volcano backend translates this into a single Volcano `PodGroup`. The gang-level topology constraint becomes `PodGroup.spec.networkTopology`, and the subset constraint becomes an entry in `PodGroup.spec.subGroupPolicy`:

```yaml
apiVersion: scheduling.volcano.sh/v1beta1
kind: PodGroup
metadata:
  name: disaggregated-inference
  namespace: default
spec:
  minMember: 12
  networkTopology:
    mode: hard
    highestTierAllowed: 3
  subGroupPolicy:
    - name: prefill
      subGroupSize: 4
      minSubGroups: 1
      labelSelector:
        matchLabels:
          grove.io/podgang-podgroup: prefill
      networkTopology:
        mode: hard
        highestTierAllowed: 2
    - name: decode
      subGroupSize: 8
      minSubGroups: 1
      labelSelector:
        matchLabels:
          grove.io/podgang-podgroup: decode
```

The translation computes `highestTierAllowed` from the required topology key and the referenced `ClusterTopology`. For `[block, rack, host]`, a required key of `topology.grove.io/block` becomes `highestTierAllowed: 3`, `topology.grove.io/rack` becomes `highestTierAllowed: 2`, and `kubernetes.io/hostname` becomes `highestTierAllowed: 1`.

If a PodGang has no topology constraints, the Volcano backend will keep generating a normal Volcano `PodGroup` without `networkTopology` fields.

### Operator Configuration and Dependencies

Volcano topology-aware scheduling requires:

* Volcano 1.14 or newer.
* `podgroups.scheduling.volcano.sh` CRD.
* `hypernodes.topology.volcano.sh` CRD.
* Volcano scheduler configured according to GREP-376's Volcano 1.14+ requirement, with topology-aware scheduling APIs available.
* Grove `topologyAwareScheduling.enabled=true`.
* A Volcano scheduler profile enabled in `OperatorConfiguration`.

The Volcano scheduler profile name is `volcano`, consistent with GREP-376. A topology-aware workload that selects Volcano must be rejected at admission if Volcano is not enabled.

### Monitoring

This feature uses the existing topology monitoring model from GREP-244:

* `ClusterTopology.status.conditions[type=SchedulerTopologyDrift]` reports aggregate backend topology health.
* `ClusterTopology.status.schedulerTopologyStatuses` reports Volcano-specific drift details, including the referenced root HyperNode and observed generation.
* `PodCliqueSet.status.conditions[type=TopologyLevelsUnavailable]` reports workloads whose referenced topology or domain is unavailable.
* Volcano `PodGroup.status.phase` and `PodGroup.status.conditions` remain the scheduler-side signal for pending, running, and unschedulable workloads.

The implementation should emit Kubernetes events when HyperNode sync fails, when an externally managed HyperNode drifts, and when a PodGroup cannot be generated with required topology constraints.

### Test Plan

Unit tests:

* Verify `TopologyGVR()` returns `topology.volcano.sh/v1alpha1` and `hypernodes`.
* Verify deterministic HyperNode generation from a `ClusterTopology`.
* Verify generated HyperNodes use `labelMatch` selectors with the expected `ClusterTopology` label keys.
* Verify `SyncTopology()` creates, updates, and deletes stale auto-managed HyperNodes.
* Verify generated HyperNodes have `ClusterTopology` owner references and are eligible for Kubernetes garbage collection on topology deletion.
* Verify `CheckTopologyDrift()` returns in-sync for matching externally managed HyperNodes and drift for missing HyperNode, wrong tier, or mismatched Node selector.
* Verify `packDomain` translation uses `networkTopology.highestTierAllowed` with `mode: hard`.
* Verify PodGang-level topology constraints populate `PodGroup.spec.networkTopology`.
* Verify subgroup-level topology constraints populate `PodGroup.spec.subGroupPolicy[*].networkTopology`.

Integration and e2e tests:

* Install Volcano 1.14+ and apply a `ClusterTopology`.
* Create a Grove workload with a PodGang-level topology constraint and confirm the generated Volcano `PodGroup` contains the expected `networkTopology`.
* Create a workload with subgroup topology constraints and confirm the generated `subGroupPolicy` entries contain the expected `networkTopology`.
* Confirm Pods schedule within the requested HyperNode tier when enough resources exist.
* Confirm the workload remains pending and surfaces useful status when the topology constraint cannot be satisfied.

## Appendix

Related work:

* [GREP-244: Topology Aware Scheduling](../244-topology-aware-scheduling/README.md)
* [GREP-375: Scheduler Backend Framework](../375-scheduler-backend-framework/README.md)
* GREP-376: Volcano Gang Scheduling Backend, proposed in PR #560
* Volcano 1.14+ `HyperNode` API in `topology.volcano.sh/v1alpha1`
* Volcano 1.14+ `PodGroup.spec.networkTopology` and `PodGroup.spec.subGroupPolicy[*].networkTopology`

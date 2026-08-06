# GREP-9999: Kueue TAS Integration

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Kueue Quota and TAS Semantics](#kueue-quota-and-tas-semantics)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Shared Cluster Capacity for Training and Inference](#story-1-shared-cluster-capacity-for-training-and-inference)
    - [Story 2: Kueue TAS for Grove Pods](#story-2-kueue-tas-for-grove-pods)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
    - [Phase 1 <code>minCount</code> Mapping](#phase-1-mincount-mapping)
    - [CPU-Only Validation](#cpu-only-validation)
- [Design Details](#design-details)
  - [Kueue Scheduler Backend](#kueue-scheduler-backend)
  - [Kueue Queue Selection](#kueue-queue-selection)
  - [Kueue Topology Synchronization](#kueue-topology-synchronization)
  - [PodSet Topology Request Flow](#podset-topology-request-flow)
  - [Kueue Cleanup](#kueue-cleanup)
  - [Monitoring](#monitoring)
  - [Dependencies](#dependencies)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Alternatives](#alternatives)
  - [Custom Kueue Job Framework Integration](#custom-kueue-job-framework-integration)
  - [Kueue-Constructed Workloads from Plain Pods](#kueue-constructed-workloads-from-plain-pods)
  - [Kueue-Specific Topology Field Only](#kueue-specific-topology-field-only)
<!-- /toc -->

## Summary

This GREP proposes integrating Grove with Kueue so Grove-managed inference workloads can use Kueue for queue-based quota admission and topology-aware scheduling. Grove will continue to describe application structure through `PodCliqueSet`, `PodClique`, and `PodGang`, while Kueue will admit those workloads against `LocalQueue` and `ClusterQueue` policy and assign topology-aware placement through its existing `Workload` and plain Pod integration.

## Motivation

Kueue is a Kubernetes-native way to express queueing, quota, and admission policy through `LocalQueue`, `ClusterQueue`, `ResourceFlavor`, and `Workload` resources. Many Grove users run on shared Kubernetes clusters where quota and capacity are managed by Kueue. Grove should integrate with Kueue so users can use the existing quota management system for Grove-managed workloads.

Kueue also provides topology-aware scheduling (TAS) for admitted workloads. Grove already has a topology model through `ClusterTopologyBinding`, `PodCliqueSet` topology constraints, and translated `PodGang` constraints. Integrating these systems allows Grove users to express inference topology requirements once, while allowing cluster administrators to keep Kueue as the source of truth for quota, admission, and topology-aware capacity.

The integration should preserve the separation of responsibilities: Grove describes the inference workload and its clique relationships; Kueue decides whether and where that workload can run based on queue, quota, resource flavor, and topology policy.

### Kueue Quota and TAS Semantics

Kueue separates workload submission from workload execution. A user or controller submits work to a namespace-scoped `LocalQueue`. The `LocalQueue` points to a cluster-scoped `ClusterQueue`, which defines the quota and admission policy for that work. The `ClusterQueue` admits workloads against one or more `ResourceFlavor`s. A `ResourceFlavor` represents a class of capacity, such as a pool of GPU nodes with specific labels, taints, tolerations, or topology characteristics.

Quota is evaluated at the `ClusterQueue` and `ResourceFlavor` level. For example, if an inference workload requests eight GPUs and is admitted with the `h100` flavor, Kueue reserves eight GPUs from the quota configured for that flavor in the selected `ClusterQueue`. This quota reservation is independent of where those GPUs are located topologically. Unless the administrator models racks, zones, or other domains as separate queues or flavors, the quota is not divided by topology domain.

Topology-aware scheduling adds a second check after flavor quota is considered. A `ResourceFlavor` can reference a Kueue `Topology` through `spec.topologyName`. The `Topology` describes the node label hierarchy Kueue should use for placement decisions, such as rack and host labels. When a `Workload` includes a PodSet topology request, Kueue asks whether the requested resources can fit inside an allowed topology domain for the selected flavor. A required topology request must fit within the requested level; a preferred request asks Kueue to try that level first and relax placement if needed.

A workload is admitted only when both decisions succeed: Kueue can reserve quota for the selected flavor, and Kueue can find enough available capacity in a topology domain that satisfies the PodSet topology request. In other words, quota answers "is there enough capacity in this flavor for this workload?", while TAS answers "is there a suitable topology domain in that capacity where this workload can fit?"

The following example shows the two layers. The `ClusterQueue` defines quota for the `h100-tas` flavor. The `ResourceFlavor` links that quota to a node pool and, separately, to the topology Kueue should use for TAS decisions:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: Topology
metadata:
  name: gb200-topology
spec:
  levels:
  - nodeLabel: nvidia.com/gb200-rack
  - nodeLabel: kubernetes.io/hostname
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor
metadata:
  name: gb200-tas
spec:
  nodeLabels:
    accelerator: gb200
  topologyName: gb200-topology
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: inference-gpu
spec:
  namespaceSelector: {}
  resourceGroups:
  - coveredResources:
    - nvidia.com/gpu
    flavors:
    - name: gb200-tas
      resources:
      - name: nvidia.com/gpu
        nominalQuota: "720"
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: LocalQueue
metadata:
  namespace: serving
  name: inference
spec:
  clusterQueue: inference-gpu
```

A workload submitted to `LocalQueue/inference` consumes `nvidia.com/gpu` quota from `ClusterQueue/inference-gpu`. If the workload also requests `nvidia.com/gb200-rack` as its PodSet topology level, Kueue uses `Topology/gb200-topology` to choose a rack domain where the admitted Pods can fit.

After admission, Kueue records the placement decision in `Workload.status.admission.podSetAssignments[].topologyAssignment`. Kueue does not call a special `kube-scheduler` API to communicate this decision. Instead, Kueue updates the admitted Pods using normal Kubernetes scheduling fields, such as `nodeSelector`, and removes the Kueue scheduling gates. Once the gates are removed, `kube-scheduler` schedules the Pods normally, constrained by the selectors that Kueue applied.

### Goals

* Provide an API for Grove workloads to select which Kueue quota they use.
* Translate Grove topology constraints into Kueue PodSet topology annotations for Kueue TAS admission.

### Non-Goals


* Replacing Kueue queue, quota, preemption, or topology policies with Grove-owned policy.
* Automatically discovering physical cluster topology or GPU interconnect topology.

## Proposal

Grove will support Kueue as an admission and topology-aware scheduling backend for `PodCliqueSet` workloads. Users will be able to select a Kueue `LocalQueue` from the Grove API and use Grove topology constraints to request Kueue TAS placement, while Grove continues to manage the workload structure and Pods.

Kueue will remain responsible for queue selection, quota reservation, admission, and topology assignment. Grove will remain responsible for describing the inference workload, creating and reconciling the Pods, and translating portable Grove topology constraints into scheduler-specific placement requests.

The proposed API change is intentionally small: Grove workloads can select the Kueue `LocalQueue` they submit to, and existing Grove topology constraints can be used for Kueue TAS. This allows platform administrators to keep Kueue as the source of truth for quota and topology-aware capacity while Grove users continue to express inference workload structure through `PodCliqueSet`.

The target scheduling model is that each Grove `PodGang` is admitted as one Kueue `Workload`, with its `PodGroups` represented as Kueue PodSets where possible. This preserves Grove's workload grouping semantics while letting Kueue make the admission and placement decision.

### User Stories

#### Story 1: Shared Cluster Capacity for Training and Inference

As a platform administrator, I want to divide shared cluster capacity between training and inference workloads so that training jobs cannot consume all accelerator capacity and starve production inference. Kueue should remain the central quota and admission system, while Grove inference workloads submit to the Kueue `LocalQueue` assigned to inference.

#### Story 2: Kueue TAS for Grove Pods

As a cluster operator using Kueue TAS, I want Grove-created Pods to become Kueue `Workload`s with topology requests so that Kueue can reserve capacity and apply topology-aware placement before Pods are scheduled.

### Limitations/Risks & Mitigations

#### Phase 1 `minCount` Mapping

The phase 1 design assumes that standalone `PodClique`s can preserve Grove `minAvailable` semantics by mapping `PodGroup.MinReplicas` to Kueue `PodSet.minCount`. This is only applied when `minReplicas` is smaller than the PodSet `count`.

For `PodClique`s that are members of a `PodCliqueScalingGroup`, phase 1 treats the corresponding Kueue PodSets as all-or-nothing and omits `minCount`. Kueue interprets an omitted `minCount` as `minCount == count`. This assumes `PodCliqueScalingGroup.minAvailable == PodCliqueScalingGroup.replicas` for workloads using the phase 1 Kueue integration.

Kueue currently allows only one PodSet in a Workload to specify `minCount`. If a Grove `PodGang` contains multiple standalone `PodClique`s with partial `minAvailable` semantics, only one of those PodSets can use `minCount` in phase 1. Supporting multiple partially admitted PodSets would require a Kueue API or scheduler behavior change.

#### CPU-Only Validation

The kind demo validates Kueue TAS control flow with CPU and memory quota, synthetic node labels, and a single-node topology. It does not validate GPU availability, real rack placement, or multi-node inference performance. GPU and multi-node e2e coverage should be added before graduating the feature.

## Design Details

```text
+-----------------------------+       +-----------------------------+       +-----------------------------+
|            Grove            |       |            Kueue            |       |         Kubernetes          |
+-----------------------------+       +-----------------------------+       +-----------------------------+
| PodCliqueSet                |       | LocalQueue                  |       | Kubernetes API              |
| - user workload API         |       | - namespace-facing queue    |       | - Pods and CRDs             |
| - queue + topology intent   |       |                             |       |                             |
|                             |       | ClusterQueue                |       | default-scheduler           |
| ClusterTopologyBinding      |       | - quota/admission policy    |       | - binds ungated Pods        |
| - domain -> node label keys |       |                             |       |                             |
|                             |       | ResourceFlavor              |       | Nodes                       |
| Generated PodClique         |       | - resource class            |       | - resource labels           |
| - per-role Pod controller   |       | - topologyName              |       | - topology labels           |
|                             |       |                             |       |                             |
| Generated PodGang           |       | Kueue Topology              |       |                             |
| - one scheduling unit       |       | - node label hierarchy      |       |                             |
|   per PCS replica           |       |                             |       |                             |
|                             |       | Plain Pod controller        |       |                             |
| Kueue scheduler backend     |       | - watches Kueue-managed     |       |                             |
| - translates Grove intent   |       |   Pods                      |       |                             |
|   to Kueue metadata         |       |                             |       |                             |
|                             |       | Workload                    |       |                             |
| Grove-created Pods          |       | - admission unit            |       |                             |
| - actual Kubernetes Pods    |       |                             |       |                             |
|                             |       | TAS assignment              |       |                             |
+-----------------------------+       +-----------------------------+       +-----------------------------+

1. Grove user creates a PodCliqueSet.
   - The PodCliqueSet selects a Kueue LocalQueue.
   - The PodCliqueSet may reference Grove topology constraints.

2. Grove creates internal workload objects.
   - PodCliqueSet -> one PodClique per role per replica.
   - PodCliqueSet -> one PodGang per PodCliqueSet replica.
   - ClusterTopologyBinding -> Kueue Topology.

3. Grove creates Pods.
   - PodClique builds the actual Kubernetes Pods.
   - The Kueue backend creates a prebuilt Kueue Workload for the PodGang.
   - The Kueue backend stamps Pods with the prebuilt Workload name, pod-group name, role hash, and total group count.
   - Pods are created through the Kubernetes API with Kueue scheduling gates.

4. Kueue admits the workload.
   - Kueue plain Pod controller watches the Kueue-managed Pods.
   - Kueue matches the Pods to the prebuilt Workload.
   - The Workload uses LocalQueue -> ClusterQueue -> ResourceFlavor -> Topology.
   - Kueue reserves quota and computes a TAS assignment.

5. Kubernetes schedules the Pods.
   - Kueue applies node selectors and removes Kueue gates.
   - default-scheduler binds the ungated Pods to matching nodes.
```

### Kueue Scheduler Backend

Grove exposes a `kueue` scheduler backend profile. Users select it by setting `podSpec.schedulerName: kueue` on a `PodCliqueSet` clique template.

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  labels:
    kueue.x-k8s.io/queue-name: inference
spec:
  template:
    cliques:
    - name: worker
      spec:
        podSpec:
          schedulerName: kueue
```

The Grove operator resolves the `kueue` backend during `PodGang` and Pod reconciliation. For each `PodGang`, the backend creates a prebuilt Kueue `Workload`. For each Pod, the backend sets the underlying Kubernetes scheduler name and stamps Kueue metadata:

* `kueue.x-k8s.io/queue-name`
* `kueue.x-k8s.io/pod-group-name`
* `kueue.x-k8s.io/prebuilt-workload-name`
* `kueue.x-k8s.io/pod-group-total-count`
* `kueue.x-k8s.io/role-hash`
* `kueue.x-k8s.io/retriable-in-group`

The phase 1 implementation maps one Grove `PodGang` to one prebuilt Kueue `Workload`. Each Grove `PodGroup` becomes one Kueue PodSet in that Workload. The Kueue pod-group name and prebuilt Workload name are both the generated `PodGang` name, and `kueue.x-k8s.io/pod-group-total-count` is the total number of Pods across all `PodGroups` in that `PodGang`.

The Kueue PodSet name is the Grove `PodGroup` name. Grove stamps `kueue.x-k8s.io/role-hash` on each Pod with the same value, so Kueue's plain Pod controller can match the Pod back to the corresponding prebuilt PodSet without computing an internal pod-spec hash. This keeps Grove `PodGroup` identity stable in the Kueue `Workload`.

For standalone `PodClique`s, Grove maps `PodGroup.MinReplicas` to Kueue `PodSet.minCount`, preserving Grove's partial-gang `minAvailable` semantics. For PodCliques that are members of a `PodCliqueScalingGroup`, the phase 1 design treats each PodSet as all-or-nothing and omits `minCount`, which Kueue interprets as `minCount == count`.

### Kueue Queue Selection

A `PodCliqueSet` selects the Kueue `LocalQueue` by setting the standard Kueue queue label on the `PodCliqueSet` metadata. The Grove operator uses `metadata.labels["kueue.x-k8s.io/queue-name"]` as the queue name for the prebuilt Kueue `Workload` generated for each `PodGang`, and propagates the same value to the Pods it creates.

For example, a platform team can partition a shared GPU cluster into separate Kueue queues for training and inference:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: training-gpu
spec:
  resourceGroups:
  - coveredResources: ["nvidia.com/gpu"]
    flavors:
    - name: h100
      resources:
      - name: nvidia.com/gpu
        nominalQuota: "64"
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: inference-gpu
spec:
  resourceGroups:
  - coveredResources: ["nvidia.com/gpu"]
    flavors:
    - name: h100
      resources:
      - name: nvidia.com/gpu
        nominalQuota: "64"
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: LocalQueue
metadata:
  name: inference
  namespace: serving
spec:
  clusterQueue: inference-gpu
```

A Grove inference workload then targets the inference share:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: llm-serving
  namespace: serving
  labels:
    kueue.x-k8s.io/queue-name: inference
spec:
  template:
    cliques:
    - name: decode
      spec:
        podSpec:
          schedulerName: kueue
```

A more representative inference workload can include standalone prefill capacity and a decode scaling group:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: llm
  namespace: serving
  labels:
    kueue.x-k8s.io/queue-name: inference
spec:
  replicas: 1
  template:
    cliques:
    - name: prefill
      spec:
        roleName: prefill
        replicas: 2
        minAvailable: 1
        podSpec:
          schedulerName: kueue
    - name: decode-leader
      spec:
        roleName: decode-leader
        replicas: 1
        minAvailable: 1
        podSpec:
          schedulerName: kueue
    - name: decode-worker
      spec:
        roleName: decode-worker
        replicas: 4
        minAvailable: 4
        podSpec:
          schedulerName: kueue
    podCliqueScalingGroups:
    - name: decode
      replicas: 3
      minAvailable: 3
      cliqueNames:
      - decode-leader
      - decode-worker
```

Grove would translate this into `PodGang`s that Kueue can admit independently:

```text
PodGang llm-0
  PodGroup llm-0-prefill
    Pods: prefill replica 0, prefill replica 1
    minReplicas: 1
  PodGroup llm-0-decode-0-decode-leader
    Pods: decode group 0 leader replica 0
    minReplicas: 1
  PodGroup llm-0-decode-0-decode-worker
    Pods: decode group 0 worker replicas 0..3
    minReplicas: 4
  PodGroup llm-0-decode-1-decode-leader
    Pods: decode group 1 leader replica 0
    minReplicas: 1
  PodGroup llm-0-decode-1-decode-worker
    Pods: decode group 1 worker replicas 0..3
    minReplicas: 4

PodGang llm-0-decode-2
  PodGroup llm-0-decode-2-decode-leader
    Pods: decode group 2 leader replica 0
    minReplicas: 1
  PodGroup llm-0-decode-2-decode-worker
    Pods: decode group 2 worker replicas 0..3
    minReplicas: 4
```

The target Kueue mapping is one `Workload` per `PodGang`. For example, `PodGang llm-0` becomes one Kueue `Workload` containing PodSets for prefill, decode leader, and decode worker. The additional scaled decode `PodGang llm-0-decode-2` becomes a separate Kueue `Workload`.

In phase 1, the standalone `prefill` PodSet can set `minCount: 1` because it is not part of a `PodCliqueScalingGroup`. The decode PodSets omit `minCount` and are admitted all-or-nothing because they are members of the `decode` scaling group.

For the `decode` scaling group portion of the example, the prebuilt Kueue `Workload` generated for `PodGang llm-0` would contain distinct PodSets for each PCSG replica and role:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: Workload
metadata:
  name: llm-0
spec:
  queueName: inference
  podSets:
  - name: llm-0-prefill
    count: 2
    minCount: 1
    topologyRequest:
      required: topology.example.com/rack
    template:
      spec:
        # prefill PodSpec
  - name: llm-0-decode-0-decode-leader
    count: 1
    topologyRequest:
      required: topology.example.com/rack
    template:
      spec:
        # decode leader PodSpec
  - name: llm-0-decode-0-decode-worker
    count: 4
    topologyRequest:
      required: topology.example.com/rack
    template:
      spec:
        # decode worker PodSpec
  - name: llm-0-decode-1-decode-leader
    count: 1
    topologyRequest:
      required: topology.example.com/rack
    template:
      spec:
        # decode leader PodSpec
  - name: llm-0-decode-1-decode-worker
    count: 4
    topologyRequest:
      required: topology.example.com/rack
    template:
      spec:
        # decode worker PodSpec
```

The PCSG PodSets intentionally do not specify `minCount` in phase 1. Grove instead stamps Pods with `kueue.x-k8s.io/role-hash` equal to the PodGroup name, such as `llm-0-decode-1-decode-worker`, so Kueue's plain Pod controller matches otherwise-identical PCSG Pods to the corresponding prebuilt PodSet rather than collapsing them by Pod template hash.

Training jobs can continue to submit to their own Kueue queue, while Grove-managed inference workloads use the inference queue. Kueue remains responsible for enforcing the capacity split and admission policy.

### Kueue Topology Synchronization

The Kueue backend implements Grove's `TopologyAwareBackend` interface. For every auto-managed `ClusterTopologyBinding`, the backend creates a Kueue `Topology` resource with the same name.

```yaml
apiVersion: grove.io/v1alpha1
kind: ClusterTopologyBinding
metadata:
  name: h100-topology
spec:
  levels:
  - domain: rack
    key: topology.example.com/rack
  - domain: host
    key: kubernetes.io/hostname
```

is translated to:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: Topology
metadata:
  name: h100-topology
spec:
  levels:
  - nodeLabel: topology.example.com/rack
  - nodeLabel: kubernetes.io/hostname
```

The Kueue topology is owner-referenced by the `ClusterTopologyBinding`. Externally managed topologies are supported by using `schedulerTopologyBindings` and drift detection, following the same model as other topology-aware backends.

### PodSet Topology Request Flow

Grove already translates workload topology constraints from domain names to node label keys when generating the intermediate `PodGang` resource. The Kueue integration reuses this translated value when creating the prebuilt Workload.

```text
PodCliqueSet topologyConstraint.pack.required: rack
-> ClusterTopologyBinding level rack -> topology.example.com/rack
-> PodGang.spec.topologyConstraint.packConstraint.required
-> Kueue Workload spec.podSets[].topologyRequest.required
-> Kueue Workload topologyAssignment
```

Pods may still carry Kueue metadata needed for the plain Pod controller to associate them with the prebuilt Workload, but the topology request is represented directly on the prebuilt Workload PodSet.

### Kueue Cleanup

Kueue `Workload` resources are created by the Grove Kueue backend and owned by the corresponding Grove `PodGang`. Kueue also adds owner references from admitted Pods to the Workload. The phase 1 cleanup path relies on Kubernetes garbage collection and Kueue pod finalization: when a Grove `PodGang` and its Pods are removed, the corresponding prebuilt Workload is removed after Kueue has finalized the managed Pods.

### Monitoring

The integration should be observable through existing Grove and Kueue resources:

* `ClusterTopologyBinding.status.schedulerTopologyStatuses` reports whether the Kueue topology is in sync.
* `ClusterTopologyBinding` conditions report scheduler topology drift.
* `PodCliqueSet`, `PodClique`, and `PodGang` status continue to report Grove reconciliation state.
* Kueue `Workload.status.conditions` reports quota reservation and admission.
* Kueue `Workload.status.admission.podSetAssignments[].topologyAssignment` reports TAS placement.
* Kueue `LocalQueue` and `ClusterQueue` status report pending and admitted workloads.

The operator should emit errors if it cannot create or delete Kueue `Topology` or `Workload` resources due to missing CRDs, missing RBAC, or conflicts with externally managed resources.

### Dependencies

This integration depends on:

* Kueue installed with the plain Pod integration enabled.
* Kueue TAS feature support enabled in the Kueue deployment.
* Kueue CRDs for `Workload`, `LocalQueue`, `ClusterQueue`, `ResourceFlavor`, and `Topology`.
* Grove topology-aware scheduling enabled in the operator configuration.
* Nodes labeled with the node labels referenced by the selected `ClusterTopologyBinding` and `ResourceFlavor`.
* Grove operator RBAC that allows managing Kueue `Workload` and `Topology` resources.

### Test Plan

Unit tests should cover:

* Kueue backend initialization and Pod metadata stamping.
* `PodCliqueSet` queue selection propagation to generated `PodClique` and Pods.
* Kueue `Topology` creation and drift detection from `ClusterTopologyBinding`.
* Kueue prebuilt `Workload` creation and cleanup for generated `PodGang`s.
* `PodGroup.MinReplicas` is mapped to Kueue `PodSet.minCount` for eligible standalone `PodClique`s.
* Kueue `Workload.spec.podSets[].topologyRequest` is created from translated `PodGang` constraints.

Integration and e2e tests should cover:

* Kueue quota admission for a Grove workload using a CPU-only kind cluster.
* Kueue TAS admission for a Grove workload using a kind topology, Kueue `Topology`, TAS `ResourceFlavor`, and synthetic node labels.
* A multi-clique Grove `PodGang` is represented as one Kueue `Workload` with multiple Kueue PodSets.
* Cleanup of Grove `PodCliqueSet` resources also removes Kueue `Workload` resources.
* A negative case where a missing `LocalQueue` or missing topology prevents admission and exposes actionable status/events.

### Graduation Criteria

Alpha:

* Kueue backend supports queue selection, prebuilt Workload creation, quota admission, Kueue topology sync, `minCount` mapping, and TAS topology requests for `PodGang`-scoped Workloads.
* Unit tests cover the backend, topology sync, pod annotation, and cleanup paths.
* A CPU-only kind demo validates quota and TAS admission.

Beta:

* E2E tests run in CI against a Kueue-enabled cluster.
* Documentation explains setup, queue selection, topology setup, limitations, and cleanup behavior.
* At least one non-kind environment validates multi-node TAS behavior.
* API fields are reviewed and considered stable enough for broader adoption.

GA:

* The integration is used by multiple Grove workloads without known cleanup or admission correctness issues.
* Upgrade and rollback behavior is documented.
* Monitoring and status conditions are sufficient for operators to diagnose queue, quota, and topology failures.

## Alternatives

### Custom Kueue Job Framework Integration

Grove could implement a first-class Kueue job framework integration for Grove `PodGang` resources. This would give Kueue direct knowledge of Grove workload structure and lifecycle, but it requires Kueue to import or understand Grove APIs and significantly increases the integration surface. This proposal targets the same `PodGang -> Workload` admission unit while using Kueue's existing plain Pod integration.

### Kueue-Constructed Workloads from Plain Pods

Grove could avoid creating Kueue `Workload` resources and rely on Kueue's plain Pod controller to construct Workloads entirely from Pod metadata. This keeps Workload construction inside Kueue, but it does not provide a clean way to map Grove `PodGroup.MinReplicas` to Kueue `PodSet.minCount`, and Kueue's plain Pod integration groups PodSets by pod-spec role hash rather than by Grove `PodGroup` identity. Phase 1 therefore uses prebuilt Workloads while still using the plain Pod controller to associate Pods with those Workloads.

### Kueue-Specific Topology Field Only

Grove could expose a Kueue-specific field such as `requiredTopologyKey` and write that value directly to Pods. This is simple but bypasses Grove's existing topology abstraction and makes workloads less portable. This proposal instead reuses `ClusterTopologyBinding` and existing topology constraints.

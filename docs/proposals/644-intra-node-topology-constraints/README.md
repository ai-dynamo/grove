# GREP-644: Intra-Node Topology Constraints

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: NUMA-Aligned Multi-GPU Pods](#story-1-numa-aligned-multi-gpu-pods)
    - [Story 2: Disaggregated Prefill and Decode on One NUMA Node](#story-2-disaggregated-prefill-and-decode-on-one-numa-node)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [ClusterTopologyBinding: Intra-Node Levels](#clustertopologybinding-intra-node-levels)
  - [PodCliqueSet: Topology Constraints](#podcliqueset-topology-constraints)
  - [Enforcement: Per-Pod Devices in resourceSharing Claims](#enforcement-per-pod-devices-in-resourcesharing-claims)
  - [Admission](#admission)
  - [Scheduler Backends](#scheduler-backends)
  - [Spread Constraints](#spread-constraints)
  - [Open Questions](#open-questions)
  - [Monitoring](#monitoring)
  - [Dependencies](#dependencies)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
- [Implementation History](#implementation-history)
- [Alternatives](#alternatives)
  - [Intra-Node Levels in the Levels List](#intra-node-levels-in-the-levels-list)
  - [Scheduler-Native Enforcement Through PodGang](#scheduler-native-enforcement-through-podgang)
  - [A Grove Field for Per-Pod Packing](#a-grove-field-for-per-pod-packing)
  - [A DRA Attribute Instead of a Level Type](#a-dra-attribute-instead-of-a-level-type)
  - [Copying Requests From the Pods' Own Templates](#copying-requests-from-the-pods-own-templates)
  - [resourceSharing Without Per-Pod Devices](#resourcesharing-without-per-pod-devices)
<!-- /toc -->

## Summary

Grove's topology model ([GREP-244](../244-topology-aware-scheduling/README.md)) identifies every topology level by a node label. That covers `region` through `host`, but not domains inside a node. `numa` is already a well-known domain name and GREP-244 Story 3 motivates it, yet no scheduler backend can act on `pack.required: numa` today. Following the discussion in [#644](https://github.com/ai-dynamo/grove/issues/644), this GREP separates two cases. Locality within one pod, such as two GPUs from the same NUMA node, is already expressible with a DRA constraint in the pod's own ResourceClaimTemplate and needs no new Grove API. Locality across a group of pods, such as a prefill worker and the decode workers that read its KV cache sharing a NUMA node, needs Grove, because only Grove knows which pods form the group. This GREP adds intra-node levels to `ClusterTopologyBinding`, defines PodClique-level `pack` as packing all pods of the clique together, and lets `pack.required` name an intra-node domain on a PodCliqueScalingGroup or its member PodCliques. Grove enforces such a constraint through the group's [resourceSharing](../390-hierarchical-resource-sharing/README.md) claim, extended so that each pod gets its own devices in the claim. Any scheduler that allocates shared DRA claims then honors the constraint without scheduler changes. The GREP also addresses whether Grove should offer a spread constraint.

## Motivation

On multi-socket GPU nodes, where a pod's devices sit relative to each other and to the CPUs affects performance. Traffic between a GPU and host memory, between a GPU and its RDMA NIC, and between GPUs that cannot use peer-to-peer over NVLink all cross the inter-socket link when the endpoints are on different NUMA nodes. For disaggregated inference, [dynamo#10171](https://github.com/ai-dynamo/dynamo/issues/10171) reports about 50% lower throughput and about 30x higher per-batch latency when both prefill workers on an 8-GPU node land on one PCIe root and half of the decode workers sit on the other.

Grove cannot express any of this today. As discussed in #644, `TopologyDomainNuma` is declared in the API but nothing consumes it. `TopologyLevel.key` is a required node label key, and a NUMA node has no node label, so an intra-node level cannot be declared in a `ClusterTopologyBinding`. Declaring one with a placeholder label would be worse than not declaring it, because the KAI backend copies every level into its `Topology` resource as a node label.

Scheduler-native NUMA support works one pod at a time. KAI Scheduler's NUMA plugin ([v0.16.0](https://github.com/kai-scheduler/KAI-Scheduler/releases/tag/v0.16.0), with scoring added in [v0.17.0](https://github.com/kai-scheduler/KAI-Scheduler/releases/tag/v0.17.0)) filters and scores nodes for each pod using NodeResourceTopology data and the node's kubelet Topology Manager policy. A workload cannot request it, and it does not place several pods of a gang in one NUMA node ([design](https://github.com/kai-scheduler/KAI-Scheduler/blob/v0.17.0/docs/developer/designs/numa-topology/README.md)). Volcano's numa-aware plugin covers CPUs only ([design](https://github.com/volcano-sh/volcano/blob/v1.15.2/docs/design/numa-aware.md)). The kubelet's Topology Manager aligns each pod separately, and with device plugins it is the kubelet, not the scheduler, that picks the devices.

DRA now has the pieces Grove needs. Kubernetes has [standardized](https://kubernetes.io/docs/reference/node/dra-standard-device-attributes/) the device attributes `resource.kubernetes.io/pcieRoot`, in v1.34, and `resource.kubernetes.io/numaNode`, in v1.37 through [KEP-6072](https://github.com/kubernetes/enhancements/issues/6072). The NVIDIA GPU DRA driver publishes both as of [v0.5.0](https://github.com/kubernetes-sigs/dra-driver-nvidia-gpu/releases/tag/v0.5.0). A `matchAttribute` constraint aligns the devices allocated for one ResourceClaim. Several pods can share one ResourceClaim, and each container can take only its own request from it ([API](https://github.com/kubernetes/kubernetes/blob/v1.37.0/staging/src/k8s.io/api/core/v1/types.go#L3109-L3114)), so a shared claim can align devices that different pods use. Grove already creates such shared claims: [resourceSharing](../390-hierarchical-resource-sharing/README.md) creates one ResourceClaim per PodCliqueScalingGroup replica from a template and injects it into every pod of the replica. Two things are missing. Every container gets the whole claim, so the pods cannot each have their own devices in it, and nothing constrains the claim's devices to one intra-node domain.

One ambiguity has to be resolved as well. `pack` applies to "each replica of the resource". At the PodCliqueSet and PodCliqueScalingGroup levels a replica is a group of pods. At the PodClique level a replica is a single pod, yet the implementation packs all pods of the clique together: the operator emits one `PodGroup` per clique, and the KAI backend turns it into a subgroup whose topology constraint covers all of its pods. For node-scoped domains the two readings never differed in practice, because a single pod always fits in one host. For intra-node domains they differ: packing the clique's pods together (resource-level packing) or packing each pod on its own (replica-level packing). The API has to say which one it means.

### Goals

- Let cluster administrators declare intra-node topology levels, NUMA node and PCIe root, in a `ClusterTopologyBinding` without a node label.
- Define the PodClique-level meaning of `pack` explicitly, and give a clear path for each of the two readings.
- Let workload authors require that the pods of a group be placed within one instance of an intra-node domain.
- Enforce those constraints through resourceSharing claims and standard DRA allocation, without scheduler changes.
- Reject intra-node constraints at admission when they cannot be enforced, instead of accepting and ignoring them.
- Leave existing `ClusterTopologyBinding` levels, `PodGang` fields, and backend behavior unchanged.

### Non-Goals

- Choosing devices or NUMA nodes in Grove. The scheduler's DRA allocator does that.
- A Grove API for locality within a single pod. The pod's own ResourceClaimTemplate already expresses it (see [Story 1](#story-1-numa-aligned-multi-gpu-pods)).
- Changing how existing resourceSharing entries behave. The per-pod option is opt-in.
- Preferred (best-effort) intra-node constraints. DRA constraints are hard requirements.
- Aligning CPUs and memory with the devices. DRA devices do not take part in the kubelet's Topology Manager alignment ([KEP-5517](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/5517-dra-node-allocatable-resources/README.md)), so a claim aligns devices only.
- Pods that get their devices through device plugins instead of DRA.
- A spread constraint. See [Spread Constraints](#spread-constraints), which recommends a follow-up.
- Multi-node NVLink domains, which GREP-417 covers.

## Proposal

The proposal has four parts.

1. **Intra-node levels.** A `ClusterTopologyBinding` gets an optional `intraNodeLevels` list. Each entry names a domain and a `type`, `NUMANode` or `PCIeRoot`, and each type corresponds to a standardized DRA device attribute. Intra-node levels are narrower than every entry in `levels`, so the existing hierarchy rules extend to them.
2. **PodClique-level `pack`.** `pack` on a PodClique keeps its current meaning, resource-level packing, and its documentation now says so. Replica-level packing is expressed in the pod's own ResourceClaimTemplate, as in Story 1.
3. **Per-pod devices in resourceSharing claims.** resourceSharing entries get a `deviceAssignment` option. With `PerPod`, the claim created from the template holds a copy of the template's requests for each pod that shares it, and each pod's containers use only that pod's copy. `pack.required` may name an intra-node domain on a PodCliqueScalingGroup or on one of its member PodCliques. Grove then adds a `matchAttribute` constraint for that domain to the group's `PerPod` claim.
4. **Admission.** The PodCliqueSet webhook rejects intra-node constraints that cannot be enforced: `preferred` intra-node domains, constraints outside PodCliqueScalingGroups, groups without a `PerPod` resourceSharing entry that covers them, claims that would exceed DRA's per-claim limits, and scheduler backends that do not support shared claims.

A binding for 8-GPU nodes with two NUMA nodes each:

```yaml
apiVersion: grove.io/v1alpha1
kind: ClusterTopologyBinding
metadata:
  name: h100-topology
spec:
  levels:
    - domain: zone
      key: topology.kubernetes.io/zone
    - domain: rack
      key: example.com/rack
    - domain: host
      key: kubernetes.io/hostname
  intraNodeLevels:
    - domain: numa
      type: NUMANode
```

### User Stories

#### Story 1: NUMA-Aligned Multi-GPU Pods

This is Story 3 of GREP-244. As a developer running a tensor-parallel worker that uses 2 of the 8 GPUs on a node, I want both GPUs from the same NUMA node, and different workers may use different NUMA nodes. This needs no Grove API. The pod's own ResourceClaimTemplate requires it:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: two-gpus-one-numa-node
spec:
  spec:
    devices:
      requests:
        - name: gpus
          exactly:
            deviceClassName: gpu.nvidia.com
            count: 2
      constraints:
        - requests: [gpus]
          matchAttribute: resource.kubernetes.io/numaNode
```

The PodClique's pod template references this template as it would any other. [#850](https://github.com/ai-dynamo/grove/pull/850) documents this path in Grove's user guide.

#### Story 2: Disaggregated Prefill and Decode on One NUMA Node

As a developer running disaggregated inference on multi-socket nodes, I want each prefill worker in the same NUMA node as the decode workers that read its KV cache, so KV-cache transfers do not cross the inter-socket link (dynamo#10171). A PodCliqueScalingGroup whose replica holds one prefill pod and two decode pods expresses this. Its `PerReplica` resourceSharing entry gives each replica one claim, `deviceAssignment: PerPod` gives each pod its own GPU in that claim, and the intra-node constraint keeps the replica's GPUs on one NUMA node:

```yaml
resourceClaimTemplates:
  - name: gpu
    templateSpec:
      spec:
        devices:
          requests:
            - name: gpu
              exactly:
                deviceClassName: gpu.nvidia.com
                count: 1
cliques:
  - name: prefill
    spec:
      replicas: 1
  - name: decode
    spec:
      replicas: 2
podCliqueScalingGroups:
  - name: pd
    cliqueNames: [prefill, decode]
    replicas: 2
    resourceSharing:
      - name: gpu
        scope: PerReplica
        deviceAssignment: PerPod
    topologyConstraint:
      topologyName: h100-topology
      pack:
        required: numa
```

Each `pd` replica gets a claim with three GPU requests, one per pod, and one constraint that they share a NUMA node, as shown in [Enforcement](#enforcement-per-pod-devices-in-resourcesharing-claims). The constraint keeps each replica together but does not coordinate the replicas. They end up on different NUMA nodes or different hosts here only because two three-GPU replicas cannot fit in one four-GPU NUMA node.

By contrast, `pack.required: numa` on a prefill PodClique with two pods would put both in one NUMA node, the slow layout dynamo#10171 reports.

### Limitations/Risks & Mitigations

- **Group size is fixed when the claim is allocated.** A claim is allocated as a whole when the first pod that uses it is scheduled, and it cannot grow afterwards. A PodCliqueScalingGroup scales by whole replicas, and its member PodCliques cannot autoscale on their own, so each replica keeps the same pods. Standalone PodCliques and PodCliqueSet replicas can change size, so phase 1 limits `PerPod` entries and intra-node constraints to PodCliqueScalingGroups and their member PodCliques. A standalone clique can get the same behavior by wrapping it in a single-clique PodCliqueScalingGroup.
- **A group is tied to one node.** GPUs are node-local, so all pods of a group run on the node where the claim is allocated. That is inherent to intra-node packing. The claim keeps its name when a pod is recreated, as every resourceSharing claim does today, so a recreated pod returns to that node while the claim is allocated. The resource claim controller keeps a pod's reservation until the pod object is gone ([source](https://github.com/kubernetes/kubernetes/blob/v1.37.0/pkg/controller/resourceclaim/controller.go#L1511-L1535)), so a pod stuck terminating on an unreachable node keeps the claim on that node. This is the standard lost-node case, and it clears the standard way: an `out-of-service` taint on the node, or deleting the Node, lets PodGC delete the stuck pods ([source](https://github.com/kubernetes/kubernetes/blob/v1.37.0/pkg/controller/podgc/gc_controller.go#L148-L186)), and the claim is released once its last consumer is gone. *Mitigation:* the user guide describes this recovery path. Faster failover, if it is ever needed, belongs in resourceSharing as a whole.
- **The first pod reserves devices for the whole group.** When the claim is allocated for the first pod, every device in it is reserved, on a node chosen for that pod. If the rest of the group cannot fit on that node for another reason, such as CPU or memory, those pods stay pending while the devices remain reserved. *Mitigation:* a backend that schedules the gang as a unit checks the whole group before placing any of it. The kube backend does not schedule gangs until WAS support lands, so on kube a group can get stuck this way. The user guide will say so, and beta requires a backend that schedules gangs as a unit.
- **Devices only.** The claim aligns devices. CPUs and memory still follow the kubelet's CPU and memory managers. Once CPUs are available as DRA devices, for example through [dra-driver-cpu](https://github.com/kubernetes-sigs/dra-driver-cpu), which publishes `numaNode`, the resourceSharing template can request them alongside the GPUs.
- **Devices without NUMA affinity.** A device whose NUMA node is unknown does not publish `numaNode`, and `matchAttribute` never selects a device that lacks the attribute. A constrained group stays pending on such nodes. *Mitigation:* the pods' pending reason reports the allocation failure, and the user guide calls this out.
- **Shared claims need support from the scheduler and drivers.** kube-scheduler has allocated shared claims since DRA reached GA in v1.34. Whether other backends honor constraints on claims shared across a gang, and whether each DRA driver prepares a claim that several pods share, has to be validated. *Mitigation:* backends opt in through the interface in [Admission](#admission), and the alpha criteria include validation with the NVIDIA GPU DRA driver.

## Design Details

### ClusterTopologyBinding: Intra-Node Levels

```go
// ClusterTopologyBindingSpec defines the desired topology hierarchy and backend binding behavior.
type ClusterTopologyBindingSpec struct {
	...
	// IntraNodeLevels is an ordered list of topology levels inside a single node,
	// from broadest to narrowest. Every intra-node level is narrower than every entry
	// in Levels. Intra-node levels have no node label and are identified by Type.
	// +optional
	// +kubebuilder:validation:MaxItems=2
	IntraNodeLevels []IntraNodeTopologyLevel `json:"intraNodeLevels,omitempty"`
	...
}

// IntraNodeTopologyLevel defines a topology level inside a single node.
type IntraNodeTopologyLevel struct {
	// Domain is the topology domain name used in TopologyConstraint references.
	// Must be unique across Levels and IntraNodeLevels.
	// +kubebuilder:validation:Required
	Domain TopologyDomain `json:"domain"`
	// Type identifies the hardware boundary this level represents.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=NUMANode;PCIeRoot
	Type IntraNodeLevelType `json:"type"`
}

// IntraNodeLevelType identifies an intra-node hardware boundary.
type IntraNodeLevelType string

const (
	// IntraNodeLevelNUMANode groups devices by resource.kubernetes.io/numaNode.
	IntraNodeLevelNUMANode IntraNodeLevelType = "NUMANode"
	// IntraNodeLevelPCIeRoot groups devices by resource.kubernetes.io/pcieRoot.
	IntraNodeLevelPCIeRoot IntraNodeLevelType = "PCIeRoot"
)
```

With the alpha `DRAListTypeAttributes` gate, `numaNode` can be a list, and `matchAttribute` then only requires the devices' lists to overlap, so devices on different NUMA nodes of the same socket can match. Grove passes the constraint through unchanged, and the user guide will warn that the gate weakens a required constraint.

The effective hierarchy is `levels` followed by `intraNodeLevels`. The ClusterTopologyBinding webhook adds these checks to the ones GREP-244 defines:

- Domain names are unique across `levels` and `intraNodeLevels` together.
- Each `type` appears at most once in `intraNodeLevels`.
- When both types are present, `NUMANode` comes before `PCIeRoot`, because a PCIe root complex belongs to one NUMA node.

Backends that derive a topology resource from `levels`, as the KAI backend does for its `Topology` resource, ignore `intraNodeLevels`, so auto-managed backend topology resources and drift detection are unchanged. Readers outside Grove that assume every entry in `levels` has a node label, such as the Dynamo operator's topology projection for KV-transfer routing, are unaffected as well.

Inside Grove, three readers look up a workload's domains, and all three go through `GetClusterTopologyLevels`, which returns only `levels`. Each has to account for the intra-node domains:

- The PodCliqueSet webhook validates constraints against the effective hierarchy.
- The PodCliqueSet status reconciler counts intra-node domains as available when it computes `TopologyLevelsUnavailable`. Otherwise every workload with an intra-node constraint would report the condition as `True`.
- PodGang sync skips intra-node domains deliberately, because the claim enforces them. Today such a domain would fall through to the path that treats a missing domain as drift and logs it on every sync.

### PodCliqueSet: Topology Constraints

The `TopologyConstraint` type is unchanged. The `Pack` documentation is corrected:

```go
	// Pack specifies topology packing constraints. On a PodCliqueSet or
	// PodCliqueScalingGroup, each replica is packed within one instance of the
	// domain. On a PodClique, all pods of the clique in a PodGang are packed
	// together within one instance of the domain.
	// +optional
	Pack *TopologyPackConstraint `json:"pack,omitempty"`
```

The PodCliqueSet webhook extends the rules from GREP-244 and [GREP-0368](../0368-preferred-topology-constraint/README.md). The rules apply to the effective required domain, `RequiredDomain()`, which falls back to the deprecated `packDomain`, so `packDomain: numa` is treated exactly like `pack.required: numa`:

- `pack.required` may name an intra-node domain on a PodCliqueScalingGroup or on a PodClique that is a member of one. On a PodCliqueSet or a standalone PodClique it is rejected in phase 1.
- `pack.preferred` may not name an intra-node domain.
- The hierarchy rule applies unchanged over the effective hierarchy. A member PodClique may narrow its PodCliqueScalingGroup's constraint, for example `pcie-root` inside `numa`.

GREP-0368's CEL rules reject `packDomain` on create, so the legacy spelling can only appear on a workload created before those rules. If such a workload names an intra-node domain outside the phase 1 scope and is never updated, admission never sees it, so the reconciler records an event instead of skipping the constraint silently.

### Enforcement: Per-Pod Devices in resourceSharing Claims

resourceSharing ([GREP-390](../390-hierarchical-resource-sharing/README.md)) already creates, names, owns, and cleans up ResourceClaims shared by a group of pods, and injects them into every pod of the group. Intra-node constraints build on it. The new work is a per-pod option on resourceSharing entries, a `request` on each container's claim reference, and the `matchAttribute` constraint.

`ResourceSharingSpec` gets one field:

```go
type ResourceSharingSpec struct {
	...
	// DeviceAssignment controls how the devices of each ResourceClaim created from
	// this template are divided among the pods that share it. Shared, the default,
	// gives every pod every device. PerPod gives each pod its own copy of the
	// template's requests.
	// +optional
	// +kubebuilder:default=Shared
	DeviceAssignment DeviceAssignment `json:"deviceAssignment,omitempty"`
}

// DeviceAssignment defines how a shared ResourceClaim's devices are assigned to pods.
// +kubebuilder:validation:Enum=Shared;PerPod
type DeviceAssignment string
```

`PerPod` is valid only where the set of pods sharing a claim is fixed: on a PodCliqueScalingGroup entry with `scope: PerReplica`, and on an `AllReplicas` entry of a PodClique that belongs to a PodCliqueScalingGroup. The webhook rejects it anywhere else.

**Claim spec.** For a `PerPod` entry, Grove builds the claim from the template instead of copying the template unchanged:

- **Requests.** Each template request is copied once for each pod that shares the claim, named from the clique name, the pod's index within the replica, and the original request name. Names are shortened with a hash when needed to fit DRA's name limits. Subrequests keep their names under the renamed request.
- **Constraints.** Each template constraint is copied once per pod and lists that pod's copies. An empty `requests` list is expanded to that pod's copies, because left empty it would apply across pods. Subrequest references (`<request>/<subrequest>`) are rewritten the same way.
- **Configuration.** Each `config` entry is copied once, with each request name replaced by every pod's copies of that request. An empty `requests` list stays empty, which applies the entry to every copy. The claim therefore has as many configuration entries as the template.
- **Intra-node constraints.** Each intra-node constraint adds one `matchAttribute` constraint on its level's attribute, listing the copies of the pods it covers. A PodCliqueScalingGroup constraint covers every pod of the replica. A member PodClique constraint covers that clique's pods within the replica.

**Injection.** Grove adds the pod-level claim reference exactly as it does today. For a `PerPod` entry, each container's claim entry names the pod's own copies with `request:`, one entry per copy, instead of the whole claim. The kubelet gives each container only the devices of the requests it names, so each pod sees only its own GPUs.

The claim for one `pd` replica in Story 2, and the wiring for its second decode pod:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: my-inference-0-pd-1-gpu
spec:
  devices:
    requests:
      - name: prefill-0-gpu
        exactly: {deviceClassName: gpu.nvidia.com, count: 1}
      - name: decode-0-gpu
        exactly: {deviceClassName: gpu.nvidia.com, count: 1}
      - name: decode-1-gpu
        exactly: {deviceClassName: gpu.nvidia.com, count: 1}
    constraints:
      - requests: [prefill-0-gpu, decode-0-gpu, decode-1-gpu]
        matchAttribute: resource.kubernetes.io/numaNode
---
# Second decode pod of that replica
spec:
  resourceClaims:
    - name: my-inference-0-pd-1-gpu
      resourceClaimName: my-inference-0-pd-1-gpu
  containers:
    - name: worker
      resources:
        claims:
          - name: my-inference-0-pd-1-gpu
            request: decode-1-gpu
```

The scheduler allocates the whole claim when it schedules the first pod of the group, choosing devices on one node that satisfy every constraint, and places the rest of the group on that node. Nothing in the scheduler needs to know about Grove: from its point of view, the group is a set of pods sharing a claim.

**Lifecycle.** Creating, naming, owning, and cleaning up the claim work as resourceSharing does them today. A PodCliqueScalingGroup gets one claim per replica, owned by the PodCliqueScalingGroup, with a name that stays the same when pods are recreated. Scale-in deletes the claims of removed replicas. Also as today, a claim's spec never changes after it is created, so a template change that alters device requests, or a change to a member PodClique's replica count, only takes effect in a new claim.

The `PodGang` API is unchanged. The scheduler learns about the constraint through the claim, so it needs no new information in the gang.

[KEP-5729](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/5729-resourceclaim-support-for-workloads/README.md) lets Kubernetes generate one claim per PodGroup from a template (`DRAWorkloadResourceClaims`, beta and off by default in v1.37). If resourceSharing adopts it for backends that use the Workload API, Kubernetes could create these claims instead of Grove. Grove would still wire each pod to its copies.

### Admission

A new optional backend interface marks backends that can schedule groups that share a claim:

```go
// SharedResourceClaimBackend is implemented by scheduler backends that allocate a
// ResourceClaim referenced by several pods of a gang, honoring the claim's
// constraints, and place those pods on the node the claim is allocated on.
type SharedResourceClaimBackend interface {
	// SchedulesSharedResourceClaims marks the capability. It has no behavior.
	SchedulesSharedResourceClaims()
}
```

The PodCliqueSet webhook already resolves one scheduler backend per PodCliqueSet. When a PodCliqueSet has an intra-node constraint, the webhook rejects it with an error naming the reason if any of these hold:

- The backend does not implement the interface.
- No single `PerPod` resourceSharing entry covers the constraint's group. That entry is on the PodCliqueScalingGroup for a PodCliqueScalingGroup constraint, or on the member PodClique when only the PodClique has an intra-node constraint. Every device the group should align must come from that one entry, because DRA constraints cannot span claims.
- The claim would exceed DRA's per-claim limits: 32 requests, 32 constraints, and 32 allocated devices where the template fixes the device count ([API](https://github.com/kubernetes/kubernetes/blob/v1.37.0/staging/src/k8s.io/api/resource/v1/types.go#L1270-L1272)). Configuration entries are not multiplied per pod, so the template's own count applies.

Templates declared in the PodCliqueSet's `resourceClaimTemplates` are part of the PodCliqueSet, so these checks always run for them. An external ResourceClaimTemplate may not exist yet when a PodCliqueSet is created, so the webhook checks it only if it exists, and the reconciler reports later failures as described in [Monitoring](#monitoring).

### Scheduler Backends

| Backend | Shared ResourceClaims |
|---|---|
| kube | Supported, without gang scheduling until WAS support lands. kube-scheduler allocates a shared claim with its constraints and places later pods on the claim's node ([source](https://github.com/kubernetes/kubernetes/blob/v1.37.0/pkg/scheduler/framework/plugins/dynamicresources/dynamicresources.go)). |
| KAI | To be confirmed by the backend owner. KAI's NUMA plugin does not cover DRA-backed resources ([KAI-Scheduler#1859](https://github.com/kai-scheduler/KAI-Scheduler/issues/1859)). This path relies on KAI's DRA allocation instead. |
| Volcano | To be confirmed by the backend owner. |
| LPX | Not supported. LPX rejects topology constraints. |

The kube backend does not enforce node-scoped `pack` today: its `ValidatePodCliqueSet` accepts it and its `SyncPodGang` is a no-op. A PodCliqueSet that combines a node-scoped and an intra-node constraint therefore gets only the intra-node part on kube. Rejecting node-scoped `pack` on the kube backend is a separate fix.

### Spread Constraints

Discussion of this proposal raised whether a spread API makes sense. A spread constraint would place pods across instances of a domain, for example at most one prefill pod per NUMA node, which is the direct fix for the layout in dynamo#10171.

For node-scoped domains, Kubernetes already has pod `topologySpreadConstraints`. A Grove spread constraint over node labels would duplicate them.

For intra-node domains, spread makes sense, and `PerPod` claims make it expressible. DRA's `distinctAttribute` constraint, beta and on by default since v1.36 under `DRAConsumableCapacity`, requires the devices of the listed requests to have distinct values of an attribute. It is the inverse of `matchAttribute` ([API](https://github.com/kubernetes/kubernetes/blob/v1.37.0/staging/src/k8s.io/api/resource/v1/types.go)). A `PerPod` claim could combine both. For the dynamo#10171 layout on one node, one claim could require each prefill pod to share a NUMA node with its two decode pods, and require the two prefill pods to use different NUMA nodes.

This GREP still recommends a follow-up rather than including spread now, for two reasons:

- Spreading groups needs one claim across all of them, so those groups must be sized and scaled together. That conflicts with PodCliqueScalingGroup scale-out, which adds replicas after the first claim is allocated.
- `distinctAttribute` applies to every device of the listed requests. A pod with two GPUs could not keep both on one NUMA node while being spread from another pod, so spread would be limited to pods with one device each.

The API has room for it as a `spread` field next to `pack` in `topologyConstraint`.

### Open Questions

These are the decisions to settle in review:

1. Phase 1 scope: PodCliqueScalingGroups and their member PodCliques only, or also standalone PodCliques with a fixed replica count.
2. Whether an intra-node constraint should align devices from several resourceSharing entries of the same group. DRA constraints cannot span claims, so phase 1 requires one `PerPod` entry that holds every device to align.
3. Whether KAI and Volcano can schedule gangs that share claims, and when the kube backend gains gang scheduling.
4. How `PerPod` claims should follow template updates that change device requests or a member PodClique's replica count. A claim never changes after creation, which already holds for every resourceSharing claim.
5. Whether spread gets a follow-up GREP, and whether it should be limited to pods with one device each.

### Monitoring

- The `TopologyLevelsUnavailable` condition counts intra-node domains as available, as described in [ClusterTopologyBinding: Intra-Node Levels](#clustertopologybinding-intra-node-levels). If an administrator removes an intra-node level that a deployed PodCliqueSet uses, the condition is set and Grove stops adding that domain's `matchAttribute` to new claims. Existing claims keep theirs.
- When Grove cannot build a `PerPod` claim, for example because an external template is missing or the claim would exceed DRA's limits, it records an event on the PodCliqueSet and does not create the group's pods. A condition can be added later if events prove insufficient.
- When allocation fails, the pods' scheduling events report it as for any other DRA claim.

### Dependencies

- Kubernetes with DRA, GA since v1.34. `numaNode` is standardized as of v1.37.
- DRA drivers that publish the standardized attributes, such as the NVIDIA GPU DRA driver v0.5.0 or later.
- A scheduler backend that implements `SharedResourceClaimBackend`.
- No new RBAC. resourceSharing already needs to create and delete ResourceClaims and read ResourceClaimTemplates, and the operator's ClusterRole grants both.

### Test Plan

Three behaviors matter most. Each gets unit coverage in the webhook and the claim builder, and an e2e test:

1. A group whose devices fit in one intra-node domain is placed on one node, each pod gets only its own copies, and the devices share the level's attribute.
2. A group that fits in no domain stays pending, and admission rejects constraints that cannot be enforced.
3. `PerPod` claims follow resourceSharing's lifecycle: one per replica on scale-out, deleted on scale-in, and the same claim used again when a pod is recreated.

E2E tests run on kind with [dra-example-driver](https://github.com/kubernetes-sigs/dra-example-driver), whose mock GPUs can publish the standard `pcieRoot` attribute from a configured list of roots, so the `PCIeRoot` level needs no hardware. Testing the `NUMANode` level the same way needs a small change to that driver to publish `numaNode` on its mock GPUs.

### Graduation Criteria

#### Alpha

- `intraNodeLevels`, `deviceAssignment: PerPod`, and intra-node `pack.required` on PodCliqueScalingGroups and their member PodCliques, enforced with the kube backend, which does not schedule gangs yet.
- Admission rejects intra-node constraints that cannot be enforced.
- The user guide documents per-pod locality through ResourceClaimTemplates (Story 1) and the recovery path for a group on a lost node.
- `PerPod` claims validated with the NVIDIA GPU DRA driver.

#### Beta

- Validated on multi-socket GPU nodes with a real workload, for example the dynamo#10171 layout.
- `PerPod` claims enforced on a backend that schedules gangs as a unit.
- A decision on spread.
- No breaking API changes since alpha.

#### GA

- Stable API.
- No open issues against the feature.

## Implementation History

- 2026-06-02: [#644](https://github.com/ai-dynamo/grove/issues/644) opened, asking how Grove will support intra-node NUMA-aware scheduling.
- 2026-07-07: Direction agreed in #644: Grove expresses packing intent, and enforcement comes from scheduler-native mechanisms or DRA.
- 2026-09-23: #644 reopened, with implementation handled per backend. The same day, the thread split the problem into locality within a pod, which pod templates already cover, and locality across a group, which needs this GREP. This GREP drafted.
- 2026-09-24: The #644 discussion favors documenting per-pod locality over wrapping it in `topologyConstraint`. [#850](https://github.com/ai-dynamo/grove/pull/850) adds that documentation.
- 2026-09-29: Revised after review to build on resourceSharing instead of a separate claim mechanism, and to keep resourceSharing's stable claim names.

## Alternatives

### Intra-Node Levels in the Levels List

Intra-node levels could go into `levels`, with `key` made optional and a field marking a level as intra-node. That keeps a single ordered list, but every reader of `levels` would have to learn to skip entries without a key. Within Grove, the KAI backend copies every level into its `Topology` resource as a node label, and drift detection compares the two lists. Outside Grove, the Dynamo operator reads the levels to project topology labels for KV-transfer routing. A separate list leaves all of them unchanged.

### Scheduler-Native Enforcement Through PodGang

Grove could pass intra-node constraints to schedulers through new `PodGang` fields and leave enforcement to each scheduler. No scheduler can place several pods in one NUMA node today: KAI's NUMA plugin and the NodeResourceTopologyMatch plugin evaluate one pod at a time, and Volcano's numa-aware plugin covers CPUs only. With device plugins, the kubelet also picks the devices, so a scheduler's choice of NUMA node for a group would not bind the allocation. DRA allocation does bind it. A backend that adds native support later can propose `PodGang` fields then.

### A Grove Field for Per-Pod Packing

Grove could add a PodClique field, such as `podPack` or a `scope` on `pack`, meaning each pod within one instance of a domain. The #644 discussion considered this option and favors documentation instead. With DRA, Grove would enforce it by adding a `matchAttribute` constraint to the pod's own claim, which the user can already write directly, as in Story 1. With device plugins, no mechanism is available to Grove. The field would add API surface without adding expressiveness. It is worth revisiting if a backend without DRA gains a per-pod mechanism that needs a portable knob.

### A DRA Attribute Instead of a Level Type

Each intra-node level could name a DRA device attribute directly instead of a `type`. That is more flexible, but it ties the Grove API to DRA naming, and a future scheduler-native path could not interpret an arbitrary attribute. Upstream standardization fixes the attribute for each type, so the mapping lives in Grove.

### Copying Requests From the Pods' Own Templates

An earlier draft of this GREP built the group's claim from each pod's own ResourceClaimTemplates, so users would not have to change their pod specs. That meant Grove rewriting user-written claims, handling several templates per pod with overlapping request names, and running a claim mechanism parallel to resourceSharing. Declaring the group's devices in a resourceSharing entry keeps one mechanism for claims shared across pods, and the user states the group's devices where the group is defined.

### resourceSharing Without Per-Pod Devices

A `Shared` resourceSharing entry whose template requests all of a group's GPUs, with a `matchAttribute` in the template, would already keep those GPUs on one NUMA node. But every container in the group gets every device in the claim, so each pod would see all of the group's GPUs. `PerPod` closes that gap.

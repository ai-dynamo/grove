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
  - [Enforcement: Group ResourceClaims](#enforcement-group-resourceclaims)
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
  - [Shared Claims Written by Users](#shared-claims-written-by-users)
<!-- /toc -->

## Summary

Grove's topology model ([GREP-244](../244-topology-aware-scheduling/README.md)) identifies every topology level by a node label. That covers `region` through `host`, but not domains inside a node. `numa` is already a well-known domain name and GREP-244 Story 3 motivates it, yet no scheduler backend can act on `pack.required: numa` today. Following the discussion in [#644](https://github.com/ai-dynamo/grove/issues/644), this GREP separates two cases. Locality within one pod, such as two GPUs from the same NUMA node, is already expressible with a DRA constraint in the pod's own ResourceClaimTemplate and needs no new Grove API. Locality across a group of pods, such as a prefill worker and the decode workers that read its KV cache sharing a NUMA node, needs Grove, because only Grove knows which pods form the group. This GREP adds intra-node levels to `ClusterTopologyBinding`, defines PodClique-level `pack` as packing all pods of the clique together, and lets `pack.required` name an intra-node domain on a PodCliqueScalingGroup or its member PodCliques. Grove enforces such a constraint by generating one ResourceClaim per group and giving each pod its own requests in it, so any scheduler that allocates shared DRA claims honors the constraint without scheduler changes. The GREP also addresses whether Grove should offer a spread constraint.

## Motivation

On multi-socket GPU nodes, where a pod's devices sit relative to each other and to the CPUs affects performance. Traffic between a GPU and host memory, between a GPU and its RDMA NIC, and between GPUs that cannot use peer-to-peer over NVLink all cross the inter-socket link when the endpoints are on different NUMA nodes. For disaggregated inference, [dynamo#10171](https://github.com/ai-dynamo/dynamo/issues/10171) reports about 50% lower throughput and about 30x higher per-batch latency when both prefill workers on an 8-GPU node land on one PCIe root and half of the decode workers sit on the other.

Grove cannot express any of this today. As discussed in #644, `TopologyDomainNuma` is declared in the API but nothing consumes it. `TopologyLevel.key` is a required node label key, and a NUMA node has no node label, so an intra-node level cannot be declared in a `ClusterTopologyBinding`. Declaring one with a placeholder label would be worse than not declaring it, because the KAI backend copies every level into its `Topology` resource as a node label.

Scheduler-native NUMA support works one pod at a time. KAI Scheduler's NUMA plugin ([v0.16.0](https://github.com/kai-scheduler/KAI-Scheduler/releases/tag/v0.16.0), with scoring added in [v0.17.0](https://github.com/kai-scheduler/KAI-Scheduler/releases/tag/v0.17.0)) filters and scores nodes for each pod using NodeResourceTopology data and the node's kubelet Topology Manager policy. A workload cannot request it, and it does not place several pods of a gang in one NUMA node ([design](https://github.com/kai-scheduler/KAI-Scheduler/blob/v0.17.0/docs/developer/designs/numa-topology/README.md)). Volcano's numa-aware plugin covers CPUs only ([design](https://github.com/volcano-sh/volcano/blob/v1.15.2/docs/design/numa-aware.md)). The kubelet's Topology Manager aligns each pod separately, and with device plugins it is the kubelet, not the scheduler, that picks the devices.

DRA now has the pieces Grove needs. Kubernetes has [standardized](https://kubernetes.io/docs/reference/node/dra-standard-device-attributes/) the device attributes `resource.kubernetes.io/pcieRoot`, in v1.34, and `resource.kubernetes.io/numaNode`, in v1.37 through [KEP-6072](https://github.com/kubernetes/enhancements/issues/6072). The NVIDIA GPU DRA driver publishes both as of [v0.5.0](https://github.com/kubernetes-sigs/dra-driver-nvidia-gpu/releases/tag/v0.5.0). A `matchAttribute` constraint aligns the devices allocated for one ResourceClaim. Several pods can share one ResourceClaim, and each container can take only its own request from it ([API](https://github.com/kubernetes/kubernetes/blob/v1.37.0/staging/src/k8s.io/api/core/v1/types.go#L3109-L3114)), so a shared claim can align devices that different pods use. What is missing is something that creates one claim per group and wires each pod to its share of it. Grove already creates ComputeDomains and injects claim references into pod specs for [auto-MNNVL](../417-auto-mnnvl/README.md), so it is a natural place to do this.

One ambiguity has to be resolved as well. `pack` applies to "each replica of the resource". At the PodCliqueSet and PodCliqueScalingGroup levels a replica is a group of pods. At the PodClique level a replica is a single pod, yet the implementation packs all pods of the clique together: the operator emits one `PodGroup` per clique, and the KAI backend turns it into a subgroup whose topology constraint covers all of its pods. For node-scoped domains the two readings never differed in practice, because a single pod always fits in one host. For intra-node domains they differ: packing the clique's pods together (resource-level packing) or packing each pod on its own (replica-level packing). The API has to say which one it means.

### Goals

- Let cluster administrators declare intra-node topology levels, NUMA node and PCIe root, in a `ClusterTopologyBinding` without a node label.
- Define the PodClique-level meaning of `pack` explicitly, and give a clear path for each of the two readings.
- Let workload authors require that the pods of a group be placed within one instance of an intra-node domain.
- Enforce those constraints with standard DRA allocation, without scheduler changes.
- Reject intra-node constraints at admission when they cannot be enforced, instead of accepting and ignoring them.
- Leave existing `ClusterTopologyBinding` levels, `PodGang` fields, and backend behavior unchanged.

### Non-Goals

- Choosing devices or NUMA nodes in Grove. The scheduler's DRA allocator does that.
- A Grove API for locality within a single pod. The pod's own ResourceClaimTemplate already expresses it (see [Story 1](#story-1-numa-aligned-multi-gpu-pods)).
- Preferred (best-effort) intra-node constraints. DRA constraints are hard requirements.
- Aligning CPUs and memory with the devices. DRA devices do not take part in the kubelet's Topology Manager alignment ([KEP-5517](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/5517-dra-node-allocatable-resources/README.md)), so a group claim aligns devices only.
- Pods that get their devices through device plugins instead of DRA.
- A spread constraint. See [Spread Constraints](#spread-constraints), which recommends a follow-up.
- Multi-node NVLink domains, which GREP-417 covers.

## Proposal

The proposal has four parts.

1. **Intra-node levels.** A `ClusterTopologyBinding` gets an optional `intraNodeLevels` list. Each entry names a domain and a `type`, `NUMANode` or `PCIeRoot`, and each type corresponds to a standardized DRA device attribute. Intra-node levels are narrower than every entry in `levels`, so the existing hierarchy rules extend to them.
2. **PodClique-level `pack`.** `pack` on a PodClique keeps its current meaning, resource-level packing, and its documentation now says so. Replica-level packing is expressed in the pod's own ResourceClaimTemplate, as in Story 1.
3. **Group claims.** `pack.required` may name an intra-node domain on a PodCliqueScalingGroup or on one of its member PodCliques. For each group of pods such a constraint covers, Grove creates one ResourceClaim holding every pod's device requests, copied from the pods' ResourceClaimTemplates, plus a `matchAttribute` constraint for each intra-node constraint. Each pod references the group claim, and each container takes only its own requests from it.
4. **Admission.** The PodCliqueSet webhook rejects intra-node constraints that cannot be enforced: `preferred` intra-node domains, constraints outside PodCliqueScalingGroups, pods without claim templates to copy, pods that already use the claim name Grove reserves, groups that exceed DRA's per-claim limits, and scheduler backends that do not support shared claims.

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

As a developer running disaggregated inference on multi-socket nodes, I want each prefill worker in the same NUMA node as the decode workers that read its KV cache, so KV-cache transfers do not cross the inter-socket link (dynamo#10171). A PodCliqueScalingGroup whose replica holds one prefill pod and two decode pods expresses this, because a PodCliqueScalingGroup constraint applies to each of its replicas separately:

```yaml
cliques:
  - name: prefill
    spec:
      replicas: 1
      podSpec:
        resourceClaims:
          - name: gpu
            resourceClaimTemplateName: one-gpu
        containers:
          - name: worker
            resources:
              claims:
                - name: gpu
  - name: decode
    spec:
      replicas: 2
      # podSpec: same GPU claim as prefill
podCliqueScalingGroups:
  - name: pd
    cliqueNames: [prefill, decode]
    replicas: 2
    topologyConstraint:
      topologyName: h100-topology
      pack:
        required: numa
```

For each of the two `pd` replicas, Grove creates a claim with three GPU requests and one constraint that they share a NUMA node, as shown in [Enforcement](#enforcement-group-resourceclaims). The constraint keeps each replica together but does not coordinate the replicas. They end up on different NUMA nodes or different hosts here only because two three-GPU replicas cannot fit in one four-GPU NUMA node.

By contrast, `pack.required: numa` on a prefill PodClique with two pods would put both in one NUMA node, the slow layout dynamo#10171 reports.

### Limitations/Risks & Mitigations

- **Group size is fixed when the claim is allocated.** A claim is allocated as a whole when the first pod that uses it is scheduled, and it cannot grow afterwards. A PodCliqueScalingGroup scales by whole replicas, and its member PodCliques cannot autoscale on their own, so each replica keeps the same pods. Standalone PodCliques and PodCliqueSet replicas can change size, so phase 1 limits intra-node constraints to PodCliqueScalingGroups and their member PodCliques. A standalone clique can get the same behavior by wrapping it in a single-clique PodCliqueScalingGroup.
- **A group is tied to one node.** GPUs are node-local, so all pods of a group run on the node where the claim is allocated. That is inherent to intra-node packing. A pod recreated while the rest of its group still runs returns to that node. A group recreated as a whole cannot reuse its old claim: the resource claim controller only releases a pod that has terminated or was never scheduled ([source](https://github.com/kubernetes/kubernetes/blob/v1.37.0/pkg/controller/resourceclaim/controller.go#L1829-L1834)), so a pod stuck terminating on an unreachable node keeps the claim reserved there. *Mitigation:* each incarnation of a group gets its own claim, as described in [Enforcement](#enforcement-group-resourceclaims).
- **The first pod reserves devices for the whole group.** When the claim is allocated for the first pod, every device in it is reserved, on a node chosen for that pod. If the rest of the group cannot fit on that node for another reason, such as CPU or memory, those pods stay pending while the devices remain reserved. *Mitigation:* a backend that schedules the gang as a unit checks the whole group before placing any of it. The kube backend does not schedule gangs until WAS support lands, so on kube a group can get stuck this way. The user guide will say so, and beta requires a backend that schedules gangs as a unit.
- **Devices only.** The group claim aligns devices. CPUs and memory still follow the kubelet's CPU and memory managers. Once CPUs are available as DRA devices, for example through [dra-driver-cpu](https://github.com/kubernetes-sigs/dra-driver-cpu), which publishes `numaNode`, their requests can be copied into the group claim like any other.
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

The PodCliqueSet webhook extends the rules from GREP-244 and [GREP-0368](../0368-preferred-topology-constraint/README.md):

- `pack.required` may name an intra-node domain on a PodCliqueScalingGroup or on a PodClique that is a member of one. On a PodCliqueSet or a standalone PodClique it is rejected in phase 1.
- `pack.preferred` may not name an intra-node domain.
- The hierarchy rule applies unchanged over the effective hierarchy. A member PodClique may narrow its PodCliqueScalingGroup's constraint, for example `pcie-root` inside `numa`.

### Enforcement: Group ResourceClaims

A group is the set of pods covered by the outermost intra-node constraint. When a PodCliqueScalingGroup has an intra-node constraint, each of its replicas is a group. Otherwise, each member PodClique with an intra-node constraint forms one group per replica. Pods outside any intra-node constraint keep their own claims. For each incarnation of a group, Grove creates one ResourceClaim in the PodCliqueSet's namespace before creating the group's pods. A new incarnation starts whenever Grove recreates the group as a whole, for example during gang recovery. The claim:

- **Requests.** For every pod in the group and every request in that pod's ResourceClaimTemplates, the claim gets a copy of the request under a name unique within the claim. The name is derived from the clique name, the pod's index within the group, and the original request name, and is shortened with a hash when needed to fit DRA's name limits.
- **Constraints.** The templates' own constraints are copied and rewritten to the new request names. Each intra-node constraint then adds one `matchAttribute` constraint on its level's attribute, listing the requests of the pods it covers. A PodCliqueScalingGroup constraint covers every pod of the replica. A member PodClique constraint covers that clique's pods within the replica.
- **Configuration.** The templates' `config` entries are copied and rewritten the same way.

When Grove creates a pod, it replaces the pod's template-based claims with one reference to the group claim, and points each container at its own requests. The generated claim for one `pd` replica in Story 2, and the wiring for its second decode pod:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: my-inference-0-pd-1-topology
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
    - name: grove-topology
      resourceClaimName: my-inference-0-pd-1-topology
  containers:
    - name: worker
      resources:
        claims:
          - name: grove-topology
            request: decode-1-gpu
```

A container that referenced a whole template-based claim gets one entry per copied request from that claim. The kubelet gives each container only the devices of the requests it names, so each pod sees only its own GPUs.

The scheduler allocates the whole claim when it schedules the first pod of the group, choosing devices on one node that satisfy every constraint, and places the rest of the group on that node. Nothing in the scheduler needs to know about Grove: from its point of view, the group is a set of pods sharing a claim.

Lifecycle:

- The claim is owned by the PodCliqueScalingGroup, and its name identifies the group and its incarnation. The example above omits the incarnation.
- When Grove recreates a group as a whole, it creates a claim for the new incarnation instead of reusing the old one, because pods left on an unreachable node keep the old claim reserved on that node. Grove deletes the old claim, and the resource claim controller releases its devices once its last consumer is gone ([source](https://github.com/kubernetes/kubernetes/blob/v1.37.0/pkg/controller/resourceclaim/controller.go#L1608-L1632)).
- Scale-out creates a claim for each new replica, and scale-in deletes the claims of removed replicas.
- Claims that Grove injects for auto-MNNVL are left as separate claims and are not copied into the group claim.
- A pod template change that alters device requests needs a new claim for the whole replica. How this fits the rolling update strategies of [GREP-393](../393-coherent-rolling-updates/README.md) is an open question.

`PodGang` is unchanged. The scheduler learns about the constraint through the claim, so it needs no new information in the gang.

[KEP-5729](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/5729-resourceclaim-support-for-workloads/README.md) lets Kubernetes generate one claim per PodGroup from a template (`DRAWorkloadResourceClaims`, beta and off by default in v1.37). Once a backend that uses the Workload API adopts it, Kubernetes could create the group claim instead of Grove. Grove would still wire each pod to its requests.

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
- A pod in a constrained group has no claim from a ResourceClaimTemplate that Grove could copy, for example because it requests its GPUs through a device plugin. Devices that a pod requests through device plugins are never part of a group claim and are not constrained.
- A pod template already declares a claim named `grove-topology`, which Grove reserves for the group claim.
- The group's claim would exceed DRA's per-claim limits: 32 requests, 32 constraints, and 32 allocated devices where the templates fix the device count ([API](https://github.com/kubernetes/kubernetes/blob/v1.37.0/staging/src/k8s.io/api/resource/v1/types.go#L1270-L1271)).

ResourceClaimTemplates are separate objects that may not exist yet when a PodCliqueSet is created. The webhook runs template-dependent checks only for templates that already exist. If a template is missing or invalid when Grove builds a group claim, the reconciler reports it as described in [Monitoring](#monitoring).

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

For intra-node domains, spread makes sense, and group claims make it expressible. DRA's `distinctAttribute` constraint, beta and on by default since v1.36 under `DRAConsumableCapacity`, requires the devices of the listed requests to have distinct values of an attribute. It is the inverse of `matchAttribute` ([API](https://github.com/kubernetes/kubernetes/blob/v1.37.0/staging/src/k8s.io/api/resource/v1/types.go)). A group claim could combine both. For the dynamo#10171 layout on one node, one claim could require each prefill pod to share a NUMA node with its two decode pods, and require the two prefill pods to use different NUMA nodes.

This GREP still recommends a follow-up rather than including spread now, for two reasons:

- Spreading groups needs one claim across all of them, so those groups must be sized and scaled together. That conflicts with PodCliqueScalingGroup scale-out, which adds replicas after the first claim is allocated.
- `distinctAttribute` applies to every device of the listed requests. A pod with two GPUs could not keep both on one NUMA node while being spread from another pod, so spread would be limited to pods with one device each.

The API has room for it as a `spread` field next to `pack` in `topologyConstraint`.

### Open Questions

These are the decisions to settle in review:

1. Phase 1 scope: PodCliqueScalingGroups and their member PodCliques only, or also standalone PodCliques with a fixed replica count.
2. Copying device requests out of the pods' ResourceClaimTemplates, versus asking users to declare a group's devices explicitly.
3. Whether KAI and Volcano can schedule gangs that share claims, and when the kube backend gains gang scheduling.
4. How group claims interact with template updates that change device requests or a member PodClique's replica count, both of which need a new claim for every replica.
5. Whether spread gets a follow-up GREP, and whether it should be limited to pods with one device each.
6. What identifies a group incarnation in claim names: the PodGang epoch, which PodGang names already carry, or a counter Grove keeps.

### Monitoring

- The `TopologyLevelsUnavailable` condition on PodCliqueSet covers intra-node domains the same way it covers node-scoped domains. If an administrator removes an intra-node level that a deployed PodCliqueSet uses, the condition is set and Grove stops generating group claims for that constraint. Existing pods keep their claims.
- When Grove cannot build a group claim, for example because a template is missing or the claim would exceed DRA's limits, it records an event on the PodCliqueSet and does not create the group's pods. A condition can be added later if events prove insufficient.
- When allocation fails, the pods' scheduling events report it as for any other DRA claim.

### Dependencies

- Kubernetes with DRA, GA since v1.34. `numaNode` is standardized as of v1.37.
- DRA drivers that publish the standardized attributes, such as the NVIDIA GPU DRA driver v0.5.0 or later.
- A scheduler backend that implements `SharedResourceClaimBackend`.
- No new RBAC. The operator's ClusterRole already allows creating and deleting ResourceClaims and reading ResourceClaimTemplates.

### Test Plan

Three behaviors matter most. Each gets unit coverage in the webhook and the claim builder, and an e2e test:

1. A group whose devices fit in one intra-node domain is placed on one node, with devices that share the level's attribute.
2. A group that fits in no domain stays pending, and admission rejects constraints that cannot be enforced.
3. Claims follow the group's lifecycle: one per replica on scale-out, removed on scale-in, and a new claim when the group is recreated as a whole.

E2E tests run on kind with [dra-example-driver](https://github.com/kubernetes-sigs/dra-example-driver), whose mock GPUs can publish the standard `pcieRoot` attribute from a configured list of roots, so the `PCIeRoot` level needs no hardware. Testing the `NUMANode` level the same way needs a small change to that driver to publish `numaNode` on its mock GPUs.

### Graduation Criteria

#### Alpha

- `intraNodeLevels` and intra-node `pack.required` on PodCliqueScalingGroups and their member PodCliques, enforced through group claims with the kube backend, which does not schedule gangs yet.
- Admission rejects intra-node constraints that cannot be enforced.
- The user guide documents per-pod locality through ResourceClaimTemplates (Story 1).
- Group claims validated with the NVIDIA GPU DRA driver.

#### Beta

- Validated on multi-socket GPU nodes with a real workload, for example the dynamo#10171 layout.
- Group claims enforced on a backend that schedules gangs as a unit.
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

## Alternatives

### Intra-Node Levels in the Levels List

Intra-node levels could go into `levels`, with `key` made optional and a field marking a level as intra-node. That keeps a single ordered list, but every reader of `levels` would have to learn to skip entries without a key. Within Grove, the KAI backend copies every level into its `Topology` resource as a node label, and drift detection compares the two lists. Outside Grove, the Dynamo operator reads the levels to project topology labels for KV-transfer routing. A separate list leaves all of them unchanged.

### Scheduler-Native Enforcement Through PodGang

Grove could pass intra-node constraints to schedulers through new `PodGang` fields and leave enforcement to each scheduler. No scheduler can place several pods in one NUMA node today: KAI's NUMA plugin and the NodeResourceTopologyMatch plugin evaluate one pod at a time, and Volcano's numa-aware plugin covers CPUs only. With device plugins, the kubelet also picks the devices, so a scheduler's choice of NUMA node for a group would not bind the allocation. DRA allocation does bind it. A backend that adds native support later can propose `PodGang` fields then.

### A Grove Field for Per-Pod Packing

Grove could add a PodClique field, such as `podPack` or a `scope` on `pack`, meaning each pod within one instance of a domain. The #644 discussion considered this option and favors documentation instead. With DRA, Grove would enforce it by adding a `matchAttribute` constraint to the pod's own claim, which the user can already write directly, as in Story 1. With device plugins, no mechanism is available to Grove. The field would add API surface without adding expressiveness. It is worth revisiting if a backend without DRA gains a per-pod mechanism that needs a portable knob.

### A DRA Attribute Instead of a Level Type

Each intra-node level could name a DRA device attribute directly instead of a `type`. That is more flexible, but it ties the Grove API to DRA naming, and a future scheduler-native path could not interpret an arbitrary attribute. Upstream standardization fixes the attribute for each type, so the mapping lives in Grove.

### Shared Claims Written by Users

Users can write shared ResourceClaims themselves today. A workload would need one claim per group replica, created before its pods, and each pod would need to reference its own requests. That differs per pod, and a PodClique's single pod template cannot express it. Every scale-out would also need a new claim. Grove creates the pods and knows the groups, so it can do this generically.

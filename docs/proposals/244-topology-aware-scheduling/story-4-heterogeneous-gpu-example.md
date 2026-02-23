# Story 4: Heterogeneous GPU Cluster Example

This document provides a concrete example for [Story 4: Heterogeneous GPU Clusters](README.md#story-4-heterogeneous-gpu-clusters) in GREP-244. It demonstrates how separate ClusterTopology resources partition a cluster along hardware boundaries, with each topology's node label keys naturally directing workloads to the correct hardware segment.

## Cluster Setup

A cluster contains both DGX H100 nodes and GB200 NVL72 racks with **different node labels** for their topology levels:

**DGX H100 nodes** (8 nodes in 2 racks of 4):
* 8 GPUs per node, NVLink 4.0 within the node (900 GB/s via NVSwitch)
* InfiniBand NDR 400 Gbps between nodes
* Labels: `topology.kubernetes.io/zone`, `kubernetes.io/rack`, `kubernetes.io/hostname`

**GB200 NVL72 nodes** (4 racks of 72 GPUs each, grouped into 2 blocks):
* NVLink 5.0 spans the entire rack (1.8 TB/s across 72 GPUs)
* Labels: `topology.kubernetes.io/zone`, `example.com/nvl-block`, `example.com/nvlink-domain`, `kubernetes.io/hostname`

```
Cluster: us-east-1a
├── H100 Section
│   Labels: kubernetes.io/rack, kubernetes.io/hostname
│   ├── h100-rack-1: [dgx-h100-01 .. dgx-h100-04]  (4 nodes, 32 GPUs)
│   └── h100-rack-2: [dgx-h100-05 .. dgx-h100-08]  (4 nodes, 32 GPUs)
│
└── GB200 Section
    Labels: example.com/nvl-block, example.com/nvlink-domain, kubernetes.io/hostname
    ├── gb200-block-1
    │   ├── gb200-rack-1 (nvlink-domain: nvl-domain-1):  72 GPUs via NVLink 5.0
    │   └── gb200-rack-2 (nvlink-domain: nvl-domain-2):  72 GPUs via NVLink 5.0
    └── gb200-block-2
        ├── gb200-rack-3 (nvlink-domain: nvl-domain-3):  72 GPUs via NVLink 5.0
        └── gb200-rack-4 (nvlink-domain: nvl-domain-4):  72 GPUs via NVLink 5.0
```

The domain `rack` means the same thing conceptually (pack within a rack) but maps to `kubernetes.io/rack` for H100 and `example.com/nvlink-domain` for GB200. A single ClusterTopology cannot represent both.

## Topology Definitions

The administrator creates two ClusterTopology resources, one per hardware architecture:

```yaml
apiVersion: grove.io/v1alpha1
kind: ClusterTopology
metadata:
  name: h100-topology     # admin-created, matches DGX H100 nodes
spec:
  levels:
    - domain: zone
      key: topology.kubernetes.io/zone
    - domain: rack
      key: kubernetes.io/rack
    - domain: host
      key: kubernetes.io/hostname
---
apiVersion: grove.io/v1alpha1
kind: ClusterTopology
metadata:
  name: gb200-topology    # admin-created, matches GB200 NVL72 nodes
spec:
  levels:
    - domain: zone
      key: topology.kubernetes.io/zone
    - domain: block
      key: example.com/nvl-block
    - domain: rack
      key: example.com/nvlink-domain
    - domain: host
      key: kubernetes.io/hostname
```

## H100 Path

An ML engineer deploys on H100 nodes by referencing `h100-topology`:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: llama-70b-h100
spec:
  replicas: 1
  template:
    clusterTopologyName: h100-topology
    topologyConstraint:
      packDomain: zone
    cliques:
      - name: prefill
        topologyConstraint:
          packDomain: rack
        spec:
          roleName: prefill
          replicas: 4
          podSpec:
            containers:
              - name: prefill
                resources:
                  limits:
                    nvidia.com/gpu: "8"
      - name: decode
        topologyConstraint:
          packDomain: rack
        spec:
          roleName: decode
          replicas: 2
          podSpec:
            containers:
              - name: decode
                resources:
                  limits:
                    nvidia.com/gpu: "8"
```

Grove looks up `h100-topology` and resolves `rack` to `kubernetes.io/rack` on the PodGang.

## GB200 Path

A different engineer targets GB200 nodes by referencing `gb200-topology`:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: llama-405b-gb200
spec:
  replicas: 1
  template:
    clusterTopologyName: gb200-topology
    topologyConstraint:
      packDomain: zone
    cliques:
      - name: prefill
        topologyConstraint:
          packDomain: block     # pack prefill within a block (2 NVL72 racks)
        spec:
          roleName: prefill
          replicas: 2
          podSpec:
            containers:
              - name: prefill
                resources:
                  limits:
                    nvidia.com/gpu: "72"
      - name: decode
        topologyConstraint:
          packDomain: rack      # pack decode within a single NVL72 rack
        spec:
          roleName: decode
          replicas: 2
          podSpec:
            containers:
              - name: decode
                resources:
                  limits:
                    nvidia.com/gpu: "72"
```

Grove looks up `gb200-topology` and resolves `block` to `example.com/nvl-block`, `rack` to `example.com/nvlink-domain` on the PodGang. The `block` domain exists in this topology even though it is absent from the default.

## Without Multiple Topologies

Without multiple topologies, the cluster cannot be partitioned by hardware. All workloads resolve topology domains against the single default ClusterTopology, regardless of which hardware they target. The GB200 engineer faces two problems:

1. **Unknown domain `block`**: The default topology has no `block` level. The validating webhook rejects the PCS with a Rule-1 violation (domain existence), and the engineer receives an error indicating that `block` is not a valid domain in the referenced ClusterTopology. The engineer must either remove the `block` constraint or wait for a topology that includes it.
2. **Wrong label for `rack`**: Even if the engineer removes `block` to pass validation, Grove resolves `rack` to `kubernetes.io/rack` from the default topology, but GB200 nodes use `example.com/nvlink-domain`. The scheduler won't find matching nodes.

Both problems stem from forcing a single topology definition across hardware with fundamentally different interconnect hierarchies. Separate ClusterTopology resources solve this by letting each hardware segment define its own label-to-domain mapping.

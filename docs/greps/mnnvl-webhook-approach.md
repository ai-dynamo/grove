# MNNVL Support via Annotations and Webhooks

## Overview

This document proposes an alternative approach for adding automatic MNNVL (Multi-Node NVLink) support to Grove using **annotations and webhooks only**, requiring minimal changes to the Grove operator itself. This design enables workloads to leverage NVIDIA ComputeDomains and Dynamic Resource Allocation (DRA) for high-bandwidth GPU fabrics across nodes.

### Design Principles

1. **No API schema changes** — MNNVL configuration is expressed entirely through annotations
2. **Minimal Grove modifications** — Grove only needs to propagate annotations to child resources
3. **Webhook-driven resource management** — A separate webhook component handles ComputeDomain creation, ResourceClaim creation, and PodSpec injection
4. **Automatic lifecycle management** — Owner references and Kubernetes garbage collection handle cleanup

### Key Resources

| Resource | Purpose |
|----------|---------|
| **ComputeDomain** | NVIDIA CRD representing a group of GPUs that can communicate via NVLink |
| **ResourceClaim** | Kubernetes DRA resource that pods reference to join a ComputeDomain |
| **PodClique** | Grove resource containing PodSpec; webhook injects ResourceClaim references here |
| **PodGang** | Grove scheduler resource; used as owner for ComputeDomains and ResourceClaims |

---

## Annotation Schema

```yaml
grove.io/mnnvl-enabled: "true" | "false"
grove.io/mnnvl-scope: "replica" | "clique" | "scalingGroupReplica"
grove.io/mnnvl-device-class: "<device-class-name>"  # Optional, defaults to "compute-domain.nvidia.com"
```

| Annotation | Description |
|------------|-------------|
| `grove.io/mnnvl-enabled` | Enables MNNVL for the annotated resource and its children |
| `grove.io/mnnvl-scope` | Controls ComputeDomain granularity (see Scope Options below) |
| `grove.io/mnnvl-device-class` | DRA device class name (optional) |

### Scope Options

| Scope | Granularity | Use Case |
|-------|-------------|----------|
| `replica` | One ComputeDomain per PCS replica | Distributed training where all cliques need fast communication |
| `clique` | One ComputeDomain per PodClique | Isolated GPU workloads within a replica |
| `scalingGroupReplica` | One ComputeDomain per PCSG replica | Scaled inference with per-replica isolation |

### Annotation Placement

| Scope | Where to Place Annotations |
|-------|---------------------------|
| `replica` | PodCliqueSet metadata |
| `clique` | Individual clique template annotations |
| `scalingGroupReplica` | PodCliqueScalingGroup annotations |

---

## Example 1: PodCliqueSet Replica Level (`scope: replica`)

All PodCliques within a PCS replica share a single ComputeDomain. This is ideal for distributed training where workers and parameter servers need fast inter-node communication.

### Input: PodCliqueSet

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: distributed-training
  namespace: default
  annotations:
    grove.io/mnnvl-enabled: "true"
    grove.io/mnnvl-scope: "replica"
spec:
  replicas: 2
  template:
    cliques:
      - name: worker
        spec:
          roleName: worker
          replicas: 4
          podSpec:
            containers:
              - name: trainer
                image: nvcr.io/nvidia/pytorch:24.01-py3
                resources:
                  limits:
                    nvidia.com/gpu: 8
      - name: parameter-server
        spec:
          roleName: ps
          replicas: 2
          podSpec:
            containers:
              - name: ps
                image: nvcr.io/nvidia/pytorch:24.01-py3
                resources:
                  limits:
                    nvidia.com/gpu: 2
```

### Resulting ComputeDomains (2 total)

```yaml
apiVersion: resource.nvidia.com/v1alpha1
kind: ComputeDomain
metadata:
  name: distributed-training-0-cd
  namespace: default
  labels:
    grove.io/pcs-name: distributed-training
    grove.io/pcs-replica-index: "0"
    grove.io/mnnvl-managed: "true"
  ownerReferences:
    - apiVersion: scheduler.grove.io/v1alpha1
      kind: PodGang
      name: distributed-training-0
      controller: true
spec:
  numNodes: 0
---
apiVersion: resource.nvidia.com/v1alpha1
kind: ComputeDomain
metadata:
  name: distributed-training-1-cd
  namespace: default
  labels:
    grove.io/pcs-name: distributed-training
    grove.io/pcs-replica-index: "1"
    grove.io/mnnvl-managed: "true"
  ownerReferences:
    - apiVersion: scheduler.grove.io/v1alpha1
      kind: PodGang
      name: distributed-training-1
      controller: true
spec:
  numNodes: 0
```

### Resulting ResourceClaims (2 total)

```yaml
apiVersion: resource.k8s.io/v1beta1
kind: ResourceClaim
metadata:
  name: distributed-training-0-mnnvl-claim
  namespace: default
  labels:
    grove.io/pcs-name: distributed-training
    grove.io/pcs-replica-index: "0"
    grove.io/mnnvl-managed: "true"
  ownerReferences:
    - apiVersion: scheduler.grove.io/v1alpha1
      kind: PodGang
      name: distributed-training-0
      controller: true
spec:
  devices:
    requests:
      - name: mnnvl
        deviceClassName: compute-domain.nvidia.com
        selectors:
          - cel:
              expression: 'device.attributes["compute-domain.nvidia.com"].name == "distributed-training-0-cd"'
---
apiVersion: resource.k8s.io/v1beta1
kind: ResourceClaim
metadata:
  name: distributed-training-1-mnnvl-claim
  namespace: default
  labels:
    grove.io/pcs-name: distributed-training
    grove.io/pcs-replica-index: "1"
    grove.io/mnnvl-managed: "true"
  ownerReferences:
    - apiVersion: scheduler.grove.io/v1alpha1
      kind: PodGang
      name: distributed-training-1
      controller: true
spec:
  devices:
    requests:
      - name: mnnvl
        deviceClassName: compute-domain.nvidia.com
        selectors:
          - cel:
              expression: 'device.attributes["compute-domain.nvidia.com"].name == "distributed-training-1-cd"'
```

### Resulting PodCliques (after webhook injection)

Both PodCliques in the same replica reference the **same** ResourceClaim:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodClique
metadata:
  name: distributed-training-0-worker
  labels:
    grove.io/pcs-name: distributed-training
    grove.io/pcs-replica-index: "0"
    grove.io/podgang: distributed-training-0
  annotations:
    grove.io/mnnvl-enabled: "true"
    grove.io/mnnvl-scope: "replica"
spec:
  roleName: worker
  replicas: 4
  podSpec:
    resourceClaims:
      - name: mnnvl
        resourceClaimName: distributed-training-0-mnnvl-claim
    containers:
      - name: trainer
        image: nvcr.io/nvidia/pytorch:24.01-py3
        resources:
          limits:
            nvidia.com/gpu: 8
          claims:
            - name: mnnvl
---
apiVersion: grove.io/v1alpha1
kind: PodClique
metadata:
  name: distributed-training-0-parameter-server
  labels:
    grove.io/pcs-name: distributed-training
    grove.io/pcs-replica-index: "0"
    grove.io/podgang: distributed-training-0
  annotations:
    grove.io/mnnvl-enabled: "true"
    grove.io/mnnvl-scope: "replica"
spec:
  roleName: ps
  replicas: 2
  podSpec:
    resourceClaims:
      - name: mnnvl
        resourceClaimName: distributed-training-0-mnnvl-claim  # Same claim as worker
    containers:
      - name: ps
        resources:
          limits:
            nvidia.com/gpu: 2
          claims:
            - name: mnnvl
```

### Resource Topology

```
PCS: distributed-training (replicas: 2)
│
├── Replica 0
│   ├── PodGang: distributed-training-0
│   │   └── owns:
│   │       ├── ComputeDomain: distributed-training-0-cd
│   │       └── ResourceClaim: distributed-training-0-mnnvl-claim
│   │
│   ├── PodClique: distributed-training-0-worker (4 pods)
│   │   └── all pods share distributed-training-0-mnnvl-claim
│   │
│   └── PodClique: distributed-training-0-parameter-server (2 pods)
│       └── all pods share distributed-training-0-mnnvl-claim
│
└── Replica 1
    └── (same structure with distributed-training-1-*)
```

---

## Example 2: PodClique Level (`scope: clique`)

Each PodClique gets its own isolated ComputeDomain. This is useful when different model components need GPU fabric isolation.

### Input: PodCliqueSet

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: multi-model-serving
  namespace: default
spec:
  replicas: 1
  template:
    cliques:
      - name: llama-70b
        annotations:
          grove.io/mnnvl-enabled: "true"
          grove.io/mnnvl-scope: "clique"
        spec:
          roleName: llama
          replicas: 4
          podSpec:
            containers:
              - name: inference
                image: vllm/vllm-openai:latest
                resources:
                  limits:
                    nvidia.com/gpu: 8
      
      - name: mixtral
        annotations:
          grove.io/mnnvl-enabled: "true"
          grove.io/mnnvl-scope: "clique"
        spec:
          roleName: mixtral
          replicas: 2
          podSpec:
            containers:
              - name: inference
                image: vllm/vllm-openai:latest
                resources:
                  limits:
                    nvidia.com/gpu: 8
      
      - name: router
        # No MNNVL — CPU-only load balancer
        spec:
          roleName: router
          replicas: 2
          podSpec:
            containers:
              - name: router
                image: envoyproxy/envoy:v1.28-latest
```

### Resulting ComputeDomains (2 total — one per GPU clique)

```yaml
apiVersion: resource.nvidia.com/v1alpha1
kind: ComputeDomain
metadata:
  name: multi-model-serving-0-llama-70b-cd
  labels:
    grove.io/pcs-name: multi-model-serving
    grove.io/pcs-replica-index: "0"
    grove.io/clique-name: llama-70b
  ownerReferences:
    - kind: PodGang
      name: multi-model-serving-0
      controller: true
spec:
  numNodes: 0
---
apiVersion: resource.nvidia.com/v1alpha1
kind: ComputeDomain
metadata:
  name: multi-model-serving-0-mixtral-cd
  labels:
    grove.io/pcs-name: multi-model-serving
    grove.io/pcs-replica-index: "0"
    grove.io/clique-name: mixtral
  ownerReferences:
    - kind: PodGang
      name: multi-model-serving-0
      controller: true
spec:
  numNodes: 0
```

No ComputeDomain is created for the `router` clique (no MNNVL annotation).

### Resulting ResourceClaims (2 total)

```yaml
apiVersion: resource.k8s.io/v1beta1
kind: ResourceClaim
metadata:
  name: multi-model-serving-0-llama-70b-mnnvl-claim
  ownerReferences:
    - kind: PodGang
      name: multi-model-serving-0
spec:
  devices:
    requests:
      - name: mnnvl
        deviceClassName: compute-domain.nvidia.com
        selectors:
          - cel:
              expression: 'device.attributes["compute-domain.nvidia.com"].name == "multi-model-serving-0-llama-70b-cd"'
---
apiVersion: resource.k8s.io/v1beta1
kind: ResourceClaim
metadata:
  name: multi-model-serving-0-mixtral-mnnvl-claim
  ownerReferences:
    - kind: PodGang
      name: multi-model-serving-0
spec:
  devices:
    requests:
      - name: mnnvl
        deviceClassName: compute-domain.nvidia.com
        selectors:
          - cel:
              expression: 'device.attributes["compute-domain.nvidia.com"].name == "multi-model-serving-0-mixtral-cd"'
```

### Resulting PodCliques (after webhook injection)

```yaml
apiVersion: grove.io/v1alpha1
kind: PodClique
metadata:
  name: multi-model-serving-0-llama-70b
  annotations:
    grove.io/mnnvl-enabled: "true"
    grove.io/mnnvl-scope: "clique"
spec:
  replicas: 4
  podSpec:
    resourceClaims:
      - name: mnnvl
        resourceClaimName: multi-model-serving-0-llama-70b-mnnvl-claim
    containers:
      - name: inference
        resources:
          limits:
            nvidia.com/gpu: 8
          claims:
            - name: mnnvl
---
apiVersion: grove.io/v1alpha1
kind: PodClique
metadata:
  name: multi-model-serving-0-mixtral
  annotations:
    grove.io/mnnvl-enabled: "true"
    grove.io/mnnvl-scope: "clique"
spec:
  replicas: 2
  podSpec:
    resourceClaims:
      - name: mnnvl
        resourceClaimName: multi-model-serving-0-mixtral-mnnvl-claim  # Different claim!
    containers:
      - name: inference
        resources:
          limits:
            nvidia.com/gpu: 8
          claims:
            - name: mnnvl
---
apiVersion: grove.io/v1alpha1
kind: PodClique
metadata:
  name: multi-model-serving-0-router
  # No MNNVL annotations
spec:
  replicas: 2
  podSpec:
    # No resourceClaims injected
    containers:
      - name: router
        image: envoyproxy/envoy:v1.28-latest
```

### Resource Topology

```
PCS: multi-model-serving (replicas: 1)
│
└── Replica 0
    ├── PodGang: multi-model-serving-0
    │   └── owns:
    │       ├── ComputeDomain: multi-model-serving-0-llama-70b-cd
    │       ├── ResourceClaim: multi-model-serving-0-llama-70b-mnnvl-claim
    │       ├── ComputeDomain: multi-model-serving-0-mixtral-cd
    │       └── ResourceClaim: multi-model-serving-0-mixtral-mnnvl-claim
    │
    ├── PodClique: multi-model-serving-0-llama-70b
    │   └── 4 pods → llama-70b-mnnvl-claim → llama-70b-cd
    │
    ├── PodClique: multi-model-serving-0-mixtral
    │   └── 2 pods → mixtral-mnnvl-claim → mixtral-cd
    │
    └── PodClique: multi-model-serving-0-router
        └── 2 pods → no MNNVL (CPU only)
```

---

## Example 3: PodCliqueScalingGroup Replica Level (`scope: scalingGroupReplica`)

Each PCSG replica gets its own isolated ComputeDomain, enabling horizontal scaling of inference replicas with per-replica GPU fabric isolation.

### Input: PodCliqueSet

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: scalable-inference
  namespace: default
spec:
  replicas: 1
  template:
    cliques:
      - name: gpu-worker
        spec:
          roleName: worker
          replicas: 2
          podSpec:
            containers:
              - name: inference
                image: vllm/vllm-openai:latest
                resources:
                  limits:
                    nvidia.com/gpu: 4
    
    podCliqueScalingGroups:
      - name: inference-pool
        cliqueNames: ["gpu-worker"]
        replicas: 3
        minAvailable: 1
        annotations:
          grove.io/mnnvl-enabled: "true"
          grove.io/mnnvl-scope: "scalingGroupReplica"
```

### Resulting ComputeDomains (3 total — one per PCSG replica)

```yaml
apiVersion: resource.nvidia.com/v1alpha1
kind: ComputeDomain
metadata:
  name: scalable-inference-0-inference-pool-0-cd
  labels:
    grove.io/pcs-name: scalable-inference
    grove.io/pcs-replica-index: "0"
    grove.io/pcsg-name: inference-pool
    grove.io/pcsg-replica-index: "0"
  ownerReferences:
    - kind: PodGang
      name: scalable-inference-0-inference-pool-0
      controller: true
spec:
  numNodes: 0
---
apiVersion: resource.nvidia.com/v1alpha1
kind: ComputeDomain
metadata:
  name: scalable-inference-0-inference-pool-1-cd
  labels:
    grove.io/pcsg-replica-index: "1"
  ownerReferences:
    - kind: PodGang
      name: scalable-inference-0-inference-pool-1
      controller: true
spec:
  numNodes: 0
---
apiVersion: resource.nvidia.com/v1alpha1
kind: ComputeDomain
metadata:
  name: scalable-inference-0-inference-pool-2-cd
  labels:
    grove.io/pcsg-replica-index: "2"
  ownerReferences:
    - kind: PodGang
      name: scalable-inference-0-inference-pool-2
      controller: true
spec:
  numNodes: 0
```

### Resulting ResourceClaims (3 total)

```yaml
apiVersion: resource.k8s.io/v1beta1
kind: ResourceClaim
metadata:
  name: scalable-inference-0-inference-pool-0-mnnvl-claim
  ownerReferences:
    - kind: PodGang
      name: scalable-inference-0-inference-pool-0
spec:
  devices:
    requests:
      - name: mnnvl
        deviceClassName: compute-domain.nvidia.com
        selectors:
          - cel:
              expression: 'device.attributes["compute-domain.nvidia.com"].name == "scalable-inference-0-inference-pool-0-cd"'
---
apiVersion: resource.k8s.io/v1beta1
kind: ResourceClaim
metadata:
  name: scalable-inference-0-inference-pool-1-mnnvl-claim
  ownerReferences:
    - kind: PodGang
      name: scalable-inference-0-inference-pool-1
spec:
  devices:
    requests:
      - name: mnnvl
        deviceClassName: compute-domain.nvidia.com
        selectors:
          - cel:
              expression: 'device.attributes["compute-domain.nvidia.com"].name == "scalable-inference-0-inference-pool-1-cd"'
---
apiVersion: resource.k8s.io/v1beta1
kind: ResourceClaim
metadata:
  name: scalable-inference-0-inference-pool-2-mnnvl-claim
  ownerReferences:
    - kind: PodGang
      name: scalable-inference-0-inference-pool-2
spec:
  devices:
    requests:
      - name: mnnvl
        deviceClassName: compute-domain.nvidia.com
        selectors:
          - cel:
              expression: 'device.attributes["compute-domain.nvidia.com"].name == "scalable-inference-0-inference-pool-2-cd"'
```

### Resulting PodCliques (after webhook injection)

```yaml
apiVersion: grove.io/v1alpha1
kind: PodClique
metadata:
  name: scalable-inference-0-inference-pool-0-gpu-worker
  labels:
    grove.io/pcs-name: scalable-inference
    grove.io/pcs-replica-index: "0"
    grove.io/pcsg-name: inference-pool
    grove.io/pcsg-replica-index: "0"
    grove.io/podgang: scalable-inference-0-inference-pool-0
  annotations:
    grove.io/mnnvl-enabled: "true"
    grove.io/mnnvl-scope: "scalingGroupReplica"
spec:
  replicas: 2
  podSpec:
    resourceClaims:
      - name: mnnvl
        resourceClaimName: scalable-inference-0-inference-pool-0-mnnvl-claim
    containers:
      - name: inference
        resources:
          limits:
            nvidia.com/gpu: 4
          claims:
            - name: mnnvl
---
apiVersion: grove.io/v1alpha1
kind: PodClique
metadata:
  name: scalable-inference-0-inference-pool-1-gpu-worker
  labels:
    grove.io/pcsg-replica-index: "1"
    grove.io/podgang: scalable-inference-0-inference-pool-1
  annotations:
    grove.io/mnnvl-enabled: "true"
    grove.io/mnnvl-scope: "scalingGroupReplica"
spec:
  replicas: 2
  podSpec:
    resourceClaims:
      - name: mnnvl
        resourceClaimName: scalable-inference-0-inference-pool-1-mnnvl-claim
    containers:
      - name: inference
        resources:
          limits:
            nvidia.com/gpu: 4
          claims:
            - name: mnnvl
---
apiVersion: grove.io/v1alpha1
kind: PodClique
metadata:
  name: scalable-inference-0-inference-pool-2-gpu-worker
  labels:
    grove.io/pcsg-replica-index: "2"
    grove.io/podgang: scalable-inference-0-inference-pool-2
  annotations:
    grove.io/mnnvl-enabled: "true"
    grove.io/mnnvl-scope: "scalingGroupReplica"
spec:
  replicas: 2
  podSpec:
    resourceClaims:
      - name: mnnvl
        resourceClaimName: scalable-inference-0-inference-pool-2-mnnvl-claim
    containers:
      - name: inference
        resources:
          limits:
            nvidia.com/gpu: 4
          claims:
            - name: mnnvl
```

### Resource Topology

```
PCS: scalable-inference (replicas: 1)
│
└── Replica 0
    │
    ├── PCSG: inference-pool (replicas: 3)
    │
    ├── PCSG Replica 0
    │   ├── PodGang: scalable-inference-0-inference-pool-0
    │   │   └── owns:
    │   │       ├── ComputeDomain: scalable-inference-0-inference-pool-0-cd
    │   │       └── ResourceClaim: scalable-inference-0-inference-pool-0-mnnvl-claim
    │   └── PodClique: scalable-inference-0-inference-pool-0-gpu-worker
    │       └── 2 pods → inference-pool-0-mnnvl-claim
    │
    ├── PCSG Replica 1
    │   ├── PodGang: scalable-inference-0-inference-pool-1
    │   │   └── owns: inference-pool-1-cd, inference-pool-1-mnnvl-claim
    │   └── PodClique: scalable-inference-0-inference-pool-1-gpu-worker
    │       └── 2 pods → inference-pool-1-mnnvl-claim
    │
    └── PCSG Replica 2
        ├── PodGang: scalable-inference-0-inference-pool-2
        │   └── owns: inference-pool-2-cd, inference-pool-2-mnnvl-claim
        └── PodClique: scalable-inference-0-inference-pool-2-gpu-worker
            └── 2 pods → inference-pool-2-mnnvl-claim
```

---

## Webhook Implementation

Two mutating webhooks handle MNNVL resource management:

### 1. PodClique Mutating Webhook

**Triggers on:** PodClique CREATE

**Responsibilities:**

1. **Check for MNNVL enablement**
   - Read `grove.io/mnnvl-enabled` annotation
   - If not `"true"`, allow PodClique creation without modification

2. **Compute resource names based on scope**
   
   | Scope | ComputeDomain Name | ResourceClaim Name |
   |-------|-------------------|-------------------|
   | `replica` | `{pcs}-{pcsReplica}-cd` | `{pcs}-{pcsReplica}-mnnvl-claim` |
   | `clique` | `{pcs}-{pcsReplica}-{clique}-cd` | `{pcs}-{pcsReplica}-{clique}-mnnvl-claim` |
   | `scalingGroupReplica` | `{pcs}-{pcsReplica}-{pcsg}-{pcsgReplica}-cd` | `{pcs}-{pcsReplica}-{pcsg}-{pcsgReplica}-mnnvl-claim` |

3. **Ensure ComputeDomain exists (idempotent)**
   - Check if ComputeDomain with computed name exists
   - If not, create it with owner reference to PCS (temporary)
   - If it already exists (created by another PodClique in same replica), skip creation

4. **Ensure ResourceClaim exists (idempotent)**
   - Check if ResourceClaim with computed name exists
   - If not, create it with CEL selector referencing the ComputeDomain
   - Set owner reference to PCS (temporary)

5. **Inject ResourceClaim into PodClique.Spec.PodSpec**
   - Add entry to `spec.podSpec.resourceClaims[]` with `resourceClaimName` (not template!)
   - Add `claims` reference to each container that has GPU resource requests

**Pseudo-code:**

```go
func (w *PodCliqueMutatingWebhook) Handle(ctx context.Context, req admission.Request) admission.Response {
    pclq := decode(req)
    
    // Check if MNNVL is enabled
    if pclq.Annotations["grove.io/mnnvl-enabled"] != "true" {
        return admission.Allowed("")
    }
    
    // Compute names based on scope
    cdName, claimName := w.computeResourceNames(pclq)
    
    // Ensure ComputeDomain exists (idempotent - handles race conditions)
    if err := w.ensureComputeDomainExists(ctx, pclq, cdName); err != nil {
        return admission.Errored(http.StatusInternalServerError, err)
    }
    
    // Ensure ResourceClaim exists (idempotent)
    if err := w.ensureResourceClaimExists(ctx, pclq, claimName, cdName); err != nil {
        return admission.Errored(http.StatusInternalServerError, err)
    }
    
    // Inject into PodSpec
    pclq.Spec.PodSpec.ResourceClaims = append(pclq.Spec.PodSpec.ResourceClaims,
        corev1.PodResourceClaim{
            Name:              "mnnvl",
            ResourceClaimName: &claimName,  // Shared claim, NOT a template
        })
    
    // Add claims to GPU containers
    for i := range pclq.Spec.PodSpec.Containers {
        if hasGPURequest(&pclq.Spec.PodSpec.Containers[i]) {
            pclq.Spec.PodSpec.Containers[i].Resources.Claims = append(
                pclq.Spec.PodSpec.Containers[i].Resources.Claims,
                corev1.ResourceClaim{Name: "mnnvl"})
        }
    }
    
    return admission.PatchResponseFromRaw(req.Object.Raw, marshal(pclq))
}

func (w *PodCliqueMutatingWebhook) computeResourceNames(pclq *PodClique) (cdName, claimName string) {
    pcsName := pclq.Labels["grove.io/pcs-name"]
    pcsReplica := pclq.Labels["grove.io/pcs-replica-index"]
    scope := pclq.Annotations["grove.io/mnnvl-scope"]
    
    switch scope {
    case "replica", "":
        cdName = fmt.Sprintf("%s-%s-cd", pcsName, pcsReplica)
        claimName = fmt.Sprintf("%s-%s-mnnvl-claim", pcsName, pcsReplica)
        
    case "clique":
        cliqueName := extractCliqueName(pclq.Name, pcsName)
        cdName = fmt.Sprintf("%s-%s-%s-cd", pcsName, pcsReplica, cliqueName)
        claimName = fmt.Sprintf("%s-%s-%s-mnnvl-claim", pcsName, pcsReplica, cliqueName)
        
    case "scalingGroupReplica":
        pcsgName := pclq.Labels["grove.io/pcsg-name"]
        pcsgReplica := pclq.Labels["grove.io/pcsg-replica-index"]
        cdName = fmt.Sprintf("%s-%s-%s-%s-cd", pcsName, pcsReplica, pcsgName, pcsgReplica)
        claimName = fmt.Sprintf("%s-%s-%s-%s-mnnvl-claim", pcsName, pcsReplica, pcsgName, pcsgReplica)
    }
    
    return cdName, claimName
}

func (w *PodCliqueMutatingWebhook) ensureComputeDomainExists(ctx context.Context, pclq *PodClique, cdName string) error {
    cd := &nvidiav1alpha1.ComputeDomain{}
    err := w.client.Get(ctx, client.ObjectKey{Namespace: pclq.Namespace, Name: cdName}, cd)
    
    if err == nil {
        return nil  // Already exists
    }
    
    if !apierrors.IsNotFound(err) {
        return err
    }
    
    // Create ComputeDomain
    cd = &nvidiav1alpha1.ComputeDomain{
        ObjectMeta: metav1.ObjectMeta{
            Name:      cdName,
            Namespace: pclq.Namespace,
            Labels:    buildLabelsFromPodClique(pclq),
            OwnerReferences: []metav1.OwnerReference{
                // Temporary owner - PodGang webhook will update this
                buildPCSOwnerReference(pclq),
            },
        },
        Spec: nvidiav1alpha1.ComputeDomainSpec{
            NumNodes: 0,
        },
    }
    
    err = w.client.Create(ctx, cd)
    if apierrors.IsAlreadyExists(err) {
        return nil  // Race condition - another webhook created it first
    }
    return err
}
```

### 2. PodGang Mutating Webhook

**Triggers on:** PodGang CREATE

**Responsibilities:**

1. **Find associated MNNVL resources**
   - Query for ComputeDomains and ResourceClaims matching this PodGang's labels
   - Use labels: `grove.io/pcs-name`, `grove.io/pcs-replica-index`, `grove.io/pcsg-name`, `grove.io/pcsg-replica-index`

2. **Transfer ownership**
   - Update `ownerReferences` on ComputeDomain and ResourceClaim
   - Set this PodGang as the controller owner
   - This ensures resources are deleted when PodGang is deleted (scale-down, PCS deletion)

3. **Mark adoption complete**
   - Add annotation `grove.io/mnnvl-resources-adopted: "true"` to PodGang for observability

**Pseudo-code:**

```go
func (w *PodGangMutatingWebhook) Handle(ctx context.Context, req admission.Request) admission.Response {
    pg := decode(req)
    
    // Find MNNVL resources for this PodGang
    labelSelector := buildLabelSelector(pg)
    
    // Find and adopt ComputeDomains
    cds := &nvidiav1alpha1.ComputeDomainList{}
    w.client.List(ctx, cds, client.InNamespace(pg.Namespace), client.MatchingLabels(labelSelector))
    
    for _, cd := range cds.Items {
        w.updateOwnerReference(ctx, &cd, pg)
    }
    
    // Find and adopt ResourceClaims
    claims := &resourcev1beta1.ResourceClaimList{}
    w.client.List(ctx, claims, client.InNamespace(pg.Namespace), client.MatchingLabels(labelSelector))
    
    for _, claim := range claims.Items {
        w.updateOwnerReference(ctx, &claim, pg)
    }
    
    // Mark adoption
    if pg.Annotations == nil {
        pg.Annotations = make(map[string]string)
    }
    pg.Annotations["grove.io/mnnvl-resources-adopted"] = "true"
    
    return admission.PatchResponseFromRaw(req.Object.Raw, marshal(pg))
}

func (w *PodGangMutatingWebhook) updateOwnerReference(ctx context.Context, obj client.Object, pg *PodGang) error {
    // Replace existing owner references with PodGang as controller
    obj.SetOwnerReferences([]metav1.OwnerReference{
        {
            APIVersion:         "scheduler.grove.io/v1alpha1",
            Kind:               "PodGang",
            Name:               pg.Name,
            UID:                pg.UID,
            Controller:         pointer.Bool(true),
            BlockOwnerDeletion: pointer.Bool(true),
        },
    })
    
    return w.client.Update(ctx, obj)
}
```

---

## Resource Lifecycle

### Creation Flow

```
1. User creates PodCliqueSet with MNNVL annotations
                    │
                    ▼
2. Grove propagates annotations to PodCliques
                    │
                    ▼
3. Grove creates first PodClique for replica N
                    │
                    ▼
4. PodClique Mutating Webhook intercepts
   ├── Creates ComputeDomain (owner: PCS, temporary)
   ├── Creates ResourceClaim (owner: PCS, temporary)
   └── Injects ResourceClaim reference into PodClique.Spec.PodSpec
                    │
                    ▼
5. Grove creates additional PodCliques for replica N
                    │
                    ▼
6. PodClique Mutating Webhook intercepts
   ├── ComputeDomain already exists → skip creation
   ├── ResourceClaim already exists → skip creation
   └── Injects same ResourceClaim reference into PodClique.Spec.PodSpec
                    │
                    ▼
7. PodClique Controller creates Pods with ResourceClaims
                    │
                    ▼
8. Grove creates PodGang for replica N
                    │
                    ▼
9. PodGang Mutating Webhook intercepts
   └── Transfers ComputeDomain and ResourceClaim ownership to PodGang
                    │
                    ▼
10. Resources ready, Pods scheduled with MNNVL
```

### Cleanup Flow (Scale Down or Delete)

```
1. User scales down PCS replicas (or deletes PCS)
                    │
                    ▼
2. Grove deletes excess PodGangs
                    │
                    ▼
3. Kubernetes Garbage Collector detects:
   - ComputeDomain owned by deleted PodGang
   - ResourceClaim owned by deleted PodGang
                    │
                    ▼
4. GC automatically deletes orphaned resources
   (No webhook intervention needed!)
```

### Scale Up Flow

```
1. User increases PCS replicas (or PCSG replicas)
                    │
                    ▼
2. Grove creates new PodCliques for new replicas
                    │
                    ▼
3. PodClique Webhook creates new ComputeDomain and ResourceClaim
                    │
                    ▼
4. Grove creates new PodGang
                    │
                    ▼
5. PodGang Webhook adopts new resources
                    │
                    ▼
6. New resources ready for scheduling
```

---

## Required Grove Changes

The only changes required in the Grove operator are minimal annotation propagation:

### 1. PCS Controller PodClique Component

Propagate `grove.io/mnnvl-*` annotations from PCS to standalone PodCliques:

```go
// In buildResource()
if pclq.Annotations == nil {
    pclq.Annotations = make(map[string]string)
}

// Propagate MNNVL annotations from PCS (if not overridden at clique level)
for _, key := range []string{"grove.io/mnnvl-enabled", "grove.io/mnnvl-scope", "grove.io/mnnvl-device-class"} {
    if _, exists := pclqTemplateSpec.Annotations[key]; !exists {
        if val, exists := pcs.Annotations[key]; exists {
            pclq.Annotations[key] = val
        }
    }
}
```

### 2. PCSG Controller PodClique Component

Propagate `grove.io/mnnvl-*` annotations from PCSG to PodCliques:

```go
// In buildResource()
if pclq.Annotations == nil {
    pclq.Annotations = make(map[string]string)
}

// Propagate MNNVL annotations from PCSG config
pcsgConfig := findPCSGConfig(pcs, pcsg.Name)
for _, key := range []string{"grove.io/mnnvl-enabled", "grove.io/mnnvl-scope", "grove.io/mnnvl-device-class"} {
    if val, exists := pcsgConfig.Annotations[key]; exists {
        pclq.Annotations[key] = val
    }
}
```

**No other changes required in Grove:**
- No API schema changes
- No new controllers
- No new components

---

## RBAC Requirements

The webhook component requires the following permissions:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: mnnvl-webhook
rules:
  # ComputeDomains - create and manage
  - apiGroups: ["resource.nvidia.com"]
    resources: ["computedomains"]
    verbs: ["get", "list", "watch", "create", "update", "patch"]
  
  # ResourceClaims - create and manage
  - apiGroups: ["resource.k8s.io"]
    resources: ["resourceclaims"]
    verbs: ["get", "list", "watch", "create", "update", "patch"]
  
  # PodCliqueSets - read for owner references
  - apiGroups: ["grove.io"]
    resources: ["podcliquesets"]
    verbs: ["get", "list", "watch"]
  
  # PodGangs - read for label matching
  - apiGroups: ["scheduler.grove.io"]
    resources: ["podgangs"]
    verbs: ["get", "list", "watch"]
```

---

## Summary

| Aspect | Details |
|--------|---------|
| **Configuration** | Pure annotation-based, no API changes |
| **Webhooks** | PodClique (create resources, inject claims) + PodGang (transfer ownership) |
| **Cleanup** | Automatic via Kubernetes garbage collection |
| **Grove changes** | Minimal - only annotation propagation |
| **Scopes supported** | `replica`, `clique`, `scalingGroupReplica` |

### Naming Convention Summary

| Scope | ComputeDomain | ResourceClaim |
|-------|--------------|---------------|
| `replica` | `{pcs}-{pcsReplica}-cd` | `{pcs}-{pcsReplica}-mnnvl-claim` |
| `clique` | `{pcs}-{pcsReplica}-{clique}-cd` | `{pcs}-{pcsReplica}-{clique}-mnnvl-claim` |
| `scalingGroupReplica` | `{pcs}-{pcsReplica}-{pcsg}-{pcsgReplica}-cd` | `{pcs}-{pcsReplica}-{pcsg}-{pcsgReplica}-mnnvl-claim` |

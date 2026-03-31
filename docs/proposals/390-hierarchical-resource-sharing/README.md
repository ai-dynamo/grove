# Hierarchical Resource Sharing

<!-- toc -->
  - [Summary](#summary)
  - [Motivation](#motivation)
    - [The Need for Multiple Sharing Scopes](#the-need-for-multiple-sharing-scopes)
    - [Goals](#goals)
  - [Proposal](#proposal)
    - [User Stories](#user-stories)
      - [Story 1: Disaggregated Inference with Multi-Level GPU Sharing](#story-1-disaggregated-inference-with-multi-level-gpu-sharing)
      - [Story 2: Multi-Stage Training Pipeline with GPU Sharing](#story-2-multi-stage-training-pipeline-with-gpu-sharing)
    - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
  - [Design Details](#design-details)
    - [Common Types](#common-types)
  - [count: 8](#count-8)
- [--- PodCliqueSet using both internal and external references ---](#----podcliqueset-using-both-internal-and-external-references----)
    - [PodClique-Level Resource Sharing](#podclique-level-resource-sharing)
    - [PodCliqueScalingGroup-Level Resource Sharing](#podcliquescalinggroup-level-resource-sharing)
    - [ResourceClaim Naming Convention](#resourceclaim-naming-convention)
    - [Owner References and Garbage Collection](#owner-references-and-garbage-collection)
    - [Monitoring](#monitoring)
    - [Dependencies](#dependencies)
    - [Test Plan](#test-plan)
    - [Graduation Criteria](#graduation-criteria)
  - [Implementation History](#implementation-history)
  - [Alternatives](#alternatives)
  - [Appendix](#appendix)
<!-- /toc -->

## Summary

Grove provides a hierarchical and flexible Kubernetes API to describe inference and training workloads. It encodes
scheduling and scaling constraints at every level of a `PodCliqueSet` (PCS). A PCS can directly contain one
or more `PodClique` (PCLQ) instances and/or one or more `PodCliqueScalingGroup` (PCSG) instances, where each PCSG in
turn contains one or more PCLQ instances.

This GREP enhances the `PodCliqueSet` API to allow sharing of cluster resources (such as GPU accelerators) amongst a
group of pods at multiple levels of the Grove hierarchy by leveraging
[ResourceClaim](https://github.com/kubernetes/api/blob/ffebe2b51dedadf6a36343b495ca26060cb7a93d/resource/v1/types.go#L741)
offered via Dynamic Resource Allocation (DRA) in Kubernetes. Users declare `ResourceClaimTemplateSpec` definitions
either as named templates at the PCS level (`ResourceClaimTemplateConfig`) or reference pre-existing Kubernetes
`ResourceClaimTemplate` objects. Each `resourceSharing` entry references a template by name via a
`ResourceClaimTemplateRef` with a `Scope` enum (`AllReplicas` or `PerReplica`). External references are
indicated by setting `isExternalRef: true`. Grove creates and manages `ResourceClaim` objects directly
from the resolved specs. A
`resourceSharing` field is available at three levels of the hierarchy:

* **PodCliqueSet level** — resources shared across an entire PCS or per PCS replica,
  with an optional `filter` to target specific PodCliques and/or PodCliqueScalingGroups.
* **PodClique level** — resources shared across an entire PCLQ or per PCLQ replica.
* **PodCliqueScalingGroup level** — resources shared across an entire PCSG or per PCSG replica,
  with an optional `filter` to target specific PodCliques.

This design ensures proper isolation between replicas during scaling operations and enables composable,
multi-level resource sharing.

## Motivation

Modern ML inference and training workloads often require multiple pods to share expensive cluster resources such as GPU
accelerators to optimize resource utilization and reduce costs. Grove's hierarchical API (PCS → PCSG → PCLQ) provides
natural boundaries for defining resource sharing scopes, but currently lacks the ability to specify how resources should
be shared within these boundaries.

Kubernetes DRA provides `ResourceClaim` and `ResourceClaimTemplate` APIs that enable resource sharing, but using them
directly in Grove's pod templates presents challenges:

- **ResourceClaim in pod templates**: All pods created from the template reference the same claim, which breaks isolation
  when PodCliques are instantiated multiple times across PCSG or PCS replicas.
- **ResourceClaimTemplate in pod templates**: Each pod gets a unique ResourceClaim, preventing any sharing within the
  desired scope (PCLQ or PCSG).

Grove needs a mechanism to orchestrate resource sharing that respects its hierarchical structure — allowing resources to
be shared at the PCS, PCLQ, or PCSG level while maintaining proper isolation across different instances during scaling
operations.

### The Need for Multiple Sharing Scopes

Real-world workloads require resource sharing at different granularities within the Grove hierarchy:

- **PCS `AllReplicas`**: A resource shared across ALL pods in ALL replicas of an entire PodCliqueSet
  (e.g. a shared storage pool).
- **PCS `PerReplica`**: A resource shared across ALL pods within a single PCS replica.
- **PCSG `AllReplicas`**: A shared resource across ALL replicas of a scaling group instance
  (e.g. a shared storage or interconnect resource).
- **PCSG `PerReplica`**: An NVSwitch or interconnect resource shared across all PodCliques in a scaling group
  replica (e.g. a leader and its workers sharing a fabric).
- **PCLQ `AllReplicas`**: A set of GPUs shared across all replicas of a PodClique
  (e.g. all worker replicas in a scaling group replica share one pool of GPUs).
- **PCLQ `PerReplica`**: A resource dedicated to a single PCLQ replica.

These scopes are orthogonal and composable. A single PodClique may participate in multiple scopes simultaneously.

### Goals

- Enable users to define resource sharing primitives at all three levels of the Grove hierarchy
  (PodCliqueSet, PodClique, and PodCliqueScalingGroup) via `ResourceClaimTemplateRef` entries with
  a `Scope` enum.
- Users should be able to scope resource sharing at the desired granularity, e.g. share resources
  between all pods of a PodClique (`AllReplicas`), per PCLQ replica (`PerReplica`), between
  a subset of PCLQs within a PCSG replica (`PerReplica` with `filter`), across all PCSG replicas
  (`AllReplicas`), or across an entire PCS or per PCS replica — with optional `filter`
  to target specific children at any level.
- Enable users to declare `ResourceClaimTemplateSpec` definitions as named templates at the PCS level
  (internal references) or reference pre-existing Kubernetes `ResourceClaimTemplate` objects (external
  references), avoiding spec duplication across the hierarchy.

## Proposal

### User Stories

#### Story 1: Disaggregated Inference with Multi-Level GPU Sharing

A platform team deploys a disaggregated inference workload with a prefill leader (PCA, 3 replicas) and prefill workers
(PCB, 2 replicas) grouped in a scaling group. The workload requires two levels of resource sharing:

1. **PCSG `PerReplica`**: An NVSwitch fabric claim shared across all pods in the scaling group replica
   (leader + workers).
2. **PCLQ `AllReplicas`**: A GPU pool claim shared across all replicas of a PodClique — e.g. all 3
   PCA replicas share one set of GPUs, all 2 PCB replicas share another.

_Challenge_: Without hierarchical sharing, users must either reference a single `ResourceClaim` (breaking isolation
across PCSG replicas) or use `ResourceClaimTemplate` in the PodSpec (creating per-pod claims, preventing sharing).

_Solution_: Grove orchestrates resource sharing via `ResourceClaimTemplateRef` entries with a scope enum:

- `resourceSharing` at the PCSG level with `scope: PerReplica` creates one ResourceClaim per PCSG replica,
  injected into all PodCliques in that replica.
- `resourceSharing` at the PCLQ level with `scope: AllReplicas` creates one ResourceClaim per PCLQ,
  shared across all replicas.

**Concrete example** of the ResourceClaim distribution for a PCS named `disagg` with 2 replicas,
demonstrating all three levels with different scopes:

```
PCS "disagg" (replicas: 2):
  resourceClaimTemplates:
    - name: storage1,      template: {shared storage device A}
    - name: storage2,      template: {shared storage device B}
    - name: h100,          template: {4 GPUs, nvidia.com}
    - name: b200,          template: {2 GPUs, nvidia.com}
    - name: nvswitch,      template: {1 NVSwitch fabric, nvswitch.nvidia.com}
  resourceSharing:
    - {name: storage1, scope: AllReplicas}                          # PCS AllReplicas
    - {name: storage2, scope: PerReplica}                           # PCS PerReplica
  cliques:
    - pca: replicas=3,
           resourceSharing=[{name: h100, scope: AllReplicas}]        # PCLQ AllReplicas
    - pcb: replicas=2,
           resourceSharing=[{name: b200, scope: AllReplicas}]        # PCLQ AllReplicas
  scalingGroups:
    - sgx: cliqueNames=[pca, pcb], replicas=2,
           resourceSharing=[{name: nvswitch, scope: PerReplica}]   # PCSG PerReplica
```

Grove creates the following ResourceClaim objects:

```
PCS AllReplicas (1 for the entire PCS):
  disagg-all-storage1                 → shared by ALL pods across ALL PCS replicas

PCS PerReplica (1 per PCS replica):
  disagg-0-storage2                   → shared by ALL pods in PCS replica 0
  disagg-1-storage2                   → shared by ALL pods in PCS replica 1

PCSG PerReplica (1 per PCSG replica, per PCS replica):
  disagg-0-sgx-0-nvswitch             → shared by ALL pods in PCSG replica 0, PCS replica 0
  disagg-0-sgx-1-nvswitch             → shared by ALL pods in PCSG replica 1, PCS replica 0
  disagg-1-sgx-0-nvswitch             → shared by ALL pods in PCSG replica 0, PCS replica 1
  disagg-1-sgx-1-nvswitch             → shared by ALL pods in PCSG replica 1, PCS replica 1

PCLQ AllReplicas (1 per PCLQ instance):
  disagg-0-sgx-0-pca-all-h100        → shared by all 3 pca pods in PCSG rep 0, PCS rep 0
  disagg-0-sgx-1-pca-all-h100        → shared by all 3 pca pods in PCSG rep 1, PCS rep 0
  disagg-0-sgx-0-pcb-all-b200        → shared by all 2 pcb pods in PCSG rep 0, PCS rep 0
  disagg-0-sgx-1-pcb-all-b200        → shared by all 2 pcb pods in PCSG rep 1, PCS rep 0
  disagg-1-sgx-0-pca-all-h100        → (same pattern for PCS replica 1)
  disagg-1-sgx-1-pca-all-h100
  disagg-1-sgx-0-pcb-all-b200
  disagg-1-sgx-1-pcb-all-b200
```

The resulting ResourceClaim assignment per pod (showing PCS replica 0 only for brevity — PCS replica 1 follows the same pattern):

| Pod | PCS AllReplicas | PCS PerReplica | PCSG PerReplica | PCLQ AllReplicas |
|---|---|---|---|---|
| disagg-0-sgx-0-pca-0 | disagg-all-storage1 | disagg-0-storage2 | disagg-0-sgx-0-nvswitch | disagg-0-sgx-0-pca-all-h100 |
| disagg-0-sgx-0-pca-1 | disagg-all-storage1 | disagg-0-storage2 | disagg-0-sgx-0-nvswitch | disagg-0-sgx-0-pca-all-h100 |
| disagg-0-sgx-0-pca-2 | disagg-all-storage1 | disagg-0-storage2 | disagg-0-sgx-0-nvswitch | disagg-0-sgx-0-pca-all-h100 |
| disagg-0-sgx-0-pcb-0 | disagg-all-storage1 | disagg-0-storage2 | disagg-0-sgx-0-nvswitch | disagg-0-sgx-0-pcb-all-b200 |
| disagg-0-sgx-0-pcb-1 | disagg-all-storage1 | disagg-0-storage2 | disagg-0-sgx-0-nvswitch | disagg-0-sgx-0-pcb-all-b200 |
| disagg-0-sgx-1-pca-0 | disagg-all-storage1 | disagg-0-storage2 | disagg-0-sgx-1-nvswitch | disagg-0-sgx-1-pca-all-h100 |
| disagg-0-sgx-1-pca-1 | disagg-all-storage1 | disagg-0-storage2 | disagg-0-sgx-1-nvswitch | disagg-0-sgx-1-pca-all-h100 |
| disagg-0-sgx-1-pca-2 | disagg-all-storage1 | disagg-0-storage2 | disagg-0-sgx-1-nvswitch | disagg-0-sgx-1-pca-all-h100 |
| disagg-0-sgx-1-pcb-0 | disagg-all-storage1 | disagg-0-storage2 | disagg-0-sgx-1-nvswitch | disagg-0-sgx-1-pcb-all-b200 |
| disagg-0-sgx-1-pcb-1 | disagg-all-storage1 | disagg-0-storage2 | disagg-0-sgx-1-nvswitch | disagg-0-sgx-1-pcb-all-b200 |

In this example:
- `disagg-all-storage1` is the PCS `AllReplicas` claim: one for the entire PCS, shared by all 20 pods
- `disagg-{0,1}-storage2` are PCS `PerReplica` claims: one per PCS replica, shared by all 10 pods in that replica
- `disagg-{0,1}-sgx-{0,1}-nvswitch` are PCSG `PerReplica` claims: one per PCSG replica, shared by all 5 pods (pca + pcb) in that replica
- `disagg-{0,1}-sgx-{0,1}-pca-all-h100` are PCLQ `AllReplicas` claims: one per pca instance, shared by all 3 pca pods
- `disagg-{0,1}-sgx-{0,1}-pcb-all-b200` are PCLQ `AllReplicas` claims: one per pcb instance, shared by all 2 pcb pods
- Total ResourceClaims: 1 (PCS AllReplicas) + 2 (PCS PerReplica) + 4 (PCSG PerReplica) + 8 (PCLQ AllReplicas) = 15

#### Story 2: Multi-Stage Training Pipeline with GPU Sharing

Multi-stage ML pipelines with separate preprocessing and training components are a common pattern in production ML systems. Frameworks like [Kubeflow Pipelines](https://www.kubeflow.org/docs/components/pipelines/v1/introduction/), [TensorFlow Extended (TFX)](https://www.tensorflow.org/tfx), and [Ray Train](https://docs.ray.io/en/latest/train/train.html) enable users to define pipelines where data preprocessing (ETL, feature engineering, augmentation) runs as separate containers/pods from the training workload.

In such a distributed training pipeline, data preprocessing pods load and transform data into GPU memory, while model training pods consume this preprocessed data directly from GPU memory without expensive CPU-GPU transfers. Libraries like [NVIDIA DALI](https://docs.nvidia.com/deeplearning/dali/user-guide/docs/index.html) provide GPU-accelerated data preprocessing capabilities that make this pattern efficient. The preprocessing and training pods are modeled as separate PCLQs within a PCSG, where each PCSG replica represents a different training experiment.

_Challenge_: Each experiment (PCSG instance) needs its own isolated set of GPUs, but within an experiment, both preprocessing and training pods should share the same GPU devices for efficient data transfer and memory utilization. Standard GPU allocation creates exclusive claims per pod, preventing this sharing pattern. When these stages need to share GPUs for zero-copy data transfer and to avoid CPU-GPU memory copying overhead, DRA's [shareable ResourceClaims](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/#shareable-resources) become essential.

_Solution_: By leveraging GPU sharing technologies like [NVIDIA Multi-Process Service (MPS)](https://docs.nvidia.com/deploy/mps/index.html) for efficient GPU sharing or [CUDA IPC (Inter-Process Communication)](https://docs.nvidia.com/cuda/cuda-c-programming-guide/index.html#interprocess-communication) for sharing GPU memory between processes, along with techniques like [GPU Direct Storage](https://developer.nvidia.com/gpudirect-storage) for direct data paths, Grove enables this pattern through `resourceSharing` at the PCSG level. By specifying a `ResourceClaimTemplateRef` with `scope: PerReplica` and a `filter` referencing both the preprocessing and training PCLQs, Grove creates a ResourceClaim per PCSG replica that is shared across the specified PCLQs. This enables both pod types to access the same GPU devices within each experiment while maintaining isolation across different experiments.

### Limitations/Risks & Mitigations

<!-- 
What are the current set of limitations or risks of this proposal? Think broadly by considering the impact of the changes proposed on kubernetes ecosystem. Optionally mention ways to mitigate these.
-->

## Design Details

### Common Types

```go
// ResourceSharingScope defines the sharing scope.
// +kubebuilder:validation:Enum=AllReplicas;PerReplica
type ResourceSharingScope string

const (
	// ResourceSharingScopeAllReplicas creates one ResourceClaim for the owning resource
	// (PCS, PCLQ, or PCSG), shared across all replicas and pods.
	ResourceSharingScopeAllReplicas ResourceSharingScope = "AllReplicas"
	// ResourceSharingScopePerReplica creates one ResourceClaim per replica, shared
	// across all pods within that replica.
	ResourceSharingScopePerReplica ResourceSharingScope = "PerReplica"
)

// ResourceClaimTemplateConfig defines a named ResourceClaimTemplateSpec that can be
// referenced by ResourceClaimTemplateRef entries in resourceSharing fields.
type ResourceClaimTemplateConfig struct {
	// Name is a unique identifier for this template within the PodCliqueSet.
	Name string `json:"name"`
	// Template is the ResourceClaimTemplate spec.
	Template resourcev1.ResourceClaimTemplateSpec `json:"template"`
}

// ResourceClaimTemplateRef references a ResourceClaimTemplateSpec and defines the
// sharing scope for the resulting ResourceClaim(s).
// A given template name must appear at most once per resourceSharing list.
type ResourceClaimTemplateRef struct {
	// Name of the referenced template. When IsExternalRef is false (default), this must
	// match a name in PodCliqueSetTemplateSpec.ResourceClaimTemplates. When IsExternalRef
	// is true, this is the name of a Kubernetes ResourceClaimTemplate object.
	Name string `json:"name"`
	// IsExternalRef indicates that Name refers to an externally created Kubernetes
	// ResourceClaimTemplate object rather than a PCS-level ResourceClaimTemplateConfig.
	// +optional
	IsExternalRef bool `json:"isExternalRef,omitempty"`
	// Namespace of the referenced ResourceClaimTemplate. Only used when IsExternalRef
	// is true. If empty, defaults to the namespace of the PodCliqueSet.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// Scope determines the sharing granularity for the ResourceClaims created from
	// this template.
	Scope ResourceSharingScope `json:"scope"`
	// Filter limits which children receive the ResourceClaims.
	// If absent, all children receive them (broadcast).
	// +optional
	Filter *ResourceSharingFilter `json:"filter,omitempty"`
}

// ResourceSharingFilterMode defines whether the filter includes or excludes the listed children.
// +kubebuilder:validation:Enum=Include;Exclude
type ResourceSharingFilterMode string

const (
	ResourceSharingFilterModeInclude ResourceSharingFilterMode = "Include"
	ResourceSharingFilterModeExclude ResourceSharingFilterMode = "Exclude"
)

// ResourceSharingFilter controls which children receive the ResourceClaims.
type ResourceSharingFilter struct {
	// Mode specifies whether the listed names are included or excluded.
	// Defaults to Include if not specified.
	// +optional
	Mode ResourceSharingFilterMode `json:"mode,omitempty"`
	// CliqueNames lists PodClique template names to include or exclude.
	// +optional
	CliqueNames []string `json:"cliqueNames,omitempty"`
	// GroupNames lists PodCliqueScalingGroup config names to include or exclude.
	// Only valid at PCS level.
	// +optional
	GroupNames []string `json:"groupNames,omitempty"`
}
```

**Scope Naming — Open Discussion**

The `Scope` enum determines how many ResourceClaim instances are created per resource:
- **Scope A**: 1 RC per **instance** of the resource, shared across all replicas within that instance
- **Scope B**: 1 RC per **replica** of the resource

Note that a resource (PCLQ, PCSG) can have multiple instances (e.g., PCLQ `worker` is instantiated once
per PCSG replica). Scope A creates one RC per instance — not one RC globally. The naming must convey
this "per instance, shared across replicas" semantic clearly.

Candidate naming options considered:

| Option | Scope A (1 RC per instance) | Scope B (1 RC per replica) | Notes |
|---|---|---|---|
| A | `Shared` | `PerReplica` | Intuitive but may suggest "1 RC globally" rather than "1 per instance" |
| B | `PerInstance` | `PerReplica` | Technically precise; reviewer concern: `PerInstance` sounds similar to `PerReplica` without context |
| **C (decided)** | **`AllReplicas`** | **`PerReplica`** | "Covers all replicas" vs "per replica"; short and clear |
| D | `SharedAcrossReplicas` or `SharedByReplicas` | `ExclusivePerReplica` | Most explicit; verbose but unambiguous |

The decided naming is `AllReplicas` / `PerReplica` (Option C).

---

#### ResourceClaimTemplate Referencing

Real-world `ResourceClaimTemplateSpec` definitions can be verbose (GPU device requests, sharing config,
driver parameters, etc.). Without a referencing mechanism, the same spec would be duplicated in every
`resourceSharing` entry that needs it. To address this, the API supports two sources for claim templates:

1. **Internal (PCS-level named templates)**: Declare specs once in
   `PodCliqueSetTemplateSpec.ResourceClaimTemplates` and reference them by name. This deduplicates specs
   within a single PCS. Based on these specs, the ResourceClaim resources will be created and managed
   by Grove.
2. **External (Kubernetes ResourceClaimTemplate objects)**: Reference a pre-existing `ResourceClaimTemplate`
   object by namespace/name. This enables cross-PCS and cross-namespace reuse, and allows platform teams
   to manage templates centrally.

There is no inline spec at the usage site — all specs are either declared at the PCS level or exist as
external Kubernetes objects. This forces a single source of truth and prevents inconsistent copies.

**Validation rules:**
- `isExternalRef: false` (default, can be omitted) — `name` must match a `resourceClaimTemplates[].name`
  in the PCS; `namespace` must be empty.
- `isExternalRef: true` — `name` must reference an existing `ResourceClaimTemplate` K8s object; if
  `namespace` is empty, defaults to the PCS namespace.

**Why no intermediate ResourceClaimTemplate objects are created:** Kubernetes' built-in RCT-to-RC
auto-creation (`resourceClaimTemplateName` in the pod spec) creates a unique ResourceClaim per pod, which
is the opposite of sharing. For shared claims, Grove pre-creates `ResourceClaim` objects and references them
via `resourceClaimName` in the pod spec.

**Full example mixing internal and external references:**

```yaml
# --- External ResourceClaimTemplate (created by platform team) ---
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: gb200-gpu-pool
  namespace: gpu-templates
spec:
  spec:
    devices:
      requests:
        - name: gpu
          deviceClassName: gpu.nvidia.com
          count: 8
---
# --- PodCliqueSet using both internal and external references ---
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: disagg
  namespace: default
spec:
  replicas: 1
  template:
    resourceClaimTemplates:
      - name: nvswitch-fabric
        template:
          spec:
            devices:
              requests:
                - name: nvswitch
                  deviceClassName: nvswitch.nvidia.com
                  count: 1
    cliques:
      - name: prefill-wkr
        resourceSharing:
          - name: gb200-gpu-pool
            isExternalRef: true
            namespace: gpu-templates
            scope: AllReplicas
        spec:
          roleName: prefill
          replicas: 3
          podSpec:
            containers:
              - name: prefill
                image: nvidia/cuda:12.0-runtime
            restartPolicy: Always
      - name: decode-wkr
        resourceSharing:
          - name: gb200-gpu-pool
            isExternalRef: true
            namespace: gpu-templates
            scope: AllReplicas
        spec:
          roleName: decode
          replicas: 2
          podSpec:
            containers:
              - name: decode
                image: nvidia/cuda:12.0-runtime
            restartPolicy: Always
    podCliqueScalingGroups:
      - name: model-instance
        replicas: 2
        cliqueNames: [prefill-wkr, decode-wkr]
        resourceSharing:
          - name: nvswitch-fabric
            scope: PerReplica
            filter:
              mode: Include
              cliqueNames: [prefill-wkr, decode-wkr]
```

In this example:
- The `gb200-gpu-pool` template is managed externally by a platform team in the `gpu-templates` namespace
- The `nvswitch-fabric` template is declared internally at PCS level
- Both `prefill-wkr` and `decode-wkr` reference the same external GPU template (no spec duplication)
- The PCSG references the internal NVSwitch template with `PerReplica` scope

See [ResourceClaim Naming Convention](#resourceclaim-naming-convention) for the deterministic naming scheme and
[Owner References and Garbage Collection](#owner-references-and-garbage-collection) for lifecycle semantics.

### PodCliqueSet-Level Resource Sharing

**API**

```go
type PodCliqueSetTemplateSpec struct {
	// Cliques is a slice of cliques that make up the PodCliqueSet.
	Cliques []*PodCliqueTemplateSpec `json:"cliques"`
	...
	// ResourceClaimTemplates declares named ResourceClaimTemplateSpecs that can be
	// referenced by name from resourceSharing fields at any level.
	// +optional
	ResourceClaimTemplates []ResourceClaimTemplateConfig `json:"resourceClaimTemplates,omitempty"`
	// ResourceSharing defines shared ResourceClaims at the PCS level. Each entry
	// references a template (internal or external) and specifies a Scope:
	//   - AllReplicas: one RC for the entire PCS, shared across ALL pods in ALL replicas
	//   - PerReplica: one RC per PCS replica, shared across ALL pods in that replica
	// Filter limits which children receive the claims (empty = all).
	// At PCS level, Filter may reference PodClique template names and/or
	// PodCliqueScalingGroup config names.
	// +optional
	ResourceSharing []ResourceClaimTemplateRef `json:"resourceSharing,omitempty"`
	...
}
```

Two new fields are added to `PodCliqueSetTemplateSpec`:

- `ResourceClaimTemplates`: Declares named `ResourceClaimTemplateSpec` definitions that can be referenced
  by name from any `resourceSharing` field in the hierarchy. This is the single place to define internal
  templates, avoiding spec duplication.
- `ResourceSharing`: References templates (internal or external) with a scope and optional `filter`.
  The PCS controller creates the ResourceClaim objects and all child controllers (PCSG and standalone PCLQ)
  inject the PCS-level claim references into pod specs, respecting the `filter`.

Scope semantics:
- `AllReplicas`: One RC for the entire PCS — shared by every matching pod across all PCS replicas, all PCSGs, and all PCLQs.
- `PerReplica`: One RC per PCS replica — shared by every matching pod within that PCS replica.

Filtering semantics:
- `filter` absent → broadcast to all PodCliques (default).
- `filter` specified → only PodCliques whose template name OR whose parent PCSG config name
  matches the filter receive the claims.

**Example (broadcast to all):**

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: disagg
  namespace: default
spec:
  replicas: 2
  template:
    resourceClaimTemplates:
      - name: shared-storage
        template:
          spec:
            devices:
              requests:
                - name: shared-storage
                  deviceClassName: storage.example.com
                  count: 1
      - name: interconnect
        template:
          spec:
            devices:
              requests:
                - name: interconnect
                  deviceClassName: nvswitch.nvidia.com
                  count: 1
    resourceSharing:
      - name: shared-storage
        scope: AllReplicas
      - name: interconnect
        scope: PerReplica
    cliques:
      - name: worker
        spec:
          roleName: worker
          replicas: 4
          podSpec:
            containers:
              - name: worker
                image: nvidia/cuda:12.0-runtime
            restartPolicy: Always
```

In this example:
- Two templates are declared once at PCS level (`shared-storage`, `interconnect`)
- No `filter` → broadcast to all PodCliques
- `AllReplicas` creates 1 RC (`disagg-all-shared-storage`) shared by ALL 8 pods across both PCS replicas
- `PerReplica` creates 2 RCs (`disagg-0-interconnect`, `disagg-1-interconnect`), one per PCS replica,
  each shared by the 4 worker pods in that replica

**Example (targeted with filtering):**

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: ml-platform
  namespace: default
spec:
  replicas: 1
  template:
    resourceClaimTemplates:
      - name: shared-storage
        template:
          spec:
            devices:
              requests:
                - name: storage
                  deviceClassName: storage.example.com
                  count: 1
      - name: gpu-interconnect
        template:
          spec:
            devices:
              requests:
                - name: interconnect
                  deviceClassName: nvswitch.nvidia.com
                  count: 1
    resourceSharing:
      - name: shared-storage
        scope: AllReplicas
        # Broadcast to all (no filter) — every pod gets this storage claim
      - name: gpu-interconnect
        scope: PerReplica
        # Only pods in the training group receive the interconnect
        filter:
          mode: Include
          groupNames: [training-group]
    cliques:
      - name: preprocessor
        spec:
          roleName: preprocessor
          replicas: 2
          podSpec:
            containers:
              - name: preprocessor
                image: nvidia/cuda:12.0-runtime
            restartPolicy: Always
      - name: trainer
        spec:
          roleName: trainer
          replicas: 4
          podSpec:
            containers:
              - name: trainer
                image: nvidia/cuda:12.0-runtime
            restartPolicy: Always
      - name: monitor
        spec:
          roleName: monitor
          replicas: 1
          podSpec:
            containers:
              - name: monitor
                image: python:3.11
            restartPolicy: Always
    podCliqueScalingGroups:
      - name: training-group
        replicas: 2
        cliqueNames: [preprocessor, trainer]
```

In this example:
- `shared-storage` with `AllReplicas` scope and no `filter` → broadcast to all pods (preprocessor, trainer, and standalone monitor)
- `gpu-interconnect` with `PerReplica` scope and `filter: {mode: Include, groupNames: [training-group]}` → only pods within `training-group` receive the interconnect claim. The standalone `monitor` PCLQ does not get it
- This avoids giving GPU interconnect access to the monitor pod that doesn't need it

### PodClique-Level Resource Sharing

**API**

```go
type PodCliqueTemplateSpec struct {
	// Name must be unique within a PodCliqueSet and is used to denote a role.
	Name string `json:"name"`
	...
	// ResourceSharing defines shared ResourceClaims for this PodClique. Each entry
	// references a template (internal or external) and specifies a Scope:
	//   - AllReplicas: one RC per PCLQ, shared by all replica pods
	//   - PerReplica: one RC per PCLQ replica, shared by all pods within that replica
	// NOTE: This is not the same as adding ResourceClaimTemplate inside the
	// Spec.PodSpec.ResourceClaims[x].ResourceClaimTemplateName in the PodClique since that will
	// create a unique ResourceClaim for each pod in the PodClique.
	// +optional
	ResourceSharing []ResourceClaimTemplateRef `json:"resourceSharing,omitempty"`
	// Specification of the desired behavior of a PodClique.
	Spec PodCliqueSpec `json:"spec"`
}
```

To enable resource sharing among `Pod`s within a `PodClique`, a new field `ResourceSharing` is added
to `PodCliqueTemplateSpec`. Each entry references a template by name and specifies a scope.

- `AllReplicas`: One RC per PCLQ — shared by all replica pods in that PCLQ.
- `PerReplica`: One RC per PCLQ replica — shared by all pods within that replica.

The parent controller (PCS or PCSG) processes the entries, creates the ResourceClaims, and injects the claim
references into the PCLQ's `PodSpec`.

**Example:**

The following example shows how to use `resourceSharing` with `AllReplicas` scope to share GPUs among all
pods within a single PodClique:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: shared-gpu-example
  namespace: default
spec:
  replicas: 2
  template:
    resourceClaimTemplates:
      - name: gpu-pool
        template:
          spec:
            devices:
              requests:
                - name: gpu
                  deviceClassName: gpu.nvidia.com
                  count: 2
              config:
                - opaque:
                    driver: gpu.nvidia.com
                    parameters:
                      apiVersion: gpu.nvidia.com/v1alpha1
                      kind: GpuClaimParameters
                      sharing:
                        strategy: TimeSlicing
                        replicas: 4
    cliques:
      - name: inference
        resourceSharing:
          - name: gpu-pool
            scope: AllReplicas
        spec:
          roleName: inference
          replicas: 4
          podSpec:
            containers:
              - name: inference
                image: nvidia/cuda:12.0-runtime
                command: ["/bin/sh", "-c"]
                args:
                  - |
                    echo "Pod: $POD_NAME - Using shared GPU"
                    sleep infinity
                resources:
                  requests:
                    cpu: "1"
                    memory: "2Gi"
            restartPolicy: Always
```

In this example:
- The `gpu-pool` template is declared once at PCS level and referenced by name
- The `AllReplicas` scope creates one ResourceClaim per PodClique
- All 4 pods within each PodClique share the same 2 GPUs (with time-slicing)
- The 2 PCS replicas maintain isolation (different ResourceClaims, different GPUs)

### PodCliqueScalingGroup-Level Resource Sharing

**API**

```go
type PodCliqueScalingGroupConfig struct {
	// Name is the name of the PodCliqueScalingGroupConfig. This should be unique within the PodCliqueSet.
	Name string `json:"name"`
	...
	// ResourceSharing defines shared ResourceClaims at the PCSG level. Each entry
	// references a template (internal or external) and specifies a Scope:
	//   - AllReplicas: one RC for the entire PCSG, shared across all replicas
	//   - PerReplica: one RC per PCSG replica, shared across all PCLQs in that replica
	// Filter limits which PodCliques in the group receive the claims (empty = all).
	// +optional
	ResourceSharing []ResourceClaimTemplateRef `json:"resourceSharing,omitempty"`
}
```

**Example:**

The following example demonstrates sharing resources across multiple PodCliques within a PodCliqueScalingGroup,
using `PerReplica` scope so each PCSG replica gets its own isolated ResourceClaim. The `filter`
field limits which PCLQs in the group receive the claim. Note that the `coordinator` PCLQ is part of
the scaling group but is excluded from the `filter` — it does not receive the shared GPU ResourceClaim:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: training-pipeline
  namespace: default
spec:
  replicas: 1
  template:
    resourceClaimTemplates:
      - name: gpu-mps-pool
        template:
          spec:
            devices:
              requests:
                - name: gpu
                  deviceClassName: gpu.nvidia.com
                  count: 4
              config:
                - opaque:
                    driver: gpu.nvidia.com
                    parameters:
                      apiVersion: gpu.nvidia.com/v1alpha1
                      kind: GpuClaimParameters
                      sharing:
                        strategy: MPS
                        maxClients: 8
    cliques:
      - name: data-preprocessor
        spec:
          roleName: preprocessor
          replicas: 2
          podSpec:
            containers:
              - name: preprocessor
                image: nvidia/cuda:12.0-runtime
                command: ["/bin/sh", "-c"]
                args:
                  - |
                    echo "Preprocessor pod: $POD_NAME"
                    echo "Loading data into GPU memory..."
                    sleep infinity
                resources:
                  requests:
                    cpu: "2"
                    memory: "4Gi"
            restartPolicy: Always
      - name: model-trainer
        spec:
          roleName: trainer
          replicas: 3
          podSpec:
            containers:
              - name: trainer
                image: nvidia/cuda:12.0-runtime
                command: ["/bin/sh", "-c"]
                args:
                  - |
                    echo "Training pod: $POD_NAME"
                    echo "Training model using preprocessed data from GPU memory..."
                    sleep infinity
                resources:
                  requests:
                    cpu: "4"
                    memory: "8Gi"
            restartPolicy: Always
      - name: coordinator
        spec:
          roleName: coordinator
          replicas: 1
          podSpec:
            containers:
              - name: coordinator
                image: python:3.11
                command: ["/bin/sh", "-c"]
                args:
                  - |
                    echo "Coordinator pod: $POD_NAME"
                    echo "Orchestrating training experiment..."
                    sleep infinity
                resources:
                  requests:
                    cpu: "1"
                    memory: "1Gi"
            restartPolicy: Always
    podCliqueScalingGroups:
      - name: training-experiment
        replicas: 3
        cliqueNames:
          - data-preprocessor
          - model-trainer
          - coordinator
        resourceSharing:
          - name: gpu-mps-pool
            scope: PerReplica
            filter:
              mode: Include
              cliqueNames:
                - data-preprocessor
                - model-trainer
```

In this example:
- The `gpu-mps-pool` template is declared once at PCS level (4 GPUs with NVIDIA MPS)
- The `PerReplica` scope creates one ResourceClaim per PCSG replica
- 3 PCSG replicas create 3 independent training experiments
- Within each experiment (PCSG replica):
  - 2 preprocessing pods + 3 training pods = 5 total pods share the same 4 GPUs
  - All pods can access the same GPU memory space
  - The coordinator pod does NOT receive the GPU ResourceClaim (it is excluded by the `filter`)
- Each of the 3 experiments maintains isolation (different ResourceClaims, different GPU sets)

### ResourceClaim Naming Convention

Each RC name is derived from the owning resource's Kubernetes name, a scope segment, and the
referenced template name (`rctName`). For `PerReplica` scope the segment is the replica index
(e.g. `0`, `1`). For `AllReplicas` scope the literal keyword `all` takes the place of the replica
index, ensuring that an AllReplicas RC name can never collide with a PerReplica RC name.

| Level + Scope | RC Name Format |
|---|---|
| PCS `AllReplicas` | `<pcsName>-all-<rctName>` |
| PCS `PerReplica` | `<pcsName>-<pcsReplicaIndex>-<rctName>` |
| PCLQ `AllReplicas` | `<pclqName>-all-<rctName>` |
| PCLQ `PerReplica` | `<pclqName>-<replicaIndex>-<rctName>` |
| PCSG `AllReplicas` | `<pcsgName>-all-<rctName>` |
| PCSG `PerReplica` | `<pcsgName>-<pcsgReplicaIndex>-<rctName>` |

The `rctName` is the name of the referenced `ResourceClaimTemplateConfig` or external `ResourceClaimTemplate`.

**Concrete example** — PCS `disagg` (replica 0), PCSG `model-instance` (replicas: 2), cliques:
`prefill-wkr` (replicas: 3, PCLQ AllReplicas rctName=gpu-pool), `decode-wkr` (replicas: 2, PCLQ AllReplicas
rctName=gpu-pool), PCS AllReplicas rctName=storage1, PCSG PerReplica rctName=nvswitch:

```
PCS AllReplicas ResourceClaim:
  disagg-all-storage1                                     → shared by ALL pods in the entire PCS

PCS PerReplica ResourceClaims:
  (none in this example — would be disagg-<pcsReplicaIndex>-<rctName> if configured)

PCSG PerReplica ResourceClaims:
  disagg-0-model-instance-0-nvswitch                      → shared by ALL pods in PCSG replica 0
  disagg-0-model-instance-1-nvswitch                      → shared by ALL pods in PCSG replica 1

PCLQ AllReplicas ResourceClaims:
  disagg-0-model-instance-0-prefill-wkr-all-gpu-pool      → shared by all prefill-wkr pods in PCSG replica 0
  disagg-0-model-instance-1-prefill-wkr-all-gpu-pool      → shared by all prefill-wkr pods in PCSG replica 1
  disagg-0-model-instance-0-decode-wkr-all-gpu-pool       → shared by all decode-wkr pods in PCSG replica 0
  disagg-0-model-instance-1-decode-wkr-all-gpu-pool       → shared by all decode-wkr pods in PCSG replica 1
```

**For standalone PodCliques** (not in a PCSG), the PCLQ resource name is `<pcs>-<pcsIndex>-<pclqTemplate>`,
so the pattern is the same:

```
PCLQ AllReplicas:
  my-svc-0-frontend-all-gpu-pool    → shared by all frontend pods
```

### Owner References and Garbage Collection

ResourceClaim ownership determines garbage collection behavior. All RCs are owned by the resource that
defines the broadest scope they serve. On scale-down, explicit cleanup is performed for PerReplica RCs
whose parent still exists.

| Level + Scope | Owner | Cleanup on Scale-Down |
|---|---|---|
| PCS `AllReplicas` | PCS object | GC'd when PCS is deleted |
| PCS `PerReplica` | PCS object | Explicit cleanup when PCS replicas are scaled down |
| PCSG `AllReplicas` | PCSG object | GC'd when PCSG is deleted |
| PCSG `PerReplica` | PCSG object | Explicit cleanup when PCSG replicas are scaled down |
| PCLQ `AllReplicas` (in PCSG) | PCSG object | Explicit cleanup when PCSG replicas are scaled down |
| PCLQ `AllReplicas` (standalone) | PCS object | Explicit cleanup when PCS replicas are scaled down |
| PCLQ `PerReplica` (in PCSG) | PCSG object | Explicit cleanup when PCLQ replicas are scaled down |
| PCLQ `PerReplica` (standalone) | PCS object | Explicit cleanup when PCLQ replicas are scaled down |

**Design rationale**: Owning RCs at the PCSG/PCS level (rather than the PCLQ level) avoids depending on
the controller-runtime cache to reflect freshly created PCLQ objects during the same reconcile. It also
ensures that PCLQ AllReplicas RCs survive PCLQ rolling updates — the replacement PCLQ reuses the existing
RC rather than the old RC being garbage collected and a new one created.

### Monitoring

<!--
This section contains details of events, metrics, status conditions and other status fields that will aid in determining health of the feature, or help measure any service level objectives that might be optionally defined.
-->

### Dependencies

Dynamic Resource Allocation (DRA) is a prerequisite for this GREP since it relies on the ResourceClaim API
to enable resource sharing. DRA graduated to *BETA* in *v1.32* and has been promoted to *GA* since Kubernetes *v1.34*. If you are using a Kubernetes version prior to v1.34, you will need to enable the `DynamicResourceAllocation` feature gate to use this feature. For Kubernetes
v1.34 and above, DRA is enabled by default, and you can use this feature without any additional configuration.

### Test Plan

<!--
For the functionality an epic (issue) should be created. Along with a sub-issue for the GREP, there should be a dedicated issue created for integration and e2e tests. This issue should have details of all scenarios that needs to be tested. Provide a link to issue(s) in this section.
-->

### Graduation Criteria

## Implementation History

## Alternatives

## Appendix

In case the readers are not familiar with DRA, the following links will help them get started:
* [Kubernetes DRA Official Documentation](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
* [Dynamic Resource Allocation (DRA) KEP](https://github.com/kubernetes/enhancements/tree/master/keps/sig-node/4381-dra-structured-parameters)

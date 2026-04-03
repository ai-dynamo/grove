# Hierarchical Resource Sharing

<!-- toc -->
  - [Summary](#summary)
  - [Motivation](#motivation)
    - [The Need for Multiple Sharing Scopes](#the-need-for-multiple-sharing-scopes)
    - [Goals](#goals)
  - [Proposal](#proposal)
    - [User Stories](#user-stories)
      - [Story 1: Disaggregated Inference with Multi-Level Resource Sharing](#story-1-disaggregated-inference-with-multi-level-resource-sharing)
      - [Story 2: Multi-Stage Training Pipeline with GPU Sharing](#story-2-multi-stage-training-pipeline-with-gpu-sharing)
    - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
  - [Design Details](#design-details)
    - [Common Types](#common-types)
    - [ResourceClaimTemplate Referencing](#resourceclaimtemplate-referencing)
    - [PodCliqueSet-Level Resource Sharing](#podcliqueset-level-resource-sharing)
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
    - [Follow-up: Common <code>NamespacedName</code> type](#follow-up-common-namespacedname-type)
    - [DRA Background](#dra-background)
<!-- /toc -->

## Summary

Grove provides a hierarchical and flexible Kubernetes API to describe inference and training workloads. It encodes
scheduling and scaling constraints at every level of a `PodCliqueSet` (PCS). A PCS can directly contain one
or more `PodClique` (PCLQ) instances and/or one or more `PodCliqueScalingGroup` (PCSG) instances, where each PCSG in
turn contains one or more PCLQ instances.

This GREP enhances the `PodCliqueSet` API to allow sharing of cluster resources (such as GPU accelerators) amongst a
group of pods at multiple levels of the Grove hierarchy by leveraging Kubernetes
[Dynamic Resource Allocation (DRA)](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/).
A new `resourceSharing` field is available at three levels:

* **PodCliqueSet level** — resources shared across an entire PCS or scoped per PCS replica,
  with optional filtering to target specific children.
* **PodClique level** — resources shared across an entire PCLQ or scoped per PCLQ replica.
* **PodCliqueScalingGroup level** — resources shared across an entire PCSG or scoped per PCSG replica,
  with optional filtering to target specific PodCliques.

At each level, users choose whether a resource is shared across all replicas or dedicated per replica.
This enables composable, multi-level resource sharing while giving users fine-grained scope control
at every hierarchy level.

## Motivation

Modern ML inference and training workloads often require multiple pods to share expensive cluster resources such as GPU
accelerators to optimize resource utilization and reduce costs. Grove's hierarchical API (PCS → PCSG → PCLQ) provides
natural boundaries for defining resource sharing scopes, but currently lacks the ability to specify how resources should
be shared within these boundaries.

Kubernetes DRA provides `ResourceClaim` and `ResourceClaimTemplate` APIs that enable resource sharing, but using them
directly in Grove's pod templates presents challenges:

- **ResourceClaim in pod templates**: All pods created from the template reference the same claim, which
  prevents scoping the claim to a subset of replicas — every replica shares one claim even when
  per-replica separation is desired.
- **ResourceClaimTemplate in pod templates**: Each pod gets a unique ResourceClaim, preventing any sharing within the
  desired scope (PCLQ or PCSG).

Grove needs a mechanism to orchestrate resource sharing that respects its hierarchical structure — allowing resources to
be shared at the PCS, PCLQ, or PCSG level with configurable scope at each level.

### The Need for Multiple Sharing Scopes

Real-world workloads require resource sharing at different granularities within the Grove hierarchy.
GPU sharing via DRA is the primary motivation today, but the design is intentionally generic — it
applies to any current or future DRA-managed resource. Some examples in this proposal (e.g. NVSwitch
fabrics, generic resources) may not have DRA drivers available today; they are included for
illustration purposes to make the examples more concrete and to demonstrate the genericity and
future-proofing of the design.

- **PCS `AllReplicas`**: A resource shared across ALL pods in ALL replicas of an entire PodCliqueSet.
- **PCS `PerReplica`**: A resource shared across ALL pods within a single PCS replica.
- **PCSG `AllReplicas`**: A resource shared across ALL replicas of a scaling group.
- **PCSG `PerReplica`**: A resource shared across all PodCliques in a single scaling group replica
  (e.g. a set of GPUs shared by a leader and its workers via MPS).
- **PCLQ `AllReplicas`**: A resource shared across all replicas of a standalone PodClique
  (e.g. all replicas of a metrics collector share one GPU pool).
- **PCLQ `PerReplica`**: A resource dedicated to a single PCLQ replica.

These scopes are orthogonal and composable. A single PodClique may participate in multiple scopes simultaneously.

### Goals

- Enable users to define resource sharing at all three levels of the Grove hierarchy
  (PodCliqueSet, PodClique, and PodCliqueScalingGroup).
- Enable users to scope resource sharing at the desired granularity — sharing a resource across all
  replicas of a given level or dedicating it per replica, with optional filtering to target specific
  children.
- Enable users to declare resource claim template definitions once at the PCS level, avoiding
  duplication across the hierarchy.
- Enable users to reference externally created `ResourceClaimTemplate` objects at any level in the
  hierarchy, supporting cross-PCS and cross-namespace reuse.

## Proposal

### User Stories

#### Story 1: Disaggregated Inference with Multi-Level Resource Sharing

A platform team deploys a disaggregated inference workload. It consists of:

- A **scaling group** (`sgx`) containing prefill leaders (PCA, 3 replicas) and prefill workers (PCB, 2 replicas).
- A **standalone PodClique** (`metrics`, 2 replicas) that runs outside any scaling group.

The workload requires resource sharing at all three levels of the hierarchy:

1. **PCS level**: Two generic resources (`res1`, `res2`) — `res1` shared across the entire PCS (`AllReplicas`),
   `res2` scoped per PCS replica (`PerReplica`).
2. **PCSG `PerReplica`**: A set of GPUs shared across all pods in one scaling group replica
   (leader + workers sharing GPUs via MPS).
3. **PCLQ `AllReplicas`** (standalone): A GPU pool shared across all replicas of the standalone
   `metrics` PodClique — all 2 replicas share one set of GPUs.

_Challenge_: Without hierarchical sharing, users must either reference a single `ResourceClaim` (which forces all
replicas to share one claim) or use `ResourceClaimTemplate` in the PodSpec (creating per-pod claims, preventing sharing).

_Solution_: Grove orchestrates resource sharing through a `resourceSharing` field at each hierarchy level:

- `resourceSharing` at the PCS level creates ResourceClaims shared across the entire PCS or per PCS replica.
- `resourceSharing` at the PCSG level with `scope: PerReplica` creates one ResourceClaim per PCSG replica,
  injected into all PodCliques in that replica.
- `resourceSharing` at the PCLQ level with `scope: AllReplicas` creates one ResourceClaim per PCLQ,
  shared across all replicas of that standalone PodClique.

Note: PCLQ `AllReplicas` for a standalone PodClique is distinct from PCSG `PerReplica` — the former
creates one RC shared across all replicas of a single PCLQ, while the latter creates one RC per
scaling group replica shared across multiple PCLQs.

**Concrete example** of the ResourceClaim distribution for a PCS named `disagg` with 2 replicas,
demonstrating all three levels with different scopes:

```
PCS "disagg" (replicas: 2):
  resourceClaimTemplates:
    - name: res1,          template: {generic DRA resource A}
    - name: res2,          template: {generic DRA resource B}
    - name: gpu-pool,      template: {4 GPUs, nvidia.com}
    - name: metrics-gpu,   template: {1 GPU, nvidia.com}
  resourceSharing:
    - {name: res1, scope: AllReplicas}                               # PCS AllReplicas
    - {name: res2, scope: PerReplica}                                # PCS PerReplica
  cliques:
    - pca: replicas=3   (in PCSG sgx)
    - pcb: replicas=2   (in PCSG sgx)
    - metrics: replicas=2, standalone,
               resourceSharing=[{name: metrics-gpu, scope: AllReplicas}]  # PCLQ AllReplicas
  scalingGroups:
    - sgx: cliqueNames=[pca, pcb], replicas=2,
           resourceSharing=[{name: gpu-pool, scope: PerReplica}]     # PCSG PerReplica
```

Grove creates the following ResourceClaim objects:

```
PCS AllReplicas (1 for the entire PCS):
  disagg-all-res1                     → shared by ALL pods across ALL PCS replicas

PCS PerReplica (1 per PCS replica):
  disagg-0-res2                       → shared by ALL pods in PCS replica 0
  disagg-1-res2                       → shared by ALL pods in PCS replica 1

PCSG PerReplica (1 per PCSG replica, per PCS replica):
  disagg-0-sgx-0-gpu-pool            → shared by ALL pods in PCSG replica 0, PCS replica 0
  disagg-0-sgx-1-gpu-pool            → shared by ALL pods in PCSG replica 1, PCS replica 0
  disagg-1-sgx-0-gpu-pool            → shared by ALL pods in PCSG replica 0, PCS replica 1
  disagg-1-sgx-1-gpu-pool            → shared by ALL pods in PCSG replica 1, PCS replica 1

PCLQ AllReplicas (1 per standalone PCLQ, per PCS replica):
  disagg-0-metrics-all-metrics-gpu   → shared by all 2 metrics pods in PCS replica 0
  disagg-1-metrics-all-metrics-gpu   → shared by all 2 metrics pods in PCS replica 1
```

The resulting ResourceClaim assignment per pod (showing PCS replica 0 only — PCS replica 1 follows the same pattern):

| Pod | PCS AllReplicas | PCS PerReplica | PCSG PerReplica | PCLQ AllReplicas |
|---|---|---|---|---|
| disagg-0-sgx-0-pca-0 | disagg-all-res1 | disagg-0-res2 | disagg-0-sgx-0-gpu-pool | — |
| disagg-0-sgx-0-pca-1 | disagg-all-res1 | disagg-0-res2 | disagg-0-sgx-0-gpu-pool | — |
| disagg-0-sgx-0-pca-2 | disagg-all-res1 | disagg-0-res2 | disagg-0-sgx-0-gpu-pool | — |
| disagg-0-sgx-0-pcb-0 | disagg-all-res1 | disagg-0-res2 | disagg-0-sgx-0-gpu-pool | — |
| disagg-0-sgx-0-pcb-1 | disagg-all-res1 | disagg-0-res2 | disagg-0-sgx-0-gpu-pool | — |
| disagg-0-sgx-1-pca-0 | disagg-all-res1 | disagg-0-res2 | disagg-0-sgx-1-gpu-pool | — |
| disagg-0-sgx-1-pca-1 | disagg-all-res1 | disagg-0-res2 | disagg-0-sgx-1-gpu-pool | — |
| disagg-0-sgx-1-pca-2 | disagg-all-res1 | disagg-0-res2 | disagg-0-sgx-1-gpu-pool | — |
| disagg-0-sgx-1-pcb-0 | disagg-all-res1 | disagg-0-res2 | disagg-0-sgx-1-gpu-pool | — |
| disagg-0-sgx-1-pcb-1 | disagg-all-res1 | disagg-0-res2 | disagg-0-sgx-1-gpu-pool | — |
| disagg-0-metrics-0   | disagg-all-res1 | disagg-0-res2 | — | disagg-0-metrics-all-metrics-gpu |
| disagg-0-metrics-1   | disagg-all-res1 | disagg-0-res2 | — | disagg-0-metrics-all-metrics-gpu |

In this example:
- `disagg-all-res1` is the PCS `AllReplicas` claim: one for the entire PCS, shared by all 24 pods
- `disagg-{0,1}-res2` are PCS `PerReplica` claims: one per PCS replica, shared by all 12 pods in that replica
- `disagg-{0,1}-sgx-{0,1}-gpu-pool` are PCSG `PerReplica` claims: one per PCSG replica, shared by all 5 pods (pca + pcb) in that replica
- `disagg-{0,1}-metrics-all-metrics-gpu` are PCLQ `AllReplicas` claims: one per standalone metrics PCLQ, shared by all 2 metrics pods
- Total ResourceClaims: 1 (PCS AllReplicas) + 2 (PCS PerReplica) + 4 (PCSG PerReplica) + 2 (PCLQ AllReplicas) = 9

#### Story 2: Multi-Stage Training Pipeline with GPU Sharing

Multi-stage ML pipelines with separate preprocessing and training components are a common pattern in production ML systems. Frameworks like [Kubeflow Pipelines](https://www.kubeflow.org/docs/components/pipelines/v1/introduction/), [TensorFlow Extended (TFX)](https://www.tensorflow.org/tfx), and [Ray Train](https://docs.ray.io/en/latest/train/train.html) enable users to define pipelines where data preprocessing (ETL, feature engineering, augmentation) runs as separate containers/pods from the training workload.

In such a distributed training pipeline, data preprocessing pods load and transform data into GPU memory, while model training pods consume this preprocessed data directly from GPU memory without expensive CPU-GPU transfers. Libraries like [NVIDIA DALI](https://docs.nvidia.com/deeplearning/dali/user-guide/docs/index.html) provide GPU-accelerated data preprocessing capabilities that make this pattern efficient. The preprocessing and training pods are modeled as separate PCLQs within a PCSG, where each PCSG replica represents a different training experiment.

_Challenge_: Each experiment (PCSG instance) needs its own isolated set of GPUs, but within an experiment, both preprocessing and training pods should share the same GPU devices for efficient data transfer and memory utilization. Standard GPU allocation creates exclusive claims per pod, preventing this sharing pattern. When these stages need to share GPUs for zero-copy data transfer and to avoid CPU-GPU memory copying overhead, DRA's [shareable ResourceClaims](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/#shareable-resources) become essential.

_Solution_: By leveraging GPU sharing technologies like [NVIDIA Multi-Process Service (MPS)](https://docs.nvidia.com/deploy/mps/index.html) or [CUDA IPC (Inter-Process Communication)](https://docs.nvidia.com/cuda/cuda-c-programming-guide/index.html#interprocess-communication) for sharing GPU memory between processes, Grove enables this pattern through `resourceSharing` at the PCSG level. Specifying `scope: PerReplica` creates one ResourceClaim per PCSG replica, shared by all PCLQs in that replica. This enables both pod types to access the same GPU devices within each experiment while keeping different experiments separate.

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
// referenced by ResourceSharingSpec entries in resourceSharing fields.
type ResourceClaimTemplateConfig struct {
	// Name is a unique identifier for this template within the PodCliqueSet.
	Name string `json:"name"`
	// Template is the ResourceClaimTemplate spec.
	Template resourcev1.ResourceClaimTemplateSpec `json:"template"`
}

// ResourceSharingSpec references a ResourceClaimTemplateSpec and defines the
// sharing scope for the resulting ResourceClaim(s).
// A given template name must appear at most once per resourceSharing list.
type ResourceSharingSpec struct {
	// Name of the referenced template. Resolved by first looking up
	// PodCliqueSetTemplateSpec.ResourceClaimTemplates; if no match is found,
	// the operator looks for a Kubernetes ResourceClaimTemplate object in the
	// target namespace. Internal templates shadow external ones with the same name.
	Name string `json:"name"`
	// Namespace of the external ResourceClaimTemplate. When set, the name is
	// resolved as an external Kubernetes ResourceClaimTemplate in the given
	// namespace. When empty, defaults to the PCS namespace.
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

// ResourceSharingFilter controls which children receive the ResourceClaims.
// Listed names are included; unlisted children do not receive the claims.
type ResourceSharingFilter struct {
	// CliqueNames lists PodClique template names to include.
	// +optional
	CliqueNames []string `json:"cliqueNames,omitempty"`
	// GroupNames lists PodCliqueScalingGroup config names to include.
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

**Validation and resolution rules:**
- The operator resolves `name` by first checking `PodCliqueSetTemplateSpec.ResourceClaimTemplates`.
  If a match is found, it is used as the template spec. `namespace` must be empty for internal references.
- If no internal match is found, the operator looks for a Kubernetes `ResourceClaimTemplate` object
  with the given `name`. If `namespace` is empty, the PCS namespace is used; otherwise the specified
  namespace is used.
- Internal templates shadow external ones with the same name. This is deterministic and by design.

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
	ResourceSharing []ResourceSharingSpec `json:"resourceSharing,omitempty"`
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
      - name: global-gpu-cache
        template:
          spec:
            devices:
              requests:
                - name: gpu
                  deviceClassName: gpu.nvidia.com
                  count: 4
      - name: replica-gpu-pool
        template:
          spec:
            devices:
              requests:
                - name: gpu
                  deviceClassName: gpu.nvidia.com
                  count: 2
    resourceSharing:
      - name: global-gpu-cache
        scope: AllReplicas
      - name: replica-gpu-pool
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
- Two templates are declared once at PCS level (`global-gpu-cache`, `replica-gpu-pool`)
- No `filter` → broadcast to all PodCliques
- `AllReplicas` creates 1 RC (`disagg-all-global-gpu-cache`) shared by ALL 8 pods across both PCS replicas
- `PerReplica` creates 2 RCs (`disagg-0-replica-gpu-pool`, `disagg-1-replica-gpu-pool`), one per PCS replica,
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
      - name: shared-gpu-cache
        template:
          spec:
            devices:
              requests:
                - name: gpu
                  deviceClassName: gpu.nvidia.com
                  count: 2
      - name: training-gpu-pool
        template:
          spec:
            devices:
              requests:
                - name: gpu
                  deviceClassName: gpu.nvidia.com
                  count: 4
    resourceSharing:
      - name: shared-gpu-cache
        scope: AllReplicas
      - name: training-gpu-pool
        scope: PerReplica
        filter:
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
- `shared-gpu-cache` with `AllReplicas` scope and no `filter` → broadcast to all pods (preprocessor, trainer, and standalone monitor)
- `training-gpu-pool` with `PerReplica` scope and `filter: {groupNames: [training-group]}` → only pods within `training-group` receive the GPU pool. The standalone `monitor` PCLQ does not get it
- This avoids giving GPU access to the monitor pod that doesn't need it

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
	ResourceSharing []ResourceSharingSpec `json:"resourceSharing,omitempty"`
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
- The 2 PCS replicas each get separate ResourceClaims with separate GPUs

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
	ResourceSharing []ResourceSharingSpec `json:"resourceSharing,omitempty"`
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
- Each of the 3 experiments gets separate ResourceClaims with separate GPU sets

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

**Concrete example** — PCS `disagg` (replica 0), PCSG `sgx` (replicas: 2), cliques in PCSG:
`pca` (replicas: 3), `pcb` (replicas: 2); standalone PCLQ: `metrics` (replicas: 2).
PCS AllReplicas rctName=res1, PCS PerReplica rctName=res2, PCSG PerReplica rctName=gpu-pool,
PCLQ AllReplicas rctName=metrics-gpu:

```
PCS AllReplicas ResourceClaim:
  disagg-all-res1                                         → shared by ALL pods in the entire PCS

PCS PerReplica ResourceClaims:
  disagg-0-res2                                           → shared by ALL pods in PCS replica 0

PCSG PerReplica ResourceClaims:
  disagg-0-sgx-0-gpu-pool                                 → shared by ALL pods in PCSG replica 0
  disagg-0-sgx-1-gpu-pool                                 → shared by ALL pods in PCSG replica 1

PCLQ AllReplicas ResourceClaims:
  disagg-0-metrics-all-metrics-gpu                        → shared by all metrics pods in PCS replica 0
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

### Follow-up: Common `NamespacedName` type

The scheduler module defines a `NamespacedName` type with JSON tags
(in `scheduler/api/core/v1alpha1/podgang.go`) because `types.NamespacedName` from apimachinery
lacks them. The `Name`/`Namespace` fields on `ResourceSharingSpec` serve a similar purpose but
are not a direct fit for reuse: in the scheduler's `NamespacedName` both `Namespace` and `Name` are
required fields, whereas in `ResourceSharingSpec` the `Namespace` is optional (it defaults to the
PCS namespace when omitted). A common API module (e.g., `grove/api/common`) could host a shared type
if `Namespace` is made optional (with `omitempty`), but this would require updating the scheduler's
usage as well. Tracked as a follow-up item, orthogonal to this GREP.

### DRA Background

In case the readers are not familiar with DRA, the following links will help them get started:
* [Kubernetes DRA Official Documentation](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
* [Dynamic Resource Allocation (DRA) KEP](https://github.com/kubernetes/enhancements/tree/master/keps/sig-node/4381-dra-structured-parameters)

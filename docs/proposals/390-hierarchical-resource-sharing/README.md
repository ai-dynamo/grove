# Hierarchical Resource Sharing

<!-- toc -->

- [Summary](#summary)
- [Motivation](#motivation)
    - [The Need for Multiple Sharing Scopes](#the-need-for-multiple-sharing-scopes)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
- [Proposal](#proposal)
    - [User Stories](#user-stories)
        - [Story 1: Disaggregated Inference with Multi-Level GPU Sharing](#story-1-disaggregated-inference-with-multi-level-gpu-sharing)
        - [Story 2: Multi-Stage Training Pipeline with GPU Sharing](#story-2-multi-stage-training-pipeline-with-gpu-sharing)
    - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
    - [Common Types](#common-types)
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

<!-- /toc -->

## Summary

Grove provides a hierarchical and flexible Kubernetes API to describe inference and training workloads. It encodes
scheduling and scaling constraints at every level of a `PodCliqueSet` (PCS). A PCS can directly contain one
or more `PodClique` (PCLQ) instances and/or one or more `PodCliqueScalingGroup` (PCSG) instances, where each PCSG in
turn contains one or more PCLQ instances.

This GREP enhances the `PodCliqueSet` API to allow sharing of cluster resources (such as GPU accelerators) amongst a
group of pods at multiple levels of the Grove hierarchy by leveraging
[ResourceClaim](https://github.com/kubernetes/api/blob/ffebe2b51dedadf6a36343b495ca26060cb7a93d/resource/v1/types.go#L741)
offered via Dynamic Resource Allocation (DRA) in Kubernetes. Users provide inline `ResourceClaimTemplateSpec`
definitions via a unified `ResourceSharingRule` type with a `Scope` enum (`PerInstance` or `PerReplica`), and Grove
creates and manages `ResourceClaim` objects directly — no `ResourceClaimTemplate` objects are created. A new
`resourceSharing` field is available at three levels of the hierarchy:

* **PodCliqueSet level** — resources shared across an entire PCS instance or per PCS replica.
* **PodClique level** — resources shared across an entire PCLQ instance or per PCLQ replica.
* **PodCliqueScalingGroup level** — resources shared across an entire PCSG instance or per PCSG replica,
  with an optional `cliqueNames` filter to target specific PodCliques.

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

- **PCS `PerInstance`**: A resource shared across ALL pods in ALL replicas of an entire PodCliqueSet
  (e.g. a shared storage pool).
- **PCS `PerReplica`**: A resource shared across ALL pods within a single PCS replica.
- **PCSG `PerInstance`**: A shared resource across ALL replicas of a scaling group
  (e.g. a shared storage or interconnect resource).
- **PCSG `PerReplica`**: An NVSwitch or interconnect resource shared across all PodCliques in a scaling group
  replica (e.g. a leader and its workers sharing a fabric).
- **PCLQ `PerInstance`**: A set of GPUs shared across all replicas of a PodClique instance (e.g. all worker
  replicas in a scaling group replica share one pool of GPUs).
- **PCLQ `PerReplica`**: A resource dedicated to a single PCLQ replica, shared by all pods within that replica.

These scopes are orthogonal and composable. A single PodClique may participate in multiple scopes simultaneously.

### Goals

- Enable users to define resource sharing primitives at all three levels of the Grove hierarchy
  (PodCliqueSet, PodClique, and PodCliqueScalingGroup) via a unified `ResourceSharingRule` type with
  a `Scope` enum.
- Users should be able to scope resource sharing at the desired granularity, e.g. share resources
  between all pods of a PodClique instance (`PerInstance`), per PCLQ replica (`PerReplica`), between
  a subset of PCLQs within a PCSG replica (`PerReplica` with `cliqueNames`), across all PCSG replicas
  (`PerInstance`), or across an entire PCS instance or per PCS replica.
- Enable users to provide inline `ResourceClaimTemplateSpec` definitions for resource sharing groups.

### Non Goals

## Proposal

### User Stories

#### Story 1: Disaggregated Inference with Multi-Level GPU Sharing

A platform team deploys a disaggregated inference workload with a prefill leader (PCA, 3 replicas) and prefill workers
(PCB, 2 replicas) grouped in a scaling group. The workload requires two levels of resource sharing:

1. **PCSG `PerReplica`**: An NVSwitch fabric claim shared across all pods in the scaling group replica
   (leader + workers).
2. **PCLQ `PerInstance`**: A GPU pool claim shared across all replicas of a PodClique instance — e.g. all 3
   PCA replicas share one set of GPUs, all 2 PCB replicas share another.

_Challenge_: Without hierarchical sharing, users must either reference a single `ResourceClaim` (breaking isolation
across PCSG replicas) or use `ResourceClaimTemplate` in the PodSpec (creating per-pod claims, preventing sharing).

_Solution_: Grove orchestrates resource sharing via a unified `ResourceSharingRule` type with scope enum:

- `resourceSharing` at the PCSG level with `scope: PerReplica` creates one ResourceClaim per PCSG replica,
  injected into all PodCliques in that replica.
- `resourceSharing` at the PCLQ level with `scope: PerInstance` creates one ResourceClaim per PCLQ instance,
  shared across all replicas.

**Concrete example** of the ResourceClaim distribution:

```
PCS:
  cliques:
    - PCA: replicas=3,
           resourceSharing=[{scope: PerInstance, spec: RCT-N}]
    - PCB: replicas=2,
           resourceSharing=[{scope: PerInstance, spec: RCT-P}]
  scalingGroups:
    - SGX: {PCA, PCB}, replicas=2, resourceSharing=[{scope: PerReplica, spec: RCT-M, cliqueNames: [PCA, PCB]}]

SGX-0: RC-M0   (PCSG PerReplica — shared by ALL pods in SGX-0)
  SGX-0-PCA: RC-N0   (PCLQ PerInstance — shared by all 3 PCA pods)
    SGX-0-PCA-0, SGX-0-PCA-1, SGX-0-PCA-2
  SGX-0-PCB: RC-P0   (PCLQ PerInstance — shared by all 2 PCB pods)
    SGX-0-PCB-0, SGX-0-PCB-1

SGX-1: RC-M1
  SGX-1-PCA: RC-N1
    SGX-1-PCA-0, SGX-1-PCA-1, SGX-1-PCA-2
  SGX-1-PCB: RC-P1
    SGX-1-PCB-0, SGX-1-PCB-1
```

In this example:
- RC-M0/RC-M1 are PCSG `PerReplica` claims: one per PCSG replica, shared by every pod in that replica
- RC-N0/RC-P0 are PCLQ `PerInstance` claims: one per PCLQ instance, shared by all replicas

#### Story 2: Multi-Stage Training Pipeline with GPU Sharing

Multi-stage ML pipelines with separate preprocessing and training components are a common pattern in production ML systems. Frameworks like [Kubeflow Pipelines](https://www.kubeflow.org/docs/components/pipelines/v1/introduction/), [TensorFlow Extended (TFX)](https://www.tensorflow.org/tfx), and [Ray Train](https://docs.ray.io/en/latest/train/train.html) enable users to define pipelines where data preprocessing (ETL, feature engineering, augmentation) runs as separate containers/pods from the training workload.

In such a distributed training pipeline, data preprocessing pods load and transform data into GPU memory, while model training pods consume this preprocessed data directly from GPU memory without expensive CPU-GPU transfers. Libraries like [NVIDIA DALI](https://docs.nvidia.com/deeplearning/dali/user-guide/docs/index.html) provide GPU-accelerated data preprocessing capabilities that make this pattern efficient. The preprocessing and training pods are modeled as separate PCLQs within a PCSG, where each PCSG replica represents a different training experiment.

_Challenge_: Each experiment (PCSG instance) needs its own isolated set of GPUs, but within an experiment, both preprocessing and training pods should share the same GPU devices for efficient data transfer and memory utilization. Standard GPU allocation creates exclusive claims per pod, preventing this sharing pattern. When these stages need to share GPUs for zero-copy data transfer and to avoid CPU-GPU memory copying overhead, DRA's [shareable ResourceClaims](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/#shareable-resources) become essential.

_Solution_: By leveraging GPU sharing technologies like [NVIDIA Multi-Process Service (MPS)](https://docs.nvidia.com/deploy/mps/index.html) for efficient GPU sharing or [CUDA IPC (Inter-Process Communication)](https://docs.nvidia.com/cuda/cuda-c-programming-guide/index.html#interprocess-communication) for sharing GPU memory between processes, along with techniques like [GPU Direct Storage](https://developer.nvidia.com/gpudirect-storage) for direct data paths, Grove enables this pattern through `resourceSharing` at the PCSG level. By specifying a `ResourceSharingRule` with `scope: PerReplica` and `cliqueNames` referencing both the preprocessing and training PCLQs, Grove creates a ResourceClaim per PCSG replica that is shared across the specified PCLQs. This enables both pod types to access the same GPU devices within each experiment while maintaining isolation across different experiments.

### Limitations/Risks & Mitigations

<!-- 
What are the current set of limitations or risks of this proposal? Think broadly by considering the impact of the changes proposed on kubernetes ecosystem. Optionally mention ways to mitigate these.
-->

## Design Details

### Common Types

```go
// ResourceSharingScope defines the sharing scope for a ResourceSharingRule.
// +kubebuilder:validation:Enum=PerInstance;PerReplica
type ResourceSharingScope string

const (
	// ResourceSharingScopePerInstance creates one ResourceClaim per instance of the
	// owning resource (PCS, PCLQ, or PCSG), shared across all replicas and pods.
	ResourceSharingScopePerInstance ResourceSharingScope = "PerInstance"
	// ResourceSharingScopePerReplica creates one ResourceClaim per replica, shared
	// across all pods within that replica.
	ResourceSharingScopePerReplica ResourceSharingScope = "PerReplica"
)

// ResourceSharingRule defines a single shared ResourceClaim with a scope that determines
// how many ResourceClaim instances are created.
type ResourceSharingRule struct {
	// Scope determines the sharing granularity. PerInstance creates one RC for the entire
	// resource instance. PerReplica creates one RC per replica.
	Scope ResourceSharingScope `json:"scope"`
	// Spec is an inline ResourceClaimTemplate spec. Grove creates and manages ResourceClaim
	// objects directly from this spec.
	Spec resourcev1.ResourceClaimTemplateSpec `json:"spec"`
}

// PCSGResourceSharingRule extends ResourceSharingRule with a CliqueNames field that scopes
// which PodCliques in the scaling group receive the shared ResourceClaims.
type PCSGResourceSharingRule struct {
	ResourceSharingRule `json:",inline"`
	// CliqueNames limits which PodCliques in the scaling group receive the ResourceClaims.
	// If empty, all PodCliques in the group receive them.
	// +optional
	CliqueNames []string `json:"cliqueNames,omitempty"`
}
```

Each `ResourceSharingRule` entry maps to exactly one ResourceClaim pattern: a single `Spec` and a `Scope`
that controls how many RC instances are created. Grove creates and fully manages `ResourceClaim` objects directly
from these specs — no intermediate `ResourceClaimTemplate` objects are created. This is because Kubernetes'
built-in RCT-to-RC auto-creation (`resourceClaimTemplateName` in the pod spec) creates a unique ResourceClaim
per pod, which is the opposite of sharing. For shared claims, we must pre-create ResourceClaim objects and
reference them via `resourceClaimName` in the pod spec. Since an intermediate RCT would not participate in any
Kubernetes mechanism (no pod references it), it is omitted. The inline specs in each `ResourceSharingRule` entry
serve as the source of truth. ResourceClaimTemplate objects can be added later for observability if needed.

See [ResourceClaim Naming Convention](#resourceclaim-naming-convention) for the deterministic naming scheme and
[Owner References and Garbage Collection](#owner-references-and-garbage-collection) for lifecycle semantics.

### PodCliqueSet-Level Resource Sharing

**API**

```go
type PodCliqueSetTemplateSpec struct {
	// Cliques is a slice of cliques that make up the PodGang.
	Cliques []*PodCliqueTemplateSpec `json:"cliques"`
	...
	// ResourceSharing defines shared ResourceClaims at the PCS level. Each entry
	// creates ResourceClaims at the granularity specified by its Scope:
	//   - PerInstance: one RC for the entire PCS, shared across ALL pods in ALL replicas
	//   - PerReplica: one RC per PCS replica, shared across ALL pods in that replica
	// +optional
	ResourceSharing []ResourceSharingRule `json:"resourceSharing,omitempty"`
	...
}
```

To enable resource sharing across an entire `PodCliqueSet` or per PCS replica, a new `ResourceSharing` field is
added to `PodCliqueSetTemplateSpec`. The PCS controller creates the ResourceClaim objects and all child controllers
(PCSG and standalone PCLQ) inject the PCS-level claim references into pod specs.

- `PerInstance`: One RC for the entire PCS — shared by every pod across all PCS replicas, all PCSGs, and all PCLQs.
- `PerReplica`: One RC per PCS replica — shared by every pod within that PCS replica.

**Example:**

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: disagg
  namespace: default
spec:
  replicas: 2
  template:
    resourceSharing:
      - scope: PerInstance
        spec:
          spec:
            devices:
              requests:
                - name: shared-storage
                  deviceClassName: storage.example.com
                  count: 1
      - scope: PerReplica
        spec:
          spec:
            devices:
              requests:
                - name: interconnect
                  deviceClassName: nvswitch.nvidia.com
                  count: 1
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
- `PerInstance` creates 1 RC (`disagg-rct-0`) shared by ALL 8 pods across both PCS replicas
- `PerReplica` creates 2 RCs (`disagg-0-rct-1`, `disagg-1-rct-1`), one per PCS replica,
  each shared by the 4 worker pods in that replica

### PodClique-Level Resource Sharing

**API**

```go
type PodCliqueTemplateSpec struct {
	// Name must be unique within a PodCliqueSet and is used to denote a role.
	Name string `json:"name"`
	...
	// ResourceSharing defines shared ResourceClaims for this PodClique. Each entry
	// creates ResourceClaims at the granularity specified by its Scope:
	//   - PerInstance: one RC per PCLQ instance, shared by all replica pods
	//   - PerReplica: one RC per PCLQ replica, shared by all pods within that replica
	// NOTE: This is not the same as adding ResourceClaimTemplate inside the
	// Spec.PodSpec.ResourceClaims[x].ResourceClaimTemplateName in the PodClique since that will
	// create a unique ResourceClaim for each pod in the PodClique.
	// +optional
	ResourceSharing []ResourceSharingRule `json:"resourceSharing,omitempty"`
	// Specification of the desired behavior of a PodClique.
	Spec PodCliqueSpec `json:"spec"`
}
```

To enable resource sharing among `Pod`s within a `PodClique`, a new field `ResourceSharing` is added
to `PodCliqueTemplateSpec`. Each entry has a `Scope` and a single inline `ResourceClaimTemplateSpec`.

- `PerInstance`: One RC per PCLQ instance — shared by all replica pods in that PCLQ.
- `PerReplica`: One RC per PCLQ replica — shared by all pods within that replica.

The parent controller (PCS or PCSG) processes the entries, creates the ResourceClaims, and injects the claim
references into the PCLQ's `PodSpec`.

**Example:**

The following example shows how to use `resourceSharing` with `PerInstance` scope to share GPUs among all
pods within a single PodClique instance:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: shared-gpu-example
  namespace: default
spec:
  replicas: 2
  template:
    cliques:
      - name: inference
        resourceSharing:
          - scope: PerInstance
            spec:
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
- The `PerInstance` scope creates one ResourceClaim per PodClique instance
- The inline spec defines 2 GPUs with time-slicing enabled
- All 4 pods within each PodClique instance share the same 2 GPUs
- The 2 PCS replicas maintain isolation (different ResourceClaims, different GPUs)

### PodCliqueScalingGroup-Level Resource Sharing

**API**

```go
type PodCliqueScalingGroupConfig struct {
	// Name is the name of the PodCliqueScalingGroupConfig. This should be unique within the PodCliqueSet.
	Name string `json:"name"`
	...
	// ResourceSharing defines shared ResourceClaims at the PCSG level. Each entry
	// creates ResourceClaims at the granularity specified by its Scope:
	//   - PerInstance: one RC for the entire PCSG, shared across all replicas
	//   - PerReplica: one RC per PCSG replica, shared across all PCLQs in that replica
	// CliqueNames limits which PodCliques receive the claims (empty = all).
	// +optional
	ResourceSharing []PCSGResourceSharingRule `json:"resourceSharing,omitempty"`
}
```

**Example:**

The following example demonstrates sharing resources across multiple PodCliques within a PodCliqueScalingGroup,
using `PerReplica` scope so each PCSG replica gets its own isolated ResourceClaim:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: training-pipeline
  namespace: default
spec:
  replicas: 1
  template:
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
    podCliqueScalingGroups:
      - name: training-experiment
        replicas: 3
        cliqueNames:
          - data-preprocessor
          - model-trainer
        resourceSharing:
          - scope: PerReplica
            spec:
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
            cliqueNames:
              - data-preprocessor
              - model-trainer
```

In this example:
- The `PerReplica` scope creates one ResourceClaim per PCSG replica
- The inline spec defines 4 GPUs with NVIDIA MPS for sharing
- 3 PCSG replicas create 3 independent training experiments
- Within each experiment (PCSG replica):
  - 2 preprocessing pods + 3 training pods = 5 total pods share the same 4 GPUs
  - All pods can access the same GPU memory space
- Each of the 3 experiments maintains isolation (different ResourceClaims, different GPU sets)

### ResourceClaim Naming Convention

Each RC name is derived from the owning resource's Kubernetes name plus a suffix that encodes the
allocation index (position in the `resourceSharing` list).

| Level + Scope | RC Name Format |
|---|---|
| PCS `PerInstance` | `<pcsName>-rct-<allocIndex>` |
| PCS `PerReplica` | `<pcsName>-<pcsReplicaIndex>-rct-<allocIndex>` |
| PCLQ `PerInstance` | `<pclqName>-rct-<allocIndex>` |
| PCLQ `PerReplica` | `<pclqName>-<replicaIndex>-rct-<allocIndex>` |
| PCSG `PerInstance` | `<pcsgName>-rct-<allocIndex>` |
| PCSG `PerReplica` | `<pcsgName>-<pcsgReplicaIndex>-rct-<allocIndex>` |

The `rct-<index>` suffix identifies the position of the entry in the `resourceSharing` list.

**Concrete example** — PCS `disagg` (replica 0), PCSG `model-instance` (replicas: 2), cliques:
`prefill-wkr` (replicas: 3, PCLQ PerInstance at index 0), `decode-wkr` (replicas: 2, PCLQ PerInstance
at index 0), PCS PerInstance at index 0, PCSG PerReplica at index 0:

```
PCS PerInstance ResourceClaim:
  disagg-rct-0                               → shared by ALL pods in the entire PCS

PCS PerReplica ResourceClaims:
  (none in this example — would be disagg-0-rct-<index> if configured)

PCSG PerReplica ResourceClaims:
  disagg-0-model-instance-0-rct-0            → shared by ALL pods in PCSG replica 0
  disagg-0-model-instance-1-rct-0            → shared by ALL pods in PCSG replica 1

PCLQ PerInstance ResourceClaims:
  disagg-0-model-instance-0-prefill-wkr-rct-0 → shared by all prefill-wkr pods in PCSG replica 0
  disagg-0-model-instance-1-prefill-wkr-rct-0 → shared by all prefill-wkr pods in PCSG replica 1
  disagg-0-model-instance-0-decode-wkr-rct-0  → shared by all decode-wkr pods in PCSG replica 0
  disagg-0-model-instance-1-decode-wkr-rct-0  → shared by all decode-wkr pods in PCSG replica 1
```

**For standalone PodCliques** (not in a PCSG), the PCLQ resource name is `<pcs>-<pcsIndex>-<pclqTemplate>`,
so the pattern is the same:

```
PCLQ PerInstance:
  my-svc-0-frontend-rct-0    → shared by all frontend pods
```

### Owner References and Garbage Collection

ResourceClaim ownership determines garbage collection behavior. All RCs are owned by the resource that
defines the broadest scope they serve. On scale-down, explicit cleanup is performed for PerReplica RCs
whose parent still exists.

| Level + Scope | Owner | Cleanup on Scale-Down |
|---|---|---|
| PCS `PerInstance` | PCS object | GC'd when PCS is deleted |
| PCS `PerReplica` | PCS object | Explicit cleanup when PCS replicas are scaled down |
| PCSG `PerInstance` | PCSG object | GC'd when PCSG is deleted |
| PCSG `PerReplica` | PCSG object | Explicit cleanup when PCSG replicas are scaled down |
| PCLQ `PerInstance` (in PCSG) | PCSG object | Explicit cleanup when PCSG replicas are scaled down |
| PCLQ `PerInstance` (standalone) | PCS object | Explicit cleanup when PCS replicas are scaled down |
| PCLQ `PerReplica` (in PCSG) | PCSG object | Explicit cleanup when PCLQ replicas are scaled down |
| PCLQ `PerReplica` (standalone) | PCS object | Explicit cleanup when PCLQ replicas are scaled down |

**Design rationale**: Owning RCs at the PCSG/PCS level (rather than the PCLQ level) avoids depending on
the controller-runtime cache to reflect freshly created PCLQ objects during the same reconcile. It also
ensures that PCLQ PerInstance RCs survive PCLQ rolling updates — the replacement PCLQ reuses the existing
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

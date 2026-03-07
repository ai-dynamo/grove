# Hierarchical Resource Sharing

<!-- toc -->

- [Summary](#summary)
- [Motivation](#motivation)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
- [Proposal](#proposal)
    - [User Stories (<em>Optional</em>)](#user-stories-optional)
        - [Story 1: Fault-Tolerant Inference with Shadow Pods](#story-1-fault-tolerant-inference-with-shadow-pods)
        - [Story 2: Multi-Stage Training Pipeline with GPU Sharing](#story-2-multi-stage-training-pipeline-with-gpu-sharing)
    - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
    - [Monitoring](#monitoring)
    - [Dependencies (<em>Optional</em>)](#dependencies-optional)
    - [Test Plan](#test-plan)
    - [Graduation Criteria](#graduation-criteria)
- [Implementation History (<em>Optional</em>)](#implementation-history-optional)
- [Alternatives (<em>Optional</em>)](#alternatives-optional)
- [Appendix (<em>Optional</em>)](#appendix-optional)

<!-- /toc -->

## Summary

Grove provides a hierarchical and flexible Kubernetes API to describe inference and training workloads. It encodes in 
scheduling and scaling constraints at every level of a `PodCliqueSet` (PCS). A PCS can directly contain one 
or more `PodClique` (PCLQ) instances and/or one or more `PodCliqueScalingGroup` (PCSG) instances, where each PCSG in 
turn contains one or more PCLQ instances.

This GREP enhances the `PodCliqueSet` API to allow sharing of cluster resources (such as GPU accelerators) amongst a 
group of pods either at the PCLQ level or PCSG level by leveraging [ResourceClaim](https://github.com/kubernetes/api/blob/ffebe2b51dedadf6a36343b495ca26060cb7a93d/resource/v1/types.go#L741) and [ResourceClaimTemplate](https://github.com/kubernetes/api/blob/ffebe2b51dedadf6a36343b495ca26060cb7a93d/resource/v1/types.go#L1850) 
offered via Dynamic Resource Allocation (DRA) in Kubernetes. The design enables: 

* Pods within a single PCLQ instance to share resources, or 
* Pods across a subset of PCLQs within a PCSG instance to share resources, while ensuring proper isolation between replicas during scaling operations.

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

Grove needs a mechanism to orchestrate resource sharing that respects its hierarchical structure—allowing resources to 
be shared within a PCLQ instance or across a subset of PCLQs within a PCSG instance, while maintaining proper isolation 
across different instances during scaling operations.

### Goals

- Enable users to define resource sharing primitives at multiple levels of Grove hierarchy, i.e. PodClique and
  PodCliqueScalingGroup.
- Users should be able to limit and scope resource sharing within subset of a group or within a specific level,
  e.g. share resource between pods of a PodClique instance vs between pods of a PCSG instance, or between a subset of
  PCLQs within a PCSG instance.
- Enable users to reference externally created ResourceClaimTemplates to be used for a resource sharing group.

### Non Goals

* Extending `PodCliqueSet` API for users to define `ResourceClaimTemplateSpec`s.

## Proposal




### User Stories (*Optional*)

#### Story 1: Fault-Tolerant Inference with Shadow Pods

Dynamo wants to create shadow pods alongside every active pod that share the same set of GPU(s) to enable quick recovery from faults. When an active pod fails, the shadow pod (which has already warmed up the model on the shared GPU) can immediately take over, minimizing downtime.

_Challenge_: Using a ResourceClaim directly in the PodClique's pod template causes all pods across all PCSG replicas to reference the same claim, breaking isolation. Using a ResourceClaimTemplate creates unique claims per pod, preventing the desired sharing between active and shadow pods within the same PCLQ instance.

_Solution_: By specifying ResourceClaimTemplates at the PCLQ level via `ResourceClaimTemplateNames`, Grove creates a single ResourceClaim per PCLQ instance, allowing both active and shadow pods within that instance to share the GPU while maintaining isolation across different PCLQ instances.

#### Story 2: Multi-Stage Training Pipeline with GPU Sharing

Multi-stage ML pipelines with separate preprocessing and training components are a common pattern in production ML systems. Frameworks like [Kubeflow Pipelines](https://www.kubeflow.org/docs/components/pipelines/v1/introduction/), [TensorFlow Extended (TFX)](https://www.tensorflow.org/tfx), and [Ray Train](https://docs.ray.io/en/latest/train/train.html) enable users to define pipelines where data preprocessing (ETL, feature engineering, augmentation) runs as separate containers/pods from the training workload.

In such a distributed training pipeline, data preprocessing pods load and transform data into GPU memory, while model training pods consume this preprocessed data directly from GPU memory without expensive CPU-GPU transfers. Libraries like [NVIDIA DALI](https://docs.nvidia.com/deeplearning/dali/user-guide/docs/index.html) provide GPU-accelerated data preprocessing capabilities that make this pattern efficient. The preprocessing and training pods are modeled as separate PCLQs within a PCSG, where each PCSG replica represents a different training experiment.

_Challenge_: Each experiment (PCSG instance) needs its own isolated set of GPUs, but within an experiment, both preprocessing and training pods should share the same GPU devices for efficient data transfer and memory utilization. Standard GPU allocation creates exclusive claims per pod, preventing this sharing pattern. When these stages need to share GPUs for zero-copy data transfer and to avoid CPU-GPU memory copying overhead, DRA's [shareable ResourceClaims](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/#shareable-resources) become essential.

_Solution_: By leveraging GPU sharing technologies like [NVIDIA Multi-Process Service (MPS)](https://docs.nvidia.com/deploy/mps/index.html) for efficient GPU sharing or [CUDA IPC (Inter-Process Communication)](https://docs.nvidia.com/cuda/cuda-c-programming-guide/index.html#interprocess-communication) for sharing GPU memory between processes, along with techniques like [GPU Direct Storage](https://developer.nvidia.com/gpudirect-storage) for direct data paths, Grove enables this pattern through `ResourceClaimTemplateConfigs` at the PCSG level. By specifying `resourceClaimTemplateConfigs` with `cliqueNames` referencing both the preprocessing and training PCLQs, Grove creates a ResourceClaim per PCSG instance that is shared across the specified PCLQs. This enables both pod types to access the same GPU devices within each experiment while maintaining isolation across different experiments.

### Limitations/Risks & Mitigations

<!-- 
What are the current set of limitations or risks of this proposal? Think broadly by considering the impact of the changes proposed on kubernetes ecosystem. Optionally mention ways to mitigate these.
-->

## Design Details

### PodClique-Level Resource Sharing

**API** 

```go
type PodCliqueTemplateSpec struct {
// Name must be unique within a PodCliqueSet and is used to denote a role.
// Once set it cannot be updated.
// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/names#names
Name string `json:"name"`
...
// ResourceClaimTemplateNames is a list of resource.ResourceClaimTemplate names which will be used to create
// ResourceClaims that are added to the PodSpec of each core.Pod in the PodClique instance, thus allowing sharing of
// resources such as accelerators across all pods in the PodClique. All ResourceClaims created will be completely
// managed by Grove.
// NOTE: This is not the same as adding ResourceClaimTemplate inside the
// Spec.PodSpec.ResourceClaims[x].ResourceClaimTemplateName in the PodClique since that will create a unique
// ResourceClaim for each pod in the PodClique.
ResourceClaimTemplateNames []string `json:"resourceClaimTemplateNames,omitempty"`
// Specification of the desired behavior of a PodClique.
// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#spec-and-status
Spec PodCliqueSpec `json:"spec"`
}
```

To enbable resource sharing among `Pod`s within a `PodClique`, a new field `ResourceClaimTemplateNames` will be added to `PodCliqueTemplateSpec`. It is expected that users will create all relevant `ResourceClaimTemplate` prior to referencing it inside `PodCliqueTemplateSpec` and all `ResourceClaimTemplate`s will be created in the same namespace as the `PodCliqueSet` where these are referenced.

PodClique reconciler will fetch the referred `ResourceClaimTemplateNames` and for each `ResourceClaimTemplate` name it will create a `ResourceClaim`. All of the resource claims will then be configured in the `PodSpec`.

**Example:**

The following example shows how to use `ResourceClaimTemplateNames` to enable resource sharing among pods within a single PodClique instance.

First, create a `ResourceClaimTemplate` that defines the GPU resources to be shared:

```yaml
apiVersion: resource.k8s.io/v1alpha3
kind: ResourceClaimTemplate
metadata:
  name: gpu-claim-template
  namespace: default
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
```

Then, reference this template in your `PodCliqueSet` to enable sharing within a PodClique:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: shadow-pods-example
  namespace: default
spec:
  replicas: 2  # Creates 2 instances of the PodClique (each gets its own ResourceClaim)
  template:
    cliques:
      - name: inference-with-shadows
        # Reference the ResourceClaimTemplate - Grove will create one ResourceClaim per PCLQ instance
        resourceClaimTemplateNames:
          - gpu-claim-template
        spec:
          roleName: inference
          replicas: 4  # 1 primary + 3 shadow pods, all sharing the same GPUs
          podSpec:
            containers:
              - name: inference
                image: nvidia/cuda:12.0-runtime
                command: ["/bin/sh", "-c"]
                args:
                  - |
                    echo "Pod: $POD_NAME - Using shared GPU"
                    # Model inference code here
                    sleep infinity
                resources:
                  requests:
                    cpu: "1"
                    memory: "2Gi"
            restartPolicy: Always
```

In this example:
- The `gpu-claim-template` defines 2 GPUs with time-slicing enabled
- Each PodClique instance (replica) gets its own `ResourceClaim` created from the template
- All 4 pods within each PodClique instance share the same 2 GPUs
- The 2 PCS replicas maintain isolation (different ResourceClaims, different GPUs)



### PodCliqueScalingGroup-Level Resource Sharing

**API**

```go
// ResourceClaimTemplateConfig defines a common set of resourcev1.ResourceClaimTemplate names for a set of PodCliques.
// A ResourceClaim is created per ResourceClaimTemplate name and added to the PodSpec of each PodClique specified
// in the CliqueNames field. This allows sharing of resources such as accelerators across all pods in the specified
// PodCliques that are part of one PodCliqueScalingGroup instance.
type ResourceClaimTemplateConfig struct {
// Names is a list of resourcev1.ResourceClaimTemplate names which will be used to create ResourceClaims.
Names []string `json:"names"`
// CliqueNames is a list of names of PodCliques that will use the ResourceClaimTemplates specified in the Names field.
CliqueNames []string `json:"cliqueNames,omitempty"`
}
```

```go
// PodCliqueScalingGroupConfig is a group of PodClique's that are scaled together.
// Each member PodClique.Replicas will be computed as a product of PodCliqueScalingGroupConfig.Replicas and PodCliqueTemplateSpec.Spec.Replicas.
// NOTE: If a PodCliqueScalingGroupConfig is defined, then for the member PodClique's, individual AutoScalingConfig cannot be defined.
type PodCliqueScalingGroupConfig struct {
// Name is the name of the PodCliqueScalingGroupConfig. This should be unique within the PodCliqueSet.
// It allows consumers to give a semantic name to a group of PodCliques that needs to be scaled together.
Name string `json:"name"`
...
// ResourceClaimTemplateConfigs is a list of ResourceClaimTemplateConfig which defines a common set of
// resourcev1.ResourceClaimTemplate names for a set of PodCliques in the scaling group. A ResourceClaim is created per
// ResourceClaimTemplate name and added to the PodSpec of each PodClique specified in the CliqueNames field of the
// scaling group. This allows sharing of resources such as accelerators across all pods in the specified PodCliques
// that are part of one PodCliqueScalingGroup instance.
ResourceClaimTemplateConfigs []ResourceClaimTemplateConfig `json:"resourceClaimTemplateConfigs,omitempty"`
}
```

**Example:**

The following example demonstrates sharing resources across multiple PodCliques within a PodCliqueScalingGroup.

First, create a `ResourceClaimTemplate` for shared GPUs:

```yaml
apiVersion: resource.k8s.io/v1alpha3
kind: ResourceClaimTemplate
metadata:
  name: shared-gpu-template
  namespace: default
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
                strategy: MPS  # NVIDIA Multi-Process Service
                maxClients: 8
```

Then, reference it in a `PodCliqueSet` with a scaling group that shares GPUs across preprocessing and training pods:

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
      # Preprocessing PodClique
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
                    # Data preprocessing code here
                    sleep infinity
                resources:
                  requests:
                    cpu: "2"
                    memory: "4Gi"
            restartPolicy: Always
      
      # Training PodClique
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
                    # Training code here
                    sleep infinity
                resources:
                  requests:
                    cpu: "4"
                    memory: "8Gi"
            restartPolicy: Always
    
    # Define scaling group with shared resources
    scalingGroups:
      - name: training-experiment
        replicas: 3  # Creates 3 training experiments
        cliqueNames:
          - data-preprocessor
          - model-trainer
        # Share GPUs across both preprocessing and training pods within each experiment
        resourceClaimTemplateConfigs:
          - names:
              - shared-gpu-template
            cliqueNames:
              - data-preprocessor
              - model-trainer
```

In this example:
- The `shared-gpu-template` defines 4 GPUs with NVIDIA MPS for sharing
- 3 PCSG replicas create 3 independent training experiments
- Within each experiment (PCSG instance):
  - 2 preprocessing pods + 3 training pods = 5 total pods share the same 4 GPUs
  - All pods can access the same GPU memory space
- Each of the 3 experiments maintains isolation (different ResourceClaims, different GPU sets)



### Monitoring

<!--
This section contains details of events, metrics, status conditions and other status fields that will aid in determining health of the feature, or help measure any service level objectives that might be optionally defined.
-->

### Dependencies

Dynamic Resource Allocation (DRA) is a prerequisite for this GREP since it relies on ResourceClaim and
ResourceClaimTemplate APIs to enable resource sharing. DRA graduated to *BETA* in *v1.32* and has been promoted to *GA* since Kubernetes *v1.34*. If you are using a Kubernetes version prior to v1.34, you will need to enable the `DynamicResourceAllocation` feature gate to use this feature. For Kubernetes
v1.34 and above, DRA is enabled by default, and you can use this feature without any additional configuration.

### Test Plan

<!--
For the functionality an epic (issue) should be created. Along with a sub-issue for the GREP, there should be a dedicated issue created for integration and e2e tests. This issue should have details of all scenarios that needs to be tested. Provide a link to issue(s) in this section.
-->

## Appendix

In case the readers are not familiar with DRA, the following links will help them get started:
* [Kubernetes DRA Official Documentation](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
* [Dynamic Resource Allocation (DRA) KEP](https://github.com/kubernetes/enhancements/tree/master/keps/sig-node/4381-dra-structured-parameters)
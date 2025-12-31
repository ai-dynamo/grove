# MNNVL Support Design Doc \- Phase 1

# Overview

This design document details the plan to enable the Grove operator to automatically leverage MNNVL for appropriate workloads.

## Abbreviations

| Abbreviation | Full Name | Description |
|--------------|-----------|-------------|
| CD | ComputeDomain | NVIDIA CRD representing a logical GPU fabric spanning multiple nodes |
| RCT | ResourceClaimTemplate | Kubernetes resource template for dynamic resource allocation |
| PCS | PodCliqueSet | Grove CRD that manages a set of PodCliques and PodCliqueScalingGroups |
| PC | PodClique | Grove CRD representing a group of related pods |
| PCSG | PodCliqueScalingGroup | Grove CRD that manages scaling of PodCliques |

## Motivation

The MNNVL support feature in Grove is guided by three core design principles:

**Simplicity:** Deploying inference workloads with MNNVL in Kubernetes currently demands familiarity with complex concepts like ComputeDomain CRDs, ResourceClaimTemplates, and Dynamic Resource Allocation (DRA) APIs. Grove, however, must completely abstract this complexity, minimizing required configuration and hiding all underlying implementation details from the user.

**Portability:** The Grove Custom Resource Definitions (CRDs)—specifically PodCliqueSet, PodCliqueScalingGroup, and PodClique—are designed to be generic, excluding any direct references to MNNVL, ComputeDomain, or other NVIDIA-specific components.

To maintain portability, all MNNVL-related configuration is kept external, residing in the operator configuration and runtime annotations. This approach allows the same PodCliqueSet manifest to be used without alteration, regardless of whether the target cluster supports MNNVL.

**Graceful degradation:** MNNVL is treated as an optimization, not a hard requirement. If encountering a failure creating the MNNVL resources, Grove does not block the workload. Instead, pods are created without the RCT reference and are scheduled using standard GPU allocation mechanisms.

## Background

### What is MNNVL

**MNNVL (Multi-Node NVLink)** is an NVIDIA technology that extends NVLink-class, high-bandwidth GPU-to-GPU communication across **multiple physical nodes**, rather than being limited to GPUs within a single server. It uses specialized hardware and software mechanisms (such as IMEX for secure memory export/import) to allow GPUs on different nodes to access each other’s memory with much lower latency and higher bandwidth than traditional network-based approaches like TCP or even standard RDMA. In Kubernetes, MNNVL is exposed through NVIDIA’s DRA driver so distributed workloads (for example, large-scale training or tightly coupled inference) can treat GPUs across nodes as part of a single, high-performance compute fabric while preserving isolation and security between workloads.

### Using MNNVL in a K8S cluster

To use **MNNVL (Multi-Node NVLink)** in Kubernetes with NVIDIA’s DRA driver, you start by creating a **ComputeDomain (CD)**. The ComputeDomain represents a logical GPU fabric spanning multiple nodes and defines how inter-node NVLink/IMEX channels are allocated. the `ComputeDomainSpec.Channel` references a `ResourceClaimTemplate`; this template describes the DRA resource class (MNNVL) that will be used to provision the interconnect. When the ComputeDomain is created, the NVIDIA DRA controller prepares the underlying GPU fabric and ensures that nodes participating in the domain can securely communicate over MNNVL.

Next, pods that want to use MNNVL simply **reference the ResourceClaimTemplate** in their pod spec. Each pod declares a `resourceClaims` entry pointing to the template name, and the container lists that claim under `resources.claims`. Kubernetes then automatically creates a **ResourceClaim per pod** from the template. These per-pod claims are handed to the NVIDIA DRA driver, which allocates the necessary MNNVL/IMEX channels for each pod within the ComputeDomain.

As a result, multiple pods—possibly scheduled on different nodes—are joined into the same ComputeDomain and can communicate using **multi-node NVLink semantics** without manually wiring GPUs or fabric resources. The ComputeDomain handles reachability and isolation, the ResourceClaimTemplate enables automatic per-pod allocation, and the pod spec remains simple: create a `ComputeDomain` once, then reference its template in every pod that needs MNNVL.

#### ComputeDomain Status Lifecycle

When a `CD` is first created, it exists without an operational status—it is neither ready nor failed at this point. The `CD` only becomes active once pods referencing its `RCT` are scheduled. At that point, the NVIDIA DRA driver deploys a `DaemonSet` on the nodes where those pods are scheduled to establish the MNNVL fabric. The `CD`'s status is then derived from the aggregate health of these DaemonSet pods: if all pods are healthy, the `CD` reports as ready; otherwise, it reflects a degraded or failed state.

# Goals

The following key goals, derived from the requirements document, guide the MNNVL design:
* **Homogeneous Cluster Support:** The feature will only support clusters with the exact same type of GPUs on all nodes. (AKA homogeneous cluster)
  * Grove does not validate or enforce cluster homogeneity—it is the cluster admin's responsibility to enable this feature only on clusters that meet this requirement. Enabling MNNVL on heterogeneous clusters may result in undefined scheduling behavior.
* **Global Configuration:** A global configuration for the feature is required. If the cluster does not support MNNVL, the Grove Operator will exit with a non-zero exit code.
* **Opt-Out***
  * An immutable, granular-level opt-out option must be available in the PCS.
* **Workload Impact:**
  * Failure to create MNNVL resources must not prevent the workload from running.
  * Enabling/Disabling of MNNVL feature should not impact a currently running workload.
* **ComputeDomain Management:**
  * Each PCS replica must have its own dedicated ComputeDomain.
  * The lifecycle of the ComputeDomain is tied directly to the lifecycle of the PCS replica.

## Scope and Limitations

This document covers **Phase 1** of MNNVL support in Grove. See GREP-270 for the full requirements.

**Limitations:**

- **Homogeneous Clusters Only:** This phase supports only homogeneous clusters where all nodes have identical GPU types and NVLink topology. Grove does not validate or enforce cluster homogeneity—it is the cluster administrator's responsibility to enable this feature only on clusters that meet this requirement. Enabling MNNVL on heterogeneous clusters may result in undefined scheduling behavior.

- **PCS-Level Granularity:** The MNNVL feature is applied at the PCS level—it cannot be targeted to individual PCs or PCSGs within a PCS. Either all pods in a replica receive the RCT reference, or none do.

# Design Details

## Enabling the feature

Enabling and disabling the feature will be done by the cluster admin by setting a flag in the Grove OperatorConfiguration.

```go
// MNNVLConfiguration defines the configuration for MNNVL (Multi-Node NVLink) support.
type MNNVLConfiguration struct {
   // Enabled indicates whether MNNVL support is enabled.
   // When true, the operator validates that the ComputeDomain CRD is installed at startup.
   // When MNNVL support is enabled, cluster admin should ensure that the ComputeDomain CRD has been installed.
   // If this prerequisite fails then Grove will exit with a non-zero exit code.
   Enabled bool `json:"enabled"`
}
```

The value could be set from a Helm chart under the config attribute

```yaml
config:
	mnnvl:
		enabled: false
```

### Feature validity 

When the Grove operator starts, it will check for MNNVL support (only if the feature is enabled). Support is confirmed by the presence of the ComputeDomain CRD in the cluster.

If the cluster lacks MNNVL support, the Grove operator will terminate and log an appropriate error.

## ComputeDomain lifecycle management

### Creation

The PCS controller has a reconciliation flow for managing resources in a specific order. The `ComputeDomain` component is synced **before** creating PCs and PCSGs, ensuring the CD status is available when the PC is created.

Before creating the `CD`, the controller checks the following criteria:

1. The MNNVL feature is enabled in Grove's `OperatorConfiguration`.
2. The PCS has not opted out via the `grove.io/mnnvl-enabled: "false"` annotation.
3. At least one PC in the PCS requires a GPU.

If all criteria are met, the controller creates a `ComputeDomain` resource for each PCS replica. The CD is named `{pcs-name}-cd-{replica-index}` and references an RCT named `{pcs-name}-rct-{replica-index}`. The PCS is set as the owner reference to enable automatic garbage collection.

```yaml
apiVersion: resource.nvidia.com/v1beta1
kind: ComputeDomain
metadata:
  name: my-pcs-cd-0
  labels:
    grove.io/podcliqueset: my-pcs
    grove.io/replica-index: "0"
  ownerReferences:
    - apiVersion: grove.io/v1alpha1
      kind: PodCliqueSet
      name: my-pcs
      controller: true
spec:
  channel:
    resourceClaimTemplateName: my-pcs-rct-0
```

### Status

The PCS only reports CD creation **failures** in its status. If all CDs are created successfully, no condition is set—this keeps the status clean, especially for PCS resources with many replicas.

When a failure occurs, the controller sets a `ComputeDomainsReady` condition with `status: False`, listing the failed replica indices and error details:

```yaml
status:
  conditions:
    - type: ComputeDomainsReady
      status: "False"
      reason: "CreationFailed"
      message: "ComputeDomain creation failed for replicas: [2, 5]. Error: quota exceeded"
```

If no `ComputeDomainsReady` condition exists, it means either:
1. All CDs were created successfully, or
2. No CDs were needed (feature disabled or PCS opted out)

The PC can determine which case applies by checking the MNNVL criteria (feature enabled, not opted out, GPU required).

#### Alternatives Considered

**Alt option 1: Condition per replica**

Track each replica's CD status as a separate condition (e.g., `ComputeDomainReady-0`, `ComputeDomainReady-1`).

*Rejected because:* Status becomes bloated with many replicas (e.g., 100 replicas = 100 conditions).

**Alt option 2: Dedicated status structure**

Add a `ComputeDomainStatuses` map to PodCliqueSetStatus tracking each replica's CD state.

*Rejected because:* Ties the MNNVL feature to the Grove API, violating the portability goal.

### Watching the compute domain

To ensure the PCS controller reconciles when a `ComputeDomain` is deleted, a watch must be added in the PCS controller's RegisterWithManager function. 

When the controller creates a `ComputeDomain` resource, it should attach the label `grove.io/podcliqueset` with the value `<pcs-name>` to identify which PCS owns it. 
```yaml
apiVersion: v1beta1
kind: ComputeDomain
metadata:
 labels:
   grove.io/podcliqueset: <pcs-name>
```

As part of the registration process, a new watch will added to enqueue events for `ComputeDomain` created by the controller (using the `grove.io/podcliqueset`label as a filter).

### Scale-Out and Scale-In

When scaling out (replicas increased), the subsequent reconciliation process will identify the ComputeDomains missing for the new replica indices and create them using the identical logic as the initial creation.

When scaling in (replicas decreased), the PCS controller determines which `ComputeDomains` to delete by comparing the existing ones against the new replica count. Any `ComputeDomain` with a replica index equal to or greater than the new count is removed. This process mirrors the "desired versus existing" logic used for managing `PodClique` and `PCSG`.

The controller lists existing `ComputeDomains` by label selector, computes expected resources from the current spec, and deletes the excess. The `ComputeDomainsReady` condition is updated to reflect the new state after scale operations.

### PCS Deletion  
When a PodCliqueSet is deleted, all ComputeDomain resources are automatically garbage-collected by Kubernetes through the owner reference mechanism. 

Since each ComputeDomain has controllerReference set to the PCS, Kubernetes will cascade-delete them when the PCS is removed. No explicit cleanup logic is required in the controller for this case.

## PCS opt-out

Users can opt-out of MNNVL support for a specific `PodCliqueSet` using the `grove.io/mnnvl-enabled` annotation. 

When the global MNNVL feature is enabled in the `OperatorConfiguration`, individual workloads inherit this setting by default. To disable MNNVL for a specific PCS, set the annotation to "`false`".  

When opting out, the operator will not create a `ComputeDomain` for that PCS.

This annotation is validated as immutable by the PCS validation webhook, meaning that once a PCS is created with or without the annotation, it cannot be changed or removed. This ensures consistent behavior throughout the lifecycle of the workload.

## PC and PCSG Creation

When the PCS controller creates a PC or PCSG for a replica, it checks whether a CD was successfully created for that replica. If a CD exists, the controller adds an annotation to the PC/PCSG with the RCT name. This annotation is immutable and ensures all pods within that PC/PCSG have a consistent configuration throughout their lifecycle.

**Annotation:**
- **Key:** `grove.io/compute-domain-rct`
- **Value:** The RCT name for the replica (e.g., `my-pcs-rct-0`)

If no CD exists for the replica (either due to creation failure, feature disabled, or PCS opt-out), the annotation is not added.

```yaml
apiVersion: grove.io/v1alpha1
kind: PodClique
metadata:
  name: my-pcs-0-worker
  annotations:
    grove.io/compute-domain-rct: "my-pcs-rct-0"  # Present only if CD exists
  labels:
    grove.io/podcliqueset: my-pcs
    grove.io/replica-index: "0"
```

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueScalingGroup
metadata:
  name: my-pcs-0-scaling
  annotations:
    grove.io/compute-domain-rct: "my-pcs-rct-0"  # Present only if CD exists
  labels:
    grove.io/podcliqueset: my-pcs
    grove.io/replica-index: "0"
```

When a PCSG creates PCs, it propagates the `grove.io/compute-domain-rct` annotation to its child PCs.

## Pod Reconciliation

When the PC controller reconciles pods, the Pod component checks its parent PC for the `grove.io/compute-domain-rct` annotation:

- **If the annotation exists:** The Pod is created with a `resourceClaims` entry referencing the RCT name from the annotation.
- **If the annotation is absent:** The Pod is created without an RCT reference, allowing the workload to run on any available GPUs (without NVLink optimization).

Since the annotation is set at PC creation time and is immutable, all pods within a PC have a consistent configuration throughout their lifecycle—even if the MNNVL feature is later disabled or the CD is deleted and recreated.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: my-pcs-0-worker-pod-0
  labels:
    grove.io/podcliqueset: my-pcs
    grove.io/podclique: my-pcs-0-worker
spec:
  resourceClaims:
    - name: mnnvl-claim
      resourceClaimTemplateName: my-pcs-rct-0  # From PC annotation
  containers:
    - name: inference
      image: my-inference-image
      resources:
        claims:
          - name: mnnvl-claim
```

If the PC does not have the `grove.io/compute-domain-rct` annotation, the pod is created without the `resourceClaims` section.
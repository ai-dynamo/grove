# MNNVL Support Design Doc \- Phase 1

# Overview

This design document details the plan to enable the Grove operator to automatically leverage MNNVL for appropriate workloads.

## Abbreviations

| Abbreviation | Full Name | Description |
|--------------|-----------|-------------|
| CD | ComputeDomain | NVIDIA CRD representing a logical GPU fabric spanning multiple nodes |
| RCT | ResourceClaimTemplate | Kubernetes resource template for dynamic resource allocation |
| PCS | PodCliqueSet | Grove CRD that manages a set of PodCliques and PodCliqueScalingGroups |
| PCLQ | PodClique | Grove CRD representing a group of related pods |
| PCSG | PodCliqueScalingGroup | Grove CRD that manages scaling of PodCliques |

## Motivation

The MNNVL support feature in Grove is guided by three core design principles:

**Simplicity:** Currently, Kubernetes workloads can take advantage of NVIDIA MNNVL accelerated hardware by explicitly adding NVIDIA ComputeDomain APIs to their workloads. With this feature, when applicable, Grove will abstract the MNNVL hardware from users by automatically creating ComputeDomain resources, enabling ComputeDomains support to workloads, and managing the lifecycle of the CRs.

Power users who require custom ComputeDomain can opt-out of automatic MNNVL management and specify their own `resourceClaims` directly in the pod spec template.

**Portability:** The Grove Custom Resource Definitions (CRDs)—specifically PodCliqueSet, PodCliqueScalingGroup, and PodClique—are designed to be generic, excluding any direct references to MNNVL, ComputeDomain, or other NVIDIA-specific components.

To maintain portability, all MNNVL-related configuration is kept external, residing in the operator configuration and runtime annotations. This approach allows the same PodCliqueSet manifest to be used without alteration, regardless of whether the target cluster supports MNNVL.

**Standard Kubernetes behavior:** ComputeDomain lifecycle follows the same reconciliation pattern as other Grove-managed resources. If CD creation fails (due to transient cluster issues), the sync stops and requeues for retry. Persistent failures are surfaced via Kubernetes Events, allowing cluster administrators to investigate.

## Background

### What is MNNVL?

**MNNVL (Multi-Node NVLink)** is an NVIDIA technology that extends NVLink-class, high-bandwidth GPU-to-GPU communication across **multiple physical nodes**, rather than being limited to GPUs within a single server. It uses specialized hardware and software mechanisms to allow GPUs on different nodes to access each other's memory with much lower latency and higher bandwidth than traditional network-based approaches like TCP or even standard RDMA. In Kubernetes, MNNVL is exposed through NVIDIA's DRA driver so distributed workloads (for example, large-scale training or tightly coupled inference) can treat GPUs across nodes as part of a single, high-performance compute fabric while preserving isolation and security between workloads.

### IMEX Channels

**IMEX (Import/Export)** is the underlying mechanism that enables secure GPU memory sharing across nodes in an MNNVL fabric. Each IMEX channel represents a logical connection that allows GPUs on different nodes to import and export memory regions for direct access.

**Key constraint: One IMEX channel per node.** Each node can participate in only **one** IMEX channel at a time. This means:

- A node's GPUs can only belong to a single ComputeDomain simultaneously
- Multiple independent MNNVL-enabled workloads cannot share the same node
- When a node is allocated to one ComputeDomain, its NVLink fabric participation is exclusive to that domain

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
* **Opt-Out**
  * An immutable, granular-level opt-out option must be available in the PCS.
  * Future phases may extend opt-out support to PCLQ or PCSG levels.
* **Workload Impact:**
  * Enabling/Disabling of MNNVL feature should not impact a currently running workload.
* **ComputeDomain Management:**
  * Each PCS replica must have its own dedicated ComputeDomain.
  * The lifecycle of the ComputeDomain is tied directly to the lifecycle of the PCS replica.

## Scope and Limitations

This document covers **Phase 1** of MNNVL support in Grove. See GREP-270 for the full requirements.

**Limitations:**

- **Homogeneous Clusters Only:** This phase supports only homogeneous clusters where all nodes have identical GPU types and NVLink topology. Grove does not validate or enforce cluster homogeneity—it is the cluster administrator's responsibility to enable this feature only on clusters that meet this requirement. Enabling MNNVL on heterogeneous clusters may result in undefined scheduling behavior.

- **PCS-Level Granularity (Phase 1):** The MNNVL feature is applied at the PCS level—it cannot be targeted to individual PCLQs or PCSGs within a PCS. Either all GPU-requiring pods in a replica receive the RCT reference, or none do. Non-GPU PCLQs (those without `nvidia.com/gpu` requests) are excluded from RCT referencing. This is a Phase 1 limitation; the design does not preclude adding PCLQ or PCSG-level opt-out in future phases.

- **No ComputeDomain Customization:** The ComputeDomain and ResourceClaimTemplate configurations are automatically generated by Grove and cannot be customized. Power users who require custom configurations can opt-out of automatic MNNVL management and specify their own `resourceClaims` directly in the pod spec template.

- **No ComputeDomain Status Propagation:** Grove does not surface the ComputeDomain's operational status in the PCS status fields. This is because a ComputeDomain has no meaningful status at creation time—it only becomes active after pods referencing its RCT are scheduled, at which point the NVIDIA DRA driver deploys a DaemonSet to establish the MNNVL fabric. Users should inspect the ComputeDomain resource directly (`kubectl get computedomain`) to check fabric health.

- **One IMEX Channel Per Node:** Each node can participate in only one IMEX channel at a time. This means a node's GPUs can only belong to a single ComputeDomain simultaneously, limiting multi-tenancy for MNNVL-enabled workloads. If multiple workloads require MNNVL, they cannot share nodes—each must be scheduled on exclusive node sets. This may lead to reduced cluster utilization when running multiple independent MNNVL workloads.

# Design Details

## Enabling the feature

Enabling and disabling the feature will be done by the cluster admin by setting a flag in the Grove OperatorConfiguration.

```go
// NetworkAcceleration defines the configuration for network acceleration features.
type NetworkAcceleration struct {
   // AutoMNNVLEnabled indicates whether automatic MNNVL (Multi-Node NVLink) support is enabled.
   // When true, the operator validates that the ComputeDomain CRD is installed at startup
   // and automatically creates ComputeDomain resources for GPU workloads.
   // When MNNVL support is enabled, cluster admin should ensure that the ComputeDomain CRD has been installed.
   // If this prerequisite fails then Grove will exit with a non-zero exit code.
   // Default: false
   AutoMNNVLEnabled bool `json:"autoMNNVLEnabled"`
}
```

The default value of `AutoMNNVLEnabled` is `false`, meaning MNNVL support is disabled unless explicitly enabled by the cluster administrator.

The value could be set from a Helm chart under the config attribute

```yaml
config:
  network:
    autoMNNVLEnabled: false
```

> **Note:** Using the `OperatorConfiguration` for feature enablement is chosen for simplicity in Phase 1. However, a plugin-based approach would provide better decoupling between the MNNVL feature and Grove core, and should be considered for future phases.

### Feature validity 

When the Grove operator starts, it will check for MNNVL support (only if the feature is enabled). Support is confirmed by the presence of the ComputeDomain CRD in the cluster.

If the cluster lacks MNNVL support, the Grove operator will terminate and log an appropriate error.

### PCS MNNVL Eligibility Determination

When a PodCliqueSet is created, webhooks determine and enforce the MNNVL enablement status.

#### Mutating Webhook (on Create)

The mutating webhook adds the `grove.io/auto-mnnvl: "enabled"` annotation **only** when all conditions are met:

1. Annotation does not already exist
2. MNNVL feature is enabled in `OperatorConfiguration`
3. PCS has at least one container requesting GPU (`nvidia.com/gpu`)

If any condition is false, **no annotation is added**.

```yaml
# User submits PCS without annotation (feature enabled, has GPU)
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: my-pcs
spec:
  replicas: 2
  template:
    cliques:
      - name: worker
        spec:
          podSpec:
            containers:
              - name: train
                resources:
                  limits:
                    nvidia.com/gpu: "8"

# After mutation webhook:
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: my-pcs
  annotations:
    grove.io/auto-mnnvl: "enabled"  # Added by webhook
spec:
  # ... same spec
```

#### Validating Webhook (on Create)

A validating webhook runs **on PCS creation only** to reject invalid MNNVL configurations:

- **Reject** if annotation value is not `"enabled"` or `"disabled"` (e.g., `"true"`, `"false"`, empty string)
- **Reject** if `grove.io/auto-mnnvl: "enabled"` is set but MNNVL feature is **disabled** globally

This prevents users from explicitly requesting MNNVL when the cluster doesn't support it, and ensures only valid annotation values are accepted.

#### Validating Webhook (on Update)

A validating webhook ensures the `grove.io/auto-mnnvl` annotation is **immutable** after PCS creation. Any attempt to add, modify, or remove the annotation on an existing PCS is rejected.

#### Opt-out Behavior

Users can opt-out of MNNVL for a specific PCS by explicitly setting `grove.io/auto-mnnvl: "disabled"` **before creation**. When the mutating webhook sees the annotation already exists, it does not override it.

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: my-pcs
  annotations:
    grove.io/auto-mnnvl: "disabled"  # Explicit opt-out
spec:
  # ... GPU workload that won't use MNNVL
```

When opting out, the operator will not create a `ComputeDomain` for that PCS.

## ComputeDomain lifecycle management

### Creation

The PCS controller has a reconciliation flow for managing resources in a specific order. The `ComputeDomain` component is synced **before** creating PCLQs and PCSGs, ensuring the CD exists before pods that reference it are created.

Before creating the `CD`, the controller checks the `grove.io/auto-mnnvl` annotation on the PCS:

- If `grove.io/auto-mnnvl: "enabled"` → Create ComputeDomains for each replica
- If `grove.io/auto-mnnvl: "disabled"` or annotation is absent → Skip ComputeDomain creation

Since the annotation is set by the mutating webhook at PCS creation time (based on feature enablement and GPU requirements), the controller logic is simplified to a single annotation check.

**Backward Compatibility:** Existing PCS resources created before the MNNVL feature was deployed will not have the `grove.io/auto-mnnvl` annotation. These workloads will continue to operate without ComputeDomains, even after the feature is enabled globally. To enable MNNVL for an existing workload, the PCS must be deleted and recreated.

If MNNVL is enabled, the controller creates a `ComputeDomain` resource for each PCS replica. The CD is named `{pcs-name}-{replica-index}` and references an RCT with the same name. The PCS is set as the owner reference to enable automatic garbage collection. The `app.kubernetes.io/part-of` and `grove.io/podcliqueset-replica-index` labels will be used to determine which ComputeDomain is associated with a specific PCS replica. 

```yaml
apiVersion: resource.nvidia.com/v1beta1
kind: ComputeDomain
metadata:
  name: my-pcs-0
  labels:
    app.kubernetes.io/managed-by: grove
    app.kubernetes.io/part-of: my-pcs
    app.kubernetes.io/component: pcs-computedomain
    grove.io/podcliqueset-replica-index: "0"
  ownerReferences:
    - apiVersion: grove.io/v1alpha1
      kind: PodCliqueSet
      name: my-pcs
      controller: true
spec:
  channel:
    resourceClaimTemplateName: my-pcs-0
```

### Observability

ComputeDomain creation follows the same observability pattern as other Grove-managed resources:

- **Kubernetes Events:** Success and failure events are emitted on the PCS resource.
  ```
  kubectl describe pcs my-pcs
  # Events:
  #   Normal   ComputeDomainCreated   ComputeDomain my-pcs-0 created
  #   Warning  ComputeDomainFailed    Failed to create ComputeDomain for replica 2: <error>
  ```

- **Logs:** Detailed error information is logged by the operator.

- **Requeue on failure:** If CD creation fails, the sync stops and requeues for retry. PCLQ and PCSG creation only proceeds after all CDs for the replicas have been successfully created.

### Protecting the ComputeDomain

Deleting a ComputeDomain while pods are actively using its RCT causes significant workload disruption. To prevent accidental deletion, Grove adds a **finalizer** to each ComputeDomain it creates.

**Finalizer:** `grove.io/computedomains.resource.nvidia.com`

With this finalizer, a ComputeDomain cannot be deleted until Grove explicitly removes the finalizer. This provides stronger protection than a watch-and-recreate approach, which would leave a gap where the workload is in a broken state.

```yaml
apiVersion: resource.nvidia.com/v1beta1
kind: ComputeDomain
metadata:
  name: my-pcs-0
  finalizers:
    - grove.io/computedomains.resource.nvidia.com
  labels:
    app.kubernetes.io/managed-by: grove
    app.kubernetes.io/part-of: my-pcs
  ownerReferences:
    - apiVersion: grove.io/v1alpha1
      kind: PodCliqueSet
      name: my-pcs
      controller: true
spec:
  channel:
    resourceClaimTemplateName: my-pcs-0
```

**Finalizer removal occurs when:**
- **PCS is deleted** → Grove removes the finalizer during cleanup, allowing Kubernetes to garbage-collect the CD
- **Scale-in** → The replica is removed, Grove removes the finalizer for that CD before deleting it

If a user attempts to delete a CD manually, it will remain in `Terminating` state until the PCS is deleted or scaled down.

> **Note:** The finalizer name `grove.io/computedomains.resource.nvidia.com` follows the pattern `grove.io/{crd-name}` to clearly indicate which CRD is being protected.

### Scale-Out and Scale-In

When scaling out (replicas increased), the subsequent reconciliation process will identify the ComputeDomains missing for the new replica indices and create them using the identical logic as the initial creation.

When scaling in (replicas decreased), the PCS controller determines which `ComputeDomains` to delete by comparing the existing ones against the new replica count. Any `ComputeDomain` with a replica index equal to or greater than the new count is removed. The controller removes the finalizer before deleting the CD.

The controller lists existing `ComputeDomains` by label selector, computes expected resources from the current spec, and deletes the excess.

### PCS Deletion  

When a PodCliqueSet is deleted, the PCS controller's finalizer logic removes the `grove.io/protect-computedomain` finalizer from all owned ComputeDomains. Once the finalizer is removed, Kubernetes garbage-collects the CDs through the owner reference mechanism.

## PCLQ Creation and RCT Injection

The `resourceClaims` reference is injected into the PCLQ's pod spec template at **PCLQ creation time**, not at pod creation time. This ensures the decision is made once and baked into the PCLQ spec.

### PCS Creating PCLQ

When the PCS controller creates a PCLQ, it checks:
1. Does the PCS have `grove.io/auto-mnnvl: "enabled"`?
2. Does the PCLQ's pod spec require GPU (`nvidia.com/gpu`)?

If both conditions are true, the controller injects `resourceClaims` into the PCLQ's pod spec template before creation:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodClique
metadata:
  name: my-pcs-0-worker
  labels:
    app.kubernetes.io/part-of: my-pcs
    grove.io/podcliqueset-replica-index: "0"
spec:
  podSpec:
    resourceClaims:
      - name: mnnvl-claim
        resourceClaimTemplateName: my-pcs-0  # {pcs-name}-{replica-index}
    containers:
      - name: train
        image: my-training-image
        resources:
          limits:
            nvidia.com/gpu: "8"
          claims:
            - name: mnnvl-claim
```

### PCS Creating PCSG

When the PCS controller creates a PCSG, it propagates the `grove.io/auto-mnnvl` annotation to the PCSG:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueScalingGroup
metadata:
  name: my-pcs-0-scaling
  annotations:
    grove.io/auto-mnnvl: "enabled"  # Propagated from PCS
  labels:
    app.kubernetes.io/part-of: my-pcs
    grove.io/podcliqueset-replica-index: "0"
spec:
  # ... scaling group spec
```

### PCSG Creating PCLQ

When the PCSG controller creates a PCLQ, it uses the **same injection logic** as the PCS controller:
1. Check if PCSG has `grove.io/auto-mnnvl: "enabled"` annotation
2. Check if the PCLQ's pod spec requires GPU

If both are true, inject `resourceClaims` into the PCLQ's pod spec template.

### Pod Creation

The Pod controller requires **no special logic** for MNNVL. It simply creates pods using the PCLQ's pod spec template as-is. If the PCLQ has `resourceClaims` in its spec, the pods will have them too.

### Why This Works

The key to consistency is **early binding** at PCLQ creation time:

- The PCS annotation is set **once** at creation and cannot be changed
- The annotation is propagated to PCSG at creation time
- `resourceClaims` are injected into PCLQ's pod spec at PCLQ creation time
- Pods are created from the PCLQ spec without any additional logic
- Enabling/disabling the feature globally does **not** affect existing resources

This aligns with the goal: *"Enabling/Disabling of MNNVL feature should not impact a currently running workload."*
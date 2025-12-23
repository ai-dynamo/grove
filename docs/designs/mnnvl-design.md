# MNNVL Support Design Doc \- Phase 1

# Overview

This design document details the plan to enable the Grove operator to automatically leverage MNNVL for appropriate workloads.

## Motivation

The MNNVL support feature in Grove is guided by three core design principles:

**Simplicity:** Deploying inference workloads with MNNVL in Kubernetes currently demands familiarity with complex concepts like ComputeDomain CRDs, ResourceClaimTemplates, and Dynamic Resource Allocation (DRA) APIs. Grove, however, must completely abstract this complexity, minimizing required configuration and hiding all underlying implementation details from the user.

**Portability:** The Grove Custom Resource Definitions (CRDs)—specifically PodCliqueSet, PodCliqueScalingGroup, and PodClique—are designed to be generic, excluding any direct references to MNNVL, ComputeDomain, or other NVIDIA-specific components.

To maintain portability, all MNNVL-related configuration is kept external, residing in the operator configuration and runtime annotations. This approach allows the same PodCliqueSet manifest to be used without alteration, regardless of whether the target cluster supports MNNVL.

**Failure Resilience:** MNNVL is treated as an optimization, not a hard requirement. If encountering a failure creating the MNNVL resources, Grove does not block the workload. Instead, pods are created without the RCT reference and are scheduled using standard GPU allocation mechanisms.

## Background

### What is MNNVL

**MNNVL (Multi-Node NVLink)** is an NVIDIA technology that extends NVLink-class, high-bandwidth GPU-to-GPU communication across **multiple physical nodes**, rather than being limited to GPUs within a single server. It uses specialized hardware and software mechanisms (such as IMEX for secure memory export/import) to allow GPUs on different nodes to access each other’s memory with much lower latency and higher bandwidth than traditional network-based approaches like TCP or even standard RDMA. In Kubernetes, MNNVL is exposed through NVIDIA’s DRA driver so distributed workloads (for example, large-scale training or tightly coupled inference) can treat GPUs across nodes as part of a single, high-performance compute fabric while preserving isolation and security between workloads.

### Using MNNVL in a K8S cluster

To use **MNNVL (Multi-Node NVLink)** in Kubernetes with NVIDIA’s DRA driver, you start by creating a **ComputeDomain (CD)**. The ComputeDomain represents a logical GPU fabric spanning multiple nodes and defines how inter-node NVLink/IMEX channels are allocated. In its `spec.channel`, the ComputeDomain references a **ResourceClaimTemplate**; this template describes the DRA resource class (MNNVL) that will be used to provision the interconnect. When the ComputeDomain is created, the NVIDIA DRA controller prepares the underlying GPU fabric and ensures that nodes participating in the domain can securely communicate over MNNVL.

Next, pods that want to use MNNVL simply **reference the ResourceClaimTemplate** in their pod spec. Each pod declares a `resourceClaims` entry pointing to the template name, and the container lists that claim under `resources.claims`. Kubernetes then automatically creates a **ResourceClaim per pod** from the template. These per-pod claims are handed to the NVIDIA DRA driver, which allocates the necessary MNNVL/IMEX channels for each pod within the ComputeDomain.

As a result, multiple pods—possibly scheduled on different nodes—are joined into the same ComputeDomain and can communicate using **multi-node NVLink semantics** without manually wiring GPUs or fabric resources. The ComputeDomain handles reachability and isolation, the ResourceClaimTemplate enables automatic per-pod allocation, and the pod spec remains simple: create a CD once, then reference its template in every pod that needs MNNVL.

# Goals

The following key goals, derived from the requirements document, guide the MNNVL design:
* **Cluster Support:** The feature will only support homogeneous clusters.
* **Global Configuration:** A global configuration for the feature is required. If the cluster does not support MNNVL, the operator must exit.
* **Opt-Out and Workload Impact:**
  * An immutable, granular-level opt-out option must be available in the PCS.
  * Failure to create MNNVL resources must not prevent the workload from running.
  * Changes to the feature configuration should not impact a currently running workload.
* **ComputeDomain Management:**
  * Each PCS replica must have its own dedicated ComputeDomain.
  * The lifecycle of the ComputeDomain is tied directly to the lifecycle of the PCS replica.

# Design Details

## Enabling the feature

Enabling and disabling the feature will be done by the cluster admin by setting a flag in the Grove OperatorConfiguration.

```go
// MNNVLConfiguration defines the configuration for MNNVL (Multi-Node NVLink) support.
type MNNVLConfiguration struct {
   // Enabled indicates whether MNNVL support is enabled.
   // When true, the operator validates that the ComputeDomain CRD is installed at startup.
   // If validation fails, the operator will exit with an error.
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

If the cluster lacks MNNVL support, the Grover operator will terminate and log an appropriate error.

## ComputeDomain life-cycle management

### Creation

The PodCliqueSet (PCS) controller determines if a ComputeDomain is necessary during reconciliation, based on these criteria:

1. The feature is activated within Grove’s `OperatorConfiguration`.  
2. The PCS has not explicitly opted out of the feature.  
3. At least one contained PodClique (PC) requires a GPU.

The controller will create a ComputeDomain resource for each PCS replica if one does not already exist. This resource will be named `{pcs-name}-cd-{replica-index}` and will have the PCS set as its owner reference.

### Status

After processing all replicas, the component updates a single ComputeDomainsReady condition on the PCS status—set to True with reason AllCreated if all succeeded, or False with reason PartialFailure and a message listing the failed replica indices if any creation failed.

For example

```yaml
status:
  conditions:
    - type: ComputeDomainsReady
      status: "True"   # All replicas succeeded
      reason: "AllCreated"
      message: "ComputeDomains created for all 3 replicas"
```

```yaml
status:
  conditions:
    - type: ComputeDomainsReady
      status: "False"
      reason: "PartialFailure"
      message: "ComputeDomain creation failed for replicas: [2]. Error: quota exceeded"

```

#### Alt option 1: Manage a condition per replica

```yaml
status:
  conditions:
    - type: ComputeDomainReady-0
      status: "True"
      reason: "Created"
      message: "my-workload-cd-0"
    - type: ComputeDomainReady-1  
      status: "True"
      reason: "Created"
      message: "my-workload-cd-1"
    - type: ComputeDomainReady-2
      status: "False"
      reason: "CreationFailed"
      message: "Failed to create: quota exceeded"
```

Pro:

* Per-replica granularity  
* Easy look up

Cons:

* Dynamic type names (less common but valid)  
* Need to clean up conditions on scale-down

#### Alt option 2: Use a desiccated status structure (not recommended)

```go
type PodCliqueSetStatus struct {
    // ... existing fields ...
    
    // ComputeDomainStatuses tracks the status of ComputeDomain per replica.
    // Key is the replica index as string ("0", "1", "2", ...)
    // +optional
    ComputeDomainStatuses map[string]ComputeDomainStatus `json:"computeDomainStatuses,omitempty"`
}

type ComputeDomainStatus struct {
    // Name is the name of the ComputeDomain resource
    Name string `json:"name"`
    // Ready indicates if the ComputeDomain was successfully created
    Ready bool `json:"ready"`
    // Reason provides the status reason
    // +optional
    Reason string `json:"reason,omitempty"`
    // Message provides additional details (e.g., error message)
    // +optional  
    Message string `json:"message,omitempty"`
    // LastUpdated is when this status was last updated
    // +optional
    LastUpdated *metav1.Time `json:"lastUpdated,omitempty"`
}

```

Pro:

* A well-defined structure makes everything clearer.

Cons:

* Ties the feature to Grove.

### Watching the compute domain

To ensure the PCS controller reconciles when a `ComputeDomain` is deleted or its status changes, a watch must be added in the PCS controller's RegisterWithManager function. 

When the controller creates a `ComputeDomain` resource, it should attach the label `grove.io/podcliqueset` with the value `<pcs-name>` to identify which PCS owns it. 

As part of the registration process, a new watch will be used to garud the `ComputeDomain` created by the controller (using the `grove.io/podcliqueset`label as a filter).

```yaml
apiVersion: v1beta1
kind: ComputeDomain
metadata:
 labels:
   grove.io/podcliqueset: <pcs-name>
```

### Scale-Out and Scale-In

When scaling out (replicas increased), the subsequent reconciliation process will identify the ComputeDomains missing for the new replica indices and create them using the identical logic as the initial creation.

When scaling in( replicas decreased), the PCS controller determines which `ComputeDomains` to delete by comparing the existing ones against the new replica count. Any `ComputeDomain` with a replica index equal to or greater than the new count is removed. This process mirrors the "desired versus existing" logic used for managing `PodClique` and `PCSG`.

The controller lists existing `ComputeDomains` by label selector, computes expected resources from the current spec, and deletes the excess. The `ComputeDomainsReady` condition is updated to reflect the new state after scale operations.

PCS Deletion  
When a PodCliqueSet is deleted, all ComputeDomain resources are automatically garbage-collected by Kubernetes through the owner reference mechanism. 

Since each ComputeDomain has controllerReference set to the PCS, Kubernetes will cascade-delete them when the PCS is removed. No explicit cleanup logic is required in the controller for this case.

## PCS opt-out

Users can opt-out of MNNVL support for a specific `PodCliqueSet` using the `grove.io/mnnvl-enabled` annotation. 

When the global MNNVL feature is enabled in the `OperatorConfiguration`, individual workloads inherit this setting by default. To disable MNNVL for a specific PCS, set the annotation to "`false`".  

When opting out, the operator will not create a `ComputeDomain` for that PCS.

This annotation is validated as immutable by the PCS validation webhook, meaning that once a PCS is created with or without the annotation, it cannot be changed or removed. This ensures consistent behavior throughout the lifecycle of the workload.

## Pod reconciliation

As described above, before the PCS controller creates a `PodClique` for a replica, it first creates a `ComputeDomain` for the replica. It waits for it to become ready (with a 3-minute timeout).   
The final `ComputeDomain` status is captured in the `PodClique`'s status conditions and is **never** overwritten afterward, ensuring all pods within the PodClique have a consistent configuration.

```yaml
apiVersion: grove.io/v1alpha1
kind: PodClique
metadata:
  name: my-pcs-replica-0-worker
  labels:
    grove.io/podcliqueset: my-pcs
    grove.io/replica-index: "0"
status:
  conditions:
    - type: ComputeDomainReady
      status: "True"              # or "False" if CD failed/timed out
      reason: Ready               # or "Timeout" / "AllocationFailed"
      message: "ComputeDomain my-pcs-replica-0 is ready"
      lastTransitionTime: "2024-01-15T10:00:00Z"
```

When the `PodClique` controller reconciles pods, the Pod component checks its parent PodClique's ComputeDomainReady condition. If the condition is True, the Pod is created with a resourceClaims entry that references the ComputeDomain's ResourceClaimTemplate (RCT). 

If the condition is False, the Pod is created without the RCT reference, allowing the workload to run on any available GPUs (without the NVLink optimization).

The RCT name follows a deterministic naming convention based on the PCS name and replica index: {pcs-name}-replica-{index}-rct. This allows the Pod component to derive the correct RCT name without querying the ComputeDomain directly.
# Auto MNNVL (Multi-Node NVLink)

Grove can automatically enable Multi-Node NVLink (MNNVL) acceleration without requiring any manual configuration. This guide explains how to enable, use, and manage the auto MNNVL feature.

## Overview

**MNNVL (Multi-Node NVLink)** is an NVIDIA technology that extends high-bandwidth, low-latency NVLink GPU-to-GPU communication across multiple physical nodes. 
In Kubernetes, MNNVL is exposed through NVIDIA's Dynamic Resource Allocation (DRA) driver via a custom resource called **ComputeDomain**. The ComputeDomain represents a logical GPU fabric spanning multiple nodes.

Without the auto MNNVL feature, users must manually create ComputeDomain resources and wire up `resourceClaims` in their pod specs. With auto MNNVL enabled, Grove handles this automatically:

When auto MNNVL is enabled, Grove will:
- Detect GPU containers in your PodCliqueSet
- Create one ComputeDomain per PCS replica
- Manage the full ComputeDomain lifecycle (creation, scaling, deletion)

| Mode | Description | Best For |
|------|-------------|----------|
| **Auto** (default when enabled) | Grove automatically creates ComputeDomains and injects resource claims for GPU workloads | Most GPU workloads on MNNVL-capable clusters |
| **Opt-out** | User explicitly disables MNNVL per PodCliqueSet via annotation | Custom ComputeDomain configurations, non-MNNVL GPU workloads |

## Prerequisites and Constraints

Before enabling auto MNNVL, ensure your cluster meets the following requirements:

1. **NVIDIA GPUs with MNNVL support** on all nodes that will run MNNVL workloads
2. **NVIDIA DRA driver** installed, which provides the ComputeDomain CRD (`computedomains.resource.nvidia.com`)
3. **Homogeneous GPU cluster** -- all nodes must have identical GPU types and NVLink topology. Grove does not validate or enforce cluster homogeneity; it is the cluster administrator's responsibility to ensure this requirement is met. Enabling MNNVL on heterogeneous clusters may result in undefined scheduling behavior.
4. **Grove operator** deployed via Helm

## Enabling the Feature

Auto MNNVL is disabled by default. To enable it, set the `config.network.autoMNNVLEnabled` Helm value to `true`:

```yaml
config:
  network:
    autoMNNVLEnabled: true
```

Deploy or upgrade Grove with this configuration:

```bash
helm upgrade -i grove oci://ghcr.io/ai-dynamo/grove/grove-charts:<tag> \
  --set config.network.autoMNNVLEnabled=true
```

### Startup Validation

When `autoMNNVLEnabled` is `true`, the Grove operator performs a preflight check at startup to verify that the ComputeDomain CRD (`computedomains.resource.nvidia.com`) is installed in the cluster. If the CRD is not found, the operator will exit with a non-zero exit code and log an error. This ensures you are not running with MNNVL enabled on a cluster that cannot support it.

## How It Works

When you create a PodCliqueSet with GPU containers (requesting `nvidia.com/gpu`), Grove automatically adds the `grove.io/auto-mnnvl: "enabled"` annotation to the PCS and creates one ComputeDomain per PCS replica. No changes to your PCS manifest are required.

For a PCS named `my-inference` with `replicas: 2`, Grove creates:

- `my-inference-0` -- ComputeDomain for replica 0
- `my-inference-1` -- ComputeDomain for replica 1

The ComputeDomains and all necessary resource claims are managed entirely by Grove. Your pods automatically join the MNNVL fabric when scheduled.

> **Note:** The `grove.io/auto-mnnvl` annotation is **immutable** after PCS creation. Any attempt to add, modify, or remove it on an existing PCS will be rejected. To change MNNVL behavior, delete the PCS and recreate it.

## Opting Out

If auto MNNVL is enabled globally but you want a specific PodCliqueSet to **not** use MNNVL, explicitly set the annotation to `"disabled"` at creation time:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: my-non-mnnvl-workload
  annotations:
    grove.io/auto-mnnvl: "disabled"
spec:
  replicas: 1
  template:
    cliques:
      - name: worker
        spec:
          podSpec:
            containers:
              - name: model
                image: my-model:latest
                resources:
                  limits:
                    nvidia.com/gpu: "8"
```

When opting out, the operator will **not** create ComputeDomains or any other MNNVL-related artifacts for that PCS.

> **Note:** The annotation value must be exactly `"enabled"` or `"disabled"`. Other values (e.g., `"true"`, `"false"`, empty string) will be rejected.

## Observability

### Checking ComputeDomain Status

ComputeDomains have no meaningful status at creation time -- they only become active after pods referencing their ResourceClaimTemplate are scheduled. To check the status of ComputeDomains managed by Grove:

```bash
# List all ComputeDomains for a specific PCS
kubectl get computedomain -l app.kubernetes.io/part-of=my-inference

# Get detailed status for a specific ComputeDomain
kubectl describe computedomain my-inference-0
```

### Kubernetes Events

Grove emits Kubernetes events on the PCS resource for ComputeDomain lifecycle operations:

```bash
kubectl describe pcs my-inference
# Events:
#   Normal   ComputeDomainCreated   ComputeDomain my-inference-0 created
#   Normal   ComputeDomainCreated   ComputeDomain my-inference-1 created
#   Warning  ComputeDomainFailed    Failed to create ComputeDomain for replica 2: <error>
```

### Verifying MNNVL Annotation

To check whether a PCS has auto MNNVL enabled:

```bash
kubectl get pcs my-inference -o jsonpath='{.metadata.annotations.grove\.io/auto-mnnvl}'
```

## Scaling Behavior

Auto MNNVL integrates seamlessly with PodCliqueSet scaling:

### Scale-Out

When you increase the replica count on a PCS, the operator automatically creates new ComputeDomains for the additional replicas. For example, scaling `my-inference` from 2 to 4 replicas creates `my-inference-2` and `my-inference-3`.

```bash
kubectl scale pcs my-inference --replicas=4
```

### Scale-In

When you decrease the replica count, the operator removes ComputeDomains for the excess replicas. Any ComputeDomain with a replica index equal to or greater than the new count is cleaned up.

```bash
kubectl scale pcs my-inference --replicas=1
# ComputeDomains my-inference-1, my-inference-2, my-inference-3 are deleted
```

### Deletion Protection

Grove adds a finalizer (`grove.io/computedomain-finalizer`) to each ComputeDomain it creates. This prevents accidental deletion of ComputeDomains while pods are actively using them. If a user attempts to delete a ComputeDomain manually, it will remain in `Terminating` state until the owning PCS is deleted or scaled down.

## Backward Compatibility

Existing PodCliqueSet resources created before the MNNVL feature was enabled will not have the `grove.io/auto-mnnvl` annotation. These workloads will continue to operate without ComputeDomains, even after the feature is enabled globally. To enable MNNVL for an existing workload, the PCS must be deleted and recreated.

## Configuration Reference

| Helm Value / Annotation | Type | Default | Description |
|--------------------------|------|---------|-------------|
| `config.network.autoMNNVLEnabled` | bool | `false` | Enables automatic MNNVL management. When `true`, the operator validates the ComputeDomain CRD at startup and automatically creates ComputeDomains for GPU workloads. |
| `grove.io/auto-mnnvl` (annotation) | string | Not set | Per-PCS annotation controlling MNNVL. Values: `"enabled"` (auto-set by webhook for GPU workloads) or `"disabled"` (explicit opt-out). Immutable after creation. |

**Limitations:**

- **One IMEX channel per node:** Currently, only one IMEX domain can be supported per node. Since, ComputeDomains do not support sharing IMEX domain between workloads, each node can only support pods from at most one MNNVL-enabled workload at a time. MNNVL-enabled pods from different workloads cannot share the same node at the same time.
- **PCS-level granularity:** Auto-MNNVL feature is uniformly applied to the entire PodCliqueSet. All GPU pods in a PCS will automatically get the IMEX channel setup if the underlying GPUs support it. This feature cannot be special-cased for a subset of PodCliques in a PodCliqueSet.
- **No ComputeDomain customization:** ComputeDomain and ResourceClaimTemplate configurations are automatically generated and cannot be customized. Opt out if you need custom settings.
- **Immutable annotation:** The `grove.io/auto-mnnvl` annotation cannot be changed after PCS creation. Delete and recreate the PCS to change MNNVL behavior.
- **No ComputeDomain status propagation:** Grove does not surface ComputeDomain status in PCS status fields. Inspect the ComputeDomain resource directly using `kubectl get computedomain`.

## Examples

### GPU Workload with Auto MNNVL

When auto MNNVL is enabled globally, simply deploy a PCS with GPU containers. Grove handles the rest:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: distributed-inference
  namespace: default
spec:
  replicas: 2
  template:
    cliques:
      - name: worker
        spec:
          replicas: 1
          podSpec:
            containers:
              - name: model-server
                image: my-inference-server:latest
                resources:
                  limits:
                    nvidia.com/gpu: "8"
```

After creation, verify that MNNVL is enabled and ComputeDomains exist:

```bash
# Check the annotation
kubectl get pcs distributed-inference -o jsonpath='{.metadata.annotations.grove\.io/auto-mnnvl}'
# Output: enabled

# List ComputeDomains
kubectl get computedomain -l app.kubernetes.io/part-of=distributed-inference
# NAME                       AGE
# distributed-inference-0    10s
# distributed-inference-1    10s
```
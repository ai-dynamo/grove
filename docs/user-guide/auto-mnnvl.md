# Auto MNNVL (Multi-Node NVLink)

Grove can automatically manage Multi-Node NVLink (MNNVL) acceleration for your GPU workloads. This guide explains how to enable, configure, and manage the auto MNNVL feature using the `grove.io/mnnvl-group` annotation.

## Overview

**MNNVL (Multi-Node NVLink)** is an NVIDIA technology that extends high-bandwidth, low-latency NVLink GPU-to-GPU communication across multiple physical nodes.
In Kubernetes, MNNVL is exposed through NVIDIA's Dynamic Resource Allocation (DRA) driver via a custom resource called **ComputeDomain**. The ComputeDomain represents a logical GPU fabric spanning multiple nodes.

Without the auto MNNVL feature, you must manually create ComputeDomain resources and wire up `resourceClaims` in your pod specs. With auto MNNVL enabled, Grove handles this automatically:

- Detects GPU containers in your PodCliqueSet (PCS)
- Creates one ComputeDomain per MNNVL group per PCS replica
- Injects resource claim references into enrolled PodClique (PCLQ) pod specs
- Manages the full ComputeDomain lifecycle (creation, scaling, deletion)

You control MNNVL participation with a single annotation, `grove.io/mnnvl-group`, which can be placed at any level of the PCS hierarchy:

| Annotation value | Meaning |
|---|---|
| A group name (e.g., `"my-group"`, `"workers"`) | Opt in to MNNVL. PodCliques with the same group name share a ComputeDomain per replica. |
| `"none"` | Explicit opt-out. Overrides a parent layer's group assignment. |
| Absent | Inherit from the parent layer. If no parent sets it, no MNNVL. |

## Prerequisites and Constraints

Before enabling auto MNNVL, ensure your cluster meets the following requirements:

1. **NVIDIA GPUs with MNNVL support** on the nodes that will run MNNVL workloads.
2. **NVIDIA DRA driver** installed, which provides the ComputeDomain CRD (`computedomains.resource.nvidia.com`). See the [NVIDIA DRA driver installation guide](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/gpu-operator-dra.html) for setup instructions.
3. **Grove operator** deployed via Helm.

## Enabling the Feature

Auto MNNVL is disabled by default. To enable it, set the `config.network.autoMNNVLEnabled` Helm value to `true`:

```yaml
config:
  network:
    autoMNNVLEnabled: true
```

Deploy or upgrade Grove with this configuration:

```bash
helm upgrade -i grove oci://ghcr.io/ai-dynamo/grove/grove-charts --version <version> \
  --set config.network.autoMNNVLEnabled=true
```

### Startup Validation

When `autoMNNVLEnabled` is `true`, the Grove operator performs a preflight check at startup to verify that the ComputeDomain CRD (`computedomains.resource.nvidia.com`) is installed in the cluster. If the CRD is not found, the operator exits with a non-zero exit code and logs an error. This ensures you are not running with MNNVL enabled on a cluster that cannot support it.

When the feature is disabled (`autoMNNVLEnabled: false`), any PCS that requests MNNVL (i.e., `grove.io/mnnvl-group` set to a group name) is rejected at admission time.

## How It Works

You opt in to MNNVL by adding the `grove.io/mnnvl-group` annotation at any layer of the PCS hierarchy — PCS, PodCliqueScalingGroup (PCSG), or PodClique (PCLQ). The annotation value is a group name — PodCliques sharing the same group name share a single ComputeDomain per replica. The ComputeDomains and all necessary resource claims are managed entirely by Grove. Your pods automatically join the MNNVL fabric when scheduled.

The annotation propagates downward through the hierarchy: **PCS → PCSG → PCLQ**. A lower layer overrides a higher layer, so you can set a default at the PCS level and override it on specific PodCliques or PodCliqueScalingGroups.

| PCS annotation | PCLQ annotation | Effective PCLQ group |
|---|---|---|
| `"my-group"` | Absent | `"my-group"` (inherited) |
| `"my-group"` | `"other-group"` | `"other-group"` (overridden) |
| `"my-group"` | `"none"` | No MNNVL (opted out) |
| Absent | `"my-group"` | `"my-group"` (PCLQ-level only) |

### Non-GPU PodCliques

If a non-GPU PodClique inherits `grove.io/mnnvl-group` from a parent PCS or PCSG, it is silently skipped — no resource claims are injected, no error is raised. This allows you to set MNNVL at the PCS level without adding `"none"` overrides on every non-GPU PCLQ.

If a non-GPU PodClique carries an *explicit* `grove.io/mnnvl-group` annotation (other than `"none"`), the PCS is rejected — requesting MNNVL for a PodClique with no GPUs is a user error.

> **Note:** The `grove.io/mnnvl-group` annotation is **immutable** after PCS creation. Any attempt to add, modify, or remove it on an existing PCS, PCSG, or PCLQ is rejected. To change MNNVL configuration, delete the PCS and recreate it.

## Usage Examples

### Simple Opt-In (All GPU PodCliques in One Group)

Add `grove.io/mnnvl-group` at the PCS level. All GPU PodCliques share a single ComputeDomain per replica:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: my-workload
  annotations:
    grove.io/mnnvl-group: "my-group"
spec:
  replicas: 2
  template:
    cliques:
      - name: worker
        spec:
          replicas: 1
          podSpec:
            containers:
              - name: model
                image: my-model:latest
                resources:
                  limits:
                    nvidia.com/gpu: "8"
```

This creates ComputeDomains `my-workload-0-my-group` and `my-workload-1-my-group`, for all PodCliques.

### Multiple MNNVL Groups

Assign different PodCliques to separate groups. Each group gets its own ComputeDomain per replica:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: training-job
spec:
  replicas: 2
  template:
    cliques:
      - name: workers
        annotations:
          grove.io/mnnvl-group: "workers"
        spec:
          replicas: 1
          podSpec:
            containers:
              - name: worker
                image: my-model:latest
                resources:
                  limits:
                    nvidia.com/gpu: "8"
      - name: encoders
        annotations:
          grove.io/mnnvl-group: "encoders"
        spec:
          replicas: 1
          podSpec:
            containers:
              - name: encoder
                image: my-encoder:latest
                resources:
                  limits:
                    nvidia.com/gpu: "4"
      - name: param-servers
        spec:
          replicas: 1
          podSpec:
            containers:
              - name: ps
                image: my-ps:latest
                resources:
                  limits:
                    cpu: "4"
```

Result per replica:
- `workers` → group `"workers"` → ComputeDomain `training-job-0-workers` / `training-job-1-workers`
- `encoders` → group `"encoders"` → ComputeDomain `training-job-0-encoders` / `training-job-1-encoders`
- `param-servers` → no annotation → no MNNVL

### PCS-Level Default With PCLQ Opt-Out

Set a default group at the PCS level and override on a specific PodClique:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: my-workload
  annotations:
    grove.io/mnnvl-group: "my-group"
spec:
  replicas: 1
  template:
    cliques:
      - name: workers
        spec:
          replicas: 1
          podSpec:
            containers:
              - name: worker
                image: my-model:latest
                resources:
                  limits:
                    nvidia.com/gpu: "8"
      - name: monitoring
        annotations:
          grove.io/mnnvl-group: "none"
        spec:
          replicas: 1
          podSpec:
            containers:
              - name: monitor
                image: my-monitor:latest
                resources:
                  limits:
                    nvidia.com/gpu: "1"
```

- `workers` → inherits `"my-group"` from PCS → enrolled
- `monitoring` → `"none"` overrides PCS → no MNNVL

### Three-Layer Hierarchy (PCS → PCSG → PCLQ)

A PCS-level group propagates through PodCliqueScalingGroups down to PodCliques. Each layer can override or opt out:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: my-workload
  annotations:
    grove.io/mnnvl-group: "my-group"
spec:
  replicas: 1
  template:
    podCliqueScalingGroups:
      - name: opted-out-sg
        annotations:
          grove.io/mnnvl-group: "none"
        cliques: [gpu-a, gpu-b]
      - name: inherited-sg
        cliques: [gpu-c, gpu-d]
    cliques:
      - name: gpu-a
        spec:
          replicas: 1
          podSpec:
            containers:
              - name: a
                image: my-model:latest
                resources:
                  limits:
                    nvidia.com/gpu: "8"
      - name: gpu-b
        annotations:
          grove.io/mnnvl-group: "my-group"
        spec:
          replicas: 1
          podSpec:
            containers:
              - name: b
                image: my-model:latest
                resources:
                  limits:
                    nvidia.com/gpu: "8"
      - name: gpu-c
        spec:
          replicas: 1
          podSpec:
            containers:
              - name: c
                image: my-model:latest
                resources:
                  limits:
                    nvidia.com/gpu: "8"
      - name: gpu-d
        annotations:
          grove.io/mnnvl-group: "none"
        spec:
          replicas: 1
          podSpec:
            containers:
              - name: d
                image: my-model:latest
                resources:
                  limits:
                    nvidia.com/gpu: "8"
```

Resolution for each PodClique:

| PodClique | PCLQ annotation | PCSG annotation | PCS annotation | Effective group |
|---|---|---|---|---|
| `gpu-a` | Absent | `"none"` | `"my-group"` | No MNNVL — PCSG opts out |
| `gpu-b` | `"my-group"` | `"none"` | `"my-group"` | `"my-group"` — PCLQ overrides PCSG back into the PCS group |
| `gpu-c` | Absent | Absent | `"my-group"` | `"my-group"` — inherited through PCSG from PCS |
| `gpu-d` | `"none"` | Absent | `"my-group"` | No MNNVL — PCLQ opts out |

### Group Name Requirements

The `grove.io/mnnvl-group` value must be either `"none"` (opt-out) or a valid DNS-1123 label: lowercase alphanumeric characters or dashes, starting and ending with an alphanumeric character, max 63 characters. The group name becomes part of the ComputeDomain resource name. Invalid values are rejected at admission time.

## ComputeDomain Naming

Grove creates one ComputeDomain per MNNVL group per PCS replica. The name follows the pattern `{pcs-name}-{replica-index}-{group-name}`.

For a PCS named `my-workload` with `replicas: 2` and the annotation `grove.io/mnnvl-group: "my-group"`:

| Replica | ComputeDomain name |
|---|---|
| 0 | `my-workload-0-my-group` |
| 1 | `my-workload-1-my-group` |

If multiple groups exist within the same PCS (e.g., `"workers"` and `"encoders"`), each group produces its own set of ComputeDomains: `my-workload-0-workers`, `my-workload-0-encoders`, etc.

Group names are scoped to the PCS — different PCS resources can reuse the same group names without conflict.

## Observability

### Checking ComputeDomain Status

ComputeDomains have no meaningful status at creation time — they only become active after pods referencing their ResourceClaimTemplate are scheduled. To check the status of ComputeDomains managed by Grove:

```bash
# List all ComputeDomains for a specific PCS
kubectl get computedomain -l app.kubernetes.io/part-of=my-workload

# Get detailed status for a specific ComputeDomain
kubectl describe computedomain my-workload-0-my-group
```

### Kubernetes Events

Grove emits Kubernetes events on the PCS resource for ComputeDomain lifecycle operations:

```bash
kubectl describe pcs my-workload
# Events:
#   Normal   ComputeDomainCreated   ComputeDomain my-workload-0-my-group created
#   Normal   ComputeDomainCreated   ComputeDomain my-workload-1-my-group created
#   Warning  ComputeDomainFailed    Failed to create ComputeDomain for replica 2: <error>
```

### Verifying MNNVL Group

To check which MNNVL group a PCS belongs to:

```bash
kubectl get pcs my-workload -o jsonpath='{.metadata.annotations.grove\.io/mnnvl-group}'
```

## Scaling Behavior

Auto MNNVL integrates seamlessly with PodCliqueSet scaling:

### Scale-Out

When you increase the replica count on a PCS, the operator automatically creates new ComputeDomains for the additional replicas. For example, scaling `my-workload` from 2 to 4 replicas creates `my-workload-2-my-group` and `my-workload-3-my-group`.

```bash
kubectl scale pcs my-workload --replicas=4
```

### Scale-In

When you decrease the replica count, the operator removes ComputeDomains for the excess replicas. Any ComputeDomain with a replica index equal to or greater than the new count is cleaned up.

```bash
kubectl scale pcs my-workload --replicas=1
# ComputeDomains my-workload-1-my-group, my-workload-2-my-group, my-workload-3-my-group are deleted
```

### Deletion Protection

Grove adds a finalizer (`grove.io/computedomain-finalizer`) to each ComputeDomain it creates. This prevents accidental deletion of ComputeDomains while pods are actively using them. If a user attempts to delete a ComputeDomain manually, the controller recreates it as long as the owning PCS still requires it.

## Backward Compatibility

The `grove.io/mnnvl-group` annotation replaces the previous `grove.io/auto-mnnvl` annotation. Existing PodCliqueSet resources using `grove.io/auto-mnnvl` must be updated:

| Before | After |
|---|---|
| `grove.io/auto-mnnvl: "enabled"` | `grove.io/mnnvl-group: "<group-name>"` |
| `grove.io/auto-mnnvl: "disabled"` | `grove.io/mnnvl-group: "none"` or remove the annotation |
| No annotation | No annotation (no change needed) |

The `grove.io/auto-mnnvl` annotation is no longer recognized. PCS resources carrying it will have the annotation ignored.

> **Note:** ComputeDomain naming has changed. Previously, CDs were named `{pcs-name}-{replica-index}`. Now they are named `{pcs-name}-{replica-index}-{group-name}`. Existing ComputeDomains created under the old naming scheme are not automatically migrated. Delete and recreate affected PCS resources after upgrading.

## Limitations

- **One IMEX channel per node:** Currently, only one IMEX domain can be supported per node. ComputeDomains do not support sharing an IMEX domain between workloads, so each node can support pods from at most one MNNVL-enabled workload at a time.
- **Immutable annotation:** The `grove.io/mnnvl-group` annotation cannot be changed after PCS creation at any level (PCS, PCSG, PCLQ). Delete and recreate the PCS to change MNNVL configuration.
- **No ComputeDomain status propagation:** Grove does not surface ComputeDomain status in PCS status fields. Inspect the ComputeDomain resource directly using `kubectl get computedomain`.

# Topology-Aware Scheduling

Grove topology-aware scheduling lets you describe where a PodCliqueSet (PCS) should be packed in your cluster topology without putting cluster-specific node label keys in every workload. This guide explains how to enable topology-aware scheduling, define cluster topology bindings, and add topology constraints to PodCliqueSet workloads.

## Overview

Topology-aware scheduling has two layers:

- Cluster administrators create a *ClusterTopologyBinding*. This cluster-scoped resource maps portable Grove topology domains, such as `zone`, `rack`, and `host`, to the node label keys used in the cluster.
- Workload authors add `topologyConstraint` fields to a PodCliqueSet (PCS), PodCliqueScalingGroup (PCSG), or PodClique (PCLQ). Grove validates the constraint, translates the domain to the configured node label key, and passes the translated constraint to the scheduler.

Grove currently supports topology *pack* constraints:

| Constraint | Meaning |
|---|---|
| `pack.required` | Hard requirement. The scheduler should not place the workload unless the constraint can be satisfied. |
| `pack.preferred` | Best-effort preference. The scheduler can fall back when the preferred domain cannot be satisfied. |

For example, if a ClusterTopologyBinding maps `rack` to `topology.kubernetes.io/rack`, a PCS can use `pack.required: rack`. Grove translates that domain into the scheduler-specific topology key before scheduling.

## Prerequisites and Constraints

Before using topology-aware scheduling, ensure your cluster meets the following requirements:

1. **Grove operator** deployed via Helm with topology-aware scheduling enabled.
2. **A topology-aware scheduler backend** installed and enabled in Grove. The backend must support topology-aware scheduling.
3. **Stable topology labels** on your nodes. The label keys in `ClusterTopologyBinding.spec.levels[*].key` must exist on the nodes that the scheduler can use.
4. **One ClusterTopologyBinding per topology shape** that workloads should target.

The `ClusterTopologyBinding.spec.levels` list must be ordered from broadest to narrowest scope. For example: `zone`, then `rack`, then `host`.

Each constrained PCS must resolve to one effective `topologyName`. You can set `topologyName` once at the PCS level and let child constraints inherit it, but a single PCS cannot use multiple topology names.

## Enabling the Feature

Topology-aware scheduling is disabled by default. To enable it, set the `config.topologyAwareScheduling.enabled` Helm value to `true`:

```yaml
config:
  topologyAwareScheduling:
    enabled: true
```

Deploy or upgrade Grove with this configuration:

```bash
helm upgrade -i grove oci://ghcr.io/ai-dynamo/grove/grove-charts --version <version> \
  --set config.topologyAwareScheduling.enabled=true
```

If you customize scheduler profiles, make sure your topology-aware scheduler backend is listed in `config.scheduler.profiles`. Workloads that use topology constraints should either rely on that backend as Grove's default scheduler profile or set `podSpec.schedulerName` to that backend's scheduler name.

### Validation Behavior

When topology-aware scheduling is disabled, new PCS resources with topology constraints are rejected at admission time. If an existing constrained PCS is reconciled while topology-aware scheduling is disabled, Grove reports `TopologyLevelsUnavailable` with reason `TopologyAwareSchedulingDisabled`.

## Topology Constraint Rules

Topology constraints can appear at any layer of the PCS hierarchy: PCS, PCSG, or PCLQ. Child constraints must be equal to or narrower than parent constraints according to the ClusterTopologyBinding level order.

| Parent domain | Child domain | Result |
|---|---|---|
| `rack` | `rack` | Allowed. The child constraint is equal to the parent. |
| `rack` | `host` | Allowed. `host` is narrower than `rack`. |
| `rack` | `zone` | Rejected. `zone` is broader than `rack`. |

The topology name also propagates downward through the hierarchy. A child `topologyConstraint` can omit `topologyName` if it inherits the same topology from its parent.

> **Note:** Topology constraints are immutable after PCS creation. Any attempt to add, modify, or remove `topologyName`, `pack.required`, or `pack.preferred` on an existing PCS, PCSG, or PCLQ is rejected. To change topology placement, delete the PCS and recreate it.

> **Note:** The legacy `packDomain` field is deprecated. Use `pack.required` for new workloads.

## Topology Domain Requirements

Common Grove topology domain names include `region`, `zone`, `datacenter`, `block`, `rack`, `host`, and `numa`. Any domain that you use in a PCS must be defined in the selected ClusterTopologyBinding.

Topology domain names must be lowercase DNS-label style values: start with a letter, contain lowercase letters, numbers, or hyphens, and be no longer than 63 characters.

## Scheduler Backend Topology Binding

You usually do not need to set `ClusterTopologyBinding.spec.schedulerTopologyBindings`. Omit it when Grove should manage the scheduler backend topology resource from `spec.levels`.

Set `schedulerTopologyBindings` only when the scheduler backend topology resource already exists and is managed outside Grove. The `schedulerName` must match an enabled topology-aware scheduler backend, and `topologyReference` is the backend-specific topology resource name. A ClusterTopologyBinding can have at most one binding entry for each scheduler backend.

## Usage Examples

The workload examples below assume Grove's default scheduler profile is a topology-aware backend. If your cluster uses a different default, add `podSpec.schedulerName` to each PodClique and set it to your topology-aware scheduler backend name.

### Define a Cluster Topology

Create one ClusterTopologyBinding for each hardware topology that you want workloads to target. The following example defines a GPU fabric with zones, racks, and hosts:

```yaml
apiVersion: grove.io/v1alpha1
kind: ClusterTopologyBinding
metadata:
  name: gpu-fabric
spec:
  levels:
    - domain: zone
      key: topology.kubernetes.io/zone
    - domain: rack
      key: topology.kubernetes.io/rack
    - domain: host
      key: kubernetes.io/hostname
```

Apply it:

```bash
kubectl apply -f gpu-fabric.yaml
```

### Required Rack Packing

Add `topologyConstraint` at the PCS level to constrain each PCS replica. This example requires each PCS replica to fit within one rack in the `gpu-fabric` topology:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: my-inference
  namespace: default
spec:
  replicas: 2
  template:
    topologyConstraint:
      topologyName: gpu-fabric
      pack:
        required: rack
    cliques:
      - name: prefill
        spec:
          roleName: prefill
          replicas: 1
          minAvailable: 1
          podSpec:
            containers:
              - name: prefill
                image: my-model:latest
                resources:
                  limits:
                    nvidia.com/gpu: "8"
      - name: decode
        spec:
          roleName: decode
          replicas: 1
          minAvailable: 1
          podSpec:
            containers:
              - name: decode
                image: my-model:latest
                resources:
                  limits:
                    nvidia.com/gpu: "8"
```

The `rack` requirement is evaluated for each PCS replica independently. Replicas do not all need to use the same rack.

### Preferred Host Packing

Use `pack.preferred` when the placement improves performance but should not block scheduling. In the PCS manifest above, add it only to the child that needs the preference:

```yaml
spec:
  template:
    topologyConstraint:
      topologyName: gpu-fabric
      pack:
        required: rack
    cliques:
      - name: decode
        topologyConstraint:
          pack:
            preferred: host
```

The `decode` PodClique inherits `topologyName: gpu-fabric` from the PCS-level constraint.

### Configure Scheduler Backend Topology Bindings

If a scheduler backend topology resource is managed outside Grove, reference it with `schedulerTopologyBindings`. For example, in a KAI-backed installation where an existing KAI `Topology` named `existing-gpu-fabric` is managed outside Grove:

```yaml
apiVersion: grove.io/v1alpha1
kind: ClusterTopologyBinding
metadata:
  name: gpu-fabric
spec:
  levels:
    - domain: zone
      key: topology.kubernetes.io/zone
    - domain: rack
      key: topology.kubernetes.io/rack
    - domain: host
      key: kubernetes.io/hostname
  schedulerTopologyBindings:
    - schedulerName: kai-scheduler
      topologyReference: existing-gpu-fabric
```

Grove checks that the referenced scheduler topology matches the ClusterTopologyBinding levels and reports drift in ClusterTopologyBinding status.

## Observability

### Checking ClusterTopologyBinding Status

Inspect configured topology bindings:

```bash
kubectl get clustertopologybindings
kubectl describe clustertopologybinding gpu-fabric
```

Check whether Grove reports scheduler topology drift:

```bash
kubectl wait clustertopologybinding gpu-fabric \
  --for=condition=SchedulerTopologyDrift=False \
  --timeout=60s

kubectl get clustertopologybinding gpu-fabric \
  -o jsonpath='{.status.schedulerTopologyStatuses}'
```

### Checking Scheduler Topology Status

Inspect the scheduler backend topology resource that Grove manages or checks. The resource type and namespace are backend-specific. For example, with KAI:

```bash
kubectl get topologies.kai.scheduler
kubectl describe topologies.kai.scheduler gpu-fabric
```

### Kubernetes Events

Grove emits Kubernetes events on ClusterTopologyBinding status transitions and PCS reconciliation:

```bash
kubectl get events --field-selector involvedObject.kind=ClusterTopologyBinding,involvedObject.name=gpu-fabric
kubectl get events -n default --field-selector involvedObject.kind=PodCliqueSet,involvedObject.name=my-inference
```

### Verifying PCS Topology Conditions

Check the PCS topology condition:

```bash
kubectl wait pcs my-inference -n default \
  --for=condition=TopologyLevelsUnavailable=False \
  --timeout=60s

kubectl describe pcs my-inference -n default
```

The `TopologyLevelsUnavailable` condition reports whether all topology domains referenced by the PCS are available in the selected ClusterTopologyBinding:

| Status | Reason | Meaning |
|---|---|---|
| `False` | `AllClusterTopologyLevelsAvailable` | All referenced domains exist in the selected ClusterTopologyBinding. |
| `True` | `ClusterTopologyLevelsUnavailable` | The PCS references a domain that is no longer defined in the selected ClusterTopologyBinding. |
| `Unknown` | `ClusterTopologyNotFound` | The selected ClusterTopologyBinding does not exist. |
| `Unknown` | `TopologyNameMissing` | A topology constraint exists but Grove cannot resolve an effective `topologyName`. |
| `Unknown` | `TopologyAwareSchedulingDisabled` | The PCS has topology constraints but topology-aware scheduling is disabled. |

# Kueue Integration

Grove can run a PodCliqueSet (PCS) under [Kueue](https://kueue.sigs.k8s.io/) quota. Kueue is a Kubernetes job queueing system that decides when a job is admitted to start. With the Kueue scheduler backend, a PCS waits in a Kueue queue until quota is available. Kueue can also choose a topology-aware placement for its Pods. Grove still creates and manages the Pods. This guide explains how to enable the backend, submit a PCS to a Kueue queue, and request topology-aware placement.

## Overview

Grove and Kueue have separate responsibilities. Grove manages the PCS and creates its Pods. Kueue holds the Pods back until their queue has enough quota. Once Kueue releases them, the Kubernetes default scheduler binds the Pods to nodes.

A PCS is submitted to a namespaced Kueue [LocalQueue](https://kueue.sigs.k8s.io/docs/concepts/local_queue/). The LocalQueue points to a [ClusterQueue](https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/), which defines the quota.

Grove groups the Pods that must be scheduled together into a *PodGang*. A PodGang has one *PodGroup* for each PodClique (PCLQ) in it. Grove creates one PodGang for each PCS replica. Scaling out a PodCliqueScalingGroup (PCSG) adds one PodGang for each new replica. Grove submits each PodGang to Kueue as a [Workload](https://kueue.sigs.k8s.io/docs/concepts/workload/), the unit that Kueue admits. Each Workload is admitted independently, so one PCS replica can run while another waits for quota.

The following table shows how Grove resources map to Kueue resources:

| Grove resource | Kueue resource |
|---|---|
| `kueue.x-k8s.io/queue-name` label on the PCS | LocalQueue that the PCS is submitted to |
| PodGang | Workload with the same name and namespace |
| PodGroup in a PodGang | PodSet in that Workload |
| ClusterTopologyBinding | Kueue [Topology](https://kueue.sigs.k8s.io/docs/concepts/topology_aware_scheduling/) with the same name |
| `topologyConstraint` on a PCS, PCSG, or PCLQ | Topology request on each PodSet it applies to |

## Prerequisites and Constraints

Before using the Kueue backend, ensure your cluster meets the following requirements:

1. **Grove operator** deployed via Helm with the Kueue backend enabled. See [Enabling the Feature](#enabling-the-feature).
2. **Kueue v0.20.1** installed. See the [Kueue installation guide](https://kueue.sigs.k8s.io/docs/installation/).
3. **The Kueue `pod` integration** enabled. Grove submits its Pods through Kueue's [plain Pod integration](https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/). Kueue enables this integration by default.
4. **The PCS namespace managed by Kueue.** Kueue holds Pods back only in namespaces that match its `managedJobsNamespaceSelector`. By default, the selector matches every namespace except `kube-system` and the namespace that Kueue runs in. Pods in a namespace that the selector does not match start without waiting for quota.
5. **A Kueue queue in the PCS namespace.** Create a LocalQueue in the namespace where you deploy the PCS. See [Setting Up Kueue Queues](#setting-up-kueue-queues).

Topology-aware placement has the following additional requirements:

1. **Grove topology-aware scheduling** enabled, with a ClusterTopologyBinding that describes your topology. See the [topology-aware scheduling guide](topology-aware-scheduling.md).
2. **A ResourceFlavor bound to the Kueue Topology.** Set the flavor's `spec.topologyName` to the name of the ClusterTopologyBinding. Kueue does not admit a PodSet with a topology request on a flavor without a `topologyName`.
3. **Kueue topology-aware scheduling** enabled. The `TopologyAwareScheduling` feature gate is beta in Kueue and enabled by default.

### Setting Up Kueue Queues

A ClusterQueue draws its quota from one or more *ResourceFlavors*. A ResourceFlavor represents a class of nodes. A ResourceFlavor without node labels or taints suits a cluster with homogeneous resources.

The following manifest creates a ResourceFlavor, a ClusterQueue with CPU and memory quota, and a LocalQueue in the `default` namespace. Save it as `kueue-queues.yaml`:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor
metadata:
  name: default-flavor
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: cluster-queue
spec:
  namespaceSelector: {}
  resourceGroups:
    - coveredResources: ["cpu", "memory"]
      flavors:
        - name: default-flavor
          resources:
            - name: cpu
              nominalQuota: 32
            - name: memory
              nominalQuota: 128Gi
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: LocalQueue
metadata:
  name: user-queue
  namespace: default
spec:
  clusterQueue: cluster-queue
```

Apply it:

```bash
kubectl apply -f kueue-queues.yaml
```

The manifest uses the following ClusterQueue settings:

- `namespaceSelector: {}` lets a LocalQueue in any namespace use the ClusterQueue. If you omit the field, no namespace can use it.
- `nominalQuota` is the amount of each resource that admitted Workloads can use at a point in time. Set it to the share of the cluster that this queue may use.
- `coveredResources` must list every resource that the Pods request. Otherwise, Kueue does not admit the Workload. If your Pods request other resources, such as `nvidia.com/gpu`, add them to `coveredResources` and give the flavor a quota for each.

Verify that both queues are active:

```bash
kubectl wait clusterqueue cluster-queue --for=condition=Active --timeout=60s
kubectl wait localqueue user-queue -n default --for=condition=Active --timeout=60s
```

If a queue does not become active, `kubectl get clusterqueue cluster-queue -o wide` and `kubectl get localqueue user-queue -n default -o wide` show the reason. For example, a ClusterQueue that references a missing ResourceFlavor reports `FlavorNotFound`.

For more options, such as cohorts and borrowing, see [Administer cluster quotas](https://kueue.sigs.k8s.io/docs/tasks/manage/administer_cluster_quotas/) in the Kueue documentation.

## Enabling the Feature

The Kueue backend is disabled by default. To enable it, add a `kueue` profile to the `config.scheduler.profiles` Helm value:

```yaml
config:
  scheduler:
    profiles:
      - name: default-scheduler
      - name: kueue
```

This list replaces the chart's default profiles. Keep any other profile that you use, such as `kai-scheduler`.

Deploy or upgrade Grove with this configuration:

```bash
helm upgrade -i grove oci://ghcr.io/ai-dynamo/grove/grove-charts --version <version> \
  --set 'config.scheduler.profiles[0].name=default-scheduler' \
  --set 'config.scheduler.profiles[1].name=kueue'
```

For installation details and version selection, see the [installation guide](../installation.md).

When the `kueue` profile is listed, the chart also grants the Grove operator access to Kueue Workloads and Topologies.

### Selecting the Backend

A PCS uses the Kueue backend when its PodCliques set `podSpec.schedulerName: kueue`. All PodCliques in a PCS must use the same scheduler.

To make Kueue the default backend, also set `config.scheduler.defaultProfileName` to `kueue`. A PodClique that does not set `schedulerName` then uses Kueue. The new default also covers every existing PCS that omits `schedulerName`. Change the default only if all of them should run under Kueue quota.

### Choosing the Scheduler for Admitted Pods

Grove sets the `schedulerName` of each Pod to the `underlyingSchedulerName` option of the `kueue` profile. That scheduler binds the Pods after Kueue admits them. The default is `default-scheduler`.

To use a different scheduler, set the option in the profile's `config`:

```yaml
config:
  scheduler:
    profiles:
      - name: default-scheduler
      - name: kueue
        config:
          underlyingSchedulerName: <scheduler-name>
```

Use a scheduler that honors [Pod scheduling gates](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-scheduling-readiness/). Kueue uses a scheduling gate to hold Pods back until admission.

### Validation Behavior

If the `kueue` profile is not enabled, a PCS that sets `schedulerName: kueue` is rejected at admission time.

If a ClusterTopologyBinding exists but Kueue is not installed, the Grove operator exits at startup. The operator logs `failed to synchronize cluster topology`. Install Kueue before you enable the backend.

## How It Works

### Queue Selection

A PCS selects its LocalQueue with the `kueue.x-k8s.io/queue-name` label. Every PCS that uses the Kueue backend must set this label. Grove submits every Workload of the PCS to that LocalQueue. Set the label when you create the PCS, and do not change it afterwards. Grove does not move existing Workloads to a new queue.

> **Note:** Grove does not check for the label at admission time. Without the label, Grove cannot create the Workloads or the Pods of the PCS. It records the error in the `status.lastErrors` field of each PodClique.

### Workloads and PodSets

For each PodGang, Grove creates one Workload with the same name in the PCS namespace. The Workload has one PodSet for each PodGroup of the PodGang. The PodSet has the PodGroup's name, which is the PodClique name. Its count is the PodClique's `replicas`, and its Pod template is the PodClique's Pod template. Kueue computes the quota that the Workload needs from the Pod templates. Grove creates the Workload once and does not update it.

For example, a PCS named `my-inference` with `replicas: 2` has two PodCliques, `prefill` with `replicas: 2` and `decode` with `replicas: 1`. Grove creates the following Workloads:

| Workload | PodSet | Count |
|---|---|---|
| `my-inference-0-<epoch>` | `my-inference-0-decode` | 1 |
| | `my-inference-0-prefill` | 2 |
| `my-inference-1-<epoch>` | `my-inference-1-decode` | 1 |
| | `my-inference-1-prefill` | 2 |

The `<epoch>` suffix is a number that Grove adds to each PodGang name.

### Admission

Kueue admits each Workload as follows:

1. Grove creates the Pods. Kueue adds the `kueue.x-k8s.io/admission` scheduling gate to each Pod, so the Pods stay `Pending`.
2. Kueue admits the Workload once its ClusterQueue has enough free quota. For a topology request, Kueue must also find a topology domain where each PodSet fits.
3. Kueue removes the scheduling gate. The scheduler then binds the Pods to nodes.

Grove adds the following Kueue metadata to each Pod:

| Key | Type | Value |
|---|---|---|
| `kueue.x-k8s.io/queue-name` | Label | Queue name from the PCS label |
| `kueue.x-k8s.io/pod-group-name` | Label | PodGang name |
| `kueue.x-k8s.io/prebuilt-workload-name` | Label | Workload name, which equals the PodGang name |
| `kueue.x-k8s.io/role-hash` | Annotation | PodSet name, which equals the PodClique name |
| `kueue.x-k8s.io/pod-group-total-count` | Annotation | Sum of the PodSet counts in the Workload |
| `kueue.x-k8s.io/pod-group-serving` | Annotation | `"true"`. See [Readiness and Eviction](#readiness-and-eviction). |

### Partial Admission

A standalone PodClique, which is not part of a PCSG, can set `minAvailable` lower than `replicas`. Grove then sets the PodSet's `minCount` to `minAvailable`. If the full count does not fit, Kueue can admit the Workload with a lower count for that PodSet, down to `minAvailable`. Kueue still releases every Pod of the PodClique. The ClusterQueue then reports less usage than the Pods consume.

### Readiness and Eviction

Kueue v0.20.1 enables [`waitForPodsReady`](https://kueue.sigs.k8s.io/docs/tasks/manage/setup_wait_for_pods_ready/) by default. Kueue considers a Workload ready only when all of its Pods are ready. Kueue evicts an admitted Workload in the following cases:

- Its Pods are not all ready within `waitForPodsReady.timeout`, which defaults to 30 minutes.
- A Pod of a running Workload stops being ready, and the Workload does not recover within `waitForPodsReady.recoveryTimeout`, which defaults to the timeout.

Set both timeouts to fit how long your Pods take to become ready. For example, Pods that load large model weights can need more than 30 minutes. Configure the timeouts in the Kueue [manager configuration](https://kueue.sigs.k8s.io/docs/installation/#install-a-custom-configured-released-version):

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
waitForPodsReady:
  timeout: 60m
  recoveryTimeout: 15m
```

Grove marks the Pods of each PodGang as a Kueue serving group. Kueue does not finish a serving group's Workload when its Pods complete. When Kueue evicts or preempts the Workload, it deletes the Pods. Grove then creates new Pods, and the same Workload waits in the queue for readmission.

Grove replaces a Workload that Kueue finishes, for example after an error, or that is deactivated (`spec.active: false`). It deletes the Workload and creates a new one with the same name, which is queued again. The new Workload starts with no requeue history. Hence deactivating a Workload, or reaching Kueue's `requeuingStrategy.backoffLimitCount`, does not stop a PCS replica. To release its quota, reduce the PCS `replicas` or delete the PCS.

### Pod Replacement and Cleanup

When a Pod is deleted, Grove creates its replacement only after Kueue stops counting the old Pod. Kueue stops counting a deleted Pod once it has failed or is gone, or if it never got a node. A replacement for a running Pod therefore appears after the old Pod terminates.

Each Workload is owned by its PodGang. When you delete a PCS, Kubernetes garbage collection deletes the Workloads after the PodGangs and their Pods are gone. Kueue adds the `kueue.x-k8s.io/managed` finalizer to each Pod. Grove removes it from any Pod that is being deleted, so the finalizer does not block deletion.

### Topology-Aware Placement

Grove creates a Kueue Topology for each ClusterTopologyBinding and recreates it when the binding's levels change. To use a Kueue Topology that is managed outside Grove instead, reference it in `schedulerTopologyBindings` with `schedulerName: kueue`. See the [topology-aware scheduling guide](topology-aware-scheduling.md#scheduler-backend-topology-binding).

Grove resolves the topology request of each PodClique separately for `required` and `preferred`. For each, it uses the PodClique's own `topologyConstraint`, else its PCSG's, else the PCS's. Grove translates the domain to the node label key from the ClusterTopologyBinding. The key becomes the PodSet's `topologyRequest.required` or `topologyRequest.preferred`. Grove also adds it to the Pods as the `kueue.x-k8s.io/podset-required-topology` or `kueue.x-k8s.io/podset-preferred-topology` annotation.

Kueue chooses a topology domain for each PodSet independently. A PCS-level or PCSG-level constraint therefore packs each PodClique into one domain of the requested level. Different PodCliques of the same PodGang can land in different domains.

## Usage Examples

The examples follow the shapes of the Grove [core concepts](01_core-concepts/01_overview.md) samples. Their Pods request only CPU and memory.

> **Note:** Set `minAvailable` explicitly on every PodClique and PCSG. It defaults to `1`, which makes any PodClique or PCSG with more than one replica a partial gang. The Kueue backend limits partial gangs, as described in [Limitations](#limitations).

### Single-Node Disaggregated Serving

The following PCS runs `prefill` and `decode` as standalone PodCliques in the `user-queue` LocalQueue from [Setting Up Kueue Queues](#setting-up-kueue-queues). Each PodClique sets `minAvailable` equal to `replicas`. Kueue therefore admits each PCS replica only when quota for all five Pods is free.

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: my-inference
  namespace: default
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  replicas: 1
  template:
    cliques:
      - name: prefill
        spec:
          roleName: prefill
          replicas: 3
          minAvailable: 3
          podSpec:
            schedulerName: kueue
            containers:
              - name: prefill
                image: nginx:latest
                resources:
                  requests:
                    cpu: 10m
                    memory: 32Mi
      - name: decode
        spec:
          roleName: decode
          replicas: 2
          minAvailable: 2
          podSpec:
            schedulerName: kueue
            containers:
              - name: decode
                image: nginx:latest
                resources:
                  requests:
                    cpu: 10m
                    memory: 32Mi
```

Apply it:

```bash
kubectl apply -f my-inference.yaml
```

### Multi-Node Disaggregated Serving

The following PCS runs a standalone `frontend` with multi-node `prefill` and `decode` PCSGs. Each PCSG runs a leader and workers. The PCSGs and their PodCliques set `minAvailable` equal to `replicas`, as the Kueue backend requires.

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: my-multinode-inference
  namespace: default
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  replicas: 1
  template:
    cliques:
      - name: frontend
        spec:
          roleName: frontend
          replicas: 2
          minAvailable: 2
          podSpec:
            schedulerName: kueue
            containers:
              - name: frontend
                image: nginx:latest
                resources:
                  requests:
                    cpu: 10m
                    memory: 32Mi
      - name: pleader
        spec:
          roleName: pleader
          replicas: 1
          minAvailable: 1
          podSpec:
            schedulerName: kueue
            containers:
              - name: prefill-leader
                image: nginx:latest
                resources:
                  requests:
                    cpu: 10m
                    memory: 32Mi
      - name: pworker
        spec:
          roleName: pworker
          replicas: 3
          minAvailable: 3
          podSpec:
            schedulerName: kueue
            containers:
              - name: prefill-worker
                image: nginx:latest
                resources:
                  requests:
                    cpu: 10m
                    memory: 32Mi
      - name: dleader
        spec:
          roleName: dleader
          replicas: 1
          minAvailable: 1
          podSpec:
            schedulerName: kueue
            containers:
              - name: decode-leader
                image: nginx:latest
                resources:
                  requests:
                    cpu: 10m
                    memory: 32Mi
      - name: dworker
        spec:
          roleName: dworker
          replicas: 2
          minAvailable: 2
          podSpec:
            schedulerName: kueue
            containers:
              - name: decode-worker
                image: nginx:latest
                resources:
                  requests:
                    cpu: 10m
                    memory: 32Mi
    podCliqueScalingGroups:
      - name: prefill
        cliqueNames: [pleader, pworker]
        replicas: 2
        minAvailable: 2
      - name: decode
        cliqueNames: [dleader, dworker]
        replicas: 1
        minAvailable: 1
```

Grove creates one Workload with seven PodSets for the PCS replica. The `frontend` PodClique adds one PodSet. Each of the two `prefill` replicas adds two, and the `decode` replica adds two.

### Partial Admission for the Frontend

To let Kueue admit the PCS replica with quota for only one `frontend` Pod, set `minAvailable: 1` on the `frontend` PodClique in the previous manifest:

```yaml
spec:
  template:
    cliques:
      - name: frontend
        spec:
          roleName: frontend
          replicas: 2
          minAvailable: 1
```

See [Partial Admission](#partial-admission) for how Kueue counts the quota.

### Packing Workers into a Rack

The following example packs the workers of each model instance into one rack. It uses the `gpu-fabric` ClusterTopologyBinding from [Define a Cluster Topology](topology-aware-scheduling.md#define-a-cluster-topology). It also needs the topology-aware placement items in [Prerequisites and Constraints](#prerequisites-and-constraints).

Create a ResourceFlavor that is bound to the `gpu-fabric` Kueue Topology, with a ClusterQueue and a LocalQueue that use it. Save the manifest as `tas-queues.yaml`:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor
metadata:
  name: tas-flavor
spec:
  nodeLabels:
    cloud.provider.com/node-group: tas-group
  topologyName: gpu-fabric
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: tas-cluster-queue
spec:
  namespaceSelector: {}
  resourceGroups:
    - coveredResources: ["cpu", "memory"]
      flavors:
        - name: tas-flavor
          resources:
            - name: cpu
              nominalQuota: 32
            - name: memory
              nominalQuota: 128Gi
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: LocalQueue
metadata:
  name: tas-user-queue
  namespace: default
spec:
  clusterQueue: tas-cluster-queue
```

Kueue requires at least one entry in `nodeLabels` when `topologyName` is set. Set it to a label that selects the nodes in the topology.

The following PCS runs two model instances, each with a leader and three workers. The `worker` PodClique requires its Pods to share a rack. Kueue places each `leader` Pod independently, as described in [Topology-Aware Placement](#topology-aware-placement).

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: my-packed-inference
  namespace: default
  labels:
    kueue.x-k8s.io/queue-name: tas-user-queue
spec:
  replicas: 1
  template:
    cliques:
      - name: leader
        spec:
          roleName: leader
          replicas: 1
          minAvailable: 1
          podSpec:
            schedulerName: kueue
            containers:
              - name: model-leader
                image: nginx:latest
                resources:
                  requests:
                    cpu: 10m
                    memory: 32Mi
      - name: worker
        topologyConstraint:
          topologyName: gpu-fabric
          pack:
            required: rack
        spec:
          roleName: worker
          replicas: 3
          minAvailable: 3
          podSpec:
            schedulerName: kueue
            containers:
              - name: model-worker
                image: nginx:latest
                resources:
                  requests:
                    cpu: 10m
                    memory: 32Mi
    podCliqueScalingGroups:
      - name: model-instance
        cliqueNames: [leader, worker]
        replicas: 2
        minAvailable: 2
```

Apply both manifests:

```bash
kubectl apply -f tas-queues.yaml
kubectl apply -f my-packed-inference.yaml
```

## Observability

### Finding the Workloads of a PCS

Each Workload has the same name as its PodGang. List the PodGangs of a PCS to find its Workloads:

```bash
kubectl get podgangs -n default -l app.kubernetes.io/part-of=my-inference
```

To see which Workload each Pod belongs to, show the `kueue.x-k8s.io/prebuilt-workload-name` label:

```bash
kubectl get pods -n default -l app.kubernetes.io/part-of=my-inference \
  -L kueue.x-k8s.io/prebuilt-workload-name
```

Pods that wait for admission show the status `SchedulingGated`.

### Checking Admission

List the Workloads in a namespace. The output shows the queue of each Workload, the ClusterQueue that reserves its quota, and whether it is admitted:

```bash
kubectl get workloads.kueue.x-k8s.io -n default
```

The full resource name avoids a clash with other APIs that define a `workloads` resource.

Wait for a Workload to be admitted:

```bash
kubectl wait workloads.kueue.x-k8s.io/<workload-name> -n default --for=condition=Admitted --timeout=5m
```

Describe a Workload to see its conditions and events:

```bash
kubectl describe workloads.kueue.x-k8s.io/<workload-name> -n default
```

The following Workload conditions are the most useful:

| Condition | Meaning |
|---|---|
| `QuotaReserved` | Kueue reserved quota for the Workload. While the condition is `False`, its message explains why the Workload is pending. |
| `Admitted` | Kueue admitted the Workload and released its Pods. |
| `PodsReady` | All Pods of the Workload are ready. |
| `Evicted` | Kueue evicted the Workload. The reason says why, for example `Preempted` or `PodsReadyTimeout`. |

The following messages explain the most common reasons for a pending Workload:

| Message | Cause |
|---|---|
| `insufficient unused quota for <resource> in flavor <flavor>, <amount> more needed` | The ClusterQueue has no free quota. The Workload stays queued until quota frees up. |
| `resource <resource> unavailable in ClusterQueue` | The Pods request a resource that the ClusterQueue does not cover. See [Setting Up Kueue Queues](#setting-up-kueue-queues). |
| `Flavor "<flavor>" does not support TopologyAwareScheduling` | A PodSet has a topology request, but the flavor has no `topologyName`. |

### Checking Queue Usage

Check the pending and admitted Workloads of a LocalQueue, and the quota usage of its ClusterQueue:

```bash
kubectl get localqueue user-queue -n default
kubectl describe clusterqueue cluster-queue
```

The ClusterQueue description lists the reserved and used quota of each flavor under `Flavors Reservation` and `Flavors Usage`.

### Checking Topology Placement

Grove reports whether the Kueue Topology is in sync in the ClusterTopologyBinding status. See [Checking ClusterTopologyBinding Status](topology-aware-scheduling.md#checking-clustertopologybinding-status). List the Kueue Topologies:

```bash
kubectl get topologies.kueue.x-k8s.io
```

Kueue records the topology domain assigned to each PodSet in the Workload status:

```bash
kubectl get workloads.kueue.x-k8s.io/<workload-name> -n default \
  -o jsonpath='{.status.admission.podSetAssignments[*].topologyAssignment}'
```

### Checking Grove Errors

If Grove cannot create a Workload or a Pod, it records the error in the `status.lastErrors` field of the PodClique:

```bash
kubectl get pclq -n default -l app.kubernetes.io/part-of=my-inference
kubectl get pclq <pclq-name> -n default -o jsonpath='{.status.lastErrors}'
```

## Scaling Behavior

Grove creates a Workload once and does not update it. The Kueue backend therefore supports only scaling that adds or removes whole PodGangs.

### Scaling a PCS

Each PCS replica has its own PodGang and Workload. Scaling out adds replicas that Kueue admits independently. Scaling in deletes the PodGangs of the removed replicas, and Kubernetes garbage collection deletes their Workloads.

```bash
kubectl scale pcs my-multinode-inference -n default --replicas=2
```

### Scaling a PCSG

The initial replicas of a PCSG share the PodGang of their PCS replica. Each replica that you add above the PCSG's `minAvailable` gets its own PodGang and Workload. Scaling back down to `minAvailable` deletes those PodGangs and their Workloads.

```bash
kubectl scale pcsg my-multinode-inference-0-prefill -n default --replicas=4
```

A PCSG can also autoscale with `scaleConfig`. Grove requires `scaleConfig.minReplicas` to be at least `minAvailable`, so the autoscaler only adds and removes the PodGangs above it.

Do not scale a PCSG to 0. Grove rejects a count between 1 and `minAvailable`, but it accepts 0. Scaling to 0 removes Pods from the shared PodGang, and its Workload keeps reserving quota for them.

### Scaling a PodClique

The Kueue backend rejects any change to a PodClique's `replicas`, through `kubectl scale` or through an update of the PodClique. Such a change would resize a PodGang that already has a Workload. For the same reason, the backend rejects a PCS that sets `autoScalingConfig` on a PodClique.

```bash
kubectl scale pclq my-inference-0-prefill -n default --replicas=4
```

The request fails with an error that starts with `kueue backend does not support scaling a PodClique`.

To scale a role, define it as a PCSG instead. For example, a PCSG whose only PodClique has `replicas: 1` adds one Pod of the role for each PCSG replica.

## Limitations

### PodCliqueSet Rules

The Kueue backend rejects a PCS that breaks any of the following rules when you create or update it:

- **A PCSG and its PodCliques must set `minAvailable` equal to `replicas`.** Kueue allows `minCount` on at most one PodSet per Workload, and Grove reserves it for a standalone PodClique. A PCSG is therefore always admitted in full.
- **At most one standalone PodClique can set `minAvailable` lower than `replicas`.** It is the only PodSet that can use Kueue's `minCount`.
- **A PodClique cannot set `autoScalingConfig`.** See [Scaling a PodClique](#scaling-a-podclique).
- **The PodGang of a PCS replica can have at most 18 PodGroups.** Kueue v0.20.1 allows at most 18 PodSets per Workload. The PodGang has one PodGroup for each standalone PodClique, plus one for each PodClique of each PCSG replica up to `minAvailable`.
- **A PodClique cannot resolve both a required and a preferred topology domain.** Kueue rejects a Pod with more than one topology annotation. Inherited constraints count. For example, a PCS-level `pack.required: rack` with a PodClique-level `pack.preferred: host` is rejected, as in the [Preferred Host Packing](topology-aware-scheduling.md#preferred-host-packing) example.

### Other Limitations

- **The `Coherent` update strategy is not supported.** A Coherent update splits the Pods of a PodClique across PodGroups in several PodGangs, but Grove sizes each of their PodSets for the full PodClique. Grove does not reject the strategy at admission. Use the default `RollingRecreate` strategy or `OnDelete`.
- **Do not change PodClique `replicas` in the PCS template.** Grove does not apply the change to existing PodCliques. The Kueue backend still uses the new value for the Workloads and Pods that Grove creates later, so they no longer match the running PodCliques.
- **An existing PCS cannot move to the Kueue backend.** `schedulerName` is immutable on a PCS. Recreate the PCS to move it to Kueue.
- **Kueue places each PodClique independently.** A topology constraint cannot keep the PodCliques of a PodGang in one domain. See [Topology-Aware Placement](#topology-aware-placement).
- **Partial admission undercounts quota.** See [Partial Admission](#partial-admission).
- **No gang scheduling at bind time.** Kueue reserves quota for a whole Workload at once. The scheduler then binds each Pod on its own. If nodes change between admission and binding, some Pods of an admitted Workload can stay pending until `waitForPodsReady` evicts the Workload.

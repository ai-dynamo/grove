# Environment Variables For Pod-Discovery

This guide explains the environment variables that Grove automatically injects into your pods and shows practical examples of using them for service discovery, coordination, and configuration in distributed systems.

## Prerequisites

Before starting this guide:
- Review the [core concepts tutorial](./core-concepts/overview.md) to understand Grove's primitives
- Read the [Pod Naming guide](./pod-and-resource-naming-conventions/01_overview.md) to understand Grove's naming conventions
- Set up a cluster following the [installation guide](../installation.md), the two options are:
  - [A local KIND demo cluster](../installation.md#local-kind-cluster-set-up): Create the cluster with `make kind-up FAKE_NODES=40`, set `KUBECONFIG` env variable as directed, and run `make deploy`
  - [A remote Kubernetes cluster](../installation.md#remote-cluster-set-up) with [Grove installed from package](../installation.md#install-grove-from-package)

> **Note:** The examples in this guide require at least one real node to run actual containers and inspect environment variables in pod logs. The KIND cluster created with `make kind-up FAKE_NODES=40` includes one real control-plane node alongside the fake nodes, which is sufficient for these examples.

## Overview

Grove automatically injects environment variables into every container and init container in your pods. These environment variables provide runtime information that your application can use for:
- **Service discovery**: Finding other pods in your system
- **Coordination**: Understanding your role in a distributed system
- **Configuration**: Self-configuring based on your position in the hierarchy

However, before we get to the environment variables it is important to first make a distinction between a Pod's Name and its Hostname.

## Understanding Pod Names vs. Hostnames in Kubernetes

A common source of confusion is the difference between a pod's **name** and its **hostname**. Understanding this distinction is essential for using Grove's environment variables correctly.

### Pod Name (Kubernetes Resource Identifier)

The **pod name** is the unique identifier for the Pod resource in Kubernetes (stored in `metadata.name`). When Grove creates pods, it uses Kubernetes' `generateName` feature, which appends a random 5-character suffix to ensure uniqueness:

```
<pclq-name>-<random-suffix>
Example: env-demo-standalone-0-frontend-abc12
```

This name is what you see when running `kubectl get pods`. However, you **cannot use this name for DNS-based service discovery** because the random suffix is unpredictable.

### Hostname (DNS-Resolvable Identity)

The **hostname** is a separate field (`spec.hostname`) that Grove explicitly sets on each pod. Unlike the pod name, the hostname follows a **deterministic pattern**:

```
<pclq-name>-<pod-index>
Example: env-demo-standalone-0-frontend-0
```

Grove also sets the pod's **subdomain** (`spec.subdomain`) to match the headless service name. Grove automatically creates a headless service for each PodCliqueSet replica, so you don't need to create one yourself. In Kubernetes, when a pod has both `hostname` and `subdomain` set, and a matching headless service exists, the pod becomes DNS-resolvable at:

```
<hostname>.<subdomain>.<namespace>.svc.cluster.local
```

For example:
```
env-demo-standalone-0-frontend-0.env-demo-standalone-0.default.svc.cluster.local
```

### Why This Matters

| Attribute | Pod Name | Hostname |
|-----------|----------|----------|
| Source | `metadata.name` | `spec.hostname` |
| Pattern | `<pclq-name>-<random-suffix>` | `<pclq-name>-<pod-index>` |
| Predictable? | ❌ No (random suffix) | ✅ Yes (index-based) |
| DNS resolvable? | ❌ No | ✅ Yes (with headless service) |
| Use case | `kubectl` commands, logs | Service discovery, pod-to-pod communication |

**The environment variables Grove provides (`GROVE_PCLQ_NAME`, `GROVE_PCLQ_POD_INDEX`, `GROVE_HEADLESS_SERVICE`) give you the building blocks to construct the hostname-based FQDN, not the pod name.** This is why pod discovery in Grove is deterministic and doesn't require knowledge of random suffixes.

With this explained we can now get into the environment variables Grove provides.

## Environment Variables Reference

### Available in All Pods

These environment variables are injected into every pod managed by Grove:

| Environment Variable | Description | Example Value |
|---------------------|-------------|---------------|
| `GROVE_PCS_NAME` | Name of the PodCliqueSet (as specified in metadata.name) | `my-service` |
| `GROVE_PCS_INDEX` | Replica index of the PodCliqueSet (0-based) | `0` |
| `GROVE_PCLQ_NAME` | Fully qualified PodClique resource name (see structure below) | `my-service-0-frontend` |
| `GROVE_HEADLESS_SERVICE` | FQDN of the headless service for the PodCliqueSet replica | `my-service-0.default.svc.cluster.local` |
| `GROVE_PCLQ_POD_INDEX` | Index of this pod within its PodClique (0-based) | `2` |

**Understanding `GROVE_PCLQ_NAME`:**
- For **standalone PodCliques**: `<pcs-name>-<pcs-index>-<pclq-template-name>`
  - Example: `my-service-0-frontend`
- For **PodCliques in a PCSG**: `<pcs-name>-<pcs-index>-<pcsg-template-name>-<pcsg-index>-<pclq-template-name>`
  - Example: `my-service-0-model-instance-0-leader`

### Additional Variables for PodCliqueScalingGroup Pods

If a pod belongs to a PodClique that is part of a PodCliqueScalingGroup, these additional environment variables are available:

| Environment Variable | Description | Example Value |
|---------------------|-------------|---------------|
| `GROVE_PCSG_NAME` | Fully qualified PCSG resource name (see structure below) | `my-service-0-model-instance` |
| `GROVE_PCSG_INDEX` | Replica index of the PodCliqueScalingGroup (0-based) | `1` |
| `GROVE_PCSG_TEMPLATE_NUM_PODS` | Total number of pods in the PCSG template | `4` |

**Understanding `GROVE_PCSG_NAME`:**
- Structure: `<pcs-name>-<pcs-index>-<pcsg-template-name>`
- Example: `my-service-0-model-instance`
- **Note:** This does NOT include the PCSG replica index. To construct a sibling PodClique name within the same PCSG replica, use: `$GROVE_PCSG_NAME-$GROVE_PCSG_INDEX-<pclq-template-name>`

**Note:** `GROVE_PCSG_TEMPLATE_NUM_PODS` represents the total number of pods defined in the PodCliqueScalingGroup template, calculated as the sum of replicas across all PodCliques in the PCSG. For example, if a PCSG has 1 leader replica and 3 worker replicas, this value would be 4. This value does not change based on scaling, so is only guaranteed to be accurate at startup.

## Example 1: Standalone PodClique Environment Variables

Let's deploy a simple PodCliqueSet with a standalone PodClique and inspect the environment variables.

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: env-demo-standalone
  namespace: default
spec:
  replicas: 1
  template:
    cliques:
    - name: frontend
      spec:
        replicas: 2
        podSpec:
          containers:
          - name: app
            image: busybox:latest
            command: ["/bin/sh"]
            args: 
            - "-c"
            - |
              echo "=== Grove Environment Variables ==="
              echo "GROVE_PCS_NAME=$GROVE_PCS_NAME"
              echo "GROVE_PCS_INDEX=$GROVE_PCS_INDEX"
              echo "GROVE_PCLQ_NAME=$GROVE_PCLQ_NAME"
              echo "GROVE_HEADLESS_SERVICE=$GROVE_HEADLESS_SERVICE"
              echo "GROVE_PCLQ_POD_INDEX=$GROVE_PCLQ_POD_INDEX"
              echo ""
              echo "=== Pod Name vs Hostname ==="
              echo "Pod Name (random suffix): $POD_NAME"
              echo "Hostname (deterministic): $GROVE_PCLQ_NAME-$GROVE_PCLQ_POD_INDEX"
              echo ""
              echo "My FQDN: $GROVE_PCLQ_NAME-$GROVE_PCLQ_POD_INDEX.$GROVE_HEADLESS_SERVICE"
              echo ""
              echo "Sleeping..."
              sleep infinity
            env:
            - name: POD_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
            resources:
              requests:
                cpu: "10m"
                memory: "32Mi"
```

### Deploy and Inspect

In this example, we will deploy the file: [standalone-env-vars.yaml](../../operator/samples/user-guide/naming-and-env-vars/standalone-env-vars.yaml)

```bash
# NOTE: Run the following commands from the `/path/to/grove/operator` directory,
# where `/path/to/grove` is the root of your cloned Grove repository.
kubectl apply -f samples/user-guide/naming-and-env-vars/standalone-env-vars.yaml

# Wait for pods to be ready (skip if you are manually verifying they are ready)
kubectl wait --for=condition=ready pod -l app.kubernetes.io/part-of=env-demo-standalone --timeout=60s

# List the pods
kubectl get pods -l app.kubernetes.io/part-of=env-demo-standalone
```

You should see output similar to:
```
NAME                                 READY   STATUS    RESTARTS   AGE
env-demo-standalone-0-frontend-abc12   1/1     Running   0          30s
env-demo-standalone-0-frontend-def34   1/1     Running   0          30s
```

Now, let's check the logs of one of the pods to see the environment variables:

```bash
# Get the name of the first pod
POD_NAME=$(kubectl get pods -l app.kubernetes.io/part-of=env-demo-standalone -o jsonpath='{.items[0].metadata.name}')

# View the logs
kubectl logs $POD_NAME
```

You should see output like:
```
=== Grove Environment Variables ===
GROVE_PCS_NAME=env-demo-standalone
GROVE_PCS_INDEX=0
GROVE_PCLQ_NAME=env-demo-standalone-0-frontend
GROVE_HEADLESS_SERVICE=env-demo-standalone-0.default.svc.cluster.local
GROVE_PCLQ_POD_INDEX=0

=== Pod Name vs Hostname ===
Pod Name (random suffix): env-demo-standalone-0-frontend-abc12
Hostname (deterministic): env-demo-standalone-0-frontend-0

My FQDN: env-demo-standalone-0-frontend-0.env-demo-standalone-0.default.svc.cluster.local

Sleeping...
```

**Key Observations:**
- The **pod name** (`env-demo-standalone-0-frontend-abc12`) has a random suffix—this is the Kubernetes resource identifier, not used for DNS
- The **hostname** (constructed as `$GROVE_PCLQ_NAME-$GROVE_PCLQ_POD_INDEX`) is deterministic—this is what you use for service discovery
- `GROVE_PCLQ_NAME` contains the fully qualified PodClique name without the random suffix
- `GROVE_PCLQ_POD_INDEX` tells us this is the first pod (index 0) in the PodClique
- `GROVE_HEADLESS_SERVICE` provides the FQDN for the headless service, so the full DNS address is: `$GROVE_PCLQ_NAME-$GROVE_PCLQ_POD_INDEX.$GROVE_HEADLESS_SERVICE`

### Cleanup

```bash
kubectl delete pcs env-demo-standalone
```

---

## Example 2: PodCliqueScalingGroup with Leader-Worker Communication

This example demonstrates a more complex scenario with a PodCliqueScalingGroup containing leader and worker pods. We'll show how workers can use environment variables to discover and connect to their leader.

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: env-demo-pcsg
  namespace: default
spec:
  replicas: 1
  template:
    cliques:
    - name: leader
      spec:
        roleName: leader
        replicas: 1
        podSpec:
          containers:
          - name: leader
            image: busybox:latest
            command: ["/bin/sh"]
            args:
            - "-c"
            - |
              echo "=== Leader Pod ==="
              echo "GROVE_PCS_NAME=$GROVE_PCS_NAME"
              echo "GROVE_PCS_INDEX=$GROVE_PCS_INDEX"
              echo "GROVE_PCLQ_NAME=$GROVE_PCLQ_NAME"
              echo "GROVE_PCSG_NAME=$GROVE_PCSG_NAME"
              echo "GROVE_PCSG_INDEX=$GROVE_PCSG_INDEX"
              echo "GROVE_PCLQ_POD_INDEX=$GROVE_PCLQ_POD_INDEX"
              echo "GROVE_PCSG_TEMPLATE_NUM_PODS=$GROVE_PCSG_TEMPLATE_NUM_PODS"
              echo ""
              echo "My FQDN: $GROVE_PCLQ_NAME-$GROVE_PCLQ_POD_INDEX.$GROVE_HEADLESS_SERVICE"
              echo ""
              echo "Listening for worker connections..."
              sleep infinity
            resources:
              requests:
                cpu: "10m"
                memory: "32Mi"
    - name: worker
      spec:
        roleName: worker
        replicas: 3
        podSpec:
          containers:
          - name: worker
            image: busybox:latest
            command: ["/bin/sh"]
            args:
            - "-c"
            - |
              echo "=== Worker Pod ==="
              echo "GROVE_PCS_NAME=$GROVE_PCS_NAME"
              echo "GROVE_PCS_INDEX=$GROVE_PCS_INDEX"
              echo "GROVE_PCLQ_NAME=$GROVE_PCLQ_NAME"
              echo "GROVE_PCSG_NAME=$GROVE_PCSG_NAME"
              echo "GROVE_PCSG_INDEX=$GROVE_PCSG_INDEX"
              echo "GROVE_PCLQ_POD_INDEX=$GROVE_PCLQ_POD_INDEX"
              echo "GROVE_PCSG_TEMPLATE_NUM_PODS=$GROVE_PCSG_TEMPLATE_NUM_PODS"
              echo ""
              echo "=== Constructing Leader Address ==="
              # The leader PodClique name is: PCSG name + PCSG index + "-leader"
              LEADER_PCLQ_NAME="$GROVE_PCSG_NAME-$GROVE_PCSG_INDEX-leader"
              # Leader is always pod index 0 since there's only 1 leader replica
              LEADER_POD_INDEX=0
              # Construct the leader's FQDN
              LEADER_FQDN="$LEADER_PCLQ_NAME-$LEADER_POD_INDEX.$GROVE_HEADLESS_SERVICE"
              echo "Connecting to leader at: $LEADER_FQDN"
              echo ""
              echo "Sleeping..."
              sleep infinity
            resources:
              requests:
                cpu: "10m"
                memory: "32Mi"
    podCliqueScalingGroups:
    - name: model-instance
      cliqueNames: [leader, worker]
      replicas: 2
```

### Deploy and Inspect

In this example, we will deploy the file: [pcsg-env-vars.yaml](../../operator/samples/user-guide/naming-and-env-vars/pcsg-env-vars.yaml)

```bash
# NOTE: Run the following commands from the `/path/to/grove/operator` directory,
# where `/path/to/grove` is the root of your cloned Grove repository.
kubectl apply -f samples/user-guide/naming-and-env-vars/pcsg-env-vars.yaml

# Wait for pods to be ready (skip if you are manually verifying they are ready)
kubectl wait --for=condition=ready pod -l app.kubernetes.io/part-of=env-demo-pcsg --timeout=60s

# List all pods
kubectl get pods -l app.kubernetes.io/part-of=env-demo-pcsg -o wide
```

You should see 8 pods (2 PCSG replicas × (1 leader + 3 workers)):
```
NAME                                                READY   STATUS    RESTARTS   AGE
env-demo-pcsg-0-model-instance-0-leader-abc12       1/1     Running   0          45s
env-demo-pcsg-0-model-instance-0-worker-def34       1/1     Running   0          45s
env-demo-pcsg-0-model-instance-0-worker-ghi56       1/1     Running   0          45s
env-demo-pcsg-0-model-instance-0-worker-jkl78       1/1     Running   0          45s
env-demo-pcsg-0-model-instance-1-leader-mno90       1/1     Running   0          45s
env-demo-pcsg-0-model-instance-1-worker-pqr12       1/1     Running   0          45s
env-demo-pcsg-0-model-instance-1-worker-stu34       1/1     Running   0          45s
env-demo-pcsg-0-model-instance-1-worker-vwx56       1/1     Running   0          45s
```

Let's inspect the leader logs from the first PCSG replica:

```bash
# Get the leader pod name from the first PCSG replica (model-instance-0)
LEADER_POD=$(kubectl get pods -l app.kubernetes.io/part-of=env-demo-pcsg -o name | grep "model-instance-0-leader" | head -1)

kubectl logs $LEADER_POD
```

You should see:
```
=== Leader Pod ===
GROVE_PCS_NAME=env-demo-pcsg
GROVE_PCS_INDEX=0
GROVE_PCLQ_NAME=env-demo-pcsg-0-model-instance-0-leader
GROVE_PCSG_NAME=env-demo-pcsg-0-model-instance
GROVE_PCSG_INDEX=0
GROVE_PCLQ_POD_INDEX=0
GROVE_PCSG_TEMPLATE_NUM_PODS=4

My FQDN: env-demo-pcsg-0-model-instance-0-leader-0.env-demo-pcsg-0.default.svc.cluster.local

Listening for worker connections...
```

Now let's check a worker pod from the same PCSG replica:

```bash
# Get a worker pod name from the first PCSG replica (model-instance-0)
WORKER_POD=$(kubectl get pods -l app.kubernetes.io/part-of=env-demo-pcsg -o name | grep "model-instance-0-worker" | head -1)

kubectl logs $WORKER_POD
```

You should see:
```
=== Worker Pod ===
GROVE_PCS_NAME=env-demo-pcsg
GROVE_PCS_INDEX=0
GROVE_PCLQ_NAME=env-demo-pcsg-0-model-instance-0-worker
GROVE_PCSG_NAME=env-demo-pcsg-0-model-instance
GROVE_PCSG_INDEX=0
GROVE_PCLQ_POD_INDEX=0
GROVE_PCSG_TEMPLATE_NUM_PODS=4

=== Constructing Leader Address ===
Connecting to leader at: env-demo-pcsg-0-model-instance-0-leader-0.env-demo-pcsg-0.default.svc.cluster.local

Sleeping...
```

**Key Observations:**
- Both the leader and worker share the same `GROVE_PCSG_NAME` (`env-demo-pcsg-0-model-instance`) and `GROVE_PCSG_INDEX` (`0`), confirming they belong to the same PCSG replica
- The worker successfully constructed the leader's FQDN using environment variables:
  - Leader PodClique name: `$GROVE_PCSG_NAME-$GROVE_PCSG_INDEX-leader`
  - Leader pod index: `0` (since there's only 1 leader replica)
  - Headless service: `$GROVE_HEADLESS_SERVICE`
- `GROVE_PCSG_TEMPLATE_NUM_PODS` is `4` (1 leader + 3 workers), which can be useful for workers to know the total cluster size

### Verifying Leader-Worker Connectivity

Let's verify that workers can actually reach their leader using DNS:

```bash
# Get a worker pod from the first PCSG replica
WORKER_POD=$(kubectl get pods -l app.kubernetes.io/part-of=env-demo-pcsg -o name | grep "model-instance-0-worker" | head -1)

# Try to resolve the leader from the worker
kubectl exec $WORKER_POD -- nslookup env-demo-pcsg-0-model-instance-0-leader-0.env-demo-pcsg-0.default.svc.cluster.local
```

You should see that the DNS name resolves successfully, confirming that the worker can discover its leader.

### Cleanup

```bash
kubectl delete pcs env-demo-pcsg
```

---

## Common Patterns for Using Environment Variables

Here are some common patterns for using Grove environment variables in your applications:

### Pattern 1: Constructing Pod FQDNs

To construct the FQDN for any pod in your PodClique:

```bash
# For your own FQDN
MY_FQDN="$GROVE_PCLQ_NAME-$GROVE_PCLQ_POD_INDEX.$GROVE_HEADLESS_SERVICE"

# For another pod in the same PodClique (e.g., pod index 3)
OTHER_POD_INDEX=3
OTHER_POD_FQDN="$GROVE_PCLQ_NAME-$OTHER_POD_INDEX.$GROVE_HEADLESS_SERVICE"
```

### Pattern 2: Finding the Leader in a PCSG

If you're in a worker pod and need to connect to the leader (assuming the leader PodClique is named "leader"):

```bash
# Construct the leader's PodClique name: PCSG name + PCSG index + "-leader"
LEADER_PCLQ_NAME="$GROVE_PCSG_NAME-$GROVE_PCSG_INDEX-leader"

# Leader is typically at index 0
LEADER_FQDN="$LEADER_PCLQ_NAME-0.$GROVE_HEADLESS_SERVICE"
```

### Pattern 3: Discovering All Peers in a PodClique

If you need to construct addresses for all pods in your PodClique:

```bash
# Assuming you have a way to know the total number of replicas in your PodClique
# (this could be passed in as a custom env var or ConfigMap)
TOTAL_REPLICAS=5

for i in $(seq 0 $((TOTAL_REPLICAS - 1))); do
  PEER_FQDN="$GROVE_PCLQ_NAME-$i.$GROVE_HEADLESS_SERVICE"
  echo "Peer $i: $PEER_FQDN"
done
```

### Pattern 4: Determining Your Role in a PCSG

You can use the `GROVE_PCLQ_NAME` to determine which role this pod plays:

```bash
# Extract the role from the PodClique name
# The role is typically the last component after the final hyphen
ROLE=$(echo $GROVE_PCLQ_NAME | awk -F- '{print $NF}')

if [ "$ROLE" = "leader" ]; then
  echo "I am a leader pod"
elif [ "$ROLE" = "worker" ]; then
  echo "I am a worker pod"
fi
```

### Pattern 5: Using Headless Service for Service Discovery

The `GROVE_HEADLESS_SERVICE` provides a DNS name that resolves to all pods in the PodCliqueSet replica:

```bash
# This will return DNS records for all pods in the same PodCliqueSet replica
nslookup $GROVE_HEADLESS_SERVICE
```

---

## Key Takeaways

1. **Automatic Context Injection**  
   Grove injects a consistent set of environment variables into every pod, giving each container precise runtime context about *where it sits* in the PodCliqueSet hierarchy.

2. **Explicit, Predictable Addressing**  
   Grove does not hide service topology. Instead, it provides the building blocks (`GROVE_PCS_NAME`, `GROVE_PCLQ_NAME`, `GROVE_PCSG_NAME`, indices, and the headless service) so applications can **explicitly construct the addresses they need**, including those of other PodCliques.

3. **Stable Pod Identity**  
   `GROVE_PCLQ_POD_INDEX` gives each pod a stable, deterministic identity within its PodClique, making it easy to assign ranks, shard work, or implement leader/worker logic.

4. **Scaling-Group Awareness**  
   For pods in a PodCliqueScalingGroup, Grove exposes additional variables that identify the PCSG replica and its composition. This allows components to understand which *logical unit (super-pod)* they belong to and how many peers are expected.

5. **Designed for Distributed Systems**  
   Grove’s environment variables are intentionally low-level and composable. They are meant to support a wide range of distributed system patterns—leader election, sharding, rendezvous, collective communication—without imposing a fixed discovery or coordination model.
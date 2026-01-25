# Hands-On Examples

This guide walks through deploying example PodCliqueSets, observing the environment variables that Grove injects, and using them to construct FQDNs for pod discovery. Make sure you've read the [Environment Variables Reference](./02_env_var_reference.md) guide first to understand the environment variables we'll see in action.

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

In this example, we will deploy the file: [standalone-env-vars.yaml](../../../operator/samples/user-guide/03_environment-variables-for-pod-discovery/standalone-env-vars.yaml)

```bash
# NOTE: Run the following commands from the `/path/to/grove/operator` directory,
# where `/path/to/grove` is the root of your cloned Grove repository.
kubectl apply -f samples/user-guide/03_environment-variables-for-pod-discovery/standalone-env-vars.yaml

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
- The **hostname** (constructed as `$GROVE_PCLQ_NAME-$GROVE_PCLQ_POD_INDEX`) is deterministic—this is what you use for pod discovery
- `GROVE_PCLQ_NAME` contains the fully qualified PodClique name without the random suffix
- `GROVE_PCLQ_POD_INDEX` tells us this is the first pod (index 0) in the PodClique
- `GROVE_HEADLESS_SERVICE` provides the headless service domain, which you combine with the hostname to construct the pod's FQDN: `$GROVE_PCLQ_NAME-$GROVE_PCLQ_POD_INDEX.$GROVE_HEADLESS_SERVICE`

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

In this example, we will deploy the file: [pcsg-env-vars.yaml](../../../operator/samples/user-guide/03_environment-variables-for-pod-discovery/pcsg-env-vars.yaml)

```bash
# NOTE: Run the following commands from the `/path/to/grove/operator` directory,
# where `/path/to/grove` is the root of your cloned Grove repository.
kubectl apply -f samples/user-guide/03_environment-variables-for-pod-discovery/pcsg-env-vars.yaml

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

## Next Steps

Now that you've seen the environment variables in action, continue to [Common Patterns and Takeaways](./04_common-patterns-and-takeaways.md) for reusable patterns you can adapt for your applications and a summary of key concepts.


# Hands-On Example: Multi-Node Disaggregated Inference

This guide walks through deploying a realistic example that demonstrates both naming patterns and the requirement for unique PodClique names. Make sure you've read the [Naming Conventions](./02_naming-conventions.md) guide first to understand the patterns we'll see in action.

## The Example System

Let's deploy an example with the structure of a multi-node disaggregated inference system:
- A standalone frontend component
- A prefill PodCliqueScalingGroup with leader and workers
- A decode PodCliqueScalingGroup with leader and workers

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: multinode-disaggregated
  namespace: default
spec:
  replicas: 1
  template:
    cliques:
    # Standalone PodClique
    - name: frontend
      spec:
        replicas: 2
        podSpec:
          tolerations:
          - key: fake-node
            operator: Equal
            value: "true"
            effect: NoSchedule
          containers:
          - name: frontend
            image: nginx:latest
            command: ["/bin/sh"]
            args: ["-c", "echo 'Frontend' && hostname && sleep infinity"]
            resources:
              requests:
                cpu: "10m"
                memory: "32Mi"
    # Prefill PodCliqueScalingGroup PodCliques
    - name: pleader
      spec:
        roleName: pleader
        replicas: 1
        podSpec:
          tolerations:
          - key: fake-node
            operator: Equal
            value: "true"
            effect: NoSchedule
          containers:
          - name: pleader
            image: nginx:latest
            command: ["/bin/sh"]
            args: ["-c", "echo 'Prefill Leader' && hostname && sleep infinity"]
            resources:
              requests:
                cpu: "10m"
                memory: "32Mi"
    - name: pworker
      spec:
        roleName: pworker
        replicas: 3
        podSpec:
          tolerations:
          - key: fake-node
            operator: Equal
            value: "true"
            effect: NoSchedule
          containers:
          - name: pworker
            image: nginx:latest
            command: ["/bin/sh"]
            args: ["-c", "echo 'Prefill Worker' && hostname && sleep infinity"]
            resources:
              requests:
                cpu: "10m"
                memory: "32Mi"
    # Decode PodCliqueScalingGroup PodCliques
    - name: dleader
      spec:
        roleName: dleader
        replicas: 1
        podSpec:
          tolerations:
          - key: fake-node
            operator: Equal
            value: "true"
            effect: NoSchedule
          containers:
          - name: dleader
            image: nginx:latest
            command: ["/bin/sh"]
            args: ["-c", "echo 'Decode Leader' && hostname && sleep infinity"]
            resources:
              requests:
                cpu: "10m"
                memory: "32Mi"
    - name: dworker
      spec:
        roleName: dworker
        replicas: 2
        podSpec:
          tolerations:
          - key: fake-node
            operator: Equal
            value: "true"
            effect: NoSchedule
          containers:
          - name: dworker
            image: nginx:latest
            command: ["/bin/sh"]
            args: ["-c", "echo 'Decode Worker' && hostname && sleep infinity"]
            resources:
              requests:
                cpu: "10m"
                memory: "32Mi"
    podCliqueScalingGroups:
    - name: prefill
      cliqueNames: [pleader, pworker]
      replicas: 2
    - name: decode
      cliqueNames: [dleader, dworker]
      replicas: 1
```

## Why Unique PodClique Names Matter

Notice in the YAML above:
- Prefill PCSG uses `pleader` and `pworker` (not just `leader` and `worker`)
- Decode PCSG uses `dleader` and `dworker` (not just `leader` and `worker`)

**Why?**

PodClique names **must be unique within a PodCliqueSet**. Reusing generic names like `leader` and `worker` across multiple PodCliqueScalingGroups is therefore **not allowed**.

More importantly, this reflects **Grove's core philosophy**.

In Grove, a `PodCliqueSet` is how you describe the **components of your system**. Each `PodClique` represents one component with a clearly defined, globally meaningful role. PodClique names are meant to be stable identifiers for those roles (e.g. prefill leader, decode worker), not local labels that change depending on how components are grouped or scaled.

When multiple components need to be co-scheduled, scaled together, and function as a single logical unit (e.g. a *super-pod* for prefill), they are placed into a `PodCliqueScalingGroup`. The scaling group defines *how components run together*, but it does not redefine or merge their identities.

That said, Grove also aims to keep names concise. Rather than naming a `PodClique` `prefill-leader`, we use short prefixes such as `p` (prefill) and `d` (decode) to preserve uniqueness while keeping names short and readable. The `PodCliqueScalingGroup` name already conveys which logical unit a PodClique belongs to, allowing the PodClique name itself to remain compact without losing clarity.

This is why `pleader`/`pworker` and `dleader`/`dworker` are the intended and recommended pattern.


## Deploy and Observe

In this example, we will deploy the file: [multinode-disaggregated-with-frontend.yaml](../../../operator/samples/user-guide/naming-and-env-vars/multinode-disaggregated-with-frontend.yaml)

```bash
# NOTE: Run the following commands from the `/path/to/grove/operator` directory,
# where `/path/to/grove` is the root of your cloned Grove repository.
kubectl apply -f samples/user-guide/naming-and-env-vars/multinode-disaggregated-with-frontend.yaml

# Get all pods - observe the self-documenting names
kubectl get pods -l app.kubernetes.io/part-of=multinode-disaggregated -o wide
```

You should see output like:
```
NAME                                               READY   STATUS    RESTARTS   AGE
multinode-disaggregated-0-decode-0-dleader-abc12   1/1     Running   0          45s
multinode-disaggregated-0-decode-0-dworker-def34   1/1     Running   0          45s
multinode-disaggregated-0-decode-0-dworker-ghi56   1/1     Running   0          45s
multinode-disaggregated-0-frontend-jkl78           1/1     Running   0          45s
multinode-disaggregated-0-frontend-mno90           1/1     Running   0          45s
multinode-disaggregated-0-prefill-0-pleader-pqr12  1/1     Running   0          45s
multinode-disaggregated-0-prefill-0-pworker-stu34  1/1     Running   0          45s
multinode-disaggregated-0-prefill-0-pworker-vwx56  1/1     Running   0          45s
multinode-disaggregated-0-prefill-0-pworker-yza78  1/1     Running   0          45s
multinode-disaggregated-0-prefill-1-pleader-bcd90  1/1     Running   0          45s
multinode-disaggregated-0-prefill-1-pworker-efg12  1/1     Running   0          45s
multinode-disaggregated-0-prefill-1-pworker-hij34  1/1     Running   0          45s
multinode-disaggregated-0-prefill-1-pworker-klm56  1/1     Running   0          45s
```

## Parsing the Naming Hierarchy

Looking at this output, you can immediately understand the system structure:

**1. Standalone PodClique (frontend):**
```
multinode-disaggregated-0-frontend-*
```
- Simpler naming: `<pcs>-<pcs-idx>-<pclq>-<suffix>`
- 2 frontend pods serving requests

**2. PodCliqueScalingGroup (prefill) - 2 replicas:**
```
multinode-disaggregated-0-prefill-0-*
multinode-disaggregated-0-prefill-1-*
```
- Deeper hierarchy: `<pcs>-<pcs-idx>-<pcsg>-<pcsg-idx>-<pclq>-<suffix>`
- Each replica has 1 `pleader` + 3 `pworker` pods
- Two independent prefill clusters

**3. PodCliqueScalingGroup (decode) - 1 replica:**
```
multinode-disaggregated-0-decode-0-*
```
- Same deep hierarchy as prefill
- Has 1 `dleader` + 2 `dworker` pods
- One decode cluster

**4. Clear role identification through naming:**
- `frontend` = frontend component
- `pleader` = prefill leader
- `pworker` = prefill worker
- `dleader` = decode leader
- `dworker` = decode worker

## Examining Resources

Let's look at the underlying Grove resources:

```bash
# List PodCliques
kubectl get pclq
```

Output:
```
NAME                                       AGE
multinode-disaggregated-0-decode-0-dleader  2m
multinode-disaggregated-0-decode-0-dworker  2m
multinode-disaggregated-0-frontend          2m
multinode-disaggregated-0-prefill-0-pleader 2m
multinode-disaggregated-0-prefill-0-pworker 2m
multinode-disaggregated-0-prefill-1-pleader 2m
multinode-disaggregated-0-prefill-1-pworker 2m
```

**Observations:**
- Standalone PodClique: `multinode-disaggregated-0-frontend`
- Prefill PCSG PodCliques: Names include `prefill-0` or `prefill-1` to show which replica
- Decode PCSG PodCliques: Names include `decode-0`
- All names are unique and under 63 characters

```bash
# List PodCliqueScalingGroups
kubectl get pcsg
```

Output:
```
NAME                                AGE
multinode-disaggregated-0-decode    2m
multinode-disaggregated-0-prefill   2m
```

The PCSG names clearly identify the two scaling groups.

## Name Length Analysis

Let's verify our names fit within the 63-character limit:

- Longest pod name: `multinode-disaggregated-0-prefill-1-pworker-klm56`
  - Characters: 49 (well under 63) ✅

- PodClique names (max): `multinode-disaggregated-0-prefill-1-pworker`
  - Characters: 43 (under 63) ✅

- PCSG names: `multinode-disaggregated-0-prefill`
  - Characters: 33 (under 63) ✅

All resource names are comfortably within the limit!

## Cleanup

```bash
kubectl delete pcs multinode-disaggregated
```

---

## Next Steps

Now that you've seen the naming conventions in action, check out:
- The [Key Takeaways](./02_naming-conventions.md#key-takeaways) section for a summary of naming best practices
- The [Environment Variables guide](../environment-variables.md) to learn how to use these names programmatically for service discovery


# PodCliqueScalingGroup

In the [previous guide](./02_pcs_and_pclq_intro.md) we covered some hands on examples on how to use PodCliqueSet and PodCliques. In this guide we go over some hands-on examples on how to use PodCliqueScalingGroup to represent multinode components.

Refer to [Overview](./01_overview.md) for instructions on how to run the examples in this guide.

## Example 3: Multi-Node Aggregated Inference

Now we introduce **PodCliqueScalingGroup** for multi-node deployments, where multiple pods collectively make up a single instance of the application and must scale together.
These setups are increasingly common for serving large models that do not fit on one node and consequently one model instance ends up spanning multiple nodes and therefore multiple pods. In thse cases, inference frameworks typically follow a leader-worker topology: one leader pod coordinates work for N workers that connect to it.
Scaling out means replicating the entire unit (1 leader + N workers) to create additional model instances.
A PodCliqueScalingGroup encodes this by grouping the relevant PodCliques and scaling them in lockstep while preserving the pod ratios.
The example below shows how to model this leader-worker pattern in Grove:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: multinode-aggregated
  namespace: default
spec:
  replicas: 1
  template:
    cliques:
    - name: leader
      spec:
        roleName: leader
        replicas: 1
        podSpec:  # This is a standard Kubernetes PodSpec
          tolerations:
          - key: fake-node
            operator: Equal
            value: "true"
            effect: NoSchedule
          containers:
          - name: model-leader
            image: nginx:latest
            command: ["/bin/sh"]
            args: ["-c", "echo 'Model Leader (Aggregated) on node:' && hostname && sleep infinity"]
            resources:
              requests:
                cpu: "10m"
                memory: "32Mi"
    - name: worker
      spec:
        roleName: worker
        replicas: 3
        podSpec:  # This is a standard Kubernetes PodSpec
          tolerations:
          - key: fake-node
            operator: Equal
            value: "true"
            effect: NoSchedule
          containers:
          - name: model-worker
            image: nginx:latest
            command: ["/bin/sh"]
            args: ["-c", "echo 'Model Worker (Aggregated) on node:' && hostname && sleep infinity"]
            resources:
              requests:
                cpu: "10m"
                memory: "32Mi"
    podCliqueScalingGroups:
    - name: model-instance
      cliqueNames: [leader, worker]
      replicas: 2
```

### **Key Points:**
- **PodCliqueScalingGroup** named `model-cluster` with `replicas: 2`
- Creates 2 model isntances, each with 1 leader + 3 workers
- Total pods: 2 × (1 leader + 3 workers) = 8 pods
- Scaling the group preserves the 1:3 leader-to-worker ratio

### **Deploy:**

In this example, we will deploy the file: [multi-node-aggregated.yaml](../../../operator/samples/user-guide/01_core-concepts/multi-node-aggregated.yaml)
```bash
# NOTE: Run the following commands from the `/path/to/grove/operator` directory,
# where `/path/to/grove` is the root of your cloned Grove repository.
kubectl apply -f samples/user-guide/01_core-concepts/multi-node-aggregated.yaml
kubectl get pods -l app.kubernetes.io/part-of=multinode-aggregated -o wide
```

After running you should observe
```bash
rohanv@rohanv-mlt operator % kubectl get pods -l app.kubernetes.io/part-of=multinode-aggregated -o wide
NAME                                                   READY   STATUS    RESTARTS   AGE   IP            NODE            NOMINATED NODE   READINESS GATES
multinode-aggregated-0-model-instance-0-leader-zq4j5   1/1     Running   0          11s   10.244.2.0    fake-node-002   <none>           <none>
multinode-aggregated-0-model-instance-0-worker-7kcv7   1/1     Running   0          11s   10.244.13.0   fake-node-013   <none>           <none>
multinode-aggregated-0-model-instance-0-worker-829k9   1/1     Running   0          11s   10.244.7.0    fake-node-007   <none>           <none>
multinode-aggregated-0-model-instance-0-worker-vrmrb   1/1     Running   0          11s   10.244.10.0   fake-node-010   <none>           <none>
multinode-aggregated-0-model-instance-1-leader-t8ptp   1/1     Running   0          11s   10.244.6.0    fake-node-006   <none>           <none>
multinode-aggregated-0-model-instance-1-worker-bscfv   1/1     Running   0          11s   10.244.4.0    fake-node-004   <none>           <none>
multinode-aggregated-0-model-instance-1-worker-sgd6r   1/1     Running   0          11s   10.244.17.0   fake-node-017   <none>           <none>
multinode-aggregated-0-model-instance-1-worker-vpkwb   1/1     Running   0          11s   10.244.18.0   fake-node-018   <none>           <none>
```
Note how within the same `multinode-aggregated-0` PodCliqueSet there are two replicas of the `model-instance` PodCliqueScalingGroup, `model-instance-0` and `model-instance-1`, each consisting of a `leader` PodClique with one replica and a `worker` PodClique with 3 replicas.

### **Scaling**

As mentioned before, PodCliqueScalingGroups represent "super-pods" where scaling means replicating the pods in constituent PodCliques together while preserving the ratios. To illustrate this, run the following command to scale the replicas of the `model-instance` PodCliqueScalingGroup from two to three. `pcsg` is short for PodCliqueScalingGroup and can be used to reference PodCliqueScalingGroup as a resource in kubectl commands. Similar to standalone PodCliques, PodCliqueScalingGroups include the name of the PodCliqueSet in their name to disambiguate from replicas of the same PodCliqueScalingGroup in a different PodCliqueSet. This is why the scaling command references `multinode-aggregated-0-model-instance` instead of `model-instance`

```bash
kubectl scale pcsg multinode-aggregated-0-model-instance --replicas=3
```
After running this command you should observe

```bash
rohanv@rohanv-mlt operator % kubectl get pods -l app.kubernetes.io/part-of=multinode-aggregated -o wide
NAME                                                   READY   STATUS    RESTARTS   AGE   IP            NODE            NOMINATED NODE   READINESS GATES
multinode-aggregated-0-model-instance-0-leader-zq4j5   1/1     Running   0          68m   10.244.2.0    fake-node-002   <none>           <none>
multinode-aggregated-0-model-instance-0-worker-7kcv7   1/1     Running   0          68m   10.244.13.0   fake-node-013   <none>           <none>
multinode-aggregated-0-model-instance-0-worker-829k9   1/1     Running   0          68m   10.244.7.0    fake-node-007   <none>           <none>
multinode-aggregated-0-model-instance-0-worker-vrmrb   1/1     Running   0          68m   10.244.10.0   fake-node-010   <none>           <none>
multinode-aggregated-0-model-instance-1-leader-t8ptp   1/1     Running   0          68m   10.244.6.0    fake-node-006   <none>           <none>
multinode-aggregated-0-model-instance-1-worker-bscfv   1/1     Running   0          68m   10.244.4.0    fake-node-004   <none>           <none>
multinode-aggregated-0-model-instance-1-worker-sgd6r   1/1     Running   0          68m   10.244.17.0   fake-node-017   <none>           <none>
multinode-aggregated-0-model-instance-1-worker-vpkwb   1/1     Running   0          68m   10.244.18.0   fake-node-018   <none>           <none>
multinode-aggregated-0-model-instance-2-leader-w5wfm   1/1     Running   0          25s   10.244.19.0   fake-node-019   <none>           <none>
multinode-aggregated-0-model-instance-2-worker-59qm9   1/1     Running   0          25s   10.244.14.0   fake-node-014   <none>           <none>
multinode-aggregated-0-model-instance-2-worker-9qqnx   1/1     Running   0          25s   10.244.20.0   fake-node-020   <none>           <none>
multinode-aggregated-0-model-instance-2-worker-qqnl8   1/1     Running   0          25s   10.244.5.0    fake-node-005   <none>           <none>
```
Note how now there is now an additional leader pod `multinode-aggregated-0-model-instance-2-leader` and 3 additional worker pods `multinode-aggregated-0-model-instance-2-leader`. This demonstrates how PodCliqueScalingGroups allow you to create "super-pods" that are a group of pods that scale together.

While you can scale the PodCliqueScalingGroup to replicate the "super-pod" unit, you can still scale the individual PodCliques on a given PodCliqueScalingGroup replica. Before showing an example of that it is important to explain that the naming format of PodCliques that are in a PodCliqueScalingGroup is different than for standalone PodCliques. For standalone PodCliques the format is `<pcs-name>-<pcs-replica-idx>-<pclq-name>` whereas for PodCliques that are part of a PodCliqueScalingGroup, the format is `<pcs-name>-<pcs-replica-idx>-<pcsg-name>-<pcsg-replica-idx>-<pclq-name>`. To illustrate this run the following command to show the names of the leader and worker PodCliques

```bash
kubectl get pclq
```
After running this you should observe the following PodCliques, with the naming format in line with what we described above.
```bash
rohanv@rohanv-mlt operator % kubectl get pclq
NAME                                             AGE
multinode-aggregated-0-model-instance-0-leader   95m
multinode-aggregated-0-model-instance-0-worker   95m
multinode-aggregated-0-model-instance-1-leader   95m
multinode-aggregated-0-model-instance-1-worker   95m
multinode-aggregated-0-model-instance-2-leader   27m
multinode-aggregated-0-model-instance-2-worker   27m
```
Now that we know the PodClique names we can scale the replicas on a specific PodClique similar to previous examples. Run the following command to increase `multinode-aggregated-0-model-instance-0-worker` from three replicas to four

```bash
kubectl scale pclq multinode-aggregated-0-model-instance-0-worker --replicas=4
```
After running this you will observe:

```bash
rohanv@rohanv-mlt operator % kubectl get pods -l app.kubernetes.io/part-of=multinode-aggregated -o wide
NAME                                                   READY   STATUS    RESTARTS   AGE   IP            NODE            NOMINATED NODE   READINESS GATES
multinode-aggregated-0-model-instance-0-leader-zq4j5   1/1     Running   0          12h   10.244.2.0    fake-node-002   <none>           <none>
multinode-aggregated-0-model-instance-0-worker-7kcv7   1/1     Running   0          12h   10.244.13.0   fake-node-013   <none>           <none>
multinode-aggregated-0-model-instance-0-worker-829k9   1/1     Running   0          12h   10.244.7.0    fake-node-007   <none>           <none>
multinode-aggregated-0-model-instance-0-worker-gjc87   1/1     Running   0          83s   10.244.1.0    fake-node-001   <none>           <none>
multinode-aggregated-0-model-instance-0-worker-vrmrb   1/1     Running   0          12h   10.244.10.0   fake-node-010   <none>           <none>
multinode-aggregated-0-model-instance-1-leader-t8ptp   1/1     Running   0          12h   10.244.6.0    fake-node-006   <none>           <none>
multinode-aggregated-0-model-instance-1-worker-bscfv   1/1     Running   0          12h   10.244.4.0    fake-node-004   <none>           <none>
multinode-aggregated-0-model-instance-1-worker-sgd6r   1/1     Running   0          12h   10.244.17.0   fake-node-017   <none>           <none>
multinode-aggregated-0-model-instance-1-worker-vpkwb   1/1     Running   0          12h   10.244.18.0   fake-node-018   <none>           <none>
multinode-aggregated-0-model-instance-2-leader-w5wfm   1/1     Running   0          11h   10.244.19.0   fake-node-019   <none>           <none>
multinode-aggregated-0-model-instance-2-worker-59qm9   1/1     Running   0          11h   10.244.14.0   fake-node-014   <none>           <none>
multinode-aggregated-0-model-instance-2-worker-9qqnx   1/1     Running   0          11h   10.244.20.0   fake-node-020   <none>           <none>
multinode-aggregated-0-model-instance-2-worker-qqnl8   1/1     Running   0          11h   10.244.5.0    fake-node-005   <none>           <none>
```
Note how there are now four pods belonging to `multinode-aggregated-0-model-instance-0-worker`

**When to scale what:**
- **Scale the PodCliqueScalingGroup** (`kubectl scale pcsg ...`) when you want to add more complete model instances (e.g., adding a second leader+workers unit for more capacity)
- **Scale individual PodCliques** (`kubectl scale pclq ...`) when you want to adjust the number of pods in a specific role within one instance (e.g., adding more workers to an existing leader-worker group as frameworks support elastic world sizes)

### Cleanup
To teardown the example delete the `multinode-aggregated` PodCliqueSet, the operator will tear down all the constituent pieces

```bash
kubectl delete pcs multinode-aggregated
```

---

## Example 4: Multi-Node Disaggregated Inference

You can put together all the things we've covered to represent the most complex scenario: multi-node disaggregated serving where both the prefill and decode components are multi-node. We represent this in Grove by creating PodCliqueScalingGroups for both prefill and decode. Additionally each PodCliqueScalingGroup consists of two PodCliques, one for the leader and one for the worker. 

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
    - name: pleader
      spec:
        roleName: pleader
        replicas: 1
        podSpec:  # This is a standard Kubernetes PodSpec
          tolerations:
          - key: fake-node
            operator: Equal
            value: "true"
            effect: NoSchedule
          containers:
          - name: prefill-leader
            image: nginx:latest
            command: ["/bin/sh"]
            args: ["-c", "echo 'Prefill Leader on node:' && hostname && sleep infinity"]
            resources:
              requests:
                cpu: "10m"
                memory: "32Mi"
    - name: pworker
      spec:
        roleName: pworker
        replicas: 4
        podSpec:  # This is a standard Kubernetes PodSpec
          tolerations:
          - key: fake-node
            operator: Equal
            value: "true"
            effect: NoSchedule
          containers:
          - name: prefill-worker
            image: nginx:latest
            command: ["/bin/sh"]
            args: ["-c", "echo 'Prefill Worker on node:' && hostname && sleep infinity"]
            resources:
              requests:
                cpu: "10m"
                memory: "32Mi"
    - name: dleader
      spec:
        roleName: dleader
        replicas: 1
        podSpec:  # This is a standard Kubernetes PodSpec
          tolerations:
          - key: fake-node
            operator: Equal
            value: "true"
            effect: NoSchedule
          containers:
          - name: decode-leader
            image: nginx:latest
            command: ["/bin/sh"]
            args: ["-c", "echo 'Decode Leader on node:' && hostname && sleep infinity"]
            resources:
              requests:
                cpu: "10m"
                memory: "32Mi"
    - name: dworker
      spec:
        roleName: dworker
        replicas: 2
        podSpec:  # This is a standard Kubernetes PodSpec
          tolerations:
          - key: fake-node
            operator: Equal
            value: "true"
            effect: NoSchedule
          containers:
          - name: decode-worker
            image: nginx:latest
            command: ["/bin/sh"]
            args: ["-c", "echo 'Decode Worker on node:' && hostname && sleep infinity"]
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

### **Key Points:**
- Two independent **PodCliqueScalingGroups**: `prefill` and `decode`
- Each PodCliqueScalingGroup (PCSG) has PodCliques for leader and worker `pleader`,`pworker`,`dleader`,`dworker`. PodClique names need to be unique within a PodCliqueSet which is why we do not name the PodCliques `leader` and `worker` unlike the previous example
- Prefill PCSG consists of : 2 replicas × (1 leader + 4 workers) = 10 pods
- Decode PCSG: 1 replica × (1 leader + 2 workers) = 3 pods
- Each PCSG can scale independently based on workload demands
- Each PCSG can have different resource allocations

### **Deploy**

In this example, we will deploy the file: [multi-node-disaggregated.yaml](../../../operator/samples/user-guide/01_core-concepts/multi-node-disaggregated.yaml)
```bash
# NOTE: Run the following commands from the `/path/to/grove/operator` directory,
# where `/path/to/grove` is the root of your cloned Grove repository.
kubectl apply -f samples/user-guide/01_core-concepts/multi-node-disaggregated.yaml
kubectl get pods -l app.kubernetes.io/part-of=multinode-disaggregated -o wide
```

After running you will observe
```bash
rohanv@rohanv-mlt operator % kubectl get pods -l app.kubernetes.io/part-of=multinode-disaggregated -o wide
NAME                                                READY   STATUS    RESTARTS   AGE   IP            NODE            NOMINATED NODE   READINESS GATES
multinode-disaggregated-0-decode-0-dleader-khqxf    1/1     Running   0          35s   10.244.19.0   fake-node-019   <none>           <none>
multinode-disaggregated-0-decode-0-dworker-6d7cq    1/1     Running   0          35s   10.244.18.0   fake-node-018   <none>           <none>
multinode-disaggregated-0-decode-0-dworker-g6ksp    1/1     Running   0          35s   10.244.20.0   fake-node-020   <none>           <none>
multinode-disaggregated-0-prefill-0-pleader-f5w5j   1/1     Running   0          35s   10.244.6.0    fake-node-006   <none>           <none>
multinode-disaggregated-0-prefill-0-pworker-7spmm   1/1     Running   0          35s   10.244.9.0    fake-node-009   <none>           <none>
multinode-disaggregated-0-prefill-0-pworker-jgnkq   1/1     Running   0          35s   10.244.10.0   fake-node-010   <none>           <none>
multinode-disaggregated-0-prefill-0-pworker-v49gf   1/1     Running   0          35s   10.244.11.0   fake-node-011   <none>           <none>
multinode-disaggregated-0-prefill-0-pworker-xst4z   1/1     Running   0          35s   10.244.2.0    fake-node-002   <none>           <none>
multinode-disaggregated-0-prefill-1-pleader-xwf45   1/1     Running   0          35s   10.244.16.0   fake-node-016   <none>           <none>
multinode-disaggregated-0-prefill-1-pworker-6jrpz   1/1     Running   0          35s   10.244.15.0   fake-node-015   <none>           <none>
multinode-disaggregated-0-prefill-1-pworker-bd5ct   1/1     Running   0          35s   10.244.14.0   fake-node-014   <none>           <none>
multinode-disaggregated-0-prefill-1-pworker-fdl7s   1/1     Running   0          35s   10.244.7.0    fake-node-007   <none>           <none>
multinode-disaggregated-0-prefill-1-pworker-kpplp   1/1     Running   0          35s   10.244.4.0    fake-node-004   <none>           <none>
```
Note how we have one replica of the decode PodCliqueScalingGroup and two replicas of the prefill PodCliqueScalingGroup. Also note how each prefill replica consists of 4 pods whereas each decode replica consists of 3 pods. This independence is critical to disaggregated serving as you can independently specify and scale prefill and decode components.

### **Scaling**
Each of the PodCliqueScalingGroups and PodCliques can be scaled similar to the [previous example](#scaling-3). If you scale a PodCliqueScalingGroup it will replicate all its PodCliques while maintaining the replica ratio between them. If you scale a PodClique it will horizontally scale like a deployment.

### Cleanup
To teardown the example delete the `multinode-disaggregated` PodCliqueSet, the operator will tear down all the constituent pieces

```bash
kubectl delete pcs multinode-disaggregated
```
In the [next guide](./04_takeaways.md) we showcase how Grove can represent an arbitrary number of components and summarize the key takeaways.

# PodCliqueSet and PodClique

In this guide we go over some hands-on examples showcasing how to use a PodCliqueSet and PodCliques.

Refer to [Overview](./overview.md) for instructions on how to run the examples in this guide.

## Example 1: Single-Node Aggregated Inference

In this simplest scenario, each pod is a complete model instance that can service requests. This is mapped to a single standalone PodClique within the PodCliqueSet. The PodClique provides horizontal scaling capabilities at the model replica level similar to a ReplicaSet (with gang termination behavior), and the PodCliqueSet provides horizontal scaling capabilities at the system level (useful for things such as canary deployments, A/B testing, and spreading across availability zones for high availability).

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: single-node-aggregated
  namespace: default
spec:
  replicas: 1
  template:
    cliques:
    - name: model-worker
      spec:
        replicas: 2
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
```

### **Key Points:**
- Single PodClique named `model-worker`
- `replicas: 2` creates 2 instances for horizontal scaling
- Each replica handles complete inference pipeline
- Tolerations allow scheduling on fake nodes for demo, remove if you are trying to deploy on a real cluster

### **Deploy:**

In this example, we will deploy the file: [single-node-aggregated.yaml](../../../operator/samples/user-guide/concept-overview/single-node-aggregated.yaml)
```bash
# NOTE: Run the following commands from the `/path/to/grove/operator` directory,
# where `/path/to/grove` is the root of your cloned Grove repository.
kubectl apply -f samples/user-guide/concept-overview/single-node-aggregated.yaml
kubectl get pods -l app.kubernetes.io/part-of=single-node-aggregated -o wide
```

If you are using the demo-cluster you should observe output similar to
```bash
rohanv@rohanv-mlt operator % kubectl get pods -l app.kubernetes.io/part-of=single-node-aggregated -o wide
NAME                                          READY   STATUS    RESTARTS   AGE   IP            NODE            NOMINATED NODE   READINESS GATES
single-node-aggregated-0-model-worker-n9gcq   1/1     Running   0          18m   10.244.7.0    fake-node-007   <none>           <none>
single-node-aggregated-0-model-worker-zfhbb   1/1     Running   0          18m   10.244.15.0   fake-node-015   <none>           <none>
```
The demo-cluster consists of fake nodes spawned by [KWOK](https://kwok.sigs.k8s.io/) so the pods won't have any logs, but if you deployed to a real cluster you should observe the echo command complete successfully. Note that the spawned pods have descriptive names. Grove intentionally aims to allow users to immediately be able to map pods to their specifications in the yaml. All pods are prefixed with `single-node-aggregated-0` to represent they are part of the first replica of the `single-node-aggregated` PodCliqueSet. After the PodCliqueSet identifier is `model-worker`, signifying that the pods belong to the `model-worker` PodClique.

### **Scaling**
As mentioned earlier, you can scale the `model-worker` PodClique to get more model replicas similar to a ReplicaSet. For instance run the following command to increase the replicas on `model-worker` from 2 to 4. `pclq` is short for PodClique and can be used to reference PodClique as a resource in kubectl commands. Note that the name of the PodClique provided to the scaling command is `single-node-aggregated-0-model-worker` and not just `model-worker`. This is necessary since the PodCliqueSet can be replicated (as we will see later) and therefore the name of PodCliques includes the PodCliqueSet replica they belong to.
```bash
kubectl scale pclq single-node-aggregated-0-model-worker --replicas=4
```
After running you will observe there are now 4 `model-worker` pods belonging to the `single-node-aggregated-0` PodCliqueSet
```
rohanv@rohanv-mlt operator % kubectl get pods -l app.kubernetes.io/part-of=single-node-aggregated -o wide
NAME                                          READY   STATUS    RESTARTS   AGE   IP            NODE            NOMINATED NODE   READINESS GATES
single-node-aggregated-0-model-worker-jvgdd   1/1     Running   0          22s   10.244.11.0   fake-node-011   <none>           <none>
single-node-aggregated-0-model-worker-n9gcq   1/1     Running   0          44m   10.244.7.0    fake-node-007   <none>           <none>
single-node-aggregated-0-model-worker-tjb78   1/1     Running   0          22s   10.244.8.0    fake-node-008   <none>           <none>
single-node-aggregated-0-model-worker-zfhbb   1/1     Running   0          44m   10.244.15.0   fake-node-015   <none>           <none>
```
You can also scale the entire PodCliqueSet. For instance run the following command to increase the replicas on `single-node-aggregated` to 3. `pcs` is short for PodCliqueSet and can be used to reference PodCliqueSet as a resource in kubectl commands.

```bash
kubectl scale pcs single-node-aggregated --replicas=3
```
After running you will observe
```
rohanv@rohanv-mlt operator % kubectl get pods -l app.kubernetes.io/part-of=single-node-aggregated -o wide
NAME                                          READY   STATUS    RESTARTS   AGE    IP            NODE            NOMINATED NODE   READINESS GATES
single-node-aggregated-0-model-worker-2xl58   1/1     Running   0          99s    10.244.7.0    fake-node-007   <none>           <none>
single-node-aggregated-0-model-worker-5jlfr   1/1     Running   0          2m7s   10.244.20.0   fake-node-020   <none>           <none>
single-node-aggregated-0-model-worker-78974   1/1     Running   0          2m7s   10.244.5.0    fake-node-005   <none>           <none>
single-node-aggregated-0-model-worker-zn888   1/1     Running   0          99s    10.244.13.0   fake-node-013   <none>           <none>
single-node-aggregated-1-model-worker-kkmsq   1/1     Running   0          74s    10.244.10.0   fake-node-010   <none>           <none>
single-node-aggregated-1-model-worker-pn5cm   1/1     Running   0          74s    10.244.15.0   fake-node-015   <none>           <none>
single-node-aggregated-2-model-worker-h5xqk   1/1     Running   0          74s    10.244.3.0    fake-node-003   <none>           <none>
single-node-aggregated-2-model-worker-p4kjj   1/1     Running   0          74s    10.244.16.0   fake-node-016   <none>           <none>
```

Note how there are pods belonging to `single-node-aggregated-0`, `single-node-aggregated-1`, and `single-node-aggregated-2`, representing 3 different PodCliqueSet replicas. Each of these PodCliqueSet replicas contains a PodClique resulting in three distinct PodCliques - `single-node-aggregated-0-model-worker`, `single-node-aggregated-1-model-worker`, and `single-node-aggregated-2-model-worker`. Also note how the `single-node-aggregated-1-model-worker` and `single-node-aggregated-2-model-worker` PodCliques only have two replicas. This is in line with k8s patterns and occurs because the template that was applied (single-node-aggregated.yaml) specified the number of replicas on `model-worker` as 2. To scale them up you would have to apply `kubectl scale pclq` commands like previously done for `single-node-aggregated-0-model-worker` above.

### Cleanup
To teardown the example delete the `single-node-aggregated` PodCliqueSet, the operator will tear down all the constituent pieces

```bash
kubectl delete pcs single-node-aggregated
```

---

## Example 2: Single-Node Disaggregated Inference

Here we separate prefill and decode operations into different workers, allowing independent scaling of each component. Modelling this in Grove primitives is simple, in the previous example that demonstrated aggregated serving, we had one PodClique for the model-worker, which handled both prefill and decode. To disaggregate prefill and decode, we simply create two PodCliques, one for prefill, and one for decode. Note that the clique names can be set to whatever your want, although we recommend setting them up to match the component they represent (e.g prefill, decode).

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: single-node-disaggregated
  namespace: default
spec:
  replicas: 1
  template:
    cliques:
    - name: prefill
      spec:
        roleName: prefill
        replicas: 3
        podSpec:  # This is a standard Kubernetes PodSpec
          tolerations:
          - key: fake-node
            operator: Equal
            value: "true"
            effect: NoSchedule
          containers:
          - name: prefill
            image: nginx:latest
            command: ["/bin/sh"]
            args: ["-c", "echo 'Prefill Worker on node:' && hostname && sleep infinity"]
            resources:
              requests:
                cpu: "10m"
                memory: "32Mi"
    - name: decode
      spec:
        roleName: decode
        replicas: 2
        podSpec:  # This is a standard Kubernetes PodSpec
          tolerations:
          - key: fake-node
            operator: Equal
            value: "true"
            effect: NoSchedule
          containers:
          - name: decode
            image: nginx:latest
            command: ["/bin/sh"]
            args: ["-c", "echo 'Decode Worker on node:' && hostname && sleep infinity"]
            resources:
              requests:
                cpu: "10m"
                memory: "32Mi"
```

### **Key Points:**
- Two separate PodCliques: `prefill` and `decode`
- Independent scaling: We start with 3 prefill workers, 2 decode workers and can scale them independently based on workload characteristics
- Different resource requirements for each component are supported (in the example prefill requests 2 cpu and decode only 1)

### **Deploy**

In this example, we will deploy the file: [single-node-disaggregated.yaml](../../../operator/samples/user-guide/concept-overview/single-node-disaggregated.yaml)
```bash
# NOTE: Run the following commands from the `/path/to/grove/operator` directory,
# where `/path/to/grove` is the root of your cloned Grove repository.
kubectl apply -f samples/user-guide/concept-overview/single-node-disaggregated.yaml
kubectl get pods -l app.kubernetes.io/part-of=single-node-disaggregated -o wide
```

After running you will observe:
```bash
rohanv@rohanv-mlt operator % kubectl get pods -l app.kubernetes.io/part-of=single-node-disaggregated -o wide
NAME                                        READY   STATUS    RESTARTS   AGE   IP            NODE            NOMINATED NODE   READINESS GATES
single-node-disaggregated-0-decode-bvl94    1/1     Running   0          29s   10.244.6.0    fake-node-006   <none>           <none>
single-node-disaggregated-0-decode-wlqxj    1/1     Running   0          29s   10.244.4.0    fake-node-004   <none>           <none>
single-node-disaggregated-0-prefill-tnw26   1/1     Running   0          29s   10.244.17.0   fake-node-017   <none>           <none>
single-node-disaggregated-0-prefill-xvvtk   1/1     Running   0          29s   10.244.11.0   fake-node-011   <none>           <none>
single-node-disaggregated-0-prefill-zglvn   1/1     Running   0          29s   10.244.9.0    fake-node-009   <none>           <none>
```
Note how within the `single-node-disaggregated-0` PodCliqueSet replica there are pods from the `prefill` PodClique and `decode` PodClique

### **Scaling**
You can scale the `prefill` and `decode` PodCliques the same way the [`model-worker` PodClique was scaled](#scaling) in the previous example. 

Additionally, the `single-node-disaggregated` PodCliqueSet can be scaled the same way the `single-node-aggregated` PodCliqueSet was scaled in the previous example. We show an example to demonstrate how when PodCliqueSets are scaled, all constituent PodCliques are replicated, underscoring why scaling PodCliqueSets should be treated as scaling the entire system (useful for canary deployments, A/B testing, or high availability across zones).
```bash
kubectl scale pcs single-node-disaggregated --replicas=2
```
After running this you will observe
```bash
rohanv@rohanv-mlt operator % kubectl get pods -l app.kubernetes.io/part-of=single-node-disaggregated -o wide
NAME                                        READY   STATUS    RESTARTS   AGE   IP            NODE            NOMINATED NODE   READINESS GATES
single-node-disaggregated-0-decode-9fvsj    1/1     Running   0          77s   10.244.13.0   fake-node-013   <none>           <none>
single-node-disaggregated-0-decode-xw62b    1/1     Running   0          77s   10.244.18.0   fake-node-018   <none>           <none>
single-node-disaggregated-0-prefill-dfss8   1/1     Running   0          77s   10.244.8.0    fake-node-008   <none>           <none>
single-node-disaggregated-0-prefill-fgkrc   1/1     Running   0          77s   10.244.14.0   fake-node-014   <none>           <none>
single-node-disaggregated-0-prefill-ljnms   1/1     Running   0          77s   10.244.11.0   fake-node-011   <none>           <none>
single-node-disaggregated-1-decode-f9tmf    1/1     Running   0          10s   10.244.16.0   fake-node-016   <none>           <none>
single-node-disaggregated-1-decode-psd6h    1/1     Running   0          10s   10.244.10.0   fake-node-010   <none>           <none>
single-node-disaggregated-1-prefill-2mktc   1/1     Running   0          10s   10.244.7.0    fake-node-007   <none>           <none>
single-node-disaggregated-1-prefill-4smsf   1/1     Running   0          10s   10.244.3.0    fake-node-003   <none>           <none>
single-node-disaggregated-1-prefill-5n6qv   1/1     Running   0          10s   10.244.12.0   fake-node-012   <none>           <none>
```
Note how now there is `single-node-disaggregated-0` and `single-node-disaggregated-1` each with their own `prefill` and `decode` PodCliques that can be scaled.

### Cleanup
To teardown the example delete the `single-node-disaggregated` PodCliqueSet, the operator will tear down all the constituent pieces

```bash
kubectl delete pcs single-node-disaggregated
```

In the [next guide](./pcsg_intro.md) we showcase how to use PodCliqueScalingGroup to represent multi-node components

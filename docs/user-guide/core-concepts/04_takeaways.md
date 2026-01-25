# Takeaways

Refer to [Overview](./01_overview.md) for instructions on how to run the examples in this guide.

## Example 5: Complete Inference Pipeline

The [previous examples](./03_pcsg_intro.md) have focused on mapping various inference workloads into Grove primitives, focusing on the model instances. However, the primitives are generic and the point of Grove is to allow the user to represent as many components as they'd like. To illustrate this point we now provide an example where we represent additional components such as a frontend and vision encoder. To add additional components you simply add additional PodCliques and PodCliqueScalingGroups into the PodCliqueSet

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: comp-inf-ppln
  namespace: default
spec:
  replicas: 1
  template:
    cliques:
    #single node components
    - name: frontend
      spec:
        roleName: frontend
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
            args: ["-c", "echo 'Frontend Service on node:' && hostname && sleep infinity"]
            resources:
              requests:
                cpu: "10m"
                memory: "32Mi"
    - name: vision-encoder
      spec:
        roleName: vision-encoder
        replicas: 1
        podSpec:
          tolerations:
          - key: fake-node
            operator: Equal
            value: "true"
            effect: NoSchedule
          containers:
          - name: vision-encoder
            image: nginx:latest
            command: ["/bin/sh"]
            args: ["-c", "echo 'Vision Encoder on node:' && hostname && sleep infinity"]
            resources:
              requests:
                cpu: "10m"
                memory: "32Mi"
    # Multi-node components
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
        podSpec:
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
        podSpec:
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
        podSpec:
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
      replicas: 1
    - name: decode-cluster
      cliqueNames: [dleader, dworker]
      replicas: 1
```

**Architecture Summary:**
- **Single-node components**: Frontend (2 replicas), Vision Encoder (1 replica)
- **Multi-node prefill**: 1 replica × (1 leader + 4 workers) = 5 pods
- **Multi-node decode**: 1 replica × (1 leader + 2 workers) = 3 pods
- **Total**: 11 pods providing a complete inference pipeline

**Deploy and explore:**

In this example, we will deploy the file: [complete-inference-pipeline.yaml](../../../operator/samples/user-guide/core-concepts/complete-inference-pipeline.yaml)
```bash
# NOTE: Run the following commands from the `/path/to/grove/operator` directory,
# where `/path/to/grove` is the root of your cloned Grove repository.
kubectl apply -f samples/user-guide/core-concepts/complete-inference-pipeline.yaml
kubectl get pods -l app.kubernetes.io/part-of=comp-inf-ppln -o wide
```

After running you will observe
```bash
rohanv@rohanv-mlt operator % kubectl get pods -l app.kubernetes.io/part-of=comp-inf-ppln -o wide
NAME                                             READY   STATUS    RESTARTS   AGE   IP            NODE            NOMINATED NODE   READINESS GATES
comp-inf-ppln-0-decode-cluster-0-dleader-wr7r2   1/1     Running   0          51s   10.244.8.0    fake-node-008   <none>           <none>
comp-inf-ppln-0-decode-cluster-0-dworker-4nm98   1/1     Running   0          51s   10.244.5.0    fake-node-005   <none>           <none>
comp-inf-ppln-0-decode-cluster-0-dworker-wqzb9   1/1     Running   0          51s   10.244.2.0    fake-node-002   <none>           <none>
comp-inf-ppln-0-frontend-fxxsg                   1/1     Running   0          51s   10.244.1.0    fake-node-001   <none>           <none>
comp-inf-ppln-0-frontend-shp8h                   1/1     Running   0          51s   10.244.20.0   fake-node-020   <none>           <none>
comp-inf-ppln-0-prefill-0-pleader-vgz8n          1/1     Running   0          51s   10.244.17.0   fake-node-017   <none>           <none>
comp-inf-ppln-0-prefill-0-pworker-95jls          1/1     Running   0          51s   10.244.9.0    fake-node-009   <none>           <none>
comp-inf-ppln-0-prefill-0-pworker-k8bck          1/1     Running   0          51s   10.244.4.0    fake-node-004   <none>           <none>
comp-inf-ppln-0-prefill-0-pworker-qlsb9          1/1     Running   0          51s   10.244.14.0   fake-node-014   <none>           <none>
comp-inf-ppln-0-prefill-0-pworker-wfxdg          1/1     Running   0          51s   10.244.15.0   fake-node-015   <none>           <none>
comp-inf-ppln-0-vision-encoder-rwvz5             1/1     Running   0          51s   10.244.7.0    fake-node-007   <none>           <none>
```

### Cleanup
To teardown the example delete the `comp-inf-ppln` PodCliqueSet, the operator will tear down all the constituent pieces

```bash
kubectl delete pcs comp-inf-ppln
```
---

## Key Takeaways

Overall Grove primitives aim to provide a declarative way to express all the components of your system, allowing you to stitch together an arbitrary amount of single-node and multi-node components.

### When to Use Each Component

1. **PodClique**:
  -Standalone Manner (not part of PodCliqueScalingGroup)
   - Single-node components that can scale independently
   - Examples: Frontend, API gateway, single-node model instances
  -Within a PodCliqueScalingGroup
   - Specific roles within a multi-node component, e.g. leader, worker

2. **PodCliqueScalingGroup**:
   - Multi-node components where one instance spans multiple pods and there are potentially different roles each pod takes (e.g. leader worker)
   - When scaled creates new copy of constituent PodCliques while maintaining the ratio between them

3. **PodCliqueSet**:
   - Top level Custom Resource for representing the entire system
   - Allows for replicating the entire system for blue-green deployments and/or availability across zones
   - Contains user specified number of PodCliques and PodCliqueScalingGroups



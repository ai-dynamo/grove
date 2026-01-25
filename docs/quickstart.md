# Quickstart: Deploy Your First Workload with Grove

This guide will walk you through deploying a simple disaggregated AI workload using Grove in about 10 minutes.

## What You'll Learn

By the end of this quickstart, you'll understand how to:
- Deploy a multi-component workload with Grove
- Scale components independently or as a group
- Observe gang scheduling in action

## Prerequisites

- A Kubernetes cluster (we'll use kind for local testing)
- `kubectl` installed and configured
- Docker Desktop running (for kind)

## Understanding the Example Workload

We'll deploy a simple workload that mimics a disaggregated inference setup with four components:

- **Role A (pca)**: Auto-scaling component (e.g., routing layer) - scales based on CPU
- **Role B (pcb)** and **Role C (pcc)**: Tightly-coupled components (e.g., prefill + decode) - must scale together as a group
- **Role D (pcd)**: Fixed-size component (e.g., model cache) - doesn't auto-scale

This demonstrates Grove's key capabilities: individual auto-scaling, grouped scaling, and gang scheduling.

## Step 1: Set Up Your Local Environment

### Install Grove Operator

Follow the [installation guide](installation.md) to:
1. Create a kind cluster with `make kind-up`
2. Export the KUBECONFIG
3. Deploy Grove with `make deploy`

**Quick check:** Verify Grove operator is running:
```bash
kubectl get pods -l app.kubernetes.io/name=grove-operator
```

You should see a pod in `Running` status.

## Step 2: Deploy Your First PodCliqueSet

Create a PodCliqueSet that defines all four components:

```bash
cd operator
kubectl apply -f samples/simple/simple1.yaml
```

**What just happened?** Grove created:
- 1 `PodCliqueSet` (the top-level resource)
- 4 `PodCliques` (one for each component role)
- 1 `PodCliqueScalingGroup` (grouping pcb and pcc)
- 1 `PodGang` (for gang scheduling)
- 9 `Pods` (3 for pca, 2 each for pcb/pcc/pcd)

## Step 3: Observe the Resources

Watch as Grove creates and schedules all resources:

```bash
kubectl get pcs,pclq,pcsg,pg,pod -o wide
```

Expected output:
```
NAME                            AGE
podcliqueset.grove.io/simple1   34s

NAME                                     AGE
podclique.grove.io/simple1-0-pca         33s
podclique.grove.io/simple1-0-pcd         33s
podclique.grove.io/simple1-0-sga-0-pcb   33s
podclique.grove.io/simple1-0-sga-0-pcc   33s

NAME                                           AGE
podcliquescalinggroup.grove.io/simple1-0-sga   33s

NAME                                   AGE
podgang.scheduler.grove.io/simple1-0   33s

NAME                                  READY   STATUS    RESTARTS   AGE
pod/grove-operator-699c77979f-7x2zc   1/1     Running   0          51s
pod/simple1-0-pca-pkl2b               1/1     Running   0          33s
pod/simple1-0-pca-s7dz2               1/1     Running   0          33s
pod/simple1-0-pca-wjfqz               1/1     Running   0          33s
pod/simple1-0-pcd-l4vnk               1/1     Running   0          33s
pod/simple1-0-pcd-s7687               1/1     Running   0          33s
pod/simple1-0-sga-0-pcb-m9shj         1/1     Running   0          33s
pod/simple1-0-sga-0-pcb-vnrqw         1/1     Running   0          33s
pod/simple1-0-sga-0-pcc-g8rg8         1/1     Running   0          33s
pod/simple1-0-sga-0-pcc-hx4zn         1/1     Running   0          33s
```

**Key observation:** Notice all pods reached `Running` state together - that's gang scheduling! Grove ensured all pods were scheduled before starting any of them.

## Step 4: Scale a PodCliqueScalingGroup

Scale the tightly-coupled components (pcb and pcc) as a group:

```bash
kubectl scale pcsg simple1-0-sga --replicas=2
```

Observe the new resources:
```bash
kubectl get pcs,pclq,pcsg,pg,pod -o wide
```

**What happened?**
- Grove created a new replica of the scaling group
- Both `pcb` and `pcc` scaled together from 2 to 4 pods each
- A new `PodGang` was created for gang scheduling the new replica
- New `PodCliques` appeared: `simple1-0-sga-1-pcb` and `simple1-0-sga-1-pcc`

Scale back down:
```bash
kubectl scale pcsg simple1-0-sga --replicas=1
```

## Step 5: Scale the Entire PodCliqueSet

Scale the entire workload to create a complete second instance:

```bash
kubectl scale pcs simple1 --replicas=2
```

Check the resources:
```bash
kubectl get pcs,pclq,pcsg,pg,pod -o wide
```

**What happened?**
- Grove created a complete duplicate of your workload
- All new resources have `-1-` in their names (second replica)
- A new `PodGang` (`simple1-1`) was created
- All components (pca, pcb, pcc, pcd) were duplicated

Scale back:
```bash
kubectl scale pcs simple1 --replicas=1
```

## Step 6: Understand the Hierarchy

Let's visualize what you just created:

```
PodCliqueSet (simple1)
├── Replica 0 (PodGang: simple1-0)
│   ├── PodClique: pca (3 pods)
│   ├── PodClique: pcd (2 pods)
│   └── PodCliqueScalingGroup: sga
│       ├── PodClique: pcb (2 pods)
│       └── PodClique: pcc (2 pods)
└── Replica 1 (when scaled to 2)
    └── [same structure]
```

**Scaling behaviors:**
- `kubectl scale pcs simple1`: Creates/removes complete replicas
- `kubectl scale pcsg simple1-0-sga`: Scales just the grouped components (pcb + pcc)
- Auto-scaling: Grove can automatically create and manage HPA resources based on the `ScaleConfig` defined at PodClique and PodCliqueScalingGroup levels of a PodCliqueSet, or delegate scaling responsibility to a custom autoscaler, such as [Dynamo Planer](https://docs.nvidia.com/dynamo/latest/planner/planner_intro.html).

## Step 7: Clean Up

Remove the sample workload:

```bash
kubectl delete -f samples/simple/simple1.yaml
```

Verify cleanup:
```bash
kubectl get pcs,pclq,pcsg,pg,pod
```

Only the Grove operator pod should remain.

## What's Next?

Now that you understand the basics, explore:

- **[Installation Guide](installation.md)** - Learn more about local and remote cluster deployment
- **[Core Concepts Tutorial](user-guide/core-concepts/01_overview.md)** - Step-by-step hands-on tutorial on Grove application development
- **[API Reference](api-reference/operator-api.md)** - Deep dive into all configuration options
- **[Samples](../operator/samples/)** - Explore more examples

## Key Concepts Recap

| Concept | What It Does | When to Use |
|---------|--------------|-------------|
| **PodCliqueSet** | Top-level resource defining your workload | Every Grove deployment |
| **PodClique** | Group of pods with the same role | Each component type in your system |
| **PodCliqueScalingGroup** | Multiple PodCliques that scale together | Tightly-coupled components (prefill+decode) |
| **PodGang** | Ensures all components are co-scheduled | Automatically created by Grove |

## Troubleshooting

### Pods stuck in Pending
- Check PodGang status: `kubectl describe pg simple1-0`
- Ensure your cluster has enough resources for gang scheduling

### Auto-scaling not working
- For kind clusters, install metrics-server (*choose one of following methods*):
    - Use `operator/Makefile` target
  ```bash
  make deploy-addons
  ```
    - Manual setup
  ```bash
  kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
  ```
- Add `--kubelet-insecure-tls` to metrics-server deployment for kind

### Need help?
- See the full [Troubleshooting Guide](installation.md#troubleshooting)
- Join the [Grove mailing list](https://groups.google.com/g/grove-k8s)
- File an [issue](https://github.com/NVIDIA/grove/issues)

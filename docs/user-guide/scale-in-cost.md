# Scale-in Cost

A scale-in has to choose which replica or pod leaves. Grove chooses by availability first, so a broken replica goes before a serving one, and then by its own tie-breaks: the highest replica index, or for pods the outdated template and then the newest pod. Two healthy replicas are indistinguishable to Grove. An operator or a platform controller that needs a particular one gone, for example the replica on a node being drained, can say so with a *scale-in cost*.

## Overview

The cost is the standard [`controller.kubernetes.io/pod-deletion-cost`](https://kubernetes.io/docs/concepts/workloads/controllers/replicaset/#pod-deletion-cost) annotation, carried on pods the way a ReplicaSet reads it. Grove reads it from its informer cache at the moment a scale-in chooses, and orders candidates in three tiers:

1. **Availability**, as today. A cost never promotes an available replica ahead of an unavailable one.
2. **Cost**. Within one availability tier the lowest cost leaves first. A PodCliqueSet (PCS) replica's cost is the minimum over its pods, so marking any one pod of a gang marks the gang. A pod without the annotation costs 0.
3. **Grove's tie-breaks**, as today: the highest replica index, or for pods the outdated template and then the newest pod.

The cost applies to PCS replicas and to the pods of a PodClique (PCLQ). PodCliqueScalingGroup (PCSG) replicas keep their index order until scaling groups can hold sparse replica indices.

| `scaleIn.costSource` | Description | Best For |
|------|-------------|----------|
| `none` (default) | Grove reads no cost. Every scale-in selects in Grove's own order. | Clusters where nothing marks pods, or where another tool stamps the annotation for its own reasons. |
| `pod-deletion-cost` | Grove reads `controller.kubernetes.io/pod-deletion-cost` off the pods. | Clusters with a drain or rebalancing controller that marks the pods it wants gone. |

## Prerequisites and Constraints

1. The feature is off by default. Enable it in the operator configuration as described below.
2. The annotation value must be an integer in the int32 range, as the Kubernetes API server validates it when the `PodDeletionCost` feature gate is on (beta, on by default since Kubernetes 1.22). A value that does not parse, including one with a leading plus sign or a leading zero, counts as no cost.
3. Marks are read from the operator's informer cache. Write them before you lower the replica count, and confirm they are on the pods, as shown under [Usage Examples](#usage-examples).

## Enabling the Feature

Set the scale-in cost source in the operator configuration:

```yaml
config:
  scaleIn:
    costSource: pod-deletion-cost   # default: none
```

Or with Helm:

```bash
helm upgrade grove-operator <chart> --namespace grove-system --reuse-values \
  --set config.scaleIn.costSource=pod-deletion-cost
```

The operator reads its configuration at startup, so the pods of the operator Deployment restart with the new value. Setting the value back to `none` restores Grove's own order without a rollback.

### Startup Validation

The operator validates `scaleIn.costSource` when it starts. A value other than `none` or `pod-deletion-cost` stops the operator with an error such as:

```
scaleIn.costSource: Unsupported value: "grove-annotation": supported values: "none", "pod-deletion-cost"
```

An absent value is `none`. Existing configuration files need no change.

## How It Works

When you lower a PCS's replica count, Grove sorts the candidate replicas by availability as before, then, within each availability tier, by the lowest cost first. For a PCS named `my-set` with `replicas: 3`, all three replicas available, and the pods of replica 1 marked with `controller.kubernetes.io/pod-deletion-cost: "-1000000"`, scaling to `replicas: 2` removes replica 1. Without the mark, Grove would remove replica 2, the highest index.

For a PCLQ, the cost orders the pods inside the PCLQ in the same way: among the pods that are equally serving, the lowest cost leaves first, then the outdated template, then the newest pod. A standalone PCLQ spread over more than one PodGang trims the PodGang Grove chooses first, and the cost orders the pods inside it, so a marked pod in another PodGang stays.

The mark dies with the pod. A replacement replica starts unmarked.

> **Note:** The order is best effort, as it is for a ReplicaSet. A mark written after a scale-in has read the pods is seen by the next scale-in. Write the marks first, confirm they are on the pods, then lower the replica count.

> **Note:** A cost orders scale-ins only. During a coherent update of a PCS replica, the pods of the previous revision are taken down in Grove's own order whatever their cost.

## Usage Examples

### Draining the replica on a node

To drain PCS replica 1 of `my-set`, mark its pods, raise the replica count so a replacement starts elsewhere, wait for the replacement to become available, then lower the count:

```bash
kubectl annotate pod \
  -l app.kubernetes.io/part-of=my-set,grove.io/podcliqueset-replica-index=1 \
  controller.kubernetes.io/pod-deletion-cost=-1000000

# Confirm the marks are on the pods.
kubectl get pods -l app.kubernetes.io/part-of=my-set \
  -o custom-columns=NAME:.metadata.name,COST:.metadata.annotations.controller\.kubernetes\.io/pod-deletion-cost

# A replacement replica starts. Wait until it is available before lowering the count,
# or the scale-in removes the replacement, which is not yet available, instead of replica 1.
kubectl scale podcliqueset my-set --replicas=4
kubectl wait podcliqueset my-set --for=jsonpath='{.status.availableReplicas}'=4 --timeout=30m

kubectl scale podcliqueset my-set --replicas=3   # replica 1 leaves
```

Replica 1 is the only available replica with a negative cost, so it is the one removed.

### Removing a particular pod of a PodClique

Mark the pod, then lower the PCLQ's replica count:

```bash
kubectl annotate pod my-set-0-worker-abcde controller.kubernetes.io/pod-deletion-cost=-1000000
kubectl scale podclique my-set-0-worker --replicas=1
```

## Observability

- The marks are on the pods:

  ```bash
  kubectl get pods -l app.kubernetes.io/part-of=my-set \
    -o custom-columns=NAME:.metadata.name,COST:.metadata.annotations.controller\.kubernetes\.io/pod-deletion-cost
  ```

- The `PodCliqueSetReplicaIndicesSelectedForScaleIn` event on the PCS carries the selected replicas' costs whenever one of them is non-zero:

  ```bash
  kubectl describe podcliqueset my-set
  # Events:
  #   Normal  PodCliqueSetReplicaIndicesSelectedForScaleIn  Selected PCS replica indices [1] for scale-in (scale-in cost: 1=-1000000)
  ```

- The operator's selection log line carries `scaleInCost` beside the selected replica indices on every scale-in.

## Backward Compatibility

- With `scaleIn.costSource` absent or `none`, every selection is the order Grove produced before the feature existed, whatever annotations the pods carry. Upgrading the operator changes nothing until you set the value.
- Operator configuration files at `operator.config.grove.io/v1alpha1` decode with the source set to `none`.
- No PodCliqueSet, PodCliqueScalingGroup or PodClique field is involved. Existing workloads need no change.

## Limitations

- **A cost cannot override availability.** Removing a serving replica while a broken one stays is the failure the availability order prevents.
- **Any actor with pod patch rights can influence the order**, as for a ReplicaSet. It cannot change how many replicas Grove removes.
- **PodCliqueScalingGroup replicas are not yet ordered by cost.** They leave by descending index until scaling groups can hold sparse replica indices.
- **The cost does not order the take-down of a coherent update.** It orders scale-ins only.

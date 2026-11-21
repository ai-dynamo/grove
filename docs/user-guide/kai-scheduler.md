# KAI Scheduler

When a PodCliqueSet (PCS) is scheduled by [KAI Scheduler](https://github.com/kai-scheduler/kai-scheduler), Grove creates one KAI PodGroup for each PodGang and skips KAI's own podgrouper. This guide lists the KAI metadata Grove reads from the PCS to fill in that PodGroup.

## Metadata Grove reads

| Key | Where Grove reads it | PodGroup field | When unset |
|---|---|---|---|
| `kai.scheduler/queue` | PCS label, then PCS annotation, then the PodClique templates' labels and annotations | `queue` | Admission rejects the PCS |
| `kai.scheduler/preemptibility` | PCS label | `preemptibility` | KAI derives preemptibility from the priority class |

Both keys follow KAI's own contract, so a workload keeps the queue and preemptibility it would have had under KAI's podgrouper.

## Queue

KAI accepts one queue per PodGroup. A queue set on the PCS applies to the whole PodGang. Any queue set on a PodClique template must name the same queue. Admission rejects a PCS with no queue or with conflicting queues.

## Preemptibility

`kai.scheduler/preemptibility` takes `"preemptible"`, `"non-preemptible"` or `"semi-preemptible"`. Without it, KAI treats a PodGroup whose priority value is below 100 as preemptible.

Admission rejects any other value. If a PCS admitted before this check carries an invalid value, Grove records an `InvalidKAIPreemptibility` warning event on the PodGang and leaves the PodGroup's preemptibility unset.

> **Note:** Grove reads preemptibility from the PCS label only, as KAI's podgrouper does. An annotation with this key is ignored.

## Example

```yaml
apiVersion: grove.io/v1beta1
kind: PodCliqueSet
metadata:
  name: my-inference
  namespace: default
  labels:
    kai.scheduler/queue: team-a
    kai.scheduler/preemptibility: non-preemptible
spec:
  replicas: 1
  template:
    cliques:
      - name: worker
        spec:
          roleName: worker
          replicas: 2
          podSpec:
            containers:
              - name: server
                image: <image>
                resources:
                  limits:
                    nvidia.com/gpu: "8"
```

To check what Grove wrote:

```bash
kubectl get podgroups.scheduling.run.ai -n <namespace> -o custom-columns=NAME:.metadata.name,QUEUE:.spec.queue,PREEMPTIBILITY:.spec.preemptibility
```

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

`"semi-preemptible"` is accepted but not applied. KAI does not allow the minimums of a semi-preemptible PodGroup to grow, and Grove grows them when a PCS replica gains a PodGang on scale-out or during an update. Grove records an `UnsupportedKAIPreemptibility` warning event on the PCS and leaves the PodGroup's preemptibility unset.

> **Note:** Grove reads preemptibility from the PCS label only, as KAI's podgrouper does. An annotation with this key is ignored.

## Stale-gang eviction

KAI evicts every running Pod of a PodGroup that stays below its minimum for longer than the staleness grace period. Grove terminates a gang itself after the PCS's `terminationDelay`, so it disables this eviction on every PodGroup it creates. This requires KAI v0.17.0+.

Grove ignores the `kai.scheduler/staleness-grace-period` annotation. To have a broken gang terminated sooner, set `terminationDelay` on the PCS.

## Rolling back the Grove operator

Grove puts the finalizer `kai.scheduler/pcs-replica-podgroup` on each PodGang it schedules with KAI. It holds the PodGang until the Pods of its PCS replica are gone. A Grove version that creates one KAI PodGroup per PodGang does not know this finalizer and never removes it, so after a rollback to such a version a deleted PodGang stays in `Terminating`.

After the rollback, remove the finalizer from every PodGang:

```bash
kubectl get podgangs.scheduler.grove.io --all-namespaces -o json |
  jq -r '.items[]
    | select((.metadata.finalizers // []) | index("kai.scheduler/pcs-replica-podgroup"))
    | [.metadata.namespace, .metadata.name,
       ({metadata: {finalizers: (.metadata.finalizers - ["kai.scheduler/pcs-replica-podgroup"])}} | tojson)]
    | @tsv' |
  while IFS=$'\t' read -r namespace name patch; do
    kubectl patch podgangs.scheduler.grove.io "$name" --namespace "$namespace" --type merge --patch "$patch"
  done
```

Run it after the older operator is running, because the newer operator adds the finalizer again when it syncs a PodGang.

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

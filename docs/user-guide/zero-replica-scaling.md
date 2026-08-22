# Zero-Replica Scaling

Grove supports an intentional idle state for PodClique (PCLQ) and PodCliqueScalingGroup (PCSG) resources. Setting `spec.replicas` to `0` removes the component from gang scheduling while preserving its configured `minAvailable` value for the next scale-out.

## Replica Semantics

Grove keeps the requested replica count in `spec.replicas` and derives an internal effective replica count:

```text
desired == 0 -> effective = 0
desired > 0  -> effective = max(desired, minAvailable)
```

The three replica values have different purposes:

| Value | Meaning |
|---|---|
| **Desired** | The value stored in `spec.replicas` by you or an autoscaler. |
| **Effective** | The number of Pods or PCSG replicas that Grove reconciles. |
| **Observed** | The actual non-terminating Pods or PCSG replica indexes reported in `status.replicas`. |

Observed replicas can temporarily differ from effective replicas while resources are being created or deleted.
The clamp is direction-agnostic: both wake-up and active scale-in requests in `0 < replicas < minAvailable` are accepted, while effective replicas remain at `minAvailable`.

| Resource | Desired | `minAvailable` | Effective | Steady-state observed |
|---|---:|---:|---:|---:|
| PCLQ | 0 | 2 | 0 | 0 Pods |
| PCLQ | 1 | 2 | 2 | 2 Pods |
| PCLQ | 3 | 2 | 3 | 3 Pods |
| PCSG | 0 | 2 | 0 | 0 replica indexes |
| PCSG | 1 | 2 | 2 | 2 replica indexes |
| PCSG | 3 | 2 | 3 | 3 replica indexes |

## Default Behavior

Existing defaults remain unchanged:

- An omitted PCLQ `replicas` field defaults to `1`.
- A positive PCLQ without `minAvailable` defaults `minAvailable` to the PCLQ replica count.
- PCSG `replicas` and `minAvailable` continue to default to `1`.
- Workloads with `replicas >= minAvailable` keep their existing reconciliation and gang scheduling behavior.

An explicit PCLQ `replicas: 0` is distinct from an omitted field. When `replicas` is `0` and `minAvailable` is omitted, Grove defaults `minAvailable` to `1`.

## Behavior Changes

| Scenario | Previous behavior | New behavior |
|---|---|---|
| PCLQ or PCSG explicitly set to `0` | The value could be rewritten to `1` or rejected, depending on the API path. | The component enters intentional idle. |
| Idle component in a PodGang | The component could remain in the gang and appear unhealthy. | Grove removes it from the PodGang and scheduler subgroup. |
| Idle availability | Zero ready replicas could trigger a quorum breach, warning, or gang termination. | Idle is healthy and does not trigger these actions. |
| `0 < replicas < minAvailable` | Grove ran the desired count and reported a breach. | Grove preserves the desired count and runs `minAvailable` replicas. |
| PCSG `status.replicas` | Reported desired replicas. | Reports actual non-terminating replica indexes. |
| PodGang materialization | A PodGang could be published from template replica fallbacks before its PCLQs and Pods existed. | Grove waits for all constituent PCLQs and effective Pods before publishing the PodGang. |

Grove does not create an empty PodGang or an empty topology constraint group when all of its components are idle.

## Complete Example

The following PodCliqueSet (PCS) keeps the router active while both worker components start idle. The standalone worker and PCSG retain `minAvailable: 2`, so scaling either component from `0` to `1` creates two effective replicas.

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: my-inference
  namespace: default
spec:
  replicas: 1
  template:
    cliques:
      - name: router
        spec:
          roleName: router
          replicas: 1
          minAvailable: 1
          podSpec:
            containers:
              - name: router
                image: example.com/inference-router:v1
      - name: standalone-worker
        spec:
          roleName: standalone-worker
          replicas: 0
          minAvailable: 2
          podSpec:
            containers:
              - name: worker
                image: example.com/inference-worker:v1
      - name: grouped-worker
        spec:
          roleName: grouped-worker
          replicas: 1
          minAvailable: 1
          podSpec:
            containers:
              - name: worker
                image: example.com/inference-worker:v1
    podCliqueScalingGroups:
      - name: workers
        cliqueNames:
          - grouped-worker
        replicas: 0
        minAvailable: 2
```

After the PCS is created, Grove creates the standalone PCLQ `my-inference-0-standalone-worker` and the PCSG `my-inference-0-workers`. You can exercise the idle and below-quorum states through their scale subresources:

```bash
kubectl scale podclique my-inference-0-standalone-worker --replicas=1
kubectl scale podcliquescalinggroup my-inference-0-workers --replicas=1
```

Both objects retain `spec.replicas: 1`, while Grove reconciles an effective count of `2`.

## Gang and Availability Behavior

When a PCLQ or PCSG has desired replicas set to `0`:

- Grove removes its PodGroups from the PodGang.
- Grove removes empty PCSG topology constraint groups.
- Idle components do not block `startsAfter`; `InOrder` startup advances to the nearest earlier active clique.
- The `MinAvailableBreached` condition remains `False`.
- Grove does not emit an all-replicas-lost warning for the idle transition.
- Grove does not use a stale breach condition to terminate the gang.
- Rolling updates can complete after generation and template hashes converge; Ready replicas are not required.

Scaling back to a positive value re-adds the component to gang scheduling. If the positive desired count is below `minAvailable`, Grove creates the full `minAvailable` count before considering the component available.

## Status and Autoscalers

PCLQ `status.replicas` continues to report the actual non-terminating Pod count. PCSG `status.replicas` reports the actual number of non-terminating replica indexes.

An HPA or KEDA scaler can therefore observe:

```text
spec.replicas:   1
status.replicas: 2
minAvailable:    2
```

This is expected. The autoscaler owns the desired value, while Grove enforces the effective quorum internally. Grove does not write the effective value back to `spec.replicas`.

Set KEDA `minReplicaCount` to `0` only when the scaler supports the required scale-from-zero activation path.
For Grove-managed HPA configuration, `minReplicas: 0` additionally requires the Kubernetes `HPAScaleToZero` feature gate and at least one Object or External metric. The feature gate is disabled by default in Kubernetes 1.35.

## Validation Rules

- PCLQ and PCSG replicas can be `0` but cannot be negative.
- Positive template replicas must be greater than or equal to `minAvailable`.
- `minAvailable` remains positive.
- Autoscaling `minReplicas` can be `0` or greater than or equal to `minAvailable`. Zero also requires at least one Object or External metric and a cluster with `HPAScaleToZero` enabled.
- `maxReplicas` must remain greater than or equal to `minReplicas`.

## Upgrade Impact

Normal workloads using the existing defaults require no migration.

Before upgrading, inspect PCLQ and PCSG objects that are already at zero or below quorum:

```bash
kubectl get podcliques,podcliquescalinggroups -A \
  -o custom-columns='KIND:.kind,NAMESPACE:.metadata.namespace,NAME:.metadata.name,DESIRED:.spec.replicas,MIN_AVAILABLE:.spec.minAvailable,OBSERVED:.status.replicas'
```

After upgrading:

- Objects already at zero leave their gang and are treated as intentionally idle.
- Objects with `0 < replicas < minAvailable` can automatically create additional Pods or PCSG replica indexes until the effective count reaches `minAvailable`.
- No CRD data rewrite or migration Job is required.

## Rollback Impact

An older Grove operator can reject, rewrite, or treat zero replicas as unhealthy. It can also reintroduce an idle component into gang availability and termination decisions.

After adopting zero-replica semantics, do not roll back directly to an operator version that does not support intentional idle without first scaling affected PCLQ and PCSG objects to valid positive replica counts.

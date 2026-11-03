# GREP-970: Steer scale-in with a per-replica deletion cost

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [Reading the cost](#reading-the-cost)
  - [API changes](#api-changes)
  - [PodClique controller](#podclique-controller)
  - [PodCliqueSet controller](#podcliqueset-controller)
  - [PodCliqueScalingGroup controller](#podcliquescalinggroup-controller)
  - [Monitoring](#monitoring)
  - [Dependencies](#dependencies)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Open Questions](#open-questions)
  - [1. PodCliqueScalingGroup scope](#1-podcliquescalinggroup-scope)
  - [2. Default at beta](#2-default-at-beta)
- [Alternatives](#alternatives)
- [Appendix](#appendix)
<!-- /toc -->

## Summary

Scale-in today chooses which PodCliqueSet replica to remove by availability and then by descending index, with no input from outside the controller. PodCliqueScalingGroup replicas and the pods of a PodClique are chosen by index and by readiness and age. Two healthy replicas are therefore indistinguishable to Grove, and an operator who needs a particular one gone, for example the one on a node being drained, has no way to say so.

This GREP adds a scale-in cost to Grove's selection: a per-pod value carried by the standard `controller.kubernetes.io/pod-deletion-cost` annotation, read off the pods at the moment a scale-in chooses, the way a ReplicaSet reads it, and aggregated per replica as the minimum over the replica's pods. Within the availability order that [#918](https://github.com/ai-dynamo/grove/pull/918) established, the replica with the lowest cost leaves first. With no annotation present, selection is exactly what it is today. The carrier sits behind a cost source the operator selects, so a Grove-owned key, or a target-based selection API if Kubernetes standardises one, can replace the annotation without touching the selection logic. PodCliqueSet replicas and PodClique pods are in scope. PodCliqueScalingGroup replicas take the same order once scaling groups can hold sparse indices (Open Question 1).

Tracking issue: [#970](https://github.com/ai-dynamo/grove/issues/970). Follows the second iteration deferred on [#862](https://github.com/ai-dynamo/grove/issues/862).

## Motivation

A ReplicaSet removes its lowest-cost pods first, so a platform controller can mark the pods it wants gone, raise capacity elsewhere, lower the count, and get exactly those pods removed. Node drains, spot reclamation and rebalancing all rely on it. Grove reads no cost, so for a gang on a draining node the only safe path is to hold it until the node is force-evicted and let the owner recreate it, one full gang start per drained gang, with the owner then trimming by its own order.

#862 raised this and [#918](https://github.com/ai-dynamo/grove/pull/918) delivered the availability half. Targeted selection was deferred because a replica has no resource of its own to carry a cost. This GREP keeps the cost on the pods, where its lifetime is the pod's, and aggregates it at selection time.

### Goals

- Let an external actor steer which PodCliqueSet replica and which PodClique pod a scale-in removes, by marking pods with a cost. Define the same order for PodCliqueScalingGroup replicas, for when scaling groups can hold sparse indices.
- Keep the availability order of #918. A cost never promotes an available replica ahead of an unavailable one.
- Change nothing when no pod carries a cost or the source is off.
- Read from the informer cache the operator already keeps. No new workload field, no new watch. The carrier is replaceable: selection consumes a cost, never a key.

### Non-Goals

- Triggering a scale-in. The cost orders replicas once a scale-in is requested and never changes `spec.replicas`.
- Guarantees. As for the ReplicaSet annotation, the order is best effort.
- A cost on gang-level objects, or any change to #918's availability buckets, or to rolling updates.
- Sparse PodCliqueScalingGroup indices. A scaling group expects indices 0 to n−1 and recreates a missing one, so a cost-chosen removal there needs status-tracked indices first, as #918 added for PodCliqueSet (Open Question 1).

## Proposal

Every scale-in in Grove orders its candidates in three tiers:

1. **Availability**, as today for PodCliqueSet replicas and PodClique pods. For replicas these are the buckets of #918: insufficiently scheduled, insufficiently available, scaling out, available. For pods, the existing terminating, unassigned, phase and readiness criteria. PodCliqueScalingGroup replicas are index-only today. The availability tier they will take, a replica whose PodCliques are not all available leaving first, arrives with the scaling-group GREP (Open Question 1).
2. **Cost**, new. Within one tier, the lowest cost leaves first. A replica's cost is the minimum cost of its pods, so marking any one pod of a gang marks the gang. A pod with no annotation costs 0, as it does for a ReplicaSet.
3. **The existing tie-break**, as today: descending index for replicas, template hash and age for pods.

The cost is carried on pods by `controller.kubernetes.io/pod-deletion-cost`, read through a cost source the operator configures. That annotation is the only source this GREP ships. Each scale-in reads the cost live, at the moment it chooses, from the pods in the operator's shared informer cache, as the ReplicaSet controller does: the PodCliqueSet controller's selection takes one label-filtered list of the set's pods from that cache, the same cache its PodGang sync already lists on every reconcile (`getExistingPodsByPCLQForPCS`), and the PodClique controller has its pods in hand. The list is a cache read taken only when a scale-in is being computed. It adds no watch and no apiserver call. No status field is written. The cost a scale-in sees is the cost on the pods when it runs.

A writer marks the pods of the replica it wants gone, then lowers the count:

```yaml
# on every pod of PodCliqueSet replica 1, patched by the drain controller
metadata:
  annotations:
    controller.kubernetes.io/pod-deletion-cost: "-1000000"
```

```yaml
# then, on the PodCliqueSet
spec:
  replicas: 2   # was 3: replica 1 leaves, replicas 0 and 2 stay
```

The operator reads the key only when its configuration says so:

```yaml
# OperatorConfiguration
scaleIn:
  costSource: pod-deletion-cost   # default: none
```

### User Stories

**Draining a node.** A placement controller marks every pod on node N with `controller.kubernetes.io/pod-deletion-cost: "-1000000"`, raises the PodCliqueSet's replicas by one so a replacement gang starts elsewhere, waits for it to be ready, and lowers the replicas by one. Grove removes the replica with a member on N, the only available replica with a negative cost. No gang is started twice and no healthy replica elsewhere is touched. The mark dies with the pod.

**Fragmentation clean-up.** Before a scheduled scale-in, a rebalancer marks the pods of the replica on the least-packed nodes. That replica leaves and its nodes become whole.

### Limitations/Risks & Mitigations

- **Best effort.** The cost is read from the informer cache when the scale-in is computed. A mark written after that read is missed by that scale-in and seen by the next, as for a ReplicaSet. Writers mark pods before they lower the count.
- **Nothing to observe before the count moves.** There is no status field. The selection event and log carry the chosen replicas' costs, and the annotation is on the pods for `kubectl` to show.
- **A cost cannot override availability.** Removing a serving replica while a broken one stays is the failure #918 fixed. Writers that want the broken one gone first already get it.
- **One marked pod marks the gang.** The minimum over a replica's pods is the intended semantics for gangs, which leave whole. A different aggregate is a case for a future selection API, not for this carrier.
- **Third-party writers.** Any actor with pod patch rights can influence the order, as for ReplicaSets, and cannot change how many replicas Grove removes. Tools that stamp the key for a ReplicaSet's benefit may already stamp Grove pods, which is why the source is off until a cluster operator turns it on. The apiserver validates the value as an int32. A defensive parse failure counts as 0.

## Design Details

### Reading the cost

The carrier is known in exactly one implementation: a cost source the selecting components share.

```go
// ScaleInCostSource yields a pod's scale-in cost from whatever carries it.
// A pod the source reports as absent costs 0.
type ScaleInCostSource interface {
    CostOf(pod *corev1.Pod) (cost int32, present bool)
}

// podDeletionCostSource reads controller.kubernetes.io/pod-deletion-cost. The
// apiserver validates the value as an int32, so a parse failure is treated as
// absent.
type podDeletionCostSource struct{}
```

The operator selects the source in `OperatorConfiguration`, where every other behaviour setting lives (the binary takes only `--config` and `--version`): a new `scaleIn.costSource` field whose values are `none` and `pod-deletion-cost`. `none`, the default, makes every pod cost 0, so selection is exactly today's order. `pod-deletion-cost` turns the feature on. A second carrier, such as a Grove-owned annotation, is a new value and a new implementation, with no change at any call site. The source is constructed once in the operator and handed to the controllers that scale in.

Three layers keep the carrier out of everything else:

1. **Selection consumes a number.** `selectReplicaIndicesForScaleIn`, the scaling-group selection and `DeletionSorter` take a cost per candidate and know nothing about where it came from.
2. **The source is an interface with one implementation.** The replica-cost computation in each selecting controller calls `CostOf` on the pods of each candidate, from the cache, when a scale-in runs. The annotation key appears in the `podDeletionCostSource` implementation and nowhere else.
3. **No workload API carries the carrier.** Nothing about the source reaches a Grove CRD. A new source is one more value of `scaleIn.costSource` in the operator's configuration API, an additive enum value, and no change to any PodCliqueSet, PodCliqueScalingGroup or PodClique field.

What this layering does not absorb is a target-based request, a list of replicas to remove, the likely shape of a Kubernetes selection API if one appears. It maps onto the same selection by giving the targeted replicas the lowest cost. Its extension point is the replica-cost computation, one level above the source.

### API changes

No spec or status field changes on any Grove resource, and no new CRD. `OperatorConfiguration` (`config.grove.io/v1beta1`) gains one additive field:

```go
// ScaleInCostSourceName names a source of per-pod scale-in costs.
type ScaleInCostSourceName string

const (
    // ScaleInCostSourceNone reads no cost: every pod costs 0 and selection
    // is the order Grove produces today. This is the default.
    ScaleInCostSourceNone ScaleInCostSourceName = "none"
    // ScaleInCostSourcePodDeletionCost reads the value of the
    // controller.kubernetes.io/pod-deletion-cost annotation on each pod.
    ScaleInCostSourcePodDeletionCost ScaleInCostSourceName = "pod-deletion-cost"
)

// AllScaleInCostSourceNames lists the supported sources, for validation.
var AllScaleInCostSourceNames = []ScaleInCostSourceName{ScaleInCostSourceNone, ScaleInCostSourcePodDeletionCost}

// ScaleInConfiguration configures how a scale-in chooses what to remove.
type ScaleInConfiguration struct {
    // CostSource names where a pod's scale-in cost is read from.
    // Default: none
    CostSource ScaleInCostSourceName `json:"costSource,omitempty"`
}
```

held as `ScaleIn ScaleInConfiguration` (`json:"scaleIn"`) on `OperatorConfiguration`. This is the package's pattern for `LogLevel` and `LogFormat`: a named string type, its constants, and an `All…` slice. `SetDefaults_OperatorConfiguration` sets an empty `CostSource` to `none`, so existing configuration files are unaffected, and `ValidateOperatorConfiguration` rejects any other value with `field.NotSupported`, as it does for `logLevel`.

### PodClique controller

The pod-level scale-in (`selectExcessPodsToDelete` with `DeletionSorter`) inserts the cost after readiness and before the template-hash and age criteria:

```
0. terminating      < not terminating        (unchanged)
1. unassigned       < assigned               (unchanged)
2. Pending, Unknown < Running                (unchanged)
3. not ready        < ready                  (unchanged)
4. lower cost       < higher cost            (new)
5. older template   < current template       (unchanged)
6. newer            < older                  (unchanged)
```

This is the ReplicaSet's position: in `ActivePodsWithRanks.Less` the cost is criterion 4, after unassigned, phase and readiness and before every tie-break among serving pods. A mark never removes a serving pod ahead of one that is not serving. Criterion 2 is listed as the code behaves: `DeletionSorter`'s comment says Pending < Unknown < Running, but its `podPhaseToOrdinal` map has no `PodUnknown` entry, so Unknown ties with Pending. Both are on the not-serving side of the cost boundary, and the one-line fix is outside this GREP.

`DeletionSorter` serves both pod-level paths, `selectExcessPodsToDelete` for a PodClique inside a scaling group and `buildPerPodGangDeletionTasks` for a standalone PodClique, which trims each PodGang separately. On the standalone path the PodGangMap decides which PodGang gives up pods first and the cost orders pods within it. A marked pod in a PodGang that is not being trimmed stays.

### PodCliqueSet controller

`selectReplicaIndicesForScaleIn` keeps its four buckets. `takeReplicaResourceSnapshot` runs on every `syncScalingProgress`, so the pods are not added to the snapshot. The list is taken inside the scale-in branch of `computeReplicaIndices`, only when `numReplicasToRemove` is positive: one label-filtered list of the set's non-terminating pods against the shared informer cache, grouped by their `grove.io/podcliqueset-replica-index` label and handed to `selectReplicaIndicesForScaleIn` as costs. `computeReplicaIndices` is a pure function with no client, so `syncScalingProgress` passes the read in as a lookup, a closure over the cached client that lists the pods and returns a cost per replica index, and the scale-in branch is the only caller. The `podgang` component already lists the same pods from that cache on every sync (`getExistingPodsByPCLQForPCS`), so a scale-in adds one read of cached objects, not a new watch or an apiserver call, and a reconcile with no scale-in adds nothing. A replica's cost is the minimum of `CostOf` over those pods, 0 when none carries a cost. `sortAndCoalesceReplicaIndices` orders each bucket by ascending cost, then descending index, instead of descending index alone. The buckets are never merged, so an available replica cannot outrank an unavailable one whatever its cost.

### PodCliqueScalingGroup controller

`computePCSGReplicasToDelete` removes the highest indices, and the reconcile expects indices 0 to n−1: `getExpectedPodCliqueFQNsByPCSGReplica` enumerates them, `createExpectedPCLQs` recreates any that is missing, and the PodGangMap drains scaling-group indices in role order (ScaleOut, Tail, Anchor). A cost-chosen low index would be refilled on the next reconcile, the refill #918 removed for PodCliqueSet by tracking `scaleInReplicaIndices` in status. Scaling groups need sparse, status-tracked indices before a cost can apply. That is a reconcile and PodGangMap change, not a selection change, and this GREP does not design it (Open Question 1). The order is defined so it is mechanical when it lands: availability first, then ascending cost, then descending index, with a replica's cost the minimum of `CostOf` over the pods of its PodCliques, from one cache list by the scaling group's labels.

### Monitoring

- The input is the annotation on the pods (`kubectl get pods -o jsonpath` on the key).
- `ReasonPodCliqueSetReplicaIndicesSelectedForScaleIn` carries the selected replicas' costs when any is non-zero. The scaling group gets an equivalent event with its selection change.
- Selection log lines carry `scaleInCost` beside the replica index. No new metric: the cost only reorders scale-ins.

### Dependencies

- `controller.kubernetes.io/pod-deletion-cost` is validated under the `PodDeletionCost` feature gate (beta, on by default since 1.22). With the gate off the value is a plain string the source parses.

### Test Plan

Unit tests:

- `podDeletionCostSource`: present, absent, negative, malformed. The `none` source reports every pod absent.
- Layering: `selectReplicaIndicesForScaleIn`, `sortAndCoalesceReplicaIndices` and `DeletionSorter` take costs, never pods or keys, and the quoted annotation key appears in exactly one non-test file, the `podDeletionCostSource` implementation.
- Configuration: absent and `none` give today's order, `pod-deletion-cost` honours a mark, an unknown value is rejected by `ValidateOperatorConfiguration`.
- `DeletionSorter`: cost after readiness, before the template hash. Equal costs fall through.
- Replica cost: the minimum over non-terminating pods, 0 when none is marked.
- PodCliqueSet selection: within each bucket lower cost first, then descending index. A costed available replica never outranks an unavailable one.

E2E:

- The repro from #970: `replicas: 3`, all available, replica 1's pods at `-1000000`, scaled to 2. Replica 1 leaves.
- The same with replica 2 unavailable: replica 2 leaves, the cost on replica 1 notwithstanding.
- A mark written in the same reconcile window: either documented outcome is accepted, pinning the best-effort contract.
- Pod level: a standalone clique with one anchor PodGang scaled 4 → 3 with one pod marked. The marked pod goes. With two anchors and the mark in the PodGang not being trimmed, the mark is ignored.
- No annotation anywhere: the existing scale-in e2e tests pass unchanged.

### Graduation Criteria

With `scaleIn.costSource` absent or `none`, every selection is byte-for-byte today's order. The feature stays off until a cluster operator turns it on, because other tools already write this key on pods for a ReplicaSet's benefit. An upgrade that silently honoured those stamps would change which replica a scale-in removes with no change to any Grove manifest.

- **Alpha**: PodCliqueSet and pod-level selection behind `scaleIn.costSource`, default `none`. Setting it back to `none` restores today's order without a rollback.
- **Beta**: after one release with no reported selection regressions, e2e tests in the release pipeline. Whether the default flips is Open Question 2. The field stays either way, as the source selector.
- **GA**: with the `v1beta1` API's own graduation. No workload CRD field is involved.

## Implementation History

- 2026-11-02: tracking issue #970 opened.
- 2026-11-03: a reviewer asks for this GREP, settling the read location, the aggregation and precedence, and the scope.

## Open Questions

### 1. PodCliqueScalingGroup scope

Scaling-group replicas need sparse indices before a cost can choose among them (see the PodCliqueScalingGroup controller section). Either this GREP designs that change and the three selections ship together, or PodCliqueSet and pod-level selection land first and scaling groups follow in a GREP of their own. The draft takes the second: the gap is a replica-lifecycle change, the scaling-group counterpart of #918, not a selection rule. It alters scaling-group reconcile with no cost present, where this GREP is otherwise a no-op. Scaling-group index placement also lives on the PodGangMap, whose multi-anchor handling is deferred to the coherent-update engine. Bringing scaling groups up to #918 is a GREP of its own, after which the order defined here applies without further design.

### 2. Default at beta

`scaleIn.costSource` defaults to `none` in alpha so an upgrade changes nothing. At beta the default could flip to `pod-deletion-cost` so the feature works without configuration, at the cost of making existing third-party stamps on Grove pods take effect at upgrade. The draft leaves the flip open.

## Alternatives

- **A Grove-owned annotation instead of the Kubernetes key.** Clean ownership, but writers that already mark ReplicaSet pods would write two keys, and the Kubernetes key is apiserver-validated. Behind the source interface a Grove key is a second value, not a redesign.
- **Publish the aggregated cost in PodClique status and read it from there.** The PodClique controller, which lists its pods on every reconcile, would write `status.scaleInCost`, and the set's selection would read it from the PodClique objects already in its snapshot, so the scale-in path lists nothing. It matches Grove's pattern of child status feeding the parent and gives an operator a number to read before the count moves. Not chosen because it adds an API field that outlives the carrier, needs the PodClique pod watch widened to admit annotation changes (today it admits only status changes and deletion marks, so the field would go stale), and adds one reconcile of lag between a mark and its effect. The live read costs none of those. It stays the right move if the maintainers want no pod reads on the scale-in path at all.
- **A `ReplicaSelection` resource mapping replica index to cost** (sketched on #862). Needs an owner to clear entries and a replica identity so a reused index does not inherit a stale cost. A pod-level cost dies with the pod.
- **A spec field naming the replicas to remove.** Precise, but couples the writer to indices Grove may reassign and needs its own clearing rules. It remains the natural shape for a future selection API and would plug in above the cost source.
- **Sum or maximum instead of the minimum.** A sum lets many small marks outweigh one deliberate one. A maximum lets one healthy pod cancel a drain mark on its siblings. The minimum lets one marked pod speak for its gang, which a node drain needs.

## Appendix

- Kubernetes pod-deletion-cost: https://kubernetes.io/docs/concepts/workloads/controllers/replicaset/#pod-deletion-cost
- #862 Support targeted PCS scale-down, and #918 make scale-in availability-aware: https://github.com/ai-dynamo/grove/issues/862, https://github.com/ai-dynamo/grove/pull/918
- Selection code this GREP changes: `operator/internal/controller/podcliqueset/components/podcliquesetreplica/scaling.go` (`selectReplicaIndicesForScaleIn`, `sortAndCoalesceReplicaIndices`), `operator/internal/controller/podcliquescalinggroup/components/podclique/sync.go` (`computePCSGReplicasToDelete`), `operator/internal/controller/podclique/components/pod/deletionsort.go` (`DeletionSorter`, shared by `syncflow.go` and `standalonepclqdistribution.go`), `operator/api/config/v1beta1/types.go` (`OperatorConfiguration`).

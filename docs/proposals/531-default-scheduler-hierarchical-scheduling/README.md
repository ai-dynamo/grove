# GREP-531: Hierarchical Scheduling with CompositePodGroup

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [Topology-Aware Scheduling](#topology-aware-scheduling)
  - [Lifecycle](#lifecycle)
  - [Monitoring](#monitoring)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Open Questions](#open-questions)
<!-- /toc -->

## Summary

Use upstream Kubernetes `Workload`, `CompositePodGroup`, and `PodGroup` APIs to schedule each Grove `PodGang` as one hierarchical gang.

## Motivation

Grove already defines the scheduling intent: `PodGangMap` creates the `PodGang` layout, and each `PodGang` defines its groups, minimums, and topology. This GREP keeps Grove's general Kubernetes >=1.36 baseline. The default-scheduler backend translates that intent into upstream objects and reuses `workloadbuilder` where applicable when the required WAS capabilities are available. No new Grove API or scheduling model is needed.

### Goals

- Implement Grove `PodGang` scheduling with upstream Kubernetes APIs, reusing `workloadbuilder` where applicable.
- Preserve Grove gang, scaling, update, and required-topology semantics for supported mappings.

### Non-Goals

- Changing existing Grove APIs or scheduling semantics.

## Proposal

With hierarchical scheduling enabled for the default-scheduler backend, Grove translates each `PodGang` into one upstream scheduling hierarchy. Existing Grove APIs, scaling, rollout, and `DependsOn` semantics remain unchanged.

### Limitations/Risks & Mitigations

- Kubernetes [Workload-Aware Scheduling](https://github.com/orgs/kubernetes/projects/251) is still evolving, and Grove must track upstream API changes.
- `workloadbuilder` is reused where applicable; Grove handles translation, runtime object reconciliation, and Pod membership.
- WAS requires `minCount >= 1`. An initial `MinReplicas=0` mapping fails closed; when Grove releases it to zero after initial placement, the backend retains the last positive upstream `minCount`.
- WAS limits each template list to 8 entries and hierarchy depth to 4. `PodGang`s exceeding these limits fail closed before Pods are ungated.
- WAS supports a single required topology key per generated group. Preferred topology constraints are unsupported and fail closed.
- The backend requires `GenericWorkload` on kube-apiserver, kube-scheduler, and kube-controller-manager, plus `CompositePodGroup` and `TopologyAwareWorkloadScheduling` on kube-apiserver and kube-scheduler. Kubernetes 1.37 is the first upstream release with the complete hierarchy, and these gates are disabled by default. Missing capabilities or unsupported `PodGang` mappings fail closed; Grove does not fall back to scheduling Pods independently.

## Design Details

Each Grove `PodGang` maps to:

```text
Workload template tree
└─ CompositePodGroupTemplate: Grove PodGang
   ├─ CompositePodGroupTemplate: topology subgroup (optional)
   │  └─ PodGroupTemplate: Grove PodGroup
   └─ PodGroupTemplate: Grove PodGroup
```

Grove maps `MinReplicas` to `minCount` and one required topology key to the corresponding hierarchy level. It creates the runtime `CompositePodGroup` and `PodGroup` objects from these templates and sets their parent links.

The hierarchy does not change the scheduling unit. Each generated `CompositePodGroup` uses gang scheduling with `minGroupCount` set to its number of direct child groups, preserving each Grove `PodGang` as one complete gang.

### Topology-Aware Scheduling

Upstream [Topology-Aware Scheduling](https://kubernetes.io/docs/concepts/workloads/workload-api/topology-aware-scheduling/) attaches a required topology constraint to a group, requiring all descendant Pods to share the same value for the specified node-label key. Nested constraints are resolved from parent to child.

```text
root CompositePodGroup                 <- PodGang.TopologyConstraint
├─ child CompositePodGroup             <- TopologyConstraintGroupConfig
│  ├─ leaf PodGroup                    <- PodGroup.TopologyConstraint
│  └─ leaf PodGroup
└─ leaf PodGroup                       <- ungrouped PodGroup
```

Grove maps `PodGang` constraints to the root `CompositePodGroup`, `PodGroup` constraints to leaves, and each `TopologyConstraintGroupConfig` with member `PodGroup`s to a child `CompositePodGroup`. The child gives its descendants one shared topology domain; applying the constraint separately to each leaf could select different domains. Without a group config, leaves attach directly to the root.

For base `PodGang`s, each `TopologyConstraintGroupConfig` generated from a PCSG constraint maps to a child `CompositePodGroup`. For scaled `PodGang`s, the PCSG occupies the entire `PodGang`, so its constraint is carried by `PodGang.TopologyConstraint` and maps to the root.

### Lifecycle

Pod count changes update existing leaf `PodGroup`s. PCSG scale-out creates new `PodGang` hierarchies, while scale-in deletes the corresponding hierarchies.

`PreparePod()` sets each Pod's immutable `spec.schedulingGroup.podGroupName` to its leaf `PodGroup`. Pods remain gated until the hierarchy for the current `PodGang` generation is ready. Existing Pods cannot be migrated in place and must be recreated through a Grove rollout.

Grove owns the lifecycle and desired state of generated scheduling objects, while kube-scheduler owns their runtime status.

### Monitoring

No new monitoring is introduced.

### Test Plan

- Unit tests verify object mapping, topology hierarchy, and validation.
- Integration and end-to-end tests verify ordering, scaling, gang scheduling, and topology scheduling.

### Graduation Criteria

Graduation follows upstream Kubernetes Workload-Aware Scheduling maturity and Grove production validation.

## Open Questions

Should hierarchical scheduling remain opt-in while Grove supports Kubernetes >=1.36, or should it raise the default-scheduler backend requirement to Kubernetes >=1.37?

# GREP-531: Hierarchical Scheduling with CompositePodGroup

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [Monitoring](#monitoring)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
<!-- /toc -->

## Summary

Use upstream Kubernetes `Workload`, `CompositePodGroup`, and `PodGroup` APIs to schedule each Grove `PodGang` as one hierarchical gang.

## Motivation

Grove already defines the scheduling intent: `PodGangMap` creates the `PodGang` layout, and each `PodGang` defines its groups, minimums, and topology. The default-scheduler backend translates that intent into upstream objects and reuses `workloadbuilder` from Kubernetes >1.36 where applicable. No new Grove API or scheduling model is needed.

### Goals

- Implement Grove `PodGang` scheduling with upstream Kubernetes APIs, reusing `workloadbuilder` where applicable.
- Preserve Grove gang, scaling, update, and required-topology semantics for supported mappings.

### Non-Goals

- Changing existing Grove APIs or scheduling semantics.

## Proposal

When the default-scheduler backend is selected, its existing `SyncPodGang()` hook translates each Grove `PodGang` into:

```text
Workload
└─ CompositePodGroup: Grove PodGang
   ├─ CompositePodGroup: topology subgroup (optional)
   │  └─ PodGroup: Grove PodGroup
   └─ PodGroup: Grove PodGroup
```

`PodGangMap`, scaling, rollout, and `DependsOn` remain unchanged.

### Limitations/Risks & Mitigations

- Kubernetes [Workload-Aware Scheduling](https://github.com/orgs/kubernetes/projects/251) is still evolving, and Grove must track upstream API changes.
- `workloadbuilder` is only partially reusable. Grove still handles `PodGang` conversion, CPG parent links, object lifecycle, pod membership, status, `MinReplicas=0`, and preferred topology.
- WAS requires `minCount >= 1`. When Grove releases `MinReplicas` to zero after initial placement, the backend retains the last positive upstream `minCount`.
- WAS limits template lists to eight entries and hierarchy depth to four. `PodGang`s exceeding these limits fail closed before Pods are ungated.
- WAS only supports required topology constraints. `PodGang`s using preferred topology fail closed.
- The backend requires upstream WAS APIs and feature gates on both kube-apiserver and kube-scheduler. Missing capabilities or unsupported `PodGang` mappings fail closed; Grove does not fall back to scheduling Pods independently.

## Design Details

The backend translates each Grove `PodGang` into upstream scheduling objects and reuses `workloadbuilder` where applicable.

The translation maps:

- `PodGang` to one `Workload` and root `CompositePodGroup`;
- `TopologyConstraintGroupConfig` to a child `CompositePodGroup`;
- `PodGroup` to a leaf `PodGroup`;
- `MinReplicas` to `minCount`;
- supported topology constraints to the corresponding hierarchy level.

The hierarchy does not change the scheduling unit. Each generated `CompositePodGroup` uses gang scheduling with `minGroupCount` set to its number of direct child groups, preserving each Grove `PodGang` as one complete gang.

Pod count changes update existing groups. Adding or removing groups rebuilds the hierarchy before Pods are ungated.

**Pod membership and creation order**

`PreparePod()` assigns each Pod to its leaf `PodGroup`. Pods remain gated until the hierarchy for the current `PodGang` generation is ready.

Grove owns the lifecycle and desired state of generated scheduling objects, while kube-scheduler owns their runtime status.

### Monitoring

No new monitoring is introduced.

### Test Plan

- Unit tests verify object mapping and validation.
- Integration and end-to-end tests verify ordering, scaling, gang scheduling, and topology scheduling.

### Graduation Criteria

Graduation follows upstream Kubernetes Workload-Aware Scheduling maturity and Grove production validation.

# GREP-578: PodClique affinity over topology domains

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
    - [Topology Enumeration](#topology-enumeration)
    - [Scheduling](#scheduling)
    - [Validation](#validation)
    - [Scale-up / scale-in / rolling updates](#scale-up--scale-in--rolling-updates)
  - [User Stories](#user-stories)
    - [Story 1: Attention-FFN disaggregated inference](#story-1-attention-ffn-disaggregated-inference)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
    - [API Changes](#api-changes)
    - [PodCliqueStatus](#podcliquestatus)
      - [Failure Modes](#failure-modes)
    - [Example Usage](#example-usage)
  - [Monitoring](#monitoring)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Alternatives](#alternatives)
  - [Placeholder pods](#placeholder-pods)
  - [Two-phase scheduling](#two-phase-scheduling)
  - [Higher-level implementation](#higher-level-implementation)
<!-- /toc -->

## Summary

Disaggregated Attention-FFN (AFD) workloads for AI inference require multiple pod groups with different scale and resource requirements. Networked communication between the pods is latency sensitive; there can be many back-and-forth requests between worker pods to generate a single token and delays can impact whole-system performance substantially.

This proposal introduces a new `topologyAffinity` concept to the `PodClique` template within a `PodCliqueSet` that allows users to link two or more `PodCliques` together.
Grove creates the configured per-domain replica count for the affine clique in each topology domain occupied by the referenced cliques, using a `ClusterTopology` domain to resolve the underlying node label.

## Motivation

A Kubernetes cluster can be composed of multiple racks of nodes connected through one or more network switches. For a packet, each hop through a switch from one server to another can add latency.

In order to minimize latency, attention and FFN worker pods must be scheduled as near as possible based on the network switches underlying the Kubernetes cluster. These are modeled as topology domains for racks and nodes associated with a primary network switch.

For users deploying to a cluster with multiple topology domains, they cannot know the final number of domains a `PodClique`’s pods can be scheduled into. This requirement also cannot be encoded in a `PodCliqueSet`.  For example, if the FFN workload is split across three domains, the number of decode pods must also be at least three. If the user defines a number of decode `replicas` that is not three, the consequences could be a failure to schedule or unacceptable performance impact when cross-pod communication inevitably spans the topology domain.

### Goals

* Allow users to define inter-clique affinities where Grove will ensure that N pods are created in each target domain from the union of the referenced `PodClique` group’s scheduled pods.
* No impact to existing workloads or topology constraint definitions. Where possible, conflicting requirements are surfaced as errors during admission.
* No special handling required in schedulers for the base case; Grove creates pods with required node affinity for the resolved topology label. Special handling can be added as-needed.
* Support scale-up, scale-down, and rolling updates.

### Non-Goals

* Support dynamic rescheduling; if a pod is evicted and moved to a new topology domain, Grove will not ensure the affinity is still respected.

## Proposal

```
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: afd-disagg
spec:
  replicas: 1
  template:
    cliques:
      - name: ffn
      - name: decode
        spec:
          replicas: 1
          affinity:
            topologyAffinity:
              topologyName: afd-disagg-fabric
              domain: block
              cliqueNames:
                - ffn
```

Grove will implement affinity between `PodClique` within a topology domain for cliques in the same `PodCliqueScalingGroup` (PCSG).

1. Resolve the `domain` in the `ClusterTopology` indicated by `topologyName` to the concrete node label key.
2. Optimistically create N (`replicas`) pods in each `domain` for the `PodClique` with a defined `affinity.topologyAffinity`.
3. Inject the `grove-initc` init container into topology-affinity pods. The init container waits for the associated `cliqueNames` to be ready and for the owning `PodClique` to publish `TopologyAffinityReady=True`.
4. Once all associated `cliqueNames` reach their scheduled minimum, extract the union of scheduled `domain` values from the nodes assigned to those associated pods.
5. Delete pods outside the target domain set or above `replicas` per target domain. `TopologyAffinityReady` is set once every target domain has exactly `replicas` non-terminating pods and no extra topology-affinity pods remain.

Affinity is resolved independently for each PCSG replica; Grove does not compute a union across different PCSGs or across different PCSG replica indexes.
Standalone `PodCliques` are out of scope for the initial implementation.

#### Topology Enumeration

* For large clusters, listing and watching all nodes can be an expensive operation.
* Grove runs a `node-label-value-store` controller that watches Node metadata rather than full Node objects.
* Label keys are tracked lazily: the first topology-affinity `PodClique` that resolves a `ClusterTopology` domain to a label key registers that key and triggers a metadata rebuild.
* A single store is shared across topology-affinity rules and caches only unique values for the requested node label keys.

#### Scheduling

* With no underlying scheduler changes, this should work as-is for pods that do not require special considerations because Grove adds required node affinity for the resolved topology label value.
* The `PodGang` controller sizes topology-affinity pod groups from `PodClique.status.topologyAffinity.targetDomains`, requeueing until the topology-affinity status is available. The resulting `PodGroup.minReplicas` is the expanded replica count, `len(targetDomains) * spec.replicas`.
* The init container holds application startup until the associated cliques are ready and the topology-affinity clique has converged on its final target domains.
* Existing topology constraints take precedence over `topologyAffinity`.

#### Validation

* No circular dependencies between two or more `PodClique` templates.
* A `PodClique` that defines `topologyAffinity` and every referenced `cliqueName` must belong to the same PCSG. Cross-PCSG references and references from standalone `PodCliques` are rejected.
* A `PodClique` that defines `podSpec.nodeSelector` or `podSpec.affinity.nodeAffinity` against the same resolved node label key as `topologyAffinity` will be rejected. Other node affinity is allowed.
  * Pod affinities will not be rejected, but could lead to unschedulable situations.

#### Scale-up / scale-in / rolling updates

The implementation reconciles toward `len(targetDomains) * spec.replicas` pods. `spec.replicas` remains the per-domain scale value, while `status.totalReplicas` reports the expanded pod count. Scale-up creates only per-domain deficits. Scale-in and target-domain shrink delete pods outside target domains or above the per-domain count, using the normal deletion ordering so outdated pods are preferred during updates.

### User Stories

#### Story 1: Attention-FFN disaggregated inference

An operator runs a Kubernetes cluster with 12 GPU nodes:

* 6 GPU nodes are in `topology.kubernetes.io/network-domain=A`.
* 6 GPU nodes are in `topology.kubernetes.io/network-domain=B`.

A user would like to deploy their AFD workload to the cluster using a `PodCliqueSet`.

The user defines the `PodCliqueSet` as follows:

```
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: ffn-decode-disagg
spec:
  replicas: 1
  template:
    cliques:
      - name: ffn
        spec:
          replicas: 10
      - name: decode
        spec:
          replicas: 1
          affinity:
            topologyAffinity:
              topologyName: ffn-decode-disagg-fabric
              domain: block
              cliqueNames:
                - ffn
```

We define the `decode` `PodClique` with 1 replica, but because of the affinity to the `ffn` `PodClique`, it consequently deploys that 1 replica to both network domain `A` and `B`:

```
+------------------------------------------------+
| Cluster                                        |
|                                                |
| Network domain A                               |
|   [FFN]        [FFN]        [FFN]              |
|   [FFN]        [FFN]        [decode]           |
|                                                |
| Network domain B                               |
|   [FFN]        [FFN]        [FFN]              |
|   [FFN]        [FFN]        [decode]           |
+------------------------------------------------+
```

If, instead, the `ffn` `PodClique` is deployed with 5 replicas and can fit in network domain `A`, the intermediate state is still that a `decode` pod is deployed in each network domain:

```
+------------------------------------------------+
| Cluster                                        |
|                                                |
| Network domain A                               |
|   [FFN]        [FFN]        [FFN]              |
|   [FFN]        [FFN]        [decode]           |
|                                                |
| Network domain B                               |
|   [decode]                                     |
|                                                |
+------------------------------------------------+
```

However, the final state of the cluster is a single `decode` replica in `A`:

```
+------------------------------------------------+
| Cluster                                        |
|                                                |
| Network domain A                               |
|   [FFN]        [FFN]        [FFN]              |
|   [FFN]        [FFN]        [decode]           |
|                                                |
| Network domain B                               |
|   (no FFN or decode pods)                      |
|                                                |
+------------------------------------------------+
```

### Limitations/Risks & Mitigations

* If the associated cliques never reach their scheduled minimum, topology-affinity pods continue to exist in all known domains and their init containers continue to wait until the owning PCSG or `PodCliqueSet` replica is terminated after `spec.template.terminationDelay`.
* We’re relying on a relatively static set of pre-created domains. This may not be well supported for cloud clusters where a domain might not be currently represented in the cluster (e.g., where scale-up into an availability zone would be supported).
* This changes the meaning of the `replicas` field in the presence of a `topologyAffinity`. Rather than the desired number of total replicas, it is per-topology domain. `status.replicas` preserves that per-domain value for scale clients, and `status.totalReplicas` reports the expanded total.
* `minAvailable` must equal `replicas` on the template and the effective minimum is expanded to `len(targetDomains) * replicas`.

## Design Details

#### API Changes

```go
// PodCliqueSpec defines the specification of a PodClique.
type PodCliqueSpec struct {
	[snip]
	// Affinity is a group of affinity scheduling rules for a PodClique.
	// +optional
	Affinity *PodCliqueAffinity `json:"affinity,omitempty"`
}

// PodCliqueAffinity is a group of affinity scheduling rules for a PodClique.
type PodCliqueAffinity struct {
	// Describes PodClique topology affinity rules.
	// This clique's pods are placed in the topology domains occupied by the referenced cliques.
	// +optional
	TopologyAffinity *TopologyAffinity `json:"topologyAffinity,omitempty"`
}

// TopologyAffinity defines PodClique topology affinity rules.
type TopologyAffinity struct {
	// TopologyName is the name of the ClusterTopology resource to use for topology-aware scheduling.
	// If topologyAffinity is set, topologyName and domain must both be specified.
	// +required
	TopologyName string `json:"topologyName"`
	// Domain specifies the topology domain for creating affine replicas.
	// Must reference a domain in the topology levels defined in the ClusterTopology CR name as set in TopologyName.
	// Example: "rack" means replicas placed within all racks that the dependent cliqueNames are scheduled in.
	// +required
	Domain string `json:"domain"`
	// CliqueNames is the list of names of the PodCliques that are part of the affinity group.
	// Each referenced PodClique must belong to the same PodCliqueScalingGroup as this PodClique.
	// Pods are scheduled at the union of all scheduled PodClique domains within the same PCSG replica.
	// +required
	CliqueNames []string `json:"cliqueNames"`
}
```

#### PodCliqueStatus

A `TopologyAffinityReady` condition is added when `spec.affinity.topologyAffinity` is defined. It is `False` while associated `PodCliques` have not reached their scheduled minimum, while pods outside the final target domains are still present, or while any target domain has a pod count different from `spec.replicas`. It is `True` once the topology-affinity pods exactly match the resolved target domains. The condition is removed if topology affinity is removed from the `PodClique`.

`PodCliqueStatus` also publishes the resolved topology-affinity state:

```go
type PodCliqueStatus struct {
	[snip]
	// Replicas is the replica count exposed through the scale subresource.
	// For topology-affinity PodCliques, this matches Spec.Replicas so autoscalers scale per-domain replicas.
	// Use TotalReplicas for the total non-terminated Pod count across all topology domains.
	Replicas int32 `json:"replicas,omitempty"`
	// TotalReplicas is the total number of non-terminated Pods targeted by this PodClique.
	// For topology-affinity PodCliques, this is the expanded count across all topology domains.
	TotalReplicas int32 `json:"totalReplicas,omitempty"`
	// TopologyAffinity captures the resolved topology-affinity state used by controllers that need to size or gate
	// topology-affinity PodCliques without directly reading Node labels.
	// +optional
	TopologyAffinity *PodCliqueTopologyAffinityStatus `json:"topologyAffinity,omitempty"`
}

// PodCliqueTopologyAffinityStatus captures the resolved topology-affinity state for a PodClique.
type PodCliqueTopologyAffinityStatus struct {
	// LabelKey is the node label key for the configured topology domain.
	// +optional
	LabelKey string `json:"labelKey,omitempty"`
	// AllDomains is the set of all currently known values for LabelKey.
	// +listType=set
	// +optional
	AllDomains []string `json:"allDomains,omitempty"`
	// AssociatedDomains is the set of topology domains used by associated PodCliques.
	// +listType=set
	// +optional
	AssociatedDomains []string `json:"associatedDomains,omitempty"`
	// TargetDomains is the set of topology domains this PodClique should currently occupy.
	// +listType=set
	// +optional
	TargetDomains []string `json:"targetDomains,omitempty"`
	// AssociatedReady indicates whether all associated PodCliques have reached their scheduled minimum.
	// +optional
	AssociatedReady bool `json:"associatedReady,omitempty"`
}
```

##### Failure Modes

* If the `ClusterTopology` cannot be resolved or the configured domain is removed, status reconciliation fails and the controller retries. Admission rejects this on create/update when the topology is already invalid.
* If an associated pod is scheduled onto a node that does not carry the resolved topology label key, Grove cannot resolve the associated domain and retries with an error.
* If target domains become empty, Grove does not create new topology-affinity pods for that reconcile.

#### Example Usage

```
apiVersion: grove.io/v1alpha1
kind: ClusterTopology
metadata:
  name: afd-disagg-fabric
spec:
  levels:
    - domain: block
      key: network.topology.nvidia.com/fabric-pod

---

apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: afd-disagg
spec:
  replicas: 1
  template:
    cliques:
      - name: ffn
        spec:
          replicas: 2
      - name: decode
        spec:
          replicas: 2
          affinity:
            topologyAffinity:
              topologyName: afd-disagg-fabric
              domain: block
              cliqueNames:
                - ffn
```

### Monitoring

Topology-affinity health is observable through `PodClique.status.topologyAffinity` and the `TopologyAffinityReady` condition. Operators can compare `allDomains`, `associatedDomains`, and `targetDomains` to understand whether Grove is still in the optimistic all-domain phase or has converged to the domains used by the associated cliques.

`status.replicas` remains the per-domain scale value for topology-affinity `PodCliques`; `status.totalReplicas`, `readyReplicas`, `scheduledReplicas`, `scheduleGatedReplicas`, and `updatedReplicas` report the actual pod state across all domains. Grove also emits the existing pod create/delete events and the `AllScheduledReplicasLost` warning when scheduled replicas drop from non-zero to zero.

### Test Plan

**Unit tests** for topology affinity should be included with the implementation in the following areas:

* API and generated-client coverage for `PodCliqueAffinity`, `TopologyAffinity`, `PodCliqueStatus.totalReplicas`, and `PodCliqueStatus.topologyAffinity`.
* PCS validating webhook tests for required `topologyName` and `domain`, valid referenced `cliqueNames`, rejection of circular dependencies, rejection of cross-PCSG or standalone topology-affinity references, rejection when `minAvailable != replicas`, and rejection of direct `nodeSelector` or `nodeAffinity` conflicts on the resolved topology label key.
* Node-label value store tests for dynamic label-key registration, metadata-only Node watches, unique value caching, and rebuild behavior when a new topology label key is registered.
* PodClique pod sync tests for optimistic per-domain pod creation, required node affinity injection for each resolved topology value, init container injection, deletion of pods outside `targetDomains`, per-domain scale-up and scale-in, and rolling-update deletion ordering.
* PodClique status tests for `TopologyAffinityReady`, `allDomains`, `associatedDomains`, `targetDomains`, `associatedReady`, `status.replicas`, and `status.totalReplicas`.
* PodGang sync tests for requeueing until `targetDomains` is available and for setting expanded `PodGroup.minReplicas` to `len(targetDomains) * spec.replicas`.
* Gang-termination tests showing that overprovisioned topology-affinity pods keep the owning PCSG below availability until `spec.template.terminationDelay` expires, then trigger normal PCSG or `PodCliqueSet` replica termination.

**E2E tests** should cover the following scenarios:

* Two-domain convergence: label worker nodes into two fabric domains, deploy a PCSG with an `ffn` clique spread across both domains and a `decode` clique with `topologyAffinity`, and verify that `decode` converges to `spec.replicas` pods per occupied domain.
* Single-domain cleanup: deploy the same workload with the referenced clique fitting in one domain, verify that topology-affinity pods are initially created in all known domains, then verify that pods outside the final target domain are deleted and `TopologyAffinityReady=True`.
* Scale behavior: scale the topology-affinity `PodClique` or owning PCSG and verify that Grove adds or removes the per-domain replica delta without changing the resolved target domain set.
* Failure handling: deploy a workload whose referenced clique cannot reach its scheduled minimum, set a short `terminationDelay`, and verify that overprovisioned topology-affinity pods wait in init until the delay expires and the affected PCSG or `PodCliqueSet` replica is gang-terminated.
* Topology-constraint precedence: deploy a workload with compatible PCS/PCSG/PCLQ topology constraints and `topologyAffinity`, then verify placement stays within the constrained topology and affinity only narrows placement to the referenced cliques' scheduled domains.
* PCSG scoping: deploy two PCSGs with similarly named cliques or deliberately invalid cross-PCSG references, and verify that valid affinity resolves only within the same PCSG replica while cross-PCSG references are rejected by admission.

### Graduation Criteria

* Alpha: Introduce a disabled-by-default feature flag `PodCliqueTopologyAffinity`.
* Beta: Use and evolve this internally while FFN/decode support is implemented.
* GA: Enable the flag by default when Dynamo support for FFN/decode deployments is released.

## Alternatives

### Placeholder pods

Rather than using an init container, ‘placeholder’ pods are created with the correct resource requests but running only the `registry.k8s.io/pause` image.

Once all pods are scheduled and ready, preemption is used to replace the placeholder pods with real pods.
This still requires underlying scheduler support to know when and how to preempt or race conditions apply.
This would require more complex pod management support in Grove.

### Two-phase scheduling

If scheduler support cannot place the decode and FFN pods correctly when created simultaneously, the `PodClique` with an affinity is scheduled first. In the examples, this is the decode worker. Then, based on the scheduled location of the existing pods, the FFN worker pods are created and can be scheduled appropriately before the extraneous decode pods are deleted.

If, instead, the FFN worker pods are scheduled first, this would require hints to be provided to the scheduler, otherwise the pods could be scheduled in a domain where there is no corresponding decode capacity.

Both solutions introduce higher potential for race conditions and incorrect placement.

### Higher-level implementation

It’s possible that the Dynamo Operator could implement this as a higher-level orchestration function.

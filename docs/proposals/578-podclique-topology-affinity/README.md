# GREP-578: PodClique affinity over topology domains

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Topology Representation and Enumeration](#topology-representation-and-enumeration)
    - [Scheduling](#scheduling)
  - [Validation](#validation)
  - [Scale-Up, Scale-In, and Rolling Updates](#scale-up-scale-in-and-rolling-updates)
- [User Stories](#user-stories)
    - [Story 1: Attention-FFN disaggregated inference](#story-1-attention-ffn-disaggregated-inference)
- [Limitations, Risks, and Mitigations](#limitations-risks-and-mitigations)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
  - [PodClique Status](#podclique-status)
  - [Failure Modes](#failure-modes)
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
* No special handling required in schedulers for the base case; Grove creates pods with required node affinity for the resolved topology label key and value. Special handling can be added as-needed.
* Support scale-up, scale-down, and rolling updates.

### Non-Goals
* Support dynamic rescheduling; if a pod is evicted and moved to a new topology domain, Grove will not ensure the affinity is still respected.
* Support for independent autoscaling of different target domains.

## Proposal

```yaml
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
    podCliqueScalingGroups:
      - name: workers
        replicas: 1
        minAvailable: 1
        cliqueNames:
          - ffn
          - decode
```

Grove will implement affinity between `PodClique` within a topology domain for cliques in the same `PodCliqueScalingGroup` (PCSG).

1. Resolve `topologyName` and `domain` to the required Node-label key and any configured ResourceSlice-attribute evidence mappings.
2. Enumerate the unique non-empty values of that Node label and create `spec.replicas` candidate Pods for the topology-affinity clique in every such domain.
3. Restrict every candidate to its assigned domain with required `key=value` Node affinity.
4. Inject the `grove-initc` init container into topology-affinity pods. The init container waits for the associated `cliqueNames` to be ready and for the owning `PodClique` to publish `TopologyAffinityReady=True`.
5. Once all associated `cliqueNames` reach their scheduled minimum, extract the union of their scheduled Pods' resolved domain values. ResourceSlice attributes may provide values from allocated devices.
6. Delete pods outside the target domain set or above `replicas` per target domain. `TopologyAffinityReady` is set once every target domain has exactly `replicas` non-terminating pods and no extra topology-affinity pods remain.

Affinity is resolved independently for each PCSG replica. Grove does not compute a union across different PCSGs or PCSG replica indexes. Standalone PodCliques are outside the initial scope.

### Topology Representation and Enumeration

The new `PodCliqueSet` API does not select Node labels, ResourceClaims, drivers, or ResourceSlice attributes. Those infrastructure mappings belong to the referenced `ClusterTopologyBinding`:

```yaml
apiVersion: grove.io/v1alpha1
kind: ClusterTopologyBinding
metadata:
  name: afd-disagg-fabric
spec:
  levels:
    - domain: block
      key: network.topology.nvidia.com/block-domain
      resourceSliceAttributes:
        - driver: myresource.nvidia.com
          name: network.topology.nvidia.com/block_domain
```

A `ClusterTopologyBinding` level requires a Node-label `key`. It may also contain driver-specific ResourceSlice attribute mappings. The key is used for topology-affinity target placement while ResourceSlice attributes are only used for resolving the domains of associated Pods with allocated devices.

Domain enumeration reads only the unique non-empty values for the configured Node label. For ResourceClaim-backed Pods ,  the attributes configured for a driver are read from allocated devices.

The scheduler remains responsible for satisfying the generated Node affinity together with any claim allocation constraints.

#### Scheduling

* With no underlying scheduler changes, this should work as-is for pods that do not require special considerations because Grove adds required node affinity for the resolved topology label key and value.
* The `PodGang` controller sizes topology-affinity pod groups from `PodClique.status.topologyAffinity.targetDomains`, requeueing until the topology-affinity status is available. The resulting `PodGroup.minReplicas` is the expanded replica count, `len(targetDomains) * spec.replicas`.
* The init container holds application startup until the associated cliques are ready and the topology-affinity clique has converged on its final target domains.
* Existing topology constraints take precedence over `topologyAffinity`.

### Validation

Admission and runtime validation enforce the following rules:

* No circular dependencies between two or more `PodClique` templates.
* A `PodClique` that defines `topologyAffinity` and every referenced `cliqueName` must belong to the same PCSG. Cross-PCSG references and references from standalone `PodCliques` are rejected.
* A `PodClique` that defines `podSpec.nodeSelector` or `podSpec.affinity.nodeAffinity` against the same resolved node label key as `topologyAffinity` will be rejected. Other node affinity outside of `podSpec.nodeName` is allowed.
  * Pod affinities will not be rejected, but could lead to unschedulable situations.
* The complete `topologyAffinity` value is immutable after PodCliqueSet creation. Adding, removing, or changing `topologyName`, `domain`, or `cliqueNames` requires workload recreation; live `replicas` remains mutable as the per-domain scale value.

### Scale-Up, Scale-In, and Rolling Updates

The implementation reconciles toward `len(targetDomains) * spec.replicas` pods. `spec.replicas` remains the per-domain scale value, while `status.totalReplicas` reports the expanded pod count. Scale-up creates only per-domain deficits. Scale-in and target-domain shrink delete pods outside target domains or above the per-domain count, using the normal deletion ordering so outdated pods are preferred during updates.

## User Stories

#### Story 1: Attention-FFN disaggregated inference

An operator runs a Kubernetes cluster with 12 GPU nodes:

* 6 GPU nodes are in `topology.kubernetes.io/network-domain=A`.
* 6 GPU nodes are in `topology.kubernetes.io/network-domain=B`.

A user would like to deploy their AFD workload to the cluster using a `PodCliqueSet`.

The user defines the `PodCliqueSet` as follows:

```yaml
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
    podCliqueScalingGroups:
      - name: inference
        replicas: 1
        minAvailable: 1
        cliqueNames:
          - ffn
          - decode
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

## Limitations, Risks, and Mitigations

* If the associated cliques never reach their scheduled minimum, topology-affinity pods continue to exist in all known domains and their init containers continue to wait until the owning PCSG or `PodCliqueSet` replica is terminated after `spec.template.terminationDelay`.
* We’re relying on a relatively static set of pre-created domains. This may not be well supported for cloud clusters where a domain might not be currently represented in the cluster (e.g., where scale-up into an availability zone would be supported).
* This changes the meaning of the `replicas` field in the presence of a `topologyAffinity`. Rather than the desired number of total replicas, it is per-topology domain. `status.replicas` preserves that per-domain value for scale clients, and `status.totalReplicas` reports the expanded total.
* `minAvailable` must equal `replicas` on the template and the effective minimum is expanded to `len(targetDomains) * replicas`.

## Design Details

### API Changes

`TopologyLevel` is extended so one logical domain may be represented by a Node nabel and by different attribute names from different DRA drivers:

```go
// TopologyLevel maps one logical topology domain to its infrastructure representations.
type TopologyLevel struct {
	// Domain is the logical topology level identifier.
	Domain TopologyDomain `json:"domain"`

	// Key is the required Node label key representing this domain and used for
	// all topology-affinity Pod placement.
	Key string `json:"key"`

	// ResourceSliceAttributes maps DRA drivers to the device attribute that
	// carries this domain's canonical string value.
	// +listType=map
	// +listMapKey=driver
	// +optional
	ResourceSliceAttributes []ResourceSliceAttributeReference `json:"resourceSliceAttributes,omitempty"`
}

// ResourceSliceAttributeReference identifies one driver's representation of a topology domain.
type ResourceSliceAttributeReference struct {
	// Driver is the DRA driver name published in ResourceSlice.spec.driver.
	Driver string `json:"driver"`

	// Name is the fully qualified DRA device attribute name.
	Name resourcev1.FullyQualifiedName `json:"name"`
}
```

`key` is required. `resourceSliceAttributes` are optional. Duplicate driver entries are invalid.

```go
type PodCliqueSpec struct {
	// Existing fields omitted.

	// Affinity defines inter-clique placement and startup relationships.
	// +optional
	Affinity *PodCliqueAffinity `json:"affinity,omitempty"`
}

// PodCliqueAffinity is a group of affinity scheduling rules for a PodClique.
type PodCliqueAffinity struct {
	// TopologyAffinity places this clique's Pods in the logical topology domains
	// occupied by the referenced cliques.
	// Immutable after PodCliqueSet creation.
	// +optional
	TopologyAffinity *TopologyAffinity `json:"topologyAffinity,omitempty"`
}

// TopologyAffinity defines PodClique topology affinity rules.
type TopologyAffinity struct {
	// TopologyName names the ClusterTopologyBinding used for domain resolution.
	// It is required, establishes the PodCliqueSet's single effective topology,
	// must match every other topology-affinity rule, and is immutable.
	TopologyName string `json:"topologyName"`

	// Domain names the logical topology level within the ClusterTopologyBinding.
	Domain TopologyDomain `json:"domain"`

	// CliqueNames is the list of names of the PodCliques that are part of the affinity group.
	// Each referenced PodClique must belong to the same PodCliqueScalingGroup as this PodClique.
	// Pods are scheduled at the union of all scheduled PodClique domains within the same PCSG replica.
	CliqueNames []string `json:"cliqueNames"`
}
```

### PodClique Status

`TopologyAffinityReady` is present whenever `spec.affinity.topologyAffinity` is defined. It is `False` while domains are unresolved, associated PodCliques have not reached their readiness threshold, or target candidates are still converging. It is `True` only when the final candidate set exactly matches the target domains and all readiness gates pass.

The condition uses a small stable reason set:

| Status | Reason | Meaning |
| --- | --- | --- |
| `False` | `FeatureDisabled` | The alpha feature gate is disabled for a pre-existing affine workload. |
| `False` | `TopologyUnavailable` | The binding, selected level, required DRA API, or topology inventory is unavailable. |
| `False` | `AssociatedCliquesNotReady` | Domain resolution is complete, but the associated cliques have not met their readiness requirement. |
| `False` | `TopologyDrift` | A scheduled candidate's current Node label disagrees with its assigned domain. |
| `True` | `` | Domain resolution, candidate placement, and associated readiness all satisfy the rule. |

```go
type PodCliqueStatus struct {
	// Replicas is the replica count exposed through the scale subresource.
	// For topology-affinity PodCliques, this matches Spec.Replicas so autoscalers scale per-domain replicas.
	// Use TotalReplicas for the total non-terminated Pod count across all topology domains.
	Replicas int32 `json:"replicas,omitempty"`

	// TotalReplicas is the total number of non-terminated Pods targeted by this PodClique.
	// For topology-affinity PodCliques, this is the expanded count across all topology domains.
	TotalReplicas int32 `json:"totalReplicas,omitempty"`

	// TopologyAffinity captures logical domain resolution and convergence state.
	// +optional
	TopologyAffinity *PodCliqueTopologyAffinityStatus `json:"topologyAffinity,omitempty"`
}

type PodCliqueTopologyAffinityStatus struct {
	// ObservedTopologyBindingGeneration is the ClusterTopologyBinding generation
	// last accepted for a complete topology resolution.
	ObservedTopologyBindingGeneration int64 `json:"observedTopologyBindingGeneration,omitempty"`

	// AllDomains is the set of unique non-empty values currently published for
	// the selected topology level's required Node label key.
	// +listType=set
	AllDomains []string `json:"allDomains,omitempty"`

	// AssociatedDomains is the resolved union for the associated PodCliques.
	// +listType=set
	AssociatedDomains []string `json:"associatedDomains,omitempty"`

	// TargetDomains is the domain set toward which this PodClique is reconciling.
	// +listType=set
	TargetDomains []string `json:"targetDomains,omitempty"`

	// AssociatedReady reports whether the associated PodCliques satisfy their
	// readiness threshold independently of domain discovery.
	AssociatedReady bool `json:"associatedReady,omitempty"`
}
```

### Failure Modes

* If the `ClusterTopologyBinding` cannot be resolved or the configured domain is removed, status reconciliation fails and the controller retries. Admission rejects this on create/update when the topology is already invalid.
* If an associated pod is scheduled onto a node that does not carry the resolved topology label key, Grove cannot resolve the associated domain and retries with an error.
* If one associated Pod's allocated devices resolve to multiple domain values, Grove rejects that Pod's topology resolution as ambiguous.
* If a ResourceSlice-derived associated value is not selectable through at least one Node carrying the configured `key=value`, Grove retries with an error rather than creating an unconstrained target.
* If associated domains are empty while any associated PodClique has non-zero replicas, Grove treats the evidence as unresolved rather than deleting every target Pod.

### Monitoring

Topology-affinity health is observable through `PodClique.status.topologyAffinity` and the `TopologyAffinityReady` condition. Operators can compare `allDomains`, `associatedDomains`, and `targetDomains` to understand whether Grove is still in the optimistic all-domain phase or has converged to the domains used by the associated cliques.

`status.replicas` remains the per-domain scale value for topology-affinity `PodCliques`; `status.totalReplicas`, `readyReplicas`, `scheduledReplicas`, `scheduleGatedReplicas`, and `updatedReplicas` report the actual pod state across all domains. Grove also emits the existing pod create/delete events and the `AllScheduledReplicasLost` warning when scheduled replicas drop from non-zero to zero.

### Test Plan

**Unit tests** for topology affinity should be included with the implementation in the following areas:

* API and generated-client coverage for `PodCliqueAffinity`, `TopologyAffinity`, `PodCliqueStatus.totalReplicas`, and `PodCliqueStatus.topologyAffinity`.
* PCS validating webhook tests for required `topologyName` and `domain`, valid referenced `cliqueNames`, rejection of circular dependencies, rejection of cross-PCSG or standalone topology-affinity references, rejection when `minAvailable != replicas`, and rejection of direct `nodeSelector` or `nodeAffinity` conflicts on the resolved topology label key.
* Topology resolver tests for dynamic label-key registration, metadata-only Node watches, unique value caching, allocated-device ResourceSlice evidence, rejection of per-Pod multi-value evidence, and rebuild behavior when a new topology mapping is registered.
* PodClique pod sync tests for optimistic per-domain pod creation, required `key=value` Node affinity injection for every candidate including claim-backed candidates, unchanged claims, init container injection, deletion of pods outside `targetDomains`, per-domain scale-up and scale-in, and rolling-update deletion ordering.
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

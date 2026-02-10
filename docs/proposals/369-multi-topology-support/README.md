# GREP-369: Support Multiple Topologies

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Heterogeneous GPU Clusters](#story-1-heterogeneous-gpu-clusters)
    - [Story 2: Multi-Cloud Clusters](#story-2-multi-cloud-clusters)
    - [Story 3: Topology Retry Before Scheduling](#story-3-topology-retry-before-scheduling)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
    - [ClusterTopology Deletion Protection](#clustertopology-deletion-protection)
    - [ClusterTopology Updates Blocked When In Use](#clustertopology-updates-blocked-when-in-use)
    - [Topology Reference Immutability After Scheduling](#topology-reference-immutability-after-scheduling)
    - [Two Management Paths for Topologies](#two-management-paths-for-topologies)
- [Design Details](#design-details)
  - [Change Summary](#change-summary)
  - [PodCliqueSet API Changes](#podcliqueset-api-changes)
  - [ClusterTopology Lifecycle](#clustertopology-lifecycle)
  - [Admission Control](#admission-control)
  - [Backward Compatibility](#backward-compatibility)
  - [Monitoring](#monitoring)
  - [Dependencies (<em>Optional</em>)](#dependencies-optional)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History (<em>Optional</em>)](#implementation-history-optional)
- [Alternatives (<em>Optional</em>)](#alternatives-optional)
  - [Fully Operator-Managed Topologies](#fully-operator-managed-topologies)
- [Appendix (<em>Optional</em>)](#appendix-optional)
<!-- /toc -->

<!--
Include a table of contents as it helps to navigate easily in the document.

Ensure the TOC is wrapped with
   <code>&lt;!-- toc --&gt;&lt;!-- /toc --&gt;</code>
tags, and then generate by invoking the make target `update-toc`.

-->

## Summary

Heterogeneous clusters may contain hardware with different topology characteristics, such as multiple GPU architectures or nodes spanning multiple cloud providers. This GREP proposes extending the Grove topology API to support multiple named topologies, allowing workloads to specify which topology applies to them.

## Motivation

Grove's topology API currently supports a single cluster-wide topology defined in the OperatorConfiguration. This works well for homogeneous clusters but becomes limiting when infrastructure diversity increases.

AI clusters often contain heterogeneous hardware with different interconnect characteristics, or span multiple cloud environments. Each hardware type or environment may require a distinct topology definition for optimal scheduling.

Enabling multiple topologies allows:

- **Accurate Infrastructure Modeling**: Administrators can define topologies matching their actual hardware rather than using a single approximation.
- **Workload Portability**: Users can target specific topologies without embedding infrastructure details into workload specifications.

### Goals

- Define a mechanism for creating multiple named topology resources within a single cluster
- Extend the PodCliqueSet API to reference a specific topology
- Provide default topology selection when none is explicitly specified
- Maintain backward compatibility with existing deployments

### Non-Goals

- Automatic topology discovery or inference from node labels
- Dynamic topology switching for running workloads
- Cross-topology scheduling within a single PodCliqueSet

## Proposal

This GREP extends the existing topology API to support multiple ClusterTopology resources within a single cluster, and adds a reference mechanism for PodCliqueSets to select which topology applies.

The existing `TopologyConstraint` API, where users specify topology domains (e.g., "rack", "host"), remains unchanged. What changes is how the operator resolves which ClusterTopology to use when translating those domain names to infrastructure-specific node label keys.

The approach has four parts:

1. **Multiple ClusterTopology resources**: Remove the restriction to a single operator-managed ClusterTopology. Allow administrators to create multiple named ClusterTopology resources, each describing a different topology hierarchy.

2. **Topology reference on PodCliqueSet**: Add a [`clusterTopologyName`](#podcliqueset-api-changes) field to PodCliqueSet that references a specific ClusterTopology by name. This determines which topology definition is used for scheduling. The PCS admission controller rejects creation or update if the referenced ClusterTopology does not exist.

3. **Default topology**: When no topology is explicitly referenced, a designated default topology is used, preserving backward compatibility.

4. **Mutable topology reference until scheduling**: The topology reference on a PodCliqueSet can be changed while the workload is not yet running, allowing users to retry scheduling against a different topology. Once any pod in the gang has been scheduled, the topology reference becomes immutable and updates are rejected.

### User Stories

#### Story 1: Heterogeneous GPU Clusters

As a cluster administrator managing a cluster with different GPU architectures, I want to define separate topologies for each architecture so that workloads are scheduled with topology awareness appropriate to the hardware they're running on.

**Example:** A cluster contains both DGX H100 nodes and GB200 NVL72 racks. These architectures have different topology depths and interconnect characteristics:

- **DGX H100**: 8 GPUs per node connected via NVLink 4.0 at 900 GB/s per GPU. Cross-node communication falls back to InfiniBand, which is significantly slower. The topology has three levels: zone → rack → host, and packing workloads at the host level keeps them within the high-bandwidth NVLink domain.
- **GB200 NVL72**: 72 GPUs connected via rack-level NVSwitch using NVLink 5.0 at 1.8 TB/s per GPU. All GPUs in the rack communicate at NVLink speed — there is no slower intra-rack tier. The topology has four levels: zone → block → rack → host, where block groups racks sharing a spine switch for cross-rack MNNVL traffic.

A single ClusterTopology defines one hierarchy for all nodes. Using the 4-level GB200 hierarchy would require adding synthetic "block" labels to all H100 nodes. With separate ClusterTopology resources, each architecture uses its natural hierarchy, and workloads select the appropriate one via `clusterTopologyName`.

#### Story 2: Multi-Cloud Clusters

As a platform engineer managing AI workloads on a cluster spanning multiple cloud environments, I want to define environment-specific topologies so that workloads get optimal placement regardless of where they run, without users needing to know the underlying infrastructure details.

#### Story 3: Topology Retry Before Scheduling

As a user submitting a PodCliqueSet to a cluster with multiple scheduling shards, I want to be able to change the target topology while my workload is pending, so I can retry on a different shard if the first one cannot accommodate my gang. Once the gang starts running, the topology should be locked.

### Limitations/Risks & Mitigations

#### ClusterTopology Deletion Protection

A ClusterTopology resource cannot be deleted while any PodCliqueSet references it via `clusterTopologyName`. A finalizer is added to each ClusterTopology by a controller to enforce this. The finalizer is only removed once no PCS references the topology.

#### ClusterTopology Updates Blocked When In Use

Updates to a ClusterTopology's topology levels are blocked if any PodCliqueSet with scheduled pods references it. This prevents topology definition changes from invalidating in-flight scheduling decisions. Level updates are allowed when all referencing PCSs have no scheduled pods. Metadata changes (labels, annotations) are not blocked.

#### Topology Reference Immutability After Scheduling

The `clusterTopologyName` field on a PodCliqueSet becomes immutable once any pod in the PCS has been scheduled (bound to a node). The scheduler has already made placement decisions based on the current topology, and changing it would invalidate those decisions. Users can change `clusterTopologyName` freely while all pods are still pending, supporting the topology retry use case (Story 3).

#### Two Management Paths for Topologies

The default ClusterTopology is managed through the operator's Helm configuration, while additional topologies are created directly by administrators. The operator-managed default is labeled with `app.kubernetes.io/managed-by: grove-operator` and is reconciled to match the operator configuration. User-created topologies are not reconciled by the operator and should not use the reserved default topology name.

## Design Details

### Change Summary

This proposal requires the following changes:

1. **PodCliqueSet API**: Add optional `clusterTopologyName` field to `PodCliqueSetTemplateSpec`
2. **ClusterTopology validating webhook** (new): Block topology level updates when any PCS with scheduled pods references the topology
3. **ClusterTopology controller** (new): Watch ClusterTopology resources and manage finalizers based on PCS references
4. **PCS validating webhook**: Add `clusterTopologyName` existence check, allow topology reference changes when no pods are scheduled, reject when TAS is disabled or no `TopologyConstraint` is set
5. **PCS reconciler**: Resolve `clusterTopologyName` (or the default) to the correct ClusterTopology when building the PodGang, including fetching the correct topology levels and setting the topology name annotation on the PodGang
6. **Startup synchronization**: Label the default topology with `app.kubernetes.io/managed-by: grove-operator` and handle finalizer removal when TAS is disabled

### PodCliqueSet API Changes

A new optional field `clusterTopologyName` is added to `PodCliqueSetTemplateSpec`, alongside the existing `topologyConstraint` field:

```go
type PodCliqueSetTemplateSpec struct {
    // ... existing fields ...

    // ClusterTopologyName is the name of the ClusterTopology resource to use for topology-aware scheduling.
    // If not specified and TAS is enabled, the default cluster topology is used.
    // +optional
    ClusterTopologyName string `json:"clusterTopologyName,omitempty"`

    // TopologyConstraint defines topology placement requirements for PodCliqueSet.
    // +optional
    TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`

    // ... existing fields ...
}
```

**Example YAML:**

```yaml
apiVersion: core.grove.ai/v1alpha1
kind: PodCliqueSet
metadata:
  name: my-inference
spec:
  template:
    clusterTopologyName: aws-p5    # references a ClusterTopology; if omitted, uses grove-topology
    topologyConstraint:
      packDomain: rack
    cliques:
      - name: worker
        # ...
```

**Validation rules:**
- On create: if `clusterTopologyName` is set, the referenced ClusterTopology must exist
- On update: if `clusterTopologyName` is changed, the new ClusterTopology must exist and no pod in the PCS may be scheduled (`ScheduledReplicas == 0` across all PodCliques)

### ClusterTopology Lifecycle

ClusterTopology resources follow a hybrid management model:

- **Operator-managed default**: The operator creates and owns the default ClusterTopology (`grove-topology`) from the `OperatorConfiguration.TopologyAwareScheduling` settings. It is labeled with `app.kubernetes.io/managed-by: grove-operator` and reconciled to match the operator configuration on each startup. Manual changes to the default topology are overwritten.
- **User-managed topologies**: Administrators can create additional ClusterTopology resources directly via kubectl or GitOps. These are not reconciled by the operator.

**Deletion protection**: A controller watches ClusterTopology resources and adds finalizers. The finalizer prevents deletion while any PCS references the topology via `clusterTopologyName`. When TAS is disabled, the operator handles removing finalizers on the default topology.

### Admission Control

**PCS validating webhook changes:**

The existing PCS validating webhook is extended with the following rules for `clusterTopologyName`:
- On create: if `clusterTopologyName` is set, the referenced ClusterTopology must exist
- On update: if `clusterTopologyName` is changed, the new ClusterTopology must exist and no pod in the PCS may be scheduled (`ScheduledReplicas == 0` across all PodCliques)
- Reject if `clusterTopologyName` is set but no `TopologyConstraint` is specified on the PCS, any PCSG, or any PodClique
- Reject if `clusterTopologyName` is set but TAS is disabled cluster-wide
- Existing `TopologyConstraint` immutability rules remain unchanged

**New ClusterTopology validating webhook:**

A new validating webhook is introduced for ClusterTopology:
- Block updates to a ClusterTopology's topology levels if any PCS with scheduled pods (`ScheduledReplicas > 0`) references it
- Block deletion is handled by the finalizer (see [ClusterTopology Lifecycle](#clustertopology-lifecycle))

### Backward Compatibility

Migration is zero-effort for existing users:

- The operator continues to create `grove-topology` from OperatorConfiguration
- Existing PCSs without `clusterTopologyName` use `grove-topology` by default
- No changes required to existing workload specifications
- Multi-topology support is purely additive

The operator resolves the topology as follows:
- PCS has `TopologyConstraint` set but no `clusterTopologyName` → use `grove-topology` (default)
- PCS has no `TopologyConstraint` at any level → topology does not apply
- PCS has an explicit `clusterTopologyName` → use that topology

### Monitoring

Understanding which topologies are in use is important before attempting updates or deletions. Administrators can list which PodCliqueSets reference each ClusterTopology using kubectl:

```bash
# List all PCSs grouped by their ClusterTopology reference
kubectl get podcliquesets -A -o json | jq -r '
  .items[]
  | {
      name: .metadata.name,
      namespace: .metadata.namespace,
      topology: (.spec.template.clusterTopologyName // "grove-topology (default)")
    }
  | [.topology, .namespace + "/" + .name]
  | @tsv
' | sort | column -t
```

Example output:
```
aws-p5                   ml-team/inference-llama
aws-p5                   ml-team/inference-mixtral
gcp-a3                   batch/training-job-1
grove-topology (default) default/simple-inference
```

### Dependencies (*Optional*)

This feature has the same dependencies as the existing [Topology Aware Scheduling](../244-topology-aware-scheduling/README.md#dependencies) implementation: a scheduler backend with TAS support and nodes labeled with topology-specific labels.

Additionally, the scheduling backend must support multiple Topology resources within a single cluster. KAI Scheduler supports this.

### Test Plan

**Unit Tests:**
- PCS validating webhook: `clusterTopologyName` existence check on create and update, immutability after scheduling, rejection when TAS is disabled or no `TopologyConstraint` is set
- ClusterTopology validating webhook: block updates when referenced by PCSs with scheduled pods
- ClusterTopology controller: finalizer addition and removal logic
- PCS reconciler: topology resolution logic that resolves `clusterTopologyName` (or the default) to the correct ClusterTopology when building the PodGang

**E2E Tests:**
- Extend existing TAS e2e tests to include a multi-topology case: relabel a subset of worker nodes with different topology label keys, create a second ClusterTopology resource referencing those keys, deploy a PCS with `clusterTopologyName` pointing to the new topology, and verify that pods are placed correctly and the KAI PodGroup references the expected topology

### Graduation Criteria

<!-- 
In this section graduation milestones should be defined. The progression of the overall feature can be evaluated w.r.t API maturity, staged sub-feature implementation or some other criteria.

In general we try to use the same stages (alpha, beta, GA), regardless of how the
functionality is accessed. Refer to these for more details:"

* [Feature Gates](https://git.k8s.io/community/contributors/devel/sig-architecture/feature-gates.md)
* [Maturity levels](https://git.k8s.io/community/contributors/devel/sig-architecture/api_changes.md#alpha-beta-and-stable-versions)
* [Deprecation Policy](https://kubernetes.io/docs/reference/using-api/deprecation-policy/ ) 

**Note:** Generally we also wait at least two releases between beta and
GA/stable, because there's no opportunity for user feedback, or even bug reports,
in back-to-back releases.
-->

**Alpha** — minimal usability with no safeguards:
- PodCliqueSet API: add optional `clusterTopologyName` field to `PodCliqueSetTemplateSpec`
- PCS reconciler: resolve `clusterTopologyName` (or the default) to the correct ClusterTopology when building the PodGang

**Beta** — safeguards on ClusterTopology for cluster administrators:
- ClusterTopology validating webhook: block topology level updates when any PCS with scheduled pods references the topology
- ClusterTopology controller: watch ClusterTopology resources and manage finalizers based on PCS references
- Startup synchronization: label the default topology with `app.kubernetes.io/managed-by: grove-operator` and handle finalizer removal when TAS is disabled

**GA** — safeguards on PCS for ML engineers:
- PCS validating webhook: add `clusterTopologyName` existence check, allow topology reference changes when no pods are scheduled, reject when TAS is disabled or no `TopologyConstraint` is set

## Implementation History (*Optional*)

<!--
Major milestones in the lifecycle of a GREP should be tracked in this section.
Major milestones might include:

- The date proposal was accepted and merged.
- The date implementation started.
- The date of Alpha release for the feature.
- The date the feature graduated to beta/GA

-->

## Alternatives (*Optional*)

<!--
What are the alternative approaches considered and reasons to rule those out. This section should have sufficient details (not too much) to express the alternative idea and why it was not accepted.
-->

### Fully Operator-Managed Topologies

An alternative is to define all topologies in the OperatorConfiguration ConfigMap rather than allowing administrators to create ClusterTopology resources directly. The operator would reconcile all topologies from its configuration at startup, extending the current single-topology model to a list.

This approach offers a single source of truth and a simpler ownership model — the operator fully manages all topologies with no hybrid management paths.

However, because the ConfigMap is managed by the Helm chart, it operates outside of the webhook and finalizer safeguards. A `helm upgrade` that removes a topology from the config would cause the operator to delete the corresponding ClusterTopology on next startup, even if PodCliqueSets still reference it. The finalizer would prevent the deletion, creating a stuck state where the config and the cluster are not aligned. Similarly, a Helm-driven update to a topology's levels would conflict with the protection against modifying topologies referenced by scheduled workloads. Additionally, topology management is coupled to the operator lifecycle — adding a topology for a new hardware segment requires redeploying the operator.

With the hybrid model chosen in this proposal, only the default topology has this Helm-managed lifecycle, which is acceptable since it matches today's behavior. Additional topologies are standard Kubernetes resources subject to the full admission control and finalizer protections, and can be managed independently by different teams.

## Appendix (*Optional*)

<!-- 
Use this section to put any prerequisite reading links or helpful information/data that supplements the proposal, thus providing additional context to the reviewer.
-->

> NOTE: This GREP template has been inspired by [KEP Template](https://github.com/kubernetes/enhancements/blob/f90055d254c356b2c038a1bdf4610bf4acd8d7be/keps/NNNN-kep-template/README.md).


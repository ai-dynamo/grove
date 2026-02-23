# GREP-244: Topology Aware Scheduling

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Workload Portability](#story-1-workload-portability)
    - [Story 2: Disaggregated Inference Locality](#story-2-disaggregated-inference-locality)
    - [Story 3: NUMA-Aware GPU Benchmarking](#story-3-numa-aware-gpu-benchmarking)
    - [Story 4: Heterogeneous GPU Clusters](#story-4-heterogeneous-gpu-clusters)
    - [Story 5: Topology Retry Before Scheduling](#story-5-topology-retry-before-scheduling)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
    - [Topology Constraints Only Guaranteed for Initial Deployment](#topology-constraints-only-guaranteed-for-initial-deployment)
    - [Operational Complexity](#operational-complexity)
    - [Topology Configuration Drift](#topology-configuration-drift)
    - [Topology Aware Cluster Autoscaling](#topology-aware-cluster-autoscaling)
    - [Workload Portability](#workload-portability)
    - [ClusterTopology Deletion Protection](#clustertopology-deletion-protection)
    - [ClusterTopology Updates Blocked When In Use](#clustertopology-updates-blocked-when-in-use)
    - [Topology Reference Immutability After Scheduling](#topology-reference-immutability-after-scheduling)
    - [Two Management Paths for Topologies](#two-management-paths-for-topologies)
- [Design Details](#design-details)
  - [Cluster Admin API](#cluster-admin-api)
    - [Supported Topology domains:](#supported-topology-domains)
    - [Validation](#validation)
    - [Operator Startup behavior](#operator-startup-behavior)
    - [Topology Configuration Updates](#topology-configuration-updates)
  - [ClusterTopology custom resource](#clustertopology-custom-resource)
    - [ClusterTopology Lifecycle](#clustertopology-lifecycle)
  - [Topology Constraints in PodCliqueSet](#topology-constraints-in-podcliqueset)
    - [ClusterTopology Reference](#clustertopology-reference)
    - [Validation](#validation-1)
  - [PodGang: Scheduler API Enhancements](#podgang-scheduler-api-enhancements)
  - [Backward Compatibility](#backward-compatibility)
  - [Monitoring](#monitoring)
  - [Dependencies](#dependencies)
  - [Test Plan](#test-plan)
- [Alternatives](#alternatives)
  - [Fully Operator-Managed Topologies](#fully-operator-managed-topologies)
<!-- /toc -->

## Summary

AI Inference workloads require low-latency data transfer between model layers or shards. Topology-aware placement of such workloads is critical to maximize performance on GPU scale-out clusters. This GREP proposes a unified topology model in Grove and introduces new API for users to define scheduling constraints that will guarantee topology optimized placement of their workloads.

In clusters with heterogeneous hardware, a single topology definition cannot accurately represent different interconnect hierarchies. This GREP also extends the topology API to support multiple named ClusterTopology resources, enabling administrators to partition the cluster into segments with distinct topology hierarchies. Each ClusterTopology defines its own set of node label keys, so nodes matching one topology's labels are naturally separated from nodes matching another. Workloads select the appropriate partition via a `clusterTopologyName` field on PodCliqueSet.

## Motivation

In multi-node disaggregated AI inference workloads, minimizing time-to-first-token (TTFT) and maximizing tokens per second (TPS) are key objectives. This requires optimal placement of prefills and decodes across Kubernetes nodes. These applications are highly sensitive to network latency and bandwidth, as model shards, leaders, and workers frequently exchange large volumes of data. Topology-aware scheduling is therefore essential to:

* Maximize network locality and leverage high-bandwidth interconnects.
* Minimize network hops between interdependent components.
* Co-locate related model shards within the same topology domain (rack/zone/host group).

Since different inference workloads have distinct communication patterns and packing needs, an advanced scheduler, such as KAI, is necessary to ensure topology optimized workload scheduling. Workload operators must be able to declaratively specify their topology and packing requirements when defining `PodCliqueSet`s. Combining expressive workload intent with topology-aware scheduling unlocks significant latency and throughput improvements for production-scale, multi-node LLM inference.

AI clusters often contain heterogeneous hardware with different interconnect characteristics. Each hardware type may require a distinct topology definition for optimal scheduling. For example, a cluster with both 3-level (zone > rack > host) DGX H100 nodes and 4-level (zone > block > rack > host) GB200 NVL72 racks cannot be accurately represented by a single topology definition. Supporting multiple ClusterTopology resources allows administrators to effectively split the cluster along hardware boundaries, where each topology definition captures the interconnect hierarchy of a specific hardware segment. This enables:

* **Cluster Partitioning by Hardware**: Each ClusterTopology defines its own node label keys, naturally partitioning the cluster into segments. Workloads targeting a specific topology are scheduled only on nodes that match that topology's labels.
* **Accurate Infrastructure Modeling**: Administrators can define topologies matching their actual hardware rather than forcing a single approximation across different interconnect hierarchies.
* **Workload Portability**: Users specify topology domains (e.g., "rack", "block") without embedding infrastructure-specific label keys into their workload definitions.

### Goals

* Define a uniform cluster topology model for any Kubernetes cluster across cloud providers and on-prem clusters.
* Enable cluster administrator to declaratively specify the cluster network topology (manually or auto-generated by a tool) as a startup configuration option for Grove operator.
* Extend the existing Grove declarative APIs to provide a way to define hierarchical topology pack constraints at `PodCliqueSet`, `PodCliqueScalingGroup` and `PodClique` levels.
* Enhance existing Grove scheduler APIs (`PodGang`) to translate user-defined topology constraints defined in `PodCliqueSet` to cluster-specific scheduling constraints.
* Automatically generate and synchronize relevant custom resources for the downstream schedulers that implement topology-aware-scheduling.
* Define a mechanism for creating multiple named ClusterTopology resources within a single cluster.
* Extend the PodCliqueSet API to reference a specific ClusterTopology via `clusterTopologyName`.
* Provide default topology selection when none is explicitly specified.
* Maintain backward compatibility with existing deployments.

### Non-Goals

* Honoring defined pack constraints for scale-outs for any `Scale` subresource in a `PodCliqueSet`.
* Define and honor pack constraints for proportional scaling amongst scaling groups. For e.g. one wishes to proportionally scale decodes and prefills in a disaggregated inference workload and ensure that decodes and prefills for every such scale are packed optimally.
* Automatic topology discovery or inference from node labels.
* Dynamic topology switching for running workloads.
* Cross-topology scheduling within a single PodCliqueSet.

## Proposal

<img src="assets/tas-highlevel-architecture.excalidraw.excalidraw.png" alt="tas-overview" style="zoom:30%;" />

Grove implements topology-aware scheduling through a two-layer approach:

**Admin Layer:**
Grove defines a ClusterTopology CRD and manages the lifecycle of a default ClusterTopology CR of a reserved name. Administrators configure the default topology hierarchy through `OperatorConfiguration`, and Grove creates a single operator-managed `ClusterTopology` resource named "grove-topology". This ensures a predictable, centrally-managed topology configuration for the common case. For clusters with heterogeneous hardware, administrators can create additional ClusterTopology resources directly via kubectl or GitOps to describe different topology hierarchies. These user-managed topologies are not reconciled by the operator.

The scheduling backend determines what additional resources it requires for topology-aware scheduling. Grove creates these resources based on backend-specific configuration in the `OperatorConfiguration`. Each scheduler backend defines its own configuration struct within its `SchedulerProfile.Config` (see [GREP-375: OperatorConfiguration Extension](../375-scheduler-backend-framework/README.md#operatorconfiguration-extension)). For example, the KAI scheduler requires a `Topology` custom resource for each ClusterTopology. When the KAI scheduler profile is enabled, Grove creates and manages a KAI `Topology` CR for every ClusterTopology in the cluster, both the operator-managed default and any administrator-created topologies. This behavior is controlled by `KaiSchedulerConfig.CreateTopologyResources`, which defaults to `true` when the KAI profile is active.

```yaml
# OperatorConfiguration snippet — enables KAI topology resource creation
scheduler:
  profiles:
    - name: "kai-scheduler"
      config:
        createTopologyResources: true
      default: true
```

```go
// KaiSchedulerConfig holds the configuration for the KAI scheduler backend.
type KaiSchedulerConfig struct {
    // CreateTopologyResources controls whether Grove creates and manages
    // KAI Topology CRs for each ClusterTopology resource in the cluster.
    // Defaults to true when the KAI scheduler profile is enabled.
    // +optional
    CreateTopologyResources *bool `json:"createTopologyResources,omitempty"`
}
```

When the KAI scheduler profile is enabled and `createTopologyResources` is omitted, it defaults to `true`. This means enabling the KAI profile is sufficient to get topology resource management — administrators only need to set this field explicitly to opt out (`createTopologyResources: false`).

**User Layer:**
Workload developers can specify topology constraints at three hierarchical levels (`PodCliqueSet`, `PodCliqueScalingGroup`, and `PodClique`) using domain names. They can also select which ClusterTopology to use via the `clusterTopologyName` field on PodCliqueSet. When no topology is explicitly referenced, the default `grove-topology` is used.

The operator validates these constraints against the referenced ClusterTopology using three key validation rules:

1. *Domain existence*: All topology domains referenced in workload's topology constraints must exist in the ClusterTopology CR. This ensures workloads only reference valid, configured topology levels.
2. *Topology Constraint Hierarchy*:  Topology levels are ordered by their total network distance in each level, from maximum (`region`) down to minimum (`numa`). When topology constraints are hierarchically applied to a workload from  PodCliqueSet → PodCliqueScalingGroup → PodClique, each level's  constraints must be set to equal or lower than the parent level workload primitive. A child resource cannot specify a higher topology domain  than its parent. For example, if PodCliqueSet specifies `rack`, then PodCliqueScalingGroup can specify `rack` (equal), `host` (lower), or `numa` (lowest), but not `zone` or `region` (higher).
3. *ClusterTopology reference*: If `clusterTopologyName` is set on a PodCliqueSet, the referenced ClusterTopology must exist. The reference can only be changed while no pods in the PCS are scheduled.

After validation, the operator translates the topology domain names (e.g., "rack", "host") into cluster-specific topology keys (e.g., "topology.kubernetes.io/zone", "kubernetes.io/hostname") using the referenced ClusterTopology and configures these hierarchical topology keys in the `PodGang` API . The `PodGang` serves as an intermediate representation that will eventually be mapped to the specific types that the configured scheduler backend understands. This abstraction allows workload portability across clusters with different topology configurations and scheduler implementations.

**Workload portability across clusters**

Grove, via `ClusterTopology`, defines a uniform set of topology domain names (e.g. `rack`, `zone`, `host`) that abstract away infrastructure-specific node labels. Workloads specify topology constraints using these domain names, and the operator resolves them to the correct label keys for the target cluster. This indirection enables workload portability across clusters with different infrastructure providers and topology configurations.

### User Stories

#### Story 1: Workload Portability

As a cluster admin who manages multiple clusters, I would like a capability to configure `Grove` to use infrastructure provider specific node labels mapped to uniform topology domains, thus allowing migration of workloads across clusters without impacting the topology pack constraints defined on `PodCliqueSet` resources.

#### Story 2: Disaggregated Inference Locality

As an AI application developer running disaggregated inference workloads at scale, I need my multi-node prefill and decode tasks to be co-located within a high-performance network domain to minimize the KV cache transfer latency. Grove's topology model should allow me to specify my locality requirement as a scheduling constraint so that my application runs with deterministic performance.

#### Story 3: NUMA-Aware GPU Benchmarking

As a software developer of benchmarking applications, when I request only 2 GPUs from a 8-GPU node, I want the two GPUs to be allocated on the same NUMA node along with all the CPUs. This will optimize communication costs between the host and device resulting in benchmark performance improvements. On GPU generations before NVSwitch, this optimization is also critical to optimize GPU-GPU communication costs over NVLink.

#### Story 4: Heterogeneous GPU Clusters

As a cluster administrator managing a cluster with different GPU architectures, I want to define separate topologies for each architecture to partition the cluster into hardware-specific segments. Each topology captures the interconnect hierarchy of its hardware, and workloads targeting a specific topology are scheduled only on nodes whose labels match that topology's definitions.

For a concrete example with DGX H100 and GB200 NVL72 hardware demonstrating the H100 and GB200 paths, see [Story 4: Heterogeneous GPU Cluster Example](story-4-heterogeneous-gpu-example.md).

#### Story 5: Topology Retry Before Scheduling

As a user submitting a PodCliqueSet to a cluster with multiple scheduling shards, I want to be able to change the target topology while my workload is pending, so I can retry on a different shard if the first one cannot accommodate my gang. Once the gang starts running, the topology should be locked.

### Limitations/Risks & Mitigations

#### Topology Constraints Only Guaranteed for Initial Deployment

Topology-aware scheduling constraints are only guaranteed to be honored during the initial deployment of a PodCliqueSet. In several scenarios, these constraints may not be satisfied:

**Scale-Out scenarios:**

*Scale-Out of PodClique:*
This will result in creation of additional Pods. There is no guarantee that the topology constraints will be honoured for these additional Pods as that is subject to resource availability.

*Scale-Out of PodCliqueScalingGroup:*
This results in creation of a new `PodGang`. At present there is no way to correlate multiple `PodGang`s to KAI scheduler as belonging to a single PCS replica. If there is a topology constraint defined at the `PodCliqueSet` level, then without the association amongst `PodGang`s it is not possible to enforce that all Pods that are part of the correlated PodGangs respect the topology constraint defined for a `PodCliqueSet` replica.

Consider the following example:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: hierarchical-inference
spec:
  replicas: 2
  template:
    topologyConstraint:
      packDomain: zone  # Each replica within a zone
    cliques:
      - name: p-leader
        topologyConstraint:
          packDomain: host  # Each leader on its own host
        spec:
          replicas: 2
      - name: p-worker
        spec:
          replicas: 4  # Workers spread across hosts in the rack
    podCliqueScalingGroups:
      - name: prefill
        topologyConstraint:
          packDomain: "block"
        replicas: 3
        minAvailable: 1
        cliqueNames:
          - p-worker
          - p-leader
```

In the above `PodCliqueSet` for replica indexes 1 and 2 (above the `minAvailable`) of `prefill` PodCliqueScalingGroup two new scaled `PodGang`s will be created. Each `PodGang` will have `p-leader` and `p-worker` PodClique Pods which should all be scheduled such that they are packed within topology domain `block`. However there is no guarantee that these `block` topology domains should be within the same `zone` (topology constraint on a PodCliqueSet replica) for a single PodCliqueSet replica.

**Pod Rescheduling Scenarios**

Pods have to rescheduled when:

* When there are higher priority pods which wish to use resources that are used by a lower priority workload Pods.
* Node failures or maintenance which causes pod evictions.
* Explicit deletion of Pods

Without resource reservation for `PodCliqueSet`, the scheduler cannot satisfy topology constraints since other workloads might consume the resources in preferred node-pod placements.

#### Operational Complexity

Providing a way to define cluster topology entails that the cluster administrators must:

* Understand the cluster network topology.
* Ensure that the nodes are correctly labeled with the topology information.
* Define appropriate topology levels in `OperatorConfiguration` when configuring the `Grove` operator.
* When using multiple topologies, create and maintain additional `ClusterTopology` resources for each hardware type that differs from the default topology.

While validations will be provided to ensure that an admin does not configure an unsupported topology domain, however there is no way for `Grove` operator to ensure that the node labels that are mapped to each topology domain are in line with the ones that will be put onto nodes in a kubernetes cluster.

**Mitigation**

* Adequate documentation will be provided to the cluster administrators to help them properly configure topology levels in `OperatorConfiguration`.
* Tools like [Topograph](https://github.com/NVIDIA/topograph) can be leveraged to automate discovery of cluster network topology and ensuring that topology levels are added as labels on Kubernetes Node(s).

#### Topology Configuration Drift

If a ClusterTopology's levels change (e.g. adding or removing levels), existing PodCliqueSets that reference it may have topology constraints that no longer match the available levels.

The risk varies by topology type. For user-created ClusterTopology resources, the validating webhook blocks level updates when any PCS with scheduled pods references the topology (see [ClusterTopology Lifecycle](#clustertopology-lifecycle)), so drift can only occur for PCSs that have no scheduled pods at the time of the update. For the default `grove-topology`, levels are updated via `OperatorConfiguration` changes followed by an operator restart, which bypasses the webhook and can cause drift for any referencing PCS.

**Mitigation:**

Grove operator will:

* Remove invalid topology constraints from the `PodGang` resource(s) that are created for a `PodCliqueSet`.
* Clearly reflect that one or more topology levels are no longer available by setting appropriate status conditions on respective `PodCliqueSet` resources (see [Monitoring](#monitoring)).
* Ensure that the validating webhook rejects new `PodCliqueSet` resources that reference topology domains not present in the referenced ClusterTopology.

#### Topology Aware Cluster Autoscaling

When there are insufficient nodes to gang schedule PodGangs created from a PodCliqueSet, cluster autoscalers need to provision additional nodes. However, there is currently *no support from any cluster autoscaling solution* to launch nodes that would match the topology-aware scheduling (TAS) constraints defined within a PodGang. Underline reason is that no public cloud provider today provides APIs offering an ability to specify preferences on topology placements when launching instances. In addition none of the existing cluster autoscaling solutions (CA - [Issue#8783](https://github.com/kubernetes/autoscaler/issues/8783), Karpenter) have first class support for gang scheduled pod groups.

*Impact:* `PodGang`'s with strict topology constraints may remain unscheduled indefinitely.

**Mitigation**

There are ways in which you can either minimize the need for on-demand scaling or reduce the risk of pods remaining in pending state.

* Leverage cloud provider capabilities

  * AWS provides [Cluster Placement Groups](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/placement-groups.html)
  * GCP provides [Compact Placement Policies](https://docs.cloud.google.com/compute/docs/instances/use-compact-placement-policies)
  * Azure provides [Proximity Placement Groups](https://learn.microsoft.com/en-us/azure/virtual-machines/co-location)

  However these might not always give you the best packing as they only offer best-effort placement of newly launched nodes. So the best bet is to club placement policies with capacity reservation.

#### Workload Portability

`PodCliqueSet` with strict topology constraints may not always be portable across clusters which are created with different topology configurations. For example: A workload requiring "block" level packing may fail on a cluster that does not define this topology level.

**Mitigation**

Validating webhook for `PodCliqueSet` will reject resources that are created with unsupported topology constraints.

#### ClusterTopology Deletion Protection

A ClusterTopology resource cannot be deleted while any PodCliqueSet references it via `clusterTopologyName`. A controller watches ClusterTopology resources and adds a finalizer to each one. The finalizer is only removed once no PodCliqueSet references the topology, at which point the deletion proceeds.

**Mitigation**

* The finalizer-based approach ensures that topology resources are never removed while workloads depend on them. Administrators can identify which PodCliqueSets reference a given topology using the kubectl query described in [Monitoring](#monitoring), then migrate or delete those workloads before retrying the deletion.

#### ClusterTopology Updates Blocked When In Use

Updates to a ClusterTopology's topology levels are blocked by a validating webhook if any PodCliqueSet with scheduled pods references it. This prevents topology definition changes from invalidating in-flight scheduling decisions. Level updates are allowed when all referencing PodCliqueSets have no scheduled pods. Metadata changes (labels, annotations) are not blocked.

**Mitigation**

* Administrators who need to update a topology's levels can first drain the referencing workloads (scale to zero or delete), apply the topology update, and then redeploy. The webhook provides a clear rejection message identifying which PodCliqueSets are blocking the update.

#### Topology Reference Immutability After Scheduling

The `clusterTopologyName` field on a PodCliqueSet becomes immutable once any pod in the PCS has been scheduled (bound to a node). The scheduler has already made placement decisions based on the referenced topology, and changing the topology reference after scheduling would invalidate those decisions. Users can change `clusterTopologyName` freely while all pods are still pending, supporting the topology retry use case (Story 5).

**Mitigation**

* This restriction only applies after pods are scheduled. Users who need to change the topology reference for a running workload can delete the PodCliqueSet and recreate it with the desired topology. The pending-state flexibility supports the common case of retrying on a different topology when the first choice cannot accommodate the gang.

#### Two Management Paths for Topologies

The default ClusterTopology is managed through the operator's Helm configuration, while additional topologies are created directly by administrators via kubectl or GitOps. The operator-managed default is labeled with `app.kubernetes.io/managed-by: grove-operator` and is reconciled to match the operator configuration on each startup. User-created topologies are not reconciled by the operator and should not use the reserved default topology name.

**Mitigation**

* The two-path model is intentional: the default topology follows the existing Helm-managed lifecycle (matching today's behavior), while additional topologies are standard Kubernetes resources subject to the full admission control and finalizer protections. Documentation will clearly distinguish between operator-managed and user-managed topologies to avoid confusion.

## Design Details

> NOTE: For brevity we will refer to topology aware scheduling as `TAS`.

### Cluster Admin API

Topology levels will be defined by a cluster admin as part of `OperatorConfiguration`

```go
// OperatorConfiguration defines the configuration for the Grove operator.
type OperatorConfiguration struct {
  TopologyAwareScheduling TopologyAwareSchedulingConfiguration `json:"topologyAwareScheduling"`
}

// TopologyAwareSchedulingConfiguration defines the configuration for topology-aware scheduling.
type TopologyAwareSchedulingConfiguration struct {
	// Enabled indicates whether topology-aware scheduling is enabled.
	Enabled bool `json:"enabled"`
	// Levels is a list of topology levels.
	// Used to create/update the ClusterTopology CR at operator startup.
	// +optional
	Levels []corev1alpha1.TopologyLevel `json:"levels,omitempty"`
}

```

Example `OperatorConfiguration` (only shows TAS configuration for brevity):
```yaml
apiVersion: operator.config.grove.io/v1alpha1
kind: OperatorConfiguration
...
topologyAwareScheduling:
  enabled: true
  levels:
    - domain: region
      key: "topology.kubernetes.io/region"
    - domain: zone
      key: "topology.kubernetes.io/zone"
    - domain: datacenter
      key: "topology.kubernetes.io/datacenter"
    - domain: rack
      key: "topology.kubernetes.io/rack"
    - domain: host
      key: "kubernetes.io/hostname"
    ...
```

> NOTE: The above values for `key` is just an example and will/can be different for different infrastructure providers.

#### Supported Topology domains:

Topology domains are sorted by their total network distance in descending order from maximum to minimum.

| Domain       | Description                                         |
| ------------ | --------------------------------------------------- |
| `region`     | Cloud provider region                               |
| `zone`       | Availability zone within a region                   |
| `datacenter` | Physical data center within a zone                  |
| `block`      | Large switching block or network segment            |
| `rack`       | Physical rack containing multiple hosts             |
| `host`       | Individual host (virtual/server)                    |
| `numa`       | NUMA (Non-Uniform Memory Access) node within a host |

A topology domain provides an infrastructure agnostic identifier for a topology level and thus allows the same workload to be deployed across clusters hosted by any cloud provider or private data centers. Across `GCP`, `AWS` and `Azure` it has been observed that the network topology node labels differ. It was thus a natural choice to define a uniform topology convention which can be used by workload designers when creating `PodCliqueSet` resources. Using `ClusterTopology` CR, Grove operator then maps these uniform topology domains to infrastructure provider specific topology node labels.

#### Validation

`OperatorConfiguration` is validated upon starting of `Grove` operator. If `TopologyAwareScheduling.Enabled` is true, then following is checked:

* At least one `TopologyLevel` should be set.
* For each `TopologyLevel`, its `TopologyDomain` should be one of the supported topology domains as mentioned above.
* Each `TopologyLevel` should be unique, neither the domain nor the key should be duplicated.

> NOTE: There is no validation done for `TopologyLevel.Key` (which is a node label) as that can be different across cloud providers and on-prem data centers. The only exception is the node label for `host` since that is set by the `kubelet`.

If any of the validation fails then the operator will exit with a non-zero error code and an appropriate error message which will be visible in the logs of the operator `Pod`.

#### Operator Startup behavior

When `Grove` operator starts, it checks if TAS is enabled.

**TAS is enabled**

* Validate TAS configuration.

* Create or Update the default `ClusterTopology` custom resource (`grove-topology`). The default topology is labeled with `app.kubernetes.io/managed-by: grove-operator` and is reconciled to match the operator configuration on each startup.

* If the active scheduler backend's profile has topology resource creation enabled (`KaiSchedulerConfig.CreateTopologyResources`, which defaults to `true`), create or update the corresponding scheduler backend topology CR for the default `ClusterTopology`. For the KAI scheduler, this means creating a `Topology` CR if it does not exist, or re-creating it (delete + create) if it needs an update, since KAI's `Topology` CR is currently immutable. The scheduler backend topology CR is created with the `ClusterTopology` CR as its owner via `OwnerReference`.

If any of the create/update/delete of `ClusterTopology` CR or scheduler backend topology CR fails, the operator exits with a non-zero exit code and a clear message indicating the issue.

**TAS is disabled**

* Delete any existing default `ClusterTopology` CR and handle removing finalizers on the default topology. Because the scheduler backend topology CR is owned by the `ClusterTopology` CR (via `OwnerReference`), it will be cascade-deleted.

Deletion failures are non-fatal. These will be logged and the operator will continue.

**User-created ClusterTopology resources** are not created, updated, or deleted by operator startup. Their lifecycle is managed entirely by administrators via kubectl or GitOps, subject to the protections described in [ClusterTopology Lifecycle](#clustertopology-lifecycle). When the active scheduler backend's profile has topology resource creation enabled (`KaiSchedulerConfig.CreateTopologyResources`, which defaults to `true`), the ClusterTopology controller creates and manages the corresponding scheduler backend topology CR for each user-created ClusterTopology as well, using the same `OwnerReference` pattern.

#### Topology Configuration Updates

`OperatorConfiguration` is mounted as an immutable `ConfigMap` to the operator. To make any changes to the default topology's TAS configuration via `OperatorConfiguration`, the `Grove` operator needs to be restarted with the changed `OperatorConfiguration`.

User-created ClusterTopology resources can be updated directly via kubectl or GitOps, subject to the protections described in [ClusterTopology Lifecycle](#clustertopology-lifecycle).

### ClusterTopology custom resource

`ClusterTopology` is a custom resource that defines an ordered list of topology levels from largest to smallest network distance. Each `TopologyLevel` is a pair of topology domain and a node label key specific for the infrastructure provider.

ClusterTopology Go API:

```go
// TopologyDomain represents a predefined topology level in the hierarchy.
type TopologyDomain string

const (
    TopologyDomainRegion     TopologyDomain = "region"
    TopologyDomainZone       TopologyDomain = "zone"
    TopologyDomainDataCenter TopologyDomain = "datacenter"
    TopologyDomainBlock      TopologyDomain = "block"
    TopologyDomainRack       TopologyDomain = "rack"
    TopologyDomainHost       TopologyDomain = "host"
    TopologyDomainNuma       TopologyDomain = "numa"
)

// ClusterTopologySpec defines the topology hierarchy specification.
type ClusterTopologySpec struct {
    // Levels is an ordered list of topology levels.
    // The order in this list defines the hierarchy (index 0 = highest level with maximum total network distance).
    // This field is immutable after creation.
    // +kubebuilder:validation:MinItems=1
    // +kubebuilder:validation:MaxItems=7
    // +kubebuilder:validation:XValidation:rule="self.all(x, self.filter(y, y.domain == x.domain).size() == 1)",message="domain must be unique across all levels"
    // +kubebuilder:validation:XValidation:rule="self.all(x, self.filter(y, y.key == x.key).size() == 1)",message="key must be unique across all levels"
    Levels []TopologyLevel `json:"levels"`
}

type TopologyLevel struct {
    // Domain is the predefined level identifier used in TopologyConstraint references.
    // Must be one of: region, zone, datacenter, block, rack, host, numa
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:Enum=region;zone;datacenter;block;rack;host;numa
    Domain TopologyDomain `json:"domain"`

    // Key is the node label key that identifies this topology domain.
    // Must be a valid Kubernetes label key (qualified name).
    // Examples: "topology.kubernetes.io/zone", "kubernetes.io/hostname"
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MinLength=1
    // +kubebuilder:validation:MaxLength=63
    // +kubebuilder:validation:Pattern=`^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]/)?([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]$`
    Key string `json:"key"`
}
```

#### ClusterTopology Lifecycle

ClusterTopology resources are cluster-scoped and define the mapping between topology domain names and infrastructure-specific node labels. Their lifecycle is managed through two paths depending on how they are created.

**Creation and Ownership**

* The operator creates and owns the default ClusterTopology (`grove-topology`) from the `OperatorConfiguration.TopologyAwareScheduling` settings at startup. It is labeled with `app.kubernetes.io/managed-by: grove-operator`. Manual changes to the default topology are overwritten on operator restart.
* Administrators can create additional ClusterTopology resources directly via kubectl or GitOps. These are not reconciled by the operator and their lifecycle is managed entirely by the administrator.

**Updates**

Updates to a ClusterTopology's topology levels are validated by a webhook. The webhook blocks level updates if any PodCliqueSet with scheduled pods references the topology, preventing changes that would invalidate in-flight scheduling decisions. Metadata changes (labels, annotations) are not blocked. The default topology's levels are updated via `OperatorConfiguration` changes followed by an operator restart, which bypasses the webhook (see [Topology Configuration Drift](#topology-configuration-drift)).

**Deletion**

A controller watches ClusterTopology resources and adds a finalizer to each one. The finalizer prevents deletion while any PodCliqueSet references the topology via `clusterTopologyName`. The finalizer is removed once no PodCliqueSet references the topology, at which point deletion proceeds. When TAS is disabled, the operator handles removing finalizers on the default topology so it can be cleaned up.

```mermaid
sequenceDiagram
    participant Op as Operator
    participant Admin
    participant API as API Server
    participant CTW as CT Webhook
    participant CTC as CT Controller

    Note over Op, CTC: Create ClusterTopology

    rect rgb(230, 245, 230)
        Note left of Op: Startup
        Op->>API: Create grove-topology<br/>with managed-by label
        API-->>Op: Created
    end

    rect rgb(230, 235, 245)
        Note left of Admin: kubectl / GitOps
        Admin->>API: Create ClusterTopology
        API-->>Admin: Created
    end

    CTC->>API: Add finalizer

    Note over Op, CTC: Update ClusterTopology Levels

    Admin->>API: Update CT levels
    API->>CTW: Validate update
    CTW->>API: List PCSs referencing this CT
    alt Any PCS has ScheduledReplicas > 0
        CTW-->>Admin: Reject (topology in use by scheduled workloads)
    else No PCS has scheduled pods
        CTW-->>Admin: Allow
    end

    Note over Op, CTC: Delete ClusterTopology

    Admin->>API: Delete CT
    Note right of API: Finalizer prevents immediate deletion
    CTC->>API: Check if any PCS references this CT
    alt PCSs still reference CT
        Note right of CTC: Finalizer remains, deletion pending
    else No PCS references CT
        CTC->>API: Remove finalizer
        Note right of API: CT deleted
    end
```

### Topology Constraints in PodCliqueSet

`PodCliqueSet` API has been enhanced, allowing users to specify topology constraints. A new type `TopologyConstraint` has been introduced which allows users to define `required` topology constraints that must be satisfied for the scheduling to succeed.

```go
// TopologyConstraint defines topology placement requirements.
type TopologyConstraint struct {
	// PackDomain specifies the topology domain for grouping replicas.
	// Controls placement constraint for EACH individual replica instance.
	// Must be one of: region, zone, datacenter, block, rack, host, numa
	// Example: "rack" means each replica independently placed within one rack.
	// Note: Does NOT constrain all replicas to the same rack together.
	// Different replicas can be in different topology domains.
	// +kubebuilder:validation:Enum=region;zone;datacenter;block;rack;host;numa
	PackDomain TopologyDomain `json:"packDomain"`
}
```

`TopologyConstraint` can be specified at three levels:

At `PodCliqueSet` you can set the constraints using:

```go
// PodCliqueSetTemplateSpec defines a template spec for a PodGang.
// A PodGang does not have a RestartPolicy field because the restart policy is predefined:
// If the number of pods in any of the cliques falls below the threshold, the entire PodGang will be restarted.
// The threshold is determined by either:
// - The value of "MinReplicas", if specified in the ScaleConfig of that clique, or
// - The "Replicas" value of that clique
type PodCliqueSetTemplateSpec struct {
  ...
  	// TopologyConstraint defines topology placement requirements for PodCliqueSet.
	// +optional
	TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`
  ...
}
```

At `PodCliqueScalingGroup` level you can set the topology constraint using:

```go
// PodCliqueScalingGroupConfig is a group of PodClique's that are scaled together.
// Each member PodClique.Replicas will be computed as a product of PodCliqueScalingGroupConfig.Replicas and PodCliqueTemplateSpec.Spec.Replicas.
// NOTE: If a PodCliqueScalingGroupConfig is defined, then for the member PodClique's, individual AutoScalingConfig cannot be defined.
type PodCliqueScalingGroupConfig struct {
  ...
  // TopologyConstraint defines topology placement requirements for PodCliqueScalingGroup.
	// Must be equal to or stricter than parent PodCliqueSet constraints.
	// +optional
	TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`
  ...
}
```

`TopologyConstraint` defined at the `PodCliqueScalingGroup` level should be:

* Equal to or lower than the one that is defined at `PodCliqueSet` level.
* Equal to or higher than the constraints defined for each constituent `PodClique`.

At `PodClique` level you can set the topology constraint using:

```go
// PodCliqueTemplateSpec defines a template spec for a PodClique.
type PodCliqueTemplateSpec struct {
  ...
  // TopologyConstraint defines topology placement requirements for PodClique.
	// Must be equal to or stricter than parent resource constraints.
	// +optional
	TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`
  ...
}
```

`TopologyConstraint` defined at the `PodClique` level should be lower than or equal to the ones defined for the parent resources a.k.a (`PodCliqueScalingGroup` and `PodCliqueSet`).

Example PodCliqueSet with topology constraints (For brevity many parts of the PodCliqueSet spec has been omitted):

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: disaggregated-inference
spec:
  replicas: 1
  template:
    topologyConstraint:
      packDomain: "zone"
    cliques:
      - name: router
        topologyConstraint:
          packDomain: "block"
        spec:
          roleName: router
          replicas: 1
          podSpec:
            ...
      - name: p-leader
        topologyConstraint:
          packDomain: "rack"
        spec:
          roleName: prefill-leader
          replicas: 1
          podSpec:
            ...
      - name: p-worker
        topologyConstraint:
          packDomain: "rack"
        spec:
          roleName: prefill-worker
          replicas: 4
          podSpec:
            ...
      - name: d-leader
        topologyConstraint:
          packDomain: "rack"
        spec:
          roleName: decode-leader
          replicas: 1
          podSpec:
            ...
      - name: d-worker
        topologyConstraint:
          packDomain: "rack"
        spec:
          roleName: decode-worker
          replicas: 2
          podSpec:
            ...
    podCliqueScalingGroups:
      - name: prefill
        topologyConstraint:
          packDomain: "block"
        replicas: 2
        minAvailable: 1
        cliqueNames:
          - p-worker
          - p-leader
      - name: decode
        topologyConstraint:
          packDomain: "rack"
        replicas: 2
        minAvailable: 1
        cliqueNames:
          - d-worker
          - d-leader
```

The above example is only a representation of how users can set topology constraints at different levels to control how the pods are going to be packed during the initial deployment.

#### ClusterTopology Reference

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
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: my-inference
spec:
  template:
    clusterTopologyName: gb200-topology    # references a ClusterTopology; if omitted, uses grove-topology
    topologyConstraint:
      packDomain: rack
    cliques:
      - name: worker
        # ...
```

#### Validation

Existing validating webhook which validates `PodCliqueSet`, has been enhanced to additionally check topology constraints.

*Rule-1: Check for supported TopologyDomains*

* All topology domains that are referenced in the `PodCliqueSet` must be amongst the defined topology levels in the referenced ClusterTopology CR (or the default `grove-topology` if `clusterTopologyName` is not set). If a non-supported topology domain is found then creation of the `PodCliqueSet` will be rejected.
* Topology domains for an already deployed `PodCliqueSet` cannot be changed. Validating webhook will reject such updates on the `PodCliqueSet`.

*Rule-2: Check for Hierarchical strictness*

As you traverse down the resource hierarchy (PodCliqueSet → PodCliqueScalingGroup → PodClique), topology constraint levels must become equal or lower. A child resource cannot specify a broader topology domain than its parent. If this rule is violated, then the creation of the `PodCliqueSet` will be rejected by the validating webhook.

Example:

| Parent | Child   | Valid? | Reason                                                       |
| ------ | ------- | ------ | ------------------------------------------------------------ |
| `rack` | `host`  | ✅ Yes  | `host` is lower than `rack`                    |
| `rack` | `rack`  | ✅ Yes  | Equal is allowed                                             |
| `rack` | `numa`  | ✅ Yes  | `numa` is lowest                                          |
| `host` | `rack`  | ❌ No   | `rack` is higher than `host`                                |
| `zone` | `block` | ✅ Yes   | `zone` is higher than `block` |

*Rule-3: ClusterTopology reference validation*

* On create: if `clusterTopologyName` is set, the referenced ClusterTopology must exist
* On update: if `clusterTopologyName` is changed, the new ClusterTopology must exist and no pod in the PCS may be scheduled (`ScheduledReplicas == 0` across all PodCliques)
* Reject if `clusterTopologyName` is set but no `TopologyConstraint` is specified on the PCS, any PCSG, or any PodClique
* Reject if `clusterTopologyName` is set but TAS is disabled cluster-wide
* Reject if any `TopologyConstraint` is set (at PCS, PCSG, or PodClique level) but TAS is disabled cluster-wide

Rules 1 and 2 apply to `TopologyConstraint` fields. Rule-3 validates the `clusterTopologyName` reference. Together, these three rules ensure that workloads can only reference valid topology levels, maintain logical topology nesting throughout the resource hierarchy, and target an existing ClusterTopology.

### PodGang: Scheduler API Enhancements

Grove operator translates the hierarchical topology constraints to infrastructure specific node labels in the `PodGang` scheduler API. The operator resolves the topology as follows:
* PCS has `TopologyConstraint` set but no `clusterTopologyName` → use `grove-topology` (default)
* PCS has no `TopologyConstraint` at any level → topology does not apply
* PCS has an explicit `clusterTopologyName` → use that topology

The following additional types have been defined to capture the topology constraints. Provision has been made to capture:

* `Required` topology constraints which are hard requirements for the scheduler to consider.  These constraints are guaranteed to be satisfied.
* `Preferred` topology constraints are soft requirements and often point to the best possible packing that can be achieved.

```go
// TopologyConstraint defines topology packing constraints with required and preferred levels.
type TopologyConstraint struct {
	// PackConstraint defines topology packing constraint with required and preferred levels.
	// Operator translates user's level name to corresponding topologyKeys.
	// +optional
	PackConstraint *TopologyPackConstraint `json:"packConstraint,omitempty"`
}

// TopologyPackConstraint defines a topology packing constraint.
// Each of Required and Preferred fields hold a topologyKey, e.g. "kubernetes.io/hostname" ( these are key of labels added on nodes).
type TopologyPackConstraint struct {
	// Required defines a topology constraint that must be satisfied as a hard requirement. The workload will not be
	// scheduled if this constraint cannot be satisfied. Generally, it is easier for the scheduler to satisfy constraints
	// on topology domains with larger compute capacity, (e.g. zone or datacenter), than smaller domains, (e.g. host or
	// numa). Holds topologyKey (not level name) translated from user's packLevel specification.
	// Example: "topology.kubernetes.io/rack"
	// +optional
	Required *string `json:"required,omitempty"`
	// Preferred defines best-effort topology constraint. Topology domains that provide the most optimized performance
	// with dense packing (e.g. host or numa) are typically used as preferred constraints for topology packing. It might be
	// harder to satisfy these constraints if the topology domains are limited in compute  capacity. Since it is preferred
	// constraint, it is therefore not binding on the scheduler to mandatorily satisfy this packing constraint. Scheduler
	// can fall back to higher topology levels (upto Required constraint) if preferred cannot be satisfied.
	// Example: "kubernetes.io/hostname"
	// +optional
	Preferred *string `json:"preferred,omitempty"`
}
```

`TopologyConstraint`s can be defined at multiple levels:

**PodGangSpec.TopologyConstraint**

This is the top level constraint defined at the `PodGang` level that applies to all the `PodGroup`s in the `PodGang`.  We have two variants of `PodGang`s:

* `PodGang` that comprises of minimum number of replicas across all standalone `PodClique` and `PodCliqueScalingGroup`s that together make a workload functional. At present we also name this as the `base` PodGang. For the base PodGang, `TopologyConstraint` is the value of `PodCliqueSetTemplateSpec.TopologyConstraint` (`spec.template.topologyConstraint`)
* `PodGang`that is created for every replica of `PodCliqueScalingGroup` above the `minAvailable` as specified in `spec.template.podCliqueScalingGroups[x].minAvailable`. At present we call these as `scaled` PodGang. For the scaled PodGang, `TopologyConstraint` is the value of `PodCliqueScalingGroupConfig.TopologyConstraint` (`spec.template.podCliqueScalingGroups[x].topologyConstraint`).
  * In case there is no topology constraint defined at `spec.template.podCliqueScalingGroups[x].topologyConstraint` then it will inherit the topology constraint  defined at `spec.template.topologyConstraint` if defined.


**PodGangSpec.TopologyConstraintGroupConfigs**

Users can define topology constraints that are applied for all constituent `PodClique`s in a `PodCliqueScalingGroup` (`spec.template.podCliqueScalingGroups[x].topologyConstraint`).

> NOTE: This field is used only for `Base` PodGangs. For `Scaled` PodGangs this will be empty.


**PodGroup.TopologyConstraint**

Users can define topology constraints at the `PodClique` level (`spec.template.cliques[x].topologyConstraint`) This will be captured in `PodGroup.TopologyConstraint`.

```go
// PodGangSpec defines the specification of a PodGang.
type PodGangSpec struct {
	// PodGroups is a list of member pod groups in the PodGang.
	PodGroups []PodGroup `json:"podgroups"`
	// TopologyConstraint defines topology packing constraints for entire pod gang.
	// This is the top level topology constraint that applies to all PodGroups in the PodGang.
	// Updated by operator on each reconciliation when PodCliqueSet topology constraints change.
	// +optional
	TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`
	// TopologyConstraintGroupConfigs defines TopologyConstraints for a strict subset of PodGroups.
	// +optional
	TopologyConstraintGroupConfigs []TopologyConstraintGroupConfig `json:"topologyConstraintGroupConfigs,omitempty"`
  ...
}

// PodGroup defines a set of pods in a PodGang that share the same PodTemplateSpec.
type PodGroup struct {
	// Name is the name of the PodGroup.
	Name string `json:"name"`
  ...
	// TopologyConstraint defines topology packing constraints for this PodGroup.
	// Enables PodClique-level topology constraints.
	// Updated by operator when PodClique topology constraints change.
	// +optional
	TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`
}

```

### Backward Compatibility

The addition of multiple ClusterTopology resources and the `clusterTopologyName` field introduces new capabilities without changing existing behavior. Migration is zero-effort for existing users:

* The operator continues to create `grove-topology` from OperatorConfiguration.
* Existing PodCliqueSets without `clusterTopologyName` use `grove-topology` by default.
* No changes are required to existing workload specifications.
* Multi-topology support is purely additive.

### Monitoring

**PodCliqueSet Status Conditions**

It is possible that one or more topology constraints defined on a deployed `PodCliqueSet` are no longer available because the cluster admin decided to make changes to the `ClusterTopology`. It is therefore important to create visibility that one or more topology levels are no longer available. A new `metav1.Condition` has been introduced for `PodCliqueSet`.

```go
// PodCliqueSetStatus defines the status of a PodCliqueSet.
type PodCliqueSetStatus struct {
  ...
	// Conditions represents the latest available observations of the PodCliqueSet by its controller.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	...
}

```

Condition: `TopologyLevelsUnavailable`
Condition States:

| Status    | Reason                              | Description                                                  |
| --------- | ----------------------------------- | ------------------------------------------------------------ |
| `Unknown` | `ClusterTopologyNotFound`           | When `ClusterTopology` CR is no longer existing             |
| `True`    | `ClusterTopologyLevelsUnavailable`  | When one or more topology levels used by a deployed `PodCliqueSet` are no longer present in `ClusterTopology` |
| `False`   | `AllClusterTopologyLevelsAvailable` | All topology levels used by a deployed `PodCliqueSet` are amongst the supported topology levels as defined in `ClusterTopology` |

**Topology usage overview**

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
gb200-topology           ml-team/inference-llama-405b
gb200-topology           ml-team/inference-mixtral
grove-topology (default) default/simple-inference
grove-topology (default) ml-team/inference-llama-70b
```

### Dependencies

**Scheduler backend with Topology Aware Scheduling Support**

Currently the only scheduler backend that supports hierarchical TAS is [KAI Scheduler](https://github.com/NVIDIA/KAI-Scheduler). See [here](https://github.com/NVIDIA/KAI-Scheduler/tree/main/docs/topology) for more information.
Follow [instructions](https://github.com/NVIDIA/KAI-Scheduler?tab=readme-ov-file#installation) to install KAI scheduler. When the KAI scheduler profile is enabled (see [OperatorConfiguration Extension](../375-scheduler-backend-framework/README.md#operatorconfiguration-extension)), Grove automatically creates and manages a KAI `Topology` CR for each ClusterTopology in the cluster (both the default and user-created topologies). This is controlled by `KaiSchedulerConfig.CreateTopologyResources`, which defaults to `true`. KAI Scheduler supports multiple `Topology` resources within a single cluster.

> NOTE: The scheduling backend determines what resources it requires. Grove Operator is not limited to one scheduler backend, and any other scheduler providing TAS functionality can be plugged in. Each backend defines its own configuration struct within `SchedulerProfile.Config`, and topology resource creation is controlled by backend-specific configuration fields (e.g. `KaiSchedulerConfig.CreateTopologyResources` for KAI).

**Nodes labeled with Topology specific labels**

To enable the scheduler to select/filter nodes that satisfy the topology constraints defined for a `PodCliqueSet` it is essential that the topology specific labels are set on kubernetes `Node` objects.

### Test Plan

**Unit tests** for TAS has been implemented in the following packages:

* `OperatorConfiguration` validation tests are present at `operator/api/config/validation/validation_test.go`
* Core API helper function tests are included at `operator/api/core/v1alpha1/clustertopology_test.go`
* Validating webhook specific validation tests are present at `operator/internal/webhook/admission/pcs/validation/topologyconstraints_test.go`
* Reconciler specific tests that inspect `PodCliqueSet` topology constraints and update the `PodGang` resource are present at `operator/internal/controller/podcliqueset/components/podgang/syncflow_test.go`

**Unit tests** for multi-topology:

* PCS validating webhook: `clusterTopologyName` existence check on create and update, immutability after scheduling, rejection when TAS is disabled or no `TopologyConstraint` is set
* ClusterTopology validating webhook: block updates when referenced by PCSs with scheduled pods
* ClusterTopology controller: finalizer addition and removal logic
* PCS reconciler: topology resolution logic that resolves `clusterTopologyName` (or the default) to the correct ClusterTopology when building the PodGang

**E2E tests** are defined in [Issue#305](https://github.com/ai-dynamo/grove/issues/305).

**E2E tests** for multi-topology:

* Extend existing TAS e2e tests to include a multi-topology case: relabel a subset of worker nodes with different topology label keys, create a second ClusterTopology resource referencing those keys, deploy a PCS with `clusterTopologyName` pointing to the new topology, and verify that pods are placed correctly and the KAI PodGroup references the expected topology

## Alternatives

An alternative was discussed to allow cluster admin to externally create `ClusterTopology` CR and provide a controller in Grove whose responsibility would be to reconcile creates/updates/deletes on externally managed `ClusterTopology` resource. Grove operator can then be started by specifying the name  of the `ClusterTopology` CR.

In the initial iteration, we wanted to take complete control over the lifecycle of the `ClusterTopology` CR and also reduce the effort for a cluster admin to manage such resource(s). In future we will auto-detect cluster topology via tools similar to [Topograph](https://github.com/NVIDIA/topograph) and extend it to also automatically create `ClusterTopology` CR.

### Fully Operator-Managed Topologies

An alternative is to define all topologies in the OperatorConfiguration ConfigMap rather than allowing administrators to create ClusterTopology resources directly. The operator would reconcile all topologies from its configuration at startup, extending the current single-topology model to a list.

This approach offers a single source of truth and a simpler ownership model — the operator fully manages all topologies with no hybrid management paths.

However, because the ConfigMap is managed by the Helm chart, it operates outside of the webhook and finalizer safeguards. A `helm upgrade` that removes a topology from the config would cause the operator to delete the corresponding ClusterTopology on next startup, even if PodCliqueSets still reference it. The finalizer would prevent the deletion, creating a stuck state where the config and the cluster are not aligned. Similarly, a Helm-driven update to a topology's levels would conflict with the protection against modifying topologies referenced by scheduled workloads. Additionally, topology management is coupled to the operator lifecycle — adding a topology for a new hardware segment requires redeploying the operator.

With the hybrid model chosen in this proposal, only the default topology has this Helm-managed lifecycle, which is acceptable since it matches today's behavior. Additional topologies are standard Kubernetes resources subject to the full admission control and finalizer protections, and can be managed independently by different teams.

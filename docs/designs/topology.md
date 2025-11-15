# Topology-Aware Scheduling - Grove Operator Design

## Overview

This document defines the design for supporting topology-aware scheduling in the Grove operator.

**Motivation**: Topology-aware scheduling is critical for Grove's multi-node inference workloads because these
applications require:

- **Network Locality**: Proximity improves high-bandwidth communication between leaders and their respective workers
- **Coordinated Placement**: Related components (e.g., model shards) perform better when co-located within the same
  topology domain
- **Latency Optimization**: Minimizing network hops between interdependent inference components improves end-to-end
  performance

## Goals

- Provide flexible, cluster-agnostic topology hierarchy definition via ClusterTopology CRD
- Enable packing constraints for network locality across all Grove scalable resources
- Immutable topology configuration ensuring scheduling consistency
- Hierarchical constraint validation (child stricter than parent)

## Non-Goals

- Spread constraints across topology domains (ReplicaSpreadDomain)
- Root domain constraints for entire resource (RootDomain)
- Ratio-based affinity groups between scaling groups (AffinityGroups with PackRatio)
- Dynamic topology reconfiguration after creation
- Automatic suggest topology according to workload characteristics

## Proposal

Grove implements topology-aware scheduling through a ClusterTopology CRD,
operator configuration to enable/disable features, and user-specified TopologyConstraints in workloads.
The operator automatically generates preferred constraints (lower bound) for optimization
while allowing users to specify required constraints for strict placement (upper bound).

## Design Details

### Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────┐
│                        Topology Architecture                            │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  Admin Layer:                                                           │
│  ┌──────────────────────┐          ┌──────────────────────┐             │
│  │ ClusterTopology      │          │ Kueue Topology       │             │
│  │ "grove-topology"     │          │ "grove-topology"     │             │
│  │                      │          │ (manual creation)    │             │
│  └──────────┬───────────┘          └──────────-─┬─────────┘             │
│             │                                   │                       │
│             │                                   │                       │
│  Operator Config: clusterTopology.enabled=true  │                       |                       |
│             │                                   │                       │
│             │ (validates against)               │ (referenced by)       │
├─────────────┼───────────────────────────────────┼───────────────────────┤
│             │                                   │                       │
│  User Layer:                                    │                       │
│             ▼                                   │                       │
│  ┌──────────────────┐              ┌────────────────────┐               │
│  │ PodCliqueSet     │─────────────▶│ Grove Operator     │               │
│  │ (packDomain)     │              │ (reconciles)       │               │
│  └──────────────────┘              └─────────┬──────────┘               │
│                                              │                          │
│                                              │ (translates)             │
│                                              ▼                          │
│                                    ┌────────────────────┐               │
│                                    │ PodGang            │───────▶ KAI   │
│                                    │ • Annotation:      │     Scheduler │
│                                    │   topology-name    │               │
│                                    │ • 3-level topology │               │
│                                    │   (required+       │               │
│                                    │    preferred)      │               │
│                                    └────────────────────┘               │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

### 1. ClusterTopology Infrastructure

#### ClusterTopology CR

ClusterTopology is a cluster-scoped CR that defines consistent naming for cluster topology hierarchy to be used by
workload designers. It maps topology level domains to Kubernetes node labels and establishes ordering from broadest to
narrowest scope.

**Characteristics:**

- **Cluster-scoped resource**: Can have multiple ClusterTopology resources defined
- **Default name**: In operator configuration, clusterTopologyName defaults to "grove-topology" when not specified
- **Partially immutable**: After creation, only `key` field values can be updated; `domain` fields and level ordering
  are immutable
- **List-ordered hierarchy**: Index 0 represents the broadest category (e.g., region), and the final index represents the narrowest (e.g., host).
- **Supported topology levels**: Region > Zone > DataCenter > Block > Rack > Host > Numa (broadest to narrowest)
- **Webhook-validated**: Webhook validates constraint hierarchy and immutability

**TopologyDomain Definitions:**

- **Region**: Network local to a CSP region
- **Zone**: Network local to a CSP availability-zone within a region
- **DataCenter**: Network local to a data-center within a CSP availability-zone
- **Block**: Network local to a switching block unit within a data-center
- **Rack**: First-level network grouping of compute hosts (includes NVLink domains as logical racks)
- **Host**: Individual compute host
- **Numa**: NUMA node (processor and memory locality domain) within a compute host

**API Structure:**

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

// Topology ordering (broadest to narrowest):
// Region > Zone > DataCenter > Block > Rack > Host > Numa

// ClusterTopology defines the topology hierarchy for the cluster.
// This resource is immutable after creation.
type ClusterTopology struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec ClusterTopologySpec `json:"spec,omitempty"`
}

type ClusterTopologySpec struct {
    // Levels is an ordered list of topology levels from broadest to narrowest scope.
    // The order in this list defines the hierarchy (index 0 = highest level).
    // This field is immutable after creation.
    // +kubebuilder:validation:XValidation:rule="self == oldSelf",message="levels list is immutable"
    // +kubebuilder:validation:MinItems=1
    // +kubebuilder:validation:MaxItems=8
    Levels []TopologyLevel `json:"levels"`
}

type TopologyLevel struct {
    // Domain is the predefined level identifier used in TopologyConstraint references.
    // Must be one of: region, zone, datacenter, block, rack, host, numa.
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:Enum=region;zone;datacenter;block;rack;host;numa
    Domain TopologyDomain `json:"domain"`

    // Key is the node label key that identifies this topology domain.
    // Must be a valid Kubernetes label key (qualified name).
    // Examples: "topology.kubernetes.io/zone", "kubernetes.io/hostname".
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MinLength=1
    // +kubebuilder:validation:MaxLength=64
    Key string `json:"key"`
}
```

**Example ClusterTopology:**

```yaml
apiVersion: grove.io/v1alpha1
kind: ClusterTopology
metadata:
   name: my-cluster-topology  # User chooses name
spec:
  levels:
    - domain: region
      key: "topology.kubernetes.io/region"
    - domain: zone
      key: "topology.kubernetes.io/zone"
    - domain: datacenter
      key: "topology.kubernetes.io/datacenter"
    - domain: block
      key: "topology.kubernetes.io/block"
    - domain: rack
      key: "topology.kubernetes.io/rack"
    - domain: host
      key: "kubernetes.io/hostname"
    - domain: numa
      key: "topology.kubernetes.io/numa"
```

**Creating ClusterTopology:**

1. Customize example above with your cluster's actual `key` values
2. Choose a name for your topology:
   - Use custom name (e.g., "my-cluster-topology") OR
   - Use default name "grove-topology" (no config needed)
3. Create resource: `kubectl apply -f clustertopology.yaml`
4. If using custom name: configure operator with topology name in operator config
5. See Admin Responsibilities section for additional setup requirements

**Validation:**

- Level names must be from predefined set: region, zone, datacenter, block, rack, host, numa (enum validation)
- Each level `domain` and `key` must be unique
- Admins can skip intermediate levels (e.g., define only region, rack, host)
- Partially immutable after creation (see Characteristics above for details)
- Deletion protection via controller finalizer (blocks deletion while PodCliqueSet resources reference this topology OR
  topology is enabled)

**Mutation Webhook:**

The mutation webhook automatically modifies ClusterTopology resources on CREATE operations:

- **Level Reordering**: Automatically reorders levels to match predefined ordering (Region > Zone > DataCenter > Block >
  Rack > Host > Numa)
- **Subset Support**: Admins can define any subset of levels; webhook reorders only the levels provided
- **Consistency**: Ensures all ClusterTopology resources follow the same ordering convention across the cluster
- **Example**: If admin defines levels as `[host, rack, region]`, webhook reorders to `[region, rack, host]`

#### ClusterTopology Controller

The ClusterTopology controller manages the ClusterTopology resource lifecycle:

**Deletion Protection**

Prevents ClusterTopology deletion while in use, requiring both conditions for deletion using Kubernetes finalizer.

Deletion Workflow:

1. Admin runs `kubectl delete clustertopology <name>`
2. Kubernetes blocks deletion (finalizer `grove.io/clustertopology` present)
3. Controller reconciles:
    - Detects deletion request (deletion timestamp set)
   - Checks if any PodCliqueSet (in any namespace) references this ClusterTopology (via `grove.io/cluster-topology-name` label)
   - Checks if topology is enabled in operator config (`clusterTopology.enabled: true`)
   - If ANY PodCliqueSet references this topology OR topology is enabled: Keeps finalizer, deletion blocked
   - If NO PodCliqueSet references this topology AND topology is disabled: Removes finalizer, deletion proceeds
4. Once finalizer removed, Kubernetes deletes ClusterTopology

Key Points:

- Admin must satisfy BOTH conditions before deletion:
    - Delete all PodCliqueSet resources that reference this ClusterTopology (check `grove.io/cluster-topology-name` label)
    - Disable TAS in operator config (`clusterTopology.enabled: false`) and restart operator
- Controller checks both conditions before allowing deletion
- Controller continuously reconciles deletion requests
- Prevents orphaned workloads with invalid topology configuration
- Prevents accidental deletion of active topology configurations

#### Operator Configuration

Operator enables/disables topology features via operator config:

```yaml
topology:
  enabled: true
  name: "my-cluster-topology"  # Optional, defaults to "grove-topology"
```

**Startup Behavior:**

- Topology configuration loaded only at operator startup
- Changes to `clusterTopology.enabled` or `name` require operator restart to take effect
- If `clusterTopology.enabled: true`:
    - `name` not specified → defaults to "grove-topology"
  - Operator looks for ClusterTopology with configured name (defaults to "grove-topology")
  - If ClusterTopology with that name doesn't exist → operator exits with ENOENT and crashloops
- If `clusterTopology.enabled: false`: topology features disabled
- Admin must create ClusterTopology with matching name OR disable topology in operator config

**Admin Responsibilities:**

- Manually create Kueue Topology with same name as Grove ClusterTopology for KAI scheduler
- Ensure topology levels align between Grove ClusterTopology and Kueue Topology

#### Enable/Disable Behavior

**Enabling Topology (clusterTopology.enabled: false → true):**

1. Admin creates ClusterTopology CR with desired topology hierarchy
2. Admin updates operator config: `clusterTopology.enabled: true`
3. Admin restarts operator (see Startup Behavior section for details)
4. Operator validates ClusterTopology CR exists
5. For existing workloads:
    - Workloads with topology constraints: operator uses topology from PodCliqueSet label (`grove.io/cluster-topology-name`)
    - Workloads without topology constraints: no impact (checked via label presence)
6. For new workloads:
    - Topology constraints validated against ClusterTopology CR
   - Mutation webhook adds `grove.io/cluster-topology-name` label to PodCliqueSet (see Mutation Webhook section)

**Disabling Topology (clusterTopology.enabled: true → false):**

1. Admin updates operator config: `clusterTopology.enabled: false`
2. Admin restarts operator
3. For existing workloads:
    - Workloads with topology constraints: all topology constraints removed from PodGang, status updated
    - Workloads without topology constraints: no impact
4. For new workloads:
    - Workloads with topology constraints: validation webhook rejects with error "topology support is not enabled in the
      operator"
    - Workloads without topology constraints: no impact

**Updating ClusterTopology CR:**

1. Admin disables topology per above workflow
2. Restart operator
3. Admin edits ClusterTopology CR (only `key` field values can be changed; `domain` fields immutable)
4. Admin enables topology per above workflow
5. Restart operator
6. Operator reconciles existing workloads with updated topology configuration

*note: in the future, we may support dynamic updates to ClusterTopology without disabling topology first.*

### 2. Operator API Changes (Grove CRDs)

#### TopologyConstraint Model

```go
type TopologyConstraint struct {
    // PackDomain specifies the topology level name for grouping replicas
    // Controls placement constraint for EACH individual replica instance
    // Must be one of: region, zone, datacenter, block, rack, host, numa
    // Example: "rack" means each replica independently placed within one rack
    // Note: Does NOT constrain all replicas to the same rack together
    // Different replicas can be in different topology domains
    // +kubebuilder:validation:Enum=region;zone;datacenter;block;rack;host;numa
    PackDomain *TopologyDomain `json:"packDomain,omitempty"`
}
```

#### Fields Removed from Current API

**From PodCliqueSetSpec:**

- `ReplicaSpreadConstraints []corev1.TopologySpreadConstraint` - Removed (spread not supported)

**From PodCliqueSetTemplateSpec:**

- `SchedulingPolicyConfig *SchedulingPolicyConfig` - Removed (replaced by TopologyConstraint)

**Types Removed:**

- `SchedulingPolicyConfig` struct - Removed entirely
- `NetworkPackGroupConfig` struct - Removed entirely

#### PodCliqueSet CRD Extensions

```go
type PodCliqueSetTemplateSpec struct {
    // ... existing fields ...

    // TopologyConstraint defines topology placement requirements for PodCliqueSet.
    // +optional
    TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`
}
```

#### PodCliqueScalingGroup CRD Extensions

```go
type PodCliqueScalingGroupConfig struct {
    // ... existing fields ...

    // TopologyConstraint defines topology placement requirements for PodCliqueScalingGroup.
    // Must be equal to or stricter than parent PodCliqueSet constraints.
    // +optional
    TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`
}
```

#### PodClique CRD Extensions

```go
type PodCliqueTemplateSpec struct {
    // ... existing fields ...

    // TopologyConstraint defines topology placement requirements for PodClique.
    // Must be equal to or stricter than parent resource constraints.
    // +optional
    TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`
}
```

#### Mutation Webhook

The mutation webhook automatically modifies PodCliqueSet resources on CREATE operations:

- **Label Addition**: Automatically adds `grove.io/cluster-topology-name` label to PodCliqueSet
- **Label Value**: Set to the configured ClusterTopology name from operator config (e.g., "grove-topology")
- **Condition**: Label only added when `clusterTopology.enabled: true` in operator configuration
- **Purpose**: Enables workload-to-topology mapping for controller lifecycle management and deletion protection
- **Immutability**: Once set, label cannot be changed (enforced by validation webhook)
- **No Topology**: If topology disabled (`clusterTopology.enabled: false`), no label is added

#### Validation Webhook

**Hierarchy Constraints:**

- Child PackDomain must be equal to or stricter than parent (stricter = higher index in levels list)
- PodCliqueSet → PodCliqueScalingGroup → PodClique hierarchy
- Referenced PackDomain name must exist in ClusterTopology.Spec.Levels
- Validation applies on both CREATE and UPDATE operations

**Topology Enablement Validation:**

- Webhook rejects PodCliqueSet with topology constraints when `clusterTopology.enabled: false`
- Error message: "topology support is not enabled in the operator"
- Prevents workload admission failure when topology is disabled

**Label Immutability:**

- Webhook rejects changes to `grove.io/cluster-topology-name` label on PodCliqueSet after creation
- Label can only be set during PodCliqueSet creation (see Mutation Webhook section above)
- Ensures topology reference remains consistent throughout resource lifecycle

### 3. Scheduler API Changes (Contract with KAI)

#### PodGang CRD Extensions

The Grove Operator translates topology configuration into Grove Scheduler API format, which serves as the contract with
KAI scheduler.

**PodGangSpec:**

```go
type PodGangSpec struct {
    // PodGroups is a list of member pod groups in the PodGang
    PodGroups []PodGroup `json:"podgroups"`

    // TopologyConstraint defines topology packing constraints for entire pod gang
    // Translated from PodCliqueSet.TopologyConstraint
    // Updated by operator on each reconciliation when PodCliqueSet topology constraints change
    // +optional
    TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`

    // TopologyConstraintGroupConfigs defines groups of PodGroups for topology-aware placement
    // Enhanced with topology constraints for PodCliqueScalingGroup (PCSG) level packing
    // Updated by operator on each reconciliation when PCSG topology constraints change
    // +optional
    TopologyConstraintGroupConfigs []TopologyConstraintGroupConfig `json:"topologyConstraintGroupConfigs,omitempty"`

    // PriorityClassName is the name of the PriorityClass for the PodGang
    PriorityClassName string `json:"priorityClassName,omitempty"`
}
```

**PodGang Metadata:**

The operator adds topology information to PodGang metadata via annotation:

```yaml
# Annotation added to PodGang
metadata:
  annotations:
    grove.io/topology-name: "<user-configured-name>"
```

This annotation allows the scheduler to locate the Kueue Topology resource without requiring a spec field, providing
flexibility for future API changes.

**TopologyConstraintGroupConfig:**

```go
// TopologyConstraintGroupConfig defines topology constraints for a group of PodGroups.
type TopologyConstraintGroupConfig struct {
    // PodGroupNames is the list of PodGroup names in the topology constraint group.
    PodGroupNames []string `json:"podGroupNames"`

    // TopologyConstraint defines topology packing constraints for this group.
    // Enables PCSG-level topology constraints.
    // Updated by operator when PodCliqueScalingGroup topology constraints change.
    // +optional
    TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`
}
```

**PodGroup:**

```go
type PodGroup struct {
    // Name is the name of the PodGroup
    Name string `json:"name"`

    // PodReferences is a list of references to the Pods in this group
    PodReferences []NamespacedName `json:"podReferences"`

    // MinReplicas is the number of replicas that needs to be gang scheduled
    MinReplicas int32 `json:"minReplicas"`

    // TopologyConstraint defines topology packing constraints for this PodGroup
    // Enables PodClique-level topology constraints
    // Updated by operator when PodClique topology constraints change
    // +optional
    TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`
}
```

**Supporting Types:**

```go
type TopologyConstraint struct {
    // PackConstraint defines topology packing constraint with required and preferred levels.
    // Operator translates user's level name to corresponding keys.
    // +optional
    PackConstraint *TopologyPackConstraint `json:"packConstraint,omitempty"`
}

type TopologyPackConstraint struct {
    // Required defines topology constraint that must be satisfied.
    // Holds key (not level name) translated from user's packDomain specification.
    // Example: "topology.kubernetes.io/rack".
    // +optional
    Required *string `json:"required,omitempty"`

    // Preferred defines best-effort topology constraint.
    // Auto-generated by operator using strictest level key for optimization.
    // Scheduler can fallback to less strict levels if preferred cannot be satisfied.
    // Example: "kubernetes.io/hostname".
    // +optional
    Preferred *string `json:"preferred,omitempty"`
}
```

**Changes Summary:**

Fields Added:

- `PodGangSpec.TopologyConstraint *TopologyConstraint` - PodGang-level packing from PodCliqueSet (optional pointer)
- `TopologyConstraintGroupConfig.TopologyConstraint *TopologyConstraint` - PCSG-level packing from
  PodCliqueScalingGroup (optional pointer)
- `PodGroup.TopologyConstraint *TopologyConstraint` - PodClique-level packing from PodClique (optional pointer)

Annotations Added:

- `grove.io/topology-name: "<user-configured-name>"` - Annotation on PodGang metadata referencing topology name

Fields Removed:

- `PodGangSpec.SpreadConstraints` - Not implemented; spread will be part of TopologyConstraint in future

**Note:** All TopologyConstraint fields are pointers with omitempty, allowing workloads without topology constraints.

#### Translation Logic

The operator translates Grove operator API to Grove Scheduler API with three-level topology constraint hierarchy:

**Topology Annotation:**

- Operator adds annotation `grove.io/topology-name: "<topology-name>"` to PodGang metadata
- Annotation value matches the ClusterTopology name from operator configuration
- KAI scheduler uses this annotation to locate the corresponding Kueue Topology CRD
- Annotation approach provides API flexibility for future changes without breaking spec

**Constraint Translation (Required and Preferred):**

The operator translates user's level names to keys and builds required/preferred structure:

**Required Constraints:**

- User specifies level name: `packDomain: "rack"`
- Operator looks up key from ClusterTopology: `"topology.kubernetes.io/rack"`
- Writes to PodGang: `TopologyConstraint.PackConstraint.Required = "topology.kubernetes.io/rack"`
- If user doesn't specify packDomain → `PackConstraint.Required` is nil

**Preferred Constraints (Auto-Generated):**

- Operator ALWAYS generates preferred constraint at all three levels
- Uses key of strictest level (e.g., `"kubernetes.io/hostname"` for "host" level)
- Writes to PodGang: `TopologyConstraint.PackConstraint.Preferred = "kubernetes.io/hostname"`
- Enables out-of-box optimization even without user configuration
- Scheduler can fallback to less strict levels if preferred cannot be satisfied

**Three-Level Translation:**

1. **PodGang Level** (from PodCliqueSet):
    - `PodGangSpec.TopologyConstraint.PackConstraint.Required` ← key looked up from user's level name (if set)
    - `PodGangSpec.TopologyConstraint.PackConstraint.Preferred` ← key of strictest level (e.g.,
     `"kubernetes.io/hostname"`)

2. **TopologyConstraintGroup Level** (from PodCliqueScalingGroup):
   - For each PCSG with TopologyConstraint, create TopologyConstraintGroupConfig
   - `TopologyConstraintGroupConfig.TopologyConstraint.PackConstraint.Required` ← key looked up from PCSG level
     name (if set)
   - `TopologyConstraintGroupConfig.TopologyConstraint.PackConstraint.Preferred` ← key of strictest level

3. **PodGroup Level** (from PodClique):
    - `PodGroup.TopologyConstraint.PackConstraint.Required` ← key looked up from PodClique level name (if set)
    - `PodGroup.TopologyConstraint.PackConstraint.Preferred` ← key of strictest level

**Example Translation:**

User creates PodCliqueSet with 3 replicas:

```yaml
spec:
  replicas: 3
  template:
    topologyConstraint:
      packDomain: "rack"  # User specifies level NAME (per-replica constraint)
```

Operator translates to PodGang:

```yaml
spec:
  topologyConstraint:
    packConstraint:
      required: "topology.kubernetes.io/rack"  # Operator looks up topologyKEY
      preferred: "kubernetes.io/hostname"  # Auto-generated topologyKEY of strictest level
```

**Per-Replica Behavior:**

- Replica 0: all pods constrained to one rack (e.g., rack-a)
- Replica 1: all pods constrained to one rack (e.g., rack-b)
- Replica 2: all pods constrained to one rack (e.g., rack-a)
- Different replicas can be in different racks (NOT all forced to same rack)

**Hierarchy Validation:**

- Maintains hierarchy validation rules (see Validation Webhook section)
- PodGang > TopologyConstraintGroupConfig > PodGroup hierarchy maintained

**Mutable Topology Constraints:**

- Users can update topology constraints at any time
- Changes only affect new or unscheduled pods (already scheduled pods retain placement)
- Operator re-translates constraints to PodGang on each reconciliation

## Security and RBAC

Grove operator requires read access to ClusterTopology and permission to manage finalizers:

```yaml
rules:
  - apiGroups: [ "grove.io" ]
    resources: [ "clustertopologies", "clustertopologies/finalizers" ]
    verbs: [ "get", "list", "watch", "update" ]
```

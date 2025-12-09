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
- Mutable topology configuration allowing runtime updates
- Flexible topology level ordering without enforced hierarchy

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
│  Admin Layer (OperatorConfiguration):                                   │
│  ┌────────────────────────────────────────────┐                         │
│  │ topology:                                  │                         │
│  │   enabled: true                            │                         │
│  │   levels:                                  │                         │
│  │     - domain: rack                         │                         │
│  │       key: "topology.kubernetes.io/rack"   │                         │
│  │     - domain: host                         │                         │
│  │       key: "kubernetes.io/hostname"        │                         │
│  └──────────────┬─────────────────────────────┘                         │
│                 │ (operator generates)                                  │
│                 ▼                                                       │
│  ┌──────────────────────┐          ┌──────────────────────┐             │
│  │ ClusterTopology      │          │ KAI Topology         │             │
│  │ "grove-topology"     │─────────▶│ "grove-topology"     │             │
│  │ (operator-managed)   │  Manage  │ (operator-managed)   │             │
│  └──────────┬───────────┘          └──────────┬───────────┘             │
│             │                                   │                       │
│             │ (validates against)               │ (used by)             │
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
│                                    │ • 3-level topology │     Scheduler │
│                                    │   (required+       │               │
│                                    │    preferred)      │               │
│                                    └────────────────────┘               │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

### 1. ClusterTopology Infrastructure

#### ClusterTopology CR

ClusterTopology is a cluster-scoped CR that defines consistent naming for cluster topology hierarchy to be used by
workload designers. It maps topology level domains to Kubernetes node labels.

**Characteristics:**

- **Cluster-scoped resource**: Only one ClusterTopology resource managed by operator: "grove-topology"
- **Operator-managed resource**: Created and managed by Grove operator based on OperatorConfiguration
- **Fixed name**: Always named "grove-topology" (no user configuration)
- **Fully mutable**: All fields can be updated after creation (levels count, domain values, key values)
- **Flexible ordering**: Levels can be specified in any order - no hierarchical ordering enforced
- **Supported topology levels**: Region, Zone, DataCenter, Block, Rack, Host, Numa
- **Webhook-validated**: Webhook validates domain/key uniqueness, key format, and authorization

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

// ClusterTopology defines the topology hierarchy for the cluster.
type ClusterTopology struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec ClusterTopologySpec `json:"spec"`
    // Status defines the observed state of ClusterTopology
    Status ClusterTopologyStatus `json:"status,omitempty"`
}

type ClusterTopologyStatus struct {
    // Conditions represent the latest available observations of the ClusterTopology's state
    Conditions []metav1.Condition `json:"conditions,omitempty"`
    // ObservedGeneration is the most recent generation observed by the controller
    ObservedGeneration *int64 `json:"observedGeneration,omitempty"`
    // LastErrors captures the last errors observed by the controller when reconciling the ClusterTopology
    LastErrors []LastError `json:"lastErrors,omitempty"`
}
```

**Condition Constants:**

```go
// ClusterTopology condition types
const (
    ConditionTypeReady = "Ready"
)

// ClusterTopology condition reasons
const (
    ConditionReasonTopologyReady = "TopologyReady"
    ConditionReasonKAITopologyCreationFailed = "KAITopologyCreationOrUpdateFailed"
)
```

**Ready Condition Semantics:**

- **Condition Type**: `Ready`
- **Status Values**:
  - `True` = ClusterTopology is ready and KAI Topology CR successfully created
  - `False` = ClusterTopology has errors or KAI Topology creation failed
  - `Unknown` = Status cannot be determined or reconciliation in progress
- **Reasons**:
  - `TopologyReady` - ClusterTopology configured and KAI Topology created successfully
  - `KAITopologyCreationOrUpdateFailed` - Failed to create/update KAI Topology CR
- **Message**: Human-readable details including KAI Topology creation status

**Status Examples:**

Success Case (ClusterTopology ready, KAI Topology created):
```yaml
status:
  observedGeneration: 1
  conditions:
    - type: Ready
      status: "True"
      reason: TopologyReady
      message: ""
      lastTransitionTime: "2025-12-07T10:00:00Z"
```

Failure Case (KAI Topology creation failed):
```yaml
status:
  observedGeneration: 1
  conditions:
    - type: Ready
      status: "False"
      reason: KAITopologyCreationOrUpdateFailed
      message: "Failed to create KAI Topology CR: <error details>"
      lastTransitionTime: "2025-12-07T10:00:00Z"
  lastErrors:
    - code: "KAI_TOPOLOGY_CREATE_ERROR"
      description: "Failed to create KAI Topology resource"
      observedAt: "2025-12-07T10:00:00Z"
```

```go
type ClusterTopologySpec struct {
    // Levels is a list of topology levels.
    // Levels can be specified in any order - no hierarchical ordering enforced.
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

Note: This CR is auto-generated by the operator - do not create manually.

```yaml
apiVersion: grove.io/v1alpha1
kind: ClusterTopology
metadata:
   name: grove-topology  # Operator-managed name (always "grove-topology")
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

**Configuring ClusterTopology:**

The ClusterTopology CR is generated and managed by the Grove operator. To configure it:

1. Define topology levels in OperatorConfiguration under `topology.levels` (see OperatorConfiguration section below)
2. Set `topology.enabled: true` in OperatorConfiguration
3. Restart the Grove operator
4. Operator creates ClusterTopology CR named "grove-topology"
5. Operator creates and continuously reconciles KAI Topology CR
6. If configuration is invalid or CR creation fails → operator exits with error

**OperatorConfiguration Example:**

```yaml
# In OperatorConfiguration
topology:
  enabled: true
  levels:
    - domain: rack
      key: "topology.kubernetes.io/rack"
    - domain: host
      key: "kubernetes.io/hostname"
```

Notes:
- Levels can be specified in any order - no automatic reordering performed
- Operator validates configuration at startup, exits if invalid
- Changes require operator restart to take effect


**A. Webhook Validation**

The validation webhook (`operator/internal/webhook/admission/clustertopology/validation/`) enforces business logic constraints:

- **On CREATE**: Validates domain uniqueness, key uniqueness, and key format (valid Kubernetes label key)
- **On UPDATE**: Runs same CREATE validations (uniqueness, format)
- **Authorization**: Only operator service account can CREATE/UPDATE/DELETE ClusterTopology
- **Important**: No hierarchical order enforcement - levels can be specified in any order
- **Important**: Resource is fully mutable - all fields can be updated after creation

**B. Operator Startup Validation**

The operator validates topology configuration in OperatorConfiguration at startup:

- Validates `topology.levels` configuration before creating ClusterTopology CR
- Fails fast with descriptive error if configuration is invalid
- Separate from ClusterTopology CR validation layers above

#### ClusterTopology Controller

The ClusterTopology controller manages the ClusterTopology resource lifecycle and KAI Topology synchronization.

**KAI Topology CR Generation and Reconciliation**

The controller continuously reconciles KAI Topology CR to keep it synchronized with ClusterTopology:

- On ClusterTopology creation → Create corresponding KAI Topology CR
- On ClusterTopology update → Update KAI Topology CR to match
- On reconciliation → Verify KAI Topology exists and matches ClusterTopology spec
- Name: Always matches ClusterTopology name ("grove-topology")
- Namespace: Cluster-scoped like ClusterTopology
- Status: Success/failure reflected in ClusterTopology Ready condition (reason: TopologyReady or KAITopologyCreationFailed)

**Authorization Validation**

Prevents ClusterTopology creation, modification, or deletion by non-operator service accounts.

Validation Mechanism:

1. Validation webhook intercepts CREATE, UPDATE, and DELETE operations on ClusterTopology
2. Webhook verifies the service account of the request
3. If service account is the operator's service account → Allow operation
4. If service account is any other (kubectl, UI, etc.) → Reject with error

This ensures only the operator can manage the ClusterTopology resource.

Key Points:

- Only the operator can create, modify, or delete ClusterTopology (operator-managed resource)
- Operator manages ClusterTopology lifecycle based on configuration

**Webhook Availability Limitation**

Current limitation: Webhooks are unavailable when the operator is down.

Impact:
- Cannot validate ClusterTopology CREATE/UPDATE/DELETE operations when operator unavailable
- Affects unmanaged ClusterTopology resources (if any were allowed)
- Not a practical concern for Grove's managed topology workflow

Scope:
- Grove only supports operator-managed ClusterTopology ("grove-topology")
- Users cannot and should not create unmanaged ClusterTopology resources
- The webhook limitation only matters if unmanaged topologies were supported

Potential Solution:
- Deploy webhooks in a separate pod from the operator controller
- Ensures webhook availability even when operator controller crashes
- Adds deployment and operational complexity

Current Decision:
- Not implementing separate webhook deployment
- Overhead not justified for managed-only topology model
- Webhook unavailability during operator downtime is acceptable trade-off

#### Operator Configuration

Operator enables/disables topology features and defines topology levels via operator config:

```yaml
topology:
  enabled: true
  levels:
    - domain: rack
      key: "topology.kubernetes.io/rack"
    - domain: host
      key: "kubernetes.io/hostname"
```

**Startup Behavior:**

- Topology configuration loaded only at operator startup
- Changes to `topology.enabled` or `levels` require operator restart to take effect
- If `topology.enabled: true`:
  - Operator generates ClusterTopology CR named "grove-topology"
  - Operator validates topology config at startup
  - If config invalid (duplicate domains, invalid keys, etc.) → operator exits with error
  - If ClusterTopology CR creation fails → operator exits with error
  - Operator generates KAI Topology CR
  - If KAI Topology creation fails → reflected in ClusterTopology Ready condition and LastErrors (not operator failure)
- If `topology.enabled: false`: topology features disabled

**Configuration Validation:**

At operator startup, Grove validates topology configuration in OperatorConfiguration:

- All domain values must be from predefined set (region, zone, datacenter, block, rack, host, numa)
- Each domain must be unique within levels list
- Each key must be unique within levels list
- Keys must be valid Kubernetes label keys
- If validation fails → operator exits with descriptive error
- Note: This validates OperatorConfiguration topology config, not ClusterTopology CR
- Note: ClusterTopology CR has separate validation layers (API server + webhook - see Validation section above)

**Admin Responsibilities:**

- Define topology levels in OperatorConfiguration
- Restart operator when changing topology configuration
- Grove operator automatically creates and manages both ClusterTopology and KAI Topology CRs

#### Enable/Disable Behavior

**Enabling Topology (topology.enabled: false → true):**

1. Admin defines topology levels in operator config
2. Admin sets `topology.enabled: true`
3. Admin restarts operator
4. Operator generates ClusterTopology CR "grove-topology"
5. Operator generates KAI Topology CR "grove-topology"
6. For existing workloads: operator validates constraints, removes invalid ones, updates status
7. For new workloads: validation webhook validates constraints

**Disabling Topology (topology.enabled: true → false):**

1. Admin updates operator config: `topology.enabled: false`
2. Admin restarts operator
3. For existing workloads:
   - Workloads with invalid constraints: remove required constraints that don't align
   - Keep preferred constraints (always valid)
   - Update PodCliqueSet status to reflect constraint removal
4. For new workloads:
   - Workloads with topology constraints: validation webhook rejects with error "topology support is not enabled in the operator"
   - Workloads without topology constraints: no impact

**Updating ClusterTopology:**

1. Admin updates topology levels in OperatorConfiguration
2. Admin restarts operator
3. Operator detects config change
4. Operator updates ClusterTopology CR to match new config
5. Operator updates KAI Topology CR to match
6. For existing workloads: validate constraints against new topology
   - Remove invalid required constraints
   - Keep preferred constraints
   - Update status fields

*note: in the future, we may support dynamic updates to ClusterTopology without operator restart.*

**Workload Constraint Handling During Topology Changes:**

When topology is disabled or levels change:

For Existing Workloads:
- If constraint references non-existent level:
  - Remove required constraint only
  - Keep preferred constraint (always uses strictest level)
  - Update PodCliqueSet status with constraint removal reason
- If constraint still valid:
  - No changes to constraints
- Changes affect only unscheduled pods
- Already scheduled pods retain their placement

For New Workloads:
- Validation webhook rejects workloads with invalid constraints
- Error message indicates which constraint is invalid
- Users must update workload spec to match available topology levels

Preferred Constraint Updates:
- When lowest topology level changes (e.g., host → numa)
- Operator updates preferred constraint to new strictest level
- Applies to all three levels (PodGang, TopologyConstraintGroup, PodGroup)

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

The mutation webhook for PodCliqueSet resources:

- **Label Removal**: No longer adds `grove.io/cluster-topology-name` label to PodCliqueSet
- **Reason**: Label removed as operator now directly manages single ClusterTopology ("grove-topology")

#### Validation Webhook

**Hierarchy Constraints:**

- Child PackDomain must be equal to or stricter than parent (stricter = higher index in levels list)
- PodCliqueSet → PodCliqueScalingGroup → PodClique hierarchy
- Referenced PackDomain name must exist in ClusterTopology.Spec.Levels
- Validation applies on both CREATE and UPDATE operations

**Topology Enablement Validation:**

- Webhook rejects PodCliqueSet with topology constraints when `topology.enabled: false`
- Error message: "topology support is not enabled in the operator"
- Prevents workload admission failure when topology is disabled

**Validation Location:**

- ClusterTopology validation occurs in OperatorConfiguration validation at startup (not webhooks)
- PodCliqueSet constraints validated by webhook against ClusterTopology

**Authorization Validation:**

- Validation webhook checks CREATE, UPDATE, and DELETE operations on ClusterTopology
- Verifies the service account making the request
- Only permits operations from the operator's service account
- Rejects unauthorized access attempts from kubectl, UI, or other clients
- This ensures only the operator can manage the ClusterTopology resource
- Error messages:
  - CREATE: "ClusterTopology can only be created by the operator"
  - UPDATE: "ClusterTopology can only be modified by the operator"
  - DELETE: "ClusterTopology can only be deleted by the operator"

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

Fields Removed:

- `PodGangSpec.SpreadConstraints` - Not implemented; spread will be part of TopologyConstraint in future

**Note:** All TopologyConstraint fields are pointers with omitempty, allowing workloads without topology constraints.

#### Translation Logic

The operator translates Grove operator API to Grove Scheduler API with three-level topology constraint hierarchy.

**Scheduler Topology Discovery:**

- KAI scheduler uses fixed ClusterTopology name "grove-topology" to locate KAI Topology CR
- No annotation needed since topology name is fixed and known

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

Grove operator requires permissions to manage ClusterTopology and KAI Topology resources:

```yaml
rules:
  - apiGroups: [ "grove.io" ]
    resources: [ "clustertopologies", "clustertopologies/status" ]
    verbs: [ "get", "list", "watch", "create", "update", "patch", "delete" ]
  - apiGroups: [ "<kai-topology-api-group>" ]  # API group for KAI Topology (to be determined)
    resources: [ "topologies" ]
    verbs: [ "get", "list", "watch", "create", "update", "patch", "delete" ]
```

**Permission Requirements:**

ClusterTopology:
- `create`: Generate ClusterTopology CR at startup
- `update`/`patch`: Update spec when config changes, update status
- `delete`: Clean up when topology disabled
- `status`: Update readiness and KAI Topology creation status

KAI Topology:
- `create`: Generate KAI Topology CR at startup
- `update`/`patch`: Keep KAI Topology synchronized with ClusterTopology
- `delete`: Clean up when topology disabled

## Workload Status Updates

When topology constraints become invalid (due to topology disable or level changes), Grove updates PodCliqueSet status to inform users about constraint validity using standard Kubernetes conditions.

### Status Fields

Grove uses `metav1.Condition` to report topology constraint status, following Kubernetes API conventions:

```go
type PodCliqueSetStatus struct {
    // ... existing fields ...

    // Conditions represent the latest available observations of PodCliqueSet state
    // +optional
    // +patchMergeKey=type
    // +patchStrategy=merge
    // +listType=map
    // +listMapKey=type
    Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
```

**Condition Type:** `TopologyConstraints`

**Condition Status Values:**
- `True` - All topology constraints are valid and satisfied
- `False` - One or more topology constraints are invalid
- `Unknown` - Topology constraint validity cannot be determined

**Condition Reasons:**
- `TopologyLevelsAvailable` - All required topology levels exist in ClusterTopology
- `TopologyLevelNotFound` - Required topology level not found in ClusterTopology
- `TopologyDisabled` - Topology support disabled in operator configuration
- `TopologyLevelsRemoved` - Multiple topology levels removed from ClusterTopology

### Status Update Scenarios

**Topology Constraints Valid:**

When all topology constraints are satisfied:

```yaml
status:
  conditions:
  - type: TopologyConstraints
    status: "True"
    observedGeneration: 5
    lastTransitionTime: "2025-12-08T10:00:00Z"
    reason: TopologyLevelsAvailable
    message: "All topology constraints satisfied"
```

**Topology Disabled:**

When topology is disabled in operator configuration:

```yaml
status:
  conditions:
  - type: TopologyConstraints
    status: "False"
    observedGeneration: 5
    lastTransitionTime: "2025-12-08T10:05:00Z"
    reason: TopologyDisabled
    message: "Topology support disabled in operator configuration. Required constraints removed."
```

**Topology Level Not Found:**

When a specific topology level is removed from ClusterTopology:

```yaml
status:
  conditions:
  - type: TopologyConstraints
    status: "False"
    observedGeneration: 5
    lastTransitionTime: "2025-12-08T10:10:00Z"
    reason: TopologyLevelNotFound
    message: "Topology level 'block' not found in ClusterTopology 'grove-topology'. Remove packDomain or update ClusterTopology."
```

**Multiple Topology Levels Removed:**

When multiple topology levels are removed from ClusterTopology:

```yaml
status:
  conditions:
  - type: TopologyConstraints
    status: "False"
    observedGeneration: 5
    lastTransitionTime: "2025-12-08T10:15:00Z"
    reason: TopologyLevelsRemoved
    message: "Topology levels removed from ClusterTopology: [block, rack]. Update packDomain constraints."
```

**Constraint Behavior:**

- Only **required** constraints are validated and reported in condition status
- **Preferred** constraints are always valid (they use strictest available level)
- Changes affect only **unscheduled pods**
- Already scheduled pods retain their placement
- Users can inspect condition status to understand constraint validity
- `ObservedGeneration` tracks which PodCliqueSet generation the condition reflects

## Open Questions

### Should PodGang Include Topology Name Annotation?

**Current Design:**
- ClusterTopology is always named "grove-topology" (fixed constant)
- KAI scheduler knows to look for KAI Topology CR with this hardcoded name
- No annotation on PodGang indicating which topology to use

**Alternative Approach:**

Add annotation to PodGang metadata:

```yaml
metadata:
  annotations:
    grove.io/topology-name: "grove-topology"
```

**Benefits:**

1. **Decoupling**: KAI doesn't need to hardcode "grove-topology" name
2. **Future Multi-Topology Support**: Could support multiple ClusterTopology CRs in the future
3. **Explicit Contract**: Clear indication of which topology a PodGang uses
4. **No KAI Changes Needed**: KAI reads annotation instead of assuming fixed name

**Trade-offs:**

1. **Added Complexity**: Reintroduces annotation we removed for simplicity
2. **Consistency**: Annotation must always be set when topology enabled
3. **Migration**: Existing design assumes fixed name; would need transition plan

**Recommendation:** Consider adding annotation for future flexibility, even if currently only one topology supported.

### Should Admins Be Able to Configure Topology Name?

**Current Design:**
- ClusterTopology name is hardcoded as "grove-topology" throughout the system
- Operator always creates and manages ClusterTopology with this fixed name
- KAI scheduler and all components assume this name

**Alternative Approach:**

Allow admins to configure the topology name in OperatorConfiguration:

```yaml
topology:
  enabled: true
  name: "grove-topology"  # Default value, admin can customize
  levels:
    - domain: rack
      key: "topology.kubernetes.io/rack"
    - domain: host
      key: "kubernetes.io/hostname"
```

**Benefits:**

1. **Admin Flexibility**: Admins can use custom naming that aligns with organizational conventions
2. **Cluster Control**: Better control over cluster resource naming
3. **Environment Distinction**: Different names for dev/staging/prod clusters if needed

**Trade-offs:**

1. **Added Complexity**: Additional configuration field to manage and validate
2. **Name Propagation**: Configured name must be passed consistently through the system
3. **Cross-Resource Consistency**: Must ensure ClusterTopology and KAI Topology use same configured name
4. **Validation**: Need to validate name format and uniqueness

**Recommendation:** Evaluate whether naming flexibility justifies the added configuration complexity. Default to "grove-topology" if configurable.

## Operational Scenarios

This section demonstrates how the topology-aware scheduling design handles different operational flows, helping users understand system behavior in various situations.

### Scenario 1: Initial Setup - TAS Enabled with Valid Config

**Initial State:**
- Grove operator not yet started
- OperatorConfiguration ready with topology config

**Flow:**

1. Admin configures topology in OperatorConfiguration:
   ```yaml
   topology:
     enabled: true
     levels:
       - domain: rack
         key: "topology.kubernetes.io/rack"
       - domain: host
         key: "kubernetes.io/hostname"
   ```

2. Admin starts/restarts Grove operator

3. Operator validates config at startup:
   - Checks domain values are from predefined set ✓
   - Checks each domain is unique ✓
   - Checks each key is unique ✓
   - Checks keys are valid Kubernetes label keys ✓

4. Operator creates ClusterTopology CR "grove-topology" with levels as specified in configuration

5. Operator creates KAI Topology CR "grove-topology"

6. ClusterTopology status updated:
   ```yaml
   status:
     observedGeneration: 1
     conditions:
       - type: Ready
         status: "True"
         reason: TopologyReady
         message: "ClusterTopology configured and KAI Topology created successfully"
   ```

**Key Behaviors:**
- Config validation happens at operator startup (fail-fast)
- Both ClusterTopology and KAI Topology CRs created automatically
- Levels used as specified - no automatic reordering
- Ready condition reflects successful setup

**Related Design Sections:** [Operator Configuration](#operator-configuration), [ClusterTopology Controller](#clustertopology-controller)

---

### Scenario 2: TAS Disabled

**Initial State:**
- TAS previously enabled with workloads using topology constraints
- Some PodCliqueSets running with `packDomain: "rack"`

**Flow:**

1. Admin updates OperatorConfiguration:
   ```yaml
   topology:
     enabled: false
   ```

2. Admin restarts operator

3. Operator detects TAS is disabled

4. For existing workloads:
   - Operator reconciles each PodCliqueSet
   - Removes **required** constraints from PodGang
   - Keeps **preferred** constraints (no-op, harmless)
   - Updates PodCliqueSet status:
     ```yaml
     conditions:
     - type: TopologyConstraints
       status: "False"
       observedGeneration: 5
       lastTransitionTime: "2025-12-08T10:00:00Z"
       reason: TopologyDisabled
       message: "Topology support disabled in operator configuration. Required constraints removed."
     ```

5. For new workloads:
   - Validation webhook rejects any PodCliqueSet with topology constraints
   - Error: "topology support is not enabled in the operator"

**Key Behaviors:**
- Existing workloads gracefully degraded (only required constraints removed)
- Already scheduled pods continue running (no rescheduling)
- New workloads with topology constraints blocked at admission
- Status clearly indicates why constraints were removed

**Related Design Sections:** [Enable/Disable Behavior](#enabledisable-behavior), [Validation Webhook](#validation-webhook)

---

### Scenario 3: TAS Enabled with Invalid Config

**Initial State:**
- Grove operator not running
- OperatorConfiguration has invalid topology config

**Flow:**

1. Admin sets invalid config (e.g., duplicate domains):
   ```yaml
   topology:
     enabled: true
     levels:
       - domain: rack
         key: "topology.kubernetes.io/rack"
       - domain: rack  # Duplicate!
         key: "topology.kubernetes.io/other-rack"
   ```

2. Admin attempts to start operator

3. Operator startup validation fails:
   - Detects duplicate domain "rack"
   - Logs descriptive error: "duplicate topology domain 'rack' in configuration"

4. Operator exits with error code

5. No ClusterTopology CR created (no partial state)

**Key Behaviors:**
- Fail-fast at operator startup (before creating any resources)
- Clear error messages for troubleshooting
- No partial or inconsistent state left behind
- Operator won't start until config is fixed

**Related Design Sections:** [Operator Configuration](#operator-configuration), [Configuration Validation](#configuration-validation)

---

### Scenario 4: Valid Workload Submission

**Initial State:**
- TAS enabled
- ClusterTopology exists with levels: rack, host

**Flow:**

1. User submits PodCliqueSet:
   ```yaml
   apiVersion: grove.io/v1alpha1
   kind: PodCliqueSet
   metadata:
     name: inference-workload
   spec:
     replicas: 2
     template:
       topologyConstraint:
         packDomain: "rack"
   ```

2. Validation webhook intercepts CREATE request and checks:
   - TAS is enabled in operator ✓
   - "rack" exists in ClusterTopology levels ✓
   - Hierarchy constraints satisfied (no child resources yet) ✓

3. Workload admitted successfully

4. Operator reconciles PodCliqueSet:
   - Translates "rack" domain → "topology.kubernetes.io/rack" key (required)
   - Adds "kubernetes.io/hostname" key (preferred, auto-generated from strictest level)
   - Creates PodGang with topology constraints:
     ```yaml
     spec:
       topologyConstraint:
         packConstraint:
           required: "topology.kubernetes.io/rack"
           preferred: "kubernetes.io/hostname"
     ```

5. KAI scheduler reads PodGang and schedules pods:
   - Replica 0: All pods on one rack (e.g., rack-a)
   - Replica 1: All pods on one rack (e.g., rack-b)

**Key Behaviors:**
- Validation at admission time (early failure detection)
- Automatic translation from domain names to cluster-specific keys
- Automatic preferred constraint generation for optimization
- Per-replica constraint behavior (each replica independently constrained)

**Related Design Sections:** [Validation Webhook](#validation-webhook), [Translation Logic](#translation-logic)

---

### Scenario 5: Invalid Workload Submission - Constraint Not in ClusterTopology

**Initial State:**
- TAS enabled
- ClusterTopology exists with levels: rack, host (no "block" level)

**Flow:**

1. User submits PodCliqueSet with non-existent level:
   ```yaml
   spec:
     template:
       topologyConstraint:
         packDomain: "block"  # Not in ClusterTopology!
   ```

2. Validation webhook intercepts CREATE request and checks:
   - TAS is enabled ✓
   - "block" exists in ClusterTopology ✗

3. Webhook rejects request with error:
   ```
   admission webhook "podcliqueset.grove.io" denied the request:
   topology level 'block' not defined in ClusterTopology 'grove-topology'
   ```

4. Workload not admitted (kubectl returns error to user)

**Key Behaviors:**
- Fail-fast at admission time (prevents invalid workloads)
- Clear error message indicating which level is missing
- User can fix by either changing constraint or asking admin to add level

**Related Design Sections:** [Validation Webhook](#validation-webhook)

---

### Scenario 6: Invalid Workload Submission - TAS Disabled

**Initial State:**
- TAS disabled (`topology.enabled: false`)
- No ClusterTopology CR exists

**Flow:**

1. User submits PodCliqueSet with topology constraint:
   ```yaml
   spec:
     template:
       topologyConstraint:
         packDomain: "rack"
   ```

2. Validation webhook intercepts CREATE request and checks:
   - TAS is enabled ✗

3. Webhook rejects request with error:
   ```
   admission webhook "podcliqueset.grove.io" denied the request:
   topology support is not enabled in the operator
   ```

4. Workload not admitted

**Key Behaviors:**
- Cannot submit topology constraints when TAS disabled
- Clear error message indicating TAS is disabled
- Prevents confusion about why topology isn't working

**Related Design Sections:** [Validation Webhook](#validation-webhook), [Topology Enablement Validation](#topology-enablement-validation)

---

### Scenario 7: Updating Topology Configuration - Adding Level

**Initial State:**
- TAS enabled
- ClusterTopology with levels: rack, host
- Workloads running with "rack" constraints

**Flow:**

1. Admin updates OperatorConfiguration to add "block" level:
   ```yaml
   topology:
     enabled: true
     levels:
       - domain: rack
         key: "topology.kubernetes.io/rack"
       - domain: block
         key: "topology.kubernetes.io/block"
       - domain: host
         key: "kubernetes.io/hostname"
   ```

2. Admin restarts operator

3. Operator detects configuration change

4. Operator updates ClusterTopology CR:
   - Adds "block" level
   - Levels used as specified in configuration

5. Operator updates KAI Topology CR to match

6. For existing workloads:
   - Required constraints still valid ("rack", "host" still present)
   - Preferred constraint still "kubernetes.io/hostname" (still strictest)
   - No PodGang updates needed
   - No status changes

7. New workloads can now use "block" level

**Key Behaviors:**
- Backward compatible (existing levels preserved)
- Levels used as specified in configuration
- Existing workloads completely unaffected
- New capability added seamlessly

**Related Design Sections:** [Updating ClusterTopology](#updating-clustertopology)

---

### Scenario 8: Updating Topology Configuration - Removing Level

**Initial State:**
- TAS enabled
- ClusterTopology with levels: rack, block, host
- Workload "wl-1" using "block" constraint
- Workload "wl-2" using "rack" constraint

**Flow:**

1. Admin updates config to remove "block":
   ```yaml
   topology:
     enabled: true
     levels:
       - domain: rack
         key: "topology.kubernetes.io/rack"
       - domain: host
         key: "kubernetes.io/hostname"
   ```

2. Admin restarts operator

3. Operator detects "block" level removed

4. Operator updates ClusterTopology CR (removes "block" level)

5. Operator updates KAI Topology CR

6. For workload "wl-1" (was using "block"):
   - Operator reconciles PodCliqueSet
   - Removes required constraint from PodGang
   - Keeps preferred constraint ("kubernetes.io/hostname")
   - Updates PodCliqueSet status:
     ```yaml
     conditions:
     - type: TopologyConstraints
       status: "False"
       observedGeneration: 5
       lastTransitionTime: "2025-12-08T10:00:00Z"
       reason: TopologyLevelNotFound
       message: "Topology level 'block' not found in ClusterTopology 'grove-topology'. Remove packDomain or update ClusterTopology."
     ```
   - Already scheduled pods continue running (no rescheduling)
   - New pods scheduled without "block" constraint

7. For workload "wl-2" (using "rack"):
   - No changes (constraint still valid)

8. New workloads cannot use "block" (webhook rejects)

**Key Behaviors:**
- Graceful degradation for affected workloads
- Only required constraints removed (preferred kept)
- Status clearly indicates what happened
- No disruption to scheduled pods
- Unaffected workloads continue normally

**Related Design Sections:** [Updating ClusterTopology](#updating-clustertopology), [Workload Constraint Handling During Topology Changes](#workload-constraint-handling-during-topology-changes)

---

### Scenario 9: KAI Topology Creation Failure

**Initial State:**
- TAS enabled with valid config
- KAI Topology API unavailable or permission denied

**Flow:**

1. Operator starts with valid topology configuration

2. Operator creates ClusterTopology CR "grove-topology" successfully

3. Operator attempts to create KAI Topology CR

4. KAI Topology creation fails (error: "API not available")

5. Operator does NOT exit (non-fatal error)

6. Operator updates ClusterTopology status:
   ```yaml
   status:
     observedGeneration: 1
     conditions:
       - type: Ready
         status: "False"
         reason: KAITopologyCreationFailed
         message: "Failed to create KAI Topology CR: API not available"
     lastErrors:
       - code: "KAI_TOPOLOGY_CREATE_ERROR"
         description: "Failed to create KAI Topology resource: API not available"
         observedAt: "2025-12-07T10:00:00Z"
   ```

7. Operator continues running normally

8. ClusterTopology controller continues reconciliation (automatic retry)

9. Once KAI API becomes available, next reconciliation succeeds and status updated to Ready=True

**Key Behaviors:**
- Non-fatal error (operator stays running)
- Status reflects failure with details
- Automatic retry through reconciliation loop
- Clear error information for troubleshooting

**Related Design Sections:** [KAI Topology CR Generation and Reconciliation](#kai-topology-cr-generation-and-reconciliation), [Startup Behavior](#startup-behavior)

---

### Scenario 10: Hierarchy Constraint Violation

**Initial State:**
- TAS enabled
- ClusterTopology with levels: rack, host

**Flow:**

1. User submits PodCliqueSet with hierarchy violation:
   ```yaml
   apiVersion: grove.io/v1alpha1
   kind: PodCliqueSet
   spec:
     template:
       topologyConstraint:
         packDomain: "host"  # Parent: index 1 (stricter)
       scalingGroups:
         - name: workers
           config:
             topologyConstraint:
               packDomain: "rack"  # Child: index 0 (less strict!) ✗
   ```

2. Validation webhook checks hierarchy:
   - Parent (PCS) constraint: "host" (index 1 in levels)
   - Child (PCSG) constraint: "rack" (index 0 in levels)
   - Validation: Child index (0) < Parent index (1) ✗
   - Rule: Child must be >= parent (stricter or equal)

3. Webhook rejects with error:
   ```
   admission webhook "podcliqueset.grove.io" denied the request:
   child topology constraint 'rack' must be equal to or stricter than parent constraint 'host'
   ```

4. Workload not admitted

**Key Behaviors:**
- Hierarchy validation at admission time
- Child must be equal to or stricter than parent (higher index)
- Clear error explaining the violation
- Prevents invalid constraint hierarchies

**Related Design Sections:** [Validation Webhook](#validation-webhook), [Hierarchy Constraints](#hierarchy-constraints)

---

### Scenario 11: Dynamic Constraint Update on Running Workload

**Initial State:**
- PodCliqueSet "inference-wl" running with `packDomain: "rack"`
- Replica 0: 3 pods scheduled on rack-a
- Replica 1: 3 pods scheduled on rack-b

**Flow:**

1. User updates PodCliqueSet to stricter constraint:
   ```yaml
   spec:
     template:
       topologyConstraint:
         packDomain: "host"  # Changed from "rack" to "host"
   ```

2. Validation webhook checks update:
   - Hierarchy still valid (no child resources affected) ✓
   - "host" exists in ClusterTopology ✓

3. Update accepted

4. Operator reconciles PodCliqueSet:
   - Detects topology constraint change
   - Updates PodGang:
     - Required: "topology.kubernetes.io/rack" → "kubernetes.io/hostname"
     - Preferred: "kubernetes.io/hostname" (unchanged)

5. For already scheduled pods:
   - Replica 0 pods remain on rack-a (no rescheduling)
   - Replica 1 pods remain on rack-b (no rescheduling)

6. For new pods (e.g., scale-up, pod replacement):
   - Scheduled with new "host" constraint
   - Each replica constrained to single host

**Key Behaviors:**
- Topology constraints are mutable
- Changes only affect unscheduled pods
- No disruption to running pods
- Allows runtime constraint tightening/loosening

**Related Design Sections:** [Mutable Topology Constraints](#mutable-topology-constraints)

---

### Scenario 12: Complete TAS Lifecycle

This scenario demonstrates the full lifecycle of topology-aware scheduling from initial disabled state through various operational phases.

**Phase 1: Initial State (TAS Disabled)**

- Cluster running with `topology.enabled: false`
- Workloads running without topology constraints
- No ClusterTopology CR exists

**Phase 2: Enable TAS**

1. Admin configures topology:
   ```yaml
   topology:
     enabled: true
     levels:
       - domain: rack
         key: "topology.kubernetes.io/rack"
       - domain: host
         key: "kubernetes.io/hostname"
   ```

2. Admin restarts operator

3. Operator creates ClusterTopology + KAI Topology CRs

4. ClusterTopology status: Ready=True

5. Existing workloads:
   - Continue running unchanged (no topology constraints)
   - PodGangs gain preferred constraints automatically (optimization)

**Phase 3: Submit New Workload with Topology**

1. User creates PodCliqueSet:
   ```yaml
   spec:
     template:
       topologyConstraint:
         packDomain: "rack"
   ```

2. Workload admitted and scheduled with rack affinity

**Phase 4: Update Topology (Add Level)**

1. Admin adds "block" level between rack and host

2. Admin restarts operator

3. ClusterTopology updated: rack < block < host

4. Existing workload continues working (rack still valid)

5. New workloads can now use "block" level

**Phase 5: Update Topology (Remove Level)**

1. Admin removes "block" level

2. Admin restarts operator

3. Any workloads using "block":
   - Required constraint removed
   - Status updated to reflect removal

**Phase 6: Disable TAS**

1. Admin sets `topology.enabled: false`

2. Admin restarts operator

3. Existing workloads with topology:
   - Required constraints removed from PodGang
   - Preferred constraints kept (harmless)
   - Status updated: "topology disabled"

4. New workload submissions with topology rejected

**Key Behaviors:**
- Complete lifecycle coverage
- Graceful transitions at each phase
- Status always reflects current state
- No data loss or disruption during transitions

**Related Design Sections:** [Enable/Disable Behavior](#enabledisable-behavior), [Updating ClusterTopology](#updating-clustertopology)

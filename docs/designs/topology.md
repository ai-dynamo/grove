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

- Define standard topology model uniform across cloud and on-prem clusters
- Enable cluster-specific topology mapping through admin configuration
- Enable packing constraints at PodCliqueSet, PodCliqueScalingGroup, and PodClique levels
- Translate user-defined topology constraints to scheduler-specific API
- Generate downstream scheduler topology CRs (initial implementation: KAI Topology)

## Non-Goals

- Spread constraints across topology domains (anti-packing)
- Root domain constraints for entire workloads
 (constraining all replicas to the same single domain instance - e.g., all 3 replicas must be in rack-1. PackDomain 
 allows each replica to be in different instances of the same domain level - e.g.,
 replica-1 in rack-1, replica-2 in rack-2, replica-3 in rack-3)
- Ratio-based affinity between scaling groups
- Multi-cluster topology support
- Automatic topology suggestion based on workload characteristics

## Proposal

Grove implements topology-aware scheduling by allowing admins to define cluster topology levels
in OperatorConfiguration. Users can then reference these topology levels when specifying packing
constraints in their workloads. The operator translates admin topology definitions into
ClusterTopology CR and downstream scheduler topology CRs (KAI Topology), and translates user
packing constraints into scheduler-specific API, automatically generating preferred constraints
for optimization while allowing users to specify required constraints for strict placement.

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
│  └──────────┬───────────┘          └────────────┬─────────┘             │
│             │                                   │                       │
│             │ (validates against)               │ (used by KAI Scheduler) │
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

**TopologyDomain Definitions:**

- **Region**: Network local to a CSP region
- **Zone**: Network local to a CSP availability-zone within a region
- **DataCenter**: Network local to a data-center within a CSP availability-zone
- **Block**: Network local to a switching block unit within a data-center
- **Rack**: First-level network grouping of compute hosts (includes NVLink domains as logical racks)
- **Host**: Individual compute host
- **Numa**: NUMA node (processor and memory locality domain) within a compute host

**Characteristics:**

- **Cluster-scoped resource**: Only one ClusterTopology resource managed by operator: "grove-topology"
- **Operator-managed resource**: Created and managed by Grove operator based on OperatorConfiguration
- **User-created CRs allowed**: Users can create additional ClusterTopology CRs but Grove does not use them
- **Fixed name for managed CR**: Operator-managed CR always named "grove-topology"
- **Mutability**:
  - **Operator-managed CR** ("grove-topology"): Only modified by operator at startup (not runtime)
  - **User-created CRs**: Fully mutable - can be modified anytime
- **Order-Independent Configuration**: The mapping of topology domains to kubernetes labels, in order to define levels, can be specified in any order - no hierarchical ordering enforced in OperatorConfiguration
- **Supported topology levels**: Region, Zone, DataCenter, Block, Rack, Host, Numa
- **Webhook-validated**: Webhook validates domain/key uniqueness, key format, and authorization (only operator can modify "grove-topology")

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

// ClusterTopologySpec defines the topology hierarchy specification.
type ClusterTopologySpec struct {
// Levels is an ordered list of topology levels from broadest to narrowest scope.
// The order in this list defines the hierarchy (index 0 = broadest level).
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


type ClusterTopologyStatus struct {
    // Conditions represent the latest available observations of the ClusterTopology's state
    Conditions []metav1.Condition `json:"conditions,omitempty"`
    // ObservedGeneration is the most recent generation observed by the controller
    ObservedGeneration *int64 `json:"observedGeneration,omitempty"`
    // LastErrors captures the last errors observed by the controller when reconciling the ClusterTopology
    LastErrors []LastError `json:"lastErrors,omitempty"`
}
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
  - `False` = Downstream topology creation or update failed (currently KAI Topology)
  - `Unknown` = Status cannot be determined or reconciliation in progress
- **Reasons**:
  - `TopologyReady` - ClusterTopology configured and KAI Topology created successfully
  - `TopologyCreationOrUpdateFailed` - Failed to create/update KAI Topology CR
- **Message**: Human-readable details including KAI Topology creation status

**Status Condition States:**

| Status | Reason                         | Scenario                            | LastErrors                |
|--------|--------------------------------|-------------------------------------|---------------------------|
| True   | TopologyReady                  | KAI Topology created successfully   | None                      |
| False  | TopologyCreationOrUpdateFailed | KAI Topology creation/update failed | KAI_TOPOLOGY_CREATE_ERROR |


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

The Manged ClusterTopology CR is generated and managed sole by the Grove operator. To configure it:

1. Define topology levels in OperatorConfiguration under `clusterTopology.levels` (see OperatorConfiguration section below)
2. Set `clusterTopology.enabled: true` in OperatorConfiguration
3. Restart the Grove operator
4. Operator creates ClusterTopology CR named "grove-topology"
5. Operator creates and continuously reconciles KAI Topology CR
6. If configuration is invalid or ClusterTopology CR creation fails → operator exits with error
7. If downstream topology CR creation fails (KAI Topology) → reflected in ClusterTopology Ready condition (operator continues)


**A. Webhook Validation**

The validation webhook enforces business logic constraints:

- **On CREATE**: Validates domain uniqueness, key uniqueness, and key format
- **On UPDATE**: Runs same CREATE validations (uniqueness, format)
- **Authorization**: Only operator service account can CREATE/UPDATE/DELETE the Managed ClusterTopology with the resource Name grove-topology
- **Important**: No hierarchical order enforcement - levels can be specified in any order. The order is already defined (Region, Zone, DataCenter, Block, Rack, Host, Numa)


**Authorization Validation**

Validation webhook ensures only operator service account can modify operator-managed ClusterTopology ("grove-topology"):
- Operator service account: Can CREATE/UPDATE/DELETE "grove-topology"
- User service accounts: Cannot modify "grove-topology" (rejected by webhook)
- User service accounts: CAN create/modify/delete their own ClusterTopology CRs (Grove ignores them)

*Note: Webhooks unavailable during operator downtime, but not a concern for operator-managed workflow.*

#### ClusterTopology Controller

The ClusterTopology controller manages the ClusterTopology resource lifecycle and KAI Topology synchronization.

**KAI Topology CR Generation and Reconciliation**

The controller continuously reconciles KAI Topology CR to keep it synchronized with ClusterTopology:

- On ClusterTopology creation → Create corresponding KAI Topology CR
- On ClusterTopology update → Update KAI Topology CR to match
- On reconciliation → Verify KAI Topology exists and matches ClusterTopology spec
- Name: Always matches ClusterTopology name (e.g., "grove-topology" for managed CR)
- Namespace: Cluster-scoped like ClusterTopology
- Status: Success/failure reflected in ClusterTopology Ready condition (reason: TopologyReady or KAITopologyCreationFailed)

**Reconciliation Details:**

- **Owner Reference**: ClusterTopology owns KAI Topology CR for cascading deletion
- **Conflict Resolution**: ClusterTopology spec is source of truth

**Future Extensibility:**

*Note: Currently, the Grove topology controller generates only KAI Topology CRs.
In the future, the controller may be extended to generate different types of underlying topology
CRDs for various schedulers or topology consumers, allowing Grove to support multiple scheduling backends simultaneously.*

#### Operator Configuration

Operator enables/disables topology features and defines topology levels via operator config:

```yaml
clusterTopology:
  enabled: true
  levels:
    - domain: rack
      key: "topology.kubernetes.io/rack"
    - domain: host
      key: "kubernetes.io/hostname"
```

**OperatorConfiguration API Structure:**

```go
type OperatorConfiguration struct {
    // Topology configuration for cluster topology hierarchy
    // +optional
    ClusterTopology *TopologyConfiguration `json:"clusterTopology,omitempty"`
}

type ClusterTopologyConfiguration struct {
    // Enabled indicates whether topology-aware scheduling is enabled
    Enabled bool `json:"enabled"`

    // Levels is a list of topology levels for the cluster
    // Order in this list does not affect hierarchy (semantic order used for validation)
    Levels []TopologyLevel `json:"levels"`
}
```

**Startup Behavior:**

- Topology configuration loaded only at operator startup
- Changes to `clusterTopology.enabled` or `levels` require operator restart to take effect
- If `clusterTopology.enabled: true`:
  - Operator generates ClusterTopology CR named "grove-topology"
  - Operator validates topology config at startup
  - If config invalid (duplicate domains, invalid keys, etc.) → operator exits with error
  - If ClusterTopology CR creation fails → operator exits with error
  - Operator generates KAI Topology CR
  - If KAI Topology creation fails → reflected in ClusterTopology Ready condition and LastErrors (not operator failure)
- If `clusterTopology.enabled: false`: topology features disabled, operator attempts to delete ClusterTopology CR at startup (non-fatal - logs error and continues if deletion fails). KAI Topology cascade-deleted via owner reference.

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

**Enabling Topology (clusterTopology.enabled: false → true):**

***For existing workloads:***
* operator validates constraints, removes constraints referencing non-existent topology levels from PodGang, updates PodCliqueSet status to indicate constraints are ignored

***For new workloads:***
* validation webhook validates constraints

**Disabling Topology (clusterTopology.enabled: true → false):**

Operator behavior at startup:
- Operator deletes ClusterTopology CR
- KAI Topology CR is cascade-deleted via owner reference
- If deletion fails: non-fatal error, operator logs and continues

For existing workloads:
   - remove all topology constraints (required/preferrd) in PodGang
   - Update PodCliqueSet status to reflect constraint removal

For new workloads:
   - Workloads with topology constraints: validation webhook rejects with error "topology support is not enabled in the operator"
   - Workloads without topology constraints: no impact

**Updating ClusterTopology Levels:**

For Existing Workloads:
- If constraint references non-existent level:
  - Remove required constraint for invalid level from PodGang
  - Update preferred constraint to new strictest level in updated ClusterTopology
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
- When strictest (narrowest) topology level changes (e.g., host → numa)
- Operator updates preferred constraint to new strictest level
- Applies to all three levels in scheduler API: PodGang (from PodCliqueSet), TopologyConstraintGroup (from PodCliqueScalingGroup), and PodGroup (from PodClique)

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

**Condition Type:** `TopologyConstraintSatisfied`

**Condition Status Values:**
- `True` - All topology constraints are valid and satisfied
- `False` - One or more topology constraints are invalid
- `Unknown` - Topology constraint validity cannot be determined

**Condition Reasons:**
- `TopologyLevelNotFound` - One or More topology levels not found in ClusterTopology
- `TopologyDisabled` - Topology support disabled in operator configuration

### Status Update Scenarios

**TopologyConstraintSatisfied Condition States:**

| Status | Reason                | Scenario              | Message Example                                                        |
|--------|-----------------------|-----------------------|------------------------------------------------------------------------|
| True   |                       | All constraints valid |                                                                        |
| False  | TopologyDisabled      | Topology disabled     | "Topology support disabled in operator configuration"                  |
| False  | TopologyLevelNotFound | Single level missing  | "Topology level 'block' not found in ClusterTopology 'grove-topology'" |

**Example Status:**

```yaml
status:
  conditions:
  - type: TopologyConstraintSatisfied
    status: "False"
    observedGeneration: 5
    lastTransitionTime: "2025-12-08T10:10:00Z"
    reason: TopologyLevelNotFound
    message: "Topology level 'block' not found in ClusterTopology 'grove-topology'.  Topology constraint will be ignored. Please remove packDomain or update ClusterTopology."


#### Validation Webhook

**Hierarchy Constraints:**

- Child PackDomain must be semantically equal to or stricter than parent
- **Semantic Order** (narrowest to broadest): numa < host < rack < block < datacenter < zone < region
- Stricter means semantically narrower/more specific (e.g., host is stricter than rack)
- Validation uses semantic order, NOT OperatorConfiguration list index
- PodCliqueSet → PodCliqueScalingGroup → PodClique hierarchy
- Referenced PackDomain name must exist in ClusterTopology.Spec.Levels
- Validation applies on both CREATE and UPDATE operations

**Example**: If parent has `packDomain: "rack"`, child can use `rack` (equal), `host` (stricter), or `numa` (strictest), but NOT `block` or broader domains.

**Topology Enablement Validation:**

- Webhook rejects PodCliqueSet with topology constraints when `clusterTopology.enabled: false`
- Error message: "topology support is not enabled in the operator"
- Prevents workload admission failure when topology is disabled

### 3. Scheduler API Changes (Contract with KAI)

#### PodGang CRD Extensions

The Grove Operator translates topology configuration into Grove Scheduler API format, which serves as the contract with
KAI scheduler.

**PodGangSpec:**

```go
type PodGangSpec struct {
    // TopologyConstraint defines topology packing constraints for entire pod gang
    // Translated from PodCliqueSet.TopologyConstraint
    // Updated by operator on each reconciliation when PodCliqueSet topology constraints change
    // +optional
    TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`
}
```

**TopologyConstraintGroupConfig:**

```go
// TopologyConstraintGroupConfig defines topology constraints for a group of PodGroups.
type TopologyConstraintGroupConfig struct {
    // Name is the name of the topology constraint group.
    // It will drive from the corresponding PCSG name.
    Name string `json:"name"`   
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


#### Translation Logic

The operator translates Grove operator API to Grove Scheduler API with three-level topology constraint hierarchy.

**Scheduler Topology Discovery:**

- KAI will use topology annotation on the podGroup to allow KAI discover which KAI topology is used.
- The annotation key will be `grove.io/topology-name` and the value will be the name of the KAI Topology CR created by Grove operator, which is `grove-topology`.
- This annotation decouples KAI scheduler from Grove - KAI doesn't need to know which topology Grove created, it discovers it dynamically via the annotation.

*Note: This allows KAI to discover the topology configuration defined by Grove operator without hardcoding topology names in KAI Scheduler,
allowing changing the name from our side*

**Constraint Translation (Required and Preferred):**

The operator translates user's level names to keys and builds required/preferred structure:

**Required Constraints:**

- User specifies level name: `packDomain: "rack"`
- Operator looks up key from ClusterTopology: `"topology.kubernetes.io/rack"`
- Writes to PodGang: `TopologyConstraint.PackConstraint.Required = "topology.kubernetes.io/rack"`
- If user doesn't specify packDomain → `PackConstraint.Required` is nil

**Preferred Constraints (Auto-Generated):**

- Operator ALWAYS generates preferred constraint at all three levels
- Uses key of strictest (narrowest) level configured in ClusterTopology (for example if the ClusterTopology is host, rack, zone use "host" level, `"kubernetes.io/hostname"`)
- Example: If levels include "host", preferred = `"kubernetes.io/hostname"`
- Writes to PodGang: `TopologyConstraint.PackConstraint.Preferred = "kubernetes.io/hostname"`
- Enables out-of-box optimization even without user configuration
- Scheduler can fallback to less strict levels if preferred cannot be satisfied

**Edge Cases:**

1 **Strictest Level Changes During Topology Update**:
   - Initial topology: rack, host (strictest = host)
   - Updated topology: rack, host, numa (strictest = numa)
   - Operator updates preferred constraints to "topology.kubernetes.io/numa" for all affected PodGangs
   - Changes only affect new/unscheduled pods
   - Already scheduled pods retain their placement

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

**Translation Example:**

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

## Testing

This section defines testing strategies for topology-aware scheduling, covering integration tests and E2E test scenarios.

### Integration Tests

Integration tests validate topology components in isolation using table-driven tests and envtest clusters.

#### Webhook Validation Tests

**ClusterTopology Validation**:
- Valid single-level topology
- Valid multi-level topology
- Duplicate domain rejection
- Duplicate key rejection
- Invalid key format rejection
- Domain uniqueness validation
- Key uniqueness validation

**Authorization Validation**:
- Operator service account can CREATE "grove-topology"
- User service account cannot CREATE "grove-topology"
- User service account CAN CREATE user-named ClusterTopology CRs
- Operator service account can UPDATE "grove-topology"
- User service account cannot UPDATE "grove-topology"
- User service account CAN UPDATE their own ClusterTopology CRs

**PodCliqueSet Hierarchy Validation**:
- Valid hierarchy using semantic order (host parent, numa child)
- Invalid hierarchy rejection (host parent, rack child)
- Equal constraints allowed (rack parent, rack child)


#### Controller Reconciliation Tests

**KAI Topology CR Generation**:
- Create KAI Topology on ClusterTopology creation
- Update KAI Topology on ClusterTopology spec change
- Set owner reference from ClusterTopology to KAI Topology
- Update status condition on success (Ready=True, reason=TopologyReady)
- Update status condition on failure (Ready=False, reason=KAITopologyCreationFailed)

**Drift Detection**:
- Detect manual KAI Topology modification
- Reset KAI Topology to match ClusterTopology spec
- Recreate deleted KAI Topology automatically

**Status Updates**:
- Set TopologyReady condition when KAI Topology successfully created
- Set KAITopologyCreationFailed condition when creation fails
- Update ObservedGeneration field to match ClusterTopology generation

## Open Questions

### Should Admins Be Able to Configure Topology Name?

**Current Design:**
- ClusterTopology name is hardcoded as "grove-topology" throughout the system
- Operator always creates and manages ClusterTopology with this fixed name
- KAI scheduler and all components assume this name

**Alternative Approach:**

Allow admins to configure the topology name in OperatorConfiguration:

```yaml
clusterTopology:
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


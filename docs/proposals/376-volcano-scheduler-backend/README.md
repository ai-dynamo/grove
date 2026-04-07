# Volcano Scheduler Backend

## Overview

This document describes the implementation of [Volcano](https://volcano.sh) scheduler backend support in Grove Operator.
Volcano is a CNCF open-source batch scheduler designed for AI/ML workloads, providing Gang Scheduling and network topology-aware scheduling capabilities.

Grove already has a Scheduler Backend Framework (see [GREP-375](../375-scheduler-backend-framework/README.md)).
This implementation follows that framework, adding Volcano as a third optional scheduler backend without modifying any existing controller logic.

---

## Background: Grove Scheduler Backend Framework

Before understanding this implementation, it's important to understand Grove's scheduler backend framework.

### SchedBackend Interface

All scheduler backends must implement the following interface (`operator/internal/schedulerbackend/types.go`):

```go
type SchedBackend interface {
    Name() string
    Init() error
    SyncPodGang(ctx context.Context, podGang *PodGang) error
    OnPodGangDelete(ctx context.Context, podGang *PodGang) error
    PreparePod(pod *corev1.Pod)
    ValidatePodCliqueSet(ctx context.Context, pcs *PodCliqueSet) error
}
```

| Method | Trigger | Purpose |
|---|---|---|
| `Name()` | Any time | Returns the unique backend identifier, also the value for `Pod.Spec.SchedulerName` |
| `Init()` | Operator startup | One-time initialization, create cluster-level resources |
| `SyncPodGang()` | PodGang create/update | Sync PodGang to scheduler-specific CR |
| `OnPodGangDelete()` | PodGang deletion | Clean up scheduler-specific CR |
| `PreparePod()` | Before Pod creation | Inject scheduler-required schedulerName, annotations, etc. |
| `ValidatePodCliqueSet()` | PodCliqueSet admission | Backend-specific validation |

### Existing Backends

| Backend | SchedulerName | Description |
|---|---|---|
| `kube` | `kube-scheduler` | Kubernetes default scheduler, Pod.Spec.SchedulerName = "default-scheduler" |
| `kaischeduler` | `kai-scheduler` | NVIDIA KAI scheduler |
| `volcano` (this impl) | `volcano` | Volcano batch scheduler |

---

## Volcano Concepts

### PodGroup (`scheduling.volcano.sh/v1beta1`)

Volcano uses the `PodGroup` CR to implement Gang Scheduling. It's the core resource that Volcano uses to identify a batch of Pods belonging to the same scheduling group.

```yaml
apiVersion: scheduling.volcano.sh/v1beta1
kind: PodGroup
metadata:
  name: my-training-job
  namespace: default
spec:
  minMember: 8          # At least 8 Pods must be scheduled together
  queue: default         # Volcano Queue to use
  priorityClassName: high-priority
  networkTopology:       # Optional: network topology constraint (requires HyperNode support)
    mode: hard           # hard=strict constraint, soft=best effort
    highestTierAllowed: 2  # Maximum HyperNode tier allowed to span
```

### HyperNode (`topology.volcano.sh/v1alpha1`)

`HyperNode` is a cluster-level CR introduced in Volcano v1.12+, representing a group of nodes with the same network topology characteristics (e.g., same rack, same zone). Lower tier numbers indicate tighter topology domains (closer to hardware).

```
Tier 1 = Host (tightest, same host)
Tier 2 = Rack (same rack)
Tier 3 = Zone (same availability zone)
```

**Note**: HyperNodes are created and maintained by cluster administrators. Grove does not manage them.

### Pod Annotation

Volcano identifies which PodGroup a Pod belongs to through annotations on the Pod:

```
scheduling.volcano.sh/group-name: <podgroup-name>
```

---

## Implementation

### Mapping: Grove PodGang → Volcano PodGroup

| Grove PodGang Field | Volcano PodGroup Field | Description |
|---|---|---|
| `sum(PodGroup[i].MinReplicas)` | `spec.minMember` | Sum of all sub-group minimum replicas |
| `spec.priorityClassName` | `spec.priorityClassName` | Direct mapping |
| `VolcanoConfig.Queue` (default "default") | `spec.queue` | Read from backend config |
| `TopologyConstraint.PackConstraint.Required` | `spec.networkTopology.mode = "hard"` + `highestTierAllowed = tier` | Hard constraint: topology key maps to HyperNode tier |
| `TopologyConstraint.PackConstraint.Preferred` (only this field) | `spec.networkTopology.mode = "soft"` + `highestTierAllowed = tier` | Soft constraint: topology key maps to HyperNode tier |
| No topology constraint | `spec.networkTopology` not set | Plain gang scheduling |

#### Topology Key → Tier Mapping

Grove's PodGang `TopologyConstraint` stores node label keys (e.g., `topology.kubernetes.io/rack`), while Volcano's `NetworkTopology` requires an integer tier number.

This mapping is explicitly configured via `VolcanoSchedulerConfiguration.TopologyKeyToTier` to avoid relying on implicit conventions.

Example configuration:
```yaml
scheduler:
  profiles:
  - name: volcano
    config:
      queue: gpu-jobs
      topologyKeyToTier:
        "kubernetes.io/hostname": 1          # Host = tier 1 (tightest)
        "topology.kubernetes.io/rack": 2     # Rack = tier 2
        "topology.kubernetes.io/zone": 3     # Zone = tier 3
```

The tier numbers used by administrators when creating HyperNodes must match this configuration.

---

## Code Changes

### 1. `operator/go.mod`

**Change**: Add `volcano.sh/apis v1.13.0` as a direct dependency.

```
volcano.sh/apis v1.13.0
```

Volcano API packages used:
- `volcano.sh/apis/pkg/apis/scheduling/v1beta1` — PodGroup, NetworkTopologySpec, etc.

Packages not used:
- `topology/v1alpha1` (HyperNode) — Grove doesn't create/manage HyperNodes, no need to import

---

### 2. `operator/internal/client/scheme.go`

**Change**: Register `volcanoschedulingv1beta1` in the global `Scheme` so that:
- `controllerutil.CreateOrPatch` can construct the correct GVK for Volcano PodGroup
- Fake clients used in tests can access Volcano PodGroup objects

```go
// Add
volcanoschedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

// Add to localSchemeBuilder
volcanoschedulingv1beta1.AddToScheme,
```

---

### 3. `operator/api/config/v1alpha1/types.go`

**Change 1**: Add scheduler name constant and update supported list.

```go
// Add constant
SchedulerNameVolcano SchedulerName = "volcano"

// Add volcano to SupportedSchedulerNames
SupportedSchedulerNames = []SchedulerName{SchedulerNameKai, SchedulerNameKube, SchedulerNameVolcano}
```

**Change 2**: Add `volcano` to kubebuilder enum annotation for `SchedulerProfile.Name`.

```go
// +kubebuilder:validation:Enum=kai-scheduler;kube-scheduler;volcano
```

**Change 3**: Add `VolcanoSchedulerConfiguration` config type.

```go
type VolcanoSchedulerConfiguration struct {
    // Queue is the Volcano Queue name for submitting PodGroups, defaults to "default".
    Queue string `json:"queue,omitempty"`

    // TopologyKeyToTier maps node label keys to Volcano HyperNode tier numbers.
    // Must be configured when enabling topology-aware scheduling. Tier numbers must match
    // the HyperNodes created by cluster administrators.
    // Example: {"kubernetes.io/hostname": 1, "topology.kubernetes.io/rack": 2}
    TopologyKeyToTier map[string]int `json:"topologyKeyToTier,omitempty"`
}
```

---

### 4. `operator/api/config/v1alpha1/zz_generated.deepcopy.go`

**Change**: Add deepcopy methods for `VolcanoSchedulerConfiguration`.

`TopologyKeyToTier` is `map[string]int` which cannot be directly value-copied, needs manual iteration:

```go
func (in *VolcanoSchedulerConfiguration) DeepCopyInto(out *VolcanoSchedulerConfiguration) {
    *out = *in
    if in.TopologyKeyToTier != nil {
        in, out := &in.TopologyKeyToTier, &out.TopologyKeyToTier
        *out = make(map[string]int, len(*in))
        for key, val := range *in {
            (*out)[key] = val
        }
    }
}
```

---

### 5. `operator/internal/schedulerbackend/volcano/backend.go` (new file)

Core implementation file.

#### Backend Struct

```go
type Backend struct {
    client        client.Client
    scheme        *runtime.Scheme
    eventRecorder record.EventRecorder
    profile       configv1alpha1.SchedulerProfile
    queue         string          // Volcano queue, parsed from config, defaults to "default"
    keyToTier     map[string]int  // topology key → HyperNode tier mapping
}
```

#### `New()` — Constructor

Deserializes `VolcanoSchedulerConfiguration` from `profile.Config` (`runtime.RawExtension`),
extracts `Queue` and `TopologyKeyToTier`. Uses defaults (queue = "default", no topology mapping) if Config is empty or parsing fails.

#### `SyncPodGang()` — Core Sync Logic

1. Construct a `PodGroup` skeleton with the same name and namespace as PodGang
2. Call `controllerutil.CreateOrPatch` for idempotent create or update
3. In the mutate function:
   - Set OwnerReference (`SetControllerReference`) so PodGroup is GC'd with PodGang
   - Sum all `PodGroup.MinReplicas` to get `spec.minMember`
   - Set `spec.queue` and `spec.priorityClassName`
   - Call `buildNetworkTopology()` to populate `spec.networkTopology` (may be nil)

#### `OnPodGangDelete()` — Deletion Cleanup

Explicitly delete Volcano PodGroup, use `client.IgnoreNotFound` for idempotency.
(Owner Reference mechanism also triggers GC; explicit deletion makes cleanup more timely)

#### `PreparePod()` — Pod Preparation

```go
pod.Spec.SchedulerName = "volcano"
pod.Annotations["scheduling.volcano.sh/group-name"] = podGangName
```

PodGang name is read from Pod's `grove.io/podgang` label. Skip annotation if label doesn't exist (graceful degradation).

#### `buildNetworkTopology()` — Topology Mapping

```
PodGang.Spec.TopologyConstraint.PackConstraint
  ├── Required = "topology.kubernetes.io/rack"
  │     → keyToTier["topology.kubernetes.io/rack"] = 2
  │     → NetworkTopologySpec{Mode: "hard", HighestTierAllowed: &2}
  │
  └── Preferred = "kubernetes.io/hostname" (only Preferred set)
        → keyToTier["kubernetes.io/hostname"] = 1
        → NetworkTopologySpec{Mode: "soft", HighestTierAllowed: &1}
```

Returns `nil` if `keyToTier` is empty or key is not found in map (no NetworkTopology, plain gang scheduling).

---

### 6. `operator/internal/schedulerbackend/manager.go`

**Change 1**: Add compile-time check.

```go
_ SchedBackend = (*volcano.Backend)(nil)
```

**Change 2**: Add volcano case in `newBackendForProfile()` switch.

```go
case configv1alpha1.SchedulerNameVolcano:
    b := volcano.New(cl, scheme, rec, p)
    if err := b.Init(); err != nil {
        return nil, err
    }
    return b, nil
```

---

### 7. `operator/internal/schedulerbackend/manager_test.go`

**Change**: Original test expecting "volcano" to return "not supported" error now expects success:

```go
// Before
{schedulerName: "volcano", wantErr: true, errContains: "not supported"}

// After
{schedulerName: configv1alpha1.SchedulerNameVolcano, wantErr: false, expectedName: "volcano"}
```

---

### 8. `operator/api/config/validation/validation_test.go`

**Changes**:
- Changed test that used "volcano" as unsupported profile to use "unknown-scheduler" (preserving test case semantics)
- Added "valid: volcano profile" test case to verify volcano is now a valid profile name

---

### 9. `operator/internal/schedulerbackend/volcano/backend_test.go` (new file)

10 test cases covering all major scenarios:

| Test Name | Verification |
|---|---|
| `TestBackend_Name` | Name() returns "volcano" |
| `TestBackend_PreparePod` | SchedulerName and group-name annotation set correctly |
| `TestBackend_PreparePod_NoLabel` | No panic when podgang label missing, annotation not set |
| `TestBackend_SyncPodGang_Create` | minMember=5 (3+2), default queue, priority, owner ref |
| `TestBackend_SyncPodGang_Update` | Idempotent update: existing minMember=1 → updated to 4 |
| `TestBackend_SyncPodGang_CustomQueue` | Custom queue parsed from profile.Config |
| `TestBackend_SyncPodGang_WithTopology_Required` | Required key → hard mode + tier=2 |
| `TestBackend_SyncPodGang_WithTopology_PreferredOnly` | Preferred key → soft mode + tier=1 |
| `TestBackend_SyncPodGang_NoTopologyMapping` | No keyToTier config → NetworkTopology=nil |
| `TestBackend_OnPodGangDelete` | PodGroup successfully deleted |
| `TestBackend_OnPodGangDelete_AlreadyGone` | No error when PodGroup doesn't exist (idempotent) |

---

## Usage

### OperatorConfiguration Example

**Minimal config (gang scheduling only, no topology):**

```yaml
scheduler:
  profiles:
  - name: volcano
  defaultProfileName: volcano
```

**Full config (with topology-aware scheduling):**

```yaml
scheduler:
  profiles:
  - name: volcano
    config:
      queue: gpu-training
      topologyKeyToTier:
        "kubernetes.io/hostname": 1
        "topology.kubernetes.io/rack": 2
        "topology.kubernetes.io/zone": 3
  defaultProfileName: volcano
```

### Prerequisites

1. Volcano is installed in the cluster (`volcano.sh` CRDs are registered)
2. Target Queue is created and in Open state
3. If enabling topology-aware scheduling, administrators must pre-create HyperNode CRs with tier numbers matching `topologyKeyToTier` configuration

### Scheduling Flow

```
User creates PodCliqueSet
    │
    ▼
Operator creates PodGang (with MinReplicas, TopologyConstraint)
    │
    ▼
Volcano Backend: SyncPodGang()
    ├── Create Volcano PodGroup (minMember, queue, networkTopology)
    └── PodGroup ownerRef → PodGang (automatic GC)
    │
    ▼
Operator creates Pods
    └── Volcano Backend: PreparePod()
        ├── Pod.Spec.SchedulerName = "volcano"
        └── Pod.Annotations["scheduling.volcano.sh/group-name"] = <podgang-name>
    │
    ▼
Volcano Scheduler reads PodGroup and Pods, performs gang scheduling
```

---

## Design Decisions

### Why explicit TopologyKeyToTier instead of auto-derivation?

**Option A**: Auto-derive tier from `ClusterTopology.spec.levels` order (reverse sort, narrowest = tier 1).

**Option B**: Explicitly configure key → tier mapping in `VolcanoSchedulerConfiguration`.

Chose Option B because:
- `ClusterTopology` may not exist (when TAS is not enabled)
- Auto-derivation relies on implicit convention between ClusterTopology level order and HyperNode tier numbers; any mismatch causes hard-to-debug issues
- Explicit configuration serves as documentation; administrators can immediately see key-to-tier mapping

### Why not manage HyperNodes?

Volcano's `HyperNode` represents actual hardware topology of the cluster (one HyperNode per rack). Creating them correctly requires knowledge of cluster physical structure. This is outside Grove Operator's responsibility scope and is better maintained by cluster administrators.

### Why use only Required when both Required and Preferred are set?

When both are set:
- `Required` is a hard constraint (must satisfy), maps to `mode=hard`
- `Preferred` is a soft preference within Required's scope

Volcano's `NetworkTopologySpec` only supports a single constraint (one mode + one tier), cannot express two-layer constraints simultaneously. Choosing Required because it's an inviolable hard constraint with stronger semantics than Preferred.

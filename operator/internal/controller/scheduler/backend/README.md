# Scheduler Backend Architecture

## Overview

The scheduler backend system provides a pluggable architecture for Grove to support multiple Kubernetes schedulers. Each backend is responsible for translating Grove's internal gang scheduling representation (`PodGangInfo`) into scheduler-specific Custom Resources.

**Design Inspired by**: [Kubernetes scheduler backend queue](https://github.com/kubernetes/kubernetes/tree/master/pkg/scheduler/backend/queue)

## Architecture

```
PodCliqueSet
     ↓
Backend Registry (auto-selects based on schedulerName)
     ↓
┌──────────────────┬────────────────────┬─────────────────────┐
│  Workload        │  PodGang          │  Koordinator        │
│  Backend         │  Backend          │  Backend            │
│                  │                   │                     │
│  schedulerName:  │  schedulerName:   │  schedulerName:     │
│  ""              │  "kai-scheduler"  │  "koord-scheduler"  │
│  "default"       │  "grove-*"        │                     │
│                  │                   │                     │
│  Output:         │  Output:          │  Output:            │
│  Workload        │  PodGang          │  PodGroup           │
│  (k8s.io)        │  (grove.io)       │  (koordinator.sh)   │
└──────────────────┴────────────────────┴─────────────────────┘
```

## Supported Backends

### 1. Workload Backend (Default Scheduler)
- **Scheduler**: kube-scheduler (Kubernetes 1.35+)
- **Matching Logic**: `schedulerName == ""` or `schedulerName == "default-scheduler"`
- **Output CR**: `scheduling.k8s.io/v1alpha1/Workload`
- **Scheduling Gate**: `scheduling.k8s.io/workload`
- **Use Case**: Standard Kubernetes gang scheduling using native Workload API

**Example:**
```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
spec:
  template:
    cliques:
    - spec:
        podSpec:
          schedulerName: ""  # or "default-scheduler"
```

### 2. PodGang Backend (KAI/Grove Scheduler)
- **Scheduler**: KAI scheduler / Grove scheduler
- **Matching Logic**: `schedulerName != "" && schedulerName != "default-scheduler"`
- **Output CR**: `scheduler.grove.io/v1alpha1/PodGang`
- **Scheduling Gate**: `grove.io/podgang`
- **Use Case**: Advanced gang scheduling with topology awareness, hierarchical scheduling, and network optimization

**Example:**
```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
spec:
  template:
    cliques:
    - spec:
        podSpec:
          schedulerName: "kai-scheduler"
```

### 3. Koordinator Backend
- **Scheduler**: Koordinator scheduler
- **Matching Logic**: `schedulerName == "koord-scheduler"` or contains `"koord"`
- **Output CR**: `scheduling.koordinator.sh/v1alpha1/PodGroup`
- **Scheduling Gate**: `scheduling.koordinator.sh/gang`
- **Use Case**: Integration with Koordinator's advanced scheduling features
- **Status**: Partially implemented (framework complete, needs full CR mapping)

**Example:**
```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
spec:
  template:
    cliques:
    - spec:
        podSpec:
          schedulerName: "koord-scheduler"
```

## Key Components

### SchedulerBackend Interface

All backends must implement this interface:

```go
type SchedulerBackend interface {
    // Name returns the unique name of this backend
    Name() string

    // Matches returns true if this backend should be used
    Matches(pcs *PodCliqueSet) bool

    // GetExistingResourceNames lists existing gang resources
    GetExistingResourceNames(ctx, logger, pcsObjMeta) ([]string, error)

    // Sync creates/updates/deletes gang scheduling resources
    Sync(ctx, logger, pcs) error

    // Delete removes all gang resources
    Delete(ctx, logger, pcsObjMeta) error

    // CheckGangReady checks if gang is ready for pod scheduling
    CheckGangReady(ctx, logger, pclq) (isReady bool, gangName string, error)

    // GetSchedulingGateName returns the scheduling gate name
    GetSchedulingGateName() string
}
```

### PodGangInfo (Internal Representation)

Common representation used by all backends:

```go
type PodGangInfo struct {
    Name                           string
    Namespace                      string
    PodGroups                      []PodGroupInfo
    TopologyConstraint             *TopologyConstraint
    TopologyConstraintGroupConfigs []TopologyConstraintGroupConfig
    PriorityClassName              string
    ReuseReservationRef            *NamespacedName
}
```

Each backend converts `PodGangInfo` to its scheduler-specific CR format.

### GangInfoBuilder

Builds `PodGangInfo` from Grove resources:

```go
builder := backend.NewGangInfoBuilder(client)
gangInfos, err := builder.BuildGangInfos(ctx, podCliqueSet)
```

### Registry

Auto-selects the appropriate backend based on schedulerName:

```go
// Auto-select backend
backend, err := backend.GetBackend(podCliqueSet, client, scheme, recorder)

// Or get specific backend by name
backend, err := backend.GetBackendByName("workload", client, scheme, recorder)
```

## Usage in Operator

### Component Adapter

The `ComponentAdapter` integrates backends into the existing component framework:

```go
// In component registry
import "github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend/adapter"

reg.Register(component.KindPodGang, adapter.NewComponentAdapter(client, scheme, recorder))
```

The adapter:
1. Auto-detects which backend to use based on schedulerName
2. Delegates all operations to the selected backend
3. Works seamlessly with existing component architecture

### Pod Scheduling Gates

Each backend specifies its scheduling gate name. The operator:
1. Adds the gate when creating Pods
2. Removes the gate when the gang is ready (based on backend's `CheckGangReady()`)

## Adding a New Backend

1. **Create backend package**:
   ```
   operator/internal/controller/scheduler/backend/myscheduler/backend.go
   ```

2. **Implement SchedulerBackend interface**:
   ```go
   type Backend struct {
       client client.Client
       scheme *runtime.Scheme
       eventRecorder record.EventRecorder
   }
   
   func (b *Backend) Matches(pcs *PodCliqueSet) bool {
       // Check if any clique uses your scheduler
       for _, clique := range pcs.Spec.Template.Cliques {
           if clique.Spec.PodSpec.SchedulerName == "my-scheduler" {
               return true
           }
       }
       return false
   }
   
   func (b *Backend) Sync(ctx, logger, pcs) error {
       // Convert PodGangInfo to your CR
       builder := backend.NewGangInfoBuilder(b.client)
       gangInfos, _ := builder.BuildGangInfos(ctx, pcs)
       
       for _, gangInfo := range gangInfos {
           myCR := b.ConvertToMyCR(gangInfo)
           // Create/update your CR
       }
   }
   
   // ... implement other methods
   ```

3. **Register backend in init()**:
   ```go
   func init() {
       backend.Register("myscheduler", &Factory{})
   }
   ```

4. **Import in main**:
   ```go
   import _ "github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend/myscheduler"
   ```

## Benefits

1. **Pluggable**: Easy to add support for new schedulers
2. **Consistent**: All backends use the same internal representation
3. **Maintainable**: Backend-specific logic is isolated
4. **Testable**: Each backend can be tested independently
5. **Backward Compatible**: Works with existing component architecture

## Future Enhancements

- [ ] Implement full Koordinator PodGroup conversion
- [ ] Add Volcano scheduler backend
- [ ] Add YUNIKORN scheduler backend
- [ ] Implement topology constraint conversion
- [ ] Add backend selection via annotations (override schedulerName matching)
- [ ] Add backend metrics and observability


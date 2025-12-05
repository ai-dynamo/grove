# Scheduler Backend Usage Guide

## Quick Start

### 1. Automatic Backend Selection

The operator automatically selects the appropriate backend based on the `schedulerName` in your PodSpec:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: my-app
spec:
  replicas: 3
  template:
    cliques:
    - name: workers
      spec:
        replicas: 4
        podSpec:
          schedulerName: "kai-scheduler"  # Uses PodGang backend
          containers:
          - name: worker
            image: my-worker:latest
```

### 2. Backend Selection Logic

```
schedulerName == "" or "default-scheduler"
    → Workload Backend
    → Creates: scheduling.k8s.io/v1alpha1/Workload
    → Gate: scheduling.k8s.io/workload

schedulerName == "kai-scheduler" or "grove-scheduler"
    → PodGang Backend  
    → Creates: scheduler.grove.io/v1alpha1/PodGang
    → Gate: grove.io/podgang

schedulerName == "koord-scheduler"
    → Koordinator Backend
    → Creates: scheduling.koordinator.sh/v1alpha1/PodGroup
    → Gate: scheduling.koordinator.sh/gang
```

## Integration with Operator Components

### Registering Backend Adapter

In your component registry:

```go
import (
    "github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend/adapter"
)

func CreateOperatorRegistry(mgr manager.Manager, eventRecorder record.EventRecorder) component.OperatorRegistry[v1alpha1.PodCliqueSet] {
    cl := mgr.GetClient()
    reg := component.NewOperatorRegistry[v1alpha1.PodCliqueSet]()
    
    // ... other components ...
    
    // Register backend adapter (replaces separate podgang/workload components)
    reg.Register(component.KindPodGang, adapter.NewComponentAdapter(cl, mgr.GetScheme(), eventRecorder))
    
    return reg
}
```

### Pod Scheduling Gate Management

When creating Pods, use the backend's scheduling gate:

```go
import "github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend"

func createPod(pcs *PodCliqueSet, podSpec corev1.PodSpec) *corev1.Pod {
    // Get appropriate backend
    be, _ := backend.GetBackend(pcs, client, scheme, recorder)
    
    // Create pod with scheduling gate
    pod := &corev1.Pod{
        Spec: podSpec,
    }
    pod.Spec.SchedulingGates = []corev1.PodSchedulingGate{
        {Name: be.GetSchedulingGateName()},
    }
    
    return pod
}
```

### Checking Gang Readiness

```go
func shouldRemoveGate(ctx context.Context, pcs *PodCliqueSet, pclq *PodClique) (bool, error) {
    // Get appropriate backend
    be, _ := backend.GetBackend(pcs, client, scheme, recorder)
    
    // Check if gang is ready
    isReady, gangName, err := be.CheckGangReady(ctx, logger, pclq)
    if err != nil {
        return false, err
    }
    
    if isReady {
        logger.Info("Gang is ready, can remove scheduling gate", "gang", gangName)
        return true, nil
    }
    
    return false, nil
}
```

## Implementing a Custom Backend

### 1. Create Backend Package

```go
// operator/internal/controller/scheduler/backend/myscheduler/backend.go
package myscheduler

import (
    "context"
    "github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend"
    // ... other imports
)

const (
    BackendName = "myscheduler"
    SchedulingGateName = "my.scheduler.io/gang"
)

type Backend struct {
    client client.Client
    scheme *runtime.Scheme
    eventRecorder record.EventRecorder
}

type Factory struct{}

func (f *Factory) CreateBackend(cl client.Client, scheme *runtime.Scheme, recorder record.EventRecorder) (backend.SchedulerBackend, error) {
    return &Backend{
        client: cl,
        scheme: scheme,
        eventRecorder: recorder,
    }, nil
}

func (b *Backend) Name() string {
    return BackendName
}

func (b *Backend) Matches(pcs *PodCliqueSet) bool {
    for _, clique := range pcs.Spec.Template.Cliques {
        if clique.Spec.PodSpec.SchedulerName == "my-scheduler" {
            return true
        }
    }
    return false
}

func (b *Backend) Sync(ctx context.Context, logger logr.Logger, pcs *PodCliqueSet) error {
    // Build gang info
    builder := backend.NewGangInfoBuilder(b.client)
    gangInfos, err := builder.BuildGangInfos(ctx, pcs)
    if err != nil {
        return err
    }
    
    // Convert to your scheduler's CR and create/update
    for _, gangInfo := range gangInfos {
        myCR := b.convertToMyCR(gangInfo)
        // Create or update myCR
    }
    
    return nil
}

// ... implement other required methods ...

func init() {
    backend.Register(BackendName, &Factory{})
}
```

### 2. Register Your Backend

Import your backend in `init.go`:

```go
// operator/internal/controller/scheduler/backend/init.go
import (
    _ "github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend/myscheduler"
)
```

### 3. Use Your Scheduler

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
spec:
  template:
    cliques:
    - spec:
        podSpec:
          schedulerName: "my-scheduler"  # Will use your backend
```

## Testing

### Unit Test Example

```go
func TestMyBackend(t *testing.T) {
    // Create test PodCliqueSet
    pcs := &v1alpha1.PodCliqueSet{
        Spec: v1alpha1.PodCliqueSetSpec{
            Template: v1alpha1.PodCliqueSetTemplate{
                Cliques: []v1alpha1.PodCliqueTemplate{
                    {
                        Spec: v1alpha1.PodCliqueTemplateSpec{
                            PodSpec: corev1.PodSpec{
                                SchedulerName: "my-scheduler",
                            },
                        },
                    },
                },
            },
        },
    }
    
    // Get backend
    be, err := backend.GetBackendByName("myscheduler", fakeClient, scheme, recorder)
    require.NoError(t, err)
    
    // Test matching
    assert.True(t, be.Matches(pcs))
    
    // Test sync
    err = be.Sync(context.Background(), logger, pcs)
    assert.NoError(t, err)
}
```

## Troubleshooting

### Backend Not Selected

**Problem**: Wrong backend is being used

**Solution**: Check schedulerName in your PodSpec. Use:
- `""` or `"default-scheduler"` for Workload backend
- `"kai-scheduler"` or `"grove-scheduler"` for PodGang backend  
- `"koord-scheduler"` for Koordinator backend

### Gang Not Getting Scheduled

**Problem**: Pods stuck with scheduling gates

**Solution**:
1. Check if gang CR was created: `kubectl get podgangs` or `kubectl get workloads`
2. Check gang CR status
3. Verify scheduler is running and has correct permissions
4. Check operator logs for gate removal logic

### Multiple Backends Match

**Problem**: More than one backend matches your PodCliqueSet

**Solution**: The first matching backend will be used. Ensure your `Matches()` logic is exclusive. Consider ordering in registration.

## Best Practices

1. **Consistent schedulerName**: Use the same schedulerName across all cliques in a PodCliqueSet
2. **Backend labels**: Backends automatically add labels to gang CRs for tracking
3. **Gate management**: Always use `be.GetSchedulingGateName()` rather than hardcoding gate names
4. **Error handling**: Handle backend selection errors gracefully
5. **Testing**: Test with different schedulerName values to ensure correct backend selection

## Migration Guide

### From Old Component Architecture

**Before:**
```go
reg.Register(component.KindPodGang, podgang.New(cl, scheme, recorder))
reg.Register(component.KindWorkload, workload.New(cl, scheme, recorder))
```

**After:**
```go
reg.Register(component.KindPodGang, adapter.NewComponentAdapter(cl, scheme, recorder))
// The adapter handles both PodGang and Workload automatically
```

### Benefits

- ✅ Single registration point for all gang scheduling
- ✅ Automatic scheduler detection
- ✅ Easier to add new scheduler support
- ✅ Cleaner separation of concerns
- ✅ Better testability


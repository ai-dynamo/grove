# Volcano Scheduler Backend

## Overview

This document describes the initial implementation of [Volcano](https://volcano.sh) scheduler backend support in Grove Operator.

Grove already has a scheduler backend framework defined in [GREP-375](../375-scheduler-backend-framework/README.md). This change adds Volcano as an optional scheduler backend without changing the existing PodCliqueSet, PodGang, or Pod lifecycle.

The current scope is intentionally narrow:

- support Volcano gang scheduling through `PodGroup`
- prepare Pods with `schedulerName: volcano`
- attach the official Volcano PodGroup annotation to Pods
- reject topology-aware scheduling constraints for the Volcano backend

This proposal documents the implementation that is now merged into the operator codebase.

## Background

All scheduler backends in Grove implement the same interface:

```go
type Backend interface {
	Name() string
	Init() error
	SyncPodGang(ctx context.Context, podGang *PodGang) error
	OnPodGangDelete(ctx context.Context, podGang *PodGang) error
	PreparePod(pod *corev1.Pod)
	ValidatePodCliqueSet(ctx context.Context, pcs *PodCliqueSet) error
}
```

This lets Grove keep its controller flow unchanged while plugging in backend-specific behavior for:

- PodGang synchronization
- Pod preparation before creation
- backend-specific PodCliqueSet validation

## Volcano Concepts

### PodGroup

Volcano uses the `PodGroup` CR (`scheduling.volcano.sh/v1beta1`) to represent a gang of Pods that should be scheduled together.

Minimal example:

```yaml
apiVersion: scheduling.volcano.sh/v1beta1
kind: PodGroup
metadata:
  name: training-job
  namespace: default
spec:
  minMember: 8
  queue: default
  priorityClassName: high-priority
```

### Pod Annotation

Volcano uses a Pod annotation to associate a Pod with its PodGroup:

```text
scheduling.k8s.io/group-name: <podgroup-name>
```

Grove writes this annotation during Pod preparation.

## Scope

### Supported

- Volcano scheduler profile registration in operator config
- PodGang to PodGroup synchronization
- Pod `schedulerName` preparation
- Pod Volcano group annotation injection
- queue configuration and validation

### Not Supported Yet

- topology-aware scheduling with Volcano
- HyperNode integration
- queue lifecycle management
- PodGroup status propagation back into Grove-specific status fields

If a PodCliqueSet uses `topologyConstraint` with the Volcano backend, the request is rejected during validation.

## Configuration

### Scheduler Profile

The operator adds a new scheduler profile name:

```yaml
scheduler:
  profiles:
  - name: volcano
    config:
      queue: default
```

`VolcanoSchedulerConfiguration` currently contains:

```go
type VolcanoSchedulerConfiguration struct {
	Queue string `json:"queue,omitempty"`
}
```

Behavior:

- if `queue` is omitted, it defaults to `default`
- validation requires the final queue value to be non-empty

## Design

### Mapping: PodGang -> PodGroup

Grove maps a PodGang into a Volcano PodGroup as follows:

| Grove PodGang | Volcano PodGroup | Notes |
|---|---|---|
| `metadata.name` | `metadata.name` | Same name |
| `metadata.namespace` | `metadata.namespace` | Same namespace |
| `sum(spec.podGroups[].minReplicas)` | `spec.minMember` | Gang minimum |
| `spec.priorityClassName` | `spec.priorityClassName` | Direct mapping |
| `scheduler.profiles[].config.queue` | `spec.queue` | Defaults to `default` |

The operator also sets an owner reference from PodGroup to PodGang so normal Kubernetes garbage collection can clean up the Volcano resource when the Grove resource is deleted.

### Pod Preparation

When the Volcano backend is selected, Grove mutates the Pod before creation:

```go
pod.Spec.SchedulerName = "volcano"
pod.Annotations["scheduling.k8s.io/group-name"] = podGangName
```

The PodGroup name is the same as the Grove PodGang name.

## Code Changes

### `operator/go.mod`

Adds Volcano API dependency:

```text
volcano.sh/apis v1.13.2
```

### `operator/internal/client/scheme.go`

Registers Volcano scheduling types into the shared runtime scheme:

- `volcano.sh/apis/pkg/apis/scheduling/v1beta1`

This is required for:

- controller-runtime client operations
- `CreateOrPatch`
- fake client tests

### `operator/api/config/v1alpha1/types.go`

Adds:

- `SchedulerNameVolcano`
- `volcano` to supported scheduler names
- `VolcanoSchedulerConfiguration`

### `operator/api/config/v1alpha1/defaults.go`

Adds Volcano defaulting behavior:

- if Volcano profile config is empty, default `queue` to `default`

### `operator/api/config/validation/validation.go`

Adds Volcano-specific validation:

- Volcano is accepted as a valid scheduler profile name
- Volcano config is decoded and validated
- queue must be non-empty after defaulting

### `operator/internal/scheduler/manager/manager.go`

Registers the Volcano backend in the scheduler backend factory.

### `operator/internal/scheduler/volcano/backend.go`

Implements the Volcano backend.

Key responsibilities:

- `Name()` returns `volcano`
- `SyncPodGang()` creates or patches a Volcano PodGroup
- `OnPodGangDelete()` relies on owner reference based cleanup
- `PreparePod()` sets `schedulerName` and PodGroup annotation
- `ValidatePodCliqueSet()` rejects `topologyConstraint`

### `operator/charts/templates/clusterrole.yaml`

Adds RBAC for Volcano PodGroup resources.

## Validation Behavior

The initial Volcano backend intentionally refuses topology-aware scheduling constraints.

Examples of rejected cases:

- `spec.template.topologyConstraint`
- `spec.template.cliques[i].topologyConstraint`
- `spec.template.podCliqueScalingGroupConfigs[i].topologyConstraint`

Typical validation error:

```text
volcano scheduler backend does not support topologyConstraint on PodCliqueSet
```

This keeps the first Volcano integration limited to gang scheduling and avoids implying support for placement semantics that are not implemented yet.

## Testing

### Unit Tests

The Volcano backend includes unit coverage for:

- backend name
- Pod preparation
- PodGroup creation
- PodGroup update
- queue handling
- topologyConstraint rejection

### End-to-End Validation

The implementation was also validated in a QA cluster with Volcano installed.

Positive case:

- create a PodCliqueSet with `schedulerName: volcano`
- verify Grove creates PodGang
- verify operator creates Volcano PodGroup
- verify Pods are created with:
  - `schedulerName=volcano`
  - `scheduling.k8s.io/group-name=<podgang-name>`
- verify Pods reach `Running`

Negative case:

- create a PodCliqueSet with `schedulerName: volcano` and `topologyConstraint`
- verify admission rejects the request

## Future Work

Potential follow-up work includes:

- Volcano topology-aware scheduling support
- richer PodGroup status integration
- optional queue existence checks during initialization
- additional end-to-end coverage in automated CI

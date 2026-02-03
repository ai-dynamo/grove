# GREP-375: Scheduler Backend Framework

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Third-Party Scheduler Integration](#story-1-third-party-scheduler-integration)
    - [Story 2: Multi-Cluster Deployment with Different Schedulers](#story-2-multi-cluster-deployment-with-different-schedulers)
    - [Story 3: Scheduler Migration Path](#story-3-scheduler-migration-path)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
    - [Single Backend Per Deployment](#single-backend-per-deployment)
    - [Backend Implementation Responsibility](#backend-implementation-responsibility)
    - [Migration Complexity](#migration-complexity)
    - [Backend API Stability](#backend-api-stability)
- [Design Details](#design-details)
  - [Architecture Overview](#architecture-overview)
    - [Layer 1: Configuration Layer](#layer-1-configuration-layer)
    - [Layer 2: Grove Control Plane](#layer-2-grove-control-plane)
    - [Layer 3: Scheduler Backend Layer](#layer-3-scheduler-backend-layer)
    - [Layer 4: Scheduler Layer](#layer-4-scheduler-layer)
  - [Backend Interface Definition](#backend-interface-definition)
  - [Backend Manager](#backend-manager)
  - [OperatorConfiguration Extension](#operatorconfiguration-extension)
  - [KAI Backend Implementation](#kai-backend-implementation)
  - [Kube Backend Implementation (Default Scheduler)](#kube-backend-implementation-default-scheduler)
  - [PodGang Lifecycle Changes](#podgang-lifecycle-changes)
    - [Previous Flow (Before Framework):](#previous-flow-before-framework)
    - [New Flow (With Framework):](#new-flow-with-framework)
    - [New PodGang Status Condition](#new-podgang-status-condition)
  - [Backend Controller](#backend-controller)
  - [Pod Template Mutation](#pod-template-mutation)
  - [Monitoring](#monitoring)
    - [Status Conditions](#status-conditions)
  - [Dependencies](#dependencies)
    - [Required Dependencies](#required-dependencies)
    - [Optional Dependencies](#optional-dependencies)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [E2E Tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
- [Implementation History](#implementation-history)
- [Alternatives](#alternatives)
  - [Alternative 1: Direct Scheduler Integration (Status Quo)](#alternative-1-direct-scheduler-integration-status-quo)
  - [Glossary](#glossary)
<!-- /toc -->

<!--
Include a table of contents as it helps to navigate easily in the document.

Ensure the TOC is wrapped with
   <code>&lt;!-- toc --&gt;&lt;!-- /toc --&gt;</code>
tags, and then generate by invoking the make target `update-toc`.

-->

## Summary

Grove's scheduler API is currently integrated with the KAI scheduler as the only advanced AI scheduler that supports hierarchical gang-scheduling and topology-aware scheduling. While any custom scheduler can integrate with Grove's PodGang scheduling API, the onus remains on the external scheduler to do the heavy lifting of the integration effort. It also becomes difficult to add any scheduler specific logic from the operator. This proposal introduces a Scheduler Backend Framework that standardizes and simplifies the process of adding new scheduler support directly into Grove, making it easier to handle scheduler specific logic in its own backend.

## Motivation
Grove's unified API for AI workloads--training, inference, agents, etc.--allows the Grove operator to realize a workload's scheduling constraints through the PodGang API. Since the PodGang API is specific to Grove, it needs to be translated to constraints that are specific to the backend scheduler. While it is possible for each scheduler to support Grove independently by implementing its own translation layer, it will likely introduce maintenance issues over time as Grove keep evolving its scheduler constraint generation capabilities.
Many scenarios and customers today use different schedulers, including the Kubernetes default scheduler and schedulers built on the scheduler plugin framework. These schedulers require appropriate support from Grove, especially as the Kubernetes community continues to improve AI workload scheduling capabilities, including gang scheduling and topology-aware scheduling (TAS). Even the KAI scheduler required modifications to support Grove's PodGang API. 

The current tight coupling between Grove and specific scheduler implementations creates several challenges:

* **High Integration Cost**: Adding support for a new scheduler requires extensive modifications across Grove's codebase, touching multiple components and requiring deep knowledge of both Grove and the target scheduler's internals.
* **Maintenance Burden**: Each scheduler integration introduces scheduler-specific code paths that must be maintained independently, increasing complexity and the risk of regressions.
* **Limited Extensibility**: The lack of a standardized interface makes it difficult for third-party scheduler developers to integrate with Grove without modifying Grove's core code.
* **Scheduler Vendor Lock-in**: Users who want to switch schedulers face significant migration challenges due to scheduler-specific implementations.

Introducing a Scheduler Backend Framework addresses these challenges by providing:

* **Standardized Integration**: A well-defined interface and abstraction layer that makes adding a new scheduler backend easier and more standardized.
* **Reduced Invasiveness**: Minimal modifications to existing scheduler implementations, allowing schedulers to integrate with Grove through a plugin-like architecture.
* **Improved Scheduling Flow**: A more efficient and streamlined scheduling workflow that clearly separates concerns between Grove's workload management and scheduler-specific placement decisions.
* **Future-Proof Architecture**: A foundation that can adapt to emerging scheduling requirements and new scheduler implementations in the Kubernetes ecosystem.

In summary, refining Grove and introducing a Scheduler Backend Framework is both an urgent and inevitable improvement that will ensure Grove's long-term viability and extensibility in the evolving Kubernetes scheduling landscape.

### Goals

* **Define Scheduler Backend Interface**: Introduce a well-defined Go interface that abstracts scheduler-specific operations, enabling Grove to work with multiple scheduler backends through a standardized contract.
* **Refine PodGang Lifecycle Management**: Optimize the PodGang creation and update workflow to integrate with the Scheduler Backend Framework, allowing scheduler backends to customize pod specifications during reconciliation.
* **Enable Custom Resource Management**: Provide interfaces that allow Scheduler Backends to create, update, and delete their own custom resources in response to PodGang lifecycle events (create, update, delete, status changes).
* **Simplify User Experience**: Allow users to configure their preferred scheduler backend during Grove installation via OperatorConfiguration, eliminating the need to specify schedulerName in every pod specification.
* **Maintain Backward Compatibility**: Ensure existing KAI scheduler deployments continue to work without any changes.
* **Support Dynamic Backend Selection**: Enable Grove to determine which scheduler backend to use based on configuration, with clear mechanisms for backend registration and initialization.

### Non-Goals

* **Implement Additional Scheduler Backends**: Adding support for the default Kubernetes scheduler or other schedulers beyond KAI is explicitly out of scope for this initial framework implementation. The community support for gang scheduling and topology-aware scheduling in the default scheduler is still evolving. Support for additional schedulers will be added in subsequent PRs once the framework is established.
* **Change PodGang API Semantics**: PodGang remains Grove's primary scheduler API. This proposal does not alter the fundamental contract or semantics of the PodGang API, only how it interfaces with scheduler backends.
* **Remove Existing KAI Scheduler Modifications**: The current modifications made to KAI scheduler to support PodGang will remain. This proposal does not aim to revert or remove those changes, but rather to provide a framework that could reduce such modifications for future schedulers.
* **Extract PodGang Reconciler**: Moving the PodGang reconciliation logic from the PodCliqueSet reconciler into an independent reconciler is out of scope. The current reconciliation architecture will be maintained.
* **Multi-Scheduler Support**: Running multiple different schedulers simultaneously within a single Grove installation is not supported in this iteration. Users can only configure one scheduler backend per Grove deployment.
* **Scheduler Performance Optimization**: This proposal focuses on extensibility and maintainability, not on optimizing the performance characteristics of any particular scheduler implementation.


## Proposal

The Scheduler Backend Framework introduces a plugin-like architecture that decouples Grove's workload management from scheduler-specific implementations. The framework consists of three main components:

1. **Backend Interface**: A Go interface defining the contract between Grove and scheduler backends
2. **Backend Registry**: A registration mechanism allowing backends to register themselves during initialization
3. **Backend Lifecycle Hooks**: Well-defined points in the PodGang lifecycle where backends can inject custom logic

The framework follows a provider pattern where:
- Grove manages the high-level workflow and PodGang lifecycle
- Scheduler backends implement the interface to provide scheduler-specific behavior
- The operator configuration determines which backend is active at runtime

### User Stories

#### Story 1: Third-Party Scheduler Integration

As a third-party scheduler developer, I want to integrate my custom gang scheduler with Grove without modifying Grove's core codebase. The Scheduler Backend Framework should provide clear interfaces and documentation that allow me to implement a backend plugin for my scheduler, register it with Grove, and have Grove automatically use my scheduler for workload placement decisions.

#### Story 2: Multi-Cluster Deployment with Different Schedulers

As a platform engineer managing multiple Kubernetes clusters, I want to deploy Grove across clusters that use different schedulers (e.g., KAI in production clusters, default scheduler in development clusters). The framework should allow me to configure the appropriate scheduler backend for each cluster through OperatorConfiguration without changing workload specifications or Grove's deployment manifests.

#### Story 3: Scheduler Migration Path

As a cluster administrator, I want to migrate from one scheduler to another (e.g., from a custom scheduler to KAI or vice versa) without significant disruption. The Scheduler Backend Framework should provide a clear migration path where I can update the OperatorConfiguration, restart Grove, and have new workloads use the new scheduler while existing workloads continue running.

### Limitations/Risks & Mitigations

#### Single Backend Per Deployment in Phase1

**Limitation**: Grove can only be configured with one scheduler backend per deployment. Users cannot mix schedulers for different workloads within the same Grove installation.

**Mitigation**: This is acceptable for most use cases as clusters typically standardize on a single scheduler. Users requiring multiple schedulers can run separate Grove installations with different configurations in different clusters.

#### Backend Implementation Responsibility

**Risk**: Scheduler backend implementations may introduce bugs or incompatibilities that affect Grove's functionality.

**Mitigation**: 
- Provide comprehensive interface documentation and reference implementations
- Define clear contracts and invariants that backends must maintain
- Implement validation in Grove to detect and reject invalid backend behaviors
- Provide a testing framework for backend developers to validate their implementations

#### Migration Complexity

**Risk**: Migrating between scheduler backends may require workload redeployment and could cause temporary disruption.

**Mitigation**: 
- Document clear migration procedures and best practices
- Provide validation tools to test backend configurations before applying them
- Recommend blue-green deployment strategies for production migrations

#### Backend API Stability

**Risk**: Changes to the backend interface in future Grove versions could break existing backend implementations.

**Mitigation**:
- Follow semantic versioning for backend interfaces
- Maintain backward compatibility within major versions
- Provide deprecation notices and migration guides for interface changes
- Consider versioned interfaces if breaking changes are necessary

## Design Details

### Architecture Overview

The Scheduler Backend Framework introduces a clean separation between Grove's control plane logic and scheduler-specific implementations. The architecture is organized into four distinct layers:

<img src="assets/scheduler-backend-framework.png" alt="scheduler-backend-architecture" style="zoom:50%;" />

#### Layer 1: Configuration Layer
User-facing configuration that defines which scheduler backend to use:
- **OperatorConfiguration**: Kubernetes CR with `schedulerName` field
- **Helm Values**: Deployment-time configuration (`config.schedulerName`)

#### Layer 2: Grove Control Plane
Core controllers managing Grove workload lifecycle:
- **PodCliqueSet Controller**: Creates PodGang resources, fills PodReferences
- **Backend Controller**: Watches PodGang, calls `SyncPodGang()` to create scheduler CRs
- **PodClique Controller**: Creates Pods, calls `PreparePod()` to configure scheduler settings
- **Resources**: PodGang (with Initialized condition) and Pod (with schedulerName/gates)

#### Layer 3: Scheduler Backend Layer
Abstraction layer bridging Grove and specific schedulers:
- **Backend Manager**: Singleton that initializes and provides access to active backend
- **KAI Backend**: Implementation for KAI scheduler (creates PodGroup CRs in future)
- **Kube Backend**: Minimal implementation for default scheduler (no custom CRs)

#### Layer 4: Scheduler Layer
Kubernetes schedulers that actually place pods:
- **KAI Scheduler**: Gang scheduling with topology awareness
- **Kube Scheduler**: Default Kubernetes scheduler
- **KAI PodGroup**: Custom resource consumed by KAI scheduler (future phase)

**Key Data Flow:**
1. Operator startup → `Initialize(schedulerName)` → Backend Manager initializes backend
2. PodCliqueSet Controller creates PodGang with `Initialized=False` → Backend Controller watches → `SyncPodGang()` creates scheduler CRs
3. PodClique Controller creates Pods → `PreparePod()` sets schedulerName and annotations
4. PodCliqueSet fills PodReferences → Sets `Initialized=True` → Gates removed → Scheduler places pods

### Backend Interface Definition

The core of the framework is the `SchedulerBackend` interface, which defines all operations that a scheduler backend must implement. The interface is intentionally simple and focused:

```go

// SchedulerBackend defines the interface that different scheduler backends must implement.
//
// Architecture: Backend converts PodGang to scheduler-specific CR (PodGroup/Workload/etc)
// and prepares Pods with scheduler-specific configurations.
type SchedulerBackend interface {
	// Name is a unique name of the scheduler backend.
	// Used for logging and identification purposes.
	Name() string

	// Init provides a hook to initialize/setup one-time scheduler resources,
	// called at the startup of Grove operator.
	// Backends can perform tasks such as:
	// - Creating global custom resources required by the scheduler
	// - Validating scheduler availability and configuration
	// - Setting up any initial state
	Init() error

	// SyncPodGang synchronizes (creates/updates) scheduler specific resources for a PodGang
	// reacting to a creation or update of a PodGang resource.
	// This is called by the Backend Controller when PodGang spec changes.
	// Backends should:
	// - Create scheduler-specific custom resources (e.g., PodGroup, Workload)
	// - Update existing resources if the PodGang spec changed
	// - Use owner references to enable automatic cleanup
	SyncPodGang(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error

	// OnPodGangDelete cleans up scheduler specific resources for the given PodGang.
	// This is called when a PodGang is deleted.
	// Note: If using owner references, cleanup is automatic and this can be a no-op.
	OnPodGangDelete(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error

	// PreparePod adds scheduler backend specific configuration to the given Pod object
	// prior to its creation. This includes setting schedulerName,
	// annotations, etc.
	// This is called during Pod creation in the PodClique controller.
	PreparePod(pod *corev1.Pod)
}
```

**Key Design Decisions:**

1. **Simple Interface**: Only 5 methods, focusing on essential operations
2. **No Context in PreparePod**: Pod preparation is synchronous and doesn't need async operations
3. **Single PodGang Hook**: `SyncPodGang` handles both create and update, simplifying the interface

### Backend Manager

The manager handles backend initialization and provides global access to the active backend instance using a singleton pattern:

```go

// Initialize creates the global backend instance based on schedulerName.
// This should be called once during operator startup before controllers start.
// Supported scheduler names: "kai-scheduler", "default-scheduler"
func Initialize(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, schedulerName string) error {
	var initErr error
	initOnce.Do(func() {
		// Default to "default-scheduler" if not specified
		if schedulerName == "" {
			schedulerName = "default-scheduler"
		}

		// Create the appropriate backend based on scheduler name
		switch schedulerName {
		case "kai-scheduler":
			globalBackend = kai.New(client, scheme, eventRecorder, schedulerName)

		case "default-scheduler":
			globalBackend = kube.New(client, scheme, eventRecorder, schedulerName)

		// Future backends - uncomment and implement as needed:
		// case "volcano":
		//     globalBackend = volcano.New(client, scheme, eventRecorder, schedulerName)

		default:
			initErr = fmt.Errorf("unsupported scheduler %q (supported: kai-scheduler, default-scheduler)", schedulerName)
			return
		}

		// Initialize the backend
		if err := globalBackend.Init(); err != nil {
			initErr = fmt.Errorf("failed to initialize backend: %w", err)
			globalBackend = nil
			return
		}
	})
	return initErr
}

// Get returns the global backend instance.
// Returns nil if not initialized (caller should check).
func Get() SchedulerBackend {
	return globalBackend
}

// PreparePod is a convenience function that calls PreparePod on the global backend.
func PreparePod(pod *corev1.Pod) error {
	backend := Get()
	if backend == nil {
		return fmt.Errorf("backend not initialized")
	}
	backend.PreparePod(pod)
	return nil
}
```

**Design Rationale:**

- **Singleton Pattern**: Simpler than a full registry for the single-backend-per-cluster use case
- **Switch-Case Selection**: Clear and explicit backend selection, easy to add new backends
- **Initialization Safety**: `sync.Once` ensures thread-safe single initialization
- **Explicit Backend Imports**: All backends are compiled in, making dependencies clear

### OperatorConfiguration Extension

The OperatorConfiguration is extended with a simple `schedulerName` field to select the backend:

```go
// OperatorConfiguration defines the configuration for the Grove operator.
type OperatorConfiguration struct {
	// ... existing fields (Controllers, LogLevel, etc.) ...
	
	// SchedulerName is the name of the scheduler backend with which this instance of Grove operator will run.
	// Valid values: "kai-scheduler" or "default-scheduler"
	// +required
	// +kubebuilder:validation:Enum=kai-scheduler;default-scheduler
	SchedulerName string `json:"schedulerName,omitempty"`
	
	// TopologyAwareScheduling configures TAS (existing field)
	TopologyAwareScheduling TopologyAwareSchedulingConfiguration `json:"topologyAwareScheduling"`
	
	// ... other existing fields ...
}
```

**Helm Chart Configuration:**

The Helm chart `values.yaml` exposes the scheduler configuration:

```yaml
config:
  controllers:
    podCliqueSet:
      concurrentSyncs: 3
    podClique:
      concurrentSyncs: 3
  # SchedulerName is the name of the scheduler backend
  # Valid values: "kai-scheduler" or "default-scheduler"
  # Default: "default-scheduler"
  schedulerName: "default-scheduler"
  logLevel: info
  logFormat: json
  topologyAwareScheduling:
    enabled: false
```

### KAI Backend Implementation

The KAI scheduler backend serves as the reference implementation:

```go
// Name returns the backend name.
func (b *Backend) Name() string {
    return BackendName
}

// Init initializes the KAI backend.
// For KAI backend, no special initialization is needed currently.
func (b *Backend) Init() error {
    return nil
}

// SyncPodGang converts PodGang to KAI PodGroup and synchronizes it.
// TODO: Currently disabled - will be implemented in phase 2.
// Phase 1: KAI scheduler still reads PodGang directly (existing behavior).
// Phase 2: Will convert PodGang to PodGroup (scheduling.run.ai/v2alpha2) for cleaner separation.
func (b *Backend) SyncPodGang(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
    // Phase 1: Skip PodGroup creation/update
    // Phase 2: Will convert PodGang to PodGroup and synchronize
    return nil
}

// OnPodGangDelete removes the PodGroup owned by this PodGang.
// TODO: Currently disabled - will be implemented in phase 2.
func (b *Backend) OnPodGangDelete(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
    // Phase 1: Skip PodGroup deletion
    // Phase 2: Will delete PodGroup when PodGang is deleted
    return nil
}

// PreparePod adds KAI scheduler-specific configuration to the Pod.
// This includes: labels and annotations for observability.
func (b *Backend) PreparePod(_ *corev1.Pod) {}
```

**Phase 1 vs Phase 2:**

- **Phase 1** (Current): KAI backend only implements `PreparePod`. KAI scheduler continues to read PodGang CRs directly.
- **Phase 2** (Future): KAI backend will implement `SyncPodGang` to create PodGroup CRs (scheduling.run.ai/v2alpha2), providing cleaner separation and allowing KAI scheduler modifications to be minimized.

### Kube Backend Implementation (Default Scheduler)

The Kube backend is a minimal implementation for the Kubernetes default scheduler:

```go
// Name returns the backend name.
func (b *Backend) Name() string {
	return BackendName
}

// Init initializes the Kube backend.
// For Kube backend, no special initialization is needed.
func (b *Backend) Init() error {
	return nil
}

// SyncPodGang synchronizes PodGang resources.
// For default kube scheduler, no additional resources are needed.
// Future: May create Workload CRs when KEP-4817 support is added.
func (b *Backend) SyncPodGang(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	// No-op: default kube scheduler doesn't need any custom resources yet
	// Future: Will create Workload CRs for gang scheduling support
	return nil
}

// OnPodGangDelete handles PodGang deletion.
// For default kube scheduler, no cleanup is needed.
func (b *Backend) OnPodGangDelete(_ context.Context, _ *groveschedulerv1alpha1.PodGang) error {
	// No-op: default kube scheduler doesn't have any resources to clean up
	return nil
}

// PreparePod adds Kubernetes default scheduler-specific configuration to the Pod.
// This simply sets the scheduler name.
func (b *Backend) PreparePod(_ *corev1.Pod) {}
```

### PodGang Lifecycle Changes

**Critical Design Change**: The PodGang creation flow has been fundamentally changed to support scheduler backends that require resources to exist before pods are created (e.g., Workload API):

#### Previous Flow (Before Framework):
1. Pods are created first
2. Wait for all pods to have back-references to PodGang
3. Create PodGang with complete PodReferences

#### New Flow (With Framework):
1. **Create PodGang early** with PodGroups having empty PodReferences and `Initialized=False`
2. **Backend Controller reacts** to PodGang creation and calls `SyncPodGang` to create scheduler-specific resources
3. **Pods are created** (with scheduling gates blocking actual scheduling)
4. **Operator fills PodReferences** once all pods exist, and sets `Initialized=True`
5. **PodClique Controller observes** `Initialized=True` and removes scheduling gates to allow scheduling

#### New PodGang Status Condition

```go
const (
	// PodGangConditionTypeInitialized indicates that the PodGang has been populated
	// with pod references and pods can lift scheduling gates.
	PodGangConditionTypeInitialized PodGangConditionType = "Initialized"
)
```

We introduce Initialized as new PodGang Status Condition to signal that:
- All expected pods have been created
- PodGang.Spec.PodGroups[].PodReferences have been populated
- Pods can now lift their scheduling gates and proceed with scheduling

### Backend Controller

A dedicated Backend Controller watches PodGang resources and invokes backend hooks:

```go
// BackendReconciler reconciles PodGang objects and converts them to scheduler-specific CRs.
type BackendReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Backend schedulerbackend.SchedulerBackend
}

// Reconcile processes PodGang changes and synchronizes to backend-specific CRs.
func (r *BackendReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Fetch the PodGang
	podGang := &groveschedulerv1alpha1.PodGang{}
	if err := r.Get(ctx, req.NamespacedName, podGang); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion
	if !podGang.DeletionTimestamp.IsZero() {
		if err := r.Backend.OnPodGangDelete(ctx, podGang); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Sync PodGang to backend-specific CR
	if err := r.Backend.SyncPodGang(ctx, podGang); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *BackendReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&groveschedulerv1alpha1.PodGang{}).
		WithEventFilter(podGangSpecChangePredicate()).
		Named(fmt.Sprintf("backend-%s", r.Backend.Name())).
		Complete(r)
}

// podGangSpecChangePredicate filters PodGang events to only process spec changes.
// Status-only updates (like Initialized condition) are ignored.
func podGangSpecChangePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool {
			return true // Always process creation events
		},
		DeleteFunc: func(_ event.DeleteEvent) bool {
			return true // Process deletion for cleanup
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Only process if generation changed (spec was modified)
			// Generation doesn't change for status-only updates
			return e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration()
		},
		GenericFunc: func(_ event.GenericEvent) bool {
			return false
		},
	}
}
```

### Pod Template Mutation

During pod creation, the backend mutates the pod specification in operator/internal/controller/podclique/components/pod/pod.go:

```go
// buildResource constructs a Pod resource from PodClique specifications, setting up metadata, labels, scheduling gates, and dependencies
func (r _resource) buildResource(pcs *grovecorev1alpha1.PodCliqueSet, pclq *grovecorev1alpha1.PodClique, podGangName string, pod *corev1.Pod, podIndex int) error {
	...

	pod.Spec.SchedulerName = schedulerbackend.Get().Name()

	// Use backend to prepare Pod spec based on scheduler requirements
	// This adds labels, annotations, etc.
	if err = schedulerbackend.PreparePod(pod); err != nil {
		return groveerr.WrapError(err,
			errCodeBuildPodResource,
			component.OperationSync,
			"failed to prepare pod spec with scheduler backend",
		)
	}
}
```

### Monitoring

#### Status Conditions

A new condition will be added to the Grove operator's status:

Condition: `Initialized`
Condition States:

| Status    | Reason                              | Description                                                  |
| --------- | ----------------------------------- | ------------------------------------------------------------ |
| `True`    | `AllPodsCreated`  | All pods have been created and references populated |
| `False`   | `PodsNotCreated` | Waiting for all pods to be created and wait for all pods references to be filled in PodGang|


### Dependencies

#### Required Dependencies

1. **Scheduler Implementation**: Each backend requires its corresponding scheduler to be deployed:
   - KAI Backend: [KAI Scheduler](https://github.com/NVIDIA/KAI-Scheduler) must be deployed
   - Default Backend (future): Standard Kubernetes scheduler with gang scheduling support

#### Optional Dependencies
1. **Backend-Specific CRDs**: Some backends may require additional CRDs to be installed:
   - KAI Backend: KAI PodGang CRD, Topology CRD (if TAS is enabled)
   - Future backends: Custom CRDs as defined by the scheduler implementation


### Test Plan

#### Unit Tests

Unit tests will be implemented for all framework related components:

**Backend Interface and Registry** (`operator/internal/controller/backend/`)
- Test backend registration (success, duplicate registration)
- Test backend retrieval (existing, non-existing)

**KAI Backend Implementation** (`operator/internal/controller/backend/kai/`)
- Test pod spec mutation
- Test configuration validation

**Controller Integration** (`operator/internal/controller/podcliqueset/`)
- Test PodGang creation with empty pods references
- Test PodGang update with filling pods references

**Controller Integration** (`operator/internal/controller/podclique/`)
- Test backend errors and failure handling
- Test pod creation with backend mutation
- Test lift schedulingGate after Initialized is true

**OperatorConfiguration** (`operator/api/config/v1alpha1/`)
- Test configuration validation
- Test backend configuration parsing
- Test default values
- Test invalid backend names


#### E2E Tests

All existing e2e tests should be passed based on all supported schedulers.


### Graduation Criteria

The Scheduler Backend Framework will follow a staged rollout approach:

#### Alpha
- Core backend interface defined and implemented
- Backend registry functional
- KAI backend fully implemented as reference
- Basic operator configuration support

#### Beta 
- Backend interface stabilized (no breaking changes expected)
- Documentation for third-party backend development
- At least one additional backend implemented (e.g., default scheduler support)

#### GA
- Backend interface is stable and versioned
- Multiple production deployments using the framework
- Comprehensive documentation and examples
- All tests passing consistently
- Performance optimizations complete
- Support for at least 2-3 different scheduler backends

## Implementation History
- **2026-01-27**: Initial GREP proposal created and submitted for review

## Alternatives

### Alternative 1: Direct Scheduler Integration (Status Quo)

**Description**: Continue the current approach where scheduler-specific logic is embedded directly in Grove's controllers without abstraction.

**Pros**:
- No additional abstraction layer overhead
- Direct control over scheduler integration
- Simpler codebase for single-scheduler scenarios

**Cons**:
- High maintenance burden as schedulers evolve
- Adding new schedulers requires invasive changes to Grove core
- Difficult for third-party schedulers to integrate
- Code becomes increasingly complex with each scheduler addition
- **Rejected**: Does not scale as Grove adoption grows across different scheduler ecosystems


### Glossary

- **Backend**: An implementation of the SchedulerBackend interface that integrates Grove with a specific scheduler
- **Backend Manager**: A singleton manager that initializes and provides access to the active scheduler backend
- **Backend Controller**: A dedicated controller that watches PodGang resources and invokes backend `SyncPodGang` and `OnPodGangDelete` methods
- **Scheduler Backend Framework**: The overall architecture and interfaces enabling pluggable scheduler support in Grove
- **Active Backend**: The currently configured and initialized scheduler backend in a Grove deployment
- **PodGang Initialized Condition**: A status condition indicating that PodGang has been populated with pod references and pods can proceed with scheduling
- **PreparePod**: The backend method called during pod creation to inject scheduler-specific configuration (schedulerName, annotations, etc.)
- **SyncPodGang**: The backend method called to create/update scheduler-specific custom resources in response to PodGang changes


# GREP-375: Scheduler Backend Framework

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Third-Party Scheduler Integration](#story-1-third-party-scheduler-integration)
    - [Story 2: Multi-Cluster Deployment with Different Schedulers](#story-2-multi-cluster-deployment-with-different-schedulers)
    - [Story 3: Scheduler Migration Path](#story-3-scheduler-migration-path)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
    - [Backend API Stability](#backend-api-stability)
    - [Scheduler Capability Mismatch](#scheduler-capability-mismatch)
  - [Requirements](#requirements)
- [Design Details](#design-details)
  - [Architecture Overview](#architecture-overview)
    - [Layer 1: Configuration Layer](#layer-1-configuration-layer)
    - [Layer 2: Grove Control Plane](#layer-2-grove-control-plane)
    - [Layer 3: Scheduler Backend Layer](#layer-3-scheduler-backend-layer)
    - [Layer 4: Scheduler Layer](#layer-4-scheduler-layer)
  - [Backend Interface Definition](#backend-interface-definition)
  - [Backend Manager](#backend-manager)
  - [OperatorConfiguration Extension](#operatorconfiguration-extension)
  - [PodGang Lifecycle Changes](#podgang-lifecycle-changes)
    - [Previous Flow (Before Framework):](#previous-flow-before-framework)
    - [New Flow (With Framework):](#new-flow-with-framework)
    - [New PodGang Status Condition](#new-podgang-status-condition)
  - [Backend Controller](#backend-controller)
  - [Monitoring](#monitoring)
    - [Status Conditions](#status-conditions)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [E2E Tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
- [Implementation History](#implementation-history)
- [Alternatives](#alternatives)
  - [Alternative 1: Direct Scheduler Integration](#alternative-1-direct-scheduler-integration)
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
Grove's unified API for AI workloads *training, inference, agents, etc.* allows the Grove operator to realize a workload's scheduling constraints through the PodGang API. Since the PodGang API is specific to Grove, it needs to be translated to constraints that are specific to the backend scheduler. While it is possible for each scheduler to support Grove independently by implementing its own translation layer, it will likely introduce maintenance issues over time as Grove keep evolving its scheduler constraint generation capabilities.

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
* **Simplify Configuration and Selection**: Allow admins to configure scheduler backends during Grove installation via OperatorConfiguration. By default, the configured default scheduler is used; otherwise the workload can specify a scheduler backend (e.g. via `schedulerName` on the PodCliqueSet or in the pod spec).
* **Support Dynamic Backend Selection**: Enable Grove to determine which scheduler backend to use based on OperatorConfiguration and the workload’s `schedulerName` (e.g. on PodCliqueSet or in the pod spec), with clear mechanisms for backend registration and initialization.
* **Support Multiple Scheduler Backends**: Provide built-in support for multiple scheduler backends including the Kubernetes kube-scheduler and KAI scheduler, with a clear path for adding additional third-party schedulers. The framework should enable easy integration of new schedulers as the community support for advanced features (gang scheduling, topology-aware scheduling) evolves.

## Proposal

The Scheduler Backend Framework introduces a plugin-like architecture that decouples Grove's workload management from scheduler-specific implementations. The framework consists of four main components:

1. **Interface**: A Go interface defining the contract between Grove and scheduler backends.
2. **Registry**: Mechanism for scheduler backends to register themselves during initialization.
3. **Lifecycle Hooks**: Well-defined points in the PodGang lifecycle where backend schedulers can inject custom logic.
4. **Operator Configuration**: OperatorConfiguration allows enabling multiple scheduler backends simultaneously. 

The framework follows an architecture where:
- The operator configuration determines which schedulers are active at runtime and which scheduler is marked as default. Grove support multiple active schedulers.
- Grove manages the high-level workflow and `PodGang` lifecycle
- Scheduler backend(s) implement the interface to provide scheduler-specific behavior

### User Stories

#### Story 1: Third-Party Scheduler Integration

As a third-party scheduler developer, I want to integrate my custom gang scheduler with Grove without modifying Grove's core codebase. The Scheduler Backend Framework should provide clear interfaces and documentation that allow me to implement a backend plugin for my scheduler, register it with Grove, and have Grove automatically use my scheduler for workload placement decisions.

#### Story 2: Multi-Cluster Deployment with Different Schedulers

As a platform engineer managing multiple Kubernetes clusters, I want to deploy Grove across clusters that use different schedulers (e.g., KAI in production clusters, default scheduler in development clusters). The framework should allow me to configure the appropriate scheduler backend for each cluster through OperatorConfiguration without changing workload specifications or Grove's deployment manifests.

#### Story 3: Scheduler Migration Path

As a cluster administrator, I want to migrate from one scheduler to another (e.g., from a custom scheduler to KAI or vice versa) without significant disruption. The Scheduler Backend Framework should provide a clear migration path where I can update the OperatorConfiguration, restart Grove to use a different scheduler in Grove.

### Limitations/Risks & Mitigations

#### Backend API Stability

**Risk**: Changes to the backend interface in future Grove versions could break existing backend implementations.

**Mitigation**:
- Follow semantic versioning for backend interfaces
- Maintain backward compatibility within major versions
- Provide deprecation notices and migration guides for interface changes
- Consider versioned interfaces if breaking changes are necessary

#### Scheduler Capability Mismatch

**Limitation**:
Different schedulers have varying capabilities, which may not align with the uniform capability set exposed through the PodGang API.

**Mitigation**:
- **For Missing Capabilities**: The framework provides a clear contract so that each scheduler backend can decide how to handle requested features it does not support:
  - **Fail submit**: The scheduler backend can reject PodCliqueSet when the missing capability is required for correctness or safety.
  - **Pass through**: The backend allows the request and the workload proceeds; the backend should ensure PodCliqueSet status is updated with conditions indicating which features are not supported (e.g. "GangScheduling requested but not supported by this backend—workload scheduled without gang guarantees"). This gives users explicit feedback while allowing best-effort scheduling.

  The choice between fail vs pass-through is entirely up to each backend implementation, so that different schedulers can adopt the policy that fits their semantics
- **For Additional Capabilities**: Document and track scheduler-specific capabilities during the integration process. If a scheduler provides valuable additional features that require configuration, evaluate adding new fields to the PodCliqueSet and PodGang APIs. These API extensions can be implemented incrementally in phases as new schedulers are integrated.

### Requirements

* Support multiple active scheduler backends via OperatorConfiguration.
* **kube-scheduler backend is always enabled and active**.
* Have a provision in OperatorConfiguration to set a default scheduler. Default this field to `kube-scheduler` if not set(since it is always active).
* When the scheduler-related configuration in OperatorConfiguration is empty (i.e. no additional scheduler backends are configured), only admit PodCliqueSet (PCS) resources whose `schedulerName` is empty or `"default-scheduler"` (kube-scheduler is still the only active backend in that case).
* Provide a config in OperatorConfiguration for each scheduler profile to decide its internal behavior. For example, if it is the kube-scheduler and GangScheduling has been disabled then do not create Workload Objects. 

## Design Details

### Architecture Overview

The Scheduler Backend Framework introduces a clean separation between Grove's control plane logic and scheduler-specific implementations. The architecture is organized into four distinct layers:

<img src="assets/scheduler-backend-framework.excalidraw.png" alt="scheduler-backend-framework-architecture" style="zoom:50%;" />

#### Layer 1: Configuration Layer
User-facing configuration that defines scheduler profiles (which backends are active and which is the default):
- **OperatorConfiguration**: Add additional scheduler-related configuration fields
- **Helm Values**: Deployment-time configuration (`config.scheduler`)

#### Layer 2: Grove Control Plane
Core controllers and webhooks managing Grove workload lifecycle:
- **PodCliqueSet Controller**: Creates PodGang resources, fills PodReferences
- **PodCliqueSet Validation Webhook**: Admits create/update of PodCliqueSet by calling the resolved backend's `ValidatePodCliqueSet()`
- **Backend Controller**: Watches PodGang, calls `SyncPodGang()` to create scheduler CRs
- **PodClique Controller**: Creates Pods, calls `PreparePod()` to configure scheduler settings
- **Resources**: PodGang (with Initialized condition) and Pod (with schedulerName/gates)

#### Layer 3: Scheduler Backend Layer
Abstraction layer bridging Grove and specific schedulers:
- **Backend Manager**: Singleton that initializes and provides access to active backend
- **KAI Backend**: Implementation for KAI scheduler (creates PodGroup CRs in future)
- **Kube Backend**: Minimal implementation for default kube-scheduler (no custom CRs)

#### Layer 4: Scheduler Layer
Kubernetes schedulers that actually place pods:
- **KAI Scheduler**: Gang scheduling with topology awareness
- **Kube Scheduler**: Default Kubernetes scheduler

**Key Data Flow:**

The framework orchestrates workload scheduling through a coordinated flow between layers:
1. **Backend Initialization**: Operator startup initializes the configured scheduler backend via Backend Manager
2. **PCS Admission**: On PodCliqueSet create or update, the validation webhook resolves the backend (from `schedulerName` or default) and calls `ValidatePodCliqueSet()`; the request is admitted or rejected accordingly
3. **PodGang Creation**: PodCliqueSet Controller creates PodGang resources with `Initialized=False` condition, triggering Backend Controller to create scheduler-specific resources via `SyncPodGang()`
4. **Pod Configuration**: PodClique Controller creates Pods with scheduling gates, calling `PreparePod()` to inject scheduler-specific settings
5. **Scheduling Activation**: Once all pods exist and PodReferences are populated, the `Initialized` condition is set to `True`, gates are removed, and scheduling begins

For detailed lifecycle flow, see [PodGang Lifecycle Changes](#podgang-lifecycle-changes).

### Backend Interface Definition

The core of the framework is the `SchedulerBackend` interface, which defines all operations that a scheduler backend must implement. The interface is intentionally simple and focused:

```go

// SchedulerBackend defines the interface that different scheduler backends must implement.
//
// Architecture: Backend validates PodCliqueSet at admission, converts
// PodGang to scheduler-specific CR (PodGroup/Workload/etc), and prepares Pods with scheduler-specific configurations.
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

	// ValidatePodCliqueSet validates a PodCliqueSet for this scheduler backend.
	// Called by the PodCliqueSet validation webhook (create and update). Backends can perform
	// scheduler-specific validation (e.g. that the requested schedulerName is enabled,
	// that requested features are supported, capacity limits). Return nil to allow, or an error
	// to reject the request (the error message is returned to the client).
	ValidatePodCliqueSet(ctx context.Context, pcs *groveschedulerv1alpha1.PodCliqueSet) error
}
```

**Future note:** Cluster topology (e.g. multi-cluster topology support) may require its own extension point or additional methods on this interface; the interface is expected to evolve as those needs are clarified.

### Backend Manager

The manager initializes scheduler backends: the kube-scheduler backend is always created and active; additional backends are created from OperatorConfiguration profiles. It provides access by name and a default:

```go

// Initialize creates and registers backend instances: kube-scheduler is always registered;
// backends named in config.Profiles are also registered (profiles can include or omit kube-scheduler).
// Called once during operator startup before controllers start.
func Initialize(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, config SchedulerConfiguration) error

// Get returns the backend for the given name. kube-scheduler is always available; other backends return nil if not enabled via a profile.
func Get(name string) SchedulerBackend

// GetDefault returns the default backend (the profile with default: true, or kube-scheduler if no profile is default).
func GetDefault() SchedulerBackend

```


### OperatorConfiguration Extension

The OperatorConfiguration is extended with a `scheduler` block: `profiles` lists scheduler profiles (each with `name`, optional `config`, and `default`). **The kube-scheduler backend is always enabled and active**, regardless of whether it appears in `profiles`. Kube-scheduler may also be listed in `profiles` with custom configuration (e.g. per-backend options), same as other backends.

Profiles are used to enable other backends (e.g. kai-scheduler), to give kube-scheduler optional custom config when listed, and to choose which profile is the default (exactly one profile should have `default: true`). The profile with `default: true` is used when a workload does not specify a scheduler. If no profile has `default: true`, kube-scheduler is used as the default (since it is always active).

**When `profiles` is empty:** If `scheduler.profiles` is explicitly set to an empty list, only the kube-scheduler backend is active (it is always active). In this case, only admit PodCliqueSet resources whose `schedulerName` is empty or `"default-scheduler"`; reject (do not admit) PCS that specify any other scheduler name.

**API shape:**

```go
// OperatorConfiguration defines the configuration for the Grove operator.
type OperatorConfiguration struct {
	// ... existing fields (Controllers, LogLevel, etc.) ...

	// Scheduler configures which scheduler backends are active and their per-backend options.
	Scheduler SchedulerConfiguration `json:"scheduler,omitempty"`

	// TopologyAwareScheduling configures TAS (existing field)
	TopologyAwareScheduling TopologyAwareSchedulingConfiguration `json:"topologyAwareScheduling"`

	// ... other existing fields ...
}

// SchedulerConfiguration configures scheduler profiles and which is the default.
type SchedulerConfiguration struct {
	// Profiles is the list of scheduler profiles. Each profile has a backend name, optional config, and whether it is the default.
	// The kube-scheduler backend is always enabled and active even if not listed here. Listing "kube-scheduler" in profiles
	// only adds a profile (e.g. with config like GangScheduling: false) and allows marking it as default.
	// Valid backend names: "kube-scheduler", "kai-scheduler". Exactly one profile should have default: true; if none, kube-scheduler is the default.
	// +optional
	Profiles []SchedulerProfile `json:"profiles,omitempty"`
}

// SchedulerProfile defines a scheduler backend profile with optional backend-specific config and default flag.
type SchedulerProfile struct {
	// Name is the scheduler backend name. Valid values: "kube-scheduler", "kai-scheduler".
	// +kubebuilder:validation:Enum=kai-scheduler;kube-scheduler
	Name string `json:"name"`

	// Config holds backend-specific options. The operator unmarshals it into the config type for this backend (see backend config types below).
	// +optional
	Config *runtime.RawExtension `json:"config,omitempty"`

	// Default indicates this profile is the default scheduler when a workload does not specify one. Exactly one profile should have default: true.
	// +optional
	Default bool `json:"default,omitempty"`
}
```

The operator unmarshals `SchedulerProfile.Config` into the config type for that backend name (see below). New backends register their config type without changing `SchedulerProfile`.

**Backend config types (per-backend)**

```go
// KubeSchedulerConfig is the config type for the "kube-scheduler" backend.
type KubeSchedulerConfig struct {
	// GangScheduling enables or disables gang-scheduling behavior (e.g. Workload API). If false, the backend does not create Workload objects.
	// +optional
	GangScheduling *bool `json:"GangScheduling,omitempty"`
}
```

**How `profiles` and `config` relate**

Each item in `scheduler.profiles` has a `name` (which backend), an optional `config` (that backend's options), and `default` (whether it is the default). The operator deserializes `config` into the type for that backend: for `"kube-scheduler"` → `KubeSchedulerConfig`. The config shape is thus per-backend.

**Configuration options and ways to configure**

| Goal | How to configure |
|------|-------------------|
| Use kube-scheduler only (no extra options) | Omit `scheduler` or set `scheduler.profiles: []`. kube-scheduler is always active and is the default. |
| Use kube-scheduler and tune its behavior | One profile: `name: "kube-scheduler"`, `config: { GangScheduling: true }`, `default: true`. |
| Use kai-scheduler as default | `scheduler.profiles: [{ name: "kai-scheduler", config: {}, default: true }]`. kube-scheduler remains active; default is kai-scheduler. |
| Multiple backends, kube-scheduler default | Add profiles for each; e.g. kube-scheduler (with optional config) and kai-scheduler; set `default: true` on kube-scheduler profile. |
| Multiple backends, kai-scheduler default | Add profiles for each; e.g. kube-scheduler (with optional config) and kai-scheduler; set `default: true` on kai-scheduler profile. |

**Example YAML (OperatorConfiguration / Helm `config`):**

```yaml
# --- Style 1: Omit scheduler (all defaults) ---
# Same as profiles: [{ name: "kube-scheduler", default: true }]
# (scheduler block can be omitted entirely)
```

```yaml
# --- Style 2: Single backend, no backend-specific options ---
scheduler:
  profiles:
    - name: "kube-scheduler"
      default: true
```

```yaml
# --- Style 3: Single backend with backend-specific options (config = KubeSchedulerConfig) ---
scheduler:
  profiles:
    - name: "kube-scheduler"
      config:
        GangScheduling: true
      default: true
```

```yaml
# --- Style 4: Multiple backends; default is kube-scheduler ---
scheduler:
  profiles:
    - name: "kube-scheduler"
      config:
        GangScheduling: false
      default: true
    - name: "kai-scheduler"
      config: {}
```

```yaml
# --- Style 5: Multiple backends; default is kai-scheduler ---
scheduler:
  profiles:
    - name: "kube-scheduler"
      config:
        GangScheduling: false
    - name: "kai-scheduler"
      config: {}
      default: true
```

```yaml
# --- Style 6: Only KAI profile, but kube-scheduler is also active even if not set in profiles --- 
scheduler:
  profiles:
    - name: "kai-scheduler"
      config: {}
      default: true
```


**Phase 1 vs Phase 2:**

- **Phase 1** (Current): Both KAI and Kube backends have minimal no-op implementations. All backend interface methods (`Init`, `SyncPodGang`, `OnPodGangDelete`, `PreparePod`, `ValidatePodCliqueSet`) return immediately without creating any scheduler-specific resources. KAI scheduler continues to read PodGang CRs directly. This phase focuses on establishing the framework infrastructure and interfaces.
- **Phase 2** (Future):
  - **KAI Backend**: All PodGang-related management and KAI’s own CR (e.g. PodGroup) management will be moved into the KAI backend. The backend will implement scheduler backend interfaces to create update KAI CRs, providing cleaner separation and allowing KAI scheduler modifications to be minimized.
  - **Kube Backend**: Will support advanced community features as they become available, including Workload API for gang scheduling and other emerging Kubernetes scheduling capabilities. The backend will translate PodGang specifications to the appropriate Kubernetes-native scheduling primitives.


### PodGang Lifecycle Changes

**Critical Design Change**: The PodGang creation flow has been fundamentally changed to support scheduler backends that require resources to exist before pods are created (e.g., Workload API):

#### Previous Flow (Before Framework):
1. Pods are created first
2. Wait for all pods to have back-references to PodGang
3. Create PodGang with complete PodReferences

#### New Flow (With Framework):
1. **Create PodGang early** with PodGroups having empty PodReferences and `Initialized=False`
2. **Create Pods** (with scheduling gates to block scheduling)
3. **Update PodGang** with PodReferences once all pods are created, and set `Initialized=True`
4. **Scheduling gates removed** to allow pods to be scheduled

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

**Future note:** The existing definition and semantics of the `PodReferences` field (e.g. structure, ownership, and how it is populated) may need to be discussed and documented in a follow-up; this proposal assumes current behavior and may be updated as that is clarified.

### Backend Controller

A dedicated Backend Controller watches PodGang resources and invokes backend hooks

```go
// Reconcile processes PodGang changes and synchronizes to backend-specific CRs.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error)

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error 
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


### Test Plan

#### Unit Tests

Unit tests will be implemented for all framework related components:

**Backend Interface and Registry** (`operator/internal/schedulerBackend/`)
- Test backend registration (success, duplicate registration)
- Test backend retrieval (existing, non-existing)

**KAI Backend Implementation** (`operator/internal/schedulerBackend/kai/`)
- Test pod spec mutation
- Test configuration validation
- Test ValidatePodCliqueSet (allow when valid, reject when scheduler-specific rules fail)

**Kube-scheduler Backend Implementation** (`operator/internal/schedulerBackend/kube/`)
- Test pod spec mutation
- Test configuration validation
- Test ValidatePodCliqueSet (allow when valid, reject when scheduler-specific rules fail)

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
- Basic operator configuration support

#### Beta 
- Backend interface stabilized (no breaking changes expected)
- Documentation for third-party backend development

#### GA
- Backend interface is stable and versioned
- Multiple production deployments using the framework
- Comprehensive documentation and examples
- All tests passing consistently
- Support for at least 2-3 different scheduler backends

## Implementation History
- **2026-01-27**: Initial GREP proposal created and submitted for review

## Alternatives

### Alternative 1: Direct Scheduler Integration

**Description**: Continue the current approach where scheduler-specific logic is embedded directly in Grove's controllers without abstraction.

**Pros**:
- No additional abstraction layer overhead
- Direct control over scheduler integration

**Cons**:
- High maintenance burden as schedulers evolve
- Adding new schedulers requires invasive changes to Grove core
- Difficult for third-party schedulers to integrate
- Code becomes increasingly complex with each scheduler addition


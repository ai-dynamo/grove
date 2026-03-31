# E2E Test Framework Architecture

## Component Diagram

```
┌──────────────────────────────────────────────────────────────────────────┐
│                         TestMain (main_test.go)                         │
│                  Entry point: sets up cluster once per run              │
│                                    │                                    │
│                                    ▼                                    │
│                      ┌───────────────────────────┐                     │
│                      │   SharedClusterManager     │                     │
│                      │  (setup/shared_cluster.go) │                     │
│                      │  ─────────────────────     │                     │
│                      │  • Singleton per run       │                     │
│                      │  • Setup / Teardown        │                     │
│                      │  • PrepareForTest          │                     │
│                      │  • CleanupWorkloads        │                     │
│                      │  • Creates Clients once    │                     │
│                      └─────────────┬──────────────┘                     │
│                                    │ GetAllClients()                    │
│                                    ▼                                    │
│                        ┌─────────────────────┐                         │
│                        │    k8s.Clients       │                         │
│                        │  (k8s/clients.go)    │                         │
│                        │  ─────────────────   │                         │
│                        │  Clientset           │                         │
│                        │  DynamicClient       │                         │
│                        │  CRClient            │                         │
│                        │  RestMapper          │                         │
│                        │  RestConfig          │                         │
│                        └──────────┬───────────┘                         │
│                                   │ shared reference                    │
└───────────────────────────────────┼─────────────────────────────────────┘
                                    │
                  ┌─────────────────┴──────────────────┐
                  │                                    │
                  ▼                                    ▼
    ┌───────────────────────┐           ┌───────────────────────────┐
    │  prepareTest()         │           │  prepareTestCluster()     │
    │  (context.go:122)       │           │  (setup.go:174)           │
    │  ── NEW PATH ──       │           │  ── LEGACY PATH ──       │
    │                       │           │                           │
    │  Used by:             │           │  Used by:                 │
    │  • topology_test      │           │  • auto-mnnvl/ tests     │
    │  • gang_scheduling    │           │                           │
    │  • scale_test         │           │  Returns:                 │
    │  • startup_ordering   │           │  • clientCollection       │
    │  • rolling_updates    │           │  • cleanup func           │
    │  • cert_management    │           │                           │
    │                       │           │  Tests use raw clients +  │
    │  Returns:             │           │  util functions directly  │
    │  • *TestContext         │           │  via LegacyTestContext     │
    │  • cleanup func       │           └───────────────────────────┘
    └───────────┬───────────┘
                │
                ▼
  ┌──────────────────────────────────────────────────┐
  │                  TestContext                        │
  │               (tests/context.go)                   │
  │  ────────────────────────────                    │
  │                                                  │
  │  T         *testing.T                            │
  │  Ctx       context.Context                       │
  │  Clients   *k8s.Clients                          │
  │  Namespace string                                │
  │  Timeout   time.Duration                         │
  │  Interval  time.Duration                         │
  │  Workload  *WorkloadConfig                       │
  │                                                  │
  │  ┌── K8s Managers ────────────────────────────┐  │
  │  │  Pods       *PodManager                    │  │
  │  │  Nodes      *NodeManager                   │  │
  │  │  Resources  *ResourceManager               │  │
  │  ├── Grove Managers ──────────────────────────┤  │
  │  │  Workloads  *WorkloadManager ──┐           │  │
  │  │  Topology   *TopologyVerifier  │ composes  │  │
  │  │  PodGroups  *PodGroupVerifier  │ Resources │  │
  │  │  Config     *OperatorConfig    │ + Pods    │  │
  │  ├── Diagnostics ─────────────────┘───────────┤  │
  │  │  Diag       *DiagCollector                 │  │
  │  └────────────────────────────────────────────┘  │
  │                                                  │
  │  Convenience methods: ListPods, WaitForPods,     │
  │  ScalePCS, DeployAndVerifyWorkload, ...          │
  │  (delegate to managers with suite defaults)      │
  └──────────────────────────────────────────────────┘

  ┌──────────────────────────────────────────────────────────────────┐
  │                    K8s Managers (k8s/)                           │
  │                                                                  │
  │  ┌─────────────────┐ ┌─────────────────┐ ┌──────────────────┐  │
  │  │  PodManager     │ │  NodeManager    │ │ ResourceManager  │  │
  │  │  (pods.go)      │ │  (nodes.go)     │ │ (resources.go)   │  │
  │  │  ────────────   │ │  ────────────   │ │ ──────────────   │  │
  │  │  List           │ │  Cordon         │ │ ApplyYAMLFile    │  │
  │  │  WaitForReady   │ │  Uncordon       │ │ ApplyYAMLData    │  │
  │  │  WaitForCount   │ │  CordonAll      │ │ ScaleCRD         │  │
  │  │  CountByPhase   │ │  GetWorkerNodes │ │                  │  │
  │  └─────────────────┘ └─────────────────┘ └──────────────────┘  │
  │                                                                  │
  │  ┌────────────────────────────────────────────────┐             │
  │  │  PollForCondition (polling.go)                  │             │
  │  │  Core polling primitive used by all managers    │             │
  │  └────────────────────────────────────────────────┘             │
  └──────────────────────────────────────────────────────────────────┘

  ┌──────────────────────────────────────────────────────────────────┐
  │                   Grove Managers (grove/)                        │
  │                                                                  │
  │  ┌──────────────────┐ ┌───────────────────┐ ┌───────────────┐  │
  │  │ WorkloadManager  │ │ TopologyVerifier  │ │PodGroupVerifier│  │
  │  │ (workload.go)    │ │ (topology.go)     │ │(podgroup.go)  │  │
  │  │ ──────────────   │ │ ───────────────   │ │─────────────  │  │
  │  │ ScalePCS         │ │ VerifyCluster     │ │GetKAIPodGroups│  │
  │  │ ScalePCSG        │ │   TopologyLevels  │ │WaitForKAI     │  │
  │  │ DeletePCS        │ │ VerifyPodsInSame  │ │  PodGroups    │  │
  │  │ WaitForPCSG      │ │   TopologyDomain  │ │VerifyTopology │  │
  │  │ WaitForPodClique │ │ VerifyPCSG        │ │  Constraint   │  │
  │  │                  │ │   Replicas        │ │VerifySubGroups│  │
  │  └──────────────────┘ └───────────────────┘ └───────────────┘  │
  │                                                                  │
  │  ┌──────────────────┐                                           │
  │  │  OperatorConfig  │                                           │
  │  │  (config.go)     │                                           │
  │  │  ──────────────  │                                           │
  │  │ ReadGroveMetadata│                                           │
  │  └──────────────────┘                                           │
  └──────────────────────────────────────────────────────────────────┘

  ┌──────────────────────────────────────────────────────────────────┐
  │                  Support Components                              │
  │                                                                  │
  │  ┌──────────────────┐ ┌──────────────────┐ ┌────────────────┐  │
  │  │  DiagCollector   │ │     Logger       │ │  Measurement   │  │
  │  │ (diagnostics/)   │ │  (utils/logger)  │ │ (utils/meas.)  │  │
  │  │ ──────────────   │ │  ──────────────  │ │ ────────────   │  │
  │  │ CollectAll       │ │  Debugf/Infof    │ │ Phase tracking │  │
  │  │ dumpOperatorLogs │ │  Warnf/Errorf    │ │ Milestones     │  │
  │  │ dumpGroveResources││  WriterLevel     │ │ TrackerResult  │  │
  │  │ dumpPodDetails   │ │                  │ │                │  │
  │  │ dumpRecentEvents │ │                  │ │                │  │
  │  └──────────────────┘ └──────────────────┘ └────────────────┘  │
  └──────────────────────────────────────────────────────────────────┘
```

## Lifecycle Flow

```
TestMain
  │
  ├── SharedClusterManager.Setup()          ← connect to cluster, create Clients once
  │
  ├── test_topology(t)                      ← uses NEW path (TestContext)
  │     ├── prepareTest(ctx, t, 28, WithWorkload(...))
  │     │     ├── SharedCluster.PrepareForTest()    ← cordon excess nodes
  │     │     ├── SharedCluster.GetAllClients()      ← reuse shared *Clients
  │     │     └── NewTestContext(t, ctx, clients)      ← instantiate all managers
  │     │
  │     ├── tc.DeployAndVerifyWorkload()             ← high-level helpers
  │     ├── tc.Topology.VerifyPodsInSame...(...)     ← domain managers
  │     ├── tc.PodGroups.VerifySubGroups(...)
  │     │
  │     └── cleanup()
  │           ├── if failed: tc.Diag.CollectAll()    ← dump diagnostics
  │           └── SharedCluster.CleanupWorkloads()   ← remove all test resources
  │
  ├── test_auto_mnnvl(t)                    ← uses LEGACY path (LegacyTestContext)
  │     ├── prepareTestCluster(ctx, t, 0)
  │     │     ├── SharedCluster.PrepareForTest()
  │     │     ├── SharedCluster.GetAllClients()
  │     │     └── returns clientCollection + cleanup
  │     │
  │     ├── uses raw clients + util functions
  │     │
  │     └── cleanup()
  │           ├── if failed: LegacyTestContext.CollectAllDiagnostics()
  │           └── SharedCluster.CleanupWorkloads()
  │
  └── SharedClusterManager.Teardown()                ← final cleanup
```

## Shared Cluster vs Per-Test Configuration

All tests run against the **same physical cluster** and reuse the **same `*k8s.Clients`** instance (created once in `TestMain`). What differs per test:

| Aspect | Shared (same for all) | Per-Test (varies per test) |
|--------|----------------------|---------------------------|
| Cluster | Single cluster for entire run | -- |
| K8s Clients | One `*k8s.Clients` instance | -- |
| Available nodes | -- | `requiredWorkerNodes` arg to `prepareTest()` controls how many nodes are uncordoned |
| Workload | -- | Each test provides its own `WorkloadConfig` (YAML path, expected pods, name) |
| Timeouts | -- | Optionally overridden via `WithTimeout()` / `WithInterval()` (e.g., scale tests use 15m vs default 4m) |
| Manager instances | -- | Fresh structs per test, but all point to the shared `*k8s.Clients` |
| Diagnostics | -- | Fresh `DiagCollector` per test, scoped to the test name for output files |
| Resource state | -- | `cleanup()` calls `CleanupWorkloads()` after each test, so each test starts clean |

The `TestContext` is essentially a **per-test view** over the shared cluster: "give me N nodes, this workload config, these timeouts, and fresh manager wrappers."

## Component Explanations

### SharedClusterManager
Singleton that owns the cluster lifecycle for the entire test run. Connects to the cluster once, creates a shared `Clients` bundle, and between tests it cordons/uncordons nodes and cleans up leftover resources. Ensures tests start from a known state. Both the new `prepareTest()` and legacy `prepareTestCluster()` paths go through it.

### k8s.Clients
A bundle of all Kubernetes client types needed to interact with the cluster: standard clientset, dynamic client (for CRDs), controller-runtime client, REST mapper, and raw REST config. Created once by `SharedClusterManager` and shared across all managers and tests.

### TestContext
The primary abstraction most test files use. Created per-test via `prepareTest()`. Holds the testing context, shared clients, configuration (namespace, timeouts), and all manager instances. Provides convenience methods (e.g. `tc.ListPods()`, `tc.ScalePCS()`) that delegate to managers with per-test defaults. Used by: topology, gang scheduling, scale, startup ordering, rolling updates, cert management tests.

### LegacyTestContext
Deprecated struct with raw client fields and wrapper methods around standalone utility functions. Still used by `auto-mnnvl/` tests. The legacy `prepareTestCluster()` returns a `clientCollection` which is manually unpacked into a `LegacyTestContext`. Will be removed once all consumers migrate to `TestContext`.

### PodManager
Handles pod lifecycle queries: listing pods, waiting for specific counts, waiting for readiness, and counting pods by phase (Running/Pending/Failed). Used heavily in workload verification.

### NodeManager
Controls node scheduling state. Cordons and uncordons nodes to simulate cluster constraints (e.g., testing behavior with limited node availability). Used by `SharedClusterManager.PrepareForTest()` to control how many worker nodes are available.

### ResourceManager
Applies Kubernetes resources from YAML files or raw YAML data, handling multi-document YAML, namespace injection, and create-or-update semantics. Also scales CRDs by patching their replica count.

### WorkloadManager
Domain-specific manager for Grove workloads (PodCliqueSet, PodCliqueScalingGroup). Composes `ResourceManager` and `PodManager` to provide higher-level operations like "scale PCSG and wait for it to be ready." The only manager that depends on other managers.

### TopologyVerifier
Validates that pods are placed according to topology constraints. Checks that pods within a replica land in the same topology domain (e.g., same rack or switch), and that PCSG replicas are distributed correctly.

### PodGroupVerifier
Verifies KAI PodGroup resources that coordinate gang scheduling. Checks that PodGroups have the correct topology constraints, subgroup structure, and parent-child relationships.

### OperatorConfig
Reads the Grove operator's runtime configuration and metadata (image version, operator settings) from the cluster. Used for test assertions about operator state.

### DiagCollector
Activated on test failure. Dumps operator logs, Grove custom resources, pod details, and recent Kubernetes events to stdout or files. Provides the forensic data needed to debug failures without cluster access.

### PollForCondition
The core polling primitive in `k8s/polling.go`. Takes a condition function and retries it at a configurable interval until timeout. All "wait for X" methods across all managers are built on this.

### WorkloadConfig
Simple configuration struct that pairs a workload name with its YAML path, namespace, and expected pod count. Generates the label selector (`app.kubernetes.io/part-of=<name>`) used to find the workload's pods.

### Logger
Structured logging wrapper around zap. Provides leveled logging (Debug/Info/Warn/Error) and an `io.Writer` adapter for redirecting command output through the logging system.

### Measurement Framework
Optional performance tracking in `utils/measurement/`. Defines phases and milestones within a test, tracks timing, and produces structured results for benchmarking workload deployment times.

## LegacyTestContext vs TestContext — Migration Comparison

The refactor changed how tests **consume** the shared cluster and clients. The cluster lifecycle itself (`SharedClusterManager`) is identical in both paths.

### 1. Client Ownership: Scattered Fields vs Bundled Struct

**LegacyTestContext** holds 5 separate client fields directly on the struct:
```go
type LegacyTestContext struct {
    Clientset     kubernetes.Interface
    DynamicClient dynamic.Interface
    RestMapper    meta.RESTMapper
    RestConfig    *rest.Config
    CRClient      client.Client
    // ...
}
```

**TestContext** holds a single `*k8s.Clients` bundle:
```go
type TestContext struct {
    Clients *k8s.Clients   // all 5 clients in one struct
    // ...
}
```

The legacy path also has a `clientCollection` intermediary (`setup.go:236`) that unpacks `*k8s.Clients` back into individual fields for backward compatibility.

### 2. Operations: Wrapper Functions vs Domain Managers

**LegacyTestContext** methods are thin wrappers that pass individual clients to standalone `utils.*` functions:
```go
func (tc LegacyTestContext) listPods() (*v1.PodList, error) {
    return utils.ListPods(tc.Ctx, tc.Clientset, tc.Namespace, ...)
}
func (tc LegacyTestContext) cordonNode(name string) error {
    return utils.SetNodeSchedulable(tc.Ctx, tc.Clientset, name, false)
}
```
Every method manually threads the right client through. The logic lives in the `utils` package as free functions.

**TestContext** delegates to manager structs that already hold the clients internally:
```go
func (tc *TestContext) ListPods() (*v1.PodList, error) {
    return tc.Pods.List(tc.Ctx, tc.Namespace, tc.getLabelSelector())
}
func (tc *TestContext) CordonNode(name string) error {
    return tc.Nodes.Cordon(tc.Ctx, name)
}
```
Managers (`PodManager`, `NodeManager`, etc.) encapsulate the client reference — no need to pass it per call.

### 3. Diagnostics: Inline Methods vs Dedicated DiagCollector

**LegacyTestContext** has diagnostics methods directly on itself (`debug_utils.go`): `CollectAllDiagnostics()`, `dumpOperatorLogs()`, `dumpGroveResources()`, etc. These methods directly use `tc.Clientset`, `tc.DynamicClient`, and handle file output inline.

**TestContext** delegates to a separate `*diagnostics.DiagCollector` struct:
```go
tc.Diag = diagnostics.NewDiagCollector(clients, ns, mode, dir, logger)
tc.Diag.CollectAll(t.Name())
```

### 4. Cleanup: Manual Wiring vs Consistent Pattern

**Legacy** cleanup (`setup.go:196-229`) manually creates a `LegacyTestContext` just for diagnostics, then calls `CleanupWorkloads`:
```go
cleanup := func() {
    diagnosticsTc := LegacyTestContext{    // manual construction just for diag
        T: t, Clientset: clients.Clientset, DynamicClient: clients.DynamicClient, ...
    }
    if t.Failed() { diagnosticsTc.CollectAllDiagnostics() }
    sharedCluster.CleanupWorkloads(ctx)
}
```

**New** cleanup (`context.go:134-148`) uses the already-constructed test context:
```go
cleanup := func() {
    if t.Failed() { tc.Diag.CollectAll(t.Name()) }
    sharedCluster.CleanupWorkloads(ctx)
}
```

### 5. The auto-mnnvl Special Case

The `auto-mnnvl/` tests have their own `testContext` (lowercase, different type in `testutils.go`) and their own `prepareTestCluster()` that returns raw clients individually (`clientset, restConfig, dynamicClient, groveClient, cleanup`). They don't use `LegacyTestContext` from `setup.go` at all — they call `SharedCluster` directly and pass raw clients around.

### 6. What Stayed the Same

Both paths go through `SharedClusterManager` identically:
- `SharedCluster(logger).PrepareForTest(ctx, N)` — cordon/uncordon nodes
- `SharedCluster.GetAllClients()` — same shared `*k8s.Clients`
- `SharedCluster.CleanupWorkloads(ctx)` — same cleanup

The cluster lifecycle and client creation are identical. The refactor only changed how tests consume those shared resources.

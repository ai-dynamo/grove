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
│                        │  clients.Clients     │                         │
│                        │ (k8s/clients/)       │                         │
│                        │  ─────────────────   │                         │
│                        │  Clientset           │                         │
│                        │  DynamicClient       │                         │
│                        │  CRClient            │                         │
│                        │  GroveClient         │                         │
│                        │  RestMapper          │                         │
│                        │  RestConfig          │                         │
│                        └──────────┬───────────┘                         │
│                                   │ shared reference                    │
└───────────────────────────────────┼─────────────────────────────────────┘
                                    │
                                    ▼
                      ┌───────────────────────┐
                      │  PrepareTest()         │
                      │  (tests/context.go)    │
                      │                        │
                      │  Used by ALL suites:   │
                      │  • topology_test       │
                      │  • gang_scheduling     │
                      │  • scale_test          │
                      │  • startup_ordering    │
                      │  • rolling_updates     │
                      │  • cert_management     │
                      │  • auto-mnnvl          │
                      │                        │
                      │  Returns:              │
                      │  • *TestContext         │
                      │  • cleanup func        │
                      └───────────┬────────────┘
                                  │
                                  ▼
  ┌──────────────────────────────────────────────────┐
  │                  TestContext                        │
  │               (tests/context.go)                   │
  │  ────────────────────────────                    │
  │                                                  │
  │  T         *testing.T                            │
  │  Ctx       context.Context                       │
  │  Clients   *clients.Clients                      │
  │  Namespace string                                │
  │  Timeout   time.Duration                         │
  │  Interval  time.Duration                         │
  │  Workload  *WorkloadConfig                       │
  │                                                  │
  │  No manager fields — tests create managers       │
  │  on demand as local variables:                   │
  │                                                  │
  │    topologyVerifier := topology.NewTopology       │
  │      Verifier(tc.Clients, Logger)                │
  │    podGroupVerifier := podgroup.NewPodGroup      │
  │      Verifier(tc.Clients, Logger)                │
  │                                                  │
  │  Convenience methods: ListPods, WaitForPods,     │
  │  ScalePCS, DeployAndVerifyWorkload, ...          │
  │  (create managers internally per call)           │
  └──────────────────────────────────────────────────┘

  ┌──────────────────────────────────────────────────────────────────┐
  │                K8s Domain Packages (k8s/*)                       │
  │                                                                  │
  │  k8s/clients/        k8s/pods/         k8s/nodes/               │
  │  ┌──────────────┐   ┌──────────────┐   ┌──────────────┐        │
  │  │ Clients      │   │ PodManager   │   │ NodeManager  │        │
  │  │ NewClients   │   │ List         │   │ Cordon       │        │
  │  │              │   │ WaitForReady │   │ Uncordon     │        │
  │  │ Bundles:     │   │ WaitForCount │   │ GetWorkerNodes│       │
  │  │  Clientset   │   │ CountByPhase │   │ IsReady (fn) │        │
  │  │  DynamicCli  │   │ CountReady   │   │ SetSchedulable│       │
  │  │  CRClient    │   │              │   │   (fn)       │        │
  │  │  GroveClient │   │ Standalone:  │   │ WaitAndGet   │        │
  │  │  RestMapper  │   │ WaitForReady │   │  ReadyNode   │        │
  │  │  RestConfig  │   │  InNamespace │   │   (fn)       │        │
  │  └──────────────┘   │  WithClientset│  └──────────────┘        │
  │                      └──────────────┘                           │
  │  k8s/resources/                                                 │
  │  ┌──────────────────┐   k8s/ (parent — shared utilities)       │
  │  │ ResourceManager  │   ┌────────────────────────────────┐     │
  │  │ ApplyYAMLFile    │   │ PollForCondition (polling.go)  │     │
  │  │ ApplyYAMLData    │   │ ConvertUnstructuredToTyped     │     │
  │  │ ScaleCRD         │   │ ConvertTypedToUnstructured     │     │
  │  │ AppliedResource  │   │     (conversions.go)           │     │
  │  └──────────────────┘   └────────────────────────────────┘     │
  └──────────────────────────────────────────────────────────────────┘

  ┌──────────────────────────────────────────────────────────────────┐
  │                Grove Domain Packages (grove/*)                   │
  │                                                                  │
  │  grove/workload/       grove/topology/     grove/podgroup/      │
  │  ┌──────────────────┐ ┌──────────────────┐ ┌────────────────┐  │
  │  │ WorkloadManager  │ │ TopologyVerifier │ │PodGroupVerifier│  │
  │  │ ScalePCS         │ │ VerifyCluster    │ │GetKAIPodGroups │  │
  │  │ ScalePCSG        │ │  TopologyLevels  │ │WaitForKAI      │  │
  │  │ DeletePCS        │ │ VerifyPodsInSame │ │  PodGroups     │  │
  │  │ GetPCS           │ │  TopologyDomain  │ │VerifyTopology  │  │
  │  │ WaitForPCSG      │ │ VerifyPCSG       │ │  Constraint    │  │
  │  │ WaitForPodClique │ │  Replicas        │ │VerifySubGroups │  │
  │  │ PodCliqueSetGVR  │ │ PCSGTypeConfig   │ │ExpectedSubGroup│  │
  │  └──────────────────┘ └──────────────────┘ └────────────────┘  │
  │                                                                  │
  │  grove/config/                                                   │
  │  ┌──────────────────┐                                           │
  │  │  OperatorConfig  │                                           │
  │  │ ReadGroveMetadata│                                           │
  │  │  GroveMetadata   │                                           │
  │  └──────────────────┘                                           │
  └──────────────────────────────────────────────────────────────────┘

  ┌──────────────────────────────────────────────────────────────────┐
  │                  Support Components                              │
  │                                                                  │
  │  ┌──────────────────┐ ┌──────────────────┐ ┌────────────────┐  │
  │  │  DiagCollector   │ │     Logger       │ │  Measurement   │  │
  │  │ (diagnostics/)   │ │  (e2e/log/)      │ │ (utils/meas.)  │  │
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
  ├── test_topology(t)
  │     ├── PrepareTest(ctx, t, 28, WithWorkload(...))
  │     │     ├── SharedCluster.PrepareForTest()    ← cordon excess nodes
  │     │     ├── SharedCluster.GetAllClients()      ← reuse shared *Clients
  │     │     └── NewTestContext(t, ctx, clients)
  │     │
  │     ├── topologyVerifier := topology.NewTopologyVerifier(tc.Clients, Logger)
  │     ├── podGroupVerifier := podgroup.NewPodGroupVerifier(tc.Clients, Logger)
  │     ├── tc.DeployAndVerifyWorkload()
  │     ├── topologyVerifier.VerifyPodsInSame...(...)
  │     ├── podGroupVerifier.VerifySubGroups(...)
  │     │
  │     └── cleanup()
  │           ├── if failed: diag.CollectAll(ctx, t.Name())
  │           └── SharedCluster.CleanupWorkloads()
  │
  ├── test_auto_mnnvl(t)                    ← uses same PrepareTest path
  │     ├── PrepareTest(ctx, t, 0)
  │     ├── wm := workload.NewWorkloadManager(tc.Clients, logger)
  │     ├── wm.DeletePCS(...), wm.WaitForPCSG(...)
  │     └── cleanup()
  │
  └── SharedClusterManager.Teardown()
```

## Shared Cluster vs Per-Test Configuration

All tests run against the **same physical cluster** and reuse the **same `*clients.Clients`** instance (created once in `TestMain`). What differs per test:

| Aspect | Shared (same for all) | Per-Test (varies per test) |
|--------|----------------------|---------------------------|
| Cluster | Single cluster for entire run | -- |
| K8s Clients | One `*clients.Clients` instance | -- |
| Available nodes | -- | `requiredWorkerNodes` arg to `PrepareTest()` controls how many nodes are uncordoned |
| Workload | -- | Each test provides its own `WorkloadConfig` (YAML path, expected pods, name) |
| Timeouts | -- | Optionally overridden via `WithTimeout()` / `WithInterval()` (e.g., scale tests use 15m vs default 4m) |
| Managers | -- | Created on demand as local variables by each test that needs them |
| Diagnostics | -- | Fresh `DiagCollector` per test, scoped to the test name for output files |
| Resource state | -- | `cleanup()` calls `CleanupWorkloads()` after each test, so each test starts clean |

The `TestContext` is essentially a **per-test view** over the shared cluster: "give me N nodes, this workload config, these timeouts, and shared clients."

## Component Explanations

### SharedClusterManager
Singleton that owns the cluster lifecycle for the entire test run. Connects to the cluster once, creates a shared `Clients` bundle, and between tests it cordons/uncordons nodes and cleans up leftover resources. Ensures tests start from a known state.

### clients.Clients (k8s/clients/)
A bundle of all Kubernetes client types needed to interact with the cluster: standard clientset, dynamic client (for CRDs), Grove typed client, controller-runtime client, REST mapper, and raw REST config. Created once by `SharedClusterManager` and shared across all tests.

### TestContext (tests/context.go)
The primary abstraction all test files use. Created per-test via `PrepareTest()`. Holds the testing context, shared clients, and configuration (namespace, timeouts, workload). Provides convenience methods (e.g. `tc.ListPods()`, `tc.ScalePCS()`) that create managers internally. Tests that need domain-specific verifiers create them as local variables.

### PodManager (k8s/pods/)
Handles pod lifecycle queries: listing pods, waiting for specific counts, waiting for readiness, and counting pods by phase (Running/Pending/Failed). Also provides standalone functions (`WaitForReadyInNamespaceWithClientset`) for setup code that doesn't have full `*clients.Clients`.

### NodeManager (k8s/nodes/)
Controls node scheduling state. Cordons and uncordons nodes to simulate cluster constraints. Also provides standalone functions (`IsReady`, `SetSchedulable`, `WaitAndGetReadyNode`) that accept a bare `kubernetes.Interface` for use by setup code.

### ResourceManager (k8s/resources/)
Applies Kubernetes resources from YAML files or raw YAML data, handling multi-document YAML, namespace injection, and create-or-update semantics. Also scales CRDs by patching their replica count.

### PollForCondition (k8s/)
The core polling primitive in `k8s/polling.go`. Takes a condition function and retries it at a configurable interval until timeout. All "wait for X" methods across all managers are built on this. Conversion helpers (`ConvertUnstructuredToTyped`, `ConvertTypedToUnstructured`) also live in the parent `k8s/` package.

### WorkloadManager (grove/workload/)
Domain-specific manager for Grove workloads (PodCliqueSet, PodCliqueScalingGroup). Creates its own `ResourceManager` and `PodManager` internally. Provides scale, delete, get, and wait operations for Grove CRDs.

### TopologyVerifier (grove/topology/)
Validates that pods are placed according to topology constraints. Checks that pods within a replica land in the same topology domain (e.g., same rack or switch), and that PCSG replicas are distributed correctly.

### PodGroupVerifier (grove/podgroup/)
Verifies KAI PodGroup resources that coordinate gang scheduling. Checks that PodGroups have the correct topology constraints, subgroup structure, and parent-child relationships.

### OperatorConfig (grove/config/)
Reads the Grove operator's runtime configuration and metadata (image version, operator settings) from the cluster. Used for test assertions about operator state.

### DiagCollector (diagnostics/)
Activated on test failure. Accepts a `context.Context` so collection can be cancelled. Dumps operator logs, Grove custom resources, pod details, and recent Kubernetes events to stdout or files.

### Logger (e2e/log/)
Structured logging wrapper around zap. Provides leveled logging (Debug/Info/Warn/Error) and an `io.Writer` adapter for redirecting command output through the logging system.

### WorkloadConfig
Simple configuration struct that pairs a workload name with its YAML path, namespace, and expected pod count. Generates the label selector (`app.kubernetes.io/part-of=<name>`) used to find the workload's pods.

### Measurement Framework (utils/measurement/)
Performance tracking framework. Defines phases and milestones within a test, tracks timing, and produces structured results for benchmarking workload deployment times. Self-contained subpackage with no dependency on the parent utils.

## Package Dependency Graph

```
                    k8s/clients/
                   ╱     │      ╲
                  ╱      │       ╲
           k8s/pods/  k8s/nodes/  k8s/resources/
              │          │
              │          │         ← all use k8s/ parent for PollForCondition
              ▼          ▼
         grove/workload/          ← creates its own pods + resources managers
         grove/topology/          ← uses k8s/clients/ + k8s/ conversions
         grove/podgroup/          ← uses k8s/clients/ + k8s/ polling + conversions
         grove/config/            ← uses k8s/clients/

         diagnostics/             ← uses k8s/clients/
         tests/context.go         ← uses k8s/*, grove/workload/
         tests/*_test.go          ← uses tests/context.go + grove/* as needed
```

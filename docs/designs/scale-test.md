# Detailed Design: Scale Test Measurement Building Blocks

## 1. Overview

### Problem Statement

The Grove operator needs to prove its reconciliation speed at scale — how fast PodCliqueSet creation cascades through the hierarchy (PCS → PCSG → PCLQ → Pod).
Today there is no Go test infrastructure to measure this.

### Scope

**In scope:**
- Three reusable Go building blocks for: timeline tracking, metrics endpoint scraping, Pyroscope profile collection
- A unified result struct for accumulating and exporting raw measurement data
- Supports multiple PCS submissions per test run (e.g., 50 PCS × 20 pods each)
- Supports comparison across scales (100 / 500 / 1000 / 5000 pods) and regression detection across runs

**Out of scope:**
- Visualization (handled by existing Python/bash scripts or Grafana)
- Percentile/aggregation computation — aggregate timings exported; downstream tools do deeper analysis
- Per-component latency correlation (PCS→PCSG, PCSG→PCLQ, etc.) — measure aggregate wall-clock time only
- Test orchestration — test files compose the building blocks; building blocks don't drive tests
- Operator code changes — no new Prometheus metrics emitted from controllers
- PodGang tracking (`scheduler.grove.io/v1alpha1/podgangs`) — scheduler-level, not operator-level

### Key Design Decisions

| #  | Decision                                                                               | Rationale                                                                                                                                                      |
|----|----------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------|
| 1  | All files in `operator/e2e/utils/`                                                     | Reuses existing Go module, TestContext, K8s clients. No new `go.mod`                                                                                           |
| 2  | Central `ScaleTestResult` struct                                                       | Single accumulator for all raw data. Building blocks write in, tests export out                                                                                |
| 3  | No interfaces on building blocks                                                       | Scale test code, not operator code. Keep simple. Concrete structs with methods                                                                                 |
| 4  | `HTTPClient` interface only for HTTP callers                                           | Enables `httptest.Server` in unit tests for scraper/collector                                                                                                  |
| 5  | `metadata.creationTimestamp` for created timing, `LastTransitionTime` for ready timing | API server assigns `creationTimestamp` — zero clock skew. `PodsReady` uses `pod.Status.Conditions[Ready].LastTransitionTime` for accurate ready timing         |
| 6  | Timeline with phases + milestones                                                      | Flexible: phases/milestones defined at runtime. Supports deploy, scale, rolling update scenarios with one tracker                                              |
| 7  | Multi-PCS via test-run label                                                           | Track events across many PCS objects using a shared `grove.io/scale-test-run` label                                                                            |
| 8  | Durations as `float64` seconds in JSON                                                 | `time.Duration` marshals as nanoseconds — use explicit seconds for correct tooling                                                                             |
| 9  | Aggregate timing only                                                                  | Measure wall-clock time for all pods created and all pods ready — no per-component breakdown                                                                   |
| 10 | Human-readable summary via `WriteSummary`                                              | Test logs need quick-glance output; Prometheus `.prom` is for tooling, not humans                                                                              |
| 11 | In-process port-forward via client-go                                                  | No kubectl dep. rest.Config from TestContext. Pattern proven in cert_management_test.go                                                                        |
| 12 | Port-forward session with sync.Once + done channel                                     | Prevents double-close panic, blocks until goroutine exits — no leaked goroutines                                                                               |
| 13 | Watch-based MilestoneConditions with atomic counters                                   | LIST polling at 5000+ pods overloads API server. Internal watch + atomic counter is accurate and efficient                                                     |
| 14 | Use `expfmt` for Prometheus text parsing                                               | `github.com/prometheus/common/expfmt` (transitive dep via controller-runtime) handles histograms, multi-line help, edge cases correctly. No hand-rolled parser |
| 15 | `PortForwardSession` constructed only after goroutine startup confirmed                | Prevents deadlock if forwarder goroutine never starts — `readyChan` pattern from cert_management_test.go                                                       |
| 16 | RunID includes random suffix                                                           | Prevents label collision with zombie pods from prior failed test runs in the same namespace                                                                    |
| 17 | Port-forward supports explicit pod name (`PortForwardToPod`)                           | Some tests need deterministic targeting of a known pod instead of service resolution                                                                           |
| 18 | Service-based port-forward supports pod-backed services only                           | Avoids ambiguous/non-portforwardable targets and fails fast with clear error (`ErrServiceNotPodBacked`)                                                        |

---

## 2. Package Structure

All new files in the existing `operator/e2e/utils/` package:

```
operator/e2e/utils/
├── scale_test_result.go          # NEW — ScaleTestResult struct + export methods
├── timeline_tracker.go           # NEW — Test timeline tracker with phases and milestones
├── port_forward.go               # NEW — Shared port-forward helper (client-go portforward API)
├── metrics_scraper.go            # NEW — Port-forwards to services, scrapes /metrics endpoints
├── profile_collector.go          # NEW — Fetches Pyroscope profiles after test
│
├── k8s_client.go                 # EXISTING — ApplyYAMLFile, WaitForPods, ListPods
├── grove_resources.go            # EXISTING — GVR constants (add PCSG + PCLQ GVRs)
├── polling.go                    # EXISTING — PollForCondition
├── logger.go                     # EXISTING — Logger
└── ...
```

**Why this location:** The `operator/e2e/utils/` package already holds all E2E building blocks. These are the same kind — reusable utilities that tests compose. No reason for a separate package.

**Build tags:** Unit tests for `ScaleTestResult`, `MetricsScraper`, `ProfileCollector` do NOT need the `e2e` tag. Only `TimelineTracker` integration tests (which need a cluster) use `//go:build e2e`.

---

## 3. Core Types

### 3.1 `scale_test_result.go` — The Result Accumulator

The central struct that all building blocks write into. Tests create one at the start, pass it to each building block, then export at the end.

```go
// ScaleTestResult accumulates all raw measurement data from a scale test run.
// Not safe for concurrent use — building blocks write to it sequentially.
type ScaleTestResult struct {
    // Test identity
    TestName  string `json:"testName"`
    RunID     string `json:"runID"`
    Namespace string `json:"namespace"`

    // Scale metadata
    TotalExpectedPods int `json:"totalExpectedPods"`
    PCSCount          int `json:"pcsCount"`

    // Cluster topology — required for meaningful regression comparison across environments
    NodeCount      int    `json:"nodeCount"`
    ClusterProfile string `json:"clusterProfile,omitempty"` // e.g. "10-node-a100", "50-node-h100"

    // Timeline — phases and milestones from TimelineTracker
    Phases              []Phase `json:"phases"`
    TestDurationSeconds float64 `json:"testDurationSeconds"`

    // From MetricsScraper
    PrometheusMetrics []PrometheusSample `json:"prometheusMetrics,omitempty"`

    // From ProfileCollector
    Profiles []CollectedProfile `json:"profiles,omitempty"`
}
```

**Design choice — aggregate timings only:** Measure wall-clock time for all pods created and all pods ready. No per-component breakdown — keeps the design simple. Downstream tools aggregate scraped metrics for deeper analysis.

#### Sub-types

```go
// PrometheusSample is a single metric data point scraped from a /metrics endpoint.
type PrometheusSample struct {
    MetricName string            `json:"metricName"`
    Labels     map[string]string `json:"labels"`
    Value      float64           `json:"value"`
    Timestamp  time.Time         `json:"timestamp"`
    Source     string            `json:"source"` // endpoint name: "operator", "pyroscope", etc.
}

// CollectedProfile holds raw profile data fetched from Pyroscope.
type CollectedProfile struct {
    Type      string    `json:"type"`      // "cpu", "memory", "goroutine"
    StartTime time.Time `json:"startTime"`
    EndTime   time.Time `json:"endTime"`
    Data      []byte    `json:"-"`         // Raw pprof binary data — saved to file, not inlined
    FilePath  string    `json:"filePath"`  // Path where profile was saved (set by SaveToDir)
}
```

#### Export Methods

```go
// WriteSummary writes a human-readable test summary to the given writer.
// Output format:
//   === Scale Test: ScaleTest_1000 (run: scale-123) ===
//   PCS count:        50
//   Expected pods:    1000
//   Total test time:  72.5s
//   Timeline:
//     Phase: deploy (started +0.0s)
//       all-pods-created  +10.0s (10.0s from phase start)
//       all-pods-ready    +15.0s (15.0s from phase start)
//     Phase: cleanup (started +16.0s)
//       all-pods-gone     +22.0s (6.0s from phase start)
func (r *ScaleTestResult) WriteSummary(w io.Writer) error

// WritePrometheus writes timing data in Prometheus exposition format.
// Includes # HELP and # TYPE headers for pushgateway compatibility.
// Output format:
//   # HELP grove_scale_test_milestone_seconds Time from phase start to milestone completion
//   # TYPE grove_scale_test_milestone_seconds gauge
//   grove_scale_test_milestone_seconds{test="ScaleTest_1000",run_id="scale-123",phase="deploy",milestone="all-pods-created"} 10.0
//   grove_scale_test_milestone_seconds{test="ScaleTest_1000",run_id="scale-123",phase="deploy",milestone="all-pods-ready"} 15.0
//   grove_scale_test_milestone_seconds{test="ScaleTest_1000",run_id="scale-123",phase="cleanup",milestone="all-pods-gone"} 6.0
//   # HELP grove_scale_test_total_seconds Total test duration in seconds
//   # TYPE grove_scale_test_total_seconds gauge
//   grove_scale_test_total_seconds{test="ScaleTest_1000",run_id="scale-123"} 72.5
//   # HELP grove_scale_test_expected_pods Number of expected pods
//   # TYPE grove_scale_test_expected_pods gauge
//   grove_scale_test_expected_pods{test="ScaleTest_1000",run_id="scale-123"} 1000
//   # HELP grove_scale_test_pcs_count Number of PCS objects
//   # TYPE grove_scale_test_pcs_count gauge
//   grove_scale_test_pcs_count{test="ScaleTest_1000",run_id="scale-123"} 50
//   # HELP grove_scale_test_node_count Number of cluster nodes
//   # TYPE grove_scale_test_node_count gauge
//   grove_scale_test_node_count{test="ScaleTest_1000",run_id="scale-123"} 10
// Also writes scraped PrometheusMetrics if present.
func (r *ScaleTestResult) WritePrometheus(w io.Writer) error

// WriteJSON writes the full result as indented JSON for regression comparison.
func (r *ScaleTestResult) WriteJSON(w io.Writer) error

// SaveToDir creates the output directory (os.MkdirAll) and writes all outputs:
//   <dir>/metrics-<runID>.prom              — raw Prometheus gauge samples
//   <dir>/results-<runID>.json              — full result for regression comparison
//   <dir>/summary-<runID>.txt               — human-readable summary
//   <dir>/profiles/<type>-<runID>.pprof     — pprof binary profiles from Pyroscope
// Callers should use an absolute path or a path derived from the test artifact
// directory (e.g., Ginkgo ReportDir) — not a relative path like "results/".
func (r *ScaleTestResult) SaveToDir(dir string) error
```

---

### 3.2 `HTTPClient` — Shared Interface for HTTP Callers

Defined in `scale_test_result.go` (shared by MetricsScraper and ProfileCollector).

```go
// HTTPClient is satisfied by *http.Client. Used by MetricsScraper and
// ProfileCollector to enable testing with httptest.Server.
type HTTPClient interface {
    Do(req *http.Request) (*http.Response, error)
}
```

---

### 3.3 `port_forward.go` — Shared Port-Forward Helper

In-process port-forwarding via client-go's `portforward` API. No `kubectl` dependency. `rest.Config` from `TestContext.RestConfig`. Pattern proven in `cert_management_test.go` (lines 606-708).

```go
// ServiceEndpoint identifies a K8s service to port-forward to.
type ServiceEndpoint struct {
    Name string // human label: "operator", "pyroscope", etc.
    URL  string // K8s service URL, e.g. "http://grove-operator-metrics.grove-system.svc:8443"
}

// ParseServiceURL parses a K8s service URL into its components.
// Expected format: http://<svc>.<ns>.svc:<port>
// Returns error with clear message if the format is invalid.
func ParseServiceURL(url string) (name, namespace string, port int, err error)

// PortForwardToPod establishes a direct pod port-forward using namespace/pod/port.
// Use this when tests want deterministic explicit pod targeting.
func PortForwardToPod(ctx context.Context, restConfig *rest.Config,
    namespace, podName string, remotePort int) (*PortForwardSession, error)

// PortForwardSession represents an active port-forward tunnel.
// Close is safe to call multiple times (guarded by sync.Once).
//
// Construction: PortForwardSession is only returned by PortForwardToService
// after the forwarder goroutine has confirmed startup via readyChan.
// This prevents a deadlock where Close() blocks on <-s.done for a
// goroutine that never started.
type PortForwardSession struct {
    LocalPort int
    stop      chan struct{}
    done      chan struct{} // closed when forwarder goroutine exits
    closeOnce sync.Once
    errOut    bytes.Buffer  // captures SPDY/port-forward stderr for diagnostics
}

// Close tears down the port-forward tunnel and blocks until the
// forwarder goroutine has exited. Safe to call multiple times.
func (s *PortForwardSession) Close() {
    s.closeOnce.Do(func() { close(s.stop) })
    <-s.done
}

// LocalURL returns the base URL for the forwarded port: http://localhost:<LocalPort>
func (s *PortForwardSession) LocalURL() string

// Errors returns any SPDY/port-forward errors captured during the session.
func (s *PortForwardSession) Errors() string {
    return s.errOut.String()
}

// PortForwardToService parses the service URL via ParseServiceURL to extract
// service name, namespace, and port, resolves it to a backing pod (first
// Running pod selected — any pod is acceptable for metrics/profiles), and
// establishes a port-forward using client-go's portforward API
// (spdy.RoundTripperFor + portforward.New).
//
// Supported targets: pod-backed services only. Service must resolve to a Pod
// target (EndpointSlice targetRef.kind=Pod or selector-based Pod lookup).
// Non pod-backed services (for example kubernetes.default.svc in some clusters)
// return ErrServiceNotPodBacked.
//
// The session is only returned after the forwarder goroutine confirms
// startup via readyChan — prevents deadlock on Close() if startup fails.
// SPDY errors are captured in PortForwardSession.errOut for diagnostics.
// The forwarder goroutine respects ctx cancellation.
// Pattern: cert_management_test.go lines 606-708.
func PortForwardToService(ctx context.Context, restConfig *rest.Config,
    clientset kubernetes.Interface, ep ServiceEndpoint) (*PortForwardSession, error)
```

**Design notes:**
- `sync.Once` prevents double-close panic on the stop channel
- `done` channel ensures `Close()` blocks until the goroutine exits — no leaked goroutines in tests
- `PortForwardSession` is only constructed after forwarder goroutine confirms startup via `readyChan` — prevents deadlock if goroutine never starts
- `errOut` buffer captures SPDY errors for diagnostics (stderr from the port-forwarder)
- `ParseServiceURL` validates URL format early with clear error messages
- Pod selection: first Running pod backing the service (any pod is acceptable for metrics/profiles endpoints)
- Explicit pod support: `PortForwardToPod(namespace, podName, port)` for deterministic targeting
- Supported targets: pod-backed services only. Services without Pod targets return `ErrServiceNotPodBacked`
- Context cancellation triggers `Close()` internally

---

## 4. Building Block Designs

### 4.1 `timeline_tracker.go` — Test Timeline Tracker

**Responsibility:** Record a continuous timeline of phases and milestones across the entire test. Phases and milestones are defined at runtime. Each milestone records both absolute timestamp and duration from phase start.

#### Data Types

```go
// Milestone is a named data point on the timeline, recorded when a condition is met.
type Milestone struct {
    Name                   string    `json:"name"`
    Timestamp              time.Time `json:"timestamp"`
    DurationFromPhaseStart float64   `json:"durationFromPhaseStartSeconds"`
}

// Phase is a named segment of the test timeline containing milestones.
type Phase struct {
    Name                    string      `json:"name"`
    StartTime               time.Time   `json:"startTime"`
    DurationFromTestStart   float64     `json:"durationFromTestStartSeconds"` // seconds from test start to phase start
    Milestones              []Milestone `json:"milestones"`
}

// MilestoneCondition checks whether a milestone has been reached.
// Implementations use watches or polls to detect the condition.
// Extensible: implement this interface for custom conditions.
type MilestoneCondition interface {
    // Met returns true when the condition is satisfied.
    // Called repeatedly by the tracker until it returns true or ctx expires.
    Met(ctx context.Context) (bool, error)
}

// MilestoneDefinition pairs a milestone name with its condition.
type MilestoneDefinition struct {
    Name      string
    Condition MilestoneCondition
}
```

#### TimelineTracker Struct

```go
type TimelineTracker struct {
    testStart    time.Time
    phases       []Phase
    pollInterval time.Duration // default: 2s
    logger       *Logger
}
```

#### Constructor

```go
func NewTimelineTracker(logger *Logger) *TimelineTracker
```

Creates a tracker with `testStart` set to `time.Now()` and default `pollInterval` of 2 seconds.

#### Methods

```go
// WithPollInterval overrides the default poll interval (2s) for milestone evaluation.
// Shorter intervals (250-500ms) improve timing accuracy but increase API server load.
func (t *TimelineTracker) WithPollInterval(d time.Duration) *TimelineTracker

// RunPhase defines a named phase with all its milestones upfront, then
// executes the action and waits for milestones in order.
// 1. Records phase start time (and DurationFromTestStart relative to testStart)
// 2. Calls actionFn (the test action: deploy, scale, delete, etc.)
// 3. Evaluates milestones sequentially — for each milestone:
//    - Polls Condition.Met(ctx) every pollInterval until true
//    - Records timestamp and duration from phase start
// 4. Phase completes when the last milestone is reached
// Returns error with phase and milestone context: fmt.Errorf("phase %q: milestone %q: %w", ...)
// Caller controls timeout via ctx (e.g., context.WithTimeout).
//
// Recommended timeouts per scale tier:
//   - 100 pods:  2 minutes
//   - 500 pods:  5 minutes
//   - 1000 pods: 10 minutes
//   - 5000 pods: 30 minutes
func (t *TimelineTracker) RunPhase(ctx context.Context, name string,
    actionFn func(ctx context.Context) error,
    milestones ...MilestoneDefinition) error

// Phases returns all recorded phases with their milestones.
func (t *TimelineTracker) Phases() []Phase
```

#### Built-in MilestoneCondition Implementations

All pod-counting conditions use an **internal watch with atomic counter** — not LIST polling.
On first `Met()` call, the condition starts a watch goroutine via `watchtools.UntilWithSync`
(handles initial list + watch with automatic retry). The watch goroutine maintains an
`atomic.Int32` counter updated on each watch event. Subsequent `Met()` calls read the
atomic counter — O(1) with zero API server load per poll tick.

This is a hard requirement at 5000+ pods. LIST polling would generate 1 LIST call per
poll interval per milestone, overwhelming the API server.

```go
// PodsCreatedCondition checks if expectedCount pods matching the label selector exist.
// Uses an internal watch (watchtools.UntilWithSync) with atomic counter.
// Met() starts the watch on first call; subsequent calls read the atomic counter.
// Timing: records creationTimestamp from the watch event for the Nth pod.
type PodsCreatedCondition struct {
    Clientset     kubernetes.Interface
    Namespace     string
    LabelSelector string
    ExpectedCount int
}
func (c *PodsCreatedCondition) Met(ctx context.Context) (bool, error)

// PodsReadyCondition checks if expectedCount pods matching the label selector are Ready.
// Uses an internal watch (watchtools.UntilWithSync) with atomic counter.
// Met() starts the watch on first call; subsequent calls read the atomic counter.
// Timing: uses pod.Status.Conditions[Ready].LastTransitionTime for accurate ready timing
// (not creationTimestamp — that only applies to PodsCreatedCondition).
type PodsReadyCondition struct {
    Clientset     kubernetes.Interface
    Namespace     string
    LabelSelector string
    ExpectedCount int
}
func (c *PodsReadyCondition) Met(ctx context.Context) (bool, error)

// FirstPodReadyCondition checks if at least one pod matching the label selector is Ready.
// Measures operator dispatch speed — time from action to first pod becoming Ready.
// Uses an internal watch with the same pattern as PodsReadyCondition.
type FirstPodReadyCondition struct {
    Clientset     kubernetes.Interface
    Namespace     string
    LabelSelector string
}
func (c *FirstPodReadyCondition) Met(ctx context.Context) (bool, error)

// PodsGoneCondition checks if zero pods match the label selector.
// Uses an internal watch (watchtools.UntilWithSync) with atomic counter.
type PodsGoneCondition struct {
    Clientset     kubernetes.Interface
    Namespace     string
    LabelSelector string
}
func (c *PodsGoneCondition) Met(ctx context.Context) (bool, error)

// PodsTerminatingCondition checks if all pods matching the label selector
// have a non-nil DeletionTimestamp (scheduled for deletion by the operator).
// Separates operator deletion dispatch time from kubelet container teardown time.
type PodsTerminatingCondition struct {
    Clientset     kubernetes.Interface
    Namespace     string
    LabelSelector string
    ExpectedCount int
}
func (c *PodsTerminatingCondition) Met(ctx context.Context) (bool, error)
```

Custom conditions implement `MilestoneCondition` — e.g., `PCSReadyCondition`, `AllPCLQsHealthy`, etc.

#### Example Usage (Scenario 1 — Initial Deploy)

```go
timeline := NewTimelineTracker(logger)
labelSel := "grove.io/scale-test-run=" + runID

timeline.RunPhase(ctx, "deploy",
    func(ctx context.Context) error {
        return ApplyYAMLFile(ctx, pcsManifest, ...)
    },
    MilestoneDefinition{"all-pods-created",
        &PodsCreatedCondition{clientset, ns, labelSel, 1000}},
    MilestoneDefinition{"all-pods-ready",
        &PodsReadyCondition{clientset, ns, labelSel, 1000}},
)

timeline.RunPhase(ctx, "scale-to-2",
    func(ctx context.Context) error {
        return ScalePCS(ctx, pcsName, 2)
    },
    MilestoneDefinition{"all-pods-created",
        &PodsCreatedCondition{clientset, ns, labelSel, 2000}},
    MilestoneDefinition{"all-pods-ready",
        &PodsReadyCondition{clientset, ns, labelSel, 2000}},
)

timeline.RunPhase(ctx, "cleanup",
    func(ctx context.Context) error {
        return DeleteResource(ctx, pcsGVR, ns, pcsName)
    },
    MilestoneDefinition{"all-pods-gone",
        &PodsGoneCondition{clientset, ns, labelSel}},
)
```

#### Output Timeline

```
Phase: deploy           start=t+0s
  ├─ all-pods-created   t+10s  (10.0s from phase start)
  └─ all-pods-ready     t+15s  (15.0s from phase start)
Phase: scale-to-2       start=t+16s
  ├─ all-pods-created   t+22s  (6.0s from phase start)
  └─ all-pods-ready     t+28s  (12.0s from phase start)
Phase: cleanup          start=t+29s
  └─ all-pods-gone      t+35s  (6.0s from phase start)
```

---

### 4.2 `metrics_scraper.go` — Metrics Endpoint Scraper

**Responsibility:** Port-forward to pod-backed K8s services and scrape their `/metrics` endpoints directly (operator by default). No Prometheus server involved.

**Default endpoints:** Operator metrics service is the default. API server metrics are opt-in and should only be configured when the environment has a known pod-backed target path.

```go
type MetricsScraper struct {
    endpoints   []ServiceEndpoint
    metricNames []string              // prefix filter (nil = keep all)
    restConfig  *rest.Config
    clientset   kubernetes.Interface
    client      HTTPClient            // when set, skip port-forward (unit test bypass)
    logger      *Logger
}
```

#### Constructor

```go
func NewMetricsScraper(endpoints []ServiceEndpoint, restConfig *rest.Config,
    clientset kubernetes.Interface, logger *Logger) *MetricsScraper
```

#### Methods

```go
// WithHTTPClient sets a custom HTTP client (for testing).
// When set, ScrapeInto skips port-forward and hits the client directly.
func (s *MetricsScraper) WithHTTPClient(client HTTPClient) *MetricsScraper

// WithMetricPrefixFilter sets metric name prefixes to keep.
// Only metrics whose name starts with one of these prefixes are included.
// Default: DefaultMetricPrefixes. Pass nil to keep all.
// WARNING: nil filter at 5000+ pods can return very large responses from /metrics.
// Only use nil for debugging, not in CI.
func (s *MetricsScraper) WithMetricPrefixFilter(prefixes []string) *MetricsScraper

// ScrapeInto port-forwards to each endpoint's K8s service, GETs /metrics,
// parses Prometheus exposition format, filters by prefix, and appends
// PrometheusSamples to result.PrometheusMetrics.
// Point-in-time snapshot — no time range needed.
// When WithHTTPClient is set, skips port-forward and hits client directly.
// Continues on individual endpoint failures (logs warning, proceeds).
func (s *MetricsScraper) ScrapeInto(ctx context.Context, result *ScaleTestResult) error
```

#### Default Metric Filters

```go
var DefaultMetricPrefixes = []string{
    "controller_runtime_reconcile",
    "controller_runtime_reconcile_errors_total",
    "workqueue_depth",
    "workqueue_adds_total",
    "workqueue_queue_duration_seconds",
    "rest_client_requests_total",
    "apiserver_request_duration_seconds",
    "apiserver_request_total",
    "etcd_request_duration_seconds",
}
```

#### HTTP

```
Port-forward: client-go portforward API via PortForwardToService()
Scrape:       GET http://localhost:<localPort>/metrics
Format:       Prometheus exposition format (text/plain), parsed via github.com/prometheus/common/expfmt
```

**Parsing:** Uses `expfmt.TextParser` from the `prometheus/common` library (transitive dependency via controller-runtime, already in `go.mod`). Handles histograms, multi-line help text, and edge cases correctly. No hand-rolled line parser.

---

### 4.3 `profile_collector.go` — Pyroscope Profile Collector

**Responsibility:** Fetch CPU, memory, and goroutine profiles from Pyroscope for the test time window. Writes profile files directly to output directory.

```go
type ProfileCollector struct {
    endpoint     ServiceEndpoint
    appName      string
    restConfig   *rest.Config
    clientset    kubernetes.Interface
    client       HTTPClient           // when set, skip port-forward
    profileTypes []string             // default: ["cpu", "memory", "goroutine"]
    logger       *Logger
}
```

#### Constructor

```go
func NewProfileCollector(endpoint ServiceEndpoint, appName string,
    restConfig *rest.Config, clientset kubernetes.Interface, logger *Logger) *ProfileCollector
```

#### Methods

```go
// WithHTTPClient sets a custom HTTP client (for testing).
// When set, CollectInto skips port-forward and hits the client directly.
func (c *ProfileCollector) WithHTTPClient(client HTTPClient) *ProfileCollector

// CollectInto port-forwards to the Pyroscope service, fetches all profile types
// for the given time window [startTime, endTime] in pprof format, and appends
// to result.Profiles with raw pprof data. File writing is handled by SaveToDir.
// startTime/endTime must be provided explicitly by the caller (typically testStart
// and time.Now() at collection time) — not derived internally.
// When WithHTTPClient is set, skips port-forward.
// Continues on individual profile failures (logs warning, proceeds).
func (c *ProfileCollector) CollectInto(ctx context.Context,
    startTime, endTime time.Time, result *ScaleTestResult) error
```

#### Pyroscope HTTP API

```
GET /pyroscope/render?query=<appName>.<profileType>&from=<unix_seconds>&until=<unix_seconds>&format=pprof
```

**Profile type naming:** Uses `cpu`, `memory`, `goroutine` consistently. The Pyroscope query maps `cpu` to `process_cpu` internally. Response is binary pprof format.

---

## 5. Data Flow

```
Test Function
    │
    ├─ 1. runID := fmt.Sprintf("scale-%d-%s", time.Now().Unix(), rand.String(5))
    │     testStart := time.Now()
    │     labelSel := "grove.io/scale-test-run=" + runID
    │     nodeCount := countNodes(ctx, clientset)
    │     result := &ScaleTestResult{
    │         TestName: "ScaleTest_1000",
    │         RunID:    runID,
    │         Namespace: "default",
    │         TotalExpectedPods: 1000,
    │         PCSCount: 50,
    │         NodeCount: nodeCount,
    │     }
    │
    ├─ 2. // Create all building blocks upfront
    │     timeline := NewTimelineTracker(logger)
    │     scraper := NewMetricsScraper([]ServiceEndpoint{
    │         {Name: "operator", URL: "http://grove-operator-metrics.grove-system.svc:8443"},
    │     }, restConfig, clientset, logger)
    │     collector := NewProfileCollector(
    │         ServiceEndpoint{Name: "pyroscope", URL: "http://pyroscope.monitoring.svc:4040"},
    │         "grove-operator", restConfig, clientset, logger,
    │     )
    │
    ├─ 3. timeline.RunPhase(ctx, "deploy",
    │         func(ctx context.Context) error {
    │             for _, manifest := range pcsManifests {
    │                 ApplyYAMLFile(ctx, manifest, ...)
    │             }
    │             return nil
    │         },
    │         MilestoneDefinition{"all-pods-created",
    │             &PodsCreatedCondition{clientset, ns, labelSel, 1000}},
    │         MilestoneDefinition{"all-pods-ready",
    │             &PodsReadyCondition{clientset, ns, labelSel, 1000}},
    │     )
    │
    ├─ 4. timeline.RunPhase(ctx, "cleanup",
    │         func(ctx context.Context) error {
    │             for _, pcsName := range pcsNames {
    │                 DeleteResource(ctx, pcsGVR, namespace, pcsName)
    │             }
    │             return nil
    │         },
    │         MilestoneDefinition{"all-pods-gone",
    │             &PodsGoneCondition{clientset, ns, labelSel}},
    │     )
    │
    ├─ 5. // Collect supplementary data after test completes
    │     if err := scraper.ScrapeInto(ctx, result); err != nil {
    │         logger.Warnf("metrics scrape failed: %v", err)
    │     }
    │     if err := collector.CollectInto(ctx, testStart, time.Now(), result); err != nil {
    │         logger.Warnf("pyroscope collection failed: %v", err)
    │     }
    │
    ├─ 6. result.Phases = timeline.Phases()
    │     result.TestDurationSeconds = time.Since(testStart).Seconds()
    │     result.SaveToDir("results/")     // .prom + .json + .txt + .pprof
    │
    └─ 7. result.WriteSummary(os.Stdout)
```

**Graceful degradation:** Step 5 (metrics scraping, Pyroscope) is supplementary. Failures are logged but don't fail the test.

**Building blocks are created upfront** (step 2) so the test reads as setup → execute → cleanup → collect → export.

---

## 6. Supported Test Scenarios

All scenarios use one `TimelineTracker` — phases accumulate on the same timeline. `RunPhase` defines milestones upfront and waits for all of them in order.

### Scenario 1: Initial Deployment — PCS to All Pods Ready

```go
timeline := NewTimelineTracker(logger)
labelSel := "grove.io/scale-test-run=" + runID

timeline.RunPhase(ctx, "deploy", deployAction,
    MilestoneDefinition{"first-pod-ready",
        &FirstPodReadyCondition{clientset, ns, labelSel}},
    MilestoneDefinition{"all-pods-created",
        &PodsCreatedCondition{clientset, ns, labelSel, 1000}},
    MilestoneDefinition{"all-pods-ready",
        &PodsReadyCondition{clientset, ns, labelSel, 1000}},
)
timeline.RunPhase(ctx, "cleanup", cleanupAction,
    MilestoneDefinition{"all-pods-terminating",
        &PodsTerminatingCondition{clientset, ns, labelSel, 1000}},
    MilestoneDefinition{"all-pods-gone",
        &PodsGoneCondition{clientset, ns, labelSel}},
)
```

### Scenario 2: Scale PCS Replicas 1 → 2

```go
// ... Scenario 1 deploy phase ...
timeline.RunPhase(ctx, "scale-to-2",
    func(ctx context.Context) error { return ScalePCS(ctx, pcsName, 2) },
    MilestoneDefinition{"all-pods-created",
        &PodsCreatedCondition{clientset, ns, labelSel, 2000}},
    MilestoneDefinition{"all-pods-ready",
        &PodsReadyCondition{clientset, ns, labelSel, 2000}},
)
// ... cleanup phase ...
```

### Scenario 3: Scale PCS Replicas 2 → 4

```go
// ... Scenario 2 phases ...
timeline.RunPhase(ctx, "scale-to-4",
    func(ctx context.Context) error { return ScalePCS(ctx, pcsName, 4) },
    MilestoneDefinition{"all-pods-created",
        &PodsCreatedCondition{clientset, ns, labelSel, 4000}},
    MilestoneDefinition{"all-pods-ready",
        &PodsReadyCondition{clientset, ns, labelSel, 4000}},
)
// ... cleanup phase ...
```

### Scenario 4 (Optional): Rolling Update Latency

```go
// ... after initial deploy phase ...
timeline.RunPhase(ctx, "rolling-update",
    func(ctx context.Context) error {
        for _, clique := range []string{"frontend", "pleader", "pworker", "dleader", "dworker"} {
            UpdatePodCliqueSpec(ctx, clique, newSpec)
        }
        return nil
    },
    MilestoneDefinition{"all-pods-ready",
        &PodsReadyCondition{clientset, ns, labelSel, expectedPods}},
)
// ... cleanup phase ...
```

### Scenario 5: Scale Down — PCS Replicas 2 → 1

Measures how quickly the operator schedules excess pod deletions separately
from kubelet container teardown time.

```go
// ... after scale-to-2 deploy phase ...
timeline.RunPhase(ctx, "scale-down-to-1",
    func(ctx context.Context) error { return ScalePCS(ctx, pcsName, 1) },
    MilestoneDefinition{"excess-pods-terminating",
        &PodsTerminatingCondition{clientset, ns, labelSel, 1000}},
    MilestoneDefinition{"all-pods-ready",
        &PodsReadyCondition{clientset, ns, labelSel, 1000}},
    MilestoneDefinition{"excess-pods-gone",
        &PodsGoneCondition{clientset, ns, "grove.io/scale-test-run=" + runID + ",!active"}},
)
// ... cleanup phase ...
```

---

## 7. Error Handling

| Component | Error Behavior |
|-----------|---------------|
| `TimelineTracker.RunPhase()` | Returns error with phase and milestone context: `fmt.Errorf("phase %q: milestone %q: %w", ...)`. Fatal for test. |
| `MilestoneCondition.Met()` | Returns error on unrecoverable failures. Tracker propagates to RunPhase caller. |
| `PortForwardToService()` | Returns `ErrServiceNotPodBacked` when service cannot resolve to a pod target (EndpointSlice/selector path). |
| `MetricsScraper.ScrapeInto()` | Returns error if all endpoints unreachable. Individual failures logged, continues. |
| `ProfileCollector.CollectInto()` | Returns error if port-forward to Pyroscope service fails. Individual profile failures logged, continues. |
| `ScaleTestResult.SaveToDir()` | Returns first error encountered during file writes. |

**Wrapping convention:** Simple `fmt.Errorf("description: %w", err)` at the boundary. No deep wrapping chains — these are test utilities.

---

## 8. Testability

| Component | Testable Without Cluster? | Strategy |
|-----------|--------------------------|----------|
| `ScaleTestResult.WriteSummary()` | Yes | Populate struct with known Phases, verify human-readable output |
| `ScaleTestResult.WritePrometheus()` | Yes | Populate struct with known Phases, verify phase/milestone timing gauges |
| `ScaleTestResult.WriteJSON()` | Yes | Populate struct, verify JSON structure and `float64` seconds |
| `TimelineTracker` | Yes | Create tracker, run phases with mock MilestoneConditions, verify Phases() output |
| `MetricsScraper.ScrapeInto()` | Yes | `httptest.Server` returning Prometheus exposition format text |
| `ProfileCollector.CollectInto()` | Yes | `httptest.Server` returning mock pprof binary |
| `TimelineTracker` (full with real conditions) | No | Needs real K8s API. Integration test only (`//go:build e2e`). |

### Test Skeleton: Prometheus Export

```go
func TestScaleTestResult_WritePrometheus(t *testing.T) {
    phaseStart := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
    result := &ScaleTestResult{
        TestName:          "ScaleTest_1000",
        RunID:             "test-run-1",
        TotalExpectedPods: 1000,
        PCSCount:          50,
        TestDurationSeconds: 72.5,
        Phases: []Phase{
            {
                Name:      "deploy",
                StartTime: phaseStart,
                Milestones: []Milestone{
                    {Name: "all-pods-created", Timestamp: phaseStart.Add(10 * time.Second), DurationFromPhaseStart: 10.0},
                    {Name: "all-pods-ready", Timestamp: phaseStart.Add(15 * time.Second), DurationFromPhaseStart: 15.0},
                },
            },
            {
                Name:      "cleanup",
                StartTime: phaseStart.Add(16 * time.Second),
                Milestones: []Milestone{
                    {Name: "all-pods-gone", Timestamp: phaseStart.Add(22 * time.Second), DurationFromPhaseStart: 6.0},
                },
            },
        },
    }
    var buf bytes.Buffer
    err := result.WritePrometheus(&buf)
    // assert no error
    // assert contains: grove_scale_test_milestone_seconds{test="ScaleTest_1000",run_id="test-run-1",phase="deploy",milestone="all-pods-created"} 10.0
    // assert contains: grove_scale_test_milestone_seconds{test="ScaleTest_1000",run_id="test-run-1",phase="deploy",milestone="all-pods-ready"} 15.0
    // assert contains: grove_scale_test_milestone_seconds{test="ScaleTest_1000",run_id="test-run-1",phase="cleanup",milestone="all-pods-gone"} 6.0
    // assert contains: grove_scale_test_total_seconds{test="ScaleTest_1000",run_id="test-run-1"} 72.5
    // assert contains: grove_scale_test_expected_pods{test="ScaleTest_1000",run_id="test-run-1"} 1000
    // assert contains: grove_scale_test_pcs_count{test="ScaleTest_1000",run_id="test-run-1"} 50
}
```

### Test Skeleton: Metrics Scraper

```go
func TestMetricsScraper_ScrapeInto(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintln(w, `# HELP workqueue_depth Current depth of workqueue`)
        fmt.Fprintln(w, `# TYPE workqueue_depth gauge`)
        fmt.Fprintln(w, `workqueue_depth{name="podclique"} 5`)
    }))
    defer srv.Close()

    // Unit test bypasses port-forward via WithHTTPClient.
    // When client is set, ScrapeInto uses each endpoint's URL directly.
    scraper := NewMetricsScraper(
        []ServiceEndpoint{{Name: "operator", URL: srv.URL}},
        nil, nil, NewLogger(t),
    ).WithHTTPClient(srv.Client())
    result := &ScaleTestResult{}
    err := scraper.ScrapeInto(context.Background(), result)
    // assert no error, len(result.PrometheusMetrics) > 0, Source == "operator"
}
```

---

## 9. Edge Cases and Constraints

### Concurrency
- `TimelineTracker` is single-goroutine — phases and milestones recorded sequentially by the test
- `MilestoneCondition` implementations use internal watch goroutines but `Met()` is called sequentially by the tracker
- `MetricsScraper` and `ProfileCollector` are single-goroutine — no concurrency concerns
- `ScaleTestResult` is NOT thread-safe — building blocks write to it sequentially after their work completes. If parallel scraping is added in the future, a mutex must be added to protect `PrometheusMetrics` and `Profiles` slices

### Pod Watch (inside MilestoneCondition) — Hard Requirement

All pod-counting conditions MUST use `watchtools.UntilWithSync` (not LIST polling):
- `UntilWithSync` handles initial list + watch with automatic retry and resource version bookkeeping
- The watch goroutine maintains an `atomic.Int32` counter — O(1) reads per `Met()` call
- At 5000+ pods, LIST polling would generate 1 LIST call per poll interval per milestone, overwhelming the API server
- The watch goroutine maintains a **counter** (not a slice of events) to avoid unbounded memory growth
- This is a hard requirement, not an optimization

### Known Limitations
- **Metrics are point-in-time snapshots** — /metrics returns counters/gauges at scrape time. No time-range queries.
- **Pyroscope app name must match** — `appName` parameter must match the label configured in Pyroscope scraping.
- **Port-forward helper supports pod-backed services only** — services without pod targets are unsupported and return `ErrServiceNotPodBacked`.

### RunID and Label Collision Prevention
- RunID format: `scale-<unix_seconds>-<random_5char>` (e.g., `scale-1709568000-x7k2m`)
- Random suffix prevents label collision with zombie pods from prior failed test runs
- Tests should assert zero pre-existing pods matching `grove.io/scale-test-run=<runID>` before starting

### Recommended Timeouts Per Scale Tier

| Scale | Recommended ctx Timeout |
|-------|------------------------|
| 100 pods | 2 minutes |
| 500 pods | 5 minutes |
| 1000 pods | 10 minutes |
| 5000 pods | 30 minutes |

### Port-Forward Lifecycle
- `PortForwardSession` only constructed after goroutine confirms startup via `readyChan`
- `PortForwardSession.Close()` uses `sync.Once` — safe to call multiple times
- `done` channel ensures `Close()` blocks until forwarder goroutine exits
- `errOut` buffer captures SPDY errors for diagnostics
- Context cancellation triggers session teardown automatically
- If service has no backing pod, `PortForwardToService` returns `ErrServiceNotPodBacked` immediately

---

## 10. Files to Reference During Implementation

| File | What to Reuse |
|------|--------------|
| `operator/e2e/tests/rolling_update_tracker.go` | Watch goroutine + ready-channel pattern |
| `operator/e2e/utils/k8s_client.go` | `ApplyYAMLFile`, `WaitForPods`, `ListPods` patterns |
| `operator/e2e/utils/grove_resources.go` | GVR definitions |
| `my-own-hack/test/scale/analyze-metrics.sh` | Expected `.prom` metric names — will need updating for new gauge format |
| `my-own-hack/test/scale/visualize-metrics.py` | Metric parsing — update to handle gauge format |
| `operator/e2e/tests/cert_management_test.go` | Port-forward pattern (lines 606-708) |

---

## 11. Verification

1. **Compile:** `cd operator && go build -tags=e2e ./e2e/...`
2. **Unit tests:** `cd operator && go test ./e2e/utils/ -run TestScaleTestResult` (no `e2e` tag needed)
3. **Unit tests:** `cd operator && go test ./e2e/utils/ -run TestTimelineTracker`
4. **Unit tests:** `cd operator && go test ./e2e/utils/ -run TestMetricsScraper`
5. **Lint:** `make lint`
6. **Integration** (with cluster + monitoring stack):
    - Verify scraper auto-port-forwards to operator metrics service
    - Optionally verify additional pod-backed metrics services if configured by the test
    - Verify `PortForwardToPod` works with an explicit operator pod name (deterministic pod target)
    - Apply multiple PCS manifests with `grove.io/scale-test-run` label
    - Verify `results/metrics-*.prom` contains phase/milestone timing gauges
    - Verify `results/results-*.json` contains Phases with milestones and `float64` seconds
    - If Pyroscope is installed, verify `results/profiles/` has `cpu`, `memory`, `goroutine` `.pprof` files

# GREP-498: Controller Reconciliation Prometheus Metrics

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Overview](#overview)
  - [Relationship to Built-in Metrics](#relationship-to-built-in-metrics)
  - [User Stories](#user-stories)
    - [Story 1: Diagnosing Slow Deployment at Scale](#story-1-diagnosing-slow-deployment-at-scale)
    - [Story 2: Identifying Cross-Controller Resource Contention](#story-2-identifying-cross-controller-resource-contention)
    - [Story 3: Distinguishing Transient vs Persistent Errors](#story-3-distinguishing-transient-vs-persistent-errors)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [Metric Definitions](#metric-definitions)
    - [Sub-Operation Timing](#sub-operation-timing)
    - [Conflict Tracking](#conflict-tracking)
    - [Error Categorization](#error-categorization)
  - [MetricsContext Implementation](#metricscontext-implementation)
  - [Usage in Controllers](#usage-in-controllers)
  - [Conflict and Error Tracking](#conflict-and-error-tracking)
  - [Built-in controller-runtime Metrics Reference](#built-in-controller-runtime-metrics-reference)
  - [File Changes Summary](#file-changes-summary)
  - [Example PromQL Queries](#example-promql-queries)
  - [Monitoring](#monitoring)
  - [Dependencies](#dependencies)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Alternatives](#alternatives)
  - [Alt 1: Structured Logging Only](#alt-1-structured-logging-only)
  - [Alt 2: OpenTelemetry Tracing](#alt-2-opentelemetry-tracing)
  - [Alt 3: ObservedReconciler Wrapper for All Metrics](#alt-3-observedreconciler-wrapper-for-all-metrics)
- [Appendix](#appendix)
  - [Appendix A: Cross-Controller Conflict Storm Analysis](#appendix-a-cross-controller-conflict-storm-analysis)
  - [Appendix B: Terminology](#appendix-b-terminology)
<!-- /toc -->

## Summary

The Grove operator currently has **no custom Prometheus metrics**. While controller-runtime already provides built-in reconcile-level metrics (`controller_runtime_reconcile_total`, `controller_runtime_reconcile_time_seconds`, `controller_runtime_active_workers`) and workqueue metrics (`workqueue_depth`, `workqueue_queue_duration_seconds`, etc.), these only cover the overall reconcile outcome and duration. They provide no visibility into **which sub-operation within a reconcile is slow**, **how often 409 Conflicts occur on specific resource types**, or **what categories of errors are causing failures**.

This proposal adds three focused custom metrics to all Grove controllers (PodCliqueSet, PodClique, PodCliqueScalingGroup) that complement the existing built-in metrics, filling the observability gaps that cannot be addressed by controller-runtime alone.

## Motivation

During large-scale inference graph deployments using Grove (via NVIDIA Dynamo), we observed significant reconciliation performance degradation. Deploying an inference graph with 5 services × 10 replicas produces 50+ PodClique Pods, each transitioning through multiple states (Pending → Running → Ready). This generates a high rate of status changes that cascade through the Grove controller hierarchy (PodClique → PodCliqueScalingGroup → PodCliqueSet).

The built-in controller-runtime metrics tell us **that** reconciliation is slow or erroring, but not **why**:

1. **No sub-operation visibility** — Each reconcile performs multiple steps (ensureFinalizer, processRollingUpdate, syncResources, updateObservedGeneration, reconcileStatus). The built-in `controller_runtime_reconcile_time_seconds` only captures the total duration. We cannot determine which step is the bottleneck.

2. **No conflict attribution** — Status updates via `r.client.Status().Update()` and `r.client.Status().Patch()` can fail with 409 Conflict when an external controller (e.g., Dynamo's DGD controller) concurrently modifies the same PodClique/PodCliqueSet objects. The built-in `controller_runtime_reconcile_total{result="error"}` counts all errors equally — we cannot see the conflict rate or which resource type is affected.

3. **No error categorization** — The built-in metrics only distinguish `success`, `error`, `requeue`, and `requeue_after`. We cannot distinguish a transient 409 Conflict (likely to self-resolve on retry) from a persistent validation error or a timeout, making it impossible to prioritize debugging effort.

### Goals

* Add three custom Prometheus metrics to fill gaps not covered by controller-runtime built-in metrics
* Track sub-operation timing within each reconcile loop to identify internal bottlenecks
* Track 409 Conflict errors with target-resource-kind attribution to diagnose cross-controller contention
* Track error categorization by Kubernetes error type for targeted debugging
* Document the full set of available metrics (built-in + custom) for Grove operators
* Design the framework as extensible — controller-specific business metrics can be added incrementally

### Non-Goals

* This proposal does NOT duplicate any controller-runtime built-in metrics
* This proposal does NOT add metrics to the Grove scheduler component (only the operator)
* This proposal does NOT change reconciliation logic; it is purely additive instrumentation
* This proposal does NOT define alert thresholds or SLOs (those are deployment-specific)
* This proposal does NOT add metrics to upstream consumers of Grove (e.g., Dynamo Operator)
* Architectural mitigations for the conflict storm (e.g., watch predicate throttling, SSA adoption) require separate proposals informed by data this proposal provides

## Proposal

### Overview

Introduce a lightweight `MetricsContext` mechanism that controllers use to record sub-operation timings, conflict events, and error classifications. The context is injected at the reconcile entry point and propagated via Go's `context.Context`.

New package: `operator/internal/controller/metrics/` containing:

1. **`metrics.go`** — Three custom Prometheus metric definitions and `InitMetrics()` registration.
2. **`metrics_context.go`** — `MetricsContext` struct, context helpers, `RecordSubOperation`, `RecordConflict`, and `RecordError`.

No `ObservedReconciler` wrapper is needed — controller-runtime already handles reconcile-level metrics. The `MetricsContext` is created at the beginning of each controller's `Reconcile()` method and used throughout the reconcile flow.

### Relationship to Built-in Metrics

The following metrics are already provided by controller-runtime and should NOT be duplicated:

| Built-in Metric | What It Provides |
|----------------|-----------------|
| `controller_runtime_reconcile_time_seconds` | Total reconcile duration per controller (histogram) |
| `controller_runtime_reconcile_total` | Reconcile count by result: success, error, requeue, requeue_after |
| `controller_runtime_active_workers` | In-flight reconciliation count per controller |
| `controller_runtime_max_concurrent_reconciles` | Configured MaxConcurrentReconciles per controller |
| `workqueue_depth` | Current work queue depth per controller |
| `workqueue_adds_total` | Total items added to work queue |
| `workqueue_queue_duration_seconds` | Time items wait in queue before processing |
| `workqueue_work_duration_seconds` | Time spent processing items |
| `workqueue_retries_total` | Total retries |
| `rest_client_requests_total` | API requests by verb and status code |
| `rest_client_request_duration_seconds` | API request latency by verb and URL |

This proposal adds **only** what the built-in metrics cannot provide.

### User Stories

#### Story 1: Diagnosing Slow Deployment at Scale

As a platform operator deploying large inference graphs via Grove, I see from `controller_runtime_reconcile_time_seconds` that PodClique reconciliations are taking 5+ seconds. I need to see **which sub-operation** is slow (is it `sync_pods`, `reconcile_status`, or `process_rolling_update`?) so that I can file a targeted bug report or tune the system.

#### Story 2: Identifying Cross-Controller Resource Contention

As a developer integrating Grove with an upstream operator (e.g., Dynamo), I see from `controller_runtime_reconcile_total{result="error"}` that errors are increasing. I need to know **how many are 409 Conflicts** and **which resource type** (PodClique vs PodCliqueSet) is the contention hotspot, so that I can determine the right mitigation strategy.

#### Story 3: Distinguishing Transient vs Persistent Errors

As a cluster administrator, I see reconcile errors but cannot tell if they are transient conflicts (self-resolving on retry) or persistent validation errors (requiring manual intervention). I need error categorization by Kubernetes error type.

### Limitations/Risks & Mitigations

| Risk | Mitigation |
|------|------------|
| Label cardinality from `operation` × histogram buckets | `operation` values are bounded and explicitly defined per controller (~6 per controller). Without `namespace` label, estimated total new time series: ~500-1,000, very low. |
| Performance overhead of metric recording | All metric operations (Inc, Observe) are O(1) atomic operations. Overhead is < 1μs per call. The `MetricsContext` nil-check pattern ensures zero overhead when not injected. |

## Design Details

### Metric Definitions

All custom metrics use the `grove_operator` namespace prefix and are registered with the controller-runtime metrics registry via `InitMetrics()`.

#### Sub-Operation Timing

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `grove_operator_reconcile_sub_operation_duration_seconds` | Histogram | `controller`, `operation` | Duration of individual sub-operations within a reconcile loop |

**`operation` label values per controller:**

| Controller | Operations |
|------------|------------|
| PodCliqueSet | `ensure_finalizer`, `process_generation_hash`, `sync_resources`, `update_observed_generation`, `reconcile_status` |
| PodClique | `ensure_finalizer`, `process_rolling_update`, `sync_pods`, `update_observed_generation`, `reconcile_status` |
| PodCliqueScalingGroup | `ensure_finalizer`, `process_rolling_update`, `sync_resources`, `update_observed_generation`, `reconcile_status` |

#### Conflict Tracking

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `grove_operator_status_update_conflict_total` | Counter | `controller`, `target_kind` | Count of 409 Conflict errors on Status Update/Patch operations, attributed to the target resource kind |

**`target_kind` values:** `PodCliqueSet`, `PodClique`, `PodCliqueScalingGroup`

This metric answers "which resource type is the contention hotspot?" — a question the built-in `controller_runtime_reconcile_total{result="error"}` cannot answer because it only counts errors at the reconcile level without identifying the target resource.

#### Error Categorization

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `grove_operator_reconcile_errors_total` | Counter | `controller`, `error_type` | Reconciliation errors categorized by Kubernetes error type |

**`error_type` values:** `not_found`, `conflict`, `already_exists`, `timeout`, `forbidden`, `invalid`, `rate_limited`, `internal`

This metric answers "what kinds of errors are occurring?" — the built-in metrics only tell you `result="error"` without further classification.

### MetricsContext Implementation

```go
package metrics

import (
    "context"
    "time"

    "github.com/prometheus/client_golang/prometheus"
    k8serrors "k8s.io/apimachinery/pkg/api/errors"
    ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

const metricsNamespace = "grove_operator"

var (
    reconcileSubOpDuration = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Namespace: metricsNamespace,
            Name:      "reconcile_sub_operation_duration_seconds",
            Help:      "Duration of individual sub-operations within a reconcile loop",
            Buckets:   []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
        },
        []string{"controller", "operation"},
    )

    statusUpdateConflictTotal = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Namespace: metricsNamespace,
            Name:      "status_update_conflict_total",
            Help:      "Total 409 Conflict errors on Status Update/Patch, by target resource kind",
        },
        []string{"controller", "target_kind"},
    )

    reconcileErrorsTotal = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Namespace: metricsNamespace,
            Name:      "reconcile_errors_total",
            Help:      "Reconciliation errors categorized by Kubernetes error type",
        },
        []string{"controller", "error_type"},
    )
)

func InitMetrics() {
    ctrlmetrics.Registry.MustRegister(
        reconcileSubOpDuration,
        statusUpdateConflictTotal,
        reconcileErrorsTotal,
    )
}
```

```go
package metrics

import (
    "context"
    "time"

    k8serrors "k8s.io/apimachinery/pkg/api/errors"
)

type contextKey struct{}

type MetricsContext struct {
    controllerName string
}

func NewMetricsContext(controllerName string) *MetricsContext {
    return &MetricsContext{controllerName: controllerName}
}

func WithMetricsContext(ctx context.Context, mc *MetricsContext) context.Context {
    return context.WithValue(ctx, contextKey{}, mc)
}

func MetricsContextFromContext(ctx context.Context) *MetricsContext {
    mc, _ := ctx.Value(contextKey{}).(*MetricsContext)
    return mc
}

func (mc *MetricsContext) RecordSubOperation(operation string, start time.Time) {
    if mc == nil {
        return
    }
    reconcileSubOpDuration.WithLabelValues(
        mc.controllerName, operation,
    ).Observe(time.Since(start).Seconds())
}

func (mc *MetricsContext) RecordConflict(targetKind string) {
    if mc == nil {
        return
    }
    statusUpdateConflictTotal.WithLabelValues(
        mc.controllerName, targetKind,
    ).Inc()
}

func (mc *MetricsContext) RecordError(err error) {
    if mc == nil || err == nil {
        return
    }
    reconcileErrorsTotal.WithLabelValues(
        mc.controllerName, CategorizeError(err),
    ).Inc()
}

func CategorizeError(err error) string {
    switch {
    case k8serrors.IsNotFound(err):
        return "not_found"
    case k8serrors.IsConflict(err):
        return "conflict"
    case k8serrors.IsAlreadyExists(err):
        return "already_exists"
    case k8serrors.IsTimeout(err), k8serrors.IsServerTimeout(err):
        return "timeout"
    case k8serrors.IsForbidden(err):
        return "forbidden"
    case k8serrors.IsInvalid(err):
        return "invalid"
    case k8serrors.IsTooManyRequests(err):
        return "rate_limited"
    default:
        return "internal"
    }
}
```

### Usage in Controllers

No `ObservedReconciler` wrapper is needed. Controllers create a `MetricsContext` at the beginning of their `Reconcile()` method:

```go
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    logger := ctrllogger.FromContext(ctx).WithName(controllerName)

    mc := metrics.NewMetricsContext(controllerName)
    ctx = metrics.WithMetricsContext(ctx, mc)

    // ... existing reconcile logic unchanged ...
}
```

Sub-operations are timed at step boundaries in `reconcileSpec` and `reconcileStatus`:

```go
func (r *Reconciler) reconcileSpec(ctx context.Context, logger logr.Logger, pclq *grovecorev1alpha1.PodClique) ctrlcommon.ReconcileStepResult {
    mc := metrics.MetricsContextFromContext(ctx)

    start := time.Now()
    if stepResult := r.ensureFinalizer(ctx, logger, pclq); ctrlcommon.ShortCircuitReconcileFlow(stepResult) {
        return r.recordIncompleteReconcile(ctx, logger, pclq, &stepResult)
    }
    mc.RecordSubOperation("ensure_finalizer", start)

    start = time.Now()
    if stepResult := r.processRollingUpdate(ctx, logger, pclq); ctrlcommon.ShortCircuitReconcileFlow(stepResult) {
        return r.recordIncompleteReconcile(ctx, logger, pclq, &stepResult)
    }
    mc.RecordSubOperation("process_rolling_update", start)

    start = time.Now()
    if stepResult := r.syncPCLQResources(ctx, logger, pclq); ctrlcommon.ShortCircuitReconcileFlow(stepResult) {
        return r.recordIncompleteReconcile(ctx, logger, pclq, &stepResult)
    }
    mc.RecordSubOperation("sync_pods", start)

    start = time.Now()
    if stepResult := r.updateObservedGeneration(ctx, logger, pclq); ctrlcommon.ShortCircuitReconcileFlow(stepResult) {
        return r.recordIncompleteReconcile(ctx, logger, pclq, &stepResult)
    }
    mc.RecordSubOperation("update_observed_generation", start)

    return ctrlcommon.ContinueReconcile()
}
```

### Conflict and Error Tracking

Conflicts and errors are recorded at the call sites where `Status().Update()` or `Status().Patch()` may fail:

```go
func (r *Reconciler) reconcileStatus(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
    mc := metrics.MetricsContextFromContext(ctx)

    // ... existing logic to mutate pcs.Status ...

    if err = r.client.Status().Update(ctx, pcs); err != nil {
        if k8serrors.IsConflict(err) {
            mc.RecordConflict("PodCliqueSet")
        }
        mc.RecordError(err)
        return ctrlcommon.ReconcileWithErrors("failed to update PodCliqueSet status", err)
    }
    return ctrlcommon.ContinueReconcile()
}
```

### Built-in controller-runtime Metrics Reference

The following metrics are already available and should be used for reconcile-level monitoring. They require no code changes:

| Metric | What It Tells You |
|--------|------------------|
| `controller_runtime_reconcile_time_seconds{controller="..."}` | How long reconciliations take (P50/P95/P99) |
| `controller_runtime_reconcile_total{controller="...",result="..."}` | Reconcile count by outcome (success/error/requeue) |
| `controller_runtime_active_workers{controller="..."}` | Current in-flight reconcile count |
| `controller_runtime_max_concurrent_reconciles{controller="..."}` | Configured concurrency limit |
| `workqueue_depth{name="..."}` | Work queue backlog |
| `workqueue_queue_duration_seconds{name="..."}` | How long items wait in queue |
| `workqueue_retries_total{name="..."}` | Retry count |

### File Changes Summary

| File | Change |
|------|--------|
| `internal/controller/metrics/metrics.go` | **New**: 3 metric definitions + `InitMetrics()` |
| `internal/controller/metrics/metrics_context.go` | **New**: `MetricsContext` with `RecordSubOperation`, `RecordConflict`, `RecordError` |
| `internal/controller/manager.go` | Add `metrics.InitMetrics()` call |
| `internal/controller/podcliqueset/reconciler.go` | Create and inject `MetricsContext` |
| `internal/controller/podcliqueset/reconcilespec.go` | Add `mc.RecordSubOperation()` at step boundaries |
| `internal/controller/podcliqueset/reconcilestatus.go` | Add `mc.RecordSubOperation()`, `mc.RecordConflict()`, `mc.RecordError()` |
| `internal/controller/podclique/reconciler.go` | Create and inject `MetricsContext` |
| `internal/controller/podclique/reconcilespec.go` | Add `mc.RecordSubOperation()` at step boundaries |
| `internal/controller/podclique/reconcilestatus.go` | Add `mc.RecordSubOperation()`, `mc.RecordConflict()`, `mc.RecordError()` |
| `internal/controller/podcliquescalinggroup/reconciler.go` | Create and inject `MetricsContext` |
| `internal/controller/podcliquescalinggroup/reconcilespec.go` | Add `mc.RecordSubOperation()` at step boundaries |
| `internal/controller/podcliquescalinggroup/reconcilestatus.go` | Add `mc.RecordSubOperation()`, `mc.RecordConflict()`, `mc.RecordError()` |

### Example PromQL Queries

```promql
# ===== Built-in Metrics (already available) =====

# P95 reconcile duration by controller
histogram_quantile(0.95,
  sum by (controller, le) (
    rate(controller_runtime_reconcile_time_seconds_bucket[5m])
  )
)

# Reconcile rate by result
sum by (controller, result) (
  rate(controller_runtime_reconcile_total[5m])
)

# In-flight reconciliations vs configured max
controller_runtime_active_workers / controller_runtime_max_concurrent_reconciles

# Work queue depth per controller
workqueue_depth{name=~"podcliqueset-controller|podclique-controller|podcliquescalinggroup-controller"}

# Work queue wait time P99
histogram_quantile(0.99,
  sum by (name, le) (
    rate(workqueue_queue_duration_seconds_bucket[5m])
  )
)

# ===== New Custom Metrics (added by this proposal) =====

# Sub-operation P95 latency for PodClique — where is time spent?
histogram_quantile(0.95,
  sum by (operation, le) (
    rate(grove_operator_reconcile_sub_operation_duration_seconds_bucket{
      controller="podclique-controller"
    }[5m])
  )
)

# 409 Conflict rate — which resource is the contention hotspot?
sum by (controller, target_kind) (
  rate(grove_operator_status_update_conflict_total[5m])
)

# Error breakdown by type — transient conflicts vs persistent errors
sum by (controller, error_type) (
  rate(grove_operator_reconcile_errors_total[5m])
)

# Conflict-to-total-error ratio — what fraction of errors are conflicts?
sum(rate(grove_operator_reconcile_errors_total{error_type="conflict"}[5m]))
/
sum(rate(controller_runtime_reconcile_total{result="error"}[5m]))
```

### Monitoring

The custom metrics introduced by this proposal, combined with the built-in controller-runtime metrics, provide:

1. **Sub-Operation Bottleneck Identification**: Per-step latency breakdown to pinpoint slow operations (e.g., `sync_pods` vs `reconcile_status`).
2. **Conflict Detection and Attribution**: 409 Conflict rate by target resource kind, identifying cross-controller contention hotspots.
3. **Error Triage**: Errors categorized by Kubernetes error type, distinguishing transient conflicts from persistent failures.

The built-in metrics already provide reconciliation health (duration, throughput, success rate), capacity saturation (active workers, queue depth), and API client performance.

Recommended alert examples (not enforced by this proposal):
- `grove_operator_status_update_conflict_total` rate > 1/s → cross-controller contention
- `grove_operator_reconcile_errors_total{error_type="timeout"}` rate > 0.1/s → API server pressure
- `workqueue_depth{name="podclique-controller"}` > 50 for 5m → PodClique controller falling behind

### Dependencies

- `github.com/prometheus/client_golang` — already an indirect dependency via controller-runtime
- `sigs.k8s.io/controller-runtime/pkg/metrics` — already used for the metrics server endpoint

No new external dependencies are introduced.

### Test Plan

1. **Unit tests** for `MetricsContext`: Verify `RecordSubOperation`, `RecordConflict`, and `RecordError` correctly observe histograms and increment counters.
2. **Unit tests** for `CategorizeError`: Verify error categorization for all Kubernetes error types.
3. **Integration test**: Deploy Grove, create a PodCliqueSet, and verify custom metrics are exposed at the `/metrics` endpoint alongside the existing built-in metrics.
4. **E2E test**: Deploy at scale (multiple PodCliqueSets with many replicas), scrape metrics, and verify sub-operation and conflict metrics are populated.

### Graduation Criteria

**Alpha (this proposal):**
- Three custom metrics registered and recorded in all three controllers
- Sub-operation timing at major step boundaries
- Conflict tracking on status update/patch calls
- Error categorization on reconcile errors
- Documentation covering both built-in and custom metrics
- Unit tests for all new code

**Beta (future):**
- Grafana dashboard JSON included in the chart (combining built-in and custom metrics)
- ServiceMonitor template in Helm chart
- Additional controller-specific metrics based on alpha data analysis

**GA (future):**
- Metrics stable for 2+ releases
- Community feedback incorporated
- Alert rule examples based on production usage patterns

## Implementation History

- 2026-03-11: Initial GREP draft created
- 2026-03-24: PR #499 opened
- 2026-03-30: Updated based on review feedback — removed 4 duplicate metrics, removed namespace label, refocused on 3 genuinely new metrics

## Alternatives

### Alt 1: Structured Logging Only

**Pros:** No code changes to metrics infrastructure; flexible querying.

**Cons:** Cannot create real-time dashboards or alerts; higher storage cost; slower to query than Prometheus; no time-series aggregation.

**Rejected because:** Prometheus metrics are the established Kubernetes ecosystem standard. Log analysis is complementary but not a substitute for time-series monitoring.

### Alt 2: OpenTelemetry Tracing

**Pros:** Rich per-request trace context with spans; distributed tracing across operator → API server.

**Cons:** Requires additional infrastructure (Jaeger/Tempo); higher per-request overhead; not yet standard in controller-runtime; overkill for aggregate monitoring.

**Rejected because:** Tracing is valuable for deep-dive debugging but requires significant infrastructure investment. Prometheus metrics provide the aggregate view needed for dashboards and alerts with minimal overhead. Tracing can be added later.

### Alt 3: ObservedReconciler Wrapper for All Metrics

**Pros:** Single wrapper provides all metrics automatically without per-controller changes.

**Cons:** Duplicates controller-runtime built-in metrics (`controller_runtime_reconcile_time_seconds`, `controller_runtime_reconcile_total`, `controller_runtime_active_workers`). Creates confusion with two sources of truth for the same data.

**Rejected because:** controller-runtime already provides reconcile-level metrics. Wrapping the reconciler would duplicate these. The `MetricsContext` approach focuses on what's genuinely missing (sub-operation timing, conflict attribution, error categorization) without duplication.

## Appendix

### Appendix A: Cross-Controller Conflict Storm Analysis

When Grove is used by an upstream operator (e.g., NVIDIA Dynamo), the following contention pattern occurs during large-scale deployments:

**Object ownership:**
```
Upstream Operator (e.g., Dynamo DGD Controller)
  creates → PodCliqueSet (PCS)
  scales  → PodClique (PC) / PodCliqueScalingGroup (PCSG) via Scale subresource
  reads   → PC/PCSG status for readiness checks

Grove Operator
  PCS Controller → creates PC/PCSG, updates PCS status
  PC Controller  → creates Pods, updates PC status (replicas, readyReplicas)
  PCSG Controller → manages PC replicas, updates PCSG status
```

**Contention mechanism:**

1. Upstream operator scales PodClique via Scale subresource (Get scale → Update scale).
2. Between the Get and Update, Grove's PC controller updates PC status (e.g., readyReplicas changes as a Pod becomes Ready).
3. The upstream operator's Scale Update receives 409 Conflict (resourceVersion mismatch).
4. Similarly, Grove's PCS `reconcileStatus` calls `r.client.Status().Update(ctx, pcs)`. If the upstream operator concurrently modifies PCS, Grove's status update can also conflict.

**Why Grove needs these metrics:**

Without metrics on the Grove side, we can only see the upstream operator's view of the conflict. The `grove_operator_status_update_conflict_total` metric provides the other half: how often Grove's own status updates fail due to concurrent external writes. Combined with the upstream operator's metrics, this gives a complete picture of the contention and guides architectural decisions (e.g., switching from `Status().Update()` to field-level `Status().Patch()`, or using Server-Side Apply).

**Future work enabled by these metrics:**

1. **Status Patch optimization** — If `status_update_conflict_total` is high for PCS, consider switching `reconcileStatus` from `Status().Update()` to field-level `Status().Patch()` to reduce conflict surface.
2. **Rate limiter tuning** — Use built-in `controller_runtime_active_workers` and `workqueue_depth` data to tune `MaxConcurrentReconciles` per controller.
3. **Watch predicate optimization** — Use sub-operation latency data to determine if watch predicates should be tightened to reduce unnecessary reconcile triggers.
4. **Upstream coordination** — Share conflict data with upstream operator teams to jointly optimize the interaction pattern.

### Appendix B: Terminology

| Term | Definition |
|------|------------|
| **PCS** | PodCliqueSet — top-level Grove CRD representing a set of PodCliques |
| **PC / PCLQ** | PodClique — Grove CRD representing a group of co-scheduled Pods |
| **PCSG** | PodCliqueScalingGroup — Grove CRD for managing multiple PodClique replicas |
| **Conflict storm** | A pattern where concurrent writes to the same Kubernetes object produce cascading 409 Conflict errors |
| **Optimistic concurrency** | Kubernetes mechanism where every object has a `resourceVersion`; an Update must match the current version or receive 409 Conflict |
| **Sub-operation** | A discrete step within a reconciliation loop (e.g., syncResources, reconcileStatus) |

> NOTE: This GREP template has been inspired by [KEP Template](https://github.com/kubernetes/enhancements/blob/f90055d254c356b2c038a1bdf4610bf4acd8d7be/keps/NNNN-kep-template/README.md).

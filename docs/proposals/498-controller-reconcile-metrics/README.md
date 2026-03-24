# GREP-498: Controller Reconciliation Prometheus Metrics

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Diagnosing Slow Deployment at Scale](#story-1-diagnosing-slow-deployment-at-scale)
    - [Story 2: Identifying Cross-Controller Resource Contention](#story-2-identifying-cross-controller-resource-contention)
    - [Story 3: Capacity Planning for MaxConcurrentReconciles](#story-3-capacity-planning-for-maxconcurrentreconciles)
  - [Limitations/Risks & Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [Metric Definitions](#metric-definitions)
  - [ObservedReconciler Wrapper](#observedreconciler-wrapper)
  - [MetricsContext for Sub-Operation Timing](#metricscontext-for-sub-operation-timing)
  - [Conflict Tracking](#conflict-tracking)
  - [Built-in controller-runtime Metrics](#built-in-controller-runtime-metrics)
  - [File Changes Summary](#file-changes-summary)
  - [Example PromQL Queries](#example-promql-queries)
  - [Monitoring](#monitoring)
  - [Dependencies](#dependencies)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Alternatives](#alternatives)
- [Appendix](#appendix)
  - [Appendix A: Cross-Controller Conflict Storm Analysis](#appendix-a-cross-controller-conflict-storm-analysis)
  - [Appendix B: Terminology](#appendix-b-terminology)
<!-- /toc -->

## Summary

The Grove operator currently exposes **zero custom Prometheus metrics**. The three controllers (PodCliqueSet, PodClique, PodCliqueScalingGroup) have no observability into reconciliation performance, sub-operation latency, error categorization, or resource contention. This proposal adds a comprehensive Prometheus metrics framework to all Grove controllers, providing reconcile-level and sub-operation-level visibility. When large numbers of PodCliques and Pods are created — particularly when Grove is used by an upstream operator like Dynamo — reconciliation performance degrades due to high event rates and cross-controller resource contention, but there is currently no way to observe, quantify, or diagnose these issues.

## Motivation

During large-scale inference graph deployments using Grove (via NVIDIA Dynamo), we observed significant reconciliation performance degradation. Deploying an inference graph with 5 services × 10 replicas produces 50+ PodClique Pods, each transitioning through multiple states (Pending → Running → Ready). This generates a high rate of status changes that cascade through the Grove controller hierarchy (PodClique → PodCliqueScalingGroup → PodCliqueSet).

**The fundamental problem**: Grove has no metrics to answer basic operational questions:

1. **How long do reconciliations take?** There is no duration tracking for any controller's reconcile loop.
2. **Which sub-operation is the bottleneck?** Each reconcile performs multiple steps (ensureFinalizer, processRollingUpdate, syncResources, updateObservedGeneration, reconcileStatus) but we cannot see which step is slow.
3. **How often do API calls fail?** Status updates via `r.client.Status().Update()` and `r.client.Status().Patch()` can fail with 409 Conflict when an external controller (e.g., Dynamo's DGD controller) concurrently modifies the same PodClique/PodCliqueSet objects. We cannot see the conflict rate.
4. **How deep is the work queue?** We have no visibility into per-controller queue depth, queue wait time, or event processing rate.
5. **What is the reconcile throughput?** We cannot determine if controllers are keeping up with the event rate.

This lack of observability means that performance issues require ad-hoc debugging via log analysis. Operators cannot set alerts, create dashboards, or make data-driven tuning decisions.

### Goals

* Add Prometheus metrics to all three Grove controllers (PodCliqueSet, PodClique, PodCliqueScalingGroup) with a consistent, general-purpose framework
* Track reconcile duration, count, error rate, and error categorization per controller
* Track sub-operation timing within each reconcile loop to identify bottlenecks
* Track in-flight (concurrent) reconciliation count per controller
* Track 409 Conflict errors with target-resource attribution to diagnose cross-controller contention
* Verify and document controller-runtime built-in workqueue and REST client metrics
* Design the framework as an extensible foundation — controller-specific business metrics can be added incrementally in the future
* Provide example Grafana dashboard panels and PromQL queries

### Non-Goals

* This proposal does NOT add metrics to the Grove scheduler component (only the operator)
* This proposal does NOT change reconciliation logic; it is purely additive instrumentation
* This proposal does NOT define alert thresholds or SLOs (those are deployment-specific)
* This proposal does NOT add metrics to upstream consumers of Grove (e.g., Dynamo Operator)
* This proposal does NOT prescribe architectural mitigations for performance issues (e.g., watch predicate throttling); those require separate proposals informed by the data this proposal provides
* Detailed per-controller business-logic metrics (e.g., rolling update step counts, PodGang recreation rates) are deferred to future incremental additions

## Proposal

### Overview

Introduce a `metrics` package under `operator/internal/controller/metrics/` containing:

1. **`ObservedReconciler`** — A wrapper around any `reconcile.Reconciler` that automatically records total reconcile duration, count, errors, in-flight count, and requeue reasons. All three controllers will be wrapped with this.
2. **`MetricsContext`** — A struct injected into `context.Context` that controllers can use to record sub-operation timings and conflict events. This is the extension point for future metrics.
3. **Metric definitions** — All Prometheus metric variables and the `InitMetrics()` registration function.

All metrics use the `grove_operator` namespace prefix.

### User Stories

#### Story 1: Diagnosing Slow Deployment at Scale

As a platform operator deploying large inference graphs via Grove, I want to see which sub-operation within PodClique reconciliation is slow (e.g., is it `sync_pods`, `reconcile_status`, or `update_observed_generation`?) so that I can file a targeted bug report or tune the system.

#### Story 2: Identifying Cross-Controller Resource Contention

As a developer integrating Grove with an upstream operator (e.g., Dynamo), I want to see the 409 Conflict rate on PodClique and PodCliqueSet status updates, broken down by target resource kind, so that I can determine if cross-controller contention is the root cause of reconciliation failures.

#### Story 3: Capacity Planning for MaxConcurrentReconciles

As a cluster administrator, I want to see the in-flight reconciliation count and work queue depth per controller so that I can decide whether to increase `MaxConcurrentReconciles` for the PodClique controller during large-scale deployments.

### Limitations/Risks & Mitigations

| Risk | Mitigation |
|------|------------|
| Label cardinality explosion from `namespace` × `operation` × histogram buckets | `operation` values are bounded and explicitly defined per controller (~5-8 per controller). Estimated total new time series in a 10-namespace cluster: ~3,000-6,000, well within Prometheus capacity. |
| Performance overhead of metric recording | All metric operations (Inc, Observe) are O(1) atomic operations. Overhead is < 1μs per call. The `MetricsContext` nil-check pattern ensures zero overhead when metrics context is not injected. |
| `ObservedReconciler` wrapper changes the reconciler type passed to `Complete()` | This is the same pattern used by NVIDIA Dynamo's operator and by controller-runtime's own metrics. Fully compatible with `builder.Complete()`. |

## Design Details

### Metric Definitions

All metrics are registered with the controller-runtime metrics registry via `InitMetrics()`.

#### Reconcile-Level Metrics (recorded by ObservedReconciler)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `grove_operator_reconcile_duration_seconds` | Histogram | `controller`, `namespace`, `result` | Total duration of each reconciliation loop |
| `grove_operator_reconcile_total` | Counter | `controller`, `namespace`, `result` | Total number of reconciliations |
| `grove_operator_reconcile_errors_total` | Counter | `controller`, `namespace`, `error_type` | Total reconciliation errors, categorized by Kubernetes error type |
| `grove_operator_reconcile_inflight` | Gauge | `controller` | Number of reconciliations currently in progress |
| `grove_operator_reconcile_requeue_total` | Counter | `controller`, `namespace`, `reason` | Count of requeue events with categorized reasons |

**Label values:**
- `controller`: `PodCliqueSet`, `PodClique`, `PodCliqueScalingGroup`
- `result`: `success`, `error`, `requeue`
- `error_type`: `not_found`, `conflict`, `already_exists`, `timeout`, `forbidden`, `invalid`, `internal`
- `reason`: `explicit_requeue`, `conflict`, `timeout`, `internal`, etc.

#### Sub-Operation Metrics (recorded by controllers via MetricsContext)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `grove_operator_reconcile_sub_operation_duration_seconds` | Histogram | `controller`, `namespace`, `operation` | Duration of individual sub-operations within a reconcile loop |

**`operation` label values per controller:**

| Controller | Operations |
|------------|------------|
| PodCliqueSet | `get_resource`, `ensure_finalizer`, `process_generation_hash`, `sync_resources`, `update_observed_generation`, `reconcile_status` |
| PodClique | `get_resource`, `ensure_finalizer`, `process_rolling_update`, `sync_pods`, `update_observed_generation`, `reconcile_status` |
| PodCliqueScalingGroup | `get_resource`, `ensure_finalizer`, `process_rolling_update`, `sync_resources`, `update_observed_generation`, `reconcile_status` |

#### Conflict Tracking Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `grove_operator_status_update_conflict_total` | Counter | `controller`, `namespace`, `target_kind` | Count of 409 Conflict errors on Status Update/Patch operations |

This metric captures conflicts that occur when Grove's status updates race against an external controller (e.g., Dynamo scaling PodClique) modifying the same object. The `target_kind` label identifies which object type (PodCliqueSet, PodClique, PodCliqueScalingGroup) experienced the conflict.

### ObservedReconciler Wrapper

```go
package metrics

import (
    "context"
    "time"

    "github.com/prometheus/client_golang/prometheus"
    k8serrors "k8s.io/apimachinery/pkg/api/errors"
    ctrl "sigs.k8s.io/controller-runtime"
    ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
    "sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const metricsNamespace = "grove_operator"

type ObservedReconciler struct {
    reconcile.Reconciler
    controllerName string
}

func NewObservedReconciler(r reconcile.Reconciler, controllerName string) *ObservedReconciler {
    return &ObservedReconciler{Reconciler: r, controllerName: controllerName}
}

func (m *ObservedReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    reconcileInflight.WithLabelValues(m.controllerName).Inc()
    defer reconcileInflight.WithLabelValues(m.controllerName).Dec()

    mc := NewMetricsContext(m.controllerName, req.Namespace)
    ctx = WithMetricsContext(ctx, mc)

    startTime := time.Now()
    result, err := m.Reconciler.Reconcile(ctx, req)
    duration := time.Since(startTime)

    requeue := result.Requeue || result.RequeueAfter > 0

    resultLabel := "success"
    if err != nil {
        resultLabel = "error"
        reconcileErrors.WithLabelValues(m.controllerName, req.Namespace, categorizeError(err)).Inc()
    } else if requeue {
        resultLabel = "requeue"
    }

    reconcileDuration.WithLabelValues(m.controllerName, req.Namespace, resultLabel).Observe(duration.Seconds())
    reconcileTotal.WithLabelValues(m.controllerName, req.Namespace, resultLabel).Inc()

    if err != nil {
        reconcileRequeueTotal.WithLabelValues(m.controllerName, req.Namespace, categorizeError(err)).Inc()
    } else if requeue {
        reconcileRequeueTotal.WithLabelValues(m.controllerName, req.Namespace, "explicit_requeue").Inc()
    }

    return result, err
}

func categorizeError(err error) string {
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

### MetricsContext for Sub-Operation Timing

```go
package metrics

import (
    "context"
    "time"
)

type contextKey struct{}

type MetricsContext struct {
    controllerName string
    namespace      string
}

func NewMetricsContext(controllerName, namespace string) *MetricsContext {
    return &MetricsContext{controllerName: controllerName, namespace: namespace}
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
        mc.controllerName, mc.namespace, operation,
    ).Observe(time.Since(start).Seconds())
}

func (mc *MetricsContext) RecordConflict(targetKind string) {
    if mc == nil {
        return
    }
    statusUpdateConflictTotal.WithLabelValues(
        mc.controllerName, mc.namespace, targetKind,
    ).Inc()
}
```

### Conflict Tracking

Status update conflicts are recorded at the call sites where `r.client.Status().Update()` or `r.client.Status().Patch()` can return a 409 error. Key locations:

**PodCliqueSet controller:**
- `reconcileStatus` → `r.client.Status().Update(ctx, pcs)` — updates replica counts, conditions
- `reconcileSpec` → `setGenerationHash` / `initRollingUpdateProgress` → `r.client.Status().Update(ctx, pcs)`

**PodClique controller:**
- `reconcileStatus` → `r.client.Status().Patch(ctx, pclq, patch)` — updates replicas, readyReplicas, conditions
- `reconcileSpec` → `initOrResetRollingUpdate` → `r.client.Status().Patch(ctx, pclq, patch)`

**PodCliqueScalingGroup controller:**
- `reconcileStatus` → `r.client.Status().Patch(ctx, pcsg, patch)` — updates replicas, availability

Example instrumentation in PodCliqueSet `reconcileStatus`:

```go
func (r *Reconciler) reconcileStatus(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) ctrlcommon.ReconcileStepResult {
    // ... existing logic to mutate pcs.Status ...

    if err = r.client.Status().Update(ctx, pcs); err != nil {
        if k8serrors.IsConflict(err) {
            mc := metrics.MetricsContextFromContext(ctx)
            mc.RecordConflict("PodCliqueSet")
        }
        return ctrlcommon.ReconcileWithErrors("failed to update PodCliqueSet status", err)
    }
    return ctrlcommon.ContinueReconcile()
}
```

### Built-in controller-runtime Metrics

controller-runtime (v0.22.4) already exposes the following metrics via the standard workqueue and REST client instrumentation. These require no code changes but should be **verified and documented**:

**Work Queue Metrics** (labeled by controller `name`):
- `workqueue_depth` — current queue depth
- `workqueue_adds_total` — total items added
- `workqueue_queue_duration_seconds` — time items wait in queue before processing
- `workqueue_work_duration_seconds` — time spent processing items
- `workqueue_retries_total` — total retries
- `workqueue_unfinished_work_seconds` — unfinished work duration
- `workqueue_longest_running_processor_seconds` — longest running processor

**REST Client Metrics**:
- `rest_client_requests_total` — API requests by verb and status code
- `rest_client_request_duration_seconds` — request latency by verb and URL

### File Changes Summary

| File | Change |
|------|--------|
| `internal/controller/metrics/metrics.go` | **New**: Prometheus metric variable definitions, `InitMetrics()` registration |
| `internal/controller/metrics/reconciler_wrapper.go` | **New**: `ObservedReconciler` wrapper with duration, count, error, inflight, requeue tracking |
| `internal/controller/metrics/metrics_context.go` | **New**: `MetricsContext` struct, context helpers, `RecordSubOperation`, `RecordConflict` |
| `internal/controller/manager.go` | Add `metrics.InitMetrics()` call after manager creation |
| `internal/controller/podcliqueset/register.go` | Wrap reconciler with `metrics.NewObservedReconciler(r, "PodCliqueSet")` in `Complete()` |
| `internal/controller/podclique/register.go` | Wrap reconciler with `metrics.NewObservedReconciler(r, "PodClique")` in `Complete()` |
| `internal/controller/podcliquescalinggroup/register.go` | Wrap reconciler with `metrics.NewObservedReconciler(r, "PodCliqueScalingGroup")` in `Complete()` |
| `internal/controller/podcliqueset/reconcilespec.go` | Add `mc.RecordSubOperation()` calls at each step boundary |
| `internal/controller/podcliqueset/reconcilestatus.go` | Add `mc.RecordSubOperation()` and `mc.RecordConflict()` on 409 |
| `internal/controller/podclique/reconcilespec.go` | Add `mc.RecordSubOperation()` calls at each step boundary |
| `internal/controller/podclique/reconcilestatus.go` | Add `mc.RecordSubOperation()` and `mc.RecordConflict()` on 409 |
| `internal/controller/podcliquescalinggroup/reconcilespec.go` | Add `mc.RecordSubOperation()` calls at each step boundary |
| `internal/controller/podcliquescalinggroup/reconcilestatus.go` | Add `mc.RecordSubOperation()` and `mc.RecordConflict()` on 409 |
| `docs/` | Document all new metrics, built-in metrics, and example PromQL queries |

### Example PromQL Queries

```promql
# P95 reconcile duration by controller
histogram_quantile(0.95,
  sum by (controller, le) (
    rate(grove_operator_reconcile_duration_seconds_bucket[5m])
  )
)

# Reconcile error rate by error type
sum by (controller, error_type) (
  rate(grove_operator_reconcile_errors_total[5m])
)

# 409 Conflict rate — which resource is the contention hotspot?
sum by (controller, target_kind) (
  rate(grove_operator_status_update_conflict_total[5m])
)

# In-flight reconciliations — are controllers saturated?
grove_operator_reconcile_inflight

# Sub-operation P95 latency for PodClique — where is time spent?
histogram_quantile(0.95,
  sum by (operation, le) (
    rate(grove_operator_reconcile_sub_operation_duration_seconds_bucket{
      controller="PodClique"
    }[5m])
  )
)

# Work queue depth per controller (built-in)
workqueue_depth{name=~"PodCliqueSet|PodClique|PodCliqueScalingGroup"}

# Work queue wait time P99 (built-in)
histogram_quantile(0.99,
  sum by (name, le) (
    rate(workqueue_queue_duration_seconds_bucket{
      name=~"PodCliqueSet|PodClique|PodCliqueScalingGroup"
    }[5m])
  )
)

# Requeue rate by reason
sum by (controller, reason) (
  rate(grove_operator_reconcile_requeue_total[5m])
)
```

### Monitoring

The metrics introduced by this proposal provide the following monitoring capabilities:

1. **Reconciliation Health**: P50/P95/P99 reconcile duration, success/error/requeue rates per controller.
2. **Sub-Operation Bottleneck Identification**: Per-step latency breakdown (e.g., `sync_pods` vs `reconcile_status` in PodClique controller).
3. **Conflict Detection**: 409 Conflict rate with target-kind attribution, enabling detection of cross-controller contention.
4. **Capacity Saturation**: In-flight reconcile count vs MaxConcurrentReconciles, work queue depth and wait time.
5. **Error Classification**: Errors categorized by Kubernetes error type (not_found, conflict, timeout, etc.) for targeted debugging.

Recommended alert examples (not enforced by this proposal):
- `grove_operator_reconcile_errors_total{error_type="conflict"}` rate > 1/s → cross-controller contention
- `workqueue_depth{name="PodClique"}` > 50 for 5m → PodClique controller falling behind
- `grove_operator_reconcile_inflight{controller="PodClique"}` = MaxConcurrentReconciles for 5m → controller saturated

### Dependencies

- `github.com/prometheus/client_golang` — already an indirect dependency via controller-runtime
- `sigs.k8s.io/controller-runtime/pkg/metrics` — already used for the metrics server endpoint

No new external dependencies are introduced.

### Test Plan

1. **Unit tests** for `ObservedReconciler`: Verify that metrics are correctly recorded for success, error, and requeue outcomes. Mock reconciler returning different results.
2. **Unit tests** for `MetricsContext`: Verify `RecordSubOperation` and `RecordConflict` correctly increment counters and observe histograms.
3. **Unit tests** for `categorizeError`: Verify error categorization for all Kubernetes error types.
4. **Integration test**: Deploy Grove with metrics enabled, create a PodCliqueSet, and verify metrics are exposed at the `/metrics` endpoint with correct label values.
5. **E2E test**: Deploy at scale (multiple PodCliqueSets with many replicas), scrape metrics, and verify sub-operation and conflict metrics are populated.

### Graduation Criteria

**Alpha (this proposal):**
- All three controllers wrapped with `ObservedReconciler`
- Sub-operation timing at major step boundaries
- Conflict tracking on status update/patch calls
- Documentation and example PromQL queries
- Unit tests for all new code

**Beta (future):**
- Grafana dashboard JSON included in the chart
- ServiceMonitor template in Helm chart
- Additional controller-specific business metrics based on alpha data analysis

**GA (future):**
- Metrics stable for 2+ releases
- Community feedback incorporated
- Alert rule examples based on production usage patterns

## Implementation History

- 2026-03-11: Initial GREP draft created

## Alternatives

### Alt 1: Structured Logging Only

**Pros:** No code changes to metrics infrastructure; flexible querying.

**Cons:** Cannot create real-time dashboards or alerts; higher storage cost; slower to query than Prometheus; no time-series aggregation.

**Rejected because:** Prometheus metrics are the established Kubernetes ecosystem standard. Log analysis is complementary but not a substitute for time-series monitoring.

### Alt 2: OpenTelemetry Tracing

**Pros:** Rich per-request trace context with spans; distributed tracing across operator → API server.

**Cons:** Requires additional infrastructure (Jaeger/Tempo); higher per-request overhead; not yet standard in controller-runtime; overkill for aggregate monitoring.

**Rejected because:** Tracing is valuable for deep-dive debugging but requires significant infrastructure investment. Prometheus metrics provide the aggregate view needed for dashboards and alerts with minimal overhead. Tracing can be added later.

### Alt 3: Custom client.Client Wrapper

**Pros:** Automatically instruments all API calls without per-controller changes.

**Cons:** Loses controller context (which controller triggered the call); complex to implement (must handle all client.Client methods); client-go already provides `rest_client_*` metrics.

**Rejected because:** The `MetricsContext` approach provides controller attribution while being simpler. Combined with existing client-go metrics, this is sufficient.

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
4. Similarly, Grove's PCS `reconcileStatus` calls `r.client.Status().Update(ctx, pcs)`. If the upstream operator concurrently modifies PCS (e.g., spec update via SyncResource), Grove's status update can also conflict.

**Why Grove needs these metrics:**

Without metrics on the Grove side, we can only see the upstream operator's view of the conflict. The `grove_operator_status_update_conflict_total` metric provides the other half: how often Grove's own status updates are failing due to concurrent external writes. Combined with the upstream operator's metrics, this gives a complete picture of the contention and guides architectural decisions (e.g., switching from `Status().Update()` to `Status().Patch()` with optimistic merge, or using Server-Side Apply).

**Future work enabled by these metrics:**

1. **Status Patch optimization** — If `status_update_conflict_total` is high for PCS, consider switching PCS `reconcileStatus` from `Status().Update()` to field-level `Status().Patch()` to reduce conflict surface.
2. **Rate limiter tuning** — Use `reconcile_inflight` and `workqueue_depth` data to tune `MaxConcurrentReconciles` per controller.
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
| **Watch amplification** | The effect of Watch predicates triggering many reconciliations from frequent status changes |

> NOTE: This GREP template has been inspired by [KEP Template](https://github.com/kubernetes/enhancements/blob/f90055d254c356b2c038a1bdf4610bf4acd8d7be/keps/NNNN-kep-template/README.md).

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
  - [Metric Surface](#metric-surface)
    - [<code>grove_operator_status_update_conflict_total</code>](#grove_operator_status_update_conflict_total)
    - [<code>grove_operator_reconcile_errors_total</code>](#grove_operator_reconcile_errors_total)
  - [Cardinality Bound](#cardinality-bound)
  - [Recording Verbs (Contract Only)](#recording-verbs-contract-only)
  - [Built-in controller-runtime Metrics Reference](#built-in-controller-runtime-metrics-reference)
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
- [Future Work](#future-work)
  - [Per-Reconcile Sub-Operation Visibility via Tracing](#per-reconcile-sub-operation-visibility-via-tracing)
  - [Target-Kind Breakdown on <code>reconcile_errors_total</code>](#target-kind-breakdown-on-reconcile_errors_total)
- [Appendix](#appendix)
  - [Appendix A: Cross-Controller Conflict Storm Analysis](#appendix-a-cross-controller-conflict-storm-analysis)
  - [Appendix B: Terminology](#appendix-b-terminology)
<!-- /toc -->

## Summary

The Grove operator currently has **no custom Prometheus metrics**. While controller-runtime already provides built-in reconcile-level metrics (`controller_runtime_reconcile_total`, `controller_runtime_reconcile_time_seconds`, `controller_runtime_active_workers`) and workqueue metrics (`workqueue_depth`, `workqueue_queue_duration_seconds`, etc.), these only cover the overall reconcile outcome and duration. They provide no visibility into **how often 409 Conflicts occur on specific resource types**, or **what categories of errors are causing failures**.

This proposal adds two focused custom metrics to all Grove controllers (PodCliqueSet, PodClique, PodCliqueScalingGroup) that complement the existing built-in metrics, filling aggregate, fleet-level observability gaps that cannot be addressed by controller-runtime alone.

Per-request, sub-operation visibility ("which step inside *this* reconcile is slow?") is a distributed-tracing concern and is explicitly out of scope for this proposal; see [Alternatives](#alt-2-opentelemetry-tracing) and [Future Work](#per-reconcile-sub-operation-visibility-via-tracing).

## Motivation

During large-scale inference graph deployments using Grove (via NVIDIA Dynamo), we observed significant reconciliation performance degradation. Deploying an inference graph with 5 services × 10 replicas produces 50+ PodClique Pods, each transitioning through multiple states (Pending → Running → Ready). This generates a high rate of status changes that cascade through the Grove controller hierarchy (PodClique → PodCliqueScalingGroup → PodCliqueSet).

The built-in controller-runtime metrics tell us **that** reconciliation is slow or erroring, but not in sufficient detail to answer the following aggregate, fleet-level questions:

1. **No conflict attribution** — Status updates via `r.client.Status().Update()` and `r.client.Status().Patch()` can fail with 409 Conflict when an external controller (e.g., Dynamo's DGD controller) concurrently modifies the same PodClique/PodCliqueSet objects. The built-in `controller_runtime_reconcile_total{result="error"}` counts all errors equally — we cannot see the conflict rate or which resource type is affected.

2. **No error categorization** — Errors are returned with rich type information (`k8serrors.IsConflict`, `IsNotFound`, `IsAlreadyExists`, `IsTimeout`, etc.), but the built-in metrics collapse all errors into a single `result="error"` label. We cannot distinguish transient errors (conflict, timeout) from persistent errors (forbidden, invalid) at the fleet level.

Additionally, sub-operation timing within a single reconcile (i.e., "which step of *this* reconcile is slow?") is a **per-request** question best served by distributed tracing, not by aggregate histograms with function-name labels. This proposal deliberately scopes itself to the aggregate, fleet-level questions where Prometheus metrics are the right tool, and tracks sub-operation visibility as future work under tracing (see [Future Work](#per-reconcile-sub-operation-visibility-via-tracing)).

### Goals

* Add a custom metric to track 409 Conflict errors by target resource kind, complementing built-in error counters that do not distinguish conflict attribution.
* Add a custom metric to categorize reconciliation errors by Kubernetes error type, enabling triage of transient vs persistent failures at the fleet level.
* Document how Grove's built-in controller-runtime metrics answer standard reconciliation questions, so custom metrics remain scoped to what's genuinely missing.
* Stay within the aggregate, fleet-level scope that Prometheus metrics are designed for; defer per-request sub-operation visibility to distributed tracing.

### Non-Goals

* This proposal does NOT duplicate built-in controller-runtime metrics (`controller_runtime_reconcile_time_seconds`, `controller_runtime_reconcile_total`, `controller_runtime_active_workers`). These are already exposed by the metrics endpoint and should be used as-is for reconcile-level observability.
* This proposal does NOT add metrics to Grove's scheduler backends (these are external components Grove supports, not part of Grove itself); it covers only the Grove operator controllers.
* This proposal does NOT add per-request, sub-operation timing via Prometheus metrics. That question belongs to tracing and is tracked as Future Work.
* This proposal does NOT modify the Grove CRD API, reconciliation behavior, or any user-facing functionality beyond the `/metrics` endpoint.
* This proposal does NOT specify alerting rules or dashboard JSON (these are consumer concerns, not operator-side concerns).

## Proposal

### Overview

Introduce two custom Prometheus metrics exposed by all three Grove controllers (PodCliqueSet, PodClique, PodCliqueScalingGroup). Both are **aggregate, fleet-level** signals that complement the built-in controller-runtime metrics without duplicating them.

How controllers record these metrics (recording helpers, context propagation, registration) is an implementation detail of the follow-up PR. This proposal describes only the **metric surface** — names, labels, semantics — and the rationale for each.

### Relationship to Built-in Metrics

controller-runtime automatically registers a rich set of metrics via `sigs.k8s.io/controller-runtime/pkg/metrics`. The following table maps common reconciliation questions to the metric that answers them:

| Question | Metric | Source |
|----------|--------|--------|
| How long does reconciliation take? | `controller_runtime_reconcile_time_seconds` (Histogram) | built-in |
| How many reconciles have occurred? | `controller_runtime_reconcile_total` (Counter, labeled by `result`) | built-in |
| How many reconciles are in flight? | `controller_runtime_active_workers` (Gauge) | built-in |
| How deep is the work queue? | `workqueue_depth` (Gauge) | built-in |
| How long do items wait in queue? | `workqueue_queue_duration_seconds` (Histogram) | built-in |
| How long before a retry? | `workqueue_unfinished_work_seconds` (Gauge) | built-in |
| API client performance? | `rest_client_request_duration_seconds` (Histogram) | built-in |
| **Which resource type is the 409 contention hotspot?** | `grove_operator_status_update_conflict_total` | **new (this proposal)** |
| **What fraction of errors are transient vs persistent?** | `grove_operator_reconcile_errors_total` | **new (this proposal)** |

### User Stories

#### Story 1: Diagnosing Slow Deployment at Scale

**As** a Grove operator
**When** a user reports that deploying a large inference graph (50+ PodCliques) takes significantly longer than expected
**I want** to query built-in metrics (`controller_runtime_reconcile_time_seconds`, `workqueue_depth`, `workqueue_queue_duration_seconds`) to see whether the bottleneck is reconcile duration, queue backlog, or queue-wait latency
**So that** I can determine whether to tune `MaxConcurrentReconciles`, optimize the reconcile path, or investigate upstream load.

For drilling into *which step of a single reconcile* is slow, tracing (not this proposal) is the right tool.

#### Story 2: Identifying Cross-Controller Resource Contention

**As** a Grove developer
**When** investigating status update failures during high-load deployments
**I want** to see which Grove resource types (`PodCliqueSet` vs `PodClique` vs `PodCliqueScalingGroup`) are most affected by 409 Conflicts
**So that** I can prioritize architectural changes (e.g., migrating to `Status().Patch()` or Server-Side Apply) for the highest-contention resources.

This is an aggregate, fleet-level question: "across all instances and all time, which resource type sees the most contention?" It is exactly what Prometheus metrics do well.

#### Story 3: Distinguishing Transient vs Persistent Errors

**As** an SRE operating Grove in production
**When** the `controller_runtime_reconcile_total{result="error"}` rate is elevated
**I want** to see the breakdown by error type (conflict, timeout, not_found, invalid, etc.)
**So that** I can distinguish transient issues (retries will succeed) from persistent issues (require intervention) without reading logs.

### Limitations/Risks & Mitigations

1. **Risk:** High-cardinality labels can cause Prometheus memory pressure.
   **Mitigation:** All labels in this proposal have bounded, small cardinalities (controller name: 3 values; target_kind: 3 values; error_type: 8 values from `k8serrors.Is*` predicates). Total new time-series count is bounded (see [Cardinality Bound](#cardinality-bound)).

2. **Risk:** Metric naming collision with upstream or sibling operators.
   **Mitigation:** All custom metrics use the `grove_operator_` namespace prefix, following Prometheus naming conventions and matching Grove's existing label (`app.kubernetes.io/name=grove-operator`).

3. **Risk:** Overlap with controller-runtime built-ins.
   **Mitigation:** This proposal explicitly avoids duplicating `controller_runtime_reconcile_*` and `workqueue_*` metrics. The [Relationship to Built-in Metrics](#relationship-to-built-in-metrics) table defines the boundary.

4. **Risk:** Coupling metric labels to internal code structure.
   **Mitigation:** No label value in this proposal references a Go function name, file path, or package. All labels are domain-level identifiers that survive refactors (`target_kind` = Kubernetes Kind; `error_type` = semantic class from `k8serrors.Is*`).

## Design Details

### Metric Surface

Two custom metrics are exposed. Both use the `grove_operator` namespace prefix and are registered against `ctrlmetrics.Registry` so they appear at the same `/metrics` endpoint as the built-in controller-runtime metrics.

#### `grove_operator_status_update_conflict_total`

- **Type:** Counter
- **Labels:** `controller`, `target_kind`
- **Description:** Count of 409 Conflict errors returned by `Status().Update()` or `Status().Patch()` calls, attributed to the target resource kind.
- **`controller` values:** `podcliqueset-controller`, `podclique-controller`, `podcliquescalinggroup-controller`
- **`target_kind` values:** `PodCliqueSet`, `PodClique`, `PodCliqueScalingGroup`
- **Question answered:** "Which resource type is the cross-controller contention hotspot?"
- **Why built-in metrics cannot answer this:** `controller_runtime_reconcile_total{result="error"}` aggregates all errors at the reconcile level; it does not identify the target resource of the failing Status Update/Patch, nor does it distinguish conflicts from other error kinds.

#### `grove_operator_reconcile_errors_total`

- **Type:** Counter
- **Labels:** `controller`, `error_type`
- **Description:** Reconciliation errors categorized by well-known Kubernetes error classes.
- **`controller` values:** same as above.
- **`error_type` values:** one of `not_found`, `conflict`, `already_exists`, `timeout`, `forbidden`, `invalid`, `rate_limited`, `internal` (mapped 1:1 from `k8serrors.IsNotFound`, `IsConflict`, `IsAlreadyExists`, `IsTimeout`, `IsForbidden`, `IsInvalid`, `IsTooManyRequests`, default).
- **Question answered:** "What fraction of errors are transient (conflict, timeout, rate_limited) vs persistent (invalid, forbidden)?"
- **Why built-in metrics cannot answer this:** `controller_runtime_reconcile_total{result="error"}` provides no error classification.

### Cardinality Bound

With three controllers, eight error types, and three target kinds, the total new time-series count is bounded by:

- `status_update_conflict_total`: 3 controllers × 3 target kinds = **9 series**
- `reconcile_errors_total`: 3 controllers × 8 error types = **24 series**

**Total: ≤ 33 new series.** No high-cardinality labels are introduced (no namespace, no resource name, no function name, no pod UID). The bound is static and refactor-insensitive.

### Recording Verbs (Contract Only)

Recording is intended to be synchronous, O(1), and side-effect-free on the reconciler's control flow:

- `status_update_conflict_total` is incremented in the error path of `Status().Update()` / `Status().Patch()` immediately after a 409 is detected (via `k8serrors.IsConflict`). The increment does not alter the returned error.
- `reconcile_errors_total` is incremented in the reconcile-loop error path, after the error is classified by the `k8serrors.Is*` predicates. A single error increments exactly one series.

Exact helper types, function signatures, registration code, and call sites are defined in the implementation PR and are not part of the contract reviewed here.

### Built-in controller-runtime Metrics Reference

For convenience of reviewers and future readers, the built-in metrics that Grove already exposes (automatically, via the controller-runtime dependency) are summarized below. This proposal adds to this surface; it does not modify or replace any of the below.

| Metric | Type | Labels | What it answers |
|--------|------|--------|-----------------|
| `controller_runtime_reconcile_time_seconds` | Histogram | `controller` | Reconcile duration distribution |
| `controller_runtime_reconcile_total` | Counter | `controller`, `result` | Reconcile count by outcome (success/error/requeue) |
| `controller_runtime_active_workers` | Gauge | `controller` | In-flight reconciles |
| `controller_runtime_max_concurrent_reconciles` | Gauge | `controller` | Configured concurrency limit |
| `workqueue_adds_total` | Counter | `name` | Total items added to queue |
| `workqueue_depth` | Gauge | `name` | Current queue depth |
| `workqueue_queue_duration_seconds` | Histogram | `name` | Queue wait time |
| `workqueue_retries_total` | Counter | `name` | Retry count |
| `rest_client_request_duration_seconds` | Histogram | `verb`, `host`, `code` | API client request latency |
| `rest_client_requests_total` | Counter | `verb`, `host`, `code` | API client request count |

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

# ===== New Custom Metrics (added by this proposal) =====

# 409 Conflict rate — which resource is the contention hotspot?
sum by (controller, target_kind) (
  rate(grove_operator_status_update_conflict_total[5m])
)

# Error breakdown by type — transient vs persistent
sum by (controller, error_type) (
  rate(grove_operator_reconcile_errors_total[5m])
)

# Fraction of errors that are conflicts
sum(rate(grove_operator_reconcile_errors_total{error_type="conflict"}[5m]))
/
sum(rate(grove_operator_reconcile_errors_total[5m]))
```

### Monitoring

The custom metrics introduced by this proposal, combined with the built-in controller-runtime metrics, provide:

1. **Conflict Detection and Attribution:** 409 Conflict rate by target resource kind, identifying cross-controller contention hotspots.
2. **Error Triage:** Errors categorized by Kubernetes error type, distinguishing transient issues from persistent failures.

The built-in metrics already provide reconciliation health (duration, throughput, success rate), capacity saturation (active workers, queue depth), and API client performance.

Recommended alert examples (not enforced by this proposal):
- `grove_operator_status_update_conflict_total` rate > 1/s per `(controller, target_kind)` → cross-controller contention
- `grove_operator_reconcile_errors_total{error_type="timeout"}` rate > 0.1/s → API server pressure
- `workqueue_depth{name="podclique-controller"}` > 50 for 5m → PodClique controller falling behind

### Dependencies

- `github.com/prometheus/client_golang` — already an indirect dependency via controller-runtime.
- `sigs.k8s.io/controller-runtime/pkg/metrics` — already used for the metrics server endpoint.

No new external dependencies are introduced.

### Test Plan

1. **Unit tests (implementation PR):** Error-type classification covers all targeted `k8serrors.Is*` predicates. Conflict-path recording is triggered iff a 409 Conflict is observed on a Status Update/Patch call.
2. **Integration test:** With a Grove operator running against a real API server, verify both custom metrics appear at `/metrics` alongside the existing built-in metrics, with the expected label sets and only bounded cardinality.
3. **E2E test:** A concurrent-write workload (Grove reconciler + a synthetic upstream writer modifying the same resources) produces a non-zero `grove_operator_status_update_conflict_total` counter for the contended resource kind.
4. **Coexistence check:** Verify these metrics do not duplicate or interfere with the scale-test milestone instrumentation in `operator/e2e/measurement/`. The new metrics expose aggregate per-controller signals via the `/metrics` endpoint; the measurement package continues to own scale-test milestone logging. Different pipelines, no expected interference.

### Graduation Criteria

**Alpha (this proposal):**
- Two custom metrics registered and recorded in all three controllers.
- Conflict tracking on Status Update/Patch calls.
- Error categorization on reconcile-loop errors.
- Documentation covering both built-in and custom metrics.
- Unit and integration tests for the metric surface.

**Beta (future):**
- Grafana dashboard JSON included in the chart (combining built-in and custom metrics).
- ServiceMonitor template in Helm chart.
- Additional controller-specific metrics based on alpha data analysis.

**GA (future):**
- Metrics stable for 2+ releases.
- Community feedback incorporated.
- Alert rule examples based on production usage patterns.

## Implementation History

- 2026-03-11: Initial GREP draft created.
- 2026-03-24: PR #499 opened.
- 2026-03-30: Updated based on review feedback — removed 4 duplicate metrics, removed namespace label, refocused on 3 genuinely new metrics.
- 2026-05-09: Revised per review — scoped to aggregate, fleet-level metrics only (dropped sub-operation histogram in favor of future tracing work); removed embedded Go source per design-doc-scope feedback; fixed scheduler-backend terminology; PC → PCLQ across the document.

## Alternatives

### Alt 1: Structured Logging Only

**Pros:** No code changes to metrics infrastructure; flexible querying.

**Cons:** Cannot create real-time dashboards or alerts; higher storage cost; slower to query than Prometheus; no time-series aggregation.

**Rejected because:** Prometheus metrics are the established Kubernetes ecosystem standard for aggregate, fleet-level observability. Log analysis is complementary but not a substitute for time-series monitoring.

### Alt 2: OpenTelemetry Tracing

**Pros:** Rich per-request trace context with spans; distributed tracing across operator → API server; the natural tool for "which step inside *this* reconcile is slow?"

**Cons:** Requires additional infrastructure (collector + backend such as Jaeger or Tempo); larger adoption effort than adding two metrics; tracing is not yet standard in controller-runtime.

**Scoped out (not rejected):** Tracing is the correct tool for per-request, sub-operation visibility. An aggregate histogram labeled by function name (e.g., `ensure_finalizer`, `sync_pods`) cannot answer a per-request question and risks breaking silently on refactor.

**This proposal deliberately scopes itself to aggregate, fleet-level signals.** Per-request sub-operation visibility via OpenTelemetry is complementary and is tracked as [Future Work](#per-reconcile-sub-operation-visibility-via-tracing). It is not in competition with this proposal.

### Alt 3: ObservedReconciler Wrapper for All Metrics

**Pros:** A single wrapper could provide reconcile-level metrics automatically without per-controller changes.

**Cons:** Duplicates controller-runtime built-ins (`controller_runtime_reconcile_time_seconds`, `controller_runtime_reconcile_total`, `controller_runtime_active_workers`). Creates two sources of truth for the same data.

**Rejected because:** controller-runtime already provides reconcile-level metrics. Wrapping the reconciler would duplicate them. This proposal focuses on what's genuinely missing at the fleet level (conflict attribution, error categorization) without duplication.

## Future Work

### Per-Reconcile Sub-Operation Visibility via Tracing

The question "which step inside *this* reconcile is slow?" is a per-request, single-trace question. It belongs to distributed tracing, not Prometheus metrics.

A follow-up GREP will propose adding OpenTelemetry span instrumentation to Grove controllers so that operators can inspect the span tree of an individual reconcile, with stable span names that do not break on internal refactors.

This proposal deliberately does not fill that gap with a sub-operation histogram labeled by function name, because such labels couple the metric surface to internal code structure.

### Target-Kind Breakdown on `reconcile_errors_total`

If `grove_operator_status_update_conflict_total` does not fully capture contention patterns in production, a `target_kind` label can be added to `reconcile_errors_total` in a follow-up. Deferred until alpha-stage data is available.

## Appendix

### Appendix A: Cross-Controller Conflict Storm Analysis

When Grove is used by an upstream operator (e.g., NVIDIA Dynamo), the following contention pattern occurs during large-scale deployments:

**Object ownership:**
- **Upstream Operator (e.g., Dynamo DGD Controller)** creates PodCliqueSet (PCS); scales PodClique (PCLQ) / PodCliqueScalingGroup (PCSG) via the Scale subresource; reads PCLQ/PCSG status for readiness checks.
- **Grove Operator** — PCS Controller creates PCLQ/PCSG and updates PCS status; PCLQ Controller creates Pods and updates PCLQ status (replicas, readyReplicas); PCSG Controller manages PCLQ replicas and updates PCSG status.

**Contention mechanism:**

1. Upstream operator scales PodClique via Scale subresource (Get scale → Update scale).
2. Between the Get and Update, Grove's PCLQ controller updates PCLQ status (e.g., readyReplicas changes as a Pod becomes Ready).
3. The upstream operator's Scale Update receives 409 Conflict (resourceVersion mismatch).
4. Similarly, Grove's PCS `reconcileStatus` calls `r.client.Status().Update(ctx, pcs)`. If the upstream operator concurrently modifies PCS, Grove's status update can also conflict.

**Why Grove needs these metrics:**

Without fleet-level metrics on the Grove side, we can only see the upstream operator's view of the conflict. The `grove_operator_status_update_conflict_total` metric provides the other half: how often Grove's own status updates fail due to concurrent external writes, and on which resource kind. Combined with the upstream operator's metrics, this gives a complete picture of the contention and guides architectural decisions (e.g., switching from `Status().Update()` to field-level `Status().Patch()`, or adopting Server-Side Apply).

**Future work enabled by these metrics:**

1. **Status Patch optimization** — If `status_update_conflict_total` is high for PCS, consider switching `reconcileStatus` from `Status().Update()` to field-level `Status().Patch()` to reduce the conflict surface.
2. **Rate limiter tuning** — Use built-in `controller_runtime_active_workers` and `workqueue_depth` data to tune `MaxConcurrentReconciles` per controller.
3. **Watch predicate optimization** — Sub-operation latency data (from tracing, in Future Work) can guide whether watch predicates should be tightened to reduce unnecessary reconcile triggers.
4. **Upstream coordination** — Share conflict data with upstream operator teams to jointly optimize the interaction pattern.

### Appendix B: Terminology

| Term | Definition |
|------|------------|
| **PCS** | PodCliqueSet — top-level Grove CRD representing a set of PodCliques |
| **PCLQ** | PodClique — Grove CRD representing a group of co-scheduled Pods |
| **PCSG** | PodCliqueScalingGroup — Grove CRD for managing multiple PodClique replicas |
| **Conflict storm** | A pattern where concurrent writes to the same Kubernetes object produce cascading 409 Conflict errors |
| **Optimistic concurrency** | Kubernetes mechanism where every object has a `resourceVersion`; an Update must match the current version or receive 409 Conflict |

> NOTE: This GREP template has been inspired by [KEP Template](https://github.com/kubernetes/enhancements/blob/f90055d254c356b2c038a1bdf4610bf4acd8d7be/keps/NNNN-kep-template/README.md).

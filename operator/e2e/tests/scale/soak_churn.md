# Soak / Churn Benchmark

## GitHub Issue

Use the **Enhancement** template (`.github/ISSUE_TEMPLATE/ENHANCEMENT.yaml`).
The fields below are ready to paste into the form.

**Title**

> Add long-running soak / churn benchmark to e2e scale tests

**Labels**

`enhancement`, `area/testing`, `area/operator`

---

### What you would like to be added?

Add a long-running benchmark under `operator/e2e/tests/scale/` that
boots a small `PodCliqueSet` (~100 pods) and then drives repeated
scale-up / scale-down cycles against it over an extended duration.
The goal is to surface bugs that only appear after many incremental
reconciles — leaks, monotonically growing slices, gradually drifting
counters, finalizer pile-ups — that single-shot benchmarks cannot see.

**Scope (single test, parameterized):**

| Parameter | Default | Env override | Rationale |
|---|---|---|---|
| Base PCS replicas | 25 (50 pods) | `SOAK_BASE` | Small enough to fit dev clusters; large enough to exercise multi-child reconciles. |
| Peak PCS replicas | 50 (100 pods) | `SOAK_PEAK` | 2x amplitude per cycle. |
| Cycles | 10 | `SOAK_CYCLES` | Enough per-cycle iterations to surface slow drift; bounded total runtime. |
| Per-cycle hold | ~30 s | — | Lets watch events flush and pprof capture overlap consecutive no-op reconciles. |
| Worker nodes | 30 kwok nodes | — | Same fixture as scale-up/down tests. |
| Timeout | 45 min | — | Soft cap; expected runtime ~25–30 min. |

**Deliverables:**

- `operator/e2e/tests/scale/soak_test.go`.
- `operator/e2e/yaml/soak-churn.yaml` (PCS sized at base; ~50 pods).
- Reuse `runScaleTest`, `ScalePCS`, `PodsCreatedCondition`,
  `TimerCondition`. The same scale-down milestone gap noted in the
  scale-up-down issue applies here (`PodsAtCountCondition` is
  prerequisite).
- Final-check phase asserting no leaks/orphans/unbounded counters at
  the end of the run.

**Cycle shape (executed N times):**

1. **scale-up** — patch `spec.replicas` base → peak. Wait until live
   pod count reaches `peak * podsPerReplica`.
2. **hold-peak** — 30 s window.
3. **scale-down** — patch `spec.replicas` peak → base. Wait until live
   pod count reaches `base * podsPerReplica`.
4. **hold-base** — 30 s window.

**Final-check assertions:**

- Live pod count equals base target (no leaked pods).
- No `PodCliqueScalingGroup` or `PodClique` exists outside the expected
  child set.
- `status.updateProgress` (if present) has bounded counters:
  `UpdatedPodCliquesCount ≤ TotalPodCliquesCount`.
- `status.lastErrors` is empty.

---

### Why is this needed?

Today's scale benchmarks run for minutes and measure a single event
(deploy, resize, delete). They are good at catching gross regressions
in per-event throughput, but they pass cleanly on a controller that is
slowly leaking heap, accumulating stale entries in an informer index,
or letting one status field drift by an item per reconcile.

Issues we have personally been bitten by, and that a soak/churn test
**would catch**:

- **#567 — unbounded slices in status.** The original
  `UpdatedPodCliques` / `UpdatedPodCliqueScalingGroups` slices grew
  with every reconcile. A single-shot benchmark looked fine; a soak
  test would have flagged it within an hour.
- **Finalizer / cascade-delete drift.** Repeated scale-down cycles
  exercise the partial-delete path many times. Any reconcile that
  forgets to clear an expectation, or that re-emits a finalizer, will
  accumulate across cycles.
- **Watch-event amplification.** Status mutators that fire a Patch on
  byte-identical status (no real change) waste a write per reconcile
  and trigger a cascade of watch events. The equality-short-circuit
  guards added in #567 prevent this, but only a churn test exercises
  the steady-state-Patch-suppression path enough to verify it.
- **Heap growth without a leak.** Even without an obvious bug, churn
  reveals whether the controller's working-set memory plateaus or
  keeps climbing. A pprof window over the second half of the run is
  the cheapest way to spot a slow trend.

---

## Design Detail

The sections below are for the implementation PR — they are not part of
the GitHub issue body.

### Goals

- Drive ~10 scale-up / scale-down cycles against a single PCS over a
  duration long enough to expose slow leaks (target: 30–60 min).
- Run on the smaller dev fixture (≤ 30 worker nodes, ~100 pods peak).
  The point is reconcile churn, not absolute capacity.
- Reuse the existing `runScaleTest` scaffolding and pprof hook so the
  output format and capture mechanism match the rest of the suite.
- Assert at the end that the PCS reaches its final steady state
  cleanly — no orphaned children, counters bounded, no unexpected
  `lastErrors`.

### Non-Goals

- Multi-PCS / multi-tenant soak. Single-PCS churn is enough to surface
  controller-internal leaks; multi-tenant belongs in a separate test.
- Failure-injection (operator restarts, API server flakes). The soak
  test is for steady-state churn; chaos is out of scope.
- Comparison against historical runs. The output is captured in the
  same JSON format as other scale tests, but no regression gate is
  defined here.

### Test Shape

Three top-level phases:

1. **deploy** — apply the YAML at base; wait for `pods-ready`.
2. **churn-cycles** — loop N times executing the cycle shape above.
   Each cycle adds milestones to the timeline so the per-cycle cost is
   visible in the output.
3. **soak-final-check** — assertions listed in the issue body; if any
   fails the test fails.

There is no explicit `delete` phase — the test fixture cleanup tears
the PCS down at the end. (If we want a delete-time measurement, add
it; otherwise keep the scope tight.)

### Implementation Notes

- New file: `operator/e2e/tests/scale/soak_test.go`.
- New YAML fixture: `operator/e2e/yaml/soak-churn.yaml`. PCS sized at
  the base (25 replicas, 50 pods) so the deploy phase is short and the
  churn phases dominate the timeline.
- Per-cycle phases can be added programmatically in a loop inside the
  test's `addPhases` callback — each iteration calls
  `tracker.AddPhase` four times with unique names suffixed by the
  cycle index (e.g. `scale-up-c3`).
- Reuses existing `ScalePCS`, `PodsCreatedCondition`, and
  `TimerCondition`. **Same caveat as the scale-down doc:** the
  scale-down legs need a "live pod count drops to N" condition that
  `PodsCreatedCondition` (which is monotonic-up) does not provide.
- The pprof hook in `setupPprofHook` is already per-run; one capture
  spans the whole soak. If we want per-cycle pprof captures, that's a
  follow-up.
- Make cycle count and amplitude env-tunable (`SOAK_CYCLES`,
  `SOAK_BASE`, `SOAK_PEAK`) so the same test can run at small scale in
  CI and at larger scale on dev clusters without a new test function.

### Open Questions

- Should the soak test be tagged with a build tag stricter than `e2e`
  (e.g. `e2e_soak`) so it isn't pulled into the default suite by
  accident? The 30-min default runtime is too long for routine CI.
- Where should the final-check assertions live? Adding them as
  measurement conditions keeps the timeline coherent; embedding them
  as `t.Fatalf` checks after `tracker.Wait()` is simpler but breaks
  the "everything is a phase" pattern.
- Do we want a heap-comparison assertion (e.g. compare pprof heap
  samples between cycle 1 and cycle N, fail if delta exceeds a
  threshold)? Useful but stateful — defer until the first real
  regression motivates it.

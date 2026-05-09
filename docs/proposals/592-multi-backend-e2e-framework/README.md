# GREP-592: Multi-Backend E2E Test Framework

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Relationship to GREP-375](#relationship-to-grep-375)
  - [User Stories](#user-stories)
    - [Story 1: A reviewer needs fast PR feedback on a backend-specific change](#story-1-a-reviewer-needs-fast-pr-feedback-on-a-backend-specific-change)
    - [Story 2: A maintainer wants to add a third-party scheduler backend](#story-2-a-maintainer-wants-to-add-a-third-party-scheduler-backend)
    - [Story 3: A contributor proposes a new capability](#story-3-a-contributor-proposes-a-new-capability)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
    - [Risk: Backend matrix explosion in nightly](#risk-backend-matrix-explosion-in-nightly)
    - [Risk: Test-matrix.yaml drift](#risk-test-matrixyaml-drift)
    - [Risk: Primary-only agnostic suites mask cross-backend bugs](#risk-primary-only-agnostic-suites-mask-cross-backend-bugs)
    - [Risk: PR CI selectivity reduces coverage on shared paths](#risk-pr-ci-selectivity-reduces-coverage-on-shared-paths)
    - [Risk: Migration disrupts current KAI coverage](#risk-migration-disrupts-current-kai-coverage)
    - [Limitation: Selector cannot detect runtime feature flags](#limitation-selector-cannot-detect-runtime-feature-flags)
- [Design Details](#design-details)
  - [Three-Tier Classification](#three-tier-classification)
  - [Backend Capability Declaration](#backend-capability-declaration)
  - [Test Suite Registry: <code>operator/e2e/test-matrix.yaml</code>](#test-suite-registry-operatore2etest-matrixyaml)
  - [Initial Classification Table](#initial-classification-table)
  - [Selector: <code>hack/e2e-select</code>](#selector-hacke2e-select)
  - [PR CI Workflow Changes](#pr-ci-workflow-changes)
  - [Nightly CI Workflow](#nightly-ci-workflow)
  - [Migration Plan](#migration-plan)
  - [Monitoring](#monitoring)
  - [Dependencies](#dependencies)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
- [Open Questions](#open-questions)
- [Future Work](#future-work)
- [Implementation History](#implementation-history)
- [Alternatives](#alternatives)
  - [Alternative 1: Run every suite on every backend on every PR](#alternative-1-run-every-suite-on-every-backend-on-every-pr)
  - [Alternative 2: Move all e2e under a single suite-set (&quot;e2e-core&quot;) and rely on Ginkgo labels](#alternative-2-move-all-e2e-under-a-single-suite-set-e2e-core-and-rely-on-ginkgo-labels)
  - [Alternative 3: Backend-specific duplicated test suites](#alternative-3-backend-specific-duplicated-test-suites)
  - [Alternative 4: Only run multi-backend on nightly; keep PR CI single-backend](#alternative-4-only-run-multi-backend-on-nightly-keep-pr-ci-single-backend)
  - [Alternative 5: Land the capability resolver in alpha, not as future work](#alternative-5-land-the-capability-resolver-in-alpha-not-as-future-work)
  - [Alternative 6: A separate <code>backends.yaml</code> parallel to <code>test-matrix.yaml</code>](#alternative-6-a-separate-backendsyaml-parallel-to-test-matrixyaml)
- [Appendix](#appendix)
  - [Reference: Existing E2E Matrix in <code>build-check-test.yaml</code>](#reference-existing-e2e-matrix-in-build-check-testyaml)
  - [Reference: GREP-375 capability extension pattern](#reference-grep-375-capability-extension-pattern)
  - [Glossary](#glossary)
<!-- /toc -->

## Summary

[GREP-375](../375-scheduler-backend-framework/README.md) introduced a pluggable `Backend` interface and shipped two scheduler backend implementations (`kai-scheduler`, `default-scheduler`). Grove's existing E2E suite, however, still assumes a single backend (KAI) deployed by `make run-e2e-full`, and the GitHub Actions matrix runs 9 suites against that one backend. As more backends are added — whether default-scheduler graduating to gang scheduling via the Kubernetes Workload API, or third-party schedulers as envisioned by GREP-375 Story 1 — this 1×N model breaks down.

This proposal defines a backend-aware E2E test selection framework. Each suite is classified into one of three tiers (**agnostic**, **sensitive**, **capability**), declared in a small `operator/e2e/test-matrix.yaml`, and consumed by a `hack/e2e-select` Go binary that emits the GitHub Actions matrix. PR CI continues to be path-filtered and lean; a new nightly workflow runs the exhaustive (suite × capable backend) matrix. Existing test code does not change. The Backend interface from GREP-375 does not change.

## Motivation

Today's `build-check-test.yaml` matrix has the shape `9 suites × 1 backend (KAI)`. The status quo has three concrete problems:

1. **Cost when backends multiply.** Once a second backend is enabled in the e2e harness, naively expanding to `9 × 2` doubles runner cost (each job is ~10–60min on a self-hosted runner). At `9 × 3` the matrix becomes the dominant CI cost. Most of those job-slots add no signal: `cert_management` and `crd_installer` exercise webhooks and CRD installation paths that are entirely independent of scheduler choice.
2. **Coverage gap when backends differ.** Conversely, if the harness keeps running the matrix against a single backend (KAI), backend-specific regressions in `default-scheduler` go undetected on every PR. Sensitive suites — `rolling_updates`, `ondelete_updates`, `startup_ordering` — exercise behaviors that *should* be backend-agnostic in principle but in practice depend on the backend's `PreparePod` / `SyncPodGang` lifecycle hooks; differences between backends are exactly what we need to catch.
3. **Capability mismatches.** Some suites require a capability that not every backend supports. `topology_aware_scheduling` exercises the `TopologyAwareSchedBackend` interface that only KAI implements today; running it against `default-scheduler` would either hard-fail or require inserting per-backend skip logic into test code. The test-suite layer is the wrong place to encode "skip if not implemented" — that information already lives in the backend's interface set.

Grove already has the building blocks for the right answer:

- `operator/internal/scheduler/types.go` defines a `Backend` interface plus a `TopologyAwareSchedBackend` extension interface — the existing pattern is **capability == "implements interface X"**.
- `OperatorConfiguration.Scheduler.Profiles` lets the operator be deployed with one or more backends.
- `.github/workflows/build-check-test.yaml` already uses `dorny/paths-filter` to skip E2E entirely on docs-only PRs.

What is missing is the binding layer — declarative metadata that says "suite Y needs capability Z" and a selector that combines that with the backend registry to produce a matrix.

This GREP fills that gap.

### Goals

- Define a three-tier classification (`agnostic` / `sensitive` / `capability`) for E2E suites, expressed declaratively in `operator/e2e/test-matrix.yaml`.
- Implement a `hack/e2e-select` Go binary that consumes `test-matrix.yaml` + the changed-file set + the CI mode (`pr` | `nightly`) and emits a GHA matrix JSON.
- Refine the existing PR CI path filter so backend-specific changes select only the affected backend's relevant suites, while shared scheduler-framework changes select all sensitive suites.
- Add a new nightly workflow that runs the exhaustive (suite × capable backend) matrix.
- Treat the **set of capability interfaces a backend implements** (e.g. `TopologyAwareSchedBackend`) as the long-term source of truth for backend capabilities. Alpha uses a temporary YAML mirror inside `test-matrix.yaml` (validated against a hand-maintained allowlist) to keep the selector contract small; that mirror is removed by the future-work resolver — see [Backend Capability Declaration](#backend-capability-declaration).
- Preserve all existing E2E behavior during migration: KAI continues to be the primary backend, and the current 9 suites continue to run unchanged on KAI.
- Make adding a new backend require only (a) registering it via GREP-375's `SchedulerProfile`, (b) implementing the relevant capability interfaces, and (c) declaring the deploy profile in the e2e harness — no edits to per-suite test files.

### Non-Goals

- This GREP does not modify GREP-375's `Backend` interface or any of its implementations. Capability declaration may be enriched in a follow-up (see [Future Work](#future-work) and [Open Question 1](#open-questions) below) but that is a separate, optional change tracked by its own issue.
- This GREP does not rewrite or restructure any existing E2E test code under `operator/e2e/tests/`. The test-selection mechanism is purely additive.
- This GREP does not migrate the test runner away from `testing.T + -run pattern`. Ginkgo migration is a separate discussion.
- This GREP does not define performance benchmark suites or `make run-scale-test` policy.
- This GREP does not modify Dynamo. Scope is `ai-dynamo/grove` only.
- This GREP does not require every backend to support every capability. The framework's whole point is to make capability gaps explicit and machine-checkable rather than a runtime surprise.

## Proposal

### Relationship to GREP-375

[GREP-375](../375-scheduler-backend-framework/README.md) defines *how* multiple backends coexist in Grove (interface, registry, OperatorConfiguration, lifecycle hooks). It explicitly defers the E2E test-strategy question to a follow-up:

> "Today, E2E tests assume a single scheduler backend (e.g. KAI) and there is no way to configure which backend the test environment uses. […] The test plan should also cover running against other supported backends where feasible." — GREP-375, *Test Plan / E2E Tests*

This GREP is that follow-up. It consumes GREP-375's primitives and adds the test-selection layer on top:

| GREP-375 owns | This GREP owns |
|---|---|
| `Backend` interface and capability extension interfaces (e.g. `TopologyAwareSchedBackend`) | `test-matrix.yaml` mapping suites → required capabilities |
| Backend registration via `SchedulerProfile` and `manager.Initialize()` | `hack/e2e-select` selector logic |
| OperatorConfiguration deployment / runtime selection | E2E harness profiles (which backends to deploy for which test mode) |
| Capability source of truth (interface implementations) | CI workflow shape (PR matrix, nightly matrix) |

Crucially, capability declaration is conceptually GREP-375's concern: a backend "declares" a capability by implementing the corresponding interface in `operator/internal/scheduler/`. `test-matrix.yaml` says "suite X requires capability Y"; the selector resolves "which backends provide Y" using the `backends:` section as an alpha stop-gap (see [Backend Capability Declaration](#backend-capability-declaration)) and, after future-work, by querying the backend registry directly. The end state is a single source of truth in the scheduler package.

### User Stories

#### Story 1: A reviewer needs fast PR feedback on a backend-specific change

A contributor changes only files under `operator/internal/scheduler/kai/`. Today, the PR CI runs all 9 suites on KAI even though `cert_management` and `crd_installer` cannot possibly be affected, and `default-scheduler`-specific behavior is irrelevant. After this GREP: the selector consumes the changed paths, restricts the matrix to KAI's relevant suites (sensitive + KAI's capability suites), and skips the agnostic suites because they passed on the previous main commit and were not touched.

#### Story 2: A maintainer wants to add a third-party scheduler backend

Per GREP-375 Story 1, a third party adds backend `bar-scheduler` by implementing the `Backend` interface and (optionally) `TopologyAwareSchedBackend`. They register it via `SchedulerProfile` and add a deploy profile in the e2e harness. With this GREP, no test files are edited: the selector picks up the new backend automatically, runs all sensitive suites against it on every nightly run, and runs `topology_aware_scheduling` against it because it declares that capability. If `bar-scheduler` does not implement TAS, the selector simply skips that combination — no test-level skip logic needed.

#### Story 3: A contributor proposes a new capability

A contributor lands a capability tied to a new interface, say `PreemptionAwareSchedBackend`. They:

1. Define the interface and any new capability suites under `operator/e2e/tests/`.
2. Add an entry to `test-matrix.yaml` declaring the new suites' classification (`capability`) and required capability name.
3. Mark the relevant backends as implementing the interface.

The selector and CI workflows pick it up automatically. No selector code changes needed.

### Limitations/Risks & Mitigations

#### Risk: Backend matrix explosion in nightly

**Limitation.** Nightly runs `(sensitive suites × all backends) + (capability suites × capable backends) + (agnostic suites × primary)`. With 4 sensitive + 4 capability + 2 agnostic suites and 3 backends with overlapping capabilities, this is roughly `4×3 + 4×~2 + 2×1 = 22` jobs (vs. today's 9). Self-hosted runner concurrency or per-job cost may make the full nightly slow.

**Mitigation.** (a) Nightly does not need to gate PR merges, so wall-clock time is less critical. (b) Capability suites only run on capable backends; suites with narrow capability requirements (e.g. `auto_mnnvl`) often fan out to fewer backends, not more. (c) If the matrix grows large enough that wall-clock time becomes a problem, the selector can be extended with a sharding flag in a follow-up — see [Future Work](#future-work).

#### Risk: Test-matrix.yaml drift

**Limitation.** A new suite or capability could be added without updating `test-matrix.yaml`, silently dropping coverage.

**Mitigation.** (a) `make verify-test-matrix` (added by the implementation PR) statically checks every test file under `operator/e2e/tests/` is classified by `test-matrix.yaml`, failing CI on drift. (b) The same check verifies every capability name in a suite's `requires:` is declared by at least one backend in the `backends:` section, and that the `backends:` section's declared capabilities match the hand-maintained allowlist mirroring the actual interface implementations. (c) `make verify-test-matrix` runs as part of the existing `check` job in `build-check-test.yaml`, so drift is caught on every PR — not deferred to nightly.

#### Risk: Primary-only agnostic suites mask cross-backend bugs

**Limitation.** If a suite is misclassified as `agnostic` but actually depends on the backend (e.g. exercises a webhook that secretly calls into the scheduler), the bug only shows on the primary backend.

**Mitigation.** (a) The classification table in this GREP is conservative: when in doubt, mark `sensitive`. (b) Agnostic suites are reviewed against `git grep -l 'schedulerName\|PodGang\|KAI'` in their package — if any reference exists, they cannot be `agnostic`. (c) Quarterly review of the classification table by the scheduler-backend code owners.

#### Risk: PR CI selectivity reduces coverage on shared paths

**Limitation.** If path filters are too aggressive ("only run KAI suites when `kai/**` changed"), a shared change in `operator/internal/scheduler/` (refactor of the dispatcher) could pass PR CI without running default-scheduler suites.

**Mitigation.** Path rules are layered: changes under `operator/internal/scheduler/<backend>/**` select only that backend's suites; changes under `operator/internal/scheduler/**` (anything not in a backend subdir) select all sensitive + all capable-backend capability suites. The rules are written so the broader rule is the default and backend-specific narrowing is explicit.

#### Risk: Migration disrupts current KAI coverage

**Limitation.** Cutting over `build-check-test.yaml` to use the selector could accidentally drop coverage of an existing suite.

**Mitigation.** Migration is staged (see [Graduation Criteria](#graduation-criteria)). Alpha runs the selector in dry-run mode side-by-side with the existing matrix, comparing outputs; only after a soak period does the selector become authoritative.

#### Limitation: Selector cannot detect runtime feature flags

**Limitation.** The selector reasons about static capabilities (interface implementation). It cannot know that, e.g., gang scheduling is *configured off* for default-scheduler in a particular profile via `KubeSchedulerConfig.GangScheduling: false`.

**Mitigation.** This is a deliberate scope choice. Runtime configuration is the responsibility of the e2e harness's deploy profile, which declares "for this matrix entry, deploy the operator with these `SchedulerProfile`s." If the harness deploys default-scheduler with gang disabled, the gang_scheduling suite will fail at runtime and that's the expected behavior. The framework intentionally does not double-encode runtime configuration as static capability metadata.

## Design Details

### Three-Tier Classification

Every E2E suite under `operator/e2e/tests/` is classified into exactly one tier:

**Agnostic.** The suite exercises functionality with no scheduler-specific behavior on any code path it touches. It runs **on the primary backend only** because running it against multiple backends produces no new signal.

> Examples: `cert_management` (validates webhook TLS round-trip), `crd_installer` (validates CRD presence and init-container behavior). Note that even `cert_management` indirectly creates a workload to exercise webhooks, so it does require *some* scheduler to be deployed; what makes it agnostic is that any scheduler that admits the workload satisfies the test — the test does not assert scheduler-specific behavior.

**Sensitive.** The suite's correctness can vary across backend implementations even when no explicit capability is required. These typically exercise the `Backend.PreparePod` / `Backend.SyncPodGang` lifecycle hooks. They run **on every enabled backend**.

> Examples: `rolling_updates`, `ondelete_updates`, `startup_ordering`. Each backend can implement pod-creation order, recreate-vs-update semantics, and gate-removal differently; the suites must pass on every backend.

**Capability.** The suite requires the backend to implement a specific capability. It runs **only on backends that declare the capability**, where "declares" means "implements the corresponding capability interface in `operator/internal/scheduler/`."

> Examples: `gang_scheduling` (requires gang capability), `topology_aware_scheduling` (requires `TopologyAwareSchedBackend`), `resource_sharing` (requires hierarchical resource sharing — KAI-specific today), `auto_mnnvl` (requires the MNNVL capability).

The classification of every existing suite is given in the [Initial Classification Table](#initial-classification-table) below.

### Backend Capability Declaration

Conceptually, a capability is a property of a backend implementation. The conceptual source of truth is the set of capability extension interfaces a backend's type implements in `operator/internal/scheduler/` — GREP-375 already shipped this pattern (e.g. `TopologyAwareSchedBackend`):

```go
// operator/internal/scheduler/types.go (already exists, GREP-375)
type Backend interface { /* base */ }

type TopologyAwareSchedBackend interface {
    // capability-specific methods
}
```

A backend implements `TopologyAwareSchedBackend` to "declare" it has the topology-aware-scheduling capability; today KAI does, default-scheduler does not.

The selector needs to resolve the question: *given capability `X`, which backends provide it?* There are two ways to answer this, with different trade-offs:

| Approach | Pros | Cons |
|---|---|---|
| **(a) Code-side resolver** — a small Go shim (e.g. `operator/internal/scheduler/capabilities.go`) maps capability names to interface checks against the backend registry | Single SoT; impossible to drift from the actual interface implementations | Requires a new file in the scheduler package; couples e2e selector to scheduler internals; a small public API surface to maintain |
| **(b) YAML stop-gap** — `test-matrix.yaml` carries a `backends:` section listing each backend's capabilities, edited alongside backend additions | Minimal new code; selector is fully self-contained | Two sources of truth (YAML and interface set) — drift is possible until validation closes the loop |

**Alpha takes approach (b)** as a stop-gap to keep the selector contract small and let the framework land before any scheduler-package changes are negotiated. The `backends:` section in `test-matrix.yaml` (see schema below) is **explicitly marked as a temporary mirror of the interface set**, with a `make verify-test-matrix` rule that fails CI if a backend's declared capabilities diverge from a hand-maintained allowlist (see [Test Plan](#test-plan)). This is acknowledged technical debt — the scheduler-side interface implementations remain conceptually authoritative; the YAML is the practical input the selector reads.

**Beta and beyond** migrate to approach (a) as a follow-up: introducing `operator/internal/scheduler/capabilities.go` (or, depending on [Open Question 1](#open-questions), an explicit `Backend.Capabilities() []Capability` method), and removing the `backends:` section from `test-matrix.yaml`. That migration is tracked separately and requires its own review because it touches the scheduler package — the precise shape of the resolver is intentionally **not pinned by this GREP**, so reviewers of the future PR have full latitude.

This split is deliberate: alpha lands the selector contract and the classification model (where most of the design discussion belongs); the resolver implementation that lifts the YAML stop-gap is mechanical and best handled in its own PR.

### Test Suite Registry: `operator/e2e/test-matrix.yaml`

```yaml
# operator/e2e/test-matrix.yaml
#
# Source of truth for E2E test classification.
# Each suite must appear in exactly one of: agnostic, sensitive, capability.
#
# Validated by `make verify-test-matrix` against:
#   - the test files actually present under operator/e2e/tests/
#   - the capability names referenced by suites must be declared by at least one
#     backend in the `backends:` section.

apiVersion: e2e.grove.io/v1alpha1
kind: TestMatrix

# Backend declarations — alpha stop-gap.
# The conceptual source of truth for each backend's capabilities is its interface
# implementations in operator/internal/scheduler/<backend>/. This section mirrors
# that information for the selector to consume; it is intended to be replaced by
# a code-side resolver in a follow-up PR (see "Backend Capability Declaration").
backends:
  - name: kai-scheduler
    primary: true
    capabilities:
      - gang-scheduling
      - topology-aware-scheduling
      - hierarchical-resource-sharing
      - auto-mnnvl
  - name: default-scheduler
    primary: false
    capabilities: []   # gang-scheduling pending Workload API graduation in K8s

# Path-filter rules: which paths require which suite-set to run on PRs.
# Used by hack/e2e-select in --mode=pr.
pathFilters:
  - paths: ["operator/internal/scheduler/kai/**"]
    selects: { backends: ["kai-scheduler"], tiers: ["sensitive", "capability"] }
  - paths: ["operator/internal/scheduler/kube/**"]
    selects: { backends: ["default-scheduler"], tiers: ["sensitive", "capability"] }
  - paths: ["operator/internal/scheduler/**"]   # framework-shared, anything not matched above
    selects: { backends: "all", tiers: ["sensitive", "capability"] }
  - paths: ["operator/api/**", "operator/charts/**"]
    selects: { backends: "primary", tiers: ["agnostic", "sensitive"] }
  - paths: ["operator/e2e/**", ".github/**"]
    selects: { backends: "all", tiers: ["sensitive", "capability"] }
  - paths: ["docs/**", "**.md"]
    selects: {}   # no e2e

# Suite classification.
suites:
  agnostic:
    - name: cert_management
      pattern: "^Test_CM"
      makeTarget: run-e2e-full
    - name: crd_installer
      pattern: "^Test_CRD_Installer"
      makeTarget: run-e2e-full

  sensitive:
    - name: rolling_updates
      pattern: "^Test_RU"
      makeTarget: run-e2e-full
    - name: ondelete_updates
      pattern: "^Test_OD"
      makeTarget: run-e2e-full
    - name: startup_ordering
      pattern: "^Test_SO"
      makeTarget: run-e2e-real-full

  capability:
    - name: gang_scheduling
      pattern: "^Test_GS"
      makeTarget: run-e2e-full
      requires: ["gang-scheduling"]
    - name: topology_aware_scheduling
      pattern: "^Test_TAS"
      makeTarget: run-e2e-full
      requires: ["topology-aware-scheduling"]
    - name: resource_sharing
      pattern: "^Test_RS"
      makeTarget: run-e2e-full
      requires: ["hierarchical-resource-sharing"]
    - name: auto_mnnvl
      pattern: "^Test_AutoMNNVL"
      makeTarget: run-e2e-mnnvl-full
      requires: ["auto-mnnvl"]
```

### Initial Classification Table

The classification of the 9 existing suites at the time of writing:

| Suite | Tier | Required Capability | Justification |
|---|---|---|---|
| `cert_management` | agnostic | — | Webhook TLS round-trip; no scheduler-specific assertions. Validated by `git grep schedulerName operator/e2e/tests/cert_management_test.go` (no hits). |
| `crd_installer` | agnostic | — | CRD presence + init-container; no PodGang or scheduler path. |
| `rolling_updates` | sensitive | — | Tests rollout semantics that depend on `Backend.PreparePod` ordering. |
| `ondelete_updates` | sensitive | — | Tests delete-recreate semantics; backend lifecycle hooks involved. |
| `startup_ordering` | sensitive | — | Pod ordering depends on backend gate-management. (Uses `run-e2e-real-full` target.) |
| `gang_scheduling` | capability | `gang-scheduling` | Asserts gang admission and pod batch scheduling. |
| `topology_aware_scheduling` | capability | `topology-aware-scheduling` | Calls into `TopologyAwareSchedBackend`. |
| `resource_sharing` | capability | `hierarchical-resource-sharing` | KAI-specific hierarchical queue model (GREP-390). |
| `auto_mnnvl` | capability | `auto-mnnvl` | Single-annotation MNNVL grouping (GREP-417). (Uses `run-e2e-mnnvl-full` target.) |

`scale_test` is intentionally omitted because it is exercised by `make run-scale-test`, not the standard CI matrix. If/when it is wired into the matrix, it will be classified at that time.

### Selector: `hack/e2e-select`

A small Go binary (~200 lines) consumes `test-matrix.yaml`, the changed-file set, the CI mode, and the backend registry, and emits a GHA matrix JSON.

**Inputs:**

```
--matrix-file   operator/e2e/test-matrix.yaml
--mode          pr | nightly
--changed-files - | path/to/list   (one path per line; from `gh pr diff --name-only` or `git diff --name-only`)
```

**Output (stdout, GHA-consumable):**

```json
{
  "include": [
    {"backend": "kai-scheduler", "test_name": "rolling_updates", "test_pattern": "^Test_RU", "make_target": "run-e2e-full"},
    {"backend": "kai-scheduler", "test_name": "topology_aware_scheduling", "test_pattern": "^Test_TAS", "make_target": "run-e2e-full"},
    {"backend": "default-scheduler", "test_name": "rolling_updates", "test_pattern": "^Test_RU", "make_target": "run-e2e-full"}
  ]
}
```

**Algorithm (PR mode):**

1. For each changed file, match `pathFilters` in declaration order and use the first matching rule for that file. Then union the selected backends and tiers across all changed files. (Most PRs match one rule; framework changes may match the broader rule alone.)
2. Translate `backends: "all"` to every backend in the `backends:` section; `"primary"` to the backend with `primary: true`.
3. For each `(backend, tier)` pair, expand to `(backend, suite)` pairs, filtering capability-tier suites to those whose `requires` are a subset of the backend's declared `capabilities`.
4. If a path filter `selects: {}` matched (e.g. docs-only PR), output an empty matrix.

**Algorithm (nightly mode):**

1. Ignore `pathFilters`. For each suite:
   - `agnostic` → 1 entry on the primary backend.
   - `sensitive` → N entries, one per backend in the `backends:` section.
   - `capability` → entries on each backend whose `capabilities` is a superset of the suite's `requires`.

**Validation (`make verify-test-matrix`):**

- Every `*_test.go` file under `operator/e2e/tests/` (modulo helper packages) maps to exactly one classified suite.
- Every `requires:` entry in `test-matrix.yaml` is referenced by at least one backend's `capabilities:` in the `backends:` section (or the selector cannot ever schedule the suite).
- Every `pattern:` is non-empty and matches at least one `func Test_*` in the corresponding suite file.
- Exactly one backend has `primary: true`.
- The `backends:` section's declared `capabilities:` are reviewed against a hand-maintained allowlist that mirrors the actual interface implementations under `operator/internal/scheduler/<backend>/`. Drift here is the alpha stop-gap's main risk and the only thing that prevents this from being a single-SoT design.

### PR CI Workflow Changes

`build-check-test.yaml` is restructured so the `e2e` job no longer hardcodes the matrix:

```yaml
# new job, runs early on ubuntu-latest
e2e-select:
  needs: changes
  if: |
    github.event_name == 'pull_request' &&
    needs.changes.outputs.e2e-relevant == 'true' &&
    (github.event.pull_request.draft == false || contains(github.event.pull_request.labels.*.name, 'run-e2e'))
  runs-on: ubuntu-latest
  outputs:
    matrix: ${{ steps.select.outputs.matrix }}
    has-jobs: ${{ steps.select.outputs.has-jobs }}
  steps:
    - uses: actions/checkout@v4
    - run: |
        gh pr diff --name-only ${{ github.event.pull_request.number }} > /tmp/changed-files
        ./hack/e2e-select --matrix-file operator/e2e/test-matrix.yaml \
                         --mode pr \
                         --changed-files /tmp/changed-files \
                         > /tmp/matrix.json
        echo "matrix=$(cat /tmp/matrix.json)" >> $GITHUB_OUTPUT
        echo "has-jobs=$(jq '.include | length > 0' /tmp/matrix.json)" >> $GITHUB_OUTPUT
      id: select

e2e:
  needs: [test, build, check, e2e-select]
  if: needs.e2e-select.outputs.has-jobs == 'true'
  runs-on: prod-grove-e2e-v1
  timeout-minutes: 60
  strategy:
    fail-fast: false
    matrix: ${{ fromJson(needs.e2e-select.outputs.matrix) }}
  name: E2E - ${{ matrix.backend }} - ${{ matrix.test_name }}
  steps:
    # ... existing steps ...
    - name: Run e2e tests
      run: |
        make ${{ matrix.make_target }} \
             TEST_PATTERN='${{ matrix.test_pattern }}' \
             BACKEND='${{ matrix.backend }}' \
             E2E_CREATE_FLAGS='--dind-memory-mode'
      working-directory: operator
```

The `e2e-skip` job (which today reports a synthetic pass for required-status-check satisfaction on docs-only PRs) is preserved but adapted to consume the same matrix shape, so branch protection rules continue to require the same job names.

The `BACKEND` parameter is consumed by `make run-e2e-full` (and variants), which resolves it into an `OperatorConfiguration` deploy profile (via the existing `operator/e2e/setup/grove.go` helmValues struct). `BACKEND=kai-scheduler` deploys with kai as the default profile (the current behavior); other values plug in the relevant profile. The harness layer is GREP-375's responsibility — this GREP only adds the `BACKEND` parameter contract.

### Nightly CI Workflow

A new file `.github/workflows/e2e-nightly.yaml`:

```yaml
name: Nightly E2E

on:
  schedule:
    - cron: "0 7 * * *"   # 07:00 UTC daily; adjust to maintainer preference
  workflow_dispatch:        # allow manual trigger

jobs:
  e2e-select:
    runs-on: ubuntu-latest
    outputs:
      matrix: ${{ steps.select.outputs.matrix }}
    steps:
      - uses: actions/checkout@v4
      - run: |
          ./hack/e2e-select --matrix-file operator/e2e/test-matrix.yaml \
                           --mode nightly \
                           > /tmp/matrix.json
          echo "matrix=$(cat /tmp/matrix.json)" >> $GITHUB_OUTPUT
        id: select

  e2e:
    needs: e2e-select
    runs-on: prod-grove-e2e-v1
    timeout-minutes: 90
    strategy:
      fail-fast: false
      matrix: ${{ fromJson(needs.e2e-select.outputs.matrix) }}
    name: Nightly - ${{ matrix.backend }} - ${{ matrix.test_name }}
    steps:
      # same body as PR e2e job
      ...
  
  notify:
    needs: e2e
    if: failure()
    runs-on: ubuntu-latest
    steps:
      - name: Open / update tracking issue on failure
        # See "Open Question 2" — exact mechanism TBD with maintainers
        run: ...
```

### Migration Plan

The change rolls out in three phases, keyed to the [Graduation Criteria](#graduation-criteria).

**Phase 1 — Selector + dry-run (Alpha).**

- Add `operator/e2e/test-matrix.yaml` (with the `backends:` stop-gap section) and `hack/e2e-select`.
- Add `make verify-test-matrix` to the `check` make target so drift is caught on every PR.
- In `build-check-test.yaml`, **leave the existing matrix unchanged** but add an `e2e-select-dry-run` job that runs the selector and posts the would-be matrix as a PR comment for visual diffing.
- Soak for ≥2 weeks. Compare selector output vs. existing matrix on every PR.

(The capability resolver — `capabilities.go` or `Backend.Capabilities()` — is intentionally **not** part of alpha. See [Future Work](#future-work).)

**Phase 2 — Selector authoritative on PR (Beta).**

- Cut `build-check-test.yaml` over: selector becomes the matrix source.
- Initially the selector still emits only KAI entries (since default-scheduler is not yet enabled in the e2e harness deploy profile).
- Add the `BACKEND` parameter to make targets and the harness; default to `kai-scheduler` for backward compatibility.

**Phase 3 — Default-scheduler enabled + Nightly (Beta → GA).**

- Enable default-scheduler in the e2e harness deploy profile.
- Selector now emits both KAI and default-scheduler entries for sensitive suites; capability suites remain on KAI only until default-scheduler implements the relevant interfaces.
- Add `.github/workflows/e2e-nightly.yaml`.

At every phase, KAI's existing coverage is preserved; the framework only adds entries, never removes.

### Monitoring

The framework adds no runtime metrics to Grove (it operates entirely in CI). The CI-side observability points are:

- **`make verify-test-matrix` on every PR.** Hard-fails if `test-matrix.yaml` drifts from the set of test files under `operator/e2e/tests/`, if a suite's `requires:` references an undeclared capability, or if the `backends:` section's declared capabilities diverge from the hand-maintained allowlist. This is the primary defense against silent coverage loss during the alpha stop-gap.
- **Dry-run comparison artifact in Phase 1.** During the alpha soak, every PR run posts a comment showing the selector's would-be matrix vs. the actual matrix. Maintainers eyeball it; once stable, this becomes an artifact rather than a comment.
- **Nightly failure routing.** See [Open Question 2](#open-questions). Whatever mechanism is chosen, it is a visible signal that some backend × suite combination has regressed even if PR CI was green (because PR CI by design does not run the full nightly matrix).
- **GHA job count per PR.** Used as a soft cost signal — if median PR matrix size grows unexpectedly after path-rule changes, it indicates a bug in the rule set.

### Dependencies

- **GREP-375 must be merged.** This GREP consumes `Backend`, `SchedulerProfile`, and `OperatorConfiguration.Scheduler` from GREP-375. As of writing, GREP-375 is merged on main; verified by inspecting `operator/internal/scheduler/types.go` and `operator/charts/values.yaml`.
- **No new external tools.** `dorny/paths-filter@v3` is already used by `build-check-test.yaml`. The selector binary is plain Go using `sigs.k8s.io/yaml` and stdlib JSON; no new go.mod entries.
- **No changes to `operator/e2e/setup`** beyond adding a `BACKEND` parameter that picks an existing profile in `helmValues`. The k3d cluster shape (28 worker nodes), KAI installation, and GPU operator setup are unchanged.

### Test Plan

This proposal adds three layers that need testing.

**Unit tests for the selector (`hack/e2e-select`).** ≥ 15 cases covering:

1. Agnostic suite + path matching primary-only filter → 1 entry on primary.
2. Sensitive suite + framework-shared path → N entries (one per backend).
3. Capability suite + capable backend → 1 entry; same suite + non-capable backend → 0 entries.
4. Docs-only path → empty matrix.
5. Backend-specific path → only that backend's relevant suites.
6. Multiple matching paths → union of selects (no duplicates).
7. Unknown capability in `requires` → selector exits non-zero.
8. Suite present in `test-matrix.yaml` but no test file → fail (verify-test-matrix).
9. Test file present but not in `test-matrix.yaml` → fail (verify-test-matrix).
10. Nightly mode ignores path filters and emits the full matrix.
11–14: Edge cases (empty changed-files, single-suite test-matrix, capability with no implementer in `backends:`, missing or duplicate `primary: true`).

**`make verify-test-matrix` integration test.** A small fixture in `hack/e2e-select/testdata/` with intentional drift — extra suite, missing capability, mismatched pattern — verifies the check fails with the right diagnostic.

**Phase 1 dry-run comparison.** During alpha, every PR triggers a dry-run that diffs selector output against the existing matrix. The proposal merges only after ≥2 weeks of dry-run with zero unexpected diffs.

**L20 lightweight validation.** Before submitting the implementation PRs, a maintainer or contributor with access to a Grove e2e cluster runs `cert_management` + `crd_installer` (the agnostic suites) end-to-end, confirming that today's pipeline still works exactly as today. This is the minimum bar for "selector framework + same suites = same outcome." Results attached as `l20-validation.md` in this proposal directory once available.

**Existing E2E suites.** No changes; they continue to run on KAI throughout migration.

### Graduation Criteria

#### Alpha

- `test-matrix.yaml` (with `backends:` stop-gap), `hack/e2e-select`, `make verify-test-matrix` all merged.
- Existing `build-check-test.yaml` matrix unchanged; selector runs in dry-run side-by-side.
- Selector unit tests ≥ 90% coverage.
- Documentation in `operator/e2e/README.md` describes the classification model and selector.

#### Beta

- Selector becomes authoritative for PR CI.
- `BACKEND` parameter wired through make targets and harness.
- ≥4 weeks of stable PR CI behavior under the selector.
- Default-scheduler enabled in e2e harness for at least the sensitive suites.
- Nightly workflow merged and running on schedule.

#### GA

- ≥1 release cycle of stable nightly with failure-routing in place.
- At least one of: (a) a third backend integrated and exercised by sensitive suites under this framework, or (b) default-scheduler implementing one capability interface (e.g. gang-scheduling via Workload API) with the corresponding capability suite running against it.
- `test-matrix.yaml` has been edited at least once by a non-author contributor (signal that the schema is understandable in practice).

## Open Questions

1. **Capability resolver shape.** The follow-up that lifts the alpha YAML stop-gap (see [Backend Capability Declaration](#backend-capability-declaration)) has two reasonable forms: (a) a small shim file `operator/internal/scheduler/capabilities.go` mapping names to interface checks against the registry, or (b) an explicit `Capabilities() []Capability` method on the `Backend` interface. (a) keeps `Backend` unchanged; (b) makes capabilities first-class but is a public API change requiring its own GREP. This proposal does not pin the choice — the alpha contract is independent of which path is taken later.
2. **Nightly failure routing.** Three options: (a) one tracking issue per failing combination, auto-opened, deduplicated by title; (b) a single rolling "nightly status" issue that's updated daily; (c) Slack/email notification only. This GREP defers the choice to maintainers; the implementation PR will pick one based on review.

## Future Work

The following are deliberately scoped out of this GREP to keep alpha small. They are listed here so reviewers know what is explicitly *not* being decided now, and so future PRs have a documented anchor.

1. **Capability resolver replacing the YAML `backends:` stop-gap.** See [Open Question 1](#open-questions). Once the resolver lands, the `backends:` section is removed from `test-matrix.yaml` and the selector reads capabilities from the scheduler package directly. This eliminates the only drift risk in the alpha design.
2. **Selector sharding (`--shard i/n`).** If the nightly matrix grows large enough that wall-clock time becomes a concern, the selector can emit lexicographically-sharded subsets of the (backend × suite) matrix so nightly can be split across multiple scheduled runs (e.g. weekday-based or hourly). Today's matrix is small enough that sharding is YAGNI.
3. **`scale_test` integration.** Today exercised by `make run-scale-test` outside the standard CI matrix. If/when scale tests are wired in, classification will be assigned at that time (likely `sensitive`).
4. **Per-test capability annotations as a complement to suite-level classification.** Today classification is suite-grained (a whole `Test_GS*` set is `capability/gang-scheduling`). A finer-grained model — per-test annotations — would only be worth adding if individual tests within a suite legitimately diverge across backends. Not needed today.

## Implementation History

- **2026-MM-DD**: GREP draft submitted (this PR).

## Alternatives

### Alternative 1: Run every suite on every backend on every PR

Conceptually simple; gives maximum coverage. Rejected because cost grows linearly with backend count, the marginal signal from running `cert_management` on every backend is zero, and capability suites would either need test-level skip logic (worse than YAML classification) or fail spuriously on non-capable backends.

### Alternative 2: Move all e2e under a single suite-set ("e2e-core") and rely on Ginkgo labels

Some Kubernetes ecosystem projects use Ginkgo `Label("backend:kai")` to express the same idea. Rejected because (a) Grove already uses standard `testing.T + -run pattern`, switching to Ginkgo is a much larger change with its own design cost; (b) labels in test code couple classification metadata to source — a new backend cannot be added without touching every relevant test file, defeating the goal.

### Alternative 3: Backend-specific duplicated test suites

E.g. `tests/kai/rolling_updates_test.go` and `tests/kube/rolling_updates_test.go` as separate copies. Rejected because of maintenance burden and inevitable behavioral drift between copies.

### Alternative 4: Only run multi-backend on nightly; keep PR CI single-backend

Lower migration cost; but defers backend-specific regression detection to nightly, which means breakage lands on main and is found ≤24h later instead of being blocked at PR. Also gives no incentive to keep the matrix lean — every PR pays full KAI cost regardless of relevance. Rejected.

### Alternative 5: Land the capability resolver in alpha, not as future work

Considered: ship `operator/internal/scheduler/capabilities.go` (or `Backend.Capabilities()`) in alpha alongside the selector, eliminating the YAML stop-gap from day one. Rejected for alpha because it (a) couples this GREP's review to a scheduler-package API decision (see [Open Question 1](#open-questions)) that is best handled in its own PR and review thread, and (b) bundles two distinct concerns — selector contract and capability SoT — into one change, making it harder to revert or iterate on either independently. The selector contract is the larger and more discussed surface; lifting the YAML stop-gap is mechanical. Splitting them is the lower-risk path. The stop-gap's drift risk is contained by the `make verify-test-matrix` allowlist check.

### Alternative 6: A separate `backends.yaml` parallel to `test-matrix.yaml`

Earlier drafts considered `operator/e2e/backends.yaml` as a permanent home for backend capability declarations. Rejected because backend capabilities are conceptually a property of the backend implementation, not of the e2e framework. Two separate files would invite drift between the test-classification file and the backend-capability file with no benefit over a single `test-matrix.yaml` for the alpha stop-gap (which is itself temporary). The future-work resolver eliminates the stop-gap entirely rather than codifying it as a permanent second config.

## Appendix

### Reference: Existing E2E Matrix in `build-check-test.yaml`

For context, the matrix this GREP replaces (extracted from `.github/workflows/build-check-test.yaml`):

```yaml
strategy:
  matrix:
    include:
      - test_name: gang_scheduling          ; test_pattern: "^Test_GS"
      - test_name: rolling_updates          ; test_pattern: "^Test_RU"
      - test_name: ondelete_updates         ; test_pattern: "^Test_OD"
      - test_name: startup_ordering         ; test_pattern: "^Test_SO"
        make_target: run-e2e-real-full
      - test_name: Topology_Aware_Scheduling; test_pattern: "^Test_TAS"
      - test_name: cert_management          ; test_pattern: "^Test_CM"
      - test_name: auto_mnnvl               ; test_pattern: "^Test_AutoMNNVL"
        make_target: run-e2e-mnnvl-full
      - test_name: crd_installer            ; test_pattern: "^Test_CRD_Installer"
      - test_name: resource_sharing         ; test_pattern: "^Test_RS"
```

(The `test_name` casing is inconsistent today — `Topology_Aware_Scheduling` mixed case vs. others snake_case. The implementation PR will normalize all entries to kebab-case via `test-matrix.yaml`, which is also more consistent with backend names like `kai-scheduler`.)

### Reference: GREP-375 capability extension pattern

The pattern this GREP relies on already exists in `operator/internal/scheduler/types.go`:

```go
// Base interface every backend implements.
type Backend interface {
    Name() string
    Init() error
    SyncPodGang(ctx, *PodGang) error
    OnPodGangDelete(ctx, *PodGang) error
    PreparePod(*corev1.Pod)
    ValidatePodCliqueSet(ctx, *PodCliqueSet) error
}

// Capability extension interface; KAI implements, default-scheduler does not.
type TopologyAwareSchedBackend interface {
    TopologyGVR() schema.GroupVersionResource
    SyncTopology(ctx, ...) error
    OnTopologyDelete(ctx, ...) error
    CheckTopologyDrift(...) (bool, string, int64, error)
}
```

This GREP names this pattern explicitly and uses it as the capability source of truth. Future capabilities follow the same shape (a small Go interface in `operator/internal/scheduler/`, optionally implemented by each backend).

### Glossary

- **Backend** — a registered scheduler backend; e.g. `kai-scheduler`, `default-scheduler`. Defined by GREP-375.
- **Capability** — a discrete scheduler feature (e.g. gang-scheduling, topology-aware-scheduling) declared by a backend implementing the corresponding extension interface in `operator/internal/scheduler/`.
- **Suite** — a group of E2E tests under `operator/e2e/tests/` matched by a single `-run` pattern (e.g. `^Test_GS` is the `gang_scheduling` suite).
- **Tier** — agnostic, sensitive, or capability — determines which backends a suite runs against.
- **Primary backend** — the backend designated as `defaultProfileName` in the e2e harness deploy profile. Today this is `kai-scheduler`.

> NOTE: This GREP follows the [GREP template](../NNNN-template/README.md). Inspired by [Kubernetes KEP template](https://github.com/kubernetes/enhancements/blob/master/keps/NNNN-kep-template/README.md).

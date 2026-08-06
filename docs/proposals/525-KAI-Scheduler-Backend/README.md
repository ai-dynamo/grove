# GREP-525: KAI Scheduler Backend for Scheduler Backend Framework

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Platform Operator Enables KAI Backend](#story-1-platform-operator-enables-kai-backend)
    - [Story 2: Workload Owner Uses KAI Scheduler](#story-2-workload-owner-uses-kai-scheduler)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
    - [Minimum Supported KAI Version](#minimum-supported-kai-version)
- [Design Details](#design-details)
  - [Architecture Overview](#architecture-overview)
  - [Backend Lifecycle Contract](#backend-lifecycle-contract)
  - [Precondition: KAI Backend Enabled](#precondition-kai-backend-enabled)
  - [KAI Backend Responsibilities](#kai-backend-responsibilities)
  - [PodCliqueSet to PodGroup Mapping](#podcliqueset-to-podgroup-mapping)
    - [KAI Queue Resolution](#kai-queue-resolution)
    - [SubGroup Mapping Rules](#subgroup-mapping-rules)
    - [Topology Mapping](#topology-mapping)
  - [Pod Preparation](#pod-preparation)
  - [PodGroup Update Semantics](#podgroup-update-semantics)
  - [Reconciliation Flow](#reconciliation-flow)
  - [API and Registration Requirements](#api-and-registration-requirements)
  - [RBAC Matrix](#rbac-matrix)
  - [Dynamic RBAC Strategy](#dynamic-rbac-strategy)
  - [Test Plan](#test-plan)
    - [Phase 1 (Current): Unit and Upgrade E2E Tests](#phase-1-current-unit-and-upgrade-e2e-tests)
    - [Phase 2 (Follow-up): E2E Tests](#phase-2-follow-up-e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
- [Appendix](#appendix)
<!-- /toc -->

## Summary

This proposal adds a dedicated KAI scheduler backend to Grove's Scheduler Backend Framework. Grove creates one KAI PodGroup per PodCliqueSet replica, represents Base and Scaled PodGangs as subgroups, and relies on KAI-Scheduler's externally-created PodGroup support. Topology constraints follow the PodGang representation defined by [GREP-244](../244-topology-aware-scheduling/README.md).

## Motivation

GREP-375 introduced a generic Scheduler Backend Framework, but the KAI integration still needs a concrete backend implementation pattern and operational contract for production use. Without this backend, KAI support depends on legacy behavior that can cause ambiguous ownership of PodGroup resources and complicate migration as Grove evolves.

### Goals

- Define the KAI backend behavior under the Scheduler Backend Framework lifecycle.
- Define `PreparePod` behavior so Pods are scheduled by KAI consistently with Grove's scheduling gate flow and opt out of KAI podgrouper reconciliation when Grove owns the PodGroup.
- Specify PodCliqueSet-replica to KAI PodGroup translation, migration, and reconciliation responsibilities.
- Define ownership-based cleanup behavior for KAI-owned scheduling resources.
- Document the minimum supported KAI-Scheduler version and required PodGroup capabilities.
- Clarify required RBAC, scheme registration, and dependency/version expectations for KAI resources.
- Establish test expectations for pod preparation, PodGroup sync, and delete paths.

### Non-Goals

- Redesigning the Scheduler Backend Framework introduced by GREP-375.
- Introducing new user-facing scheduling APIs in PodCliqueSet or PodGang for this phase.
- Covering support for all third-party schedulers; this proposal only scopes KAI backend behavior.
- Defining advanced KAI-only scheduling semantics beyond existing PodGang intent.
- Replacing or deprecating non-KAI backends.
- Defining how scheduler backends are enabled, selected, or resolved from operator configuration and workload templates. This proposal assumes the `kai-scheduler` backend is already enabled by the Scheduler Backend Framework.
- Requiring PodGang status-only updates to trigger backend reconciliation. The current backend controller reacts to create, delete, and generation-changing updates.
- Extending or refactoring KAI Topology resource management defined by GREP-244.

## Proposal

Grove will ship a built-in `kai-scheduler` backend that uses the existing Scheduler Backend Framework hooks. The backend converts all PodGangs belonging to one PodCliqueSet replica into one KAI PodGroup, prepares Pods to use that aggregate, and keeps the representation synchronized from PodGang events. No new backend interface or status condition is introduced.

At a high level, the proposal introduces:

1. **KAI backend ownership model**: Grove backend controller is the single owner of KAI PodGroup reconciliation for PodGang resources that select `kai-scheduler`.
2. **Deterministic lifecycle behavior**: `PreparePod` routes new Pods to the aggregate PodGroup, while `SyncPodGang` reconciles the complete PCS-replica representation. Aggregate PodGroups are owned by the PodCliqueSet and cleaned up by Kubernetes garbage collection.
3. **KAI version dependency**: This backend requires KAI-Scheduler `v0.15.0` or newer, because that version supports both PodGroup subgroups and externally-created PodGroups, including `kai.scheduler/skip-podgrouper`.
4. **Operator readiness requirements**: KAI PodGroup API types are registered in Grove scheme and RBAC allows backend operations on KAI PodGroups.
5. **Update safety**: Grove preserves fields that KAI runtime components own so backend reconciliation does not erase scheduler decisions or mutable runtime state.

### User Stories

#### Story 1: Platform Operator Enables KAI Backend

As a platform operator, I want Grove to manage KAI scheduling resources through its backend framework so that KAI integration follows a consistent operator lifecycle and is easier to operate and troubleshoot.

#### Story 2: Workload Owner Uses KAI Scheduler

As a workload owner, I want my PodGang workloads targeting KAI to automatically produce and maintain the required KAI PodGroup resources so that gang scheduling intent is enforced without manual intervention.

### Limitations/Risks & Mitigations

The KAI backend depends on KAI scheduler features that are not available in older KAI releases.

#### Minimum Supported KAI Version

The minimum supported KAI-Scheduler version for this backend is **v0.15.0**.

KAI-Scheduler v0.15.0 is required because it provides both capabilities this backend relies on:

- **PodGroup subgroups**: Grove maps PodGang pod groups to KAI PodGroup subgroups so KAI can preserve per-group gang semantics.
- **Externally-created PodGroup support**: Grove owns PodGroup creation and reconciliation, while KAI consumes those PodGroups without recreating or overwriting them. This includes the `kai.scheduler/skip-podgrouper` behavior introduced by [KAI PR #1552](https://github.com/kai-scheduler/KAI-Scheduler/pull/1552).

Operational behavior:

- During backend `Init()`, Grove checks that the detected KAI version is `v0.15.0` or newer.
- If KAI is below `v0.15.0`, backend startup returns an unsupported-version error and does not enable KAI PodGroup ownership reconciliation.
- Operators must disable KAI stale-gang eviction with `--default-staleness-grace-period=-1` before upgrading. Otherwise KAI can evict a transitioning gang while Grove moves Pods between PodGroups.
- Grove release notes MUST publish and maintain a Grove-to-KAI compatibility matrix whenever the minimum supported KAI version changes.

## Design Details

### Architecture Overview

The KAI backend extends GREP-375 by implementing KAI-specific translations and lifecycle handling while preserving framework-level control flow.

```mermaid
flowchart TD
    A[Operator startup] --> B[KAI backend Init: version/capability guard]
    B -->|compatible| G[PodCliqueSet controller]
    B -->|incompatible| X[Fail closed: backend not enabled]
    G[PodCliqueSet controller] --> H[Create PodGang with scheduler label]
    I[PodClique controller] --> J[PreparePod sets schedulerName and Pod skip-podgrouper annotation]
    H --> K[PodGang backend controller]
    K --> L[KAI Backend SyncPodGang sets PodGang skip-podgrouper annotation]
    L --> M[Create or update PCS-replica KAI PodGroup]
    M --> N[Patch existing Pod membership]
    N --> O[Delete legacy per-PodGang PodGroups]
```

### Backend Lifecycle Contract

The backend must cover the PodGroup-related backend surface from GREP-375:

| Lifecycle surface | Trigger | KAI backend responsibility |
| --- | --- | --- |
| Backend initialization | Operator startup | Validate KAI-Scheduler version is `v0.15.0` or newer; otherwise fail closed and do not enable backend ownership mode. |
| Pod preparation | PodClique controller builds a Pod | Set `schedulerName`, skip-podgrouper annotation, aggregate `pod-group-name`, and leaf subgroup label. |
| PodGang sync | PodGang create, generation-changing update, or deletion start | Ensure KAI metadata and reconcile the complete PCS-replica KAI PodGroup. |
| PodCliqueSet deletion | Kubernetes owner deletion | Garbage-collect the PCS-owned aggregate PodGroups. |

### Precondition: KAI Backend Enabled

This proposal assumes the Scheduler Backend Framework has already enabled and initialized the `kai-scheduler` backend. The mechanics of enabling scheduler profiles, default scheduler selection, and validation of scheduler names are defined by GREP-375 and are not redefined here.

Under that assumption, this proposal only relies on the resolved backend identity:

- Pods prepared by this backend are scheduled with `schedulerName: kai-scheduler`.
- PodGang resources routed to this backend are reconciled into KAI PodGroups.

### KAI Backend Responsibilities

- Resolve only workloads assigned to `kai-scheduler`.
- Rely on KAI-Scheduler external PodGroup support, ensure prepared Pods and Grove PodGangs have `kai.scheduler/skip-podgrouper` annotation so KAI podgrouper does not create or overwrite PodGroups that Grove owns.
- Enforce compatibility guardrails during `Init()`: require KAI-Scheduler `v0.15.0` or newer and fail closed when the minimum supported version is not met.
- Translate Grove Base PodGang and Scaled PodGang semantics to KAI PodGroup subgroup semantics.
- Reconcile KAI PodGroup state on PodGang create, update, and deletion start.
- Migrate existing Pod membership before removing legacy per-PodGang PodGroups.

### PodCliqueSet to PodGroup Mapping

The KAI backend creates one Grove-owned KAI PodGroup for each PodCliqueSet replica, then maps that replica's Grove PodGang structure into the KAI PodGroup subgroup layer. This follows Grove's existing PodGang construction model:

- **Base PodGang (BPG)**: the foundational PodGang created for each PodCliqueSet replica. It contains standalone PodCliques and the PodCliqueScalingGroup replicas that are within `[0, minAvailable-1]`.
- **Scaled PodGang (SPG)**: a PodGang created for a PodCliqueScalingGroup replica above `minAvailable`. These are the scaled-out PCSG replicas that Grove schedules as independent PodGang resources today.

In the KAI representation, BPG and SPG are not separate KAI PodGroups. They are subgroup branches under the same PCS-level KAI PodGroup.

The PodGang controller labels every PodGang with `grove.io/podcliqueset-replica-index`. It also labels each Scaled PodGang with its `grove.io/podcliquescalinggroup`; the Base PodGang omits that label because it can contain multiple scaling groups. The KAI backend uses the replica label to select one replica's PodGangs and validates the scaling-group label on each Scaled PodGang without parsing generated resource names or re-reading the PCS scaling-group configuration. When Scaled PodGangs exist, they are placed under one shared `scaled-podgangs` collection subgroup; the label value does not partition that hierarchy.

Because different PodGangs can reconcile concurrently while targeting the same aggregate PodGroup, the backend serializes the complete aggregate reconciliation by `<namespace>/<aggregate-podgroup-name>`. PodGangs from different PodCliqueSet replicas continue reconciling concurrently.

| Grove source | KAI PodGroup target |
| --- | --- |
| PodCliqueSet replica | One KAI PodGroup named `grove-<pcs-name>-<replica>` |
| PodCliqueSet and PodGang labels/annotations | PodGroup labels and annotations, preserving existing target-only keys |
| Required Base PodGang branch | PodGroup `minSubGroup: 1` before scale-out and `2` while the zero-minimum Scaled PodGang collection exists, leaving the Base PodGang as the required branch |
| PodGang priority class | PodGroup priority class |
| PodCliqueSet `kai.scheduler/queue` metadata, or a shared PodClique-template queue | PodGroup queue on initial creation |
| Base PodGang | Top-level KAI subgroup, usually named from the BPG name |
| Scaled PodGangs for the PCS replica | One shared top-level KAI subgroup named `scaled-podgangs` that groups all Scaled PodGang replicas |
| Individual Scaled PodGang replica | Child KAI subgroup under the SPG collection subgroup |
| PodGang `spec.podgroups[]` / constituent PodCliques | Leaf KAI subgroups with `name`, `minMember`, and `parent` |
| PodCliqueSet owner reference | Aggregate PodGroup ownership and garbage collection |

Topology constraints use translated PodGang fields; the backend does not retranslate user-facing topology domains.

#### KAI Queue Resolution

KAI accepts one queue per PodGroup. The backend resolves that queue from the PodGang's owning PodCliqueSet:

1. A `kai.scheduler/queue` label on the PodCliqueSet selects the queue for the complete scheduling unit. Its annotation is a compatibility fallback when the label is absent.
2. Any explicitly configured PodClique-template queue must resolve to that same queue. Without a PodCliqueSet-level queue, labels are resolved first and annotations second on every PodClique template; all non-empty template values must name the same queue.
3. For a PodCliqueSet targeting the KAI backend, admission rejects missing queue configuration and conflicting effective queue values. Reconciliation retains the same checks for pre-existing objects and does not create or update the KAI PodGroup when mapping fails.

This preserves existing workloads that set the queue on PodClique templates while allowing the PodCliqueSet to select one queue explicitly for its complete scheduling unit.

#### SubGroup Mapping Rules

SubGroup mapping is always used for KAI backend PodGroup generation.

The intended subgroup tree is:

```text
KAI PodGroup for one PCS scheduling unit (minSubGroup: 2 after scale-out)
├── BPG (minSubGroup: number of required direct children)
│   ├── PodClique / PodGang podgroup leaf
│   ├── PodClique / PodGang podgroup leaf
│   └── PodClique / PodGang podgroup leaf
└── scaled-podgangs collection (minSubGroup: 0)
    ├── SPG-1
    │   ├── PodClique / PodGang podgroup leaf
    │   ├── PodClique / PodGang podgroup leaf
    │   └── PodClique / PodGang podgroup leaf
    ├── SPG-2
    │   └── ...
    └── SPG-N
        └── ...
```

This mirrors Grove's current split between base PodGangs and scaled PodGangs while giving KAI one hierarchical PodGroup for the PCS-level scheduling unit.

Mapping contract:

- The Base PodGang maps to a top-level KAI subgroup.
- One shared Scaled PodGang collection named `scaled-podgangs` maps to the other top-level KAI subgroup whenever Scaled PodGangs exist. It is omitted when empty because KAI v0.15.2 rejects `minSubGroup` on a SubGroup without children.
- The aggregate PodGroup sets `minSubGroup: 1` while only the Base PodGang exists and `minSubGroup: 2` while the shared Scaled PodGang collection exists. KAI counts the shared `minSubGroup: 0` collection as satisfied without allocated Pods, leaving the Base PodGang as the remaining required branch regardless of subgroup ordering.
- The shared Scaled PodGang collection sets `minSubGroup: 0`, making all scaled-out PodGang branches elastic while retaining their own branch and leaf minimums.
- Each individual Scaled PodGang maps to a child subgroup under the SPG collection subgroup by setting the KAI subgroup `parent` to the SPG collection subgroup name.
- Each constituent Grove PodGang `spec.podgroups[]` entry maps to a leaf KAI subgroup under its BPG or SPG replica subgroup.
- Subgroup names must be DNS-label compatible, lowercase, and unique within the generated KAI PodGroup.
- Grove `minAvailable` / `minReplicas` requirements map to the appropriate KAI subgroup threshold:
  - BPG and SPG branch-level requirements map to KAI subgroup `minSubGroup`.
  - Leaf PodGang `spec.podgroups[]` requirements map to KAI subgroup `minMember`.
- Pod references in each Grove PodGang group are labeled with `kai.scheduler/subgroup-name=<subgroup-name>` during pod preparation/patching flow so every pod is assigned to a valid leaf subgroup.

Validation behavior:

- Because KAI-Scheduler `v0.15.0` is the minimum supported version, subgroup support is assumed to be available when this backend is enabled.
- If a pod points to a subgroup name that does not exist in the generated KAI PodGroup spec, backend returns a retryable error and emits a Warning Event on the triggering PodGang.
- If the backend cannot derive a valid Base PodGang / Scaled PodGang subgroup tree, backend validation fails.

Out of scope for this GREP:

- Defining arbitrary user-authored multi-level subgroup trees beyond the Base PodGang / Scaled PodGang structure represented by Grove PodGang semantics. Future GREP can extend parent/minSubGroup authoring semantics if Grove needs direct user-facing control.

#### Topology Mapping

Topology behavior follows GREP-244:

- Base PodGang `spec.topologyConstraint` maps to the aggregate PodGroup root.
- Base PodGang `spec.topologyConstraintGroupConfigs` map to per-PCSG-replica subgroups.
- A Scaled PodGang root constraint maps to that Scaled PodGang branch. Its collection subgroup remains topology-free because each PCSG replica is an independent placement unit.
- `spec.podgroups[].topologyConstraint` maps to the corresponding PodClique leaf subgroup.
- Required and preferred levels are copied from PodGang fields after Grove has translated topology domains to node-label keys.
- `grove.io/topology-name` identifies the ClusterTopologyBinding. The backend uses its `schedulerTopologyBindings` entry for `kai-scheduler`, or the ClusterTopologyBinding name for an auto-managed KAI Topology.
- Missing or inconsistent bindings are retried and reported through Warning Events on the triggering PodGang. When GREP-244 removes invalid PodGang constraints, the backend removes them from the aggregate without recreating Pods.

### Pod Preparation

When the KAI backend prepares a Pod, it must:

- Set `pod.spec.schedulerName` to `kai-scheduler`.
- Ensure `pod.metadata.annotations["kai.scheduler/skip-podgrouper"]` is present when missing.
- Set `pod.metadata.annotations["pod-group-name"]` to `grove-<pcs-name>-<replica>`.
- Set `pod.metadata.labels["kai.scheduler/subgroup-name"]` to the normalized PodClique leaf name.
- Preserve any existing user or controller annotations on the Pod.

The KAI backend must also ensure the routed PodGang itself has `podGang.metadata.annotations["kai.scheduler/skip-podgrouper"]` during `SyncPodGang()`.

The skip-podgrouper annotation is required because the KAI PodGroup is created externally by Grove. It must be present on both the Pods and the Grove PodGang so KAI podgrouper does not infer or reconcile PodGroup membership through either object path and compete with the Grove-owned PodGroup.

### PodGroup Update Semantics

After creation, some PodGroup fields are owned or mutated by KAI runtime components. The KAI backend must not blindly overwrite them on every Grove reconciliation. Existing runtime-managed values are inherited before comparison and update. This includes:

- Scheduler backoff state.
- Mark-unschedulable state.
- Existing queue value.
- Runtime-assigned KAI queue and node-pool labels.

For source-owned labels and annotations, Grove ensures values from the desired PodGang are present on the PodGroup while preserving unrelated existing keys.

### Reconciliation Flow

1. During startup, backend `Init()` checks that KAI-Scheduler is `v0.15.0` or newer; initialization fails closed when the minimum supported version is not met.
2. Backend controller receives PodGang event and resolves `kai-scheduler` backend.
3. KAI backend ensures the PodGang has `kai.scheduler/skip-podgrouper`.
4. KAI backend lists sibling PodGangs and computes the desired PCS-replica PodGroup, including GREP-244 topology constraints.
5. Backend creates or updates the aggregate while preserving KAI runtime-managed fields.
6. Backend patches existing Pods to the aggregate PodGroup and verifies every Pod references a valid leaf subgroup.
7. Only after verification, backend deletes legacy per-PodGang PodGroups.
8. PodGang deletion updates the aggregate tree; PodCliqueSet deletion garbage-collects the aggregate.

This same flow performs automatic upgrades from both legacy one-PodGroup-per-PodGang layouts: Grove-created PodGroups named after the PodGang, and KAI podgrouper-created PodGroups named `pg-<podgang>-<uid>`. Legacy groups are discovered through their PodGang ownership so cleanup remains retryable after Pods have already moved to the aggregate. There is no compatibility switch, migration phase, or migration condition. Failures remain retryable, are logged, and emit Warning Events on the triggering PodGang.

The backend controller only handles PodGang create, delete, and generation-changing update events. Status-only transitions, such as the PodGang `Initialized` condition, do not trigger backend reconciliation. The KAI backend design must therefore rely on spec and metadata changes for PodGroup reconciliation.

### API and Registration Requirements

- Grove runtime scheme includes KAI PodGroup API types for backend client operations.
- Existing `PreparePod` and `SyncPodGang` interfaces are sufficient; no scheduler-backend API extension is required.
- Phase 1 uses static minimal RBAC for enabled `kai-scheduler` support. Dynamic RBAC generation is planned for Phase 2 (Beta).
- KAI-Scheduler version is `v0.15.0` or newer, which includes subgroup and externally-created PodGroup support.
- Backend initialization must validate required API availability and the minimum supported KAI version before normal reconciliation.
- KAI dependency imports should consistently use the same module path and version across backend code, scheme registration, unit tests, and e2e helpers (canonical module path: `github.com/kai-scheduler/KAI-scheduler`).

### RBAC Matrix

| Backend | API group | Resource | Scope | Required verbs | Purpose |
| --- | --- | --- | --- | --- | --- |
| `kai-scheduler` | `scheduling.run.ai` | `podgroups` | Namespaced | create, get, list, watch, patch, update, delete | PodGang to KAI PodGroup reconciliation and cleanup. |

### Dynamic RBAC Strategy

This strategy is intentionally deferred to **Phase 2 (Beta)**. Phase 1 keeps static minimal RBAC for `kai-scheduler`.

In Phase 2, RBAC permissions are derived from enabled scheduler backends (`operatorConfig.scheduler.profiles`) rather than statically granting all backend permissions.

Design:

- Maintain a backend-to-rule registry in operator code (for example: `kai-scheduler` -> PodGroup CRUD rules, `default-scheduler` -> no extra scheduler CR rules).
- At startup and on scheduler profile configuration updates, the operator computes the union of rules for currently enabled backends.
- Operator reconciles a managed RBAC object set (`ClusterRole`/`Role` plus binding) containing only computed rules and marks them with Grove ownership labels/annotations.
- Rules for disabled backends are removed from the managed RBAC set on next reconcile.

Safety behavior:

- RBAC reconcile failures are treated as fatal for backend activation: backend initialization fails closed and scheduler-specific reconciliation does not start.
- Drift detection compares live managed RBAC rules with computed desired rules; drift triggers update and warning event.
- Unmanaged RBAC objects are not modified unless explicitly marked as Grove-managed.

Operational implications:

- Enabling `kai-scheduler` backend adds KAI PodGroup permissions automatically.
- Disabling `kai-scheduler` backend removes KAI PodGroup permissions from the managed RBAC set.
- Multi-backend deployments receive the union of enabled backend rules only, not blanket permissions for all supported backends.

### Test Plan

#### Phase 1 (Current): Unit and Upgrade E2E Tests

- Validate `PreparePod` sets Pod `schedulerName` to `kai-scheduler` and adds Pod annotation `kai.scheduler/skip-podgrouper` when missing without dropping existing annotations.
- Validate `Init()` compatibility guardrails: KAI versions below `v0.15.0` fail closed.
- Validate `SyncPodGang` creates and updates one aggregate KAI PodGroup per PCS replica, including queue resolution and runtime-managed field preservation.
- Validate PodCliqueSet queue precedence, consistent PodClique-template queue fallback, and mapping failures for missing or conflicting queue configuration.
- Validate `SyncPodGang` adds PodGang annotation `kai.scheduler/skip-podgrouper` when missing without dropping existing annotations.
- Validate subgroup and GREP-244 topology translation across PCS, PCSG replica, Scaled PodGang, and PodClique levels.
- Validate the aggregate root transitions from `minSubGroup: 1` without Scaled PodGangs to `2` with one shared elastic Scaled PodGang collection, and returns to `1` after the last Scaled PodGang is removed.
- Validate subgroup-name constraints (lowercase/unique/valid label) and explicit error surfacing on invalid subgroup references.
- Validate existing Pods migrate before legacy PodGroups are deleted, partial failures retry safely, and scale-down removes obsolete branches.
- Run a pinned upgrade E2E from Grove `v0.1.0-alpha.11`: preserve existing Pod UIDs and node assignments, migrate KAI podgrouper-created PodGroups, and validate the complete PCS-derived aggregate hierarchy and topology.

#### Phase 2 (Follow-up): E2E Tests

Phase 2 adds broader backend lifecycle coverage and dynamic RBAC beyond the focused upgrade E2E delivered in Phase 1:

- Dynamic RBAC implementation:
  - synthesize RBAC rules from enabled scheduler backends only,
  - remove rules when a backend is disabled,
  - fail closed when managed RBAC reconciliation fails.
- E2E coverage in cluster environments for PodGroup create/update/delete, subgroup behavior, and ownership/compatibility guardrails.
- Gang-scheduling E2E coverage for a required Base PodGang followed by incremental admission of elastic Scaled PodGangs.

Phase 2 test plan includes unit/integration tests for dynamic RBAC and E2E tests for end-to-end scheduler-backend behavior.

### Graduation Criteria

#### Alpha

- KAI backend is implemented behind framework lifecycle hooks.
- Phase 1 unit tests cover pod preparation, PodGroup translation, sync, and delete behavior.

#### Beta

- Phase 2 delivers dynamic RBAC strategy and corresponding tests.
- Phase 2 E2E coverage validates KAI backend behavior in realistic cluster environments.

#### GA

- KAI backend is stable across multiple releases with no unresolved critical issues.

## Appendix

- Scheduler Backend Framework baseline: GREP-375.
- Minimum supported KAI-Scheduler version: `v0.15.0`.
- KAI scheduler dependency context: [kai-scheduler/KAI-Scheduler PR #1552](https://github.com/kai-scheduler/KAI-Scheduler/pull/1552), which adds support for externally-created PodGroups and allows Grove to own PodGroup creation through this backend.

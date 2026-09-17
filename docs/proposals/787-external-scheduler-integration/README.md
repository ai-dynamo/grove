# GREP-787: External Scheduler Integration

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Distributing a scheduler that integrates with Grove](#story-1-distributing-a-scheduler-that-integrates-with-grove)
    - [Story 2: Keeping a placement guarantee from degrading silently](#story-2-keeping-a-placement-guarantee-from-degrading-silently)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
    - [PodGang is not a stable API](#podgang-is-not-a-stable-api)
    - [Grove does not validate scheduler version compatibility](#grove-does-not-validate-scheduler-version-compatibility)
    - [Capability declarations are unverified assertions](#capability-declarations-are-unverified-assertions)
    - [An external profile in a ClusterTopologyBinding degrades that binding's status](#an-external-profile-in-a-clustertopologybinding-degrades-that-bindings-status)
    - [An open integration surface changes the project's support posture](#an-open-integration-surface-changes-the-projects-support-posture)
    - [Name collisions and profile shadowing](#name-collisions-and-profile-shadowing)
- [Design Details](#design-details)
  - [Profile types](#profile-types)
  - [Defaulting and validation](#defaulting-and-validation)
  - [The external backend](#the-external-backend)
  - [Workload validation model](#workload-validation-model)
    - [Correctness features and quality features](#correctness-features-and-quality-features)
    - [How the vocabulary grows](#how-the-vocabulary-grows)
  - [Topology](#topology)
  - [The PodGang contract](#the-podgang-contract)
  - [Profile removal and renaming](#profile-removal-and-renaming)
  - [Monitoring](#monitoring)
  - [Dependencies](#dependencies)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
- [Implementation History](#implementation-history)
- [Alternatives](#alternatives)
  - [Validation models considered](#validation-models-considered)
  - [Treat any unrecognized profile name as external](#treat-any-unrecognized-profile-name-as-external)
  - [Query the scheduler at admission time](#query-the-scheduler-at-admission-time)
- [Appendix](#appendix)
<!-- /toc -->

## Summary

Grove can be configured to use one of several scheduler backends, but the set of names it accepts is closed:
adding a scheduler requires an entry in a Go enum, a case in a registry switch, and a backend package in the
Grove tree. This proposal defines how a scheduler that needs none of that translation work — one that reads
Grove's `PodGang` resources directly — can be registered through operator configuration alone. It covers the
configuration surface, how Grove validates a workload whose scheduler it knows nothing about, how topology
constraints are handled, and what a `PodGang` consumer may rely on.

## Motivation

[GREP-375](../375-scheduler-backend-framework/README.md) introduced the Scheduler Backend
Framework and, as its first user story, promised that a third-party scheduler developer could "integrate my
custom gang scheduler with Grove without modifying Grove's core codebase." The framework delivered the
interfaces. Registration, however, stayed closed: `SchedulerName` is an enum, `newSchedulerBackend` is a
switch over that enum with a `default` that returns an error, and every backend is a package under
`operator/internal/scheduler/`.

The consequence is that a scheduler which needs *no* Grove-side logic still cannot be used with a released
Grove build. Two of the four backends Grove ships — `kube` and `lpx` — are proof that this case is real: both
have a no-op `SyncPodGang`, and `lpx` does nothing beyond stamping `schedulerName` and rejecting topology
constraints. Anyone in that position today must carry a fork whose entire content is one enum constant and a
copy of `lpx/backend.go`, and rebase it on every release. That is a poor trade for both sides: the fork
carries no useful information, and Grove gets no signal about who is integrating or against which API shape.

Opening registration raises a second question — what Grove promises a `PodGang` consumer — which this GREP
answers narrowly. It asks for no stability commitment for `PodGang` and proposes no changes to it. External
registration ships as an alpha feature with no compatibility guarantee, which is what makes it safe to add
while the API is still moving.

### Goals

* **Specify the integration requirements.** Document what an external scheduler must do to work with Grove,
  what Grove guarantees in return, and where the boundary between them lies.
* **Make registration configuration-driven.** Allow a scheduler to be registered by name in
  `OperatorConfiguration` with no Go code, no rebuild, and no dynamic loading.
* **Define the workload validation model.** Specify how Grove admits or rejects a workload whose resolved
  scheduler's capabilities it cannot introspect, without silently downgrading user-visible guarantees.
* **Define topology behavior.** State precisely which topology features are available to an external
  scheduler and which are not, including the currently known cross-`PodGang` limitation.
* **Keep the change additive.** Existing configurations and workloads must be unaffected, with no migration.

### Non-Goals

* **A plugin framework.** No dynamic linking, Go plugins, sidecar backends, or out-of-process backend RPC.
  The only thing being opened is the *name*, not the ability to inject behavior into the operator.
* **Changing or stabilizing `PodGang`.** This GREP proposes no API changes to `PodGang`, does not promote it
  to a new version, and does not constrain in-flight work on it.
* **Defining `PodGang` semantics.** [The PodGang contract](#the-podgang-contract) describes behaviour as
  currently implemented, so that an integrator has something to build against. Settling what those semantics
  should be is separate work.
* **A home for schedulers that need real integration.** A scheduler that requires Grove to create its CRs,
  submit to its queues, or reconcile its topology resources needs an in-tree backend. External registration
  is for schedulers whose translation layer is empty.
* **Replacing or deprecating built-in backends.** `kube`, `kai`, `volcano`, and `lpx` remain in-tree.
* **Support commitments for external schedulers.** Grove does not take on testing, support, or
  compatibility obligations for a scheduler it does not ship.
* **Multi-cluster scheduling**, consistent with GREP-375's non-goals.

## Proposal

Three parts, in decreasing order of code size:

1. **A profile *type*.** `SchedulerProfile` gains a `type` field with values `BuiltIn` (the default) and
   `External`. A `BuiltIn` profile is served by a backend compiled into the operator and its name must be one
   of the known backend names, exactly as today. An `External` profile carries a user-defined name and is
   served by a single generic backend that stamps `schedulerName` onto pods and does nothing else.

2. **A capability declaration.** Because Grove cannot introspect an external scheduler, the profile declares
   which Grove workload features that scheduler understands. The declaration is consulted only by admission
   validation; it never changes what Grove writes. It is a short, slow-growing vocabulary, not a configuration
   passthrough.

3. **A written contract.** One document describes what a `PodGang` consumer can currently depend on and what
   it owes in return, as implemented rather than as promised. The consumer's side is short: read `PodGang`,
   honour its gang thresholds, schedule ungated pods carrying your `schedulerName`, leave Grove's resources
   alone. A compatibility statement says how far either side can rely on the other. The API is already
   packaged for outside use — `scheduler/api` and `scheduler/client` are separately versioned Go modules
   with a generated typed client.

The gang machinery that makes this viable is already backend-agnostic. Grove creates `PodGang` resources for
every workload regardless of backend, adds and removes the `grove.io/podgang-pending-creation` scheduling
gate generically, and owns the full lifecycle of pods and `PodGang` resources. A backend's job is only to
translate `PodGang` into scheduler-specific resources, stamp `schedulerName`, and validate workload shape.
When the translation is empty, so is the backend.

No feature-gate machinery is needed: an `External` profile only exists if a cluster administrator writes it
into `OperatorConfiguration`, so the configuration entry is itself the opt-in.

### User Stories

#### Story 1: Distributing a scheduler that integrates with Grove

As the author of a gang scheduler that consumes `PodGang` resources directly, I want my users to install my
scheduler alongside an upstream Grove release and enable it with a configuration entry, rather than shipping
them a Grove fork whose only content is an enum constant and a stub backend. My integration should be a
documented install step, not a build pipeline that has to be rebased every release.

#### Story 2: Keeping a placement guarantee from degrading silently

As the platform engineer who registered an external scheduler, I am the only person who knows which Grove
features it actually implements. The teams deploying on my cluster read Grove's API, which offers
`pack.required: rack` on any `PodCliqueSet` regardless of which scheduler will place its pods. I want to
record my scheduler's limits once, in the operator configuration, and have Grove reject a workload that
exceeds them at submission, naming the scheduler in the error. A hard placement constraint that is admitted
and then ignored leaves a workload reporting healthy while violating the guarantee it asked for; a rejection
at submission is a fixable mistake.

### Limitations/Risks & Mitigations

#### PodGang is not a stable API

**Risk**: `PodGang` is today an internal API between the operator and its in-tree backends, and it is still
changing. An external consumer that treats it as stable will break.

**Mitigation**: Alpha carries no compatibility guarantee, stated in the API documentation, the user guide,
and the release notes; an external scheduler is expected to track `PodGang` changes release to release.
Because `scheduler/api` is a separately versioned Go module that consumers pin, a change surfaces as a
compile error at an explicit dependency bump rather than as a silent runtime mismatch. Nothing in this
proposal reads or reshapes `PodGang` — the external backend's `SyncPodGang` is a no-op — so it does not
constrain in-flight API work.

#### Grove does not validate scheduler version compatibility

**Limitation**: A scheduler profile names a scheduler; it does not describe which Grove or `PodGang` versions
that scheduler was built against. Grove performs no version negotiation, exposes no field for a consumer to
declare a supported range, and cannot detect that a registered scheduler predates the `PodGang` shape the
operator is writing. A mismatch is not rejected at startup or at admission — it surfaces as gangs that are
never acted on.

**Mitigation**: An external scheduler documents the Grove versions it supports, as it would for any API it
consumes. The cluster administrator keeps the operator and the scheduler within that range, and treats a Grove
upgrade as requiring the same check as any other coupled component. Nothing in this proposal weakens that:
Grove's release notes are the announcement channel, and pinning `scheduler/api` is what turns a structural
change into a visible one for whoever builds the scheduler. Machine-checkable version declaration is a natural
candidate for the capability CRD sketched under [Alternatives](#validation-models-considered), where the
scheduler asserts its own compatibility instead of an administrator vouching for it.

#### Capability declarations are unverified assertions

**Limitation**: A declaration in `OperatorConfiguration` is an administrator's claim about a scheduler, not
something Grove can verify.

**Mitigation**: The declaration is administrator-scoped, not workload-scoped — the person making the claim is
the person operating both components, and the blast radius is their own cluster. Defaults are chosen so that
the *absence* of a claim is safe: an undeclared capability is denied, never assumed.

#### An external profile in a ClusterTopologyBinding degrades that binding's status

**Limitation**: External backends cannot participate in `ClusterTopologyBinding` reconciliation (see
[Topology](#topology)), and the natural attempt to bridge that — naming the external profile in
`spec.schedulerTopologyBindings`, the field meant for topology resources Grove does not manage itself — has
a worse outcome than not working. The controller resolves each entry against the topology-aware backend map,
misses, and sets `topologyNotFound`, which short-circuits the aggregate before any per-backend evaluation:
the binding's whole `SchedulerTopologyDrift` condition goes to `Unknown` with reason `TopologyNotFound`. One
entry for an external profile therefore masks real drift for every topology-aware backend on that
binding.

**Mitigation**: The `ClusterTopologyBinding` validating webhook rejects an entry naming a profile that is not
a topology-aware backend, so the misuse fails at admission with a clear message instead of silently
degrading the status of a shared resource. Listed under [Alpha](#alpha).

#### An open integration surface changes the project's support posture

**Risk**: Moving from a curated set of supported schedulers to an open registration surface invites bug
reports about schedulers Grove neither ships nor tests.

**Mitigation**: `External` is explicitly outside Grove's support boundary, stated in documentation. Grove
tests the mechanism — registration, validation, `schedulerName` propagation — not any particular external
scheduler, and because a profile must be written by an administrator there is no default configuration in
which an untested integration is active. If a specific external scheduler warrants support commitments, that
is an argument for an in-tree backend, which remains the existing path.

#### Name collisions and profile shadowing

**Risk**: The registry keys backends by name. Once names are user-defined, an external profile could shadow a
built-in backend, and the resolution would depend on profile order.

**Mitigation**: Validation rejects an `External` profile whose name matches a built-in backend, and rejects
duplicate profile names across both types. The registry additionally fails startup rather than overwriting,
so a validation gap cannot become a silent misroute.

## Design Details

### Profile types

```go
// SchedulerProfileType defines whether a scheduler profile is backed by a built-in Grove
// scheduler backend or by an external scheduler that integrates purely through PodGang CRs.
type SchedulerProfileType string

const (
    SchedulerProfileTypeBuiltIn  SchedulerProfileType = "BuiltIn"
    SchedulerProfileTypeExternal SchedulerProfileType = "External"
)

type SchedulerProfile struct {
    // For BuiltIn, must be one of SupportedSchedulerNames. For External, user-defined.
    Name SchedulerName `json:"name"`

    // Defaults to BuiltIn. Defaulted in code, not by the kubebuilder marker.
    // +optional
    // +kubebuilder:validation:Enum=BuiltIn;External
    Type SchedulerProfileType `json:"type,omitempty"`

    // For External, unmarshalled into ExternalSchedulerConfiguration.
    // +optional
    Config *runtime.RawExtension `json:"config,omitempty"`
}

// ExternalSchedulerConfiguration declares which Grove workload features an external scheduler
// supports. Consulted only by admission validation; never changes what Grove writes.
type ExternalSchedulerConfiguration struct {
    // +optional
    TopologyConstraints bool `json:"topologyConstraints,omitempty"`
}
```

```yaml
scheduler:
  defaultProfileName: default-scheduler
  profiles:
    - name: default-scheduler
    - name: some-custom-scheduler
      type: External
      config:
        topologyConstraints: false
```

An `External` profile may be named as `defaultProfileName`, in which case workloads that leave
`schedulerName` unset are routed to it.

An explicit `type` is preferred over inferring the type from whether the name is recognized. With `BuiltIn`
as the default, a misspelt built-in name (`kai-schedular`) still fails validation loudly instead of quietly
registering as an external scheduler that nothing is listening for. See
[Alternatives](#treat-any-unrecognized-profile-name-as-external).

### Defaulting and validation

`Type` must be defaulted **in code**, in `SetDefaults_SchedulerConfiguration`. `OperatorConfiguration` is
loaded from a file and decoded through a scheme, not served by the apiserver, so the
`+kubebuilder:default=BuiltIn` marker never executes; it is retained only because it renders into the
generated API reference.

Validation of `scheduler.profiles[i]`:

| Check | Applies to | Failure |
|---|---|---|
| `type` is one of `BuiltIn`, `External` | both | `NotSupported` on `type` |
| `name` is non-empty | both | `Required` on `name` |
| `name` is in `SupportedSchedulerNames` | `BuiltIn` | `NotSupported` on `name` |
| `name` is a valid DNS subdomain | `External` | `Invalid` on `name` |
| `name` is not a built-in backend name | `External` | `Invalid` on `name` |
| `name` is unique across all profiles | both | `Duplicate` on `name` |
| `config` decodes into `ExternalSchedulerConfiguration` with no unknown fields | `External` | `Invalid` on `config` |

The DNS-subdomain rule follows from the name being written verbatim into `Pod.Spec.SchedulerName`. An unknown
field under `config` is rejected rather than ignored because a misspelt capability key would otherwise decode
as "capability absent" and reject valid workloads at admission, with nothing pointing at the typo.

A name that fails validation is not recorded for the purposes of the existing "`default-scheduler` profile is
required" and "`defaultProfileName` must name a configured profile" checks, so an invalid profile cannot
satisfy either.

### The external backend

One generic implementation of the existing `scheduler.Backend` interface serves every `External` profile:

| Method | Behavior |
|---|---|
| `Name()` | The profile name. |
| `Init()` | No-op. Grove creates nothing on the scheduler's behalf. |
| `SyncPodGang()` | No-op. `PodGang` *is* the integration surface; there is nothing to translate it into. |
| `PreparePod()` | Sets `pod.Spec.SchedulerName` to the profile name. Leaves the scheduling gate intact. |
| `ValidatePodCliqueSet()` | Applies the [validation model](#workload-validation-model) against the declared capabilities. |

`SyncPodGang` must stay a no-op. A non-trivial `SyncPodGang` means the scheduler needs Grove to manage
resources on its behalf, which is what an in-tree backend is for. That bound is what keeps
`ExternalSchedulerConfiguration` from drifting into a general configuration passthrough.

The framework also carries optional interfaces that a backend may implement to unlock a feature, and a
generic backend serving arbitrarily-named profiles can implement none of them — each one requires knowledge
of a specific scheduler's resources:

| Optional interface | What it gates | Consequence for an external profile |
|---|---|---|
| `TopologyAwareBackend` | `ClusterTopologyBinding` reconciliation of a scheduler-specific topology CRD | Absent from `Registry.AllTopologyAware()`; the `ClusterTopologyBinding` controller never selects it. See [Topology](#topology). |
| `PodGangStatusProvider` | The `SchedulingBackendReady` condition on `PodGang.status` | Reported as `SchedulingBackendStatusUnavailable` rather than a backend-supplied verdict. |

`PodGangStatusProvider` is under review at the time of writing, so the list should be expected to grow, and
each addition widens the gap between a built-in backend and an external one. `External` is therefore scoped as
a second-class profile type: it can only ever offer the subset of Grove's features that need no
scheduler-specific code in the operator.

### Workload validation model

Grove cannot introspect an external scheduler, so for every workload feature that requires scheduler
cooperation it must choose between rejecting the workload, admitting it and hoping, or asking. GREP-375
already frames this as a per-backend choice between "fail submit" and "pass through," and adds the constraint
that matters most here:

> Validation should respect API semantics. For example: If TAS `required` constraint(s) are defined for a PCS
> and the scheduler chosen does not yet support TAS then it should return an error.

The full range of models considered is in [Alternatives](#validation-models-considered). The proposal is the
narrowest one that satisfies that constraint.

#### Correctness features and quality features

Rather than picking one policy for all features, features are split by what happens when a scheduler ignores
them:

**Correctness features — ignoring one breaks a stated guarantee.** A hard topology requirement, or gang
semantics themselves. Silently dropping these produces a workload that appears healthy while violating the
invariant the user asked for. These are **denied unless declared**: `ValidatePodCliqueSet` rejects the
workload at admission with an error naming both the feature and the profile.

**Quality features — ignoring one degrades placement without breaking a guarantee.** A preferred topology
constraint, a placement-score preference. These are already best-effort by definition; a scheduler that
ignores one has produced a legal, if worse, placement. These are **admitted with a warning** — an admission
warning at create/update time, so the person submitting the workload sees it.

Gang semantics deserve a note: `minAvailable` is a correctness feature, but it is not declarable. It is the
price of admission for using an external profile at all, which is why it appears in the contract rather than
in the capability vocabulary. A scheduler that does not honour gang thresholds has no business consuming
`PodGang`.

At alpha the vocabulary is exactly one key, `topologyConstraints`, covering the one correctness feature that
exists today beyond gang semantics. Everything else Grove writes into `PodGang` today — priority class,
`reuseReservationRef`, `placementScore` — is a quality feature or advisory, and needs no key.

#### How the vocabulary grows

The split above is what keeps the vocabulary small, and the growth rule follows from it:

- A new correctness feature adds a key. Because an undeclared capability is denied, existing external
  profiles keep working: they simply cannot use the new feature until their administrator declares it.
  Fail-closed defaults mean adding a key is never a breaking change for a workload that was already valid.
- A new quality feature adds no key. It reaches the scheduler through `PodGang` and is ignored or honoured at
  the scheduler's discretion, with an admission warning if the profile has not declared it.

Which of the two a new feature is gets decided when the feature is added, in that feature's own GREP.

### Topology

"Topology" covers two separate features in Grove, and only the first is available to an external scheduler:

1. **Topology constraints inside `PodGang`.** Grove translates a workload's domain names (`rack`, `host`)
   into cluster-specific node label keys using the `ClusterTopologyBinding`, and writes the result into
   `PodGang.spec.topologyConstraint`, `spec.topologyConstraintGroupConfigs`, and per-`PodGroup`. An external
   scheduler reads these like any other field. Available, and gated on `topologyConstraints: true`.

2. **`ClusterTopologyBinding` reconciliation of scheduler-specific topology CRDs.** For a backend that
   implements the optional `TopologyAwareBackend` interface, Grove keeps that backend's own topology CRD in
   sync from `ClusterTopologyBinding.spec.levels`. That needs per-scheduler code, so it is unavailable to an
   external profile, and declaring `topologyConstraints: true` does not change that. The cost is that the
   topology hierarchy is maintained twice — once in the binding, once in whatever the scheduler reads — with
   no drift detection between them. A scheduler that works from the constraint on the `PodGang` and node
   labels directly has no second copy and pays nothing.

`topologyConstraints: true` therefore grants exactly (1), with one caveat that applies equally to in-tree
backends: a `PodCliqueSet`-level constraint is copied onto each `PodGang` of a replica and satisfied
independently, so a replica split across several `PodGang` resources can span domains ([#648][i648]). Grove's
topology guarantees do not currently hold end-to-end for KAI either, and #648 resolves it for external and
in-tree consumers together.

### The PodGang contract

The statements below describe how Grove behaves today. They ask for no stability promise — see
[PodGang is not a stable API](#podgang-is-not-a-stable-api).

1. **Grove creates and maintains a `PodGang` for every workload,** whichever backend serves it. Each carries
   one `PodGroup` per participating `PodClique`, listing that group's member pods in `podReferences` and its
   gang-scheduling floor in `minReplicas`. Every pod Grove creates carries `spec.schedulerName` set to the
   profile name, so a scheduler recognizes its own work, and the `grove.io/podgang` label naming the pod's
   gang, so it can resolve a pod to a gang at filter or bind time.

2. **Grove holds a scheduling gate on every pod until its gang is ready to place,** and is the only party
   that removes it. Removal means the pod is recorded in its gang's `podReferences` and every `PodGang` this
   one depends on reports `Scheduled=True`. A `PodCliqueSet` replica may comprise several `PodGang` resources
   with dependencies between them, so a scheduler that looks only at ungated pods never has to model that
   ordering.

3. **The scheduler reserves capacity for `minReplicas` pods of a `PodGroup` before binding any pod of that
   group.** "Together" is a reservation requirement rather than an atomicity one: Grove does not expect a
   single transaction, it expects that no pod of a group is bound until capacity for that group's floor is
   committed. Pods beyond `minReplicas` are best-effort and may be bound as capacity allows.

4. **The scheduler does not decide gang health.** It does not evict, preempt, or terminate gang members on
   availability grounds; Grove owns that, evaluating it at the `PodCliqueSet` level from `minAvailable` and
   `terminationDelay`. `minReplicas` is a placement input only, and it does not hold its initial value for
   the life of the gang (see the note below). `kai-scheduler` needs `default-staleness-grace-period=-1` for
   this reason.

5. **The scheduler does not write `PodGang.spec`, modify pods beyond binding them, or remove scheduling
   gates.** Grove reconciles all three and reverts outside writes. The one field it may write is
   `status.placementScore`, which records how network-optimal a placement was and which nothing in Grove
   sets; Grove derives the rest of `PodGang` status by observing pods, so a scheduler that only binds is a
   complete implementation.

6. **A gang the scheduler cannot place stays pending,** and there is no obligation to signal
   unschedulability. Grove observes the resulting `minAvailable` breach on the constituent `PodClique` or
   `PodCliqueScalingGroup` and, once it has persisted for `terminationDelay`, terminates the `PodCliqueSet`
   replica and recreates its gangs and pods.

> **Note on `minReplicas` stability.** Once every `PodGroup` of a `PodGang` has been placed, Grove patches
> `minReplicas` to `0` on standalone-`PodClique` `PodGroups`; `PodCliqueScalingGroup`-member `PodGroups` keep
> their original value. This exists only because there is no API today for Grove to tell a backend not to
> terminate gangs, and it is how Grove opts out of that behaviour in backends that implement it. GREP-393
> records the intent to remove the workaround once such an API exists, at which point `minReplicas` would
> hold its initial value for the life of the gang. A scheduler that follows (3) and (4) — reading
> `minReplicas` at placement time, never as a termination trigger — is correct under either regime.

While this feature is alpha, Grove makes no compatibility promise for `PodGang`: it may change in any
release, including breaking changes to `PodGroups` structure, status, and gang semantics. Consumers pin
`scheduler/api`, so such a change surfaces as a compile error at an explicit dependency bump. Grove does not
check versions at either end of this contract — the scheduler documents the Grove versions it supports, and
the cluster administrator keeps the two within that range.

### Profile removal and renaming

Once names are user-defined, a workload outliving the profile it names becomes an ordinary lifecycle event
rather than an operator bug. Admission cannot catch it: the webhook only sees a workload at create and update
time, and the profile disappears from `OperatorConfiguration` afterwards.

The behavior is unchanged — pod creation fails during reconciliation, existing pods are untouched — but it
becomes observable: the operator records a `SchedulerBackendNotFound` warning event on the `PodCliqueSet`
naming the missing profile and the affected `PodClique`. Recovery is to restore the profile or update the
workload's `schedulerName`. The same event covers a `BuiltIn` profile being removed, which is equally
possible today and equally unobservable.

### Monitoring

Grove currently exposes no custom Prometheus metrics, only controller-runtime defaults, so this proposal adds
none; if a metric surface is introduced later, per-profile pod routing counts would belong in it.
Observability is therefore events and conditions:

| Signal | Object | When |
|---|---|---|
| `SchedulerBackendNotFound` warning event | `PodCliqueSet` | A workload names a profile that is not configured, so its pods cannot be created. |
| Admission error | `PodCliqueSet` create/update | A correctness feature is used without the capability being declared. Names both feature and profile. |
| Admission warning | `PodCliqueSet` create/update | A quality feature is used and not declared. |
| Startup failure | operator | Invalid profile configuration: unknown type, invalid or shadowing name, duplicate name, undecodable `config`. Fails fast with a field path. |

### Dependencies

* **[GREP-375](../375-scheduler-backend-framework/README.md)** — provides the `Backend`
  interface, registry, and `SchedulerConfiguration` this GREP extends. No changes to the interface are
  proposed.
* **[#648][i648]** — not a blocker, but its resolution determines whether a `PodCliqueSet`-level topology
  constraint means anything across multiple `PodGang` resources, for external and in-tree backends alike.
* **A first-class way to disable backend gang termination** — recorded as intent in GREP-393 with no issue or
  design behind it yet. This GREP neither proposes nor needs it, but it is the change that would let
  `minReplicas` mean one thing for the lifetime of a gang, and it would simplify the contract in
  [The PodGang contract](#the-podgang-contract) to a single obligation.

### Test Plan

Unit tests, extending the existing suites:

* `api/config/validation` — the full validation table above, including an `External` profile shadowing a
  built-in, duplicates across types, a non-subdomain name, an unknown `type`, and an undecodable `config`.
* `api/config/v1alpha1` — `Type` defaulted to `BuiltIn` for an omitted value and preserved for an explicit
  one, verified through `DecodeOperatorConfig` so the file-loading path is what is exercised.
* `internal/scheduler/external` — `schedulerName` stamped, scheduling gate preserved, `SyncPodGang` a no-op,
  and `ValidatePodCliqueSet` accepting or rejecting each topology-constraint position (`PodCliqueSet`,
  `PodClique`, `PodCliqueScalingGroup`) against both capability settings.
* `internal/scheduler/registry` — an external profile registers, can be the default, is absent from
  `AllTopologyAware()`, and a duplicate name fails startup.
* `internal/controller/podclique/components/pod` — the missing-profile path emits
  `SchedulerBackendNotFound` on the `PodCliqueSet`.

E2E coverage is the gap: a meaningful test needs a scheduler on the other end of the profile, and CI has none.
The preferred approach is to register `default-scheduler`'s own name through an `External` profile in a
dedicated test configuration and assert that a workload schedules end-to-end, which exercises registration,
`schedulerName` propagation, gate removal, and gang completion against a real scheduler without shipping a
test scheduler. If that proves too artificial, the fallback is a minimal `PodGang`-watching test scheduler in
the e2e harness, which additionally serves as executable documentation of the contract. A tracking issue
should be filed against whichever is chosen.

### Graduation Criteria

#### Alpha
- `type: External` implemented, with validation, in-code defaulting, and the single-key capability vocabulary
- Generic external backend implemented, with `SyncPodGang` a no-op
- `SchedulerBackendNotFound` event on the `PodCliqueSet` for a missing profile
- Contract documented in the user guide, including the no-compatibility-guarantee statement, the topology
  limitations, and that Grove tests the mechanism rather than any external scheduler
- `ClusterTopologyBinding` webhook rejects an entry in `schedulerTopologyBindings` naming a profile that is
  not a topology-aware backend, so the misuse cannot degrade a shared binding's drift status
- Unit tests passing; e2e approach chosen and tracked

#### Beta
- At least one external scheduler integrated against an upstream release, with feedback incorporated
- E2E coverage in place
- No breaking changes to the profile API or the capability vocabulary since alpha
- A `PodGang` compatibility statement published, appropriate to whatever stability `PodGang` has reached,
  covering at minimum the two surfaces an external consumer reads: the `status` condition set, and the
  lifecycle and meaning of `minReplicas`

#### GA
- Stable profile API and capability vocabulary
- Two or more external integrations running in production
- No open issues related to the feature

## Implementation History

- **2026-08-21**: Tracking issue [#787][issue] opened.
- **2026-08-24**: Direction supported with review conditions; GREP requested.

## Alternatives

### Validation models considered

The design question is what Grove does at admission when it cannot introspect the resolved scheduler.

| Model | Behavior | Why not chosen |
|---|---|---|
| **Pass everything through** | Admit any workload; the scheduler ignores what it does not understand. | Contradicts GREP-375's rule on `required` constraints. A hard topology requirement would be silently dropped, which is a correctness failure, not a degradation. |
| **Deny all advanced features, nothing declarable** | External profiles accept only the plain gang contract; no configuration surface at all. | Excludes the schedulers this GREP exists to serve: one that does support topology has no way to say so, and is pushed back to a fork. |
| **Declare capabilities, deny undeclared** *(proposed)* | Small vocabulary in operator config; correctness features denied unless declared, quality features warned. | — |
| **Declare capabilities, warn on undeclared** | Same declaration, but never reject; surface unsupported features as a `PodCliqueSet` condition. | Conditions are easy to miss, and the failure mode for a hard constraint is a workload that looks healthy while violating its placement guarantee. Adopted for quality features only, where the guarantee is best-effort by construction. |
| **Capability CRD published by the scheduler** | The scheduler's own operator installs a resource declaring its capabilities. | Better long-term: the scheduler asserts its own capabilities instead of the admin guessing. Too large for a first step, introduces ordering and staleness problems, and is closer to the plugin framework this GREP is explicitly not proposing. A natural later evolution — the operator-config form is admin-facing configuration, so replacing it is cheap. |
| **Ask the scheduler at admission** | Grove queries the scheduler during admission. | Rejected outright; see below. |

### Treat any unrecognized profile name as external

Drop the `type` field and infer: a known name is built-in, anything else is external. Smaller API surface,
but a typo in a built-in name (`kai-schedular`) would then register successfully as an external scheduler and
fail at scheduling time as pods sitting pending forever, instead of failing at startup with a clear error.
The explicit field costs one enum and buys a loud failure for the most likely mistake.

### Query the scheduler at admission time

Grove could call the external scheduler during `PodCliqueSet` admission to ask whether it can schedule the
workload. This is authoritative and always current, and it is the wrong trade: it puts a network dependency
on an external component in the admission path, couples `PodCliqueSet` creation to that component's
availability, and adds latency to every create and update. A static declaration that is occasionally stale is
preferable to an admission path that can time out.

## Appendix

Prerequisite reading:

- [GREP-375: Scheduler Backend Framework](../375-scheduler-backend-framework/README.md) — the
  `Backend` interface, registry, and `SchedulerConfiguration` this GREP extends, and the existing treatment
  of scheduler capability mismatch.
- [`scheduler/api/core/v1alpha1/podgang.go`](../../../scheduler/api/core/v1alpha1/podgang.go) — the API an external
  scheduler consumes.
- [Topology-Aware Scheduling user guide](../../user-guide/topology-aware-scheduling.md) — how workload domain
  names become node label keys before reaching `PodGang`.

[issue]: https://github.com/ai-dynamo/grove/issues/787
[i648]: https://github.com/ai-dynamo/grove/issues/648

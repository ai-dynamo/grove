# GREP-417: Selective MNNVL — Annotation-Based ComputeDomain Injection

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Selective MNNVL in a Heterogeneous Cluster](#story-1-selective-mnnvl-in-a-heterogeneous-cluster)
    - [Story 2: Partial MNNVL Within a PodCliqueSet Replica](#story-2-partial-mnnvl-within-a-podcliqueset-replica)
    - [Story 3: Simple MNNVL Without Cluster-Wide Auto-MNNVL](#story-3-simple-mnnvl-without-cluster-wide-auto-mnnvl)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
- [Design Details](#design-details)
  - [Annotation Semantics](#annotation-semantics)
  - [Operator Behavior](#operator-behavior)
  - [Interaction with Auto-MNNVL](#interaction-with-auto-mnnvl)
  - [Deployment and Configuration](#deployment-and-configuration)
  - [Monitoring](#monitoring)
  - [Dependencies (*Optional*)](#dependencies-optional)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History (*Optional*)](#implementation-history-optional)
- [Open Questions](#open-questions)
- [Alternatives](#alternatives)
- [Appendix (*Optional*)](#appendix-optional)
<!-- /toc -->

## Summary

Today, Grove supports automatic MNNVL setup (auto-MNNVL) at the cluster level: when enabled in the `OperatorConfiguration`, a single `ComputeDomain` is created per `PodCliqueSet`'s replica and all GPU-requesting pods in that replica are connected through it. 

While convenient, this all-or-nothing model has significant limitations — it does not allow per-PodClique control, **does not support [heterogeneous clusters](#homogeneous-vs-heterogeneous-clusters)**, and requires the feature to be explicitly enabled by the cluster administrator.

This GREP introduces **selective MNNVL**: a default-on, annotation-based mechanism that lets users control MNNVL participation at the **PodClique level**. By adding a vendor-scoped annotation (`grove.io/selective-mnnvl`) to individual PodCliques, users can specify exactly which pods share a `ComputeDomain`, without writing verbose `ComputeDomain` resources and without requiring cluster-wide auto-MNNVL enablement.

## Motivation

The current auto-MNNVL feature requires explicit cluster-level opt-in and places **all** GPU pods of a PodCliqueSet replica into a single `ComputeDomain`. This creates several pain points:

- **Heterogeneous clusters are not supported:** If a pod references a ComputeDomain's ResourceClaimTemplate (RCT) and is scheduled on a node without the NVIDIA DRA driver, the pod will fail to start. In clusters with mixed hardware from multiple vendors, enabling auto-MNNVL cluster-wide is impractical.
- **All pods in a replica share one ComputeDomain:** regardless of whether they actually need to communicate with each other over NVLink. This may lead to unpredictable and undesired side effects — such as unnecessary scheduling constraints and wasted IMEX channels — that can be avoided when only the pods that need NVLink are enrolled.
- **When auto-MNNVL is not enabled:** there is significant overhead for users to manually set up MNNVL for a `PodCliqueSet`. They must author and manage `ComputeDomain` resources and wire up the references in the pod spec themselves.

Selective MNNVL addresses these limitations by shifting the unit of MNNVL control from the entire PCS replica down to the individual PodClique, giving users an explicit, low-friction, and composable way to declare MNNVL intent.

### Goals

- Provide a **PodClique-level annotation** (`grove.io/selective-mnnvl`) that enables MNNVL participation on a per-PodClique basis.
- **Enable the feature by default** — users can annotate PodCliques without the cluster administrator needing to flip a global switch.
- Have the operator **automatically manage ComputeDomain lifecycle** (create, update, delete, scale) per PCS replica based on the annotations it discovers.
- Allow **selective MNNVL participation** within a PCS replica — only annotated PodCliques join a ComputeDomain; others are unaffected.
- **Support heterogeneous clusters** — only pods in PodCliques that request MNNVL need to be scheduled on NVIDIA DRA-capable nodes; the rest are unconstrained.
- Define **clear precedence and error semantics** when selective MNNVL annotations coexist with the cluster-wide auto-MNNVL feature.
- Ensure the design is **extensible to other accelerator vendors** in the future.

### Non-Goals

- This GREP does **not** introduce support for user-provided custom `ComputeDomain` specs with arbitrary attributes — the annotation triggers operator-managed domains only.
- This GREP does **not** define scheduling or topology placement policy (e.g., which specific nodes or racks pods should land on).

## Proposal

Instead of grouping all pods of a PCS replica under a single ComputeDomain (as auto-MNNVL does today), each PodClique can be individually marked with the `grove.io/selective-mnnvl` annotation:

```yaml
spec:
  template:
    cliques:
      - name: decoder
        annotations:
          grove.io/selective-mnnvl: "decoder-domain"
        spec:
          ...
```

The operator discovers these annotations, determines the set of ComputeDomains required(per replica), 
- manages their full lifecycle — creation, deletion, scaling and guarding.
- RCT injection to pods, only for the the annotated PodCliques.

A PCS that uses selective MNNVL annotations **will not** have auto-MNNVL applied to it, even if auto-MNNVL is enabled at the cluster level. The presence of `grove.io/selective-mnnvl` annotations on any PodClique within a PCS acts as an explicit signal that the user is taking control of ComputeDomain assignment. The two mechanisms are mutually exclusive per PCS — selective MNNVL annotations take precedence and auto-MNNVL is suppressed for that workload. See [Interaction with Auto-MNNVL](#interaction-with-auto-mnnvl) for the full precedence rules.

### User Stories

#### Story 1: Selective MNNVL in a Heterogeneous Cluster

As a platform engineer running a heterogeneous cluster with both MNNVL-supported and non-MNNVL-supported nodes, I cannot enable auto-MNNVL cluster-wide because the NVIDIA IMEXfd driver is only installed on a subset of nodes. I want to annotate specific PodCliques in my PodCliqueSet with `grove.io/selective-mnnvl` so that only those pods participate in an MNNVL domain, while other PodCliques (running on non-MNNVL-supported hardware) are unaffected.

#### Story 2: Partial MNNVL Within a PodCliqueSet Replica

As a data scientist running a multi-component training workload, my PCS replica consists of several PodCliques — workers, parameter servers, and data loaders. Only the workers need NVLink connectivity. I want to annotate only the worker PodClique with a `ComputeDomain` name, so that the parameter servers and data loaders do not get unnecessarily tied to MNNVL scheduling constraints.

#### Story 3: Simple MNNVL Without Cluster-Wide Auto-MNNVL

As a user on a cluster where the administrator has not enabled auto-MNNVL, I want a simple way to get MNNVL working for my workload. By adding a single annotation to my PodClique, the operator should create and manage the `ComputeDomain` for me — no verbose CR authoring required.

### Limitations/Risks & Mitigations

#### Limitation: Node scheduling constraints for annotated PodCliques
In a Heterogeneous clusters, pods in PodCliques that carry the `grove.io/selective-mnnvl` annotation must be scheduled on nodes with NVIDIA IMEX driver support. If these pods land on unsupported nodes, they will fail to start.

**Mitigation:** Users must ensure that annotated PodCliques include appropriate node scheduling constraints — either via node selector, node affinity, or other scheduling techniques — to target NVIDIA IMEX-capable nodes. The operator should surface a clear event or condition when a pod fails due to missing IMEX support.

## Design Details

### Annotation Semantics

A PodClique opts into MNNVL by carrying the following annotation:

```yaml
grove.io/selective-mnnvl: <name>
```

Where `<name>` is a user-chosen identifier for the ComputeDomain. Multiple PodCliques within the same PCS replica may reference the **same** name to share a single ComputeDomain, or use **different** names to participate in separate domains.

**Value validation:** The annotation value becomes part of the ComputeDomain resource name (`{value}-{replica-index}`), so it must be a valid Kubernetes resource name component. The validating webhook must reject any PCS where the annotation value:
- Is empty
- Contains characters not allowed in Kubernetes resource names (must match `[a-z0-9]([-a-z0-9]*[a-z0-9])?`)
- Would produce a ComputeDomain name exceeding 253 characters (the Kubernetes name limit)

**Immutability:** The `grove.io/selective-mnnvl` annotation is **immutable** after PCS creation. The validating webhook must reject any update that attempts to add, modify, or remove the annotation on an existing PCS. This is consistent with how the `grove.io/auto-mnnvl` annotation is handled, and ensures that the ComputeDomain assignment for a running workload cannot be changed — preventing unexpected disruption to active MNNVL domains.

**Example PodCliqueSet snippet:**

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: training-job
spec:
  replicas: 2
  template:
    cliques:
      - name: workers
        annotations:
          grove.io/selective-mnnvl: "training-domain"
        spec:
          replicas: 1
          podSpec:
            containers:
              - name: worker
                resources:
                  limits:
                    nvidia.com/gpu: 8
      - name: param-servers
        spec:
          replicas: 1
          podSpec:
            containers:
              - name: ps
                resources:
                  limits:
                    cpu: "4"
```

In this example, the `workers` PodClique participates in the `training-domain` ComputeDomain, while the `param-servers` PodClique does not participate in any ComputeDomain.

### Operator Behavior

The operator creates **one ComputeDomain per unique annotation value per PCS replica**. For example, if two PodCliques in a PCS both carry `grove.io/selective-mnnvl: "domain-a"`, a single ComputeDomain is created for `domain-a` in each replica(e.g. `domain-a-1`, `domain-a-2`, `domain-a-3`)

When the operator reconciles a PodCliqueSet, it performs the following for each replica:

1. **Discovery:** Scan all PodClique definitions for the `grove.io/selective-mnnvl` annotation. Collect the set of distinct ComputeDomain names referenced across the replica.

2. **ComputeDomain lifecycle management:**
   - **Create:** For each unique ComputeDomain name discovered, create a `ComputeDomain` resource (if it does not already exist) scoped to the replica. The ComputeDomain is named `{name}-{replica-index}` (e.g., `domain-a-0`, `domain-a-1`).
   - **Scale:** When replicas scale up, create new `ComputeDomain` instances for the new replicas. When replicas scale down, clean up the corresponding instances.
   - **Delete:** When a PCS replica is deleted or scaled down, delete the associated `ComputeDomain` resources.
   - **Finalizer:** Add a finalizer (`grove.io/computedomain-finalizer`) to each managed `ComputeDomain` to prevent accidental deletion while workloads are using it.

3. **RCT reference injection:** The `resourceClaims` reference is injected into the PCLQ's pod spec template at **PCLQ creation time** — not at pod creation time. This early-binding approach ensures the decision is made once and baked into the PCLQ spec, consistent with how auto-MNNVL works. The injection flow follows the same pattern as auto-MNNVL:
   - **PCS → PCLQ:** When the PCS controller creates a PCLQ for an annotated PodClique, it injects the `resourceClaims` reference into the PCLQ's pod spec template.
   - **PCS → PCSG:** The PCS controller propagates the `grove.io/selective-mnnvl` annotation to the PCSG.
   - **PCSG → PCLQ:** When the PCSG controller creates a PCLQ, it uses the same injection logic — check the annotation, inject the RCT reference.
   - **Pod creation:** The Pod controller requires no special logic. It creates pods using the PCLQ's pod spec template as-is.

4. **Non-annotated PodCliques:** Pods from PodCliques without the annotation are left untouched — no ComputeDomain reference is injected.

#### Reconciliation Ordering

ComputeDomain resources must be synced **before** creating PCLQs and PCSGs, ensuring the CD exists before pods that reference its RCT are created. If CD creation fails (due to transient cluster issues), the sync stops and requeues for retry — PCLQ and PCSG creation only proceeds after all CDs for the replica have been successfully created. This follows the same reconciliation ordering as auto-MNNVL.

#### ComputeDomain Resource Structure

The created ComputeDomain follows the same labeling pattern as auto-MNNVL:

```yaml
apiVersion: resource.nvidia.com/v1beta1
kind: ComputeDomain
metadata:
  name: domain-a-0
  labels:
    app.kubernetes.io/managed-by: grove-operator
    app.kubernetes.io/part-of: <pcs-name>
    app.kubernetes.io/name: domain-a-0
    app.kubernetes.io/component: pcs-computedomain
    grove.io/podcliqueset-replica-index: "0"
  finalizers:
    - grove.io/computedomain-finalizer
  ownerReferences:
    - apiVersion: grove.io/v1alpha1
      kind: PodCliqueSet
      name: <pcs-name>
      controller: true
spec:
  channel:
    resourceClaimTemplateName: domain-a-0
```

### Deployment and Configuration

Selective MNNVL is controlled via a mode field in `OperatorConfiguration`. The valid values are:

| Mode | Default | Behavior |
|---|---|---|
| `auto` | Yes | Feature is active **if** the `ComputeDomain` CRD is present in the cluster. If the CRD is absent, the operator starts normally but rejects any PCS with `grove.io/selective-mnnvl` annotations at admission time. |
| `enabled` | No | Feature is always active. The operator validates that the `ComputeDomain` CRD is installed at startup and **fails to start** if it is missing. |
| `disabled` | No | Feature is off. Any PCS with `grove.io/selective-mnnvl` annotations is rejected by the validating webhook. |

- **Independent of auto-MNNVL:** Selective MNNVL does not require auto-MNNVL to be enabled. The two features are orthogonal, with conflict rules defined in the following section.

```yaml
# OperatorConfiguration example
apiVersion: grove.io/v1alpha1
kind: OperatorConfiguration
spec:
  selectiveMNNVL:
    mode: auto  # default; valid values: auto, enabled, disabled
```

#### Behavior Per Mode

**`auto` (default):**
At operator startup, check whether the `ComputeDomain` CRD is present. If it is, selective MNNVL is fully active. If it is not, the operator starts normally but the validating webhook rejects any PCS that carries `grove.io/selective-mnnvl` annotations, with a clear error explaining that the cluster does not support ComputeDomains.

**`enabled`:**
The operator requires the `ComputeDomain` CRD at startup — if the CRD is missing, the operator fails to start with a clear error. This mode is for cluster administrators who want to guarantee that MNNVL is available and catch misconfigurations early.

**`disabled`:**
The feature is completely off. If a user submits a PCS with `grove.io/selective-mnnvl` annotations on any PodClique, the validating webhook rejects it with a clear error. This is consistent with how auto-MNNVL handles the same situation — explicit rejection avoids silent performance degradation.

#### Impact on Existing Workloads When Mode Changes

Changing the `selectiveMNNVL.mode` in `OperatorConfiguration` must **not** affect currently running workloads. Specifically:

- Switching from `auto` or `enabled` to `disabled` does **not** delete existing ComputeDomains or modify existing PCS resources. Workloads that were created with selective MNNVL continue to operate with their CDs intact.
- New PCS submissions with `grove.io/selective-mnnvl` annotations will be rejected after the mode change.
- To remove MNNVL from an existing workload, the PCS must be deleted and recreated.

This is consistent with the auto-MNNVL design principle: *"Enabling/Disabling of MNNVL feature should not impact a currently running workload."*

### Interaction with Auto-MNNVL

The selective MNNVL feature and the existing auto-MNNVL feature have overlapping scope. Their interaction is defined at two levels:

#### Cluster-Level: Feature Coexistence

Auto-MNNVL can be enabled or disabled at the cluster level via `OperatorConfiguration`. This is independent of selective MNNVL — both features can be active on the same cluster:

| Scenario | Auto-MNNVL Enabled (cluster) | `grove.io/selective-mnnvl` Annotation Present | Behavior |
|---|---|---|---|
| A | No | No | No MNNVL. Standard behavior. |
| B | No | Yes | Selective MNNVL is honored. Operator creates and manages per-annotation ComputeDomains. |
| C | Yes | No | Auto-MNNVL applies. Single ComputeDomain per replica (existing behavior). |
| D | Yes | Yes | **Selective MNNVL takes precedence.** Auto-MNNVL is suppressed for this PCS. The operator manages ComputeDomains based on the annotations only. |

**Scenario D rationale:** The presence of `grove.io/selective-mnnvl` annotations is an explicit signal that the user wants explicit control over ComputeDomain assignment. Applying auto-MNNVL on top would create ambiguous ownership and unpredictable behavior. Selective MNNVL annotations act as an implicit opt-out of auto-MNNVL for that PCS.

**Scenario D — mutating webhook behavior:** When auto-MNNVL is enabled at the cluster level, the mutating webhook normally adds `grove.io/auto-mnnvl: "enabled"` to new PCS resources that contain GPU-requesting pods. To support Scenario D, the mutating webhook must **skip** adding the `grove.io/auto-mnnvl` annotation if any PodClique in the PCS carries a `grove.io/selective-mnnvl` annotation. This prevents the PCS-level annotation mutual exclusion rule from triggering a rejection, and allows selective MNNVL to take precedence cleanly.

#### PCS-Level: Annotation Mutual Exclusion

While both features can coexist at the cluster level, the two **annotations** cannot coexist on the same PCS. A PCS **must not** carry both `grove.io/auto-mnnvl` and `grove.io/selective-mnnvl` annotations simultaneously. If both are present, the validating webhook must **reject** the PCS with a clear error explaining that the two MNNVL mechanisms are mutually exclusive per PCS.

The user must choose one approach:
- Rely on auto-MNNVL → remove the `grove.io/selective-mnnvl` annotations.
- Use selective MNNVL → set `grove.io/auto-mnnvl: "disabled"` or remove the auto-MNNVL annotation.
  
### Monitoring

ComputeDomain observability follows the same pattern as auto-MNNVL: **Kubernetes Events** on the PodCliqueSet resource. The operator should emit events when:

- A ComputeDomain is created or deleted as a result of annotation processing.
- Auto-MNNVL is suppressed for a PCS because selective MNNVL annotations take precedence (Scenario D).
- A PCS is rejected because both `grove.io/auto-mnnvl: "enabled"` and `grove.io/selective-mnnvl` annotations are present on the same PCS.
- ComputeDomain creation fails (the sync stops and requeues for retry).

### Dependencies (*Optional*)

- NVIDIA IMEX drivers must be installed on nodes where annotated PodClique pods are scheduled.
- The `ComputeDomain` CRD must be available in the cluster.

### Test Plan

- **Unit tests:** Cover annotation discovery, ComputeDomain lifecycle management (create/delete/scale), RCT injection logic, conflict detection with auto-MNNVL, and configuration parsing.
- **E2E tests:**
  - PCS with annotated PodCliques → ComputeDomains created and RCT injected correctly.
  - PCS with mix of annotated and non-annotated PodCliques → only annotated pods get ComputeDomain references.
  - PCS scale-up/scale-down → ComputeDomains created/deleted accordingly.
  - Auto-MNNVL enabled at cluster level + selective MNNVL annotations present → selective MNNVL takes precedence.
  - Both `grove.io/auto-mnnvl: "enabled"` and `grove.io/selective-mnnvl` annotations on same PCS → PCS rejected by validating webhook.
  - Mode `disabled` + annotations present → PCS rejected with clear error.
  - Mode `auto` + CRD absent + annotations present → PCS rejected with clear error.
  - Mode `enabled` + CRD absent → operator fails to start.

### Graduation Criteria

#### Alpha
- Annotation-based ComputeDomain injection implemented and functional.
- Operator manages ComputeDomain lifecycle (create, delete, scale) per replica.
- Conflict detection with auto-MNNVL in place.
- Three-mode configuration (`auto` / `enabled` / `disabled`) implemented in OperatorConfiguration.
- Unit and basic integration tests passing.

#### Beta
- E2E tests covering all key scenarios.
- Documentation for users on how to use the annotation.
- Events fully implemented.

#### GA
- Production deployments validated.
- API and annotation semantics stable.
- Comprehensive test coverage.

## Open Questions

The following items require discussion with the broader team before finalizing the design:

### 1. Annotation vs. label for ComputeDomain declaration

This GREP proposes using an **annotation** (`grove.io/selective-mnnvl`) on the PodClique. Some team members have suggested using a **label** instead.

**Arguments for annotation (current proposal):**
- This is a configuration directive that triggers operator behavior (CD creation, RCT injection) — the standard Kubernetes use case for annotations.
- Labels are for identity and selection; no one needs to select PodCliques by ComputeDomain name via label selectors.
- Consistent with how auto-MNNVL uses an annotation (`grove.io/auto-mnnvl`).

**Arguments for label:**
- Labels propagate to pods and are queryable (`kubectl get pods -l grove.io/selective-mnnvl=my-domain`), which aids observability and debugging.
- However, the operator could add a label to pods as a secondary step even if the declaration is via annotation.

### 2. Multi-level annotation support (PCS / PCSG / PodClique)

Should the `grove.io/selective-mnnvl` annotation be supported only at the PodClique level, or also at the PCS and PCSG levels with downward propagation?

**PodClique-level only (current proposal):**
- Simple, explicit, no ambiguity about which PodCliques participate.

**Multi-level with propagation:**
- An annotation on the PCS would apply to all PodCliques (unless overridden). An annotation on a PCSG would apply to all PodCliques within that scaling group.
- Reduces boilerplate when most or all PodCliques need the same ComputeDomain.
- **Must not override:** If a lower level (e.g., PodClique) already has the annotation, a higher-level annotation must not override it.

**Concerns with multi-level:**
- **Implicit behavior:** A PodClique might participate in MNNVL without any visible annotation on it, because the annotation is inherited from a parent. This makes the PCS harder to reason about — reviewers must check multiple levels to understand MNNVL participation.
- **Opt-out complexity:** If the PCS-level annotation enrolls all PodCliques, how does a single PodClique opt out? An empty string? A special sentinel value like `"none"`? This adds API surface and edge cases.
- **Interaction with auto-MNNVL:** If the PCS-level annotation behaves like a blanket "all PodCliques get MNNVL," it starts to resemble auto-MNNVL semantics, blurring the distinction between the two features.
- **Validation complexity:** The webhook must validate consistency across levels — e.g., a PCS-level annotation of `"domain-a"` and a PodClique-level annotation of `"domain-b"` are both valid (different domains), but what about a PCSG overriding a PCS-level value? The rules become harder to specify and test.

### 3. Two-value vs. three-value configuration mode

The current proposal uses three modes for `selectiveMNNVL.mode`: `disabled`, `enabled`, and `auto`. An alternative is to use only two values: `disabled` and `enabledWhenSupported` (equivalent to `auto`), removing the strict `enabled` mode that fails operator startup when the CRD is missing. The upside is simplicity — two values are easier to document and reason about. The downside is that cluster administrators who want to guarantee MNNVL availability lose the ability to catch CRD misconfigurations at operator startup; they would only discover the problem at PCS admission time.

### 4. Reject vs. silently ignore annotations when the feature is disabled

When the feature is disabled and a user submits a PCS with `grove.io/selective-mnnvl` annotations, the current proposal rejects the PCS. An alternative is to silently ignore the annotations and let the PCS run without MNNVL. The benefit is **portability** — the same PCS manifest could be deployed across clusters regardless of whether the feature is enabled. The risk is **silent performance degradation** — the user explicitly requested MNNVL but the workload runs without it, which is difficult to detect and debug. The current proposal favors explicit rejection, consistent with how auto-MNNVL handles the same scenario.

## Alternatives

**Alternative 1: Require users to author full `ComputeDomain` resources manually.**
This is the current fallback when auto-MNNVL is not enabled. It works but imposes significant authoring overhead and is error-prone. The annotation approach provides the same outcome with a single line of configuration.

**Alternative 2: Extend auto-MNNVL with per-PodClique opt-in/opt-out flags.**
Instead of a new annotation, auto-MNNVL could be extended with fields on each PodClique to control participation. This was considered but rejected because it couples selective control to the auto-MNNVL feature, which must be enabled cluster-wide — defeating the goal of working without cluster-level opt-in.

## Appendix (*Optional*)

### Terminology

| Term | Definition |
|---|---|
| **MNNVL** | Multi-Node NVLink — NVIDIA's technology for extending NVLink connectivity across multiple nodes. |
| **ComputeDomain** | An NVIDIA CRD that defines a set of pods sharing a high-bandwidth NVLink domain. |
| **RCT** | ResourceClaimTemplate — a Kubernetes DRA resource used to claim hardware resources (e.g., GPUs) for a pod. |
| **DRA** | Dynamic Resource Allocation — a Kubernetes API for managing hardware resources. |
| **Auto-MNNVL** | The existing Grove feature that automatically creates a single ComputeDomain per PCS replica when enabled cluster-wide. |
| **Selective MNNVL** | The feature proposed in this GREP — annotation-based, per-PodClique ComputeDomain injection. Formerly referred to as "fine-grained MNNVL." |
| **PCS** | PodCliqueSet — the Grove workload abstraction that groups PodCliques into replicas. |


### Homogeneous vs. Heterogeneous Clusters

For the purpose of this document, the distinction between homogeneous and heterogeneous clusters is based on NVIDIA IMEX driver support:

- **Homogeneous cluster:** All nodes in the cluster support the NVIDIA IMEX driver. Auto-MNNVL can be safely enabled cluster-wide.
- **Heterogeneous cluster:** Some nodes support the NVIDIA IMEX driver and some do not. Auto-MNNVL cannot be safely enabled cluster-wide in this configuration, since pods referencing a ComputeDomain's RCT may be scheduled on nodes without IMEX support and fail to start. Selective MNNVL is designed to address this scenario by allowing MNNVL participation to be scoped to specific PodCliques.

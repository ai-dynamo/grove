## GREP-270: Automatic MNNVL Support in Grove

**GREP owner**: TBD  
**Status**: Draft  
**Tracking issue**: [Add automatic support for MNNVL](https://github.com/ai-dynamo/grove/issues/270)

### Problem statement

Grove today does not provide first-class support for NVIDIA Multi-Node NVLink (MNNVL). As a result, workloads running on systems like the NVIDIA GB200 NVL72 cannot automatically form secure, high-bandwidth GPU fabrics across nodes using NVIDIA ComputeDomains and Dynamic Resource Allocation (DRA).

We need a way for a `PodCliqueSet` (PCS) workload to:

- Express that it **requires a ComputeDomain** (i.e. wants to run with MNNVL).
- Have Grove take care of:
  - Creating and wiring **ComputeDomains**.
  - Creating and wiring **ResourceClaimTemplates** (RCTs).
  - Injecting **pod-level resource claims** and **container-level resource claim usage**.
  - Cleaning up these resources when the workload terminates.

### How ComputeDomains work

A **ComputeDomain** is a NVIDIA abstraction that enables workloads to use MNNVL (Multi-Node NVLink) across one or more physical racks. Key aspects:

- **One ComputeDomain per PCS replica**, regardless of how many MNNVL racks the pods are distributed across.
- The ComputeDomain controller **automatically orchestrates IMEX domains** (one per physical rack).
  - Pods on the same rack communicate via fast MNNVL/NVLink.
  - Pods on different racks fall back to the fastest available inter-rack fabric (slower than NVLink).
- **Auto-scaling support**: New pods joining a workload can automatically join existing IMEX domains on their respective racks (requires DRA driver 25.8.0+ and GPU driver 570.150.x+).

**Bottom line**: One ComputeDomain per PCS replica, automatic multi-rack orchestration, with fast MNNVL communication within racks but not across them.

### Goals and non-goals

- **Goals**
  - Allow a PCS to declare MNNVL usage declaratively in its spec.
  - Support **per-replica ComputeDomains** so each PCS replica has its own isolated GPU fabric.
  - Automatically manage the lifecycle of ComputeDomains and RCTs.
  - Ensure pods are correctly wired via Kubernetes `resourceClaims` and container `resources.claims`.

### User-facing API changes

Add a `computeDomainConfig` section to `PodCliqueSet.spec.template`:

- **`enabled`**: bool – turn MNNVL on/off for the PCS.
- **`deviceClassName`**: string – device class for the RCT, default `compute-domain.nvidia.com`.

In this design:
- ComputeDomains are always **per-replica** (one ComputeDomain per PCS replica).
- Users **do not define per-replica ComputeDomains explicitly** (names or instances); Grove derives and manages those resources from the PCS spec and replica indices.
- The PCS API only captures **high-level intent** (enable MNNVL).

#### Manual / non-automatic usage limitations

If `computeDomainConfig.enabled` is **disabled**, users can still hand-craft `resourceClaims` and `resources.claims` in the PCS/PodClique templates, but only in a **replica-agnostic** way:

- The PCS and PodClique specs do not carry the PCS replica index.
- Therefore, users **cannot express true per-replica ComputeDomains manually** (for example, "replica-0 uses CD-0, replica-1 uses CD-1") just by editing the PCS.
- Per-replica ComputeDomains are only available through Grove's automatic wiring, which has access to the replica index at reconciliation time.

### Design: where to inject ResourceClaims

Given that ComputeDomains are **per-replica** and Grove's current webhook architecture (PCS has webhooks, but PodClique does not), the injection must happen in the **PCS controller's PodClique component**.

#### Why PCS-level injection is NOT viable

The PCS template is **replica-agnostic**:
- The PCS defaulting webhook runs once when the PCS is created/updated and operates on `PodCliqueSet.spec.template`.
- The template is shared across **all replicas**.
- Kubernetes PodSpec has no templating/variable system to express "use `<pcs-name>-{replicaIndex}-mnnvl-claim`".
- If the webhook injects a ResourceClaimTemplate name, all replicas would reference the **same** RCT, defeating the per-replica isolation goal.

Therefore, **per-replica ResourceClaim injection cannot happen at the PCS admission webhook level**.

#### Recommended approach: PodClique controller injection

**How it works:**
1. **PCS admission webhook**:
   - Validates `computeDomainConfig` fields (e.g., GPU requests present, no conflicting user-defined ResourceClaims).
   - Defaults `deviceClassName` to `compute-domain.nvidia.com`.
   - **Does NOT inject ResourceClaims** (cannot solve per-replica problem).

2. **PCS controller's PodClique component** (`buildResource` function in `internal/controller/podcliqueset/components/podclique/podclique.go`):
   - When building each PodClique for a `(PCS replica index, clique)` pair:
     - Check if `pcs.spec.template.computeDomainConfig.enabled=true`.
     - If enabled:
       - Compute per-replica ComputeDomain and ResourceClaimTemplate names from `pcs.name + replicaIndex`.
       - Inject `resourceClaims` entry into `PodClique.spec.PodSpec` referencing the per-replica RCT.
       - Inject `resources.claims` entries into each container that should use the ComputeDomain (e.g., containers with GPU requests).

3. **ComputeDomain/RCT lifecycle controllers** (new PCS components):
   - Ensure one ComputeDomain and one ResourceClaimTemplate exist per PCS replica.
   - Delete them on scale-down or PCS deletion.

4. **Pod creation**:
   - The PodClique controller's Pod component simply copies `PodClique.spec.PodSpec` to `Pod.spec`.
   - Pods inherit the per-replica ResourceClaim wiring from their parent PodClique.

**Pros:**
- ✅ **Natural fit for per-replica semantics**: The PodClique controller already knows the replica index.
- ✅ **Clean mapping**: `PCS replica index → PodClique → ComputeDomain/RCT` is straightforward.
- ✅ **No placeholder/pattern resolution**: The correct per-replica RCT name is computed and injected directly.
- ✅ **No new webhooks needed**: Uses existing PCS controller infrastructure.
- ✅ **PodClique spec is the single source of truth**: Pods mirror their PodClique's PodSpec; no hidden mutations.
- ✅ **Consistent with existing patterns**: The PCS controller's PodClique component already injects labels, dependencies, and environment variables.

**Cons:**
- ❌ **Less transparent at PCS level**: MNNVL wiring is not visible in the PCS spec; users must inspect PodClique objects to see the actual ResourceClaim configuration.
- ❌ **Validation is partial at admission time**: The webhook can validate high-level intent, but cannot validate the final per-replica wiring until PodCliques are created.

**Trade-off rationale:**
- Per-replica semantics fundamentally cannot be expressed at the PCS template level (it's replica-agnostic by design).
- Users can still see that MNNVL is enabled at the PCS level (`computeDomainConfig.enabled=true`).
- PodClique objects are inspectable and show the actual per-replica wiring.
- This is consistent with how Grove already handles per-replica concerns (e.g., replica-specific labels, startup dependencies).

#### Alternative: Create a new PodClique defaulting webhook

Another viable approach would be to **create a new PodClique defaulting webhook** (which doesn't exist in Grove today) that injects ResourceClaims when a PodClique is created.

**How it would work:**
1. When a PodClique is created, the webhook intercepts it.
2. The webhook:
   - Fetches the parent PCS to check `computeDomainConfig.enabled`.
   - Extracts the replica index from PodClique labels (e.g., `grove.io/pcs-replica-index`).
   - Computes the per-replica ComputeDomain/RCT names.
   - Injects `resourceClaims` and container `resources.claims` into `PodClique.spec.PodSpec`.

**Pros:**
- ✅ Keeps injection in the admission layer (consistent with separation of concerns: webhooks mutate, controllers reconcile).
- ✅ PodClique spec shows final wiring before it reaches any controller.

**Cons:**
- ❌ **Requires new infrastructure**: Creating a new webhook requires chart updates, webhook configuration, and testing.
- ❌ **Extra API calls**: Webhook must fetch the parent PCS (adds latency, potential for race conditions during PCS create/update).
- ❌ **More complex error handling**: Webhook runs at admission time; if parent PCS is not found or misconfigured, the PodClique creation fails, which is harder to debug than controller errors.
- ❌ **Still not transparent at PCS level**: Like the PodClique controller approach, users must inspect PodClique objects to see MNNVL wiring.

**Recommendation:** Stick with **PodClique controller injection** because:
- It's simpler (no new webhook infrastructure).
- The PodClique controller already has the parent PCS object in hand; no need to fetch it.
- Controller errors are easier to observe and debug (via events, status, logs) than webhook errors.
- Grove today has no PodClique webhooks, so adding one would be a significant architectural change just for this feature.

### ComputeDomain & ResourceClaimTemplate lifecycle

- Grove, given a PCS with `computeDomainConfig.enabled=true`, ensures one ComputeDomain and one ResourceClaimTemplate per PCS replica.
- For each PCS replica index:
  - Create a **ComputeDomain** with:
    - Name derived from PCS name + replica index (e.g. `<pcs-name>-<replica-index>-cd`).
    - `numNodes: 0` (elastic).
  - Create a **ResourceClaimTemplate** with:
    - Name derived from PCS name + replica index (e.g. `<pcs-name>-<replica-index>-mnnvl-claim`).
    - CEL selector matching the ComputeDomain name.
- These resources are owned by the PCS and are deleted when:
  - A replica is scaled down, or
  - The PCS is deleted.

### Implementation summary

The MNNVL feature will be implemented across three layers:

1. **PCS API** (`operator/api/core/v1alpha1/podcliqueset.go`):
   - Add `computeDomainConfig` struct to `PodCliqueSetTemplateSpec`.
   - Fields: `enabled` (bool), `deviceClassName` (string).

2. **PCS admission webhook** (`operator/internal/webhook/admission/pcs/`):
   - **Defaulting**: Set `deviceClassName` to `compute-domain.nvidia.com` when `enabled=true`.
   - **Validation**: Ensure GPU requests are present in containers, no conflicting user-defined ResourceClaims with the internal claim name (`mnnvl`).

3. **PCS controller's PodClique component** (`operator/internal/controller/podcliqueset/components/podclique/podclique.go`):
   - In `buildResource`: Check if `computeDomainConfig.enabled=true`.
   - Compute per-replica CD/RCT names: `<pcs-name>-<replica-index>-cd` and `<pcs-name>-<replica-index>-mnnvl-claim`.
   - Inject `PodClique.spec.PodSpec.resourceClaims` and container `resources.claims`.

4. **New PCS components** (in `operator/internal/controller/podcliqueset/components/`):
   - **ComputeDomain operator**: Create/update/delete ComputeDomain objects per PCS replica.
   - **ResourceClaimTemplate operator**: Create/update/delete RCT objects per PCS replica with CEL selectors.
   - Register these in the PCS controller's operator registry.

5. **RBAC updates** (`operator/charts/templates/clusterrole.yaml`):
   - Add permissions for `resource.nvidia.com/v1alpha1/computedomains` (CRUD).
   - Add permissions for `resource.k8s.io/v1beta1/resourceclaimtemplates` (CRUD).

### Open questions for discussion

- What is the desired failure behavior if the NVIDIA stack (ComputeDomain CRD/DRA) is not installed or misconfigured?
- Should we add a status field to PCS to surface per-replica ComputeDomain readiness, or rely on users inspecting ComputeDomain objects directly?



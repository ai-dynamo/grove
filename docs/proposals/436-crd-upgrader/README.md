# GREP-436: Automated CRD Upgrade Strategy for Grove

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
    - [Story 3](#story-3)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
    - [Risk: CRD Deletion on Helm Uninstall](#risk-crd-deletion-on-helm-uninstall)
    - [Risk: RBAC for CRD Management](#risk-rbac-for-crd-management)
    - [Risk: Init Container Failure Visibility](#risk-init-container-failure-visibility)
    - [Risk: Schema Incompatibility During Upgrade](#risk-schema-incompatibility-during-upgrade)
- [Design Details](#design-details)
  - [Init Container Using the Operator Image](#init-container-using-the-operator-image)
    - [Embedding CRD YAML in the Operator Binary](#embedding-crd-yaml-in-the-operator-binary)
    - [install-crds Subcommand](#install-crds-subcommand)
    - [Opting Out of Automated CRD Management](#opting-out-of-automated-crd-management)
    - [Init Container Spec](#init-container-spec)
    - [Changes to the grove Chart](#changes-to-the-grove-chart)
    - [GitOps Integration](#gitops-integration)
  - [Considered Alternatives](#considered-alternatives)
    - [Helm Pre-Upgrade Hook Job](#helm-pre-upgrade-hook-job)
    - [Hash-Based Skip Optimization](#hash-based-skip-optimization)
  - [Monitoring](#monitoring)
  - [Dependencies](#dependencies)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Appendix](#appendix)
<!-- /toc -->

## Summary

Grove currently places its CRDs (`PodCliqueSet`, `PodClique`, `PodCliqueScalingGroup`, `PodGang`, `ClusterTopology`) under the Helm `crds/` directory. Helm 3 intentionally installs CRDs on `helm install` but silently skips them on `helm upgrade`, meaning users upgrading the Grove operator may continue running stale CRD schemas. This GREP proposes adding an init container to the grove operator Deployment that automatically applies all CRDs before the operator process starts. The init container uses the grove operator image itself — which embeds the CRD YAML files via Go's `embed` package — so no external images, no separate Jobs, and no dependency on Helm hook ordering are introduced. A single deployment of the grove operator is sufficient to keep both CRDs and the operator in sync, regardless of the deployment toolchain.

## Motivation

Helm's `crds/` directory has intentional lifecycle limitations: CRDs placed there are **never upgraded** and **never deleted** by Helm. This means that today, any user who runs `helm upgrade grove` to move to a newer operator version receives a new binary but keeps the old CRD schemas. New CRD fields, validation rules, or structural changes introduced between releases go unnoticed until a production failure surfaces them.

For an operator such as Grove, where the API surface (`PodCliqueSet`, `PodClique`, etc.) is actively evolving, this creates a latent correctness risk: the running controller may attempt to read or write fields that do not yet exist in the stored schema, or that are now subject to new CEL validation rules. The outcome is subtle and hard to debug: admission webhooks may pass objects that newer schemas would reject, or status fields may be silently dropped.

### Goals

* Ensure CRDs are applied/upgraded before the operator process starts on every deployment and every restart.
* Require zero manual steps from users for CRD lifecycle management during normal upgrades.
* Work correctly for all deployment models: native Helm, `helm template` with kustomize/ArgoCD/Flux, and raw `kubectl apply`.
* Allow users to opt out of automated CRD management and take full ownership of CRD lifecycle themselves.
* Preserve existing CRD data — no CRD deletion must occur as part of normal upgrades.

### Non-Goals

* CRD version conversion webhooks or multi-version CRD support — this GREP only addresses delivery of CRD schemas, not API versioning strategy.
* Automatic migration of existing Custom Resources when breaking schema changes are introduced — that is a separate concern handled by conversion webhooks.
* Support for partially managed CRDs (i.e., users managing a subset of Grove CRDs externally) in the initial implementation.
* Changes to how CRDs are generated from Go API types — `controller-gen` and `make generate` remain the authoritative source.

## Proposal

Add an init container to the grove operator Deployment that runs the grove operator image with a dedicated `install-crds` subcommand. The init container reads CRD YAML files embedded directly in the binary (via Go's `//go:embed` directive) and applies them to the cluster using server-side apply before the main operator container starts. Kubernetes itself enforces that the init container completes successfully before the operator process runs, making CRD upgrade ordering a property of the Deployment object rather than a property of the deployment toolchain.

The `crds/` directory is removed from the `grove` chart. The init container becomes the sole owner of CRD lifecycle for both fresh installs and upgrades.

### User Stories

#### Story 1

As a platform engineer operating Grove in a production cluster, when I run `helm upgrade grove oci://ghcr.io/ai-dynamo/grove --version 0.4.0`, I expect the CRDs to be upgraded automatically to the schemas that the 0.4.0 operator requires, without needing to run any additional command or consult release notes for manual CRD steps.

#### Story 2

As a GitOps engineer managing Grove via ArgoCD or Flux using rendered manifests from `helm template`, I want CRD upgrades to happen automatically when the operator Deployment rolls out, without needing to manage a separate CRD application, sync-wave ordering, or Helm hook lifecycle.

#### Story 3

As a platform engineer with strict change-management requirements, I want to disable Grove's automated CRD management entirely. I will source the CRD manifests directly from the Grove GitHub release, review and approve them through our standard change process, and apply them to the cluster myself before upgrading the operator. I need the operator to start normally without attempting to touch CRDs.

### Limitations/Risks & Mitigations

#### Risk: CRD Deletion on Helm Uninstall

The init container applies CRDs directly to the cluster via server-side apply — they are not Helm-managed resources. Therefore `helm uninstall grove` will not delete the CRDs.

Users who intentionally want to remove all CRDs must do so explicitly with `kubectl delete crd`.

#### Risk: RBAC for CRD Management

The init container runs under the operator's existing `ServiceAccount` and requires `get`, `create`, `update`, and `patch` verbs on `customresourcedefinitions`. This slightly expands the RBAC footprint of the operator `ServiceAccount`.

**Mitigation:** The verbs are scoped to the minimum required and cover only the five specific CRD names managed by Grove. The operator already holds broad cluster-scoped RBAC to reconcile its own resources; CRD management is consistent with that footprint.

#### Risk: Init Container Failure Visibility

If the init container fails (e.g., due to a transient API server error or RBAC misconfiguration), the operator Pod will not start and Kubernetes will restart the init container according to the Pod's `restartPolicy`. The failure is visible in `kubectl describe pod` and `kubectl logs`.

**Mitigation:** The `install-crds` subcommand logs each CRD name and the outcome of its apply operation (`created`, `updated`, `unchanged`) at `INFO` level and any errors at `ERROR` level before exiting with a non-zero code. Log retrieval is straightforward: `kubectl logs -n grove-system <pod-name> -c crd-installer`. The Pod's restart behaviour (`restartPolicy: Always` on Deployments) means transient failures self-recover without operator intervention.

#### Risk: Schema Incompatibility During Upgrade

During a rolling upgrade, new operator Pods must complete the init container (CRD apply) before the operator process starts. Old Pods continue running with the previous CRD schema until they are replaced.

**Mitigation:** Server-side apply ensures the API server acknowledges the updated schemas before the init container exits. Because new Pods apply CRDs before starting, the schema is always at least as new as the operator version that reads it. The rolling update ensures at most one schema version transition is in progress at any time.

## Design Details

### Init Container Using the Operator Image

#### Embedding CRD YAML in the Operator Binary

CRD YAML files are embedded in the grove operator binary using Go's `//go:embed` directive. The embedding is wired into a new `internal/crds` package:

```go
// operator/internal/crds/embed.go
package crds

import "embed"

//go:embed files/*.yaml
var FS embed.FS
```

The `files/` directory is populated by `prepare-charts.sh` as part of the build, copying the generated CRD YAML from `operator/api/core/v1alpha1/crds/` and `scheduler/api/core/v1alpha1/crds/` — the same sources used today. This ensures the embedded CRDs are always in sync with the controller-gen output and require no separate maintenance step.

#### install-crds Subcommand

A new `install-crds` subcommand is added to the grove operator binary:

```go
// operator/cmd/install-crds/main.go
func run(ctx context.Context) error {
    cfg, err := rest.InClusterConfig()
    // ...
    entries, err := crds.FS.ReadDir("files")
    // ...
    for _, entry := range entries {
        data, _ := crds.FS.ReadFile("files/" + entry.Name())
        result, err := applyCRD(ctx, client, data)
        if err != nil {
            return fmt.Errorf("applying %s: %w", entry.Name(), err)
        }
        log.Info("applied CRD", "name", entry.Name(), "result", result) // result: created|updated|unchanged
    }
    return nil
}
```

The subcommand uses server-side apply (`--force-conflicts`) so that fields previously managed by other field managers (e.g., a cluster admin who manually patched a CRD) do not block the upgrade.

#### Opting Out of Automated CRD Management

Some users prefer to take full ownership of CRD lifecycle — sourcing CRD manifests directly from the Grove GitHub release, reviewing them through their change-management process, and applying them independently of the operator rollout. The automated init container can be disabled for these users via a `values.yaml` flag:

```yaml
# values.yaml
crdInstaller:
  enabled: true  # set to false to disable the init container and manage CRDs manually
```

When `crdInstaller.enabled` is `false`, the init container is omitted from the Deployment template entirely and the operator's CRD-related RBAC verbs are also removed. The operator starts without attempting to touch CRDs.

Users who opt out are responsible for ensuring the correct CRD schemas are applied before upgrading the operator. CRD manifests for each Grove release are published as part of the GitHub release artifacts and are also available in the `operator/api/core/v1alpha1/crds/` and `scheduler/api/core/v1alpha1/crds/` directories of the release tag.

> **Warning:** Running the operator against stale CRD schemas may result in silent data loss, rejected objects, or undefined reconciliation behaviour. Users who opt out must apply updated CRDs before or simultaneously with upgrading the operator.

#### Init Container Spec

The init container is added directly to the operator Deployment template and is conditional on `crdInstaller.enabled`. It uses the same image reference as the operator container, so no additional image needs to be built, pushed, or pulled:

```yaml
# templates/deployment.yaml (operator Deployment)
spec:
  template:
    spec:
      {{- if .Values.crdInstaller.enabled }}
      initContainers:
        - name: crd-installer
          image: "{{ .Values.operator.image.repository }}:{{ .Values.operator.image.tag }}"
          imagePullPolicy: {{ .Values.operator.image.pullPolicy }}
          command: ["/manager", "install-crds"]
      {{- end }}
      containers:
        - name: manager
          image: "{{ .Values.operator.image.repository }}:{{ .Values.operator.image.tag }}"
          # ... existing operator container spec
```

Kubernetes guarantees that `crd-installer` completes successfully before the `manager` container starts. This ordering is a property of the Pod spec itself and is honoured by every Kubernetes-compatible deployment tool.

#### Changes to the grove Chart

* `charts/crds/` directory is removed — the init container becomes the default CRD delivery mechanism.
* The init container spec is added to the existing operator Deployment template, guarded by `{{- if .Values.crdInstaller.enabled }}`.
* No new template files, Jobs, ServiceAccounts, or ClusterRoles are required.
* The operator `ClusterRole` gains `get`, `create`, `update`, `patch` on `customresourcedefinitions` for the five Grove CRD names, also guarded by `crdInstaller.enabled`.
* `values.yaml` gains a `crdInstaller.enabled` field defaulting to `true`.
* `prepare-charts.sh` is updated to copy CRD files into `operator/internal/crds/files/` in addition to (temporarily, during transition) `operator/charts/crds/`.

#### GitOps Integration

Because the ordering guarantee is provided by Kubernetes (init container semantics), no GitOps-specific configuration is required. The approach works identically across all deployment models:

* **Native Helm**: `helm upgrade grove` rolls out a new Deployment; the init container runs and applies CRDs before the operator starts.
* **`helm template` + GitOps**: The rendered Deployment manifest contains the init container spec. ArgoCD or Flux applies it; Kubernetes enforces the init container ordering regardless of how the manifest was delivered.
* **Raw `kubectl apply`**: Identical behaviour — the init container runs before the operator.

No sync-wave ordering, `dependsOn` between separate applications, or Helm hook support from the GitOps tool is needed.

### Considered Alternatives

#### Helm Pre-Upgrade Hook Job

An earlier version of this design used a Helm `pre-install`/`pre-upgrade` hook Job instead of an init container. The hook Job ran the same `install-crds` subcommand and achieved the same CRD ordering for native Helm users.

This approach was rejected because the ordering guarantee only holds when the Helm lifecycle layer is active. Customers who use `helm template` to render manifests and manage them in a git repository via kustomize, ArgoCD, or Flux bypass the Helm lifecycle entirely — `helm.sh/hook` annotations become dead metadata and the Job is applied alongside all other resources with no ordering. The init container approach moves the ordering guarantee into the Kubernetes object model, where it is enforced unconditionally by the kubelet regardless of deployment toolchain.

#### Hash-Based Skip Optimization

A possible optimisation is to compute a hash of each CRD's YAML content, store it as an annotation on the CRD object (e.g., `grove.io/crd-content-hash`), and skip the server-side apply call when the hash matches on startup.

This was considered and rejected. Server-side apply is already effectively a no-op when the desired state matches the current state — the API server performs an internal diff and returns `unchanged` without mutating the object. The optimisation would save one write API call per CRD per operator restart (five calls total), which is negligible. In exchange it introduces meaningful complexity: the init container must issue a GET before each apply, handle the case where the annotation is absent (first install, manual CRD edit, annotation stripped by a tool), and maintain the hash computation in sync with the embedded CRD content. The `apply-result: unchanged` log line already provides equivalent auditability. The optimisation is not worth the complexity.

### Monitoring

No new metrics or status conditions are introduced by this GREP. Observability of CRD schema state relies on existing Kubernetes mechanisms:

* `kubectl get crd <name> -o jsonpath='{.status.storedVersions}'` reports which API versions are currently served.
* The init container logs each CRD apply result (`created`, `updated`, `unchanged`) at `INFO` level, providing a clear audit trail per operator rollout.
* The grove operator's existing startup validation logs surface any schema mismatch at startup time.

### Dependencies

* Go `embed` package (standard library, no new dependencies).
* The grove operator image must include the `install-crds` subcommand entrypoint — no separate image is required.
* `prepare-charts.sh` must copy CRD files to `operator/internal/crds/files/` before the operator binary is built. This step is added to the existing `make generate` → `make build` flow.

### Test Plan

* **Unit:** The `install-crds` subcommand is tested with a fake Kubernetes client, verifying that all five CRDs are applied via server-side apply and that the command exits non-zero on any apply error.
* **Embed integrity:** A CI check verifies that the CRD files embedded in the binary match the canonical generated sources in `operator/api/` and `scheduler/api/`, preventing drift between the embedded CRDs and the controller-gen output.
* **E2E upgrade test:** A new E2E scenario is added (tracked in a sub-issue of #436) that:
  1. Installs `grove` at version N using `helm install`.
  2. Simulates a CRD schema change (e.g., adds a new optional field to `PodCliqueSet`) in version N+1.
  3. Runs `helm upgrade grove` to version N+1.
  4. Asserts that the operator Pod's init container completed successfully.
  5. Asserts that `kubectl get crd podcliquesets.grove.io -o json` reflects the new field.
  6. Verifies that existing `PodCliqueSet` CRs are unaffected.
* **Opt-out test:** CI verifies that installing with `--set crdInstaller.enabled=false` produces a Deployment with no init container and a ClusterRole without CRD verbs, and that the operator starts normally when CRDs are pre-applied manually.
* **Init container failure test:** CI verifies that a simulated apply failure (e.g., RBAC denied) causes the operator Pod to remain in `Init:Error` state, and that the init container logs contain the structured error message.

### Graduation Criteria

**Alpha (initial merge):**

* `install-crds` subcommand is implemented and CRD YAML is embedded in the operator binary.
* Init container is added to the operator Deployment template, conditional on `crdInstaller.enabled`.
* `crdInstaller.enabled` defaults to `true` in `values.yaml`.
* `crds/` directory is removed from the `grove` chart.
* A single `helm upgrade grove` correctly upgrades all five CRDs in a live cluster before the operator rolls out.
* E2E upgrade scenario passes in CI.
* Opt-out test passes in CI.
* Init container failure test passes in CI.

**GA:**

* At least two releases have shipped using the init container model without regressions.
* User-facing documentation confirms that no manual CRD steps are required on upgrade.

## Implementation History

* 2026-03-19: GREP created, tracking issue [#436](https://github.com/ai-dynamo/grove/issues/436).
* 2026-03-23: Design updated to use init container instead of Helm pre-upgrade hook Job, to support customers who manage rendered manifests via kustomize/ArgoCD/Flux.
* 2026-03-23: Added `crdInstaller.enabled` opt-out flag for customers who manage CRD lifecycle independently.

## Appendix

* Tracking issue: [#436 — Helm upgrade does not upgrade CRDs](https://github.com/ai-dynamo/grove/issues/436)
* Helm 3 CRD documentation: [Helm Docs — Custom Resource Definitions](https://helm.sh/docs/chart_best_practices/custom_resource_definitions/)
* Go embed package: [pkg.go.dev/embed](https://pkg.go.dev/embed)
* NIM operator CRD init container pattern: reference implementation used as prior art for this approach.

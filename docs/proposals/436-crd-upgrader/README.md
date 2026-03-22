# GREP-436: Automated CRD Upgrade Strategy for Grove Helm Chart

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
    - [Risk: CRD Deletion on Helm Uninstall](#risk-crd-deletion-on-helm-uninstall)
    - [Risk: RBAC for CRD Management](#risk-rbac-for-crd-management)
    - [Risk: Hook Job Failure Visibility](#risk-hook-job-failure-visibility)
    - [Risk: Schema Incompatibility During Upgrade](#risk-schema-incompatibility-during-upgrade)
- [Design Details](#design-details)
  - [Pre-Upgrade Hook Job Using the Operator Image](#pre-upgrade-hook-job-using-the-operator-image)
    - [Embedding CRD YAML in the Operator Binary](#embedding-crd-yaml-in-the-operator-binary)
    - [install-crds Subcommand](#install-crds-subcommand)
    - [Hook Job Manifest](#hook-job-manifest)
    - [ServiceAccount and ClusterRole](#serviceaccount-and-clusterrole)
    - [Hook Cleanup Policy](#hook-cleanup-policy)
    - [Changes to the grove Chart](#changes-to-the-grove-chart)
    - [GitOps Integration](#gitops-integration)
  - [Monitoring](#monitoring)
  - [Dependencies](#dependencies)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Appendix](#appendix)
<!-- /toc -->

## Summary

Grove currently places its CRDs (`PodCliqueSet`, `PodClique`, `PodCliqueScalingGroup`, `PodGang`, `ClusterTopology`) under the Helm `crds/` directory. Helm 3 intentionally installs CRDs on `helm install` but silently skips them on `helm upgrade`, meaning users upgrading the Grove operator may continue running stale CRD schemas. This GREP proposes adding a Helm `pre-install`/`pre-upgrade` hook Job to the `grove` chart that automatically applies all CRDs before the operator is deployed or updated. The hook uses the grove operator image itself — which embeds the CRD YAML files via Go's `embed` package — so no external images, no ConfigMaps, and no etcd size pressure are introduced. A single `helm upgrade grove` is sufficient to keep both CRDs and the operator in sync.

## Motivation

Helm's `crds/` directory has intentional lifecycle limitations: CRDs placed there are **never upgraded** and **never deleted** by Helm. This means that today, any user who runs `helm upgrade grove` to move to a newer operator version receives a new binary but keeps the old CRD schemas. New CRD fields, validation rules, or structural changes introduced between releases go unnoticed until a production failure surfaces them.

For an operator such as Grove, where the API surface (`PodCliqueSet`, `PodClique`, etc.) is actively evolving, this creates a latent correctness risk: the running controller may attempt to read or write fields that do not yet exist in the stored schema, or that are now subject to new CEL validation rules. The outcome is subtle and hard to debug: admission webhooks may pass objects that newer schemas would reject, or status fields may be silently dropped.

### Goals

* Ensure CRDs are applied/upgraded atomically with every `helm install` and `helm upgrade` of the Grove operator.
* Require zero manual steps from users for CRD lifecycle management during normal upgrades.
* Provide a clear, documented path for GitOps users (ArgoCD, Flux) to manage CRD upgrades via sync-waves or `dependsOn`.
* Preserve existing CRD data — no CRD deletion must occur as part of normal upgrades.

### Non-Goals

* CRD version conversion webhooks or multi-version CRD support — this GREP only addresses delivery of CRD schemas, not API versioning strategy.
* Automatic migration of existing Custom Resources when breaking schema changes are introduced — that is a separate concern handled by conversion webhooks.
* Support for partially managed CRDs (i.e., users managing a subset of Grove CRDs externally) in the initial implementation.
* Changes to how CRDs are generated from Go API types — `controller-gen` and `make generate` remain the authoritative source.

## Proposal

Add a Helm hook Job to the existing `grove` chart that fires on `pre-install` and `pre-upgrade`. The Job runs the grove operator image with a dedicated `install-crds` subcommand that reads CRD YAML files embedded directly in the binary (via Go's `//go:embed` directive) and applies them to the cluster using server-side apply. The operator image is already pulled as part of every `helm upgrade grove`, so no additional image is needed and the CRD version is always guaranteed to match the operator version.

The `crds/` directory is removed from the `grove` chart. The hook becomes the sole owner of CRD lifecycle for both fresh installs and upgrades.

### User Stories

#### Story 1

As a platform engineer operating Grove in a production cluster, when I run `helm upgrade grove oci://ghcr.io/ai-dynamo/grove --version 0.4.0`, I expect the CRDs to be upgraded automatically to the schemas that the 0.4.0 operator requires, without needing to run any additional command or consult release notes for manual CRD steps.

#### Story 2

As a GitOps engineer managing Grove via ArgoCD or Flux, I want CRD upgrades to happen automatically as part of the single `grove` application sync, without needing to manage a separate CRD application or enforce explicit sync-wave ordering.

### Limitations/Risks & Mitigations

#### Risk: CRD Deletion on Helm Uninstall

Hook resources annotated with `helm.sh/hook` are created and deleted by Helm as part of the hook lifecycle, but the CRDs themselves are applied directly to the cluster via `kubectl apply` inside the Job — they are not Helm-managed resources. Therefore `helm uninstall grove` will not delete the CRDs.

Users who intentionally want to remove all CRDs must do so explicitly with `kubectl delete crd`.

#### Risk: RBAC for CRD Management

The hook Job requires a `ServiceAccount` and `ClusterRole` with permissions on `apiextensions.k8s.io/customresourcedefinitions`. This slightly expands the RBAC surface of the `grove` chart.

**Mitigation:** The `ClusterRole` is scoped to the minimum required verbs (`get`, `create`, `update`, `patch`) and only covers the five specific CRD names managed by Grove. The `ServiceAccount` is used exclusively by the hook Job and is not reused by the operator Deployment. Both resources are annotated with `helm.sh/hook: pre-install,pre-upgrade` and `helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded` so they do not persist after the hook completes.

#### Risk: Hook Job Failure Visibility

A failed hook Job blocks the `helm upgrade` command with a generic hook failure error, requiring the user to inspect Job and Pod logs to determine the root cause.

**Mitigation:** The `install-crds` subcommand logs each CRD name and the outcome of its apply operation (`created`, `updated`, `unchanged`) at `INFO` level and any errors at `ERROR` level before exiting with a non-zero code. The Helm error message will reference the Job name, making log retrieval straightforward: `kubectl logs -n grove-system job/grove-crd-installer`.

#### Risk: Schema Incompatibility During Upgrade

During a rolling upgrade, the new operator version may start before CRD schemas are fully propagated by the Kubernetes API server.

**Mitigation:** The hook Job runs synchronously and Helm waits for it to complete successfully before proceeding with the rest of the chart resources. Server-side apply ensures the API server acknowledges the updated schemas before the Job exits. The operator's existing readiness probe and leader-election startup sequence provide an additional buffer before the operator begins reconciling.

## Design Details

### Pre-Upgrade Hook Job Using the Operator Image

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
        // server-side apply each CRD
        if err := applyCRD(ctx, client, data); err != nil {
            return fmt.Errorf("applying %s: %w", entry.Name(), err)
        }
        log.Info("applied CRD", "name", entry.Name())
    }
    return nil
}
```

The subcommand uses server-side apply (`--force-conflicts`) so that fields previously managed by other field managers (e.g., a cluster admin who manually patched a CRD) do not block the upgrade.

#### Hook Job Manifest

```yaml
# templates/crd-installer-job.yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ include "grove.fullname" . }}-crd-installer
  annotations:
    "helm.sh/hook": pre-install,pre-upgrade
    "helm.sh/hook-weight": "-10"
    "helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded
spec:
  template:
    spec:
      serviceAccountName: {{ include "grove.fullname" . }}-crd-installer
      restartPolicy: OnFailure
      containers:
        - name: crd-installer
          image: "{{ .Values.operator.image.repository }}:{{ .Values.operator.image.tag }}"
          imagePullPolicy: {{ .Values.operator.image.pullPolicy }}
          command: ["/manager", "install-crds"]
```

The Job uses the same image reference as the operator Deployment. No additional image needs to be built, pushed, or pulled.

#### ServiceAccount and ClusterRole

```yaml
# templates/crd-installer-rbac.yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "grove.fullname" . }}-crd-installer
  annotations:
    "helm.sh/hook": pre-install,pre-upgrade
    "helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: {{ include "grove.fullname" . }}-crd-installer
  annotations:
    "helm.sh/hook": pre-install,pre-upgrade
    "helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded
rules:
  - apiGroups: ["apiextensions.k8s.io"]
    resources: ["customresourcedefinitions"]
    verbs: ["get", "create", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: {{ include "grove.fullname" . }}-crd-installer
  annotations:
    "helm.sh/hook": pre-install,pre-upgrade
    "helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: {{ include "grove.fullname" . }}-crd-installer
subjects:
  - kind: ServiceAccount
    name: {{ include "grove.fullname" . }}-crd-installer
    namespace: {{ .Release.Namespace }}
```

All three resources carry the same hook annotations and are cleaned up automatically by Helm after the hook succeeds.

#### Hook Cleanup Policy

`hook-delete-policy: before-hook-creation,hook-succeeded` ensures:

* On each `helm upgrade`, the previous hook Job is deleted before the new one is created, so stale Jobs do not accumulate.
* On success, the Job is removed immediately, leaving no persistent hook artefacts in the namespace.
* On failure, the Job is **not** deleted, allowing `kubectl logs` inspection before the next upgrade attempt cleans it up.

#### Changes to the grove Chart

* `charts/crds/` directory is removed — the hook Job becomes the sole CRD delivery mechanism.
* New template files are added: `crd-installer-job.yaml`, `crd-installer-rbac.yaml`.
* `prepare-charts.sh` is updated to copy CRD files into `operator/internal/crds/files/` in addition to (temporarily, during transition) `operator/charts/crds/`.
* No changes to `values.yaml` are required — the hook uses the same image values already present for the operator Deployment.

#### GitOps Integration

Because the hook fires as part of the single `grove` Helm release, no special GitOps configuration is required. ArgoCD and Flux both honour Helm hooks natively:

* **ArgoCD** executes `pre-upgrade` hooks during sync and waits for the Job to complete before proceeding with remaining resources. A single `Application` for `grove` is sufficient.
* **Flux** `helm-controller` respects Helm hook annotations. The `grove` `HelmRelease` resource alone covers both CRD upgrades and operator upgrades.

No sync-wave ordering or `dependsOn` between separate applications is needed.

### Monitoring

No new metrics or status conditions are introduced by this GREP. Observability of CRD schema state relies on existing Kubernetes mechanisms:

* `kubectl get crd <name> -o jsonpath='{.status.storedVersions}'` reports which API versions are currently served.
* The hook Job logs each CRD apply result at `INFO` level, providing a clear audit trail per upgrade.
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
  4. Asserts that the hook Job completed successfully.
  5. Asserts that `kubectl get crd podcliquesets.grove.io -o json` reflects the new field.
  6. Verifies that existing `PodCliqueSet` CRs are unaffected.
* **Hook failure test:** CI verifies that a simulated apply failure (e.g., RBAC denied) causes `helm upgrade` to fail with a clear error referencing the hook Job, and that the Job pod logs contain the structured error message.

### Graduation Criteria

**Alpha (initial merge):**

* `install-crds` subcommand is implemented and CRD YAML is embedded in the operator binary.
* Hook Job, ServiceAccount, ClusterRole, and ClusterRoleBinding templates are added to the `grove` chart.
* `crds/` directory is removed from the `grove` chart.
* A single `helm upgrade grove` correctly upgrades all five CRDs in a live cluster before the operator rolls out.
* E2E upgrade scenario passes in CI.
* Hook failure test passes in CI.

**GA:**

* At least two releases have shipped using the hook-based model without regressions.
* User-facing documentation confirms that no manual CRD steps are required on upgrade.

## Implementation History

* 2026-03-19: GREP created, tracking issue [#436](https://github.com/ai-dynamo/grove/issues/436).

## Appendix

* Tracking issue: [#436 — Helm upgrade does not upgrade CRDs](https://github.com/ai-dynamo/grove/issues/436)
* Helm 3 CRD documentation: [Helm Docs — Custom Resource Definitions](https://helm.sh/docs/chart_best_practices/custom_resource_definitions/)
* Go embed package: [pkg.go.dev/embed](https://pkg.go.dev/embed)
* cert-manager CRD chart pattern: [cert-manager installation](https://cert-manager.io/docs/installation/helm/)
* kube-prometheus-stack CRD subchart: [prometheus-community/helm-charts](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack/charts/crds)
* Crossplane CRD management: [Crossplane Helm install](https://docs.crossplane.io/latest/software/install/)
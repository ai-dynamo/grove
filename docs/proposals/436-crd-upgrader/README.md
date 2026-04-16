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
    - [Story 3](#story-3)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
    - [Risk: CRD Deletion on Helm Uninstall](#risk-crd-deletion-on-helm-uninstall)
    - [Risk: RBAC for CRD Management](#risk-rbac-for-crd-management)
    - [Risk: Init Container Failure Visibility](#risk-init-container-failure-visibility)
    - [Risk: Schema Incompatibility During Upgrade](#risk-schema-incompatibility-during-upgrade)
- [Design Details](#design-details)
  - [Fresh Install: crds/ Directory](#fresh-install-crds-directory)
  - [Opt-In Upgrade: crdInstaller Init Container](#opt-in-upgrade-crdinstaller-init-container)
    - [values.yaml](#valuesyaml)
    - [Init Container in the Deployment](#init-container-in-the-deployment)
    - [Conditional RBAC](#conditional-rbac)
    - [Manual Upgrade Path (default)](#manual-upgrade-path-default)
    - [Note for helm template / GitOps users](#note-for-helm-template--gitops-users)
  - [Considered Alternatives](#considered-alternatives)
    - [Helm Pre-Upgrade Hook Job](#helm-pre-upgrade-hook-job)
    - [Separate grove-crds Helm Chart](#separate-grove-crds-helm-chart)
  - [Monitoring](#monitoring)
  - [Dependencies](#dependencies)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Appendix](#appendix)
<!-- /toc -->

## Summary

Grove currently places its CRDs (`PodCliqueSet`, `PodClique`, `PodCliqueScalingGroup`, `PodGang`, `ClusterTopology`) under the Helm `crds/` directory. Helm 3 installs CRDs on `helm install` but silently skips them on `helm upgrade`. This GREP introduces a `crdInstaller` opt-in mechanism: an init container in the grove operator Deployment that applies all CRDs via server-side apply before the operator process starts. The default is `crdInstaller.enabled: false` — fresh installs continue to work via the `crds/` directory unchanged, and users who upgrade manually retain full control. Users who want automated CRD upgrades on every `helm upgrade` set `crdInstaller.enabled: true`.

## Motivation

Helm's `crds/` directory has intentional lifecycle limitations: CRDs placed there are **never upgraded** and **never deleted** by Helm. This means that a user who runs `helm upgrade grove` to move to a newer operator version receives a new binary but keeps the old CRD schemas. New CRD fields, validation rules, or structural changes introduced between releases go unnoticed until a production failure surfaces them.

For an operator such as Grove, where the API surface (`PodCliqueSet`, `PodClique`, etc.) is actively evolving, this creates a latent correctness risk: the running controller may attempt to read or write fields that do not yet exist in the stored schema, or that are now subject to new CEL validation rules. The outcome is subtle and hard to debug: admission webhooks may pass objects that newer schemas would reject, or status fields may be silently dropped.

This GREP does not force a new behaviour on all users. It adds a supported, tested, opt-in path to automated CRD upgrades while preserving the existing manual upgrade path as the default.

### Goals

* Preserve existing behaviour on fresh install — the `crds/` directory installs CRDs as before, with no user-visible change.
* Provide an opt-in init container mechanism (`crdInstaller.enabled: true`) that automatically upgrades CRDs on every `helm upgrade`.
* Allow users who prefer manual control to continue upgrading CRDs themselves, exactly as before this change.
* Ensure the opt-in path works correctly regardless of deployment toolchain: native Helm, `helm template` with kustomize/ArgoCD/Flux, or raw `kubectl apply`.
* Preserve existing CRD data — no CRD deletion must occur as part of normal upgrades.

### Non-Goals

* Making automated CRD upgrades the default — users must explicitly opt in.
* CRD version conversion webhooks or multi-version CRD support — this GREP only addresses delivery of CRD schemas, not API versioning strategy.
* Automatic migration of existing Custom Resources when breaking schema changes are introduced.
* Changes to how CRDs are generated from Go API types — `controller-gen` and `make generate` remain the authoritative source.

## Proposal

Two paths exist side-by-side after this change:

Grove is deployed using a variety of toolchains. Some users run `helm install`/`helm upgrade` directly. Others render manifests with `helm template` (piped to `kubectl apply` or committed to a GitOps repo) and apply them via ArgoCD, Flux, or kustomize. These toolchains interact with the `crds/` directory differently: `helm install` applies it automatically, but `helm template` silently omits it unless `--include-crds` is passed, and GitOps tools that render manifests offline never see it. This GREP must work correctly across all of these deployment styles.

**Default path (manual):** The `crds/` directory remains in the `grove` chart. `helm install` applies CRDs from it on a fresh install as today. On `helm upgrade`, CRDs are not touched by Helm. Users who use `helm template` or GitOps tooling must handle CRDs separately (see [Note for helm template / GitOps users](#note-for-helm-template--gitops-users) below). Users who want to upgrade CRDs manually apply the latest CRD manifests before upgrading the operator. This is identical to the behaviour before this GREP.

**Opt-in path (automated):** Users set `crdInstaller.enabled: true` in their values. A `crd-installer` init container is injected into the grove operator Deployment. On every Pod start — including rollouts triggered by `helm upgrade`, `helm template | kubectl apply`, or a GitOps sync — the init container runs the `grove-install-crds` image, which applies all five Grove CRDs via server-side apply before the main operator container starts. Kubernetes enforces that the init container completes successfully before the operator process runs, making CRD lifecycle management a property of the Deployment itself rather than of the deployment toolchain.

When `crdInstaller.enabled: true` the RBAC rules for `apiextensions.k8s.io` are conditionally added to the operator ClusterRole. This provides the necessary RBAC for the init container to install the CRDs.

### User Stories

#### Story 1

As a platform engineer upgrading Grove from an existing installation, I want the default upgrade experience to be identical to what I had before — I apply CRDs from the GitHub release myself, then run `helm upgrade grove`. No behaviour change, no new required flags.

#### Story 2

As a platform engineer who prefers a fully automated upgrade path, I set `crdInstaller.enabled: true` once and from that point forward a single `helm upgrade grove` upgrades both the CRDs and the operator, in the correct order, without any manual steps.

#### Story 3

As a GitOps engineer managing Grove via ArgoCD or Flux with rendered manifests from `helm template`, I need CRDs to be present on both fresh installs and upgrades. `helm template` does **not** render the `crds/` directory by default — the `--include-crds` flag is required to emit CRD manifests. Rather than managing that flag and a separate CRD sync-wave, I set `crdInstaller.enabled: true` once. The init container handles both cases identically: it calls server-side apply with `create`/`patch`, so it creates CRDs if they are absent on a fresh cluster and upgrades them on subsequent rollouts.

### Limitations/Risks & Mitigations

#### Risk: CRD Deletion on Helm Uninstall

The init container applies CRDs directly to the cluster via server-side apply — they are not Helm-managed resources. Therefore `helm uninstall grove` will not delete the CRDs regardless of whether `crdInstaller.enabled` is true or false.

Users who intentionally want to remove all CRDs must do so explicitly with `kubectl delete crd`.

#### Risk: RBAC for CRD Management

When `crdInstaller.enabled: true`, the operator ClusterRole gains `get`, `create`, `update`, and `patch` verbs on the five specific Grove CRD names in the `apiextensions.k8s.io` API group. This is scoped to named resources only and is not granted by default.

**Mitigation:** The RBAC rules are gated on `{{- if .Values.crdInstaller.enabled }}` in `clusterrole.yaml` and are absent from the default installation. Users who enable the feature accept this expanded footprint consciously.

#### Risk: Init Container Failure Visibility

If the init container fails (e.g., transient API server error, RBAC misconfiguration), the operator Pod will not start. Kubernetes will restart the init container according to the Pod's `restartPolicy`. The failure is visible in `kubectl describe pod` and `kubectl logs <pod> -c crd-installer`.

**Mitigation:** The `grove-install-crds` image logs each CRD name and outcome (`created`, `updated`, `unchanged`) at `INFO` level and errors at `ERROR` level before exiting non-zero. The pod event stream surfaces the init container failure immediately, making diagnosis straightforward.

#### Risk: Schema Incompatibility During Upgrade

During a rolling upgrade with `crdInstaller.enabled: true`, the new operator Pod may start before older Pods finish. The init container on the new Pod upgrades CRDs before the operator process in that Pod runs. Existing Pods continue running against the same CRD schema they started with.

**Mitigation:** Grove follows Kubernetes API compatibility conventions — new optional fields are additive and existing fields are not removed within a version. Server-side apply completes synchronously before the init container exits, so the API server acknowledges the schema before the operator process in that Pod starts.

## Design Details

### Fresh Install: crds/ Directory

The `operator/charts/crds/` directory is **retained**. On `helm install`, Helm applies all five CRD manifests from this directory before any other chart resources. This is unchanged from the behaviour before this GREP.

The `crds/` directory is populated by copying the `controller-gen` output from `operator/api/core/v1alpha1/crds/` and `scheduler/api/core/v1alpha1/crds/` as part of the chart preparation step. This copy must be kept in sync with the generated sources on every release.

### Opt-In Upgrade: crdInstaller Init Container

#### values.yaml

```yaml
crdInstaller:
  # enabled controls whether the operator automatically installs/upgrades Grove CRDs on startup
  # via an init container. Defaults to false — CRDs are installed by Helm on fresh install via
  # the crds/ directory, and upgrades are managed manually by the user.
  # Set to true to opt in to automated CRD upgrades on every helm upgrade.
  # WARNING: when enabled, the operator ServiceAccount receives apiextensions.k8s.io permissions.
  enabled: false
  image:
    repository: grove-install-crds
    # Overrides the image tag whose default is the chart appVersion.
    tag: v0.1.0-dev
    pullPolicy: IfNotPresent
```

#### Init Container in the Deployment

The init container is conditionally injected into the operator Deployment:

```yaml
{{- if .Values.crdInstaller.enabled }}
initContainers:
  - name: crd-installer
    image: {{ include "image" .Values.crdInstaller.image }}
    imagePullPolicy: {{ .Values.crdInstaller.image.pullPolicy }}
    volumeMounts:
      - name: kube-api-access-grove
        mountPath: /var/run/secrets/kubernetes.io/serviceaccount
        readOnly: true
{{- end }}
```

The init container reuses the existing `kube-api-access-grove` projected volume that the operator container already mounts for its own API access. No new volumes are required.

#### Conditional RBAC

The CRD management rules in `clusterrole.yaml` are only rendered when `crdInstaller.enabled: true`:

```yaml
{{- if .Values.crdInstaller.enabled }}
- apiGroups:
  - apiextensions.k8s.io
  resources:
  - customresourcedefinitions
  resourceNames:
  - podcliquesets.grove.io
  - podcliques.grove.io
  - podcliquescalinggroups.grove.io
  - clustertopologies.grove.io
  - podgangs.scheduler.grove.io
  verbs:
  - get
  - create
  - update
  - patch
{{- end }}
```

#### Manual Upgrade Path (default)

Users who keep the default (`crdInstaller.enabled: false`) upgrade CRDs by applying the manifests published in the Grove GitHub release before running `helm upgrade`:

```bash
# 1. Apply the latest CRD manifests from the release
kubectl apply --server-side --force-conflicts -f https://github.com/ai-dynamo/grove/releases/download/<version>/crds.yaml

# 2. Upgrade the operator
helm upgrade grove oci://ghcr.io/ai-dynamo/grove --version <version>
```

The `--force-conflicts` flag is required because Helm holds field ownership over `.spec.versions` on CRDs it originally installed. Without it, `kubectl apply --server-side` reports a conflict and refuses to update.

#### Note for helm template / GitOps users

`helm template` does **not** render the `crds/` directory unless `--include-crds` is passed. Users rendering manifests with `helm template` and applying them via ArgoCD, Flux, or `kubectl apply` will not get CRDs installed on a fresh cluster unless they either:

1. Pass `--include-crds` to `helm template` and apply the resulting CRD manifests as a separate sync-wave before the operator manifests, or
2. Set `crdInstaller.enabled: true` — the recommended approach for GitOps users.

With `crdInstaller.enabled: true`, the init container handles fresh installs (CRDs absent → creates them) and upgrades (CRDs present → patches them) identically, with no sync-wave ordering required.

### Considered Alternatives

#### Helm Pre-Upgrade Hook Job

A `pre-install`/`pre-upgrade` hook Job was considered as an alternative to the init container. The hook fires automatically on every `helm upgrade grove`, satisfying Goal 1 of zero manual steps. However the hook requires embedding CRD YAML in a ConfigMap or init container image, introduces a separate Job lifecycle to manage, and surfaces failures as obscure Helm hook errors rather than clear pod events. The init container approach uses an existing mechanism (pod init containers), reuses the operator's service account, and surfaces failures in familiar Kubernetes pod status.

#### Separate grove-crds Helm Chart

A dedicated `grove-crds` chart was considered but requires users to manage two charts with an explicit upgrade ordering dependency. A user who upgrades `grove` without upgrading `grove-crds` first is in the same broken state as today — it does not eliminate the human error vector, only moves it. The init container approach, when opted in, is fully self-contained within the single `grove` release.

### Monitoring

No new metrics or status conditions are introduced by this GREP. When `crdInstaller.enabled: true`:

* The init container logs each CRD apply outcome at `INFO` level per upgrade.
* `kubectl describe pod <grove-operator-pod>` shows init container status and exit code.
* `kubectl logs <grove-operator-pod> -c crd-installer` shows the full apply log.

### Dependencies

* A `grove-install-crds` container image is built and published as part of the Grove release pipeline alongside the existing `grove-operator` image. Both images share the same version tag.
* The image contains the five CRD YAML files and a minimal binary that applies them via server-side apply.
* No new Go dependencies are introduced in the operator module.

### Test Plan

* **E2E upgrade test:** A new E2E scenario is added (tracked in a sub-issue of #436) that:
  1. Installs `grove` at version N with `crdInstaller.enabled: true`.
  2. Simulates a CRD schema change (e.g., adds a new optional field to `PodCliqueSet`) in version N+1.
  3. Runs `helm upgrade grove` to version N+1.
  4. Asserts that the init container completed successfully.
  5. Asserts that `kubectl get crd podcliquesets.grove.io -o json` reflects the new field.
  6. Verifies that existing `PodCliqueSet` CRs are unaffected.
* **Default-off test:** CI verifies that a fresh install with default values (`crdInstaller.enabled: false`) produces no init container in the Deployment manifest and no `apiextensions.k8s.io` rules in the ClusterRole.
* **Init container failure test:** A test verifies that an RBAC failure in the init container prevents the operator Pod from starting and surfaces a clear event message.

### Graduation Criteria

**Alpha (initial merge):**

* `grove-install-crds` image is built and published by the release pipeline.
* `crdInstaller.enabled: false` is the default in `values.yaml`.
* `crds/` directory is present in the chart and populated on release.
* `helm upgrade grove` with `crdInstaller.enabled: true` correctly upgrades all five CRDs before the operator rolls out.
* `helm upgrade grove` with default values leaves CRD upgrade behaviour unchanged from pre-GREP.

**Beta:**

* E2E upgrade scenario passes in CI.
* Default-off and init container failure tests pass in CI.

**GA:**

* At least two releases have shipped with the opt-in path available without regressions.
* User-facing documentation covers both the manual upgrade path and the opt-in automated path.

## Appendix

* Tracking issue: [#436 — Helm upgrade does not upgrade CRDs](https://github.com/ai-dynamo/grove/issues/436)
* Helm 3 CRD documentation: [Helm Docs — Custom Resource Definitions](https://helm.sh/docs/chart_best_practices/custom_resource_definitions/)
* Go embed package: [pkg.go.dev/embed](https://pkg.go.dev/embed)
* NIM operator CRD init container pattern: reference implementation used as prior art for this approach.

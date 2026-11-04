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
  - [Mandatory crdInstaller Init Container](#mandatory-crdinstaller-init-container)
    - [values.yaml](#valuesyaml)
    - [Init Container in the Deployment](#init-container-in-the-deployment)
    - [RBAC](#rbac)
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

Grove currently places its six CRDs (`PodCliqueSet`, `PodClique`, `PodCliqueScalingGroup`, `ClusterTopologyBinding`, `PodGangMap`, and `PodGang`) under the Helm `crds/` directory. Helm 3 installs CRDs on `helm install` but silently skips them on `helm upgrade`. This GREP adds a mandatory init container to the Grove operator Deployment. It always configures the PodCliqueSet conversion webhook and applies all bundled CRDs when `crdInstaller.upgradeCRDs=true`.

## Motivation

Helm's `crds/` directory has intentional lifecycle limitations: CRDs placed there are **never upgraded** and **never deleted** by Helm. This means that a user who runs `helm upgrade grove` to move to a newer operator version receives a new binary but keeps the old CRD schemas. New CRD fields, validation rules, or structural changes introduced between releases go unnoticed until a production failure surfaces them.

For an operator such as Grove, where the API surface (`PodCliqueSet`, `PodClique`, etc.) is actively evolving, this creates a latent correctness risk: the running controller may attempt to read or write fields that do not yet exist in the stored schema, or that are now subject to new CEL validation rules. The outcome is subtle and hard to debug: admission webhooks may pass objects that newer schemas would reject, or status fields may be silently dropped.

CRD upgrades are explicit. Conversion webhook configuration remains mandatory.

### Goals

* Preserve existing behaviour on fresh install — the `crds/` directory installs CRDs as before.
* Always configure the deployment-specific PodCliqueSet conversion webhook address.
* Allow users to upgrade CRDs before the operator starts.
* Work regardless of deployment toolchain: native Helm, `helm template` with kustomize/ArgoCD/Flux, or raw `kubectl apply`.
* Preserve existing CRD data — no CRD deletion must occur as part of normal upgrades.

### Non-Goals

* Automatic migration of existing Custom Resources when breaking schema changes are introduced.
* Changes to how CRDs are generated from Go API types — `controller-gen` and `make generate` remain the authoritative source.

## Proposal

Grove is deployed using a variety of toolchains. Some users run `helm install`/`helm upgrade` directly. Others render manifests with `helm template` (piped to `kubectl apply` or committed to a GitOps repo) and apply them via ArgoCD, Flux, or kustomize. These toolchains interact with the `crds/` directory differently: `helm install` applies it automatically, but `helm template` silently omits it unless `--include-crds` is passed, and GitOps tools that render manifests offline never see it. This GREP must work correctly across all of these deployment styles.

A `crd-installer` init container is always present in the Grove operator Deployment. On every Pod start, it patches the PodCliqueSet conversion webhook before the main operator container starts. When `crdInstaller.upgradeCRDs=true`, it also applies all bundled CRDs via server-side apply. Kubernetes enforces that the init container completes successfully before the operator process runs.

The operator ClusterRole always includes the `apiextensions.k8s.io` permissions required by the installer.

### User Stories

#### Story 1

As a platform engineer upgrading Grove, I want to opt in to upgrading the CRDs before the operator starts.

#### Story 2

As a platform engineer, I want the operator to refuse to start when an explicitly requested CRD upgrade fails.

#### Story 3

As a GitOps engineer managing Grove via ArgoCD or Flux, I want one value that upgrades existing CRDs before the operator starts.

### Limitations/Risks & Mitigations

#### Risk: CRD Deletion on Helm Uninstall

With `crdInstaller.upgradeCRDs=true`, the init container applies CRDs directly to the cluster via server-side apply. They are not deleted by `helm uninstall grove`.

Users who intentionally want to remove all CRDs must do so explicitly with `kubectl delete crd`.

#### Risk: RBAC for CRD Management

The operator ClusterRole has `create`, `update`, `patch`, and `delete` on the specific CRDs managed by the installer. The cert-controller also has `get`, `list`, and `watch` on CRDs so it can inject the conversion webhook CA bundle.

**Mitigation:** CRD write permissions use `resourceNames` and do not grant write access to arbitrary CRDs.

#### Risk: Init Container Failure Visibility

If the init container fails (e.g., transient API server error, RBAC misconfiguration), the operator Pod will not start. Kubernetes will restart the init container according to the Pod's `restartPolicy`. The failure is visible in `kubectl describe pod` and `kubectl logs <pod> -c crd-installer`.

**Mitigation:** The `grove-install-crds` image logs each CRD that it applies at `INFO` level and errors at `ERROR` level before exiting non-zero. The pod event stream surfaces the init container failure immediately, making diagnosis straightforward.

#### Risk: Schema Incompatibility During Upgrade

During a rolling upgrade with `crdInstaller.upgradeCRDs=true`, the init container on the new Pod upgrades CRDs before the operator process in that Pod runs. Existing Pods continue running against the same CRD schema they started with.

**Mitigation:** Grove follows Kubernetes API compatibility conventions — new optional fields are additive and existing fields are not removed within a version. Server-side apply completes synchronously before the init container exits, so the API server acknowledges the schema before the operator process in that Pod starts.

## Design Details

### Fresh Install: crds/ Directory

The `operator/charts/crds/` directory is **retained**. On `helm install`, Helm applies all six CRD manifests from this directory before any other chart resources. This is unchanged from the behaviour before this GREP.

The `crds/` directory is populated by copying the generated CRDs from `operator/api/core/crds/` and `scheduler/api/core/v1alpha1/crds/` as part of the chart preparation step. This copy must be kept in sync with the generated sources on every release.

### Mandatory crdInstaller Init Container

#### values.yaml

```yaml
crdInstaller:
  upgradeCRDs: false
  image:
    repository: grove-install-crds
    # Overrides the image tag whose default is the chart appVersion.
    tag: v0.1.0-dev
    pullPolicy: IfNotPresent
```

#### Init Container in the Deployment

The init container is always present in the operator Deployment:

```yaml
initContainers:
  - name: crd-installer
    image: {{ include "image" .Values.crdInstaller.image }}
    imagePullPolicy: {{ .Values.crdInstaller.image.pullPolicy }}
    args:
      - --upgrade-crds={{ .Values.crdInstaller.upgradeCRDs }}
      - --conversion-webhook-port={{ .Values.config.server.webhooks.port }}
      {{- if eq .Values.config.server.webhooks.certProvisionMode "manual" }}
      - --conversion-webhook-ca-file={{ .Values.config.server.webhooks.serverCertDir }}/ca.crt
      {{- end }}
    env:
      - name: GROVE_OPERATOR_SERVICE_NAME
        value: {{ .Values.service.name | quote }}
    volumeMounts:
      - name: kube-api-access-grove
        mountPath: /var/run/secrets/kubernetes.io/serviceaccount
        readOnly: true
      {{- if eq .Values.config.server.webhooks.certProvisionMode "manual" }}
      - name: grove-webhook-server-cert
        mountPath: {{ .Values.config.server.webhooks.serverCertDir }}
        readOnly: true
      {{- end }}
```

The init container reuses the existing `kube-api-access-grove` projected volume that the operator container already mounts for its own API access. It always patches the PodCliqueSet conversion webhook. In manual certificate mode, it reads `ca.crt` from the webhook certificate Secret and sets the conversion webhook CA bundle. It applies all bundled CRDs only when `crdInstaller.upgradeCRDs=true`.

#### RBAC

The CRD read and management rules are always rendered:

```yaml
- apiGroups:
  - apiextensions.k8s.io
  resources:
  - customresourcedefinitions
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - apiextensions.k8s.io
  resources:
  - customresourcedefinitions
  resourceNames:
  - podcliquesets.grove.io
  - podcliques.grove.io
  - podcliquescalinggroups.grove.io
  - clustertopologies.grove.io
  - clustertopologybindings.grove.io
  - podgangmaps.grove.io
  - podgangs.scheduler.grove.io
  verbs:
  - create
  - update
  - patch
  - delete
```

The installer can delete an empty legacy `clustertopologies.grove.io` CRD during a CRD upgrade. It retains the CRD when ClusterTopology objects still exist.

#### Note for helm template / GitOps users

`helm template` does **not** render the `crds/` directory unless `--include-crds` is passed. Use that flag for a fresh install. Set `crdInstaller.upgradeCRDs=true` when the init container must upgrade existing CRDs.

Reapplying the generated PodCliqueSet CRD after the init container completes can restore its generated conversion webhook address. The operator Pod must restart after such a sync so that the init container restores the deployment-specific Service name, namespace, and port.

### Considered Alternatives

#### Helm Pre-Upgrade Hook Job

A `pre-install`/`pre-upgrade` hook Job was considered as an alternative to the init container. However the hook requires embedding CRD YAML in a ConfigMap or init container image, introduces a separate Job lifecycle to manage, and surfaces failures as obscure Helm hook errors rather than clear pod events. The init container approach uses an existing mechanism (pod init containers), reuses the operator's service account, and surfaces failures in familiar Kubernetes pod status.

#### Separate grove-crds Helm Chart

A dedicated `grove-crds` chart was considered but requires users to manage two charts with an explicit upgrade ordering dependency. The init container is self-contained within the single `grove` release.

### Monitoring

No new metrics or status conditions are introduced by this GREP.

* With `crdInstaller.upgradeCRDs=true`, the init container logs each applied CRD at `INFO` level.
* `kubectl describe pod <grove-operator-pod>` shows init container status and exit code.
* `kubectl logs <grove-operator-pod> -c crd-installer` shows the full apply log.

### Dependencies

* A `grove-install-crds` container image is built and published as part of the Grove release pipeline alongside the existing `grove-operator` image. Both images share the same version tag.
* The image contains the six CRD YAML files and a minimal binary that applies them via server-side apply.
* No new Go dependencies are introduced in the operator module.

### Test Plan

* **E2E upgrade test:** A new E2E scenario is added (tracked in a sub-issue of #436) that:
  1. Installs `grove` at version N.
  2. Simulates a CRD schema change (e.g., adds a new optional field to `PodCliqueSet`) in version N+1.
  3. Runs `helm upgrade grove` to version N+1 with `crdInstaller.upgradeCRDs=true`.
  4. Asserts that the init container completed successfully.
  5. Asserts that `kubectl get crd podcliquesets.grove.io -o json` reflects the new field.
  6. Verifies that existing `PodCliqueSet` CRs are unaffected.
* **Chart test:** CI verifies that the init container is always rendered and receives `crdInstaller.upgradeCRDs`.
* **Init container failure test:** A test verifies that an RBAC failure in the init container prevents the operator Pod from starting and surfaces a clear event message.

### Graduation Criteria

**Alpha (initial merge):**

* `grove-install-crds` image is built and published by the release pipeline.
* `crds/` directory is present in the chart and populated on release.
* `helm upgrade grove --set crdInstaller.upgradeCRDs=true` upgrades all CRDs before the operator rolls out.

**Beta:**

* E2E upgrade scenario passes in CI.
* Chart and init container failure tests pass in CI.

**GA:**

* At least two releases have shipped with the mandatory installer without regressions.
* User-facing documentation covers the automated upgrade path.

## Appendix

* Tracking issue: [#436 — Helm upgrade does not upgrade CRDs](https://github.com/ai-dynamo/grove/issues/436)
* Helm 3 CRD documentation: [Helm Docs — Custom Resource Definitions](https://helm.sh/docs/chart_best_practices/custom_resource_definitions/)
* Go embed package: [pkg.go.dev/embed](https://pkg.go.dev/embed)
* NIM operator CRD init container pattern: reference implementation used as prior art for this approach.

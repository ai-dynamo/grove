# Upgrading to v0.2.0-beta1

Grove v0.2.0-beta1 introduces `grove.io/v1beta1`, which replaces `grove.io/v1alpha1` as the version Grove stores and uses, and `operator.config.grove.io/v1beta1` for the operator configuration. The new versions have the same behavior as the old ones; they drop only fields that Grove no longer uses. Existing workloads keep running during the upgrade, and you can keep using `grove.io/v1alpha1` manifests while you move to `grove.io/v1beta1`. This guide explains what changes, how to upgrade from v0.1.x, how to verify the upgrade, and how to downgrade.

## What Changes

| API | v0.1.x | v0.2.0-beta1 |
|-----|--------|--------------|
| PodCliqueSet (PCS), PodClique (PCLQ), PodCliqueScalingGroup (PCSG), ClusterTopologyBinding, PodGangMap | `grove.io/v1alpha1` | `grove.io/v1beta1` is served and stored. `grove.io/v1alpha1` is still served and deprecated. |
| Operator configuration | `operator.config.grove.io/v1alpha1` | `operator.config.grove.io/v1beta1`. `v1alpha1` is still accepted and deprecated. |
| PodGang | `scheduler.grove.io/v1alpha1` | Unchanged. |

`grove.io/v1beta1` differs from `grove.io/v1alpha1` in two fields:

- **`topologyConstraint.packDomain` is removed.** Use `topologyConstraint.pack.required`. Grove converts a `packDomain` stored by an earlier release to `pack.required`, so existing workloads keep their placement.
- **`status.podGangStatuses` of the PCS is removed.** Grove never set it.

`operator.config.grove.io/v1beta1` has the same fields as `operator.config.grove.io/v1alpha1`.

In both versions, an unset `scaleConfig.minReplicas` of a PodClique or PodCliqueScalingGroup now defaults to its `minAvailable` instead of its `replicas`. Existing PodCliqueSets already have `minReplicas` set, so they keep their value. To keep the old behavior for new PodCliqueSets, set `minReplicas` to `replicas` explicitly.

## Prerequisites and Constraints

1. Grove v0.1.x is installed. Upgrades from the latest v0.1.x release, v0.1.0-alpha.16, are tested in CI.
2. The Grove operator can serve its conversion webhook. The API server calls the operator to convert PCS objects between versions, so the operator Service must be reachable from the API server, as it already is for the admission webhooks.
3. If you manage the operator configuration yourself instead of through the Helm chart, read [Operator Configuration](#operator-configuration) before upgrading.

## Upgrading

Helm does not upgrade CRDs, so the CRDs of v0.2.0-beta1 must be applied before the new operator starts. The simplest way is to let the operator's CRD installer init container apply them:

```bash
helm upgrade grove oci://ghcr.io/ai-dynamo/grove/grove-charts \
  --version v0.2.0-beta1 \
  --namespace <namespace> \
  --reuse-values \
  --set crdInstaller.enabled=true
```

If you do not use the CRD installer, apply the CRDs from the chart yourself, then upgrade:

```bash
helm pull oci://ghcr.io/ai-dynamo/grove/grove-charts --version v0.2.0-beta1 --untar
kubectl apply --server-side --force-conflicts -f grove-charts/crds/
helm upgrade grove oci://ghcr.io/ai-dynamo/grove/grove-charts \
  --version v0.2.0-beta1 \
  --namespace <namespace> \
  --reuse-values
```

When the new operator starts, it:

1. Points the PCS CRD's conversion webhook at itself.
2. Rewrites every PCS, PCLQ, PCSG, ClusterTopologyBinding, and PodGangMap without changes, so the API server stores it as `grove.io/v1beta1`.
3. Sets `status.storedVersions` of each CRD to `["v1beta1"]`.
4. Starts its controllers.

The rewrite changes no spec, so no Pod is recreated.

> **Note:** Between applying the new CRDs and the new operator becoming ready, requests for PCS objects that need conversion fail, because the operator that serves the conversion webhook is not running yet. With `crdInstaller.enabled=true`, this window is the operator restart.

## Verifying the Upgrade

Check that every Grove CRD stores only `v1beta1`:

```bash
for crd in podcliquesets podcliques podcliquescalinggroups clustertopologybindings podgangmaps; do
  echo "$crd: $(kubectl get crd $crd.grove.io -o jsonpath='{.status.storedVersions}')"
done
```

Each line shows `["v1beta1"]` once the migration is complete. The operator also reports the migration in its logs and metrics:

```bash
kubectl logs -n <namespace> deploy/grove-operator | grep "storage version"
```

| Metric | Meaning |
|--------|---------|
| `grove_storage_migration_complete{crd, version}` | `1` once every object of the CRD is stored at `version`. |
| `grove_storage_migration_objects_rewritten_total{crd, version}` | Number of objects rewritten. |
| `grove_storage_migration_objects_rejected_total{crd, version}` | Number of objects that an admission webhook rejected when rewritten. |

If an object is rejected, its CRD keeps `v1alpha1` in `status.storedVersions` and the operator logs the object's name. The operator starts its controllers anyway, and the next operator start retries the object. Fix or delete the object, then restart the operator:

```bash
kubectl rollout restart -n <namespace> deploy/grove-operator
```

The rewrite does not change the object, so validation rules added after the object was created do not reject it. For example, a PCS whose scaling group has `scaleConfig.maxReplicas` below `scaleConfig.minReplicas` is rewritten, but any change to that `scaleConfig` must fix it.

## Moving Your Manifests to v1beta1

`kubectl` reads and writes Grove objects as `grove.io/v1beta1` by default. Manifests that still use `grove.io/v1alpha1` keep working, and the API server returns a warning for each request:

```
Warning: grove.io/v1alpha1 is deprecated; use grove.io/v1beta1
```

To move a manifest to `grove.io/v1beta1`, change its `apiVersion`, and replace any `packDomain` with `pack.required`:

```yaml
apiVersion: grove.io/v1beta1
kind: PodCliqueSet
metadata:
  name: my-workload
  namespace: default
spec:
  replicas: 1
  template:
    topologyConstraint:
      topologyName: my-topology
      pack:
        required: rack # was packDomain: rack
    cliques:
      - name: worker
        spec:
          roleName: worker
          replicas: 2
          podSpec:
            containers:
              - name: worker
                image: registry.k8s.io/pause:3.10
```

## Operator Configuration

The Helm chart renders the operator configuration as `operator.config.grove.io/v1beta1`, so no action is needed when you configure the operator through Helm values.

If you provide your own configuration file, the operator still accepts `operator.config.grove.io/v1alpha1` and logs a deprecation warning. To move it to `v1beta1`, change its `apiVersion`:

```yaml
apiVersion: operator.config.grove.io/v1beta1
kind: OperatorConfiguration
```

## Downgrading

After the upgrade, the API server stores Grove objects as `grove.io/v1beta1`, which v0.1.x does not know. A plain `helm rollback` leaves the operator unable to read them, so you must first move the objects back to `grove.io/v1alpha1` with `grove-install-crds prepare-downgrade`. The command:

1. Makes `v1alpha1` the storage version of the Grove CRDs, keeping both versions served.
2. Rewrites every Grove object so that it is stored as `grove.io/v1alpha1`.
3. Sets `status.storedVersions` of each CRD to `["v1alpha1"]`.
4. Removes `v1beta1` and the conversion webhook from the CRDs.

Run it while the v0.2.0-beta1 operator is still running, because PCS objects are converted by its conversion webhook. Downgrade to v0.1.0-alpha.16: the CRDs of earlier releases lack fields that v0.1.0-alpha.16 and later set, and the API server drops those fields.

### Step 1: Prepare the downgrade

From a checkout of the `v0.2.0-beta1` tag, with a kubeconfig that can update CRDs and Grove objects:

```bash
cd operator
go run ./cmd/install-crds prepare-downgrade
```

Alternatively, if the release was installed with `crdInstaller.enabled=true`, run the command in the cluster as the operator's ServiceAccount, which then has the required permissions:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: grove-prepare-downgrade
  namespace: <namespace>
spec:
  backoffLimit: 0
  template:
    spec:
      serviceAccountName: grove-operator
      automountServiceAccountToken: true
      restartPolicy: Never
      containers:
        - name: prepare-downgrade
          image: ghcr.io/ai-dynamo/grove/grove-install-crds:v0.2.0-beta1
          args: ["prepare-downgrade"]
```

```bash
kubectl apply -f prepare-downgrade-job.yaml
kubectl wait -n <namespace> --for=condition=complete job/grove-prepare-downgrade --timeout=10m
kubectl logs -n <namespace> job/grove-prepare-downgrade
```

The command is idempotent. If it fails, for example because an admission webhook rejects an object, fix the object and run it again.

### Step 2: Roll back the release

Roll back right after the command completes:

```bash
helm history grove -n <namespace>
helm rollback grove <revision-of-v0.1.0-alpha.16> -n <namespace>
```

> **Note:** If the v0.2.0-beta1 operator restarts before the rollback with `crdInstaller.enabled=true`, its init container applies the v0.2.0-beta1 CRDs again and the operator migrates the objects back to `grove.io/v1beta1`. Run `prepare-downgrade` again in that case.

### Step 3: Verify the downgrade

```bash
for crd in podcliquesets podcliques podcliquescalinggroups clustertopologybindings podgangmaps; do
  echo "$crd: $(kubectl get crd $crd.grove.io -o jsonpath='{.spec.versions[*].name} {.status.storedVersions}')"
done
```

Each line shows `v1alpha1 ["v1alpha1"]`. Any `packDomain` that existed before the upgrade now reads as `pack.required`, which v0.1.0-alpha.16 also understands.

## Limitations

- **`packDomain` cannot be set on new objects.** As in v0.1.x, a new `grove.io/v1alpha1` PCS with `packDomain` is rejected. Use `pack.required`.
- **Re-applying a manifest that sets `packDomain` fails.** After the upgrade, an existing PCS reads with `pack.required` instead of `packDomain`, so `kubectl apply` of a `grove.io/v1alpha1` manifest that still sets `packDomain` produces an object with both fields, which is rejected. Replace `packDomain` with `pack.required` in your manifests.
- **The deprecated versions are removed in a later release.** A future release stops serving `grove.io/v1alpha1` and stops accepting `operator.config.grove.io/v1alpha1`. Move your manifests and configuration to `v1beta1` before then.
- **A downgrade needs the v0.2.0-beta1 operator.** `prepare-downgrade` cannot run after the operator is removed, because PCS objects are converted by its conversion webhook.

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

Enable CRD upgrades so the installer applies the v0.2.0-beta1 CRDs before the new operator starts:

```bash
helm upgrade grove oci://ghcr.io/ai-dynamo/grove/grove-charts \
  --version v0.2.0-beta1 \
  --namespace <namespace> \
  --set crdInstaller.upgradeCRDs=true \
  --reuse-values
```

The installer always points the PCS CRD's conversion webhook at the operator. `crdInstaller.upgradeCRDs=true` also applies the bundled CRDs. Existing objects are not rewritten: the API server stores an object as `grove.io/v1beta1` the next time it is written, and the operator writes the status of most Grove objects as it reconciles them. Objects that are not written stay stored as `grove.io/v1alpha1`, which the API server converts when they are read. A later release, before `grove.io/v1alpha1` is removed, migrates the remaining objects.

> **Note:** Between applying the new CRDs and the new operator becoming ready, requests for PCS objects that need conversion fail because the conversion webhook is not running yet.

## Verifying the Upgrade

Check that every Grove CRD stores `v1beta1`:

```bash
for crd in podcliquesets podcliques podcliquescalinggroups clustertopologybindings podgangmaps; do
  echo "$crd: $(kubectl get crd $crd.grove.io -o jsonpath='{.status.storedVersions}')"
done
```

Each line shows `["v1alpha1","v1beta1"]`: objects written before the upgrade can still be stored as `v1alpha1`. Then check that your workloads read at both versions:

```bash
kubectl get podcliquesets.v1beta1.grove.io -A
kubectl get podcliquesets.v1alpha1.grove.io -A
```

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

After the upgrade, the API server stores every Grove object that is written as `grove.io/v1beta1`, which v0.1.x does not know, and the CRDs list `v1beta1` in `status.storedVersions`. A plain `helm rollback` fails, because the API server rejects CRDs that drop a stored version, so you must first move the objects back to `grove.io/v1alpha1`. The downgrade script:

1. Makes `v1alpha1` the storage version of the Grove CRDs, keeping both versions served.
2. Rewrites every Grove object so that it is stored as `grove.io/v1alpha1`.
3. Sets `status.storedVersions` of each CRD to `["v1alpha1"]`.
4. Removes `v1beta1` and the conversion webhook from the CRDs.

Run the script while the v0.2.0-beta1 operator is still running, because PCS objects are converted by its conversion webhook. Downgrade to v0.1.0-alpha.16: the CRDs of earlier releases lack fields that v0.1.0-alpha.16 and later set, and the API server drops those fields.

### Step 1: Prepare the downgrade

From a checkout of the `v0.2.0-beta1` tag, run the script with credentials that can update Grove objects in every namespace and patch CRDs and their status:

```bash
make --directory=operator run-api-downgrade
```

The script uses your current `kubectl` context. Set `KUBECONFIG` before running the command to use another kubeconfig. The script is safe to run again. If it stops because an object changed while it was being rewritten, run it again. If an admission webhook rejects an object, fix the object first. For example, the rewrite rejects a PCS admitted by an earlier release when a scaling group's `scaleConfig.maxReplicas` is less than its `scaleConfig.minReplicas`.

### Step 2: Roll back the release

Roll back right after the script completes:

```bash
helm history grove -n <namespace>
helm rollback grove <revision-of-v0.1.0-alpha.16> -n <namespace>
```

> **Note:** If the v0.2.0-beta1 operator restarts with `crdInstaller.upgradeCRDs=true` before the rollback, its init container applies the v0.2.0-beta1 CRDs again, and the objects that the operator writes are stored as `grove.io/v1beta1` again. Run the downgrade script again in that case.

### Step 3: Verify the downgrade

```bash
for crd in podcliquesets podcliques podcliquescalinggroups clustertopologybindings podgangmaps; do
  echo "$crd: $(kubectl get crd $crd.grove.io -o jsonpath='{.spec.versions[*].name} {.status.storedVersions}')"
done
```

Each line shows `v1alpha1 ["v1alpha1"]`. Any `packDomain` that existed before the upgrade now reads as `pack.required`, which v0.1.0-alpha.16 also understands.

## Limitations

- **`packDomain` is converted, not stored.** As in v0.1.x, `grove.io/v1alpha1` rejects `packDomain` on a new PCS, and accepts it on an existing one. Grove stores the value as `pack.required`, so reading the PCS back at `v1alpha1` shows `pack.required`, so tools that compare the applied manifest with the live object report a difference until you replace `packDomain` with `pack.required` in your manifests.
- **The deprecated versions are removed in a later release.** A future release stops serving `grove.io/v1alpha1` and stops accepting `operator.config.grove.io/v1alpha1`. Move your manifests and configuration to `v1beta1` before then.
- **A downgrade needs the v0.2.0-beta1 operator.** The downgrade script cannot run after the operator is removed, because PCS objects are converted by its conversion webhook.

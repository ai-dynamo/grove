# Investigation: webhook certificate startup restart

## Problem

On a fresh installation, `grove-operator` generated its webhook certificate and
then exited successfully. Kubernetes restarted the container, and only the second
process became ready. Depending on Secret volume propagation, that second startup
could also take a minute or more.

The intended behavior is for the process that generates the certificate to observe
the updated Secret volume, start the webhook server, and remain running.

## Root cause

Grove explicitly configured the OPA cert-controller with:

```go
RestartOnSecretRefresh: true
```

After cert-controller updated the Secret, it logged the following message and
called `os.Exit(0)`:

```text
Secrets have been updated; exiting so pod can be restarted
```

The chart also disabled the kubelet-managed ServiceAccount projection and replaced
it with a custom 12-hour projected token volume. Earlier experiments on kind saw
the certificate Secret take 77-117 seconds to appear with that custom projection,
while the kubelet-managed projection consistently exposed it in about 1-2 seconds.
That delay did not cause the exit, but it made startup without the forced restart
unacceptably slow.

Both parts therefore matter:

1. Disable `RestartOnSecretRefresh` so cert-controller does not terminate the
   process that generated the certificate.
2. Use the kubelet-managed ServiceAccount projection so the updated certificate
   reaches the mounted Secret promptly.

## Upstream changes since PR 706

The original PR also changed the certificate Secret type and name and removed the
operator's placeholder-Secret path. Those changes are no longer appropriate on
current `main`:

- cert-controller v0.16.0 is already in use;
- Helm preserves existing certificate data across upgrades; and
- the operator can create the placeholder Secret for GitOps deployments.

The rebase drops those superseded commits and keeps the current Secret lifecycle.

## Fix

- Set `RestartOnSecretRefresh` to `false`.
- Set `serviceAccount.automount` to `true`.
- Remove the pod-level `automountServiceAccountToken: false`.
- Remove the custom `kube-api-access-grove` projected volume and its explicit
  mounts. The API server now injects the standard `kube-api-access-*` volume.

The operator still reads its namespace from the same path,
`/var/run/secrets/kubernetes.io/serviceaccount/namespace`.

## Kind validation

Validated on 2026-11-03 with the repository Makefile targets:

```text
make kind-up
make deploy
```

The kubeconfig emitted by `make kind-up` was used for all checks.

Before disabling `RestartOnSecretRefresh`, the first process logged:

```text
07:16:51.051 server certs refreshed
07:16:51.051 Secrets have been updated; exiting so pod can be restarted
```

The pod subsequently reported `restartCount: 1`.

After the fix, a clean uninstall and deploy produced:

```text
07:19:35.579423 server certs refreshed
07:19:35.579442 certs are ready in /etc/grove-operator/webhook-certs
07:19:37.218473 Certs are ready and injected into webhook configurations
```

The pod became Ready with `restartCount: 0`. The effective pod spec contained a
kubelet-injected `kube-api-access-*` volume with a short-lived token, and both the
init container and operator mounted it at the standard ServiceAccount path.

## Security note

The old chart already gave both containers an API token; it merely constructed the
projection itself with a 12-hour expiration. The kubelet-managed projection uses a
short-lived bound token (about one hour in the kind validation) and rotates it
automatically. The containers retain the same ServiceAccount and RBAC permissions.

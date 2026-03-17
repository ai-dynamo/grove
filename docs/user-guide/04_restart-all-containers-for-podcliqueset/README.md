# PodCliqueSet In-Place Restart Guide

This guide uses Kubernetes 1.35+ **RestartAllContainers** to trigger in-place restarts of all Pods in a Grove **PodCliqueSet** via a single **ConfigMap** field `restartGeneration`. Pod names, UIDs, and IPs stay the same (no rescheduling).

## Use case: restart without rescheduling

Sometimes we want a **PodCliqueSet** (or all its Pods) to restart **without going through rescheduling**—for example when **upgrading container image versions** or when we need a clean re-run of init containers and main containers while keeping the same Pod identity and placement.

Deleting and recreating Pods is costly: it involves the scheduler, node allocation, and re-initialization of networking and storage. Kubernetes 1.35’s [Restart All Containers](https://kubernetes.io/blog/2026/01/02/kubernetes-v1-35-restart-all-containers/) feature provides an **in-place** restart instead: the kubelet restarts all containers in the Pod while preserving the Pod’s UID, IP address, volumes, and node assignment. Init containers run again in order, then all main containers start with a fresh state—so an image update or configuration change can take effect without any rescheduling. This guide shows how to trigger that in-place restart for an entire Grove PodCliqueSet at once via a ConfigMap.

### Limitations

This guide applies only to **restarting PodCliqueSets** (in-place restart of all Pods belonging to a Grove PodCliqueSet). It does not cover other workload types or cluster-wide restart scenarios.

## Idea

- Each Pod runs a **restart-watcher** sidecar (Go). It uses in-cluster config to poll the ConfigMap `grove-restart-control` in the same namespace for the key `restartGeneration`.
- When it sees `restartGeneration` **increase**, the watcher exits with a configured code (default 88), which triggers **RestartAllContainers** for that Pod.
- To trigger a batch in-place restart, **kubectl patch** the ConfigMap to increment `restartGeneration`; all Pods with the watcher will see the new value on the next poll and restart in place.

## Directory layout

```
04_restart-all-containers-for-podcliqueset/
├── src/
│   ├── main.go       # restart-watcher sidecar source
│   └── go.mod
├── Dockerfile        # build watcher image
├── Makefile          # build and push image
├── manifests/
│   ├── namespace.yaml
│   ├── rbac.yaml     # SA + Role + RoleBinding (read ConfigMap)
│   ├── configmap.yaml
│   └── podcliqueset.yaml
└── README.md
```

## Prerequisites

1. **Cluster**: Kubernetes **1.35+** with **RestartAllContainersOnContainerExits** and **NodeDeclaredFeatures** enabled. Both feature gates must be enabled on **both** the API server and the kubelet. **RestartAllContainersOnContainerExits** depends on **NodeDeclaredFeatures**, so enable them together. See your cluster or distribution docs for how to set feature gates.
2. **Grove**: CRD and Operator installed, **v0.1.0-alpha.4 or later**.
3. **Registry**: A Docker registry you can push the `restart-watcher` image to and that cluster nodes can pull from.

## Steps

### 1. Build and push the restart-watcher image

From this guide's directory:

```bash
# Set your registry (required)
export REGISTRY=your-registry.io/your-user
export IMAGE_TAG=latest

make push
```

Note the image name, e.g. `$(REGISTRY)/restart-watcher:$(IMAGE_TAG)`.

### 2. Set the watcher image in the PodCliqueSet

Edit `manifests/podcliqueset.yaml` and replace both `WATCHER_IMAGE` with the image you pushed, e.g.:

```bash
sed -i "s|WATCHER_IMAGE|${REGISTRY}/restart-watcher:${IMAGE_TAG}|g" manifests/podcliqueset.yaml
```

Or change `image: WATCHER_IMAGE` to e.g. `image: your-registry.io/your-user/restart-watcher:latest` by hand.

### 3. Deploy PodCliqueSet and ConfigMap

```bash
kubectl apply -f manifests/namespace.yaml
kubectl apply -f manifests/rbac.yaml
kubectl apply -f manifests/configmap.yaml
kubectl apply -f manifests/podcliqueset.yaml
```

The [example PodCliqueSet](manifests/podcliqueset.yaml) has two PodCliques, **pca** and **pcb**, and both include the **restart-watcher** sidecar so that incrementing `restartGeneration` restarts all 6 Pods. If you only want to restart one PodClique, add the restart-watcher sidecar only to that clique’s `podSpec` in the manifest; Pods without the sidecar will not react to the ConfigMap.

### 4. Wait for Pods to be ready

The PodCliqueSet has **pca** (replicas=2) and **pcb** (replicas=4), 6 Pods in total:

```bash
kubectl get podcliqueset -n grove-restart-demo
kubectl get pods -n grove-restart-demo -l app.kubernetes.io/part-of=grove-restart-demo-pcs -o wide
```

Confirm all 6 Pods are `Running`.

### 5. (Optional) Record Pod name, Pod ID, and IP

To compare before and after the trigger (names, UIDs, and IPs should stay the same):

```bash
kubectl get pods -n grove-restart-demo -l app.kubernetes.io/part-of=grove-restart-demo-pcs \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.metadata.uid}{"\t"}{.status.podIP}{"\n"}{end}'
```

### 6. Trigger one “full PodCliqueSet in-place restart”

Increment the ConfigMap `grove-restart-control` key `restartGeneration`:

```bash
# Get current value
current=$(kubectl get configmap grove-restart-control -n grove-restart-demo -o jsonpath='{.data.restartGeneration}')
next=$((current + 1))

# Patch with next value
kubectl patch configmap grove-restart-control -n grove-restart-demo \
  --type merge \
  -p "{\"data\":{\"restartGeneration\":\"${next}\"}}"
```

Within the next poll interval (default 5 seconds), all Pods with the restart-watcher will see the new value, exit with 88, and trigger RestartAllContainers for their Pod.

### 7. Observe

After about 10–20 seconds:

```bash
kubectl get pods -n grove-restart-demo -l app.kubernetes.io/part-of=grove-restart-demo-pcs -o wide
```

**Expected**:

- Pod **names, UIDs and IPs unchanged** (no new Pods created or deleted).
- **Restart counts** increased (e.g. `kubectl get pod <name> -n grove-restart-demo -o jsonpath='{range .status.containerStatuses[*]}{.name} restarts={.restartCount}{"\n"}{end}'`).

To trigger again, repeat step 6 (increment `restartGeneration` again).

### 8. Cleanup

```bash
kubectl delete -f manifests/podcliqueset.yaml
kubectl delete -f manifests/configmap.yaml
kubectl delete -f manifests/rbac.yaml
kubectl delete -f manifests/namespace.yaml
```

## Environment variables (restart-watcher)

| Variable | Meaning | Default |
|----------|---------|--------|
| `CM_NAMESPACE` | ConfigMap namespace | Prefer `metadata.namespace` via fieldRef |
| `CM_NAME` | ConfigMap name | `grove-restart-control` |
| `KEY_NAME` | Key name | `restartGeneration` |
| `POLL_INTERVAL_SECONDS` | Poll interval (seconds) | `5` |
| `TRIGGER_EXIT_CODE` | Exit code that triggers RestartAllContainers | `88` |

## References

- Kubernetes 1.35: [Restart All Containers](https://kubernetes.io/blog/2026/01/02/kubernetes-v1-35-restart-all-containers/), [KEP-5532](https://kep.k8s.io/5532)

# Installation

To install Grove, you can choose one of the following options:
- Install Grove from the published Helm charts under the [GitHub packages section](https://github.com/orgs/ai-dynamo/packages?repo_name=grove).
- Build from source and install Grove using the `make` targets we provide as a part of the repository.

## Install Grove from published packages

You can directly install Grove in your cluster using the published [`grove-charts`](https://github.com/ai-dynamo/grove/pkgs/container/grove%2Fgrove-charts) Helm packages.
Locate the [release tag](https://github.com/ai-dynamo/grove/releases) to install.
Set the `KUBECONFIG` in your shell session, and run the following:

```bash
helm upgrade -i grove oci://ghcr.io/ai-dynamo/grove/grove-charts:<tag>
```

## Build and install Grove from source (*for developers*)

You can build and deploy Grove to your local kind cluster or remote cluster using the provided `make` targets in the repository.
All grove operator `make` targets are located in [Operator Makefile](../operator/Makefile).

### Local Kind cluster set-up

In case you wish to develop Grove using a local [kind](https://kind.sigs.k8s.io/) cluster or are following along with our tutorials on your local machine, please do the following:

- **Navigate to the operator directory:**

  ```bash
  cd operator
  ```

- **Set up a KIND cluster with local docker registry:**

  ```bash
  make kind-up
  ```

- **Optional**: To create a KIND cluster with fake nodes for testing at scale, specify the number of fake nodes:

  ```bash
  # Create a cluster with 20 fake nodes
  make kind-up FAKE_NODES=20
  ```

  This will automatically install [KWOK](https://kwok.sigs.k8s.io/) (Kubernetes WithOut Kubelet) and create the specified number of fake nodes. These fake nodes are tainted with `fake-node=true:NoSchedule`, so you'll need to add the following toleration to your pod specs to schedule on them:

  ```yaml
  tolerations:
  - key: fake-node
    operator: Exists
    effect: NoSchedule
  ```

- Specify the `KUBECONFIG` environment variable in your shell session to the path printed out at the end of the previous step:

  ```bash
  # You would see something like `export KUBECONFIG=/path-to-your-grove-clone/grove/operator/hack/kind/kubeconfig` printed.
  # If you are already in `/path-to-your-grove-clone/grove/operator`, then you can simply:
  export KUBECONFIG=./hack/kind/kubeconfig
  ```

### Remote cluster set-up

If you wish to use your own Kubernetes cluster instead of the local KIND cluster, follow these steps:

- **Set the KUBECONFIG environment variable** to point to your Kubernetes cluster configuration:

  ```bash
  # Set KUBECONFIG to use your Kubernetes cluster kubeconfig
  export KUBECONFIG=/path/to/your/kubernetes/kubeconfig
  ```

- **Set the CONTAINER_REGISTRY environment variable** to specify your container registry:

  ```bash
  # Set a container registry to push your images to
  export CONTAINER_REGISTRY=your-container-registry
  ```

### Installation using make targets

> **Important:** All commands in this section must be run from the `operator/` directory.

```bash
# Navigate to the operator directory (if not already there)
cd operator

# Optional: Deploy to a custom namespace
export NAMESPACE=custom-ns

# Deploy Grove operator and all resources
make deploy
```

This make target installs all relevant CRDs, builds `grove-operator`, `grove-initc`, and deploys the operator to the cluster.
You can configure the Grove operator by modifying the [values.yaml](../operator/charts/values.yaml).

This make target leverages Grove [Helm](https://helm.sh/) charts and [Skaffold](https://skaffold.dev/) to install the following resources to the cluster:

- [CRDs](../operator/charts):
  - Grove operator CRD - `podcliquesets.grove.io`, `podcliques.grove.io` and `podcliquescalinggroups.grove.io`.
  - Grove Scheduler CRDs - `podgangs.scheduler.grove.io`.
- All Grove operator resources defined as a part of [Grove Helm chart templates](../operator/charts/templates).

## Certificate Management

By default, Grove automatically generates and manages TLS certificates for its webhook server. For production environments, you may want to use certificates from your organization's PKI or a certificate manager like cert-manager.

See the [Certificate Management Guide](user-guide/certificate-management.md) for detailed configuration options.

## Auto MNNVL (Multi-Node NVLink)

On clusters with NVIDIA MNNVL support, you can enable automatic Multi-Node NVLink for GPU workloads by setting `config.network.autoMNNVLEnabled: true` in the operator configuration (e.g. via Helm `--set config.network.autoMNNVLEnabled=true`). See the [Auto MNNVL user guide](user-guide/auto-mnnvl.md) for prerequisites, enabling the feature, and usage.

## Verify Installation

Follow the instructions in the [quickstart guide](quickstart.md) to deploy a PodCliqueSet and validate your installation.

## Troubleshooting

### Deployment Issues

#### `make deploy` fails with "No rule to make target 'deploy'"

**Cause:** You're running the command from the wrong directory.

**Solution:** Ensure you're in the `operator/` directory:
```bash
cd operator
make deploy
```

#### `make deploy` fails with "unable to connect to Kubernetes"

**Cause:** The `KUBECONFIG` environment variable is not set correctly.

**Solution:** Export the kubeconfig for your kind cluster:
```bash
kind get kubeconfig --name grove-test-cluster > hack/kind/kubeconfig
export KUBECONFIG=$(pwd)/hack/kind/kubeconfig
make deploy
```

#### Grove operator pod is in `CrashLoopBackOff`

**Cause:** Check the operator logs for specific errors.

**Solution:**
```bash
kubectl logs -l app.kubernetes.io/name=grove-operator
```

### Runtime Issues

#### Pods stuck in `Pending` state

**Cause:** Gang scheduling requirements might not be met, or there aren't enough resources.

**Solution:**
1. Check PodGang status:
   ```bash
   kubectl get pg -o yaml
   ```
2. Check if MinAvailable requirements can be satisfied by your cluster resources
3. Check node resources:
   ```bash
   kubectl describe nodes
   ```

#### `kubectl scale` command fails with "not found"

**Cause:** The resource name might be incorrect.

**Solution:** List the actual resource names first:
```bash
# For PodCliqueScalingGroups
kubectl get pcsg

# For PodCliqueSets
kubectl get pcs
```

Then use the exact name from the output.

#### PodCliqueScalingGroup not auto-scaling

**Cause:** HPA might not be created or metrics-server might be missing.

**Solution:**
1. Verify HPA exists:
   ```bash
   kubectl get hpa
   ```
2. Check if metrics-server is running (required for HPA):
   ```bash
   kubectl get deployment metrics-server -n kube-system
   ```
3. For kind clusters, you may need to install metrics-server separately (*choose one of following methods*):
  * Use `operator/Makefile` target
  ```bash
  make deploy-addons
  ```
  * Manual setup
  ```bash
  kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
  ```

### Getting Help

If you encounter issues not covered here:
1. Check the [GitHub Issues](https://github.com/NVIDIA/grove/issues) for similar problems
2. Join the [Grove mailing list](https://groups.google.com/g/grove-k8s)
3. Start a [discussion thread](https://github.com/NVIDIA/grove/discussions)

## Supported Schedulers

Currently the following schedulers support gang scheduling of `PodGang`s created by the Grove operator:

- [NVIDIA/KAI-scheduler](https://github.com/NVIDIA/KAI-Scheduler)

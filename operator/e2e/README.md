# E2E Test Infrastructure

This directory contains the E2E test infrastructure for the Grove Operator, including dependency management for external components.

## Running E2E Tests

### Prerequisites

The following tools must be installed:
- **Docker** - For running containers and k3d
- **skaffold** (v2.x) - For deploying Grove operator
- **helm** - For deploying Helm charts
- **Go** (1.26.3+) - For running the tests

The following tools are nice to have
- **k3d** (v5.x) - For creating local Kubernetes clusters

On macOS with Homebrew:
```bash
brew install docker skaffold helm go
```
To install k3d
```bash
brew install k3d
```


### Running Locally

From the repository root:
```bash
make run-e2e
```

Or directly from the operator directory:
```bash
cd operator
make run-e2e
```

The test suite will:
1. Create a k3d cluster with 28 worker nodes
2. Install Grove, Kai Scheduler, and GPU Operator
3. Run all e2e testing suites
4. Clean up the cluster

### Native Workload API Scheduling (WAS)

From `operator/`, run the complete WAS suite:

```bash
make run-e2e-was-full
```

This target builds Grove from the current checkout, creates a Kubernetes 1.37 k3d cluster with three real workers, enables the native scheduling APIs and feature gates, deploys Grove with `default-scheduler.config.gangScheduling=true`, runs `Test_WAS[0-9]`, and deletes the cluster on success or failure. KAI, Kueue, and KWOK are disabled. The Helm chart supplies the required WAS permissions; no supplemental RBAC is needed.

The control plane enables `GenericWorkload` on kube-apiserver, kube-scheduler, and kube-controller-manager, and `CompositePodGroup` and `TopologyAwareWorkloadScheduling` on kube-apiserver and kube-scheduler. It serves `scheduling.k8s.io/v1beta1` and `scheduling.k8s.io/v1alpha3`. The k3s image is pinned by `K3S_IMAGE` in `operator/Makefile`.

The five scenarios cover hierarchy and Pod membership, gang admission with insufficient capacity, required topology with an ignored preferred constraint, whole-gang scaling and garbage collection, and coherent updates across anchor epochs. Same-epoch hierarchy contraction and recovery after external deletion are not covered by this suite.

`GROVE_E2E_REQUIRE_WAS=true` is set by the full target, so missing APIs fail the run instead of skipping it. To keep a cluster for debugging, use `make e2e-cluster-up-was`, then run `GROVE_E2E_REQUIRE_WAS=true make run-e2e TEST_PATTERN='^Test_WAS[0-9]' E2E_GO_TEST_FLAGS='-p 1'` with the same `KUBECONFIG`. Delete it with `make e2e-cluster-down`.

The default WAS kubeconfig is `$HOME/.kube/grove-was-e2e`, not the shared kubeconfig. For concurrent runs, choose an unused cluster name, dedicated kubeconfig, and unused host ports, and retain these exports for setup, testing, and cleanup:

```bash
export KUBECONFIG="$HOME/.kube/grove-was-my-test"
export E2E_CLUSTER__NAME=grove-was-my-test
export E2E_CLUSTER__API_PORT=16560
export E2E_CLUSTER__REGISTRY_PORT=15001
export E2E_CLUSTER__LB_PORT=18090:80
make run-e2e-was-full
```

Test manifests continue to use `registry:5001` inside the cluster. The setup maps that alias to the cluster's own registry, regardless of the host port.

Use Helm 3 with the existing Skaffold deployment path. Kubernetes nodes must be able to pull the k3s system images; a host-only proxy does not configure containerd inside the nodes.

Run `make test-unit-was` to exercise the native scheduling API contracts with the pinned Kubernetes 1.37 envtest binaries, and `make test-infra` for the setup configuration and command tests. The ordinary unit-test target still supports its existing envtest baseline.

### Running in CI/CD

The workflow in `.github/workflows/build-check-test.yaml` runs E2E on trusted `pull-request/*` branches when changes affect `operator/` or `.github/`. The `was` matrix entry invokes the same `make run-e2e-was-full` target used locally. It runs after build, unit tests, infrastructure tests, and repository checks succeed. The matching skip matrix reports status for changes that do not require E2E.

## Managing Dependencies

E2E test dependencies (container images and Helm charts) are managed in `dependencies.yaml`, similar to how Go dependencies are managed in `go.mod`.

### File: `dependencies.yaml`

This file defines all external dependencies used in E2E tests:

- **Container Images**: Images that are pre-pulled into the test cluster to speed up test execution
- **Helm Charts**: External Helm charts (Kai Scheduler, NVIDIA GPU Operator) with their versions and configuration

### Updating Dependencies

To update a dependency version:

1. Edit `e2e/dependencies.yaml`
2. Update the `version` field for the desired component
3. Run tests to verify: `cd e2e && go test -v`

#### Example: Updating Kai Scheduler

```yaml
helmCharts:
  kaiScheduler:
    releaseName: kai-scheduler
    chartRef: oci://ghcr.io/kai-scheduler/kai-scheduler/kai-scheduler
    version: v0.16.9  # <- Update this version
    namespace: kai-scheduler
```

#### Example: Adding a New Image to Pre-pull

```yaml
images:
  # ... existing images ...
  - name: docker.io/myorg/myimage
    version: v1.2.3
```

## Troubleshooting

### Stale k3d Cluster

If tests fail with cluster creation errors, clean up any existing cluster:
```bash
k3d cluster delete shared-e2e-test-cluster
```

### Test Timeout

E2E tests can take 10-15 minutes. If tests timeout, increase the timeout:
```bash
cd e2e && go test -tags=e2e ./tests/... -v -timeout 45m
```

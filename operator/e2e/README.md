# E2E Test Infrastructure

This directory contains the E2E test infrastructure for the Grove Operator, including dependency management for external components.

## Running E2E Tests

### Prerequisites

The following tools must be installed:
- **Docker** - For running containers and k3d
- **k3d** (v5.x) - For creating local Kubernetes clusters
- **skaffold** (v2.x) - For deploying Grove operator
- **helm** - For deploying Helm charts
- **Go** (1.24.5+) - For running the tests

On macOS with Homebrew:
```bash
brew install docker k3d skaffold helm go
```

### Running Locally

From the repository root:
```bash
make test-e2e
```

Or directly from the operator directory:
```bash
cd operator
make test-e2e
```

The test suite will:
1. Copy CRDs to Helm charts (via `prepare-charts.sh`)
2. Create a k3d cluster with 28 worker nodes
3. Install Grove, Kai Scheduler, and GPU Operator
4. Run all gang scheduling tests (~10-15 minutes)
5. Clean up the cluster

### Running in CI/CD

E2E tests are automatically run on GitHub Actions for:
- **All non-draft pull requests** to `main`
- **Draft pull requests** with the `run-e2e` label

To trigger e2e tests on a draft PR:
1. Add the `run-e2e` label to the pull request
2. The workflow will run automatically

The CI workflow is defined in `.github/workflows/e2e-test.yaml`.

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
    chartRef: oci://ghcr.io/nvidia/kai-scheduler/kai-scheduler
    version: v0.9.4  # <- Update this version
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

### Docker Authentication Errors

If you see errors like "Pull failed due to an unauthenticated request":
```bash
docker login
```

The e2e tests check for cached images before pulling, but some images may need to be pulled fresh.

### "No matches for kind PodCliqueSet" Error

This means CRDs weren't copied to the Helm charts. The `make test-e2e` target automatically runs `prepare-charts.sh`, but if you're running tests manually:
```bash
./hack/prepare-charts.sh
cd e2e && go test -tags=e2e ./tests/... -v
```

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

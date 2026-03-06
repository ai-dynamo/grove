# autoMNNVL E2E Test Scripts

Scripts for running the autoMNNVL (Multi-Node NVLink) end-to-end tests locally.

These tests validate the operator's MNNVL feature across 4 configurations:

| Order | ComputeDomain CRD | Feature Flag | Description |
|-------|-------------------|--------------|-------------|
| 1st | Unsupported (no GPU) | Disabled | Baseline (matches initial cluster state) |
| 2nd | Unsupported (no GPU) | Enabled | Operator fails to start (expected) |
| 3rd | Supported (fake GPU) | Enabled | Main feature path |
| 4th | Supported (fake GPU) | Disabled | Feature off, CRD present |

## What These Tests Cover (and Don't)

These tests exercise the **operator's control plane behavior** around MNNVL —
they do **not** test actual DRA scheduling or GPU fabric connectivity.

- The [fake-gpu-operator](https://github.com/run-ai/fake-gpu-operator) provides
  only the `ComputeDomain` CRD schema; no real DRA driver is running.
- The operator's reconciliation logic (creating ComputeDomain CRs, injecting
  `resourceClaim` references into PodSpecs) only requires the CRD to be
  registered in the API server, not a functioning driver.
- Test pods are expected to stay **Pending** — there is no GPU hardware or DRA
  driver to fulfill the claims.

## Architecture

Cluster lifecycle is handled by the **Makefile** (`make run-e2e-mnnvl-full`),
which creates a lightweight k3d cluster, runs the Python test orchestrator, and
cleans up. The Python scripts only handle **configuration** (via
`config-cluster.py`) and **test execution** — they assume an existing cluster.

In CI, the `auto_mnnvl` entry in the `e2e` matrix triggers the same Makefile
target.

## Quick Start

Run all 4 configurations end-to-end (cluster setup, tests, teardown):

```bash
# From the operator/ directory
make run-e2e-mnnvl-full
```

## Scripts

| Script | Description |
|--------|-------------|
| `run_autoMNNVL_e2e_all.py` | Run all 4 configurations sequentially (expects existing cluster) |
| `run_autoMNNVL_e2e.py` | Run a single configuration (configure + test, expects existing cluster) |
| `../infra-manager.py` | Unified CLI for cluster infrastructure management |
| `../config-cluster.py` | Declaratively configure fake GPU + MNNVL on an existing cluster |

## Usage Examples

```bash
# Run all configs via Makefile (recommended — handles cluster lifecycle)
make run-e2e-mnnvl-full

# Or, manage cluster manually and run tests separately:

# 1. Create the MNNVL cluster (lightweight: 2 workers, skip image prepull only)
E2E_WORKER_NODES=2 E2E_K3S_IMAGE=rancher/k3s:v1.34.2-k3s1 make e2e-cluster-up E2E_CREATE_FLAGS="--skip-prepull"

# 2. Push alpine image for test workloads
docker pull alpine:latest && docker tag alpine:latest localhost:5001/alpine:latest && docker push localhost:5001/alpine:latest

# 3. Run all configs on the existing cluster
python3 ./hack/e2e-autoMNNVL/run_autoMNNVL_e2e_all.py

# 4. Or run a single config
python3 ./hack/e2e-autoMNNVL/run_autoMNNVL_e2e.py --fake-gpu=yes --auto-mnnvl=enabled

# 5. Configure an existing cluster directly (without running tests)
python3 ./hack/config-cluster.py --fake-gpu=yes --auto-mnnvl=enabled

# 6. Delete the cluster
make e2e-cluster-down
```

## Prerequisites

- Docker Desktop running
- `k3d` installed
- `kubectl` installed
- `helm` installed
- `skaffold` installed
- Go 1.25+

## Cluster Details

- **Cluster name:** `shared-e2e-test-cluster` (same as standard e2e)
- **Nodes:** 1 server + 2 agents (lightweight — standard e2e uses 30)
- **Registry:** local registry on port 5001
- **Skaffold profile:** `topology-test` (same as standard e2e; Kai and topology are installed, only worker count and prepull are reduced)
- **Fake GPU:** [fake-gpu-operator](https://github.com/run-ai/fake-gpu-operator) v0.0.72 (provides ComputeDomain CRD)

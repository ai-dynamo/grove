# autoMNNVL E2E Test Scripts

Scripts for running the autoMNNVL (Multi-Node NVLink) end-to-end tests locally.

These tests validate the operator's MNNVL feature across 4 configurations:

| Config | ComputeDomain CRD | Feature Flag | Description |
|--------|-------------------|--------------|-------------|
| 1 | Supported (fake GPU) | Enabled | Main feature path |
| 2 | Supported (fake GPU) | Disabled | Feature off, CRD present |
| 3 | Unsupported (no GPU) | Enabled | Operator fails to start (expected) |
| 4 | Unsupported (no GPU) | Disabled | Baseline, no MNNVL artifacts |

## Quick Start

Run all 4 configurations end-to-end (build, cluster setup, tests, teardown):

```bash
# From the operator/ directory
python3 ./hack/e2e-autoMNNVL/run_autoMNNVL_e2e_all.py
```

## Scripts

### Python (recommended)

| Script | Description |
|--------|-------------|
| `run_autoMNNVL_e2e_all.py` | Run all 4 configurations sequentially |
| `run_autoMNNVL_e2e.py` | Run a single configuration (setup + tests) |
| `setup_autoMNNVL_cluster.py` | Set up a k3d cluster with configurable options |

### Shell (equivalent)

| Script | Description |
|--------|-------------|
| `run-autoMNNVL-e2e-all.sh` | Run all 4 configurations sequentially |
| `run-autoMNNVL-e2e.sh` | Run a single configuration (setup + tests) |
| `setup-autoMNNVL-cluster.sh` | Set up a k3d cluster with configurable options |

## Usage Examples

```bash
# Run all configs (full matrix)
python3 ./hack/e2e-autoMNNVL/run_autoMNNVL_e2e_all.py

# Run all configs, skip the initial image build
python3 ./hack/e2e-autoMNNVL/run_autoMNNVL_e2e_all.py --skip-build

# Run all configs, keep cluster alive after tests
python3 ./hack/e2e-autoMNNVL/run_autoMNNVL_e2e_all.py --keep-cluster

# Run a single config: supported + enabled
python3 ./hack/e2e-autoMNNVL/run_autoMNNVL_e2e.py --with-fake-gpu --mnnvl-enabled

# Run a single config: unsupported + disabled, reuse existing cluster
python3 ./hack/e2e-autoMNNVL/run_autoMNNVL_e2e.py --without-fake-gpu --mnnvl-disabled --skip-cluster-create

# Just set up the cluster (no tests)
python3 ./hack/e2e-autoMNNVL/setup_autoMNNVL_cluster.py --with-fake-gpu --mnnvl-enabled

# Tear down the cluster
python3 ./hack/e2e-autoMNNVL/setup_autoMNNVL_cluster.py --shutdown
```

## Prerequisites

- Docker Desktop running
- `k3d` installed
- `kubectl` installed
- `helm` installed
- Go 1.25+

## Cluster Details

- **Cluster name:** `mnnvl-test-cluster`
- **Nodes:** 1 server + 2 agents
- **Registry:** local registry on port 5001
- **k3s version:** v1.34.2-k3s1
- **Fake GPU:** [fake-gpu-operator](https://github.com/run-ai/fake-gpu-operator) v0.0.72 (provides ComputeDomain CRD)

# Hack Scripts

This directory contains utility scripts for Grove operator development and testing.

## Directory Structure

```
hack/
├── infra-manager.py          # Primary CLI for cluster infrastructure management
├── config-cluster.py         # Declarative cluster configuration (fake GPU, MNNVL)
├── requirements.txt          # Python dependencies
├── infra_manager/            # Python package with modular cluster management
│   ├── __init__.py
│   ├── cluster.py            # k3d cluster operations
│   ├── components.py         # Kai, Grove, Pyroscope installation
│   ├── config.py             # Configuration models
│   ├── constants.py          # Constants and dependency loading
│   ├── kwok.py               # KWOK simulated node management
│   ├── orchestrator.py       # Workflow orchestration
│   ├── utils.py              # Shared utilities
│   ├── dependencies.yaml     # Centralized dependency versions
│   └── pyroscope-values.yaml # Pyroscope Helm values
├── e2e-autoMNNVL/            # Auto-MNNVL E2E test runners
├── kind/                     # Kind cluster configuration
├── build-operator.sh         # Build operator image
├── build-initc.sh            # Build init container image
├── docker-build.sh           # Docker build helper
├── deploy.sh                 # Deploy operator
├── deploy-addons.sh          # Deploy addon components
├── prepare-charts.sh         # Prepare Helm charts
├── kind-up.sh                # Create Kind cluster
└── kind-down.sh              # Delete Kind cluster
```

## Python Scripts

### infra-manager.py (Primary)

Unified CLI for Grove infrastructure management. Delegates to the `infra_manager` package.

**Installation:**

```bash
pip3 install -r hack/requirements.txt
```

**Usage:**

```bash
# Full e2e setup
./hack/infra-manager.py setup e2e

# View all options
./hack/infra-manager.py --help

# Delete the cluster
./hack/infra-manager.py delete k3d-cluster

# Skip specific components
./hack/infra-manager.py setup e2e --skip-grove
./hack/infra-manager.py setup e2e --skip-kai --skip-prepull

# Scale test setup with KWOK simulated nodes
./hack/infra-manager.py setup scale --kwok-nodes 1000

# Install individual components
./hack/infra-manager.py install grove --profiling
./hack/infra-manager.py install pyroscope
```

### config-cluster.py

Declarative configuration for an existing E2E cluster. Supports fake GPU operator
and auto-MNNVL toggle.

```bash
./hack/config-cluster.py --fake-gpu=yes --auto-mnnvl=enabled
```

### Environment Variables

All configuration can be overridden via `E2E_*` environment variables (used by `infra-manager.py`):

**Cluster (K3dConfig):**

- `E2E_CLUSTER_NAME` - Cluster name (default: `shared-e2e-test-cluster`)
- `E2E_REGISTRY_PORT` - Registry port (default: `5001`)
- `E2E_API_PORT` - Kubernetes API port (default: `6560`)
- `E2E_LB_PORT` - Load balancer port mapping (default: `8090:80`)
- `E2E_WORKER_NODES` - Number of worker nodes (default: `30`)
- `E2E_WORKER_MEMORY` - Memory per worker node (default: `150m`)
- `E2E_K3S_IMAGE` - K3s container image (default: `rancher/k3s:v1.33.5-k3s1`)
- `E2E_MAX_RETRIES` - Max retries for cluster operations (default: `3`)

**Components (ComponentConfig):**

- `E2E_KAI_VERSION` - Kai Scheduler version (default: from `dependencies.yaml`)
- `E2E_SKAFFOLD_PROFILE` - Skaffold profile for Grove (default: `topology-test`)
- `E2E_GROVE_NAMESPACE` - Grove operator namespace (default: `grove-system`)
- `E2E_REGISTRY` - Container registry override (default: none)

**KWOK / Observability (KwokConfig):**

- `E2E_KWOK_NODES` - Number of KWOK simulated nodes (default: none)
- `E2E_KWOK_BATCH_SIZE` - Batch size for KWOK node creation (default: `150`)
- `E2E_KWOK_NODE_CPU` - CPU capacity per KWOK node (default: `64`)
- `E2E_KWOK_NODE_MEMORY` - Memory capacity per KWOK node (default: `512Gi`)
- `E2E_KWOK_MAX_PODS` - Max pods per KWOK node (default: `110`)
- `E2E_PYROSCOPE_NS` - Pyroscope namespace (default: `pyroscope`)

## Shell Scripts

Other scripts in this directory are bash scripts that handle building, deploying, and managing the Grove operator.

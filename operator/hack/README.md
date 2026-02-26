# Hack Scripts

This directory contains utility scripts for Grove operator development and testing.

## Directory Structure

```
hack/
├── infra-manager.py          # Primary entry point for cluster infrastructure management
├── create-cluster.py         # Legacy monolith (kept for reference)
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

Modular cluster setup for Grove E2E testing. Delegates to the `infra_manager` package.

**Installation:**

```bash
pip3 install -r hack/requirements.txt
```

**Usage:**

```bash
# Full e2e setup (default — no flags needed)
./hack/infra-manager.py

# View all options
./hack/infra-manager.py --help

# Delete the cluster
./hack/infra-manager.py --delete

# Skip specific components
./hack/infra-manager.py --skip-grove
./hack/infra-manager.py --skip-kai --skip-prepull

# KWOK simulated nodes
./hack/infra-manager.py --kwok-nodes 1000

# Pyroscope profiling
./hack/infra-manager.py --pyroscope
```

### config-cluster.py

Declarative configuration for an existing E2E cluster. Supports fake GPU operator
and auto-MNNVL toggle.

```bash
./hack/config-cluster.py --fake-gpu=yes --auto-mnnvl=enabled
```

**Environment Variables:**

All configuration can be overridden via `E2E_*` environment variables:

- `E2E_CLUSTER_NAME` - Cluster name (default: shared-e2e-test-cluster)
- `E2E_REGISTRY_PORT` - Registry port (default: 5001)
- `E2E_API_PORT` - Kubernetes API port (default: 6560)
- `E2E_WORKER_NODES` - Number of worker nodes (default: 30)
- `E2E_KAI_VERSION` - Kai Scheduler version (from dependencies.yaml)
- `E2E_SKAFFOLD_PROFILE` - Skaffold profile for Grove (default: topology-test)

## Shell Scripts

Other scripts in this directory are bash scripts that handle building, deploying, and managing the Grove operator.

# Hack Scripts

This directory contains utility scripts for Grove operator development and testing.

## Python Scripts

### create-e2e-cluster.py

Creates and configures a k3d cluster for E2E testing with **parallel image pre-pulling** for faster cluster startup.

**Features:**
- ✨ **Parallel image pre-pulling** - Pulls 7 Kai Scheduler images in parallel (~45s instead of 3.5min)
- 🎨 **Beautiful terminal output** - Progress bars and colored status messages
- ⚡ **Fast cluster creation** - Images are pre-loaded into local registry
- 🔧 **Configurable** - All settings via environment variables
- 🛡️ **Type-safe** - Pydantic models with validation

**Installation:**

```bash
# Install Python dependencies (one-time setup)
pip3 install -r requirements.txt

# Or using a virtual environment (recommended)
python3 -m venv venv
source venv/bin/activate  # On Windows: venv\Scripts\activate
pip install -r requirements.txt
```

**Usage:**

```bash
# Create a cluster with all components (includes image pre-pulling)
./hack/e2e-cluster/create-e2e-cluster.py

# View all options
./hack/e2e-cluster/create-e2e-cluster.py --help

# Delete the cluster
./hack/e2e-cluster/create-e2e-cluster.py --delete

# Skip specific components
./hack/e2e-cluster/create-e2e-cluster.py --skip-grove
./hack/e2e-cluster/create-e2e-cluster.py --skip-kai

# Skip image pre-pulling (faster script start, but slower cluster startup)
./hack/e2e-cluster/create-e2e-cluster.py --skip-prepull
```

**Environment Variables:**

All configuration can be overridden via environment variables:

```bash
export E2E_CLUSTER_NAME=my-cluster
export E2E_WORKER_NODES=50
export E2E_KAI_VERSION=v0.14.0
./hack/e2e-cluster/create-e2e-cluster.py
```

Available variables:
- `E2E_CLUSTER_NAME` - Cluster name (default: shared-e2e-test-cluster)
- `E2E_REGISTRY_PORT` - Registry port (default: 5001)
- `E2E_API_PORT` - Kubernetes API port (default: 6560)
- `E2E_LB_PORT` - Load balancer port mapping (default: 8090:80)
- `E2E_WORKER_NODES` - Number of worker nodes (default: 30)
- `E2E_WORKER_MEMORY` - Worker node memory (default: 150m)
- `E2E_K3S_IMAGE` - K3s image (default: rancher/k3s:v1.33.5-k3s1)
- `E2E_KAI_VERSION` - Kai Scheduler version (default: v0.13.0-rc1)
- `E2E_MAX_RETRIES` - Max cluster creation retries (default: 3)
- `E2E_SKAFFOLD_PROFILE` - Skaffold profile for Grove (default: topology-test)

**Image Pre-Pulling:**

The script pre-pulls the following Kai Scheduler images in parallel before installation:
- `ghcr.io/nvidia/kai-scheduler/admission`
- `ghcr.io/nvidia/kai-scheduler/binder`
- `ghcr.io/nvidia/kai-scheduler/operator`
- `ghcr.io/nvidia/kai-scheduler/podgroupcontroller`
- `ghcr.io/nvidia/kai-scheduler/podgrouper`
- `ghcr.io/nvidia/kai-scheduler/queuecontroller`
- `ghcr.io/nvidia/kai-scheduler/scheduler`

**Performance:**
- Without pre-pull: ~3-7 minutes for images to pull during pod startup
- With pre-pull: ~45 seconds for parallel pre-pull, then instant pod startup

## Shell Scripts

Other scripts in this directory are bash scripts that handle building, deploying, and managing the Grove operator.

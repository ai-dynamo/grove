> [!NOTE]
>
> :construction_worker: `This project site is currently under active construction, keep watching for announcements as we approach alpha launch!`

# Grove

Modern AI inference workloads need capabilities that Kubernetes doesn't provide out-of-the-box:

- **Gang scheduling** - Prefill and decode pods must start together or not at all
- **Grouped scaling** - Tightly-coupled components that need to scale as a unit
- **Startup ordering** - Different components in a workload which must start in an explicit ordering
- **Topology-aware placement** - NVLink-connected GPUs or workloads shouldn't be scattered across nodes

Grove is a Kubernetes API that provides a single declarative interface for orchestrating any AI inference workload — from simple, single-pod deployments to complex multi-node, disaggregated systems.

**One API. Any inference architecture.**

## Quick Start

Get Grove running in 5 minutes:

```bash
# 1. Create a local kind cluster
cd operator && make kind-up

# 2. Deploy Grove
kind get kubeconfig --name grove-test-cluster > hack/kind/kubeconfig
export KUBECONFIG=$(pwd)/hack/kind/kubeconfig
make deploy

# 3. Deploy your first workload
kubectl apply -f samples/simple/simple1.yaml

# 4. Watch it scale
kubectl get pcs,pclq,pcsg,pg,pod -owide
```

**→ [Full Quickstart Guide](docs/quickstart.md)** | **[Installation Docs](docs/installation.md)**

## What Grove Solves

Grove handles the complexities of modern AI inference deployments:

| Your Setup | What Grove Does |
|------------|-----------------|
| **Disaggregated inference** (prefill + decode) | Gang schedules all components together, scales them independently and as a unit |
| **Multi-model pipelines** | Enforces startup order (router → workers), auto-scales each stage |
| **Multi-node inference** (DeepSeek-R1, Llama 405B) | Packs pods onto NVLink-connected GPUs for optimal network performance |
| **Simple single-pod serving** | Works for this too! One API for any architecture |

**Use Cases:** [Multi-node disaggregated](docs/assets/multinode-disaggregated.excalidraw.png) · [Single-node disaggregated](docs/assets/singlenode-disaggregated.excalidraw.png) · [Agentic pipelines](docs/assets/agentic-pipeline.excalidraw.png) · [Standard serving](docs/assets/singlenode-aggregated.excalidraw.png)

## How It Works

Grove introduces four simple concepts:

| Concept | What It Does |
|---------|--------------|
| **PodCliqueSet** | Your entire workload (e.g., "my-inference-stack") |
| **PodClique** | A component role (e.g., "prefill", "decode", "router") |
| **PodCliqueScalingGroup** | Components that must scale together (e.g., prefill + decode) |
| **PodGang** | Internal scheduler primitive for gang scheduling (you don't touch this) |

**→ [API Reference](docs/api-reference/operator-api.md)**

## Roadmap

### 2025 Priorities

> **Note:** We are aligning our release schedule with [NVIDIA Dynamo](https://github.com/ai-dynamo/dynamo) to ensure seamless integration. Release dates will be updated once our cadence (e.g., weekly, monthly) is finalized.

**Q4 2025**
- Topology-Aware Scheduling
- Multi-Level Horizontal Auto-Scaling
- Startup Ordering
- Rolling Updates

**Q1 2026**
- Resource-Optimized Rolling Updates
- Multi-Node NVLink Auto-Scaling Support
- Automatic MNNVL Discovery
- User Guides for All Supported Features

**Q2 2026**
- Terminal States for Training and Batch Workloads
- Scheduler Plugin Architecture (with Support for Third-Party Scheduler Backends)
- kubectl-grove CLI
- Advanced Topology Scheduling

## Contributions

Please read the [contribution guide](CONTRIBUTING.md) before creating you first PR!

## Community, Discussion, and Support

Grove is an open-source project and we welcome community engagement!

Please feel free to start a [discussion thread](https://github.com/NVIDIA/grove/discussions) if you want to discuss a topic of interest.

In case, you have run into any issue or would like a feature enhancement, please create a [GitHub Issue](https://github.com/NVIDIA/grove/issues) with the appropriate tag.

To directly reach out to the Grove user and developer community, please join the [Grove mailing list](https://groups.google.com/g/grove-k8s).

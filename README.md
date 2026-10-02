# Grove
[![Go Report Card](https://goreportcard.com/badge/github.com/ai-dynamo/grove/operator)](https://goreportcard.com/report/github.com/NVIDIA/grove/operator)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![GitHub Release](https://img.shields.io/github/v/release/ai-dynamo/grove)](https://github.com/ai-dynamo/grove/releases/latest)
[![Discord](https://dcbadge.limes.pink/api/server/D92uqZRjCZ?style=flat)](https://discord.gg/UxcbxEYqS4)

**One workload API. Any AI workload. Any scheduler.**

Grove is a Kubernetes workload API for AI systems across training, inference, and hybrid pipelines (e.g. RL). Grove's primitives are framework-agnostic: a controller, adapter, or platform layer can translate workload intent from systems such as Ray, Dynamo, llm-d, NeMo-RL, Megatron-LM, or Kubeflow Trainer into [PodCliqueSet](operator/api/core/v1alpha1/podcliqueset.go), and Grove compiles that intent into scheduler-compatible `PodGang` resources composed of `PodGroup`s. From one declarative spec, Grove coordinates hierarchical gang scheduling, topology-aware placement, multi-level autoscaling, and explicit startup ordering, so platform teams can support flexible framework requirements for AI/ML workloads without stitching together scheduler-specific controllers, YAML, or scripts.

## Quick Start on Local Kind Cluster

Get Grove running in 5 minutes on a local [kind](https://kind.sigs.k8s.io/) cluster.

```bash
# 1. Create a local kind cluster
cd operator && make kind-up

# 2. Deploy Grove
make deploy

# 3. Deploy your first workload
kubectl apply -f samples/simple/simple1.yaml

# 4. Fetch the resources created by grove
kubectl get pcs,pclq,pcsg,pg,pod -o wide
```

Follow along with this example in the
**→ [Quickstart Doc](docs/quickstart.md)**

For more install options including local and remote K8s clusters, see the
**→ [Installation Docs](docs/installation.md)**

## Motivation

Modern AI workloads need orchestration capabilities that Kubernetes doesn't provide out of the box:

- **Scaling for multi-node, multi-pod units** - Large model instances, training jobs, and hybrid pipelines may span multiple pods across multiple nodes. In these cases, the scaling unit is no longer an individual pod, but a group of pods that form one functional unit.
- **Hierarchical gang scheduling** - Distributed jobs require the right pods to be scheduled together; otherwise resources sit idle and the workload can deadlock. Grove supports gang semantics at multiple levels, from leader/worker training jobs to disaggregated inference pipelines with prefill, decode, routing, and other roles.
- **Startup ordering** - Even when components are scheduled together, they may need to start in a specific order. MPI workloads, parameter-server jobs, and leader/worker model instances often require workers to be ready before the coordinator launches the application.
- **Topology-aware placement** - AI workloads communicate heavily across components. Network-optimized placement, e.g. within NVLink domains, is crucial to reduce communication overhead and improve performance.
- **Network-aware GPU setup** - Multi-node GPU workloads often need more than placement; when enabled and opted into, Grove can automate MNNVL-related resource wiring for high-bandwidth GPU communication.

## How It Works

Grove introduces four simple concepts for expressing application workload intent:

| Concept                                                             | Description                                                                                                                                                                                              |
|---------------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| [PodClique](operator/api/core/v1alpha1/podclique.go)                | A group of pods representing a specific role (e.g., leader, worker, frontend). Each clique has an independent configuration and supports custom scaling logic.                                           |
| [PodCliqueScalingGroup](operator/api/core/v1alpha1/scalinggroup.go) | A set of PodCliques that scale and are scheduled together as a gang. Ideal for tightly coupled roles like training coordinator and workers, or prefill leader and worker.                                |
| [PodCliqueSet](operator/api/core/v1alpha1/podcliqueset.go)          | The top-level Grove object that defines a workload composed of colocated components. Also supports autoscaling with topology-aware spread of PodCliqueSet replicas for availability.                     |
| [PodGang](scheduler/api/core/v1alpha1/podgang.go)                   | The scheduler API that defines a unit of gang-scheduling. A PodGang is a collection of groups of similar pods, where each pod group defines a minimum number of replicas guaranteed for gang-scheduling. |

Get started with a step-by-step hands-on Grove tutorial here
**→ [Core Concepts Overview](docs/user-guide/01_core-concepts/01_overview.md)**

Refer to all Grove APIs here
**→ [API Reference](docs/api-reference/operator-api.md)**

## Example Use Cases

- **Multi-Node, Disaggregated Inference for large models** ***(Dynamo, llm-d, SGLang, etc.)*** : [Visualization](docs/assets/multinode-disaggregated.excalidraw.png)
- **Distributed Training Jobs** ***(Megatron-LM, PyTorch Distributed, Kubeflow Trainer, etc.)***
- **RL and Hybrid Training/Serving Pipelines** ***(NeMo-RL, Ray, verl, etc.)***
- **Single-Node, Disaggregated Inference** : [Visualization](docs/assets/singlenode-disaggregated.excalidraw.png)
- **Agentic Pipeline of Models** : [Visualization](docs/assets/agentic-pipeline.excalidraw.png)
- **Standard Aggregated Single Node or Single GPU Inference** : [Visualization](docs/assets/singlenode-aggregated.excalidraw.png)

## Roadmap

### 2026 Priorities

- Resource-Optimized Rolling Updates
- Scheduler Backend Framework
- Third-party CRD support
- Topology Spread Constraints
- Automatic Topology Detection
- And More!

## Contributions

Please read the [contribution guide](CONTRIBUTING.md) before creating you first PR!

## Community, Discussion, and Support

Grove is an open-source project and we welcome community engagement!

Please feel free to start a [discussion thread](https://github.com/ai-dynamo/grove/discussions) if you want to discuss a topic of interest.

In case, you have run into any issue or would like a feature enhancement, please create a [GitHub Issue](https://github.com/ai-dynamo/grove/issues) with the appropriate tag.

To directly reach out to the Grove user and developer community, please join the [#grove channel on CNCF Slack](https://slack.cncf.io/), or [Grove mailing list](https://groups.google.com/g/grove-k8s).

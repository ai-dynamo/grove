# Grove
[![Go Report Card](https://goreportcard.com/badge/github.com/ai-dynamo/grove/operator)](https://goreportcard.com/report/github.com/NVIDIA/grove/operator)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![GitHub Release](https://img.shields.io/github/v/release/ai-dynamo/grove)](https://github.com/ai-dynamo/grove/releases/latest)
[![Discord](https://dcbadge.limes.pink/api/server/D92uqZRjCZ?style=flat)](https://discord.gg/UxcbxEYqS4)

**One API. Any inference architecture.**

Grove is a Kubernetes API that provides a single declarative interface for orchestrating any AI inference workload — from simple, single-pod deployments to complex multi-node, disaggregated systems. Grove lets you scale your multinode inference deployment from a single replica to data center scale, supporting tens of thousands of GPUs. It allows you to describe your whole inference serving system in Kubernetes - e.g. prefill, decode, routing or any other component - as a single Custom Resource (CR). From that one spec, the platform coordinates hierarchical gang scheduling, topology‑aware placement, multi-level autoscaling and explicit startup ordering. You get precise control of how the system behaves without stitching together scripts, YAML files, or custom controllers.

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

Modern AI inference workloads need capabilities that Kubernetes natively doesn't provide out-of-the-box:

- **Scaling for Multi-Node/Multi-Pod Units** - Large models may be sharded across multiple nodes, meaning a single model instance spans multiple pods. In this case, the fundamental scaling unit is no longer an individual pod, but an entire group of pods that together form one model instance.
- **Hierarchical Gang scheduling** - Multi-node model instances require pods to be scheduled together; if only a subset of the required pods are scheduled, the model is unusable, resources remain idle, and the system can deadlock waiting for the remaining pods. Disaggregated inference has similar constraints: at least one prefill instance and one decode instance must be scheduled to form a functional pipeline. Therefore, gang scheduling must occur at multiple levels, ensuring required components start together as an all-or-nothing unit.
- **Startup ordering** - Even when components must be scheduled together (e.g., leader and worker pods in a multi-node model instance), there are cases where they must start in a specific order. For example, MPI workloads require all worker pods to be ready before the leader pod launches the application. Explicit startup ordering ensures correct initialization and avoids failures caused by components starting out-of-order.
- **Topology-aware placement** - Components in an inference system often communicate heavily between each other. Network optimized placement, e.g. within NVLink domains, is crucial to minimize communication overheads and maximize performance.

## How It Works

Grove introduces four simple concepts:

| Concept                                                             | Description                                                                                                                                                                                              |
|---------------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| [PodClique](operator/api/core/v1alpha1/podclique.go)                | A group of pods representing a specific role (e.g., leader, worker, frontend). Each clique has an independent configuration and supports custom scaling logic.                                           |
| [PodCliqueScalingGroup](operator/api/core/v1alpha1/scalinggroup.go) | A set of PodCliques that scale and are scheduled together as a gang. Ideal for tightly coupled roles like prefill leader and worker.                                                                     |
| [PodCliqueSet](operator/api/core/v1alpha1/podcliqueset.go)          | The top-level Grove object that defines a group of components managed and colocated together. Also supports autoscaling with topology aware spread of PodCliqueSet replicas for availability.            |
| [PodGang](scheduler/api/core/v1alpha1/podgang.go)                   | The scheduler API that defines a unit of gang-scheduling. A PodGang is a collection of groups of similar pods, where each pod group defines a minimum number of replicas guaranteed for gang-scheduling. |

Get started with a step-by-step hands-on Grove tutorial here
**→ [Core Concepts Overview](docs/user-guide/01_core-concepts/01_overview.md)**

Refer to all Grove APIs here
**→ [API Reference](docs/api-reference/operator-api.md)**

## Example Use Cases

- **Multi-Node, Disaggregated Inference for large models** ***(DeepSeek-R1, Llama-4-Maverick)*** : [Visualization](docs/assets/multinode-disaggregated.excalidraw.png)
- **Single-Node, Disaggregated Inference** : [Visualization](docs/assets/singlenode-disaggregated.excalidraw.png)
- **Agentic Pipeline of Models** : [Visualization](docs/assets/agentic-pipeline.excalidraw.png)
- **Standard Aggregated Single Node or Single GPU Inference** : [Visualization](docs/assets/singlenode-aggregated.excalidraw.png)

## Roadmap

### 2025 Priorities

- Hierarchical Gang Scheduling ✅
- Multi-Level Horizontal Auto-Scaling ✅
- Startup Ordering ✅
- Rolling Updates ✅
- Topology-Aware Scheduling

### 2026 Priorities

- Resource-Optimized Rolling Updates
- Topology Spread Constraints
- Automatic Topology Detection
- And More!

## Contributions

Please read the [contribution guide](CONTRIBUTING.md) before creating you first PR!

## Community, Discussion, and Support

Grove is an open-source project and we welcome community engagement!

Please feel free to start a [discussion thread](https://github.com/ai-dynamo/grove/discussions) if you want to discuss a topic of interest.

In case, you have run into any issue or would like a feature enhancement, please create a [GitHub Issue](https://github.com/ai-dynamo/grove/issues) with the appropriate tag.

To directly reach out to the Grove user and developer community, please join the [NVIDIA Dynamo Discord server](https://discord.gg/UxcbxEYqS4), or [Grove mailing list](https://groups.google.com/g/grove-k8s).

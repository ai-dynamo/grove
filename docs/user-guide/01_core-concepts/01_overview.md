# Grove Core Concepts Tutorial

This tutorial provides a comprehensive overview of Grove's core concepts: **PodClique**, **PodCliqueSet**, and **PodCliqueScalingGroup**. Through practical examples, you'll learn how to deploy and scale inference workloads from simple single-node setups to complex multi-node distributed systems. Since Grove's creation was motivated by inference the examples are tailored to inference but the core idea is to demonstrate how Grove's primitives allow you to express a collection of single node and multinode components that require tighter coupling from a scheduling (and in future releases network topology) aspect.

## Prerequisites

Before starting this tutorial, please go through the [installation guide](../../installation.md). Choose one of the two cluster setup procedures:
- [A local KIND demo cluster](../../installation.md#local-kind-cluster-set-up) Make sure to run `make kind-up FAKE_NODES=40`, set `KUBECONFIG` env variable as directed in the instructions, and run `make deploy`
- [A remote Kubernetes cluster](../../installation.md#remote-cluster-set-up) with [Grove installed from package](../../installation.md#install-grove-from-package) If you choose this path make sure to adjust the tolerations in the example to fit your cluster requirements

A basic understanding of Kubernetes concepts will help, [this is a good place to start](https://kubernetes.io/docs/tutorials/kubernetes-basics/).


## Core Concepts Overview

### PodClique: The Fundamental Unit
A **PodClique** is the core building block in Grove. It represents a group of pods with the same exact configuration - similar to a ReplicaSet, but with gang termination behavior. It can be used in a standalone manner to represent single-node components (components where each instance fits within one node and can be represented by one pod) of your system, or can represent roles within a multi-node component such as leader and worker.

### PodCliqueScalingGroup: Multi-Node Coordination
A **PodCliqueScalingGroup** coordinates multiple PodCliques that must scale together, preserving specified replica ratios across roles (e.g. leader/worker) in multi-node components (components where each instance spans multiple pods often on different nodes).

### PodCliqueSet: The Inference Service Container
A **PodCliqueSet** contains all the components for a complete service. It manages one or more PodCliques or PodCliqueScalingGroups that work together to form a functional system. PodCliqueSet replicas enable system-level scaling use cases such as deploying multiple complete instances of your inference stack (e.g., for canary deployments, A/B testing, or spreading across availability zones for high availability).

### Understanding Scaling Levels

Grove provides three levels of scaling to match different operational needs:

- **Scale PodCliqueSet replicas** (`kubectl scale pcs ...`) - Replicate your entire inference service with all its components. Use this for system-level operations like canary deployments, A/B testing, or spreading across availability zones for high availability.

- **Scale PodCliqueScalingGroup replicas** (`kubectl scale pcsg ...`) - Add more instances of a multi-node component within your service. Use this when you need more capacity of a specific multi-node component (e.g., add another leader+workers unit).

- **Scale PodClique replicas** (`kubectl scale pclq ...`) - Adjust the number of pods in a specific role. Use this for fine-tuning individual components (e.g., add more workers to an existing leader-worker group).

In the [next guide](./02_pcs_and_pclq_intro.md) we go through some examples showcasing PodCliqueSet and PodClique

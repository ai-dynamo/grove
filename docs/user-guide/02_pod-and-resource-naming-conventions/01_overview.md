# Pod and Resource Naming Conventions

This section explains Grove's hierarchical naming scheme for pods and resources. Grove's naming convention is designed to be **self-documenting**: when you run `kubectl get pods`, the pod names immediately tell you which PodCliqueSet, PodCliqueScalingGroup (if applicable), and PodClique each pod belongs to.

## Prerequisites

Before starting this section:
- Review the [core concepts tutorial](../01_core-concepts/01_overview.md) to understand Grove's primitives
- Set up a cluster following the [installation guide](../../installation.md), the two options are:
  - [A local KIND demo cluster](../../installation.md#local-kind-cluster-set-up): Create the cluster with `make kind-up FAKE_NODES=40`, set `KUBECONFIG` env variable as directed, and run `make deploy`
  - [A remote Kubernetes cluster](../../installation.md#remote-cluster-set-up) with [Grove installed from package](../../installation.md#install-grove-from-package)

## Guides in This Section

1. **[Naming Conventions](./02_naming-conventions.md)**: Learn the naming patterns, best practices, and how to plan names for your resources.

2. **[Hands-On Example](./03_hands-on-example.md)**: Deploy an example system with the structure of a multi-node disaggregated inference system and observe the naming hierarchy in action.

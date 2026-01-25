# Environment Variables for Pod Discovery

This section explains the environment variables that Grove automatically injects into your pods and shows how to use them for pod discovery, coordination, and configuration in distributed systems.

## Prerequisites

Before starting this section:
- Review the [core concepts tutorial](../core-concepts/01_overview.md) to understand Grove's primitives
- Read the [Pod Naming guide](../pod-and-resource-naming-conventions/01_overview.md) to understand Grove's naming conventions
- Set up a cluster following the [installation guide](../../installation.md), the two options are:
  - [A local KIND demo cluster](../../installation.md#local-kind-cluster-set-up): Create the cluster with `make kind-up FAKE_NODES=40`, set `KUBECONFIG` env variable as directed, and run `make deploy`
  - [A remote Kubernetes cluster](../../installation.md#remote-cluster-set-up) with [Grove installed from package](../../installation.md#install-grove-from-package)

> **Note:** The examples in this section require at least one real node to run actual containers and inspect environment variables in pod logs. The KIND cluster created with `make kind-up FAKE_NODES=40` includes one real control-plane node alongside the fake nodes, which is sufficient for these examples.

## Guides in This Section

1. **[Environment Variables Reference](./02_env_var_reference.md)**: Understand the distinction between pod names and hostnames, and see the complete reference of all injected Grove environment variables.

2. **[Hands-On Examples](./03_hands-on-examples.md)**: Deploy example PodCliqueSets and use environment variables to construct FQDNs and discover other pods. **We strongly recommend working through these examples**—they demonstrate the practical techniques you'll need to implement pod discovery in your own applications.

3. **[Common Patterns and Takeaways](./04_common-patterns-and-takeaways.md)**: Learn practical patterns for using environment variables in your applications.

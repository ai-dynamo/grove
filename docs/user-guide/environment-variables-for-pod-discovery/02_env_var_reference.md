# Environment Variables Reference

This guide covers the environment variables that Grove automatically injects into your pods.

## Overview

Grove automatically injects environment variables into every container and init container in your pods. These environment variables provide runtime information that your application can use for:
- **Pod discovery**: Finding other pods in your system
- **Coordination**: Understanding your role in a distributed system
- **Configuration**: Self-configuring based on your position in the hierarchy

However, before we get to the environment variables it is important to first make a distinction between a Pod's Name and its Hostname.

## Understanding Pod Names vs. Hostnames in Kubernetes

A common source of confusion is the difference between a pod's **name** and its **hostname**. Understanding this distinction is essential for using Grove's environment variables correctly.

### Pod Name (Kubernetes Resource Identifier)

The **pod name** is the unique identifier for the Pod resource in Kubernetes (stored in `metadata.name`). When Grove creates pods, it uses Kubernetes' `generateName` feature, which appends a random 5-character suffix to ensure uniqueness:

```
<pclq-name>-<random-suffix>
Example: env-demo-standalone-0-frontend-abc12
```

This name is what you see when running `kubectl get pods`. However, you **cannot use this name for DNS-based pod discovery** because the random suffix is unpredictable.

### Hostname (DNS-Resolvable Identity)

The **hostname** is a separate field (`spec.hostname`) that Grove explicitly sets on each pod. Unlike the pod name, the hostname follows a **deterministic pattern**:

```
<pclq-name>-<pod-index>
Example: env-demo-standalone-0-frontend-0
```

Grove automatically creates a headless service for each PodCliqueSet replica, so you don't need to create one yourself. Grove also sets the pod's **subdomain** (`spec.subdomain`) to match the headless service name. In Kubernetes, when a pod has both `hostname` and `subdomain` set, and a matching headless service exists, the pod becomes DNS-resolvable at:

```
<hostname>.<subdomain>.<namespace>.svc.cluster.local
```

For example:
```
env-demo-standalone-0-frontend-0.env-demo-standalone-0.default.svc.cluster.local
└──────── hostname ────────────┘ └──── subdomain ────┘ └─────────────────────┘
                                                        namespace + cluster suffix
```

### Why This Matters

| Attribute | Pod Name | Hostname |
|-----------|----------|----------|
| Source | `metadata.name` | `spec.hostname` |
| Pattern | `<pclq-name>-<random-suffix>` | `<pclq-name>-<pod-index>` |
| Predictable? | ❌ No (random suffix) | ✅ Yes (index-based) |
| DNS resolvable? | ❌ No | ✅ Yes (with headless service) |
| Use case | `kubectl` commands, logs | Pod discovery, pod-to-pod communication |

**The environment variables Grove provides give you the building blocks to construct the hostname-based FQDN, not the pod name.** This is why pod discovery in Grove is deterministic and doesn't require knowledge of random suffixes.

## Environment Variables Reference

### Available in All Pods

These environment variables are injected into every pod managed by Grove:

| Environment Variable | Description | Example Value |
|---------------------|-------------|---------------|
| `GROVE_PCS_NAME` | Name of the PodCliqueSet (as specified in metadata.name) | `my-service` |
| `GROVE_PCS_INDEX` | Replica index of the PodCliqueSet (0-based) | `0` |
| `GROVE_PCLQ_NAME` | Fully qualified PodClique resource name (see structure below) | `my-service-0-frontend` |
| `GROVE_HEADLESS_SERVICE` | FQDN of the headless service for the PodCliqueSet replica | `my-service-0.default.svc.cluster.local` |
| `GROVE_PCLQ_POD_INDEX` | Index of this pod within its PodClique (0-based) | `2` |

**Understanding `GROVE_PCLQ_NAME`:**
- For **standalone PodCliques**: `<pcs-name>-<pcs-index>-<pclq-template-name>`
  - Example: `my-service-0-frontend`
- For **PodCliques in a PCSG**: `<pcs-name>-<pcs-index>-<pcsg-template-name>-<pcsg-index>-<pclq-template-name>`
  - Example: `my-service-0-model-instance-0-leader`

### Additional Variables for PodCliqueScalingGroup Pods

If a pod belongs to a PodClique that is part of a PodCliqueScalingGroup, these additional environment variables are available:

| Environment Variable | Description | Example Value |
|---------------------|-------------|---------------|
| `GROVE_PCSG_NAME` | Fully qualified PCSG resource name (see structure below) | `my-service-0-model-instance` |
| `GROVE_PCSG_INDEX` | Replica index of the PodCliqueScalingGroup (0-based) | `1` |
| `GROVE_PCSG_TEMPLATE_NUM_PODS` | Total number of pods in the PCSG template | `4` |

**Understanding `GROVE_PCSG_NAME`:**
- Structure: `<pcs-name>-<pcs-index>-<pcsg-template-name>`
- Example: `my-service-0-model-instance`
- **Note:** This does NOT include the PCSG replica index. To construct a sibling PodClique name within the same PCSG replica, use: `$GROVE_PCSG_NAME-$GROVE_PCSG_INDEX-<pclq-template-name>`

**Note:** `GROVE_PCSG_TEMPLATE_NUM_PODS` represents the total number of pods defined in the PodCliqueScalingGroup template, calculated as the sum of replicas across all PodCliques in the PCSG. For example, if a PCSG has 1 leader replica and 3 worker replicas, this value would be 4. This value does not change based on scaling, so is only guaranteed to be accurate at startup.

## Next Steps

Continue to the [Hands-On Examples](./03_hands-on-examples.md) to deploy example PodCliqueSets and use environment variables to construct FQDNs and discover other pods. We strongly recommend working through these examples as they demonstrate the practical techniques you'll need to implement pod discovery in your own applications.


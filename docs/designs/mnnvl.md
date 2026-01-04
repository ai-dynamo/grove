# GREP-270: MNNVL Support – Phase 1 Requirements

**Related:** GREP-270 Design Document  
**Tracking issue:** Add automatic support for MNNVL  
**Status:** Ending team agreement

---

## Overview

This document captures the **Phase 1 requirements** for automatic **MNNVL (Multi-Node NVLink)** support in Grove.

---

## Scope

| Phase | Scope |
|------|------|
| Phase 1 (this document) | Operator-config-driven ComputeDomain management for homogeneous clusters |

---

## Definitions

- **Homogeneous cluster**: A cluster with the exact same type of GPUs on all nodes.

---

## Requirements

| ID | Description |
|----|-------------|
| **REQ-1** | Automatic MNNVL support must be enabled at the operator level by the cluster admin.<br/><br/>If ComputeDomain support is installed in an MNNVL-capable cluster, the cluster admin must explicitly enable the auto-MNNVL feature in the operator config. |
| **REQ-2** | When enabled, MNNVL injection must be fully automatic for all distributed Grove workloads.<br/><br/>NVLink communication should always be enabled if supported. |
| **REQ-3** | Only homogeneous clusters are supported.<br/><br/>In heterogeneous clusters, enabling the feature may result in unpredictable behavior. |
| **REQ-4** | When MNNVL is enabled, **one ComputeDomain** is created for each PCS replica.<br/><br/>In this initial version, there is no per-podClique toggle — the feature is all or nothing. |
| **REQ-5** | The lifecycle of a ComputeDomain is tied to the PCS replica lifecycle. |
| **REQ-6** | When MNNVL is enabled, only pods requesting GPUs via **extended resources** must be associated with the ComputeDomain. |
| **REQ-6-b** | Pods that request GPUs using **DRA only** will **not** be treated as GPU pods for RCT injection.<br/><br/>This will be addressed in Phase 2. |
| **REQ-7** | Failure behavior and error handling (see dedicated section below). |
| **REQ-8** | Observability and status reporting (see dedicated section below). |
| **REQ-9** | When MNNVL is enabled, users must be allowed to **opt out** of MNNVL when creating a new PCS.<br/><br/>This choice must be **immutable** — updating an existing PCS to opt out is not allowed.<br/><br/>Rationale: since only one ComputeDomain can exist per node, users may need to opt out when MNNVL is broken and want to fall back to InfiniBand, RoCE, or TCP. |
| **REQ-10** | When the MNNVL feature is disabled after being enabled:<br/>• Existing workloads must remain unchanged.<br/>• Workloads created with MNNVL enabled must keep CD and RCT active.<br/>• All new PCS instances must be created without MNNVL. |

---

## REQ-7: Failure Behavior and Error Handling

### Problem Statement

What should happen when a cluster admin enables MNNVL but preconditions are not met or required resources fail to create?

---

### Failure Scenarios

| Scenario | Description | Severity | Decision and Rationale |
|--------|-------------|----------|-----------------------|
| **S1: CRD not installed** | ComputeDomain CRD is not available (DRA not installed). | Cluster-level, affects all workloads | Enabling ComputeDomain support in the OperatorConfig must cause the Grove operator to **exit with error on startup**. Treated as an admin error. |
| **Failed to create CD** | Operator failed to create a ComputeDomain for a PCS replica. | PCS replica level, low | PCS should still be deployed **without** CD and RCT usage. This is a **permanent state** — the operator will not retry CD creation. Status must be updated accordingly. |

---

### Open Questions

- Which preconditions are **required** vs **nice-to-have**?
- Should node-level preconditions be validated by the operator or left to the scheduler?  
  **Decision:** No — leave node-level checks to the scheduler.

---

## REQ-8: Observability and Status Reporting

### Problem Statement

How should MNNVL status and failures be surfaced to users?

---

### Information to Surface

| Information | Description | Where to Surface | Decision and Rationale |
|------------|-------------|------------------|-----------------------|
| **MNNVL enabled** | Whether MNNVL is active for the PCS | PCS status, events, logs | Configuration should appear in PCS status. If the operator is running, the config is considered valid. |
| **Per-replica CD status** | Readiness status of ComputeDomain per replica | PCS status or CD objects | PCS status |
| **Precondition failures** | Reasons MNNVL could not be enabled (e.g., missing CRD, permissions) | PCS status, operator logs, events | PCS status for realtime state, logs for persistence, events for debugging. Operator exits with non-zero code. |
| **Warnings** | Non-fatal issues (e.g., failed to create ComputeDomain) | PCS status, operator logs, events | Operator logs warnings; PCS status reflects the issue. |

---

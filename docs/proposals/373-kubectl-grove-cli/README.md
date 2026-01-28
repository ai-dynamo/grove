# GREP-373: kubectl-grove CLI Plugin

<!-- toc -->
<!-- /toc -->

## Summary

This GREP proposes `kubectl-grove`, a kubectl plugin that provides a rich interaction layer for Grove workloads on Kubernetes. The CLI bridges the gap between raw `kubectl` commands and the complex, hierarchical nature of Grove resources (PodCliqueSets, PodGangs, PodCliques, Pods), offering both command-line tools and an interactive Terminal User Interface (TUI) called **Arborist**.

## Motivation

Managing distributed AI/ML workloads with Grove involves understanding complex resource hierarchies and placement topologies. Users currently need to:

1. Run multiple `kubectl get` commands to understand the state of their deployment
2. Manually correlate PodCliqueSets → PodGangs → PodCliques → Pods relationships
3. Lack visibility into GPU allocation, topology placement, and fragmentation
4. Have no intuitive way to visualize how pods are distributed across racks and nodes

A purpose-built CLI solves these problems by providing Grove-aware commands and visualizations that understand the resource model.

### Goals

1. **Unified visibility**: Single interface to view the entire Grove resource hierarchy
2. **Topology-first visualization**: Rich visual representation of pod placement across racks, nodes, and GPUs
3. **Interactive exploration**: TUI for real-time hierarchical navigation and event monitoring
4. **Grove-aware operations**: Commands that understand placement scores, roles, and workload topology
5. **Operational tooling**: Status, health, metrics, diagnostics, and lifecycle management

### Non-Goals

1. Replacing `kubectl` for general Kubernetes operations
2. Direct model serving or workload execution
3. Multi-cluster federation (initial version)

## Proposal

### Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                        kubectl-grove                            │
├─────────────────────────────────────────────────────────────────┤
│  Commands Layer                                                 │
│  ┌─────────┐ ┌─────────┐ ┌──────────┐ ┌─────────┐ ┌──────────┐  │
│  │ status  │ │topology │ │  health  │ │ metrics │ │   tui    │  │
│  └─────────┘ └─────────┘ └──────────┘ └─────────┘ └──────────┘  │
│  ┌─────────┐ ┌──────────┐ ┌─────────┐                           │
│  │  plan   │ │ rollout  │ │  diag   │                           │
│  └─────────┘ └──────────┘ └─────────┘                           │
├─────────────────────────────────────────────────────────────────┤
│  Kubernetes Client Layer (dynamic + typed clients)             │
├─────────────────────────────────────────────────────────────────┤
│  Grove CRDs: PodCliqueSet, PodClique, PodGang, ClusterTopology │
└─────────────────────────────────────────────────────────────────┘
```

### Resource Hierarchy

kubectl-grove operates on Grove's hierarchical resource model:

```
PodCliqueSet (deployment unit)
  └── PodCliqueSetReplica (revision/instance)
        └── PodGang (scheduling unit - all-or-nothing)
              └── PodClique (role group)
                    └── Pod (actual workload)
```

### User Stories

#### Story 1: Visualizing Topology Placement

As a platform engineer, I want to see how my pods are distributed across racks and nodes so I can identify fragmentation issues and optimize placement.

```bash
$ kubectl grove topology my-workload -n prod
```

#### Story 2: Interactive Debugging

As an operator, I want to interactively navigate the Grove resource hierarchy to understand the state of my deployment and correlate events with specific pods.

```bash
$ kubectl grove tui -n prod
```

### Limitations/Risks & Mitigations

| Risk | Mitigation |
|------|------------|
| TUI complexity may be difficult to maintain | Use established TUI library (tview) with modular view components |
| kubectl plugin discovery requires PATH setup | Provide installation script and documentation |
| Real-time updates may cause API server load | Configurable refresh intervals with sensible defaults |

## Design Details

### Command Reference

#### Phase 1: Core Visibility (MVP)

##### `kubectl grove status`

Display PodCliqueSet status with progress visualization.

```bash
kubectl grove status my-workload -n prod
kubectl grove status -A  # all namespaces
```

**Output:**
```
PodCliqueSet: my-workload    Namespace: prod
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  Clique          Ready    Status       Progress
  ──────          ─────    ──────       ────────
  prefill         4/4      Running      [████████████████] 100%
  decode          7/8      Scaling      [██████████████░░]  88%

  PlacementScore: 0.94    Phase: Running    Age: 2h34m
```

##### `kubectl grove topology`

Visualize pod placement across the cluster topology hierarchy.

```bash
kubectl grove topology my-workload -n prod
kubectl grove topology my-workload -n prod -w  # watch mode
```

**Output:**
```
╭─ ClusterTopology: grove-topology ─────────────────────────────────╮
│ Hierarchy: rack → host → pod                                      │
│ PlacementScore: 0.94 [█████████░]                                 │
╰───────────────────────────────────────────────────────────────────╯

PodCliqueSet: my-workload    PlacementScore: 0.94

Rack: rack-1 [optimal]  (24/32 GPUs allocated)
  │
  ├── node-gpu-01: [████████████░░░░] (6/8 GPUs)
  │     ├── [P] my-workload-prefill-0     Running   gpu:2
  │     ├── [P] my-workload-prefill-1     Running   gpu:2
  │     └── [D] my-workload-decode-0      Running   gpu:2
  │
  ├── node-gpu-02: [████████████████] (8/8 GPUs)
  │     ├── [D] my-workload-decode-1      Running   gpu:2
  │     ├── [D] my-workload-decode-2      Running   gpu:2
  │     ├── [D] my-workload-decode-3      Running   gpu:2
  │     └── [D] my-workload-decode-4      Running   gpu:2
  │
  └── node-gpu-03: [████████████░░░░] (6/8 GPUs)
        ├── [P] my-workload-prefill-2     Running   gpu:2
        ├── [P] my-workload-prefill-3     Running   gpu:2
        └── [D] my-workload-decode-5      Running   gpu:2

Rack: rack-2 [suboptimal]  (2/32 GPUs allocated)
  │
  └── node-gpu-04: [██░░░░░░░░░░░░░░] (2/8 GPUs)
        └── [D] my-workload-decode-6      Running   gpu:2

⚠ Warning: Pods fragmented across 2 racks
  → 1 decode pod in rack-2 may experience higher latency
```

##### `kubectl grove health`

Gang-aware health dashboard.

```bash
kubectl grove health my-workload -n prod
kubectl grove health -A -w  # all namespaces, watch mode
```

##### `kubectl grove diagnostics`

Collect comprehensive diagnostic data for troubleshooting.

```bash
kubectl grove diagnostics -n prod -o ./diag-output
```

**Collects:**
- All Grove CRs (PodCliqueSet, PodClique, PodGang, etc.)
- Pod logs and status
- Events filtered by namespace
- Operator logs
- Node information for placed pods

#### Phase 2: Interactive TUI (Arborist)

##### `kubectl grove tui`

Launch the **Arborist** interactive terminal user interface.

```bash
kubectl grove tui -n prod
```

**Layout:**
```
┌─────────────────────────────────────────────────────────────────────┐
│          Arborist | Grove Operator | Hierarchical Resource Viewer   │
├─────────────────────────────────────────────────────────────────────┤
│ > Forest > my-workload > gang-0 > prefill                           │
├───────────────────────────────────────────┬─────────────────────────┤
│ Resources                                 │ Events                  │
│ ─────────                                 │ ──────                  │
│ ► prefill-0     Running    gpu:2   node-1│ 2m  Pod scheduled       │
│   prefill-1     Running    gpu:2   node-1│ 2m  Pod started         │
│   prefill-2     Running    gpu:2   node-3│ 1m  Health check passed │
│   prefill-3     Running    gpu:2   node-3│ 30s Metrics available   │
│                                           │                         │
├───────────────────────────────────────────┴─────────────────────────┤
│ Enter: Drill down | Esc: Back | Tab: Switch pane | t: Topology | q  │
└─────────────────────────────────────────────────────────────────────┘
```

**Navigation Hierarchy:**
```
ViewForest (all PodCliqueSets)
  └── ViewPodCliqueSet (PodGangs in selected PCS)
        └── ViewPodGang (PodCliques in selected Gang)
              └── ViewPodClique (Pods in selected Clique)
                    └── ViewPod (YAML detail view)
```

**Keybindings:**
| Key | Action |
|-----|--------|
| `Enter` | Drill down into selected resource |
| `Esc` | Navigate back up the hierarchy |
| `Tab` | Switch focus between Resources and Events panes |
| `t` | Toggle topology visualization view |
| `↑/↓` | Navigate items |
| `q` / `Ctrl+Q` | Quit |

#### Phase 3: Lifecycle Management

| Command | Description |
|---------|-------------|
| `kubectl grove rollout` | Manage rolling updates |
| `kubectl grove scale` | Scale workers by role with placement analysis |
| `kubectl grove update` | High-level updates with dry-run preview |
| `kubectl grove restart` | Rolling restart without spec changes |
| `kubectl grove apply` | Apply config with diff and plan comparison |

#### Phase 4: Metrics

##### `kubectl grove metrics`

Live metrics from pod endpoints.

```bash
kubectl grove metrics my-workload -n prod
kubectl grove metrics my-workload -n prod --role prefill
kubectl grove metrics my-workload -n prod -w --json
```

### Monitoring

The CLI itself does not emit metrics but surfaces metrics from:
- Pod-level Prometheus endpoints (GPU utilization, memory)
- Grove operator metrics (placement scores, reconciliation)
- Kubernetes events related to Grove resources

### Test Plan

- Unit tests for each command's output formatting and data transformation
- Integration tests against a kind cluster with Grove CRDs installed
- E2E tests for TUI navigation flows

### Graduation Criteria

#### Alpha
- Core commands implemented: `status`, `topology`, `health`, `diagnostics`
- Basic TUI with hierarchical navigation
- Installation via `kubectl krew`

#### Beta
- TUI topology view embedded
- Lifecycle commands: `rollout`, `scale`
- Watch mode for all applicable commands

#### GA
- Full lifecycle command suite
- Metrics integration
- Comprehensive documentation and tutorials

## Implementation History

- 2026-01-27: GREP proposed

## Alternatives

### Alternative 1: Web Dashboard

A web-based UI was considered but rejected for initial implementation because:
- Requires additional infrastructure (ingress, auth)
- kubectl plugin provides zero-infrastructure solution
- TUI works in SSH sessions and constrained environments

### Alternative 2: Extend kubectl with custom columns

Using `kubectl get -o custom-columns` was considered but:
- Cannot represent hierarchical relationships
- No interactive capabilities
- Limited visualization options

## Appendix

### Example Session

```bash
# Deploy workload
$ kubectl apply -f my-workload-pcs.yaml

# Check deployment status
$ kubectl grove status my-workload -n prod
PodCliqueSet: my-workload    Phase: Scaling    Progress: [██████████░░░░░░] 62%

# Visualize topology placement
$ kubectl grove topology my-workload -n prod
rack-1 (16/32 GPUs)
  ├── node-01 [████████░░░░░░░░] (4/8)
  │     [P] prefill-0  [P] prefill-1
  └── node-02 [████████░░░░░░░░] (4/8)
        [D] decode-0  [D] decode-1

⚠ Warning: Only 50% of target pods scheduled

# Launch TUI for interactive exploration
$ kubectl grove tui -n prod
# (Interactive TUI opens - press 't' for topology view)

# Collect diagnostics if issues arise
$ kubectl grove diagnostics -n prod -o ./debug
Collecting PodCliqueSets... done
Collecting PodCliques... done
Collecting PodGangs... done
Collecting Pod logs... done
Collecting Events... done
Output written to ./debug/
```

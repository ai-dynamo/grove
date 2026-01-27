# GREP-001: kubectl-grove CLI Plugin

**Status:** Proposal
**Author:** Anish Maddipoti
**Created:** 2026-01-23

## Summary

This GREP proposes `kubectl-grove`, a kubectl plugin that provides a rich interaction layer for Grove AI inference workloads on Kubernetes. The CLI bridges the gap between raw `kubectl` commands and the complex, hierarchical nature of Grove resources (PodCliqueSets, PodGangs, PodCliques, Pods), offering both command-line tools and an interactive Terminal User Interface (TUI) called **Arborist**.

## Motivation

Managing distributed AI inference workloads with Grove involves understanding complex resource hierarchies and placement topologies. Users currently need to:

1. Run multiple `kubectl get` commands to understand the state of their inference deployment
2. Manually correlate PodCliqueSets → PodGangs → PodCliques → Pods relationships
3. Lack visibility into GPU allocation, topology placement, and fragmentation
4. Have no intuitive way to visualize how pods are distributed across racks and nodes

A purpose-built CLI solves these problems by providing inference-aware commands and visualizations that understand Grove's resource model.

## Goals

1. **Unified visibility**: Single interface to view the entire Grove resource hierarchy
2. **Topology-first visualization**: Rich visual representation of pod placement across racks, nodes, and GPUs
3. **Interactive exploration**: TUI for real-time hierarchical navigation and event monitoring
4. **Inference-aware operations**: Commands that understand prefill/decode roles, placement scores, and SLOs
5. **Operational tooling**: Status, health, metrics, diagnostics, and lifecycle management

## Non-Goals

1. Replacing `kubectl` for general Kubernetes operations
2. Direct model serving or inference execution
3. Multi-cluster federation (initial version)

---

## Design

### Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                        kubectl-grove                             │
├─────────────────────────────────────────────────────────────────┤
│  Commands Layer                                                  │
│  ┌─────────┐ ┌─────────┐ ┌──────────┐ ┌─────────┐ ┌──────────┐ │
│  │ status  │ │topology │ │  health  │ │ metrics │ │   tui    │ │
│  └─────────┘ └─────────┘ └──────────┘ └─────────┘ └──────────┘ │
│  ┌─────────┐ ┌──────────┐ ┌─────────┐                          │
│  │  plan   │ │ compare  │ │  diag   │                          │
│  └─────────┘ └──────────┘ └─────────┘                          │
├─────────────────────────────────────────────────────────────────┤
│  Kubernetes Client Layer (dynamic + typed clients)              │
├─────────────────────────────────────────────────────────────────┤
│  Grove CRDs: PodCliqueSet, PodClique, PodGang, ClusterTopology  │
└─────────────────────────────────────────────────────────────────┘
```

### Resource Hierarchy

kubectl-grove operates on Grove's hierarchical resource model:

```
PodCliqueSet (deployment unit)
  └── PodCliqueSetReplica (revision/instance)
        └── PodGang (scheduling unit - all-or-nothing)
              └── PodClique (role group: prefill, decode)
                    └── Pod (actual workload)
```

---

## Command Reference

### Phase 1: Core Visibility (MVP)

#### `kubectl grove status`

Display PodCliqueSet status with progress visualization.

```bash
kubectl grove status my-inference -n prod
kubectl grove status -a -n prod  # all PodCliqueSets
```

**Output:**
```
PodCliqueSet: my-inference    Namespace: prod
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  Clique          Ready    Status       Progress
  ──────          ─────    ──────       ────────
  prefill         4/4      Running      [████████████████] 100%
  decode          7/8      Scaling      [██████████████░░]  88%

  PlacementScore: 0.94    Phase: Running    Age: 2h34m
```

#### `kubectl grove topology` (Priority: HIGH)

Visualize pod placement across the cluster topology hierarchy.

```bash
kubectl grove topology my-inference -n prod
kubectl grove topology my-inference -n prod -w  # watch mode
```

**Output:**
```
╭─ ClusterTopology: grove-topology ─────────────────────────────────╮
│ Hierarchy: rack → host → pod                                       │
│ PlacementScore: 0.94 [█████████░]                                  │
╰────────────────────────────────────────────────────────────────────╯

PodCliqueSet: my-inference    PlacementScore: 0.94

Rack: rack-1 [optimal]  (24/32 GPUs allocated)
  │
  ├── node-gpu-01: [████████████░░░░] (6/8 GPUs)
  │     ├── [P] my-inference-prefill-0     Running   gpu:2
  │     ├── [P] my-inference-prefill-1     Running   gpu:2
  │     └── [D] my-inference-decode-0      Running   gpu:2
  │
  ├── node-gpu-02: [████████████████] (8/8 GPUs)
  │     ├── [D] my-inference-decode-1      Running   gpu:2
  │     ├── [D] my-inference-decode-2      Running   gpu:2
  │     ├── [D] my-inference-decode-3      Running   gpu:2
  │     └── [D] my-inference-decode-4      Running   gpu:2
  │
  └── node-gpu-03: [████████████░░░░] (6/8 GPUs)
        ├── [P] my-inference-prefill-2     Running   gpu:2
        ├── [P] my-inference-prefill-3     Running   gpu:2
        └── [D] my-inference-decode-5      Running   gpu:2

Rack: rack-2 [suboptimal]  (2/32 GPUs allocated)
  │
  └── node-gpu-04: [██░░░░░░░░░░░░░░] (2/8 GPUs)
        └── [D] my-inference-decode-6      Running   gpu:2

⚠ Warning: Pods fragmented across 2 racks
  → 1 decode pod in rack-2 may experience higher latency
  → Consider consolidating to rack-1 for optimal NVLink connectivity
```

**Key Features:**
- Rack → Node → Pod hierarchy visualization
- GPU allocation bars with color gradient (green → yellow → red)
- Role badges: `[P]` prefill, `[D]` decode
- Placement fragmentation warnings
- Watch mode with 2-second refresh

#### `kubectl grove health`

Gang-aware health dashboard.

```bash
kubectl grove health my-inference -n prod
kubectl grove health -a -n prod -w  # all, watch mode
```

**Output:**
```
Gang Health Dashboard: my-inference
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

Gang: my-inference-gang-0    Phase: Running    PlacementScore: 0.94
  │
  ├─ Clique: prefill (4 pods)
  │    ✓ All pods healthy
  │    Avg GPU Util: 78%    Memory: 45GB/80GB
  │
  └─ Clique: decode (8 pods)
       ⚠ 1 pod pending
       Avg GPU Util: 65%    Memory: 38GB/80GB

Events (last 5m):
  3m ago  [Warning]  Pod decode-6 pending: Insufficient GPU in rack-1
  1m ago  [Normal]   Pod decode-6 scheduled to rack-2
```

#### `kubectl grove diagnostics`

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

---

### Phase 2: Interactive TUI (Arborist)

#### `kubectl grove tui` (Priority: CRITICAL)

Launch the **Arborist** interactive terminal user interface.

```bash
kubectl grove tui -n prod
```

**Layout:**
```
┌─────────────────────────────────────────────────────────────────────┐
│          Arborist | Grove Operator | Hierarchical Resource Viewer   │
├─────────────────────────────────────────────────────────────────────┤
│ > Forest > my-inference > gang-0 > prefill                          │
├───────────────────────────────────────────┬─────────────────────────┤
│ Resources                                 │ Events                  │
│ ─────────                                 │ ──────                  │
│ ► prefill-0     Running    gpu:2   node-1│ 2m  Pod scheduled       │
│   prefill-1     Running    gpu:2   node-1│ 2m  Pod started         │
│   prefill-2     Running    gpu:2   node-3│ 1m  Health check passed │
│   prefill-3     Running    gpu:2   node-3│ 30s Metrics available   │
│                                           │                         │
│                                           │                         │
├───────────────────────────────────────────┴─────────────────────────┤
│ Enter: Drill down | Esc: Back | Tab: Switch pane | t: Topology | q: │
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

**TUI Topology View (Priority: CRITICAL):**

Pressing `t` in the TUI switches to an embedded topology visualization:

```
┌─────────────────────────────────────────────────────────────────────┐
│ Topology: my-inference                                [Esc to back] │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  PlacementScore: 0.94 [█████████░]                                  │
│                                                                     │
│  rack-1 (24/32 GPUs) ─────────────────────────────────────────────  │
│    │                                                                │
│    ├─ node-gpu-01 [████████████░░░░]                                │
│    │    [P] prefill-0  [P] prefill-1  [D] decode-0                  │
│    │                                                                │
│    ├─ node-gpu-02 [████████████████]                                │
│    │    [D] decode-1  [D] decode-2  [D] decode-3  [D] decode-4      │
│    │                                                                │
│    └─ node-gpu-03 [████████████░░░░]                                │
│         [P] prefill-2  [P] prefill-3  [D] decode-5                  │
│                                                                     │
│  rack-2 (2/32 GPUs) ──────────────────────────────────────────────  │
│    │                                                                │
│    └─ node-gpu-04 [██░░░░░░░░░░░░░░]                                │
│         [D] decode-6  ⚠ fragmented                                  │
│                                                                     │
├─────────────────────────────────────────────────────────────────────┤
│ ⚠ 1 pod fragmented across racks - consider consolidation            │
└─────────────────────────────────────────────────────────────────────┘
```

**Real-Time Updates:**
- Background goroutine fetches data every 2 seconds
- Non-blocking UI updates via `QueueUpdateDraw`
- Events sidebar auto-filters based on current view context

---

### Phase 3: Lifecycle Management (dependent on rolling upgrades work etc. being merged in)

| Command | Description |
|---------|-------------|
| `kubectl grove rollout` | Manage rolling updates with inference metrics |
| `kubectl grove scale` | Scale workers by role with placement analysis |
| `kubectl grove update` | High-level updates with dry-run preview |
| `kubectl grove restart` | Rolling restart without spec changes |
| `kubectl grove apply` | Apply config with diff and plan comparison |

See [cli-update-commands.md](./cli-update-commands.md) for detailed design.

---

### Phase 4: Metrics & Comparison (lowest priority)

#### `kubectl grove metrics`

Live inference metrics from pod endpoints.

```bash
kubectl grove metrics my-inference -n prod
kubectl grove metrics my-inference -n prod --role prefill
kubectl grove metrics my-inference -n prod -w --json
```

**Output:**
```
Inference Metrics: my-inference
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  Role       Pods    Throughput    TTFT p99    TPOT p99    GPU Util
  ────       ────    ──────────    ────────    ────────    ────────
  prefill    4       1,240 tok/s   124ms       -           78%
  decode     8       8,920 tok/s   -           11ms        65%

  Queue Depth: 12    Active Requests: 45
```

#### `kubectl grove compare`

Compare stored deployment plan vs actual state.

```bash
kubectl grove compare my-inference -n prod --verbose
```

**Output:**
```
Plan vs Actual: my-inference
━━━━━━━━━━━━━━━━━━━━━━━━━━━━

Configuration Differences:
  prefill.replicas:    4 (plan) = 4 (actual)  ✓
  decode.replicas:     8 (plan) = 8 (actual)  ✓
  image:               vllm:v1.2  ≠  vllm:v1.3  ⚠

Topology Analysis:
  Planned Racks:       1
  Actual Racks:        2  ⚠ fragmentation
  PlacementScore:      0.94 (target: 0.95)

Recommendation:
  → Image drift detected. Run: kubectl grove update --image=vllm:v1.2
  → Consider scaling decode to consolidate placement
```

---

## Prioritization

### Critical (Must Have)

1. **Arborist TUI** (`kubectl grove tui`)
   - Hierarchical navigation (Forest → PCS → Gang → Clique → Pod)
   - Real-time refresh
   - Events sidebar with context filtering
   - **Embedded topology view** (press `t`)

2. **Topology Command** (`kubectl grove topology`)
   - Rack → Node → Pod visualization
   - GPU allocation bars
   - Fragmentation warnings
   - Watch mode

### High Priority

3. **Status Command** (`kubectl grove status`)
4. **Health Command** (`kubectl grove health`)
5. **Diagnostics Command** (`kubectl grove diagnostics`)

### Medium Priority

6. **Lifecycle Commands** (`kubectl grove rollout/scale/update/restart/apply`)
   - Rolling updates with inference awareness
   - Role-based scaling with placement analysis
   - See [cli-update-commands.md](./cli-update-commands.md)

### Lower Priority

7. **Metrics Command** (`kubectl grove metrics`)
8. **Compare Command** (`kubectl grove compare`)
9. **Plan Commands** (`kubectl grove plan store/show/diff`)

---

## Success Criteria

1. User can visualize topology placement in under 5 seconds
2. TUI provides intuitive navigation through entire resource hierarchy
3. Fragmentation and placement issues are surfaced proactively
4. Diagnostics collection works reliably across all Grove resources
5. Commands follow kubectl conventions and feel native

---

## References

- [Arborist v2 Prototype](../../cli-plugin/) - Reference implementation
- [CLI Update Commands Design](./cli-update-commands.md) - Lifecycle command design
- [kubectl Plugin Documentation](https://kubernetes.io/docs/tasks/extend-kubectl/kubectl-plugins/)
- [tview Documentation](https://github.com/rivo/tview)

---

## Appendix: Example Session

```bash
# Deploy inference workload
$ kubectl apply -f my-inference-pcs.yaml

# Check deployment status
$ kubectl grove status my-inference -n prod
PodCliqueSet: my-inference    Phase: Scaling    Progress: [██████████░░░░░░] 62%

# Visualize topology placement
$ kubectl grove topology my-inference -n prod
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

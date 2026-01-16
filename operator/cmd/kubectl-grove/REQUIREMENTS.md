# kubectl-grove: Requirements & Design Document

**Status:** Draft
**Authors:** Grove PM Team
**Last Updated:** January 2026
**Branch:** `arborist-v2`
**Binary:** `kubectl-grove` (kubectl plugin)

---

## Executive Summary

`kubectl grove` is Grove's CLI plugin for managing, visualizing, and diagnosing AI inference workloads on Kubernetes. This document outlines the roadmap to transform the existing diagnostics tool into a comprehensive operations tool that **differentiates Grove from RBG** through superior observability and closed-loop feedback with AIConfigurator.

### The Pitch

> **RBG helps you deploy. kubectl grove helps you succeed.**
>
> RBG can generate a config from AIConfigurator and deploy it. But then what?
>
> kubectl grove shows you:
> - **Where** your pods actually landed (topology view)
> - **How well** they landed (PlacementScore)
> - **Whether** they're meeting your plan (plan vs actual)
> - **Why** they might be underperforming (diagnosis)
> - **What** to do about it (recommendations)

---

## Key Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| CLI Naming | `kubectl grove` | Matches RBG's `kubectl rbg` pattern, discoverable via krew |
| P0 Priority | Parallel (status + topology) | Build parity AND differentiation together |
| Plan Storage | ConfigMap with `grove.io/aic-plan` label | Simple, no CRD needed |
| TUI Priority | Phase 3 (after core CLI, before metrics) | High value, user loves TUI |
| Code Location | `operator/cmd/kubectl-grove/` | Keep with operator code, delete empty `cli-plugin/` |
| Metrics Source | Direct pod scraping | Works without Prometheus operator dependency |

---

## Background & Motivation

### Competitive Context

RBG (RoleBasedGroup) has shipped several features that Grove lacks:

| Feature | RBG Status | Grove Status |
|---------|------------|--------------|
| `kubectl rbg status` | ✅ Progress bars, role status | ❌ Empty placeholder |
| `kubectl rbg llm generate` | ✅ AIConfigurator integration | ❌ Not implemented |
| `kubectl rbg rollout history/undo` | ✅ Revision tracking | ❌ No revision system |
| In-place updates | ✅ InstanceSet | ❌ Not implemented |
| Rolling update coordination | ✅ maxSkew across roles | ❌ Basic hash-triggered |

### Grove's Unique Advantages

Grove has data that RBG doesn't expose:

1. **PlacementScore** (0.0-1.0) - Network optimality score per PodGang
2. **ClusterTopology** CRD - Platform-agnostic topology hierarchy
3. **TopologyConstraint.PackDomain** - rack, host, numa, etc.
4. **ReuseReservationRef** - Placement reuse hints during updates
5. **TerminationDelay** countdown - Time until gang termination
6. **ScheduleGatedReplicas** - Two-tier gang scheduling visibility

**Strategy:** Don't just catch up to RBG—leapfrog them by building the observability layer they're missing.

---

## Current State (Diagnostics MVP)

The current implementation (from `gflarity/diagnostics_cli_command` branch) provides:

```bash
kubectl grove diag -n <namespace> -o <output-dir>
```

**Collectors:**
1. `CollectOperatorLogs` - Last 2000 lines from grove-operator pods
2. `CollectGroveResources` - YAML dump of PodCliqueSets, PodCliques, PodCliqueScalingGroups, PodGangs
3. `CollectPodDetails` - Pod table with phase, ready status, node, conditions
4. `CollectEvents` - Last 10 minutes of Kubernetes events

**Output:** Files written to `grove-diagnostics-{timestamp}/` directory.

**Limitations:**
- One-shot dump only, no interactive mode
- No visualization
- No AIConfigurator integration
- No topology awareness
- No metrics integration

---

## Target Architecture

```
operator/cmd/kubectl-grove/
├── main.go                     # CLI entry point
├── cli.go                      # Kong CLI definitions
├── internal/
│   ├── commands/               # Subcommand implementations
│   │   ├── status.go           # kubectl grove status
│   │   ├── topology.go         # kubectl grove topology
│   │   ├── health.go           # kubectl grove health
│   │   ├── generate.go         # kubectl grove generate (AIC)
│   │   ├── plan.go             # kubectl grove plan show/diff
│   │   ├── compare.go          # kubectl grove compare
│   │   ├── metrics.go          # kubectl grove metrics
│   │   └── diag.go             # kubectl grove diag (existing)
│   ├── tui/                    # Terminal UI (Bubble Tea)
│   │   ├── app.go              # Main TUI application
│   │   ├── views/
│   │   │   ├── hierarchy.go    # PCS → PCLQ → Pod tree
│   │   │   ├── topology.go     # Rack/node heatmap
│   │   │   ├── health.go       # Gang health dashboard
│   │   │   └── updates.go      # Rolling update progress
│   │   └── components/
│   │       ├── progress.go     # Progress bars
│   │       ├── table.go        # Tables
│   │       └── tree.go         # Tree rendering
│   ├── watch/                  # Kubernetes resource watchers
│   │   ├── watcher.go          # Generic watch infrastructure
│   │   ├── podcliqueset.go     # PCS watcher
│   │   ├── podgang.go          # PodGang watcher
│   │   └── pod.go              # Pod watcher
│   ├── aic/                    # AIConfigurator integration
│   │   ├── executor.go         # Run AIConfigurator CLI
│   │   ├── parser.go           # Parse AIConfigurator output
│   │   ├── renderer.go         # Generate Grove manifests
│   │   └── plan.go             # Plan storage/retrieval
│   ├── metrics/                # Metrics integration
│   │   ├── scraper.go          # Direct pod metrics scraping
│   │   ├── sglang.go           # SGLang metrics parser
│   │   ├── vllm.go             # vLLM metrics parser
│   │   └── sla.go              # SLA comparison
│   └── diagnostics/            # Existing diagnostics
│       ├── collector.go
│       ├── resources.go
│       └── pods.go
└── pkg/
    └── types/                  # Shared types
```

---

## Feature Specifications

### P0: Foundation + Differentiation (Parallel)

#### P0.1: `kubectl grove status`

**Goal:** Match RBG's `kubectl rbg status` command + show PlacementScore.

**Usage:**
```bash
kubectl grove status <podcliqueset-name> [-n namespace]
kubectl grove status --all  # All PCS in namespace
```

**Output:**
```
📊 Resource Overview
  Namespace: vllm-disagg
  Name:      my-inference
  Age:       2h15m

📦 Clique Statuses
prefill      3/3     (min: 2)    [████████████████] 100%
decode       5/5     (min: 3)    [████████████████] 100%
router       1/1     (min: 1)    [████████████████] 100%

🎯 PodGang Status
my-inference-0    Running    PlacementScore: 0.95 ████████▓░

∑ Summary: 3 cliques | 9/9 Ready | 1/1 Gangs Running
```

**Data Sources:**
- `PodCliqueSet.Status.AvailableReplicas`
- `PodClique.Status.ReadyReplicas`, `ScheduledReplicas`
- `PodGang.Status.Phase`, `PlacementScore`
- `PodClique.Spec.MinAvailable`

---

#### P0.2: `kubectl grove topology`

**Goal:** Visualize pod placement across cluster topology (RBG doesn't have this).

**Usage:**
```bash
kubectl grove topology <podcliqueset-name> [-n namespace]
kubectl grove topology <podcliqueset-name> --watch
```

**Output:**
```
┌─ ClusterTopology: grove-topology ─────────────────────────────────┐
│ Hierarchy: region → zone → rack → host → numa                    │
└───────────────────────────────────────────────────────────────────┘

PodCliqueSet: my-inference    PlacementScore: 0.92 ████████▓░

TopologyConstraint: packDomain=rack

rack-1 [optimal]  12 GPUs allocated
├─ node-1: ████████ (8/8 GPUs)
│  ├─ prefill-0  Running  gpu:0-3
│  └─ prefill-1  Running  gpu:4-7
├─ node-2: ████████ (8/8 GPUs)
│  ├─ prefill-2  Running  gpu:0-3
│  └─ decode-0   Running  gpu:4-7
└─ node-3: ████░░░░ (4/8 GPUs)
   ├─ decode-1   Running  gpu:0-1
   └─ decode-2   Running  gpu:2-3

rack-2 [fragmented] ⚠  2 GPUs allocated
└─ node-5: ██░░░░░░ (2/8 GPUs)
   └─ router-0   Running  gpu:0-1

⚠ Warning: Pods split across 2 racks. PlacementScore: 0.72
  Recommendation: Consolidate to rack-1 for optimal NVLink connectivity
```

**Data Sources:**
- `ClusterTopology` CRD - topology hierarchy
- `PodGang.Status.PlacementScore`
- `PodGang.Spec.TopologyConstraint`
- `Pod.Spec.NodeName` + node labels
- Node GPU allocations from `nvidia.com/gpu`

---

### P1: AIConfigurator Integration

#### P1.1: `kubectl grove generate`

**Goal:** Match RBG's `kubectl rbg llm generate` command.

**Usage:**
```bash
kubectl grove generate \
  --model QWEN3_32B \
  --system h200_sxm \
  --total-gpus 32 \
  --backend sglang \
  --isl 4000 --osl 1000 \
  --ttft 300 --tpot 10 \
  --save-dir /tmp/output
```

**Workflow:**
1. Check if `aiconfigurator` CLI is installed
2. Run AIConfigurator with provided parameters
3. Parse output (prefill workers, decode workers, TP/PP/DP configs)
4. Generate Grove `PodCliqueSet` YAML for both disagg and agg modes
5. Optionally store plan as ConfigMap for later comparison

**Output:**
```
✓ AIConfigurator completed successfully

Plan 1: Prefill-Decode Disaggregated Mode
  File: /tmp/output/qwen3-32b-sglang-disagg.yaml
  Configuration:
    - Prefill Workers: 4 (tp1, 1 GPU each)
    - Decode Workers: 1 (tp4, 4 GPUs)
    - Total GPU Usage: 8
  Expected Performance:
    - Throughput: 804 tok/s/gpu
    - TTFT: 486ms
    - TPOT: 9.16ms

Plan 2: Aggregated Mode
  File: /tmp/output/qwen3-32b-sglang-agg.yaml
  ...

To deploy:
  kubectl apply -f /tmp/output/qwen3-32b-sglang-disagg.yaml

To store plan for later comparison:
  kubectl grove plan store my-inference -f /tmp/output/qwen3-32b-sglang-disagg.yaml
```

---

#### P1.2: `kubectl grove plan`

**Goal:** Store and display AIConfigurator plans for later comparison.

**Usage:**
```bash
# Store a plan
kubectl grove plan store <name> -f <yaml-or-json>

# Show stored plan
kubectl grove plan show <podcliqueset-name>

# Compare plan to deployed config
kubectl grove plan diff <podcliqueset-name>
```

**Storage:** ConfigMap with label `grove.io/aic-plan: <podcliqueset-name>`

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-inference-aic-plan
  labels:
    grove.io/aic-plan: my-inference
data:
  plan.json: |
    {
      "model": "QWEN3_32B",
      "system": "h200_sxm",
      "serving_mode": "disagg",
      "config": {
        "prefill_workers": 4,
        "decode_workers": 1,
        "prefill_tp": 1,
        "decode_tp": 4
      },
      "expected": {
        "throughput_tokens_per_sec_per_gpu": 804.83,
        "ttft_ms": 486.53,
        "tpot_ms": 9.16
      },
      "sla": {
        "ttft_ms": 300,
        "tpot_ms": 10
      }
    }
```

---

#### P1.3: `kubectl grove health`

**Goal:** Monitor gang health with termination countdown.

**Usage:**
```bash
kubectl grove health <podcliqueset-name> [-n namespace]
kubectl grove health --all
kubectl grove health <name> --watch
```

**Output:**
```
🏥 Gang Health Dashboard

PodCliqueSet: my-inference

┌─ PodGang: my-inference-0 ─────────────────────────────────────────┐
│ Phase: Running    PlacementScore: 0.95                            │
│ Status: ✓ Healthy                                                 │
│                                                                   │
│ Clique Health:                                                    │
│   prefill   3/3 ready  (min: 2)  ✓ Above threshold                │
│   decode    5/5 ready  (min: 3)  ✓ Above threshold                │
└───────────────────────────────────────────────────────────────────┘

┌─ PodGang: my-inference-1 ─────────────────────────────────────────┐
│ Phase: Running    PlacementScore: 0.72                            │
│ Status: ⚠ UNHEALTHY (Termination in 3h 42m)                       │
│                                                                   │
│ Issues:                                                           │
│   - prefill-2: Pending (Unschedulable: insufficient GPU)          │
│                                                                   │
│ TerminationDelay: 4h (configured)                                 │
│ Time Remaining: 3h 42m ████████████░░░░░░░░ 78%                   │
└───────────────────────────────────────────────────────────────────┘

Summary: 1/2 gangs healthy | 1 gang at risk of termination
```

---

### P2: TUI Mode

#### P2.1: `kubectl grove tui`

**Goal:** Interactive terminal UI for real-time monitoring.

**Usage:**
```bash
kubectl grove tui [-n namespace]
```

**Interface:**
```
┌─ kubectl grove ────────────────────────────────────────────────────────────┐
│ Namespace: vllm-disagg    Cluster: prod-us-west-2                          │
├────────────────────────────────────────────────────────────────────────────┤
│ [1] Hierarchy  [2] Topology  [3] Health  [4] Metrics  [q]uit               │
├────────────────────────────────────────────────────────────────────────────┤
│                                                                            │
│  PodCliqueSet: my-inference (2/2 available)                                │
│  ├── PodGang: my-inference-0 [Running, Score: 0.95]                        │
│  │   ├── prefill [3/3] ████████████████ 100%                               │
│  │   │   ├── pod-abc  Running  node-1  gpu:0-3                             │
│  │   │   ├── pod-def  Running  node-1  gpu:4-7                             │
│  │   │   └── pod-ghi  Running  node-2  gpu:0-3                             │
│  │   └── decode [5/5] ████████████████ 100%                                │
│  │       └── (5 pods)                                                      │
│  └── PodGang: my-inference-1 [Running, Score: 0.72] ⚠                      │
│      └── ...                                                               │
│                                                                            │
├────────────────────────────────────────────────────────────────────────────┤
│ ↑/↓: Navigate  Enter: Expand  r: Refresh  ?: Help                          │
└────────────────────────────────────────────────────────────────────────────┘
```

**Views:**
1. **Hierarchy** - PCS → PodGang → PodClique → Pod tree
2. **Topology** - Visual rack/node layout with PlacementScore
3. **Health** - Gang health with termination countdown
4. **Metrics** - Live throughput/latency (when P3 is complete)

**Implementation:**
- Framework: [Bubble Tea](https://github.com/charmbracelet/bubbletea)
- Styling: [Lip Gloss](https://github.com/charmbracelet/lipgloss)
- Real-time: K8s watch API

---

### P3: Metrics Integration

#### P3.1: `kubectl grove metrics`

**Goal:** Scrape inference engine metrics directly from pods.

**Usage:**
```bash
kubectl grove metrics <podcliqueset-name> [-n namespace]
kubectl grove metrics <name> --watch
kubectl grove metrics <name> --json
```

**Output:**
```
Live Metrics: my-inference (last 5m)

Throughput:
  Input:  12,450 tok/s
  Output: 8,230 tok/s
  Per-GPU: 756 tok/s/gpu (plan: 804, -6%)

Latency:
  TTFT p50:  125ms    TTFT p99:  342ms (SLA: 300ms) ❌
  TPOT p50:  6.1ms    TPOT p99:  8.2ms (SLA: 10ms) ✓

Queue:
  Queue Depth: 12 requests
  Active Requests: 48

Per-Role:
  prefill: Token Usage 78%
  decode:  Token Usage 45% ⚠ Underutilized
```

**Implementation:**
- Direct HTTP scrape from pod IPs (no Prometheus dependency)
- Auto-detect engine: SGLang (`/metrics`), vLLM (`/metrics`), TRT-LLM
- Compare against SLA from stored plan

---

#### P3.2: `kubectl grove compare`

**Goal:** Compare AIConfigurator predictions to actual performance.

**Usage:**
```bash
kubectl grove compare <podcliqueset-name> [-n namespace]
```

**Output:**
```
Plan vs Actual Comparison: my-inference

┌─────────────────────┬──────────────┬──────────────┬────────────┐
│ Metric              │ Planned      │ Actual       │ Status     │
├─────────────────────┼──────────────┼──────────────┼────────────┤
│ Throughput          │ 804 tok/s/gpu│ 756 tok/s/gpu│ ⚠ -6%      │
│ TTFT (p99)          │ ≤300ms       │ 342ms        │ ❌ EXCEED  │
│ TPOT (p99)          │ ≤10ms        │ 8.2ms        │ ✓ OK       │
│ PlacementScore      │ 1.0          │ 0.72         │ ⚠ -28%     │
└─────────────────────┴──────────────┴──────────────┴────────────┘

Diagnosis:
  PlacementScore 0.72 → prefill pods split across 2 racks

Recommendations:
  1. [HIGH] Reschedule prefill-2, prefill-3 to consolidate on rack-1
     Expected: PlacementScore → 0.95, TTFT → 280ms
```

---

### P4: Future Features

- `kubectl grove recommend` - AI-powered recommendations
- `kubectl grove dashboard` - Web-based dashboard (Streamlit)
- k9s plugin integration

---

## Technical Specifications

### Dependencies

```go
require (
    github.com/alecthomas/kong v1.x          // CLI framework
    github.com/charmbracelet/bubbletea v1.x  // TUI
    github.com/charmbracelet/lipgloss v1.x   // TUI styling
    github.com/charmbracelet/bubbles v0.x    // TUI components
    k8s.io/client-go v0.34.x                 // K8s client
)
```

### Configuration

```yaml
# ~/.config/kubectl-grove/config.yaml
defaults:
  namespace: default
  operator_namespace: grove-system

metrics:
  scrape_timeout: 5s
  port: 8000

tui:
  refresh_interval: 2s
  color_scheme: auto

aiconfigurator:
  path: aiconfigurator
  default_backend: sglang
```

### kubectl Plugin Installation

```bash
# Build
cd operator/cmd/kubectl-grove
go build -o kubectl-grove .

# Install (move to PATH)
sudo mv kubectl-grove /usr/local/bin/

# Verify
kubectl grove --help
```

Future: Distribute via krew.

---

## Implementation Roadmap

### Phase 0: Foundation (Week 1-2)
- [ ] Rename arborist → kubectl-grove
- [ ] Restructure CLI with subcommands
- [ ] Implement `kubectl grove status`
- [ ] Implement `kubectl grove topology`
- [ ] Set up test infrastructure

### Phase 1: AIConfigurator Integration (Week 3-4)
- [ ] Implement `kubectl grove generate`
- [ ] Implement AIConfigurator executor/parser
- [ ] Implement Grove manifest renderer
- [ ] Implement `kubectl grove plan store/show/diff`
- [ ] Implement `kubectl grove health`

### Phase 2: TUI Mode (Week 5-8)
- [ ] Set up Bubble Tea framework
- [ ] Implement hierarchy view
- [ ] Implement topology view
- [ ] Implement health view
- [ ] Add K8s watch for real-time updates

### Phase 3: Metrics Integration (Week 9-12)
- [ ] Implement direct pod metrics scraping
- [ ] Implement `kubectl grove metrics`
- [ ] Implement `kubectl grove compare`
- [ ] Add SLA comparison

---

## GitHub Issues

Create the following issues for tracking:

1. **[P0] kubectl grove status command** - Match RBG status + PlacementScore
2. **[P0] kubectl grove topology command** - Topology visualization (differentiator)
3. **[P1] kubectl grove generate command** - AIConfigurator integration
4. **[P1] kubectl grove plan commands** - Plan storage and diff
5. **[P1] kubectl grove health command** - Gang health monitoring
6. **[P2] kubectl grove tui** - Interactive terminal UI
7. **[P3] kubectl grove metrics command** - Direct pod metrics scraping
8. **[P3] kubectl grove compare command** - Plan vs actual comparison

---

## Success Metrics

1. **Adoption:** 50% of Grove users using kubectl grove within 3 months
2. **Issue Resolution:** 40% reduction in time-to-diagnosis
3. **Competitive:** Feature parity with RBG + 3 differentiating features
4. **Satisfaction:** Positive feedback on topology visualization

---

## References

- [RBG CLI Implementation](https://github.com/kubernetes-sigs/rbgs/tree/main/cmd/cli)
- [AIConfigurator](https://github.com/ai-dynamo/aiconfigurator)
- [srtctl Dashboard](https://github.com/ishandhanani/srt-slurm) - Visualization inspiration
- [Bubble Tea](https://github.com/charmbracelet/bubbletea) - TUI framework
- [Grove Competitive Analysis](https://gist.github.com/athreesh/3f3c868f2e5f8b02c1f632159788af98)

---

## Changelog

- **2026-01-16:** Renamed to kubectl-grove, updated priorities per PM decisions
- **2026-01-15:** Initial draft (as arborist)

# Arborist v2: Requirements & Design Document

**Status:** Draft
**Authors:** Grove PM Team
**Last Updated:** January 2026
**Branch:** `arborist-v2`

---

## Executive Summary

Arborist is Grove's CLI tool for managing, visualizing, and diagnosing AI inference workloads on Kubernetes. This document outlines the roadmap to transform Arborist from a basic diagnostics collector into a comprehensive operations tool that **differentiates Grove from RBG** through superior observability and closed-loop feedback with AIConfigurator.

### The Pitch

> **RBG helps you deploy. Arborist helps you succeed.**
>
> RBG can generate a config from AIConfigurator and deploy it. But then what?
>
> Arborist shows you:
> - **Where** your pods actually landed (topology view)
> - **How well** they landed (PlacementScore)
> - **Whether** they're meeting your plan (plan vs actual)
> - **Why** they might be underperforming (diagnosis)
> - **What** to do about it (recommendations)

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

## Current State (v1 - MVP)

The current Arborist implementation (`gflarity/diagnostics_cli_command` branch) provides:

```bash
arborist -n <namespace> -o <output-dir>
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

## Target Architecture (v2)

```
arborist
├── cmd/arborist/
│   └── main.go                 # CLI entry point (Kong)
├── internal/
│   ├── commands/               # Subcommand implementations
│   │   ├── status.go           # arborist status
│   │   ├── topology.go         # arborist topology
│   │   ├── health.go           # arborist health
│   │   ├── generate.go         # arborist generate (AIC integration)
│   │   ├── plan.go             # arborist plan show/diff
│   │   ├── compare.go          # arborist compare (plan vs actual)
│   │   ├── metrics.go          # arborist metrics (Prometheus)
│   │   └── diag.go             # arborist diag (existing diagnostics)
│   ├── tui/                    # Terminal UI (Bubble Tea)
│   │   ├── app.go              # Main TUI application
│   │   ├── views/
│   │   │   ├── hierarchy.go    # PCS → PCLQ → Pod tree view
│   │   │   ├── topology.go     # Rack/node heatmap view
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
│   ├── metrics/                # Prometheus integration
│   │   ├── scraper.go          # Metrics scraping
│   │   └── sla.go              # SLA comparison
│   └── diagnostics/            # Existing diagnostics (from v1)
│       └── ...
└── pkg/
    └── types/                  # Shared types
```

---

## Feature Specifications

### P0: Parity Features

#### P0.1: `arborist status`

**Goal:** Match RBG's `kubectl rbg status` command.

**Usage:**
```bash
arborist status <podcliqueset-name> [-n namespace]
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
my-inference-0    Running    PlacementScore: 0.95

∑ Summary: 3 cliques | 9/9 Ready | 1/1 Gangs Running
```

**Data Sources:**
- `PodCliqueSet.Status.AvailableReplicas`
- `PodClique.Status.ReadyReplicas`, `ScheduledReplicas`, `ScheduleGatedReplicas`
- `PodGang.Status.Phase`, `PlacementScore`
- `PodClique.Spec.MinAvailable`

**Implementation Notes:**
- Use progress bar rendering similar to RBG (`strings.Repeat("█", filled)`)
- Show MinAvailable thresholds
- Highlight unhealthy cliques
- Display PlacementScore (Grove-unique)

---

#### P0.2: `arborist generate`

**Goal:** Match RBG's `kubectl rbg llm generate` command - integrate with AIConfigurator.

**Usage:**
```bash
arborist generate \
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
  arborist plan store my-inference -f /tmp/output/qwen3-32b-sglang-disagg.yaml
```

**Implementation Notes:**
- Reference RBG's implementation: `rbg/cmd/cli/cmd/llm/generate/`
- Support same backends: sglang, vllm, trtllm
- Generate Grove-native `PodCliqueSet` manifests (not RBG's `RoleBasedGroup`)
- Store expected performance metrics for later comparison

---

### P1: Differentiation Features

#### P1.1: `arborist topology`

**Goal:** Visualize pod placement across cluster topology (RBG doesn't have this).

**Usage:**
```bash
arborist topology <podcliqueset-name> [-n namespace]
arborist topology <podcliqueset-name> --watch  # Live updates
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
- Node GPU allocations from `nvidia.com/gpu` resources

**Implementation Notes:**
- Parse ClusterTopology to understand level hierarchy
- Group pods by topology domain (rack, host, etc.)
- Color-code by PlacementScore (green=optimal, yellow=suboptimal, red=poor)
- Support `--watch` for real-time updates using K8s watch API

---

#### P1.2: `arborist health`

**Goal:** Monitor gang health with termination countdown (RBG doesn't have this).

**Usage:**
```bash
arborist health <podcliqueset-name> [-n namespace]
arborist health --all  # All PodCliqueSets in namespace
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
│   router    1/1 ready  (min: 1)  ✓ Above threshold                │
└───────────────────────────────────────────────────────────────────┘

┌─ PodGang: my-inference-1 ─────────────────────────────────────────┐
│ Phase: Running    PlacementScore: 0.72                            │
│ Status: ⚠ UNHEALTHY (Termination in 3h 42m)                       │
│                                                                   │
│ Clique Health:                                                    │
│   prefill   2/3 ready  (min: 2)  ⚠ AT threshold                   │
│   decode    5/5 ready  (min: 3)  ✓ Above threshold                │
│                                                                   │
│ Issues:                                                           │
│   - prefill-2: Pending (Unschedulable: insufficient GPU)          │
│                                                                   │
│ Conditions:                                                       │
│   Unhealthy: True (since 18m ago)                                 │
│   DisruptionTarget: False                                         │
│                                                                   │
│ TerminationDelay: 4h (configured)                                 │
│ Time Remaining: 3h 42m ████████████░░░░░░░░ 78%                   │
└───────────────────────────────────────────────────────────────────┘

Summary: 1/2 gangs healthy | 1 gang at risk of termination
```

**Data Sources:**
- `PodGang.Status.Conditions` (Unhealthy, DisruptionTarget)
- `PodCliqueSet.Spec.Template.TerminationDelay`
- `PodClique.Status.ReadyReplicas` vs `Spec.MinAvailable`
- Condition timestamps for countdown calculation

**Implementation Notes:**
- Calculate time remaining from Unhealthy condition timestamp + TerminationDelay
- Show progress bar for termination countdown
- Highlight pods causing health issues
- Support `--watch` for real-time monitoring

---

#### P1.3: `arborist plan`

**Goal:** Store and display AIConfigurator plans for later comparison.

**Usage:**
```bash
# Store a plan
arborist plan store <name> -f <yaml-or-json> [--expected-throughput X] [--expected-ttft Y]

# Show stored plan
arborist plan show <podcliqueset-name>

# Compare plan to deployed
arborist plan diff <podcliqueset-name>
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

**`arborist plan show` Output:**
```
AIConfigurator Plan for: my-inference
Stored: 2026-01-15 14:30:00

Model: QWEN3_32B
System: h200_sxm
Mode: disagg

Configuration:
  Prefill Workers: 4 (tp1)
  Decode Workers: 1 (tp4)

Expected Performance:
  Throughput: 804.83 tok/s/gpu
  TTFT: 486.53ms
  TPOT: 9.16ms

SLA Targets:
  TTFT: ≤300ms
  TPOT: ≤10ms
```

**`arborist plan diff` Output:**
```
Plan vs Deployed Comparison: my-inference

┌─────────────────────┬──────────────┬──────────────┬────────────┐
│ Parameter           │ Planned      │ Deployed     │ Match      │
├─────────────────────┼──────────────┼──────────────┼────────────┤
│ Prefill Workers     │ 4            │ 4            │ ✓          │
│ Decode Workers      │ 1            │ 1            │ ✓          │
│ Prefill TP          │ 1            │ 1            │ ✓          │
│ Decode TP           │ 4            │ 4            │ ✓          │
│ Total GPUs          │ 8            │ 8            │ ✓          │
└─────────────────────┴──────────────┴──────────────┴────────────┘

✓ Deployment matches plan
```

---

### P2: Leapfrog Features

#### P2.1: `arborist compare`

**Goal:** Compare AIConfigurator predictions to actual runtime performance.

**Prerequisites:**
- P1.3 (`arborist plan`) for plan storage
- P2.2 (`arborist metrics`) for runtime metrics

**Usage:**
```bash
arborist compare <podcliqueset-name> [-n namespace]
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
  PlacementScore 0.72 indicates suboptimal pod placement.
  Prefill pods are split across 2 racks, adding ~40ms network latency.

Root Cause Analysis:
  - Expected placement: all pods in single rack
  - Actual placement: rack-1 (6 pods), rack-2 (2 pods)
  - Impact: +14% TTFT due to cross-rack KV transfer

Recommendations:
  1. [HIGH] Reschedule prefill-2, prefill-3 to rack-1
     Command: kubectl delete pod my-inference-0-prefill-2 my-inference-0-prefill-3
     Expected: PlacementScore → 0.95, TTFT → 280ms

  2. [MEDIUM] Consider reducing decode workers (utilization at 45%)
     Command: Edit PodCliqueSet to reduce decode replicas
     Expected: Cost savings with no performance impact
```

---

#### P2.2: `arborist metrics`

**Goal:** Scrape Prometheus metrics from inference engine pods.

**Usage:**
```bash
arborist metrics <podcliqueset-name> [-n namespace]
arborist metrics <podcliqueset-name> --watch
arborist metrics <podcliqueset-name> --json  # Machine-readable
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
  Inflight Requests: 36

Per-Role Metrics:
  prefill:
    Batch Size (avg): 4.2
    Token Usage: 78%
  decode:
    Batch Size (avg): 32.1
    Token Usage: 45%  ⚠ Underutilized
```

**Data Sources:**
- SGLang metrics: `sglang_*` Prometheus metrics
- vLLM metrics: `vllm:*` Prometheus metrics
- Custom metrics endpoint on port 8000

**Implementation Notes:**
- Auto-detect inference engine (SGLang, vLLM, TRT-LLM)
- Scrape metrics from pod IPs directly or via ServiceMonitor
- Compare against SLA targets from stored plan
- Support streaming output with `--watch`

---

#### P2.3: `arborist tui`

**Goal:** Interactive terminal UI for real-time monitoring.

**Usage:**
```bash
arborist tui [-n namespace]
```

**Interface:**
```
┌─ arborist ─────────────────────────────────────────────────────────────────┐
│ Namespace: vllm-disagg    Cluster: prod-us-west-2                          │
├────────────────────────────────────────────────────────────────────────────┤
│ [1] Hierarchy  [2] Topology  [3] Health  [4] Metrics  [5] Updates  [q]uit │
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

**Implementation Notes:**
- Use [Bubble Tea](https://github.com/charmbracelet/bubbletea) for TUI framework
- Use [Lip Gloss](https://github.com/charmbracelet/lipgloss) for styling
- Implement K8s watch for real-time updates
- Support vim-style navigation (j/k, gg, G)

---

### P3: Future Features

#### P3.1: `arborist recommend`

AI-powered recommendations based on metrics + topology + plan.

#### P3.2: Dashboard Mode

Web-based dashboard (like srtctl's Streamlit dashboard) for team visibility.

#### P3.3: k9s Plugin

Integration as k9s plugin for users already using k9s.

---

## Technical Specifications

### Dependencies

```go
// go.mod additions
require (
    github.com/alecthomas/kong v1.13.0       // CLI framework (existing)
    github.com/charmbracelet/bubbletea v1.x  // TUI framework
    github.com/charmbracelet/lipgloss v1.x   // TUI styling
    github.com/charmbracelet/bubbles v0.x    // TUI components
    k8s.io/client-go v0.34.x                 // K8s client (existing)
    github.com/prometheus/client_golang v1.x // Prometheus client
)
```

### Configuration

```yaml
# ~/.arborist/config.yaml
defaults:
  namespace: default
  operator_namespace: grove-system

metrics:
  scrape_interval: 15s
  prometheus_port: 8000

tui:
  refresh_interval: 2s
  color_scheme: auto  # auto, light, dark

aiconfigurator:
  path: aiconfigurator  # or full path
  default_backend: sglang
```

### Testing Strategy

1. **Unit Tests:** Command parsing, output formatting, plan diffing
2. **Integration Tests:** K8s client interactions with envtest
3. **E2E Tests:** Full workflow with kind cluster + sample PodCliqueSets

---

## Implementation Roadmap

### Phase 0: Foundation (Week 1-2)
- [ ] Restructure CLI with subcommands (Kong)
- [ ] Add `arborist status` command
- [ ] Add basic progress bar rendering
- [ ] Set up test infrastructure

### Phase 1: AIConfigurator Integration (Week 3-4)
- [ ] Add `arborist generate` command
- [ ] Implement AIConfigurator executor
- [ ] Implement Grove manifest renderer
- [ ] Add `arborist plan store/show/diff` commands

### Phase 2: Topology Visualization (Week 5-6)
- [ ] Add `arborist topology` command
- [ ] Implement ClusterTopology parsing
- [ ] Implement node/rack grouping logic
- [ ] Add PlacementScore visualization

### Phase 3: Health Monitoring (Week 7-8)
- [ ] Add `arborist health` command
- [ ] Implement termination countdown
- [ ] Add condition parsing
- [ ] Support `--watch` mode

### Phase 4: TUI Mode (Week 9-12)
- [ ] Set up Bubble Tea framework
- [ ] Implement hierarchy view
- [ ] Implement topology view
- [ ] Implement health view
- [ ] Add real-time watch updates

### Phase 5: Metrics Integration (Week 13-16)
- [ ] Add Prometheus scraping
- [ ] Implement `arborist metrics` command
- [ ] Add SLA comparison
- [ ] Implement `arborist compare` command

---

## Success Metrics

1. **Adoption:** 50% of Grove users using arborist within 3 months
2. **Issue Resolution:** 40% reduction in time-to-diagnosis for deployment issues
3. **Competitive:** Feature parity with RBG CLI + 3 differentiating features
4. **NPS:** Positive feedback on topology visualization and plan comparison

---

## References

- [RBG CLI Implementation](https://github.com/kubernetes-sigs/rbgs/tree/main/cmd/cli)
- [AIConfigurator](https://github.com/ai-dynamo/aiconfigurator)
- [srtctl Dashboard](https://github.com/ishandhanani/srt-slurm) - Inspiration for visualization
- [Bubble Tea](https://github.com/charmbracelet/bubbletea) - TUI framework
- [Grove API Types](../api/core/v1alpha1/) - PodCliqueSet, PodClique, PodGang

---

## Appendix: Learnings from srtctl

The [srtctl](https://github.com/ishandhanani/srt-slurm) project provides useful patterns:

1. **Declarative YAML Configuration:** Replace complex CLI flags with YAML config files
2. **Filtering UI:** Sidebar filters for GPU type, topology, ISL/OSL, tags
3. **Pareto Visualization:** Show throughput vs latency tradeoffs
4. **Run Comparison:** Compare multiple configurations side-by-side
5. **Tagging System:** Tag runs for organization and filtering

**Applicable to Arborist:**
- Pareto graph for showing PlacementScore vs throughput tradeoffs
- Topology filtering in TUI mode
- Configuration comparison between deployments
- Tagging PodCliqueSets for organization

---

## Changelog

- **2026-01-15:** Initial draft

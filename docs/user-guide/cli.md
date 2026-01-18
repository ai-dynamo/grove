# kubectl-grove CLI Reference

kubectl-grove is a kubectl plugin for managing Grove AI inference workloads on Kubernetes.

## Installation

### From Source

```bash
cd cli-plugin
go build -o kubectl-grove .
mv kubectl-grove /usr/local/bin/
```

### Verify Installation

```bash
kubectl grove --version
```

## Shell Completion

Enable shell completion for better command-line experience:

### Bash

```bash
# Add to ~/.bashrc
eval "$(kubectl-grove completion bash)"
```

### Zsh

```bash
# Add to ~/.zshrc
eval "$(kubectl-grove completion zsh)"
```

### Fish

```bash
kubectl-grove completion fish > ~/.config/fish/completions/kubectl-grove.fish
```

## Global Flags

| Flag | Description |
|------|-------------|
| `--kubeconfig FILE` | Path to kubeconfig file (default: `$KUBECONFIG` or `~/.kube/config`) |
| `--context NAME` | Kubernetes context to use |
| `-v, --version` | Print version and exit |

## Commands

### status

Show status of Grove PodCliqueSets.

```bash
# Show status of a specific PodCliqueSet
kubectl grove status my-inference -n my-namespace

# Show status of all PodCliqueSets in namespace
kubectl grove status -a -n my-namespace
```

**Flags:**
- `-n, --namespace`: Namespace to query (default: from kubeconfig context or "default")
- `-a, --all`: Show all PodCliqueSets in the namespace

### topology

Visualize pod placement topology showing how pods are distributed across nodes and racks.

```bash
# One-time topology view
kubectl grove topology my-inference -n my-namespace

# Watch mode with auto-refresh
kubectl grove topology my-inference -n my-namespace -w
```

**Flags:**
- `-n, --namespace`: Namespace to query
- `-w, --watch`: Watch for changes and update display every 2 seconds

**Output includes:**
- ClusterTopology hierarchy (rack -> host)
- PodCliqueSet info with placement score
- GPU allocation per node with visual bars
- Pod status with role badges ([P] prefill, [D] decode)
- Fragmentation warnings

### health

Show gang health dashboard with pod and scheduling status.

```bash
# Health for specific PodCliqueSet
kubectl grove health my-inference -n my-namespace

# Health for all PodCliqueSets
kubectl grove health -a -n my-namespace

# Watch mode
kubectl grove health my-inference -n my-namespace -w
```

**Flags:**
- `-n, --namespace`: Namespace to query
- `-a, --all`: Show all PodCliqueSets
- `-w, --watch`: Watch for changes

### metrics

Show live inference metrics from pods (requires metrics endpoint on pods).

```bash
# View metrics
kubectl grove metrics my-inference -n my-namespace

# Filter by role
kubectl grove metrics my-inference -n my-namespace --role prefill

# JSON output
kubectl grove metrics my-inference -n my-namespace --json

# Watch mode
kubectl grove metrics my-inference -n my-namespace -w
```

**Flags:**
- `-n, --namespace`: Namespace to query
- `-w, --watch`: Watch for changes
- `--json`: Output in JSON format
- `--role`: Filter by role (e.g., prefill, decode)

### tui

Launch the interactive terminal UI (Arborist) for exploring Grove resources.

```bash
kubectl grove tui -n my-namespace
```

**Navigation:**
- `Tab`: Switch between Resources and Events panes
- `Enter`: Drill down into selected resource
- `Esc`: Navigate back up the hierarchy
- `↑/↓`: Navigate items
- `q`: Quit

**Hierarchy:**
```
Forest (all PodCliqueSets)
  └── PodCliqueSet
        └── PodCliqueSetReplica
              └── PodClique → Pods
```

### generate

Generate Grove PodCliqueSet manifests using AIConfigurator.

**Prerequisites:** Install AIConfigurator: `pip install aiconfigurator`

```bash
kubectl grove generate \
  --model QWEN3_32B \
  --system h200_sxm \
  --total-gpus 32 \
  --backend sglang \
  --isl 4000 \
  --osl 1000 \
  --ttft 300 \
  --tpot 10 \
  --save-dir ./output \
  -n my-namespace
```

**Required Flags:**
- `--model`: Model name (e.g., QWEN3_32B, LLAMA3_70B)
- `--system`: Hardware system type (e.g., h200_sxm, h100_sxm, a100_sxm)
- `--total-gpus`: Total number of GPUs available
- `--backend`: Inference backend (sglang, vllm, trtllm)
- `--save-dir`: Output directory for generated manifests

**Optional Flags:**
- `--hf-id`: HuggingFace model ID (e.g., Qwen/Qwen3-32B)
- `--decode-system`: Hardware system for decode workers (disagg mode)
- `--backend-version`: Specific backend version
- `--isl`: Input sequence length (default: 4000)
- `--osl`: Output sequence length (default: 1000)
- `--prefix`: Prefix cache length (default: 0)
- `--ttft`: Target time-to-first-token in ms (default: 2000)
- `--tpot`: Target time-per-output-token in ms (default: 30)
- `--request-latency`: End-to-end request latency target in ms
- `--database-mode`: AIConfigurator database mode (SILICON, HYBRID, EMPIRICAL, SOL)
- `-n, --namespace`: Kubernetes namespace for generated manifests
- `--image`: Container image for worker pods
- `--debug`: Enable debug mode

**Output:**
Generates two PodCliqueSet manifests:
- `{model}-{backend}-disagg-pcs.yaml`: Disaggregated mode (separate prefill/decode)
- `{model}-{backend}-agg-pcs.yaml`: Aggregated mode (combined workers)

### plan

Manage AIConfigurator deployment plans stored as ConfigMaps.

#### plan store

Store a deployment plan for later comparison.

```bash
kubectl grove plan store my-inference -f ./output/qwen3-32b-sglang-disagg-pcs.yaml -n my-namespace
```

**Flags:**
- `-f, --file`: Path to the plan file (required)
- `-n, --namespace`: Namespace for the plan

#### plan show

Display a stored plan.

```bash
kubectl grove plan show my-inference -n my-namespace
```

#### plan diff

Compare a stored plan with the actual deployed configuration.

```bash
kubectl grove plan diff my-inference -n my-namespace
```

### compare

Compare a stored plan with the actual deployment, showing differences.

```bash
# Basic comparison
kubectl grove compare my-inference -n my-namespace

# JSON output
kubectl grove compare my-inference -n my-namespace --json

# Verbose output with per-pod placement
kubectl grove compare my-inference -n my-namespace --verbose
```

**Flags:**
- `-n, --namespace`: Namespace to query
- `--json`: Output in JSON format
- `--verbose`: Show verbose output including per-pod placement

### diagnostics

Collect diagnostic information from Grove resources for troubleshooting.

```bash
kubectl grove diagnostics -n my-namespace -o ./diagnostics-output
```

**Flags:**
- `-n, --namespace`: Namespace to collect diagnostics from
- `-o, --output-dir`: Output directory (default: ./grove-diagnostics-{timestamp})
- `--operator-namespace`: Namespace where Grove operator is deployed (default: grove-system)

**Collected Information:**
- PodCliqueSet, PodClique, PodCliqueScalingGroup resources
- PodGang, PodGangSet resources
- Pod logs and status
- Events
- Operator logs

## Examples

### Deploy a New Inference Workload

```bash
# 1. Generate manifests
kubectl grove generate \
  --model QWEN3_32B \
  --hf-id Qwen/Qwen3-32B \
  --system h200_sxm \
  --total-gpus 32 \
  --backend sglang \
  --save-dir ./manifests \
  -n inference

# 2. Review generated manifests
cat ./manifests/qwen3-32b-sglang-disagg-pcs.yaml

# 3. Store the plan for later comparison
kubectl grove plan store qwen3-inference \
  -f ./manifests/qwen3-32b-sglang-disagg-pcs.yaml \
  -n inference

# 4. Deploy
kubectl apply -f ./manifests/qwen3-32b-sglang-disagg-pcs.yaml

# 5. Monitor deployment
kubectl grove status qwen3-inference -n inference
kubectl grove topology qwen3-inference -n inference -w
```

### Troubleshoot a Deployment

```bash
# Check health status
kubectl grove health my-inference -n my-namespace

# View topology and placement
kubectl grove topology my-inference -n my-namespace

# Compare with original plan
kubectl grove compare my-inference -n my-namespace --verbose

# Collect diagnostics for support
kubectl grove diagnostics -n my-namespace -o ./debug-output
```

### Monitor Running Workloads

```bash
# Use TUI for interactive exploration
kubectl grove tui -n my-namespace

# Or use watch mode for specific views
kubectl grove health -a -n my-namespace -w
kubectl grove topology my-inference -n my-namespace -w
```

## Namespace Resolution

The CLI respects namespace configuration in the following order:
1. Explicit `-n/--namespace` flag
2. Namespace from current kubeconfig context
3. Falls back to "default"

This means if your kubeconfig context has a namespace set, you don't need to specify `-n` for every command.

## Troubleshooting

### "aiconfigurator is not installed"

Install AIConfigurator:
```bash
pip install aiconfigurator
```

Verify installation:
```bash
aiconfigurator --version
```

### "failed to build Kubernetes clients"

Check your kubeconfig:
```bash
kubectl config current-context
kubectl cluster-info
```

### No PodCliqueSets found

Verify Grove CRDs are installed:
```bash
kubectl get crd | grep grove
```

Expected CRDs:
- podcliquesets.grove.io
- podcliques.grove.io
- podcliquescalinggroups.grove.io

# Grove k9s Integration

This directory contains k9s plugins and aliases for Grove resources.

## Installation

### 1. Install kubectl-grove

```bash
# Build from source
cd operator
go build -o kubectl-grove ./cmd/kubectl-grove
sudo mv kubectl-grove /usr/local/bin/

# Verify
kubectl-grove --help
```

### 2. Install k9s Plugins

```bash
# Create k9s config directory if it doesn't exist
mkdir -p ~/.config/k9s

# Copy or merge plugins
cp k9s/plugins.yaml ~/.config/k9s/plugins.yaml

# Or merge with existing plugins
cat k9s/plugins.yaml >> ~/.config/k9s/plugins.yaml
```

### 3. Install k9s Aliases

```bash
# Copy or merge aliases
cp k9s/aliases.yaml ~/.config/k9s/aliases.yaml

# Or merge with existing aliases
cat k9s/aliases.yaml >> ~/.config/k9s/aliases.yaml
```

### 4. Restart k9s

k9s will automatically pick up new plugins. Aliases require a restart.

## Usage

### Quick Navigation (Aliases)

| Alias | Resource |
|-------|----------|
| `:pcs` | PodCliqueSets |
| `:pc` | PodCliques |
| `:pg` | PodGangs |
| `:ct` | ClusterTopologies |
| `:pcsg` | PodCliqueScalingGroups |

### Plugin Shortcuts

When viewing **PodCliqueSets**:

| Shortcut | Action |
|----------|--------|
| `Shift-T` | Show topology visualization |
| `Shift-S` | Show detailed status |
| `Shift-H` | Show health dashboard |
| `Shift-M` | Show inference metrics |
| `Shift-C` | Compare plan vs actual |
| `Shift-W` | Watch topology (live) |
| `Ctrl-H` | Watch health (live) |
| `Ctrl-M` | Watch metrics (live) |

When viewing **any resource**:

| Shortcut | Action |
|----------|--------|
| `Ctrl-G` | Open Grove TUI |
| `Ctrl-Shift-S` | Show all PodCliqueSet status |
| `Ctrl-Shift-D` | Collect diagnostics |

When viewing **PodCliques**:

| Shortcut | Action |
|----------|--------|
| `Shift-H` | Show parent PodCliqueSet health |

When viewing **PodGangs**:

| Shortcut | Action |
|----------|--------|
| `Shift-T` | Show parent PodCliqueSet topology |

## Example Workflow

```
1. Start k9s
2. Type :pcs to view PodCliqueSets
3. Navigate to a PodCliqueSet
4. Press Shift-T to see topology visualization
5. Press Shift-H for health dashboard
6. Press Shift-M for live inference metrics
```

## Topology Visualization

The topology command now includes ANSI colors:

- **Green**: Running pods, optimal placement, high scores
- **Yellow**: Pending pods, warning states
- **Red**: Failed pods, fragmented placement, low scores
- **Cyan**: Prefill role `[P]`
- **Magenta**: Decode role `[D]`

GPU usage bars show utilization with color coding:
- `[████░░░░]` Green: 30-70% utilization
- `[██████░░]` Yellow: 70-90% utilization
- `[████████]` Red: 90%+ utilization

## Troubleshooting

### Plugins not showing up

1. Verify kubectl-grove is in PATH: `which kubectl-grove`
2. Check plugins.yaml syntax: `cat ~/.config/k9s/plugins.yaml | yq .`
3. Ensure scopes match resource types

### Aliases not working

1. Restart k9s completely
2. Verify CRDs are installed: `kubectl get crd | grep grove`
3. Check aliases.yaml syntax

### Colors not displaying

- Set `TERM=xterm-256color` in your terminal
- Disable with `NO_COLOR=1` environment variable

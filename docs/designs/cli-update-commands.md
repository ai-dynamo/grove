# Design: Grove CLI Update Commands

## Overview

This document proposes CLI commands for managing rolling updates, in-place updates, and scaling operations for Grove PodCliqueSets.

## Design Principles

1. **Kubernetes-native**: Leverage existing K8s primitives where possible
2. **Inference-aware**: Consider latency impact, placement quality, and inference continuity
3. **Progressive disclosure**: Simple commands for common cases, advanced options for power users
4. **Visibility**: Always show what's happening and what will happen

## Proposed Commands

### 1. `kubectl grove rollout` - Manage Rollouts

Similar to RBG and native Kubernetes, but with inference-specific enhancements.

```bash
# View rollout status with inference metrics
kubectl grove rollout status my-inference -n prod

# View revision history
kubectl grove rollout history my-inference -n prod

# Compare current state with a revision
kubectl grove rollout diff my-inference --revision=2 -n prod

# Rollback to previous revision
kubectl grove rollout undo my-inference -n prod

# Rollback to specific revision
kubectl grove rollout undo my-inference --revision=3 -n prod

# Pause an in-progress rollout
kubectl grove rollout pause my-inference -n prod

# Resume a paused rollout
kubectl grove rollout resume my-inference -n prod
```

#### `rollout status` Output (Inference-Aware)

```
Rollout Status: my-inference (namespace: prod)
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

Revision: 4 → 5 (in progress)
Strategy: RollingUpdate (maxUnavailable: 1, maxSurge: 0)

Role Progress:
  prefill   [████████████░░░░░░░░] 3/5 updated   TTFT p99: 245ms ↑
  decode    [████████░░░░░░░░░░░░] 2/8 updated   TPOT p99: 12ms  →

Placement Score: 0.92 → 0.88 (temporary degradation)

Events:
  2m ago   Pod prefill-3 updated (image: v1.2.0 → v1.3.0)
  1m ago   Pod prefill-3 ready, TTFT stabilized
  30s ago  Pod prefill-4 updating...

Estimated completion: ~4 minutes
Press Ctrl+C to exit, rollout continues in background
```

### 2. `kubectl grove scale` - Scale Workers

Dedicated scaling command with role-aware options.

```bash
# Scale all workers proportionally
kubectl grove scale my-inference --replicas=16 -n prod

# Scale specific roles
kubectl grove scale my-inference --prefill=4 --decode=8 -n prod

# Scale with dry-run (preview)
kubectl grove scale my-inference --prefill=6 --dry-run -n prod

# Scale down with drain (graceful)
kubectl grove scale my-inference --decode=4 --drain-timeout=60s -n prod
```

#### Scale Output

```
Scaling my-inference (namespace: prod)
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

Current → Target:
  prefill:  4 → 6  (+2)
  decode:   8 → 8  (unchanged)

Resource Impact:
  GPUs:     32 → 40 (+8)

Placement Analysis:
  ✓ Sufficient capacity in rack-1 for 2 additional prefill workers
  ✓ Placement score will remain at 0.95

Proceed? [y/N]: y

Scaling prefill from 4 to 6...
  ✓ prefill-4 scheduled on node-5 (rack-1)
  ✓ prefill-5 scheduled on node-6 (rack-1)

Scale complete. New configuration:
  prefill: 6 (all ready)
  decode:  8 (all ready)
```

### 3. `kubectl grove update` - High-Level Update Command

Convenience wrapper for common update operations with validation.

```bash
# Update container image
kubectl grove update my-inference --image=nvcr.io/nvidia/vllm:v1.3.0 -n prod

# Update with role targeting
kubectl grove update my-inference --image=vllm:v1.3.0 --role=prefill -n prod

# Update with placement constraint
kubectl grove update my-inference --image=vllm:v1.3.0 --preserve-placement -n prod

# Update with approval workflow
kubectl grove update my-inference --image=vllm:v1.3.0 --wait-for-approval -n prod

# Preview what will change
kubectl grove update my-inference --image=vllm:v1.3.0 --dry-run -n prod
```

#### Update Dry-Run Output

```
Update Preview: my-inference (namespace: prod)
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

Changes:
  spec.template.cliques[0].spec.podSpec.containers[0].image:
    - nvcr.io/nvidia/vllm:v1.2.0
    + nvcr.io/nvidia/vllm:v1.3.0

  spec.template.cliques[1].spec.podSpec.containers[0].image:
    - nvcr.io/nvidia/vllm:v1.2.0
    + nvcr.io/nvidia/vllm:v1.3.0

Affected Pods: 13 (5 prefill, 8 decode)

Update Strategy: RollingUpdate
  - maxUnavailable: 1
  - Order: prefill first, then decode

Estimated Duration: ~10 minutes
Estimated Latency Impact: TTFT +50-100ms during transition

To apply: kubectl grove update my-inference --image=nvcr.io/nvidia/vllm:v1.3.0 -n prod
```

### 4. `kubectl grove restart` - Rolling Restart

Restart pods without spec changes (useful for model weight reload, cache clearing).

```bash
# Rolling restart all pods
kubectl grove restart my-inference -n prod

# Restart specific role only
kubectl grove restart my-inference --role=prefill -n prod

# Restart with custom batch size
kubectl grove restart my-inference --batch-size=2 -n prod

# Force restart (delete and recreate vs graceful)
kubectl grove restart my-inference --force -n prod
```

### 5. `kubectl grove apply` - Apply Configuration

Enhanced apply with validation and preview.

```bash
# Apply new configuration from file
kubectl grove apply -f updated-pcs.yaml -n prod

# Apply with diff preview
kubectl grove apply -f updated-pcs.yaml --diff -n prod

# Apply with plan comparison
kubectl grove apply -f updated-pcs.yaml --compare-plan=my-inference -n prod
```

## Advanced Features

### Canary Deployments

```bash
# Start canary with 1 pod
kubectl grove rollout canary my-inference --image=vllm:v1.3.0 --pods=1 -n prod

# Promote canary to full rollout
kubectl grove rollout promote my-inference -n prod

# Abort canary
kubectl grove rollout abort my-inference -n prod
```

### Placement-Aware Updates

```bash
# Update while maintaining topology constraints
kubectl grove update my-inference --image=vllm:v1.3.0 --preserve-placement -n prod

# Update with rack-at-a-time strategy
kubectl grove update my-inference --image=vllm:v1.3.0 --rack-by-rack -n prod
```

### Metrics-Gated Rollouts

```bash
# Only continue rollout if latency stays below threshold
kubectl grove rollout start my-inference \
  --image=vllm:v1.3.0 \
  --gate-ttft-p99=500ms \
  --gate-tpot-p99=20ms \
  -n prod
```

## Command Summary

| Command | Description |
|---------|-------------|
| `rollout status` | Show rollout progress with inference metrics |
| `rollout history` | List revision history |
| `rollout diff` | Compare revisions |
| `rollout undo` | Rollback to previous/specific revision |
| `rollout pause` | Pause in-progress rollout |
| `rollout resume` | Resume paused rollout |
| `scale` | Scale workers by role |
| `update` | High-level update with validation |
| `restart` | Rolling restart without spec change |
| `apply` | Apply configuration with preview |

## Implementation Priority

### Phase 1 (MVP)
1. `rollout status` - Essential visibility
2. `rollout history` - Basic revision tracking
3. `rollout undo` - Safety net for rollbacks
4. `scale` - Core scaling operations

### Phase 2 (Enhanced UX)
1. `rollout diff` - Change preview
2. `rollout pause/resume` - Rollout control
3. `update --dry-run` - Preview changes
4. `restart` - Convenience command

### Phase 3 (Advanced)
1. Canary deployments
2. Metrics-gated rollouts
3. Placement-aware updates
4. `apply` with plan comparison

## Open Questions

1. **Revision Storage**: Use ControllerRevision (like RBG) or custom CRD?
2. **Metrics Integration**: Should rollout status show live metrics or historical?
3. **Placement During Updates**: How aggressive should we be about maintaining placement?
4. **Multi-Cluster**: Any considerations for federated deployments?

## References

- [RBG CLI Implementation](../../../rbg/cmd/cli/)
- [Kubernetes Deployment Rollouts](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#rolling-update-deployment)
- [Argo Rollouts](https://argoproj.github.io/argo-rollouts/) - Advanced deployment strategies

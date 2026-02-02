#!/bin/bash
# /*
# Copyright 2025 The Grove Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
# */

#
# create-e2e-cluster.sh - k3d cluster setup for local E2E testing
#
# This script creates and configures a k3d cluster for Grove E2E tests.
# It is OPTIONAL - the E2E tests work with any Kubernetes cluster that has:
#   - Grove operator deployed and ready
#   - Kai scheduler deployed and ready
#   - Required node labels and topology configuration
#
# This script is provided as a convenience for local development and CI.
# For other cluster types (kind, minikube, EKS, GKE, etc.), manually deploy
# the required components and configure kubectl/KUBECONFIG.
#
# USAGE:
#   ./hack/create-e2e-cluster.sh [OPTIONS]
#
# OPTIONS:
#   --skip-kai          Skip Kai Scheduler installation
#   --skip-grove        Skip Grove operator deployment
#   --skip-topology     Skip topology label application
#   --delete            Delete the cluster and exit
#   --help              Show this help message
#
# ENVIRONMENT VARIABLES (can override defaults):
#   E2E_CLUSTER_NAME      Cluster name (default: shared-e2e-test-cluster)
#   E2E_REGISTRY_PORT     Registry port (default: 5001)
#   E2E_API_PORT          Kubernetes API port (default: 6560)
#   E2E_LB_PORT           Load balancer port mapping (default: 8090:80)
#   E2E_WORKER_NODES      Number of worker nodes (default: 30)
#   E2E_WORKER_MEMORY     Worker node memory (default: 150m)
#   E2E_K3S_IMAGE         K3s image (default: rancher/k3s:v1.33.5-k3s1)
#   E2E_KAI_VERSION       Kai Scheduler version (default: v0.13.0-rc1)
#   E2E_MAX_RETRIES       Max cluster creation retries (default: 3)
#   E2E_SKAFFOLD_PROFILE  Skaffold profile for Grove (default: topology-test)
#
# OUTPUT:
#   After successful creation, you can run E2E tests. If using a local registry:
#
#   export E2E_REGISTRY_PORT=5001
#
# EXAMPLES:
#   # Create cluster with defaults (recommended for local development)
#   ./hack/create-e2e-cluster.sh
#
#   # Create cluster with custom name
#   E2E_CLUSTER_NAME=my-test-cluster ./hack/create-e2e-cluster.sh
#
#   # Create cluster, skip Grove deployment (for manual testing)
#   ./hack/create-e2e-cluster.sh --skip-grove
#
#   # Delete an existing cluster
#   ./hack/create-e2e-cluster.sh --delete
#
# USING OTHER CLUSTER TYPES:
#   For non-k3d clusters, ensure the following are deployed:
#   1. Grove operator (in grove-system namespace)
#   2. Kai scheduler (in kai-scheduler namespace)
#   3. Worker nodes with label: node_role.e2e.grove.nvidia.com=agent
#   4. Topology labels: kubernetes.io/zone, kubernetes.io/block, kubernetes.io/rack
#
#   Then run tests with:
#   export E2E_REGISTRY_PORT=<your-registry-port>  # if using local registry
#   make test-e2e
#

set -euo pipefail

# Script directory resolution
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
OPERATOR_DIR="$REPO_ROOT/operator"

# ============================================================================
# Configuration - defaults can be overridden via environment variables
# ============================================================================

# Cluster configuration
CLUSTER_NAME="${E2E_CLUSTER_NAME:-shared-e2e-test-cluster}"
REGISTRY_PORT="${E2E_REGISTRY_PORT:-5001}"
API_PORT="${E2E_API_PORT:-6560}"
LB_PORT="${E2E_LB_PORT:-8090:80}"
WORKER_NODES="${E2E_WORKER_NODES:-30}"
WORKER_MEMORY="${E2E_WORKER_MEMORY:-150m}"
K3S_IMAGE="${E2E_K3S_IMAGE:-rancher/k3s:v1.33.5-k3s1}"

# Component versions
KAI_VERSION="${E2E_KAI_VERSION:-v0.13.0-rc1}"

# Build configuration
SKAFFOLD_PROFILE="${E2E_SKAFFOLD_PROFILE:-topology-test}"

# Retry configuration
MAX_RETRIES="${E2E_MAX_RETRIES:-3}"
CLUSTER_TIMEOUT="120s"

# Topology configuration (from topology.go)
NODES_PER_ZONE=28
NODES_PER_BLOCK=14
NODES_PER_RACK=7

# ============================================================================
# Command line argument parsing
# ============================================================================

SKIP_KAI=false
SKIP_GROVE=false
SKIP_TOPOLOGY=false
DELETE_CLUSTER=false
SHOW_HELP=false

while [[ $# -gt 0 ]]; do
  case $1 in
    --skip-kai)
      SKIP_KAI=true
      shift
      ;;
    --skip-grove)
      SKIP_GROVE=true
      shift
      ;;
    --skip-topology)
      SKIP_TOPOLOGY=true
      shift
      ;;
    --delete)
      DELETE_CLUSTER=true
      shift
      ;;
    --help|-h)
      SHOW_HELP=true
      shift
      ;;
    *)
      echo "ERROR: Unknown option: $1" >&2
      echo "Run with --help for usage information" >&2
      exit 1
      ;;
  esac
done

# ============================================================================
# Help message
# ============================================================================

if [ "$SHOW_HELP" = true ]; then
  # Print the header comment from this file as help
  sed -n '17,72p' "$0"
  exit 0
fi

# ============================================================================
# Utility functions
# ============================================================================

log_info() {
  echo "ℹ️  $*" >&2
}

log_success() {
  echo "✅ $*" >&2
}

log_warn() {
  echo "⚠️  $*" >&2
}

log_error() {
  echo "❌ $*" >&2
}

log_step() {
  echo "" >&2
  echo "==============================================================================" >&2
  echo "  $*" >&2
  echo "==============================================================================" >&2
}

# Check if a command exists
require_command() {
  local cmd="$1"
  if ! command -v "$cmd" &> /dev/null; then
    log_error "Required command '$cmd' not found. Please install it first."
    exit 1
  fi
}

# ============================================================================
# Delete cluster function
# ============================================================================

delete_cluster() {
  log_info "Deleting k3d cluster '$CLUSTER_NAME'..."
  if k3d cluster delete "$CLUSTER_NAME" 2>/dev/null; then
    log_success "Cluster '$CLUSTER_NAME' deleted"
  else
    log_warn "Cluster '$CLUSTER_NAME' not found or already deleted"
  fi
}

if [ "$DELETE_CLUSTER" = true ]; then
  delete_cluster
  exit 0
fi

# ============================================================================
# Prerequisites check
# ============================================================================

log_step "Checking prerequisites"

require_command k3d
require_command kubectl
require_command docker

if [ "$SKIP_KAI" = false ]; then
  require_command helm
fi

if [ "$SKIP_GROVE" = false ]; then
  require_command skaffold
  require_command jq
fi

log_success "All required tools are available"

# ============================================================================
# Prepare charts (required for both Kai and Grove)
# ============================================================================

if [ "$SKIP_GROVE" = false ]; then
  log_step "Preparing Helm charts"
  
  if [ -f "$OPERATOR_DIR/hack/prepare-charts.sh" ]; then
    "$OPERATOR_DIR/hack/prepare-charts.sh"
    log_success "Charts prepared"
  else
    log_warn "prepare-charts.sh not found, skipping chart preparation"
  fi
fi

# ============================================================================
# Create k3d cluster with retry logic
# ============================================================================

log_step "Creating k3d cluster"

log_info "Configuration:"
log_info "  Cluster Name:    $CLUSTER_NAME"
log_info "  Registry Port:   $REGISTRY_PORT"
log_info "  API Port:        $API_PORT"
log_info "  Load Balancer:   $LB_PORT"
log_info "  Worker Nodes:    $WORKER_NODES"
log_info "  Worker Memory:   $WORKER_MEMORY"
log_info "  K3s Image:       $K3S_IMAGE"

RETRY_COUNT=0

until [ $RETRY_COUNT -ge $MAX_RETRIES ]; do
  RETRY_COUNT=$((RETRY_COUNT + 1))
  log_info "Cluster creation attempt $RETRY_COUNT of $MAX_RETRIES..."
  
  # Clean up any partial cluster from previous attempt
  k3d cluster delete "$CLUSTER_NAME" 2>/dev/null || true
  
  # Create k3d cluster with registry
  if k3d cluster create "$CLUSTER_NAME" \
    --servers 1 \
    --agents "$WORKER_NODES" \
    --image "$K3S_IMAGE" \
    --api-port "$API_PORT" \
    --port "$LB_PORT@loadbalancer" \
    --registry-create "registry:0.0.0.0:$REGISTRY_PORT" \
    --k3s-arg "--node-taint=node_role.e2e.grove.nvidia.com=agent:NoSchedule@agent:*" \
    --k3s-node-label "node_role.e2e.grove.nvidia.com=agent@agent:*" \
    --k3s-node-label "nvidia.com/gpu.deploy.operands=false@server:*" \
    --k3s-node-label "nvidia.com/gpu.deploy.operands=false@agent:*" \
    --agents-memory "$WORKER_MEMORY" \
    --timeout "$CLUSTER_TIMEOUT" \
    --wait; then
    log_success "Cluster created successfully on attempt $RETRY_COUNT"
    break
  fi
  
  if [ $RETRY_COUNT -lt $MAX_RETRIES ]; then
    log_warn "Cluster creation failed, retrying in 10 seconds..."
    sleep 10
  else
    log_error "Cluster creation failed after $MAX_RETRIES attempts"
    exit 1
  fi
done

# Wait for nodes to be ready
log_info "Waiting for all nodes to be ready..."
kubectl wait --for=condition=Ready nodes --all --timeout=5m
log_success "All nodes are ready"

# ============================================================================
# Install Kai Scheduler
# ============================================================================

if [ "$SKIP_KAI" = false ]; then
  log_step "Installing Kai Scheduler"
  
  log_info "Version: $KAI_VERSION"
  
  # Delete existing installation if present (for idempotency)
  helm uninstall kai-scheduler -n kai-scheduler 2>/dev/null || true
  
  helm install kai-scheduler oci://ghcr.io/nvidia/kai-scheduler/kai-scheduler \
    --version "$KAI_VERSION" \
    --namespace kai-scheduler \
    --create-namespace \
    --set global.tolerations[0].key=node-role.kubernetes.io/control-plane \
    --set global.tolerations[0].operator=Exists \
    --set global.tolerations[0].effect=NoSchedule \
    --set global.tolerations[1].key=node_role.e2e.grove.nvidia.com \
    --set global.tolerations[1].operator=Equal \
    --set global.tolerations[1].value=agent \
    --set global.tolerations[1].effect=NoSchedule
  
  log_success "Kai Scheduler installed"
fi

# ============================================================================
# Deploy Grove operator via Skaffold
# ============================================================================

if [ "$SKIP_GROVE" = false ]; then
  log_step "Deploying Grove operator"
  
  cd "$OPERATOR_DIR"
  
  # Delete existing installation if present (for idempotency)
  helm uninstall grove-operator -n grove-system 2>/dev/null || true
  
  # Set environment variables required by skaffold build
  export VERSION="E2E_TESTS"
  export LD_FLAGS="-X github.com/ai-dynamo/grove/operator/internal/version.gitCommit=e2e-test-commit -X github.com/ai-dynamo/grove/operator/internal/version.gitTreeState=clean -X github.com/ai-dynamo/grove/operator/internal/version.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ) -X github.com/ai-dynamo/grove/operator/internal/version.gitVersion=E2E_TESTS"
  
  # Build and push to localhost (accessible from host)
  # Then deploy with images rewritten to registry:PORT (accessible inside k3d cluster)
  PUSH_REPO="localhost:$REGISTRY_PORT"
  PULL_REPO="registry:$REGISTRY_PORT"
  
  log_info "Building images (push to $PUSH_REPO)..."
  BUILD_OUTPUT=$(skaffold build \
    --default-repo "$PUSH_REPO" \
    --profile "$SKAFFOLD_PROFILE" \
    --quiet \
    --output='{{json .}}')
  
  # Parse built images and rewrite repo for deployment
  GROVE_OPERATOR_TAG=$(echo "$BUILD_OUTPUT" | jq -r '.builds[] | select(.imageName=="grove-operator") | .tag' | sed "s|$PUSH_REPO|$PULL_REPO|")
  GROVE_INITC_TAG=$(echo "$BUILD_OUTPUT" | jq -r '.builds[] | select(.imageName=="grove-initc") | .tag' | sed "s|$PUSH_REPO|$PULL_REPO|")
  
  log_info "Deploying with images:"
  log_info "  grove-operator=$GROVE_OPERATOR_TAG"
  log_info "  grove-initc=$GROVE_INITC_TAG"
  
  # Set CONTAINER_REGISTRY for skaffold helm template (used for init container image)
  export CONTAINER_REGISTRY="$PULL_REPO"
  
  skaffold deploy \
    --profile "$SKAFFOLD_PROFILE" \
    --namespace grove-system \
    --status-check=false \
    --default-repo="" \
    --images "grove-operator=$GROVE_OPERATOR_TAG" \
    --images "grove-initc=$GROVE_INITC_TAG"
  
  cd "$REPO_ROOT"
  
  log_success "Grove operator deployed"
  
  # Wait for Grove pods
  log_info "Waiting for Grove pods to be ready..."
  kubectl wait --for=condition=Ready pods --all -n grove-system --timeout=5m
  
  # Wait for Grove webhook
  log_info "Waiting for Grove webhook to be ready..."
  for i in $(seq 1 60); do
    WEBHOOK_OUTPUT=$(kubectl create -f "$OPERATOR_DIR/e2e/yaml/workload1.yaml" --dry-run=server -n default 2>&1) || true
    if echo "$WEBHOOK_OUTPUT" | grep -qE "(validated|denied|error|invalid|created|podcliqueset)"; then
      log_success "Grove webhook is ready"
      break
    fi
    if [ $i -eq 60 ]; then
      log_error "Timed out waiting for Grove webhook"
      log_error "Last response: $WEBHOOK_OUTPUT"
      kubectl get validatingwebhookconfiguration,mutatingwebhookconfiguration -o wide || true
      kubectl logs -n grove-system -l app=grove-operator --tail=50 || true
      exit 1
    fi
    log_info "Webhook not ready yet, retrying in 5s... ($i/60)"
    sleep 5
  done
fi

# Wait for Kai Scheduler pods (if installed)
if [ "$SKIP_KAI" = false ]; then
  log_info "Waiting for Kai Scheduler pods to be ready..."
  kubectl wait --for=condition=Ready pods --all -n kai-scheduler --timeout=5m
  
  # Apply default queues
  log_info "Creating default Kai queues..."
  kubectl apply -f "$OPERATOR_DIR/e2e/yaml/queues.yaml"
fi

# ============================================================================
# Apply topology labels
# ============================================================================

if [ "$SKIP_TOPOLOGY" = false ]; then
  log_step "Applying topology labels to worker nodes"
  
  # Get worker nodes sorted by name (matching Go code behavior)
  WORKER_NODE_LIST=$(kubectl get nodes -l 'node_role.e2e.grove.nvidia.com=agent' -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | sort)
  
  NODE_COUNT=0
  for NODE in $WORKER_NODE_LIST; do
    ZONE=$((NODE_COUNT / NODES_PER_ZONE))
    BLOCK=$((NODE_COUNT / NODES_PER_BLOCK))
    RACK=$((NODE_COUNT / NODES_PER_RACK))
    
    kubectl label node "$NODE" \
      "kubernetes.io/zone=zone-$ZONE" \
      "kubernetes.io/block=block-$BLOCK" \
      "kubernetes.io/rack=rack-$RACK" \
      --overwrite
    
    NODE_COUNT=$((NODE_COUNT + 1))
  done
  
  log_success "Applied topology labels to $NODE_COUNT worker nodes"
fi

# ============================================================================
# Output
# ============================================================================

log_step "Cluster setup complete!"

log_info "To run E2E tests against this cluster:"
echo ""
echo "  # If using local registry for test images:"
echo "  export E2E_REGISTRY_PORT=$REGISTRY_PORT"
echo ""
echo "  # Run tests:"
echo "  make test-e2e"
echo "  make test-e2e TEST_PATTERN=Test_GS  # specific tests"
echo ""

log_success "Cluster '$CLUSTER_NAME' is ready for E2E testing!"

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

# This script mimics the exact steps that CI performs for E2E tests.
# Use it to debug CI failures locally.
#
# Usage:
#   ./hack/debug-e2e-ci.sh [--skip-cluster] [--skip-deploy] [--test-pattern PATTERN]
#
# Options:
#   --skip-cluster    Skip cluster creation (use existing cluster)
#   --skip-deploy     Skip Kai and Grove deployment (only run tests)
#   --test-pattern    Test pattern to run (default: all tests)
#   --cleanup         Delete cluster and exit
#
# Examples:
#   ./hack/debug-e2e-ci.sh                           # Full run
#   ./hack/debug-e2e-ci.sh --test-pattern "^Test_GS" # Only gang scheduling tests
#   ./hack/debug-e2e-ci.sh --skip-cluster            # Skip cluster creation
#   ./hack/debug-e2e-ci.sh --cleanup                 # Delete cluster

set -e

# Change to repository root
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
cd "$REPO_ROOT"

# Parse arguments
SKIP_CLUSTER=false
SKIP_DEPLOY=false
TEST_PATTERN=""
CLEANUP_ONLY=false

while [[ $# -gt 0 ]]; do
  case $1 in
    --skip-cluster)
      SKIP_CLUSTER=true
      shift
      ;;
    --skip-deploy)
      SKIP_DEPLOY=true
      shift
      ;;
    --test-pattern)
      TEST_PATTERN="$2"
      shift 2
      ;;
    --cleanup)
      CLEANUP_ONLY=true
      shift
      ;;
    *)
      echo "Unknown option: $1"
      exit 1
      ;;
  esac
done

# Configuration (same as CI)
CLUSTER_NAME="shared-e2e-test-cluster"
REGISTRY_PORT="5001"
API_PORT="6560"
MAX_RETRIES=3

# Cleanup function
cleanup() {
  echo "🧹 Cleaning up cluster '$CLUSTER_NAME'..."
  k3d cluster delete "$CLUSTER_NAME" 2>/dev/null || true
  echo "✅ Cleanup complete"
}

if [ "$CLEANUP_ONLY" = true ]; then
  cleanup
  exit 0
fi

echo "=============================================="
echo "  Grove E2E CI Debug Script"
echo "=============================================="
echo ""
echo "Configuration:"
echo "  Cluster Name:   $CLUSTER_NAME"
echo "  Registry Port:  $REGISTRY_PORT"
echo "  API Port:       $API_PORT"
echo "  Skip Cluster:   $SKIP_CLUSTER"
echo "  Skip Deploy:    $SKIP_DEPLOY"
echo "  Test Pattern:   ${TEST_PATTERN:-<all>}"
echo ""

# Step 1: Print system specs
echo "=============================================="
echo "Step 1: System specs"
echo "=============================================="
echo "CPUs: $(nproc 2>/dev/null || sysctl -n hw.ncpu)"
echo "RAM: $(free -h 2>/dev/null | awk '/^Mem:/ {print $2}' || sysctl -n hw.memsize | awk '{print $1/1024/1024/1024 "GB"}')"
echo ""

# Step 2: Prepare charts
echo "=============================================="
echo "Step 2: Prepare charts"
echo "=============================================="
cd operator
echo "> Preparing charts (copying CRDs)..."
./hack/prepare-charts.sh
cd ..
echo ""

if [ "$SKIP_CLUSTER" = false ]; then
  # Step 3: Create k3d cluster
  echo "=============================================="
  echo "Step 3: Create k3d cluster"
  echo "=============================================="
  
  RETRY_COUNT=0
  echo "🚀 Creating k3d cluster '$CLUSTER_NAME'..."
  
  # Retry loop for cluster creation (k3d rolls back entire cluster on any node failure)
  until [ $RETRY_COUNT -ge $MAX_RETRIES ]; do
    RETRY_COUNT=$((RETRY_COUNT + 1))
    echo "📦 Cluster creation attempt $RETRY_COUNT of $MAX_RETRIES..."
    
    # Clean up any partial cluster from previous attempt
    k3d cluster delete "$CLUSTER_NAME" 2>/dev/null || true
    
    # Create k3d cluster with registry
    if k3d cluster create "$CLUSTER_NAME" \
      --servers 1 \
      --agents 30 \
      --image "rancher/k3s:v1.33.5-k3s1" \
      --api-port "$API_PORT" \
      --port "8090:80@loadbalancer" \
      --registry-create "registry:0.0.0.0:$REGISTRY_PORT" \
      --k3s-arg "--node-taint=node_role.e2e.grove.nvidia.com=agent:NoSchedule@agent:*" \
      --k3s-node-label "node_role.e2e.grove.nvidia.com=agent@agent:*" \
      --k3s-node-label "nvidia.com/gpu.deploy.operands=false@server:*" \
      --k3s-node-label "nvidia.com/gpu.deploy.operands=false@agent:*" \
      --agents-memory "150m" \
      --timeout "120s" \
      --wait; then
      echo "✅ Cluster created successfully on attempt $RETRY_COUNT"
      break
    fi
    
    if [ $RETRY_COUNT -lt $MAX_RETRIES ]; then
      echo "⚠️ Cluster creation failed, retrying in 10 seconds..."
      sleep 10
    else
      echo "❌ Cluster creation failed after $MAX_RETRIES attempts"
      exit 1
    fi
  done
  
  # Wait for nodes to be ready
  echo "⏳ Waiting for all nodes to be ready..."
  kubectl wait --for=condition=Ready nodes --all --timeout=5m
  echo ""
fi

if [ "$SKIP_DEPLOY" = false ]; then
  # Step 4: Install Kai Scheduler
  echo "=============================================="
  echo "Step 4: Install Kai Scheduler"
  echo "=============================================="
  echo "🚀 Installing Kai Scheduler..."
  
  # Delete existing installation if present
  helm uninstall kai-scheduler -n kai-scheduler 2>/dev/null || true
  
  helm install kai-scheduler oci://ghcr.io/nvidia/kai-scheduler/kai-scheduler \
    --version v0.13.0-rc1 \
    --namespace kai-scheduler \
    --create-namespace \
    --set global.tolerations[0].key=node-role.kubernetes.io/control-plane \
    --set global.tolerations[0].operator=Exists \
    --set global.tolerations[0].effect=NoSchedule \
    --set global.tolerations[1].key=node_role.e2e.grove.nvidia.com \
    --set global.tolerations[1].operator=Equal \
    --set global.tolerations[1].value=agent \
    --set global.tolerations[1].effect=NoSchedule
  echo ""
  
  # Step 5: Deploy Grove via Skaffold
  echo "=============================================="
  echo "Step 5: Deploy Grove via Skaffold"
  echo "=============================================="
  echo "🚀 Deploying Grove operator via Skaffold..."
  cd operator
  
  # Delete existing installation if present
  helm uninstall grove-operator -n grove-system 2>/dev/null || true
  
  # Set environment variables required by skaffold build
  export VERSION="E2E_TESTS"
  export LD_FLAGS="-X github.com/ai-dynamo/grove/operator/internal/version.gitCommit=e2e-test-commit -X github.com/ai-dynamo/grove/operator/internal/version.gitTreeState=clean -X github.com/ai-dynamo/grove/operator/internal/version.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ) -X github.com/ai-dynamo/grove/operator/internal/version.gitVersion=E2E_TESTS"
  
  echo "  VERSION=$VERSION"
  echo "  LD_FLAGS=$LD_FLAGS"
  echo ""
  
  # Build and push to localhost (accessible from host)
  # Then deploy with images rewritten to registry:PORT (accessible inside k3d cluster)
  PUSH_REPO="localhost:$REGISTRY_PORT"
  PULL_REPO="registry:$REGISTRY_PORT"
  
  echo "  Building images (push to $PUSH_REPO)..."
  BUILD_OUTPUT=$(skaffold build \
    --default-repo "$PUSH_REPO" \
    --profile topology-test \
    --quiet \
    --output='{{json .}}')
  
  echo "  Build output: $BUILD_OUTPUT"
  
  # Parse built images and rewrite repo for deployment
  GROVE_OPERATOR_TAG=$(echo "$BUILD_OUTPUT" | jq -r '.builds[] | select(.imageName=="grove-operator") | .tag' | sed "s|$PUSH_REPO|$PULL_REPO|")
  GROVE_INITC_TAG=$(echo "$BUILD_OUTPUT" | jq -r '.builds[] | select(.imageName=="grove-initc") | .tag' | sed "s|$PUSH_REPO|$PULL_REPO|")
  
  echo "  Deploying with images:"
  echo "    grove-operator=$GROVE_OPERATOR_TAG"
  echo "    grove-initc=$GROVE_INITC_TAG"
  
  # Set CONTAINER_REGISTRY for skaffold helm template (used for init container image)
  export CONTAINER_REGISTRY="$PULL_REPO"
  
  skaffold deploy \
    --profile topology-test \
    --namespace grove-system \
    --status-check=false \
    --default-repo="" \
    --images "grove-operator=$GROVE_OPERATOR_TAG" \
    --images "grove-initc=$GROVE_INITC_TAG"
  cd ..
  echo ""
  
  # Step 6: Wait for pods to be ready
  echo "=============================================="
  echo "Step 6: Wait for pods to be ready"
  echo "=============================================="
  
  echo "⏳ Waiting for Grove pods to be ready..."
  kubectl wait --for=condition=Ready pods --all -n grove-system --timeout=5m
  
  # Wait for Grove webhook to be ready (polls until webhook responds)
  echo "⏳ Waiting for Grove webhook to be ready..."
  for i in $(seq 1 60); do
    WEBHOOK_OUTPUT=$(kubectl create -f operator/e2e/yaml/workload1.yaml --dry-run=server -n default 2>&1) || true
    # Check if the webhook responded (success or any processing response)
    if echo "$WEBHOOK_OUTPUT" | grep -qE "(validated|denied|error|invalid|created|podcliqueset)"; then
      echo "✅ Grove webhook is ready"
      echo "  Response: $WEBHOOK_OUTPUT"
      break
    fi
    if [ $i -eq 60 ]; then
      echo "❌ Timed out waiting for Grove webhook"
      echo "  Last response: $WEBHOOK_OUTPUT"
      # Collect diagnostics on timeout
      echo "  --- Webhook configurations ---"
      kubectl get validatingwebhookconfiguration,mutatingwebhookconfiguration -o wide || true
      echo "  --- Grove operator logs (last 50 lines) ---"
      kubectl logs -n grove-system -l app=grove-operator --tail=50 || true
      echo "  --- Grove webhook secret ---"
      kubectl get secret grove-webhook-server-cert -n grove-system -o yaml || true
      exit 1
    fi
    echo "  Webhook not ready yet, retrying in 5s... ($i/60)"
    echo "  Response: $WEBHOOK_OUTPUT"
    sleep 5
  done
  
  echo "⏳ Waiting for Kai Scheduler pods to be ready..."
  kubectl wait --for=condition=Ready pods --all -n kai-scheduler --timeout=5m
  echo ""
  
  # Step 7: Apply queues and topology labels
  echo "=============================================="
  echo "Step 7: Apply queues and topology labels"
  echo "=============================================="
  
  echo "📋 Creating default Kai queues..."
  kubectl apply -f operator/e2e/yaml/queues.yaml
  
  # Apply topology labels to worker nodes (hierarchical structure)
  # Must match the labels expected by e2e tests (see operator/e2e/setup/topology.go)
  echo "🏷️ Applying topology labels to worker nodes..."
  
  # Get worker nodes sorted by name (matching Go code behavior)
  WORKER_NODES=$(kubectl get nodes -l 'node_role.e2e.grove.nvidia.com=agent' -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | sort)
  
  # Topology distribution (from topology.go):
  # - Zone: 28 nodes per zone (all in zone-0 for 30 nodes)
  # - Block: 14 nodes per block
  # - Rack: 7 nodes per rack
  NODES_PER_ZONE=28
  NODES_PER_BLOCK=14
  NODES_PER_RACK=7
  
  NODE_COUNT=0
  for NODE in $WORKER_NODES; do
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
  echo "✅ Applied topology labels to $NODE_COUNT worker nodes"
  echo ""
fi

# Step 8: Run E2E tests
echo "=============================================="
echo "Step 8: Run E2E tests"
echo "=============================================="

export E2E_USE_EXISTING_CLUSTER=true
export E2E_CLUSTER_NAME="$CLUSTER_NAME"
export E2E_REGISTRY_PORT="$REGISTRY_PORT"

echo "Environment variables:"
echo "  E2E_USE_EXISTING_CLUSTER=$E2E_USE_EXISTING_CLUSTER"
echo "  E2E_CLUSTER_NAME=$E2E_CLUSTER_NAME"
echo "  E2E_REGISTRY_PORT=$E2E_REGISTRY_PORT"
echo ""

cd operator/e2e
echo "> Using existing cluster: $E2E_CLUSTER_NAME"

if [ -n "$TEST_PATTERN" ]; then
  echo "> Running e2e tests matching pattern: $TEST_PATTERN"
  go test -tags=e2e ./tests/... -v -timeout 45m -run "$TEST_PATTERN"
else
  echo "> Running all e2e tests..."
  go test -tags=e2e ./tests/... -v -timeout 45m
fi

echo ""
echo "=============================================="
echo "✅ E2E tests complete!"
echo "=============================================="
echo ""
echo "To cleanup the cluster, run:"
echo "  ./operator/hack/debug-e2e-ci.sh --cleanup"

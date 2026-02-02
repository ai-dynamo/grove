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
# debug-e2e-ci.sh - Local E2E test runner that mimics CI
#
# This script creates a k3d cluster (using create-e2e-cluster.sh) and runs E2E tests,
# matching the exact CI workflow for debugging failures locally.
#
# USAGE:
#   ./hack/debug-e2e-ci.sh [OPTIONS]
#
# OPTIONS:
#   --skip-cluster    Skip cluster creation (use existing cluster)
#   --skip-deploy     Skip Kai and Grove deployment (assumes already deployed)
#   --test-pattern    Test pattern to run (default: all tests)
#   --cleanup         Delete cluster and exit
#   --help            Show this help message
#
# EXAMPLES:
#   ./hack/debug-e2e-ci.sh                           # Full run
#   ./hack/debug-e2e-ci.sh --test-pattern "^Test_GS" # Only gang scheduling tests
#   ./hack/debug-e2e-ci.sh --skip-cluster            # Skip cluster creation
#   ./hack/debug-e2e-ci.sh --cleanup                 # Delete cluster
#

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
SHOW_HELP=false

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
    --help|-h)
      SHOW_HELP=true
      shift
      ;;
    *)
      echo "Unknown option: $1"
      echo "Run with --help for usage"
      exit 1
      ;;
  esac
done

if [ "$SHOW_HELP" = true ]; then
  sed -n '17,42p' "$0"
  exit 0
fi

# Configuration (same as CI) - uses defaults from create-e2e-cluster.sh
export E2E_CLUSTER_NAME="${E2E_CLUSTER_NAME:-shared-e2e-test-cluster}"
export E2E_REGISTRY_PORT="${E2E_REGISTRY_PORT:-5001}"

if [ "$CLEANUP_ONLY" = true ]; then
  "$SCRIPT_DIR/create-e2e-cluster.sh" --delete
  exit 0
fi

echo "=============================================="
echo "  Grove E2E CI Debug Script"
echo "=============================================="
echo ""
echo "Configuration:"
echo "  Cluster Name:   $E2E_CLUSTER_NAME"
echo "  Registry Port:  $E2E_REGISTRY_PORT"
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

# Step 2: Create cluster and deploy components
if [ "$SKIP_CLUSTER" = false ]; then
  echo "=============================================="
  echo "Step 2: Create cluster and deploy components"
  echo "=============================================="
  
  # Build script options based on flags
  CREATE_OPTS=""
  if [ "$SKIP_DEPLOY" = true ]; then
    CREATE_OPTS="--skip-kai --skip-grove --skip-topology"
  fi
  
  "$SCRIPT_DIR/create-e2e-cluster.sh" $CREATE_OPTS
  echo ""
elif [ "$SKIP_DEPLOY" = false ]; then
  echo "=============================================="
  echo "Step 2: Deploy components to existing cluster"
  echo "=============================================="
  
  # Just deploy to existing cluster (cluster already exists)
  "$SCRIPT_DIR/create-e2e-cluster.sh" --skip-kai --skip-grove --skip-topology || true
  
  # Now deploy only the components
  echo "🚀 Installing Kai Scheduler..."
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
  
  echo "🚀 Deploying Grove operator..."
  cd operator
  helm uninstall grove-operator -n grove-system 2>/dev/null || true
  
  export VERSION="E2E_TESTS"
  export LD_FLAGS="-X github.com/ai-dynamo/grove/operator/internal/version.gitCommit=e2e-test-commit -X github.com/ai-dynamo/grove/operator/internal/version.gitTreeState=clean -X github.com/ai-dynamo/grove/operator/internal/version.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ) -X github.com/ai-dynamo/grove/operator/internal/version.gitVersion=E2E_TESTS"
  
  PUSH_REPO="localhost:$E2E_REGISTRY_PORT"
  PULL_REPO="registry:$E2E_REGISTRY_PORT"
  
  BUILD_OUTPUT=$(skaffold build --default-repo "$PUSH_REPO" --profile topology-test --quiet --output='{{json .}}')
  GROVE_OPERATOR_TAG=$(echo "$BUILD_OUTPUT" | jq -r '.builds[] | select(.imageName=="grove-operator") | .tag' | sed "s|$PUSH_REPO|$PULL_REPO|")
  GROVE_INITC_TAG=$(echo "$BUILD_OUTPUT" | jq -r '.builds[] | select(.imageName=="grove-initc") | .tag' | sed "s|$PUSH_REPO|$PULL_REPO|")
  
  export CONTAINER_REGISTRY="$PULL_REPO"
  skaffold deploy --profile topology-test --namespace grove-system --status-check=false --default-repo="" \
    --images "grove-operator=$GROVE_OPERATOR_TAG" --images "grove-initc=$GROVE_INITC_TAG"
  cd ..
  
  echo "⏳ Waiting for pods..."
  kubectl wait --for=condition=Ready pods --all -n grove-system --timeout=5m
  kubectl wait --for=condition=Ready pods --all -n kai-scheduler --timeout=5m
  kubectl apply -f operator/e2e/yaml/queues.yaml
  echo ""
fi

# Step 3: Run E2E tests
echo "=============================================="
echo "Step 3: Run E2E tests"
echo "=============================================="

export E2E_USE_EXISTING_CLUSTER=true

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

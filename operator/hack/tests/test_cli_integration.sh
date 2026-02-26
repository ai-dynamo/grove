#!/usr/bin/env bash
# /*
# Copyright 2026 The Grove Authors.
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

# CLI Integration Test
#
# Exercises every CLI command against real infrastructure (k3d, helm, kubectl, skaffold).
# No mocking — tests call real tools and verify real cluster state.
#
# Prerequisites: k3d, kubectl, docker, helm, skaffold, jq installed; Docker daemon running.
# Runtime: ~20-25 minutes (grove builds twice, KWOK + pyroscope add time).
# Note: Skaffold requires a clean git state. The script will stash uncommitted changes
#       before running and restore them on exit.
set -euo pipefail

CLUSTER_NAME="cli-integ-test"
DEFAULT_CLUSTER="shared-e2e-test-cluster"
API_PORT=6570
WORKERS=2
KWOK_NODES=3
KWOK_BATCH_SIZE=3
PYROSCOPE_TEST_NS="pyroscope-test"

SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
CLI="${SCRIPT_DIR}/cli.py"
PASS=0
FAIL=0
TOTAL=0
GIT_STASHED=false

# ============================================================================
# Helpers
# ============================================================================

info() {
    echo ""
    echo "═══ $1 ═══"
}

check() {
    local desc="$1"
    shift
    ((TOTAL++)) || true
    if eval "$@" > /dev/null 2>&1; then
        ((PASS++)) || true
        echo "  ✅ ${desc}"
    else
        ((FAIL++)) || true
        echo "  ❌ ${desc}"
    fi
}

must() {
    local desc="$1"
    shift
    ((TOTAL++)) || true
    if eval "$@" > /dev/null 2>&1; then
        ((PASS++)) || true
        echo "  ✅ ${desc}"
    else
        ((FAIL++)) || true
        echo "  ❌ ${desc} — FATAL"
        summary
        exit 1
    fi
}

summary() {
    echo ""
    echo "═══ Results: ${PASS}/${TOTAL} passed, ${FAIL} failed ═══"
}

# ============================================================================
# Git stash — skaffold requires clean git state
# ============================================================================

git_make_clean() {
    if [ -n "$(git -C "${REPO_ROOT}" status --porcelain 2>/dev/null)" ]; then
        info "Creating temporary commit (skaffold requires clean git)"
        git -C "${REPO_ROOT}" add -A
        git -C "${REPO_ROOT}" commit --no-verify -m "cli-integ-test: temporary commit" > /dev/null
        GIT_STASHED=true
    fi
}

git_undo_clean() {
    if [ "${GIT_STASHED}" = true ]; then
        echo "Undoing temporary commit..."
        git -C "${REPO_ROOT}" reset --soft HEAD~1 || true
        GIT_STASHED=false
    fi
}

# ============================================================================
# Cleanup trap — delete BOTH possible clusters on exit, restore git stash
# ============================================================================

cleanup() {
    echo ""
    echo "Cleaning up..."
    $CLI delete kwok-nodes 2>/dev/null || true
    $CLI delete k3d-cluster --cluster-name "${CLUSTER_NAME}" 2>/dev/null || true
    $CLI delete k3d-cluster --cluster-name "${DEFAULT_CLUSTER}" 2>/dev/null || true
    git_undo_clean
}
trap cleanup EXIT

git_make_clean

# ============================================================================
# Phase 1: Create k3d cluster
# ============================================================================

info "Phase 1: Create k3d cluster"
$CLI create k3d-cluster \
    --workers "${WORKERS}" \
    --cluster-name "${CLUSTER_NAME}"

must "k3d cluster exists" \
    "k3d cluster list -o json | jq -e '.[] | select(.name==\"${CLUSTER_NAME}\")'"
must "correct node count (server + ${WORKERS} agents = $((WORKERS + 1)))" \
    "[ \$(kubectl get nodes --no-headers | wc -l | tr -d ' ') -eq $((WORKERS + 1)) ]"
must "all nodes Ready" \
    "kubectl wait --for=condition=Ready nodes --all --timeout=120s"

# ============================================================================
# Phase 2: Setup — apply-topology + prepull-images
# ============================================================================

info "Phase 2: Setup — apply-topology + prepull-images"
$CLI setup apply-topology

AGENT_NODES=$(kubectl get nodes --no-headers -o custom-columns=NAME:.metadata.name | grep -v "server")
for node in ${AGENT_NODES}; do
    check "${node} has zone label" \
        "[ -n \"\$(kubectl get node ${node} -o jsonpath='{.metadata.labels.kubernetes\\.io/zone}')\" ]"
    check "${node} has rack label" \
        "[ -n \"\$(kubectl get node ${node} -o jsonpath='{.metadata.labels.kubernetes\\.io/rack}')\" ]"
    check "${node} has block label" \
        "[ -n \"\$(kubectl get node ${node} -o jsonpath='{.metadata.labels.kubernetes\\.io/block}')\" ]"
done

$CLI setup prepull-images
check "prepull-images exit code 0" "true"

# ============================================================================
# Phase 3: Install individual components
# ============================================================================

info "Phase 3: Install Kai Scheduler"
$CLI install kai

check "kai helm release exists" \
    "helm list -n kai-scheduler -q | grep -q kai-scheduler"
check "kai pods Ready" \
    "kubectl wait --for=condition=Ready pods --all -n kai-scheduler --timeout=300s"

info "Phase 3: Install Grove operator"
$CLI install grove

check "grove helm release exists" \
    "helm list -n grove-system -q | grep -q grove-operator"
check "grove deployment Available" \
    "kubectl wait --for=condition=Available deploy -l app.kubernetes.io/name=grove-operator -n grove-system --timeout=300s"
check "grove CRD podcliquesets.grove.io exists" \
    "kubectl get crd podcliquesets.grove.io"

# ============================================================================
# Phase 4: Uninstall components
# ============================================================================

info "Phase 4: Uninstall Grove"
$CLI uninstall grove

check "grove helm release removed" \
    "[ -z \"\$(helm list -n grove-system -q)\" ]"

info "Phase 4: Uninstall Kai"
$CLI uninstall kai

check "kai helm release removed" \
    "[ -z \"\$(helm list -n kai-scheduler -q)\" ]"

# ============================================================================
# Phase 5: Reinstall with options
# ============================================================================

info "Phase 5: Reinstall Kai + Grove with options (--profiling, --pcs-syncs 5)"
$CLI install kai
$CLI install grove --profiling --pcs-syncs 5

check "grove helm release exists after reinstall" \
    "helm list -n grove-system -q | grep -q grove-operator"
check "grove deployment Available after reinstall" \
    "kubectl wait --for=condition=Available deploy -l app.kubernetes.io/name=grove-operator -n grove-system --timeout=300s"
check "grove profiling enabled in helm values" \
    "helm get values grove-operator -n grove-system -o json | jq -e '.config.debugging.enableProfiling == true'"

# ============================================================================
# Phase 6: KWOK nodes — create and delete
# ============================================================================

info "Phase 6: Create KWOK nodes"
$CLI create kwok-nodes --nodes "${KWOK_NODES}" --batch-size "${KWOK_BATCH_SIZE}"

check "${KWOK_NODES} KWOK nodes created" \
    "[ \$(kubectl get nodes -l type=kwok --no-headers | wc -l | tr -d ' ') -eq ${KWOK_NODES} ]"

KWOK_NODE=$(kubectl get nodes -l type=kwok --no-headers -o custom-columns=NAME:.metadata.name | head -1)
check "KWOK node has zone label" \
    "[ -n \"\$(kubectl get node ${KWOK_NODE} -o jsonpath='{.metadata.labels.kubernetes\\.io/zone}')\" ]"
check "KWOK node has rack label" \
    "[ -n \"\$(kubectl get node ${KWOK_NODE} -o jsonpath='{.metadata.labels.kubernetes\\.io/rack}')\" ]"
check "KWOK node has block label" \
    "[ -n \"\$(kubectl get node ${KWOK_NODE} -o jsonpath='{.metadata.labels.kubernetes\\.io/block}')\" ]"
check "KWOK node has fake-node taint" \
    "kubectl get node ${KWOK_NODE} -o json | jq -e '.spec.taints[]? | select(.key==\"fake-node\" and .effect==\"NoSchedule\")'"

info "Phase 6: Delete KWOK nodes"
$CLI delete kwok-nodes

check "KWOK nodes deleted" \
    "[ \$(kubectl get nodes -l type=kwok --no-headers 2>/dev/null | wc -l | tr -d ' ') -eq 0 ]"

# ============================================================================
# Phase 7: Composite — setup e2e --skip-cluster-creation --skip-prepull
# ============================================================================

info "Phase 7: Composite — setup e2e (skip cluster + prepull)"

$CLI uninstall grove
$CLI uninstall kai

$CLI setup e2e --skip-cluster-creation --skip-prepull

check "kai installed via setup e2e" \
    "helm list -n kai-scheduler -q | grep -q kai-scheduler"
check "grove installed via setup e2e" \
    "helm list -n grove-system -q | grep -q grove-operator"

for node in ${AGENT_NODES}; do
    check "${node} still has topology labels" \
        "[ -n \"\$(kubectl get node ${node} -o jsonpath='{.metadata.labels.kubernetes\\.io/zone}')\" ]"
done

check "no new cluster created (still ${CLUSTER_NAME})" \
    "k3d cluster list -o json | jq -e '.[] | select(.name==\"${CLUSTER_NAME}\")'"

# ============================================================================
# Phase 8: Composite — setup scale (full end-to-end)
# ============================================================================

info "Phase 8: Composite — setup scale"

$CLI uninstall grove
$CLI uninstall kai

# Delete test cluster; setup scale creates its own default cluster
$CLI delete k3d-cluster --cluster-name "${CLUSTER_NAME}"

$CLI setup scale \
    --kwok-nodes "${KWOK_NODES}" \
    --kwok-batch-size "${KWOK_BATCH_SIZE}" \
    --pcs-syncs 5 \
    --pclq-syncs 5 \
    --pcsg-syncs 5 \
    --pyroscope-namespace "${PYROSCOPE_TEST_NS}"

# Re-register cleanup for the default cluster
trap 'echo ""; echo "Cleaning up..."; $CLI delete kwok-nodes 2>/dev/null || true; $CLI delete k3d-cluster --cluster-name "${DEFAULT_CLUSTER}" 2>/dev/null || true; git_undo_clean' EXIT

must "default cluster exists" \
    "k3d cluster list -o json | jq -e '.[] | select(.name==\"${DEFAULT_CLUSTER}\")'"
check "kai installed via setup scale" \
    "helm list -n kai-scheduler -q | grep -q kai-scheduler"
check "grove installed via setup scale" \
    "helm list -n grove-system -q | grep -q grove-operator"
check "grove profiling enabled via setup scale" \
    "helm get values grove-operator -n grove-system -o json | jq -e '.config.debugging.enableProfiling == true'"
check "${KWOK_NODES} KWOK nodes via setup scale" \
    "[ \$(kubectl get nodes -l type=kwok --no-headers | wc -l | tr -d ' ') -eq ${KWOK_NODES} ]"
check "pyroscope installed in ${PYROSCOPE_TEST_NS}" \
    "helm list -n ${PYROSCOPE_TEST_NS} -q | grep -q pyroscope"

SCALE_AGENTS=$(kubectl get nodes --no-headers -o custom-columns=NAME:.metadata.name | grep -v "server")
for node in ${SCALE_AGENTS}; do
    if kubectl get node "${node}" -o jsonpath='{.metadata.labels.type}' 2>/dev/null | grep -q kwok; then
        continue
    fi
    check "${node} has topology labels (setup scale)" \
        "[ -n \"\$(kubectl get node ${node} -o jsonpath='{.metadata.labels.kubernetes\\.io/zone}')\" ]"
done

# ============================================================================
# Phase 9: Cleanup setup scale cluster
# ============================================================================

info "Phase 9: Cleanup setup scale cluster"
$CLI delete kwok-nodes

check "KWOK nodes cleaned up" \
    "[ \$(kubectl get nodes -l type=kwok --no-headers 2>/dev/null | wc -l | tr -d ' ') -eq 0 ]"

$CLI delete k3d-cluster --cluster-name "${DEFAULT_CLUSTER}"

check "default cluster deleted" \
    "! k3d cluster list -o json | jq -e '.[] | select(.name==\"${DEFAULT_CLUSTER}\")'"

# ============================================================================
# Done — disable cleanup trap and print summary
# ============================================================================

trap - EXIT
git_undo_clean
summary
[ "${FAIL}" -eq 0 ] || exit 1

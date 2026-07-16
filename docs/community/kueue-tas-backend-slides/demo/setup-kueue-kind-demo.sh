#!/usr/bin/env bash
set -euo pipefail

# Prepares the 2-node kind cluster for Grove + Kueue TAS demos:
# - labels every node for the kind-cpu ResourceFlavor
# - applies ClusterTopologyBinding (host -> kubernetes.io/hostname)
# - applies Kueue queues/flavors
#
# Prereq: kind cluster with WORKERS=1 (control-plane + worker), Grove operator,
# and Kueue installed. Grove syncs ClusterTopologyBinding -> Kueue Topology.

KUBECTL="${KUBECTL:-kubectl}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

require_two_nodes() {
  local count
  count="$("${KUBECTL}" get nodes --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  if [[ "${count}" -lt 2 ]]; then
    echo "Expected at least 2 kind nodes for hostname TAS demos; found ${count}." >&2
    echo "Recreate the cluster with one worker:" >&2
    echo "  cd operator && make kind-down && make kind-up WORKERS=1" >&2
    exit 1
  fi
}

label_tas_nodes() {
  local node
  for node in $("${KUBECTL}" get nodes -o jsonpath='{.items[*].metadata.name}'); do
    "${KUBECTL}" label node "${node}" topology.ai-dynamo.io/tas-node=true --overwrite >/dev/null
  done
}

require_two_nodes
label_tas_nodes

"${KUBECTL}" apply -f "${SCRIPT_DIR}/grove-kind-topology.yaml"
"${KUBECTL}" apply -f "${SCRIPT_DIR}/kueue-kind-queue.yaml"

echo "Labeled nodes:"
"${KUBECTL}" get nodes -L topology.ai-dynamo.io/tas-node
echo
echo "Grove topology binding:"
"${KUBECTL}" get clustertopologybinding grove-kind-topology
echo
echo "Wait for Grove to sync Kueue Topology, then verify:"
echo "  kubectl get topology grove-kind-topology"
echo
echo "Run demos:"
echo "  ${SCRIPT_DIR}/validate-grove-kueue-mincount.sh"
echo "  ${SCRIPT_DIR}/validate-grove-kueue-pcsg.sh"

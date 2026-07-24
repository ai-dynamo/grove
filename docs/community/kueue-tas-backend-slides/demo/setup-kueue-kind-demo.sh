#!/usr/bin/env bash
set -euo pipefail

# Prepares the 2-node kind cluster for Grove + Kueue TAS demos:
# - labels both nodes as members of the same rack
# - applies ClusterTopologyBinding (rack only; hostname spread via anti-affinity)
# - applies Kueue queues/flavors
#
# Prereq: kind cluster with WORKERS=1 (control-plane + worker), Grove operator,
# and Kueue installed. Grove syncs ClusterTopologyBinding -> Kueue Topology.

KUBECTL="${KUBECTL:-kubectl}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KUEUE_NAMESPACE="${KUEUE_NAMESPACE:-kueue-system}"
KUEUE_DEPLOYMENT="${KUEUE_DEPLOYMENT:-kueue-controller-manager}"

require_exactly_two_nodes() {
  local count
  count="$("${KUBECTL}" get nodes --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  if [[ "${count}" -ne 2 ]]; then
    echo "Expected exactly 2 kind nodes for the rack-spread demo; found ${count}." >&2
    echo "Recreate the cluster with one worker:" >&2
    echo "  cd operator && make kind-down && make kind-up WORKERS=1" >&2
    exit 1
  fi
}

label_tas_nodes() {
  local node
  for node in $("${KUBECTL}" get nodes -o jsonpath='{.items[*].metadata.name}'); do
    "${KUBECTL}" label node "${node}" topology.ai-dynamo.io/tas-node=true --overwrite >/dev/null
    "${KUBECTL}" label node "${node}" topology.ai-dynamo.io/rack=rack-0 --overwrite >/dev/null
  done
}

enable_partial_admission() {
  local args
  args="$("${KUBECTL}" -n "${KUEUE_NAMESPACE}" get deployment "${KUEUE_DEPLOYMENT}" \
    -o jsonpath='{.spec.template.spec.containers[0].args[*]}')"
  if [[ "${args}" == *"AllAlpha=true"* || "${args}" == *"PartialAdmission=true"* ]]; then
    return
  fi
  if [[ "${args}" == *"--feature-gates="* ]]; then
    echo "Kueue has a feature-gates argument that does not enable PartialAdmission:" >&2
    echo "  ${args}" >&2
    echo "Enable PartialAdmission in that argument and rerun this script." >&2
    exit 1
  fi
  "${KUBECTL}" -n "${KUEUE_NAMESPACE}" patch deployment "${KUEUE_DEPLOYMENT}" --type=json \
    -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--feature-gates=PartialAdmission=true"}]' >/dev/null
  "${KUBECTL}" -n "${KUEUE_NAMESPACE}" rollout status deployment "${KUEUE_DEPLOYMENT}"
}

require_exactly_two_nodes
label_tas_nodes
enable_partial_admission

"${KUBECTL}" apply -f "${SCRIPT_DIR}/grove-kind-topology.yaml"
"${KUBECTL}" apply -f "${SCRIPT_DIR}/kueue-kind-queue.yaml"

echo "Labeled nodes:"
"${KUBECTL}" get nodes -L topology.ai-dynamo.io/tas-node -L topology.ai-dynamo.io/rack
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

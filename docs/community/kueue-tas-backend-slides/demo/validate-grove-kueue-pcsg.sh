#!/usr/bin/env bash
set -euo pipefail

# Validates the Grove Kueue "PCS with PCSG" path. Grove pre-builds one Kueue
# Workload per PodGang for every PodCliqueSet. PodCliqueScalingGroup cliques are
# all-or-nothing: their podSets use minCount == count (POC assumption
# PCSG.minAvailable == PCSG.replicas). Pods reference the prebuilt Workload.

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE="${NAMESPACE:-default}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-120}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MANIFEST="${MANIFEST:-${SCRIPT_DIR}/grove-kueue-pcsg.yaml}"
PCS_NAME="grove-kueue-pcsg"
PODGANG_NAME="${PCS_NAME}-0"
# decode-leader(1) + decode-worker(2) per PCSG replica, times 2 replicas = 6.
EXPECTED_TOTAL="6"

cleanup() {
  "${KUBECTL}" delete -f "${MANIFEST}" --ignore-not-found >/dev/null 2>&1 || true
  "${KUBECTL}" -n "${NAMESPACE}" delete workload "${PODGANG_NAME}" --ignore-not-found >/dev/null 2>&1 || true
}

wait_for_jsonpath() {
  local name="$1"
  local jsonpath="$2"
  local expected="$3"
  local timeout_seconds="${4:-120}"
  local deadline=$((SECONDS + timeout_seconds))

  while (( SECONDS < deadline )); do
    local actual
    actual="$("${KUBECTL}" -n "${NAMESPACE}" get "${name}" -o "jsonpath=${jsonpath}" 2>/dev/null || true)"
    if [[ "${actual}" == "${expected}" ]]; then
      return 0
    fi
    sleep 2
  done

  echo "Timed out waiting for ${name} jsonpath ${jsonpath} to equal ${expected}" >&2
  return 1
}

cleanup

"${KUBECTL}" apply -f "${MANIFEST}" >/dev/null

# Grove builds one prebuilt Workload named after the PodGang, even for PCSG.
wait_for_jsonpath \
  "workload/${PODGANG_NAME}" \
  "{.metadata.name}" \
  "${PODGANG_NAME}" \
  "${TIMEOUT_SECONDS}"

# Every PCSG podSet is all-or-nothing: minCount is omitted (Kueue defaults it to count, and Kueue rejects
# Workloads where more than one podSet sets minCount).
min_counts="$("${KUBECTL}" -n "${NAMESPACE}" get "workload/${PODGANG_NAME}" \
  -o "jsonpath={range .spec.podSets[*]}{.minCount}{' '}{end}" | tr -d ' ')"
if [[ -n "${min_counts}" ]]; then
  echo "Expected no PCSG podSet to set minCount (all-or-nothing), got minCounts='${min_counts}'" >&2
  exit 1
fi

# Pods reference the prebuilt Workload.
pod="$("${KUBECTL}" -n "${NAMESPACE}" get pods \
  -l "app.kubernetes.io/part-of=${PCS_NAME}" \
  -o "jsonpath={.items[0].metadata.name}")"
if [[ -z "${pod}" ]]; then
  echo "Expected at least one Pod for PodCliqueSet ${PCS_NAME}" >&2
  exit 1
fi

group="$("${KUBECTL}" -n "${NAMESPACE}" get pod "${pod}" \
  -o "jsonpath={.metadata.labels.kueue\\.x-k8s\\.io/pod-group-name}")"
if [[ "${group}" != "${PODGANG_NAME}" ]]; then
  echo "Expected Pod ${pod} to use kueue.x-k8s.io/pod-group-name=${PODGANG_NAME}, got '${group}'" >&2
  exit 1
fi

prebuilt="$("${KUBECTL}" -n "${NAMESPACE}" get pod "${pod}" \
  -o "jsonpath={.metadata.labels.kueue\\.x-k8s\\.io/prebuilt-workload-name}")"
if [[ "${prebuilt}" != "${PODGANG_NAME}" ]]; then
  echo "Expected Pod ${pod} to carry kueue.x-k8s.io/prebuilt-workload-name=${PODGANG_NAME}, got '${prebuilt}'" >&2
  exit 1
fi

total="$("${KUBECTL}" -n "${NAMESPACE}" get pod "${pod}" \
  -o "jsonpath={.metadata.annotations.kueue\\.x-k8s\\.io/pod-group-total-count}")"
if [[ "${total}" != "${EXPECTED_TOTAL}" ]]; then
  echo "Expected pod-group-total-count=${EXPECTED_TOTAL}, got '${total}'" >&2
  exit 1
fi

echo "PASS: Grove created one prebuilt Workload named ${PODGANG_NAME} for the PCSG PodCliqueSet."
echo "PASS: every PCSG podSet is all-or-nothing (minCount omitted; Kueue allows minCount on at most one podSet)."
echo "PASS: Pods reference the prebuilt Workload and share pod-group-name=${PODGANG_NAME} (total ${EXPECTED_TOTAL})."
echo
echo "Inspect:"
echo "  ${KUBECTL} -n ${NAMESPACE} get workload ${PODGANG_NAME} -o yaml"
echo "  ${KUBECTL} -n ${NAMESPACE} get pods -l app.kubernetes.io/part-of=${PCS_NAME} -o wide"
echo
echo "Cleanup:"
echo "  ${KUBECTL} delete -f ${MANIFEST} --ignore-not-found"
echo "  ${KUBECTL} -n ${NAMESPACE} delete workload ${PODGANG_NAME} --ignore-not-found"

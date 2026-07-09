#!/usr/bin/env bash
set -euo pipefail

# Validates the Grove Kueue "simple PCS with minCount" path: Grove builds one
# prebuilt Kueue Workload per PodGang with a podSet whose count == clique
# replicas and minCount == clique minAvailable, and stamps the Pods with
# kueue.x-k8s.io/prebuilt-workload-name.

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE="${NAMESPACE:-default}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-120}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MANIFEST="${MANIFEST:-${SCRIPT_DIR}/grove-kueue-simple-mincount.yaml}"
PCS_NAME="grove-kueue-mincount"
PODGANG_NAME="${PCS_NAME}-0"

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

# Grove builds one prebuilt Workload named after the PodGang.
wait_for_jsonpath \
  "workload/${PODGANG_NAME}" \
  "{.metadata.name}" \
  "${PODGANG_NAME}" \
  "${TIMEOUT_SECONDS}"

wait_for_jsonpath "workload/${PODGANG_NAME}" "{.spec.podSets[0].count}" "4" "${TIMEOUT_SECONDS}"
wait_for_jsonpath "workload/${PODGANG_NAME}" "{.spec.podSets[0].minCount}" "2" "${TIMEOUT_SECONDS}"

# Pods reference the prebuilt Workload.
pod="$("${KUBECTL}" -n "${NAMESPACE}" get pods \
  -l "app.kubernetes.io/part-of=${PCS_NAME}" \
  -o "jsonpath={.items[0].metadata.name}")"
if [[ -z "${pod}" ]]; then
  echo "Expected at least one Pod for PodCliqueSet ${PCS_NAME}" >&2
  exit 1
fi

prebuilt="$("${KUBECTL}" -n "${NAMESPACE}" get pod "${pod}" \
  -o "jsonpath={.metadata.labels.kueue\\.x-k8s\\.io/prebuilt-workload-name}")"
if [[ "${prebuilt}" != "${PODGANG_NAME}" ]]; then
  echo "Expected Pod ${pod} to carry kueue.x-k8s.io/prebuilt-workload-name=${PODGANG_NAME}, got '${prebuilt}'" >&2
  exit 1
fi

echo "PASS: Grove created one prebuilt Workload named ${PODGANG_NAME}."
echo "PASS: the Workload podSet has count=4 and minCount=2."
echo "PASS: Pods reference the prebuilt Workload via kueue.x-k8s.io/prebuilt-workload-name."
echo
echo "Inspect:"
echo "  ${KUBECTL} -n ${NAMESPACE} get workload ${PODGANG_NAME} -o yaml"
echo "  ${KUBECTL} -n ${NAMESPACE} get pods -l app.kubernetes.io/part-of=${PCS_NAME} -o wide"
echo
echo "Cleanup:"
echo "  ${KUBECTL} delete -f ${MANIFEST} --ignore-not-found"
echo "  ${KUBECTL} -n ${NAMESPACE} delete workload ${PODGANG_NAME} --ignore-not-found"

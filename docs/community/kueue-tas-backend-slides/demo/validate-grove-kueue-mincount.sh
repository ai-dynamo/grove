#!/usr/bin/env bash
set -euo pipefail

# Validates Kueue partial admission via minCount.
# grove-kueue-simple-mincount.yaml has one clique: replicas=4, minAvailable=2, each
# Pod requesting 1 CPU. The demo ClusterQueue (kueue-kind-queue.yaml) grants only
# 2 CPU, so Kueue admits the prebuilt Workload at minCount=2: Grove creates all 4
# Pods, Kueue ungates 2 (Running) and leaves 2 scheduling-gated. Result: 2 of 4 run.
#
# With pack.required: host, the admitted Pods pack onto a single host, so this demo
# does NOT assert Pods land on distinct nodes.

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE="${NAMESPACE:-default}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-180}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MANIFEST="${MANIFEST:-${SCRIPT_DIR}/grove-kueue-simple-mincount.yaml}"
QUEUE_MANIFEST="${QUEUE_MANIFEST:-${SCRIPT_DIR}/kueue-kind-queue.yaml}"
PCS_NAME="grove-kueue-mincount"
PODGANG_NAME="${PCS_NAME}-0"
CLUSTER_QUEUE="kind-cpu"

cleanup() {
  "${KUBECTL}" delete -f "${MANIFEST}" --ignore-not-found >/dev/null 2>&1 || true
  "${KUBECTL}" -n "${NAMESPACE}" delete workload "${PODGANG_NAME}" --ignore-not-found >/dev/null 2>&1 || true
}

diagnose() {
  echo "---- diagnostics ----" >&2
  echo "Pods:" >&2
  "${KUBECTL}" -n "${NAMESPACE}" get pods -l "app.kubernetes.io/part-of=${PCS_NAME}" -o wide >&2 || true
  echo "Workload podSets (count / minCount / request):" >&2
  "${KUBECTL}" -n "${NAMESPACE}" get "workload/${PODGANG_NAME}" \
    -o jsonpath='{range .spec.podSets[*]}{.name}{" count="}{.count}{" minCount="}{.minCount}{" cpu="}{.template.spec.containers[0].resources.requests.cpu}{"\n"}{end}' >&2 || true
  echo >&2
  echo "Workload admission (admitted count):" >&2
  "${KUBECTL}" -n "${NAMESPACE}" get "workload/${PODGANG_NAME}" \
    -o jsonpath='{range .status.admission.podSetAssignments[*]}{.name}{" count="}{.count}{"\n"}{end}' >&2 || true
  echo >&2
  echo "ClusterQueue ${CLUSTER_QUEUE} nominalQuota vs usage:" >&2
  "${KUBECTL}" get clusterqueue "${CLUSTER_QUEUE}" \
    -o jsonpath='nominal={range .spec.resourceGroups[*].flavors[*].resources[*]}{.name}={.nominalQuota}{" "}{end}{"\n"}' >&2 || true
  "${KUBECTL}" get clusterqueue "${CLUSTER_QUEUE}" \
    -o jsonpath='usage={range .status.flavorsUsage[*].resources[*]}{.name}={.total}{" "}{end}{"\n"}' >&2 || true
  echo >&2
  echo "If all 4 Pods are Running, the ClusterQueue quota is not capping admission." >&2
  echo "Re-apply the demo quota (2 CPU) and retry:" >&2
  echo "  ${KUBECTL} apply -f ${QUEUE_MANIFEST}" >&2
  echo "---------------------" >&2
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

running_pods() {
  "${KUBECTL}" -n "${NAMESPACE}" get pods \
    -l "app.kubernetes.io/part-of=${PCS_NAME}" \
    --field-selector=status.phase=Running \
    -o name 2>/dev/null | wc -l | tr -d ' '
}

total_pods() {
  "${KUBECTL}" -n "${NAMESPACE}" get pods \
    -l "app.kubernetes.io/part-of=${PCS_NAME}" \
    -o name 2>/dev/null | wc -l | tr -d ' '
}

cleanup

"${KUBECTL}" apply -f "${MANIFEST}" >/dev/null

wait_for_jsonpath \
  "workload/${PODGANG_NAME}" \
  "{.metadata.name}" \
  "${PODGANG_NAME}" \
  "${TIMEOUT_SECONDS}"

wait_for_jsonpath "workload/${PODGANG_NAME}" "{.spec.podSets[0].count}" "4" "${TIMEOUT_SECONDS}"
wait_for_jsonpath "workload/${PODGANG_NAME}" "{.spec.podSets[0].minCount}" "2" "${TIMEOUT_SECONDS}"

wait_for_jsonpath \
  "workload/${PODGANG_NAME}" \
  '{.status.conditions[?(@.type=="Admitted")].status}' \
  "True" \
  "${TIMEOUT_SECONDS}"

# Grove creates all 4 Pods; Kueue partial-admits at minCount=2, so exactly 2 become
# Running and the other 2 remain scheduling-gated. Wait for at least 2 Running, then
# assert the count settles at exactly 2 (quota only fits 2).
running_count=0
deadline=$((SECONDS + TIMEOUT_SECONDS))
while (( SECONDS < deadline )); do
  running_count="$(running_pods)"
  if [[ "${running_count}" -ge 2 ]]; then
    break
  fi
  sleep 2
done
if [[ "${running_count}" -lt 2 ]]; then
  echo "Expected at least 2 Running Pods for ${PCS_NAME} (minCount=2), got ${running_count}" >&2
  diagnose
  exit 1
fi

# Give Kueue a moment to (not) admit more, then confirm the Running count stays at 2.
sleep 5
running_count="$(running_pods)"
if [[ "${running_count}" -ne 2 ]]; then
  echo "Expected exactly 2 Running Pods (quota fits only 2), got ${running_count}" >&2
  diagnose
  exit 1
fi

total_count="$(total_pods)"
if [[ "${total_count}" -ne 4 ]]; then
  echo "Expected 4 Pods total (2 Running, 2 gated), got ${total_count}" >&2
  diagnose
  exit 1
fi

echo "PASS: Grove created one prebuilt Workload named ${PODGANG_NAME}."
echo "PASS: the Workload podSet has count=4 and minCount=2."
echo "PASS: Kueue partial-admitted the Workload at minCount (quota fits only 2 CPU)."
echo "PASS: 2 of 4 Pods are Running; the other 2 remain scheduling-gated."
echo
echo "Inspect:"
echo "  ${KUBECTL} -n ${NAMESPACE} get workload ${PODGANG_NAME} -o yaml"
echo "  ${KUBECTL} -n ${NAMESPACE} get pods -l app.kubernetes.io/part-of=${PCS_NAME} -o wide"
echo
echo "Cleanup:"
echo "  ${KUBECTL} delete -f ${MANIFEST} --ignore-not-found"
echo "  ${KUBECTL} -n ${NAMESPACE} delete workload ${PODGANG_NAME} --ignore-not-found"

#!/usr/bin/env bash
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

# Deploys complete monitoring stack: Prometheus, Grafana, Pyroscope

set -o errexit
set -o nounset
set -o pipefail

# Default configuration
DEPLOY_PROMETHEUS=false
DEPLOY_GRAFANA=false
DEPLOY_PYROSCOPE=false
DEPLOY_SERVICEMONITOR=false
PROMETHEUS_NAMESPACE="monitoring"
GRAFANA_NAMESPACE="monitoring"
PYROSCOPE_NAMESPACE="pyroscope"
SERVICEMONITOR_NAMESPACE="grove-system"
OPERATOR_NAMESPACE="grove-system"

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"
MODULE_ROOT="$(dirname "$SCRIPT_DIR")"

USAGE=""

function create_usage() {
  usage=$(printf '%s\n' "
  Deploy monitoring stack for Grove operator scale testing.

  usage: $(basename $0) [Options]

  Options:
    --all | -a                  Deploy complete stack (Prometheus + Grafana + Pyroscope + ServiceMonitor)
    --prometheus | -p           Deploy Prometheus only
    --grafana | -g              Deploy Grafana only
    --pyroscope | -y            Deploy Pyroscope only
    --servicemonitor | -s       Deploy ServiceMonitor only
    --operator-namespace NS     Operator namespace for ServiceMonitor (default: grove-system)
    --prometheus-namespace NS   Prometheus namespace (default: monitoring)
    --grafana-namespace NS      Grafana namespace (default: monitoring)
    --pyroscope-namespace NS    Pyroscope namespace (default: pyroscope)
    -h | --help                 Show this help message

  Examples:
    $(basename $0) --all                    # Full stack
    $(basename $0) --prometheus --grafana   # Prometheus + Grafana only
    $(basename $0) --pyroscope              # Pyroscope only
  ")
  echo "${usage}"
}

function parse_flags() {
  while test $# -gt 0; do
    case "$1" in
      --all | -a)
        DEPLOY_PROMETHEUS=true
        DEPLOY_GRAFANA=true
        DEPLOY_PYROSCOPE=true
        DEPLOY_SERVICEMONITOR=true
        ;;
      --prometheus | -p)
        DEPLOY_PROMETHEUS=true
        ;;
      --grafana | -g)
        DEPLOY_GRAFANA=true
        ;;
      --pyroscope | -y)
        DEPLOY_PYROSCOPE=true
        ;;
      --servicemonitor | -s)
        DEPLOY_SERVICEMONITOR=true
        ;;
      --operator-namespace)
        shift
        OPERATOR_NAMESPACE=$1
        SERVICEMONITOR_NAMESPACE=$1
        ;;
      --prometheus-namespace)
        shift
        PROMETHEUS_NAMESPACE=$1
        ;;
      --grafana-namespace)
        shift
        GRAFANA_NAMESPACE=$1
        ;;
      --pyroscope-namespace)
        shift
        PYROSCOPE_NAMESPACE=$1
        ;;
      -h | --help)
        echo "${USAGE}"
        exit 0
        ;;
      *)
        echo "Unknown flag: $1"
        echo "${USAGE}"
        exit 1
        ;;
    esac
    shift
  done
}

function check_prerequisites() {
  if ! command -v kubectl &> /dev/null; then
    echo "kubectl is not installed. Please install from https://kubernetes.io/docs/tasks/tools/install-kubectl/"
    exit 1
  fi
  if ! command -v helm &> /dev/null; then
    echo "helm is not installed. Please install from https://helm.sh/docs/intro/install/"
    exit 1
  fi
}

function add_helm_repos() {
  echo "Adding Helm repositories..."

  if [ "${DEPLOY_PROMETHEUS}" = true ]; then
    helm repo add prometheus-community https://prometheus-community.github.io/helm-charts 2>/dev/null || true
  fi

  if [ "${DEPLOY_GRAFANA}" = true ] || [ "${DEPLOY_PYROSCOPE}" = true ]; then
    helm repo add grafana https://grafana.github.io/helm-charts 2>/dev/null || true
  fi

  helm repo update
  echo "Helm repositories updated!"
}

function create_namespace() {
  local namespace=$1
  if ! kubectl get namespace "${namespace}" &> /dev/null; then
    echo "Creating namespace: ${namespace}"
    kubectl create namespace "${namespace}"
  else
    echo "Namespace ${namespace} already exists"
  fi
}

function deploy_prometheus() {
  echo ""
  echo "=========================================="
  echo "Deploying Prometheus Stack"
  echo "=========================================="

  create_namespace "${PROMETHEUS_NAMESPACE}"

  # Check if already deployed
  if helm list -n "${PROMETHEUS_NAMESPACE}" | grep -q "kube-prometheus-stack"; then
    echo "Prometheus stack already deployed, upgrading..."
    helm upgrade kube-prometheus-stack prometheus-community/kube-prometheus-stack \
      --namespace "${PROMETHEUS_NAMESPACE}" \
      --set prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false \
      --wait
  else
    echo "Installing Prometheus stack..."
    helm install kube-prometheus-stack prometheus-community/kube-prometheus-stack \
      --namespace "${PROMETHEUS_NAMESPACE}" \
      --set prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false \
      --wait
  fi

  echo "Prometheus stack deployed successfully!"
  echo "Access Prometheus: kubectl port-forward -n ${PROMETHEUS_NAMESPACE} svc/kube-prometheus-stack-prometheus 9090:9090"
}

function deploy_pyroscope() {
  echo ""
  echo "=========================================="
  echo "Deploying Pyroscope"
  echo "=========================================="

  create_namespace "${PYROSCOPE_NAMESPACE}"

  if helm list -n "${PYROSCOPE_NAMESPACE}" | grep -q "pyroscope"; then
    echo "Pyroscope already deployed, upgrading..."
    helm upgrade pyroscope grafana/pyroscope \
      --namespace "${PYROSCOPE_NAMESPACE}" \
      -f "${MODULE_ROOT}/pyroscope-values.yaml" \
      --wait
  else
    echo "Installing Pyroscope..."
    helm install pyroscope grafana/pyroscope \
      --namespace "${PYROSCOPE_NAMESPACE}" \
      -f "${MODULE_ROOT}/pyroscope-values.yaml" \
      --wait
  fi

  echo "Pyroscope deployed successfully!"
  echo "Access Pyroscope: kubectl port-forward -n ${PYROSCOPE_NAMESPACE} svc/pyroscope 4040:4040"
}

function deploy_grafana_dashboards() {
  echo "Creating Grafana dashboard ConfigMap..."

  kubectl create configmap grove-dashboards \
    --from-file=grove-controller-dashboard.json="${MODULE_ROOT}/grove-controller-dashboard.json" \
    --from-file=grove-pyroscope-dashboard.json="${MODULE_ROOT}/grove-pyroscope-dashboard.json" \
    --from-file=grove-unified-dashboard.json="${MODULE_ROOT}/grove-unified-dashboard.json" \
    --from-file=k8s-apiserver-dashboard.json="${MODULE_ROOT}/k8s-apiserver-dashboard.json" \
    --namespace="${GRAFANA_NAMESPACE}" \
    --dry-run=client -o yaml | kubectl apply -f -

  echo "Dashboard ConfigMap created with all Grove dashboards!"
}

function deploy_grafana() {
  echo ""
  echo "=========================================="
  echo "Deploying Grafana"
  echo "=========================================="

  create_namespace "${GRAFANA_NAMESPACE}"

  # Deploy dashboards first
  deploy_grafana_dashboards

  if helm list -n "${GRAFANA_NAMESPACE}" | grep -q "grafana"; then
    echo "Grafana already deployed, upgrading..."
    helm upgrade grafana grafana/grafana \
      --namespace "${GRAFANA_NAMESPACE}" \
      -f "${MODULE_ROOT}/grafana-values.yaml" \
      --wait
  else
    echo "Installing Grafana..."
    helm install grafana grafana/grafana \
      --namespace "${GRAFANA_NAMESPACE}" \
      -f "${MODULE_ROOT}/grafana-values.yaml" \
      --wait
  fi

  echo "Grafana deployed successfully!"
  echo "Access Grafana: kubectl port-forward -n ${GRAFANA_NAMESPACE} svc/grafana 3000:80"
  echo "Default credentials: admin / admin"
}

function deploy_servicemonitor() {
  echo ""
  echo "=========================================="
  echo "Deploying ServiceMonitor"
  echo "=========================================="

  create_namespace "${SERVICEMONITOR_NAMESPACE}"

  echo "Deploying ServiceMonitor for Grove operator in namespace: ${SERVICEMONITOR_NAMESPACE}"

  # Apply ServiceMonitor
  kubectl apply -f "${MODULE_ROOT}/grove-servicemonitor.yaml" -n "${SERVICEMONITOR_NAMESPACE}"

  # Apply RBAC
  kubectl apply -f "${MODULE_ROOT}/prometheus-servicemonitor-rbac.yaml"

  echo "ServiceMonitor deployed successfully!"
  echo "Prometheus will discover metrics at http://grove-operator.${SERVICEMONITOR_NAMESPACE}.svc.cluster.local:9445/metrics"
}

function print_summary() {
  echo ""
  echo "=========================================="
  echo "Monitoring Stack Deployment Complete!"
  echo "=========================================="
  echo ""

  if [ "${DEPLOY_PROMETHEUS}" = true ]; then
    echo "Prometheus:"
    echo "  kubectl port-forward -n ${PROMETHEUS_NAMESPACE} svc/kube-prometheus-stack-prometheus 9090:9090"
    echo "  URL: http://localhost:9090"
    echo ""
  fi

  if [ "${DEPLOY_GRAFANA}" = true ]; then
    echo "Grafana:"
    echo "  kubectl port-forward -n ${GRAFANA_NAMESPACE} svc/grafana 3000:80"
    echo "  URL: http://localhost:3000"
    echo "  Credentials: admin / admin"
    echo ""
  fi

  if [ "${DEPLOY_PYROSCOPE}" = true ]; then
    echo "Pyroscope:"
    echo "  kubectl port-forward -n ${PYROSCOPE_NAMESPACE} svc/pyroscope 4040:4040"
    echo "  URL: http://localhost:4040"
    echo ""
  fi

  if [ "${DEPLOY_SERVICEMONITOR}" = true ]; then
    echo "ServiceMonitor:"
    echo "  Namespace: ${SERVICEMONITOR_NAMESPACE}"
    echo "  Target: grove-operator.${OPERATOR_NAMESPACE}.svc.cluster.local:9445"
    echo ""
  fi

  echo "Next Steps:"
  echo "1. Deploy Grove operator (if not already deployed):"
  echo "   make deploy NAMESPACE=${OPERATOR_NAMESPACE}"
  if [ "${DEPLOY_SERVICEMONITOR}" = false ]; then
    echo "2. Deploy ServiceMonitor:"
    echo "   ./hack/deploy-monitoring-stack.sh --servicemonitor --operator-namespace ${OPERATOR_NAMESPACE}"
  fi
  echo "3. Create scale test environment:"
  echo "   make setup-scale-test NODES=1000"
  echo "4. Deploy test workload:"
  echo "   kubectl apply -f hack/scale-test-1000.yaml"
}

function main() {
  check_prerequisites
  parse_flags "$@"

  # Check that at least one component is selected
  if [ "${DEPLOY_PROMETHEUS}" = false ] && \
     [ "${DEPLOY_GRAFANA}" = false ] && \
     [ "${DEPLOY_PYROSCOPE}" = false ] && \
     [ "${DEPLOY_SERVICEMONITOR}" = false ]; then
    echo "Error: No components selected for deployment"
    echo "${USAGE}"
    exit 1
  fi

  add_helm_repos

  # Deploy in dependency order
  if [ "${DEPLOY_PROMETHEUS}" = true ]; then
    deploy_prometheus
  fi

  if [ "${DEPLOY_PYROSCOPE}" = true ]; then
    deploy_pyroscope
  fi

  if [ "${DEPLOY_GRAFANA}" = true ]; then
    deploy_grafana
  fi

  # Deploy ServiceMonitor last (requires operator to be deployed first)
  if [ "${DEPLOY_SERVICEMONITOR}" = true ]; then
    deploy_servicemonitor
  fi

  print_summary
}

USAGE=$(create_usage)
main "$@"

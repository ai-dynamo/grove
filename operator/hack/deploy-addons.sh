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


set -o errexit
set -o nounset
set -o pipefail

# This script deploys add-ons to the KIND cluster. Run this script after `make kind-up` has created a KIND cluster.
# Ensure that the KUBECONFIG points to the KIND cluster and invoke this script.

USAGE=""
DEPLOY_METRICS_SERVER=false
DEPLOY_KWOK=false

function create_usage() {
  usage=$(printf '%s\n' "
  usage: $(basename $0) [Options]
  Options:
    --all | -a                 Deploys all the supported add-ons.
    --metrics-server | -m      Deploys a metrics server if this option is specified.
    --kwok | -k                Deploys KWOK if this option is specified.
  ")
  echo "${usage}"
}

function parse_flags() {
  while test $# -gt 0; do
    case "$1" in
      --all | -a)
        DEPLOY_KWOK=true
        DEPLOY_METRICS_SERVER=true
        ;;
      --metrics-server | -m)
        DEPLOY_METRICS_SERVER=true
        ;;
      --kwok | -k)
        DEPLOY_KWOK=true
        ;;
      -h | --help)
        shift
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

function check_prereq() {
  if ! command -v kubectl &> /dev/null; then
    echo >&2 "kubectl is not installed, please install kubectl from https://kubernetes.io/docs/tasks/tools/install-kubectl/"
    exit 1
  fi
  if ! command -v jq &> /dev/null; then
    echo >&2 "jq is not installed, please install jq from https://jqlang.org/download"
    exit 1
  fi
}

# deploy_metrics_server deploys the metrics server.
function deploy_metrics_server() {
  if [ "${DEPLOY_METRICS_SERVER}" = true ]; then
    printf "Deploying [Metrics-Server]\n"
    printf "%s\n" "---------------------------------------------------"
    kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
    printf "[Metrics-Server]: Patching deployment to set insecure TLS for local deployment...\n"
    kubectl patch -n kube-system deployments.apps metrics-server --type=json -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
    printf "\n[Metrics-Server]: Deployed successfully!\n"
  fi
}

# deploy_kwok deploys KWOK using the instructions at https://kwok.sigs.k8s.io/docs/user/kwok-in-cluster/
function deploy_kwok() {
  if [ "${DEPLOY_KWOK}" = true ]; then
    local kwok_repo="kubernetes-sigs/kwok"
    local kwok_latest_release=$(curl "https://api.github.com/repos/${kwok_repo}/releases/latest" | jq -r '.tag_name')
    printf "Deploying [KWOK]\n"
    printf "%s\n" "---------------------------------------------------"
    # deploy KWOK CRDs and controller
    printf "[KWOK]: Deploying CRDs and controller...\n"
    kubectl apply -f "https://github.com/${kwok_repo}/releases/download/${kwok_latest_release}/kwok.yaml"

    # setup default custom resources of stages
    printf "[KWOK]: Setting up default custom resources of stages...\n"
    kubectl apply -f "https://github.com/${kwok_repo}/releases/download/${kwok_latest_release}/stage-fast.yaml"
    printf "\n[KWOK]: Deployed successfully!\n"
  fi
}

function main() {
  check_prereq
  parse_flags "$@"
  deploy_metrics_server
  deploy_kwok
}

USAGE=$(create_usage)
main "$@"
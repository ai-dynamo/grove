#!/usr/bin/env bash
# /*
# Copyright 2024 The Grove Authors.
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


SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"
OPERATOR_GO_MODULE_ROOT="$(dirname "$SCRIPT_DIR")"
KIND_CONFIG_DIR="${SCRIPT_DIR}/kind"
CLUSTER_NAME="grove-test-cluster"
DEPLOY_REGISTRY=true
RECREATE_CLUSTER=false
FEATURE_GATES=()
FAKE_NODES=0
USAGE=""

function kind::create_usage() {
  usage=$(printf '%s\n' "
  usage: $(basename $0) [Options]
  Options:
    -n | --cluster-name  <cluster-name>   Name of the kind cluster to create. Default value is 'grove-test-cluster'
    -s | --skip-registry                  Skip creating a local docker registry. Default value is false.
    -r | --recreate                       If this flag is specified then it will recreate the cluster if it already exists.
    -g | --feature-gates <feature-gates>  Comma separated list of feature gates to enable on the cluster.
    -f | --fake-nodes    <count>          Number of fake nodes to create using KWOK. Default value is 0.
  ")
  echo "${usage}"
}

function kind::check_prerequisites() {
  if ! command -v docker &> /dev/null; then
    echo "docker is not installed. Please install docker from https://docs.docker.com/get-docker/"
    exit 1
  fi
  if ! command -v kind &> /dev/null; then
    echo "kind is not installed. Please install kind from https://kind.sigs.k8s.io/docs/user/quick-start/"
    exit 1
  fi
  if ! command -v yq &> /dev/null; then
    echo "yq is not installed. Please install yq from https://mikefarah.gitbook.io/yq/"
    exit 1
  fi
}

function kind::parse_flags() {
  while test $# -gt 0; do
    case "$1" in
      --cluster-name | -n)
        shift
        CLUSTER_NAME=$1
        ;;
      --skip-registry | -s)
        DEPLOY_REGISTRY=false
        shift
        ;;
      --recreate | -r)
        RECREATE_CLUSTER=true
        shift
        ;;
      --feature-gates | -g)
        shift
        IFS=',' read -r -a FEATURE_GATES <<< "$1"
        unset IFS
        ;;
      --fake-nodes | -f)
        shift
        FAKE_NODES=$1
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

function kind::warn_mtu_issues() {
  if [[ "$OSTYPE" != "darwin"* ]]; then
    echo "NOTE: If you experience network timeouts, you may have MTU/PMTU issues with Docker networking."
    echo "See: https://github.com/kubernetes/test-infra/issues/23741"
  fi
}

function kind::generate_config() {
  echo "Generating kind cluster config..."
  cat >"${KIND_CONFIG_DIR}/cluster-config.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  image: kindest/node:v1.33.4
EOF
  if [ "${DEPLOY_REGISTRY}" = true ]; then
    echo "Adding registry config to the kind cluster config..."
    printf -v reg '[plugins."io.containerd.grpc.v1.cri".registry]
      config_path = "/etc/containerd/certs.d"'; reg="$reg" yq -i '.containerdConfigPatches[0] = strenv(reg)' "${KIND_CONFIG_DIR}/cluster-config.yaml"
  fi
  if [ ${#FEATURE_GATES[@]} -gt 0 ]; then
    echo "Adding feature gates to the kind cluster config..."
    for key in "${FEATURE_GATES[@]}"; do
      feature_key="$key" yq -i 'with(.featureGates.[strenv(feature_key)]; . = true | key style="double")' "${KIND_CONFIG_DIR}/cluster-config.yaml"
    done
  fi
}

function kind::create_cluster() {
  if [ "${DEPLOY_REGISTRY}" = true ]; then
    kind::create_local_docker_registry_container
  fi
  if [[ "${RECREATE_CLUSTER}" == true ]]; then
    cluster_exists=$(kind::does_cluster_exist)
    if [[ "${cluster_exists}" == "true" ]]; then
      echo "Deleting the existing cluster as you have chosen to recreate"
      kind::delete_cluster
    fi
  fi
  mkdir -p "${KIND_CONFIG_DIR}"
  echo "Creating kind cluster ${CLUSTER_NAME}..."
  kind::generate_config
  
  # If KUBECONFIG is not already set (e.g., by the Makefile), set it to our default location
  # This ensures kubectl commands target the correct cluster
  if [ -z "${KUBECONFIG:-}" ]; then
    export KUBECONFIG="${KIND_CONFIG_DIR}/kubeconfig"
    echo "Setting KUBECONFIG to ${KUBECONFIG}"
  fi
  
  kind create cluster --name "${CLUSTER_NAME}" --config "${KIND_CONFIG_DIR}/cluster-config.yaml"
  
  if [ "${DEPLOY_REGISTRY}" = true ]; then
    kind::initialize_registry
    kind::create_local_container_reg_configmap
  fi
}

function kind::does_cluster_exist() {
  local existing_clusters exists
  exists="false"
  existing_clusters=($(echo $( (kind get clusters) 2>&1) | tr '\n' ' '))
  for cluster in "${existing_clusters[@]}"; do
    if [[ "${cluster}" =~ ^"${CLUSTER_NAME}"$ ]]; then
      exists="true"
      break
    fi
  done
  echo "${exists}"
}

# NOTE: Container Registry Creation has been taken from https://kind.sigs.k8s.io/docs/user/local-registry/

REG_NAME='kind-registry'
REG_PORT='5001'

function kind::create_local_docker_registry_container() {
  # create registry container unless it already exists
  if [ "$(docker inspect -f '{{.State.Running}}' "${REG_NAME}" 2>/dev/null || true)" != 'true' ]; then
    echo "Creating local docker registry..."
    docker run \
      -d --restart=always -p "127.0.0.1:${REG_PORT}:5000" \
      --network bridge --name "${REG_NAME}" \
      registry:2
  fi
}

function kind::initialize_registry() {
  # Add the registry config to the node(s)
  # This is necessary because localhost resolves to loopback addresses that are
  # network-namespace local.
  # In other words: localhost in the container is not localhost on the host.
  # We want a consistent name that works from both ends, so we tell containerd to
  # alias localhost:${REG_PORT} to the registry container when pulling images.
  echo "Initializing local docker registry..."
  local registry_dir="/etc/containerd/certs.d/localhost:${REG_PORT}"
  for node in $(kind get nodes --name ${CLUSTER_NAME}); do
    docker exec "${node}" mkdir -p "${registry_dir}"
    cat <<EOF | docker exec -i "${node}" cp /dev/stdin "${registry_dir}/hosts.toml"
  [host."http://${REG_NAME}:5000"]
EOF
  done

  # Connect the registry to the cluster network if not already connected
  # This allows kind to bootstrap the network but ensures they're on the same network
  if [ "$(docker inspect -f='{{json .NetworkSettings.Networks.kind}}' "${REG_NAME}")" = 'null' ]; then
    docker network connect "kind" "${REG_NAME}"
  fi
}

function kind::create_local_container_reg_configmap() {
  # Document the local registry
  # https://github.com/kubernetes/enhancements/tree/master/keps/sig-cluster-lifecycle/generic/1755-communicating-a-local-registry
  cat <<EOF | kubectl apply -f -
  apiVersion: v1
  kind: ConfigMap
  metadata:
    name: local-registry-hosting
    namespace: kube-public
  data:
    localRegistryHosting.v1: |
      host: "localhost:${REG_PORT}"
      help: "https://kind.sigs.k8s.io/docs/user/local-registry/"
EOF
}

function kind::delete_cluster() {
  echo "Deleting kind cluster..."
  kind delete cluster --name ${CLUSTER_NAME}
}

function kind::delete_container_registry() {
	local reg_container_name="kind-registry"
	if [ "$(docker ps -qa -f name=${reg_container_name})" ]; then
	  if [ "$(docker ps -q -f name=${reg_container_name})" ]; then
	    echo "Stopping running container $reg_container_name..."
      docker stop "${reg_container_name}" > /dev/null
    fi
    echo "Removing container $reg_container_name..."
    docker rm "${reg_container_name}" > /dev/null
	fi
}

function kind::check_fake_nodes_prerequisites() {
  if ! command -v kubectl &> /dev/null; then
    echo "kubectl is not installed. Please install kubectl from https://kubernetes.io/docs/tasks/tools/install-kubectl/"
    exit 1
  fi
  if ! command -v jq &> /dev/null; then
    echo "jq is not installed. Please install jq from https://jqlang.org/download"
    exit 1
  fi
  if ! command -v curl &> /dev/null; then
    echo "curl is not installed. Please install curl."
    exit 1
  fi
}

# deploy_kwok deploys KWOK using the instructions at https://kwok.sigs.k8s.io/docs/user/kwok-in-cluster/
function kind::deploy_kwok() {
  local kwok_repo="kubernetes-sigs/kwok"
  local kwok_latest_release=$(curl -s "https://api.github.com/repos/${kwok_repo}/releases/latest" | jq -r '.tag_name')
  echo "Deploying KWOK ${kwok_latest_release}..."
  
  # deploy KWOK CRDs and controller
  echo "  Installing KWOK CRDs and controller..."
  kubectl apply -f "https://github.com/${kwok_repo}/releases/download/${kwok_latest_release}/kwok.yaml" > /dev/null
  
  # setup default custom resources of stages
  echo "  Setting up KWOK stage configurations..."
  kubectl apply -f "https://github.com/${kwok_repo}/releases/download/${kwok_latest_release}/stage-fast.yaml" > /dev/null
  
  # Wait for KWOK controller to be ready
  echo "  Waiting for KWOK controller to be ready..."
  kubectl wait --for=condition=available --timeout=60s deployment/kwok-controller -n kube-system > /dev/null
  
  echo "KWOK deployed successfully!"
}

function kind::create_fake_nodes() {
  local node_count=$1
  echo "Creating ${node_count} fake nodes..."
  
  for ((i=1; i<=node_count; i++)); do
    local node_name="fake-node-$(printf "%03d" $i)"
    cat <<EOF | kubectl apply -f - > /dev/null
apiVersion: v1
kind: Node
metadata:
  annotations:
    node.alpha.kubernetes.io/ttl: "0"
    kwok.x-k8s.io/node: fake
  labels:
    beta.kubernetes.io/arch: amd64
    beta.kubernetes.io/os: linux
    kubernetes.io/arch: amd64
    kubernetes.io/hostname: ${node_name}
    kubernetes.io/os: linux
    kubernetes.io/role: agent
    node-role.kubernetes.io/agent: ""
    type: kwok
  name: ${node_name}
spec:
  taints:
  - effect: NoSchedule
    key: fake-node
    value: "true"
status:
  allocatable:
    cpu: "64"
    ephemeral-storage: 1Ti
    hugepages-1Gi: "0"
    hugepages-2Mi: "0"
    memory: 512Gi
    pods: "110"
  capacity:
    cpu: "64"
    ephemeral-storage: 1Ti
    hugepages-1Gi: "0"
    hugepages-2Mi: "0"
    memory: 512Gi
    pods: "110"
  conditions:
  - lastHeartbeatTime: "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    lastTransitionTime: "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    message: kubelet is posting ready status
    reason: KubeletReady
    status: "True"
    type: Ready
  - lastHeartbeatTime: "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    lastTransitionTime: "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    message: kubelet has sufficient memory available
    reason: KubeletHasSufficientMemory
    status: "False"
    type: MemoryPressure
  - lastHeartbeatTime: "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    lastTransitionTime: "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    message: kubelet has no disk pressure
    reason: KubeletHasNoDiskPressure
    status: "False"
    type: DiskPressure
  - lastHeartbeatTime: "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    lastTransitionTime: "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    message: kubelet has sufficient PID available
    reason: KubeletHasSufficientPID
    status: "False"
    type: PIDPressure
  - lastHeartbeatTime: "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    lastTransitionTime: "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    message: network is available
    reason: RouteCreated
    status: "False"
    type: NetworkUnavailable
  nodeInfo:
    architecture: amd64
    bootID: ""
    containerRuntimeVersion: ""
    kernelVersion: ""
    kubeProxyVersion: fake
    kubeletVersion: fake
    machineID: ""
    operatingSystem: linux
    osImage: ""
    systemUUID: ""
  phase: Running
EOF
    echo "  Created fake node: ${node_name}"
  done
  
  echo "Successfully created ${node_count} fake nodes!"
  echo ""
  echo "Fake nodes are tainted with 'fake-node=true:NoSchedule'"
  echo "To schedule pods on these nodes, add this toleration to your pod specs:"
  echo "  tolerations:"
  echo "  - key: fake-node"
  echo "    operator: Exists"
  echo "    effect: NoSchedule"
}

function kind::setup_fake_nodes() {
  if [ "${FAKE_NODES}" -gt 0 ]; then
    echo ""
    echo "Setting up ${FAKE_NODES} fake nodes..."
    kind::check_fake_nodes_prerequisites
    kind::deploy_kwok
    kind::create_fake_nodes "${FAKE_NODES}"
  fi
}

function main() {
  kind::check_prerequisites
  kind::parse_flags "$@"
  kind::warn_mtu_issues
  kind::create_cluster
  kind::setup_fake_nodes
  printf "\n\033[0;33m📌 NOTE: To target the newly created kind cluster, please run the following command:\n\n export KUBECONFIG=${KUBECONFIG}\n\033[0m\n"
}

USAGE=$(kind::create_usage)
main "$@"

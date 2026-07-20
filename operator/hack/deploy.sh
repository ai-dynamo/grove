#!/usr/bin/env bash
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

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"
MODULE_ROOT="$(dirname $SCRIPT_DIR)"
REPO_ROOT="$(dirname $MODULE_ROOT)"

# Default container registry
DEFAULT_CONTAINER_REGISTRY="localhost:5001"

# Specify the variables necessary for the generation of the ldflags before sourcing the function
PACKAGE_PATH=${PACKAGE_PATH}
PROGRAM_NAME=${PROGRAM_NAME}
VERSION="$(cat "${MODULE_ROOT}/VERSION")"
export VERSION="${VERSION}"

source $REPO_ROOT/hack/ld-flags.sh

function check_prereq() {
  if ! command -v skaffold &>/dev/null; then
    echo >&2 "skaffold is not installed, please install skaffold from https://skaffold.dev/docs/install/"
    exit 1
  fi
  if ! command -v kubectl &>/dev/null; then
    echo >&2 "kubectl is not installed, please install kubectl from https://kubernetes.io/docs/tasks/tools/install-kubectl/"
    exit 1
  fi
}

function skaffold_deploy() {
  local ld_flags=$(build_ld_flags)
  export LD_FLAGS="${ld_flags}"
  
  # Set default registry if not provided
  if [[ -z "${CONTAINER_REGISTRY:-}" ]]; then
    export CONTAINER_REGISTRY="${DEFAULT_CONTAINER_REGISTRY}"
  fi

  skaffold --default-repo=${CONTAINER_REGISTRY} "$@"
}

function main() {
  echo "Checking prerequisites..."
  check_prereq
  echo "Skaffolding grove operator..."
  skaffold_deploy "$@"
}

main "$@"


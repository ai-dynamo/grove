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
PROJECT_ROOT="$(dirname "$OPERATOR_GO_MODULE_ROOT")"
SCHEDULER_GO_MODULE_ROOT="${PROJECT_ROOT}/scheduler"

function copy_crds() {
  target_path="${OPERATOR_GO_MODULE_ROOT}/charts/crds"
  echo "Creating ${target_path} to copy the CRDs if not present..."
  mkdir -p ${target_path}

  echo "Copying grove-operator CRDS..."
  declare -a operator_crds=("grove.io_podcliquesets.yaml" "grove.io_podcliques.yaml" "grove.io_podcliquescalinggroups.yaml" "grove.io_clustertopologies.yaml")
  for crd in "${operator_crds[@]}"; do
    local src_crd_path="${OPERATOR_GO_MODULE_ROOT}/api/core/v1alpha1/crds/${crd}"
    if [ ! -f ${src_crd_path} ]; then
      echo "CRD ${crd} not found in ${src_crd_path}, run 'make generate' first"
      make generate
    fi
    echo "Copying CRD ${crd} to ${target_path}"
    cp ${src_crd_path} ${target_path}
  done

  echo "Copying scheduler CRDS..."
  declare -a scheduler_crds=("scheduler.grove.io_podgangs.yaml")
  for crd in "${scheduler_crds[@]}"; do
    local src_crd_path="${SCHEDULER_GO_MODULE_ROOT}/api/core/v1alpha1/crds/${crd}"
    if [ ! -f ${src_crd_path} ]; then
      echo "CRD ${crd} not found in ${src_crd_path}, run 'make generate' first"
      make --directory="${SCHEDULER_GO_MODULE_ROOT}"/api generate
    fi
    echo "Copying CRD ${crd} to ${target_path}"
    cp ${src_crd_path} ${target_path}
  done
}

echo "Copying CRDs to helm charts..."
copy_crds

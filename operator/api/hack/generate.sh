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
MODULE_ROOT="$(dirname "$SCRIPT_DIR")"
OPERATOR_ROOT="$(dirname "$MODULE_ROOT")"
REPO_ROOT="$(dirname "$OPERATOR_ROOT")"
REPO_HACK_DIR=${REPO_ROOT}/hack
TOOLS_BIN_DIR="${REPO_HACK_DIR}/tools/bin"

trap cleanup EXIT

function setup() {
  # ensure that the version of code-generator used is the same as that of k8s.io/api
  k8s_api_version=$(GOWORK=off go list -mod=mod -f '{{ .Version }}' -m k8s.io/api)
  go get -tool k8s.io/code-generator@${k8s_api_version}
  CODE_GEN_DIR=$(go list -m -f '{{.Dir}}' k8s.io/code-generator)
  source "${CODE_GEN_DIR}/kube_codegen.sh"
}

function cleanup() {
  rm -rf ${MODULE_ROOT}/hack/tools
}

function check_controller_gen_prereq() {
  if ! command -v controller-gen &>/dev/null; then
    echo >&2 "controller-gen is not available, cannot generate deepcopy/runtime.Object for the API types and cannot generate CRDs"
    exit 1
  fi
}

function generate_deepcopy_defaulter() {
  kube::codegen::gen_helpers \
    --boilerplate "${REPO_HACK_DIR}/boilerplate.generatego.txt" \
    "${MODULE_ROOT}"
}

function generate_crds() {
  local output_dir="${MODULE_ROOT}/core/v1alpha1/crds"
  local package="github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
  local package_path="$(go list -f '{{.Dir}}' "${package}")"

  if [ -z "${package_path}" ]; then
    echo >&2 "Could not locate directory for package: ${package}"
    exit 1
  fi

  if [ -z "${output_dir}" ]; then
    mkdir -p "${output_dir}"
  fi

  # clean all generated crd files
  if ls "${output_dir}/*.yaml" 1> /dev/null 2>&1; then
    rm "${output_dir}/*.yaml"
  fi

  controller-gen crd paths="${package_path}" output:crd:dir="${output_dir}" output:stdout
}

function generate_clientset() {
  kube::codegen::gen_client \
    --with-watch \
    --output-dir "${OPERATOR_ROOT}/client" \
    --output-pkg "github.com/ai-dynamo/grove/operator/client" \
    --boilerplate "${REPO_HACK_DIR}/boilerplate.generatego.txt" \
    "${MODULE_ROOT}"
}

function main() {
  setup

  echo "> Generate..."
  go generate "${MODULE_ROOT}/..."

  echo "> Generating DeepCopy and Defaulting functions..."
  generate_deepcopy_defaulter

  check_controller_gen_prereq
  echo "> Generate CRDs..."
  generate_crds

  echo "> Generating ClientSet..."
  generate_clientset
}

main

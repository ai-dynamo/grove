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

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"
MODULE_ROOT="$(dirname $SCRIPT_DIR)"
REPO_ROOT="$(dirname $MODULE_ROOT)"
GOOS=${GOOS:-$(go env GOOS)}
GOARCH=${GOARCH:-$(go env GOARCH)}
BINARY_DIR="${MODULE_ROOT}/bin"

# Specify the variables necessary for the generation of the ldflags before sourcing the function
PACKAGE_PATH=${PACKAGE_PATH}
PROGRAM_NAME=${PROGRAM_NAME}
VERSION=${VERSION:-$(cat "${MODULE_ROOT}/VERSION")}

source $REPO_ROOT/hack/ld-flags.sh

function build_grove_install_crds() {
  local ld_flags=$(build_ld_flags)
  printf '%s\n' "Building grove-install-crds with:
   GOOS: $GOOS
   GOARCH: $GOARCH
   ldflags: $ld_flags ..."

  CGO_ENABLED=0 GOOS=${GOOS} GOARCH=${GOARCH} GO111MODULE=on \
    go build \
    -o "${BINARY_DIR}/grove-install-crds" \
    -ldflags "${ld_flags}" \
    cmd/install-crds/main.go
}

mkdir -p ${BINARY_DIR}
build_grove_install_crds

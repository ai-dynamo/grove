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
MODULE_ROOT="$(dirname "$SCRIPT_DIR")"
REPO_ROOT="$(dirname "$MODULE_ROOT")"
GOARCH=${GOARCH:-$(go env GOARCH)}
PLATFORM=${PLATFORM:-linux/${GOARCH}}
VERSION=${VERSION:-$(cat "${MODULE_ROOT}/VERSION")}
DOCKER_BUILD_ADDITIONAL_ARGS=${DOCKER_BUILD_ADDITIONAL_ARGS:-""}

if [[ -n "${REGISTRY:-}" ]]; then
  INITC_IMAGE="${REGISTRY}/grove-initc"
  OPERATOR_IMAGE="${REGISTRY}/grove-operator"
  INSTALL_CRDS_IMAGE="${REGISTRY}/grove-install-crds"
else
  INITC_IMAGE="grove-initc"
  OPERATOR_IMAGE="grove-operator"
  INSTALL_CRDS_IMAGE="grove-install-crds"
fi

function build_docker_images() {
  printf '%s\n' "Building grove-initc:${VERSION} with:
   PLATFORM: ${PLATFORM}... "
  docker buildx build \
    ${DOCKER_BUILD_ADDITIONAL_ARGS} \
    --platform ${PLATFORM} \
    --build-arg VERSION=${VERSION} \
    --tag ${INITC_IMAGE}:latest \
    --tag ${INITC_IMAGE}:${VERSION} \
    --target grove-initc \
    --file ${MODULE_ROOT}/Dockerfile \
    $REPO_ROOT # docker context is as the repository root to access `.git/`

  printf '%s\n' "Building grove-operator:${VERSION} with:
   PLATFORM: ${PLATFORM}... "
  docker buildx build \
    ${DOCKER_BUILD_ADDITIONAL_ARGS} \
    --platform ${PLATFORM} \
    --build-arg VERSION=${VERSION} \
    --tag ${OPERATOR_IMAGE}:latest \
    --tag ${OPERATOR_IMAGE}:${VERSION} \
    --target grove-operator \
    --file ${MODULE_ROOT}/Dockerfile \
    $REPO_ROOT # docker context is as the repository root to access `.git/`

  printf '%s\n' "Building grove-install-crds:${VERSION} with:
   PLATFORM: ${PLATFORM}... "
  docker buildx build \
    ${DOCKER_BUILD_ADDITIONAL_ARGS} \
    --platform ${PLATFORM} \
    --build-arg VERSION=${VERSION} \
    --tag ${INSTALL_CRDS_IMAGE}:latest \
    --tag ${INSTALL_CRDS_IMAGE}:${VERSION} \
    --target grove-install-crds \
    --file ${MODULE_ROOT}/Dockerfile \
    $REPO_ROOT # docker context is as the repository root to access `.git/`
}

build_docker_images

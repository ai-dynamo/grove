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
CHARTS_DIR="${OPERATOR_GO_MODULE_ROOT}/charts"

function copy_crds() {
  # CRDs are no longer copied to charts/crds/ for Helm chart delivery.
  # Grove CRDs are now embedded in the operator binary and applied at startup
  # via an init container running the "install-crds" subcommand (server-side apply).
  # See operator/internal/crdinstaller and operator/charts/templates/deployment.yaml.
  echo "CRDs are delivered via the operator init container — no chart CRD copy needed."
}

echo "Preparing helm charts..."
copy_crds
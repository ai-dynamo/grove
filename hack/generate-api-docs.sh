#!/usr/bin/env bash
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

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"
REPO_ROOT="$(dirname $SCRIPT_DIR)"

mkdir -p "${REPO_ROOT}/docs/api-reference"

echo "> Generating API docs for Grove Operator APIs..."
crd-ref-docs \
  --source-path "${REPO_ROOT}/operator/api" \
  --config "${SCRIPT_DIR}/apidocs/config.yaml" \
  --output-path "${REPO_ROOT}/docs/api-reference/operator-api.md" \
  --renderer markdown

echo "> Generating API docs for Grove Scheduler APIs..."
crd-ref-docs \
  --source-path "${REPO_ROOT}/scheduler/api/core" \
  --config "${SCRIPT_DIR}/apidocs/config.yaml" \
  --output-path "${REPO_ROOT}/docs/api-reference/scheduler-api.md" \
  --renderer markdown

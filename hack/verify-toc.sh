#!/usr/bin/env bash
# /*
# Copyright 2026 The Grove Authors.
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
REPO_ROOT="$(dirname $SCRIPT_DIR)"

echo "> Verifying if table of contents for docs/proposals are up-to-date..."

find ${REPO_ROOT}/docs/proposals -name '*.md' \
  | sed "s|^${REPO_ROOT}/||" \
  | grep -Fxvf "${SCRIPT_DIR}/.notableofcontents" \
  | sed "s|^|${REPO_ROOT}/|" \
  | xargs mdtoc --inplace --max-depth=6 --dryrun || (
    echo "Table of content not up to date. Did you run 'make update-toc' ?"
    exit 1
  )

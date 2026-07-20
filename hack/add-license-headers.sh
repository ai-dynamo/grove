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

echo "> Adding Apache License header to all go files where it is not present"

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"
REPO_HACK_DIR=$(dirname "$SCRIPT_DIR")/hack

# addlicense with a license file (parameter -f) expects no comments in the file,
# as it adds the comment style appropriate for each file type itself.

addlicense \
  -f "${REPO_HACK_DIR}/boilerplate.go.txt" \
  -c "The Grove Authors." \
  -ignore "**/*.md" \
  -ignore "**/*.yaml" \
  -ignore "**/*.yml" \
  -ignore "**/Dockerfile" \
  .

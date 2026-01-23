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

echo "> Updating table of contents for docs/proposals..."

# Process each markdown file individually
while IFS= read -r file; do
  # Convert to relative path for exclusion check
  rel_path="${file#${REPO_ROOT}/}"

  # Check if file should be excluded
  if grep -Fxq "${rel_path}" "${SCRIPT_DIR}/.notableofcontents" 2>/dev/null; then
    echo "⊘ Excluded: ${rel_path}"
    continue
  fi


  # Create a temporary copy to compare against
  temp_file=$(mktemp)
  cp "${file}" "${temp_file}"

  # Run mdtoc on the file
  if mdtoc --inplace --max-depth=6 "${file}" 2>/dev/null; then
    # Compare to see if anything changed
    if diff -q "${temp_file}" "${file}" >/dev/null 2>&1; then
      echo "✓ Unchanged: ${rel_path}"
    else
      echo "✓ Updated: ${rel_path}"
    fi
  else
    echo "✗ Failed: ${rel_path}"
    rm -f "${temp_file}"
    exit 1
  fi

  rm -f "${temp_file}"

done < <(find "${REPO_ROOT}/docs/proposals" -name '*.md' -type f | sort)
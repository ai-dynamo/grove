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

# NOTE: This script should be sourced into other scripts.
# This script expects the following variables to be declared by the shell scripts sourcing this file:
# - PACKAGE_PATH: this variable defines where the package in which the variables written to by the ld-flags exist. Not optional
# - PROGRAM_NAME: the name of the program written to the binary. Not optional.
# - VERSION: the git version that is to be baked into the binary. Optional.

# A reusable function which computes the LD Flags.
function build_ld_flags() {
  # NOTE: the program_name override does not seem to be working, please troubelshoot later
  local build_date="$(date '+%Y-%m-%dT%H:%M:%S%z' | sed 's/\([0-9][0-9]\)$/:\1/g')"

  # .dockerignore ignores files that are not relevant for the build. It only copies the relevant source files to the
  # build container. Git, on the other hand will always detect a dirty work tree when building in a container (many deleted files).
  # This command filters out all deleted files that are ignored by .dockerignore and only detects changes to files that are included
  # to determine dirty work tree.
  # Use `git status --porcelain $MODULE_ROOT` to only check the status of the directory
  local tree_state="$([ -z "$(git status --porcelain $MODULE_ROOT 2>/dev/null | grep -vf <(git ls-files -o --deleted --ignored --exclude-from=${MODULE_ROOT}/Dockerfile.dockerignore))" ] && echo clean || echo dirty)"

  FLAGS="-X $PACKAGE_PATH/version.gitCommit=$(git rev-parse --verify HEAD)
         -X $PACKAGE_PATH/version.gitTreeState=$tree_state
         -X $PACKAGE_PATH/version.buildDate=$build_date"

  # The k8s.component-base/version.gitVersion can not be set to the version of grove
  # due to the error: "emulation version 1.33 is not between [1.31, 0.1.0-dev]".
  # https://github.com/kubernetes/kubernetes/blob/5dc8b8dd268f2170286a75c142781f4db1da9020/staging/src/k8s.io/component-base/compatibility/version.go#L165
  if [[ -n "${VERSION:-}" ]]; then
    FLAGS="$FLAGS
         -X $PACKAGE_PATH/version.gitVersion=$VERSION"
  fi

  echo "$FLAGS"
}

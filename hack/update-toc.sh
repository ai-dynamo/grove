#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"
REPO_ROOT="$(dirname $SCRIPT_DIR)"

echo "> Updating table of contents for docs/proposals..."

find ${REPO_ROOT}/docs/proposals -name '*.md' \
  | sed "s|^${REPO_ROOT}/||" \
  | grep -Fxvf "${SCRIPT_DIR}/.notableofcontents" \
  | sed "s|^|${REPO_ROOT}/|" \
  | xargs mdtoc --inplace --max-depth=6 || (
    echo " Failed to generate/update table of contents for docs/proposals"
    exit 1
  )

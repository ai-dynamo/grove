#!/usr/bin/env bash

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

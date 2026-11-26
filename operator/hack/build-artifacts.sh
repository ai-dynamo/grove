#!/usr/bin/env bash
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


set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd -P)"
SOURCE_ROOT="$REPO_ROOT"
PR=""
PR_REPOSITORY="https://github.com/ai-dynamo/grove.git"
REGISTRY=""
PLATFORM=""
VERSION=""
OUTPUT_DIR=""
WORK_DIR=""
COMMIT=""
IMAGES=(grove-operator grove-initc grove-install-crds)

# usage describes the arguments, required tools, and build-only scope of this script.
function usage() {
  printf '%s\n' "Usage: $0 --registry URL --platform PLATFORM [options]

Build Grove test images and package the matching Helm chart from this checkout or a PR.
No images are pushed and no cluster is contacted.
Your working checkout and its Git refs are not changed.

Required:
  --registry URL       Lowercase registry URL without a scheme or trailing slash.
                       Examples: registry.example.com/team/grove or localhost/grove-test
  --platform PLATFORM  linux/amd64 or linux/arm64 (one platform per invocation)

Options:
  --pr NUMBER          Build the PR head from ai-dynamo/grove in a temporary checkout
  --version VERSION    Helm-compatible image tag, without '+' build metadata
                       Default: 0.0.0-g<12-character commit SHA>
  --output-dir DIR     New directory outside the checkout, with an existing parent
                       Default: \$TMPDIR/grove-test-artifacts-<version> (or /tmp)
  -h, --help           Show this help

Requires Git, Make, Go, Docker with Buildx, Helm, and tar. No gh CLI is needed.
Local builds require clean source files, except a new, uncommitted copy of this helper.
PR builds ignore local changes and execute the selected PR's build code. Use a reviewed PR.
The existing Docker build target requires a checkout path without whitespace.
It also assigns local 'latest' image tags.
Printed publishing commands use only the versioned tags."
}

# fail reports an error on stderr and stops the script with a nonzero exit status.
function fail() {
  printf 'Error: %s\n' "$*" >&2
  exit 1
}

# parse_flags reads command-line options into the build settings and rejects unknown or incomplete options.
function parse_flags() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --pr|--registry|--platform|--version|--output-dir)
        [[ $# -ge 2 && -n "$2" && "$2" != --* ]] || fail "$1 requires a value"
        case "$1" in
          --pr) PR="$2" ;;
          --registry) REGISTRY="$2" ;;
          --platform) PLATFORM="$2" ;;
          --version) VERSION="$2" ;;
          --output-dir) OUTPUT_DIR="$2" ;;
        esac
        shift 2
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *) fail "unknown argument: $1 (use --help)" ;;
    esac
  done
}

# validate_flags rejects missing or malformed build settings before any build work.
# Helm performs the final version validation when packaging the chart.
function validate_flags() {
  if [[ -n "$PR" ]]; then
    [[ "$PR" =~ ^[1-9][0-9]*$ ]] || fail "--pr must be a positive PR number"
  fi
  [[ -n "$REGISTRY" ]] || fail "--registry is required"
  [[ -n "$PLATFORM" ]] || fail "--platform is required"
  [[ "$REGISTRY" =~ ^[a-z0-9][a-z0-9.-]*(:[0-9]+)?(/[a-z0-9]+([._-][a-z0-9]+)*)*$ ]] ||
    fail "--registry must be a lowercase registry URL such as registry.example.com/team/grove or localhost/grove-test, without a scheme or trailing slash"
  case "$PLATFORM" in
    linux/amd64|linux/arm64) ;;
    *) fail "--platform must be linux/amd64 or linux/arm64" ;;
  esac
  if [[ -n "$VERSION" ]]; then
    [[ "$VERSION" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ && ${#VERSION} -le 128 ]] ||
      fail "--version must be a Helm-compatible version and Docker tag, without '+' build metadata"
  fi
}

# check_prerequisites verifies that the required command-line tools are available on PATH.
# Docker daemon and Buildx availability are checked just before building images.
function check_prerequisites() {
  local tool
  for tool in git make go docker helm tar; do
    command -v "$tool" >/dev/null 2>&1 || fail "required tool not found: $tool"
  done
}

# prepare_source_checkout fetches the PR head into a separate repository when --pr is set.
# The local helper drives the build, so the PR does not need to contain this script.
function prepare_source_checkout() {
  [[ -n "$PR" ]] || return 0
  SOURCE_ROOT="$WORK_DIR/source"
  printf 'Fetching PR #%s from %s\n' "$PR" "$PR_REPOSITORY"
  git init --quiet "$SOURCE_ROOT"
  GIT_TERMINAL_PROMPT=0 git -C "$SOURCE_ROOT" fetch --quiet --no-tags --depth=1 "$PR_REPOSITORY" "refs/pull/$PR/head" ||
    fail "could not fetch PR #$PR from $PR_REPOSITORY"
  git -C "$SOURCE_ROOT" checkout --quiet --detach FETCH_HEAD
}

# validate_source_checkout requires clean source files, records the commit, and derives a default version.
# A local helper absent from HEAD may be untracked or staged for its first commit.
# The existing Docker build helper cannot handle whitespace in the checkout path.
function validate_source_checkout() {
  local checkout_status
  [[ "$SOURCE_ROOT" != *[[:space:]]* ]] ||
    fail "the existing Docker build target requires a checkout path without whitespace"
  COMMIT="$(git -C "$SOURCE_ROOT" rev-parse --verify HEAD)"
  if [[ -z "$PR" ]] && ! git -C "$SOURCE_ROOT" cat-file -e "$COMMIT:operator/hack/build-artifacts.sh" 2>/dev/null; then
    checkout_status="$(git -C "$SOURCE_ROOT" status --porcelain --untracked-files=all -- . ':(exclude)operator/hack/build-artifacts.sh')"
  else
    checkout_status="$(git -C "$SOURCE_ROOT" status --porcelain --untracked-files=all)"
  fi
  [[ -z "$checkout_status" ]] || fail "the checkout has uncommitted changes; build a clean, reviewed revision"
  VERSION="${VERSION:-0.0.0-g${COMMIT:0:12}}"
}

# configure_output resolves a new output directory outside the checkout and temporary staging.
# Its parent must already exist, and existing output directories are never reused.
function configure_output() {
  local output_parent
  OUTPUT_DIR="${OUTPUT_DIR:-${TMPDIR:-/tmp}/grove-test-artifacts-${VERSION}}"
  [[ ! -e "$OUTPUT_DIR" && ! -L "$OUTPUT_DIR" ]] || fail "output directory already exists: $OUTPUT_DIR"
  output_parent="$(cd "$(dirname "$OUTPUT_DIR")" && pwd -P)" || fail "the output directory's parent must exist"
  OUTPUT_DIR="$output_parent/$(basename "$OUTPUT_DIR")"
  case "$OUTPUT_DIR/" in
    "$REPO_ROOT/"*|"$WORK_DIR/"*) fail "--output-dir must be outside the source checkout and temporary build directory" ;;
  esac
}

# package_chart exports the committed chart and CRDs into temporary staging and creates the Helm archive.
# Ignored local files are excluded so they cannot accidentally enter the package.
function package_chart() {
  local chart_dir="$WORK_DIR/operator/charts"
  # Export only committed chart files and CRD bundles, excluding ignored local files.
  git -C "$SOURCE_ROOT" archive "$COMMIT" operator/charts operator/api/core/crds scheduler/api/core/v1alpha1/crds |
    tar -x -C "$WORK_DIR"
  mkdir -p "$chart_dir/crds"
  cp "$WORK_DIR/operator/api/core/crds/"*.yaml "$chart_dir/crds/"
  cp "$WORK_DIR/scheduler/api/core/v1alpha1/crds/"*.yaml "$chart_dir/crds/"

  # Package first so Helm can reject an invalid version before the image build starts.
  helm package "$chart_dir" --version "$VERSION" --app-version "$VERSION" --destination "$WORK_DIR"
}

# write_helm_image_overrides points Helm at the built images and enables CRD upgrades.
# The init-container setting contains only its repository because Grove appends the build version.
function write_helm_image_overrides() {
  cat > "$WORK_DIR/values.yaml" <<EOF
image:
  repository: ${REGISTRY}/grove-operator
  tag: "${VERSION}"
crdInstaller:
  upgradeCRDs: true
  image:
    repository: ${REGISTRY}/grove-install-crds
    tag: "${VERSION}"
deployment:
  env:
    - name: GROVE_INIT_CONTAINER_IMAGE
      # Grove appends the version embedded in the operator binary.
      value: ${REGISTRY}/grove-initc
EOF
}

# build_images uses the existing Make target to build all three images for the requested platform.
# Images are loaded into Docker locally, not pushed to a registry.
function build_images() {
  docker info >/dev/null
  docker buildx version >/dev/null
  printf 'Building revision %s for %s with version %s\n' "$COMMIT" "$PLATFORM" "$VERSION"
  make --directory="$SOURCE_ROOT/operator" docker-build \
    REGISTRY="$REGISTRY" VERSION="$VERSION" PLATFORM="$PLATFORM" \
    DOCKER_BUILD_ADDITIONAL_ARGS=--load
}

# write_output verifies the local images and saves the chart, Helm overrides, image list, and build metadata.
# The output directory is created only after the build succeeds, without overwriting existing artifacts.
function write_output() {
  local image
  for image in "${IMAGES[@]}"; do
    docker image inspect "$REGISTRY/$image:$VERSION" >/dev/null
    printf '%s/%s:%s\n' "$REGISTRY" "$image" "$VERSION"
  done > "$WORK_DIR/images.txt"
  {
    if [[ -n "$PR" ]]; then
      printf 'repository=%s\npr=%s\n' "$PR_REPOSITORY" "$PR"
    fi
    printf 'commit=%s\nversion=%s\nplatform=%s\n' "$COMMIT" "$VERSION" "$PLATFORM"
  } > "$WORK_DIR/build-info.txt"

  # Refuse an existing directory, including one created while the build was running.
  mkdir "$OUTPUT_DIR"
  cp "$WORK_DIR/"*.tgz "$WORK_DIR/values.yaml" "$WORK_DIR/images.txt" "$WORK_DIR/build-info.txt" "$OUTPUT_DIR/"
}

# print_next_steps shows publishing and installation commands, plus upgrade and registry-authentication notes.
# It prints these commands for the user to review and does not execute them.
function print_next_steps() {
  local image
  printf '\nBuild complete\n\n'
  printf 'Artifacts\n  Directory:   %s\n' "$OUTPUT_DIR"
  printf '  Chart:       grove-charts-%s.tgz\n' "$VERSION"
  printf '%s\n' \
    '  Helm values: values.yaml' \
    '  Image list:  images.txt' \
    '  Build info:  build-info.txt'

  printf '\nNext steps\n==========\n'
  printf '\n↑ 1. Scan and publish the images\n\n'
  printf '  After scanning, run these commands through your registry process:\n\n'
  for image in "${IMAGES[@]}"; do
    printf '  docker push %q\n' "$REGISTRY/$image:$VERSION"
  done

  printf '\n◎ 2. Choose the target cluster\n\n'
  printf '%s\n' \
    '  Replace these placeholders with your release, namespace, and kubeconfig:' \
    '' \
    '  RELEASE="<release-name>"' \
    '  NAMESPACE="<namespace>"' \
    '  KUBECONFIG="/absolute/path/to/kubeconfig"'

  printf '\n→ 3. Install or upgrade Grove after publishing\n\n'
  printf '  ARTIFACT_DIR=%q\n\n' "$OUTPUT_DIR"
  printf '  helm upgrade --install "$RELEASE" \\\n'
  printf '    "$ARTIFACT_DIR/grove-charts-%s.tgz" \\\n' "$VERSION"
  printf '%s\n' \
    '    --namespace "$NAMESPACE" \' \
    '    --create-namespace \' \
    '    --kubeconfig "$KUBECONFIG" \' \
    '    --values "$ARTIFACT_DIR/values.yaml" \' \
    '    --wait'

  printf '\nNotes\n'
  printf '%s\n' \
    '  - For an upgrade, keep the existing release, namespace, and custom values.' \
    '  - Merge the generated image overrides with those values, including any' \
    '    additional deployment.env entries.' \
    '  - For a private registry, configure pull credentials for both operator' \
    '    and workload pods.'
  printf '\nNo images were pushed and no Kubernetes resources were changed.\n'
}

# cleanup removes temporary staging and any PR checkout, leaving output artifacts and local images intact.
function cleanup() {
  if [[ -n "$WORK_DIR" ]]; then
    rm -rf "$WORK_DIR"
  fi
}

# main runs the build-only workflow and registers cleanup before staging artifacts.
function main() {
  parse_flags "$@"
  validate_flags
  check_prerequisites
  WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/grove-build.XXXXXX")"
  trap cleanup EXIT
  WORK_DIR="$(cd "$WORK_DIR" && pwd -P)"
  prepare_source_checkout
  validate_source_checkout
  configure_output
  package_chart
  write_helm_image_overrides
  build_images
  write_output
  print_next_steps
}

main "$@"

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

readonly -a KUBECTL_ARGS=("$@")
readonly -a CRDS=(
  podcliques.grove.io
  podcliquescalinggroups.grove.io
  clustertopologybindings.grove.io
  podgangmaps.grove.io
  podcliquesets.grove.io
)
readonly -a RESOURCES=(
  podcliques.v1alpha1.grove.io
  podcliquescalinggroups.v1alpha1.grove.io
  clustertopologybindings.v1alpha1.grove.io
  podgangmaps.v1alpha1.grove.io
  podcliquesets.v1alpha1.grove.io
)

run_kubectl() {
  kubectl "${KUBECTL_ARGS[@]}" "$@"
}

crd_versions() {
  run_kubectl get crd "$1" -o jsonpath='{.spec.versions[*].name}'
}

echo "> Checking the Grove CRDs and the v1alpha1 API"
for crd in "${CRDS[@]}"; do
  versions="$(crd_versions "$crd")"
  case "$versions" in
    "v1alpha1 v1beta1" | "v1alpha1") ;;
    *)
      echo "Unexpected versions on $crd: $versions" >&2
      exit 1
      ;;
  esac
done
for resource in "${RESOURCES[@]}"; do
  run_kubectl get "$resource" --all-namespaces >/dev/null
done

echo "> Making v1alpha1 the storage version"
for crd in "${CRDS[@]}"; do
  if [[ "$(crd_versions "$crd")" == "v1alpha1 v1beta1" ]]; then
    run_kubectl patch crd "$crd" --type=json -p='[
      {"op":"replace","path":"/spec/versions/0/storage","value":true},
      {"op":"replace","path":"/spec/versions/1/storage","value":false}
    ]'
  fi
done

echo "> Rewriting Grove objects at v1alpha1"
for resource in "${RESOURCES[@]}"; do
  if [[ -n "$(run_kubectl get "$resource" --all-namespaces -o name)" ]]; then
    run_kubectl get "$resource" --all-namespaces -o json |
      run_kubectl replace -f - >/dev/null
  fi
done

echo "> Recording v1alpha1 as the only stored version"
for crd in "${CRDS[@]}"; do
  run_kubectl patch crd "$crd" \
    --subresource=status \
    --type=merge \
    -p='{"status":{"storedVersions":["v1alpha1"]}}'
done

echo "> Removing v1beta1"
for crd in "${CRDS[@]}"; do
  [[ "$(crd_versions "$crd")" == "v1alpha1" ]] && continue

  if [[ "$crd" == "podcliquesets.grove.io" ]]; then
    run_kubectl patch crd "$crd" --type=json -p='[
      {"op":"remove","path":"/spec/versions/1"},
      {"op":"add","path":"/spec/conversion","value":{"strategy":"None"}}
    ]'
  else
    run_kubectl patch crd "$crd" --type=json \
      -p='[{"op":"remove","path":"/spec/versions/1"}]'
  fi
done

echo "> The Grove CRDs serve and store only v1alpha1"
for crd in "${CRDS[@]}"; do
  run_kubectl get crd "$crd" \
    -o jsonpath='{.metadata.name}: {.spec.versions[?(@.storage==true)].name} {.status.storedVersions}{"\n"}'
done

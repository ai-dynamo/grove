// /*
// Copyright 2025 The Grove Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// */

package mnnvl

import (
	"fmt"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/internal/constants"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// groupStatus describes the MNNVL enrollment state derived from annotations.
type groupStatus int

const (
	groupAbsent    groupStatus = iota // annotation not present — inherit from parent
	groupWithdrawn                    // annotation set to "none" — explicitly not enrolled
	groupEnrolled                     // annotation set to a group name — enrolled
)

// ValidateMNNVLGroupName validates that the given string is a valid mnnvl-group
// annotation value. The value must be either "none" (opt-out) or a valid
// DNS-1123 label, since the group name becomes part of the ComputeDomain
// resource name.
func ValidateMNNVLGroupName(name string) error {
	if name == "" {
		return fmt.Errorf("mnnvl-group value must not be empty")
	}
	if name == AnnotationMNNVLGroupOptOut {
		return nil
	}
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		return fmt.Errorf("mnnvl-group value %q is not a valid DNS-1123 label: %s", name, strings.Join(errs, "; "))
	}
	return nil
}

// resolveGroupName extracts the MNNVL group status from a single annotation set.
//   - groupEnrolled:  mnnvl-group is set to a group name; group contains the name.
//   - groupWithdrawn: mnnvl-group is "none" — explicit opt-out; group is "".
//   - groupAbsent:    mnnvl-group is not present — inherit from parent; group is "".
func resolveGroupName(annotations map[string]string) (group string, status groupStatus) {
	val, exists := annotations[AnnotationMNNVLGroup]
	if !exists {
		return "", groupAbsent
	}
	if val == AnnotationMNNVLGroupOptOut {
		return "", groupWithdrawn
	}
	return val, groupEnrolled
}

// ResolveGroupNameHierarchically resolves the MNNVL group from multiple
// annotation layers, ordered from most specific to least specific
// (e.g., PCLQ → PCSG → PCS). The first layer where the annotation is
// present wins — even if it's "none" (withdrawn), which stops the walk.
func ResolveGroupNameHierarchically(annotationLayers ...map[string]string) (string, bool) {
	for _, annotations := range annotationLayers {
		group, status := resolveGroupName(annotations)
		if status != groupAbsent {
			return group, status == groupEnrolled
		}
	}
	return "", false
}

// GenerateRCTName creates the ResourceClaimTemplate name for a PCS replica.
// Format: {pcs-name}-{replica-index}-{group-name}.
func GenerateRCTName(pcsNameReplica apicommon.ResourceNameReplica, groupName string) string {
	return fmt.Sprintf("%s-%d-%s", pcsNameReplica.Name, pcsNameReplica.Replica, groupName)
}

// HasGPUInPodSpec checks if any container in the PodSpec requests GPU resources.
func HasGPUInPodSpec(podSpec *corev1.PodSpec) bool {
	if podSpec == nil {
		return false
	}
	return hasGPUInContainers(podSpec.Containers) || hasGPUInContainers(podSpec.InitContainers)
}

// hasGPUInContainers checks if any container in the slice requests GPU resources.
func hasGPUInContainers(containers []corev1.Container) bool {
	for i := range containers {
		if containerHasGPU(&containers[i]) {
			return true
		}
	}
	return false
}

// containerHasGPU checks if a single container requests GPU resources.
// TODO: This check is incomplete - it only looks at Resources.Limits and Resources.Requests.
// Pods can also require GPUs via resourceClaims (Dynamic Resource Allocation).
// This should be extended in a future PR to handle all GPU requirement patterns.
func containerHasGPU(container *corev1.Container) bool {
	if container == nil {
		return false
	}
	if quantity, exists := container.Resources.Limits[constants.GPUResourceName]; exists {
		if !quantity.IsZero() {
			return true
		}
	}
	if quantity, exists := container.Resources.Requests[constants.GPUResourceName]; exists {
		if !quantity.IsZero() {
			return true
		}
	}
	return false
}

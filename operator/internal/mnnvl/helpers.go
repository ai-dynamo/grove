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

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"

	corev1 "k8s.io/api/core/v1"
)

// IsAutoMNNVLEnabled checks if MNNVL is enabled via the grove.io/auto-mnnvl annotation.
func IsAutoMNNVLEnabled(annotations map[string]string) bool {
	if annotations == nil {
		return false
	}
	return annotations[AnnotationAutoMNNVL] == AnnotationAutoMNNVLEnabled
}

// GenerateRCTName creates the ResourceClaimTemplate name for a PCS replica.
// The RCT name matches the ComputeDomain name: {pcs-name}-{replica-index}
func GenerateRCTName(pcsNameReplica apicommon.ResourceNameReplica) string {
	return fmt.Sprintf("%s-%d", pcsNameReplica.Name, pcsNameReplica.Replica)
}

// hasGPURequirement checks if any container in any clique of the PCS requests nvidia.com/gpu.
func hasGPURequirement(pcs *grovecorev1alpha1.PodCliqueSet) bool {
	for _, clique := range pcs.Spec.Template.Cliques {
		if clique == nil {
			continue
		}
		if hasGPUInPodSpec(&clique.Spec.PodSpec) {
			return true
		}
	}
	return false
}

// hasGPUInPodSpec checks if any container in the PodSpec requests GPU resources.
func hasGPUInPodSpec(podSpec *corev1.PodSpec) bool {
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
	// Check limits
	if quantity, exists := container.Resources.Limits[constants.GPUResourceName]; exists {
		if !quantity.IsZero() {
			return true
		}
	}
	// Check requests
	if quantity, exists := container.Resources.Requests[constants.GPUResourceName]; exists {
		if !quantity.IsZero() {
			return true
		}
	}
	return false
}

// getAnnotationValue safely retrieves an annotation value from a PCS.
func getAnnotationValue(pcs *grovecorev1alpha1.PodCliqueSet, key string) (string, bool) {
	if pcs.Annotations == nil {
		return "", false
	}
	value, exists := pcs.Annotations[key]
	return value, exists
}

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
	apicommon "github.com/ai-dynamo/grove/operator/api/common"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
)

// InjectAutoMNNVLIntoPodSpec injects MNNVL resourceClaims into the PodSpec if it has GPU requirements.
// It adds a PodResourceClaim referencing the generated RCT name, and adds claim references to each
// container that requests GPU resources. The operation is idempotent.
// Should only be called when MNNVL is enabled (check IsAutoMNNVLEnabled first).
func InjectAutoMNNVLIntoPodSpec(logger logr.Logger, podSpec *corev1.PodSpec, pcsNameReplica apicommon.ResourceNameReplica) {
	if podSpec == nil {
		return
	}

	rctName := GenerateRCTName(pcsNameReplica)

	// Check if already injected (idempotent)
	for _, claim := range podSpec.ResourceClaims {
		if claim.Name == MNNVLClaimName {
			logger.V(1).Info("MNNVL resourceClaims already injected, skipping", "rctName", rctName)
			return
		}
	}

	// Add claim reference to each container that requests GPU (single pass)
	hasGPUContainer := false
	for i := range podSpec.Containers {
		if containerHasGPU(&podSpec.Containers[i]) {
			addClaimToContainer(&podSpec.Containers[i])
			hasGPUContainer = true
		}
	}
	for i := range podSpec.InitContainers {
		if containerHasGPU(&podSpec.InitContainers[i]) {
			addClaimToContainer(&podSpec.InitContainers[i])
			hasGPUContainer = true
		}
	}

	if !hasGPUContainer {
		logger.V(1).Info("No GPU containers found, skipping MNNVL resourceClaims injection")
		return
	}

	podSpec.ResourceClaims = append(podSpec.ResourceClaims, corev1.PodResourceClaim{
		Name:                      MNNVLClaimName,
		ResourceClaimTemplateName: &rctName,
	})

	logger.V(1).Info("Injected MNNVL resourceClaims", "rctName", rctName)
}

// addClaimToContainer adds the MNNVL claim reference to a container's resource claims.
func addClaimToContainer(container *corev1.Container) {
	// Check if already added
	for _, claim := range container.Resources.Claims {
		if claim.Name == MNNVLClaimName {
			return
		}
	}
	container.Resources.Claims = append(container.Resources.Claims, corev1.ResourceClaim{
		Name: MNNVLClaimName,
	})
}

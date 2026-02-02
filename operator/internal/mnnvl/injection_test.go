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
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/internal/constants"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestInjectMNNVLIntoPodSpec(t *testing.T) {
	tests := []struct {
		description                         string
		podSpec                             *corev1.PodSpec
		pcsNameReplica                      apicommon.ResourceNameReplica
		callTwice                           bool     // for idempotency testing
		expectedContainersWithClaims        []string // container names that should have claims
		expectedContainersWithoutClaims     []string // container names that should NOT have claims
		expectedInitContainersWithClaims    []string // init container names that should have claims
		expectedInitContainersWithoutClaims []string // init container names that should NOT have claims
		expectedRCTName                     string   // expected RCT name (empty = skip check)
	}{
		{
			description:    "nil PodSpec does not panic",
			podSpec:        nil,
			pcsNameReplica: apicommon.ResourceNameReplica{Name: "pcs", Replica: 0},
		},
		{
			description: "injects claims into PodSpec with GPU container",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "gpu-container",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								constants.GPUResourceName: resource.MustParse("8"),
							},
						},
					},
				},
			},
			pcsNameReplica:                  apicommon.ResourceNameReplica{Name: "my-pcs", Replica: 0},
			expectedContainersWithClaims:    []string{"gpu-container"},
			expectedContainersWithoutClaims: []string{},
			expectedRCTName:                 "my-pcs-0",
		},
		{
			description: "injects claims into init container with GPU",
			podSpec: &corev1.PodSpec{
				InitContainers: []corev1.Container{
					{
						Name: "init-gpu",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								constants.GPUResourceName: resource.MustParse("1"),
							},
						},
					},
				},
				Containers: []corev1.Container{
					{Name: "main"},
				},
			},
			pcsNameReplica:                      apicommon.ResourceNameReplica{Name: "my-pcs", Replica: 0},
			expectedContainersWithClaims:        []string{},
			expectedContainersWithoutClaims:     []string{"main"},
			expectedInitContainersWithClaims:    []string{"init-gpu"},
			expectedInitContainersWithoutClaims: []string{},
		},
		{
			description: "only GPU containers get claim reference",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "gpu-container",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								constants.GPUResourceName: resource.MustParse("8"),
							},
						},
					},
					{
						Name: "sidecar",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("1"),
							},
						},
					},
				},
			},
			pcsNameReplica:                  apicommon.ResourceNameReplica{Name: "my-pcs", Replica: 0},
			expectedContainersWithClaims:    []string{"gpu-container"},
			expectedContainersWithoutClaims: []string{"sidecar"},
		},
		{
			description: "idempotent - calling twice does not duplicate",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "gpu-container",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								constants.GPUResourceName: resource.MustParse("8"),
							},
						},
					},
				},
			},
			pcsNameReplica:                  apicommon.ResourceNameReplica{Name: "my-pcs", Replica: 0},
			callTwice:                       true,
			expectedContainersWithClaims:    []string{"gpu-container"},
			expectedContainersWithoutClaims: []string{},
		},
		{
			description: "multiple GPU containers all get claims",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "worker-1",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								constants.GPUResourceName: resource.MustParse("4"),
							},
						},
					},
					{
						Name: "worker-2",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								constants.GPUResourceName: resource.MustParse("4"),
							},
						},
					},
				},
			},
			pcsNameReplica:                  apicommon.ResourceNameReplica{Name: "my-pcs", Replica: 0},
			expectedContainersWithClaims:    []string{"worker-1", "worker-2"},
			expectedContainersWithoutClaims: []string{},
		},
		{
			description: "no injection when no GPU containers",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "cpu-only",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("1"),
							},
						},
					},
				},
			},
			pcsNameReplica:                  apicommon.ResourceNameReplica{Name: "my-pcs", Replica: 0},
			expectedContainersWithClaims:    []string{},
			expectedContainersWithoutClaims: []string{"cpu-only"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			// Call the function
			InjectMNNVLIntoPodSpec(logr.Discard(), tc.podSpec, tc.pcsNameReplica)

			// Call twice for idempotency test
			if tc.callTwice {
				InjectMNNVLIntoPodSpec(logr.Discard(), tc.podSpec, tc.pcsNameReplica)
			}

			// Skip remaining checks for nil PodSpec
			if tc.podSpec == nil {
				return
			}

			// Count MNNVL claims and verify RCT name
			mnnvlClaimCount := 0
			for _, claim := range tc.podSpec.ResourceClaims {
				if claim.Name == MNNVLClaimName {
					mnnvlClaimCount++
					// Verify RCT name if expected
					if tc.expectedRCTName != "" {
						require.NotNil(t, claim.ResourceClaimTemplateName)
						assert.Equal(t, tc.expectedRCTName, *claim.ResourceClaimTemplateName)
					}
				}
			}

			// Verify MNNVL claim count matches expectation
			expectInjection := len(tc.expectedContainersWithClaims) > 0 || len(tc.expectedInitContainersWithClaims) > 0
			if expectInjection {
				assert.Equal(t, 1, mnnvlClaimCount, "MNNVL claim should be added exactly once")
			} else {
				assert.Equal(t, 0, mnnvlClaimCount, "MNNVL claim should not be added")
			}

			// Verify container claims
			withClaims, withoutClaims := triageContainers(tc.podSpec.Containers)
			assert.ElementsMatch(t, tc.expectedContainersWithClaims, withClaims,
				"containers with claims should match expected")
			assert.ElementsMatch(t, tc.expectedContainersWithoutClaims, withoutClaims,
				"containers without claims should match expected")

			// Verify init container claims
			initWithClaims, initWithoutClaims := triageContainers(tc.podSpec.InitContainers)
			assert.ElementsMatch(t, tc.expectedInitContainersWithClaims, initWithClaims,
				"init containers with claims should match expected")
			assert.ElementsMatch(t, tc.expectedInitContainersWithoutClaims, initWithoutClaims,
				"init containers without claims should match expected")
		})
	}
}

// triageContainers separates containers into those with MNNVL claim and those without.
func triageContainers(containers []corev1.Container) (withMNNVLClaim, withoutMNNVLClaim []string) {
	for _, c := range containers {
		if hasMNNVLClaim(c.Resources.Claims) {
			withMNNVLClaim = append(withMNNVLClaim, c.Name)
		} else {
			withoutMNNVLClaim = append(withoutMNNVLClaim, c.Name)
		}
	}
	return withMNNVLClaim, withoutMNNVLClaim
}

// hasMNNVLClaim checks if the MNNVL claim exists in the claims list.
func hasMNNVLClaim(claims []corev1.ResourceClaim) bool {
	for _, claim := range claims {
		if claim.Name == MNNVLClaimName {
			return true
		}
	}
	return false
}

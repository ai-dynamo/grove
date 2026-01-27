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

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func Test_hasGPURequirement(t *testing.T) {
	tests := []struct {
		name     string
		pcs      *grovecorev1alpha1.PodCliqueSet
		expected bool
	}{
		{
			name:     "container with GPU limits",
			pcs:      createPCSWithGPU(nil),
			expected: true,
		},
		{
			name:     "container without GPU",
			pcs:      createPCSWithoutGPU(nil),
			expected: false,
		},
		{
			name:     "empty cliques",
			pcs:      &grovecorev1alpha1.PodCliqueSet{},
			expected: false,
		},
		{
			name: "GPU in init container",
			pcs: testutils.NewPodCliqueSetBuilder("test-pcs", "default", "").
				WithPodCliqueTemplateSpec(
					testutils.NewPodCliqueTemplateSpecBuilder("worker").
						WithInitContainer(corev1.Container{
							Name: "init",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									constants.GPUResourceName: resource.MustParse("1"),
								},
							},
						}).
						Build(),
				).
				Build(),
			expected: true,
		},
		{
			name: "GPU in requests not limits",
			pcs: testutils.NewPodCliqueSetBuilder("test-pcs", "default", "").
				WithPodCliqueTemplateSpec(
					testutils.NewPodCliqueTemplateSpecBuilder("worker").
						WithContainer(corev1.Container{
							Name: "train",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									constants.GPUResourceName: resource.MustParse("2"),
								},
							},
						}).
						Build(),
				).
				Build(),
			expected: true,
		},
		{
			name: "GPU with zero quantity",
			pcs: testutils.NewPodCliqueSetBuilder("test-pcs", "default", "").
				WithPodCliqueTemplateSpec(
					testutils.NewPodCliqueTemplateSpecBuilder("worker").
						WithContainer(corev1.Container{
							Name: "train",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									constants.GPUResourceName: resource.MustParse("0"),
								},
							},
						}).
						Build(),
				).
				Build(),
			expected: false,
		},
		{
			name: "multiple cliques - one with GPU",
			pcs: testutils.NewPodCliqueSetBuilder("test-pcs", "default", "").
				WithPodCliqueTemplateSpec(
					testutils.NewPodCliqueTemplateSpecBuilder("controller").
						WithContainer(testutils.NewContainer("ctrl", "busybox")).
						Build(),
				).
				WithPodCliqueTemplateSpec(
					testutils.NewPodCliqueTemplateSpecBuilder("worker").
						WithContainer(testutils.NewGPUContainer("train", "nvidia/cuda:latest", 8)).
						Build(),
				).
				Build(),
			expected: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := hasGPURequirement(test.pcs)
			assert.Equal(t, test.expected, result)
		})
	}
}

// createPCSWithGPU creates a PCS with GPU using the builder for tests in this package.
func createPCSWithGPU(annotations map[string]string) *grovecorev1alpha1.PodCliqueSet {
	return testutils.NewPodCliqueSetBuilder("test-pcs", "default", "").
		WithAnnotations(annotations).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder("worker").
				WithContainer(testutils.NewGPUContainer("train", "nvidia/cuda:latest", 8)).
				Build(),
		).
		Build()
}

// createPCSWithoutGPU creates a PCS without GPU using the builder for tests in this package.
func createPCSWithoutGPU(annotations map[string]string) *grovecorev1alpha1.PodCliqueSet {
	return testutils.NewPodCliqueSetBuilder("test-pcs", "default", "").
		WithAnnotations(annotations).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder("worker").
				WithContainer(testutils.NewContainer("app", "nginx:latest")).
				Build(),
		).
		Build()
}

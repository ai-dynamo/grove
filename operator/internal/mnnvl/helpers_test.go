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
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestIsAutoMNNVLEnabled(t *testing.T) {
	tests := []struct {
		description string
		annotations map[string]string
		expected    bool
	}{
		{
			description: "nil annotations returns false",
			annotations: nil,
			expected:    false,
		},
		{
			description: "empty annotations returns false",
			annotations: map[string]string{},
			expected:    false,
		},
		{
			description: "annotation set to enabled returns true",
			annotations: map[string]string{
				AnnotationAutoMNNVL: AnnotationAutoMNNVLEnabled,
			},
			expected: true,
		},
		{
			description: "annotation set to disabled returns false",
			annotations: map[string]string{
				AnnotationAutoMNNVL: AnnotationAutoMNNVLDisabled,
			},
			expected: false,
		},
		{
			description: "annotation set to invalid value returns false",
			annotations: map[string]string{
				AnnotationAutoMNNVL: "invalid",
			},
			expected: false,
		},
		{
			description: "other annotations without MNNVL returns false",
			annotations: map[string]string{
				"some-other-annotation": "value",
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			result := IsAutoMNNVLEnabled(tc.annotations)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestGenerateRCTName(t *testing.T) {
	tests := []struct {
		description    string
		pcsNameReplica apicommon.ResourceNameReplica
		expected       string
	}{
		{
			description:    "simple name with index 0",
			pcsNameReplica: apicommon.ResourceNameReplica{Name: "my-pcs", Replica: 0},
			expected:       "my-pcs-0",
		},
		{
			description:    "name with index 5",
			pcsNameReplica: apicommon.ResourceNameReplica{Name: "workload", Replica: 5},
			expected:       "workload-5",
		},
		{
			description:    "name with dashes",
			pcsNameReplica: apicommon.ResourceNameReplica{Name: "my-long-pcs-name", Replica: 10},
			expected:       "my-long-pcs-name-10",
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			result := GenerateRCTName(tc.pcsNameReplica)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestValidateMNNVLGroupName(t *testing.T) {
	tests := []struct {
		description string
		name        string
		expectErr   bool
	}{
		{
			description: "simple lowercase name",
			name:        "training",
			expectErr:   false,
		},
		{
			description: "name with dashes",
			name:        "my-workers",
			expectErr:   false,
		},
		{
			description: "single character",
			name:        "a",
			expectErr:   false,
		},
		{
			description: "alphanumeric with numbers",
			name:        "group1",
			expectErr:   false,
		},
		{
			description: "starts with number",
			name:        "1group",
			expectErr:   false,
		},
		{
			description: "max length (63 chars)",
			name:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			expectErr:   false,
		},
		{
			description: "empty string",
			name:        "",
			expectErr:   true,
		},
		{
			description: "exceeds 63 chars",
			name:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			expectErr:   true,
		},
		{
			description: "uppercase letters",
			name:        "Training",
			expectErr:   true,
		},
		{
			description: "contains underscore",
			name:        "my_group",
			expectErr:   true,
		},
		{
			description: "contains dot",
			name:        "my.group",
			expectErr:   true,
		},
		{
			description: "contains space",
			name:        "my group",
			expectErr:   true,
		},
		{
			description: "starts with dash",
			name:        "-workers",
			expectErr:   true,
		},
		{
			description: "ends with dash",
			name:        "workers-",
			expectErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			err := ValidateMNNVLGroupName(tc.name)
			if tc.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestDetectMNNVLConflict(t *testing.T) {
	tests := []struct {
		description string
		annotations map[string]string
		expectErr   bool
	}{
		{
			description: "nil annotations — no conflict",
			annotations: nil,
			expectErr:   false,
		},
		{
			description: "empty annotations — no conflict",
			annotations: map[string]string{},
			expectErr:   false,
		},
		{
			description: "auto-mnnvl enabled only — no conflict",
			annotations: map[string]string{
				AnnotationAutoMNNVL: AnnotationAutoMNNVLEnabled,
			},
			expectErr: false,
		},
		{
			description: "auto-mnnvl disabled only — no conflict",
			annotations: map[string]string{
				AnnotationAutoMNNVL: AnnotationAutoMNNVLDisabled,
			},
			expectErr: false,
		},
		{
			description: "mnnvl-group only — no conflict",
			annotations: map[string]string{
				AnnotationMNNVLGroup: "workers",
			},
			expectErr: false,
		},
		{
			description: "enabled + group — no conflict",
			annotations: map[string]string{
				AnnotationAutoMNNVL:  AnnotationAutoMNNVLEnabled,
				AnnotationMNNVLGroup: "workers",
			},
			expectErr: false,
		},
		{
			description: "disabled + group — conflict",
			annotations: map[string]string{
				AnnotationAutoMNNVL:  AnnotationAutoMNNVLDisabled,
				AnnotationMNNVLGroup: "workers",
			},
			expectErr: true,
		},
		{
			description: "disabled (uppercase) + group — conflict (case-insensitive)",
			annotations: map[string]string{
				AnnotationAutoMNNVL:  "Disabled",
				AnnotationMNNVLGroup: "training",
			},
			expectErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			err := DetectMNNVLConflict(tc.annotations)
			if tc.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

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

type cliqueAnnotation struct {
	name        string
	annotations map[string]string
}

// createPCSWithCliques creates a PCS with per-clique annotations.
func createPCSWithCliques(cliques []cliqueAnnotation) *grovecorev1alpha1.PodCliqueSet {
	builder := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "")
	for _, c := range cliques {
		builder.WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder(c.name).
				WithAnnotations(c.annotations).
				WithContainer(testutils.NewGPUContainer("train", "nvidia/cuda:latest", 8)).
				Build(),
		)
	}
	return builder.Build()
}

// createPCSWithPCSGConfigAnnotations creates a PCS with a single PCSG config carrying the given annotations.
func createPCSWithPCSGConfigAnnotations(annotations map[string]string) *grovecorev1alpha1.PodCliqueSet {
	builder := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "").
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder("worker").
				WithContainer(testutils.NewGPUContainer("train", "nvidia/cuda:latest", 8)).
				Build(),
		)
	builder.WithPodCliqueScalingGroupConfig(grovecorev1alpha1.PodCliqueScalingGroupConfig{
		Name:        "scaling-group-1",
		CliqueNames: []string{"worker"},
		Annotations: annotations,
	})
	return builder.Build()
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

func Test_hasGPUInPodSpec(t *testing.T) {
	tests := []struct {
		description string
		podSpec     *corev1.PodSpec
		expected    bool
	}{
		{
			description: "nil PodSpec returns false",
			podSpec:     nil,
			expected:    false,
		},
		{
			description: "empty PodSpec returns false",
			podSpec:     &corev1.PodSpec{},
			expected:    false,
		},
		{
			description: "container with GPU in limits returns true",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "gpu-container",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								constants.GPUResourceName: resource.MustParse("1"),
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			description: "container with GPU in requests returns true",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "gpu-container",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								constants.GPUResourceName: resource.MustParse("2"),
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			description: "init container with GPU returns true",
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
			},
			expected: true,
		},
		{
			description: "container without GPU returns false",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "cpu-container",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("1"),
							},
						},
					},
				},
			},
			expected: false,
		},
		{
			description: "container with zero GPU returns false",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "no-gpu",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								constants.GPUResourceName: resource.MustParse("0"),
							},
						},
					},
				},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			result := hasGPUInPodSpec(tc.podSpec)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func Test_containerHasGPU(t *testing.T) {
	tests := []struct {
		description string
		container   *corev1.Container
		expected    bool
	}{
		{
			description: "nil container returns false",
			container:   nil,
			expected:    false,
		},
		{
			description: "container with GPU in limits returns true",
			container: &corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						constants.GPUResourceName: resource.MustParse("1"),
					},
				},
			},
			expected: true,
		},
		{
			description: "container with GPU in requests returns true",
			container: &corev1.Container{
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						constants.GPUResourceName: resource.MustParse("1"),
					},
				},
			},
			expected: true,
		},
		{
			description: "container without GPU returns false",
			container: &corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
				},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			result := containerHasGPU(tc.container)
			assert.Equal(t, tc.expected, result)
		})
	}
}

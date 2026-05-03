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

func TestResolveGroupName(t *testing.T) {
	testCases := []struct {
		description    string
		annotations    map[string]string
		expectedGroup  string
		expectedStatus groupStatus
	}{
		{
			description:    "absent annotation — inherit from parent",
			annotations:    nil,
			expectedGroup:  "",
			expectedStatus: groupAbsent,
		},
		{
			description:    "empty annotations — inherit from parent",
			annotations:    map[string]string{},
			expectedGroup:  "",
			expectedStatus: groupAbsent,
		},
		{
			description:    "unrelated annotations only — inherit from parent",
			annotations:    map[string]string{"other": "value"},
			expectedGroup:  "",
			expectedStatus: groupAbsent,
		},
		{
			description:    "mnnvl-group set to training — enrolled",
			annotations:    map[string]string{AnnotationMNNVLGroup: "training"},
			expectedGroup:  "training",
			expectedStatus: groupEnrolled,
		},
		{
			description:    "mnnvl-group set to default — enrolled",
			annotations:    map[string]string{AnnotationMNNVLGroup: "default"},
			expectedGroup:  "default",
			expectedStatus: groupEnrolled,
		},
		{
			description:    "mnnvl-group set to none — withdrawn",
			annotations:    map[string]string{AnnotationMNNVLGroup: AnnotationMNNVLGroupOptOut},
			expectedGroup:  "",
			expectedStatus: groupWithdrawn,
		},
	}

	t.Parallel()
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()
			group, status := resolveGroupName(tc.annotations)
			assert.Equal(t, tc.expectedGroup, group)
			assert.Equal(t, tc.expectedStatus, status)
		})
	}
}

func TestResolveGroupNameHierarchically(t *testing.T) {
	tests := []struct {
		description   string
		layers        []map[string]string
		expectedGroup string
		expectedOk    bool
	}{
		{
			description:   "PCLQ has group — parent ignored",
			layers:        []map[string]string{{AnnotationMNNVLGroup: "pclq-group"}, {AnnotationMNNVLGroup: "parent-group"}},
			expectedGroup: "pclq-group",
			expectedOk:    true,
		},
		{
			description:   "PCLQ absent — falls back to parent group",
			layers:        []map[string]string{{}, {AnnotationMNNVLGroup: "parent-group"}},
			expectedGroup: "parent-group",
			expectedOk:    true,
		},
		{
			description: "PCLQ has none — parent group overridden, opt-out",
			layers:      []map[string]string{{AnnotationMNNVLGroup: AnnotationMNNVLGroupOptOut}, {AnnotationMNNVLGroup: "parent-group"}},
			expectedOk:  false,
		},
		{
			description: "both absent — not enrolled",
			layers:      []map[string]string{{}, {}},
			expectedOk:  false,
		},
		{
			description: "nil layers — not enrolled",
			layers:      nil,
			expectedOk:  false,
		},
		{
			description:   "PCLQ has group — nil parent is safe",
			layers:        []map[string]string{{AnnotationMNNVLGroup: "pclq-group"}, nil},
			expectedGroup: "pclq-group",
			expectedOk:    true,
		},
		{
			description: "PCLQ empty — nil parent is safe",
			layers:      []map[string]string{{}, nil},
			expectedOk:  false,
		},
	}

	t.Parallel()
	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()
			group, ok := ResolveGroupNameHierarchically(tc.layers...)
			assert.Equal(t, tc.expectedOk, ok)
			if ok {
				assert.Equal(t, tc.expectedGroup, group)
			}
		})
	}
}

func TestGenerateRCTName(t *testing.T) {
	tests := []struct {
		description    string
		pcsNameReplica apicommon.ResourceNameReplica
		groupName      string
		expected       string
	}{
		{
			description:    "default group index 0",
			pcsNameReplica: apicommon.ResourceNameReplica{Name: "my-pcs", Replica: 0},
			groupName:      "default",
			expected:       "my-pcs-0-default",
		},
		{
			description:    "default group index 5",
			pcsNameReplica: apicommon.ResourceNameReplica{Name: "workload", Replica: 5},
			groupName:      "default",
			expected:       "workload-5-default",
		},
		{
			description:    "default group with dashes",
			pcsNameReplica: apicommon.ResourceNameReplica{Name: "my-long-pcs-name", Replica: 10},
			groupName:      "default",
			expected:       "my-long-pcs-name-10-default",
		},
		{
			description:    "named group",
			pcsNameReplica: apicommon.ResourceNameReplica{Name: "my-pcs", Replica: 0},
			groupName:      "workers",
			expected:       "my-pcs-0-workers",
		},
		{
			description:    "named group higher replica",
			pcsNameReplica: apicommon.ResourceNameReplica{Name: "training", Replica: 3},
			groupName:      "encoders",
			expected:       "training-3-encoders",
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			result := GenerateRCTName(tc.pcsNameReplica, tc.groupName)
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
			description: "none is accepted (opt-out)",
			name:        "none",
			expectErr:   false,
		},
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

// createPCSWithNonGPUCliqueAnnotations creates a PCS with a single non-GPU clique
// carrying the given annotations.
func createPCSWithNonGPUCliqueAnnotations(cliqueAnnotations map[string]string) *grovecorev1alpha1.PodCliqueSet {
	return testutils.NewPodCliqueSetBuilder("test-pcs", "default", "").
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder("cpu-worker").
				WithAnnotations(cliqueAnnotations).
				WithContainer(testutils.NewContainer("app", "nginx:latest")).
				Build(),
		).
		Build()
}

// createPCSWithGPUCliqueAnnotations creates a PCS with a single GPU clique
// carrying the given annotations.
func createPCSWithGPUCliqueAnnotations(cliqueAnnotations map[string]string) *grovecorev1alpha1.PodCliqueSet {
	return testutils.NewPodCliqueSetBuilder("test-pcs", "default", "").
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder("gpu-worker").
				WithAnnotations(cliqueAnnotations).
				WithContainer(testutils.NewGPUContainer("train", "nvidia/cuda:latest", 8)).
				Build(),
		).
		Build()
}

func TestHasGPUInPodSpec(t *testing.T) {
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
			result := HasGPUInPodSpec(tc.podSpec)
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

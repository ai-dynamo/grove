// Copyright 2026 The Grove Authors.
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

package validation

import (
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
)

// createPCSWithGPU creates a PCS with GPU for validation tests.
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
	for _, clique := range cliques {
		builder.WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder(clique.name).
				WithAnnotations(clique.annotations).
				WithContainer(testutils.NewGPUContainer("train", "nvidia/cuda:latest", 8)).
				Build(),
		)
	}
	return builder.Build()
}

// createPCSWithPCSGConfigAnnotations creates a PCS with an annotated PCSG config.
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

// createPCSWithNonGPUCliqueAnnotations creates a PCS with an annotated non-GPU clique.
func createPCSWithNonGPUCliqueAnnotations(annotations map[string]string) *grovecorev1alpha1.PodCliqueSet {
	return testutils.NewPodCliqueSetBuilder("test-pcs", "default", "").
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder("cpu-worker").
				WithAnnotations(annotations).
				WithContainer(testutils.NewContainer("app", "nginx:latest")).
				Build(),
		).
		Build()
}

// createPCSWithGPUCliqueAnnotations creates a PCS with an annotated GPU clique.
func createPCSWithGPUCliqueAnnotations(annotations map[string]string) *grovecorev1alpha1.PodCliqueSet {
	return testutils.NewPodCliqueSetBuilder("test-pcs", "default", "").
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder("gpu-worker").
				WithAnnotations(annotations).
				WithContainer(testutils.NewGPUContainer("train", "nvidia/cuda:latest", 8)).
				Build(),
		).
		Build()
}

func TestValidatePCSOnCreate_Metadata(t *testing.T) {
	tests := []struct {
		description      string
		pcs              *grovecorev1alpha1.PodCliqueSet
		autoMNNVLEnabled bool
		expectError      bool
		errorContains    string
	}{
		{
			description:      "valid mnnvl-group + feature enabled -> no error",
			pcs:              createPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description:      "valid mnnvl-group + feature disabled -> error",
			pcs:              createPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}),
			autoMNNVLEnabled: false,
			expectError:      true,
			errorContains:    "MNNVL is not enabled",
		},
		{
			description:      "mnnvl-group 'none' + feature enabled -> no error",
			pcs:              createPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: mnnvl.AnnotationMNNVLGroupOptOut}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description:      "mnnvl-group 'none' + feature disabled -> no error",
			pcs:              createPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: mnnvl.AnnotationMNNVLGroupOptOut}),
			autoMNNVLEnabled: false,
			expectError:      false,
		},
		{
			description:      "no annotation + feature disabled -> no error",
			pcs:              createPCSWithGPU(nil),
			autoMNNVLEnabled: false,
			expectError:      false,
		},
		{
			description:      "no annotation + feature enabled -> no error",
			pcs:              createPCSWithGPU(nil),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description:      "nil annotations map -> no error",
			pcs:              &grovecorev1alpha1.PodCliqueSet{},
			autoMNNVLEnabled: false,
			expectError:      false,
		},
		{
			description:      "invalid mnnvl-group value (uppercase) -> error",
			pcs:              createPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: "Workers"}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "not a valid DNS-1123 label",
		},
		{
			description:      "invalid mnnvl-group value (empty) -> error",
			pcs:              createPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: ""}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "must not be empty",
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			errs := validateMNNVLOnCreate(test.pcs, test.autoMNNVLEnabled)

			if test.expectError {
				assert.NotEmpty(t, errs, "expected validation errors")
				assert.Contains(t, errs.ToAggregate().Error(), test.errorContains)
			} else {
				assert.Empty(t, errs, "expected no validation errors")
			}
		})
	}
}

func TestValidatePCSOnUpdate_Metadata(t *testing.T) {
	tests := []struct {
		description string
		oldPCS      *grovecorev1alpha1.PodCliqueSet
		newPCS      *grovecorev1alpha1.PodCliqueSet
		expectError bool
		errorMsg    string
	}{
		{
			description: "no annotation on both -> no error",
			oldPCS:      createPCSWithGPU(nil),
			newPCS:      createPCSWithGPU(nil),
			expectError: false,
		},
		{
			description: "mnnvl-group unchanged -> no error",
			oldPCS:      createPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}),
			newPCS:      createPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}),
			expectError: false,
		},
		{
			description: "mnnvl-group added -> error",
			oldPCS:      createPCSWithGPU(nil),
			newPCS:      createPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}),
			expectError: true,
			errorMsg:    "cannot be added",
		},
		{
			description: "mnnvl-group removed -> error",
			oldPCS:      createPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}),
			newPCS:      createPCSWithGPU(nil),
			expectError: true,
			errorMsg:    "cannot be removed",
		},
		{
			description: "mnnvl-group changed -> error",
			oldPCS:      createPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}),
			newPCS:      createPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}),
			expectError: true,
			errorMsg:    "immutable",
		},
		{
			description: "other annotations changed but mnnvl-group unchanged -> no error",
			oldPCS:      createPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers", "other": "old"}),
			newPCS:      createPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers", "other": "new"}),
			expectError: false,
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			errs := validateMNNVLOnUpdate(test.oldPCS, test.newPCS)

			if test.expectError {
				assert.NotEmpty(t, errs, "expected validation errors")
				assert.Contains(t, errs.ToAggregate().Error(), test.errorMsg)
			} else {
				assert.Empty(t, errs, "expected no validation errors")
			}
		})
	}
}

func TestValidatePCSOnCreate_Spec(t *testing.T) {
	tests := []struct {
		description      string
		pcs              *grovecorev1alpha1.PodCliqueSet
		autoMNNVLEnabled bool
		expectError      bool
		errorContains    string
	}{
		{
			description: "valid mnnvl-group on clique template + feature enabled -> no error",
			pcs: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}},
			}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description: "invalid mnnvl-group on clique template -> error",
			pcs: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "INVALID"}},
			}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "not a valid DNS-1123 label",
		},
		{
			description: "mnnvl-group 'none' on clique template -> no error",
			pcs: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: mnnvl.AnnotationMNNVLGroupOptOut}},
			}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description: "mnnvl-group on clique template + feature disabled -> error",
			pcs: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}},
			}),
			autoMNNVLEnabled: false,
			expectError:      true,
			errorContains:    "MNNVL is not enabled",
		},
		{
			description: "clique with empty mnnvl-group -> error",
			pcs: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: ""}},
			}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "must not be empty",
		},
		{
			description: "multiple cliques, one valid one invalid -> error",
			pcs: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}},
				{name: "encoders", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "-bad"}},
			}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "not a valid DNS-1123 label",
		},
		{
			description: "multiple cliques all valid -> no error",
			pcs: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}},
				{name: "encoders", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "encoding"}},
			}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description: "clique without annotations -> no error",
			pcs: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: nil},
			}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			errs := validateMNNVLOnCreate(test.pcs, test.autoMNNVLEnabled)

			if test.expectError {
				assert.NotEmpty(t, errs, "expected validation errors")
				assert.Contains(t, errs.ToAggregate().Error(), test.errorContains)
			} else {
				assert.Empty(t, errs, "expected no validation errors")
			}
		})
	}
}

func TestValidatePCSOnCreate_PCSGConfig(t *testing.T) {
	tests := []struct {
		description      string
		pcs              *grovecorev1alpha1.PodCliqueSet
		autoMNNVLEnabled bool
		expectError      bool
		errorContains    string
	}{
		{
			description:      "valid mnnvl-group on PCSG config + feature enabled -> no error",
			pcs:              createPCSWithPCSGConfigAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description:      "invalid mnnvl-group on PCSG config -> error",
			pcs:              createPCSWithPCSGConfigAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "INVALID"}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "not a valid DNS-1123 label",
		},
		{
			description:      "mnnvl-group on PCSG config + feature disabled -> error",
			pcs:              createPCSWithPCSGConfigAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}),
			autoMNNVLEnabled: false,
			expectError:      true,
			errorContains:    "not enabled",
		},
		{
			description:      "mnnvl-group 'none' on PCSG config + feature enabled -> no error",
			pcs:              createPCSWithPCSGConfigAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: mnnvl.AnnotationMNNVLGroupOptOut}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description:      "PCSG config without annotations -> no error",
			pcs:              createPCSWithPCSGConfigAnnotations(nil),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			errs := validateMNNVLOnCreate(test.pcs, test.autoMNNVLEnabled)

			if test.expectError {
				assert.NotEmpty(t, errs, "expected validation errors")
				assert.Contains(t, errs.ToAggregate().Error(), test.errorContains)
			} else {
				assert.Empty(t, errs, "expected no validation errors")
			}
		})
	}
}

func TestValidatePCSOnUpdate_PCSGConfig(t *testing.T) {
	tests := []struct {
		description string
		oldPCS      *grovecorev1alpha1.PodCliqueSet
		newPCS      *grovecorev1alpha1.PodCliqueSet
		expectError bool
		errorMsg    string
	}{
		{
			description: "PCSG config mnnvl-group unchanged -> no error",
			oldPCS:      createPCSWithPCSGConfigAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}),
			newPCS:      createPCSWithPCSGConfigAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}),
			expectError: false,
		},
		{
			description: "PCSG config mnnvl-group added -> error",
			oldPCS:      createPCSWithPCSGConfigAnnotations(nil),
			newPCS:      createPCSWithPCSGConfigAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}),
			expectError: true,
			errorMsg:    "cannot be added",
		},
		{
			description: "PCSG config mnnvl-group changed -> error",
			oldPCS:      createPCSWithPCSGConfigAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}),
			newPCS:      createPCSWithPCSGConfigAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "inference"}),
			expectError: true,
			errorMsg:    "immutable",
		},
		{
			description: "PCSG config mnnvl-group removed -> error",
			oldPCS:      createPCSWithPCSGConfigAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}),
			newPCS:      createPCSWithPCSGConfigAnnotations(nil),
			expectError: true,
			errorMsg:    "cannot be removed",
		},
		{
			description: "PCSG config without annotations on both -> no error",
			oldPCS:      createPCSWithPCSGConfigAnnotations(nil),
			newPCS:      createPCSWithPCSGConfigAnnotations(nil),
			expectError: false,
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			errs := validateMNNVLOnUpdate(test.oldPCS, test.newPCS)

			if test.expectError {
				assert.NotEmpty(t, errs, "expected validation errors")
				assert.Contains(t, errs.ToAggregate().Error(), test.errorMsg)
			} else {
				assert.Empty(t, errs, "expected no validation errors")
			}
		})
	}
}

func TestValidatePCSOnUpdate_Spec(t *testing.T) {
	tests := []struct {
		description string
		oldPCS      *grovecorev1alpha1.PodCliqueSet
		newPCS      *grovecorev1alpha1.PodCliqueSet
		expectError bool
		errorMsg    string
	}{
		{
			description: "clique-level mnnvl-group unchanged -> no error",
			oldPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}},
			}),
			newPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}},
			}),
			expectError: false,
		},
		{
			description: "clique-level mnnvl-group added -> error",
			oldPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: nil},
			}),
			newPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}},
			}),
			expectError: true,
			errorMsg:    "cannot be added",
		},
		{
			description: "clique-level mnnvl-group removed -> error",
			oldPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}},
			}),
			newPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: nil},
			}),
			expectError: true,
			errorMsg:    "cannot be removed",
		},
		{
			description: "clique-level mnnvl-group changed -> error",
			oldPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}},
			}),
			newPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "inference"}},
			}),
			expectError: true,
			errorMsg:    "immutable",
		},
		{
			description: "clique without annotations on both -> no error",
			oldPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: nil},
			}),
			newPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: nil},
			}),
			expectError: false,
		},
		{
			description: "two cliques reordered (AnyOrder): MNNVL annotations unchanged per clique name -> no error",
			oldPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}},
				{name: "encoders", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "encoding"}},
			}),
			newPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "encoders", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "encoding"}},
				{name: "workers", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}},
			}),
			expectError: false,
		},
		{
			description: "two cliques reordered and mnnvl-group changed on one clique (by name) -> error",
			oldPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}},
				{name: "encoders", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "encoding"}},
			}),
			newPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "encoders", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "encoding"}},
				{name: "workers", annotations: map[string]string{mnnvl.AnnotationMNNVLGroup: "inference"}},
			}),
			expectError: true,
			errorMsg:    "immutable",
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			errs := validateMNNVLOnUpdate(test.oldPCS, test.newPCS)

			if test.expectError {
				assert.NotEmpty(t, errs, "expected validation errors")
				assert.Contains(t, errs.ToAggregate().Error(), test.errorMsg)
			} else {
				assert.Empty(t, errs, "expected no validation errors")
			}
		})
	}
}

// TestValidatePCSOnCreate_NonGPUCliqueWithMNNVL verifies that MNNVL annotations
// on non-GPU cliques are accepted (silently ignored at injection time).
func TestValidatePCSOnCreate_NonGPUCliqueWithMNNVL(t *testing.T) {
	tests := []struct {
		description      string
		pcs              *grovecorev1alpha1.PodCliqueSet
		autoMNNVLEnabled bool
	}{
		{
			description:      "non-GPU clique with mnnvl-group -> accepted (silently skipped at injection)",
			pcs:              createPCSWithNonGPUCliqueAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}),
			autoMNNVLEnabled: true,
		},
		{
			description:      "GPU clique with mnnvl-group -> accepted",
			pcs:              createPCSWithGPUCliqueAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}),
			autoMNNVLEnabled: true,
		},
		{
			description:      "non-GPU clique without MNNVL annotations -> accepted",
			pcs:              createPCSWithNonGPUCliqueAnnotations(nil),
			autoMNNVLEnabled: true,
		},
		{
			description:      "non-GPU clique with mnnvl-group 'none' -> accepted",
			pcs:              createPCSWithNonGPUCliqueAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: mnnvl.AnnotationMNNVLGroupOptOut}),
			autoMNNVLEnabled: true,
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			errs := validateMNNVLOnCreate(test.pcs, test.autoMNNVLEnabled)
			assert.Empty(t, errs, "MNNVL annotations on non-GPU cliques should be accepted")
		})
	}
}

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

package mnnvl

import (
	"strings"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

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
			pcs:              createPCSWithGPU(map[string]string{AnnotationMNNVLGroup: "workers"}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description:      "valid mnnvl-group + feature disabled -> error",
			pcs:              createPCSWithGPU(map[string]string{AnnotationMNNVLGroup: "workers"}),
			autoMNNVLEnabled: false,
			expectError:      true,
			errorContains:    "MNNVL is not enabled",
		},
		{
			description:      "mnnvl-group 'none' + feature enabled -> no error",
			pcs:              createPCSWithGPU(map[string]string{AnnotationMNNVLGroup: AnnotationMNNVLGroupOptOut}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description:      "mnnvl-group 'none' + feature disabled -> no error",
			pcs:              createPCSWithGPU(map[string]string{AnnotationMNNVLGroup: AnnotationMNNVLGroupOptOut}),
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
			pcs:              createPCSWithGPU(map[string]string{AnnotationMNNVLGroup: "Workers"}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "not a valid DNS-1123 label",
		},
		{
			description:      "invalid mnnvl-group value (empty) -> error",
			pcs:              createPCSWithGPU(map[string]string{AnnotationMNNVLGroup: ""}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "must not be empty",
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			errs := ValidatePCSOnCreate(test.pcs, test.autoMNNVLEnabled)

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
			oldPCS:      createPCSWithGPU(map[string]string{AnnotationMNNVLGroup: "workers"}),
			newPCS:      createPCSWithGPU(map[string]string{AnnotationMNNVLGroup: "workers"}),
			expectError: false,
		},
		{
			description: "mnnvl-group added -> error",
			oldPCS:      createPCSWithGPU(nil),
			newPCS:      createPCSWithGPU(map[string]string{AnnotationMNNVLGroup: "workers"}),
			expectError: true,
			errorMsg:    "cannot be added",
		},
		{
			description: "mnnvl-group removed -> error",
			oldPCS:      createPCSWithGPU(map[string]string{AnnotationMNNVLGroup: "workers"}),
			newPCS:      createPCSWithGPU(nil),
			expectError: true,
			errorMsg:    "cannot be removed",
		},
		{
			description: "mnnvl-group changed -> error",
			oldPCS:      createPCSWithGPU(map[string]string{AnnotationMNNVLGroup: "workers"}),
			newPCS:      createPCSWithGPU(map[string]string{AnnotationMNNVLGroup: "training"}),
			expectError: true,
			errorMsg:    "immutable",
		},
		{
			description: "other annotations changed but mnnvl-group unchanged -> no error",
			oldPCS:      createPCSWithGPU(map[string]string{AnnotationMNNVLGroup: "workers", "other": "old"}),
			newPCS:      createPCSWithGPU(map[string]string{AnnotationMNNVLGroup: "workers", "other": "new"}),
			expectError: false,
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			errs := ValidatePCSOnUpdate(test.oldPCS, test.newPCS, true)

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
				{name: "workers", annotations: map[string]string{AnnotationMNNVLGroup: "workers"}},
			}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description: "invalid mnnvl-group on clique template -> error",
			pcs: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{AnnotationMNNVLGroup: "INVALID"}},
			}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "not a valid DNS-1123 label",
		},
		{
			description: "mnnvl-group 'none' on clique template -> no error",
			pcs: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{AnnotationMNNVLGroup: AnnotationMNNVLGroupOptOut}},
			}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description: "mnnvl-group on clique template + feature disabled -> error",
			pcs: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{AnnotationMNNVLGroup: "workers"}},
			}),
			autoMNNVLEnabled: false,
			expectError:      true,
			errorContains:    "MNNVL is not enabled",
		},
		{
			description: "clique with empty mnnvl-group -> error",
			pcs: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{AnnotationMNNVLGroup: ""}},
			}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "must not be empty",
		},
		{
			description: "multiple cliques, one valid one invalid -> error",
			pcs: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{AnnotationMNNVLGroup: "workers"}},
				{name: "encoders", annotations: map[string]string{AnnotationMNNVLGroup: "-bad"}},
			}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "not a valid DNS-1123 label",
		},
		{
			description: "multiple cliques all valid -> no error",
			pcs: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{AnnotationMNNVLGroup: "training"}},
				{name: "encoders", annotations: map[string]string{AnnotationMNNVLGroup: "encoding"}},
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
			errs := ValidatePCSOnCreate(test.pcs, test.autoMNNVLEnabled)

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
			pcs:              createPCSWithPCSGConfigAnnotations(map[string]string{AnnotationMNNVLGroup: "training"}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description:      "invalid mnnvl-group on PCSG config -> error",
			pcs:              createPCSWithPCSGConfigAnnotations(map[string]string{AnnotationMNNVLGroup: "INVALID"}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "not a valid DNS-1123 label",
		},
		{
			description:      "mnnvl-group on PCSG config + feature disabled -> error",
			pcs:              createPCSWithPCSGConfigAnnotations(map[string]string{AnnotationMNNVLGroup: "workers"}),
			autoMNNVLEnabled: false,
			expectError:      true,
			errorContains:    "not enabled",
		},
		{
			description:      "mnnvl-group 'none' on PCSG config + feature enabled -> no error",
			pcs:              createPCSWithPCSGConfigAnnotations(map[string]string{AnnotationMNNVLGroup: AnnotationMNNVLGroupOptOut}),
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
			errs := ValidatePCSOnCreate(test.pcs, test.autoMNNVLEnabled)

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
			oldPCS:      createPCSWithPCSGConfigAnnotations(map[string]string{AnnotationMNNVLGroup: "training"}),
			newPCS:      createPCSWithPCSGConfigAnnotations(map[string]string{AnnotationMNNVLGroup: "training"}),
			expectError: false,
		},
		{
			description: "PCSG config mnnvl-group added -> error",
			oldPCS:      createPCSWithPCSGConfigAnnotations(nil),
			newPCS:      createPCSWithPCSGConfigAnnotations(map[string]string{AnnotationMNNVLGroup: "training"}),
			expectError: true,
			errorMsg:    "cannot be added",
		},
		{
			description: "PCSG config mnnvl-group changed -> error",
			oldPCS:      createPCSWithPCSGConfigAnnotations(map[string]string{AnnotationMNNVLGroup: "training"}),
			newPCS:      createPCSWithPCSGConfigAnnotations(map[string]string{AnnotationMNNVLGroup: "inference"}),
			expectError: true,
			errorMsg:    "immutable",
		},
		{
			description: "PCSG config mnnvl-group removed -> error",
			oldPCS:      createPCSWithPCSGConfigAnnotations(map[string]string{AnnotationMNNVLGroup: "training"}),
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
			errs := ValidatePCSOnUpdate(test.oldPCS, test.newPCS, true)

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
				{name: "workers", annotations: map[string]string{AnnotationMNNVLGroup: "training"}},
			}),
			newPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{AnnotationMNNVLGroup: "training"}},
			}),
			expectError: false,
		},
		{
			description: "clique-level mnnvl-group added -> error",
			oldPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: nil},
			}),
			newPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{AnnotationMNNVLGroup: "training"}},
			}),
			expectError: true,
			errorMsg:    "cannot be added",
		},
		{
			description: "clique-level mnnvl-group removed -> error",
			oldPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{AnnotationMNNVLGroup: "training"}},
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
				{name: "workers", annotations: map[string]string{AnnotationMNNVLGroup: "training"}},
			}),
			newPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{AnnotationMNNVLGroup: "inference"}},
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
				{name: "workers", annotations: map[string]string{AnnotationMNNVLGroup: "training"}},
				{name: "encoders", annotations: map[string]string{AnnotationMNNVLGroup: "encoding"}},
			}),
			newPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "encoders", annotations: map[string]string{AnnotationMNNVLGroup: "encoding"}},
				{name: "workers", annotations: map[string]string{AnnotationMNNVLGroup: "training"}},
			}),
			expectError: false,
		},
		{
			description: "two cliques reordered and mnnvl-group changed on one clique (by name) -> error",
			oldPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{AnnotationMNNVLGroup: "training"}},
				{name: "encoders", annotations: map[string]string{AnnotationMNNVLGroup: "encoding"}},
			}),
			newPCS: createPCSWithCliques([]cliqueAnnotation{
				{name: "encoders", annotations: map[string]string{AnnotationMNNVLGroup: "encoding"}},
				{name: "workers", annotations: map[string]string{AnnotationMNNVLGroup: "inference"}},
			}),
			expectError: true,
			errorMsg:    "immutable",
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			errs := ValidatePCSOnUpdate(test.oldPCS, test.newPCS, true)

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
			pcs:              createPCSWithNonGPUCliqueAnnotations(map[string]string{AnnotationMNNVLGroup: "workers"}),
			autoMNNVLEnabled: true,
		},
		{
			description:      "GPU clique with mnnvl-group -> accepted",
			pcs:              createPCSWithGPUCliqueAnnotations(map[string]string{AnnotationMNNVLGroup: "workers"}),
			autoMNNVLEnabled: true,
		},
		{
			description:      "non-GPU clique without MNNVL annotations -> accepted",
			pcs:              createPCSWithNonGPUCliqueAnnotations(nil),
			autoMNNVLEnabled: true,
		},
		{
			description:      "non-GPU clique with mnnvl-group 'none' -> accepted",
			pcs:              createPCSWithNonGPUCliqueAnnotations(map[string]string{AnnotationMNNVLGroup: AnnotationMNNVLGroupOptOut}),
			autoMNNVLEnabled: true,
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			errs := ValidatePCSOnCreate(test.pcs, test.autoMNNVLEnabled)
			assert.Empty(t, errs, "MNNVL annotations on non-GPU cliques should be accepted")
		})
	}
}

func TestValidatePCSOnCreate_ComputeDomainName(t *testing.T) {
	tests := []struct {
		description        string
		pcs                *grovecorev1alpha1.PodCliqueSet
		expectedNameLength int
		expectError        bool
	}{
		{
			description:        "63-character generated label value is accepted",
			pcs:                createPCSForComputeDomainNameValidation(strings.Repeat("p", 55), "group", 10, true),
			expectedNameLength: 63,
			expectError:        false,
		},
		{
			description:        "64-character generated label value is rejected",
			pcs:                createPCSForComputeDomainNameValidation(strings.Repeat("p", 55), "group", 11, true),
			expectedNameLength: 64,
			expectError:        true,
		},
		{
			description: "non-GPU clique does not create a ComputeDomain",
			pcs:         createPCSForComputeDomainNameValidation(strings.Repeat("p", 56), "group", 1, false),
			expectError: false,
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			if test.expectedNameLength > 0 {
				checks := buildGeneratedComputeDomainNameChecks(test.pcs)
				assert.Len(t, checks, 1)
				assert.Len(t, checks[0].generatedName, test.expectedNameLength)
			}
			errs := ValidatePCSOnCreate(test.pcs, true)

			if test.expectError {
				assert.Len(t, errs, 1)
				assert.Equal(t, "metadata.name", errs[0].Field)
				assert.Contains(t, errs[0].Detail, "generated ComputeDomain name")
				assert.Contains(t, errs[0].Detail, "app.kubernetes.io/name")
			} else {
				assert.Empty(t, errs)
			}
		})
	}
}

func TestValidatePCSOnUpdate_ComputeDomainName(t *testing.T) {
	tests := []struct {
		description      string
		oldPCS           *grovecorev1alpha1.PodCliqueSet
		newPCS           *grovecorev1alpha1.PodCliqueSet
		autoMNNVLEnabled bool
		expectedField    string
	}{
		{
			description:      "scale-out crossing the label length boundary is rejected",
			oldPCS:           createPCSForComputeDomainNameValidation(strings.Repeat("p", 55), "group", 10, true),
			newPCS:           createPCSForComputeDomainNameValidation(strings.Repeat("p", 55), "group", 11, true),
			autoMNNVLEnabled: true,
			expectedField:    "spec.replicas",
		},
		{
			description:      "unchanged legacy invalid name is accepted",
			oldPCS:           createPCSForComputeDomainNameValidation(strings.Repeat("p", 56), "group", 1, true),
			newPCS:           createPCSForComputeDomainNameValidation(strings.Repeat("p", 56), "group", 1, true),
			autoMNNVLEnabled: true,
		},
		{
			description:      "legacy invalid name can scale in",
			oldPCS:           createPCSForComputeDomainNameValidation(strings.Repeat("p", 55), "group", 11, true),
			newPCS:           createPCSForComputeDomainNameValidation(strings.Repeat("p", 55), "group", 10, true),
			autoMNNVLEnabled: true,
		},
		{
			description:      "newly effective invalid group is rejected",
			oldPCS:           createPCSForComputeDomainNameValidation(strings.Repeat("p", 56), "group", 1, false),
			newPCS:           createPCSForComputeDomainNameValidation(strings.Repeat("p", 56), "group", 1, true),
			autoMNNVLEnabled: true,
			expectedField:    "metadata.name",
		},
		{
			description:      "generated name validation is skipped when Auto-MNNVL is disabled",
			oldPCS:           createPCSForComputeDomainNameValidation(strings.Repeat("p", 55), "group", 10, true),
			newPCS:           createPCSForComputeDomainNameValidation(strings.Repeat("p", 55), "group", 11, true),
			autoMNNVLEnabled: false,
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			errs := ValidatePCSOnUpdate(test.oldPCS, test.newPCS, test.autoMNNVLEnabled)

			if test.expectedField == "" {
				assert.Empty(t, errs)
				return
			}
			assert.Len(t, errs, 1)
			assert.Equal(t, test.expectedField, errs[0].Field)
			assert.Contains(t, errs[0].Detail, "generated ComputeDomain name")
		})
	}
}

func createPCSForComputeDomainNameValidation(
	name, groupName string,
	replicas int32,
	withGPU bool,
) *grovecorev1alpha1.PodCliqueSet {
	pcs := createPCSWithGPU(map[string]string{AnnotationMNNVLGroup: groupName})
	pcs.Name = name
	pcs.Spec.Replicas = replicas
	if !withGPU {
		pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Resources = corev1.ResourceRequirements{}
	}
	return pcs
}

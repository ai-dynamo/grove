// /*
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
// */

package mnnvl

import (
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
)

func TestMutateAutoMNNVL(t *testing.T) {
	tests := []struct {
		description        string
		pcs                *grovecorev1alpha1.PodCliqueSet
		autoMNNVLEnabled   bool
		expectedAnnotation string
	}{
		{
			description:        "feature enabled + GPU + no annotation -> add enabled",
			pcs:                createPCSWithGPU(nil),
			autoMNNVLEnabled:   true,
			expectedAnnotation: AnnotationAutoMNNVLEnabled,
		},
		{
			description:        "feature enabled + GPU + existing disabled -> no change",
			pcs:                createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLDisabled}),
			autoMNNVLEnabled:   true,
			expectedAnnotation: AnnotationAutoMNNVLDisabled,
		},
		{
			description:        "feature enabled + GPU + existing enabled -> no change",
			pcs:                createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLEnabled}),
			autoMNNVLEnabled:   true,
			expectedAnnotation: AnnotationAutoMNNVLEnabled,
		},
		{
			description:        "feature enabled + no GPU -> no annotation",
			pcs:                createPCSWithoutGPU(nil),
			autoMNNVLEnabled:   true,
			expectedAnnotation: "",
		},
		{
			description:        "feature disabled + GPU -> no annotation",
			pcs:                createPCSWithGPU(nil),
			autoMNNVLEnabled:   false,
			expectedAnnotation: "",
		},
		{
			description:        "feature disabled + no GPU -> no annotation",
			pcs:                createPCSWithoutGPU(nil),
			autoMNNVLEnabled:   false,
			expectedAnnotation: "",
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			MutateAutoMNNVL(logr.Discard(), test.pcs, test.autoMNNVLEnabled)

			if test.expectedAnnotation == "" {
				if test.pcs.Annotations != nil {
					_, exists := test.pcs.Annotations[AnnotationAutoMNNVL]
					assert.False(t, exists, "annotation should not exist")
				}
			} else {
				assert.Equal(t, test.expectedAnnotation, test.pcs.Annotations[AnnotationAutoMNNVL])
			}
		})
	}
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
			description:      "annotation enabled + feature enabled -> no error",
			pcs:              createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLEnabled}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description:      "annotation enabled + feature disabled -> error",
			pcs:              createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLEnabled}),
			autoMNNVLEnabled: false,
			expectError:      true,
			errorContains:    "MNNVL is not enabled",
		},
		{
			description:      "annotation disabled + feature disabled -> no error",
			pcs:              createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLDisabled}),
			autoMNNVLEnabled: false,
			expectError:      false,
		},
		{
			description:      "annotation disabled + feature enabled -> no error",
			pcs:              createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLDisabled}),
			autoMNNVLEnabled: true,
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
		// Invalid annotation value tests
		{
			description:      "invalid annotation value 'true' -> error",
			pcs:              createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "true"}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "must be",
		},
		{
			description:      "invalid annotation value 'false' -> error",
			pcs:              createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "false"}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "must be",
		},
		{
			description:      "invalid annotation value empty string -> error",
			pcs:              createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: ""}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "must be",
		},
		{
			description:      "invalid annotation value 'yes' -> error",
			pcs:              createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "yes"}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "must be",
		},
		{
			description:      "invalid annotation value 'ENABLED' (wrong case) -> error",
			pcs:              createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "ENABLED"}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "must be",
		},
		{
			description:      "invalid annotation value with feature disabled -> error (value validation first)",
			pcs:              createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "invalid"}),
			autoMNNVLEnabled: false,
			expectError:      true,
			errorContains:    "must be",
		},
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
		{
			description:      "enabled + mnnvl-group -> no error (compatible)",
			pcs:              createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLEnabled, AnnotationMNNVLGroup: "training"}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description:      "disabled + mnnvl-group -> error (conflict)",
			pcs:              createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLDisabled, AnnotationMNNVLGroup: "training"}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "contradictory",
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
			description: "annotation unchanged enabled -> no error",
			oldPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLEnabled}),
			newPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLEnabled}),
			expectError: false,
		},
		{
			description: "annotation unchanged disabled -> no error",
			oldPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLDisabled}),
			newPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLDisabled}),
			expectError: false,
		},
		{
			description: "annotation added -> error",
			oldPCS:      createPCSWithGPU(nil),
			newPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLEnabled}),
			expectError: true,
			errorMsg:    "cannot be added",
		},
		{
			description: "annotation removed -> error",
			oldPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLEnabled}),
			newPCS:      createPCSWithGPU(nil),
			expectError: true,
			errorMsg:    "cannot be removed",
		},
		{
			description: "annotation changed enabled to disabled -> error",
			oldPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLEnabled}),
			newPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLDisabled}),
			expectError: true,
			errorMsg:    "immutable",
		},
		{
			description: "annotation changed disabled to enabled -> error",
			oldPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLDisabled}),
			newPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLEnabled}),
			expectError: true,
			errorMsg:    "immutable",
		},
		{
			description: "other annotations changed but mnnvl unchanged -> no error",
			oldPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLEnabled, "other": "old"}),
			newPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLEnabled, "other": "new"}),
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
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			errs := ValidatePCSOnUpdate(test.oldPCS, test.newPCS)

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
			description: "conflict on clique template (disabled + group) -> error",
			pcs: createPCSWithCliques([]cliqueAnnotation{
				{name: "workers", annotations: map[string]string{AnnotationAutoMNNVL: AnnotationAutoMNNVLDisabled, AnnotationMNNVLGroup: "training"}},
			}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "contradictory",
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
			errs := ValidatePCSOnUpdate(test.oldPCS, test.newPCS)

			if test.expectError {
				assert.NotEmpty(t, errs, "expected validation errors")
				assert.Contains(t, errs.ToAggregate().Error(), test.errorMsg)
			} else {
				assert.Empty(t, errs, "expected no validation errors")
			}
		})
	}
}

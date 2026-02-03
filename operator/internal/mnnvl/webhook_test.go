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

func TestValidateMetadataOnCreate(t *testing.T) {
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
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			errs := ValidateMetadataOnCreate(test.pcs, test.autoMNNVLEnabled)

			if test.expectError {
				assert.NotEmpty(t, errs, "expected validation errors")
				assert.Contains(t, errs.ToAggregate().Error(), test.errorContains)
			} else {
				assert.Empty(t, errs, "expected no validation errors")
			}
		})
	}
}

func TestValidateMetadataOnUpdate(t *testing.T) {
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
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			errs := ValidateMetadataOnUpdate(test.oldPCS, test.newPCS)

			if test.expectError {
				assert.NotEmpty(t, errs, "expected validation errors")
				assert.Contains(t, errs.ToAggregate().Error(), test.errorMsg)
			} else {
				assert.Empty(t, errs, "expected no validation errors")
			}
		})
	}
}

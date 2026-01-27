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

	"github.com/stretchr/testify/assert"
)

func TestMutateAutoMNNVL(t *testing.T) {
	tests := []struct {
		description        string
		pcs                *grovecorev1alpha1.PodCliqueSet
		autoMNNVLEnabled   bool
		expectMutation     bool
		expectedAnnotation string
	}{
		{
			description:        "feature enabled + GPU + no annotation -> add true",
			pcs:                createPCSWithGPU(nil),
			autoMNNVLEnabled:   true,
			expectMutation:     true,
			expectedAnnotation: "true",
		},
		{
			description:        "feature enabled + GPU + existing false -> no change",
			pcs:                createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "false"}),
			autoMNNVLEnabled:   true,
			expectMutation:     false,
			expectedAnnotation: "false",
		},
		{
			description:        "feature enabled + GPU + existing true -> no change",
			pcs:                createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "true"}),
			autoMNNVLEnabled:   true,
			expectMutation:     false,
			expectedAnnotation: "true",
		},
		{
			description:        "feature enabled + no GPU -> no annotation",
			pcs:                createPCSWithoutGPU(nil),
			autoMNNVLEnabled:   true,
			expectMutation:     false,
			expectedAnnotation: "",
		},
		{
			description:        "feature disabled + GPU -> no annotation",
			pcs:                createPCSWithGPU(nil),
			autoMNNVLEnabled:   false,
			expectMutation:     false,
			expectedAnnotation: "",
		},
		{
			description:        "feature disabled + no GPU -> no annotation",
			pcs:                createPCSWithoutGPU(nil),
			autoMNNVLEnabled:   false,
			expectMutation:     false,
			expectedAnnotation: "",
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			mutated := MutateAutoMNNVL(test.pcs, test.autoMNNVLEnabled)

			assert.Equal(t, test.expectMutation, mutated)

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

func TestValidateAutoMNNVLOnCreate(t *testing.T) {
	tests := []struct {
		description      string
		pcs              *grovecorev1alpha1.PodCliqueSet
		autoMNNVLEnabled bool
		expectError      bool
	}{
		{
			description:      "annotation true + feature enabled -> no error",
			pcs:              createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "true"}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description:      "annotation true + feature disabled -> error",
			pcs:              createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "true"}),
			autoMNNVLEnabled: false,
			expectError:      true,
		},
		{
			description:      "annotation false + feature disabled -> no error",
			pcs:              createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "false"}),
			autoMNNVLEnabled: false,
			expectError:      false,
		},
		{
			description:      "annotation false + feature enabled -> no error",
			pcs:              createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "false"}),
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
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			err := ValidateAutoMNNVLOnCreate(test.pcs, test.autoMNNVLEnabled)

			if test.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "MNNVL is not enabled")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateAutoMNNVLOnUpdate(t *testing.T) {
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
			description: "annotation unchanged true -> no error",
			oldPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "true"}),
			newPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "true"}),
			expectError: false,
		},
		{
			description: "annotation unchanged false -> no error",
			oldPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "false"}),
			newPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "false"}),
			expectError: false,
		},
		{
			description: "annotation added -> error",
			oldPCS:      createPCSWithGPU(nil),
			newPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "true"}),
			expectError: true,
			errorMsg:    "cannot be added",
		},
		{
			description: "annotation removed -> error",
			oldPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "true"}),
			newPCS:      createPCSWithGPU(nil),
			expectError: true,
			errorMsg:    "cannot be removed",
		},
		{
			description: "annotation changed true to false -> error",
			oldPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "true"}),
			newPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "false"}),
			expectError: true,
			errorMsg:    "immutable",
		},
		{
			description: "annotation changed false to true -> error",
			oldPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "false"}),
			newPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "true"}),
			expectError: true,
			errorMsg:    "immutable",
		},
		{
			description: "other annotations changed but mnnvl unchanged -> no error",
			oldPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "true", "other": "old"}),
			newPCS:      createPCSWithGPU(map[string]string{AnnotationAutoMNNVL: "true", "other": "new"}),
			expectError: false,
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			err := ValidateAutoMNNVLOnUpdate(test.oldPCS, test.newPCS)

			if test.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), test.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

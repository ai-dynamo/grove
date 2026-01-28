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

package validation

import (
	"context"
	"testing"
	"time"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
)

// TestValidateCreate_MNNVL tests the MNNVL annotation validation on create.
func TestValidateCreate_MNNVL(t *testing.T) {
	tests := []struct {
		description      string
		pcs              *grovecorev1alpha1.PodCliqueSet
		autoMNNVLEnabled bool
		expectError      bool
		errorContains    string
	}{
		{
			description:      "annotation enabled + feature enabled -> no error",
			pcs:              createValidPCSWithGPU(map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLEnabled}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description:      "annotation enabled + feature disabled -> error",
			pcs:              createValidPCSWithGPU(map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLEnabled}),
			autoMNNVLEnabled: false,
			expectError:      true,
			errorContains:    "MNNVL is not enabled",
		},
		{
			description:      "annotation disabled + feature disabled -> no error",
			pcs:              createValidPCSWithGPU(map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLDisabled}),
			autoMNNVLEnabled: false,
			expectError:      false,
		},
		{
			description:      "no annotation + feature disabled -> no error",
			pcs:              createValidPCSWithGPU(nil),
			autoMNNVLEnabled: false,
			expectError:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			cl := testutils.NewTestClientBuilder().Build()
			mgr := &testutils.FakeManager{
				Client: cl,
				Scheme: cl.Scheme(),
				Logger: logr.Discard(),
			}

			networkConfig := configv1alpha1.NetworkAcceleration{
				AutoMNNVLEnabled: tt.autoMNNVLEnabled,
			}
			handler := NewHandler(mgr, getDefaultTASConfig(), networkConfig)

			ctx := context.Background()
			warnings, err := handler.ValidateCreate(ctx, tt.pcs)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.Empty(t, warnings)
			}
		})
	}
}

// TestValidateUpdate_MNNVL tests the MNNVL annotation immutability on update.
func TestValidateUpdate_MNNVL(t *testing.T) {
	tests := []struct {
		description   string
		oldPCS        *grovecorev1alpha1.PodCliqueSet
		newPCS        *grovecorev1alpha1.PodCliqueSet
		expectError   bool
		errorContains string
	}{
		{
			description: "annotation unchanged -> no error",
			oldPCS:      createValidPCSWithGPU(map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLEnabled}),
			newPCS:      createValidPCSWithGPU(map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLEnabled}),
			expectError: false,
		},
		{
			description:   "annotation added -> error",
			oldPCS:        createValidPCSWithGPU(nil),
			newPCS:        createValidPCSWithGPU(map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLEnabled}),
			expectError:   true,
			errorContains: "cannot be added",
		},
		{
			description:   "annotation removed -> error",
			oldPCS:        createValidPCSWithGPU(map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLEnabled}),
			newPCS:        createValidPCSWithGPU(nil),
			expectError:   true,
			errorContains: "cannot be removed",
		},
		{
			description:   "annotation changed enabled to disabled -> error",
			oldPCS:        createValidPCSWithGPU(map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLEnabled}),
			newPCS:        createValidPCSWithGPU(map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLDisabled}),
			expectError:   true,
			errorContains: "immutable",
		},
		{
			description:   "annotation changed disabled to enabled -> error",
			oldPCS:        createValidPCSWithGPU(map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLDisabled}),
			newPCS:        createValidPCSWithGPU(map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLEnabled}),
			expectError:   true,
			errorContains: "immutable",
		},
		{
			description: "no annotation on both -> no error",
			oldPCS:      createValidPCSWithGPU(nil),
			newPCS:      createValidPCSWithGPU(nil),
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			cl := testutils.NewTestClientBuilder().Build()
			mgr := &testutils.FakeManager{
				Client: cl,
				Scheme: cl.Scheme(),
				Logger: logr.Discard(),
			}

			// MNNVL validation on update doesn't depend on feature flag
			handler := NewHandler(mgr, getDefaultTASConfig(), getDefaultNetworkConfig())

			ctx := context.Background()
			warnings, err := handler.ValidateUpdate(ctx, tt.oldPCS, tt.newPCS)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.Empty(t, warnings)
			}
		})
	}
}

// createValidPCSWithGPU creates a fully valid PCS with GPU for validation tests.
func createValidPCSWithGPU(annotations map[string]string) *grovecorev1alpha1.PodCliqueSet {
	return testutils.NewPodCliqueSetBuilder("test-pcs", "default", "").
		WithAnnotations(annotations).
		WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder)).
		WithTerminationDelay(4 * time.Hour).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder("worker").
				WithRoleName("worker").
				WithMinAvailable(1).
				WithContainer(testutils.NewGPUContainer("train", "nvidia/cuda:latest", 8)).
				Build(),
		).
		Build()
}

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

package defaulting

import (
	"context"
	"testing"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// TestDefault_MNNVL tests the MNNVL auto-annotation behavior in the defaulting webhook.
func TestDefault_MNNVL(t *testing.T) {
	tests := []struct {
		description        string
		pcs                *grovecorev1alpha1.PodCliqueSet
		autoMNNVLEnabled   bool
		expectedAnnotation string // empty string means annotation should not exist
	}{
		{
			description:        "feature enabled + GPU + no annotation -> adds annotation",
			pcs:                createPCSWithGPU(nil),
			autoMNNVLEnabled:   true,
			expectedAnnotation: mnnvl.AnnotationAutoMNNVLEnabled,
		},
		{
			description:        "feature disabled + GPU -> no annotation added",
			pcs:                createPCSWithGPU(nil),
			autoMNNVLEnabled:   false,
			expectedAnnotation: "",
		},
		{
			description:        "feature enabled + no GPU -> no annotation added",
			pcs:                createPCSWithoutGPU(nil),
			autoMNNVLEnabled:   true,
			expectedAnnotation: "",
		},
		{
			description:        "feature enabled + GPU + existing disabled annotation -> unchanged",
			pcs:                createPCSWithGPU(map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLDisabled}),
			autoMNNVLEnabled:   true,
			expectedAnnotation: mnnvl.AnnotationAutoMNNVLDisabled,
		},
		{
			description:        "feature enabled + GPU + existing enabled annotation -> unchanged",
			pcs:                createPCSWithGPU(map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLEnabled}),
			autoMNNVLEnabled:   true,
			expectedAnnotation: mnnvl.AnnotationAutoMNNVLEnabled,
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
			handler := NewHandler(mgr, networkConfig)

			ctx := admission.NewContextWithRequest(context.Background(), admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Name:      "test-pcs",
					Namespace: "default",
					Operation: admissionv1.Create,
					UserInfo: authenticationv1.UserInfo{
						Username: "test-user",
					},
				},
			})

			err := handler.Default(ctx, tt.pcs)
			require.NoError(t, err)

			if tt.expectedAnnotation == "" {
				if tt.pcs.Annotations != nil {
					_, exists := tt.pcs.Annotations[mnnvl.AnnotationAutoMNNVL]
					assert.False(t, exists, "annotation should not exist")
				}
			} else {
				require.NotNil(t, tt.pcs.Annotations)
				assert.Equal(t, tt.expectedAnnotation, tt.pcs.Annotations[mnnvl.AnnotationAutoMNNVL])
			}
		})
	}
}

// TestDefault_MNNVL_UpdateOperation tests that the defaulting webhook does NOT inject
// the auto-mnnvl annotation during UPDATE operations.
func TestDefault_MNNVL_UpdateOperation(t *testing.T) {
	tests := []struct {
		description        string
		pcs                *grovecorev1alpha1.PodCliqueSet
		autoMNNVLEnabled   bool
		expectedAnnotation string // empty string means annotation should not exist
	}{
		{
			description:        "update: legacy PCS without annotation + feature enabled + GPU -> annotation NOT added",
			pcs:                createPCSWithGPU(nil),
			autoMNNVLEnabled:   true,
			expectedAnnotation: "",
		},
		{
			description:        "update: legacy PCS without annotation + feature disabled + GPU -> annotation NOT added",
			pcs:                createPCSWithGPU(nil),
			autoMNNVLEnabled:   false,
			expectedAnnotation: "",
		},
		{
			description:        "update: PCS with existing enabled annotation + feature enabled -> annotation preserved",
			pcs:                createPCSWithGPU(map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLEnabled}),
			autoMNNVLEnabled:   true,
			expectedAnnotation: mnnvl.AnnotationAutoMNNVLEnabled,
		},
		{
			description:        "update: PCS with existing disabled annotation + feature enabled -> annotation preserved",
			pcs:                createPCSWithGPU(map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLDisabled}),
			autoMNNVLEnabled:   true,
			expectedAnnotation: mnnvl.AnnotationAutoMNNVLDisabled,
		},
		{
			description:        "update: legacy PCS without annotation + feature enabled + no GPU -> annotation NOT added",
			pcs:                createPCSWithoutGPU(nil),
			autoMNNVLEnabled:   true,
			expectedAnnotation: "",
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
			handler := NewHandler(mgr, networkConfig)

			ctx := admission.NewContextWithRequest(context.Background(), admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Name:      "test-pcs",
					Namespace: "default",
					Operation: admissionv1.Update,
					UserInfo: authenticationv1.UserInfo{
						Username: "test-user",
					},
				},
			})

			err := handler.Default(ctx, tt.pcs)
			require.NoError(t, err)

			if tt.expectedAnnotation == "" {
				if tt.pcs.Annotations != nil {
					_, exists := tt.pcs.Annotations[mnnvl.AnnotationAutoMNNVL]
					assert.False(t, exists, "annotation should not exist on update")
				}
			} else {
				require.NotNil(t, tt.pcs.Annotations)
				assert.Equal(t, tt.expectedAnnotation, tt.pcs.Annotations[mnnvl.AnnotationAutoMNNVL])
			}
		})
	}
}

// createPCSWithGPU creates a PCS with GPU using the builder.
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

// createPCSWithoutGPU creates a PCS without GPU using the builder.
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

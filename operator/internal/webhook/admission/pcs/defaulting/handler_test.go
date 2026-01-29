// /*
// Copyright 2024 The Grove Authors.
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
	"time"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// TestNewHandler tests the creation of a new defaulting handler.
func TestNewHandler(t *testing.T) {
	cl := testutils.NewTestClientBuilder().Build()
	mgr := &testutils.FakeManager{
		Client: cl,
		Scheme: cl.Scheme(),
		Logger: logr.Discard(),
	}

	handler := NewHandler(mgr, configv1alpha1.NetworkAcceleration{})
	require.NotNil(t, handler)
	assert.NotNil(t, handler.logger)
}

// TestDefault tests the Default method which applies default values to PodCliqueSet.
func TestDefault(t *testing.T) {
	tests := []struct {
		// name identifies this test case
		name string
		// obj is the runtime object to apply defaults to
		obj runtime.Object
		// setupContext sets up the admission context if needed
		setupContext func(context.Context) context.Context
		// expectError indicates whether defaulting should fail
		expectError bool
		// errorContains is a substring expected in the error message
		errorContains string
		// verify is a function to verify the defaulting was applied correctly
		verify func(*testing.T, runtime.Object)
	}{
		{
			name: "valid PodCliqueSet has defaults applied",
			obj: testutils.NewPodCliqueSetBuilder("test-pcs", "default", uuid.NewUUID()).
				WithReplicas(1).
				WithTerminationDelay(4 * time.Hour).
				WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder)).
				WithPodCliqueTemplateSpec(
					testutils.NewPodCliqueTemplateSpecBuilder("test").
						WithReplicas(1).
						WithRoleName("test-role").
						WithMinAvailable(1).
						Build()).
				Build(),
			setupContext: func(ctx context.Context) context.Context {
				return admission.NewContextWithRequest(ctx, admission.Request{
					AdmissionRequest: admissionv1.AdmissionRequest{
						Name:      "test-pcs",
						Namespace: "default",
						Operation: admissionv1.Create,
						UserInfo: authenticationv1.UserInfo{
							Username: "test-user",
						},
					},
				})
			},
			expectError: false,
			verify: func(t *testing.T, obj runtime.Object) {
				pcs, ok := obj.(*grovecorev1alpha1.PodCliqueSet)
				require.True(t, ok)
				// Verify that some defaults are set (e.g., startup type should remain as set)
				assert.NotNil(t, pcs.Spec.Template.StartupType)
			},
		},
		{
			name: "PodCliqueSet without startup type gets default",
			obj: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						StartupType: nil,
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{
								Name: "test",
								Spec: grovecorev1alpha1.PodCliqueSpec{
									Replicas: 1,
									RoleName: "test-role",
									PodSpec: corev1.PodSpec{
										Containers: []corev1.Container{
											{
												Name:  "test",
												Image: "test:latest",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			setupContext: func(ctx context.Context) context.Context {
				return admission.NewContextWithRequest(ctx, admission.Request{
					AdmissionRequest: admissionv1.AdmissionRequest{
						Name:      "test-pcs",
						Namespace: "default",
						Operation: admissionv1.Create,
						UserInfo: authenticationv1.UserInfo{
							Username: "test-user",
						},
					},
				})
			},
			expectError: false,
			verify: func(t *testing.T, obj runtime.Object) {
				pcs, ok := obj.(*grovecorev1alpha1.PodCliqueSet)
				require.True(t, ok)
				// TerminationDelay should be nil by default (gang termination disabled)
				assert.Nil(t, pcs.Spec.Template.TerminationDelay)
			},
		},
		{
			name: "wrong object type returns error",
			obj:  &corev1.Pod{},
			setupContext: func(ctx context.Context) context.Context {
				return admission.NewContextWithRequest(ctx, admission.Request{
					AdmissionRequest: admissionv1.AdmissionRequest{
						Name:      "test-pod",
						Namespace: "default",
						Operation: admissionv1.Create,
						UserInfo: authenticationv1.UserInfo{
							Username: "test-user",
						},
					},
				})
			},
			expectError:   true,
			errorContains: "expected an PodCliqueSet object",
		},
		{
			name: "context without admission request returns error",
			obj: testutils.NewPodCliqueSetBuilder("test-pcs", "default", uuid.NewUUID()).
				WithReplicas(1).
				WithTerminationDelay(4 * time.Hour).
				WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder)).
				WithPodCliqueTemplateSpec(
					testutils.NewPodCliqueTemplateSpecBuilder("test").
						WithReplicas(1).
						WithRoleName("test-role").
						WithMinAvailable(1).
						Build()).
				Build(),
			setupContext:  func(ctx context.Context) context.Context { return ctx },
			expectError:   true,
			errorContains: "not found in context",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := testutils.NewTestClientBuilder().Build()
			mgr := &testutils.FakeManager{
				Client: cl,
				Scheme: cl.Scheme(),
				Logger: logr.Discard(),
			}

			handler := NewHandler(mgr, configv1alpha1.NetworkAcceleration{})

			ctx := context.Background()
			if tt.setupContext != nil {
				ctx = tt.setupContext(ctx)
			}

			err := handler.Default(ctx, tt.obj)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				if tt.verify != nil {
					tt.verify(t, tt.obj)
				}
			}
		})
	}
}

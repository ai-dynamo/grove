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

package validation

import (
	"context"
	"testing"
	"time"

	groveconfigv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
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

// TestNewHandler tests the creation of a new validation handler.
func TestNewHandler(t *testing.T) {
	cl := testutils.NewTestClientBuilder().Build()
	mgr := &testutils.FakeManager{
		Client: cl,
		Scheme: cl.Scheme(),
		Logger: logr.Discard(),
	}

	cfg := groveconfigv1alpha1.OperatorConfiguration{
		TopologyAwareScheduling: getDefaultTASConfig(),
		Network:                 getDefaultNetworkConfig(),
		Scheduler:               groveconfigv1alpha1.SchedulerConfiguration{Profiles: []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube, Default: true}}},
	}
	handler := NewHandler(mgr, &cfg)
	require.NotNil(t, handler)
	assert.NotNil(t, handler.logger)
}

// TestValidateCreate tests validation of PodCliqueSet creation requests.
func TestValidateCreate(t *testing.T) {
	tests := []struct {
		// name identifies this test case
		name string
		// obj is the runtime object to validate
		obj runtime.Object
		// expectError indicates whether validation should fail
		expectError bool
		// errorContains is a substring expected in the error message
		errorContains string
	}{
		{
			name: "valid PodCliqueSet passes validation",
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
			expectError: false,
		},
		{
			name: "invalid PodCliqueSet with nil startup type fails validation",
			obj: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						StartupType: nil,
						Cliques:     []*grovecorev1alpha1.PodCliqueTemplateSpec{},
					},
				},
			},
			expectError:   true,
			errorContains: "field is required",
		},
		{
			name:          "wrong object type fails validation",
			obj:           &corev1.Pod{},
			expectError:   true,
			errorContains: "failed to cast object to PodCliqueSet",
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

			cfg := groveconfigv1alpha1.OperatorConfiguration{
				TopologyAwareScheduling: getDefaultTASConfig(),
				Network:                 getDefaultNetworkConfig(),
				Scheduler:               groveconfigv1alpha1.SchedulerConfiguration{Profiles: []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube, Default: true}}},
			}
			handler := NewHandler(mgr, &cfg)

			ctx := context.Background()
			warnings, err := handler.ValidateCreate(ctx, tt.obj)

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

// TestValidateUpdate tests validation of PodCliqueSet update requests.
func TestValidateUpdate(t *testing.T) {
	startupType := ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder)
	tests := []struct {
		// name identifies this test case
		name string
		// newObj is the new version of the object
		newObj runtime.Object
		// oldObj is the old version of the object
		oldObj runtime.Object
		// expectError indicates whether validation should fail
		expectError bool
		// errorContains is a substring expected in the error message
		errorContains string
	}{
		{
			name: "valid update passes validation",
			oldObj: testutils.NewPodCliqueSetBuilder("test-pcs", "default", uuid.NewUUID()).
				WithReplicas(2).
				WithTerminationDelay(4 * time.Hour).
				WithCliqueStartupType(startupType).
				WithPodCliqueTemplateSpec(
					testutils.NewPodCliqueTemplateSpecBuilder("test").
						WithReplicas(1).
						WithRoleName("test-role").
						WithMinAvailable(1).
						Build()).
				Build(),
			newObj: testutils.NewPodCliqueSetBuilder("test-pcs", "default", uuid.NewUUID()).
				WithReplicas(1).
				WithTerminationDelay(4 * time.Hour).
				WithCliqueStartupType(startupType).
				WithPodCliqueTemplateSpec(
					testutils.NewPodCliqueTemplateSpecBuilder("test").
						WithReplicas(1).
						WithRoleName("test-role").
						WithMinAvailable(1).
						Build()).
				Build(),
			expectError: false,
		},
		{
			name:   "wrong new object type fails validation",
			oldObj: &corev1.Pod{},
			newObj: testutils.NewPodCliqueSetBuilder("test", "default", uuid.NewUUID()).
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
			expectError:   true,
			errorContains: "failed to cast new object to PodCliqueSet",
		},
		{
			name: "wrong old object type fails validation",
			oldObj: testutils.NewPodCliqueSetBuilder("test", "default", uuid.NewUUID()).
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
			newObj:        &corev1.Pod{},
			expectError:   true,
			errorContains: "failed to cast old object to PodCliqueSet",
		},
		{
			name: "invalid update with changed startup type fails validation",
			oldObj: testutils.NewPodCliqueSetBuilder("test-pcs", "default", uuid.NewUUID()).
				WithReplicas(1).
				WithTerminationDelay(4 * time.Hour).
				WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeInOrder)).
				WithPodCliqueTemplateSpec(
					testutils.NewPodCliqueTemplateSpecBuilder("test").
						WithReplicas(1).
						WithRoleName("test-role").
						WithMinAvailable(1).
						Build()).
				Build(),
			newObj: testutils.NewPodCliqueSetBuilder("test-pcs", "default", uuid.NewUUID()).
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
			expectError:   true,
			errorContains: "field is immutable",
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

			cfg := groveconfigv1alpha1.OperatorConfiguration{
				TopologyAwareScheduling: getDefaultTASConfig(),
				Network:                 getDefaultNetworkConfig(),
				Scheduler:               groveconfigv1alpha1.SchedulerConfiguration{Profiles: []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube, Default: true}}},
			}
			handler := NewHandler(mgr, &cfg)

			ctx := context.Background()
			warnings, err := handler.ValidateUpdate(ctx, tt.newObj, tt.oldObj)

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

// TestValidateDelete tests validation of PodCliqueSet deletion requests.
func TestValidateDelete(t *testing.T) {
	cl := testutils.NewTestClientBuilder().Build()
	mgr := &testutils.FakeManager{
		Client: cl,
		Scheme: cl.Scheme(),
		Logger: logr.Discard(),
	}

	cfg := groveconfigv1alpha1.OperatorConfiguration{
		TopologyAwareScheduling: getDefaultTASConfig(),
		Network:                 getDefaultNetworkConfig(),
		Scheduler:               groveconfigv1alpha1.SchedulerConfiguration{Profiles: []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube, Default: true}}},
	}
	handler := NewHandler(mgr, &cfg)

	// Deletion validation always succeeds
	ctx := context.Background()
	pcs := testutils.NewPodCliqueSetBuilder("test", "default", uuid.NewUUID()).
		WithReplicas(1).
		WithTerminationDelay(4 * time.Hour).
		WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder)).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder("test").
				WithReplicas(1).
				WithRoleName("test-role").
				WithMinAvailable(1).
				Build()).
		Build()
	warnings, err := handler.ValidateDelete(ctx, pcs)
	assert.NoError(t, err)
	assert.Empty(t, warnings)
}

// TestCastToPodCliqueSet tests casting runtime.Object to PodCliqueSet.
func TestCastToPodCliqueSet(t *testing.T) {
	tests := []struct {
		// name identifies this test case
		name string
		// obj is the runtime object to cast
		obj runtime.Object
		// expectError indicates whether casting should fail
		expectError bool
	}{
		{
			name: "valid PodCliqueSet casts successfully",
			obj: testutils.NewPodCliqueSetBuilder("test", "default", uuid.NewUUID()).
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
			expectError: false,
		},
		{
			name:        "Pod object fails to cast",
			obj:         &corev1.Pod{},
			expectError: true,
		},
		{
			name:        "nil object fails to cast",
			obj:         nil,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs, err := castToPodCliqueSet(tt.obj)
			if tt.expectError {
				require.Error(t, err)
				assert.Nil(t, pcs)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, pcs)
			}
		})
	}
}

// TestLogValidatorFunctionInvocation tests logging of validation request details.
func TestLogValidatorFunctionInvocation(t *testing.T) {
	tests := []struct {
		// name identifies this test case
		name string
		// ctx is the context to use
		ctx context.Context
		// expectError indicates if an error should be logged
		expectError bool
	}{
		{
			name: "valid context with admission request logs successfully",
			ctx: admission.NewContextWithRequest(context.Background(), admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Name:      "test-pcs",
					Namespace: "default",
					Operation: admissionv1.Create,
					UserInfo: authenticationv1.UserInfo{
						Username: "test-user",
					},
				},
			}),
			expectError: false,
		},
		{
			name:        "context without admission request logs error",
			ctx:         context.Background(),
			expectError: true,
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

			cfg := groveconfigv1alpha1.OperatorConfiguration{
				TopologyAwareScheduling: getDefaultTASConfig(),
				Network:                 getDefaultNetworkConfig(),
				Scheduler:               groveconfigv1alpha1.SchedulerConfiguration{Profiles: []groveconfigv1alpha1.SchedulerProfile{{Name: groveconfigv1alpha1.SchedulerNameKube, Default: true}}},
			}
			handler := NewHandler(mgr, &cfg)

			// This function doesn't return an error, but we can verify it doesn't panic
			assert.NotPanics(t, func() {
				handler.logValidatorFunctionInvocation(tt.ctx)
			})
		})
	}
}

// ---------------------------- Helper Functions ----------------------------

// getDefaultTASConfig returns a default TAS configuration with TAS disabled.
func getDefaultTASConfig() groveconfigv1alpha1.TopologyAwareSchedulingConfiguration {
	return groveconfigv1alpha1.TopologyAwareSchedulingConfiguration{
		Enabled: false,
	}
}

// getDefaultNetworkConfig returns a default network configuration with MNNVL disabled.
func getDefaultNetworkConfig() groveconfigv1alpha1.NetworkAcceleration {
	return groveconfigv1alpha1.NetworkAcceleration{
		AutoMNNVLEnabled: false,
	}
}

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
	"github.com/ai-dynamo/grove/operator/internal/webhook/admission/pcs/defaulting"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
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
		{
			description:      "mnnvl-group on PCS + feature enabled -> no error",
			pcs:              createValidPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description:      "mnnvl-group on PCS + feature disabled -> error",
			pcs:              createValidPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}),
			autoMNNVLEnabled: false,
			expectError:      true,
			errorContains:    "MNNVL is not enabled",
		},
		{
			description:      "invalid mnnvl-group on PCS -> error",
			pcs:              createValidPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: "INVALID"}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "not a valid DNS-1123 label",
		},
		{
			description:      "conflict: auto-mnnvl disabled + mnnvl-group on PCS -> error",
			pcs:              createValidPCSWithGPU(map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLDisabled, mnnvl.AnnotationMNNVLGroup: "training"}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "contradictory",
		},
		// mnnvl-group on clique template
		{
			description:      "mnnvl-group on clique + feature enabled -> no error",
			pcs:              createValidPCSWithCliqueAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description:      "invalid mnnvl-group on clique -> error",
			pcs:              createValidPCSWithCliqueAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "-bad"}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "not a valid DNS-1123 label",
		},
		{
			description:      "conflict on clique: disabled + group -> error",
			pcs:              createValidPCSWithCliqueAnnotations(map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLDisabled, mnnvl.AnnotationMNNVLGroup: "training"}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "contradictory",
		},
		{
			description:      "mnnvl-group on PCSG config + feature enabled -> no error",
			pcs:              createValidPCSWithPCSGConfigAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}),
			autoMNNVLEnabled: true,
			expectError:      false,
		},
		{
			description:      "invalid mnnvl-group on PCSG config -> error",
			pcs:              createValidPCSWithPCSGConfigAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "-bad"}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "not a valid DNS-1123 label",
		},
		{
			description:      "conflict on PCSG config: disabled + group -> error",
			pcs:              createValidPCSWithPCSGConfigAnnotations(map[string]string{mnnvl.AnnotationAutoMNNVL: mnnvl.AnnotationAutoMNNVLDisabled, mnnvl.AnnotationMNNVLGroup: "training"}),
			autoMNNVLEnabled: true,
			expectError:      true,
			errorContains:    "contradictory",
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
			cfg := configv1alpha1.OperatorConfiguration{
				TopologyAwareScheduling: getDefaultTASConfig(),
				Network:                 networkConfig,
				Scheduler:               configv1alpha1.SchedulerConfiguration{Profiles: []configv1alpha1.SchedulerProfile{{Name: configv1alpha1.SchedulerNameKube}}, DefaultProfileName: string(configv1alpha1.SchedulerNameKube)},
			}
			handler := NewHandler(mgr, &cfg)

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
		{
			description: "mnnvl-group unchanged -> no error",
			oldPCS:      createValidPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}),
			newPCS:      createValidPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}),
			expectError: false,
		},
		{
			description:   "mnnvl-group added -> error",
			oldPCS:        createValidPCSWithGPU(nil),
			newPCS:        createValidPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}),
			expectError:   true,
			errorContains: "cannot be added",
		},
		{
			description:   "mnnvl-group changed -> error",
			oldPCS:        createValidPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: "workers"}),
			newPCS:        createValidPCSWithGPU(map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}),
			expectError:   true,
			errorContains: "immutable",
		},
		// mnnvl-group immutability on clique template
		{
			description: "clique mnnvl-group unchanged -> no error",
			oldPCS:      createValidPCSWithCliqueAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}),
			newPCS:      createValidPCSWithCliqueAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}),
			expectError: false,
		},
		{
			description:   "clique mnnvl-group changed -> error",
			oldPCS:        createValidPCSWithCliqueAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}),
			newPCS:        createValidPCSWithCliqueAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "inference"}),
			expectError:   true,
			errorContains: "immutable",
		},
		// mnnvl-group immutability on PCSG config
		{
			description: "PCSG config mnnvl-group unchanged -> no error",
			oldPCS:      createValidPCSWithPCSGConfigAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}),
			newPCS:      createValidPCSWithPCSGConfigAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}),
			expectError: false,
		},
		{
			description:   "PCSG config mnnvl-group changed -> error",
			oldPCS:        createValidPCSWithPCSGConfigAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}),
			newPCS:        createValidPCSWithPCSGConfigAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "inference"}),
			expectError:   true,
			errorContains: "immutable",
		},
		{
			description:   "PCSG config mnnvl-group added -> error",
			oldPCS:        createValidPCSWithPCSGConfigAnnotations(nil),
			newPCS:        createValidPCSWithPCSGConfigAnnotations(map[string]string{mnnvl.AnnotationMNNVLGroup: "training"}),
			expectError:   true,
			errorContains: "cannot be added",
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
			cfg := configv1alpha1.OperatorConfiguration{
				TopologyAwareScheduling: getDefaultTASConfig(),
				Network:                 getDefaultNetworkConfig(),
				Scheduler:               configv1alpha1.SchedulerConfiguration{Profiles: []configv1alpha1.SchedulerProfile{{Name: configv1alpha1.SchedulerNameKube}}, DefaultProfileName: string(configv1alpha1.SchedulerNameKube)},
			}
			handler := NewHandler(mgr, &cfg)

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

// TestMNNVL_WebhookPipeline_LegacyPCSUpdate simulates the full Kubernetes admission pipeline
// (defaulting webhook -> validating webhook) for the migration scenario where a PCS was created
// before the MNNVL feature existed. This test verifies that legacy resources can be updated
// without the webhooks creating a deadlock.
//
// Before the fix, this scenario caused a deadlock:
//  1. Defaulting webhook ran on UPDATE and injected the auto-mnnvl annotation
//  2. Validating webhook saw the annotation was "added" (old=absent, new=present) and rejected it
//  3. The resource could not be modified at all (e.g., finalizer removal was blocked)
func TestMNNVL_WebhookPipeline_LegacyPCSUpdate(t *testing.T) {
	tests := []struct {
		description      string
		autoMNNVLEnabled bool
	}{
		{
			description:      "legacy PCS updated with MNNVL feature enabled -> no deadlock",
			autoMNNVLEnabled: true,
		},
		{
			description:      "legacy PCS updated with MNNVL feature disabled -> no deadlock",
			autoMNNVLEnabled: false,
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

			// Simulate a legacy PCS (created before MNNVL feature) — no auto-mnnvl annotation.
			// The oldPCS represents the stored object in etcd.
			oldPCS := createValidPCSWithGPU(nil)

			// The newPCS represents the user's update request (e.g., removing a finalizer).
			// Start with a copy of oldPCS — no annotation, just like the stored object.
			newPCS := createValidPCSWithGPU(nil)

			// Step 1: Simulate the defaulting webhook running on the new object during an UPDATE.
			// In the real admission pipeline, the mutating webhook runs first and modifies newPCS.
			// We use the actual defaulting handler (not MutateAutoMNNVL directly) to test
			// the real code path including the operation-type guard.
			defaultingHandler := defaulting.NewHandler(mgr, networkConfig)
			updateCtx := admission.NewContextWithRequest(context.Background(), admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Name:      "test-pcs",
					Namespace: "default",
					Operation: admissionv1.Update,
					UserInfo: authenticationv1.UserInfo{
						Username: "test-user",
					},
				},
			})
			err := defaultingHandler.Default(updateCtx, newPCS)
			require.NoError(t, err, "defaulting webhook should not error on update")

			// Step 2: Simulate the validating webhook running with oldPCS vs (possibly mutated) newPCS.
			validationCfg := configv1alpha1.OperatorConfiguration{
				TopologyAwareScheduling: getDefaultTASConfig(),
				Network:                 networkConfig,
				Scheduler:               configv1alpha1.SchedulerConfiguration{Profiles: []configv1alpha1.SchedulerProfile{{Name: configv1alpha1.SchedulerNameKube}}, DefaultProfileName: string(configv1alpha1.SchedulerNameKube)},
			}
			validationHandler := NewHandler(mgr, &validationCfg)

			ctx := context.Background()
			warnings, err := validationHandler.ValidateUpdate(ctx, oldPCS, newPCS)

			// The update MUST succeed — the defaulting webhook should not have injected the annotation
			// during an update, so the validating webhook should see no annotation change.
			assert.NoError(t, err, "legacy PCS update should not be rejected by validation webhook")
			_ = warnings // warnings (e.g., restartPolicy) are informational and not relevant to this test

			// Verify the annotation was NOT added to newPCS by the defaulting webhook.
			if newPCS.Annotations != nil {
				_, exists := newPCS.Annotations[mnnvl.AnnotationAutoMNNVL]
				assert.False(t, exists, "defaulting webhook should not inject auto-mnnvl annotation during update")
			}
		})
	}
}

// createValidPCSWithGPU creates a fully valid PCS with GPU and PCS-level annotations.
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

// createValidPCSWithCliqueAnnotations creates a fully valid PCS with
// clique-level annotations for testing spec-level validation.
func createValidPCSWithCliqueAnnotations(cliqueAnnotations map[string]string) *grovecorev1alpha1.PodCliqueSet {
	return testutils.NewPodCliqueSetBuilder("test-pcs", "default", "").
		WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder)).
		WithTerminationDelay(4 * time.Hour).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder("worker").
				WithRoleName("worker").
				WithMinAvailable(1).
				WithAnnotations(cliqueAnnotations).
				WithContainer(testutils.NewGPUContainer("train", "nvidia/cuda:latest", 8)).
				Build(),
		).
		Build()
}

// createValidPCSWithPCSGConfigAnnotations creates a fully valid PCS with a
// PCSG config carrying the given annotations, for testing spec-level validation.
func createValidPCSWithPCSGConfigAnnotations(pcsgAnnotations map[string]string) *grovecorev1alpha1.PodCliqueSet {
	return testutils.NewPodCliqueSetBuilder("test-pcs", "default", "").
		WithCliqueStartupType(ptr.To(grovecorev1alpha1.CliqueStartupTypeAnyOrder)).
		WithTerminationDelay(4 * time.Hour).
		WithPodCliqueTemplateSpec(
			testutils.NewPodCliqueTemplateSpecBuilder("worker").
				WithRoleName("worker").
				WithMinAvailable(1).
				WithContainer(testutils.NewGPUContainer("train", "nvidia/cuda:latest", 8)).
				Build(),
		).
		WithPodCliqueScalingGroupConfig(grovecorev1alpha1.PodCliqueScalingGroupConfig{
			Name:        "scaling-group-1",
			CliqueNames: []string{"worker"},
			Annotations: pcsgAnnotations,
		}).
		Build()
}

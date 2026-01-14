/*
Copyright 2025 The Grove Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pod

import (
	"testing"

	"github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestGetSelectorLabelsForPods_PCSGOwnedPodClique tests that getSelectorLabelsForPods
// returns the correct selector labels for PCSG-owned PodCliques.
//
// BUG: The current implementation uses GetFirstOwnerName() which returns the PCSG name
// for PCSG-owned PodCliques, but pods are labeled with the PCS name (from LabelPartOfKey).
// This causes a label mismatch during deletion, orphaning pods.
//
// This test will FAIL with the current buggy code and PASS after the fix.
func TestGetSelectorLabelsForPods_PCSGOwnedPodClique(t *testing.T) {
	const (
		pcsName  = "workload1"               // The PodCliqueSet name
		pcsgName = "workload1-0-sg-x"        // The PodCliqueScalingGroup name (owner)
		pclqName = "workload1-0-sg-x-0-pc-a" // The PodClique name
	)

	tests := []struct {
		name           string
		pclqObjectMeta metav1.ObjectMeta
		expectedLabels map[string]string
		description    string
	}{
		{
			name: "PCS-owned PodClique should use PCS name in selector",
			pclqObjectMeta: metav1.ObjectMeta{
				Name:      "workload1-0-pc-a",
				Namespace: "default",
				Labels: map[string]string{
					common.LabelPartOfKey:    pcsName,
					common.LabelManagedByKey: common.LabelManagedByValue,
					common.LabelPodClique:    "workload1-0-pc-a",
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "grove.io/v1alpha1",
						Kind:       "PodCliqueSet",
						Name:       pcsName, // Owner is PCS
					},
				},
			},
			expectedLabels: map[string]string{
				common.LabelManagedByKey: common.LabelManagedByValue,
				common.LabelPartOfKey:    pcsName, // Should be PCS name
				common.LabelPodClique:    "workload1-0-pc-a",
			},
			description: "PCS-owned PodCliques work correctly (owner name == PCS name)",
		},
		{
			name: "PCSG-owned PodClique should use PCS name in selector (not PCSG name)",
			pclqObjectMeta: metav1.ObjectMeta{
				Name:      pclqName,
				Namespace: "default",
				Labels: map[string]string{
					common.LabelPartOfKey:             pcsName, // Pods are labeled with PCS name
					common.LabelManagedByKey:          common.LabelManagedByValue,
					common.LabelPodClique:             pclqName,
					common.LabelPodCliqueScalingGroup: pcsgName,
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "grove.io/v1alpha1",
						Kind:       "PodCliqueScalingGroup",
						Name:       pcsgName, // Owner is PCSG, NOT PCS!
					},
				},
			},
			expectedLabels: map[string]string{
				common.LabelManagedByKey: common.LabelManagedByValue,
				common.LabelPartOfKey:    pcsName, // Should be PCS name, NOT pcsgName!
				common.LabelPodClique:    pclqName,
			},
			description: "PCSG-owned PodCliques should use PCS name from labels, not owner reference",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actualLabels := getSelectorLabelsForPods(tt.pclqObjectMeta)

			// Check each expected label
			for key, expectedValue := range tt.expectedLabels {
				actualValue, exists := actualLabels[key]
				if !exists {
					t.Errorf("Expected label %q not found in selector", key)
					continue
				}
				if actualValue != expectedValue {
					t.Errorf("Label %q: expected %q, got %q. %s",
						key, expectedValue, actualValue, tt.description)
				}
			}

			// Specifically check the LabelPartOfKey which is the root cause of the bug
			if actualLabels[common.LabelPartOfKey] != tt.expectedLabels[common.LabelPartOfKey] {
				t.Errorf("BUG DETECTED: LabelPartOfKey mismatch!\n"+
					"  Expected (PCS name): %q\n"+
					"  Got (owner name):    %q\n"+
					"  This causes pods to be orphaned during PCSG-owned PodClique deletion.",
					tt.expectedLabels[common.LabelPartOfKey],
					actualLabels[common.LabelPartOfKey])
			}
		})
	}
}

func TestAddEnvironmentVariables(t *testing.T) {
	tests := []struct {
		name              string
		pclq              *grovecorev1alpha1.PodClique
		expectedEnvVars   []string
		unexpectedEnvVars []string
	}{
		{
			name: "standalone PodClique",
			pclq: &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pclq",
					Namespace: "test-ns",
				},
				Spec: grovecorev1alpha1.PodCliqueSpec{
					PodSpec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
							},
						},
					},
				},
			},
			expectedEnvVars: []string{
				constants.EnvVarPodCliqueSetName,
				constants.EnvVarPodCliqueSetIndex,
				constants.EnvVarPodCliqueName,
				constants.EnvVarHeadlessService,
				constants.EnvVarPodIndex,
			},
		},
		{
			name: "PCSG member PodClique",
			pclq: &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pclq",
					Namespace: "test-ns",
					Labels: map[string]string{
						common.LabelPodCliqueScalingGroup: "test-pcsg",
					},
				},
				Spec: grovecorev1alpha1.PodCliqueSpec{
					PodSpec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
							},
						},
					},
				},
			},
			expectedEnvVars: []string{
				constants.EnvVarPodCliqueSetName,
				constants.EnvVarPodCliqueSetIndex,
				constants.EnvVarPodCliqueName,
				constants.EnvVarHeadlessService,
				constants.EnvVarPodIndex,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{
				Spec: tt.pclq.Spec.PodSpec,
			}

			addEnvironmentVariables(pod, tt.pclq, "test-pcs", 0, 0)

			// Check that all containers have the expected environment variables
			for _, container := range pod.Spec.Containers {
				assertExpectedEnvVars(t, container, tt.expectedEnvVars)

				// Check unexpected environment variables are not present
				envVarNames := make(map[string]bool)
				for _, env := range container.Env {
					envVarNames[env.Name] = true
				}
				for _, unexpectedEnv := range tt.unexpectedEnvVars {
					if envVarNames[unexpectedEnv] {
						t.Errorf("unexpected environment variable %s found in container %s", unexpectedEnv, container.Name)
					}
				}

				// Verify Grove environment variables use direct values
				assertGroveEnvVarsDirectValues(t, container, tt.expectedEnvVars)
			}
		})
	}
}

func TestAddGroveEnvironmentVariables_NoDuplicates(t *testing.T) {
	tests := []struct {
		name            string
		pclq            *grovecorev1alpha1.PodClique
		existingEnvVars []corev1.EnvVar
		expectedEnvVars []string
		shouldReplace   map[string]string // env var name -> expected value
		shouldPreserve  []string          // env var names that should be preserved
	}{
		{
			name: "Container with existing Grove env vars - should replace",
			pclq: &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pclq",
					Namespace: "test-ns",
				},
				Spec: grovecorev1alpha1.PodCliqueSpec{
					PodSpec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
								Env: []corev1.EnvVar{
									{Name: "GROVE_PCS_NAME", Value: "old-pcs-name"},
									{Name: "GROVE_PCS_INDEX", Value: "old-index"},
								},
							},
						},
					},
				},
			},
			expectedEnvVars: []string{
				constants.EnvVarPodCliqueSetName,
				constants.EnvVarPodCliqueSetIndex,
				constants.EnvVarPodCliqueName,
				constants.EnvVarHeadlessService,
			},
			shouldReplace: map[string]string{
				"GROVE_PCS_NAME":  "test-pcs",
				"GROVE_PCS_INDEX": "0",
			},
		},
		{
			name: "Container with user env vars - should preserve",
			pclq: &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pclq",
					Namespace: "test-ns",
				},
				Spec: grovecorev1alpha1.PodCliqueSpec{
					PodSpec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
								Env: []corev1.EnvVar{
									{Name: "USER_VAR", Value: "user-value"},
									{Name: "CUSTOM_CONFIG", Value: "custom-value"},
								},
							},
						},
					},
				},
			},
			expectedEnvVars: []string{
				constants.EnvVarPodCliqueSetName,
				constants.EnvVarPodCliqueSetIndex,
				constants.EnvVarPodCliqueName,
				constants.EnvVarHeadlessService,
			},
			shouldPreserve: []string{"USER_VAR", "CUSTOM_CONFIG"},
		},
		{
			name: "Container with mixed env vars - should replace Grove, preserve user",
			pclq: &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pclq",
					Namespace: "test-ns",
				},
				Spec: grovecorev1alpha1.PodCliqueSpec{
					PodSpec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
								Env: []corev1.EnvVar{
									{Name: "USER_VAR", Value: "user-value"},
									{Name: "GROVE_PCS_NAME", Value: "old-pcs-name"},
									{Name: "CUSTOM_CONFIG", Value: "custom-value"},
								},
							},
						},
					},
				},
			},
			expectedEnvVars: []string{
				constants.EnvVarPodCliqueSetName,
				constants.EnvVarPodCliqueSetIndex,
				constants.EnvVarPodCliqueName,
				constants.EnvVarHeadlessService,
			},
			shouldReplace: map[string]string{
				"GROVE_PCS_NAME": "test-pcs",
			},
			shouldPreserve: []string{"USER_VAR", "CUSTOM_CONFIG"},
		},
		{
			name: "PCSG PodClique with existing env vars",
			pclq: &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pclq",
					Namespace: "test-ns",
					Labels: map[string]string{
						common.LabelPodCliqueScalingGroup: "test-pcsg",
					},
				},
				Spec: grovecorev1alpha1.PodCliqueSpec{
					PodSpec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
								Env: []corev1.EnvVar{
									{Name: "GROVE_PCSG_NAME", Value: "old-pcsg-name"},
									{Name: "USER_VAR", Value: "user-value"},
								},
							},
						},
					},
				},
			},
			expectedEnvVars: []string{
				constants.EnvVarPodCliqueSetName,
				constants.EnvVarPodCliqueSetIndex,
				constants.EnvVarPodCliqueName,
				constants.EnvVarHeadlessService,
			},
			shouldReplace:  map[string]string{},
			shouldPreserve: []string{"USER_VAR"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{
				Spec: tt.pclq.Spec.PodSpec,
			}

			addEnvironmentVariables(pod, tt.pclq, "test-pcs", 0, 0)

			// Check that all containers have the expected environment variables
			for _, container := range pod.Spec.Containers {
				assertExpectedEnvVars(t, container, tt.expectedEnvVars)
				assertReplacedEnvVars(t, container, tt.shouldReplace)
				assertPreservedEnvVars(t, container, tt.shouldPreserve)
			}
		})
	}
}

func TestAddGroveEnvironmentVariables_EmptyContainers(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{},
		},
	}
	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pclq",
			Namespace: "test-ns",
		},
	}

	// Should not panic with empty containers
	addEnvironmentVariables(pod, pclq, "test-pcs", 0, 0)
	assert.Empty(t, pod.Spec.Containers)
}

func TestAddGroveEnvironmentVariables_MultipleContainers(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "container1",
					Image: "image1",
				},
				{
					Name:  "container2",
					Image: "image2",
					Env: []corev1.EnvVar{
						{Name: "EXISTING_VAR", Value: "existing-value"},
					},
				},
			},
		},
	}
	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pclq",
			Namespace: "test-ns",
		},
	}

	addEnvironmentVariables(pod, pclq, "test-pcs", 0, 0)

	// Both containers should have Grove environment variables
	expectedEnvVars := []string{
		constants.EnvVarPodCliqueSetName,
		constants.EnvVarPodCliqueSetIndex,
		constants.EnvVarPodCliqueName,
		constants.EnvVarHeadlessService,
	}

	for _, container := range pod.Spec.Containers {
		assertExpectedEnvVars(t, container, expectedEnvVars)
		assertNoDuplicateEnvVars(t, container)
	}

	// Second container should preserve existing environment variable
	envVarNames := make(map[string]bool)
	for _, env := range pod.Spec.Containers[1].Env {
		envVarNames[env.Name] = true
	}
	assert.True(t, envVarNames["EXISTING_VAR"], "existing environment variable should be preserved")
}

// Helper functions
// -------------------------------------------------------------------------------------------

// assertExpectedEnvVars asserts that the expected environment variables are present.
func assertExpectedEnvVars(t *testing.T, container corev1.Container, expectedEnvVars []string) {
	envVarNames := make(map[string]bool)
	for _, env := range container.Env {
		envVarNames[env.Name] = true
	}
	for _, expectedEnv := range expectedEnvVars {
		assert.True(t, envVarNames[expectedEnv], "expected environment variable %s not found in container %s", expectedEnv, container.Name)
	}
}

// assertGroveEnvVarsDirectValues asserts Grove environment variables have direct values.
func assertGroveEnvVarsDirectValues(t *testing.T, container corev1.Container, groveEnvVars []string) {
	// Create a set of Grove env vars for quick lookup
	groveEnvVarSet := make(map[string]bool)
	for _, envVar := range groveEnvVars {
		groveEnvVarSet[envVar] = true
	}

	for _, env := range container.Env {
		// Only validate Grove environment variables
		if groveEnvVarSet[env.Name] {
			assert.NotEmpty(t, env.Value, "Grove environment variable %s should have a direct value", env.Name)
			assert.Nil(t, env.ValueFrom, "Grove environment variable %s should not use ValueFrom (Downward API)", env.Name)
		}
	}
}

// Helper function to assert replaced environment variables use correct Downward API
func assertReplacedEnvVars(t *testing.T, container corev1.Container, shouldReplace map[string]string) {
	if shouldReplace == nil {
		return
	}

	envVarNames := make(map[string]corev1.EnvVar)
	for _, env := range container.Env {
		envVarNames[env.Name] = env
	}

	for envName, expectedValue := range shouldReplace {
		envVar, found := envVarNames[envName]
		assert.True(t, found, "environment variable %s should exist", envName)
		if found {
			assert.Equal(t, expectedValue, envVar.Value,
				"environment variable %s has wrong value", envName)
		}
	}
}

// Helper function to assert preserved environment variables maintain their values
func assertPreservedEnvVars(t *testing.T, container corev1.Container, shouldPreserve []string) {
	if shouldPreserve == nil {
		return
	}

	envVarNames := make(map[string]corev1.EnvVar)
	for _, env := range container.Env {
		envVarNames[env.Name] = env
	}

	for _, preserveEnv := range shouldPreserve {
		envVar, found := envVarNames[preserveEnv]
		assert.True(t, found, "expected preserved environment variable %s not found in container %s", preserveEnv, container.Name)
		if found {
			assert.NotEmpty(t, envVar.Value, "preserved environment variable %s should have its original value", preserveEnv)
		}
	}
}

// Helper function to assert no duplicate environment variables
func assertNoDuplicateEnvVars(t *testing.T, container corev1.Container) {
	envVarCounts := make(map[string]int)
	for _, env := range container.Env {
		envVarCounts[env.Name]++
	}
	for envName, count := range envVarCounts {
		assert.Equal(t, 1, count, "environment variable %s appears %d times (should be 1)", envName, count)
	}
}

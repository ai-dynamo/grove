// /*
// Copyright 2025 The Grove Authors.
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

package utils

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestGetPCLQPods tests listing pods that belong to a PodClique.
func TestGetPCLQPods(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = grovecorev1alpha1.AddToScheme(scheme)
	
	// Test with owned pods
	t.Run("returns owned pods only", func(t *testing.T) {
		pclq := &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pclq",
				Namespace: "default",
				UID:       "pclq-uid-123",
			},
		}
		
		// Pod owned by the PodClique
		ownedPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owned-pod",
				Namespace: "default",
				Labels: map[string]string{
					apicommon.LabelPartOfKey:   "test-pcs",
					apicommon.LabelPodClique:   "test-pclq",
					apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "grove.ai-dynamo.io/v1alpha1",
						Kind:       "PodClique",
						Name:       "test-pclq",
						UID:        "pclq-uid-123",
						Controller: ptr(true),
					},
				},
			},
		}
		
		// Pod with matching labels but not owned
		notOwnedPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "not-owned-pod",
				Namespace: "default",
				Labels: map[string]string{
					apicommon.LabelPartOfKey:   "test-pcs",
					apicommon.LabelPodClique:   "test-pclq",
					apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
				},
			},
		}
		
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(ownedPod, notOwnedPod).
			Build()
		
		pods, err := GetPCLQPods(context.Background(), cl, "test-pcs", pclq)
		
		require.NoError(t, err)
		assert.Len(t, pods, 1)
		assert.Equal(t, "owned-pod", pods[0].Name)
	})

	// Test with no pods
	t.Run("no pods", func(t *testing.T) {
		pclq := &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pclq",
				Namespace: "default",
				UID:       "pclq-uid-123",
			},
		}
		
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()
		
		pods, err := GetPCLQPods(context.Background(), cl, "test-pcs", pclq)
		
		require.NoError(t, err)
		assert.Empty(t, pods)
	})

	// Test with multiple owned pods
	t.Run("multiple owned pods", func(t *testing.T) {
		pclq := &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pclq",
				Namespace: "default",
				UID:       "pclq-uid-123",
			},
		}
		
		pods := []*corev1.Pod{}
		for i := 0; i < 3; i++ {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod-" + string(rune('a'+i)),
					Namespace: "default",
					Labels: map[string]string{
						apicommon.LabelPartOfKey:   "test-pcs",
						apicommon.LabelPodClique:   "test-pclq",
						apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "grove.ai-dynamo.io/v1alpha1",
							Kind:       "PodClique",
							Name:       "test-pclq",
							UID:        "pclq-uid-123",
							Controller: ptr(true),
						},
					},
				},
			}
			pods = append(pods, pod)
		}
		
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(pods[0], pods[1], pods[2]).
			Build()
		
		result, err := GetPCLQPods(context.Background(), cl, "test-pcs", pclq)
		
		require.NoError(t, err)
		assert.Len(t, result, 3)
	})
}

// TestAddEnvVarsToContainers tests adding environment variables to containers.
func TestAddEnvVarsToContainers(t *testing.T) {
	// Test adding to empty containers
	t.Run("add to empty containers", func(t *testing.T) {
		containers := []corev1.Container{
			{Name: "container1"},
			{Name: "container2"},
		}
		
		envVars := []corev1.EnvVar{
			{Name: "VAR1", Value: "value1"},
			{Name: "VAR2", Value: "value2"},
		}
		
		AddEnvVarsToContainers(containers, envVars)
		
		assert.Len(t, containers[0].Env, 2)
		assert.Len(t, containers[1].Env, 2)
		assert.Equal(t, "VAR1", containers[0].Env[0].Name)
		assert.Equal(t, "value1", containers[0].Env[0].Value)
	})

	// Test adding to containers with existing env vars
	t.Run("add to containers with existing env", func(t *testing.T) {
		containers := []corev1.Container{
			{
				Name: "container1",
				Env: []corev1.EnvVar{
					{Name: "EXISTING", Value: "existing-value"},
				},
			},
		}
		
		newEnvVars := []corev1.EnvVar{
			{Name: "NEW_VAR", Value: "new-value"},
		}
		
		AddEnvVarsToContainers(containers, newEnvVars)
		
		assert.Len(t, containers[0].Env, 2)
		assert.Equal(t, "EXISTING", containers[0].Env[0].Name)
		assert.Equal(t, "NEW_VAR", containers[0].Env[1].Name)
	})

	// Test with no env vars to add
	t.Run("add empty env vars", func(t *testing.T) {
		containers := []corev1.Container{
			{Name: "container1"},
		}
		
		AddEnvVarsToContainers(containers, []corev1.EnvVar{})
		
		assert.Empty(t, containers[0].Env)
	})

	// Test with no containers
	t.Run("no containers", func(t *testing.T) {
		containers := []corev1.Container{}
		envVars := []corev1.EnvVar{
			{Name: "VAR1", Value: "value1"},
		}
		
		// Should not panic
		AddEnvVarsToContainers(containers, envVars)
	})
}

// TestPodsToObjectNames tests converting pods to object name strings.
func TestPodsToObjectNames(t *testing.T) {
	// Test with multiple pods
	t.Run("multiple pods", func(t *testing.T) {
		pods := []*corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod1",
					Namespace: "default",
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pod2",
					Namespace: "production",
				},
			},
		}
		
		names := PodsToObjectNames(pods)
		
		assert.Len(t, names, 2)
		assert.Contains(t, names, "default/pod1")
		assert.Contains(t, names, "production/pod2")
	})

	// Test with empty slice
	t.Run("empty slice", func(t *testing.T) {
		pods := []*corev1.Pod{}
		
		names := PodsToObjectNames(pods)
		
		assert.Empty(t, names)
	})

	// Test with single pod
	t.Run("single pod", func(t *testing.T) {
		pods := []*corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-pod",
					Namespace: "my-namespace",
				},
			},
		}
		
		names := PodsToObjectNames(pods)
		
		assert.Len(t, names, 1)
		assert.Equal(t, "my-namespace/my-pod", names[0])
	})
}

// ptr is a helper function to get a pointer to a bool
func ptr(b bool) *bool {
	return &b
}

// /*
// Copyright 2025 The Grove Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License")
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

package internal

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// TestGetLabelSelectorForPods verifies the label selector generation for pod filtering.
func TestGetLabelSelectorForPods(t *testing.T) {
	tests := []struct {
		// Test case name for identifying failures
		name string
		// Input PodGang name to generate selector for
		podGangName string
		// Expected label selector map
		expected map[string]string
	}{
		{
			// Basic PodGang name should generate correct selector
			name:        "basic_podgang_name",
			podGangName: "my-podgang",
			expected: map[string]string{
				apicommon.LabelPodGang: "my-podgang",
			},
		},
		{
			// Empty PodGang name should still generate valid selector
			name:        "empty_podgang_name",
			podGangName: "",
			expected: map[string]string{
				apicommon.LabelPodGang: "",
			},
		},
		{
			// PodGang name with special characters should be preserved
			name:        "podgang_name_with_special_chars",
			podGangName: "my-podgang-123_test",
			expected: map[string]string{
				apicommon.LabelPodGang: "my-podgang-123_test",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getLabelSelectorForPods(tt.podGangName)
			assert.Equal(t, tt.expected, result, "Label selector should match expected result")
		})
	}
}

// TestCheckAllParentsReady tests the readiness checking logic.
func TestCheckAllParentsReady(t *testing.T) {
	tests := []struct {
		// Test case name for identifying failures
		name string
		// Minimum required pods per PodClique
		pclqFQNToMinAvailable map[string]int
		// Currently ready pods per PodClique (podclique name -> set of pod names)
		currentPCLQReadyPods map[string]sets.Set[string]
		// Expected readiness result
		expected bool
	}{
		{
			// All dependencies met should return true
			name: "all_dependencies_met",
			pclqFQNToMinAvailable: map[string]int{
				"podclique-a": 2,
				"podclique-b": 1,
			},
			currentPCLQReadyPods: map[string]sets.Set[string]{
				"podclique-a": sets.New("pod-a-1", "pod-a-2"),
				"podclique-b": sets.New("pod-b-1"),
			},
			expected: true,
		},
		{
			// Exceeding minimum requirements should return true
			name: "exceeding_requirements",
			pclqFQNToMinAvailable: map[string]int{
				"podclique-a": 1,
			},
			currentPCLQReadyPods: map[string]sets.Set[string]{
				"podclique-a": sets.New("pod-a-1", "pod-a-2", "pod-a-3"),
			},
			expected: true,
		},
		{
			// One dependency not met should return false
			name: "one_dependency_not_met",
			pclqFQNToMinAvailable: map[string]int{
				"podclique-a": 2,
				"podclique-b": 2,
			},
			currentPCLQReadyPods: map[string]sets.Set[string]{
				"podclique-a": sets.New("pod-a-1", "pod-a-2"),
				"podclique-b": sets.New("pod-b-1"), // Only 1 pod ready, need 2
			},
			expected: false,
		},
		{
			// No ready pods should return false when pods required
			name: "no_ready_pods",
			pclqFQNToMinAvailable: map[string]int{
				"podclique-a": 1,
			},
			currentPCLQReadyPods: map[string]sets.Set[string]{
				"podclique-a": sets.New[string](), // Empty set
			},
			expected: false,
		},
		{
			// Single pod requirement should be met with one ready pod
			name: "single_pod_requirement",
			pclqFQNToMinAvailable: map[string]int{
				"podclique-a": 1,
			},
			currentPCLQReadyPods: map[string]sets.Set[string]{
				"podclique-a": sets.New("pod-a-1"),
			},
			expected: true,
		},
		{
			// Empty dependencies should return true
			name:                  "no_dependencies",
			pclqFQNToMinAvailable: map[string]int{},
			currentPCLQReadyPods:  map[string]sets.Set[string]{},
			expected:              true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &ParentPodCliqueDependencies{
				pclqFQNToMinAvailable: tt.pclqFQNToMinAvailable,
				currentPCLQReadyPods:  tt.currentPCLQReadyPods,
				allReadyCh:            make(chan struct{}, 1),
			}

			deps.notifyIfAllParentsReady()

			result := false
			select {
			case <-deps.allReadyCh:
				result = true
			default:
			}

			assert.Equal(t, tt.expected, result, "Readiness check should match expected result")
		})
	}
}

// TestRefreshReadyPodsOfPodClique tests pod readiness tracking.
func TestRefreshReadyPodsOfPodClique(t *testing.T) {
	tests := []struct {
		// Test case name for identifying failures
		name string
		// Initial state of ready pods
		initialReadyPods map[string]sets.Set[string]
		// Pod object to process
		pod *corev1.Pod
		// Whether this is a deletion event
		deletionEvent bool
		// Expected state after processing
		expectedReadyPods map[string]sets.Set[string]
	}{
		{
			// Ready pod should be added to tracking
			name: "add_ready_pod",
			initialReadyPods: map[string]sets.Set[string]{
				"podclique-a": sets.New[string](),
			},
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "podclique-a-pod-1",
				},
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			deletionEvent: false,
			expectedReadyPods: map[string]sets.Set[string]{
				"podclique-a": sets.New("podclique-a-pod-1"),
			},
		},
		{
			// Non-ready pod should not be added to tracking
			name: "add_non_ready_pod",
			initialReadyPods: map[string]sets.Set[string]{
				"podclique-a": sets.New[string](),
			},
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "podclique-a-pod-1",
				},
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			deletionEvent: false,
			expectedReadyPods: map[string]sets.Set[string]{
				"podclique-a": sets.New[string](),
			},
		},
		{
			// Deletion event should remove pod from tracking
			name: "delete_pod",
			initialReadyPods: map[string]sets.Set[string]{
				"podclique-a": sets.New("podclique-a-pod-1", "podclique-a-pod-2"),
			},
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "podclique-a-pod-1",
				},
			},
			deletionEvent: true,
			expectedReadyPods: map[string]sets.Set[string]{
				"podclique-a": sets.New("podclique-a-pod-2"),
			},
		},
		{
			// Pod not belonging to tracked PodClique should not affect state
			name: "untracked_podclique",
			initialReadyPods: map[string]sets.Set[string]{
				"podclique-a": sets.New[string](),
			},
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "podclique-b-pod-1",
				},
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			deletionEvent: false,
			expectedReadyPods: map[string]sets.Set[string]{
				"podclique-a": sets.New[string](),
			},
		},
		{
			// Pod changing from ready to not ready should be removed
			name: "pod_becomes_not_ready",
			initialReadyPods: map[string]sets.Set[string]{
				"podclique-a": sets.New("podclique-a-pod-1"),
			},
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "podclique-a-pod-1",
				},
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			deletionEvent: false,
			expectedReadyPods: map[string]sets.Set[string]{
				"podclique-a": sets.New[string](),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &ParentPodCliqueDependencies{
				pclqFQNToMinAvailable: map[string]int{
					"podclique-a": 1,
				},
				currentPCLQReadyPods: tt.initialReadyPods,
			}

			deps.refreshReadyPodsOfPodClique(tt.pod, tt.deletionEvent)

			assert.Equal(t, tt.expectedReadyPods, deps.currentPCLQReadyPods, "Ready pods state should match expected")
		})
	}
}

// TestNewPodCliqueStateWithInfo tests the core initialization logic without file dependencies.
func TestNewPodCliqueStateWithInfo(t *testing.T) {
	tests := []struct {
		// Test case name for identifying failures
		name string
		// Pod clique dependencies to initialize with
		dependencies map[string]int
		// Namespace to set
		namespace string
		// PodGang name to set
		podGang string
	}{
		{
			// Basic initialization with multiple dependencies
			name: "basic_initialization",
			dependencies: map[string]int{
				"podclique-a": 2,
				"podclique-b": 3,
			},
			namespace: "test-namespace",
			podGang:   "test-podgang",
		},
		{
			// Empty dependencies should work
			name:         "empty_dependencies",
			dependencies: map[string]int{},
			namespace:    "test-namespace",
			podGang:      "test-podgang",
		},
		{
			// Empty namespace and podgang should work
			name: "empty_namespace_podgang",
			dependencies: map[string]int{
				"podclique-x": 1,
			},
			namespace: "",
			podGang:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := newPodCliqueStateWithInfo(tt.dependencies, tt.namespace, tt.podGang)

			require.NotNil(t, result, "Result should not be nil")
			assert.Equal(t, tt.namespace, result.namespace, "Namespace should match")
			assert.Equal(t, tt.podGang, result.podGang, "PodGang should match")
			assert.Equal(t, tt.dependencies, result.pclqFQNToMinAvailable, "Dependencies should match")

			// Verify ready pods map is initialized for all dependencies
			assert.Len(t, result.currentPCLQReadyPods, len(tt.dependencies), "Ready pods map should have entry for each dependency")
			for depName := range tt.dependencies {
				readySet, exists := result.currentPCLQReadyPods[depName]
				assert.True(t, exists, "Ready pods set should exist for dependency %s", depName)
				assert.Equal(t, 0, readySet.Len(), "Ready pods set should be empty initially for %s", depName)
			}
		})
	}
}

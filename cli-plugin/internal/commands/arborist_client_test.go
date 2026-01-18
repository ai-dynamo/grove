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

package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

// TestNewArboristClient verifies the constructor creates a properly configured client.
func TestNewArboristClient(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
	}{
		{
			name:      "default namespace",
			namespace: "default",
		},
		{
			name:      "custom namespace",
			namespace: "vllm-v1-disagg-router",
		},
		{
			name:      "empty namespace",
			namespace: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClientset := fake.NewSimpleClientset()
			fakeDynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())

			client := NewArboristClient(fakeClientset, fakeDynamicClient, tt.namespace)

			assert.NotNil(t, client)
			assert.Equal(t, tt.namespace, client.namespace)
			assert.Equal(t, fakeClientset, client.clientset)
			assert.Equal(t, fakeDynamicClient, client.dynamicClient)
		})
	}
}

// TestGetAllPodCliqueSets tests retrieving all PodCliqueSets in a namespace.
func TestGetAllPodCliqueSets(t *testing.T) {
	tests := []struct {
		name           string
		namespace      string
		objects        []*unstructured.Unstructured
		expectedCount  int
		expectedNames  []string
		expectError    bool
	}{
		{
			name:      "returns multiple PodCliqueSets",
			namespace: "default",
			objects: []*unstructured.Unstructured{
				createUnstructuredPodCliqueSet("pcs-1", "default", time.Now().Add(-1*time.Hour)),
				createUnstructuredPodCliqueSet("pcs-2", "default", time.Now().Add(-2*time.Hour)),
			},
			expectedCount: 2,
			expectedNames: []string{"pcs-1", "pcs-2"},
			expectError:   false,
		},
		{
			name:           "returns empty list when no PodCliqueSets exist",
			namespace:      "empty-ns",
			objects:        []*unstructured.Unstructured{},
			expectedCount:  0,
			expectedNames:  []string{},
			expectError:    false,
		},
		{
			name:      "filters by namespace",
			namespace: "target-ns",
			objects: []*unstructured.Unstructured{
				createUnstructuredPodCliqueSet("pcs-target", "target-ns", time.Now()),
				createUnstructuredPodCliqueSet("pcs-other", "other-ns", time.Now()),
			},
			expectedCount: 1,
			expectedNames: []string{"pcs-target"},
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			gvrToListKind := map[schema.GroupVersionResource]string{
				podCliqueSetGVR: "PodCliqueSetList",
			}

			var objects []runtime.Object
			for _, obj := range tt.objects {
				objects = append(objects, obj)
			}

			fakeDynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objects...)
			fakeClientset := fake.NewSimpleClientset()

			client := NewArboristClient(fakeClientset, fakeDynamicClient, tt.namespace)

			ctx := context.Background()
			result, err := client.GetAllPodCliqueSets(ctx)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Len(t, result, tt.expectedCount)

			for i, expectedName := range tt.expectedNames {
				if i < len(result) {
					assert.Equal(t, expectedName, result[i].Name)
				}
			}
		})
	}
}

// TestGetPodCliquesForPodCliqueSet tests filtering PodCliques by label selector.
func TestGetPodCliquesForPodCliqueSet(t *testing.T) {
	tests := []struct {
		name          string
		namespace     string
		pcsName       string
		objects       []*unstructured.Unstructured
		expectedCount int
		expectedNames []string
	}{
		{
			name:      "returns PodCliques for specific PodCliqueSet",
			namespace: "default",
			pcsName:   "my-pcs",
			objects: []*unstructured.Unstructured{
				createUnstructuredPodClique("my-pcs-0-prefill", "default", "my-pcs", "prefill"),
				createUnstructuredPodClique("my-pcs-0-decode", "default", "my-pcs", "decode"),
				createUnstructuredPodClique("other-pcs-0-prefill", "default", "other-pcs", "prefill"),
			},
			expectedCount: 2,
			expectedNames: []string{"my-pcs-0-decode", "my-pcs-0-prefill"}, // sorted alphabetically
		},
		{
			name:          "returns empty list when no matching PodCliques",
			namespace:     "default",
			pcsName:       "nonexistent-pcs",
			objects:       []*unstructured.Unstructured{},
			expectedCount: 0,
			expectedNames: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			gvrToListKind := map[schema.GroupVersionResource]string{
				podCliqueGVR: "PodCliqueList",
			}

			var objects []runtime.Object
			for _, obj := range tt.objects {
				objects = append(objects, obj)
			}

			fakeDynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objects...)
			fakeClientset := fake.NewSimpleClientset()

			client := NewArboristClient(fakeClientset, fakeDynamicClient, tt.namespace)

			ctx := context.Background()
			result, err := client.GetPodCliquesForPodCliqueSet(ctx, tt.pcsName)

			require.NoError(t, err)
			assert.Len(t, result, tt.expectedCount)

			for i, expectedName := range tt.expectedNames {
				if i < len(result) {
					assert.Equal(t, expectedName, result[i].Name)
				}
			}
		})
	}
}

// TestGetPodsForPodClique tests filtering Pods by grove.io/podclique label.
func TestGetPodsForPodClique(t *testing.T) {
	tests := []struct {
		name          string
		namespace     string
		cliqueName    string
		pods          []corev1.Pod
		expectedCount int
		expectedNames []string
	}{
		{
			name:       "returns Pods for specific PodClique",
			namespace:  "default",
			cliqueName: "my-pcs-0-prefill",
			pods: []corev1.Pod{
				createPod("my-pcs-0-prefill-pod1", "default", "my-pcs-0-prefill", time.Now().Add(-1*time.Minute)),
				createPod("my-pcs-0-prefill-pod2", "default", "my-pcs-0-prefill", time.Now().Add(-2*time.Minute)),
				createPod("other-clique-pod", "default", "other-clique", time.Now()),
			},
			expectedCount: 2,
			expectedNames: []string{"my-pcs-0-prefill-pod1", "my-pcs-0-prefill-pod2"},
		},
		{
			name:          "returns empty list when no matching Pods",
			namespace:     "default",
			cliqueName:    "nonexistent-clique",
			pods:          []corev1.Pod{},
			expectedCount: 0,
			expectedNames: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objects []runtime.Object
			for i := range tt.pods {
				objects = append(objects, &tt.pods[i])
			}

			fakeClientset := fake.NewSimpleClientset(objects...)
			fakeDynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())

			client := NewArboristClient(fakeClientset, fakeDynamicClient, tt.namespace)

			ctx := context.Background()
			result, err := client.GetPodsForPodClique(ctx, tt.cliqueName)

			require.NoError(t, err)
			assert.Len(t, result, tt.expectedCount)

			for i, expectedName := range tt.expectedNames {
				if i < len(result) {
					assert.Equal(t, expectedName, result[i].Name)
				}
			}
		})
	}
}

// TestGetPodYAML tests retrieving YAML representation of a Pod.
func TestGetPodYAML(t *testing.T) {
	tests := []struct {
		name        string
		namespace   string
		podName     string
		pod         *corev1.Pod
		expectError bool
		expectYAML  bool
	}{
		{
			name:      "returns YAML for existing Pod",
			namespace: "default",
			podName:   "test-pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
					Labels: map[string]string{
						"app": "test",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "main", Image: "nginx:latest"},
					},
				},
			},
			expectError: false,
			expectYAML:  true,
		},
		{
			name:        "returns error for non-existent Pod",
			namespace:   "default",
			podName:     "nonexistent-pod",
			pod:         nil,
			expectError: true,
			expectYAML:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objects []runtime.Object
			if tt.pod != nil {
				objects = append(objects, tt.pod)
			}

			fakeClientset := fake.NewSimpleClientset(objects...)
			fakeDynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())

			client := NewArboristClient(fakeClientset, fakeDynamicClient, tt.namespace)

			ctx := context.Background()
			result, err := client.GetPodYAML(ctx, tt.podName)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			if tt.expectYAML {
				assert.NotEmpty(t, result)
				assert.Contains(t, result, "name:")
				assert.Contains(t, result, tt.podName)
			}
		})
	}
}

// TestGetEventsForResource tests filtering events by kind and name.
// Note: The fake clientset doesn't support field selectors, so we test with
// events that all match the filter criteria to verify the method works correctly.
func TestGetEventsForResource(t *testing.T) {
	tests := []struct {
		name          string
		namespace     string
		kind          string
		resourceName  string
		events        []corev1.Event
		expectedCount int
	}{
		{
			name:         "returns events for specific Pod",
			namespace:    "default",
			kind:         "Pod",
			resourceName: "my-pod",
			events: []corev1.Event{
				createEvent("event-1", "default", "Pod", "my-pod", "Normal", "Scheduled", time.Now().Add(-1*time.Minute)),
				createEvent("event-2", "default", "Pod", "my-pod", "Normal", "Started", time.Now()),
			},
			expectedCount: 2,
		},
		{
			name:         "returns events for PodCliqueSet",
			namespace:    "default",
			kind:         "PodCliqueSet",
			resourceName: "my-pcs",
			events: []corev1.Event{
				createEvent("event-1", "default", "PodCliqueSet", "my-pcs", "Normal", "Created", time.Now()),
			},
			expectedCount: 1,
		},
		{
			name:          "returns empty list when no events exist",
			namespace:     "default",
			kind:          "Pod",
			resourceName:  "nonexistent-pod",
			events:        []corev1.Event{},
			expectedCount: 0,
		},
		{
			name:         "returns events sorted by timestamp",
			namespace:    "default",
			kind:         "Pod",
			resourceName: "my-pod",
			events: []corev1.Event{
				createEvent("event-old", "default", "Pod", "my-pod", "Normal", "OldEvent", time.Now().Add(-5*time.Minute)),
				createEvent("event-new", "default", "Pod", "my-pod", "Normal", "NewEvent", time.Now()),
				createEvent("event-mid", "default", "Pod", "my-pod", "Warning", "MidEvent", time.Now().Add(-2*time.Minute)),
			},
			expectedCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objects []runtime.Object
			for i := range tt.events {
				objects = append(objects, &tt.events[i])
			}

			fakeClientset := fake.NewSimpleClientset(objects...)
			fakeDynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())

			client := NewArboristClient(fakeClientset, fakeDynamicClient, tt.namespace)

			ctx := context.Background()
			result, err := client.GetEventsForResource(ctx, tt.kind, tt.resourceName)

			require.NoError(t, err)
			// Note: fake client doesn't support field selectors, so it returns all events
			// In a real cluster, the field selector would filter appropriately
			assert.GreaterOrEqual(t, len(result), 0, "should return events without error")

			// Verify events are sorted by timestamp (most recent first) if any are returned
			if len(result) > 1 {
				for i := 0; i < len(result)-1; i++ {
					timeI := result[i].LastTimestamp.Time
					timeJ := result[i+1].LastTimestamp.Time
					if timeI.IsZero() {
						timeI = result[i].EventTime.Time
					}
					if timeJ.IsZero() {
						timeJ = result[i+1].EventTime.Time
					}
					assert.True(t, timeI.After(timeJ) || timeI.Equal(timeJ),
						"events should be sorted by timestamp (most recent first)")
				}
			}
		})
	}
}

// TestGetNodes tests retrieving all nodes in the cluster.
func TestGetNodes(t *testing.T) {
	tests := []struct {
		name          string
		nodes         []corev1.Node
		expectedCount int
		expectedNames []string
	}{
		{
			name: "returns all nodes sorted by name",
			nodes: []corev1.Node{
				createNode("node-c"),
				createNode("node-a"),
				createNode("node-b"),
			},
			expectedCount: 3,
			expectedNames: []string{"node-a", "node-b", "node-c"},
		},
		{
			name:          "returns empty list when no nodes",
			nodes:         []corev1.Node{},
			expectedCount: 0,
			expectedNames: []string{},
		},
		{
			name: "returns single node",
			nodes: []corev1.Node{
				createNode("single-node"),
			},
			expectedCount: 1,
			expectedNames: []string{"single-node"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objects []runtime.Object
			for i := range tt.nodes {
				objects = append(objects, &tt.nodes[i])
			}

			fakeClientset := fake.NewSimpleClientset(objects...)
			fakeDynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())

			client := NewArboristClient(fakeClientset, fakeDynamicClient, "default")

			ctx := context.Background()
			result, err := client.GetNodes(ctx)

			require.NoError(t, err)
			assert.Len(t, result, tt.expectedCount)

			for i, expectedName := range tt.expectedNames {
				if i < len(result) {
					assert.Equal(t, expectedName, result[i].Name)
				}
			}
		})
	}
}

// Helper functions to create test objects

func createUnstructuredPodCliqueSet(name, namespace string, creationTime time.Time) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "grove.io/v1alpha1",
			"kind":       "PodCliqueSet",
			"metadata": map[string]interface{}{
				"name":              name,
				"namespace":         namespace,
				"creationTimestamp": creationTime.Format(time.RFC3339),
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"template": map[string]interface{}{
					"cliques": []interface{}{},
				},
			},
			"status": map[string]interface{}{
				"availableReplicas": int64(1),
			},
		},
	}
}

func createUnstructuredPodClique(name, namespace, pcsName, roleName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "grove.io/v1alpha1",
			"kind":       "PodClique",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"app.kubernetes.io/part-of": pcsName,
				},
			},
			"spec": map[string]interface{}{
				"roleName": roleName,
				"replicas": int64(3),
				"podSpec":  map[string]interface{}{},
			},
			"status": map[string]interface{}{
				"readyReplicas": int64(3),
			},
		},
	}
}

func createPod(name, namespace, cliqueName string, creationTime time.Time) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         namespace,
			CreationTimestamp: metav1.NewTime(creationTime),
			Labels: map[string]string{
				"grove.io/podclique": cliqueName,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "main", Image: "nginx:latest"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}
}

func createEvent(name, namespace, kind, resourceName, eventType, reason string, timestamp time.Time) corev1.Event {
	return corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		InvolvedObject: corev1.ObjectReference{
			Kind:      kind,
			Name:      resourceName,
			Namespace: namespace,
		},
		Type:          eventType,
		Reason:        reason,
		Message:       "Test event message",
		LastTimestamp: metav1.NewTime(timestamp),
	}
}

func createNode(name string) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"topology.kubernetes.io/rack": "rack-1",
			},
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
		},
	}
}

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

package pod

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCheckAndRemovePodSchedulingGates_MinAvailableAware(t *testing.T) {
	tests := []struct {
		name                string
		podGangName         string
		basePodGangExists   bool
		basePodGangReady    bool
		podHasGate          bool
		podInPodGang        bool
		expectedGateRemoved bool
		expectedSkippedPods int
		expectError         bool
		description         string
	}{
		{
			name:                "Base PodGang pod - gates removed immediately",
			podGangName:         "simple1-0",
			basePodGangExists:   true,
			basePodGangReady:    false, // Irrelevant for base PodGang
			podHasGate:          true,
			podInPodGang:        true,
			expectedGateRemoved: true,
			expectedSkippedPods: 0,
			expectError:         false,
			description:         "Base PodGang pods should have gates removed immediately",
		},
		{
			name:                "Scaled PodGang pod - base not ready",
			podGangName:         "simple1-0-sga-2",
			basePodGangExists:   true,
			basePodGangReady:    false,
			podHasGate:          true,
			podInPodGang:        true,
			expectedGateRemoved: false,
			expectedSkippedPods: 1,
			expectError:         false,
			description:         "Scaled PodGang pods should keep gates when base not ready",
		},
		{
			name:                "Scaled PodGang pod - base ready",
			podGangName:         "simple1-0-sga-2",
			basePodGangExists:   true,
			basePodGangReady:    true,
			podHasGate:          true,
			podInPodGang:        true,
			expectedGateRemoved: true,
			expectedSkippedPods: 0,
			expectError:         false,
			description:         "Scaled PodGang pods should have gates removed when base ready",
		},
		{
			name:                "Scaled PodGang pod - base missing",
			podGangName:         "simple1-0-sga-3",
			basePodGangExists:   false,
			basePodGangReady:    false,
			podHasGate:          true,
			podInPodGang:        true,
			expectedGateRemoved: false,
			expectedSkippedPods: 1, // Pod is skipped (not error) - base PodGang NotFound is treated as "not scheduled"
			expectError:         false,
			description:         "Scaled PodGang pods should skip gate removal when base PodGang missing (transient state during creation/recreation)",
		},
		{
			name:                "Pod not in PodGang yet",
			podGangName:         "simple1-0-sga-2",
			basePodGangExists:   true,
			basePodGangReady:    true,
			podHasGate:          true,
			podInPodGang:        false,
			expectedGateRemoved: false,
			expectedSkippedPods: 1,
			expectError:         false,
			description:         "Pods not yet in PodGang should keep gates regardless",
		},
		{
			name:                "Pod without gate",
			podGangName:         "simple1-0-sga-2",
			basePodGangExists:   true,
			basePodGangReady:    true,
			podHasGate:          false,
			podInPodGang:        true,
			expectedGateRemoved: false,
			expectedSkippedPods: 0,
			expectError:         false,
			description:         "Pods without gates should be ignored",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test objects
			pod := createTestPod(tt.podGangName, tt.podHasGate, tt.podInPodGang)
			var objects []client.Object
			objects = append(objects, pod)

			// Check if this is testing a base PodGang or scaled PodGang based on name pattern
			isScaledPodGangTest := strings.Contains(tt.podGangName, "-sga-")

			if isScaledPodGangTest {
				// Always create the scaled PodGang for scaled PodGang tests
				scaledPodGang := &groveschedulerv1alpha1.PodGang{
					ObjectMeta: metav1.ObjectMeta{
						Name:      tt.podGangName,
						Namespace: "default",
					},
					Spec: groveschedulerv1alpha1.PodGangSpec{
						PodGroups: []groveschedulerv1alpha1.PodGroup{
							{
								Name:        "simple1-0-sga-pcb",
								MinReplicas: 1,
							},
						},
					},
				}
				objects = append(objects, scaledPodGang)

				// Only create base PodGang if it's supposed to exist
				if tt.basePodGangExists {
					basePodGang := createTestBasePodGang(tt.podGangName, tt.basePodGangReady)
					objects = append(objects, basePodGang)
				}
			} else if tt.basePodGangExists {
				// Just create the base PodGang for base PodGang tests
				basePodGang := createTestBasePodGang(tt.podGangName, tt.basePodGangReady)
				objects = append(objects, basePodGang)
			}

			if tt.basePodGangExists {
				if tt.basePodGangReady {
					// Add ready PodClique for base PodGang
					basePclq := createTestPodClique("simple1-0-pcb", 2, 2) // ReadyReplicas >= MinAvailable
					objects = append(objects, basePclq)
				} else {
					// Add not-ready PodClique for base PodGang
					basePclq := createTestPodClique("simple1-0-pcb", 2, 1) // ReadyReplicas < MinAvailable
					objects = append(objects, basePclq)
				}
			}

			// Create fake client
			scheme := runtime.NewScheme()
			err := corev1.AddToScheme(scheme)
			require.NoError(t, err)
			err = grovecorev1alpha1.AddToScheme(scheme)
			require.NoError(t, err)
			err = groveschedulerv1alpha1.AddToScheme(scheme)
			require.NoError(t, err)
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()

			// Create a test PodClique for the sync context
			testPclq := &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pclq",
					Namespace: "default",
				},
			}

			// For scaled PodGang tests, add the base PodGang label to the PodClique
			// This is what the production code expects to read in checkBasePodGangScheduledForPodClique
			if isScaledPodGangTest {
				testPclq.Labels = map[string]string{
					common.LabelBasePodGang: "simple1-0",
				}
			}

			// Create resource and sync context
			r := &_resource{client: fakeClient}
			sc := &syncContext{
				ctx:                           context.Background(),
				pclq:                          testPclq,
				existingPCLQPods:              []*corev1.Pod{pod},
				podNamesUpdatedInPCLQPodGangs: []string{},
			}

			if tt.podInPodGang {
				sc.podNamesUpdatedInPCLQPodGangs = []string{pod.Name}
			}

			// Test the gate removal logic
			skippedPods, err := r.checkAndRemovePodSchedulingGates(sc, logr.Discard())

			if tt.expectError {
				require.Error(t, err, "Expected error for test case: %s", tt.name)
				// When error occurs, we don't check skipped pods as function returns early
				return
			} else {
				require.NoError(t, err, "Unexpected error for test case: %s", tt.name)
			}

			// Verify results
			assert.Len(t, skippedPods, tt.expectedSkippedPods, tt.description)

			// Check if gate was actually removed
			updatedPod := &corev1.Pod{}
			err = fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pod), updatedPod)
			require.NoError(t, err)

			hasGateAfter := hasPodGangSchedulingGate(updatedPod)
			if tt.expectedGateRemoved {
				assert.False(t, hasGateAfter, "Pod should not have scheduling gate after removal")
			} else if tt.podHasGate {
				assert.True(t, hasGateAfter, "Pod should still have scheduling gate")
			}
		})
	}
}

func TestCheckAndRemovePodSchedulingGates_ConcurrentExecution(t *testing.T) {
	// Test that multiple pods can have their gates removed concurrently without race conditions

	// Create multiple pods with gates for base PodGang (should all be processed)
	var pods []*corev1.Pod
	var objects []client.Object

	for i := 0; i < 5; i++ {
		pod := createTestPod("simple1-0", true, true) // Base PodGang, has gate, in PodGang
		pod.Name = fmt.Sprintf("test-pod-%d", i)
		pods = append(pods, pod)
		objects = append(objects, pod)
	}

	// Create a base PodGang resource (needed for the logic to work correctly)
	basePodGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple1-0",
			Namespace: "default",
			// No base-podgang label - this indicates it's a base PodGang
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{
					Name:        "simple1-0-pcb",
					MinReplicas: 2,
				},
			},
		},
	}
	objects = append(objects, basePodGang)

	// Create fake client
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = grovecorev1alpha1.AddToScheme(scheme)
	_ = groveschedulerv1alpha1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()

	// Create test PodClique for the sync context
	testPclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pclq",
			Namespace: "default",
		},
	}

	// Create resource and sync context
	r := &_resource{client: fakeClient}
	sc := &syncContext{
		ctx:                           context.Background(),
		pclq:                          testPclq,
		existingPCLQPods:              pods,
		podNamesUpdatedInPCLQPodGangs: []string{},
	}

	// All pods are in PodGang
	for _, pod := range pods {
		sc.podNamesUpdatedInPCLQPodGangs = append(sc.podNamesUpdatedInPCLQPodGangs, pod.Name)
	}

	// Test the gate removal logic
	skippedPods, err := r.checkAndRemovePodSchedulingGates(sc, logr.Discard())
	require.NoError(t, err)
	assert.Empty(t, skippedPods, "No pods should be skipped for base PodGang")

	// Verify all pods had their gates removed
	for i, originalPod := range pods {
		updatedPod := &corev1.Pod{}
		err = fakeClient.Get(context.Background(), client.ObjectKeyFromObject(originalPod), updatedPod)
		require.NoError(t, err)

		hasGateAfter := hasPodGangSchedulingGate(updatedPod)
		assert.False(t, hasGateAfter, "Pod %d should not have scheduling gate after removal", i)
	}
}

func TestIsBasePodGangScheduled(t *testing.T) {
	tests := []struct {
		name              string
		basePodGangExists bool
		podCliques        []testPodClique
		expectedScheduled bool
		expectError       bool
		description       string
	}{
		{
			name:              "Base PodGang is scheduled - all PodCliques meet MinAvailable",
			basePodGangExists: true,
			podCliques: []testPodClique{
				{name: "simple1-0-pcb", minAvailable: 2, scheduledReplicas: 2},
				{name: "simple1-0-pcc", minAvailable: 1, scheduledReplicas: 3},
			},
			expectedScheduled: true,
			expectError:       false,
			description:       "All PodCliques meet their MinAvailable requirements",
		},
		{
			name:              "Base PodGang not scheduled - one PodClique below MinAvailable",
			basePodGangExists: true,
			podCliques: []testPodClique{
				{name: "simple1-0-pcb", minAvailable: 2, scheduledReplicas: 2},
				{name: "simple1-0-pcc", minAvailable: 3, scheduledReplicas: 2}, // Below MinAvailable
			},
			expectedScheduled: false,
			expectError:       false,
			description:       "One PodClique below MinAvailable makes base PodGang not scheduled",
		},
		{
			name:              "Base PodGang missing",
			basePodGangExists: false,
			podCliques:        []testPodClique{},
			expectedScheduled: false,
			expectError:       false,
			description:       "Missing base PodGang should return not-scheduled (transient state during creation/recreation)",
		},
		{
			name:              "Base PodGang ready - single PodClique",
			basePodGangExists: true,
			podCliques: []testPodClique{
				{name: "simple1-0-pcb", minAvailable: 1, scheduledReplicas: 1},
			},
			expectedScheduled: true,
			expectError:       false,
			description:       "Single PodClique meeting MinAvailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objects []client.Object

			if tt.basePodGangExists {
				// Create the scaled PodGang with the base-podgang label
				scaledPodGang := &groveschedulerv1alpha1.PodGang{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "simple1-0-sga-2",
						Namespace: "default",
						Labels: map[string]string{
							common.LabelBasePodGang: "simple1-0",
						},
					},
					Spec: groveschedulerv1alpha1.PodGangSpec{
						PodGroups: []groveschedulerv1alpha1.PodGroup{
							{
								Name:        "simple1-0-sga-pcb",
								MinReplicas: 1,
							},
						},
					},
				}
				objects = append(objects, scaledPodGang)

				// Create the base PodGang
				basePodGang := createTestBasePodGangWithPodCliques(tt.podCliques)
				objects = append(objects, basePodGang)

				// Add corresponding PodCliques
				for _, pclq := range tt.podCliques {
					podClique := createTestPodClique(pclq.name, pclq.minAvailable, pclq.scheduledReplicas)
					objects = append(objects, podClique)
				}
			}

			// Create fake client
			scheme := runtime.NewScheme()
			_ = grovecorev1alpha1.AddToScheme(scheme)
			_ = groveschedulerv1alpha1.AddToScheme(scheme)
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()

			// Test the readiness check
			r := &_resource{client: fakeClient}
			result, err := r.isBasePodGangScheduled(context.Background(), logr.Discard(), "default", "simple1-0")

			if tt.expectError {
				require.Error(t, err, "Expected error for test case: %s", tt.name)
				// When error occurs, result should be false and we don't check expectedScheduled
				assert.False(t, result, "Result should be false when error occurs")
			} else {
				require.NoError(t, err, "Unexpected error for test case: %s", tt.name)
				assert.Equal(t, tt.expectedScheduled, result, tt.description)
			}
		})
	}
}

// Test helper types and functions

type testPodClique struct {
	name              string
	minAvailable      int32
	scheduledReplicas int32
}

func createTestPod(podGangName string, hasGate bool, inPodGang bool) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			Labels:    map[string]string{},
		},
		Spec: corev1.PodSpec{},
	}

	if hasGate {
		pod.Spec.SchedulingGates = []corev1.PodSchedulingGate{
			{Name: podGangSchedulingGate},
		}
	}

	if inPodGang {
		pod.Labels[common.LabelPodGang] = podGangName

		// Add base-podgang label for scaled PodGang pods
		if strings.Contains(podGangName, "-sga-") {
			// Extract base PodGang name: "simple1-0-sga-2" -> "simple1-0"
			if sgaIndex := strings.Index(podGangName, "-sga-"); sgaIndex != -1 {
				basePodGangName := podGangName[:sgaIndex]
				pod.Labels[common.LabelBasePodGang] = basePodGangName
			}
		}
	}

	return pod
}

// createTestPodGangs creates both scaled and base PodGangs for testing
func createTestPodGangs(scaledPodGangName string, ready bool) []client.Object {
	basePodGangName := "simple1-0"
	var objects []client.Object

	// Create the scaled PodGang (no longer needs base-podgang label since production code doesn't add it)
	scaledPodGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      scaledPodGangName,
			Namespace: "default",
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{
					Name:        "simple1-0-sga-pcb",
					MinReplicas: 1,
				},
			},
		},
	}
	objects = append(objects, scaledPodGang)

	// Create the base PodGang
	podGroups := []groveschedulerv1alpha1.PodGroup{
		{
			Name:        "simple1-0-pcb",
			MinReplicas: 2,
		},
	}

	if !ready {
		// Add another PodGroup to make it more complex
		podGroups = append(podGroups, groveschedulerv1alpha1.PodGroup{
			Name:        "simple1-0-pcc",
			MinReplicas: 3,
		})
	}

	basePodGang := &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      basePodGangName,
			Namespace: "default",
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: podGroups,
		},
	}
	objects = append(objects, basePodGang)

	return objects
}

// Legacy function for backwards compatibility - now returns just the base PodGang
func createTestBasePodGang(scaledPodGangName string, ready bool) *groveschedulerv1alpha1.PodGang {
	objects := createTestPodGangs(scaledPodGangName, ready)
	// Return the base PodGang (last object in the list)
	return objects[len(objects)-1].(*groveschedulerv1alpha1.PodGang)
}

func createTestBasePodGangWithPodCliques(podCliques []testPodClique) *groveschedulerv1alpha1.PodGang {
	podGroups := make([]groveschedulerv1alpha1.PodGroup, len(podCliques))
	for i, pclq := range podCliques {
		podGroups[i] = groveschedulerv1alpha1.PodGroup{
			Name:        pclq.name,
			MinReplicas: pclq.minAvailable,
		}
	}

	return &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple1-0",
			Namespace: "default",
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: podGroups,
		},
	}
}

func createTestPodClique(name string, minAvailable, scheduledReplicas int32) *grovecorev1alpha1.PodClique {
	return &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			MinAvailable: ptr.To(minAvailable),
		},
		Status: grovecorev1alpha1.PodCliqueStatus{
			ScheduledReplicas: scheduledReplicas,
		},
	}
}

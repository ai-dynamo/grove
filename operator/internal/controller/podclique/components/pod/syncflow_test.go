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

package pod

import (
	"context"
	"fmt"
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

// pcsName and pcsReplicaIndex used across the read-side gate-removal tests.
const (
	testPCSName         = "simple1"
	testPCSReplicaIndex = 0
	testAnchorEpoch     = "1000"
	testScaledEpoch     = "1001"
	testAnchorPCLQName  = "simple1-0-pcb"
	testScaledPodGang   = "simple1-0-1001-sga-2"
)

// newGateRemovalSyncSnapshot builds a syncSnapshot for the gate-removal tests. associatedPodGangEpoch
// is the epoch of the PodGang the tested PodClique's pods belong to; an empty epoch models a PodGang
// that is not materialized yet. pgm carries the PodGangMap entries the dependency is resolved from.
func newGateRemovalSyncSnapshot(pods []*corev1.Pod, associatedPodGangEpoch string, pgm *grovecorev1alpha1.PodGangMap, podsInPodGang bool) *syncSnapshot {
	sc := &syncSnapshot{
		pcs:                    &grovecorev1alpha1.PodCliqueSet{ObjectMeta: metav1.ObjectMeta{Name: testPCSName, Namespace: "default"}},
		pclq:                   &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: "test-pclq", Namespace: "default"}},
		pcsReplicaIndex:        testPCSReplicaIndex,
		pgm:                    pgm,
		associatedPodGangName:  "simple1-0-" + associatedPodGangEpoch,
		associatedPodGangEpoch: associatedPodGangEpoch,
		existingPCLQPods:       pods,
	}
	if podsInPodGang {
		for _, pod := range pods {
			sc.podNamesUpdatedInPCLQPodGangs = append(sc.podNamesUpdatedInPCLQPodGangs, pod.Name)
		}
	}
	return sc
}

// anchorPGM returns a PodGangMap whose anchor entry (epoch testAnchorEpoch) has no dependency and
// whose scaled entry (epoch testScaledEpoch) depends on the anchor.
func anchorPGM() *grovecorev1alpha1.PodGangMap {
	return &grovecorev1alpha1.PodGangMap{
		ObjectMeta: metav1.ObjectMeta{Name: "simple1-0", Namespace: "default"},
		Spec: grovecorev1alpha1.PodGangMapSpec{
			Entries: []grovecorev1alpha1.PodGangEntry{
				{Epoch: testAnchorEpoch, Role: grovecorev1alpha1.PodGangEntryRoleAnchor},
				{Epoch: testScaledEpoch, Role: grovecorev1alpha1.PodGangEntryRoleScaleOut, DependsOn: []string{testAnchorEpoch}},
			},
		},
	}
}

// anchorPodGang returns the anchor PodGang named simple1-0-1000 with a single PodGroup requiring
// MinReplicas. Its readiness is decided by the scheduled state of the referenced PodClique.
func anchorPodGang() *groveschedulerv1alpha1.PodGang {
	return &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{Name: "simple1-0-" + testAnchorEpoch, Namespace: "default"},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{{Name: testAnchorPCLQName, MinReplicas: 2}},
		},
	}
}

func TestCheckAndRemovePodSchedulingGates_MinAvailableAware(t *testing.T) {
	tests := []struct {
		name                string
		podEpoch            string // epoch of the pod's PodGang; anchor or scaled
		anchorExists        bool
		anchorScheduled     bool
		podHasGate          bool
		podInPodGang        bool
		expectedGateRemoved bool
		expectedSkippedPods int
		expectError         bool
		description         string
	}{
		{
			name:                "anchor pod - gate removed immediately",
			podEpoch:            testAnchorEpoch,
			podHasGate:          true,
			podInPodGang:        true,
			expectedGateRemoved: true,
			expectedSkippedPods: 0,
			description:         "anchor PodGang pods have no dependency and get gates removed immediately",
		},
		{
			name:                "scaled pod - dependency not scheduled",
			podEpoch:            testScaledEpoch,
			anchorExists:        true,
			anchorScheduled:     false,
			podHasGate:          true,
			podInPodGang:        true,
			expectedGateRemoved: false,
			expectedSkippedPods: 1,
			description:         "scaled PodGang pods keep gates when the dependency anchor is not scheduled",
		},
		{
			name:                "scaled pod - dependency scheduled",
			podEpoch:            testScaledEpoch,
			anchorExists:        true,
			anchorScheduled:     true,
			podHasGate:          true,
			podInPodGang:        true,
			expectedGateRemoved: true,
			expectedSkippedPods: 0,
			description:         "scaled PodGang pods have gates removed when the dependency anchor is scheduled",
		},
		{
			name:                "scaled pod - dependency PodGang missing",
			podEpoch:            testScaledEpoch,
			anchorExists:        false,
			podHasGate:          true,
			podInPodGang:        true,
			expectedGateRemoved: false,
			expectedSkippedPods: 0, // no skips recorded when the check errors and returns early
			expectError:         true,
			description:         "a missing dependency anchor PodGang causes a requeue error",
		},
		{
			name:                "pod not in PodGang yet",
			podEpoch:            testScaledEpoch,
			anchorExists:        true,
			anchorScheduled:     true,
			podHasGate:          true,
			podInPodGang:        false,
			expectedGateRemoved: false,
			expectedSkippedPods: 1,
			description:         "pods not yet recorded in the PodGang keep gates regardless of dependency state",
		},
		{
			name:                "pod without gate",
			podEpoch:            testScaledEpoch,
			anchorExists:        true,
			anchorScheduled:     true,
			podHasGate:          false,
			podInPodGang:        true,
			expectedGateRemoved: false,
			expectedSkippedPods: 0,
			description:         "pods without gates are ignored",
		},
		{
			name:                "pod's PodGang not materialized yet - stays gated",
			podEpoch:            "", // empty epoch models an unmaterialized PodGang
			podHasGate:          true,
			podInPodGang:        true,
			expectedGateRemoved: false,
			expectedSkippedPods: 1,
			description:         "when the pod's PodGang is not created yet the dependency cannot be resolved, so it stays gated",
		},
		{
			name:                "pod's epoch not present in the PodGangMap - requeue error",
			podEpoch:            "9999", // no PodGangMap entry carries this epoch
			podHasGate:          true,
			podInPodGang:        true,
			expectedGateRemoved: false,
			expectedSkippedPods: 0,
			expectError:         true,
			description:         "an epoch absent from the PodGangMap is a contract violation and requeues",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			podGangName := "simple1-0-" + tt.podEpoch
			if tt.podEpoch == testScaledEpoch {
				podGangName = testScaledPodGang
			}
			pod := createTestPod(podGangName, tt.podHasGate, tt.podInPodGang)
			objects := []client.Object{pod}

			// Seed the anchor PodGang and its PodClique so the dependency scheduled-check can resolve.
			if tt.anchorExists {
				objects = append(objects, anchorPodGang())
				scheduledReplicas := int32(1) // below MinReplicas (2) => not scheduled
				if tt.anchorScheduled {
					scheduledReplicas = 2
				}
				objects = append(objects, createTestPodClique(testAnchorPCLQName, 2, scheduledReplicas))
			}

			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
			require.NoError(t, groveschedulerv1alpha1.AddToScheme(scheme))
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()

			r := &_resource{client: fakeClient}
			ss := newGateRemovalSyncSnapshot([]*corev1.Pod{pod}, tt.podEpoch, anchorPGM(), tt.podInPodGang)

			skippedPods, err := r.checkAndRemovePodSchedulingGates(context.Background(), logr.Discard(), ss)
			if tt.expectError {
				require.Error(t, err, "expected error for test case: %s", tt.name)
				return
			}
			require.NoError(t, err, "unexpected error for test case: %s", tt.name)
			assert.Len(t, skippedPods, tt.expectedSkippedPods, tt.description)

			updatedPod := &corev1.Pod{}
			require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pod), updatedPod))
			hasGateAfter := hasPodGangSchedulingGate(updatedPod)
			if tt.expectedGateRemoved {
				assert.False(t, hasGateAfter, "pod should not have scheduling gate after removal")
			} else if tt.podHasGate {
				assert.True(t, hasGateAfter, "pod should still have scheduling gate")
			}
		})
	}
}

func TestCheckAndRemovePodSchedulingGates_ConcurrentExecution(t *testing.T) {
	// Multiple anchor pods (no dependency) should all have their gates removed concurrently.
	var pods []*corev1.Pod
	var objects []client.Object
	for i := 0; i < 5; i++ {
		pod := createTestPod("simple1-0-"+testAnchorEpoch, true, true)
		pod.Name = fmt.Sprintf("test-pod-%d", i)
		pods = append(pods, pod)
		objects = append(objects, pod)
	}

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	require.NoError(t, groveschedulerv1alpha1.AddToScheme(scheme))
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()

	r := &_resource{client: fakeClient}
	ss := newGateRemovalSyncSnapshot(pods, testAnchorEpoch, anchorPGM(), true)

	skippedPods, err := r.checkAndRemovePodSchedulingGates(t.Context(), logr.Discard(), ss)
	require.NoError(t, err)
	assert.Empty(t, skippedPods, "no anchor pods should be skipped")

	for i, originalPod := range pods {
		updatedPod := &corev1.Pod{}
		require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(originalPod), updatedPod))
		assert.False(t, hasPodGangSchedulingGate(updatedPod), "pod %d should not have scheduling gate after removal", i)
	}
}

func TestCheckAndRemovePodSchedulingGates_PreservesForeignGates(t *testing.T) {
	const foreignAdmissionGate = "foo.io/admission"

	pod := createTestPod("simple1-0-"+testAnchorEpoch, true, true)
	pod.Spec.SchedulingGates = append(pod.Spec.SchedulingGates, corev1.PodSchedulingGate{Name: foreignAdmissionGate})

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	require.NoError(t, groveschedulerv1alpha1.AddToScheme(scheme))
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()

	r := &_resource{client: fakeClient}
	sc := newGateRemovalSyncSnapshot([]*corev1.Pod{pod}, testAnchorEpoch, anchorPGM(), true)

	skippedPods, err := r.checkAndRemovePodSchedulingGates(t.Context(), logr.Discard(), sc)
	require.NoError(t, err)
	assert.Empty(t, skippedPods)

	updatedPod := &corev1.Pod{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pod), updatedPod))
	assert.False(t, hasPodGangSchedulingGate(updatedPod), "Grove PodGang gate should be removed")
	require.Len(t, updatedPod.Spec.SchedulingGates, 1)
	assert.Equal(t, foreignAdmissionGate, updatedPod.Spec.SchedulingGates[0].Name, "foreign admission gate must be preserved")
}

func TestRemovePodGangSchedulingGate(t *testing.T) {
	tests := []struct {
		name          string
		gates         []corev1.PodSchedulingGate
		wantRemoved   bool
		wantRemaining []string
	}{
		{
			name:          "removes the grove gate when present",
			gates:         []corev1.PodSchedulingGate{{Name: podGangSchedulingGate}},
			wantRemoved:   true,
			wantRemaining: nil,
		},
		{
			name:          "returns false when the grove gate is absent",
			gates:         []corev1.PodSchedulingGate{{Name: "foo.io/other"}},
			wantRemoved:   false,
			wantRemaining: []string{"foo.io/other"},
		},
		{
			name:          "removes only the grove gate and preserves others",
			gates:         []corev1.PodSchedulingGate{{Name: podGangSchedulingGate}, {Name: "foo.io/other"}},
			wantRemoved:   true,
			wantRemaining: []string{"foo.io/other"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{Spec: corev1.PodSpec{SchedulingGates: tt.gates}}
			removed := removePodGangSchedulingGate(pod)
			assert.Equal(t, tt.wantRemoved, removed)
			var remaining []string
			for _, g := range pod.Spec.SchedulingGates {
				remaining = append(remaining, g.Name)
			}
			assert.Equal(t, tt.wantRemaining, remaining)
		})
	}
}

func TestIsPodGangScheduled(t *testing.T) {
	tests := []struct {
		name                 string
		podGangExists        bool
		podCliques           []testPodClique
		skipPodCliqueSeeding bool
		expectedScheduled    bool
		expectError          bool
		description          string
	}{
		{
			name:          "scheduled - all PodCliques meet MinAvailable",
			podGangExists: true,
			podCliques: []testPodClique{
				{name: "simple1-0-pcb", minAvailable: 2, scheduledReplicas: 2},
				{name: "simple1-0-pcc", minAvailable: 1, scheduledReplicas: 3},
			},
			expectedScheduled: true,
			description:       "all PodCliques meet their MinAvailable requirements",
		},
		{
			name:          "not scheduled - one PodClique below MinAvailable",
			podGangExists: true,
			podCliques: []testPodClique{
				{name: "simple1-0-pcb", minAvailable: 2, scheduledReplicas: 2},
				{name: "simple1-0-pcc", minAvailable: 3, scheduledReplicas: 2},
			},
			expectedScheduled: false,
			description:       "one PodClique below MinAvailable makes the PodGang not scheduled",
		},
		{
			name:              "PodGang missing",
			podGangExists:     false,
			expectedScheduled: false,
			expectError:       true,
			description:       "a missing PodGang returns an error for requeue",
		},
		{
			name:          "scheduled - single PodClique",
			podGangExists: true,
			podCliques: []testPodClique{
				{name: "simple1-0-pcb", minAvailable: 1, scheduledReplicas: 1},
			},
			expectedScheduled: true,
			description:       "single PodClique meeting MinAvailable",
		},
		{
			name:          "PodGang references a PodClique that does not exist - requeue error",
			podGangExists: true,
			podCliques: []testPodClique{
				{name: "simple1-0-pcb", minAvailable: 1, scheduledReplicas: 1},
			},
			skipPodCliqueSeeding: true,
			expectedScheduled:    false,
			expectError:          true,
			description:          "a PodGroup whose PodClique is not found returns an error for requeue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objects []client.Object
			if tt.podGangExists {
				podGroups := make([]groveschedulerv1alpha1.PodGroup, len(tt.podCliques))
				for i, pclq := range tt.podCliques {
					podGroups[i] = groveschedulerv1alpha1.PodGroup{Name: pclq.name, MinReplicas: pclq.minAvailable}
					if !tt.skipPodCliqueSeeding {
						objects = append(objects, createTestPodClique(pclq.name, pclq.minAvailable, pclq.scheduledReplicas))
					}
				}
				objects = append(objects, &groveschedulerv1alpha1.PodGang{
					ObjectMeta: metav1.ObjectMeta{Name: "simple1-0-" + testAnchorEpoch, Namespace: "default"},
					Spec:       groveschedulerv1alpha1.PodGangSpec{PodGroups: podGroups},
				})
			}

			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
			require.NoError(t, groveschedulerv1alpha1.AddToScheme(scheme))
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()

			r := &_resource{client: fakeClient}
			result, err := r.isPodGangScheduled(context.Background(), logr.Discard(), "default", "simple1-0-"+testAnchorEpoch)

			if tt.expectError {
				require.Error(t, err, "expected error for test case: %s", tt.name)
				assert.False(t, result, "result should be false when error occurs")
			} else {
				require.NoError(t, err, "unexpected error for test case: %s", tt.name)
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
	}
	if hasGate {
		pod.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: podGangSchedulingGate}}
	}
	if inPodGang {
		pod.Labels[common.LabelPodGang] = podGangName
	}
	return pod
}

func createTestPodClique(name string, minAvailable, scheduledReplicas int32) *grovecorev1alpha1.PodClique {
	return &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       grovecorev1alpha1.PodCliqueSpec{MinAvailable: ptr.To(minAvailable)},
		Status:     grovecorev1alpha1.PodCliqueStatus{ScheduledReplicas: scheduledReplicas},
	}
}

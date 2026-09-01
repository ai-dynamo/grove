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
	"slices"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	"github.com/ai-dynamo/grove/operator/internal/expect"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testPCSName         = "simple1"
	testNamespace       = "default"
	testPCSReplicaIndex = 0
	testCliqueName      = "pcb"

	testAnchor0Epoch  = "1000"
	testAnchor1Epoch  = "1001"
	testTailEpoch     = "1002"
	testScaleOutEpoch = "1003"
)

func TestSyncPCSGPodIndexLabels(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pcs", Namespace: "default"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
			Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "leader", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
				{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2}},
			},
			PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{{
				Name: "engine", CliqueNames: []string{"leader", "worker"},
			}},
		}},
	}
	pclq := &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{
		Name:      "test-pcs-0-engine-0-worker",
		Namespace: "default",
		Labels: map[string]string{
			apicommon.LabelPodCliqueScalingGroup:             "test-pcs-0-engine",
			apicommon.LabelPodCliqueScalingGroupReplicaIndex: "0",
		},
	}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      "test-pod",
		Namespace: "default",
		UID:       types.UID("unchanged-uid"),
		Labels: map[string]string{
			apicommon.LabelPodCliquePodIndex: "1",
		},
	}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
	r := _resource{client: cl}
	ss := &syncSnapshot{
		pcs:              pcs,
		pclq:             pclq,
		pcsReplicaIndex:  0,
		existingPCLQPods: []*corev1.Pod{pod},
	}

	require.NoError(t, r.syncPCSGPodIndexLabels(context.Background(), ss))

	updatedPod := &corev1.Pod{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pod), updatedPod))
	assert.Equal(t, "2", updatedPod.Labels[apicommon.LabelPodCliqueScalingGroupPodIndex])
	assert.Equal(t, types.UID("unchanged-uid"), updatedPod.UID)
}

// TestIsPodInPodReferences verifies isPodInPodReferences reports whether a pod appears in the
// PodGroup for a given PodClique FQN.
func TestIsPodInPodReferences(t *testing.T) {
	podGang := testutils.NewPodGangBuilder(podGangNameForEpoch(testAnchor0Epoch), testNamespace).
		WithPodGroupPods(testCliqueName, "pod-a", "pod-b").
		Build()
	tests := []struct {
		name     string
		pclqFQN  string
		podName  string
		expected bool
	}{
		{"pod present in the PodGroup", testCliqueName, "pod-a", true},
		{"pod absent from the PodGroup", testCliqueName, "pod-x", false},
		{"PodClique not part of the PodGang", "other", "pod-a", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := isPodInPodReferences(podGang, tc.pclqFQN, tc.podName)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

// TestCanRemoveSchedulingGate verifies the gate is liftable only when the pod's PodGang exists,
// records the pod in its PodReferences, and that PodGang's epoch dependencies are satisfied.
func TestCanRemoveSchedulingGate(t *testing.T) {
	podGangName := podGangNameForEpoch(testAnchor0Epoch)
	pod := gatedPod("pod-a", podGangName)
	podGangWithPod := testutils.NewPodGangBuilder(podGangName, testNamespace).
		WithLabels(map[string]string{apicommon.LabelEpoch: testAnchor0Epoch}).
		WithPodGroupPods(testCliqueName, "pod-a").
		Build()
	podGangWithoutPod := testutils.NewPodGangBuilder(podGangName, testNamespace).
		WithLabels(map[string]string{apicommon.LabelEpoch: testAnchor0Epoch}).
		WithPodGroupPods(testCliqueName).
		Build()

	tests := []struct {
		name                       string
		podGangByName              map[string]*groveschedulerv1alpha1.PodGang
		dependencySatisfiedByEpoch map[string]bool
		expected                   bool
	}{
		{"pod recorded and epoch dependencies satisfied", map[string]*groveschedulerv1alpha1.PodGang{podGangName: podGangWithPod}, map[string]bool{testAnchor0Epoch: true}, true},
		{"PodGang not found", map[string]*groveschedulerv1alpha1.PodGang{podGangName: nil}, map[string]bool{testAnchor0Epoch: true}, false},
		{"pod not recorded in PodReferences", map[string]*groveschedulerv1alpha1.PodGang{podGangName: podGangWithoutPod}, map[string]bool{testAnchor0Epoch: true}, false},
		{"epoch dependencies not satisfied", map[string]*groveschedulerv1alpha1.PodGang{podGangName: podGangWithPod}, map[string]bool{testAnchor0Epoch: false}, false},
		{"epoch absent from dependency map", map[string]*groveschedulerv1alpha1.PodGang{podGangName: podGangWithPod}, map[string]bool{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := canRemoveSchedulingGate(logr.Discard(), pod, testCliqueName, tc.podGangByName, tc.dependencySatisfiedByEpoch)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

// TestResolveDependencySatisfiedByEpoch verifies each PodGangMap entry's epoch is marked satisfied
// only when every epoch it directly DependsOn has all its PodGangs scheduled. Scheduling is
// monotonic, so a direct-dependency check suffices for the transitive chain. A dependency epoch may
// belong to an anchor or a tail; the resolution does not depend on the role.
func TestResolveDependencySatisfiedByEpoch(t *testing.T) {
	tests := []struct {
		name            string
		entries         []grovecorev1alpha1.PodGangEntry
		scheduledEpochs []string
		expected        map[string]bool
	}{
		{
			name:            "single anchor with no dependency is always satisfied",
			entries:         []grovecorev1alpha1.PodGangEntry{anchorEntry()},
			scheduledEpochs: nil,
			expected:        map[string]bool{testAnchor0Epoch: true},
		},
		{
			name: "entry satisfied when its dependency epoch is scheduled",
			entries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(),
				tailEntry(testTailEpoch, testAnchor0Epoch),
			},
			scheduledEpochs: []string{testAnchor0Epoch},
			expected:        map[string]bool{testAnchor0Epoch: true, testTailEpoch: true},
		},
		{
			name: "entry unsatisfied when its dependency epoch is not scheduled",
			entries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(),
				tailEntry(testTailEpoch, testAnchor0Epoch),
			},
			scheduledEpochs: nil,
			expected:        map[string]bool{testAnchor0Epoch: true, testTailEpoch: false},
		},
		{
			name: "dependency epoch belonging to a tail is resolved the same as an anchor",
			entries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(),
				tailEntry(testTailEpoch, testAnchor0Epoch),
				tailEntry(testScaleOutEpoch, testTailEpoch), // depends on a tail, not an anchor
			},
			scheduledEpochs: []string{testAnchor0Epoch}, // tail epoch not scheduled
			expected: map[string]bool{
				testAnchor0Epoch:  true,
				testTailEpoch:     true,  // depends on anchor0 (scheduled)
				testScaleOutEpoch: false, // depends on tail (not scheduled)
			},
		},
		{
			name: "multi-anchor chain and scale-out resolved by their direct dependency",
			entries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(),
				anchorDependentEntry(testAnchor1Epoch, 1, testAnchor0Epoch),
				tailEntry(testTailEpoch, testAnchor1Epoch),
				scaleOutEntry(testScaleOutEpoch, testAnchor0Epoch),
			},
			scheduledEpochs: []string{testAnchor0Epoch},
			expected: map[string]bool{
				testAnchor0Epoch:  true,  // no dependency
				testAnchor1Epoch:  true,  // depends on anchor0 (scheduled)
				testTailEpoch:     false, // depends on anchor1 (not scheduled)
				testScaleOutEpoch: true,  // depends on anchor0 (scheduled)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var objects []client.Object
			for _, entry := range tc.entries {
				builder := testutils.NewPodGangBuilder(podGangNameForEpoch(entry.Epoch), testNamespace).
					WithLabels(map[string]string{
						apicommon.LabelPartOfKey:                testPCSName,
						apicommon.LabelPodCliqueSetReplicaIndex: "0",
						apicommon.LabelEpoch:                    entry.Epoch,
					})
				if slices.Contains(tc.scheduledEpochs, entry.Epoch) {
					builder = builder.WithLastScheduled()
				}
				objects = append(objects, builder.Build())
			}
			cl := testutils.NewTestClientBuilder().WithObjects(objects...).Build()
			r := &_resource{client: cl}
			ss := &syncSnapshot{pcs: pcs(), pcsReplicaIndex: testPCSReplicaIndex, pgm: pgmWithEntries(tc.entries...)}

			actual, err := r.resolveDependencySatisfiedByEpoch(context.Background(), ss)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

// TestCheckAndRemovePodSchedulingGates verifies end-to-end that a gated pod's gate is lifted only
// when its PodGang records it and its epoch dependencies are scheduled, and that foreign scheduling
// gates are preserved.
func TestCheckAndRemovePodSchedulingGates(t *testing.T) {
	const foreignGate = "foo.io/admission"
	anchorPodGangName := podGangNameForEpoch(testAnchor0Epoch)
	tailPodGangName := podGangNameForEpoch(testTailEpoch)

	tests := []struct {
		name              string
		pod               *corev1.Pod
		anchorScheduled   bool
		expectGateRemoved bool
		expectSkipped     bool
	}{
		{
			name:              "anchor pod with no dependency and recorded in PodReferences has its gate removed",
			pod:               gatedPod("pod-a", anchorPodGangName),
			expectGateRemoved: true,
		},
		{
			name:            "tail pod is skipped when its dependency anchor is not scheduled",
			pod:             gatedPod("pod-t", tailPodGangName),
			anchorScheduled: false,
			expectSkipped:   true,
		},
		{
			name:              "tail pod has its gate removed when its dependency anchor is scheduled",
			pod:               gatedPod("pod-t", tailPodGangName),
			anchorScheduled:   true,
			expectGateRemoved: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// The pod's PodGang records the pod, so the only variable is dependency scheduling.
			podGangName := tc.pod.Labels[apicommon.LabelPodGang]
			epoch := testAnchor0Epoch
			if podGangName == tailPodGangName {
				epoch = testTailEpoch
			}
			podGang := testutils.NewPodGangBuilder(podGangName, testNamespace).
				WithLabels(map[string]string{
					apicommon.LabelPartOfKey:                testPCSName,
					apicommon.LabelPodCliqueSetReplicaIndex: "0",
					apicommon.LabelEpoch:                    epoch,
				}).
				WithPodGroupPods(testCliqueName, tc.pod.Name).
				Build()

			objects := []client.Object{tc.pod, podGang}
			// Seed the anchor PodGang the tail depends on, scheduled or not.
			if podGangName == tailPodGangName {
				anchorBuilder := testutils.NewPodGangBuilder(anchorPodGangName, testNamespace).
					WithLabels(map[string]string{
						apicommon.LabelPartOfKey:                testPCSName,
						apicommon.LabelPodCliqueSetReplicaIndex: "0",
						apicommon.LabelEpoch:                    testAnchor0Epoch,
					}).
					WithPodGroupPods(testCliqueName)
				if tc.anchorScheduled {
					anchorBuilder = anchorBuilder.WithLastScheduled()
				}
				objects = append(objects, anchorBuilder.Build())
			}

			cl := testutils.NewTestClientBuilder().WithObjects(objects...).Build()
			r := &_resource{client: cl}
			ss := gateRemovalSnapshot([]*corev1.Pod{tc.pod}, twoEpochPGM())

			skipped, err := r.checkAndRemovePodSchedulingGates(context.Background(), logr.Discard(), ss)
			require.NoError(t, err)

			if tc.expectSkipped {
				assert.Equal(t, []string{tc.pod.Name}, skipped)
			} else {
				assert.Empty(t, skipped)
			}

			updated := &corev1.Pod{}
			require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(tc.pod), updated))
			assert.Equal(t, !tc.expectGateRemoved, hasPodGangSchedulingGate(updated))
		})
	}

	t.Run("preserves foreign scheduling gates", func(t *testing.T) {
		pod := gatedPod("pod-a", anchorPodGangName)
		pod.Spec.SchedulingGates = append(pod.Spec.SchedulingGates, corev1.PodSchedulingGate{Name: foreignGate})
		podGang := testutils.NewPodGangBuilder(anchorPodGangName, testNamespace).
			WithLabels(map[string]string{apicommon.LabelEpoch: testAnchor0Epoch}).
			WithPodGroupPods(testCliqueName, pod.Name).
			Build()
		cl := testutils.NewTestClientBuilder().WithObjects(pod, podGang).Build()
		r := &_resource{client: cl}
		ss := gateRemovalSnapshot([]*corev1.Pod{pod}, twoEpochPGM())

		skipped, err := r.checkAndRemovePodSchedulingGates(context.Background(), logr.Discard(), ss)
		require.NoError(t, err)
		assert.Empty(t, skipped)

		updated := &corev1.Pod{}
		require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pod), updated))
		assert.False(t, hasPodGangSchedulingGate(updated), "Grove PodGang gate must be removed")
		require.Len(t, updated.Spec.SchedulingGates, 1)
		assert.Equal(t, foreignGate, updated.Spec.SchedulingGates[0].Name, "foreign gate must be preserved")
	})
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

// TestSelectExcessPodsToDelete_ExcludesPodsAlreadyBeingDeleted covers the scale-in path when Pods
// whose deletion was already triggered are still present in the informer cache. GetPCLQPods returns
// them, and a Pod stays Running and Ready for the whole of its termination grace period, so without
// an explicit filter they are counted as excess and can consume the deletion budget that should have
// spared a Pod that is still serving.
func TestSelectExcessPodsToDelete_ExcludesPodsAlreadyBeingDeleted(t *testing.T) {
	createdAt := metav1.NewTime(time.Now())
	const templateHash = "hash-1"
	const podGangName = "pclq-1-pg-0"

	newPod := func(name string, terminating bool) *corev1.Pod {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              name,
				UID:               types.UID("uid-" + name),
				CreationTimestamp: createdAt,
				Labels:            map[string]string{apicommon.LabelPodTemplateHash: templateHash, apicommon.LabelPodGang: podGangName},
			},
			Spec: corev1.PodSpec{NodeName: "node-a"},
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			},
		}
		if terminating {
			pod.DeletionTimestamp = &createdAt
		}
		return pod
	}

	tests := []struct {
		name string
		// pods that already carry a deletionTimestamp
		terminatingPods []string
		// pods for which a delete expectation was recorded but the cache has not caught up yet
		deleteExpected []string
		healthyPods    []string
		replicas       int32
		expectedNames  []string
	}{
		{
			name:          "no terminating pods - excess selected normally",
			healthyPods:   []string{"a", "b", "c"},
			replicas:      1,
			expectedNames: []string{"a", "b"},
		},
		{
			name:            "terminating pods are not counted as excess",
			terminatingPods: []string{"a", "b"},
			healthyPods:     []string{"c"},
			replicas:        1,
			expectedNames:   nil,
		},
		{
			name:            "only the genuinely excess healthy pod is selected",
			terminatingPods: []string{"a"},
			healthyPods:     []string{"b", "c"},
			replicas:        1,
			expectedNames:   []string{"b"},
		},
		{
			name:           "pods with a recorded delete expectation are not counted as excess",
			deleteExpected: []string{"a", "b"},
			healthyPods:    []string{"c"},
			replicas:       1,
			expectedNames:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := expect.NewExpectationsStore()
			r := _resource{expectationsStore: store}

			pclq := &grovecorev1alpha1.PodClique{
				ObjectMeta: metav1.ObjectMeta{Name: "pclq-1", Namespace: testNamespace},
				Spec:       grovecorev1alpha1.PodCliqueSpec{Replicas: tt.replicas},
			}
			key, err := componentutils.PodGangScopedExpectationsStoreKey(pclq.ObjectMeta, podGangName)
			require.NoError(t, err)

			var pods []*corev1.Pod
			for _, name := range tt.terminatingPods {
				pods = append(pods, newPod(name, true))
			}
			for _, name := range tt.deleteExpected {
				pod := newPod(name, false)
				require.NoError(t, store.ExpectDeletions(logr.Discard(), key, pod.GetUID()))
				pods = append(pods, pod)
			}
			for _, name := range tt.healthyPods {
				pods = append(pods, newPod(name, false))
			}

			sc := &syncSnapshot{
				pclq:             pclq,
				existingPCLQPods: pods,
			}

			selected := r.selectExcessPodsToDelete(sc, logr.Discard())

			var gotNames []string
			for _, pod := range selected {
				gotNames = append(gotNames, pod.Name)
			}
			assert.ElementsMatch(t, tt.expectedNames, gotNames)

			for _, pod := range selected {
				assert.Nil(t, pod.DeletionTimestamp, "pod %s is already terminating and must not be selected", pod.Name)
				assert.False(t, store.HasDeleteExpectation(key, pod.GetUID()), "pod %s already has a delete expectation", pod.Name)
			}
		})
	}
}

// TestReconcilePCSGLabellessPods verifies a PodCliqueScalingGroup PodClique's labelless pods are
// relabeled to its single PodGang.
func TestReconcilePCSGLabellessPods(t *testing.T) {
	const pcsgPodGangName = "simple1-0-1001-sga-2"

	t.Run("relabels labelless pods to the PodCliqueScalingGroup PodGang", func(t *testing.T) {
		pod := testutils.NewPodBuilder("pod-a", testNamespace).Build()
		cl := testutils.NewTestClientBuilder().WithObjects(pod).Build()
		r := &_resource{client: cl}
		ss := &syncSnapshot{pclq: pclqWithReplicas(1), pcsgReplicaPodGangName: pcsgPodGangName, existingPCLQPods: []*corev1.Pod{pod}}

		repaired, err := r.reconcilePCSGLabellessPods(context.Background(), logr.Discard(), ss)
		require.NoError(t, err)
		assert.True(t, repaired)

		updated := &corev1.Pod{}
		require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pod), updated))
		assert.Equal(t, pcsgPodGangName, updated.Labels[apicommon.LabelPodGang])
	})

	t.Run("no labelless pods is a no-op", func(t *testing.T) {
		pod := gatedPod("pod-a", pcsgPodGangName)
		cl := testutils.NewTestClientBuilder().WithObjects(pod).Build()
		r := &_resource{client: cl}
		ss := &syncSnapshot{pclq: pclqWithReplicas(1), pcsgReplicaPodGangName: pcsgPodGangName, existingPCLQPods: []*corev1.Pod{pod}}

		repaired, err := r.reconcilePCSGLabellessPods(context.Background(), logr.Discard(), ss)
		require.NoError(t, err)
		assert.False(t, repaired)
	})
}

// ---------------- test helpers ----------------

// pcs returns a minimal PodCliqueSet for the gate-removal tests.
func pcs() *grovecorev1alpha1.PodCliqueSet {
	return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").Build()
}

// gatedPod returns a pod labeled for podGangName carrying the Grove PodGang scheduling gate.
func gatedPod(name, podGangName string) *corev1.Pod {
	return testutils.NewPodBuilder(name, testNamespace).
		WithLabels(map[string]string{apicommon.LabelPodGang: podGangName}).
		WithSchedulingGate(podGangSchedulingGate).
		Build()
}

// gateRemovalSnapshot builds a syncSnapshot for the end-to-end gate-removal tests. pclq.Name is the
// PodClique FQN that matches the PodGroup name recorded on the seeded PodGangs.
func gateRemovalSnapshot(pods []*corev1.Pod, pgm *grovecorev1alpha1.PodGangMap) *syncSnapshot {
	return &syncSnapshot{
		pcs:              pcs(),
		pclq:             &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: testCliqueName, Namespace: testNamespace}},
		pcsReplicaIndex:  testPCSReplicaIndex,
		pgm:              pgm,
		cliqueName:       testCliqueName,
		existingPCLQPods: pods,
	}
}

// podGangNameForEpoch returns the anchor PodGang name for an epoch. The gate-removal resolution keys
// PodGangs by their epoch label, so the exact name form does not matter as long as it is unique per
// epoch.
func podGangNameForEpoch(epoch string) string {
	return apicommon.GenerateAnchorPodGangName(apicommon.ResourceNameReplica{Name: testPCSName, Replica: testPCSReplicaIndex}, epoch)
}

// twoEpochPGM returns a PodGangMap whose anchor entry (testAnchor0Epoch) has no dependency and whose
// tail entry (testTailEpoch) depends on the anchor.
func twoEpochPGM() *grovecorev1alpha1.PodGangMap {
	return pgmWithEntries(
		anchorEntry(),
		tailEntry(testTailEpoch, testAnchor0Epoch),
	)
}

// pgmWithEntries builds a PodGangMap for the test PCS replica carrying the given entries.
func pgmWithEntries(entries ...grovecorev1alpha1.PodGangEntry) *grovecorev1alpha1.PodGangMap {
	return testutils.NewPodGangMapBuilder(testPCSName, testNamespace, "uid", testPCSReplicaIndex).
		WithEntries(entries...).
		Build()
}

// anchorEntry builds the dependency-free anchor entry. A nil-DependsOn anchor is always the
// AnchorIndex 0 anchor at testAnchor0Epoch; higher-index anchors depend on a prior epoch and are
// built with anchorDependentEntry.
func anchorEntry() grovecorev1alpha1.PodGangEntry {
	return testutils.NewPodGangEntryBuilder("hash", testAnchor0Epoch).
		WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
		WithAnchorIndex(0).
		Build()
}

func anchorDependentEntry(epoch string, anchorIndex int32, dependsOnEpoch string) grovecorev1alpha1.PodGangEntry {
	return testutils.NewPodGangEntryBuilder("hash", epoch).
		WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
		WithAnchorIndex(anchorIndex).
		WithDependsOn(dependsOnEpoch).
		Build()
}

func tailEntry(epoch, dependsOnEpoch string) grovecorev1alpha1.PodGangEntry {
	return testutils.NewPodGangEntryBuilder("hash", epoch).
		WithRole(grovecorev1alpha1.PodGangEntryRoleTail).
		WithDependsOn(dependsOnEpoch).
		Build()
}

func scaleOutEntry(epoch, dependsOnEpoch string) grovecorev1alpha1.PodGangEntry {
	return testutils.NewPodGangEntryBuilder("hash", epoch).
		WithRole(grovecorev1alpha1.PodGangEntryRoleScaleOut).
		WithDependsOn(dependsOnEpoch).
		Build()
}

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

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testNamespace  = "default"
	testPCSName    = "workload1"
	testPCSReplica = 0
	testHash       = "abc12"
	// Epochs identify PodGangMap entries (numeric, unix-nano-like). An anchor entry has no
	// DependsOn; a tail entry DependsOn the epoch of the anchor it follows.
	epoch0 = "100"
	epoch1 = "200"
	epoch2 = "300"
	// PodGang resource names a gated pod's grove.io/podgang label points at. The name is decoupled
	// from the epoch; a PodGang carries its epoch in the grove.io/epoch label.
	pg0 = "workload1-0-abc12-0"
	pg1 = "workload1-0-abc12-1"
	pg2 = "workload1-0-abc12-2"
	// PCLQ FQN used as PodGroup name inside PodGang resources.
	pclqFQN = "workload1-0-pca"
)

func TestCheckAndRemovePodSchedulingGates(t *testing.T) {
	tests := []struct {
		name                string
		setupObjects        func() []client.Object
		gatedPods           func() []*corev1.Pod
		expectedSkipped     []string
		expectedGateRemoved []string
		expectError         bool
	}{
		{
			name: "pod in PodGang with no DependsOn — gate removed",
			setupObjects: func() []client.Object {
				pgm := buildPGM([]grovecorev1alpha1.PodGangEntry{
					{Epoch: epoch0, PodCliques: map[string]int32{"pca": 1}},
				})
				pg := buildPodGangWithRef(pg0, epoch0, "pod-0")
				return []client.Object{pgm, pg}
			},
			gatedPods: func() []*corev1.Pod {
				return []*corev1.Pod{withGate(buildGatedPod("pod-0", pg0))}
			},
			expectedGateRemoved: []string{"pod-0"},
		},
		{
			name: "pod depends on unscheduled PodGang — gate skipped",
			setupObjects: func() []client.Object {
				pgm := buildPGM([]grovecorev1alpha1.PodGangEntry{
					{Epoch: epoch0, PodCliques: map[string]int32{"pca": 1}},
					{Epoch: epoch1, PodCliques: map[string]int32{"pca": 1}, DependsOn: []string{epoch0}},
				})
				pg1PG := buildPodGangWithRef(pg1, epoch1, "pod-1")
				pg0PG := buildDependencyPodGang(pg0, epoch0, false) // epoch0 not scheduled
				return []client.Object{pgm, pg1PG, pg0PG}
			},
			gatedPods: func() []*corev1.Pod {
				return []*corev1.Pod{withGate(buildGatedPod("pod-1", pg1))}
			},
			expectedSkipped: []string{"pod-1"},
		},
		{
			name: "pod depends on scheduled PodGang — gate removed",
			setupObjects: func() []client.Object {
				pgm := buildPGM([]grovecorev1alpha1.PodGangEntry{
					{Epoch: epoch0, PodCliques: map[string]int32{"pca": 1}},
					{Epoch: epoch1, PodCliques: map[string]int32{"pca": 1}, DependsOn: []string{epoch0}},
				})
				pg1PG := buildPodGangWithRef(pg1, epoch1, "pod-1")
				pg0PG := buildDependencyPodGang(pg0, epoch0, true) // epoch0 scheduled
				return []client.Object{pgm, pg1PG, pg0PG}
			},
			gatedPods: func() []*corev1.Pod {
				return []*corev1.Pod{withGate(buildGatedPod("pod-1", pg1))}
			},
			expectedGateRemoved: []string{"pod-1"},
		},
		{
			name: "pod depends on multiple epochs — one unscheduled — gate skipped",
			setupObjects: func() []client.Object {
				pgm := buildPGM([]grovecorev1alpha1.PodGangEntry{
					{Epoch: epoch0, PodCliques: map[string]int32{"pca": 2}},
					{Epoch: epoch1, PodCliques: map[string]int32{"pca": 2}},
					{Epoch: epoch2, PodCliques: map[string]int32{"pca": 1}, DependsOn: []string{epoch0, epoch1}},
				})
				pg2PG := buildPodGangWithRef(pg2, epoch2, "pod-tail")
				pg0PG := buildDependencyPodGang(pg0, epoch0, true)  // scheduled
				pg1PG := buildDependencyPodGang(pg1, epoch1, false) // not yet scheduled
				return []client.Object{pgm, pg2PG, pg0PG, pg1PG}
			},
			gatedPods: func() []*corev1.Pod {
				return []*corev1.Pod{withGate(buildGatedPod("pod-tail", pg2))}
			},
			expectedSkipped: []string{"pod-tail"},
		},
		{
			name: "pod depends on multiple epochs — all scheduled — gate removed",
			setupObjects: func() []client.Object {
				pgm := buildPGM([]grovecorev1alpha1.PodGangEntry{
					{Epoch: epoch0, PodCliques: map[string]int32{"pca": 2}},
					{Epoch: epoch1, PodCliques: map[string]int32{"pca": 2}},
					{Epoch: epoch2, PodCliques: map[string]int32{"pca": 1}, DependsOn: []string{epoch0, epoch1}},
				})
				pg2PG := buildPodGangWithRef(pg2, epoch2, "pod-tail")
				pg0PG := buildDependencyPodGang(pg0, epoch0, true)
				pg1PG := buildDependencyPodGang(pg1, epoch1, true)
				return []client.Object{pgm, pg2PG, pg0PG, pg1PG}
			},
			gatedPods: func() []*corev1.Pod {
				return []*corev1.Pod{withGate(buildGatedPod("pod-tail", pg2))}
			},
			expectedGateRemoved: []string{"pod-tail"},
		},
		{
			name: "pod not yet in PodReferences — gate skipped",
			setupObjects: func() []client.Object {
				pgm := buildPGM([]grovecorev1alpha1.PodGangEntry{
					{Epoch: epoch0, PodCliques: map[string]int32{"pca": 1}},
				})
				// PodGang exists at epoch0 but does not record pod-0 in PodReferences.
				pg := buildDependencyPodGang(pg0, epoch0, true)
				return []client.Object{pgm, pg}
			},
			gatedPods: func() []*corev1.Pod {
				return []*corev1.Pod{withGate(buildGatedPod("pod-0", pg0))}
			},
			expectedSkipped: []string{"pod-0"},
		},
		{
			name: "pod missing LabelPodGang — hard error",
			setupObjects: func() []client.Object {
				pgm := buildPGM([]grovecorev1alpha1.PodGangEntry{
					{Epoch: epoch0, PodCliques: map[string]int32{"pca": 1}},
				})
				return []client.Object{pgm}
			},
			gatedPods: func() []*corev1.Pod {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "pod-no-label", Namespace: testNamespace},
					Spec:       corev1.PodSpec{SchedulingGates: []corev1.PodSchedulingGate{{Name: podGangSchedulingGate}}},
				}
				return []*corev1.Pod{pod}
			},
			expectError: true,
		},
		{
			name: "PodGangMap not found — all gated pods skipped",
			setupObjects: func() []client.Object {
				return []client.Object{}
			},
			gatedPods: func() []*corev1.Pod {
				return []*corev1.Pod{withGate(buildGatedPod("pod-0", pg0))}
			},
			expectedSkipped: []string{"pod-0"},
		},
		{
			name: "no gated pods — returns immediately",
			setupObjects: func() []client.Object {
				return []client.Object{}
			},
			gatedPods: func() []*corev1.Pod {
				return []*corev1.Pod{
					testutils.NewPodBuilder("pod-0", testNamespace).
						WithLabels(map[string]string{apicommon.LabelPodGang: pg0}).
						Build(),
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gatedPods := tc.gatedPods()
			objects := tc.setupObjects()
			for _, p := range gatedPods {
				objects = append(objects, p)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(buildTestScheme(t)).
				WithObjects(objects...).
				Build()

			r := &_resource{client: fakeClient}
			sc := &syncContext{
				ctx:              context.Background(),
				pcs:              &grovecorev1alpha1.PodCliqueSet{ObjectMeta: metav1.ObjectMeta{Name: testPCSName, Namespace: testNamespace}},
				pclq:             &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: pclqFQN, Namespace: testNamespace}},
				pcsReplicaIndex:  testPCSReplica,
				existingPCLQPods: gatedPods,
				pgm:              loadPGMFromFakeClient(t, fakeClient),
			}

			skipped, err := r.checkAndRemovePodSchedulingGates(sc, logr.Discard())

			if tc.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.ElementsMatch(t, tc.expectedSkipped, skipped)

			for _, podName := range tc.expectedGateRemoved {
				updated := &corev1.Pod{}
				require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: podName, Namespace: testNamespace}, updated))
				assert.False(t, hasPodGangSchedulingGate(updated), "pod %s should have gate removed", podName)
			}
		})
	}
}

func TestCheckAndRemovePodSchedulingGates_ConcurrentExecution(t *testing.T) {
	const numPods = 5

	pgm := buildPGM([]grovecorev1alpha1.PodGangEntry{
		{Epoch: epoch0, PodCliques: map[string]int32{"pca": numPods}},
	})

	var gatedPods []*corev1.Pod
	var podRefs []groveschedulerv1alpha1.NamespacedName
	objects := []client.Object{pgm}

	for i := range numPods {
		name := fmt.Sprintf("pod-%d", i)
		pod := withGate(buildGatedPod(name, pg0))
		gatedPods = append(gatedPods, pod)
		objects = append(objects, pod)
		podRefs = append(podRefs, groveschedulerv1alpha1.NamespacedName{Namespace: testNamespace, Name: name})
	}

	pgBuilder := testutils.NewPodGangBuilder(pg0, testNamespace).
		WithPodGroups([]groveschedulerv1alpha1.PodGroup{
			{Name: pclqFQN, MinReplicas: numPods, PodReferences: podRefs},
		})
	for k, v := range epochLabels(epoch0) {
		pgBuilder = pgBuilder.WithLabel(k, v)
	}
	objects = append(objects, pgBuilder.Build())

	fakeClient := fake.NewClientBuilder().
		WithScheme(buildTestScheme(t)).
		WithObjects(objects...).
		Build()

	r := &_resource{client: fakeClient}
	sc := &syncContext{
		ctx:              context.Background(),
		pcs:              &grovecorev1alpha1.PodCliqueSet{ObjectMeta: metav1.ObjectMeta{Name: testPCSName, Namespace: testNamespace}},
		pclq:             &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: pclqFQN, Namespace: testNamespace}},
		pcsReplicaIndex:  testPCSReplica,
		existingPCLQPods: gatedPods,
		pgm:              loadPGMFromFakeClient(t, fakeClient),
	}

	skipped, err := r.checkAndRemovePodSchedulingGates(sc, logr.Discard())
	require.NoError(t, err)
	assert.Empty(t, skipped)

	for i := range numPods {
		updated := &corev1.Pod{}
		require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: fmt.Sprintf("pod-%d", i), Namespace: testNamespace}, updated))
		assert.False(t, hasPodGangSchedulingGate(updated), "pod %d should have gate removed", i)
	}
}

func TestRemovePodGangSchedulingGate(t *testing.T) {
	t.Run("removes only the Grove gate", func(t *testing.T) {
		pod := &corev1.Pod{
			Spec: corev1.PodSpec{
				SchedulingGates: []corev1.PodSchedulingGate{
					{Name: "foo.io/admission"},
					{Name: podGangSchedulingGate},
					{Name: "foo.io/topology"},
				},
			},
		}
		assert.True(t, removePodGangSchedulingGate(pod))
		assert.Equal(t, []corev1.PodSchedulingGate{
			{Name: "foo.io/admission"},
			{Name: "foo.io/topology"},
		}, pod.Spec.SchedulingGates)
	})
	t.Run("returns false when Grove gate is absent", func(t *testing.T) {
		pod := &corev1.Pod{
			Spec: corev1.PodSpec{
				SchedulingGates: []corev1.PodSchedulingGate{
					{Name: "foo.io/admission"},
				},
			},
		}
		assert.False(t, removePodGangSchedulingGate(pod))
		assert.Equal(t, []corev1.PodSchedulingGate{
			{Name: "foo.io/admission"},
		}, pod.Spec.SchedulingGates)
	})
}

// Test helper types and functions

func buildTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, grovecorev1alpha1.AddToScheme(s))
	require.NoError(t, groveschedulerv1alpha1.AddToScheme(s))
	return s
}

func buildPGM(entries []grovecorev1alpha1.PodGangEntry) *grovecorev1alpha1.PodGangMap {
	pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: testPCSName, Replica: testPCSReplica})
	return &grovecorev1alpha1.PodGangMap{
		ObjectMeta: metav1.ObjectMeta{Name: pgmName, Namespace: testNamespace},
		Spec:       grovecorev1alpha1.PodGangMapSpec{Entries: entries},
	}
}

func buildGatedPod(name, podGangName string) *corev1.Pod {
	return testutils.NewPodBuilder(name, testNamespace).
		WithLabels(map[string]string{apicommon.LabelPodGang: podGangName}).
		Build()
}

func withGate(pod *corev1.Pod) *corev1.Pod {
	pod.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: podGangSchedulingGate}}
	return pod
}

// epochLabels are the labels a PodGang at the given epoch carries. AllPodGangsAtEpochEverScheduled
// lists dependency PodGangs by these, and gate removal reads grove.io/epoch to resolve the pod's
// PodGangMap entry.
func epochLabels(epoch string) map[string]string {
	return map[string]string{
		apicommon.LabelPartOfKey:                testPCSName,
		apicommon.LabelPodCliqueSetReplicaIndex: "0",
		apicommon.LabelEpoch:                    epoch,
	}
}

// buildPodGangWithRef builds the PodGang a gated pod belongs to: it records the pod in
// PodReferences and carries the epoch label used to resolve the pod's PodGangMap entry.
func buildPodGangWithRef(pgName, epoch, podName string) *groveschedulerv1alpha1.PodGang {
	b := testutils.NewPodGangBuilder(pgName, testNamespace).
		WithPodGroups([]groveschedulerv1alpha1.PodGroup{
			{
				Name:        pclqFQN,
				MinReplicas: 1,
				PodReferences: []groveschedulerv1alpha1.NamespacedName{
					{Namespace: testNamespace, Name: podName},
				},
			},
		})
	for k, v := range epochLabels(epoch) {
		b = b.WithLabel(k, v)
	}
	return b.Build()
}

// buildDependencyPodGang builds a PodGang at the given epoch, discoverable by
// AllPodGangsAtEpochEverScheduled. When scheduled, Status.LastScheduled is set so the epoch counts
// as scheduled.
func buildDependencyPodGang(pgName, epoch string, scheduled bool) *groveschedulerv1alpha1.PodGang {
	b := testutils.NewPodGangBuilder(pgName, testNamespace)
	for k, v := range epochLabels(epoch) {
		b = b.WithLabel(k, v)
	}
	if scheduled {
		b = b.WithStatusLastScheduled(metav1.Now())
	}
	return b.Build()
}

func TestGetPCSReplicaIndexFromPCLQ(t *testing.T) {
	t.Run("extracts valid index", func(t *testing.T) {
		pclq := &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "2"},
			},
		}
		idx, err := getPCSReplicaIndexFromPCLQ(pclq)
		require.NoError(t, err)
		assert.Equal(t, 2, idx)
	})

	t.Run("errors when label missing", func(t *testing.T) {
		pclq := &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pclq", Labels: map[string]string{}},
		}
		_, err := getPCSReplicaIndexFromPCLQ(pclq)
		assert.Error(t, err)
	})

	t.Run("errors when label not an integer", func(t *testing.T) {
		pclq := &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "my-pclq",
				Labels: map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "abc"},
			},
		}
		_, err := getPCSReplicaIndexFromPCLQ(pclq)
		assert.Error(t, err)
	})
}

// loadPGMFromFakeClient fetches the PodGangMap for the test PCS replica from the fake client.
// Returns nil when the PGM is not present (mirrors the production behaviour in prepareSyncFlow).
func loadPGMFromFakeClient(t *testing.T, cl client.Client) *grovecorev1alpha1.PodGangMap {
	t.Helper()
	pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: testPCSName, Replica: testPCSReplica})
	pgm := &grovecorev1alpha1.PodGangMap{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: pgmName, Namespace: testNamespace}, pgm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		require.NoError(t, err)
	}
	return pgm
}

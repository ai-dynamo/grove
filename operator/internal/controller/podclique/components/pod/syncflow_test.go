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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testNamespace  = "default"
	testPCSName    = "workload1"
	testPCSReplica = 0
	testHash       = "abc12"
	// PodGang names follow the unified GeneratePodGangName format: <pcsName>-<replicaIndex>-<uniqueSuffix>.
	// DependsOn determines role: nil = MPG/BPG, non-nil = TailPG/SPG.
	pg0 = "workload1-0-0"
	pg1 = "workload1-0-1"
	pg2 = "workload1-0-2"
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
					{Name: pg0, PodCliques: map[string]int32{"pca": 1}},
				})
				pg := buildPodGangWithRef(pg0, "pod-0")
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
					{Name: pg0, PodCliques: map[string]int32{"pca": 1}},
					{Name: pg1, PodCliques: map[string]int32{"pca": 1}, DependsOn: []string{pg0}},
				})
				pg1PG := buildPodGangWithRef(pg1, "pod-1")
				pg0PG := testutils.NewPodGangBuilder(pg0, testNamespace).
					WithPodGroups([]groveschedulerv1alpha1.PodGroup{{Name: pclqFQN, MinReplicas: 1}}).Build()
				pg0PCLQ := buildTestPodClique(pclqFQN, 1, 0) // scheduledReplicas=0 < minReplicas=1
				return []client.Object{pgm, pg1PG, pg0PG, pg0PCLQ}
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
					{Name: pg0, PodCliques: map[string]int32{"pca": 1}},
					{Name: pg1, PodCliques: map[string]int32{"pca": 1}, DependsOn: []string{pg0}},
				})
				pg1PG := buildPodGangWithRef(pg1, "pod-1")
				pg0PG := testutils.NewPodGangBuilder(pg0, testNamespace).
					WithPodGroups([]groveschedulerv1alpha1.PodGroup{{Name: pclqFQN, MinReplicas: 1}}).Build()
				pg0PCLQ := buildTestPodClique(pclqFQN, 1, 1) // scheduledReplicas=1 >= minReplicas=1
				return []client.Object{pgm, pg1PG, pg0PG, pg0PCLQ}
			},
			gatedPods: func() []*corev1.Pod {
				return []*corev1.Pod{withGate(buildGatedPod("pod-1", pg1))}
			},
			expectedGateRemoved: []string{"pod-1"},
		},
		{
			name: "pod depends on multiple PodGangs — one unscheduled — gate skipped",
			setupObjects: func() []client.Object {
				// pg0 and pg1 each own a distinct PCLQ (different replica indices).
				pclq0FQN := "workload1-0-pca"
				pclq1FQN := "workload1-1-pca"
				pgm := buildPGM([]grovecorev1alpha1.PodGangEntry{
					{Name: pg0, PodCliques: map[string]int32{"pca": 2}},
					{Name: pg1, PodCliques: map[string]int32{"pca": 2}},
					{Name: pg2, PodCliques: map[string]int32{"pca": 1}, DependsOn: []string{pg0, pg1}},
				})
				pg2PG := buildPodGangWithRef(pg2, "pod-tail")
				pg0PG := testutils.NewPodGangBuilder(pg0, testNamespace).
					WithPodGroups([]groveschedulerv1alpha1.PodGroup{{Name: pclq0FQN, MinReplicas: 2}}).Build()
				pg1PG := testutils.NewPodGangBuilder(pg1, testNamespace).
					WithPodGroups([]groveschedulerv1alpha1.PodGroup{{Name: pclq1FQN, MinReplicas: 2}}).Build()
				pg0PCLQ := buildTestPodClique(pclq0FQN, 2, 2) // scheduled
				pg1PCLQ := buildTestPodClique(pclq1FQN, 2, 1) // not yet scheduled
				return []client.Object{pgm, pg2PG, pg0PG, pg1PG, pg0PCLQ, pg1PCLQ}
			},
			gatedPods: func() []*corev1.Pod {
				return []*corev1.Pod{withGate(buildGatedPod("pod-tail", pg2))}
			},
			expectedSkipped: []string{"pod-tail"},
		},
		{
			name: "pod depends on multiple PodGangs — all scheduled — gate removed",
			setupObjects: func() []client.Object {
				pclq0FQN := "workload1-0-pca"
				pclq1FQN := "workload1-1-pca"
				pgm := buildPGM([]grovecorev1alpha1.PodGangEntry{
					{Name: pg0, PodCliques: map[string]int32{"pca": 2}},
					{Name: pg1, PodCliques: map[string]int32{"pca": 2}},
					{Name: pg2, PodCliques: map[string]int32{"pca": 1}, DependsOn: []string{pg0, pg1}},
				})
				pg2PG := buildPodGangWithRef(pg2, "pod-tail")
				pg0PG := testutils.NewPodGangBuilder(pg0, testNamespace).
					WithPodGroups([]groveschedulerv1alpha1.PodGroup{{Name: pclq0FQN, MinReplicas: 2}}).Build()
				pg1PG := testutils.NewPodGangBuilder(pg1, testNamespace).
					WithPodGroups([]groveschedulerv1alpha1.PodGroup{{Name: pclq1FQN, MinReplicas: 2}}).Build()
				pg0PCLQ := buildTestPodClique(pclq0FQN, 2, 2)
				pg1PCLQ := buildTestPodClique(pclq1FQN, 2, 2)
				return []client.Object{pgm, pg2PG, pg0PG, pg1PG, pg0PCLQ, pg1PCLQ}
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
					{Name: pg0, PodCliques: map[string]int32{"pca": 1}},
				})
				pg := testutils.NewPodGangBuilder(pg0, testNamespace).
					WithPodGroups([]groveschedulerv1alpha1.PodGroup{{Name: pclqFQN, MinReplicas: 1}}).Build()
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
					{Name: pg0, PodCliques: map[string]int32{"pca": 1}},
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
		{Name: pg0, PodCliques: map[string]int32{"pca": numPods}},
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

	pg := testutils.NewPodGangBuilder(pg0, testNamespace).
		WithPodGroups([]groveschedulerv1alpha1.PodGroup{
			{Name: pclqFQN, MinReplicas: numPods, PodReferences: podRefs},
		}).Build()
	objects = append(objects, pg)

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

func TestIsPodGangScheduled(t *testing.T) {
	tests := []struct {
		name              string
		podCliques        []testPodClique
		expectedScheduled bool
		expectError       bool
	}{
		{
			name: "all PodCliques meet MinReplicas",
			podCliques: []testPodClique{
				{name: "workload1-0-pca", minAvailable: 2, scheduledReplicas: 2},
				{name: "workload1-0-pcb", minAvailable: 1, scheduledReplicas: 3},
			},
			expectedScheduled: true,
		},
		{
			name: "one PodClique below MinReplicas",
			podCliques: []testPodClique{
				{name: "workload1-0-pca", minAvailable: 2, scheduledReplicas: 2},
				{name: "workload1-0-pcb", minAvailable: 3, scheduledReplicas: 2},
			},
			expectedScheduled: false,
		},
		{
			name: "single PodClique meets MinReplicas",
			podCliques: []testPodClique{
				{name: "workload1-0-pca", minAvailable: 1, scheduledReplicas: 1},
			},
			expectedScheduled: true,
		},
		{
			name:        "PodClique not found — error",
			podCliques:  []testPodClique{},
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var objects []client.Object

			podGroups := make([]groveschedulerv1alpha1.PodGroup, len(tc.podCliques))
			for i, pclq := range tc.podCliques {
				podGroups[i] = groveschedulerv1alpha1.PodGroup{Name: pclq.name, MinReplicas: pclq.minAvailable}
				objects = append(objects, buildTestPodClique(pclq.name, pclq.minAvailable, pclq.scheduledReplicas))
			}

			var pg *groveschedulerv1alpha1.PodGang
			if len(tc.podCliques) == 0 {
				pg = testutils.NewPodGangBuilder(pg0, testNamespace).
					WithPodGroups([]groveschedulerv1alpha1.PodGroup{{Name: "missing-pclq", MinReplicas: 1}}).Build()
			} else {
				pg = testutils.NewPodGangBuilder(pg0, testNamespace).WithPodGroups(podGroups).Build()
			}
			objects = append(objects, pg)

			fakeClient := fake.NewClientBuilder().
				WithScheme(buildTestScheme(t)).
				WithObjects(objects...).
				Build()

			r := &_resource{client: fakeClient}
			result, err := r.isPodGangScheduled(context.Background(), logr.Discard(), testNamespace, pg)

			if tc.expectError {
				require.Error(t, err)
				assert.False(t, result)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectedScheduled, result)
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

func buildPodGangWithRef(pgName, podName string) *groveschedulerv1alpha1.PodGang {
	return testutils.NewPodGangBuilder(pgName, testNamespace).
		WithPodGroups([]groveschedulerv1alpha1.PodGroup{
			{
				Name:        pclqFQN,
				MinReplicas: 1,
				PodReferences: []groveschedulerv1alpha1.NamespacedName{
					{Namespace: testNamespace, Name: podName},
				},
			},
		}).Build()
}

func buildTestPodClique(name string, minAvailable, scheduledReplicas int32) *grovecorev1alpha1.PodClique {
	return &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec:       grovecorev1alpha1.PodCliqueSpec{MinAvailable: ptr.To(minAvailable)},
		Status:     grovecorev1alpha1.PodCliqueStatus{ScheduledReplicas: scheduledReplicas},
	}
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

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
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/expect"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// -----------------------------------------------------------------------------
// latestPodGangName
// -----------------------------------------------------------------------------

func TestLatestPodGangName(t *testing.T) {
	t.Run("empty mapping returns empty name and no error", func(t *testing.T) {
		got, err := latestPodGangName(map[string]int32{})
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("single entry", func(t *testing.T) {
		got, err := latestPodGangName(map[string]int32{"workload1-0-3": 2})
		require.NoError(t, err)
		assert.Equal(t, "workload1-0-3", got)
	})

	t.Run("highest counter wins", func(t *testing.T) {
		got, err := latestPodGangName(map[string]int32{
			"workload1-0-0": 2,
			"workload1-0-3": 1,
			"workload1-0-1": 5,
		})
		require.NoError(t, err)
		assert.Equal(t, "workload1-0-3", got)
	})

	t.Run("numeric not lexicographic ordering on multi-digit counters", func(t *testing.T) {
		// Lexicographic sort would place "...10" before "...2"; numeric must place "...10" after "...2".
		got, err := latestPodGangName(map[string]int32{
			"workload1-0-2":  1,
			"workload1-0-10": 1,
			"workload1-0-9":  1,
		})
		require.NoError(t, err)
		assert.Equal(t, "workload1-0-10", got)
	})

	t.Run("malformed name returns error", func(t *testing.T) {
		_, err := latestPodGangName(map[string]int32{
			"workload1-0-1":      1,
			"malformed-no-int-x": 1,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "malformed-no-int-x")
	})
}

// -----------------------------------------------------------------------------
// nonTerminatingPods
// -----------------------------------------------------------------------------

func TestNonTerminatingPods(t *testing.T) {
	now := metav1.NewTime(time.Now())
	pod := func(name string, terminating bool) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace}}
		if terminating {
			p.DeletionTimestamp = &now
			p.Finalizers = []string{"keepalive"}
		}
		return p
	}
	t.Run("all non-terminating", func(t *testing.T) {
		pods := []*corev1.Pod{pod("a", false), pod("b", false)}
		got := nonTerminatingPods(pods)
		assert.Len(t, got, 2)
	})
	t.Run("all terminating filtered out", func(t *testing.T) {
		pods := []*corev1.Pod{pod("a", true), pod("b", true)}
		got := nonTerminatingPods(pods)
		assert.Empty(t, got)
	})
	t.Run("mixed", func(t *testing.T) {
		pods := []*corev1.Pod{pod("a", false), pod("b", true), pod("c", false)}
		got := nonTerminatingPods(pods)
		require.Len(t, got, 2)
		assert.Equal(t, "a", got[0].Name)
		assert.Equal(t, "c", got[1].Name)
	})
}

// -----------------------------------------------------------------------------
// buildLivePodGangMapping
// -----------------------------------------------------------------------------

func TestBuildLivePodGangMapping(t *testing.T) {
	now := metav1.NewTime(time.Now())
	mkPod := func(name, pgName string, terminating bool) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels:    map[string]string{},
		}}
		if pgName != "" {
			p.Labels[apicommon.LabelPodGang] = pgName
		}
		if terminating {
			p.DeletionTimestamp = &now
			p.Finalizers = []string{"keepalive"}
		}
		return p
	}
	pods := []*corev1.Pod{
		mkPod("p1", "pg-0", false),
		mkPod("p2", "pg-0", false),
		mkPod("p3", "pg-1", false),
		mkPod("p4", "pg-1", true), // terminating, must be skipped
		mkPod("p5", "", false),    // missing label, must be skipped
	}
	got := buildLivePodGangMapping(pods)
	assert.Equal(t, map[string]int32{"pg-0": 2, "pg-1": 1}, got)
}

// -----------------------------------------------------------------------------
// groupNonTerminatingPodsByPodGang
// -----------------------------------------------------------------------------

func TestGroupNonTerminatingPodsByPodGang(t *testing.T) {
	now := metav1.NewTime(time.Now())
	mkPod := func(name, pgName string, terminating bool) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels:    map[string]string{},
		}}
		if pgName != "" {
			p.Labels[apicommon.LabelPodGang] = pgName
		}
		if terminating {
			p.DeletionTimestamp = &now
			p.Finalizers = []string{"keepalive"}
		}
		return p
	}
	pods := []*corev1.Pod{
		mkPod("p1", "pg-0", false),
		mkPod("p2", "pg-0", false),
		mkPod("p3", "pg-1", false),
		mkPod("p4", "pg-1", true), // skipped: terminating
		mkPod("p5", "", false),    // skipped: no label
	}
	got := groupNonTerminatingPodsByPodGang(pods)
	require.Len(t, got, 2)
	assert.Len(t, got["pg-0"], 2)
	assert.Len(t, got["pg-1"], 1)
}

// -----------------------------------------------------------------------------
// computePerPodGangDeltas
// -----------------------------------------------------------------------------

func TestComputePerPodGangDeltas(t *testing.T) {
	t.Run("both empty", func(t *testing.T) {
		assert.Empty(t, computePerPodGangDeltas(map[string]int32{}, map[string]int32{}))
	})
	t.Run("desired only — all positive", func(t *testing.T) {
		got := computePerPodGangDeltas(map[string]int32{"pg-0": 2, "pg-1": 1}, nil)
		assert.Equal(t, map[string]int32{"pg-0": 2, "pg-1": 1}, got)
	})
	t.Run("current only — all negative", func(t *testing.T) {
		got := computePerPodGangDeltas(nil, map[string]int32{"pg-0": 3})
		assert.Equal(t, map[string]int32{"pg-0": -3}, got)
	})
	t.Run("matching keys with equal counts dropped", func(t *testing.T) {
		got := computePerPodGangDeltas(
			map[string]int32{"pg-0": 2},
			map[string]int32{"pg-0": 2},
		)
		assert.Empty(t, got)
	})
	t.Run("mixed deltas", func(t *testing.T) {
		got := computePerPodGangDeltas(
			map[string]int32{"pg-0": 2, "pg-1": 1, "pg-2": 4}, // pg-2 only in desired
			map[string]int32{"pg-0": 1, "pg-1": 3, "pg-3": 2}, // pg-3 only in current
		)
		assert.Equal(t, map[string]int32{
			"pg-0": 1,  // 2 - 1
			"pg-1": -2, // 1 - 3
			"pg-2": 4,  // not in current
			"pg-3": -2, // not in desired
		}, got)
	})
}

// -----------------------------------------------------------------------------
// decrementMappingForScaleIn
// -----------------------------------------------------------------------------

func TestDecrementMappingForScaleIn(t *testing.T) {
	mkPod := func(name, pgName string) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels:    map[string]string{},
			UID:       types.UID(name),
		}}
		if pgName != "" {
			p.Labels[apicommon.LabelPodGang] = pgName
		}
		return p
	}

	t.Run("count <= 0 is no-op", func(t *testing.T) {
		desired := map[string]int32{"pg-0": 2}
		decrementMappingForScaleIn(desired, []*corev1.Pod{mkPod("p1", "pg-0")}, "", 0)
		assert.Equal(t, map[string]int32{"pg-0": 2}, desired)
	})

	t.Run("no live pods is no-op", func(t *testing.T) {
		desired := map[string]int32{"pg-0": 2}
		decrementMappingForScaleIn(desired, nil, "", 1)
		assert.Equal(t, map[string]int32{"pg-0": 2}, desired)
	})

	t.Run("decrement count from a single PodGang", func(t *testing.T) {
		desired := map[string]int32{"pg-0": 3}
		pods := []*corev1.Pod{mkPod("p1", "pg-0"), mkPod("p2", "pg-0"), mkPod("p3", "pg-0")}
		decrementMappingForScaleIn(desired, pods, "", 2)
		assert.Equal(t, int32(1), desired["pg-0"])
	})

	t.Run("count clamped to len(pods)", func(t *testing.T) {
		desired := map[string]int32{"pg-0": 5}
		pods := []*corev1.Pod{mkPod("p1", "pg-0"), mkPod("p2", "pg-0")}
		decrementMappingForScaleIn(desired, pods, "", 10)
		// Only 2 pods available; mapping decrements by 2, not 10.
		assert.Equal(t, int32(3), desired["pg-0"])
	})

	t.Run("pod missing PodGang label is skipped without consuming budget", func(t *testing.T) {
		desired := map[string]int32{"pg-0": 3}
		pods := []*corev1.Pod{
			mkPod("p1", ""),     // skipped, doesn't consume budget
			mkPod("p2", "pg-0"), // consumes 1
			mkPod("p3", "pg-0"), // consumes 1
		}
		decrementMappingForScaleIn(desired, pods, "", 2)
		// Both decrements landed on pg-0 (count goes 3 → 1), the unlabeled pod was skipped.
		assert.Equal(t, int32(1), desired["pg-0"])
	})

	t.Run("does not decrement entry already at zero", func(t *testing.T) {
		desired := map[string]int32{"pg-0": 0, "pg-1": 1}
		pods := []*corev1.Pod{mkPod("p1", "pg-0"), mkPod("p2", "pg-1")}
		decrementMappingForScaleIn(desired, pods, "", 2)
		// pg-0 stays at 0 (the live pod against pg-0 is silently dropped because budget allows it).
		assert.Equal(t, int32(0), desired["pg-0"])
		assert.Equal(t, int32(0), desired["pg-1"])
	})

	t.Run("terminating pods filtered out before sorting", func(t *testing.T) {
		now := metav1.NewTime(time.Now())
		terminating := mkPod("term", "pg-0")
		terminating.DeletionTimestamp = &now
		terminating.Finalizers = []string{"keepalive"}
		desired := map[string]int32{"pg-0": 2}
		decrementMappingForScaleIn(desired, []*corev1.Pod{terminating}, "", 1)
		assert.Equal(t, int32(2), desired["pg-0"])
	})
}

// -----------------------------------------------------------------------------
// buildMappingFromPodGangMap
// -----------------------------------------------------------------------------

func TestBuildMappingFromPodGangMap(t *testing.T) {
	t.Run("nil PGM returns empty mapping", func(t *testing.T) {
		r := &_resource{}
		sc := &syncContext{cliqueName: "pca"}
		got, err := r.buildMappingFromPodGangMap(sc)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("populates from entries that reference the clique", func(t *testing.T) {
		r := &_resource{}
		sc := &syncContext{
			cliqueName: "pca",
			pgm: &grovecorev1alpha1.PodGangMap{
				Spec: grovecorev1alpha1.PodGangMapSpec{
					Entries: []grovecorev1alpha1.PodGangEntry{
						{Name: "pg-0", PodCliques: map[string]int32{"pca": 2}},
						{Name: "pg-1", PodCliques: map[string]int32{"pca": 3}},
					},
				},
			},
		}
		got, err := r.buildMappingFromPodGangMap(sc)
		require.NoError(t, err)
		assert.Equal(t, map[string]int32{"pg-0": 2, "pg-1": 3}, got)
	})

	t.Run("skips entries that don't reference the clique", func(t *testing.T) {
		r := &_resource{}
		sc := &syncContext{
			cliqueName: "pca",
			pgm: &grovecorev1alpha1.PodGangMap{
				Spec: grovecorev1alpha1.PodGangMapSpec{
					Entries: []grovecorev1alpha1.PodGangEntry{
						{Name: "pg-0", PodCliques: map[string]int32{"pca": 2}},
						{Name: "pg-other", PodCliques: map[string]int32{"pcb": 5}},
					},
				},
			},
		}
		got, err := r.buildMappingFromPodGangMap(sc)
		require.NoError(t, err)
		assert.Equal(t, map[string]int32{"pg-0": 2}, got)
	})

	t.Run("skips entries with zero count", func(t *testing.T) {
		r := &_resource{}
		sc := &syncContext{
			cliqueName: "pca",
			pgm: &grovecorev1alpha1.PodGangMap{
				Spec: grovecorev1alpha1.PodGangMapSpec{
					Entries: []grovecorev1alpha1.PodGangEntry{
						{Name: "pg-0", PodCliques: map[string]int32{"pca": 0}},
						{Name: "pg-1", PodCliques: map[string]int32{"pca": 1}},
					},
				},
			},
		}
		got, err := r.buildMappingFromPodGangMap(sc)
		require.NoError(t, err)
		assert.Equal(t, map[string]int32{"pg-1": 1}, got)
	})
}

// -----------------------------------------------------------------------------
// computeDesiredPodGangMapping
// -----------------------------------------------------------------------------

func TestComputeDesiredPodGangMapping(t *testing.T) {
	const clique = "pca"
	mkPCS := func(updateInProgress bool) *grovecorev1alpha1.PodCliqueSet {
		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: testPCSName, Namespace: testNamespace},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy},
			},
		}
		if updateInProgress {
			pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
				UpdateStartedAt: metav1.NewTime(time.Now()),
			}
		}
		return pcs
	}
	mkPCLQ := func(replicas int32, status map[string]int32) *grovecorev1alpha1.PodClique {
		return &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{Name: pclqFQN, Namespace: testNamespace},
			Spec:       grovecorev1alpha1.PodCliqueSpec{Replicas: replicas},
			Status:     grovecorev1alpha1.PodCliqueStatus{PodGangMapping: status},
		}
	}
	mkPGM := func(entries ...grovecorev1alpha1.PodGangEntry) *grovecorev1alpha1.PodGangMap {
		return &grovecorev1alpha1.PodGangMap{Spec: grovecorev1alpha1.PodGangMapSpec{Entries: entries}}
	}

	t.Run("coherent update overwrites status from PGM", func(t *testing.T) {
		r := &_resource{}
		sc := &syncContext{
			cliqueName: clique,
			pcs:        mkPCS(true),
			pclq:       mkPCLQ(5, map[string]int32{"stale": 99}),
			pgm: mkPGM(
				grovecorev1alpha1.PodGangEntry{Name: "workload1-0-0", PodCliques: map[string]int32{clique: 2}},
				grovecorev1alpha1.PodGangEntry{Name: "workload1-0-1", PodCliques: map[string]int32{clique: 3}},
			),
		}
		got, err := r.computeDesiredPodGangMapping(sc)
		require.NoError(t, err)
		assert.Equal(t, map[string]int32{"workload1-0-0": 2, "workload1-0-1": 3}, got)
		// status field on input PCLQ is not mutated.
		assert.Equal(t, map[string]int32{"stale": 99}, sc.pclq.Status.PodGangMapping)
	})

	t.Run("steady state, status nil — seeds from PGM", func(t *testing.T) {
		r := &_resource{}
		sc := &syncContext{
			cliqueName: clique,
			pcs:        mkPCS(false),
			pclq:       mkPCLQ(3, nil),
			pgm: mkPGM(
				grovecorev1alpha1.PodGangEntry{Name: "workload1-0-0", PodCliques: map[string]int32{clique: 1}},
				grovecorev1alpha1.PodGangEntry{Name: "workload1-0-1", PodCliques: map[string]int32{clique: 2}},
			),
		}
		got, err := r.computeDesiredPodGangMapping(sc)
		require.NoError(t, err)
		assert.Equal(t, map[string]int32{"workload1-0-0": 1, "workload1-0-1": 2}, got)
	})

	t.Run("steady state, status nil + PGM nil — empty mapping with no spec-diff", func(t *testing.T) {
		r := &_resource{}
		sc := &syncContext{
			cliqueName: clique,
			pcs:        mkPCS(false),
			pclq:       mkPCLQ(0, nil),
			// pgm is nil
		}
		got, err := r.computeDesiredPodGangMapping(sc)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("steady state, status non-empty, no spec-diff — returned mapping equals status", func(t *testing.T) {
		r := &_resource{}
		status := map[string]int32{"workload1-0-0": 2, "workload1-0-1": 3}
		sc := &syncContext{
			cliqueName: clique,
			pcs:        mkPCS(false),
			pclq:       mkPCLQ(5, status),
		}
		got, err := r.computeDesiredPodGangMapping(sc)
		require.NoError(t, err)
		assert.Equal(t, status, got)
		// Returned map must be a clone — mutating it must not affect the input status.
		got["workload1-0-0"] = 99
		assert.Equal(t, int32(2), sc.pclq.Status.PodGangMapping["workload1-0-0"])
	})

	t.Run("steady state scale-out — increments highest-index PG", func(t *testing.T) {
		r := &_resource{}
		sc := &syncContext{
			cliqueName: clique,
			pcs:        mkPCS(false),
			pclq:       mkPCLQ(7, map[string]int32{"workload1-0-0": 2, "workload1-0-1": 3}),
		}
		got, err := r.computeDesiredPodGangMapping(sc)
		require.NoError(t, err)
		assert.Equal(t, map[string]int32{"workload1-0-0": 2, "workload1-0-1": 5}, got)
	})

	t.Run("steady state scale-out with empty seed — error", func(t *testing.T) {
		r := &_resource{}
		sc := &syncContext{
			cliqueName: clique,
			pcs:        mkPCS(false),
			pclq:       mkPCLQ(2, nil),
			// pgm nil → seed is empty; spec-diff > 0 with no entry to attach → error
		}
		_, err := r.computeDesiredPodGangMapping(sc)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty after seed")
	})

	t.Run("steady state scale-in — deletion sorter decrements per chosen pod", func(t *testing.T) {
		r := &_resource{}
		mkPod := func(name, pgName string) *corev1.Pod {
			return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: testNamespace,
				UID:       types.UID(name),
				Labels:    map[string]string{apicommon.LabelPodGang: pgName},
			}}
		}
		sc := &syncContext{
			cliqueName: clique,
			pcs:        mkPCS(false),
			pclq:       mkPCLQ(3, map[string]int32{"workload1-0-0": 2, "workload1-0-1": 3}),
			existingPCLQPods: []*corev1.Pod{
				mkPod("p0", "workload1-0-0"),
				mkPod("p1", "workload1-0-0"),
				mkPod("p2", "workload1-0-1"),
				mkPod("p3", "workload1-0-1"),
				mkPod("p4", "workload1-0-1"),
			},
		}
		got, err := r.computeDesiredPodGangMapping(sc)
		require.NoError(t, err)
		// 5 pods, spec wants 3, so 2 must be decremented somewhere.
		var sum int32
		for _, v := range got {
			sum += v
		}
		assert.Equal(t, int32(3), sum)
	})

	t.Run("malformed name in status fails scale-out path", func(t *testing.T) {
		r := &_resource{}
		sc := &syncContext{
			cliqueName: clique,
			pcs:        mkPCS(false),
			pclq:       mkPCLQ(5, map[string]int32{"malformed-no-int-x": 3}),
		}
		_, err := r.computeDesiredPodGangMapping(sc)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "malformed-no-int-x")
	})
}

// -----------------------------------------------------------------------------
// patchPodGangMapping
// -----------------------------------------------------------------------------

func TestPatchPodGangMapping(t *testing.T) {
	mkPCLQ := func(status map[string]int32) *grovecorev1alpha1.PodClique {
		return &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pclqFQN,
				Namespace:       testNamespace,
				ResourceVersion: "1",
			},
			Status: grovecorev1alpha1.PodCliqueStatus{PodGangMapping: status},
		}
	}

	t.Run("equal mapping — no patch", func(t *testing.T) {
		pclq := mkPCLQ(map[string]int32{"pg-0": 1})
		cl := fake.NewClientBuilder().WithScheme(buildTestScheme(t)).WithObjects(pclq).WithStatusSubresource(pclq).Build()
		r := &_resource{client: cl}
		sc := &syncContext{ctx: context.Background(), pclq: pclq}

		err := r.patchPodGangMapping(sc, map[string]int32{"pg-0": 1})
		require.NoError(t, err)
		// resourceVersion unchanged confirms no patch was issued.
		fresh := &grovecorev1alpha1.PodClique{}
		require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pclq), fresh))
		assert.Equal(t, "1", fresh.ResourceVersion)
	})

	t.Run("different mapping — patch issued", func(t *testing.T) {
		pclq := mkPCLQ(map[string]int32{"pg-0": 1})
		cl := fake.NewClientBuilder().WithScheme(buildTestScheme(t)).WithObjects(pclq).WithStatusSubresource(pclq).Build()
		r := &_resource{client: cl}
		sc := &syncContext{ctx: context.Background(), pclq: pclq}

		err := r.patchPodGangMapping(sc, map[string]int32{"pg-0": 2, "pg-1": 1})
		require.NoError(t, err)
		fresh := &grovecorev1alpha1.PodClique{}
		require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pclq), fresh))
		assert.Equal(t, map[string]int32{"pg-0": 2, "pg-1": 1}, fresh.Status.PodGangMapping)
	})

	t.Run("empty desired normalizes to nil status", func(t *testing.T) {
		pclq := mkPCLQ(map[string]int32{"pg-0": 1})
		cl := fake.NewClientBuilder().WithScheme(buildTestScheme(t)).WithObjects(pclq).WithStatusSubresource(pclq).Build()
		r := &_resource{client: cl}
		sc := &syncContext{ctx: context.Background(), pclq: pclq}

		err := r.patchPodGangMapping(sc, map[string]int32{})
		require.NoError(t, err)
		fresh := &grovecorev1alpha1.PodClique{}
		require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pclq), fresh))
		assert.Nil(t, fresh.Status.PodGangMapping)
	})

	t.Run("nil status, nil desired — no patch", func(t *testing.T) {
		pclq := mkPCLQ(nil)
		cl := fake.NewClientBuilder().WithScheme(buildTestScheme(t)).WithObjects(pclq).WithStatusSubresource(pclq).Build()
		r := &_resource{client: cl}
		sc := &syncContext{ctx: context.Background(), pclq: pclq}

		err := r.patchPodGangMapping(sc, nil)
		require.NoError(t, err)
		fresh := &grovecorev1alpha1.PodClique{}
		require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pclq), fresh))
		assert.Equal(t, "1", fresh.ResourceVersion)
	})

	t.Run("PCLQ deleted from server — patch swallowed via IgnoreNotFound", func(t *testing.T) {
		pclq := mkPCLQ(map[string]int32{"pg-0": 1})
		// fake client has no objects; patch will fail with NotFound and must be swallowed.
		cl := fake.NewClientBuilder().WithScheme(buildTestScheme(t)).WithStatusSubresource(&grovecorev1alpha1.PodClique{}).Build()
		r := &_resource{client: cl}
		sc := &syncContext{ctx: context.Background(), pclq: pclq}

		err := r.patchPodGangMapping(sc, map[string]int32{"pg-0": 2})
		require.NoError(t, err)
	})

	t.Run("zero-count entry pruned before persistence", func(t *testing.T) {
		// Scale-in scenario: decrementMappingForScaleIn took a count to 0. The pruning at the
		// persistence layer must drop the dead entry so subsequent scale-out logic
		// (latestPodGangName) does not target a phantom PodGang.
		pclq := mkPCLQ(map[string]int32{"pg-0": 1})
		cl := fake.NewClientBuilder().WithScheme(buildTestScheme(t)).WithObjects(pclq).WithStatusSubresource(pclq).Build()
		r := &_resource{client: cl}
		sc := &syncContext{ctx: context.Background(), pclq: pclq}

		err := r.patchPodGangMapping(sc, map[string]int32{"pg-0": 1, "pg-1": 0})
		require.NoError(t, err)
		fresh := &grovecorev1alpha1.PodClique{}
		require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pclq), fresh))
		assert.Equal(t, map[string]int32{"pg-0": 1}, fresh.Status.PodGangMapping, "zero-count entry must be pruned")
	})

	t.Run("only-zero-count entries normalize to nil", func(t *testing.T) {
		pclq := mkPCLQ(map[string]int32{"pg-0": 1})
		cl := fake.NewClientBuilder().WithScheme(buildTestScheme(t)).WithObjects(pclq).WithStatusSubresource(pclq).Build()
		r := &_resource{client: cl}
		sc := &syncContext{ctx: context.Background(), pclq: pclq}

		err := r.patchPodGangMapping(sc, map[string]int32{"pg-0": 0, "pg-1": 0})
		require.NoError(t, err)
		fresh := &grovecorev1alpha1.PodClique{}
		require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pclq), fresh))
		assert.Nil(t, fresh.Status.PodGangMapping, "all-zero map must collapse to nil")
	})

	t.Run("equal-after-prune triggers no patch", func(t *testing.T) {
		// Stored mapping is already pruned. Caller passes the same shape with a stale zero entry
		// from in-memory math. Prune must equalize to existing and skip the patch.
		pclq := mkPCLQ(map[string]int32{"pg-0": 2})
		cl := fake.NewClientBuilder().WithScheme(buildTestScheme(t)).WithObjects(pclq).WithStatusSubresource(pclq).Build()
		r := &_resource{client: cl}
		sc := &syncContext{ctx: context.Background(), pclq: pclq}

		err := r.patchPodGangMapping(sc, map[string]int32{"pg-0": 2, "pg-stale": 0})
		require.NoError(t, err)
		fresh := &grovecorev1alpha1.PodClique{}
		require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pclq), fresh))
		assert.Equal(t, "1", fresh.ResourceVersion, "no patch issued when pruned-desired equals current")
	})
}

// -----------------------------------------------------------------------------
// reconcileStandalonePCLQDistribution — end-to-end behaviour
// -----------------------------------------------------------------------------

func TestReconcileStandalonePCLQDistribution_NoOp(t *testing.T) {
	// Steady state, status matches spec; current pods match status; nothing to do.
	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pclqFQN,
			Namespace:       testNamespace,
			ResourceVersion: "1",
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2},
		Status: grovecorev1alpha1.PodCliqueStatus{
			PodGangMapping: map[string]int32{"workload1-0-0": 2},
		},
	}
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: testPCSName, Namespace: testNamespace},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy},
		},
	}
	mkPod := func(name, pg string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			UID:       types.UID(name),
			Labels:    map[string]string{apicommon.LabelPodGang: pg},
		}}
	}
	cl := fake.NewClientBuilder().WithScheme(buildTestScheme(t)).WithObjects(pclq).WithStatusSubresource(pclq).Build()
	r := &_resource{
		client:            cl,
		expectationsStore: expect.NewExpectationsStore(),
	}
	sc := &syncContext{
		ctx:                      context.Background(),
		pcs:                      pcs,
		pclq:                     pclq,
		isStandalonePCLQ:         true,
		cliqueName:               "pca",
		pclqExpectationsStoreKey: "key",
		existingPCLQPods: []*corev1.Pod{
			mkPod("p0", "workload1-0-0"),
			mkPod("p1", "workload1-0-0"),
		},
	}
	err := r.reconcileStandalonePCLQDistribution(logr.Discard(), sc)
	require.NoError(t, err)

	fresh := &grovecorev1alpha1.PodClique{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pclq), fresh))
	// No patch → resourceVersion still 1.
	assert.Equal(t, "1", fresh.ResourceVersion)
	assert.Equal(t, map[string]int32{"workload1-0-0": 2}, fresh.Status.PodGangMapping)
}

func TestReconcileStandalonePCLQDistribution_CoherentRealign(t *testing.T) {
	// Coherent update in progress; status had stale entry; PGM is the new source of truth.
	// Pods already match the PGM-derived desired distribution so no create/delete tasks run —
	// the test focuses on the status realignment, the core behavior of the coherent branch.
	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pclqFQN,
			Namespace:       testNamespace,
			ResourceVersion: "1",
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2},
		Status: grovecorev1alpha1.PodCliqueStatus{
			PodGangMapping: map[string]int32{"workload1-0-stale-0": 99},
		},
	}
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: testPCSName, Namespace: testNamespace},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy},
		},
		Status: grovecorev1alpha1.PodCliqueSetStatus{
			UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
				UpdateStartedAt: metav1.NewTime(time.Now()),
			},
		},
	}
	pgm := &grovecorev1alpha1.PodGangMap{
		Spec: grovecorev1alpha1.PodGangMapSpec{
			Entries: []grovecorev1alpha1.PodGangEntry{
				{Name: "workload1-0-0", PodCliques: map[string]int32{"pca": 1}},
				{Name: "workload1-0-1", PodCliques: map[string]int32{"pca": 1}},
			},
		},
	}
	mkPod := func(name, pg string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			UID:       types.UID(name),
			Labels:    map[string]string{apicommon.LabelPodGang: pg},
		}}
	}
	cl := fake.NewClientBuilder().WithScheme(buildTestScheme(t)).WithObjects(pclq).WithStatusSubresource(pclq).Build()
	r := &_resource{
		client:            cl,
		expectationsStore: expect.NewExpectationsStore(),
	}
	sc := &syncContext{
		ctx:                      context.Background(),
		pcs:                      pcs,
		pclq:                     pclq,
		pgm:                      pgm,
		isStandalonePCLQ:         true,
		cliqueName:               "pca",
		pclqExpectationsStoreKey: "key",
		existingPCLQPods: []*corev1.Pod{
			mkPod("p0", "workload1-0-0"),
			mkPod("p1", "workload1-0-1"),
		},
	}

	err := r.reconcileStandalonePCLQDistribution(logr.Discard(), sc)
	require.NoError(t, err)

	fresh := &grovecorev1alpha1.PodClique{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pclq), fresh))
	assert.Equal(t, map[string]int32{"workload1-0-0": 1, "workload1-0-1": 1}, fresh.Status.PodGangMapping)
}

// -----------------------------------------------------------------------------
// guardAgainstStaleSpecDuringCoherentUpdate
// -----------------------------------------------------------------------------

// TestGuardAgainstStaleSpecDuringCoherentUpdate covers the gate that requeues when the PCLQ cache
// view is stale relative to an in-flight coherent update. The fixture exercises the three states
// described on the function plus the negative-paths where the gate must not fire.
func TestGuardAgainstStaleSpecDuringCoherentUpdate(t *testing.T) {
	const (
		oldHash    = "oldgen"
		newHash    = "newgen"
		oldTpl     = "oldtpl"
		newTpl     = "newtpl"
		cliqueName = "pca"
	)
	mkPCS := func(strategy grovecorev1alpha1.UpdateStrategyType, curGenHash string, updatedStandalone []string) *grovecorev1alpha1.PodCliqueSet {
		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: testPCSName, Namespace: testNamespace},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: strategy},
			},
		}
		if curGenHash != "" {
			pcs.Status.CurrentGenerationHash = ptr.To(curGenHash)
		}
		if updatedStandalone != nil {
			pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
				UpdateStartedAt:             metav1.Now(),
				UpdatedStandalonePodCliques: updatedStandalone,
			}
		}
		return pcs
	}
	mkPCLQ := func(labelTpl string, curTpl *string, progress *grovecorev1alpha1.PodCliqueUpdateProgress) *grovecorev1alpha1.PodClique {
		return &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pclqFQN,
				Namespace: testNamespace,
				Labels:    map[string]string{apicommon.LabelPodTemplateHash: labelTpl},
			},
			Status: grovecorev1alpha1.PodCliqueStatus{
				CurrentPodTemplateHash: curTpl,
				UpdateProgress:         progress,
			},
		}
	}

	tests := []struct {
		name       string
		pcs        *grovecorev1alpha1.PodCliqueSet
		pclq       *grovecorev1alpha1.PodClique
		expectFire bool
	}{
		{
			name: "RollingRecreate strategy bypasses guard",
			pcs:  mkPCS(grovecorev1alpha1.RollingRecreateStrategy, newHash, nil),
			pclq: mkPCLQ(oldTpl, ptr.To(oldTpl), nil),
		},
		{
			name: "Coherent update not in progress (no UpdateProgress) bypasses guard",
			pcs:  mkPCS(grovecorev1alpha1.CoherentStrategy, newHash, nil),
			pclq: mkPCLQ(oldTpl, ptr.To(oldTpl), nil),
		},
		{
			name: "Clique not in UpdatedStandalonePodCliques bypasses guard",
			pcs:  mkPCS(grovecorev1alpha1.CoherentStrategy, newHash, []string{"other-clique"}),
			pclq: mkPCLQ(oldTpl, ptr.To(oldTpl), nil),
		},
		{
			name: "Status.CurrentPodTemplateHash nil bypasses guard (PCLQ never reconciled)",
			pcs:  mkPCS(grovecorev1alpha1.CoherentStrategy, newHash, []string{cliqueName}),
			pclq: mkPCLQ(oldTpl, nil, nil),
		},
		{
			name: "State 2 (roll in progress, label != status) bypasses guard",
			pcs:  mkPCS(grovecorev1alpha1.CoherentStrategy, newHash, []string{cliqueName}),
			pclq: mkPCLQ(newTpl, ptr.To(oldTpl), &grovecorev1alpha1.PodCliqueUpdateProgress{
				UpdateStartedAt:            metav1.Now(),
				PodCliqueSetGenerationHash: newHash,
				PodTemplateHash:            newTpl,
			}),
		},
		{
			name: "State 3 (roll complete for current gen) bypasses guard",
			pcs:  mkPCS(grovecorev1alpha1.CoherentStrategy, newHash, []string{cliqueName}),
			pclq: mkPCLQ(newTpl, ptr.To(newTpl), &grovecorev1alpha1.PodCliqueUpdateProgress{
				UpdateStartedAt:            metav1.Now(),
				UpdateEndedAt:              &metav1.Time{Time: time.Now()},
				PodCliqueSetGenerationHash: newHash,
				PodTemplateHash:            newTpl,
			}),
		},
		{
			name:       "State 1 (cache stale, label == status, no UpdateProgress) requeues",
			pcs:        mkPCS(grovecorev1alpha1.CoherentStrategy, newHash, []string{cliqueName}),
			pclq:       mkPCLQ(oldTpl, ptr.To(oldTpl), nil),
			expectFire: true,
		},
		{
			name: "State 1 (UpdateEndedAt set but for previous PCS generation) requeues",
			// UpdateProgress lingers from a prior PCS update at oldHash; fresh update at newHash
			// hasn't been observed yet, so label==status==oldTpl. Guard must not mistake the
			// stale UpdateEndedAt for completion of the current generation.
			pcs: mkPCS(grovecorev1alpha1.CoherentStrategy, newHash, []string{cliqueName}),
			pclq: mkPCLQ(oldTpl, ptr.To(oldTpl), &grovecorev1alpha1.PodCliqueUpdateProgress{
				UpdateStartedAt:            metav1.Now(),
				UpdateEndedAt:              &metav1.Time{Time: time.Now()},
				PodCliqueSetGenerationHash: oldHash,
				PodTemplateHash:            oldTpl,
			}),
			expectFire: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sc := &syncContext{pcs: tc.pcs, pclq: tc.pclq, cliqueName: cliqueName}
			err := guardAgainstStaleSpecDuringCoherentUpdate(sc)
			if tc.expectFire {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestIsPCLQUpdateEndedForCurrentPCSGeneration exercises the helper's pointer-safety and
// generation-tagging logic in isolation.
func TestIsPCLQUpdateEndedForCurrentPCSGeneration(t *testing.T) {
	now := metav1.Time{Time: time.Now()}
	pcs := func(curHash *string) *grovecorev1alpha1.PodCliqueSet {
		return &grovecorev1alpha1.PodCliqueSet{Status: grovecorev1alpha1.PodCliqueSetStatus{CurrentGenerationHash: curHash}}
	}
	pclq := func(progress *grovecorev1alpha1.PodCliqueUpdateProgress) *grovecorev1alpha1.PodClique {
		return &grovecorev1alpha1.PodClique{Status: grovecorev1alpha1.PodCliqueStatus{UpdateProgress: progress}}
	}

	t.Run("nil UpdateProgress returns false", func(t *testing.T) {
		assert.False(t, isPCLQUpdateEndedForCurrentPCSGeneration(pcs(ptr.To("hash")), pclq(nil)))
	})
	t.Run("UpdateEndedAt nil returns false", func(t *testing.T) {
		assert.False(t, isPCLQUpdateEndedForCurrentPCSGeneration(pcs(ptr.To("hash")),
			pclq(&grovecorev1alpha1.PodCliqueUpdateProgress{PodCliqueSetGenerationHash: "hash"})))
	})
	t.Run("PCS CurrentGenerationHash nil returns false", func(t *testing.T) {
		assert.False(t, isPCLQUpdateEndedForCurrentPCSGeneration(pcs(nil),
			pclq(&grovecorev1alpha1.PodCliqueUpdateProgress{UpdateEndedAt: &now, PodCliqueSetGenerationHash: "hash"})))
	})
	t.Run("hash mismatch returns false (stale prior-update record)", func(t *testing.T) {
		assert.False(t, isPCLQUpdateEndedForCurrentPCSGeneration(pcs(ptr.To("new-hash")),
			pclq(&grovecorev1alpha1.PodCliqueUpdateProgress{UpdateEndedAt: &now, PodCliqueSetGenerationHash: "old-hash"})))
	})
	t.Run("ended for current generation returns true", func(t *testing.T) {
		assert.True(t, isPCLQUpdateEndedForCurrentPCSGeneration(pcs(ptr.To("hash")),
			pclq(&grovecorev1alpha1.PodCliqueUpdateProgress{UpdateEndedAt: &now, PodCliqueSetGenerationHash: "hash"})))
	})
}

// /*
// Copyright 2026 The Grove Authors.
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
// buildMappingFromPodGangMap
// -----------------------------------------------------------------------------

func TestBuildMappingFromPodGangMap(t *testing.T) {
	const hash = "gen"
	mkSC := func(entries ...grovecorev1alpha1.PodGangEntry) *syncContext {
		sc := &syncContext{
			cliqueName:      "pca",
			pcsReplicaIndex: 0,
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: testPCSName, Namespace: testNamespace},
				Status:     grovecorev1alpha1.PodCliqueSetStatus{CurrentGenerationHash: ptr.To(hash)},
			},
		}
		if entries != nil {
			sc.pgm = &grovecorev1alpha1.PodGangMap{Spec: grovecorev1alpha1.PodGangMapSpec{Entries: entries}}
		}
		return sc
	}
	anchorName := func(anchorIndex int32) string {
		return apicommon.GenerateAnchorPodGangName(apicommon.ResourceNameReplica{Name: testPCSName, Replica: 0}, hash, anchorIndex)
	}

	t.Run("nil PGM returns empty mapping", func(t *testing.T) {
		r := &_resource{}
		assert.Empty(t, r.buildMappingFromPodGangMap(mkSC()))
	})

	t.Run("populates from entries that reference the clique", func(t *testing.T) {
		r := &_resource{}
		got := r.buildMappingFromPodGangMap(mkSC(
			grovecorev1alpha1.PodGangEntry{Epoch: "100", AnchorIndex: 0, PodCliques: map[string]int32{"pca": 2}},
			grovecorev1alpha1.PodGangEntry{Epoch: "200", AnchorIndex: 1, PodCliques: map[string]int32{"pca": 3}},
		))
		assert.ElementsMatch(t, []grovecorev1alpha1.PodGangPodCountAssignment{
			{PodGangName: anchorName(0), Epoch: "100", PodCount: 2},
			{PodGangName: anchorName(1), Epoch: "200", PodCount: 3},
		}, got)
	})

	t.Run("skips entries that don't reference the clique", func(t *testing.T) {
		r := &_resource{}
		got := r.buildMappingFromPodGangMap(mkSC(
			grovecorev1alpha1.PodGangEntry{Epoch: "100", AnchorIndex: 0, PodCliques: map[string]int32{"pca": 2}},
			grovecorev1alpha1.PodGangEntry{Epoch: "200", AnchorIndex: 1, PodCliques: map[string]int32{"pcb": 5}},
		))
		assert.Equal(t, []grovecorev1alpha1.PodGangPodCountAssignment{
			{PodGangName: anchorName(0), Epoch: "100", PodCount: 2},
		}, got)
	})

	t.Run("skips entries with zero count", func(t *testing.T) {
		r := &_resource{}
		got := r.buildMappingFromPodGangMap(mkSC(
			grovecorev1alpha1.PodGangEntry{Epoch: "100", AnchorIndex: 0, PodCliques: map[string]int32{"pca": 0}},
			grovecorev1alpha1.PodGangEntry{Epoch: "200", AnchorIndex: 1, PodCliques: map[string]int32{"pca": 1}},
		))
		assert.Equal(t, []grovecorev1alpha1.PodGangPodCountAssignment{
			{PodGangName: anchorName(1), Epoch: "200", PodCount: 1},
		}, got)
	})
}

// -----------------------------------------------------------------------------
// computeDesiredPodGangMapping
// -----------------------------------------------------------------------------

func TestComputeDesiredPodGangMapping(t *testing.T) {
	const clique = "pca"
	const hash = "gen"
	rnr := apicommon.ResourceNameReplica{Name: testPCSName, Replica: 0}
	anchorName := func(idx int32) string { return apicommon.GenerateAnchorPodGangName(rnr, hash, idx) }

	mkPCS := func(updateInProgress bool) *grovecorev1alpha1.PodCliqueSet {
		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: testPCSName, Namespace: testNamespace},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy},
			},
			Status: grovecorev1alpha1.PodCliqueSetStatus{CurrentGenerationHash: ptr.To(hash)},
		}
		if updateInProgress {
			pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
				UpdateStartedAt: metav1.NewTime(time.Now()),
			}
		}
		return pcs
	}
	mkPCLQ := func(replicas int32, status []grovecorev1alpha1.PodGangPodCountAssignment) *grovecorev1alpha1.PodClique {
		return &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{Name: pclqFQN, Namespace: testNamespace},
			Spec:       grovecorev1alpha1.PodCliqueSpec{Replicas: replicas},
			Status:     grovecorev1alpha1.PodCliqueStatus{PodGangMapping: status},
		}
	}
	pgmWithAnchorEntries := func(entries ...grovecorev1alpha1.PodGangEntry) *grovecorev1alpha1.PodGangMap {
		return &grovecorev1alpha1.PodGangMap{Spec: grovecorev1alpha1.PodGangMapSpec{Entries: entries}}
	}
	anchorEntry := func(epoch string, idx int32, count int32) grovecorev1alpha1.PodGangEntry {
		return grovecorev1alpha1.PodGangEntry{Epoch: epoch, AnchorIndex: idx, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{clique: count}}
	}

	t.Run("coherent update overwrites status from PGM", func(t *testing.T) {
		r := &_resource{}
		sc := &syncContext{
			cliqueName: clique, pcsReplicaIndex: 0,
			pcs:  mkPCS(true),
			pclq: mkPCLQ(5, []grovecorev1alpha1.PodGangPodCountAssignment{{PodGangName: "stale", Epoch: "stale", PodCount: 99}}),
			pgm:  pgmWithAnchorEntries(anchorEntry("100", 0, 2), anchorEntry("200", 1, 3)),
		}
		got, err := r.computeDesiredPodGangMapping(sc)
		require.NoError(t, err)
		assert.ElementsMatch(t, []grovecorev1alpha1.PodGangPodCountAssignment{
			{PodGangName: anchorName(0), Epoch: "100", PodCount: 2},
			{PodGangName: anchorName(1), Epoch: "200", PodCount: 3},
		}, got)
		// input status not mutated.
		assert.Equal(t, []grovecorev1alpha1.PodGangPodCountAssignment{{PodGangName: "stale", Epoch: "stale", PodCount: 99}}, sc.pclq.Status.PodGangMapping)
	})

	t.Run("steady state, status nil — seeds from PGM (no spec-diff)", func(t *testing.T) {
		r := &_resource{}
		sc := &syncContext{
			cliqueName: clique, pcsReplicaIndex: 0,
			pcs:  mkPCS(false),
			pclq: mkPCLQ(3, nil), // 1+2 = 3, matches seed
			pgm:  pgmWithAnchorEntries(anchorEntry("100", 0, 1), anchorEntry("200", 1, 2)),
		}
		got, err := r.computeDesiredPodGangMapping(sc)
		require.NoError(t, err)
		assert.ElementsMatch(t, []grovecorev1alpha1.PodGangPodCountAssignment{
			{PodGangName: anchorName(0), Epoch: "100", PodCount: 1},
			{PodGangName: anchorName(1), Epoch: "200", PodCount: 2},
		}, got)
	})

	t.Run("steady state, status nil + PGM nil — empty mapping, no spec-diff", func(t *testing.T) {
		r := &_resource{}
		sc := &syncContext{cliqueName: clique, pcsReplicaIndex: 0, pcs: mkPCS(false), pclq: mkPCLQ(0, nil)}
		got, err := r.computeDesiredPodGangMapping(sc)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("steady state, status non-empty, no spec-diff — returns status clone", func(t *testing.T) {
		r := &_resource{}
		status := []grovecorev1alpha1.PodGangPodCountAssignment{
			{PodGangName: anchorName(0), Epoch: "100", PodCount: 2},
			{PodGangName: anchorName(1), Epoch: "200", PodCount: 3},
		}
		sc := &syncContext{cliqueName: clique, pcsReplicaIndex: 0, pcs: mkPCS(false), pclq: mkPCLQ(5, status)}
		got, err := r.computeDesiredPodGangMapping(sc)
		require.NoError(t, err)
		assert.ElementsMatch(t, status, got)
		// returned slice is a clone — mutating it must not affect input status.
		got[0].PodCount = 99
		assert.Equal(t, int32(2), sc.pclq.Status.PodGangMapping[0].PodCount)
	})

	t.Run("steady state scale-out — increments highest-AnchorIndex anchor", func(t *testing.T) {
		r := &_resource{}
		status := []grovecorev1alpha1.PodGangPodCountAssignment{
			{PodGangName: anchorName(0), Epoch: "100", PodCount: 2},
			{PodGangName: anchorName(1), Epoch: "200", PodCount: 3},
		}
		sc := &syncContext{
			cliqueName: clique, pcsReplicaIndex: 0,
			pcs:  mkPCS(false),
			pclq: mkPCLQ(7, status), // 5 -> 7, +2 to highest anchor (idx 1, epoch 200)
			pgm:  pgmWithAnchorEntries(anchorEntry("100", 0, 2), anchorEntry("200", 1, 3)),
		}
		got, err := r.computeDesiredPodGangMapping(sc)
		require.NoError(t, err)
		assert.ElementsMatch(t, []grovecorev1alpha1.PodGangPodCountAssignment{
			{PodGangName: anchorName(0), Epoch: "100", PodCount: 2},
			{PodGangName: anchorName(1), Epoch: "200", PodCount: 5},
		}, got)
	})

	t.Run("steady state scale-out with no anchor entry — error", func(t *testing.T) {
		r := &_resource{}
		sc := &syncContext{
			cliqueName: clique, pcsReplicaIndex: 0,
			pcs:  mkPCS(false),
			pclq: mkPCLQ(2, nil),
			pgm:  pgmWithAnchorEntries(), // no anchor entry; seed empty, diff>0 -> error
		}
		_, err := r.computeDesiredPodGangMapping(sc)
		require.Error(t, err)
	})

	t.Run("steady state scale-in — drains highest-AnchorIndex anchor first", func(t *testing.T) {
		r := &_resource{}
		status := []grovecorev1alpha1.PodGangPodCountAssignment{
			{PodGangName: anchorName(0), Epoch: "100", PodCount: 2},
			{PodGangName: anchorName(1), Epoch: "200", PodCount: 3},
		}
		sc := &syncContext{
			cliqueName: clique, pcsReplicaIndex: 0,
			pcs:  mkPCS(false),
			pclq: mkPCLQ(3, status), // 5 -> 3, drain 2 from highest anchor (idx 1)
			pgm:  pgmWithAnchorEntries(anchorEntry("100", 0, 2), anchorEntry("200", 1, 3)),
		}
		got, err := r.computeDesiredPodGangMapping(sc)
		require.NoError(t, err)
		assert.ElementsMatch(t, []grovecorev1alpha1.PodGangPodCountAssignment{
			{PodGangName: anchorName(0), Epoch: "100", PodCount: 2},
			{PodGangName: anchorName(1), Epoch: "200", PodCount: 1},
		}, got)
	})
}

// -----------------------------------------------------------------------------
// patchPodGangMapping
// -----------------------------------------------------------------------------

func TestPatchPodGangMapping(t *testing.T) {
	mkPCLQ := func(status []grovecorev1alpha1.PodGangPodCountAssignment) *grovecorev1alpha1.PodClique {
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
		pclq := mkPCLQ([]grovecorev1alpha1.PodGangPodCountAssignment{{PodGangName: "pg-0", Epoch: "100", PodCount: 1}})
		cl := fake.NewClientBuilder().WithScheme(buildTestScheme(t)).WithObjects(pclq).WithStatusSubresource(pclq).Build()
		r := &_resource{client: cl}
		sc := &syncContext{ctx: context.Background(), pclq: pclq}

		err := r.patchPodGangMapping(sc, []grovecorev1alpha1.PodGangPodCountAssignment{{PodGangName: "pg-0", Epoch: "100", PodCount: 1}})
		require.NoError(t, err)
		// resourceVersion unchanged confirms no patch was issued.
		fresh := &grovecorev1alpha1.PodClique{}
		require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pclq), fresh))
		assert.Equal(t, "1", fresh.ResourceVersion)
	})

	t.Run("different mapping — patch issued", func(t *testing.T) {
		pclq := mkPCLQ([]grovecorev1alpha1.PodGangPodCountAssignment{{PodGangName: "pg-0", Epoch: "100", PodCount: 1}})
		cl := fake.NewClientBuilder().WithScheme(buildTestScheme(t)).WithObjects(pclq).WithStatusSubresource(pclq).Build()
		r := &_resource{client: cl}
		sc := &syncContext{ctx: context.Background(), pclq: pclq}

		err := r.patchPodGangMapping(sc, []grovecorev1alpha1.PodGangPodCountAssignment{
			{PodGangName: "pg-0", Epoch: "100", PodCount: 2},
			{PodGangName: "pg-1", Epoch: "200", PodCount: 1},
		})
		require.NoError(t, err)
		fresh := &grovecorev1alpha1.PodClique{}
		require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pclq), fresh))
		assert.Equal(t, []grovecorev1alpha1.PodGangPodCountAssignment{
			{PodGangName: "pg-0", Epoch: "100", PodCount: 2},
			{PodGangName: "pg-1", Epoch: "200", PodCount: 1},
		}, fresh.Status.PodGangMapping)
	})

	t.Run("empty desired normalizes to nil status", func(t *testing.T) {
		pclq := mkPCLQ([]grovecorev1alpha1.PodGangPodCountAssignment{{PodGangName: "pg-0", Epoch: "100", PodCount: 1}})
		cl := fake.NewClientBuilder().WithScheme(buildTestScheme(t)).WithObjects(pclq).WithStatusSubresource(pclq).Build()
		r := &_resource{client: cl}
		sc := &syncContext{ctx: context.Background(), pclq: pclq}

		err := r.patchPodGangMapping(sc, []grovecorev1alpha1.PodGangPodCountAssignment{})
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
		pclq := mkPCLQ([]grovecorev1alpha1.PodGangPodCountAssignment{{PodGangName: "pg-0", Epoch: "100", PodCount: 1}})
		// fake client has no objects; patch will fail with NotFound and must be swallowed.
		cl := fake.NewClientBuilder().WithScheme(buildTestScheme(t)).WithStatusSubresource(&grovecorev1alpha1.PodClique{}).Build()
		r := &_resource{client: cl}
		sc := &syncContext{ctx: context.Background(), pclq: pclq}

		err := r.patchPodGangMapping(sc, []grovecorev1alpha1.PodGangPodCountAssignment{{PodGangName: "pg-0", Epoch: "100", PodCount: 2}})
		require.NoError(t, err)
	})

	t.Run("zero-count entry pruned before persistence", func(t *testing.T) {
		// Scale-in scenario: reducePodsForScaleIn took a count to 0. The pruning at the persistence
		// layer must drop the dead entry so it does not linger in status.
		pclq := mkPCLQ([]grovecorev1alpha1.PodGangPodCountAssignment{{PodGangName: "pg-0", Epoch: "100", PodCount: 1}})
		cl := fake.NewClientBuilder().WithScheme(buildTestScheme(t)).WithObjects(pclq).WithStatusSubresource(pclq).Build()
		r := &_resource{client: cl}
		sc := &syncContext{ctx: context.Background(), pclq: pclq}

		err := r.patchPodGangMapping(sc, []grovecorev1alpha1.PodGangPodCountAssignment{
			{PodGangName: "pg-0", Epoch: "100", PodCount: 1},
			{PodGangName: "pg-1", Epoch: "200", PodCount: 0},
		})
		require.NoError(t, err)
		fresh := &grovecorev1alpha1.PodClique{}
		require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pclq), fresh))
		assert.Equal(t, []grovecorev1alpha1.PodGangPodCountAssignment{{PodGangName: "pg-0", Epoch: "100", PodCount: 1}}, fresh.Status.PodGangMapping, "zero-count entry must be pruned")
	})

	t.Run("only-zero-count entries normalize to nil", func(t *testing.T) {
		pclq := mkPCLQ([]grovecorev1alpha1.PodGangPodCountAssignment{{PodGangName: "pg-0", Epoch: "100", PodCount: 1}})
		cl := fake.NewClientBuilder().WithScheme(buildTestScheme(t)).WithObjects(pclq).WithStatusSubresource(pclq).Build()
		r := &_resource{client: cl}
		sc := &syncContext{ctx: context.Background(), pclq: pclq}

		err := r.patchPodGangMapping(sc, []grovecorev1alpha1.PodGangPodCountAssignment{
			{PodGangName: "pg-0", Epoch: "100", PodCount: 0},
			{PodGangName: "pg-1", Epoch: "200", PodCount: 0},
		})
		require.NoError(t, err)
		fresh := &grovecorev1alpha1.PodClique{}
		require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pclq), fresh))
		assert.Nil(t, fresh.Status.PodGangMapping, "all-zero mapping must collapse to nil")
	})

	t.Run("equal-after-prune triggers no patch", func(t *testing.T) {
		// Stored mapping is already pruned. Caller passes the same shape with a stale zero entry
		// from in-memory math. Prune must equalize to existing and skip the patch.
		pclq := mkPCLQ([]grovecorev1alpha1.PodGangPodCountAssignment{{PodGangName: "pg-0", Epoch: "100", PodCount: 2}})
		cl := fake.NewClientBuilder().WithScheme(buildTestScheme(t)).WithObjects(pclq).WithStatusSubresource(pclq).Build()
		r := &_resource{client: cl}
		sc := &syncContext{ctx: context.Background(), pclq: pclq}

		err := r.patchPodGangMapping(sc, []grovecorev1alpha1.PodGangPodCountAssignment{
			{PodGangName: "pg-0", Epoch: "100", PodCount: 2},
			{PodGangName: "pg-stale", Epoch: "200", PodCount: 0},
		})
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
			PodGangMapping: []grovecorev1alpha1.PodGangPodCountAssignment{{PodGangName: "pg-0", Epoch: "100", PodCount: 2}},
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
			mkPod("p0", "pg-0"),
			mkPod("p1", "pg-0"),
		},
	}
	err := r.reconcileStandalonePCLQDistribution(logr.Discard(), sc)
	require.NoError(t, err)

	fresh := &grovecorev1alpha1.PodClique{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pclq), fresh))
	// No patch → resourceVersion still 1.
	assert.Equal(t, "1", fresh.ResourceVersion)
	assert.Equal(t, []grovecorev1alpha1.PodGangPodCountAssignment{{PodGangName: "pg-0", Epoch: "100", PodCount: 2}}, fresh.Status.PodGangMapping)
}

func TestReconcileStandalonePCLQDistribution_CoherentRealign(t *testing.T) {
	// Coherent update in progress; status had stale entry; PGM is the new source of truth.
	// Pods already match the PGM-derived desired distribution so no create/delete tasks run —
	// the test focuses on the status realignment, the core behavior of the coherent branch.
	const hash = "gen"
	rnr := apicommon.ResourceNameReplica{Name: testPCSName, Replica: 0}
	anchor0 := apicommon.GenerateAnchorPodGangName(rnr, hash, 0)
	anchor1 := apicommon.GenerateAnchorPodGangName(rnr, hash, 1)
	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pclqFQN,
			Namespace:       testNamespace,
			ResourceVersion: "1",
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2},
		Status: grovecorev1alpha1.PodCliqueStatus{
			PodGangMapping: []grovecorev1alpha1.PodGangPodCountAssignment{{PodGangName: "stale", Epoch: "stale", PodCount: 99}},
		},
	}
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: testPCSName, Namespace: testNamespace},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy},
		},
		Status: grovecorev1alpha1.PodCliqueSetStatus{
			CurrentGenerationHash: ptr.To(hash),
			UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
				UpdateStartedAt: metav1.NewTime(time.Now()),
			},
		},
	}
	pgm := &grovecorev1alpha1.PodGangMap{
		Spec: grovecorev1alpha1.PodGangMapSpec{
			Entries: []grovecorev1alpha1.PodGangEntry{
				{Epoch: "100", AnchorIndex: 0, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"pca": 1}},
				{Epoch: "200", AnchorIndex: 1, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"pca": 1}},
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
		pcsReplicaIndex:          0,
		isStandalonePCLQ:         true,
		cliqueName:               "pca",
		pclqExpectationsStoreKey: "key",
		existingPCLQPods: []*corev1.Pod{
			mkPod("p0", anchor0),
			mkPod("p1", anchor1),
		},
	}

	err := r.reconcileStandalonePCLQDistribution(logr.Discard(), sc)
	require.NoError(t, err)

	fresh := &grovecorev1alpha1.PodClique{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pclq), fresh))
	assert.ElementsMatch(t, []grovecorev1alpha1.PodGangPodCountAssignment{
		{PodGangName: anchor0, Epoch: "100", PodCount: 1},
		{PodGangName: anchor1, Epoch: "200", PodCount: 1},
	}, fresh.Status.PodGangMapping)
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
				InScopeStandalonePodCliques: updatedStandalone,
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
			name: "Clique not in InScopeStandalonePodCliques bypasses guard",
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

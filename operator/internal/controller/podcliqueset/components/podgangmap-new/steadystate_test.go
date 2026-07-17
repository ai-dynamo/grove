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

package podgangmap_new

import (
	"errors"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
)

const (
	testPCSName   = "my-pcs"
	testNamespace = "default"
	testHash      = "gen-hash-1"
)

func TestBuildBootstrapEntries(t *testing.T) {
	clk := clocktesting.NewFakeClock(time.Unix(0, 1000))

	t.Run("standalone PCLQs and PCSGs above minAvailable", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithStandaloneCliqueReplicas("frontend", 5).
			WithScalingGroupConfig("prefill", []string{"pworker"}, 4, 1).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()

		entries := buildBootstrapEntries(pcs, 0, clk)
		require.Len(t, entries, 2)

		anchor := anchorEntry(t, entries)
		assert.True(t, anchor.IsEpochAnchor)
		assert.Nil(t, anchor.DependsOn)
		assert.Equal(t, testHash, anchor.PodCliqueSetGenerationHash)
		assert.Equal(t, map[string]int32{"frontend": 5}, anchor.PodCliques)
		assert.Equal(t, map[string][]int32{"prefill": {0}}, anchor.PCSGReplicaIndices)

		tails := nonAnchorEntries(entries)
		require.Len(t, tails, 1)
		tail := tails[0]
		assert.Equal(t, map[string][]int32{"prefill": {1, 2, 3}}, tail.PCSGReplicaIndices)
		assert.Empty(t, tail.PodCliques)
		assert.Equal(t, []string{anchor.Labels[apicommon.LabelEpoch]}, tail.DependsOn)

		// Epoch is the batch identity: anchor and tail carry distinct epochs, tail = anchor+1.
		assert.NotEqual(t, anchor.Labels[apicommon.LabelEpoch], tail.Labels[apicommon.LabelEpoch])
		assert.Equal(t, "1000", anchor.Labels[apicommon.LabelEpoch])
		assert.Equal(t, "1001", tail.Labels[apicommon.LabelEpoch])
	})

	t.Run("PCSG total equals minAvailable emits no non-anchor entry", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithScalingGroupConfig("prefill", []string{"pworker"}, 2, 2).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()

		entries := buildBootstrapEntries(pcs, 0, clk)
		require.Len(t, entries, 1)
		anchor := anchorEntry(t, entries)
		assert.Equal(t, map[string][]int32{"prefill": {0, 1}}, anchor.PCSGReplicaIndices)
	})

	t.Run("standalone only, no PCSGs", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithStandaloneCliqueReplicas("frontend", 3).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()

		entries := buildBootstrapEntries(pcs, 0, clk)
		require.Len(t, entries, 1)
		anchor := anchorEntry(t, entries)
		assert.Equal(t, map[string]int32{"frontend": 3}, anchor.PodCliques)
		assert.Empty(t, anchor.PCSGReplicaIndices)
	})

	t.Run("multiple PCSGs above minAvailable each get a distinct non-anchor entry", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithScalingGroupConfig("prefill", []string{"pworker"}, 3, 1).
			WithScalingGroupConfig("decode", []string{"dworker"}, 2, 1).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()

		entries := buildBootstrapEntries(pcs, 0, clk)
		tails := nonAnchorEntries(entries)
		require.Len(t, tails, 2)

		tailEpoch := tails[0].Labels[apicommon.LabelEpoch]
		names := map[string]bool{}
		for _, tl := range tails {
			assert.Equal(t, tailEpoch, tl.Labels[apicommon.LabelEpoch], "all non-anchor entries share the tail batch epoch")
			assert.Equal(t, []string{anchorEntry(t, entries).Labels[apicommon.LabelEpoch]}, tl.DependsOn)
			names[tl.Name] = true
		}
		assert.Len(t, names, 2, "salted names are distinct within the shared epoch")
	})
}

func TestBuildBootstrapMPG(t *testing.T) {
	t.Run("carries standalone full counts and PCSG floor indices", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithStandaloneCliqueReplicas("frontend", 4).
			WithScalingGroupConfig("prefill", []string{"pworker"}, 5, 2).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()

		anchor := buildBootstrapMPG(pcs, 0, testHash, "epoch-0")
		assert.True(t, anchor.IsEpochAnchor)
		assert.Nil(t, anchor.DependsOn)
		assert.Equal(t, "epoch-0", anchor.Labels[apicommon.LabelEpoch])
		assert.Equal(t, map[string]int32{"frontend": 4}, anchor.PodCliques)
		assert.Equal(t, map[string][]int32{"prefill": {0, 1}}, anchor.PCSGReplicaIndices)
	})

	t.Run("PCSG-only PCS has empty PodCliques on anchor", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithScalingGroupConfig("prefill", []string{"pworker"}, 3, 1).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()

		anchor := buildBootstrapMPG(pcs, 0, testHash, "epoch-0")
		assert.Empty(t, anchor.PodCliques)
		assert.Equal(t, map[string][]int32{"prefill": {0}}, anchor.PCSGReplicaIndices)
	})
}

func TestBuildBootstrapTPGs(t *testing.T) {
	t.Run("one entry per PCSG above minAvailable", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithScalingGroupConfig("prefill", []string{"pworker"}, 4, 1).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()

		tpgs := buildBootstrapTPGs(pcs, 0, testHash, "tpg-epoch", "mpg-epoch")
		require.Len(t, tpgs, 1)
		assert.False(t, tpgs[0].IsEpochAnchor)
		assert.Equal(t, map[string][]int32{"prefill": {1, 2, 3}}, tpgs[0].PCSGReplicaIndices)
		assert.Equal(t, "tpg-epoch", tpgs[0].Labels[apicommon.LabelEpoch])
		assert.Equal(t, []string{"mpg-epoch"}, tpgs[0].DependsOn)
	})

	t.Run("PCSG at minAvailable is skipped", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithScalingGroupConfig("prefill", []string{"pworker"}, 2, 2).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()

		tpgs := buildBootstrapTPGs(pcs, 0, testHash, "tpg-epoch", "mpg-epoch")
		assert.Empty(t, tpgs)
	})

	t.Run("no PCSGs yields no entries", func(t *testing.T) {
		pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithStandaloneCliqueReplicas("frontend", 3).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()

		tpgs := buildBootstrapTPGs(pcs, 0, testHash, "tpg-epoch", "mpg-epoch")
		assert.Empty(t, tpgs)
	})
}

func TestBuildEntriesFromPCLQAndPCSGStatuses(t *testing.T) {
	clk := clocktesting.NewFakeClock(time.Unix(0, 5000))

	newPCS := func() *grovecorev1alpha1.PodCliqueSet {
		return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithStandaloneCliqueReplicas("frontend", 5).
			WithScalingGroupConfig("prefill", []string{"pworker"}, 3, 1).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()
	}
	// standalone PCLQ FQN is <pcs>-<replica>-<clique>.
	newFrontendPCLQ := func(mapping map[string]int32) grovecorev1alpha1.PodClique {
		return *testutils.NewPodCliqueBuilder(testPCSName, "uid", "frontend", testNamespace, 0).
			WithPodGangMapping(mapping).Build()
	}
	newPrefillPCSG := func(mapping map[string][]int32) grovecorev1alpha1.PodCliqueScalingGroup {
		return *testutils.NewPodCliqueScalingGroupBuilder("my-pcs-0-prefill", testNamespace, testPCSName, 0).
			WithPodGangMapping(mapping).Build()
	}

	t.Run("rebuilds entries from published owner mappings", func(t *testing.T) {
		pcs := newPCS()
		pclqs := []grovecorev1alpha1.PodClique{newFrontendPCLQ(map[string]int32{"pg-a": 2, "pg-b": 3})}
		pcsgs := []grovecorev1alpha1.PodCliqueScalingGroup{newPrefillPCSG(map[string][]int32{"pg-a": {0}, "pg-c": {1, 2}})}
		existingPGM := &grovecorev1alpha1.PodGangMap{}

		entries, err := buildEntriesFromPCLQAndPCSGStatuses(pcs, pclqs, pcsgs, existingPGM, 0, clk)
		require.NoError(t, err)
		byName := entriesByName(entries)
		require.Len(t, byName, 3)
		assert.Equal(t, int32(2), byName["pg-a"].PodCliques["frontend"])
		assert.Equal(t, []int32{0}, byName["pg-a"].PCSGReplicaIndices["prefill"])
		assert.Equal(t, int32(3), byName["pg-b"].PodCliques["frontend"])
		assert.Equal(t, []int32{1, 2}, byName["pg-c"].PCSGReplicaIndices["prefill"])
	})

	t.Run("preserves epoch, DependsOn and IsEpochAnchor of existing entries", func(t *testing.T) {
		pcs := newPCS()
		pclqs := []grovecorev1alpha1.PodClique{newFrontendPCLQ(map[string]int32{"pg-a": 2})}
		pcsgs := []grovecorev1alpha1.PodCliqueScalingGroup{newPrefillPCSG(map[string][]int32{"pg-a": {0}})}
		existingPGM := &grovecorev1alpha1.PodGangMap{
			Spec: grovecorev1alpha1.PodGangMapSpec{
				Entries: []grovecorev1alpha1.PodGangEntry{{
					Name:          "pg-a",
					IsEpochAnchor: true,
					Labels:        map[string]string{apicommon.LabelEpoch: "orig-epoch"},
					DependsOn:     []string{"dep-epoch"},
				}},
			},
		}

		entries, err := buildEntriesFromPCLQAndPCSGStatuses(pcs, pclqs, pcsgs, existingPGM, 0, clk)
		require.NoError(t, err)
		pgA := entriesByName(entries)["pg-a"]
		assert.True(t, pgA.IsEpochAnchor)
		assert.Equal(t, "orig-epoch", pgA.Labels[apicommon.LabelEpoch])
		assert.Equal(t, []string{"dep-epoch"}, pgA.DependsOn)
	})

	t.Run("new scale-out entry gets fresh epoch, nil DependsOn, not anchor", func(t *testing.T) {
		pcs := newPCS()
		pclqs := []grovecorev1alpha1.PodClique{newFrontendPCLQ(map[string]int32{"pg-a": 2})}
		pcsgs := []grovecorev1alpha1.PodCliqueScalingGroup{newPrefillPCSG(map[string][]int32{"pg-new": {5}})}
		existingPGM := &grovecorev1alpha1.PodGangMap{} // pg-new not present

		entries, err := buildEntriesFromPCLQAndPCSGStatuses(pcs, pclqs, pcsgs, existingPGM, 0, clk)
		require.NoError(t, err)
		pgNew := entriesByName(entries)["pg-new"]
		assert.False(t, pgNew.IsEpochAnchor)
		assert.Nil(t, pgNew.DependsOn)
		assert.Equal(t, "5000", pgNew.Labels[apicommon.LabelEpoch])
	})

	t.Run("unpublished PCLQ mapping triggers requeue error", func(t *testing.T) {
		pcs := newPCS()
		pclqs := []grovecorev1alpha1.PodClique{newFrontendPCLQ(nil)} // empty mapping
		pcsgs := []grovecorev1alpha1.PodCliqueScalingGroup{newPrefillPCSG(map[string][]int32{"pg-a": {0}})}

		_, err := buildEntriesFromPCLQAndPCSGStatuses(pcs, pclqs, pcsgs, &grovecorev1alpha1.PodGangMap{}, 0, clk)
		assertErrorCode(t, err, groveerr.ErrCodeContinueReconcileAndRequeue)
	})

	t.Run("fewer observed PCSGs than spec triggers requeue error", func(t *testing.T) {
		pcs := newPCS()
		pclqs := []grovecorev1alpha1.PodClique{newFrontendPCLQ(map[string]int32{"pg-a": 2})}
		var pcsgs []grovecorev1alpha1.PodCliqueScalingGroup // spec declares 1 PCSG, none observed

		_, err := buildEntriesFromPCLQAndPCSGStatuses(pcs, pclqs, pcsgs, &grovecorev1alpha1.PodGangMap{}, 0, clk)
		assertErrorCode(t, err, groveerr.ErrCodeContinueReconcileAndRequeue)
	})

	t.Run("drops entries that end up empty", func(t *testing.T) {
		pcs := newPCS()
		// pg-empty is referenced by the PCSG with an empty index slice.
		pclqs := []grovecorev1alpha1.PodClique{newFrontendPCLQ(map[string]int32{"pg-a": 2})}
		pcsgs := []grovecorev1alpha1.PodCliqueScalingGroup{newPrefillPCSG(map[string][]int32{"pg-a": {0}, "pg-empty": {}})}

		entries, err := buildEntriesFromPCLQAndPCSGStatuses(pcs, pclqs, pcsgs, &grovecorev1alpha1.PodGangMap{}, 0, clk)
		require.NoError(t, err)
		_, hasEmpty := entriesByName(entries)["pg-empty"]
		assert.False(t, hasEmpty)
	})

	t.Run("unparseable PCLQ FQN yields extract-name error", func(t *testing.T) {
		pcs := newPCS()
		badPCLQ := grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{Name: "not-a-valid-fqn", Namespace: testNamespace},
			Status:     grovecorev1alpha1.PodCliqueStatus{PodGangMapping: map[string]int32{"pg-a": 1}},
		}
		pcsgs := []grovecorev1alpha1.PodCliqueScalingGroup{newPrefillPCSG(map[string][]int32{"pg-a": {0}})}

		_, err := buildEntriesFromPCLQAndPCSGStatuses(pcs, []grovecorev1alpha1.PodClique{badPCLQ}, pcsgs, &grovecorev1alpha1.PodGangMap{}, 0, clk)
		assertErrorCode(t, err, errCodeExtractPodCliqueName)
	})
}

func TestCanRebuildPGMFromStatuses(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
		WithStandaloneCliqueReplicas("frontend", 5).
		WithScalingGroupConfig("prefill", []string{"pworker"}, 3, 1).
		WithStatusCurrentGenerationHash(ptr.To(testHash)).
		Build()

	publishedPCLQ := *testutils.NewPodCliqueBuilder(testPCSName, "uid", "frontend", testNamespace, 0).
		WithPodGangMapping(map[string]int32{"pg-a": 2}).Build()
	publishedPCSG := *testutils.NewPodCliqueScalingGroupBuilder("my-pcs-0-prefill", testNamespace, testPCSName, 0).
		WithPodGangMapping(map[string][]int32{"pg-a": {0}}).Build()
	unpublishedPCLQ := *testutils.NewPodCliqueBuilder(testPCSName, "uid", "frontend", testNamespace, 0).Build()
	unpublishedPCSG := *testutils.NewPodCliqueScalingGroupBuilder("my-pcs-0-prefill", testNamespace, testPCSName, 0).Build()

	tests := []struct {
		name  string
		pclqs []grovecorev1alpha1.PodClique
		pcsgs []grovecorev1alpha1.PodCliqueScalingGroup
		want  bool
	}{
		{
			name:  "true when all owners observed and published",
			pclqs: []grovecorev1alpha1.PodClique{publishedPCLQ},
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{publishedPCSG},
			want:  true,
		},
		{
			name:  "false when fewer standalone PCLQs than spec",
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{publishedPCSG},
			want:  false,
		},
		{
			name:  "false when fewer PCSGs than configs",
			pclqs: []grovecorev1alpha1.PodClique{publishedPCLQ},
			want:  false,
		},
		{
			name:  "false when a PCLQ mapping is empty",
			pclqs: []grovecorev1alpha1.PodClique{unpublishedPCLQ},
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{publishedPCSG},
			want:  false,
		},
		{
			name:  "false when a PCSG mapping is empty",
			pclqs: []grovecorev1alpha1.PodClique{publishedPCLQ},
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{unpublishedPCSG},
			want:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, canRebuildPGMFromStatuses(pcs, tc.pclqs, tc.pcsgs))
		})
	}
}

func TestRemoveEmptyEntries(t *testing.T) {
	t.Run("drops all-empty entry, keeps others", func(t *testing.T) {
		entries := []grovecorev1alpha1.PodGangEntry{
			{Name: "empty", PodCliques: map[string]int32{"a": 0}, PCSGReplicaIndices: map[string][]int32{"p": {}}},
			{Name: "has-pods", PodCliques: map[string]int32{"a": 1}},
			{Name: "has-indices", PCSGReplicaIndices: map[string][]int32{"p": {0}}},
		}
		result := removeEmptyEntries(entries)
		byName := entriesByName(result)
		require.Len(t, byName, 2)
		_, hasEmpty := byName["empty"]
		assert.False(t, hasEmpty)
	})

	t.Run("empty input yields empty output", func(t *testing.T) {
		assert.Empty(t, removeEmptyEntries(nil))
	})
}

func TestReconstructEntriesFromExistingPodGangs(t *testing.T) {
	clk := clocktesting.NewFakeClock(time.Unix(0, 2000))
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
		WithStandaloneCliqueReplicas("frontend", 2).
		WithScalingGroupConfig("prefill", []string{"pworker"}, 2, 1).
		WithStatusCurrentGenerationHash(ptr.To(testHash)).
		Build()

	// A base PodGang carries the standalone clique group; a scaled PodGang carries the
	// base-podgang label and a PCSG-owned clique group.
	bpg := testutils.NewPodGangBuilder("my-pcs-0", testNamespace).
		WithPodGroup("my-pcs-0-frontend", 2).Build()
	// give the standalone group two pod references so the reconstructed count is 2.
	bpg.Spec.PodGroups[0].PodReferences = []groveschedulerv1alpha1.NamespacedName{
		{Namespace: testNamespace, Name: "p0"}, {Namespace: testNamespace, Name: "p1"},
	}
	spg := testutils.NewPodGangBuilder("my-pcs-0-prefill-1", testNamespace).
		WithLabel(apicommon.LabelBasePodGang, "my-pcs-0").
		WithPodGroup("my-pcs-0-prefill-1-pworker", 1).Build()

	t.Run("BPG becomes anchor E0, SPG depends on E0 at E1", func(t *testing.T) {
		entries, err := reconstructEntriesFromExistingPodGangs(pcs, []groveschedulerv1alpha1.PodGang{*bpg, *spg}, 0, clk)
		require.NoError(t, err)
		byName := entriesByName(entries)

		base := byName["my-pcs-0"]
		assert.True(t, base.IsEpochAnchor)
		assert.Nil(t, base.DependsOn)
		assert.Equal(t, int32(2), base.PodCliques["frontend"])

		scaled := byName["my-pcs-0-prefill-1"]
		assert.False(t, scaled.IsEpochAnchor)
		require.Len(t, scaled.DependsOn, 1)
		assert.Equal(t, base.Labels[apicommon.LabelEpoch], scaled.DependsOn[0])
		assert.Equal(t, []int32{1}, scaled.PCSGReplicaIndices["prefill"])
		// E1 > E0.
		assert.Greater(t, scaled.Labels[apicommon.LabelEpoch], base.Labels[apicommon.LabelEpoch])
	})

	t.Run("BPG only yields a single anchor", func(t *testing.T) {
		entries, err := reconstructEntriesFromExistingPodGangs(pcs, []groveschedulerv1alpha1.PodGang{*bpg}, 0, clk)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.True(t, entries[0].IsEpochAnchor)
	})

	t.Run("unparseable PodGroup name yields reconstruction error", func(t *testing.T) {
		bad := testutils.NewPodGangBuilder("my-pcs-0", testNamespace).
			WithPodGroup("my-pcs-0-unknownclique", 1).Build()
		_, err := reconstructEntriesFromExistingPodGangs(pcs, []groveschedulerv1alpha1.PodGang{*bad}, 0, clk)
		assertErrorCode(t, err, errCodeReconstructPodGangMapEntry)
	})
}

func TestBuildEntryFromPodGang(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
		WithStandaloneCliqueReplicas("frontend", 2).
		WithScalingGroupConfig("prefill", []string{"pworker"}, 3, 1).
		WithStatusCurrentGenerationHash(ptr.To(testHash)).
		Build()

	t.Run("standalone group contributes pod count, PCSG group contributes replica index", func(t *testing.T) {
		pg := testutils.NewPodGangBuilder("my-pcs-0", testNamespace).
			WithPodGroup("my-pcs-0-frontend", 2).
			WithPodGroup("my-pcs-0-prefill-0-pworker", 1).Build()
		pg.Spec.PodGroups[0].PodReferences = []groveschedulerv1alpha1.NamespacedName{
			{Namespace: testNamespace, Name: "p0"}, {Namespace: testNamespace, Name: "p1"},
		}

		entry, err := buildEntryFromPodGang(pcs, 0, testHash, *pg)
		require.NoError(t, err)
		assert.Equal(t, int32(2), entry.PodCliques["frontend"])
		assert.Equal(t, []int32{0}, entry.PCSGReplicaIndices["prefill"])
	})

	t.Run("same PCSG replica across multiple cliques de-duplicates", func(t *testing.T) {
		pcsMulti := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
			WithScalingGroupConfig("prefill", []string{"pworker", "leader"}, 3, 1).
			WithStatusCurrentGenerationHash(ptr.To(testHash)).
			Build()
		pg := testutils.NewPodGangBuilder("my-pcs-0-prefill-0", testNamespace).
			WithPodGroup("my-pcs-0-prefill-0-pworker", 1).
			WithPodGroup("my-pcs-0-prefill-0-leader", 1).Build()

		entry, err := buildEntryFromPodGang(pcsMulti, 0, testHash, *pg)
		require.NoError(t, err)
		assert.Equal(t, []int32{0}, entry.PCSGReplicaIndices["prefill"])
	})

	t.Run("unknown clique template yields error", func(t *testing.T) {
		pg := testutils.NewPodGangBuilder("my-pcs-0", testNamespace).
			WithPodGroup("my-pcs-0-nope", 1).Build()
		_, err := buildEntryFromPodGang(pcs, 0, testHash, *pg)
		require.Error(t, err)
	})
}

func TestExtractCliqueName(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
		WithStandaloneCliqueReplicas("frontend", 2).
		WithStandaloneCliqueReplicas("backend", 2).
		Build()
	tests := []struct {
		name    string
		fqn     string
		want    string
		wantErr bool
	}{
		{name: "matches trailing template segment", fqn: "my-pcs-0-frontend", want: "frontend"},
		{name: "no matching template yields error", fqn: "my-pcs-0-unknown", wantErr: true},
		// len(pclqFQN) must exceed len(suffix): "frontend" is not longer than "-frontend".
		{name: "FQN equal to bare suffix does not match", fqn: "frontend", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, err := extractCliqueName(pcs, tc.fqn)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, name)
		})
	}
}

func TestExtractPCSGReplicaIndexFromPCLQFQN(t *testing.T) {
	tests := []struct {
		name    string
		fqn     string
		want    int32
		wantErr bool
	}{
		{name: "well-formed FQN parses index", fqn: "my-pcs-0-prefill-2-pworker", want: 2},
		{name: "multi-digit index", fqn: "my-pcs-0-prefill-13-pworker", want: 13},
		{name: "wrong suffix yields error", fqn: "my-pcs-0-prefill-2-other", wantErr: true},
		{name: "non-integer middle yields error", fqn: "my-pcs-0-prefill-x-pworker", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idx, err := extractPCSGReplicaIndexFromPCLQFQN(tc.fqn, testPCSName, 0, "prefill", "pworker")
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, idx)
		})
	}
}

// assertErrorCode asserts that err unwraps to a GroveError carrying the expected code.
func assertErrorCode(t *testing.T, err error, code grovecorev1alpha1.ErrorCode) {
	t.Helper()
	require.Error(t, err)
	var groveErr *groveerr.GroveError
	require.True(t, errors.As(err, &groveErr), "error is not a GroveError: %v", err)
	assert.Equal(t, code, groveErr.Code)
}

// entriesByName indexes entries by their PodGang name for order-independent assertions.
func entriesByName(entries []grovecorev1alpha1.PodGangEntry) map[string]grovecorev1alpha1.PodGangEntry {
	byName := make(map[string]grovecorev1alpha1.PodGangEntry, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}
	return byName
}

// anchorEntry returns the single entry with IsEpochAnchor true, failing if not exactly one.
func anchorEntry(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) grovecorev1alpha1.PodGangEntry {
	t.Helper()
	var anchors []grovecorev1alpha1.PodGangEntry
	for _, e := range entries {
		if e.IsEpochAnchor {
			anchors = append(anchors, e)
		}
	}
	require.Len(t, anchors, 1, "expected exactly one anchor entry")
	return anchors[0]
}

// nonAnchorEntries returns the entries with IsEpochAnchor false.
func nonAnchorEntries(entries []grovecorev1alpha1.PodGangEntry) []grovecorev1alpha1.PodGangEntry {
	var out []grovecorev1alpha1.PodGangEntry
	for _, e := range entries {
		if !e.IsEpochAnchor {
			out = append(out, e)
		}
	}
	return out
}

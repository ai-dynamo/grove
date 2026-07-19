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

package podgangmap

import (
	"context"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const testPCSUID = types.UID("pcs-uid")

func TestMVUTemplateClone(t *testing.T) {
	orig := &mvuTemplate{
		standalonePCLQs: map[string]int32{"frontend": 2},
		pcsgs:           map[string]int32{"prefill": 1},
	}
	clone := orig.clone()
	clone.standalonePCLQs["frontend"] = 99
	clone.pcsgs["prefill"] = 99
	assert.Equal(t, int32(2), orig.standalonePCLQs["frontend"], "clone must not alias original")
	assert.Equal(t, int32(1), orig.pcsgs["prefill"])
}

func TestMVUTemplateComponentNames(t *testing.T) {
	mvu := &mvuTemplate{
		standalonePCLQs: map[string]int32{"frontend": 2, "backend": 1},
		pcsgs:           map[string]int32{"prefill": 1},
	}
	assert.Equal(t, []string{"backend", "frontend", "prefill"}, mvu.componentNames())
}

func TestMVUTemplateMinAvailableByComponent(t *testing.T) {
	mvu := &mvuTemplate{
		standalonePCLQs: map[string]int32{"frontend": 2},
		pcsgs:           map[string]int32{"prefill": 1},
	}
	assert.Equal(t, map[string]int32{"frontend": 2, "prefill": 1}, mvu.minAvailableByComponent())
}

func TestComputeMVUTemplate(t *testing.T) {
	tests := []struct {
		name           string
		updateProgress *grovecorev1alpha1.PodCliqueSetUpdateProgress
		wantErrCode    grovecorev1alpha1.ErrorCode
		wantStandalone map[string]int32
		wantPCSGs      map[string]int32
	}{
		{
			name:           "builds template from in-scope components",
			updateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{InScopeStandalonePodCliques: []string{"frontend"}, InScopePodCliqueScalingGroups: []string{"prefill"}},
			wantStandalone: map[string]int32{"frontend": 2},
			wantPCSGs:      map[string]int32{"prefill": 1},
		},
		{
			name:        "error when UpdateProgress is nil",
			wantErrCode: errCodeComputeMVUTemplate,
		},
		{
			name:           "error when scoped standalone PCLQ no longer in spec",
			updateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{InScopeStandalonePodCliques: []string{"ghost"}},
			wantErrCode:    errCodeComputeMVUTemplate,
		},
		{
			name:           "error when scoped PCSG no longer in spec",
			updateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{InScopePodCliqueScalingGroups: []string{"ghost"}},
			wantErrCode:    errCodeComputeMVUTemplate,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcsBuilder := pcsWithCoherentUpdateStrategy(testHash)
			if tc.updateProgress != nil {
				pcsBuilder = pcsBuilder.WithUpdateProgress(tc.updateProgress)
			}
			mvu, err := computeMVUTemplate(pcsBuilder.Build())
			if tc.wantErrCode != "" {
				assertErrorCode(t, err, tc.wantErrCode)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantStandalone, mvu.standalonePCLQs)
			assert.Equal(t, tc.wantPCSGs, mvu.pcsgs)
		})
	}
}

func TestExtractPCSReplicaIndex(t *testing.T) {
	tests := []struct {
		name    string
		podGang *groveschedulerv1alpha1.PodGang
		wantIdx int
		wantOK  bool
	}{
		{
			name:    "reads replica-index label",
			podGang: testutils.NewPodGangBuilder("my-pcs-0", testNamespace).WithLabel(apicommon.LabelPodCliqueSetReplicaIndex, "3").Build(),
			wantIdx: 3,
			wantOK:  true,
		},
		{
			name:    "falls back to parsing the name",
			podGang: testutils.NewPodGangBuilder("my-pcs-2-prefill-0", testNamespace).Build(),
			wantIdx: 2,
			wantOK:  true,
		},
		{
			name:    "unparseable name yields false",
			podGang: testutils.NewPodGangBuilder("unrelated-name", testNamespace).Build(),
			wantOK:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idx, ok := extractPCSReplicaIndex(*tc.podGang, testPCSName)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantIdx, idx)
			}
		})
	}
}

func TestAnyReplicaLacksPGMEntries(t *testing.T) {
	emptyPGM := testutils.NewPodGangMapBuilder(testPCSName, testNamespace, testPCSUID, 1).Build()
	tests := []struct {
		name string
		pgms map[int]grovecorev1alpha1.PodGangMap
		want bool
	}{
		{
			name: "true when a replica has no PGM",
			pgms: map[int]grovecorev1alpha1.PodGangMap{0: *pgmWithEpochEntry(0, "e0")},
			want: true,
		},
		{
			name: "true when a PGM has no entries",
			pgms: map[int]grovecorev1alpha1.PodGangMap{0: *pgmWithEpochEntry(0, "e0"), 1: *emptyPGM},
			want: true,
		},
		{
			name: "false when all replicas have entries",
			pgms: map[int]grovecorev1alpha1.PodGangMap{0: *pgmWithEpochEntry(0, "e0"), 1: *pgmWithEpochEntry(1, "e0")},
			want: false,
		},
	}
	pcs := pcsWithCoherentUpdateStrategy(testHash).WithReplicas(2).Build()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snap := &syncSnapshot{pcs: pcs, existingPGMByReplica: tc.pgms}
			assert.Equal(t, tc.want, anyReplicaLacksPGMEntries(snap))
		})
	}
}

func TestIsPCSReplicaUnderUpdate(t *testing.T) {
	ended := metav1.NewTime(time.Unix(0, 0))
	tests := []struct {
		name           string
		updateProgress *grovecorev1alpha1.PodCliqueSetUpdateProgress
		want           bool
	}{
		{
			name: "false when UpdateProgress nil",
			want: false,
		},
		{
			name:           "true when replica currently updating with nil UpdateEndedAt",
			updateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}}},
			want:           true,
		},
		{
			name:           "false when replica's UpdateEndedAt is set",
			updateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0, UpdateEndedAt: &ended}}},
			want:           false,
		},
		{
			name:           "false when replica not in CurrentlyUpdating",
			updateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 1}}},
			want:           false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcsBuilder := pcsWithCoherentUpdateStrategy(testHash)
			if tc.updateProgress != nil {
				pcsBuilder = pcsBuilder.WithUpdateProgress(tc.updateProgress)
			}
			assert.Equal(t, tc.want, isPCSReplicaUnderUpdate(pcsBuilder.Build(), 0))
		})
	}
}

func TestTakeSnapshot(t *testing.T) {
	t.Run("groups children by replica and skips mvuTemplate when no update in flight", func(t *testing.T) {
		pcs := pcsWithCoherentUpdateStrategy(testHash).Build()
		cl := newFakeClient(pcs,
			standalonePCLQWithMapping("frontend", 0, map[string]int32{"pg-0": 2}),
			pgmWithEpochEntry(0, "e0"),
		)
		r := newResource(cl)

		snap, err := r.takeSnapshot(context.Background(), logr.Discard(), pcs)
		require.NoError(t, err)
		assert.Len(t, snap.existingStandalonePCLQsByReplica[0], 1)
		assert.Contains(t, snap.existingPGMByReplica, 0)
		assert.Nil(t, snap.mvuTemplate, "mvuTemplate is nil when no update is in flight")
	})

	t.Run("computes mvuTemplate during a coherent update", func(t *testing.T) {
		pcs := pcsWithCoherentUpdateStrategy(testHash).WithUpdateProgress(&grovecorev1alpha1.PodCliqueSetUpdateProgress{
			InScopeStandalonePodCliques: []string{"frontend"},
		}).Build()
		r := newResource(newFakeClient(pcs))

		snap, err := r.takeSnapshot(context.Background(), logr.Discard(), pcs)
		require.NoError(t, err)
		require.NotNil(t, snap.mvuTemplate)
		assert.Equal(t, map[string]int32{"frontend": 2}, snap.mvuTemplate.standalonePCLQs)
	})
}

func TestReconcileEntriesWhenPGMEmpty(t *testing.T) {
	tests := []struct {
		name     string
		podGangs []groveschedulerv1alpha1.PodGang
		assert   func(t *testing.T, pgm *grovecorev1alpha1.PodGangMap)
	}{
		{
			name: "bootstraps a fresh replica and persists Spec only",
			assert: func(t *testing.T, pgm *grovecorev1alpha1.PodGangMap) {
				assert.NotEmpty(t, pgm.Spec.Entries)
			},
		},
		{
			name:     "reconstructs from legacy PodGangs when present",
			podGangs: []groveschedulerv1alpha1.PodGang{*testutils.NewPodGangBuilder("my-pcs-0", testNamespace).WithPodGroup("my-pcs-0-frontend", 2).Build()},
			assert: func(t *testing.T, pgm *grovecorev1alpha1.PodGangMap) {
				require.Len(t, pgm.Spec.Entries, 1)
				assert.True(t, pgm.Spec.Entries[0].IsEpochAnchor)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := pcsWithCoherentUpdateStrategy(testHash).Build()
			r := newResource(newFakeClient(pcs))
			snap := &syncSnapshot{logger: logr.Discard(), pcs: pcs}

			require.NoError(t, r.reconcileEntriesWhenPGMEmpty(context.Background(), snap, tc.podGangs, 0))
			tc.assert(t, getPGM(t, r.client, 0))
		})
	}
}

func TestReconcileSteadyStateEntries(t *testing.T) {
	pcs := pcsWithCoherentUpdateStrategy(testHash).Build()
	pclq := standalonePCLQWithMapping("frontend", 0, map[string]int32{"pg-0": 2})
	pcsg := prefillPCSGWithMapping(0, map[string][]int32{"pg-0": {0}})
	existingPGM := testutils.NewPodGangMapBuilder(testPCSName, testNamespace, testPCSUID, 0).
		WithEntries(testutils.NewPodGangEntryBuilder("pg-0", testHash, "e0").WithEpochAnchor(true).Build()).Build()
	cl := newFakeClient(pcs, pclq, pcsg, existingPGM)
	r := newResource(cl)
	snap := snapshotFor(pcs,
		map[int][]grovecorev1alpha1.PodClique{0: {*pclq}},
		map[int][]grovecorev1alpha1.PodCliqueScalingGroup{0: {*pcsg}},
		map[int]grovecorev1alpha1.PodGangMap{0: *existingPGM})

	require.NoError(t, r.reconcileSteadyStateEntries(context.Background(), snap, 0))

	pgm := getPGM(t, r.client, 0)
	assert.NotEmpty(t, pgm.Spec.Entries)
}

func TestReconcileCoherentUpdateEntries(t *testing.T) {
	pcs := pcsWithCoherentUpdateInProgress(testHash)
	pclq := standalonePCLQWithMapping("frontend", 0, map[string]int32{"pg-0": 2})
	pcsg := prefillPCSGWithMapping(0, map[string][]int32{"pg-0": {0}})
	pgm := testutils.NewPodGangMapBuilder(testPCSName, testNamespace, testPCSUID, 0).
		WithEntries(testutils.NewPodGangEntryBuilder("pg-0", "old-hash", "e0").WithEpochAnchor(true).
			WithPodCliques(map[string]int32{"frontend": 2}).WithPCSGReplicaIndices(map[string][]int32{"prefill": {0}}).Build()).Build()
	cl := newFakeClient(pcs, pclq, pcsg, pgm)
	r := newResource(cl)
	snap := snapshotForCoherent(t, pcs, pclq, pcsg, pgm)

	require.NoError(t, r.reconcileCoherentUpdateEntries(context.Background(), snap, 0))
	assert.NotEmpty(t, getPGM(t, r.client, 0).Spec.Entries, "Spec is persisted")
}

func TestCreateOrPatchPodGangMapSpec(t *testing.T) {
	pcs := pcsWithCoherentUpdateStrategy(testHash).Build()
	r := newResource(newFakeClient(pcs))
	entries := []grovecorev1alpha1.PodGangEntry{
		testutils.NewPodGangEntryBuilder("pg-b", testHash, "e1").Build(),
		testutils.NewPodGangEntryBuilder("pg-a", testHash, "e0").Build(),
	}
	pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: testPCSName, Replica: 0})

	require.NoError(t, r.createOrPatchPodGangMapSpec(context.Background(), pcs, pgmName, 0, entries))

	pgm := getPGM(t, r.client, 0)
	require.Len(t, pgm.Spec.Entries, 2)
	assert.Equal(t, "pg-a", pgm.Spec.Entries[0].Name, "entries sorted by name")
	assert.Equal(t, "pg-b", pgm.Spec.Entries[1].Name)
	assert.Equal(t, int32(0), pgm.Spec.PodCliqueSetReplicaIndex)
	assert.True(t, metav1.IsControlledBy(pgm, pcs))
}

func TestDeleteOrphanedPodGangMaps(t *testing.T) {
	pcs := pcsWithCoherentUpdateStrategy(testHash).WithReplicas(1).Build()
	live := pgmWithEpochEntry(0, "e0")
	orphan := pgmWithEpochEntry(1, "e0") // replica index 1 >= replicas 1
	cl := newFakeClient(pcs, live, orphan)
	r := newResource(cl)
	snap := &syncSnapshot{
		logger:               logr.Discard(),
		pcs:                  pcs,
		existingPGMByReplica: map[int]grovecorev1alpha1.PodGangMap{0: *live, 1: *orphan},
	}

	require.NoError(t, r.deleteOrphanedPodGangMaps(context.Background(), snap))

	assert.True(t, pgmExists(cl, 0), "live PGM kept")
	assert.False(t, pgmExists(cl, 1), "orphan PGM deleted")
}

func TestNewPodGangEntry(t *testing.T) {
	entry := newPodGangEntry(testPCSName, 0, "sfx", testHash, "epoch-1", []string{"dep"})
	assert.Equal(t, "my-pcs-0-sfx", entry.Name)
	assert.Equal(t, testHash, entry.PodCliqueSetGenerationHash)
	assert.Equal(t, "epoch-1", entry.Labels[apicommon.LabelEpoch])
	assert.Equal(t, []string{"dep"}, entry.DependsOn)
}

func TestNewPodGangEntryWithName(t *testing.T) {
	entry := newPodGangEntryWithName("explicit-name", testHash, "epoch-1", nil)
	assert.Equal(t, "explicit-name", entry.Name)
	assert.Equal(t, "epoch-1", entry.Labels[apicommon.LabelEpoch])
	assert.Nil(t, entry.DependsOn)
}

func TestRunSyncFlow(t *testing.T) {
	tests := []struct {
		name    string
		pcs     *grovecorev1alpha1.PodCliqueSet
		objects []client.Object
		assert  func(t *testing.T, cl client.Client)
	}{
		{
			name: "empty PGM with no PodGangs bootstraps a fresh replica",
			pcs:  pcsWithCoherentUpdateStrategy(testHash).Build(),
			assert: func(t *testing.T, cl client.Client) {
				assert.NotEmpty(t, getPGM(t, cl, 0).Spec.Entries, "bootstrap entries created")
			},
		},
		{
			name: "empty PGM with legacy PodGangs reconstructs entries",
			pcs:  pcsWithCoherentUpdateStrategy(testHash).Build(),
			objects: []client.Object{
				testutils.NewPodGangBuilder("my-pcs-0", testNamespace).
					WithLabel(apicommon.LabelPodCliqueSetReplicaIndex, "0").
					WithPodGroup("my-pcs-0-frontend", 2).Build(),
			},
			assert: func(t *testing.T, cl client.Client) {
				pgm := getPGM(t, cl, 0)
				require.Len(t, pgm.Spec.Entries, 1)
				assert.True(t, pgm.Spec.Entries[0].IsEpochAnchor)
			},
		},
		{
			name: "steady-state replica rebuilds entries",
			pcs:  pcsWithCoherentUpdateStrategy(testHash).Build(),
			objects: []client.Object{
				standalonePCLQWithMapping("frontend", 0, map[string]int32{"pg-0": 2}),
				prefillPCSGWithMapping(0, map[string][]int32{"pg-0": {0}}),
				testutils.NewPodGangMapBuilder(testPCSName, testNamespace, testPCSUID, 0).
					WithEntries(testutils.NewPodGangEntryBuilder("pg-0", testHash, "e0").WithEpochAnchor(true).Build()).Build(),
			},
			assert: func(t *testing.T, cl client.Client) {
				pgm := getPGM(t, cl, 0)
				assert.NotEmpty(t, pgm.Spec.Entries)
			},
		},
		{
			name: "orphaned PGM beyond replica count is deleted",
			pcs:  pcsWithCoherentUpdateStrategy(testHash).WithReplicas(1).Build(),
			objects: []client.Object{
				standalonePCLQWithMapping("frontend", 0, map[string]int32{"pg-0": 2}),
				prefillPCSGWithMapping(0, map[string][]int32{"pg-0": {0}}),
				pgmWithEpochEntry(0, "e0"),
				pgmWithEpochEntry(1, "e0"),
			},
			assert: func(t *testing.T, cl client.Client) {
				assert.True(t, pgmExists(cl, 0), "live replica PGM kept")
				assert.False(t, pgmExists(cl, 1), "orphan PGM deleted")
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			objs := append([]client.Object{tc.pcs}, tc.objects...)
			cl := testutils.NewTestClientBuilder().WithObjects(objs...).Build()
			r := newResource(cl)

			require.NoError(t, r.Sync(context.Background(), logr.Discard(), tc.pcs))
			tc.assert(t, cl)
		})
	}
}

// --- helpers (after all Test* functions per the package test convention) ---

// pcsWithCoherentUpdateStrategy builds a Coherent-strategy PCS with a standalone frontend clique
// (minAvailable 2) and a prefill PCSG (minAvailable 1), at the given generation hash. It has no
// update in flight (Status.UpdateProgress is unset).
func pcsWithCoherentUpdateStrategy(hash string) *testutils.PodCliqueSetBuilder {
	return testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, testPCSUID).
		WithReplicas(1).
		WithStandaloneCliqueReplicas("frontend", 2).
		WithStandaloneCliqueMinAvailable("frontend", 2).
		WithScalingGroupConfig("prefill", []string{"pworker"}, 1, 1).
		WithStandaloneCliqueMaxUnavailable("frontend", 1).
		WithScalingGroupMaxUnavailable("prefill", 1).
		WithStatusCurrentGenerationHash(ptr.To(hash)).
		WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy})
}

// pcsWithCoherentUpdateInProgress builds a Coherent-strategy PCS with replica 0 currently updating
// and a frozen in-scope set covering the frontend clique and prefill PCSG.
func pcsWithCoherentUpdateInProgress(hash string) *grovecorev1alpha1.PodCliqueSet {
	return pcsWithCoherentUpdateStrategy(hash).WithUpdateProgress(&grovecorev1alpha1.PodCliqueSetUpdateProgress{
		CurrentlyUpdating:             []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}},
		InScopeStandalonePodCliques:   []string{"frontend"},
		InScopePodCliqueScalingGroups: []string{"prefill"},
	}).Build()
}

// standalonePCLQWithMapping builds a standalone PodClique for the given replica with a PodGangMapping.
// Its Spec.Replicas is the total pod count across the mapping, so live replicas stay consistent with
// the mapping the test declares. Status.ReadyReplicas is seeded to the full replica count so the
// coherent-update MaxUnavailable budget gate treats it as fully available.
func standalonePCLQWithMapping(clique string, pcsReplicaIndex int32, mapping map[string]int32) *grovecorev1alpha1.PodClique {
	var replicas int32
	for _, count := range mapping {
		replicas += count
	}
	return testutils.NewPodCliqueBuilder(testPCSName, testPCSUID, clique, testNamespace, pcsReplicaIndex).
		WithReplicas(replicas).WithPodGangMapping(mapping).WithStatusReadyReplicas(replicas).Build()
}

// prefillPCSGWithMapping builds the prefill PodCliqueScalingGroup for the given replica with a
// PodGangMapping. Status.AvailableReplicas is seeded from the mapping's replica-index count so the
// coherent-update MaxUnavailable budget gate treats it as fully available.
func prefillPCSGWithMapping(pcsReplicaIndex int, mapping map[string][]int32) *grovecorev1alpha1.PodCliqueScalingGroup {
	var available int32
	for _, indices := range mapping {
		available += int32(len(indices))
	}
	return testutils.NewPodCliqueScalingGroupBuilder("my-pcs-0-prefill", testNamespace, testPCSName, pcsReplicaIndex).
		WithPodGangMapping(mapping).WithStatusAvailableReplicas(available).Build()
}

// pgmWithEpochEntry builds a PodGangMap for the given replica carrying a single entry at the given epoch.
func pgmWithEpochEntry(replicaIndex int, epoch string) *grovecorev1alpha1.PodGangMap {
	return testutils.NewPodGangMapBuilder(testPCSName, testNamespace, testPCSUID, replicaIndex).
		WithEntries(testutils.NewPodGangEntryBuilder("pg-0", testHash, epoch).WithEpochAnchor(true).
			WithPodCliques(map[string]int32{"frontend": 1}).Build()).Build()
}

// newFakeClient builds a fake client seeded with the given objects.
func newFakeClient(objs ...client.Object) client.Client {
	return testutils.NewTestClientBuilder().WithObjects(objs...).Build()
}

// newResource wraps a client in a _resource with the test scheme and a fixed fake clock.
func newResource(cl client.Client) _resource {
	return _resource{client: cl, scheme: groveclientscheme.Scheme, clk: clocktesting.NewFakeClock(time.Unix(0, 1000))}
}

// getPGM fetches the PodGangMap of the given replica from the client.
func getPGM(t *testing.T, cl client.Client, replicaIndex int) *grovecorev1alpha1.PodGangMap {
	t.Helper()
	pgm := &grovecorev1alpha1.PodGangMap{}
	name := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: testPCSName, Replica: replicaIndex})
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: name}, pgm))
	return pgm
}

// pgmExists reports whether the PodGangMap of the given replica exists in the client.
func pgmExists(cl client.Client, replicaIndex int) bool {
	pgm := &grovecorev1alpha1.PodGangMap{}
	name := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: testPCSName, Replica: replicaIndex})
	return cl.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: name}, pgm) == nil
}

// snapshotFor assembles a syncSnapshot from pre-grouped children.
func snapshotFor(pcs *grovecorev1alpha1.PodCliqueSet,
	pclqs map[int][]grovecorev1alpha1.PodClique,
	pcsgs map[int][]grovecorev1alpha1.PodCliqueScalingGroup,
	pgms map[int]grovecorev1alpha1.PodGangMap) *syncSnapshot {
	return &syncSnapshot{
		logger:                           logr.Discard(),
		pcs:                              pcs,
		existingStandalonePCLQsByReplica: pclqs,
		existingPCSGsByReplica:           pcsgs,
		existingPGMByReplica:             pgms,
	}
}

// snapshotForCoherent assembles a coherent-update syncSnapshot with the computed mvuTemplate.
func snapshotForCoherent(t *testing.T, pcs *grovecorev1alpha1.PodCliqueSet, pclq *grovecorev1alpha1.PodClique, pcsg *grovecorev1alpha1.PodCliqueScalingGroup, pgm *grovecorev1alpha1.PodGangMap) *syncSnapshot {
	t.Helper()
	mvu, err := computeMVUTemplate(pcs)
	require.NoError(t, err)
	snap := snapshotFor(pcs,
		map[int][]grovecorev1alpha1.PodClique{0: {*pclq}},
		map[int][]grovecorev1alpha1.PodCliqueScalingGroup{0: {*pcsg}},
		map[int]grovecorev1alpha1.PodGangMap{0: *pgm})
	snap.mvuTemplate = mvu
	return snap
}

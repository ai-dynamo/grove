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

package podgangmap

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	coherentTestPCSName    = "pcs"
	coherentTestNamespace  = "default"
	coherentTestCurrentGen = "v2"
	coherentTestOldGen     = "v1"
)

// TestInScopeStandalonePCLQsByComponent checks that the standalone PodCliques of a replica are indexed by
// component name and that components outside the update scope are dropped.
func TestInScopeStandalonePCLQsByComponent(t *testing.T) {
	pcsNameReplica := apicommon.ResourceNameReplica{Name: coherentTestPCSName, Replica: 0}
	snap := &syncSnapshot{
		pcs:         &grovecorev1alpha1.PodCliqueSet{ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSName}},
		mvuTemplate: &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}},
		existingStandalonePCLQsByReplica: map[int][]grovecorev1alpha1.PodClique{
			0: {
				{ObjectMeta: metav1.ObjectMeta{Name: apicommon.GeneratePodCliqueName(pcsNameReplica, "frontend")}, Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 5}},
				{ObjectMeta: metav1.ObjectMeta{Name: apicommon.GeneratePodCliqueName(pcsNameReplica, "router")}, Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4}},
			},
		},
	}

	pclqByComponent := snap.inScopeStandalonePCLQsByComponent(0)

	require.Len(t, pclqByComponent, 1)
	assert.Contains(t, pclqByComponent, "frontend")
	assert.NotContains(t, pclqByComponent, "router")
	assert.Equal(t, int32(5), pclqByComponent["frontend"].Spec.Replicas)
}

// TestInScopePCSGsByComponent checks that the PodCliqueScalingGroups of a replica are indexed by component
// name, that out-of-scope components are dropped, and that a malformed PCSG name is reported as an error.
func TestInScopePCSGsByComponent(t *testing.T) {
	pcsNameReplica := apicommon.ResourceNameReplica{Name: coherentTestPCSName, Replica: 0}

	t.Run("indexes in-scope PodCliqueScalingGroups only", func(t *testing.T) {
		snap := &syncSnapshot{
			pcs:         &grovecorev1alpha1.PodCliqueSet{ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSName}},
			mvuTemplate: &mvuTemplate{pcsgs: map[string]int32{"decode": 3}},
			existingPCSGsByReplica: map[int][]grovecorev1alpha1.PodCliqueScalingGroup{
				0: {
					{ObjectMeta: metav1.ObjectMeta{Name: apicommon.GeneratePodCliqueScalingGroupName(pcsNameReplica, "decode")}, Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{Replicas: 6}},
					{ObjectMeta: metav1.ObjectMeta{Name: apicommon.GeneratePodCliqueScalingGroupName(pcsNameReplica, "prefill")}, Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{Replicas: 2}},
				},
			},
		}

		pcsgByComponent, err := snap.inScopePCSGsByComponent(0)

		require.NoError(t, err)
		require.Len(t, pcsgByComponent, 1)
		assert.Contains(t, pcsgByComponent, "decode")
		assert.Equal(t, int32(6), pcsgByComponent["decode"].Spec.Replicas)
	})

	t.Run("errors on a malformed PodCliqueScalingGroup name", func(t *testing.T) {
		snap := &syncSnapshot{
			pcs:         &grovecorev1alpha1.PodCliqueSet{ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSName}},
			mvuTemplate: &mvuTemplate{pcsgs: map[string]int32{"decode": 3}},
			existingPCSGsByReplica: map[int][]grovecorev1alpha1.PodCliqueScalingGroup{
				0: {{ObjectMeta: metav1.ObjectMeta{Name: "malformed"}, Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{Replicas: 6}}},
			},
		}

		_, err := snap.inScopePCSGsByComponent(0)

		require.Error(t, err)
	})
}

// TestComputeDesiredReplicas checks that the desired replica count of each in-scope component comes from
// the live child object when it exists and from the PCS template when the object is absent.
func TestComputeDesiredReplicas(t *testing.T) {
	const (
		frontendTemplateReplicas int32 = 4
		decodeTemplateReplicas   int32 = 6
	)
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSName, Namespace: coherentTestNamespace},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: frontendTemplateReplicas}},
					{Name: "decode-worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
				},
				PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
					{Name: "decode", CliqueNames: []string{"decode-worker"}, Replicas: ptr.To(decodeTemplateReplicas)},
				},
			},
		},
	}
	snap := &syncSnapshot{
		pcs:         pcs,
		mvuTemplate: &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 2}, pcsgs: map[string]int32{"decode": 3}},
	}
	frontendObj := grovecorev1alpha1.PodClique{Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 5}}
	decodeObj := grovecorev1alpha1.PodCliqueScalingGroup{Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{Replicas: 7}}

	tests := []struct {
		name       string
		standalone map[string]grovecorev1alpha1.PodClique
		pcsg       map[string]grovecorev1alpha1.PodCliqueScalingGroup
		want       map[string]int32
	}{
		{
			name:       "both components present use the live spec replicas",
			standalone: map[string]grovecorev1alpha1.PodClique{"frontend": frontendObj},
			pcsg:       map[string]grovecorev1alpha1.PodCliqueScalingGroup{"decode": decodeObj},
			want:       map[string]int32{"frontend": 5, "decode": 7},
		},
		{
			name:       "absent standalone falls back to the template replicas",
			standalone: map[string]grovecorev1alpha1.PodClique{},
			pcsg:       map[string]grovecorev1alpha1.PodCliqueScalingGroup{"decode": decodeObj},
			want:       map[string]int32{"frontend": frontendTemplateReplicas, "decode": 7},
		},
		{
			name:       "absent scaling group falls back to the template replicas",
			standalone: map[string]grovecorev1alpha1.PodClique{"frontend": frontendObj},
			pcsg:       map[string]grovecorev1alpha1.PodCliqueScalingGroup{},
			want:       map[string]int32{"frontend": 5, "decode": decodeTemplateReplicas},
		},
		{
			name:       "both absent fall back to the template replicas",
			standalone: map[string]grovecorev1alpha1.PodClique{},
			pcsg:       map[string]grovecorev1alpha1.PodCliqueScalingGroup{},
			want:       map[string]int32{"frontend": frontendTemplateReplicas, "decode": decodeTemplateReplicas},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := snap.computeDesiredReplicas(tc.standalone, tc.pcsg)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestSubsumedPodsScheduled covers the standalone-Pod scheduling gate. Each case names the standalone
// components with their committed current-hash count and their UpdateProgress.UpdatedScheduledReplicas, and
// states whether the gate lets the next sub-step proceed.
func TestSubsumedPodsScheduled(t *testing.T) {
	testCases := []struct {
		description                 string
		standalonePCLQByComponent   map[string]grovecorev1alpha1.PodClique
		currentHashCountByComponent map[string]int32
		want                        bool
	}{
		{
			description:                 "every subsumed Pod is scheduled so the gate proceeds",
			standalonePCLQByComponent:   map[string]grovecorev1alpha1.PodClique{"frontend": pclqWithUpdatedScheduledReplicas(5)},
			currentHashCountByComponent: map[string]int32{"frontend": 5},
			want:                        true,
		},
		{
			description:                 "a component is still catching up so the gate holds",
			standalonePCLQByComponent:   map[string]grovecorev1alpha1.PodClique{"frontend": pclqWithUpdatedScheduledReplicas(3)},
			currentHashCountByComponent: map[string]int32{"frontend": 5},
			want:                        false,
		},
		{
			description:                 "nothing committed yet so there is nothing to wait on",
			standalonePCLQByComponent:   map[string]grovecorev1alpha1.PodClique{"frontend": pclqWithUpdatedScheduledReplicas(0)},
			currentHashCountByComponent: map[string]int32{"frontend": 0},
			want:                        true,
		},
		{
			description:                 "an entry is committed but UpdateProgress is not yet initialized so the gate holds",
			standalonePCLQByComponent:   map[string]grovecorev1alpha1.PodClique{"frontend": {}},
			currentHashCountByComponent: map[string]int32{"frontend": 2},
			want:                        false,
		},
		{
			description:                 "a previously scheduled Pod regressed so the gate re-holds",
			standalonePCLQByComponent:   map[string]grovecorev1alpha1.PodClique{"frontend": pclqWithUpdatedScheduledReplicas(7)},
			currentHashCountByComponent: map[string]int32{"frontend": 8},
			want:                        false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			assert.Equal(t, tc.want, subsumedPodsScheduled(tc.standalonePCLQByComponent, planPosition{currentHashCountByComponent: tc.currentHashCountByComponent}))
		})
	}
}

// TestMaxUnavailableBudgetSatisfied covers the drain-aware disruption budget gate. Each case names the
// in-scope components with their desired replicas, MaxUnavailable, the available count a standalone
// PodClique reports through ReadyReplicas or a PodCliqueScalingGroup through AvailableReplicas, and what the
// next sub-step would drain, and states whether the drain keeps unavailability within MaxUnavailable.
func TestMaxUnavailableBudgetSatisfied(t *testing.T) {
	testCases := []struct {
		description               string
		standalonePCLQByComponent map[string]grovecorev1alpha1.PodClique
		pcsgByComponent           map[string]grovecorev1alpha1.PodCliqueScalingGroup
		desiredReplicas           map[string]int32
		maxUnavailableByComponent map[string]int32
		drainByComponent          map[string]int32
		numMissingOldVersionPods  map[string]int32
		want                      bool
	}{
		{
			description:               "a fully available standalone PodClique absorbs a full-budget drain",
			standalonePCLQByComponent: map[string]grovecorev1alpha1.PodClique{"frontend": pclqWithReadyReplicas(10)},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			drainByComponent:          map[string]int32{"frontend": 3},
			want:                      true,
		},
		{
			description:               "a standalone PodClique already at the budget holds any further drain",
			standalonePCLQByComponent: map[string]grovecorev1alpha1.PodClique{"frontend": pclqWithReadyReplicas(7)},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			drainByComponent:          map[string]int32{"frontend": 1},
			want:                      false,
		},
		{
			description:               "unrelated unavailability plus the sub-step drain crossing the budget holds",
			pcsgByComponent:           map[string]grovecorev1alpha1.PodCliqueScalingGroup{"decode": pcsgWithAvailableReplicas(4)},
			desiredReplicas:           map[string]int32{"decode": 6},
			maxUnavailableByComponent: map[string]int32{"decode": 2},
			drainByComponent:          map[string]int32{"decode": 2},
			want:                      false,
		},
		{
			description:               "a PodCliqueScalingGroup drain that exactly reaches the budget proceeds",
			pcsgByComponent:           map[string]grovecorev1alpha1.PodCliqueScalingGroup{"decode": pcsgWithAvailableReplicas(6)},
			desiredReplicas:           map[string]int32{"decode": 6},
			maxUnavailableByComponent: map[string]int32{"decode": 2},
			drainByComponent:          map[string]int32{"decode": 2},
			want:                      true,
		},
		{
			description:               "a component the sub-step does not drain is not gated by its own unavailability",
			standalonePCLQByComponent: map[string]grovecorev1alpha1.PodClique{"frontend": pclqWithReadyReplicas(6)},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			drainByComponent:          map[string]int32{},
			want:                      true,
		},
		{
			description:               "a standalone PodClique reclaiming only missing old-version Pods proceeds even when over budget",
			standalonePCLQByComponent: map[string]grovecorev1alpha1.PodClique{"frontend": pclqWithReadyReplicas(6)},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			drainByComponent:          map[string]int32{"frontend": 3},
			numMissingOldVersionPods:  map[string]int32{"frontend": 4},
			want:                      true,
		},
		{
			description:               "a standalone drain beyond its missing old-version Pods crossing the budget holds",
			standalonePCLQByComponent: map[string]grovecorev1alpha1.PodClique{"frontend": pclqWithReadyReplicas(10)},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			drainByComponent:          map[string]int32{"frontend": 5},
			numMissingOldVersionPods:  map[string]int32{"frontend": 1},
			want:                      false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			// The budget gate reads the available count, the number of Pods that are Ready and not
			// terminating. Each case's PodClique ReadyReplicas stands in for that available count.
			availableByComponent := make(map[string]int32, len(tc.standalonePCLQByComponent))
			for name, pclq := range tc.standalonePCLQByComponent {
				availableByComponent[name] = pclq.Status.ReadyReplicas
			}
			assert.Equal(t, tc.want, maxUnavailableBudgetSatisfied(tc.pcsgByComponent, tc.desiredReplicas, tc.maxUnavailableByComponent, tc.drainByComponent, availableByComponent, tc.numMissingOldVersionPods))
		})
	}
}

// TestCurrentBatchScheduled covers the scheduling gate on the most recent current-hash batch. It reports
// true when no current-hash entry exists yet, and otherwise tracks whether the PodGangs at the latest
// current-hash epoch have been scheduled at least once.
func TestCurrentBatchScheduled(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSName, Namespace: coherentTestNamespace},
		Status:     grovecorev1alpha1.PodCliqueSetStatus{CurrentGenerationHash: ptr.To(coherentTestCurrentGen)},
	}
	rnr := apicommon.ResourceNameReplica{Name: coherentTestPCSName, Replica: 0}
	anchorEntry := grovecorev1alpha1.PodGangEntry{Epoch: "200", PodCliqueSetGenerationHash: coherentTestCurrentGen, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 2}}
	tailEntry := grovecorev1alpha1.PodGangEntry{Epoch: "200", PodCliqueSetGenerationHash: coherentTestCurrentGen, Role: grovecorev1alpha1.PodGangEntryRoleTail, PCSGReplicaIndices: map[string][]int32{"inference": {0, 1}}}
	anchorPodGang := apicommon.GenerateAnchorPodGangName(rnr, "200")
	tailPodGang0 := apicommon.GenerateNonAnchorPodGangName(rnr, "200", "inference", 0)
	tailPodGang1 := apicommon.GenerateNonAnchorPodGangName(rnr, "200", "inference", 1)

	t.Run("no current-hash entry yet so nothing to wait on", func(t *testing.T) {
		entries := []grovecorev1alpha1.PodGangEntry{{Epoch: "50", PodCliqueSetGenerationHash: coherentTestOldGen, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 2}}}
		r := _resource{client: testutils.NewTestClientBuilder().Build()}

		ready, err := r.currentBatchScheduled(t.Context(), pcs, 0, entries)

		require.NoError(t, err)
		assert.True(t, ready)
	})

	t.Run("anchor batch is scheduled", func(t *testing.T) {
		r := _resource{client: testutils.NewTestClientBuilder().WithObjects(podGangAtEpoch(anchorPodGang, true)).Build()}

		ready, err := r.currentBatchScheduled(t.Context(), pcs, 0, []grovecorev1alpha1.PodGangEntry{anchorEntry})

		require.NoError(t, err)
		assert.True(t, ready)
	})

	t.Run("anchor batch is not scheduled", func(t *testing.T) {
		r := _resource{client: testutils.NewTestClientBuilder().WithObjects(podGangAtEpoch(anchorPodGang, false)).Build()}

		ready, err := r.currentBatchScheduled(t.Context(), pcs, 0, []grovecorev1alpha1.PodGangEntry{anchorEntry})

		require.NoError(t, err)
		assert.False(t, ready)
	})

	t.Run("tail batch fully materialized and scheduled", func(t *testing.T) {
		r := _resource{client: testutils.NewTestClientBuilder().WithObjects(podGangAtEpoch(tailPodGang0, true), podGangAtEpoch(tailPodGang1, true)).Build()}

		ready, err := r.currentBatchScheduled(t.Context(), pcs, 0, []grovecorev1alpha1.PodGangEntry{tailEntry})

		require.NoError(t, err)
		assert.True(t, ready)
	})

	t.Run("tail batch only partially materialized holds", func(t *testing.T) {
		// The entry commits indices 0 and 1, but only index 0's PodGang exists. The gate must not pass on the
		// gang that happens to exist, since the executor has already taken down the old pods for index 1.
		r := _resource{client: testutils.NewTestClientBuilder().WithObjects(podGangAtEpoch(tailPodGang0, true)).Build()}

		ready, err := r.currentBatchScheduled(t.Context(), pcs, 0, []grovecorev1alpha1.PodGangEntry{tailEntry})

		require.NoError(t, err)
		assert.False(t, ready)
	})
}

// TestBuildCoherentUpdateEntries covers the sub-step authoring for a single standalone component frontend
// with liveReplicas 4, MinAvailable 2, and MaxUnavailable 2. The anchor commits 2 pods and one tail
// sub-step subsumes the remaining 2. Each case states the committed entries and asserts whether the entries
// are held unchanged or advanced.
func TestBuildCoherentUpdateEntries(t *testing.T) {
	pcsNameReplica := apicommon.ResourceNameReplica{Name: coherentTestPCSName, Replica: 0}
	anchorV1 := grovecorev1alpha1.PodGangEntry{Epoch: "50", PodCliqueSetGenerationHash: coherentTestOldGen, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 2}}
	anchorV2 := grovecorev1alpha1.PodGangEntry{Epoch: "200", PodCliqueSetGenerationHash: coherentTestCurrentGen, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 2}}

	t.Run("no sub-step remains so the entries are unchanged", func(t *testing.T) {
		fullyRolledAnchor := grovecorev1alpha1.PodGangEntry{Epoch: "200", PodCliqueSetGenerationHash: coherentTestCurrentGen, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: map[string]int32{"frontend": 4}}
		pgm := &grovecorev1alpha1.PodGangMap{Spec: grovecorev1alpha1.PodGangMapSpec{Entries: []grovecorev1alpha1.PodGangEntry{fullyRolledAnchor}}}
		snap := newCoherentTestSnapshot(pcsNameReplica, 4, 2, nil)
		r := _resource{client: testutils.NewTestClientBuilder().WithPodControllerUIDIndex().Build(), clk: clocktesting.NewFakeClock(metav1.Now().Time)}

		entries, err := r.buildCoherentUpdateEntries(t.Context(), snap, 0, pgm)

		require.NoError(t, err)
		assert.Equal(t, pgm.Spec.Entries, entries)
	})

	t.Run("gate holds when the current batch is not ready so the entries are unchanged", func(t *testing.T) {
		pgm := &grovecorev1alpha1.PodGangMap{Spec: grovecorev1alpha1.PodGangMapSpec{Entries: []grovecorev1alpha1.PodGangEntry{anchorV1, anchorV2}}}
		snap := newCoherentTestSnapshot(pcsNameReplica, 4, 2, ptr.To[int32](2))
		// No PodGang exists at the latest current-hash epoch, so currentBatchScheduled is false.
		r := _resource{client: testutils.NewTestClientBuilder().WithPodControllerUIDIndex().Build(), clk: clocktesting.NewFakeClock(metav1.Now().Time)}

		entries, err := r.buildCoherentUpdateEntries(t.Context(), snap, 0, pgm)

		require.NoError(t, err)
		assert.Equal(t, pgm.Spec.Entries, entries)
	})

	t.Run("gate passes so the next sub-step is emitted", func(t *testing.T) {
		pgm := &grovecorev1alpha1.PodGangMap{Spec: grovecorev1alpha1.PodGangMapSpec{Entries: []grovecorev1alpha1.PodGangEntry{anchorV1, anchorV2}}}
		snap := newCoherentTestSnapshot(pcsNameReplica, 4, 2, ptr.To[int32](2))
		r := _resource{client: testutils.NewTestClientBuilder().WithPodControllerUIDIndex().WithObjects(podGangAtEpoch(apicommon.GenerateAnchorPodGangName(pcsNameReplica, "200"), true)).Build(), clk: clocktesting.NewFakeClock(metav1.Now().Time)}

		entries, err := r.buildCoherentUpdateEntries(t.Context(), snap, 0, pgm)

		require.NoError(t, err)
		// The tail sub-step subsumes the remaining 2 pods into the current-hash anchor and drains the old
		// anchor to empty, leaving a single current-hash anchor carrying all 4 pods.
		require.Len(t, entries, 1)
		assert.Equal(t, coherentTestCurrentGen, entries[0].PodCliqueSetGenerationHash)
		assert.Equal(t, int32(4), entries[0].PodCliques["frontend"])
	})
}

// TestEntryHoldsInScopeContent checks the predicate that gates reconvergence, reporting whether an entry
// still carries pods or replica indices for any component within the coherent update scope.
func TestEntryHoldsInScopeContent(t *testing.T) {
	mvu := &mvuTemplate{
		standalonePCLQs: map[string]int32{"frontend": 1},
		pcsgs:           map[string]int32{"inference": 1},
	}
	testCases := []struct {
		description string
		entry       grovecorev1alpha1.PodGangEntry
		want        bool
	}{
		{"holds an in-scope standalone clique", grovecorev1alpha1.PodGangEntry{PodCliques: map[string]int32{"frontend": 2}}, true},
		{"holds in-scope PCSG replica indices", grovecorev1alpha1.PodGangEntry{PCSGReplicaIndices: map[string][]int32{"inference": {0}}}, true},
		{"holds only out-of-scope standalone content", grovecorev1alpha1.PodGangEntry{PodCliques: map[string]int32{"router": 3}}, false},
		{"in-scope standalone at zero count holds nothing", grovecorev1alpha1.PodGangEntry{PodCliques: map[string]int32{"frontend": 0}}, false},
		{"in-scope PCSG with empty indices holds nothing", grovecorev1alpha1.PodGangEntry{PCSGReplicaIndices: map[string][]int32{"inference": {}}}, false},
		{"empty entry holds nothing", grovecorev1alpha1.PodGangEntry{}, false},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			assert.Equal(t, tc.want, entryHoldsInScopeContent(tc.entry, mvu))
		})
	}
}

// TestAdvanceFullyDrainedEntries covers the three reconvergence outcomes for an old-generation entry. An
// entry drained of its in-scope content but still holding out-of-scope content advances to the current
// generation, an entry still holding in-scope content keeps its generation, and an empty entry is left for
// removeEmptyEntries to drop. frontend and inference are in scope, router is out of scope.
func TestAdvanceFullyDrainedEntries(t *testing.T) {
	mvu := &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": 1}, pcsgs: map[string]int32{"inference": 1}}
	entryAt := func(gen string, pclqs map[string]int32, pcsg map[string][]int32) grovecorev1alpha1.PodGangEntry {
		return grovecorev1alpha1.PodGangEntry{Epoch: "50", PodCliqueSetGenerationHash: gen, Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliques: pclqs, PCSGReplicaIndices: pcsg}
	}
	testCases := []struct {
		description string
		entry       grovecorev1alpha1.PodGangEntry
		wantGen     string
	}{
		{"advances an old-gen entry drained of in-scope content but holding out-of-scope content", entryAt(coherentTestOldGen, map[string]int32{"router": 2}, nil), coherentTestCurrentGen},
		{"keeps an old-gen entry still holding in-scope content", entryAt(coherentTestOldGen, nil, map[string][]int32{"inference": {0}}), coherentTestOldGen},
		{"leaves an empty old-gen entry for removal", entryAt(coherentTestOldGen, nil, nil), coherentTestOldGen},
		{"leaves a current-gen entry unchanged", entryAt(coherentTestCurrentGen, map[string]int32{"router": 2}, nil), coherentTestCurrentGen},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			got := advanceFullyDrainedEntries([]grovecorev1alpha1.PodGangEntry{tc.entry}, coherentTestCurrentGen, mvu)
			assert.Equal(t, tc.wantGen, got[0].PodCliqueSetGenerationHash)
		})
	}
}

// TestHeadroomByComponent checks the per-component drain headroom. For a standalone PodClique it is the
// running-Pod takedown headroom plus the free missing old-version term. For a PodCliqueScalingGroup it is
// only the running-replica takedown headroom, since a PodCliqueScalingGroup has no missing old-version term.
func TestHeadroomByComponent(t *testing.T) {
	testCases := []struct {
		description               string
		standalonePCLQByComponent map[string]grovecorev1alpha1.PodClique
		pcsgByComponent           map[string]grovecorev1alpha1.PodCliqueScalingGroup
		desiredReplicas           map[string]int32
		maxUnavailableByComponent map[string]int32
		numMissingOldVersionPods  map[string]int32
		want                      map[string]int32
	}{
		{
			description:               "standalone within budget and no missing old-version Pod leaves the remaining budget",
			standalonePCLQByComponent: map[string]grovecorev1alpha1.PodClique{"frontend": pclqWithReadyReplicas(8)},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			want:                      map[string]int32{"frontend": 1},
		},
		{
			description:               "standalone fully available takes the full budget",
			standalonePCLQByComponent: map[string]grovecorev1alpha1.PodClique{"frontend": pclqWithReadyReplicas(10)},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			want:                      map[string]int32{"frontend": 3},
		},
		{
			description:               "standalone over budget with no missing old-version Pod has zero headroom",
			standalonePCLQByComponent: map[string]grovecorev1alpha1.PodClique{"frontend": pclqWithReadyReplicas(6)},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			want:                      map[string]int32{"frontend": 0},
		},
		{
			description:               "standalone over budget with missing old-version Pods has headroom equal to that count",
			standalonePCLQByComponent: map[string]grovecorev1alpha1.PodClique{"frontend": pclqWithReadyReplicas(6)},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			numMissingOldVersionPods:  map[string]int32{"frontend": 4},
			want:                      map[string]int32{"frontend": 4},
		},
		{
			description:               "standalone within budget plus missing old-version Pods adds both",
			standalonePCLQByComponent: map[string]grovecorev1alpha1.PodClique{"frontend": pclqWithReadyReplicas(8)},
			desiredReplicas:           map[string]int32{"frontend": 10},
			maxUnavailableByComponent: map[string]int32{"frontend": 3},
			numMissingOldVersionPods:  map[string]int32{"frontend": 1},
			want:                      map[string]int32{"frontend": 2},
		},
		{
			description:               "PodCliqueScalingGroup within budget leaves the remaining budget",
			pcsgByComponent:           map[string]grovecorev1alpha1.PodCliqueScalingGroup{"decode": pcsgWithAvailableReplicas(6)},
			desiredReplicas:           map[string]int32{"decode": 6},
			maxUnavailableByComponent: map[string]int32{"decode": 2},
			want:                      map[string]int32{"decode": 2},
		},
		{
			description:               "PodCliqueScalingGroup over budget has zero headroom and ignores any missing old-version count",
			pcsgByComponent:           map[string]grovecorev1alpha1.PodCliqueScalingGroup{"decode": pcsgWithAvailableReplicas(4)},
			desiredReplicas:           map[string]int32{"decode": 6},
			maxUnavailableByComponent: map[string]int32{"decode": 2},
			numMissingOldVersionPods:  map[string]int32{"decode": 5},
			want:                      map[string]int32{"decode": 0},
		},
		{
			description:               "standalone and PodCliqueScalingGroup headroom computed together",
			standalonePCLQByComponent: map[string]grovecorev1alpha1.PodClique{"frontend": pclqWithReadyReplicas(8)},
			pcsgByComponent:           map[string]grovecorev1alpha1.PodCliqueScalingGroup{"decode": pcsgWithAvailableReplicas(6)},
			desiredReplicas:           map[string]int32{"frontend": 10, "decode": 6},
			maxUnavailableByComponent: map[string]int32{"frontend": 3, "decode": 2},
			numMissingOldVersionPods:  map[string]int32{"frontend": 1},
			want:                      map[string]int32{"frontend": 2, "decode": 2},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			availableByComponent := make(map[string]int32, len(tc.standalonePCLQByComponent))
			for name, pclq := range tc.standalonePCLQByComponent {
				availableByComponent[name] = pclq.Status.ReadyReplicas
			}
			assert.Equal(t, tc.want, headroomByComponent(tc.pcsgByComponent, tc.desiredReplicas, tc.maxUnavailableByComponent, availableByComponent, tc.numMissingOldVersionPods))
		})
	}
}

// TestNumMissingOldVersionPodsByStandalonePCLQ covers the count of missing old-version Pods per standalone
// PodClique: old-version anchor deficits are counted, current-version anchors and out-of-scope cliques are
// ignored, running at or above committed yields none, and deficits sum across old anchors.
func TestNumMissingOldVersionPodsByStandalonePCLQ(t *testing.T) {
	anchor := func(gen, epoch string, pclqs map[string]int32) grovecorev1alpha1.PodGangEntry {
		return grovecorev1alpha1.PodGangEntry{Role: grovecorev1alpha1.PodGangEntryRoleAnchor, PodCliqueSetGenerationHash: gen, Epoch: epoch, PodCliques: pclqs}
	}
	testCases := []struct {
		description string
		entries     []grovecorev1alpha1.PodGangEntry
		running     map[string]map[string]int32
		want        map[string]int32
	}{
		{
			description: "an old-version anchor with fewer running Pods than committed yields the deficit",
			entries:     []grovecorev1alpha1.PodGangEntry{anchor(coherentTestOldGen, "50", map[string]int32{"frontend": 3})},
			running:     map[string]map[string]int32{"frontend": {"50": 2}},
			want:        map[string]int32{"frontend": 1},
		},
		{
			description: "a current-version anchor is never a missing old-version Pod",
			entries:     []grovecorev1alpha1.PodGangEntry{anchor(coherentTestCurrentGen, "200", map[string]int32{"frontend": 3})},
			running:     map[string]map[string]int32{"frontend": {"200": 0}},
			want:        map[string]int32{},
		},
		{
			description: "an out-of-scope clique on an old-version anchor is ignored",
			entries:     []grovecorev1alpha1.PodGangEntry{anchor(coherentTestOldGen, "50", map[string]int32{"frontend": 2, "sidecar": 2})},
			running:     map[string]map[string]int32{"frontend": {"50": 2}},
			want:        map[string]int32{},
		},
		{
			description: "running equal to committed yields no missing old-version Pod",
			entries:     []grovecorev1alpha1.PodGangEntry{anchor(coherentTestOldGen, "50", map[string]int32{"frontend": 2})},
			running:     map[string]map[string]int32{"frontend": {"50": 2}},
			want:        map[string]int32{},
		},
		{
			description: "more running than committed clamps to zero",
			entries:     []grovecorev1alpha1.PodGangEntry{anchor(coherentTestOldGen, "50", map[string]int32{"frontend": 2})},
			running:     map[string]map[string]int32{"frontend": {"50": 3}},
			want:        map[string]int32{},
		},
		{
			description: "deficits sum across multiple old-version anchors",
			entries: []grovecorev1alpha1.PodGangEntry{
				anchor(coherentTestOldGen, "50", map[string]int32{"frontend": 2}),
				anchor("v0", "40", map[string]int32{"frontend": 3}),
			},
			running: map[string]map[string]int32{"frontend": {"50": 1, "40": 1}},
			want:    map[string]int32{"frontend": 3},
		},
		{
			description: "a non-anchor entry is ignored",
			entries:     []grovecorev1alpha1.PodGangEntry{{Role: grovecorev1alpha1.PodGangEntryRoleTail, PodCliqueSetGenerationHash: coherentTestOldGen, Epoch: "60", PodCliques: map[string]int32{"frontend": 5}}},
			running:     map[string]map[string]int32{"frontend": {}},
			want:        map[string]int32{},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			assert.Equal(t, tc.want, numMissingOldVersionPodsByStandalonePCLQ(tc.entries, coherentTestCurrentGen, tc.running))
		})
	}
}

// TestGatherStandalonePodCounts covers the single-pass Pod count read. runningByCliqueAndAnchor buckets
// non-terminating Pods by their grove.io/podgang label mapped to an anchor epoch, and excludes terminating
// Pods and Pods on a PodGang that is not an anchor of this replica. availableByComponent counts Pods that
// are Ready and not terminating across all of the PodClique's Pods, so it excludes a not-ready Pod and a
// Ready-but-terminating Pod, and includes a Ready Pod that is not on an anchor.
func TestGatherStandalonePodCounts(t *testing.T) {
	rnr := apicommon.ResourceNameReplica{Name: coherentTestPCSName, Replica: 0}
	pcs := &grovecorev1alpha1.PodCliqueSet{ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSName, Namespace: coherentTestNamespace}}
	pclq := grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: "frontend-pclq", Namespace: coherentTestNamespace, UID: "frontend-uid"}}
	entries := []grovecorev1alpha1.PodGangEntry{
		{Role: grovecorev1alpha1.PodGangEntryRoleAnchor, Epoch: "50"},
		{Role: grovecorev1alpha1.PodGangEntryRoleAnchor, Epoch: "200"},
	}
	gang50 := apicommon.GenerateAnchorPodGangName(rnr, "50")
	gang200 := apicommon.GenerateAnchorPodGangName(rnr, "200")
	pod := func(name, gang string, terminating, ready bool) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       coherentTestNamespace,
			Labels:          map[string]string{apicommon.LabelPodGang: gang},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "grove.io/v1alpha1", Kind: "PodClique", Name: pclq.Name, UID: pclq.UID, Controller: ptr.To(true)}},
		}}
		if terminating {
			p.DeletionTimestamp = ptr.To(metav1.Now())
			p.Finalizers = []string{"grove.io/test"}
		}
		if ready {
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		}
		return p
	}
	objs := []client.Object{
		pod("p1", gang50, false, true),
		pod("p2", gang50, false, false),
		pod("p3", gang200, false, true),
		pod("p4", gang200, true, true),
		pod("p5", "unrelated-gang", false, true),
	}
	r := _resource{client: testutils.NewTestClientBuilder().WithPodControllerUIDIndex().WithObjects(objs...).Build()}

	got, err := r.gatherStandalonePodCounts(t.Context(), pcs, 0, entries, map[string]grovecorev1alpha1.PodClique{"frontend": pclq})

	require.NoError(t, err)
	assert.Equal(t, map[string]map[string]int32{"frontend": {"50": 2, "200": 1}}, got.runningByCliqueAndAnchor)
	assert.Equal(t, map[string]int32{"frontend": 3}, got.availableByComponent)
}

// pclqWithUpdatedScheduledReplicas builds a standalone PodClique reporting the given new-hash scheduled
// Pod count on its in-progress UpdateProgress.
func pclqWithUpdatedScheduledReplicas(updatedReady int32) grovecorev1alpha1.PodClique {
	return grovecorev1alpha1.PodClique{
		Status: grovecorev1alpha1.PodCliqueStatus{
			UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{UpdatedScheduledReplicas: updatedReady},
		},
	}
}

// pclqWithReadyReplicas builds a standalone PodClique reporting the given total Ready Pod count.
func pclqWithReadyReplicas(ready int32) grovecorev1alpha1.PodClique {
	return grovecorev1alpha1.PodClique{Status: grovecorev1alpha1.PodCliqueStatus{ReadyReplicas: ready}}
}

// pcsgWithAvailableReplicas builds a PodCliqueScalingGroup reporting the given available replica count.
func pcsgWithAvailableReplicas(available int32) grovecorev1alpha1.PodCliqueScalingGroup {
	return grovecorev1alpha1.PodCliqueScalingGroup{Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{AvailableReplicas: available}}
}

// podGangAtEpoch builds a PodGang owned by the test PCS replica 0 and stamped with the given epoch, marked
// scheduled when scheduled is true.
func podGangAtEpoch(name string, scheduled bool) *groveschedulerv1alpha1.PodGang {
	builder := testutils.NewPodGangBuilder(name, coherentTestNamespace).
		WithLabels(map[string]string{
			apicommon.LabelPartOfKey:                coherentTestPCSName,
			apicommon.LabelPodCliqueSetReplicaIndex: "0",
			apicommon.LabelEpoch:                    "200",
		})
	if scheduled {
		builder = builder.WithLastScheduled()
	}
	return builder.Build()
}

// newCoherentTestSnapshot builds a syncSnapshot for a single standalone component frontend under a coherent
// update, with the given live replicas and MinAvailable. maxUnavailable sets the component's
// RollingUpdate.MaxUnavailable, or leaves it unset so the Coherent default of MinAvailable applies. It sets
// a Ready and UpdatedReady count equal to liveReplicas so the subsumed-Pods and budget gates pass unless a
// case overrides them.
func newCoherentTestSnapshot(pcsNameReplica apicommon.ResourceNameReplica, liveReplicas, minAvailable int32, maxUnavailable *int32) *syncSnapshot {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: coherentTestPCSName, Namespace: coherentTestNamespace},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{
			Replicas: 1,
			Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
					{
						Name:          "frontend",
						RollingUpdate: &grovecorev1alpha1.RollingUpdateConfiguration{MaxUnavailable: maxUnavailable},
						Spec: grovecorev1alpha1.PodCliqueSpec{
							Replicas:     liveReplicas,
							MinAvailable: ptr.To(minAvailable),
						},
					},
				},
			},
		},
		Status: grovecorev1alpha1.PodCliqueSetStatus{CurrentGenerationHash: ptr.To(coherentTestCurrentGen)},
	}
	frontendPCLQ := grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: apicommon.GeneratePodCliqueName(pcsNameReplica, "frontend")},
		Spec:       grovecorev1alpha1.PodCliqueSpec{Replicas: liveReplicas},
		Status: grovecorev1alpha1.PodCliqueStatus{
			ReadyReplicas:  liveReplicas,
			UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{UpdatedScheduledReplicas: liveReplicas},
		},
	}
	return &syncSnapshot{
		pcs:                              pcs,
		mvuTemplate:                      &mvuTemplate{standalonePCLQs: map[string]int32{"frontend": minAvailable}},
		existingStandalonePCLQsByReplica: map[int][]grovecorev1alpha1.PodClique{0: {frontendPCLQ}},
	}
}

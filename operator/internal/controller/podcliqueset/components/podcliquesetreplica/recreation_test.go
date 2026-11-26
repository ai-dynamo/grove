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

package podcliquesetreplica

import (
	"slices"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// TestGroupPCSReplicaReplacements covers lifecycle grouping, source ordering, and owned conversion of
// retired-source indices from API progress. Preparation and mutations of the result must leave progress intact.
func TestGroupPCSReplicaReplacements(t *testing.T) {
	t.Parallel()
	completedAt := metav1.Now()
	canceledAt := metav1.Now()
	tests := []struct {
		name     string
		progress *grovecorev1beta1.PodCliqueSetUpdateProgress
		want     pcsReplicaReplacementGroups
	}{
		{name: "no update progress"},
		{
			name: "replica recreation not required ignores replacement entries",
			progress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
					ReplicaIndex: 2,
					Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
				}},
			},
		},
		{
			name: "completed update ignores replacement entries",
			progress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				RequiresReplicaRecreation: true,
				UpdateEndedAt:             &completedAt,
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
					ReplicaIndex: 2,
					Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
				}},
			},
		},
		{
			name: "groups and sorts incomplete replacement entries",
			progress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				RequiresReplicaRecreation: true,
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
					{ReplicaIndex: 5, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 3}},
					{ReplicaIndex: 1},
					{ReplicaIndex: 7, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 6, CanceledAt: &canceledAt}},
					{ReplicaIndex: 4, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 2}, UpdateEndedAt: &completedAt},
					{ReplicaIndex: 2, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1}},
					{ReplicaIndex: 3, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0, CanceledAt: &canceledAt}},
				},
			},
			want: pcsReplicaReplacementGroups{
				active: []pcsReplicaReplacement{
					{sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1},
					{sourcePCSReplicaIndex: 5, targetPCSReplicaIndex: 3},
				},
				canceled: []pcsReplicaReplacement{
					{sourcePCSReplicaIndex: 3, targetPCSReplicaIndex: 0},
					{sourcePCSReplicaIndex: 7, targetPCSReplicaIndex: 6},
				},
			},
		},
		{
			name: "nil and empty retired-source lists preserve the zero value",
			progress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				RequiresReplicaRecreation: true,
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
					{ReplicaIndex: 0, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0}},
					{
						ReplicaIndex: 3,
						Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
							TargetReplicaIndex: 1, RetiredSourceReplicaIndices: []int32{},
						},
					},
					{
						ReplicaIndex: 2,
						Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
							TargetReplicaIndex: 2, RetiredSourceReplicaIndices: []int32{}, CanceledAt: &canceledAt,
						},
					},
				},
			},
			want: pcsReplicaReplacementGroups{
				active: []pcsReplicaReplacement{
					{sourcePCSReplicaIndex: 0, targetPCSReplicaIndex: 0},
					{sourcePCSReplicaIndex: 3, targetPCSReplicaIndex: 1},
				},
				canceled: []pcsReplicaReplacement{{sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 2}},
			},
		},
		{
			name: "active and canceled replacements retain their previous-source cleanup indices",
			progress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				RequiresReplicaRecreation: true,
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
					{
						ReplicaIndex: 5,
						Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
							TargetReplicaIndex: 2, RetiredSourceReplicaIndices: []int32{6, 3}, CanceledAt: &canceledAt,
						},
					},
					{
						ReplicaIndex: 1,
						Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
							TargetReplicaIndex: 1, RetiredSourceReplicaIndices: []int32{4, 0},
						},
					},
				},
			},
			want: pcsReplicaReplacementGroups{
				active: []pcsReplicaReplacement{{
					sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, retiredSourcePCSReplicaIndices: []int{4, 0},
				}},
				canceled: []pcsReplicaReplacement{{
					sourcePCSReplicaIndex: 5, targetPCSReplicaIndex: 2, retiredSourcePCSReplicaIndices: []int{6, 3},
				}},
			},
		},
		{
			name: "retired indices retain the full non-negative int32 range",
			progress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
				RequiresReplicaRecreation: true,
				CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
					ReplicaIndex: 1,
					Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
						TargetReplicaIndex: 1, RetiredSourceReplicaIndices: []int32{0, 1<<31 - 1},
					},
				}},
			},
			want: pcsReplicaReplacementGroups{
				active: []pcsReplicaReplacement{{
					sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, retiredSourcePCSReplicaIndices: []int{0, 1<<31 - 1},
				}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			progress := tc.progress.DeepCopy()
			before := progress.DeepCopy()

			got := groupPCSReplicaReplacements(progress)
			again := groupPCSReplicaReplacements(progress)

			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.want, again)
			assert.Equal(t, before, progress, "grouping must not mutate persisted progress")
			for _, replacements := range [][]pcsReplicaReplacement{got.active, got.canceled} {
				for _, replacement := range replacements {
					for i := range replacement.retiredSourcePCSReplicaIndices {
						replacement.retiredSourcePCSReplicaIndices[i] = 99
					}
				}
			}
			assert.Equal(t, before, progress, "mutating grouped indices must not mutate persisted progress")
			assert.Equal(t, tc.want, again, "independent grouping results must not share retired-source slices")
		})
	}
}

// TestPCSReplicaReplacementGroupsReservedReplicaIndices covers source, target, and retired-source
// reservations, set deduplication, and independence of the returned sets from replacement progress.
func TestPCSReplicaReplacementGroupsReservedReplicaIndices(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		groups pcsReplicaReplacementGroups
		want   sets.Set[int]
	}{
		{
			name: "zero-value groups return an empty set",
			want: sets.New[int](),
		},
		{
			name: "empty groups return an empty set",
			groups: pcsReplicaReplacementGroups{
				active:   []pcsReplicaReplacement{},
				canceled: []pcsReplicaReplacement{},
			},
			want: sets.New[int](),
		},
		{
			name: "active replacements reserve same-index and different-index mappings",
			groups: pcsReplicaReplacementGroups{
				active: []pcsReplicaReplacement{
					{sourcePCSReplicaIndex: 0, targetPCSReplicaIndex: 0},
					{sourcePCSReplicaIndex: 5, targetPCSReplicaIndex: 3},
				},
			},
			want: sets.New(0, 3, 5),
		},
		{
			name: "canceled replacements reserve same-index and different-index mappings",
			groups: pcsReplicaReplacementGroups{
				canceled: []pcsReplicaReplacement{
					{sourcePCSReplicaIndex: 0, targetPCSReplicaIndex: 0},
					{sourcePCSReplicaIndex: 7, targetPCSReplicaIndex: 6},
				},
			},
			want: sets.New(0, 6, 7),
		},
		{
			name: "active and canceled replacements reserve every source and target",
			groups: pcsReplicaReplacementGroups{
				active:   []pcsReplicaReplacement{{sourcePCSReplicaIndex: 0, targetPCSReplicaIndex: 2}},
				canceled: []pcsReplicaReplacement{{sourcePCSReplicaIndex: 3, targetPCSReplicaIndex: 1}},
			},
			want: sets.New(0, 1, 2, 3),
		},
		{
			name: "duplicate mappings and shared indices are deduplicated",
			groups: pcsReplicaReplacementGroups{
				active: []pcsReplicaReplacement{
					{sourcePCSReplicaIndex: 5, targetPCSReplicaIndex: 3},
					{sourcePCSReplicaIndex: 5, targetPCSReplicaIndex: 3},
				},
				canceled: []pcsReplicaReplacement{
					{sourcePCSReplicaIndex: 3, targetPCSReplicaIndex: 1},
					{sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1},
				},
			},
			want: sets.New(1, 3, 5),
		},
		{
			name: "active replacements reserve every retired source including index zero",
			groups: pcsReplicaReplacementGroups{active: []pcsReplicaReplacement{{
				sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, retiredSourcePCSReplicaIndices: []int{0, 2, 4},
			}}},
			want: sets.New(0, 1, 2, 4),
		},
		{
			name: "canceled replacements reserve every retired source",
			groups: pcsReplicaReplacementGroups{canceled: []pcsReplicaReplacement{{
				sourcePCSReplicaIndex: 3, targetPCSReplicaIndex: 5, retiredSourcePCSReplicaIndices: []int{0, 2, 4},
			}}},
			want: sets.New(0, 2, 3, 4, 5),
		},
		{
			name: "retired indices shared by replacement groups are deduplicated",
			groups: pcsReplicaReplacementGroups{
				active: []pcsReplicaReplacement{{
					sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, retiredSourcePCSReplicaIndices: []int{2, 4},
				}},
				canceled: []pcsReplicaReplacement{{
					sourcePCSReplicaIndex: 3, targetPCSReplicaIndex: 3, retiredSourcePCSReplicaIndices: []int{2, 4},
				}},
			},
			want: sets.New(1, 2, 3, 4),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			groupsBefore := pcsReplicaReplacementGroups{
				active:   slices.Clone(tc.groups.active),
				canceled: slices.Clone(tc.groups.canceled),
			}
			for _, replacements := range [][]pcsReplicaReplacement{groupsBefore.active, groupsBefore.canceled} {
				for i := range replacements {
					replacements[i].retiredSourcePCSReplicaIndices = slices.Clone(replacements[i].retiredSourcePCSReplicaIndices)
				}
			}

			got := tc.groups.reservedReplicaIndices()
			another := tc.groups.reservedReplicaIndices()

			require.Equal(t, tc.want, got, "reservedReplicaIndices must include all active and canceled source, target, and retired-source indices")
			require.Equal(t, tc.want, another, "reservedReplicaIndices must return the same reservations on each call")
			clear(got)
			got.Insert(99)
			assert.Equal(t, tc.want, another, "reservedReplicaIndices returned aliased sets")
			assert.Equal(t, tc.want, tc.groups.reservedReplicaIndices(), "mutating returned indices changed later reservations")
			assert.Equal(t, groupsBefore, tc.groups, "reservedReplicaIndices mutated replacement groups")
		})
	}
}

func TestPCSReplicaRecreationPlannerNext(t *testing.T) {
	t.Run("selects the next replacement when no replacement is active", func(t *testing.T) {
		pcs := schedulingConfigRolloutTestPCS()
		replicaInfosByIndex := pcsReplicaInfoByIndex{
			0: rollingUpdateReplicaInfoAtGeneration(pcs, 0, "pending", false),
		}
		planner := newPCSReplicaRecreationPlanner(pcs, replicaInfosByIndex, pcsReplicaScalingIndices{})

		got := planner.next()

		assert.Equal(t, &pcsReplicaReplacement{sourcePCSReplicaIndex: 0, targetPCSReplicaIndex: 0}, got)
	})

	t.Run("returns nil when an active replacement consumes the budget", func(t *testing.T) {
		pcs := schedulingConfigRolloutTestPCS()
		pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
			ReplicaIndex: 0,
			Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
		}}
		planner := newPCSReplicaRecreationPlanner(pcs, pcsReplicaInfoByIndex{}, pcsReplicaScalingIndices{})

		assert.Nil(t, planner.next())
	})

	t.Run("returns nil when no source replica needs recreation", func(t *testing.T) {
		pcs := schedulingConfigRolloutTestPCS()
		replicaInfosByIndex := pcsReplicaInfoByIndex{
			0: replicaRecreationSelectionInfo(pcs, 0, *pcs.Status.CurrentGenerationHash, true, false),
		}
		planner := newPCSReplicaRecreationPlanner(pcs, replicaInfosByIndex, pcsReplicaScalingIndices{})

		assert.Nil(t, planner.next())
	})
}

// TestDifferentIndexCleanupConfirmationReleasesSourceForNextReplacement verifies that an absent source
// remains reserved until participant-confirmed cleanup and target completion. After 2 -> 1 completes,
// source 3 can allocate target 2 without reopening the historical source-2 completion record.
func TestDifferentIndexCleanupConfirmationReleasesSourceForNextReplacement(t *testing.T) {
	t.Parallel()
	startedAt := metav1.NewTime(time.Unix(100, 0).UTC())
	firstCompletedAt := metav1.NewTime(time.Unix(200, 0).UTC())
	cleanupCompletedAt := metav1.NewTime(time.Unix(300, 0).UTC())
	completedAt := metav1.NewTime(time.Unix(400, 0).UTC())
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "test-uid").
		WithReplicas(3).
		WithObservedGeneration(new(int64(1))).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithMinAvailable(1).Build()).
		WithPodCliqueSetGenerationHash(new("current")).
		WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
			RequiresReplicaRecreation:          true,
			UpdateStartedAt:                    startedAt,
			PreviousPodCliqueSetGenerationHash: new("previous"),
			CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				{ReplicaIndex: 0, UpdateStartedAt: startedAt, UpdateEndedAt: &firstCompletedAt,
					Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0, SourceCleanupCompletedAt: &startedAt}},
				{ReplicaIndex: 2, UpdateStartedAt: startedAt,
					Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1}},
			},
		}).Build()
	replicaInfos := pcsReplicaInfoByIndex{
		0: rollingUpdateReplicaInfo(pcs, 0, true),
		1: rollingUpdateReplicaInfo(pcs, 1, true),
		3: rollingUpdateReplicaInfoAtGeneration(pcs, 3, "previous", true),
	}
	hooks := newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, pcsReplicaScalingIndices{})

	pending, err := hooks.reconcileCurrentlyUpdatingReplicas(pcs.Status.UpdateProgress.CurrentlyUpdating)

	require.NoError(t, err)
	assert.Empty(t, pending.completedPCSReplicaIndices, "source 2 absence cannot confirm cleanup")
	assert.Empty(t, hooks.selectReplicasToUpdate(), "source 3 must wait for the active replacement")
	groups := groupPCSReplicaReplacements(pcs.Status.UpdateProgress)
	assert.True(t, groups.reservedReplicaIndices().Has(2), "source 2 remains reserved during cleanup")
	state := computePCSReplicaState(pcs, pcsReplicaScalingIndices{}, replicaInfos.existingReplicaIndices(), groups)
	assert.Equal(t, []int{2}, state.ScaleInReplicaIndices)
	assert.Contains(t, state.ExpectedReplicaIndices, 1, "different-index target creation may overlap source cleanup")
	assert.Contains(t, state.ScaleOutReplicaIndices, 1)

	// Model post-sync participant confirmation, followed by update advancement recording completion.
	pcs.Status.UpdateProgress.CurrentlyUpdating[1].Replacement.SourceCleanupCompletedAt = &cleanupCompletedAt
	confirmed, err := hooks.reconcileCurrentlyUpdatingReplicas(pcs.Status.UpdateProgress.CurrentlyUpdating)
	require.NoError(t, err)
	require.Equal(t, []int{2}, confirmed.completedPCSReplicaIndices)
	pcs.Status.UpdateProgress.CurrentlyUpdating[1].UpdateEndedAt = &completedAt
	historicalCompletion := pcs.Status.UpdateProgress.CurrentlyUpdating[1].DeepCopy()

	selected := hooks.selectReplicasToUpdate()

	require.Equal(t, []pcsReplicaUpdateSelection{{replicaIndex: 3, targetReplicaIndex: new(2)}}, selected)
	pcs.Status.UpdateProgress.CurrentlyUpdating = append(pcs.Status.UpdateProgress.CurrentlyUpdating,
		grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex:    3,
			UpdateStartedAt: metav1.NewTime(time.Unix(500, 0).UTC()),
			Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex: 2, SourceCleanupCompletedAt: new(metav1.NewTime(time.Unix(600, 0).UTC())),
			},
		},
	)
	freshTarget := rollingUpdateReplicaInfo(pcs, 2, true)
	freshTarget.standalonePCLQs[0].Status.ReadyReplicas = 0
	replicaInfos[2] = freshTarget

	reused, err := hooks.reconcileCurrentlyUpdatingReplicas(pcs.Status.UpdateProgress.CurrentlyUpdating)

	require.NoError(t, err)
	assert.Empty(t, reused.completedPCSReplicaIndices, "fresh target 2 cannot inherit source 2's historical completion")
	assert.Nil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[2].UpdateEndedAt)
	assert.Equal(t, *historicalCompletion, pcs.Status.UpdateProgress.CurrentlyUpdating[1], "target reuse must preserve the historical 2 -> 1 completion")
}

func TestRecreationSelectionUsesAppliedTemplatesNotBootstrapStatus(t *testing.T) {
	cases := []struct {
		name             string
		marker           string
		recordedHash     *string
		reservedScaleOut bool
		reservedScaleIn  bool
		want             []int
	}{
		{name: "fresh S1 after scale-out reservation clears", marker: "current", want: []int{}},
		{name: "S1 materialized with old status", marker: "current", recordedHash: new("superseded"), want: []int{}},
		{name: "S0 bootstrap interrupted by S1", marker: "superseded", want: []int{0}},
		{name: "S0 status cannot substitute for missing marker", recordedHash: new("superseded"), want: []int{0}},
		{name: "unknown resource remains eligible", want: []int{0}},
		{name: "S0 reservation still excludes recreation", marker: "superseded", reservedScaleOut: true, want: []int{}},
		{name: "scale-in excludes old replica from recreation", marker: "superseded", reservedScaleIn: true, want: []int{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := replicaRecreationSelectionTestPCS()
			info := replicaRecreationSelectionInfo(pcs, 0, tc.marker, true, true)
			info.standalonePCLQs[0].Status.CurrentPodCliqueSetGenerationHash = tc.recordedHash
			info.pcsgs[0].Status.CurrentPodCliqueSetGenerationHash = tc.recordedHash
			scaling := pcsReplicaScalingIndices{}
			if tc.reservedScaleOut {
				scaling.scaleOut = []int{0}
			}
			if tc.reservedScaleIn {
				scaling.scaleIn = []int{0}
			}
			got := newPCSReplicaRecreationPlanner(pcs, pcsReplicaInfoByIndex{0: info}, scaling).selectSourceReplicaIndices(1)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRecreationCompletionDoesNotWaitForMaterializedScaleOutStatus(t *testing.T) {
	pcs := replicaRecreationSelectionTestPCS()
	pcs.Spec.Replicas = 1
	info := replicaRecreationSelectionInfo(pcs, 0, "current", true, true)
	info.standalonePCLQs[0].Status = grovecorev1beta1.PodCliqueStatus{}
	info.pcsgs[0].Status = grovecorev1beta1.PodCliqueScalingGroupStatus{}
	assert.True(t, info.hasMaterializedPCSGeneration(pcs, "current"))
	hooks := newPCSReplicaRecreationStrategyHooks(pcs, pcsReplicaInfoByIndex{0: info}, pcsReplicaScalingIndices{})
	assert.False(t, info.isUpdateComplete(pcs), "this replica has not completed its live convergence")
	assert.True(t, hooks.isUpdateComplete(), "fresh S1 scale-out with no update entry must not delay recreation progress")
}

func TestPCSReplicaRecreationPlannerSelectSourceReplicaIndices(t *testing.T) {
	t.Run("selects replicas in rollout priority order", func(t *testing.T) {
		pcs := schedulingConfigRolloutTestPCS()
		pcsReplicaInfosByIndex := pcsReplicaInfoByIndex{
			5: rollingUpdateReplicaInfoAtGeneration(pcs, 5, "superseded", true),
			4: rollingUpdateReplicaInfoAtGeneration(pcs, 4, "pending", true),
			3: rollingUpdateReplicaInfoAtGeneration(pcs, 3, "pending", false),
			2: rollingUpdateReplicaInfoAtGeneration(pcs, 2, "superseded", true),
			1: rollingUpdateReplicaInfoAtGeneration(pcs, 1, "pending", true),
			0: rollingUpdateReplicaInfoAtGeneration(pcs, 0, "pending", false),
			6: replicaRecreationSelectionInfo(pcs, 6, *pcs.Status.CurrentGenerationHash, true, false),
		}

		got := newPCSReplicaRecreationPlanner(pcs, pcsReplicaInfosByIndex, pcsReplicaScalingIndices{}).selectSourceReplicaIndices(len(pcsReplicaInfosByIndex))

		assert.Equal(t, []int{0, 3, 1, 4, 2, 5}, got)
	})

	t.Run("selects no more replicas than the remaining update slots", func(t *testing.T) {
		pcs := schedulingConfigRolloutTestPCS()
		pcsReplicaInfosByIndex := pcsReplicaInfoByIndex{
			0: rollingUpdateReplicaInfoAtGeneration(pcs, 0, "pending", false),
			1: rollingUpdateReplicaInfoAtGeneration(pcs, 1, "pending", false),
		}

		got := newPCSReplicaRecreationPlanner(pcs, pcsReplicaInfosByIndex, pcsReplicaScalingIndices{}).selectSourceReplicaIndices(1)

		assert.Equal(t, []int{0}, got)
	})

	t.Run("excludes scale-in and active replacement commitments", func(t *testing.T) {
		pcs := schedulingConfigRolloutTestPCS()
		pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			{ReplicaIndex: 1, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 2}},
		}
		pcsReplicaInfosByIndex := pcsReplicaInfoByIndex{
			0: rollingUpdateReplicaInfoAtGeneration(pcs, 0, "pending", false),
			1: rollingUpdateReplicaInfoAtGeneration(pcs, 1, "pending", false),
			2: rollingUpdateReplicaInfoAtGeneration(pcs, 2, "pending", false),
			3: rollingUpdateReplicaInfoAtGeneration(pcs, 3, "pending", false),
		}

		got := newPCSReplicaRecreationPlanner(
			pcs,
			pcsReplicaInfosByIndex,
			pcsReplicaScalingIndices{scaleIn: []int{0}},
		).selectSourceReplicaIndices(len(pcsReplicaInfosByIndex))

		assert.Equal(t, []int{3}, got)
	})

	t.Run("excludes canceled replacement source and target while cleanup is pending", func(t *testing.T) {
		pcs := schedulingConfigRolloutTestPCS()
		pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
			ReplicaIndex: 1,
			Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex: 2,
				CanceledAt:         new(metav1.Now()),
			},
		}}
		pcsReplicaInfosByIndex := pcsReplicaInfoByIndex{
			1: rollingUpdateReplicaInfoAtGeneration(pcs, 1, "pending", false),
			2: rollingUpdateReplicaInfoAtGeneration(pcs, 2, "pending", false),
			3: rollingUpdateReplicaInfoAtGeneration(pcs, 3, "pending", false),
		}
		planner := newPCSReplicaRecreationPlanner(pcs, pcsReplicaInfosByIndex, pcsReplicaScalingIndices{})

		got := planner.selectSourceReplicaIndices(len(pcsReplicaInfosByIndex))

		assert.Equal(t, []int{3}, got)
	})

	for _, tc := range []struct {
		name       string
		isCanceled bool
	}{
		{name: "excludes retired sources of active replacements"},
		{name: "excludes retired sources of canceled replacements", isCanceled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			replacementProgress := &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex: 2, RetiredSourceReplicaIndices: []int32{0},
			}
			if tc.isCanceled {
				canceledAt := metav1.NewTime(time.Unix(150, 0).UTC())
				replacementProgress.CanceledAt = &canceledAt
			}
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "uid").
				WithStandaloneClique("worker").
				WithPodCliqueSetGenerationHash(new("current")).
				WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
					RequiresReplicaRecreation: true,
					CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
						ReplicaIndex: 1, Replacement: replacementProgress,
					}},
				}).Build()
			pcsReplicaInfosByIndex := pcsReplicaInfoByIndex{
				0: rollingUpdateReplicaInfoAtGeneration(pcs, 0, "superseded", false),
				3: rollingUpdateReplicaInfoAtGeneration(pcs, 3, "superseded", false),
			}
			before := pcs.DeepCopy()
			planner := newPCSReplicaRecreationPlanner(pcs, pcsReplicaInfosByIndex, pcsReplicaScalingIndices{})

			got := planner.selectSourceReplicaIndices(len(pcsReplicaInfosByIndex))

			assert.Equal(t, []int{3}, got, "retired index 0 must not become a new recreation source")
			assert.Equal(t, before, pcs, "source selection must not mutate replacement progress")
		})
	}

	t.Run("completed source keys and target identities cannot be reselected", func(t *testing.T) {
		pcs := schedulingConfigRolloutTestPCS()
		pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
			ReplicaIndex:  0,
			Replacement:   &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
			UpdateEndedAt: new(metav1.Now()),
		}}
		pcsReplicaInfosByIndex := pcsReplicaInfoByIndex{
			0: rollingUpdateReplicaInfoAtGeneration(pcs, 0, "pending", false),
			1: rollingUpdateReplicaInfoAtGeneration(pcs, 1, "pending", false),
		}

		got := newPCSReplicaRecreationPlanner(pcs, pcsReplicaInfosByIndex, pcsReplicaScalingIndices{}).
			selectSourceReplicaIndices(len(pcsReplicaInfosByIndex))

		assert.Empty(t, got)
	})

	t.Run("only scale-out with some expected PCLQs or PCSGs is eligible", func(t *testing.T) {
		pcs := replicaRecreationSelectionTestPCS()
		pcsReplicaInfosByIndex := pcsReplicaInfoByIndex{
			0: {index: 0},
			1: replicaRecreationSelectionInfo(pcs, 1, "superseded", true, false),
			2: replicaRecreationSelectionInfo(pcs, 2, "superseded", false, true),
			3: replicaRecreationSelectionInfo(pcs, 3, "superseded", true, true),
			4: replicaRecreationSelectionInfo(pcs, 4, "superseded", true, true),
		}

		got := newPCSReplicaRecreationPlanner(
			pcs,
			pcsReplicaInfosByIndex,
			pcsReplicaScalingIndices{scaleOut: []int{0, 1, 2, 3}},
		).selectSourceReplicaIndices(len(pcsReplicaInfosByIndex))

		assert.Equal(t, []int{1, 2, 4}, got)
	})

	t.Run("old-generation scale-out becomes eligible after its reservation clears", func(t *testing.T) {
		pcs := replicaRecreationSelectionTestPCS()
		pcsReplicaInfosByIndex := pcsReplicaInfoByIndex{
			0: replicaRecreationSelectionInfo(pcs, 0, "superseded", true, true),
		}

		whileReserved := newPCSReplicaRecreationPlanner(
			pcs,
			pcsReplicaInfosByIndex,
			pcsReplicaScalingIndices{scaleOut: []int{0}},
		).selectSourceReplicaIndices(1)
		afterReservationClears := newPCSReplicaRecreationPlanner(pcs, pcsReplicaInfosByIndex, pcsReplicaScalingIndices{}).
			selectSourceReplicaIndices(1)

		assert.Empty(t, whileReserved)
		assert.Equal(t, []int{0}, afterReservationClears)
	})

	t.Run("current-generation scale-out does not become eligible after its reservation clears", func(t *testing.T) {
		pcs := replicaRecreationSelectionTestPCS()
		pcsReplicaInfosByIndex := pcsReplicaInfoByIndex{
			0: replicaRecreationSelectionInfo(pcs, 0, *pcs.Status.CurrentGenerationHash, true, true),
		}

		got := newPCSReplicaRecreationPlanner(pcs, pcsReplicaInfosByIndex, pcsReplicaScalingIndices{}).
			selectSourceReplicaIndices(1)

		assert.Empty(t, got)
	})

	t.Run("returns nil when no update slot remains", func(t *testing.T) {
		pcs := schedulingConfigRolloutTestPCS()

		got := newPCSReplicaRecreationPlanner(
			pcs,
			pcsReplicaInfoByIndex{0: rollingUpdateReplicaInfoAtGeneration(pcs, 0, "pending", false)},
			pcsReplicaScalingIndices{},
		).selectSourceReplicaIndices(0)

		assert.Nil(t, got)
	})
}

func TestCollectCompletedPCSReplicaUpdateIndices(t *testing.T) {
	t.Parallel()
	completedAt := metav1.NewTime(time.Unix(150, 0).UTC())
	tests := []struct {
		name        string
		progress    *grovecorev1beta1.PodCliqueSetUpdateProgress
		wantSources []int
		wantTargets []int
	}{
		{name: "nil update progress"},
		{name: "empty update progress", progress: &grovecorev1beta1.PodCliqueSetUpdateProgress{}},
		{
			name:        "completed child-resource update uses its index in both sets",
			progress:    &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0, UpdateEndedAt: &completedAt}}},
			wantSources: []int{0}, wantTargets: []int{0},
		},
		{
			name: "completed same-index replacement uses its index in both sets",
			progress: &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex: 2, UpdateEndedAt: &completedAt,
				Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 2},
			}}},
			wantSources: []int{2}, wantTargets: []int{2},
		},
		{
			name: "completed different-index replacement keeps source and target separate",
			progress: &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex: 2, UpdateEndedAt: &completedAt,
				Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
			}}},
			wantSources: []int{2}, wantTargets: []int{1},
		},
		{
			name: "unfinished child-resource and replacement entries contribute no completed indices",
			progress: &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				{ReplicaIndex: 0},
				{ReplicaIndex: 2, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1}},
				{ReplicaIndex: 3, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 4, CanceledAt: &completedAt}},
			}},
		},
		{
			name: "mixed progress includes only completed sources and targets",
			progress: &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				{ReplicaIndex: 0, UpdateEndedAt: &completedAt},
				{ReplicaIndex: 2, UpdateEndedAt: &completedAt, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1}},
				{ReplicaIndex: 3, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 3}},
			}},
			wantSources: []int{0, 2}, wantTargets: []int{0, 1},
		},
		{
			name: "multiple completed mappings deduplicate shared target identities",
			progress: &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				{ReplicaIndex: 2, UpdateEndedAt: &completedAt, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1}},
				{ReplicaIndex: 3, UpdateEndedAt: &completedAt, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1}},
			}},
			wantSources: []int{2, 3}, wantTargets: []int{1},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			before := tc.progress.DeepCopy()

			sources, targets := collectCompletedPCSReplicaUpdateIndices(tc.progress)

			assert.Equal(t, sets.New(tc.wantSources...), sources)
			assert.Equal(t, sets.New(tc.wantTargets...), targets)
			sources.Insert(95)
			assert.False(t, targets.Has(95), "source and target sets must not alias")
			targets.Insert(96)
			assert.False(t, sources.Has(96), "target mutations must not affect sources")
			assert.Equal(t, before, tc.progress, "collection must not mutate progress")
			nextSources, nextTargets := collectCompletedPCSReplicaUpdateIndices(tc.progress)
			assert.Equal(t, sets.New(tc.wantSources...), nextSources, "each call must return an owned source set")
			assert.Equal(t, sets.New(tc.wantTargets...), nextTargets, "each call must return an owned target set")
		})
	}
}

// TestRecreationProgressSeparatesScaleOutAndPendingSources covers released target allocation and
// completion bookkeeping without treating fresh scale-out resources as unfinished update sources.
func TestRecreationProgressSeparatesScaleOutAndPendingSources(t *testing.T) {
	t.Parallel()
	completedAt := metav1.NewTime(time.Unix(150, 0).UTC())
	tests := []struct {
		name                      string
		scaleOutIndices           []int
		freshScaleOutReplicaIndex int
		wantTargetPCSReplicaIndex int
	}{
		{name: "source 3 can use released index 2", freshScaleOutReplicaIndex: -1, wantTargetPCSReplicaIndex: 2},
		{name: "scale-out reservation at 2 forces source 3 to reuse index 3", scaleOutIndices: []int{2}, freshScaleOutReplicaIndex: 2, wantTargetPCSReplicaIndex: 3},
		{name: "fresh partial scale-out at an unrelated index is not an update source", scaleOutIndices: []int{4}, freshScaleOutReplicaIndex: 4, wantTargetPCSReplicaIndex: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scalingIndices := pcsReplicaScalingIndices{scaleOut: tc.scaleOutIndices}
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "uid").
				WithReplicas(int32(3+len(tc.scaleOutIndices))).
				WithStandaloneClique("frontend").
				WithScalingGroup("workers", []string{"worker"}).
				WithPodCliqueSetGenerationHash(new("current")).
				WithScalingProgress(scalingProgressFromScalingIndices(scalingIndices)).
				WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
					RequiresReplicaRecreation: true,
					CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
						{ReplicaIndex: 0, UpdateEndedAt: &completedAt, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0}},
						{ReplicaIndex: 2, UpdateEndedAt: &completedAt, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1}},
					},
				}).Build()
			replicaInfos := pcsReplicaInfoByIndex{
				0: replicaRecreationSelectionInfo(pcs, 0, "current", true, true),
				1: replicaRecreationSelectionInfo(pcs, 1, "current", true, true),
				3: replicaRecreationSelectionInfo(pcs, 3, "superseded", true, true),
			}
			if tc.freshScaleOutReplicaIndex >= 0 {
				replicaInfos[tc.freshScaleOutReplicaIndex] = replicaRecreationSelectionInfo(pcs, tc.freshScaleOutReplicaIndex, "current", true, false)
			}
			before := pcs.DeepCopy()
			hooks := newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, scalingIndices)
			assert.False(t, hooks.isUpdateComplete(), "unselected source 3 still needs updating")

			selections := hooks.selectReplicasToUpdate()

			require.Len(t, selections, 1)
			assert.Equal(t, 3, selections[0].replicaIndex)
			require.NotNil(t, selections[0].targetReplicaIndex)
			assert.Equal(t, tc.wantTargetPCSReplicaIndex, *selections[0].targetReplicaIndex)
			pcs.Status.UpdateProgress.CurrentlyUpdating = mergeReplicaUpdateSelections(pcs.Status.UpdateProgress.CurrentlyUpdating, selections, metav1.Now())
			require.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 3)
			assert.Equal(t, before.Status.UpdateProgress.CurrentlyUpdating, pcs.Status.UpdateProgress.CurrentlyUpdating[:2], "completed 0 -> 0 and 2 -> 1 must remain unchanged")
			assert.Nil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[2].UpdateEndedAt, "source 3 starts unfinished even when its target is index 2")
			hooks = newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, scalingIndices)
			assert.False(t, hooks.isUpdateComplete(), "the active source-3 replacement prevents completion")

			// Model confirmed completion of source 3 followed by loss of its target resources. Fresh
			// scale-out remains partial, but neither case may reopen the recorded update progress.
			pcs.Status.UpdateProgress.CurrentlyUpdating[2].UpdateEndedAt = &completedAt
			delete(replicaInfos, 3)
			delete(replicaInfos, tc.wantTargetPCSReplicaIndex)
			hooks = newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, scalingIndices)
			assert.True(t, hooks.isUpdateComplete(), "completed entries and unrelated S1 provisioning must not keep recreation active")
		})
	}
}

func TestCompletedReplacementIsNotReselectedAfterChildLoss(t *testing.T) {
	t.Parallel()
	completedAt := metav1.NewTime(time.Unix(150, 0).UTC())
	tests := []struct {
		name                  string
		sourcePCSReplicaIndex int32
		targetPCSReplicaIndex int
	}{
		{name: "same-index completed target loses its scaling group"},
		{name: "different-index completed target loses its scaling group", sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "uid").
				WithStandaloneClique("frontend").
				WithScalingGroup("workers", []string{"worker"}).
				WithPodCliqueSetGenerationHash(new("current")).
				WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
					RequiresReplicaRecreation: true,
					CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
						ReplicaIndex: tc.sourcePCSReplicaIndex, UpdateEndedAt: &completedAt,
						Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: int32(tc.targetPCSReplicaIndex)},
					}},
				}).Build()
			replicaInfos := pcsReplicaInfoByIndex{
				tc.targetPCSReplicaIndex: replicaRecreationSelectionInfo(pcs, tc.targetPCSReplicaIndex, "current", true, false),
				4:                        replicaRecreationSelectionInfo(pcs, 4, "superseded", true, true),
			}
			before := pcs.DeepCopy()
			planner := newPCSReplicaRecreationPlanner(pcs, replicaInfos, pcsReplicaScalingIndices{})

			assert.Equal(t, []int{4}, planner.selectSourceReplicaIndices(1), "select only the unfinished replica, not the completed target with a missing child")
			assert.Equal(t, before, pcs, "selection must preserve completed progress and timestamps")
		})
	}
}

func TestPCSReplicaRecreationPlannerAllocateReplacementTargets(t *testing.T) {
	tests := []struct {
		name                            string
		existingPCSReplicaIndices       sets.Set[int]
		scalingProgress                 *grovecorev1beta1.PodCliqueSetScalingProgress
		currentlyUpdating               []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress
		selectedSourcePCSReplicaIndices []int
		want                            []pcsReplicaReplacement
	}{
		{
			name:                      "no selected sources",
			existingPCSReplicaIndices: sets.New(0, 1),
		},
		{
			name:                            "dense indices reuse source index",
			existingPCSReplicaIndices:       sets.New(0, 1),
			selectedSourcePCSReplicaIndices: []int{1},
			want: []pcsReplicaReplacement{
				{sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1},
			},
		},
		{
			name:                            "sparse indices fill lowest hole",
			existingPCSReplicaIndices:       sets.New(0, 2),
			selectedSourcePCSReplicaIndices: []int{2},
			want: []pcsReplicaReplacement{
				{sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1},
			},
		},
		{
			name:                            "multiple existing gaps fill the lowest gap",
			existingPCSReplicaIndices:       sets.New(0, 3, 5),
			selectedSourcePCSReplicaIndices: []int{5},
			want: []pcsReplicaReplacement{
				{sourcePCSReplicaIndex: 5, targetPCSReplicaIndex: 1},
			},
		},
		{
			name:                      "PCS scale-out reservation blocks hole",
			existingPCSReplicaIndices: sets.New(0, 2),
			scalingProgress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleOutReplicaIndices: []int32{1},
			},
			selectedSourcePCSReplicaIndices: []int{2},
			want: []pcsReplicaReplacement{
				{sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 2},
			},
		},
		{
			name:                      "PCS scale-in reservation blocks hole",
			existingPCSReplicaIndices: sets.New(0, 1, 3),
			scalingProgress: &grovecorev1beta1.PodCliqueSetScalingProgress{
				ScaleInReplicaIndices: []int32{2},
			},
			selectedSourcePCSReplicaIndices: []int{3},
			want: []pcsReplicaReplacement{
				{sourcePCSReplicaIndex: 3, targetPCSReplicaIndex: 3},
			},
		},
		{
			name:                      "active replacement source and target block allocation",
			existingPCSReplicaIndices: sets.New(0, 3, 5),
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				{ReplicaIndex: 3, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1}},
			},
			selectedSourcePCSReplicaIndices: []int{5},
			want: []pcsReplicaReplacement{
				{sourcePCSReplicaIndex: 5, targetPCSReplicaIndex: 2},
			},
		},
		{
			name:                      "canceled replacement source and target block allocation",
			existingPCSReplicaIndices: sets.New(0, 4),
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				{
					ReplicaIndex: 3,
					Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
						TargetReplicaIndex: 1,
						CanceledAt:         new(metav1.Now()),
					},
				},
			},
			selectedSourcePCSReplicaIndices: []int{4},
			want: []pcsReplicaReplacement{
				{sourcePCSReplicaIndex: 4, targetPCSReplicaIndex: 2},
			},
		},
		{
			name:                      "absent active retired sources block replacement target allocation",
			existingPCSReplicaIndices: sets.New(0, 3, 5),
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex: 3,
				Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
					TargetReplicaIndex: 2, RetiredSourceReplicaIndices: []int32{1},
				},
			}},
			selectedSourcePCSReplicaIndices: []int{5},
			want: []pcsReplicaReplacement{{
				sourcePCSReplicaIndex: 5, targetPCSReplicaIndex: 4,
			}},
		},
		{
			name:                      "absent canceled retired sources block replacement target allocation",
			existingPCSReplicaIndices: sets.New(0, 5),
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex: 3,
				Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
					TargetReplicaIndex: 2, RetiredSourceReplicaIndices: []int32{1},
					CanceledAt: new(metav1.NewTime(time.Unix(150, 0).UTC())),
				},
			}},
			selectedSourcePCSReplicaIndices: []int{5},
			want: []pcsReplicaReplacement{{
				sourcePCSReplicaIndex: 5, targetPCSReplicaIndex: 4,
			}},
		},
		{
			name:                      "completed replacement does not block allocation",
			existingPCSReplicaIndices: sets.New(0, 2),
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				{ReplicaIndex: 5, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1}, UpdateEndedAt: new(metav1.Now())},
			},
			selectedSourcePCSReplicaIndices: []int{2},
			want: []pcsReplicaReplacement{
				{sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1},
			},
		},
		{
			name:                            "multiple sources are sorted and cannot use each other as targets",
			existingPCSReplicaIndices:       sets.New(0, 2, 4),
			selectedSourcePCSReplicaIndices: []int{4, 2},
			want: []pcsReplicaReplacement{
				{sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1},
				{sourcePCSReplicaIndex: 4, targetPCSReplicaIndex: 3},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			updateProgress := &grovecorev1beta1.PodCliqueSetUpdateProgress{
				RequiresReplicaRecreation: true,
				CurrentlyUpdating:         tc.currentlyUpdating,
			}
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "uid").
				WithScalingProgress(tc.scalingProgress.DeepCopy()).
				WithUpdateProgress(updateProgress.DeepCopy()).Build()
			replicaInfosByIndex := make(pcsReplicaInfoByIndex, tc.existingPCSReplicaIndices.Len())
			for pcsReplicaIndex := range tc.existingPCSReplicaIndices {
				replicaInfosByIndex[pcsReplicaIndex] = pcsReplicaInfo{index: pcsReplicaIndex}
			}
			existingPCSReplicaIndicesBefore := tc.existingPCSReplicaIndices.Clone()
			selectedSourcePCSReplicaIndicesBefore := slices.Clone(tc.selectedSourcePCSReplicaIndices)
			before := pcs.DeepCopy()
			planner := newPCSReplicaRecreationPlanner(pcs, replicaInfosByIndex, replicaScalingIndicesFromProgress(pcs.Status.ScalingProgress))

			got := planner.allocateReplacementTargets(tc.selectedSourcePCSReplicaIndices)

			assert.Equal(t, tc.want, got)
			assert.Equal(t, existingPCSReplicaIndicesBefore, tc.existingPCSReplicaIndices, "allocateReplacementTargets mutated existing replica indices")
			assert.Equal(t, selectedSourcePCSReplicaIndicesBefore, tc.selectedSourcePCSReplicaIndices, "allocateReplacementTargets mutated selected source replica indices")
			assert.Equal(t, before, pcs, "target allocation must not mutate replacement progress")
		})
	}
}

func TestReconcileCurrentlyUpdatingReplicasReportsCompletedReplacements(t *testing.T) {
	pcs := rollingUpdateTestPCS()
	pcs.Status.UpdateProgress.RequiresReplicaRecreation = true
	canceledAt := metav1.NewTime(time.Unix(150, 0).UTC())
	endedAt := metav1.NewTime(time.Unix(200, 0).UTC())
	barrierCompletedAt := metav1.NewTime(time.Unix(100, 0).UTC())

	differentIndex := func() grovecorev1beta1.PodCliqueSetReplicaUpdateProgress {
		return grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex: 0,
			Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 2},
		}
	}
	buildConfirmedDifferentIndexReplacement := func() grovecorev1beta1.PodCliqueSetReplicaUpdateProgress {
		progress := differentIndex()
		progress.Replacement.SourceCleanupCompletedAt = &barrierCompletedAt
		return progress
	}
	sameIndexWithBarrier := func() grovecorev1beta1.PodCliqueSetReplicaUpdateProgress {
		return grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex: 1,
			Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1, SourceCleanupCompletedAt: &barrierCompletedAt},
		}
	}
	convergedUnavailableInfo := func(replicaIndex int) pcsReplicaInfo {
		info := rollingUpdateReplicaInfo(pcs, replicaIndex, true)
		info.standalonePCLQs[0].DeletionTimestamp = &canceledAt
		return info
	}

	tests := []struct {
		name              string
		currentlyUpdating []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress
		replicaInfos      pcsReplicaInfoByIndex
		want              []int
	}{
		{
			name:              "completes a different-index replacement after source cleanup confirmation and target convergence",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{buildConfirmedDifferentIndexReplacement()},
			replicaInfos:      pcsReplicaInfoByIndex{2: rollingUpdateReplicaInfo(pcs, 2, true)},
			want:              []int{0},
		},
		{
			name:              "source snapshot absence cannot replace participant-confirmed different-index cleanup",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{differentIndex()},
			replicaInfos:      pcsReplicaInfoByIndex{2: rollingUpdateReplicaInfo(pcs, 2, true)},
		},
		{
			name:              "confirmed different-index cleanup remains authoritative over a stale source snapshot",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{buildConfirmedDifferentIndexReplacement()},
			replicaInfos:      pcsReplicaInfoByIndex{0: rollingUpdateReplicaInfo(pcs, 0, true), 2: rollingUpdateReplicaInfo(pcs, 2, true)},
			want:              []int{0},
		},
		{
			name:              "keeps a different-index replacement active while its source resources remain",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{differentIndex()},
			replicaInfos:      pcsReplicaInfoByIndex{0: rollingUpdateReplicaInfo(pcs, 0, true), 2: rollingUpdateReplicaInfo(pcs, 2, true)},
		},
		{
			name:              "keeps a different-index replacement active while its target has not converged",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{buildConfirmedDifferentIndexReplacement()},
			replicaInfos:      pcsReplicaInfoByIndex{2: rollingUpdateReplicaInfo(pcs, 2, false)},
		},
		{
			name:              "keeps a different-index replacement active while its target is converged but unavailable",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{buildConfirmedDifferentIndexReplacement()},
			replicaInfos:      pcsReplicaInfoByIndex{2: convergedUnavailableInfo(2)},
		},
		{
			name:              "completes a same-index replacement once its barrier is set and its target converged",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{sameIndexWithBarrier()},
			replicaInfos:      pcsReplicaInfoByIndex{1: rollingUpdateReplicaInfo(pcs, 1, true)},
			want:              []int{1},
		},
		{
			name: "keeps a same-index replacement active while its cleanup barrier is unset",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex: 1,
				Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
			}},
			replicaInfos: pcsReplicaInfoByIndex{1: rollingUpdateReplicaInfo(pcs, 1, true)},
		},
		{
			name:              "keeps a same-index replacement active while its target has not converged",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{sameIndexWithBarrier()},
			replicaInfos:      pcsReplicaInfoByIndex{1: rollingUpdateReplicaInfo(pcs, 1, false)},
		},
		{
			name: "does not report a canceled replacement as completed",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex: 0,
				Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 2, CanceledAt: &canceledAt},
			}},
			replicaInfos: pcsReplicaInfoByIndex{2: rollingUpdateReplicaInfo(pcs, 2, true)},
		},
		{
			name: "does not re-report an already completed replacement",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex:  0,
				Replacement:   &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 2},
				UpdateEndedAt: &endedAt,
			}},
			replicaInfos: pcsReplicaInfoByIndex{2: rollingUpdateReplicaInfo(pcs, 2, true)},
		},
		{
			name:              "reports multiple completed replacements together",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{buildConfirmedDifferentIndexReplacement(), sameIndexWithBarrier()},
			replicaInfos:      pcsReplicaInfoByIndex{1: rollingUpdateReplicaInfo(pcs, 1, true), 2: rollingUpdateReplicaInfo(pcs, 2, true)},
			want:              []int{0, 1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hooks := newPCSReplicaRecreationStrategyHooks(pcs, tc.replicaInfos, pcsReplicaScalingIndices{})

			result, err := hooks.reconcileCurrentlyUpdatingReplicas(tc.currentlyUpdating)

			assert.NoError(t, err)
			assert.Equal(t, tc.want, result.completedPCSReplicaIndices)
			assert.Empty(t, result.pcsReplicaUpdateIndicesToRemove)
		})
	}
}

// TestReconcileCurrentlyUpdatingReplicasRetiredSourceCleanup covers replacement-owned retirement barriers.
// Resource absence alone does not confirm cleanup, and unrelated PCS scale-in must not block a replacement.
func TestReconcileCurrentlyUpdatingReplicasRetiredSourceCleanup(t *testing.T) {
	t.Parallel()
	cleanupCompletedAt := metav1.NewTime(time.Unix(100, 0).UTC())
	tests := []struct {
		name                                   string
		sourcePCSReplicaIndex                  int32
		targetPCSReplicaIndex                  int32
		retiredSourcePCSReplicaIndices         []int32
		observedRetiredSourcePCSReplicaIndices []int
		ordinaryScaleInPCSReplicaIndices       []int32
		targetMissing                          bool
		wantComplete                           bool
	}{
		{name: "same-index replacement waits for an observed retired source", sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, retiredSourcePCSReplicaIndices: []int32{2}, observedRetiredSourcePCSReplicaIndices: []int{2}},
		{name: "same-index replacement waits for confirmation of an absent retired source", sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, retiredSourcePCSReplicaIndices: []int32{2}},
		{name: "different-index replacement waits for an observed retired source", sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, retiredSourcePCSReplicaIndices: []int32{3}, observedRetiredSourcePCSReplicaIndices: []int{3}},
		{name: "different-index replacement waits for confirmation of an absent retired source", sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, retiredSourcePCSReplicaIndices: []int32{3}},
		{name: "multiple retired sources remain pending despite partial resource disappearance", sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, retiredSourcePCSReplicaIndices: []int32{2, 3}, observedRetiredSourcePCSReplicaIndices: []int{3}},
		{name: "same-index replacement waits for retired index zero", sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, retiredSourcePCSReplicaIndices: []int32{0}},
		{name: "different-index replacement waits for multiple retired sources including zero", sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, retiredSourcePCSReplicaIndices: []int32{0, 3}, observedRetiredSourcePCSReplicaIndices: []int{0, 3}},
		{name: "same-index replacement completes with a nil retired list", sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, wantComplete: true},
		{name: "same-index replacement completes with an empty retired list", sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, retiredSourcePCSReplicaIndices: []int32{}, wantComplete: true},
		{name: "different-index replacement completes with a nil retired list", sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, wantComplete: true},
		{name: "different-index replacement completes with an empty retired list", sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, retiredSourcePCSReplicaIndices: []int32{}, wantComplete: true},
		{name: "unrelated ordinary scale-in does not block same-index completion", sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, ordinaryScaleInPCSReplicaIndices: []int32{9}, wantComplete: true},
		{name: "unrelated ordinary scale-in does not block different-index completion", sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, ordinaryScaleInPCSReplicaIndices: []int32{9}, wantComplete: true},
		{name: "same-index completion still requires a target after retirement finishes", sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, targetMissing: true},
		{name: "different-index completion still requires a target after retirement finishes", sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, targetMissing: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			replacement := &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex:          tc.targetPCSReplicaIndex,
				RetiredSourceReplicaIndices: slices.Clone(tc.retiredSourcePCSReplicaIndices),
				SourceCleanupCompletedAt:    &cleanupCompletedAt,
			}
			var scalingProgress *grovecorev1beta1.PodCliqueSetScalingProgress
			if len(tc.ordinaryScaleInPCSReplicaIndices) > 0 {
				scalingProgress = &grovecorev1beta1.PodCliqueSetScalingProgress{
					ScaleInReplicaIndices: slices.Clone(tc.ordinaryScaleInPCSReplicaIndices),
				}
			}
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "test-uid").
				WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithMinAvailable(1).Build()).
				WithPodCliqueSetGenerationHash(new("generation")).
				WithScalingProgress(scalingProgress).
				WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
					RequiresReplicaRecreation: true,
					UpdateStartedAt:           cleanupCompletedAt,
					CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
						ReplicaIndex: tc.sourcePCSReplicaIndex, Replacement: replacement,
					}},
				}).Build()
			replicaInfos := make(pcsReplicaInfoByIndex)
			if !tc.targetMissing {
				targetPCSReplicaIndex := int(tc.targetPCSReplicaIndex)
				replicaInfos[targetPCSReplicaIndex] = rollingUpdateReplicaInfo(pcs, targetPCSReplicaIndex, true)
			}
			for _, pcsReplicaIndex := range tc.observedRetiredSourcePCSReplicaIndices {
				replicaInfos[pcsReplicaIndex] = rollingUpdateReplicaInfo(pcs, pcsReplicaIndex, false)
			}
			for _, pcsReplicaIndex := range tc.ordinaryScaleInPCSReplicaIndices {
				replicaInfos[int(pcsReplicaIndex)] = rollingUpdateReplicaInfo(pcs, int(pcsReplicaIndex), true)
			}
			hooks := newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, replicaScalingIndicesFromProgress(pcs.Status.ScalingProgress))
			before := pcs.DeepCopy()

			result, err := hooks.reconcileCurrentlyUpdatingReplicas(pcs.Status.UpdateProgress.CurrentlyUpdating)

			require.NoError(t, err)
			var wantCompletedPCSReplicaIndices []int
			if tc.wantComplete {
				wantCompletedPCSReplicaIndices = []int{int(tc.sourcePCSReplicaIndex)}
			}
			assert.Equal(t, wantCompletedPCSReplicaIndices, result.completedPCSReplicaIndices)
			assert.Empty(t, result.pcsReplicaUpdateIndicesToRemove)
			assert.Equal(t, before, pcs, "completion reporting must not mutate replacement progress")
		})
	}
}

func TestPCSReplicaRecreationIsUpdateComplete(t *testing.T) {
	canceledAt := metav1.Now()
	newRecreatingPCS := func() *grovecorev1beta1.PodCliqueSet {
		pcs := rollingUpdateTestPCS()
		pcs.Status.UpdateProgress.RequiresReplicaRecreation = true
		return pcs
	}

	t.Run("complete when no replacement remains and no source needs recreation", func(t *testing.T) {
		pcs := newRecreatingPCS()
		info := rollingUpdateReplicaInfo(pcs, 0, true)
		info.standalonePCLQs[0].Labels[apicommon.LabelPodCliqueSetGenerationHash] = *pcs.Status.CurrentGenerationHash
		replicaInfos := pcsReplicaInfoByIndex{0: info}
		hooks := newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, pcsReplicaScalingIndices{})

		assert.True(t, hooks.isUpdateComplete())
	})

	t.Run("incomplete while an active replacement remains", func(t *testing.T) {
		pcs := newRecreatingPCS()
		pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			{ReplicaIndex: 0, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 2}},
		}
		replicaInfos := pcsReplicaInfoByIndex{0: rollingUpdateReplicaInfo(pcs, 0, true)}
		hooks := newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, pcsReplicaScalingIndices{})

		assert.False(t, hooks.isUpdateComplete())
	})

	t.Run("incomplete while a canceled replacement awaits cleanup", func(t *testing.T) {
		pcs := newRecreatingPCS()
		pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			{ReplicaIndex: 0, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 2, CanceledAt: &canceledAt}},
		}
		replicaInfos := pcsReplicaInfoByIndex{0: rollingUpdateReplicaInfo(pcs, 0, true)}
		hooks := newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, pcsReplicaScalingIndices{})

		assert.False(t, hooks.isUpdateComplete())
	})

	t.Run("ordinary scale-out with no update entry does not block completion", func(t *testing.T) {
		pcs := newRecreatingPCS()
		hooks := newPCSReplicaRecreationStrategyHooks(pcs, pcsReplicaInfoByIndex{}, pcsReplicaScalingIndices{scaleOut: []int{0}})

		assert.True(t, hooks.isUpdateComplete())
	})

	t.Run("incomplete while a recorded child-resource update has not converged", func(t *testing.T) {
		pcs := newRecreatingPCS()
		pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}}
		replicaInfos := pcsReplicaInfoByIndex{0: rollingUpdateReplicaInfo(pcs, 0, false)}
		hooks := newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, pcsReplicaScalingIndices{})

		assert.False(t, hooks.isUpdateComplete())
	})

	t.Run("incomplete while a recorded child-resource update is converged but unavailable", func(t *testing.T) {
		pcs := newRecreatingPCS()
		pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}}
		info := rollingUpdateReplicaInfo(pcs, 0, true)
		info.standalonePCLQs[0].DeletionTimestamp = &canceledAt
		replicaInfos := pcsReplicaInfoByIndex{0: info}
		hooks := newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, pcsReplicaScalingIndices{})

		assert.False(t, hooks.isUpdateComplete())
	})
}

func replicaRecreationSelectionTestPCS() *grovecorev1beta1.PodCliqueSet {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "test-uid").
		WithStandaloneClique("frontend").
		WithScalingGroup("workers", []string{"worker"}).
		WithPodCliqueSetGenerationHash(new("current")).
		Build()
	pcs.Spec.Template.Cliques[0].Spec.MinAvailable = new(int32(1))
	pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
		RequiresReplicaRecreation:          true,
		PreviousPodCliqueSetGenerationHash: new("superseded"),
	}
	return pcs
}

func replicaRecreationSelectionInfo(
	pcs *grovecorev1beta1.PodCliqueSet,
	pcsReplicaIndex int,
	pcsGenerationHash string,
	includePCLQ bool,
	includePCSG bool) pcsReplicaInfo {
	pcsReplicaInfoForSelection := pcsReplicaInfo{index: pcsReplicaIndex}
	if includePCLQ {
		pclqName := componentutils.GetPodCliqueFQNsForPCSReplicaNotInPCSG(pcs, pcsReplicaIndex)[0]
		pclq := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, "frontend", pcs.Namespace, int32(pcsReplicaIndex)).
			WithMinAvailable(1).
			Build()
		pclq.Name = pclqName
		pclq.Labels[apicommon.LabelPodCliqueSetGenerationHash] = pcsGenerationHash
		pclq.Status.CurrentPodCliqueSetGenerationHash = new(pcsGenerationHash)
		pclq.Status.ReadyReplicas = 1
		pcsReplicaInfoForSelection.standalonePCLQs = []grovecorev1beta1.PodClique{*pclq}
	}
	if includePCSG {
		pcsgName := componentutils.GetPCSGFQNsForPCSReplica(pcs, pcsReplicaIndex)[0]
		pcsg := testutils.NewPodCliqueScalingGroupBuilder(pcsgName, pcs.Namespace, pcs.Name, pcsReplicaIndex).Build()
		pcsg.Labels[apicommon.LabelPodCliqueSetGenerationHash] = pcsGenerationHash
		pcsg.Status.CurrentPodCliqueSetGenerationHash = new(pcsGenerationHash)
		pcsg.Status.AvailableReplicas = 1
		pcsReplicaInfoForSelection.pcsgs = []grovecorev1beta1.PodCliqueScalingGroup{*pcsg}
	}
	return pcsReplicaInfoForSelection
}

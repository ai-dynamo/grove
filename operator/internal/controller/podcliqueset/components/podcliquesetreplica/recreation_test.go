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
	"context"
	"slices"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	pcsexpectations "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/expectations"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

func TestRecreationCompletionWaitsForMaterializedScaleOutStatus(t *testing.T) {
	pcs := replicaRecreationSelectionTestPCS()
	pcs.Spec.Replicas = 1
	info := replicaRecreationSelectionInfo(pcs, 0, "current", true, true)
	info.standalonePCLQs[0].Status = grovecorev1beta1.PodCliqueStatus{}
	info.pcsgs[0].Status = grovecorev1beta1.PodCliqueScalingGroupStatus{}
	assert.True(t, info.hasMaterializedPCSGeneration(pcs, "current"))
	hooks := newPCSReplicaRecreationStrategyHooks(pcs, pcsReplicaInfoByIndex{0: info}, pcsReplicaScalingIndices{})
	assert.False(t, hooks.isUpdateComplete(), "marker cannot replace status convergence or availability")
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

	t.Run("completed replacement no longer reserves its source or target", func(t *testing.T) {
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

		assert.Equal(t, []int{0, 1}, got)
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
	canceledAt := metav1.Now()
	endedAt := metav1.Now()
	barrierCompletedAt := metav1.Now()

	differentIndex := func() grovecorev1beta1.PodCliqueSetReplicaUpdateProgress {
		return grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex: 0,
			Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 2},
		}
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
			name:              "completes a different-index replacement once its source is gone and its target converged",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{differentIndex()},
			replicaInfos:      pcsReplicaInfoByIndex{2: rollingUpdateReplicaInfo(pcs, 2, true)},
			want:              []int{0},
		},
		{
			name:              "keeps a different-index replacement active while its source resources remain",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{differentIndex()},
			replicaInfos:      pcsReplicaInfoByIndex{0: rollingUpdateReplicaInfo(pcs, 0, true), 2: rollingUpdateReplicaInfo(pcs, 2, true)},
		},
		{
			name:              "keeps a different-index replacement active while its target has not converged",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{differentIndex()},
			replicaInfos:      pcsReplicaInfoByIndex{2: rollingUpdateReplicaInfo(pcs, 2, false)},
		},
		{
			name:              "keeps a different-index replacement active while its target is converged but unavailable",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{differentIndex()},
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
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{differentIndex(), sameIndexWithBarrier()},
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
			}
			if tc.sourcePCSReplicaIndex == tc.targetPCSReplicaIndex {
				replacement.SourceCleanupCompletedAt = &cleanupCompletedAt
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

// TestOrchestrateWaitsForRetiredSourceCleanup verifies that a ready target cannot end an update while retired
// cleanup remains pending. Completion and the applied scheduling hash advance only after every retirement clears.
func TestOrchestrateWaitsForRetiredSourceCleanup(t *testing.T) {
	t.Parallel()
	cleanupCompletedAt := metav1.NewTime(time.Unix(100, 0).UTC())
	tests := []struct {
		name                           string
		sourcePCSReplicaIndex          int32
		targetPCSReplicaIndex          int32
		retiredSourcePCSReplicaIndices []int32
	}{
		{name: "same-index replacement waits for one retired source", sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, retiredSourcePCSReplicaIndices: []int32{2}},
		{name: "same-index replacement waits for every retired source including zero", sourcePCSReplicaIndex: 1, targetPCSReplicaIndex: 1, retiredSourcePCSReplicaIndices: []int32{0, 2}},
		{name: "different-index replacement waits for one retired source", sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, retiredSourcePCSReplicaIndices: []int32{3}},
		{name: "different-index replacement waits for every retired source including zero", sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1, retiredSourcePCSReplicaIndices: []int32{0, 3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			replacement := &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex:          tc.targetPCSReplicaIndex,
				RetiredSourceReplicaIndices: slices.Clone(tc.retiredSourcePCSReplicaIndices),
			}
			if tc.sourcePCSReplicaIndex == tc.targetPCSReplicaIndex {
				replacement.SourceCleanupCompletedAt = &cleanupCompletedAt
			}
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "test-uid").
				WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithMinAvailable(1).Build()).
				WithPodCliqueSetGenerationHash(new("generation")).
				WithAppliedSchedulingConfigurationHash(new("superseded")).
				WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
					RequiresReplicaRecreation:         true,
					UpdateStartedAt:                   cleanupCompletedAt,
					TargetSchedulingConfigurationHash: "target-scheduling",
					CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
						ReplicaIndex: tc.sourcePCSReplicaIndex, Replacement: replacement,
					}},
				}).Build()
			fakeClient := testutils.NewTestClientBuilder().WithObjects(pcs).WithStatusSubresource(pcs).Build()
			replicaInfos := pcsReplicaInfoByIndex{int(tc.targetPCSReplicaIndex): rollingUpdateReplicaInfo(pcs, int(tc.targetPCSReplicaIndex), true)}
			for len(pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.RetiredSourceReplicaIndices) > 0 {
				before := pcs.DeepCopy()
				hooks := newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, pcsReplicaScalingIndices{})
				orchestrator := newRollingUpdateOrchestrator(fakeClient.Status(), &pcsexpectations.CurrentlyUpdating{}, pcs, hooks)

				inProgress, err := orchestrator.orchestrate(t.Context(), pcs.DeepCopy(), nil)

				require.NoError(t, err)
				assert.True(t, inProgress)
				require.NotNil(t, pcs.Status.UpdateProgress)
				require.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1)
				assert.Nil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].UpdateEndedAt)
				assert.Nil(t, pcs.Status.UpdateProgress.UpdateEndedAt)
				assert.Equal(t, before.Status, pcs.Status, "pending retirement must preserve update progress and the applied hash")
				storedPCS := &grovecorev1beta1.PodCliqueSet{}
				require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), storedPCS))
				assert.True(t, apiequality.Semantic.DeepEqual(before.Status, storedPCS.Status), "pending retirement must leave persisted status unchanged")

				// Model the persisted removal of one participant-confirmed retired source before the next reconcile.
				pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.RetiredSourceReplicaIndices =
					pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.RetiredSourceReplicaIndices[1:]
				require.NoError(t, fakeClient.Status().Patch(t.Context(), pcs, client.MergeFrom(before)))
				require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), pcs))
			}
			hooks := newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, pcsReplicaScalingIndices{})
			orchestrator := newRollingUpdateOrchestrator(fakeClient.Status(), &pcsexpectations.CurrentlyUpdating{}, pcs, hooks)

			inProgress, err := orchestrator.orchestrate(t.Context(), pcs.DeepCopy(), nil)

			require.NoError(t, err)
			assert.False(t, inProgress)
			storedPCS := &grovecorev1beta1.PodCliqueSet{}
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), storedPCS))
			require.NotNil(t, storedPCS.Status.UpdateProgress)
			assert.NotNil(t, storedPCS.Status.UpdateProgress.UpdateEndedAt)
			assert.Empty(t, storedPCS.Status.UpdateProgress.CurrentlyUpdating)
			require.NotNil(t, storedPCS.Status.AppliedSchedulingConfigurationHash)
			assert.Equal(t, "target-scheduling", *storedPCS.Status.AppliedSchedulingConfigurationHash)
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

	t.Run("complete when no replacement remains and every expected replica converged and is available", func(t *testing.T) {
		pcs := newRecreatingPCS()
		replicaInfos := pcsReplicaInfoByIndex{0: rollingUpdateReplicaInfo(pcs, 0, true)}
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

	t.Run("incomplete while an expected replica is missing", func(t *testing.T) {
		pcs := newRecreatingPCS()
		hooks := newPCSReplicaRecreationStrategyHooks(pcs, pcsReplicaInfoByIndex{}, pcsReplicaScalingIndices{scaleOut: []int{0}})

		assert.False(t, hooks.isUpdateComplete())
	})

	t.Run("incomplete while an expected replica has not converged", func(t *testing.T) {
		pcs := newRecreatingPCS()
		replicaInfos := pcsReplicaInfoByIndex{0: rollingUpdateReplicaInfo(pcs, 0, false)}
		hooks := newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, pcsReplicaScalingIndices{})

		assert.False(t, hooks.isUpdateComplete())
	})

	t.Run("incomplete while an expected replica is converged but unavailable", func(t *testing.T) {
		pcs := newRecreatingPCS()
		info := rollingUpdateReplicaInfo(pcs, 0, true)
		info.standalonePCLQs[0].DeletionTimestamp = &canceledAt
		replicaInfos := pcsReplicaInfoByIndex{0: info}
		hooks := newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, pcsReplicaScalingIndices{})

		assert.False(t, hooks.isUpdateComplete())
	})
}

// TestOrchestrateCancelsReadyReplacementBeforeCompletion verifies that cancellation takes precedence over
// completion when a replacement target is already ready and has no pending retired-source cleanup.
func TestOrchestrateCancelsReadyReplacementBeforeCompletion(t *testing.T) {
	t.Parallel()
	cleanupCompletedAt := metav1.NewTime(time.Unix(100, 0).UTC())
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "test-uid").
		WithReplicas(0).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithMinAvailable(1).Build()).
		WithPodCliqueSetGenerationHash(new("generation")).
		WithAppliedSchedulingConfigurationHash(new("superseded")).
		WithScalingProgress(&grovecorev1beta1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{1}}).
		WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
			RequiresReplicaRecreation:         true,
			UpdateStartedAt:                   cleanupCompletedAt,
			TargetSchedulingConfigurationHash: "target-scheduling",
			CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex: 1,
				Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
					TargetReplicaIndex: 1, SourceCleanupCompletedAt: &cleanupCompletedAt,
				},
			}},
		}).Build()
	fakeClient := testutils.NewTestClientBuilder().WithObjects(pcs).WithStatusSubresource(pcs).Build()
	replicaInfos := pcsReplicaInfoByIndex{1: rollingUpdateReplicaInfo(pcs, 1, true)}
	hooks := newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, replicaScalingIndicesFromProgress(pcs.Status.ScalingProgress))
	require.True(t, hooks.isReplacementComplete(pcs.Status.UpdateProgress.CurrentlyUpdating[0]), "without cancellation this replacement would complete")
	orchestrator := newRollingUpdateOrchestrator(fakeClient.Status(), &pcsexpectations.CurrentlyUpdating{}, pcs, hooks)

	inProgress, err := orchestrator.orchestrate(t.Context(), pcs.DeepCopy(), []int{1})

	require.NoError(t, err)
	assert.True(t, inProgress)
	storedPCS := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(pcs), storedPCS))
	require.NotNil(t, storedPCS.Status.UpdateProgress)
	require.Len(t, storedPCS.Status.UpdateProgress.CurrentlyUpdating, 1)
	progress := storedPCS.Status.UpdateProgress.CurrentlyUpdating[0]
	assert.Equal(t, int32(1), progress.ReplicaIndex)
	require.NotNil(t, progress.Replacement)
	assert.Equal(t, int32(1), progress.Replacement.TargetReplicaIndex)
	assert.NotNil(t, progress.Replacement.CanceledAt, "the ready replacement must be canceled rather than completed")
	assert.Nil(t, progress.UpdateEndedAt)
	assert.Nil(t, storedPCS.Status.UpdateProgress.UpdateEndedAt)
	require.NotNil(t, storedPCS.Status.AppliedSchedulingConfigurationHash)
	assert.Equal(t, "superseded", *storedPCS.Status.AppliedSchedulingConfigurationHash)
}

func TestOrchestrateAdvancesAppliedHashOnReplacementCompletion(t *testing.T) {
	pcs := rollingUpdateTestPCS()
	pcs.Status.AppliedSchedulingConfigurationHash = new("superseded")
	pcs.Status.UpdateProgress.RequiresReplicaRecreation = true
	pcs.Status.UpdateProgress.TargetSchedulingConfigurationHash = "target-scheduling"
	pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
		{ReplicaIndex: 0, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0, SourceCleanupCompletedAt: new(metav1.Now())}},
	}
	replicaInfos := pcsReplicaInfoByIndex{0: rollingUpdateReplicaInfo(pcs, 0, true)}
	fakeClient := testutils.NewTestClientBuilder().WithObjects(pcs).WithStatusSubresource(pcs).Build()
	hooks := newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, pcsReplicaScalingIndices{})
	orchestrator := newRollingUpdateOrchestrator(fakeClient.Status(), &pcsexpectations.CurrentlyUpdating{}, pcs, hooks)

	_, err := orchestrator.orchestrate(context.Background(), pcs.DeepCopy(), nil)

	assert.NoError(t, err)
	require.NotNil(t, pcs.Status.AppliedSchedulingConfigurationHash)
	assert.Equal(t, "target-scheduling", *pcs.Status.AppliedSchedulingConfigurationHash)
	require.NotNil(t, pcs.Status.UpdateProgress.UpdateEndedAt)
	assert.Nil(t, pcs.Status.UpdateProgress.CurrentlyUpdating)
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

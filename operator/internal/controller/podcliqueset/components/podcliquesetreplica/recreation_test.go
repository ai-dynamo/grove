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

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestGetActivePCSReplicaReplacements(t *testing.T) {
	completedAt := metav1.Now()
	tests := []struct {
		name string
		pcs  *grovecorev1beta1.PodCliqueSet
		want []pcsReplicaReplacement
	}{
		{
			name: "no update progress",
			pcs:  &grovecorev1beta1.PodCliqueSet{},
		},
		{
			name: "replica recreation not required ignores replacement indices",
			pcs: &grovecorev1beta1.PodCliqueSet{Status: grovecorev1beta1.PodCliqueSetStatus{
				UpdateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
					CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
						{ReplicaIndex: 2, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1}},
					},
				},
			}},
		},
		{
			name: "one active replacement",
			pcs: &grovecorev1beta1.PodCliqueSet{Status: grovecorev1beta1.PodCliqueSetStatus{
				UpdateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
					RequiresReplicaRecreation: true,
					CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
						{ReplicaIndex: 2, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1}},
					},
				},
			}},
			want: []pcsReplicaReplacement{
				{sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1},
			},
		},
		{
			// Multiple active replacements will be possible when MaxUnavailable-based concurrency is added by #888.
			name: "multiple active replacements are sorted and incomplete entries are filtered",
			pcs: &grovecorev1beta1.PodCliqueSet{Status: grovecorev1beta1.PodCliqueSetStatus{
				UpdateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
					RequiresReplicaRecreation: true,
					CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
						{ReplicaIndex: 5, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 3}},
						{ReplicaIndex: 1},
						{ReplicaIndex: 4, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 2}, UpdateEndedAt: &completedAt},
						{ReplicaIndex: 2, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1}},
					},
				},
			}},
			want: []pcsReplicaReplacement{
				{sourcePCSReplicaIndex: 2, targetPCSReplicaIndex: 1},
				{sourcePCSReplicaIndex: 5, targetPCSReplicaIndex: 3},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, getActivePCSReplicaReplacements(tt.pcs))
		})
	}
}

func TestSelectPCSReplicaRecreationSourceIndices(t *testing.T) {
	t.Run("selects replicas in rollout priority order", func(t *testing.T) {
		pcs := schedulingConfigRolloutTestPCS()
		pcsReplicaInfosByIndex := pcsReplicaInfoByIndex{
			5: rollingUpdateReplicaInfoAtGeneration(pcs, 5, "superseded", true),
			4: rollingUpdateReplicaInfoAtGeneration(pcs, 4, "pending", true),
			3: rollingUpdateReplicaInfoAtGeneration(pcs, 3, "pending", false),
			2: rollingUpdateReplicaInfoAtGeneration(pcs, 2, "superseded", true),
			1: rollingUpdateReplicaInfoAtGeneration(pcs, 1, "pending", true),
			0: rollingUpdateReplicaInfoAtGeneration(pcs, 0, "pending", false),
			6: rollingUpdateReplicaInfoAtGeneration(pcs, 6, *pcs.Status.CurrentGenerationHash, true),
		}

		got := selectPCSReplicaRecreationSourceIndices(pcs, pcsReplicaInfosByIndex, pcsReplicaScalingIndices{}, len(pcsReplicaInfosByIndex))

		assert.Equal(t, []int{0, 3, 1, 4, 2, 5}, got)
	})

	t.Run("selects no more replicas than the remaining update slots", func(t *testing.T) {
		pcs := schedulingConfigRolloutTestPCS()
		pcsReplicaInfosByIndex := pcsReplicaInfoByIndex{
			0: rollingUpdateReplicaInfoAtGeneration(pcs, 0, "pending", false),
			1: rollingUpdateReplicaInfoAtGeneration(pcs, 1, "pending", false),
		}

		got := selectPCSReplicaRecreationSourceIndices(pcs, pcsReplicaInfosByIndex, pcsReplicaScalingIndices{}, 1)

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

		got := selectPCSReplicaRecreationSourceIndices(
			pcs,
			pcsReplicaInfosByIndex,
			pcsReplicaScalingIndices{scaleIn: []int{0}},
			len(pcsReplicaInfosByIndex),
		)

		assert.Equal(t, []int{3}, got)
	})

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

		got := selectPCSReplicaRecreationSourceIndices(
			pcs,
			pcsReplicaInfosByIndex,
			pcsReplicaScalingIndices{},
			len(pcsReplicaInfosByIndex),
		)

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

		got := selectPCSReplicaRecreationSourceIndices(
			pcs,
			pcsReplicaInfosByIndex,
			pcsReplicaScalingIndices{scaleOut: []int{0, 1, 2, 3}},
			len(pcsReplicaInfosByIndex),
		)

		assert.Equal(t, []int{1, 2, 4}, got)
	})

	t.Run("old-generation scale-out becomes eligible after its reservation clears", func(t *testing.T) {
		pcs := replicaRecreationSelectionTestPCS()
		pcsReplicaInfosByIndex := pcsReplicaInfoByIndex{
			0: replicaRecreationSelectionInfo(pcs, 0, "superseded", true, true),
		}

		whileReserved := selectPCSReplicaRecreationSourceIndices(
			pcs,
			pcsReplicaInfosByIndex,
			pcsReplicaScalingIndices{scaleOut: []int{0}},
			1,
		)
		afterReservationClears := selectPCSReplicaRecreationSourceIndices(
			pcs,
			pcsReplicaInfosByIndex,
			pcsReplicaScalingIndices{},
			1,
		)

		assert.Empty(t, whileReserved)
		assert.Equal(t, []int{0}, afterReservationClears)
	})

	t.Run("current-generation scale-out does not become eligible after its reservation clears", func(t *testing.T) {
		pcs := replicaRecreationSelectionTestPCS()
		pcsReplicaInfosByIndex := pcsReplicaInfoByIndex{
			0: replicaRecreationSelectionInfo(pcs, 0, *pcs.Status.CurrentGenerationHash, true, true),
		}

		got := selectPCSReplicaRecreationSourceIndices(
			pcs,
			pcsReplicaInfosByIndex,
			pcsReplicaScalingIndices{},
			1,
		)

		assert.Empty(t, got)
	})

	t.Run("returns nil when no update slot remains", func(t *testing.T) {
		pcs := schedulingConfigRolloutTestPCS()

		got := selectPCSReplicaRecreationSourceIndices(
			pcs,
			pcsReplicaInfoByIndex{0: rollingUpdateReplicaInfoAtGeneration(pcs, 0, "pending", false)},
			pcsReplicaScalingIndices{},
			0,
		)

		assert.Nil(t, got)
	})
}

func TestAllocatePCSReplicaReplacementTargets(t *testing.T) {
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
			name:                      "ordinary scale-out reservation blocks hole",
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
			name:                      "ordinary scale-in reservation blocks hole",
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

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs := &grovecorev1beta1.PodCliqueSet{Status: grovecorev1beta1.PodCliqueSetStatus{
				ScalingProgress: tt.scalingProgress,
				UpdateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
					RequiresReplicaRecreation: true,
					CurrentlyUpdating:         tt.currentlyUpdating,
				},
			}}
			existingPCSReplicaIndicesBefore := tt.existingPCSReplicaIndices.Clone()
			selectedSourcePCSReplicaIndicesBefore := slices.Clone(tt.selectedSourcePCSReplicaIndices)

			got := allocatePCSReplicaReplacementTargets(pcs, tt.existingPCSReplicaIndices, tt.selectedSourcePCSReplicaIndices)

			assert.Equal(t, tt.want, got)
			assert.Equal(t, existingPCSReplicaIndicesBefore, tt.existingPCSReplicaIndices, "allocatePCSReplicaReplacementTargets mutated existing replica indices")
			assert.Equal(t, selectedSourcePCSReplicaIndicesBefore, tt.selectedSourcePCSReplicaIndices, "allocatePCSReplicaReplacementTargets mutated selected source replica indices")
		})
	}
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
		pclq.Status.CurrentPodCliqueSetGenerationHash = new(pcsGenerationHash)
		pclq.Status.ReadyReplicas = 1
		pcsReplicaInfoForSelection.standalonePCLQs = []grovecorev1beta1.PodClique{*pclq}
	}
	if includePCSG {
		pcsgName := componentutils.GetPCSGFQNsForPCSReplica(pcs, pcsReplicaIndex)[0]
		pcsg := testutils.NewPodCliqueScalingGroupBuilder(pcsgName, pcs.Namespace, pcs.Name, pcsReplicaIndex).Build()
		pcsg.Status.CurrentPodCliqueSetGenerationHash = new(pcsGenerationHash)
		pcsg.Status.AvailableReplicas = 1
		pcsReplicaInfoForSelection.pcsgs = []grovecorev1beta1.PodCliqueScalingGroup{*pcsg}
	}
	return pcsReplicaInfoForSelection
}

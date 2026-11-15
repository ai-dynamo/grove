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
	"testing"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestExpectedReplicaIndicesFromScaling(t *testing.T) {
	existing := sets.New(0, 2)
	scalingIndices := pcsReplicaScalingIndices{scaleOut: []int{1}, scaleIn: []int{2}}

	got := expectedReplicaIndicesFromScaling(existing, scalingIndices)

	assert.Equal(t, sets.New(0, 1), got)
	assert.Equal(t, sets.New(0, 2), existing, "input set must not be mutated")
}

func TestComputePCSReplicaStateReplacementOverlay(t *testing.T) {
	observedPCS := func() *grovecorev1beta1.PodCliqueSet {
		return &grovecorev1beta1.PodCliqueSet{
			Status: grovecorev1beta1.PodCliqueSetStatus{
				ObservedGeneration: new(int64(1)),
			},
		}
	}
	withReplacements := func(pcs *grovecorev1beta1.PodCliqueSet, entries ...grovecorev1beta1.PodCliqueSetReplicaUpdateProgress) *grovecorev1beta1.PodCliqueSet {
		pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
			RequiresReplicaRecreation: true,
			CurrentlyUpdating:         entries,
		}
		return pcs
	}

	t.Run("different-index active replacement moves source to scale-in and target to scale-out", func(t *testing.T) {
		pcs := withReplacements(observedPCS(), grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex: 2,
			Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
		})
		replicaInfos := pcsReplicaInfoByIndex{0: {index: 0}, 2: {index: 2}}
		expected := sets.New(0, 2)

		got := computePCSReplicaState(pcs, replicaInfos, pcsReplicaScalingIndices{}, expected, groupPCSReplicaReplacements(pcs.Status.UpdateProgress))

		assert.Equal(t, []int{0, 1}, got.ExpectedReplicaIndices)
		assert.Equal(t, []int{1}, got.ScaleOutReplicaIndices)
		assert.Equal(t, []int{2}, got.ScaleInReplicaIndices)
	})

	t.Run("same-index active replacement deletes before source cleanup completes", func(t *testing.T) {
		pcs := withReplacements(observedPCS(), grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex: 1,
			Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
		})
		replicaInfos := pcsReplicaInfoByIndex{0: {index: 0}, 1: {index: 1}}
		expected := sets.New(0, 1)

		got := computePCSReplicaState(pcs, replicaInfos, pcsReplicaScalingIndices{}, expected, groupPCSReplicaReplacements(pcs.Status.UpdateProgress))

		assert.Equal(t, []int{0}, got.ExpectedReplicaIndices)
		assert.Empty(t, got.ScaleOutReplicaIndices)
		assert.Equal(t, []int{1}, got.ScaleInReplicaIndices)
	})

	t.Run("same-index active replacement creates after source cleanup completes", func(t *testing.T) {
		cleanupTime := metav1.Now()
		pcs := withReplacements(observedPCS(), grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex: 1,
			Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex:       1,
				SourceCleanupCompletedAt: &cleanupTime,
			},
		})
		replicaInfos := pcsReplicaInfoByIndex{0: {index: 0}}
		expected := sets.New(0)

		got := computePCSReplicaState(pcs, replicaInfos, pcsReplicaScalingIndices{}, expected, groupPCSReplicaReplacements(pcs.Status.UpdateProgress))

		assert.Equal(t, []int{0, 1}, got.ExpectedReplicaIndices)
		assert.Equal(t, []int{1}, got.ScaleOutReplicaIndices)
		assert.Empty(t, got.ScaleInReplicaIndices)
	})

	t.Run("canceled replacement drains present source and target through scale-in", func(t *testing.T) {
		canceledAt := metav1.Now()
		pcs := withReplacements(observedPCS(), grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex: 2,
			Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex: 1,
				CanceledAt:         &canceledAt,
			},
		})
		replicaInfos := pcsReplicaInfoByIndex{0: {index: 0}, 1: {index: 1}, 2: {index: 2}}
		expected := sets.New(0, 1, 2)

		got := computePCSReplicaState(pcs, replicaInfos, pcsReplicaScalingIndices{}, expected, groupPCSReplicaReplacements(pcs.Status.UpdateProgress))

		assert.Equal(t, []int{0}, got.ExpectedReplicaIndices)
		assert.Empty(t, got.ScaleOutReplicaIndices)
		assert.Equal(t, []int{1, 2}, got.ScaleInReplicaIndices)
	})

	t.Run("canceled replacement omits absent target from scale-in", func(t *testing.T) {
		canceledAt := metav1.Now()
		pcs := withReplacements(observedPCS(), grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex: 2,
			Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex: 1,
				CanceledAt:         &canceledAt,
			},
		})
		replicaInfos := pcsReplicaInfoByIndex{0: {index: 0}, 2: {index: 2}}
		expected := sets.New(0, 2)

		got := computePCSReplicaState(pcs, replicaInfos, pcsReplicaScalingIndices{}, expected, groupPCSReplicaReplacements(pcs.Status.UpdateProgress))

		assert.Equal(t, []int{0}, got.ExpectedReplicaIndices)
		assert.Equal(t, []int{2}, got.ScaleInReplicaIndices)
	})

	t.Run("does not mutate the caller expected set", func(t *testing.T) {
		pcs := withReplacements(observedPCS(), grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			ReplicaIndex: 2,
			Replacement:  &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
		})
		replicaInfos := pcsReplicaInfoByIndex{0: {index: 0}, 2: {index: 2}}
		expected := sets.New(0, 2)

		computePCSReplicaState(pcs, replicaInfos, pcsReplicaScalingIndices{}, expected, groupPCSReplicaReplacements(pcs.Status.UpdateProgress))

		assert.Equal(t, sets.New(0, 2), expected)
	})
}

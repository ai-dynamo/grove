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
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestPCSReplicaUpdateSelectionBudget(t *testing.T) {
	pcs := &grovecorev1beta1.PodCliqueSet{}

	assert.Equal(t, 1, pcsReplicaUpdateSelectionBudget(pcs, 0))
	assert.Equal(t, 0, pcsReplicaUpdateSelectionBudget(pcs, 1))
	assert.Equal(t, 0, pcsReplicaUpdateSelectionBudget(pcs, 2))
}

func TestPCSReplicaInfoHasConvergedToPCSGeneration(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-uid")).
		WithStandaloneClique("frontend").
		WithScalingGroup("workers", []string{"worker"}).
		Build()
	generationHash := "generation"
	converged := pcsReplicaInfo{
		index: 0,
		standalonePCLQs: []grovecorev1beta1.PodClique{{
			Status: grovecorev1beta1.PodCliqueStatus{CurrentPodCliqueSetGenerationHash: new(generationHash)},
		}},
		pcsgs: []grovecorev1beta1.PodCliqueScalingGroup{{
			Status: grovecorev1beta1.PodCliqueScalingGroupStatus{CurrentPodCliqueSetGenerationHash: new(generationHash)},
		}},
	}

	tests := []struct {
		name string
		info pcsReplicaInfo
		want bool
	}{
		{name: "all expected children converged", info: converged, want: true},
		{name: "missing standalone PodClique", info: pcsReplicaInfo{index: 0, pcsgs: converged.pcsgs}},
		{name: "missing PodCliqueScalingGroup", info: pcsReplicaInfo{index: 0, standalonePCLQs: converged.standalonePCLQs}},
		{name: "standalone PodClique has no generation", info: pcsReplicaInfo{index: 0, standalonePCLQs: []grovecorev1beta1.PodClique{{}}, pcsgs: converged.pcsgs}},
		{name: "standalone PodClique has another generation", info: pcsReplicaInfo{index: 0, standalonePCLQs: []grovecorev1beta1.PodClique{{Status: grovecorev1beta1.PodCliqueStatus{CurrentPodCliqueSetGenerationHash: new("other")}}}, pcsgs: converged.pcsgs}},
		{name: "PodCliqueScalingGroup has no generation", info: pcsReplicaInfo{index: 0, standalonePCLQs: converged.standalonePCLQs, pcsgs: []grovecorev1beta1.PodCliqueScalingGroup{{}}}},
		{name: "PodCliqueScalingGroup has another generation", info: pcsReplicaInfo{index: 0, standalonePCLQs: converged.standalonePCLQs, pcsgs: []grovecorev1beta1.PodCliqueScalingGroup{{Status: grovecorev1beta1.PodCliqueScalingGroupStatus{CurrentPodCliqueSetGenerationHash: new("other")}}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.info.hasConvergedToPCSGeneration(pcs, generationHash))
		})
	}
}

func TestPCSReplicaInfoIsAvailable(t *testing.T) {
	pcs := rollingUpdateTestPCS()
	available := rollingUpdateReplicaInfoAtGeneration(pcs, 0, "generation", true)
	unavailable := rollingUpdateReplicaInfoAtGeneration(pcs, 0, "generation", false)

	assert.True(t, available.isAvailable(pcs))
	assert.False(t, unavailable.isAvailable(pcs))
	assert.False(t, (&pcsReplicaInfo{index: 0}).isAvailable(pcs))
}

func TestReconcileCurrentlyUpdatingPCSReplicasForConfiguredStrategy(t *testing.T) {
	pcs := rollingUpdateTestPCS()
	// Entry 0 completed, entry 1 is still updating, entry 3 is no longer eligible (removed by scale-in or
	// gang termination, absent from replicaInfos), and entry 2 already ended.
	pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
		{ReplicaIndex: 0},
		{ReplicaIndex: 1},
		{ReplicaIndex: 2, UpdateEndedAt: new(metav1.Now())},
		{ReplicaIndex: 3},
	}
	replicaInfos := []pcsReplicaInfo{
		rollingUpdateReplicaInfo(pcs, 0, true),
		rollingUpdateReplicaInfo(pcs, 1, false),
	}

	result, stillUpdating := reconcileCurrentlyUpdatingPCSReplicasForConfiguredStrategy(pcs, replicaInfos, pcs.Status.UpdateProgress.CurrentlyUpdating)

	assert.Equal(t, []int{0}, result.completedPCSReplicaIndices)
	assert.Equal(t, []int{3}, result.pcsReplicaUpdateIndicesToRemove)
	assert.Equal(t, sets.New(1), stillUpdating)
}

func TestReconcileCurrentlyUpdatingPCSReplicasForConfiguredStrategyRetainsIncompleteSelection(t *testing.T) {
	pcs := rollingUpdateTestPCS()
	pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 1}}
	replicaInfos := []pcsReplicaInfo{
		rollingUpdateReplicaInfo(pcs, 0, false),
		rollingUpdateReplicaInfo(pcs, 1, false),
	}

	result, stillUpdating := reconcileCurrentlyUpdatingPCSReplicasForConfiguredStrategy(pcs, replicaInfos, pcs.Status.UpdateProgress.CurrentlyUpdating)

	assert.Empty(t, result.completedPCSReplicaIndices)
	assert.Empty(t, result.pcsReplicaUpdateIndicesToRemove)
	assert.Equal(t, sets.New(1), stillUpdating)
}

func TestSelectPCSReplicasForConfiguredStrategy(t *testing.T) {
	pcs := rollingUpdateTestPCS()
	replicaInfos := []pcsReplicaInfo{
		rollingUpdateReplicaInfo(pcs, 0, false),
		rollingUpdateReplicaInfo(pcs, 1, false),
	}

	t.Run("selects a pending replica within budget", func(t *testing.T) {
		selections := selectPCSReplicasForConfiguredStrategy(pcs, replicaInfos, sets.New[int](), nil)
		assert.Equal(t, []pcsReplicaUpdateSelection{{replicaIndex: 0}}, selections)
	})

	t.Run("returns nil when the budget is exhausted", func(t *testing.T) {
		selections := selectPCSReplicasForConfiguredStrategy(pcs, replicaInfos, sets.New(0), nil)
		assert.Nil(t, selections)
	})

	t.Run("excludes completed replicas and selects the next pending one", func(t *testing.T) {
		infos := []pcsReplicaInfo{
			rollingUpdateReplicaInfo(pcs, 0, true),
			rollingUpdateReplicaInfo(pcs, 1, false),
			rollingUpdateReplicaInfo(pcs, 2, false),
		}
		selections := selectPCSReplicasForConfiguredStrategy(pcs, infos, sets.New[int](), nil)
		assert.Equal(t, []pcsReplicaUpdateSelection{{replicaIndex: 1}}, selections)
	})
}

func TestIsConfiguredStrategyUpdateComplete(t *testing.T) {
	pcs := rollingUpdateTestPCS()

	t.Run("complete when every replica converged", func(t *testing.T) {
		infos := []pcsReplicaInfo{rollingUpdateReplicaInfo(pcs, 0, true)}
		assert.True(t, isConfiguredStrategyUpdateComplete(pcs, infos))
	})

	t.Run("incomplete when a replica has not converged", func(t *testing.T) {
		infos := []pcsReplicaInfo{rollingUpdateReplicaInfo(pcs, 0, false)}
		assert.False(t, isConfiguredStrategyUpdateComplete(pcs, infos))
	})
}

func TestCancelReplacements(t *testing.T) {
	newProgress := func() []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress {
		return []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			{ReplicaIndex: 0, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 3}},
			{ReplicaIndex: 1},
			{ReplicaIndex: 2, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 4}},
		}
	}

	t.Run("stamps CanceledAt and message on the named replacement source", func(t *testing.T) {
		currentlyUpdating := newProgress()
		cancelReplacements(currentlyUpdating, []int{0})

		require.NotNil(t, currentlyUpdating[0].Replacement.CanceledAt)
		require.NotNil(t, currentlyUpdating[0].Replacement.CancellationMessage)
		assert.Equal(t, cancelReplacementMessage, *currentlyUpdating[0].Replacement.CancellationMessage)
		assert.Nil(t, currentlyUpdating[2].Replacement.CanceledAt)
	})

	t.Run("leaves an entry without a replacement unchanged", func(t *testing.T) {
		currentlyUpdating := newProgress()
		cancelReplacements(currentlyUpdating, []int{1})

		assert.Nil(t, currentlyUpdating[1].Replacement)
	})

	t.Run("does not re-stamp an already canceled replacement", func(t *testing.T) {
		currentlyUpdating := newProgress()
		canceledAt := metav1.NewTime(metav1.Now().Add(-time.Hour))
		priorMessage := "prior"
		currentlyUpdating[0].Replacement.CanceledAt = &canceledAt
		currentlyUpdating[0].Replacement.CancellationMessage = &priorMessage

		cancelReplacements(currentlyUpdating, []int{0})

		assert.Equal(t, canceledAt, *currentlyUpdating[0].Replacement.CanceledAt)
		assert.Equal(t, priorMessage, *currentlyUpdating[0].Replacement.CancellationMessage)
	})

	t.Run("is a no-op for an empty cancellation set", func(t *testing.T) {
		currentlyUpdating := newProgress()
		cancelReplacements(currentlyUpdating, nil)

		assert.Nil(t, currentlyUpdating[0].Replacement.CanceledAt)
		assert.Nil(t, currentlyUpdating[2].Replacement.CanceledAt)
	})
}

func schedulingConfigRolloutTestPCS() *grovecorev1beta1.PodCliqueSet {
	pcs := rollingUpdateTestPCS()
	pcs.Status.AppliedSchedulingConfigurationHash = new("applied")
	pcs.Status.UpdateProgress.RequiresReplicaRecreation = true
	pcs.Status.UpdateProgress.TargetSchedulingConfigurationHash = "target"
	pcs.Status.UpdateProgress.PreviousPodCliqueSetGenerationHash = new("superseded")
	return pcs
}

func rollingUpdateTestPCS() *grovecorev1beta1.PodCliqueSet {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-uid")).
		WithStandaloneClique("worker").
		WithPodCliqueSetGenerationHash(new("generation")).
		Build()
	pcs.Spec.Template.Cliques[0].Spec.MinAvailable = new(int32(1))
	pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{UpdateStartedAt: metav1.Now()}
	return pcs
}

func rollingUpdateReplicaInfoAtGeneration(pcs *grovecorev1beta1.PodCliqueSet, replicaIndex int, generationHash string, available bool) pcsReplicaInfo {
	info := rollingUpdateReplicaInfo(pcs, replicaIndex, false)
	info.standalonePCLQs[0].Status.CurrentPodCliqueSetGenerationHash = new(generationHash)
	if available {
		info.standalonePCLQs[0].Status.ReadyReplicas = 1
	} else {
		info.standalonePCLQs[0].Status.ReadyReplicas = 0
	}
	return info
}

func rollingUpdateReplicaInfo(pcs *grovecorev1beta1.PodCliqueSet, replicaIndex int, complete bool) pcsReplicaInfo {
	expectedPodTemplateHash := componentutils.ComputePCLQPodTemplateHash(pcs.Spec.Template.Cliques[0], pcs.Spec.Template.PriorityClassName)
	podTemplateHash := "stale"
	generationHash := "stale"
	if complete {
		podTemplateHash = expectedPodTemplateHash
		generationHash = *pcs.Status.CurrentGenerationHash
	}
	pclq := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, "worker", pcs.Namespace, int32(replicaIndex)).
		WithMinAvailable(1).
		WithLabels(map[string]string{apicommon.LabelPodTemplateHash: podTemplateHash}).
		Build()
	pclq.Status.CurrentPodTemplateHash = &podTemplateHash
	pclq.Status.CurrentPodCliqueSetGenerationHash = &generationHash
	pclq.Status.UpdatedReplicas = 1
	pclq.Status.ReadyReplicas = 1
	pclq.Status.ScheduledReplicas = 1
	return pcsReplicaInfo{index: replicaIndex, standalonePCLQs: []grovecorev1beta1.PodClique{*pclq}}
}

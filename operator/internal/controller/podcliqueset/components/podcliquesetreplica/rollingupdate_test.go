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

func TestPCSReplicaInfoExistingResourcesMatchPCSGeneration(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-uid")).
		WithStandaloneClique("frontend").
		WithScalingGroup("workers", []string{"worker"}).
		Build()
	tests := []struct {
		name         string
		pclqHashes   []string
		pcsgHashes   []string
		wantExisting bool
		wantComplete bool
	}{
		{name: "all expected children current", pclqHashes: []string{"current"}, pcsgHashes: []string{"current"}, wantExisting: true, wantComplete: true},
		{name: "missing PCLQ with current PCSG", pcsgHashes: []string{"current"}, wantExisting: true},
		{name: "missing PCSG with current PCLQ", pclqHashes: []string{"current"}, wantExisting: true},
		{name: "no observed children", wantExisting: true},
		{name: "existing PCLQ has old generation", pclqHashes: []string{"old"}, pcsgHashes: []string{"current"}},
		{name: "existing PCSG has old generation", pclqHashes: []string{"current"}, pcsgHashes: []string{"old"}},
		{name: "missing PCSG with old PCLQ", pclqHashes: []string{"old"}},
		{name: "missing PCLQ with old PCSG", pcsgHashes: []string{"old"}},
		{name: "mixed observed generations", pclqHashes: []string{"current", "old"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info := pcsReplicaInfo{index: 0}
			for _, hash := range tt.pclqHashes {
				info.standalonePCLQs = append(info.standalonePCLQs, grovecorev1beta1.PodClique{
					Status: grovecorev1beta1.PodCliqueStatus{CurrentPodCliqueSetGenerationHash: new(hash)},
				})
			}
			for _, hash := range tt.pcsgHashes {
				info.pcsgs = append(info.pcsgs, grovecorev1beta1.PodCliqueScalingGroup{
					Status: grovecorev1beta1.PodCliqueScalingGroupStatus{CurrentPodCliqueSetGenerationHash: new(hash)},
				})
			}

			assert.Equal(t, tt.wantExisting, info.existingResourcesMatchPCSGeneration("current"))
			assert.Equal(t, tt.wantComplete, info.hasConvergedToPCSGeneration(pcs, "current"))
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
	reconcileTime := metav1.NewTime(time.Unix(200, 0).UTC())
	newProgress := func() []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress {
		return []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			{ReplicaIndex: 0, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 3}},
			{ReplicaIndex: 1},
			{ReplicaIndex: 2, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 4}},
		}
	}

	t.Run("stamps CanceledAt and message on the named replacement source", func(t *testing.T) {
		currentlyUpdating := newProgress()
		cancelReplacements(currentlyUpdating, []int{0}, reconcileTime)

		require.NotNil(t, currentlyUpdating[0].Replacement.CanceledAt)
		assert.Equal(t, reconcileTime, *currentlyUpdating[0].Replacement.CanceledAt)
		require.NotNil(t, currentlyUpdating[0].Replacement.CancellationMessage)
		assert.Equal(t, cancelReplacementMessage, *currentlyUpdating[0].Replacement.CancellationMessage)
		assert.Nil(t, currentlyUpdating[2].Replacement.CanceledAt)
	})

	t.Run("leaves an entry without a replacement unchanged", func(t *testing.T) {
		currentlyUpdating := newProgress()
		cancelReplacements(currentlyUpdating, []int{1}, reconcileTime)

		assert.Nil(t, currentlyUpdating[1].Replacement)
	})

	t.Run("does not re-stamp an already canceled replacement", func(t *testing.T) {
		currentlyUpdating := newProgress()
		canceledAt := metav1.NewTime(metav1.Now().Add(-time.Hour))
		priorMessage := "prior"
		currentlyUpdating[0].Replacement.CanceledAt = &canceledAt
		currentlyUpdating[0].Replacement.CancellationMessage = &priorMessage

		cancelReplacements(currentlyUpdating, []int{0}, reconcileTime)

		assert.Equal(t, canceledAt, *currentlyUpdating[0].Replacement.CanceledAt)
		assert.Equal(t, priorMessage, *currentlyUpdating[0].Replacement.CancellationMessage)
	})

	t.Run("is a no-op for an empty cancellation set", func(t *testing.T) {
		currentlyUpdating := newProgress()
		cancelReplacements(currentlyUpdating, nil, reconcileTime)

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

// TestAdvancePCSUpdateProgressCompletedReplacementChildLoss checks that a completed target's resource loss cannot
// trigger reselection or delay another replacement. Missing resources on the unfinished target remain
// provisioning work until they reappear at the current generation and completion can be recorded.
func TestAdvancePCSUpdateProgressCompletedReplacementChildLoss(t *testing.T) {
	t.Parallel()
	startedAt := metav1.NewTime(time.Unix(100, 0).UTC())
	completedAt := metav1.NewTime(time.Unix(150, 0).UTC())
	tests := []struct {
		name                        string
		completedSourceReplicaIndex int32
		completedTargetReplicaIndex int
		allCompletedResourcesLost   bool
	}{
		{name: "same-index completed target loses a child"},
		{name: "different-index completed target loses a child", completedSourceReplicaIndex: 2, completedTargetReplicaIndex: 1},
		{name: "completed target loses every resource", allCompletedResourcesLost: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var scalingProgress *grovecorev1beta1.PodCliqueSetScalingProgress
			if tc.allCompletedResourcesLost {
				scalingProgress = &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleOutReplicaIndices: []int32{int32(tc.completedTargetReplicaIndex)}}
			}
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "uid").
				WithReplicas(2).
				WithObservedGeneration(new(int64(1))).
				WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithMinAvailable(1).Build()).
				WithScalingGroup("workers", []string{"member"}).
				WithPodCliqueSetGenerationHash(new("current")).
				WithAppliedSchedulingConfigurationHash(new("superseded")).
				WithScalingProgress(scalingProgress).
				WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
					RequiresReplicaRecreation:         true,
					UpdateStartedAt:                   startedAt,
					TargetSchedulingConfigurationHash: "target-scheduling",
					CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
						{ReplicaIndex: tc.completedSourceReplicaIndex, UpdateStartedAt: startedAt, UpdateEndedAt: &completedAt,
							Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: int32(tc.completedTargetReplicaIndex), SourceCleanupCompletedAt: &startedAt}},
						{ReplicaIndex: 3, UpdateStartedAt: startedAt,
							Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 3, SourceCleanupCompletedAt: &startedAt}},
					},
				}).Build()
			replicaInfos := pcsReplicaInfoByIndex{3: rollingUpdateReplicaInfo(pcs, 3, true)}
			if !tc.allCompletedResourcesLost {
				replicaInfos[tc.completedTargetReplicaIndex] = rollingUpdateReplicaInfo(pcs, tc.completedTargetReplicaIndex, true)
			}
			scalingIndices := replicaScalingIndicesFromProgress(pcs.Status.ScalingProgress)
			before := pcs.DeepCopy()
			hooks := newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, scalingIndices)

			err := advancePCSUpdateProgress(pcs, hooks, metav1.Now())

			require.NoError(t, err, "missing target resources must not cause a new-selection requeue that skips synchronization")
			require.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 2)
			assert.Equal(t, before.Status.UpdateProgress.CurrentlyUpdating[0], pcs.Status.UpdateProgress.CurrentlyUpdating[0], "completed progress must remain unchanged after child loss")
			assert.Nil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[1].UpdateEndedAt, "the unfinished target is still missing its scaling group")
			assert.Nil(t, pcs.Status.UpdateProgress.UpdateEndedAt)
			expected := computePCSReplicaIndicesAfterScaling(replicaInfos.existingReplicaIndices(), scalingIndices)
			state := computePCSReplicaState(pcs, scalingIndices, expected, groupPCSReplicaReplacements(pcs.Status.UpdateProgress))
			assert.Equal(t, []int{tc.completedTargetReplicaIndex, 3}, state.ExpectedReplicaIndices, "component synchronization must be allowed to repair missing children")
			assert.Contains(t, state.ScaleOutReplicaIndices, 3, "the unfinished target remains provisioning work")

			// Model synchronization recreating the unfinished target's missing PCSG at S1. The completed
			// target remains missing its resources, so it must not gate the update's completion.
			pcsgName := componentutils.GetPCSGFQNsForPCSReplica(pcs, 3)[0]
			pcsg := testutils.NewPodCliqueScalingGroupBuilder(pcsgName, pcs.Namespace, pcs.Name, 3).
				WithMinAvailable(1).Build()
			pcsg.Status.CurrentPodCliqueSetGenerationHash = new("current")
			pcsg.Status.AvailableReplicas = 1
			readyTarget := replicaInfos[3]
			readyTarget.pcsgs = []grovecorev1beta1.PodCliqueScalingGroup{*pcsg}
			replicaInfos[3] = readyTarget
			hooks = newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, scalingIndices)

			err = advancePCSUpdateProgress(pcs, hooks, metav1.Now())

			require.NoError(t, err, "completed child loss must not cause repeated replacement selection")
			require.NotNil(t, pcs.Status.UpdateProgress)
			assert.NotNil(t, pcs.Status.UpdateProgress.UpdateEndedAt)
			assert.Empty(t, pcs.Status.UpdateProgress.CurrentlyUpdating)
			require.NotNil(t, pcs.Status.AppliedSchedulingConfigurationHash)
			assert.Equal(t, "target-scheduling", *pcs.Status.AppliedSchedulingConfigurationHash)
		})
	}
}

// TestAdvancePCSUpdateProgressWaitsForRetiredSourceCleanup verifies that a ready target cannot end an update while retired
// cleanup remains pending. Completion and the applied scheduling hash advance only after every retirement clears.
func TestAdvancePCSUpdateProgressWaitsForRetiredSourceCleanup(t *testing.T) {
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
				SourceCleanupCompletedAt:    &cleanupCompletedAt,
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
			replicaInfos := pcsReplicaInfoByIndex{int(tc.targetPCSReplicaIndex): rollingUpdateReplicaInfo(pcs, int(tc.targetPCSReplicaIndex), true)}
			for len(pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.RetiredSourceReplicaIndices) > 0 {
				before := pcs.DeepCopy()
				hooks := newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, pcsReplicaScalingIndices{})

				err := advancePCSUpdateProgress(pcs, hooks, metav1.Now())

				require.NoError(t, err)
				require.NotNil(t, pcs.Status.UpdateProgress)
				require.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1)
				assert.Nil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].UpdateEndedAt)
				assert.Nil(t, pcs.Status.UpdateProgress.UpdateEndedAt)
				assert.Equal(t, before.Status, pcs.Status, "pending retirement must preserve update progress and the applied hash")

				// Model removal of one participant-confirmed retired source before the next planning pass.
				pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.RetiredSourceReplicaIndices =
					pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.RetiredSourceReplicaIndices[1:]
			}
			hooks := newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, pcsReplicaScalingIndices{})

			err := advancePCSUpdateProgress(pcs, hooks, metav1.Now())

			require.NoError(t, err)
			require.NotNil(t, pcs.Status.UpdateProgress)
			assert.NotNil(t, pcs.Status.UpdateProgress.UpdateEndedAt)
			assert.Empty(t, pcs.Status.UpdateProgress.CurrentlyUpdating)
			require.NotNil(t, pcs.Status.AppliedSchedulingConfigurationHash)
			assert.Equal(t, "target-scheduling", *pcs.Status.AppliedSchedulingConfigurationHash)
		})
	}
}

func TestAdvancePCSUpdateProgressAdvancesAppliedHashOnReplacementCompletion(t *testing.T) {
	pcs := rollingUpdateTestPCS()
	pcs.Status.AppliedSchedulingConfigurationHash = new("superseded")
	pcs.Status.UpdateProgress.RequiresReplicaRecreation = true
	pcs.Status.UpdateProgress.TargetSchedulingConfigurationHash = "target-scheduling"
	pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
		{ReplicaIndex: 0, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0, SourceCleanupCompletedAt: new(metav1.Now())}},
	}
	replicaInfos := pcsReplicaInfoByIndex{0: rollingUpdateReplicaInfo(pcs, 0, true)}
	hooks := newPCSReplicaRecreationStrategyHooks(pcs, replicaInfos, pcsReplicaScalingIndices{})

	err := advancePCSUpdateProgress(pcs, hooks, metav1.Now())

	assert.NoError(t, err)
	require.NotNil(t, pcs.Status.AppliedSchedulingConfigurationHash)
	assert.Equal(t, "target-scheduling", *pcs.Status.AppliedSchedulingConfigurationHash)
	require.NotNil(t, pcs.Status.UpdateProgress.UpdateEndedAt)
	assert.Nil(t, pcs.Status.UpdateProgress.CurrentlyUpdating)
}

func TestNewUpdateStrategyHooksKeepsConfiguredParticipationScope(t *testing.T) {
	for _, strategy := range []grovecorev1beta1.UpdateStrategyType{grovecorev1beta1.RollingRecreateStrategy, grovecorev1beta1.CoherentStrategy} {
		t.Run(string(strategy), func(t *testing.T) {
			pcs := rollingUpdateTestPCS()
			pcs.Spec.UpdateStrategy = &grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: strategy}
			infos := pcsReplicaInfoByIndex{0: rollingUpdateReplicaInfo(pcs, 0, false), 1: rollingUpdateReplicaInfo(pcs, 1, false)}
			// Replica 1 is scaled in and replica 0 is assigned to gang termination. Neither has
			// a configured-strategy update completion obligation.
			hooks := newUpdateStrategyHooks(pcs, infos, sets.New(0), &deletionWork{pcsIndicesToTerminate: []int{0}})
			assert.True(t, hooks.isUpdateComplete())
			pcs.Status.UpdateProgress.RequiresReplicaRecreation = true
			pcs.Status.ScalingProgress = &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{1}}
			hooks = newUpdateStrategyHooks(pcs, infos, sets.New(0), &deletionWork{pcsIndicesToTerminate: []int{0}})
			recreationHooks, found := hooks.(*pcsReplicaRecreationStrategyHooks)
			require.True(t, found, "recreation must override the configured strategy")
			assert.Equal(t, infos, recreationHooks.replicaInfosByIndex, "recreation needs full resource observations")
			assert.Equal(t, []int{1}, recreationHooks.scalingIndices.scaleIn)
			assert.Same(t, pcs, recreationHooks.pcs)
		})
	}
}

func TestAdvancePCSUpdateProgressRemovesObsoleteEntriesBeforeSelecting(t *testing.T) {
	t.Parallel()
	pcs := rollingUpdateTestPCS()
	pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}, {ReplicaIndex: 1}}
	infos := pcsReplicaInfoByIndex{1: rollingUpdateReplicaInfo(pcs, 1, true), 2: rollingUpdateReplicaInfo(pcs, 2, false)}
	hooks := newUpdateStrategyHooks(pcs, infos, sets.New(1, 2), &deletionWork{})
	reconcileTime := metav1.NewTime(time.Unix(200, 0).UTC())
	require.NoError(t, advancePCSUpdateProgress(pcs, hooks, reconcileTime))
	require.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1)
	selection := pcs.Status.UpdateProgress.CurrentlyUpdating[0]
	assert.Equal(t, int32(2), selection.ReplicaIndex, "scaled-in 0 is removed and completed non-replacement 1 is pruned")
	assert.Equal(t, reconcileTime, selection.UpdateStartedAt)
	assert.Nil(t, selection.UpdateEndedAt)
	assert.Nil(t, pcs.Status.UpdateProgress.UpdateEndedAt)
	before := pcs.DeepCopy()
	hooks = newUpdateStrategyHooks(pcs, pcsReplicaInfoByIndex{}, sets.New(2), &deletionWork{})
	require.NoError(t, advancePCSUpdateProgress(pcs, hooks, metav1.NewTime(reconcileTime.Add(time.Minute))))
	assert.Equal(t, before, pcs, "missing children alone must not discard a selected replica's obligation")
}

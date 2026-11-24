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

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	coherentTestPCSName    = "test-pcs"
	coherentTestNamespace  = "test-namespace"
	coherentTestPCSUID     = "uid"
	coherentTestCurrentGen = "v2"
)

func TestInScopeUpdateCounts(t *testing.T) {
	pcs := coherentTestPCS([]string{"frontend", "router"}, []string{"decode"})
	replicaInfo := pcsReplicaInfo{
		index: 0,
		standalonePCLQs: []grovecorev1beta1.PodClique{
			standalonePCLQAtHash(pcs, "frontend", coherentHashOf(pcs, "frontend")), // converged
			standalonePCLQAtHash(pcs, "router", "stale-hash"),                      // not converged
		},
		pcsgs: []grovecorev1beta1.PodCliqueScalingGroup{
			pcsgAtGenerationHash(pcs, "decode", nil), // not converged
		},
	}

	inScopeStandalone, updatedStandalone, inScopePCSG, updatedPCSG := inScopeUpdateCounts(pcs, replicaInfo)

	assert.Equal(t, 2, inScopeStandalone)
	assert.Equal(t, 1, updatedStandalone)
	assert.Equal(t, 1, inScopePCSG)
	assert.Equal(t, 0, updatedPCSG)
}

func TestCoherentProgressMessage(t *testing.T) {
	t.Run("returns nil when every in-scope component has converged", func(t *testing.T) {
		pcs := coherentTestPCS([]string{"frontend"}, []string{"decode"})
		replicaInfo := pcsReplicaInfo{
			standalonePCLQs: []grovecorev1beta1.PodClique{standalonePCLQAtHash(pcs, "frontend", coherentHashOf(pcs, "frontend"))},
			pcsgs:           []grovecorev1beta1.PodCliqueScalingGroup{pcsgAtGenerationHash(pcs, "decode", new(coherentTestCurrentGen))},
		}
		assert.Nil(t, coherentProgressMessage(pcs, replicaInfo))
	})

	t.Run("summarizes standalone PodCliques and PodCliqueScalingGroups when both are in scope", func(t *testing.T) {
		pcs := coherentTestPCS([]string{"frontend", "router"}, []string{"decode"})
		replicaInfo := pcsReplicaInfo{
			standalonePCLQs: []grovecorev1beta1.PodClique{
				standalonePCLQAtHash(pcs, "frontend", coherentHashOf(pcs, "frontend")),
				standalonePCLQAtHash(pcs, "router", "stale-hash"),
			},
			pcsgs: []grovecorev1beta1.PodCliqueScalingGroup{pcsgAtGenerationHash(pcs, "decode", nil)},
		}
		require.NotNil(t, coherentProgressMessage(pcs, replicaInfo))
		assert.Equal(t, "1/2 standalone PodCliques and 0/1 PodCliqueScalingGroups updated to the current revision", *coherentProgressMessage(pcs, replicaInfo))
	})

	t.Run("omits the standalone part when only PodCliqueScalingGroups are in scope", func(t *testing.T) {
		pcs := coherentTestPCS(nil, []string{"decode"})
		replicaInfo := pcsReplicaInfo{
			pcsgs: []grovecorev1beta1.PodCliqueScalingGroup{pcsgAtGenerationHash(pcs, "decode", nil)},
		}
		require.NotNil(t, coherentProgressMessage(pcs, replicaInfo))
		assert.Equal(t, "0/1 PodCliqueScalingGroups updated to the current revision", *coherentProgressMessage(pcs, replicaInfo))
	})
}

func TestPCSReplicaInfosExcludingTerminationIndices(t *testing.T) {
	expectedPCSReplicaIndices := []int{0, 2, 4}
	pcsReplicaIndicesToTerminate := []int{2}
	pcsReplicaInfosByIndex := pcsReplicaInfoByIndex{
		0: {index: 0, pgmEntries: []grovecorev1beta1.PodGangEntry{{Epoch: "100"}}},
		2: {index: 2},
	}

	got := pcsReplicaInfosForUpdate(
		pcsReplicaInfosByIndex,
		expectedPCSReplicaIndices,
		pcsReplicaIndicesToTerminate)

	gotPCSReplicaIndices := lo.Map(got, func(info pcsReplicaInfo, _ int) int { return info.index })
	assert.Equal(t, []int{0, 4}, gotPCSReplicaIndices)
	assert.Equal(t, pcsReplicaInfosByIndex[0].pgmEntries, got[0].pgmEntries)
	assert.Equal(t, []int{0, 2, 4}, expectedPCSReplicaIndices)
	assert.Equal(t, []int{2}, pcsReplicaIndicesToTerminate)
}

func TestIsUpdateComplete(t *testing.T) {
	singleGenEntries := []grovecorev1beta1.PodGangEntry{
		{Epoch: "100", PodCliqueSetGenerationHash: coherentTestCurrentGen},
		{Epoch: "200", PodCliqueSetGenerationHash: coherentTestCurrentGen},
	}
	multiGenEntries := []grovecorev1beta1.PodGangEntry{
		{Epoch: "50", PodCliqueSetGenerationHash: "v1"}, // a stray older generation left by a cascade
		{Epoch: "200", PodCliqueSetGenerationHash: coherentTestCurrentGen},
	}
	convergedReplicaInfo := func(pcs *grovecorev1beta1.PodCliqueSet, entries []grovecorev1beta1.PodGangEntry) pcsReplicaInfo {
		return pcsReplicaInfo{
			index:           0,
			standalonePCLQs: []grovecorev1beta1.PodClique{standalonePCLQAtHash(pcs, "frontend", coherentHashOf(pcs, "frontend"))},
			pcsgs:           []grovecorev1beta1.PodCliqueScalingGroup{pcsgAtGenerationHash(pcs, "decode", new(coherentTestCurrentGen))},
			pgmEntries:      entries,
		}
	}

	t.Run("coherent update is complete when all components converged and the PodGangMap is single-generation", func(t *testing.T) {
		pcs := coherentTestPCS([]string{"frontend"}, []string{"decode"})
		ri := convergedReplicaInfo(pcs, singleGenEntries)
		assert.True(t, ri.isUpdateComplete(pcs))
	})

	t.Run("coherent update is not complete when the PodGangMap still holds an older generation", func(t *testing.T) {
		pcs := coherentTestPCS([]string{"frontend"}, []string{"decode"})
		ri := convergedReplicaInfo(pcs, multiGenEntries)
		assert.False(t, ri.isUpdateComplete(pcs))
	})

	t.Run("RollingRecreate update does not apply the single-generation gate", func(t *testing.T) {
		pcs := coherentTestPCS([]string{"frontend"}, []string{"decode"})
		pcs.Spec.UpdateStrategy = &grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.RollingRecreateStrategy}
		ri := convergedReplicaInfo(pcs, multiGenEntries)
		assert.True(t, ri.isUpdateComplete(pcs))
	})

	t.Run("coherent update with an empty PodGangMap is complete", func(t *testing.T) {
		pcs := coherentTestPCS([]string{"frontend"}, []string{"decode"})
		ri := convergedReplicaInfo(pcs, nil)
		assert.True(t, ri.isUpdateComplete(pcs))
	})

	t.Run("an unconverged standalone PodClique keeps the update incomplete even when the PodGangMap is single-generation", func(t *testing.T) {
		pcs := coherentTestPCS([]string{"frontend"}, []string{"decode"})
		ri := convergedReplicaInfo(pcs, singleGenEntries)
		ri.standalonePCLQs = []grovecorev1beta1.PodClique{standalonePCLQAtHash(pcs, "frontend", "stale-hash")}
		assert.False(t, ri.isUpdateComplete(pcs))
	})

	t.Run("an unconverged PodCliqueScalingGroup keeps the update incomplete even when the PodGangMap is single-generation", func(t *testing.T) {
		pcs := coherentTestPCS([]string{"frontend"}, []string{"decode"})
		ri := convergedReplicaInfo(pcs, singleGenEntries)
		ri.pcsgs = []grovecorev1beta1.PodCliqueScalingGroup{pcsgAtGenerationHash(pcs, "decode", nil)} // stale generation
		assert.False(t, ri.isUpdateComplete(pcs))
	})
}

func TestRefreshCoherentReplicaProgress(t *testing.T) {
	for _, tc := range []struct {
		name        string
		entries     []grovecorev1beta1.PodGangEntry
		isConverged bool
		wantEpochs  []string
		wantError   bool
	}{
		{name: "missing map clears stale epochs"},
		{name: "old generations do not supply the in-flight epoch", entries: []grovecorev1beta1.PodGangEntry{{Epoch: "300", PodCliqueSetGenerationHash: "old"}}},
		{name: "latest current epoch wins regardless of entry order", entries: []grovecorev1beta1.PodGangEntry{{Epoch: "200", PodCliqueSetGenerationHash: coherentTestCurrentGen}, {Epoch: "300", PodCliqueSetGenerationHash: "old"}, {Epoch: "100", PodCliqueSetGenerationHash: coherentTestCurrentGen}}, wantEpochs: []string{"200"}},
		{name: "convergence clears a stale wait message", entries: []grovecorev1beta1.PodGangEntry{{Epoch: "200", PodCliqueSetGenerationHash: coherentTestCurrentGen}}, isConverged: true, wantEpochs: []string{"200"}},
		{name: "invalid current epoch leaves progress unchanged", entries: []grovecorev1beta1.PodGangEntry{{Epoch: "invalid", PodCliqueSetGenerationHash: coherentTestCurrentGen}}, wantError: true},
		{name: "invalid old epoch is ignored", entries: []grovecorev1beta1.PodGangEntry{{Epoch: "invalid", PodCliqueSetGenerationHash: "old"}, {Epoch: "100", PodCliqueSetGenerationHash: coherentTestCurrentGen}}, wantEpochs: []string{"100"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := coherentTestPCS([]string{"frontend"}, nil)
			podTemplateHash := "stale"
			if tc.isConverged {
				podTemplateHash = coherentHashOf(pcs, "frontend")
			}
			info := pcsReplicaInfo{index: 0, pgmEntries: tc.entries, standalonePCLQs: []grovecorev1beta1.PodClique{standalonePCLQAtHash(pcs, "frontend", podTemplateHash)}}
			progress := &grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{ReplicaIndex: 0, UpdateStartedAt: metav1.NewTime(time.Unix(100, 0).UTC()), InFlightEpochs: []string{"stale"}, Message: new("stale message")}
			before := progress.DeepCopy()
			pcsBefore := pcs.DeepCopy()
			entriesBefore := slices.Clone(tc.entries)

			err := refreshCoherentReplicaProgress(pcs, progress, info)

			if tc.wantError {
				assertGroveErrorCode(t, err, errCodeInvalidEpoch)
				assert.Equal(t, before, progress)
				return
			}
			require.NoError(t, err)
			want := before.DeepCopy()
			want.InFlightEpochs = tc.wantEpochs
			want.Message = nil
			if !tc.isConverged {
				want.Message = new("0/1 standalone PodCliques updated to the current revision")
			}
			assert.Equal(t, want, progress)
			assert.Equal(t, pcsBefore, pcs)
			assert.Equal(t, entriesBefore, info.pgmEntries)
		})
	}
}

func TestCoherentStrategyHooksRefreshAndSelect(t *testing.T) {
	for _, tc := range []struct {
		name           string
		hasSelection   bool
		isComplete     bool
		isScaledIn     bool
		wantCompleted  []int
		wantRemoved    []int
		wantSelections []pcsReplicaUpdateSelection
	}{
		{name: "available budget selects the breached replica", wantSelections: []pcsReplicaUpdateSelection{{replicaIndex: 1}}},
		{name: "unfinished selection consumes the budget", hasSelection: true},
		{name: "completion releases the selection budget", hasSelection: true, isComplete: true, wantCompleted: []int{0}, wantSelections: []pcsReplicaUpdateSelection{{replicaIndex: 1}}},
		{name: "scale-in removes the selection without completing it", hasSelection: true, isScaledIn: true, wantRemoved: []int{0}, wantSelections: []pcsReplicaUpdateSelection{{replicaIndex: 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := coherentTestPCS([]string{"frontend"}, nil)
			if tc.hasSelection {
				pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0, Message: new("waiting")}}
			}
			var pcsReplicaInfos []pcsReplicaInfo
			for pcsReplicaIndex := range 2 {
				if pcsReplicaIndex == 0 && tc.isScaledIn {
					continue
				}
				podTemplateHash := "stale"
				if pcsReplicaIndex == 0 && tc.isComplete {
					podTemplateHash = coherentHashOf(pcs, "frontend")
				}
				pclq := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, "frontend", pcs.Namespace, int32(pcsReplicaIndex)).
					WithMinAvailable(1).WithLabels(map[string]string{apicommon.LabelPodTemplateHash: podTemplateHash}).
					WithOptions(func(child *grovecorev1beta1.PodClique) {
						child.Status = grovecorev1beta1.PodCliqueStatus{
							CurrentPodTemplateHash: new(podTemplateHash), CurrentPodCliqueSetGenerationHash: new(coherentTestCurrentGen),
							ScheduledReplicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1,
						}
					}).Build()
				pcsReplicaInfos = append(pcsReplicaInfos, pcsReplicaInfo{index: pcsReplicaIndex, standalonePCLQs: []grovecorev1beta1.PodClique{*pclq}, pgmEntries: []grovecorev1beta1.PodGangEntry{{Epoch: "200", PodCliqueSetGenerationHash: coherentTestCurrentGen}}})
			}
			hooks := newCoherentStrategyHooks(pcs, pcsReplicaInfos, []int{1})

			result, err := hooks.reconcileCurrentlyUpdatingReplicas(pcs.Status.UpdateProgress.CurrentlyUpdating)

			require.NoError(t, err)
			assert.Equal(t, reconcileCurrentlyUpdatingResult{completedPCSReplicaIndices: tc.wantCompleted, pcsReplicaUpdateIndicesToRemove: tc.wantRemoved}, result)
			assert.Equal(t, tc.wantSelections, hooks.selectReplicasToUpdate())
			if tc.hasSelection {
				assert.Nil(t, pcs.Status.UpdateProgress.CurrentlyUpdating[0].UpdateEndedAt, "hooks report completion without stamping it")
				if !tc.isComplete && !tc.isScaledIn {
					assert.Equal(t, []string{"200"}, pcs.Status.UpdateProgress.CurrentlyUpdating[0].InFlightEpochs)
					assert.Equal(t, new("0/1 standalone PodCliques updated to the current revision"), pcs.Status.UpdateProgress.CurrentlyUpdating[0].Message)
				}
			}
		})
	}
}

// coherentTestPCS builds a PodCliqueSet with the given standalone and PodCliqueScalingGroup component
// names in scope for an in-flight coherent update at coherentTestCurrentGen.
func coherentTestPCS(standaloneCliques, pcsgConfigs []string) *grovecorev1beta1.PodCliqueSet {
	builder := testutils.NewPodCliqueSetBuilder(coherentTestPCSName, coherentTestNamespace, coherentTestPCSUID)
	for _, cliqueName := range standaloneCliques {
		builder = builder.WithStandaloneCliqueReplicas(cliqueName, 1)
	}
	for _, pcsgConfig := range pcsgConfigs {
		builder = builder.WithScalingGroupConfig(pcsgConfig, []string{pcsgConfig + "-worker"}, 1, 1)
	}
	pcs := builder.WithPodCliqueSetGenerationHash(new(coherentTestCurrentGen)).
		WithUpdateStrategy(&grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.CoherentStrategy}).Build()
	pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
		InScopeStandalonePodCliques:   standaloneCliques,
		InScopePodCliqueScalingGroups: pcsgConfigs,
	}
	return pcs
}

// coherentHashOf returns the expected pod template hash of a clique from the PodCliqueSet spec.
//
//nolint:unparam // cliqueName is a genuine PCLQ dimension. Current tests only exercise "frontend".
func coherentHashOf(pcs *grovecorev1beta1.PodCliqueSet, cliqueName string) string {
	return componentutils.ComputePCLQPodTemplateHash(componentutils.FindPodCliqueTemplateSpecByName(pcs, cliqueName), pcs.Spec.Template.PriorityClassName)
}

// standalonePCLQAtHash builds a standalone PodClique for replica 0 whose deployed hash and status hashes
// are podTemplateHash. When podTemplateHash equals the clique's expected hash the PodClique reads as
// converged, otherwise it reads as still updating.
func standalonePCLQAtHash(pcs *grovecorev1beta1.PodCliqueSet, cliqueName, podTemplateHash string) grovecorev1beta1.PodClique {
	pclq := testutils.NewPodCliqueBuilder(pcs.Name, coherentTestPCSUID, cliqueName, coherentTestNamespace, 0).
		WithMinAvailable(1).
		WithLabels(map[string]string{apicommon.LabelPodTemplateHash: podTemplateHash}).Build()
	pclq.Status.CurrentPodTemplateHash = new(podTemplateHash)
	pclq.Status.CurrentPodCliqueSetGenerationHash = new(coherentTestCurrentGen)
	pclq.Status.ReadyReplicas = 1
	pclq.Status.UpdatedReplicas = 1
	return *pclq
}

// pcsgAtGenerationHash builds a PodCliqueScalingGroup for replica 0 whose status generation hash is
// currentGenerationHash. Setting it to coherentTestCurrentGen makes it read as converged.
//
//nolint:unparam // pcsgConfigName is a genuine PCSG dimension. Current tests only exercise "decode".
func pcsgAtGenerationHash(pcs *grovecorev1beta1.PodCliqueSet, pcsgConfigName string, currentGenerationHash *string) grovecorev1beta1.PodCliqueScalingGroup {
	return grovecorev1beta1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 0}, pcsgConfigName),
			Namespace: coherentTestNamespace,
		},
		Status: grovecorev1beta1.PodCliqueScalingGroupStatus{CurrentPodCliqueSetGenerationHash: currentGenerationHash},
	}
}

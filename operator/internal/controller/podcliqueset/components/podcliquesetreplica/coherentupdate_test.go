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
	"encoding/json"
	"fmt"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	commonrevision "github.com/ai-dynamo/grove/operator/internal/controller/common/revision"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
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
	revision, err := testutils.NewRevision(pcs)
	require.NoError(t, err)
	replicaInfo := pcsReplicaInfo{
		replicaIndex: 0,
		pclqs: []grovecorev1alpha1.PodClique{
			standalonePCLQAtHash(pcs, "frontend", coherentHashOf(pcs, "frontend")), // converged
			standalonePCLQAtHash(pcs, "router", "stale-hash"),                      // not converged
		},
		pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{
			pcsgAtGenerationHash(pcs, "decode", nil), // not converged
		},
	}

	inScopeStandalone, updatedStandalone, inScopePCSG, updatedPCSG := inScopeUpdateCounts(pcs, revision, replicaInfo)

	assert.Equal(t, 2, inScopeStandalone)
	assert.Equal(t, 1, updatedStandalone)
	assert.Equal(t, 1, inScopePCSG)
	assert.Equal(t, 0, updatedPCSG)
}

func TestCoherentProgressMessage(t *testing.T) {
	t.Run("returns nil when every in-scope component has converged", func(t *testing.T) {
		pcs := coherentTestPCS([]string{"frontend"}, []string{"decode"})
		revision, err := testutils.NewRevision(pcs)
		require.NoError(t, err)
		replicaInfo := pcsReplicaInfo{
			pclqs: []grovecorev1alpha1.PodClique{standalonePCLQAtHash(pcs, "frontend", coherentHashOf(pcs, "frontend"))},
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{pcsgAtGenerationHash(pcs, "decode", new(coherentTestCurrentGen))},
		}
		assert.Nil(t, coherentProgressMessage(pcs, revision, replicaInfo))
	})

	t.Run("summarizes standalone PodCliques and PodCliqueScalingGroups when both are in scope", func(t *testing.T) {
		pcs := coherentTestPCS([]string{"frontend", "router"}, []string{"decode"})
		revision, err := testutils.NewRevision(pcs)
		require.NoError(t, err)
		replicaInfo := pcsReplicaInfo{
			pclqs: []grovecorev1alpha1.PodClique{
				standalonePCLQAtHash(pcs, "frontend", coherentHashOf(pcs, "frontend")),
				standalonePCLQAtHash(pcs, "router", "stale-hash"),
			},
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{pcsgAtGenerationHash(pcs, "decode", nil)},
		}
		require.NotNil(t, coherentProgressMessage(pcs, revision, replicaInfo))
		assert.Equal(t, "1/2 standalone PodCliques and 0/1 PodCliqueScalingGroups updated to the current revision", *coherentProgressMessage(pcs, revision, replicaInfo))
	})

	t.Run("omits the standalone part when only PodCliqueScalingGroups are in scope", func(t *testing.T) {
		pcs := coherentTestPCS(nil, []string{"decode"})
		revision, err := testutils.NewRevision(pcs)
		require.NoError(t, err)
		replicaInfo := pcsReplicaInfo{
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{pcsgAtGenerationHash(pcs, "decode", nil)},
		}
		require.NotNil(t, coherentProgressMessage(pcs, revision, replicaInfo))
		assert.Equal(t, "0/1 PodCliqueScalingGroups updated to the current revision", *coherentProgressMessage(pcs, revision, replicaInfo))
	})
}

func TestInFlightEpochsForReplica(t *testing.T) {
	pcs := coherentTestPCS([]string{"frontend"}, nil)
	currentHashEntry := grovecorev1alpha1.PodGangEntry{Epoch: "200", PodCliqueSetGenerationHash: coherentTestCurrentGen, PodCliques: map[string]int32{"frontend": 1}}
	oldHashEntry := grovecorev1alpha1.PodGangEntry{Epoch: "50", PodCliqueSetGenerationHash: "v1", PodCliques: map[string]int32{"frontend": 1}}

	t.Run("returns the latest current-hash epoch on the PodGangMap", func(t *testing.T) {
		pgm := testutils.NewPodGangMapBuilder(coherentTestPCSName, coherentTestNamespace, coherentTestPCSUID, 0).
			WithEntries(oldHashEntry, currentHashEntry).Build()
		r := _resource{client: testutils.SetupFakeClient(pcs, pgm)}

		epochs, err := r.inFlightEpochsForReplica(context.Background(), pcs, 0)

		require.NoError(t, err)
		assert.Equal(t, []string{"200"}, epochs)
	})

	t.Run("returns empty when the PodGangMap has no current-hash entry", func(t *testing.T) {
		pgm := testutils.NewPodGangMapBuilder(coherentTestPCSName, coherentTestNamespace, coherentTestPCSUID, 0).
			WithEntries(oldHashEntry).Build()
		r := _resource{client: testutils.SetupFakeClient(pcs, pgm)}

		epochs, err := r.inFlightEpochsForReplica(context.Background(), pcs, 0)

		require.NoError(t, err)
		assert.Empty(t, epochs)
	})

	t.Run("returns empty when the PodGangMap does not exist", func(t *testing.T) {
		r := _resource{client: testutils.SetupFakeClient(pcs)}

		epochs, err := r.inFlightEpochsForReplica(context.Background(), pcs, 0)

		require.NoError(t, err)
		assert.Empty(t, epochs)
	})
}

func TestUpdateCoherentReplicaProgress(t *testing.T) {
	pcs := coherentTestPCS([]string{"frontend"}, nil)
	revision, err := testutils.NewRevision(pcs)
	require.NoError(t, err)
	pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}}
	pgm := testutils.NewPodGangMapBuilder(coherentTestPCSName, coherentTestNamespace, coherentTestPCSUID, 0).
		WithEntries(grovecorev1alpha1.PodGangEntry{Epoch: "200", PodCliqueSetGenerationHash: coherentTestCurrentGen, PodCliques: map[string]int32{"frontend": 1}}).Build()
	replicaInfo := pcsReplicaInfo{
		replicaIndex: 0,
		pclqs:        []grovecorev1alpha1.PodClique{standalonePCLQAtHash(pcs, "frontend", "stale-hash")}, // not converged
	}
	r := _resource{client: testutils.SetupFakeClient(pcs, pgm)}

	err = r.updateCoherentReplicaProgress(context.Background(), logr.Discard(), pcs, revision, replicaInfo)

	require.NoError(t, err)
	current := pcs.Status.UpdateProgress.CurrentlyUpdating[0]
	assert.Equal(t, []string{"200"}, current.InFlightEpochs)
	require.NotNil(t, current.Message)
	assert.Equal(t, "0/1 standalone PodCliques updated to the current revision", *current.Message)
}

func TestIsUpdateComplete(t *testing.T) {
	revision, err := testutils.NewRevision(coherentTestPCS([]string{"frontend"}, []string{"decode"}))
	require.NoError(t, err)
	singleGenEntries := []grovecorev1alpha1.PodGangEntry{
		{Epoch: "100", PodCliqueSetGenerationHash: coherentTestCurrentGen},
		{Epoch: "200", PodCliqueSetGenerationHash: coherentTestCurrentGen},
	}
	multiGenEntries := []grovecorev1alpha1.PodGangEntry{
		{Epoch: "50", PodCliqueSetGenerationHash: "v1"}, // a stray older generation left by a cascade
		{Epoch: "200", PodCliqueSetGenerationHash: coherentTestCurrentGen},
	}
	convergedReplicaInfo := func(pcs *grovecorev1alpha1.PodCliqueSet, entries []grovecorev1alpha1.PodGangEntry) pcsReplicaInfo {
		return pcsReplicaInfo{
			replicaIndex: 0,
			pclqs:        []grovecorev1alpha1.PodClique{standalonePCLQAtHash(pcs, "frontend", coherentHashOf(pcs, "frontend"))},
			pcsgs:        []grovecorev1alpha1.PodCliqueScalingGroup{pcsgAtGenerationHash(pcs, "decode", new(coherentTestCurrentGen))},
			pgmEntries:   entries,
		}
	}

	t.Run("coherent update is complete when all components converged and the PodGangMap is single-generation", func(t *testing.T) {
		pcs := coherentTestPCS([]string{"frontend"}, []string{"decode"})
		ri := convergedReplicaInfo(pcs, singleGenEntries)
		assert.True(t, ri.isUpdateComplete(pcs, revision))
	})

	t.Run("coherent update is not complete when the PodGangMap still holds an older generation", func(t *testing.T) {
		pcs := coherentTestPCS([]string{"frontend"}, []string{"decode"})
		ri := convergedReplicaInfo(pcs, multiGenEntries)
		assert.False(t, ri.isUpdateComplete(pcs, revision))
	})

	t.Run("RollingRecreate update does not apply the single-generation gate", func(t *testing.T) {
		pcs := coherentTestPCS([]string{"frontend"}, []string{"decode"})
		pcs.Spec.UpdateStrategy = &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.RollingRecreateStrategy}
		ri := convergedReplicaInfo(pcs, multiGenEntries)
		assert.True(t, ri.isUpdateComplete(pcs, revision))
	})

	t.Run("coherent update with an empty PodGangMap is complete", func(t *testing.T) {
		pcs := coherentTestPCS([]string{"frontend"}, []string{"decode"})
		ri := convergedReplicaInfo(pcs, nil)
		assert.True(t, ri.isUpdateComplete(pcs, revision))
	})

	t.Run("an unconverged standalone PodClique keeps the update incomplete even when the PodGangMap is single-generation", func(t *testing.T) {
		pcs := coherentTestPCS([]string{"frontend"}, []string{"decode"})
		ri := convergedReplicaInfo(pcs, singleGenEntries)
		ri.pclqs = []grovecorev1alpha1.PodClique{standalonePCLQAtHash(pcs, "frontend", "stale-hash")}
		assert.False(t, ri.isUpdateComplete(pcs, revision))
	})

	t.Run("an unconverged PodCliqueScalingGroup keeps the update incomplete even when the PodGangMap is single-generation", func(t *testing.T) {
		pcs := coherentTestPCS([]string{"frontend"}, []string{"decode"})
		ri := convergedReplicaInfo(pcs, singleGenEntries)
		ri.pcsgs = []grovecorev1alpha1.PodCliqueScalingGroup{pcsgAtGenerationHash(pcs, "decode", nil)} // stale generation
		assert.False(t, ri.isUpdateComplete(pcs, revision))
	})
}

func TestIsUpdateCompleteUsesPersistedTemplateIdentity(t *testing.T) {
	pcs := coherentTestPCS([]string{"frontend"}, nil)
	controllerRevision, err := testutils.NewPodCliqueSetControllerRevision(pcs)
	require.NoError(t, err)
	data := commonrevision.Data{}
	require.NoError(t, json.Unmarshal(controllerRevision.Data.Raw, &data))
	data.Cliques[0].Hash = "legacy-frontend"
	controllerRevision.Data.Raw, err = json.Marshal(data)
	require.NoError(t, err)
	revision, err := commonrevision.DecodeRevision(controllerRevision)
	require.NoError(t, err)

	for _, replicas := range []int32{1, 0} {
		t.Run(fmt.Sprintf("replicas=%d", replicas), func(t *testing.T) {
			pclq := standalonePCLQAtHash(pcs, "frontend", "legacy-frontend")
			pclq.Spec.Replicas = replicas
			pclq.Status.ReadyReplicas = replicas
			pclq.Status.UpdatedReplicas = replicas
			info := pcsReplicaInfo{pclqs: []grovecorev1alpha1.PodClique{pclq}}
			assert.True(t, info.isUpdateComplete(pcs, revision))
			assert.Nil(t, coherentProgressMessage(pcs, revision, info))

			info.pclqs[0].Status.CurrentPodTemplateHash = new("stale-hash")
			assert.False(t, info.isUpdateComplete(pcs, revision))
			assert.NotNil(t, coherentProgressMessage(pcs, revision, info))
		})
	}
}

// coherentTestPCS builds a PodCliqueSet with the given standalone and PodCliqueScalingGroup component
// names in scope for an in-flight coherent update at coherentTestCurrentGen.
func coherentTestPCS(standaloneCliques, pcsgConfigs []string) *grovecorev1alpha1.PodCliqueSet {
	builder := testutils.NewPodCliqueSetBuilder(coherentTestPCSName, coherentTestNamespace, coherentTestPCSUID)
	for _, cliqueName := range standaloneCliques {
		builder = builder.WithStandaloneCliqueReplicas(cliqueName, 1)
	}
	for _, pcsgConfig := range pcsgConfigs {
		builder = builder.WithScalingGroupConfig(pcsgConfig, []string{pcsgConfig + "-worker"}, 1, 1)
	}
	pcs := builder.WithPodCliqueSetGenerationHash(new(coherentTestCurrentGen)).
		WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy}).Build()
	pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
		InScopeStandalonePodCliques:   standaloneCliques,
		InScopePodCliqueScalingGroups: pcsgConfigs,
	}
	return pcs
}

// coherentHashOf returns the expected pod template hash of a clique from the PodCliqueSet spec.
//
//nolint:unparam // cliqueName is a genuine PCLQ dimension. Current tests only exercise "frontend".
func coherentHashOf(pcs *grovecorev1alpha1.PodCliqueSet, cliqueName string) string {
	return testutils.ComputePodCliqueTemplateHashes(pcs)[cliqueName]
}

// standalonePCLQAtHash builds a standalone PodClique for replica 0 whose deployed hash and status hashes
// are podTemplateHash. When podTemplateHash equals the clique's expected hash the PodClique reads as
// converged, otherwise it reads as still updating.
func standalonePCLQAtHash(pcs *grovecorev1alpha1.PodCliqueSet, cliqueName, podTemplateHash string) grovecorev1alpha1.PodClique {
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
func pcsgAtGenerationHash(pcs *grovecorev1alpha1.PodCliqueSet, pcsgConfigName string, currentGenerationHash *string) grovecorev1alpha1.PodCliqueScalingGroup {
	return grovecorev1alpha1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 0}, pcsgConfigName),
			Namespace: coherentTestNamespace,
		},
		Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{CurrentPodCliqueSetGenerationHash: currentGenerationHash},
	}
}

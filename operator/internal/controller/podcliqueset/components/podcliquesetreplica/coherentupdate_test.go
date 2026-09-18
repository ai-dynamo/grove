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
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
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
		replicaIndex: 0,
		pclqs: []grovecorev1alpha1.PodClique{
			standalonePCLQAtHash(pcs, "frontend", coherentHashOf(pcs, "frontend")), // converged
			standalonePCLQAtHash(pcs, "router", "stale-hash"),                      // not converged
		},
		pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{
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
			pclqs: []grovecorev1alpha1.PodClique{standalonePCLQAtHash(pcs, "frontend", coherentHashOf(pcs, "frontend"))},
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{pcsgAtGenerationHash(pcs, "decode", ptr.To(coherentTestCurrentGen))},
		}
		assert.Nil(t, coherentProgressMessage(pcs, replicaInfo))
	})

	t.Run("summarizes standalone PodCliques and PodCliqueScalingGroups when both are in scope", func(t *testing.T) {
		pcs := coherentTestPCS([]string{"frontend", "router"}, []string{"decode"})
		replicaInfo := pcsReplicaInfo{
			pclqs: []grovecorev1alpha1.PodClique{
				standalonePCLQAtHash(pcs, "frontend", coherentHashOf(pcs, "frontend")),
				standalonePCLQAtHash(pcs, "router", "stale-hash"),
			},
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{pcsgAtGenerationHash(pcs, "decode", nil)},
		}
		require.NotNil(t, coherentProgressMessage(pcs, replicaInfo))
		assert.Equal(t, "1/2 standalone PodCliques and 0/1 PodCliqueScalingGroups updated to the current revision", *coherentProgressMessage(pcs, replicaInfo))
	})

	t.Run("omits the standalone part when only PodCliqueScalingGroups are in scope", func(t *testing.T) {
		pcs := coherentTestPCS(nil, []string{"decode"})
		replicaInfo := pcsReplicaInfo{
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{pcsgAtGenerationHash(pcs, "decode", nil)},
		}
		require.NotNil(t, coherentProgressMessage(pcs, replicaInfo))
		assert.Equal(t, "0/1 PodCliqueScalingGroups updated to the current revision", *coherentProgressMessage(pcs, replicaInfo))
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
	pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}}
	pgm := testutils.NewPodGangMapBuilder(coherentTestPCSName, coherentTestNamespace, coherentTestPCSUID, 0).
		WithEntries(grovecorev1alpha1.PodGangEntry{Epoch: "200", PodCliqueSetGenerationHash: coherentTestCurrentGen, PodCliques: map[string]int32{"frontend": 1}}).Build()
	replicaInfo := pcsReplicaInfo{
		replicaIndex: 0,
		pclqs:        []grovecorev1alpha1.PodClique{standalonePCLQAtHash(pcs, "frontend", "stale-hash")}, // not converged
	}
	r := _resource{client: testutils.SetupFakeClient(pcs, pgm)}

	err := r.updateCoherentReplicaProgress(context.Background(), logr.Discard(), pcs, replicaInfo)

	require.NoError(t, err)
	current := pcs.Status.UpdateProgress.CurrentlyUpdating[0]
	assert.Equal(t, []string{"200"}, current.InFlightEpochs)
	require.NotNil(t, current.Message)
	assert.Equal(t, "0/1 standalone PodCliques updated to the current revision", *current.Message)
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
	pcs := builder.WithPodCliqueSetGenerationHash(ptr.To(coherentTestCurrentGen)).Build()
	pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
		InScopeStandalonePodCliques:   standaloneCliques,
		InScopePodCliqueScalingGroups: pcsgConfigs,
	}
	return pcs
}

// coherentHashOf returns the expected pod template hash of a clique from the PodCliqueSet spec.
func coherentHashOf(pcs *grovecorev1alpha1.PodCliqueSet, cliqueName string) string {
	return componentutils.ComputePCLQPodTemplateHash(componentutils.FindPodCliqueTemplateSpecByName(pcs, cliqueName), pcs.Spec.Template.PriorityClassName)
}

// standalonePCLQAtHash builds a standalone PodClique for replica 0 whose deployed hash and status hashes
// are podTemplateHash. When podTemplateHash equals the clique's expected hash the PodClique reads as
// converged, otherwise it reads as still updating.
func standalonePCLQAtHash(pcs *grovecorev1alpha1.PodCliqueSet, cliqueName, podTemplateHash string) grovecorev1alpha1.PodClique {
	pclq := testutils.NewPodCliqueBuilder(pcs.Name, coherentTestPCSUID, cliqueName, coherentTestNamespace, 0).
		WithMinAvailable(1).
		WithLabels(map[string]string{apicommon.LabelPodTemplateHash: podTemplateHash}).Build()
	pclq.Status.CurrentPodTemplateHash = ptr.To(podTemplateHash)
	pclq.Status.CurrentPodCliqueSetGenerationHash = ptr.To(coherentTestCurrentGen)
	pclq.Status.ReadyReplicas = 1
	pclq.Status.UpdatedReplicas = 1
	return *pclq
}

// pcsgAtGenerationHash builds a PodCliqueScalingGroup for replica 0 whose status generation hash is
// currentGenerationHash. Setting it to coherentTestCurrentGen makes it read as converged.
//
//nolint:unparam // pcsgConfigName is a genuine PCSG dimension; current tests only exercise "decode".
func pcsgAtGenerationHash(pcs *grovecorev1alpha1.PodCliqueSet, pcsgConfigName string, currentGenerationHash *string) grovecorev1alpha1.PodCliqueScalingGroup {
	return grovecorev1alpha1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 0}, pcsgConfigName),
			Namespace: coherentTestNamespace,
		},
		Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{CurrentPodCliqueSetGenerationHash: currentGenerationHash},
	}
}

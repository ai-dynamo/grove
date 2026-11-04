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
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCurrentlyUpdatingReplicaInfos(t *testing.T) {
	pcs := &grovecorev1beta1.PodCliqueSet{Status: grovecorev1beta1.PodCliqueSetStatus{
		UpdateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{
			CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}, {ReplicaIndex: 2}},
		},
	}}

	assert.Equal(t, []int{0, 2}, replicaIndices(currentlyUpdatingReplicaInfos(pcs, []pcsReplicaInfo{{replicaIndex: 0}, {replicaIndex: 1}, {replicaIndex: 2}})))
	assert.Equal(t, []int{0}, replicaIndices(currentlyUpdatingReplicaInfos(pcs, []pcsReplicaInfo{{replicaIndex: 0}, {replicaIndex: 1}})))
	assert.Nil(t, currentlyUpdatingReplicaInfos(&grovecorev1beta1.PodCliqueSet{}, []pcsReplicaInfo{{replicaIndex: 0}}))
}

func TestPCSReplicaUpdateSelectionBudget(t *testing.T) {
	pcs := &grovecorev1beta1.PodCliqueSet{}

	assert.Equal(t, 1, pcsReplicaUpdateSelectionBudget(pcs, nil))
	assert.Equal(t, 0, pcsReplicaUpdateSelectionBudget(pcs, []pcsReplicaInfo{{replicaIndex: 0}}))
	assert.Equal(t, 0, pcsReplicaUpdateSelectionBudget(pcs, []pcsReplicaInfo{{replicaIndex: 0}, {replicaIndex: 1}}))
}

func TestSelectReplicasToUpdate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-uid")).
		WithStandaloneClique("worker").
		WithPodCliqueSetGenerationHash(new("generation")).
		Build()
	replicaInfos := []pcsReplicaInfo{{replicaIndex: 0}, {replicaIndex: 1}, {replicaIndex: 2}}

	t.Run("retains updating replicas and fills the shortage", func(t *testing.T) {
		selected := selectReplicasToUpdate(pcs, replicaInfos, []pcsReplicaInfo{{replicaIndex: 1}}, nil, 1)
		assert.Equal(t, []int{1, 0}, replicaIndices(selected))
	})

	t.Run("retains every updating replica when no slot remains", func(t *testing.T) {
		selected := selectReplicasToUpdate(pcs, replicaInfos, []pcsReplicaInfo{{replicaIndex: 0}, {replicaIndex: 1}}, nil, 0)
		assert.Equal(t, []int{0, 1}, replicaIndices(selected))
	})

	t.Run("fills multiple synthetic concurrency slots", func(t *testing.T) {
		selected := selectReplicasToUpdate(pcs, replicaInfos, nil, nil, 2)
		assert.Equal(t, []int{0, 1}, replicaIndices(selected))
	})

	t.Run("excludes completed replicas", func(t *testing.T) {
		complete := rollingUpdateReplicaInfo(pcs, 0, true, 1)
		pending := rollingUpdateReplicaInfo(pcs, 1, false, 1)
		selected := selectReplicasToUpdate(pcs, []pcsReplicaInfo{complete, pending}, nil, nil, 1)
		assert.Equal(t, []int{1}, replicaIndices(selected))
	})
}

func TestSelectReplicasToUpdateOrdering(t *testing.T) {
	pcs := rollingUpdateTestPCS()
	replicaInfos := []pcsReplicaInfo{
		rollingUpdateReplicaInfo(pcs, 3, false, 1),
		rollingUpdateReplicaInfo(pcs, 2, false, 1),
		rollingUpdateReplicaInfo(pcs, 1, false, 0),
		rollingUpdateReplicaInfo(pcs, 0, false, 1),
	}

	selected := selectReplicasToUpdate(pcs, replicaInfos, nil, []int{2, 3}, len(replicaInfos))

	assert.Equal(t, []int{1, 2, 3, 0}, replicaIndices(selected))
}

func TestOrchestrateRollingUpdate(t *testing.T) {
	t.Run("retains an incomplete active replica", func(t *testing.T) {
		pcs := rollingUpdateTestPCS()
		pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 1, UpdateStartedAt: metav1.Now()}}
		replicaInfos := []pcsReplicaInfo{
			rollingUpdateReplicaInfo(pcs, 0, false, 1),
			rollingUpdateReplicaInfo(pcs, 1, false, 1),
		}
		r, persisted := rollingUpdateTestResource(t, pcs)

		err := r.orchestrateRollingUpdate(context.Background(), logr.Discard(), pcs, replicaInfos, nil)

		require.Error(t, err)
		assert.True(t, groveerr.IsContinueReconcileAndRequeue(err))
		progress := persisted()
		require.Len(t, progress.CurrentlyUpdating, 1)
		assert.Equal(t, int32(1), progress.CurrentlyUpdating[0].ReplicaIndex)
	})

	t.Run("marks a completed replica and selects the next pending replica", func(t *testing.T) {
		pcs := rollingUpdateTestPCS()
		pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0, UpdateStartedAt: metav1.Now()}}
		replicaInfos := []pcsReplicaInfo{
			rollingUpdateReplicaInfo(pcs, 0, true, 1),
			rollingUpdateReplicaInfo(pcs, 1, false, 1),
		}
		r, persisted := rollingUpdateTestResource(t, pcs)

		err := r.orchestrateRollingUpdate(context.Background(), logr.Discard(), pcs, replicaInfos, nil)

		require.Error(t, err)
		assert.True(t, groveerr.IsContinueReconcileAndRequeue(err))
		progress := persisted()
		require.Len(t, progress.CurrentlyUpdating, 1)
		assert.Equal(t, int32(1), progress.CurrentlyUpdating[0].ReplicaIndex)
		assert.Nil(t, progress.CurrentlyUpdating[0].UpdateEndedAt)
	})

	t.Run("reuses selection budget released by scale-in", func(t *testing.T) {
		pcs := rollingUpdateTestPCS()
		pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 2, UpdateStartedAt: metav1.Now()}}
		replicaInfos := []pcsReplicaInfo{
			rollingUpdateReplicaInfo(pcs, 0, false, 1),
			rollingUpdateReplicaInfo(pcs, 1, false, 1),
		}
		r, persisted := rollingUpdateTestResource(t, pcs)

		err := r.orchestrateRollingUpdate(context.Background(), logr.Discard(), pcs, replicaInfos, nil)

		require.Error(t, err)
		assert.True(t, groveerr.IsContinueReconcileAndRequeue(err))
		progress := persisted()
		require.Len(t, progress.CurrentlyUpdating, 1)
		assert.Equal(t, int32(0), progress.CurrentlyUpdating[0].ReplicaIndex)
	})

	t.Run("retains multiple active replicas without selecting more", func(t *testing.T) {
		pcs := rollingUpdateTestPCS()
		pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
			{ReplicaIndex: 0, UpdateStartedAt: metav1.Now()},
			{ReplicaIndex: 1, UpdateStartedAt: metav1.Now()},
		}
		replicaInfos := []pcsReplicaInfo{
			rollingUpdateReplicaInfo(pcs, 0, false, 1),
			rollingUpdateReplicaInfo(pcs, 1, false, 1),
			rollingUpdateReplicaInfo(pcs, 2, false, 1),
		}
		r, persisted := rollingUpdateTestResource(t, pcs)

		err := r.orchestrateRollingUpdate(context.Background(), logr.Discard(), pcs, replicaInfos, nil)

		require.Error(t, err)
		assert.True(t, groveerr.IsContinueReconcileAndRequeue(err))
		assert.Equal(t, []int32{0, 1}, updateProgressReplicaIndices(persisted()))
	})

	t.Run("completes when no selected or pending work remains", func(t *testing.T) {
		pcs := rollingUpdateTestPCS()
		pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0, UpdateStartedAt: metav1.Now()}}
		replicaInfos := []pcsReplicaInfo{rollingUpdateReplicaInfo(pcs, 0, true, 1)}
		r, persisted := rollingUpdateTestResource(t, pcs)

		err := r.orchestrateRollingUpdate(context.Background(), logr.Discard(), pcs, replicaInfos, nil)

		require.NoError(t, err)
		progress := persisted()
		assert.Empty(t, progress.CurrentlyUpdating)
		assert.NotNil(t, progress.UpdateEndedAt)
	})
}

func TestMarkUpdateEndForReplicas(t *testing.T) {
	pcs := updateTestPCS()
	pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}, {ReplicaIndex: 1}}
	fakeClient := testutils.NewTestClientBuilder().WithObjects(pcs).WithStatusSubresource(pcs).Build()
	r := &_resource{client: fakeClient}

	err := r.markUpdateEndForReplicas(context.Background(), logr.Discard(), pcs, []pcsReplicaInfo{{replicaIndex: 1}})

	require.NoError(t, err)
	persisted := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), persisted))
	assert.Nil(t, persisted.Status.UpdateProgress.CurrentlyUpdating[0].UpdateEndedAt)
	assert.NotNil(t, persisted.Status.UpdateProgress.CurrentlyUpdating[1].UpdateEndedAt)
}

func TestUpdatePCSWithSelectedReplicas(t *testing.T) {
	pcs := updateTestPCS()
	startedAt := metav1.NewTime(time.Now().Add(-time.Minute))
	pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
		ReplicaIndex:    1,
		UpdateStartedAt: startedAt,
		InFlightEpochs:  []string{"epoch"},
	}}
	fakeClient := testutils.NewTestClientBuilder().WithObjects(pcs).WithStatusSubresource(pcs).Build()
	r := &_resource{client: fakeClient}

	err := r.updatePCSWithSelectedReplicas(context.Background(), logr.Discard(), pcs, []pcsReplicaInfo{{replicaIndex: 1}, {replicaIndex: 0}})

	require.NoError(t, err)
	persisted := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), persisted))
	require.Len(t, persisted.Status.UpdateProgress.CurrentlyUpdating, 2)
	assert.Equal(t, int32(0), persisted.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex)
	assert.Equal(t, int32(1), persisted.Status.UpdateProgress.CurrentlyUpdating[1].ReplicaIndex)
	assert.Equal(t, startedAt.Unix(), persisted.Status.UpdateProgress.CurrentlyUpdating[1].UpdateStartedAt.Unix())
	assert.Equal(t, []string{"epoch"}, persisted.Status.UpdateProgress.CurrentlyUpdating[1].InFlightEpochs)
}

func TestUpdatePCSWithSelectedReplicasIsIdempotent(t *testing.T) {
	pcs := updateTestPCS()
	pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0, UpdateStartedAt: metav1.Now()}}
	r := &_resource{client: nil}

	err := r.updatePCSWithSelectedReplicas(context.Background(), logr.Discard(), pcs, []pcsReplicaInfo{{replicaIndex: 0}})

	require.NoError(t, err)
}

func TestUpdatePCSWithSelectedReplicasCompletesUpdate(t *testing.T) {
	pcs := updateTestPCS()
	pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}}
	fakeClient := testutils.NewTestClientBuilder().WithObjects(pcs).WithStatusSubresource(pcs).Build()
	r := &_resource{client: fakeClient}

	err := r.updatePCSWithSelectedReplicas(context.Background(), logr.Discard(), pcs, nil)

	require.NoError(t, err)
	persisted := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), persisted))
	assert.Empty(t, persisted.Status.UpdateProgress.CurrentlyUpdating)
	assert.NotNil(t, persisted.Status.UpdateProgress.UpdateEndedAt)
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

func rollingUpdateReplicaInfo(pcs *grovecorev1beta1.PodCliqueSet, replicaIndex int, complete bool, scheduledReplicas int32) pcsReplicaInfo {
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
	pclq.Status.CurrentPodTemplateHash = new(podTemplateHash)
	pclq.Status.CurrentPodCliqueSetGenerationHash = new(generationHash)
	pclq.Status.UpdatedReplicas = 1
	pclq.Status.ReadyReplicas = 1
	pclq.Status.ScheduledReplicas = scheduledReplicas
	return pcsReplicaInfo{replicaIndex: replicaIndex, pclqs: []grovecorev1beta1.PodClique{*pclq}}
}

func rollingUpdateTestResource(t *testing.T, pcs *grovecorev1beta1.PodCliqueSet) (*_resource, func() *grovecorev1beta1.PodCliqueSetUpdateProgress) {
	t.Helper()
	fakeClient := testutils.NewTestClientBuilder().WithObjects(pcs).WithStatusSubresource(pcs).Build()
	persisted := func() *grovecorev1beta1.PodCliqueSetUpdateProgress {
		t.Helper()
		got := &grovecorev1beta1.PodCliqueSet{}
		require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), got))
		return got.Status.UpdateProgress
	}
	return &_resource{client: fakeClient}, persisted
}

func updateProgressReplicaIndices(progress *grovecorev1beta1.PodCliqueSetUpdateProgress) []int32 {
	indices := make([]int32, len(progress.CurrentlyUpdating))
	for i := range progress.CurrentlyUpdating {
		indices[i] = progress.CurrentlyUpdating[i].ReplicaIndex
	}
	return indices
}

func updateTestPCS() *grovecorev1beta1.PodCliqueSet {
	return &grovecorev1beta1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pcs", Namespace: "default", UID: types.UID("test-uid")},
		Status: grovecorev1beta1.PodCliqueSetStatus{
			UpdateProgress: &grovecorev1beta1.PodCliqueSetUpdateProgress{UpdateStartedAt: metav1.Now()},
		},
	}
}

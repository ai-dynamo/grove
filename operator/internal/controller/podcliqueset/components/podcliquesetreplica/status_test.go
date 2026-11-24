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
	pcsexpectations "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/expectations"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestPatchPCSScalingAndUpdateProgressNoOp(t *testing.T) {
	t.Parallel()
	pcs := generationTestPCS()
	pcs.Status.CurrentGenerationHash = new("current")
	pcs.Status.AppliedSchedulingConfigurationHash = new("scheduling")
	generationExpectations := &pcsexpectations.GenerationHashes{}
	generationExpectations.Record(pcs.ObjectMeta, "pending", new("pending-scheduling"))
	r := &_resource{generationHashExpectations: generationExpectations}
	before := pcs.DeepCopy()

	changed, err := r.patchPCSScalingAndUpdateProgress(t.Context(), pcs, pcs.DeepCopy())

	require.NoError(t, err)
	assert.False(t, changed, "equal status must not need a client or record expectations")
	assert.Equal(t, before, pcs)
	assert.False(t, generationExpectations.ClearIfSatisfied(pcs.ObjectMeta, pcs.Status.CurrentGenerationHash, pcs.Status.AppliedSchedulingConfigurationHash))
	assert.True(t, generationExpectations.ClearIfSatisfied(pcs.ObjectMeta, new("pending"), new("pending-scheduling")))
}

func TestPatchPCSScalingAndUpdateProgressConflictPreservesExpectations(t *testing.T) {
	t.Parallel()
	persisted := generationTestPCS()
	persisted.ResourceVersion = "2"
	persisted.Status.CurrentGenerationHash = new("current")
	persisted.Status.AppliedSchedulingConfigurationHash = new("scheduling")
	cl := testutils.SetupFakeClient(persisted)
	pcs := persisted.DeepCopy()
	pcs.ResourceVersion = "1"
	before := pcs.DeepCopy()
	working := pcs.DeepCopy()
	working.Status.CurrentGenerationHash = new("new-generation")
	working.Status.ScalingProgress = &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleOutReplicaIndices: []int32{1}}
	working.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 1}}}
	r := &_resource{client: cl, generationHashExpectations: &pcsexpectations.GenerationHashes{},
		scalingProgressExpectations: &pcsexpectations.ScalingProgress{}, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
	previousScaling := &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleOutReplicaIndices: []int32{9}}
	previousSelections := []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 9}}
	r.generationHashExpectations.Record(pcs.ObjectMeta, "pending", new("pending-scheduling"))
	r.scalingProgressExpectations.Record(pcs.ObjectMeta, previousScaling)
	r.currentlyUpdatingExpectations.Record(pcs.ObjectMeta, previousSelections)

	changed, err := r.patchPCSScalingAndUpdateProgress(t.Context(), pcs, working)

	var groveError *groveerr.GroveError
	require.ErrorAs(t, err, &groveError)
	assert.True(t, apierrors.IsConflict(groveError.Cause))
	assert.False(t, changed)
	assert.Equal(t, before, pcs, "a conflict must not publish the rejected working copy")
	assert.False(t, r.generationHashExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil, nil))
	assert.True(t, r.generationHashExpectations.ClearIfSatisfied(pcs.ObjectMeta, new("pending"), new("pending-scheduling")))
	assert.False(t, r.scalingProgressExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil))
	assert.True(t, r.scalingProgressExpectations.ClearIfSatisfied(pcs.ObjectMeta, previousScaling))
	assert.False(t, r.currentlyUpdatingExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil))
	assert.True(t, r.currentlyUpdatingExpectations.ClearIfSatisfied(pcs.ObjectMeta, previousSelections))
	stored := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pcs), stored))
	assert.Equal(t, persisted.Status, stored.Status)
}

func TestPatchPCSScalingAndUpdateProgressPublishesPersistedStatus(t *testing.T) {
	t.Parallel()
	pcs := generationTestPCS()
	pcs.ResourceVersion = "1"
	pcs.Status.CurrentGenerationHash = new("current")
	pcs.Status.AppliedSchedulingConfigurationHash = new("scheduling")
	cl := testutils.SetupFakeClient(pcs)
	r := &_resource{client: cl, generationHashExpectations: &pcsexpectations.GenerationHashes{},
		scalingProgressExpectations: &pcsexpectations.ScalingProgress{}, currentlyUpdatingExpectations: &pcsexpectations.CurrentlyUpdating{}}
	working := pcs.DeepCopy()
	working.Status.CurrentGenerationHash = new("new-generation")
	working.Status.ScalingProgress = &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleOutReplicaIndices: []int32{1}}
	working.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 1}}}

	changed, err := r.patchPCSScalingAndUpdateProgress(t.Context(), pcs, working)

	require.NoError(t, err)
	assert.True(t, changed)
	stored := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pcs), stored))
	assert.Equal(t, stored.Status, pcs.Status)
	assert.Equal(t, stored.ResourceVersion, pcs.ResourceVersion)
	assert.NotEqual(t, "1", pcs.ResourceVersion)
	*working.Status.CurrentGenerationHash = "mutated"
	working.Status.ScalingProgress.ScaleOutReplicaIndices[0] = 7
	working.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex = 7
	assert.Equal(t, stored.Status, pcs.Status, "published status must not alias the working copy")
	assert.False(t, r.generationHashExpectations.ClearIfSatisfied(pcs.ObjectMeta, new("current"), new("scheduling")))
	assert.True(t, r.generationHashExpectations.ClearIfSatisfied(pcs.ObjectMeta, stored.Status.CurrentGenerationHash, stored.Status.AppliedSchedulingConfigurationHash))
	assert.False(t, r.scalingProgressExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil))
	assert.True(t, r.scalingProgressExpectations.ClearIfSatisfied(pcs.ObjectMeta, stored.Status.ScalingProgress))
	assert.False(t, r.currentlyUpdatingExpectations.ClearIfSatisfied(pcs.ObjectMeta, nil))
	assert.True(t, r.currentlyUpdatingExpectations.ClearIfSatisfied(pcs.ObjectMeta, stored.Status.UpdateProgress.CurrentlyUpdating))
}

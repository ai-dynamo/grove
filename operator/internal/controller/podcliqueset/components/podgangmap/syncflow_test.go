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

package podgangmap

import (
	"testing"
	"time"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/sync"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestRunSyncFlowReplanContinuesIndependentReplicaWork(t *testing.T) {
	t.Parallel()
	pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
		WithPodCliqueSetGenerationHash(new("v2")).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("worker").WithReplicas(1).WithMinAvailable(1).Build()).
		WithPodCliqueScalingGroupConfig(grovecorev1beta1.PodCliqueScalingGroupConfig{Name: "decode", CliqueNames: []string{"worker"}, Replicas: new(int32(3)), MinAvailable: new(int32(2))}).
		WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
			InScopePodCliqueScalingGroups: []string{"decode"},
			CurrentlyUpdating:             []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}},
		}).Build()
	pcs.Spec.UpdateStrategy = &grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.CoherentStrategy}
	pgm := testutils.NewPodGangMapBuilder(pcs.Name, pcs.Namespace, pcs.UID, 0).WithEntries(grovecorev1beta1.PodGangEntry{
		Epoch: "100", Role: grovecorev1beta1.PodGangEntryRoleAnchor, PodCliqueSetGenerationHash: "v1", PCSGReplicaIndices: map[string][]int32{"decode": {0, 1, 2}},
	}).Build()
	orphan := testutils.NewPodGangMapBuilder(pcs.Name, pcs.Namespace, pcs.UID, 2).Build()
	cl := testutils.NewTestClientBuilder().WithObjects(pcs, pgm, orphan).Build()
	r := _resource{client: cl, scheme: cl.Scheme(), clk: clocktesting.NewFakeClock(time.Unix(1000, 0).UTC())}
	snapshot := &syncSnapshot{
		logger: logr.Discard(), pcs: pcs, expectedPCSReplicaIndices: []int{0, 1}, scaleInPCSReplicaIndices: []int{2},
		existingPGMsByPCSReplicaIndex: map[int]*grovecorev1beta1.PodGangMap{0: pgm, 2: orphan},
		mvuTemplate:                   &mvuTemplate{pcsgs: map[string]int32{"decode": 2}},
	}
	before := pgm.DeepCopy()

	result, err := r.runSyncFlow(t.Context(), snapshot)

	require.NoError(t, err)
	assert.Equal(t, pcssync.Result{RequeueAfter: new(constants.ComponentSyncRetryInterval), StopAndRequeue: true}, result)
	unchanged := &grovecorev1beta1.PodGangMap{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pgm), unchanged))
	assert.Equal(t, before.Spec, unchanged.Spec, "insufficient old inventory must not emit a short anchor")
	created := &grovecorev1beta1.PodGangMap{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKey{Namespace: pcs.Namespace, Name: "pcs-1"}, created))
	assert.NotEmpty(t, created.Spec.Entries, "another replica still bootstraps")
	assert.True(t, apierrors.IsNotFound(cl.Get(t.Context(), client.ObjectKeyFromObject(orphan), &grovecorev1beta1.PodGangMap{})))
}

func TestComputeMVUTemplate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("fe").WithReplicas(5).WithMinAvailable(2).Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("other").WithReplicas(4).WithMinAvailable(1).Build()).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("pf-leader").WithReplicas(1).WithMinAvailable(1).Build()).
		WithPodCliqueScalingGroupConfig(grovecorev1beta1.PodCliqueScalingGroupConfig{
			Name:         "pf",
			CliqueNames:  []string{"pf-leader"},
			MinAvailable: ptr.To[int32](3),
		}).
		WithUpdateProgress(&grovecorev1beta1.PodCliqueSetUpdateProgress{
			InScopeStandalonePodCliques:   []string{"fe"},
			InScopePodCliqueScalingGroups: []string{"pf"},
		}).
		Build()

	mvuTmpl := computeMVUTemplate(pcs)
	assert.Equal(t, map[string]int32{"fe": 2}, mvuTmpl.standalonePCLQs)
	assert.Equal(t, map[string]int32{"pf": 3}, mvuTmpl.pcsgs)
}

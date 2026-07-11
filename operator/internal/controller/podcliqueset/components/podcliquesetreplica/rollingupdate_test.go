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
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestUpdatePCSWithNextSelectedReplica(t *testing.T) {
	tests := []struct {
		name               string
		nextReplica        *int
		wantCurrentReplica *int32
		wantCompleted      bool
	}{
		{
			name:               "starts selected replica",
			nextReplica:        ptr.To(2),
			wantCurrentReplica: ptr.To(int32(2)),
		},
		{
			name:          "completes without pending replica",
			wantCompleted: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs := &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
						UpdateStartedAt: metav1.Now(),
						CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{
							ReplicaIndex:    1,
							UpdateStartedAt: metav1.Now(),
						}},
					},
				},
			}
			fakeClient := testutils.SetupFakeClient(pcs)
			r := _resource{client: fakeClient}

			require.NoError(t, r.updatePCSWithNextSelectedReplica(context.Background(), logr.Discard(), pcs, tt.nextReplica))

			updatedPCS := &grovecorev1alpha1.PodCliqueSet{}
			require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), updatedPCS))
			require.NotNil(t, updatedPCS.Status.UpdateProgress)
			assert.Equal(t, tt.wantCompleted, updatedPCS.Status.UpdateProgress.UpdateEndedAt != nil)
			if tt.wantCurrentReplica == nil {
				assert.Empty(t, updatedPCS.Status.UpdateProgress.CurrentlyUpdating)
				return
			}
			require.Len(t, updatedPCS.Status.UpdateProgress.CurrentlyUpdating, 1)
			assert.Equal(t, *tt.wantCurrentReplica, updatedPCS.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex)
		})
	}
}

// A replica selected for gang termination is excluded from pending update
// work. Once every remaining replica is updated, orchestration must close the
// rollout and clear the stale current-replica entry.
func TestOrchestrateRollingUpdateCompletesWhenGangTerminationExcludesReplica(t *testing.T) {
	const (
		pcsName   = "test"
		namespace = "default"
	)
	pcsUID := uuid.NewUUID()
	pcs := testutils.NewPodCliqueSetBuilder(pcsName, namespace, pcsUID).
		WithReplicas(2).
		WithStandaloneClique("worker").
		Build()
	revision, err := testutils.NewPodCliqueSetControllerRevision(pcs)
	require.NoError(t, err)

	finishedAt := metav1.Now()
	pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
		UpdateStartedAt: metav1.Now(),
		CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{
			ReplicaIndex:    0,
			UpdateStartedAt: metav1.Now(),
			UpdateEndedAt:   &finishedAt,
		}},
	}

	podTemplateHash := testutils.ComputePodCliqueTemplateHashes(pcs)["worker"]
	converged := testutils.NewPodCliqueBuilder(pcsName, pcsUID, "worker", namespace, 0).Build()
	converged.Labels[apicommon.LabelPodTemplateHash] = podTemplateHash
	converged.Status.CurrentPodTemplateHash = ptr.To(podTemplateHash)
	converged.Status.CurrentPodCliqueSetGenerationHash = pcs.Status.CurrentGenerationHash
	converged.Status.UpdatedReplicas = 1
	converged.Status.ReadyReplicas = 1

	terminating := testutils.NewPodCliqueBuilder(pcsName, pcsUID, "worker", namespace, 1).
		WithOptions(testutils.WithPCLQTerminating()).
		Build()

	fakeClient := testutils.SetupFakeClient(pcs, revision, converged, terminating)
	r := _resource{client: fakeClient}
	require.NoError(t, r.orchestrateRollingUpdate(context.Background(), logr.Discard(), pcs, []int{1}, nil))

	updatedPCS := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), updatedPCS))
	require.NotNil(t, updatedPCS.Status.UpdateProgress)
	assert.NotNil(t, updatedPCS.Status.UpdateProgress.UpdateEndedAt)
	assert.Empty(t, updatedPCS.Status.UpdateProgress.CurrentlyUpdating)
}

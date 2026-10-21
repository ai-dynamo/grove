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

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestReplicaRevisionAdvancesWhenReplicaUpdateCompletes(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test", "default", uuid.NewUUID()).WithReplicas(2).Build()
	pcs.Status.CurrentRevision = "revision-a"
	pcs.Status.UpdateRevision = "revision-b"
	pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
		UpdateStartedAt: metav1.Now(),
		CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{
			ReplicaIndex: 1, UpdateStartedAt: metav1.Now(),
		}},
		ReplicaRevisions: []grovecorev1alpha1.PodCliqueSetReplicaRevision{
			{ReplicaIndex: 0, Revision: "revision-a"},
			{ReplicaIndex: 1, Revision: "revision-a"},
		},
	}
	cl := testutils.SetupFakeClient(pcs)
	r := _resource{client: cl}

	require.NoError(t, r.markCurrentReplicaUpdateEnded(t.Context(), logr.Discard(), pcs))
	updated := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pcs), updated))
	assert.Equal(t, "revision-a", updated.Status.UpdateProgress.ReplicaRevisions[0].Revision)
	assert.Equal(t, "revision-b", updated.Status.UpdateProgress.ReplicaRevisions[1].Revision)
	require.NotNil(t, updated.Status.UpdateProgress.CurrentlyUpdating[0].UpdateEndedAt)

	require.NoError(t, r.updatePCSWithNextSelectedReplica(t.Context(), logr.Discard(), updated, nil))
	completed := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pcs), completed))
	assert.Equal(t, "revision-b", completed.Status.CurrentRevision)
	require.NotNil(t, completed.Status.UpdateProgress.UpdateEndedAt)
}

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

package controllerrevision

import (
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/uuid"
)

func TestResolveForReplica(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test", "default", uuid.NewUUID()).
		WithPodCliqueParameters("worker", 1, nil).
		Build()
	data, err := EncodeTemplate(&pcs.Spec.Template)
	require.NoError(t, err)
	revision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-revision",
			Namespace: pcs.Namespace,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(pcs, schema.GroupVersionKind{
				Group: grovecorev1alpha1.SchemeGroupVersion.Group, Version: grovecorev1alpha1.SchemeGroupVersion.Version, Kind: "PodCliqueSet",
			})},
		},
		Data: runtime.RawExtension{Raw: data},
	}
	pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
		ReplicaRevisions: []grovecorev1alpha1.PodCliqueSetReplicaRevision{{ReplicaIndex: 0, Revision: revision.Name}},
	}
	pcs.Status.UpdateRevision = revision.Name
	cl := testutils.SetupFakeClient(pcs, revision)

	pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}}
	pcs.Status.UpdateProgress.ReplicaRevisions[0].Revision = "old-revision"
	resolved, err := ResolveForReplica(t.Context(), cl, pcs, 0)
	require.NoError(t, err)
	assert.Equal(t, revision.Name, resolved.Name)
	assert.Equal(t, pcs.Spec.Template, *resolved.Template)

	pcs.Status.UpdateProgress.CurrentlyUpdating = nil
	pcs.Status.UpdateProgress.ReplicaRevisions[0].Revision = revision.Name
	resolved, err = ResolveForReplica(t.Context(), cl, pcs, 0)
	require.NoError(t, err)
	assert.Equal(t, revision.Name, resolved.Name)

	_, err = ResolveForReplica(t.Context(), cl, pcs, 1)
	require.ErrorContains(t, err, "no ControllerRevision binding")

	pcs.Status.UpdateProgress.ReplicaRevisions = []grovecorev1alpha1.PodCliqueSetReplicaRevision{{ReplicaIndex: 1, Revision: "missing-revision"}}
	_, err = ResolveForReplica(t.Context(), cl, pcs, 1)
	require.ErrorContains(t, err, "could not get ControllerRevision")

	pcs.Status.UpdateProgress = nil
	resolved, err = ResolveForReplica(t.Context(), cl, pcs, 1)
	require.NoError(t, err)
	assert.Equal(t, revision.Name, resolved.Name)

	now := metav1.Now()
	pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{UpdateEndedAt: &now}
	resolved, err = ResolveForReplica(t.Context(), cl, pcs, 1)
	require.NoError(t, err)
	assert.Equal(t, revision.Name, resolved.Name)
}

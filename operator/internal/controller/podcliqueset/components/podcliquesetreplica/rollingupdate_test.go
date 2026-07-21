// /*
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
// */

package podcliquesetreplica

import (
	"context"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestStartPCSReplicaUpdate(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Status: grovecorev1alpha1.PodCliqueSetStatus{
			UpdateProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{UpdateStartedAt: metav1.Now()},
		},
	}
	fakeClient := testutils.SetupFakeClient(pcs)
	r := _resource{client: fakeClient}

	require.NoError(t, r.startPCSReplicaUpdate(context.Background(), logr.Discard(), pcs, 2))
	require.Len(t, pcs.Status.UpdateProgress.CurrentlyUpdating, 1)
	assert.Equal(t, int32(2), pcs.Status.UpdateProgress.CurrentlyUpdating[0].ReplicaIndex)
	assert.Nil(t, pcs.Status.UpdateProgress.UpdateEndedAt)
}

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

package podclique

import (
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPrepareSyncContextWaitsForPCSGenerationInitialization(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", "pcs-uid").WithPodCliqueSetGenerationHash(nil).Build()
	pcsg := testutils.NewPodCliqueScalingGroupBuilder("pcs-0-sg", "default", pcs.Name, 0).WithOwnerReference("PodCliqueSet", pcs.Name, pcs.UID).WithOptions(func(group *grovecorev1beta1.PodCliqueScalingGroup) { group.UID = "group-uid" }).Build()
	cl := testutils.NewTestClientBuilder().WithObjects(pcs, pcsg).Build()
	r := _resource{client: cl, scheme: groveclientscheme.Scheme}
	snapshot, err := r.prepareSyncContext(t.Context(), pcsg)
	require.Error(t, err)
	assert.True(t, groveerr.IsRequeueAfter(err), "initialization must request a control-flow requeue, not a failure")
	assert.Nil(t, snapshot)
}

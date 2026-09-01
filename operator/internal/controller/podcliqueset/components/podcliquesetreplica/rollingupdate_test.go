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

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
)

const (
	testPCSName      = "test-pcs"
	testPCSNamespace = "default"
)

// TestIsPCLQUpdateCompleteIdle verifies GREP-0677: a RollingRecreate completes for an idle
// standalone PodClique (replicas: 0) once its hashes converge, and does not stall waiting for
// UpdatedReplicas/ReadyReplicas >= MinAvailable (which zero pods can never satisfy).
func TestIsPCLQUpdateCompleteIdle(t *testing.T) {
	hash := "h"
	pcsUID := uuid.NewUUID()
	pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testPCSNamespace, pcsUID).
		WithStandaloneClique("worker").
		WithPodCliqueSetGenerationHash(&hash).
		Build()

	pclq := testutils.NewPodCliqueBuilder(testPCSName, pcsUID, "worker", testPCSNamespace, 0).Build()
	expectedTemplateHash, err := componentutils.GetExpectedPCLQPodTemplateHash(pcs, pclq.ObjectMeta)
	require.NoError(t, err)
	if pclq.Labels == nil {
		pclq.Labels = map[string]string{}
	}
	pclq.Labels[apicommon.LabelPodTemplateHash] = expectedTemplateHash
	pclq.Status.CurrentPodTemplateHash = ptr.To(expectedTemplateHash)
	pclq.Status.CurrentPodCliqueSetGenerationHash = ptr.To(hash)
	// Idle: no pods, so ready/updated stay at zero.
	pclq.Spec.Replicas = 0
	pclq.Status.ReadyReplicas = 0
	pclq.Status.UpdatedReplicas = 0

	assert.True(t, isPCLQUpdateComplete(pcs, pclq), "idle PCLQ with converged hashes must be update-complete")

	stale := pclq.DeepCopy()
	stale.Status.CurrentPodCliqueSetGenerationHash = ptr.To("old")
	assert.False(t, isPCLQUpdateComplete(pcs, stale), "idle PCLQ with stale generation hash must not be update-complete")
}

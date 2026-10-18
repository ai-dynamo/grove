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

package pod

import (
	"context"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestMarkCoherentUpdateEndIfConverged(t *testing.T) {
	testCases := []struct {
		description     string
		specReplicas    int32
		statusReplicas  int32
		updatedReplicas int32
		wantUpdateEnded bool
	}{
		{"not all pods exist yet", 3, 2, 2, false},
		{"all pods exist but not all at the new hash", 3, 3, 2, false},
		{"all pods exist and all at the new hash", 3, 3, 3, true},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			const generation = "generation-1"
			pclq := testutils.NewPodCliqueBuilder(testPCSName, "uid", testCliqueName, testNamespace, 0).
				WithReplicas(tc.specReplicas).
				WithOptions(func(p *grovecorev1alpha1.PodClique) {
					p.Status.Replicas = tc.statusReplicas
					p.Status.UpdatedReplicas = tc.updatedReplicas
					p.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueUpdateProgress{
						PodCliqueSetGenerationHash: generation,
						PodTemplateHash:            testNewHash,
					}
				}).Build()
			cl := testutils.NewTestClientBuilder().
				WithObjects(pclq).
				WithStatusSubresource(&grovecorev1alpha1.PodClique{}).
				Build()
			r := _resource{client: cl}
			ss := &syncSnapshot{pclq: pclq}

			require.NoError(t, r.markCoherentUpdateEndIfConverged(context.Background(), logr.Discard(), ss))

			persistedPCLQ := &grovecorev1alpha1.PodClique{}
			require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(pclq), persistedPCLQ))
			require.NotNil(t, persistedPCLQ.Status.UpdateProgress)
			if tc.wantUpdateEnded {
				assert.NotNil(t, persistedPCLQ.Status.UpdateProgress.UpdateEndedAt)
				require.NotNil(t, persistedPCLQ.Status.CurrentPodTemplateHash)
				assert.Equal(t, testNewHash, *persistedPCLQ.Status.CurrentPodTemplateHash)
				require.NotNil(t, persistedPCLQ.Status.CurrentPodCliqueSetGenerationHash)
				assert.Equal(t, generation, *persistedPCLQ.Status.CurrentPodCliqueSetGenerationHash)
			} else {
				assert.Nil(t, persistedPCLQ.Status.UpdateProgress.UpdateEndedAt)
				assert.Nil(t, persistedPCLQ.Status.CurrentPodTemplateHash)
				assert.Nil(t, persistedPCLQ.Status.CurrentPodCliqueSetGenerationHash)
			}
		})
	}
}

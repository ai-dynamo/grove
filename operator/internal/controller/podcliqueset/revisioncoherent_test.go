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

package podcliqueset

import (
	"context"
	"encoding/json"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	commonrevision "github.com/ai-dynamo/grove/operator/internal/controller/common/revision"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestProcessRevisionPreservesCoherentUpdateScope(t *testing.T) {
	tests := []struct {
		name              string
		previousUpdate    bool
		pendingGeneration string
		wantScalingGroups []string
	}{
		{
			name: "unchanged components retain legacy identities outside the new scope",
		},
		{
			name:              "unfinished components from the previous update remain in scope",
			previousUpdate:    true,
			pendingGeneration: "older-generation",
			wantScalingGroups: []string{"decode"},
		},
		{
			name:              "completed components from the previous update leave the scope",
			previousUpdate:    true,
			pendingGeneration: "previous-generation",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pcs := testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithStandaloneClique("frontend").
				WithStandaloneClique("router").
				WithScalingGroupConfig("decode", []string{"decodeworker"}, 2, 1).
				WithScalingGroupConfig("prefill", []string{"prefillworker"}, 2, 1).
				WithPodCliqueSetGenerationHash(ptr.To("previous-generation")).
				WithUpdateStrategy(&grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: grovecorev1alpha1.CoherentStrategy}).
				Build()
			pcs.Generation = 1
			for _, clique := range pcs.Spec.Template.Cliques {
				clique.Spec.PodSpec.Containers = []corev1.Container{{Name: clique.Name, Image: clique.Name + ":v1"}}
			}
			oldRevision, err := testutils.NewPodCliqueSetControllerRevision(pcs)
			require.NoError(t, err)
			data := commonrevision.Data{}
			require.NoError(t, json.Unmarshal(oldRevision.Data.Raw, &data))
			for i := range data.Cliques {
				data.Cliques[i].Hash = "legacy-" + data.Cliques[i].Name
			}
			oldRevision.Data.Raw, err = json.Marshal(data)
			require.NoError(t, err)
			if tt.previousUpdate {
				pcs.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueSetUpdateProgress{
					UpdateStartedAt:               metav1.Now(),
					InScopePodCliqueScalingGroups: []string{"decode"},
				}
			}
			pgm := testutils.NewPodGangMapBuilder(pcs.Name, pcs.Namespace, pcs.UID, 0).
				WithEntries(grovecorev1alpha1.PodGangEntry{
					PodCliqueSetGenerationHash: tt.pendingGeneration,
					PCSGReplicaIndices:         map[string][]int32{"decode": {0}},
				}).Build()
			objects := []client.Object{pcs, oldRevision, pgm}
			for _, name := range []string{"frontend", "router"} {
				objects = append(objects, testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, name, pcs.Namespace, 0).
					WithLabels(map[string]string{apicommon.LabelPodTemplateHash: "legacy-" + name}).Build())
			}
			for _, name := range []string{"decode", "prefill"} {
				pcsgName := apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 0}, name)
				cliqueName := name + "worker"
				pclqName := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsgName, Replica: 0}, cliqueName)
				objects = append(objects, testutils.NewPCSGPodCliqueBuilder(pclqName, pcs.Namespace, pcs.Name, pcsgName, 0, 0).
					WithLabels(map[string]string{apicommon.LabelPodTemplateHash: "legacy-" + cliqueName}).Build())
			}

			pcs.Generation++
			pcs.Spec.Template.Cliques[0].Spec.PodSpec.Containers[0].Image = "frontend:v2"
			cl := testutils.SetupFakeClient(objects...)
			r := &Reconciler{client: cl, apiReader: cl}
			result := r.processRevision(ctx, logr.Discard(), pcs)
			require.False(t, result.HasErrors(), "%v", result.GetErrors())
			assert.True(t, result.NeedsRequeue())

			updatedPCS := &grovecorev1alpha1.PodCliqueSet{}
			require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(pcs), updatedPCS))
			require.NotNil(t, updatedPCS.Status.UpdateProgress)
			assert.Equal(t, []string{"frontend"}, updatedPCS.Status.UpdateProgress.InScopeStandalonePodCliques)
			assert.ElementsMatch(t, tt.wantScalingGroups, updatedPCS.Status.UpdateProgress.InScopePodCliqueScalingGroups)
			assert.Nil(t, updatedPCS.Status.UpdateProgress.UpdateEndedAt)
			assert.Empty(t, updatedPCS.Status.UpdateProgress.CurrentlyUpdating)
			assert.NotEqual(t, "previous-generation", ptr.Deref(updatedPCS.Status.CurrentGenerationHash, ""))

			revision, err := componentutils.GetPodCliqueSetRevision(ctx, cl, updatedPCS)
			require.NoError(t, err)
			for _, name := range []string{"router", "decodeworker", "prefillworker"} {
				hash, err := revision.CliqueHash(name)
				require.NoError(t, err)
				assert.Equal(t, "legacy-"+name, hash)
			}
		})
	}
}

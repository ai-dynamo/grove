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
	"testing"

	"github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestInjectAllResourceClaimRefsRespectsChildTypes(t *testing.T) {
	const pcsName = "model"
	pcsgName := common.GeneratePodCliqueScalingGroupName(common.ResourceNameReplica{Name: pcsName, Replica: 2}, "batch")
	standalone := testutils.NewPodCliqueBuilder(pcsName, "pcs-uid", "batch", "default", 2).Build()
	groupMember := testutils.NewPCSGPodCliqueBuilder(pcsgName+"-0-trainer", "default", pcsName, pcsgName, 2, 0).Build()

	tests := []struct {
		name       string
		filter     *grovecorev1beta1.PCSResourceSharingFilter
		standalone bool
		wantClaims bool
	}{
		{
			name:       "clique filter selects standalone clique",
			filter:     &grovecorev1beta1.PCSResourceSharingFilter{ChildCliqueNames: []string{"batch"}},
			standalone: true,
			wantClaims: true,
		},
		{
			name:   "clique filter does not select same-named group",
			filter: &grovecorev1beta1.PCSResourceSharingFilter{ChildCliqueNames: []string{"batch"}},
		},
		{
			name:       "group filter selects group member",
			filter:     &grovecorev1beta1.PCSResourceSharingFilter{ChildScalingGroupNames: []string{"batch"}},
			wantClaims: true,
		},
		{
			name:       "clique filter selects group member by template name",
			filter:     &grovecorev1beta1.PCSResourceSharingFilter{ChildCliqueNames: []string{"trainer"}},
			wantClaims: true,
		},
		{
			name:       "group filter does not select same-named standalone clique",
			filter:     &grovecorev1beta1.PCSResourceSharingFilter{ChildScalingGroupNames: []string{"batch"}},
			standalone: true,
		},
		{
			name:       "broadcast selects standalone clique",
			standalone: true,
			wantClaims: true,
		},
		{
			name:       "broadcast selects group member",
			wantClaims: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pcs := &grovecorev1beta1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: pcsName, Namespace: "default"},
				Spec: grovecorev1beta1.PodCliqueSetSpec{
					Template: grovecorev1beta1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1beta1.PodCliqueTemplateSpec{{Name: "batch"}, {Name: "trainer"}},
						PodCliqueScalingGroupConfigs: []grovecorev1beta1.PodCliqueScalingGroupConfig{{
							Name:        "batch",
							CliqueNames: []string{"trainer"},
						}},
						ResourceSharing: []grovecorev1beta1.PCSResourceSharingSpec{
							{
								ResourceSharingSpec: grovecorev1beta1.ResourceSharingSpec{Name: "shared", Scope: grovecorev1beta1.ResourceSharingScopeAllReplicas},
								Filter:              tt.filter,
							},
							{
								ResourceSharingSpec: grovecorev1beta1.ResourceSharingSpec{Name: "replica", Scope: grovecorev1beta1.ResourceSharingScopePerReplica},
								Filter:              tt.filter,
							},
						},
					},
				},
			}
			pclq := groupMember
			if tt.standalone {
				pclq = standalone
			}
			podSpec := corev1.PodSpec{
				Containers:     []corev1.Container{{Name: "worker"}, {Name: "sidecar"}},
				InitContainers: []corev1.Container{{Name: "init"}},
			}
			require.NoError(t, injectAllResourceClaimRefs(pcs, pclq, &podSpec, 2, 0))
			var wantPodClaims []corev1.PodResourceClaim
			var wantContainerClaims []corev1.ResourceClaim
			if tt.wantClaims {
				for _, claimName := range []string{"model-all-shared", "model-2-replica"} {
					wantPodClaims = append(wantPodClaims, corev1.PodResourceClaim{Name: claimName, ResourceClaimName: &claimName})
					wantContainerClaims = append(wantContainerClaims, corev1.ResourceClaim{Name: claimName})
				}
			}
			assert.Equal(t, wantPodClaims, podSpec.ResourceClaims)
			for _, container := range append(podSpec.Containers, podSpec.InitContainers...) {
				assert.Equal(t, wantContainerClaims, container.Resources.Claims, "container %s", container.Name)
			}
		})
	}
}

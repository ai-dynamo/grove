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

package validation

import (
	"strings"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestValidatePodCliqueSetReplicas(t *testing.T) {
	t.Run("zero PCS still validates descendant names", func(t *testing.T) {
		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("a", 63)},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				Replicas: 0,
				Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{{
					Name: strings.Repeat("b", 63),
					Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1},
				}}},
			},
		}

		errs := ValidatePodCliqueSetReplicas(pcs, nil)
		require.Len(t, errs, 1)
		assert.Contains(t, errs.ToAggregate().Error(), "generated pod hostname")
	})

	t.Run("zero PCS still validates AllReplicas PCS claim", func(t *testing.T) {
		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("a", 63)},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				ResourceSharing: []grovecorev1alpha1.PCSResourceSharingSpec{
					{ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{Name: "gpu", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas}},
					{ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{Name: "gpu", Scope: grovecorev1alpha1.ResourceSharingScopePerReplica}},
				},
			}},
		}

		errs := ValidatePodCliqueSetReplicas(pcs, nil)
		require.Len(t, errs, 1)
		assert.Equal(t, "spec.template.resourceSharing[0].name", errs[0].Field)
	})

	t.Run("uses standalone HPA maximum", func(t *testing.T) {
		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("p", 30)},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				Replicas: 1,
				Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{{
					Name: strings.Repeat("c", 26),
					Spec: grovecorev1alpha1.PodCliqueSpec{
						Replicas:    1,
						ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{MaxReplicas: 1000},
					},
				}}},
			},
		}
		assert.Empty(t, ValidatePodCliqueSetReplicas(pcs, nil))

		pcs.Spec.Template.Cliques[0].Spec.ScaleConfig.MaxReplicas = 1001
		errs := ValidatePodCliqueSetReplicas(pcs, nil)
		require.NotEmpty(t, errs)
		assert.Equal(t, "spec.template.cliques[0].replicas", errs[0].Field)
		assert.Contains(t, errs.ToAggregate().Error(), "generated pod hostname")
	})

	t.Run("uses scaling group HPA maximum", func(t *testing.T) {
		cliqueName := strings.Repeat("c", 18)
		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("p", 20)},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				Replicas: 1,
				Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
					Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{{
						Name: cliqueName,
						Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1},
					}},
					PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{{
						Name:        strings.Repeat("g", 15),
						CliqueNames: []string{cliqueName},
						Replicas:    ptr.To[int32](1),
						ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{MaxReplicas: 1000},
					}},
				},
			},
		}
		assert.Empty(t, ValidatePodCliqueSetReplicas(pcs, nil))

		pcs.Spec.Template.PodCliqueScalingGroupConfigs[0].ScaleConfig.MaxReplicas = 1001
		errs := ValidatePodCliqueSetReplicas(pcs, nil)
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "generated pod hostname")

		t.Run("zero PCS still validates configured scaling group descendants", func(t *testing.T) {
			pcs.Spec.Replicas = 0
			errs := ValidatePodCliqueSetReplicas(pcs, nil)
			require.NotEmpty(t, errs)
			assert.Contains(t, errs.ToAggregate().Error(), "generated pod hostname")
		})
	})

	t.Run("uses live scaling group", func(t *testing.T) {
		cliqueName := strings.Repeat("c", 18)
		pcsgName := strings.Repeat("g", 15)
		pcs := &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("p", 20)},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				Replicas: 2,
				Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
					Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{{
						Name: cliqueName,
						Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1},
					}},
					PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{{
						Name:        pcsgName,
						CliqueNames: []string{cliqueName},
						Replicas:    ptr.To[int32](1),
						ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{MaxReplicas: 1000},
					}},
				},
			},
		}
		pcsgs := []grovecorev1alpha1.PodCliqueScalingGroup{{
			ObjectMeta: metav1.ObjectMeta{
				Name:   apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 1}, pcsgName),
				Labels: map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "1"},
			},
			Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
				Replicas: 1000,
			},
		}}

		assert.Empty(t, ValidatePodCliqueSetReplicas(pcs, pcsgs))

		pcsgs[0].Spec.Replicas = 1001
		errs := ValidatePodCliqueSetReplicas(pcs, pcsgs)
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "generated pod hostname")

		t.Run("validates a live scaling group on a non-last PCS replica", func(t *testing.T) {
			pcsgs[0].Name = apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 0}, pcsgName)
			pcsgs[0].Labels[apicommon.LabelPodCliqueSetReplicaIndex] = "0"
			errs := ValidatePodCliqueSetReplicas(pcs, pcsgs)
			require.NotEmpty(t, errs)
			assert.Contains(t, errs.ToAggregate().Error(), "generated pod hostname")
		})
	})
}

func TestValidatePodCliqueReplicas(t *testing.T) {
	t.Run("enforces minAvailable boundary", func(t *testing.T) {
		pclq := &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{Name: "worker"},
			Spec: grovecorev1alpha1.PodCliqueSpec{
				Replicas:     3,
				MinAvailable: ptr.To[int32](3),
			},
		}
		assert.Empty(t, ValidatePodCliqueReplicas(pclq, &grovecorev1alpha1.PodCliqueTemplateSpec{}))

		pclq.Spec.Replicas = 2
		errs := ValidatePodCliqueReplicas(pclq, &grovecorev1alpha1.PodCliqueTemplateSpec{})
		require.Len(t, errs, 1)
		assert.Equal(t, "spec.replicas", errs[0].Field)
		assert.Contains(t, errs[0].Detail, "minAvailable")
	})

	t.Run("validates concrete external scale", func(t *testing.T) {
		pclq := &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("a", 59)},
			Spec: grovecorev1alpha1.PodCliqueSpec{
				Replicas:     1000,
				MinAvailable: ptr.To[int32](1),
			},
		}
		template := &grovecorev1alpha1.PodCliqueTemplateSpec{
			Spec: grovecorev1alpha1.PodCliqueSpec{ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{MaxReplicas: 10}},
		}
		assert.Empty(t, ValidatePodCliqueReplicas(pclq, template))

		pclq.Spec.Replicas = 1001
		errs := ValidatePodCliqueReplicas(pclq, template)
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "generated pod hostname")
	})

	t.Run("validates PerReplica claim at requested index", func(t *testing.T) {
		pclq := &grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("a", 55)},
			Spec: grovecorev1alpha1.PodCliqueSpec{
				Replicas:     1000,
				MinAvailable: ptr.To[int32](1),
			},
		}
		template := &grovecorev1alpha1.PodCliqueTemplateSpec{ResourceSharing: []grovecorev1alpha1.ResourceSharingSpec{
			grovecorev1alpha1.ResourceSharingSpec{Name: "gpu", Scope: grovecorev1alpha1.ResourceSharingScopePerReplica},
		}}
		assert.Empty(t, ValidatePodCliqueReplicas(pclq, template))

		pclq.Spec.Replicas = 1001
		errs := ValidatePodCliqueReplicas(pclq, template)
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "resource claim reference name")
	})
}

func TestValidatePodCliqueScalingGroupReplicas(t *testing.T) {
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "parent"},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
			Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{{
				Name: strings.Repeat("c", 16),
				Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1},
			}},
		}},
	}
	config := &grovecorev1alpha1.PodCliqueScalingGroupConfig{
		ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{MaxReplicas: 10},
	}
	pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("g", 40)},
		Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
			Replicas:    1000,
			CliqueNames: []string{strings.Repeat("c", 16)},
		},
	}

	t.Run("enforces minAvailable boundary", func(t *testing.T) {
		pcsg.Spec.Replicas = 3
		pcsg.Spec.MinAvailable = ptr.To[int32](3)
		assert.Empty(t, ValidatePodCliqueScalingGroupReplicas(pcsg, pcs, config))

		pcsg.Spec.Replicas = 2
		errs := ValidatePodCliqueScalingGroupReplicas(pcsg, pcs, config)
		require.Len(t, errs, 1)
		assert.Equal(t, "spec.replicas", errs[0].Field)
		assert.Contains(t, errs[0].Detail, "minAvailable")
	})

	t.Run("validates PCSG PerReplica claim at requested index", func(t *testing.T) {
		claimPCSG := &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("g", 55)},
			Spec:       grovecorev1alpha1.PodCliqueScalingGroupSpec{Replicas: 1000},
		}
		claimConfig := &grovecorev1alpha1.PodCliqueScalingGroupConfig{
			ResourceSharing: []grovecorev1alpha1.PCSGResourceSharingSpec{{
				ResourceSharingSpec: grovecorev1alpha1.ResourceSharingSpec{Name: "gpu", Scope: grovecorev1alpha1.ResourceSharingScopePerReplica},
			}},
		}
		assert.Empty(t, ValidatePodCliqueScalingGroupReplicas(claimPCSG, pcs, claimConfig))

		claimPCSG.Spec.Replicas = 1001
		errs := ValidatePodCliqueScalingGroupReplicas(claimPCSG, pcs, claimConfig)
		require.Len(t, errs, 1)
		assert.Contains(t, errs[0].Detail, "resource claim reference name")
	})

	t.Run("validates member PodClique claim at requested index", func(t *testing.T) {
		claimPCS := &grovecorev1alpha1.PodCliqueSet{
			Spec: grovecorev1alpha1.PodCliqueSetSpec{Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{{
					Name: "c",
					Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1},
					ResourceSharing: []grovecorev1alpha1.ResourceSharingSpec{{
						Name: "gpu", Scope: grovecorev1alpha1.ResourceSharingScopeAllReplicas,
					}},
				}},
			}},
		}
		claimPCSG := &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("g", 49)},
			Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
				Replicas: 1000, CliqueNames: []string{"c"},
			},
		}
		assert.Empty(t, ValidatePodCliqueScalingGroupReplicas(claimPCSG, claimPCS, &grovecorev1alpha1.PodCliqueScalingGroupConfig{}))

		claimPCSG.Spec.Replicas = 1001
		errs := ValidatePodCliqueScalingGroupReplicas(claimPCSG, claimPCS, &grovecorev1alpha1.PodCliqueScalingGroupConfig{})
		require.Len(t, errs, 1)
		assert.Contains(t, errs[0].Detail, "resource claim reference name")
	})

	t.Run("validates generated child names at requested index", func(t *testing.T) {
		pcsg.Spec.Replicas = 1000
		pcsg.Spec.MinAvailable = nil
		assert.Empty(t, ValidatePodCliqueScalingGroupReplicas(pcsg, pcs, config))

		pcsg.Spec.Replicas = 1001
		errs := ValidatePodCliqueScalingGroupReplicas(pcsg, pcs, config)
		require.NotEmpty(t, errs)
		assert.Equal(t, "spec.replicas", errs[0].Field)
		assert.Contains(t, errs.ToAggregate().Error(), "generated pod hostname")
	})
}

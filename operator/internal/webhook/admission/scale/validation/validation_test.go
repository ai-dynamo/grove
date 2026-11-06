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
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestValidatePodCliqueSetReplicas(t *testing.T) {
	t.Run("zero PCS still validates descendant names", func(t *testing.T) {
		pcs := &grovecorev1beta1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("a", 63)},
			Spec: grovecorev1beta1.PodCliqueSetSpec{
				Replicas: 0,
				Template: grovecorev1beta1.PodCliqueSetTemplateSpec{Cliques: []*grovecorev1beta1.PodCliqueTemplateSpec{{
					Name: strings.Repeat("b", 63),
					Spec: grovecorev1beta1.PodCliqueSpec{Replicas: 1},
				}}},
			},
		}

		errs := ValidatePodCliqueSetReplicas(pcs, nil, -1)
		require.Len(t, errs, 2)
		assert.Contains(t, errs.ToAggregate().Error(), "generated pod hostname")
	})

	t.Run("zero PCS still validates AllReplicas PCS claim", func(t *testing.T) {
		pcs := &grovecorev1beta1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("a", 41)},
			Spec: grovecorev1beta1.PodCliqueSetSpec{Template: grovecorev1beta1.PodCliqueSetTemplateSpec{
				ResourceSharing: []grovecorev1beta1.PCSResourceSharingSpec{
					{ResourceSharingSpec: grovecorev1beta1.ResourceSharingSpec{Name: strings.Repeat("g", 25), Scope: grovecorev1beta1.ResourceSharingScopeAllReplicas}},
					{ResourceSharingSpec: grovecorev1beta1.ResourceSharingSpec{Name: strings.Repeat("g", 25), Scope: grovecorev1beta1.ResourceSharingScopePerReplica}},
				},
			}},
		}

		errs := ValidatePodCliqueSetReplicas(pcs, nil, -1)
		require.Len(t, errs, 1)
		assert.Equal(t, "spec.template.resourceSharing[0].name", errs[0].Field)
	})

	t.Run("uses standalone HPA maximum", func(t *testing.T) {
		pcs := &grovecorev1beta1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("p", 30)},
			Spec: grovecorev1beta1.PodCliqueSetSpec{
				Replicas: 1,
				Template: grovecorev1beta1.PodCliqueSetTemplateSpec{Cliques: []*grovecorev1beta1.PodCliqueTemplateSpec{{
					Name: strings.Repeat("c", 26),
					Spec: grovecorev1beta1.PodCliqueSpec{
						Replicas:    1,
						ScaleConfig: &grovecorev1beta1.AutoScalingConfig{MaxReplicas: 1000},
					},
				}}},
			},
		}
		assert.Empty(t, ValidatePodCliqueSetReplicas(pcs, nil, -1))

		pcs.Spec.Template.Cliques[0].Spec.ScaleConfig.MaxReplicas = 1001
		errs := ValidatePodCliqueSetReplicas(pcs, nil, -1)
		require.NotEmpty(t, errs)
		assert.Equal(t, "spec.template.cliques[0].replicas", errs[0].Field)
		assert.Contains(t, errs.ToAggregate().Error(), "generated pod hostname")
	})

	t.Run("uses scaling group HPA maximum", func(t *testing.T) {
		cliqueName := strings.Repeat("c", 18)
		pcs := &grovecorev1beta1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("p", 20)},
			Spec: grovecorev1beta1.PodCliqueSetSpec{
				Replicas: 1,
				Template: grovecorev1beta1.PodCliqueSetTemplateSpec{
					Cliques: []*grovecorev1beta1.PodCliqueTemplateSpec{{
						Name: cliqueName,
						Spec: grovecorev1beta1.PodCliqueSpec{Replicas: 1},
					}},
					PodCliqueScalingGroupConfigs: []grovecorev1beta1.PodCliqueScalingGroupConfig{{
						Name:        strings.Repeat("g", 15),
						CliqueNames: []string{cliqueName},
						Replicas:    ptr.To[int32](1),
						ScaleConfig: &grovecorev1beta1.AutoScalingConfig{MaxReplicas: 1000},
					}},
				},
			},
		}
		assert.Empty(t, ValidatePodCliqueSetReplicas(pcs, nil, -1))

		pcs.Spec.Template.PodCliqueScalingGroupConfigs[0].ScaleConfig.MaxReplicas = 1001
		errs := ValidatePodCliqueSetReplicas(pcs, nil, -1)
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "generated pod hostname")

		t.Run("zero PCS still validates configured scaling group descendants", func(t *testing.T) {
			pcs.Spec.Replicas = 0
			errs := ValidatePodCliqueSetReplicas(pcs, nil, -1)
			require.NotEmpty(t, errs)
			assert.Contains(t, errs.ToAggregate().Error(), "generated pod hostname")
		})
	})

	t.Run("uses live scaling group", func(t *testing.T) {
		cliqueName := strings.Repeat("c", 18)
		pcsgName := strings.Repeat("g", 15)
		pcs := &grovecorev1beta1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("p", 20)},
			Spec: grovecorev1beta1.PodCliqueSetSpec{
				Replicas: 2,
				Template: grovecorev1beta1.PodCliqueSetTemplateSpec{
					Cliques: []*grovecorev1beta1.PodCliqueTemplateSpec{{
						Name: cliqueName,
						Spec: grovecorev1beta1.PodCliqueSpec{Replicas: 1},
					}},
					PodCliqueScalingGroupConfigs: []grovecorev1beta1.PodCliqueScalingGroupConfig{{
						Name:        pcsgName,
						CliqueNames: []string{cliqueName},
						Replicas:    ptr.To[int32](1),
						ScaleConfig: &grovecorev1beta1.AutoScalingConfig{MaxReplicas: 1000},
					}},
				},
			},
		}
		pcsgs := []grovecorev1beta1.PodCliqueScalingGroup{{
			ObjectMeta: metav1.ObjectMeta{
				Name:   apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 1}, pcsgName),
				Labels: map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "1"},
			},
			Spec: grovecorev1beta1.PodCliqueScalingGroupSpec{
				Replicas: 1000,
			},
		}}

		groupedPCSGs, err := componentutils.GroupPCSGsByPCSReplicaIndex(pcsgs)
		require.NoError(t, err)
		assert.Empty(t, ValidatePodCliqueSetReplicas(pcs, groupedPCSGs, 1))

		pcsgs[0].Spec.Replicas = 1001
		groupedPCSGs, err = componentutils.GroupPCSGsByPCSReplicaIndex(pcsgs)
		require.NoError(t, err)
		errs := ValidatePodCliqueSetReplicas(pcs, groupedPCSGs, 1)
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "generated pod hostname")

		t.Run("validates a live scaling group on a non-last PCS replica", func(t *testing.T) {
			pcsgs[0].Name = apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 0}, pcsgName)
			pcsgs[0].Labels[apicommon.LabelPodCliqueSetReplicaIndex] = "0"
			groupedPCSGs, err := componentutils.GroupPCSGsByPCSReplicaIndex(pcsgs)
			require.NoError(t, err)
			errs := ValidatePodCliqueSetReplicas(pcs, groupedPCSGs, 0)
			require.NotEmpty(t, errs)
			assert.Contains(t, errs.ToAggregate().Error(), "generated pod hostname")
		})
	})
}

func TestValidatePodCliqueSetTemplateAgainstLiveScalingGroup(t *testing.T) {
	pcs, pcsg := scalingGroupFixture(strings.Repeat("p", 20), strings.Repeat("g", 15), strings.Repeat("c", 18))
	pcsg.Spec.Replicas = 1000
	groups := map[int][]grovecorev1beta1.PodCliqueScalingGroup{0: {*pcsg}}
	require.Empty(t, ValidatePodCliqueSetReplicas(pcs, groups, 0))

	// The existing group uses three-digit indexes, although its template still requests one replica.
	// Increasing the clique template count requires longer hostnames when its children are recreated.
	pcs.Spec.Template.Cliques[0].Spec.Replicas = 11
	require.Empty(t, ValidatePodCliqueSetReplicas(pcs, nil, 0))
	require.ErrorContains(t, ValidatePodCliqueSetReplicas(pcs, groups, 0).ToAggregate(), "generated pod hostname")
}

func TestValidatePodCliqueSetPodGangLabels(t *testing.T) {
	t.Run("anchor label includes the PCS replica index", func(t *testing.T) {
		pcs, _ := standaloneFixture(strings.Repeat("p", 41), "worker")
		pcs.Spec.Replicas = 10
		require.Empty(t, ValidatePodCliqueSetReplicas(pcs, nil, -1))

		pcs.Spec.Replicas = 11
		require.ErrorContains(t, ValidatePodCliqueSetReplicas(pcs, nil, -1).ToAggregate(), "generated PodGang label")
	})

	t.Run("non-anchor label includes the group replica index", func(t *testing.T) {
		pcs, _ := scalingGroupFixture("p", strings.Repeat("g", 37), "worker")
		config := &pcs.Spec.Template.PodCliqueScalingGroupConfigs[0]
		config.Replicas = ptr.To[int32](10)
		require.Empty(t, ValidatePodCliqueSetReplicas(pcs, nil, -1))

		config.Replicas = ptr.To[int32](11)
		require.ErrorContains(t, ValidatePodCliqueSetReplicas(pcs, nil, -1).ToAggregate(), "generated PodGang label")

		config.Replicas = ptr.To[int32](1)
		config.ScaleConfig = &grovecorev1beta1.AutoScalingConfig{MaxReplicas: 11}
		require.ErrorContains(t, ValidatePodCliqueSetReplicas(pcs, nil, -1).ToAggregate(), "generated PodGang label")
	})

	t.Run("checks live groups above the requested PCS replica count", func(t *testing.T) {
		pcs, pcsg := scalingGroupFixture("p", strings.Repeat("g", 37), "worker")
		pcsg.Spec.Replicas = 11
		for _, replica := range []string{"0", "1", "-1"} {
			t.Run(replica, func(t *testing.T) {
				pcsg.Labels[apicommon.LabelPodCliqueSetReplicaIndex] = replica
				pcsg.Name = "p-" + replica + "-" + pcs.Spec.Template.PodCliqueScalingGroupConfigs[0].Name
				replicaIndex, err := strconv.Atoi(replica)
				require.NoError(t, err)
				groupedPCSGs, err := componentutils.GroupPCSGsByPCSReplicaIndex([]grovecorev1beta1.PodCliqueScalingGroup{*pcsg})
				require.NoError(t, err)
				errs := ValidatePodCliqueSetReplicas(pcs, groupedPCSGs, replicaIndex)
				if replica != "-1" {
					require.ErrorContains(t, errs.ToAggregate(), "generated PodGang label")
				} else {
					require.Empty(t, errs)
				}
			})
		}
	})
}

func TestValidatePodCliqueSetMaxReplicas(t *testing.T) {
	pcs, _ := scalingGroupFixture("p", "g", "c")
	pcs.Spec.Replicas = math.MaxInt32
	start := time.Now()
	errs := ValidatePodCliqueSetReplicas(pcs, nil, -1)
	require.Empty(t, errs)
	// This empty-cluster request must finish well within the 10-second webhook timeout.
	require.Less(t, time.Since(start), 5*time.Second)
}

func TestValidatePodCliqueSetAnchorOnlyGroup(t *testing.T) {
	for _, tt := range []struct {
		name         string
		liveReplicas *int32
		maxReplicas  int32
		wantErr      bool
	}{
		{name: "new group uses only the anchor"},
		{name: "existing group uses only the anchor", liveReplicas: ptr.To[int32](1)},
		{name: "idle group without HPA", liveReplicas: ptr.To[int32](0)},
		{name: "HPA maximum fits the anchor", maxReplicas: 1},
		{name: "HPA maximum fits the existing anchor", liveReplicas: ptr.To[int32](1), maxReplicas: 1},
		{name: "HPA can create a non-anchor", maxReplicas: 2, wantErr: true},
		{name: "live group needs a non-anchor", liveReplicas: ptr.To[int32](2), wantErr: true},
		{name: "HPA can restore an idle group in a non-anchor", liveReplicas: ptr.To[int32](0), maxReplicas: 1, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pcs, pcsg := scalingGroupFixture("p", strings.Repeat("g", 38), "c")
			if tt.maxReplicas > 0 {
				pcs.Spec.Template.PodCliqueScalingGroupConfigs[0].ScaleConfig = &grovecorev1beta1.AutoScalingConfig{MaxReplicas: tt.maxReplicas}
			}
			var pcsgs map[int][]grovecorev1beta1.PodCliqueScalingGroup
			if tt.liveReplicas != nil {
				pcsg.Spec.Replicas = *tt.liveReplicas
				pcsgs = map[int][]grovecorev1beta1.PodCliqueScalingGroup{0: {*pcsg}}
			}
			errs := ValidatePodCliqueSetReplicas(pcs, pcsgs, 0)
			if tt.wantErr {
				require.ErrorContains(t, errs.ToAggregate(), "generated PodGang label")
			} else {
				require.Empty(t, errs)
			}
		})
	}
}

func TestValidatePodCliqueReplicas(t *testing.T) {
	t.Run("allows scale to zero with minAvailable", func(t *testing.T) {
		pclq := &grovecorev1beta1.PodClique{
			ObjectMeta: metav1.ObjectMeta{Name: "worker"},
			Spec: grovecorev1beta1.PodCliqueSpec{
				Replicas:     3,
				MinAvailable: ptr.To[int32](3),
			},
		}
		assert.Empty(t, ValidatePodCliqueReplicas(pclq, &grovecorev1beta1.PodCliqueTemplateSpec{}))

		pclq.Spec.Replicas = 0
		assert.Empty(t, ValidatePodCliqueReplicas(pclq, &grovecorev1beta1.PodCliqueTemplateSpec{}))
	})

	t.Run("validates concrete external scale", func(t *testing.T) {
		pclq := &grovecorev1beta1.PodClique{
			ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("a", 59)},
			Spec: grovecorev1beta1.PodCliqueSpec{
				Replicas:     1000,
				MinAvailable: ptr.To[int32](1),
			},
		}
		template := &grovecorev1beta1.PodCliqueTemplateSpec{
			Spec: grovecorev1beta1.PodCliqueSpec{ScaleConfig: &grovecorev1beta1.AutoScalingConfig{MaxReplicas: 10}},
		}
		assert.Empty(t, ValidatePodCliqueReplicas(pclq, template))

		pclq.Spec.Replicas = 1001
		errs := ValidatePodCliqueReplicas(pclq, template)
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "generated pod hostname")
	})

	t.Run("validates PerReplica claim at requested index", func(t *testing.T) {
		pclq := &grovecorev1beta1.PodClique{
			ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("a", 55)},
			Spec: grovecorev1beta1.PodCliqueSpec{
				Replicas:     1000,
				MinAvailable: ptr.To[int32](1),
			},
		}
		template := &grovecorev1beta1.PodCliqueTemplateSpec{ResourceSharing: []grovecorev1beta1.ResourceSharingSpec{
			grovecorev1beta1.ResourceSharingSpec{Name: "gpu", Scope: grovecorev1beta1.ResourceSharingScopePerReplica},
		}}
		assert.Empty(t, ValidatePodCliqueReplicas(pclq, template))

		pclq.Spec.Replicas = 1001
		errs := ValidatePodCliqueReplicas(pclq, template)
		require.NotEmpty(t, errs)
		assert.Contains(t, errs.ToAggregate().Error(), "resource claim reference name")
	})
}

func TestValidatePodCliqueScalingGroupReplicas(t *testing.T) {
	pcs := &grovecorev1beta1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "parent"},
		Spec: grovecorev1beta1.PodCliqueSetSpec{Template: grovecorev1beta1.PodCliqueSetTemplateSpec{
			Cliques: []*grovecorev1beta1.PodCliqueTemplateSpec{{
				Name: strings.Repeat("c", 16),
				Spec: grovecorev1beta1.PodCliqueSpec{Replicas: 1},
			}},
		}},
	}
	config := &grovecorev1beta1.PodCliqueScalingGroupConfig{
		ScaleConfig: &grovecorev1beta1.AutoScalingConfig{MaxReplicas: 10},
	}
	pcsg := &grovecorev1beta1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("g", 40)},
		Spec: grovecorev1beta1.PodCliqueScalingGroupSpec{
			Replicas:    1000,
			CliqueNames: []string{strings.Repeat("c", 16)},
		},
	}

	t.Run("allows scale to zero with minAvailable", func(t *testing.T) {
		pcsg.Spec.Replicas = 3
		pcsg.Spec.MinAvailable = ptr.To[int32](3)
		assert.Empty(t, ValidatePodCliqueScalingGroupReplicas(pcsg, pcs, config, 0, 1))

		pcsg.Spec.Replicas = 0
		assert.Empty(t, ValidatePodCliqueScalingGroupReplicas(pcsg, pcs, config, 0, 1))
	})

	t.Run("validates PCSG PerReplica claim at requested index", func(t *testing.T) {
		claimPCSG := &grovecorev1beta1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("g", 55)},
			Spec:       grovecorev1beta1.PodCliqueScalingGroupSpec{Replicas: 1000},
		}
		claimConfig := &grovecorev1beta1.PodCliqueScalingGroupConfig{
			ResourceSharing: []grovecorev1beta1.PCSGResourceSharingSpec{{
				ResourceSharingSpec: grovecorev1beta1.ResourceSharingSpec{Name: "gpu", Scope: grovecorev1beta1.ResourceSharingScopePerReplica},
			}},
		}
		assert.Empty(t, ValidatePodCliqueScalingGroupReplicas(claimPCSG, pcs, claimConfig, 0, 1))

		claimPCSG.Spec.Replicas = 1001
		errs := ValidatePodCliqueScalingGroupReplicas(claimPCSG, pcs, claimConfig, 0, 1)
		require.Len(t, errs, 1)
		assert.Contains(t, errs[0].Detail, "resource claim reference name")
	})

	t.Run("validates member PodClique claim at requested index", func(t *testing.T) {
		claimPCS := &grovecorev1beta1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: "parent"},
			Spec: grovecorev1beta1.PodCliqueSetSpec{Template: grovecorev1beta1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1beta1.PodCliqueTemplateSpec{{
					Name: "c",
					Spec: grovecorev1beta1.PodCliqueSpec{Replicas: 1},
					ResourceSharing: []grovecorev1beta1.ResourceSharingSpec{{
						Name: "gpu", Scope: grovecorev1beta1.ResourceSharingScopeAllReplicas,
					}},
				}},
			}},
		}
		claimPCSG := &grovecorev1beta1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("g", 49)},
			Spec: grovecorev1beta1.PodCliqueScalingGroupSpec{
				Replicas: 1000, CliqueNames: []string{"c"},
			},
		}
		assert.Empty(t, ValidatePodCliqueScalingGroupReplicas(claimPCSG, claimPCS, &grovecorev1beta1.PodCliqueScalingGroupConfig{}, 0, 1))

		claimPCSG.Spec.Replicas = 1001
		errs := ValidatePodCliqueScalingGroupReplicas(claimPCSG, claimPCS, &grovecorev1beta1.PodCliqueScalingGroupConfig{}, 0, 1)
		require.Len(t, errs, 1)
		assert.Contains(t, errs[0].Detail, "resource claim reference name")
	})

	t.Run("validates generated child names at requested index", func(t *testing.T) {
		pcsg.Spec.Replicas = 1000
		pcsg.Spec.MinAvailable = nil
		assert.Empty(t, ValidatePodCliqueScalingGroupReplicas(pcsg, pcs, config, 0, 1))

		pcsg.Spec.Replicas = 1001
		errs := ValidatePodCliqueScalingGroupReplicas(pcsg, pcs, config, 0, 1)
		require.NotEmpty(t, errs)
		assert.Equal(t, "spec.replicas", errs[0].Field)
		assert.Contains(t, errs.ToAggregate().Error(), "generated pod hostname")
	})
}

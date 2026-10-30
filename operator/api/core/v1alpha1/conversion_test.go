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

package v1alpha1

import (
	"fmt"
	"testing"

	"github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/randfill"
)

const roundTripIterations = 200

func TestConvertTopologyConstraintToBeta(t *testing.T) {
	testCases := []struct {
		description string
		in          *TopologyConstraint
		expected    *v1beta1.TopologyConstraint
	}{
		{
			description: "packDomain only becomes pack.required",
			in:          &TopologyConstraint{TopologyName: "topo", PackDomain: TopologyDomainRack},
			expected: &v1beta1.TopologyConstraint{
				TopologyName: "topo",
				Pack:         &v1beta1.TopologyPackConstraint{RequiredDomain: v1beta1.TopologyDomainRack},
			},
		},
		{
			description: "packDomain is merged into a pack that only has preferred",
			in: &TopologyConstraint{
				PackDomain: TopologyDomainRack,
				Pack:       &TopologyPackConstraint{PreferredDomain: TopologyDomainHost},
			},
			expected: &v1beta1.TopologyConstraint{
				Pack: &v1beta1.TopologyPackConstraint{RequiredDomain: v1beta1.TopologyDomainRack, PreferredDomain: v1beta1.TopologyDomainHost},
			},
		},
		{
			description: "pack.required takes precedence over packDomain",
			in: &TopologyConstraint{
				PackDomain: TopologyDomainRack,
				Pack:       &TopologyPackConstraint{RequiredDomain: TopologyDomainZone},
			},
			expected: &v1beta1.TopologyConstraint{
				Pack: &v1beta1.TopologyPackConstraint{RequiredDomain: v1beta1.TopologyDomainZone},
			},
		},
		{
			description: "pack is copied unchanged without packDomain",
			in:          &TopologyConstraint{Pack: &TopologyPackConstraint{PreferredDomain: TopologyDomainHost}},
			expected:    &v1beta1.TopologyConstraint{Pack: &v1beta1.TopologyPackConstraint{PreferredDomain: v1beta1.TopologyDomainHost}},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			out := &v1beta1.TopologyConstraint{}
			require.NoError(t, Convert_v1alpha1_TopologyConstraint_To_v1beta1_TopologyConstraint(tc.in, out, nil))
			assert.Equal(t, tc.expected, out)
		})
	}
}

func TestConvertPodCliqueSetNormalizesPackDomainAtEveryLevel(t *testing.T) {
	legacy := func() *TopologyConstraint { return &TopologyConstraint{PackDomain: TopologyDomainRack} }
	in := &PodCliqueSet{Spec: PodCliqueSetSpec{Template: PodCliqueSetTemplateSpec{
		TopologyConstraint:           legacy(),
		Cliques:                      []*PodCliqueTemplateSpec{{Name: "worker", TopologyConstraint: legacy()}, nil},
		PodCliqueScalingGroupConfigs: []PodCliqueScalingGroupConfig{{Name: "sg", TopologyConstraint: legacy()}},
	}}}

	beta := &v1beta1.PodCliqueSet{}
	require.NoError(t, Convert_v1alpha1_PodCliqueSet_To_v1beta1_PodCliqueSet(in, beta, nil))
	template := beta.Spec.Template
	for _, tc := range []*v1beta1.TopologyConstraint{
		template.TopologyConstraint, template.Cliques[0].TopologyConstraint, template.PodCliqueScalingGroupConfigs[0].TopologyConstraint,
	} {
		assert.Equal(t, v1beta1.TopologyDomainRack, tc.RequiredDomain())
	}
	assert.Nil(t, template.Cliques[1])

	alpha := &PodCliqueSet{}
	require.NoError(t, Convert_v1beta1_PodCliqueSet_To_v1alpha1_PodCliqueSet(beta, alpha, nil))
	assert.Empty(t, alpha.Spec.Template.TopologyConstraint.PackDomain)
	assert.Equal(t, TopologyDomainRack, alpha.Spec.Template.TopologyConstraint.Pack.RequiredDomain)
}

// TestRoundTripFromBeta checks that every v1beta1 object survives a conversion to v1alpha1 and back unchanged.
func TestRoundTripFromBeta(t *testing.T) {
	f := randfill.New().NilChance(0.2).NumElements(0, 2).MaxDepth(12)
	for i := 0; i < roundTripIterations; i++ {
		t.Run(fmt.Sprintf("iteration-%d", i), func(t *testing.T) {
			t.Run("PodCliqueSet", func(t *testing.T) {
				in := &v1beta1.PodCliqueSet{}
				f.Fill(in)
				in.TypeMeta = metav1.TypeMeta{}

				alpha := &PodCliqueSet{}
				require.NoError(t, Convert_v1beta1_PodCliqueSet_To_v1alpha1_PodCliqueSet(in, alpha, nil))
				out := &v1beta1.PodCliqueSet{}
				require.NoError(t, Convert_v1alpha1_PodCliqueSet_To_v1beta1_PodCliqueSet(alpha, out, nil))
				if !equality.Semantic.DeepEqual(in, out) {
					t.Fatalf("round trip mismatch (-want +got): \n%s", cmp.Diff(in, out))
				}
			})
			t.Run("PodClique", func(t *testing.T) {
				in := &v1beta1.PodClique{}
				f.Fill(in)
				in.TypeMeta = metav1.TypeMeta{}
				mid, out := &PodClique{}, &v1beta1.PodClique{}
				require.NoError(t, Convert_v1beta1_PodClique_To_v1alpha1_PodClique(in, mid, nil))
				require.NoError(t, Convert_v1alpha1_PodClique_To_v1beta1_PodClique(mid, out, nil))
				if !equality.Semantic.DeepEqual(in, out) {
					t.Fatalf("round trip mismatch (-want +got): \n%s", cmp.Diff(in, out))
				}
			})
			t.Run("PodCliqueScalingGroup", func(t *testing.T) {
				in := &v1beta1.PodCliqueScalingGroup{}
				f.Fill(in)
				in.TypeMeta = metav1.TypeMeta{}
				mid, out := &PodCliqueScalingGroup{}, &v1beta1.PodCliqueScalingGroup{}
				require.NoError(t, Convert_v1beta1_PodCliqueScalingGroup_To_v1alpha1_PodCliqueScalingGroup(in, mid, nil))
				require.NoError(t, Convert_v1alpha1_PodCliqueScalingGroup_To_v1beta1_PodCliqueScalingGroup(mid, out, nil))
				if !equality.Semantic.DeepEqual(in, out) {
					t.Fatalf("round trip mismatch (-want +got): \n%s", cmp.Diff(in, out))
				}
			})
			t.Run("ClusterTopologyBinding", func(t *testing.T) {
				in := &v1beta1.ClusterTopologyBinding{}
				f.Fill(in)
				in.TypeMeta = metav1.TypeMeta{}
				mid, out := &ClusterTopologyBinding{}, &v1beta1.ClusterTopologyBinding{}
				require.NoError(t, Convert_v1beta1_ClusterTopologyBinding_To_v1alpha1_ClusterTopologyBinding(in, mid, nil))
				require.NoError(t, Convert_v1alpha1_ClusterTopologyBinding_To_v1beta1_ClusterTopologyBinding(mid, out, nil))
				if !equality.Semantic.DeepEqual(in, out) {
					t.Fatalf("round trip mismatch (-want +got): \n%s", cmp.Diff(in, out))
				}
			})
			t.Run("PodGangMap", func(t *testing.T) {
				in := &v1beta1.PodGangMap{}
				f.Fill(in)
				in.TypeMeta = metav1.TypeMeta{}
				mid, out := &PodGangMap{}, &v1beta1.PodGangMap{}
				require.NoError(t, Convert_v1beta1_PodGangMap_To_v1alpha1_PodGangMap(in, mid, nil))
				require.NoError(t, Convert_v1alpha1_PodGangMap_To_v1beta1_PodGangMap(mid, out, nil))
				if !equality.Semantic.DeepEqual(in, out) {
					t.Fatalf("round trip mismatch (-want +got): \n%s", cmp.Diff(in, out))
				}
			})
		})
	}
}

// TestRoundTripFromAlpha checks that a v1alpha1 PodCliqueSet without the deprecated packDomain and the unused
// podGangStatuses survives a conversion to v1beta1 and back unchanged.
func TestRoundTripFromAlpha(t *testing.T) {
	f := randfill.New().NilChance(0.2).NumElements(0, 2).MaxDepth(12)
	for i := 0; i < roundTripIterations; i++ {
		pcs := &PodCliqueSet{}
		f.Fill(pcs)
		pcs.TypeMeta = metav1.TypeMeta{}
		clearPackDomain(pcs)
		pcs.Status.PodGangStatutes = nil

		beta := &v1beta1.PodCliqueSet{}
		require.NoError(t, Convert_v1alpha1_PodCliqueSet_To_v1beta1_PodCliqueSet(pcs, beta, nil))
		back := &PodCliqueSet{}
		require.NoError(t, Convert_v1beta1_PodCliqueSet_To_v1alpha1_PodCliqueSet(beta, back, nil))
		require.True(t, equality.Semantic.DeepEqual(pcs, back), "PodCliqueSet changed in a v1alpha1 round trip")
	}
}

func clearPackDomain(pcs *PodCliqueSet) {
	clear := func(tc *TopologyConstraint) {
		if tc != nil {
			tc.PackDomain = ""
		}
	}
	clear(pcs.Spec.Template.TopologyConstraint)
	for _, clique := range pcs.Spec.Template.Cliques {
		if clique != nil {
			clear(clique.TopologyConstraint)
		}
	}
	for i := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		clear(pcs.Spec.Template.PodCliqueScalingGroupConfigs[i].TopologyConstraint)
	}
}

// Copyright 2025 The Grove Authors.
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

package component

import (
	"errors"
	"testing"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	"github.com/stretchr/testify/assert"
)

func TestResolveEffectiveTopologyNameForPodCliqueSet(t *testing.T) {
	makePCS := func(mutate func(*grovecorev1beta1.PodCliqueSet)) *grovecorev1beta1.PodCliqueSet {
		pcs := &grovecorev1beta1.PodCliqueSet{
			Spec: grovecorev1beta1.PodCliqueSetSpec{
				Template: grovecorev1beta1.PodCliqueSetTemplateSpec{
					Cliques: []*grovecorev1beta1.PodCliqueTemplateSpec{
						{Name: "worker", Spec: grovecorev1beta1.PodCliqueSpec{Replicas: 1}},
					},
				},
			},
		}
		if mutate != nil {
			mutate(pcs)
		}
		return pcs
	}

	tests := []struct {
		name         string
		setupPCS     func() *grovecorev1beta1.PodCliqueSet
		wantTopology string
		wantErr      error
		wantHasAny   bool
	}{
		{
			name:         "no constraints",
			setupPCS:     func() *grovecorev1beta1.PodCliqueSet { return makePCS(nil) },
			wantTopology: "",
			wantErr:      ErrTopologyNameMissing,
			wantHasAny:   false,
		},
		{
			name: "pcs topology only",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				return makePCS(func(pcs *grovecorev1beta1.PodCliqueSet) {
					pcs.Spec.Template.TopologyConstraint = &grovecorev1beta1.TopologyConstraint{
						TopologyName: "topo-a",
						Pack:         &grovecorev1beta1.TopologyPackConstraint{RequiredDomain: grovecorev1beta1.TopologyDomainRack},
					}
				})
			},
			wantTopology: "topo-a",
			wantHasAny:   true,
		},
		{
			name: "pcs preferred-only topology constraint resolves",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				return makePCS(func(pcs *grovecorev1beta1.PodCliqueSet) {
					pcs.Spec.Template.TopologyConstraint = &grovecorev1beta1.TopologyConstraint{
						TopologyName: "topo-a",
						Pack: &grovecorev1beta1.TopologyPackConstraint{
							PreferredDomain: grovecorev1beta1.TopologyDomainHost,
						},
					}
				})
			},
			wantTopology: "topo-a",
			wantHasAny:   true,
		},
		{
			name: "matching child topology name is allowed",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				return makePCS(func(pcs *grovecorev1beta1.PodCliqueSet) {
					pcs.Spec.Template.TopologyConstraint = &grovecorev1beta1.TopologyConstraint{
						TopologyName: "topo-a",
						Pack:         &grovecorev1beta1.TopologyPackConstraint{RequiredDomain: grovecorev1beta1.TopologyDomainRack},
					}
					pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1beta1.TopologyConstraint{
						TopologyName: "topo-a",
						Pack:         &grovecorev1beta1.TopologyPackConstraint{RequiredDomain: grovecorev1beta1.TopologyDomainHost},
					}
				})
			},
			wantTopology: "topo-a",
			wantHasAny:   true,
		},
		{
			name: "child inherits topology name from pcs",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				return makePCS(func(pcs *grovecorev1beta1.PodCliqueSet) {
					pcs.Spec.Template.TopologyConstraint = &grovecorev1beta1.TopologyConstraint{
						TopologyName: "topo-a",
						Pack:         &grovecorev1beta1.TopologyPackConstraint{RequiredDomain: grovecorev1beta1.TopologyDomainRack},
					}
					pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1beta1.TopologyConstraint{
						Pack: &grovecorev1beta1.TopologyPackConstraint{RequiredDomain: grovecorev1beta1.TopologyDomainHost},
					}
				})
			},
			wantTopology: "topo-a",
			wantHasAny:   true,
		},
		{
			name: "pclq inherits topology name from constrained pcsg",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				return makePCS(func(pcs *grovecorev1beta1.PodCliqueSet) {
					pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1beta1.TopologyConstraint{
						Pack: &grovecorev1beta1.TopologyPackConstraint{RequiredDomain: grovecorev1beta1.TopologyDomainHost},
					}
					pcs.Spec.Template.PodCliqueScalingGroupConfigs = []grovecorev1beta1.PodCliqueScalingGroupConfig{
						{
							Name:        "sg1",
							CliqueNames: []string{"worker"},
							TopologyConstraint: &grovecorev1beta1.TopologyConstraint{
								TopologyName: "topo-a",
								Pack:         &grovecorev1beta1.TopologyPackConstraint{RequiredDomain: grovecorev1beta1.TopologyDomainRack},
							},
						},
					}
				})
			},
			wantTopology: "topo-a",
			wantHasAny:   true,
		},
		{
			name: "standalone clique does not inherit topology from unrelated pcsg",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				return makePCS(func(pcs *grovecorev1beta1.PodCliqueSet) {
					pcs.Spec.Template.Cliques = []*grovecorev1beta1.PodCliqueTemplateSpec{
						{
							Name: "standalone",
							TopologyConstraint: &grovecorev1beta1.TopologyConstraint{
								Pack: &grovecorev1beta1.TopologyPackConstraint{RequiredDomain: grovecorev1beta1.TopologyDomainHost},
							},
							Spec: grovecorev1beta1.PodCliqueSpec{Replicas: 1},
						},
						{
							Name: "grouped",
							TopologyConstraint: &grovecorev1beta1.TopologyConstraint{
								Pack: &grovecorev1beta1.TopologyPackConstraint{RequiredDomain: grovecorev1beta1.TopologyDomainHost},
							},
							Spec: grovecorev1beta1.PodCliqueSpec{Replicas: 1},
						},
					}
					pcs.Spec.Template.PodCliqueScalingGroupConfigs = []grovecorev1beta1.PodCliqueScalingGroupConfig{
						{
							Name:        "sg1",
							CliqueNames: []string{"grouped"},
							TopologyConstraint: &grovecorev1beta1.TopologyConstraint{
								TopologyName: "topo-a",
								Pack:         &grovecorev1beta1.TopologyPackConstraint{RequiredDomain: grovecorev1beta1.TopologyDomainRack},
							},
						},
					}
				})
			},
			wantErr:    ErrTopologyNameMissing,
			wantHasAny: true,
		},
		{
			name: "child-only explicit topology name resolves",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				return makePCS(func(pcs *grovecorev1beta1.PodCliqueSet) {
					pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1beta1.TopologyConstraint{
						TopologyName: "topo-a",
						Pack:         &grovecorev1beta1.TopologyPackConstraint{RequiredDomain: grovecorev1beta1.TopologyDomainHost},
					}
				})
			},
			wantTopology: "topo-a",
			wantHasAny:   true,
		},
		{
			name: "incomplete child topology constraint is rejected",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				return makePCS(func(pcs *grovecorev1beta1.PodCliqueSet) {
					pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1beta1.TopologyConstraint{
						TopologyName: "topo-a",
					}
				})
			},
			wantErr:    ErrPackDomainMissing,
			wantHasAny: true,
		},
		{
			name: "pcsg topology constraint without packDomain is rejected",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				return makePCS(func(pcs *grovecorev1beta1.PodCliqueSet) {
					pcs.Spec.Template.PodCliqueScalingGroupConfigs = []grovecorev1beta1.PodCliqueScalingGroupConfig{
						{
							Name:        "sg1",
							CliqueNames: []string{"worker"},
							TopologyConstraint: &grovecorev1beta1.TopologyConstraint{
								TopologyName: "topo-a",
							},
						},
					}
				})
			},
			wantErr:    ErrPackDomainMissing,
			wantHasAny: true,
		},
		{
			name: "multiple topology names are rejected",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				return makePCS(func(pcs *grovecorev1beta1.PodCliqueSet) {
					pcs.Spec.Template.TopologyConstraint = &grovecorev1beta1.TopologyConstraint{
						TopologyName: "topo-a",
						Pack:         &grovecorev1beta1.TopologyPackConstraint{RequiredDomain: grovecorev1beta1.TopologyDomainRack},
					}
					pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1beta1.TopologyConstraint{
						TopologyName: "topo-b",
						Pack:         &grovecorev1beta1.TopologyPackConstraint{RequiredDomain: grovecorev1beta1.TopologyDomainHost},
					}
				})
			},
			wantErr:    ErrMultipleTopologyNamesUnsupported,
			wantHasAny: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := tc.setupPCS()
			assert.Equal(t, tc.wantHasAny, HasAnyTopologyConstraint(pcs))
			topologyName, err := ResolveEffectiveTopologyNameForPodCliqueSet(pcs)
			if tc.wantErr != nil {
				assert.True(t, errors.Is(err, tc.wantErr))
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tc.wantTopology, topologyName)
		})
	}
}

func TestGetUniqueTopologyDomainsInPodCliqueSet(t *testing.T) {
	pcs := &grovecorev1beta1.PodCliqueSet{
		Spec: grovecorev1beta1.PodCliqueSetSpec{
			Template: grovecorev1beta1.PodCliqueSetTemplateSpec{
				TopologyConstraint: &grovecorev1beta1.TopologyConstraint{
					Pack: &grovecorev1beta1.TopologyPackConstraint{
						RequiredDomain:  grovecorev1beta1.TopologyDomainRack,
						PreferredDomain: grovecorev1beta1.TopologyDomainHost,
					},
				},
				Cliques: []*grovecorev1beta1.PodCliqueTemplateSpec{
					{
						Name: "worker",
						TopologyConstraint: &grovecorev1beta1.TopologyConstraint{
							Pack: &grovecorev1beta1.TopologyPackConstraint{
								PreferredDomain: grovecorev1beta1.TopologyDomainNuma,
							},
						},
					},
				},
				PodCliqueScalingGroupConfigs: []grovecorev1beta1.PodCliqueScalingGroupConfig{
					{
						Name:        "sg1",
						CliqueNames: []string{"worker"},
						TopologyConstraint: &grovecorev1beta1.TopologyConstraint{
							Pack: &grovecorev1beta1.TopologyPackConstraint{RequiredDomain: grovecorev1beta1.TopologyDomainRack},
						},
					},
				},
			},
		},
	}

	assert.ElementsMatch(t,
		[]grovecorev1beta1.TopologyDomain{
			grovecorev1beta1.TopologyDomainRack,
			grovecorev1beta1.TopologyDomainHost,
			grovecorev1beta1.TopologyDomainNuma,
		},
		GetUniqueTopologyDomainsInPodCliqueSet(pcs),
	)
}

func TestFindExplicitTopologyNameForPodCliqueSet(t *testing.T) {
	makePCS := func(mutate func(*grovecorev1beta1.PodCliqueSet)) *grovecorev1beta1.PodCliqueSet {
		pcs := &grovecorev1beta1.PodCliqueSet{
			Spec: grovecorev1beta1.PodCliqueSetSpec{
				Template: grovecorev1beta1.PodCliqueSetTemplateSpec{
					Cliques: []*grovecorev1beta1.PodCliqueTemplateSpec{
						{Name: "worker", Spec: grovecorev1beta1.PodCliqueSpec{Replicas: 1}},
					},
				},
			},
		}
		if mutate != nil {
			mutate(pcs)
		}
		return pcs
	}

	tests := []struct {
		name         string
		setupPCS     func() *grovecorev1beta1.PodCliqueSet
		wantTopology string
		wantErr      error
	}{
		{
			name:         "no constraints",
			setupPCS:     func() *grovecorev1beta1.PodCliqueSet { return makePCS(nil) },
			wantTopology: "",
			wantErr:      ErrTopologyNameMissing,
		},
		{
			name: "pcs topology name is returned first",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				return makePCS(func(pcs *grovecorev1beta1.PodCliqueSet) {
					pcs.Spec.Template.TopologyConstraint = &grovecorev1beta1.TopologyConstraint{
						TopologyName: "topo-a",
					}
					pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1beta1.TopologyConstraint{
						TopologyName: "topo-a",
						Pack:         &grovecorev1beta1.TopologyPackConstraint{RequiredDomain: grovecorev1beta1.TopologyDomainHost},
					}
				})
			},
			wantTopology: "topo-a",
		},
		{
			name: "pcsg topology name is returned when pcs topology name is absent",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				return makePCS(func(pcs *grovecorev1beta1.PodCliqueSet) {
					pcs.Spec.Template.PodCliqueScalingGroupConfigs = []grovecorev1beta1.PodCliqueScalingGroupConfig{
						{
							Name:        "sg1",
							CliqueNames: []string{"worker"},
							TopologyConstraint: &grovecorev1beta1.TopologyConstraint{
								TopologyName: "topo-a",
							},
						},
					}
				})
			},
			wantTopology: "topo-a",
		},
		{
			name: "clique topology name is returned when it is the first explicit topology name",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				return makePCS(func(pcs *grovecorev1beta1.PodCliqueSet) {
					pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1beta1.TopologyConstraint{
						TopologyName: "topo-a",
					}
				})
			},
			wantTopology: "topo-a",
		},
		{
			name: "constraints without any explicit topology name are rejected",
			setupPCS: func() *grovecorev1beta1.PodCliqueSet {
				return makePCS(func(pcs *grovecorev1beta1.PodCliqueSet) {
					pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1beta1.TopologyConstraint{
						Pack: &grovecorev1beta1.TopologyPackConstraint{RequiredDomain: grovecorev1beta1.TopologyDomainHost},
					}
				})
			},
			wantErr: ErrTopologyNameMissing,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			topologyName, err := FindExplicitTopologyNameForPodCliqueSet(tc.setupPCS())
			if tc.wantErr != nil {
				assert.True(t, errors.Is(err, tc.wantErr))
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tc.wantTopology, topologyName)
		})
	}
}

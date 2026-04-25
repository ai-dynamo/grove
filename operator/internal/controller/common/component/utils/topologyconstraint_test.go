// /*
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
// */

package utils

import (
	"errors"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
)

func TestResolveTopologyNameForPodCliqueSet(t *testing.T) {
	makePCS := func(mutate func(*grovecorev1alpha1.PodCliqueSet)) *grovecorev1alpha1.PodCliqueSet {
		pcs := &grovecorev1alpha1.PodCliqueSet{
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
					Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
						{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1}},
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
		setupPCS     func() *grovecorev1alpha1.PodCliqueSet
		wantTopology string
		wantErr      error
		wantPaths    []string
	}{
		{
			name:         "no constraints",
			setupPCS:     func() *grovecorev1alpha1.PodCliqueSet { return makePCS(nil) },
			wantTopology: "",
			wantPaths:    nil,
		},
		{
			name: "pcs topology only",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return makePCS(func(pcs *grovecorev1alpha1.PodCliqueSet) {
					pcs.Spec.Template.TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
						TopologyName: "topo-a",
						PackDomain:   grovecorev1alpha1.TopologyDomainRack,
					}
				})
			},
			wantTopology: "topo-a",
			wantPaths:    nil,
		},
		{
			name: "matching child topology name is allowed",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return makePCS(func(pcs *grovecorev1alpha1.PodCliqueSet) {
					pcs.Spec.Template.TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
						TopologyName: "topo-a",
						PackDomain:   grovecorev1alpha1.TopologyDomainRack,
					}
					pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
						TopologyName: "topo-a",
						PackDomain:   grovecorev1alpha1.TopologyDomainHost,
					}
				})
			},
			wantTopology: "topo-a",
			wantPaths:    nil,
		},
		{
			name: "missing pcs topology name with child constraint",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return makePCS(func(pcs *grovecorev1alpha1.PodCliqueSet) {
					pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
						PackDomain: grovecorev1alpha1.TopologyDomainHost,
					}
				})
			},
			wantErr:   ErrTopologyNameMissing,
			wantPaths: nil,
		},
		{
			name: "child topology name must match pcs topology name",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				return makePCS(func(pcs *grovecorev1alpha1.PodCliqueSet) {
					pcs.Spec.Template.TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
						TopologyName: "topo-a",
						PackDomain:   grovecorev1alpha1.TopologyDomainRack,
					}
					pcs.Spec.Template.Cliques[0].TopologyConstraint = &grovecorev1alpha1.TopologyConstraint{
						TopologyName: "topo-b",
						PackDomain:   grovecorev1alpha1.TopologyDomainHost,
					}
				})
			},
			wantErr:   ErrTopologyNameMismatch,
			wantPaths: []string{"spec.template.cliques[0].topologyConstraint.topologyName"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := tc.setupPCS()
			topologyName, err := ResolveTopologyNameForPodCliqueSet(pcs)
			if tc.wantErr != nil {
				assert.True(t, errors.Is(err, tc.wantErr))
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tc.wantTopology, topologyName)
			pcsTopologyName := ""
			if pcs.Spec.Template.TopologyConstraint != nil {
				pcsTopologyName = pcs.Spec.Template.TopologyConstraint.TopologyName
			}
			assert.ElementsMatch(t, tc.wantPaths, GetChildTopologyNameMismatchPaths(pcs, pcsTopologyName))
		})
	}
}

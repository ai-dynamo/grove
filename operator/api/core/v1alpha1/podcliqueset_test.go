/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTopologyConstraintPackDomainHelpers(t *testing.T) {
	tests := []struct {
		name              string
		constraint        *TopologyConstraint
		hasAnyPackDomain  bool
		requiredDomain    TopologyDomain
		preferredDomain   TopologyDomain
		referencedDomains []TopologyDomain
	}{
		{
			name:             "nil constraint",
			hasAnyPackDomain: false,
		},
		{
			name: "legacy required domain",
			constraint: &TopologyConstraint{
				PackDomain: TopologyDomainRack,
			},
			hasAnyPackDomain:  true,
			requiredDomain:    TopologyDomainRack,
			referencedDomains: []TopologyDomain{TopologyDomainRack},
		},
		{
			name: "new required domain",
			constraint: &TopologyConstraint{
				Pack: &TopologyPackConstraint{RequiredDomain: TopologyDomainHost},
			},
			hasAnyPackDomain:  true,
			requiredDomain:    TopologyDomainHost,
			referencedDomains: []TopologyDomain{TopologyDomainHost},
		},
		{
			name: "preferred-only domain",
			constraint: &TopologyConstraint{
				Pack: &TopologyPackConstraint{PreferredDomain: TopologyDomainHost},
			},
			hasAnyPackDomain:  true,
			preferredDomain:   TopologyDomainHost,
			referencedDomains: []TopologyDomain{TopologyDomainHost},
		},
		{
			name: "required and preferred domains",
			constraint: &TopologyConstraint{
				Pack: &TopologyPackConstraint{
					RequiredDomain:  TopologyDomainRack,
					PreferredDomain: TopologyDomainHost,
				},
			},
			hasAnyPackDomain:  true,
			requiredDomain:    TopologyDomainRack,
			preferredDomain:   TopologyDomainHost,
			referencedDomains: []TopologyDomain{TopologyDomainRack, TopologyDomainHost},
		},
		{
			name: "matching required and preferred domains are returned once",
			constraint: &TopologyConstraint{
				Pack: &TopologyPackConstraint{
					RequiredDomain:  TopologyDomainHost,
					PreferredDomain: TopologyDomainHost,
				},
			},
			hasAnyPackDomain:  true,
			requiredDomain:    TopologyDomainHost,
			preferredDomain:   TopologyDomainHost,
			referencedDomains: []TopologyDomain{TopologyDomainHost},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.hasAnyPackDomain, tt.constraint.HasAnyPackDomain())
			assert.Equal(t, tt.requiredDomain, tt.constraint.RequiredDomain())
			assert.Equal(t, tt.preferredDomain, tt.constraint.PreferredDomain())
			assert.Equal(t, tt.referencedDomains, tt.constraint.ReferencedDomains())
		})
	}
}

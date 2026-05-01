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

package validation

import (
	"context"
	"fmt"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestValidateTASDisabledWithConstraints(t *testing.T) {
	tests := []struct {
		name                  string
		pcsTopologyConstraint *grovecorev1alpha1.TopologyConstraint
		cliques               []*grovecorev1alpha1.PodCliqueTemplateSpec
		pcsgConfigs           []grovecorev1alpha1.PodCliqueScalingGroupConfig
		errorMatchers         []testutils.ErrorMatcher
	}{
		{
			name:                  "Should not allow PCS level constraint when TAS is disabled",
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainZone},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.topologyConstraint"},
			},
		},
		{
			name: "Should not allow PodClique level constraint when TAS is disabled",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainHost},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.cliques[0].topologyConstraint"},
			},
		},
		{
			name: "Should not allow PCSG level constraint when TAS is disabled",
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:               "sg1",
					CliqueNames:        []string{"worker"},
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainRack},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroups[0].topologyConstraint"},
			},
		},
		{
			name:                  "Should report all errors when Topology constraints are set during create when TAS disabled",
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainRegion},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker1",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainZone},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker1-role"},
				},
				{
					Name:               "worker2",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainHost},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker2-role"},
				},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:               "sg1",
					CliqueNames:        []string{"worker1"},
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainRack},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.topologyConstraint"},
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.cliques[0].topologyConstraint"},
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.cliques[1].topologyConstraint"},
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroups[0].topologyConstraint"},
			},
		},
		{
			name:          "Should allow PCS create with no constraints when TAS is disabled",
			errorMatchers: []testutils.ErrorMatcher{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := buildTestPCS(tc.pcsTopologyConstraint, tc.cliques, tc.pcsgConfigs)
			validator := newTopologyConstraintsValidator(pcs, false, []string{})
			errs := validator.validate()
			assert.Len(t, errs, len(tc.errorMatchers), "unexpected number of errors")
			testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
		})
	}
}

func TestValidateTASEnabledWhenDomainNotInClusterTopology(t *testing.T) {
	clusterDomains := []string{
		string(grovecorev1alpha1.TopologyDomainRegion),
		string(grovecorev1alpha1.TopologyDomainZone),
		string(grovecorev1alpha1.TopologyDomainRack),
	}

	tests := []struct {
		name                  string
		pcsTopologyConstraint *grovecorev1alpha1.TopologyConstraint
		cliques               []*grovecorev1alpha1.PodCliqueTemplateSpec
		pcsgConfigs           []grovecorev1alpha1.PodCliqueScalingGroupConfig
		errorMatchers         []testutils.ErrorMatcher
	}{
		{
			name:                  "Should report error when PCS level domain not in cluster topology",
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainHost},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.topologyConstraint"},
			},
		},
		{
			name: "Should report error when PodClique level domain not in cluster topology",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainNuma},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.cliques[0].topologyConstraint"},
			},
		},
		{
			name: "Should report error when PCSG level domain not in cluster topology",
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:               "sg1",
					CliqueNames:        []string{"worker"},
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainDataCenter},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroups[0].topologyConstraint"},
			},
		},
		{
			name:                  "Should allow Valid domains in cluster topology",
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainRegion},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainZone},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{},
		},
		{
			name: "Should return early on invalid domain and not run hierarchical validation",
			// PCS has invalid domain (numa not in cluster topology: region, zone, rack)
			// AND PCS is narrower than child (rack < zone) - would be hierarchical violation
			// Should ONLY report domain error, NOT hierarchical error
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainNuma},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainZone},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				// Should ONLY get domain validation error, NOT hierarchical violation error
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.topologyConstraint"},
			},
		},
		{
			name: "Should return early when child domain is invalid even if hierarchy would be violated",
			// PCS is valid (rack is in cluster topology)
			// Child has invalid domain (numa not in cluster topology)
			// AND there's a hierarchical violation (rack is narrower than region if it existed)
			// Should ONLY report child's domain error
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainRack},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainNuma},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.cliques[0].topologyConstraint"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := buildTestPCS(tc.pcsTopologyConstraint, tc.cliques, tc.pcsgConfigs)
			validator := newTopologyConstraintsValidator(pcs, true, clusterDomains)
			errs := validator.validate()
			assert.Len(t, errs, len(tc.errorMatchers), "unexpected number of errors")
			testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
		})
	}
}

func TestValidateHierarchyViolations(t *testing.T) {
	clusterDomains := []string{
		string(grovecorev1alpha1.TopologyDomainRegion),
		string(grovecorev1alpha1.TopologyDomainZone),
		string(grovecorev1alpha1.TopologyDomainRack),
		string(grovecorev1alpha1.TopologyDomainHost),
		string(grovecorev1alpha1.TopologyDomainNuma),
	}

	tests := []struct {
		name                  string
		pcsTopologyConstraint *grovecorev1alpha1.TopologyConstraint
		cliques               []*grovecorev1alpha1.PodCliqueTemplateSpec
		pcsgConfigs           []grovecorev1alpha1.PodCliqueScalingGroupConfig
		errorMatchers         []testutils.ErrorMatcher
	}{
		{
			name:                  "Should allow PCS topology constraints broader than PodClique",
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainZone},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainHost},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{},
		},
		{
			name:                  "Should forbid PCS topology constraints narrower than PodClique",
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainHost},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainZone},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.topologyConstraint"},
			},
		},
		{
			name:                  "Should forbid PCS topology constraints narrower than PCSG",
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainNuma},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:               "sg1",
					CliqueNames:        []string{"worker"},
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainRack},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.topologyConstraint"},
			},
		},
		{
			name: "Should forbid PCSG topology constraints narrower than PodClique",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainZone},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:               "sg1",
					CliqueNames:        []string{"worker"},
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainHost},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroups[0].topologyConstraint"},
			},
		},
		{
			name:                  "Should allow same level for topology constraints",
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainZone},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainZone},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{},
		},
		{
			name:                  "Should disallow topology constraints at multiple levels",
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainNuma},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker1",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainZone},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker1-role"},
				},
				{
					Name:               "worker2",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainRack},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker2-role"},
				},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:               "sg1",
					CliqueNames:        []string{"worker1", "worker2"},
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainHost},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.topologyConstraint"},
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.topologyConstraint"},
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.topologyConstraint"},
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroups[0].topologyConstraint"},
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroups[0].topologyConstraint"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := buildTestPCS(tc.pcsTopologyConstraint, tc.cliques, tc.pcsgConfigs)
			validator := newTopologyConstraintsValidator(pcs, true, clusterDomains)
			errs := validator.validate()
			assert.Len(t, errs, len(tc.errorMatchers), "unexpected number of errors")
			testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
		})
	}
}

func TestValidateUpdateTopologyNameImmutability(t *testing.T) {
	tests := []struct {
		name             string
		oldPCSConstraint *grovecorev1alpha1.TopologyConstraint
		newPCSConstraint *grovecorev1alpha1.TopologyConstraint
		errorMatchers    []testutils.ErrorMatcher
	}{
		{
			name:             "Should allow when topologyName is unchanged",
			oldPCSConstraint: &grovecorev1alpha1.TopologyConstraint{TopologyName: "topo-a", PackDomain: "zone"},
			newPCSConstraint: &grovecorev1alpha1.TopologyConstraint{TopologyName: "topo-a", PackDomain: "zone"},
			errorMatchers:    []testutils.ErrorMatcher{},
		},
		{
			name:             "Should disallow when topologyName is changed",
			oldPCSConstraint: &grovecorev1alpha1.TopologyConstraint{TopologyName: "topo-a", PackDomain: "zone"},
			newPCSConstraint: &grovecorev1alpha1.TopologyConstraint{TopologyName: "topo-b", PackDomain: "zone"},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.topologyConstraint.topologyName"},
			},
		},
		{
			name:             "Should disallow when topologyName is added",
			oldPCSConstraint: nil,
			newPCSConstraint: &grovecorev1alpha1.TopologyConstraint{TopologyName: "topo-a", PackDomain: "zone"},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.topologyConstraint.topologyName"},
			},
		},
		{
			name:             "Should disallow when topologyName is removed",
			oldPCSConstraint: &grovecorev1alpha1.TopologyConstraint{TopologyName: "topo-a", PackDomain: "zone"},
			newPCSConstraint: nil,
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.topologyConstraint.topologyName"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldPCS := buildTestPCS(tc.oldPCSConstraint, nil, nil)
			newPCS := buildTestPCS(tc.newPCSConstraint, nil, nil)
			validator := newTopologyConstraintsValidator(newPCS, true, nil)
			errs := validator.validateUpdate(oldPCS)
			assert.Len(t, errs, len(tc.errorMatchers), "unexpected number of errors")
			testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
		})
	}
}

func TestValidateHierarchyWithCustomDomains(t *testing.T) {
	// Custom domains ordered broadest to narrowest in the ClusterTopology.
	clusterDomains := []string{"datacenter", "rack", "gpu-module", "host"}

	tests := []struct {
		name                  string
		pcsTopologyConstraint *grovecorev1alpha1.TopologyConstraint
		cliques               []*grovecorev1alpha1.PodCliqueTemplateSpec
		errorMatchers         []testutils.ErrorMatcher
	}{
		{
			name:                  "Should allow custom domain hierarchy: datacenter > host",
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "datacenter"},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "host"},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{},
		},
		{
			name:                  "Should reject custom domain hierarchy: host > datacenter (narrower parent)",
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "host"},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: "datacenter"},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.topologyConstraint"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := buildTestPCS(tc.pcsTopologyConstraint, tc.cliques, nil)
			validator := newTopologyConstraintsValidator(pcs, true, clusterDomains)
			errs := validator.validate()
			assert.Len(t, errs, len(tc.errorMatchers), "unexpected number of errors")
			testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
		})
	}
}

func TestValidateUpdateTopologyConstraintImmutability(t *testing.T) {
	clusterDomains := []string{
		string(grovecorev1alpha1.TopologyDomainRegion),
		string(grovecorev1alpha1.TopologyDomainZone),
		string(grovecorev1alpha1.TopologyDomainRack),
		string(grovecorev1alpha1.TopologyDomainHost),
	}

	zone := grovecorev1alpha1.TopologyDomainZone
	host := grovecorev1alpha1.TopologyDomainHost
	rack := grovecorev1alpha1.TopologyDomainRack
	region := grovecorev1alpha1.TopologyDomainRegion

	workerWithHost := []*grovecorev1alpha1.PodCliqueTemplateSpec{
		{Name: "worker", TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: host}, Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"}},
	}
	workerWithZone := []*grovecorev1alpha1.PodCliqueTemplateSpec{
		{Name: "worker", TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: zone}, Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"}},
	}

	tests := []struct {
		name string
		// Old PCS fields
		oldPCSConstraint *grovecorev1alpha1.TopologyConstraint
		oldCliques       []*grovecorev1alpha1.PodCliqueTemplateSpec
		oldPCSGConfigs   []grovecorev1alpha1.PodCliqueScalingGroupConfig
		// New PCS fields
		newPCSConstraint *grovecorev1alpha1.TopologyConstraint
		newCliques       []*grovecorev1alpha1.PodCliqueTemplateSpec
		newPCSGConfigs   []grovecorev1alpha1.PodCliqueScalingGroupConfig

		errorMatchers []testutils.ErrorMatcher
	}{
		{
			name:             "Should allow when there are no changes to topology constraints",
			oldPCSConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: zone},
			oldCliques:       workerWithHost,
			newPCSConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: zone},
			newCliques:       workerWithHost,
			errorMatchers:    []testutils.ErrorMatcher{},
		},
		{
			name:             "Should disallow when PCS constraint are changed",
			oldPCSConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: zone},
			newPCSConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: rack},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.topologyConstraint"},
			},
		},
		{
			name:             "Should disallow when PCS constraint are added",
			newPCSConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: zone},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.topologyConstraint.topologyName"},
			},
		},
		{
			name:             "Should disallow when PCS constraint are removed",
			oldPCSConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: zone},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.topologyConstraint.topologyName"},
			},
		},
		{
			name:       "Should disallow when PodClique constraint is changed",
			oldCliques: workerWithZone,
			newCliques: workerWithHost,
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.cliques[0].topologyConstraint"},
			},
		},
		{
			name:       "Should disallow when PodClique constraint is added",
			newCliques: workerWithHost,
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.cliques[0].topologyConstraint"},
			},
		},
		{
			name:       "Should disallow when PodClique constraint is removed",
			oldCliques: workerWithHost,
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.cliques[0].topologyConstraint"},
			},
		},
		{
			name: "Should disallow when PCSG constraint is changed",
			oldPCSGConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg1", CliqueNames: []string{"worker"}, TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: zone}},
			},
			newPCSGConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg1", CliqueNames: []string{"worker"}, TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: rack}},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.podCliqueScalingGroups[0].topologyConstraint"},
			},
		},
		{
			name: "Should disallow when PCSG constraint is added",
			oldPCSGConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg1", CliqueNames: []string{"worker"}},
			},
			newPCSGConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg1", CliqueNames: []string{"worker"}, TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: zone}},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.podCliqueScalingGroups[0].topologyConstraint"},
			},
		},
		{
			name: "Should disallow when PCSG constraint is removed",
			oldPCSGConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg1", CliqueNames: []string{"worker"}, TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: zone}},
			},
			newPCSGConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg1", CliqueNames: []string{"worker"}},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.podCliqueScalingGroups[0].topologyConstraint"},
			},
		},
		{
			name:             "Should disallow when multiple constraint are changed",
			oldPCSConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: region},
			oldCliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker1", TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: zone}, Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker1-role"}},
				{Name: "worker2", TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: rack}, Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker2-role"}},
			},
			newPCSConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: zone},
			newCliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker1", TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: rack}, Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker1-role"}},
				{Name: "worker2", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker2-role"}},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.topologyConstraint"},
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.cliques[0].topologyConstraint"},
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.cliques[1].topologyConstraint"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldPCS := buildTestPCS(tc.oldPCSConstraint, tc.oldCliques, tc.oldPCSGConfigs)
			newPCS := buildTestPCS(tc.newPCSConstraint, tc.newCliques, tc.newPCSGConfigs)
			validator := newTopologyConstraintsValidator(newPCS, true, clusterDomains)
			errs := validator.validateUpdate(oldPCS)
			assert.Len(t, errs, len(tc.errorMatchers), "unexpected number of errors")
			testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
		})
	}
}

// Helper functions to reduce test duplication

// buildTestPCS constructs a PodCliqueSet for testing based on provided parameters.
// If cliques is nil or empty, a default worker clique will be added.
func buildTestPCS(pcsConstraint *grovecorev1alpha1.TopologyConstraint,
	cliques []*grovecorev1alpha1.PodCliqueTemplateSpec,
	pcsgConfigs []grovecorev1alpha1.PodCliqueScalingGroupConfig) *grovecorev1alpha1.PodCliqueSet {
	const defaultTopologyName = "test-topology"
	normalizeConstraint := func(tc *grovecorev1alpha1.TopologyConstraint) *grovecorev1alpha1.TopologyConstraint {
		if tc == nil {
			return nil
		}
		normalized := tc.DeepCopy()
		if normalized.TopologyName == "" {
			normalized.TopologyName = defaultTopologyName
		}
		return normalized
	}

	builder := testutils.NewPodCliqueSetBuilder("test-pcs", "default", uuid.NewUUID()).
		WithReplicas(1).
		WithTopologyConstraint(normalizeConstraint(pcsConstraint))

	if len(cliques) == 0 {
		builder = builder.WithPodCliqueTemplateSpec(testutils.NewBasicPodCliqueTemplateSpec("worker"))
	} else {
		for _, clique := range cliques {
			clique = clique.DeepCopy()
			clique.TopologyConstraint = normalizeConstraint(clique.TopologyConstraint)
			builder = builder.WithPodCliqueTemplateSpec(clique)
		}
	}

	for _, pcsg := range pcsgConfigs {
		pcsg = *pcsg.DeepCopy()
		pcsg.TopologyConstraint = normalizeConstraint(pcsg.TopologyConstraint)
		builder = builder.WithPodCliqueScalingGroupConfig(pcsg)
	}

	return builder.Build()
}

func TestResolveTopologyDomains(t *testing.T) {
	tests := []struct {
		name                   string
		setupPCS               func() *grovecorev1alpha1.PodCliqueSet
		pcsConstraint          *grovecorev1alpha1.TopologyConstraint
		cliques                []*grovecorev1alpha1.PodCliqueTemplateSpec
		pcsgConfigs            []grovecorev1alpha1.PodCliqueScalingGroupConfig
		clusterTopologyObjects []client.Object
		expectedDomains        []string
		expectedErrorMatchers  []testutils.ErrorMatcher
		setupClient            func() client.Client
	}{
		{
			name: "Happy path: PCS has constraint and ClusterTopology exists",
			pcsConstraint: &grovecorev1alpha1.TopologyConstraint{
				TopologyName: "my-topo",
				PackDomain:   "zone",
			},
			clusterTopologyObjects: []client.Object{
				&grovecorev1alpha1.ClusterTopology{
					ObjectMeta: v1.ObjectMeta{Name: "my-topo"},
					Spec: grovecorev1alpha1.ClusterTopologySpec{
						Levels: []grovecorev1alpha1.TopologyLevel{
							{Domain: "zone", Key: "topology.kubernetes.io/zone"},
							{Domain: "host", Key: "kubernetes.io/hostname"},
						},
					},
				},
			},
			expectedDomains:       []string{"zone", "host"},
			expectedErrorMatchers: []testutils.ErrorMatcher{},
			setupClient:           nil,
		},
		{
			name:          "Child topologyName matching PCS is allowed",
			pcsConstraint: &grovecorev1alpha1.TopologyConstraint{TopologyName: "my-topo", PackDomain: "zone"},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
						TopologyName: "my-topo",
						PackDomain:   "host",
					},
					Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			clusterTopologyObjects: []client.Object{
				&grovecorev1alpha1.ClusterTopology{
					ObjectMeta: v1.ObjectMeta{Name: "my-topo"},
					Spec: grovecorev1alpha1.ClusterTopologySpec{
						Levels: []grovecorev1alpha1.TopologyLevel{
							{Domain: "zone", Key: "topology.kubernetes.io/zone"},
							{Domain: "host", Key: "kubernetes.io/hostname"},
						},
					},
				},
			},
			expectedDomains:       []string{"zone", "host"},
			expectedErrorMatchers: []testutils.ErrorMatcher{},
			setupClient:           nil,
		},
		{
			name: "Incomplete PCS topology constraint is rejected",
			setupPCS: func() *grovecorev1alpha1.PodCliqueSet {
				pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", uuid.NewUUID()).
					WithReplicas(1).
					WithTopologyConstraint(&grovecorev1alpha1.TopologyConstraint{PackDomain: "zone"}).
					WithPodCliqueTemplateSpec(testutils.NewBasicPodCliqueTemplateSpec("worker")).
					Build()
				return pcs
			},
			clusterTopologyObjects: []client.Object{},
			expectedDomains:        nil,
			expectedErrorMatchers: []testutils.ErrorMatcher{
				{
					ErrorType: field.ErrorTypeRequired,
					Field:     "spec.template.topologyConstraint.topologyName",
				},
			},
			setupClient: nil,
		},
		{
			name:          "Child-only explicit topology constraint is allowed",
			pcsConstraint: nil,
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
						TopologyName: "my-topo",
						PackDomain:   "host",
					},
					Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			clusterTopologyObjects: []client.Object{
				&grovecorev1alpha1.ClusterTopology{
					ObjectMeta: v1.ObjectMeta{Name: "my-topo"},
					Spec: grovecorev1alpha1.ClusterTopologySpec{
						Levels: []grovecorev1alpha1.TopologyLevel{
							{Domain: "zone", Key: "topology.kubernetes.io/zone"},
							{Domain: "host", Key: "kubernetes.io/hostname"},
						},
					},
				},
			},
			expectedDomains:       []string{"zone", "host"},
			expectedErrorMatchers: []testutils.ErrorMatcher{},
			setupClient:           nil,
		},
		{
			name:          "Child topologyName mismatch is rejected",
			pcsConstraint: &grovecorev1alpha1.TopologyConstraint{TopologyName: "my-topo", PackDomain: "zone"},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name: "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{
						TopologyName: "other-topo",
						PackDomain:   "host",
					},
					Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			clusterTopologyObjects: []client.Object{},
			expectedDomains:        nil,
			expectedErrorMatchers: []testutils.ErrorMatcher{
				{
					ErrorType: field.ErrorTypeInvalid,
					Field:     "spec.template.cliques[0].topologyConstraint.topologyName",
				},
				{
					ErrorType: field.ErrorTypeInvalid,
					Field:     "spec.template.topologyConstraint.topologyName",
				},
			},
			setupClient: nil,
		},
		{
			name:                   "No constraints: PCS has no topology constraints at all",
			pcsConstraint:          nil,
			clusterTopologyObjects: []client.Object{},
			expectedDomains:        nil,
			expectedErrorMatchers:  []testutils.ErrorMatcher{},
			setupClient:            nil,
		},
		{
			name: "CT not found: ClusterTopology referenced by topologyName does not exist",
			pcsConstraint: &grovecorev1alpha1.TopologyConstraint{
				TopologyName: "missing-topo",
				PackDomain:   "zone",
			},
			clusterTopologyObjects: []client.Object{},
			expectedDomains:        nil,
			expectedErrorMatchers: []testutils.ErrorMatcher{
				{
					ErrorType: field.ErrorTypeInvalid,
					Field:     "spec.template.topologyConstraint.topologyName",
				},
			},
			setupClient: nil,
		},
		{
			name: "API error: non-404 error is surfaced as InternalError",
			pcsConstraint: &grovecorev1alpha1.TopologyConstraint{
				TopologyName: "broken-topo",
				PackDomain:   "zone",
			},
			clusterTopologyObjects: []client.Object{},
			expectedDomains:        nil,
			expectedErrorMatchers: []testutils.ErrorMatcher{
				{
					ErrorType: field.ErrorTypeInternal,
					Field:     "spec.template.topologyConstraint.topologyName",
				},
			},
			setupClient: func() client.Client {
				injectedErr := apierrors.NewInternalError(fmt.Errorf("API server unavailable"))
				return testutils.NewTestClientBuilder().
					RecordErrorForObjects(testutils.ClientMethodGet, injectedErr, client.ObjectKey{Name: "broken-topo"}).
					Build()
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var pcs *grovecorev1alpha1.PodCliqueSet
			if tc.setupPCS != nil {
				pcs = tc.setupPCS()
			} else {
				pcs = buildTestPCS(tc.pcsConstraint, tc.cliques, tc.pcsgConfigs)
			}
			var fakeClient client.Client
			if tc.setupClient != nil {
				fakeClient = tc.setupClient()
			} else {
				fakeClient = testutils.CreateDefaultFakeClient(tc.clusterTopologyObjects)
			}

			validator := &pcsValidator{
				pcs:    pcs,
				client: fakeClient,
			}

			domains, errs := validator.resolveTopologyDomains(context.Background())

			if tc.expectedDomains == nil {
				if domains != nil {
					t.Errorf("expected domains to be nil, got %v", domains)
				}
			} else {
				if len(domains) != len(tc.expectedDomains) {
					t.Errorf("expected %d domains, got %d: %v", len(tc.expectedDomains), len(domains), domains)
				} else {
					for i, expected := range tc.expectedDomains {
						if domains[i] != expected {
							t.Errorf("expected domain[%d]=%q, got %q", i, expected, domains[i])
						}
					}
				}
			}

			assert.Len(t, errs, len(tc.expectedErrorMatchers), "unexpected number of errors")
			testutils.AssertErrorMatches(t, errs, tc.expectedErrorMatchers)
		})
	}
}

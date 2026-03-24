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
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

const (
	// Topology domain constants used across tests.
	domainRegion     = grovecorev1alpha1.TopologyDomain("region")
	domainZone       = grovecorev1alpha1.TopologyDomain("zone")
	domainRack       = grovecorev1alpha1.TopologyDomain("rack")
	domainHost       = grovecorev1alpha1.TopologyDomain("host")
	domainNuma       = grovecorev1alpha1.TopologyDomain("numa")
	domainDataCenter = grovecorev1alpha1.TopologyDomain("datacenter")

	// Custom domain constants for testing free-form topology domains.
	domainNVLDomain   = grovecorev1alpha1.TopologyDomain("nvl-domain")
	domainBlade       = grovecorev1alpha1.TopologyDomain("blade")
	domainSwitchGroup = grovecorev1alpha1.TopologyDomain("switch-group")

	// Topology name constants.
	topologyNameDefault = "my-topo"
	topologyNameGPU     = "gpu-topo"
)

func TestValidateTASDisabledWithConstraints(t *testing.T) {
	tests := []struct {
		name                  string
		pcsTopologyConstraint *grovecorev1alpha1.PodCliqueSetTopologyConstraint
		cliques               []*grovecorev1alpha1.PodCliqueTemplateSpec
		pcsgConfigs           []grovecorev1alpha1.PodCliqueScalingGroupConfig
		errorMatchers         []testutils.ErrorMatcher
	}{
		{
			name:                  "Should not allow PCS level constraint when TAS is disabled",
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainZone},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.topologyConstraint"},
			},
		},
		{
			name: "Should not allow PodClique level constraint when TAS is disabled",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainHost},
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
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainRack},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroupConfigs[0].topologyConstraint"},
			},
		},
		{
			name:                  "Should report all errors when Topology constraints are set during create when TAS disabled",
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainRegion},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker1",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainZone},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker1-role"},
				},
				{
					Name:               "worker2",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainHost},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker2-role"},
				},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:               "sg1",
					CliqueNames:        []string{"worker1"},
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainRack},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.topologyConstraint"},
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.cliques[0].topologyConstraint"},
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.cliques[1].topologyConstraint"},
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroupConfigs[0].topologyConstraint"},
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
			validator := newTopologyConstraintsValidator(pcs, false, nil)
			errs := validator.validate()
			testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
		})
	}
}

func TestValidateTopologyNamePresence(t *testing.T) {
	levels := []grovecorev1alpha1.TopologyLevel{
		{Domain: domainRegion, Key: "topology.kubernetes.io/region"},
		{Domain: domainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: domainRack, Key: "topology.example.com/rack"},
	}

	tests := []struct {
		name                  string
		pcsTopologyConstraint *grovecorev1alpha1.PodCliqueSetTopologyConstraint
		cliques               []*grovecorev1alpha1.PodCliqueTemplateSpec
		pcsgConfigs           []grovecorev1alpha1.PodCliqueScalingGroupConfig
		errorMatchers         []testutils.ErrorMatcher
	}{
		{
			name:                  "Should require topologyName when PCS packDomain is set",
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{PackDomain: domainZone},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeRequired, Field: "spec.template.topologyConstraint.topologyName"},
			},
		},
		{
			name: "Should require topologyName when PodClique packDomain is set",
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainHost},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeRequired, Field: "spec.template.topologyConstraint.topologyName"},
			},
		},
		{
			name: "Should require topologyName when PCSG packDomain is set",
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:               "sg1",
					CliqueNames:        []string{"worker"},
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainRack},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeRequired, Field: "spec.template.topologyConstraint.topologyName"},
			},
		},
		{
			name:                  "Should reject topologyName when no packDomain is set",
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.topologyConstraint.topologyName"},
			},
		},
		{
			name:                  "Should allow topologyName with PCS packDomain",
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainZone},
			errorMatchers:         []testutils.ErrorMatcher{},
		},
		{
			name:          "Should allow no constraints at all",
			errorMatchers: []testutils.ErrorMatcher{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := buildTestPCS(tc.pcsTopologyConstraint, tc.cliques, tc.pcsgConfigs)
			validator := newTopologyConstraintsValidator(pcs, true, levels)
			errs := validator.validate()
			testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
		})
	}
}

func TestValidateTASEnabledWhenDomainNotInClusterTopology(t *testing.T) {
	levels := []grovecorev1alpha1.TopologyLevel{
		{Domain: domainRegion, Key: "topology.kubernetes.io/region"},
		{Domain: domainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: domainRack, Key: "topology.example.com/rack"},
	}

	tests := []struct {
		name                  string
		pcsTopologyConstraint *grovecorev1alpha1.PodCliqueSetTopologyConstraint
		cliques               []*grovecorev1alpha1.PodCliqueTemplateSpec
		pcsgConfigs           []grovecorev1alpha1.PodCliqueScalingGroupConfig
		errorMatchers         []testutils.ErrorMatcher
	}{
		{
			name:                  "Should report error when PCS level domain not in cluster topology",
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainHost},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.topologyConstraint"},
			},
		},
		{
			name: "Should report error when PodClique level domain not in cluster topology",
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainNuma},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.cliques[0].topologyConstraint"},
			},
		},
		{
			name: "Should report error when PCSG level domain not in cluster topology",
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:               "sg1",
					CliqueNames:        []string{"worker"},
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainDataCenter},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroupConfigs[0].topologyConstraint"},
			},
		},
		{
			name:                  "Should allow valid domains in cluster topology",
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainRegion},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainZone},
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
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainNuma},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainZone},
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
			// Should ONLY report child's domain error
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainRack},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainNuma},
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
			validator := newTopologyConstraintsValidator(pcs, true, levels)
			errs := validator.validate()
			testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
		})
	}
}

func TestValidateHierarchyViolations(t *testing.T) {
	levels := []grovecorev1alpha1.TopologyLevel{
		{Domain: domainRegion, Key: "topology.kubernetes.io/region"},
		{Domain: domainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: domainRack, Key: "topology.example.com/rack"},
		{Domain: domainHost, Key: "kubernetes.io/hostname"},
		{Domain: domainNuma, Key: "topology.example.com/numa"},
	}

	tests := []struct {
		name                  string
		pcsTopologyConstraint *grovecorev1alpha1.PodCliqueSetTopologyConstraint
		cliques               []*grovecorev1alpha1.PodCliqueTemplateSpec
		pcsgConfigs           []grovecorev1alpha1.PodCliqueScalingGroupConfig
		errorMatchers         []testutils.ErrorMatcher
	}{
		{
			name:                  "Should allow PCS topology constraints broader than PodClique",
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainZone},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainHost},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{},
		},
		{
			name:                  "Should forbid PCS topology constraints narrower than PodClique",
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainHost},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainZone},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.topologyConstraint"},
			},
		},
		{
			name:                  "Should forbid PCS topology constraints narrower than PCSG",
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainNuma},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:               "sg1",
					CliqueNames:        []string{"worker"},
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainRack},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.topologyConstraint"},
			},
		},
		{
			name: "Should forbid PCSG topology constraints narrower than PodClique",
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainZone},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:               "sg1",
					CliqueNames:        []string{"worker"},
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainHost},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroupConfigs[0].topologyConstraint"},
			},
		},
		{
			name:                  "Should allow same level for topology constraints",
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainZone},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainZone},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{},
		},
		{
			name:                  "Should disallow topology constraints at multiple levels",
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainNuma},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker1",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainZone},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker1-role"},
				},
				{
					Name:               "worker2",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainRack},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker2-role"},
				},
			},
			pcsgConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{
					Name:               "sg1",
					CliqueNames:        []string{"worker1", "worker2"},
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainHost},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.topologyConstraint"},
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.podCliqueScalingGroupConfigs[0].topologyConstraint"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := buildTestPCS(tc.pcsTopologyConstraint, tc.cliques, tc.pcsgConfigs)
			validator := newTopologyConstraintsValidator(pcs, true, levels)
			errs := validator.validate()
			testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
		})
	}
}

func TestValidateHierarchyWithCustomDomains(t *testing.T) {
	// Hierarchy ordering is determined by position in the levels array, not by any global ordering.
	// Here: switch-group (broadest) > nvl-domain > blade (narrowest)
	levels := []grovecorev1alpha1.TopologyLevel{
		{Domain: domainSwitchGroup, Key: "nvidia.com/switch-group"},
		{Domain: domainNVLDomain, Key: "nvidia.com/nvl-domain"},
		{Domain: domainBlade, Key: "nvidia.com/blade"},
	}

	tests := []struct {
		name                  string
		pcsTopologyConstraint *grovecorev1alpha1.PodCliqueSetTopologyConstraint
		cliques               []*grovecorev1alpha1.PodCliqueTemplateSpec
		errorMatchers         []testutils.ErrorMatcher
	}{
		{
			name:                  "Should allow PCS broader than child with custom domains",
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameGPU, PackDomain: domainSwitchGroup},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainBlade},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{},
		},
		{
			name:                  "Should forbid PCS narrower than child with custom domains",
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameGPU, PackDomain: domainBlade},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainSwitchGroup},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeInvalid, Field: "spec.template.topologyConstraint"},
			},
		},
		{
			name:                  "Should allow same custom domain at both levels",
			pcsTopologyConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameGPU, PackDomain: domainNVLDomain},
			cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{
					Name:               "worker",
					TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainNVLDomain},
					Spec:               grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"},
				},
			},
			errorMatchers: []testutils.ErrorMatcher{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := buildTestPCS(tc.pcsTopologyConstraint, tc.cliques, nil)
			validator := newTopologyConstraintsValidator(pcs, true, levels)
			errs := validator.validate()
			testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
		})
	}
}

func TestValidateUpdateTopologyConstraintImmutability(t *testing.T) {
	workerWithHost := []*grovecorev1alpha1.PodCliqueTemplateSpec{
		{Name: "worker", TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainHost}, Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"}},
	}
	workerWithZone := []*grovecorev1alpha1.PodCliqueTemplateSpec{
		{Name: "worker", TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainZone}, Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker-role"}},
	}

	tests := []struct {
		name string
		// Old PCS fields
		oldPCSConstraint *grovecorev1alpha1.PodCliqueSetTopologyConstraint
		oldCliques       []*grovecorev1alpha1.PodCliqueTemplateSpec
		oldPCSGConfigs   []grovecorev1alpha1.PodCliqueScalingGroupConfig
		// New PCS fields
		newPCSConstraint *grovecorev1alpha1.PodCliqueSetTopologyConstraint
		newCliques       []*grovecorev1alpha1.PodCliqueTemplateSpec
		newPCSGConfigs   []grovecorev1alpha1.PodCliqueScalingGroupConfig

		errorMatchers []testutils.ErrorMatcher
	}{
		{
			name:             "Should allow when there are no changes to topology constraints",
			oldPCSConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainZone},
			oldCliques:       workerWithHost,
			newPCSConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainZone},
			newCliques:       workerWithHost,
			errorMatchers:    []testutils.ErrorMatcher{},
		},
		{
			name:             "Should disallow when PCS packDomain is changed",
			oldPCSConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainZone},
			newPCSConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainRack},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.topologyConstraint"},
			},
		},
		{
			name:             "Should disallow when PCS topologyName is changed",
			oldPCSConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainZone},
			newPCSConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameGPU, PackDomain: domainZone},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.topologyConstraint"},
			},
		},
		{
			name:             "Should disallow when PCS constraint is added",
			newPCSConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainZone},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.topologyConstraint"},
			},
		},
		{
			name:             "Should disallow when PCS constraint is removed",
			oldPCSConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainZone},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.topologyConstraint"},
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
				{Name: "sg1", CliqueNames: []string{"worker"}, TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainZone}},
			},
			newPCSGConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg1", CliqueNames: []string{"worker"}, TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainRack}},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.podCliqueScalingGroupConfigs[0].topologyConstraint"},
			},
		},
		{
			name: "Should disallow when PCSG constraint is added",
			oldPCSGConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg1", CliqueNames: []string{"worker"}},
			},
			newPCSGConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg1", CliqueNames: []string{"worker"}, TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainZone}},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.podCliqueScalingGroupConfigs[0].topologyConstraint"},
			},
		},
		{
			name: "Should disallow when PCSG constraint is removed",
			oldPCSGConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg1", CliqueNames: []string{"worker"}, TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainZone}},
			},
			newPCSGConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
				{Name: "sg1", CliqueNames: []string{"worker"}},
			},
			errorMatchers: []testutils.ErrorMatcher{
				{ErrorType: field.ErrorTypeForbidden, Field: "spec.template.podCliqueScalingGroupConfigs[0].topologyConstraint"},
			},
		},
		{
			name:             "Should disallow when multiple constraints are changed",
			oldPCSConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainRegion},
			oldCliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker1", TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainZone}, Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker1-role"}},
				{Name: "worker2", TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainRack}, Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker2-role"}},
			},
			newPCSConstraint: &grovecorev1alpha1.PodCliqueSetTopologyConstraint{TopologyName: topologyNameDefault, PackDomain: domainZone},
			newCliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "worker1", TopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: domainRack}, Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 1, RoleName: "worker1-role"}},
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
			validator := newTopologyConstraintsValidator(newPCS, true, nil)
			errs := validator.validateUpdate(oldPCS)
			testutils.AssertErrorMatches(t, errs, tc.errorMatchers)
		})
	}
}

// Helper functions to reduce test duplication

// buildTestPCS constructs a PodCliqueSet for testing based on provided parameters.
// If cliques is nil or empty, a default worker clique will be added.
func buildTestPCS(pcsConstraint *grovecorev1alpha1.PodCliqueSetTopologyConstraint,
	cliques []*grovecorev1alpha1.PodCliqueTemplateSpec,
	pcsgConfigs []grovecorev1alpha1.PodCliqueScalingGroupConfig) *grovecorev1alpha1.PodCliqueSet {
	builder := testutils.NewPodCliqueSetBuilder("test-pcs", "default", uuid.NewUUID()).
		WithReplicas(1).
		WithTopologyConstraint(pcsConstraint)

	if len(cliques) == 0 {
		builder = builder.WithPodCliqueTemplateSpec(testutils.NewBasicPodCliqueTemplateSpec("worker"))
	} else {
		for _, clique := range cliques {
			builder = builder.WithPodCliqueTemplateSpec(clique)
		}
	}

	for _, pcsg := range pcsgConfigs {
		builder = builder.WithPodCliqueScalingGroupConfig(pcsg)
	}

	return builder.Build()
}

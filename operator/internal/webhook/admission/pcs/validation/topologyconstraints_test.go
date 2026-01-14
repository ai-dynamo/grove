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
	"fmt"
	"strings"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestValidateTASDisabledWithConstraints(t *testing.T) {
	tests := []struct {
		name                  string
		pcsTopologyConstraint *grovecorev1alpha1.TopologyConstraint
		cliques               []*grovecorev1alpha1.PodCliqueTemplateSpec
		pcsgConfigs           []grovecorev1alpha1.PodCliqueScalingGroupConfig
		errorMatchers         []errorMatcher
	}{
		{
			name:                  "Should not allow PCS level constraint when TAS is disabled",
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainZone},
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeInvalid, field: "spec.template.topologyConstraint"},
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
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeInvalid, field: "spec.template.cliques[0].topologyConstraint"},
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
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeInvalid, field: "spec.template.podCliqueScalingGroupConfigs[0].topologyConstraint"},
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
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeInvalid, field: "spec.template.topologyConstraint"},
				{errorType: field.ErrorTypeInvalid, field: "spec.template.cliques[0].topologyConstraint"},
				{errorType: field.ErrorTypeInvalid, field: "spec.template.cliques[1].topologyConstraint"},
				{errorType: field.ErrorTypeInvalid, field: "spec.template.podCliqueScalingGroupConfigs[0].topologyConstraint"},
			},
		},
		{
			name:          "Should allow PCS create with no constraints when TAS is disabled",
			errorMatchers: []errorMatcher{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := buildTestPCS(tc.pcsTopologyConstraint, tc.cliques, tc.pcsgConfigs)
			validator := newTopologyConstraintsValidator(pcs, false, []string{})
			errs := validator.validate()
			assertErrorMatches(t, errs, tc.errorMatchers)
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
		errorMatchers         []errorMatcher
	}{
		{
			name:                  "Should report error when PCS level domain not in cluster topology",
			pcsTopologyConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: grovecorev1alpha1.TopologyDomainHost},
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeInvalid, field: "spec.template.topologyConstraint"},
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
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeInvalid, field: "spec.template.cliques[0].topologyConstraint"},
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
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeInvalid, field: "spec.template.podCliqueScalingGroupConfigs[0].topologyConstraint"},
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
			errorMatchers: []errorMatcher{},
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
			errorMatchers: []errorMatcher{
				// Should ONLY get domain validation error, NOT hierarchical violation error
				{errorType: field.ErrorTypeInvalid, field: "spec.template.topologyConstraint"},
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
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeInvalid, field: "spec.template.cliques[0].topologyConstraint"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := buildTestPCS(tc.pcsTopologyConstraint, tc.cliques, tc.pcsgConfigs)
			validator := newTopologyConstraintsValidator(pcs, true, clusterDomains)
			errs := validator.validate()
			assertErrorMatches(t, errs, tc.errorMatchers)
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
		errorMatchers         []errorMatcher
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
			errorMatchers: []errorMatcher{},
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
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeInvalid, field: "spec.template.topologyConstraint"},
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
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeInvalid, field: "spec.template.topologyConstraint"},
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
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeInvalid, field: "spec.template.podCliqueScalingGroupConfigs[0].topologyConstraint"},
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
			errorMatchers: []errorMatcher{},
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
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeInvalid, field: "spec.template.topologyConstraint"},
				{errorType: field.ErrorTypeInvalid, field: "spec.template.podCliqueScalingGroupConfigs[0].topologyConstraint"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcs := buildTestPCS(tc.pcsTopologyConstraint, tc.cliques, tc.pcsgConfigs)
			validator := newTopologyConstraintsValidator(pcs, true, clusterDomains)
			errs := validator.validate()
			assertErrorMatches(t, errs, tc.errorMatchers)
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

		errorMatchers []errorMatcher
	}{
		{
			name:             "Should allow when there are no changes to topology constraints",
			oldPCSConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: zone},
			oldCliques:       workerWithHost,
			newPCSConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: zone},
			newCliques:       workerWithHost,
			errorMatchers:    []errorMatcher{},
		},
		{
			name:             "Should disallow when PCS constraint are changed",
			oldPCSConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: zone},
			newPCSConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: rack},
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeForbidden, field: "spec.template.topologyConstraint"},
			},
		},
		{
			name:             "Should disallow when PCS constraint are added",
			newPCSConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: zone},
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeForbidden, field: "spec.template.topologyConstraint"},
			},
		},
		{
			name:             "Should disallow when PCS constraint are removed",
			oldPCSConstraint: &grovecorev1alpha1.TopologyConstraint{PackDomain: zone},
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeForbidden, field: "spec.template.topologyConstraint"},
			},
		},
		{
			name:       "Should disallow when PodClique constraint is changed",
			oldCliques: workerWithZone,
			newCliques: workerWithHost,
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeForbidden, field: "spec.template.cliques[0].topologyConstraint"},
			},
		},
		{
			name:       "Should disallow when PodClique constraint is added",
			newCliques: workerWithHost,
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeForbidden, field: "spec.template.cliques[0].topologyConstraint"},
			},
		},
		{
			name:       "Should disallow when PodClique constraint is removed",
			oldCliques: workerWithHost,
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeForbidden, field: "spec.template.cliques[0].topologyConstraint"},
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
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeForbidden, field: "spec.template.podCliqueScalingGroupConfigs[0].topologyConstraint"},
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
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeForbidden, field: "spec.template.podCliqueScalingGroupConfigs[0].topologyConstraint"},
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
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeForbidden, field: "spec.template.podCliqueScalingGroupConfigs[0].topologyConstraint"},
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
			errorMatchers: []errorMatcher{
				{errorType: field.ErrorTypeForbidden, field: "spec.template.topologyConstraint"},
				{errorType: field.ErrorTypeForbidden, field: "spec.template.cliques[0].topologyConstraint"},
				{errorType: field.ErrorTypeForbidden, field: "spec.template.cliques[1].topologyConstraint"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldPCS := buildTestPCS(tc.oldPCSConstraint, tc.oldCliques, tc.oldPCSGConfigs)
			newPCS := buildTestPCS(tc.newPCSConstraint, tc.newCliques, tc.newPCSGConfigs)
			validator := newTopologyConstraintsValidator(newPCS, true, clusterDomains)
			errs := validator.validateUpdate(oldPCS)
			assertErrorMatches(t, errs, tc.errorMatchers)
		})
	}
}

// Helper functions to reduce test duplication

// buildTestPCS constructs a PodCliqueSet for testing based on provided parameters.
// If cliques is nil or empty, a default worker clique will be added.
func buildTestPCS(pcsConstraint *grovecorev1alpha1.TopologyConstraint,
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

type errorMatcher struct {
	field     string
	errorType field.ErrorType
}

func (m errorMatcher) Matches(errs field.ErrorList) bool {
	for _, err := range errs {
		if err.Field == m.field && err.Type == m.errorType {
			return true
		}
	}
	return false
}

func assertErrorMatches(t *testing.T, errs field.ErrorList, expectedErrorMatchers []errorMatcher) {
	unmatched := make([]errorMatcher, 0, len(expectedErrorMatchers))
	for _, matcher := range expectedErrorMatchers {
		if !matcher.Matches(errs) {
			unmatched = append(unmatched, matcher)
		}
	}
	assert.True(t, len(unmatched) == 0, aggregateMatchingErrorMessage(unmatched, errs))
}

func aggregateMatchingErrorMessage(unmatched []errorMatcher, errs field.ErrorList) string {
	var sb strings.Builder
	sb.WriteString("Expected error to consist of:\n")
	for _, um := range unmatched {
		sb.WriteString(fmt.Sprintf("  Field: %s, Type: %s\n", um.field, um.errorType))
	}
	sb.WriteString("But got errors:\n")
	for _, err := range errs {
		sb.WriteString(fmt.Sprintf("  Field: %s, Type: %s\n", err.Field, err.Type))
	}
	return sb.String()
}

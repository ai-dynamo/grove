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

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"k8s.io/apimachinery/pkg/util/sets"
)

var (
	// ErrTopologyNameMissing indicates that a topology constraint is incomplete and does not specify both topologyName and packDomain.
	ErrTopologyNameMissing = errors.New("topology constraints require both topologyName and packDomain")
	// ErrMultipleTopologyNamesUnsupported indicates that topology constraints within a single PCS reference different topology names.
	ErrMultipleTopologyNamesUnsupported = errors.New("multiple topology names within a single PodCliqueSet are not supported")
)

// HasAnyTopologyConstraint reports whether the PCS contains a topology constraint at any supported level.
func HasAnyTopologyConstraint(pcs *grovecorev1alpha1.PodCliqueSet) bool {
	if pcs.Spec.Template.TopologyConstraint != nil {
		return true
	}
	for _, clique := range pcs.Spec.Template.Cliques {
		if clique.TopologyConstraint != nil {
			return true
		}
	}
	for _, pcsg := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if pcsg.TopologyConstraint != nil {
			return true
		}
	}
	return false
}

// ResolveTopologyNameForPodCliqueSet resolves the single topologyName used by all explicit topology constraints in the PCS.
// Callers that need to distinguish "no topology constraints at all" from invalid topology constraints
// must first call HasAnyTopologyConstraint.
func ResolveTopologyNameForPodCliqueSet(pcs *grovecorev1alpha1.PodCliqueSet) (string, error) {
	topologyNames := sets.New[string]()
	for _, tc := range getAllTopologyConstraintsInPodCliqueSet(pcs) {
		if tc.TopologyName == "" || tc.PackDomain == "" {
			return "", ErrTopologyNameMissing
		}
		topologyNames.Insert(tc.TopologyName)
	}
	switch topologyNames.Len() {
	case 0:
		return "", ErrTopologyNameMissing
	case 1:
		return sets.List(topologyNames)[0], nil
	default:
		return "", ErrMultipleTopologyNamesUnsupported
	}
}

// GetUniqueTopologyDomainsInPodCliqueSet returns all unique, non-empty pack domains referenced by the PCS.
func GetUniqueTopologyDomainsInPodCliqueSet(pcs *grovecorev1alpha1.PodCliqueSet) []grovecorev1alpha1.TopologyDomain {
	topologyDomains := sets.New[grovecorev1alpha1.TopologyDomain]()
	for _, tc := range getAllTopologyConstraintsInPodCliqueSet(pcs) {
		if tc.PackDomain != "" {
			topologyDomains.Insert(tc.PackDomain)
		}
	}
	return topologyDomains.UnsortedList()
}

func getAllTopologyConstraintsInPodCliqueSet(pcs *grovecorev1alpha1.PodCliqueSet) []*grovecorev1alpha1.TopologyConstraint {
	constraints := make([]*grovecorev1alpha1.TopologyConstraint, 0, 1+len(pcs.Spec.Template.Cliques)+len(pcs.Spec.Template.PodCliqueScalingGroupConfigs))
	if pcs.Spec.Template.TopologyConstraint != nil {
		constraints = append(constraints, pcs.Spec.Template.TopologyConstraint)
	}
	for _, pclqTemplateSpec := range pcs.Spec.Template.Cliques {
		if pclqTemplateSpec.TopologyConstraint != nil {
			constraints = append(constraints, pclqTemplateSpec.TopologyConstraint)
		}
	}
	for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if pcsgConfig.TopologyConstraint != nil {
			constraints = append(constraints, pcsgConfig.TopologyConstraint)
		}
	}
	return constraints
}

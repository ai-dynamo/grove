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
	// ErrTopologyNameMissing indicates that topology constraints are present but the PCS-level topologyName is absent.
	ErrTopologyNameMissing = errors.New("topology constraints require pcs-level topologyName")
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

// ResolveTopologyNameForPodCliqueSet resolves the PCS-level topology name for topology-aware scheduling.
func ResolveTopologyNameForPodCliqueSet(pcs *grovecorev1alpha1.PodCliqueSet) (string, error) {
	if !HasAnyTopologyConstraint(pcs) {
		return "", nil
	}
	if pcs.Spec.Template.TopologyConstraint == nil || pcs.Spec.Template.TopologyConstraint.TopologyName == "" {
		return "", ErrTopologyNameMissing
	}
	return pcs.Spec.Template.TopologyConstraint.TopologyName, nil
}

// GetUniqueTopologyDomainsInPodCliqueSet returns all unique, non-empty pack domains referenced by the PCS.
func GetUniqueTopologyDomainsInPodCliqueSet(pcs *grovecorev1alpha1.PodCliqueSet) []grovecorev1alpha1.TopologyDomain {
	topologyDomains := sets.New[grovecorev1alpha1.TopologyDomain]()
	if pcs.Spec.Template.TopologyConstraint != nil && pcs.Spec.Template.TopologyConstraint.PackDomain != "" {
		topologyDomains.Insert(pcs.Spec.Template.TopologyConstraint.PackDomain)
	}
	for _, pclqTemplateSpec := range pcs.Spec.Template.Cliques {
		if pclqTemplateSpec.TopologyConstraint != nil && pclqTemplateSpec.TopologyConstraint.PackDomain != "" {
			topologyDomains.Insert(pclqTemplateSpec.TopologyConstraint.PackDomain)
		}
	}
	for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if pcsgConfig.TopologyConstraint != nil && pcsgConfig.TopologyConstraint.PackDomain != "" {
			topologyDomains.Insert(pcsgConfig.TopologyConstraint.PackDomain)
		}
	}
	return topologyDomains.UnsortedList()
}

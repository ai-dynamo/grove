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
	// ErrTopologyNameMissing indicates that a constrained level has no effective topologyName after inheritance is applied.
	ErrTopologyNameMissing = errors.New("topology constraints require an explicit or inherited topologyName")
	// ErrPackDomainMissing indicates that a topologyConstraint exists but does not specify packDomain.
	ErrPackDomainMissing = errors.New("topology constraints require packDomain")
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

// ResolveEffectiveTopologyNameForConstraint resolves the topologyName for a single constrained level.
func ResolveEffectiveTopologyNameForConstraint(explicitTopologyName, inheritedTopologyName string) (string, error) {
	if explicitTopologyName != "" {
		if inheritedTopologyName != "" && explicitTopologyName != inheritedTopologyName {
			return "", ErrMultipleTopologyNamesUnsupported
		}
		return explicitTopologyName, nil
	}
	if inheritedTopologyName != "" {
		return inheritedTopologyName, nil
	}
	return "", ErrTopologyNameMissing
}

// ResolveEffectiveTopologyNameForPodCliqueSet resolves the single effective topologyName for the PCS after inheritance is applied.
// Callers that need to distinguish "no topology constraints at all" from invalid topology constraints
// must first call HasAnyTopologyConstraint.
func ResolveEffectiveTopologyNameForPodCliqueSet(pcs *grovecorev1alpha1.PodCliqueSet) (string, error) {
	resolvedTopologyName := ""
	recordTopologyName := func(topologyName string) error {
		if resolvedTopologyName == "" {
			resolvedTopologyName = topologyName
			return nil
		}
		if resolvedTopologyName != topologyName {
			return ErrMultipleTopologyNamesUnsupported
		}
		return nil
	}

	pcsEffectiveTopologyName := ""
	if tc := pcs.Spec.Template.TopologyConstraint; tc != nil {
		if tc.PackDomain == "" {
			return "", ErrPackDomainMissing
		}
		effectiveTopologyName, err := ResolveEffectiveTopologyNameForConstraint(tc.TopologyName, "")
		if err != nil {
			return "", err
		}
		pcsEffectiveTopologyName = effectiveTopologyName
		if err := recordTopologyName(effectiveTopologyName); err != nil {
			return "", err
		}
	}

	pcsgTopologyNameByCliqueName := make(map[string]string)
	for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if pcsgConfig.TopologyConstraint == nil {
			continue
		}
		if pcsgConfig.TopologyConstraint.PackDomain == "" {
			return "", ErrPackDomainMissing
		}
		effectiveTopologyName, err := ResolveEffectiveTopologyNameForConstraint(pcsgConfig.TopologyConstraint.TopologyName, pcsEffectiveTopologyName)
		if err != nil {
			return "", err
		}
		if err := recordTopologyName(effectiveTopologyName); err != nil {
			return "", err
		}
		for _, cliqueName := range pcsgConfig.CliqueNames {
			if _, exists := pcsgTopologyNameByCliqueName[cliqueName]; !exists {
				pcsgTopologyNameByCliqueName[cliqueName] = effectiveTopologyName
			}
		}
	}

	for _, pclqTemplateSpec := range pcs.Spec.Template.Cliques {
		if pclqTemplateSpec.TopologyConstraint == nil {
			continue
		}
		if pclqTemplateSpec.TopologyConstraint.PackDomain == "" {
			return "", ErrPackDomainMissing
		}

		inheritedTopologyName := pcsEffectiveTopologyName
		if pcsgTopologyName, exists := pcsgTopologyNameByCliqueName[pclqTemplateSpec.Name]; exists {
			inheritedTopologyName = pcsgTopologyName
		}

		effectiveTopologyName, err := ResolveEffectiveTopologyNameForConstraint(pclqTemplateSpec.TopologyConstraint.TopologyName, inheritedTopologyName)
		if err != nil {
			return "", err
		}
		if err := recordTopologyName(effectiveTopologyName); err != nil {
			return "", err
		}
	}

	if resolvedTopologyName == "" {
		return "", ErrTopologyNameMissing
	}
	return resolvedTopologyName, nil
}

// FindExplicitTopologyNameForPodCliqueSet returns one explicit topologyName from the PCS.
// It is intended for callers operating on already-validated PCS objects where all explicit topologyName
// values are expected to match. Callers that need to distinguish "no topology constraints at all" from
// missing explicit topologyName values must first call HasAnyTopologyConstraint.
func FindExplicitTopologyNameForPodCliqueSet(pcs *grovecorev1alpha1.PodCliqueSet) (string, error) {
	if tc := pcs.Spec.Template.TopologyConstraint; tc != nil && tc.TopologyName != "" {
		return tc.TopologyName, nil
	}
	for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if tc := pcsgConfig.TopologyConstraint; tc != nil && tc.TopologyName != "" {
			return tc.TopologyName, nil
		}
	}
	for _, pclqTemplateSpec := range pcs.Spec.Template.Cliques {
		if tc := pclqTemplateSpec.TopologyConstraint; tc != nil && tc.TopologyName != "" {
			return tc.TopologyName, nil
		}
	}
	return "", ErrTopologyNameMissing
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

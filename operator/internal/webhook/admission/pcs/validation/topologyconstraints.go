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

package validation

import (
	"fmt"
	"slices"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

// topologyConstraintsValidator is a simplified validator for topology constraints specified for a PodCliqueSet.
// This validator validates directly against the PCS structure without intermediate data extraction.
type topologyConstraintsValidator struct {
	pcs *grovecorev1alpha1.PodCliqueSet
	// tasEnabled indicates whether Topology Aware Scheduling is enabled for the cluster.
	tasEnabled bool
	// clusterTopologyDomains is a list of topology domains configured for the cluster.
	// This is taken from the ClusterTopology resource which is assumed to have a sorted list of levels.
	clusterTopologyDomains []string
}

// newTopologyConstraintsValidatorV2 creates a new topologyConstraintsValidator for the given PodCliqueSet.
func newTopologyConstraintsValidator(pcs *grovecorev1alpha1.PodCliqueSet, tasEnabled bool, clusterTopologyDomains []string) *topologyConstraintsValidator {
	return &topologyConstraintsValidator{
		pcs:                    pcs,
		tasEnabled:             tasEnabled,
		clusterTopologyDomains: clusterTopologyDomains,
	}
}

// validate performs validation specific to PodCliqueSet creates.
func (v *topologyConstraintsValidator) validate() field.ErrorList {
	pcsTemplateFLDPath := field.NewPath("spec").Child("template")

	// If TAS is disabled, disallow any topology constraints, other validations are not applicable and are skipped.
	if !v.tasEnabled {
		return v.disallowConstraintsForCreateWhenTASIsDisabled(pcsTemplateFLDPath)
	}

	var allErrs field.ErrorList
	allErrs = append(allErrs, v.validateTopologyDomainsExistInClusterTopology(pcsTemplateFLDPath)...)
	allErrs = append(allErrs, v.validateHierarchicalTopologyConstraints(pcsTemplateFLDPath)...)
	return allErrs
}

// validateUpdate performs validation specific to PodCliqueSet updates.
// If TAS has been enabled then ensure that topology constraints are not being modified for this PodCliqueSet.
// NOTE:
// This is a short-term limitation and is brought in due to KAI scheduler not supporting dynamic changes to PodGangs.
// Once we have scheduler backends, we can then implement handling of dynamic topology constraints properly and remove this validation.
// Why not use CEL in CRD?
// We could have done the immutability check via CEL expressions baked into the CRD. However, since this is a temporary
// limitation and will be removed in the future, we chose to implement it in validating webhook to prevent updating
// the CRD once this validation is no longer needed.
func (v *topologyConstraintsValidator) validateUpdate(oldPCS *grovecorev1alpha1.PodCliqueSet) field.ErrorList {
	fldPath := field.NewPath("spec").Child("template")
	return v.disallowChangesToTopologyConstraintsWhenPCSIsUpdated(oldPCS, fldPath)
}

// disallowConstraintsForCreateWhenTASIsDisabled ensures that no topology constraints are specified for new PodCliqueSet
// creations when Topology Aware Scheduling is disabled for the cluster.
func (v *topologyConstraintsValidator) disallowConstraintsForCreateWhenTASIsDisabled(fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	// Validate PCS level
	if v.pcs.Spec.Template.TopologyConstraint != nil {
		allErrs = append(allErrs, field.Invalid(
			fldPath.Child("topologyConstraint"),
			v.pcs.Spec.Template.TopologyConstraint.PackDomain,
			"topology constraints are not allowed when Topology Aware Scheduling is disabled"))
	}

	// Validate PodClique level
	for i, clique := range v.pcs.Spec.Template.Cliques {
		if clique.TopologyConstraint != nil {
			allErrs = append(allErrs, field.Invalid(
				fldPath.Child("cliques").Index(i).Child("topologyConstraint"),
				clique.TopologyConstraint.PackDomain,
				"topology constraints are not allowed when Topology Aware Scheduling is disabled"))
		}
	}

	// Validate PCSG level
	for i, pcsg := range v.pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if pcsg.TopologyConstraint != nil {
			allErrs = append(allErrs, field.Invalid(
				fldPath.Child("podCliqueScalingGroupConfigs").Index(i).Child("topologyConstraint"),
				pcsg.TopologyConstraint.PackDomain,
				"topology constraints are not allowed when Topology Aware Scheduling is disabled"))
		}
	}

	return allErrs
}

// validateTopologyDomainsExistInClusterTopology ensures that all specified topology constraint domains exist in the cluster topology.
func (v *topologyConstraintsValidator) validateTopologyDomainsExistInClusterTopology(fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	// Helper to validate a single domain
	validateDomain := func(domain grovecorev1alpha1.TopologyDomain, fieldPath *field.Path) {
		if !slices.Contains(v.clusterTopologyDomains, string(domain)) {
			allErrs = append(allErrs, field.Invalid(fieldPath, domain,
				fmt.Sprintf("topology domain '%s' does not exist in cluster topology %v",
					domain, v.clusterTopologyDomains)))
		}
	}

	// Validate PCS level
	if tc := v.pcs.Spec.Template.TopologyConstraint; tc != nil {
		validateDomain(tc.PackDomain, fldPath.Child("topologyConstraint"))
	}

	// Validate PodClique level
	for i, clique := range v.pcs.Spec.Template.Cliques {
		if tc := clique.TopologyConstraint; tc != nil {
			validateDomain(tc.PackDomain, fldPath.Child("cliques").Index(i).Child("topologyConstraint"))
		}
	}

	// Validate PCSG level
	for i, pcsg := range v.pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if tc := pcsg.TopologyConstraint; tc != nil {
			validateDomain(tc.PackDomain, fldPath.Child("podCliqueScalingGroupConfigs").Index(i).Child("topologyConstraint"))
		}
	}

	return allErrs
}

// isParentBroaderThanChild checks if a parent topology constraint domain is broader than (or equal to)
// a child topology constraint domain. Returns true if valid (parent is broader or equal), false if invalid (parent is narrower).
func isParentBroaderThanChild(parentDomain, childDomain grovecorev1alpha1.TopologyDomain) bool {
	return !parentDomain.IsTopologyDomainNarrower(childDomain)
}

// buildHierarchyViolationMsg generates an error message when a parent constraint is narrower than a child constraint.
func buildHierarchyViolationMsg(parentDomain grovecorev1alpha1.TopologyDomain, parentKind, parentName string,
	childDomain grovecorev1alpha1.TopologyDomain, childKind, childName string) string {
	parentIdentifier := parentKind
	if parentName != "" {
		parentIdentifier = fmt.Sprintf("%s '%s'", parentKind, parentName)
	}
	return fmt.Sprintf("%s topology constraint domain '%s' is narrower than %s '%s' topology constraint domain '%s'",
		parentIdentifier, parentDomain, childKind, childName, childDomain)
}

// validateHierarchicalTopologyConstraints ensures that topology constraint domains specified at different levels
// (PodCliqueSet, PodCliqueScalingGroup, PodClique) adhere to the hierarchical rules i.e., a parent level topology domain
// must not be narrower than any of its child level topology domains.
func (v *topologyConstraintsValidator) validateHierarchicalTopologyConstraints(fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	pcsConstraint := v.pcs.Spec.Template.TopologyConstraint

	// Validate PodCliqueSet level constraints against children (if PodCliqueSet constraint exists)
	if pcsConstraint != nil {
		pcsDomain := pcsConstraint.PackDomain

		// Validate: PodCliqueSet must be broader than all PodCliques
		for _, clique := range v.pcs.Spec.Template.Cliques {
			if clique.TopologyConstraint != nil {
				if !isParentBroaderThanChild(pcsDomain, clique.TopologyConstraint.PackDomain) {
					allErrs = append(allErrs, field.Invalid(
						fldPath.Child("topologyConstraint"),
						pcsDomain,
						buildHierarchyViolationMsg(pcsDomain, apicommonconstants.KindPodCliqueSet, "",
							clique.TopologyConstraint.PackDomain, apicommonconstants.KindPodClique, clique.Name)))
				}
			}
		}

		// Validate: PodCliqueSet must be broader than all PodCliqueScalingGroups
		for _, pcsg := range v.pcs.Spec.Template.PodCliqueScalingGroupConfigs {
			if pcsg.TopologyConstraint != nil {
				if !isParentBroaderThanChild(pcsDomain, pcsg.TopologyConstraint.PackDomain) {
					allErrs = append(allErrs, field.Invalid(
						fldPath.Child("topologyConstraint"),
						pcsDomain,
						buildHierarchyViolationMsg(pcsDomain, apicommonconstants.KindPodCliqueSet, "",
							pcsg.TopologyConstraint.PackDomain, apicommonconstants.KindPodCliqueScalingGroup, pcsg.Name)))
				}
			}
		}
	}

	// Validate: For Each PodCliqueScalingGroup, its topology constraint must be broader than its constituent PodCliques
	for i, pcsg := range v.pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if pcsg.TopologyConstraint == nil {
			continue
		}

		pcsgDomain := pcsg.TopologyConstraint.PackDomain
		for _, cliqueName := range pcsg.CliqueNames {
			// Find the clique by name
			matchingPCLQTemplate := utils.FindPodCliqueTemplateSpecByName(v.pcs, cliqueName)
			if matchingPCLQTemplate != nil && matchingPCLQTemplate.TopologyConstraint != nil {
				if !isParentBroaderThanChild(pcsgDomain, matchingPCLQTemplate.TopologyConstraint.PackDomain) {
					allErrs = append(allErrs, field.Invalid(
						fldPath.Child("podCliqueScalingGroupConfigs").Index(i).Child("topologyConstraint"),
						pcsgDomain,
						buildHierarchyViolationMsg(pcsgDomain, apicommonconstants.KindPodCliqueScalingGroup, pcsg.Name,
							matchingPCLQTemplate.TopologyConstraint.PackDomain, apicommonconstants.KindPodClique, matchingPCLQTemplate.Name)))
				}
			}
		}
	}

	return allErrs
}

// disallowChangesToTopologyConstraintsWhenPCSIsUpdated ensures that topology constraints are not modified during updates.
func (v *topologyConstraintsValidator) disallowChangesToTopologyConstraintsWhenPCSIsUpdated(oldPCS *grovecorev1alpha1.PodCliqueSet, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	// Validate PCS level
	oldPCSConstraint := oldPCS.Spec.Template.TopologyConstraint
	newPCSConstraint := v.pcs.Spec.Template.TopologyConstraint
	if constraintChanged(oldPCSConstraint, newPCSConstraint) {
		allErrs = append(allErrs, field.Forbidden(
			fldPath.Child("topologyConstraint"),
			constraintChangeMsg(apicommonconstants.KindPodCliqueSet, "", oldPCSConstraint, newPCSConstraint)))
	}

	// Validate PodClique level
	// Build map for quick lookup
	oldCliqueConstraints := make(map[string]*grovecorev1alpha1.TopologyConstraint)
	for _, clique := range oldPCS.Spec.Template.Cliques {
		oldCliqueConstraints[clique.Name] = clique.TopologyConstraint
	}

	for i, clique := range v.pcs.Spec.Template.Cliques {
		oldConstraint := oldCliqueConstraints[clique.Name]
		if constraintChanged(oldConstraint, clique.TopologyConstraint) {
			allErrs = append(allErrs, field.Forbidden(
				fldPath.Child("cliques").Index(i).Child("topologyConstraint"),
				constraintChangeMsg(apicommonconstants.KindPodClique, clique.Name, oldConstraint, clique.TopologyConstraint)))
		}
	}

	// Validate PCSG level
	// Build map for quick lookup
	oldPCSGConstraints := make(map[string]*grovecorev1alpha1.TopologyConstraint)
	for _, pcsg := range oldPCS.Spec.Template.PodCliqueScalingGroupConfigs {
		oldPCSGConstraints[pcsg.Name] = pcsg.TopologyConstraint
	}

	for i, pcsg := range v.pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		oldConstraint := oldPCSGConstraints[pcsg.Name]
		if constraintChanged(oldConstraint, pcsg.TopologyConstraint) {
			allErrs = append(allErrs, field.Forbidden(
				fldPath.Child("podCliqueScalingGroupConfigs").Index(i).Child("topologyConstraint"),
				constraintChangeMsg(apicommonconstants.KindPodCliqueScalingGroup, pcsg.Name, oldConstraint, pcsg.TopologyConstraint)))
		}
	}

	return allErrs
}

// constraintChanged checks if two topology constraints are different, considering nil values.
func constraintChanged(old, new *grovecorev1alpha1.TopologyConstraint) bool {
	if old == nil && new == nil {
		return false
	}
	if old == nil || new == nil {
		return true
	}
	return old.PackDomain != new.PackDomain
}

// constraintChangeMsg generates a specific error message based on the type of change.
func constraintChangeMsg(kind, name string, old, new *grovecorev1alpha1.TopologyConstraint) string {
	identifier := ""
	if name != "" {
		identifier = fmt.Sprintf(" '%s'", name)
	}

	if old == nil {
		return fmt.Sprintf("%s%s topology constraint cannot be added after creation", kind, identifier)
	}
	if new == nil {
		return fmt.Sprintf("%s%s topology constraint cannot be removed after creation", kind, identifier)
	}
	return fmt.Sprintf("%s%s topology constraint cannot be changed from '%s' to '%s'",
		kind, identifier, old.PackDomain, new.PackDomain)
}

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

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

// topologyConstraintsValidator validates topology constraints specified for a PodCliqueSet.
type topologyConstraintsValidator struct {
	pcs        *grovecorev1alpha1.PodCliqueSet
	tasEnabled bool
	// domainIndex maps topology domain names to their position in the ClusterTopology levels array.
	// Index 0 is the broadest domain. A lower index means a broader domain.
	domainIndex map[string]int
}

func newTopologyConstraintsValidator(pcs *grovecorev1alpha1.PodCliqueSet, tasEnabled bool, levels []grovecorev1alpha1.TopologyLevel) *topologyConstraintsValidator {
	domainIndex := make(map[string]int, len(levels))
	for i, level := range levels {
		domainIndex[string(level.Domain)] = i
	}
	return &topologyConstraintsValidator{
		pcs:         pcs,
		tasEnabled:  tasEnabled,
		domainIndex: domainIndex,
	}
}

// validate performs validation specific to PodCliqueSet creates.
func (v *topologyConstraintsValidator) validate() field.ErrorList {
	pcsTemplateFLDPath := field.NewPath("spec").Child("template")

	// If TAS is disabled, disallow any topology constraints, other validations are not applicable and are skipped.
	if !v.tasEnabled {
		return v.disallowConstraintsForCreateWhenTASIsDisabled(pcsTemplateFLDPath)
	}

	// Validate topologyName presence: required iff any packDomain is set at any level.
	if errs := v.validateTopologyNamePresence(pcsTemplateFLDPath); len(errs) > 0 {
		return errs
	}

	errs := v.validateTopologyDomainsExistInClusterTopology(pcsTemplateFLDPath)
	if len(errs) > 0 {
		return errs
	}
	return v.validateHierarchicalTopologyConstraints(pcsTemplateFLDPath)
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

// validateTopologyNamePresence validates that topologyName is set iff any packDomain is set at any level.
func (v *topologyConstraintsValidator) validateTopologyNamePresence(fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	hasAnyPackDomain := false
	if tc := v.pcs.Spec.Template.TopologyConstraint; tc != nil && tc.PackDomain != "" {
		hasAnyPackDomain = true
	}
	if !hasAnyPackDomain {
		for _, clique := range v.pcs.Spec.Template.Cliques {
			if clique.TopologyConstraint != nil && clique.TopologyConstraint.PackDomain != "" {
				hasAnyPackDomain = true
				break
			}
		}
	}
	if !hasAnyPackDomain {
		for _, pcsg := range v.pcs.Spec.Template.PodCliqueScalingGroupConfigs {
			if pcsg.TopologyConstraint != nil && pcsg.TopologyConstraint.PackDomain != "" {
				hasAnyPackDomain = true
				break
			}
		}
	}

	topologyName := ""
	if tc := v.pcs.Spec.Template.TopologyConstraint; tc != nil {
		topologyName = tc.TopologyName
	}

	if hasAnyPackDomain && topologyName == "" {
		allErrs = append(allErrs, field.Required(
			fldPath.Child("topologyConstraint", "topologyName"),
			"topologyName is required when any packDomain is specified"))
	}

	if !hasAnyPackDomain && topologyName != "" {
		allErrs = append(allErrs, field.Invalid(
			fldPath.Child("topologyConstraint", "topologyName"),
			topologyName,
			"topologyName must not be set when no packDomain is specified"))
	}

	return allErrs
}

// disallowConstraintsForCreateWhenTASIsDisabled ensures that no topology constraints are specified for new PodCliqueSet
// creations when Topology Aware Scheduling is disabled for the cluster.
func (v *topologyConstraintsValidator) disallowConstraintsForCreateWhenTASIsDisabled(fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	// Validate PCS level
	if tc := v.pcs.Spec.Template.TopologyConstraint; tc != nil && (tc.TopologyName != "" || tc.PackDomain != "") {
		allErrs = append(allErrs, field.Invalid(
			fldPath.Child("topologyConstraint"),
			tc.PackDomain,
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

	validateDomain := func(domain grovecorev1alpha1.TopologyDomain, fieldPath *field.Path) {
		if _, ok := v.domainIndex[string(domain)]; !ok {
			allErrs = append(allErrs, field.Invalid(fieldPath, domain,
				fmt.Sprintf("topology domain '%s' does not exist in cluster topology", domain)))
		}
	}

	// Validate PCS level
	if tc := v.pcs.Spec.Template.TopologyConstraint; tc != nil && tc.PackDomain != "" {
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

// isDomainBroaderOrEqual checks if parentDomain is broader than or equal to childDomain
// based on their position in the ClusterTopology levels array (index 0 = broadest).
func (v *topologyConstraintsValidator) isDomainBroaderOrEqual(parentDomain, childDomain grovecorev1alpha1.TopologyDomain) bool {
	parentIdx, parentOK := v.domainIndex[string(parentDomain)]
	childIdx, childOK := v.domainIndex[string(childDomain)]
	if !parentOK || !childOK {
		return false
	}
	return parentIdx <= childIdx
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
	if pcsConstraint != nil && pcsConstraint.PackDomain != "" {
		pcsDomain := pcsConstraint.PackDomain

		// Validate: PodCliqueSet must be broader than all PodCliques
		for _, clique := range v.pcs.Spec.Template.Cliques {
			if clique.TopologyConstraint != nil {
				if !v.isDomainBroaderOrEqual(pcsDomain, clique.TopologyConstraint.PackDomain) {
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
				if !v.isDomainBroaderOrEqual(pcsDomain, pcsg.TopologyConstraint.PackDomain) {
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
				if !v.isDomainBroaderOrEqual(pcsgDomain, matchingPCLQTemplate.TopologyConstraint.PackDomain) {
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

	// Validate PCS level (PodCliqueSetTopologyConstraint — includes both topologyName and packDomain)
	oldPCSConstraint := oldPCS.Spec.Template.TopologyConstraint
	newPCSConstraint := v.pcs.Spec.Template.TopologyConstraint
	if pcsConstraintChanged(oldPCSConstraint, newPCSConstraint) {
		allErrs = append(allErrs, field.Forbidden(
			fldPath.Child("topologyConstraint"),
			pcsConstraintChangeMsg(apicommonconstants.KindPodCliqueSet, "", oldPCSConstraint, newPCSConstraint)))
	}

	// Validate PodClique level
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

// pcsConstraintChanged checks if two PodCliqueSetTopologyConstraints are different, considering nil values.
func pcsConstraintChanged(old, new *grovecorev1alpha1.PodCliqueSetTopologyConstraint) bool {
	if old == nil && new == nil {
		return false
	}
	if old == nil || new == nil {
		return true
	}
	return old.TopologyName != new.TopologyName || old.PackDomain != new.PackDomain
}

func pcsConstraintChangeMsg(kind, name string, old, new *grovecorev1alpha1.PodCliqueSetTopologyConstraint) string {
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
	return fmt.Sprintf("%s%s topology constraint cannot be changed after creation", kind, identifier)
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

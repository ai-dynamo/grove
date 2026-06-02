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
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

// topologyConstraintsValidator is a simplified validator for topology constraints specified for a PodCliqueSet.
// This validator validates directly against the PCS structure without intermediate data extraction.
type topologyConstraintsValidator struct {
	pcs *grovecorev1alpha1.PodCliqueSet
	// tasEnabled indicates whether Topology Aware Scheduling is enabled for the cluster.
	tasEnabled bool
	// clusterTopologyDomains is a list of topology domains configured for the cluster.
	// This is taken from the ClusterTopologyBinding resource which is assumed to have a sorted list of levels.
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

	errs := v.validateTopologyDomainsExistInClusterTopology()
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
	return v.validateTopologyConstraintImmutability(oldPCS, fldPath, false)
}

func (v *topologyConstraintsValidator) warnings() []string {
	return v.validateSameResourcePreferredWarnings()
}

func (v *topologyConstraintsValidator) updateWarnings() []string {
	warnings := v.validateSameResourcePreferredWarnings()
	forEachTopologyConstraint(v.pcs, func(tc *grovecorev1alpha1.TopologyConstraint, tcPath *field.Path) {
		if tc.PackDomain != "" {
			warnings = append(warnings, fmt.Sprintf("%s is deprecated; use %s instead", tcPath.Child("packDomain"), tcPath.Child("pack", "required")))
		}
	})
	return warnings
}

// disallowConstraintsForCreateWhenTASIsDisabled ensures that no topology constraints are specified for new PodCliqueSet
// creations when Topology Aware Scheduling is disabled for the cluster.
func (v *topologyConstraintsValidator) disallowConstraintsForCreateWhenTASIsDisabled(fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	// Validate PCS level
	if v.pcs.Spec.Template.TopologyConstraint != nil {
		allErrs = append(allErrs, field.Invalid(
			fldPath.Child("topologyConstraint"),
			v.pcs.Spec.Template.TopologyConstraint.ReferencedDomains(),
			"topology constraints are not allowed when Topology Aware Scheduling is disabled"))
	}

	// Validate PodClique level
	for i, clique := range v.pcs.Spec.Template.Cliques {
		if clique.TopologyConstraint != nil {
			allErrs = append(allErrs, field.Invalid(
				fldPath.Child("cliques").Index(i).Child("topologyConstraint"),
				clique.TopologyConstraint.ReferencedDomains(),
				"topology constraints are not allowed when Topology Aware Scheduling is disabled"))
		}
	}

	// Validate PCSG level
	for i, pcsg := range v.pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if pcsg.TopologyConstraint != nil {
			allErrs = append(allErrs, field.Invalid(
				fldPath.Child("podCliqueScalingGroups").Index(i).Child("topologyConstraint"),
				pcsg.TopologyConstraint.ReferencedDomains(),
				"topology constraints are not allowed when Topology Aware Scheduling is disabled"))
		}
	}

	return allErrs
}

func (v *topologyConstraintsValidator) validateForLegacyRepair() field.ErrorList {
	fldPath := field.NewPath("spec").Child("template")
	if !v.tasEnabled {
		return v.disallowConstraintsForCreateWhenTASIsDisabled(fldPath)
	}
	errs := v.validateTopologyDomainsExistInClusterTopology()
	if len(errs) > 0 {
		return errs
	}
	return v.validateHierarchicalTopologyConstraints(fldPath)
}

// validateTopologyDomainsExistInClusterTopology ensures that all specified topology constraint domains exist in the cluster topology.
// When clusterTopologyDomains is nil, validation is skipped (no topology constraints or topologyName not set).
func (v *topologyConstraintsValidator) validateTopologyDomainsExistInClusterTopology() field.ErrorList {
	if v.clusterTopologyDomains == nil {
		return nil
	}

	var allErrs field.ErrorList

	validateDomain := func(domain grovecorev1alpha1.TopologyDomain, fieldPath *field.Path) {
		if domain == "" {
			return
		}
		if !slices.Contains(v.clusterTopologyDomains, string(domain)) {
			allErrs = append(allErrs, field.Invalid(fieldPath, domain,
				fmt.Sprintf("topology domain '%s' does not exist in cluster topology %v",
					domain, v.clusterTopologyDomains)))
		}
	}

	forEachTopologyConstraint(v.pcs, func(tc *grovecorev1alpha1.TopologyConstraint, tcPath *field.Path) {
		if tc.PackDomain != "" {
			validateDomain(tc.PackDomain, tcPath.Child("packDomain"))
		}
		if tc.Pack != nil {
			validateDomain(tc.Pack.RequiredDomain, tcPath.Child("pack", "required"))
			validateDomain(tc.Pack.PreferredDomain, tcPath.Child("pack", "preferred"))
		}
	})

	return allErrs
}

// hasHierarchyViolation reports whether the parent domain is narrower than the child domain,
// which violates the constraint hierarchy. Returns false when either domain is unknown (check skipped).
func hasHierarchyViolation(parentDomain, childDomain grovecorev1alpha1.TopologyDomain, clusterTopologyDomains []string) bool {
	parentIdx := slices.Index(clusterTopologyDomains, string(parentDomain))
	childIdx := slices.Index(clusterTopologyDomains, string(childDomain))
	if parentIdx == -1 || childIdx == -1 {
		return false
	}
	return parentIdx > childIdx
}

func isCoarserThan(domain, other grovecorev1alpha1.TopologyDomain, clusterTopologyDomains []string) bool {
	domainIdx := slices.Index(clusterTopologyDomains, string(domain))
	otherIdx := slices.Index(clusterTopologyDomains, string(other))
	if domainIdx == -1 || otherIdx == -1 {
		return false
	}
	return domainIdx < otherIdx
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
		pcsRequiredDomain := pcsConstraint.RequiredDomain()
		pcsPreferredDomain := pcsConstraint.PreferredDomain()

		// Validate: PodCliqueSet must be broader than all PodCliques
		for _, clique := range v.pcs.Spec.Template.Cliques {
			if clique.TopologyConstraint != nil {
				if hasHierarchyViolation(pcsRequiredDomain, clique.TopologyConstraint.RequiredDomain(), v.clusterTopologyDomains) {
					allErrs = append(allErrs, field.Invalid(
						fldPath.Child("topologyConstraint"),
						pcsRequiredDomain,
						buildHierarchyViolationMsg(pcsRequiredDomain, apicommonconstants.KindPodCliqueSet, "",
							clique.TopologyConstraint.RequiredDomain(), apicommonconstants.KindPodClique, clique.Name)))
				}
				if hasHierarchyViolation(pcsPreferredDomain, clique.TopologyConstraint.PreferredDomain(), v.clusterTopologyDomains) {
					allErrs = append(allErrs, field.Invalid(
						fldPath.Child("topologyConstraint").Child("pack", "preferred"),
						pcsPreferredDomain,
						buildHierarchyViolationMsg(pcsPreferredDomain, apicommonconstants.KindPodCliqueSet, "",
							clique.TopologyConstraint.PreferredDomain(), apicommonconstants.KindPodClique, clique.Name)))
				}
			}
		}

		// Validate: PodCliqueSet must be broader than all PodCliqueScalingGroups
		for _, pcsg := range v.pcs.Spec.Template.PodCliqueScalingGroupConfigs {
			if pcsg.TopologyConstraint != nil {
				if hasHierarchyViolation(pcsRequiredDomain, pcsg.TopologyConstraint.RequiredDomain(), v.clusterTopologyDomains) {
					allErrs = append(allErrs, field.Invalid(
						fldPath.Child("topologyConstraint"),
						pcsRequiredDomain,
						buildHierarchyViolationMsg(pcsRequiredDomain, apicommonconstants.KindPodCliqueSet, "",
							pcsg.TopologyConstraint.RequiredDomain(), apicommonconstants.KindPodCliqueScalingGroup, pcsg.Name)))
				}
				if hasHierarchyViolation(pcsPreferredDomain, pcsg.TopologyConstraint.PreferredDomain(), v.clusterTopologyDomains) {
					allErrs = append(allErrs, field.Invalid(
						fldPath.Child("topologyConstraint").Child("pack", "preferred"),
						pcsPreferredDomain,
						buildHierarchyViolationMsg(pcsPreferredDomain, apicommonconstants.KindPodCliqueSet, "",
							pcsg.TopologyConstraint.PreferredDomain(), apicommonconstants.KindPodCliqueScalingGroup, pcsg.Name)))
				}
			}
		}
	}

	// Validate: For Each PodCliqueScalingGroup, its topology constraint must be broader than its constituent PodCliques
	for i, pcsg := range v.pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if pcsg.TopologyConstraint == nil {
			continue
		}

		pcsgRequiredDomain := pcsg.TopologyConstraint.RequiredDomain()
		pcsgPreferredDomain := pcsg.TopologyConstraint.PreferredDomain()
		for _, cliqueName := range pcsg.CliqueNames {
			// Find the clique by name
			matchingPCLQTemplate := componentutils.FindPodCliqueTemplateSpecByName(v.pcs, cliqueName)
			if matchingPCLQTemplate != nil && matchingPCLQTemplate.TopologyConstraint != nil {
				if hasHierarchyViolation(pcsgRequiredDomain, matchingPCLQTemplate.TopologyConstraint.RequiredDomain(), v.clusterTopologyDomains) {
					allErrs = append(allErrs, field.Invalid(
						fldPath.Child("podCliqueScalingGroups").Index(i).Child("topologyConstraint"),
						pcsgRequiredDomain,
						buildHierarchyViolationMsg(pcsgRequiredDomain, apicommonconstants.KindPodCliqueScalingGroup, pcsg.Name,
							matchingPCLQTemplate.TopologyConstraint.RequiredDomain(), apicommonconstants.KindPodClique, matchingPCLQTemplate.Name)))
				}
				if hasHierarchyViolation(pcsgPreferredDomain, matchingPCLQTemplate.TopologyConstraint.PreferredDomain(), v.clusterTopologyDomains) {
					allErrs = append(allErrs, field.Invalid(
						fldPath.Child("podCliqueScalingGroups").Index(i).Child("topologyConstraint").Child("pack", "preferred"),
						pcsgPreferredDomain,
						buildHierarchyViolationMsg(pcsgPreferredDomain, apicommonconstants.KindPodCliqueScalingGroup, pcsg.Name,
							matchingPCLQTemplate.TopologyConstraint.PreferredDomain(), apicommonconstants.KindPodClique, matchingPCLQTemplate.Name)))
				}
			}
		}
	}

	return allErrs
}

func (v *topologyConstraintsValidator) validateSameResourcePreferredWarnings() []string {
	var warnings []string
	if v.clusterTopologyDomains == nil {
		return warnings
	}
	forEachTopologyConstraint(v.pcs, func(tc *grovecorev1alpha1.TopologyConstraint, tcPath *field.Path) {
		requiredDomain := tc.RequiredDomain()
		preferredDomain := tc.PreferredDomain()
		if requiredDomain != "" && preferredDomain != "" && isCoarserThan(preferredDomain, requiredDomain, v.clusterTopologyDomains) {
			warnings = append(warnings, fmt.Sprintf("%s is coarser than %s and is redundant",
				tcPath.Child("pack", "preferred"), tcPath.Child("pack", "required")))
		}
	})
	return warnings
}

// validateTopologyConstraintImmutability ensures that topology constraints are not modified during updates.
// When allowInvalidConstraintRepair is true, the validator permits the legacy upgrade repair path:
// adding a missing topologyName to an existing constraint that already had a packDomain.
func (v *topologyConstraintsValidator) validateTopologyConstraintImmutability(oldPCS *grovecorev1alpha1.PodCliqueSet, fldPath *field.Path, allowInvalidConstraintRepair bool) field.ErrorList {
	var allErrs field.ErrorList

	oldPCSConstraint := oldPCS.Spec.Template.TopologyConstraint
	newPCSConstraint := v.pcs.Spec.Template.TopologyConstraint

	oldTopologyName := ""
	newTopologyName := ""
	if oldPCSConstraint != nil {
		oldTopologyName = oldPCSConstraint.TopologyName
	}
	if newPCSConstraint != nil {
		newTopologyName = newPCSConstraint.TopologyName
	}
	allowedPCSRepair := allowInvalidConstraintRepair && isAllowedInvalidTopologyConstraintRepair(oldPCSConstraint, newPCSConstraint)
	allowedPCSMigration := isAllowedDeprecatedPackDomainMigration(oldPCSConstraint, newPCSConstraint)

	if oldTopologyName != newTopologyName && !allowedPCSRepair {
		allErrs = append(allErrs, field.Forbidden(
			fldPath.Child("topologyConstraint").Child("topologyName"),
			fmt.Sprintf("topologyName cannot be changed from %q to %q", oldTopologyName, newTopologyName)))
	}

	// Validate PCS level pack changes only when topologyName is unchanged; adding or removing
	// the whole topologyConstraint or changing topologyName is already reported above.
	if oldTopologyName == newTopologyName && constraintChanged(oldPCSConstraint, newPCSConstraint) && !allowedPCSRepair && !allowedPCSMigration {
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
		if constraintChanged(oldConstraint, clique.TopologyConstraint) &&
			(!allowInvalidConstraintRepair || !isAllowedInvalidTopologyConstraintRepair(oldConstraint, clique.TopologyConstraint)) &&
			!isAllowedDeprecatedPackDomainMigration(oldConstraint, clique.TopologyConstraint) {
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
		if constraintChanged(oldConstraint, pcsg.TopologyConstraint) &&
			(!allowInvalidConstraintRepair || !isAllowedInvalidTopologyConstraintRepair(oldConstraint, pcsg.TopologyConstraint)) &&
			!isAllowedDeprecatedPackDomainMigration(oldConstraint, pcsg.TopologyConstraint) {
			allErrs = append(allErrs, field.Forbidden(
				fldPath.Child("podCliqueScalingGroups").Index(i).Child("topologyConstraint"),
				constraintChangeMsg(apicommonconstants.KindPodCliqueScalingGroup, pcsg.Name, oldConstraint, pcsg.TopologyConstraint)))
		}
	}

	return allErrs
}

func isAllowedInvalidTopologyConstraintRepair(old, new *grovecorev1alpha1.TopologyConstraint) bool {
	if old == nil || new == nil {
		return false
	}
	// Legacy objects can only be missing topologyName, because older API versions exposed packDomain
	// but not topologyName. topologyName-without-packDomain is not considered a repairable legacy shape.
	return old.TopologyName == "" &&
		old.PackDomain != "" &&
		new.TopologyName != "" &&
		new.PackDomain == old.PackDomain
}

func isAllowedDeprecatedPackDomainMigration(old, new *grovecorev1alpha1.TopologyConstraint) bool {
	if !isDeprecatedPackDomainMigrationAttempt(old, new) {
		return false
	}
	return old.TopologyName == new.TopologyName &&
		new.RequiredDomain() == old.PackDomain &&
		new.PreferredDomain() == old.PreferredDomain()
}

func isDeprecatedPackDomainMigrationAttempt(old, new *grovecorev1alpha1.TopologyConstraint) bool {
	return old != nil &&
		new != nil &&
		old.PackDomain != "" &&
		new.PackDomain == "" &&
		new.Pack != nil &&
		new.Pack.RequiredDomain != ""
}

func hasRepairableLegacyTopologyConstraint(pcs *grovecorev1alpha1.PodCliqueSet) bool {
	if pcs.Spec.Template.TopologyConstraint != nil && pcs.Spec.Template.TopologyConstraint.TopologyName == "" &&
		pcs.Spec.Template.TopologyConstraint.PackDomain != "" {
		return true
	}

	for _, clique := range pcs.Spec.Template.Cliques {
		if clique.TopologyConstraint != nil && clique.TopologyConstraint.TopologyName == "" &&
			clique.TopologyConstraint.PackDomain != "" {
			return true
		}
	}

	for _, pcsg := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if pcsg.TopologyConstraint != nil && pcsg.TopologyConstraint.TopologyName == "" &&
			pcsg.TopologyConstraint.PackDomain != "" {
			return true
		}
	}

	return false
}

// constraintChanged checks if two topology constraints are different, considering nil values.
func constraintChanged(old, new *grovecorev1alpha1.TopologyConstraint) bool {
	if old == nil && new == nil {
		return false
	}
	if old == nil || new == nil {
		return true
	}
	return old.TopologyName != new.TopologyName ||
		old.PackDomain != new.PackDomain ||
		old.RequiredDomain() != new.RequiredDomain() ||
		old.PreferredDomain() != new.PreferredDomain()
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
	if old.TopologyName != new.TopologyName {
		return fmt.Sprintf("%s%s topology constraint topologyName cannot be changed from '%s' to '%s'",
			kind, identifier, old.TopologyName, new.TopologyName)
	}
	if isDeprecatedPackDomainMigrationAttempt(old, new) {
		return fmt.Sprintf("%s%s deprecated packDomain migration can only move packDomain to pack.required without changing the required or preferred topology domain (old required=%q preferred=%q, new required=%q preferred=%q)",
			kind, identifier, old.PackDomain, old.PreferredDomain(), new.RequiredDomain(), new.PreferredDomain())
	}
	return fmt.Sprintf("%s%s topology constraint cannot be changed from required=%q preferred=%q to required=%q preferred=%q",
		kind, identifier, old.RequiredDomain(), old.PreferredDomain(), new.RequiredDomain(), new.PreferredDomain())
}

func forEachTopologyConstraint(pcs *grovecorev1alpha1.PodCliqueSet, fn func(*grovecorev1alpha1.TopologyConstraint, *field.Path)) {
	fldPath := field.NewPath("spec").Child("template")
	if tc := pcs.Spec.Template.TopologyConstraint; tc != nil {
		fn(tc, fldPath.Child("topologyConstraint"))
	}
	for i, clique := range pcs.Spec.Template.Cliques {
		if tc := clique.TopologyConstraint; tc != nil {
			fn(tc, fldPath.Child("cliques").Index(i).Child("topologyConstraint"))
		}
	}
	for i, pcsg := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if tc := pcsg.TopologyConstraint; tc != nil {
			fn(tc, fldPath.Child("podCliqueScalingGroups").Index(i).Child("topologyConstraint"))
		}
	}
}

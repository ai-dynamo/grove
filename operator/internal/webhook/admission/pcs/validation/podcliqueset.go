// /*
// Copyright 2024 The Grove Authors.
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
	"strings"

	groveconfigv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/schedulerbackend"
	"github.com/ai-dynamo/grove/operator/internal/utils"

	"github.com/samber/lo"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1validation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

const (
	maxCombinedResourceNameLength = 45
)

var allowedStartupTypes = sets.New(grovecorev1alpha1.CliqueStartupTypeInOrder, grovecorev1alpha1.CliqueStartupTypeAnyOrder, grovecorev1alpha1.CliqueStartupTypeExplicit)

// pcsValidator validates PodCliqueSet resources for create and update operations.
type pcsValidator struct {
	operation              admissionv1.Operation
	pcs                    *grovecorev1alpha1.PodCliqueSet
	tasEnabled             bool
	clusterTopologyDomains []string
	schedulerConfig        groveconfigv1alpha1.SchedulerConfiguration
}

// newPCSValidator creates a new PodCliqueSet validator for the given operation.
// schedulerConfig is the full scheduler configuration; the validator uses it for
// scheduler-name matching and may use per-scheduler config for future validations.
func newPCSValidator(pcs *grovecorev1alpha1.PodCliqueSet, operation admissionv1.Operation, tasConfig groveconfigv1alpha1.TopologyAwareSchedulingConfiguration, schedulerConfig groveconfigv1alpha1.SchedulerConfiguration) *pcsValidator {
	topologyDomains := lo.Map(tasConfig.Levels, func(level grovecorev1alpha1.TopologyLevel, _ int) string {
		return string(level.Domain)
	})
	return &pcsValidator{
		operation:              operation,
		pcs:                    pcs,
		tasEnabled:             tasConfig.Enabled,
		clusterTopologyDomains: topologyDomains,
		schedulerConfig:        schedulerConfig,
	}
}

// ---------------------------- validate create of PodCliqueSet -----------------------------------------------

// validate validates the PodCliqueSet object.
func (v *pcsValidator) validate() ([]string, field.ErrorList) {
	allErrs := field.ErrorList{}

	allErrs = append(allErrs, apivalidation.ValidateObjectMeta(&v.pcs.ObjectMeta, true,
		apivalidation.NameIsDNSSubdomain, field.NewPath("metadata"))...)
	fldPath := field.NewPath("spec")
	warnings, errs := v.validatePodCliqueSetSpec(fldPath)
	if len(errs) != 0 {
		allErrs = append(allErrs, errs...)
	}

	return warnings, allErrs
}

// validatePodCliqueSetSpec validates the specification of a PodCliqueSet object.
func (v *pcsValidator) validatePodCliqueSetSpec(fldPath *field.Path) ([]string, field.ErrorList) {
	allErrs := field.ErrorList{}

	allErrs = append(allErrs, apivalidation.ValidateNonnegativeField(int64(v.pcs.Spec.Replicas), fldPath.Child("replicas"))...)
	warnings, errs := v.validatePodCliqueSetTemplateSpec(fldPath.Child("template"))
	if len(errs) != 0 {
		allErrs = append(allErrs, errs...)
	}

	return warnings, allErrs
}

// validatePodCliqueSetTemplateSpec validates the template specification including startup type, cliques, scheduling policy, and scaling groups.
func (v *pcsValidator) validatePodCliqueSetTemplateSpec(fldPath *field.Path) ([]string, field.ErrorList) {
	allErrs := field.ErrorList{}

	allErrs = append(allErrs, validateEnumType(v.pcs.Spec.Template.StartupType, allowedStartupTypes, fldPath.Child("cliqueStartupType"))...)
	warnings, errs := v.validatePodCliqueTemplates(fldPath.Child("cliques"))
	if len(errs) != 0 {
		allErrs = append(allErrs, errs...)
	}
	allErrs = append(allErrs, v.validatePodCliqueScalingGroupConfigs(fldPath.Child("podCliqueScalingGroups"))...)
	allErrs = append(allErrs, v.validateTerminationDelay(fldPath.Child("terminationDelay"))...)

	return warnings, allErrs
}

// validatePodCliqueTemplates validates all PodClique templates ensuring unique names, roles, scheduler names, and proper dependencies.
func (v *pcsValidator) validatePodCliqueTemplates(fldPath *field.Path) ([]string, field.ErrorList) {
	allErrs := field.ErrorList{}

	var warnings []string
	cliqueTemplateSpecs := v.pcs.Spec.Template.Cliques
	if len(cliqueTemplateSpecs) == 0 {
		allErrs = append(allErrs, field.Required(fldPath, "at least one PodClique must be defined"))
	}

	// Get all clique names that belong to scaling groups
	scalingGroupCliqueNames := v.getScalingGroupCliqueNames()

	cliqueNames := make([]string, 0, len(cliqueTemplateSpecs))
	cliqueRoles := make([]string, 0, len(cliqueTemplateSpecs))
	schedulerNames := make([]string, 0, len(cliqueTemplateSpecs))
	for i, cliqueTemplateSpec := range cliqueTemplateSpecs {
		warns, errs := v.validatePodCliqueTemplateSpec(cliqueTemplateSpec, fldPath.Index(i), scalingGroupCliqueNames)
		if len(errs) != 0 {
			allErrs = append(allErrs, errs...)
		}
		if len(warns) != 0 {
			warnings = append(warnings, warns...)
		}
		cliqueNames = append(cliqueNames, cliqueTemplateSpec.Name)
		cliqueRoles = append(cliqueRoles, cliqueTemplateSpec.Spec.RoleName)
		schedulerNames = append(schedulerNames, cliqueTemplateSpec.Spec.PodSpec.SchedulerName)
	}

	allErrs = append(allErrs, sliceMustHaveUniqueElements(cliqueNames, fldPath.Child("name"), "cliqueTemplateSpec names must be unique")...)
	allErrs = append(allErrs, sliceMustHaveUniqueElements(cliqueRoles, fldPath.Child("roleName"), "cliqueTemplateSpec.Spec roleNames must be unique")...)

	// Validate scheduler names: all must be the same
	// When schedulerName is empty, use the default backend's name from schedulerbackend.
	uniqueSchedulerNames := lo.Uniq(lo.Map(schedulerNames, func(item string, _ int) string {
		if item == "" {
			if def := schedulerbackend.GetDefault(); def != nil {
				return def.Name()
			}
			return "default-scheduler" // fallback when backend not initialized (e.g. in tests)
		}
		return item
	}))
	if len(uniqueSchedulerNames) > 1 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("spec").Child("podSpec").Child("schedulerName"), uniqueSchedulerNames[0], "the schedulerName for all pods have to be the same"))
	}

	// Validate that the scheduler name is enabled (present in OperatorConfiguration profiles or default)
	pcsSchedulerName := ""
	if len(uniqueSchedulerNames) > 0 && uniqueSchedulerNames[0] != "" {
		pcsSchedulerName = uniqueSchedulerNames[0]
	}
	if pcsSchedulerName == "" {
		if def := schedulerbackend.GetDefault(); def != nil {
			pcsSchedulerName = def.Name()
		}
	}
	// default-scheduler is the pod-facing name for kube-scheduler and is always accepted when kube backend is enabled
	if pcsSchedulerName != "" && pcsSchedulerName != "default-scheduler" && schedulerbackend.Get(pcsSchedulerName) == nil {
		allErrs = append(allErrs, field.Invalid(
			fldPath.Child("spec").Child("podSpec").Child("schedulerName"),
			pcsSchedulerName,
			"schedulerName must be an enabled scheduler backend; this scheduler is not enabled in OperatorConfiguration",
		))
	}
	if v.isStartupTypeExplicit() {
		allErrs = append(allErrs, validateCliqueDependencies(cliqueTemplateSpecs, fldPath)...)
	}

	return warnings, allErrs
}

// validatePodCliqueNameConstraints validates that PodClique names meet DNS subdomain requirements and pod naming constraints.
func (v *pcsValidator) validatePodCliqueNameConstraints(fldPath *field.Path, cliqueTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, scalingGroupCliqueNames sets.Set[string]) field.ErrorList {
	allErrs := field.ErrorList{}
	if err := apivalidation.NameIsDNSSubdomain(cliqueTemplateSpec.Name, false); err != nil {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("name"), cliqueTemplateSpec.Name,
			"invalid PodCliqueTemplateSpec name, must be a valid DNS subdomain"))
	}

	// Only validate pod name constraints for PodCliques that are NOT part of any scaling group
	// any pod clique that is part of scaling groups will be checked as part of scaling group pod name constraints.
	if !scalingGroupCliqueNames.Has(cliqueTemplateSpec.Name) {
		allErrs = append(allErrs, validateStandalonePodClique(fldPath, v, cliqueTemplateSpec)...)
	}
	return allErrs
}

// validateStandalonePodClique validates pod naming constraints for PodCliques that are not part of any scaling group.
func validateStandalonePodClique(fldPath *field.Path, v *pcsValidator, cliqueTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec) field.ErrorList {
	allErrs := field.ErrorList{}
	if err := validatePodNameConstraints(v.pcs.Name, "", cliqueTemplateSpec.Name); err != nil {
		// add error to each of filed paths that compose the podName in case of a PodCliqueTemplateSpec
		allErrs = append(allErrs, field.Invalid(fldPath.Child("name"), cliqueTemplateSpec.Name, err.Error()))
		allErrs = append(allErrs, field.Invalid(field.NewPath("metadata").Child("name"), v.pcs.Name, err.Error()))
	}
	return allErrs
}

// validatePodCliqueScalingGroupConfigs validates scaling group configurations including name uniqueness, clique references, and replica settings.
func (v *pcsValidator) validatePodCliqueScalingGroupConfigs(fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	allPodCliqueSetCliqueNames := lo.Map(v.pcs.Spec.Template.Cliques, func(cliqueTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec, _ int) string {
		return cliqueTemplateSpec.Name
	})
	pclqScalingGroupNames := make([]string, 0, len(v.pcs.Spec.Template.PodCliqueScalingGroupConfigs))
	var cliqueNamesAcrossAllScalingGroups []string

	for i, scalingGroupConfig := range v.pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if err := apivalidation.NameIsDNSSubdomain(scalingGroupConfig.Name, false); err != nil {
			allErrs = append(allErrs, field.Invalid(fldPath.Index(i).Child("name"), scalingGroupConfig.Name,
				"invalid PodCliqueScalingGroupConfig name, must be a valid DNS subdomain"))
		}
		pclqScalingGroupNames = append(pclqScalingGroupNames, scalingGroupConfig.Name)
		cliqueNamesAcrossAllScalingGroups = append(cliqueNamesAcrossAllScalingGroups, scalingGroupConfig.CliqueNames...)
		// validate that scaling groups only contains clique names that are defined in the PodCliqueSet.
		allErrs = append(allErrs, v.validateScalingGroupPodCliqueNames(scalingGroupConfig.Name, allPodCliqueSetCliqueNames,
			scalingGroupConfig.CliqueNames, fldPath.Index(i).Child("cliqueNames"), fldPath.Index(i).Child("name"))...)

		// validate Replicas field
		if scalingGroupConfig.Replicas != nil {
			if *scalingGroupConfig.Replicas <= 0 {
				allErrs = append(allErrs, field.Invalid(fldPath.Index(i).Child("replicas"), *scalingGroupConfig.Replicas, "must be greater than 0"))
			}
		}

		// validate MinAvailable field
		if scalingGroupConfig.MinAvailable != nil {
			if *scalingGroupConfig.MinAvailable <= 0 {
				allErrs = append(allErrs, field.Invalid(fldPath.Index(i).Child("minAvailable"), *scalingGroupConfig.MinAvailable, "must be greater than 0"))
			}
		}

		// validate MinAvailable <= Replicas
		if scalingGroupConfig.Replicas != nil && scalingGroupConfig.MinAvailable != nil {
			if *scalingGroupConfig.MinAvailable > *scalingGroupConfig.Replicas {
				allErrs = append(allErrs, field.Invalid(fldPath.Index(i).Child("minAvailable"), *scalingGroupConfig.MinAvailable, "minAvailable must not be greater than replicas"))
			}
		}

		// validate ScaleConfig.MinReplicas >= MinAvailable
		if scalingGroupConfig.ScaleConfig != nil && scalingGroupConfig.MinAvailable != nil {
			if scalingGroupConfig.ScaleConfig.MinReplicas != nil && *scalingGroupConfig.ScaleConfig.MinReplicas < *scalingGroupConfig.MinAvailable {
				allErrs = append(allErrs, field.Invalid(fldPath.Index(i).Child("scaleConfig", "minReplicas"), *scalingGroupConfig.ScaleConfig.MinReplicas, "scaleConfig.minReplicas must be greater than or equal to minAvailable"))
			}
		}
	}

	// validate that the scaling group names are unique
	allErrs = append(allErrs, sliceMustHaveUniqueElements(pclqScalingGroupNames, fldPath.Child("name"), "PodCliqueScalingGroupConfig names must be unique")...)
	// validate that there should not be any overlapping clique names across scaling groups.
	allErrs = append(allErrs, sliceMustHaveUniqueElements(cliqueNamesAcrossAllScalingGroups, fldPath.Child("cliqueNames"), "clique names must not overlap across scaling groups, every scaling group should have unique clique names")...)

	// validate that for all pod cliques that are part of defined scaling groups, separate AutoScalingConfig is not defined for them.
	scalingGroupCliqueNames := lo.Uniq(cliqueNamesAcrossAllScalingGroups)
	for _, cliqueTemplateSpec := range v.pcs.Spec.Template.Cliques {
		if slices.Contains(scalingGroupCliqueNames, cliqueTemplateSpec.Name) && cliqueTemplateSpec.Spec.ScaleConfig != nil {
			allErrs = append(allErrs, field.Invalid(fldPath, cliqueTemplateSpec.Name, "AutoScalingConfig is not allowed to be defined for PodClique that is part of scaling group"))
		}
	}

	return allErrs
}

// validateTerminationDelay validates that terminationDelay is set and greater than zero.
func (v *pcsValidator) validateTerminationDelay(fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	// This should ideally not happen, the defaulting webhook will always set the default value for terminationDelay.
	if v.pcs.Spec.Template.TerminationDelay == nil {
		return append(allErrs, field.Required(fldPath, "terminationDelay is required"))
	}
	if v.pcs.Spec.Template.TerminationDelay.Duration <= 0 {
		allErrs = append(allErrs, field.Invalid(fldPath, v.pcs.Spec.Template.TerminationDelay, "terminationDelay must be greater than 0"))
	}

	return allErrs
}

func (v *pcsValidator) validateTopologyConstraintsOnCreate() field.ErrorList {
	topoConstraintsValidator := newTopologyConstraintsValidator(v.pcs, v.tasEnabled, v.clusterTopologyDomains)
	return topoConstraintsValidator.validate()
}

// validatePodCliqueTemplateSpec validates a single PodClique template specification including metadata and spec.
func (v *pcsValidator) validatePodCliqueTemplateSpec(cliqueTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec,
	fldPath *field.Path, scalingGroupCliqueNames sets.Set[string]) ([]string, field.ErrorList) {
	allErrs := field.ErrorList{}

	allErrs = append(allErrs, validateNonEmptyStringField(cliqueTemplateSpec.Name, fldPath.Child("name"))...)
	allErrs = append(allErrs, metav1validation.ValidateLabels(cliqueTemplateSpec.Labels, fldPath.Child("labels"))...)
	allErrs = append(allErrs, apivalidation.ValidateAnnotations(cliqueTemplateSpec.Annotations, fldPath.Child("annotations"))...)

	warnings, errs := v.validatePodCliqueSpec(cliqueTemplateSpec.Name, cliqueTemplateSpec.Spec, fldPath.Child("spec"))
	if len(errs) != 0 {
		allErrs = append(allErrs, errs...)
	}
	allErrs = append(allErrs, v.validatePodCliqueNameConstraints(fldPath, cliqueTemplateSpec, scalingGroupCliqueNames)...)

	return warnings, allErrs
}

// validateCliqueDependencies validates that all clique dependencies refer to existing cliques and contain no circular dependencies.
func validateCliqueDependencies(cliques []*grovecorev1alpha1.PodCliqueTemplateSpec, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	depG := NewPodCliqueDependencyGraph()
	var discoveredCliqueNames []string
	for _, clique := range cliques {
		discoveredCliqueNames = append(discoveredCliqueNames, clique.Name)
		depG.AddDependencies(clique.Name, clique.Spec.StartsAfter)
	}

	unknownCliquesDeps := depG.GetUnknownCliques(discoveredCliqueNames)
	if len(unknownCliquesDeps) > 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("startsAfter"),
			strings.Join(unknownCliquesDeps, ","), "unknown clique names found, all clique dependencies must be defined as cliques"))
	}

	// check for strongly connected components a.k.a cycles in the directed graph of clique dependencies
	cycles := depG.GetStronglyConnectedCliques()
	if len(cycles) > 0 {
		allErrs = append(allErrs, field.Invalid(fldPath, cycles, "clique must not have circular dependencies"))
	}

	return allErrs
}

// getScalingGroupCliqueNames returns a set of all clique names that belong to scaling groups.
func (v *pcsValidator) getScalingGroupCliqueNames() sets.Set[string] {
	scalingGroupCliqueNames := sets.New[string]()
	for _, scalingGroupConfig := range v.pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		scalingGroupCliqueNames.Insert(scalingGroupConfig.CliqueNames...)
	}
	return scalingGroupCliqueNames
}

// validateScalingGroupPodCliqueNames validates that scaling group clique references exist and meet naming constraints.
func (v *pcsValidator) validateScalingGroupPodCliqueNames(pcsgName string, allPclqNames, pclqNameInScalingGrp []string, fldPath, pcsgNameFieldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	_, unidentifiedPclqNames := lo.Difference(allPclqNames, lo.Uniq(pclqNameInScalingGrp))
	if len(unidentifiedPclqNames) > 0 {
		allErrs = append(allErrs, field.Invalid(fldPath, strings.Join(unidentifiedPclqNames, ","), "unidentified PodClique names found"))
	}

	// validate scaling group  PodClique pods names are valid.
	for i, pclqName := range pclqNameInScalingGrp {
		if err := validatePodNameConstraints(v.pcs.Name, pcsgName, pclqName); err != nil {
			// add error to each of filed paths that compose the podName
			allErrs = append(allErrs, field.Invalid(fldPath.Index(i).Child("name"), pclqName, err.Error()))
			allErrs = append(allErrs, field.Invalid(pcsgNameFieldPath, pclqName, err.Error()))
			allErrs = append(allErrs, field.Invalid(field.NewPath("metadata").Child("name"), v.pcs.Name, err.Error()))
		}
	}
	return allErrs
}

// validatePodCliqueSpec validates the specification of a PodClique including replicas, minAvailable, dependencies, and autoscaling configuration.
func (v *pcsValidator) validatePodCliqueSpec(name string, cliqueSpec grovecorev1alpha1.PodCliqueSpec, fldPath *field.Path) ([]string, field.ErrorList) {
	allErrs := field.ErrorList{}

	if cliqueSpec.Replicas <= 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("replicas"), cliqueSpec.Replicas, "must be greater than 0"))
	}

	// Ideally this should never happen, the defaulting webhook will always set the default value for minAvailable.
	if cliqueSpec.MinAvailable == nil {
		allErrs = append(allErrs, field.Required(fldPath.Child("minAvailable"), "field is required"))
	} else {
		// prevent nil pointer dereference, no point checking the value if it is nil
		if *cliqueSpec.MinAvailable <= 0 {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("minAvailable"), *cliqueSpec.MinAvailable, "must be greater than 0"))
		}
		if *cliqueSpec.MinAvailable > cliqueSpec.Replicas {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("minAvailable"), *cliqueSpec.MinAvailable, "minAvailable must not be greater than replicas"))
		}
	}

	if v.isStartupTypeExplicit() && len(cliqueSpec.StartsAfter) > 0 {
		for _, dep := range cliqueSpec.StartsAfter {
			if utils.IsEmptyStringType(dep) {
				allErrs = append(allErrs, field.Required(fldPath.Child("startsAfter"), "clique dependency must not be empty"))
			}
			if dep == name {
				allErrs = append(allErrs, field.Invalid(fldPath.Child("startsAfter"), dep, "clique dependency cannot refer to itself"))
			}
		}
		allErrs = append(allErrs, sliceMustHaveUniqueElements(cliqueSpec.StartsAfter, fldPath.Child("startsAfter"), "clique dependencies must be unique")...)
	}

	if cliqueSpec.ScaleConfig != nil {
		allErrs = append(allErrs, validateScaleConfig(cliqueSpec.ScaleConfig, *cliqueSpec.MinAvailable, fldPath.Child("autoScalingConfig"))...)
		if cliqueSpec.ScaleConfig.MaxReplicas < cliqueSpec.Replicas {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("autoScalingConfig", "maxReplicas"), cliqueSpec.ScaleConfig.MaxReplicas, "must be greater than or equal to replicas"))
		}
	}

	warnings, cliquePodSpecErrs := v.validatePodSpec(cliqueSpec.PodSpec, fldPath.Child("podSpec"))
	if len(cliquePodSpecErrs) != 0 {
		allErrs = append(allErrs, cliquePodSpecErrs...)
	}

	return warnings, allErrs
}

// isStartupTypeExplicit returns true if the startup type is Explicit.
func (v *pcsValidator) isStartupTypeExplicit() bool {
	return v.pcs.Spec.Template.StartupType != nil && *v.pcs.Spec.Template.StartupType == grovecorev1alpha1.CliqueStartupTypeExplicit
}

// validateScaleConfig validates autoscaling configuration ensuring minReplicas and maxReplicas are properly set relative to minAvailable.
func validateScaleConfig(scaleConfig *grovecorev1alpha1.AutoScalingConfig, minAvailable int32, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	// This should ideally not happen, the defaulting webhook will always set the default value for minReplicas.
	if scaleConfig.MinReplicas == nil {
		allErrs = append(allErrs, field.Required(fldPath.Child("minReplicas"), "field is required"))
	}
	// scaleConfig.MinReplicas should be greater than or equal to minAvailable else it will trigger a PodGang termination.
	if *scaleConfig.MinReplicas < minAvailable {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("minReplicas"), *scaleConfig.MinReplicas, "must be greater than or equal to podCliqueSpec.minAvailable"))
	}
	if scaleConfig.MaxReplicas < *scaleConfig.MinReplicas {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("maxReplicas"), scaleConfig.MaxReplicas, "must be greater than or equal to podCliqueSpec.minReplicas"))
	}
	return allErrs
}

// validatePodSpec validates the PodSpec ensuring certain fields are not set that would conflict with operator management.
func (v *pcsValidator) validatePodSpec(spec corev1.PodSpec, fldPath *field.Path) ([]string, field.ErrorList) {
	allErrs := field.ErrorList{}
	var warnings []string

	if !utils.IsEmptyStringType(spec.RestartPolicy) {
		warnings = append(warnings, "restartPolicy will be ignored, it will be set to Always")
	}

	specFldPath := fldPath.Child("spec")
	if v.operation == admissionv1.Create {
		if spec.TopologySpreadConstraints != nil {
			allErrs = append(allErrs, field.Invalid(specFldPath.Child("topologySpreadConstraints"), spec.TopologySpreadConstraints, "must not be set"))
		}
		if !utils.IsEmptyStringType(spec.NodeName) {
			allErrs = append(allErrs, field.Invalid(specFldPath.Child("nodeName"), spec.NodeName, "must not be set"))
		}
	}

	return warnings, allErrs
}

// ---------------------------- validate update of PodCliqueSet -----------------------------------------------

// validateUpdate validates the update to a PodCliqueSet object. It compares the old and new PodCliqueSet objects and validates that the changes done are allowed/valid.
func (v *pcsValidator) validateUpdate(oldPCS *grovecorev1alpha1.PodCliqueSet) error {
	allErrs := field.ErrorList{}
	fldPath := field.NewPath("spec")
	allErrs = append(allErrs, v.validatePodCliqueSetSpecUpdate(oldPCS, fldPath)...)
	return allErrs.ToAggregate()
}

// validatePodCliqueSetSpecUpdate validates updates to the PodCliqueSet specification.
func (v *pcsValidator) validatePodCliqueSetSpecUpdate(oldPCS *grovecorev1alpha1.PodCliqueSet, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, v.validatePodCliqueSetTemplateSpecUpdate(oldPCS, fldPath.Child("template"))...)
	return allErrs
}

// validatePodCliqueSetTemplateSpecUpdate validates updates to the template specification ensuring immutability of critical fields.
func (v *pcsValidator) validatePodCliqueSetTemplateSpecUpdate(oldPCS *grovecorev1alpha1.PodCliqueSet, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	allErrs = append(allErrs, v.validatePodCliqueUpdate(oldPCS.Spec.Template.Cliques, fldPath.Child("cliques"))...)
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(v.pcs.Spec.Template.StartupType, oldPCS.Spec.Template.StartupType, fldPath.Child("cliqueStartupType"))...)
	allErrs = append(allErrs, v.validatePodCliqueScalingGroupConfigsUpdate(oldPCS.Spec.Template.PodCliqueScalingGroupConfigs, fldPath.Child("podCliqueScalingGroups"))...)
	allErrs = append(allErrs, v.validateTopologyConstraintsUpdate(oldPCS)...)

	return allErrs
}

// validatePodCliqueScalingGroupConfigsUpdate validates immutable fields in PodCliqueScalingGroupConfigs.
func (v *pcsValidator) validatePodCliqueScalingGroupConfigsUpdate(oldConfigs []grovecorev1alpha1.PodCliqueScalingGroupConfig, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	newConfigs := v.pcs.Spec.Template.PodCliqueScalingGroupConfigs
	// Validate that scaling group composition hasn't changed
	if len(newConfigs) != len(oldConfigs) {
		allErrs = append(allErrs, field.Forbidden(fldPath, "not allowed to add or remove PodCliqueScalingGroupConfigs"))
		return allErrs
	}

	// Create a map of old configs by name for efficient lookup
	oldConfigMap := lo.SliceToMap(oldConfigs, func(config grovecorev1alpha1.PodCliqueScalingGroupConfig) (string, grovecorev1alpha1.PodCliqueScalingGroupConfig) {
		return config.Name, config
	})

	// Validate each new config against its corresponding old config by name
	for _, newConfig := range newConfigs {
		oldConfig, exists := oldConfigMap[newConfig.Name]
		if !exists {
			allErrs = append(allErrs, field.Forbidden(fldPath.Child("name"), fmt.Sprintf("not allowed to change scaling group composition, new scaling group name '%s' is not allowed", newConfig.Name)))
			continue
		}

		// Validate immutable fields
		configFldPath := fldPath
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(newConfig.CliqueNames, oldConfig.CliqueNames, configFldPath.Child("cliqueNames"))...)
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(newConfig.MinAvailable, oldConfig.MinAvailable, configFldPath.Child("minAvailable"))...)
	}

	return allErrs
}

func (v *pcsValidator) validateTopologyConstraintsUpdate(oldPCS *grovecorev1alpha1.PodCliqueSet) field.ErrorList {
	return newTopologyConstraintsValidator(v.pcs, v.tasEnabled, v.clusterTopologyDomains).validateUpdate(oldPCS)
}

// requiresOrderValidation checks if the StartupType requires clique order validation.
func requiresOrderValidation(startupType *grovecorev1alpha1.CliqueStartupType) bool {
	return startupType != nil && (*startupType == grovecorev1alpha1.CliqueStartupTypeInOrder || *startupType == grovecorev1alpha1.CliqueStartupTypeExplicit)
}

// validatePodCliqueUpdate validates that PodClique updates maintain composition, order (when required), and immutable fields.
func (v *pcsValidator) validatePodCliqueUpdate(oldCliques []*grovecorev1alpha1.PodCliqueTemplateSpec, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	newCliques := v.pcs.Spec.Template.Cliques
	if len(newCliques) != len(oldCliques) {
		allErrs = append(allErrs, field.Forbidden(fldPath, "not allowed to change clique composition"))
	}

	// Create a map of old cliques by name for efficient lookup
	// this allows checking the type and order without dealing with non-existent indexes in a slice of the old cliques
	// if the length the old cliques and new cliques is different, this is an error but we don't return it immediately so that further validation can be done
	// therefore we should not assume the length of the oldCliques slice is the same as the newCliques slice
	oldCliqueIndexMap := make(map[string]lo.Tuple2[int, *grovecorev1alpha1.PodCliqueTemplateSpec], len(oldCliques))
	lo.ForEach(oldCliques, func(clique *grovecorev1alpha1.PodCliqueTemplateSpec, i int) {
		oldCliqueIndexMap[clique.Name] = lo.Tuple2[int, *grovecorev1alpha1.PodCliqueTemplateSpec]{A: i, B: clique}
	})
	orderIsEnforced := requiresOrderValidation(v.pcs.Spec.Template.StartupType)
	// Validate each new clique against its corresponding old clique
	for newCliqueIndex, newClique := range newCliques {
		oldIndexCliqueTuple, exists := oldCliqueIndexMap[newClique.Name]
		if !exists {
			allErrs = append(allErrs, field.Forbidden(fldPath.Child("name"), fmt.Sprintf("not allowed to change clique composition, new clique name '%s' is not allowed", newClique.Name)))
			continue
		}

		// Validate clique order for StartupType InOrder and Explicit
		// If the StartupType is InOrder or Explicit, the order of cliques cannot change
		// the index new clique is compared with the index of the old clique
		if orderIsEnforced && newCliqueIndex != oldIndexCliqueTuple.A {
			allErrs = append(allErrs, field.Invalid(fldPath, newClique.Name,
				fmt.Sprintf("clique order cannot be changed when StartupType is InOrder or Explicit. Expected '%s' at position %d, got '%s'",
					oldIndexCliqueTuple.B.Name, newCliqueIndex, newClique.Name)))
		}

		// Validate immutable PodClique fields
		cliqueFldPath := fldPath.Child("spec")
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(newClique.Spec.RoleName, oldIndexCliqueTuple.B.Spec.RoleName, cliqueFldPath.Child("roleName"))...)
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(newClique.Spec.MinAvailable, oldIndexCliqueTuple.B.Spec.MinAvailable, cliqueFldPath.Child("minAvailable"))...)
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(newClique.Spec.StartsAfter, oldIndexCliqueTuple.B.Spec.StartsAfter, cliqueFldPath.Child("startsAfter"))...)
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(newClique.Spec.PodSpec.SchedulerName, oldIndexCliqueTuple.B.Spec.PodSpec.SchedulerName, cliqueFldPath.Child("podSpec", "schedulerName"))...)
	}

	return allErrs
}

// validatePodNameConstraints validates Grove pod name component constraints.
// This function validates the constraints for component names that will be used
// to construct pod names.
//
// Pod names that belong to a PCSG follow the format:
// <pcs-name>-<pcs-index>-<pcsg-name>-<pcsg-index>-<pclq-name>-<random>
//
// Pod names that do not belong to a PCSG follow the format:
// <pcs-name>-<pcs-index>-<pclq-name>-<random>
//
// Constraints:
// - Random string + hyphens: 10 chars for PCSG pods, 8 chars for non-PCSG pods
// - Max sum of all resource name characters: 45 chars
func validatePodNameConstraints(pcsName, pcsgName, pclqName string) error {
	// Check resource name constraints
	resourceNameLength := len(pcsName) + len(pclqName)
	if pcsgName != "" {
		resourceNameLength += len(pcsgName)
	}

	if resourceNameLength > maxCombinedResourceNameLength {
		if pcsgName != "" {
			return fmt.Errorf("combined resource name length %d exceeds 45-character limit required for pod naming. Consider shortening: PodCliqueSet '%s', PodCliqueScalingGroup '%s', or PodClique '%s'",
				resourceNameLength, pcsName, pcsgName, pclqName)
		}
		return fmt.Errorf("combined resource name length %d exceeds 45-character limit required for pod naming. Consider shortening: PodCliqueSet '%s' or PodClique '%s'",
			resourceNameLength, pcsName, pclqName)
	}
	return nil
}

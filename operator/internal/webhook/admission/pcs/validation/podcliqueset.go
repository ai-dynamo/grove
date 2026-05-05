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
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	groveconfigv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/clustertopology"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	schedmanager "github.com/ai-dynamo/grove/operator/internal/scheduler/manager"
	"github.com/ai-dynamo/grove/operator/internal/utils"

	"github.com/samber/lo"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1validation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/util/sets"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	maxCombinedResourceNameLength = 45
)

var allowedStartupTypes = sets.New(grovecorev1alpha1.CliqueStartupTypeInOrder, grovecorev1alpha1.CliqueStartupTypeAnyOrder, grovecorev1alpha1.CliqueStartupTypeExplicit)

// pcsValidator validates PodCliqueSet resources for create and update operations.
type pcsValidator struct {
	operation       admissionv1.Operation
	pcs             *grovecorev1alpha1.PodCliqueSet
	tasEnabled      bool
	schedulerConfig groveconfigv1alpha1.SchedulerConfiguration
	client          client.Client
}

// newPCSValidator creates a new PodCliqueSet validator for the given operation.
// schedulerConfig is the full scheduler configuration; the validator uses it for
// scheduler-name matching and may use per-scheduler config for future validations.
func newPCSValidator(pcs *grovecorev1alpha1.PodCliqueSet, operation admissionv1.Operation, tasConfig groveconfigv1alpha1.TopologyAwareSchedulingConfiguration, schedulerConfig groveconfigv1alpha1.SchedulerConfiguration, cl client.Client) *pcsValidator {
	return &pcsValidator{
		operation:       operation,
		pcs:             pcs,
		tasEnabled:      tasConfig.Enabled,
		schedulerConfig: schedulerConfig,
		client:          cl,
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
	allErrs = append(allErrs, v.validateResourceClaimTemplates(fldPath.Child("resourceClaimTemplates"))...)
	allErrs = append(allErrs, v.validatePCSResourceSharing(v.pcs.Spec.Template.ResourceSharing, fldPath.Child("resourceSharing"))...)
	warnings, errs := v.validatePodCliqueTemplates(fldPath.Child("cliques"))
	if len(errs) != 0 {
		allErrs = append(allErrs, errs...)
	}
	allErrs = append(allErrs, v.validatePodCliqueScalingGroupConfigs(fldPath.Child("podCliqueScalingGroups"))...)
	allErrs = append(allErrs, v.validateTerminationDelay(fldPath.Child("terminationDelay"))...)

	return warnings, allErrs
}

// validateResourceClaimTemplates validates PCS-level ResourceClaimTemplateConfig entries.
func (v *pcsValidator) validateResourceClaimTemplates(fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	names := make([]string, 0, len(v.pcs.Spec.Template.ResourceClaimTemplates))
	for i, rct := range v.pcs.Spec.Template.ResourceClaimTemplates {
		rctPath := fldPath.Index(i)
		if rct.Name == "" {
			allErrs = append(allErrs, field.Required(rctPath.Child("name"), "template name is required"))
		}
		if len(rct.TemplateSpec.Spec.Devices.Requests) == 0 {
			allErrs = append(allErrs, field.Required(rctPath.Child("templateSpec", "spec", "devices", "requests"), "at least one device request is required"))
		}
		names = append(names, rct.Name)
	}
	allErrs = append(allErrs, sliceMustHaveUniqueElements(names, fldPath.Child("name"))...)
	return allErrs
}

// validatePCSResourceSharing validates PCS-level ResourceSharing entries and their filters.
func (v *pcsValidator) validatePCSResourceSharing(refs []grovecorev1alpha1.PCSResourceSharingSpec, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	bases := make([]grovecorev1alpha1.ResourceSharingSpec, len(refs))
	for i := range refs {
		bases[i] = refs[i].ResourceSharingSpec
	}
	allErrs = append(allErrs, v.validateResourceSharingSpecs(bases, fldPath)...)

	cliqueNames := sets.New[string]()
	for _, c := range v.pcs.Spec.Template.Cliques {
		cliqueNames.Insert(c.Name)
	}
	groupNames := sets.New[string]()
	for _, g := range v.pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		groupNames.Insert(g.Name)
	}
	for i, ref := range refs {
		if ref.Filter == nil {
			continue
		}
		filterPath := fldPath.Index(i).Child("filter")
		if len(ref.Filter.ChildCliqueNames) == 0 && len(ref.Filter.ChildScalingGroupNames) == 0 {
			allErrs = append(allErrs, field.Required(filterPath, "filter must specify at least one childCliqueNames or childScalingGroupNames entry"))
		}
		for j, cn := range ref.Filter.ChildCliqueNames {
			if !cliqueNames.Has(cn) {
				allErrs = append(allErrs, field.NotFound(filterPath.Child("childCliqueNames").Index(j), cn))
			}
		}
		for j, gn := range ref.Filter.ChildScalingGroupNames {
			if !groupNames.Has(gn) {
				allErrs = append(allErrs, field.NotFound(filterPath.Child("childScalingGroupNames").Index(j), gn))
			}
		}
	}
	return allErrs
}

// validatePCSGResourceSharing validates PCSG-level ResourceSharing entries and their filters.
func (v *pcsValidator) validatePCSGResourceSharing(cfg grovecorev1alpha1.PodCliqueScalingGroupConfig, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	cliqueNameSet := sets.New(cfg.CliqueNames...)
	bases := make([]grovecorev1alpha1.ResourceSharingSpec, len(cfg.ResourceSharing))
	for i := range cfg.ResourceSharing {
		bases[i] = cfg.ResourceSharing[i].ResourceSharingSpec
	}
	allErrs = append(allErrs, v.validateResourceSharingSpecs(bases, fldPath)...)
	for i, ref := range cfg.ResourceSharing {
		if ref.Filter == nil {
			continue
		}
		filterPath := fldPath.Index(i).Child("filter")
		if len(ref.Filter.ChildCliqueNames) == 0 {
			allErrs = append(allErrs, field.Required(filterPath, "filter must specify at least one childCliqueNames entry"))
		}
		for j, cn := range ref.Filter.ChildCliqueNames {
			if !cliqueNameSet.Has(cn) {
				allErrs = append(allErrs, field.NotFound(filterPath.Child("childCliqueNames").Index(j), cn))
			}
		}
	}
	return allErrs
}

// validateResourceSharingSpecs validates the common fields (Name, Namespace, Scope) across all levels.
func (v *pcsValidator) validateResourceSharingSpecs(refs []grovecorev1alpha1.ResourceSharingSpec, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	templateNames := sets.New[string]()
	for _, rct := range v.pcs.Spec.Template.ResourceClaimTemplates {
		templateNames.Insert(rct.Name)
	}
	validScopes := sets.New(
		grovecorev1alpha1.ResourceSharingScopeAllReplicas,
		grovecorev1alpha1.ResourceSharingScopePerReplica,
	)
	seenNames := sets.New[string]()
	for i, ref := range refs {
		refPath := fldPath.Index(i)
		if ref.Name == "" {
			allErrs = append(allErrs, field.Required(refPath.Child("name"), "reference name is required"))
		}
		if ref.Name != "" && seenNames.Has(ref.Name) {
			allErrs = append(allErrs, field.Duplicate(refPath.Child("name"), ref.Name))
		}
		seenNames.Insert(ref.Name)
		if ref.Namespace != "" && ref.Name != "" && templateNames.Has(ref.Name) {
			allErrs = append(allErrs, field.Invalid(refPath.Child("namespace"), ref.Namespace, "namespace must be empty when name matches an internal resourceClaimTemplate"))
		}
		if !validScopes.Has(ref.Scope) {
			allErrs = append(allErrs, field.NotSupported(refPath.Child("scope"), ref.Scope, validScopes.UnsortedList()))
		}
	}
	return allErrs
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

	allErrs = append(allErrs, sliceMustHaveUniqueElements(cliqueNames, fldPath.Child("name"))...)
	allErrs = append(allErrs, sliceMustHaveUniqueElements(cliqueRoles, fldPath.Child("roleName"))...)

	allErrs = append(allErrs, v.validateSchedulerNames(schedulerNames, fldPath)...)

	if v.isStartupTypeExplicit() {
		allErrs = append(allErrs, validateCliqueDependencies(cliqueTemplateSpecs, fldPath)...)
	}

	return warnings, allErrs
}

// validateSchedulerNames ensures all pod scheduler names resolve to the same scheduler and that scheduler is enabled.
// Empty schedulerName is resolved to the default backend name from schedmanager.GetDefault().
func (v *pcsValidator) validateSchedulerNames(schedulerNames []string, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	specPath := fldPath.Child("spec").Child("podSpec").Child("schedulerName")

	defaultSchedulerName := string(groveconfigv1alpha1.SchedulerNameKube)
	if def := schedmanager.GetDefault(); def != nil {
		defaultSchedulerName = def.Name()
	}

	// Resolve empty to default backend name; then require all resolved names to be the same.
	uniqueSchedulerNames := lo.Uniq(lo.Map(schedulerNames, func(item string, _ int) string {
		if item == "" {
			return defaultSchedulerName
		}
		return item
	}))
	if len(uniqueSchedulerNames) > 1 {
		allErrs = append(allErrs, field.Invalid(specPath, strings.Join(uniqueSchedulerNames, ", "), "the schedulerName for all pods have to be the same"))
	}

	// Validate that the resolved scheduler is enabled.
	pcsSchedulerName := ""
	if len(uniqueSchedulerNames) > 0 && uniqueSchedulerNames[0] != "" {
		pcsSchedulerName = uniqueSchedulerNames[0]
	}
	if pcsSchedulerName != string(groveconfigv1alpha1.SchedulerNameKube) && schedmanager.Get(pcsSchedulerName) == nil {
		allErrs = append(allErrs, field.Invalid(
			specPath,
			pcsSchedulerName,
			"schedulerName must be an enabled scheduler backend; this scheduler is not enabled in OperatorConfiguration",
		))
	}
	return allErrs
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

		// validate PCSG-level ResourceSharing
		allErrs = append(allErrs, v.validatePCSGResourceSharing(scalingGroupConfig, fldPath.Index(i).Child("resourceSharing"))...)
	}

	// validate that the scaling group names are unique
	allErrs = append(allErrs, sliceMustHaveUniqueElements(pclqScalingGroupNames, fldPath.Child("name"))...)
	// validate that there should not be any overlapping clique names across scaling groups.
	allErrs = append(allErrs, sliceMustHaveUniqueElements(cliqueNamesAcrossAllScalingGroups, fldPath.Child("cliqueNames"))...)

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

func (v *pcsValidator) validateTopologyConstraintsOnCreate(ctx context.Context) field.ErrorList {
	if !v.tasEnabled {
		return newTopologyConstraintsValidator(v.pcs, v.tasEnabled, nil).validate()
	}
	domains, errs := v.resolveTopologyDomains(ctx)
	if len(errs) > 0 {
		return errs
	}
	return newTopologyConstraintsValidator(v.pcs, v.tasEnabled, domains).validate()
}

// validatePodCliqueTemplateSpec validates a single PodClique template specification including metadata and spec.
func (v *pcsValidator) validatePodCliqueTemplateSpec(cliqueTemplateSpec *grovecorev1alpha1.PodCliqueTemplateSpec,
	fldPath *field.Path, scalingGroupCliqueNames sets.Set[string]) ([]string, field.ErrorList) {
	allErrs := field.ErrorList{}

	allErrs = append(allErrs, validateNonEmptyStringField(cliqueTemplateSpec.Name, fldPath.Child("name"))...)
	allErrs = append(allErrs, metav1validation.ValidateLabels(cliqueTemplateSpec.Labels, fldPath.Child("labels"))...)
	allErrs = append(allErrs, apivalidation.ValidateAnnotations(cliqueTemplateSpec.Annotations, fldPath.Child("annotations"))...)

	allErrs = append(allErrs, v.validateResourceSharingSpecs(cliqueTemplateSpec.ResourceSharing, fldPath.Child("resourceSharing"))...)
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
		allErrs = append(allErrs, sliceMustHaveUniqueElements(cliqueSpec.StartsAfter, fldPath.Child("startsAfter"))...)
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
	} else {
		// scaleConfig.MinReplicas should be greater than or equal to minAvailable else it will trigger a PodGang termination.
		if *scaleConfig.MinReplicas < minAvailable {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("minReplicas"), *scaleConfig.MinReplicas, "must be greater than or equal to podCliqueSpec.minAvailable"))
		}
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

	for i, container := range spec.Containers {
		allErrs = append(allErrs, validateContainer(container, specFldPath.Child("containers").Index(i))...)
	}
	for i, container := range spec.InitContainers {
		allErrs = append(allErrs, validateContainer(container, specFldPath.Child("initContainers").Index(i))...)
	}

	return warnings, allErrs
}

// validateContainer validates a single container's fields.
func validateContainer(container corev1.Container, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, validateContainerEnvVars(container.Env, fldPath.Child("env"))...)
	return allErrs
}

// validateContainerEnvVars validates environment variable names, checking for invalid names and duplicates.
func validateContainerEnvVars(envVars []corev1.EnvVar, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	envNames := make([]string, 0, len(envVars))
	for j, envVar := range envVars {
		if errs := k8svalidation.IsEnvVarName(envVar.Name); len(errs) > 0 {
			allErrs = append(allErrs, field.Invalid(fldPath.Index(j).Child("name"), envVar.Name, strings.Join(errs, "; ")))
		}
		envNames = append(envNames, envVar.Name)
	}
	allErrs = append(allErrs, sliceMustHaveUniqueElements(envNames, fldPath)...)
	return allErrs
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
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(v.pcs.Spec.Template.ResourceClaimTemplates, oldPCS.Spec.Template.ResourceClaimTemplates, fldPath.Child("resourceClaimTemplates"))...)
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(v.pcs.Spec.Template.ResourceSharing, oldPCS.Spec.Template.ResourceSharing, fldPath.Child("resourceSharing"))...)

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
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(newConfig.ResourceSharing, oldConfig.ResourceSharing, configFldPath.Child("resourceSharing"))...)
	}

	return allErrs
}

func (v *pcsValidator) validateTopologyConstraintsUpdate(oldPCS *grovecorev1alpha1.PodCliqueSet) field.ErrorList {
	immutabilityValidator := newTopologyConstraintsValidator(v.pcs, v.tasEnabled, nil)

	// Allow only the legacy upgrade case to be repaired under the current rules: older objects could carry
	// packDomain without topologyName, because topologyName did not exist in the API yet. Other invalid shapes
	// are not treated as repairable legacy state here.
	if componentutils.HasAnyTopologyConstraint(oldPCS) && hasRepairableLegacyTopologyConstraint(oldPCS) {
		if _, err := componentutils.ResolveTopologyNameForPodCliqueSet(oldPCS); err != nil {
			if errors.Is(err, componentutils.ErrTopologyNameMissing) {
				if allErrs := immutabilityValidator.validateTopologyConstraintImmutability(oldPCS, field.NewPath("spec").Child("template"), true); len(allErrs) > 0 {
					return allErrs
				}
				return v.validateTopologyConstraintsOnCreate(context.Background())
			}
			// Surface any other resolution failure as an internal error rather than
			// assuming it is a repairable legacy state.
			return field.ErrorList{field.InternalError(field.NewPath("spec", "template", "topologyConstraint", "topologyName"), err)}
		}
	}

	// Domain/hierarchy validation is not needed on update because topology constraints are immutable.
	// Only topologyName and packDomain immutability are checked here for already valid objects.
	return immutabilityValidator.validateUpdate(oldPCS)
}

// resolveTopologyDomains resolves the ordered list of topology domains from the ClusterTopology
// referenced by the PCS's topologyName. Returns nil domains (no validation) if no topology constraints exist.
func (v *pcsValidator) resolveTopologyDomains(ctx context.Context) ([]string, field.ErrorList) {
	// No constraints at all — nothing to validate.
	if !componentutils.HasAnyTopologyConstraint(v.pcs) {
		return nil, nil
	}
	if completenessErrs := topologyConstraintCompletenessFieldErrors(v.pcs); len(completenessErrs) > 0 {
		return nil, completenessErrs
	}
	if mismatchErrs := multipleTopologyNamesFieldErrors(v.pcs); len(mismatchErrs) > 0 {
		return nil, mismatchErrs
	}

	topologyName, err := componentutils.ResolveTopologyNameForPodCliqueSet(v.pcs)
	if err != nil {
		fldPath := field.NewPath("spec", "template", "topologyConstraint")
		if errors.Is(err, componentutils.ErrTopologyNameMissing) {
			return nil, field.ErrorList{field.Required(fldPath,
				"topologyConstraint must specify both topologyName and packDomain")}
		}
		if errors.Is(err, componentutils.ErrMultipleTopologyNamesUnsupported) {
			return nil, field.ErrorList{field.Invalid(fldPath, nil,
				"all topologyConstraint.topologyName values within a PodCliqueSet must match in the current implementation")}
		}
		return nil, field.ErrorList{field.InternalError(fldPath, err)}
	}

	fldPath := field.NewPath("spec", "template", "topologyConstraint", "topologyName")

	// Fetch the referenced ClusterTopology.
	levels, err := clustertopology.GetClusterTopologyLevels(ctx, v.client, topologyName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, field.ErrorList{field.Invalid(fldPath, topologyName,
				fmt.Sprintf("ClusterTopology %q not found", topologyName))}
		}
		return nil, field.ErrorList{field.InternalError(fldPath,
			fmt.Errorf("failed to fetch ClusterTopology %q: %w", topologyName, err))}
	}

	domains := make([]string, len(levels))
	for i, level := range levels {
		domains[i] = string(level.Domain)
	}
	return domains, nil
}

func topologyConstraintCompletenessFieldErrors(pcs *grovecorev1alpha1.PodCliqueSet) field.ErrorList {
	var allErrs field.ErrorList

	validateConstraint := func(tc *grovecorev1alpha1.TopologyConstraint, tcPath *field.Path) {
		if tc == nil {
			return
		}
		if tc.TopologyName == "" {
			allErrs = append(allErrs, field.Required(tcPath.Child("topologyName"),
				"topologyName is required when topologyConstraint is set"))
		}
		if tc.PackDomain == "" {
			allErrs = append(allErrs, field.Required(tcPath.Child("packDomain"),
				"packDomain is required when topologyConstraint is set"))
		}
	}

	validateConstraint(pcs.Spec.Template.TopologyConstraint, field.NewPath("spec", "template", "topologyConstraint"))
	for i, clique := range pcs.Spec.Template.Cliques {
		validateConstraint(clique.TopologyConstraint, field.NewPath("spec", "template", "cliques").Index(i).Child("topologyConstraint"))
	}
	for i, pcsg := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		validateConstraint(pcsg.TopologyConstraint, field.NewPath("spec", "template", "podCliqueScalingGroups").Index(i).Child("topologyConstraint"))
	}

	return allErrs
}

func multipleTopologyNamesFieldErrors(pcs *grovecorev1alpha1.PodCliqueSet) field.ErrorList {
	var allErrs field.ErrorList
	topologyNames := map[string]struct{}{}

	recordTopologyName := func(name string) {
		if name != "" {
			topologyNames[name] = struct{}{}
		}
	}

	if pcs.Spec.Template.TopologyConstraint != nil {
		recordTopologyName(pcs.Spec.Template.TopologyConstraint.TopologyName)
	}

	for i, clique := range pcs.Spec.Template.Cliques {
		if clique.TopologyConstraint != nil {
			recordTopologyName(clique.TopologyConstraint.TopologyName)
		}
		if clique.TopologyConstraint != nil && len(topologyNames) > 1 {
			allErrs = append(allErrs, field.Invalid(
				field.NewPath("spec", "template", "cliques").Index(i).Child("topologyConstraint", "topologyName"),
				clique.TopologyConstraint.TopologyName,
				"all topologyConstraint.topologyName values within a PodCliqueSet must match in the current implementation"))
		}
	}

	for i, pcsg := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if pcsg.TopologyConstraint != nil {
			recordTopologyName(pcsg.TopologyConstraint.TopologyName)
		}
		if pcsg.TopologyConstraint != nil && len(topologyNames) > 1 {
			allErrs = append(allErrs, field.Invalid(
				field.NewPath("spec", "template", "podCliqueScalingGroups").Index(i).Child("topologyConstraint", "topologyName"),
				pcsg.TopologyConstraint.TopologyName,
				"all topologyConstraint.topologyName values within a PodCliqueSet must match in the current implementation"))
		}
	}

	if len(topologyNames) > 1 && pcs.Spec.Template.TopologyConstraint != nil {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec", "template", "topologyConstraint", "topologyName"),
			pcs.Spec.Template.TopologyConstraint.TopologyName,
			"all topologyConstraint.topologyName values within a PodCliqueSet must match in the current implementation"))
	}

	return allErrs
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
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(newClique.ResourceSharing, oldIndexCliqueTuple.B.ResourceSharing, fldPath.Index(newCliqueIndex).Child("resourceSharing"))...)
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

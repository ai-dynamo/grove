// Copyright 2026 The Grove Authors.
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

package mnnvl

import (
	"fmt"
	"slices"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	kubeutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"k8s.io/apimachinery/pkg/util/sets"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

const mnnvlNotEnabledMsgFormat = "MNNVL is not enabled in the operator configuration. Either enable MNNVL globally or remove the %s annotation"

// ValidatePCSOnCreate validates MNNVL annotations and generated resource names
// on a PodCliqueSet during creation.
func ValidatePCSOnCreate(pcs *grovecorev1alpha1.PodCliqueSet, autoMNNVLEnabled bool) field.ErrorList {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, validateMetadataOnCreate(pcs, autoMNNVLEnabled)...)
	allErrs = append(allErrs, validateSpecOnCreate(pcs, autoMNNVLEnabled)...)
	if autoMNNVLEnabled {
		allErrs = append(allErrs, validateGeneratedComputeDomainNames(pcs, field.NewPath("metadata", "name"))...)
	}
	return allErrs
}

// ValidatePCSOnUpdate validates MNNVL annotation immutability and generated
// resource names on a PodCliqueSet during update.
func ValidatePCSOnUpdate(oldPCS, newPCS *grovecorev1alpha1.PodCliqueSet, autoMNNVLEnabled bool) field.ErrorList {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, validateMetadataOnUpdate(oldPCS, newPCS)...)
	allErrs = append(allErrs, validateSpecOnUpdate(oldPCS, newPCS)...)
	if autoMNNVLEnabled {
		allErrs = append(allErrs, validateGeneratedComputeDomainNamesOnUpdate(oldPCS, newPCS)...)
	}
	return allErrs
}

func validateMetadataOnCreate(pcs *grovecorev1alpha1.PodCliqueSet, autoMNNVLEnabled bool) field.ErrorList {
	return validateMNNVLAnnotationsOnCreate(pcs.Annotations, autoMNNVLEnabled, field.NewPath("metadata", "annotations"))
}

func validateSpecOnCreate(pcs *grovecorev1alpha1.PodCliqueSet, autoMNNVLEnabled bool) field.ErrorList {
	return validatePodCliqueSetTemplateSpecOnCreate(&pcs.Spec.Template, autoMNNVLEnabled, field.NewPath("spec", "template"))
}

func validateMetadataOnUpdate(oldPCS, newPCS *grovecorev1alpha1.PodCliqueSet) field.ErrorList {
	return validateMNNVLAnnotationsImmutability(oldPCS.Annotations, newPCS.Annotations, field.NewPath("metadata", "annotations"))
}

func validateSpecOnUpdate(oldPCS, newPCS *grovecorev1alpha1.PodCliqueSet) field.ErrorList {
	return validatePodCliqueSetTemplateSpecOnUpdate(&oldPCS.Spec.Template, &newPCS.Spec.Template, field.NewPath("spec", "template"))
}

// validateMNNVLAnnotationsOnCreate validates the mnnvl-group annotation on a single
// annotation map: value correctness and feature enablement.
func validateMNNVLAnnotationsOnCreate(annotations map[string]string, autoMNNVLEnabled bool, basePath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	if value, exists := annotations[AnnotationMNNVLGroup]; exists {
		path := basePath.Child(AnnotationMNNVLGroup)
		if err := ValidateMNNVLGroupName(value); err != nil {
			allErrs = append(allErrs, field.Invalid(path, value, err.Error()))
		}
		// Opt-out ("none") is always allowed, even when the feature is disabled.
		if !autoMNNVLEnabled && value != AnnotationMNNVLGroupOptOut {
			allErrs = append(allErrs, field.Invalid(path, value,
				fmt.Sprintf(mnnvlNotEnabledMsgFormat, AnnotationMNNVLGroup)))
		}
	}

	return allErrs
}

func validatePodCliqueSetTemplateSpecOnCreate(templateSpec *grovecorev1alpha1.PodCliqueSetTemplateSpec, autoMNNVLEnabled bool, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	for i, clique := range templateSpec.Cliques {
		if clique == nil {
			continue
		}
		allErrs = append(allErrs, validatePodCliqueTemplateSpecOnCreate(clique, autoMNNVLEnabled, fldPath.Child("cliques").Index(i))...)
	}
	for i := range templateSpec.PodCliqueScalingGroupConfigs {
		pcsgConfig := &templateSpec.PodCliqueScalingGroupConfigs[i]
		allErrs = append(allErrs, validateMNNVLAnnotationsOnCreate(
			pcsgConfig.Annotations, autoMNNVLEnabled,
			fldPath.Child("podCliqueScalingGroups").Index(i).Child("annotations"),
		)...)
	}
	return allErrs
}

func validatePodCliqueTemplateSpecOnCreate(clique *grovecorev1alpha1.PodCliqueTemplateSpec, autoMNNVLEnabled bool, fldPath *field.Path) field.ErrorList {
	return validateMNNVLAnnotationsOnCreate(clique.Annotations, autoMNNVLEnabled, fldPath.Child("annotations"))
}

// validatePodCliqueSetTemplateSpecOnUpdate checks MNNVL annotation immutability for each clique
// template. Old and new specs are matched by clique name, not slice index: the default
// CliqueStartupTypeAnyOrder allows reordering cliques, and PCS validation already pairs by name.
// Adding, removing, or renaming cliques is not allowed on update; that is enforced by the
// PodCliqueSet validating admission path (validatePodCliqueUpdate), which runs after this
// package’s ValidatePCSOnUpdate. A new clique name with no counterpart in oldTemplate is
// therefore skipped here. Field paths use the index in newTemplate so errors point at the
// object being admitted.
func validatePodCliqueSetTemplateSpecOnUpdate(oldTemplate, newTemplate *grovecorev1alpha1.PodCliqueSetTemplateSpec, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	// Validate cliques
	oldByName := make(map[string]*grovecorev1alpha1.PodCliqueTemplateSpec, len(oldTemplate.Cliques))
	for _, clique := range oldTemplate.Cliques {
		if clique == nil {
			continue
		}
		oldByName[clique.Name] = clique
	}
	for i, newClique := range newTemplate.Cliques {
		if newClique == nil {
			continue
		}
		oldClique, ok := oldByName[newClique.Name]
		if !ok {
			continue
		}
		allErrs = append(allErrs, validatePodCliqueTemplateSpecOnUpdate(oldClique, newClique, fldPath.Child("cliques").Index(i))...)
	}

	// Validate PCSG Configs
	oldPCSGByName := make(map[string]*grovecorev1alpha1.PodCliqueScalingGroupConfig, len(oldTemplate.PodCliqueScalingGroupConfigs))
	for i := range oldTemplate.PodCliqueScalingGroupConfigs {
		oldPCSGByName[oldTemplate.PodCliqueScalingGroupConfigs[i].Name] = &oldTemplate.PodCliqueScalingGroupConfigs[i]
	}
	for i := range newTemplate.PodCliqueScalingGroupConfigs {
		newConfig := &newTemplate.PodCliqueScalingGroupConfigs[i]
		oldConfig, ok := oldPCSGByName[newConfig.Name]
		if !ok {
			continue
		}
		allErrs = append(allErrs, validateMNNVLAnnotationsImmutability(
			oldConfig.Annotations, newConfig.Annotations,
			fldPath.Child("podCliqueScalingGroups").Index(i).Child("annotations"),
		)...)
	}

	return allErrs
}

func validatePodCliqueTemplateSpecOnUpdate(oldClique, newClique *grovecorev1alpha1.PodCliqueTemplateSpec, fldPath *field.Path) field.ErrorList {
	return validateMNNVLAnnotationsImmutability(oldClique.Annotations, newClique.Annotations, fldPath.Child("annotations"))
}

var mnnvlImmutableKeys = []string{AnnotationMNNVLGroup}

func validateMNNVLAnnotationsImmutability(oldAnnotations, newAnnotations map[string]string, basePath *field.Path) field.ErrorList {
	return kubeutils.ValidateAnnotationsImmutability(oldAnnotations, newAnnotations, mnnvlImmutableKeys, basePath)
}

type generatedComputeDomainNameCheck struct {
	groupName        string
	generatedName    string
	validationErrors []string
}

func validateGeneratedComputeDomainNames(pcs *grovecorev1alpha1.PodCliqueSet, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	for _, check := range buildGeneratedComputeDomainNameChecks(pcs) {
		if len(check.validationErrors) > 0 {
			allErrs = append(allErrs, generatedComputeDomainNameError(check, fldPath, pcs.Name))
		}
	}
	return allErrs
}

func validateGeneratedComputeDomainNamesOnUpdate(
	oldPCS, newPCS *grovecorev1alpha1.PodCliqueSet,
) field.ErrorList {
	oldInvalidGroups := invalidGeneratedComputeDomainGroups(oldPCS)
	oldEffectiveGroups := EffectiveMNNVLGroupNames(oldPCS)

	var allErrs field.ErrorList
	for _, check := range buildGeneratedComputeDomainNameChecks(newPCS) {
		if len(check.validationErrors) == 0 {
			continue
		}
		if oldInvalidGroups.Has(check.groupName) && newPCS.Spec.Replicas <= oldPCS.Spec.Replicas {
			continue
		}

		fldPath := field.NewPath("metadata", "name")
		badValue := any(newPCS.Name)
		if oldEffectiveGroups.Has(check.groupName) && newPCS.Spec.Replicas > oldPCS.Spec.Replicas {
			fldPath = field.NewPath("spec", "replicas")
			badValue = newPCS.Spec.Replicas
		}
		allErrs = append(allErrs, generatedComputeDomainNameError(check, fldPath, badValue))
	}
	return allErrs
}

func buildGeneratedComputeDomainNameChecks(pcs *grovecorev1alpha1.PodCliqueSet) []generatedComputeDomainNameCheck {
	groupNames := EffectiveMNNVLGroupNames(pcs).UnsortedList()
	slices.Sort(groupNames)

	maxReplicaIndex := 0
	if pcs.Spec.Replicas > 1 {
		maxReplicaIndex = int(pcs.Spec.Replicas - 1)
	}

	checks := make([]generatedComputeDomainNameCheck, 0, len(groupNames))
	for _, groupName := range groupNames {
		generatedName := GenerateComputeDomainName(
			apicommon.ResourceNameReplica{Name: pcs.Name, Replica: maxReplicaIndex},
			groupName,
		)
		checks = append(checks, generatedComputeDomainNameCheck{
			groupName:        groupName,
			generatedName:    generatedName,
			validationErrors: k8svalidation.IsValidLabelValue(generatedName),
		})
	}
	return checks
}

func invalidGeneratedComputeDomainGroups(pcs *grovecorev1alpha1.PodCliqueSet) sets.Set[string] {
	groups := sets.New[string]()
	for _, check := range buildGeneratedComputeDomainNameChecks(pcs) {
		if len(check.validationErrors) > 0 {
			groups.Insert(check.groupName)
		}
	}
	return groups
}

func generatedComputeDomainNameError(
	check generatedComputeDomainNameCheck,
	fldPath *field.Path,
	badValue any,
) *field.Error {
	return field.Invalid(
		fldPath,
		badValue,
		fmt.Sprintf(
			"generated ComputeDomain name %q for MNNVL group %q must be a valid label value because it is used as app.kubernetes.io/name: %s",
			check.generatedName,
			check.groupName,
			strings.Join(check.validationErrors, "; "),
		),
	)
}

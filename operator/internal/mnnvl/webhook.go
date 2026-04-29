// /*
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
// */

package mnnvl

import (
	"fmt"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

const mnnvlNotEnabledMsgFormat = "MNNVL is not enabled in the operator configuration. Either enable MNNVL globally or remove the %s annotation"

// ValidatePCSOnCreate validates all MNNVL annotations on a PodCliqueSet during creation:
// PCS-level metadata and each PodCliqueTemplateSpec in the spec.
func ValidatePCSOnCreate(pcs *grovecorev1alpha1.PodCliqueSet, autoMNNVLEnabled bool) field.ErrorList {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, validateMetadataOnCreate(pcs, autoMNNVLEnabled)...)
	allErrs = append(allErrs, validateSpecOnCreate(pcs, autoMNNVLEnabled)...)
	return allErrs
}

// ValidatePCSOnUpdate validates MNNVL annotation immutability on a PodCliqueSet during update:
// PCS-level metadata and each PodCliqueTemplateSpec in the spec.
func ValidatePCSOnUpdate(oldPCS, newPCS *grovecorev1alpha1.PodCliqueSet) field.ErrorList {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, validateMetadataOnUpdate(oldPCS, newPCS)...)
	allErrs = append(allErrs, validateSpecOnUpdate(oldPCS, newPCS)...)
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

// validateMNNVLAnnotationsOnCreate validates both MNNVL annotations on a single
// annotation map: value correctness, feature enablement, and conflict detection.
func validateMNNVLAnnotationsOnCreate(annotations map[string]string, autoMNNVLEnabled bool, basePath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	if value, exists := annotations[AnnotationAutoMNNVL]; exists {
		path := basePath.Child(AnnotationAutoMNNVL)
		if value != AnnotationAutoMNNVLEnabled && value != AnnotationAutoMNNVLDisabled {
			allErrs = append(allErrs, field.Invalid(path, value,
				fmt.Sprintf("must be %q or %q", AnnotationAutoMNNVLEnabled, AnnotationAutoMNNVLDisabled)))
		}
		if value == AnnotationAutoMNNVLEnabled && !autoMNNVLEnabled {
			allErrs = append(allErrs, field.Invalid(path, value,
				fmt.Sprintf(mnnvlNotEnabledMsgFormat, AnnotationAutoMNNVL)))
		}
	}

	if value, exists := annotations[AnnotationMNNVLGroup]; exists {
		path := basePath.Child(AnnotationMNNVLGroup)
		if err := ValidateMNNVLGroupName(value); err != nil {
			allErrs = append(allErrs, field.Invalid(path, value, err.Error()))
		}
		if !autoMNNVLEnabled {
			allErrs = append(allErrs, field.Invalid(path, value,
				fmt.Sprintf(mnnvlNotEnabledMsgFormat, AnnotationMNNVLGroup)))
		}
	}

	if err := DetectMNNVLConflict(annotations); err != nil {
		allErrs = append(allErrs, field.Forbidden(basePath, err.Error()))
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

// validateMNNVLAnnotationsImmutability checks both MNNVL annotations are
// unchanged between old and new annotation maps.
func validateMNNVLAnnotationsImmutability(oldAnnotations, newAnnotations map[string]string, basePath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	for _, key := range []string{AnnotationAutoMNNVL, AnnotationMNNVLGroup} {
		path := basePath.Child(key)
		oldValue, oldExists := oldAnnotations[key]
		newValue, newExists := newAnnotations[key]
		allErrs = append(allErrs, validateAnnotationNotAdded(oldExists, newExists, key, path)...)
		allErrs = append(allErrs, validateAnnotationNotRemoved(oldExists, newExists, key, path)...)
		allErrs = append(allErrs, validateAnnotationNotChanged(oldValue, newValue, oldExists, newExists, key, path)...)
	}
	return allErrs
}

func validateAnnotationNotAdded(oldExists, newExists bool, annotationKey string, path *field.Path) field.ErrorList {
	if !oldExists && newExists {
		return field.ErrorList{
			field.Forbidden(path, fmt.Sprintf("annotation %s cannot be added after PodCliqueSet creation", annotationKey)),
		}
	}
	return nil
}

func validateAnnotationNotRemoved(oldExists, newExists bool, annotationKey string, path *field.Path) field.ErrorList {
	if oldExists && !newExists {
		return field.ErrorList{
			field.Forbidden(path, fmt.Sprintf("annotation %s cannot be removed after PodCliqueSet creation", annotationKey)),
		}
	}
	return nil
}

func validateAnnotationNotChanged(oldValue, newValue string, oldExists, newExists bool, annotationKey string, path *field.Path) field.ErrorList {
	if newExists && oldExists && oldValue != newValue {
		return field.ErrorList{
			field.Invalid(path, newValue, fmt.Sprintf("annotation %s is immutable and cannot be changed from %q to %q",
				annotationKey, oldValue, newValue)),
		}
	}
	return nil
}

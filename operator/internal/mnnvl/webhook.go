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

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// MutateAutoMNNVL adds the grove.io/auto-mnnvl annotation to a PodCliqueSet
// if all conditions are met:
// 1. Annotation does not already exist
// 2. MNNVL feature is enabled globally (autoMNNVLEnabled)
// 3. PCS has at least one container requesting GPU
func MutateAutoMNNVL(logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, autoMNNVLEnabled bool) {
	// If feature is disabled, don't add annotation
	if !autoMNNVLEnabled {
		return
	}

	// If annotation already exists (user explicitly set it), don't override
	if pcs.Annotations != nil {
		if value, exists := pcs.Annotations[AnnotationAutoMNNVL]; exists {
			logger.V(1).Info("Annotation already exists, skipping auto-mnnvl mutation",
				"annotation", AnnotationAutoMNNVL, "value", value)
			return
		}
	}

	// Check if PCS has GPU requirements
	if !hasGPURequirement(pcs) {
		logger.V(1).Info("PCS does not have GPU requirements, skipping auto-mnnvl mutation")
		return
	}

	// All conditions met - add the annotation
	if pcs.Annotations == nil {
		pcs.Annotations = make(map[string]string)
	}
	pcs.Annotations[AnnotationAutoMNNVL] = AnnotationAutoMNNVLEnabled

	logger.Info("Added auto-mnnvl annotation",
		"namespace", pcs.Namespace,
		"name", pcs.Name,
		"annotation", AnnotationAutoMNNVL,
		"value", AnnotationAutoMNNVLEnabled)
}

// ValidateAutoMNNVLOnCreate validates the MNNVL annotation on PCS creation.
// Returns field errors if the annotation is set to "enabled" but the MNNVL feature is disabled.
// This prevents users from explicitly requesting MNNVL when the cluster doesn't support it.
func ValidateAutoMNNVLOnCreate(pcs *grovecorev1alpha1.PodCliqueSet, autoMNNVLEnabled bool) field.ErrorList {
	value, exists := pcs.Annotations[AnnotationAutoMNNVL]
	if !exists {
		return nil
	}

	// If annotation is "enabled" but feature is disabled, reject
	if value == AnnotationAutoMNNVLEnabled && !autoMNNVLEnabled {
		return field.ErrorList{
			field.Invalid(
				field.NewPath("metadata", "annotations", AnnotationAutoMNNVL),
				value,
				fmt.Sprintf("MNNVL is not enabled in the operator configuration. "+
					"Either enable MNNVL globally or remove the %s annotation", AnnotationAutoMNNVL),
			),
		}
	}

	return nil
}

// ValidateAutoMNNVLOnUpdate ensures the grove.io/auto-mnnvl annotation is immutable.
// Returns field errors if the annotation was added, removed, or its value was changed.
func ValidateAutoMNNVLOnUpdate(oldPCS, newPCS *grovecorev1alpha1.PodCliqueSet) field.ErrorList {
	oldValue, oldExists := getAnnotationValue(oldPCS, AnnotationAutoMNNVL)
	newValue, newExists := getAnnotationValue(newPCS, AnnotationAutoMNNVL)

	annotationPath := field.NewPath("metadata", "annotations", AnnotationAutoMNNVL)

	// Check if annotation was added
	if !oldExists && newExists {
		return field.ErrorList{
			field.Forbidden(
				annotationPath,
				fmt.Sprintf("annotation %s cannot be added after PodCliqueSet creation", AnnotationAutoMNNVL),
			),
		}
	}

	// Check if annotation was removed
	if oldExists && !newExists {
		return field.ErrorList{
			field.Forbidden(
				annotationPath,
				fmt.Sprintf("annotation %s cannot be removed after PodCliqueSet creation", AnnotationAutoMNNVL),
			),
		}
	}

	// Check if annotation value was changed
	if newExists && oldValue != newValue {
		return field.ErrorList{
			field.Invalid(
				annotationPath,
				newValue,
				fmt.Sprintf("annotation %s is immutable and cannot be changed from %q to %q",
					AnnotationAutoMNNVL, oldValue, newValue),
			),
		}
	}

	return nil
}

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

package kubernetes

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

// ValidateAnnotationsImmutability checks that the given annotation keys are
// unchanged between old and new annotation maps. For each key it rejects
// additions, removals, and value changes.
func ValidateAnnotationsImmutability(oldAnnotations, newAnnotations map[string]string, keys []string, basePath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	for _, key := range keys {
		path := basePath.Child(key)
		oldValue, oldExists := oldAnnotations[key]
		newValue, newExists := newAnnotations[key]
		if !oldExists && newExists {
			allErrs = append(allErrs, field.Forbidden(path,
				fmt.Sprintf("annotation %s cannot be added after creation", key)))
		}
		if oldExists && !newExists {
			allErrs = append(allErrs, field.Forbidden(path,
				fmt.Sprintf("annotation %s cannot be removed after creation", key)))
		}
		if oldExists && newExists && oldValue != newValue {
			allErrs = append(allErrs, field.Invalid(path, newValue,
				fmt.Sprintf("annotation %s is immutable and cannot be changed from %q to %q", key, oldValue, newValue)))
		}
	}
	return allErrs
}

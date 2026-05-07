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

package validation

import (
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

func validateClusterTopology(
	ct *grovecorev1alpha1.ClusterTopology,
	enabledBackends map[string]struct{},
	topologyAwareBackends map[string]struct{},
) field.ErrorList {
	allErrs := validateClusterTopologyLevels(ct.Spec.Levels, field.NewPath("spec", "levels"))
	allErrs = append(allErrs,
		validateSchedulerTopologyReferences(
			ct.Spec.SchedulerTopologyReferences,
			enabledBackends,
			topologyAwareBackends,
			field.NewPath("spec", "schedulerTopologyReferences"),
		)...,
	)
	return allErrs
}

// validateClusterTopologyLevels validates that domain and key values are unique across levels.
func validateClusterTopologyLevels(levels []grovecorev1alpha1.TopologyLevel, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	seenDomains := make(map[grovecorev1alpha1.TopologyDomain]bool)
	seenKeys := make(map[string]bool)

	for i, level := range levels {
		levelPath := fldPath.Index(i)

		if _, exists := seenDomains[level.Domain]; exists {
			allErrs = append(allErrs, field.Duplicate(levelPath.Child("domain"), level.Domain))
		}
		seenDomains[level.Domain] = true

		if _, exists := seenKeys[level.Key]; exists {
			allErrs = append(allErrs, field.Duplicate(levelPath.Child("key"), level.Key))
		}
		seenKeys[level.Key] = true
	}

	return allErrs
}

// validateSchedulerTopologyReferences validates that each scheduler backend is referenced at most once
// and that each referenced backend is enabled and topology-aware in the running Grove configuration.
func validateSchedulerTopologyReferences(
	refs []grovecorev1alpha1.SchedulerTopologyReference,
	enabledBackends map[string]struct{},
	topologyAwareBackends map[string]struct{},
	fldPath *field.Path,
) field.ErrorList {
	var allErrs field.ErrorList
	seenSchedulers := make(map[string]bool, len(refs))

	for i, ref := range refs {
		refPath := fldPath.Index(i)
		schedulerNamePath := refPath.Child("schedulerName")

		if seenSchedulers[ref.SchedulerName] {
			allErrs = append(allErrs, field.Duplicate(schedulerNamePath, ref.SchedulerName))
		}
		seenSchedulers[ref.SchedulerName] = true

		if _, ok := enabledBackends[ref.SchedulerName]; !ok {
			allErrs = append(allErrs, field.Invalid(
				schedulerNamePath,
				ref.SchedulerName,
				"scheduler backend is not enabled in Grove",
			))
			continue
		}
		if _, ok := topologyAwareBackends[ref.SchedulerName]; !ok {
			allErrs = append(allErrs, field.Invalid(
				schedulerNamePath,
				ref.SchedulerName,
				"scheduler backend does not implement topology-aware scheduling",
			))
		}
	}

	return allErrs
}

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

package validation

import (
	"fmt"
	"strings"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// builtInStartupConditions are the Pod conditions that the scheduler and the kubelet set, so a dependency
// can always wait on them.
var builtInStartupConditions = sets.New(
	corev1.PodScheduled,
	corev1.PodReadyToStartContainers,
	corev1.PodInitialized,
	corev1.ContainersReady,
	corev1.PodReady,
)

// validateStartsAfter validates the startsAfter dependencies of the clique named name. Each name must be
// non-empty, unique and not the clique itself. Each conditionType must be a qualified name and one of the
// built-in Pod conditions, or a custom condition that the parent clique declares in its pod readinessGates.
// DisruptionTarget is rejected because the control plane sets it just before a pod is evicted. The readiness
// gate check runs on every write, so an update that drops a gate a dependent still waits on is rejected.
func (v *pcsValidator) validateStartsAfter(name string, startsAfter []grovecorev1beta1.StartupDependency, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	for i, dependency := range startsAfter {
		dependencyPath := fldPath.Index(i)
		if utils.IsEmptyStringType(dependency.Name) {
			allErrs = append(allErrs, field.Required(dependencyPath.Child("name"), "clique dependency must not be empty"))
		}
		if dependency.Name == name {
			allErrs = append(allErrs, field.Invalid(dependencyPath.Child("name"), dependency.Name, "clique dependency cannot refer to itself"))
		}
		allErrs = append(allErrs, v.validateStartupConditionType(dependency, dependencyPath.Child("conditionType"))...)
	}
	names := lo.Map(startsAfter, func(dependency grovecorev1beta1.StartupDependency, _ int) string { return dependency.Name })
	allErrs = append(allErrs, sliceMustHaveUniqueElements(names, fldPath.Child("name"))...)
	return allErrs
}

func (v *pcsValidator) validateStartupConditionType(dependency grovecorev1beta1.StartupDependency, fldPath *field.Path) field.ErrorList {
	conditionType := corev1.PodConditionType(dependency.ConditionType)
	if conditionType == "" {
		// The schema defaults conditionType to Ready.
		return nil
	}
	if errs := k8svalidation.IsQualifiedName(dependency.ConditionType); len(errs) > 0 {
		return field.ErrorList{field.Invalid(fldPath, dependency.ConditionType, strings.Join(errs, "; "))}
	}
	if conditionType == corev1.DisruptionTarget {
		return field.ErrorList{field.Invalid(fldPath, dependency.ConditionType,
			"DisruptionTarget is not allowed, because it is set when a pod is about to be evicted or preempted")}
	}
	if builtInStartupConditions.Has(conditionType) {
		return nil
	}
	parent := componentutils.FindPodCliqueTemplateSpecByName(v.pcs, dependency.Name)
	if parent == nil {
		// An unknown parent is reported by validateCliqueDependencies.
		return nil
	}
	declared := lo.ContainsBy(parent.Spec.PodSpec.ReadinessGates, func(gate corev1.PodReadinessGate) bool {
		return gate.ConditionType == conditionType
	})
	if !declared {
		return field.ErrorList{field.Invalid(fldPath, dependency.ConditionType,
			fmt.Sprintf("dependency %q waits on custom condition %q, which parent clique %q must declare in spec.podSpec.readinessGates", dependency.Name, dependency.ConditionType, dependency.Name))}
	}
	return nil
}

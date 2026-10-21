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
	"strconv"
	"strings"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
)

// minReplicaComponent is a clique template or PodCliqueScalingGroup config, viewed through the fields that
// carry its min-replica values.
type minReplicaComponent struct {
	init          *grovecorev1beta1.InitSpec
	termination   *grovecorev1beta1.TerminationSpec
	rollingUpdate *grovecorev1beta1.RollingUpdateConfiguration
	scaleConfig   *grovecorev1beta1.AutoScalingConfig
	// replicas is the template replica count, or nil when it is not set.
	replicas *int32
	// fldPath is the path of the field that holds init and termination: the clique template's spec, or the
	// PodCliqueScalingGroup config.
	fldPath *field.Path
	// scaleConfigPath is the path of the autoscaling configuration.
	scaleConfigPath *field.Path
	// rollingUpdatePath is the path of rollingUpdate.
	rollingUpdatePath *field.Path
	// ownsRollingUpdate is false for a PCSG-member clique template, whose update is governed by its
	// PodCliqueScalingGroup.
	ownsRollingUpdate bool
}

func cliqueTemplateComponent(cliqueTemplate *grovecorev1beta1.PodCliqueTemplateSpec, fldPath *field.Path, isPCSGMember bool) minReplicaComponent {
	return minReplicaComponent{
		init:              cliqueTemplate.Spec.Init,
		termination:       cliqueTemplate.Spec.Termination,
		rollingUpdate:     cliqueTemplate.RollingUpdate,
		scaleConfig:       cliqueTemplate.Spec.ScaleConfig,
		replicas:          ptr.To(cliqueTemplate.Spec.Replicas),
		fldPath:           fldPath.Child("spec"),
		scaleConfigPath:   fldPath.Child("spec", "autoScalingConfig"),
		rollingUpdatePath: fldPath.Child("rollingUpdate"),
		ownsRollingUpdate: !isPCSGMember,
	}
}

func scalingGroupConfigComponent(cfg *grovecorev1beta1.PodCliqueScalingGroupConfig, fldPath *field.Path) minReplicaComponent {
	return minReplicaComponent{
		init:              cfg.Init,
		termination:       cfg.Termination,
		rollingUpdate:     cfg.RollingUpdate,
		scaleConfig:       cfg.ScaleConfig,
		replicas:          cfg.Replicas,
		fldPath:           fldPath,
		scaleConfigPath:   fldPath.Child("scaleConfig"),
		rollingUpdatePath: fldPath.Child("rollingUpdate"),
		ownsRollingUpdate: true,
	}
}

// validateMinReplicas checks the ordering 1 <= availability.minReplicas <= init.minReplicas <= replicas, the
// autoscaling floor, and the rollingUpdate rules of a component. It runs on every write, so a change to one
// of these values must keep the others consistent in the same edit.
func (v *pcsValidator) validateMinReplicas(c minReplicaComponent) field.ErrorList {
	allErrs := field.ErrorList{}
	initPath := c.fldPath.Child("init", "minReplicas")
	availabilityPath := c.fldPath.Child("termination", "resource", "availability")

	// Ideally init.minReplicas is never nil here, the defaulting webhook always sets it.
	initMinReplicas := componentutils.PCLQInitMinReplicas(&grovecorev1beta1.PodCliqueSpec{Init: c.init, Termination: c.termination})
	if initMinReplicas <= 0 {
		allErrs = append(allErrs, field.Invalid(initPath, initMinReplicas, "must be greater than 0"))
	}
	if c.replicas != nil && initMinReplicas > *c.replicas {
		allErrs = append(allErrs, field.Invalid(initPath, initMinReplicas, fmt.Sprintf("must not be greater than replicas (%d)", *c.replicas)))
	}

	if availability := componentutils.Availability(c.termination); availability != nil {
		availabilityMinReplicas := ptr.Deref(availability.MinReplicas, 1)
		switch {
		case availabilityMinReplicas <= 0:
			allErrs = append(allErrs, field.Invalid(availabilityPath.Child("minReplicas"), availabilityMinReplicas, "must be greater than 0"))
		case availabilityMinReplicas > initMinReplicas:
			allErrs = append(allErrs, field.Invalid(availabilityPath.Child("minReplicas"), availabilityMinReplicas, fmt.Sprintf("must not be greater than init.minReplicas (%d)", initMinReplicas)))
		}
		if availability.BreachTolerance != nil && availability.BreachTolerance.Duration <= 0 {
			allErrs = append(allErrs, field.Invalid(availabilityPath.Child("breachTolerance"), availability.BreachTolerance.Duration.String(), "must be greater than 0"))
		}
	}

	if c.scaleConfig != nil {
		allErrs = append(allErrs, validateScaleConfig(c.scaleConfig, componentutils.ScaleFloor(c.termination), c.scaleConfigPath)...)
	}

	if c.ownsRollingUpdate {
		allErrs = append(allErrs, v.validateRollingUpdateConfiguration(c.rollingUpdate, initMinReplicas, c.replicas, c.rollingUpdatePath)...)
	} else if c.rollingUpdate != nil {
		allErrs = append(allErrs, field.Forbidden(c.rollingUpdatePath,
			"rollingUpdate must not be set on a PodClique that is a member of a PodCliqueScalingGroup. Set it on the PodCliqueScalingGroup instead"))
	}
	return allErrs
}

// validateRollingUpdateConfiguration checks a component's RollingUpdate against the active update
// strategy. OnDelete forbids it entirely. RollingRecreate forbids minReplicas. Under Coherent, minReplicas
// must be in [1, min(init.minReplicas, replicas)], and the resolved MaxUnavailable must not be less than
// minReplicas, since the MVU sub-step takes down minReplicas of the component at once. MaxUnavailable must
// be an integer of at least 1 or a percentage in [1%, 100%], and ProgressDeadline must be greater than 0.
// A nil MaxUnavailable is allowed since the field is optional and the consumer supplies an effective
// value.
//
// MaxUnavailable is deliberately not bounded above by replicas. The replicas supplied by the PCS
// template is a creation-time seed. The live PodClique / PodCliqueScalingGroup is scaled independently
// through its /scale subresource, so the template count goes stale after any scale. MaxUnavailable is a
// ceiling on rollout disruption, not a demand to disrupt that many. Runtime rollout planning already
// caps disruption to the replicas that actually remain.
func (v *pcsValidator) validateRollingUpdateConfiguration(rollingUpdate *grovecorev1beta1.RollingUpdateConfiguration, initMinReplicas int32, replicas *int32, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	strategy := v.updateStrategyType()
	if strategy == grovecorev1beta1.OnDeleteStrategy {
		if rollingUpdate != nil {
			allErrs = append(allErrs, field.Forbidden(fldPath, "rollingUpdate must not be set when the update strategy is OnDelete"))
		}
		return allErrs
	}
	if rollingUpdate == nil {
		return allErrs
	}

	var updateMinReplicas *int32
	if rollingUpdate.MinReplicas != nil {
		minReplicasPath := fldPath.Child("minReplicas")
		upperBound := initMinReplicas
		if replicas != nil {
			upperBound = min(upperBound, *replicas)
		}
		switch {
		case strategy != grovecorev1beta1.CoherentStrategy:
			allErrs = append(allErrs, field.Forbidden(minReplicasPath, fmt.Sprintf("must not be set when the update strategy is %s", strategy)))
		case *rollingUpdate.MinReplicas < 1:
			allErrs = append(allErrs, field.Invalid(minReplicasPath, *rollingUpdate.MinReplicas, "must be greater than 0"))
		case *rollingUpdate.MinReplicas > upperBound:
			allErrs = append(allErrs, field.Invalid(minReplicasPath, *rollingUpdate.MinReplicas, fmt.Sprintf("must not be greater than min(init.minReplicas, replicas) (%d)", upperBound)))
		default:
			updateMinReplicas = rollingUpdate.MinReplicas
		}
	}

	if rollingUpdate.MaxUnavailable != nil {
		allErrs = append(allErrs, validateMaxUnavailable(*rollingUpdate.MaxUnavailable, updateMinReplicas, replicas, fldPath.Child("maxUnavailable"))...)
	}
	if rollingUpdate.ProgressDeadline != nil && rollingUpdate.ProgressDeadline.Duration <= 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("progressDeadline"), rollingUpdate.ProgressDeadline.Duration.String(), "must be greater than 0"))
	}
	return allErrs
}

// validateMaxUnavailable checks that maxUnavailable is an integer of at least 1 or a percentage in
// [1%, 100%]. When updateMinReplicas is set (Coherent), the value resolved against the template replicas must
// not be less than it.
func validateMaxUnavailable(maxUnavailable intstr.IntOrString, updateMinReplicas *int32, replicas *int32, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	if maxUnavailable.Type == intstr.String {
		percent, ok := parsePercentage(maxUnavailable.StrVal)
		switch {
		case !ok:
			return append(allErrs, field.Invalid(fldPath, maxUnavailable.StrVal, "must be an integer or a percentage, such as '25%'"))
		case percent < 1 || percent > 100:
			return append(allErrs, field.Invalid(fldPath, maxUnavailable.StrVal, "must be a percentage between 1% and 100%"))
		}
	} else if maxUnavailable.IntVal < 1 {
		return append(allErrs, field.Invalid(fldPath, maxUnavailable.IntVal, "must be greater than 0"))
	}
	if updateMinReplicas == nil {
		return allErrs
	}
	resolved, err := intstr.GetScaledValueFromIntOrPercent(&maxUnavailable, int(ptr.Deref(replicas, 1)), false)
	if err != nil {
		return append(allErrs, field.Invalid(fldPath, maxUnavailable.String(), err.Error()))
	}
	resolved = max(resolved, 1)
	if int32(resolved) < *updateMinReplicas {
		allErrs = append(allErrs, field.Invalid(fldPath, maxUnavailable.String(),
			fmt.Sprintf("resolves to %d, which must not be less than rollingUpdate.minReplicas (%d) under the Coherent update strategy", resolved, *updateMinReplicas)))
	}
	return allErrs
}

// parsePercentage parses a value of the form "<integer>%".
func parsePercentage(value string) (int, bool) {
	digits, found := strings.CutSuffix(value, "%")
	if !found {
		return 0, false
	}
	percent, err := strconv.Atoi(digits)
	return percent, err == nil
}

// validateMinReplicasUpdate checks the update rules of the min-replica fields of a component:
//   - adding or removing availability is rejected, because it switches gang termination on or off;
//   - rollingUpdate.minReplicas cannot change while a Coherent update is in progress;
//   - a request sent as v1alpha1 cannot change init.minReplicas, availability.minReplicas or
//     rollingUpdate.minReplicas, which keeps v1alpha1 minAvailable immutable.
//
// A strategy switch that adds or removes rollingUpdate.minReplicas does not count as a change of the value.
func (v *pcsValidator) validateMinReplicasUpdate(oldPCS *grovecorev1beta1.PodCliqueSet, newC, oldC minReplicaComponent) field.ErrorList {
	allErrs := field.ErrorList{}
	newAvailability := componentutils.Availability(newC.termination)
	oldAvailability := componentutils.Availability(oldC.termination)
	availabilityPath := newC.fldPath.Child("termination", "resource", "availability")
	if (newAvailability == nil) != (oldAvailability == nil) {
		allErrs = append(allErrs, field.Forbidden(availabilityPath, "adding or removing availability is not allowed, because it switches gang termination on or off"))
	}

	sameStrategy := componentutils.ResolveUpdateStrategyType(oldPCS) == v.updateStrategyType()
	updateMinReplicasChanged := sameStrategy && !equality.Semantic.DeepEqual(updateMinReplicasOf(newC.rollingUpdate), updateMinReplicasOf(oldC.rollingUpdate))
	if updateMinReplicasChanged && componentutils.IsCoherentUpdateInProgress(oldPCS) {
		allErrs = append(allErrs, field.Forbidden(newC.rollingUpdatePath.Child("minReplicas"), "must not change while a Coherent update is in progress"))
	}

	if v.alphaRequest {
		if !equality.Semantic.DeepEqual(newC.init, oldC.init) {
			allErrs = append(allErrs, field.Forbidden(newC.fldPath.Child("minAvailable"), "field is immutable for grove.io/v1alpha1 requests, use grove.io/v1beta1 to change init.minReplicas"))
		}
		if newAvailability != nil && oldAvailability != nil && !equality.Semantic.DeepEqual(newAvailability.MinReplicas, oldAvailability.MinReplicas) {
			allErrs = append(allErrs, field.Forbidden(availabilityPath.Child("minReplicas"), "field is immutable for grove.io/v1alpha1 requests, use grove.io/v1beta1 to change availability.minReplicas"))
		}
		if updateMinReplicasChanged {
			allErrs = append(allErrs, field.Forbidden(newC.rollingUpdatePath.Child("minReplicas"), "field is immutable for grove.io/v1alpha1 requests, use grove.io/v1beta1 to change rollingUpdate.minReplicas"))
		}
	}
	return allErrs
}

// validateUpdateStrategyUpdate rejects a change of updateStrategy.type away from Coherent while a Coherent
// update is in progress, because the change removes rollingUpdate.minReplicas.
func (v *pcsValidator) validateUpdateStrategyUpdate(oldPCS *grovecorev1beta1.PodCliqueSet, fldPath *field.Path) field.ErrorList {
	oldStrategy := componentutils.ResolveUpdateStrategyType(oldPCS)
	newStrategy := v.updateStrategyType()
	if oldStrategy == grovecorev1beta1.CoherentStrategy && newStrategy != oldStrategy && componentutils.IsCoherentUpdateInProgress(oldPCS) {
		return field.ErrorList{field.Forbidden(fldPath.Child("type"), "must not change away from Coherent while a Coherent update is in progress")}
	}
	return nil
}

func updateMinReplicasOf(rollingUpdate *grovecorev1beta1.RollingUpdateConfiguration) *int32 {
	if rollingUpdate == nil {
		return nil
	}
	return rollingUpdate.MinReplicas
}

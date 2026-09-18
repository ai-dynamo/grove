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

package component

import (
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"k8s.io/apimachinery/pkg/util/sets"
)

// DefaultRollingRecreateMaxUnavailable is the MaxUnavailable applied to a component under the
// RollingRecreate strategy when none is configured. It is shared by the defaulting webhook and by
// EffectiveMaxUnavailable so both agree on the value.
const DefaultRollingRecreateMaxUnavailable int32 = 1

// CoherentMaxUnavailableByComponent returns, keyed by in-scope component name, the effective
// MaxUnavailable for each component being rolled under the Coherent update strategy (the
// UpdateStrategyType value "Coherent"). It is consumed by the coherent step planner and the
// per-sub-step MaxUnavailable budget gate, which run only when the PodCliqueSet uses the Coherent
// update strategy.
func CoherentMaxUnavailableByComponent(pcs *grovecorev1alpha1.PodCliqueSet, inScopeComponentNames []string) map[string]int32 {
	inScopeComponents := sets.New(inScopeComponentNames...)
	maxUnavailableByComponent := make(map[string]int32, len(inScopeComponentNames))
	for _, cliqueTemplate := range pcs.Spec.Template.Cliques {
		if inScopeComponents.Has(cliqueTemplate.Name) && IsStandalonePCLQ(pcs, cliqueTemplate.Name) {
			maxUnavailableByComponent[cliqueTemplate.Name] = int32(EffectiveMaxUnavailable(cliqueTemplate.RollingUpdate, grovecorev1alpha1.CoherentStrategy, *cliqueTemplate.Spec.MinAvailable))
		}
	}
	for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if inScopeComponents.Has(pcsgConfig.Name) {
			maxUnavailableByComponent[pcsgConfig.Name] = int32(EffectiveMaxUnavailable(pcsgConfig.RollingUpdate, grovecorev1alpha1.CoherentStrategy, *pcsgConfig.MinAvailable))
		}
	}
	return maxUnavailableByComponent
}

// EffectiveMaxUnavailable returns the MaxUnavailable to use for a component under the given update
// strategy, tolerating a nil rollingUpdate or a nil MaxUnavailable by applying the strategy's
// default: MinAvailable for Coherent, since the MVU sub-step takes down MinAvailable at once, and 1
// for RollingRecreate. This mirrors the defaulting webhook so an in-flight object that predates the
// RollingUpdate fields resolves to the same value the webhook would have written.
//
// NOTE: the nil-tolerance exists only for PodCliqueSets created before the RollingUpdate fields
// existed, which reconcile without re-admission and carry a nil MaxUnavailable. Once every
// PodCliqueSet has been re-admitted and carries a MaxUnavailable populated by the defaulting webhook,
// this can be simplified to reading the field directly.
func EffectiveMaxUnavailable(rollingUpdate *grovecorev1alpha1.RollingUpdateConfiguration, updateStrategy grovecorev1alpha1.UpdateStrategyType, minAvailable int32) int {
	if rollingUpdate != nil && rollingUpdate.MaxUnavailable != nil {
		return int(*rollingUpdate.MaxUnavailable)
	}
	if updateStrategy == grovecorev1alpha1.CoherentStrategy {
		return int(minAvailable)
	}
	return int(DefaultRollingRecreateMaxUnavailable)
}

// CoherentMinAvailableByComponent returns the MinAvailable of each in-scope component under the
// Coherent update strategy (the UpdateStrategyType value "Coherent"), split by kind:
// standalonePCLQMinAvailable keys a standalone PodClique name to its MinAvailable pod count, and
// pcsgMinAvailable keys a PodCliqueScalingGroup name to its MinAvailable replica count. A component
// not named in inScopeComponentNames is excluded. It backs the coherent MVU template.
func CoherentMinAvailableByComponent(pcs *grovecorev1alpha1.PodCliqueSet, inScopeComponentNames []string) (standalonePCLQMinAvailable, pcsgMinAvailable map[string]int32) {
	inScopeComponents := sets.New(inScopeComponentNames...)
	standalonePCLQMinAvailable = make(map[string]int32)
	pcsgMinAvailable = make(map[string]int32)
	for _, cliqueTemplate := range pcs.Spec.Template.Cliques {
		if inScopeComponents.Has(cliqueTemplate.Name) && IsStandalonePCLQ(pcs, cliqueTemplate.Name) {
			standalonePCLQMinAvailable[cliqueTemplate.Name] = *cliqueTemplate.Spec.MinAvailable
		}
	}
	for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if inScopeComponents.Has(pcsgConfig.Name) {
			pcsgMinAvailable[pcsgConfig.Name] = *pcsgConfig.MinAvailable
		}
	}
	return
}

// ComputeAllowedBudget returns the number of units (Pods for a standalone PodClique, complete logical
// replicas for a PodCliqueScalingGroup) that may be disrupted this reconcile. It is the MaxUnavailable
// headroom (effectiveMaxUnavailable minus the currently unavailable units), floored at 0. MinAvailable
// is deliberately not a bound, so a PodClique whose MinAvailable equals its replica count can still roll
// (gang termination is suspended during an update, so the transient dip below MinAvailable is safe).
func ComputeAllowedBudget(desiredNumUnits, numReadyUnits, effectiveMaxUnavailable int) int {
	unavailable := desiredNumUnits - numReadyUnits
	return max(0, effectiveMaxUnavailable-unavailable)
}

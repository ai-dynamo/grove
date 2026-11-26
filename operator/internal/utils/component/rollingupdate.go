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
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	"k8s.io/apimachinery/pkg/util/sets"
)

// DefaultRollingRecreateMaxUnavailable is the runtime MaxUnavailable default for a component
// under the RollingRecreate strategy when no value is configured.
const DefaultRollingRecreateMaxUnavailable int32 = 1

// CoherentMaxUnavailableByComponent returns, keyed by in-scope component name, the effective
// MaxUnavailable for each component being rolled under the Coherent update strategy (the
// UpdateStrategyType value "Coherent"). It is consumed by the coherent step planner and the
// per-sub-step MaxUnavailable budget gate, which run only when the PodCliqueSet uses the Coherent
// update strategy.
func CoherentMaxUnavailableByComponent(pcs *grovecorev1beta1.PodCliqueSet, inScopeComponentNames []string) map[string]int32 {
	inScopeComponents := sets.New(inScopeComponentNames...)
	maxUnavailableByComponent := make(map[string]int32, len(inScopeComponentNames))
	for _, cliqueTemplate := range pcs.Spec.Template.Cliques {
		if inScopeComponents.Has(cliqueTemplate.Name) && IsStandalonePCLQTemplate(pcs, cliqueTemplate.Name) {
			maxUnavailableByComponent[cliqueTemplate.Name] = int32(EffectiveMaxUnavailable(cliqueTemplate.RollingUpdate, grovecorev1beta1.CoherentStrategy, *cliqueTemplate.Spec.MinAvailable))
		}
	}
	for _, pcsgConfig := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		if inScopeComponents.Has(pcsgConfig.Name) {
			maxUnavailableByComponent[pcsgConfig.Name] = int32(EffectiveMaxUnavailable(pcsgConfig.RollingUpdate, grovecorev1beta1.CoherentStrategy, *pcsgConfig.MinAvailable))
		}
	}
	return maxUnavailableByComponent
}

// EffectiveMaxUnavailable returns the effective MaxUnavailable for a component under the given
// update strategy. A non-nil MaxUnavailable takes precedence, including defaults stored by older
// webhooks. If rollingUpdate or MaxUnavailable is nil, it returns minAvailable for Coherent and
// DefaultRollingRecreateMaxUnavailable otherwise.
//
// The defaulting webhook preserves nil for omitted MaxUnavailable values so the Coherent default
// follows changes to MinAvailable.
func EffectiveMaxUnavailable(rollingUpdate *grovecorev1beta1.RollingUpdateConfiguration, updateStrategy grovecorev1beta1.UpdateStrategyType, minAvailable int32) int {
	if rollingUpdate != nil && rollingUpdate.MaxUnavailable != nil {
		return int(*rollingUpdate.MaxUnavailable)
	}
	if updateStrategy == grovecorev1beta1.CoherentStrategy {
		return int(minAvailable)
	}
	return int(DefaultRollingRecreateMaxUnavailable)
}

// CoherentMinAvailableByComponent returns the MinAvailable of each in-scope component under the
// Coherent update strategy (the UpdateStrategyType value "Coherent"), split by kind:
// standalonePCLQMinAvailable keys a standalone PodClique name to its MinAvailable pod count, and
// pcsgMinAvailable keys a PodCliqueScalingGroup name to its MinAvailable replica count. A component
// not named in inScopeComponentNames is excluded. It backs the coherent MVU template.
func CoherentMinAvailableByComponent(pcs *grovecorev1beta1.PodCliqueSet, inScopeComponentNames []string) (standalonePCLQMinAvailable, pcsgMinAvailable map[string]int32) {
	inScopeComponents := sets.New(inScopeComponentNames...)
	standalonePCLQMinAvailable = make(map[string]int32)
	pcsgMinAvailable = make(map[string]int32)
	for _, cliqueTemplate := range pcs.Spec.Template.Cliques {
		if inScopeComponents.Has(cliqueTemplate.Name) && IsStandalonePCLQTemplate(pcs, cliqueTemplate.Name) {
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
// headroom (effectiveMaxUnavailable minus the currently unavailable units), clamped at 0. MinAvailable
// is deliberately not a bound, so a PodClique whose MinAvailable equals its replica count can still roll
// (gang termination is suspended during an update, so the transient dip below MinAvailable is safe).
func ComputeAllowedBudget(desiredNumUnits, numReadyUnits, effectiveMaxUnavailable int) int {
	unavailable := desiredNumUnits - numReadyUnits
	return max(0, effectiveMaxUnavailable-unavailable)
}

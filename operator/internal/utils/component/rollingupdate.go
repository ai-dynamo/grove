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
)

// DefaultRollingRecreateMaxUnavailable is the MaxUnavailable applied to a component under the
// RollingRecreate strategy when none is configured. It is shared by the defaulting webhook and by
// EffectiveMaxUnavailable so both agree on the value.
const DefaultRollingRecreateMaxUnavailable int32 = 1

// EffectiveMaxUnavailable returns the MaxUnavailable to use while rolling a component under
// RollingRecreate, tolerating a nil rollingUpdate or a nil MaxUnavailable by applying
// DefaultRollingRecreateMaxUnavailable.
//
// NOTE: this exists only for PodCliqueSets created before the RollingUpdate fields existed, which
// reconcile without re-admission and carry a nil MaxUnavailable. Once every PodCliqueSet has been
// re-admitted and carries a MaxUnavailable populated by the defaulting webhook, this accessor is no
// longer required and should be removed in favor of reading the field directly.
func EffectiveMaxUnavailable(rollingUpdate *grovecorev1alpha1.RollingUpdateConfiguration) int {
	if rollingUpdate != nil && rollingUpdate.MaxUnavailable != nil {
		return int(*rollingUpdate.MaxUnavailable)
	}
	return int(DefaultRollingRecreateMaxUnavailable)
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

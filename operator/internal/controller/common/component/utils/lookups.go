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

package utils

import (
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
)

// Reusable name-keyed views over the common Grove resource slices. Each function builds a
// fresh map (or set) — call once per sync context and pass the result around. Callers that
// only need membership checks should prefer the *NameSet variants.

// PodCliqueByName builds a name-keyed map for O(1) PodClique lookups.
func PodCliqueByName(pclqs []grovecorev1alpha1.PodClique) map[string]grovecorev1alpha1.PodClique {
	return MapBy(pclqs, func(pclq grovecorev1alpha1.PodClique) (string, grovecorev1alpha1.PodClique) {
		return pclq.Name, pclq
	})
}

// PodCliqueNameSet builds a set of PodClique names for O(1) membership checks.
func PodCliqueNameSet(pclqs []grovecorev1alpha1.PodClique) Set[string] {
	return NewSetBy(pclqs, func(pclq grovecorev1alpha1.PodClique) string {
		return pclq.Name
	})
}

// PCSGByName builds a name-keyed map for O(1) PodCliqueScalingGroup lookups.
func PCSGByName(pcsgs []grovecorev1alpha1.PodCliqueScalingGroup) map[string]grovecorev1alpha1.PodCliqueScalingGroup {
	return MapBy(pcsgs, func(pcsg grovecorev1alpha1.PodCliqueScalingGroup) (string, grovecorev1alpha1.PodCliqueScalingGroup) {
		return pcsg.Name, pcsg
	})
}

// PodGangByName builds a name-keyed map for O(1) PodGang lookups.
func PodGangByName(podGangs []groveschedulerv1alpha1.PodGang) map[string]groveschedulerv1alpha1.PodGang {
	return MapBy(podGangs, func(podGang groveschedulerv1alpha1.PodGang) (string, groveschedulerv1alpha1.PodGang) {
		return podGang.Name, podGang
	})
}

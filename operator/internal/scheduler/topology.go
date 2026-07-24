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

package scheduler

import groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"

// RequiredTopologyKeyForPodGroup returns the required topology key for a PodGroup,
// preferring its constraint over the PodGang constraint and the supplied fallback.
func RequiredTopologyKeyForPodGroup(podGang *groveschedulerv1alpha1.PodGang, podGroupName, fallback string) string {
	if podGang == nil {
		return fallback
	}
	for _, podGroup := range podGang.Spec.PodGroups {
		if podGroup.Name == podGroupName {
			if key := requiredTopologyKey(podGroup.TopologyConstraint); key != "" {
				return key
			}
			break
		}
	}
	if key := requiredTopologyKey(podGang.Spec.TopologyConstraint); key != "" {
		return key
	}
	return fallback
}

func requiredTopologyKey(topologyConstraint *groveschedulerv1alpha1.TopologyConstraint) string {
	if topologyConstraint == nil || topologyConstraint.PackConstraint == nil || topologyConstraint.PackConstraint.Required == nil {
		return ""
	}
	return *topologyConstraint.PackConstraint.Required
}

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
	return topologyKeyForPodGroup(podGang, podGroupName, fallback, func(constraint *groveschedulerv1alpha1.TopologyPackConstraint) *string {
		return constraint.Required
	})
}

// PreferredTopologyKeyForPodGroup returns the preferred topology key for a PodGroup,
// preferring its constraint over the PodGang constraint.
func PreferredTopologyKeyForPodGroup(podGang *groveschedulerv1alpha1.PodGang, podGroupName string) string {
	return topologyKeyForPodGroup(podGang, podGroupName, "", func(constraint *groveschedulerv1alpha1.TopologyPackConstraint) *string {
		return constraint.Preferred
	})
}

func topologyKeyForPodGroup(podGang *groveschedulerv1alpha1.PodGang, podGroupName, fallback string, keyForConstraint func(*groveschedulerv1alpha1.TopologyPackConstraint) *string) string {
	if podGang == nil {
		return fallback
	}
	for _, podGroup := range podGang.Spec.PodGroups {
		if podGroup.Name == podGroupName {
			if key := topologyKey(podGroup.TopologyConstraint, keyForConstraint); key != "" {
				return key
			}
			break
		}
	}
	if key := topologyKey(podGang.Spec.TopologyConstraint, keyForConstraint); key != "" {
		return key
	}
	return fallback
}

func topologyKey(topologyConstraint *groveschedulerv1alpha1.TopologyConstraint, keyForConstraint func(*groveschedulerv1alpha1.TopologyPackConstraint) *string) string {
	if topologyConstraint == nil || topologyConstraint.PackConstraint == nil {
		return ""
	}
	key := keyForConstraint(topologyConstraint.PackConstraint)
	if key == nil {
		return ""
	}
	return *key
}

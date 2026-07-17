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

import (
	"testing"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
)

func TestRequiredTopologyKeyForPodGroup(t *testing.T) {
	podGroupKey := "kubernetes.io/hostname"
	podGangKey := "topology.kubernetes.io/zone"
	podGang := &groveschedulerv1alpha1.PodGang{
		Spec: groveschedulerv1alpha1.PodGangSpec{
			TopologyConstraint: requiredTopologyConstraint(podGangKey),
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{Name: "worker", TopologyConstraint: requiredTopologyConstraint(podGroupKey)},
				{Name: "frontend"},
			},
		},
	}

	assert.Equal(t, podGroupKey, RequiredTopologyKeyForPodGroup(podGang, "worker", "fallback"))
	assert.Equal(t, podGangKey, RequiredTopologyKeyForPodGroup(podGang, "frontend", "fallback"))
	assert.Equal(t, podGangKey, RequiredTopologyKeyForPodGroup(podGang, "missing", "fallback"))
	assert.Equal(t, "fallback", RequiredTopologyKeyForPodGroup(nil, "worker", "fallback"))
}

func requiredTopologyConstraint(key string) *groveschedulerv1alpha1.TopologyConstraint {
	return &groveschedulerv1alpha1.TopologyConstraint{
		PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: &key},
	}
}

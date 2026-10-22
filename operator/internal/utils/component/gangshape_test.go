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
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"
)

func newGangShapePCS() *grovecorev1beta1.PodCliqueSet {
	return &grovecorev1beta1.PodCliqueSet{
		Spec: grovecorev1beta1.PodCliqueSetSpec{
			Template: grovecorev1beta1.PodCliqueSetTemplateSpec{
				Cliques: []*grovecorev1beta1.PodCliqueTemplateSpec{
					{Name: "fe", Spec: grovecorev1beta1.PodCliqueSpec{Replicas: 4, Init: initSpec(ptr.To[int32](3)), Termination: availabilityTermination(ptr.To[int32](2), nil)}},
					{Name: "leader", Spec: grovecorev1beta1.PodCliqueSpec{Replicas: 1, Init: initSpec(ptr.To[int32](1))}},
				},
				PodCliqueScalingGroupConfigs: []grovecorev1beta1.PodCliqueScalingGroupConfig{
					{Name: "sg", CliqueNames: []string{"leader"}, Replicas: ptr.To[int32](3), Init: initSpec(ptr.To[int32](2))},
				},
			},
		},
	}
}

// TestComputeGangShapeHash pins that only init.minReplicas enters the gang-shape hash.
func TestComputeGangShapeHash(t *testing.T) {
	base := ComputeGangShapeHash(newGangShapePCS())

	testCases := []struct {
		description string
		mutate      func(*grovecorev1beta1.PodCliqueSet)
		wantChanged bool
	}{
		{
			description: "clique init.minReplicas change changes the hash",
			mutate: func(pcs *grovecorev1beta1.PodCliqueSet) {
				pcs.Spec.Template.Cliques[0].Spec.Init = initSpec(ptr.To[int32](4))
			},
			wantChanged: true,
		},
		{
			description: "PCSG init.minReplicas change changes the hash",
			mutate: func(pcs *grovecorev1beta1.PodCliqueSet) {
				pcs.Spec.Template.PodCliqueScalingGroupConfigs[0].Init = initSpec(ptr.To[int32](3))
			},
			wantChanged: true,
		},
		{
			description: "availability.minReplicas change keeps the hash",
			mutate: func(pcs *grovecorev1beta1.PodCliqueSet) {
				pcs.Spec.Template.Cliques[0].Spec.Termination = availabilityTermination(ptr.To[int32](3), nil)
			},
		},
		{
			description: "rollingUpdate.minReplicas change keeps the hash",
			mutate: func(pcs *grovecorev1beta1.PodCliqueSet) {
				pcs.Spec.Template.Cliques[0].RollingUpdate = &grovecorev1beta1.RollingUpdateConfiguration{MinReplicas: ptr.To[int32](1)}
			},
		},
		{
			description: "replicas change keeps the hash",
			mutate:      func(pcs *grovecorev1beta1.PodCliqueSet) { pcs.Spec.Template.Cliques[0].Spec.Replicas = 8 },
		},
		{
			description: "clique order keeps the hash",
			mutate: func(pcs *grovecorev1beta1.PodCliqueSet) {
				cliques := pcs.Spec.Template.Cliques
				cliques[0], cliques[1] = cliques[1], cliques[0]
			},
		},
		{
			description: "an unset init.minReplicas that resolves to the same value keeps the hash",
			mutate:      func(pcs *grovecorev1beta1.PodCliqueSet) { pcs.Spec.Template.Cliques[1].Spec.Init = nil },
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			pcs := newGangShapePCS()
			tc.mutate(pcs)
			if tc.wantChanged {
				assert.NotEqual(t, base, ComputeGangShapeHash(pcs))
			} else {
				assert.Equal(t, base, ComputeGangShapeHash(pcs))
			}
		})
	}
}

// TestGangShapeHashLabelValue pins that an existing label is kept and a missing one adopts the current hash.
func TestGangShapeHashLabelValue(t *testing.T) {
	pcs := newGangShapePCS()
	current := ComputeGangShapeHash(pcs)
	assert.Equal(t, current, GangShapeHashLabelValue(pcs, nil))
	assert.Equal(t, current, GangShapeHashLabelValue(pcs, map[string]string{apicommon.LabelGangShapeHash: ""}))
	assert.Equal(t, "stale", GangShapeHashLabelValue(pcs, map[string]string{apicommon.LabelGangShapeHash: "stale"}))
}

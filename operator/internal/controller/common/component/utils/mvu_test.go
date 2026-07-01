// /*
// Copyright 2025 The Grove Authors.
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
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestComputeMVUTemplateFromPCSTemplateSpec(t *testing.T) {
	tests := []struct {
		name                    string
		pcs                     *grovecorev1alpha1.PodCliqueSet
		expectedStandalonePCLQs map[string]int32
		expectedPCSGs           map[string]int32
	}{
		{
			name: "standalone PCLQs only",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pcs"},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To(int32(2))}},
							{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To(int32(1))}},
						},
					},
				},
			},
			expectedStandalonePCLQs: map[string]int32{"worker": 2, "frontend": 1},
			expectedPCSGs:           map[string]int32{},
		},
		{
			name: "PCSGs only",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pcs"},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "sg-worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2, MinAvailable: ptr.To(int32(2))}},
						},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{Name: "scaling-group", Replicas: ptr.To(int32(4)), MinAvailable: ptr.To(int32(2)), CliqueNames: []string{"sg-worker"}},
						},
					},
				},
			},
			expectedStandalonePCLQs: map[string]int32{},
			expectedPCSGs:           map[string]int32{"scaling-group": 2},
		},
		{
			name: "mixed standalone PCLQs and PCSGs",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pcs"},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
							{Name: "frontend", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 5, MinAvailable: ptr.To(int32(2))}},
							{Name: "prefill-worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 3, MinAvailable: ptr.To(int32(2))}},
							{Name: "decode-worker", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 4, MinAvailable: ptr.To(int32(2))}},
						},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{Name: "prefill", Replicas: ptr.To(int32(4)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"prefill-worker"}},
							{Name: "decode", Replicas: ptr.To(int32(3)), MinAvailable: ptr.To(int32(1)), CliqueNames: []string{"decode-worker"}},
						},
					},
				},
			},
			expectedStandalonePCLQs: map[string]int32{"frontend": 2},
			expectedPCSGs:           map[string]int32{"prefill": 1, "decode": 1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			template := ComputeMVUTemplateFromPCSTemplateSpec(tc.pcs)
			assert.Equal(t, tc.expectedStandalonePCLQs, template.StandalonePCLQs)
			assert.Equal(t, tc.expectedPCSGs, template.PCSGs)
		})
	}
}

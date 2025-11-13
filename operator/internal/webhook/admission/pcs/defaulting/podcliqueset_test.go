// /*
// Copyright 2024 The Grove Authors.
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

package defaulting

import (
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestDefaultPodCliqueSet(t *testing.T) {
	tests := []struct {
		name           string
		input          grovecorev1alpha1.PodCliqueSet
		topologyConfig configv1alpha1.ClusterTopologyConfiguration
		want           grovecorev1alpha1.PodCliqueSet
	}{
		{
			name: "basic defaults",
			input: grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "PCS1",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{{
							Name: "test",
							Spec: grovecorev1alpha1.PodCliqueSpec{
								Replicas: 2,
								ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{
									MinReplicas: ptr.To(int32(2)),
									MaxReplicas: 3,
								},
							},
						}},
					},
				},
			},
			topologyConfig: configv1alpha1.ClusterTopologyConfiguration{
				Enabled: false,
			},
			want: grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "PCS1",
					Namespace: "default",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{{
							Name: "test",
							Spec: grovecorev1alpha1.PodCliqueSpec{
								Replicas: 2,
								PodSpec: corev1.PodSpec{
									RestartPolicy:                 corev1.RestartPolicyAlways,
									TerminationGracePeriodSeconds: ptr.To[int64](30),
								},
								ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{
									MinReplicas: ptr.To(int32(2)),
									MaxReplicas: 3,
								},
								MinAvailable: ptr.To[int32](2),
							},
						}},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{},
						HeadlessServiceConfig: &grovecorev1alpha1.HeadlessServiceConfig{
							PublishNotReadyAddresses: true,
						},
						TerminationDelay: &metav1.Duration{Duration: 4 * time.Hour},
					},
				},
			},
		},
		{
			name: "topology enabled - label added with topology name",
			input: grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "PCS1",
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{{
							Name: "test",
							Spec: grovecorev1alpha1.PodCliqueSpec{
								Replicas: 2,
								ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{
									MinReplicas: ptr.To(int32(2)),
									MaxReplicas: 3,
								},
							},
						}},
					},
				},
			},
			topologyConfig: configv1alpha1.ClusterTopologyConfiguration{
				Enabled: true,
				Name:    "my-cluster-topology",
			},
			want: grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "PCS1",
					Namespace: "default",
					Labels: map[string]string{
						apicommon.LabelTopologyName: "my-cluster-topology",
					},
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{{
							Name: "test",
							Spec: grovecorev1alpha1.PodCliqueSpec{
								Replicas: 2,
								PodSpec: corev1.PodSpec{
									RestartPolicy:                 corev1.RestartPolicyAlways,
									TerminationGracePeriodSeconds: ptr.To[int64](30),
								},
								ScaleConfig: &grovecorev1alpha1.AutoScalingConfig{
									MinReplicas: ptr.To(int32(2)),
									MaxReplicas: 3,
								},
								MinAvailable: ptr.To[int32](2),
							},
						}},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{},
						HeadlessServiceConfig: &grovecorev1alpha1.HeadlessServiceConfig{
							PublishNotReadyAddresses: true,
						},
						TerminationDelay: &metav1.Duration{Duration: 4 * time.Hour},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defaultPodCliqueSet(&tt.input, tt.topologyConfig)
			assert.Equal(t, tt.want, tt.input)
		})
	}
}

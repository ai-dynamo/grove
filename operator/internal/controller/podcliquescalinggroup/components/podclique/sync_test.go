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

package podclique

import (
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

func TestFindMatchingPCSGConfig(t *testing.T) {
	tests := []struct {
		name               string
		pcs                *grovecorev1alpha1.PodCliqueSet
		pcsg               *grovecorev1alpha1.PodCliqueScalingGroup
		expectedConfig     *grovecorev1alpha1.PodCliqueScalingGroupConfig
		expectedConfigName string
	}{
		{
			name: "matching_config_found",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       types.UID("test-uid"),
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:         "sg1",
								CliqueNames:  []string{"clique1", "clique2"},
								Replicas:     ptr.To(int32(2)),
								MinAvailable: ptr.To(int32(1)),
							},
							{
								Name:         "sg2",
								CliqueNames:  []string{"clique3"},
								Replicas:     ptr.To(int32(3)),
								MinAvailable: ptr.To(int32(2)),
							},
						},
					},
				},
			},
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs-0-sg1", // Generated name: pcs-name-replicaIndex-sgName
					Namespace: "default",
					Labels: map[string]string{
						apicommon.LabelPodCliqueSetReplicaIndex: "0",
					},
				},
			},
			expectedConfigName: "sg1",
		},
		{
			name: "matching_config_found_second_config",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       types.UID("test-uid"),
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:         "sg1",
								CliqueNames:  []string{"clique1", "clique2"},
								Replicas:     ptr.To(int32(2)),
								MinAvailable: ptr.To(int32(1)),
							},
							{
								Name:         "sg2",
								CliqueNames:  []string{"clique3"},
								Replicas:     ptr.To(int32(3)),
								MinAvailable: ptr.To(int32(2)),
							},
						},
					},
				},
			},
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs-0-sg2", // Matches sg2
					Namespace: "default",
					Labels: map[string]string{
						apicommon.LabelPodCliqueSetReplicaIndex: "0",
					},
				},
			},
			expectedConfigName: "sg2",
		},
		{
			name: "no_matching_config",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       types.UID("test-uid"),
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:         "sg1",
								CliqueNames:  []string{"clique1"},
								Replicas:     ptr.To(int32(2)),
								MinAvailable: ptr.To(int32(1)),
							},
						},
					},
				},
			},
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs-0-sg-unknown", // No matching config
					Namespace: "default",
					Labels: map[string]string{
						apicommon.LabelPodCliqueSetReplicaIndex: "0",
					},
				},
			},
			expectedConfig: nil,
		},
		{
			name: "missing_replica_index_label",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       types.UID("test-uid"),
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:        "sg1",
								CliqueNames: []string{"clique1"},
							},
						},
					},
				},
			},
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs-0-sg1",
					Namespace: "default",
					Labels:    map[string]string{}, // Missing replica index label
				},
			},
			expectedConfig: nil,
		},
		{
			name: "invalid_replica_index_label",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       types.UID("test-uid"),
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:        "sg1",
								CliqueNames: []string{"clique1"},
							},
						},
					},
				},
			},
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs-0-sg1",
					Namespace: "default",
					Labels: map[string]string{
						apicommon.LabelPodCliqueSetReplicaIndex: "invalid", // Invalid value
					},
				},
			},
			expectedConfig: nil,
		},
		{
			name: "empty_pcsg_configs",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       types.UID("test-uid"),
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{},
					},
				},
			},
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs-0-sg1",
					Namespace: "default",
					Labels: map[string]string{
						apicommon.LabelPodCliqueSetReplicaIndex: "0",
					},
				},
			},
			expectedConfig: nil,
		},
		{
			name: "different_replica_index",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       types.UID("test-uid"),
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 3,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:         "sg1",
								CliqueNames:  []string{"clique1"},
								Replicas:     ptr.To(int32(2)),
								MinAvailable: ptr.To(int32(1)),
							},
						},
					},
				},
			},
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs-2-sg1", // Replica index 2
					Namespace: "default",
					Labels: map[string]string{
						apicommon.LabelPodCliqueSetReplicaIndex: "2",
					},
				},
			},
			expectedConfigName: "sg1",
		},
		{
			name: "config_with_termination_delay_override",
			pcs: &grovecorev1alpha1.PodCliqueSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs",
					Namespace: "default",
					UID:       types.UID("test-uid"),
				},
				Spec: grovecorev1alpha1.PodCliqueSetSpec{
					Replicas: 1,
					Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
						TerminationDelay: &metav1.Duration{Duration: 5 * time.Minute},
						PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{
							{
								Name:             "sg1",
								CliqueNames:      []string{"clique1"},
								Replicas:         ptr.To(int32(2)),
								MinAvailable:     ptr.To(int32(1)),
								TerminationDelay: &metav1.Duration{Duration: 2 * time.Minute}, // Override
							},
						},
					},
				},
			},
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcs-0-sg1",
					Namespace: "default",
					Labels: map[string]string{
						apicommon.LabelPodCliqueSetReplicaIndex: "0",
					},
				},
			},
			expectedConfigName: "sg1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := findMatchingPCSGConfig(tt.pcs, tt.pcsg)

			if tt.expectedConfig == nil && tt.expectedConfigName == "" {
				assert.Nil(t, result, "expected nil config")
			} else {
				assert.NotNil(t, result, "expected non-nil config")
				if result != nil {
					assert.Equal(t, tt.expectedConfigName, result.Name, "config name mismatch")
				}
			}
		})
	}
}

func TestGetExpectedPodCliqueFQNsByPCSGReplica(t *testing.T) {
	tests := []struct {
		name     string
		pcsg     *grovecorev1alpha1.PodCliqueScalingGroup
		expected map[int][]string
	}{
		{
			name: "single_replica_single_clique",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcsg",
					Namespace: "default",
				},
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					Replicas:    1,
					CliqueNames: []string{"clique1"},
				},
			},
			expected: map[int][]string{
				0: {"test-pcsg-0-clique1"},
			},
		},
		{
			name: "multiple_replicas_multiple_cliques",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcsg",
					Namespace: "default",
				},
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					Replicas:    3,
					CliqueNames: []string{"frontend", "backend"},
				},
			},
			expected: map[int][]string{
				0: {"test-pcsg-0-frontend", "test-pcsg-0-backend"},
				1: {"test-pcsg-1-frontend", "test-pcsg-1-backend"},
				2: {"test-pcsg-2-frontend", "test-pcsg-2-backend"},
			},
		},
		{
			name: "zero_replicas",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcsg",
					Namespace: "default",
				},
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					Replicas:    0,
					CliqueNames: []string{"clique1"},
				},
			},
			expected: map[int][]string{},
		},
		{
			name: "empty_clique_names",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pcsg",
					Namespace: "default",
				},
				Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
					Replicas:    2,
					CliqueNames: []string{},
				},
			},
			expected: map[int][]string{
				0: {},
				1: {},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getExpectedPodCliqueFQNsByPCSGReplica(tt.pcsg)

			assert.Equal(t, len(tt.expected), len(result), "number of replica keys mismatch")
			for replicaIndex, expectedFQNs := range tt.expected {
				actualFQNs, exists := result[replicaIndex]
				assert.True(t, exists, "replica index %d not found in result", replicaIndex)
				assert.ElementsMatch(t, expectedFQNs, actualFQNs, "PCLQ FQNs mismatch for replica %d", replicaIndex)
			}
		})
	}
}

func TestComputePCSGReplicasToDelete(t *testing.T) {
	tests := []struct {
		name             string
		existingReplicas int
		expectedReplicas int
		expectedIndices  []string
	}{
		{
			name:             "no_deletion_needed",
			existingReplicas: 3,
			expectedReplicas: 3,
			expectedIndices:  []string{},
		},
		{
			name:             "scale_down_by_one",
			existingReplicas: 3,
			expectedReplicas: 2,
			expectedIndices:  []string{"2"},
		},
		{
			name:             "scale_down_by_multiple",
			existingReplicas: 5,
			expectedReplicas: 2,
			expectedIndices:  []string{"2", "3", "4"},
		},
		{
			name:             "scale_to_zero",
			existingReplicas: 3,
			expectedReplicas: 0,
			expectedIndices:  []string{"0", "1", "2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := computePCSGReplicasToDelete(tt.existingReplicas, tt.expectedReplicas)

			assert.ElementsMatch(t, tt.expectedIndices, result, "indices to delete mismatch")
		})
	}
}

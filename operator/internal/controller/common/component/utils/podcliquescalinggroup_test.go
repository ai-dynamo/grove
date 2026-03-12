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

func TestFindScalingGroupConfigForClique(t *testing.T) {
	// Create test scaling group configurations
	scalingGroupConfigs := []grovecorev1alpha1.PodCliqueScalingGroupConfig{
		{
			Name:        "sga",
			CliqueNames: []string{"pca", "pcb"},
		},
		{
			Name:        "sgb",
			CliqueNames: []string{"pcc", "pcd", "pce"},
		},
		{
			Name:        "sgc",
			CliqueNames: []string{"pcf"},
		},
	}

	tests := []struct {
		name               string
		configs            []grovecorev1alpha1.PodCliqueScalingGroupConfig
		cliqueName         string
		expectedFound      bool
		expectedConfigName string
	}{
		{
			name:               "clique found in first scaling group",
			configs:            scalingGroupConfigs,
			cliqueName:         "pca",
			expectedFound:      true,
			expectedConfigName: "sga",
		},
		{
			name:               "clique found in second scaling group",
			configs:            scalingGroupConfigs,
			cliqueName:         "pcd",
			expectedFound:      true,
			expectedConfigName: "sgb",
		},
		{
			name:               "clique found in third scaling group",
			configs:            scalingGroupConfigs,
			cliqueName:         "pcf",
			expectedFound:      true,
			expectedConfigName: "sgc",
		},
		{
			name:               "clique not found in any scaling group",
			configs:            scalingGroupConfigs,
			cliqueName:         "nonexistent",
			expectedFound:      false,
			expectedConfigName: "",
		},
		{
			name:               "empty clique name",
			configs:            scalingGroupConfigs,
			cliqueName:         "",
			expectedFound:      false,
			expectedConfigName: "",
		},
		{
			name:               "empty configs",
			configs:            []grovecorev1alpha1.PodCliqueScalingGroupConfig{},
			cliqueName:         "anyClique",
			expectedFound:      false,
			expectedConfigName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := FindScalingGroupConfigForClique(tt.configs, tt.cliqueName)
			assert.Equal(t, tt.expectedFound, config != nil)
			if tt.expectedFound {
				assert.Equal(t, tt.expectedConfigName, config.Name)
			} else {
				// When not found, config should be nil
				assert.Nil(t, config)
			}
		})
	}
}

// TestIsPCSGUpdateInProgress tests the IsPCSGUpdateInProgress function
func TestIsPCSGUpdateInProgress(t *testing.T) {
	tests := []struct {
		name     string
		pcsg     *grovecorev1alpha1.PodCliqueScalingGroup
		expected bool
	}{
		{
			name: "returns_false_when_update_progress_is_nil",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					UpdateProgress: nil,
				},
			},
			expected: false,
		},
		{
			name: "returns_true_when_update_in_progress",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{
						UpdateStartedAt: metav1.Now(),
						UpdateEndedAt:   nil, // nil means in progress
					},
				},
			},
			expected: true,
		},
		{
			name: "returns_false_when_update_completed",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					UpdateProgress: &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{
						UpdateStartedAt: metav1.Now(),
						UpdateEndedAt:   ptr.To(metav1.Now()), // set means completed
					},
				},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := IsPCSGUpdateInProgress(tc.pcsg)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestIsPCSGUpdateComplete tests the IsPCSGUpdateComplete function
func TestIsPCSGUpdateComplete(t *testing.T) {
	tests := []struct {
		name              string
		pcsg              *grovecorev1alpha1.PodCliqueScalingGroup
		pcsGenerationHash string
		expected          bool
	}{
		{
			name: "returns_false_when_current_pcs_generation_hash_is_nil",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					CurrentPodCliqueSetGenerationHash: nil,
				},
			},
			pcsGenerationHash: "hash1",
			expected:          false,
		},
		{
			name: "returns_true_when_hash_matches",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					CurrentPodCliqueSetGenerationHash: ptr.To("hash1"),
				},
			},
			pcsGenerationHash: "hash1",
			expected:          true,
		},
		{
			name: "returns_false_when_hash_differs",
			pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					CurrentPodCliqueSetGenerationHash: ptr.To("old-hash"),
				},
			},
			pcsGenerationHash: "new-hash",
			expected:          false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := IsPCSGUpdateComplete(tc.pcsg, tc.pcsGenerationHash)
			assert.Equal(t, tc.expected, result)
		})
	}
}

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

package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerateInitContainerSATokenSecretName(t *testing.T) {
	assert.Equal(t, "test-pcs-ic-sat", GenerateInitContainerSATokenSecretName("test-pcs"))
	assert.Equal(t, "test-pcs-initc-sa-token-secret", GenerateLegacyInitContainerSATokenSecretName("test-pcs"))
}

func TestExtractScalingGroupNameFromPCSGFQN(t *testing.T) {
	tests := []struct {
		name           string
		pcsgName       string
		pcsNameReplica ResourceNameReplica
		expected       string
	}{
		{
			name:     "simple scaling group name",
			pcsgName: "simple1-0-sga",
			pcsNameReplica: ResourceNameReplica{
				Name:    "simple1",
				Replica: 0,
			},
			expected: "sga",
		},
		{
			name:     "scaling group with different replica index",
			pcsgName: "simple1-2-sga",
			pcsNameReplica: ResourceNameReplica{
				Name:    "simple1",
				Replica: 2,
			},
			expected: "sga",
		},
		{
			name:     "complex scaling group name",
			pcsgName: "test-workload-1-gpu-workers",
			pcsNameReplica: ResourceNameReplica{
				Name:    "test-workload",
				Replica: 1,
			},
			expected: "gpu-workers",
		},
		{
			name:     "scaling group with hyphens in name",
			pcsgName: "my-app-0-data-processing-group",
			pcsNameReplica: ResourceNameReplica{
				Name:    "my-app",
				Replica: 0,
			},
			expected: "data-processing-group",
		},
		{
			name:     "single character scaling group",
			pcsgName: "app-5-x",
			pcsNameReplica: ResourceNameReplica{
				Name:    "app",
				Replica: 5,
			},
			expected: "x",
		},
		{
			name:     "numeric scaling group name",
			pcsgName: "workload-0-123",
			pcsNameReplica: ResourceNameReplica{
				Name:    "workload",
				Replica: 0,
			},
			expected: "123",
		},
		{
			name:     "long PCS name with scaling group",
			pcsgName: "very-long-podcliqueset-name-0-sg",
			pcsNameReplica: ResourceNameReplica{
				Name:    "very-long-podcliqueset-name",
				Replica: 0,
			},
			expected: "sg",
		},
		{
			name:     "scaling group name with numbers and hyphens",
			pcsgName: "app-3-worker-group-v2",
			pcsNameReplica: ResourceNameReplica{
				Name:    "app",
				Replica: 3,
			},
			expected: "worker-group-v2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ExtractScalingGroupNameFromPCSGFQN(tt.pcsgName, tt.pcsNameReplica)
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGenerateBasePodGangName(t *testing.T) {
	tests := []struct {
		name           string
		pcsNameReplica ResourceNameReplica
		expected       string
	}{
		{
			name:           "simple base PodGang name",
			pcsNameReplica: ResourceNameReplica{Name: "simple1", Replica: 0},
			expected:       "simple1-0",
		},
		{
			name:           "base PodGang with different replica",
			pcsNameReplica: ResourceNameReplica{Name: "test-app", Replica: 2},
			expected:       "test-app-2",
		},
		{
			name:           "complex PCS name",
			pcsNameReplica: ResourceNameReplica{Name: "my-complex-workload", Replica: 5},
			expected:       "my-complex-workload-5",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := GenerateBasePodGangName(tc.pcsNameReplica)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestExtractScalingGroupNameFromPCSGFQN_Consistency(t *testing.T) {
	// Test that ExtractScalingGroupNameFromPCSGFQN is the inverse of GeneratePodCliqueScalingGroupName
	testCases := []struct {
		pcsNameReplica   ResourceNameReplica
		scalingGroupName string
	}{
		{
			pcsNameReplica:   ResourceNameReplica{Name: "simple1", Replica: 0},
			scalingGroupName: "sga",
		},
		{
			pcsNameReplica:   ResourceNameReplica{Name: "test-app", Replica: 2},
			scalingGroupName: "worker-group",
		},
		{
			pcsNameReplica:   ResourceNameReplica{Name: "my-workload", Replica: 1},
			scalingGroupName: "gpu-nodes",
		},
	}

	for _, tc := range testCases {
		t.Run("consistency_test", func(t *testing.T) {
			// Generate PCSG name
			generatedPCSGName := GeneratePodCliqueScalingGroupName(tc.pcsNameReplica, tc.scalingGroupName)

			// Extract scaling group name back
			extractedScalingGroupName, err := ExtractScalingGroupNameFromPCSGFQN(generatedPCSGName, tc.pcsNameReplica)
			assert.NoError(t, err)

			// They should match
			assert.Equal(t, tc.scalingGroupName, extractedScalingGroupName)
		})
	}
}

func TestCreatePodGangNameFromPCSGFQN(t *testing.T) {
	tests := []struct {
		name             string
		pcsgFQN          string
		pcsgReplicaIndex int
		expected         string
	}{
		{
			name:             "scaled PodGang name from FQN",
			pcsgFQN:          "simple1-0-sga",
			pcsgReplicaIndex: 1,
			expected:         "simple1-0-sga-1",
		},
		{
			name:             "scaled PodGang name from FQN with different replica",
			pcsgFQN:          "simple1-0-sga",
			pcsgReplicaIndex: 2,
			expected:         "simple1-0-sga-2",
		},
		{
			name:             "complex scaling group name",
			pcsgFQN:          "test-2-complex-sg",
			pcsgReplicaIndex: 0,
			expected:         "test-2-complex-sg-0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := CreatePodGangNameFromPCSGFQN(tc.pcsgFQN, tc.pcsgReplicaIndex)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestGenerateAnchorPodGangName(t *testing.T) {
	tests := []struct {
		name         string
		pcsName      string
		replicaIndex int
		hash         string
		anchorIndex  int32
		expected     string
	}{
		{
			name:         "single anchor index 0",
			pcsName:      "my-pcs",
			replicaIndex: 0,
			hash:         "f5b9x",
			anchorIndex:  0,
			expected:     "my-pcs-0-f5b9x-0",
		},
		{
			name:         "second anchor of same hash",
			pcsName:      "my-pcs",
			replicaIndex: 2,
			hash:         "f5b9x",
			anchorIndex:  1,
			expected:     "my-pcs-2-f5b9x-1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := GenerateAnchorPodGangName(ResourceNameReplica{Name: tc.pcsName, Replica: tc.replicaIndex}, tc.hash, tc.anchorIndex)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

func TestGenerateNonAnchorPodGangName(t *testing.T) {
	tests := []struct {
		name         string
		pcsName      string
		replicaIndex int
		hash         string
		pcsgName     string
		index        int32
		expected     string
	}{
		{
			name:         "single-segment pcsg name",
			pcsName:      "my-pcs",
			replicaIndex: 0,
			hash:         "f5b9x",
			pcsgName:     "prefill",
			index:        3,
			expected:     "my-pcs-0-f5b9x-prefill-3",
		},
		{
			name:         "pcsg name with hyphens",
			pcsName:      "my-pcs",
			replicaIndex: 0,
			hash:         "f5b9x",
			pcsgName:     "gpu-workers",
			index:        0,
			expected:     "my-pcs-0-f5b9x-gpu-workers-0",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := GenerateNonAnchorPodGangName(ResourceNameReplica{Name: tc.pcsName, Replica: tc.replicaIndex}, tc.hash, tc.pcsgName, tc.index)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

func TestExtractPCSGNameAndIndexFromPodGangName(t *testing.T) {
	tests := []struct {
		name          string
		pcsName       string
		replicaIndex  int
		hash          string
		podGangName   string
		expectedPCSG  string
		expectedIndex int32
		expectErr     bool
	}{
		{
			name:          "single-segment pcsg name",
			pcsName:       "my-pcs",
			replicaIndex:  0,
			hash:          "f5b9x",
			podGangName:   "my-pcs-0-f5b9x-prefill-3",
			expectedPCSG:  "prefill",
			expectedIndex: 3,
		},
		{
			name:          "pcsg name with hyphens",
			pcsName:       "my-pcs",
			replicaIndex:  0,
			hash:          "f5b9x",
			podGangName:   "my-pcs-0-f5b9x-gpu-workers-0",
			expectedPCSG:  "gpu-workers",
			expectedIndex: 0,
		},
		{
			name:         "prefix missing",
			pcsName:      "my-pcs",
			replicaIndex: 0,
			hash:         "f5b9x",
			podGangName:  "other-pcs-1-abcde-prefill-3",
			expectErr:    true,
		},
		{
			name:         "non-integer index",
			pcsName:      "my-pcs",
			replicaIndex: 0,
			hash:         "f5b9x",
			podGangName:  "my-pcs-0-f5b9x-prefill-x",
			expectErr:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pcsgName, index, err := ExtractPCSGNameAndIndexFromPodGangName(tc.podGangName, ResourceNameReplica{Name: tc.pcsName, Replica: tc.replicaIndex}, tc.hash)
			if tc.expectErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.expectedPCSG, pcsgName)
			assert.Equal(t, tc.expectedIndex, index)
		})
	}
}

func TestGeneratePodGangMapName(t *testing.T) {
	pcsNameReplica := ResourceNameReplica{Name: "my-pcs", Replica: 0}
	assert.Equal(t, "my-pcs-0", GeneratePodGangMapName(pcsNameReplica))
}

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

package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := ExtractScalingGroupNameFromPCSGFQN(tc.pcsgName, tc.pcsNameReplica)
			assert.Equal(t, tc.expected, result)
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
			extractedScalingGroupName := ExtractScalingGroupNameFromPCSGFQN(generatedPCSGName, tc.pcsNameReplica)

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

func TestGeneratePodGangName(t *testing.T) {
	tests := []struct {
		name         string
		pcsName      string
		replicaIndex int32
		uniqueSuffix string
		expected     string
	}{
		{
			name:         "numeric suffix",
			pcsName:      "my-pcs",
			replicaIndex: 0,
			uniqueSuffix: "1748266985000123456",
			expected:     "my-pcs-0-1748266985000123456",
		},
		{
			name:         "different replica index",
			pcsName:      "my-pcs",
			replicaIndex: 2,
			uniqueSuffix: "1748266985000123456",
			expected:     "my-pcs-2-1748266985000123456",
		},
		{
			name:         "different PCS name",
			pcsName:      "inference-workload",
			replicaIndex: 1,
			uniqueSuffix: "1748266985999999999",
			expected:     "inference-workload-1-1748266985999999999",
		},
		{
			name:         "opaque non-numeric suffix",
			pcsName:      "my-pcs",
			replicaIndex: 0,
			uniqueSuffix: "abc123",
			expected:     "my-pcs-0-abc123",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := GeneratePodGangName(tc.pcsName, tc.replicaIndex, tc.uniqueSuffix)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestGeneratePodGangMapName(t *testing.T) {
	pcsNameReplica := ResourceNameReplica{Name: "my-pcs", Replica: 0}
	assert.Equal(t, "my-pcs-0", GeneratePodGangMapName(pcsNameReplica))
}

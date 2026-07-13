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
	"regexp"
	"testing"

	"github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
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

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractScalingGroupNameFromPCSGFQN(tt.pcsgName, tt.pcsNameReplica)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGeneratePodName(t *testing.T) {
	podName := GeneratePodName("workload-0-prefill-1-worker", 12)

	assert.Regexp(t, regexp.MustCompile(`^workload-0-prefill-1-worker-12-[a-z0-9]{5}$`), podName)
	assert.Len(t, podName[len("workload-0-prefill-1-worker-12-"):], PodNameRandomSuffixLength)
}

func TestGeneratePodHostname(t *testing.T) {
	assert.Equal(t, "workload-0-prefill-1-worker-12", GeneratePodHostname("workload-0-prefill-1-worker", 12))
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

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateBasePodGangName(tt.pcsNameReplica)
			assert.Equal(t, tt.expected, result)
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

func TestGeneratePodGangNameForPodCliqueOwnedByPodCliqueSet(t *testing.T) {
	// Create a PodCliqueSet for testing
	pcs := &v1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "simple1",
		},
	}

	tests := []struct {
		name                string
		pcsReplicaIndex     int
		expectedPodGangName string
	}{
		{
			name:                "PCS replica 0",
			pcsReplicaIndex:     0,
			expectedPodGangName: "simple1-0",
		},
		{
			name:                "PCS replica 1",
			pcsReplicaIndex:     1,
			expectedPodGangName: "simple1-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GeneratePodGangNameForPodCliqueOwnedByPodCliqueSet(pcs, tt.pcsReplicaIndex)
			assert.Equal(t, tt.expectedPodGangName, result)
		})
	}
}

func TestGeneratePodGangNameForPodCliqueOwnedByPCSG(t *testing.T) {
	// Create a PodCliqueSet for testing
	pcs := &v1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "simple1",
		},
	}

	tests := []struct {
		name                string
		pcsReplicaIndex     int
		pcsg                *v1alpha1.PodCliqueScalingGroup
		pcsgReplicaIndex    int
		expectedPodGangName string
	}{
		{
			name:            "PCSG PodClique within minAvailable",
			pcsReplicaIndex: 0,
			pcsg: &v1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "simple1-0-sga",
				},
				Spec: v1alpha1.PodCliqueScalingGroupSpec{
					MinAvailable: ptr.To[int32](3),
				},
			},
			pcsgReplicaIndex:    1, // Within minAvailable (< 3)
			expectedPodGangName: "simple1-0",
		},
		{
			name:            "PCSG PodClique beyond minAvailable - first scaled",
			pcsReplicaIndex: 0,
			pcsg: &v1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "simple1-0-sga",
				},
				Spec: v1alpha1.PodCliqueScalingGroupSpec{
					MinAvailable: ptr.To[int32](3),
				},
			},
			pcsgReplicaIndex:    3,                 // Beyond minAvailable (>= 3)
			expectedPodGangName: "simple1-0-sga-0", // First scaled PodGang (3-3=0)
		},
		{
			name:            "PCSG PodClique beyond minAvailable - second scaled",
			pcsReplicaIndex: 0,
			pcsg: &v1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "simple1-0-sga",
				},
				Spec: v1alpha1.PodCliqueScalingGroupSpec{
					MinAvailable: ptr.To[int32](3),
				},
			},
			pcsgReplicaIndex:    4,                 // Beyond minAvailable (>= 3)
			expectedPodGangName: "simple1-0-sga-1", // Second scaled PodGang (4-3=1)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GeneratePodGangNameForPodCliqueOwnedByPCSG(pcs, tt.pcsReplicaIndex, tt.pcsg, tt.pcsgReplicaIndex)
			assert.Equal(t, tt.expectedPodGangName, result)
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

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CreatePodGangNameFromPCSGFQN(tt.pcsgFQN, tt.pcsgReplicaIndex)
			assert.Equal(t, tt.expected, result)
		})
	}
}

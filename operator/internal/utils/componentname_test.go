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

package utils

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGetPodCliqueSetReplicaIndexFromPodCliqueFQN(t *testing.T) {
	testCases := []struct {
		description   string
		pcsName       string
		pclqFQNName   string
		expectedIndex int
		expectedErr   bool
	}{
		{
			description:   "PodClique and PCS name without hyphen",
			pcsName:       "inference",
			pclqFQNName:   "inference-0-prefill",
			expectedIndex: 0,
			expectedErr:   false,
		},
		{
			description:   "PodClique name with hyphen and PCS name without hyphen",
			pcsName:       "inference",
			pclqFQNName:   "inference-1-prefill-leader",
			expectedIndex: 1,
			expectedErr:   false,
		},
		{
			description:   "PodClique name with hyphen and PCS name with hyphen",
			pcsName:       "pcs-inference",
			pclqFQNName:   "pcs-inference-2-prefill-worker",
			expectedIndex: 2,
			expectedErr:   false,
		},
		{
			description: "Malformed PodClique FQN name",
			pcsName:     "inference",
			pclqFQNName: "inference-prefill",
			expectedErr: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			index, err := GetPodCliqueSetReplicaIndexFromPodCliqueFQN(tc.pcsName, tc.pclqFQNName)
			if tc.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedIndex, index)
			}
		})
	}
}

// TestGetPodCliqueNameFromPodCliqueFQN tests extracting unqualified PodClique names from fully qualified names.
// It covers both PodCliqueScalingGroup and PodCliqueSet scenarios, as well as error cases with missing labels.
func TestGetPodCliqueNameFromPodCliqueFQN(t *testing.T) {
	testCases := []struct {
		// name identifies the test case
		name string
		// pclqObjectMeta contains the PodClique metadata to test
		pclqObjectMeta metav1.ObjectMeta
		// expectedName is the expected extracted name
		expectedName string
		// expectedErr indicates whether an error is expected
		expectedErr bool
	}{
		{
			// PodCliqueScalingGroup with all required labels present
			name: "pcsg-valid-labels",
			pclqObjectMeta: metav1.ObjectMeta{
				Name:      "my-pcsg-1-worker",
				Namespace: "default",
				Labels: map[string]string{
					apicommon.LabelPodCliqueScalingGroup:             "my-pcsg",
					apicommon.LabelPodCliqueScalingGroupReplicaIndex: "1",
				},
			},
			expectedName: "worker",
			expectedErr:  false,
		},
		{
			// PodCliqueScalingGroup missing replica index label
			name: "pcsg-missing-replica-index",
			pclqObjectMeta: metav1.ObjectMeta{
				Name:      "my-pcsg-1-worker",
				Namespace: "default",
				Labels: map[string]string{
					apicommon.LabelPodCliqueScalingGroup: "my-pcsg",
				},
			},
			expectedErr: true,
		},
		{
			// PodCliqueSet with all required labels present
			name: "pcs-valid-labels",
			pclqObjectMeta: metav1.ObjectMeta{
				Name:      "my-pcs-2-prefill",
				Namespace: "default",
				Labels: map[string]string{
					apicommon.LabelPartOfKey:                "my-pcs",
					apicommon.LabelPodCliqueSetReplicaIndex: "2",
				},
			},
			expectedName: "prefill",
			expectedErr:  false,
		},
		{
			// PodCliqueSet missing part-of label
			name: "pcs-missing-part-of-label",
			pclqObjectMeta: metav1.ObjectMeta{
				Name:      "my-pcs-2-prefill",
				Namespace: "default",
				Labels: map[string]string{
					apicommon.LabelPodCliqueSetReplicaIndex: "2",
				},
			},
			expectedErr: true,
		},
		{
			// PodCliqueSet missing replica index label
			name: "pcs-missing-replica-index",
			pclqObjectMeta: metav1.ObjectMeta{
				Name:      "my-pcs-2-prefill",
				Namespace: "default",
				Labels: map[string]string{
					apicommon.LabelPartOfKey: "my-pcs",
				},
			},
			expectedErr: true,
		},
		{
			// PodClique name with multiple hyphens
			name: "pcs-name-with-hyphens",
			pclqObjectMeta: metav1.ObjectMeta{
				Name:      "inference-pipeline-0-prefill-worker",
				Namespace: "default",
				Labels: map[string]string{
					apicommon.LabelPartOfKey:                "inference-pipeline",
					apicommon.LabelPodCliqueSetReplicaIndex: "0",
				},
			},
			expectedName: "prefill-worker",
			expectedErr:  false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			name, err := GetPodCliqueNameFromPodCliqueFQN(tc.pclqObjectMeta)
			if tc.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedName, name)
			}
		})
	}
}

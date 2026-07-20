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

package kubernetes

import (
	"errors"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Test helper functions
func newTestObjectMetaWithReplicaIndex(index string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      "test-resource",
		Namespace: "test-ns",
		Labels: map[string]string{
			apicommon.LabelPodCliqueSetReplicaIndex: index,
		},
	}
}

func newTestObjectMetaWithLabels(labels map[string]string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      "test-resource",
		Namespace: "test-ns",
		Labels:    labels,
	}
}

func newTestObjectMetaNilLabels() metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      "test-resource",
		Namespace: "test-ns",
		Labels:    nil,
	}
}

func newLabelsWithReplicaIndexAndExtras(index string, extraLabels map[string]string) map[string]string {
	labels := map[string]string{
		apicommon.LabelPodCliqueSetReplicaIndex: index,
	}
	for k, v := range extraLabels {
		labels[k] = v
	}
	return labels
}

func TestGetPodCliqueSetReplicaIndex(t *testing.T) {
	testCases := []struct {
		description   string
		objMeta       metav1.ObjectMeta
		expectedIndex int
		expectedError error
	}{
		{
			description: "valid replica index with multiple labels",
			objMeta: newTestObjectMetaWithLabels(newLabelsWithReplicaIndexAndExtras("5", map[string]string{
				"app":         "my-app",
				"version":     "v1.0",
				"environment": "test",
			})),
			expectedIndex: 5,
			expectedError: nil,
		},
		{
			description: "missing replica index label",
			objMeta: newTestObjectMetaWithLabels(map[string]string{
				"app":     "my-app",
				"version": "v1.0",
			}),
			expectedIndex: 0,
			expectedError: errNotFoundPodCliqueSetReplicaIndexLabel,
		},
		{
			description:   "nil labels map",
			objMeta:       newTestObjectMetaNilLabels(),
			expectedIndex: 0,
			expectedError: errNotFoundPodCliqueSetReplicaIndexLabel,
		},
		{
			description:   "invalid replica index conversion",
			objMeta:       newTestObjectMetaWithReplicaIndex("abc"),
			expectedIndex: 0,
			expectedError: errReplicaIndexIntConversion,
		},
		{
			description:   "negative replica index allowed",
			objMeta:       newTestObjectMetaWithReplicaIndex("-1"),
			expectedIndex: -1,
			expectedError: nil,
		},
		{
			description:   "large replica index supported",
			objMeta:       newTestObjectMetaWithReplicaIndex("9999"),
			expectedIndex: 9999,
			expectedError: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			index, err := GetPodCliqueSetReplicaIndex(tc.objMeta)

			if tc.expectedError != nil {
				assert.Error(t, err)
				assert.True(t, errors.Is(err, tc.expectedError),
					"expected error %v to contain %v", err, tc.expectedError)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tc.expectedIndex, index)
		})
	}
}

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
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/uuid"
)

func TestGetTopologyName(t *testing.T) {
	tests := []struct {
		name     string
		pcs      *grovecorev1alpha1.PodCliqueSet
		expected string
	}{
		{
			name: "PodCliqueSet with topology label",
			pcs: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				WithTopologyLabel("test-topology").
				Build(),
			expected: "test-topology",
		},
		{
			name: "PodCliqueSet without topology label",
			pcs: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				Build(),
			expected: "",
		},
		{
			name:     "nil PodCliqueSet",
			pcs:      nil,
			expected: "",
		},
		{
			name: "PodCliqueSet with empty topology label",
			pcs: testutils.NewPodCliqueSetBuilder("test-pcs", "test-ns", uuid.NewUUID()).
				WithTopologyLabel("").
				Build(),
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetTopologyName(tt.pcs)
			assert.Equal(t, tt.expected, result)
		})
	}
}

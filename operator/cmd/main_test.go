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

package main

import (
	"testing"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsComputeDomainCRDPresent(t *testing.T) {
	tests := []struct {
		name            string
		apiResourceList []*metav1.APIResourceList
		expected        bool
	}{
		{
			name:            "empty resource list",
			apiResourceList: []*metav1.APIResourceList{},
			expected:        false,
		},
		{
			name:            "nil resource list",
			apiResourceList: nil,
			expected:        false,
		},
		{
			name: "ComputeDomain CRD present with correct group version",
			apiResourceList: []*metav1.APIResourceList{
				{
					GroupVersion: apicommonconstants.ComputeDomainGroup + "/" + apicommonconstants.ComputeDomainVersion,
					APIResources: []metav1.APIResource{
						{Name: apicommonconstants.ComputeDomainResource},
					},
				},
			},
			expected: true,
		},
		{
			name: "ComputeDomain CRD present among other resources",
			apiResourceList: []*metav1.APIResourceList{
				{
					GroupVersion: "v1",
					APIResources: []metav1.APIResource{
						{Name: "pods"},
						{Name: "services"},
					},
				},
				{
					GroupVersion: apicommonconstants.ComputeDomainGroup + "/" + apicommonconstants.ComputeDomainVersion,
					APIResources: []metav1.APIResource{
						{Name: "resourceclaims"},
						{Name: apicommonconstants.ComputeDomainResource},
					},
				},
			},
			expected: true,
		},
		{
			name: "wrong resource name",
			apiResourceList: []*metav1.APIResourceList{
				{
					GroupVersion: apicommonconstants.ComputeDomainGroup + "/" + apicommonconstants.ComputeDomainVersion,
					APIResources: []metav1.APIResource{
						{Name: "wrongresource"},
					},
				},
			},
			expected: false,
		},
		{
			name: "wrong group",
			apiResourceList: []*metav1.APIResourceList{
				{
					GroupVersion: "wrong.group/v1beta1",
					APIResources: []metav1.APIResource{
						{Name: apicommonconstants.ComputeDomainResource},
					},
				},
			},
			expected: false,
		},
		{
			name: "wrong version",
			apiResourceList: []*metav1.APIResourceList{
				{
					GroupVersion: apicommonconstants.ComputeDomainGroup + "/v2",
					APIResources: []metav1.APIResource{
						{Name: apicommonconstants.ComputeDomainResource},
					},
				},
			},
			expected: false,
		},
		{
			name: "correct resource but different group version",
			apiResourceList: []*metav1.APIResourceList{
				{
					GroupVersion: "resource.nvidia.com/v1",
					APIResources: []metav1.APIResource{
						{Name: apicommonconstants.ComputeDomainResource},
					},
				},
			},
			expected: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := isComputeDomainCRDPresent(test.apiResourceList)
			assert.Equal(t, test.expected, result)
		})
	}
}

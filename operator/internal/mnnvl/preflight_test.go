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

package mnnvl

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakediscovery "k8s.io/client-go/discovery/fake"
	k8stesting "k8s.io/client-go/testing"
)

func Test_validateComputeDomainCRD(t *testing.T) {
	tests := []struct {
		name        string
		resources   []*metav1.APIResourceList
		reactor     func(action k8stesting.Action) (bool, runtime.Object, error)
		expectError bool
		errorMsg    string
	}{
		{
			name: "CRD present - success",
			resources: []*metav1.APIResourceList{
				{
					GroupVersion: ComputeDomainGroup + "/" + ComputeDomainVersion,
					APIResources: []metav1.APIResource{
						{Name: ComputeDomainResource},
					},
				},
			},
			expectError: false,
		},
		{
			name: "CRD present among many resources - success",
			resources: []*metav1.APIResourceList{
				{
					GroupVersion: "v1",
					APIResources: []metav1.APIResource{
						{Name: "pods"},
						{Name: "services"},
						{Name: "configmaps"},
					},
				},
				{
					GroupVersion: "apps/v1",
					APIResources: []metav1.APIResource{
						{Name: "deployments"},
						{Name: "statefulsets"},
					},
				},
				{
					GroupVersion: ComputeDomainGroup + "/" + ComputeDomainVersion,
					APIResources: []metav1.APIResource{
						{Name: "resourceclaims"},
						{Name: ComputeDomainResource},
						{Name: "resourceclaimtemplates"},
					},
				},
			},
			expectError: false,
		},
		{
			name:        "empty resources - error",
			resources:   []*metav1.APIResourceList{},
			expectError: true,
			errorMsg:    "MNNVL is enabled but ComputeDomain CRD",
		},
		{
			name:        "nil resources - error",
			resources:   nil,
			expectError: true,
			errorMsg:    "MNNVL is enabled but ComputeDomain CRD",
		},
		{
			name: "wrong resource name - error",
			resources: []*metav1.APIResourceList{
				{
					GroupVersion: ComputeDomainGroup + "/" + ComputeDomainVersion,
					APIResources: []metav1.APIResource{
						{Name: "wrongresource"},
					},
				},
			},
			expectError: true,
			errorMsg:    "MNNVL is enabled but ComputeDomain CRD",
		},
		{
			name: "wrong API group - error",
			resources: []*metav1.APIResourceList{
				{
					GroupVersion: "wrong.group/v1beta1",
					APIResources: []metav1.APIResource{
						{Name: ComputeDomainResource},
					},
				},
			},
			expectError: true,
			errorMsg:    "MNNVL is enabled but ComputeDomain CRD",
		},
		{
			name: "wrong API version - error",
			resources: []*metav1.APIResourceList{
				{
					GroupVersion: ComputeDomainGroup + "/v2",
					APIResources: []metav1.APIResource{
						{Name: ComputeDomainResource},
					},
				},
			},
			expectError: true,
			errorMsg:    "MNNVL is enabled but ComputeDomain CRD",
		},
		{
			name:      "discovery error - returns error",
			resources: []*metav1.APIResourceList{},
			reactor: func(_ k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, errors.New("connection refused")
			},
			expectError: true,
			errorMsg:    "failed to discover API resources",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &k8stesting.Fake{
				Resources: test.resources,
			}
			fakeDiscovery := &fakediscovery.FakeDiscovery{Fake: fake}

			if test.reactor != nil {
				fake.AddReactor("*", "*", test.reactor)
			}

			err := validateComputeDomainCRD(fakeDiscovery)

			if test.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), test.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

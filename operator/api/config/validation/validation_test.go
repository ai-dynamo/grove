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

package validation

import (
	"testing"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestValidateClusterTopologyConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		config      configv1alpha1.ClusterTopologyConfiguration
		expectError bool
		errorField  string
	}{
		{
			name: "valid: enabled with levels",
			config: configv1alpha1.ClusterTopologyConfiguration{
				Enabled: true,
			},
			expectError: false,
		},
		{
			name: "valid: disabled with no levels",
			config: configv1alpha1.ClusterTopologyConfiguration{
				Enabled: false,
			},
			expectError: false,
		},
		{
			name: "valid: disabled with levels",
			config: configv1alpha1.ClusterTopologyConfiguration{
				Enabled: false,
			},
			expectError: false,
		},
		{
			name: "invalid: enabled with empty levels",
			config: configv1alpha1.ClusterTopologyConfiguration{
				Enabled: true,
			},
			expectError: true,
			errorField:  "clusterTopology.levels",
		},
		{
			name: "invalid: enabled with empty levels",
			config: configv1alpha1.ClusterTopologyConfiguration{
				Enabled: true,
			},
			expectError: true,
			errorField:  "clusterTopology.levels",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateClusterTopologyConfiguration(tt.config, field.NewPath("clusterTopology"))

			if tt.expectError {
				assert.NotEmpty(t, errs, "expected validation errors but got none")
				if len(errs) > 0 {
					assert.Equal(t, tt.errorField, errs[0].Field)
				}
			} else {
				assert.Empty(t, errs, "expected no validation errors but got: %v", errs)
			}
		})
	}
}

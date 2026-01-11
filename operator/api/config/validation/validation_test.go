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
	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestValidateTopologyAwareSchedulingConfiguration(t *testing.T) {
	tests := []struct {
		name           string
		config         configv1alpha1.TopologyAwareSchedulingConfiguration
		expectErrors   int
		expectedFields []string
		expectedTypes  []field.ErrorType
	}{
		{
			name: "valid: disabled with no levels",
			config: configv1alpha1.TopologyAwareSchedulingConfiguration{
				Enabled: false,
			},
			expectErrors: 0,
		},
		{
			name: "valid: disabled with levels (levels are ignored when disabled)",
			config: configv1alpha1.TopologyAwareSchedulingConfiguration{
				Enabled: false,
				Levels: []corev1alpha1.TopologyLevel{
					{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
				},
			},
			expectErrors: 0,
		},
		{
			name: "valid: enabled with single level",
			config: configv1alpha1.TopologyAwareSchedulingConfiguration{
				Enabled: true,
				Levels: []corev1alpha1.TopologyLevel{
					{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
				},
			},
			expectErrors: 0,
		},
		{
			name: "valid: enabled with multiple levels",
			config: configv1alpha1.TopologyAwareSchedulingConfiguration{
				Enabled: true,
				Levels: []corev1alpha1.TopologyLevel{
					{Domain: corev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/region"},
					{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
					{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
				},
			},
			expectErrors: 0,
		},
		{
			name: "valid: enabled with all supported domains",
			config: configv1alpha1.TopologyAwareSchedulingConfiguration{
				Enabled: true,
				Levels: []corev1alpha1.TopologyLevel{
					{Domain: corev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/region"},
					{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
					{Domain: corev1alpha1.TopologyDomainDataCenter, Key: "topology.kubernetes.io/datacenter"},
					{Domain: corev1alpha1.TopologyDomainBlock, Key: "topology.kubernetes.io/block"},
					{Domain: corev1alpha1.TopologyDomainRack, Key: "topology.kubernetes.io/rack"},
					{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
					{Domain: corev1alpha1.TopologyDomainNuma, Key: "topology.kubernetes.io/numa"},
				},
			},
			expectErrors: 0,
		},
		{
			name: "invalid: enabled with empty levels",
			config: configv1alpha1.TopologyAwareSchedulingConfiguration{
				Enabled: true,
				Levels:  []corev1alpha1.TopologyLevel{},
			},
			expectErrors:   1,
			expectedFields: []string{"clusterTopology.levels"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeRequired},
		},
		{
			name: "invalid: enabled with nil levels",
			config: configv1alpha1.TopologyAwareSchedulingConfiguration{
				Enabled: true,
				Levels:  nil,
			},
			expectErrors:   1,
			expectedFields: []string{"clusterTopology.levels"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeRequired},
		},
		{
			name: "invalid: unsupported domain",
			config: configv1alpha1.TopologyAwareSchedulingConfiguration{
				Enabled: true,
				Levels: []corev1alpha1.TopologyLevel{
					{Domain: "invalid-domain", Key: "some.key"},
				},
			},
			expectErrors:   1,
			expectedFields: []string{"clusterTopology.levels[0].domain"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeInvalid},
		},
		{
			name: "invalid: duplicate domains",
			config: configv1alpha1.TopologyAwareSchedulingConfiguration{
				Enabled: true,
				Levels: []corev1alpha1.TopologyLevel{
					{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
					{Domain: corev1alpha1.TopologyDomainZone, Key: "another.zone.key"},
				},
			},
			expectErrors:   1,
			expectedFields: []string{"clusterTopology.levels[1].domain"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeDuplicate},
		},
		{
			name: "invalid: duplicate keys",
			config: configv1alpha1.TopologyAwareSchedulingConfiguration{
				Enabled: true,
				Levels: []corev1alpha1.TopologyLevel{
					{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
					{Domain: corev1alpha1.TopologyDomainHost, Key: "topology.kubernetes.io/zone"},
				},
			},
			expectErrors:   1,
			expectedFields: []string{"clusterTopology.levels[1].key"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeDuplicate},
		},
		{
			name: "invalid: duplicate domains and keys",
			config: configv1alpha1.TopologyAwareSchedulingConfiguration{
				Enabled: true,
				Levels: []corev1alpha1.TopologyLevel{
					{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
					{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
				},
			},
			expectErrors:   2,
			expectedFields: []string{"clusterTopology.levels[1].domain", "clusterTopology.levels[1].key"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeDuplicate, field.ErrorTypeDuplicate},
		},
		{
			name: "invalid: multiple unsupported domains",
			config: configv1alpha1.TopologyAwareSchedulingConfiguration{
				Enabled: true,
				Levels: []corev1alpha1.TopologyLevel{
					{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
					{Domain: "invalid1", Key: "key1"},
					{Domain: "invalid2", Key: "key2"},
				},
			},
			expectErrors:   2,
			expectedFields: []string{"clusterTopology.levels[1].domain", "clusterTopology.levels[2].domain"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeInvalid, field.ErrorTypeInvalid},
		},
		{
			name: "invalid: multiple validation errors - unsupported domain and duplicates",
			config: configv1alpha1.TopologyAwareSchedulingConfiguration{
				Enabled: true,
				Levels: []corev1alpha1.TopologyLevel{
					{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
					{Domain: "invalid", Key: "invalid.key"},
					{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
				},
			},
			expectErrors:   3,
			expectedFields: []string{"clusterTopology.levels[1].domain", "clusterTopology.levels[2].domain", "clusterTopology.levels[2].key"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeInvalid, field.ErrorTypeDuplicate, field.ErrorTypeDuplicate},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errs := validateTopologyAwareSchedulingConfig(test.config, field.NewPath("clusterTopology"))

			assert.Len(t, errs, test.expectErrors, "expected %d validation errors but got %d: %v", test.expectErrors, len(errs), errs)

			if test.expectErrors > 0 {
				// Verify each expected error
				for i, expectedField := range test.expectedFields {
					assert.Equal(t, expectedField, errs[i].Field, "error %d: expected field %s but got %s", i, expectedField, errs[i].Field)
					if i < len(test.expectedTypes) {
						assert.Equal(t, test.expectedTypes[i], errs[i].Type, "error %d: expected type %s but got %s", i, test.expectedTypes[i], errs[i].Type)
					}
				}
			}
		})
	}
}

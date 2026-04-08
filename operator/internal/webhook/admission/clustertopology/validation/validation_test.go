// /*
// Copyright 2026 The Grove Authors.
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
	"context"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func newTestClusterTopology(levels []grovecorev1alpha1.TopologyLevel) *grovecorev1alpha1.ClusterTopology {
	return &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-topology",
		},
		Spec: grovecorev1alpha1.ClusterTopologySpec{
			Levels: levels,
		},
	}
}

func TestValidateCreate(t *testing.T) {
	tests := []struct {
		name        string
		levels      []grovecorev1alpha1.TopologyLevel
		expectError bool
		errContains string
	}{
		{
			name: "valid unique levels",
			levels: []grovecorev1alpha1.TopologyLevel{
				{Domain: grovecorev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/region"},
				{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
				{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
			},
			expectError: false,
		},
		{
			name: "single level",
			levels: []grovecorev1alpha1.TopologyLevel{
				{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
			},
			expectError: false,
		},
		{
			name: "duplicate domain",
			levels: []grovecorev1alpha1.TopologyLevel{
				{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
				{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "kubernetes.io/hostname"},
			},
			expectError: true,
			errContains: "spec.levels[1].domain",
		},
		{
			name: "duplicate key",
			levels: []grovecorev1alpha1.TopologyLevel{
				{Domain: grovecorev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/zone"},
				{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
			},
			expectError: true,
			errContains: "spec.levels[1].key",
		},
		{
			name: "duplicate both domain and key",
			levels: []grovecorev1alpha1.TopologyLevel{
				{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
				{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
			},
			expectError: true,
			errContains: "spec.levels[1].domain",
		},
	}

	handler := &Handler{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ct := newTestClusterTopology(tc.levels)
			_, err := handler.ValidateCreate(context.Background(), ct)
			if tc.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateCreate_InvalidObject(t *testing.T) {
	handler := &Handler{}
	_, err := handler.ValidateCreate(context.Background(), &runtime.Unknown{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected a ClusterTopology object")
}

func TestValidateUpdate_Valid(t *testing.T) {
	handler := &Handler{}
	oldCT := newTestClusterTopology([]grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/region"},
	})
	newCT := newTestClusterTopology([]grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/region"},
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
	})

	_, err := handler.ValidateUpdate(context.Background(), oldCT, newCT)
	assert.NoError(t, err)
}

func TestValidateUpdate_DuplicateDomain(t *testing.T) {
	handler := &Handler{}
	oldCT := newTestClusterTopology([]grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/region"},
	})
	newCT := newTestClusterTopology([]grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "kubernetes.io/hostname"},
	})

	_, err := handler.ValidateUpdate(context.Background(), oldCT, newCT)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.levels[1].domain")
}

func TestValidateDelete(t *testing.T) {
	handler := &Handler{}
	ct := newTestClusterTopology([]grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	})

	_, err := handler.ValidateDelete(context.Background(), ct)
	assert.NoError(t, err)
}

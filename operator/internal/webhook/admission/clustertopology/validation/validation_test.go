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

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func newTestHandler() *Handler {
	return &Handler{
		enabledBackends: map[string]struct{}{
			string(configv1alpha1.SchedulerNameKai):  {},
			string(configv1alpha1.SchedulerNameKube): {},
		},
		topologyAwareBackends: map[string]struct{}{
			string(configv1alpha1.SchedulerNameKai): {},
		},
	}
}

func newTestClusterTopology(
	levels []grovecorev1alpha1.TopologyLevel,
	refs []grovecorev1alpha1.SchedulerTopologyReference,
) *grovecorev1alpha1.ClusterTopology {
	return &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-topology",
		},
		Spec: grovecorev1alpha1.ClusterTopologySpec{
			Levels:                      levels,
			SchedulerTopologyReferences: refs,
		},
	}
}

func TestValidateCreate(t *testing.T) {
	tests := []struct {
		name        string
		levels      []grovecorev1alpha1.TopologyLevel
		refs        []grovecorev1alpha1.SchedulerTopologyReference
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
			name: "valid scheduler topology reference",
			levels: []grovecorev1alpha1.TopologyLevel{
				{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
			},
			refs: []grovecorev1alpha1.SchedulerTopologyReference{
				{SchedulerName: string(configv1alpha1.SchedulerNameKai), TopologyReference: "kai-topology"},
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
		{
			name: "duplicate scheduler backend reference",
			levels: []grovecorev1alpha1.TopologyLevel{
				{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
			},
			refs: []grovecorev1alpha1.SchedulerTopologyReference{
				{SchedulerName: string(configv1alpha1.SchedulerNameKai), TopologyReference: "kai-topology-a"},
				{SchedulerName: string(configv1alpha1.SchedulerNameKai), TopologyReference: "kai-topology-b"},
			},
			expectError: true,
			errContains: "spec.schedulerTopologyReferences[1].schedulerName",
		},
		{
			name: "unknown scheduler backend reference",
			levels: []grovecorev1alpha1.TopologyLevel{
				{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
			},
			refs: []grovecorev1alpha1.SchedulerTopologyReference{
				{SchedulerName: "unknown-scheduler", TopologyReference: "topology"},
			},
			expectError: true,
			errContains: "scheduler backend is not enabled in Grove",
		},
		{
			name: "non topology aware scheduler backend reference",
			levels: []grovecorev1alpha1.TopologyLevel{
				{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
			},
			refs: []grovecorev1alpha1.SchedulerTopologyReference{
				{SchedulerName: string(configv1alpha1.SchedulerNameKube), TopologyReference: "kube-topology"},
			},
			expectError: true,
			errContains: "scheduler backend does not implement topology-aware scheduling",
		},
	}

	handler := newTestHandler()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ct := newTestClusterTopology(tc.levels, tc.refs)
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
	handler := newTestHandler()
	_, err := handler.ValidateCreate(context.Background(), &runtime.Unknown{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected a ClusterTopology object")
}

func TestValidateUpdate_Valid(t *testing.T) {
	handler := newTestHandler()
	oldCT := newTestClusterTopology([]grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/region"},
	}, nil)
	newCT := newTestClusterTopology([]grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/region"},
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
	}, []grovecorev1alpha1.SchedulerTopologyReference{
		{SchedulerName: string(configv1alpha1.SchedulerNameKai), TopologyReference: "kai-topology"},
	})

	_, err := handler.ValidateUpdate(context.Background(), oldCT, newCT)
	assert.NoError(t, err)
}

func TestValidateUpdate_DuplicateDomain(t *testing.T) {
	handler := newTestHandler()
	oldCT := newTestClusterTopology([]grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/region"},
	}, nil)
	newCT := newTestClusterTopology([]grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "kubernetes.io/hostname"},
	}, nil)

	_, err := handler.ValidateUpdate(context.Background(), oldCT, newCT)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.levels[1].domain")
}

func TestValidateUpdate_UnknownSchedulerBackend(t *testing.T) {
	handler := newTestHandler()
	oldCT := newTestClusterTopology([]grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/region"},
	}, nil)
	newCT := newTestClusterTopology([]grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/region"},
	}, []grovecorev1alpha1.SchedulerTopologyReference{
		{SchedulerName: "unknown-scheduler", TopologyReference: "topology"},
	})

	_, err := handler.ValidateUpdate(context.Background(), oldCT, newCT)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scheduler backend is not enabled in Grove")
}

func TestValidateDelete(t *testing.T) {
	handler := newTestHandler()
	ct := newTestClusterTopology([]grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}, nil)

	_, err := handler.ValidateDelete(context.Background(), ct)
	assert.NoError(t, err)
}

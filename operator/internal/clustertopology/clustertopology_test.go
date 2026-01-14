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

package clustertopology

import (
	"context"
	"errors"
	"testing"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	kaitopologyv1alpha1 "github.com/NVIDIA/KAI-scheduler/pkg/apis/kai/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const topologyName = "test-topology"

func TestEnsureClusterTopologyWhenNonExists(t *testing.T) {
	// Setup
	ctx := context.Background()
	cl := testutils.CreateDefaultFakeClient(nil)
	logger := logr.Discard()

	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}

	// Test
	topology, err := ensureClusterTopology(ctx, cl, logger, topologyName, topologyLevels)

	// Assert
	require.NoError(t, err)
	assert.NotNil(t, topology)
	assert.Equal(t, topologyName, topology.Name)
	assert.Equal(t, topologyLevels, topology.Spec.Levels)

	// Verify it was actually created
	fetched := &grovecorev1alpha1.ClusterTopology{}
	err = cl.Get(ctx, client.ObjectKey{Name: topologyName}, fetched)
	require.NoError(t, err)
	assert.Equal(t, topologyLevels, fetched.Spec.Levels)
}

func TestEnsureClusterTopologyHostDomainKeyNotOverridden(t *testing.T) {
	// Setup
	ctx := context.Background()
	cl := testutils.CreateDefaultFakeClient(nil)
	logger := logr.Discard()

	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "custom-hostname-key"},
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: corev1.LabelTopologyZone},
	}

	// Test
	topology, err := ensureClusterTopology(ctx, cl, logger, topologyName, topologyLevels)

	// Assert
	require.NoError(t, err)
	assert.NotNil(t, topology)
	assert.Equal(t, topologyName, topology.Name)
	// Host domain key should not be overridden
	grovecorev1alpha1.SortTopologyLevels(topologyLevels)
	assert.Equal(t, topologyLevels, topology.Spec.Levels)

	// Verify it was actually created
	fetched := &grovecorev1alpha1.ClusterTopology{}
	err = cl.Get(ctx, client.ObjectKey{Name: topologyName}, fetched)
	require.NoError(t, err)
	assert.Equal(t, topologyLevels, fetched.Spec.Levels)
}

func TestEnsureClusterTopologyWhenUpdated(t *testing.T) {
	// Setup
	ctx := context.Background()
	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	// Create initial topology
	existing := &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
		},
		Spec: grovecorev1alpha1.ClusterTopologySpec{
			Levels: topologyLevels,
		},
	}
	cl := testutils.CreateDefaultFakeClient([]client.Object{existing})
	logger := logr.Discard()

	// Update with new levels and test
	updatedTopologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/region"},
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	topology, err := ensureClusterTopology(ctx, cl, logger, topologyName, updatedTopologyLevels)

	// Assert
	require.NoError(t, err)
	assert.NotNil(t, topology)
	assert.Equal(t, updatedTopologyLevels, topology.Spec.Levels)

	// Verify it was actually updated
	fetched := &grovecorev1alpha1.ClusterTopology{}
	err = cl.Get(ctx, client.ObjectKey{Name: topologyName}, fetched)
	require.NoError(t, err)
	assert.Equal(t, updatedTopologyLevels, fetched.Spec.Levels)
}

func TestEnsureClusterTopologyWhenNoUpdate(t *testing.T) {
	// Setup
	ctx := context.Background()
	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	existing := &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name:            topologyName,
			ResourceVersion: "1",
		},
		Spec: grovecorev1alpha1.ClusterTopologySpec{
			Levels: topologyLevels,
		},
	}
	cl := testutils.CreateDefaultFakeClient([]client.Object{existing})
	logger := logr.Discard()

	// Test
	topology, err := ensureClusterTopology(ctx, cl, logger, topologyName, topologyLevels)
	// Assert
	require.NoError(t, err)
	assert.NotNil(t, topology)
	assert.Equal(t, topologyLevels, topology.Spec.Levels)
	// Resource version should remain the same (no update occurred)
	assert.Equal(t, "1", topology.ResourceVersion)
}

func TestEnsureClusterTopologyWhenCreateError(t *testing.T) {
	ctx := context.Background()
	logger := logr.Discard()
	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	// Create a client that will fail on create
	createErr := apierrors.NewInternalError(assert.AnError)
	cl := testutils.NewTestClientBuilder().
		RecordErrorForObjects(testutils.ClientMethodCreate, createErr, client.ObjectKey{Name: topologyName}).
		Build()

	topology, err := ensureClusterTopology(ctx, cl, logger, topologyName, topologyLevels)
	assert.Error(t, err)
	assert.Nil(t, topology)
	assert.True(t, errors.Is(err, createErr))
}

func TestEnsureClusterTopologyWhenUpdateError(t *testing.T) {
	// Setup
	ctx := context.Background()
	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	existing := &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
		},
		Spec: grovecorev1alpha1.ClusterTopologySpec{
			Levels: topologyLevels,
		},
	}
	// Create a client that will fail on update
	updateErr := apierrors.NewInternalError(assert.AnError)
	cl := testutils.NewTestClientBuilder().
		WithObjects(existing).
		RecordErrorForObjects(testutils.ClientMethodUpdate, updateErr, client.ObjectKey{Name: topologyName}).
		Build()
	logger := logr.Discard()
	// Test
	updatedTopologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/region"},
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	topology, err := ensureClusterTopology(ctx, cl, logger, topologyName, updatedTopologyLevels)
	assert.Error(t, err)
	assert.Nil(t, topology)
	assert.True(t, errors.Is(err, updateErr))
}

func TestEnsureClusterTopologyWhenGetError(t *testing.T) {
	ctx := context.Background()
	logger := logr.Discard()

	// Create a client that will fail on get (non-NotFound error)
	getErr := apierrors.NewInternalError(assert.AnError)
	cl := testutils.NewTestClientBuilder().
		RecordErrorForObjects(testutils.ClientMethodGet, getErr, client.ObjectKey{Name: topologyName}).
		Build()

	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	topology, err := ensureClusterTopology(ctx, cl, logger, topologyName, topologyLevels)
	assert.Error(t, err)
	assert.Nil(t, topology)
	assert.Contains(t, err.Error(), "failed to get ClusterTopology")
}

func TestEnsureKAITopologyWhenNonExists(t *testing.T) {
	// Setup
	ctx := context.Background()
	cl := testutils.CreateDefaultFakeClient(nil)
	logger := logr.Discard()

	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainRack, Key: "topology.kubernetes.io/rack"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	clusterTopology := createTestClusterTopology(topologyName, topologyLevels)

	// Test
	err := ensureKAITopology(ctx, cl, logger, topologyName, clusterTopology)
	require.NoError(t, err)

	// Verify KAI Topology was created
	kaiTopology := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(ctx, client.ObjectKey{Name: topologyName}, kaiTopology)
	require.NoError(t, err)
	assert.Equal(t, topologyName, kaiTopology.Name)
	assert.Len(t, kaiTopology.Spec.Levels, 3)
	assert.Equal(t, "topology.kubernetes.io/zone", kaiTopology.Spec.Levels[0].NodeLabel)
	assert.Equal(t, "topology.kubernetes.io/rack", kaiTopology.Spec.Levels[1].NodeLabel)
	assert.Equal(t, "kubernetes.io/hostname", kaiTopology.Spec.Levels[2].NodeLabel)

	// Verify owner reference is set
	assert.True(t, metav1.IsControlledBy(kaiTopology, clusterTopology))
}

func TestEnsureKAITopologyWhenClusterTopologyUpdated(t *testing.T) {
	// Setup
	ctx := context.Background()
	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	clusterTopology := createTestClusterTopology(topologyName, topologyLevels)

	// Create existing KAI Topology with old levels
	existingKAITopology := createTestKAITopology(topologyName, []kaitopologyv1alpha1.TopologyLevel{
		{NodeLabel: "kubernetes.io/hostname"},
	}, clusterTopology)

	cl := testutils.CreateDefaultFakeClient([]client.Object{clusterTopology, existingKAITopology})
	logger := logr.Discard()

	err := ensureKAITopology(ctx, cl, logger, topologyName, clusterTopology)
	require.NoError(t, err)

	// Verify KAI Topology was recreated with new levels
	kaiTopology := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(ctx, client.ObjectKey{Name: topologyName}, kaiTopology)
	require.NoError(t, err)
	// Since it is recreated, generation should be "1" thus testing that the existing resource is not updated.
	assert.Equal(t, int64(0), kaiTopology.Generation)
	assert.Len(t, kaiTopology.Spec.Levels, 2)
	assert.Equal(t, "topology.kubernetes.io/zone", kaiTopology.Spec.Levels[0].NodeLabel)
	assert.Equal(t, "kubernetes.io/hostname", kaiTopology.Spec.Levels[1].NodeLabel)
}

func TestEnsureKAITopologyWhenNoChangeRequired(t *testing.T) {
	// Setup
	ctx := context.Background()
	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	clusterTopology := createTestClusterTopology(topologyName, topologyLevels)

	// Create existing KAI Topology with same levels
	existingKAITopology := createTestKAITopology(topologyName, convertToKAITopologyLevels(topologyLevels), clusterTopology)
	existingKAITopology.ResourceVersion = "1"

	cl := testutils.CreateDefaultFakeClient([]client.Object{clusterTopology, existingKAITopology})
	logger := logr.Discard()

	err := ensureKAITopology(ctx, cl, logger, topologyName, clusterTopology)
	require.NoError(t, err)

	// Verify KAI Topology was NOT recreated (resource version should be same)
	kaiTopology := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(ctx, client.ObjectKey{Name: topologyName}, kaiTopology)
	require.NoError(t, err)
	assert.Equal(t, int64(0), kaiTopology.Generation)
	assert.Equal(t, "1", kaiTopology.ResourceVersion)
}

func TestEnsureKAITopologyWithUnknownOwner(t *testing.T) {
	// Setup
	ctx := context.Background()
	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	clusterTopology := createTestClusterTopology(topologyName, topologyLevels)

	// Create existing KAI Topology owned by different ClusterTopology
	differentOwner := createTestClusterTopology("different-owner", topologyLevels)
	existingKAITopology := createTestKAITopology(topologyName, []kaitopologyv1alpha1.TopologyLevel{
		{NodeLabel: "topology.kubernetes.io/zone"},
	}, differentOwner)

	cl := testutils.CreateDefaultFakeClient([]client.Object{clusterTopology, existingKAITopology})
	logger := logr.Discard()
	// Test and Assert
	err := ensureKAITopology(ctx, cl, logger, topologyName, clusterTopology)
	assert.Error(t, err)
}

func TestEnsureKAITopologyWhenGetError(t *testing.T) {
	// Setup
	ctx := context.Background()
	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	clusterTopology := createTestClusterTopology(topologyName, topologyLevels)

	// Create a client that will fail on get (non-NotFound error)
	getErr := apierrors.NewInternalError(assert.AnError)
	cl := testutils.NewTestClientBuilder().
		WithObjects(clusterTopology).
		RecordErrorForObjects(testutils.ClientMethodGet, getErr, client.ObjectKey{Name: topologyName}).
		Build()
	logger := logr.Discard()
	// Test
	err := ensureKAITopology(ctx, cl, logger, topologyName, clusterTopology)
	// Assert
	assert.Error(t, err)
	assert.True(t, errors.Is(err, getErr))
}

func TestEnsureKAITopologyWhenCreateError(t *testing.T) {
	// Setup
	ctx := context.Background()
	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	clusterTopology := createTestClusterTopology(topologyName, topologyLevels)

	// Create a client that will fail on create
	createErr := apierrors.NewInternalError(assert.AnError)
	cl := testutils.NewTestClientBuilder().
		WithObjects(clusterTopology).
		RecordErrorForObjects(testutils.ClientMethodCreate, createErr, client.ObjectKey{Name: topologyName}).
		Build()
	logger := logr.Discard()

	// Test
	err := ensureKAITopology(ctx, cl, logger, topologyName, clusterTopology)
	// Assert
	assert.Error(t, err)
	assert.True(t, errors.Is(err, createErr))
}

func TestEnsureKAITopologyWhenDeleteError(t *testing.T) {
	// Setup
	ctx := context.Background()
	// New topology levels that ClusterTopology will have
	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	clusterTopology := createTestClusterTopology(topologyName, topologyLevels)

	// Create existing KAI Topology with OLD/DIFFERENT levels to trigger delete-and-recreate
	existingKAITopology := createTestKAITopology(topologyName, []kaitopologyv1alpha1.TopologyLevel{
		{NodeLabel: "old-label"}, // Different from ClusterTopology levels
	}, clusterTopology)

	// Create a client that will fail on delete
	deleteErr := apierrors.NewInternalError(assert.AnError)
	cl := testutils.NewTestClientBuilder().
		WithObjects(clusterTopology, existingKAITopology).
		RecordErrorForObjects(testutils.ClientMethodDelete, deleteErr, client.ObjectKey{Name: topologyName}).
		Build()
	logger := logr.Discard()

	// Test
	err := ensureKAITopology(ctx, cl, logger, topologyName, clusterTopology)
	// Assert
	assert.Error(t, err)
	assert.True(t, errors.Is(err, deleteErr))
}

func TestEnsureKAITopologyWhenCreateFailsDuringRecreate(t *testing.T) {
	// Setup
	ctx := context.Background()
	updatedTopologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	clusterTopology := createTestClusterTopology(topologyName, updatedTopologyLevels)

	// Create existing KAI Topology with old levels
	existingKAITopology := createTestKAITopology(topologyName, []kaitopologyv1alpha1.TopologyLevel{
		{NodeLabel: "kubernetes.io/hostname"},
	}, clusterTopology)

	// Create a client that succeeds when deleting KAI Topology resource but fail on create
	// This simulates the scenario where delete succeeds but create fails during recreation
	createErr := apierrors.NewInternalError(assert.AnError)
	cl := testutils.NewTestClientBuilder().
		WithObjects(clusterTopology, existingKAITopology).
		RecordErrorForObjects(testutils.ClientMethodCreate, createErr, client.ObjectKey{Name: topologyName}).
		Build()
	logger := logr.Discard()

	err := ensureKAITopology(ctx, cl, logger, topologyName, clusterTopology)
	assert.Error(t, err)
	// After delete succeeds, create will fail, so we get the create error message
	assert.True(t, errors.Is(err, createErr))
}

func TestDeleteClusterTopology(t *testing.T) {
	// Setup
	ctx := context.Background()
	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	existing := &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
		},
		Spec: grovecorev1alpha1.ClusterTopologySpec{
			Levels: topologyLevels,
		},
	}
	cl := testutils.CreateDefaultFakeClient([]client.Object{existing})

	// Test
	err := deleteClusterTopology(ctx, cl, topologyName)
	// Assert
	require.NoError(t, err)
	// Verify it was deleted
	fetched := &grovecorev1alpha1.ClusterTopology{}
	err = cl.Get(ctx, client.ObjectKey{Name: topologyName}, fetched)
	assert.True(t, apierrors.IsNotFound(err))
}

func TestDeleteClusterTopologyWhenNoneExists(t *testing.T) {
	// Setup
	ctx := context.Background()
	cl := testutils.CreateDefaultFakeClient(nil)
	// Test and Assert
	// Should not error when deleting non-existent topology
	err := deleteClusterTopology(ctx, cl, topologyName)
	assert.NoError(t, err)
}

func TestDeleteClusterTopologyError(t *testing.T) {
	ctx := context.Background()
	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	existing := &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
		},
		Spec: grovecorev1alpha1.ClusterTopologySpec{
			Levels: topologyLevels,
		},
	}

	// Create a client that will fail on delete (non-NotFound error)
	deleteErr := apierrors.NewInternalError(assert.AnError)
	cl := testutils.NewTestClientBuilder().
		WithObjects(existing).
		RecordErrorForObjects(testutils.ClientMethodDelete, deleteErr, client.ObjectKey{Name: topologyName}).
		Build()

	err := deleteClusterTopology(ctx, cl, topologyName)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, deleteErr))
}

func TestBuildClusterTopology(t *testing.T) {
	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	topology := buildClusterTopology(topologyName, topologyLevels)
	assert.Equal(t, topologyName, topology.Name)
	assert.Equal(t, topologyLevels, topology.Spec.Levels)
}

func TestIsClusterTopologyChanged(t *testing.T) {
	tests := []struct {
		name            string
		oldLevels       []grovecorev1alpha1.TopologyLevel
		newLevels       []grovecorev1alpha1.TopologyLevel
		expectedChanged bool
	}{
		{
			name: "No change in levels",
			oldLevels: []grovecorev1alpha1.TopologyLevel{
				{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
				{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
			},
			newLevels: []grovecorev1alpha1.TopologyLevel{
				{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
				{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
			},
			expectedChanged: false,
		},
		{
			name: "Change in levels",
			oldLevels: []grovecorev1alpha1.TopologyLevel{
				{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
				{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
			},
			newLevels: []grovecorev1alpha1.TopologyLevel{
				{Domain: grovecorev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/region"},
			},
			expectedChanged: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			oldTopology := &grovecorev1alpha1.ClusterTopology{
				Spec: grovecorev1alpha1.ClusterTopologySpec{
					Levels: test.oldLevels,
				},
			}
			newTopology := &grovecorev1alpha1.ClusterTopology{
				Spec: grovecorev1alpha1.ClusterTopologySpec{
					Levels: test.newLevels,
				},
			}
			topologyChanged := isClusterTopologyChanged(oldTopology, newTopology)
			assert.Equal(t, test.expectedChanged, topologyChanged)
		})
	}
}

func TestBuildKAITopology(t *testing.T) {
	// Setup
	cl := testutils.CreateDefaultFakeClient(nil)
	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	clusterTopology := &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
			UID:  uuid.NewUUID(),
		},
		Spec: grovecorev1alpha1.ClusterTopologySpec{
			Levels: topologyLevels,
		},
	}

	// Test
	kaiTopology, err := buildKAITopology(topologyName, clusterTopology, cl.Scheme())
	// Assert
	require.NoError(t, err)
	assert.Equal(t, topologyName, kaiTopology.Name)
	assert.Len(t, kaiTopology.Spec.Levels, 2)
	assert.Equal(t, "topology.kubernetes.io/zone", kaiTopology.Spec.Levels[0].NodeLabel)
	assert.Equal(t, "kubernetes.io/hostname", kaiTopology.Spec.Levels[1].NodeLabel)
	assert.True(t, metav1.IsControlledBy(kaiTopology, clusterTopology))
}

func TestIsKAITopologyChanged(t *testing.T) {
	tests := []struct {
		name            string
		oldLevels       []kaitopologyv1alpha1.TopologyLevel
		newLevels       []kaitopologyv1alpha1.TopologyLevel
		expectedChanged bool
	}{
		{
			name: "No change in levels",
			oldLevels: []kaitopologyv1alpha1.TopologyLevel{
				{NodeLabel: "topology.kubernetes.io/zone"},
				{NodeLabel: "kubernetes.io/hostname"},
			},
			newLevels: []kaitopologyv1alpha1.TopologyLevel{
				{NodeLabel: "topology.kubernetes.io/zone"},
				{NodeLabel: "kubernetes.io/hostname"},
			},
			expectedChanged: false,
		},
		{
			name: "Change in levels",
			oldLevels: []kaitopologyv1alpha1.TopologyLevel{
				{NodeLabel: "topology.kubernetes.io/zone"},
				{NodeLabel: "kubernetes.io/hostname"},
			},
			newLevels: []kaitopologyv1alpha1.TopologyLevel{
				{NodeLabel: "topology.kubernetes.io/region"},
			},
			expectedChanged: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			oldTopology := &kaitopologyv1alpha1.Topology{
				Spec: kaitopologyv1alpha1.TopologySpec{
					Levels: test.oldLevels,
				},
			}
			newTopology := &kaitopologyv1alpha1.Topology{
				Spec: kaitopologyv1alpha1.TopologySpec{
					Levels: test.newLevels,
				},
			}
			topologyChanged := isKAITopologyChanged(oldTopology, newTopology)
			assert.Equal(t, test.expectedChanged, topologyChanged)
		})
	}
}

func TestSynchronizeTopologyWhenDisabled(t *testing.T) {
	// Setup
	ctx := context.Background()
	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	// Create existing ClusterTopology and KAI Topology
	existingClusterTopology := createTestClusterTopology(grovecorev1alpha1.DefaultClusterTopologyName, topologyLevels)
	existingKAITopology := createTestKAITopology(grovecorev1alpha1.DefaultClusterTopologyName, convertToKAITopologyLevels(topologyLevels), existingClusterTopology)

	cl := testutils.CreateDefaultFakeClient([]client.Object{existingClusterTopology, existingKAITopology})
	logger := logr.Discard()

	// Create configuration with topology disabled
	operatorCfg := &configv1alpha1.OperatorConfiguration{
		TopologyAwareScheduling: configv1alpha1.TopologyAwareSchedulingConfiguration{
			Enabled: false,
		},
	}

	// Test
	err := SynchronizeTopology(ctx, cl, logger, operatorCfg)

	// Assert
	require.NoError(t, err)

	// Verify ClusterTopology was deleted
	fetchedClusterTopology := &grovecorev1alpha1.ClusterTopology{}
	err = cl.Get(ctx, client.ObjectKey{Name: grovecorev1alpha1.DefaultClusterTopologyName}, fetchedClusterTopology)
	assert.True(t, apierrors.IsNotFound(err))

	// Note: In a real Kubernetes cluster, KAI Topology would be cascade-deleted due to owner reference,
	// but the fake client doesn't automatically handle cascade deletion. That's a Kubernetes controller behavior.
	// In the actual implementation, the KAI Topology will be deleted by the garbage collector.
}

func TestSynchronizeTopologyWhenDisabledAndNoResourcesExist(t *testing.T) {
	// Setup
	ctx := context.Background()
	cl := testutils.CreateDefaultFakeClient(nil)
	logger := logr.Discard()

	// Create configuration with topology disabled
	operatorCfg := &configv1alpha1.OperatorConfiguration{
		TopologyAwareScheduling: configv1alpha1.TopologyAwareSchedulingConfiguration{
			Enabled: false,
		},
	}

	// Test - should not error when deleting non-existent resources
	err := SynchronizeTopology(ctx, cl, logger, operatorCfg)

	// Assert
	require.NoError(t, err)
}

func TestSynchronizeTopologyWhenEnabledCreatesResources(t *testing.T) {
	// Setup
	ctx := context.Background()
	cl := testutils.CreateDefaultFakeClient(nil)
	logger := logr.Discard()

	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/region"},
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}

	// Create configuration with topology enabled
	operatorCfg := &configv1alpha1.OperatorConfiguration{
		TopologyAwareScheduling: configv1alpha1.TopologyAwareSchedulingConfiguration{
			Enabled: true,
			Levels:  topologyLevels,
		},
	}

	// Test
	err := SynchronizeTopology(ctx, cl, logger, operatorCfg)

	// Assert
	require.NoError(t, err)

	// Verify ClusterTopology was created
	fetchedClusterTopology := &grovecorev1alpha1.ClusterTopology{}
	err = cl.Get(ctx, client.ObjectKey{Name: grovecorev1alpha1.DefaultClusterTopologyName}, fetchedClusterTopology)
	require.NoError(t, err)
	assert.Equal(t, topologyLevels, fetchedClusterTopology.Spec.Levels)

	// Verify KAI Topology was created
	fetchedKAITopology := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(ctx, client.ObjectKey{Name: grovecorev1alpha1.DefaultClusterTopologyName}, fetchedKAITopology)
	require.NoError(t, err)
	assert.Len(t, fetchedKAITopology.Spec.Levels, 3)
	assert.Equal(t, "topology.kubernetes.io/region", fetchedKAITopology.Spec.Levels[0].NodeLabel)
	assert.Equal(t, "topology.kubernetes.io/zone", fetchedKAITopology.Spec.Levels[1].NodeLabel)
	assert.Equal(t, "kubernetes.io/hostname", fetchedKAITopology.Spec.Levels[2].NodeLabel)

	// Verify owner reference
	assert.True(t, metav1.IsControlledBy(fetchedKAITopology, fetchedClusterTopology))
}

func TestSynchronizeTopologyWhenEnabledUpdatesExistingResources(t *testing.T) {
	// Setup
	ctx := context.Background()
	oldTopologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	// Create existing ClusterTopology with old levels
	existingClusterTopology := createTestClusterTopology(grovecorev1alpha1.DefaultClusterTopologyName, oldTopologyLevels)
	// Create existing KAI Topology with old levels
	existingKAITopology := createTestKAITopology(grovecorev1alpha1.DefaultClusterTopologyName, convertToKAITopologyLevels(oldTopologyLevels), existingClusterTopology)

	cl := testutils.CreateDefaultFakeClient([]client.Object{existingClusterTopology, existingKAITopology})
	logger := logr.Discard()

	// Create configuration with updated topology levels
	newTopologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/region"},
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	operatorCfg := &configv1alpha1.OperatorConfiguration{
		TopologyAwareScheduling: configv1alpha1.TopologyAwareSchedulingConfiguration{
			Enabled: true,
			Levels:  newTopologyLevels,
		},
	}

	// Test
	err := SynchronizeTopology(ctx, cl, logger, operatorCfg)

	// Assert
	require.NoError(t, err)

	// Verify ClusterTopology was updated
	fetchedClusterTopology := &grovecorev1alpha1.ClusterTopology{}
	err = cl.Get(ctx, client.ObjectKey{Name: grovecorev1alpha1.DefaultClusterTopologyName}, fetchedClusterTopology)
	require.NoError(t, err)
	assert.Equal(t, newTopologyLevels, fetchedClusterTopology.Spec.Levels)

	// Verify KAI Topology was recreated with new levels
	fetchedKAITopology := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(ctx, client.ObjectKey{Name: grovecorev1alpha1.DefaultClusterTopologyName}, fetchedKAITopology)
	require.NoError(t, err)
	assert.Len(t, fetchedKAITopology.Spec.Levels, 3)
	assert.Equal(t, "topology.kubernetes.io/region", fetchedKAITopology.Spec.Levels[0].NodeLabel)
	assert.Equal(t, "topology.kubernetes.io/zone", fetchedKAITopology.Spec.Levels[1].NodeLabel)
	assert.Equal(t, "kubernetes.io/hostname", fetchedKAITopology.Spec.Levels[2].NodeLabel)
}

func TestSynchronizeTopologyWhenEnabledWithEmptyLevels(t *testing.T) {
	// Setup
	ctx := context.Background()
	cl := testutils.CreateDefaultFakeClient(nil)
	logger := logr.Discard()

	// Create configuration with no topology levels (should add host level automatically)
	operatorCfg := &configv1alpha1.OperatorConfiguration{
		TopologyAwareScheduling: configv1alpha1.TopologyAwareSchedulingConfiguration{
			Enabled: true,
			Levels:  []grovecorev1alpha1.TopologyLevel{},
		},
	}

	// Test
	err := SynchronizeTopology(ctx, cl, logger, operatorCfg)

	// Assert
	require.NoError(t, err)

	// Verify ClusterTopology was created with host level automatically added
	fetchedClusterTopology := &grovecorev1alpha1.ClusterTopology{}
	err = cl.Get(ctx, client.ObjectKey{Name: grovecorev1alpha1.DefaultClusterTopologyName}, fetchedClusterTopology)
	require.NoError(t, err)
	assert.Len(t, fetchedClusterTopology.Spec.Levels, 0)

	// Verify KAI Topology was created
	fetchedKAITopology := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(ctx, client.ObjectKey{Name: grovecorev1alpha1.DefaultClusterTopologyName}, fetchedKAITopology)
	require.NoError(t, err)
	assert.Len(t, fetchedKAITopology.Spec.Levels, 0)
}

func TestSynchronizeTopologyWhenClusterTopologyCreationFails(t *testing.T) {
	// Setup
	ctx := context.Background()
	logger := logr.Discard()

	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}

	// Create a client that will fail on creating ClusterTopology
	createErr := apierrors.NewInternalError(assert.AnError)
	cl := testutils.NewTestClientBuilder().
		RecordErrorForObjects(testutils.ClientMethodCreate, createErr, client.ObjectKey{Name: grovecorev1alpha1.DefaultClusterTopologyName}).
		Build()

	operatorCfg := &configv1alpha1.OperatorConfiguration{
		TopologyAwareScheduling: configv1alpha1.TopologyAwareSchedulingConfiguration{
			Enabled: true,
			Levels:  topologyLevels,
		},
	}

	// Test
	err := SynchronizeTopology(ctx, cl, logger, operatorCfg)

	// Assert
	assert.Error(t, err)
	assert.True(t, errors.Is(err, createErr))
}

func TestSynchronizeTopologyWhenClusterTopologyUpdateFails(t *testing.T) {
	// Setup
	ctx := context.Background()
	logger := logr.Discard()

	oldTopologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}

	// Create existing ClusterTopology
	existingClusterTopology := &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: grovecorev1alpha1.DefaultClusterTopologyName,
		},
		Spec: grovecorev1alpha1.ClusterTopologySpec{
			Levels: oldTopologyLevels,
		},
	}

	// Create a client that will fail when updating ClusterTopology
	updateErr := apierrors.NewInternalError(assert.AnError)
	cl := testutils.NewTestClientBuilder().
		WithObjects(existingClusterTopology).
		RecordErrorForObjects(testutils.ClientMethodUpdate, updateErr, client.ObjectKey{Name: grovecorev1alpha1.DefaultClusterTopologyName}).
		Build()

	// Try to update with new topology levels
	newTopologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}

	operatorCfg := &configv1alpha1.OperatorConfiguration{
		TopologyAwareScheduling: configv1alpha1.TopologyAwareSchedulingConfiguration{
			Enabled: true,
			Levels:  newTopologyLevels,
		},
	}

	// Test
	err := SynchronizeTopology(ctx, cl, logger, operatorCfg)

	// Assert
	assert.Error(t, err)
	assert.True(t, errors.Is(err, updateErr))
}

func TestSynchronizeTopologyWhenDeleteClusterTopologyFails(t *testing.T) {
	// Setup
	ctx := context.Background()

	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	// Create existing ClusterTopology
	existingClusterTopology := &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: grovecorev1alpha1.DefaultClusterTopologyName,
		},
		Spec: grovecorev1alpha1.ClusterTopologySpec{
			Levels: topologyLevels,
		},
	}

	// Create a client that will fail on delete
	deleteErr := apierrors.NewInternalError(assert.AnError)
	cl := testutils.NewTestClientBuilder().
		WithObjects(existingClusterTopology).
		RecordErrorForObjects(testutils.ClientMethodDelete, deleteErr, client.ObjectKey{Name: grovecorev1alpha1.DefaultClusterTopologyName}).
		Build()
	logger := logr.Discard()

	// Create configuration with topology disabled
	operatorCfg := &configv1alpha1.OperatorConfiguration{
		TopologyAwareScheduling: configv1alpha1.TopologyAwareSchedulingConfiguration{
			Enabled: false,
		},
	}

	// Test
	err := SynchronizeTopology(ctx, cl, logger, operatorCfg)

	// Assert
	assert.Error(t, err)
	assert.True(t, errors.Is(err, deleteErr))
}

func TestGetClusterTopologyLevels(t *testing.T) {
	tests := []struct {
		name              string
		clusterTopology   *grovecorev1alpha1.ClusterTopology
		topologyName      string
		getError          *apierrors.StatusError
		expectedLevels    []grovecorev1alpha1.TopologyLevel
		expectError       bool
		expectedErrorType error
	}{
		{
			name: "successfully retrieve topology levels",
			clusterTopology: &grovecorev1alpha1.ClusterTopology{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-topology",
				},
				Spec: grovecorev1alpha1.ClusterTopologySpec{
					Levels: []grovecorev1alpha1.TopologyLevel{
						{
							Domain: grovecorev1alpha1.TopologyDomainRegion,
							Key:    "topology.kubernetes.io/region",
						},
						{
							Domain: grovecorev1alpha1.TopologyDomainZone,
							Key:    "topology.kubernetes.io/zone",
						},
						{
							Domain: grovecorev1alpha1.TopologyDomainHost,
							Key:    "kubernetes.io/hostname",
						},
					},
				},
			},
			topologyName: "test-topology",
			expectedLevels: []grovecorev1alpha1.TopologyLevel{
				{
					Domain: grovecorev1alpha1.TopologyDomainRegion,
					Key:    "topology.kubernetes.io/region",
				},
				{
					Domain: grovecorev1alpha1.TopologyDomainZone,
					Key:    "topology.kubernetes.io/zone",
				},
				{
					Domain: grovecorev1alpha1.TopologyDomainHost,
					Key:    "kubernetes.io/hostname",
				},
			},
			expectError: false,
		},
		{
			name: "topology not found",
			clusterTopology: &grovecorev1alpha1.ClusterTopology{
				ObjectMeta: metav1.ObjectMeta{
					Name: "existing-topology",
				},
				Spec: grovecorev1alpha1.ClusterTopologySpec{
					Levels: []grovecorev1alpha1.TopologyLevel{
						{
							Domain: grovecorev1alpha1.TopologyDomainRegion,
							Key:    "topology.kubernetes.io/region",
						},
					},
				},
			},
			topologyName: "non-existent-topology",
			getError: apierrors.NewNotFound(
				schema.GroupResource{Group: apicommonconstants.OperatorGroupName, Resource: "clustertopologies"},
				"non-existent-topology",
			),
			expectError:       true,
			expectedErrorType: &apierrors.StatusError{},
		},
		{
			name: "client Get returns error",
			clusterTopology: &grovecorev1alpha1.ClusterTopology{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-topology",
				},
				Spec: grovecorev1alpha1.ClusterTopologySpec{
					Levels: []grovecorev1alpha1.TopologyLevel{
						{
							Domain: grovecorev1alpha1.TopologyDomainZone,
							Key:    "topology.kubernetes.io/zone",
						},
					},
				},
			},
			topologyName:      "test-topology",
			getError:          apierrors.NewInternalError(assert.AnError),
			expectError:       true,
			expectedErrorType: &apierrors.StatusError{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Create fake client
			var existingObjects []client.Object
			if test.clusterTopology != nil {
				existingObjects = append(existingObjects, test.clusterTopology)
			}
			clientBuilder := testutils.NewTestClientBuilder().WithObjects(existingObjects...)
			// Record error if specified
			if test.getError != nil {
				clientBuilder.RecordErrorForObjects(
					testutils.ClientMethodGet,
					test.getError,
					client.ObjectKey{Name: test.topologyName},
				)
			}
			fakeClient := clientBuilder.Build()

			// Call the function
			topologLevels, err := GetClusterTopologyLevels(context.Background(), fakeClient, test.topologyName)

			// Validate results
			if test.expectError {
				require.Error(t, err)
				if test.expectedErrorType != nil {
					assert.IsType(t, test.expectedErrorType, err)
				}
				assert.Nil(t, topologLevels)
			} else {
				require.NoError(t, err)
				assert.Equal(t, test.expectedLevels, topologLevels)
			}
		})
	}
}

// Helper functions for creating test resources
// --------------------------------------------------
// createTestClusterTopology creates a ClusterTopology with the given name and topology levels.
func createTestClusterTopology(name string, levels []grovecorev1alpha1.TopologyLevel) *grovecorev1alpha1.ClusterTopology {
	return &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			UID:  uuid.NewUUID(),
		},
		Spec: grovecorev1alpha1.ClusterTopologySpec{
			Levels: levels,
		},
	}
}

// createTestKAITopology creates a KAI Topology with the given name, levels, and owner reference to ClusterTopology.
func createTestKAITopology(name string, levels []kaitopologyv1alpha1.TopologyLevel, owner *grovecorev1alpha1.ClusterTopology) *kaitopologyv1alpha1.Topology {
	topology := &kaitopologyv1alpha1.Topology{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: kaitopologyv1alpha1.TopologySpec{
			Levels: levels,
		},
	}
	if owner != nil {
		topology.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion:         grovecorev1alpha1.SchemeGroupVersion.String(),
				Kind:               apicommonconstants.KindClusterTopology,
				Name:               owner.Name,
				UID:                owner.UID,
				Controller:         ptr.To(true),
				BlockOwnerDeletion: ptr.To(true),
			},
		}
	}
	return topology
}

// convertToKAITopologyLevels converts ClusterTopology levels to KAI Topology levels.
func convertToKAITopologyLevels(levels []grovecorev1alpha1.TopologyLevel) []kaitopologyv1alpha1.TopologyLevel {
	kaiLevels := make([]kaitopologyv1alpha1.TopologyLevel, len(levels))
	for i, level := range levels {
		kaiLevels[i] = kaitopologyv1alpha1.TopologyLevel{
			NodeLabel: level.Key,
		}
	}
	return kaiLevels
}

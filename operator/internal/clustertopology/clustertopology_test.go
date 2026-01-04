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
	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	kaitopologyv1alpha1 "github.com/NVIDIA/KAI-scheduler/pkg/apis/kai/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	topologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}

	// Test
	topology, err := EnsureClusterTopology(ctx, cl, logger, topologyName, topologyLevels)

	// Assert
	require.NoError(t, err)
	assert.NotNil(t, topology)
	assert.Equal(t, topologyName, topology.Name)
	assert.Equal(t, topologyLevels, topology.Spec.Levels)

	// Verify it was actually created
	fetched := &corev1alpha1.ClusterTopology{}
	err = cl.Get(ctx, client.ObjectKey{Name: topologyName}, fetched)
	require.NoError(t, err)
	assert.Equal(t, topologyLevels, fetched.Spec.Levels)
}

func TestEnsureClusterTopologyWhenUpdated(t *testing.T) {
	// Setup
	ctx := context.Background()
	topologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	// Create initial topology
	existing := &corev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
		},
		Spec: corev1alpha1.ClusterTopologySpec{
			Levels: topologyLevels,
		},
	}
	cl := testutils.CreateDefaultFakeClient([]client.Object{existing})
	logger := logr.Discard()

	// Update with new levels and test
	updatedTopologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/region"},
		{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	topology, err := EnsureClusterTopology(ctx, cl, logger, topologyName, updatedTopologyLevels)

	// Assert
	require.NoError(t, err)
	assert.NotNil(t, topology)
	assert.Equal(t, updatedTopologyLevels, topology.Spec.Levels)

	// Verify it was actually updated
	fetched := &corev1alpha1.ClusterTopology{}
	err = cl.Get(ctx, client.ObjectKey{Name: topologyName}, fetched)
	require.NoError(t, err)
	assert.Equal(t, updatedTopologyLevels, fetched.Spec.Levels)
}

func TestEnsureClusterTopologyWhenNoUpdate(t *testing.T) {
	// Setup
	ctx := context.Background()
	topologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	existing := &corev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name:            topologyName,
			ResourceVersion: "1",
		},
		Spec: corev1alpha1.ClusterTopologySpec{
			Levels: topologyLevels,
		},
	}
	cl := testutils.CreateDefaultFakeClient([]client.Object{existing})
	logger := logr.Discard()

	// Test
	topology, err := EnsureClusterTopology(ctx, cl, logger, topologyName, topologyLevels)
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
	topologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	// Create a client that will fail on create
	createErr := apierrors.NewInternalError(assert.AnError)
	cl := testutils.NewTestClientBuilder().
		RecordErrorForObjects(testutils.ClientMethodCreate, createErr, client.ObjectKey{Name: topologyName}).
		Build()

	topology, err := EnsureClusterTopology(ctx, cl, logger, topologyName, topologyLevels)
	assert.Error(t, err)
	assert.Nil(t, topology)
	assert.True(t, errors.Is(err, createErr))
}

func TestEnsureClusterTopologyWhenUpdateError(t *testing.T) {
	// Setup
	ctx := context.Background()
	topologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	existing := &corev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
		},
		Spec: corev1alpha1.ClusterTopologySpec{
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
	updatedTopologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/region"},
		{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	topology, err := EnsureClusterTopology(ctx, cl, logger, topologyName, updatedTopologyLevels)
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

	topologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	topology, err := EnsureClusterTopology(ctx, cl, logger, topologyName, topologyLevels)
	assert.Error(t, err)
	assert.Nil(t, topology)
	assert.Contains(t, err.Error(), "failed to get ClusterTopology")
}

func TestEnsureKAITopologyWhenNonExists(t *testing.T) {
	// Setup
	ctx := context.Background()
	cl := testutils.CreateDefaultFakeClient(nil)
	logger := logr.Discard()

	topologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: corev1alpha1.TopologyDomainRack, Key: "topology.kubernetes.io/rack"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	clusterTopology := &corev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
			UID:  uuid.NewUUID(),
		},
		Spec: corev1alpha1.ClusterTopologySpec{
			Levels: topologyLevels,
		},
	}

	// Test
	err := EnsureKAITopology(ctx, cl, logger, topologyName, clusterTopology)
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
	topologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	clusterTopology := &corev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
			UID:  uuid.NewUUID(),
		},
		Spec: corev1alpha1.ClusterTopologySpec{
			Levels: topologyLevels,
		},
	}

	// Create existing KAI Topology with old levels
	existingKAITopology := &kaitopologyv1alpha1.Topology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         corev1alpha1.SchemeGroupVersion.String(),
					Kind:               apicommonconstants.KindClusterTopology,
					Name:               clusterTopology.Name,
					UID:                clusterTopology.UID,
					Controller:         ptr.To(true),
					BlockOwnerDeletion: ptr.To(true),
				},
			},
		},
		Spec: kaitopologyv1alpha1.TopologySpec{
			Levels: []kaitopologyv1alpha1.TopologyLevel{
				{NodeLabel: "kubernetes.io/hostname"},
			},
		},
	}

	cl := testutils.CreateDefaultFakeClient([]client.Object{clusterTopology, existingKAITopology})
	logger := logr.Discard()

	err := EnsureKAITopology(ctx, cl, logger, topologyName, clusterTopology)
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
	topologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	clusterTopology := &corev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
			UID:  uuid.NewUUID(),
		},
		Spec: corev1alpha1.ClusterTopologySpec{
			Levels: topologyLevels,
		},
	}

	// Create existing KAI Topology with same levels
	existingKAITopology := &kaitopologyv1alpha1.Topology{
		ObjectMeta: metav1.ObjectMeta{
			Name:            topologyName,
			ResourceVersion: "1",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "grove.io/v1alpha1",
					Kind:               "ClusterTopology",
					Name:               clusterTopology.Name,
					UID:                clusterTopology.UID,
					Controller:         ptr.To(true),
					BlockOwnerDeletion: ptr.To(true),
				},
			},
		},
		Spec: kaitopologyv1alpha1.TopologySpec{
			Levels: []kaitopologyv1alpha1.TopologyLevel{
				{NodeLabel: "topology.kubernetes.io/zone"},
				{NodeLabel: "kubernetes.io/hostname"},
			},
		},
	}

	cl := testutils.CreateDefaultFakeClient([]client.Object{clusterTopology, existingKAITopology})
	logger := logr.Discard()

	err := EnsureKAITopology(ctx, cl, logger, topologyName, clusterTopology)
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
	topologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	clusterTopology := &corev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
			UID:  uuid.NewUUID(),
		},
		Spec: corev1alpha1.ClusterTopologySpec{
			Levels: topologyLevels,
		},
	}

	// Create existing KAI Topology owned by different ClusterTopology
	existingKAITopology := &kaitopologyv1alpha1.Topology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "grove.io/v1alpha1",
					Kind:               "ClusterTopology",
					Name:               "different-owner",
					UID:                "different-uid",
					Controller:         ptr.To(true),
					BlockOwnerDeletion: ptr.To(true),
				},
			},
		},
		Spec: kaitopologyv1alpha1.TopologySpec{
			Levels: []kaitopologyv1alpha1.TopologyLevel{
				{NodeLabel: "topology.kubernetes.io/zone"},
			},
		},
	}

	cl := testutils.CreateDefaultFakeClient([]client.Object{clusterTopology, existingKAITopology})
	logger := logr.Discard()
	// Test and Assert
	err := EnsureKAITopology(ctx, cl, logger, topologyName, clusterTopology)
	assert.Error(t, err)
}

func TestEnsureKAITopologyWhenGetError(t *testing.T) {
	// Setup
	ctx := context.Background()
	topologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	clusterTopology := &corev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
			UID:  uuid.NewUUID(),
		},
		Spec: corev1alpha1.ClusterTopologySpec{
			Levels: topologyLevels,
		},
	}

	// Create a client that will fail on get (non-NotFound error)
	getErr := apierrors.NewInternalError(assert.AnError)
	cl := testutils.NewTestClientBuilder().
		WithObjects(clusterTopology).
		RecordErrorForObjects(testutils.ClientMethodGet, getErr, client.ObjectKey{Name: topologyName}).
		Build()
	logger := logr.Discard()
	// Test
	err := EnsureKAITopology(ctx, cl, logger, topologyName, clusterTopology)
	// Assert
	assert.Error(t, err)
	assert.True(t, errors.Is(err, getErr))
}

func TestEnsureKAITopologyWhenCreateError(t *testing.T) {
	// Setup
	ctx := context.Background()
	topologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	clusterTopology := &corev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
			UID:  uuid.NewUUID(),
		},
		Spec: corev1alpha1.ClusterTopologySpec{
			Levels: topologyLevels,
		},
	}

	// Create a client that will fail on create
	createErr := apierrors.NewInternalError(assert.AnError)
	cl := testutils.NewTestClientBuilder().
		WithObjects(clusterTopology).
		RecordErrorForObjects(testutils.ClientMethodCreate, createErr, client.ObjectKey{Name: topologyName}).
		Build()
	logger := logr.Discard()

	// Test
	err := EnsureKAITopology(ctx, cl, logger, topologyName, clusterTopology)
	// Assert
	assert.Error(t, err)
	assert.True(t, errors.Is(err, createErr))
}

func TestEnsureKAITopologyWhenDeleteError(t *testing.T) {
	// Setup
	ctx := context.Background()
	// New topology levels that ClusterTopology will have
	topologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	clusterTopology := &corev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
			UID:  uuid.NewUUID(),
		},
		Spec: corev1alpha1.ClusterTopologySpec{
			Levels: topologyLevels,
		},
	}

	// Create existing KAI Topology with OLD/DIFFERENT levels to trigger delete-and-recreate
	existingKAITopology := &kaitopologyv1alpha1.Topology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "grove.io/v1alpha1",
					Kind:               "ClusterTopology",
					Name:               clusterTopology.Name,
					UID:                clusterTopology.UID,
					Controller:         ptr.To(true),
					BlockOwnerDeletion: ptr.To(true),
				},
			},
		},
		Spec: kaitopologyv1alpha1.TopologySpec{
			Levels: []kaitopologyv1alpha1.TopologyLevel{
				{NodeLabel: "old-label"}, // Different from ClusterTopology levels
			},
		},
	}

	// Create a client that will fail on delete
	deleteErr := apierrors.NewInternalError(assert.AnError)
	cl := testutils.NewTestClientBuilder().
		WithObjects(clusterTopology, existingKAITopology).
		RecordErrorForObjects(testutils.ClientMethodDelete, deleteErr, client.ObjectKey{Name: topologyName}).
		Build()
	logger := logr.Discard()

	// Test
	err := EnsureKAITopology(ctx, cl, logger, topologyName, clusterTopology)
	// Assert
	assert.Error(t, err)
	assert.True(t, errors.Is(err, deleteErr))
}

func TestEnsureKAITopologyWhenCreateFailsDuringRecreate(t *testing.T) {
	// Setup
	ctx := context.Background()
	updatedTopologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	clusterTopology := &corev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
			UID:  uuid.NewUUID(),
		},
		Spec: corev1alpha1.ClusterTopologySpec{
			Levels: updatedTopologyLevels,
		},
	}

	// Create existing KAI Topology with old levels
	existingKAITopology := &kaitopologyv1alpha1.Topology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "grove.io/v1alpha1",
					Kind:               "ClusterTopology",
					Name:               clusterTopology.Name,
					UID:                clusterTopology.UID,
					Controller:         ptr.To(true),
					BlockOwnerDeletion: ptr.To(true),
				},
			},
		},
		Spec: kaitopologyv1alpha1.TopologySpec{
			Levels: []kaitopologyv1alpha1.TopologyLevel{
				{NodeLabel: "kubernetes.io/hostname"},
			},
		},
	}

	// Create a client that succeeds when deleting KAI Topology resource but fail on create
	// This simulates the scenario where delete succeeds but create fails during recreation
	createErr := apierrors.NewInternalError(assert.AnError)
	cl := testutils.NewTestClientBuilder().
		WithObjects(clusterTopology, existingKAITopology).
		RecordErrorForObjects(testutils.ClientMethodCreate, createErr, client.ObjectKey{Name: topologyName}).
		Build()
	logger := logr.Discard()

	err := EnsureKAITopology(ctx, cl, logger, topologyName, clusterTopology)
	assert.Error(t, err)
	// After delete succeeds, create will fail, so we get the create error message
	assert.True(t, errors.Is(err, createErr))
}

func TestDeleteClusterTopology(t *testing.T) {
	// Setup
	ctx := context.Background()
	topologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	existing := &corev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
		},
		Spec: corev1alpha1.ClusterTopologySpec{
			Levels: topologyLevels,
		},
	}
	cl := testutils.CreateDefaultFakeClient([]client.Object{existing})

	// Test
	err := DeleteClusterTopology(ctx, cl, topologyName)
	// Assert
	require.NoError(t, err)
	// Verify it was deleted
	fetched := &corev1alpha1.ClusterTopology{}
	err = cl.Get(ctx, client.ObjectKey{Name: topologyName}, fetched)
	assert.True(t, apierrors.IsNotFound(err))
}

func TestDeleteClusterTopologyWhenNoneExists(t *testing.T) {
	// Setup
	ctx := context.Background()
	cl := testutils.CreateDefaultFakeClient(nil)
	// Test and Assert
	// Should not error when deleting non-existent topology
	err := DeleteClusterTopology(ctx, cl, topologyName)
	assert.NoError(t, err)
}

func TestDeleteClusterTopologyError(t *testing.T) {
	ctx := context.Background()
	topologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	existing := &corev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
		},
		Spec: corev1alpha1.ClusterTopologySpec{
			Levels: topologyLevels,
		},
	}

	// Create a client that will fail on delete (non-NotFound error)
	deleteErr := apierrors.NewInternalError(assert.AnError)
	cl := testutils.NewTestClientBuilder().
		WithObjects(existing).
		RecordErrorForObjects(testutils.ClientMethodDelete, deleteErr, client.ObjectKey{Name: topologyName}).
		Build()

	err := DeleteClusterTopology(ctx, cl, topologyName)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, deleteErr))
}

func TestBuildClusterTopology(t *testing.T) {
	topologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	topology := buildClusterTopology(topologyName, topologyLevels)
	assert.Equal(t, topologyName, topology.Name)
	assert.Equal(t, topologyLevels, topology.Spec.Levels)
}

func TestIsClusterTopologyChanged(t *testing.T) {
	tests := []struct {
		name            string
		oldLevels       []corev1alpha1.TopologyLevel
		newLevels       []corev1alpha1.TopologyLevel
		expectedChanged bool
	}{
		{
			name: "No change in levels",
			oldLevels: []corev1alpha1.TopologyLevel{
				{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
				{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
			},
			newLevels: []corev1alpha1.TopologyLevel{
				{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
				{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
			},
			expectedChanged: false,
		},
		{
			name: "Change in levels",
			oldLevels: []corev1alpha1.TopologyLevel{
				{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
				{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
			},
			newLevels: []corev1alpha1.TopologyLevel{
				{Domain: corev1alpha1.TopologyDomainRegion, Key: "topology.kubernetes.io/region"},
			},
			expectedChanged: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			oldTopology := &corev1alpha1.ClusterTopology{
				Spec: corev1alpha1.ClusterTopologySpec{
					Levels: test.oldLevels,
				},
			}
			newTopology := &corev1alpha1.ClusterTopology{
				Spec: corev1alpha1.ClusterTopologySpec{
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
	topologyLevels := []corev1alpha1.TopologyLevel{
		{Domain: corev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: corev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	clusterTopology := &corev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
			UID:  uuid.NewUUID(),
		},
		Spec: corev1alpha1.ClusterTopologySpec{
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

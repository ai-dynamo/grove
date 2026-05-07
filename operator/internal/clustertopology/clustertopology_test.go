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
	"testing"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/kai"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	kaitopologyv1alpha1 "github.com/kai-scheduler/KAI-scheduler/pkg/apis/kai/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const topologyName = "test-topology"

func newKaiBackends(cl client.Client) map[string]scheduler.Backend {
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}
	b := kai.New(cl, cl.Scheme(), nil, profile)
	return map[string]scheduler.Backend{b.Name(): b}
}

func TestSynchronizeTopologyListsAndSyncs(t *testing.T) {
	ctx := context.Background()
	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	ct := createTestClusterTopology(topologyName, topologyLevels)

	cl := testutils.CreateDefaultFakeClient([]client.Object{ct})
	logger := logr.Discard()

	err := SynchronizeTopology(ctx, cl, logger, newKaiBackends(cl))
	require.NoError(t, err)

	// Verify KAI Topology was created
	fetchedKAITopology := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(ctx, client.ObjectKey{Name: topologyName}, fetchedKAITopology)
	require.NoError(t, err)
	assert.Len(t, fetchedKAITopology.Spec.Levels, 2)
	assert.Equal(t, "topology.kubernetes.io/zone", fetchedKAITopology.Spec.Levels[0].NodeLabel)
	assert.Equal(t, "kubernetes.io/hostname", fetchedKAITopology.Spec.Levels[1].NodeLabel)
	assert.True(t, metav1.IsControlledBy(fetchedKAITopology, ct))
}

func TestSynchronizeTopologyMultipleCTs(t *testing.T) {
	ctx := context.Background()
	ct1 := createTestClusterTopology("topology-a", []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
	})
	ct2 := createTestClusterTopology("topology-b", []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	})

	cl := testutils.CreateDefaultFakeClient([]client.Object{ct1, ct2})
	logger := logr.Discard()

	err := SynchronizeTopology(ctx, cl, logger, newKaiBackends(cl))
	require.NoError(t, err)

	// Verify both KAI Topologies were created
	kai1 := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(ctx, client.ObjectKey{Name: "topology-a"}, kai1)
	require.NoError(t, err)
	assert.Len(t, kai1.Spec.Levels, 1)

	kai2 := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(ctx, client.ObjectKey{Name: "topology-b"}, kai2)
	require.NoError(t, err)
	assert.Len(t, kai2.Spec.Levels, 1)
}

func TestSynchronizeTopologyNoCTs(t *testing.T) {
	ctx := context.Background()
	cl := testutils.CreateDefaultFakeClient(nil)
	logger := logr.Discard()

	err := SynchronizeTopology(ctx, cl, logger, newKaiBackends(cl))
	require.NoError(t, err)
}

func TestSynchronizeTopologyNoTASBackends(t *testing.T) {
	ctx := context.Background()
	ct := createTestClusterTopology(topologyName, []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	})
	cl := testutils.CreateDefaultFakeClient([]client.Object{ct})
	logger := logr.Discard()

	// Pass nil backends
	err := SynchronizeTopology(ctx, cl, logger, nil)
	require.NoError(t, err)
}

func TestSynchronizeTopologySkipsExternallyManaged(t *testing.T) {
	ctx := context.Background()
	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	ct := createTestClusterTopology(topologyName, topologyLevels)
	ct.Spec.SchedulerTopologyReferences = []grovecorev1alpha1.SchedulerTopologyReference{
		{SchedulerName: "kai-scheduler", TopologyReference: "external-kai-topology"},
	}

	cl := testutils.CreateDefaultFakeClient([]client.Object{ct})
	logger := logr.Discard()

	// KAI backend is listed in schedulerTopologyReferences — SyncTopology should NOT be called.
	// If it were called, it would try to create a KAI Topology and succeed, so we verify
	// that no KAI Topology was created.
	err := SynchronizeTopology(ctx, cl, logger, newKaiBackends(cl))
	require.NoError(t, err)

	kaiTopology := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(ctx, client.ObjectKey{Name: topologyName}, kaiTopology)
	assert.True(t, apierrors.IsNotFound(err), "KAI Topology should not have been created for externally-managed CT")
}

func TestSynchronizeTopologyListError(t *testing.T) {
	ctx := context.Background()
	listErr := apierrors.NewInternalError(assert.AnError)
	ctListGVK := grovecorev1alpha1.SchemeGroupVersion.WithKind("ClusterTopologyList")
	cl := testutils.NewTestClientBuilder().
		RecordErrorForObjectsMatchingLabels(testutils.ClientMethodList, client.ObjectKey{}, ctListGVK, nil, listErr).
		Build()
	logger := logr.Discard()

	err := SynchronizeTopology(ctx, cl, logger, newKaiBackends(cl))
	assert.Error(t, err)
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

func TestBuildSchedulerReferenceMap(t *testing.T) {
	refs := []grovecorev1alpha1.SchedulerTopologyReference{
		{SchedulerName: "kai-scheduler", TopologyReference: "kai-topo"},
		{SchedulerName: "other-scheduler", TopologyReference: "other-topo"},
	}
	m := BuildSchedulerReferenceMap(refs)
	assert.NotNil(t, m["kai-scheduler"])
	assert.Equal(t, "kai-topo", m["kai-scheduler"].TopologyReference)
	assert.Nil(t, m["nonexistent"])
	assert.Empty(t, BuildSchedulerReferenceMap(nil))
}

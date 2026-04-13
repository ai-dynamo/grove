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

package kai

import (
	"context"
	"errors"
	"testing"

	apicommonconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	kaitopologyv1alpha1 "github.com/NVIDIA/KAI-scheduler/pkg/apis/kai/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const topologyName = "test-topology"

// -- Shared helpers --

func newTASBackend(cl client.Client) scheduler.TopologyAwareSchedBackend {
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai}
	b := New(cl, cl.Scheme(), recorder, profile)
	return b.(scheduler.TopologyAwareSchedBackend)
}

func defaultTopologyLevels() []grovecorev1alpha1.TopologyLevel {
	return []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
}

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

func createTestKAITopology(levels []kaitopologyv1alpha1.TopologyLevel, owner *grovecorev1alpha1.ClusterTopology) *kaitopologyv1alpha1.Topology {
	topology := &kaitopologyv1alpha1.Topology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
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

func convertToKAITopologyLevels(levels []grovecorev1alpha1.TopologyLevel) []kaitopologyv1alpha1.TopologyLevel {
	kaiLevels := make([]kaitopologyv1alpha1.TopologyLevel, len(levels))
	for i, level := range levels {
		kaiLevels[i] = kaitopologyv1alpha1.TopologyLevel{
			NodeLabel: level.Key,
		}
	}
	return kaiLevels
}

// -- GVR and no-op tests --

func TestTopologyGVR(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	b := newTASBackend(cl)

	gvr := b.TopologyGVR()
	assert.Equal(t, schema.GroupVersionResource{
		Group:    "kai.scheduler",
		Version:  "v1alpha1",
		Resource: "topologies",
	}, gvr)
}

func TestOnTopologyDeleteIsNoOp(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	b := newTASBackend(cl)

	ct := createTestClusterTopology(topologyName, defaultTopologyLevels())
	err := b.OnTopologyDelete(context.Background(), cl, ct)
	assert.NoError(t, err)
}

// -- Happy-path SyncTopology tests --

func TestSyncTopologyWhenNoneExists(t *testing.T) {
	ctx := context.Background()
	cl := testutils.CreateDefaultFakeClient(nil)
	b := newTASBackend(cl)

	topologyLevels := []grovecorev1alpha1.TopologyLevel{
		{Domain: grovecorev1alpha1.TopologyDomainZone, Key: "topology.kubernetes.io/zone"},
		{Domain: grovecorev1alpha1.TopologyDomainRack, Key: "topology.kubernetes.io/rack"},
		{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
	}
	ct := createTestClusterTopology(topologyName, topologyLevels)

	err := b.SyncTopology(ctx, cl, ct)
	require.NoError(t, err)

	kaiTopology := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(ctx, client.ObjectKey{Name: topologyName}, kaiTopology)
	require.NoError(t, err)
	assert.Equal(t, topologyName, kaiTopology.Name)
	assert.Len(t, kaiTopology.Spec.Levels, 3)
	assert.Equal(t, "topology.kubernetes.io/zone", kaiTopology.Spec.Levels[0].NodeLabel)
	assert.Equal(t, "topology.kubernetes.io/rack", kaiTopology.Spec.Levels[1].NodeLabel)
	assert.Equal(t, "kubernetes.io/hostname", kaiTopology.Spec.Levels[2].NodeLabel)

	assert.True(t, metav1.IsControlledBy(kaiTopology, ct))
}

func TestSyncTopologyWhenLevelsUpdated(t *testing.T) {
	ctx := context.Background()
	topologyLevels := defaultTopologyLevels()
	ct := createTestClusterTopology(topologyName, topologyLevels)

	existingKAITopology := createTestKAITopology([]kaitopologyv1alpha1.TopologyLevel{
		{NodeLabel: "kubernetes.io/hostname"},
	}, ct)

	cl := testutils.CreateDefaultFakeClient([]client.Object{ct, existingKAITopology})
	b := newTASBackend(cl)

	err := b.SyncTopology(ctx, cl, ct)
	require.NoError(t, err)

	kaiTopology := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(ctx, client.ObjectKey{Name: topologyName}, kaiTopology)
	require.NoError(t, err)
	assert.Equal(t, int64(0), kaiTopology.Generation)
	assert.Len(t, kaiTopology.Spec.Levels, 2)
	assert.Equal(t, "topology.kubernetes.io/zone", kaiTopology.Spec.Levels[0].NodeLabel)
	assert.Equal(t, "kubernetes.io/hostname", kaiTopology.Spec.Levels[1].NodeLabel)
}

func TestSyncTopologyWhenNoChangeRequired(t *testing.T) {
	ctx := context.Background()
	topologyLevels := defaultTopologyLevels()
	ct := createTestClusterTopology(topologyName, topologyLevels)

	existingKAITopology := createTestKAITopology(convertToKAITopologyLevels(topologyLevels), ct)
	existingKAITopology.ResourceVersion = "1"

	cl := testutils.CreateDefaultFakeClient([]client.Object{ct, existingKAITopology})
	b := newTASBackend(cl)

	err := b.SyncTopology(ctx, cl, ct)
	require.NoError(t, err)

	kaiTopology := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(ctx, client.ObjectKey{Name: topologyName}, kaiTopology)
	require.NoError(t, err)
	assert.Equal(t, int64(0), kaiTopology.Generation)
	assert.Equal(t, "1", kaiTopology.ResourceVersion)
}

func TestSyncTopologyWithUnknownOwner(t *testing.T) {
	ctx := context.Background()
	topologyLevels := defaultTopologyLevels()
	ct := createTestClusterTopology(topologyName, topologyLevels)

	differentOwner := createTestClusterTopology("different-owner", topologyLevels)
	existingKAITopology := createTestKAITopology([]kaitopologyv1alpha1.TopologyLevel{
		{NodeLabel: "topology.kubernetes.io/zone"},
	}, differentOwner)

	cl := testutils.CreateDefaultFakeClient([]client.Object{ct, existingKAITopology})
	b := newTASBackend(cl)

	err := b.SyncTopology(ctx, cl, ct)
	assert.Error(t, err)
}

// -- Error-path SyncTopology tests (table-driven) --

func TestSyncTopologyErrors(t *testing.T) {
	topologyLevels := defaultTopologyLevels()

	tests := []struct {
		name            string
		method          testutils.ClientMethod
		existingObjects func(ct *grovecorev1alpha1.ClusterTopology) []client.Object
	}{
		{
			name:   "GetError",
			method: testutils.ClientMethodGet,
			existingObjects: func(ct *grovecorev1alpha1.ClusterTopology) []client.Object {
				return []client.Object{ct}
			},
		},
		{
			name:   "CreateError",
			method: testutils.ClientMethodCreate,
			existingObjects: func(ct *grovecorev1alpha1.ClusterTopology) []client.Object {
				return []client.Object{ct}
			},
		},
		{
			name:   "DeleteError",
			method: testutils.ClientMethodDelete,
			existingObjects: func(ct *grovecorev1alpha1.ClusterTopology) []client.Object {
				// Need existing KAI topology with different levels to trigger update path
				existingKAI := createTestKAITopology([]kaitopologyv1alpha1.TopologyLevel{
					{NodeLabel: "old-label"},
				}, ct)
				return []client.Object{ct, existingKAI}
			},
		},
		{
			name:   "CreateFailsDuringRecreate",
			method: testutils.ClientMethodCreate,
			existingObjects: func(ct *grovecorev1alpha1.ClusterTopology) []client.Object {
				// Need existing KAI topology with different levels to trigger update path
				existingKAI := createTestKAITopology([]kaitopologyv1alpha1.TopologyLevel{
					{NodeLabel: "kubernetes.io/hostname"},
				}, ct)
				return []client.Object{ct, existingKAI}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ct := createTestClusterTopology(topologyName, topologyLevels)
			injectedErr := apierrors.NewInternalError(assert.AnError)
			objects := tc.existingObjects(ct)

			cl := testutils.NewTestClientBuilder().
				WithObjects(objects...).
				RecordErrorForObjects(tc.method, injectedErr, client.ObjectKey{Name: topologyName}).
				Build()
			b := newTASBackend(cl)

			err := b.SyncTopology(context.Background(), cl, ct)
			assert.Error(t, err)
			assert.True(t, errors.Is(err, injectedErr))
		})
	}
}

// -- buildKAITopology and isKAITopologyChanged tests --

func TestBuildKAITopology(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	topologyLevels := defaultTopologyLevels()
	ct := &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: topologyName,
			UID:  uuid.NewUUID(),
		},
		Spec: grovecorev1alpha1.ClusterTopologySpec{
			Levels: topologyLevels,
		},
	}

	kaiTopology, err := buildKAITopology(topologyName, ct, cl.Scheme())
	require.NoError(t, err)
	assert.Equal(t, topologyName, kaiTopology.Name)
	assert.Len(t, kaiTopology.Spec.Levels, 2)
	assert.Equal(t, "topology.kubernetes.io/zone", kaiTopology.Spec.Levels[0].NodeLabel)
	assert.Equal(t, "kubernetes.io/hostname", kaiTopology.Spec.Levels[1].NodeLabel)
	assert.True(t, metav1.IsControlledBy(kaiTopology, ct))
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

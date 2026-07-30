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

package kueue

import (
	"context"
	"testing"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kueuev1beta2 "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestTopologyGVR(t *testing.T) {
	b := newKueueBackend(testutils.CreateDefaultFakeClient(nil))

	assert.Equal(t, schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta2",
		Resource: "topologies",
	}, b.TopologyGVR())
}

func TestSyncTopologyCreatesKueueTopology(t *testing.T) {
	ctx := context.Background()
	cl := testutils.CreateDefaultFakeClient(nil)
	b := newKueueBackend(cl)
	ct := testClusterTopology()

	require.NoError(t, b.SyncTopology(ctx, cl, ct))

	topology := &kueuev1beta2.Topology{}
	require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: ct.Name}, topology))
	assert.True(t, metav1.IsControlledBy(topology, ct))
	assert.Equal(t, []kueuev1beta2.TopologyLevel{
		{NodeLabel: "topology.ai-dynamo.io/rack"},
		{NodeLabel: "kubernetes.io/hostname"},
	}, topology.Spec.Levels)
}

func TestCheckTopologyDrift(t *testing.T) {
	ctx := context.Background()
	ct := testClusterTopology()
	topology, err := buildKueueTopology(ct.Name, ct, testutils.CreateDefaultFakeClient(nil).Scheme())
	require.NoError(t, err)
	cl := testutils.NewTestClientBuilder().WithObjects(ct, topology).Build()
	b := newKueueBackend(cl)

	inSync, message, _, err := b.CheckTopologyDrift(ctx, ct, grovecorev1alpha1.SchedulerTopologyBinding{
		SchedulerName:     string(configv1alpha1.SchedulerNameKueue),
		TopologyReference: ct.Name,
	})

	require.NoError(t, err)
	assert.True(t, inSync)
	assert.Empty(t, message)
}

func newKueueBackend(cl client.Client) scheduler.TopologyAwareBackend {
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKueue}
	b := New(cl, cl.Scheme(), recorder, profile)
	return b.(scheduler.TopologyAwareBackend)
}

func testClusterTopology() *grovecorev1alpha1.ClusterTopologyBinding {
	return &grovecorev1alpha1.ClusterTopologyBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "grove-kind-topology",
			UID:  uuid.NewUUID(),
		},
		Spec: grovecorev1alpha1.ClusterTopologyBindingSpec{
			Levels: []grovecorev1alpha1.TopologyLevel{
				{Domain: grovecorev1alpha1.TopologyDomainRack, Key: "topology.ai-dynamo.io/rack"},
				{Domain: grovecorev1alpha1.TopologyDomainHost, Key: "kubernetes.io/hostname"},
			},
		},
	}
}

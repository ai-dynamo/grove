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
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"

	kaitopologyv1alpha1 "github.com/NVIDIA/KAI-scheduler/pkg/apis/kai/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// createTestClusterTopology creates a test ClusterTopology with the given parameters.
func createTestClusterTopology(name string, levels []grovecorev1alpha1.TopologyLevel, refs []grovecorev1alpha1.SchedulerReference) *grovecorev1alpha1.ClusterTopology {
	return &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			UID:  "test-uid",
		},
		Spec: grovecorev1alpha1.ClusterTopologySpec{
			Levels:              levels,
			SchedulerReferences: refs,
		},
	}
}

// createTestKAITopology creates a test KAI Topology with optional owner reference.
func createTestKAITopology(name string, levels []kaitopologyv1alpha1.TopologyLevel, owner *grovecorev1alpha1.ClusterTopology) *kaitopologyv1alpha1.Topology {
	topology := &kaitopologyv1alpha1.Topology{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Generation: 1,
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

// setupReconciler creates a reconciler with a fake client and the given objects.
func setupReconciler(objects []client.Object, tasEnabled bool) (*Reconciler, client.Client) {
	cl := fake.NewClientBuilder().
		WithScheme(groveclientscheme.Scheme).
		WithStatusSubresource(&grovecorev1alpha1.ClusterTopology{}).
		WithObjects(objects...).
		Build()
	r := &Reconciler{
		client:     cl,
		scheme:     groveclientscheme.Scheme,
		tasEnabled: tasEnabled,
	}
	return r, cl
}

// findCondition finds a condition by type in the given slice.
func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

// testTopologyLevels returns a sample list of topology levels for testing.
func testTopologyLevels() []grovecorev1alpha1.TopologyLevel {
	return []grovecorev1alpha1.TopologyLevel{
		{Domain: "region", Key: "topology.kubernetes.io/region"},
		{Domain: "zone", Key: "topology.kubernetes.io/zone"},
		{Domain: "host", Key: "kubernetes.io/hostname"},
	}
}

// testKAITopologyLevels converts test topology levels to KAI topology levels.
func testKAITopologyLevels() []kaitopologyv1alpha1.TopologyLevel {
	return []kaitopologyv1alpha1.TopologyLevel{
		{NodeLabel: "topology.kubernetes.io/region"},
		{NodeLabel: "topology.kubernetes.io/zone"},
		{NodeLabel: "kubernetes.io/hostname"},
	}
}

// ================================
// Test Cases
// ================================

// TestReconcileClusterTopologyNotFound tests that reconciliation succeeds when CT doesn't exist.
func TestReconcileClusterTopologyNotFound(t *testing.T) {
	r, _ := setupReconciler([]client.Object{}, true)

	req := ctrl.Request{
		NamespacedName: client.ObjectKey{Name: "nonexistent"},
	}

	result, err := r.Reconcile(context.Background(), req)

	assert.NoError(t, err)
	assert.False(t, result.Requeue)
}

// TestReconcileTASDisabled tests that reconciliation skips when TAS is disabled.
func TestReconcileTASDisabled(t *testing.T) {
	levels := testTopologyLevels()
	ct := createTestClusterTopology("test-topology", levels, nil)

	r, cl := setupReconciler([]client.Object{ct}, false)

	req := ctrl.Request{
		NamespacedName: client.ObjectKey{Name: "test-topology"},
	}

	result, err := r.Reconcile(context.Background(), req)

	assert.NoError(t, err)
	assert.False(t, result.Requeue)

	// Verify no KAI Topology was created
	kaiTopology := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(context.Background(), client.ObjectKey{Name: "test-topology"}, kaiTopology)
	assert.True(t, client.IgnoreNotFound(err) == nil || err != nil)
}

// TestReconcileAutoManagedCreateKAITopology tests creating a KAI Topology from ClusterTopology.
func TestReconcileAutoManagedCreateKAITopology(t *testing.T) {
	levels := testTopologyLevels()
	ct := createTestClusterTopology("test-topology", levels, nil)

	r, cl := setupReconciler([]client.Object{ct}, true)

	req := ctrl.Request{
		NamespacedName: client.ObjectKey{Name: "test-topology"},
	}

	result, err := r.Reconcile(context.Background(), req)

	assert.NoError(t, err)
	assert.False(t, result.Requeue)

	// Verify KAI Topology was created with correct levels
	kaiTopology := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(context.Background(), client.ObjectKey{Name: "test-topology"}, kaiTopology)
	require.NoError(t, err)

	assert.Len(t, kaiTopology.Spec.Levels, 3)
	assert.Equal(t, "topology.kubernetes.io/region", kaiTopology.Spec.Levels[0].NodeLabel)
	assert.Equal(t, "topology.kubernetes.io/zone", kaiTopology.Spec.Levels[1].NodeLabel)
	assert.Equal(t, "kubernetes.io/hostname", kaiTopology.Spec.Levels[2].NodeLabel)

	// Verify owner reference
	require.Len(t, kaiTopology.OwnerReferences, 1)
	assert.Equal(t, "test-topology", kaiTopology.OwnerReferences[0].Name)

	// Verify status condition
	updatedCT := &grovecorev1alpha1.ClusterTopology{}
	err = cl.Get(context.Background(), client.ObjectKey{Name: "test-topology"}, updatedCT)
	require.NoError(t, err)

	cond := findCondition(updatedCT.Status.Conditions, apicommonconstants.ConditionTypeSchedulerTopologyDrift)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, apicommonconstants.ConditionReasonInSync, cond.Reason)
	assert.Nil(t, updatedCT.Status.SchedulerTopologyStatuses)
}

// TestReconcileAutoManagedKAITopologyInSync tests that in-sync KAI Topology is not recreated.
func TestReconcileAutoManagedKAITopologyInSync(t *testing.T) {
	levels := testTopologyLevels()
	ct := createTestClusterTopology("test-topology", levels, nil)
	kaiLevels := testKAITopologyLevels()
	kaiTopology := createTestKAITopology("test-topology", kaiLevels, ct)

	r, cl := setupReconciler([]client.Object{ct, kaiTopology}, true)

	req := ctrl.Request{
		NamespacedName: client.ObjectKey{Name: "test-topology"},
	}

	result, err := r.Reconcile(context.Background(), req)

	assert.NoError(t, err)
	assert.False(t, result.Requeue)

	// Verify KAI Topology still exists and was not modified (same generation)
	retrievedKAI := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(context.Background(), client.ObjectKey{Name: "test-topology"}, retrievedKAI)
	require.NoError(t, err)
	assert.Equal(t, int64(1), retrievedKAI.Generation)

	// Verify status condition
	updatedCT := &grovecorev1alpha1.ClusterTopology{}
	err = cl.Get(context.Background(), client.ObjectKey{Name: "test-topology"}, updatedCT)
	require.NoError(t, err)

	cond := findCondition(updatedCT.Status.Conditions, apicommonconstants.ConditionTypeSchedulerTopologyDrift)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, apicommonconstants.ConditionReasonInSync, cond.Reason)
}

// TestReconcileAutoManagedKAITopologyLevelsChanged tests recreating KAI Topology when levels change.
func TestReconcileAutoManagedKAITopologyLevelsChanged(t *testing.T) {
	levels := testTopologyLevels()
	ct := createTestClusterTopology("test-topology", levels, nil)

	// KAI Topology with different levels
	oldKAILevels := []kaitopologyv1alpha1.TopologyLevel{
		{NodeLabel: "old.label.io/zone"},
	}
	kaiTopology := createTestKAITopology("test-topology", oldKAILevels, ct)

	r, cl := setupReconciler([]client.Object{ct, kaiTopology}, true)

	req := ctrl.Request{
		NamespacedName: client.ObjectKey{Name: "test-topology"},
	}

	result, err := r.Reconcile(context.Background(), req)

	assert.NoError(t, err)
	assert.False(t, result.Requeue)

	// Verify KAI Topology was recreated with new levels
	recreatedKAI := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(context.Background(), client.ObjectKey{Name: "test-topology"}, recreatedKAI)
	require.NoError(t, err)

	assert.Len(t, recreatedKAI.Spec.Levels, 3)
	assert.Equal(t, "topology.kubernetes.io/region", recreatedKAI.Spec.Levels[0].NodeLabel)
	assert.Equal(t, "topology.kubernetes.io/zone", recreatedKAI.Spec.Levels[1].NodeLabel)
	assert.Equal(t, "kubernetes.io/hostname", recreatedKAI.Spec.Levels[2].NodeLabel)

	// Verify status condition
	updatedCT := &grovecorev1alpha1.ClusterTopology{}
	err = cl.Get(context.Background(), client.ObjectKey{Name: "test-topology"}, updatedCT)
	require.NoError(t, err)

	cond := findCondition(updatedCT.Status.Conditions, apicommonconstants.ConditionTypeSchedulerTopologyDrift)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, apicommonconstants.ConditionReasonInSync, cond.Reason)
}

// TestReconcileAutoManagedKAITopologyNotOwnedByCT tests handling of externally owned KAI Topology.
func TestReconcileAutoManagedKAITopologyNotOwnedByCT(t *testing.T) {
	levels := testTopologyLevels()
	ct := createTestClusterTopology("test-topology", levels, nil)

	// KAI Topology owned by a different CT with different levels
	differentLevels := []kaitopologyv1alpha1.TopologyLevel{
		{NodeLabel: "different.label/one"},
	}
	otherCT := &grovecorev1alpha1.ClusterTopology{
		ObjectMeta: metav1.ObjectMeta{
			Name: "other-topology",
			UID:  "other-uid",
		},
		Spec: grovecorev1alpha1.ClusterTopologySpec{
			Levels: []grovecorev1alpha1.TopologyLevel{},
		},
	}
	kaiTopology := createTestKAITopology("test-topology", differentLevels, otherCT)

	r, cl := setupReconciler([]client.Object{ct, kaiTopology}, true)

	req := ctrl.Request{
		NamespacedName: client.ObjectKey{Name: "test-topology"},
	}

	result, err := r.Reconcile(context.Background(), req)

	assert.NoError(t, err)
	assert.False(t, result.Requeue)

	// Verify KAI Topology was NOT modified
	retrievedKAI := &kaitopologyv1alpha1.Topology{}
	err = cl.Get(context.Background(), client.ObjectKey{Name: "test-topology"}, retrievedKAI)
	require.NoError(t, err)
	assert.Len(t, retrievedKAI.Spec.Levels, 1) // Original levels still present

	// Verify status shows drift
	updatedCT := &grovecorev1alpha1.ClusterTopology{}
	err = cl.Get(context.Background(), client.ObjectKey{Name: "test-topology"}, updatedCT)
	require.NoError(t, err)

	cond := findCondition(updatedCT.Status.Conditions, apicommonconstants.ConditionTypeSchedulerTopologyDrift)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, apicommonconstants.ConditionReasonDrift, cond.Reason)
}

// TestReconcileDriftDetectionAllInSync tests drift detection with all topologies in sync.
func TestReconcileDriftDetectionAllInSync(t *testing.T) {
	levels := testTopologyLevels()
	refs := []grovecorev1alpha1.SchedulerReference{
		{SchedulerName: "kai-scheduler", Reference: "kai-topology-1"},
		{SchedulerName: "second-scheduler", Reference: "kai-topology-2"},
	}
	ct := createTestClusterTopology("test-topology", levels, refs)

	kaiLevels := testKAITopologyLevels()
	kai1 := &kaitopologyv1alpha1.Topology{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "kai-topology-1",
			Generation: 5,
		},
		Spec: kaitopologyv1alpha1.TopologySpec{
			Levels: kaiLevels,
		},
	}
	kai2 := &kaitopologyv1alpha1.Topology{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "kai-topology-2",
			Generation: 10,
		},
		Spec: kaitopologyv1alpha1.TopologySpec{
			Levels: kaiLevels,
		},
	}

	r, cl := setupReconciler([]client.Object{ct, kai1, kai2}, true)

	req := ctrl.Request{
		NamespacedName: client.ObjectKey{Name: "test-topology"},
	}

	result, err := r.Reconcile(context.Background(), req)

	assert.NoError(t, err)
	assert.False(t, result.Requeue)

	// Verify status condition
	updatedCT := &grovecorev1alpha1.ClusterTopology{}
	err = cl.Get(context.Background(), client.ObjectKey{Name: "test-topology"}, updatedCT)
	require.NoError(t, err)

	cond := findCondition(updatedCT.Status.Conditions, apicommonconstants.ConditionTypeSchedulerTopologyDrift)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, apicommonconstants.ConditionReasonInSync, cond.Reason)

	// Verify per-scheduler status
	require.Len(t, updatedCT.Status.SchedulerTopologyStatuses, 2)
	for _, status := range updatedCT.Status.SchedulerTopologyStatuses {
		assert.True(t, status.InSync)
		assert.Equal(t, "", status.Message)
	}
	assert.Equal(t, int64(5), updatedCT.Status.SchedulerTopologyStatuses[0].SchedulerBackendTopologyObservedGeneration)
	assert.Equal(t, int64(10), updatedCT.Status.SchedulerTopologyStatuses[1].SchedulerBackendTopologyObservedGeneration)
}

// TestReconcileDriftDetectionTopologyNotFound tests drift detection when topology is missing.
func TestReconcileDriftDetectionTopologyNotFound(t *testing.T) {
	levels := testTopologyLevels()
	refs := []grovecorev1alpha1.SchedulerReference{
		{SchedulerName: "kai-scheduler", Reference: "kai-topology-1"},
		{SchedulerName: "second-scheduler", Reference: "missing-topology"},
	}
	ct := createTestClusterTopology("test-topology", levels, refs)

	kaiLevels := testKAITopologyLevels()
	kai1 := &kaitopologyv1alpha1.Topology{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "kai-topology-1",
			Generation: 5,
		},
		Spec: kaitopologyv1alpha1.TopologySpec{
			Levels: kaiLevels,
		},
	}

	r, cl := setupReconciler([]client.Object{ct, kai1}, true)

	req := ctrl.Request{
		NamespacedName: client.ObjectKey{Name: "test-topology"},
	}

	result, err := r.Reconcile(context.Background(), req)

	assert.NoError(t, err)
	assert.False(t, result.Requeue)

	// Verify status condition is Unknown
	updatedCT := &grovecorev1alpha1.ClusterTopology{}
	err = cl.Get(context.Background(), client.ObjectKey{Name: "test-topology"}, updatedCT)
	require.NoError(t, err)

	cond := findCondition(updatedCT.Status.Conditions, apicommonconstants.ConditionTypeSchedulerTopologyDrift)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionUnknown, cond.Status)
	assert.Equal(t, apicommonconstants.ConditionReasonTopologyNotFound, cond.Reason)

	// Verify per-scheduler status
	require.Len(t, updatedCT.Status.SchedulerTopologyStatuses, 2)

	// First one should be in sync
	assert.Equal(t, "kai-scheduler", updatedCT.Status.SchedulerTopologyStatuses[0].SchedulerName)
	assert.True(t, updatedCT.Status.SchedulerTopologyStatuses[0].InSync)

	// Second one should be not in sync with not found message
	assert.Equal(t, "second-scheduler", updatedCT.Status.SchedulerTopologyStatuses[1].SchedulerName)
	assert.False(t, updatedCT.Status.SchedulerTopologyStatuses[1].InSync)
	assert.Contains(t, updatedCT.Status.SchedulerTopologyStatuses[1].Message, "not found")
}

// TestReconcileDriftDetectionLevelsMismatch tests drift detection with level mismatches.
func TestReconcileDriftDetectionLevelsMismatch(t *testing.T) {
	levels := testTopologyLevels()
	refs := []grovecorev1alpha1.SchedulerReference{
		{SchedulerName: "kai-scheduler", Reference: "kai-topology-1"},
	}
	ct := createTestClusterTopology("test-topology", levels, refs)

	// KAI Topology with different levels
	mismatchedKAILevels := []kaitopologyv1alpha1.TopologyLevel{
		{NodeLabel: "topology.kubernetes.io/region"},
		{NodeLabel: "different.label.io/zone"}, // Different!
	}
	kai1 := &kaitopologyv1alpha1.Topology{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "kai-topology-1",
			Generation: 5,
		},
		Spec: kaitopologyv1alpha1.TopologySpec{
			Levels: mismatchedKAILevels,
		},
	}

	r, cl := setupReconciler([]client.Object{ct, kai1}, true)

	req := ctrl.Request{
		NamespacedName: client.ObjectKey{Name: "test-topology"},
	}

	result, err := r.Reconcile(context.Background(), req)

	assert.NoError(t, err)
	assert.False(t, result.Requeue)

	// Verify status condition shows drift
	updatedCT := &grovecorev1alpha1.ClusterTopology{}
	err = cl.Get(context.Background(), client.ObjectKey{Name: "test-topology"}, updatedCT)
	require.NoError(t, err)

	cond := findCondition(updatedCT.Status.Conditions, apicommonconstants.ConditionTypeSchedulerTopologyDrift)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, apicommonconstants.ConditionReasonDrift, cond.Reason)

	// Verify per-scheduler status
	require.Len(t, updatedCT.Status.SchedulerTopologyStatuses, 1)
	assert.Equal(t, "kai-scheduler", updatedCT.Status.SchedulerTopologyStatuses[0].SchedulerName)
	assert.False(t, updatedCT.Status.SchedulerTopologyStatuses[0].InSync)
	assert.Contains(t, updatedCT.Status.SchedulerTopologyStatuses[0].Message, "mismatch")
}

// TestReconcileObservedGenerationUpdated tests that ObservedGeneration is updated.
func TestReconcileObservedGenerationUpdated(t *testing.T) {
	levels := testTopologyLevels()
	ct := createTestClusterTopology("test-topology", levels, nil)
	ct.Generation = 5

	r, cl := setupReconciler([]client.Object{ct}, true)

	req := ctrl.Request{
		NamespacedName: client.ObjectKey{Name: "test-topology"},
	}

	result, err := r.Reconcile(context.Background(), req)

	assert.NoError(t, err)
	assert.False(t, result.Requeue)

	// Verify ObservedGeneration was updated
	updatedCT := &grovecorev1alpha1.ClusterTopology{}
	err = cl.Get(context.Background(), client.ObjectKey{Name: "test-topology"}, updatedCT)
	require.NoError(t, err)

	assert.Equal(t, int64(5), updatedCT.Status.ObservedGeneration)
}

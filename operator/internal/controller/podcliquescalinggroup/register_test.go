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

package podcliquescalinggroup

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestPodCliqueScalingGroupReconcilePredicate(t *testing.T) {
	pred, ok := podCliqueScalingGroupReconcilePredicate().(predicate.Funcs)
	require.True(t, ok, "predicate must be predicate.Funcs")

	newPCSG := func(generation int64, annotations map[string]string) *grovecorev1alpha1.PodCliqueScalingGroup {
		return &grovecorev1alpha1.PodCliqueScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Generation: generation, Annotations: annotations},
		}
	}
	tests := []struct {
		name string
		old  *grovecorev1alpha1.PodCliqueScalingGroup
		new  *grovecorev1alpha1.PodCliqueScalingGroup
		want bool
	}{
		{name: "spec generation changed", old: newPCSG(1, nil), new: newPCSG(2, nil), want: true},
		{
			name: "parent generation annotation changed",
			old:  newPCSG(1, map[string]string{constants.AnnotationOnDeletePodCliqueSetGenerationHash: "old"}),
			new:  newPCSG(1, map[string]string{constants.AnnotationOnDeletePodCliqueSetGenerationHash: "new"}),
			want: true,
		},
		{
			name: "unrelated annotation changed",
			old:  newPCSG(1, map[string]string{"example.com/value": "old"}),
			new:  newPCSG(1, map[string]string{"example.com/value": "new"}),
			want: false,
		},
		{name: "status-only update", old: newPCSG(1, nil), new: newPCSG(1, nil), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pred.UpdateFunc(event.UpdateEvent{ObjectOld: tt.old, ObjectNew: tt.new})
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMapPCSToPCSG(t *testing.T) {
	pcsUID := types.UID("pcs-uid")
	pcsgConfig := grovecorev1alpha1.PodCliqueScalingGroupConfig{Name: "workers"}
	newPCS := func(strategy grovecorev1alpha1.UpdateStrategyType, progress *grovecorev1alpha1.PodCliqueSetUpdateProgress) *grovecorev1alpha1.PodCliqueSet {
		return &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{Name: "pcs", Namespace: "default", UID: pcsUID},
			Spec: grovecorev1alpha1.PodCliqueSetSpec{
				Replicas:       3,
				UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: strategy},
				Template:       grovecorev1alpha1.PodCliqueSetTemplateSpec{PodCliqueScalingGroupConfigs: []grovecorev1alpha1.PodCliqueScalingGroupConfig{pcsgConfig}},
			},
			Status: grovecorev1alpha1.PodCliqueSetStatus{UpdateProgress: progress},
		}
	}
	newPCSG := func(replicaIndex int) *grovecorev1alpha1.PodCliqueScalingGroup {
		return testutils.NewPodCliqueScalingGroupBuilder(
			apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: "pcs", Replica: replicaIndex}, pcsgConfig.Name),
			"default", "pcs", replicaIndex,
		).WithOwnerReference(constants.KindPodCliqueSet, "pcs", pcsUID).Build()
	}

	t.Run("maps selected sparse replica", func(t *testing.T) {
		pcs := newPCS(grovecorev1alpha1.RollingRecreateStrategy, &grovecorev1alpha1.PodCliqueSetUpdateProgress{
			CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 4}},
		})
		got := mapPCSToPCSG(testutils.SetupFakeClient(pcs, newPCSG(0), newPCSG(4)))(t.Context(), pcs)
		assert.Equal(t, []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: "default", Name: "pcs-4-workers"}}}, got)
	})

	t.Run("maps all sparse replicas for OnDelete", func(t *testing.T) {
		pcs := newPCS(grovecorev1alpha1.OnDeleteStrategy, &grovecorev1alpha1.PodCliqueSetUpdateProgress{})
		got := mapPCSToPCSG(testutils.SetupFakeClient(pcs, newPCSG(0), newPCSG(4)))(t.Context(), pcs)
		assert.ElementsMatch(t, []reconcile.Request{
			{NamespacedName: types.NamespacedName{Namespace: "default", Name: "pcs-0-workers"}},
			{NamespacedName: types.NamespacedName{Namespace: "default", Name: "pcs-4-workers"}},
		}, got)
	})

	t.Run("maps all sparse replicas without a selected replica", func(t *testing.T) {
		pcs := newPCS(grovecorev1alpha1.RollingRecreateStrategy, &grovecorev1alpha1.PodCliqueSetUpdateProgress{})
		got := mapPCSToPCSG(testutils.SetupFakeClient(pcs, newPCSG(0), newPCSG(4)))(t.Context(), pcs)
		assert.ElementsMatch(t, []reconcile.Request{
			{NamespacedName: types.NamespacedName{Namespace: "default", Name: "pcs-0-workers"}},
			{NamespacedName: types.NamespacedName{Namespace: "default", Name: "pcs-4-workers"}},
		}, got)
	})
}

func TestHasNewPCSReplicaBeenSelectedForUpdate(t *testing.T) {
	progress := func(indices ...int32) *grovecorev1alpha1.PodCliqueSetUpdateProgress {
		result := &grovecorev1alpha1.PodCliqueSetUpdateProgress{}
		for _, index := range indices {
			result.CurrentlyUpdating = append(result.CurrentlyUpdating, grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{ReplicaIndex: index})
		}
		return result
	}

	assert.True(t, hasNewPCSReplicaBeenSelectedForUpdate(nil, progress(4)))
	assert.True(t, hasNewPCSReplicaBeenSelectedForUpdate(progress(1), progress(4)))
	assert.False(t, hasNewPCSReplicaBeenSelectedForUpdate(progress(4), progress(4)))
	assert.False(t, hasNewPCSReplicaBeenSelectedForUpdate(progress(4), progress()))
}

func TestPodCliqueSetRevisionChangeEnqueuesAllPCSGs(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", types.UID("pcs-uid")).
		WithReplicas(2).
		WithScalingGroup("group", []string{"worker"}).
		Build()
	oldPCS := pcs.DeepCopy()
	oldPCS.Status.UpdateRevision = "old-revision"
	pcs.Status.UpdateRevision = "new-revision"
	pcsg0 := testutils.NewPodCliqueScalingGroupBuilder("pcs-0-group", pcs.Namespace, pcs.Name, 0).
		WithOwnerReference(constants.KindPodCliqueSet, pcs.Name, pcs.UID).
		Build()
	pcsg4 := testutils.NewPodCliqueScalingGroupBuilder("pcs-4-group", pcs.Namespace, pcs.Name, 4).
		WithOwnerReference(constants.KindPodCliqueSet, pcs.Name, pcs.UID).
		Build()
	cl := testutils.SetupFakeClient(pcs, pcsg0, pcsg4)

	pred, ok := podCliqueSetPredicate().(predicate.Funcs)
	require.True(t, ok)
	assert.True(t, pred.UpdateFunc(event.UpdateEvent{ObjectOld: oldPCS, ObjectNew: pcs}))
	assert.ElementsMatch(t, []reconcile.Request{
		{NamespacedName: types.NamespacedName{Namespace: pcs.Namespace, Name: "pcs-0-group"}},
		{NamespacedName: types.NamespacedName{Namespace: pcs.Namespace, Name: "pcs-4-group"}},
	}, mapPCSToPCSG(cl)(t.Context(), pcs))
}

// TestPodGangMapPredicate verifies the PodGangMap watch predicate. It must fire on Create so a
// reconstructed PodGangMap that already carries a multi-anchor distribution is processed, and on an
// Update that moves a PodCliqueScalingGroup replica index between entries or changes the indices an
// entry carries. It must not fire when only the standalone PodClique counts change, which the
// PodCliqueScalingGroup controller does not consume.
func TestPodGangMapPredicate(t *testing.T) {
	const ns, pcsName, hash = "default", "pcs", "gen"
	pred, ok := podGangMapPredicate().(predicate.Funcs)
	require.True(t, ok, "predicate must be predicate.Funcs")

	pgmWith := func(entries ...grovecorev1alpha1.PodGangEntry) *grovecorev1alpha1.PodGangMap {
		return testutils.NewPodGangMapBuilder(pcsName, ns, "pcs-uid", 0).WithEntries(entries...).Build()
	}
	anchor := func(epoch string, indices map[string][]int32) grovecorev1alpha1.PodGangEntry {
		return testutils.NewPodGangEntryBuilder(hash, epoch).
			WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
			WithPCSGReplicaIndices(indices).
			Build()
	}

	tests := []struct {
		name     string
		isCreate bool
		old      *grovecorev1alpha1.PodGangMap
		new      *grovecorev1alpha1.PodGangMap
		want     bool
	}{
		{
			name:     "create always fires",
			isCreate: true,
			new:      pgmWith(anchor("100", map[string][]int32{"sga": {0, 1, 2}})),
			want:     true,
		},
		{
			name: "same-total redistribution of replica indices across entries fires",
			old:  pgmWith(anchor("100", map[string][]int32{"sga": {0, 1, 2}})),
			new: pgmWith(
				anchor("100", map[string][]int32{"sga": {0}}),
				anchor("200", map[string][]int32{"sga": {1, 2}}),
			),
			want: true,
		},
		{
			name: "indices carried by an entry change fires",
			old:  pgmWith(anchor("100", map[string][]int32{"sga": {0, 1, 2}})),
			new:  pgmWith(anchor("100", map[string][]int32{"sga": {0, 1}})),
			want: true,
		},
		{
			name: "a config moving entirely to a new epoch fires",
			old:  pgmWith(anchor("100", map[string][]int32{"sga": {0, 1}})),
			new:  pgmWith(anchor("200", map[string][]int32{"sga": {0, 1}})),
			want: true,
		},
		{
			name: "unchanged placement does not fire",
			old:  pgmWith(anchor("100", map[string][]int32{"sga": {0, 1, 2}})),
			new:  pgmWith(anchor("100", map[string][]int32{"sga": {0, 1, 2}})),
			want: false,
		},
		{
			name: "only standalone PodClique counts change does not fire",
			old: pgmWith(testutils.NewPodGangEntryBuilder(hash, "100").
				WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
				WithPodCliques(map[string]int32{"frontend": 6}).
				WithPCSGReplicaIndices(map[string][]int32{"sga": {0, 1, 2}}).Build()),
			new: pgmWith(testutils.NewPodGangEntryBuilder(hash, "100").
				WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
				WithPodCliques(map[string]int32{"frontend": 3}).
				WithPCSGReplicaIndices(map[string][]int32{"sga": {0, 1, 2}}).Build()),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got bool
			if tt.isCreate {
				got = pred.CreateFunc(event.CreateEvent{Object: tt.new})
			} else {
				got = pred.UpdateFunc(event.UpdateEvent{ObjectOld: tt.old, ObjectNew: tt.new})
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestMapPodGangMapToPCSGs verifies that the PodGangMap mapper enqueues one reconcile request per
// PodCliqueScalingGroup config named across the entries, using the PodCliqueSet name from the
// part-of label and the replica index from the spec.
func TestMapPodGangMapToPCSGs(t *testing.T) {
	const ns, pcsName, hash = "default", "pcs", "gen"
	mapFn := mapPodGangMapToPCSGs()

	pgm := testutils.NewPodGangMapBuilder(pcsName, ns, "pcs-uid", 0).WithEntries(
		testutils.NewPodGangEntryBuilder(hash, "100").
			WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
			WithPCSGReplicaIndices(map[string][]int32{"sga": {0, 1}, "sgb": {0}}).Build(),
		testutils.NewPodGangEntryBuilder(hash, "200").
			WithRole(grovecorev1alpha1.PodGangEntryRoleTail).
			WithPCSGReplicaIndices(map[string][]int32{"sga": {2}}).Build(),
	).Build()

	actual := mapFn(t.Context(), pgm)

	expected := []reconcile.Request{
		{NamespacedName: types.NamespacedName{Namespace: ns, Name: "pcs-0-sga"}},
		{NamespacedName: types.NamespacedName{Namespace: ns, Name: "pcs-0-sgb"}},
	}
	assert.ElementsMatch(t, expected, actual)
}

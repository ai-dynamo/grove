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

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

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
	anchor := func(epoch string, anchorIndex int32, indices map[string][]int32) grovecorev1alpha1.PodGangEntry {
		return testutils.NewPodGangEntryBuilder(hash, epoch).
			WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
			WithAnchorIndex(anchorIndex).
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
			new:      pgmWith(anchor("100", 0, map[string][]int32{"sga": {0, 1, 2}})),
			want:     true,
		},
		{
			name: "same-total redistribution of replica indices across entries fires",
			old:  pgmWith(anchor("100", 0, map[string][]int32{"sga": {0, 1, 2}})),
			new: pgmWith(
				anchor("100", 0, map[string][]int32{"sga": {0}}),
				anchor("200", 1, map[string][]int32{"sga": {1, 2}}),
			),
			want: true,
		},
		{
			name: "indices carried by an entry change fires",
			old:  pgmWith(anchor("100", 0, map[string][]int32{"sga": {0, 1, 2}})),
			new:  pgmWith(anchor("100", 0, map[string][]int32{"sga": {0, 1}})),
			want: true,
		},
		{
			name: "a config moving entirely to a new epoch fires",
			old:  pgmWith(anchor("100", 0, map[string][]int32{"sga": {0, 1}})),
			new:  pgmWith(anchor("200", 0, map[string][]int32{"sga": {0, 1}})),
			want: true,
		},
		{
			name: "unchanged placement does not fire",
			old:  pgmWith(anchor("100", 0, map[string][]int32{"sga": {0, 1, 2}})),
			new:  pgmWith(anchor("100", 0, map[string][]int32{"sga": {0, 1, 2}})),
			want: false,
		},
		{
			name: "only standalone PodClique counts change does not fire",
			old: pgmWith(testutils.NewPodGangEntryBuilder(hash, "100").
				WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).WithAnchorIndex(0).
				WithPodCliques(map[string]int32{"frontend": 6}).
				WithPCSGReplicaIndices(map[string][]int32{"sga": {0, 1, 2}}).Build()),
			new: pgmWith(testutils.NewPodGangEntryBuilder(hash, "100").
				WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).WithAnchorIndex(0).
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
			WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).WithAnchorIndex(0).
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

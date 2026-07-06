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

package podcliquescalinggroup

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// TestMapPodGangMapToPCSGs verifies that a PodGangMap is fanned out to reconcile.Requests
// for the PodCliqueScalingGroups referenced by its entries.
func TestMapPodGangMapToPCSGs(t *testing.T) {
	const ns = "test-ns"
	pcsOwner := metav1.OwnerReference{Kind: "PodCliqueSet", Name: "my-pcs"}

	pgmWith := func(meta metav1.ObjectMeta, replicaIndex int32, entries []grovecorev1alpha1.PodGangEntry) *grovecorev1alpha1.PodGangMap {
		return &grovecorev1alpha1.PodGangMap{
			ObjectMeta: meta,
			Spec: grovecorev1alpha1.PodGangMapSpec{
				PodCliqueSetReplicaIndex: replicaIndex,
				Entries:                  entries,
			},
		}
	}

	tests := []struct {
		name     string
		obj      client.Object
		wantFQNs []string
	}{
		{
			name: "single entry with two PCSGs",
			obj: pgmWith(
				metav1.ObjectMeta{Name: "my-pcs-0", Namespace: ns, OwnerReferences: []metav1.OwnerReference{pcsOwner}},
				0,
				[]grovecorev1alpha1.PodGangEntry{
					{Name: "my-pcs-0", PCSGReplicaIndices: map[string][]int32{"prefill": {0, 1}, "decode": {0}}},
				},
			),
			wantFQNs: []string{"my-pcs-0-prefill", "my-pcs-0-decode"},
		},
		{
			name: "dedups PCSGs that appear in multiple entries",
			obj: pgmWith(
				metav1.ObjectMeta{Name: "my-pcs-1", Namespace: ns, OwnerReferences: []metav1.OwnerReference{pcsOwner}},
				1,
				[]grovecorev1alpha1.PodGangEntry{
					{Name: "my-pcs-1", PCSGReplicaIndices: map[string][]int32{"prefill": {0, 1}}},
					{Name: "my-pcs-1-tail-0", PCSGReplicaIndices: map[string][]int32{"prefill": {2}}},
				},
			),
			wantFQNs: []string{"my-pcs-1-prefill"},
		},
		{
			name: "uses replica index from spec, not parsed from name",
			// Name suffix says "0" but spec says replica 3 — assert FQNs use 3.
			obj: pgmWith(
				metav1.ObjectMeta{Name: "my-pcs-0", Namespace: ns, OwnerReferences: []metav1.OwnerReference{pcsOwner}},
				3,
				[]grovecorev1alpha1.PodGangEntry{
					{Name: "my-pcs-3", PCSGReplicaIndices: map[string][]int32{"prefill": {0}}},
				},
			),
			wantFQNs: []string{"my-pcs-3-prefill"},
		},
		{
			name: "entry with only PCLQ references emits no PCSG requests",
			obj: pgmWith(
				metav1.ObjectMeta{Name: "my-pcs-0", Namespace: ns, OwnerReferences: []metav1.OwnerReference{pcsOwner}},
				0,
				[]grovecorev1alpha1.PodGangEntry{
					{Name: "my-pcs-0", PodCliques: map[string]int32{"frontend": 2}},
				},
			),
			wantFQNs: nil,
		},
		{
			name: "missing PodCliqueSet owner ref returns empty",
			obj: pgmWith(
				metav1.ObjectMeta{Name: "my-pcs-0", Namespace: ns},
				0,
				[]grovecorev1alpha1.PodGangEntry{
					{Name: "my-pcs-0", PCSGReplicaIndices: map[string][]int32{"prefill": {0}}},
				},
			),
			wantFQNs: nil,
		},
		{
			name:     "object is not a PodGangMap returns empty",
			obj:      &grovecorev1alpha1.PodCliqueSet{ObjectMeta: metav1.ObjectMeta{Name: "my-pcs", Namespace: ns}},
			wantFQNs: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requests := mapPodGangMapToPCSGs()(context.TODO(), tc.obj)
			gotNames := make([]string, 0, len(requests))
			for _, r := range requests {
				assert.Equal(t, ns, r.Namespace)
				gotNames = append(gotNames, r.Name)
			}
			assert.ElementsMatch(t, tc.wantFQNs, gotNames)
		})
	}
}

// TestPodGangMapPredicate verifies that the PodGangMap predicate triggers on Create and on
// spec/generation changes for managed PodGangMaps, skips Delete, and rejects unmanaged objects.
func TestPodGangMapPredicate(t *testing.T) {
	managedLabels := map[string]string{apicommon.LabelManagedByKey: apicommon.LabelManagedByValue}
	pcsOwner := []metav1.OwnerReference{{Kind: "PodCliqueSet", Name: "my-pcs"}}

	managedPGM := func(generation int64) *grovecorev1alpha1.PodGangMap {
		return &grovecorev1alpha1.PodGangMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "my-pcs-0",
				Namespace:       "default",
				Generation:      generation,
				Labels:          managedLabels,
				OwnerReferences: pcsOwner,
			},
		}
	}

	pred := podGangMapPredicate()
	funcs, ok := pred.(predicate.Funcs)
	require.True(t, ok)

	t.Run("create on managed PGM triggers", func(t *testing.T) {
		assert.True(t, funcs.CreateFunc(event.CreateEvent{Object: managedPGM(1)}))
	})

	t.Run("create on unmanaged PGM (missing label) is filtered", func(t *testing.T) {
		pgm := managedPGM(1)
		pgm.Labels = nil
		assert.False(t, funcs.CreateFunc(event.CreateEvent{Object: pgm}))
	})

	t.Run("create on PGM with wrong owner kind is filtered", func(t *testing.T) {
		pgm := managedPGM(1)
		pgm.OwnerReferences = []metav1.OwnerReference{{Kind: "WrongKind", Name: "x"}}
		assert.False(t, funcs.CreateFunc(event.CreateEvent{Object: pgm}))
	})

	t.Run("delete is always skipped", func(t *testing.T) {
		assert.False(t, funcs.DeleteFunc(event.DeleteEvent{Object: managedPGM(1)}))
	})

	t.Run("update with generation change triggers", func(t *testing.T) {
		assert.True(t, funcs.UpdateFunc(event.UpdateEvent{ObjectOld: managedPGM(1), ObjectNew: managedPGM(2)}))
	})

	t.Run("update without generation change is filtered", func(t *testing.T) {
		assert.False(t, funcs.UpdateFunc(event.UpdateEvent{ObjectOld: managedPGM(2), ObjectNew: managedPGM(2)}))
	})

	t.Run("update on unmanaged old object is filtered", func(t *testing.T) {
		oldPGM := managedPGM(1)
		oldPGM.Labels = nil
		assert.False(t, funcs.UpdateFunc(event.UpdateEvent{ObjectOld: oldPGM, ObjectNew: managedPGM(2)}))
	})

	t.Run("generic is always skipped", func(t *testing.T) {
		assert.False(t, funcs.GenericFunc(event.GenericEvent{Object: managedPGM(1)}))
	})
}

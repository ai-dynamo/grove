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

package podclique

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	"github.com/ai-dynamo/grove/operator/internal/expect"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// TestControllerConstants tests the controller constants
func TestControllerConstants(t *testing.T) {
	// Verifies that controller name is set correctly
	assert.Equal(t, "podclique-controller", controllerName)
}

// TestPodPredicate_Delete tests the pod predicate's Delete path for the scenario:
// when a managed pod (e.g. pending) is manually deleted, the informer sees a Delete event before the next reconcile.
// The predicate must call ObserveDeletions so the pod's UID is removed from create expectations (uidsToAdd),
// allowing the controller to recreate the pod on the next reconcile instead of treating it as "informer slow".
func TestPodPredicate_Delete(t *testing.T) {
	const ns, pclqName, podName, podGangName = "default", "pclq-1", "pclq-1-0", "pclq-1-pg-0"
	pclqObjMeta := metav1.ObjectMeta{Namespace: ns, Name: pclqName}
	// The observer records and lowers expectations under the pod's PodGang-scoped key.
	expectationsKey, err := componentutils.PodGangScopedExpectationsStoreKey(pclqObjMeta, podGangName)
	require.NoError(t, err)

	t.Run("managed pod with PodClique owner: ObserveDeletions removes UID from create expectations so pod can be recreated", func(t *testing.T) {
		store := expect.NewExpectationsStore()
		podUID := types.UID("pod-deleted-manually")
		require.NoError(t, store.ExpectCreations(logr.Discard(), expectationsKey, podUID))

		createExpectations := store.GetCreateExpectations(expectationsKey)
		require.Contains(t, createExpectations, podUID, "setup: create expectation should contain pod UID")

		r := &Reconciler{expectationsStore: store}
		pred := r.podPredicate()
		pod := testutils.NewPodBuilder(podName, ns).
			WithOwner(pclqName).
			WithLabels(map[string]string{
				apicommon.LabelManagedByKey: apicommon.LabelManagedByValue,
				apicommon.LabelPodGang:      podGangName,
			}).
			Build()
		pod.UID = podUID

		funcs, ok := pred.(predicate.Funcs)
		require.True(t, ok, "predicate must be predicate.Funcs")
		result := funcs.DeleteFunc(event.DeleteEvent{Object: pod})

		createExpectationsAfter := store.GetCreateExpectations(expectationsKey)
		assert.NotContains(t, createExpectationsAfter, podUID,
			"ObserveDeletions should remove the deleted pod UID from uidsToAdd so next reconcile can recreate the pod")
		assert.True(t, result, "predicate should allow the event so the handler enqueues reconcile")
	})
}

// TestPodPredicateUpdate verifies the pod predicate's Update path. A managed pod whose Scheduled or
// Ready condition transitions must enqueue a reconcile so PodClique.Status.ScheduledReplicas and
// ReadyReplicas stay current. Updates with no relevant status change, a spec change, or an
// unmanaged pod must not enqueue.
func TestPodPredicateUpdate(t *testing.T) {
	const ns, pclqName, podName = "default", "pclq-1", "pclq-1-0"
	r := &Reconciler{expectationsStore: expect.NewExpectationsStore()}
	pred, ok := r.podPredicate().(predicate.Funcs)
	require.True(t, ok, "predicate must be predicate.Funcs")

	managedPod := func(conds ...corev1.PodCondition) *corev1.Pod {
		b := testutils.NewPodBuilder(podName, ns).
			WithOwner(pclqName).
			WithLabels(map[string]string{apicommon.LabelManagedByKey: apicommon.LabelManagedByValue})
		for _, c := range conds {
			b = b.WithCondition(c)
		}
		return b.Build()
	}
	scheduled := corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}
	notScheduled := corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionFalse}
	ready := corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}
	notReady := corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionFalse}

	tests := []struct {
		name     string
		oldPod   *corev1.Pod
		newPod   *corev1.Pod
		wantFire bool
	}{
		{
			name:     "Scheduled False to True enqueues",
			oldPod:   managedPod(notScheduled),
			newPod:   managedPod(scheduled),
			wantFire: true,
		},
		{
			name:     "Scheduled condition appears as True enqueues",
			oldPod:   managedPod(),
			newPod:   managedPod(scheduled),
			wantFire: true,
		},
		{
			name:     "Ready False to True enqueues",
			oldPod:   managedPod(scheduled, notReady),
			newPod:   managedPod(scheduled, ready),
			wantFire: true,
		},
		{
			name:     "no relevant status change does not enqueue",
			oldPod:   managedPod(scheduled, ready),
			newPod:   managedPod(scheduled, ready),
			wantFire: false,
		},
		{
			name:     "unmanaged pod does not enqueue",
			oldPod:   testutils.NewPodBuilder(podName, ns).WithCondition(notScheduled).Build(),
			newPod:   testutils.NewPodBuilder(podName, ns).WithCondition(scheduled).Build(),
			wantFire: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantFire, pred.UpdateFunc(event.UpdateEvent{ObjectOld: tt.oldPod, ObjectNew: tt.newPod}))
		})
	}

	t.Run("spec change does not enqueue even with a Scheduled transition", func(t *testing.T) {
		oldPod := managedPod(notScheduled)
		newPod := managedPod(scheduled)
		newPod.Generation = oldPod.Generation + 1
		assert.False(t, pred.UpdateFunc(event.UpdateEvent{ObjectOld: oldPod, ObjectNew: newPod}))
	})
}

// TestPodCliqueSetPredicateCurrentlyUpdatingReplicaChanges verifies that the PodCliqueSet
// watch predicate enqueues PodClique reconciles when the replica currently being rolled out
// changes.
func TestPodCliqueSetPredicateCurrentlyUpdatingReplicaChanges(t *testing.T) {
	pred, ok := podCliqueSetPredicate().(predicate.Funcs)
	require.True(t, ok, "predicate must be predicate.Funcs")

	tests := []struct {
		name        string
		oldProgress *grovecorev1alpha1.PodCliqueSetUpdateProgress
		newProgress *grovecorev1alpha1.PodCliqueSetUpdateProgress
		want        bool
	}{
		{
			name: "currently updating starts",
			newProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}},
			},
			want: true,
		},
		{
			name: "currently updating clears",
			oldProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}},
			},
			newProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{},
			want:        true,
		},
		{
			name: "currently updating moves",
			oldProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}},
			},
			newProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 1}},
			},
			want: true,
		},
		{
			name: "currently updating unchanged",
			oldProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}},
			},
			newProgress: &grovecorev1alpha1.PodCliqueSetUpdateProgress{
				CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pred.UpdateFunc(event.UpdateEvent{
				ObjectOld: &grovecorev1alpha1.PodCliqueSet{Status: grovecorev1alpha1.PodCliqueSetStatus{CurrentGenerationHash: ptr.To("generation"), UpdateProgress: tt.oldProgress}},
				ObjectNew: &grovecorev1alpha1.PodCliqueSet{Status: grovecorev1alpha1.PodCliqueSetStatus{CurrentGenerationHash: ptr.To("generation"), UpdateProgress: tt.newProgress}},
			})
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestPodCliqueScalingGroupPredicateGenerationStatusChanges verifies that the
// PodCliqueScalingGroup watch predicate triggers PodClique reconciles when the PCSG's view
// of the PodCliqueSet generation changes during a rolling update.
func TestPodCliqueScalingGroupPredicateGenerationStatusChanges(t *testing.T) {
	pred, ok := podCliqueScalingGroupPredicate().(predicate.Funcs)
	require.True(t, ok, "predicate must be predicate.Funcs")

	tests := []struct {
		name    string
		oldPCSG *grovecorev1alpha1.PodCliqueScalingGroup
		newPCSG *grovecorev1alpha1.PodCliqueScalingGroup
		want    bool
	}{
		{
			name: "current generation changes",
			oldPCSG: &grovecorev1alpha1.PodCliqueScalingGroup{Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
				CurrentPodCliqueSetGenerationHash: ptr.To("old-generation"),
			}},
			newPCSG: &grovecorev1alpha1.PodCliqueScalingGroup{Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
				CurrentPodCliqueSetGenerationHash: ptr.To("new-generation"),
			}},
			want: true,
		},
		{
			name: "equal current generation values with different pointers do not enqueue",
			oldPCSG: &grovecorev1alpha1.PodCliqueScalingGroup{Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
				CurrentPodCliqueSetGenerationHash: ptr.To("generation"),
			}},
			newPCSG: &grovecorev1alpha1.PodCliqueScalingGroup{Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
				CurrentPodCliqueSetGenerationHash: ptr.To("generation"),
			}},
			want: false,
		},
		{
			name: "update target generation changes",
			oldPCSG: &grovecorev1alpha1.PodCliqueScalingGroup{Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
				UpdateProgress: &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{PodCliqueSetGenerationHash: "old-generation"},
			}},
			newPCSG: &grovecorev1alpha1.PodCliqueScalingGroup{Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
				UpdateProgress: &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{PodCliqueSetGenerationHash: "new-generation"},
			}},
			want: true,
		},
		{
			name: "generation status unchanged",
			oldPCSG: &grovecorev1alpha1.PodCliqueScalingGroup{Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
				CurrentPodCliqueSetGenerationHash: ptr.To("generation"),
				UpdateProgress:                    &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{PodCliqueSetGenerationHash: "generation"},
			}},
			newPCSG: &grovecorev1alpha1.PodCliqueScalingGroup{Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
				CurrentPodCliqueSetGenerationHash: ptr.To("generation"),
				UpdateProgress:                    &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{PodCliqueSetGenerationHash: "generation"},
			}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pred.UpdateFunc(event.UpdateEvent{ObjectOld: tt.oldPCSG, ObjectNew: tt.newPCSG})
			assert.Equal(t, tt.want, got)
		})
	}
}

// Test_isMarkedForDeletion tests if a deletion timestamp is set on the pod
func Test_isMarkedForDeletion(t *testing.T) {
	now := ptr.To(metav1.Now())
	tests := []struct {
		name        string
		updateEvent event.UpdateEvent
		want        bool
	}{
		{
			name: "deletion timestamp set on the pod in the update",
			updateEvent: event.UpdateEvent{
				ObjectOld: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						DeletionTimestamp: nil,
					},
				},
				ObjectNew: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						DeletionTimestamp: now,
					},
				},
			},
			want: true,
		},
		{
			name: "deletion timestamp not set on the pod in the update",
			updateEvent: event.UpdateEvent{
				ObjectOld: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						DeletionTimestamp: nil,
					},
				},
				ObjectNew: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						DeletionTimestamp: nil,
					},
				},
			},
			want: false,
		},
		{
			name: "deletion timestamp was already set on the pod before the update",
			updateEvent: event.UpdateEvent{
				ObjectOld: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						DeletionTimestamp: now,
					},
				},
				ObjectNew: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						DeletionTimestamp: now,
					},
				},
			},
			want: false,
		},
		{
			name: "objects are not pods (type cast fails)",
			updateEvent: event.UpdateEvent{
				ObjectOld: &corev1.ConfigMap{},
				ObjectNew: &corev1.ConfigMap{},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, isMarkedForDeletion(tt.updateEvent), "isMarkedForDeletionChanged(%v)", tt.updateEvent)
		})
	}
}

// TestPodGangMapPredicate verifies the PodGangMap watch predicate. It must fire on Create so a
// reconstructed PodGangMap that already carries a multi-anchor distribution is processed, and on an
// Update that moves a standalone PodClique between entries or changes its count. It must not fire
// when only PodCliqueScalingGroup replica indices change, which the PodClique controller does not
// consume.
func TestPodGangMapPredicate(t *testing.T) {
	const ns, pcsName, hash = "default", "pcs", "gen"
	pred, ok := podGangMapPredicate().(predicate.Funcs)
	require.True(t, ok, "predicate must be predicate.Funcs")

	pgmWith := func(entries ...grovecorev1alpha1.PodGangEntry) *grovecorev1alpha1.PodGangMap {
		return testutils.NewPodGangMapBuilder(pcsName, ns, "pcs-uid", 0).WithEntries(entries...).Build()
	}
	anchor := func(epoch string, anchorIndex int32, podCliques map[string]int32) grovecorev1alpha1.PodGangEntry {
		return testutils.NewPodGangEntryBuilder(hash, epoch).
			WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
			WithAnchorIndex(anchorIndex).
			WithPodCliques(podCliques).
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
			new:      pgmWith(anchor("100", 0, map[string]int32{"frontend": 6})),
			want:     true,
		},
		{
			name: "same-total redistribution across anchors fires",
			old:  pgmWith(anchor("100", 0, map[string]int32{"frontend": 6})),
			new: pgmWith(
				anchor("100", 0, map[string]int32{"frontend": 3}),
				anchor("200", 1, map[string]int32{"frontend": 3}),
			),
			want: true,
		},
		{
			name: "count change on the same anchor fires",
			old:  pgmWith(anchor("100", 0, map[string]int32{"frontend": 6})),
			new:  pgmWith(anchor("100", 0, map[string]int32{"frontend": 5})),
			want: true,
		},
		{
			name: "a standalone PodClique moving entirely to a new epoch fires",
			old:  pgmWith(anchor("100", 0, map[string]int32{"frontend": 3})),
			new:  pgmWith(anchor("200", 0, map[string]int32{"frontend": 3})),
			want: true,
		},
		{
			name: "unchanged distribution does not fire",
			old:  pgmWith(anchor("100", 0, map[string]int32{"frontend": 6})),
			new:  pgmWith(anchor("100", 0, map[string]int32{"frontend": 6})),
			want: false,
		},
		{
			name: "only PodCliqueScalingGroup indices change does not fire",
			old: pgmWith(testutils.NewPodGangEntryBuilder(hash, "100").
				WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).WithAnchorIndex(0).
				WithPodCliques(map[string]int32{"frontend": 6}).
				WithPCSGReplicaIndices(map[string][]int32{"sga": {0, 1, 2}}).Build()),
			new: pgmWith(testutils.NewPodGangEntryBuilder(hash, "100").
				WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).WithAnchorIndex(0).
				WithPodCliques(map[string]int32{"frontend": 6}).
				WithPCSGReplicaIndices(map[string][]int32{"sga": {0, 1}}).Build()),
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

// TestMapPodGangMapToPCLQs verifies that the PodGangMap mapper enqueues one reconcile request per
// standalone PodClique named across the entries, using the PodCliqueSet name from the part-of label
// and the replica index from the spec.
func TestMapPodGangMapToPCLQs(t *testing.T) {
	const ns, pcsName, hash = "default", "pcs", "gen"
	mapFn := mapPodGangMapToPCLQs()

	pgm := testutils.NewPodGangMapBuilder(pcsName, ns, "pcs-uid", 0).WithEntries(
		testutils.NewPodGangEntryBuilder(hash, "100").
			WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).WithAnchorIndex(0).
			WithPodCliques(map[string]int32{"frontend": 3, "backend": 2}).Build(),
		testutils.NewPodGangEntryBuilder(hash, "200").
			WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).WithAnchorIndex(1).
			WithPodCliques(map[string]int32{"frontend": 3}).Build(),
	).Build()

	actual := mapFn(t.Context(), pgm)

	expected := []reconcile.Request{
		{NamespacedName: types.NamespacedName{Namespace: ns, Name: "pcs-0-frontend"}},
		{NamespacedName: types.NamespacedName{Namespace: ns, Name: "pcs-0-backend"}},
	}
	assert.ElementsMatch(t, expected, actual)
}

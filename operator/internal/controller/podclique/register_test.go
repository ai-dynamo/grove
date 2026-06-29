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

package podclique

import (
	"testing"

	"github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
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
	const ns, pclqName, podName = "default", "pclq-1", "pclq-1-0"
	pclqKey, err := expect.ControlleeKeyFunc(&grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: pclqName}})
	require.NoError(t, err)

	t.Run("managed pod with PodClique owner: ObserveDeletions removes UID from create expectations so pod can be recreated", func(t *testing.T) {
		store := expect.NewExpectationsStore()
		podUID := types.UID("pod-deleted-manually")
		require.NoError(t, store.ExpectCreations(logr.Discard(), pclqKey, podUID))

		createExpectations := store.GetCreateExpectations(pclqKey)
		require.Contains(t, createExpectations, podUID, "setup: create expectation should contain pod UID")

		r := &Reconciler{expectationsStore: store}
		pred := r.podPredicate()
		pod := testutils.NewPodBuilder(podName, ns).
			WithOwner(pclqName).
			WithLabels(map[string]string{common.LabelManagedByKey: common.LabelManagedByValue}).
			Build()
		pod.UID = podUID

		funcs, ok := pred.(predicate.Funcs)
		require.True(t, ok, "predicate must be predicate.Funcs")
		result := funcs.DeleteFunc(event.DeleteEvent{Object: pod})

		createExpectationsAfter := store.GetCreateExpectations(pclqKey)
		assert.NotContains(t, createExpectationsAfter, podUID,
			"ObserveDeletions should remove the deleted pod UID from uidsToAdd so next reconcile can recreate the pod")
		assert.True(t, result, "predicate should allow the event so the handler enqueues reconcile")
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

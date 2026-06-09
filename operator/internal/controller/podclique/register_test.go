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

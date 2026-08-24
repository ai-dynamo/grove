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

package podgang

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// predicateTestCase describes a scenario and expected predicate result per event type.
type predicateTestCase struct {
	name                    string
	managedOld              bool
	managedNew              bool
	generationChanged       bool
	shouldAllowCreateEvent  bool
	shouldAllowDeleteEvent  bool
	shouldAllowGenericEvent bool
	shouldAllowUpdateEvent  bool
}

func TestPodGangChangePredicate(t *testing.T) {
	pred := podGangChangePredicate()

	tests := []predicateTestCase{
		{
			name:                    "managed PodGang create",
			managedOld:              true,
			managedNew:              true,
			shouldAllowCreateEvent:  true,
			shouldAllowDeleteEvent:  true,
			shouldAllowGenericEvent: false,
			shouldAllowUpdateEvent:  false,
		},
		{
			name:                    "unmanaged PodGang create",
			managedOld:              false,
			managedNew:              false,
			shouldAllowCreateEvent:  false,
			shouldAllowDeleteEvent:  false,
			shouldAllowGenericEvent: false,
			shouldAllowUpdateEvent:  false,
		},
		{
			name:                    "managed PodGang update with spec change (generation changed)",
			managedOld:              true,
			managedNew:              true,
			generationChanged:       true,
			shouldAllowCreateEvent:  true,
			shouldAllowDeleteEvent:  true,
			shouldAllowGenericEvent: false,
			shouldAllowUpdateEvent:  true,
		},
		{
			name:                    "managed PodGang update with status-only change (generation unchanged)",
			managedOld:              true,
			managedNew:              true,
			generationChanged:       false,
			shouldAllowCreateEvent:  true,
			shouldAllowDeleteEvent:  true,
			shouldAllowGenericEvent: false,
			shouldAllowUpdateEvent:  false,
		},
		{
			name:                    "update with old managed and new unmanaged",
			managedOld:              true,
			managedNew:              false,
			generationChanged:       true,
			shouldAllowCreateEvent:  false, // Create/Delete use newPG which is unmanaged
			shouldAllowDeleteEvent:  false,
			shouldAllowGenericEvent: false,
			shouldAllowUpdateEvent:  false,
		},
		{
			name:                    "update with old unmanaged and new managed",
			managedOld:              false,
			managedNew:              true,
			generationChanged:       true,
			shouldAllowCreateEvent:  true, // Create/Delete use newPG which is managed
			shouldAllowDeleteEvent:  true,
			shouldAllowGenericEvent: false,
			shouldAllowUpdateEvent:  false, // old is unmanaged
		},
		{
			name:                    "generic event always rejected",
			managedOld:              true,
			managedNew:              true,
			shouldAllowCreateEvent:  true,
			shouldAllowDeleteEvent:  true,
			shouldAllowGenericEvent: false,
			shouldAllowUpdateEvent:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldPG := testutils.NewPodGangBuilder("test-pg", "default").
				WithGeneration(1).
				WithManaged(tc.managedOld).
				WithPodGroup("pg0", 1).
				Build()
			newPG := testutils.NewPodGangBuilder("test-pg", "default").
				WithGeneration(1).
				WithManaged(tc.managedNew).
				WithPodGroup("pg0", 1).
				Build()
			if tc.generationChanged {
				newPG.SetGeneration(oldPG.GetGeneration() + 1)
			}

			assert.Equal(t, tc.shouldAllowCreateEvent, pred.Create(event.CreateEvent{Object: newPG}), "Create")
			assert.Equal(t, tc.shouldAllowDeleteEvent, pred.Delete(event.DeleteEvent{Object: newPG}), "Delete")
			assert.Equal(t, tc.shouldAllowGenericEvent, pred.Generic(event.GenericEvent{Object: newPG}), "Generic")
			assert.Equal(t, tc.shouldAllowUpdateEvent, pred.Update(event.UpdateEvent{ObjectOld: oldPG, ObjectNew: newPG}), "Update")
		})
	}
}

func TestPodGangChangePredicateAllowsInitialization(t *testing.T) {
	pred := podGangChangePredicate()
	oldPodGang := testutils.NewPodGangBuilder("test-pg", "default").
		WithGeneration(1).
		WithManaged(true).
		Build()
	oldPodGang.Status.Conditions = []metav1.Condition{{
		Type:   string(groveschedulerv1alpha1.PodGangConditionTypeInitialized),
		Status: metav1.ConditionFalse,
	}}
	newPodGang := oldPodGang.DeepCopy()
	newPodGang.Status.Conditions[0].Status = metav1.ConditionTrue

	assert.True(t, pred.Update(event.UpdateEvent{ObjectOld: oldPodGang, ObjectNew: newPodGang}))
}

func TestPodStatusChangePredicate(t *testing.T) {
	pred := podStatusChangePredicate()
	oldPod := unscheduledPod("worker-0", "default", "test-podgang")
	newPod := oldPod.DeepCopy()
	newPod.Status.Conditions = []corev1.PodCondition{{
		Type:   corev1.PodScheduled,
		Status: corev1.ConditionTrue,
	}}

	assert.True(t, pred.Create(event.CreateEvent{Object: newPod}))
	assert.True(t, pred.Delete(event.DeleteEvent{Object: newPod}))
	assert.True(t, pred.Update(event.UpdateEvent{ObjectOld: oldPod, ObjectNew: newPod}))
	assert.False(t, pred.Generic(event.GenericEvent{Object: newPod}))

	unchangedPod := newPod.DeepCopy()
	unchangedPod.Status.Conditions[0].Message = "diagnostic changed"
	assert.False(t, pred.Update(event.UpdateEvent{ObjectOld: newPod, ObjectNew: unchangedPod}))

	readyPod := newPod.DeepCopy()
	readyPod.Status.Conditions = append(readyPod.Status.Conditions, corev1.PodCondition{
		Type:   corev1.PodReady,
		Status: corev1.ConditionTrue,
	})
	assert.True(t, pred.Update(event.UpdateEvent{ObjectOld: newPod, ObjectNew: readyPod}))
}

func TestMapPodToPodGang(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      "worker-0",
		Namespace: "default",
		Labels:    map[string]string{apicommon.LabelPodGang: "test-podgang"},
	}}
	requests := mapPodToPodGang(t.Context(), pod)
	require.Len(t, requests, 1)
	assert.Equal(t, types.NamespacedName{Namespace: "default", Name: "test-podgang"}, requests[0].NamespacedName)
}

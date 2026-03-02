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

package podgang

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestPodGangSpecChangePredicate(t *testing.T) {
	pred := podGangSpecChangePredicate()

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
			oldPG := createPodGang("test-pg", 1, tc.managedOld)
			newPG := createPodGang("test-pg", 1, tc.managedNew)
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

func createPodGang(name string, generation int64, managed bool) *groveschedulerv1alpha1.PodGang {
	labels := make(map[string]string)
	if managed {
		labels[apicommon.LabelManagedByKey] = apicommon.LabelManagedByValue
	}
	return &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "default",
			Labels:     labels,
			Generation: generation,
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{Name: "pg0", MinReplicas: 1},
			},
		},
	}
}

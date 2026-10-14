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
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
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

func TestPodGangFirstScheduledPredicate(t *testing.T) {
	pred := podGangFirstScheduledPredicate()
	unscheduled := testutils.NewPodGangBuilder("test-pg", "default").WithManaged(true).Build()
	scheduled := testutils.NewPodGangBuilder("test-pg", "default").WithManaged(true).WithLastScheduled().Build()
	unmanagedUnscheduled := testutils.NewPodGangBuilder("test-pg", "default").Build()
	unmanagedScheduled := testutils.NewPodGangBuilder("test-pg", "default").WithLastScheduled().Build()

	assert.True(t, pred.Update(event.UpdateEvent{ObjectOld: unscheduled, ObjectNew: scheduled}), "first scheduled")
	assert.False(t, pred.Update(event.UpdateEvent{ObjectOld: scheduled, ObjectNew: scheduled}), "already scheduled")
	assert.False(t, pred.Update(event.UpdateEvent{ObjectOld: unscheduled, ObjectNew: unscheduled}), "still unscheduled")
	assert.False(t, pred.Update(event.UpdateEvent{ObjectOld: unmanagedUnscheduled, ObjectNew: unmanagedScheduled}), "unmanaged")
	assert.False(t, pred.Create(event.CreateEvent{Object: scheduled}), "Create")
	assert.False(t, pred.Delete(event.DeleteEvent{Object: scheduled}), "Delete")
	assert.False(t, pred.Generic(event.GenericEvent{Object: scheduled}), "Generic")
}

func TestMapPodGangToDependentPodGangs(t *testing.T) {
	const (
		pcsName     = "demo"
		namespace   = "default"
		anchorEpoch = "1000"
		tailEpoch   = "1001"
	)
	pgm := testutils.NewPodGangMapBuilder(pcsName, namespace, "pcs-uid", 0).
		WithEntries(
			testutils.NewAnchorEntry("hash", anchorEpoch, "sg", 0),
			testutils.NewPodGangEntryBuilder("hash", tailEpoch).
				WithRole(grovecorev1alpha1.PodGangEntryRoleTail).
				WithPCSGReplicaIndices(map[string][]int32{"sg": {1, 2}}).
				WithDependsOn(anchorEpoch).
				Build(),
		).
		Build()
	cl := testutils.NewTestClientBuilder().WithObjects(pgm).Build()
	mapFn := mapPodGangToDependentPodGangs(cl)
	podGangAtEpoch := func(epoch string) *groveschedulerv1alpha1.PodGang {
		return testutils.NewPodGangBuilder("pg", namespace).WithLabels(map[string]string{
			apicommon.LabelPartOfKey:                pcsName,
			apicommon.LabelPodCliqueSetReplicaIndex: "0",
			apicommon.LabelEpoch:                    epoch,
		}).Build()
	}

	got := mapFn(context.Background(), podGangAtEpoch(anchorEpoch))
	assert.ElementsMatch(t, []reconcile.Request{
		{NamespacedName: types.NamespacedName{Namespace: namespace, Name: "demo-0-1001-sg-1"}},
		{NamespacedName: types.NamespacedName{Namespace: namespace, Name: "demo-0-1001-sg-2"}},
	}, got, "the anchor maps to the tail PodGangs that depend on it")
	assert.Empty(t, mapFn(context.Background(), podGangAtEpoch(tailEpoch)), "nothing depends on the tail")
	assert.Empty(t, mapFn(context.Background(), testutils.NewPodGangBuilder("legacy", namespace).Build()), "a PodGang without an epoch has no dependents")
}

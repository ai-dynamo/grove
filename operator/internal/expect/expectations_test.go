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

package expect

import (
	"fmt"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
)

const controlleeKey = "test-ns/test-resource"

func TestExpectCreations(t *testing.T) {
	testCases := []struct {
		description                   string
		existingUIDs                  []types.UID
		newUIDs                       []types.UID
		expectedCreateExpectationUIDs []types.UID
	}{
		{
			description:                   "should create new expectation when none exists",
			existingUIDs:                  nil,
			newUIDs:                       []types.UID{"1", "2"},
			expectedCreateExpectationUIDs: []types.UID{"1", "2"},
		},
		{
			description:                   "should add unique new expectations when there are existing expectations",
			existingUIDs:                  []types.UID{"1", "2"},
			newUIDs:                       []types.UID{"1", "3", "4"},
			expectedCreateExpectationUIDs: []types.UID{"1", "2", "3", "4"},
		},
	}

	expStore := NewExpectationsStore()
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			if tc.existingUIDs != nil {
				assert.NoError(t, initializeControlleeExpectations(expStore, controlleeKey, tc.existingUIDs, nil))
			}
			for i, uid := range tc.newUIDs {
				assert.NoError(t, expStore.ExpectCreations(logr.Discard(), controlleeKey, uid, i))
			}
			assert.ElementsMatch(t, tc.expectedCreateExpectationUIDs, expStore.GetCreateExpectations(controlleeKey))
		})
	}
}

func TestObserveDeletions(t *testing.T) {
	testCases := []struct {
		description                   string
		existingUIDs                  []types.UID
		observedDeleteExpectationUIDs []types.UID
		expectedDeleteExpectationUIDs []types.UID
	}{
		{
			description:                   "should be no-op if UIDs to delete have already been removed",
			existingUIDs:                  []types.UID{"1", "3", "5"},
			observedDeleteExpectationUIDs: []types.UID{"8"},
			expectedDeleteExpectationUIDs: []types.UID{"1", "3", "5"},
		},
		{
			description:                   "should remove the observed deletions",
			existingUIDs:                  []types.UID{"1", "2", "3", "5"},
			observedDeleteExpectationUIDs: []types.UID{"2", "5"},
			expectedDeleteExpectationUIDs: []types.UID{"1", "3"},
		},
	}

	expStore := NewExpectationsStore()
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			if tc.existingUIDs != nil {
				assert.NoError(t, initializeControlleeExpectations(expStore, controlleeKey, nil, tc.existingUIDs))
			}
			expStore.ObserveDeletions(logr.Discard(), controlleeKey, tc.observedDeleteExpectationUIDs...)
			leftOverActualDeleteExpectations := expStore.GetDeleteExpectations(controlleeKey)
			assert.ElementsMatch(t, tc.expectedDeleteExpectationUIDs, leftOverActualDeleteExpectations)
		})
	}
}

func TestExpectDeletions(t *testing.T) {
	testCases := []struct {
		description                   string
		existingUIDs                  []types.UID
		newUIDs                       []types.UID
		expectedDeleteExpectationUIDs []types.UID
	}{
		{
			description:                   "should create new expectation when none exists",
			existingUIDs:                  nil,
			newUIDs:                       []types.UID{"1", "2"},
			expectedDeleteExpectationUIDs: []types.UID{"1", "2"},
		},
		{
			description:                   "should add unique new expectations when there are existing expectations",
			existingUIDs:                  []types.UID{"1", "2"},
			newUIDs:                       []types.UID{"1", "3", "4"},
			expectedDeleteExpectationUIDs: []types.UID{"1", "2", "3", "4"},
		},
	}

	expStore := NewExpectationsStore()
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			if tc.existingUIDs != nil {
				assert.NoError(t, initializeControlleeExpectations(expStore, controlleeKey, nil, tc.existingUIDs))
			}
			// test the method
			wg := sync.WaitGroup{}
			wg.Add(len(tc.newUIDs))
			for _, uid := range tc.newUIDs {
				go func() {
					defer wg.Done()
					err := expStore.ExpectDeletions(logr.Discard(), controlleeKey, uid)
					assert.NoError(t, err)
				}()
			}
			wg.Wait()
			// compare the expected with actual
			assert.ElementsMatch(t, tc.expectedDeleteExpectationUIDs, expStore.GetDeleteExpectations(controlleeKey))
		})
	}
}

func TestDeleteExpectations(t *testing.T) {
	testCases := []struct {
		description       string
		expectationsExist bool
	}{
		{
			description:       "should be a no-op when expectations do not exist",
			expectationsExist: false,
		},
		{
			description:       "should delete the existing expectations",
			expectationsExist: true,
		},
	}

	expStore := NewExpectationsStore()
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			if tc.expectationsExist {
				assert.NoError(t, initializeControlleeExpectations(expStore,
					controlleeKey,
					[]types.UID{"3"},
					[]types.UID{"1", "2"}))
			}
			err := expStore.DeleteExpectations(logr.Discard(), controlleeKey)
			assert.NoError(t, err)
			_, exists, err := expStore.GetExpectations(controlleeKey)
			assert.NoError(t, err)
			assert.False(t, exists)
		})
	}
}

func TestSyncExpectations(t *testing.T) {
	testCases := []struct {
		description                   string
		controlleeKey                 string
		createExpectationUIDs         []types.UID
		deleteExpectationsUIDs        []types.UID
		existingNonTerminatingUIDs    []types.UID
		existingTerminatingUIDs       []types.UID
		createExpectationUIDsPostSync []types.UID
		deleteExpectationUIDsPostSync []types.UID
	}{
		{
			description:                   "should sync both create and delete expectations",
			controlleeKey:                 controlleeKey,
			createExpectationUIDs:         []types.UID{"1", "2"},
			deleteExpectationsUIDs:        []types.UID{"3", "6"},
			existingNonTerminatingUIDs:    []types.UID{"1", "3", "4", "7"},
			createExpectationUIDsPostSync: []types.UID{"2"},
			deleteExpectationUIDsPostSync: []types.UID{"3"},
		},
		{
			description:                   "should re-add terminating pods",
			controlleeKey:                 controlleeKey,
			createExpectationUIDs:         []types.UID{"1"},
			deleteExpectationsUIDs:        []types.UID{},
			existingNonTerminatingUIDs:    []types.UID{"2", "3"},
			existingTerminatingUIDs:       []types.UID{"4", "5"},
			createExpectationUIDsPostSync: []types.UID{"1"},
			deleteExpectationUIDsPostSync: []types.UID{"4", "5"},
		},
		{
			description:                   "should be a no-op when expectations do not exist",
			controlleeKey:                 "does-not-exist",
			existingNonTerminatingUIDs:    []types.UID{"1", "2"},
			createExpectationUIDsPostSync: []types.UID{},
			deleteExpectationUIDsPostSync: []types.UID{},
		},
	}

	expStore := NewExpectationsStore()
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			assert.NoError(t, initializeControlleeExpectations(expStore, tc.controlleeKey, tc.createExpectationUIDs, tc.deleteExpectationsUIDs))
			expStore.SyncExpectations(tc.controlleeKey, tc.existingNonTerminatingUIDs, tc.existingTerminatingUIDs)
			assert.ElementsMatch(t, tc.createExpectationUIDsPostSync, expStore.GetCreateExpectations(tc.controlleeKey))
			assert.ElementsMatch(t, tc.deleteExpectationUIDsPostSync, expStore.GetDeleteExpectations(tc.controlleeKey))
		})
	}
}

func TestExpectCreations_with_GetCreateExpectationIndices(t *testing.T) {
	expStore := NewExpectationsStore()
	assert.NoError(t, expStore.ExpectCreations(logr.Discard(), controlleeKey, types.UID("uid-1"), 2))
	assert.NoError(t, expStore.ExpectCreations(logr.Discard(), controlleeKey, types.UID("uid-2"), 3))

	assert.ElementsMatch(t, []types.UID{"uid-1", "uid-2"}, expStore.GetCreateExpectations(controlleeKey))
	indices := expStore.GetCreateExpectationIndices(controlleeKey)
	assert.Len(t, indices, 2)
	assert.ElementsMatch(t, []int{2, 3}, indices)

	// After sync (observe uid-1), only uid-2's index remains reserved
	expStore.SyncExpectations(controlleeKey, []types.UID{"uid-1"}, nil)
	assert.ElementsMatch(t, []int{3}, expStore.GetCreateExpectationIndices(controlleeKey))
}

func TestGetCreateExpectationIndices_NilIndices(t *testing.T) {
	// When expectations exist but no indices were recorded (e.g. via initializeControlleeExpectations),
	// GetCreateExpectationIndices should return nil.
	expStore := NewExpectationsStore()
	assert.NoError(t, initializeControlleeExpectations(expStore, controlleeKey, []types.UID{"uid-1", "uid-2"}, nil))

	indices := expStore.GetCreateExpectationIndices(controlleeKey)
	assert.Nil(t, indices)
}

func TestGetCreateExpectationIndices_NoExpectations(t *testing.T) {
	expStore := NewExpectationsStore()
	indices := expStore.GetCreateExpectationIndices("nonexistent-key")
	assert.Nil(t, indices)
}

func TestLowerExpectations_CleansCreationIndices(t *testing.T) {
	// Verify that when CreationObserved is called (via lowerExpectations/ObserveDeletions path for add UIDs),
	// the corresponding creationIndices entries are removed.
	expStore := NewExpectationsStore()
	assert.NoError(t, expStore.ExpectCreations(logr.Discard(), controlleeKey, types.UID("uid-1"), 10))
	assert.NoError(t, expStore.ExpectCreations(logr.Discard(), controlleeKey, types.UID("uid-2"), 20))
	assert.NoError(t, expStore.ExpectCreations(logr.Discard(), controlleeKey, types.UID("uid-3"), 30))

	// Verify all 3 indices are present
	assert.ElementsMatch(t, []int{10, 20, 30}, expStore.GetCreateExpectationIndices(controlleeKey))

	// Sync with uid-1 observed (it appears in non-terminating UIDs)
	expStore.SyncExpectations(controlleeKey, []types.UID{"uid-1"}, nil)

	// uid-1's index (10) should be gone
	assert.ElementsMatch(t, []int{20, 30}, expStore.GetCreateExpectationIndices(controlleeKey))
	assert.ElementsMatch(t, []types.UID{"uid-2", "uid-3"}, expStore.GetCreateExpectations(controlleeKey))
}

func TestExpectCreations_OverwriteIndex(t *testing.T) {
	// If the same UID is registered again with a different index, the new index should overwrite.
	expStore := NewExpectationsStore()
	assert.NoError(t, expStore.ExpectCreations(logr.Discard(), controlleeKey, types.UID("uid-1"), 5))
	assert.ElementsMatch(t, []int{5}, expStore.GetCreateExpectationIndices(controlleeKey))

	// Re-register same UID with different index
	assert.NoError(t, expStore.ExpectCreations(logr.Discard(), controlleeKey, types.UID("uid-1"), 99))
	indices := expStore.GetCreateExpectationIndices(controlleeKey)
	assert.Len(t, indices, 1)
	assert.Equal(t, 99, indices[0])
}

func TestExpectCreationsAndDeletions_Interleaved(t *testing.T) {
	// Test the scenario where a UID is added and then deleted in the same expectations set.
	// The delete should remove the UID from uidsToAdd and clean up its creationIndex.
	expStore := NewExpectationsStore()

	// Create expectations with indices
	assert.NoError(t, expStore.ExpectCreations(logr.Discard(), controlleeKey, types.UID("uid-1"), 10))
	assert.NoError(t, expStore.ExpectCreations(logr.Discard(), controlleeKey, types.UID("uid-2"), 20))

	// Now expect deletion of uid-1 (this removes uid-1 from uidsToAdd)
	assert.NoError(t, expStore.ExpectDeletions(logr.Discard(), controlleeKey, types.UID("uid-1")))

	// uid-1 should be removed from create expectations and its index cleaned up
	assert.ElementsMatch(t, []types.UID{"uid-2"}, expStore.GetCreateExpectations(controlleeKey))
	assert.ElementsMatch(t, []int{20}, expStore.GetCreateExpectationIndices(controlleeKey))
}

func TestFullLifecycle_CreateExpectation_GetIndices_Observe_Cleanup(t *testing.T) {
	// Integration-style test: full lifecycle
	expStore := NewExpectationsStore()

	// Step 1: Reconciler creates 3 pods with indices 0, 1, 2
	assert.NoError(t, expStore.ExpectCreations(logr.Discard(), controlleeKey, types.UID("pod-uid-a"), 0))
	assert.NoError(t, expStore.ExpectCreations(logr.Discard(), controlleeKey, types.UID("pod-uid-b"), 1))
	assert.NoError(t, expStore.ExpectCreations(logr.Discard(), controlleeKey, types.UID("pod-uid-c"), 2))

	// Step 2: Next reconcile starts, informer cache has pod-uid-a but not b and c yet
	// GetCreateExpectationIndices should return all 3 indices as reserved
	allIndices := expStore.GetCreateExpectationIndices(controlleeKey)
	assert.Len(t, allIndices, 3)
	assert.ElementsMatch(t, []int{0, 1, 2}, allIndices)

	// Step 3: SyncExpectations sees pod-uid-a in cache
	expStore.SyncExpectations(controlleeKey, []types.UID{"pod-uid-a"}, nil)

	// Step 4: Now only indices 1, 2 should be reserved
	reservedIndices := expStore.GetCreateExpectationIndices(controlleeKey)
	assert.Len(t, reservedIndices, 2)
	assert.ElementsMatch(t, []int{1, 2}, reservedIndices)

	// Step 5: pod-uid-b and pod-uid-c appear in cache
	expStore.SyncExpectations(controlleeKey, []types.UID{"pod-uid-b", "pod-uid-c"}, nil)

	// Step 6: No more reserved indices
	finalIndices := expStore.GetCreateExpectationIndices(controlleeKey)
	assert.Nil(t, finalIndices)
	assert.Empty(t, expStore.GetCreateExpectations(controlleeKey))
}

func TestConcurrentExpectCreations(t *testing.T) {
	// Verify that concurrent ExpectCreations calls are safe (race detector will catch issues)
	expStore := NewExpectationsStore()
	const numGoroutines = 50

	wg := sync.WaitGroup{}
	wg.Add(numGoroutines)
	for i := range numGoroutines {
		go func(idx int) {
			defer wg.Done()
			uid := types.UID(fmt.Sprintf("uid-%d", idx))
			_ = expStore.ExpectCreations(logr.Discard(), controlleeKey, uid, idx)
		}(i)
	}
	wg.Wait()

	// All 50 UIDs should be present
	createExps := expStore.GetCreateExpectations(controlleeKey)
	assert.Len(t, createExps, numGoroutines)

	// All 50 indices should be present
	indices := expStore.GetCreateExpectationIndices(controlleeKey)
	assert.Len(t, indices, numGoroutines)
}

func TestConcurrentWriteOperations(t *testing.T) {
	// Verify concurrent write operations (ExpectCreations, SyncExpectations, ExpectDeletions) are safe.
	// Note: read methods are intentionally lock-free by design — controller-runtime work queue serializes
	// reconciles for the same key, so reads and writes for the same controlleeKey never overlap.
	// Write methods DO need locking because RunConcurrentlyWithSlowStart spawns goroutines that
	// concurrently call ExpectCreations for the same controlleeKey.
	expStore := NewExpectationsStore()
	const numCreates = 30

	// Pre-create some expectations
	for i := range numCreates {
		uid := types.UID(fmt.Sprintf("uid-%d", i))
		assert.NoError(t, expStore.ExpectCreations(logr.Discard(), controlleeKey, uid, i))
	}

	wg := sync.WaitGroup{}

	// Concurrently sync with some UIDs observed (write)
	wg.Add(1)
	go func() {
		defer wg.Done()
		observedUIDs := make([]types.UID, 0, 10)
		for i := range 10 {
			observedUIDs = append(observedUIDs, types.UID(fmt.Sprintf("uid-%d", i)))
		}
		expStore.SyncExpectations(controlleeKey, observedUIDs, nil)
	}()

	// Concurrently add more expectations (write)
	wg.Add(10)
	for i := numCreates; i < numCreates+10; i++ {
		go func(idx int) {
			defer wg.Done()
			uid := types.UID(fmt.Sprintf("uid-%d", idx))
			_ = expStore.ExpectCreations(logr.Discard(), controlleeKey, uid, idx)
		}(i)
	}

	wg.Wait()
	// No panic or race = success
}

func TestSyncExpectations_WithCreationIndices(t *testing.T) {
	// Verify that SyncExpectations removes creationIndices entries for UIDs that appear as non-terminating.
	expStore := NewExpectationsStore()
	key := "test-ns/indices-sync"
	assert.NoError(t, expStore.ExpectCreations(logr.Discard(), key, types.UID("uid-1"), 10))
	assert.NoError(t, expStore.ExpectCreations(logr.Discard(), key, types.UID("uid-2"), 20))
	assert.NoError(t, expStore.ExpectCreations(logr.Discard(), key, types.UID("uid-3"), 30))

	// Sync: uid-1 is now visible as non-terminating
	expStore.SyncExpectations(key, []types.UID{"uid-1"}, nil)

	// uid-1's index must be gone; uid-2, uid-3 still reserved
	assert.ElementsMatch(t, []int{20, 30}, expStore.GetCreateExpectationIndices(key))
	assert.ElementsMatch(t, []types.UID{"uid-2", "uid-3"}, expStore.GetCreateExpectations(key))
}

func TestSyncExpectations_PodDeletedBeforeAppearingNonTerminating(t *testing.T) {
	// Documents a known limitation: if a pod is created (uid recorded), then goes terminating and is
	// fully deleted before the next SyncExpectations observes it as non-terminating, its index remains
	// reserved until the expectations are deleted or the controller restarts.
	// This is acceptable because such rapid pod churn is rare, and the lease is short-lived (next
	// reconcile will see the pod gone from terminating→deleted and the expectation count mismatch
	// will trigger re-evaluation).
	expStore := NewExpectationsStore()
	key := "test-ns/vanished-pod"
	assert.NoError(t, expStore.ExpectCreations(logr.Discard(), key, types.UID("uid-vanished"), 5))

	// Step 1: pod appears as terminating (we see it going away)
	expStore.SyncExpectations(key, nil, []types.UID{"uid-vanished"})
	// Still reserved — pod hasn't gone fully yet
	assert.ElementsMatch(t, []int{5}, expStore.GetCreateExpectationIndices(key))

	// Step 2: pod is now fully gone from both lists (delete completed)
	expStore.SyncExpectations(key, nil, nil)
	// Known limitation: uid-vanished is still in uidsToAdd because SyncExpectations cannot
	// distinguish "in-flight pod not yet in cache" from "pod was deleted before it was ever
	// observed non-terminating". Index 5 remains reserved until expectations are deleted.
	assert.ElementsMatch(t, []int{5}, expStore.GetCreateExpectationIndices(key),
		"index is still reserved (known limitation: cannot distinguish in-flight from vanished)")
}

func TestDeleteExpectations_CleansIndices(t *testing.T) {
	expStore := NewExpectationsStore()
	assert.NoError(t, expStore.ExpectCreations(logr.Discard(), controlleeKey, types.UID("uid-1"), 5))
	assert.NoError(t, expStore.ExpectCreations(logr.Discard(), controlleeKey, types.UID("uid-2"), 10))

	// Delete all expectations
	assert.NoError(t, expStore.DeleteExpectations(logr.Discard(), controlleeKey))

	// Everything should be gone
	assert.Nil(t, expStore.GetCreateExpectationIndices(controlleeKey))
	assert.Nil(t, expStore.GetCreateExpectations(controlleeKey))
}

func initializeControlleeExpectations(expStore *ExpectationsStore, controlleeKey string, uidsToAdd, uidsToDelete []types.UID) error {
	return expStore.Add(&ControlleeExpectations{
		key:          controlleeKey,
		uidsToAdd:    sets.New(uidsToAdd...),
		uidsToDelete: sets.New(uidsToDelete...),
	})
}

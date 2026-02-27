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
			// test the method
			wg := sync.WaitGroup{}
			wg.Add(len(tc.newUIDs))
			for _, uid := range tc.newUIDs {
				go func() {
					defer wg.Done()
					err := expStore.ExpectCreations(logr.Discard(), controlleeKey, uid)
					assert.NoError(t, err)
				}()
			}
			wg.Wait()
			// compare the expected with actual
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

func TestObserveCreationCancellation(t *testing.T) {
	expStore := NewExpectationsStore()
	_ = expStore.ExpectCreations(logr.Discard(), controlleeKey, types.UID("uid-1"), types.UID("uid-2"))
	expStore.ObserveCreationCancellation(logr.Discard(), controlleeKey, types.UID("uid-1"))
	assert.ElementsMatch(t, []types.UID{"uid-2"}, expStore.GetCreateExpectations(controlleeKey))
}

func initializeControlleeExpectations(expStore *ExpectationsStore, controlleeKey string, uidsToAdd, uidsToDelete []types.UID) error {
	return expStore.Add(&ControlleeExpectations{
		key:          controlleeKey,
		uidsToAdd:    sets.New(uidsToAdd...),
		uidsToDelete: sets.New(uidsToDelete...),
	})
}

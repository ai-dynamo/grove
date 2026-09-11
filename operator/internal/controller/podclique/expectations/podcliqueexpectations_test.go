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

package expectations

import (
	"testing"

	"github.com/ai-dynamo/grove/operator/internal/expect"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// TestPodGangScopedExpectationsStoreKey verifies the storage key is scoped to the PodClique namespace
// and name and the PodGang name.
func TestPodGangScopedExpectationsStoreKey(t *testing.T) {
	key, err := PodGangScopedExpectationsStoreKey(metav1.ObjectMeta{Namespace: "ns", Name: "pclq"}, "pg-0")
	require.NoError(t, err)
	assert.Equal(t, "ns/pclq/pg-0", key)
}

// TestIndexExpectationByPodClique verifies an expectation is grouped under the PodClique portion of
// its key, a key without a PodGang segment is returned unchanged, and a wrong object type errors.
func TestIndexExpectationByPodClique(t *testing.T) {
	tests := []struct {
		name      string
		obj       any
		expected  []string
		expectErr bool
	}{
		{
			name:     "composite key groups under the PodClique portion",
			obj:      controlleeExpectationWithKey(t, "ns/pclq/pg-0"),
			expected: []string{"ns/pclq"},
		},
		{
			name:     "key without a PodGang segment is returned unchanged",
			obj:      controlleeExpectationWithKey(t, "ns"),
			expected: []string{"ns"},
		},
		{
			name:      "a non-expectation object errors",
			obj:       "not-an-expectation",
			expectErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			groups, err := indexExpectationByPodClique(tc.obj)
			if tc.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.expected, groups)
		})
	}
}

// TestClearPodCliqueExpectations verifies every PodGang-scoped expectation of the target PodClique is
// removed while another PodClique's expectations are left intact.
func TestClearPodCliqueExpectations(t *testing.T) {
	store := expect.NewExpectationsStore()
	require.NoError(t, store.AddIndexers(PodCliqueExpectationsIndexers()))

	targetPCLQ := metav1.ObjectMeta{Namespace: "ns", Name: "pclq-a"}
	otherPCLQ := metav1.ObjectMeta{Namespace: "ns", Name: "pclq-b"}
	// pclq-a spans two PodGangs, pclq-b has one.
	expectCreationUnder(t, store, targetPCLQ, "pg-0", "uid-a0")
	expectCreationUnder(t, store, targetPCLQ, "pg-1", "uid-a1")
	expectCreationUnder(t, store, otherPCLQ, "pg-0", "uid-b0")

	require.NoError(t, ClearPodCliqueExpectations(logr.Discard(), store, targetPCLQ))

	assertExpectationAbsent(t, store, targetPCLQ, "pg-0")
	assertExpectationAbsent(t, store, targetPCLQ, "pg-1")
	assertExpectationPresent(t, store, otherPCLQ, "pg-0")
}

// TestObservePodDeletion verifies the delete expectation for a pod is lowered under its PodGang-scoped
// key.
func TestObservePodDeletion(t *testing.T) {
	store := expect.NewExpectationsStore()
	pclq := metav1.ObjectMeta{Namespace: "ns", Name: "pclq-a"}
	key, err := PodGangScopedExpectationsStoreKey(pclq, "pg-0")
	require.NoError(t, err)
	require.NoError(t, store.ExpectDeletions(logr.Discard(), key, "uid-0"))

	require.NoError(t, ObservePodDeletion(logr.Discard(), store, pclq, "pg-0", "uid-0"))

	assert.NotContains(t, store.GetDeleteExpectations(key), types.UID("uid-0"))
}

func controlleeExpectationWithKey(t *testing.T, key string) *expect.ControlleeExpectations {
	t.Helper()
	store := expect.NewExpectationsStore()
	require.NoError(t, store.ExpectCreations(logr.Discard(), key, "uid-0"))
	exp, exists, err := store.GetExpectations(key)
	require.NoError(t, err)
	require.True(t, exists)
	return exp
}

func expectCreationUnder(t *testing.T, store *expect.ExpectationsStore, pclqObjMeta metav1.ObjectMeta, podGangName string, uid types.UID) {
	t.Helper()
	key, err := PodGangScopedExpectationsStoreKey(pclqObjMeta, podGangName)
	require.NoError(t, err)
	require.NoError(t, store.ExpectCreations(logr.Discard(), key, uid))
}

func assertExpectationAbsent(t *testing.T, store *expect.ExpectationsStore, pclqObjMeta metav1.ObjectMeta, podGangName string) {
	t.Helper()
	key, err := PodGangScopedExpectationsStoreKey(pclqObjMeta, podGangName)
	require.NoError(t, err)
	_, exists, err := store.GetExpectations(key)
	require.NoError(t, err)
	assert.False(t, exists)
}

func assertExpectationPresent(t *testing.T, store *expect.ExpectationsStore, pclqObjMeta metav1.ObjectMeta, podGangName string) {
	t.Helper()
	key, err := PodGangScopedExpectationsStoreKey(pclqObjMeta, podGangName)
	require.NoError(t, err)
	_, exists, err := store.GetExpectations(key)
	require.NoError(t, err)
	assert.True(t, exists)
}

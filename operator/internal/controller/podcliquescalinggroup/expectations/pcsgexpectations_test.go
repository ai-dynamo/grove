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

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/expect"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// TestPCSGScopedExpectationsStoreKey verifies the key is the PodCliqueScalingGroup namespace and name.
func TestPCSGScopedExpectationsStoreKey(t *testing.T) {
	key, err := PCSGScopedExpectationsStoreKey(metav1.ObjectMeta{Namespace: "ns", Name: "pcsg-0"})
	require.NoError(t, err)
	assert.Equal(t, "ns/pcsg-0", key)
}

// TestPCSGScopedExpectationsStoreKeyForMemberPodClique verifies the key is derived from the member's
// scaling group label, and that a member without the label is reported as not owned by a PCSG.
func TestPCSGScopedExpectationsStoreKeyForMemberPodClique(t *testing.T) {
	t.Run("derives the key from the scaling group label", func(t *testing.T) {
		pclq := &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns",
			Name:      "pcsg-0-0-worker",
			Labels:    map[string]string{apicommon.LabelPodCliqueScalingGroup: "pcsg-0"},
		}}
		key, ok := PCSGScopedExpectationsStoreKeyForMemberPodClique(pclq)
		require.True(t, ok)
		assert.Equal(t, "ns/pcsg-0", key)
	})
	t.Run("returns false when the scaling group label is absent", func(t *testing.T) {
		pclq := &grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "standalone"}}
		_, ok := PCSGScopedExpectationsStoreKeyForMemberPodClique(pclq)
		assert.False(t, ok)
	})
}

// TestRecordPCSGReplicaDeleteExpectations verifies a delete expectation is recorded for every member
// and that a replica with no members records nothing.
func TestRecordPCSGReplicaDeleteExpectations(t *testing.T) {
	t.Run("records a delete expectation for each member", func(t *testing.T) {
		store := expect.NewExpectationsStore()
		members := []grovecorev1alpha1.PodClique{memberWithUID("a"), memberWithUID("b")}
		require.NoError(t, RecordPCSGReplicaDeleteExpectations(logr.Discard(), store, "ns/pcsg-0", members))
		assert.True(t, store.HasDeleteExpectation("ns/pcsg-0", types.UID("a")))
		assert.True(t, store.HasDeleteExpectation("ns/pcsg-0", types.UID("b")))
	})
	t.Run("is a no-op when there are no members", func(t *testing.T) {
		store := expect.NewExpectationsStore()
		require.NoError(t, RecordPCSGReplicaDeleteExpectations(logr.Discard(), store, "ns/pcsg-0", nil))
		_, exists, err := store.GetExpectations("ns/pcsg-0")
		require.NoError(t, err)
		assert.False(t, exists)
	})
}

// TestHasPCSGReplicaDisruptionBeenTriggered verifies a replica is reported disrupted when any member
// carries a pending delete expectation, and not otherwise.
func TestHasPCSGReplicaDisruptionBeenTriggered(t *testing.T) {
	store := expect.NewExpectationsStore()
	require.NoError(t, store.ExpectDeletions(logr.Discard(), "ns/pcsg-0", types.UID("a")))
	t.Run("true when a member has a pending delete expectation", func(t *testing.T) {
		members := []grovecorev1alpha1.PodClique{memberWithUID("a"), memberWithUID("b")}
		assert.True(t, HasPCSGReplicaDisruptionBeenTriggered(store, "ns/pcsg-0", members))
	})
	t.Run("false when no member has a delete expectation", func(t *testing.T) {
		members := []grovecorev1alpha1.PodClique{memberWithUID("c"), memberWithUID("d")}
		assert.False(t, HasPCSGReplicaDisruptionBeenTriggered(store, "ns/pcsg-0", members))
	})
}

// TestSyncPCSGReplicaDeleteExpectations verifies a delete expectation is retained while its member is
// still observed in the cache, whether running or terminating, and cleared once the member is gone.
func TestSyncPCSGReplicaDeleteExpectations(t *testing.T) {
	t.Run("keeps present members and clears members no longer in the cache", func(t *testing.T) {
		store := expect.NewExpectationsStore()
		require.NoError(t, store.ExpectDeletions(logr.Discard(), "ns/pcsg-0", types.UID("present"), types.UID("gone")))
		SyncPCSGReplicaDeleteExpectations(store, "ns/pcsg-0", []grovecorev1alpha1.PodClique{memberWithUID("present")})
		assert.True(t, store.HasDeleteExpectation("ns/pcsg-0", types.UID("present")), "expectation for a still-present member is retained")
		assert.False(t, store.HasDeleteExpectation("ns/pcsg-0", types.UID("gone")), "expectation for a member no longer in the cache is cleared")
	})
	t.Run("retains the expectation for a terminating member", func(t *testing.T) {
		store := expect.NewExpectationsStore()
		require.NoError(t, store.ExpectDeletions(logr.Discard(), "ns/pcsg-0", types.UID("terminating")))
		SyncPCSGReplicaDeleteExpectations(store, "ns/pcsg-0", []grovecorev1alpha1.PodClique{terminatingMemberWithUID("terminating")})
		assert.True(t, store.HasDeleteExpectation("ns/pcsg-0", types.UID("terminating")))
	})
}

// TestClearPCSGExpectations verifies the PodCliqueScalingGroup's expectations entry is removed, so the
// store does not leak an entry per PodCliqueScalingGroup lifecycle, and that clearing a key with no
// entry is a no-op.
func TestClearPCSGExpectations(t *testing.T) {
	store := expect.NewExpectationsStore()
	pcsgObjMeta := metav1.ObjectMeta{Namespace: "ns", Name: "pcsg-0"}
	key, err := PCSGScopedExpectationsStoreKey(pcsgObjMeta)
	require.NoError(t, err)
	require.NoError(t, store.ExpectDeletions(logr.Discard(), key, types.UID("a")))

	_, exists, err := store.GetExpectations(key)
	require.NoError(t, err)
	require.True(t, exists, "precondition: expectation should exist before clearing")

	require.NoError(t, ClearPCSGExpectations(logr.Discard(), store, pcsgObjMeta))

	_, exists, err = store.GetExpectations(key)
	require.NoError(t, err)
	assert.False(t, exists, "expectations entry should have been cleared, no leak")

	// Clearing again, with no entry present, must be a no-op and must not error.
	assert.NoError(t, ClearPCSGExpectations(logr.Discard(), store, pcsgObjMeta))
}

func memberWithUID(uid string) grovecorev1alpha1.PodClique {
	return grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: uid, UID: types.UID(uid)}}
}

func terminatingMemberWithUID(uid string) grovecorev1alpha1.PodClique {
	now := metav1.Now()
	return grovecorev1alpha1.PodClique{ObjectMeta: metav1.ObjectMeta{
		Namespace:         "ns",
		Name:              uid,
		UID:               types.UID(uid),
		DeletionTimestamp: &now,
		Finalizers:        []string{"test.grove.io/finalizer"},
	}}
}

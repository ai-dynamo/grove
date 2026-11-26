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
	"slices"
	"testing"
	"time"

	grovev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestCurrentlyUpdatingClearIfSatisfiedWithoutExpectation(t *testing.T) {
	e := &CurrentlyUpdating{}

	assert.True(t, e.ClearIfSatisfied(currentlyUpdatingTestObjectMeta(), nil))
}

func TestCurrentlyUpdatingRecordAndClearIfSatisfied(t *testing.T) {
	objectMeta := currentlyUpdatingTestObjectMeta()
	recorded := []grovev1beta1.PodCliqueSetReplicaUpdateProgress{
		{ReplicaIndex: 0},
		{
			ReplicaIndex: 2,
			Replacement:  &grovev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
		},
	}

	t.Run("matching selection clears the expectation", func(t *testing.T) {
		e := &CurrentlyUpdating{}
		e.Record(objectMeta, recorded)

		assert.True(t, e.ClearIfSatisfied(objectMeta, recorded))
		// The expectation is removed, so a subsequent check with any value is satisfied.
		assert.True(t, e.ClearIfSatisfied(objectMeta, nil))
	})

	t.Run("selection match ignores entry order", func(t *testing.T) {
		e := &CurrentlyUpdating{}
		e.Record(objectMeta, recorded)

		reordered := []grovev1beta1.PodCliqueSetReplicaUpdateProgress{recorded[1], recorded[0]}
		assert.True(t, e.ClearIfSatisfied(objectMeta, reordered))
	})

	t.Run("mismatched selection does not clear", func(t *testing.T) {
		e := &CurrentlyUpdating{}
		e.Record(objectMeta, recorded)

		assert.False(t, e.ClearIfSatisfied(objectMeta, []grovev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}}))
		// The expectation remains, so a later matching check still clears it.
		assert.True(t, e.ClearIfSatisfied(objectMeta, recorded))
	})
}

func TestCurrentlyUpdatingComparesOnlyLifecycleFacts(t *testing.T) {
	objectMeta := currentlyUpdatingTestObjectMeta()
	startedAt := metav1.NewTime(time.Now())
	recorded := []grovev1beta1.PodCliqueSetReplicaUpdateProgress{{
		ReplicaIndex:    2,
		UpdateStartedAt: startedAt,
		Replacement:     &grovev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
	}}

	t.Run("ignores UpdateStartedAt, Message, and InFlightEpochs", func(t *testing.T) {
		e := &CurrentlyUpdating{}
		e.Record(objectMeta, recorded)

		observed := []grovev1beta1.PodCliqueSetReplicaUpdateProgress{{
			ReplicaIndex:    2,
			UpdateStartedAt: metav1.NewTime(startedAt.Add(time.Hour)),
			Message:         new("in progress"),
			InFlightEpochs:  []string{"100"},
			Replacement:     &grovev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
		}}
		assert.True(t, e.ClearIfSatisfied(objectMeta, observed))
	})

	t.Run("distinguishes targetReplicaIndex", func(t *testing.T) {
		e := &CurrentlyUpdating{}
		e.Record(objectMeta, recorded)

		observed := []grovev1beta1.PodCliqueSetReplicaUpdateProgress{{
			ReplicaIndex: 2,
			Replacement:  &grovev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 3},
		}}
		assert.False(t, e.ClearIfSatisfied(objectMeta, observed))
	})

	t.Run("distinguishes canceled", func(t *testing.T) {
		e := &CurrentlyUpdating{}
		e.Record(objectMeta, recorded)

		canceledAt := metav1.Now()
		observed := []grovev1beta1.PodCliqueSetReplicaUpdateProgress{{
			ReplicaIndex: 2,
			Replacement: &grovev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex: 1,
				CanceledAt:         &canceledAt,
			},
		}}
		assert.False(t, e.ClearIfSatisfied(objectMeta, observed))
	})

	t.Run("distinguishes sourceCleanupCompleted", func(t *testing.T) {
		e := &CurrentlyUpdating{}
		e.Record(objectMeta, recorded)

		cleanupAt := metav1.Now()
		observed := []grovev1beta1.PodCliqueSetReplicaUpdateProgress{{
			ReplicaIndex: 2,
			Replacement: &grovev1beta1.PodCliqueSetReplicaReplacementProgress{
				TargetReplicaIndex:       1,
				SourceCleanupCompletedAt: &cleanupAt,
			},
		}}
		assert.False(t, e.ClearIfSatisfied(objectMeta, observed))
	})

	t.Run("distinguishes ended", func(t *testing.T) {
		e := &CurrentlyUpdating{}
		e.Record(objectMeta, recorded)

		endedAt := metav1.Now()
		observed := []grovev1beta1.PodCliqueSetReplicaUpdateProgress{{
			ReplicaIndex:  2,
			UpdateEndedAt: &endedAt,
			Replacement:   &grovev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
		}}
		assert.False(t, e.ClearIfSatisfied(objectMeta, observed))
	})

	t.Run("distinguishes hasReplacement", func(t *testing.T) {
		e := &CurrentlyUpdating{}
		e.Record(objectMeta, []grovev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 2}})

		assert.False(t, e.ClearIfSatisfied(objectMeta, recorded))
	})
}

func TestCurrentlyUpdatingForget(t *testing.T) {
	objectMeta := currentlyUpdatingTestObjectMeta()
	e := &CurrentlyUpdating{}
	e.Record(objectMeta, []grovev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}})

	e.Forget(objectMeta)

	// After Forget, any selection is satisfied because no expectation remains.
	assert.True(t, e.ClearIfSatisfied(objectMeta, nil))
}

// TestCurrentlyUpdatingRetiredSourceComparison covers canonical retired-source comparison for active and
// canceled replacements. Stale cleanup obligations must not satisfy or remove a recorded expectation.
func TestCurrentlyUpdatingRetiredSourceComparison(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                   string
		recordedRetiredSourcePCSReplicaIndices []int32
		observedRetiredSourcePCSReplicaIndices []int32
		canceled                               bool
		wantSatisfied                          bool
	}{
		{name: "nil retired lists match", wantSatisfied: true},
		{name: "nil and empty retired lists match", observedRetiredSourcePCSReplicaIndices: []int32{}, wantSatisfied: true},
		{name: "empty and nil retired lists match", recordedRetiredSourcePCSReplicaIndices: []int32{}, wantSatisfied: true},
		{name: "identical retired sets match", recordedRetiredSourcePCSReplicaIndices: []int32{3}, observedRetiredSourcePCSReplicaIndices: []int32{3}, wantSatisfied: true},
		{name: "retired source ordering is ignored", recordedRetiredSourcePCSReplicaIndices: []int32{3, 0, 2}, observedRetiredSourcePCSReplicaIndices: []int32{2, 3, 0}, wantSatisfied: true},
		{name: "duplicates on either side are ignored", recordedRetiredSourcePCSReplicaIndices: []int32{3, 0, 3}, observedRetiredSourcePCSReplicaIndices: []int32{0, 3, 0}, wantSatisfied: true},
		{name: "stale cache missing newly retired sources does not match", recordedRetiredSourcePCSReplicaIndices: []int32{2, 3}, observedRetiredSourcePCSReplicaIndices: []int32{3}},
		{name: "stale cache retaining a cleaned retired source does not match", recordedRetiredSourcePCSReplicaIndices: []int32{3}, observedRetiredSourcePCSReplicaIndices: []int32{2, 3}},
		{name: "complete retirement does not match stale pending cleanup", observedRetiredSourcePCSReplicaIndices: []int32{2, 3}},
		{name: "different retired sets of equal length do not match", recordedRetiredSourcePCSReplicaIndices: []int32{3, 4}, observedRetiredSourcePCSReplicaIndices: []int32{3, 5}},
		{name: "retired index zero is not equivalent to an empty set", recordedRetiredSourcePCSReplicaIndices: []int32{0}},
		{name: "numeric boundaries keep different sets distinct", recordedRetiredSourcePCSReplicaIndices: []int32{1, 23}, observedRetiredSourcePCSReplicaIndices: []int32{3, 12}},
		{name: "large replica indices compare without losing precision", recordedRetiredSourcePCSReplicaIndices: []int32{0, 1<<31 - 1}, observedRetiredSourcePCSReplicaIndices: []int32{1<<31 - 1, 0}, wantSatisfied: true},
		{name: "canceled partial cleanup rejects a stale retired set", recordedRetiredSourcePCSReplicaIndices: []int32{3}, observedRetiredSourcePCSReplicaIndices: []int32{2, 3}, canceled: true},
		{name: "canceled complete cleanup rejects stale pending retirement", observedRetiredSourcePCSReplicaIndices: []int32{3}, canceled: true},
		{name: "canceled newly retired source must become visible", recordedRetiredSourcePCSReplicaIndices: []int32{3}, canceled: true},
		{name: "canceled retirement comparison also ignores ordering and duplicates", recordedRetiredSourcePCSReplicaIndices: []int32{0, 3}, observedRetiredSourcePCSReplicaIndices: []int32{3, 0, 3}, canceled: true, wantSatisfied: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var canceledAt *metav1.Time
			if tc.canceled {
				canceledAt = new(metav1.NewTime(time.Unix(150, 0).UTC()))
			}
			recordedPCS := pcsWithRetiredSourceCleanup(tc.recordedRetiredSourcePCSReplicaIndices, canceledAt)
			observedPCS := pcsWithRetiredSourceCleanup(tc.observedRetiredSourcePCSReplicaIndices, canceledAt)
			recordedBefore := recordedPCS.DeepCopy()
			observedBefore := observedPCS.DeepCopy()
			expectations := &CurrentlyUpdating{}

			expectations.Record(recordedPCS.ObjectMeta, recordedPCS.Status.UpdateProgress.CurrentlyUpdating)
			satisfied := expectations.ClearIfSatisfied(observedPCS.ObjectMeta, observedPCS.Status.UpdateProgress.CurrentlyUpdating)

			assert.Equal(t, tc.wantSatisfied, satisfied)
			assert.Equal(t, recordedBefore, recordedPCS, "recording must not sort or mutate status")
			assert.Equal(t, observedBefore, observedPCS, "comparison must not sort or mutate cached status")
			if !tc.wantSatisfied {
				assert.False(t, expectations.ClearIfSatisfied(recordedPCS.ObjectMeta, nil), "a mismatch must retain the expectation")
				assert.True(t, expectations.ClearIfSatisfied(recordedPCS.ObjectMeta, recordedBefore.Status.UpdateProgress.CurrentlyUpdating), "a later matching observation must clear the expectation")
			}
			assert.True(t, expectations.ClearIfSatisfied(recordedPCS.ObjectMeta, nil), "the matching observation must remove the expectation")
		})
	}
}

// TestCurrentlyUpdatingRetiredSourcesStayWithReplacement verifies that each retired set belongs to its own
// replacement. Matching the aggregate retired indices is insufficient when their ownership differs.
func TestCurrentlyUpdatingRetiredSourcesStayWithReplacement(t *testing.T) {
	t.Parallel()
	recordedPCS := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
		WithUpdateProgress(&grovev1beta1.PodCliqueSetUpdateProgress{
			CurrentlyUpdating: []grovev1beta1.PodCliqueSetReplicaUpdateProgress{
				{ReplicaIndex: 2, Replacement: &grovev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1, RetiredSourceReplicaIndices: []int32{3, 0}}},
				{ReplicaIndex: 5, Replacement: &grovev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 4, RetiredSourceReplicaIndices: []int32{6}}},
			},
		}).Build()
	observedPCS := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
		WithUpdateProgress(&grovev1beta1.PodCliqueSetUpdateProgress{
			CurrentlyUpdating: []grovev1beta1.PodCliqueSetReplicaUpdateProgress{
				{ReplicaIndex: 2, Replacement: &grovev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1, RetiredSourceReplicaIndices: []int32{6}}},
				{ReplicaIndex: 5, Replacement: &grovev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 4, RetiredSourceReplicaIndices: []int32{3, 0}}},
			},
		}).Build()
	expectations := &CurrentlyUpdating{}
	expectations.Record(recordedPCS.ObjectMeta, recordedPCS.Status.UpdateProgress.CurrentlyUpdating)

	assert.False(t, expectations.ClearIfSatisfied(observedPCS.ObjectMeta, observedPCS.Status.UpdateProgress.CurrentlyUpdating))
	matchingPCS := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
		WithUpdateProgress(&grovev1beta1.PodCliqueSetUpdateProgress{
			CurrentlyUpdating: []grovev1beta1.PodCliqueSetReplicaUpdateProgress{
				{ReplicaIndex: 5, Replacement: &grovev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 4, RetiredSourceReplicaIndices: []int32{6, 6}}},
				{ReplicaIndex: 2, Replacement: &grovev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1, RetiredSourceReplicaIndices: []int32{0, 3}}},
			},
		}).Build()
	assert.True(t, expectations.ClearIfSatisfied(matchingPCS.ObjectMeta, matchingPCS.Status.UpdateProgress.CurrentlyUpdating))
	assert.True(t, expectations.ClearIfSatisfied(recordedPCS.ObjectMeta, nil))
}

// TestCurrentlyUpdatingRetiredSourceDigestCopyIsolation verifies that recording snapshots the comparison
// value. Mutating the caller's retired-source slice must not alter the stored expectation.
func TestCurrentlyUpdatingRetiredSourceDigestCopyIsolation(t *testing.T) {
	t.Parallel()
	pcs := pcsWithRetiredSourceCleanup([]int32{3, 0, 3}, nil)
	before := pcs.DeepCopy()
	expectations := &CurrentlyUpdating{}
	expectations.Record(pcs.ObjectMeta, pcs.Status.UpdateProgress.CurrentlyUpdating)
	require.Equal(t, before, pcs, "recording must leave the retired-source slice unchanged")

	pcs.Status.UpdateProgress.CurrentlyUpdating[0].Replacement.RetiredSourceReplicaIndices[0] = 99

	assert.False(t, expectations.ClearIfSatisfied(pcs.ObjectMeta, pcs.Status.UpdateProgress.CurrentlyUpdating), "the stored digest must not alias the mutated input")
	assert.True(t, expectations.ClearIfSatisfied(before.ObjectMeta, before.Status.UpdateProgress.CurrentlyUpdating), "the stored digest must still match the original retired set")
	assert.True(t, expectations.ClearIfSatisfied(before.ObjectMeta, nil))
}

// pcsWithRetiredSourceCleanup builds a different-index replacement with deterministic lifecycle timestamps.
func pcsWithRetiredSourceCleanup(retiredSourcePCSReplicaIndices []int32, canceledAt *metav1.Time) *grovev1beta1.PodCliqueSet {
	return testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
		WithUpdateProgress(&grovev1beta1.PodCliqueSetUpdateProgress{
			UpdateStartedAt: metav1.NewTime(time.Unix(100, 0).UTC()),
			CurrentlyUpdating: []grovev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex: 2,
				Replacement: &grovev1beta1.PodCliqueSetReplicaReplacementProgress{
					TargetReplicaIndex:          1,
					RetiredSourceReplicaIndices: slices.Clone(retiredSourcePCSReplicaIndices),
					CanceledAt:                  canceledAt.DeepCopy(),
				},
			}},
		}).Build()
}

func currentlyUpdatingTestObjectMeta() metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: "default", Name: "pcs", UID: types.UID("uid")}
}

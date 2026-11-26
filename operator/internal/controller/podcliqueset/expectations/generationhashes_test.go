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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGenerationHashesClearIfSatisfied(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                   string
		record                 bool
		generationHash         string
		schedulingHash         *string
		observedGenerationHash *string
		observedSchedulingHash *string
		wantSatisfied          bool
	}{
		{name: "no expectation", wantSatisfied: true},
		{
			name: "both hashes match", record: true,
			generationHash: "generation", schedulingHash: new("scheduling"),
			observedGenerationHash: new("generation"), observedSchedulingHash: new("scheduling"),
			wantSatisfied: true,
		},
		{
			name: "generation hash is stale", record: true,
			generationHash: "generation", schedulingHash: new("scheduling"),
			observedGenerationHash: new("stale"), observedSchedulingHash: new("scheduling"),
		},
		{
			name: "generation hash is missing", record: true,
			generationHash: "generation", schedulingHash: new("scheduling"),
			observedSchedulingHash: new("scheduling"),
		},
		{
			name: "scheduling hash is stale", record: true,
			generationHash: "generation", schedulingHash: new("scheduling"),
			observedGenerationHash: new("generation"), observedSchedulingHash: new("stale"),
		},
		{
			name: "scheduling hash is missing", record: true,
			generationHash: "generation", schedulingHash: new("scheduling"),
			observedGenerationHash: new("generation"),
		},
		{
			name: "both scheduling hashes are absent", record: true,
			generationHash: "generation", observedGenerationHash: new("generation"),
			wantSatisfied: true,
		},
		{
			name: "unexpected scheduling hash", record: true,
			generationHash:         "generation",
			observedGenerationHash: new("generation"), observedSchedulingHash: new("scheduling"),
		},
		{
			name: "nil scheduling hash differs from empty", record: true,
			generationHash: "generation", schedulingHash: new(""),
			observedGenerationHash: new("generation"),
		},
		{
			name: "empty scheduling hash differs from nil", record: true,
			generationHash:         "generation",
			observedGenerationHash: new("generation"), observedSchedulingHash: new(""),
		},
		{
			name: "empty generation hash matches an explicitly present empty hash", record: true,
			observedGenerationHash: new(""), wantSatisfied: true,
		},
		{name: "missing generation hash does not match empty", record: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var expectations GenerationHashes
			objectMeta := metav1.ObjectMeta{Namespace: "default", Name: "pcs", UID: "uid"}
			if tc.record {
				expectations.Record(objectMeta, tc.generationHash, tc.schedulingHash)
			}

			assert.Equal(t, tc.wantSatisfied, expectations.ClearIfSatisfied(objectMeta, tc.observedGenerationHash, tc.observedSchedulingHash))
			assert.Equal(t, tc.wantSatisfied, expectations.ClearIfSatisfied(objectMeta, nil, nil), "only a satisfied expectation should be removed")
		})
	}
}

func TestGenerationHashesRecordCopiesSchedulingHash(t *testing.T) {
	t.Parallel()
	var expectations GenerationHashes
	objectMeta := metav1.ObjectMeta{Namespace: "default", Name: "pcs", UID: "uid"}
	schedulingHash := "scheduling"
	expectations.Record(objectMeta, "generation", &schedulingHash)

	schedulingHash = "changed"

	assert.False(t, expectations.ClearIfSatisfied(objectMeta, new("generation"), &schedulingHash))
	assert.True(t, expectations.ClearIfSatisfied(objectMeta, new("generation"), new("scheduling")))
}

func TestGenerationHashesRecordReplacesSnapshot(t *testing.T) {
	t.Parallel()
	for _, generationHash := range []string{"generation", "new-generation"} {
		t.Run(generationHash, func(t *testing.T) {
			t.Parallel()
			var expectations GenerationHashes
			objectMeta := metav1.ObjectMeta{Namespace: "default", Name: "pcs", UID: "uid"}
			key := generationHashesExpectationKey(objectMeta)
			expectations.Record(objectMeta, "generation", nil)
			previousSnapshot, found := expectations.expectations.Load(key)
			require.True(t, found)

			expectations.Record(objectMeta, generationHash, nil)

			currentSnapshot, found := expectations.expectations.Load(key)
			require.True(t, found)
			assert.NotSame(t, previousSnapshot, currentSnapshot, "even equal hashes must produce distinct snapshots")
			assert.False(t, expectations.expectations.CompareAndDelete(key, previousSnapshot), "an acknowledgment holding an older snapshot must retain the newer record")
			assert.True(t, expectations.ClearIfSatisfied(objectMeta, &generationHash, nil))
		})
	}
}

func TestGenerationHashesRecordSupersedesBothHashes(t *testing.T) {
	t.Parallel()
	var expectations GenerationHashes
	objectMeta := metav1.ObjectMeta{Namespace: "default", Name: "pcs", UID: "uid"}
	expectations.Record(objectMeta, "old-generation", new("old-scheduling"))
	expectations.Record(objectMeta, "new-generation", new("new-scheduling"))

	assert.False(t, expectations.ClearIfSatisfied(objectMeta, new("old-generation"), new("old-scheduling")))
	assert.False(t, expectations.ClearIfSatisfied(objectMeta, new("new-generation"), new("old-scheduling")))
	assert.True(t, expectations.ClearIfSatisfied(objectMeta, new("new-generation"), new("new-scheduling")))
}

func TestGenerationHashesIsolatesPodCliqueSets(t *testing.T) {
	t.Parallel()
	objectMeta := metav1.ObjectMeta{Namespace: "default", Name: "pcs", UID: "uid"}
	tests := []struct {
		name      string
		otherMeta metav1.ObjectMeta
	}{
		{name: "namespace", otherMeta: metav1.ObjectMeta{Namespace: "other", Name: "pcs", UID: "uid"}},
		{name: "name", otherMeta: metav1.ObjectMeta{Namespace: "default", Name: "other", UID: "uid"}},
		{name: "UID", otherMeta: metav1.ObjectMeta{Namespace: "default", Name: "pcs", UID: "other"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var expectations GenerationHashes
			expectations.Record(objectMeta, "generation", nil)
			assert.True(t, expectations.ClearIfSatisfied(tc.otherMeta, nil, nil), "another PCS must not inherit this expectation")
			expectations.Record(tc.otherMeta, "other-generation", nil)

			assert.True(t, expectations.ClearIfSatisfied(objectMeta, new("generation"), nil))
			assert.False(t, expectations.ClearIfSatisfied(tc.otherMeta, nil, nil), "acknowledging one PCS must retain the other's expectation")

			expectations.Record(objectMeta, "generation", nil)
			expectations.Forget(objectMeta)
			expectations.Forget(objectMeta)
			assert.True(t, expectations.ClearIfSatisfied(objectMeta, nil, nil), "forgetting an expectation must be idempotent")
			assert.False(t, expectations.ClearIfSatisfied(tc.otherMeta, nil, nil), "forgetting one PCS must retain the other's expectation")
			assert.True(t, expectations.ClearIfSatisfied(tc.otherMeta, new("other-generation"), nil))
		})
	}
}

func TestGenerationHashesConcurrentAccess(t *testing.T) {
	t.Parallel()
	var expectations GenerationHashes
	objectMeta := metav1.ObjectMeta{Namespace: "default", Name: "pcs", UID: "uid"}
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			for range 100 {
				expectations.Record(objectMeta, "generation", new("scheduling"))
				expectations.ClearIfSatisfied(objectMeta, new("generation"), new("scheduling"))
				expectations.Forget(objectMeta)
			}
		})
	}
	workers.Wait()

	expectations.Record(objectMeta, "final-generation", new("final-scheduling"))
	assert.False(t, expectations.ClearIfSatisfied(objectMeta, new("generation"), new("scheduling")))
	assert.True(t, expectations.ClearIfSatisfied(objectMeta, new("final-generation"), new("final-scheduling")))
}

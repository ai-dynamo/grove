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

package podgangmap

import (
	"fmt"
	"slices"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestComputeNextPodGangMapState_SingleStandalonePCLQUpdated tests a coherent update where only
// one standalone PCLQ is updated.
//
// Starting state:
//
//	BPG "bpg-0" (old hash): {5 Frontend pods, 1 Prefill replica, 1 Decode replica}
//	SPGs (old hash): 3x {1 Prefill replica}, 2x {1 Decode replica}
//	Only Frontend has been updated. PCSGs are unchanged.
//
// MVU template: {Frontend: 2 pods}
//
// Expected iterations:
//
//	Iteration 1: MVU {F:2}. Old BPG reduced to {F:3, P:1, D:1}.
//	Iteration 2: MVU {F:3}. Absorbs 1F (3-2=1 < minAvail=2). Old BPG reduced to {F:0, P:1, D:1}.
//	             BPG removed (F=0, but P and D still present so NOT removed — only F is in template).
//	Iteration 3: Done. No Tail-PGs (no PCSGs in template).
func TestComputeNextPodGangMapState_SingleStandalonePCLQUpdated(t *testing.T) {
	template := mvuTemplate{
		standalonePCLQs: map[string]int32{"frontend": 2},
		pcsgs:           map[string]int32{},
	}
	oldEntries := []grovecorev1alpha1.PodGangEntry{
		{Name: "bpg-0", PodCliqueSetGenerationHash: "old-hash", PodCliques: map[string]int32{"frontend": 5}, PCSGReplicaIndices: map[string][]int32{"prefill": {0}, "decode": {0}}},
	}
	counter := 0
	builder := makeTestEntryBuilder(&counter)

	// Iteration 1
	state := computeNextPodGangMapState(template, oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 1)
	assert.Equal(t, int32(2), state.newEntries[0].PodCliques["frontend"])
	assert.Empty(t, state.newEntries[0].PCSGReplicaIndices)
	require.Len(t, state.oldEntries, 1)
	assert.Equal(t, int32(3), state.oldEntries[0].PodCliques["frontend"])

	// Iteration 2: absorbs remaining 1F
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 1)
	assert.Equal(t, int32(3), state.newEntries[0].PodCliques["frontend"])
	// BPG still has Prefill:[0], Decode:[0] so it's not removed
	require.Len(t, state.oldEntries, 1)
	assert.Equal(t, int32(0), state.oldEntries[0].PodCliques["frontend"])
	assert.Equal(t, []int32{0}, state.oldEntries[0].PCSGReplicaIndices["prefill"])

	// Iteration 3: done (no PCSGs in template, remaining F=0)
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	assert.True(t, state.done)
	assert.Nil(t, state.newEntries)
}

// TestComputeNextPodGangMapState_MultipleStandalonePCLQsUpdated tests a coherent update where
// multiple standalone PCLQs are updated simultaneously.
//
// Starting state:
//
//	BPG "bpg-0" (old hash): {5 Frontend pods, 3 Backend pods}
//	Only Frontend and Backend have been updated. PCSGs unchanged.
//
// MVU template: {Frontend: 2 pods, Backend: 1 pod}
//
// Expected iterations:
//
//	Iteration 1: MVU {F:2, B:1}. Old BPG: {F:3, B:2}.
//	Iteration 2: MVU {F:3, B:2}. Absorbs F=1, B=1. Old BPG removed (all zero).
//	Iteration 3: Done.
func TestComputeNextPodGangMapState_MultipleStandalonePCLQsUpdated(t *testing.T) {
	template := mvuTemplate{
		standalonePCLQs: map[string]int32{"frontend": 2, "backend": 1},
		pcsgs:           map[string]int32{},
	}
	oldEntries := []grovecorev1alpha1.PodGangEntry{
		{Name: "bpg-0", PodCliqueSetGenerationHash: "old-hash", PodCliques: map[string]int32{"frontend": 5, "backend": 3}},
	}
	counter := 0
	builder := makeTestEntryBuilder(&counter)

	// Iteration 1
	state := computeNextPodGangMapState(template, oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 1)
	assert.Equal(t, int32(2), state.newEntries[0].PodCliques["frontend"])
	assert.Equal(t, int32(1), state.newEntries[0].PodCliques["backend"])
	require.Len(t, state.oldEntries, 1)
	assert.Equal(t, int32(3), state.oldEntries[0].PodCliques["frontend"])
	assert.Equal(t, int32(2), state.oldEntries[0].PodCliques["backend"])

	// Iteration 2: absorbs remaining F=1, B=1
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 1)
	assert.Equal(t, int32(3), state.newEntries[0].PodCliques["frontend"])
	assert.Equal(t, int32(2), state.newEntries[0].PodCliques["backend"])
	assert.Empty(t, state.oldEntries)

	// Iteration 3: done
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	assert.True(t, state.done)
	assert.Nil(t, state.newEntries)
}

// TestComputeNextPodGangMapState_SinglePCSGUpdated tests a coherent update where only one PCSG
// is updated (no standalone PCLQs changed).
//
// Starting state:
//
//	BPG "bpg-0" (old hash): {5 Frontend pods, 1 Prefill replica}
//	SPGs (old hash): "spg-1" {P:1}, "spg-2" {P:1}, "spg-3" {P:1}
//	Only Prefill PCSG updated. Total old Prefill replicas: 4.
//
// MVU template: {Prefill: 1 replica}
//
// Expected: 4 MVU iterations {P:1} each, deducting from lowest-indexed entry first.
func TestComputeNextPodGangMapState_SinglePCSGUpdated(t *testing.T) {
	template := mvuTemplate{
		standalonePCLQs: map[string]int32{},
		pcsgs:           map[string]int32{"prefill": 1},
	}
	oldEntries := []grovecorev1alpha1.PodGangEntry{
		{Name: "bpg-0", PodCliqueSetGenerationHash: "old-hash", PodCliques: map[string]int32{"frontend": 5}, PCSGReplicaIndices: map[string][]int32{"prefill": {0}}},
		{Name: "spg-1", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {1}}},
		{Name: "spg-2", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {2}}},
		{Name: "spg-3", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {3}}},
	}
	counter := 0
	builder := makeTestEntryBuilder(&counter)

	// Iteration 1: takes index 0 from bpg-0 (lowest index in slice order).
	state := computeNextPodGangMapState(template, oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 1)
	assert.Equal(t, []int32{0}, state.newEntries[0].PCSGReplicaIndices["prefill"])
	// bpg-0 has prefill emptied but still has F:5, so it remains. SPGs still have their indices.
	require.Len(t, state.oldEntries, 4)

	// Iterations 2-4: each takes the next index from the next SPG (1, 2, 3).
	expectedIndices := []int32{1, 2, 3}
	for i, expected := range expectedIndices {
		state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
		require.False(t, state.done, "iteration %d should not be done", i+2)
		require.Len(t, state.newEntries, 1)
		assert.Equal(t, []int32{expected}, state.newEntries[0].PCSGReplicaIndices["prefill"])
	}

	// After 4 iterations: only bpg-0 with F:5 remains (no prefill left anywhere).
	require.Len(t, state.oldEntries, 1)
	assert.Equal(t, int32(5), state.oldEntries[0].PodCliques["frontend"])

	// Iteration 5: done.
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	assert.True(t, state.done)
	assert.Nil(t, state.newEntries)
}

// TestComputeNextPodGangMapState_MultiplePCSGsUpdated tests a coherent update where multiple PCSGs
// are updated (no standalone PCLQs changed).
//
// Starting state:
//
//	BPG "bpg-0" (old hash): {5 Frontend pods, 1 Prefill replica, 1 Decode replica}
//	SPGs (old hash): "spg-1" {P:1}, "spg-2" {P:1}, "spg-3" {P:1}, "spg-4" {D:1}, "spg-5" {D:1}
//	Both Prefill and Decode PCSGs updated.
//	Total: Prefill=4, Decode=3
//
// MVU template: {Prefill: 1 replica, Decode: 1 replica}
//
// Expected:
//
//	Iterations 1-3: MVU {P:1, D:1} each.
//	Iteration 4: Tail-PG {P:1}.
//	Iteration 5: Done.
func TestComputeNextPodGangMapState_MultiplePCSGsUpdated(t *testing.T) {
	template := mvuTemplate{
		standalonePCLQs: map[string]int32{},
		pcsgs:           map[string]int32{"prefill": 1, "decode": 1},
	}
	oldEntries := []grovecorev1alpha1.PodGangEntry{
		{Name: "bpg-0", PodCliqueSetGenerationHash: "old-hash", PodCliques: map[string]int32{"frontend": 5}, PCSGReplicaIndices: map[string][]int32{"prefill": {0}, "decode": {0}}},
		{Name: "spg-1", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {1}}},
		{Name: "spg-2", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {2}}},
		{Name: "spg-3", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {3}}},
		{Name: "spg-4", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"decode": {1}}},
		{Name: "spg-5", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"decode": {2}}},
	}
	counter := 0
	builder := makeTestEntryBuilder(&counter)

	// Iterations 1-3: MVU pulls prefill index 0,1,2 (in that order) and decode 0,1,2.
	expectedPrefill := []int32{0, 1, 2}
	expectedDecode := []int32{0, 1, 2}
	state := podGangMapState{oldEntries: oldEntries}
	for i := range 3 {
		state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
		require.False(t, state.done, "iteration %d should not be done", i+1)
		require.Len(t, state.newEntries, 1)
		assert.Equal(t, []int32{expectedPrefill[i]}, state.newEntries[0].PCSGReplicaIndices["prefill"])
		assert.Equal(t, []int32{expectedDecode[i]}, state.newEntries[0].PCSGReplicaIndices["decode"])
	}

	// Iteration 4: cannot form MVU (decode exhausted). Tail-PG with prefill=[3].
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 1)
	assert.Equal(t, []int32{3}, state.newEntries[0].PCSGReplicaIndices["prefill"])

	// Iteration 5: done.
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	assert.True(t, state.done)
	assert.Nil(t, state.newEntries)
}

// TestComputeNextPodGangMapState_SingleStandalonePCLQAndSinglePCSGUpdated tests a coherent update
// where one standalone PCLQ and one PCSG are both updated.
//
// Starting state:
//
//	BPG "bpg-0" (old hash): {5 Frontend pods, 1 Prefill replica}
//	SPGs (old hash): "spg-1" {P:1}, "spg-2" {P:1}, "spg-3" {P:1}
//	Frontend (standalone) and Prefill (PCSG) updated.
//	Total: Frontend=5 pods, Prefill=4 replicas.
//
// MVU template: {Frontend: 2 pods, Prefill: 1 replica}
//
// Expected:
//
//	Iteration 1: MVU {F:2, P:1}. BPG: {F:3, P:0}.
//	Iteration 2: MVU {F:3, P:1}. Absorbs F=1. SPG-1 consumed.
//	Iteration 3: Tail-PGs: {P:1}, {P:1}.
//	Iteration 4: Done.
func TestComputeNextPodGangMapState_SingleStandalonePCLQAndSinglePCSGUpdated(t *testing.T) {
	template := mvuTemplate{
		standalonePCLQs: map[string]int32{"frontend": 2},
		pcsgs:           map[string]int32{"prefill": 1},
	}
	oldEntries := []grovecorev1alpha1.PodGangEntry{
		{Name: "bpg-0", PodCliqueSetGenerationHash: "old-hash", PodCliques: map[string]int32{"frontend": 5}, PCSGReplicaIndices: map[string][]int32{"prefill": {0}}},
		{Name: "spg-1", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {1}}},
		{Name: "spg-2", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {2}}},
		{Name: "spg-3", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {3}}},
	}
	counter := 0
	builder := makeTestEntryBuilder(&counter)

	// Iteration 1: MVU pulls prefill[0] from BPG.
	state := computeNextPodGangMapState(template, oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 1)
	assert.Equal(t, int32(2), state.newEntries[0].PodCliques["frontend"])
	assert.Equal(t, []int32{0}, state.newEntries[0].PCSGReplicaIndices["prefill"])

	// Iteration 2: MVU pulls prefill[1] from spg-1, absorbs remaining 1F.
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 1)
	assert.Equal(t, int32(3), state.newEntries[0].PodCliques["frontend"])
	assert.Equal(t, []int32{1}, state.newEntries[0].PCSGReplicaIndices["prefill"])

	// Iteration 3: Tail-PGs for prefill[2] and prefill[3].
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 2)
	tailIndices := []int32{
		state.newEntries[0].PCSGReplicaIndices["prefill"][0],
		state.newEntries[1].PCSGReplicaIndices["prefill"][0],
	}
	slices.Sort(tailIndices)
	assert.Equal(t, []int32{2, 3}, tailIndices)
	for _, e := range state.newEntries {
		assert.Empty(t, e.PodCliques)
	}

	// Iteration 4: done.
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	assert.True(t, state.done)
	assert.Nil(t, state.newEntries)
}

// TestComputeNextPodGangMapState_SingleStandalonePCLQAndMultiplePCSGsUpdated tests a coherent
// update where one standalone PCLQ and multiple PCSGs are all updated.
//
// Starting state:
//
//	BPG "bpg-0" (old hash): {5 Frontend pods, 1 Prefill replica, 1 Decode replica}
//	SPGs (old hash): "spg-1" {P:1}, "spg-2" {P:1}, "spg-3" {P:1}, "spg-4" {D:1}, "spg-5" {D:1}
//	Frontend, Prefill PCSG, and Decode PCSG all updated.
//	Total: Frontend=5, Prefill=4, Decode=3.
//
// MVU template: {Frontend: 2 pods, Prefill: 1 replica, Decode: 1 replica}
//
// Expected:
//
//	Iteration 1: MVU {F:2, P:1, D:1}.
//	Iteration 2: MVU {F:3, P:1, D:1}. Absorbs 1F.
//	Iteration 3: Tail-PGs: {P:1}, {P:1}, {D:1}.
//	Iteration 4: Done.
func TestComputeNextPodGangMapState_SingleStandalonePCLQAndMultiplePCSGsUpdated(t *testing.T) {
	template := mvuTemplate{
		standalonePCLQs: map[string]int32{"frontend": 2},
		pcsgs:           map[string]int32{"prefill": 1, "decode": 1},
	}
	oldEntries := []grovecorev1alpha1.PodGangEntry{
		{Name: "bpg-0", PodCliqueSetGenerationHash: "old-hash", PodCliques: map[string]int32{"frontend": 5}, PCSGReplicaIndices: map[string][]int32{"prefill": {0}, "decode": {0}}},
		{Name: "spg-1", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {1}}},
		{Name: "spg-2", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {2}}},
		{Name: "spg-3", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {3}}},
		{Name: "spg-4", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"decode": {1}}},
		{Name: "spg-5", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"decode": {2}}},
	}
	counter := 0
	builder := makeTestEntryBuilder(&counter)

	// Iteration 1: MVU pulls prefill[0], decode[0] from BPG; F=2.
	state := computeNextPodGangMapState(template, oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 1)
	assert.Equal(t, int32(2), state.newEntries[0].PodCliques["frontend"])
	assert.Equal(t, []int32{0}, state.newEntries[0].PCSGReplicaIndices["prefill"])
	assert.Equal(t, []int32{0}, state.newEntries[0].PCSGReplicaIndices["decode"])

	// Iteration 2: MVU pulls prefill[1] from spg-1, decode[1] from spg-4; absorbs remaining 1F.
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 1)
	assert.Equal(t, int32(3), state.newEntries[0].PodCliques["frontend"])
	assert.Equal(t, []int32{1}, state.newEntries[0].PCSGReplicaIndices["prefill"])
	assert.Equal(t, []int32{1}, state.newEntries[0].PCSGReplicaIndices["decode"])

	// Iteration 3: Tail-PGs for prefill[2], prefill[3], decode[2].
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 3)
	prefillTailIndices, decodeTailIndices := []int32{}, []int32{}
	for _, e := range state.newEntries {
		prefillTailIndices = append(prefillTailIndices, e.PCSGReplicaIndices["prefill"]...)
		decodeTailIndices = append(decodeTailIndices, e.PCSGReplicaIndices["decode"]...)
	}
	slices.Sort(prefillTailIndices)
	slices.Sort(decodeTailIndices)
	assert.Equal(t, []int32{2, 3}, prefillTailIndices)
	assert.Equal(t, []int32{2}, decodeTailIndices)

	// Iteration 4: done.
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	assert.True(t, state.done)
	assert.Nil(t, state.newEntries)
}

// TestComputeNextPodGangMapState_MultipleStandalonePCLQsAndSinglePCSGUpdated tests a coherent
// update where multiple standalone PCLQs and one PCSG are updated.
//
// Starting state:
//
//	BPG "bpg-0" (old hash): {5 Frontend pods, 3 Backend pods, 1 Prefill replica}
//	SPGs (old hash): "spg-1" {P:1}, "spg-2" {P:1}, "spg-3" {P:1}
//	Frontend, Backend (standalone), and Prefill PCSG all updated.
//	Total: Frontend=5, Backend=3, Prefill=4.
//
// MVU template: {Frontend: 2 pods, Backend: 1 pod, Prefill: 1 replica}
//
// Expected:
//
//	Iteration 1: MVU {F:2, B:1, P:1}.
//	Iteration 2: MVU {F:3, B:2, P:1}. Absorbs F=1, B=1.
//	Iteration 3: Tail-PGs: {P:1}, {P:1}.
//	Iteration 4: Done.
func TestComputeNextPodGangMapState_MultipleStandalonePCLQsAndSinglePCSGUpdated(t *testing.T) {
	template := mvuTemplate{
		standalonePCLQs: map[string]int32{"frontend": 2, "backend": 1},
		pcsgs:           map[string]int32{"prefill": 1},
	}
	oldEntries := []grovecorev1alpha1.PodGangEntry{
		{Name: "bpg-0", PodCliqueSetGenerationHash: "old-hash", PodCliques: map[string]int32{"frontend": 5, "backend": 3}, PCSGReplicaIndices: map[string][]int32{"prefill": {0}}},
		{Name: "spg-1", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {1}}},
		{Name: "spg-2", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {2}}},
		{Name: "spg-3", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {3}}},
	}
	counter := 0
	builder := makeTestEntryBuilder(&counter)

	// Iteration 1: MVU {F:2, B:1, prefill[0]}.
	state := computeNextPodGangMapState(template, oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 1)
	assert.Equal(t, int32(2), state.newEntries[0].PodCliques["frontend"])
	assert.Equal(t, int32(1), state.newEntries[0].PodCliques["backend"])
	assert.Equal(t, []int32{0}, state.newEntries[0].PCSGReplicaIndices["prefill"])

	// Iteration 2: absorbs F=1, B=1; takes prefill[1] from spg-1.
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 1)
	assert.Equal(t, int32(3), state.newEntries[0].PodCliques["frontend"])
	assert.Equal(t, int32(2), state.newEntries[0].PodCliques["backend"])
	assert.Equal(t, []int32{1}, state.newEntries[0].PCSGReplicaIndices["prefill"])

	// Iteration 3: Tail-PGs for prefill[2] and prefill[3].
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 2)
	tailIndices := []int32{
		state.newEntries[0].PCSGReplicaIndices["prefill"][0],
		state.newEntries[1].PCSGReplicaIndices["prefill"][0],
	}
	slices.Sort(tailIndices)
	assert.Equal(t, []int32{2, 3}, tailIndices)
	for _, e := range state.newEntries {
		assert.Empty(t, e.PodCliques)
	}

	// Iteration 4: done.
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	assert.True(t, state.done)
	assert.Nil(t, state.newEntries)
}

// TestComputeNextPodGangMapState_MultipleStandalonePCLQsAndMultiplePCSGsUpdated tests a coherent
// update where multiple standalone PCLQs and multiple PCSGs are all updated.
//
// Starting state:
//
//	BPG "bpg-0" (old hash): {5 Frontend, 3 Backend, 1 Prefill, 1 Decode}
//	SPGs (old hash): "spg-1" {P:1}, "spg-2" {P:1}, "spg-3" {P:1}, "spg-4" {D:1}, "spg-5" {D:1}
//	All components updated. Total: F=5, B=3, P=4, D=3.
//
// MVU template: {Frontend: 2, Backend: 1, Prefill: 1, Decode: 1}
//
// Expected:
//
//	Iteration 1: MVU {F:2, B:1, P:1, D:1}.
//	Iteration 2: MVU {F:3, B:2, P:1, D:1}. Absorbs F=1, B=1.
//	Iteration 3: Tail-PGs: {P:1}, {P:1}, {D:1}.
//	Iteration 4: Done.
func TestComputeNextPodGangMapState_MultipleStandalonePCLQsAndMultiplePCSGsUpdated(t *testing.T) {
	template := mvuTemplate{
		standalonePCLQs: map[string]int32{"frontend": 2, "backend": 1},
		pcsgs:           map[string]int32{"prefill": 1, "decode": 1},
	}
	oldEntries := []grovecorev1alpha1.PodGangEntry{
		{Name: "bpg-0", PodCliqueSetGenerationHash: "old-hash", PodCliques: map[string]int32{"frontend": 5, "backend": 3}, PCSGReplicaIndices: map[string][]int32{"prefill": {0}, "decode": {0}}},
		{Name: "spg-1", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {1}}},
		{Name: "spg-2", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {2}}},
		{Name: "spg-3", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {3}}},
		{Name: "spg-4", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"decode": {1}}},
		{Name: "spg-5", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"decode": {2}}},
	}
	counter := 0
	builder := makeTestEntryBuilder(&counter)

	// Iteration 1: MVU pulls F=2, B=1, prefill[0], decode[0].
	state := computeNextPodGangMapState(template, oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 1)
	assert.Equal(t, int32(2), state.newEntries[0].PodCliques["frontend"])
	assert.Equal(t, int32(1), state.newEntries[0].PodCliques["backend"])
	assert.Equal(t, []int32{0}, state.newEntries[0].PCSGReplicaIndices["prefill"])
	assert.Equal(t, []int32{0}, state.newEntries[0].PCSGReplicaIndices["decode"])

	// Iteration 2: absorbs F=1, B=1; pulls prefill[1] from spg-1, decode[1] from spg-4.
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 1)
	assert.Equal(t, int32(3), state.newEntries[0].PodCliques["frontend"])
	assert.Equal(t, int32(2), state.newEntries[0].PodCliques["backend"])
	assert.Equal(t, []int32{1}, state.newEntries[0].PCSGReplicaIndices["prefill"])
	assert.Equal(t, []int32{1}, state.newEntries[0].PCSGReplicaIndices["decode"])

	// Iteration 3: Tail-PGs prefill[2], prefill[3], decode[2].
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 3)
	prefillTailIndices, decodeTailIndices := []int32{}, []int32{}
	for _, e := range state.newEntries {
		prefillTailIndices = append(prefillTailIndices, e.PCSGReplicaIndices["prefill"]...)
		decodeTailIndices = append(decodeTailIndices, e.PCSGReplicaIndices["decode"]...)
	}
	slices.Sort(prefillTailIndices)
	slices.Sort(decodeTailIndices)
	assert.Equal(t, []int32{2, 3}, prefillTailIndices)
	assert.Equal(t, []int32{2}, decodeTailIndices)

	// Iteration 4: done.
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	assert.True(t, state.done)
	assert.Nil(t, state.newEntries)
}

// TestComputeNextPodGangMapState_ExactMultipleOfMinAvailable tests a coherent update where
// pod count divides evenly by minAvailable. No absorption, no tail PGs.
//
// Starting state:
//
//	BPG "bpg-0" (old hash): {4 Frontend pods}
//
// MVU template: {Frontend: 2 pods}
//
// Expected:
//
//	Iteration 1: MVU {F:2}. Old BPG: {F:2}.
//	Iteration 2: MVU {F:2}. Old BPG removed.
//	Iteration 3: Done.
func TestComputeNextPodGangMapState_ExactMultipleOfMinAvailable(t *testing.T) {
	template := mvuTemplate{
		standalonePCLQs: map[string]int32{"frontend": 2},
		pcsgs:           map[string]int32{},
	}
	oldEntries := []grovecorev1alpha1.PodGangEntry{
		{Name: "bpg-0", PodCliqueSetGenerationHash: "old-hash", PodCliques: map[string]int32{"frontend": 4}},
	}
	counter := 0
	builder := makeTestEntryBuilder(&counter)

	// Iteration 1
	state := computeNextPodGangMapState(template, oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 1)
	assert.Equal(t, int32(2), state.newEntries[0].PodCliques["frontend"])
	require.Len(t, state.oldEntries, 1)
	assert.Equal(t, int32(2), state.oldEntries[0].PodCliques["frontend"])

	// Iteration 2
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 1)
	assert.Equal(t, int32(2), state.newEntries[0].PodCliques["frontend"])
	assert.Empty(t, state.oldEntries)

	// Iteration 3: done
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	assert.True(t, state.done)
	assert.Nil(t, state.newEntries)
}

// TestComputeNextPodGangMapState_NothingRemaining tests the edge case where the function is
// called with no old entries (update already completed).
func TestComputeNextPodGangMapState_NothingRemaining(t *testing.T) {
	template := mvuTemplate{
		standalonePCLQs: map[string]int32{"frontend": 2},
		pcsgs:           map[string]int32{"prefill": 1},
	}
	counter := 0
	builder := makeTestEntryBuilder(&counter)

	state := computeNextPodGangMapState(template, nil, nil, builder)
	assert.True(t, state.done)
	assert.Nil(t, state.newEntries)
}

// TestComputeNextPodGangMapState_OnlyTailPGsFromStart tests the edge case where canFormMVU is
// false from the first call because standalone PCLQ pods are missing in old entries but
// PCSG replicas remain.
//
// Starting state:
//
//	"spg-1" (old hash): {P:1}
//	"spg-2" (old hash): {P:1}
//	No Frontend pods in old entries.
//
// MVU template: {Frontend: 2 pods, Prefill: 1 replica}
//
// Expected: immediately produces Tail-PGs.
func TestComputeNextPodGangMapState_OnlyTailPGsFromStart(t *testing.T) {
	template := mvuTemplate{
		standalonePCLQs: map[string]int32{"frontend": 2},
		pcsgs:           map[string]int32{"prefill": 1},
	}
	oldEntries := []grovecorev1alpha1.PodGangEntry{
		{Name: "spg-1", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {0}}},
		{Name: "spg-2", PodCliqueSetGenerationHash: "old-hash", PCSGReplicaIndices: map[string][]int32{"prefill": {1}}},
	}
	counter := 0
	builder := makeTestEntryBuilder(&counter)

	// Cannot form MVU (F=0 < 2). Directly produces Tail-PGs for prefill[0] and prefill[1].
	state := computeNextPodGangMapState(template, oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 2)
	tailIndices := []int32{
		state.newEntries[0].PCSGReplicaIndices["prefill"][0],
		state.newEntries[1].PCSGReplicaIndices["prefill"][0],
	}
	slices.Sort(tailIndices)
	assert.Equal(t, []int32{0, 1}, tailIndices)
	for _, e := range state.newEntries {
		assert.Empty(t, e.PodCliques)
	}
	assert.Empty(t, state.oldEntries)

	// Next call: done.
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	assert.True(t, state.done)
	assert.Nil(t, state.newEntries)
}

// TestComputeNextPodGangMapState_DeductsFromLowestIndexFirst verifies that pods and replicas
// are always deducted from the lowest-indexed old entry first.
//
// Starting state:
//
//	"pg-0" (old hash): {F:1}
//	"pg-1" (old hash): {F:3}
//	"pg-2" (old hash): {F:1}
//
// MVU template: {Frontend: 2 pods}
// Total F=5. Iterations: MVU{F:2}, MVU{F:3}(absorbs 1), done.
//
// Expected:
//
//	Iteration 1: Takes F:1 from pg-0 and F:1 from pg-1. MVU {F:2}. pg-0 removed. Old: pg-1{F:2}, pg-2{F:1}.
//	Iteration 2: Takes F:2 from pg-1. Remaining F=1 < minAvail=2. Absorbs F:1 from pg-2.
//	             MVU {F:3}. pg-1 removed, pg-2 removed. Old: empty.
//	Iteration 3: Done.
func TestComputeNextPodGangMapState_DeductsFromLowestIndexFirst(t *testing.T) {
	template := mvuTemplate{
		standalonePCLQs: map[string]int32{"frontend": 2},
		pcsgs:           map[string]int32{},
	}
	oldEntries := []grovecorev1alpha1.PodGangEntry{
		{Name: "pg-0", PodCliqueSetGenerationHash: "old-hash", PodCliques: map[string]int32{"frontend": 1}},
		{Name: "pg-1", PodCliqueSetGenerationHash: "old-hash", PodCliques: map[string]int32{"frontend": 3}},
		{Name: "pg-2", PodCliqueSetGenerationHash: "old-hash", PodCliques: map[string]int32{"frontend": 1}},
	}
	counter := 0
	builder := makeTestEntryBuilder(&counter)

	// Iteration 1: takes 1 from pg-0 (exhausts it) and 1 from pg-1
	state := computeNextPodGangMapState(template, oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 1)
	assert.Equal(t, int32(2), state.newEntries[0].PodCliques["frontend"])
	// pg-0 removed (F=0), pg-1 has F:2, pg-2 has F:1
	require.Len(t, state.oldEntries, 2)
	assert.Equal(t, "pg-1", state.oldEntries[0].Name)
	assert.Equal(t, int32(2), state.oldEntries[0].PodCliques["frontend"])
	assert.Equal(t, "pg-2", state.oldEntries[1].Name)
	assert.Equal(t, int32(1), state.oldEntries[1].PodCliques["frontend"])

	// Iteration 2: takes 2 from pg-1, remaining F=1 < 2. Absorbs 1 from pg-2.
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	require.False(t, state.done)
	require.Len(t, state.newEntries, 1)
	assert.Equal(t, int32(3), state.newEntries[0].PodCliques["frontend"])
	assert.Empty(t, state.oldEntries)

	// Iteration 3: done
	state = computeNextPodGangMapState(template, state.oldEntries, nil, builder)
	assert.True(t, state.done)
	assert.Nil(t, state.newEntries)
}

// makeTestEntryBuilder creates a simple entryBuilder that produces entries with an incrementing
// name and the given composition. PCSG content is supplied as an explicit slice of replica
// indices (not a count), matching the post-refactor PodGangEntryBuilder signature.
func makeTestEntryBuilder(counter *int) componentutils.PodGangEntryBuilder {
	return func(standalonePCLQPods map[string]int32, pcsgReplicaIndices map[string][]int32, dependsOn []string) grovecorev1alpha1.PodGangEntry {
		name := fmt.Sprintf("mvu-%d", *counter)
		*counter++
		return grovecorev1alpha1.PodGangEntry{
			Name:                       name,
			PodCliqueSetGenerationHash: "new-hash",
			PodCliques:                 standalonePCLQPods,
			PCSGReplicaIndices:         pcsgReplicaIndices,
			DependsOn:                  dependsOn,
		}
	}
}

// TestPopPCSGIndicesFromOldEntries_SingleEntryEnoughReplicas pops indices from a single entry
// that holds enough replicas. Verifies smallest indices are popped first and the entry's
// slice has them removed.
func TestPopPCSGIndicesFromOldEntries_SingleEntryEnoughReplicas(t *testing.T) {
	entries := []grovecorev1alpha1.PodGangEntry{
		{Name: "old-0", PCSGReplicaIndices: map[string][]int32{"sg": {0, 1, 2, 3}}},
	}

	mutated, taken := popPCSGIndicesFromOldEntries(entries, "sg", 2)

	assert.Equal(t, []int32{0, 1}, taken, "should pop the two smallest indices")
	require.Len(t, mutated, 1)
	assert.Equal(t, []int32{2, 3}, mutated[0].PCSGReplicaIndices["sg"], "remaining indices should be the larger ones")
}

// TestPopPCSGIndicesFromOldEntries_AcrossMultipleEntries pops indices spanning multiple
// entries. Verifies the walk takes the smallest indices from the first entry before moving
// to the next, and that the returned slice is sorted across entries.
func TestPopPCSGIndicesFromOldEntries_AcrossMultipleEntries(t *testing.T) {
	entries := []grovecorev1alpha1.PodGangEntry{
		{Name: "old-0", PCSGReplicaIndices: map[string][]int32{"sg": {0, 5}}},
		{Name: "old-1", PCSGReplicaIndices: map[string][]int32{"sg": {2, 7}}},
		{Name: "old-2", PCSGReplicaIndices: map[string][]int32{"sg": {3}}},
	}

	mutated, taken := popPCSGIndicesFromOldEntries(entries, "sg", 4)

	// take min(4,2)=2 from old-0 → [0,5], remaining 2; take min(2,2)=2 from old-1 → [2,7],
	// remaining 0. Final taken (sorted): [0, 2, 5, 7].
	assert.Equal(t, []int32{0, 2, 5, 7}, taken)
	assert.Empty(t, mutated[0].PCSGReplicaIndices["sg"])
	assert.Empty(t, mutated[1].PCSGReplicaIndices["sg"])
	assert.Equal(t, []int32{3}, mutated[2].PCSGReplicaIndices["sg"], "third entry untouched")
}

// TestPopPCSGIndicesFromOldEntries_PartialFromEntry confirms that when an entry has more
// indices than needed, only the smallest are taken and the rest stay.
func TestPopPCSGIndicesFromOldEntries_PartialFromEntry(t *testing.T) {
	entries := []grovecorev1alpha1.PodGangEntry{
		{Name: "old-0", PCSGReplicaIndices: map[string][]int32{"sg": {1, 4, 8}}},
	}

	_, taken := popPCSGIndicesFromOldEntries(entries, "sg", 1)

	assert.Equal(t, []int32{1}, taken)
	assert.Equal(t, []int32{4, 8}, entries[0].PCSGReplicaIndices["sg"])
}

// TestPopPCSGIndicesFromOldEntries_FewerAvailableThanRequested asks for more than is
// available. Verifies it returns whatever's there without erroring.
func TestPopPCSGIndicesFromOldEntries_FewerAvailableThanRequested(t *testing.T) {
	entries := []grovecorev1alpha1.PodGangEntry{
		{Name: "old-0", PCSGReplicaIndices: map[string][]int32{"sg": {2}}},
	}

	_, taken := popPCSGIndicesFromOldEntries(entries, "sg", 5)

	assert.Equal(t, []int32{2}, taken)
	assert.Empty(t, entries[0].PCSGReplicaIndices["sg"])
}

// TestPopPCSGIndicesFromOldEntries_UnsortedSliceWithinEntry confirms defensive sorting:
// even if an entry's slice is unsorted on entry, the function pops the smallest first.
func TestPopPCSGIndicesFromOldEntries_UnsortedSliceWithinEntry(t *testing.T) {
	entries := []grovecorev1alpha1.PodGangEntry{
		{Name: "old-0", PCSGReplicaIndices: map[string][]int32{"sg": {7, 2, 5}}},
	}

	_, taken := popPCSGIndicesFromOldEntries(entries, "sg", 2)

	assert.Equal(t, []int32{2, 5}, taken, "should pop the two smallest regardless of input order")
	assert.Equal(t, []int32{7}, entries[0].PCSGReplicaIndices["sg"])
}

// TestPopPCSGIndicesFromOldEntries_DifferentPCSGUntouched confirms that popping for one
// PCSG does not disturb other PCSGs' index slices on the same entry.
func TestPopPCSGIndicesFromOldEntries_DifferentPCSGUntouched(t *testing.T) {
	entries := []grovecorev1alpha1.PodGangEntry{
		{Name: "old-0", PCSGReplicaIndices: map[string][]int32{"sg-a": {0, 1}, "sg-b": {0, 1}}},
	}

	_, taken := popPCSGIndicesFromOldEntries(entries, "sg-a", 1)

	assert.Equal(t, []int32{0}, taken)
	assert.Equal(t, []int32{1}, entries[0].PCSGReplicaIndices["sg-a"])
	assert.Equal(t, []int32{0, 1}, entries[0].PCSGReplicaIndices["sg-b"], "sg-b unchanged")
}

// TestPopPCSGIndicesFromOldEntries_ZeroCount popping zero indices is a no-op.
func TestPopPCSGIndicesFromOldEntries_ZeroCount(t *testing.T) {
	entries := []grovecorev1alpha1.PodGangEntry{
		{Name: "old-0", PCSGReplicaIndices: map[string][]int32{"sg": {0, 1, 2}}},
	}

	_, taken := popPCSGIndicesFromOldEntries(entries, "sg", 0)

	assert.Empty(t, taken)
	assert.Equal(t, []int32{0, 1, 2}, entries[0].PCSGReplicaIndices["sg"], "entry untouched")
}

// TestSumPCSGReplicasInEntries_SumsLengthsAcrossEntries verifies sum is computed from slice
// lengths, not from a separate count.
func TestSumPCSGReplicasInEntries_SumsLengthsAcrossEntries(t *testing.T) {
	entries := []grovecorev1alpha1.PodGangEntry{
		{Name: "e-0", PCSGReplicaIndices: map[string][]int32{"sg": {0, 1}}},
		{Name: "e-1", PCSGReplicaIndices: map[string][]int32{"sg": {2}}},
		{Name: "e-2", PCSGReplicaIndices: map[string][]int32{"sg-other": {0}}},
	}

	assert.Equal(t, int32(3), sumPCSGReplicasInEntries(entries, "sg"))
	assert.Equal(t, int32(1), sumPCSGReplicasInEntries(entries, "sg-other"))
	assert.Equal(t, int32(0), sumPCSGReplicasInEntries(entries, "sg-missing"))
}

// TestRemoveEmptyEntries_KeepsEntriesWithIndicesOrPods verifies an entry stays as long as
// it has at least one positive PCLQ count or one non-empty PCSG index slice.
func TestRemoveEmptyEntries_KeepsEntriesWithIndicesOrPods(t *testing.T) {
	entries := []grovecorev1alpha1.PodGangEntry{
		{Name: "all-empty", PodCliques: map[string]int32{"f": 0}, PCSGReplicaIndices: map[string][]int32{"sg": {}}},
		{Name: "has-pclq", PodCliques: map[string]int32{"f": 2}},
		{Name: "has-pcsg", PCSGReplicaIndices: map[string][]int32{"sg": {3}}},
		{Name: "all-nil"},
	}

	out := removeEmptyEntries(entries)

	require.Len(t, out, 2)
	names := []string{out[0].Name, out[1].Name}
	assert.Contains(t, names, "has-pclq")
	assert.Contains(t, names, "has-pcsg")
}

// TestComputeNextMVUPodGang_AssignsLowestIndicesToMVU verifies that during a coherent
// update, the MVU PodGang absorbs the LOWEST replica indices from old entries, leaving
// higher-indexed replicas in old entries (which become Tail-PGs once no further MVU forms).
//
// Starting state:
//
//	Old BPG: {sg replicas [0]} (minAvailable indices)
//	Old SPG-0: {sg replicas [1]}
//	Old SPG-1: {sg replicas [2]}
//	Old SPG-2: {sg replicas [3]}
//
// MVU template requires sg minAvailable=2.
//
// Expected after iteration 1:
//
//	MVU absorbs sg=[0, 1] (lowest two across BPG and SPG-0).
//	Old BPG empty (entry removed). SPG-0 empty (removed). SPG-1 and SPG-2 retain [2] and [3].
func TestComputeNextMVUPodGang_AssignsLowestIndicesToMVU(t *testing.T) {
	template := mvuTemplate{
		standalonePCLQs: map[string]int32{},
		pcsgs:           map[string]int32{"sg": 2},
	}
	oldEntries := []grovecorev1alpha1.PodGangEntry{
		{Name: "bpg-0", PodCliqueSetGenerationHash: "old", PCSGReplicaIndices: map[string][]int32{"sg": {0}}},
		{Name: "spg-0", PodCliqueSetGenerationHash: "old", PCSGReplicaIndices: map[string][]int32{"sg": {1}}},
		{Name: "spg-1", PodCliqueSetGenerationHash: "old", PCSGReplicaIndices: map[string][]int32{"sg": {2}}},
		{Name: "spg-2", PodCliqueSetGenerationHash: "old", PCSGReplicaIndices: map[string][]int32{"sg": {3}}},
	}
	counter := 0
	builder := makeTestEntryBuilder(&counter)

	updatedOld, newEntries := computeNextMVUPodGang(template, oldEntries, builder)

	require.Len(t, newEntries, 1)
	assert.Equal(t, []int32{0, 1}, newEntries[0].PCSGReplicaIndices["sg"], "MVU should absorb the two lowest indices")

	require.Len(t, updatedOld, 2, "BPG and SPG-0 should be removed (their indices absorbed)")
	assert.Equal(t, []int32{2}, updatedOld[0].PCSGReplicaIndices["sg"])
	assert.Equal(t, []int32{3}, updatedOld[1].PCSGReplicaIndices["sg"])
}

// TestComputeNextMVUPodGang_PCSGAbsorptionDoesNotHappen verifies the documented invariant
// that PCSG replicas are NOT absorbed beyond what the MVU template requires (only standalone
// PCLQ pods are absorbed when no more MVU can form). Leftover PCSG replicas should remain
// in old entries to become Tail-PGs.
func TestComputeNextMVUPodGang_PCSGAbsorptionDoesNotHappen(t *testing.T) {
	// Template requires sg=2 plus 2 frontend pods. After this MVU step, only 1 sg replica is
	// left so another MVU cannot form. Frontend pods (none left here) would be absorbed; sg
	// replicas must NOT be absorbed.
	template := mvuTemplate{
		standalonePCLQs: map[string]int32{"frontend": 2},
		pcsgs:           map[string]int32{"sg": 2},
	}
	oldEntries := []grovecorev1alpha1.PodGangEntry{
		{Name: "bpg-0", PodCliqueSetGenerationHash: "old", PodCliques: map[string]int32{"frontend": 2}, PCSGReplicaIndices: map[string][]int32{"sg": {0, 1, 2}}},
	}
	counter := 0
	builder := makeTestEntryBuilder(&counter)

	updatedOld, newEntries := computeNextMVUPodGang(template, oldEntries, builder)

	require.Len(t, newEntries, 1)
	assert.Equal(t, []int32{0, 1}, newEntries[0].PCSGReplicaIndices["sg"], "MVU absorbs only the template-required count")
	assert.Equal(t, int32(2), newEntries[0].PodCliques["frontend"])

	require.Len(t, updatedOld, 1, "BPG should still hold the leftover sg index")
	assert.Equal(t, []int32{2}, updatedOld[0].PCSGReplicaIndices["sg"], "leftover sg index stays for Tail-PG")
}

// TestComputeTailPodGangs_OneEntryPerLeftoverIndex verifies that each leftover PCSG replica
// becomes its own Tail-PG, with that single index recorded.
func TestComputeTailPodGangs_OneEntryPerLeftoverIndex(t *testing.T) {
	template := mvuTemplate{
		standalonePCLQs: map[string]int32{},
		pcsgs:           map[string]int32{"sg": 1},
	}
	oldEntries := []grovecorev1alpha1.PodGangEntry{
		{Name: "bpg-0", PodCliqueSetGenerationHash: "old", PCSGReplicaIndices: map[string][]int32{"sg": {2, 3}}},
		{Name: "spg-0", PodCliqueSetGenerationHash: "old", PCSGReplicaIndices: map[string][]int32{"sg": {5}}},
	}
	counter := 0
	builder := makeTestEntryBuilder(&counter)
	mpgNames := []string{"mpg-0"}

	updatedOld, newEntries, done := computeTailPodGangs(template, oldEntries, mpgNames, builder)

	assert.False(t, done)
	require.Len(t, newEntries, 3, "one Tail-PG per leftover replica index")
	// Tail-PGs are minted in pop order: 2, 3, 5.
	assert.Equal(t, []int32{2}, newEntries[0].PCSGReplicaIndices["sg"])
	assert.Equal(t, []int32{3}, newEntries[1].PCSGReplicaIndices["sg"])
	assert.Equal(t, []int32{5}, newEntries[2].PCSGReplicaIndices["sg"])
	for _, e := range newEntries {
		assert.Equal(t, mpgNames, e.DependsOn, "Tail-PGs depend on the supplied MPG names")
	}
	assert.Empty(t, updatedOld, "all leftover entries fully drained")
}

// TestComputeTailPodGangs_DoneWhenNothingLeft confirms the done flag is set when there are
// no leftover indices to drain.
func TestComputeTailPodGangs_DoneWhenNothingLeft(t *testing.T) {
	template := mvuTemplate{
		standalonePCLQs: map[string]int32{},
		pcsgs:           map[string]int32{"sg": 1},
	}
	oldEntries := []grovecorev1alpha1.PodGangEntry{}
	counter := 0
	builder := makeTestEntryBuilder(&counter)

	updatedOld, newEntries, done := computeTailPodGangs(template, oldEntries, nil, builder)

	assert.True(t, done)
	assert.Nil(t, newEntries)
	assert.Empty(t, updatedOld)
}

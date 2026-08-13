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

package podgangmap

import (
	"strconv"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
)

const (
	testPCSName   = "pcs"
	testNamespace = "default"
	testGenHash   = "hash1"
)

func TestBuildBootstrapEntries(t *testing.T) {
	tests := []struct {
		name          string
		pcs           *grovecorev1alpha1.PodCliqueSet
		expectedRoles []grovecorev1alpha1.PodGangEntryRole
		assertEntries func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry)
	}{
		{
			name: "standalone clique only, no scaling group",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithStandaloneCliqueReplicas("clq-a", 3).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).
				Build(),
			expectedRoles: []grovecorev1alpha1.PodGangEntryRole{grovecorev1alpha1.PodGangEntryRoleAnchor},
			assertEntries: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				assert.Equal(t, int32(3), anchor.PodCliques["clq-a"])
				assert.Empty(t, anchor.PCSGReplicaIndices)
				assert.Nil(t, anchor.DependsOn)
			},
		},
		{
			name: "scaling group only, no standalone clique, replicas equal minAvailable",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithScalingGroupConfig("sg", []string{"c"}, 2, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).
				Build(),
			expectedRoles: []grovecorev1alpha1.PodGangEntryRole{
				grovecorev1alpha1.PodGangEntryRoleAnchor,
				grovecorev1alpha1.PodGangEntryRoleScaleOut,
			},
			assertEntries: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				assert.Empty(t, anchor.PodCliques)
				assert.Equal(t, []int32{0, 1}, anchor.PCSGReplicaIndices["sg"])
				scaleOut := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleScaleOut)
				assert.Empty(t, scaleOut.PCSGReplicaIndices["sg"])
				assert.Equal(t, []string{anchor.Epoch}, scaleOut.DependsOn)
			},
		},
		{
			name: "scaling group only, replicas above minAvailable yields tail",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithScalingGroupConfig("sg", []string{"c"}, 4, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).
				Build(),
			expectedRoles: []grovecorev1alpha1.PodGangEntryRole{
				grovecorev1alpha1.PodGangEntryRoleAnchor,
				grovecorev1alpha1.PodGangEntryRoleTail,
				grovecorev1alpha1.PodGangEntryRoleScaleOut,
			},
			assertEntries: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				assert.Empty(t, anchor.PodCliques)
				assert.Equal(t, []int32{0, 1}, anchor.PCSGReplicaIndices["sg"])
				tail := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleTail)
				assert.Equal(t, []int32{2, 3}, tail.PCSGReplicaIndices["sg"])
				assert.Equal(t, []string{anchor.Epoch}, tail.DependsOn)
			},
		},
		{
			name: "standalone clique and scaling group together",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithStandaloneCliqueReplicas("clq-a", 3).
				WithScalingGroupConfig("sg", []string{"c"}, 4, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).
				Build(),
			expectedRoles: []grovecorev1alpha1.PodGangEntryRole{
				grovecorev1alpha1.PodGangEntryRoleAnchor,
				grovecorev1alpha1.PodGangEntryRoleTail,
				grovecorev1alpha1.PodGangEntryRoleScaleOut,
			},
			assertEntries: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				assert.Equal(t, int32(3), anchor.PodCliques["clq-a"])
				assert.Equal(t, []int32{0, 1}, anchor.PCSGReplicaIndices["sg"])
				tail := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleTail)
				assert.Equal(t, []int32{2, 3}, tail.PCSGReplicaIndices["sg"])
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := clocktesting.NewFakeClock(time.Unix(0, 1000))
			actual := buildBootstrapEntries(tt.pcs, clk)
			assert.Equal(t, tt.expectedRoles, testutils.RolesOf(actual))
			tt.assertEntries(t, actual)
		})
	}
}

func TestSyncEntries(t *testing.T) {
	scaleOutEpoch := strconv.FormatInt(time.Unix(0, 5000).UnixNano(), 10)

	tests := []struct {
		name            string
		pcs             *grovecorev1alpha1.PodCliqueSet
		existingEntries []grovecorev1alpha1.PodGangEntry
		standalonePCLQs []grovecorev1alpha1.PodClique
		pcsgs           []grovecorev1alpha1.PodCliqueScalingGroup
		assertResult    func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry)
	}{
		{
			name: "no change keeps entries as is",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithStandaloneCliqueReplicas("clq-a", 3).
				WithScalingGroupConfig("sg", []string{"c"}, 2, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).Build(),
			existingEntries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(map[string]int32{"clq-a": 3}, map[string][]int32{"sg": {0, 1}}),
				scaleOutEntry(nil),
			},
			standalonePCLQs: []grovecorev1alpha1.PodClique{standalonePCLQ("clq-a", 3)},
			pcsgs:           []grovecorev1alpha1.PodCliqueScalingGroup{pcsg("sg", 2)},
			assertResult: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				assert.Equal(t, []int32{0, 1}, anchor.PCSGReplicaIndices["sg"])
				assert.Equal(t, int32(3), anchor.PodCliques["clq-a"])
			},
		},
		{
			name: "scale out appends new index to scaleout entry",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithScalingGroupConfig("sg", []string{"c"}, 2, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).Build(),
			existingEntries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(nil, map[string][]int32{"sg": {0, 1}}),
				scaleOutEntry(nil),
			},
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{pcsg("sg", 3)},
			assertResult: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				scaleOut := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleScaleOut)
				assert.Equal(t, []int32{2}, scaleOut.PCSGReplicaIndices["sg"])
			},
		},
		{
			name: "scale in drains only the scaleout entry",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithScalingGroupConfig("sg", []string{"c"}, 4, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).Build(),
			existingEntries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(nil, map[string][]int32{"sg": {0, 1}}),
				tailEntry(map[string][]int32{"sg": {2, 3}}),
				scaleOutEntry(map[string][]int32{"sg": {4, 5}}),
			},
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{pcsg("sg", 4)},
			assertResult: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				// live=4, current=6, drain 2: both come from the scaleout entry (highest indices first).
				assert.Empty(t, testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleScaleOut).PCSGReplicaIndices["sg"])
				assert.Equal(t, []int32{2, 3}, testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleTail).PCSGReplicaIndices["sg"])
				// The anchor is never drained; its MinAvailable indices are preserved.
				assert.Equal(t, []int32{0, 1}, testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor).PCSGReplicaIndices["sg"])
			},
		},
		{
			name: "scale in drains tail when scaleout has no indices",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithScalingGroupConfig("sg", []string{"c"}, 4, 2).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).Build(),
			existingEntries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(nil, map[string][]int32{"sg": {0, 1}}),
				tailEntry(map[string][]int32{"sg": {2, 3}}),
				scaleOutEntry(nil),
			},
			pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{pcsg("sg", 3)},
			assertResult: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				// live=3, current=4, drain 1: the scaleout entry has nothing, so the tail is drained.
				assert.Empty(t, testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleScaleOut).PCSGReplicaIndices["sg"])
				assert.Equal(t, []int32{2}, testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleTail).PCSGReplicaIndices["sg"])
				// The anchor is never drained; its MinAvailable indices are preserved.
				assert.Equal(t, []int32{0, 1}, testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor).PCSGReplicaIndices["sg"])
			},
		},
		{
			name: "standalone pod count refreshed from live replicas",
			pcs: testutils.NewPodCliqueSetBuilder(testPCSName, testNamespace, "uid").
				WithStandaloneCliqueReplicas("clq-a", 3).
				WithPodCliqueSetGenerationHash(ptr.To(testGenHash)).Build(),
			existingEntries: []grovecorev1alpha1.PodGangEntry{
				anchorEntry(map[string]int32{"clq-a": 3}, nil),
			},
			standalonePCLQs: []grovecorev1alpha1.PodClique{standalonePCLQ("clq-a", 5)},
			assertResult: func(t *testing.T, entries []grovecorev1alpha1.PodGangEntry) {
				anchor := testutils.EntryByRole(entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
				assert.Equal(t, int32(5), anchor.PodCliques["clq-a"])
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := reconcileEntries(tt.pcs, tt.existingEntries, tt.standalonePCLQs, tt.pcsgs, 0, scaleOutEpoch)
			require.NoError(t, err)
			tt.assertResult(t, actual)
		})
	}
}

func anchorEntry(podCliques map[string]int32, pcsgIndices map[string][]int32) grovecorev1alpha1.PodGangEntry {
	return testutils.NewPodGangEntryBuilder(testGenHash, "100").
		WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
		WithPodCliques(podCliques).
		WithPCSGReplicaIndices(pcsgIndices).
		Build()
}

func tailEntry(pcsgIndices map[string][]int32) grovecorev1alpha1.PodGangEntry {
	return testutils.NewPodGangEntryBuilder(testGenHash, "101").
		WithRole(grovecorev1alpha1.PodGangEntryRoleTail).
		WithPCSGReplicaIndices(pcsgIndices).
		WithDependsOn("100").
		Build()
}

func scaleOutEntry(pcsgIndices map[string][]int32) grovecorev1alpha1.PodGangEntry {
	return testutils.NewPodGangEntryBuilder(testGenHash, "102").
		WithRole(grovecorev1alpha1.PodGangEntryRoleScaleOut).
		WithPCSGReplicaIndices(pcsgIndices).
		WithDependsOn("100").
		Build()
}

func standalonePCLQ(cliqueName string, replicas int32) grovecorev1alpha1.PodClique {
	return grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name: apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: testPCSName, Replica: 0}, cliqueName),
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: replicas},
	}
}

func pcsg(pcsgConfigName string, replicas int32) grovecorev1alpha1.PodCliqueScalingGroup {
	return grovecorev1alpha1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: apicommon.GeneratePodCliqueScalingGroupName(apicommon.ResourceNameReplica{Name: testPCSName, Replica: 0}, pcsgConfigName),
		},
		Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{Replicas: replicas, MinAvailable: ptr.To(int32(2))},
	}
}

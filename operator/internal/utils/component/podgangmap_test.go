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

package component

import (
	"context"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestGetPodGangMap(t *testing.T) {
	const namespace = "default"
	pgm := testutils.NewPodGangMapBuilder("pcs", namespace, types.UID("uid"), 0).Build()
	cl := testutils.CreateDefaultFakeClient([]client.Object{pgm})
	pcsObjectKey := client.ObjectKey{Namespace: namespace, Name: "pcs"}

	t.Run("returns the PodGangMap when it exists", func(t *testing.T) {
		actual, err := GetPodGangMap(context.Background(), cl, pcsObjectKey, 0)
		require.NoError(t, err)
		assert.Equal(t, "pcs-0", actual.Name)
	})

	t.Run("returns a NotFound error when it does not exist", func(t *testing.T) {
		_, err := GetPodGangMap(context.Background(), cl, pcsObjectKey, 9)
		assert.True(t, apierrors.IsNotFound(err))
	})
}

func TestListPodGangMapsForPCS(t *testing.T) {
	const (
		pcsName   = "pcs"
		namespace = "default"
		pcsUID    = types.UID("uid")
	)

	pgm0 := testutils.NewPodGangMapBuilder(pcsName, namespace, pcsUID, 0).Build()
	pgm1 := testutils.NewPodGangMapBuilder(pcsName, namespace, pcsUID, 1).Build()
	// A PodGangMap owned by a PodCliqueSet of a different name must not be returned.
	otherPGM := testutils.NewPodGangMapBuilder("other-pcs", namespace, types.UID("other-uid"), 0).Build()
	// A PodGangMap of the same name but controlled by an older PodCliqueSet UID must not be returned,
	// modelling a same-name recreation before garbage collection removed the old PodGangMap.
	stalePGM := testutils.NewPodGangMapBuilder(pcsName, namespace, types.UID("old-uid"), 2).Build()

	fakeClient := testutils.CreateDefaultFakeClient([]client.Object{pgm0, pgm1, otherPGM, stalePGM})

	actual, err := ListPodGangMapsForPCS(context.Background(), fakeClient, metav1.ObjectMeta{Namespace: namespace, Name: pcsName, UID: pcsUID})
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{pgm0.Name, pgm1.Name}, pgmNames(actual))
}

func TestPodGangMapByPCSReplicaIndex(t *testing.T) {
	const (
		pcsName   = "pcs"
		namespace = "default"
		pcsUID    = types.UID("uid")
	)

	tests := []struct {
		name        string
		pgms        []grovecorev1alpha1.PodGangMap
		expectErr   bool
		expectedIdx []int
	}{
		{
			name: "groups by replica index",
			pgms: []grovecorev1alpha1.PodGangMap{
				*testutils.NewPodGangMapBuilder(pcsName, namespace, pcsUID, 0).Build(),
				*testutils.NewPodGangMapBuilder(pcsName, namespace, pcsUID, 2).Build(),
			},
			expectedIdx: []int{0, 2},
		},
		{
			name:      "missing replica-index label is an error",
			pgms:      []grovecorev1alpha1.PodGangMap{pgmWithoutReplicaIndexLabel(pcsName, namespace)},
			expectErr: true,
		},
		{
			name:      "non-integer replica-index label is an error",
			pgms:      []grovecorev1alpha1.PodGangMap{pgmWithReplicaIndexLabel(pcsName, namespace, "abc")},
			expectErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := PodGangMapByPCSReplicaIndex(tt.pgms)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.expectedIdx, indicesOf(actual))
		})
	}
}

func pgmNames(pgms []grovecorev1alpha1.PodGangMap) []string {
	names := make([]string, 0, len(pgms))
	for i := range pgms {
		names = append(names, pgms[i].Name)
	}
	return names
}

func indicesOf(byIndex map[int]*grovecorev1alpha1.PodGangMap) []int {
	idx := make([]int, 0, len(byIndex))
	for i := range byIndex {
		idx = append(idx, i)
	}
	return idx
}

func pgmWithoutReplicaIndexLabel(pcsName, namespace string) grovecorev1alpha1.PodGangMap {
	return grovecorev1alpha1.PodGangMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pcsName + "-0",
			Namespace: namespace,
			Labels:    map[string]string{apicommon.LabelPartOfKey: pcsName},
		},
	}
}

func pgmWithReplicaIndexLabel(pcsName, namespace, value string) grovecorev1alpha1.PodGangMap {
	return grovecorev1alpha1.PodGangMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pcsName + "-x",
			Namespace: namespace,
			Labels:    map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: value},
		},
	}
}

func TestDependsOnForEpoch(t *testing.T) {
	const (
		pcsName       = "pcs"
		namespace     = "default"
		genHash       = "hash-1"
		pcsgName      = "sg"
		anchorEpoch   = "1000"
		tailEpoch     = "1001"
		scaleOutEpoch = "1002"
	)
	pgm := testutils.NewPodGangMapBuilder(pcsName, namespace, "uid", 0).WithEntries(
		testutils.NewAnchorEntry(genHash, anchorEpoch, pcsgName, 0),
		testutils.NewPodGangEntryBuilder(genHash, tailEpoch).
			WithRole(grovecorev1alpha1.PodGangEntryRoleTail).
			WithPCSGReplicaIndices(map[string][]int32{pcsgName: {1, 2}}).
			WithDependsOn(anchorEpoch).Build(),
		testutils.NewPodGangEntryBuilder(genHash, scaleOutEpoch).
			WithRole(grovecorev1alpha1.PodGangEntryRoleScaleOut).
			WithDependsOn(anchorEpoch).Build(),
	).Build()

	tests := []struct {
		name              string
		epoch             string
		expectedDependsOn []string
	}{
		{"anchor entry epoch has no dependency", anchorEpoch, nil},
		{"tail entry epoch depends on the anchor epoch", tailEpoch, []string{anchorEpoch}},
		{"scale-out entry epoch depends on the anchor epoch", scaleOutEpoch, []string{anchorEpoch}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := DependsOnForEpoch(pgm, test.epoch)
			require.NoError(t, err)
			assert.Equal(t, test.expectedDependsOn, actual)
		})
	}

	t.Run("errors when no entry carries the epoch", func(t *testing.T) {
		_, err := DependsOnForEpoch(pgm, "9999")
		require.Error(t, err)
	})
}

func TestAnchorPodGangEpoch(t *testing.T) {
	const (
		pcsName     = "pcs"
		namespace   = "default"
		genHash     = "hash-1"
		anchorEpoch = "1000"
	)

	t.Run("returns the lowest-epoch anchor entry epoch", func(t *testing.T) {
		pgm := testutils.NewPodGangMapBuilder(pcsName, namespace, "uid", 0).WithEntries(
			testutils.NewPodGangEntryBuilder(genHash, "2000").
				WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).Build(),
			testutils.NewPodGangEntryBuilder(genHash, anchorEpoch).
				WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).Build(),
			testutils.NewPodGangEntryBuilder(genHash, "1002").
				WithRole(grovecorev1alpha1.PodGangEntryRoleScaleOut).Build(),
		).Build()
		actual, err := AnchorPodGangEpoch(pgm)
		require.NoError(t, err)
		assert.Equal(t, anchorEpoch, actual)
	})

	t.Run("errors when no anchor entry exists", func(t *testing.T) {
		pgm := testutils.NewPodGangMapBuilder(pcsName, namespace, "uid", 0).WithEntries(
			testutils.NewPodGangEntryBuilder(genHash, "1002").
				WithRole(grovecorev1alpha1.PodGangEntryRoleScaleOut).Build(),
		).Build()
		_, err := AnchorPodGangEpoch(pgm)
		require.Error(t, err)
	})
}

// TestMinAvailableAnchorEpoch verifies that the MinAvailable anchor (the lowest-epoch anchor) is
// selected, optionally filtered by generation hash, that non-anchor entries are ignored, and that a
// non-numeric epoch surfaces an error.
func TestMinAvailableAnchorEpoch(t *testing.T) {
	entries := []grovecorev1alpha1.PodGangEntry{
		{Epoch: "300", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleAnchor},
		{Epoch: "100", PodCliqueSetGenerationHash: "v1", Role: grovecorev1alpha1.PodGangEntryRoleAnchor},
		{Epoch: "200", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleAnchor},
		{Epoch: "150", PodCliqueSetGenerationHash: "v2", Role: grovecorev1alpha1.PodGangEntryRoleTail},
	}

	t.Run("returns the lowest-epoch anchor across all generations when the hash filter is nil", func(t *testing.T) {
		epoch, found, err := MinAvailableAnchorEpoch(entries, nil)
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "100", epoch)
	})

	t.Run("filters to the given generation hash and ignores the lower-epoch tail", func(t *testing.T) {
		epoch, found, err := MinAvailableAnchorEpoch(entries, ptr.To("v2"))
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "200", epoch)
	})

	t.Run("reports not found when no anchor matches the generation hash", func(t *testing.T) {
		epoch, found, err := MinAvailableAnchorEpoch(entries, ptr.To("v3"))
		require.NoError(t, err)
		assert.False(t, found)
		assert.Equal(t, "", epoch)
	})

	t.Run("errors on a non-numeric anchor epoch", func(t *testing.T) {
		bad := []grovecorev1alpha1.PodGangEntry{{Epoch: "abc", Role: grovecorev1alpha1.PodGangEntryRoleAnchor}}
		_, _, err := MinAvailableAnchorEpoch(bad, nil)
		require.Error(t, err)
	})
}

func TestPodGangNameForPCSGReplica(t *testing.T) {
	const (
		pcsName       = "pcs"
		namespace     = "default"
		genHash       = "hash-1"
		pcsgName      = "sg"
		anchorEpoch   = "1000"
		tailEpoch     = "1001"
		scaleOutEpoch = "1002"
	)
	pcsRnr := apicommon.ResourceNameReplica{Name: pcsName, Replica: 0}
	pgm := testutils.NewPodGangMapBuilder(pcsName, namespace, "uid", 0).WithEntries(
		testutils.NewAnchorEntry(genHash, anchorEpoch, pcsgName, 0),
		testutils.NewTailEntry(genHash, tailEpoch, pcsgName, 1, 2),
		testutils.NewScaleOutEntry(genHash, scaleOutEpoch, pcsgName, 3),
	).Build()

	tests := []struct {
		name         string
		index        int32
		expectedName string
	}{
		{"anchor replica resolves to the anchor PodGang name", 0, apicommon.GenerateAnchorPodGangName(pcsRnr, anchorEpoch)},
		{"tail replica resolves to a non-anchor PodGang name", 2, apicommon.GenerateNonAnchorPodGangName(pcsRnr, tailEpoch, pcsgName, 2)},
		{"placed scale-out replica resolves to a non-anchor PodGang name", 3, apicommon.GenerateNonAnchorPodGangName(pcsRnr, scaleOutEpoch, pcsgName, 3)},
		{"not-yet-placed scale-out replica falls back to the ScaleOut entry", 4, apicommon.GenerateNonAnchorPodGangName(pcsRnr, scaleOutEpoch, pcsgName, 4)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := PodGangNameForPCSGReplica(pgm, pcsRnr, pcsgName, test.index)
			require.NoError(t, err)
			assert.Equal(t, test.expectedName, actual)
		})
	}

	t.Run("errors when no owning entry and no ScaleOut entry exist", func(t *testing.T) {
		anchorOnly := testutils.NewPodGangMapBuilder(pcsName, namespace, "uid", 0).WithEntries(
			testutils.NewAnchorEntry(genHash, anchorEpoch, pcsgName, 0),
		).Build()
		_, err := PodGangNameForPCSGReplica(anchorOnly, pcsRnr, pcsgName, 5)
		require.Error(t, err)
	})
}

// TestIndexPodGangEntriesByEpoch verifies the entries are indexed by their epoch.
func TestIndexPodGangEntriesByEpoch(t *testing.T) {
	t.Run("indexes each entry under its epoch", func(t *testing.T) {
		anchor := testutils.NewPodGangEntryBuilder("hash", "1000").WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).Build()
		tail := testutils.NewPodGangEntryBuilder("hash", "1001").WithRole(grovecorev1alpha1.PodGangEntryRoleTail).WithDependsOn("1000").Build()

		actual := IndexPodGangEntriesByEpoch([]grovecorev1alpha1.PodGangEntry{anchor, tail})

		require.Len(t, actual, 2)
		assert.Equal(t, anchor, actual["1000"])
		assert.Equal(t, tail, actual["1001"])
	})
	t.Run("returns an empty map for no entries", func(t *testing.T) {
		actual := IndexPodGangEntriesByEpoch(nil)
		assert.Empty(t, actual)
	})
}

func TestLatestEpochForGenerationHash(t *testing.T) {
	const (
		hashA = "hash-a"
		hashB = "hash-b"
	)
	entries := []grovecorev1alpha1.PodGangEntry{
		testutils.NewPodGangEntryBuilder(hashA, "1000").Build(),
		testutils.NewPodGangEntryBuilder(hashA, "3000").Build(),
		testutils.NewPodGangEntryBuilder(hashB, "2000").Build(),
	}

	t.Run("returns the largest epoch for the queried generation hash", func(t *testing.T) {
		latestEpoch, err := LatestEpochForGenerationHash(entries, hashA)
		require.NoError(t, err)
		require.NotNil(t, latestEpoch)
		assert.Equal(t, "3000", *latestEpoch)
	})
	t.Run("ignores entries of other generation hashes", func(t *testing.T) {
		latestEpoch, err := LatestEpochForGenerationHash(entries, hashB)
		require.NoError(t, err)
		require.NotNil(t, latestEpoch)
		assert.Equal(t, "2000", *latestEpoch)
	})
	t.Run("returns nil when no entry carries the generation hash", func(t *testing.T) {
		latestEpoch, err := LatestEpochForGenerationHash(entries, "hash-absent")
		require.NoError(t, err)
		assert.Nil(t, latestEpoch)
	})
	t.Run("errors on a non-numeric epoch for the queried hash", func(t *testing.T) {
		badEntries := []grovecorev1alpha1.PodGangEntry{testutils.NewPodGangEntryBuilder(hashA, "not-a-number").Build()}
		_, err := LatestEpochForGenerationHash(badEntries, hashA)
		require.Error(t, err)
	})
}

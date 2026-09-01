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

package utils

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
	// A PodGangMap owned by a different PodCliqueSet must not be returned.
	otherPGM := testutils.NewPodGangMapBuilder("other-pcs", namespace, types.UID("other-uid"), 0).Build()

	fakeClient := testutils.CreateDefaultFakeClient([]client.Object{pgm0, pgm1, otherPGM})

	actual, err := ListPodGangMapsForPCS(context.Background(), fakeClient, client.ObjectKey{Namespace: namespace, Name: pcsName})
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

func indicesOf(byIndex map[int]grovecorev1alpha1.PodGangMap) []int {
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
		testutils.NewAnchorEntry(genHash, anchorEpoch, 0, pcsgName, 0),
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

	t.Run("returns the AnchorIndex 0 entry epoch", func(t *testing.T) {
		pgm := testutils.NewPodGangMapBuilder(pcsName, namespace, "uid", 0).WithEntries(
			testutils.NewPodGangEntryBuilder(genHash, anchorEpoch).
				WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).WithAnchorIndex(0).Build(),
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
		testutils.NewAnchorEntry(genHash, anchorEpoch, 0, pcsgName, 0),
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
			testutils.NewAnchorEntry(genHash, anchorEpoch, 0, pcsgName, 0),
		).Build()
		_, err := PodGangNameForPCSGReplica(anchorOnly, pcsRnr, pcsgName, 5)
		require.Error(t, err)
	})
}

func TestPodGangNameForStandalonePCLQ(t *testing.T) {
	rnr := apicommon.ResourceNameReplica{Name: "pcs", Replica: 0}
	pgm := testutils.NewPodGangMapBuilder("pcs", "default", "uid", 0).WithEntries(
		testutils.NewPodGangEntryBuilder("hash", "1000").
			WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
			WithAnchorIndex(0).
			WithPodCliques(map[string]int32{"router": 1}).
			Build(),
		testutils.NewPodGangEntryBuilder("hash", "1001").
			WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
			WithAnchorIndex(1).
			WithPodCliques(map[string]int32{"worker": 2}).
			Build(),
	).Build()

	actual, err := PodGangNameForStandalonePCLQ(pgm, rnr, "hash", "worker")
	require.NoError(t, err)
	assert.Equal(t, apicommon.GenerateAnchorPodGangName(rnr, "1001"), actual)

	_, err = PodGangNameForStandalonePCLQ(pgm, rnr, "hash", "idle")
	require.Error(t, err)

	t.Run("rejects duplicate membership", func(t *testing.T) {
		duplicate := pgm.DeepCopy()
		duplicate.Spec.Entries[0].PodCliques["worker"] = 1
		_, err := PodGangNameForStandalonePCLQ(duplicate, rnr, "hash", "worker")
		require.Error(t, err)
	})

	t.Run("ignores membership from another generation", func(t *testing.T) {
		otherGeneration := pgm.DeepCopy()
		otherGeneration.Spec.Entries[1].PodCliqueSetGenerationHash = "old"
		_, err := PodGangNameForStandalonePCLQ(otherGeneration, rnr, "hash", "worker")
		require.Error(t, err)
	})
}

func TestActivePodCliqueNamesForPodGang(t *testing.T) {
	const (
		pcsName   = "pcs"
		namespace = "default"
		pcsgName  = "sg"
	)
	rnr := apicommon.ResourceNameReplica{Name: pcsName, Replica: 0}
	pcs := testutils.NewPodCliqueSetBuilder(pcsName, namespace, "uid").
		WithStandaloneClique("router").
		WithStandaloneCliqueReplicas("idle", 0).
		WithScalingGroupConfig(pcsgName, []string{"prefill", "decode"}, 2, 1).
		Build()
	pgm := testutils.NewPodGangMapBuilder(pcsName, namespace, "uid", 0).WithEntries(
		testutils.NewPodGangEntryBuilder("hash", "1000").
			WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
			WithAnchorIndex(0).
			WithPodCliques(map[string]int32{"router": 1, "idle": 0}).
			WithPCSGReplicaIndices(map[string][]int32{pcsgName: {0}}).
			Build(),
		testutils.NewPodGangEntryBuilder("hash", "1001").
			WithRole(grovecorev1alpha1.PodGangEntryRoleTail).
			WithPCSGReplicaIndices(map[string][]int32{pcsgName: {1}}).
			Build(),
	).Build()

	t.Run("anchor includes positive standalone and colocated PCSG members", func(t *testing.T) {
		actual, err := ActivePodCliqueNamesForPodGang(pcs, pgm, rnr, apicommon.GenerateAnchorPodGangName(rnr, "1000"))
		require.NoError(t, err)
		assert.Equal(t, NewSet([]string{
			"pcs-0-router",
			"pcs-0-sg-0-prefill",
			"pcs-0-sg-0-decode",
		}), actual)
	})

	t.Run("non-anchor includes only the materialized PCSG replica", func(t *testing.T) {
		actual, err := ActivePodCliqueNamesForPodGang(pcs, pgm, rnr, apicommon.GenerateNonAnchorPodGangName(rnr, "1001", pcsgName, 1))
		require.NoError(t, err)
		assert.Equal(t, NewSet([]string{
			"pcs-0-sg-1-prefill",
			"pcs-0-sg-1-decode",
		}), actual)
	})

	t.Run("unknown PodGang is an error", func(t *testing.T) {
		_, err := ActivePodCliqueNamesForPodGang(pcs, pgm, rnr, "missing")
		require.Error(t, err)
	})

	t.Run("unknown standalone membership is an error", func(t *testing.T) {
		entry := pgm.Spec.Entries[0].DeepCopy()
		entry.PodCliques["unknown"] = 1
		_, err := ActivePodCliqueNamesForEntry(pcs, rnr, entry)
		require.Error(t, err)
	})

	t.Run("unknown scaling group membership is an error", func(t *testing.T) {
		entry := pgm.Spec.Entries[0].DeepCopy()
		entry.PCSGReplicaIndices["unknown"] = []int32{0}
		_, err := ActivePodCliqueNamesForEntry(pcs, rnr, entry)
		require.Error(t, err)
	})
}

func TestFindPodGangEntryForPCSGReplica(t *testing.T) {
	entries := []grovecorev1alpha1.PodGangEntry{
		testutils.NewAnchorEntry("hash", "1000", 0, "sg", 0),
		testutils.NewTailEntry("hash", "1001", "sg", 1),
	}

	entry, err := FindPodGangEntryForPCSGReplica(entries, "hash", "sg", 1)
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, "1001", entry.Epoch)

	duplicate := append(entries, testutils.NewScaleOutEntry("hash", "1002", "sg", 1))
	_, err = FindPodGangEntryForPCSGReplica(duplicate, "hash", "sg", 1)
	require.Error(t, err)
}

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
	"context"
	"strconv"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestSyncBootstrapsPodGangMapForFreshReplica(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
		WithReplicas(1).
		WithScalingGroupConfig("sg", []string{"c"}, 4, 2).
		WithPodCliqueSetGenerationHash(ptr.To("hash1")).
		Build()

	cl := testutils.CreateDefaultFakeClient([]client.Object{pcs})
	operator := New(cl, groveclientscheme.Scheme, clocktesting.NewFakeClock(time.Unix(0, 1000)))

	err := operator.Sync(context.Background(), logr.Discard(), pcs)
	require.NoError(t, err)

	pgm := getPodGangMap(t, cl, "pcs-0")
	assert.Equal(t, int32(0), pgm.Spec.PodCliqueSetReplicaIndex)
	assert.Equal(t, []grovecorev1alpha1.PodGangEntryRole{
		grovecorev1alpha1.PodGangEntryRoleAnchor,
		grovecorev1alpha1.PodGangEntryRoleTail,
		grovecorev1alpha1.PodGangEntryRoleScaleOut,
	}, testutils.RolesOf(pgm.Spec.Entries))

	anchor := testutils.EntryByRole(pgm.Spec.Entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
	assert.Equal(t, []int32{0, 1}, anchor.PCSGReplicaIndices["sg"])
	assert.Nil(t, anchor.DependsOn)

	tail := testutils.EntryByRole(pgm.Spec.Entries, grovecorev1alpha1.PodGangEntryRoleTail)
	assert.Equal(t, []int32{2, 3}, tail.PCSGReplicaIndices["sg"])
	assert.Equal(t, []string{anchor.Epoch}, tail.DependsOn)

	scaleOut := testutils.EntryByRole(pgm.Spec.Entries, grovecorev1alpha1.PodGangEntryRoleScaleOut)
	assert.Empty(t, scaleOut.PCSGReplicaIndices["sg"])
	assert.Equal(t, []string{anchor.Epoch}, scaleOut.DependsOn)
}

func TestSyncReusesEpochFromExistingPodGangsWhenPodGangMapIsMissing(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
		WithReplicas(1).
		WithStandaloneCliqueReplicas("clq-a", 1).
		WithPodCliqueSetGenerationHash(ptr.To("hash1")).
		Build()

	// The PodGangMap for replica 0 is gone, but its anchor PodGang still carries the epoch its pods use.
	anchorPodGang := podGangForReplica(t, "pcs-0-1500", "pcs", 0, "1500", grovecorev1alpha1.PodGangEntryRoleAnchor)

	cl := testutils.CreateDefaultFakeClient([]client.Object{pcs, anchorPodGang})
	// A clock at 9999 would assign a different epoch, so reusing 1500 proves the epoch came from the PodGang.
	operator := New(cl, groveclientscheme.Scheme, clocktesting.NewFakeClock(time.Unix(0, 9999)))

	err := operator.Sync(context.Background(), logr.Discard(), pcs)
	require.NoError(t, err)

	pgm := getPodGangMap(t, cl, "pcs-0")
	anchor := testutils.EntryByRole(pgm.Spec.Entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
	assert.Equal(t, "1500", anchor.Epoch)
}

func TestSyncIgnoresLegacyPodGangsWithoutEpochLabelWhenPodGangMapIsMissing(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
		WithReplicas(1).
		WithStandaloneCliqueReplicas("clq-a", 1).
		WithPodCliqueSetGenerationHash(ptr.To("hash1")).
		Build()

	// A legacy PodGang from before the epoch scheme carries no grove.io/epoch or replica-index label.
	// It must be skipped, not error the sync, so migration can proceed.
	legacyPodGang := testutils.NewPodGangBuilder("pcs-0", "default").
		WithLabels(componentutils.GetPodGangSelectorLabels(metav1.ObjectMeta{Name: "pcs"})).
		Build()

	cl := testutils.CreateDefaultFakeClient([]client.Object{pcs, legacyPodGang})
	operator := New(cl, groveclientscheme.Scheme, clocktesting.NewFakeClock(time.Unix(0, 1000)))

	err := operator.Sync(context.Background(), logr.Discard(), pcs)
	require.NoError(t, err)

	// The PodGangMap is created from the spec, assigning a new epoch since no epoch-scheme PodGang exists.
	pgm := getPodGangMap(t, cl, "pcs-0")
	anchor := testutils.EntryByRole(pgm.Spec.Entries, grovecorev1alpha1.PodGangEntryRoleAnchor)
	assert.Equal(t, "1000", anchor.Epoch)
}

func TestSyncRequeuesWhenPodGangMapHasNoEntries(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
		WithReplicas(1).
		WithStandaloneCliqueReplicas("clq-a", 1).
		WithPodCliqueSetGenerationHash(ptr.To("hash1")).
		Build()

	// A PodGangMap that exists but has no entries is a stale read.
	emptyPGM := testutils.NewPodGangMapBuilder("pcs", "default", types.UID("uid"), 0).Build()

	cl := testutils.CreateDefaultFakeClient([]client.Object{pcs, emptyPGM})
	operator := New(cl, groveclientscheme.Scheme, clocktesting.NewFakeClock(time.Unix(0, 1000)))

	err := operator.Sync(context.Background(), logr.Discard(), pcs)
	testutils.AssertGroveError(t, &groveerr.GroveError{Code: groveerr.ErrCodeRequeueAfter, Operation: "Sync"}, err)
}

func TestSyncDeletesOrphanedPodGangMaps(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
		WithReplicas(1).
		WithStandaloneCliqueReplicas("clq-a", 1).
		WithPodCliqueSetGenerationHash(ptr.To("hash1")).
		Build()

	// A PodGangMap for replica 1 exists but the PCS is now scaled to a single replica.
	orphan := testutils.NewPodGangMapBuilder("pcs", "default", types.UID("uid"), 1).
		WithEntries(testutils.NewPodGangEntryBuilder("hash1", "100").
			WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).Build()).
		Build()

	cl := testutils.CreateDefaultFakeClient([]client.Object{pcs, orphan})
	operator := New(cl, groveclientscheme.Scheme, clocktesting.NewFakeClock(time.Unix(0, 1000)))

	err := operator.Sync(context.Background(), logr.Discard(), pcs)
	require.NoError(t, err)

	assertPodGangMapExists(t, cl, "pcs-0")
	assertPodGangMapAbsent(t, cl, "pcs-1")
}

func TestSyncAdvancesGenerationHashOnlyForReplicaUnderUpdate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").
		WithReplicas(2).
		WithStandaloneCliqueReplicas("clq-a", 1).
		WithPodCliqueSetGenerationHash(ptr.To("new-hash")).
		WithUpdateProgress(&grovecorev1alpha1.PodCliqueSetUpdateProgress{
			CurrentlyUpdating: []grovecorev1alpha1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}},
		}).
		Build()

	// Both replicas have an existing PodGangMap carrying the OLD generation hash.
	existing0 := oldHashPodGangMap("pcs", "default", 0)
	existing1 := oldHashPodGangMap("pcs", "default", 1)

	cl := testutils.CreateDefaultFakeClient([]client.Object{pcs, existing0, existing1})
	operator := New(cl, groveclientscheme.Scheme, clocktesting.NewFakeClock(time.Unix(0, 1000)))

	err := operator.Sync(context.Background(), logr.Discard(), pcs)
	require.NoError(t, err)

	// Replica 0 is under update, so its entries advance to the new hash.
	pgm0 := getPodGangMap(t, cl, "pcs-0")
	assert.Equal(t, "new-hash", pgm0.Spec.Entries[0].PodCliqueSetGenerationHash)

	// Replica 1 is not under update, so its entries keep the old hash.
	pgm1 := getPodGangMap(t, cl, "pcs-1")
	assert.Equal(t, "old-hash", pgm1.Spec.Entries[0].PodCliqueSetGenerationHash)
}

func TestDeleteRemovesAllPodGangMapsOfPCS(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").Build()
	pgm0 := testutils.NewPodGangMapBuilder("pcs", "default", types.UID("uid"), 0).Build()
	pgm1 := testutils.NewPodGangMapBuilder("pcs", "default", types.UID("uid"), 1).Build()

	cl := testutils.CreateDefaultFakeClient([]client.Object{pgm0, pgm1})
	operator := New(cl, groveclientscheme.Scheme, clocktesting.NewFakeClock(time.Unix(0, 1000)))

	err := operator.Delete(context.Background(), logr.Discard(), pcs.ObjectMeta)
	require.NoError(t, err)

	assertPodGangMapAbsent(t, cl, "pcs-0")
	assertPodGangMapAbsent(t, cl, "pcs-1")
}

func TestGetExistingResourceNames(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("pcs", "default", "uid").Build()
	pgm0 := testutils.NewPodGangMapBuilder("pcs", "default", types.UID("uid"), 0).Build()
	otherPGM := testutils.NewPodGangMapBuilder("other-pcs", "default", types.UID("other-uid"), 0).Build()

	cl := testutils.CreateDefaultFakeClient([]client.Object{pgm0, otherPGM})
	operator := New(cl, groveclientscheme.Scheme, clocktesting.NewFakeClock(time.Unix(0, 1000)))

	actual, err := operator.GetExistingResourceNames(context.Background(), logr.Discard(), pcs.ObjectMeta)
	require.NoError(t, err)
	assert.Equal(t, []string{"pcs-0"}, actual)
}

func oldHashPodGangMap(pcsName, namespace string, replicaIndex int) *grovecorev1alpha1.PodGangMap {
	return testutils.NewPodGangMapBuilder(pcsName, namespace, types.UID("uid"), replicaIndex).
		WithEntries(testutils.NewPodGangEntryBuilder("old-hash", "100").
			WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
			WithPodCliques(map[string]int32{"clq-a": 1}).Build()).
		Build()
}

// podGangForReplica builds a PodGang for a PCS replica carrying the labels the PodGangMap component
// selects, groups, and reads epochs on: the PodGang selector labels, the replica index, the epoch, and
// the role.
func podGangForReplica(t *testing.T, name, pcsName string, replicaIndex int, epoch string, role grovecorev1alpha1.PodGangEntryRole) *groveschedulerv1alpha1.PodGang {
	t.Helper()
	labels := lo.Assign(
		componentutils.GetPodGangSelectorLabels(metav1.ObjectMeta{Name: pcsName}),
		map[string]string{
			apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(replicaIndex),
			apicommon.LabelEpoch:                    epoch,
			apicommon.LabelPodGangRole:              string(role),
		},
	)
	return testutils.NewPodGangBuilder(name, "default").WithLabels(labels).Build()
}

func getPodGangMap(t *testing.T, cl client.Client, name string) *grovecorev1alpha1.PodGangMap {
	t.Helper()
	pgm := &grovecorev1alpha1.PodGangMap{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name}, pgm))
	return pgm
}

func assertPodGangMapExists(t *testing.T, cl client.Client, name string) {
	t.Helper()
	err := cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name}, &grovecorev1alpha1.PodGangMap{})
	assert.NoError(t, err)
}

func assertPodGangMapAbsent(t *testing.T, cl client.Client, name string) {
	t.Helper()
	err := cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name}, &grovecorev1alpha1.PodGangMap{})
	assert.True(t, apierrors.IsNotFound(err))
}

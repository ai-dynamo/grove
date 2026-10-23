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

package podcliquesetreplica

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pcssync "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/sync"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestReplicaIndicesFromScalingProgress(t *testing.T) {
	progress := &grovecorev1alpha1.PodCliqueSetScalingProgress{
		ScaleOutReplicaIndices: []int32{1, 3},
		ScaleInReplicaIndices:  []int32{2, 4},
	}

	scaleOutReplicaIndices, scaleInReplicaIndices := replicaIndicesFromScalingProgress(progress)

	assert.Equal(t, []int{1, 3}, scaleOutReplicaIndices)
	assert.Equal(t, []int{2, 4}, scaleInReplicaIndices)

	scaleOutReplicaIndices, scaleInReplicaIndices = replicaIndicesFromScalingProgress(nil)
	assert.Nil(t, scaleOutReplicaIndices)
	assert.Nil(t, scaleInReplicaIndices)
}

func TestScalingProgressFromState(t *testing.T) {
	state := pcssync.State{
		ScaleOutReplicaIndices: []int{1, 3},
		ScaleInReplicaIndices:  []int{2, 4},
	}

	assert.Equal(t, &grovecorev1alpha1.PodCliqueSetScalingProgress{
		ScaleOutReplicaIndices: []int32{1, 3},
		ScaleInReplicaIndices:  []int32{2, 4},
	}, scalingProgressFromState(state))
	assert.Nil(t, scalingProgressFromState(pcssync.State{}))
}

func TestPatchScalingProgress(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-uid")).Build()
	progress := &grovecorev1alpha1.PodCliqueSetScalingProgress{ScaleOutReplicaIndices: []int32{1}}
	clientBuilder := testutils.NewTestClientBuilder().
		WithObjects(pcs).
		WithStatusSubresource(pcs)
	fakeClient := clientBuilder.Build()
	expectations := &pcssync.ScalingProgressExpectations{}
	r := &_resource{client: fakeClient, scalingProgressExpectations: expectations}

	err := r.patchScalingProgress(context.Background(), pcs, progress)

	require.NoError(t, err)
	assert.Equal(t, progress, pcs.Status.ScalingProgress)
	assert.True(t, expectations.ClearIfSatisfied(pcs.ObjectMeta, progress))

	persistedPCS := &grovecorev1alpha1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), persistedPCS))
	assert.Equal(t, progress, persistedPCS.Status.ScalingProgress)
}

func TestPatchScalingProgressClearsProgress(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-uid")).Build()
	pcs.Status.ScalingProgress = &grovecorev1alpha1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{1}}
	fakeClient := testutils.NewTestClientBuilder().
		WithObjects(pcs).
		WithStatusSubresource(pcs).
		Build()
	expectations := &pcssync.ScalingProgressExpectations{}
	r := &_resource{client: fakeClient, scalingProgressExpectations: expectations}

	err := r.patchScalingProgress(context.Background(), pcs, nil)

	require.NoError(t, err)
	assert.Nil(t, pcs.Status.ScalingProgress)
	assert.True(t, expectations.ClearIfSatisfied(pcs.ObjectMeta, nil))
}

func TestPatchScalingProgressRemovesExpectationOnPatchFailure(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-uid")).Build()
	conflict := apierrors.NewConflict(
		schema.GroupResource{Group: "grove.io", Resource: "podcliquesets"},
		pcs.Name,
		errors.New("stale resource version"),
	)
	fakeClient := testutils.NewTestClientBuilder().
		WithObjects(pcs).
		WithStatusSubresource(pcs).
		RecordErrorForObjects(testutils.ClientMethodStatusPatch, conflict, client.ObjectKeyFromObject(pcs)).
		Build()
	expectations := &pcssync.ScalingProgressExpectations{}
	r := &_resource{client: fakeClient, scalingProgressExpectations: expectations}
	progress := &grovecorev1alpha1.PodCliqueSetScalingProgress{ScaleOutReplicaIndices: []int32{1}}

	err := r.patchScalingProgress(context.Background(), pcs, progress)

	require.Error(t, err)
	assert.Nil(t, pcs.Status.ScalingProgress)
	assert.True(t, expectations.ClearIfSatisfied(pcs.ObjectMeta, nil))
}

func TestRecordScalingSelectionEvents(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-uid")).Build()
	eventRecorder := record.NewFakeRecorder(10)
	r := &_resource{eventRecorder: eventRecorder}

	r.recordNewScalingSelectionEvents(
		logr.Discard(),
		pcs,
		[]int{0},
		[]int{5},
		pcssync.State{
			ScaleOutReplicaIndices: []int{0, 2, 4},
			ScaleInReplicaIndices:  []int{1, 3, 5},
		},
	)

	assert.Equal(t, []string{
		"Normal PodCliqueSetReplicaIndicesReservedForScaleOut Reserved PCS replica indices [2 4] for scale-out",
		"Normal PodCliqueSetReplicaIndicesSelectedForScaleIn Selected PCS replica indices [1 3] for scale-in",
	}, []string{<-eventRecorder.Events, <-eventRecorder.Events})
}

func TestProjectedReplicaIndices(t *testing.T) {
	snapshot := replicaResourceSnapshotForIndexTests()
	scaleOutReplicaIndices := []int{2}
	scaleInReplicaIndices := []int{1}

	assert.Equal(t, sets.New(0, 2, 4), snapshot.projectedReplicaIndices(scaleOutReplicaIndices, scaleInReplicaIndices))
	assert.Equal(t, []int{2}, scaleOutReplicaIndices)
	assert.Equal(t, []int{1}, scaleInReplicaIndices)
	assert.Empty(t, (&replicaResourceSnapshot{}).projectedReplicaIndices([]int{3}, []int{3}))
}

func TestReservedReplicaIndices(t *testing.T) {
	snapshot := replicaResourceSnapshotForIndexTests()
	scaleOutReplicaIndices := []int{2}
	scaleInReplicaIndices := []int{1}

	assert.Equal(t, sets.New(0, 1, 2, 4), snapshot.reservedReplicaIndices(scaleOutReplicaIndices, scaleInReplicaIndices))
	assert.Equal(t, []int{2}, scaleOutReplicaIndices)
	assert.Equal(t, []int{1}, scaleInReplicaIndices)
	assert.Equal(t, sets.New(3), (&replicaResourceSnapshot{}).reservedReplicaIndices([]int{3}, []int{3}))
}

func TestComputeReplicaIndices(t *testing.T) {
	newPCS := func(replicas int32) *grovecorev1alpha1.PodCliqueSet {
		pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-pcs-uid")).
			WithReplicas(replicas).
			WithStandaloneClique("worker").
			Build()
		observedGeneration := pcs.Generation
		pcs.Status.ObservedGeneration = &observedGeneration
		return pcs
	}
	newUnobservedPCS := func(replicas int32) *grovecorev1alpha1.PodCliqueSet {
		pcs := newPCS(replicas)
		pcs.Status.ObservedGeneration = nil
		return pcs
	}
	newSnapshot := func(pcs *grovecorev1alpha1.PodCliqueSet, availabilityByIndex map[int]bool) *replicaResourceSnapshot {
		pclqsByReplicaIndex := make(map[int][]grovecorev1alpha1.PodClique, len(availabilityByIndex))
		for replicaIndex, isAvailable := range availabilityByIndex {
			pclqsByReplicaIndex[replicaIndex] = []grovecorev1alpha1.PodClique{
				pclqWithAvailability(pcs, replicaIndex, isAvailable),
			}
		}
		return &replicaResourceSnapshot{standalonePCLQsByReplicaIndex: pclqsByReplicaIndex}
	}

	tests := []struct {
		name                   string
		pcs                    *grovecorev1alpha1.PodCliqueSet
		snapshot               *replicaResourceSnapshot
		scaleOutReplicaIndices []int
		scaleInReplicaIndices  []int
		want                   pcssync.State
	}{
		{
			name:     "initial provisioning uses contiguous indices without scale-out progress",
			pcs:      newUnobservedPCS(3),
			snapshot: &replicaResourceSnapshot{},
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1, 2},
			},
		},
		{
			name:     "scale-out from zero reserves indices",
			pcs:      newPCS(3),
			snapshot: &replicaResourceSnapshot{},
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1, 2},
				ScaleOutReplicaIndices: []int{0, 1, 2},
			},
		},
		{
			name: "headless Service preserves standalone-only replica identity during gang termination",
			pcs:  newPCS(3),
			snapshot: &replicaResourceSnapshot{
				standalonePCLQsByReplicaIndex:     map[int][]grovecorev1alpha1.PodClique{0: {{}}, 2: {{}}},
				headlessServicesByPCSReplicaIndex: map[int][]corev1.Service{0: {{}}, 2: {{}}, 4: {{}}},
			},
			want: pcssync.State{ExpectedReplicaIndices: []int{0, 2, 4}},
		},
		{
			name:     "replica indices already match desired count",
			pcs:      newPCS(2),
			snapshot: newSnapshot(newPCS(2), map[int]bool{0: true, 1: true}),
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1},
			},
		},
		{
			name:                   "materialized scale-out index no longer needs a reservation",
			pcs:                    newPCS(2),
			snapshot:               newSnapshot(newPCS(2), map[int]bool{0: true, 1: false}),
			scaleOutReplicaIndices: []int{1},
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1},
			},
		},
		{
			name:                   "scale-out index without resources remains reserved",
			pcs:                    newPCS(2),
			snapshot:               newSnapshot(newPCS(2), map[int]bool{0: true}),
			scaleOutReplicaIndices: []int{1},
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1},
				ScaleOutReplicaIndices: []int{1},
			},
		},
		{
			name:     "scale-in selects unavailable replica before available replica",
			pcs:      newPCS(1),
			snapshot: newSnapshot(newPCS(1), map[int]bool{0: false, 1: true}),
			want: pcssync.State{
				ExpectedReplicaIndices: []int{1},
				ScaleInReplicaIndices:  []int{0},
			},
		},
		{
			name:     "scale-out fills lowest available index",
			pcs:      newPCS(3),
			snapshot: newSnapshot(newPCS(3), map[int]bool{0: true, 2: true}),
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1, 2},
				ScaleOutReplicaIndices: []int{1},
			},
		},
		{
			name:                  "scale-out does not reuse an index reserved for scale-in",
			pcs:                   newPCS(4),
			snapshot:              newSnapshot(newPCS(4), map[int]bool{0: true, 1: false, 2: true, 4: true}),
			scaleInReplicaIndices: []int{1},
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 2, 3, 4},
				ScaleOutReplicaIndices: []int{3},
				ScaleInReplicaIndices:  []int{1},
			},
		},
		{
			name:     "scale-in removes every current replica when desired count is zero",
			pcs:      newPCS(0),
			snapshot: newSnapshot(newPCS(0), map[int]bool{0: true, 2: false}),
			want: pcssync.State{
				ScaleInReplicaIndices: []int{0, 2},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scaleOutBefore := slices.Clone(test.scaleOutReplicaIndices)
			scaleInBefore := slices.Clone(test.scaleInReplicaIndices)

			got := computeReplicaIndices(
				test.pcs,
				test.snapshot,
				test.scaleOutReplicaIndices,
				test.scaleInReplicaIndices,
			)

			assert.Equal(t, test.want, got)
			assert.Equal(t, scaleOutBefore, test.scaleOutReplicaIndices)
			assert.Equal(t, scaleInBefore, test.scaleInReplicaIndices)
			assert.Len(t, got.ExpectedReplicaIndices, int(test.pcs.Spec.Replicas))
		})
	}
}

func TestSelectReplicaIndicesForScaleIn(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-pcs-uid")).
		WithStandaloneClique("worker").
		Build()
	snapshot := &replicaResourceSnapshot{
		standalonePCLQsByReplicaIndex: map[int][]grovecorev1alpha1.PodClique{
			0: {pclqWithAvailability(pcs, 0, false)},
			2: {pclqWithAvailability(pcs, 2, true)},
			3: {pclqWithAvailability(pcs, 3, true)},
			5: {pclqWithAvailability(pcs, 5, false)},
		},
	}
	projectedReplicaIndices := sets.New(0, 1, 2, 3, 4, 5)
	scaleOutReplicaIndices := sets.New(1, 4)

	tests := []struct {
		name  string
		count int
		want  []int
	}{
		{
			name:  "selects by availability scale-out progress and descending index",
			count: 6,
			want:  []int{5, 0, 4, 1, 3, 2},
		},
		{
			name:  "selects only requested number of indices",
			count: 3,
			want:  []int{5, 0, 4},
		},
		{
			name: "selects no indices for zero count",
		},
		{
			name:  "caps selection at number of desired indices",
			count: 10,
			want:  []int{5, 0, 4, 1, 3, 2},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := selectReplicaIndicesForScaleIn(
				pcs,
				snapshot,
				projectedReplicaIndices,
				scaleOutReplicaIndices,
				test.count,
			)
			assert.Equal(t, test.want, got)
			assert.Equal(t, sets.New(0, 1, 2, 3, 4, 5), projectedReplicaIndices)
			assert.Equal(t, sets.New(1, 4), scaleOutReplicaIndices)
		})
	}
}

func TestSortAndCoalesceReplicaIndices(t *testing.T) {
	got := sortAndCoalesceReplicaIndices(
		[]int{0, 5},
		[]int{1, 4},
		[]int{2, 3},
	)

	assert.Equal(t, []int{5, 0, 4, 1, 3, 2}, got)
}

func TestSortReplicaIndicesDescending(t *testing.T) {
	replicaIndices := []int{1, 4, 2, 3}

	sortReplicaIndicesDescending(replicaIndices)

	assert.Equal(t, []int{4, 3, 2, 1}, replicaIndices)
}

func TestFindMaterializedScaleOutReplicaIndices(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-pcs-uid")).
		WithStandaloneClique("router").
		WithStandaloneClique("metrics").
		WithScalingGroup("workers", []string{"worker"}).
		Build()

	pclq := func(replicaIndex int, cliqueName string) grovecorev1alpha1.PodClique {
		return *testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, cliqueName, pcs.Namespace, int32(replicaIndex)).Build()
	}
	pcsg := func(replicaIndex int, groupName string) grovecorev1alpha1.PodCliqueScalingGroup {
		name := apicommon.GeneratePodCliqueScalingGroupName(
			apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replicaIndex},
			groupName,
		)
		return *testutils.NewPodCliqueScalingGroupBuilder(name, pcs.Namespace, pcs.Name, replicaIndex).Build()
	}

	tests := []struct {
		name                   string
		scaleOutReplicaIndices []int
		pclqsByReplica         map[int][]grovecorev1alpha1.PodClique
		pcsgsByReplica         map[int][]grovecorev1alpha1.PodCliqueScalingGroup
		want                   []int
	}{
		{
			name: "no recorded scale-out indices",
		},
		{
			name:                   "recorded index has no resources",
			scaleOutReplicaIndices: []int{1},
		},
		{
			name:                   "recorded index is missing a standalone PodClique",
			scaleOutReplicaIndices: []int{1},
			pclqsByReplica: map[int][]grovecorev1alpha1.PodClique{
				1: {pclq(1, "router")},
			},
			pcsgsByReplica: map[int][]grovecorev1alpha1.PodCliqueScalingGroup{
				1: {pcsg(1, "workers")},
			},
		},
		{
			name:                   "recorded index is missing a PodCliqueScalingGroup",
			scaleOutReplicaIndices: []int{1},
			pclqsByReplica: map[int][]grovecorev1alpha1.PodClique{
				1: {pclq(1, "router"), pclq(1, "metrics")},
			},
		},
		{
			name:                   "recorded index has every expected resource",
			scaleOutReplicaIndices: []int{1},
			pclqsByReplica: map[int][]grovecorev1alpha1.PodClique{
				1: {pclq(1, "router"), pclq(1, "metrics")},
			},
			pcsgsByReplica: map[int][]grovecorev1alpha1.PodCliqueScalingGroup{
				1: {pcsg(1, "workers")},
			},
			want: []int{1},
		},
		{
			name:                   "unexpected resources do not replace missing expected resources",
			scaleOutReplicaIndices: []int{1},
			pclqsByReplica: map[int][]grovecorev1alpha1.PodClique{
				1: {pclq(1, "router"), pclq(1, "unexpected")},
			},
			pcsgsByReplica: map[int][]grovecorev1alpha1.PodCliqueScalingGroup{
				1: {pcsg(1, "workers")},
			},
		},
		{
			name:                   "indices materialize independently and return in ascending order",
			scaleOutReplicaIndices: []int{3, 1, 2},
			pclqsByReplica: map[int][]grovecorev1alpha1.PodClique{
				1: {pclq(1, "router"), pclq(1, "metrics")},
				2: {pclq(2, "router")},
				3: {pclq(3, "router"), pclq(3, "metrics")},
			},
			pcsgsByReplica: map[int][]grovecorev1alpha1.PodCliqueScalingGroup{
				1: {pcsg(1, "workers")},
				2: {pcsg(2, "workers")},
				3: {pcsg(3, "workers")},
			},
			want: []int{1, 3},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := findMaterializedScaleOutReplicaIndices(
				pcs,
				test.scaleOutReplicaIndices,
				&replicaResourceSnapshot{
					standalonePCLQsByReplicaIndex: test.pclqsByReplica,
					pcsgsByReplicaIndex:           test.pcsgsByReplica,
				},
			)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestTakeReplicaResourceSnapshot(t *testing.T) {
	const (
		pcsName      = "test-pcs"
		otherPCSName = "other-pcs"
		namespace    = "default"
	)
	pcsUID := types.UID("test-pcs-uid")
	pcs := testutils.NewPodCliqueSetBuilder(pcsName, namespace, pcsUID).Build()

	standalonePCLQ0 := testutils.NewPodCliqueBuilder(pcsName, pcsUID, "standalone-a", namespace, 0).Build()
	standalonePCLQ2 := testutils.NewPodCliqueBuilder(pcsName, pcsUID, "standalone-b", namespace, 2).Build()
	pcsg0 := testutils.NewPodCliqueScalingGroupBuilder("test-pcs-0-group", namespace, pcsName, 0).
		WithOwnerReference(apiconstants.KindPodCliqueSet, pcsName, pcsUID).
		Build()
	pcsg2 := testutils.NewPodCliqueScalingGroupBuilder("test-pcs-2-group", namespace, pcsName, 2).
		WithOwnerReference(apiconstants.KindPodCliqueSet, pcsName, pcsUID).
		Build()

	pcsgOwnedPCLQ := testutils.NewPCSGPodCliqueBuilder("test-pcs-0-group-0-member", namespace, pcsName, pcsg0.Name, 0, 0).Build()
	otherPCSUID := types.UID("other-pcs-uid")
	otherPCSPCLQ := testutils.NewPodCliqueBuilder(pcsName, otherPCSUID, "stale", namespace, 4).Build()
	otherPCSG := testutils.NewPodCliqueScalingGroupBuilder("test-pcs-4-group", namespace, pcsName, 4).
		WithOwnerReference(apiconstants.KindPodCliqueSet, otherPCSName, otherPCSUID).
		Build()
	headlessService2 := headlessServiceForPCSReplica(t, pcs, 2)

	r := _resource{client: newScalingTestClient(t,
		standalonePCLQ0,
		standalonePCLQ2,
		pcsg0,
		pcsg2,
		pcsgOwnedPCLQ,
		otherPCSPCLQ,
		otherPCSG,
		headlessService2,
	)}

	snapshot, err := r.takeReplicaResourceSnapshot(context.Background(), pcs)
	require.NoError(t, err)

	require.Equal(t, []int{0, 2}, sortedMapKeys(snapshot.standalonePCLQsByReplicaIndex))
	assert.Equal(t, standalonePCLQ0.Name, snapshot.standalonePCLQsByReplicaIndex[0][0].Name)
	assert.Equal(t, standalonePCLQ2.Name, snapshot.standalonePCLQsByReplicaIndex[2][0].Name)
	require.Equal(t, []int{0, 2}, sortedMapKeys(snapshot.pcsgsByReplicaIndex))
	assert.Equal(t, pcsg0.Name, snapshot.pcsgsByReplicaIndex[0][0].Name)
	assert.Equal(t, pcsg2.Name, snapshot.pcsgsByReplicaIndex[2][0].Name)
	require.Equal(t, []int{2}, sortedMapKeys(snapshot.headlessServicesByPCSReplicaIndex))
	assert.Equal(t, headlessService2.Name, snapshot.headlessServicesByPCSReplicaIndex[2][0].Name)
}

func TestTakeReplicaResourceSnapshotReturnsEmptyMaps(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-pcs-uid")).Build()
	r := _resource{client: newScalingTestClient(t)}

	snapshot, err := r.takeReplicaResourceSnapshot(context.Background(), pcs)
	require.NoError(t, err)
	assert.Empty(t, snapshot.standalonePCLQsByReplicaIndex)
	assert.Empty(t, snapshot.pcsgsByReplicaIndex)
	assert.Empty(t, snapshot.headlessServicesByPCSReplicaIndex)
}

func TestTakeReplicaResourceSnapshotRejectsInvalidPCLQReplicaIndex(t *testing.T) {
	pcsUID := types.UID("test-pcs-uid")
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", pcsUID).Build()
	pclq := testutils.NewPodCliqueBuilder(pcs.Name, pcsUID, "standalone", pcs.Namespace, 0).
		WithLabels(map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "invalid"}).
		Build()
	r := _resource{client: newScalingTestClient(t, pclq)}

	_, err := r.takeReplicaResourceSnapshot(context.Background(), pcs)
	assertGroveErrorCode(t, err, errCodeListPCLQs)
}

func TestTakeReplicaResourceSnapshotRejectsInvalidPCSGReplicaIndex(t *testing.T) {
	pcsUID := types.UID("test-pcs-uid")
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", pcsUID).Build()
	pcsg := testutils.NewPodCliqueScalingGroupBuilder("test-pcs-group", pcs.Namespace, pcs.Name, 0).
		WithLabels(map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "invalid"}).
		WithOwnerReference(apiconstants.KindPodCliqueSet, pcs.Name, pcsUID).
		Build()
	r := _resource{client: newScalingTestClient(t, pcsg)}

	_, err := r.takeReplicaResourceSnapshot(context.Background(), pcs)
	assertGroveErrorCode(t, err, errCodeListPCSGs)
}

func TestTakeReplicaResourceSnapshotWrapsListErrors(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-pcs-uid")).Build()
	tests := []struct {
		name      string
		failedGVK runtime.Object
		wantCode  grovecorev1alpha1.ErrorCode
	}{
		{
			name:      "PodClique list",
			failedGVK: &grovecorev1alpha1.PodCliqueList{},
			wantCode:  errCodeListPCLQs,
		},
		{
			name:      "PodCliqueScalingGroup list",
			failedGVK: &grovecorev1alpha1.PodCliqueScalingGroupList{},
			wantCode:  errCodeListPCSGs,
		},
		{
			name:      "Service list",
			failedGVK: &corev1.ServiceList{},
			wantCode:  errCodeListServices,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := _resource{
				client: &failingListClient{
					Client:       newScalingTestClient(t),
					failedObject: test.failedGVK,
				},
			}

			_, err := r.takeReplicaResourceSnapshot(context.Background(), pcs)
			assertGroveErrorCode(t, err, test.wantCode)
		})
	}
}

func headlessServiceForPCSReplica(t *testing.T, pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int) *corev1.Service {
	t.Helper()
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", pcs.Name, pcsReplicaIndex),
			Namespace: pcs.Namespace,
			Labels: map[string]string{
				apicommon.LabelManagedByKey:             apicommon.LabelManagedByValue,
				apicommon.LabelPartOfKey:                pcs.Name,
				apicommon.LabelComponentKey:             apicommon.LabelComponentNamePodCliqueSetReplicaHeadlessService,
				apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(pcsReplicaIndex),
			},
		},
	}
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, controllerutil.SetControllerReference(pcs, service, scheme))
	return service
}

func pclqWithAvailability(pcs *grovecorev1alpha1.PodCliqueSet, replicaIndex int, isAvailable bool) grovecorev1alpha1.PodClique {
	pclq := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, "worker", pcs.Namespace, int32(replicaIndex)).Build()
	if isAvailable {
		pclq.Status.ReadyReplicas = *pclq.Spec.MinAvailable
	}
	return *pclq
}

func replicaResourceSnapshotForIndexTests() *replicaResourceSnapshot {
	return &replicaResourceSnapshot{
		standalonePCLQsByReplicaIndex: map[int][]grovecorev1alpha1.PodClique{
			0: {{}},
			1: {{}},
		},
		pcsgsByReplicaIndex: map[int][]grovecorev1alpha1.PodCliqueScalingGroup{
			1: {{}},
			4: {{}},
		},
	}
}

type failingListClient struct {
	client.Client
	failedObject runtime.Object
}

func (c *failingListClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if runtimeObjectTypesEqual(list, c.failedObject) {
		return errors.New("list failed")
	}
	return c.Client.List(ctx, list, opts...)
}

func newScalingTestClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func sortedMapKeys[V any](values map[int]V) []int {
	keys := make([]int, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func runtimeObjectTypesEqual(a, b runtime.Object) bool {
	return reflect.TypeOf(a) == reflect.TypeOf(b)
}

func assertGroveErrorCode(t *testing.T, err error, wantCode grovecorev1alpha1.ErrorCode) {
	t.Helper()
	require.Error(t, err)
	var groveErr *groveerr.GroveError
	require.True(t, errors.As(err, &groveErr))
	assert.Equal(t, wantCode, groveErr.Code)
	assert.Equal(t, component.OperationSync, groveErr.Operation)
}

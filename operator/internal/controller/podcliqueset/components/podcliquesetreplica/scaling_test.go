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
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
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

func TestReplicaScalingIndicesFromProgress(t *testing.T) {
	progress := &grovecorev1beta1.PodCliqueSetScalingProgress{
		ScaleOutReplicaIndices: []int32{1, 3},
		ScaleInReplicaIndices:  []int32{2, 4},
	}

	scalingIndices := replicaScalingIndicesFromProgress(progress)

	assert.Equal(t, pcsReplicaScalingIndices{scaleOut: []int{1, 3}, scaleIn: []int{2, 4}}, scalingIndices)
	assert.Equal(t, pcsReplicaScalingIndices{}, replicaScalingIndicesFromProgress(nil))
}

func TestScalingProgressFromState(t *testing.T) {
	state := pcssync.State{
		ScaleOutReplicaIndices: []int{1, 3},
		ScaleInReplicaIndices:  []int{2, 4},
	}

	assert.Equal(t, &grovecorev1beta1.PodCliqueSetScalingProgress{
		ScaleOutReplicaIndices: []int32{1, 3},
		ScaleInReplicaIndices:  []int32{2, 4},
	}, scalingProgressFromState(state))
	assert.Nil(t, scalingProgressFromState(pcssync.State{}))
}

func TestPatchScalingProgress(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-uid")).Build()
	progress := &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleOutReplicaIndices: []int32{1}}
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

	persistedPCS := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pcs), persistedPCS))
	assert.Equal(t, progress, persistedPCS.Status.ScalingProgress)
}

func TestPatchScalingProgressClearsProgress(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-uid")).Build()
	pcs.Status.ScalingProgress = &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{1}}
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
	progress := &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleOutReplicaIndices: []int32{1}}

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
	replicaInfos := replicaInfoByIndexForIndexTests()
	scaleOutReplicaIndices := []int{2}
	scaleInReplicaIndices := []int{1}

	assert.Equal(t, sets.New(0, 2, 4), replicaInfos.projectedReplicaIndices(pcsReplicaScalingIndices{scaleOut: scaleOutReplicaIndices, scaleIn: scaleInReplicaIndices}))
	assert.Equal(t, []int{2}, scaleOutReplicaIndices)
	assert.Equal(t, []int{1}, scaleInReplicaIndices)
	assert.Empty(t, pcsReplicaInfoByIndex{}.projectedReplicaIndices(pcsReplicaScalingIndices{scaleOut: []int{3}, scaleIn: []int{3}}))
}

func TestReservedReplicaIndices(t *testing.T) {
	replicaInfos := replicaInfoByIndexForIndexTests()
	scaleOutReplicaIndices := []int{2}
	scaleInReplicaIndices := []int{1}

	assert.Equal(t, sets.New(0, 1, 2, 4), replicaInfos.reservedReplicaIndices(pcsReplicaScalingIndices{scaleOut: scaleOutReplicaIndices, scaleIn: scaleInReplicaIndices}))
	assert.Equal(t, []int{2}, scaleOutReplicaIndices)
	assert.Equal(t, []int{1}, scaleInReplicaIndices)
	assert.Equal(t, sets.New(3), pcsReplicaInfoByIndex{}.reservedReplicaIndices(pcsReplicaScalingIndices{scaleOut: []int{3}, scaleIn: []int{3}}))
}

func TestComputeReplicaIndices(t *testing.T) {
	newPCS := func(replicas int32) *grovecorev1beta1.PodCliqueSet {
		pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-pcs-uid")).
			WithReplicas(replicas).
			WithStandaloneClique("worker").
			Build()
		observedGeneration := pcs.Generation
		pcs.Status.ObservedGeneration = &observedGeneration
		return pcs
	}
	newUnobservedPCS := func(replicas int32) *grovecorev1beta1.PodCliqueSet {
		pcs := newPCS(replicas)
		pcs.Status.ObservedGeneration = nil
		return pcs
	}
	newReplicaInfos := func(pcs *grovecorev1beta1.PodCliqueSet, availabilityByIndex map[int]bool) pcsReplicaInfoByIndex {
		replicaInfos := make(pcsReplicaInfoByIndex, len(availabilityByIndex))
		for replicaIndex, isAvailable := range availabilityByIndex {
			replicaInfos[replicaIndex] = pcsReplicaInfo{
				index:           replicaIndex,
				standalonePCLQs: []grovecorev1beta1.PodClique{pclqWithAvailability(pcs, replicaIndex, isAvailable)},
			}
		}
		return replicaInfos
	}

	tests := []struct {
		name                   string
		pcs                    *grovecorev1beta1.PodCliqueSet
		replicaInfos           pcsReplicaInfoByIndex
		scaleOutReplicaIndices []int
		scaleInReplicaIndices  []int
		want                   pcssync.State
	}{
		{
			name:         "initial provisioning uses contiguous indices without scale-out progress",
			pcs:          newUnobservedPCS(3),
			replicaInfos: pcsReplicaInfoByIndex{},
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1, 2},
			},
		},
		{
			name:         "scale-out from zero reserves indices",
			pcs:          newPCS(3),
			replicaInfos: pcsReplicaInfoByIndex{},
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1, 2},
				ScaleOutReplicaIndices: []int{0, 1, 2},
			},
		},
		{
			name: "headless Service preserves standalone-only replica identity during gang termination",
			pcs:  newPCS(3),
			replicaInfos: pcsReplicaInfoByIndex{
				0: {index: 0, standalonePCLQs: []grovecorev1beta1.PodClique{{}}, headlessSvcs: []corev1.Service{{}}},
				2: {index: 2, standalonePCLQs: []grovecorev1beta1.PodClique{{}}, headlessSvcs: []corev1.Service{{}}},
				4: {index: 4, headlessSvcs: []corev1.Service{{}}},
			},
			want: pcssync.State{ExpectedReplicaIndices: []int{0, 2, 4}},
		},
		{
			name:         "replica indices already match desired count",
			pcs:          newPCS(2),
			replicaInfos: newReplicaInfos(newPCS(2), map[int]bool{0: true, 1: true}),
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1},
			},
		},
		{
			name:                   "scale-out index with expected PCLQs and PCSGs no longer needs a reservation",
			pcs:                    newPCS(2),
			replicaInfos:           newReplicaInfos(newPCS(2), map[int]bool{0: true, 1: false}),
			scaleOutReplicaIndices: []int{1},
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1},
			},
		},
		{
			name:                   "scale-out index without resources remains reserved",
			pcs:                    newPCS(2),
			replicaInfos:           newReplicaInfos(newPCS(2), map[int]bool{0: true}),
			scaleOutReplicaIndices: []int{1},
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1},
				ScaleOutReplicaIndices: []int{1},
			},
		},
		{
			name:         "scale-in selects unavailable replica before available replica",
			pcs:          newPCS(1),
			replicaInfos: newReplicaInfos(newPCS(1), map[int]bool{0: false, 1: true}),
			want: pcssync.State{
				ExpectedReplicaIndices: []int{1},
				ScaleInReplicaIndices:  []int{0},
			},
		},
		{
			name:         "scale-out fills lowest available index",
			pcs:          newPCS(3),
			replicaInfos: newReplicaInfos(newPCS(3), map[int]bool{0: true, 2: true}),
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 1, 2},
				ScaleOutReplicaIndices: []int{1},
			},
		},
		{
			name:                  "scale-out does not reuse an index reserved for scale-in",
			pcs:                   newPCS(4),
			replicaInfos:          newReplicaInfos(newPCS(4), map[int]bool{0: true, 1: false, 2: true, 4: true}),
			scaleInReplicaIndices: []int{1},
			want: pcssync.State{
				ExpectedReplicaIndices: []int{0, 2, 3, 4},
				ScaleOutReplicaIndices: []int{3},
				ScaleInReplicaIndices:  []int{1},
			},
		},
		{
			name:         "scale-in removes every current replica when desired count is zero",
			pcs:          newPCS(0),
			replicaInfos: newReplicaInfos(newPCS(0), map[int]bool{0: true, 2: false}),
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
				test.replicaInfos,
				pcsReplicaScalingIndices{scaleOut: test.scaleOutReplicaIndices, scaleIn: test.scaleInReplicaIndices},
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
	insufficientAvailablePCLQ := func(replicaIndex int) grovecorev1beta1.PodClique {
		pclq := pclqWithAvailability(pcs, replicaIndex, false)
		pclq.Status.ScheduledReplicas = *pclq.Spec.MinAvailable
		return pclq
	}
	replicaInfos := pcsReplicaInfoByIndex{
		0: {index: 0, standalonePCLQs: []grovecorev1beta1.PodClique{pclqWithAvailability(pcs, 0, false)}},
		2: {index: 2, standalonePCLQs: []grovecorev1beta1.PodClique{pclqWithAvailability(pcs, 2, true)}},
		3: {index: 3, standalonePCLQs: []grovecorev1beta1.PodClique{insufficientAvailablePCLQ(3)}},
		5: {index: 5, standalonePCLQs: []grovecorev1beta1.PodClique{pclqWithAvailability(pcs, 5, false)}},
		6: {index: 6, standalonePCLQs: []grovecorev1beta1.PodClique{insufficientAvailablePCLQ(6)}},
		7: {index: 7, standalonePCLQs: []grovecorev1beta1.PodClique{pclqWithAvailability(pcs, 7, true)}},
	}
	projectedReplicaIndices := sets.New(0, 1, 2, 3, 4, 5, 6, 7)
	scaleOutReplicaIndices := sets.New(1, 4)

	tests := []struct {
		name  string
		count int
		want  []int
	}{
		{
			name:  "selects by scheduled capacity availability scale-out progress and descending index",
			count: 8,
			want:  []int{5, 0, 6, 3, 4, 1, 7, 2},
		},
		{
			name:  "selects only requested number of indices",
			count: 3,
			want:  []int{5, 0, 6},
		},
		{
			name: "selects no indices for zero count",
		},
		{
			name:  "caps selection at number of desired indices",
			count: 10,
			want:  []int{5, 0, 6, 3, 4, 1, 7, 2},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := selectReplicaIndicesForScaleIn(
				pcs,
				replicaInfos,
				projectedReplicaIndices,
				scaleOutReplicaIndices,
				test.count,
			)
			assert.Equal(t, test.want, got)
			assert.Equal(t, sets.New(0, 1, 2, 3, 4, 5, 6, 7), projectedReplicaIndices)
			assert.Equal(t, sets.New(1, 4), scaleOutReplicaIndices)
		})
	}

	t.Run("preserves assigned replica that is warming over unassigned replica", func(t *testing.T) {
		warmingPCLQ := pclqWithAvailability(pcs, 4, false)
		warmingPCLQ.Status.ScheduledReplicas = *warmingPCLQ.Spec.MinAvailable
		replicaInfos := pcsReplicaInfoByIndex{
			1: {index: 1, standalonePCLQs: []grovecorev1beta1.PodClique{pclqWithAvailability(pcs, 1, false)}},
			4: {index: 4, standalonePCLQs: []grovecorev1beta1.PodClique{warmingPCLQ}},
		}

		got := selectReplicaIndicesForScaleIn(pcs, replicaInfos, sets.New(1, 4), sets.New[int](), 1)

		assert.Equal(t, []int{1}, got)
	})
}

func TestSortAndCoalesceReplicaIndices(t *testing.T) {
	got := sortAndCoalesceReplicaIndices(
		[]int{0, 7},
		[]int{1, 6},
		[]int{2, 5},
		[]int{3, 4},
	)

	assert.Equal(t, []int{7, 0, 6, 1, 5, 2, 4, 3}, got)
}

func TestSortReplicaIndicesDescending(t *testing.T) {
	replicaIndices := []int{1, 4, 2, 3}

	sortReplicaIndicesDescending(replicaIndices)

	assert.Equal(t, []int{4, 3, 2, 1}, replicaIndices)
}

func TestFindScaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-pcs-uid")).
		WithStandaloneClique("router").
		WithStandaloneClique("metrics").
		WithScalingGroup("workers", []string{"worker"}).
		Build()

	pclq := func(replicaIndex int, cliqueName string) grovecorev1beta1.PodClique {
		return *testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, cliqueName, pcs.Namespace, int32(replicaIndex)).Build()
	}
	pcsg := func(replicaIndex int, groupName string) grovecorev1beta1.PodCliqueScalingGroup {
		name := apicommon.GeneratePodCliqueScalingGroupName(
			apicommon.ResourceNameReplica{Name: pcs.Name, Replica: replicaIndex},
			groupName,
		)
		return *testutils.NewPodCliqueScalingGroupBuilder(name, pcs.Namespace, pcs.Name, replicaIndex).Build()
	}

	tests := []struct {
		name                   string
		scaleOutReplicaIndices []int
		pclqsByReplica         map[int][]grovecorev1beta1.PodClique
		pcsgsByReplica         map[int][]grovecorev1beta1.PodCliqueScalingGroup
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
			pclqsByReplica: map[int][]grovecorev1beta1.PodClique{
				1: {pclq(1, "router")},
			},
			pcsgsByReplica: map[int][]grovecorev1beta1.PodCliqueScalingGroup{
				1: {pcsg(1, "workers")},
			},
		},
		{
			name:                   "recorded index is missing a PodCliqueScalingGroup",
			scaleOutReplicaIndices: []int{1},
			pclqsByReplica: map[int][]grovecorev1beta1.PodClique{
				1: {pclq(1, "router"), pclq(1, "metrics")},
			},
		},
		{
			name:                   "recorded index has every expected resource",
			scaleOutReplicaIndices: []int{1},
			pclqsByReplica: map[int][]grovecorev1beta1.PodClique{
				1: {pclq(1, "router"), pclq(1, "metrics")},
			},
			pcsgsByReplica: map[int][]grovecorev1beta1.PodCliqueScalingGroup{
				1: {pcsg(1, "workers")},
			},
			want: []int{1},
		},
		{
			name:                   "unexpected resources do not replace missing expected resources",
			scaleOutReplicaIndices: []int{1},
			pclqsByReplica: map[int][]grovecorev1beta1.PodClique{
				1: {pclq(1, "router"), pclq(1, "unexpected")},
			},
			pcsgsByReplica: map[int][]grovecorev1beta1.PodCliqueScalingGroup{
				1: {pcsg(1, "workers")},
			},
		},
		{
			name:                   "indices materialize independently and return in ascending order",
			scaleOutReplicaIndices: []int{3, 1, 2},
			pclqsByReplica: map[int][]grovecorev1beta1.PodClique{
				1: {pclq(1, "router"), pclq(1, "metrics")},
				2: {pclq(2, "router")},
				3: {pclq(3, "router"), pclq(3, "metrics")},
			},
			pcsgsByReplica: map[int][]grovecorev1beta1.PodCliqueScalingGroup{
				1: {pcsg(1, "workers")},
				2: {pcsg(2, "workers")},
				3: {pcsg(3, "workers")},
			},
			want: []int{1, 3},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := findScaleOutReplicaIndicesWithExpectedPCLQsAndPCSGs(
				pcs,
				test.scaleOutReplicaIndices,
				replicaInfosFromResources(test.pclqsByReplica, test.pcsgsByReplica),
			)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestGatherPCSReplicaInfosByIndex(t *testing.T) {
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

	replicaInfos, err := r.gatherPCSReplicaInfosByIndex(context.Background(), pcs)
	require.NoError(t, err)

	require.Equal(t, []int{0, 2}, sortedMapKeys(replicaInfos))
	assert.Equal(t, standalonePCLQ0.Name, replicaInfos[0].standalonePCLQs[0].Name)
	assert.Equal(t, standalonePCLQ2.Name, replicaInfos[2].standalonePCLQs[0].Name)
	assert.Equal(t, pcsg0.Name, replicaInfos[0].pcsgs[0].Name)
	assert.Equal(t, pcsg2.Name, replicaInfos[2].pcsgs[0].Name)
	assert.Equal(t, headlessService2.Name, replicaInfos[2].headlessSvcs[0].Name)
}

func TestGatherPCSReplicaInfosByIndexIncludesPodGangMapEntriesForCoherentUpdate(t *testing.T) {
	pcs := coherentTestPCS([]string{"frontend"}, nil)
	podGangMapEntries := []grovecorev1beta1.PodGangEntry{{
		Epoch:                      "200",
		PodCliqueSetGenerationHash: coherentTestCurrentGen,
	}}
	podGangMap := testutils.NewPodGangMapBuilder(pcs.Name, pcs.Namespace, pcs.UID, 1).
		WithEntries(podGangMapEntries...).
		Build()
	r := _resource{client: newScalingTestClient(t, podGangMap)}

	pcsReplicaInfosByIndex, err := r.gatherPCSReplicaInfosByIndex(context.Background(), pcs)

	require.NoError(t, err)
	require.Contains(t, pcsReplicaInfosByIndex, 1)
	assert.Equal(t, podGangMapEntries, pcsReplicaInfosByIndex[1].pgmEntries)
}

func TestGatherPCSReplicaInfosByIndexIgnoresPodGangMapsOutsideCoherentUpdate(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-pcs-uid")).Build()
	podGangMap := testutils.NewPodGangMapBuilder(pcs.Name, pcs.Namespace, pcs.UID, 1).
		WithEntries(grovecorev1beta1.PodGangEntry{Epoch: "200"}).
		Build()
	r := _resource{client: newScalingTestClient(t, podGangMap)}

	pcsReplicaInfosByIndex, err := r.gatherPCSReplicaInfosByIndex(context.Background(), pcs)

	require.NoError(t, err)
	assert.Empty(t, pcsReplicaInfosByIndex)
}

func TestGatherPCSReplicaInfosByIndexReturnsEmptyMaps(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-pcs-uid")).Build()
	r := _resource{client: newScalingTestClient(t)}

	replicaInfos, err := r.gatherPCSReplicaInfosByIndex(context.Background(), pcs)
	require.NoError(t, err)
	assert.Empty(t, replicaInfos)
}

func TestGatherPCSReplicaInfosByIndexRejectsInvalidPCLQReplicaIndex(t *testing.T) {
	pcsUID := types.UID("test-pcs-uid")
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", pcsUID).Build()
	pclq := testutils.NewPodCliqueBuilder(pcs.Name, pcsUID, "standalone", pcs.Namespace, 0).
		WithLabels(map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "invalid"}).
		Build()
	r := _resource{client: newScalingTestClient(t, pclq)}

	_, err := r.gatherPCSReplicaInfosByIndex(context.Background(), pcs)
	assertGroveErrorCode(t, err, errCodeListPCLQs)
}

func TestGatherPCSReplicaInfosByIndexRejectsInvalidPCSGReplicaIndex(t *testing.T) {
	pcsUID := types.UID("test-pcs-uid")
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", pcsUID).Build()
	pcsg := testutils.NewPodCliqueScalingGroupBuilder("test-pcs-group", pcs.Namespace, pcs.Name, 0).
		WithLabels(map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "invalid"}).
		WithOwnerReference(apiconstants.KindPodCliqueSet, pcs.Name, pcsUID).
		Build()
	r := _resource{client: newScalingTestClient(t, pcsg)}

	_, err := r.gatherPCSReplicaInfosByIndex(context.Background(), pcs)
	assertGroveErrorCode(t, err, errCodeListPCSGs)
}

func TestGatherPCSReplicaInfosByIndexRejectsInvalidPodGangMapReplicaIndex(t *testing.T) {
	pcs := coherentTestPCS([]string{"frontend"}, nil)
	podGangMap := testutils.NewPodGangMapBuilder(pcs.Name, pcs.Namespace, pcs.UID, 0).Build()
	podGangMap.Labels[apicommon.LabelPodCliqueSetReplicaIndex] = "invalid"
	r := _resource{client: newScalingTestClient(t, podGangMap)}

	_, err := r.gatherPCSReplicaInfosByIndex(context.Background(), pcs)

	assertGroveErrorCode(t, err, errCodeGetPodGangMap)
}

func TestGatherPCSReplicaInfosByIndexWrapsPodGangMapListError(t *testing.T) {
	pcs := coherentTestPCS([]string{"frontend"}, nil)
	r := _resource{
		client: &failingListClient{
			Client:       newScalingTestClient(t),
			failedObject: &grovecorev1beta1.PodGangMapList{},
		},
	}

	_, err := r.gatherPCSReplicaInfosByIndex(context.Background(), pcs)

	assertGroveErrorCode(t, err, errCodeGetPodGangMap)
}

func TestGatherPCSReplicaInfosByIndexWrapsListErrors(t *testing.T) {
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", types.UID("test-pcs-uid")).Build()
	tests := []struct {
		name      string
		failedGVK runtime.Object
		wantCode  grovecorev1beta1.ErrorCode
	}{
		{
			name:      "PodClique list",
			failedGVK: &grovecorev1beta1.PodCliqueList{},
			wantCode:  errCodeListPCLQs,
		},
		{
			name:      "PodCliqueScalingGroup list",
			failedGVK: &grovecorev1beta1.PodCliqueScalingGroupList{},
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

			_, err := r.gatherPCSReplicaInfosByIndex(context.Background(), pcs)
			assertGroveErrorCode(t, err, test.wantCode)
		})
	}
}

func headlessServiceForPCSReplica(t *testing.T, pcs *grovecorev1beta1.PodCliqueSet, pcsReplicaIndex int) *corev1.Service {
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
	require.NoError(t, grovecorev1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, controllerutil.SetControllerReference(pcs, service, scheme))
	return service
}

func pclqWithAvailability(pcs *grovecorev1beta1.PodCliqueSet, replicaIndex int, isAvailable bool) grovecorev1beta1.PodClique {
	pclq := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, "worker", pcs.Namespace, int32(replicaIndex)).Build()
	if isAvailable {
		pclq.Status.ReadyReplicas = *pclq.Spec.MinAvailable
	}
	return *pclq
}

func replicaInfoByIndexForIndexTests() pcsReplicaInfoByIndex {
	return pcsReplicaInfoByIndex{
		0: {index: 0, standalonePCLQs: []grovecorev1beta1.PodClique{{}}},
		1: {index: 1, standalonePCLQs: []grovecorev1beta1.PodClique{{}}, pcsgs: []grovecorev1beta1.PodCliqueScalingGroup{{}}},
		4: {index: 4, pcsgs: []grovecorev1beta1.PodCliqueScalingGroup{{}}},
	}
}

func replicaInfosFromResources(
	standalonePCLQsByReplicaIndex map[int][]grovecorev1beta1.PodClique,
	pcsgsByReplicaIndex map[int][]grovecorev1beta1.PodCliqueScalingGroup,
) pcsReplicaInfoByIndex {
	replicaIndices := sets.KeySet(standalonePCLQsByReplicaIndex)
	replicaIndices.Insert(sets.List(sets.KeySet(pcsgsByReplicaIndex))...)
	replicaInfos := make(pcsReplicaInfoByIndex, replicaIndices.Len())
	for replicaIndex := range replicaIndices {
		replicaInfos[replicaIndex] = pcsReplicaInfo{
			index:           replicaIndex,
			standalonePCLQs: standalonePCLQsByReplicaIndex[replicaIndex],
			pcsgs:           pcsgsByReplicaIndex[replicaIndex],
		}
	}
	return replicaInfos
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
	require.NoError(t, grovecorev1beta1.AddToScheme(scheme))
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

func assertGroveErrorCode(t *testing.T, err error, wantCode grovecorev1beta1.ErrorCode) {
	t.Helper()
	require.Error(t, err)
	var groveErr *groveerr.GroveError
	require.True(t, errors.As(err, &groveErr))
	assert.Equal(t, wantCode, groveErr.Code)
	assert.Equal(t, component.OperationSync, groveErr.Operation)
}

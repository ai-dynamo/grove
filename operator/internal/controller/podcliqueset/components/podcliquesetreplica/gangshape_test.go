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
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	gangShapeTestPCSName   = "pcs"
	gangShapeTestNamespace = "default"
)

// gangShapeChild describes the "fe" PodClique of one PCS replica.
type gangShapeChild struct {
	// hash is the gang-shape hash label. "current" is replaced by the current hash, "" omits the label.
	hash      string
	available bool
	// missing omits the PodClique, as for a replica whose PodCliques were deleted and are not recreated yet.
	missing bool
	// breached sets MinAvailableBreached=True on the PodClique, longer ago than the default breach tolerance.
	breached bool
}

func newGangShapeTestPCS(replicas int32) *grovecorev1beta1.PodCliqueSet {
	return testutils.NewPodCliqueSetBuilder(gangShapeTestPCSName, gangShapeTestNamespace, "uid").
		WithReplicas(replicas).
		WithPodCliqueTemplateSpec(testutils.NewPodCliqueTemplateSpecBuilder("fe").WithReplicas(2).WithMinAvailable(2).Build()).
		Build()
}

func newGangShapeTestClient(t *testing.T, pcs *grovecorev1beta1.PodCliqueSet, children []gangShapeChild, extraObjects ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1beta1.AddToScheme(scheme))
	currentHash := componentutils.ComputeGangShapeHash(pcs)
	objects := append([]client.Object{pcs}, extraObjects...)
	for replicaIndex, child := range children {
		if child.missing {
			continue
		}
		pclq := testutils.NewPodCliqueBuilder(pcs.Name, pcs.UID, "fe", pcs.Namespace, int32(replicaIndex)).WithReplicas(2).WithMinAvailable(2).Build()
		switch child.hash {
		case "":
		case "current":
			pclq.Labels[apicommon.LabelGangShapeHash] = currentHash
		default:
			pclq.Labels[apicommon.LabelGangShapeHash] = child.hash
		}
		if child.available {
			pclq.Status.ReadyReplicas = 2
		}
		if child.breached {
			pclq.Status.Conditions = []metav1.Condition{{
				Type:               apiconstants.ConditionTypeMinAvailableBreached,
				Status:             metav1.ConditionTrue,
				Reason:             apiconstants.ConditionReasonInsufficientReadyPods,
				LastTransitionTime: metav1.NewTime(time.Now().Add(-24 * time.Hour)),
			}}
		}
		objects = append(objects, pclq)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&grovecorev1beta1.PodCliqueSet{}, &grovecorev1beta1.PodCliqueScalingGroup{}).
		Build()
}

func withCurrentlyRecreating(pcs *grovecorev1beta1.PodCliqueSet, replicaIndex int32) {
	pcs.Status.GangShapeRecreate = &grovecorev1beta1.GangShapeRecreateProgress{
		GangShapeHash:       componentutils.ComputeGangShapeHash(pcs),
		CurrentlyRecreating: ptr.To(replicaIndex),
	}
}

// TestGetGangShapeWork pins the recreate order, the wait for available replicas, and how the replica recreated
// last is tracked.
func TestGetGangShapeWork(t *testing.T) {
	testCases := []struct {
		description             string
		children                []gangShapeChild
		currentlyUpdating       *int32
		currentlyRecreating     *int32
		pcsIndicesToTerminate   []int
		wantStale               []int
		wantRecreateIndex       *int
		wantCurrentlyRecreating *int
		wantRebuilding          bool
	}{
		{
			description: "no stale replica, no work",
			children:    []gangShapeChild{{hash: "current", available: true}, {hash: "current", available: true}},
		},
		{
			description: "a child without the label is adopted, not stale",
			children:    []gangShapeChild{{hash: "", available: true}, {hash: "current", available: true}},
		},
		{
			description:             "the lowest stale replica is recreated when every replica is available",
			children:                []gangShapeChild{{hash: "current", available: true}, {hash: "old", available: true}, {hash: "old", available: true}},
			wantStale:               []int{2},
			wantRecreateIndex:       ptr.To(1),
			wantCurrentlyRecreating: ptr.To(1),
			wantRebuilding:          true,
		},
		{
			description:             "an unavailable stale replica is recreated before an available one",
			children:                []gangShapeChild{{hash: "current", available: true}, {hash: "old", available: true}, {hash: "old"}},
			wantStale:               []int{1},
			wantRecreateIndex:       ptr.To(2),
			wantCurrentlyRecreating: ptr.To(2),
			wantRebuilding:          true,
		},
		{
			description:             "unavailable stale replicas go by lowest index, then available ones",
			children:                []gangShapeChild{{hash: "old", available: true}, {hash: "old"}, {hash: "old"}},
			wantStale:               []int{2, 0},
			wantRecreateIndex:       ptr.To(1),
			wantCurrentlyRecreating: ptr.To(1),
			wantRebuilding:          true,
		},
		{
			description:             "the stale replica under a rolling update is recreated first",
			children:                []gangShapeChild{{hash: "old"}, {hash: "old", available: true}},
			currentlyUpdating:       ptr.To[int32](1),
			wantStale:               []int{0},
			wantRecreateIndex:       ptr.To(1),
			wantCurrentlyRecreating: ptr.To(1),
			wantRebuilding:          true,
		},
		{
			description: "no recreate while a replica that is not stale is unavailable",
			children:    []gangShapeChild{{hash: "current", available: false}, {hash: "old"}},
			wantStale:   []int{1},
		},
		{
			description:           "no recreate while a replica is being gang-terminated",
			children:              []gangShapeChild{{hash: "current", available: true}, {hash: "old"}},
			pcsIndicesToTerminate: []int{0},
			wantStale:             []int{1},
		},
		{
			description:             "the replica recreated last is rebuilding while its PodCliques are missing",
			children:                []gangShapeChild{{missing: true}, {hash: "old", available: true}},
			currentlyRecreating:     ptr.To[int32](0),
			wantStale:               []int{1},
			wantCurrentlyRecreating: ptr.To(0),
			wantRebuilding:          true,
		},
		{
			description:             "the replica recreated last is waited for until it is available",
			children:                []gangShapeChild{{hash: "current"}, {hash: "old", available: true}},
			currentlyRecreating:     ptr.To[int32](0),
			wantStale:               []int{1},
			wantCurrentlyRecreating: ptr.To(0),
		},
		{
			description:             "the replica recreated last is waited for after the last stale replica",
			children:                []gangShapeChild{{hash: "current"}, {hash: "current", available: true}},
			currentlyRecreating:     ptr.To[int32](0),
			wantCurrentlyRecreating: ptr.To(0),
		},
		{
			description:             "once the replica recreated last is available, the next is recreated",
			children:                []gangShapeChild{{hash: "current", available: true}, {hash: "old", available: true}},
			currentlyRecreating:     ptr.To[int32](0),
			wantRecreateIndex:       ptr.To(1),
			wantCurrentlyRecreating: ptr.To(1),
			wantRebuilding:          true,
		},
		{
			description:             "after an init.minReplicas edit mid-recreate, a replica recreated to the earlier target is stale again",
			children:                []gangShapeChild{{hash: "earlier-target", available: true}, {hash: "current", available: true}, {hash: "old", available: true}},
			wantStale:               []int{2},
			wantRecreateIndex:       ptr.To(0),
			wantCurrentlyRecreating: ptr.To(0),
			wantRebuilding:          true,
		},
		{
			description:         "once the last recreated replica is available, the recreate ends",
			children:            []gangShapeChild{{hash: "current", available: true}, {hash: "current", available: true}},
			currentlyRecreating: ptr.To[int32](1),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			pcs := newGangShapeTestPCS(int32(len(tc.children)))
			if tc.currentlyUpdating != nil {
				pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
					CurrentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: *tc.currentlyUpdating}},
				}
			}
			if tc.currentlyRecreating != nil {
				withCurrentlyRecreating(pcs, *tc.currentlyRecreating)
			}
			r := _resource{client: newGangShapeTestClient(t, pcs, tc.children), eventRecorder: record.NewFakeRecorder(10)}

			work, err := r.getGangShapeWork(context.Background(), logr.Discard(), pcs, tc.pcsIndicesToTerminate)
			require.NoError(t, err)
			assert.Equal(t, tc.wantStale, lo.Ternary(len(work.staleReplicaIndices) == 0, nil, work.staleReplicaIndices))
			assert.Equal(t, tc.wantCurrentlyRecreating, work.currentlyRecreating)
			assert.Equal(t, tc.wantRebuilding, work.rebuilding)
			assert.Equal(t, tc.wantStale != nil || tc.wantCurrentlyRecreating != nil, work.inProgress())
			if tc.wantRecreateIndex == nil {
				assert.Nil(t, work.recreateTask)
				return
			}
			require.NotNil(t, work.recreateTask)
			assert.Equal(t, *tc.wantRecreateIndex, *work.currentlyRecreating)
		})
	}
}

// TestGangShapeRecreateDeletesReplicaAndEmitsEvent pins that the recreate deletes the stale replica's
// PodCliques and emits a GangShapeChanged event.
func TestGangShapeRecreateDeletesReplicaAndEmitsEvent(t *testing.T) {
	pcs := newGangShapeTestPCS(2)
	cl := newGangShapeTestClient(t, pcs, []gangShapeChild{{hash: "current", available: true}, {hash: "old"}})
	recorder := record.NewFakeRecorder(10)
	r := _resource{client: cl, eventRecorder: recorder}

	work, err := r.getGangShapeWork(context.Background(), logr.Discard(), pcs, nil)
	require.NoError(t, err)
	require.NotNil(t, work.recreateTask)
	require.NoError(t, work.recreateTask.Fn(context.Background()))

	pclqs := &grovecorev1beta1.PodCliqueList{}
	require.NoError(t, cl.List(context.Background(), pclqs, client.InNamespace(gangShapeTestNamespace)))
	require.Len(t, pclqs.Items, 1)
	assert.Equal(t, "0", pclqs.Items[0].Labels[apicommon.LabelPodCliqueSetReplicaIndex])

	select {
	case event := <-recorder.Events:
		assert.Contains(t, event, "GangShapeChanged")
	default:
		t.Fatal("expected a GangShapeChanged event")
	}
}

// TestMutateGangShapeRecreateStatus pins status.gangShapeRecreate and the GangShapeRecreateInProgress condition.
func TestMutateGangShapeRecreateStatus(t *testing.T) {
	now := metav1.NewTime(time.Now().Truncate(time.Second))
	earlier := metav1.NewTime(now.Add(-time.Minute))

	t.Run("a PodCliqueSet whose gang shape never changed gets no status or condition", func(t *testing.T) {
		pcs := newGangShapeTestPCS(2)
		mutateGangShapeRecreateStatus(pcs, &gangShapeWork{currentHash: "h"}, now)
		assert.Nil(t, pcs.Status.GangShapeRecreate)
		assert.Empty(t, pcs.Status.Conditions)
	})

	t.Run("a replica being recreated", func(t *testing.T) {
		pcs := newGangShapeTestPCS(3)
		mutateGangShapeRecreateStatus(pcs, &gangShapeWork{currentHash: "h", staleReplicaIndices: []int{2, 0}, currentlyRecreating: ptr.To(1), rebuilding: true}, now)
		assert.Equal(t, &grovecorev1beta1.GangShapeRecreateProgress{
			StartedAt:           now,
			GangShapeHash:       "h",
			CurrentlyRecreating: ptr.To[int32](1),
		}, pcs.Status.GangShapeRecreate)
		cond := meta.FindStatusCondition(pcs.Status.Conditions, apiconstants.ConditionTypeGangShapeRecreateInProgress)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
		assert.Equal(t, apiconstants.ConditionReasonRecreatingReplica, cond.Reason)
	})

	t.Run("waiting for available replicas keeps StartedAt and takes the new hash", func(t *testing.T) {
		pcs := newGangShapeTestPCS(2)
		pcs.Status.GangShapeRecreate = &grovecorev1beta1.GangShapeRecreateProgress{StartedAt: earlier, GangShapeHash: "old"}
		mutateGangShapeRecreateStatus(pcs, &gangShapeWork{currentHash: "new", staleReplicaIndices: []int{1}, currentlyRecreating: ptr.To(0)}, now)
		assert.Equal(t, earlier, pcs.Status.GangShapeRecreate.StartedAt)
		assert.Equal(t, "new", pcs.Status.GangShapeRecreate.GangShapeHash)
		cond := meta.FindStatusCondition(pcs.Status.Conditions, apiconstants.ConditionTypeGangShapeRecreateInProgress)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
		assert.Equal(t, apiconstants.ConditionReasonWaitingForAvailableReplicas, cond.Reason)
	})

	t.Run("the end of a recreate clears the status and sets the condition to False", func(t *testing.T) {
		pcs := newGangShapeTestPCS(2)
		pcs.Status.GangShapeRecreate = &grovecorev1beta1.GangShapeRecreateProgress{StartedAt: earlier, GangShapeHash: "h", CurrentlyRecreating: ptr.To[int32](1)}
		mutateGangShapeRecreateStatus(pcs, &gangShapeWork{currentHash: "h"}, now)
		assert.Nil(t, pcs.Status.GangShapeRecreate)
		cond := meta.FindStatusCondition(pcs.Status.Conditions, apiconstants.ConditionTypeGangShapeRecreateInProgress)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, apiconstants.ConditionReasonGangShapeCurrent, cond.Reason)
	})
}

// TestSyncRecordsGangShapeRecreateBeforeDeleting pins that Sync records the replica it recreates in the status,
// deletes it, and requeues without starting a rolling update.
func TestSyncRecordsGangShapeRecreateBeforeDeleting(t *testing.T) {
	pcs := newGangShapeTestPCS(2)
	pcs.Spec.UpdateStrategy = &grovecorev1beta1.PodCliqueSetUpdateStrategy{Type: grovecorev1beta1.RollingRecreateStrategy}
	pcs.Status.UpdateProgress = &grovecorev1beta1.PodCliqueSetUpdateProgress{UpdateStartedAt: metav1.Now()}
	cl := newGangShapeTestClient(t, pcs, []gangShapeChild{{hash: "current", available: true}, {hash: "old", available: true}})
	r := _resource{client: cl, eventRecorder: record.NewFakeRecorder(10)}

	err := r.Sync(context.Background(), logr.Discard(), pcs)
	var groveErr *groveerr.GroveError
	require.ErrorAs(t, err, &groveErr)
	assert.Equal(t, groveerr.ErrCodeContinueReconcileAndRequeue, groveErr.Code)

	got := &grovecorev1beta1.PodCliqueSet{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pcs), got))
	require.NotNil(t, got.Status.GangShapeRecreate)
	assert.Equal(t, ptr.To[int32](1), got.Status.GangShapeRecreate.CurrentlyRecreating)
	assert.True(t, meta.IsStatusConditionTrue(got.Status.Conditions, apiconstants.ConditionTypeGangShapeRecreateInProgress))
	assert.Equal(t, pcs.Status.UpdateProgress, got.Status.UpdateProgress, "the recreate must leave status.updateProgress untouched")
	assert.Empty(t, got.Status.UpdateProgress.CurrentlyUpdating, "a rolling update must not start a replica during a recreate")

	pclqs := &grovecorev1beta1.PodCliqueList{}
	require.NoError(t, cl.List(context.Background(), pclqs, client.InNamespace(gangShapeTestNamespace)))
	require.Len(t, pclqs.Items, 1)
	assert.Equal(t, "0", pclqs.Items[0].Labels[apicommon.LabelPodCliqueSetReplicaIndex])
}

// TestGangTerminationSkipsCurrentlyRecreating pins that gang termination skips only the replica that the
// gang-shape recreate deleted last.
func TestGangTerminationSkipsCurrentlyRecreating(t *testing.T) {
	pcs := newGangShapeTestPCS(2)
	withCurrentlyRecreating(pcs, 0)
	cl := newGangShapeTestClient(t, pcs, []gangShapeChild{{hash: "current", breached: true}, {hash: "current", breached: true}})
	r := _resource{client: cl, eventRecorder: record.NewFakeRecorder(10)}

	work, err := r.getPCSReplicaDeletionWork(context.Background(), logr.Discard(), pcs)
	require.NoError(t, err)
	assert.Equal(t, []int{1}, work.pcsIndicesToTerminate)
	assert.NotContains(t, work.minAvailableBreachedConstituents, 0)
}

// TestGangTerminationOfStaleReplicaWritesCurrentHash pins that gang termination writes the current gang-shape
// hash onto the replica's PCSGs, so a stale replica it deletes is not recreated again.
func TestGangTerminationOfStaleReplicaWritesCurrentHash(t *testing.T) {
	pcs := newGangShapeTestPCS(1)
	pcsg := &grovecorev1beta1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pcs-0-sg",
			Namespace: gangShapeTestNamespace,
			Labels: lo.Assign(apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name), map[string]string{
				apicommon.LabelPodCliqueSetReplicaIndex: "0",
				apicommon.LabelGangShapeHash:            "old",
			}),
		},
	}
	cl := newGangShapeTestClient(t, pcs, []gangShapeChild{{hash: "old"}}, pcsg)
	r := _resource{client: cl, eventRecorder: record.NewFakeRecorder(10)}

	task := r.createPCSReplicaDeleteTask(logr.Discard(), pcs, 0, constants.ReasonPodCliqueSetReplicaDeleteSuccessful, "breach")
	require.NoError(t, task.Fn(context.Background()))

	got := &grovecorev1beta1.PodCliqueScalingGroup{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(pcsg), got))
	assert.Equal(t, componentutils.ComputeGangShapeHash(pcs), got.Labels[apicommon.LabelGangShapeHash])
}

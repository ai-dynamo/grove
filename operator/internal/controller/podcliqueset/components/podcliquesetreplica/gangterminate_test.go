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

package podcliquesetreplica

import (
	"context"
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestGetMinAvailableBreachedPCSGInfoGangTerminationGate pins the PCS-level gate added to break
// the post-recycle loop: a PCSG that is currently MinAvailableBreached=True but already carries
// GangTerminationInProgress=True must NOT appear in the breach-candidate list — a previous
// recycle is in flight, the action would just churn.
func TestGetMinAvailableBreachedPCSGInfoGangTerminationGate(t *testing.T) {
	pastTransition := metav1.NewTime(time.Now().Add(-1 * time.Hour))
	now := time.Now()
	terminationDelay := 10 * time.Second

	breachedTrue := metav1.Condition{
		Type:               apiconstants.ConditionTypeMinAvailableBreached,
		Status:             metav1.ConditionTrue,
		Reason:             apiconstants.ConditionReasonScheduledReplicasBelowMinAvailable,
		LastTransitionTime: pastTransition,
	}
	inProgressTrue := metav1.Condition{
		Type:               apiconstants.ConditionTypeGangTerminationInProgress,
		Status:             metav1.ConditionTrue,
		Reason:             apiconstants.ConditionReasonGangTerminationActive,
		LastTransitionTime: pastTransition,
	}

	tests := []struct {
		name       string
		pcsg       grovecorev1beta1.PodCliqueScalingGroup
		wantInList bool
	}{
		{
			name: "breached, no in-progress flag — candidate for fire",
			pcsg: grovecorev1beta1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "pcsg-fresh-breach"},
				Status: grovecorev1beta1.PodCliqueScalingGroupStatus{
					Conditions: []metav1.Condition{breachedTrue},
				},
			},
			wantInList: true,
		},
		{
			name: "breached AND in-progress flag set — skipped (recycle in flight)",
			pcsg: grovecorev1beta1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "pcsg-recycling"},
				Status: grovecorev1beta1.PodCliqueScalingGroupStatus{
					Conditions: []metav1.Condition{breachedTrue, inProgressTrue},
				},
			},
			wantInList: false,
		},
		{
			name: "not breached, in-progress flag still set — skipped (no action needed)",
			pcsg: grovecorev1beta1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "pcsg-recovered-but-stale-flag"},
				Status: grovecorev1beta1.PodCliqueScalingGroupStatus{
					Conditions: []metav1.Condition{
						{Type: apiconstants.ConditionTypeMinAvailableBreached, Status: metav1.ConditionFalse, LastTransitionTime: pastTransition},
						inProgressTrue,
					},
				},
			},
			wantInList: false,
		},
		{
			name: "no MinAvailableBreached condition at all — skipped",
			pcsg: grovecorev1beta1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "pcsg-fresh"},
			},
			wantInList: false,
		},
		{
			name: "breached but never healthy (initial startup) — skipped by wasHealthy gate",
			pcsg: grovecorev1beta1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "pcsg-initial-startup",
					CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Second)),
				},
				Status: grovecorev1beta1.PodCliqueScalingGroupStatus{
					Conditions: []metav1.Condition{
						{
							Type:               apiconstants.ConditionTypeMinAvailableBreached,
							Status:             metav1.ConditionTrue,
							Reason:             apiconstants.ConditionReasonScheduledReplicasBelowMinAvailable,
							LastTransitionTime: metav1.NewTime(now.Add(-1 * time.Second)),
						},
					},
				},
			},
			wantInList: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			names, _ := getMinAvailableBreachedPCSGInfo([]grovecorev1beta1.PodCliqueScalingGroup{tc.pcsg}, terminationDelay, now)
			if tc.wantInList {
				assert.Equal(t, []string{tc.pcsg.Name}, names, "expected PCSG in breach candidate list")
			} else {
				assert.Empty(t, names, "expected PCSG to be filtered out")
			}
		})
	}
}

func TestGetPCSReplicaDeletionWork(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1beta1.AddToScheme(scheme))
	pcs := &grovecorev1beta1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "pcs", Namespace: "default"},
		Spec: grovecorev1beta1.PodCliqueSetSpec{
			Template: grovecorev1beta1.PodCliqueSetTemplateSpec{TerminationDelay: &metav1.Duration{}},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &_resource{client: cl}
	expectedPCSReplicaIndices := []int{0, 2, 4}

	work, err := r.getPCSReplicaDeletionWork(context.Background(), logr.Discard(), pcs, expectedPCSReplicaIndices, time.Now())

	require.NoError(t, err)
	assert.Empty(t, work.pcsIndicesToTerminate)
	assert.Empty(t, work.minAvailableBreachedConstituents)
	assert.Equal(t, []int{0, 2, 4}, expectedPCSReplicaIndices)
}

// TestCreatePCSReplicaDeleteTaskIgnoresStaleSiblingFlag pins the cross-episode overlap case:
// sg-b still carries GangTerminationInProgress from an earlier fire (its recycled pods have
// not recovered yet) while a NEW fire targets the replica because sg-a regressed again. The
// stale sibling flag must not suppress the DeleteAllOf — gating the delete on existing flags
// would leave sg-a breached, freshly flagged, and never recycled (permanent suppression).
func TestCreatePCSReplicaDeleteTaskIgnoresStaleSiblingFlag(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1beta1.AddToScheme(scheme))

	pcs := &grovecorev1beta1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "pcs", Namespace: "default"},
	}
	replicaLabels := lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
		map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "0"},
	)

	sgA := &grovecorev1beta1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "pcs-0-sg-a", Namespace: "default", Labels: replicaLabels},
	}
	sgB := &grovecorev1beta1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "pcs-0-sg-b", Namespace: "default", Labels: replicaLabels},
		Status: grovecorev1beta1.PodCliqueScalingGroupStatus{
			Conditions: []metav1.Condition{{
				Type:               apiconstants.ConditionTypeGangTerminationInProgress,
				Status:             metav1.ConditionTrue,
				Reason:             apiconstants.ConditionReasonGangTerminationActive,
				LastTransitionTime: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			}},
		},
	}
	pclq := &grovecorev1beta1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: "pcs-0-sg-a-pc-x", Namespace: "default", Labels: replicaLabels},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pcs, sgA, sgB, pclq).
		WithStatusSubresource(&grovecorev1beta1.PodCliqueScalingGroup{}).
		Build()
	r := _resource{client: cl, eventRecorder: record.NewFakeRecorder(10)}

	task := r.createPCSReplicaDeleteTask(logr.Discard(), pcs, 0, "sg-a regressed again")
	require.NoError(t, task.Fn(context.Background()))

	// The delete must have run despite sg-b's stale flag.
	pclqList := &grovecorev1beta1.PodCliqueList{}
	require.NoError(t, cl.List(context.Background(), pclqList, client.InNamespace("default"), client.MatchingLabels(replicaLabels)))
	assert.Empty(t, pclqList.Items, "PodCliques must be deleted even when a sibling PCSG carries a stale GangTerminationInProgress flag")

	// Both PCSGs end up flagged: sg-a freshly, sg-b unchanged.
	for _, name := range []string{sgA.Name, sgB.Name} {
		got := &grovecorev1beta1.PodCliqueScalingGroup{}
		require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "default"}, got))
		assert.True(t, meta.IsStatusConditionTrue(got.Status.Conditions, apiconstants.ConditionTypeGangTerminationInProgress), "expected %s to carry GangTerminationInProgress", name)
	}
}

// TestCreatePCSReplicaDeleteTaskDeletesPodGangMap pins that a full PCS-replica gang termination also
// deletes the replica's PodGangMap. The replica is recreated as a fresh initial deployment, so its map
// must bootstrap fresh rather than keep a structure a past coherent update may have left behind.
func TestCreatePCSReplicaDeleteTaskDeletesPodGangMap(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1beta1.AddToScheme(scheme))

	pcs := &grovecorev1beta1.PodCliqueSet{ObjectMeta: metav1.ObjectMeta{Name: "pcs", Namespace: "default"}}
	replicaLabels := lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
		map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "0"},
	)
	pclq := &grovecorev1beta1.PodClique{ObjectMeta: metav1.ObjectMeta{Name: "pcs-0-pc-a", Namespace: "default", Labels: replicaLabels}}
	pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: 0})
	pgm := &grovecorev1beta1.PodGangMap{ObjectMeta: metav1.ObjectMeta{Name: pgmName, Namespace: "default"}}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pcs, pclq, pgm).
		WithStatusSubresource(&grovecorev1beta1.PodCliqueScalingGroup{}).
		Build()
	r := _resource{client: cl, eventRecorder: record.NewFakeRecorder(10)}

	require.NoError(t, r.createPCSReplicaDeleteTask(logr.Discard(), pcs, 0, "full replica gang termination").Fn(context.Background()))

	// PodCliques of the replica are deleted.
	pclqList := &grovecorev1beta1.PodCliqueList{}
	require.NoError(t, cl.List(context.Background(), pclqList, client.InNamespace("default"), client.MatchingLabels(replicaLabels)))
	assert.Empty(t, pclqList.Items)

	// The replica's PodGangMap is deleted so the next reconcile bootstraps a fresh one.
	err := cl.Get(context.Background(), client.ObjectKey{Name: pgmName, Namespace: "default"}, &grovecorev1beta1.PodGangMap{})
	assert.True(t, apierrors.IsNotFound(err), "expected PodGangMap to be deleted, got %v", err)
}

func TestComputePCSReplicaGangTerminationCandidates(t *testing.T) {
	t.Parallel()
	endedAt := metav1.NewTime(time.Unix(100, 0).UTC())
	pcs := rollingUpdateTestPCS()
	pcs.Status.UpdateProgress.RequiresReplicaRecreation = true
	pcs.Status.UpdateProgress.CurrentlyUpdating = []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
		{ReplicaIndex: 2, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1, RetiredSourceReplicaIndices: []int32{3}}},
		{ReplicaIndex: 4},
		{ReplicaIndex: 5, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 6, CanceledAt: &endedAt, RetiredSourceReplicaIndices: []int32{7}}},
		{ReplicaIndex: 8, UpdateEndedAt: &endedAt},
		{ReplicaIndex: 9, UpdateEndedAt: &endedAt, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 9}},
	}
	indices := sets.New(9, 8, 7, 6, 5, 4, 3, 2, 1, 0)
	beforeIndices := indices.Clone()
	before := pcs.DeepCopy()
	assert.Equal(t, []int{0, 8, 9}, computePCSReplicaGangTerminationCandidates(pcs, indices))
	assert.Equal(t, beforeIndices, indices, "candidate filtering must not alter the resource-existence view")
	assert.Equal(t, before, pcs)
	pcs.Status.UpdateProgress.UpdateEndedAt = &endedAt
	assert.Equal(t, sets.List(indices), computePCSReplicaGangTerminationCandidates(pcs, indices), "completed progress cannot exclude later gang termination")
}

func TestGetPCSReplicaDeletionWorkUsesReconcileTimeWithoutExecuting(t *testing.T) {
	t.Parallel()
	pcs := lifecycleTestPCS()
	info := rollingUpdateReplicaInfo(pcs, 0, true)
	breachedAt := time.Unix(100, 0).UTC()
	info.standalonePCLQs[0].Status.LastScheduled = new(metav1.NewTime(breachedAt.Add(-time.Minute)))
	info.standalonePCLQs[0].Status.Conditions = []metav1.Condition{{Type: apiconstants.ConditionTypeMinAvailableBreached, Status: metav1.ConditionTrue, LastTransitionTime: metav1.NewTime(breachedAt)}}
	cl := testutils.SetupFakeClient(&info.standalonePCLQs[0])
	r := &_resource{client: cl}
	work, err := r.getPCSReplicaDeletionWork(t.Context(), logr.Discard(), pcs, []int{0}, breachedAt.Add(time.Minute-time.Second))
	require.NoError(t, err)
	assert.Empty(t, work.pcsIndicesToTerminate)
	assert.True(t, work.shouldRequeue())
	work, err = r.getPCSReplicaDeletionWork(t.Context(), logr.Discard(), pcs, []int{0}, breachedAt.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, []int{0}, work.pcsIndicesToTerminate)
	assert.False(t, work.shouldRequeue())
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(&info.standalonePCLQs[0]), &grovecorev1beta1.PodClique{}), "assessment must leave the resource present")
	require.NoError(t, (&_resource{}).executePCSReplicaGangTermination(t.Context(), logr.Discard(), pcs, nil), "empty execution needs no client")
}

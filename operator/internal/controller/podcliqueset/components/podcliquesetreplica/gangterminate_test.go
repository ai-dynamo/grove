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
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetMinAvailableBreachedPCSGInfoUsesPersistentReason(t *testing.T) {
	pastTransition := metav1.NewTime(time.Now().Add(-1 * time.Hour))
	now := time.Now()
	terminationDelay := 10 * time.Second

	condition := func(status metav1.ConditionStatus, reason string, observedGeneration int64) metav1.Condition {
		return metav1.Condition{
			Type:               apiconstants.ConditionTypeMinAvailableBreached,
			Status:             status,
			Reason:             reason,
			ObservedGeneration: observedGeneration,
			LastTransitionTime: pastTransition,
		}
	}

	tests := []struct {
		name       string
		pcsg       grovecorev1alpha1.PodCliqueScalingGroup
		wantInList bool
	}{
		{
			name: "healthy then regressed",
			pcsg: grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "pcsg-regressed", Generation: 1},
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					Conditions: []metav1.Condition{condition(metav1.ConditionTrue, apiconstants.ConditionReasonInsufficientAvailablePCSGReplicas, 1)},
				},
			},
			wantInList: true,
		},
		{
			name: "initial scheduling is skipped",
			pcsg: grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "pcsg-initial", Generation: 1},
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					Conditions: []metav1.Condition{condition(metav1.ConditionTrue, apiconstants.ConditionReasonInitialScheduling, 1)},
				},
			},
			wantInList: false,
		},
		{
			name: "legacy condition is skipped",
			pcsg: grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "pcsg-legacy", Generation: 1},
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					Conditions: []metav1.Condition{condition(metav1.ConditionTrue, apiconstants.ConditionReasonInsufficientAvailablePCSGReplicas, 0)},
				},
			},
			wantInList: false,
		},
		{
			name: "healthy condition is skipped",
			pcsg: grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "pcsg-healthy", Generation: 1},
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					Conditions: []metav1.Condition{condition(metav1.ConditionFalse, apiconstants.ConditionReasonSufficientAvailablePCSGReplicas, 1)},
				},
			},
			wantInList: false,
		},
		{
			name: "idle is skipped",
			pcsg: grovecorev1alpha1.PodCliqueScalingGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "pcsg-idle", Generation: 1},
				Spec:       grovecorev1alpha1.PodCliqueScalingGroupSpec{Replicas: 0},
				Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
					Conditions: []metav1.Condition{condition(metav1.ConditionTrue, apiconstants.ConditionReasonInsufficientAvailablePCSGReplicas, 1)},
				},
			},
			wantInList: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.pcsg.Name != "pcsg-idle" {
				tc.pcsg.Spec.Replicas = 1
			}
			names, _ := getMinAvailableBreachedPCSGInfo([]grovecorev1alpha1.PodCliqueScalingGroup{tc.pcsg}, terminationDelay, now)
			if tc.wantInList {
				assert.Equal(t, []string{tc.pcsg.Name}, names, "expected PCSG in breach candidate list")
			} else {
				assert.Empty(t, names, "expected PCSG to be filtered out")
			}
		})
	}
}

func TestCreatePCSReplicaDeleteTaskResetsPCSGState(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "pcs", Namespace: "default"},
	}
	replicaLabels := lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
		map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "0"},
	)

	sgA := &grovecorev1alpha1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "pcs-0-sg-a", Namespace: "default", Labels: replicaLabels, Generation: 1},
		Spec:       grovecorev1alpha1.PodCliqueScalingGroupSpec{Replicas: 1},
	}
	sgB := &grovecorev1alpha1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "pcs-0-sg-b", Namespace: "default", Labels: replicaLabels, Generation: 1},
		Spec:       grovecorev1alpha1.PodCliqueScalingGroupSpec{Replicas: 1},
		Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
			Conditions: []metav1.Condition{{
				Type:               apiconstants.ConditionTypeMinAvailableBreached,
				Status:             metav1.ConditionTrue,
				Reason:             apiconstants.ConditionReasonInsufficientAvailablePCSGReplicas,
				ObservedGeneration: 1,
				LastTransitionTime: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			}},
		},
	}
	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{Name: "pcs-0-sg-a-pc-x", Namespace: "default", Labels: replicaLabels},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pcs, sgA, sgB, pclq).
		WithStatusSubresource(&grovecorev1alpha1.PodCliqueScalingGroup{}).
		Build()
	r := _resource{client: cl, eventRecorder: record.NewFakeRecorder(10)}

	task := r.createPCSReplicaDeleteTask(logr.Discard(), pcs, 0, "gang regression")
	require.NoError(t, task.Fn(context.Background()))

	pclqList := &grovecorev1alpha1.PodCliqueList{}
	require.NoError(t, cl.List(context.Background(), pclqList, client.InNamespace("default"), client.MatchingLabels(replicaLabels)))
	assert.Empty(t, pclqList.Items)

	for _, name := range []string{sgA.Name, sgB.Name} {
		got := &grovecorev1alpha1.PodCliqueScalingGroup{}
		require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "default"}, got))
		condition := meta.FindStatusCondition(got.Status.Conditions, apiconstants.ConditionTypeMinAvailableBreached)
		require.NotNil(t, condition)
		assert.Equal(t, metav1.ConditionTrue, condition.Status)
		assert.Equal(t, apiconstants.ConditionReasonInitialScheduling, condition.Reason)
		assert.Equal(t, got.Generation, condition.ObservedGeneration)
	}
}

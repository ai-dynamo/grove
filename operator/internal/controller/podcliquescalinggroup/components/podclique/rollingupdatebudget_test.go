// /*
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
// */

package podclique

import (
	"context"
	"math"
	"strconv"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestEvaluateRollingUpdateBudget(t *testing.T) {
	maxOne := int32(1)
	maxTwo := int32(2)
	maxNine := int32(9)
	tests := []struct {
		name            string
		strategy        *grovecorev1alpha1.PodCliqueScalingGroupRollingUpdateStrategy
		replicas        []grovecorev1alpha1.PodClique
		wantEnabled     bool
		wantAllowed     int
		wantUnavailable int
		wantLimit       int
		wantReason      rollingUpdateBudgetBlockReason
	}{
		{
			name:        "unset strategy preserves original behavior",
			replicas:    readyBudgetReplicas(3),
			wantAllowed: math.MaxInt,
		},
		{
			name:        "empty strategy preserves original behavior",
			strategy:    &grovecorev1alpha1.PodCliqueScalingGroupRollingUpdateStrategy{},
			replicas:    readyBudgetReplicas(3),
			wantAllowed: math.MaxInt,
		},
		{
			name: "all available allows one replica",
			strategy: &grovecorev1alpha1.PodCliqueScalingGroupRollingUpdateStrategy{
				MaxUnavailable: &maxOne,
			},
			replicas:    readyBudgetReplicas(3),
			wantEnabled: true, wantAllowed: 1, wantLimit: 1,
		},
		{
			name: "max unavailable one blocks when one replica is unavailable",
			strategy: &grovecorev1alpha1.PodCliqueScalingGroupRollingUpdateStrategy{
				MaxUnavailable: &maxOne,
			},
			replicas: append(
				readyBudgetReplicas(2),
				budgetPCLQ(2, 0),
			),
			wantEnabled: true, wantUnavailable: 1, wantLimit: 1,
			wantReason: rollingUpdateBudgetMaxUnavailableReached,
		},
		{
			name: "one unavailable leaves one deletion from max two",
			strategy: &grovecorev1alpha1.PodCliqueScalingGroupRollingUpdateStrategy{
				MaxUnavailable: &maxTwo,
			},
			replicas: append(
				readyBudgetReplicas(2),
				budgetPCLQ(2, 0),
			),
			wantEnabled: true, wantAllowed: 1, wantUnavailable: 1, wantLimit: 2,
		},
		{
			name: "missing replica is unavailable",
			strategy: &grovecorev1alpha1.PodCliqueScalingGroupRollingUpdateStrategy{
				MaxUnavailable: &maxOne,
			},
			replicas:    readyBudgetReplicas(2),
			wantEnabled: true, wantUnavailable: 1, wantLimit: 1,
			wantReason: rollingUpdateBudgetMaxUnavailableReached,
		},
		{
			name: "max unavailable larger than replicas is capped",
			strategy: &grovecorev1alpha1.PodCliqueScalingGroupRollingUpdateStrategy{
				MaxUnavailable: &maxNine,
			},
			replicas:    readyBudgetReplicas(3),
			wantEnabled: true, wantAllowed: 3, wantLimit: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := budgetSyncContext(tt.strategy, tt.replicas)
			got, err := evaluateRollingUpdateBudget(sc)
			require.NoError(t, err)
			assert.Equal(t, tt.wantEnabled, got.enabled)
			assert.Equal(t, tt.wantAllowed, got.allowed)
			assert.Equal(t, tt.wantUnavailable, got.unavailable)
			assert.Equal(t, tt.wantLimit, got.limit)
			assert.Equal(t, tt.wantReason, got.reason)
		})
	}
}

func TestEvaluateRollingUpdateBudgetCountsSelectedReplicaBeforeCacheObservation(t *testing.T) {
	sc := budgetSyncContext(
		&grovecorev1alpha1.PodCliqueScalingGroupRollingUpdateStrategy{
			MaxUnavailable: ptr.To(int32(1)),
		},
		readyBudgetReplicas(3),
	)
	sc.pcsg.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{
		ReadyReplicaIndicesSelectedToUpdate: &grovecorev1alpha1.PodCliqueScalingGroupReplicaUpdateProgress{
			Current: 0,
		},
	}
	sc.expectedPCLQPodTemplateHashMap = map[string]string{
		budgetPCLQName(0): "new-hash",
	}

	got, err := evaluateRollingUpdateBudget(sc)
	require.NoError(t, err)
	assert.Equal(t, 1, got.unavailable)
	assert.True(t, got.blocked())
	assert.Equal(t, rollingUpdateBudgetMaxUnavailableReached, got.reason)
}

func TestIsCurrentReplicaUpdateComplete(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*syncContext)
		want   bool
	}{
		{
			name: "all child PodCliques converged and available",
			want: true,
		},
		{
			name: "desired label has not converged",
			mutate: func(sc *syncContext) {
				sc.existingPCLQs[0].Labels[apicommon.LabelPodTemplateHash] = "old-hash"
			},
		},
		{
			name: "current pod template hash has not converged",
			mutate: func(sc *syncContext) {
				sc.existingPCLQs[0].Status.CurrentPodTemplateHash = ptr.To("old-hash")
			},
		},
		{
			name: "PodCliqueSet generation has not converged",
			mutate: func(sc *syncContext) {
				sc.existingPCLQs[0].Status.CurrentPodCliqueSetGenerationHash = ptr.To("old-generation")
			},
		},
		{
			name: "updated replicas are below minAvailable",
			mutate: func(sc *syncContext) {
				sc.existingPCLQs[0].Status.UpdatedReplicas = 0
			},
		},
		{
			name: "ready replicas are below minAvailable",
			mutate: func(sc *syncContext) {
				sc.existingPCLQs[0].Status.ReadyReplicas = 0
			},
		},
		{
			name: "not all expected PodCliques exist",
			mutate: func(sc *syncContext) {
				sc.expectedPCLQFQNsPerPCSGReplica[0] = append(
					sc.expectedPCLQFQNsPerPCSGReplica[0],
					"test-pcsg-0-sidecar",
				)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pclq := budgetPCLQ(0, 1)
			pclq.Labels[apicommon.LabelPodTemplateHash] = "new-hash"
			pclq.Status.CurrentPodTemplateHash = ptr.To("new-hash")
			pclq.Status.CurrentPodCliqueSetGenerationHash = ptr.To("new-generation")
			pclq.Status.UpdatedReplicas = 1

			sc := budgetSyncContext(nil, []grovecorev1alpha1.PodClique{pclq})
			sc.pcs = &grovecorev1alpha1.PodCliqueSet{
				Status: grovecorev1alpha1.PodCliqueSetStatus{
					CurrentGenerationHash: ptr.To("new-generation"),
				},
			}
			sc.pcsg.Status.UpdateProgress = &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{
				ReadyReplicaIndicesSelectedToUpdate: &grovecorev1alpha1.PodCliqueScalingGroupReplicaUpdateProgress{
					Current: 0,
				},
			}
			sc.expectedPCLQPodTemplateHashMap = map[string]string{
				pclq.Name: "new-hash",
			}
			if tt.mutate != nil {
				tt.mutate(sc)
			}

			assert.Equal(t, tt.want, isCurrentReplicaUpdateComplete(sc))
		})
	}
}

func TestProcessPendingUpdatesHonorsReplicaBudget(t *testing.T) {
	strategy := &grovecorev1alpha1.PodCliqueScalingGroupRollingUpdateStrategy{
		MaxUnavailable: ptr.To(int32(1)),
	}

	t.Run("deletes exactly one complete replica when all replicas are available", func(t *testing.T) {
		r, sc := newRollingUpdateBudgetFixture(t, strategy, readyBudgetReplicas(3), nil)

		err := r.processPendingUpdates(logr.Discard(), sc)
		require.Error(t, err)
		require.NotNil(t, sc.pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate)
		assert.Equal(t, int32(0), sc.pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate.Current)
		assert.Equal(t, []string{"1", "2"}, remainingReplicaIndices(t, r.client))
	})

	t.Run("does not delete anything while one replica is unavailable", func(t *testing.T) {
		replicas := readyBudgetReplicas(3)
		replicas[0].Status.ReadyReplicas = 0
		r, sc := newRollingUpdateBudgetFixture(t, strategy, replicas, nil)

		err := r.processPendingUpdates(logr.Discard(), sc)
		require.Error(t, err)
		assert.Nil(t, sc.pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate)
		assert.Equal(t, []string{"0", "1", "2"}, remainingReplicaIndices(t, r.client))
	})

	t.Run("does not select a second replica during a rapid target change", func(t *testing.T) {
		current := int32(0)
		r, sc := newRollingUpdateBudgetFixture(t, strategy, readyBudgetReplicas(3), &current)

		err := r.processPendingUpdates(logr.Discard(), sc)
		require.Error(t, err)
		assert.Equal(t, int32(0), sc.pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate.Current)
		assert.Equal(t, []string{"0", "1", "2"}, remainingReplicaIndices(t, r.client))
	})
}

func TestTriggerDeletionOfExcessPCSGReplicasHonorsMaxUnavailable(t *testing.T) {
	tests := []struct {
		name             string
		strategy         *grovecorev1alpha1.PodCliqueScalingGroupRollingUpdateStrategy
		mutateReplicas   func([]grovecorev1alpha1.PodClique)
		wantRemaining    []string
		wantScalePending bool
	}{
		{
			name:             "unset max unavailable preserves legacy scale-in behavior",
			wantRemaining:    []string{"0"},
			wantScalePending: true,
		},
		{
			name: "max unavailable one deletes one logical replica",
			strategy: &grovecorev1alpha1.PodCliqueScalingGroupRollingUpdateStrategy{
				MaxUnavailable: ptr.To(int32(1)),
			},
			wantRemaining:    []string{"0", "1"},
			wantScalePending: true,
		},
		{
			name: "max unavailable two deletes two logical replicas",
			strategy: &grovecorev1alpha1.PodCliqueScalingGroupRollingUpdateStrategy{
				MaxUnavailable: ptr.To(int32(2)),
			},
			wantRemaining:    []string{"0"},
			wantScalePending: true,
		},
		{
			name: "terminating excess replica blocks a second scale-in deletion",
			strategy: &grovecorev1alpha1.PodCliqueScalingGroupRollingUpdateStrategy{
				MaxUnavailable: ptr.To(int32(1)),
			},
			mutateReplicas: func(replicas []grovecorev1alpha1.PodClique) {
				now := metav1.Now()
				replicas[2].DeletionTimestamp = &now
				replicas[2].Finalizers = []string{"test-finalizer"}
			},
			wantRemaining:    []string{"0", "1", "2"},
			wantScalePending: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			replicas := readyBudgetReplicas(3)
			if tt.mutateReplicas != nil {
				tt.mutateReplicas(replicas)
			}
			r, sc := newRollingUpdateBudgetFixture(t, tt.strategy, replicas, nil)
			sc.pcsg.Spec.Replicas = 1

			scaleInPending, err := r.triggerDeletionOfExcessPCSGReplicas(logr.Discard(), sc)
			require.NoError(t, err)
			assert.Equal(t, tt.wantScalePending, scaleInPending)
			assert.ElementsMatch(t, tt.wantRemaining, remainingReplicaIndices(t, r.client))
		})
	}
}

func budgetSyncContext(
	strategy *grovecorev1alpha1.PodCliqueScalingGroupRollingUpdateStrategy,
	replicas []grovecorev1alpha1.PodClique,
) *syncContext {
	expectedNames := make(map[int][]string, 3)
	for replicaIndex := range 3 {
		expectedNames[replicaIndex] = []string{budgetPCLQName(replicaIndex)}
	}
	return &syncContext{
		pcsg: &grovecorev1alpha1.PodCliqueScalingGroup{
			Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
				Replicas:     3,
				MinAvailable: ptr.To(int32(1)),
				CliqueNames:  []string{"worker"},
			},
		},
		pcsgConfig: &grovecorev1alpha1.PodCliqueScalingGroupConfig{
			RollingUpdate: strategy,
		},
		existingPCLQs:                  replicas,
		expectedPCLQFQNsPerPCSGReplica: expectedNames,
	}
}

func readyBudgetReplicas(count int) []grovecorev1alpha1.PodClique {
	replicas := make([]grovecorev1alpha1.PodClique, 0, count)
	for replicaIndex := range count {
		replicas = append(replicas, budgetPCLQ(replicaIndex, 1))
	}
	return replicas
}

func budgetPCLQ(replicaIndex int, ready int32) grovecorev1alpha1.PodClique {
	return grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      budgetPCLQName(replicaIndex),
			Namespace: "default",
			Labels: map[string]string{
				apicommon.LabelManagedByKey:                      apicommon.LabelManagedByValue,
				apicommon.LabelPartOfKey:                         "test-pcs",
				apicommon.LabelComponentKey:                      apicommon.LabelComponentNamePodCliqueScalingGroupPodClique,
				apicommon.LabelPodCliqueScalingGroup:             "test-pcsg",
				apicommon.LabelPodCliqueScalingGroupReplicaIndex: strconv.Itoa(replicaIndex),
				apicommon.LabelPodTemplateHash:                   "old-hash",
			},
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			Replicas:     1,
			MinAvailable: ptr.To(int32(1)),
		},
		Status: grovecorev1alpha1.PodCliqueStatus{
			ScheduledReplicas: 1,
			ReadyReplicas:     ready,
		},
	}
}

func budgetPCLQName(replicaIndex int) string {
	return "test-pcsg-" + strconv.Itoa(replicaIndex) + "-worker"
}

func newRollingUpdateBudgetFixture(
	t *testing.T,
	strategy *grovecorev1alpha1.PodCliqueScalingGroupRollingUpdateStrategy,
	replicas []grovecorev1alpha1.PodClique,
	currentReplica *int32,
) (*_resource, *syncContext) {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

	pcsg := &grovecorev1alpha1.PodCliqueScalingGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pcsg",
			Namespace: "default",
		},
		Spec: grovecorev1alpha1.PodCliqueScalingGroupSpec{
			Replicas:     3,
			MinAvailable: ptr.To(int32(1)),
			CliqueNames:  []string{"worker"},
		},
		Status: grovecorev1alpha1.PodCliqueScalingGroupStatus{
			AvailableReplicas: 3,
			UpdateProgress: &grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress{
				UpdateStartedAt:            metav1.Now(),
				PodCliqueSetGenerationHash: "new-generation",
			},
		},
	}
	if currentReplica != nil {
		pcsg.Status.UpdateProgress.ReadyReplicaIndicesSelectedToUpdate =
			&grovecorev1alpha1.PodCliqueScalingGroupReplicaUpdateProgress{Current: *currentReplica}
	}

	objects := make([]client.Object, 0, len(replicas)+1)
	objects = append(objects, pcsg)
	for i := range replicas {
		objects = append(objects, &replicas[i])
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&grovecorev1alpha1.PodCliqueScalingGroup{}).
		Build()

	expectedNames := make(map[int][]string, 3)
	expectedHashes := make(map[string]string, 3)
	for replicaIndex := range 3 {
		name := budgetPCLQName(replicaIndex)
		expectedNames[replicaIndex] = []string{name}
		expectedHashes[name] = "new-hash"
	}
	sc := &syncContext{
		ctx: context.Background(),
		pcs: &grovecorev1alpha1.PodCliqueSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pcs",
				Namespace: "default",
			},
		},
		pcsg:                           pcsg,
		pcsgConfig:                     &grovecorev1alpha1.PodCliqueScalingGroupConfig{RollingUpdate: strategy},
		existingPCLQs:                  replicas,
		expectedPCLQFQNsPerPCSGReplica: expectedNames,
		expectedPCLQPodTemplateHashMap: expectedHashes,
	}
	return &_resource{
		client:        fakeClient,
		scheme:        scheme,
		eventRecorder: record.NewFakeRecorder(32),
	}, sc
}

func remainingReplicaIndices(t *testing.T, cl client.Client) []string {
	t.Helper()
	var pclqList grovecorev1alpha1.PodCliqueList
	require.NoError(t, cl.List(context.Background(), &pclqList, client.InNamespace("default")))
	indices := make([]string, 0, len(pclqList.Items))
	for _, pclq := range pclqList.Items {
		indices = append(indices, pclq.Labels[apicommon.LabelPodCliqueScalingGroupReplicaIndex])
	}
	return indices
}

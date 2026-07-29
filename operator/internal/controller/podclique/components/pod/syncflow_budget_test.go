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

package pod

import (
	"testing"
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

func TestDeleteExcessPodsHonorsMaxUnavailable(t *testing.T) {
	tests := []struct {
		name                 string
		strategy             *grovecorev1alpha1.PodCliqueRollingUpdateStrategy
		existingDeletionUIDs []types.UID
		wantDeletionCount    int
	}{
		{
			name:              "unset max unavailable preserves legacy scale-in behavior",
			wantDeletionCount: 2,
		},
		{
			name: "max unavailable one starts one scale-in deletion",
			strategy: &grovecorev1alpha1.PodCliqueRollingUpdateStrategy{
				MaxUnavailable: ptr.To(int32(1)),
			},
			wantDeletionCount: 1,
		},
		{
			name: "max unavailable two permits two scale-in deletions",
			strategy: &grovecorev1alpha1.PodCliqueRollingUpdateStrategy{
				MaxUnavailable: ptr.To(int32(2)),
			},
			wantDeletionCount: 2,
		},
		{
			name: "an in-flight scale-in deletion blocks a second deletion",
			strategy: &grovecorev1alpha1.PodCliqueRollingUpdateStrategy{
				MaxUnavailable: ptr.To(int32(1)),
			},
			existingDeletionUIDs: []types.UID{"newest-uid"},
			wantDeletionCount:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now()
			pods := []*corev1.Pod{
				readyBudgetPodWithHashAndCreationTime("oldest", testOldHash, now.Add(-3*time.Minute)),
				readyBudgetPodWithHashAndCreationTime("middle", testOldHash, now.Add(-2*time.Minute)),
				readyBudgetPodWithHashAndCreationTime("newest", testOldHash, now.Add(-time.Minute)),
			}
			pclq, r, sc := newRollingUpdateStrategyFixture(t, tt.strategy, pods)
			pclq.Spec.Replicas = 1
			sc.pcs = &grovecorev1alpha1.PodCliqueSet{}
			if len(tt.existingDeletionUIDs) > 0 {
				require.NoError(t, r.expectationsStore.ExpectDeletions(
					logr.Discard(),
					sc.pclqExpectationsStoreKey,
					tt.existingDeletionUIDs...,
				))
			}

			require.NoError(t, r.deleteExcessPods(sc, logr.Discard(), 2))
			assert.Len(t, r.expectationsStore.GetDeleteExpectations(sc.pclqExpectationsStoreKey), tt.wantDeletionCount)
		})
	}
}

func TestDeleteExcessPodsCanRemoveAlreadyUnavailableCandidate(t *testing.T) {
	strategy := &grovecorev1alpha1.PodCliqueRollingUpdateStrategy{
		MaxUnavailable: ptr.To(int32(1)),
	}
	pods := []*corev1.Pod{
		pendingBudgetPod("pending"),
		readyBudgetPod("ready-1"),
		readyBudgetPod("ready-2"),
	}
	pclq, r, sc := newRollingUpdateStrategyFixture(t, strategy, pods)
	pclq.Spec.Replicas = 2
	sc.pcs = &grovecorev1alpha1.PodCliqueSet{}

	require.NoError(t, r.deleteExcessPods(sc, logr.Discard(), 1))
	assert.Equal(t, []types.UID{"pending-uid"}, r.expectationsStore.GetDeleteExpectations(sc.pclqExpectationsStoreKey))
}

func TestRunSyncFlowDoesNotOverlapScaleInAndRollingUpdateDeletion(t *testing.T) {
	now := time.Now()
	strategy := &grovecorev1alpha1.PodCliqueRollingUpdateStrategy{
		MaxUnavailable: ptr.To(int32(1)),
	}
	pods := []*corev1.Pod{
		readyBudgetPodWithHashAndCreationTime("oldest", testOldHash, now.Add(-3*time.Minute)),
		readyBudgetPodWithHashAndCreationTime("middle", testOldHash, now.Add(-2*time.Minute)),
		readyBudgetPodWithHashAndCreationTime("newest", testOldHash, now.Add(-time.Minute)),
	}
	pclq, r, sc := newRollingUpdateStrategyFixture(t, strategy, pods)
	pclq.Spec.Replicas = 2
	sc.pcs = &grovecorev1alpha1.PodCliqueSet{}

	r.runSyncFlow(logr.Discard(), sc)

	assert.Len(t, r.expectationsStore.GetDeleteExpectations(sc.pclqExpectationsStoreKey), 1)
	assert.Nil(t, pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate)

	// A second desired-state change before informer convergence must not consume
	// another deletion slot.
	pclq.Spec.Replicas = 1
	sc.expectedPodTemplateHash = "generation-newer"
	r.runSyncFlow(logr.Discard(), sc)

	assert.Len(t, r.expectationsStore.GetDeleteExpectations(sc.pclqExpectationsStoreKey), 1)
	assert.Nil(t, pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate)
}

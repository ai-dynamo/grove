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

package pod

import (
	"context"
	"math"
	"testing"
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/expect"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
		replicas        int32
		strategy        *grovecorev1alpha1.PodCliqueRollingUpdateStrategy
		pods            []*corev1.Pod
		deletions       []types.UID
		wantEnabled     bool
		wantAllowed     int
		wantUnavailable int
		wantLimit       int
		wantReason      rollingUpdateBudgetBlockReason
		wantErr         bool
	}{
		{
			name: "unset strategy preserves original behavior",
			pods: []*corev1.Pod{
				readyBudgetPod("p0"),
				readyBudgetPod("p1"),
				readyBudgetPod("p2"),
			},
			wantAllowed: math.MaxInt,
		},
		{
			name:     "empty strategy preserves original behavior",
			strategy: &grovecorev1alpha1.PodCliqueRollingUpdateStrategy{},
			pods: []*corev1.Pod{
				readyBudgetPod("p0"),
				readyBudgetPod("p1"),
				readyBudgetPod("p2"),
			},
			wantAllowed: math.MaxInt,
		},
		{
			name: "max one allows one deletion when all desired pods are available",
			strategy: &grovecorev1alpha1.PodCliqueRollingUpdateStrategy{
				MaxUnavailable: &maxOne,
			},
			pods: []*corev1.Pod{
				readyBudgetPod("p0"),
				readyBudgetPod("p1"),
				readyBudgetPod("p2"),
			},
			wantEnabled: true, wantAllowed: 1, wantLimit: 1,
		},
		{
			name: "pending pod exhausts max one",
			strategy: &grovecorev1alpha1.PodCliqueRollingUpdateStrategy{
				MaxUnavailable: &maxOne,
			},
			pods: []*corev1.Pod{
				pendingBudgetPod("p0"),
				readyBudgetPod("p1"),
				readyBudgetPod("p2"),
			},
			wantEnabled: true, wantUnavailable: 1, wantLimit: 1,
			wantReason: rollingUpdateBudgetMaxUnavailableReached,
		},
		{
			name: "one pending pod leaves one deletion from max two",
			strategy: &grovecorev1alpha1.PodCliqueRollingUpdateStrategy{
				MaxUnavailable: &maxTwo,
			},
			pods: []*corev1.Pod{
				pendingBudgetPod("p0"),
				readyBudgetPod("p1"),
				readyBudgetPod("p2"),
			},
			wantEnabled: true, wantAllowed: 1, wantUnavailable: 1, wantLimit: 2,
		},
		{
			name: "delete expectation immediately consumes availability budget",
			strategy: &grovecorev1alpha1.PodCliqueRollingUpdateStrategy{
				MaxUnavailable: &maxOne,
			},
			pods: []*corev1.Pod{
				readyBudgetPod("p0"),
				readyBudgetPod("p1"),
				readyBudgetPod("p2"),
			},
			deletions:   []types.UID{"p0-uid"},
			wantEnabled: true, wantUnavailable: 1, wantLimit: 1,
			wantReason: rollingUpdateBudgetMaxUnavailableReached,
		},
		{
			name: "terminating pod is unavailable",
			strategy: &grovecorev1alpha1.PodCliqueRollingUpdateStrategy{
				MaxUnavailable: &maxOne,
			},
			pods: []*corev1.Pod{
				terminatingBudgetPod("p0"),
				readyBudgetPod("p1"),
				readyBudgetPod("p2"),
			},
			wantEnabled: true, wantUnavailable: 1, wantLimit: 1,
			wantReason: rollingUpdateBudgetMaxUnavailableReached,
		},
		{
			name: "max unavailable larger than replicas is capped",
			strategy: &grovecorev1alpha1.PodCliqueRollingUpdateStrategy{
				MaxUnavailable: &maxNine,
			},
			pods: []*corev1.Pod{
				readyBudgetPod("p0"),
				readyBudgetPod("p1"),
				readyBudgetPod("p2"),
			},
			wantEnabled: true, wantAllowed: 3, wantLimit: 3,
		},
		{
			name:     "non-positive replicas fail closed",
			replicas: -1,
			strategy: &grovecorev1alpha1.PodCliqueRollingUpdateStrategy{
				MaxUnavailable: &maxOne,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			replicas := tt.replicas
			if replicas == 0 {
				replicas = 3
			}
			pclq := &grovecorev1alpha1.PodClique{
				Spec: grovecorev1alpha1.PodCliqueSpec{
					Replicas:      replicas,
					RollingUpdate: tt.strategy,
				},
			}

			got, err := evaluateRollingUpdateBudget(pclq, tt.pods, tt.deletions, nil)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantEnabled, got.enabled)
			assert.Equal(t, tt.wantAllowed, got.allowed)
			assert.Equal(t, tt.wantUnavailable, got.unavailable)
			assert.Equal(t, tt.wantLimit, got.limit)
			assert.Equal(t, tt.wantReason, got.reason)
		})
	}
}

func TestProcessPendingUpdatesHonorsRollingUpdateStrategy(t *testing.T) {
	t.Run("max one blocks all update deletions while a pod is pending", func(t *testing.T) {
		strategy := &grovecorev1alpha1.PodCliqueRollingUpdateStrategy{
			MaxUnavailable: ptr.To(int32(1)),
		}
		_, r, sc := newRollingUpdateStrategyFixture(t, strategy, []*corev1.Pod{
			pendingBudgetPodWithHash("pending", testOldHash),
			readyBudgetPod("ready-1"),
			readyBudgetPod("ready-2"),
		})

		err := r.processPendingUpdates(logr.Discard(), sc)
		require.Error(t, err)
		assert.Empty(t, r.expectationsStore.GetDeleteExpectations(sc.pclqExpectationsStoreKey))
	})

	t.Run("max one deletes only the oldest ready pod", func(t *testing.T) {
		now := time.Now()
		strategy := &grovecorev1alpha1.PodCliqueRollingUpdateStrategy{
			MaxUnavailable: ptr.To(int32(1)),
		}
		pclq, r, sc := newRollingUpdateStrategyFixture(t, strategy, []*corev1.Pod{
			readyBudgetPodWithCreationTime("middle", now.Add(-2*time.Minute)),
			readyBudgetPodWithCreationTime("newest", now.Add(-time.Minute)),
			readyBudgetPodWithCreationTime("oldest", now.Add(-3*time.Minute)),
		})

		err := r.processPendingUpdates(logr.Discard(), sc)
		require.Error(t, err)
		require.NotNil(t, pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate)
		assert.Equal(t, "oldest", pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate.Current)
		assert.Equal(t, []types.UID{"oldest-uid"}, r.expectationsStore.GetDeleteExpectations(sc.pclqExpectationsStoreKey))
	})

	t.Run("delete expectation prevents a second deletion before cache observation", func(t *testing.T) {
		strategy := &grovecorev1alpha1.PodCliqueRollingUpdateStrategy{
			MaxUnavailable: ptr.To(int32(1)),
		}
		_, r, sc := newRollingUpdateStrategyFixture(t, strategy, []*corev1.Pod{
			readyBudgetPod("ready-0"),
			readyBudgetPod("ready-1"),
			readyBudgetPod("ready-2"),
		})
		require.NoError(t, r.expectationsStore.ExpectDeletions(
			logr.Discard(), sc.pclqExpectationsStoreKey, sc.existingPCLQPods[0].UID,
		))

		err := r.processPendingUpdates(logr.Discard(), sc)
		require.Error(t, err)
		assert.Equal(t, []types.UID{"ready-0-uid"}, r.expectationsStore.GetDeleteExpectations(sc.pclqExpectationsStoreKey))
	})

	t.Run("a pending replacement blocks deletion after the target changes again", func(t *testing.T) {
		strategy := &grovecorev1alpha1.PodCliqueRollingUpdateStrategy{
			MaxUnavailable: ptr.To(int32(1)),
		}
		_, r, sc := newRollingUpdateStrategyFixture(t, strategy, []*corev1.Pod{
			pendingBudgetPodWithHash("replacement", testNewHash),
			readyBudgetPod("ready-1"),
			readyBudgetPod("ready-2"),
		})

		err := r.processPendingUpdates(logr.Discard(), sc)
		require.Error(t, err)
		assert.Empty(t, r.expectationsStore.GetDeleteExpectations(sc.pclqExpectationsStoreKey))
	})

	t.Run("scale-in clears a selected update whose replacement is no longer desired", func(t *testing.T) {
		strategy := &grovecorev1alpha1.PodCliqueRollingUpdateStrategy{
			MaxUnavailable: ptr.To(int32(1)),
		}
		pclq, r, sc := newRollingUpdateStrategyFixture(t, strategy, []*corev1.Pod{
			readyBudgetPod("remaining-old"),
		})
		pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate = &grovecorev1alpha1.PodsSelectedToUpdate{
			Current: "scaled-in-selected-pod",
		}

		err := r.processPendingUpdates(logr.Discard(), sc)
		require.Error(t, err)
		assert.Nil(t, pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate)
		assert.Empty(t, r.expectationsStore.GetDeleteExpectations(sc.pclqExpectationsStoreKey))

		err = r.processPendingUpdates(logr.Discard(), sc)
		require.Error(t, err)
		require.NotNil(t, pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate)
		assert.Equal(t, "remaining-old", pclq.Status.UpdateProgress.ReadyPodsSelectedToUpdate.Current)
		assert.Equal(t, []types.UID{"remaining-old-uid"}, r.expectationsStore.GetDeleteExpectations(sc.pclqExpectationsStoreKey))
	})
}

func newRollingUpdateStrategyFixture(
	t *testing.T,
	strategy *grovecorev1alpha1.PodCliqueRollingUpdateStrategy,
	pods []*corev1.Pod,
) (*grovecorev1alpha1.PodClique, *_resource, *syncContext) {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))

	readyReplicas := int32(0)
	objects := make([]client.Object, 0, len(pods)+1)
	for _, pod := range pods {
		if pod.Status.Phase == corev1.PodRunning &&
			pod.DeletionTimestamp == nil &&
			len(pod.Status.Conditions) > 0 &&
			pod.Status.Conditions[0].Status == corev1.ConditionTrue {
			readyReplicas++
		}
		objects = append(objects, pod)
	}

	pclq := &grovecorev1alpha1.PodClique{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rolling-update-pclq",
			Namespace: testNS,
		},
		Spec: grovecorev1alpha1.PodCliqueSpec{
			Replicas:      int32(len(pods)),
			MinAvailable:  ptr.To(int32(1)),
			RollingUpdate: strategy,
		},
		Status: grovecorev1alpha1.PodCliqueStatus{
			Replicas:      int32(len(pods)),
			ReadyReplicas: readyReplicas,
			UpdateProgress: &grovecorev1alpha1.PodCliqueUpdateProgress{
				UpdateStartedAt:            metav1.Now(),
				PodCliqueSetGenerationHash: "generation-new",
				PodTemplateHash:            testNewHash,
			},
		},
	}
	objects = append(objects, pclq)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&grovecorev1alpha1.PodClique{}).
		Build()
	store := expect.NewExpectationsStore()
	r := &_resource{
		client:            fakeClient,
		scheme:            scheme,
		eventRecorder:     record.NewFakeRecorder(32),
		expectationsStore: store,
	}
	sc := &syncContext{
		ctx:                      context.Background(),
		pclq:                     pclq,
		existingPCLQPods:         pods,
		expectedPodTemplateHash:  testNewHash,
		pclqExpectationsStoreKey: testNS + "/" + pclq.Name,
	}
	return pclq, r, sc
}

func readyBudgetPod(name string) *corev1.Pod {
	return readyBudgetPodWithCreationTime(name, time.Time{})
}

func readyBudgetPodWithCreationTime(name string, creationTime time.Time) *corev1.Pod {
	pod := newTestPod(
		name,
		testOldHash,
		withPhase(corev1.PodRunning),
		withReadyCondition(),
		withContainerStatus(ptr.To(true), true),
	)
	pod.UID = types.UID(name + "-uid")
	if !creationTime.IsZero() {
		pod.CreationTimestamp = metav1.NewTime(creationTime)
	}
	return pod
}

func pendingBudgetPod(name string) *corev1.Pod {
	return pendingBudgetPodWithHash(name, testOldHash)
}

func pendingBudgetPodWithHash(name, hash string) *corev1.Pod {
	pod := newTestPod(name, hash, withPhase(corev1.PodPending))
	pod.UID = types.UID(name + "-uid")
	return pod
}

func terminatingBudgetPod(name string) *corev1.Pod {
	pod := readyBudgetPod(name)
	withDeletionTimestamp()(pod)
	return pod
}

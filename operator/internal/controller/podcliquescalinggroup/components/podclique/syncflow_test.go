// /*
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
// */

package podclique

import (
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGetMinAvailableBreachedPCSGIndices(t *testing.T) {
	terminationDelay := 30 * time.Second
	now := time.Now()
	// breachedLongAgo → since.Sub(transitionTime) > terminationDelay → minWaitFor <= 0 → terminate.
	breachedLongAgo := now.Add(-2 * terminationDelay)
	// breachedRecently → since.Sub(transitionTime) < terminationDelay → minWaitFor > 0 → requeue.
	breachedRecently := now.Add(-terminationDelay / 4)

	pclqWithBreach := func(name, replicaIdx string, status metav1.ConditionStatus, transitionTime time.Time) grovecorev1alpha1.PodClique {
		return grovecorev1alpha1.PodClique{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: map[string]string{
					apicommon.LabelPodCliqueScalingGroupReplicaIndex: replicaIdx,
				},
			},
			Status: grovecorev1alpha1.PodCliqueStatus{
				Conditions: []metav1.Condition{
					{
						Type:               apiconstants.ConditionTypeMinAvailableBreached,
						Status:             status,
						LastTransitionTime: metav1.Time{Time: transitionTime},
					},
				},
			},
		}
	}

	t.Run("classifies breached replica past terminationDelay as terminate", func(t *testing.T) {
		pclqs := []grovecorev1alpha1.PodClique{
			pclqWithBreach("pclq-0", "0", metav1.ConditionTrue, breachedLongAgo),
		}
		toTerminate, toRequeue, err := getMinAvailableBreachedPCSGIndices(logr.Discard(), pclqs, terminationDelay)
		require.NoError(t, err)
		assert.Equal(t, []int{0}, toTerminate)
		assert.Empty(t, toRequeue)
	})

	t.Run("classifies recently-breached replica as requeue", func(t *testing.T) {
		pclqs := []grovecorev1alpha1.PodClique{
			pclqWithBreach("pclq-0", "0", metav1.ConditionTrue, breachedRecently),
		}
		toTerminate, toRequeue, err := getMinAvailableBreachedPCSGIndices(logr.Discard(), pclqs, terminationDelay)
		require.NoError(t, err)
		assert.Empty(t, toTerminate)
		assert.Equal(t, []int{0}, toRequeue)
	})

	t.Run("ignores replicas without breach condition true", func(t *testing.T) {
		pclqs := []grovecorev1alpha1.PodClique{
			pclqWithBreach("pclq-0", "0", metav1.ConditionFalse, breachedLongAgo),
			pclqWithBreach("pclq-1", "1", metav1.ConditionUnknown, breachedLongAgo),
		}
		toTerminate, toRequeue, err := getMinAvailableBreachedPCSGIndices(logr.Discard(), pclqs, terminationDelay)
		require.NoError(t, err)
		assert.Empty(t, toTerminate)
		assert.Empty(t, toRequeue)
	})

	t.Run("partitions multiple replicas across terminate and requeue", func(t *testing.T) {
		pclqs := []grovecorev1alpha1.PodClique{
			pclqWithBreach("pclq-0", "0", metav1.ConditionTrue, breachedLongAgo),
			pclqWithBreach("pclq-1", "1", metav1.ConditionTrue, breachedRecently),
			pclqWithBreach("pclq-2", "2", metav1.ConditionTrue, breachedLongAgo),
		}
		toTerminate, toRequeue, err := getMinAvailableBreachedPCSGIndices(logr.Discard(), pclqs, terminationDelay)
		require.NoError(t, err)
		assert.ElementsMatch(t, []int{0, 2}, toTerminate)
		assert.ElementsMatch(t, []int{1}, toRequeue)
	})

	t.Run("groups multiple PCLQs of one replica into a single index", func(t *testing.T) {
		// Two PCLQs (different cliques) share replica index 0. Both breached → still one index emitted.
		pclqs := []grovecorev1alpha1.PodClique{
			pclqWithBreach("pclq-0-frontend", "0", metav1.ConditionTrue, breachedLongAgo),
			pclqWithBreach("pclq-0-backend", "0", metav1.ConditionTrue, breachedLongAgo),
		}
		toTerminate, toRequeue, err := getMinAvailableBreachedPCSGIndices(logr.Discard(), pclqs, terminationDelay)
		require.NoError(t, err)
		assert.Equal(t, []int{0}, toTerminate)
		assert.Empty(t, toRequeue)
	})

	t.Run("non_numeric_replica_index_label_propagates_error", func(t *testing.T) {
		pclqs := []grovecorev1alpha1.PodClique{
			pclqWithBreach("pclq-bad", "abc", metav1.ConditionTrue, breachedLongAgo),
		}
		_, _, err := getMinAvailableBreachedPCSGIndices(logr.Discard(), pclqs, terminationDelay)
		assert.Error(t, err)
	})

	t.Run("empty input", func(t *testing.T) {
		toTerminate, toRequeue, err := getMinAvailableBreachedPCSGIndices(logr.Discard(), nil, terminationDelay)
		require.NoError(t, err)
		assert.Empty(t, toTerminate)
		assert.Empty(t, toRequeue)
	})
}

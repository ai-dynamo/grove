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

package pod

import (
	"sort"
	"testing"
	"time"

	"github.com/ai-dynamo/grove/operator/api/common"
	configv1beta1 "github.com/ai-dynamo/grove/operator/api/config/v1beta1"
	"github.com/ai-dynamo/grove/operator/internal/scalein"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// sortTestTemplateHash is the pod-template-hash shared by every Pod these tests build, so that
// DeletionSorter criterion 4 always ties and the criteria under test decide the order.
const sortTestTemplateHash = "abc123"

// newSortablePod builds a Pod that is scheduled, Running and Ready, i.e. indistinguishable from a
// healthy serving Pod on every DeletionSorter criterion except the ones the test varies.
func newSortablePod(name string, createdAt metav1.Time, terminating bool) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: createdAt,
			Labels:            map[string]string{common.LabelPodTemplateHash: sortTestTemplateHash},
		},
		Spec: corev1.PodSpec{NodeName: "node-a"},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	if terminating {
		pod.DeletionTimestamp = &createdAt
	}
	return pod
}

func podNames(pods []*corev1.Pod) []string {
	names := make([]string, 0, len(pods))
	for _, pod := range pods {
		names = append(names, pod.Name)
	}
	return names
}

// TestDeletionSorterPrefersTerminatingPods pins the defensive criterion 0.
//
// The scale-in caller (selectExcessPodsToDelete) filters terminating Pods out before sorting, so this
// criterion does not fire on that path. It exists for callers that hand DeletionSorter an unfiltered
// list — the type and its Pods field are both exported. A Pod inside its termination grace period
// keeps reporting Running and Ready, so without an explicit deletionTimestamp rule it ties with a
// healthy Pod on every other criterion and the resulting order is whatever the caller passed in.
func TestDeletionSorterPrefersTerminatingPods(t *testing.T) {
	createdAt := metav1.NewTime(time.Now())

	tests := []struct {
		name          string
		pods          []*corev1.Pod
		expectedFirst []string
	}{
		{
			name: "terminating pod sorts ahead of a healthy pod regardless of input order",
			pods: []*corev1.Pod{
				newSortablePod("healthy", createdAt, false),
				newSortablePod("terminating", createdAt, true),
			},
			expectedFirst: []string{"terminating", "healthy"},
		},
		{
			name: "terminating pod sorts ahead even when it is passed last",
			pods: []*corev1.Pod{
				newSortablePod("healthy-1", createdAt, false),
				newSortablePod("healthy-2", createdAt, false),
				newSortablePod("terminating", createdAt, true),
			},
			expectedFirst: []string{"terminating"},
		},
		{
			name: "all pods terminating leaves the remaining criteria in charge",
			pods: []*corev1.Pod{
				newSortablePod("older", metav1.NewTime(createdAt.Add(-time.Hour)), true),
				newSortablePod("newer", createdAt, true),
			},
			// criterion 5 prefers the newer pod for deletion
			expectedFirst: []string{"newer", "older"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sorter := DeletionSorter{Pods: tt.pods, ExpectedPodTemplateHash: sortTestTemplateHash}
			sort.Sort(sorter)
			assert.Equal(t, tt.expectedFirst, podNames(sorter.Pods)[:len(tt.expectedFirst)])
		})
	}
}

// TestDeletionSorterScaleInCost pins criterion 4: a lower scale-in cost is deleted first, after the serving
// criteria and before the template-hash and age tie-breaks. Equal costs fall through to those tie-breaks, and
// a nil cost map leaves the order exactly what it is without a cost.
func TestDeletionSorterScaleInCost(t *testing.T) {
	createdAt := metav1.NewTime(time.Now())
	older := metav1.NewTime(createdAt.Add(-time.Hour))

	notReady := func(name string) *corev1.Pod {
		pod := newSortablePod(name, createdAt, false)
		pod.Status.Conditions = nil
		return pod
	}
	outdated := func(name string) *corev1.Pod {
		pod := newSortablePod(name, createdAt, false)
		pod.Labels[common.LabelPodTemplateHash] = "old-hash"
		return pod
	}

	tests := []struct {
		name         string
		pods         []*corev1.Pod
		scaleInCosts map[string]int32
		expected     []string
	}{
		{
			name: "the lowest cost leaves first among serving pods",
			pods: []*corev1.Pod{
				newSortablePod("cost-5", createdAt, false),
				newSortablePod("cost-0", createdAt, false),
				newSortablePod("cost-negative", createdAt, false),
			},
			scaleInCosts: map[string]int32{"cost-5": 5, "cost-negative": -1000000},
			expected:     []string{"cost-negative", "cost-0", "cost-5"},
		},
		{
			name: "a cost never removes a serving pod ahead of one that is not serving",
			pods: []*corev1.Pod{
				newSortablePod("marked-ready", createdAt, false),
				notReady("unmarked-not-ready"),
			},
			scaleInCosts: map[string]int32{"marked-ready": -1000000},
			expected:     []string{"unmarked-not-ready", "marked-ready"},
		},
		{
			name: "a cost outranks the template hash and age",
			pods: []*corev1.Pod{
				outdated("outdated-unmarked"),
				newSortablePod("newest-unmarked", createdAt, false),
				newSortablePod("oldest-marked", older, false),
			},
			scaleInCosts: map[string]int32{"oldest-marked": -1},
			expected:     []string{"oldest-marked", "outdated-unmarked", "newest-unmarked"},
		},
		{
			name: "equal costs fall through to the template hash and age",
			pods: []*corev1.Pod{
				newSortablePod("older", older, false),
				newSortablePod("newer", createdAt, false),
				outdated("outdated"),
			},
			scaleInCosts: map[string]int32{"older": -3, "newer": -3, "outdated": -3},
			expected:     []string{"outdated", "newer", "older"},
		},
		{
			name: "no costs is today's order",
			pods: []*corev1.Pod{
				newSortablePod("older", older, false),
				newSortablePod("newer", createdAt, false),
			},
			expected: []string{"newer", "older"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sorter := DeletionSorter{Pods: tt.pods, ExpectedPodTemplateHash: sortTestTemplateHash, ScaleInCosts: tt.scaleInCosts}
			sort.Sort(sorter)
			assert.Equal(t, tt.expected, podNames(sorter.Pods))
		})
	}
}

func TestScaleInCostsByPodName(t *testing.T) {
	source, err := scalein.NewCostSource(configv1beta1.ScaleInCostSourcePodDeletionCost)
	require.NoError(t, err)
	marked := newSortablePod("marked", metav1.Now(), false)
	marked.Annotations = map[string]string{corev1.PodDeletionCost: "-9"}
	unmarked := newSortablePod("unmarked", metav1.Now(), false)
	zero := newSortablePod("zero", metav1.Now(), false)
	zero.Annotations = map[string]string{corev1.PodDeletionCost: "0"}
	pods := []*corev1.Pod{marked, unmarked, zero}

	assert.Equal(t, map[string]int32{"marked": -9}, scaleInCostsByPodName(source, pods))
	assert.Empty(t, scaleInCostsByPodName(nil, pods), "a nil source reads no cost")
}

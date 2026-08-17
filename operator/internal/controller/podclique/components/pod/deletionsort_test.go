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

	"github.com/stretchr/testify/assert"
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

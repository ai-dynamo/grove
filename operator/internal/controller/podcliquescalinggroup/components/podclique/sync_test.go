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

package podclique

import (
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func pclqAtIndex(name, replicaIndex string, terminating bool) grovecorev1alpha1.PodClique {
	objMeta := metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{apicommon.LabelPodCliqueScalingGroupReplicaIndex: replicaIndex},
	}
	if terminating {
		deletionTime := metav1.NewTime(time.Now())
		objMeta.DeletionTimestamp = &deletionTime
	}
	return grovecorev1alpha1.PodClique{ObjectMeta: objMeta}
}

func TestComputeExcessPCSGReplicaIndices(t *testing.T) {
	testCases := []struct {
		name             string
		existingPCLQs    []grovecorev1alpha1.PodClique
		expectedReplicas int
		wantIndices      []string
	}{
		{
			name: "sparse live indices with a terminating lower replica still delete the high replicas",
			existingPCLQs: []grovecorev1alpha1.PodClique{
				pclqAtIndex("pcs-0-sg-0-w", "0", false),
				pclqAtIndex("pcs-0-sg-1-w", "1", true),
				pclqAtIndex("pcs-0-sg-2-w", "2", false),
				pclqAtIndex("pcs-0-sg-3-w", "3", false),
			},
			expectedReplicas: 2,
			wantIndices:      []string{"2", "3"},
		},
		{
			name: "contiguous scale-down deletes only the index at or above the bound",
			existingPCLQs: []grovecorev1alpha1.PodClique{
				pclqAtIndex("pcs-0-sg-0-w", "0", false),
				pclqAtIndex("pcs-0-sg-1-w", "1", false),
				pclqAtIndex("pcs-0-sg-2-w", "2", false),
			},
			expectedReplicas: 2,
			wantIndices:      []string{"2"},
		},
		{
			name: "no excess returns nothing",
			existingPCLQs: []grovecorev1alpha1.PodClique{
				pclqAtIndex("pcs-0-sg-0-w", "0", false),
				pclqAtIndex("pcs-0-sg-1-w", "1", false),
			},
			expectedReplicas: 2,
			wantIndices:      []string{},
		},
		{
			name: "a terminating excess replica is not re-deleted",
			existingPCLQs: []grovecorev1alpha1.PodClique{
				pclqAtIndex("pcs-0-sg-0-w", "0", false),
				pclqAtIndex("pcs-0-sg-1-w", "1", false),
				pclqAtIndex("pcs-0-sg-2-w", "2", true),
			},
			expectedReplicas: 2,
			wantIndices:      []string{},
		},
		{
			name: "multiple PodCliques at the same excess index yield that index once",
			existingPCLQs: []grovecorev1alpha1.PodClique{
				pclqAtIndex("pcs-0-sg-0-leader", "0", false),
				pclqAtIndex("pcs-0-sg-0-worker", "0", false),
				pclqAtIndex("pcs-0-sg-2-leader", "2", false),
				pclqAtIndex("pcs-0-sg-2-worker", "2", false),
			},
			expectedReplicas: 2,
			wantIndices:      []string{"2"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeExcessPCSGReplicaIndices(tc.existingPCLQs, tc.expectedReplicas)
			assert.ElementsMatch(t, tc.wantIndices, got)
		})
	}
}

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

package validation

import (
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	"k8s.io/apimachinery/pkg/util/sets"
)

// maximumReplicaIndex includes existing identities, reservations, and new allocations.
// The allocator uses the lowest free indexes. A pending scale-in keeps its index
// reserved, so each such reservation can move a new allocation up by one index.
// This bound needs no loop over the requested count, including MaxInt32 requests.
func maximumReplicaIndex(pcs *grovecorev1beta1.PodCliqueSet, currentMaximumIndex int) int {
	maximum := max(int(pcs.Spec.Replicas)-1, currentMaximumIndex)
	if progress := pcs.Status.ScalingProgress; progress != nil {
		for _, index := range progress.ScaleOutReplicaIndices {
			maximum = max(maximum, int(index))
		}
		scaleIn := sets.New[int32]()
		for _, index := range progress.ScaleInReplicaIndices {
			if index >= 0 {
				scaleIn.Insert(index)
				maximum = max(maximum, int(index))
			}
		}
		maximum = max(maximum, int(pcs.Spec.Replicas)+scaleIn.Len()-1)
	}
	return maximum
}

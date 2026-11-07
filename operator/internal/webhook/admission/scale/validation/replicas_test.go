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
	"math"
	"testing"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	"github.com/stretchr/testify/assert"
)

func TestMaximumPCSReplicaIndex(t *testing.T) {
	for _, tt := range []struct {
		name                string
		replicas            int32
		currentMaximumIndex int
		progress            *grovecorev1beta1.PodCliqueSetScalingProgress
		want                int
	}{
		{name: "no replicas", currentMaximumIndex: -1, want: -1},
		{name: "new replicas", replicas: 11, currentMaximumIndex: -1, want: 10},
		{name: "retained identity", replicas: 1, currentMaximumIndex: 10, want: 10},
		{name: "reserved scale-out identity", replicas: 1, currentMaximumIndex: -1, progress: &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleOutReplicaIndices: []int32{10}}, want: 10},
		{name: "new identity after pending removals", replicas: 1, currentMaximumIndex: -1, progress: &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}}, want: 10},
		{name: "sparse reservations leave lower indexes free", replicas: 2, currentMaximumIndex: -1, progress: &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{10}}, want: 10},
		{name: "maximum replica count with a blocked index", replicas: math.MaxInt32, currentMaximumIndex: -1, progress: &grovecorev1beta1.PodCliqueSetScalingProgress{ScaleInReplicaIndices: []int32{0}}, want: math.MaxInt32},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pcs := &grovecorev1beta1.PodCliqueSet{Spec: grovecorev1beta1.PodCliqueSetSpec{Replicas: tt.replicas}, Status: grovecorev1beta1.PodCliqueSetStatus{ScalingProgress: tt.progress}}
			assert.Equal(t, tt.want, maximumReplicaIndex(pcs, tt.currentMaximumIndex))
		})
	}
}

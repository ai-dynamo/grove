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

package podcliquesetreplica

import (
	"slices"
	"testing"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
)

func TestGetNumScheduledPods(t *testing.T) {
	// PCS spec supplies the member clique's MinAvailable for the PCSG term of getNumScheduledPods.
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "uid").
		WithScalingGroupConfig("prefill", []string{"worker"}, 1, 2).
		WithStandaloneCliqueMinAvailable("worker", 2).
		Build()

	pclq := func(scheduled int32) grovecorev1alpha1.PodClique {
		return *testutils.NewPodCliqueBuilder("test-pcs", "uid", "frontend", "default", 0).
			WithStatusScheduledReplicas(scheduled).Build()
	}
	pcsg := func(cliqueNames []string, scheduled int32) grovecorev1alpha1.PodCliqueScalingGroup {
		return *testutils.NewPodCliqueScalingGroupBuilder("test-pcs-0-prefill", "default", "test-pcs", 0).
			WithCliqueNames(cliqueNames).WithStatusScheduledReplicas(scheduled).Build()
	}

	tests := []struct {
		name string
		pri  pcsReplicaInfo
		want int
	}{
		{name: "no children", pri: pcsReplicaInfo{}, want: 0},
		{
			name: "standalone PCLQ scheduled replicas summed",
			pri:  pcsReplicaInfo{pclqs: []grovecorev1alpha1.PodClique{pclq(3), pclq(1)}},
			want: 4,
		},
		{
			name: "PCSG contributes ScheduledReplicas times member MinAvailable",
			pri:  pcsReplicaInfo{pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{pcsg([]string{"worker"}, 3)}},
			want: 6, // 3 * MinAvailable(2)
		},
		{
			name: "PCLQ and PCSG combined",
			pri: pcsReplicaInfo{
				pclqs: []grovecorev1alpha1.PodClique{pclq(2)},
				pcsgs: []grovecorev1alpha1.PodCliqueScalingGroup{pcsg([]string{"worker"}, 1)},
			},
			want: 4, // 2 + 1*2
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.pri.getNumScheduledPods(pcs))
		})
	}
}

func TestOrderPCSReplicaInfo(t *testing.T) {
	// PCS with no PCSGs, so getNumScheduledPods reduces to the sum of PCLQ ScheduledReplicas.
	pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "uid").Build()

	// replica builds a pcsReplicaInfo at the given index whose total scheduled pods is carried by a
	// single standalone PCLQ.
	replica := func(index int, scheduled int32) pcsReplicaInfo {
		return pcsReplicaInfo{
			replicaIndex: index,
			pclqs: []grovecorev1alpha1.PodClique{
				*testutils.NewPodCliqueBuilder("test-pcs", "uid", "frontend", "default", int32(index)).
					WithStatusScheduledReplicas(scheduled).Build(),
			},
		}
	}

	tests := []struct {
		name         string
		input        []pcsReplicaInfo
		breached     []int
		wantOrderIdx []int
	}{
		{
			name:         "zero-scheduled replicas sort before scheduled ones",
			input:        []pcsReplicaInfo{replica(0, 5), replica(1, 0)},
			wantOrderIdx: []int{1, 0},
		},
		{
			name:         "among scheduled, minAvailable-breached sorts first",
			input:        []pcsReplicaInfo{replica(0, 5), replica(1, 5)},
			breached:     []int{1},
			wantOrderIdx: []int{1, 0},
		},
		{
			name:         "otherwise lowest replicaIndex first",
			input:        []pcsReplicaInfo{replica(2, 5), replica(0, 5), replica(1, 5)},
			wantOrderIdx: []int{0, 1, 2},
		},
		{
			name:         "zero-scheduled beats breached",
			input:        []pcsReplicaInfo{replica(0, 5), replica(1, 0)}, // index 0 breached, index 1 has zero scheduled
			breached:     []int{0},
			wantOrderIdx: []int{1, 0},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := append([]pcsReplicaInfo(nil), tc.input...)
			slices.SortFunc(got, orderPCSReplicaInfo(pcs, tc.breached))
			gotIdx := make([]int, len(got))
			for i, ri := range got {
				gotIdx[i] = ri.replicaIndex
			}
			assert.Equal(t, tc.wantOrderIdx, gotIdx)
		})
	}
}

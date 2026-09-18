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

package podclique

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestReplicaIndicesNotOnCommittedPodGang(t *testing.T) {
	const (
		pcsName    = "test-pcs"
		namespace  = "default"
		pcsgConfig = "sg"
		epochA     = "1000"
		epochB     = "1001"
	)
	rnr := apicommon.ResourceNameReplica{Name: pcsName, Replica: 0}
	pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(rnr, pcsgConfig)
	committedForReplica0 := apicommon.GenerateAnchorPodGangName(rnr, epochA)
	committedForReplica1 := apicommon.GenerateAnchorPodGangName(rnr, epochB)

	pcs := &grovecorev1alpha1.PodCliqueSet{ObjectMeta: metav1.ObjectMeta{Name: pcsName, Namespace: namespace}}
	pcsg := testutils.NewPodCliqueScalingGroupBuilder(pcsgFQN, namespace, pcsName, 0).
		WithReplicas(2).
		WithCliqueNames([]string{"worker"}).
		Build()
	// The PodGangMap commits replica index 0 to anchor A and replica index 1 to anchor B.
	pgm := testutils.NewPodGangMapBuilder(pcsName, namespace, "uid", 0).WithEntries(
		testutils.NewPodGangEntryBuilder("hash", epochA).
			WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
			WithPCSGReplicaIndices(map[string][]int32{pcsgConfig: {0}}).Build(),
		testutils.NewPodGangEntryBuilder("hash", epochB).
			WithRole(grovecorev1alpha1.PodGangEntryRoleAnchor).
			WithPCSGReplicaIndices(map[string][]int32{pcsgConfig: {1}}).Build(),
	).Build()

	memberOnPodGang := func(pcsgReplicaIndex int, podGangName string) grovecorev1alpha1.PodClique {
		name := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsgFQN, Replica: pcsgReplicaIndex}, "worker")
		return *testutils.NewPCSGPodCliqueBuilder(name, namespace, pcsName, pcsgFQN, 0, pcsgReplicaIndex).
			WithLabels(map[string]string{apicommon.LabelPodGang: podGangName}).
			Build()
	}

	tests := []struct {
		name          string
		existingPCLQs []grovecorev1alpha1.PodClique
		want          []string
	}{
		{
			name: "a replica on a superseded PodGang is selected for recreation",
			existingPCLQs: []grovecorev1alpha1.PodClique{
				memberOnPodGang(0, committedForReplica0),
				memberOnPodGang(1, apicommon.GenerateAnchorPodGangName(rnr, "999")),
			},
			want: []string{"1"},
		},
		{
			name: "every replica on its committed PodGang is untouched",
			existingPCLQs: []grovecorev1alpha1.PodClique{
				memberOnPodGang(0, committedForReplica0),
				memberOnPodGang(1, committedForReplica1),
			},
			want: nil,
		},
		{
			name: "a replica with no members is skipped",
			existingPCLQs: []grovecorev1alpha1.PodClique{
				memberOnPodGang(0, committedForReplica0),
			},
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ss := &syncSnapshot{pcs: pcs, pcsg: pcsg, pcsReplicaIndex: 0, pgm: pgm, existingPCLQs: tc.existingPCLQs}
			got, err := replicaIndicesNotOnCommittedPodGang(ss)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

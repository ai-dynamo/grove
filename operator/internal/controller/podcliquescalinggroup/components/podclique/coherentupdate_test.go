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
	"errors"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestReplicaIndicesToRecreate(t *testing.T) {
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

	memberOnPodGang := func(pcsgReplicaIndex int, cliqueName, podGangName string) grovecorev1alpha1.PodClique {
		name := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcsgFQN, Replica: pcsgReplicaIndex}, cliqueName)
		return *testutils.NewPCSGPodCliqueBuilder(name, namespace, pcsName, pcsgFQN, 0, pcsgReplicaIndex).
			WithLabels(map[string]string{apicommon.LabelPodGang: podGangName}).
			Build()
	}
	terminatingMemberOnPodGang := func(pcsgReplicaIndex int, cliqueName, podGangName string) grovecorev1alpha1.PodClique {
		pclq := memberOnPodGang(pcsgReplicaIndex, cliqueName, podGangName)
		now := metav1.Now()
		pclq.DeletionTimestamp = &now
		return pclq
	}

	tests := []struct {
		name          string
		existingPCLQs []grovecorev1alpha1.PodClique
		want          []string
	}{
		{
			name: "a replica on a superseded PodGang is selected for recreation",
			existingPCLQs: []grovecorev1alpha1.PodClique{
				memberOnPodGang(0, "worker", committedForReplica0),
				memberOnPodGang(1, "worker", apicommon.GenerateAnchorPodGangName(rnr, "999")),
			},
			want: []string{"1"},
		},
		{
			name: "every replica on its committed PodGang is untouched",
			existingPCLQs: []grovecorev1alpha1.PodClique{
				memberOnPodGang(0, "worker", committedForReplica0),
				memberOnPodGang(1, "worker", committedForReplica1),
			},
			want: nil,
		},
		{
			name: "a replica with no members is skipped",
			existingPCLQs: []grovecorev1alpha1.PodClique{
				memberOnPodGang(0, "worker", committedForReplica0),
			},
			want: nil,
		},
		{
			name: "a replica on a superseded PodGang with a terminating member is not recreated while it drains",
			existingPCLQs: []grovecorev1alpha1.PodClique{
				memberOnPodGang(0, "worker", committedForReplica0),
				terminatingMemberOnPodGang(1, "worker", apicommon.GenerateAnchorPodGangName(rnr, "999")),
			},
			want: nil,
		},
		{
			name: "a replica with a live sibling on the committed PodGang is not recreated while another member is terminating",
			existingPCLQs: []grovecorev1alpha1.PodClique{
				memberOnPodGang(0, "worker", committedForReplica0),
				memberOnPodGang(0, "leader", committedForReplica0),
				// Replica 1 was recreated: its worker already came back on the committed PodGang, but its
				// leader is still terminating on the superseded PodGang. The replica must not be recreated
				// again, else the live worker would be deleted on every reconcile until the leader is gone.
				memberOnPodGang(1, "worker", committedForReplica1),
				terminatingMemberOnPodGang(1, "leader", apicommon.GenerateAnchorPodGangName(rnr, "999")),
			},
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ss := &syncSnapshot{pcs: pcs, pcsg: pcsg, pcsReplicaIndex: 0, pgm: pgm, existingPCLQs: tc.existingPCLQs}
			got, err := replicaIndicesToRecreate(ss)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestMarkCoherentUpdateEndIfConvergedRequeuesWhenNotConverged verifies the coherent update is not
// ended while a replica is still draining or pending recreation, and that the reconcile is requeued so
// the roll keeps progressing rather than stalling until a later watch event.
func TestMarkCoherentUpdateEndIfConvergedRequeuesWhenNotConverged(t *testing.T) {
	pcsg := testutils.NewPodCliqueScalingGroupBuilder("test-pcsg", "test-ns", "test-pcs", 0).
		WithReplicas(1).
		WithCliqueNames([]string{"worker"}).
		Build()
	pcs := &grovecorev1alpha1.PodCliqueSet{ObjectMeta: metav1.ObjectMeta{Name: "test-pcs", Namespace: "test-ns"}}
	// Replica 0 is expected but has no members yet, so it is neither updated nor Ready.
	ss := &syncSnapshot{
		pcs:                            pcs,
		pcsg:                           pcsg,
		expectedPCLQFQNsPerPCSGReplica: map[int][]string{0: {"test-pcsg-0-worker"}},
	}

	err := _resource{}.markCoherentUpdateEndIfConverged(t.Context(), logr.Discard(), ss)

	require.Error(t, err)
	var groveError *groveerr.GroveError
	require.True(t, errors.As(err, &groveError))
	assert.Equal(t, groveerr.ErrCodeContinueReconcileAndRequeue, groveError.Code)
}

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

package component

import (
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestFindScalingGroupConfigForClique(t *testing.T) {
	// Create test scaling group configurations
	scalingGroupConfigs := []grovecorev1beta1.PodCliqueScalingGroupConfig{
		{
			Name:        "sga",
			CliqueNames: []string{"pca", "pcb"},
		},
		{
			Name:        "sgb",
			CliqueNames: []string{"pcc", "pcd", "pce"},
		},
		{
			Name:        "sgc",
			CliqueNames: []string{"pcf"},
		},
	}

	tests := []struct {
		name               string
		configs            []grovecorev1beta1.PodCliqueScalingGroupConfig
		cliqueName         string
		expectedFound      bool
		expectedConfigName string
	}{
		{
			name:               "clique found in first scaling group",
			configs:            scalingGroupConfigs,
			cliqueName:         "pca",
			expectedFound:      true,
			expectedConfigName: "sga",
		},
		{
			name:               "clique found in second scaling group",
			configs:            scalingGroupConfigs,
			cliqueName:         "pcd",
			expectedFound:      true,
			expectedConfigName: "sgb",
		},
		{
			name:               "clique found in third scaling group",
			configs:            scalingGroupConfigs,
			cliqueName:         "pcf",
			expectedFound:      true,
			expectedConfigName: "sgc",
		},
		{
			name:               "clique not found in any scaling group",
			configs:            scalingGroupConfigs,
			cliqueName:         "nonexistent",
			expectedFound:      false,
			expectedConfigName: "",
		},
		{
			name:               "empty clique name",
			configs:            scalingGroupConfigs,
			cliqueName:         "",
			expectedFound:      false,
			expectedConfigName: "",
		},
		{
			name:               "empty configs",
			configs:            []grovecorev1beta1.PodCliqueScalingGroupConfig{},
			cliqueName:         "anyClique",
			expectedFound:      false,
			expectedConfigName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := FindScalingGroupConfigForClique(tt.configs, tt.cliqueName)
			assert.Equal(t, tt.expectedFound, config != nil)
			if tt.expectedFound {
				assert.Equal(t, tt.expectedConfigName, config.Name)
			} else {
				// When not found, config should be nil
				assert.Nil(t, config)
			}
		})
	}
}

// TestIsPCSGUpdateInProgress tests the IsPCSGUpdateInProgress function
func TestIsPCSGUpdateInProgress(t *testing.T) {
	tests := []struct {
		name     string
		pcsg     *grovecorev1beta1.PodCliqueScalingGroup
		expected bool
	}{
		{
			name: "returns_false_when_update_progress_is_nil",
			pcsg: &grovecorev1beta1.PodCliqueScalingGroup{
				Status: grovecorev1beta1.PodCliqueScalingGroupStatus{
					UpdateProgress: nil,
				},
			},
			expected: false,
		},
		{
			name: "returns_true_when_update_in_progress",
			pcsg: &grovecorev1beta1.PodCliqueScalingGroup{
				Status: grovecorev1beta1.PodCliqueScalingGroupStatus{
					UpdateProgress: &grovecorev1beta1.PodCliqueScalingGroupUpdateProgress{
						UpdateStartedAt: metav1.Now(),
						UpdateEndedAt:   nil, // nil means in progress
					},
				},
			},
			expected: true,
		},
		{
			name: "returns_false_when_update_completed",
			pcsg: &grovecorev1beta1.PodCliqueScalingGroup{
				Status: grovecorev1beta1.PodCliqueScalingGroupStatus{
					UpdateProgress: &grovecorev1beta1.PodCliqueScalingGroupUpdateProgress{
						UpdateStartedAt: metav1.Now(),
						UpdateEndedAt:   ptr.To(metav1.Now()), // set means completed
					},
				},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := IsPCSGUpdateInProgress(tc.pcsg)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestIsPCSGUpdateComplete tests the IsPCSGUpdateComplete function
func TestIsPCSGUpdateComplete(t *testing.T) {
	tests := []struct {
		name           string
		pcsg           *grovecorev1beta1.PodCliqueScalingGroup
		updateRevision string
		expected       bool
	}{
		{
			name:           "returns_false_when_no_revision_is_reached",
			pcsg:           &grovecorev1beta1.PodCliqueScalingGroup{},
			updateRevision: "revision-1",
			expected:       false,
		},
		{
			name: "returns_false_when_the_pcs_has_no_update_revision",
			pcsg: &grovecorev1beta1.PodCliqueScalingGroup{
				Status: grovecorev1beta1.PodCliqueScalingGroupStatus{CurrentPodCliqueSetRevision: ""},
			},
			updateRevision: "",
			expected:       false,
		},
		{
			name: "returns_true_when_revision_matches_and_no_update_progress_exists",
			pcsg: &grovecorev1beta1.PodCliqueScalingGroup{
				Status: grovecorev1beta1.PodCliqueScalingGroupStatus{CurrentPodCliqueSetRevision: "revision-1"},
			},
			updateRevision: "revision-1",
			expected:       true,
		},
		{
			name: "returns_false_when_revision_matches_but_update_is_in_progress",
			pcsg: &grovecorev1beta1.PodCliqueScalingGroup{
				Status: grovecorev1beta1.PodCliqueScalingGroupStatus{
					CurrentPodCliqueSetRevision: "revision-1",
					UpdateProgress: &grovecorev1beta1.PodCliqueScalingGroupUpdateProgress{
						UpdateStartedAt:      metav1.Now(),
						PodCliqueSetRevision: "superseded-revision",
					},
				},
			},
			updateRevision: "revision-1",
			expected:       false,
		},
		{
			name: "returns_true_when_revision_matches_and_update_has_ended",
			pcsg: &grovecorev1beta1.PodCliqueScalingGroup{
				Status: grovecorev1beta1.PodCliqueScalingGroupStatus{
					CurrentPodCliqueSetRevision: "revision-1",
					UpdateProgress: &grovecorev1beta1.PodCliqueScalingGroupUpdateProgress{
						UpdateStartedAt:      metav1.Now(),
						UpdateEndedAt:        ptr.To(metav1.Now()),
						PodCliqueSetRevision: "revision-1",
					},
				},
			},
			updateRevision: "revision-1",
			expected:       true,
		},
		{
			name: "returns_false_when_revision_differs",
			pcsg: &grovecorev1beta1.PodCliqueScalingGroup{
				Status: grovecorev1beta1.PodCliqueScalingGroupStatus{CurrentPodCliqueSetRevision: "old-revision"},
			},
			updateRevision: "new-revision",
			expected:       false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := IsPCSGUpdateComplete(tc.pcsg, tc.updateRevision)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestIsPCSGMemberPCLQUpdated(t *testing.T) {
	newMember := func(labelRevision, reachedRevision string, ready, updated int32) *grovecorev1beta1.PodClique {
		return &grovecorev1beta1.PodClique{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{apicommon.LabelPodCliqueSetRevision: labelRevision}},
			Spec:       grovecorev1beta1.PodCliqueSpec{MinAvailable: ptr.To[int32](1)},
			Status: grovecorev1beta1.PodCliqueStatus{
				CurrentPodCliqueSetRevision: reachedRevision,
				ReadyReplicas:               ready,
				UpdatedReplicas:             updated,
			},
		}
	}
	tests := []struct {
		name     string
		pclq     *grovecorev1beta1.PodClique
		target   string
		expected bool
	}{
		{name: "reached target with MinAvailable updated and ready", pclq: newMember("r1", "r1", 1, 1), target: "r1", expected: true},
		{name: "written from another revision", pclq: newMember("r0", "r1", 1, 1), target: "r1", expected: false},
		{name: "Pods not converged to the target", pclq: newMember("r1", "r0", 1, 1), target: "r1", expected: false},
		{name: "not enough ready Pods", pclq: newMember("r1", "r1", 0, 1), target: "r1", expected: false},
		{name: "not enough updated Pods", pclq: newMember("r1", "r1", 1, 0), target: "r1", expected: false},
		{name: "no target", pclq: newMember("", "", 1, 1), target: "", expected: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, IsPCSGMemberPCLQUpdated(tc.pclq, tc.target))
		})
	}
}

func TestGroupPCSGsByPCSReplicaIndex(t *testing.T) {
	const (
		pcsName   = "pcs"
		namespace = "default"
	)
	tests := []struct {
		name          string
		pcsgs         []grovecorev1beta1.PodCliqueScalingGroup
		expectErr     bool
		expectedIndex map[int]int // replica index -> number of PCSGs in that group
	}{
		{
			name: "groups PodCliqueScalingGroups by replica index",
			pcsgs: []grovecorev1beta1.PodCliqueScalingGroup{
				*testutils.NewPodCliqueScalingGroupBuilder("pcs-0-sga", namespace, pcsName, 0).Build(),
				*testutils.NewPodCliqueScalingGroupBuilder("pcs-0-sgb", namespace, pcsName, 0).Build(),
				*testutils.NewPodCliqueScalingGroupBuilder("pcs-1-sga", namespace, pcsName, 1).Build(),
			},
			expectedIndex: map[int]int{0: 2, 1: 1},
		},
		{
			name: "non-integer replica-index label is an error",
			pcsgs: []grovecorev1beta1.PodCliqueScalingGroup{
				*testutils.NewPodCliqueScalingGroupBuilder("pcs-x-sga", namespace, pcsName, 0).
					WithLabels(map[string]string{apicommon.LabelPodCliqueSetReplicaIndex: "abc"}).Build(),
			},
			expectErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := GroupPCSGsByPCSReplicaIndex(tt.pcsgs)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, actual, len(tt.expectedIndex))
			for idx, count := range tt.expectedIndex {
				assert.Len(t, actual[idx], count)
			}
		})
	}
}

func TestComputePCSGReplicaState(t *testing.T) {
	tests := []struct {
		name            string
		members         []grovecorev1beta1.PodClique
		expectedMembers int
		want            PCSGReplicaState
	}{
		{
			name:            "ready when the only member meets MinAvailable scheduled and ready",
			members:         []grovecorev1beta1.PodClique{pclqWithMinAvailableAndStatus(2, 2, 2)},
			expectedMembers: 1,
			want:            PCSGReplicaStateReady,
		},
		{
			name:            "pending when the only member is below MinAvailable scheduled",
			members:         []grovecorev1beta1.PodClique{pclqWithMinAvailableAndStatus(2, 1, 1)},
			expectedMembers: 1,
			want:            PCSGReplicaStatePending,
		},
		{
			name:            "unavailable when the only member is scheduled but below MinAvailable ready",
			members:         []grovecorev1beta1.PodClique{pclqWithMinAvailableAndStatus(2, 2, 1)},
			expectedMembers: 1,
			want:            PCSGReplicaStateUnavailable,
		},
		{
			name: "pending when any member is below MinAvailable scheduled",
			members: []grovecorev1beta1.PodClique{
				pclqWithMinAvailableAndStatus(2, 2, 2),
				pclqWithMinAvailableAndStatus(2, 1, 1),
			},
			expectedMembers: 2,
			want:            PCSGReplicaStatePending,
		},
		{
			name: "unavailable when a scheduled member is below MinAvailable ready and none is pending",
			members: []grovecorev1beta1.PodClique{
				pclqWithMinAvailableAndStatus(2, 2, 2),
				pclqWithMinAvailableAndStatus(2, 2, 1),
			},
			expectedMembers: 2,
			want:            PCSGReplicaStateUnavailable,
		},
		{
			name: "ready when every member meets MinAvailable",
			members: []grovecorev1beta1.PodClique{
				pclqWithMinAvailableAndStatus(1, 3, 1),
				pclqWithMinAvailableAndStatus(2, 2, 2),
			},
			expectedMembers: 2,
			want:            PCSGReplicaStateReady,
		},
		{
			name:            "pending when a member is absent even though every present member is ready",
			members:         []grovecorev1beta1.PodClique{pclqWithMinAvailableAndStatus(2, 2, 2)},
			expectedMembers: 2,
			want:            PCSGReplicaStatePending,
		},
		{
			name: "pending when a terminating member leaves fewer than expected non-terminating members",
			members: []grovecorev1beta1.PodClique{
				pclqWithMinAvailableAndStatus(2, 2, 2),
				terminatingPCLQ(pclqWithMinAvailableAndStatus(2, 2, 2)),
			},
			expectedMembers: 2,
			want:            PCSGReplicaStatePending,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ComputePCSGReplicaState(tc.members, tc.expectedMembers))
		})
	}
}

func TestOrderPCSGReplicaIndicesForDisruption(t *testing.T) {
	tests := []struct {
		name  string
		infos []PCSGReplicaDisruptionInfo
		want  []int
	}{
		{
			name:  "empty input yields no indices",
			infos: []PCSGReplicaDisruptionInfo{},
			want:  []int{},
		},
		{
			name: "same state orders by ascending index",
			infos: []PCSGReplicaDisruptionInfo{
				{Index: 2, State: PCSGReplicaStateReady},
				{Index: 0, State: PCSGReplicaStateReady},
				{Index: 1, State: PCSGReplicaStateReady},
			},
			want: []int{0, 1, 2},
		},
		{
			name: "worst-off first across states with ascending index within each state",
			infos: []PCSGReplicaDisruptionInfo{
				{Index: 5, State: PCSGReplicaStateReady},
				{Index: 2, State: PCSGReplicaStatePending},
				{Index: 4, State: PCSGReplicaStateUnavailable},
				{Index: 1, State: PCSGReplicaStateReady},
				{Index: 3, State: PCSGReplicaStatePending},
				{Index: 0, State: PCSGReplicaStateUnavailable},
			},
			want: []int{2, 3, 0, 4, 1, 5},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, OrderPCSGReplicaIndicesForDisruption(tc.infos))
		})
	}
}

// pclqWithMinAvailableAndStatus builds a minimal PodClique carrying only the MinAvailable spec and the
// scheduled and ready status counts that ComputePCSGReplicaState reads.
func pclqWithMinAvailableAndStatus(minAvailable, scheduled, ready int32) grovecorev1beta1.PodClique {
	return grovecorev1beta1.PodClique{
		Spec:   grovecorev1beta1.PodCliqueSpec{MinAvailable: ptr.To(minAvailable)},
		Status: grovecorev1beta1.PodCliqueStatus{ScheduledReplicas: scheduled, ReadyReplicas: ready},
	}
}

// terminatingPCLQ marks a PodClique as terminating by setting a deletion timestamp and a finalizer.
func terminatingPCLQ(pclq grovecorev1beta1.PodClique) grovecorev1beta1.PodClique {
	pclq.DeletionTimestamp = ptr.To(metav1.Now())
	pclq.Finalizers = []string{"grove.io/test"}
	return pclq
}

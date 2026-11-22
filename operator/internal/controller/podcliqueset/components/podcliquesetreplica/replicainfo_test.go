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

package podcliquesetreplica

import (
	"testing"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestPCSReplicaInfoRecordedUpdateCompletion checks that recorded completion survives child loss and
// that an unfinished replacement at a reused target index overrides earlier completion records.
func TestPCSReplicaInfoRecordedUpdateCompletion(t *testing.T) {
	t.Parallel()
	completedAt := metav1.NewTime(time.Unix(150, 0).UTC())
	tests := []struct {
		name              string
		pcsReplicaIndex   int
		currentlyUpdating []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress
		want              bool
	}{
		{name: "no recorded update cannot complete a missing replica"},
		{name: "empty recorded update cannot complete a missing replica", currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{}},
		{
			name:              "completed child-resource update survives complete resource loss",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0, UpdateEndedAt: &completedAt}},
			want:              true,
		},
		{
			name: "completed same-index replacement survives complete resource loss",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex: 0, UpdateEndedAt: &completedAt,
				Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0},
			}},
			want: true,
		},
		{
			name: "completed different-index replacement identifies its target", pcsReplicaIndex: 1,
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex: 2, UpdateEndedAt: &completedAt,
				Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
			}},
			want: true,
		},
		{
			name: "completed different-index replacement does not complete its former source", pcsReplicaIndex: 2,
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{
				ReplicaIndex: 2, UpdateEndedAt: &completedAt,
				Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1},
			}},
		},
		{
			name:              "unfinished child-resource update cannot complete a missing replica",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{{ReplicaIndex: 0}},
		},
		{
			name: "unfinished replacement overrides an earlier completed target",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				{ReplicaIndex: 0, UpdateEndedAt: &completedAt, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0}},
				{ReplicaIndex: 2, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0}},
			},
		},
		{
			name: "unfinished replacement overrides a later completed target record",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				{ReplicaIndex: 2, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0}},
				{ReplicaIndex: 0, UpdateEndedAt: &completedAt, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0}},
			},
		},
		{
			name: "unfinished selection for another target does not invalidate completion",
			currentlyUpdating: []grovecorev1beta1.PodCliqueSetReplicaUpdateProgress{
				{ReplicaIndex: 0, UpdateEndedAt: &completedAt, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 0}},
				{ReplicaIndex: 2, Replacement: &grovecorev1beta1.PodCliqueSetReplicaReplacementProgress{TargetReplicaIndex: 1}},
			},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var progress *grovecorev1beta1.PodCliqueSetUpdateProgress
			if tc.currentlyUpdating != nil {
				progress = &grovecorev1beta1.PodCliqueSetUpdateProgress{
					RequiresReplicaRecreation: true,
					CurrentlyUpdating:         tc.currentlyUpdating,
				}
			}
			pcs := testutils.NewPodCliqueSetBuilder("test-pcs", "default", "uid").
				WithStandaloneClique("worker").
				WithPodCliqueSetGenerationHash(new("current")).
				WithUpdateProgress(progress.DeepCopy()).Build()
			before := pcs.DeepCopy()
			info := pcsReplicaInfo{index: tc.pcsReplicaIndex}

			assert.Equal(t, tc.want, info.isUpdateComplete(pcs))
			assert.Equal(t, before, pcs, "completion checks must not mutate progress or timestamps")
		})
	}
}

func TestPCSReplicaInfoUnfinishedRecreationAvailability(t *testing.T) {
	t.Parallel()
	deletingAt := metav1.NewTime(time.Unix(175, 0).UTC())
	tests := []struct {
		name                      string
		requiresReplicaRecreation bool
		includeResources          bool
		deleting                  bool
		want                      bool
	}{
		{name: "recreation waits for missing resources", requiresReplicaRecreation: true},
		{name: "recreation waits for unavailable converged resources", requiresReplicaRecreation: true, includeResources: true, deleting: true},
		{name: "recreation completes with converged available resources", requiresReplicaRecreation: true, includeResources: true, want: true},
		{name: "configured strategy preserves its existing convergence checks", includeResources: true, deleting: true, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := rollingUpdateTestPCS()
			pcs.Status.UpdateProgress.RequiresReplicaRecreation = tc.requiresReplicaRecreation
			info := pcsReplicaInfo{index: 0}
			if tc.includeResources {
				info = rollingUpdateReplicaInfo(pcs, 0, true)
				if tc.deleting {
					info.standalonePCLQs[0].DeletionTimestamp = &deletingAt
				}
			}
			assert.Equal(t, tc.want, info.isUpdateComplete(pcs))
		})
	}
}

func TestHasMaterializedPCSGeneration(t *testing.T) {
	cases := []struct {
		name              string
		pclqMarker        string
		pcsgMarker        string
		includePCLQ       bool
		includePCSG       bool
		hash              string
		want              bool
		wantExistingSpecs bool
	}{
		{name: "both templates applied before either status converges", pclqMarker: "S1", pcsgMarker: "S1", includePCLQ: true, includePCSG: true, hash: "S1", want: true, wantExistingSpecs: true},
		{name: "old standalone spec", pclqMarker: "S0", pcsgMarker: "S1", includePCLQ: true, includePCSG: true, hash: "S1"},
		{name: "old group spec despite current members", pclqMarker: "S1", pcsgMarker: "S0", includePCLQ: true, includePCSG: true, hash: "S1"},
		{name: "unknown standalone marker", pcsgMarker: "S1", includePCLQ: true, includePCSG: true, hash: "S1"},
		{name: "unknown group marker", pclqMarker: "S1", includePCLQ: true, includePCSG: true, hash: "S1"},
		{name: "missing standalone", pcsgMarker: "S1", includePCSG: true, hash: "S1", wantExistingSpecs: true},
		{name: "missing group", pclqMarker: "S1", includePCLQ: true, hash: "S1", wantExistingSpecs: true},
		{name: "no resources", hash: "S1", wantExistingSpecs: true},
		{name: "unknown target cannot match absent markers", includePCLQ: true, includePCSG: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcs := replicaRecreationSelectionTestPCS()
			info := replicaRecreationSelectionInfo(pcs, 0, "recorded-status", tc.includePCLQ, tc.includePCSG)
			for i := range info.standalonePCLQs {
				info.standalonePCLQs[i].Labels[apicommon.LabelPodCliqueSetGenerationHash] = tc.pclqMarker
				info.standalonePCLQs[i].Status = grovecorev1beta1.PodCliqueStatus{}
			}
			for i := range info.pcsgs {
				info.pcsgs[i].Labels[apicommon.LabelPodCliqueSetGenerationHash] = tc.pcsgMarker
				info.pcsgs[i].Status = grovecorev1beta1.PodCliqueScalingGroupStatus{}
			}
			assert.Equal(t, tc.want, info.hasMaterializedPCSGeneration(pcs, tc.hash))
			assert.Equal(t, tc.wantExistingSpecs, info.haveObservedResourcesAppliedPCSGeneration(tc.hash))
			assert.False(t, info.hasConvergedToPCSGeneration(pcs, tc.hash), "materialization cannot imply completion")
		})
	}
}

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
	"context"
	"fmt"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// reconcileReplicasToCommittedPodGangs realizes the PodGangMap committed placement for a
// PodCliqueScalingGroup under a coherent update. It recreates any replica whose members are not on the
// PodGang the PodGangMap assigns them, so they come back at the current revision on the committed
// PodGang. Pacing and the disruption budget belong to the PodGangMap engine, so this does no hash
// comparison and no local budget.
func (r _resource) reconcileReplicasToCommittedPodGangs(ctx context.Context, logger logr.Logger, ss *syncSnapshot) error {
	replicaIndicesToRecreate, err := replicaIndicesNotOnCommittedPodGang(ss)
	if err != nil {
		return err
	}
	if len(replicaIndicesToRecreate) == 0 {
		return nil
	}

	deleteTasks := r.createDeleteTasks(logger, ss, replicaIndicesToRecreate, "recreating replicas onto their PodGangMap committed PodGang under a coherent update")
	if err := r.triggerDeletionOfPodCliques(ctx, logger, client.ObjectKeyFromObject(ss.pcsg), deleteTasks); err != nil {
		return err
	}
	logger.Info("Recreating PodCliqueScalingGroup replicas onto their committed PodGang", "pcsg", client.ObjectKeyFromObject(ss.pcsg), "replicaIndices", replicaIndicesToRecreate)
	return groveerr.New(
		groveerr.ErrCodeContinueReconcileAndRequeue,
		component.OperationSync,
		fmt.Sprintf("recreating %d replica(s) of PodCliqueScalingGroup %v onto their committed PodGang, requeuing", len(replicaIndicesToRecreate), client.ObjectKeyFromObject(ss.pcsg)),
	)
}

// replicaIndicesNotOnCommittedPodGang returns the replica indices whose member PodCliques are not on the
// PodGang the PodGangMap has committed the replica to. A replica with no members is skipped, since
// createExpectedPCLQs creates it directly on its committed PodGang.
func replicaIndicesNotOnCommittedPodGang(ss *syncSnapshot) ([]string, error) {
	rnr := apicommon.ResourceNameReplica{Name: ss.pcs.Name, Replica: ss.pcsReplicaIndex}
	membersByReplicaIndex := componentutils.GroupPCLQsByPCSGReplicaIndex(ss.existingPCLQs)

	var replicaIndicesToRecreate []string
	for pcsgReplicaIndex := range int(ss.pcsg.Spec.Replicas) {
		members := membersByReplicaIndex[strconv.Itoa(pcsgReplicaIndex)]
		if len(members) == 0 {
			continue
		}
		committedPodGangName, err := resolvePodGangName(ss.pgm, rnr, ss.pcsg, int32(pcsgReplicaIndex))
		if err != nil {
			return nil, groveerr.WrapError(err,
				errCodeReconcileReplicaPlacement,
				component.OperationSync,
				fmt.Sprintf("failed to resolve committed PodGang for replica %d of PodCliqueScalingGroup %v", pcsgReplicaIndex, client.ObjectKeyFromObject(ss.pcsg)),
			)
		}
		if !allMembersOnPodGang(members, committedPodGangName) {
			replicaIndicesToRecreate = append(replicaIndicesToRecreate, strconv.Itoa(pcsgReplicaIndex))
		}
	}
	return replicaIndicesToRecreate, nil
}

// allMembersOnPodGang reports whether every member PodClique carries podGangName on its grove.io/podgang
// label. Every member of one PodCliqueScalingGroup replica shares a single PodGang.
func allMembersOnPodGang(members []grovecorev1alpha1.PodClique, podGangName string) bool {
	return lo.EveryBy(members, func(pclq grovecorev1alpha1.PodClique) bool {
		return pclq.Labels[apicommon.LabelPodGang] == podGangName
	})
}

// markCoherentUpdateEndIfConverged ends the PodCliqueScalingGroup update once every replica is on its
// committed PodGang at the current revision and Ready. It is readiness aware because a replica on a
// superseded PodGang cannot become Ready, so the update stays in progress until placement settles.
func (r _resource) markCoherentUpdateEndIfConverged(ctx context.Context, logger logr.Logger, ss *syncSnapshot) error {
	membersByReplicaIndex := componentutils.GroupPCLQsByPCSGReplicaIndex(ss.existingPCLQs)
	for pcsgReplicaIndex := range int(ss.pcsg.Spec.Replicas) {
		if !isReplicaUpdatedAndReady(ss, pcsgReplicaIndex, membersByReplicaIndex[strconv.Itoa(pcsgReplicaIndex)]) {
			return nil
		}
	}
	return r.markUpdateEnd(ctx, logger, ss.pcsg)
}

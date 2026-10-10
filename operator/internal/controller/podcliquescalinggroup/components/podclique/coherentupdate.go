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
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

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
	indicesToRecreate, err := replicaIndicesToRecreate(ss)
	if err != nil {
		return err
	}
	if len(indicesToRecreate) == 0 {
		return nil
	}

	deleteTasks := r.createDeleteTasks(logger, ss, indicesToRecreate, "recreating replicas onto their PodGangMap committed PodGang under a coherent update")
	if err := r.triggerDeletionOfPodCliques(ctx, logger, client.ObjectKeyFromObject(ss.pcsg), deleteTasks); err != nil {
		return err
	}
	logger.Info("Recreating PodCliqueScalingGroup replicas onto their committed PodGang", "pcsg", client.ObjectKeyFromObject(ss.pcsg), "replicaIndices", indicesToRecreate)
	return groveerr.New(
		groveerr.ErrCodeContinueReconcileAndRequeue,
		component.OperationSync,
		fmt.Sprintf("recreating %d replica(s) of PodCliqueScalingGroup %v onto their committed PodGang, requeuing", len(indicesToRecreate), client.ObjectKeyFromObject(ss.pcsg)),
	)
}

// replicaIndicesToRecreate returns the replica indices whose live member PodCliques are not on the
// PodGang the PodGangMap has committed the replica to and must be deleted so they come back on it. A
// replica with no members is omitted, since createExpectedPCLQs creates it on its committed PodGang. A
// replica with any terminating member is omitted too, so a member that already came back on the
// committed PodGang is not deleted again while a slower sibling is still draining.
func replicaIndicesToRecreate(ss *syncSnapshot) ([]string, error) {
	rnr := apicommon.ResourceNameReplica{Name: ss.pcs.Name, Replica: ss.pcsReplicaIndex}
	membersByReplicaIndex := componentutils.GroupPCLQsByPCSGReplicaIndex(ss.existingPCLQs)

	var indicesToRecreate []string
	for pcsgReplicaIndex := range int(ss.pcsg.Spec.Replicas) {
		members := membersByReplicaIndex[strconv.Itoa(pcsgReplicaIndex)]
		if len(members) == 0 || anyMemberTerminating(members) {
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
			indicesToRecreate = append(indicesToRecreate, strconv.Itoa(pcsgReplicaIndex))
		}
	}
	return indicesToRecreate, nil
}

// allMembersOnPodGang reports whether every member PodClique carries podGangName on its grove.io/podgang
// label. Every member of one PodCliqueScalingGroup replica shares a single PodGang.
func allMembersOnPodGang(members []grovecorev1alpha1.PodClique, podGangName string) bool {
	return lo.EveryBy(members, func(pclq grovecorev1alpha1.PodClique) bool {
		return pclq.Labels[apicommon.LabelPodGang] == podGangName
	})
}

// anyMemberTerminating reports whether any member PodClique of a PodCliqueScalingGroup replica has a
// deletion timestamp, so the replica is draining from a prior recreation.
func anyMemberTerminating(members []grovecorev1alpha1.PodClique) bool {
	return lo.SomeBy(members, func(pclq grovecorev1alpha1.PodClique) bool {
		return k8sutils.IsResourceTerminating(pclq.ObjectMeta)
	})
}

// markCoherentUpdateEndIfConverged ends the PodCliqueScalingGroup update once every replica is on its
// committed PodGang at the current revision and Ready. It is readiness aware because a replica on a
// superseded PodGang cannot become Ready, so the update stays in progress until placement settles.
func (r _resource) markCoherentUpdateEndIfConverged(ctx context.Context, logger logr.Logger, ss *syncSnapshot) error {
	membersByReplicaIndex := componentutils.GroupPCLQsByPCSGReplicaIndex(ss.existingPCLQs)
	for pcsgReplicaIndex := range int(ss.pcsg.Spec.Replicas) {
		if !isReplicaUpdatedAndReady(ss, pcsgReplicaIndex, membersByReplicaIndex[strconv.Itoa(pcsgReplicaIndex)]) {
			// The update has not converged. A replica is still draining, pending recreation, or not yet
			// Ready. Requeue so the roll is re-driven until every replica settles on its committed PodGang
			// at the current revision, rather than returning without rescheduling.
			return groveerr.New(
				groveerr.ErrCodeContinueReconcileAndRequeue,
				component.OperationSync,
				fmt.Sprintf("coherent update of PodCliqueScalingGroup %v has not converged, requeuing", client.ObjectKeyFromObject(ss.pcsg)),
			)
		}
	}
	return r.markUpdateEnd(ctx, logger, ss.pcsg)
}

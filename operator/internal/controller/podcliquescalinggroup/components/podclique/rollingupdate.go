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
	"context"
	"fmt"
	"slices"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	pcsgexpectations "github.com/ai-dynamo/grove/operator/internal/controller/podcliquescalinggroup/expectations"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// updateWork categorizes a PodCliqueScalingGroup's replicas for a rolling update and captures the
// counts that drive the disruption budget and the completion check. A replica is a group of member
// PodCliques at a given index, and is Ready only when every member has at least MinAvailable Ready
// Pods.
type updateWork struct {
	// oldReadyReplicaIndices are old-configuration replicas that are Ready and not already being
	// deleted. They are the candidates for replacement, disrupted lowest index first within the budget.
	oldReadyReplicaIndices []int
	// oldPendingReplicaIndices are old-configuration replicas that are not yet scheduled.
	oldPendingReplicaIndices []int
	// oldUnavailableReplicaIndices are old-configuration replicas that are scheduled but not Ready.
	oldUnavailableReplicaIndices []int
	// existingReplicas is every present, non-terminating replica slot whose deletion has not already been
	// triggered: ready and not-ready, old-configuration and new-configuration. It anchors the disruption
	// budget to the live replica count rather than a readiness delta.
	existingReplicas int
	// newNotReadyReplicas is the number of new-configuration replicas that have not finished rolling out
	// and become Ready (in-flight replacements). It is subtracted from the disruption budget so a
	// reconcile does not disrupt more than desired - effectiveMaxUnavailable allows.
	newNotReadyReplicas int
	// numUpdatedReadyReplicas is the number of replicas that are fully updated to the expected
	// configuration and Ready. The rolling update is complete only when this reaches the desired count.
	numUpdatedReadyReplicas int
}

type replicaState int

const (
	replicaStatePending replicaState = iota
	replicaStateUnAvailable
	replicaStateReady
)

// processPendingUpdates advances the rolling update of a PodCliqueScalingGroup by one reconcile step.
//
// It replaces old-configuration replicas worst-off first (pending, then unavailable, then Ready), up to
// the disruption budget (see computeDisruptionBudget). The update completes only when the
// desired number of replicas are fully updated and Ready.
func (r _resource) processPendingUpdates(ctx context.Context, logger logr.Logger, sc *syncSnapshot) error {
	uw, err := r.computePendingUpdateWork(sc)
	if err != nil {
		return groveerr.WrapError(err,
			errCodeComputePendingPodCliqueScalingGroupUpdateWork,
			component.OperationSync,
			fmt.Sprintf("failed to compute pending update work for PodCliqueScalingGroup %v", client.ObjectKeyFromObject(sc.pcsg)))
	}

	desiredNumReplicas := int(sc.pcsg.Spec.Replicas)

	// Completion is readiness-aware. End the update only when the desired number of replicas are fully
	// updated and Ready, so a rollout never completes while replacements are not yet available.
	if uw.numUpdatedReadyReplicas == desiredNumReplicas {
		return r.markRollingUpdateEnd(ctx, logger, sc.pcsg)
	}

	// Bound disruption against the live replica count and the minimum that must stay available, not a
	// readiness delta, so the budget does not collapse to 0 when replicas are already unavailable.
	effectiveMaxUnavailable := componentutils.EffectiveMaxUnavailable(rollingUpdateConfigForPCSG(sc))
	disruptionBudget := computeDisruptionBudget(uw.existingReplicas, desiredNumReplicas, effectiveMaxUnavailable, uw.newNotReadyReplicas)
	if disruptionBudget <= 0 {
		return groveerr.New(
			groveerr.ErrCodeContinueReconcileAndRequeue,
			component.OperationSync,
			fmt.Sprintf("rolling update of PodCliqueScalingGroup %v has no disruption headroom this reconcile (availability floor or in-flight replacements), re-queuing", client.ObjectKeyFromObject(sc.pcsg)),
		)
	}

	// Order old-configuration replicas worst-off first: pending, then unavailable, then Ready. Each
	// slice is already in ascending replica-index order.
	replicaIndicesToUpdate := slices.Concat(uw.oldPendingReplicaIndices, uw.oldUnavailableReplicaIndices, uw.oldReadyReplicaIndices)
	if len(replicaIndicesToUpdate) == 0 {
		// Every old-configuration replica is an in-flight replacement. Requeue and wait for them to become Ready.
		return groveerr.New(
			groveerr.ErrCodeContinueReconcileAndRequeue,
			component.OperationSync,
			fmt.Sprintf("rolling update of PodCliqueScalingGroup %v waiting for in-flight replacement replicas to become Ready, requeuing", client.ObjectKeyFromObject(sc.pcsg)),
		)
	}

	replicaIndicesToUpdate = replicaIndicesToUpdate[:min(disruptionBudget, len(replicaIndicesToUpdate))]
	replicaIndicesToUpdateStr := lo.Map(replicaIndicesToUpdate, func(index int, _ int) string {
		return strconv.Itoa(index)
	})
	logger.Info("triggering deletion of old-configuration replicas for rolling update", "replicaIndices", replicaIndicesToUpdate)
	deleteTasks := r.createDeleteTasks(logger, sc, replicaIndicesToUpdateStr, "deleting old-configuration replicas for rolling update")
	if err := r.triggerDeletionOfPodCliques(ctx, logger, client.ObjectKeyFromObject(sc.pcsg), deleteTasks); err != nil {
		return err
	}
	return groveerr.New(
		groveerr.ErrCodeContinueReconcileAndRequeue,
		component.OperationSync,
		fmt.Sprintf("deleted %d replica(s) for rolling update of PodCliqueScalingGroup %v, requeuing", len(replicaIndicesToUpdate), client.ObjectKeyFromObject(sc.pcsg)),
	)
}

// rollingUpdateConfigForPCSG returns the RollingUpdate configuration for the PodCliqueScalingGroup
// from its PodCliqueSet config, or nil when the config or the field is absent.
func rollingUpdateConfigForPCSG(sc *syncSnapshot) *grovecorev1alpha1.RollingUpdateConfiguration {
	if sc.pcsgConfig == nil {
		return nil
	}
	return sc.pcsgConfig.RollingUpdate
}

// computeDisruptionBudget returns how many old-configuration replicas may be disrupted this reconcile.
// It is the live replica count minus the number that must stay available (desired -
// effectiveMaxUnavailable) minus the in-flight new-but-not-ready replicas, so it does not collapse to 0
// when replicas are already unavailable while still capping how many are taken down at once. It may be
// negative; callers treat a value <= 0 as no headroom.
func computeDisruptionBudget(existing, desired, effectiveMaxUnavailable, newNotReady int) int {
	return existing - (desired - effectiveMaxUnavailable) - newNotReady
}

// markRollingUpdateEnd finalizes the rolling update by setting the end timestamp.
func (r _resource) markRollingUpdateEnd(ctx context.Context, logger logr.Logger, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) error {
	patch := client.MergeFrom(pcsg.DeepCopy())

	pcsg.Status.UpdateProgress.UpdateEndedAt = ptr.To(metav1.Now())

	if err := r.client.Status().Patch(ctx, pcsg, patch); err != nil {
		return groveerr.WrapError(
			err,
			errCodeUpdateStatus,
			component.OperationSync,
			fmt.Sprintf("failed to mark end of rolling update in status of PodCliqueScalingGroup: %v", client.ObjectKeyFromObject(pcsg)),
		)
	}
	logger.Info("Marked the end of rolling update of PodCliqueScalingGroup")
	return groveerr.New(
		groveerr.ErrCodeContinueReconcileAndRequeue,
		component.OperationSync,
		fmt.Sprintf("rolling update of PodCliqueScalingGroup %v has ended, requeuing for status convergence", client.ObjectKeyFromObject(pcsg)),
	)
}

// computePendingUpdateWork categorizes replicas by configuration and Ready state and records the
// counts that drive the disruption budget and the completion check.
func (r _resource) computePendingUpdateWork(ss *syncSnapshot) (*updateWork, error) {
	uw := &updateWork{}
	existingPCLQsByReplicaIndex := componentutils.GroupPCLQsByPCSGReplicaIndex(ss.existingPCLQs)
	pcsgexpectations.SyncPCSGReplicaDeleteExpectations(r.expectationsStore, ss.expectationsStoreKey, ss.existingPCLQs)
	for pcsgReplicaIndex := range int(ss.pcsg.Spec.Replicas) {
		memberPCLQs := existingPCLQsByReplicaIndex[strconv.Itoa(pcsgReplicaIndex)]

		// A replica with no PodCliques, all terminating, or whose disruption we already triggered
		// (delete expectation recorded, cache not yet caught up) is mid-replacement: not a live replica
		// and not a disruption candidate.
		if len(memberPCLQs) == 0 || allPodCliquesTerminating(memberPCLQs) || pcsgexpectations.HasPCSGReplicaDisruptionBeenTriggered(r.expectationsStore, ss.expectationsStoreKey, memberPCLQs) {
			continue
		}
		uw.existingReplicas++

		labeled, err := isReplicaLabeledWithExpectedHash(ss, memberPCLQs)
		if err != nil {
			return nil, err
		}
		state := getReplicaState(memberPCLQs)
		if !labeled {
			// Old configuration: a replacement candidate, grouped by state.
			switch state {
			case replicaStatePending:
				uw.oldPendingReplicaIndices = append(uw.oldPendingReplicaIndices, pcsgReplicaIndex)
			case replicaStateUnAvailable:
				uw.oldUnavailableReplicaIndices = append(uw.oldUnavailableReplicaIndices, pcsgReplicaIndex)
			case replicaStateReady:
				uw.oldReadyReplicaIndices = append(uw.oldReadyReplicaIndices, pcsgReplicaIndex)
			}
			continue
		}

		// New configuration: done once its rollout is confirmed and it is Ready, otherwise an in-flight
		// replacement that blocks completion and reduces the disruption budget.
		if isReplicaUpdated(ss, pcsgReplicaIndex, memberPCLQs) && state == replicaStateReady {
			uw.numUpdatedReadyReplicas++
		} else {
			uw.newNotReadyReplicas++
		}
	}
	return uw, nil
}

// isReplicaLabeledWithExpectedHash reports whether every member PodClique carries the expected pod
// template hash label, i.e. the replica already holds the new configuration. This is the old-vs-new
// discriminator and is intentionally label-only: a freshly recreated replica carries the label
// immediately while its status hashes lag, so a status-based check would misclassify it as old and
// re-disrupt it. It returns ErrMissingPodTemplateHashLabel when a member is missing the label, since a
// managed PodClique must always carry it and its absence is a malformed state, not an old replica.
func isReplicaLabeledWithExpectedHash(sc *syncSnapshot, members []grovecorev1alpha1.PodClique) (bool, error) {
	for _, pclq := range members {
		podTemplateHash, ok := pclq.Labels[apicommon.LabelPodTemplateHash]
		if !ok {
			return false, groveerr.ErrMissingPodTemplateHashLabel
		}
		if podTemplateHash != sc.expectedPCLQPodTemplateHashMap[pclq.Name] {
			return false, nil
		}
	}
	return true, nil
}

// isReplicaUpdated reports whether an already labeled replica has confirmed its rollout: every expected
// member exists and its status reports the expected pod template and PodCliqueSet generation with at
// least MinAvailable updated Pods. Call only for replicas isReplicaLabeledWithExpectedHash accepts.
func isReplicaUpdated(sc *syncSnapshot, replicaIndex int, members []grovecorev1alpha1.PodClique) bool {
	if len(sc.expectedPCLQFQNsPerPCSGReplica[replicaIndex]) != len(members) {
		return false
	}
	return lo.EveryBy(members, func(pclq grovecorev1alpha1.PodClique) bool {
		expectedPodTemplateHash := sc.expectedPCLQPodTemplateHashMap[pclq.Name]
		return expectedPodTemplateHash != "" &&
			pclq.Status.CurrentPodTemplateHash != nil && *pclq.Status.CurrentPodTemplateHash == expectedPodTemplateHash &&
			sc.pcs.Status.CurrentGenerationHash != nil &&
			pclq.Status.CurrentPodCliqueSetGenerationHash != nil && *pclq.Status.CurrentPodCliqueSetGenerationHash == *sc.pcs.Status.CurrentGenerationHash &&
			pclq.Status.UpdatedReplicas >= *pclq.Spec.MinAvailable
	})
}

// allPodCliquesTerminating reports whether every member PodClique of a replica is terminating.
func allPodCliquesTerminating(pcsgReplicaPCLQs []grovecorev1alpha1.PodClique) bool {
	return lo.EveryBy(pcsgReplicaPCLQs, func(pclq grovecorev1alpha1.PodClique) bool {
		return k8sutils.IsResourceTerminating(pclq.ObjectMeta)
	})
}

// getReplicaState determines the overall state of a PCSG replica based on its constituent PodCliques.
func getReplicaState(pcsgReplicaPCLQs []grovecorev1alpha1.PodClique) replicaState {
	for _, pclq := range pcsgReplicaPCLQs {
		if pclq.Status.ScheduledReplicas < *pclq.Spec.MinAvailable {
			return replicaStatePending
		}
		if pclq.Status.ReadyReplicas < *pclq.Spec.MinAvailable {
			return replicaStateUnAvailable
		}
	}
	return replicaStateReady
}

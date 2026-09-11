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
	// numReadyReplicas is the number of Ready replicas of either configuration that are not being
	// deleted. These are the replicas counted against MinAvailable and the budget.
	numReadyReplicas int
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
// It selects old-configuration replicas to replace worst-off first (pending, then unavailable, then
// Ready) and disrupts a bounded number of them, honoring the MaxUnavailable budget, so they are
// recreated with the expected configuration. Every disruption is bounded by the budget, so a replica
// is never replaced based on a stale not-Ready read of a member PodClique's status. The update
// completes only when the desired number of replicas are fully updated and Ready.
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

	// Order old-configuration replicas worst-off first: pending, then unavailable, then Ready. Each
	// slice is already in ascending replica-index order. This mirrors the PodCliqueSet-level
	// orderPCSReplicaInfo selection.
	replicaIndicesToUpdate := slices.Concat(uw.oldPendingReplicaIndices, uw.oldUnavailableReplicaIndices, uw.oldReadyReplicaIndices)
	if len(replicaIndicesToUpdate) == 0 {
		// All old-configuration replicas are in-flight replacements. Requeue and wait for them to become Ready.
		return groveerr.New(
			groveerr.ErrCodeContinueReconcileAndRequeue,
			component.OperationSync,
			fmt.Sprintf("rolling update of PodCliqueScalingGroup %v waiting for in-flight replacement replicas to become Ready, requeuing", client.ObjectKeyFromObject(sc.pcsg)),
		)
	}

	// Compute the disruption budget against the current desired replica count. Every disruption is
	// bounded by this budget, so when a member PodClique's readiness status is stale (numReadyReplicas
	// under-reported) the budget shrinks and the rollout waits, instead of replacing replicas that may
	// actually be Ready.
	effectiveMaxUnavailable := componentutils.EffectiveMaxUnavailable(rollingUpdateConfigForPCSG(sc))
	allowedBudget := componentutils.ComputeAllowedBudget(desiredNumReplicas, uw.numReadyReplicas, effectiveMaxUnavailable)
	if allowedBudget == 0 {
		return groveerr.New(
			groveerr.ErrCodeContinueReconcileAndRequeue,
			component.OperationSync,
			fmt.Sprintf("rolling update of PodCliqueScalingGroup %v paused, disruption budget exhausted, requeuing", client.ObjectKeyFromObject(sc.pcsg)),
		)
	}

	replicaIndicesToUpdate = replicaIndicesToUpdate[:min(allowedBudget, len(replicaIndicesToUpdate))]
	replicaIndicesToUpdateStr := lo.Map(replicaIndicesToUpdate, func(index int, _ int) string {
		return strconv.Itoa(index)
	})
	logger.Info("triggering deletion of old-configuration replicas for rolling update", "replicaIndices", replicaIndicesToUpdate)
	deleteTasks := r.createDeleteTasks(logger, sc, replicaIndicesToUpdateStr, "deleting old-configuration replicas for rolling update")
	if err = r.triggerDeletionOfPodCliques(ctx, logger, client.ObjectKeyFromObject(sc.pcsg), deleteTasks); err != nil {
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
		members := existingPCLQsByReplicaIndex[strconv.Itoa(pcsgReplicaIndex)]

		// A replica with no PodCliques, all terminating, or whose disruption we already triggered
		// (delete expectation recorded, cache not yet caught up) is mid-replacement: neither Ready nor
		// a disruption candidate.
		if len(members) == 0 || allPodCliquesTerminating(members) || pcsgexpectations.HasPCSGReplicaDisruptionBeenTriggered(r.expectationsStore, ss.expectationsStoreKey, members) {
			continue
		}

		state := getReplicaState(members)
		if state == replicaStateReady {
			uw.numReadyReplicas++
		}

		if isReplicaUpdatedAndReady(ss, pcsgReplicaIndex, members) {
			uw.numUpdatedReadyReplicas++
			continue
		}

		isUpdated, err := isReplicaUpdated(ss.expectedPCLQPodTemplateHashMap, members)
		if err != nil {
			return nil, err
		}
		if isUpdated {
			// New configuration but not yet fully Ready. It blocks completion but is not a candidate.
			continue
		}

		// Old configuration. Non-Ready replicas are deleted immediately; Ready ones are queued for
		// budgeted replacement.
		switch state {
		case replicaStatePending:
			uw.oldPendingReplicaIndices = append(uw.oldPendingReplicaIndices, pcsgReplicaIndex)
		case replicaStateUnAvailable:
			uw.oldUnavailableReplicaIndices = append(uw.oldUnavailableReplicaIndices, pcsgReplicaIndex)
		case replicaStateReady:
			uw.oldReadyReplicaIndices = append(uw.oldReadyReplicaIndices, pcsgReplicaIndex)
		}
	}
	return uw, nil
}

// isReplicaUpdatedAndReady reports whether every expected member PodClique of the replica exists,
// carries the expected pod template hash and PodCliqueSet generation hash, and has at least
// MinAvailable updated and Ready Pods.
func isReplicaUpdatedAndReady(sc *syncSnapshot, replicaIndex int, members []grovecorev1alpha1.PodClique) bool {
	expectedPCLQFQNs := sc.expectedPCLQFQNsPerPCSGReplica[replicaIndex]
	if len(expectedPCLQFQNs) != len(members) {
		return false
	}
	return lo.EveryBy(members, func(pclq grovecorev1alpha1.PodClique) bool {
		expectedPodTemplateHash := sc.expectedPCLQPodTemplateHashMap[pclq.Name]
		return expectedPodTemplateHash != "" &&
			pclq.Labels[apicommon.LabelPodTemplateHash] == expectedPodTemplateHash &&
			pclq.Status.CurrentPodTemplateHash != nil && *pclq.Status.CurrentPodTemplateHash == expectedPodTemplateHash &&
			sc.pcs.Status.CurrentGenerationHash != nil &&
			pclq.Status.CurrentPodCliqueSetGenerationHash != nil && *pclq.Status.CurrentPodCliqueSetGenerationHash == *sc.pcs.Status.CurrentGenerationHash &&
			pclq.Status.UpdatedReplicas >= *pclq.Spec.MinAvailable &&
			pclq.Status.ReadyReplicas >= *pclq.Spec.MinAvailable
	})
}

// isReplicaUpdated checks if all PodCliques in a PCSG replica have the expected pod template hash.
func isReplicaUpdated(expectedPCLQPodTemplateHashes map[string]string, pcsgReplicaPCLQs []grovecorev1alpha1.PodClique) (bool, error) {
	for _, pclq := range pcsgReplicaPCLQs {
		podTemplateHash, ok := pclq.Labels[apicommon.LabelPodTemplateHash]
		if !ok {
			return false, groveerr.ErrMissingPodTemplateHashLabel
		}
		if podTemplateHash != expectedPCLQPodTemplateHashes[pclq.Name] {
			return false, nil
		}
	}
	return true, nil
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

// /*
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
// */

package podcliquesetreplica

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	apiconstants "github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	"github.com/ai-dynamo/grove/operator/internal/utils"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// deletionWork captures the PodCliqueSet replica deletion work.
type deletionWork struct {
	// deletionTasks are a slice of PodCliqueSet replica deletion tasks. These are the replicas where there is at least
	// one child resource whose MinAvailableBreached condition is set to true and TerminationDelay has expired.
	deletionTasks []utils.Task
	// pcsIndicesToTerminate are the PodCliqueSet replica indices for which one or more constituent standalone PCLQ or PCSG
	// have MinAvailableBreached condition set to true for a duration greater than TerminationDelay.
	pcsIndicesToTerminate []int
	// minAvailableBreachedConstituents is map of PCS replica index to PCLQ FQNs which have MinAvailableBreached condition
	// set to true but for these PCLQs TerminationDelay has not expired yet. If there is at least one such PCLQ then
	// a requeue should be done and the reconciler should re-check if the TerminationDelay for these PCLQs eventually expires
	// at which point the corresponding PCS replica should be deleted.
	minAvailableBreachedConstituents map[int][]string
}

// shouldRequeue returns true if there are constituents with MinAvailable breached but termination delay not expired.
func (d deletionWork) shouldRequeue() bool {
	return len(d.minAvailableBreachedConstituents) > 0
}

// hasPendingPCSReplicaDeletion returns true if there are replica deletion tasks ready to execute.
func (d deletionWork) hasPendingPCSReplicaDeletion() bool {
	return len(d.deletionTasks) > 0
}

// getPCSReplicaDeletionWork identifies PCS replicas that need termination due to MinAvailable breaches.
func (r _resource) getPCSReplicaDeletionWork(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) (*deletionWork, error) {
	var (
		now              = time.Now()
		pcsObjectKey     = client.ObjectKeyFromObject(pcs)
		terminationDelay = pcs.Spec.Template.TerminationDelay.Duration
		deletionTasks    = make([]utils.Task, 0, pcs.Spec.Replicas)
		work             = &deletionWork{
			minAvailableBreachedConstituents: make(map[int][]string),
		}
	)

	for pcsReplicaIndex := range int(pcs.Spec.Replicas) {
		breachedPCSGNames, minPCSGWaitFor, err := r.getMinAvailableBreachedPCSGs(ctx, pcsObjectKey, pcsReplicaIndex, terminationDelay, now)
		if err != nil {
			return nil, err
		}
		breachedPCLQNames, minPCLQWaitFor, skipPCSReplicaIndex, err := r.getMinAvailableBreachedPCLQsNotInPCSG(ctx, logger, pcs, pcsReplicaIndex, now)
		if err != nil {
			return nil, err
		}
		if skipPCSReplicaIndex {
			continue
		}
		if (len(breachedPCSGNames) > 0 && minPCSGWaitFor <= 0) ||
			(len(breachedPCLQNames) > 0 && minPCLQWaitFor <= 0) {
			// terminate all PodCliques for this PCS replica index
			reason := fmt.Sprintf("Delete all PodCliques for PodCliqueSet %v with replicaIndex :%d due to MinAvailable breached longer than TerminationDelay: %s", pcsObjectKey, pcsReplicaIndex, terminationDelay)
			pclqGangTerminationTask := r.createPCSReplicaDeleteTask(logger, pcs, pcsReplicaIndex, reason)
			deletionTasks = append(deletionTasks, pclqGangTerminationTask)
			work.pcsIndicesToTerminate = append(work.pcsIndicesToTerminate, pcsReplicaIndex)
		} else if len(breachedPCSGNames) > 0 || len(breachedPCLQNames) > 0 {
			work.minAvailableBreachedConstituents[pcsReplicaIndex] = append(work.minAvailableBreachedConstituents[pcsReplicaIndex], breachedPCLQNames...)
			work.minAvailableBreachedConstituents[pcsReplicaIndex] = append(work.minAvailableBreachedConstituents[pcsReplicaIndex], breachedPCSGNames...)
		}
	}
	work.deletionTasks = deletionTasks
	return work, nil
}

// getMinAvailableBreachedPCSGs retrieves PCSGs that have breached MinAvailable for a PCS replica.
func (r _resource) getMinAvailableBreachedPCSGs(ctx context.Context, pcsObjKey client.ObjectKey, pcsReplicaIndex int, terminationDelay time.Duration, since time.Time) ([]string, time.Duration, error) {
	pcsgList := &grovecorev1alpha1.PodCliqueScalingGroupList{}
	if err := r.client.List(ctx,
		pcsgList,
		client.InNamespace(pcsObjKey.Namespace),
		client.MatchingLabels(lo.Assign(
			apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcsObjKey.Name),
			map[string]string{
				apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(pcsReplicaIndex),
			},
		)),
	); err != nil {
		return nil, 0, err
	}
	breachedPCSGNames, minWaitFor := getMinAvailableBreachedPCSGInfo(pcsgList.Items, terminationDelay, since)
	return breachedPCSGNames, minWaitFor, nil
}

// getMinAvailableBreachedPCLQsNotInPCSG retrieves standalone PCLQs that have breached MinAvailable.
func (r _resource) getMinAvailableBreachedPCLQsNotInPCSG(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int, since time.Time) (breachedPCLQNames []string, minWaitFor time.Duration, skipPCSReplica bool, err error) {
	pclqFQNsNotInPCSG := make([]string, 0, len(pcs.Spec.Template.Cliques))
	for _, pclqTemplateSpec := range pcs.Spec.Template.Cliques {
		if !isPCLQInPCSG(pclqTemplateSpec.Name, pcs.Spec.Template.PodCliqueScalingGroupConfigs) {
			pclqFQNsNotInPCSG = append(pclqFQNsNotInPCSG, apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}, pclqTemplateSpec.Name))
		}
	}
	var (
		pclqs            []grovecorev1alpha1.PodClique
		notFoundPCLQFQNs []string
	)
	pclqs, notFoundPCLQFQNs, err = r.getExistingPCLQsByNames(ctx, pcs.Namespace, pclqFQNsNotInPCSG)
	if err != nil {
		return
	}
	if len(notFoundPCLQFQNs) > 0 {
		logger.Info("PodClique(s) expected by PodCliqueSet replica not yet present; skipping MinAvailable evaluation for this replica index", "pcsName", pcs.Name, "replicaIndex", pcsReplicaIndex, "missingPodCliques", notFoundPCLQFQNs)
		skipPCSReplica = true
		return
	}
	breachedPCLQNames, minWaitFor = componentutils.GetMinAvailableBreachedPCLQInfo(pclqs, pcs.Spec.Template.TerminationDelay.Duration, since)
	return
}

// getExistingPCLQsByNames fetches PodClique objects. It returns the PCLQ objects that it found and a slice of PCLQ FQNs for which no PCLQ object exists. If there is an error it just returns the error.
func (r _resource) getExistingPCLQsByNames(ctx context.Context, namespace string, pclqFQNs []string) (pclqs []grovecorev1alpha1.PodClique, notFoundPCLQFQNs []string, err error) {
	for _, pclqFQN := range pclqFQNs {
		pclq := grovecorev1alpha1.PodClique{}
		if err = r.client.Get(ctx, client.ObjectKey{Name: pclqFQN, Namespace: namespace}, &pclq); err != nil {
			if apierrors.IsNotFound(err) {
				notFoundPCLQFQNs = append(notFoundPCLQFQNs, pclqFQN)
				continue
			}
			return nil, nil, err
		}
		pclqs = append(pclqs, pclq)
	}
	return pclqs, notFoundPCLQFQNs, nil
}

// getMinAvailableBreachedPCSGInfo filters PodCliqueScalingGroups that have grovecorev1alpha1.ConditionTypeMinAvailableBreached set to true.
// It returns the names of all such PodCliqueScalingGroups and minimum of all the waitDurations.
//
// Two gates run on top of the MinAvailableBreached=True check:
//
//  1. WasPCSGEverHealthy — a PCSG that has never reached MinAvailableBreached=False since
//     creation is in initial-startup, not a regression. Gang termination would just churn-loop
//     Pending pods against a cluster that already cannot schedule them.
//  2. GangTerminationInProgress=True — a previous fire is already in flight; re-firing while
//     it's still in flight would also churn-loop. The flag is set by createPCSReplicaDeleteTask
//     after the DeleteAllOf succeeds, and cleared by the PCSG status reconciler when
//     MinAvailableBreached transitions back to False.
func getMinAvailableBreachedPCSGInfo(pcsgs []grovecorev1alpha1.PodCliqueScalingGroup, terminationDelay time.Duration, since time.Time) ([]string, time.Duration) {
	pcsgCandidateNames := make([]string, 0, len(pcsgs))
	waitForDurations := make([]time.Duration, 0, len(pcsgs))
	for _, pcsg := range pcsgs {
		cond := meta.FindStatusCondition(pcsg.Status.Conditions, apiconstants.ConditionTypeMinAvailableBreached)
		if cond == nil {
			continue
		}
		if cond.Status != metav1.ConditionTrue {
			continue
		}
		if !componentutils.WasPCSGEverHealthy(&pcsg) {
			continue
		}
		if meta.IsStatusConditionTrue(pcsg.Status.Conditions, apiconstants.ConditionTypeGangTerminationInProgress) {
			continue
		}
		pcsgCandidateNames = append(pcsgCandidateNames, pcsg.Name)
		waitFor := terminationDelay - since.Sub(cond.LastTransitionTime.Time)
		waitForDurations = append(waitForDurations, waitFor)
	}
	if len(waitForDurations) == 0 {
		return pcsgCandidateNames, 0
	}
	slices.Sort(waitForDurations)
	return pcsgCandidateNames, waitForDurations[0]
}

// createPCSReplicaDeleteTask creates a Task to delete all the PodCliques that are part of a PCS replica.
//
// After the DeleteAllOf succeeds we set GangTerminationInProgress=True on every PCSG in the
// PCS replica, including PCSGs whose own PodCliques weren't the reason for this fire (their
// PCLQs are collateral damage of the PCS-replica-wide delete and would otherwise re-trigger
// the breach loop on the next reconcile). The PCSG status reconciler clears this flag once
// MinAvailableBreached transitions back to False, so a successful recycle naturally re-arms
// the next breach episode.
//
// Ordering is action-first / flag-second: if the controller crashes between the DeleteAllOf
// and the flag write, the next reconcile sees the breach still True (new PCLQs Pending) with
// no flag set, fires once more (one extra churn), and converges.
func (r _resource) createPCSReplicaDeleteTask(logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int, reason string) utils.Task {
	return utils.Task{
		Name: fmt.Sprintf("DeletePCSReplicaPodCliques-%d", pcsReplicaIndex),
		Fn: func(ctx context.Context) error {
			pcsReplicaLabels := lo.Assign(
				apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
				map[string]string{
					apicommon.LabelPodCliqueSetReplicaIndex: strconv.Itoa(pcsReplicaIndex),
				},
			)
			if err := r.client.DeleteAllOf(ctx,
				&grovecorev1alpha1.PodClique{},
				client.InNamespace(pcs.Namespace),
				client.MatchingLabels(pcsReplicaLabels)); err != nil {
				logger.Error(err, "failed to delete PodCliques for PCS Replica index", "pcsReplicaIndex", pcsReplicaIndex, "reason", reason)
				r.eventRecorder.Eventf(pcs, corev1.EventTypeWarning, constants.ReasonPodCliqueSetReplicaDeleteFailed, "Error deleting PodCliqueSet replica %d: %v", pcsReplicaIndex, err)
				return err
			}
			logger.Info("Deleted PCS replica PodCliques", "pcsReplicaIndex", pcsReplicaIndex, "reason", reason)
			r.eventRecorder.Eventf(pcs, corev1.EventTypeNormal, constants.ReasonPodCliqueSetReplicaDeleteSuccessful, "PodCliqueSet replica %d deleted", pcsReplicaIndex)

			// Mark every PCSG in this PCS replica as having a recycle in flight. The status
			// reconciler clears it on the next MinAvailableBreached True→False transition.
			pcsgList := &grovecorev1alpha1.PodCliqueScalingGroupList{}
			if err := r.client.List(ctx, pcsgList,
				client.InNamespace(pcs.Namespace),
				client.MatchingLabels(pcsReplicaLabels)); err != nil {
				logger.Error(err, "failed to list PCSGs to mark GangTerminationInProgress", "pcsReplicaIndex", pcsReplicaIndex)
				return err
			}
			for i := range pcsgList.Items {
				pcsg := &pcsgList.Items[i]
				patch := client.MergeFrom(pcsg.DeepCopy())
				meta.SetStatusCondition(&pcsg.Status.Conditions, metav1.Condition{
					Type:    apiconstants.ConditionTypeGangTerminationInProgress,
					Status:  metav1.ConditionTrue,
					Reason:  apiconstants.ConditionReasonGangTerminationActive,
					Message: fmt.Sprintf("Gang termination fired at PCS-replica scope for PCS replica %d; this PCSG's PodCliques were deleted as part of the recycle", pcsReplicaIndex),
				})
				if err := r.client.Status().Patch(ctx, pcsg, patch); err != nil {
					logger.Error(err, "failed to mark GangTerminationInProgress on PCSG", "pcsg", client.ObjectKeyFromObject(pcsg))
					return err
				}
			}
			return nil
		},
	}
}

// isPCLQInPCSG checks if a PodClique is part of any PCSG configuration.
func isPCLQInPCSG(pclqName string, pcsgConfigs []grovecorev1alpha1.PodCliqueScalingGroupConfig) bool {
	return lo.Reduce(pcsgConfigs, func(agg bool, pcsgConfig grovecorev1alpha1.PodCliqueScalingGroupConfig, _ int) bool {
		return agg || slices.Contains(pcsgConfig.CliqueNames, pclqName)
	}, false)
}

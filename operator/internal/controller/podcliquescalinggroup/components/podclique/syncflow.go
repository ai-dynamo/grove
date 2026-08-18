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
	"errors"
	"fmt"
	"maps"
	"strconv"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/resourceclaim"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type syncState struct {
	pcs                            *grovecorev1alpha1.PodCliqueSet
	pcsg                           *grovecorev1alpha1.PodCliqueScalingGroup
	pcsgConfig                     *grovecorev1alpha1.PodCliqueScalingGroupConfig
	pcsReplicaIndex                int
	pcsResourceNameReplica         apicommon.ResourceNameReplica
	podGangMap                     *grovecorev1alpha1.PodGangMap
	existingPCLQs                  []grovecorev1alpha1.PodClique
	existingPCLQNameSet            componentutils.Set[string]
	pcsgIndicesToTerminate         []int
	pcsgIndicesToRequeue           []int
	expectedPCLQFQNsPerPCSGReplica map[int][]string
	expectedPCLQPodTemplateHashMap map[string]string
}

// prepareSyncContext creates and initializes the synchronization context with all necessary data for PCSG reconciliation
func (r _resource) prepareSyncContext(ctx context.Context, logger logr.Logger, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) (*syncState, error) {
	var (
		ss  = &syncState{pcsg: pcsg}
		err error
	)

	// get the PodCliqueSet
	ss.pcs, err = componentutils.GetPodCliqueSet(ctx, r.client, pcsg.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGetPodCliqueSet,
			component.OperationSync,
			fmt.Sprintf("failed to get owner PodCliqueSet for PodCliqueScalingGroup %s", client.ObjectKeyFromObject(pcsg)),
		)
	}

	// Resolve PCS replica index and matching PCSG config for resource sharing
	ss.pcsReplicaIndex, err = getPCSReplicaFromPCSG(pcsg)
	if err != nil {
		return nil, err
	}
	ss.pcsgConfig = resourceclaim.FindPCSGConfig(ss.pcs, pcsg, ss.pcsReplicaIndex)
	if ss.pcsgConfig == nil {
		return nil, groveerr.New(errCodeMissingPodCliqueScalingGroupConfig, component.OperationSync,
			fmt.Sprintf("no matching PodCliqueScalingGroupConfig in PodCliqueSet %s for PodCliqueScalingGroup %s", client.ObjectKeyFromObject(ss.pcs), client.ObjectKeyFromObject(pcsg)))
	}
	ss.pcsResourceNameReplica = apicommon.ResourceNameReplica{Name: ss.pcs.Name, Replica: ss.pcsReplicaIndex}

	// Fetch the PodGangMap for this PCS replica. PodGangMap is the single source of truth
	// for resolving the PodGang name for each PCSG replica; if it does not yet exist, requeue
	// until the PodGangMap component reconciles it.
	ss.podGangMap, err = componentutils.GetPodGangMapForPCSReplica(ctx, r.client, ss.pcs.Name, pcsg.Namespace, ss.pcsReplicaIndex)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, groveerr.New(groveerr.ErrCodeRequeueAfter,
				component.OperationSync,
				fmt.Sprintf("PodGangMap not yet created for PodCliqueSet %s replica index %d; requeuing", ss.pcs.Name, ss.pcsReplicaIndex))
		}
		return nil, groveerr.WrapError(err,
			errCodeGetPodGangMap,
			component.OperationSync,
			fmt.Sprintf("failed to get PodGangMap for PodCliqueSet %s replica index %d", ss.pcs.Name, ss.pcsReplicaIndex))
	}

	// compute the expected state and get existing state.
	ss.expectedPCLQFQNsPerPCSGReplica = getExpectedPodCliqueFQNsByPCSGReplica(pcsg)
	ss.existingPCLQs, err = r.getExistingPCLQs(ctx, pcsg)
	if err != nil {
		return nil, err
	}
	ss.existingPCLQNameSet = componentutils.PodCliqueNameSet(ss.existingPCLQs)

	// compute the PCSG indices that have their MinAvailableBreached condition set to true. Segregated these into two
	// pcsgIndicesToTerminate will have the indices for which the TerminationDelay has expired.
	// pcsgIndicesToRequeue will have the indices for which the TerminationDelay has not yet expired.
	ss.pcsgIndicesToTerminate, ss.pcsgIndicesToRequeue, err = getMinAvailableBreachedPCSGIndices(logger, ss.existingPCLQs, ss.pcs.Spec.Template.TerminationDelay.Duration)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeParsePodCliqueScalingGroupReplicaIndex,
			component.OperationSync,
			fmt.Sprintf("failed to compute min-available-breached PCSG indices for %v", client.ObjectKeyFromObject(pcsg)))
	}

	// pre-compute expected PodTemplateHash for each PCLQ
	ss.expectedPCLQPodTemplateHashMap = getExpectedPCLQPodTemplateHashMap(ss.pcs, pcsg)

	return ss, nil
}

// getExpectedPodCliqueFQNsByPCSGReplica returns a map keyed by PCSG replica index where the value is
// the list of expected PCLQ FQNs for that replica.
func getExpectedPodCliqueFQNsByPCSGReplica(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) map[int][]string {
	var (
		expectedPCLQFQNs = make(map[int][]string)
	)
	for pcsgReplicaIndex := range int(pcsg.Spec.Replicas) {
		pclqFQNs := lo.Map(pcsg.Spec.CliqueNames, func(cliqueName string, _ int) string {
			return apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{
				Name:    pcsg.Name,
				Replica: pcsgReplicaIndex,
			}, cliqueName)
		})
		expectedPCLQFQNs[pcsgReplicaIndex] = pclqFQNs
	}
	return expectedPCLQFQNs
}

// getExistingPCLQs retrieves all PodCliques owned by the specified PodCliqueScalingGroup
func (r _resource) getExistingPCLQs(ctx context.Context, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) ([]grovecorev1alpha1.PodClique, error) {
	existingPCLQs, err := componentutils.GetPCLQsByOwner(ctx, r.client, constants.KindPodCliqueScalingGroup, client.ObjectKeyFromObject(pcsg), getPodCliqueSelectorLabels(pcsg.ObjectMeta))
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPodCliquesForPCSG,
			component.OperationSync,
			fmt.Sprintf("Unable to fetch existing PodCliques for PodCliqueScalingGroup: %v", client.ObjectKeyFromObject(pcsg)),
		)
	}
	return existingPCLQs, nil
}

// getMinAvailableBreachedPCSGIndices categorizes PCSG replicas based on MinAvailable breach status and termination delay
func getMinAvailableBreachedPCSGIndices(logger logr.Logger, existingPCLQs []grovecorev1alpha1.PodClique, terminationDelay time.Duration) (pcsgIndicesToTerminate []int, pcsgIndicesToRequeue []int, err error) {
	now := time.Now()
	// group existing PCLQs by PCSG replica index. These are PCLQs that belong to one replica of PCSG.
	pcsgReplicaIndexPCLQs, err := componentutils.GroupPCLQsByPCSGReplicaIndex(existingPCLQs)
	if err != nil {
		return nil, nil, err
	}
	// For each PCSG replica check if minAvailable for any constituent PCLQ has been violated. Those PCSG replicas should be marked for termination.
	for pcsgReplicaIndex, pclqs := range pcsgReplicaIndexPCLQs {
		pclqNames, minWaitFor := componentutils.GetMinAvailableBreachedPCLQInfo(pclqs, terminationDelay, now)
		if len(pclqNames) > 0 {
			logger.Info("minAvailable breached for PCLQs", "pcsgReplicaIndex", pcsgReplicaIndex, "pclqNames", pclqNames, "minWaitFor", minWaitFor)
			if minWaitFor <= 0 {
				pcsgIndicesToTerminate = append(pcsgIndicesToTerminate, pcsgReplicaIndex)
			} else {
				pcsgIndicesToRequeue = append(pcsgIndicesToRequeue, pcsgReplicaIndex)
			}
		}
	}
	return
}

// getExpectedPCLQPodTemplateHashMap computes the expected pod template hash for each PodClique in the PCSG
func getExpectedPCLQPodTemplateHashMap(pcs *grovecorev1alpha1.PodCliqueSet, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) map[string]string {
	pclqFQNToHash := make(map[string]string)
	pcsgPCLQNames := pcsg.Spec.CliqueNames
	for _, pcsgCliqueName := range pcsgPCLQNames {
		pclqTemplateSpec := componentutils.FindPodCliqueTemplateSpecByName(pcs, pcsgCliqueName)
		if pclqTemplateSpec == nil {
			continue
		}
		podTemplateHash := componentutils.ComputePCLQPodTemplateHash(pclqTemplateSpec, pcs.Spec.Template.PriorityClassName)
		for pcsgReplicaIndex := range int(pcsg.Spec.Replicas) {
			cliqueFQN := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{
				Name:    pcsg.Name,
				Replica: pcsgReplicaIndex,
			}, pcsgCliqueName)
			pclqFQNToHash[cliqueFQN] = podTemplateHash
		}
	}
	return pclqFQNToHash
}

// runSyncFlow executes the main synchronization logic for PodCliqueScalingGroup including replica management and updates
func (r _resource) runSyncFlow(ctx context.Context, logger logr.Logger, ss *syncState) error {
	// Ensure PCSG-level ResourceClaims before creating any PodCliques
	if err := r.ensurePCSGResourceClaims(ctx, ss); err != nil {
		return err
	}

	// Reconcile PodGangMapping and per-PodGang PCLQ deltas. Drives steady-state scale-out/in,
	// gap-fill (Case 1: external PCLQ delete), and coherent-update rebinding via the same
	// status-driven flow.
	if err := r.reconcilePCSGReplicaDistribution(ctx, logger, ss); err != nil {
		return err
	}

	if componentutils.IsPCSGUpdateInProgress(ss.pcsg) {
		if componentutils.IsRollingRecreateUpdateInProgress(ss.pcs) {
			if err := r.processPendingUpdates(ctx, logger, ss); err != nil {
				return err
			}
		} else if componentutils.IsCoherentUpdateInProgress(ss.pcs) {
			if err := r.checkAndMarkPCSGCoherentUpdateEnded(ctx, logger, ss); err != nil {
				return err
			}
		}
	}

	// Gang termination: only evaluated when no update is in progress.
	if !componentutils.IsPCSGUpdateInProgress(ss.pcsg) {
		if err := r.processMinAvailableBreachedPCSGReplicas(ctx, logger, ss); err != nil {
			if errors.Is(err, errPCCGMinAvailableBreached) {
				logger.Info("Skipping further reconciliation as MinAvailable for the PCSG has been breached. This can potentially trigger PCS replica deletion.")
				return nil
			}
			return err
		}
	}

	// If there are any PCSG replicas which have minAvailableBreached but the terminationDelay has not yet expired, then
	// requeue the event after a fixed delay.
	if len(ss.pcsgIndicesToRequeue) > 0 {
		return groveerr.New(groveerr.ErrCodeRequeueAfter,
			component.OperationSync,
			"Requeuing to re-process PCLQs that have breached MinAvailable but not crossed TerminationDelay",
		)
	}
	return nil
}

// ensurePCSGResourceClaims creates PCSG-level AllReplicas and PerReplica ResourceClaims
// and cleans up stale PerReplica RCs from previous scale-in operations.
func (r _resource) ensurePCSGResourceClaims(ctx context.Context, ss *syncState) error {
	if len(ss.pcsgConfig.ResourceSharing) == 0 {
		return nil
	}
	resourceSharers := resourceclaim.ResourceSharersFromPCSG(ss.pcsgConfig.ResourceSharing)
	labels := resourceclaim.ResourceClaimLabels(ss.pcs.Name)
	labels[apicommon.LabelPodCliqueScalingGroup] = ss.pcsg.Name

	if err := r.ensurePCSGAllReplicasRCs(ctx, ss, resourceSharers, labels); err != nil {
		return err
	}
	if err := r.ensurePCSGPerReplicaRCs(ctx, ss, resourceSharers, labels); err != nil {
		return err
	}

	return resourceclaim.CleanupStalePerReplicaRCs(
		ctx, r.client,
		ss.pcsg.Namespace, labels,
		int(ss.pcsg.Spec.Replicas),
		apicommon.LabelPodCliqueScalingGroupReplicaIndex,
	)
}

func (r _resource) ensurePCSGAllReplicasRCs(ctx context.Context, ss *syncState, resourceSharers []resourceclaim.ResourceSharer, labels map[string]string) error {
	if err := resourceclaim.EnsureResourceClaims(
		ctx, r.client,
		ss.pcsg.Name, ss.pcsg.Namespace,
		resourceSharers,
		ss.pcs.Spec.Template.ResourceClaimTemplates,
		labels,
		ss.pcsg, r.scheme,
		nil,
	); err != nil {
		return groveerr.WrapError(err,
			errCodeSyncPCSGResourceClaim,
			component.OperationSync,
			fmt.Sprintf("Error ensuring PCSG-level AllReplicas ResourceClaims for %s", client.ObjectKeyFromObject(ss.pcsg)),
		)
	}
	return nil
}

func (r _resource) ensurePCSGPerReplicaRCs(ctx context.Context, ss *syncState, resourceSharers []resourceclaim.ResourceSharer, labels map[string]string) error {
	for pcsgReplicaIndex := range int(ss.pcsg.Spec.Replicas) {
		repIdx := pcsgReplicaIndex
		replicaLabels := maps.Clone(labels)
		replicaLabels[apicommon.LabelPodCliqueScalingGroupReplicaIndex] = strconv.Itoa(repIdx)
		if err := resourceclaim.EnsureResourceClaims(
			ctx, r.client,
			ss.pcsg.Name, ss.pcsg.Namespace,
			resourceSharers,
			ss.pcs.Spec.Template.ResourceClaimTemplates,
			replicaLabels,
			ss.pcsg, r.scheme,
			&repIdx,
		); err != nil {
			return groveerr.WrapError(err,
				errCodeSyncPCSGResourceClaim,
				component.OperationSync,
				fmt.Sprintf("Error ensuring PCSG-level PerReplica ResourceClaims for %s rep %d", client.ObjectKeyFromObject(ss.pcsg), pcsgReplicaIndex),
			)
		}
	}
	return nil
}

// processMinAvailableBreachedPCSGReplicas handles gang termination of PCSG replicas that have breached minimum availability requirements
func (r _resource) processMinAvailableBreachedPCSGReplicas(ctx context.Context, logger logr.Logger, ss *syncState) error {
	// If pcsg.spec.minAvailable is breached, then delegate the responsibility to the PodCliqueSet reconciler which after
	// termination delay terminate the PodCliqueSet replica. No further processing is required to be done here.
	minAvailableBreachedPCSGReplicas := len(ss.pcsgIndicesToTerminate) + len(ss.pcsgIndicesToRequeue)
	if int(ss.pcsg.Spec.Replicas)-minAvailableBreachedPCSGReplicas < int(*ss.pcsg.Spec.MinAvailable) {
		return errPCCGMinAvailableBreached
	}
	// If pcsg.spec.minAvailable is not breached but if there is one more PCSG replica for which there is at least one PCLQ that has
	// its minAvailable breached for a duration > terminationDelay then gang terminate such PCSG replicas.
	if len(ss.pcsgIndicesToTerminate) > 0 {
		logger.Info("Identified PodCliqueScalingGroup indices for gang termination", "indices", ss.pcsgIndicesToTerminate)
		reason := fmt.Sprintf("Delete PodCliques %v for PodCliqueScalingGroup %v which have breached MinAvailable longer than TerminationDelay: %s", ss.pcsgIndicesToTerminate, client.ObjectKeyFromObject(ss.pcsg), ss.pcs.Spec.Template.TerminationDelay.Duration)
		pclqGangTerminationTasks := r.createDeleteTasks(logger, ss.pcs, ss.pcsg.Name, ss.pcsgIndicesToTerminate, reason)
		if err := r.triggerDeletionOfPodCliques(ctx, logger, client.ObjectKeyFromObject(ss.pcsg), pclqGangTerminationTasks); err != nil {
			return err
		}
		return groveerr.New(groveerr.ErrCodeRequeueAfter,
			component.OperationSync,
			fmt.Sprintf("Requeuing post gang termination of PodCliqueScalingGroup replicas: %v", pclqGangTerminationTasks),
		)
	}
	return nil
}

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
	"slices"
	"strconv"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/resourceclaim"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type syncContext struct {
	ctx                            context.Context
	pcs                            *grovecorev1alpha1.PodCliqueSet
	pcsg                           *grovecorev1alpha1.PodCliqueScalingGroup
	pcsgConfig                     *grovecorev1alpha1.PodCliqueScalingGroupConfig
	pcsReplicaIndex                int
	existingPCLQs                  []grovecorev1alpha1.PodClique
	existingPCLQNameSet            componentutils.Set[string]
	dependencyState                *componentutils.StartupDependencyState
	pcsgIndicesToTerminate         []string
	pcsgIndicesToRequeue           []string
	expectedPCLQFQNsPerPCSGReplica map[int][]string
	expectedPCLQPodTemplateHashMap map[string]string
}

// prepareSyncContext creates and initializes the synchronization context with all necessary data for PCSG reconciliation
func (r _resource) prepareSyncContext(ctx context.Context, logger logr.Logger, pcsg *grovecorev1alpha1.PodCliqueScalingGroup) (*syncContext, error) {
	var (
		syncCtx = &syncContext{
			ctx:  ctx,
			pcsg: pcsg,
		}
		err error
	)

	// get the PodCliqueSet
	syncCtx.pcs, err = componentutils.GetPodCliqueSet(ctx, r.client, pcsg.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGetPodCliqueSet,
			component.OperationSync,
			fmt.Sprintf("failed to get owner PodCliqueSet for PodCliqueScalingGroup %s", client.ObjectKeyFromObject(pcsg)),
		)
	}

	// Resolve PCS replica index and matching PCSG config for resource sharing
	syncCtx.pcsReplicaIndex, err = getPCSReplicaFromPCSG(pcsg)
	if err != nil {
		return nil, err
	}
	syncCtx.pcsgConfig = resourceclaim.FindPCSGConfig(syncCtx.pcs, pcsg, syncCtx.pcsReplicaIndex)

	// compute the expected state and get existing state.
	syncCtx.expectedPCLQFQNsPerPCSGReplica = getExpectedPodCliqueFQNsByPCSGReplica(pcsg)
	syncCtx.existingPCLQs, err = r.getExistingPCLQs(ctx, pcsg)
	if err != nil {
		return nil, err
	}
	syncCtx.existingPCLQNameSet = componentutils.PodCliqueNameSet(syncCtx.existingPCLQs)
	syncCtx.dependencyState, err = componentutils.GetStartupDependencyState(ctx, r.client, syncCtx.pcs)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPodCliquesForPCSG,
			component.OperationSync,
			fmt.Sprintf("failed to resolve startup dependencies for PodCliqueScalingGroup %s", client.ObjectKeyFromObject(pcsg)),
		)
	}

	// compute the PCSG indices that have their MinAvailableBreached condition set to true. Segregated these into two
	// pcsgIndicesToTerminate will have the indices for which the TerminationDelay has expired.
	// pcsgIndicesToRequeue will have the indices for which the TerminationDelay has not yet expired.
	syncCtx.pcsgIndicesToTerminate, syncCtx.pcsgIndicesToRequeue = getMinAvailableBreachedPCSGIndices(logger, syncCtx.existingPCLQs, syncCtx.pcs.Spec.Template.TerminationDelay.Duration)

	// pre-compute expected PodTemplateHash for each PCLQ
	syncCtx.expectedPCLQPodTemplateHashMap = getExpectedPCLQPodTemplateHashMap(syncCtx.pcs, pcsg)

	return syncCtx, nil
}

// runSyncFlow executes the main synchronization logic for PodCliqueScalingGroup including replica management and updates
func (r _resource) runSyncFlow(logger logr.Logger, sc *syncContext) error {
	// Ensure PCSG-level ResourceClaims before creating any PodCliques
	if err := r.ensurePCSGResourceClaims(sc); err != nil {
		return err
	}

	// If there are excess PodCliques than expected, delete the ones that are no longer expected but existing.
	// This can happen when PCSG replicas have been scaled-in.
	if err := r.triggerDeletionOfExcessPCSGReplicas(logger, sc); err != nil {
		return err
	}
	// Create or update the expected PodCliques as per the PodCliqueScalingGroup configurations defined in the PodCliqueSet.
	// For OnDelete update strategy, use createOrUpdatePCLQs which performs in-place updates.
	// For RollingRecreate (default) update strategy, use createExpectedPCLQs which only creates missing PodCliques.
	if !componentutils.IsAutoUpdateStrategy(sc.pcs) {
		if err := r.createOrUpdatePCLQs(logger, sc); err != nil {
			return err
		}
	} else {
		if err := r.createExpectedPCLQs(logger, sc); err != nil {
			return err
		}
		// startsAfter is controller-derived scheduling metadata, not a pod-template
		// rollout. Refresh it in place when sibling components enter or leave idle.
		if err := r.syncStartupDependencies(logger, sc); err != nil {
			return err
		}
	}

	// Only if the rolling update is not in progress, check for a possibility of gang termination and execute it only if
	// the pcsg.spec.minAvailable is not breached.
	if !componentutils.IsPCSGUpdateInProgress(sc.pcsg) {
		if err := r.processMinAvailableBreachedPCSGReplicas(logger, sc); err != nil {
			if errors.Is(err, errPCCGMinAvailableBreached) {
				logger.Info("Skipping further reconciliation as MinAvailable for the PCSG has been breached. This can potentially trigger PCS replica deletion.")
				return nil
			}
			return err
		}
	} else {
		if componentutils.IsAutoUpdateStrategy(sc.pcs) {
			if err := r.processPendingUpdates(logger, sc); err != nil {
				return err
			}
		}
	}

	// If there are any PCSG replicas which have minAvailableBreached but the terminationDelay has not yet expired, then
	// requeue the event after a fixed delay.
	if len(sc.pcsgIndicesToRequeue) > 0 {
		return groveerr.New(groveerr.ErrCodeRequeueAfter,
			component.OperationSync,
			"Requeuing to re-process PCLQs that have breached MinAvailable but not crossed TerminationDelay",
		)
	}
	return nil
}

// triggerDeletionOfExcessPCSGReplicas removes PCSG replicas that exceed the desired replica count due to scale-down
func (r _resource) triggerDeletionOfExcessPCSGReplicas(logger logr.Logger, sc *syncContext) error {
	effectiveReplicas := int(componentutils.EffectiveReplicas(sc.pcsg.Spec.Replicas, sc.pcsg.Spec.MinAvailable))
	replicaIndicesToDelete, err := getExcessPCSGReplicaIndices(sc.existingPCLQs, effectiveReplicas)
	if err != nil {
		return err
	}
	if len(replicaIndicesToDelete) > 0 {
		pcsgObjectKey := client.ObjectKeyFromObject(sc.pcsg)
		logger.Info("Found more PodCliques than expected, triggering deletion of excess PodCliques", "desired", sc.pcsg.Spec.Replicas, "effective", effectiveReplicas, "replicaIndices", replicaIndicesToDelete)
		reason := "Delete excess PodCliqueScalingGroup replicas"
		deletionTasks := r.createDeleteTasks(logger, sc.pcs, pcsgObjectKey.Name, replicaIndicesToDelete, reason)
		if err := r.triggerDeletionOfPodCliques(sc.ctx, logger, pcsgObjectKey, deletionTasks); err != nil {
			return err
		}

		return sc.refreshExistingPCLQs(sc.pcsg)
	}
	return nil
}

// getExcessPCSGReplicaIndices returns actual replica indices outside the effective range.
// Counting replicas is insufficient because manually deleted children can leave sparse indices.
func getExcessPCSGReplicaIndices(existingPCLQs []grovecorev1alpha1.PodClique, effectiveReplicas int) ([]string, error) {
	indexSet := make(map[int]struct{})
	for _, pclq := range existingPCLQs {
		if k8sutils.IsResourceTerminating(pclq.ObjectMeta) {
			continue
		}
		indexValue, ok := pclq.Labels[apicommon.LabelPodCliqueScalingGroupReplicaIndex]
		if !ok {
			continue
		}
		index, err := strconv.Atoi(indexValue)
		if err != nil {
			return nil, groveerr.WrapError(err,
				errCodeParsePodCliqueScalingGroupReplicaIndex,
				component.OperationSync,
				fmt.Sprintf("invalid pcsg replica index label value found on PodClique: %v", client.ObjectKeyFromObject(&pclq)),
			)
		}
		if index >= effectiveReplicas {
			indexSet[index] = struct{}{}
		}
	}
	indices := make([]int, 0, len(indexSet))
	for index := range indexSet {
		indices = append(indices, index)
	}
	slices.Sort(indices)
	return lo.Map(indices, func(index int, _ int) string {
		return strconv.Itoa(index)
	}), nil
}

// createExpectedPCLQs creates any missing PodCliques needed to satisfy the desired PCSG replica configuration
func (r _resource) createExpectedPCLQs(logger logr.Logger, sc *syncContext) error {
	var tasks []utils.Task
	for pcsgReplicaIndex, expectedPCLQNames := range sc.expectedPCLQFQNsPerPCSGReplica {
		for _, pclqFQN := range expectedPCLQNames {
			if sc.existingPCLQNameSet.Has(pclqFQN) {
				continue
			}
			pclqObjectKey := client.ObjectKey{
				Name:      pclqFQN,
				Namespace: sc.pcsg.Namespace,
			}
			createTask := utils.Task{
				Name: fmt.Sprintf("CreatePodClique-%s", pclqObjectKey),
				Fn: func(ctx context.Context) error {
					return r.doCreate(ctx, logger, sc.pcs, sc.pcsg, pcsgReplicaIndex, pclqObjectKey, sc.dependencyState)
				},
			}
			tasks = append(tasks, createTask)
		}
	}
	if runResult := utils.RunConcurrently(sc.ctx, logger, tasks); runResult.HasErrors() {
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errCodeCreatePodCliques,
			component.OperationSync,
			fmt.Sprintf("Error Create of PodCliques for PodCliqueScalingGroup: %v, run summary: %s", client.ObjectKeyFromObject(sc.pcsg), runResult.GetSummary()),
		)
	}
	return nil
}

// syncStartupDependencies refreshes derived startsAfter fields without applying other template changes.
func (r _resource) syncStartupDependencies(logger logr.Logger, sc *syncContext) error {
	tasks := make([]utils.Task, 0, len(sc.existingPCLQs))
	for i := range sc.existingPCLQs {
		pclq := sc.existingPCLQs[i]
		if k8sutils.IsResourceTerminating(pclq.ObjectMeta) {
			continue
		}
		pcsgReplicaIndexValue, ok := pclq.Labels[apicommon.LabelPodCliqueScalingGroupReplicaIndex]
		if !ok {
			continue
		}
		pcsgReplicaIndex, err := strconv.Atoi(pcsgReplicaIndexValue)
		if err != nil {
			return groveerr.WrapError(err,
				errCodeParsePodCliqueScalingGroupReplicaIndex,
				component.OperationSync,
				fmt.Sprintf("invalid pcsg replica index label value found on PodClique: %v", client.ObjectKeyFromObject(&pclq)),
			)
		}

		foundAtIndex := -1
		for _, cliqueName := range sc.pcsg.Spec.CliqueNames {
			expectedName := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{
				Name:    sc.pcsg.Name,
				Replica: pcsgReplicaIndex,
			}, cliqueName)
			if pclq.Name != expectedName {
				continue
			}
			_, foundAtIndex, _ = lo.FindIndexOf(sc.pcs.Spec.Template.Cliques, func(template *grovecorev1alpha1.PodCliqueTemplateSpec) bool {
				return template.Name == cliqueName
			})
			break
		}
		if foundAtIndex < 0 {
			continue
		}

		startsAfter, err := identifyFullyQualifiedStartupDependencyNames(
			sc.pcs,
			sc.pcsReplicaIndex,
			sc.pcsg,
			pcsgReplicaIndex,
			&pclq,
			foundAtIndex,
			sc.dependencyState,
		)
		if err != nil {
			return err
		}
		if slices.Equal(pclq.Spec.StartsAfter, startsAfter) {
			continue
		}

		tasks = append(tasks, utils.Task{
			Name: "SyncPodCliqueStartupDependencies-" + pclq.Name,
			Fn: func(ctx context.Context) error {
				original := pclq.DeepCopy()
				pclq.Spec.StartsAfter = startsAfter
				if err := r.client.Patch(ctx, &pclq, client.MergeFrom(original)); err != nil {
					return groveerr.WrapError(err,
						errCodeCreateOrUpdatePodCliques,
						component.OperationSync,
						fmt.Sprintf("failed to update startup dependencies for PodClique: %v", client.ObjectKeyFromObject(&pclq)),
					)
				}
				logger.Info("Updated PodClique startup dependencies", "pclqObjectKey", client.ObjectKeyFromObject(&pclq), "startsAfter", startsAfter)
				return nil
			},
		})
	}
	if runResult := utils.RunConcurrently(sc.ctx, logger, tasks); runResult.HasErrors() {
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errCodeCreateOrUpdatePodCliques,
			component.OperationSync,
			fmt.Sprintf("failed to update PodClique startup dependencies for PodCliqueScalingGroup: %v", client.ObjectKeyFromObject(sc.pcsg)),
		)
	}
	return nil
}

// createOrUpdatePCLQs creates or updates all expected PodCliques for the PodCliqueScalingGroup.
// This is used for the OnDelete update strategy where changes are applied in place rather than through recreation.
func (r _resource) createOrUpdatePCLQs(logger logr.Logger, sc *syncContext) error {
	var tasks []utils.Task
	for pcsgReplicaIndex, expectedPCLQNames := range sc.expectedPCLQFQNsPerPCSGReplica {
		for _, pclqFQN := range expectedPCLQNames {
			pclqObjectKey := client.ObjectKey{
				Name:      pclqFQN,
				Namespace: sc.pcsg.Namespace,
			}
			pclqExists := sc.existingPCLQNameSet.Has(pclqFQN)
			createOrUpdateTask := utils.Task{
				Name: fmt.Sprintf("CreateOrUpdatePodClique-%s", pclqObjectKey),
				Fn: func(ctx context.Context) error {
					return r.doCreateOrUpdate(ctx, logger, sc.pcs, sc.pcsg, pcsgReplicaIndex, pclqObjectKey, pclqExists, sc.dependencyState)
				},
			}
			tasks = append(tasks, createOrUpdateTask)
		}
	}
	if runResult := utils.RunConcurrently(sc.ctx, logger, tasks); runResult.HasErrors() {
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errCodeCreateOrUpdatePodCliques,
			component.OperationSync,
			fmt.Sprintf("Error CreateOrUpdate of PodCliques for PodCliqueScalingGroup: %v, run summary: %s", client.ObjectKeyFromObject(sc.pcsg), runResult.GetSummary()),
		)
	}
	return nil
}

// processMinAvailableBreachedPCSGReplicas handles gang termination of PCSG replicas that have breached minimum availability requirements
func (r _resource) processMinAvailableBreachedPCSGReplicas(logger logr.Logger, sc *syncContext) error {
	if sc.pcsg.Spec.Replicas == 0 {
		return nil
	}
	// If pcsg.spec.minAvailable is breached, then delegate the responsibility to the PodCliqueSet reconciler which after
	// termination delay terminate the PodCliqueSet replica. No further processing is required to be done here.
	minAvailableBreachedPCSGReplicas := len(sc.pcsgIndicesToTerminate) + len(sc.pcsgIndicesToRequeue)
	effectiveReplicas := int(componentutils.EffectiveReplicas(sc.pcsg.Spec.Replicas, sc.pcsg.Spec.MinAvailable))
	if effectiveReplicas-minAvailableBreachedPCSGReplicas < int(*sc.pcsg.Spec.MinAvailable) {
		return errPCCGMinAvailableBreached
	}
	// If pcsg.spec.minAvailable is not breached but if there is one more PCSG replica for which there is at least one PCLQ that has
	// its minAvailable breached for a duration > terminationDelay then gang terminate such PCSG replicas.
	if len(sc.pcsgIndicesToTerminate) > 0 {
		logger.Info("Identified PodCliqueScalingGroup indices for gang termination", "indices", sc.pcsgIndicesToTerminate)
		reason := fmt.Sprintf("Delete PodCliques %v for PodCliqueScalingGroup %v which have breached MinAvailable longer than TerminationDelay: %s", sc.pcsgIndicesToTerminate, client.ObjectKeyFromObject(sc.pcsg), sc.pcs.Spec.Template.TerminationDelay.Duration)
		pclqGangTerminationTasks := r.createDeleteTasks(logger, sc.pcs, sc.pcsg.Name, sc.pcsgIndicesToTerminate, reason)
		if err := r.triggerDeletionOfPodCliques(sc.ctx, logger, client.ObjectKeyFromObject(sc.pcsg), pclqGangTerminationTasks); err != nil {
			return err
		}
		return groveerr.New(groveerr.ErrCodeRequeueAfter,
			component.OperationSync,
			fmt.Sprintf("Requeuing post gang termination of PodCliqueScalingGroup replicas: %v", pclqGangTerminationTasks),
		)
	}
	return nil
}

// getMinAvailableBreachedPCSGIndices categorizes PCSG replicas based on MinAvailable breach status and termination delay
func getMinAvailableBreachedPCSGIndices(logger logr.Logger, existingPCLQs []grovecorev1alpha1.PodClique, terminationDelay time.Duration) (pcsgIndicesToTerminate []string, pcsgIndicesToRequeue []string) {
	now := time.Now()
	// group existing PCLQs by PCSG replica index. These are PCLQs that belong to one replica of PCSG.
	pcsgReplicaIndexPCLQs := componentutils.GroupPCLQsByPCSGReplicaIndex(existingPCLQs)
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

// It returns a map with the key being the PCSG replica index and the value is the expected PCLQ FQNs for that replica. In addition
// it also returns the total number of expected PCLQs.
func getExpectedPodCliqueFQNsByPCSGReplica(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) map[int][]string {
	var (
		expectedPCLQFQNs = make(map[int][]string)
	)
	for pcsgReplicaIndex := range int(componentutils.EffectiveReplicas(pcsg.Spec.Replicas, pcsg.Spec.MinAvailable)) {
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
		for pcsgReplicaIndex := range int(componentutils.EffectiveReplicas(pcsg.Spec.Replicas, pcsg.Spec.MinAvailable)) {
			cliqueFQN := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{
				Name:    pcsg.Name,
				Replica: pcsgReplicaIndex,
			}, pcsgCliqueName)
			pclqFQNToHash[cliqueFQN] = podTemplateHash
		}
	}
	return pclqFQNToHash
}

// refreshExistingPCLQs removes all the excess PCLQs that belong to any PCSG replica > expectedPCSGReplicas.
// After every successful delete operation of PCSG replica(s), this method will be called to ensure that further processing
// operates on a consistent state of existing PCLQs.
// NOTE: We will be adding expectations usage in this components as well. Then all deletions will be captured as expectations and after every
// deletion of PCSG we will re-queued.
// refreshExistingPCLQs updates the sync context to remove PodCliques belonging to deleted PCSG replicas
func (sc *syncContext) refreshExistingPCLQs(pcsg *grovecorev1alpha1.PodCliqueScalingGroup) error {
	revisedExistingPCLQs := make([]grovecorev1alpha1.PodClique, 0, len(sc.existingPCLQs))
	for _, pclq := range sc.existingPCLQs {
		pcsgReplicaIndexStr, ok := pclq.Labels[apicommon.LabelPodCliqueScalingGroupReplicaIndex]
		if !ok {
			continue
		}
		pcsgReplicaIndex, err := strconv.Atoi(pcsgReplicaIndexStr)
		if err != nil {
			return groveerr.WrapError(err,
				errCodeParsePodCliqueScalingGroupReplicaIndex,
				component.OperationSync,
				fmt.Sprintf("invalid pcsg replica index label value found on PodClique: %v", client.ObjectKeyFromObject(&pclq)),
			)
		}
		if pcsgReplicaIndex < int(componentutils.EffectiveReplicas(pcsg.Spec.Replicas, pcsg.Spec.MinAvailable)) {
			revisedExistingPCLQs = append(revisedExistingPCLQs, pclq)
		}
	}
	sc.existingPCLQs = revisedExistingPCLQs
	sc.existingPCLQNameSet = componentutils.PodCliqueNameSet(revisedExistingPCLQs)
	return nil
}

// ensurePCSGResourceClaims creates PCSG-level AllReplicas and PerReplica ResourceClaims
// and cleans up stale PerReplica RCs from previous scale-in operations.
func (r _resource) ensurePCSGResourceClaims(sc *syncContext) error {
	if sc.pcsgConfig == nil || len(sc.pcsgConfig.ResourceSharing) == 0 {
		return nil
	}
	resourceSharers := resourceclaim.ResourceSharersFromPCSG(sc.pcsgConfig.ResourceSharing)
	labels := resourceclaim.ResourceClaimLabels(sc.pcs.Name)
	labels[apicommon.LabelPodCliqueScalingGroup] = sc.pcsg.Name

	if err := r.ensurePCSGAllReplicasRCs(sc, resourceSharers, labels); err != nil {
		return err
	}
	if err := r.ensurePCSGPerReplicaRCs(sc, resourceSharers, labels); err != nil {
		return err
	}

	return resourceclaim.CleanupStalePerReplicaRCs(
		sc.ctx, r.client,
		sc.pcsg.Namespace, labels,
		int(componentutils.EffectiveReplicas(sc.pcsg.Spec.Replicas, sc.pcsg.Spec.MinAvailable)),
		apicommon.LabelPodCliqueScalingGroupReplicaIndex,
	)
}

func (r _resource) ensurePCSGAllReplicasRCs(sc *syncContext, resourceSharers []resourceclaim.ResourceSharer, labels map[string]string) error {
	if err := resourceclaim.EnsureResourceClaims(
		sc.ctx, r.client,
		sc.pcsg.Name, sc.pcsg.Namespace,
		resourceSharers,
		sc.pcs.Spec.Template.ResourceClaimTemplates,
		labels,
		sc.pcsg, r.scheme,
		nil,
	); err != nil {
		return groveerr.WrapError(err,
			errCodeSyncPCSGResourceClaim,
			component.OperationSync,
			fmt.Sprintf("Error ensuring PCSG-level AllReplicas ResourceClaims for %s", client.ObjectKeyFromObject(sc.pcsg)),
		)
	}
	return nil
}

func (r _resource) ensurePCSGPerReplicaRCs(sc *syncContext, resourceSharers []resourceclaim.ResourceSharer, labels map[string]string) error {
	for pcsgReplicaIndex := range int(componentutils.EffectiveReplicas(sc.pcsg.Spec.Replicas, sc.pcsg.Spec.MinAvailable)) {
		repIdx := pcsgReplicaIndex
		replicaLabels := maps.Clone(labels)
		replicaLabels[apicommon.LabelPodCliqueScalingGroupReplicaIndex] = strconv.Itoa(repIdx)
		if err := resourceclaim.EnsureResourceClaims(
			sc.ctx, r.client,
			sc.pcsg.Name, sc.pcsg.Namespace,
			resourceSharers,
			sc.pcs.Spec.Template.ResourceClaimTemplates,
			replicaLabels,
			sc.pcsg, r.scheme,
			&repIdx,
		); err != nil {
			return groveerr.WrapError(err,
				errCodeSyncPCSGResourceClaim,
				component.OperationSync,
				fmt.Sprintf("Error ensuring PCSG-level PerReplica ResourceClaims for %s rep %d", client.ObjectKeyFromObject(sc.pcsg), pcsgReplicaIndex),
			)
		}
	}
	return nil
}

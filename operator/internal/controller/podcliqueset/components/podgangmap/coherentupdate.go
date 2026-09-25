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

package podgangmap

import (
	"context"
	"fmt"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// buildCoherentUpdateEntries advances the PodGangMap of one PCS replica by at most one coherent update
// sub-step. It reconstructs the plan position from the committed current-hash entries and, when a sub-step
// remains and the sub-step gate holds, emits the next one. It returns the entry set that should exist after
// this reconcile, which is the current set unchanged when nothing remains to emit or the gate holds. It
// does not report update completion, which the orchestrator determines from live child status.
func (r _resource) buildCoherentUpdateEntries(ctx context.Context, syncSnap *syncSnapshot, pcsReplicaIndex int, pgm *grovecorev1alpha1.PodGangMap) ([]grovecorev1alpha1.PodGangEntry, error) {
	standalonePCLQByComponent := syncSnap.inScopeStandalonePCLQsByComponent(pcsReplicaIndex)
	pcsgByComponent, err := syncSnap.inScopePCSGsByComponent(pcsReplicaIndex)
	if err != nil {
		return nil, err
	}

	desiredReplicas := syncSnap.computeDesiredReplicas(standalonePCLQByComponent, pcsgByComponent)

	planner := newSubStepPlanner(syncSnap, pcsReplicaIndex, pgm.Spec.Entries, r.clk, desiredReplicas)
	planPos, err := planner.ascertainPlanPosition()
	if err != nil {
		return nil, err
	}
	syncSnap.logger.V(1).Info("Computed coherent step plan and position", "pcsReplicaIndex", pcsReplicaIndex, "plan", planner.plan.String(), "position", planPos.String())
	pcsCurrentGenerationHash := *syncSnap.pcs.Status.CurrentGenerationHash

	headroom := headroomByComponent(standalonePCLQByComponent, pcsgByComponent, planner.desiredReplicas, planner.maxUnavailableByComponent)
	ss, err := planner.next(planPos, headroom)
	if err != nil {
		return nil, err
	}
	// A nil sub-step means every in-scope component is committed to the current hash, so nothing remains to
	// emit. Reconverge any entry drained of its in-scope content to the current generation before returning.
	if ss == nil {
		syncSnap.logger.V(1).Info("No coherent update sub-step to emit, in-scope components committed to the current generation", "pcsReplicaIndex", pcsReplicaIndex)
		return advanceFullyDrainedEntries(clonePodGangEntries(pgm.Spec.Entries), pcsCurrentGenerationHash, planner.mvu), nil
	}

	// Hold the advance when the gate is not met, so the current sub-step keeps converging before the next
	// one takes more Pods down.
	canEmit, holdReason, err := r.canEmitNextSubStep(ctx, planner, planPos, ss, standalonePCLQByComponent, pcsgByComponent)
	if err != nil {
		return nil, err
	}
	if !canEmit {
		syncSnap.logger.Info("Holding coherent update sub-step", "pcsReplicaIndex", pcsReplicaIndex, "reason", holdReason)
		return advanceFullyDrainedEntries(clonePodGangEntries(pgm.Spec.Entries), pcsCurrentGenerationHash, planner.mvu), nil
	}
	syncSnap.logger.V(1).Info("Emitting coherent update sub-step", "pcsReplicaIndex", pcsReplicaIndex, "subStep", ss.String())
	applied, err := planner.applySubStep(*ss)
	if err != nil {
		return nil, err
	}
	applied = advanceFullyDrainedEntries(applied, pcsCurrentGenerationHash, planner.mvu)
	syncSnap.logger.V(1).Info("Applied coherent update sub-step", "pcsReplicaIndex", pcsReplicaIndex, "entries", formatPodGangEntries(applied))
	return applied, nil
}

// advanceFullyDrainedEntries reconverges the PodGangMap during a coherent update. It bumps an entry's
// generation hash to the current hash once the entry holds no in-scope drainable content and is
// non-empty, so each entry moves to the current generation as its in-scope content finishes draining
// and the map is single-generation by the time the update completes. Empty entries are left for
// removeEmptyEntries to drop, and entries still holding in-scope content keep their generation so the
// engine keeps draining them.
func advanceFullyDrainedEntries(entries []grovecorev1alpha1.PodGangEntry, pcsCurrentGenerationHash string, mvu *mvuTemplate) []grovecorev1alpha1.PodGangEntry {
	for i := range entries {
		if entries[i].PodCliqueSetGenerationHash == pcsCurrentGenerationHash || isPodGangEntryEmpty(entries[i]) || entryHoldsInScopeContent(entries[i], mvu) {
			continue
		}
		entries[i].PodCliqueSetGenerationHash = pcsCurrentGenerationHash
	}
	return entries
}

// entryHoldsInScopeContent reports whether the entry still carries content for any in-scope component,
// meaning the coherent roll has more to drain from it.
func entryHoldsInScopeContent(entry grovecorev1alpha1.PodGangEntry, mvu *mvuTemplate) bool {
	for cliqueName := range mvu.standalonePCLQs {
		if entry.PodCliques[cliqueName] > 0 {
			return true
		}
	}
	for pcsgName := range mvu.pcsgs {
		if len(entry.PCSGReplicaIndices[pcsgName]) > 0 {
			return true
		}
	}
	return false
}

// formatPodGangEntries renders PodGangMap entries in a compact one per entry form for tracing.
func formatPodGangEntries(entries []grovecorev1alpha1.PodGangEntry) []string {
	out := make([]string, 0, len(entries))
	for i := range entries {
		entry := entries[i]
		out = append(out, fmt.Sprintf("%s gen=%s epoch=%s pclq=%v pcsg=%v",
			entry.Role, entry.PodCliqueSetGenerationHash, entry.Epoch, entry.PodCliques, entry.PCSGReplicaIndices))
	}
	return out
}

// computeDesiredReplicas returns the replica count the plan rolls for each in-scope component: the live
// child's spec.Replicas when the object exists (so an HPA-scaled count mid-roll is honored), else the PCS
// template Replicas, since a component deleted out-of-band is recreated at template Replicas. Sourcing
// every in-scope component this way keeps each count at or above MinAvailable, so the step plan is always
// well-defined and numAnchorBearingSteps is never zero.
func (s *syncSnapshot) computeDesiredReplicas(
	standalonePCLQByComponent map[string]grovecorev1alpha1.PodClique,
	pcsgByComponent map[string]grovecorev1alpha1.PodCliqueScalingGroup,
) map[string]int32 {
	standaloneTemplateReplicas := componentutils.GetStandalonePCLQReplicasFromPCSTemplateSpec(s.pcs)
	pcsgTemplateReplicas := componentutils.GetPCSGReplicasFromPCSTemplateSpec(s.pcs)
	desiredReplicas := make(map[string]int32, len(s.mvuTemplate.standalonePCLQs)+len(s.mvuTemplate.pcsgs))
	for componentName := range s.mvuTemplate.standalonePCLQs {
		if pclq, ok := standalonePCLQByComponent[componentName]; ok {
			desiredReplicas[componentName] = pclq.Spec.Replicas
		} else {
			desiredReplicas[componentName] = standaloneTemplateReplicas[componentName]
		}
	}
	for componentName := range s.mvuTemplate.pcsgs {
		if pcsg, ok := pcsgByComponent[componentName]; ok {
			desiredReplicas[componentName] = pcsg.Spec.Replicas
		} else {
			desiredReplicas[componentName] = pcsgTemplateReplicas[componentName]
		}
	}
	return desiredReplicas
}

// inScopeStandalonePCLQsByComponent indexes the standalone PodCliques under a coherent update for one PCS
// replica by component name, keeping only components in the update scope.
func (s *syncSnapshot) inScopeStandalonePCLQsByComponent(pcsReplicaIndex int) map[string]grovecorev1alpha1.PodClique {
	pcsNameReplica := apicommon.ResourceNameReplica{Name: s.pcs.Name, Replica: pcsReplicaIndex}
	pclqByComponent := make(map[string]grovecorev1alpha1.PodClique)
	for _, pclq := range s.existingStandalonePCLQsByReplica[pcsReplicaIndex] {
		componentName := apicommon.ExtractPodCliqueNameFromStandalonePCLQFQN(pclq.Name, pcsNameReplica)
		if _, inScope := s.mvuTemplate.standalonePCLQs[componentName]; inScope {
			pclqByComponent[componentName] = pclq
		}
	}
	return pclqByComponent
}

// inScopePCSGsByComponent indexes the PodCliqueScalingGroups under a coherent update for one PCS replica by
// component name, keeping only components in the update scope.
func (s *syncSnapshot) inScopePCSGsByComponent(pcsReplicaIndex int) (map[string]grovecorev1alpha1.PodCliqueScalingGroup, error) {
	pcsNameReplica := apicommon.ResourceNameReplica{Name: s.pcs.Name, Replica: pcsReplicaIndex}
	pcsgByComponent := make(map[string]grovecorev1alpha1.PodCliqueScalingGroup)
	for _, pcsg := range s.existingPCSGsByReplica[pcsReplicaIndex] {
		componentName, err := apicommon.ExtractScalingGroupNameFromPCSGFQN(pcsg.Name, pcsNameReplica)
		if err != nil {
			return nil, groveerr.WrapError(err, errCodeExtractPCSGName, component.OperationSync,
				fmt.Sprintf("failed to extract PodCliqueScalingGroup name from %q", pcsg.Name))
		}
		if _, inScope := s.mvuTemplate.pcsgs[componentName]; inScope {
			pcsgByComponent[componentName] = pcsg
		}
	}
	return pcsgByComponent, nil
}

// canEmitNextSubStep reports whether the sub-step gate holds for one PCS replica, so the next sub-step may
// be emitted. It checks that the most recent current-hash batch is scheduled, then that the standalone Pods
// subsumed so far are scheduled, then that the next sub-step's drain keeps every in-scope component within
// its MaxUnavailable budget, and finally that the sub-step still has something to drain after headroom
// capping. The first check that fails holds the advance, and its name is returned as the hold reason for
// tracing.
func (r _resource) canEmitNextSubStep(ctx context.Context, planner *subStepPlanner, planPos planPosition, ss *subStep, standalonePCLQByComponent map[string]grovecorev1alpha1.PodClique, pcsgByComponent map[string]grovecorev1alpha1.PodCliqueScalingGroup) (canEmit bool, holdReason string, err error) {
	currentBatchScheduled, err := r.currentBatchScheduled(ctx, planner.pcs, planner.pcsReplicaIndex, planner.entries)
	if err != nil {
		return false, "", err
	}
	if !currentBatchScheduled {
		return false, "currentBatchScheduled=false", nil
	}
	if !subsumedPodsScheduled(standalonePCLQByComponent, planPos) {
		return false, "subsumedPodsScheduled=false", nil
	}
	if !maxUnavailableBudgetSatisfied(standalonePCLQByComponent, pcsgByComponent, planner.desiredReplicas, planner.maxUnavailableByComponent, ss.drainCountByComponent()) {
		return false, "maxUnavailableBudgetSatisfied=false", nil
	}
	if ss.drainsNothing() {
		return false, "noHeadroomToDrain=true", nil
	}
	return true, "", nil
}

// currentBatchScheduled reports whether every PodGang carrying the most recent current-hash epoch has been
// scheduled at least once. When no current-hash entry exists yet the first sub-step has nothing to wait on,
// so it reports true. Readiness is not required to advance because MaxUnavailable, checked separately,
// bounds availability across both revisions.
func (r _resource) currentBatchScheduled(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int, entries []grovecorev1alpha1.PodGangEntry) (bool, error) {
	latestEpoch, err := componentutils.LatestEpochForGenerationHash(entries, *pcs.Status.CurrentGenerationHash)
	if err != nil {
		return false, groveerr.WrapError(err, errCodeInvalidEpoch, component.OperationSync,
			fmt.Sprintf("failed to determine the latest current-hash epoch for PodCliqueSet %v replica %d", client.ObjectKeyFromObject(pcs), pcsReplicaIndex))
	}
	if latestEpoch == nil {
		return true, nil
	}
	return componentutils.AllPodGangsAtEpochEverScheduled(ctx, r.client, client.ObjectKeyFromObject(pcs), int32(pcsReplicaIndex), *latestEpoch)
}

// subsumedPodsScheduled reports whether every in-scope standalone PodClique has at least as many new-hash
// scheduled Pods as the plan has committed to the current hash for it. Standalone tail Pods subsume into an
// anchor rather than getting their own PodGang, so their placement is tracked through the PodClique's
// UpdatedScheduledReplicas rather than the anchor PodGang. PodCliqueScalingGroups are not checked here
// because they roll through their own tail PodGangs, whose placement currentBatchScheduled covers.
func subsumedPodsScheduled(standalonePCLQByComponent map[string]grovecorev1alpha1.PodClique, planPos planPosition) bool {
	for componentName, pclq := range standalonePCLQByComponent {
		scheduledAtCurrentHash := int32(0)
		if pclq.Status.UpdateProgress != nil {
			scheduledAtCurrentHash = pclq.Status.UpdateProgress.UpdatedScheduledReplicas
		}
		if scheduledAtCurrentHash < planPos.currentHashCountByComponent[componentName] {
			return false
		}
	}
	return true
}

// maxUnavailableBudgetSatisfied reports whether every in-scope component can absorb the next sub-step's
// drain without the number of unavailable replicas exceeding MaxUnavailable. For each component it adds what
// the sub-step will take down to the replicas already unavailable and holds the sub-step if that total would
// cross MaxUnavailable, so unrelated unavailability that already uses the budget is not stacked on top of. A
// standalone PodClique is measured by its Ready Pods and a PodCliqueScalingGroup by its available replicas.
func maxUnavailableBudgetSatisfied(standalonePCLQByComponent map[string]grovecorev1alpha1.PodClique, pcsgByComponent map[string]grovecorev1alpha1.PodCliqueScalingGroup, desiredReplicas, maxUnavailableByComponent, drainByComponent map[string]int32) bool {
	for componentName, pclq := range standalonePCLQByComponent {
		unavailableAfterDrain := desiredReplicas[componentName] - pclq.Status.ReadyReplicas + drainByComponent[componentName]
		if unavailableAfterDrain > maxUnavailableByComponent[componentName] {
			return false
		}
	}
	for componentName, pcsg := range pcsgByComponent {
		unavailableAfterDrain := desiredReplicas[componentName] - pcsg.Status.AvailableReplicas + drainByComponent[componentName]
		if unavailableAfterDrain > maxUnavailableByComponent[componentName] {
			return false
		}
	}
	return true
}

// headroomByComponent returns, per in-scope component, how many replicas may still be taken down before the
// number unavailable would exceed MaxUnavailable. It is MaxUnavailable minus the replicas currently
// unavailable, clamped at zero. A standalone PodClique is measured by its Ready Pods and a
// PodCliqueScalingGroup by its available replicas.
func headroomByComponent(standalonePCLQByComponent map[string]grovecorev1alpha1.PodClique, pcsgByComponent map[string]grovecorev1alpha1.PodCliqueScalingGroup, desiredReplicas, maxUnavailableByComponent map[string]int32) map[string]int32 {
	headroom := make(map[string]int32, len(standalonePCLQByComponent)+len(pcsgByComponent))
	for componentName, pclq := range standalonePCLQByComponent {
		unavailable := desiredReplicas[componentName] - pclq.Status.ReadyReplicas
		headroom[componentName] = max(0, maxUnavailableByComponent[componentName]-unavailable)
	}
	for componentName, pcsg := range pcsgByComponent {
		unavailable := desiredReplicas[componentName] - pcsg.Status.AvailableReplicas
		headroom[componentName] = max(0, maxUnavailableByComponent[componentName]-unavailable)
	}
	return headroom
}

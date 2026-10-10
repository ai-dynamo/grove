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
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

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

	// standalonePCLQPodCounts is derived from one live Pod list of each in-scope standalone PodClique. Its
	// running-by-anchor counts drive missing old-version Pod detection: a missing old-version Pod is a slot an
	// old-version anchor still commits but has no running Pod behind it, left by a Pod that died and was not
	// refilled (the pod controller does not refill an old-version PodGang mid-update). Its
	// available-by-component counts feed the MaxUnavailable budget gate.
	standalonePCLQPodCounts, err := r.gatherStandalonePodCounts(ctx, syncSnap.pcs, pcsReplicaIndex, pgm.Spec.Entries, standalonePCLQByComponent)
	if err != nil {
		return nil, err
	}
	planner := newSubStepPlanner(syncSnap, pcsReplicaIndex, pgm.Spec.Entries, r.clk, desiredReplicas, standalonePCLQPodCounts.runningByCliqueAndAnchor)

	planPos, err := planner.ascertainPlanPosition()
	if err != nil {
		return nil, err
	}
	syncSnap.logger.V(1).Info("Computed coherent step plan and position", "pcsReplicaIndex", pcsReplicaIndex, "plan", planner.plan.String(), "position", planPos.String())

	headroom := headroomByComponent(pcsgByComponent, planner.desiredReplicas, planner.maxUnavailableByComponent, standalonePCLQPodCounts.availableByComponent, planner.numMissingOldVersionPodsByPCLQ)
	ss, err := planner.next(planPos, headroom)
	if err != nil {
		return nil, err
	}
	// A nil sub-step means every in-scope component is committed to the current hash, so nothing remains to
	// emit. Reconverge any entry drained of its in-scope content to the current generation before returning.
	if ss == nil {
		syncSnap.logger.V(1).Info("No coherent update sub-step to emit, in-scope components committed to the current generation", "pcsReplicaIndex", pcsReplicaIndex)
		return planner.heldEntries(), nil
	}

	// Hold the advance when the gate is not met, so the current sub-step keeps converging before the next
	// one takes more Pods down.
	canEmit, holdReason, err := r.canEmitNextSubStep(ctx, planner, planPos, ss, standalonePCLQByComponent, pcsgByComponent, standalonePCLQPodCounts.availableByComponent)
	if err != nil {
		return nil, err
	}
	if !canEmit {
		syncSnap.logger.Info("Holding coherent update sub-step", "pcsReplicaIndex", pcsReplicaIndex, "reason", holdReason)
		return planner.heldEntries(), nil
	}
	syncSnap.logger.V(1).Info("Emitting coherent update sub-step", "pcsReplicaIndex", pcsReplicaIndex, "subStep", ss.String())
	applied, err := planner.applySubStep(*ss)
	if err != nil {
		return nil, err
	}
	applied = advanceFullyDrainedEntries(applied, *syncSnap.pcs.Status.CurrentGenerationHash, planner.mvu)
	syncSnap.logger.V(1).Info("Applied coherent update sub-step", "pcsReplicaIndex", pcsReplicaIndex, "entries", formatPodGangEntries(applied))
	return applied, nil
}

// heldEntries returns the current entries for a reconcile that emits no sub-step, advancing any fully
// drained old entry to the current generation. The committed content is otherwise unchanged.
func (p *subStepPlanner) heldEntries() []grovecorev1alpha1.PodGangEntry {
	return advanceFullyDrainedEntries(clonePodGangEntries(p.entries), *p.pcs.Status.CurrentGenerationHash, p.mvu)
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
func (r _resource) canEmitNextSubStep(ctx context.Context, planner *subStepPlanner, planPos planPosition, ss *subStep, standalonePCLQByComponent map[string]grovecorev1alpha1.PodClique, pcsgByComponent map[string]grovecorev1alpha1.PodCliqueScalingGroup, availableByComponent map[string]int32) (canEmit bool, holdReason string, err error) {
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
	if !maxUnavailableBudgetSatisfied(pcsgByComponent, planner.desiredReplicas, planner.maxUnavailableByComponent, ss.drainCountByComponent(), availableByComponent, planner.numMissingOldVersionPodsByPCLQ) {
		return false, "maxUnavailableBudgetSatisfied=false", nil
	}
	if ss.drainsNothing() {
		return false, "noHeadroomToDrain=true", nil
	}
	return true, "", nil
}

// currentBatchScheduled reports whether every PodGang the most recent current-hash sub-step committed has
// been scheduled at least once. It derives the expected PodGang names from that committed entry, so a batch
// that has only partially materialized (some of a tail entry's per-index PodGangs not created yet) does not
// pass. When no current-hash entry exists yet the first sub-step has nothing to wait on, so it reports true.
// Readiness is not required to advance because MaxUnavailable, checked separately, bounds availability
// across both revisions.
func (r _resource) currentBatchScheduled(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int, entries []grovecorev1alpha1.PodGangEntry) (bool, error) {
	latestEntry, err := componentutils.LatestEntryForGenerationHash(entries, *pcs.Status.CurrentGenerationHash)
	if err != nil {
		return false, groveerr.WrapError(err, errCodeInvalidEpoch, component.OperationSync,
			fmt.Sprintf("failed to determine the latest current-hash entry for PodCliqueSet %v replica %d", client.ObjectKeyFromObject(pcs), pcsReplicaIndex))
	}
	if latestEntry == nil {
		return true, nil
	}
	rnr := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}
	expectedPodGangNames := componentutils.ExpectedPodGangNamesForEntry(rnr, *latestEntry)
	return componentutils.AllPodGangsScheduled(ctx, r.client, pcs.Namespace, expectedPodGangNames)
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

// maxUnavailableBudgetSatisfied reports whether the next sub-step keeps every component it drains within its
// MaxUnavailable budget. Only components the sub-step drains are checked. A component the sub-step does not
// touch cannot be pushed past its budget by this drain.
//
// A missing old-version Pod is a slot an old-version anchor still commits but that has no running Pod
// behind it. It is already unavailable, so reclaiming it removes no running Pod. Only the drain beyond the
// missing old-version count removes a running Pod.
//
// For a standalone PodClique:
//
//	runningPodTakedown       = max(0, drain - numMissingOldVersionPods)
//	unavailableAfterTakedown = (desired - available) + runningPodTakedown
//	hold if runningPodTakedown > 0 and unavailableAfterTakedown > maxUnavailable
//
// Available is the count of Pods that are Ready and not terminating. It is not the PodClique's ReadyReplicas,
// which keeps counting a Pod as ready after the Pod is deleted until its containers stop.
//
// A pure reclaim (runningPodTakedown is 0) is always allowed, even when the component is already over budget,
// because it refills a dead slot on the current-version anchor and lowers no availability.
//
// For a PodCliqueScalingGroup:
//
//	hold if drain > 0 and (desired - available) + drain > maxUnavailable
//
// A PodCliqueScalingGroup has no missing old-version count. A not-yet-rolled PCSG replica keeps its member
// PodCliques at the old revision, so a dead member Pod is recreated at the old revision on its own gang. No
// new-revision Pod ever lands on an old gang, so there is nothing to reclaim.
func maxUnavailableBudgetSatisfied(pcsgByComponent map[string]grovecorev1alpha1.PodCliqueScalingGroup, desiredReplicas, maxUnavailableByComponent, drainByComponent, availableByComponent, numMissingOldVersionPodsByPCLQ map[string]int32) bool {
	for componentName := range availableByComponent {
		runningPodTakedown := max(0, drainByComponent[componentName]-numMissingOldVersionPodsByPCLQ[componentName])
		currentlyUnavailable := desiredReplicas[componentName] - availableByComponent[componentName]
		unavailableAfterTakedown := currentlyUnavailable + runningPodTakedown
		if runningPodTakedown > 0 && unavailableAfterTakedown > maxUnavailableByComponent[componentName] {
			return false
		}
	}
	for componentName, pcsg := range pcsgByComponent {
		drain := drainByComponent[componentName]
		currentlyUnavailable := desiredReplicas[componentName] - pcsg.Status.AvailableReplicas
		if drain > 0 && currentlyUnavailable+drain > maxUnavailableByComponent[componentName] {
			return false
		}
	}
	return true
}

// headroomByComponent returns, per in-scope component, how many old-version Pods a sub-step may drain now.
//
// A missing old-version Pod is a slot an old-version anchor still commits but that has no running Pod
// behind it, left by a Pod that died and was not refilled. Reclaiming it lowers the anchor count without
// removing a running Pod, so it costs no availability and is free budget on top of the running-Pod
// takedown headroom.
//
// For a standalone PodClique:
//
//	runningPodTakedownHeadroom = max(0, maxUnavailable - (desired - available))
//	headroom                   = runningPodTakedownHeadroom + numMissingOldVersionPods
//
// Available is the count of Pods that are Ready and not terminating, not the PodClique's ReadyReplicas.
//
// Worked deadlock case, showing why the missing old-version term is needed. maxUnavailable 1, desired 10,
// available 9 because 1 Pod died on an old-version anchor, so numMissingOldVersionPods is 1. Without the term:
//
//	runningPodTakedownHeadroom = max(0, 1 - (10 - 9)) = 0
//	headroom                   = 0
//
// Headroom 0 drains nothing. The dead Pod is on an old-version anchor the pod controller will not refill,
// so available never climbs back, headroom stays 0, and the roll never advances. With the term headroom is
// 0 + 1 = 1, so the sub-step reclaims the missing old-version Pod, the slot is refilled on the
// current-version anchor, and the roll proceeds.
//
// A PodCliqueScalingGroup has no missing old-version count. A not-yet-rolled PCSG replica keeps its member
// PodCliques at the old revision, so a dead member Pod is recreated at the old revision on its own gang. No
// new-revision Pod ever lands on an old gang, so there is nothing to reclaim. Its headroom is just the
// running-replica takedown headroom.
func headroomByComponent(pcsgByComponent map[string]grovecorev1alpha1.PodCliqueScalingGroup, desiredReplicas, maxUnavailableByComponent, availableByComponent, numMissingOldVersionPodsByPCLQ map[string]int32) map[string]int32 {
	headroom := make(map[string]int32, len(availableByComponent)+len(pcsgByComponent))
	for componentName := range availableByComponent {
		currentlyUnavailable := desiredReplicas[componentName] - availableByComponent[componentName]
		runningPodTakedownHeadroom := max(0, maxUnavailableByComponent[componentName]-currentlyUnavailable)
		headroom[componentName] = runningPodTakedownHeadroom + numMissingOldVersionPodsByPCLQ[componentName]
	}
	for componentName, pcsg := range pcsgByComponent {
		currentlyUnavailable := desiredReplicas[componentName] - pcsg.Status.AvailableReplicas
		headroom[componentName] = max(0, maxUnavailableByComponent[componentName]-currentlyUnavailable)
	}
	return headroom
}

// standalonePodCounts holds the per-PodClique counts the coherent update engine derives from one live Pod
// list of each in-scope standalone PodClique of the replica under update.
type standalonePodCounts struct {
	// runningByCliqueAndAnchor is the running (not terminating) Pod count on each anchor PodGang, keyed by
	// clique name then anchor epoch. It drives missing old-version Pod detection.
	runningByCliqueAndAnchor map[string]map[string]int32
	// availableByComponent is the number of Pods that are Ready and not terminating, keyed by clique name. It
	// is the availability figure the MaxUnavailable budget gate uses, distinct from the PodClique's
	// ReadyReplicas, which keeps counting a Pod as ready after the Pod is deleted until its containers stop.
	availableByComponent map[string]int32
}

// gatherStandalonePodCounts lists the Pods of every in-scope standalone PodClique of the replica under
// update once and returns two counts derived from that single list.
//
// runningByCliqueAndAnchor buckets running (not terminating) Pods by the grove.io/podgang label resolved to
// an anchor epoch via EpochByAnchorPodGangName. availableByComponent counts Pods that are Ready and not
// terminating, across all of the PodClique's Pods regardless of PodGang.
//
// If the cache is stale it can only show a dead Pod as still running, which lowers the missing old-version
// count, never raises it, so a drain never exceeds budget. This holds because old anchors are delete-only
// during the update, so their Pod set only shrinks and a create can never be missed.
func (r _resource) gatherStandalonePodCounts(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int, entries []grovecorev1alpha1.PodGangEntry, standalonePCLQByComponent map[string]grovecorev1alpha1.PodClique) (standalonePodCounts, error) {
	rnr := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}
	epochByAnchorPodGangName := componentutils.EpochByAnchorPodGangName(entries, rnr)

	counts := standalonePodCounts{
		runningByCliqueAndAnchor: make(map[string]map[string]int32, len(standalonePCLQByComponent)),
		availableByComponent:     make(map[string]int32, len(standalonePCLQByComponent)),
	}
	for cliqueName, pclq := range standalonePCLQByComponent {
		pods, err := componentutils.GetPCLQPods(ctx, r.client, pcs.Name, &pclq)
		if err != nil {
			return standalonePodCounts{}, groveerr.WrapError(err, errCodeListPods, component.OperationSync,
				fmt.Sprintf("could not list Pods for standalone PodClique %q under coherent update", cliqueName))
		}
		runningPodsByAnchor := make(map[string]int32)
		var available int32
		for _, pod := range pods {
			if k8sutils.IsResourceTerminating(pod.ObjectMeta) {
				continue
			}
			if k8sutils.IsPodReady(pod) {
				available++
			}
			// A Pod not on an anchor of this replica resolves to no epoch and is skipped.
			if epoch, onAnchor := epochByAnchorPodGangName[pod.Labels[apicommon.LabelPodGang]]; onAnchor {
				runningPodsByAnchor[epoch]++
			}
		}
		counts.runningByCliqueAndAnchor[cliqueName] = runningPodsByAnchor
		counts.availableByComponent[cliqueName] = available
	}
	return counts, nil
}

// numMissingOldVersionPodsByStandalonePCLQ returns the count of missing old-version Pods per in-scope
// standalone PodClique.
//
// During a coherent update the pod controller does not refill a Pod deficit on an old-version PodGang for
// the PCS replica under update. Refilling would build the Pod at the current revision and place it on an
// old-version PodGang, which breaks coherence. So a deficit on an old-version anchor is left in place and
// drained by the engine. A missing old-version Pod is such a deficit, the gap between the Pod count an
// old-version anchor entry assigns and the Pods actually running on it.
//
// Current-version anchors are skipped, since their deficits are filled normally. Only in-scope standalone
// PodCliques are counted, so an out-of-scope clique sharing an old anchor is ignored.
//
// Example. An old-version anchor assigns 3 Pods to a clique but only 2 are running because 1 died. That
// anchor contributes 1 missing old-version Pod. The counts are summed over all old-version anchors.
func numMissingOldVersionPodsByStandalonePCLQ(entries []grovecorev1alpha1.PodGangEntry, currentHash string, runningPodsByCliqueAndAnchor map[string]map[string]int32) map[string]int32 {
	missingOldVersionPodsByClique := make(map[string]int32)
	for i := range entries {
		entry := entries[i]
		if entry.Role != grovecorev1alpha1.PodGangEntryRoleAnchor || entry.PodCliqueSetGenerationHash == currentHash {
			continue
		}
		for cliqueName, anchorPodCount := range entry.PodCliques {
			runningPodsByAnchor, inScope := runningPodsByCliqueAndAnchor[cliqueName]
			if !inScope {
				continue
			}
			if runningPods := runningPodsByAnchor[entry.Epoch]; anchorPodCount > runningPods {
				missingOldVersionPodsByClique[cliqueName] += anchorPodCount - runningPods
			}
		}
	}
	return missingOldVersionPodsByClique
}

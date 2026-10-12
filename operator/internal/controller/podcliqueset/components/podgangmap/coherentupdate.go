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
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"k8s.io/apimachinery/pkg/util/sets"
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

	// pclqPodCounts is derived from one live Pod list of each in-scope standalone PodClique. Its
	// running-by-anchor counts drive missing old-version Pod detection: a missing old-version Pod is a slot an
	// old-version anchor still commits but has no running Pod behind it, left by a Pod that died and was not
	// refilled (the pod controller does not refill an old-version PodGang mid-update). Its non-terminating and
	// new-not-ready counts feed the count-anchored MaxUnavailable budget.
	pclqPodCounts, err := r.gatherStandalonePodCounts(ctx, syncSnap.pcs, pcsReplicaIndex, pgm.Spec.Entries, standalonePCLQByComponent)
	if err != nil {
		return nil, err
	}
	// pcsgReplicaInfos is derived from the member PodCliques of each in-scope PodCliqueScalingGroup under
	// update. Its per-index health and roll state feed the count-anchored MaxUnavailable budget and the
	// health-ordered drain selection.
	pcsgReplicaInfos, err := r.gatherPCSGReplicaInfos(ctx, syncSnap.pcs, pgm.Spec.Entries, pcsgByComponent)
	if err != nil {
		return nil, err
	}
	planner := newSubStepPlanner(syncSnap, pcsReplicaIndex, pgm.Spec.Entries, r.clk, desiredReplicas, standalonePCLQByComponent, pcsgReplicaInfos, pclqPodCounts)

	planPos, err := planner.ascertainPlanPosition()
	if err != nil {
		return nil, err
	}
	syncSnap.logger.V(1).Info("Computed coherent step plan and position", "pcsReplicaIndex", pcsReplicaIndex, "plan", planner.plan.String(), "position", planPos.String())

	headroom := planner.headroomByComponent()
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
	canEmit, holdReason, err := r.canEmitNextSubStep(ctx, planner, planPos, ss)
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
func (r _resource) canEmitNextSubStep(ctx context.Context, planner *subStepPlanner, planPos planPosition, ss *subStep) (canEmit bool, holdReason string, err error) {
	currentBatchScheduled, err := r.currentBatchScheduled(ctx, planner.pcs, planner.pcsReplicaIndex, planner.entries)
	if err != nil {
		return false, "", err
	}
	if !currentBatchScheduled {
		return false, "currentBatchScheduled=false", nil
	}
	if !subsumedPodsScheduled(planner.standalonePCLQByComponent, planPos) {
		return false, "subsumedPodsScheduled=false", nil
	}
	if !planner.maxUnavailableBudgetSatisfied(ss.drainCountByComponent()) {
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

// maxUnavailableBudgetSatisfied reports whether draining the given per-component counts keeps every component
// within its MaxUnavailable budget. Only components the drain touches are checked. A component the drain does
// not touch cannot be pushed past its budget by it.
//
// For a standalone PodClique the budget is count-anchored, so an old Pod that is already unavailable no longer
// blocks its own replacement. A missing old-version Pod (an anchor slot with no running Pod behind it) is a
// free Phase-1 reclaim that removes no running Pod, so only the running-Pod takedown is charged:
//
//	runningPodTakedown = max(0, drain - numMissingOldVersionPods)
//	hold if runningPodTakedown > 0 and runningPodTakedown > standaloneDisruptionBudget
//
// A missing old-version Pod does not read the old Pod's readiness, so the budget never collapses to 0 just
// because replicas are already unavailable. A pure reclaim (runningPodTakedown is 0) is always allowed, so an
// empty slot can always be reclaimed and the old entry can drain fully.
//
// For a PodCliqueScalingGroup, which has no missing old-version count so the whole drain is charged:
//
//	hold if drain > 0 and drain > pcsgDisruptionBudget
//
// A not-yet-rolled PCSG replica keeps its member PodCliques at the old revision, so a dead member Pod is
// recreated at the old revision on its own gang. No new-revision Pod ever lands on an old gang, so there is
// nothing to reclaim.
func (p *subStepPlanner) maxUnavailableBudgetSatisfied(drainByComponent map[string]int32) bool {
	for componentName := range p.pclqPodCounts.nonTerminatingByPCLQ {
		// A missing old-version Pod is a free Phase-1 reclaim that removes no running Pod, so only the
		// running-Pod takedown is charged against the budget.
		runningPodTakedown := max(0, drainByComponent[componentName]-p.numMissingOldVersionPodsByPCLQ[componentName])
		if runningPodTakedown > 0 && runningPodTakedown > p.standaloneDisruptionBudget(componentName) {
			return false
		}
	}
	for componentName := range p.pcsgReplicaInfos {
		// A PodCliqueScalingGroup has no missing old-version count, so the whole drain is a real replica
		// takedown charged against the budget.
		if drain := drainByComponent[componentName]; drain > 0 && drain > p.pcsgDisruptionBudget(componentName) {
			return false
		}
	}
	return true
}

// standaloneDisruptionBudget returns how many running old-version Pods of a standalone PodClique may be
// taken down this reconcile without dropping serving capacity below the minimum that must stay available,
// after accounting for in-flight replacements. It is count-anchored, so it does not collapse to zero when
// replicas are already unavailable. It may be negative, callers treat anything at or below zero as no
// running-Pod headroom (a pure missing-slot reclaim is still allowed).
func (p *subStepPlanner) standaloneDisruptionBudget(componentName string) int32 {
	existing := p.pclqPodCounts.nonTerminatingByPCLQ[componentName]
	// requiredAvailable is the minimum number of Pods that must stay available during the update
	// (desired - maxUnavailable). It is distinct from the component's MinAvailable spec field.
	requiredAvailable := p.desiredReplicas[componentName] - p.maxUnavailableByComponent[componentName]
	inFlightReplacements := p.pclqPodCounts.newNotReadyByPCLQ[componentName]
	return existing - requiredAvailable - inFlightReplacements
}

// pcsgDisruptionBudget returns how many PodCliqueScalingGroup replicas may be taken down this reconcile
// without dropping serving capacity below the minimum that must stay available, after accounting for
// in-flight replacements. A PodCliqueScalingGroup has no missing old-version count, so the whole drain is
// charged. It may be negative, callers treat anything at or below zero as no headroom.
func (p *subStepPlanner) pcsgDisruptionBudget(componentName string) int32 {
	infos := p.pcsgReplicaInfos[componentName]
	// requiredAvailable is the minimum number of replicas that must stay available during the update
	// (desired - maxUnavailable). It is distinct from the component's MinAvailable spec field.
	requiredAvailable := p.desiredReplicas[componentName] - p.maxUnavailableByComponent[componentName]
	var newNotReady int32
	for _, info := range infos {
		if info.atCurrentHash && info.state != componentutils.PCSGReplicaStateReady {
			newNotReady++
		}
	}
	return int32(len(infos)) - requiredAvailable - newNotReady
}

// headroomByComponent returns, per in-scope component, how many old-version Pods a sub-step may drain now.
//
// For a standalone PodClique it is the free missing old-version reclaims plus the count-anchored running-Pod
// takedown headroom:
//
//	headroom = numMissingOldVersionPods + max(0, standaloneDisruptionBudget)
//
// Because the budget is count-anchored it does not collapse to 0 when replicas are already unavailable, so a
// corrective roll still advances. A missing old-version Pod is a slot an old-version anchor still commits but
// that has no running Pod behind it, left by a Pod that died and was not refilled. Reclaiming it removes no
// running Pod, so it is free budget on top of the running-Pod takedown headroom.
//
// A PodCliqueScalingGroup has no missing old-version count, so its headroom is just the count-anchored
// takedown budget.
func (p *subStepPlanner) headroomByComponent() map[string]int32 {
	headroom := make(map[string]int32, len(p.pclqPodCounts.nonTerminatingByPCLQ)+len(p.pcsgReplicaInfos))
	for componentName := range p.pclqPodCounts.nonTerminatingByPCLQ {
		// Free missing old-version reclaims plus the count-anchored running-Pod takedown headroom.
		headroom[componentName] = p.numMissingOldVersionPodsByPCLQ[componentName] + max(0, p.standaloneDisruptionBudget(componentName))
	}
	for componentName := range p.pcsgReplicaInfos {
		headroom[componentName] = max(0, p.pcsgDisruptionBudget(componentName))
	}
	return headroom
}

// cliqueAndEpochKey identifies one standalone PodClique on one anchor PodGang epoch.
type cliqueAndEpochKey struct {
	clique string
	epoch  string
}

// anchorLivePods holds the live (non-terminating) Pod counts of one standalone PodClique on one anchor
// PodGang, as observed from the Pod list.
type anchorLivePods struct {
	// running is the count of non-terminating Pods backing this anchor's slots.
	running int32
	// notReady is how many of those running Pods are not Ready.
	notReady int32
}

// standalonePCLQPodCounts holds the per-PodClique counts the coherent update engine derives from one live
// Pod list of each in-scope standalone PodClique of the replica under update.
type standalonePCLQPodCounts struct {
	// livePodCountsByAnchor holds the live (running and not-Ready) Pod counts of each standalone PodClique on
	// each anchor PodGang, keyed by clique name and anchor epoch. Phase 1 of the drain reads running to find
	// missing old-version slots, and Phase 2a reads notReady to take unavailable Pods down before Ready ones.
	livePodCountsByAnchor map[cliqueAndEpochKey]anchorLivePods
	// nonTerminatingByPCLQ is the count of non-terminating Pods of each in-scope standalone PodClique on a
	// known anchor of this replica, keyed by clique name. It holds an entry for every in-scope standalone
	// PodClique and only those, so its key set is the in-scope set that
	// numMissingOldVersionPodsByStandalonePCLQ filters on. It is the existing figure the count-anchored
	// MaxUnavailable budget uses, so a Pod on a pruned or unrelated anchor must not inflate it.
	nonTerminatingByPCLQ map[string]int32
	// newNotReadyByPCLQ is the count of non-terminating, not-Ready Pods on current-hash anchors per clique.
	// It is the in-flight-replacement figure the count-anchored budget subtracts.
	newNotReadyByPCLQ map[string]int32
}

// gatherStandalonePodCounts lists the Pods of every in-scope standalone PodClique of the replica under
// update once and returns the counts the coherent engine derives from that single list.
//
// livePodCountsByAnchor buckets running (not terminating) Pods by the grove.io/podgang label resolved to an
// anchor epoch via EpochByAnchorPodGangName, recording the running count and how many are not Ready, for
// missing old-version detection, the Phase-1 reclaim, and the not-Ready-first drain.
// nonTerminatingByPCLQ is the non-terminating Pod count per clique on a known anchor, the budget existing
// figure.
// newNotReadyByPCLQ counts non-terminating, not-Ready Pods sitting on a current-hash anchor, the budget
// in-flight-replacement figure. An unscheduled new Pod not yet on its anchor is not counted, which is safe
// because canEmitNextSubStep gates on currentBatchScheduled before the budget, so an unscheduled batch holds
// regardless of the count.
//
// If the cache is stale it can only show a dead Pod as still running, which lowers the missing old-version
// count, never raises it, so a drain never exceeds budget. This holds because old anchors are delete-only
// during the update, so their Pod set only shrinks and a creation can never be missed.
func (r _resource) gatherStandalonePodCounts(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int, entries []grovecorev1alpha1.PodGangEntry, standalonePCLQByComponent map[string]grovecorev1alpha1.PodClique) (standalonePCLQPodCounts, error) {
	rnr := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}
	epochByAnchorPodGangName := componentutils.EpochByAnchorPodGangName(entries, rnr)
	currentHashAnchorEpochs := currentHashAnchorEpochSet(entries, *pcs.Status.CurrentGenerationHash)

	counts := standalonePCLQPodCounts{
		livePodCountsByAnchor: make(map[cliqueAndEpochKey]anchorLivePods),
		nonTerminatingByPCLQ:  make(map[string]int32, len(standalonePCLQByComponent)),
		newNotReadyByPCLQ:     make(map[string]int32, len(standalonePCLQByComponent)),
	}
	for cliqueName, pclq := range standalonePCLQByComponent {
		pods, err := componentutils.GetPCLQPods(ctx, r.client, pcs.Name, &pclq)
		if err != nil {
			return standalonePCLQPodCounts{}, groveerr.WrapError(err, errCodeListPods, component.OperationSync,
				fmt.Sprintf("could not list Pods for standalone PodClique %q under coherent update", cliqueName))
		}
		var nonTerminating, newNotReady int32
		for _, pod := range pods {
			if k8sutils.IsResourceTerminating(pod.ObjectMeta) {
				continue
			}
			podReady := k8sutils.IsPodReady(pod)
			// A Pod whose PodGang is not one of this replica's current anchor entries resolves to no epoch and is
			// skipped, and is not counted as existing capacity. This happens briefly for a Pod left on an anchor
			// the plan already pruned, before the PodClique reconciler issues its deletion. Counting it would
			// inflate the budget with capacity that is leaving and that the drain cannot select.
			epoch, onKnownAnchor := epochByAnchorPodGangName[pod.Labels[apicommon.LabelPodGang]]
			if !onKnownAnchor {
				continue
			}
			nonTerminating++
			key := cliqueAndEpochKey{clique: cliqueName, epoch: epoch}
			livePods := counts.livePodCountsByAnchor[key]
			livePods.running++
			if !podReady {
				livePods.notReady++
				if currentHashAnchorEpochs.Has(epoch) {
					newNotReady++
				}
			}
			counts.livePodCountsByAnchor[key] = livePods
		}
		counts.nonTerminatingByPCLQ[cliqueName] = nonTerminating
		counts.newNotReadyByPCLQ[cliqueName] = newNotReady
	}
	return counts, nil
}

// currentHashAnchorEpochSet returns the epochs of the current-hash anchor entries, the anchors that carry
// new-revision content.
func currentHashAnchorEpochSet(entries []grovecorev1alpha1.PodGangEntry, currentHash string) sets.Set[string] {
	epochs := sets.New[string]()
	for i := range entries {
		if entries[i].Role == grovecorev1alpha1.PodGangEntryRoleAnchor && entries[i].PodCliqueSetGenerationHash == currentHash {
			epochs.Insert(entries[i].Epoch)
		}
	}
	return epochs
}

// pcsgReplicaInfo holds the health and roll state of one PodCliqueScalingGroup replica.
type pcsgReplicaInfo struct {
	// index is the replica index of the PodCliqueScalingGroup this info describes.
	index int
	// state is the replica health from ComputePCSGReplicaState over its member PodCliques.
	state componentutils.PCSGReplicaState
	// atCurrentHash is true when a current-hash entry commits this replica index, meaning the plan has
	// moved it to the current generation. It does not imply the replica's Pods have rolled or are Ready, a
	// committed-but-not-Ready index is exactly what the newNotReady budget term counts.
	atCurrentHash bool
}

// gatherPCSGReplicaInfos returns the pcsgReplicaInfo for each present replica index of every in-scope
// gatherPCSGReplicaInfos returns one pcsgReplicaInfo for every desired replica index [0, Spec.Replicas) of
// each in-scope PodCliqueScalingGroup under update, keyed by PCSG component name. It lists each PCSG's member
// PodCliques and groups them by replica index. Every desired index is inventoried, so an index with no
// member PodCliques, or whose members are all terminating during a gang restart, is classified Pending by
// ComputePCSGReplicaState rather than dropped. Dropping it would let the anchor open with fewer than
// MinAvailable replicas. atCurrentHash is set when a current-hash entry commits the index.
func (r _resource) gatherPCSGReplicaInfos(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, entries []grovecorev1alpha1.PodGangEntry, pcsgByComponent map[string]grovecorev1alpha1.PodCliqueScalingGroup) (map[string][]pcsgReplicaInfo, error) {
	currentHash := *pcs.Status.CurrentGenerationHash
	infosByComponent := make(map[string][]pcsgReplicaInfo, len(pcsgByComponent))
	for componentName, pcsg := range pcsgByComponent {
		pcsgObjKey := client.ObjectKey{Namespace: pcs.Namespace, Name: pcsg.Name}
		memberPCLQs, err := componentutils.GetPCLQsByOwner(ctx, r.client, constants.KindPodCliqueScalingGroup, pcsgObjKey,
			map[string]string{apicommon.LabelPodCliqueScalingGroup: pcsg.Name})
		if err != nil {
			return nil, groveerr.WrapError(err, errCodeListPods, component.OperationSync,
				fmt.Sprintf("could not list member PodCliques for PodCliqueScalingGroup %q under coherent update", componentName))
		}
		committedIndices := currentHashCommittedPCSGReplicaIndices(entries, componentName, currentHash)
		membersByReplicaIndex, err := componentutils.GroupPCLQsByPCSGReplicaIndex(memberPCLQs)
		if err != nil {
			return nil, groveerr.WrapError(err, errCodeExtractPCSGName, component.OperationSync,
				fmt.Sprintf("could not group member PodCliques by replica index for PodCliqueScalingGroup %q under coherent update", componentName))
		}
		infos := make([]pcsgReplicaInfo, 0, pcsg.Spec.Replicas)
		for replicaIndex := 0; replicaIndex < int(pcsg.Spec.Replicas); replicaIndex++ {
			members := membersByReplicaIndex[replicaIndex]
			infos = append(infos, pcsgReplicaInfo{
				index:         replicaIndex,
				state:         componentutils.ComputePCSGReplicaState(members, len(pcsg.Spec.CliqueNames)),
				atCurrentHash: committedIndices.Has(replicaIndex),
			})
		}
		infosByComponent[componentName] = infos
	}
	return infosByComponent, nil
}

// currentHashCommittedPCSGReplicaIndices returns the replica indices of one PodCliqueScalingGroup that
// current-hash entries commit, meaning the plan has moved them to the current generation. Membership in a
// current-hash entry is a commitment in the PodGangMap, not an assertion that the replica's Pods have rolled
// or are Ready.
func currentHashCommittedPCSGReplicaIndices(entries []grovecorev1alpha1.PodGangEntry, pcsgName, currentHash string) sets.Set[int] {
	indices := sets.New[int]()
	for i := range entries {
		if entries[i].PodCliqueSetGenerationHash != currentHash {
			continue
		}
		for _, idx := range entries[i].PCSGReplicaIndices[pcsgName] {
			indices.Insert(int(idx))
		}
	}
	return indices
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
func numMissingOldVersionPodsByStandalonePCLQ(entries []grovecorev1alpha1.PodGangEntry, currentHash string, counts standalonePCLQPodCounts) map[string]int32 {
	missingOldVersionPodsByClique := make(map[string]int32)
	for i := range entries {
		entry := entries[i]
		if entry.Role != grovecorev1alpha1.PodGangEntryRoleAnchor || entry.PodCliqueSetGenerationHash == currentHash {
			continue
		}
		for cliqueName, committedPodCount := range entry.PodCliques {
			// Only in-scope standalone PodCliques are counted. Presence in nonTerminatingByPCLQ marks scope.
			if _, inScope := counts.nonTerminatingByPCLQ[cliqueName]; !inScope {
				continue
			}
			if running := counts.livePodCountsByAnchor[cliqueAndEpochKey{clique: cliqueName, epoch: entry.Epoch}].running; committedPodCount > running {
				missingOldVersionPodsByClique[cliqueName] += committedPodCount - running
			}
		}
	}
	return missingOldVersionPodsByClique
}

// /*
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
// */

package podgangmap

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"

	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// buildCoherentUpdateEntries computes one sub-step of a coherent update for the given PCS replica,
// deriving everything from the current-hash PodGangMap Spec.Entries plus live PodGang readiness. It
// reads and writes no persisted status.
//
// It decides among three states, cheapest first:
//   - complete: every in-scope component is fully rolled (checked from the entries alone, no I/O).
//     This is the terminal state; it returns immediately so a done replica does no readiness List each
//     reconcile while it lingers in CurrentlyUpdating before the orchestrator moves on.
//   - waiting: a batch has been emitted for the current generation but its PodGangs are not yet ready
//     (checked via live PodGangs). Returns the current entries unchanged; the next sub-step waits.
//   - proceed: nothing emitted yet, or the latest batch is ready. Computes and materialises the next
//     sub-step.
//
// The caller (orchestrator) is the authority on whether the replica's update is complete, so this
// function does not signal completeness; it just returns the entries that should exist.
//
// The dispatch in runSyncFlow routes an empty PGM to the bootstrap path before the coherent case, so
// this runs on a non-empty PGM whose entries already carry the correct (old) generation hash. Hence
// no rebuild-from-owner-status pre-sync is needed. Because the whole progress is reconstructed from
// Spec every reconcile, a mid-update re-update to a newer generation needs no special handling: the
// previous generation's entries are simply old-hash content to drain.
func buildCoherentUpdateEntries(ctx context.Context, cl client.Client, pcsReplicaIndex int, syncSnap *syncSnapshot, clk clock.Clock) ([]grovecorev1alpha1.PodGangEntry, error) {
	entries := syncSnap.existingPGMByReplica[pcsReplicaIndex].Spec.Entries
	planner := newSubStepPlanner(syncSnap, pcsReplicaIndex, entries, clk)

	prog, err := planner.ascertainUpdateProgress()
	if err != nil {
		return nil, err
	}

	if allComponentsFullyRolled(prog.rolledCounts, planner.replicas) {
		return entries, nil
	}

	// waiting: if a batch has been emitted for the current generation and its PodGangs are not yet
	// ready, do not emit the next one. A nil latestEpoch means nothing has been emitted yet.
	latestEpoch, err := componentutils.LatestEpochForGenerationHash(entries, *syncSnap.pcs.Status.CurrentGenerationHash)
	if err != nil {
		return nil, groveerr.WrapError(err, errCodeInvalidEpochLabel, component.OperationSync,
			fmt.Sprintf("failed to derive latest current-hash epoch for PodCliqueSet %s replica %d", syncSnap.pcs.Name, pcsReplicaIndex))
	}
	if latestEpoch != nil {
		ready, err := componentutils.AllPodGangsAtEpochEverReady(ctx, cl, syncSnap.pcs.Namespace, syncSnap.pcs.Name, int32(pcsReplicaIndex), *latestEpoch)
		if err != nil {
			return nil, groveerr.WrapError(err, errCodeListPodGangs, component.OperationSync,
				fmt.Sprintf("failed to check readiness of epoch %s for PodCliqueSet %s replica %d", *latestEpoch, syncSnap.pcs.Name, pcsReplicaIndex))
		}
		if !ready {
			return entries, nil
		}
	}

	// MaxUnavailable budget gate: before emitting the next sub-step, every in-scope component must be
	// at or above its MaxUnavailable availability floor (replicas - maxUnavailable) on live
	// readiness/availability. A violation (e.g. external preemption between sub-steps) is a transient
	// wait, not a failure: signal a soft requeue so the update does not advance until availability
	// recovers.
	if err = planner.maxUnavailabilityBudgetViolated(syncSnap, pcsReplicaIndex); err != nil {
		return nil, groveerr.WrapError(err, groveerr.ErrCodeContinueReconcileAndRequeue, component.OperationSync,
			fmt.Sprintf("cannot advance coherent update for PodCliqueSet %s replica %d: waiting for availability to recover", syncSnap.pcs.Name, pcsReplicaIndex))
	}

	// proceed to compute the next sub-step: nothing emitted yet, or the latest batch is ready.
	ss, err := planner.next(prog)
	if err != nil {
		return nil, err
	}
	if ss == nil {
		return entries, nil
	}
	return planner.entriesAfterSubStep(*ss), nil
}

// updateProgress is the in-memory view of how far the coherent update has progressed. It is derived
// each reconcile from the current-hash Spec.Entries and never persisted. It holds exactly what next
// branches on.
type updateProgress struct {
	// rolledCounts is the total rolled count per in-scope component across all current-hash entries.
	// It is computed only after the readiness gate passed, so every current-hash entry is
	// rolled-and-ready and contributes unconditionally.
	rolledCounts map[string]int32
	// anchorBearingStepsDone is the number of anchor-bearing steps whose per-component target is met.
	anchorBearingStepsDone int32
	// currentAnchorBearingStepRolled is how much the currently-open anchor-bearing step has rolled per
	// component (rolled in the anchor-bearing region beyond the completed steps).
	currentAnchorBearingStepRolled map[string]int32
	// leftoverRolled is how much of each component's leftover has been rolled.
	leftoverRolled map[string]int32
	// mostRecentAnchorEpoch is the epoch of the most recent anchor entry, the anchor that tail and
	// leftover sub-steps subsume their standalone pods into. Empty when there is no anchor entry yet.
	mostRecentAnchorEpoch string
}

// ascertainUpdateProgress derives the update position from the current-hash entries and the plan. It
// sums the rolled count per component, splits each component's total into the anchor-bearing region
// ([0, numAnchorBearingSteps*anchorBearingStepTarget)) and the leftover region (the rest), and counts
// fully-complete anchor-bearing steps as the min over components of
// floor(anchorRegionRolled / anchorBearingStepTarget) — a step is complete only when every component
// has met that step's target, matching how steps are emitted. What remains in the current
// anchor-bearing step and how much leftover is rolled follow directly. Only the most recent anchor
// epoch is read from the entries (for the subsume target); everything else is arithmetic.
func (p *subStepPlanner) ascertainUpdateProgress() (updateProgress, error) {
	currentHash := *p.pcs.Status.CurrentGenerationHash

	rolled := map[string]int32{}
	var (
		mostRecentAnchorEpoch string
		mostRecentAnchorValue int64
	)
	for i := range p.entries {
		if p.entries[i].PodCliqueSetGenerationHash != currentHash {
			continue
		}
		addEntryCounts(rolled, p.entries[i], p.mvu)
		if p.entries[i].IsEpochAnchor {
			v, err := parseEpoch(p.entries[i])
			if err != nil {
				return updateProgress{}, err
			}
			if mostRecentAnchorEpoch == "" || v > mostRecentAnchorValue {
				mostRecentAnchorEpoch, mostRecentAnchorValue = p.entries[i].Labels[apicommon.LabelEpoch], v
			}
		}
	}

	// Split each component's rolled total into the anchor-bearing region and the leftover region.
	anchorRegionRolled := map[string]int32{}
	leftoverRolled := map[string]int32{}
	for c := range p.replicas {
		anchorRegionCap := p.numAnchorBearingSteps * p.anchorBearingStepTarget[c]
		if rolled[c] > anchorRegionCap {
			anchorRegionRolled[c] = anchorRegionCap
			leftoverRolled[c] = rolled[c] - anchorRegionCap
		} else {
			anchorRegionRolled[c] = rolled[c]
		}
	}

	// A step counts as done only when every component has met that step's target, so the number of
	// fully-complete anchor-bearing steps is the min over components of floor(rolled/target).
	stepsDone := p.numAnchorBearingSteps
	for c := range p.replicas {
		if done := anchorRegionRolled[c] / p.anchorBearingStepTarget[c]; done < stepsDone {
			stepsDone = done
		}
	}

	// What the current anchor-bearing step has rolled is the anchor-region rolled beyond the completed steps.
	currentStepRolled := map[string]int32{}
	for c := range p.replicas {
		currentStepRolled[c] = anchorRegionRolled[c] - stepsDone*p.anchorBearingStepTarget[c]
	}

	return updateProgress{
		rolledCounts:                   rolled,
		anchorBearingStepsDone:         stepsDone,
		currentAnchorBearingStepRolled: currentStepRolled,
		leftoverRolled:                 leftoverRolled,
		mostRecentAnchorEpoch:          mostRecentAnchorEpoch,
	}, nil
}

// addEntryCounts adds the entry's in-scope component contributions (standalone PodClique pod counts
// and PodCliqueScalingGroup replica-index counts) into counts.
func addEntryCounts(counts map[string]int32, e grovecorev1alpha1.PodGangEntry, mvu *mvuTemplate) {
	for name, count := range e.PodCliques {
		if _, ok := mvu.standalonePCLQs[name]; ok {
			counts[name] += count
		}
	}
	for name, indices := range e.PCSGReplicaIndices {
		if _, ok := mvu.pcsgs[name]; ok {
			counts[name] += int32(len(indices))
		}
	}
}

// parseEpoch parses an entry's grove.io/epoch label to int64. A missing or non-numeric label is a
// contract violation (Grove is the sole writer of these labels).
func parseEpoch(e grovecorev1alpha1.PodGangEntry) (int64, error) {
	label, ok := e.Labels[apicommon.LabelEpoch]
	if !ok {
		return 0, groveerr.New(errCodeMissingEpochLabel, component.OperationSync,
			fmt.Sprintf("PodGangMap entry %s is missing the %s label", e.Name, apicommon.LabelEpoch))
	}
	value, err := strconv.ParseInt(label, 10, 64)
	if err != nil {
		return 0, groveerr.WrapError(err, errCodeInvalidEpochLabel, component.OperationSync,
			fmt.Sprintf("PodGangMap entry %s has a non-numeric %s label %q", e.Name, apicommon.LabelEpoch, label))
	}
	return value, nil
}

// allComponentsFullyRolled reports whether every in-scope component has rolledCount == replicas.
func allComponentsFullyRolled(rolledCounts, replicas map[string]int32) bool {
	for c, want := range replicas {
		if rolledCounts[c] != want {
			return false
		}
	}
	return true
}

// subStep describes the batch of PodGangMap changes one sub-step of a coherent update emits: the
// old-hash content it takes down and the new-hash content it adds, all under one epoch. Standalone
// PodClique quantities are counts (which pods materialise or die is the PodClique reconciler's
// concern); PodCliqueScalingGroup quantities are replica indices (a PCSG replica is an identity).
type subStep struct {
	// epoch is the grove.io/epoch for this sub-step's batch. Every new entry it emits carries it.
	epoch string
	// dependsOn lists the epochs this batch's PodGangs are scheduled after (today the single most
	// recent epoch across existing entries at plan time). It is empty only for the very first batch
	// of the update.
	dependsOn []string
	// anchorPCSGReplicaIndices are the PCSG floor replica indices the anchor entry carries, keyed by
	// PCSG name. Non-empty only when this sub-step opens an anchor-bearing step. The anchor's
	// standalone PodClique floor is not carried here; it is minAvailable per PodClique, read from
	// mvuTemplate at materialisation.
	anchorPCSGReplicaIndices map[string][]int32
	// subsumeStandalonePCLQCounts is the standalone PodClique pod count added to an existing anchor
	// this sub-step, keyed by PodClique name.
	subsumeStandalonePCLQCounts map[string]int32
	// subsumeAnchorEpoch is the epoch of the existing anchor that subsumeStandalonePCLQCounts grow.
	subsumeAnchorEpoch string
	// tailPCSGReplicaIndices are the PCSG replica indices this sub-step rolls to the new hash, keyed
	// by PCSG name. All indices of one PCSG collapse into a single non-anchor entry that the PodGang
	// materializer later expands into one PodGang per index.
	tailPCSGReplicaIndices map[string][]int32
	// drainStandalonePCLQCounts is the old-hash standalone PodClique pod count to take down this
	// sub-step, keyed by PodClique name.
	drainStandalonePCLQCounts map[string]int32
	// drainPCSGReplicaIndices are the old-hash PCSG replica indices to take down this sub-step, keyed
	// by PCSG name.
	drainPCSGReplicaIndices map[string][]int32
}

// subStepPlanner holds the state the coherent-update planner works over for one PCS replica: the PCS
// and its MVU template, the current entries, and the step-plan arithmetic derived from them. It is
// built once per reconcile by newSubStepPlanner and never stored. Its methods decide and build the
// next sub-step.
type subStepPlanner struct {
	// clk supplies the epoch for a newly emitted sub-step.
	clk clock.Clock
	// pcs is the PodCliqueSet being updated (read for its name and generation hash).
	pcs *grovecorev1alpha1.PodCliqueSet
	// pcsReplicaIndex is the PCS replica this planner works on.
	pcsReplicaIndex int
	// mvu is the MVU template frozen at update start (the in-scope components and their minAvailable).
	mvu *mvuTemplate
	// entries is the current PodGangMap entry set, read-only during planning.
	entries []grovecorev1alpha1.PodGangEntry
	// replicas is the live child replica count per component, read fresh every reconcile so a scale
	// operation on a non-updating boundary is absorbed.
	replicas map[string]int32
	// minAvailable is the frozen MVU floor per component (from mvuTemplate, fixed at update start).
	minAvailable map[string]int32
	// maxUnavailable bounds how many of a component a single sub-step may take down (current template).
	maxUnavailable map[string]int32
	// numAnchorBearingSteps is min over components of floor(replicas/minAvailable).
	numAnchorBearingSteps int32
	// anchorBearingStepTarget is how many of each component one anchor-bearing step rolls
	// (minAvailable plus an even share of the tail).
	anchorBearingStepTarget map[string]int32
	// leftover is what remains of each component after all anchor-bearing steps, drained by the
	// single leftover step.
	leftover map[string]int32
}

// newSubStepPlanner builds the planner for one PCS replica: live child replica counts, the frozen
// minAvailable from mvuTemplate, the current-template maxUnavailable, and the derived step-plan
// values (numAnchorBearingSteps, anchorBearingStepTarget, leftover).
func newSubStepPlanner(syncSnap *syncSnapshot, pcsReplicaIndex int, entries []grovecorev1alpha1.PodGangEntry, clk clock.Clock) *subStepPlanner {
	mvu := syncSnap.mvuTemplate.clone()
	components := mvu.componentNames()
	minAvailable := mvu.minAvailableByComponent()
	maxUnavailable := componentutils.GetMaxUnavailableForComponents(syncSnap.pcs, components)
	replicas := syncSnap.liveReplicasForComponentsUnderUpdate(pcsReplicaIndex)

	// numAnchorBearingSteps is the min over components of floor(replicas/minAvailable): the largest
	// step count for which every component can supply a full minAvailable to each step's anchor. The
	// webhook guarantees replicas >= minAvailable >= 1, so this is always >= 1.
	numAnchorBearingSteps := replicas[components[0]] / minAvailable[components[0]]
	for _, c := range components[1:] {
		if steps := replicas[c] / minAvailable[c]; steps < numAnchorBearingSteps {
			numAnchorBearingSteps = steps
		}
	}

	// Each anchor-bearing step rolls minAvailable plus an even share of the tail; leftover is what
	// remains after all anchor-bearing steps, drained by the single leftover step.
	anchorBearingStepTarget := make(map[string]int32, len(components))
	leftover := make(map[string]int32, len(components))
	for _, c := range components {
		tailPerStep := (replicas[c] - numAnchorBearingSteps*minAvailable[c]) / numAnchorBearingSteps
		anchorBearingStepTarget[c] = minAvailable[c] + tailPerStep
		leftover[c] = replicas[c] - numAnchorBearingSteps*anchorBearingStepTarget[c]
	}

	return &subStepPlanner{
		pcs:                     syncSnap.pcs,
		mvu:                     mvu,
		pcsReplicaIndex:         pcsReplicaIndex,
		entries:                 entries,
		clk:                     clk,
		replicas:                replicas,
		minAvailable:            minAvailable,
		maxUnavailable:          maxUnavailable,
		numAnchorBearingSteps:   numAnchorBearingSteps,
		anchorBearingStepTarget: anchorBearingStepTarget,
		leftover:                leftover,
	}
}

// maxUnavailabilityBudgetViolated returns a non-nil error naming the first in-scope component whose
// live availability is below its MaxUnavailable budget floor (replicas - maxUnavailable), or nil
// when every in-scope component is at or above its floor. A non-nil return is the violation signal;
// the caller wraps it as a grove error. It iterates the mvuTemplate (the frozen update scope) and
// looks up each component's live child by its generated FQN, so components that are not targets of
// this update can never block advancement. A standalone PodClique is measured against
// Status.ReadyReplicas (a pod count); a PodCliqueScalingGroup against Status.AvailableReplicas (a
// replica count; a PCSG replica is available when all constituent PodCliques have ReadyReplicas at
// or above their MinAvailable). A missing live child for an in-scope component is a violation: we
// cannot confirm availability of a component we are supposed to be updating, so we stall.
func (p *subStepPlanner) maxUnavailabilityBudgetViolated(syncSnap *syncSnapshot, pcsReplicaIndex int) error {
	pcsNameReplica := apicommon.ResourceNameReplica{Name: p.pcs.Name, Replica: pcsReplicaIndex}

	pclqByName := lo.KeyBy(syncSnap.existingStandalonePCLQsByReplica[pcsReplicaIndex], func(pclq grovecorev1alpha1.PodClique) string { return pclq.Name })
	pcsgByName := lo.KeyBy(syncSnap.existingPCSGsByReplica[pcsReplicaIndex], func(pcsg grovecorev1alpha1.PodCliqueScalingGroup) string { return pcsg.Name })

	for name := range p.mvu.standalonePCLQs {
		fqn := apicommon.GeneratePodCliqueName(pcsNameReplica, name)
		maxUnavailableFloor := p.replicas[name] - p.maxUnavailable[name]
		pclq, ok := pclqByName[fqn]
		if !ok {
			return fmt.Errorf("in-scope standalone PodClique %q has no live resource for replica %d; cannot confirm availability", name, pcsReplicaIndex)
		}
		if pclq.Status.ReadyReplicas < maxUnavailableFloor {
			return fmt.Errorf("MaxUnavailable budget violated for standalone PodClique %q: ReadyReplicas %d < maxUnavailableFloor %d (replicas %d - maxUnavailable %d)",
				name, pclq.Status.ReadyReplicas, maxUnavailableFloor, p.replicas[name], p.maxUnavailable[name])
		}
	}

	for name := range p.mvu.pcsgs {
		fqn := apicommon.GeneratePodCliqueScalingGroupName(pcsNameReplica, name)
		maxUnavailableFloor := p.replicas[name] - p.maxUnavailable[name]
		pcsg, ok := pcsgByName[fqn]
		if !ok {
			return fmt.Errorf("in-scope PodCliqueScalingGroup %q has no live resource for replica %d; cannot confirm availability", name, pcsReplicaIndex)
		}
		if pcsg.Status.AvailableReplicas < maxUnavailableFloor {
			return fmt.Errorf("MaxUnavailable budget violated for PodCliqueScalingGroup %q: AvailableReplicas %d < maxUnavailableFloor %d (replicas %d - maxUnavailable %d)",
				name, pcsg.Status.AvailableReplicas, maxUnavailableFloor, p.replicas[name], p.maxUnavailable[name])
		}
	}

	return nil
}

// next decides the next sub-step from the already-ascertained progress, or returns nil when every
// in-scope component is fully rolled. It is called only after the readiness gate passed (nothing
// emitted yet, or the latest batch is ready).
func (p *subStepPlanner) next(prog updateProgress) (*subStep, error) {
	// An anchor-bearing step is partially rolled (not yet at target) => continue it with a tail sub-step.
	if p.currentAnchorBearingStepInProgress(prog) {
		return p.buildTailSubStep(prog)
	}
	// Otherwise open the next anchor-bearing step while any remain, then the single leftover step.
	if prog.anchorBearingStepsDone < p.numAnchorBearingSteps {
		return p.buildAnchorBearingSubStep(prog)
	}
	if p.anyLeftoverRemaining(prog.rolledCounts) {
		return p.buildLeftoverSubStep(prog)
	}
	return nil, nil
}

// currentAnchorBearingStepInProgress reports whether an anchor-bearing step is open but not yet at its
// target: some component has rolled part of the current step but not the full anchorBearingStepTarget.
func (p *subStepPlanner) currentAnchorBearingStepInProgress(prog updateProgress) bool {
	if prog.anchorBearingStepsDone >= p.numAnchorBearingSteps {
		return false
	}
	for c := range p.replicas {
		if prog.currentAnchorBearingStepRolled[c] > 0 && prog.currentAnchorBearingStepRolled[c] < p.anchorBearingStepTarget[c] {
			return true
		}
	}
	return false
}

// buildTailSubStep builds the next sub-step of the current anchor-bearing step: the next
// rollBudget-bounded tail slice, subsuming standalone PodClique pods into the current step's anchor
// and rolling tail PCSG replica indices.
//
// Worked example. A PodCliqueScalingGroup has replicas 80, minAvailable 3, maxUnavailable 4, so
// anchorBearingStepTarget = 8 across 10 anchor-bearing steps. Take the current step k = 0, whose
// contiguous index block is [0, 8). The anchor sub-step already rolled the first minAvailable, indices
// [0, 3), leaving 5 to roll.
//   - This sub-step: remaining = 5, rollBudget = min(4, 5) = 4. Indices start at (k+1)*8 - 5 = 3, so
//     it rolls indices [3, 7).
//   - Next sub-step: remaining = 1, rollBudget = min(4, 1) = 1. Indices start at (k+1)*8 - 1 = 7, so
//     it rolls index [7, 8). The block is now fully rolled and the next call opens step k = 1.
func (p *subStepPlanner) buildTailSubStep(prog updateProgress) (*subStep, error) {
	epoch := strconv.FormatInt(p.clk.Now().UnixNano(), 10)
	k := prog.anchorBearingStepsDone // current anchor-bearing step's 0-based index
	remaining := map[string]int32{}
	for c := range p.replicas {
		if gap := p.anchorBearingStepTarget[c] - prog.currentAnchorBearingStepRolled[c]; gap > 0 {
			remaining[c] = gap
		}
	}
	// PCSG tail indices continue this step's contiguous block just past what it has already rolled:
	// the block ends at (k+1)*anchorBearingStepTarget, and remaining remain, so the next unrolled
	// index is (k+1)*anchorBearingStepTarget - remaining.
	pcsgIndexStartFn := func(componentName string, remainingReplicas int32) int32 {
		return (k+1)*p.anchorBearingStepTarget[componentName] - remainingReplicas
	}
	return p.buildNonAnchorSubStep(epoch, prog.mostRecentAnchorEpoch, remaining, pcsgIndexStartFn)
}

// buildNonAnchorSubStep assembles a sub-step that is not the anchor-creating one: the tail sub-steps
// of an anchor-bearing step and the sub-steps of the leftover step. remaining is the per-component
// work this sub-step must roll; the caller computes it for the step being filled. For each component
// it rolls a rollBudget of min(maxUnavailable, remaining). A PodCliqueScalingGroup gets tail PodGangs
// at indices pcsgIndexStart(component, remaining) .. +rollBudget; a standalone PodClique subsumes
// rollBudget pods into the existing anchor at anchorEpoch. The old-hash equivalent is drained in both
// cases. It returns nil when remaining is empty (nothing left to roll) and creates no anchor.
func (p *subStepPlanner) buildNonAnchorSubStep(epoch, anchorEpoch string, remaining map[string]int32, pcsgIndexStartFn func(componentName string, remainingReplicas int32) int32) (*subStep, error) {
	if len(remaining) == 0 {
		return nil, nil
	}
	dependsOn, err := p.dependsOnLatestEpoch()
	if err != nil {
		return nil, err
	}
	ss := &subStep{
		epoch:                       epoch,
		dependsOn:                   dependsOn,
		subsumeAnchorEpoch:          anchorEpoch,
		subsumeStandalonePCLQCounts: map[string]int32{},
		drainStandalonePCLQCounts:   map[string]int32{},
		tailPCSGReplicaIndices:      map[string][]int32{},
		drainPCSGReplicaIndices:     map[string][]int32{},
	}
	for componentName, remainingReplicas := range remaining {
		rollBudget := min(p.maxUnavailable[componentName], remainingReplicas)
		// check if the component is a PCSG or a standalone PCLQ
		if _, isPCSG := p.mvu.pcsgs[componentName]; isPCSG {
			indices := lo.RangeFrom(pcsgIndexStartFn(componentName, remainingReplicas), int(rollBudget))
			ss.tailPCSGReplicaIndices[componentName] = indices
			ss.drainPCSGReplicaIndices[componentName] = indices
		} else {
			ss.subsumeStandalonePCLQCounts[componentName] = rollBudget
			ss.drainStandalonePCLQCounts[componentName] = rollBudget
		}
	}
	return ss, nil
}

// dependsOnLatestEpoch returns the DependsOn slice for a newly emitted batch: the single latest
// current-hash epoch, or empty for the first batch of the update.
func (p *subStepPlanner) dependsOnLatestEpoch() ([]string, error) {
	latest, err := componentutils.LatestEpochForGenerationHash(p.entries, *p.pcs.Status.CurrentGenerationHash)
	if err != nil {
		return nil, groveerr.WrapError(err, errCodeInvalidEpochLabel, component.OperationSync,
			fmt.Sprintf("failed to derive DependsOn latest epoch for PodCliqueSet %s", p.pcs.Name))
	}
	if latest == nil {
		return nil, nil
	}
	return []string{*latest}, nil
}

// buildAnchorBearingSubStep builds the floor-only first sub-step of a new anchor-bearing step: an
// anchor carrying minAvailable of every component, with distinct PCSG floor indices allocated to
// this anchor. It drains the old-hash equivalent of that floor. No tail is emitted here; the tail
// drains in subsequent buildTailSubStep sub-steps.
func (p *subStepPlanner) buildAnchorBearingSubStep(prog updateProgress) (*subStep, error) {
	dependsOn, err := p.dependsOnLatestEpoch()
	if err != nil {
		return nil, err
	}
	epoch := strconv.FormatInt(p.clk.Now().UnixNano(), 10)

	// This anchor is the k-th anchor-bearing step (0-based). It claims PCSG indices in the range
	// [k*anchorBearingStepTarget, k*anchorBearingStepTarget + minAvailable), so anchors never collide.
	k := prog.anchorBearingStepsDone
	pcsgFloorIndices := make(map[string][]int32, len(p.mvu.pcsgs))
	for name, minAvail := range p.mvu.pcsgs {
		pcsgFloorIndices[name] = lo.RangeFrom(k*p.anchorBearingStepTarget[name], int(minAvail))
	}

	return &subStep{
		epoch:     epoch,
		dependsOn: dependsOn,
		// The anchor carries minAvailable PCSG replicas at the allocated indices, and it takes down
		// the same indices on the old hash (identity-preserving swap). The standalone PodClique floor
		// is minAvailable per PodClique, added by entriesAfterSubStep from p.mvu, and its old-hash
		// equivalent is drained here.
		anchorPCSGReplicaIndices:  pcsgFloorIndices,
		drainStandalonePCLQCounts: p.mvu.standalonePCLQs,
		drainPCSGReplicaIndices:   pcsgFloorIndices,
	}, nil
}

// anyLeftoverRemaining reports whether any component still has leftover replicas to roll after the
// anchor-bearing steps. It is called only once all anchor-bearing steps are done, so any gap between
// rolledCounts and replicas is the leftover that the leftover step must still drain.
func (p *subStepPlanner) anyLeftoverRemaining(rolledCounts map[string]int32) bool {
	for c := range p.leftover {
		if rolledCounts[c] < p.replicas[c] {
			return true
		}
	}
	return false
}

// buildLeftoverSubStep builds a sub-step of the leftover step.
//
// After the anchor-bearing steps have rolled, some replicas can remain for a component (its
// leftover). A single leftover step drains all of them across one or more sub-steps. This method
// builds one such sub-step. The leftover indices of a component are the ones above every
// anchor-bearing step's block: [numAnchorBearingSteps*anchorBearingStepTarget, replicas). This range
// ends at replicas, so as sub-steps roll it the next unrolled index is replicas - remaining.
// Standalone PodClique leftover pods subsume into the most recent anchor.
func (p *subStepPlanner) buildLeftoverSubStep(prog updateProgress) (*subStep, error) {
	epoch := strconv.FormatInt(p.clk.Now().UnixNano(), 10)
	remaining := map[string]int32{}
	for c := range p.leftover {
		if gap := p.leftover[c] - prog.leftoverRolled[c]; gap > 0 {
			remaining[c] = gap
		}
	}
	pcsgIndexStartFn := func(componentName string, remainingReplicas int32) int32 {
		return p.replicas[componentName] - remainingReplicas
	}
	return p.buildNonAnchorSubStep(epoch, prog.mostRecentAnchorEpoch, remaining, pcsgIndexStartFn)
}

// entriesAfterSubStep returns the entry set that should exist after the sub-step. It works on a deep
// copy so the snapshot's PodGangMap is untouched: it drains the old-hash take-down, grows the target
// new-hash anchor with the subsumed standalone PodClique pods, appends the sub-step's new-hash anchor
// and non-anchor entries, then drops any entry drained to empty.
func (p *subStepPlanner) entriesAfterSubStep(ss subStep) []grovecorev1alpha1.PodGangEntry {
	entries := clonePodGangEntries(p.entries)
	currentHash := *p.pcs.Status.CurrentGenerationHash

	drainStandalonePCLQs(entries, currentHash, ss.drainStandalonePCLQCounts)
	drainPCSGIndices(entries, currentHash, ss.drainPCSGReplicaIndices)
	subsumeIntoAnchor(entries, ss.subsumeAnchorEpoch, ss.subsumeStandalonePCLQCounts)

	entries = append(entries, p.newHashEntriesForSubStep(ss)...)
	return removeEmptyEntries(entries)
}

// clonePodGangEntries returns a deep copy of the entries so the working set can be mutated without
// touching the snapshot's PodGangMap.
func clonePodGangEntries(entries []grovecorev1alpha1.PodGangEntry) []grovecorev1alpha1.PodGangEntry {
	cloned := make([]grovecorev1alpha1.PodGangEntry, len(entries))
	for i := range entries {
		entries[i].DeepCopyInto(&cloned[i])
	}
	return cloned
}

// drainStandalonePCLQs subtracts each PodClique's take-down count from the old-hash anchors, oldest
// generation first, until the count is exhausted. Standalone PodClique pods live only on anchors, so
// only old-hash anchor entries are touched. Draining oldest-epoch-first retires the oldest generation
// (V1) fully before touching a newer old one (V2), which matters when a re-update mid-update left more
// than one old-hash generation live. Pod counts are identity-free, so which anchor shrinks does not
// matter beyond that ordering.
func drainStandalonePCLQs(entries []grovecorev1alpha1.PodGangEntry, currentHash string, drainCounts map[string]int32) {
	if len(drainCounts) == 0 {
		return
	}
	oldHashAnchors := make([]*grovecorev1alpha1.PodGangEntry, 0, len(entries))
	for i := range entries {
		if entries[i].PodCliqueSetGenerationHash != currentHash && entries[i].IsEpochAnchor {
			oldHashAnchors = append(oldHashAnchors, &entries[i])
		}
	}
	slices.SortFunc(oldHashAnchors, func(a, b *grovecorev1alpha1.PodGangEntry) int {
		return int(mustParseEpochValue(a) - mustParseEpochValue(b))
	})
	for name, remaining := range drainCounts {
		for _, anchor := range oldHashAnchors {
			if remaining == 0 {
				break
			}
			take := min(anchor.PodCliques[name], remaining)
			anchor.PodCliques[name] -= take
			remaining -= take
		}
	}
}

// mustParseEpochValue parses the entry's epoch label to int64 for ordering. The label is written by
// Grove and validated before entries reach here, so a parse failure would be a programming error;
// treat an unparseable label as 0 to keep ordering total rather than panic in a drain.
func mustParseEpochValue(e *grovecorev1alpha1.PodGangEntry) int64 {
	v, err := strconv.ParseInt(e.Labels[apicommon.LabelEpoch], 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// drainPCSGIndices removes each PCSG's take-down replica indices from whichever old-hash entry carries
// them. A PCSG replica index is an identity that lives in exactly one entry, so no ordering or
// budgeting is needed.
func drainPCSGIndices(entries []grovecorev1alpha1.PodGangEntry, currentHash string, drainIndices map[string][]int32) {
	for i := range entries {
		if entries[i].PodCliqueSetGenerationHash == currentHash {
			continue
		}
		for name, indices := range drainIndices {
			entries[i].PCSGReplicaIndices[name] = removeIndices(entries[i].PCSGReplicaIndices[name], indices)
		}
	}
}

// removeIndices returns from without any element present in remove, preserving from's order.
func removeIndices(from, remove []int32) []int32 {
	if len(from) == 0 || len(remove) == 0 {
		return from
	}
	removeSet := sets.New(remove...)
	return slices.DeleteFunc(from, func(idx int32) bool {
		return removeSet.Has(idx)
	})
}

// subsumeIntoAnchor adds the subsumed standalone PodClique pod counts to the new-hash anchor at
// anchorEpoch. It is a no-op when there is nothing to subsume.
func subsumeIntoAnchor(entries []grovecorev1alpha1.PodGangEntry, anchorEpoch string, subsumeCounts map[string]int32) {
	if len(subsumeCounts) == 0 {
		return
	}
	for i := range entries {
		if entries[i].Labels[apicommon.LabelEpoch] != anchorEpoch {
			continue
		}
		if entries[i].PodCliques == nil {
			entries[i].PodCliques = make(map[string]int32, len(subsumeCounts))
		}
		for name, count := range subsumeCounts {
			entries[i].PodCliques[name] += count
		}
		return
	}
}

// newHashEntriesForSubStep builds the new-hash entries a sub-step adds: the anchor entry when the
// sub-step opens an anchor-bearing step (carrying the minAvailable standalone PodClique floor and its
// allocated PCSG floor indices), and one non-anchor entry per PodCliqueScalingGroup collecting that
// PCSG's rolled indices for this epoch. Every entry shares the sub-step epoch and DependsOn, and
// carries a salted name so entries within the shared epoch stay distinct.
func (p *subStepPlanner) newHashEntriesForSubStep(ss subStep) []grovecorev1alpha1.PodGangEntry {
	hash := *p.pcs.Status.CurrentGenerationHash
	var newEntries []grovecorev1alpha1.PodGangEntry
	salt := 0

	if len(ss.anchorPCSGReplicaIndices) > 0 {
		anchor := newPodGangEntry(p.pcs.Name, p.pcsReplicaIndex, fmt.Sprintf("%s-%d", ss.epoch, salt), hash, ss.epoch, ss.dependsOn)
		anchor.IsEpochAnchor = true
		anchor.PodCliques = maps.Clone(p.mvu.standalonePCLQs)
		anchor.PCSGReplicaIndices = ss.anchorPCSGReplicaIndices
		newEntries = append(newEntries, anchor)
		salt++
	}

	for name, indices := range ss.tailPCSGReplicaIndices {
		if len(indices) == 0 {
			continue
		}
		tail := newPodGangEntry(p.pcs.Name, p.pcsReplicaIndex, fmt.Sprintf("%s-%d", ss.epoch, salt), hash, ss.epoch, ss.dependsOn)
		tail.PCSGReplicaIndices = map[string][]int32{name: indices}
		newEntries = append(newEntries, tail)
		salt++
	}
	return newEntries
}

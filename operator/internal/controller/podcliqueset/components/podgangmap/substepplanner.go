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
	"fmt"
	"math"
	"strconv"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"github.com/samber/lo"
	"k8s.io/utils/clock"
)

// subStep is the work to perform in a single sub-step of a coherent update. A coherent update rolls a
// PCS replica over multiple steps, and rolls each step over one or more sub-steps that each stay within
// maxUnavailable. A subStep captures that sub-step's work as PodGangMap changes under one new epoch:
// the old-hash content to take down and the new-hash content to add. Standalone PodClique quantities
// are pod counts because which pods materialize or die is the PodClique reconciler's concern.
// PodCliqueScalingGroup quantities are replica indices because a PCSG replica is an identity that lives
// in exactly one PodGang.
type subStep struct {
	// epoch is the grove.io/epoch stamped on every new entry this sub-step emits.
	epoch string
	// dependsOn are the epochs this batch's PodGangs are scheduled after. It holds the single most
	// recent epoch across the existing entries at plan time, and is empty only for the first batch of
	// the update.
	dependsOn []string
	// opensAnchor is true when this sub-step opens an anchor-bearing step, so it creates a new anchor
	// entry carrying MinAvailable of every standalone PodClique plus anchorPCSGReplicaIndices. It is the
	// discriminator between an anchor sub-step and a tail or leftover sub-step, independent of whether the
	// update includes any PodCliqueScalingGroup.
	opensAnchor bool
	// anchorPCSGReplicaIndices are the PodCliqueScalingGroup replica indices the new anchor entry
	// carries, keyed by PCSG name. It is set only on an anchor sub-step and is empty when the update
	// includes no PodCliqueScalingGroups. The anchor's standalone PodClique count is not carried here. It
	// is MinAvailable per PodClique, read from the mvuTemplate when the entry is materialized.
	anchorPCSGReplicaIndices map[string][]int32
	// subsumeStandalonePCLQCounts are the standalone PodClique pod counts this sub-step adds to an
	// existing anchor entry, keyed by PodClique name.
	subsumeStandalonePCLQCounts map[string]int32
	// subsumeAnchorEpoch is the epoch of the existing anchor entry that subsumeStandalonePCLQCounts grow.
	subsumeAnchorEpoch string
	// tailPCSGReplicaIndices are the PodCliqueScalingGroup replica indices this sub-step rolls to the
	// new hash outside the anchor, keyed by PCSG name. The indices of one PCSG collapse into a single
	// tail entry that the PodGang materializer later expands into one PodGang per index.
	tailPCSGReplicaIndices map[string][]int32
	// drainStandalonePCLQCounts are the old-hash standalone PodClique pod counts this sub-step takes
	// down, keyed by PodClique name.
	drainStandalonePCLQCounts map[string]int32
	// drainPCSGReplicaIndices are the old-hash PodCliqueScalingGroup replica indices this sub-step takes
	// down, keyed by PCSG name.
	drainPCSGReplicaIndices map[string][]int32
}

// String renders the sub-step in a compact single line form for tracing.
func (s subStep) String() string {
	return fmt.Sprintf("subStep(epoch=%s opensAnchor=%t anchorPCSG=%v subsumePCLQ=%v subsumeAnchorEpoch=%s tailPCSG=%v drainPCLQ=%v drainPCSG=%v dependsOn=%v)",
		s.epoch, s.opensAnchor, s.anchorPCSGReplicaIndices, s.subsumeStandalonePCLQCounts, s.subsumeAnchorEpoch, s.tailPCSGReplicaIndices, s.drainStandalonePCLQCounts, s.drainPCSGReplicaIndices, s.dependsOn)
}

// drainCountByComponent returns how many replicas of each in-scope component this sub-step takes down.
func (s subStep) drainCountByComponent() map[string]int32 {
	drainByComponent := make(map[string]int32, len(s.drainStandalonePCLQCounts)+len(s.drainPCSGReplicaIndices))
	for componentName, count := range s.drainStandalonePCLQCounts {
		drainByComponent[componentName] = count
	}
	for componentName, indices := range s.drainPCSGReplicaIndices {
		drainByComponent[componentName] = int32(len(indices))
	}
	return drainByComponent
}

// drainsNothing reports whether this sub-step takes no replicas down. A non-anchor sub-step lands here when
// headroom capping leaves nothing to roll this reconcile, so the roll waits instead of emitting an empty
// sub-step.
func (s subStep) drainsNothing() bool {
	for _, count := range s.drainStandalonePCLQCounts {
		if count > 0 {
			return false
		}
	}
	for _, indices := range s.drainPCSGReplicaIndices {
		if len(indices) > 0 {
			return false
		}
	}
	return true
}

// stepPlan is the step-level decomposition of a coherent update, computed once per reconcile from the
// live replica counts and the frozen MinAvailable. A coherent update rolls each component over
// numAnchorBearingSteps anchor-bearing steps plus a single leftover step. The planner reads this to
// identify and size each sub-step.
type stepPlan struct {
	// numAnchorBearingSteps is the number of anchor-bearing steps, the min over components of
	// floor(desiredReplicas/MinAvailable).
	numAnchorBearingSteps int32
	// anchorBearingStepTarget is how many of each component one anchor-bearing step rolls, MinAvailable
	// plus an even share of the tail, keyed by component name.
	anchorBearingStepTarget map[string]int32
	// leftover is how many of each component remain after all anchor-bearing steps, keyed by component
	// name, drained by the single leftover step.
	leftover map[string]int32
}

// String renders the step plan in a compact single line form for tracing.
func (p stepPlan) String() string {
	return fmt.Sprintf("stepPlan(numAnchorBearingSteps=%d anchorBearingStepTarget=%v leftover=%v)",
		p.numAnchorBearingSteps, p.anchorBearingStepTarget, p.leftover)
}

// subStepPlanner plans the next sub-step of a coherent update for one PCS replica. It is built once per
// reconcile by newSubStepPlanner and never stored. It holds the update inputs and the step plan, and
// its methods decide and build the next sub-step against that plan.
type subStepPlanner struct {
	// clk supplies the epoch for a newly emitted sub-step.
	clk clock.Clock
	// pcs is the PodCliqueSet being updated, read for its name and generation hash.
	pcs *grovecorev1alpha1.PodCliqueSet
	// pcsReplicaIndex is the PCS replica this planner works on.
	pcsReplicaIndex int
	// mvu is the Minimum Updateable Unit template frozen at update start, holding the in-scope
	// components and their MinAvailable.
	mvu *mvuTemplate
	// entries is the current PodGangMap entry set, read-only during planning.
	entries []grovecorev1alpha1.PodGangEntry
	// desiredReplicas is the child replica count per component (sourced from spec.Replicas), read fresh every
	// reconcile so a scale operation on a non-updating boundary is absorbed.
	desiredReplicas map[string]int32
	// maxUnavailableByComponent bounds how many of a component a single sub-step may take down, from the
	// current template.
	maxUnavailableByComponent map[string]int32
	// plan is the step-level decomposition the sub-step methods work against.
	plan stepPlan
}

// newSubStepPlanner builds the planner for one PCS replica from the in-scope live replica counts and the
// current-template maxUnavailable, and computes the step plan the planner works against.
func newSubStepPlanner(syncSnap *syncSnapshot, pcsReplicaIndex int, entries []grovecorev1alpha1.PodGangEntry, clk clock.Clock, desiredReplicas map[string]int32) *subStepPlanner {
	mvu := syncSnap.mvuTemplate
	minAvailableByComponent := lo.Assign(mvu.standalonePCLQs, mvu.pcsgs)
	return &subStepPlanner{
		clk:                       clk,
		pcs:                       syncSnap.pcs,
		pcsReplicaIndex:           pcsReplicaIndex,
		mvu:                       mvu,
		entries:                   entries,
		desiredReplicas:           desiredReplicas,
		maxUnavailableByComponent: componentutils.CoherentMaxUnavailableByComponent(syncSnap.pcs, lo.Keys(minAvailableByComponent)),
		plan:                      computeStepPlan(desiredReplicas, mvu),
	}
}

// computeNumAnchorBearingSteps returns how many anchor-bearing steps a coherent update takes for one PCS
// replica. A standalone-only MVU uses a single anchor because standalone pods can live only on an anchor
// entry, so spreading them over floor(replicas/MinAvailable) anchors would create one PodGang per pod. With
// PodCliqueScalingGroups in the MVU, the anchors are the proportional MVUs, bound by the component supplying
// the fewest MinAvailable-sized slices, the min over components of floor(replicas/MinAvailable).
func computeNumAnchorBearingSteps(desiredReplicas map[string]int32, mvu *mvuTemplate) int32 {
	if len(mvu.pcsgs) == 0 {
		return 1
	}
	numAnchorBearingSteps := int32(math.MaxInt32)
	for componentName, minAvailable := range lo.Assign(mvu.standalonePCLQs, mvu.pcsgs) {
		numAnchorBearingSteps = min(numAnchorBearingSteps, desiredReplicas[componentName]/minAvailable)
	}
	return numAnchorBearingSteps
}

// computeStepPlan derives the step plan for one PCS replica, the number of anchor-bearing steps and each
// component's per-step target and leftover.
func computeStepPlan(desiredReplicas map[string]int32, mvu *mvuTemplate) stepPlan {
	minAvailableByComponent := lo.Assign(mvu.standalonePCLQs, mvu.pcsgs)
	numAnchorBearingSteps := computeNumAnchorBearingSteps(desiredReplicas, mvu)

	// Beyond the MinAvailable that every anchor-bearing step reserves, a component's remaining replicas
	// are its tail. tailPerStep spreads that tail evenly over the anchor-bearing steps, so each step
	// rolls MinAvailable plus tailPerStep of the component, which is its anchorBearingStepTarget. Integer
	// division rounds tailPerStep down, so leftover is what remains once every anchor-bearing step has
	// rolled its target, drained afterward by the single leftover step.
	anchorBearingStepTarget := make(map[string]int32, len(minAvailableByComponent))
	leftover := make(map[string]int32, len(minAvailableByComponent))
	for componentName, componentMinAvailable := range minAvailableByComponent {
		tailPerStep := (desiredReplicas[componentName] - numAnchorBearingSteps*componentMinAvailable) / numAnchorBearingSteps
		anchorBearingStepTarget[componentName] = componentMinAvailable + tailPerStep
		leftover[componentName] = desiredReplicas[componentName] - numAnchorBearingSteps*anchorBearingStepTarget[componentName]
	}

	return stepPlan{
		numAnchorBearingSteps:   numAnchorBearingSteps,
		anchorBearingStepTarget: anchorBearingStepTarget,
		leftover:                leftover,
	}
}

// planPosition marks how far the step plan has been driven for one PCS replica, decoded every reconcile
// from the committed current-hash entries. A sub-step's new-hash entries are targets committed to the
// PGM, so this is the committed position in the plan, not a count of ready or completed replicas. It
// holds exactly what the planner branches on to pick the next sub-step.
type planPosition struct {
	// currentHashCountByComponent is how many replicas of each in-scope component have a committed entry
	// at the current generation hash.
	currentHashCountByComponent map[string]int32
	// anchorBearingStepsDone is how many anchor-bearing steps are fully committed, meaning every component
	// has met that step's target.
	anchorBearingStepsDone int32
	// currentAnchorStepCountByComponent is how much of each component the open anchor-bearing step has
	// committed to the current hash so far, beyond the fully committed steps.
	currentAnchorStepCountByComponent map[string]int32
	// leftoverCountByComponent is how many of each component's leftover replicas are committed to the
	// current hash.
	leftoverCountByComponent map[string]int32
	// mostRecentAnchorEpoch is the epoch of the most recent current-hash anchor entry, which tail and
	// leftover sub-steps subsume their standalone pods into. It is empty when no anchor entry exists yet.
	mostRecentAnchorEpoch string
}

// String renders the plan position in a compact single line form for tracing.
func (p planPosition) String() string {
	return fmt.Sprintf("planPosition(currentHashCount=%v anchorBearingStepsDone=%d currentAnchorStepCount=%v leftoverCount=%v mostRecentAnchorEpoch=%s)",
		p.currentHashCountByComponent, p.anchorBearingStepsDone, p.currentAnchorStepCountByComponent, p.leftoverCountByComponent, p.mostRecentAnchorEpoch)
}

// ascertainPlanPosition decodes how far the step plan has been driven for one PCS replica from the
// committed current-hash entries. Each component moves to the current hash in two phases. The
// anchor-bearing steps move the first numAnchorBearingSteps*anchorBearingStepTarget of it, then the
// single leftover step moves the rest. It splits each component's committed current-hash count across
// those two phases, then reports how many anchor-bearing steps are fully committed, how much the open
// anchor-bearing step has committed, and how much leftover has committed.
func (p *subStepPlanner) ascertainPlanPosition() (planPosition, error) {
	currentHashCountByComponent := p.countReplicasAtCurrentHash()

	// Split each component's current-hash count into what the anchor-bearing steps moved (capped at their
	// combined capacity) and what the leftover step moved (the rest).
	anchorPhaseCount := make(map[string]int32)
	leftoverCountByComponent := make(map[string]int32)
	for componentName := range p.desiredReplicas {
		anchorPhaseCapacity := p.plan.numAnchorBearingSteps * p.plan.anchorBearingStepTarget[componentName]
		anchorPhaseCount[componentName] = min(currentHashCountByComponent[componentName], anchorPhaseCapacity)
		leftoverCountByComponent[componentName] = currentHashCountByComponent[componentName] - anchorPhaseCount[componentName]
	}

	// An anchor-bearing step is fully committed only when every component met its target for that step, so
	// the count of fully committed steps is the min over components of floor(anchorPhaseCount/target).
	anchorBearingStepsDone := p.plan.numAnchorBearingSteps
	for componentName := range p.desiredReplicas {
		anchorBearingStepsDone = min(anchorBearingStepsDone, anchorPhaseCount[componentName]/p.plan.anchorBearingStepTarget[componentName])
	}

	// What the open anchor-bearing step has committed per component is whatever is beyond the fully
	// committed steps.
	currentAnchorStepCountByComponent := make(map[string]int32)
	for componentName := range p.desiredReplicas {
		currentAnchorStepCountByComponent[componentName] = anchorPhaseCount[componentName] - anchorBearingStepsDone*p.plan.anchorBearingStepTarget[componentName]
	}

	mostRecent, err := p.mostRecentAnchorEpoch()
	if err != nil {
		return planPosition{}, err
	}
	return planPosition{
		currentHashCountByComponent:       currentHashCountByComponent,
		anchorBearingStepsDone:            anchorBearingStepsDone,
		currentAnchorStepCountByComponent: currentAnchorStepCountByComponent,
		leftoverCountByComponent:          leftoverCountByComponent,
		mostRecentAnchorEpoch:             mostRecent,
	}, nil
}

// countReplicasAtCurrentHash sums, per in-scope component, how many replicas the current-hash entries
// carry. A standalone PodClique contributes its pod count and a PodCliqueScalingGroup its number of
// replica indices. It is a structural count of the current-hash entries and does not check readiness.
func (p *subStepPlanner) countReplicasAtCurrentHash() map[string]int32 {
	currentHash := *p.pcs.Status.CurrentGenerationHash
	countByComponent := make(map[string]int32)
	for i := range p.entries {
		entry := p.entries[i]
		if entry.PodCliqueSetGenerationHash != currentHash {
			continue
		}
		for componentName, podCount := range entry.PodCliques {
			if _, inScope := p.mvu.standalonePCLQs[componentName]; inScope {
				countByComponent[componentName] += podCount
			}
		}
		for componentName, replicaIndices := range entry.PCSGReplicaIndices {
			if _, inScope := p.mvu.pcsgs[componentName]; inScope {
				countByComponent[componentName] += int32(len(replicaIndices))
			}
		}
	}
	return countByComponent
}

// mostRecentAnchorEpoch returns the highest epoch among the current-hash anchor entries, which is the
// most recently created anchor since epoch increases with every new entry. It returns the empty string
// when no current-hash anchor exists yet, and an error when an anchor epoch is not numeric.
func (p *subStepPlanner) mostRecentAnchorEpoch() (string, error) {
	pcsCurrentGenerationHash := *p.pcs.Status.CurrentGenerationHash
	var (
		epoch             string
		found             bool
		highestEpochNanos int64
	)
	for i := range p.entries {
		entry := p.entries[i]
		if entry.PodCliqueSetGenerationHash != pcsCurrentGenerationHash || entry.Role != grovecorev1alpha1.PodGangEntryRoleAnchor {
			continue
		}
		epochNanos, err := entryEpochNanos(entry)
		if err != nil {
			return "", err
		}
		if !found || epochNanos > highestEpochNanos {
			found, highestEpochNanos, epoch = true, epochNanos, entry.Epoch
		}
	}
	return epoch, nil
}

// next returns the next sub-step to commit for one PCS replica, or nil when no sub-step remains because
// every in-scope component is already committed to the current hash. An anchor-bearing step is committed
// as an anchor sub-step for its MinAvailable, then tail sub-steps for the rest of its target, so next
// finishes an open step's tail, else opens the next anchor-bearing step, else commits a leftover sub-step.
//
// next reasons only about what the PGM has committed, so a nil return means fully committed, not that the
// replicas are ready. Whether the committed replicas are ready, and so whether the update is complete, is
// the orchestrator's determination. The caller invokes next only after the most recently committed
// sub-step is ready.
func (p *subStepPlanner) next(planPos planPosition, headroomByComponent map[string]int32) (*subStep, error) {
	// An open anchor-bearing step, one whose MinAvailable is committed but which has not reached its
	// target, still has tail to commit.
	if p.openAnchorStepHasTailRemaining(planPos) {
		return p.buildTailSubStep(planPos, headroomByComponent)
	}
	// No anchor-bearing step is open, so open the next one while any remain unopened.
	if planPos.anchorBearingStepsDone < p.plan.numAnchorBearingSteps {
		return p.buildAnchorBearingSubStep(planPos)
	}
	// Every anchor-bearing step is committed, so commit any remaining leftover.
	if p.anyLeftoverRemaining(planPos.currentHashCountByComponent) {
		return p.buildLeftoverSubStep(planPos, headroomByComponent)
	}
	return nil, nil
}

// openAnchorStepHasTailRemaining reports whether an anchor-bearing step is open with tail still to
// commit. Every step rolls a per-component target delivered over one or more sub-steps. An anchor
// sub-step commits MinAvailable, then tail sub-steps commit the rest of the target, each bounded by
// MaxUnavailable. A step is open once its MinAvailable is committed but its target is not yet reached,
// so tail sub-steps remain. For example, a step realized as one anchor sub-step followed by five tail
// sub-steps, with the anchor and two tail sub-steps committed, still has three tail sub-steps to
// commit, so this returns true.
func (p *subStepPlanner) openAnchorStepHasTailRemaining(planPos planPosition) bool {
	for componentName := range p.desiredReplicas {
		// Opening a step commits MinAvailable to every component, so a committed count above zero means
		// the step is open, while zero is a step boundary where the step is not yet opened. A committed
		// count below the step's target means this component still has tail to commit, while a count equal
		// to the target means it has finished the open step.
		committed := planPos.currentAnchorStepCountByComponent[componentName]
		if committed > 0 && committed < p.plan.anchorBearingStepTarget[componentName] {
			return true
		}
	}
	return false
}

// buildTailSubStep commits the next slice of the open anchor-bearing step's tail. The step's target is
// MinAvailable, committed by the anchor sub-step, plus a tail. When the tail exceeds MaxUnavailable it is
// committed over several tail sub-steps, so this one commits what the anchor sub-step and any earlier tail
// sub-steps of the step have not yet committed, at most MaxUnavailable of each component. It subsumes
// standalone PodClique pods into the step's anchor and rolls the PodCliqueScalingGroup tail indices.
//
// Worked example. Take one PCSG D of a multi-component update whose step count another component caps low,
// with MinAvailable 2, MaxUnavailable 3, and StepTarget 6. Step k=0's index block is [0, 6), of which the
// anchor sub-step already committed [0, 2), leaving a tail of 4 indices [2, 6). The tail exceeds
// MaxUnavailable 3, so it drains over two tail sub-steps. The first commits min(3, 4) = 3 from index
// (k+1)*6 - 4 = 2, rolling [2, 5). The next commits min(3, 1) = 1 from index (k+1)*6 - 1 = 5, rolling [5, 6).
func (p *subStepPlanner) buildTailSubStep(planPos planPosition, headroomByComponent map[string]int32) (*subStep, error) {
	stepIndex := planPos.anchorBearingStepsDone // 0-based index of the open anchor-bearing step
	remainingByComponent := make(map[string]int32)
	for componentName := range p.desiredReplicas {
		if gap := p.plan.anchorBearingStepTarget[componentName] - planPos.currentAnchorStepCountByComponent[componentName]; gap > 0 {
			remainingByComponent[componentName] = gap
		}
	}
	// A PCSG's tail indices continue this step's block just past what it has already rolled. The block
	// ends at (k+1)*target and remaining are still unrolled, so the next unrolled index is
	// (k+1)*target - remaining.
	pcsgIndexStartFn := func(pcsgName string, remaining int32) int32 {
		return (stepIndex+1)*p.plan.anchorBearingStepTarget[pcsgName] - remaining
	}
	return p.buildNonAnchorSubStep(newEpoch(p.clk), planPos.mostRecentAnchorEpoch, remainingByComponent, pcsgIndexStartFn, headroomByComponent)
}

// buildAnchorBearingSubStep opens the next anchor-bearing step by committing a new anchor entry that
// carries MinAvailable of every component, allocating each PodCliqueScalingGroup's MinAvailable replica
// indices from this step's block and draining the old-hash equivalent. The step's tail, if any, is
// committed later by buildTailSubStep.
func (p *subStepPlanner) buildAnchorBearingSubStep(planPos planPosition) (*subStep, error) {
	dependsOn, err := p.dependsOnLatestEpoch()
	if err != nil {
		return nil, err
	}
	// This anchor opens the k-th anchor-bearing step and claims the first MinAvailable indices of that
	// step's block [k*target, (k+1)*target) for each PCSG, so anchors of different steps never collide.
	stepIndex := planPos.anchorBearingStepsDone
	anchorPCSGIndices := make(map[string][]int32, len(p.mvu.pcsgs))
	for pcsgName, minAvailable := range p.mvu.pcsgs {
		anchorPCSGIndices[pcsgName] = lo.RangeFrom(stepIndex*p.plan.anchorBearingStepTarget[pcsgName], int(minAvailable))
	}
	return &subStep{
		epoch:                     newEpoch(p.clk),
		dependsOn:                 dependsOn,
		opensAnchor:               true,
		anchorPCSGReplicaIndices:  anchorPCSGIndices,
		drainStandalonePCLQCounts: p.mvu.standalonePCLQs,
		drainPCSGReplicaIndices:   anchorPCSGIndices,
	}, nil
}

// anyLeftoverRemaining reports whether any component still has leftover replicas to roll. It is called
// only once every anchor-bearing step is committed, so any gap between the current-hash count and the
// live replica count is leftover the leftover step must still roll.
func (p *subStepPlanner) anyLeftoverRemaining(currentHashCountByComponent map[string]int32) bool {
	for componentName := range p.plan.leftover {
		if currentHashCountByComponent[componentName] < p.desiredReplicas[componentName] {
			return true
		}
	}
	return false
}

// buildLeftoverSubStep builds one sub-step of the single leftover step, which rolls whatever remains
// after all anchor-bearing steps. A component's leftover indices sit above every anchor-bearing step's
// block, [numAnchorBearingSteps*target, replicas), so the next unrolled index is replicas - remaining.
// Standalone PodClique leftover pods subsume into the most recent anchor.
func (p *subStepPlanner) buildLeftoverSubStep(planPos planPosition, headroomByComponent map[string]int32) (*subStep, error) {
	remainingByComponent := make(map[string]int32)
	for componentName := range p.plan.leftover {
		if gap := p.plan.leftover[componentName] - planPos.leftoverCountByComponent[componentName]; gap > 0 {
			remainingByComponent[componentName] = gap
		}
	}
	pcsgIndexStartFn := func(pcsgName string, remaining int32) int32 {
		return p.desiredReplicas[pcsgName] - remaining
	}
	return p.buildNonAnchorSubStep(newEpoch(p.clk), planPos.mostRecentAnchorEpoch, remainingByComponent, pcsgIndexStartFn, headroomByComponent)
}

// buildNonAnchorSubStep assembles a sub-step that adds no anchor, shared by the tail sub-steps of an
// anchor-bearing step and the sub-steps of the leftover step. remainingByComponent is the work this
// sub-step must roll. For each component it rolls a budget of min(MaxUnavailable, remaining), further
// capped by headroomByComponent so it never takes down more than the component's remaining MaxUnavailable
// headroom when replicas are already unavailable for unrelated reasons. A nil headroomByComponent disables
// the headroom cap. A PodCliqueScalingGroup gets tail entries at pcsgIndexStartFn(name, remaining) onward,
// and a standalone PodClique subsumes that many pods into the anchor at anchorEpoch. The old-hash
// equivalent is drained in both cases. It returns nil when there is nothing left to roll.
func (p *subStepPlanner) buildNonAnchorSubStep(epoch, anchorEpoch string, remainingByComponent map[string]int32, pcsgIndexStartFn func(pcsgName string, remaining int32) int32, headroomByComponent map[string]int32) (*subStep, error) {
	if len(remainingByComponent) == 0 {
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
	for componentName, remaining := range remainingByComponent {
		rollBudget := min(p.maxUnavailableByComponent[componentName], remaining)
		if headroomByComponent != nil {
			rollBudget = min(rollBudget, headroomByComponent[componentName])
		}
		if _, isPCSG := p.mvu.pcsgs[componentName]; isPCSG {
			indices := lo.RangeFrom(pcsgIndexStartFn(componentName, remaining), int(rollBudget))
			ss.tailPCSGReplicaIndices[componentName] = indices
			ss.drainPCSGReplicaIndices[componentName] = indices
		} else {
			ss.subsumeStandalonePCLQCounts[componentName] = rollBudget
			ss.drainStandalonePCLQCounts[componentName] = rollBudget
		}
	}
	return ss, nil
}

// dependsOnLatestEpoch returns the DependsOn slice for a newly emitted sub-step, which is the single
// latest current-hash epoch, or nil for the first sub-step of the update when no current-hash entry exists yet.
func (p *subStepPlanner) dependsOnLatestEpoch() ([]string, error) {
	latestEpoch, err := componentutils.LatestEpochForGenerationHash(p.entries, *p.pcs.Status.CurrentGenerationHash)
	if err != nil {
		return nil, groveerr.WrapError(err, errCodeInvalidEpoch, component.OperationSync,
			fmt.Sprintf("failed to derive the DependsOn epoch for PodCliqueSet %s", p.pcs.Name))
	}
	if latestEpoch == nil {
		return nil, nil
	}
	return []string{*latestEpoch}, nil
}

// newEpoch returns a fresh epoch for a sub-step, the current time in Unix nanoseconds.
func newEpoch(clk clock.Clock) string {
	return strconv.FormatInt(clk.Now().UnixNano(), 10)
}

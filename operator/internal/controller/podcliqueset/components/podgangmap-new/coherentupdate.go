package podgangmap_new

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"
)

// buildCoherentUpdateEntries computes one sub-step of a coherent update for the given PCS replica.
// It operates on two pieces of state: the PodGangMap Spec.Entries (desired PodGangs) and
// Status.UpdateProgress (observed steps and sub-steps). It brings the progress in sync with the
// entries, advances observed readiness from the live PodGangs, and only when the previous batch is
// confirmed ready does it compute and materialise the next sub-step. It returns the entries that
// should exist and the progress that should be recorded. The caller persists Spec first, then
// Status.
func buildCoherentUpdateEntries(pcsReplicaIndex int, syncSnap *syncSnapshot, replicaPodGangs []groveschedulerv1alpha1.PodGang, clk clock.Clock) ([]grovecorev1alpha1.PodGangEntry, *grovecorev1alpha1.PodGangMapUpdateProgress, error) {
	var err error
	pgm := syncSnap.existingPGMByReplica[pcsReplicaIndex]
	entries := pgm.Spec.Entries
	progress := pgm.Status.UpdateProgress.DeepCopy()
	currentHash := *syncSnap.pcs.Status.CurrentGenerationHash

	// First coherent reconcile for this replica: bring entries in sync with the live PodClique and
	// PodCliqueScalingGroup PodGangMappings so a scale-out that landed just before the update is not
	// orphaned, then start recording progress.
	if progress == nil {
		entries, err = buildEntriesFromPCLQAndPCSGStatuses(syncSnap.pcs, syncSnap.existingStandalonePCLQsByReplica[pcsReplicaIndex], syncSnap.existingPCSGsByReplica[pcsReplicaIndex], &pgm, pcsReplicaIndex, clk)
		if err != nil {
			return nil, nil, err
		}
		progress = &grovecorev1alpha1.PodGangMapUpdateProgress{}
	}

	// Status must reflect Spec before any readiness gating. If a crash between the Spec write and the
	// Status write left one new-hash Spec epoch unrecorded, record it and return without computing a
	// new sub-step.
	if syncProgressFromSpec(entries, progress, currentHash) {
		return entries, progress, nil
	}

	// Advance the observed readiness of the recorded sub-steps from the live PodGangs.
	readinessByEpoch, err := determineReadinessByEpoch(replicaPodGangs)
	if err != nil {
		return nil, nil, err
	}
	advanceReadiness(progress, readinessByEpoch)

	// The previous batch must be confirmed ready before the next sub-step is emitted.
	if !latestBatchReady(progress) {
		return entries, progress, nil
	}

	planner := newSubStepPlanner(syncSnap, pcsReplicaIndex, entries, clk)
	ss, progress, err := planner.next(progress)
	if err != nil {
		return nil, nil, err
	}
	if ss == nil {
		return entries, progress, nil
	}
	return planner.applySubStep(*ss), progress, nil
}

// syncProgressFromSpec repairs a crash-truncated Status. The Spec write precedes the Status write, so
// a crash between them can leave exactly one new-hash epoch present in Spec.Entries but not recorded
// in progress. When such an epoch is found it is recorded as InProgress, opening a new step for an
// anchor entry or appending a tail sub-step to the current step otherwise, and true is returned.
// advanceReadiness flips the recorded State to Ready when the epoch is ready. Returns false when
// progress already reflects Spec.
func syncProgressFromSpec(entries []grovecorev1alpha1.PodGangEntry, progress *grovecorev1alpha1.PodGangMapUpdateProgress, currentHash string) bool {
	recorded := recordedEpochs(progress)
	for i := range entries {
		if entries[i].PodCliqueSetGenerationHash != currentHash {
			continue
		}
		epoch := entries[i].Labels[apicommon.LabelEpoch]
		if recorded[epoch] {
			continue
		}
		newSubStep := grovecorev1alpha1.PodGangMapUpdateSubStep{Epoch: epoch, State: grovecorev1alpha1.PodGangMapUpdateStateInProgress}
		if entries[i].IsEpochAnchor {
			progress.Steps = append(progress.Steps, grovecorev1alpha1.PodGangMapUpdateStep{
				IsAnchorBearing: true,
				State:           grovecorev1alpha1.PodGangMapUpdateStateInProgress,
				SubSteps:        []grovecorev1alpha1.PodGangMapUpdateSubStep{newSubStep},
			})
		} else {
			lastStep := &progress.Steps[len(progress.Steps)-1]
			lastStep.SubSteps = append(lastStep.SubSteps, newSubStep)
		}
		return true
	}
	return false
}

// recordedEpochs returns the set of epochs already present across all recorded sub-steps.
func recordedEpochs(progress *grovecorev1alpha1.PodGangMapUpdateProgress) map[string]bool {
	recorded := make(map[string]bool)
	for i := range progress.Steps {
		for j := range progress.Steps[i].SubSteps {
			recorded[progress.Steps[i].SubSteps[j].Epoch] = true
		}
	}
	return recorded
}

// determineReadinessByEpoch reports, per grove.io/epoch, whether every PodGang carrying that epoch
// has become ready at least once. An epoch is ready when at least one PodGang carries it and none
// of those PodGangs has a nil LastReady. Readiness is monotonic on the PodGang side (LastReady is
// never un-set), so this reflects "ever ready". Every PodGang materialised from a PodGangMap entry
// carries the epoch label, so a PodGang without it is a contract violation and is returned as an
// error rather than skipped.
func determineReadinessByEpoch(podGangs []groveschedulerv1alpha1.PodGang) (map[string]bool, error) {
	readinessByEpoch := make(map[string]bool)
	for i := range podGangs {
		epoch, ok := podGangs[i].Labels[apicommon.LabelEpoch]
		if !ok {
			return nil, groveerr.New(errCodeMissingEpochLabel, component.OperationSync,
				fmt.Sprintf("PodGang %s is missing the %s label", podGangs[i].Name, apicommon.LabelEpoch))
		}
		ready := podGangs[i].Status.LastReady != nil
		if seen, ok := readinessByEpoch[epoch]; ok {
			readinessByEpoch[epoch] = seen && ready
		} else {
			readinessByEpoch[epoch] = ready
		}
	}
	return readinessByEpoch, nil
}

// advanceReadiness flips each recorded sub-step whose epoch is ready to Ready, and each step whose
// sub-steps are all Ready to Ready. Readiness only ever advances from InProgress to Ready, matching
// the monotonic PodGang LastReady source. It is a no-op when progress is nil.
func advanceReadiness(progress *grovecorev1alpha1.PodGangMapUpdateProgress, readinessByEpoch map[string]bool) {
	if progress == nil {
		return
	}
	for i := range progress.Steps {
		step := &progress.Steps[i]
		allSubStepsReady := true
		for j := range step.SubSteps {
			recordedSubStep := &step.SubSteps[j]
			if recordedSubStep.State == grovecorev1alpha1.PodGangMapUpdateStateReady {
				continue
			}
			if readinessByEpoch[recordedSubStep.Epoch] {
				recordedSubStep.State = grovecorev1alpha1.PodGangMapUpdateStateReady
			} else {
				allSubStepsReady = false
			}
		}
		if allSubStepsReady && len(step.SubSteps) > 0 {
			step.State = grovecorev1alpha1.PodGangMapUpdateStateReady
		}
	}
}

// latestBatchReady reports whether the most recently recorded sub-step is Ready, which is the
// self-gate the next sub-step waits on. It returns true when no sub-steps are recorded yet, so the
// first sub-step is not gated. A latest step with no sub-steps yet means no batch has been emitted
// for it, so the gate stays closed (returns false).
func latestBatchReady(progress *grovecorev1alpha1.PodGangMapUpdateProgress) bool {
	if len(progress.Steps) == 0 {
		return true
	}
	subSteps := progress.Steps[len(progress.Steps)-1].SubSteps
	if len(subSteps) == 0 {
		return false
	}
	return subSteps[len(subSteps)-1].State == grovecorev1alpha1.PodGangMapUpdateStateReady
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

// next decides the next sub-step of a coherent update and returns progress with the
// sub-step recorded. It returns a nil sub-step when every in-scope component is fully rolled.
//
// It is called only after latestBatchReady has confirmed the most recent recorded sub-step is
// Ready, so it never gates on readiness; it only decides what to emit next.
//
// It reads two sources that answer different questions and never disagree at this point (they are
// kept in sync upstream by syncProgressFromSpec, so this function does not reconcile them):
//   - Status.UpdateProgress.Steps is the only record of the step boundaries: which step we are in,
//     whether it is anchor-bearing, and how many sub-steps have been emitted so far. Spec.Entries do
//     not record step structure, so boundaries cannot be read from them without reconstruction.
//   - The new-hash Spec.Entries carry the actual rolled counts per component (pod counts on
//     PodCliques, replica counts on PCSGReplicaIndices). Deriving these from Status instead would
//     require re-running the step-plan formulas, which is fragile once step targets can vary.
func (p *subStepPlanner) next(progress *grovecorev1alpha1.PodGangMapUpdateProgress) (*subStep, *grovecorev1alpha1.PodGangMapUpdateProgress, error) {
	// readyCounts[c] is how much of each in-scope component is rolled to the new hash and Ready.
	readyCounts := p.readyCountsByComponent(progress, nil)
	if allComponentsFullyRolled(readyCounts, p.replicas) {
		return nil, progress, nil
	}

	// Continue the current step when one is in progress and its per-component target is not yet met.
	if len(progress.Steps) > 0 && !p.isCurrentStepComplete(progress) {
		return p.buildTailSubStep(progress)
	}

	// The current step is complete (or none is open). Open the next anchor-bearing step while any
	// remain, otherwise the single leftover step.
	if recordedAnchorBearingSteps(progress) < p.numAnchorBearingSteps {
		return p.buildAnchorBearingSubStep(progress)
	}
	if p.anyLeftoverRemaining(readyCounts) {
		return p.buildLeftoverSubStep(progress)
	}
	return nil, progress, nil
}

// readyCountsByComponent returns, per in-scope component, the count of new-hash content that is
// updated and Ready. A standalone PodClique contributes its Ready pod count and a
// PodCliqueScalingGroup contributes its Ready replica count. Readiness is read from progress.Steps
// (stamped by advanceReadiness this reconcile), so the count reflects what is actually rolled out
// and Ready, not merely what has been emitted to Spec.
//
// matchingEpochs scopes the count: nil counts every Ready epoch across the whole update (the
// update-wide total), a populated set counts only Ready epochs in the set (scoped to one step).
func (p *subStepPlanner) readyCountsByComponent(progress *grovecorev1alpha1.PodGangMapUpdateProgress, matchingEpochs sets.Set[string]) map[string]int32 {
	currentHash := *p.pcs.Status.CurrentGenerationHash
	readyEpochs := sets.New[string]()
	for i := range progress.Steps {
		for j := range progress.Steps[i].SubSteps {
			recordedSubStep := progress.Steps[i].SubSteps[j]
			if recordedSubStep.State != grovecorev1alpha1.PodGangMapUpdateStateReady {
				continue
			}
			if matchingEpochs != nil && !matchingEpochs.Has(recordedSubStep.Epoch) {
				continue
			}
			readyEpochs.Insert(recordedSubStep.Epoch)
		}
	}

	counts := make(map[string]int32)
	for i := range p.entries {
		if p.entries[i].PodCliqueSetGenerationHash != currentHash {
			continue
		}
		if !readyEpochs.Has(p.entries[i].Labels[apicommon.LabelEpoch]) {
			continue
		}
		for name, count := range p.entries[i].PodCliques {
			if _, ok := p.mvu.standalonePCLQs[name]; ok {
				counts[name] += count
			}
		}
		for name, indices := range p.entries[i].PCSGReplicaIndices {
			if _, ok := p.mvu.pcsgs[name]; ok {
				counts[name] += int32(len(indices))
			}
		}
	}
	return counts
}

// allComponentsFullyRolled reports whether every in-scope component has readyCount == replicas.
func allComponentsFullyRolled(readyCounts, replicas map[string]int32) bool {
	for c, want := range replicas {
		if readyCounts[c] != want {
			return false
		}
	}
	return true
}

// isCurrentStepComplete reports whether the current (last recorded) step has met its per-component
// target and those pods are Ready, so the step is done and the next step may be opened.
func (p *subStepPlanner) isCurrentStepComplete(progress *grovecorev1alpha1.PodGangMapUpdateProgress) bool {
	return len(p.remainingInCurrentStep(progress)) == 0
}

// remainingInCurrentStep returns, for every in-scope component, how many replicas the current (last
// recorded) step still has to roll to reach its target. Only positive gaps are included, so an empty
// result means the current step has met its target for every component.
func (p *subStepPlanner) remainingInCurrentStep(progress *grovecorev1alpha1.PodGangMapUpdateProgress) map[string]int32 {
	lastStep := progress.Steps[len(progress.Steps)-1]
	target := p.leftover
	if lastStep.IsAnchorBearing {
		target = p.anchorBearingStepTarget
	}
	stepEpochs := sets.New[string]()
	for i := range lastStep.SubSteps {
		stepEpochs.Insert(lastStep.SubSteps[i].Epoch)
	}
	return p.remainingForStep(progress, target, stepEpochs)
}

// remainingForStep returns, per component, target[c] minus how much of that target is already rolled
// and Ready within stepEpochs. Only positive gaps are included. When stepEpochs is empty the step has
// emitted no sub-steps yet, so nothing is rolled and the whole target remains.
func (p *subStepPlanner) remainingForStep(progress *grovecorev1alpha1.PodGangMapUpdateProgress, target map[string]int32, stepEpochs sets.Set[string]) map[string]int32 {
	readyInStep := map[string]int32{}
	if len(stepEpochs) > 0 {
		readyInStep = p.readyCountsByComponent(progress, stepEpochs)
	}
	remaining := make(map[string]int32, len(target))
	for c, want := range target {
		if gap := want - readyInStep[c]; gap > 0 {
			remaining[c] = gap
		}
	}
	return remaining
}

// recordedAnchorBearingSteps returns how many anchor-bearing steps are recorded in progress. It
// counts the IsAnchorBearing flag rather than using len(progress.Steps): the two are equal today
// (anchor-bearing steps precede the single leftover step), but counting the flag stays correct if a
// future iteration lifts the scaling freeze and the step structure can vary mid-update.
func recordedAnchorBearingSteps(progress *grovecorev1alpha1.PodGangMapUpdateProgress) int32 {
	var count int32
	for i := range progress.Steps {
		if progress.Steps[i].IsAnchorBearing {
			count++
		}
	}
	return count
}

// anyLeftoverRemaining reports whether any component still has leftover replicas to roll after the
// anchor-bearing steps. It is called only once all anchor-bearing steps are recorded, so any gap
// between readyCounts and replicas is the leftover that the leftover step must still drain.
func (p *subStepPlanner) anyLeftoverRemaining(readyCounts map[string]int32) bool {
	for c := range p.leftover {
		if readyCounts[c] < p.replicas[c] {
			return true
		}
	}
	return false
}

// buildTailSubStep builds the next sub-step of the current step: the next rollBudget-bounded tail
// slice, subsuming standalone PodClique pods into the current step's anchor and emitting one tail
// PodGang per PodCliqueScalingGroup replica index.
//
// Worked example. Component D (a PodCliqueScalingGroup) has replicas 80, minAvailable 3,
// maxUnavailable 4, so anchorBearingStepTarget[D] = 8 across 10 anchor-bearing steps. Take the
// current step k = 0, whose contiguous index block for D is [0, 8). The anchor sub-step already
// rolled the first minAvailable, indices [0, 3), leaving 5 to roll.
//   - This sub-step: remainingReplicas[D] = 5, rollBudget = min(4, 5) = 4. Indices start at
//     (k+1)*8 - 5 = 3, so it emits tail PodGangs for indices [3, 7).
//   - Next sub-step: remainingReplicas[D] = 1, rollBudget = min(4, 1) = 1. Indices start at
//     (k+1)*8 - 1 = 7, so it emits a tail PodGang for index [7, 8). The step's block is now fully
//     rolled and the next call opens step k = 1 (block [8, 16)).
//
// A standalone PodClique is handled by count instead of index: its rollBudget pods are subsumed
// into the step's anchor rather than emitted as their own PodGangs.
func (p *subStepPlanner) buildTailSubStep(progress *grovecorev1alpha1.PodGangMapUpdateProgress) (*subStep, *grovecorev1alpha1.PodGangMapUpdateProgress, error) {
	epoch := strconv.FormatInt(p.clk.Now().UnixNano(), 10)
	k := recordedAnchorBearingSteps(progress) - 1 // current anchor-bearing step's 0-based index
	lastStep := &progress.Steps[len(progress.Steps)-1]

	// PCSG tail indices continue this step's contiguous block just past what it has already rolled:
	// the block ends at (k+1)*anchorBearingStepTarget, and remainingReplicas remain, so the next
	// unrolled index is (k+1)*anchorBearingStepTarget - remainingReplicas. Standalone PodClique tail
	// pods subsume into this step's own anchor (its first sub-step).
	pcsgIndexStartFn := func(componentName string, remainingReplicas int32) int32 {
		return (k+1)*p.anchorBearingStepTarget[componentName] - remainingReplicas
	}
	ss, err := p.buildNonAnchorSubStep(epoch, lastStep.SubSteps[0].Epoch, p.remainingInCurrentStep(progress), pcsgIndexStartFn)
	if err != nil {
		return nil, nil, err
	}
	if ss == nil {
		return nil, progress, nil
	}

	// Append this tail sub-step to the current step.
	lastStep.SubSteps = append(lastStep.SubSteps, grovecorev1alpha1.PodGangMapUpdateSubStep{Epoch: epoch, State: grovecorev1alpha1.PodGangMapUpdateStateInProgress})
	return ss, progress, nil
}

// buildNonAnchorSubStep assembles a sub-step that is not the anchor-creating one: the tail sub-steps
// of an anchor-bearing step and the sub-steps of the leftover step. remaining is the per-component
// work this sub-step must roll; the caller computes it for the step being filled. For each component
// it rolls a rollBudget of min(maxUnavailable, remaining). A PodCliqueScalingGroup gets tail PodGangs
// at indices pcsgIndexStart(component, remaining) .. +rollBudget; a standalone PodClique subsumes
// rollBudget pods into the existing anchor at anchorEpoch. The old-hash equivalent is drained in both
// cases. It returns nil when remaining is empty (nothing left to roll), creates no anchor, and does
// not record the sub-step in progress; the caller records it.
func (p *subStepPlanner) buildNonAnchorSubStep(epoch, anchorEpoch string, remaining map[string]int32, pcsgIndexStartFn func(componentName string, remainingReplicas int32) int32) (*subStep, error) {
	if len(remaining) == 0 {
		return nil, nil
	}
	dependsOn, err := maxEpoch(p.entries)
	if err != nil {
		return nil, err
	}
	ss := &subStep{
		epoch:                       epoch,
		dependsOn:                   epochDependencyAsSlice(dependsOn),
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

// maxEpoch returns the largest grove.io/epoch across the entries, compared numerically (epochs are
// unix-nano strings), which a newly emitted batch depends on. It returns (nil, nil) when entries is
// empty (the first batch of an update has no dependency). The largest epoch is the most recently
// emitted batch, because sub-step epochs come from a monotonic clock and only increase, and that
// most-recent batch is the one a new sub-step depends on. Every entry carries the epoch label, so
// a missing or non-numeric label is a contract violation returned as an error.
func maxEpoch(entries []grovecorev1alpha1.PodGangEntry) (*string, error) {
	var (
		maxLabel string
		maxValue int64
	)
	if len(entries) == 0 {
		return nil, nil
	}
	for i := range entries {
		label, ok := entries[i].Labels[apicommon.LabelEpoch]
		if !ok {
			return nil, groveerr.New(errCodeMissingEpochLabel, component.OperationSync,
				fmt.Sprintf("PodGangMap entry %s is missing the %s label", entries[i].Name, apicommon.LabelEpoch))
		}
		value, err := strconv.ParseInt(label, 10, 64)
		if err != nil {
			return nil, groveerr.WrapError(err, errCodeInvalidEpochLabel, component.OperationSync,
				fmt.Sprintf("PodGangMap entry %s has a non-numeric %s label %q", entries[i].Name, apicommon.LabelEpoch, label))
		}
		if value > maxValue {
			maxLabel, maxValue = label, value
		}
	}
	return &maxLabel, nil
}

// epochDependencyAsSlice wraps the single dependency epoch maxEpoch returns into the []string that
// PodGangEntry.DependsOn wants. A nil epoch (the first batch of an update) becomes a nil slice,
// which serializes as an absent DependsOn.
func epochDependencyAsSlice(epoch *string) []string {
	var dependsOn []string
	if epoch != nil {
		dependsOn = append(dependsOn, *epoch)
	}
	return dependsOn
}

// buildAnchorBearingSubStep builds the floor-only first sub-step of a new anchor-bearing step: an
// anchor carrying minAvailable of every component, with distinct PCSG floor indices allocated to
// this anchor. It drains the old-hash equivalent of that floor. No tail is emitted here; the tail
// drains in subsequent buildTailSubStep sub-steps.
func (p *subStepPlanner) buildAnchorBearingSubStep(progress *grovecorev1alpha1.PodGangMapUpdateProgress) (*subStep, *grovecorev1alpha1.PodGangMapUpdateProgress, error) {
	dependsOn, err := maxEpoch(p.entries)
	if err != nil {
		return nil, nil, err
	}
	epoch := strconv.FormatInt(p.clk.Now().UnixNano(), 10)

	// This anchor is the k-th anchor-bearing step (0-based). It claims PCSG indices in the range
	// [k*anchorBearingStepTarget, k*anchorBearingStepTarget + minAvailable), so anchors never collide.
	k := recordedAnchorBearingSteps(progress)
	pcsgFloorIndices := make(map[string][]int32, len(p.mvu.pcsgs))
	for name, minAvail := range p.mvu.pcsgs {
		pcsgFloorIndices[name] = lo.RangeFrom(k*p.anchorBearingStepTarget[name], int(minAvail))
	}

	ss := &subStep{
		epoch:     epoch,
		dependsOn: epochDependencyAsSlice(dependsOn),
		// The anchor carries minAvailable PCSG replicas at the allocated indices, and it takes down
		// the same indices on the old hash (identity-preserving swap). The standalone PodClique floor
		// is minAvailable per PodClique, added by applySubStep from p.mvu, and its old-hash equivalent
		// is drained here.
		anchorPCSGReplicaIndices:  pcsgFloorIndices,
		drainStandalonePCLQCounts: p.mvu.standalonePCLQs,
		drainPCSGReplicaIndices:   pcsgFloorIndices,
	}

	// Open a new anchor-bearing step with this floor sub-step as its first.
	progress.Steps = append(progress.Steps, grovecorev1alpha1.PodGangMapUpdateStep{
		IsAnchorBearing: true,
		State:           grovecorev1alpha1.PodGangMapUpdateStateInProgress,
		SubSteps:        []grovecorev1alpha1.PodGangMapUpdateSubStep{{Epoch: epoch, State: grovecorev1alpha1.PodGangMapUpdateStateInProgress}},
	})
	return ss, progress, nil
}

// buildLeftoverSubStep builds a sub-step of the leftover step.
//
// After the anchor-bearing steps have rolled, some replicas can remain for a component (its
// leftover). A single leftover step drains all of them across one or more sub-steps. This method
// builds one such sub-step.
//
// For each component it rolls a rollBudget of replicas (min of maxUnavailable and what remains). A
// PodCliqueScalingGroup gets one tail PodGang per rolled replica index. A standalone PodClique has
// its rolled pods subsumed into the most recent anchor rather than getting its own PodGang.
//
// The leftover indices of a component are the ones above every anchor-bearing step's block:
// [numAnchorBearingSteps*anchorBearingStepTarget, replicas). This range ends at replicas, so as
// sub-steps roll it the next unrolled index is replicas - remainingReplicas (count the remaining
// back from the end of the range).
//
// Worked example. Component D (a PodCliqueScalingGroup) has replicas 20 with leftover 2 after the
// anchor-bearing steps rolled indices [0, 18). remainingReplicas[D] = 2, so this sub-step's indices
// start at 20 - 2 = 18 and it emits tail PodGangs for indices [18, 20).
func (p *subStepPlanner) buildLeftoverSubStep(progress *grovecorev1alpha1.PodGangMapUpdateProgress) (*subStep, *grovecorev1alpha1.PodGangMapUpdateProgress, error) {
	epoch := strconv.FormatInt(p.clk.Now().UnixNano(), 10)

	pcsgIndexStartFn := func(componentName string, remainingReplicas int32) int32 {
		return p.replicas[componentName] - remainingReplicas
	}

	// The leftover step's already-rolled epochs, empty on the first leftover sub-step (the last
	// recorded step is still anchor-bearing), so remainingForStep reports the whole leftover target.
	leftoverEpochs := sets.New[string]()
	if lastStep := progress.Steps[len(progress.Steps)-1]; !lastStep.IsAnchorBearing {
		for i := range lastStep.SubSteps {
			leftoverEpochs.Insert(lastStep.SubSteps[i].Epoch)
		}
	}
	remaining := p.remainingForStep(progress, p.leftover, leftoverEpochs)

	ss, err := p.buildNonAnchorSubStep(epoch, mostRecentAnchorEpoch(progress), remaining, pcsgIndexStartFn)
	if err != nil {
		return nil, nil, err
	}
	if ss == nil {
		return nil, progress, nil
	}

	// Record this sub-step. The leftover step is opened on its first sub-step (when the last recorded
	// step is still anchor-bearing) and appended to thereafter.
	lastStep := &progress.Steps[len(progress.Steps)-1]
	if lastStep.IsAnchorBearing {
		progress.Steps = append(progress.Steps, grovecorev1alpha1.PodGangMapUpdateStep{
			IsAnchorBearing: false,
			State:           grovecorev1alpha1.PodGangMapUpdateStateInProgress,
			SubSteps:        []grovecorev1alpha1.PodGangMapUpdateSubStep{{Epoch: epoch, State: grovecorev1alpha1.PodGangMapUpdateStateInProgress}},
		})
	} else {
		lastStep.SubSteps = append(lastStep.SubSteps, grovecorev1alpha1.PodGangMapUpdateSubStep{Epoch: epoch, State: grovecorev1alpha1.PodGangMapUpdateStateInProgress})
	}
	return ss, progress, nil
}

// mostRecentAnchorEpoch returns the epoch of the most recently opened anchor-bearing step's anchor
// sub-step. A leftover step always follows at least one anchor-bearing step, so an anchor is always
// found.
func mostRecentAnchorEpoch(progress *grovecorev1alpha1.PodGangMapUpdateProgress) string {
	for i := len(progress.Steps) - 1; i >= 0; i-- {
		if progress.Steps[i].IsAnchorBearing {
			return progress.Steps[i].SubSteps[0].Epoch
		}
	}
	return ""
}

// applySubStep transforms the entries into the set that should exist after the sub-step. It works on
// a deep copy so the snapshot's PodGangMap is untouched: it drains the old-hash take-down, grows the
// target new-hash anchor with the subsumed standalone PodClique pods, appends the sub-step's new-hash
// anchor and non-anchor entries, then drops any entry drained to empty.
func (p *subStepPlanner) applySubStep(ss subStep) []grovecorev1alpha1.PodGangEntry {
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
		return strings.Compare(a.Labels[apicommon.LabelEpoch], b.Labels[apicommon.LabelEpoch])
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

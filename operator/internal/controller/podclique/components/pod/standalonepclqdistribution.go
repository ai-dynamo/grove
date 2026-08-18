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

package pod

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"sort"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/index"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// reconcileStandalonePCLQDistribution drives the desired-state sync for a standalone PodClique. It
// updates pclq.Status.PodGangMapping to the desired pod-to-PodGang distribution, then reconciles
// live pods to match.
//
// Why the desired distribution is recorded in status:
// A resource's status usually carries observed state, and desired state usually lives in the spec.
// This field is neither. It is a decision the controller derives and owns, and it is recorded in
// status because it cannot be expressed in the spec nor reconstructed by observation. Kubernetes
// sanctions status carrying controller-owned derived state of this kind, so this is not a misuse of
// status. Three points make the case:
//   - It is not user intent. The intent is Spec.Replicas = N. This field is the controller's derived
//     placement decision, not a second copy of intent.
//   - It cannot be reconstructed from what is observable. The needed placement is history-dependent,
//     meaning the correct target can differ for two pasts that present identically today. If a pod is
//     lost out of band, its replacement must go back into the PodGang the lost pod was in, but the
//     spec, the PodGangMap, and the live pods together do not record which PodGang that was. Only the
//     persisted per-PodGang count tells the reconciler to refill that PodGang rather than the most
//     recent one.
//   - It is single-writer and self-consistent. It is written and read by this same reconciler in
//     lockstep with its own actions, so its correctness does not depend on the PodGangMap
//     component's asynchronous update timing.
//
// Direction of authority:
//   - Coherent update in progress: PGM drives. status.PodGangMapping is overwritten from PGM entries
//     each reconcile.
//   - Steady state (and RollingRecreate): status.PodGangMapping drives. A Spec.Replicas change is
//     translated into a count mutation. Scale-out adds pods to the highest-AnchorIndex anchor.
//     Scale-in drains anchors highest-AnchorIndex first for the required count. This decides only
//     how many pods leave each PodGang, not which pods.
//
// After the desired mapping is persisted, live pods are reconciled to it via per-PodGang deltas. The
// deficit is created and the excess deleted, with a deletion sorter (scoped to each PodGang) choosing
// which pods to delete in this realize-vs-live phase.
func (r _resource) reconcileStandalonePCLQDistribution(logger logr.Logger, sc *syncContext) error {
	if requeueErr := guardAgainstStaleSpecDuringCoherentUpdate(sc); requeueErr != nil {
		return requeueErr
	}
	desiredMapping, err := r.computeDesiredPodGangMapping(sc)
	if err != nil {
		return err
	}
	if err = r.patchPodGangMapping(sc, desiredMapping); err != nil {
		return err
	}

	// Reconcile expectations against the live pod set so subsequent create/delete decisions
	// account for in-flight operations from prior reconciles.
	terminatingUIDs, nonTerminatingUIDs := getTerminatingAndNonTerminatingPodUIDs(sc.existingPCLQPods)
	r.expectationsStore.SyncExpectations(sc.pclqExpectationsStoreKey, nonTerminatingUIDs, terminatingUIDs)

	currentMapping := buildLivePodGangMapping(sc.existingPCLQPods)
	deltas := computePerPodGangDeltas(assignmentsToCountByName(desiredMapping), currentMapping)
	return r.applyPerPodGangDeltas(logger, sc, deltas)
}

// guardAgainstStaleSpecDuringCoherentUpdate requeues when this reconcile sees a fresh PodGangMap
// but a stale PodClique. The PCS reconciler writes PGM (G0) before the PCLQ Spec (G2); a PGM
// watch can fire before the PCLQ Spec watch, so the cache may have new MPGs in PGM while
// pclq.Spec.PodSpec is still the pre-update template. Creating pods from that stale Spec lands
// them in the new-hash MPG carrying the old pod-template-hash, and the MPG never goes Available.
//
// The guard fires only for cliques in pcs.Status.UpdateProgress.InScopeStandalonePodCliques (the
// snapshot of cliques the update must roll). For those, label==status on the PCLQ is ambiguous:
// either the cache has not yet seen the PCS-side patch (label and status both at pre-update), or
// the roll has finished and mutateCurrentHashes advanced status to match the label. UpdateProgress
// disambiguates: a finished roll for the *current* PCS generation has UpdateEndedAt set AND
// PodCliqueSetGenerationHash == pcs.Status.CurrentGenerationHash. Without the generation check a
// stale UpdateEndedAt from the prior update (preserved until initOrResetUpdate overwrites it on
// the next reconcile) would be mistaken for completion of the current one.
func guardAgainstStaleSpecDuringCoherentUpdate(sc *syncContext) error {
	if !componentutils.IsCoherentUpdateInProgress(sc.pcs) {
		return nil
	}
	if !slices.Contains(sc.pcs.Status.UpdateProgress.InScopeStandalonePodCliques, sc.cliqueName) {
		return nil
	}
	if sc.pclq.Status.CurrentPodTemplateHash == nil {
		return nil
	}
	if isPCLQUpdateEndedForCurrentPCSGeneration(sc.pcs, sc.pclq) {
		// Roll for the current PCS generation is complete (state 3). Label may equal Status now
		// (post-mutateCurrentHashes); that equality reflects a finished roll, not a stale cache.
		return nil
	}
	if sc.pclq.Labels[apicommon.LabelPodTemplateHash] != *sc.pclq.Status.CurrentPodTemplateHash {
		// State (2): label points at the new target, status still trails. Cache is fresh enough.
		return nil
	}
	// State (1): label equals status and the roll for the current PCS generation hasn't ended.
	// The cache hasn't observed the PCS-side patch yet; requeue and let the PCLQ Spec watch
	// deliver the new label.
	return groveerr.New(groveerr.ErrCodeRequeueAfter,
		component.OperationSync,
		fmt.Sprintf("PodClique %v cache is stale relative to coherent update; waiting for label propagation",
			client.ObjectKeyFromObject(sc.pclq)),
	)
}

// isPCLQUpdateEndedForCurrentPCSGeneration returns true when pclq.Status.UpdateProgress records a
// completed update tagged with the current PCS generation hash. UpdateProgress is overwritten by
// initOrResetUpdate at the start of each PCLQ-level update, so a stale UpdateEndedAt from a
// previous PCS generation can linger until the controller observes the new spec — this guard
// requires the PodCliqueSetGenerationHash to match before treating UpdateEndedAt as authoritative.
func isPCLQUpdateEndedForCurrentPCSGeneration(pcs *grovecorev1alpha1.PodCliqueSet, pclq *grovecorev1alpha1.PodClique) bool {
	if pclq.Status.UpdateProgress == nil {
		return false
	}
	if pclq.Status.UpdateProgress.UpdateEndedAt == nil {
		return false
	}
	if pcs.Status.CurrentGenerationHash == nil {
		return false
	}
	return pclq.Status.UpdateProgress.PodCliqueSetGenerationHash == *pcs.Status.CurrentGenerationHash
}

// computeDesiredPodGangMapping returns the desired pod-to-PodGang assignments for this PodClique.
//
// During a coherent update the PodGangMap is authoritative and the assignments are rebuilt from PGM
// entries. In steady state the existing status assignments are the source of truth and are adjusted
// only when Spec.Replicas drifts from the assigned pod count.
func (r _resource) computeDesiredPodGangMapping(sc *syncContext) ([]grovecorev1alpha1.PodGangPodCountAssignment, error) {
	if componentutils.IsCoherentUpdateInProgress(sc.pcs) {
		return r.buildMappingFromPodGangMap(sc), nil
	}

	var desired []grovecorev1alpha1.PodGangPodCountAssignment
	if len(sc.pclq.Status.PodGangMapping) == 0 {
		// Fresh PodClique — seed from the PodGangMap (created by the PCS reconciler from spec).
		desired = r.buildMappingFromPodGangMap(sc)
	} else {
		desired = slices.Clone(sc.pclq.Status.PodGangMapping)
	}

	currentSum := sumPodCounts(desired)
	diff := sc.pclq.Spec.Replicas - currentSum
	switch {
	case diff > 0:
		// New pods join the anchor with the highest AnchorIndex, the most recent anchor.
		if err := r.addPodsToLatestAnchor(sc, desired, diff); err != nil {
			return nil, err
		}
	case diff < 0:
		r.reducePodsForScaleIn(sc, desired, -diff)
	}
	return desired, nil
}

// buildMappingFromPodGangMap constructs this standalone PodClique's pod-to-PodGang assignments from
// the PCS replica's PodGangMap (cached on sc.pgm). Only anchor entries carry standalone PodClique pod
// counts, so every assignment references an anchor PodGang. Entries that do not include this
// PodClique's clique, or hold a zero count, are skipped. Returns an empty slice when the PodGangMap
// has not yet been created (sc.pgm is nil).
func (r _resource) buildMappingFromPodGangMap(sc *syncContext) []grovecorev1alpha1.PodGangPodCountAssignment {
	if sc.pgm == nil {
		return nil
	}
	rnr := apicommon.ResourceNameReplica{Name: sc.pcs.Name, Replica: sc.pcsReplicaIndex}
	var assignments []grovecorev1alpha1.PodGangPodCountAssignment
	for _, entry := range sc.pgm.Spec.Entries {
		count, ok := entry.PodCliques[sc.cliqueName]
		if !ok || count == 0 {
			continue
		}
		assignments = append(assignments, grovecorev1alpha1.PodGangPodCountAssignment{
			PodGangName: apicommon.GenerateAnchorPodGangName(rnr, entry.Epoch),
			Epoch:       entry.Epoch,
			PodCount:    count,
		})
	}
	return assignments
}

// sumPodCounts returns the total pod count across all assignments.
func sumPodCounts(assignments []grovecorev1alpha1.PodGangPodCountAssignment) int32 {
	var total int32
	for i := range assignments {
		total += assignments[i].PodCount
	}
	return total
}

// addPodsToLatestAnchor adds diff pods to the anchor PodGang with the highest AnchorIndex, the most
// recent anchor for this PCS replica. New standalone pods always join the most recent anchor. This
// PodClique already holds pods at every anchor (each anchor carries MinAvailable of every standalone
// PodClique), so the assignment for the latest anchor already exists and is incremented in place. It
// errors when the PodGangMap has no anchor entry, or the latest anchor has no assignment, both of
// which are contract violations for a live PCS replica.
func (r _resource) addPodsToLatestAnchor(sc *syncContext, desired []grovecorev1alpha1.PodGangPodCountAssignment, diff int32) error {
	var latest *grovecorev1alpha1.PodGangEntry
	for i := range sc.pgm.Spec.Entries {
		entry := &sc.pgm.Spec.Entries[i]
		if entry.Role != grovecorev1alpha1.PodGangEntryRoleAnchor {
			continue
		}
		if latest == nil || entry.AnchorIndex > latest.AnchorIndex {
			latest = entry
		}
	}
	if latest == nil {
		return groveerr.New(errCodeMissingAnchorPodGangEntry, component.OperationSync,
			fmt.Sprintf("cannot scale out PodClique %v: PodGangMap has no anchor entry", client.ObjectKeyFromObject(sc.pclq)))
	}
	for i := range desired {
		if desired[i].Epoch == latest.Epoch {
			desired[i].PodCount += diff
			return nil
		}
	}
	return groveerr.New(errCodeMissingAnchorPodGangEntry, component.OperationSync,
		fmt.Sprintf("cannot scale out PodClique %v: no assignment for the latest anchor with epoch %s", client.ObjectKeyFromObject(sc.pclq), latest.Epoch))
}

// reducePodsForScaleIn reduces this PodClique's desired pod counts by count for a scale-in. It drains
// the anchor with the highest AnchorIndex first and spills to the next-highest as each is exhausted,
// which peels off the most recently added anchors first, mirroring the scale-out that grows the
// highest anchor. Anchors are ordered by AnchorIndex read from the PodGangMap, a reliable integer
// order, rather than by epoch which is a string. Which pods are actually deleted is decided later,
// on live pod health, by the realize-vs-live phase.
func (r _resource) reducePodsForScaleIn(sc *syncContext, desired []grovecorev1alpha1.PodGangPodCountAssignment, count int32) {
	anchors := make([]*grovecorev1alpha1.PodGangEntry, 0, len(sc.pgm.Spec.Entries))
	for i := range sc.pgm.Spec.Entries {
		if sc.pgm.Spec.Entries[i].Role == grovecorev1alpha1.PodGangEntryRoleAnchor {
			anchors = append(anchors, &sc.pgm.Spec.Entries[i])
		}
	}
	slices.SortFunc(anchors, func(a, b *grovecorev1alpha1.PodGangEntry) int {
		return cmp.Compare(b.AnchorIndex, a.AnchorIndex) // highest AnchorIndex first
	})

	remaining := count
	for _, anchor := range anchors {
		if remaining == 0 {
			break
		}
		for i := range desired {
			if desired[i].Epoch != anchor.Epoch {
				continue
			}
			take := min(remaining, desired[i].PodCount)
			desired[i].PodCount -= take
			remaining -= take
			break
		}
	}
}

// patchPodGangMapping persists the desired assignments to pclq.Status.PodGangMapping if they differ
// from the current value. Assignments with a zero pod count are pruned so the stored mapping carries
// only PodGangs that own at least one pod of this PodClique. The assignments are canonicalized (sorted
// by epoch) so the stored order is deterministic and the equality check avoids waking other
// reconcilers via a no-op patch. An empty desired is normalized to nil.
func (r _resource) patchPodGangMapping(sc *syncContext, desired []grovecorev1alpha1.PodGangPodCountAssignment) error {
	desired = slices.DeleteFunc(desired, func(a grovecorev1alpha1.PodGangPodCountAssignment) bool {
		return a.PodCount == 0
	})
	if len(desired) == 0 {
		desired = nil
	} else {
		slices.SortFunc(desired, func(a, b grovecorev1alpha1.PodGangPodCountAssignment) int {
			return cmp.Compare(a.Epoch, b.Epoch)
		})
	}
	if reflect.DeepEqual(sc.pclq.Status.PodGangMapping, desired) {
		return nil
	}
	patch := client.MergeFrom(sc.pclq.DeepCopy())
	sc.pclq.Status.PodGangMapping = desired
	if err := client.IgnoreNotFound(r.client.Status().Patch(sc.ctx, sc.pclq, patch)); err != nil {
		return groveerr.WrapError(err,
			errCodeUpdatePodCliqueStatus,
			component.OperationSync,
			fmt.Sprintf("failed to patch PodGangMapping on PodClique %v", client.ObjectKeyFromObject(sc.pclq)),
		)
	}
	return nil
}

// buildLivePodGangMapping counts non-terminating live pods by their LabelPodGang.
func buildLivePodGangMapping(pods []*corev1.Pod) map[string]int32 {
	mapping := make(map[string]int32)
	for _, pod := range pods {
		if k8sutils.IsResourceTerminating(pod.ObjectMeta) {
			continue
		}
		pgName, ok := pod.Labels[apicommon.LabelPodGang]
		if !ok {
			continue
		}
		mapping[pgName]++
	}
	return mapping
}

// assignmentsToCountByName projects the assignments to a PodGang-name to pod-count map for delta
// computation against the live pod distribution.
func assignmentsToCountByName(assignments []grovecorev1alpha1.PodGangPodCountAssignment) map[string]int32 {
	m := make(map[string]int32, len(assignments))
	for i := range assignments {
		m[assignments[i].PodGangName] = assignments[i].PodCount
	}
	return m
}

// computePerPodGangDeltas returns desired - current for every PodGang appearing in either map.
// Positive values denote pods to create; negative values denote pods to delete. Entries that
// already match (delta == 0) are dropped so callers can iterate non-trivial work only.
func computePerPodGangDeltas(desired, current map[string]int32) map[string]int32 {
	deltas := make(map[string]int32)
	for name, want := range desired {
		if d := want - current[name]; d != 0 {
			deltas[name] = d
		}
	}
	// Cover PodGangs that exist only in `current` — pods labeled with a PodGang the desired
	// state no longer references (e.g., scale-in zeroed an entry, or a coherent-update transition
	// dropped it). Without this pass the orphaned pods would never be picked up for deletion.
	for name, have := range current {
		if _, seen := desired[name]; seen {
			continue
		}
		deltas[name] = -have
	}
	return deltas
}

// applyPerPodGangDeltas executes create and delete tasks across PodGangs. Creates and deletes
// are batched and dispatched with slow-start concurrency, mirroring the existing flow's
// resilience characteristics.
func (r _resource) applyPerPodGangDeltas(logger logr.Logger, sc *syncContext, deltas map[string]int32) error {
	if len(deltas) == 0 {
		return nil
	}

	totalToCreate := lo.Reduce(lo.Values(deltas), func(agg int32, d int32, _ int) int32 {
		if d > 0 {
			return agg + d
		}
		return agg
	}, int32(0))

	availableIndices, err := index.GetAvailableIndices(logger, sc.existingPCLQPods, int(totalToCreate))
	if err != nil {
		return groveerr.WrapError(err,
			errCodeGetAvailablePodHostNameIndices,
			component.OperationSync,
			fmt.Sprintf("error getting available indices for Pods in PodClique %v", client.ObjectKeyFromObject(sc.pclq)),
		)
	}

	// Stable iteration order for deterministic taskIndex/availableIndex assignment.
	deltaNames := sortedDeltaKeys(deltas)

	createTasks := make([]utils.Task, 0, totalToCreate)
	taskIdx := 0
	for _, pgName := range deltaNames {
		delta := deltas[pgName]
		for d := int32(0); d < delta; d++ {
			createTasks = append(createTasks, r.createPodCreationTask(logger, sc.pcs, sc.pclq, pgName, sc.pclqExpectationsStoreKey, taskIdx, availableIndices[taskIdx]))
			taskIdx++
		}
	}

	deleteTasks := r.buildPerPodGangDeletionTasks(logger, sc, deltas)

	if len(createTasks) > 0 {
		runResult := utils.RunConcurrentlyWithSlowStart(sc.ctx, logger, 1, createTasks)
		if runResult.HasErrors() {
			err = runResult.GetAggregatedError()
			logger.Error(err, "failed to create pods for PCLQ", "runSummary", runResult.GetSummary())
			return err
		}
	}
	if len(deleteTasks) > 0 {
		runResult := utils.RunConcurrentlyWithSlowStart(sc.ctx, logger, 1, deleteTasks)
		if runResult.HasErrors() {
			err = runResult.GetAggregatedError()
			logger.Error(err, "failed to delete pods for PCLQ", "runSummary", runResult.GetSummary())
			return groveerr.WrapError(err,
				errCodeDeletePod,
				component.OperationSync,
				fmt.Sprintf("failed to delete pods for PodClique %v", client.ObjectKeyFromObject(sc.pclq)),
			)
		}
	}
	return nil
}

// sortedDeltaKeys returns a stable, alphabetically sorted list of keys from the given map.
func sortedDeltaKeys(m map[string]int32) []string {
	keys := lo.Keys(m)
	sort.Strings(keys)
	return keys
}

// buildPerPodGangDeletionTasks selects pods to delete from each PodGang where the live count
// exceeds the desired count. Within each PodGang the deletion sorter prioritizes pods to remove.
func (r _resource) buildPerPodGangDeletionTasks(logger logr.Logger, sc *syncContext, deltas map[string]int32) []utils.Task {
	livePodsByPodGang := groupNonTerminatingPodsByPodGang(sc.existingPCLQPods)
	expectedHash := sc.getExpectedPodTemplateHash()

	var tasks []utils.Task
	for _, pgName := range sortedDeltaKeys(deltas) {
		delta := deltas[pgName]
		if delta >= 0 {
			continue
		}
		toDelete := int(-delta)
		pods := livePodsByPodGang[pgName]
		if len(pods) == 0 {
			continue
		}
		sorter := DeletionSorter{
			Pods:                    slices.Clone(pods),
			ExpectedPodTemplateHash: expectedHash,
		}
		sort.Sort(sorter)
		if toDelete > len(sorter.Pods) {
			toDelete = len(sorter.Pods)
		}
		for _, pod := range sorter.Pods[:toDelete] {
			tasks = append(tasks, r.createPodDeletionTask(logger, sc.pclq, pod, sc.pclqExpectationsStoreKey))
		}
	}
	return tasks
}

// groupNonTerminatingPodsByPodGang groups non-terminating pods by their LabelPodGang.
func groupNonTerminatingPodsByPodGang(pods []*corev1.Pod) map[string][]*corev1.Pod {
	out := make(map[string][]*corev1.Pod)
	for _, p := range pods {
		if k8sutils.IsResourceTerminating(p.ObjectMeta) {
			continue
		}
		pgName, ok := p.Labels[apicommon.LabelPodGang]
		if !ok {
			continue
		}
		out[pgName] = append(out[pgName], p)
	}
	return out
}

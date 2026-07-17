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

package pod

import (
	"fmt"
	"maps"
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

// reconcileStandalonePCLQDistribution drives the desired-state-driven sync flow for a standalone
// PodClique. It updates pclq.Status.PodGangMapping to the desired pod-to-PodGang distribution,
// then reconciles live pods to match.
//
// Direction of authority:
//   - Coherent update in progress: PGM drives. status.PodGangMapping is overwritten from PGM
//     entries each reconcile.
//   - Steady state (and RollingRecreate): status.PodGangMapping drives. Spec.Replicas changes
//     are translated into mapping mutations: scale-out adds to the highest-index PodGang;
//     scale-in runs the deletion sorter and decrements per chosen pod's LabelPodGang.
//
// After the desired mapping is persisted in the PCLQ status, live pods are reconciled to the desired distribution
// via per-PodGang deltas: create the deficit, delete the excess (deletion sorter scoped to
// each PodGang).
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
	deltas := computePerPodGangDeltas(desiredMapping, currentMapping)
	return r.applyPerPodGangDeltas(logger, sc, deltas)
}

// computeDesiredPodGangMapping returns the desired pod-to-PodGang mapping for this PCLQ.
//
// During a coherent update PGM is authoritative and the mapping is rebuilt from PGM entries
// every reconcile. In steady state the existing status mapping is the source of truth and is
// mutated only when Spec.Replicas drifts from sum(mapping).
func (r _resource) computeDesiredPodGangMapping(sc *syncContext) (map[string]int32, error) {
	if componentutils.IsCoherentUpdateInProgress(sc.pcs) {
		return r.buildMappingFromPodGangMap(sc)
	}

	var desired map[string]int32
	if len(sc.pclq.Status.PodGangMapping) == 0 {
		// If there are no PodGangMapping captured in the PCLQ status this indicates
		// that it is a fresh PCLQ. For a fresh PCLQ use PodGangMap as a source of truth
		// only to initialize it.
		seed, err := r.buildMappingFromPodGangMap(sc)
		if err != nil {
			return nil, err
		}
		desired = seed
	} else {
		desired = maps.Clone(sc.pclq.Status.PodGangMapping)
	}

	currentSum := lo.Reduce(lo.Values(desired), func(agg int32, v int32, _ int) int32 { return agg + v }, int32(0))
	diff := sc.pclq.Spec.Replicas - currentSum
	switch {
	case diff > 0:
		latestPGName, err := latestPodGangName(desired)
		if err != nil {
			return nil, groveerr.WrapError(err,
				errCodeGetPodGang,
				component.OperationSync,
				fmt.Sprintf("cannot determine latest PodGang for PodClique %v",
					client.ObjectKeyFromObject(sc.pclq)))
		}
		if latestPGName == "" {
			return nil, groveerr.New(errCodeGetPodGang,
				component.OperationSync,
				fmt.Sprintf("cannot scale out PodClique %v: status.PodGangMapping is empty after seed",
					client.ObjectKeyFromObject(sc.pclq)))
		}
		desired[latestPGName] += diff
	case diff < 0:
		decrementMappingForScaleIn(desired, sc.existingPCLQPods, sc.getExpectedPodTemplateHash(), int(-diff))
	}
	return desired, nil
}

// buildMappingFromPodGangMap constructs a pod-to-PodGang mapping from the PCS replica's
// PodGangMap (cached on sc.pgm). Entries that do not include this PCLQ's clique are skipped.
// Counts of zero are skipped to keep the mapping compact. Returns an empty mapping when the
// PodGangMap has not yet been created (sc.pgm is nil).
func (r _resource) buildMappingFromPodGangMap(sc *syncContext) (map[string]int32, error) {
	if sc.pgm == nil {
		return map[string]int32{}, nil
	}
	mapping := make(map[string]int32, len(sc.pgm.Spec.Entries))
	for _, entry := range sc.pgm.Spec.Entries {
		count, ok := entry.PodCliques[sc.cliqueName]
		if !ok || count == 0 {
			continue
		}
		mapping[entry.Name] = count
	}
	return mapping, nil
}

// latestPodGangName returns the entry name from the mapping whose trailing integer suffix is
// the largest. Names must follow the unified PodGang naming convention <pcs>-<replica>-<unix-nano>;
// the largest suffix corresponds to the most recently generated name. Legacy SPG names also
// have an integer trailing segment and parse correctly; a name that does not parse is treated
// as a controller bug and surfaced as an error rather than silently skipped. Returns ("", nil)
// when the mapping is empty.
func latestPodGangName(mapping map[string]int32) (string, error) {
	var (
		latestName string
		bestSuffix = -1
	)
	for name := range mapping {
		suffix, err := utils.ExtractPodGangNameSuffix(name)
		if err != nil {
			return "", fmt.Errorf("PodGang entry name %q in PodGangMapping does not match the expected naming convention: %w", name, err)
		}
		if suffix > bestSuffix {
			latestName, bestSuffix = name, suffix
		}
	}
	return latestName, nil
}

// decrementMappingForScaleIn mutates the desired mapping in place, decrementing it by `count`
// pods, where the pods are chosen via the deletion sorter. This is a pure in-memory step — no
// API calls — that records the per-PodGang scale-in decision the caller will later apply.
func decrementMappingForScaleIn(desired map[string]int32, existingPods []*corev1.Pod, expectedPodTemplateHash string, count int) {
	if count <= 0 {
		return
	}
	candidates := nonTerminatingPods(existingPods)
	if len(candidates) == 0 {
		return
	}
	sorter := DeletionSorter{
		Pods:                    slices.Clone(candidates),
		ExpectedPodTemplateHash: expectedPodTemplateHash,
	}
	sort.Sort(sorter)
	// Walk the full sorter and stop after `remaining` successful decrements. A pod missing
	// the PodGang label cannot be charged to any entry, so it is skipped without consuming
	// the budget — the next pod in deletion order is considered instead. The budget is
	// clamped to the number of pods available so we never wait for decrements we cannot make.
	remaining := min(count, len(sorter.Pods))
	for _, pod := range sorter.Pods {
		if remaining == 0 {
			break
		}
		pgName, ok := pod.Labels[apicommon.LabelPodGang]
		if !ok {
			continue
		}
		if desired[pgName] > 0 {
			desired[pgName]--
			remaining--
		}
	}
}

// nonTerminatingPods returns pods that have not been marked for deletion.
func nonTerminatingPods(pods []*corev1.Pod) []*corev1.Pod {
	return lo.Filter(pods, func(p *corev1.Pod, _ int) bool {
		return !k8sutils.IsResourceTerminating(p.ObjectMeta)
	})
}

// patchPodGangMapping persists the desired mapping to pclq.Status.PodGangMapping if it differs
// from the current value. The check avoids waking other reconcilers via a no-op watch event.
// Zero-count entries are pruned before comparison and persistence so the stored mapping carries
// only PodGangs that actually own at least one pod of this PCLQ. Without this prune, a scale-in
// that decremented an entry to zero would leave a phantom name in the mapping; subsequent
// scale-outs would see it as a candidate (e.g. via latestPodGangName) and route new pods
// into a PodGang that no longer exists in the PodGangMap. Empty maps are normalized to nil for
// status hygiene.
func (r _resource) patchPodGangMapping(sc *syncContext, desired map[string]int32) error {
	for name, count := range desired {
		if count == 0 {
			delete(desired, name)
		}
	}
	if maps.Equal(sc.pclq.Status.PodGangMapping, desired) {
		return nil
	}
	patch := client.MergeFrom(sc.pclq.DeepCopy())
	if len(desired) == 0 {
		sc.pclq.Status.PodGangMapping = nil
	} else {
		sc.pclq.Status.PodGangMapping = desired
	}
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
			err := runResult.GetAggregatedError()
			logger.Error(err, "failed to create pods for PCLQ", "runSummary", runResult.GetSummary())
			return err
		}
	}
	if len(deleteTasks) > 0 {
		runResult := utils.RunConcurrentlyWithSlowStart(sc.ctx, logger, 1, deleteTasks)
		if runResult.HasErrors() {
			err := runResult.GetAggregatedError()
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

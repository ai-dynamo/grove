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

package podclique

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// reconcilePCSGReplicaDistribution drives the desired-state-driven sync flow for the PodCliques
// owned by a PodCliqueScalingGroup. It updates pcsg.Status.PodGangMapping to the desired
// PodGang→PCSG-replica-indices mapping and then reconciles live PCLQs to match.
//
// Direction of authority:
//   - Coherent update in progress: PodGangMap (PGM) drives. status.PodGangMapping is
//     overwritten from PGM entries each reconcile.
//   - Steady state: status.PodGangMapping drives. Spec.Replicas changes are translated into
//     mapping mutations: scale-out generates a new PodGang entry (under the unified naming
//     convention) per new replica, claiming a free index from [0, Spec.Replicas); scale-in
//     walks two tiers (legacy SPGs first, then everything else) and pops indices until the
//     deficit is absorbed.
//
// The index↔PodGang binding is now recorded explicitly in status, so applyPCSGPerPodGangDeltas
// can recover desired PCLQ placement directly from the status mapping without scanning live
// PCLQ labels.
func (r _resource) reconcilePCSGReplicaDistribution(logger logr.Logger, sc *syncContext) error {
	desiredMapping, err := r.computeDesiredPCSGReplicaMapping(sc)
	if err != nil {
		return err
	}
	if err = r.patchPCSGPodGangMapping(sc, desiredMapping); err != nil {
		return err
	}
	return r.applyPCSGPerPodGangDeltas(logger, sc, desiredMapping)
}

// computeDesiredPCSGReplicaMapping returns the desired PodGang→PCSG-replica-indices mapping.
//
// During a coherent update PodGangMap is the authoritative source and the mapping is
// rebuilt from PGM entries every reconcile. In steady state the existing status mapping
// is the source of truth and is mutated only when Spec.Replicas drifts from sum-of-lengths.
func (r _resource) computeDesiredPCSGReplicaMapping(sc *syncContext) (map[string][]int32, error) {
	if componentutils.IsCoherentUpdateInProgress(sc.pcs) {
		return r.buildMappingFromPodGangMap(sc), nil
	}

	var desired map[string][]int32
	if len(sc.pcsg.Status.PodGangMapping) == 0 {
		// Fresh PCSG — seed from PGM (PGM was created by the PCS reconciler from spec).
		desired = r.buildMappingFromPodGangMap(sc)
	} else {
		desired = cloneIndexMapping(sc.pcsg.Status.PodGangMapping)
	}

	pcsNameReplica := apicommon.ResourceNameReplica{Name: sc.pcs.Name, Replica: sc.pcsReplicaIndex}

	currentSum := sumIndexCount(desired)
	diff := sc.pcsg.Spec.Replicas - currentSum
	switch {
	case diff > 0:
		// For each missing replica, claim the smallest free index in [0, Spec.Replicas) and
		// generate a new PodGang name (under the unified naming convention) to hold it.
		freeIndices := computeFreeIndices(desired, sc.pcsg.Spec.Replicas)
		if int32(len(freeIndices)) < diff {
			return nil, groveerr.New(errCodeUpdateStatus,
				component.OperationSync,
				fmt.Sprintf("not enough free PCSG replica indices to generate %d new entries for PodCliqueScalingGroup %v: free=%v",
					diff, client.ObjectKeyFromObject(sc.pcsg), freeIndices))
		}
		newNames := generateScaledPodGangNames(int(diff), sc.pcs.Name, sc.pcsReplicaIndex, r.clk)
		for i, name := range newNames {
			desired[name] = []int32{freeIndices[i]}
		}
	case diff < 0:
		legacySPGPrefix := sc.pcsg.Name + "-"
		bpgName := apicommon.GenerateBasePodGangName(pcsNameReplica)
		if err := decrementPCSGMappingForScaleIn(desired, int(-diff), legacySPGPrefix, bpgName); err != nil {
			return nil, groveerr.WrapError(err,
				errCodeUpdateStatus,
				component.OperationSync,
				fmt.Sprintf("cannot decrement PodGangMapping for scale-in on PodCliqueScalingGroup %v",
					client.ObjectKeyFromObject(sc.pcsg)))
		}
	}
	// Drop entries with empty index slices so the index space stays compact.
	// decrementPCSGMappingForScaleIn leaves entries empty rather than removing them.
	for name, indices := range desired {
		if len(indices) == 0 {
			delete(desired, name)
		}
	}
	return desired, nil
}

// cloneIndexMapping deep-clones the slice values of a PodGang→indices mapping so the caller
// can mutate without aliasing the original (which is typically a status field).
func cloneIndexMapping(in map[string][]int32) map[string][]int32 {
	out := make(map[string][]int32, len(in))
	for k, v := range in {
		out[k] = slices.Clone(v)
	}
	return out
}

// sumIndexCount returns the total count of PCSG replicas across all PodGangs in the mapping.
func sumIndexCount(mapping map[string][]int32) int32 {
	var total int32
	for _, indices := range mapping {
		total += int32(len(indices))
	}
	return total
}

// computeFreeIndices returns indices in [0, specReplicas) that are not currently claimed by
// any entry in `mapping`. Returned sorted ascending so the smallest free index is taken first.
func computeFreeIndices(mapping map[string][]int32, specReplicas int32) []int32 {
	taken := make(map[int32]struct{})
	for _, indices := range mapping {
		for _, idx := range indices {
			taken[idx] = struct{}{}
		}
	}
	free := make([]int32, 0, specReplicas)
	for i := int32(0); i < specReplicas; i++ {
		if _, ok := taken[i]; !ok {
			free = append(free, i)
		}
	}
	return free
}

// buildMappingFromPodGangMap constructs a PodGang→PCSG-replica-indices mapping from the PCS
// replica's PodGangMap (cached on sc.podGangMap). Entries that don't reference this PCSG are
// skipped; entries with empty index slices are skipped to keep the mapping compact.
func (r _resource) buildMappingFromPodGangMap(sc *syncContext) map[string][]int32 {
	pcsgConfigName := apicommon.ExtractScalingGroupNameFromPCSGFQN(sc.pcsg.Name, apicommon.ResourceNameReplica{Name: sc.pcs.Name, Replica: sc.pcsReplicaIndex})
	mapping := make(map[string][]int32, len(sc.podGangMap.Spec.Entries))
	for _, entry := range sc.podGangMap.Spec.Entries {
		indices, ok := entry.PCSGReplicaIndices[pcsgConfigName]
		if !ok || len(indices) == 0 {
			continue
		}
		cloned := slices.Clone(indices)
		slices.Sort(cloned)
		mapping[entry.Name] = cloned
	}
	return mapping
}

// generateScaledPodGangNames generates `count` new PodGang names following the unified
// PodGang naming convention <pcs>-<replica>-<unix-nano>. Names are made unique within this
// call by adding an intra-call counter to the clock-derived nano, so all names share a
// single base nano + (0..count-1) salt.
func generateScaledPodGangNames(count int, pcsName string, pcsReplicaIndex int, clk clock.Clock) []string {
	base := clk.Now().UnixNano()
	names := make([]string, 0, count)
	for i := range count {
		suffix := strconv.FormatInt(base+int64(i), 10)
		names = append(names, apicommon.GeneratePodGangName(pcsName, int32(pcsReplicaIndex), suffix))
	}
	return names
}

// decrementPCSGMappingForScaleIn pops `count` PCSG replica indices from the desired mapping.
// BPG (the base PodGang in the legacy convention) is excluded from the walk entirely:
// popping from it would breach the PCS-level MinAvailable invariant and cause gang termination
// of the PCS replica. The webhook ensures Spec.Replicas - count >= MinAvailable, so the walk
// has enough drainable replicas without touching BPG.
//
// Selection proceeds in two tiers (each tier popped highest-replica-index first within the
// tier and entry):
//   - Tier 1: Legacy SPG entries (pre-upgrade scaled PodGangs, name matching legacySPGPrefix).
//     Each holds 1 replica. Sorted by trailing PG-name suffix descending.
//   - Tier 2: Everything else not BPG-named (entries created under the unified naming
//     convention — MPGs, TailPGs, and steady-state Scaled-PGs). Sorted by trailing PG-name
//     suffix descending; multi-replica entries can absorb multiple steps (popping the
//     highest replica index each time).
//
// Walking legacy SPGs ahead of unified-naming entries naturally compacts older state first
// across a Grove-upgrade boundary, leaving newer entries in the mapping. Within tier 2,
// descending-suffix order corresponds to most-recently-generated first; under the unified
// naming convention this means steady-state Scaled-PGs (created during scale-out, after a
// coherent update completes) are drained before MPGs/TailPGs (created during the coherent
// update itself, with smaller nano suffixes).
//
// Empty entries (zero indices remaining) are left in the mapping and dropped by the caller.
//
// Returns an error if any entry name in the mapping fails to parse — Grove is the sole writer
// of these names so an unparseable name is a contract violation, not a soft skip.
func decrementPCSGMappingForScaleIn(desired map[string][]int32, count int, legacySPGPrefix, bpgName string) error {
	if count <= 0 {
		return nil
	}
	tierLegacySPG, tierUnified := partitionPodGangNamesByTier(desired, legacySPGPrefix, bpgName)
	if err := sortDescByPodGangNameSuffix(tierLegacySPG); err != nil {
		return fmt.Errorf("tier 1 (legacy SPG) entries in PCSG.Status.PodGangMapping have an unparseable name: %w", err)
	}
	if err := sortDescByPodGangNameSuffix(tierUnified); err != nil {
		return fmt.Errorf("tier 2 (unified-naming) entries in PCSG.Status.PodGangMapping have an unparseable name: %w", err)
	}

	remaining := count
	for _, name := range tierLegacySPG {
		if remaining == 0 {
			break
		}
		if popHighestIndex(desired, name) {
			remaining--
		}
	}
	for _, name := range tierUnified {
		for len(desired[name]) > 0 && remaining > 0 {
			popHighestIndex(desired, name)
			remaining--
		}
		if remaining == 0 {
			break
		}
	}
	return nil
}

// popHighestIndex pops the largest replica index from the slice at desired[name]. Returns true
// if a pop happened, false if the slice was empty. The slice is sorted in place to find the
// highest index deterministically; the smaller indices are kept in sorted order.
func popHighestIndex(desired map[string][]int32, name string) bool {
	indices := desired[name]
	if len(indices) == 0 {
		return false
	}
	slices.Sort(indices)
	desired[name] = indices[:len(indices)-1]
	return true
}

// partitionPodGangNamesByTier splits the names in the mapping into two tiers for scale-in:
//   - tierLegacySPG: legacy scaled PodGangs (names matching legacySPGPrefix). Each holds 1 replica.
//   - tierUnified: everything else (entries created under the unified naming convention —
//     MPGs, TailPGs, and steady-state Scaled-PGs). May hold one or more replicas per entry.
//
// bpgName is excluded from both tiers — BPG is the base PodGang carrying MinAvailable replicas
// in the legacy convention and must never be popped during scale-in.
func partitionPodGangNamesByTier(mapping map[string][]int32, legacySPGPrefix, bpgName string) (tierLegacySPG, tierUnified []string) {
	for name := range mapping {
		switch {
		case name == bpgName:
			continue
		case strings.HasPrefix(name, legacySPGPrefix):
			tierLegacySPG = append(tierLegacySPG, name)
		default:
			tierUnified = append(tierUnified, name)
		}
	}
	return
}

// sortDescByPodGangNameSuffix sorts PodGang names by their trailing integer suffix in
// descending order. Returns an error if any name's suffix fails to parse — Grove is the
// sole writer of these names so a parse failure indicates a contract violation, not a
// soft skip.
//
// Under the unified PodGang naming convention, the suffix is the unix-nano timestamp at
// which the name was generated, so descending suffix order corresponds to most-recently-
// generated first. For legacy SPG names, the suffix is the per-replica counter and
// descending order corresponds to highest-counter first.
func sortDescByPodGangNameSuffix(names []string) error {
	suffixes := make(map[string]int, len(names))
	for _, name := range names {
		suffix, err := utils.ExtractPodGangNameSuffix(name)
		if err != nil {
			return fmt.Errorf("PodGang entry name %q does not match the expected naming convention: %w", name, err)
		}
		suffixes[name] = suffix
	}
	sort.SliceStable(names, func(i, j int) bool {
		return suffixes[names[i]] > suffixes[names[j]]
	})
	return nil
}

// patchPCSGPodGangMapping persists the desired mapping to pcsg.Status.PodGangMapping if it
// differs from the current value. The check avoids waking other reconcilers via a no-op watch
// event. Empty maps are normalized to nil for status hygiene.
func (r _resource) patchPCSGPodGangMapping(sc *syncContext, desired map[string][]int32) error {
	if equalIndexMappings(sc.pcsg.Status.PodGangMapping, desired) {
		return nil
	}
	patch := client.MergeFrom(sc.pcsg.DeepCopy())
	if len(desired) == 0 {
		sc.pcsg.Status.PodGangMapping = nil
	} else {
		sc.pcsg.Status.PodGangMapping = desired
	}
	if err := client.IgnoreNotFound(r.client.Status().Patch(sc.ctx, sc.pcsg, patch)); err != nil {
		return groveerr.WrapError(err,
			errCodeUpdateStatus,
			component.OperationSync,
			fmt.Sprintf("failed to patch PodGangMapping on PodCliqueScalingGroup %v",
				client.ObjectKeyFromObject(sc.pcsg)))
	}
	return nil
}

// equalIndexMappings reports whether two PodGang→indices mappings have the same keys and
// the same (set-wise, since slice order on status is not contractual) indices per key.
// Used as a pre-patch no-op check to avoid spurious status updates.
func equalIndexMappings(a, b map[string][]int32) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || len(av) != len(bv) {
			return false
		}
		aSorted := slices.Clone(av)
		bSorted := slices.Clone(bv)
		slices.Sort(aSorted)
		slices.Sort(bSorted)
		if !slices.Equal(aSorted, bSorted) {
			return false
		}
	}
	return true
}

// applyPCSGPerPodGangDeltas reconciles live PCLQs to the desired mapping. For each (pgName, indices)
// pair in desired, every index `i` should have one PCLQ per CliqueName at FQN
// <pcsgFQN>-<i>-<cliqueName> labeled with grove.io/podgang=<pgName>. Indices not in
// ∪slices(desired) get their PCLQs deleted entirely.
//
// PCLQs whose live PodGang label disagrees with status are deleted; the next reconcile creates
// them under the correct PodGang. Deletes go before creates so the controller does not race
// with itself in creating PCLQs at indices currently held by a doomed PCLQ.
func (r _resource) applyPCSGPerPodGangDeltas(logger logr.Logger, sc *syncContext, desired map[string][]int32) error {
	desiredIndexToPG := make(map[int]string)
	for pgName, indices := range desired {
		for _, idx := range indices {
			desiredIndexToPG[int(idx)] = pgName
		}
	}

	deletions, creations, err := computePCSGCountDeltas(desiredIndexToPG, sc.existingPCLQs, sc.pcsg.Spec.CliqueNames)
	if err != nil {
		return groveerr.WrapError(err,
			errCodeParsePodCliqueScalingGroupReplicaIndex,
			component.OperationSync,
			fmt.Sprintf("failed to compute PCSG replica deltas for PodCliqueScalingGroup %v",
				client.ObjectKeyFromObject(sc.pcsg)))
	}
	logger.V(4).Info("pcsg indices for deletions and creations", "deletions", deletions, "creations", creations)
	if len(deletions) > 0 {
		if err := r.deletePCSGReplicas(logger, sc, deletions); err != nil {
			return err
		}
	}
	if len(creations) > 0 {
		if err := r.createPCSGReplicas(logger, sc, creations); err != nil {
			return err
		}
	}
	if componentutils.IsOnDeleteStrategy(sc.pcs) {
		if err := r.syncOnDeletePCLQSpecs(logger, sc, desiredIndexToPG); err != nil {
			return err
		}
	}
	return nil
}

// computePCSGCountDeltas compares desiredIndexToPG (the authoritative replica index → PodGang
// mapping from status) against the live PCLQs and returns:
//   - deletions: replica indices whose live PCLQs should be deleted. Sources:
//     1. Indices not in desiredIndexToPG (obsolete — index belongs to no PodGang).
//     2. Indices whose live LabelPodGang disagrees with desired (the PCLQ will be recreated
//     under the correct PodGang on the next reconcile).
//   - creations: index → PodGang for indices in desired that either have no surviving live PCLQ
//     or are only partially populated (some cliques present, some missing). Partial replicas
//     stay in `creations` so the next reconcile creates the missing siblings; the existing
//     PCLQs are left untouched (the Create attempt swallows AlreadyExists for the present ones).
//
// Terminating PCLQs are ignored entirely — they do not contribute to liveByIndex, and the
// AlreadyExists swallow in doCreate handles the brief race window where the operator tries to
// re-create an FQN whose old PCLQ is still terminating.
//
// All live PCLQs at one PCSG replica index must share the same LabelPodGang (Grove stamps it
// once at creation and never updates it). A missing label or divergent labels at one index
// indicate a contract violation and surface as an error.
func computePCSGCountDeltas(desiredIndexToPG map[int]string, livePCLQs []grovecorev1alpha1.PodClique, pcsgCliqueNames []string) (deletionIndices []int, creations map[int]string, err error) {
	creations = make(map[int]string, len(desiredIndexToPG))
	maps.Copy(creations, desiredIndexToPG)

	// liveByIndex: PCSG replica index → the LabelPodGang shared by every PCLQ at that index.
	liveByIndex := make(map[int]string)
	// liveCliquesByIndex: PCSG replica index → set of clique names with a non-terminating PCLQ.
	// Used to detect "half-populated" indices where some cliques exist and others don't.
	liveCliquesByIndex := make(map[int]sets.Set[string])

	for i := range livePCLQs {
		if k8sutils.IsResourceTerminating(livePCLQs[i].ObjectMeta) {
			continue
		}
		idx, parseErr := k8sutils.GetPodCliqueScalingGroupReplicaIndex(livePCLQs[i].ObjectMeta)
		if parseErr != nil {
			err = parseErr
			return
		}
		pgLabel, ok := livePCLQs[i].Labels[apicommon.LabelPodGang]
		if !ok {
			err = fmt.Errorf("PodClique %s is missing required %s label", livePCLQs[i].Name, apicommon.LabelPodGang)
			return
		}
		if existing, seen := liveByIndex[idx]; seen && existing != pgLabel {
			err = fmt.Errorf("PodCliques at PCSG replica index %d have divergent %s labels: %q vs %q", idx, apicommon.LabelPodGang, existing, pgLabel)
			return
		}
		liveByIndex[idx] = pgLabel

		cliqueName, parseErr := utils.GetPodCliqueNameFromPodCliqueFQN(livePCLQs[i].ObjectMeta)
		if parseErr != nil {
			err = parseErr
			return
		}
		if liveCliquesByIndex[idx] == nil {
			liveCliquesByIndex[idx] = sets.New[string]()
		}
		liveCliquesByIndex[idx].Insert(cliqueName)
	}

	expectedCliques := sets.New(pcsgCliqueNames...)
	for idx, livePodGangLabel := range liveByIndex {
		desiredPG, inDesired := desiredIndexToPG[idx]
		if !inDesired || livePodGangLabel != desiredPG {
			// Either the index is obsolete or its PodGang label disagrees with desired —
			// delete and let the next reconcile recreate under the correct PodGang.
			deletionIndices = append(deletionIndices, idx)
			continue
		}
		// Correct PodGang label. The index is only fully covered when every clique in the
		// PCSG config has a non-terminating PCLQ at this index. A half-populated replica
		// (some cliques missing) keeps creations[idx] populated so the next reconcile creates
		// the missing siblings; doCreate swallows AlreadyExists for siblings that already exist.
		if liveCliquesByIndex[idx].Equal(expectedCliques) {
			delete(creations, idx)
		}
	}

	return
}

// deletePCSGReplicas deletes all PCLQs belonging to the given PCSG replica indices.
func (r _resource) deletePCSGReplicas(logger logr.Logger, sc *syncContext, replicaIndices []int) error {
	deletionTasks := r.createDeleteTasks(logger, sc.pcs, sc.pcsg.Name, replicaIndices, "delete excess PCSG replicas")
	return r.triggerDeletionOfPodCliques(sc.ctx, logger, client.ObjectKeyFromObject(sc.pcsg), deletionTasks)
}

// createPCSGReplicas creates the PCLQs for the given PCSG replica index → PodGang name
// assignments. Each replica generates one PCLQ per CliqueName in the PCSG config. Creation
// uses doCreate (a plain Create) since this path only handles PCLQs that don't yet exist; the
// OnDelete strategy's "preserve existing replicas via CreateOrPatch" behavior is irrelevant for
// fresh PCLQs.
func (r _resource) createPCSGReplicas(logger logr.Logger, sc *syncContext, assignments map[int]string) error {
	tasks := make([]utils.Task, 0, len(assignments)*len(sc.pcsg.Spec.CliqueNames))
	// Sort assignments by index for deterministic creation order.
	indices := lo.Keys(assignments)
	sort.Ints(indices)
	for _, pcsgReplicaIndex := range indices {
		podGangName := assignments[pcsgReplicaIndex]
		for _, cliqueName := range sc.pcsg.Spec.CliqueNames {
			pclqFQN := apicommon.GeneratePodCliqueName(apicommon.ResourceNameReplica{Name: sc.pcsg.Name, Replica: pcsgReplicaIndex}, cliqueName)
			pclqObjectKey := client.ObjectKey{Name: pclqFQN, Namespace: sc.pcsg.Namespace}
			pgName := podGangName
			replicaIdx := pcsgReplicaIndex
			tasks = append(tasks, utils.Task{
				Name: fmt.Sprintf("CreatePodClique-%s", pclqFQN),
				Fn: func(ctx context.Context) error {
					return r.doCreate(ctx, logger, sc.pcs, sc.pcsg, replicaIdx, pclqObjectKey, pgName)
				},
			})
		}
	}
	if runResult := utils.RunConcurrently(sc.ctx, logger, tasks); runResult.HasErrors() {
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errCodeCreatePodCliques,
			component.OperationSync,
			fmt.Sprintf("Error creating PodCliques for PodCliqueScalingGroup: %v, run summary: %s",
				client.ObjectKeyFromObject(sc.pcsg), runResult.GetSummary()))
	}
	return nil
}

// syncOnDeletePCLQSpecs patches PCSG-owned PodCliques whose pod-template-hash label does not
// match the expected hash. Under OnDelete the reconciliation loop never deletes-and-recreates
// existing replicas — spec updates are picked up only when a pod is manually deleted. To ensure
// that the replacement pod gets the new spec, we must update the PCLQ resource itself, because
// the Pod component builds new pods from pclq.Spec.PodSpec. This is the PCSG equivalent of
// what the PCS controller does for standalone PCLQs via CreateOrPatch.
//
// Only PCLQs that belong to indices present in desiredIndexToPG are patched; PCLQs targeted for
// deletion (absent from desiredIndexToPG) are left alone — they will be removed by
// deletePCSGReplicas on this same reconcile.
func (r _resource) syncOnDeletePCLQSpecs(logger logr.Logger, sc *syncContext, desiredIndexToPG map[int]string) error {
	tasks := make([]utils.Task, 0)
	for i := range sc.existingPCLQs {
		pclq := &sc.existingPCLQs[i]
		if k8sutils.IsResourceTerminating(pclq.ObjectMeta) {
			continue
		}
		idx, err := k8sutils.GetPodCliqueScalingGroupReplicaIndex(pclq.ObjectMeta)
		if err != nil {
			return groveerr.WrapError(err,
				errCodeParsePodCliqueScalingGroupReplicaIndex,
				component.OperationSync,
				fmt.Sprintf("failed to get PCSG replica index for PodClique %v", client.ObjectKeyFromObject(pclq)))
		}
		podGangName, inDesired := desiredIndexToPG[idx]
		if !inDesired {
			continue
		}
		hashCandidates, hasExpected := sc.expectedPCLQPodTemplateHashes[pclq.Name]
		if !hasExpected {
			continue
		}
		currentHash := pclq.Labels[apicommon.LabelPodTemplateHash]
		if hashCandidates.Matches(currentHash) {
			continue
		}
		pclqObjectKey := client.ObjectKeyFromObject(pclq)
		pgName := podGangName
		replicaIdx := idx
		tasks = append(tasks, utils.Task{
			Name: fmt.Sprintf("UpdatePodClique-%s", pclqObjectKey.Name),
			Fn: func(ctx context.Context) error {
				return r.doCreateOrUpdate(ctx, logger, sc.pcs, sc.pcsg, replicaIdx, pclqObjectKey, true, pgName)
			},
		})
	}
	if len(tasks) == 0 {
		return nil
	}
	logger.Info("Patching PCSG-owned PodCliques to new spec for OnDelete strategy", "count", len(tasks))
	if runResult := utils.RunConcurrently(sc.ctx, logger, tasks); runResult.HasErrors() {
		return groveerr.WrapError(runResult.GetAggregatedError(),
			errCodeCreateOrUpdatePodCliques,
			component.OperationSync,
			fmt.Sprintf("Error updating PodCliques for OnDelete PodCliqueScalingGroup %v: %s",
				client.ObjectKeyFromObject(sc.pcsg), runResult.GetSummary()))
	}
	return nil
}

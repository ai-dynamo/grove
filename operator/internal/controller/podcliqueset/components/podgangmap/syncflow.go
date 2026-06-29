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

package podgangmap

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"
	"github.com/ai-dynamo/grove/operator/internal/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/samber/lo"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// syncContext holds the state required during the PodGangMap sync flow.
type syncContext struct {
	pcs              *grovecorev1alpha1.PodCliqueSet
	logger           logr.Logger
	pclqsByReplica   map[int][]grovecorev1alpha1.PodClique
	pcsgsByReplica   map[int][]grovecorev1alpha1.PodCliqueScalingGroup
	existingPGMNames []string
}

// prepareSyncFlow fetches the state needed for the sync flow.
func (r _resource) prepareSyncFlow(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) (*syncContext, error) {
	var (
		err error
		sc  = &syncContext{
			pcs:    pcs,
			logger: logger,
		}
	)
	sc.pclqsByReplica, err = r.getPCLQsByReplica(ctx, pcs)
	if err != nil {
		return nil, err
	}
	sc.pcsgsByReplica, err = r.getPCSGsByReplica(ctx, pcs)
	if err != nil {
		return nil, err
	}
	sc.existingPGMNames, err = r.GetExistingResourceNames(ctx, logger, pcs.ObjectMeta)
	if err != nil {
		return nil, err
	}

	return sc, nil
}

// getPCLQsByReplica fetches all PCLQs for the PCS and groups them by PCS replica index.
func (r _resource) getPCLQsByReplica(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (map[int][]grovecorev1alpha1.PodClique, error) {
	existingPCLQs, err := componentutils.GetPCLQsMatchingLabels(ctx, r.client, pcs.Namespace, apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name))
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPCLQs,
			component.OperationSync,
			fmt.Sprintf("Error listing PodCliques for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	pclqsByReplica, err := componentutils.GroupPCLQsByPCSReplicaIndex(existingPCLQs)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPCLQs,
			component.OperationSync,
			fmt.Sprintf("Error grouping PodCliques by replica index for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	return pclqsByReplica, nil
}

// getPCSGsByReplica fetches all PCSGs for the PCS and groups them by PCS replica index.
func (r _resource) getPCSGsByReplica(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (map[int][]grovecorev1alpha1.PodCliqueScalingGroup, error) {
	existingPCSGs, err := componentutils.ListPCSGsForPCS(ctx, r.client, client.ObjectKeyFromObject(pcs))
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPCLQs,
			component.OperationSync,
			fmt.Sprintf("Error listing PCSGs for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	pcsgsByReplica, err := componentutils.GroupPCSGsByPCSReplicaIndex(existingPCSGs)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPCLQs,
			component.OperationSync,
			fmt.Sprintf("Error grouping PCSGs by replica index for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	return pcsgsByReplica, nil
}

// runSyncFlow dispatches to the appropriate sync strategy based on PCS state.
func (r _resource) runSyncFlow(ctx context.Context, sc *syncContext) error {
	if componentutils.IsCoherentUpdateInProgress(sc.pcs) {
		return r.syncCoherentUpdateEntries(ctx, sc)
	}
	return r.syncSteadyStateEntries(ctx, sc)
}

// syncCoherentUpdateEntries computes and persists PodGangMap entries during a coherent update.
func (r _resource) syncCoherentUpdateEntries(ctx context.Context, sc *syncContext) error {
	// MVU template is the snapshot captured on PCS.Status.UpdateProgress at update start.
	// It is invariant for the lifetime of the update, so compute it once and reuse across replicas.
	template, err := computeMVUTemplate(sc.pcs)
	if err != nil {
		return groveerr.WrapError(err,
			errCodeSyncPodGangMap,
			component.OperationSync,
			fmt.Sprintf("Error computing MVU template for PodCliqueSet: %v", client.ObjectKeyFromObject(sc.pcs)),
		)
	}

	expectedPGMNames := make([]string, 0, sc.pcs.Spec.Replicas)
	for pcsReplicaIndex := range sc.pcs.Spec.Replicas {
		pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: sc.pcs.Name, Replica: int(pcsReplicaIndex)})
		expectedPGMNames = append(expectedPGMNames, pgmName)

		entries, err := r.computeCoherentUpdateEntries(ctx, sc.pcs, int(pcsReplicaIndex), pgmName, sc.pclqsByReplica[int(pcsReplicaIndex)], template)
		if err != nil {
			return groveerr.WrapError(err,
				errCodeSyncPodGangMap,
				component.OperationSync,
				fmt.Sprintf("Error computing entries for PodGangMap %s: %v", pgmName, client.ObjectKeyFromObject(sc.pcs)),
			)
		}
		if err = r.createOrPatchPodGangMap(ctx, sc.pcs, pgmName, int(pcsReplicaIndex), entries); err != nil {
			return err
		}
	}

	// Delete excess PodGangMaps (from scale-in).
	if err = r.deleteOrphanedPodGangMaps(ctx, sc, expectedPGMNames); err != nil {
		return err
	}

	sc.logger.Info("Successfully synced PodGangMap resources during coherent update")
	return nil
}

// computeCoherentUpdateEntries computes PodGangMap entries for a coherent update.
// On the first reconcile (PodGangMap doesn't exist yet): it initializes old entries from existing
// PodGang resources and computes the first iteration's new entries.
// On subsequent reconciles (PodGangMap exists): it reads existing entries, separates into old-hash
// and new-hash, and computes next iteration's entries.
// Returns the complete set of entries for the PodGangMap: updated old entries + all new entries.
func (r _resource) computeCoherentUpdateEntries(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, replicaIndex int, pgmName string, pclqs []grovecorev1alpha1.PodClique, template *mvuTemplate) ([]grovecorev1alpha1.PodGangEntry, error) {
	newGenerationHash := *pcs.Status.CurrentGenerationHash
	// Get old-hash and previously-created new-hash entries.
	oldEntries, existingNewEntries, err := r.getOldAndNewEntries(ctx, pcs, replicaIndex, pgmName, pclqs, newGenerationHash)
	if err != nil {
		return nil, err
	}

	// Gate the next iteration on the previously-minted new-hash entry being Available. Without
	// this, every PCS reconcile would advance the MVU iteration regardless of whether the prior
	// MPG/Tail-PG has stabilised, leading to multiple new-hash MPGs in flight at once and the
	// associated unbounded simultaneous pod churn. Only mint the next iteration's entry once
	// the most-recently-minted new-hash PodGang reports Available.
	canAdvance, err := r.canAdvanceMVUIteration(ctx, pcs, existingNewEntries)
	if err != nil {
		return nil, err
	}
	if !canAdvance {
		// Return the existing PGM contents unchanged for this reconcile. The orchestrator's
		// wait-for-Available loop will requeue once the prior PodGang transitions to Available.
		var allEntries []grovecorev1alpha1.PodGangEntry
		allEntries = append(allEntries, oldEntries...)
		allEntries = append(allEntries, existingNewEntries...)
		return allEntries, nil
	}

	// Build the entry builder closure for generating new PodGang names. The clock is read at
	// each builder invocation; an intra-builder counter salts successive calls so that K names
	// generated within one reconcile call are distinct even if the host clock does not advance
	// between successive reads.
	entryBuilder := componentutils.NewPodGangEntryBuilder(pcs.Name, int32(replicaIndex), newGenerationHash, r.clk)

	// MPG names from prior iterations of this update — Tail-PGs created in this iteration depend on them.
	mpgNames := collectMPGNamesFromEntries(existingNewEntries)

	// Compute next iteration's state.
	state := computeNextPodGangMapState(*template, oldEntries, mpgNames, entryBuilder)

	// Combine: updated old entries + previously created new entries + this iteration's new entries.
	var allEntries []grovecorev1alpha1.PodGangEntry
	allEntries = append(allEntries, state.oldEntries...)
	allEntries = append(allEntries, existingNewEntries...)
	allEntries = append(allEntries, state.newEntries...)
	return allEntries, nil
}

// canAdvanceMVUIteration reports whether the coherent-update flow may emit the next MVU
// iteration's new-hash entry. The intent is bounded disruption: each prior iteration's
// PodGangs must reach PodGangConditionTypeReady before another iteration is minted.
// Without this gate, G0 (PodGangMap component) would emit a new iteration on every reconcile
// while G1 (orchestrator) is still waiting on prior ones, producing multiple in-flight MPGs
// concurrently and breaking MVU's bounded-disruption guarantee.
//
// A single MVU iteration may emit multiple entries at once (computeTailPodGangs drains all
// remaining PCSG indices into Tail-PGs in one call), so the gate verifies the whole prior
// batch via componentutils.ArePodGangsReady rather than only the highest-indexed entry.
//
// During a coherent update only the coherent flow mints PGM entries, so every entry in
// existingNewEntries is one this update emitted; the steady-state PGM follower does not run
// while IsCoherentUpdateInProgress is true.
func (r _resource) canAdvanceMVUIteration(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, existingNewEntries []grovecorev1alpha1.PodGangEntry) (bool, error) {
	names := make([]string, 0, len(existingNewEntries))
	for _, e := range existingNewEntries {
		names = append(names, e.Name)
	}
	return componentutils.ArePodGangsReady(ctx, r.client, pcs.Namespace, names)
}

// collectMPGNamesFromEntries returns the names of MVU PodGang entries in the given slice.
// MPG entries have no DependsOn; Tail-PG entries depend on MPGs.
func collectMPGNamesFromEntries(entries []grovecorev1alpha1.PodGangEntry) []string {
	var names []string
	for _, entry := range entries {
		if len(entry.DependsOn) == 0 {
			names = append(names, entry.Name)
		}
	}
	return names
}

// getOldAndNewEntries retrieves old-hash and new-hash entries for the given replica.
// If the PodGangMap doesn't exist yet (first reconcile of this update), it initializes
// entries from existing PodGang resources and splits them by hash.
func (r _resource) getOldAndNewEntries(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, replicaIndex int, pgmName string, pclqs []grovecorev1alpha1.PodClique, newHash string) (oldEntries, newEntries []grovecorev1alpha1.PodGangEntry, err error) {
	pgm := &grovecorev1alpha1.PodGangMap{}
	err = r.client.Get(ctx, client.ObjectKey{Namespace: pcs.Namespace, Name: pgmName}, pgm)

	if err != nil {
		if apierrors.IsNotFound(err) {
			// PodGangMap doesn't exist yet — first reconcile of this update.
			var allEntries []grovecorev1alpha1.PodGangEntry
			allEntries, err = r.buildEntriesFromExistingPodGangs(ctx, pcs, replicaIndex, pclqs)
			if err != nil {
				return
			}
			for _, e := range allEntries {
				if e.PodCliqueSetGenerationHash == newHash {
					newEntries = append(newEntries, e)
				} else {
					oldEntries = append(oldEntries, e)
				}
			}
			return
		}
		return
	}

	// PodGangMap exists — separate entries by generation hash.
	for _, entry := range pgm.Spec.Entries {
		if entry.PodCliqueSetGenerationHash == newHash {
			newEntries = append(newEntries, entry)
		} else {
			oldEntries = append(oldEntries, entry)
		}
	}
	return
}

// buildEntriesFromExistingPodGangs lists every PodGang resource for this PCS replica and
// rebuilds PodGangEntries from their PodGroup specs. Each entry's
// PodCliqueSetGenerationHash is sourced in priority order:
//  1. The PodGang's LabelPodCliqueSetGenerationHash, set by the current operator on every
//     PodGang it creates.
//  2. If the label is absent (legacy PodGang from a pre-label operator) AND a coherent
//     update is in flight, the hash from any live PCLQ's status — that's the pre-update
//     hash, which is what an unlabeled live PodGang corresponds to.
//  3. Otherwise (steady state, no update in flight), pcs.Status.CurrentGenerationHash —
//     which is what every live PodGang corresponds to in steady state.
func (r _resource) buildEntriesFromExistingPodGangs(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, replicaIndex int, pclqs []grovecorev1alpha1.PodClique) ([]grovecorev1alpha1.PodGangEntry, error) {
	existingPodGangs, err := componentutils.GetExistingPodGangs(ctx, r.client, pcs.ObjectMeta, pcs.Namespace)
	if err != nil {
		return nil, err
	}

	podGangsForReplica := lo.Filter(existingPodGangs, func(pg groveschedulerv1alpha1.PodGang, _ int) bool {
		pgReplicaIndex, ok := getPodGangPCSReplicaIndex(pg, pcs.Name)
		return ok && pgReplicaIndex == replicaIndex
	})

	entries := make([]grovecorev1alpha1.PodGangEntry, 0, len(podGangsForReplica))
	for _, pg := range podGangsForReplica {
		hash := podGangGenerationHash(pcs, pg, pclqs)
		entry, err := buildEntryFromPodGang(pcs, hash, pg)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}

	populateDependsOnForReconstructedEntries(entries)
	return entries, nil
}

// podGangGenerationHash returns the generation hash a live PodGang corresponds to. Prefers the
// PodGang's own label; falls back to the pre-update hash sampled from PCLQ status when an
// update is in flight; falls back to PCS.Status.CurrentGenerationHash in steady state.
func podGangGenerationHash(pcs *grovecorev1alpha1.PodCliqueSet, pg groveschedulerv1alpha1.PodGang, pclqs []grovecorev1alpha1.PodClique) string {
	if hash := pg.Labels[apicommon.LabelPodCliqueSetGenerationHash]; hash != "" {
		return hash
	}
	if componentutils.IsCoherentUpdateInProgress(pcs) {
		return preUpdateHashFromPCLQStatus(pclqs)
	}
	if pcs.Status.CurrentGenerationHash != nil {
		return *pcs.Status.CurrentGenerationHash
	}
	return ""
}

// preUpdateHashFromPCLQStatus returns the generation hash that live PCLQs are currently running.
// Sampled from any PCLQ with a non-nil CurrentPodCliqueSetGenerationHash. During a coherent
// update PCS.Status.CurrentGenerationHash has already advanced to the new hash but PCLQ status
// updates lazily, so PCLQs that haven't rolled yet report the pre-update hash.
func preUpdateHashFromPCLQStatus(pclqs []grovecorev1alpha1.PodClique) string {
	for _, pclq := range pclqs {
		if pclq.Status.CurrentPodCliqueSetGenerationHash != nil {
			return *pclq.Status.CurrentPodCliqueSetGenerationHash
		}
	}
	return ""
}

// populateDependsOnForReconstructedEntries sets DependsOn on entries reconstructed from existing
// PodGangs. Entries with non-empty PodCliques (BPG or MPG) have no deps. Entries with empty
// PodCliques (SPG or Tail-PG) depend on every sibling entry that has non-empty PodCliques.
func populateDependsOnForReconstructedEntries(entries []grovecorev1alpha1.PodGangEntry) {
	var anchorNames []string
	for _, entry := range entries {
		if len(entry.PodCliques) > 0 {
			anchorNames = append(anchorNames, entry.Name)
		}
	}
	if len(anchorNames) == 0 {
		return
	}
	for i := range entries {
		if len(entries[i].PodCliques) == 0 {
			entries[i].DependsOn = anchorNames
		}
	}
}

// getPodGangPCSReplicaIndex returns the PodCliqueSet replica index that owns this PodGang.
// Prefers the LabelPodCliqueSetReplicaIndex label; falls back to parsing the PodGang name
// for legacy PodGangs created before the label was stamped. PodGang names are of the form
// <pcsName>-<replicaIdx>[-<suffix>], so the segment immediately after <pcsName>- is the
// replica index. Returns (index, true) on success, (0, false) on parse failure.
func getPodGangPCSReplicaIndex(pg groveschedulerv1alpha1.PodGang, pcsName string) (int, bool) {
	if v, ok := pg.Labels[apicommon.LabelPodCliqueSetReplicaIndex]; ok {
		if idx, err := strconv.Atoi(v); err == nil {
			return idx, true
		}
	}
	prefix := pcsName + "-"
	if !strings.HasPrefix(pg.Name, prefix) {
		return 0, false
	}
	rest := pg.Name[len(prefix):]
	if dash := strings.IndexByte(rest, '-'); dash >= 0 {
		rest = rest[:dash]
	}
	idx, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return idx, true
}

// buildEntryFromPodGang constructs a PodGangEntry from an existing PodGang resource.
// It maps PodGroups to standalone PCLQ pod counts and PCSG replica indices. Indices are
// recovered from the PodGroup name (which is the PCLQ FQN of the form
// <pcsgFQN>-<pcsgReplicaIndex>-<cliqueName>). Each PCSG replica appears once per constituent
// clique, so the same index is observed multiple times for one PCSG; we de-duplicate.
func buildEntryFromPodGang(pcs *grovecorev1alpha1.PodCliqueSet, pcsGenerationHash string, pg groveschedulerv1alpha1.PodGang) (grovecorev1alpha1.PodGangEntry, error) {
	pcsReplicaIndex, ok := getPodGangPCSReplicaIndex(pg, pcs.Name)
	if !ok {
		return grovecorev1alpha1.PodGangEntry{}, fmt.Errorf("cannot determine PCS replica index for PodGang %q", pg.Name)
	}
	pcsNameReplica := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}

	entry := grovecorev1alpha1.PodGangEntry{
		Name:                       pg.Name,
		PodCliqueSetGenerationHash: pcsGenerationHash,
		PodCliques:                 make(map[string]int32),
		PCSGReplicaIndices:         make(map[string][]int32),
	}

	pcsgIndexSets := make(map[string]map[int32]struct{})

	for _, podGroup := range pg.Spec.PodGroups {
		cliqueName, err := extractCliqueName(podGroup.Name, pcs)
		if err != nil {
			return grovecorev1alpha1.PodGangEntry{}, err
		}
		pcsgConfig := componentutils.FindScalingGroupConfigForClique(pcs.Spec.Template.PodCliqueScalingGroupConfigs, cliqueName)
		if pcsgConfig == nil {
			entry.PodCliques[cliqueName] = int32(len(podGroup.PodReferences))
			continue
		}
		pcsgFQN := apicommon.GeneratePodCliqueScalingGroupName(pcsNameReplica, pcsgConfig.Name)
		idx, err := extractPCSGReplicaIndexFromPCLQFQN(podGroup.Name, pcsgFQN, cliqueName)
		if err != nil {
			return grovecorev1alpha1.PodGangEntry{}, err
		}
		set, ok := pcsgIndexSets[pcsgConfig.Name]
		if !ok {
			set = make(map[int32]struct{})
			pcsgIndexSets[pcsgConfig.Name] = set
		}
		set[idx] = struct{}{}
	}

	for pcsgName, set := range pcsgIndexSets {
		indices := make([]int32, 0, len(set))
		for idx := range set {
			indices = append(indices, idx)
		}
		slices.Sort(indices)
		entry.PCSGReplicaIndices[pcsgName] = indices
	}

	return entry, nil
}

// extractPCSGReplicaIndexFromPCLQFQN parses the PCSG replica index from a PCLQ FQN of the
// form <pcsgFQN>-<pcsgReplicaIndex>-<cliqueName>. Returns an error if the FQN does not match
// the expected shape — Grove is the sole writer of these names so a parse failure indicates
// a contract violation, not a soft skip.
func extractPCSGReplicaIndexFromPCLQFQN(pclqFQN, pcsgFQN, cliqueName string) (int32, error) {
	prefix := pcsgFQN + "-"
	suffix := "-" + cliqueName
	if !strings.HasPrefix(pclqFQN, prefix) || !strings.HasSuffix(pclqFQN, suffix) {
		return 0, fmt.Errorf("PCLQ FQN %q does not match expected shape %s<index>%s", pclqFQN, prefix, suffix)
	}
	mid := pclqFQN[len(prefix) : len(pclqFQN)-len(suffix)]
	idx, err := strconv.Atoi(mid)
	if err != nil {
		return 0, fmt.Errorf("PCSG replica index in PCLQ FQN %q is not an integer: %w", pclqFQN, err)
	}
	return int32(idx), nil
}

// extractCliqueName extracts the unqualified clique name from a PodGroup name (PCLQ FQN)
// by matching against known clique templates in the PCS spec.
// Returns an error if the PodGroup name does not match any known clique template.
func extractCliqueName(podGroupName string, pcs *grovecorev1alpha1.PodCliqueSet) (string, error) {
	for _, cliqueTemplate := range pcs.Spec.Template.Cliques {
		if cliqueTemplate == nil {
			continue
		}
		suffix := "-" + cliqueTemplate.Name
		if len(podGroupName) > len(suffix) && podGroupName[len(podGroupName)-len(suffix):] == suffix {
			return cliqueTemplate.Name, nil
		}
	}
	return "", fmt.Errorf("PodGroup name %q does not match any known clique template in PCS %s", podGroupName, pcs.Name)
}

// syncSteadyStateEntries follows the desired PCLQ/PCSG status mappings into PGM entries.
// For each PCS replica:
//
//	If its PodGangMap doesn't exist yet, it is created via createPodGangMapForReplica
//	(which decides between MVU-from-spec and Base/Scaled-from-existing-resources).
//
//	If the PodGangMap exists, the follower applies a single rule: skip the replica until
//	every standalone PCLQ AND every PCSG has a non-empty Status.PodGangMapping. Once that
//	gate is open, the follower reconstructs the entry list from the current status mappings
//	via buildEntriesFromStatuses (with the existing entries supplied so that DependsOn is
//	preserved on entries that are still around and inherited by net-new Scaled-PG shells),
//	then drops zero-count entries via removeEmptyEntries.
func (r _resource) syncSteadyStateEntries(ctx context.Context, sc *syncContext) error {
	expectedPGMNames := make([]string, 0, sc.pcs.Spec.Replicas)
	existingPGMNames := sets.New[string](sc.existingPGMNames...)
	for pcsReplicaIndex := range sc.pcs.Spec.Replicas {
		pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: sc.pcs.Name, Replica: int(pcsReplicaIndex)})
		expectedPGMNames = append(expectedPGMNames, pgmName)
		if !existingPGMNames.Has(pgmName) {
			if err := r.createPodGangMapForReplica(ctx, sc, pgmName, int(pcsReplicaIndex)); err != nil {
				return err
			}
			continue
		}

		standalonePCLQs := filterStandalonePCLQs(sc.pclqsByReplica[int(pcsReplicaIndex)])
		pcsgs := sc.pcsgsByReplica[int(pcsReplicaIndex)]
		if !allOwnerMappingsInitialized(sc.pcs, standalonePCLQs, pcsgs) {
			continue
		}

		existingEntries, err := r.getExistingPGMEntries(ctx, sc.pcs, pgmName)
		if err != nil {
			return err
		}
		entries := buildEntriesFromStatuses(existingEntries, sc.pcs, standalonePCLQs, pcsgs, int(pcsReplicaIndex))
		entries = removeEmptyEntries(entries)

		if err = r.createOrPatchPodGangMap(ctx, sc.pcs, pgmName, int(pcsReplicaIndex), entries); err != nil {
			return err
		}
	}

	// Delete excess PodGangMaps left over from a PCS replica scale-in. PodGangMap is
	// owner-referenced to PCS, so it is not garbage-collected when only the PCS replica
	// count shrinks; the steady-state path must clean up explicitly.
	return r.deleteOrphanedPodGangMaps(ctx, sc, expectedPGMNames)
}

// deleteOrphanedPodGangMaps removes any PodGangMap whose name is not in expectedPGMNames.
// Used to clean up PodGangMaps left behind by a PCS replica scale-in.
func (r _resource) deleteOrphanedPodGangMaps(ctx context.Context, sc *syncContext, expectedPGMNames []string) error {
	for _, orphanPGMName := range lo.Filter(sc.existingPGMNames, func(n string, _ int) bool { return !slices.Contains(expectedPGMNames, n) }) {
		pgm := emptyPodGangMap(client.ObjectKey{Namespace: sc.pcs.Namespace, Name: orphanPGMName})
		if err := r.client.Delete(ctx, pgm); err != nil {
			return groveerr.WrapError(err,
				errCodeSyncPodGangMap,
				component.OperationSync,
				fmt.Sprintf("Error deleting orphaned PodGangMap %s for PodCliqueSet: %v", orphanPGMName, client.ObjectKeyFromObject(sc.pcs)),
			)
		}
		sc.logger.Info("Deleted orphaned PodGangMap", "name", orphanPGMName)
	}
	return nil
}

// filterStandalonePCLQs returns the subset of input PCLQs for which IsStandalonePCLQ is true.
// PCSG-owned PCLQs do not carry Status.PodGangMapping in the steady-state contract; their
// PodGang membership is conveyed via the owning PCSG's mapping.
func filterStandalonePCLQs(pclqs []grovecorev1alpha1.PodClique) []grovecorev1alpha1.PodClique {
	out := make([]grovecorev1alpha1.PodClique, 0, len(pclqs))
	for i := range pclqs {
		if componentutils.IsStandalonePCLQ(&pclqs[i]) {
			out = append(out, pclqs[i])
		}
	}
	return out
}

// allOwnerMappingsInitialized returns true when every standalone PCLQ and every PCSG that the
// PCS spec declares is observed in the cache AND has a non-empty Status.PodGangMapping. The
// caller filters PCLQs to standalone-only beforehand. Once a reconciler seeds its mapping with
// non-empty content, normal steady-state reconciles never zero it out — so the gate flips true
// once and stays true until the next coherent update.
//
// The gate compares observed-owner counts against PCS spec, not just non-empty checks against
// whatever owners are currently visible. During the bootstrap window between PCS creation and
// the first PCLQ/PCSG resource being observed by the watch cache, len(standalonePCLQs) or
// len(pcsgs) can be smaller than what spec declares. Rebuilding PGM from a partial owner set
// in that window would wipe entries seeded from spec by createPodGangMapForReplica.
func allOwnerMappingsInitialized(pcs *grovecorev1alpha1.PodCliqueSet, standalonePCLQs []grovecorev1alpha1.PodClique, pcsgs []grovecorev1alpha1.PodCliqueScalingGroup) bool {
	if len(standalonePCLQs) < componentutils.CountStandalonePCLQs(pcs) {
		return false
	}
	if len(pcsgs) < len(pcs.Spec.Template.PodCliqueScalingGroupConfigs) {
		return false
	}
	for _, pclq := range standalonePCLQs {
		if len(pclq.Status.PodGangMapping) == 0 {
			return false
		}
	}
	for _, pcsg := range pcsgs {
		if len(pcsg.Status.PodGangMapping) == 0 {
			return false
		}
	}
	return true
}

// getExistingPGMEntries reads the current entries from the named PodGangMap. Returns nil on
// NotFound (harmless — the next reconcile will recreate the PGM via createPodGangMapForReplica).
func (r _resource) getExistingPGMEntries(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pgmName string) ([]grovecorev1alpha1.PodGangEntry, error) {
	pgm, err := componentutils.GetPodGangMap(ctx, r.client, pgmName, pcs.Namespace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, groveerr.WrapError(err, errCodeSyncPodGangMap, component.OperationSync,
			fmt.Sprintf("Error reading existing PodGangMap %s for PodCliqueSet: %v", pgmName, client.ObjectKeyFromObject(pcs)))
	}
	return pgm.Spec.Entries, nil
}

// createPodGangMapForReplica creates a PodGangMap for a PCS replica that doesn't have one yet.
// If existing PodGangs are found for this replica (e.g. an upgrade from a pre-PGM Grove version)
// the entries are reconstructed from those PodGangs, preserving each PodGang's own generation
// hash. Otherwise, the entries are computed from the PCS spec using the MVU naming convention.
func (r _resource) createPodGangMapForReplica(ctx context.Context, sc *syncContext, pgmName string, pcsReplicaIndex int) error {
	entries, err := r.buildEntriesFromExistingPodGangs(ctx, sc.pcs, pcsReplicaIndex, sc.pclqsByReplica[pcsReplicaIndex])
	if err != nil {
		return groveerr.WrapError(err, errCodeSyncPodGangMap, component.OperationSync,
			fmt.Sprintf("Error reconstructing PodGangMap entries for PodCliqueSet: %v", client.ObjectKeyFromObject(sc.pcs)))
	}
	if len(entries) == 0 {
		entries = r.computeMVUEntriesFromPCSTemplateSpec(sc.pcs, pcsReplicaIndex)
	}
	return r.createOrPatchPodGangMap(ctx, sc.pcs, pgmName, pcsReplicaIndex, entries)
}

// computeMVUEntriesFromPCSTemplateSpec computes all MVU PodGang and Tail-PG entries for a PCS replica from spec.
func (r _resource) computeMVUEntriesFromPCSTemplateSpec(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int) []grovecorev1alpha1.PodGangEntry {
	template := componentutils.ComputeMVUTemplateFromPCSTemplateSpec(pcs)
	pcsGenerationHash := *pcs.Status.CurrentGenerationHash
	standalonePCLQReplicas := componentutils.GetStandalonePCLQReplicasFromPCSTemplateSpec(pcs)
	pcsgReplicas := componentutils.GetPCSGReplicasFromPCSTemplateSpec(pcs)

	// Build the initial PCSG index pool: for each PCSG, [0, totalReplicas) sorted ascending.
	// MVU PodGangs and Tail-PGs draw the lowest indices first as they are popped.
	pcsgIndexPool := make(map[string][]int32, len(pcsgReplicas))
	for name, total := range pcsgReplicas {
		indices := make([]int32, 0, total)
		for i := int32(0); i < total; i++ {
			indices = append(indices, i)
		}
		pcsgIndexPool[name] = indices
	}

	entryBuilder := componentutils.NewPodGangEntryBuilder(pcs.Name, int32(pcsReplicaIndex), pcsGenerationHash, r.clk)
	return computeAllMVUPodGangEntries(template, standalonePCLQReplicas, pcsgIndexPool, entryBuilder)
}

// computeAllMVUPodGangEntries computes all MPG and Tail-PG entries by distributing
// the given standalone PCLQ pod counts and PCSG replica indices into MVU PodGangs.
//
// pcsgIndexPool is mutated in place: indices are popped from the head of each slice as
// MVU PodGangs and Tail-PGs claim them.
func computeAllMVUPodGangEntries(
	template componentutils.MVUTemplate,
	standalonePCLQReplicas map[string]int32,
	pcsgIndexPool map[string][]int32,
	entryBuilder componentutils.PodGangEntryBuilder,
) []grovecorev1alpha1.PodGangEntry {
	var entries []grovecorev1alpha1.PodGangEntry
	var mpgNames []string

	canFormAnotherMVU := canFormMVUFromRemainingReplicas(template, standalonePCLQReplicas, pcsgIndexPool)
	for canFormAnotherMVU {
		mvuPCLQs := make(map[string]int32)
		for name, minAvail := range template.StandalonePCLQs {
			mvuPCLQs[name] = minAvail
			standalonePCLQReplicas[name] -= minAvail
		}
		mvuPCSGIndices := make(map[string][]int32, len(template.PCSGs))
		for name, minAvail := range template.PCSGs {
			pool := pcsgIndexPool[name]
			taken := append([]int32(nil), pool[:minAvail]...)
			pcsgIndexPool[name] = pool[minAvail:]
			mvuPCSGIndices[name] = taken
		}

		canFormAnotherMVU = canFormMVUFromRemainingReplicas(template, standalonePCLQReplicas, pcsgIndexPool)
		if !canFormAnotherMVU {
			for name := range template.StandalonePCLQs {
				if standalonePCLQReplicas[name] > 0 {
					mvuPCLQs[name] += standalonePCLQReplicas[name]
					standalonePCLQReplicas[name] = 0
				}
			}
		}

		mpgEntry := entryBuilder(mvuPCLQs, mvuPCSGIndices, nil)
		mpgNames = append(mpgNames, mpgEntry.Name)
		entries = append(entries, mpgEntry)
	}

	for name := range template.PCSGs {
		for len(pcsgIndexPool[name]) > 0 {
			pool := pcsgIndexPool[name]
			taken := []int32{pool[0]}
			pcsgIndexPool[name] = pool[1:]
			entries = append(entries, entryBuilder(nil, map[string][]int32{name: taken}, mpgNames))
		}
	}

	return entries
}

// canFormMVUFromRemainingReplicas checks if there are enough remaining replicas to form a complete MVU PodGang.
func canFormMVUFromRemainingReplicas(template componentutils.MVUTemplate, standalonePCLQReplicas map[string]int32, pcsgIndexPool map[string][]int32) bool {
	if len(template.StandalonePCLQs) == 0 && len(template.PCSGs) == 0 {
		return false
	}
	for name, minAvail := range template.StandalonePCLQs {
		if standalonePCLQReplicas[name] < minAvail {
			return false
		}
	}
	for name, minAvail := range template.PCSGs {
		if int32(len(pcsgIndexPool[name])) < minAvail {
			return false
		}
	}
	return true
}

// buildEntriesFromStatuses produces the steady-state PodGangEntry list from PCLQ and PCSG
// Status.PodGangMapping. Counts come from those mappings; DependsOn is preserved or inherited
// from existingEntries:
//
//   - An entry whose name is already in existingEntries inherits that entry's DependsOn.
//   - A net-new PodGang name (a Scaled-PG just minted by PCSG scale-out) inherits DependsOn
//     from the anchor PodGangs (existing entries with empty DependsOn — MPGs in MVU PGMs,
//     BPG in legacy PGMs). This guarantees a future gang-termination recreate enforces
//     "anchors schedule before scale-outs" via the pod-component gate-removal logic.
//
// Names absent from a status mapping receive count 0, which lets removeEmptyEntries drop
// scaled-in entries downstream.
func buildEntriesFromStatuses(existingEntries []grovecorev1alpha1.PodGangEntry, pcs *grovecorev1alpha1.PodCliqueSet, standalonePCLQs []grovecorev1alpha1.PodClique, pcsgs []grovecorev1alpha1.PodCliqueScalingGroup, pcsReplicaIndex int) []grovecorev1alpha1.PodGangEntry {
	hash := *pcs.Status.CurrentGenerationHash
	pcsNameReplica := apicommon.ResourceNameReplica{Name: pcs.Name, Replica: pcsReplicaIndex}

	// anchorNames are the names of "anchor" PodGang entries — entries that carry the
	// MinAvailable floor of the gang and are not themselves gated on any other PodGang
	// (i.e. their DependsOn is empty). In the new MVU naming convention these are MPGs
	// (Migration PodGangs); in the legacy convention this is the BPG (Base PodGang).
	// Net-new entries minted in this pass — Scaled-PGs created by PCSG scale-out — adopt
	// these as their DependsOn so a future gang-termination recreate keeps the
	// "anchors schedule before scale-outs" ordering enforced by the pod-component gate
	// removal logic.
	anchorNames := collectMPGNamesFromEntries(existingEntries)
	existingDependsOn := make(map[string][]string, len(existingEntries))
	for _, e := range existingEntries {
		existingDependsOn[e.Name] = e.DependsOn
	}
	dependsOnFor := func(pgName string) []string {
		if d, ok := existingDependsOn[pgName]; ok {
			return d
		}
		return anchorNames
	}

	entryByName := make(map[string]*grovecorev1alpha1.PodGangEntry)

	for _, pclq := range standalonePCLQs {
		cliqueName, _ := utils.GetPodCliqueNameFromPodCliqueFQN(pclq.ObjectMeta)
		for pgName, podCount := range pclq.Status.PodGangMapping {
			entry, ok := entryByName[pgName]
			if !ok {
				entry = &grovecorev1alpha1.PodGangEntry{
					Name:                       pgName,
					PodCliqueSetGenerationHash: hash,
					DependsOn:                  dependsOnFor(pgName),
				}
				entryByName[pgName] = entry
			}
			if entry.PodCliques == nil {
				entry.PodCliques = make(map[string]int32)
			}
			entry.PodCliques[cliqueName] = podCount
		}
	}
	for _, pcsg := range pcsgs {
		pcsgConfigName := apicommon.ExtractScalingGroupNameFromPCSGFQN(pcsg.Name, pcsNameReplica)
		for pgName, replicaIndices := range pcsg.Status.PodGangMapping {
			entry, ok := entryByName[pgName]
			if !ok {
				entry = &grovecorev1alpha1.PodGangEntry{
					Name:                       pgName,
					PodCliqueSetGenerationHash: hash,
					DependsOn:                  dependsOnFor(pgName),
				}
				entryByName[pgName] = entry
			}
			if entry.PCSGReplicaIndices == nil {
				entry.PCSGReplicaIndices = make(map[string][]int32)
			}
			indices := slices.Clone(replicaIndices)
			slices.Sort(indices)
			entry.PCSGReplicaIndices[pcsgConfigName] = indices
		}
	}

	entries := make([]grovecorev1alpha1.PodGangEntry, 0, len(entryByName))
	for _, entry := range entryByName {
		entries = append(entries, *entry)
	}
	return entries
}

// createOrPatchPodGangMap creates or patches a PodGangMap with the given entries.
func (r _resource) createOrPatchPodGangMap(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pgmName string, pcsReplicaIndex int, entries []grovecorev1alpha1.PodGangEntry) error {
	pgm := emptyPodGangMap(client.ObjectKey{Namespace: pcs.Namespace, Name: pgmName})
	if _, err := controllerutil.CreateOrPatch(ctx, r.client, pgm, func() error {
		return r.buildResource(pgm, pcs, pcsReplicaIndex, entries)
	}); err != nil {
		return groveerr.WrapError(err, errCodeSyncPodGangMap, component.OperationSync,
			fmt.Sprintf("Error creating or updating PodGangMap %s for PodCliqueSet: %v", pgmName, client.ObjectKeyFromObject(pcs)))
	}
	return nil
}

// hasInFlightPodGangs returns true if the orchestrator has in-flight PodGangs awaiting availability.
func hasInFlightPodGangs(pcs *grovecorev1alpha1.PodCliqueSet) bool {
	return pcs.Status.UpdateProgress != nil &&
		len(pcs.Status.UpdateProgress.CurrentlyUpdating) > 0 &&
		len(pcs.Status.UpdateProgress.CurrentlyUpdating[0].InFlightPodGangs) > 0
}

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
	"sort"
	"strconv"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// syncSnapshot captures the state required for reconciling PodGangMap
// resources for a PodCliqueSet. It is populated at the start of the synchronization and
// read-only thereafter. Every field reflects state at the moment when it's populated.
type syncSnapshot struct {
	logger                           logr.Logger
	pcs                              *grovecorev1alpha1.PodCliqueSet
	existingStandalonePCLQsByReplica map[int][]grovecorev1alpha1.PodClique
	existingPCSGsByReplica           map[int][]grovecorev1alpha1.PodCliqueScalingGroup
	existingPGMByReplica             map[int]grovecorev1alpha1.PodGangMap
	// mvuTemplate computes the MVU shape if a PodCliqueSet has been updated
	// During an ongoing coherent update this shape is fixed, therefore it is computed once.
	// If no coherent update is in progress this field will be nil
	mvuTemplate *mvuTemplate
}

// liveReplicasForComponentsUnderUpdate returns the live Spec.Replicas of each in-scope component
// (standalone PodClique or PodCliqueScalingGroup) for the given PCS replica, keyed by component
// name. Replicas are read live from the child resources so a scale on a non-updating boundary is
// absorbed on the next reconcile.
func (s *syncSnapshot) liveReplicasForComponentsUnderUpdate(pcsReplicaIndex int) map[string]int32 {
	replicas := make(map[string]int32)
	inScopeComponents := sets.New(s.mvuTemplate.componentNames()...)
	pcsNameReplica := apicommon.ResourceNameReplica{Name: s.pcs.Name, Replica: pcsReplicaIndex}
	for _, pclq := range s.existingStandalonePCLQsByReplica[pcsReplicaIndex] {
		pclqName := apicommon.ExtractPodCliqueNameFromStandalonePCLQFQN(pclq.Name, pcsNameReplica)
		if inScopeComponents.Has(pclqName) {
			replicas[pclqName] = pclq.Spec.Replicas
		}
	}
	for _, pcsg := range s.existingPCSGsByReplica[pcsReplicaIndex] {
		name := apicommon.ExtractScalingGroupNameFromPCSGFQN(pcsg.Name, pcsNameReplica)
		if inScopeComponents.Has(name) {
			replicas[name] = pcsg.Spec.Replicas
		}
	}
	return replicas
}

// mvuTemplate describes the composition of one MVU PodGang. MVU is a minimum viable unit that
// consists of set of MinAvailable replicas of every component in a PodCliqueSet the user has updated.
// It is computed once at update start and remains fixed for the duration of the update.
type mvuTemplate struct {
	// standalonePCLQs is a map of standalone PCLQ name to minAvailable pod count.
	standalonePCLQs map[string]int32
	//pcsgs is a map of PCSG name to minAvailable replicas
	pcsgs map[string]int32
}

// clone returns a deep copy of the template so a subStepPlanner can hold its own maps without
// aliasing the snapshot's mvuTemplate, which is shared across every replica's planner in one sync.
func (m *mvuTemplate) clone() *mvuTemplate {
	return &mvuTemplate{
		standalonePCLQs: maps.Clone(m.standalonePCLQs),
		pcsgs:           maps.Clone(m.pcsgs),
	}
}

// componentNames returns the names of all components that are in-scope for an update.
func (m *mvuTemplate) componentNames() []string {
	names := lo.Keys(m.standalonePCLQs, m.pcsgs)
	sort.Strings(names)
	return names
}

// minAvailableByComponent returns a consolidated map of component name to its minAvailable of all
// in-scope components under update.
func (m *mvuTemplate) minAvailableByComponent() map[string]int32 {
	byComponent := make(map[string]int32, len(m.standalonePCLQs)+len(m.pcsgs))
	maps.Copy(byComponent, m.standalonePCLQs)
	maps.Copy(byComponent, m.pcsgs)
	return byComponent
}

// takeSnapshot queries the live resources and creates a syncSnapshot. Live PodGangs
// are read only when at least one replica lacks PodGangMap entries (the reconstruction
// or bootstrap path may fire). The MVU template is computed only when a coherent
// update is in flight.
func (r _resource) takeSnapshot(ctx context.Context, logger logr.Logger, pcs *grovecorev1alpha1.PodCliqueSet) (syncSnap *syncSnapshot, err error) {
	syncSnap = &syncSnapshot{
		logger: logger,
		pcs:    pcs,
	}
	syncSnap.existingStandalonePCLQsByReplica, err = r.getExistingStandalonePCLQsByReplica(ctx, pcs)
	if err != nil {
		return nil, err
	}
	syncSnap.existingPCSGsByReplica, err = r.getExistingPCSGsByReplica(ctx, pcs)
	if err != nil {
		return nil, err
	}
	syncSnap.existingPGMByReplica, err = r.getExistingPGMByReplica(ctx, pcs)
	if err != nil {
		return nil, err
	}
	if componentutils.IsCoherentUpdateInProgress(pcs) {
		syncSnap.mvuTemplate, err = computeMVUTemplate(pcs)
		if err != nil {
			return nil, err
		}
	}
	return syncSnap, nil
}

// getExistingStandalonePCLQsByReplica fetches all PCLQs for the PCS and groups them by PCS replica index.
func (r _resource) getExistingStandalonePCLQsByReplica(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (map[int][]grovecorev1alpha1.PodClique, error) {
	existingStandalonePCLQs, err := componentutils.GetPCLQsMatchingLabels(ctx, r.client, pcs.Namespace, lo.Assign(
		apicommon.GetDefaultLabelsForPodCliqueSetManagedResources(pcs.Name),
		map[string]string{apicommon.LabelComponentKey: apicommon.LabelComponentNamePodCliqueSetPodClique}))
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPCLQs,
			component.OperationSync,
			fmt.Sprintf("Error listing PodCliques for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	standalonePCLQsByReplica, err := componentutils.GroupPCLQsByPCSReplicaIndex(existingStandalonePCLQs)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGroupPCLQsByReplica,
			component.OperationSync,
			fmt.Sprintf("Error grouping PodCliques by replica index for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	return standalonePCLQsByReplica, nil
}

// getExistingPCSGsByReplica fetches all PCSGs for the PCS and groups them by PCS replica index.
func (r _resource) getExistingPCSGsByReplica(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (map[int][]grovecorev1alpha1.PodCliqueScalingGroup, error) {
	existingPCSGs, err := componentutils.ListPCSGsForPCS(ctx, r.client, client.ObjectKeyFromObject(pcs))
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPCSGs,
			component.OperationSync,
			fmt.Sprintf("Error listing PCSGs for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	pcsgsByReplica, err := componentutils.GroupPCSGsByPCSReplicaIndex(existingPCSGs)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGroupPCSGsByReplica,
			component.OperationSync,
			fmt.Sprintf("Error grouping PCSGs by replica index for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	return pcsgsByReplica, nil
}

func (r _resource) getExistingPGMByReplica(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (map[int]grovecorev1alpha1.PodGangMap, error) {
	existingPGMs, err := componentutils.ListPodGangMapsForPCS(ctx, r.client, client.ObjectKeyFromObject(pcs))
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPodGangMaps,
			component.OperationSync,
			fmt.Sprintf("Error listing PodGangMaps for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	pgmByReplica, err := componentutils.PodGangMapByPCSReplicaIndex(existingPGMs)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPodGangMaps,
			component.OperationSync,
			fmt.Sprintf("Error grouping PodGangMap by replica index: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	return pgmByReplica, nil
}

// computeMVUTemplate builds the MVU template from the standalone PCLQs and PCSGs that were
// updated as part of the current PCS coherent update, captured in PCS.Status.UpdateProgress
// at update start. The captured set is frozen for the lifetime of the update so the template
// stays stable as PCLQs roll over to the new hash. MinAvailable values are looked up from PCS.Spec.Template.
// The webhook prevents MinAvailable changes mid-update, so the spec
// values are the same as they were at update start.
func computeMVUTemplate(pcs *grovecorev1alpha1.PodCliqueSet) (mvuTmpl *mvuTemplate, err error) {
	defer func() {
		if err != nil {
			err = groveerr.WrapError(err,
				errCodeComputeMVUTemplate,
				component.OperationSync,
				fmt.Sprintf("Error computing MVU template for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
			)
		}
	}()
	progress := pcs.Status.UpdateProgress
	if progress == nil {
		err = fmt.Errorf("UpdateProgress is unset on PodCliqueSet %s; cannot derive MVU template", pcs.Name)
		return
	}

	standalone := make(map[string]int32, len(progress.InScopeStandalonePodCliques))
	for _, name := range progress.InScopeStandalonePodCliques {
		template := componentutils.FindPodCliqueTemplateSpecByName(pcs, name)
		if template == nil {
			err = fmt.Errorf("standalone PodClique %q in UpdateProgress.InScopeStandalonePodCliques is no longer in PCS %s spec", name, pcs.Name)
			return
		}
		standalone[name] = *template.Spec.MinAvailable
	}

	pcsgs := make(map[string]int32, len(progress.InScopePodCliqueScalingGroups))
	for _, name := range progress.InScopePodCliqueScalingGroups {
		pcsgConfig := componentutils.FindScalingGroupConfigByName(pcs, name)
		if pcsgConfig == nil {
			err = fmt.Errorf("PodCliqueScalingGroup %q in UpdateProgress.InScopePodCliqueScalingGroups is no longer in PCS %s spec", name, pcs.Name)
			return
		}
		pcsgs[name] = *pcsgConfig.MinAvailable
	}

	return &mvuTemplate{standalonePCLQs: standalone, pcsgs: pcsgs}, nil
}

// runSyncFlow reconciles the PodGangMap for every PCS replica by routing each replica to the
// matching reconcile path, then deletes PodGangMaps orphaned by a PCS replica scale-in.
func (r _resource) runSyncFlow(ctx context.Context, syncSnap *syncSnapshot) error {
	// Reconstruction and bootstrap fire only when a replica lacks PodGangMap entries. When that is
	// the case for any replica, list the PCS PodGangs once and group them by replica index (legacy
	// PodGangs lack the replica-index label, so this uses an all-PCS list with a name-parse
	// fallback). Steady-state and coherent replicas do not use this list.
	var (
		err               error
		podGangsByReplica map[int][]groveschedulerv1alpha1.PodGang
	)
	if anyReplicaLacksPGMEntries(syncSnap) {
		if podGangsByReplica, err = r.listExistingPodGangsByPCSReplica(ctx, syncSnap.pcs); err != nil {
			return err
		}
	}
	for pcsReplicaIndex := range int(syncSnap.pcs.Spec.Replicas) {
		pgm, pgmExists := syncSnap.existingPGMByReplica[pcsReplicaIndex]
		pgmHasEntries := pgmExists && len(pgm.Spec.Entries) > 0
		switch {
		case !pgmHasEntries:
			err = r.reconcileEntriesWhenPGMEmpty(ctx, syncSnap, podGangsByReplica[pcsReplicaIndex], pcsReplicaIndex)
		case componentutils.IsCoherentStrategy(syncSnap.pcs) && isPCSReplicaUnderUpdate(syncSnap.pcs, pcsReplicaIndex):
			err = r.reconcileCoherentUpdateEntries(ctx, syncSnap, pcsReplicaIndex)
		default:
			err = r.reconcileSteadyStateEntries(ctx, syncSnap, pcsReplicaIndex)
		}
		if err != nil {
			return err
		}
	}
	return r.deleteOrphanedPodGangMaps(ctx, syncSnap)
}

// anyReplicaLacksPGMEntries returns true if any PCS replica has no PodGangMap or an
// empty PodGangMap.
func anyReplicaLacksPGMEntries(syncSnap *syncSnapshot) bool {
	for replicaIdx := range int(syncSnap.pcs.Spec.Replicas) {
		pgm, ok := syncSnap.existingPGMByReplica[replicaIdx]
		if !ok || len(pgm.Spec.Entries) == 0 {
			return true
		}
	}
	return false
}

// listExistingPodGangsByPCSReplica fetches every PodGang owned by the PCS and groups them
// by the pcs-replica-index label. Falls back to parsing the PodGang name for legacy
// PodGangs created before the replica-index label was stamped.
func (r _resource) listExistingPodGangsByPCSReplica(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (map[int][]groveschedulerv1alpha1.PodGang, error) {
	podGangs, err := componentutils.ListExistingPodGangs(ctx, r.client, pcs.ObjectMeta)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPodGangs,
			component.OperationSync,
			fmt.Sprintf("Error listing PodGangs for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)))
	}
	podGangsByPCSReplica := make(map[int][]groveschedulerv1alpha1.PodGang, int(pcs.Spec.Replicas))
	for i := range podGangs {
		idx, ok := extractPCSReplicaIndex(podGangs[i], pcs.Name)
		if !ok {
			continue
		}
		podGangsByPCSReplica[idx] = append(podGangsByPCSReplica[idx], podGangs[i])
	}
	return podGangsByPCSReplica, nil
}

// extractPCSReplicaIndex returns the PCS replica index a PodGang belongs to. Prefers the
// pcs-replica-index label. Falls back to parsing the PodGang name (shape:
// <pcsName>-<replicaIdx>[-<suffix>]) for legacy PodGangs that pre-date the label. Returns
// (0, false) on parse failure.
func extractPCSReplicaIndex(pg groveschedulerv1alpha1.PodGang, pcsName string) (int, bool) {
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

// reconcileEntriesWhenPGMEmpty handles a replica whose PodGangMap has no entries. It reconstructs
// from live legacy PodGangs when they exist, otherwise bootstraps from the PCS spec, then persists
// the Spec entries. Status is never touched on this path.
func (r _resource) reconcileEntriesWhenPGMEmpty(ctx context.Context, syncSnap *syncSnapshot, podGangsByPCSReplica []groveschedulerv1alpha1.PodGang, pcsReplicaIndex int) error {
	var (
		entries []grovecorev1alpha1.PodGangEntry
		err     error
	)
	if len(podGangsByPCSReplica) > 0 {
		// PodGangs exist but the PodGangMap has no entries. This is an existing PCS whose BPG/SPG
		// PodGangs were created by a pre-PGM Grove version and Grove has since been upgraded.
		// Reconstruct the PGM entries from the live PodGangs.
		entries, err = reconstructEntriesFromExistingPodGangs(syncSnap.pcs, podGangsByPCSReplica, pcsReplicaIndex, r.clk)
		if err != nil {
			return err
		}
	} else {
		// No PodGangs and no PodGangMap entries. This is a fresh PCS replica. Derive the initial
		// entries from the PCS spec.
		entries = buildBootstrapEntries(syncSnap.pcs, pcsReplicaIndex, r.clk)
	}
	pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: syncSnap.pcs.Name, Replica: pcsReplicaIndex})
	return r.createOrPatchPodGangMapSpec(ctx, syncSnap.pcs, pgmName, pcsReplicaIndex, entries)
}

// reconcileCoherentUpdateEntries handles a replica under coherent update. It computes the next
// sub-step's entries (derived from the current-hash Spec entries and live PodGang readiness) and
// persists the Spec. No status is written: all coherent-update progress is reconstructed from the
// entries and live PodGangs each reconcile.
func (r _resource) reconcileCoherentUpdateEntries(ctx context.Context, syncSnap *syncSnapshot, pcsReplicaIndex int) error {
	entries, err := buildCoherentUpdateEntries(ctx, r.client, pcsReplicaIndex, syncSnap, r.clk)
	if err != nil {
		return err
	}
	pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: syncSnap.pcs.Name, Replica: pcsReplicaIndex})
	return r.createOrPatchPodGangMapSpec(ctx, syncSnap.pcs, pgmName, pcsReplicaIndex, entries)
}

// reconcileSteadyStateEntries handles a replica in steady state. It rebuilds entries from live
// PCLQ and PCSG PodGangMapping statuses and persists the Spec.
func (r _resource) reconcileSteadyStateEntries(ctx context.Context, syncSnap *syncSnapshot, pcsReplicaIndex int) error {
	pgm := syncSnap.existingPGMByReplica[pcsReplicaIndex]
	entries, err := buildEntriesFromPCLQAndPCSGStatuses(syncSnap.pcs, syncSnap.existingStandalonePCLQsByReplica[pcsReplicaIndex], syncSnap.existingPCSGsByReplica[pcsReplicaIndex], &pgm, pcsReplicaIndex, r.clk)
	if err != nil {
		return err
	}
	pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: syncSnap.pcs.Name, Replica: pcsReplicaIndex})
	return r.createOrPatchPodGangMapSpec(ctx, syncSnap.pcs, pgmName, pcsReplicaIndex, entries)
}

// isPCSReplicaUnderUpdate reports whether the PCS replica at the given index is currently under
// update. True when the replica appears in pcs.Status.UpdateProgress.CurrentlyUpdating with
// UpdateEndedAt unset. Does not consult Spec.UpdateStrategy. Callers scope the strategy check.
func isPCSReplicaUnderUpdate(pcs *grovecorev1alpha1.PodCliqueSet, pcsReplicaIndex int) bool {
	return componentutils.IsPCSReplicaInCurrentlyUpdating(pcs, pcsReplicaIndex)
}

// createOrPatchPodGangMapSpec creates or patches the named PodGangMap with the given entries.
func (r _resource) createOrPatchPodGangMapSpec(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet, pgmName string, pcsReplicaIndex int, entries []grovecorev1alpha1.PodGangEntry) error {
	pgm := emptyPodGangMap(client.ObjectKey{Namespace: pcs.Namespace, Name: pgmName})
	if _, err := controllerutil.CreateOrPatch(ctx, r.client, pgm, func() error {
		return r.buildResource(pgm, pcs, pcsReplicaIndex, entries)
	}); err != nil {
		return groveerr.WrapError(err, errCodeCreateOrPatchPodGangMap, component.OperationSync,
			fmt.Sprintf("Error creating or updating PodGangMap %s for PodCliqueSet: %v", pgmName, client.ObjectKeyFromObject(pcs)))
	}
	return nil
}

// deleteOrphanedPodGangMaps deletes PodGangMaps whose replica index is at or beyond the current
// PCS replica count. PodGangMap is owner-referenced to the PCS, so a PCS replica scale-in does
// not garbage-collect them; this cleanup is explicit.
func (r _resource) deleteOrphanedPodGangMaps(ctx context.Context, syncSnap *syncSnapshot) error {
	for pcsReplicaIndex, pgm := range syncSnap.existingPGMByReplica {
		if pcsReplicaIndex < int(syncSnap.pcs.Spec.Replicas) {
			continue
		}
		if err := r.client.Delete(ctx, &pgm); err != nil {
			return groveerr.WrapError(err,
				errCodeDeletePodGangMaps,
				component.OperationSync,
				fmt.Sprintf("Error deleting orphaned PodGangMap %s for PodCliqueSet: %v", pgm.Name, client.ObjectKeyFromObject(syncSnap.pcs)),
			)
		}
		syncSnap.logger.Info("Deleted orphaned PodGangMap", "name", pgm.Name)
	}
	return nil
}

// newPodGangEntry constructs a fresh PodGangEntry. The PodGang name is derived from pcsName,
// replicaIdx, and epoch. Adds the epoch as Labels[grove.io/epoch] and sets DependsOn.
func newPodGangEntry(pcsName string, replicaIdx int, nameSuffix, hash, epoch string, dependsOn []string) grovecorev1alpha1.PodGangEntry {
	entryName := apicommon.GeneratePodGangName(pcsName, replicaIdx, nameSuffix)
	return newPodGangEntryWithName(entryName, hash, epoch, dependsOn)
}

// newPodGangEntryWithName constructs a fresh PodGangEntry given the entry name.
// Adds the epoch as Labels[grove.io/epoch] and sets DependsOn.
func newPodGangEntryWithName(name, hash, epoch string, dependsOn []string) grovecorev1alpha1.PodGangEntry {
	return grovecorev1alpha1.PodGangEntry{
		Name:                       name,
		PodCliqueSetGenerationHash: hash,
		Labels:                     map[string]string{apicommon.LabelEpoch: epoch},
		DependsOn:                  dependsOn,
	}
}

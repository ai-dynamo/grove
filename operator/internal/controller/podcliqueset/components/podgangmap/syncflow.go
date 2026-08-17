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
	"strconv"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// syncSnapshot captures the state required for reconciling PodGangMap resources for a PodCliqueSet.
// It is populated at the start of the synchronization and read-only thereafter.
type syncSnapshot struct {
	logger                           logr.Logger
	pcs                              *grovecorev1alpha1.PodCliqueSet
	existingStandalonePCLQsByReplica map[int][]grovecorev1alpha1.PodClique
	existingPCSGsByReplica           map[int][]grovecorev1alpha1.PodCliqueScalingGroup
	existingPGMByReplica             map[int]grovecorev1alpha1.PodGangMap
	existingPodGangsByReplica        map[int][]groveschedulerv1alpha1.PodGang
}

// takeSnapshot queries the live resources and creates a syncSnapshot.
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
	syncSnap.existingPodGangsByReplica, err = r.getExistingPodGangsByReplica(ctx, pcs)
	if err != nil {
		return nil, err
	}
	return syncSnap, nil
}

// getExistingStandalonePCLQsByReplica fetches all standalone PodCliques for the PCS and groups them by PCS replica index.
func (r _resource) getExistingStandalonePCLQsByReplica(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (map[int][]grovecorev1alpha1.PodClique, error) {
	existingStandalonePCLQs, err := componentutils.GetPodCliquesWithParentPCS(ctx, r.client, client.ObjectKeyFromObject(pcs))
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPCLQs,
			component.OperationSync,
			fmt.Sprintf("Error listing standalone PodCliques for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	standalonePCLQsByReplica, err := componentutils.GroupPCLQsByPCSReplicaIndex(existingStandalonePCLQs)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGroupPCLQsByReplica,
			component.OperationSync,
			fmt.Sprintf("Error grouping standalone PodCliques by replica index for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	return standalonePCLQsByReplica, nil
}

// getExistingPCSGsByReplica fetches all PodCliqueScalingGroups for the PCS and groups them by PCS replica index.
func (r _resource) getExistingPCSGsByReplica(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (map[int][]grovecorev1alpha1.PodCliqueScalingGroup, error) {
	existingPCSGs, err := componentutils.GetPCSGsForPCS(ctx, r.client, client.ObjectKeyFromObject(pcs))
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPCSGs,
			component.OperationSync,
			fmt.Sprintf("Error listing PodCliqueScalingGroups for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	pcsgsByReplica, err := componentutils.GroupPCSGsByPCSReplicaIndex(existingPCSGs)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeGroupPCSGsByReplica,
			component.OperationSync,
			fmt.Sprintf("Error grouping PodCliqueScalingGroups by replica index for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	return pcsgsByReplica, nil
}

// getExistingPGMByReplica fetches all PodGangMaps for the PCS and groups them by PCS replica index.
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
			fmt.Sprintf("Error grouping PodGangMap by replica index for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	return pgmByReplica, nil
}

// getExistingPodGangsByReplica fetches the epoch-scheme PodGangs for the PCS and groups them by PCS
// replica index. Only PodGangs carrying the grove.io/epoch label are considered. Legacy PodGangs
// created before the epoch scheme carry no epoch and are not a reconstruction source, so they are
// skipped and are replaced by the PodGang component during migration.
func (r _resource) getExistingPodGangsByReplica(ctx context.Context, pcs *grovecorev1alpha1.PodCliqueSet) (map[int][]groveschedulerv1alpha1.PodGang, error) {
	existingPodGangs, err := componentutils.GetExistingPodGangs(ctx, r.client, pcs.ObjectMeta, pcs.Namespace)
	if err != nil {
		return nil, groveerr.WrapError(err,
			errCodeListPodGangs,
			component.OperationSync,
			fmt.Sprintf("Error listing PodGangs for PodCliqueSet: %v", client.ObjectKeyFromObject(pcs)),
		)
	}
	podGangsByReplica := make(map[int][]groveschedulerv1alpha1.PodGang)
	for i := range existingPodGangs {
		labels := existingPodGangs[i].Labels
		if _, hasEpoch := labels[apicommon.LabelEpoch]; !hasEpoch {
			continue
		}
		// An epoch-scheme PodGang always carries the replica-index label, so a missing or malformed
		// value is a coding error.
		replicaIndexLabel, ok := labels[apicommon.LabelPodCliqueSetReplicaIndex]
		if !ok {
			return nil, groveerr.New(errCodeGroupPodGangsByReplica, component.OperationSync,
				fmt.Sprintf("PodGang %s has label %s but no %s", existingPodGangs[i].Name, apicommon.LabelEpoch, apicommon.LabelPodCliqueSetReplicaIndex))
		}
		pcsReplicaIndex, err := strconv.Atoi(replicaIndexLabel)
		if err != nil {
			return nil, groveerr.New(errCodeGroupPodGangsByReplica, component.OperationSync,
				fmt.Sprintf("%s label on PodGang %s is not a valid integer: %q", apicommon.LabelPodCliqueSetReplicaIndex, existingPodGangs[i].Name, replicaIndexLabel))
		}
		podGangsByReplica[pcsReplicaIndex] = append(podGangsByReplica[pcsReplicaIndex], existingPodGangs[i])
	}
	return podGangsByReplica, nil
}

// runSyncFlow reconciles the PodGangMap for every PCS replica, then deletes PodGangMaps orphaned by a
// PCS replica scale-in. Each replica is in one of three states.
//  1. No PodGangMap. Its entries are authored from the PCS spec, reusing the epoch its existing
//     PodGangs carry so a rebuilt PodGangMap does not strand pods.
//  2. A PodGangMap with no entries. This is a stale read, since a live PodGangMap always has an
//     anchor entry. The reconcile requeues.
//  3. A PodGangMap with entries. reconcileEntries re-authors them, advancing an under-update replica
//     to the current generation hash first.
func (r _resource) runSyncFlow(ctx context.Context, syncSnap *syncSnapshot) error {
	for pcsReplicaIndex := range int(syncSnap.pcs.Spec.Replicas) {
		pgm, pgmExists := syncSnap.existingPGMByReplica[pcsReplicaIndex]

		if pgmExists && len(pgm.Spec.Entries) == 0 {
			return groveerr.New(
				groveerr.ErrCodeRequeueAfter,
				component.OperationSync,
				fmt.Sprintf("PodGangMap for replica %d of PodCliqueSet %v has no entries, requeuing", pcsReplicaIndex, client.ObjectKeyFromObject(syncSnap.pcs)),
			)
		}

		var (
			entries []grovecorev1alpha1.PodGangEntry
			err     error
		)
		if !pgmExists {
			entries = buildBootstrapEntries(syncSnap.pcs, r.clk, syncSnap.existingPodGangsByReplica[pcsReplicaIndex])
		} else {
			// Deep-copy the existing entries so mutations here do not alias the snapshot's PodGangMap.
			entries = clonePodGangEntries(pgm.Spec.Entries)
			// A RollingRecreate preserves PodGangs and entries. An under-update replica only needs its
			// entries advanced to the current generation hash.
			if componentutils.IsPCSReplicaInCurrentlyUpdating(syncSnap.pcs, pcsReplicaIndex) {
				advanceEntriesGenerationHash(entries, *syncSnap.pcs.Status.CurrentGenerationHash)
			}
			scaleOutEpoch := strconv.FormatInt(r.clk.Now().UnixNano(), 10)
			entries, err = reconcileEntries(syncSnap.pcs, entries,
				syncSnap.existingStandalonePCLQsByReplica[pcsReplicaIndex],
				syncSnap.existingPCSGsByReplica[pcsReplicaIndex],
				pcsReplicaIndex, scaleOutEpoch)
			if err != nil {
				return err
			}
		}
		pgmName := apicommon.GeneratePodGangMapName(apicommon.ResourceNameReplica{Name: syncSnap.pcs.Name, Replica: pcsReplicaIndex})
		if err = r.createOrPatchPodGangMapSpec(ctx, syncSnap.pcs, pgmName, pcsReplicaIndex, entries); err != nil {
			return err
		}
	}
	return r.deleteOrphanedPodGangMaps(ctx, syncSnap)
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

// deleteOrphanedPodGangMaps deletes PodGangMaps whose replica index is at or beyond the current PCS
// replica count. PodGangMap is owner-referenced to the PCS, so a PCS replica scale-in does not
// garbage-collect them; this cleanup is explicit.
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
